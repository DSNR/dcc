package session_test

import (
	"testing"

	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

// newRelayOnlySession builds a Session with every direct path disabled, so
// only the Rendezvous's fallback relay can carry it.
func newRelayOnlySession(t *testing.T, name string) *session.Session {
	t.Helper()
	s, err := session.New(session.Options{
		Identity:  newIdentity(t),
		Name:      name,
		Tunnel:    rendezvous.Loopback{},
		RelayOnly: true,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRelayOnlySessionConnectsRelayed is the blocked-network acceptance
// path: with direct connection impossible, the Session still connects —
// announced as relayed — and messages flow both ways.
func TestRelayOnlySessionConnectsRelayed(t *testing.T) {
	host := newRelayOnlySession(t, "Hana")
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	invite := waitInvite(t, host)
	peer := newRelayOnlySession(t, "Piet")

	connectSessions(t, host, peer, invite)
	if link := waitLink(t, host); link.Link != transport.LinkRelayed {
		t.Errorf("host announced %v, want %v", link.Link, transport.LinkRelayed)
	}
	if link := waitLink(t, peer); link.Link != transport.LinkRelayed {
		t.Errorf("peer announced %v, want %v", link.Link, transport.LinkRelayed)
	}

	id, err := host.SendText("through the relay")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got := waitText(t, peer); got.Body != "through the relay" {
		t.Errorf("peer received %q, want %q", got.Body, "through the relay")
	}
	waitTextStatus(t, host, id, session.TextDelivered)

	back, err := peer.SendText("and back")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if got := waitText(t, host); got.Body != "and back" {
		t.Errorf("host received %q, want %q", got.Body, "and back")
	}
	waitTextStatus(t, peer, back, session.TextDelivered)
}

// TestRelayOnlyReconnects blips a relayed Session. The reconnect must mint a
// working relay end for the new transport on both sides — the old listener
// died with the old transport's mux.
func TestRelayOnlyReconnects(t *testing.T) {
	host := newRelayOnlySession(t, "Hana")
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	invite := waitInvite(t, host)
	peer := newRelayOnlySession(t, "Piet")
	connectSessions(t, host, peer, invite)
	waitLink(t, host)
	waitLink(t, peer)

	peer.Sever()
	waitState(t, peer, session.Reconnecting)
	waitState(t, host, session.Reconnecting)
	waitState(t, peer, session.Connected)
	waitState(t, host, session.Connected)
	if link := waitLink(t, peer); link.Link != transport.LinkRelayed {
		t.Errorf("peer reconnected %v, want %v", link.Link, transport.LinkRelayed)
	}

	id, err := peer.SendText("still here?")
	if err != nil {
		t.Fatalf("SendText after reconnect: %v", err)
	}
	if got := waitText(t, host); got.Body != "still here?" {
		t.Errorf("host received %q, want %q", got.Body, "still here?")
	}
	waitTextStatus(t, peer, id, session.TextDelivered)
}
