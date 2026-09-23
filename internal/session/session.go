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
	"github.com/DSNR/dcc/internal/transport"
	"github.com/DSNR/dcc/internal/wire"
)

// teardownWait bounds how long ending a Session may spend closing what it
// owns — chiefly waiting for cloudflared to die.
const teardownWait = 10 * time.Second

// maxHeld bounds the DataChannel frames held back while this side's Security
// Code prompt stands. A Peer that keeps talking past that into an unresolved
// prompt loses the excess; its acks never come, which is answer enough.
const maxHeld = 256

// Options configures a Session.
type Options struct {
	// Identity is this install's persistent keypair.
	Identity identity.Identity
	// Name is the Display Name announced to the other side.
	Name string
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
	cert     transport.Certificate
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
	trans    *transport.Transport
	peer     identity.PublicKey
	peerName string
	locked   bool
	accepted bool
	up       bool
	link     transport.Link
	held     []wire.Frame
	unacked  []string
}

// New builds an Idle Session, minting the DTLS certificate its handshake
// will bind. The name is checked here too, so a Session that would fail
// every handshake is refused before it starts.
func New(opts Options) (*Session, error) {
	cert, err := transport.NewCertificate()
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	if _, err := wire.EncodePeerHello(wire.PeerHello{Name: opts.Name, DTLS: cert.Fingerprint()}); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	pins := opts.Pins
	if pins == nil {
		pins = NewMemoryPins()
	}
	return &Session{
		identity: opts.Identity,
		name:     opts.Name,
		cert:     cert,
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

	hello := wire.HostHello{SessionID: sessionID.String(), Name: s.name, DTLS: s.cert.Fingerprint()}

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
// Session moves on to Connected as soon as the transport underneath is up —
// usually already, since ICE ran while the prompt stood.
func (s *Session) Accept() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Verifying {
		return fmt.Errorf("session: no Security Code prompt to accept while %s", s.state)
	}
	s.pins.Pin(s.peerName, s.peer)
	s.accepted = true
	s.maybeConnectLocked()
	return nil
}

// SendText sends one chat message to the Peer and returns its id — a UUIDv7,
// minted here, that carries the send time. The message's life after that
// arrives as TextStatus events: pending and sent at once, delivered when the
// Peer acknowledges it.
func (s *Session) SendText(body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != Connected {
		return "", fmt.Errorf("session: cannot send text while %s", s.state)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("session: minting a message id: %w", err)
	}
	f := wire.Text{ID: id.String(), Body: body}
	// A body the wire would refuse is reported here, synchronously, before
	// any TextStatus event exists to clean up after.
	if _, err := wire.Encode(f); err != nil {
		return "", err
	}
	s.events.emit(TextStatus{ID: f.ID, Status: TextPending})
	if err := s.trans.Send(f); err != nil {
		s.events.emit(TextStatus{ID: f.ID, Status: TextFailed})
		return "", err
	}
	s.unacked = append(s.unacked, f.ID)
	s.events.emit(TextStatus{ID: f.ID, Status: TextSent})
	return f.ID, nil
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
		Hello:    wire.PeerHello{Name: s.name, DTLS: s.cert.Fingerprint()},
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
	s.attachLocked(conn, hostHello.Name, hostHello.DTLS, true)
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
	s.attachLocked(conn, hello.Name, hello.DTLS, false)
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

// attachLocked adopts an authenticated connection, starts the WebRTC
// transport underneath it and raises the Security Code prompt. The Session
// holds in Verifying until Accept or Refuse — but Signaling and ICE run
// meanwhile, so an accepted prompt lands on a transport that is already up.
func (s *Session) attachLocked(conn *signaling.Conn, name, dtls string, initiator bool) {
	peer := conn.Peer()
	s.conn = conn
	s.peer = peer
	s.peerName = name

	// The Signal callback deliberately reaches past the Session's own lock:
	// the initiator's Start sends the offer before returning, while this
	// method still holds it.
	trans, err := transport.Start(transport.Options{
		Certificate: s.cert,
		Remote:      dtls,
		Initiator:   initiator,
		Signal: func(f wire.Frame) {
			ctx, cancel := context.WithTimeout(context.Background(), teardownWait)
			defer cancel()
			_ = conn.Send(ctx, f)
		},
		Up:    s.onTransportUp,
		Frame: s.onData,
		Down:  s.onTransportDown,
	})
	if err != nil {
		s.endLocked(Failed, ReasonTransportFailed)
		return
	}
	s.trans = trans

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

// readLoop watches an adopted connection: it feeds Signaling frames to the
// transport, notices the encrypted rejection, and notices death.
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
		trans := s.trans
		s.mu.Unlock()

		// Outside the lock: applying an offer makes the transport signal the
		// answer straight back through the Session.
		switch f.(type) {
		case wire.Offer, wire.Answer, wire.ICE:
			trans.HandleSignal(f)
		}
	}
}

// onTransportUp is the WebRTC transport reporting its DataChannel open, with
// the remote certificate verified against the handshake.
func (s *Session) onTransportUp(link transport.Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.up = true
	s.link = link
	s.maybeConnectLocked()
}

// onTransportDown is the transport dying, before or after it came up. Under
// a Connected Session that is a lost connection; before that, the Session
// never got going at all.
func (s *Session) onTransportDown(error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.state == Connected {
		s.endLocked(Disconnected, ReasonConnectionLost)
		return
	}
	s.endLocked(Failed, ReasonTransportFailed)
}

// maybeConnectLocked moves Verifying to Connected once both of its gates —
// the accepted prompt and the transport being up — are open, then releases
// whatever the Peer said in the meantime.
func (s *Session) maybeConnectLocked() {
	if s.state != Verifying || !s.accepted || !s.up {
		return
	}
	s.setStateLocked(Connected, ReasonNone)
	s.events.emit(LinkChanged{Link: s.link})
	held := s.held
	s.held = nil
	for _, f := range held {
		s.handleDataLocked(f)
	}
}

// onData receives every DataChannel frame. While this side's Security Code
// prompt stands, frames are held — nothing the Peer says is shown or
// acknowledged until the person here has accepted who they're talking to.
func (s *Session) onData(f wire.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.state != Connected {
		if len(s.held) < maxHeld {
			s.held = append(s.held, f)
		}
		return
	}
	s.handleDataLocked(f)
}

// handleDataLocked acts on one DataChannel frame from the Peer. Frames whose
// work hasn't landed yet — bye, the Call family — are survived, not acted on.
func (s *Session) handleDataLocked(f wire.Frame) {
	switch f := f.(type) {
	case wire.Text:
		s.events.emit(TextReceived{ID: f.ID, Body: f.Body, At: time.Now()})
		// The ack is application-level delivery: it says dcc took the
		// message, not merely that SCTP moved the bytes.
		_ = s.trans.Send(wire.Ack{ID: f.ID})
	case wire.Ack:
		for i, id := range s.unacked {
			if id == f.ID {
				s.unacked = append(s.unacked[:i], s.unacked[i+1:]...)
				s.events.emit(TextStatus{ID: f.ID, Status: TextDelivered})
				return
			}
		}
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
// stream. Messages still waiting on an ack are declared failed first — the
// Session ending is the answer they were waiting for. The slow parts —
// cloudflared dying — run off the caller's back.
func (s *Session) teardownLocked() {
	s.closed = true
	for _, id := range s.unacked {
		s.events.emit(TextStatus{ID: id, Status: TextFailed})
	}
	s.unacked = nil
	conn, rdv, trans := s.conn, s.rdv, s.trans
	s.conn, s.rdv, s.signal, s.trans = nil, nil, nil, nil
	go func() {
		if trans != nil {
			_ = trans.Close()
		}
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
