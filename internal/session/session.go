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
	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/relay"
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
	// History persists the Conversation as messages flow. Nil keeps
	// nothing.
	History History
	// ReconnectWait overrides ReconnectBudget; zero means the protocol
	// value. Tests shorten it.
	ReconnectWait time.Duration
	// RelayOnly disables every direct path, leaving the Rendezvous's
	// fallback relay as the only route. Tests use it to prove the relay
	// carries a Session alone.
	RelayOnly bool
	// Devices is where a Call's microphone and speaker come from. Nil means
	// the real ones; tests pass a media.Fake.
	Devices media.Devices
	// RingWait overrides RingTimeout; zero means the protocol value. Tests
	// shorten it.
	RingWait time.Duration
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
	history  History
	events   *eventQueue
	budget   time.Duration
	devices  media.Devices
	ringWait time.Duration

	mu       sync.Mutex
	state    State
	starting bool
	closed   bool
	rdv      *rendezvous.Rendezvous
	signal   *signaling.Server
	conn     *signaling.Conn
	trans    *transport.Transport
	// relayListener is the Host's side of the fallback relay for the current
	// transport; bridge is the Peer's. Each attachment mints its own — the
	// TCPMux inside the old transport dies with it and takes its listener
	// along.
	relayListener *relay.Listener
	bridge        *relay.Bridge
	relayOnly     bool
	peer          identity.PublicKey
	peerName      string
	locked        bool
	accepted      bool
	up            bool
	link          transport.Link
	held          []wire.Frame

	// sessionID is the id the Host minted — held by both sides, echoed by
	// the Peer's re-handshake to claim it is resuming this Session and not
	// starting another.
	sessionID string
	// isHost picks the reconnect posture: a Host waits at its Rendezvous
	// while a Peer redials it.
	isHost bool
	// invite is what the Peer joined with, kept for redials.
	invite rendezvous.Invite
	// gen counts connection attachments. Callbacks from a connection or
	// transport that has since been replaced carry a stale gen and are
	// ignored.
	gen int
	// queue holds every sent message the Peer hasn't acknowledged, in send
	// order, so a reconnect can put them back on the wire.
	queue []outText
	// seen remembers which incoming message ids were already taken this
	// Session, so a resend after a reconnect is re-acknowledged but never
	// shown twice.
	seen map[string]bool
	// redialing notes the Peer's redial loop is running; reconnectTimer is
	// the Host's budget running out.
	redialing      bool
	reconnectUntil time.Time
	reconnectTimer *time.Timer

	// The Call inside this Session, of which there is at most one. callID is
	// empty exactly when callState is NoCall; audio is the open devices,
	// held only while the Call is Active.
	callState CallState
	callID    string
	ringTimer *time.Timer
	audio     *media.Audio
	muted     bool
	// noMic records that this machine's microphone could not be opened, so
	// that unmuting one that does not exist is refused rather than
	// announced.
	noMic bool
	// remoteMedia is the other side's last announced stream state, held so
	// that one that arrives before this side is Active is not lost.
	remoteMedia     MediaChanged
	haveRemoteMedia bool
}

// outText is one message the other side hasn't acknowledged: sent and
// waiting, or written during Reconnecting and waiting to be sent at all.
type outText struct {
	id, body string
	sent     bool
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
	budget := opts.ReconnectWait
	if budget == 0 {
		budget = ReconnectBudget
	}
	ring := opts.RingWait
	if ring == 0 {
		ring = RingTimeout
	}
	return &Session{
		identity:  opts.Identity,
		name:      opts.Name,
		cert:      cert,
		tunnel:    opts.Tunnel,
		pins:      pins,
		history:   opts.History,
		events:    newEventQueue(),
		budget:    budget,
		devices:   opts.Devices,
		ringWait:  ring,
		relayOnly: opts.RelayOnly,
		state:     Idle,
		seen:      make(map[string]bool),
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

	// The relay handler routes to whichever listener belongs to the current
	// transport — reconnects mint a new one — and refuses while there is
	// none.
	relayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		listener := s.relayListener
		s.mu.Unlock()
		if listener == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		listener.ServeHTTP(w, r)
	})

	rdv, err := rendezvous.Start(ctx, rendezvous.Options{Tunnel: s.tunnel, Signal: handler, Relay: relayHandler})
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
	s.sessionID = hello.SessionID
	s.isHost = true
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
	s.invite = parsed
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
	if err := s.pins.Pin(s.peerName, s.peer); err != nil {
		// The prompt still stands: nothing was pinned, nothing flows, and
		// answering again retries.
		return fmt.Errorf("session: recording the acceptance: %w", err)
	}
	s.accepted = true
	s.maybeConnectLocked()
	return nil
}

// SendText sends one chat message to the Peer and returns its id — a UUIDv7,
// minted here, that carries the send time. The message's life after that
// arrives as TextStatus events: pending and sent at once, delivered when the
// Peer acknowledges it. A message written while Reconnecting is queued and
// goes out — with everything else still unacknowledged — the moment the
// connection is back.
func (s *Session) SendText(body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.state != Connected && s.state != Reconnecting) {
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
	// Persist-before-send: a message that cannot be kept is not said, and
	// the refusal is synchronous, like the wire's.
	if s.history != nil {
		if err := s.history.Outgoing(s.peer, s.peerName, f.ID, body, time.Now()); err != nil {
			return "", fmt.Errorf("session: keeping the message: %w", err)
		}
	}
	s.events.emit(TextStatus{ID: f.ID, Status: TextPending})
	msg := outText{id: f.ID, body: body}
	if s.state == Connected {
		// A refused Send means the transport is dying under us: the message
		// stays queued and the reconnect that follows resends it.
		msg.sent = s.trans.Send(f) == nil
	}
	s.queue = append(s.queue, msg)
	if msg.sent {
		s.status(f.ID, TextSent)
	}
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
	s.sessionID = hostHello.SessionID
	s.attachLocked(conn, hostHello.Name, hostHello.DTLS, true, false)
}

// onPeer receives every connection that authenticates at the Rendezvous.
// The first Identity through locks the Invite; a different Identity after
// that is told, under the transport keys, that the Invite is taken. The
// locked Identity handshaking again — echoing the session_id after a blip,
// or afresh after a crash — replaces the stale connection and the Session
// carries on: it was already accepted, so no new prompt stands in the way.
func (s *Session) onPeer(conn *signaling.Conn, hello wire.PeerHello) {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		conn.Close()
	case s.state == Hosting && hello.SessionID == "":
		s.locked = true
		s.attachLocked(conn, hello.Name, hello.DTLS, false, false)
		s.mu.Unlock()
	case (s.state == Connected || s.state == Reconnecting) &&
		conn.Peer() == s.peer &&
		(hello.SessionID == "" || hello.SessionID == s.sessionID):
		s.enterReconnectLocked()
		s.attachLocked(conn, hello.Name, hello.DTLS, false, true)
		s.mu.Unlock()
	case s.locked && conn.Peer() != s.peer:
		s.mu.Unlock()
		go rejectPeer(conn)
	default:
		// A claim to be resuming some other Session, or the locked
		// Identity turning up in a state with nothing to resume.
		s.mu.Unlock()
		conn.Close()
	}
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
// transport underneath it and — on a first attachment — raises the Security
// Code prompt. The Session holds in Verifying until Accept or Refuse, but
// Signaling and ICE run meanwhile, so an accepted prompt lands on a
// transport that is already up. A reconnect attachment raises no prompt:
// the Identity was verified when the Session began, and the Session stays
// Reconnecting until the new transport comes up.
func (s *Session) attachLocked(conn *signaling.Conn, name, dtls string, initiator, reconnect bool) {
	peer := conn.Peer()
	s.conn = conn
	s.peer = peer
	s.peerName = name
	s.up = false
	s.gen++
	gen := s.gen

	// Each transport gets its own end of the fallback relay: the Host a
	// fresh listener behind /v1/relay, the Peer a fresh bridge to it. A Peer
	// whose bridge won't open just has no relay to fall back on — direct
	// paths still stand.
	topts := transport.Options{
		Certificate: s.cert,
		Remote:      dtls,
		Initiator:   initiator,
		RelayOnly:   s.relayOnly,
		// The Signal callback deliberately reaches past the Session's own
		// lock: the initiator's Start sends the offer before returning,
		// while this method still holds it.
		Signal: func(f wire.Frame) {
			ctx, cancel := context.WithTimeout(context.Background(), teardownWait)
			defer cancel()
			_ = conn.Send(ctx, f)
		},
		Up:      func(link transport.Link) { s.onTransportUp(gen, link) },
		Frame:   func(f wire.Frame) { s.onData(gen, f) },
		Down:    func(err error) { s.onTransportDown(gen, err) },
		Audio:   func(payload []byte) { s.onAudio(gen, payload) },
		MediaUp: func() { s.onMediaUp(gen) },
	}
	if s.isHost {
		s.relayListener = relay.NewListener()
		topts.RelayListener = s.relayListener
	} else {
		if s.bridge != nil {
			_ = s.bridge.Close()
		}
		s.bridge = nil
		if bridge, err := relay.Open(s.invite.RelayURL()); err == nil {
			s.bridge = bridge
			topts.RelayAddr = bridge.Addr()
		}
	}
	trans, err := transport.Start(topts)
	if err != nil {
		s.endLocked(Failed, ReasonTransportFailed)
		return
	}
	s.trans = trans

	if !reconnect {
		pinned, known := s.pins.Pinned(name)
		s.setStateLocked(Verifying, ReasonNone)
		s.events.emit(VerifyPrompt{
			Code:    s.identity.SecurityCode(peer),
			Name:    name,
			Peer:    peer,
			Changed: known && pinned != peer,
		})
	}
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
			s.connectionLostLocked()
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
func (s *Session) onTransportUp(gen int, link transport.Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen {
		return
	}
	s.up = true
	s.link = link
	s.maybeConnectLocked()
}

// onTransportDown is the transport dying, before or after it came up. Under
// an established Session the reconnect machinery takes over; before that,
// the Session never got going at all.
func (s *Session) onTransportDown(gen int, _ error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen {
		return
	}
	if s.state == Connected || s.state == Reconnecting {
		s.enterReconnectLocked()
		return
	}
	s.endLocked(Failed, ReasonTransportFailed)
}

// maybeConnectLocked moves Verifying or Reconnecting to Connected once both
// of its gates — the accepted prompt and the transport being up — are open,
// resends what the Peer never acknowledged, then releases whatever the Peer
// said in the meantime.
func (s *Session) maybeConnectLocked() {
	if s.state != Verifying && s.state != Reconnecting {
		return
	}
	if !s.accepted || !s.up {
		return
	}
	if s.reconnectTimer != nil {
		s.reconnectTimer.Stop()
		s.reconnectTimer = nil
	}
	s.setStateLocked(Connected, ReasonNone)
	s.events.emit(LinkChanged{Link: s.link})
	s.resendLocked()
	held := s.held
	s.held = nil
	for _, f := range held {
		s.handleDataLocked(f)
	}
}

// onData receives every DataChannel frame. While this side's Security Code
// prompt stands, frames are held — nothing the Peer says is shown or
// acknowledged until the person here has accepted who they're talking to.
func (s *Session) onData(gen int, f wire.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || gen != s.gen {
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
		if s.seen[f.ID] {
			// A resend of something already shown — the blip ate the ack,
			// not the message. Acknowledged again, shown once.
			_ = s.trans.Send(wire.Ack{ID: f.ID})
			return
		}
		at := time.Now()
		// Persist-before-ack: the ack promises the message is kept, so a
		// message that could not be kept gets no ack and is not shown — to
		// the Peer it simply was not delivered.
		if s.history != nil {
			fresh, err := s.history.Incoming(s.peer, s.peerName, f.ID, f.Body, at)
			if err != nil {
				return
			}
			if !fresh {
				// A resend of something already kept: acknowledged again,
				// shown once.
				s.seen[f.ID] = true
				_ = s.trans.Send(wire.Ack{ID: f.ID})
				return
			}
		}
		s.seen[f.ID] = true
		s.events.emit(TextReceived{ID: f.ID, Body: f.Body, At: at})
		// The ack is application-level delivery: it says dcc took the
		// message, not merely that SCTP moved the bytes.
		_ = s.trans.Send(wire.Ack{ID: f.ID})
	case wire.Call, wire.Accept, wire.Reject, wire.Hangup, wire.Media:
		s.handleCallLocked(f)
	case wire.Ack:
		for i := range s.queue {
			if s.queue[i].id == f.ID {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				s.status(f.ID, TextDelivered)
				return
			}
		}
	}
}

// status moves one kept message's delivery along, in the History and on
// screen together. The History write's error is deliberately dropped: the
// message itself is already safe, and a stale status marker in tomorrow's
// history is not worth ending today's Session over.
func (s *Session) status(id string, status DeliveryStatus) {
	if s.history != nil {
		_ = s.history.Status(id, status)
	}
	s.events.emit(TextStatus{ID: id, Status: status})
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
	s.endCallLocked(CallLost)
	s.closed = true
	s.gen++
	if s.reconnectTimer != nil {
		s.reconnectTimer.Stop()
		s.reconnectTimer = nil
	}
	for _, m := range s.queue {
		s.status(m.id, TextFailed)
	}
	s.queue = nil
	conn, rdv, trans := s.conn, s.rdv, s.trans
	listener, bridge := s.relayListener, s.bridge
	s.conn, s.rdv, s.signal, s.trans = nil, nil, nil, nil
	s.relayListener, s.bridge = nil, nil
	go func() {
		if trans != nil {
			_ = trans.Close()
		}
		// The transport's mux closes the relay listener; closing it here too
		// covers a listener whose transport never started.
		if listener != nil {
			_ = listener.Close()
		}
		if bridge != nil {
			_ = bridge.Close()
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
