package session_test

import (
	"path/filepath"
	"testing"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
)

// newStore opens a Store in its own directory under id's db_key, closing it
// with the test.
func newStore(t *testing.T, id identity.Identity) *storage.Store {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), storage.FileName), id.DBKey())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newStoredSession builds a Session that pins and keeps history through
// store.
func newStoredSession(t *testing.T, id identity.Identity, name string, store *storage.Store) *session.Session {
	t.Helper()
	s, err := session.New(session.Options{
		Identity: id,
		Name:     name,
		Tunnel:   rendezvous.Loopback{},
		Pins:     store,
		History:  store,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestConversationContinuesAcrossRoles runs two Sessions between the same
// two people with the roles swapped — Alice hosts the first, Bob the second
// — and wants each side holding one Conversation with both messages in it,
// because a Conversation is keyed by the Peer's Identity, not by who Hosted.
// Along the way it is also the restart test: the second Session recognises
// the first's pin, so neither prompt reports a change.
func TestConversationContinuesAcrossRoles(t *testing.T) {
	alice, bob := newIdentity(t), newIdentity(t)
	aliceStore, bobStore := newStore(t, alice), newStore(t, bob)

	// Session one: Alice hosts, and says something once connected.
	host := newStoredSession(t, alice, "Alice", aliceStore)
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	invite := waitInvite(t, host)
	peer := newStoredSession(t, bob, "Bob", bobStore)
	connectSessions(t, host, peer, invite)

	id, err := host.SendText("sent while Alice hosted")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	waitText(t, peer)
	waitTextStatus(t, host, id, session.TextDelivered)
	_ = host.Close()
	_ = peer.Close()
	waitClosed(t, host)
	waitClosed(t, peer)

	// Session two: the roles swapped, Bob hosting. Both sides pinned each
	// other last time, so neither prompt may claim an Identity change.
	host = newStoredSession(t, bob, "Bob", bobStore)
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	invite = waitInvite(t, host)
	peer = newStoredSession(t, alice, "Alice", aliceStore)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if prompt := waitPrompt(t, host); prompt.Changed {
		t.Fatalf("Bob's prompt reports an Identity change for a pinned Peer")
	}
	if prompt := waitPrompt(t, peer); prompt.Changed {
		t.Fatalf("Alice's prompt reports an Identity change for a pinned Peer")
	}
	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, host, session.Connected)
	waitState(t, peer, session.Connected)

	id, err = host.SendText("sent while Bob hosted")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	waitText(t, peer)
	waitTextStatus(t, host, id, session.TextDelivered)
	_ = host.Close()
	_ = peer.Close()
	waitClosed(t, host)
	waitClosed(t, peer)

	// Each side holds exactly one Conversation with the other, carrying both
	// Sessions' messages in order.
	assertConversation(t, aliceStore, "Bob", bob.Public(), true)
	assertConversation(t, bobStore, "Alice", alice.Public(), false)
}

// assertConversation wants store to hold one Conversation, pinned to peer
// under name, with both messages — the first one mine when this side hosted
// first.
func assertConversation(t *testing.T, store *storage.Store, name string, peer identity.PublicKey, hostedFirst bool) {
	t.Helper()
	conversations, err := store.Conversations()
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("the role swap split the history: %+v", conversations)
	}
	if c := conversations[0]; c.Name != name || c.Peer != peer || c.Messages != 2 {
		t.Fatalf("Conversation with %s: %+v", name, c)
	}
	messages, err := store.Messages(peer)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(messages))
	}
	if messages[0].Body != "sent while Alice hosted" || messages[1].Body != "sent while Bob hosted" {
		t.Fatalf("messages out of order: %+v", messages)
	}
	if messages[0].Mine != hostedFirst || messages[1].Mine == hostedFirst {
		t.Fatalf("messages attributed to the wrong side: %+v", messages)
	}
	if messages[0].Mine && messages[0].Status != session.TextDelivered {
		t.Fatalf("the delivered message restored as %v", messages[0].Status)
	}
}
