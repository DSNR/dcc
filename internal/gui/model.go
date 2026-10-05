package gui

import (
	"context"
	"fmt"
	"image"
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

	// call is where the Call inside the Session stands; theirMic, theirCam
	// and theirScreen are what the other side last announced about their own
	// streams. All four mean nothing while there is no Call.
	call        session.CallState
	theirMic    bool
	theirCam    bool
	theirScreen bool
	// camPic, screenPic and myPic are the newest picture of each of a Call's
	// three streams: the other side's camera, their shared screen, and this
	// side's own camera. videoStop ends the goroutines that keep them, and is
	// nil whenever there is no Call to keep them for.
	camPic    *image.RGBA
	screenPic *image.RGBA
	myPic     *image.RGBA
	videoStop chan struct{}

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
	// What this side is sending is the Session's to answer for — a camera can
	// release itself — so it is read from the Session, before the lock rather
	// than under it: nothing here calls into a Session holding this Model's
	// lock. The cost is that the answer can be a frame out of date, which the
	// repaint that follows any change puts right.
	own := m.ownStreams()

	m.mu.Lock()
	defer m.mu.Unlock()

	entries := make([]Entry, len(m.log))
	copy(entries, m.log)
	conversations := make([]Conversation, len(m.conversations))
	copy(conversations, m.conversations)

	connected := m.sess != nil && m.state == session.Connected && m.prompt == nil && !m.closing
	s := Screen{
		Name:          m.name,
		Status:        m.statusLine(),
		Peer:          m.peer,
		Entries:       entries,
		Invite:        m.invite,
		Conversations: conversations,
		Closing:       m.closing,
		Call:          m.callView(own),
		Controls: Controls{
			Host:       m.sess == nil && !m.closing,
			Join:       m.sess == nil && !m.closing,
			Send:       connected,
			Disconnect: m.sess != nil && !m.closing,
			Call:       connected && m.call == session.NoCall,
			Answer:     m.call == session.Incoming && !m.closing,
			Reject:     m.call == session.Incoming && !m.closing,
			Hangup:     m.call != session.NoCall && !m.closing,
			Mute:       m.call != session.NoCall && !m.closing,
			Camera:     m.call == session.Active && !m.closing,
			Share:      m.call == session.Active && !m.closing,
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

// Call rings the other person. What comes of it arrives as events, so there is
// nothing to report here beyond the ringing having started.
func (m *Model) Call() {
	m.mu.Lock()
	s, peer := m.sess, m.peerName()
	switch {
	case m.prompt != nil:
		m.add(notice("Check the Security Code with the other person first."))
		m.mu.Unlock()
		m.changed()
		return
	case m.closing || s == nil || m.state != session.Connected:
		m.add(notice("There is nobody to call — host a Session, or connect to an Invite first."))
		m.mu.Unlock()
		m.changed()
		return
	case m.call != session.NoCall:
		m.add(notice("There is already a Call — hang up to end it first."))
		m.mu.Unlock()
		m.changed()
		return
	}
	m.mu.Unlock()

	if _, err := s.Call(); err != nil {
		m.say("Could not call: " + err.Error())
		return
	}
	m.say("Calling " + words.Quoted(peer) + " — hang up to give up.")
}

// Answer picks up the Call ringing here. The media comes up behind it, which
// the Call's own events announce.
func (m *Model) Answer() {
	s, ok := m.ringing("There is no Call to answer.")
	if !ok {
		return
	}
	if err := s.Answer(); err != nil {
		m.say("Could not answer: " + err.Error())
	}
}

// Reject turns down the Call ringing here, leaving the Session up for text.
func (m *Model) Reject() {
	s, ok := m.ringing("There is no Call to reject.")
	if !ok {
		return
	}
	if err := s.Reject(); err != nil {
		m.say("Could not reject: " + err.Error())
	}
}

// Hangup ends the Call — answered, still ringing here, or still ringing there
// — and leaves the Session connected for text.
func (m *Model) Hangup() {
	s, ok := m.inCall("There is no Call to hang up. Disconnect ends the Session.")
	if !ok {
		return
	}
	if err := s.Hangup(); err != nil {
		m.say(err.Error())
	}
}

// Mute stops or resumes sending this side's microphone. The other side is told
// either way: a muted microphone they cannot see is how people end up talking
// to nobody.
func (m *Model) Mute(muted bool) {
	s, ok := m.inCall("There is no Call to mute.")
	if !ok {
		return
	}
	if err := s.Mute(muted); err != nil {
		m.say(err.Error())
		return
	}
	if muted {
		m.say("Microphone muted — they can see that you are.")
		return
	}
	m.say("Microphone live.")
}

// Camera turns this side's camera on or off. Opening a camera takes a moment,
// so it happens off the window's goroutine — a window that stopped painting
// while a webcam woke up would look broken.
func (m *Model) Camera(on bool) {
	s, ok := m.inCall("There is no Call to turn a camera on in.")
	if !ok {
		return
	}
	go func() {
		if err := s.Camera(on); err != nil {
			m.say("Camera: " + err.Error())
			return
		}
		if on {
			m.say("Camera on — they can see you.")
			return
		}
		m.say("Camera off — the device is released.")
	}()
}

// Share starts or stops sharing this side's entire screen. Like the camera it
// opens a device, so it too happens off the window's goroutine.
func (m *Model) Share(on bool) {
	s, ok := m.inCall("There is no Call to share a screen into.")
	if !ok {
		return
	}
	go func() {
		if err := s.Share(on); err != nil {
			m.say("Screen share: " + err.Error())
			return
		}
		if on {
			m.say("Sharing your whole screen — Stop sharing ends it.")
			return
		}
		m.say("Screen share stopped — the display is released.")
	}()
}

// inCall is the Session a Call control is for, or the refusal said out loud
// where there is no Call for it to be about.
func (m *Model) inCall(refusal string) (Session, bool) {
	return m.forCall(refusal, func(c session.CallState) bool { return c != session.NoCall })
}

// ringing is inCall for the two controls that only mean anything while a Call
// is ringing here.
func (m *Model) ringing(refusal string) (Session, bool) {
	return m.forCall(refusal, func(c session.CallState) bool { return c == session.Incoming })
}

// forCall is what both of those are made of: the Session, if the Call is in a
// state the control means anything in, and the refusal said out loud if it is
// not. Every control the window greys out still checks — a Screen is a
// snapshot, and a Call can end between one being painted and a button on it
// being pressed.
func (m *Model) forCall(refusal string, usable func(session.CallState) bool) (Session, bool) {
	m.mu.Lock()
	s := m.sess
	if s == nil || !usable(m.call) {
		m.add(notice(refusal))
		m.mu.Unlock()
		m.changed()
		return nil, false
	}
	m.mu.Unlock()
	return s, true
}

// ownStreams is what this side is sending, asked of the Session itself rather
// than mirrored here, so that a device that stopped on its own reads as off.
// It takes no lock of its own while asking.
func (m *Model) ownStreams() streams {
	m.mu.Lock()
	s, running := m.sess, m.call != session.NoCall
	m.mu.Unlock()
	if s == nil || !running {
		return streams{}
	}
	return streams{muted: s.Muted(), camera: s.CameraOn(), screen: s.Sharing()}
}

// streams is this side's own microphone, camera and screen share.
type streams struct{ muted, camera, screen bool }

// callView is the Call as the window shows it. It is called with the lock
// held.
func (m *Model) callView(own streams) Call {
	c := Call{
		State:       m.call,
		Muted:       own.muted,
		CameraOn:    own.camera,
		Sharing:     own.screen,
		TheirMic:    m.theirMic,
		TheirCam:    m.theirCam,
		TheirScreen: m.theirScreen,
	}
	if m.call == session.Active {
		// What somebody is pointing at is what the other person needs to see,
		// so a share takes the large picture over while it runs; a stream
		// nobody has turned on shows nothing, whatever its last picture was.
		switch {
		case m.theirScreen:
			c.Large = m.screenPic
		case m.theirCam:
			c.Large = m.camPic
		}
		if own.camera {
			c.Small = m.myPic
		}
	}
	if c.Video() && c.Large == nil {
		c.Waiting = waiting(c)
	}
	c.Status = callStatus(c, m.peerName())
	return c
}

// watchVideo follows a Call's three streams, keeping the newest picture of
// each in front of the window. It is called with the lock held.
func (m *Model) watchVideo(s Session) {
	if m.videoStop != nil {
		return
	}
	stop := make(chan struct{})
	m.videoStop = stop
	go m.pumpFrames(s.Frames(), stop, func(img *image.RGBA) { m.camPic = img })
	go m.pumpFrames(s.ScreenFrames(), stop, func(img *image.RGBA) { m.screenPic = img })
	go m.pumpFrames(s.LocalFrames(), stop, func(img *image.RGBA) { m.myPic = img })
}

// stopVideo stops following a Call's streams and forgets its pictures, which
// is what the end of a Call does to them. It is called with the lock held.
func (m *Model) stopVideo() {
	if m.videoStop != nil {
		close(m.videoStop)
		m.videoStop = nil
	}
	m.camPic, m.screenPic, m.myPic = nil, nil, nil
}

// pumpFrames keeps one stream's newest picture and asks for a repaint. Only
// the newest is kept: a window painting at its own rate should show the
// current picture rather than work through stale ones. A picture still in hand
// when the Call it belongs to ends is dropped — the Call it would have been
// painted in is over, and the next one must not open on it.
func (m *Model) pumpFrames(frames <-chan *image.RGBA, stop chan struct{}, keep func(*image.RGBA)) {
	for {
		select {
		case img := <-frames:
			m.mu.Lock()
			if m.videoStop != stop {
				m.mu.Unlock()
				return
			}
			keep(img)
			m.mu.Unlock()
			m.changed()
		case <-stop:
			return
		}
	}
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

	case session.CallChanged:
		m.call = e.State
		// A Call starts with both microphones live and every camera off; the
		// other side's own media state follows and corrects this if it does
		// not.
		m.theirMic = e.State == session.Active
		m.theirCam, m.theirScreen = false, false
		if said := callNotice(e, m.peerName()); said != "" {
			m.add(notice(said))
		}
		switch e.State {
		case session.Active:
			m.watchVideo(m.sess)
		case session.NoCall:
			m.stopVideo()
		}

	case session.MediaChanged:
		if m.theirMic != e.Mic {
			m.theirMic = e.Mic
			m.add(notice(words.Mic(e.Mic, m.peerName())))
		}
		if m.theirCam != e.Cam {
			m.theirCam = e.Cam
			m.add(notice(words.Cam(e.Cam, m.peerName())))
		}
		if m.theirScreen != e.Screen {
			m.theirScreen = e.Screen
			m.add(notice(words.Share(e.Screen, m.peerName(), "the video area")))
		}

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
	m.call = session.NoCall
	m.theirMic, m.theirCam, m.theirScreen = false, false, false
	m.stopVideo()
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
