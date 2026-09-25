package cli_test

import (
	"sync"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/transport"
)

// fakeStore stands in for *storage.Store, holding whatever history a test
// says has been kept and recording what the TUI asks it to clear.
type fakeStore struct {
	mu            sync.Mutex
	conversations []storage.Conversation
	messages      map[identity.PublicKey][]storage.Message
	cleared       []identity.PublicKey
}

func (f *fakeStore) Conversations() ([]storage.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.Conversation(nil), f.conversations...), nil
}

func (f *fakeStore) Messages(peer identity.PublicKey) ([]storage.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.Message(nil), f.messages[peer]...), nil
}

func (f *fakeStore) Clear(peer identity.PublicKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, peer)
	return nil
}

func (f *fakeStore) clearedPeers() []identity.PublicKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identity.PublicKey(nil), f.cleared...)
}

// alicePeer is the Identity the stored history in these tests hangs on.
var alicePeer = identity.PublicKey{1: 0xAA}

// storedHistory is a Store holding one Conversation with Alice, spanning two
// days so the dated lines have something to separate.
func storedHistory() *fakeStore {
	first := time.Date(2026, time.September, 24, 21, 30, 0, 0, time.Local)
	second := time.Date(2026, time.September, 25, 9, 15, 0, 0, time.Local)
	return &fakeStore{
		conversations: []storage.Conversation{
			{Peer: alicePeer, Name: "Alice", Messages: 2, LastAt: second},
		},
		messages: map[identity.PublicKey][]storage.Message{
			alicePeer: {
				{ID: "id-1", Mine: true, Body: "kept from yesterday", At: first, Status: session.TextDelivered},
				{ID: "id-2", Mine: false, Body: "kept from this morning", At: second},
			},
		},
	}
}

func TestHistoryListsTheStoredConversations(t *testing.T) {
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: storedHistory()})

	h.submit("/history")
	h.mustSee(`"Alice"`, "2 messages", "25 Sep 2026")
}

func TestHistoryReadsOneConversationBack(t *testing.T) {
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: storedHistory()})

	h.submit("/history Alice")
	h.mustSee(
		"— 24 Sep 2026 —",
		"you: kept from yesterday",
		"— 25 Sep 2026 —",
		"Alice: kept from this morning",
	)
}

func TestHistoryWithNothingStoredSaysSo(t *testing.T) {
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: &fakeStore{}})

	h.submit("/history")
	h.mustSee("Nothing is stored yet")
}

func TestHistoryWithAnUnknownNameSaysSo(t *testing.T) {
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: storedHistory()})

	h.submit("/history Bob")
	h.mustSee(`Nothing is stored with "Bob"`)
}

func TestClearHistoryDeletesTheNamedConversation(t *testing.T) {
	store := storedHistory()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: store})

	h.submit("/clearhistory Alice")
	h.mustSee(`Deleted the Conversation with "Alice" from this device`)
	if cleared := store.clearedPeers(); len(cleared) != 1 || cleared[0] != alicePeer {
		t.Fatalf("cleared %v, want exactly %v", cleared, alicePeer)
	}
}

func TestClearHistoryNeedsAName(t *testing.T) {
	store := storedHistory()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake()), Store: store})

	h.submit("/clearhistory")
	h.mustSee("/clearhistory <name>")
	if cleared := store.clearedPeers(); len(cleared) != 0 {
		t.Fatalf("cleared %v on an unnamed command", cleared)
	}
}

// TestAcceptingAPeerRestoresTheConversation is the restart story on screen:
// what was said with a returning Peer is back in the conversation the moment
// they are accepted.
func TestAcceptingAPeerRestoresTheConversation(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f), Store: storedHistory()})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Alice", Peer: alicePeer})
	h.mustSee("yes / no")

	h.submit("yes")
	f.emit(session.StateChanged{State: session.Connected})
	f.emit(session.LinkChanged{Link: transport.LinkDirect})
	h.mustSee(
		`The Conversation with "Alice" so far:`,
		"you: kept from yesterday",
		"Alice: kept from this morning",
	)
}

// A first meeting has nothing to restore, and says nothing about it.
func TestAcceptingAFirstMeetingRestoresNothing(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f), Store: storedHistory()})

	h.submit("/connect " + testInvite)
	f.emit(session.StateChanged{State: session.Verifying})
	f.emit(session.VerifyPrompt{Code: testCode, Name: "Bob", Peer: identity.PublicKey{1: 0xBB}})
	h.mustSee("yes / no")

	h.submit("yes")
	h.mustSee(`Accepted — "Bob" is verified`)
	h.mustNotSee("so far:")
}

func TestHistoryWithoutAStoreSaysSo(t *testing.T) {
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(newFake())})

	h.submit("/history")
	h.mustSee("No history is kept in this run.")
}
