package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// readLimit bounds one WebSocket message on the relay path. The relay moves
// RFC 4571-framed ICE and DTLS packets and the Bridge's pipe buffers, all of
// which sit comfortably under this.
const readLimit = 64 * 1024

// maxConns caps how many relay connections may be live at once. A Session
// needs a handful — ICE probes plus the one that wins — and the cap is what
// keeps a leaked Rendezvous URL from tying up the Host.
const maxConns = 8

// dialTimeout bounds the Bridge's WebSocket dial to the Rendezvous. The ICE
// agent behind it has its own connectivity budget; a relay dial slower than
// this is not going to beat it.
const dialTimeout = 10 * time.Second

// virtualPort is the port the Listener claims to be on. Nothing dials it —
// connections arrive through the Rendezvous — but pion advertises it in the
// Host's passive TCP candidate, and the Peer rewrites that candidate to its
// Bridge before dialing anything.
const virtualPort = 4571

// Listener is the Host's side of the relay: a net.Listener whose connections
// are WebSockets accepted at the Rendezvous's PathRelay, each one wrapped to
// look like the TCP connection pion's TCPMux expects. The transport owns it
// once handed over — the TCPMux closing is what closes it.
type Listener struct {
	mu     sync.Mutex
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	active int
	// remotePort mints a distinct synthetic remote address per connection,
	// which is how the TCPMux — and ICE above it — tells them apart.
	remotePort int
}

// NewListener builds a Listener with no connections yet.
func NewListener() *Listener {
	return &Listener{
		conns:  make(chan net.Conn, maxConns),
		closed: make(chan struct{}),
	}
}

// ServeHTTP implements http.Handler at rendezvous.PathRelay: it upgrades the
// request to a WebSocket and queues it for Accept. The connection is
// unauthenticated on purpose — the first thing through a legitimate one is a
// STUN binding carrying the Session's ICE credentials, and the TCPMux drops
// anything that fails that within its timeout.
func (l *Listener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !l.reserve() {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		l.release()
		return
	}
	ws.SetReadLimit(readLimit)
	c := &conn{
		Conn:    websocket.NetConn(context.Background(), ws, websocket.MessageBinary),
		release: func() { l.release() },
		local:   l.Addr(),
		remote:  l.nextRemote(),
	}
	select {
	case l.conns <- c:
	case <-l.closed:
		_ = c.Close()
	}
}

// Accept implements net.Listener for the TCPMux's accept loop.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close implements net.Listener. Connections already accepted belong to the
// TCPMux; the queued ones are closed here. Closing twice is fine.
func (l *Listener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		for {
			select {
			case c := <-l.conns:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

// Addr implements net.Listener. The address is loopback at virtualPort: it
// names the relay in the Host's candidate, and the loopback IP is what lets
// pion gather that candidate at all.
func (l *Listener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: virtualPort}
}

// reserve claims one of the maxConns connection slots.
func (l *Listener) reserve() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= maxConns {
		return false
	}
	l.active++
	return true
}

// release returns a slot once its connection is done.
func (l *Listener) release() {
	l.mu.Lock()
	l.active--
	l.mu.Unlock()
}

// nextRemote mints the synthetic remote address for one relayed connection.
func (l *Listener) nextRemote() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remotePort = l.remotePort%65535 + 1
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.remotePort}
}

// conn dresses a WebSocket-backed net.Conn in the TCP addresses the TCPMux
// requires: the Listener's own address locally — that is how the mux finds
// the right packet conn — and a synthetic, unique remote.
type conn struct {
	net.Conn
	release func()
	once    sync.Once
	local   net.Addr
	remote  net.Addr
}

// LocalAddr reports the Listener's address for every relayed connection.
func (c *conn) LocalAddr() net.Addr { return c.local }

// RemoteAddr reports this connection's synthetic remote address.
func (c *conn) RemoteAddr() net.Addr { return c.remote }

// Close releases the connection's slot along with the WebSocket underneath.
func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// Bridge is the Peer's side of the relay: a loopback listener that answers
// the ICE agent's active TCP dials by splicing each one onto a fresh
// WebSocket to the Rendezvous. The Peer rewrites the Host's passive TCP
// candidate to the Bridge's address, so dialing "the Host's relay port" is
// dialing here.
type Bridge struct {
	url      string
	listener net.Listener
}

// Open starts a Bridge to the relay at url — ws(s):// or http(s)://, as
// coder/websocket takes either.
func Open(url string) (*Bridge, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("relay: listening for the bridge: %w", err)
	}
	b := &Bridge{url: url, listener: listener}
	go b.serve()
	return b, nil
}

// Addr reports the host:port the Bridge answers on — what the Host's passive
// TCP candidate is rewritten to.
func (b *Bridge) Addr() string { return b.listener.Addr().String() }

// Close stops answering. Connections already spliced run until either side
// hangs up, which the transport dying does. Closing twice is fine.
func (b *Bridge) Close() error {
	err := b.listener.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// serve splices each accepted connection onto its own WebSocket.
func (b *Bridge) serve() {
	for {
		c, err := b.listener.Accept()
		if err != nil {
			return
		}
		go b.splice(c)
	}
}

// splice carries one connection's bytes across the relay, both ways, until
// either side ends it.
func (b *Bridge) splice(c net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	ws, _, err := websocket.Dial(ctx, b.url, nil)
	cancel()
	if err != nil {
		_ = c.Close()
		return
	}
	ws.SetReadLimit(readLimit)
	remote := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)

	done := make(chan struct{}, 2)
	pump := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pump(remote, c)
	go pump(c, remote)
	<-done
	_ = c.Close()
	_ = remote.Close()
	<-done
}
