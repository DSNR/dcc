package gui_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
)

// waitTimeout bounds every wait for something to reach the screen. Nothing
// here talks to a network; hitting this means something is wedged.
const waitTimeout = 5 * time.Second

// tick is how long a wait sleeps between looks.
const tick = time.Millisecond

// testInvite is a well-formed Invite for a Rendezvous that does not exist.
// The window never parses it — the Session does — so a valid shape is all the
// tests need.
var testInvite = rendezvous.Invite{URL: "http://127.0.0.1:8080"}.String()

// testCode stands in for a derived Security Code: forty digits in the eight
// groups the prompt lays out.
const testCode = identity.SecurityCode("1111122222333334444455555666667777788888")

// testPeer stands in for the Identity a handshake authenticated.
var testPeer = identity.PublicKey{1, 2, 3}

// fakeSession stands in for *session.Session. It records what the window
// asked of it and lets a test post the events a real Session would raise, so
// the desktop client is tested without a Rendezvous, a handshake or a
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

	// hostErr, joinErr, sendErr and acceptErr are what the next matching
	// command returns; zero means it succeeds.
	hostErr, joinErr, sendErr, acceptErr error
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
	if f.acceptErr != nil {
		return f.acceptErr
	}
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
	return "msg-" + body, nil
}

// Close ends the fake Session the way a real one does: the events channel
// closes, which is what the window waits for.
func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	if f.closes == 1 {
		close(f.events)
	}
	return nil
}

// post raises one event, as a real Session would.
func (f *fakeSession) post(t *testing.T, e session.Event) {
	t.Helper()
	select {
	case f.events <- e:
	case <-time.After(waitTimeout):
		t.Fatal("the Session's events are not being read")
	}
}

// connect takes the fake all the way to Connected with the prompt answered,
// which is the state most of what a chat window does needs.
func (f *fakeSession) connect(t *testing.T, m *gui.Model, name string) {
	t.Helper()
	f.post(t, session.StateChanged{State: session.Verifying})
	f.post(t, session.VerifyPrompt{Code: testCode, Name: name, Peer: testPeer})
	waitFor(t, m, "a Security Code prompt", func(s gui.Screen) bool { return s.Prompt != nil })
	m.Accept()
	f.post(t, session.StateChanged{State: session.Connected})
	waitFor(t, m, "the send button to come alive", func(s gui.Screen) bool { return s.Controls.Send })
}

// counts reads the fake's tallies under its lock.
func (f *fakeSession) counts() (hosts, accepts, refuses, closes int, sent, joins []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts, f.accepts, f.refuses, f.closes,
		append([]string(nil), f.sent...), append([]string(nil), f.joins...)
}

// fakeStore stands in for *storage.Store: a Conversation or two, in memory.
type fakeStore struct {
	mu            sync.Mutex
	conversations []storage.Conversation
	messages      map[identity.PublicKey][]storage.Message
	cleared       []identity.PublicKey
	readErr       error
}

func newStore() *fakeStore {
	return &fakeStore{messages: make(map[identity.PublicKey][]storage.Message)}
}

func (s *fakeStore) Conversations() ([]storage.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	return append([]storage.Conversation(nil), s.conversations...), nil
}

func (s *fakeStore) Messages(peer identity.PublicKey) ([]storage.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	return append([]storage.Message(nil), s.messages[peer]...), nil
}

func (s *fakeStore) Clear(peer identity.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, peer)
	delete(s.messages, peer)
	kept := s.conversations[:0]
	for _, c := range s.conversations {
		if c.Peer != peer {
			kept = append(kept, c)
		}
	}
	s.conversations = kept
	return nil
}

// add puts one stored Conversation in the fake Store.
func (s *fakeStore) add(c storage.Conversation, messages ...storage.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations = append(s.conversations, c)
	s.messages[c.Peer] = messages
}

// newModel builds a Model over a fake Session it hands back, so a test can
// post events into the window it is driving.
func newModel(t *testing.T, opts gui.Options) (*gui.Model, *fakeSession) {
	t.Helper()
	fake := newFake()
	if opts.New == nil {
		opts.New = func() (gui.Session, error) { return fake, nil }
	}
	if opts.Name == "" {
		opts.Name = "me"
	}
	m := gui.New(opts)
	t.Cleanup(func() {
		m.Quit()
		select {
		case <-m.Done():
		case <-time.After(waitTimeout):
			t.Error("the window never finished closing")
		}
	})
	return m, fake
}

// waitFor polls the Screen until it satisfies want, which is how a test waits
// on work the Model does off the calling goroutine.
func waitFor(t *testing.T, m *gui.Model, what string, want func(gui.Screen) bool) gui.Screen {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		s := m.Screen()
		if want(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s\n--- conversation ---\n%s", what, transcript(s))
		}
		time.Sleep(tick)
	}
}

// waitSaid waits for one line to appear in the conversation area.
func waitSaid(t *testing.T, m *gui.Model, want string) gui.Screen {
	t.Helper()
	return waitFor(t, m, "the window to say "+want, func(s gui.Screen) bool {
		return strings.Contains(transcript(s), want)
	})
}

// transcript is the conversation area as plain text, which is what an
// assertion about what a participant can read looks at.
func transcript(s gui.Screen) string {
	var b strings.Builder
	for _, e := range s.Entries {
		b.WriteString(e.Stamp())
		b.WriteString(" ")
		if e.Notice() {
			b.WriteString("· ")
		} else {
			b.WriteString(e.Who + ": ")
		}
		b.WriteString(e.Body)
		if marker := e.Marker(); marker != "" {
			b.WriteString("  " + marker)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// said reports whether the conversation area carries a line.
func said(m *gui.Model, want string) bool {
	return strings.Contains(transcript(m.Screen()), want)
}

var errBroken = errors.New("broken")
