package storage_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
)

// newKey mints a random db_key, or a peer Identity key — the tests only need
// them distinct and realistic.
func newKey(t *testing.T) [identity.KeySize]byte {
	t.Helper()
	var key [identity.KeySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("reading randomness: %v", err)
	}
	return key
}

func newPeer(t *testing.T) identity.PublicKey {
	t.Helper()
	return identity.PublicKey(newKey(t))
}

// openStore opens a Store in dir under key, closing it with the test.
func openStore(t *testing.T, dir string, key [identity.KeySize]byte) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(dir, storage.FileName), key)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRestartRestoresConversation(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)
	at := time.Now()

	s := openStore(t, dir, key)
	if err := s.Pin("Bob", peer); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Outgoing(peer, "Bob", "id-1", "hello Bob", at); err != nil {
		t.Fatalf("Outgoing: %v", err)
	}
	if err := s.Status("id-1", session.TextDelivered); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if fresh, err := s.Incoming(peer, "Bob", "id-2", "hello back", at.Add(time.Second)); err != nil || !fresh {
		t.Fatalf("Incoming: fresh %v, err %v", fresh, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted := openStore(t, dir, key)
	if pinned, ok := restarted.Pinned("Bob"); !ok || pinned != peer {
		t.Fatalf("Pinned after restart: %v, %v", pinned, ok)
	}
	conversations, err := restarted.Conversations()
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(conversations) != 1 || conversations[0].Name != "Bob" || conversations[0].Messages != 2 {
		t.Fatalf("Conversations after restart: %+v", conversations)
	}
	messages, err := restarted.Messages(peer)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	first, second := messages[0], messages[1]
	if first.ID != "id-1" || first.Body != "hello Bob" || !first.Mine || first.Status != session.TextDelivered {
		t.Fatalf("first message restored as %+v", first)
	}
	if second.ID != "id-2" || second.Body != "hello back" || second.Mine {
		t.Fatalf("second message restored as %+v", second)
	}
	if first.At.UnixMilli() != at.UnixMilli() {
		t.Fatalf("first message at %v, want %v", first.At, at)
	}
}

// TestRawFileContainsNoPlaintext copies someone stealing the database file:
// none of the sensitive fields — bodies, Display Names, pinned keys — may
// appear in it, in any of SQLite's files.
func TestRawFileContainsNoPlaintext(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)
	const (
		name = "PROBE-DISPLAY-NAME"
		body = "PROBE-MESSAGE-BODY"
	)

	s := openStore(t, dir, key)
	if err := s.Pin(name, peer); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Outgoing(peer, name, "id-1", body, time.Now()); err != nil {
		t.Fatalf("Outgoing: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := readSQLiteFiles(t, dir)
	for _, probe := range [][]byte{[]byte(name), []byte(body), peer[:], []byte(peer.String())} {
		if bytes.Contains(raw, probe) {
			t.Errorf("the raw database contains the plaintext %q", probe)
		}
	}
}

// readSQLiteFiles concatenates the database and any journal beside it.
func readSQLiteFiles(t *testing.T, dir string) []byte {
	t.Helper()
	var raw []byte
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		raw = append(raw, b...)
	}
	if len(raw) == 0 {
		t.Fatalf("no database files in %s", dir)
	}
	return raw
}

func TestClearRemovesConversationLocally(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)

	s := openStore(t, dir, key)
	if err := s.Pin("Bob", peer); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Outgoing(peer, "Bob", "id-1", "off the record", time.Now()); err != nil {
		t.Fatalf("Outgoing: %v", err)
	}
	if err := s.Clear(peer); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if _, ok := s.Pinned("Bob"); ok {
		t.Fatalf("the pin survived Clear")
	}
	conversations, err := s.Conversations()
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("Conversations after Clear: %+v", conversations)
	}
	messages, err := s.Messages(peer)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages survived Clear: %+v", messages)
	}

	// Clearing a Peer with nothing kept is quietly nothing.
	if err := s.Clear(newPeer(t)); err != nil {
		t.Fatalf("Clear of an unknown peer: %v", err)
	}
}

// TestRepinKeepsHistory is the accepted Identity change: the same name under
// a new key moves the pin and keeps every message.
func TestRepinKeepsHistory(t *testing.T) {
	dir, key := t.TempDir(), newKey(t)
	before, after := newPeer(t), newPeer(t)

	s := openStore(t, dir, key)
	if err := s.Pin("Bob", before); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Outgoing(before, "Bob", "id-1", "from before the reinstall", time.Now()); err != nil {
		t.Fatalf("Outgoing: %v", err)
	}
	if err := s.Pin("Bob", after); err != nil {
		t.Fatalf("repin: %v", err)
	}

	if pinned, ok := s.Pinned("Bob"); !ok || pinned != after {
		t.Fatalf("Pinned after the change: %v, %v", pinned, ok)
	}
	messages, err := s.Messages(after)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "from before the reinstall" {
		t.Fatalf("history after the repin: %+v", messages)
	}
	if leftover, err := s.Messages(before); err != nil || len(leftover) != 0 {
		t.Fatalf("the old key still has messages: %+v, %v", leftover, err)
	}
	conversations, err := s.Conversations()
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("the repin split the Conversation: %+v", conversations)
	}
}

// TestRenameKeepsConversation is the other half of Pin: the same key under a
// new name is one person renaming themselves, not a new Conversation.
func TestRenameKeepsConversation(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)

	s := openStore(t, dir, key)
	if err := s.Pin("Bob", peer); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Outgoing(peer, "Bob", "id-1", "hello", time.Now()); err != nil {
		t.Fatalf("Outgoing: %v", err)
	}
	if err := s.Pin("Robert", peer); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if _, ok := s.Pinned("Bob"); ok {
		t.Fatalf("the old name is still pinned")
	}
	if pinned, ok := s.Pinned("Robert"); !ok || pinned != peer {
		t.Fatalf("Pinned under the new name: %v, %v", pinned, ok)
	}
	conversations, err := s.Conversations()
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(conversations) != 1 || conversations[0].Name != "Robert" || conversations[0].Messages != 1 {
		t.Fatalf("Conversations after the rename: %+v", conversations)
	}
}

func TestIncomingDeduplicates(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)

	s := openStore(t, dir, key)
	if fresh, err := s.Incoming(peer, "Bob", "id-1", "once", time.Now()); err != nil || !fresh {
		t.Fatalf("first Incoming: fresh %v, err %v", fresh, err)
	}
	if fresh, err := s.Incoming(peer, "Bob", "id-1", "once", time.Now()); err != nil || fresh {
		t.Fatalf("resent Incoming: fresh %v, err %v", fresh, err)
	}
	messages, err := s.Messages(peer)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("a resend was kept twice: %+v", messages)
	}
}

// TestWrongKeyRefused proves Open fails whole under the wrong db_key, so a
// pin can never later pass for absent because it would not decrypt.
func TestWrongKeyRefused(t *testing.T) {
	dir, key, peer := t.TempDir(), newKey(t), newPeer(t)

	s := openStore(t, dir, key)
	if err := s.Pin("Bob", peer); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := storage.Open(filepath.Join(dir, storage.FileName), newKey(t)); err == nil {
		t.Fatalf("Open under the wrong key succeeded")
	}
}
