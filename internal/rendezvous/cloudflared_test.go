package rendezvous_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/rendezvous"
)

// The tests here drive the real process-handling code against a fake
// cloudflared, which is this test binary re-executed: TestMain hands over to
// fakeCloudflared when the environment names a scenario. That keeps the
// launch, the stderr scraping, the readiness wait and the shutdown under test
// while needing neither cloudflared nor a Cloudflare account. Real tunnels are
// verified by hand.
const (
	fakeScenarioEnv = "DCC_TEST_CLOUDFLARED"
	fakeStateEnv    = "DCC_TEST_CLOUDFLARED_STATE"
	fakeHost        = "https://dcc-test-tunnel.trycloudflare.com"
)

func TestMain(m *testing.M) {
	if scenario := os.Getenv(fakeScenarioEnv); scenario != "" {
		os.Exit(fakeCloudflared(scenario))
	}
	os.Exit(m.Run())
}

// fakeState is what the fake records about itself for the test that spawned
// it: how it was invoked, and where its metrics server is — a port that stops
// answering the moment the process is gone.
type fakeState struct {
	Metrics string   `json:"metrics"`
	Args    []string `json:"args"`
}

// launch starts a Cloudflared pointed at the fake, in the named scenario.
func launch(t *testing.T, scenario string, tunnel *rendezvous.Cloudflared) (string, error) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state.json")
	t.Setenv(fakeScenarioEnv, scenario)
	t.Setenv(fakeStateEnv, state)
	tunnel.Path = os.Args[0]
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := tunnel.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	url, err := tunnel.Start(t.Context(), "127.0.0.1:9999")
	return url, err
}

// readState reports what the fake recorded, waiting for it to appear.
func readState(t *testing.T) fakeState {
	t.Helper()
	path := os.Getenv(fakeStateEnv)
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			var state fakeState
			if err := json.Unmarshal(b, &state); err == nil {
				return state
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake cloudflared never wrote %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertGone waits for a stopped cloudflared's metrics port to stop
// answering, which it can only do by no longer existing.
func assertGone(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("cloudflared is still listening on %s after being stopped", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The URL is logged before the tunnel is connected to anything, so seeing it
// is not enough: an Invite handed out then would fail for the Peer and look
// like a wrong Password.
func TestStartWaitsForTheTunnelToRegister(t *testing.T) {
	// A grace period long enough that waiting it out is unmistakable: this
	// cloudflared goes when it is asked, or the test notices.
	tunnel := rendezvous.Cloudflared{GracePeriod: 30 * time.Second}

	url, err := launch(t, "ready", &tunnel)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if url != fakeHost {
		t.Errorf("Start returned %q, want the URL cloudflared logged (%q)", url, fakeHost)
	}

	// The tunnel must point at the Rendezvous's own listener, and must be
	// told to log in the form the scraper reads.
	state := readState(t)
	args := strings.Join(state.Args, " ")
	for _, want := range []string{"tunnel", "--url http://127.0.0.1:9999", "--output json", "--metrics ", "--no-autoupdate"} {
		if !strings.Contains(args, want) {
			t.Errorf("cloudflared was run as %q, without %q", args, want)
		}
	}

	// A cloudflared that goes when it is asked goes at once, and is gone for
	// good: waiting out the grace period would mean the signal never landed.
	began := time.Now()
	if err := tunnel.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("stopping a willing cloudflared took %s, so it was killed rather than asked", took)
	}
	assertGone(t, state.Metrics)
}

// Nothing rules out cloudflared changing how it logs. When the scrape comes up
// empty the metrics server still knows both answers.
func TestStartFallsBackToTheMetricsEndpoints(t *testing.T) {
	var tunnel rendezvous.Cloudflared

	url, err := launch(t, "metrics", &tunnel)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if url != fakeHost {
		t.Errorf("Start returned %q, want %q", url, fakeHost)
	}
}

// When cloudflared gives up, its last word is the only explanation anyone
// has — a rate limit, a DNS failure, a firewall — so it has to reach the Host.
func TestStartSaysWhyCloudflaredExited(t *testing.T) {
	var tunnel rendezvous.Cloudflared

	_, err := launch(t, "fail", &tunnel)

	if err == nil {
		t.Fatal("Start succeeded against a cloudflared that exited")
	}
	if !strings.Contains(err.Error(), "failed to request quick Tunnel") {
		t.Errorf("Start error %q does not carry what cloudflared said", err)
	}
}

// A tunnel that prints a URL and never connects would otherwise hang the Host
// forever. Giving up has to take the process with it.
func TestStartGivesUpWhenTheTunnelNeverRegisters(t *testing.T) {
	tunnel := rendezvous.Cloudflared{StartTimeout: 500 * time.Millisecond}

	_, err := launch(t, "silent", &tunnel)

	if err == nil {
		t.Fatal("Start succeeded against a tunnel that never registered")
	}
	if !strings.Contains(err.Error(), "500ms") {
		t.Errorf("Start error %q does not say how long it waited", err)
	}
	assertGone(t, readState(t).Metrics)
}

// Stopping is graceful first, but a cloudflared that will not go must not
// outlive the app that started it.
func TestStopKillsACloudflaredThatIgnoresTheSignal(t *testing.T) {
	tunnel := rendezvous.Cloudflared{GracePeriod: 100 * time.Millisecond}

	if _, err := launch(t, "stubborn", &tunnel); err != nil {
		t.Fatalf("Start: %v", err)
	}
	metrics := readState(t).Metrics

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := tunnel.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertGone(t, metrics)
}

// Stopping a tunnel that was never started is what an app does when Start
// failed for some other reason.
func TestStoppingAnUnstartedTunnelIsNotAnError(t *testing.T) {
	var tunnel rendezvous.Cloudflared

	if err := tunnel.Stop(t.Context()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}

// dcc does not bundle cloudflared and will not download it, so the one thing
// it owes someone without it is how to get it.
func TestMissingCloudflaredSaysHowToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var tunnel rendezvous.Cloudflared

	_, err := tunnel.Start(t.Context(), "127.0.0.1:9999")

	if err == nil {
		t.Fatal("Start succeeded without cloudflared installed")
	}
	if !errors.Is(err, rendezvous.ErrNotInstalled) {
		t.Errorf("Start error %v is not ErrNotInstalled", err)
	}
	message := err.Error()
	for _, want := range []string{"cloudflared", "install", "https://"} {
		if !strings.Contains(strings.ToLower(message), want) {
			t.Errorf("the error %q does not mention %q", message, want)
		}
	}
}

// fakeCloudflared is this binary standing in for cloudflared. It reproduces
// the parts of the real one's behaviour the driver depends on: JSON log lines
// on stderr, the URL inside an ASCII box, the "Registered tunnel connection"
// line, the metrics endpoints, and a graceful exit on a signal.
func fakeCloudflared(scenario string) int {
	// Registered before anything else: the stubborn scenario must survive the
	// signal, and the others must not race it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	if scenario == "stubborn" {
		signal.Reset(os.Interrupt, syscall.SIGTERM)
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
	}

	args := os.Args[1:]
	metrics := flagValue(args, "--metrics")

	if scenario == "fail" {
		// urfave/cli prints an action's error bare, with no JSON and no level.
		fmt.Fprintln(os.Stderr, `failed to request quick Tunnel: Post "https://api.trycloudflare.com/tunnel": dial tcp: lookup api.trycloudflare.com: no such host`)
		return 1
	}

	listener, err := net.Listen("tcp", metrics)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake cloudflared: "+err.Error())
		return 1
	}
	defer listener.Close()
	go http.Serve(listener, metricsHandler(scenario))

	if err := writeState(fakeState{Metrics: listener.Addr().String(), Args: args}); err != nil {
		fmt.Fprintln(os.Stderr, "fake cloudflared: "+err.Error())
		return 1
	}

	if scenario != "metrics" {
		logJSON("Requesting new quick Tunnel on trycloudflare.com...")
		logJSON("|  " + fakeHost + "                    |")
	}
	if scenario == "ready" || scenario == "stubborn" {
		logJSON("Registered tunnel connection")
	}

	<-signals
	logJSON("Initiating graceful shutdown due to signal terminated ...")
	return 0
}

func metricsHandler(scenario string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/quicktunnel", func(w http.ResponseWriter, _ *http.Request) {
		if scenario == "silent" {
			http.Error(w, "not yet", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"hostname":%q}`, strings.TrimPrefix(fakeHost, "https://"))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if scenario == "silent" {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"status":503,"readyConnections":0}`)
			return
		}
		fmt.Fprint(w, `{"status":200,"readyConnections":1,"connectorId":"3782d772-0000-0000-0000-000000000000"}`)
	})
	return mux
}

// logJSON writes one line in cloudflared's --output json form.
func logJSON(message string) {
	line, _ := json.Marshal(map[string]any{
		"level":   "info",
		"message": message,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
	fmt.Fprintln(os.Stderr, string(line))
}

func writeState(state fakeState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(os.Getenv(fakeStateEnv), b, 0o600)
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
