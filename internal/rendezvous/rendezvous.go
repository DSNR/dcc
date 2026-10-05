package rendezvous

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// PathSignal is where the Signaling WebSocket lives. Everything the two sides
// say to each other before they are connected directly goes through this one
// path; the Rendezvous serves nothing else that matters.
const PathSignal = "/v1/signal"

// PathRelay is where the fallback relay's WebSockets arrive when direct
// connection fails. Each carries only DTLS ciphertext — the Rendezvous
// forwards bytes it cannot read.
const PathRelay = "/v1/relay"

// hint is what a browser gets at /. Someone who was handed an Invite and
// opened it the obvious way is not lost, only in the wrong program, and this
// is the only chance to tell them so.
const hint = "dcc Rendezvous. Open this Invite in the dcc app.\n"

// headerTimeout bounds how long an unfinished request may hold a connection
// open before it has even said what it wants. It applies to the WebSocket's
// upgrade request too, which arrives all at once; the long-lived socket that
// follows is hijacked and outside the server's timeouts entirely.
const headerTimeout = 10 * time.Second

// Tunnel publishes a local listener at a public address. It is the seam the
// tests replace: a real one runs cloudflared and hands back a
// trycloudflare.com URL, while Loopback hands back the local address itself
// and the whole Session runs without a Cloudflare account, a network or a
// second process.
type Tunnel interface {
	// Start publishes the local address addr and returns the base URL —
	// scheme and host, no path — that reaches it. It returns only once
	// requests to that URL will actually arrive, so that an Invite is never
	// handed out before it works.
	Start(ctx context.Context, addr string) (string, error)
	// Stop tears the tunnel down, leaving no process behind it.
	Stop(ctx context.Context) error
}

// Options configures a Rendezvous.
type Options struct {
	// Tunnel publishes the Rendezvous. Nil means a real cloudflared Quick
	// Tunnel.
	Tunnel Tunnel
	// Signal handles the Signaling WebSocket at PathSignal. Nil serves
	// nothing there, which is only useful in tests of the Rendezvous itself.
	Signal http.Handler
	// Relay handles the fallback relay's WebSockets at PathRelay. Nil serves
	// nothing there, and the Session simply has no relay to fall back on.
	Relay http.Handler
}

// Rendezvous is a running Rendezvous: a local HTTP server, the tunnel
// publishing it, and the Invite that names both.
type Rendezvous struct {
	invite Invite
	addr   string
	tunnel Tunnel
	server *http.Server

	mu      sync.Mutex
	stopped bool
}

// Start opens a Rendezvous: it mints a Password, listens on loopback, and
// publishes that listener through the Tunnel. It returns once the Rendezvous
// is reachable, so the Invite it carries is good the moment the Host sees it.
//
// ctx bounds starting up, not the Rendezvous's life: cancelling it after Start
// has returned does nothing, and Stop is what takes the Rendezvous down.
func Start(ctx context.Context, opts Options) (*Rendezvous, error) {
	password, err := NewPassword()
	if err != nil {
		return nil, err
	}
	tunnel := opts.Tunnel
	if tunnel == nil {
		tunnel = &Cloudflared{}
	}

	// Loopback only: everything from outside arrives through the tunnel, so
	// the Rendezvous never needs a port open on the machine itself.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("rendezvous: listening on loopback: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", serveHint)
	if opts.Signal != nil {
		mux.Handle(PathSignal, opts.Signal)
	}
	if opts.Relay != nil {
		mux.Handle(PathRelay, opts.Relay)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: headerTimeout}
	go func() {
		// The only interesting failure here is the listener closing, which is
		// Stop doing its job.
		_ = server.Serve(listener)
	}()

	addr := listener.Addr().String()
	url, err := tunnel.Start(ctx, addr)
	if err != nil {
		_ = server.Close()
		return nil, err
	}
	return &Rendezvous{
		invite: Invite{URL: url, Password: password},
		addr:   addr,
		tunnel: tunnel,
		server: server,
	}, nil
}

// Invite reports the Invite for this Rendezvous — the one string the Host
// hands over. It is valid for as long as the Rendezvous is up and no longer.
func (r *Rendezvous) Invite() Invite { return r.invite }

// Addr reports the loopback address the Rendezvous is served on, which is
// what the tunnel was pointed at.
func (r *Rendezvous) Addr() string { return r.addr }

// Stop takes the Rendezvous down: the local server first, then the tunnel.
// Calling it twice is not an error — a Session that ends and an app that then
// quits both want the same thing.
//
// The server is closed rather than drained. The only long-lived connection it
// has is the hijacked Signaling WebSocket, which Shutdown would not wait for
// anyway, and by the time anything calls Stop the Session is over.
func (r *Rendezvous) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return nil
	}
	r.stopped = true

	serverErr := r.server.Close()
	if errors.Is(serverErr, http.ErrServerClosed) {
		serverErr = nil
	}
	return errors.Join(serverErr, r.tunnel.Stop(ctx))
}

// serveHint answers a browser at /.
func serveHint(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, hint)
}

// Loopback is a Tunnel that publishes nothing: it hands back the local
// address it was given. It is how the Rendezvous is faked in tests — a real
// listener, a real Invite and a real WebSocket, with cloudflared and the
// internet left out.
type Loopback struct{}

// Start reports the local address as the Rendezvous's own.
func (Loopback) Start(_ context.Context, addr string) (string, error) {
	return "http://" + addr, nil
}

// Stop has nothing to stop.
func (Loopback) Stop(context.Context) error { return nil }
