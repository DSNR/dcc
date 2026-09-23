package cli_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

func TestInviteHostsAndShowsTheInvite(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/invite")
	h.until("the Rendezvous to be opened", func() bool {
		hosts, _, _, _, _ := f.counts()
		return hosts == 1
	})

	f.emit(session.StateChanged{State: session.Hosting})
	f.emit(session.InviteReady{Invite: rendezvous.Invite{URL: "http://127.0.0.1:8080"}})
	h.mustSee("Hosting", testInvite)
}

// The Session runs once, from Invite to disconnect, so a second Invite has to
// be refused rather than quietly replacing the first.
func TestInviteWhileASessionIsRunningIsRefused(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/invite")
	f.emit(session.StateChanged{State: session.Hosting})
	h.mustSee("Hosting")

	h.submit("/invite")
	h.mustSee("/disconnect")
	if hosts, _, _, _, _ := f.counts(); hosts != 1 {
		t.Fatalf("hosted %d times, want 1", hosts)
	}
}

func TestConnectJoinsWithTheInviteAsTyped(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	h.until("the Invite to be handed to the Session", func() bool {
		_, joins, _, _, _ := f.counts()
		return joins == 1
	})
	if joined := f.joined(); joined[0] != testInvite {
		t.Fatalf("joined %q, want %q", joined[0], testInvite)
	}
}

func TestConnectWithoutAnInviteExplainsItself(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect")
	h.mustSee("/connect <invite>")
	if _, joins, _, _, _ := f.counts(); joins != 0 {
		t.Fatalf("joined %d times, want 0", joins)
	}
}

// The Security Code prompt is the Session's one blocking gate, and the TUI's
// job is to show the code, keep the question on screen, and send nothing
// until it is answered.
func TestSecurityCodePromptHoldsTextBack(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice"})

	// The code is shown in the groups it is meant to be read in, not as
	// forty undifferentiated digits.
	h.mustSee("11111 22222 33333 44444", "55555 66666 77777 88888", "Alice", "yes / no")

	h.submit("hello?")
	h.mustSee("Security Code")
	if sent := f.texts(); len(sent) != 0 {
		t.Fatalf("sent %q while the prompt stood, want nothing", sent)
	}
	if _, _, accepts, refuses, _ := f.counts(); accepts != 0 || refuses != 0 {
		t.Fatalf("prompt answered by itself: %d accepts, %d refuses", accepts, refuses)
	}
}

func TestYesAcceptsThePrompt(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice"})
	h.mustSee("yes / no")

	h.submit("yes")
	h.until("the prompt to be accepted", func() bool {
		_, _, accepts, _, _ := f.counts()
		return accepts == 1
	})
	// The question is answered, so it stops standing over the input.
	h.mustNotSee("yes / no")
}

func TestNoRefusesThePrompt(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice"})
	h.mustSee("yes / no")

	h.submit("no")
	h.until("the prompt to be refused", func() bool {
		_, _, _, refuses, _ := f.counts()
		return refuses == 1
	})
}

// A Display Name that comes back with a different Identity is the one thing
// in this interface that has to shout.
func TestChangedIdentityWarns(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice", Changed: true})

	h.mustSee("changed", "Alice")
}

func TestTypedTextIsSentAndCarriesItsStatus(t *testing.T) {
	h, f := connected(t)

	h.submit("Hello 👋")
	h.until("the message to be sent", func() bool { return len(f.texts()) == 1 })
	if sent := f.texts(); sent[0] != "Hello 👋" {
		t.Fatalf("sent %q, want %q", sent[0], "Hello 👋")
	}
	// Timestamped, attributed and pending, all before the wire says anything.
	h.mustSee(time.Now().Format("15:04"), "you", "Hello 👋")

	id := "00000000-0000-7000-8000-000000000001"
	f.emit(session.TextStatus{ID: id, Status: session.TextSent})
	h.mustSee("Hello 👋  ✓")
	f.emit(session.TextStatus{ID: id, Status: session.TextDelivered})
	h.mustSee("Hello 👋  ✓✓")
	f.emit(session.TextStatus{ID: id, Status: session.TextFailed})
	h.mustSee("not delivered")
}

func TestMultilineMessagesKeepTheirLines(t *testing.T) {
	h, f := connected(t)

	h.submit("first line\nsecond 🎉 line")
	h.until("the message to be sent", func() bool { return len(f.texts()) == 1 })
	if want := "first line\nsecond 🎉 line"; f.texts()[0] != want {
		t.Fatalf("sent %q, want %q", f.texts()[0], want)
	}
	h.mustSee("first line", "second 🎉 line")
}

func TestReceivedTextShowsWhoSaidIt(t *testing.T) {
	h, f := connected(t)

	f.emit(session.TextReceived{ID: "00000000-0000-7000-8000-00000000000a", Body: "hi there 😀", At: time.Now()})
	h.mustSee("Alice", "hi there 😀")
}

// A long message wraps into the conversation view rather than running off it.
func TestLongMessagesWrap(t *testing.T) {
	h, f := connected(t)

	long := strings.Repeat("wrap ", 60)
	f.emit(session.TextReceived{ID: "00000000-0000-7000-8000-00000000000b", Body: long, At: time.Now()})
	h.mustSee("wrap")
	for _, line := range strings.Split(h.view(), "\n") {
		if width := ansi.StringWidth(line); width > testWidth {
			t.Fatalf("line of %d columns on a %d-column terminal: %q", width, testWidth, line)
		}
	}
}

// The frame must be exactly the terminal's size, with and without the
// Security Code prompt standing: a row too many scrolls the whole screen on
// every keystroke.
func TestTheFrameFillsTheTerminalExactly(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	if rows := len(strings.Split(h.view(), "\n")); rows != testHeight {
		t.Fatalf("frame is %d rows, want %d", rows, testHeight)
	}

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice", Changed: true})
	h.mustSee("yes / no")
	if rows := len(strings.Split(h.view(), "\n")); rows != testHeight {
		t.Fatalf("frame with the prompt standing is %d rows, want %d", rows, testHeight)
	}

	h.do(tea.WindowSizeMsg{Width: 60, Height: 15})
	if rows := len(strings.Split(h.view(), "\n")); rows != 15 {
		t.Fatalf("frame after resizing is %d rows, want %d", rows, 15)
	}
}

func TestStatusLineFollowsTheLifecycle(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	// Idle, and saying how to leave it.
	h.mustSee("Idle")

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Connecting})
	h.mustSee("Connecting")

	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice"})
	h.mustSee("Verifying")

	h.submit("yes")
	f.emit(session.StateChanged{State: session.Connected})
	f.emit(session.LinkChanged{Link: transport.LinkRelayed})
	h.mustSee("Connected", "relayed", "Alice")

	f.emit(session.StateChanged{State: session.Disconnected, Reason: session.ReasonConnectionLost})
	h.mustSee("Disconnected", "connection lost")
}

func TestFailedSessionSaysWhy(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Failed, Reason: session.ReasonHandshakeFailed})
	h.mustSee("Failed", "handshake")
}

func TestDisconnectEndsTheSession(t *testing.T) {
	h, f := connected(t)

	h.submit("/disconnect")
	h.until("the Session to be closed", func() bool {
		_, _, _, _, closes := f.counts()
		return closes == 1
	})
	h.mustSee("Disconnected")

	// With no Session, text has nowhere to go and is not silently swallowed.
	h.submit("still here?")
	h.mustSee("/invite")
	if sent := f.texts(); len(sent) != 0 {
		t.Fatalf("sent %q after disconnecting, want nothing", sent)
	}
}

// Quitting must not leave a cloudflared process behind, so it waits for the
// Session's teardown — the events channel closing — before exiting.
func TestQuitClosesTheSessionBeforeExiting(t *testing.T) {
	h, f := connected(t)

	h.submit("/quit")
	h.until("the Session to be closed", func() bool {
		_, _, _, _, closes := f.counts()
		return closes == 1
	})
	h.until("the program to exit", h.quitting)
}

func TestQuitWithNoSessionExitsAtOnce(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/quit")
	h.until("the program to exit", h.quitting)
}

func TestCtrlCQuits(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.do(tea.KeyMsg{Type: tea.KeyCtrlC})
	h.until("the program to exit", h.quitting)
}

func TestHelpListsTheCommands(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/help")
	h.mustSee("/invite", "/connect", "/disconnect", "/quit", "/help")
}

// Bare words are commands while there is nothing to talk to, and message text
// once there is — which is what makes chatting frictionless without making
// the word "invite" unsayable.
func TestBareWordsAreCommandsUntilConnected(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("invite")
	h.until("the Rendezvous to be opened", func() bool {
		hosts, _, _, _, _ := f.counts()
		return hosts == 1
	})

	h.mustNotSee("isn't a command")
}

func TestBareCommandWordsAreMessagesOnceConnected(t *testing.T) {
	h, f := connected(t)

	h.submit("invite")
	h.until("the word to be sent as a message", func() bool { return len(f.texts()) == 1 })
	if sent := f.texts(); sent[0] != "invite" {
		t.Fatalf("sent %q, want %q", sent[0], "invite")
	}
}

// Slash always means a command, so a message that has to start with one has
// /msg to get out through.
func TestMsgSendsTextThatLooksLikeACommand(t *testing.T) {
	h, f := connected(t)

	h.submit("/msg /invite")
	h.until("the message to be sent", func() bool { return len(f.texts()) == 1 })
	if sent := f.texts(); sent[0] != "/invite" {
		t.Fatalf("sent %q, want %q", sent[0], "/invite")
	}
}

func TestUnknownCommandIsSaidSo(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/frobnicate")
	h.mustSee("/frobnicate", "isn't a command")
}

func TestRefusedTextIsReportedNotLost(t *testing.T) {
	h, f := connected(t)

	f.mu.Lock()
	f.sendErr = errors.New("wire: body is 20000 bytes, over the 16384 byte cap")
	f.mu.Unlock()

	h.submit("far too much")
	h.mustSee("Not sent", "over the 16384 byte cap")
}

// An Identity that had to be reset is the one warning that must be in front
// of the participant before anything else happens.
func TestStartupWarningsAreShown(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{
		Name:     "scott",
		New:      oneSession(f),
		Warnings: []string{"Your dcc Identity file was damaged"},
	})

	h.mustSee("Your dcc Identity file was damaged")
}

func TestSessionThatCannotBeCreatedIsReported(t *testing.T) {
	h := newHarness(t, cli.Options{
		Name: "scott",
		New:  func() (cli.Session, error) { return nil, errors.New("no certificate for you") },
	})

	h.submit("/invite")
	h.mustSee("no certificate for you")
}

func TestHostFailureIsReported(t *testing.T) {
	f := newFake()
	f.hostErr = errors.New("rendezvous: cloudflared is not installed")
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/invite")
	h.mustSee("cloudflared is not installed")

	// The dead Session is let go of, so hosting can be tried again.
	h.until("the Session to be released", func() bool {
		_, _, _, _, closes := f.counts()
		return closes == 1
	})
}
