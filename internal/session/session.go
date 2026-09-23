package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/signaling"
	"github.com/DSNR/dcc/internal/wire"
)

// teardownWait bounds how long ending a Session may spend closing what it
// owns — chiefly waiting for cloudflared to die.
const teardownWait = 10 * time.Second

// Options configures a Session.
type Options struct {
	// Identity is this install's persistent keypair.
	Identity identity.Identity
	// Name is the Display Name announced to the other side.
	Name string
	// DTLS is this side's certificate fingerprint, bound into the handshake.
	// It is injected until the WebRTC work mints certificates itself.
	DTLS string
	// Tunnel publishes the Rendezvous when Hosting. Nil means a real
	// cloudflared Quick Tunnel; tests pass rendezvous.Loopback.
	Tunnel rendezvous.Tunnel
	// Pins remembers accepted Identities across Sessions. Nil means a fresh
	// in-memory store, which recognises no one.
	Pins Pins
}

// Session is one Session from Invite to disconnect, driven entirely through
// commands — the exported methods — and observed entirely through Events.
// Both UIs are thin consumers of exactly this API.
type Session struct {
	identity identity.Identity
	name     string
	dtls     string
	tunnel   rendezvous.Tunnel
	pins     Pins
	events   *eventQueue

	mu       sync.Mutex
	state    State
	starting bool
	closed   bool
	rdv      *rendezvous.Rendezvous
	signal   *signaling.Server
	conn     *signaling.Conn
	peer     identity.PublicKey
	peerName string
	locked   bool
}

// New builds an Idle Session. The name and fingerprint are checked here, so
// a Session that would fail every handshake is refused before it starts.
func New(opts Options) (*Session, error) {
	if _, err := wire.EncodePeerHello(wire.PeerHello{Name: opts.Name, DTLS: opts.DTLS}); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	pins := opts.Pins
	if pins == nil {
		pins = NewMemoryPins()
	}
	return &Session{
		identity: opts.Identity,
		name:     opts.Name,
		dtls:     opts.DTLS,
		tunnel:   opts.Tunnel,
		pins:     pins,
		events:   newEventQueue(),
		state:    Idle,
	}, nil
}

// Events is the Session's one output. It must be drained; it closes when
// the Session is over for good.
func (s *Session) Events() <-chan Event { return s.events.out }

// Host opens a Rendezvous and starts waiting for a Peer: Idle to Hosting,
// with the Invite arriving as an InviteReady event. ctx bounds starting up —
// mostly cloudflared's tunnel coming alive — not the Session's life.
func (s *Session) Host(ctx context.Context) error {
	if err := s.begin(); err != nil {
		return err
	}

	sessionID, err := uuid.NewV7()
	if err != nil {
		s.abortStart()
		return fmt.Errorf("session: minting a session id: %w", err)
	}

	// The handler exists before the Rendezvous does, but the signaling
	// server needs the Password the Rendezvous mints — so the handler looks
	// the server up on every request and turns nothing-yet into a refusal.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		signal := s.signal
		s.mu.Unlock()
		if signal == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		signal.ServeHTTP(w, r)
	})

	rdv, err := rendezvous.Start(ctx, rendezvous.Options{Tunnel: s.tunnel, Signal: handler})
	if err != nil {
		s.abortStart()
		return err
	}

	hello := wire.HostHello{SessionID: sessionID.String(), Name: s.name, DTLS: s.dtls}

	s.mu.Lock()
	if s.closed {
		s.starting = false
		s.mu.Unlock()
		stopCtx, cancel := context.WithTimeout(context.Background(), teardownWait)
		defer cancel()
		_ = rdv.Stop(stopCtx)
		return errors.New("session: closed")
	}
	s.rdv = rdv
	s.signal = signaling.NewServer(s.identity, rdv.Invite().Password, hello, s.onPeer)
	s.starting = false
	s.setStateLocked(Hosting, ReasonNone)
	s.events.emit(InviteReady{Invite: rdv.Invite()})
	s.mu.Unlock()
	return nil
}

// Join connects to another person's Invite: Idle to Connecting at once, then
// Verifying or Failed as events. A string that is not an Invite is refused
// here, synchronously, with the Session still Idle.
func (s *Session) Join(ctx context.Context, invite string) error {
	parsed, err := rendezvous.ParseInvite(invite)
	if err != nil {
		return err
	}
	if err := s.begin(); err != nil {
		return err
	}

	s.mu.Lock()
	s.setStateLocked(Connecting, ReasonNone)
	s.mu.Unlock()

	go s.dial(ctx, parsed)
	return nil
}

// Accept resolves the standing Security Code prompt in the other person's
// favour: their Identity is pinned under the name they announced, and the
// Session moves on to Connected.
func (s *Session) Accept() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Verifying {
		return fmt.Errorf("session: no Security Code prompt to accept while %s", s.state)
	}
	s.pins.Pin(s.peerName, s.peer)
	s.setStateLocked(Connected, ReasonNone)
	return nil
}

// Refuse resolves the standing Security Code prompt against the other
// person, and that is the end of the Session: the connection closes, the
// Rendezvous comes down, and the events channel closes after Disconnected.
func (s *Session) Refuse() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Verifying {
		return fmt.Errorf("session: no Security Code prompt to refuse while %s", s.state)
	}
	s.endLocked(Disconnected, ReasonRefused)
	return nil
}

// Close ends the Session unconditionally and releases everything it holds.
// It is safe from any state and safe twice.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.teardownLocked()
	return nil
}

// begin claims the one Idle-to-active transition a Session ever makes.
func (s *Session) begin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("session: closed")
	}
	if s.state != Idle || s.starting {
		return fmt.Errorf("session: already %s; a Session runs once", s.state)
	}
	s.starting = true
	return nil
}

// abortStart returns a failed Host or Join to Idle.
func (s *Session) abortStart() {
	s.mu.Lock()
	s.starting = false
	s.mu.Unlock()
}

// dial is the Peer's connection attempt, running off Join's goroutine.
func (s *Session) dial(ctx context.Context, invite rendezvous.Invite) {
	conn, hostHello, err := signaling.Dial(ctx, invite.SignalURL(), signaling.DialOptions{
		Identity: s.identity,
		Password: invite.Password,
		Hello:    wire.PeerHello{Name: s.name, DTLS: s.dtls},
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	s.starting = false
	if s.closed {
		if err == nil {
			conn.Close()
		}
		return
	}
	if err != nil {
		// A wrong Password, a dead Rendezvous and a hostile listener all
		// land here, indistinguishable — which is the point.
		s.endLocked(Failed, ReasonHandshakeFailed)
		return
	}
	s.attachLocked(conn, hostHello.Name)
}

// onPeer receives every connection that authenticates at the Rendezvous.
// The first Identity through locks the Invite; a different Identity after
// that is told, under the transport keys, that the Invite is taken.
func (s *Session) onPeer(conn *signaling.Conn, hello wire.PeerHello) {
	s.mu.Lock()
	if s.closed || s.state != Hosting {
		if s.locked && s.peer != conn.Peer() && !s.closed {
			s.mu.Unlock()
			go rejectPeer(conn)
			return
		}
		// The locked Identity dialing again is a reconnect, which lands
		// with the Reconnecting work; until then the fresh connection is
		// simply declined.
		s.mu.Unlock()
		conn.Close()
		return
	}
	s.locked = true
	s.attachLocked(conn, hello.Name)
	s.mu.Unlock()
}

// rejectPeer tells a locked-out Identity the one thing it is owed —
// an encrypted rejected frame — and hangs up.
func rejectPeer(conn *signaling.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), teardownWait)
	defer cancel()
	_ = conn.Send(ctx, wire.Rejected{Reason: wire.ReasonLocked})
	conn.Close()
}

// attachLocked adopts an authenticated connection and raises the Security
// Code prompt. The Session holds in Verifying until Accept or Refuse.
func (s *Session) attachLocked(conn *signaling.Conn, name string) {
	peer := conn.Peer()
	s.conn = conn
	s.peer = peer
	s.peerName = name

	pinned, known := s.pins.Pinned(name)
	s.setStateLocked(Verifying, ReasonNone)
	s.events.emit(VerifyPrompt{
		Code:    s.identity.SecurityCode(peer),
		Name:    name,
		Peer:    peer,
		Changed: known && pinned != peer,
	})
	go s.readLoop(conn)
}

// readLoop watches an adopted connection. Until the Signaling work lands,
// its jobs are noticing the encrypted rejection and noticing death.
func (s *Session) readLoop(conn *signaling.Conn) {
	for {
		f, err := conn.Recv(context.Background())

		s.mu.Lock()
		if s.closed || s.conn != conn {
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.endLocked(Disconnected, ReasonConnectionLost)
			s.mu.Unlock()
			return
		}
		if _, rejected := f.(wire.Rejected); rejected {
			s.endLocked(Failed, ReasonRejected)
			s.mu.Unlock()
			return
		}
		// offer, answer and ice arrive with the WebRTC work; until then any
		// other frame is noise to survive, not state to act on.
		s.mu.Unlock()
	}
}

// setStateLocked moves the machine and tells the UI. Callers hold s.mu.
func (s *Session) setStateLocked(state State, reason Reason) {
	s.state = state
	s.events.emit(StateChanged{State: state, Reason: reason})
}

// endLocked is every terminal transition: announce it, then tear down.
func (s *Session) endLocked(state State, reason Reason) {
	s.setStateLocked(state, reason)
	s.teardownLocked()
}

// teardownLocked releases everything the Session holds and closes the event
// stream. The slow parts — cloudflared dying — run off the caller's back.
func (s *Session) teardownLocked() {
	s.closed = true
	conn, rdv := s.conn, s.rdv
	s.conn, s.rdv, s.signal = nil, nil, nil
	go func() {
		if conn != nil {
			conn.Close()
		}
		if rdv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), teardownWait)
			_ = rdv.Stop(ctx)
			cancel()
		}
		s.events.close()
	}()
}
