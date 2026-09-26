package gui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
	"github.com/DSNR/dcc/internal/words"
)

// Options configures the desktop client.
type Options struct {
	// Name is this side's Display Name, shown on the status bar as a
	// reminder of what the other side is being told.
	Name string
	// New mints Sessions, one per Invite. Required.
	New NewSession
	// Store reads the stored Conversations back. Nil keeps and shows no
	// history.
	Store Store
	// Warnings are put in front of the participant at startup — an Identity
	// that had to be reset, chiefly.
	Warnings []string
	// Repaint asks the window to draw again, and is called from whichever
	// goroutine a Session's events arrive on. Nil is a Model nothing is
	// watching, which is what a test wants.
	Repaint func()
}

// Model is the desktop client: everything the window knows, and everything a
// participant can do to it. Every method returns at once — anything that
// blocks, from cloudflared coming alive to a Store read, happens on a
// goroutine of its own — so the window never stops painting. Send is the one
// exception: it waits for the Session to take the message, because the window
// cannot know whether to clear the message box until it has.
//
// It is safe to use from any goroutine: the Gio layer calls the actions, a
// Session's events arrive on the Session's own goroutine, and Screen takes
// one consistent snapshot of the result.
type Model struct {
	name    string
	newSess NewSession
	store   Store
	repaint func()

	mu sync.Mutex
	// sess is the running Session, nil when there is none. Only the events
	// channel closing clears it, so that a Session is released exactly once,
	// whatever ended it.
	sess   Session
	state  session.State
	reason session.Reason
	link   transport.Link
	// peer is the Display Name the other side announced — a label, never
	// proof.
	peer string
	// invite is the Invite to hand over, empty when there is none.
	invite string
	// prompt is the standing Security Code prompt, nil when none stands.
	prompt *session.VerifyPrompt

	// log is everything that has happened, in order; index finds the entry a
	// delivery status belongs to. early holds statuses that arrived before
	// their message did: sending does not hold the lock, so an ack can beat
	// the entry it belongs to onto the log.
	early map[string]session.DeliveryStatus
	log   []Entry
	index map[string]int

	// conversations is the history panel's list, as of the last read.
	conversations []Conversation
	// shown is the Peers whose stored Conversation is already in the
	// conversation area, so that reading one in twice cannot make it look
	// like it was said twice.
	shown map[identity.PublicKey]bool

	// closing reports a participant on their way out; done closes once the
	// Session has let go of everything it holds and the window may go.
	closing bool
	done    chan struct{}
	// gone guards done against being closed twice — quitting twice, or
	// quitting a Session that ended on its own.
	gone sync.Once
}

// New builds the desktop client, ready to be painted before anything has
// happened.
func New(opts Options) *Model {
	m := &Model{
		name:    opts.Name,
		newSess: opts.New,
		store:   opts.Store,
		repaint: opts.Repaint,
		state:   session.Idle,
		early:   make(map[string]session.DeliveryStatus),
		shown:   make(map[identity.PublicKey]bool),
		index:   make(map[string]int),
		done:    make(chan struct{}),
	}
	m.add(notice(welcome))
	for _, warning := range opts.Warnings {
		m.add(notice("⚠ " + warning))
	}
	m.RefreshHistory()
	return m
}

// Screen is what the window should be showing. It is a copy: the Gio layer
// paints it without holding anything, so a Session's events never wait on a
// frame.
func (m *Model) Screen() Screen {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := make([]Entry, len(m.log))
	copy(entries, m.log)
	conversations := make([]Conversation, len(m.conversations))
	copy(conversations, m.conversations)

	s := Screen{
		Name:          m.name,
		Status:        m.statusLine(),
		Peer:          m.peer,
		Entries:       entries,
		Invite:        m.invite,
		Conversations: conversations,
		Closing:       m.closing,
		Controls: Controls{
			Host:       m.sess == nil && !m.closing,
			Join:       m.sess == nil && !m.closing,
			Send:       m.sess != nil && m.state == session.Connected && m.prompt == nil && !m.closing,
			Disconnect: m.sess != nil && !m.closing,
		},
	}
	if m.prompt != nil {
		p := prompt(*m.prompt)
		s.Prompt = &p
	}
	return s
}

// Done closes when the participant has asked to leave and the Session has
// finished letting go — which is the only thing standing between quitting and
// an orphaned cloudflared.
func (m *Model) Done() <-chan struct{} { return m.done }

// Host opens a Rendezvous and starts waiting for a Peer.
func (m *Model) Host() {
	m.mu.Lock()
	s, ok := m.start()
	if !ok {
		m.mu.Unlock()
		m.changed()
		return
	}
	m.add(notice("Opening a Rendezvous — cloudflared takes a few seconds…"))
	m.mu.Unlock()
	m.changed()

	// Hosting blocks while the tunnel comes alive, and a Session that could
	// not host is closed rather than left holding one nobody knows about.
	go func() {
		if err := s.Host(context.Background()); err != nil {
			_ = s.Close()
			m.say("Could not open a Rendezvous: " + err.Error())
		}
	}()
}

// Join connects to an Invite someone handed over. The Invite is trimmed
// first: it arrives by being pasted, and a pasted line usually brings
// whitespace with it.
func (m *Model) Join(invite string) {
	invite = strings.TrimSpace(invite)

	m.mu.Lock()
	if invite == "" {
		m.add(notice("Paste the Invite the other person sent you, then connect."))
		m.mu.Unlock()
		m.changed()
		return
	}
	s, ok := m.start()
	if !ok {
		m.mu.Unlock()
		m.changed()
		return
	}
	m.add(notice("Connecting…"))
	m.mu.Unlock()
	m.changed()

	// Join returns as soon as the Invite parses, and reports the rest of the
	// attempt as events.
	go func() {
		if err := s.Join(context.Background(), invite); err != nil {
			_ = s.Close()
			m.say("Could not connect: " + err.Error())
		}
	}()
}

// start mints the Session a new Invite or connection needs and puts it in
// place, refusing to abandon one that is already running. It is called with
// the lock held.
func (m *Model) start() (Session, bool) {
	if m.closing {
		return nil, false
	}
	if m.sess != nil {
		m.add(notice("A Session is already running — disconnect first."))
		return nil, false
	}
	s, err := m.newSess()
	if err != nil {
		m.add(notice("Could not start a Session: " + err.Error()))
		return nil, false
	}
	m.sess = s
	go m.pump(s)
	return s, true
}

// pump folds one Session's events in until it is over for good.
func (m *Model) pump(s Session) {
	for e := range s.Events() {
		m.mu.Lock()
		if m.sess == s {
			m.apply(e)
		}
		m.mu.Unlock()
		m.changed()
	}
	m.released(s)
	m.changed()
}

// Accept and Refuse resolve the standing Security Code prompt. The prompt
// comes down as soon as it is answered: the Session may stay in Verifying
// until ICE finishes, but the question has been put and settled.
func (m *Model) Accept() { m.answer(true) }

// Refuse turns the other side away, which ends the Session.
func (m *Model) Refuse() { m.answer(false) }

func (m *Model) answer(yes bool) {
	m.mu.Lock()
	if m.prompt == nil || m.sess == nil {
		m.mu.Unlock()
		return
	}
	name, peer, s := m.prompt.Name, m.prompt.Peer, m.sess

	if !yes {
		if err := s.Refuse(); err != nil {
			m.add(notice(err.Error()))
			m.mu.Unlock()
			m.changed()
			return
		}
		m.prompt = nil
		m.add(notice(fmt.Sprintf("Refused — the Session is over. Nothing %q sent was shown.", name)))
		m.mu.Unlock()
		m.changed()
		return
	}
	// The prompt stays up if accepting failed — the acceptance was not
	// recorded, so the question still stands and can be answered again.
	if err := s.Accept(); err != nil {
		m.add(notice(err.Error()))
		m.mu.Unlock()
		m.changed()
		return
	}
	m.prompt = nil
	m.add(notice(fmt.Sprintf("Accepted — %q is verified, and content can flow.", name)))
	m.mu.Unlock()
	m.changed()

	// What has been said with this Peer before goes back on screen, so that
	// a restart resumes the Conversation instead of losing it.
	m.restoreHistory(peer, name)
}

// Send says something to the other person. It reports what it did with the
// body: an unsent message stays in the message box, where it can be sent
// again once there is somebody to send it to.
func (m *Model) Send(body string) (sent bool) {
	defer m.changed()

	m.mu.Lock()
	switch {
	case body == "":
		m.mu.Unlock()
		return false
	case m.prompt != nil:
		m.add(notice("Check the Security Code with the other person first."))
		m.mu.Unlock()
		return false
	case m.sess == nil || m.state != session.Connected:
		m.add(notice("Nothing to send to yet — host a Session, or connect to an Invite."))
		m.mu.Unlock()
		return false
	}
	s := m.sess
	// Sending writes the message to the Store on the way out, so the lock
	// goes back before it: a Session event arriving mid-write must not have
	// to queue behind a disk.
	m.mu.Unlock()

	id, err := s.SendText(body)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.add(notice("Not sent: " + err.Error()))
		return false
	}
	m.add(Entry{At: time.Now(), Who: words.Me, Mine: true, Body: body, ID: id, Status: session.TextPending})
	return true
}

// Disconnect ends the Session but stays in the app, so that the conversation
// can be read back and another Invite made.
func (m *Model) Disconnect() {
	m.mu.Lock()
	if m.sess == nil {
		m.mu.Unlock()
		return
	}
	m.add(notice("Disconnecting…"))
	// Close returns at once and tears down behind itself; the events channel
	// closing is what tells us it finished.
	if err := m.sess.Close(); err != nil {
		m.add(notice(err.Error()))
	}
	m.mu.Unlock()
	m.changed()
}

// Quit leaves, but not before the Session has let go of what it owns — a
// cloudflared process must not outlive the app that started it. Done closes
// when it has.
func (m *Model) Quit() {
	m.mu.Lock()
	if m.sess == nil || m.closing {
		m.mu.Unlock()
		m.finish()
		return
	}
	m.closing = true
	s := m.sess
	m.add(notice("Closing the Session and taking the Rendezvous down…"))
	m.mu.Unlock()
	m.changed()

	go func() { _ = s.Close() }()
}

// finish lets the window go. Closing twice is fine: a Session that ended on
// its own and a participant who asked to leave can arrive here together.
func (m *Model) finish() { m.gone.Do(func() { close(m.done) }) }

// apply folds one Session event into what is on screen. It is called with the
// lock held.
func (m *Model) apply(e session.Event) {
	switch e := e.(type) {
	case session.StateChanged:
		m.state, m.reason = e.State, e.Reason
		if e.State != session.Verifying {
			// A transition out of Verifying withdraws the prompt.
			m.prompt = nil
		}
		if said := words.State(e); said != "" {
			m.add(notice(said))
		}

	case session.InviteReady:
		m.invite = e.Invite.String()
		m.add(notice("Hand this Invite to the other person, over a channel you both trust:"))
		m.add(notice(m.invite))

	case session.VerifyPrompt:
		p := e
		m.prompt = &p
		m.peer = e.Name
		for _, line := range append(words.SecurityCode(e), words.IdentityChanged(e)...) {
			m.add(notice(line))
		}

	case session.LinkChanged:
		m.link = e.Link
		m.add(notice(words.Link(e.Link)))

	case session.TextReceived:
		m.add(Entry{At: e.At, Who: m.peerName(), Body: e.Body})

	case session.TextStatus:
		i, known := m.index[e.ID]
		if !known {
			m.early[e.ID] = e.Status
			return
		}
		m.log[i].Status = e.Status
	}
}

// released is a Session's events channel closing: it is over for good,
// whatever ended it.
func (m *Model) released(s Session) {
	m.mu.Lock()
	if m.sess != s {
		m.mu.Unlock()
		return
	}
	m.sess = nil
	m.prompt = nil
	m.invite = ""
	m.link = 0
	if m.state != session.Idle && m.state != session.Disconnected && m.state != session.Failed {
		m.state, m.reason = session.Disconnected, session.ReasonNone
		m.add(notice("The Session is over."))
	}
	leaving := m.closing
	m.mu.Unlock()

	// A Session leaves history behind it, so the panel is a read out of date
	// the moment one ends.
	m.RefreshHistory()
	if leaving {
		m.finish()
	}
}

// peerName is what to attribute the other side's messages to. It is called
// with the lock held.
func (m *Model) peerName() string {
	if m.peer == "" {
		return "them"
	}
	return m.peer
}

// add appends one entry to the conversation. It is called with the lock held.
func (m *Model) add(e Entry) {
	if e.ID != "" {
		m.index[e.ID] = len(m.log)
		if status, waiting := m.early[e.ID]; waiting {
			e.Status = status
			delete(m.early, e.ID)
		}
	}
	m.log = append(m.log, e)
}

// say puts one line in the conversation from a goroutine that does not hold
// the lock, and asks for a repaint.
func (m *Model) say(line string) {
	m.mu.Lock()
	m.add(notice(line))
	m.mu.Unlock()
	m.changed()
}

// changed asks the window to paint what just happened. It must be called
// without the lock: a repaint may take a Screen.
func (m *Model) changed() {
	if m.repaint != nil {
		m.repaint()
	}
}
