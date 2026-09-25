package cli_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

// waitTimeout bounds every wait for something to appear on screen. Nothing
// here talks to a network; hitting this means something is wedged.
const waitTimeout = 10 * time.Second

// tick is how long a wait sleeps between looks when no message is pending.
const tick = 5 * time.Millisecond

// The terminal the tests pretend to be: wide enough that an Invite fits on
// one line, so assertions can look for it whole.
const (
	testWidth  = 100
	testHeight = 30
)

// TestMain forces plain output, so that what these tests read is exactly what
// a participant on a colourless terminal reads.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

// testInvite is a well-formed Invite for a Rendezvous that does not exist.
// The TUI never parses it — the Session does — so a valid shape is all the
// tests need.
var testInvite = rendezvous.Invite{URL: "http://127.0.0.1:8080"}.String()

// testCode stands in for a derived Security Code: forty digits in the eight
// groups the prompt lays out.
const testCode = identity.SecurityCode("1111122222333334444455555666667777788888")

// fakeSession stands in for *session.Session. It records the commands the TUI
// gives it and lets a test post the events a real Session would raise, so the
// terminal interface is tested without a Rendezvous, a handshake or a
// PeerConnection underneath it.
type fakeSession struct {
	events chan session.Event

	mu      sync.Mutex
	hosts   int
	joins   []string
	accepts int
	refuses int
	sent    []string
	closes  int
	calls   int
	answers int
	rejects int
	hangups int
	mutes   []bool
	muted   bool

	// hostErr, joinErr, sendErr and callErr are what the next matching
	// command returns; zero means it succeeds.
	hostErr, joinErr, sendErr, callErr error
}

func newFake() *fakeSession {
	return &fakeSession{events: make(chan session.Event, 64)}
}

func (f *fakeSession) Events() <-chan session.Event { return f.events }

func (f *fakeSession) Host(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts++
	return f.hostErr
}

func (f *fakeSession) Join(_ context.Context, invite string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.joins = append(f.joins, invite)
	return f.joinErr
}

func (f *fakeSession) Accept() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepts++
	return nil
}

func (f *fakeSession) Refuse() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuses++
	return nil
}

func (f *fakeSession) SendText(body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sent = append(f.sent, body)
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", len(f.sent)), nil
}

func (f *fakeSession) Call() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return "", f.callErr
	}
	f.calls++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", f.calls), nil
}

func (f *fakeSession) Answer() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers++
	return nil
}

func (f *fakeSession) Reject() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejects++
	return nil
}

func (f *fakeSession) Hangup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangups++
	return nil
}

func (f *fakeSession) Mute(muted bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutes = append(f.mutes, muted)
	f.muted = muted
	return nil
}

func (f *fakeSession) Muted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muted
}

// callCounts is what the TUI asked of the Call controls.
func (f *fakeSession) callCounts() (calls, answers, rejects, hangups int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.answers, f.rejects, f.hangups
}

// Close ends the fake the way a real Session does: the events channel closing
// is the last thing teardown does, and it is what tells the UI the Session is
// over for good.
func (f *fakeSession) Close() error {
	f.mu.Lock()
	first := f.closes == 0
	f.closes++
	f.mu.Unlock()
	if first {
		close(f.events)
	}
	return nil
}

// emit posts one event, as a live Session would.
func (f *fakeSession) emit(e session.Event) { f.events <- e }

func (f *fakeSession) counts() (hosts, joins, accepts, refuses, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts, len(f.joins), f.accepts, f.refuses, f.closes
}

func (f *fakeSession) joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.joins...)
}

func (f *fakeSession) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// oneSession hands the TUI the same fake every time it asks for a Session.
func oneSession(f *fakeSession) cli.NewSession {
	return func() (cli.Session, error) { return f, nil }
}

// harness runs a Model the way bubbletea would: it applies messages one at a
// time on the test's own goroutine, and runs the commands that come back on
// their own, feeding their messages back in.
type harness struct {
	t     *testing.T
	model tea.Model
	msgs  chan tea.Msg

	// siblings are the other terminals sharing this test's goroutine. Waiting
	// on one terminal keeps the others' messages flowing too, since a Model
	// that is not pumped never runs the commands it asked for.
	siblings []*harness

	once sync.Once
	quit chan struct{}
}

// link makes two terminals pump each other, which is what lets one test drive
// both ends of a Session.
func link(a, b *harness) {
	a.siblings = append(a.siblings, b)
	b.siblings = append(b.siblings, a)
}

func newHarness(t *testing.T, opts cli.Options) *harness {
	t.Helper()
	h := &harness{t: t, msgs: make(chan tea.Msg, 256), quit: make(chan struct{})}
	h.model = cli.New(opts)
	h.run(h.model.Init())
	h.do(tea.WindowSizeMsg{Width: testWidth, Height: testHeight})
	return h
}

// do applies one message to the model.
func (h *harness) do(msg tea.Msg) {
	switch msg := msg.(type) {
	case tea.QuitMsg:
		h.once.Do(func() { close(h.quit) })
		return
	case tea.BatchMsg:
		for _, cmd := range msg {
			h.run(cmd)
		}
		return
	}
	m, cmd := h.model.Update(msg)
	h.model = m
	h.run(cmd)
}

// run performs one command off the test's goroutine, since a command is
// exactly the work a Model must not block on.
func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if msg := cmd(); msg != nil {
			h.msgs <- msg
		}
	}()
}

// until applies whatever arrives until want reports true, and fails with the
// screen if it never does.
func (h *harness) until(what string, want func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if want() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s\n--- screen ---\n%s", what, h.view())
		}
		if !h.pump() {
			time.Sleep(tick)
		}
	}
}

// pump applies one pending message, from this terminal or any it is linked to,
// and reports whether it found one.
func (h *harness) pump() bool {
	for _, t := range append([]*harness{h}, h.siblings...) {
		select {
		case msg := <-t.msgs:
			t.do(msg)
			return true
		default:
		}
	}
	return false
}

// submit types a line and presses enter, one key at a time, so that the input
// path itself — emoji and all — is what the tests exercise. A newline in line
// is typed as the alt+enter that inserts one.
func (h *harness) submit(line string) {
	h.t.Helper()
	for _, r := range line {
		if r == '\n' {
			h.do(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
			continue
		}
		h.do(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	h.do(tea.KeyMsg{Type: tea.KeyEnter})
}

// view is the frame a participant would be looking at.
func (h *harness) view() string { return h.model.View() }

// seen reports whether every fragment is on screen.
func (h *harness) seen(fragments ...string) bool {
	view := h.view()
	for _, f := range fragments {
		if !strings.Contains(view, f) {
			return false
		}
	}
	return true
}

// mustSee waits for every fragment to appear.
func (h *harness) mustSee(fragments ...string) {
	h.t.Helper()
	h.until(strings.Join(fragments, " + "), func() bool { return h.seen(fragments...) })
}

// mustNotSee fails if a fragment is on screen once everything pending has
// been applied.
func (h *harness) mustNotSee(fragment string) {
	h.t.Helper()
	h.settle()
	if h.seen(fragment) {
		h.t.Fatalf("did not expect %q on screen\n--- screen ---\n%s", fragment, h.view())
	}
}

// settle applies everything pending and gives in-flight commands a moment to
// report back, which is what makes an assertion about absence meaningful.
func (h *harness) settle() {
	h.t.Helper()
	for quiet := 0; quiet < 20; {
		if h.pump() {
			quiet = 0
			continue
		}
		time.Sleep(tick)
		quiet++
	}
}

// quitting reports whether the Model has asked to exit.
func (h *harness) quitting() bool {
	select {
	case <-h.quit:
		return true
	default:
		return false
	}
}

// connected drives a harness all the way to Connected as the Peer, which is
// where most of the interesting behaviour lives.
func connected(t *testing.T) (*harness, *fakeSession) {
	t.Helper()
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})

	h.submit("/connect " + testInvite)
	h.until("the Session to be joined", func() bool {
		_, joins, _, _, _ := f.counts()
		return joins == 1
	})

	f.emit(session.StateChanged{State: session.Connecting})
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice"})
	h.mustSee("yes / no")

	h.submit("yes")
	f.emit(session.StateChanged{State: session.Connected})
	f.emit(session.LinkChanged{Link: transport.LinkDirect})
	h.mustSee("Connected")
	return h, f
}
