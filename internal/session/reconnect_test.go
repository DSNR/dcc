package session_test

import (
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// severedPair builds a Connected pair and then cuts the connection between
// them, returning once both sides have noticed.
func severedPair(t *testing.T) (host, peer *session.Session) {
	t.Helper()
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer = newSession(t, newIdentity(t), "Bob", nil)
	connectSessions(t, host, peer, invite)

	peer.Sever()
	waitState(t, host, session.Reconnecting)
	waitState(t, peer, session.Reconnecting)
	return host, peer
}

// TestReconnectResendsWithoutDuplicates cuts the connection under a live
// pair, writes while Reconnecting, and checks the message arrives exactly
// once after the Session heals — along with anything that was still waiting
// on an ack when the blip hit.
func TestReconnectResendsWithoutDuplicates(t *testing.T) {
	host, peer := severedPair(t)

	// Written into the outage: queued, not sent.
	id, err := peer.SendText("did you get this?")
	if err != nil {
		t.Fatalf("SendText while Reconnecting: %v", err)
	}
	waitTextStatus(t, peer, id, session.TextPending)

	waitState(t, peer, session.Connected)
	waitState(t, host, session.Connected)

	waitTextStatus(t, peer, id, session.TextDelivered)
	if got := waitText(t, host); got.ID != id {
		t.Fatalf("host received %#v, want the queued message %s", got, id)
	}
	// Exactly once: nothing else surfaces on the host.
	assertQuiet(t, host)

	// The healed Session is a working Session, both ways.
	reply, err := host.SendText("loud and clear")
	if err != nil {
		t.Fatalf("host SendText after reconnect: %v", err)
	}
	if back := waitText(t, peer); back.ID != reply {
		t.Errorf("peer received %#v, want the reply", back)
	}
	waitTextStatus(t, host, reply, session.TextDelivered)
}

// TestReconnectResendsUnacked severs the connection immediately after a send,
// so the ack — and possibly the message itself — is lost to the blip. After
// the reconnect the message must be delivered, and shown exactly once no
// matter whether the first copy made it.
func TestReconnectResendsUnacked(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	connectSessions(t, host, peer, invite)

	id, err := peer.SendText("racing the blip")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	peer.Sever()

	waitTextStatus(t, peer, id, session.TextDelivered)
	if got := waitText(t, host); got.ID != id {
		t.Fatalf("host received %#v, want %s", got, id)
	}
	assertQuiet(t, host)
}

// TestCrashedPeerRejoins closes the Peer outright — a crash, as the Host
// sees it — and has the same Identity join the same Invite afresh. The Host
// replaces the stale connection without raising a new Security Code prompt.
func TestCrashedPeerRejoins(t *testing.T) {
	peerID := newIdentity(t)
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, peerID, "Bob", nil)
	connectSessions(t, host, peer, invite)

	if err := peer.Close(); err != nil {
		t.Fatalf("peer Close: %v", err)
	}
	waitState(t, host, session.Reconnecting)

	rejoined := newSession(t, peerID, "Bob", nil)
	if err := rejoined.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	// The rejoining side is a fresh Session and gets its prompt; the Host,
	// who already accepted this Identity, must not be asked again.
	waitPrompt(t, rejoined)
	if err := rejoined.Accept(); err != nil {
		t.Fatalf("rejoined Accept: %v", err)
	}

	for {
		e, ok := nextEvent(t, host)
		if !ok {
			t.Fatalf("host events closed before reconnecting")
		}
		if _, isPrompt := e.(session.VerifyPrompt); isPrompt {
			t.Fatalf("host raised a Security Code prompt for an Identity it already accepted")
		}
		if sc, isState := e.(session.StateChanged); isState && sc.State == session.Connected {
			break
		}
	}
	waitState(t, rejoined, session.Connected)

	id, err := host.SendText("welcome back")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got := waitText(t, rejoined); got.ID != id {
		t.Errorf("rejoined peer received %#v, want %s", got, id)
	}
	waitTextStatus(t, host, id, session.TextDelivered)
}

// TestRendezvousGoneFailsFast takes the whole Host — Rendezvous included —
// away from under a Connected Peer: the Peer's reconnect must fail fast,
// with the reason naming the Rendezvous, not grind through the budget.
func TestRendezvousGoneFailsFast(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	connectSessions(t, host, peer, invite)

	if err := host.Close(); err != nil {
		t.Fatalf("host Close: %v", err)
	}

	start := time.Now()
	for {
		e, ok := nextEvent(t, peer)
		if !ok {
			t.Fatalf("peer events closed before Failed")
		}
		sc, isState := e.(session.StateChanged)
		if !isState || sc.State != session.Failed {
			continue
		}
		if sc.Reason != session.ReasonRendezvousGone {
			t.Errorf("peer failed with reason %v, want %v", sc.Reason, session.ReasonRendezvousGone)
		}
		break
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("failing took %v; a gone Rendezvous should be recognised at once", took)
	}
	waitClosed(t, peer)
}

// TestHostReconnectBudgetExpires has the Peer vanish for good: the Host
// waits out its (shortened) budget in Reconnecting and then ends the
// Session as a lost connection, tearing everything down.
func TestHostReconnectBudgetExpires(t *testing.T) {
	hostID, peerID := newIdentity(t), newIdentity(t)
	host, err := session.New(session.Options{
		Identity:      hostID,
		Name:          "Alice",
		Tunnel:        rendezvous.Loopback{},
		ReconnectWait: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	invite := waitInvite(t, host)

	peer := newSession(t, peerID, "Bob", nil)
	connectSessions(t, host, peer, invite)

	if err := peer.Close(); err != nil {
		t.Fatalf("peer Close: %v", err)
	}
	waitState(t, host, session.Reconnecting)
	if sc := waitState(t, host, session.Disconnected); sc.Reason != session.ReasonConnectionLost {
		t.Errorf("host disconnected with reason %v, want %v", sc.Reason, session.ReasonConnectionLost)
	}
	waitClosed(t, host)
}

// TestCloseWhileReconnecting closes a Session mid-reconnect: the terminal
// Disconnected must arrive and the events channel must close — nothing may
// keep running.
func TestCloseWhileReconnecting(t *testing.T) {
	host, peer := severedPair(t)

	if err := peer.Close(); err != nil {
		t.Fatalf("peer Close: %v", err)
	}
	waitClosed(t, peer)

	if err := host.Close(); err != nil {
		t.Fatalf("host Close: %v", err)
	}
	waitClosed(t, host)
}
