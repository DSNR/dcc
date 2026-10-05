package rendezvous

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrNotInstalled reports that cloudflared is not on this machine. dcc neither
// bundles nor downloads it, so this is a dead end until someone installs it —
// which is why the error wrapping this one says how.
var ErrNotInstalled = errors.New("cloudflared is not installed")

// Defaults for Cloudflared. A Quick Tunnel takes about eight seconds to come
// up, nearly all of it waiting on Cloudflare's provisioning API, which has a
// fifteen-second timeout of its own; thirty seconds is comfortably past the
// worst case and still short enough that a Host who is going nowhere finds out
// quickly. The grace period is short because a Quick Tunnel holds no state
// worth draining.
const (
	defaultStartTimeout = 30 * time.Second
	defaultGracePeriod  = 5 * time.Second
	// killMargin is how much longer than the grace period a stop waits before
	// it stops asking and starts killing.
	killMargin = 2 * time.Second
	// pollInterval is how often the metrics server is asked what the logs
	// have not said yet.
	pollInterval = 250 * time.Millisecond
)

// quickTunnelURL matches the URL cloudflared logs. The hostname is four
// random words today, so the label is matched as opaque.
var quickTunnelURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// registered is the line cloudflared logs once an edge connection is up,
// which is the first moment the URL it printed earlier actually resolves to
// anything.
const registered = "Registered tunnel connection"

// Cloudflared is the Tunnel that runs the real thing: a Cloudflare Quick
// Tunnel, no account, no configuration, a fresh hostname every time.
//
// The process is a child of this one and is not meant to outlive it. Start
// takes it down again if the tunnel never becomes usable; Stop takes it down
// when the Session ends; and on Linux the kernel takes it down if dcc dies
// without getting the chance.
type Cloudflared struct {
	// Path is the cloudflared binary. Empty means look for it on PATH, which
	// is where every documented way of installing it puts it.
	Path string
	// StartTimeout bounds how long Start waits for a usable tunnel. Zero
	// means defaultStartTimeout.
	StartTimeout time.Duration
	// GracePeriod is how long cloudflared is given to shut down before it is
	// killed, and what it is told to allow itself. Zero means
	// defaultGracePeriod.
	GracePeriod time.Duration

	mu      sync.Mutex
	cmd     *exec.Cmd
	stderr  *os.File
	stopped chan struct{} // closed once the process has been reaped
	waitErr error         // valid once stopped is closed
}

// Start launches cloudflared pointed at addr and returns the Quick Tunnel's
// URL once the tunnel is registered with Cloudflare's edge — not when the URL
// is merely printed, which happens before any edge connection is attempted
// and would hand out an Invite that does not yet work.
func (c *Cloudflared) Start(ctx context.Context, addr string) (string, error) {
	binary, err := c.binary()
	if err != nil {
		return "", err
	}
	metrics, err := freeAddr()
	if err != nil {
		return "", err
	}

	// cloudflared writes every line of its log to stderr and nothing to
	// stdout. An *os.File is handed straight to the child, so waiting on the
	// process never waits on this end of the pipe.
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("rendezvous: opening a pipe for cloudflared's log: %w", err)
	}

	cmd := exec.Command(binary, "tunnel",
		"--url", "http://"+addr,
		"--no-autoupdate",
		"--output", "json",
		"--metrics", metrics,
		// Quick Tunnels pin QUIC unless a protocol is named, and a network
		// that drops outbound UDP would then never connect at all.
		"--protocol", "auto",
		"--grace-period", c.gracePeriod().String(),
	)
	cmd.Stderr = writer
	configureProcess(cmd)

	if err := cmd.Start(); err != nil {
		writer.Close()
		reader.Close()
		return "", fmt.Errorf("rendezvous: starting %s: %w", binary, err)
	}
	// The child holds the only write end now, so the scan below sees EOF
	// exactly when cloudflared exits.
	writer.Close()

	logs := newLogScan()
	go logs.run(reader)

	stopped := make(chan struct{})
	c.mu.Lock()
	c.cmd, c.stderr, c.stopped = cmd, reader, stopped
	c.mu.Unlock()
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.waitErr = err
		c.mu.Unlock()
		close(stopped)
	}()

	url, err := c.await(ctx, metrics, logs, stopped)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), c.gracePeriod()+killMargin)
		defer cancel()
		_ = c.Stop(stopCtx)
		return "", err
	}
	return url, nil
}

// await waits for both halves of a usable tunnel: the URL, and an edge
// connection to serve it. Each can come from the log or from the metrics
// server, because cloudflared's log format is not a contract and its
// endpoints are not documented, and between them one of the two will answer.
func (c *Cloudflared) await(ctx context.Context, metrics string, logs *logScan, stopped <-chan struct{}) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.startTimeout())
	defer cancel()

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	urls, ready := logs.urls, logs.ready
	var url string
	for url == "" || ready != nil {
		select {
		case url = <-urls:
			urls = nil
		case <-ready:
			ready = nil
		case <-poll.C:
			if url == "" {
				url = metricsHostname(ctx, metrics)
			}
			if ready != nil && metricsReady(ctx, metrics) {
				ready = nil
			}
		case <-stopped:
			return "", c.exitError(logs)
		case <-ctx.Done():
			return "", fmt.Errorf("rendezvous: cloudflared did not have the Rendezvous ready within %s%s",
				c.startTimeout(), logs.lastSaid())
		}
	}
	return url, nil
}

// exitError explains a cloudflared that quit on its own. Its last line is the
// only account anyone gets of a DNS failure, a blocked port or Cloudflare
// refusing to provision another tunnel.
func (c *Cloudflared) exitError(logs *logScan) error {
	// The log pipe reaches EOF when the process dies, but a moment later than
	// the wait does; the last line is worth that moment.
	select {
	case <-logs.done:
	case <-time.After(time.Second):
	}
	c.mu.Lock()
	waitErr := c.waitErr
	c.mu.Unlock()
	return fmt.Errorf("rendezvous: cloudflared exited before the Rendezvous was ready (%v)%s",
		waitErr, logs.lastSaid())
}

// Stop shuts cloudflared down, gracefully if it will go and by force if it
// will not, and returns once the process is gone. Stopping a tunnel that was
// never started, or stopping one twice, does nothing.
//
// A non-zero exit from a process we just asked to quit is not reported: we
// asked for this, and there is nothing left for a caller to do about it.
func (c *Cloudflared) Stop(ctx context.Context) error {
	c.mu.Lock()
	cmd, stopped, stderr := c.cmd, c.stopped, c.stderr
	c.cmd, c.stopped, c.stderr = nil, nil, nil
	c.mu.Unlock()
	if cmd == nil {
		return nil
	}
	defer stderr.Close()

	if err := terminate(cmd.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		// Asking politely failed, so there is nothing to wait for.
		_ = cmd.Process.Kill()
	}
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
	case <-time.After(c.gracePeriod() + killMargin):
	}

	_ = cmd.Process.Kill()
	<-stopped
	return nil
}

// binary reports the cloudflared to run, or says how to get one.
func (c *Cloudflared) binary() (string, error) {
	if c.Path != "" {
		return c.Path, nil
	}
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return "", fmt.Errorf("rendezvous: %w, and dcc needs it to open a Rendezvous. %s",
			ErrNotInstalled, installHint())
	}
	return path, nil
}

func (c *Cloudflared) startTimeout() time.Duration {
	if c.StartTimeout > 0 {
		return c.StartTimeout
	}
	return defaultStartTimeout
}

func (c *Cloudflared) gracePeriod() time.Duration {
	if c.GracePeriod > 0 {
		return c.GracePeriod
	}
	return defaultGracePeriod
}

// installHint is the whole of dcc's answer to a missing cloudflared: dcc will
// not bundle it and will not download it, so the only useful thing to say is
// where it comes from.
func installHint() string {
	const downloads = "https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/"
	if runtime.GOOS == "windows" {
		return "Install it with `winget install --id Cloudflare.cloudflared`, " +
			"or download cloudflared.exe from " + downloads + " and put it on your PATH."
	}
	return "Install it from Cloudflare's package repository at https://pkg.cloudflare.com, " +
		"or download the binary from " + downloads + " and put it on your PATH."
}

// logScan reads cloudflared's log, announcing the two things Start is waiting
// for and remembering the last thing said in case neither ever arrives.
type logScan struct {
	urls  chan string
	ready chan struct{}
	done  chan struct{}

	mu   sync.Mutex
	last string
}

func newLogScan() *logScan {
	return &logScan{
		urls:  make(chan string, 1),
		ready: make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// run scans until the log ends, which is when cloudflared exits. It keeps
// reading after Start is satisfied: a full pipe would block the tunnel.
func (s *logScan) run(r io.Reader) {
	defer close(s.done)

	var readyOnce, urlOnce sync.Once
	scanner := bufio.NewScanner(r)
	// Log lines are short, but a stack trace on the way out is not.
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		message := logMessage(scanner.Text())
		if message == "" {
			continue
		}
		s.mu.Lock()
		s.last = message
		s.mu.Unlock()

		if url := quickTunnelURL.FindString(message); url != "" {
			urlOnce.Do(func() { s.urls <- url })
		}
		if strings.Contains(message, registered) {
			readyOnce.Do(func() { close(s.ready) })
		}
	}
}

// lastSaid renders the last log line for an error message, or nothing at all
// if cloudflared never said anything.
func (s *logScan) lastSaid() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == "" {
		return ""
	}
	return "; cloudflared last said: " + s.last
}

// logMessage pulls the human part out of a line of --output json. Errors from
// cloudflared's own command-line layer are printed bare, with no JSON around
// them, and those are exactly the lines worth keeping — so anything that does
// not parse is taken as the message itself.
func logMessage(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	var entry struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.Message != "" {
		return entry.Message
	}
	return line
}

// metricsHostname asks cloudflared's metrics server for the hostname it was
// given, the documented-in-source fallback for a log line that changed shape.
func metricsHostname(ctx context.Context, metrics string) string {
	var body struct {
		Hostname string `json:"hostname"`
	}
	if !getJSON(ctx, "http://"+metrics+"/quicktunnel", &body) || body.Hostname == "" {
		return ""
	}
	return "https://" + body.Hostname
}

// metricsReady reports whether the tunnel has an edge connection.
func metricsReady(ctx context.Context, metrics string) bool {
	var body struct {
		ReadyConnections int `json:"readyConnections"`
	}
	return getJSON(ctx, "http://"+metrics+"/ready", &body) && body.ReadyConnections > 0
}

// getJSON fetches a metrics endpoint, reporting whether it answered with
// something usable. Every failure means the same thing — ask again shortly —
// so none of them are distinguished.
func getJSON(ctx context.Context, url string, into any) bool {
	ctx, cancel := context.WithTimeout(ctx, pollInterval)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(into) == nil
}

// freeAddr picks a loopback address for cloudflared's metrics server by
// binding one and letting it go. Something else could take the port in
// between; the window is tiny and the cost is a Rendezvous that fails to
// start and is started again.
func freeAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("rendezvous: finding a port for cloudflared's metrics: %w", err)
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}
