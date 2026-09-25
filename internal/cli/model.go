package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
	"github.com/DSNR/dcc/internal/wire"
)

const (
	// inputRows is how much of the terminal the message box occupies. A
	// longer message scrolls within it.
	inputRows = 3
	// inputMaxRows caps how many lines one message may be typed over, which
	// is what the message box's own newline key enforces.
	inputMaxRows = 12
	// inputRunes caps one message at a quarter of the wire's byte cap, so
	// that anything that can be typed can be sent even if every character of
	// it is a four-byte emoji.
	inputRunes = wire.MaxTextBytes / 4
	// defaultWidth and defaultHeight stand in until the terminal says how big
	// it is, so that a Model is renderable the moment it exists.
	defaultWidth  = 80
	defaultHeight = 24
)

// Options configures the terminal interface.
type Options struct {
	// Name is this side's Display Name, shown on the status line as a
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
}

// Model is the terminal interface: keystrokes and Session events in, one
// rendered frame out. It is a bubbletea.Model, which only Run needs to know.
type Model struct {
	name    string
	newSess NewSession
	store   Store

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
	// prompt is the standing Security Code prompt, nil when none stands.
	prompt *session.VerifyPrompt

	// call is where the Call inside the Session stands, and remoteMic is
	// what the other side last said about their microphone. Both are only
	// meaningful while a Call is running.
	call      session.CallState
	remoteMic bool

	// log is everything that has happened, in order; index finds the entry a
	// delivery status belongs to.
	log   []entry
	index map[string]int

	view  viewport.Model
	input textarea.Model

	width, height int
	// promptLimit is how many rows layout left the standing Security Code
	// prompt, which is all of it on any terminal worth using.
	promptLimit int
	// closing reports that the participant has asked to leave and the Session
	// is being torn down, which is the one time input is ignored.
	closing bool
}

// New builds the terminal interface, ready to render before the terminal has
// said how big it is.
func New(opts Options) Model {
	input := textarea.New()
	input.Placeholder = "Type a message, or /help"
	input.ShowLineNumbers = false
	input.CharLimit = inputRunes
	input.MaxHeight = inputMaxRows
	// Enter sends, so the message box gets the other newline that every
	// terminal can actually deliver.
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	input.Focus()

	view := viewport.New(defaultWidth, defaultHeight)
	// The conversation scrolls on keys that cannot be part of a message; the
	// pager defaults claim letters, which would make 'j' unsayable.
	view.KeyMap = viewport.KeyMap{
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		Up:       key.NewBinding(key.WithKeys("shift+up")),
		Down:     key.NewBinding(key.WithKeys("shift+down")),
	}

	m := Model{
		name:    opts.Name,
		newSess: opts.New,
		store:   opts.Store,
		state:   session.Idle,
		index:   make(map[string]int),
		view:    view,
		input:   input,
		width:   defaultWidth,
		height:  defaultHeight,
	}
	m.add(notice(welcome))
	for _, warning := range opts.Warnings {
		m.add(notice("⚠ " + warning))
	}
	m.layout()
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return textarea.Blink }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.pressed(msg)

	case eventMsg:
		if msg.from != m.sess {
			// A released Session's last words, after the UI moved on.
			return m, nil
		}
		m.apply(msg.event)
		return m, waitEvent(msg.from)

	case overMsg:
		if msg.from != m.sess {
			return m, nil
		}
		m.released()
		return m, nil

	case noticeMsg:
		m.add(notice(msg.text))
		return m, nil

	case closedMsg:
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// pressed handles one key. Enter submits and ctrl+c leaves; everything else
// belongs to the message box and the conversation's scrollback.
func (m Model) pressed(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "enter":
		if m.closing {
			return m, nil
		}
		line := m.input.Value()
		m.input.Reset()
		return m.submit(line)
	}
	if m.closing {
		return m, nil
	}

	var scroll, typing tea.Cmd
	m.view, scroll = m.view.Update(msg)
	m.input, typing = m.input.Update(msg)
	return m, tea.Batch(scroll, typing)
}

// submit acts on one submitted line.
func (m Model) submit(line string) (tea.Model, tea.Cmd) {
	c := parse(line, m.mode())
	switch c.kind {
	case invite:
		return m.host()
	case connect:
		return m.join(c.arg)
	case accept:
		return m.answer(true)
	case refuse:
		return m.answer(false)
	case disconnect:
		return m.disconnect()
	case quit:
		return m.quit()
	case help:
		for _, line := range helpLines {
			m.add(notice(line))
		}
	case history:
		m.showHistory(c.arg)
	case clearhistory:
		m.clearHistory(c.arg)
	case placeCall:
		return m.placeCall()
	case answerCall:
		return m.answerCall()
	case rejectCall:
		return m.rejectCall()
	case hangUp:
		return m.hangUp()
	case mute:
		return m.setMuted(true)
	case unmute:
		return m.setMuted(false)
	case text:
		return m.send(c.arg)
	case unknown:
		m.add(notice(fmt.Sprintf("%s isn't a command. /help lists the ones that are.", c.arg)))
	}
	return m, nil
}

// mode is what a bare word means right now.
func (m Model) mode() mode {
	switch {
	case m.prompt != nil:
		return modeVerify
	case m.state == session.Connected:
		return modeChat
	}
	return modeCommand
}

// host opens a Rendezvous and starts waiting for a Peer.
func (m Model) host() (tea.Model, tea.Cmd) {
	s, ok := m.start()
	if !ok {
		return m, nil
	}
	m.sess = s
	m.add(notice("Opening a Rendezvous — cloudflared takes a few seconds…"))
	return m, tea.Batch(waitEvent(s), hostCmd(s))
}

// join connects to an Invite someone handed over.
func (m Model) join(invite string) (tea.Model, tea.Cmd) {
	if invite == "" {
		m.add(notice("/connect <invite> — paste the Invite the other person sent you."))
		return m, nil
	}
	s, ok := m.start()
	if !ok {
		return m, nil
	}
	m.sess = s
	m.add(notice("Connecting…"))
	return m, tea.Batch(waitEvent(s), joinCmd(s, invite))
}

// start mints the Session a new Invite or connection needs, refusing to
// abandon one that is already running.
func (m *Model) start() (Session, bool) {
	if m.sess != nil {
		m.add(notice("A Session is already running — /disconnect first."))
		return nil, false
	}
	s, err := m.newSess()
	if err != nil {
		m.add(notice("Could not start a Session: " + err.Error()))
		return nil, false
	}
	return s, true
}

// answer resolves the standing Security Code prompt. The prompt comes down as
// soon as it is answered: the Session may stay in Verifying until ICE
// finishes, but the question has been put and settled.
func (m Model) answer(yes bool) (tea.Model, tea.Cmd) {
	if m.prompt == nil || m.sess == nil {
		m.add(notice("There is no Security Code to answer."))
		return m, nil
	}
	name, peer := m.prompt.Name, m.prompt.Peer

	if !yes {
		if err := m.sess.Refuse(); err != nil {
			m.add(notice(err.Error()))
			return m, nil
		}
		m.prompt = nil
		m.layout()
		m.add(notice(fmt.Sprintf("Refused — the Session is over. Nothing %q sent was shown.", name)))
		return m, nil
	}
	// The prompt stays up if accepting failed — the acceptance was not
	// recorded, so the question still stands and can be answered again.
	if err := m.sess.Accept(); err != nil {
		m.add(notice(err.Error()))
		return m, nil
	}
	m.prompt = nil
	m.layout()
	m.add(notice(fmt.Sprintf("Accepted — %q is verified, and content can flow.", name)))
	m.restoreHistory(peer, name)
	return m, nil
}

// send says something to the other person.
func (m Model) send(body string) (tea.Model, tea.Cmd) {
	if body == "" {
		// Only '/msg' with nothing after it arrives here empty; an empty
		// message box does nothing at all.
		m.add(notice("/msg <text> — say something that starts with a slash."))
		return m, nil
	}
	if m.prompt != nil {
		m.add(notice("Check the Security Code above with the other person first — yes or no."))
		return m, nil
	}
	if m.sess == nil || m.state != session.Connected {
		m.add(notice("Nothing to send to yet — /invite to host, or /connect <invite> to join."))
		return m, nil
	}
	id, err := m.sess.SendText(body)
	if err != nil {
		m.add(notice("Not sent: " + err.Error()))
		return m, nil
	}
	m.add(entry{at: time.Now(), who: me, mine: true, body: body, id: id, status: session.TextPending})
	return m, nil
}

// placeCall rings the other person.
func (m Model) placeCall() (tea.Model, tea.Cmd) {
	if m.sess == nil || m.state != session.Connected {
		m.add(notice("There is nobody to call — connect first."))
		return m, nil
	}
	if m.call != session.NoCall {
		m.add(notice("There is already a Call — /hangup to end it first."))
		return m, nil
	}
	if _, err := m.sess.Call(); err != nil {
		m.add(notice("Could not call: " + err.Error()))
		return m, nil
	}
	m.add(notice("Calling " + quoted(m.peerName()) + " — /hangup to give up."))
	return m, nil
}

// answerCall picks up the Call that is ringing here.
func (m Model) answerCall() (tea.Model, tea.Cmd) {
	if m.sess == nil || m.call != session.Incoming {
		m.add(notice("There is no Call to answer."))
		return m, nil
	}
	if err := m.sess.Answer(); err != nil {
		m.add(notice("Could not answer: " + err.Error()))
	}
	return m, nil
}

// rejectCall turns down the Call that is ringing here.
func (m Model) rejectCall() (tea.Model, tea.Cmd) {
	if m.sess == nil || m.call != session.Incoming {
		m.add(notice("There is no Call to reject."))
		return m, nil
	}
	if err := m.sess.Reject(); err != nil {
		m.add(notice("Could not reject: " + err.Error()))
	}
	return m, nil
}

// hangUp ends the Call and leaves the Session up for text.
func (m Model) hangUp() (tea.Model, tea.Cmd) {
	if m.sess == nil || m.call == session.NoCall {
		m.add(notice("There is no Call to hang up. /disconnect ends the Session."))
		return m, nil
	}
	if err := m.sess.Hangup(); err != nil {
		m.add(notice(err.Error()))
	}
	return m, nil
}

// setMuted stops or resumes this side's microphone.
func (m Model) setMuted(muted bool) (tea.Model, tea.Cmd) {
	if m.sess == nil || m.call == session.NoCall {
		m.add(notice("There is no Call to mute."))
		return m, nil
	}
	if err := m.sess.Mute(muted); err != nil {
		m.add(notice(err.Error()))
		return m, nil
	}
	if muted {
		m.add(notice("Microphone muted — they can see that you are."))
	} else {
		m.add(notice("Microphone live."))
	}
	return m, nil
}

// disconnect ends the Session but stays in the app, so that the conversation
// can be read back and another Invite made.
func (m Model) disconnect() (tea.Model, tea.Cmd) {
	if m.sess == nil {
		m.add(notice("There is no Session to disconnect."))
		return m, nil
	}
	m.add(notice("Disconnecting…"))
	// Close returns at once and tears down behind itself; the events channel
	// closing is what tells us it finished.
	if err := m.sess.Close(); err != nil {
		m.add(notice(err.Error()))
	}
	return m, nil
}

// quit leaves, but not before the Session has let go of what it owns — a
// cloudflared process must not outlive the app that started it.
func (m Model) quit() (tea.Model, tea.Cmd) {
	if m.sess == nil || m.closing {
		return m, tea.Quit
	}
	m.closing = true
	m.add(notice("Closing the Session and taking the Rendezvous down…"))
	return m, closeCmd(m.sess)
}

// apply folds one Session event into what is on screen.
func (m *Model) apply(e session.Event) {
	switch e := e.(type) {
	case session.StateChanged:
		m.state, m.reason = e.State, e.Reason
		if e.State != session.Verifying {
			// A transition out of Verifying withdraws the prompt.
			m.prompt = nil
			m.layout()
		}
		if said := stateNotice(e); said != "" {
			m.add(notice(said))
		}

	case session.InviteReady:
		m.add(notice("Hand this Invite to the other person, over a channel you both trust:"))
		m.add(notice(e.Invite.String()))

	case session.VerifyPrompt:
		prompt := e
		m.prompt = &prompt
		m.peer = e.Name
		for _, line := range promptRecord(e) {
			m.add(notice(line))
		}
		m.layout()

	case session.LinkChanged:
		m.link = e.Link
		m.add(notice(linkNotice(e.Link)))

	case session.TextReceived:
		m.add(entry{at: e.At, who: m.peerName(), body: e.Body})

	case session.CallChanged:
		m.call = e.State
		// A Call starts with both microphones live; the other side's own
		// media state follows and corrects this if it does not.
		m.remoteMic = e.State == session.Active
		if said := callNotice(e, m.peerName()); said != "" {
			m.add(notice(said))
		}

	case session.MediaChanged:
		if m.remoteMic != e.Mic {
			m.remoteMic = e.Mic
			m.add(notice(micNotice(e.Mic, m.peerName())))
		}

	case session.TextStatus:
		if i, known := m.index[e.ID]; known {
			m.log[i].status = e.Status
			m.syncView()
		}
	}
}

// released is the Session's events channel closing: it is over for good,
// whatever ended it.
func (m *Model) released() {
	m.sess = nil
	m.prompt = nil
	m.link = 0
	m.call = session.NoCall
	m.remoteMic = false
	m.layout()
	if m.state == session.Idle {
		// It never got going — a refused Invite string, say. Whatever
		// explains that has already been said.
		return
	}
	if m.state != session.Disconnected && m.state != session.Failed {
		m.state, m.reason = session.Disconnected, session.ReasonNone
		m.add(notice("The Session is over."))
	}
}

// peerName is what to attribute the other side's messages to.
func (m Model) peerName() string {
	if m.peer == "" {
		return "them"
	}
	return m.peer
}

// add appends one entry to the conversation.
func (m *Model) add(e entry) {
	if e.id != "" {
		m.index[e.id] = len(m.log)
	}
	m.log = append(m.log, e)
	m.syncView()
}

// syncView re-renders the conversation, following it down only if the reader
// was already at the bottom — someone scrolled back is reading, not watching.
func (m *Model) syncView() {
	bottom := m.view.AtBottom()
	m.view.SetContent(render(m.log, m.view.Width))
	if bottom {
		m.view.GotoBottom()
	}
}

// layout hands the terminal's rows out. The status line and the key hints
// always keep one each. After that the Security Code prompt is served first —
// it is the one thing on screen that has to be read — then the message box,
// and the conversation takes what is left, which on a very short terminal is
// nothing at all.
func (m *Model) layout() {
	m.input.SetWidth(m.width)
	m.view.Width = m.width

	spare := max(m.height-2, 1)
	m.promptLimit = min(len(m.promptRows()), spare-1)
	spare -= m.promptLimit
	m.input.SetHeight(min(max(spare-1, 1), inputRows))
	m.view.Height = max(spare-m.input.Height(), 0)
	m.syncView()
}

// The messages the Model sends itself. A Session's events arrive as eventMsg
// until overMsg says there will be no more.
type (
	// eventMsg is one event from a Session, tagged with the Session it came
	// from so that a released Session cannot talk over its replacement.
	eventMsg struct {
		from  Session
		event session.Event
	}
	// overMsg is a Session's events channel closing.
	overMsg struct{ from Session }
	// noticeMsg is something a command run off the UI's goroutine has to say.
	noticeMsg struct{ text string }
	// closedMsg is the final teardown finishing, which is when the app may
	// actually exit.
	closedMsg struct{}
)

// waitEvent waits for one event, and is re-issued for each one after it.
func waitEvent(s Session) tea.Cmd {
	events := s.Events()
	return func() tea.Msg {
		e, ok := <-events
		if !ok {
			return overMsg{from: s}
		}
		return eventMsg{from: s, event: e}
	}
}

// hostCmd opens the Rendezvous off the UI's goroutine, since cloudflared
// takes seconds to come alive. A Session that could not host is closed rather
// than left holding a tunnel nobody knows about.
func hostCmd(s Session) tea.Cmd {
	return func() tea.Msg {
		if err := s.Host(context.Background()); err != nil {
			_ = s.Close()
			return noticeMsg{text: "Could not open a Rendezvous: " + err.Error()}
		}
		return nil
	}
}

// joinCmd dials the Invite. Join returns as soon as the Invite parses, and
// reports the rest of the attempt as events.
func joinCmd(s Session, invite string) tea.Cmd {
	return func() tea.Msg {
		if err := s.Join(context.Background(), invite); err != nil {
			_ = s.Close()
			return noticeMsg{text: "Could not connect: " + err.Error()}
		}
		return nil
	}
}

// closeCmd ends the Session and waits for it to finish letting go — the
// events channel closing — before the app exits. That wait is the only thing
// standing between quitting and an orphaned cloudflared.
func closeCmd(s Session) tea.Cmd {
	return func() tea.Msg {
		_ = s.Close()
		for range s.Events() {
		}
		return closedMsg{}
	}
}
