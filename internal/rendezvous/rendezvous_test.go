package rendezvous_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/rendezvous"
)

// start opens a Rendezvous over the loopback stand-in for a Quick Tunnel and
// takes it down again when the test ends — the shape every later Session test
// will use.
func start(t *testing.T, signal http.Handler) *rendezvous.Rendezvous {
	t.Helper()
	r, err := rendezvous.Start(t.Context(), rendezvous.Options{
		Tunnel: rendezvous.Loopback{},
		Signal: signal,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return r
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

// Someone will paste an Invite into a browser. What they get has to tell them
// what to do about it, in text a browser will not try to render as anything
// else.
func TestBrowsingToTheRendezvousExplainsItself(t *testing.T) {
	r := start(t, nil)

	resp, body := get(t, r.Invite().URL+"/")

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / = %s, want 200", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("GET / Content-Type = %q, want text/plain", ct)
	}
	for _, want := range []string{"dcc", "Invite"} {
		if !strings.Contains(body, want) {
			t.Errorf("the hint %q does not mention %q", body, want)
		}
	}
	if strings.Contains(strings.ToLower(body), r.Invite().Password.String()) {
		t.Error("the hint served to a browser contains the Password")
	}
}

// Nothing else is served. A path we do not know about is not a Rendezvous
// path, and answering it invites someone to go looking for more.
func TestTheRendezvousServesNothingElse(t *testing.T) {
	r := start(t, nil)

	resp, _ := get(t, r.Invite().URL+"/anything-else")

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /anything-else = %s, want 404", resp.Status)
	}
}

// The Invite addresses the Signaling handler, and the Password stays out of
// everything that travels to it — the request line, the query and the headers
// all reach the Host, and an Invite pasted into a browser would send them.
func TestSignalingIsReachedWithoutThePassword(t *testing.T) {
	requests := make(chan *http.Request, 1)
	r := start(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- req
		w.WriteHeader(http.StatusOK)
	}))

	signal := r.Invite().SignalURL()
	resp, _ := get(t, strings.Replace(signal, "ws://", "http://", 1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %s, want the Signaling handler to answer", signal, resp.Status)
	}

	req := <-requests
	password := r.Invite().Password.String()
	sent := req.Method + " " + req.URL.RequestURI() + " " + req.Host
	for name, values := range req.Header {
		sent += " " + name + ": " + strings.Join(values, ",")
	}
	if strings.Contains(strings.ToLower(sent), password) {
		t.Errorf("the Password reached the Rendezvous in a request: %q", sent)
	}
}

// One Rendezvous, one Password: an Invite is minted per Rendezvous and never
// reused from a previous one.
func TestEachRendezvousMintsItsOwnPassword(t *testing.T) {
	first, second := start(t, nil), start(t, nil)

	if first.Invite().Password == second.Invite().Password {
		t.Error("two Rendezvous shared a Password")
	}
	if first.Invite().URL == second.Invite().URL {
		t.Error("two Rendezvous shared an address")
	}
	if _, err := rendezvous.ParseInvite(first.Invite().String()); err != nil {
		t.Errorf("the Invite a Rendezvous minted does not parse: %v", err)
	}
}

// Disconnecting has to leave nothing behind: no listener, no tunnel.
func TestStopLeavesNothingListening(t *testing.T) {
	stopped := make(chan context.Context, 1)
	r, err := rendezvous.Start(t.Context(), rendezvous.Options{
		Tunnel: &recordingTunnel{stopped: stopped},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	url := r.Invite().URL

	if err := r.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("Stop left the tunnel running")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Errorf("GET %s/ still answers after Stop", url)
	}

	// Stopping twice is what happens when a Session ends and the app then
	// quits; the second one is not an error.
	if err := r.Stop(t.Context()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// A tunnel that will not start is the common case — no cloudflared, no
// network, Cloudflare refusing — and it must not leave a listener behind
// waiting for a Peer that can never arrive.
func TestStartFailsWithoutLeavingTheListenerOpen(t *testing.T) {
	ports := make(chan string, 1)
	wanted := errors.New("no tunnel today")

	_, err := rendezvous.Start(t.Context(), rendezvous.Options{
		Tunnel: &recordingTunnel{addrs: ports, err: wanted},
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("Start error = %v, want it to carry %v", err, wanted)
	}

	addr := <-ports
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Errorf("the local listener at %s survived a failed Start", addr)
	}
}

// recordingTunnel is a Loopback that reports what it was asked to do.
type recordingTunnel struct {
	addrs   chan string
	stopped chan context.Context
	err     error
}

func (t *recordingTunnel) Start(ctx context.Context, addr string) (string, error) {
	if t.addrs != nil {
		t.addrs <- addr
	}
	if t.err != nil {
		return "", t.err
	}
	return (rendezvous.Loopback{}).Start(ctx, addr)
}

func (t *recordingTunnel) Stop(ctx context.Context) error {
	if t.stopped != nil {
		t.stopped <- ctx
	}
	return nil
}
