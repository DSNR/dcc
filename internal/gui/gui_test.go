package gui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
)

// TestOpensWithWelcomeAndWarnings: the window is readable before anything has
// happened, and an Identity that had to be reset is said at once rather than
// buried.
func TestOpensWithWelcomeAndWarnings(t *testing.T) {
	m, _ := newModel(t, gui.Options{Warnings: []string{"your Identity was reset"}})

	s := m.Screen()
	if !strings.Contains(transcript(s), "end-to-end encrypted chat") {
		t.Errorf("no welcome in the conversation:\n%s", transcript(s))
	}
	if !strings.Contains(transcript(s), "your Identity was reset") {
		t.Errorf("the startup warning was not shown:\n%s", transcript(s))
	}
	if !s.Controls.Host || !s.Controls.Join {
		t.Errorf("hosting and joining should be available with no Session: %+v", s.Controls)
	}
	if s.Controls.Send || s.Controls.Disconnect {
		t.Errorf("sending and disconnecting should be dead with no Session: %+v", s.Controls)
	}
	if !strings.Contains(s.Status, `you are "me"`) {
		t.Errorf("the status bar does not say who this side is: %q", s.Status)
	}
}

// TestHostShowsTheInvite: hosting opens a Rendezvous and puts the Invite
// where it can be copied out, which is the Host's whole job.
func TestHostShowsTheInvite(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	waitSaid(t, m, "Opening a Rendezvous")
	fake.post(t, session.StateChanged{State: session.Hosting})
	fake.post(t, session.InviteReady{Invite: rendezvous.Invite{URL: "http://example.invalid"}})

	s := waitFor(t, m, "the Invite", func(s gui.Screen) bool { return s.Invite != "" })
	if !strings.Contains(s.Invite, "http://example.invalid") {
		t.Errorf("the Invite is not the one the Session gave: %q", s.Invite)
	}
	if !strings.Contains(transcript(s), s.Invite) {
		t.Errorf("the Invite is not in the conversation to be copied:\n%s", transcript(s))
	}
	if hosts, _, _, _, _, _ := fake.counts(); hosts != 1 {
		t.Errorf("Host was called %d times, want 1", hosts)
	}
	if s.Controls.Host || s.Controls.Join {
		t.Errorf("hosting and joining should be dead while a Session runs: %+v", s.Controls)
	}
	if !s.Controls.Disconnect {
		t.Error("disconnecting should be available while a Session runs")
	}
}

// TestHostFailureIsSaid: cloudflared not coming up is the commonest thing
// that goes wrong, and it has to be legible rather than a window that sits
// there.
func TestHostFailureIsSaid(t *testing.T) {
	fake := newFake()
	fake.hostErr = errBroken
	m, _ := newModel(t, gui.Options{New: func() (gui.Session, error) { return fake, nil }})

	m.Host()
	waitSaid(t, m, "Could not open a Rendezvous: broken")
	waitFor(t, m, "hosting to be offered again", func(s gui.Screen) bool { return s.Controls.Host })
}

// TestJoinNeedsAnInvite: the connect button with an empty box says what is
// missing instead of starting a Session that cannot go anywhere.
func TestJoinNeedsAnInvite(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Join("   \n")
	waitSaid(t, m, "Paste the Invite")
	if _, _, _, _, _, joins := fake.counts(); len(joins) != 0 {
		t.Errorf("a blank box was dialled as %q", joins)
	}

	m.Join("  " + testInvite + "\n")
	waitFor(t, m, "the Invite to be dialled", func(gui.Screen) bool {
		_, _, _, _, _, joins := fake.counts()
		return len(joins) == 1
	})
	if _, _, _, _, _, joins := fake.counts(); joins[0] != testInvite {
		t.Errorf("the Session was asked to join %q, want the Invite trimmed", joins[0])
	}
}

// TestOneSessionAtATime: a second Invite while one is running would abandon a
// live Rendezvous, so it is refused out loud.
func TestOneSessionAtATime(t *testing.T) {
	m, _ := newModel(t, gui.Options{})

	m.Host()
	waitSaid(t, m, "Opening a Rendezvous")
	m.Join(testInvite)
	waitSaid(t, m, "A Session is already running")
}

// TestSecurityCodePromptBlocksContent: the prompt stands until it is
// answered, nothing can be sent behind it, and answering it is what lets
// content flow.
func TestSecurityCodePromptBlocksContent(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.post(t, session.StateChanged{State: session.Verifying})
	fake.post(t, session.VerifyPrompt{Code: testCode, Name: "Ada", Peer: testPeer})

	s := waitFor(t, m, "the Security Code prompt", func(s gui.Screen) bool { return s.Prompt != nil })
	if got, want := len(s.Prompt.Groups), 8; got != want {
		t.Errorf("the code is shown in %d groups, want %d", got, want)
	}
	if s.Prompt.Name != "Ada" || s.Prompt.Changed {
		t.Errorf("the prompt names %q, changed=%v", s.Prompt.Name, s.Prompt.Changed)
	}
	if !strings.Contains(s.Status, "Security Code unanswered") {
		t.Errorf("the status bar does not carry the standing prompt: %q", s.Status)
	}
	if s.Controls.Send {
		t.Error("messages must not be sendable while the Security Code is unanswered")
	}

	if sent := m.Send("hello"); sent {
		t.Error("Send claimed to have sent a message behind an unanswered prompt")
	}
	waitSaid(t, m, "Check the Security Code")

	m.Accept()
	s = waitFor(t, m, "the prompt to come down", func(s gui.Screen) bool { return s.Prompt == nil })
	if !strings.Contains(transcript(s), `Accepted — "Ada" is verified`) {
		t.Errorf("accepting was not recorded in the conversation:\n%s", transcript(s))
	}
	if _, accepts, _, _, _, _ := fake.counts(); accepts != 1 {
		t.Errorf("the Session was accepted %d times, want 1", accepts)
	}
}

// TestChangedIdentityIsWarnedAbout: a Peer whose Identity changed is the one
// case where the right answer is probably no, so the prompt says so.
func TestChangedIdentityIsWarnedAbout(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.post(t, session.StateChanged{State: session.Verifying})
	fake.post(t, session.VerifyPrompt{Code: testCode, Name: "Ada", Peer: testPeer, Changed: true})

	s := waitFor(t, m, "the Security Code prompt", func(s gui.Screen) bool { return s.Prompt != nil })
	if !s.Prompt.Changed {
		t.Error("the prompt does not know the Identity changed")
	}
	if !strings.Contains(strings.Join(s.Prompt.Lines, "\n"), "their Identity has changed") {
		t.Errorf("the prompt does not warn about the changed Identity: %q", s.Prompt.Lines)
	}
}

// TestRefuseEndsTheSession: refusing turns the other side away, and says that
// nothing they sent was shown.
func TestRefuseEndsTheSession(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.post(t, session.StateChanged{State: session.Verifying})
	fake.post(t, session.VerifyPrompt{Code: testCode, Name: "Mallory", Peer: testPeer})
	waitFor(t, m, "the Security Code prompt", func(s gui.Screen) bool { return s.Prompt != nil })

	m.Refuse()
	waitSaid(t, m, `Nothing "Mallory" sent was shown`)
	if _, _, refuses, _, _, _ := fake.counts(); refuses != 1 {
		t.Errorf("the Session was refused %d times, want 1", refuses)
	}
}

// TestSendAndDeliveryStatus: a message this side sent appears at once, and
// moves through its delivery statuses in place.
func TestSendAndDeliveryStatus(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")

	if sent := m.Send("hello there"); !sent {
		t.Fatal("Send refused a message on a Connected Session")
	}
	s := waitSaid(t, m, "hello there")
	last := s.Entries[len(s.Entries)-1]
	if last.Who != "you" || !last.Mine || last.Marker() != "…" {
		t.Errorf("a just-sent message reads as %+v, want mine and pending", last)
	}

	fake.post(t, session.TextStatus{ID: "msg-hello there", Status: session.TextDelivered})
	waitFor(t, m, "the message to read as delivered", func(s gui.Screen) bool {
		return strings.Contains(transcript(s), "hello there  ✓✓")
	})

	if _, _, _, _, sent, _ := fake.counts(); len(sent) != 1 || sent[0] != "hello there" {
		t.Errorf("the Session was given %q to send", sent)
	}
}

// TestSendFailureKeepsTheMessage: a message the Session would not take is
// reported, and Send says it did not go so the window can keep the text.
func TestSendFailureKeepsTheMessage(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")
	fake.mu.Lock()
	fake.sendErr = errBroken
	fake.mu.Unlock()

	if sent := m.Send("lost"); sent {
		t.Error("Send claimed to have sent a message the Session refused")
	}
	waitSaid(t, m, "Not sent: broken")
}

// TestReceivedTextIsAttributed: what the other side said is labelled with the
// Display Name they announced, and carries no delivery marker of its own.
func TestReceivedTextIsAttributed(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")
	fake.post(t, session.TextReceived{ID: "in-1", Body: "hi 👋", At: time.Now()})

	s := waitSaid(t, m, "hi 👋")
	last := s.Entries[len(s.Entries)-1]
	if last.Who != "Ada" || last.Mine || last.Marker() != "" {
		t.Errorf("a received message reads as %+v, want theirs and unmarked", last)
	}
}

// TestDisconnectEndsTheSessionNotTheApp: disconnecting leaves the window up
// with its history, ready to host again.
func TestDisconnectEndsTheSessionNotTheApp(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")
	m.Disconnect()

	s := waitFor(t, m, "the Session to be released", func(s gui.Screen) bool { return s.Controls.Host })
	if s.Controls.Send || s.Controls.Disconnect {
		t.Errorf("a released Session still offers its controls: %+v", s.Controls)
	}
	if s.Invite != "" {
		t.Errorf("a released Session still offers an Invite: %q", s.Invite)
	}
	if !strings.Contains(transcript(s), "The Session is over.") {
		t.Errorf("the end of the Session was not said:\n%s", transcript(s))
	}
	select {
	case <-m.Done():
		t.Error("disconnecting closed the window; it should only end the Session")
	default:
	}
}

// TestLinkIsReported: whether content is going direct or through the relay is
// worth knowing, and is not a security warning either way.
func TestLinkIsReported(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")
	fake.post(t, session.LinkChanged{Link: 2})
	waitFor(t, m, "the link on the status bar", func(s gui.Screen) bool {
		return strings.Contains(s.Status, "relayed") || strings.Contains(s.Status, "direct")
	})
}

// TestQuitWaitsForTheSession: leaving must not outlive the Rendezvous, so the
// window only goes once the Session has let everything go.
func TestQuitWaitsForTheSession(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")

	m.Quit()
	select {
	case <-m.Done():
	case <-time.After(waitTimeout):
		t.Fatal("the window never finished closing")
	}
	if _, _, _, closes, _, _ := fake.counts(); closes != 1 {
		t.Errorf("the Session was closed %d times, want 1", closes)
	}
}

// TestHistoryPanelListsAndOpens: stored Conversations are readable from the
// window with nobody connected, which is what makes history a feature rather
// than a file.
func TestHistoryPanelListsAndOpens(t *testing.T) {
	store := newStore()
	when := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	store.add(
		storage.Conversation{Peer: testPeer, Name: "Ada", Messages: 2, LastAt: when},
		storage.Message{ID: "1", Mine: true, Body: "morning", At: when, Status: session.TextDelivered},
		storage.Message{ID: "2", Body: "morning yourself", At: when.Add(time.Minute)},
	)
	m, _ := newModel(t, gui.Options{Store: store})

	s := waitFor(t, m, "the history panel to list a Conversation", func(s gui.Screen) bool {
		return len(s.Conversations) == 1
	})
	if s.Conversations[0].Name != "Ada" || !strings.Contains(s.Conversations[0].Summary, "2 messages") {
		t.Errorf("the panel lists %+v", s.Conversations[0])
	}

	m.Open(s.Conversations[0])
	s = waitSaid(t, m, "morning yourself")
	if !strings.Contains(transcript(s), "1 Mar 2026") {
		t.Errorf("history spanning a day carries no date line:\n%s", transcript(s))
	}
	if !strings.Contains(transcript(s), "morning  ✓✓") {
		t.Errorf("a stored message lost its delivery status:\n%s", transcript(s))
	}
}

// TestClearHistoryDeletesOneConversation: clearing is this device's copy
// only, and the panel reflects it without being asked again.
func TestClearHistoryDeletesOneConversation(t *testing.T) {
	store := newStore()
	store.add(storage.Conversation{Peer: testPeer, Name: "Ada", Messages: 1, LastAt: time.Now()})
	store.add(storage.Conversation{Peer: identity.PublicKey{9}, Name: "Bob", Messages: 1, LastAt: time.Now()})
	m, _ := newModel(t, gui.Options{Store: store})

	s := waitFor(t, m, "both Conversations", func(s gui.Screen) bool { return len(s.Conversations) == 2 })
	m.Clear(s.Conversations[0])

	s = waitFor(t, m, "the cleared Conversation to go", func(s gui.Screen) bool { return len(s.Conversations) == 1 })
	if s.Conversations[0].Name != "Bob" {
		t.Errorf("the wrong Conversation was cleared; %q is left", s.Conversations[0].Name)
	}
	if !said(m, `Deleted the Conversation with "Ada"`) {
		t.Errorf("clearing was not confirmed:\n%s", transcript(s))
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.cleared) != 1 || store.cleared[0] != testPeer {
		t.Errorf("the Store was asked to clear %v", store.cleared)
	}
}

// TestAcceptRestoresTheConversation: accepting a Peer puts what was said with
// them back on screen, so a restart resumes rather than starts over.
func TestAcceptRestoresTheConversation(t *testing.T) {
	store := newStore()
	store.add(
		storage.Conversation{Peer: testPeer, Name: "Ada", Messages: 1, LastAt: time.Now()},
		storage.Message{ID: "1", Body: "from last time", At: time.Now().Add(-time.Hour)},
	)
	m, fake := newModel(t, gui.Options{Store: store})

	m.Host()
	fake.connect(t, m, "Ada")
	waitSaid(t, m, "from last time")
}

// TestWithoutAStoreHistoryIsHonest: a run with no storage says so rather than
// showing an empty panel that looks like a lost Conversation.
func TestWithoutAStoreHistoryIsHonest(t *testing.T) {
	m, _ := newModel(t, gui.Options{})

	m.Open(gui.Conversation{Name: "Ada"})
	waitSaid(t, m, "No history is kept in this run.")
	if got := len(m.Screen().Conversations); got != 0 {
		t.Errorf("the panel lists %d Conversations without a Store", got)
	}
}

// TestUnreadableHistoryIsSaid: a Store that will not read is reported where
// the participant is looking, not swallowed.
func TestUnreadableHistoryIsSaid(t *testing.T) {
	store := newStore()
	store.readErr = errBroken
	m, _ := newModel(t, gui.Options{Store: store})

	waitSaid(t, m, "Could not read the history: broken")
}

// TestRepaintIsAskedFor: the window is only redrawn when it is asked to be,
// so every change to what is on screen has to ask.
func TestRepaintIsAskedFor(t *testing.T) {
	painted := make(chan struct{}, 64)
	m, fake := newModel(t, gui.Options{Repaint: func() {
		select {
		case painted <- struct{}{}:
		default:
		}
	}})

	m.Host()
	fake.post(t, session.StateChanged{State: session.Hosting})
	select {
	case <-painted:
	case <-time.After(waitTimeout):
		t.Fatal("a Session event never asked for a repaint")
	}
}

// TestStatusThatBeatsItsMessage: sending does not hold the Model's lock while
// the Session takes the message, so a delivery acknowledgement can arrive
// before the message it belongs to is on screen. It must still land on it.
func TestStatusThatBeatsItsMessage(t *testing.T) {
	m, fake := newModel(t, gui.Options{})

	m.Host()
	fake.connect(t, m, "Ada")
	fake.post(t, session.TextStatus{ID: "msg-early", Status: session.TextDelivered})
	// The status has nothing to land on yet. Giving it a moment to be taken
	// in is what makes this test the race it is about.
	time.Sleep(20 * tick)

	if sent := m.Send("early"); !sent {
		t.Fatal("Send refused a message on a Connected Session")
	}
	waitFor(t, m, "the message to read as delivered", func(s gui.Screen) bool {
		return strings.Contains(transcript(s), "early  ✓✓")
	})
}

// TestAConversationIsReadInOnce: the Read button on a Conversation already on
// screen says where it is rather than printing it again, which would read as
// the same things having been said twice.
func TestAConversationIsReadInOnce(t *testing.T) {
	store := newStore()
	store.add(
		storage.Conversation{Peer: testPeer, Name: "Ada", Messages: 1, LastAt: time.Now()},
		storage.Message{ID: "1", Body: "said once", At: time.Now()},
	)
	m, _ := newModel(t, gui.Options{Store: store})

	s := waitFor(t, m, "the history panel", func(s gui.Screen) bool { return len(s.Conversations) == 1 })
	m.Open(s.Conversations[0])
	waitSaid(t, m, "said once")

	m.Open(s.Conversations[0])
	s = waitSaid(t, m, "is already above")
	if got := strings.Count(transcript(s), "said once"); got != 1 {
		t.Errorf("the message appears %d times, want once:\n%s", got, transcript(s))
	}
}
