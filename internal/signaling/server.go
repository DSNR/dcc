package signaling

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/wire"
)

// Server answers the Signaling WebSocket at the Rendezvous's /v1/signal and
// runs the Noise handshake as the responder. Every connection that fails the
// handshake — wrong Password included — is closed without a word: a guesser
// holding only the URL gets nothing to learn from, not even a reason.
//
// A connection that authenticates is handed to the accept callback. What
// happens to it then — first Peer, reconnect, a locked Invite's rejection —
// is Session policy, decided above this package.
type Server struct {
	identity identity.Identity
	password rendezvous.Password
	hello    wire.HostHello
	accept   func(*Conn, wire.PeerHello)

	mu          sync.Mutex
	handshaking int
}

// NewServer builds the handler for one Rendezvous. hello is sent verbatim in
// Noise message 2 to every connection that proves the Password: the Session
// is minted once, whoever completes a handshake at its Invite.
func NewServer(id identity.Identity, password rendezvous.Password, hello wire.HostHello, accept func(*Conn, wire.PeerHello)) *Server {
	return &Server{identity: id, password: password, hello: hello, accept: accept}
}

// ServeHTTP implements http.Handler at rendezvous.PathSignal.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The slot is reserved before the upgrade, so the cap can never be
	// overshot by connections racing through the handshake's first bytes.
	if !s.reserve() {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)

	ctx, cancel := context.WithTimeout(r.Context(), HandshakeTimeout)
	defer cancel()

	conn, hello, err := s.respond(ctx, ws)
	if err != nil {
		// Silent close: no close frame, no reason. A wrong Password and a
		// malformed handshake look identical from outside.
		_ = ws.CloseNow()
		return
	}
	s.accept(conn, hello)
}

// reserve claims one of the MaxUnauthenticated handshake slots.
func (s *Server) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handshaking >= MaxUnauthenticated {
		return false
	}
	s.handshaking++
	return true
}

// release returns a slot once the handshake has succeeded or died.
func (s *Server) release() {
	s.mu.Lock()
	s.handshaking--
	s.mu.Unlock()
}

// respond runs the responder's side of the handshake over an open WebSocket.
func (s *Server) respond(ctx context.Context, ws *websocket.Conn) (*Conn, wire.PeerHello, error) {
	hs, err := newHandshakeState(s.identity, s.password, false)
	if err != nil {
		return nil, wire.PeerHello{}, err
	}

	// Message 1: this read failing its AEAD check is the wrong Password.
	_, msg1, err := ws.Read(ctx)
	if err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: waiting for handshake message 1: %w", err)
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: reading handshake message 1: %w", err)
	}

	// Message 2: our keys and the hello that mints the Session.
	payload, err := wire.EncodeHostHello(s.hello)
	if err != nil {
		return nil, wire.PeerHello{}, err
	}
	msg2, _, _, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: writing handshake message 2: %w", err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, msg2); err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: sending handshake message 2: %w", err)
	}

	// Message 3: the Peer's static key and hello; the transport keys fall out.
	_, msg3, err := ws.Read(ctx)
	if err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: waiting for handshake message 3: %w", err)
	}
	payload3, recvCS, sendCS, err := hs.ReadMessage(nil, msg3)
	if err != nil {
		return nil, wire.PeerHello{}, fmt.Errorf("signaling: reading handshake message 3: %w", err)
	}
	hello, err := wire.DecodePeerHello(payload3)
	if err != nil {
		return nil, wire.PeerHello{}, err
	}

	peer, err := peerKey(hs)
	if err != nil {
		return nil, wire.PeerHello{}, err
	}
	return newConn(ws, sendCS, recvCS, peer), hello, nil
}
