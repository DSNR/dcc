package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// fakeDTLS stands in for a WebRTC certificate fingerprint until the media
// work mints real ones: 64 lowercase hex characters, as the wire demands.
var fakeDTLS = strings.Repeat("2f", 32)

// waitTimeout bounds every wait for an expected event. Loopback Sessions
// settle in milliseconds; hitting this means something is wedged.
const waitTimeout = 10 * time.Second

// quiet is how long a test watches for events that must not arrive.
const quiet = 300 * time.Millisecond

func newIdentity(t *testing.T) identity.Identity {
	t.Helper()
	id, reset, err := identity.Load(t.TempDir())
	if err != nil {
		t.Fatalf("identity.Load: %v", err)
	}
	if reset != nil {
		t.Fatalf("fresh identity reported a reset: %v", reset.Cause)
	}
	return id
}

func newSession(t *testing.T, id identity.Identity, name string, pins session.Pins) *session.Session {
	t.Helper()
	s, err := session.New(session.Options{
		Identity: id,
		Name:     name,
		DTLS:     fakeDTLS,
		Tunnel:   rendezvous.Loopback{},
		Pins:     pins,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// hostSession starts a Hosting Session over the loopback Rendezvous and
// returns it with its Invite.
func hostSession(t *testing.T, id identity.Identity, name string, pins session.Pins) (*session.Session, rendezvous.Invite) {
	t.Helper()
	s := newSession(t, id, name, pins)
	if err := s.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, s, session.Hosting)
	return s, waitInvite(t, s)
}

// nextEvent returns the next event or fails on timeout; ok is false once the
// events channel has closed.
func nextEvent(t *testing.T, s *session.Session) (session.Event, bool) {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		return e, ok
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for an event")
		return nil, false
	}
}

// waitState drains events until the wanted state is announced.
func waitState(t *testing.T, s *session.Session, want session.State) session.StateChanged {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before reaching %v", want)
		}
		if sc, isState := e.(session.StateChanged); isState && sc.State == want {
			return sc
		}
	}
}

// waitInvite drains events until the Invite is announced.
func waitInvite(t *testing.T, s *session.Session) rendezvous.Invite {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the Invite was ready")
		}
		if ir, isInvite := e.(session.InviteReady); isInvite {
			return ir.Invite
		}
	}
}

// waitPrompt drains events until the Security Code prompt is emitted.
func waitPrompt(t *testing.T, s *session.Session) session.VerifyPrompt {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the Security Code prompt")
		}
		if vp, isPrompt := e.(session.VerifyPrompt); isPrompt {
			return vp
		}
	}
}

// assertQuiet fails if any event arrives within the quiet window.
func assertQuiet(t *testing.T, s *session.Session) {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatalf("events channel closed while no event was expected")
		}
		t.Fatalf("unexpected event %#v", e)
	case <-time.After(quiet):
	}
}

// waitClosed drains events until the channel closes.
func waitClosed(t *testing.T, s *session.Session) {
	t.Helper()
	for {
		_, ok := nextEvent(t, s)
		if !ok {
			return
		}
	}
}

// TestSessionsHandshake drives two in-process Sessions through the whole
// happy path over the fake Rendezvous: Host, Join, matching Security Codes,
// both sides accept, both Connected.
func TestSessionsHandshake(t *testing.T) {
	hostID, peerID := newIdentity(t), newIdentity(t)
	host, invite := hostSession(t, hostID, "Alice", nil)

	peer := newSession(t, peerID, "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitState(t, peer, session.Connecting)

	hostPrompt := waitPrompt(t, host)
	peerPrompt := waitPrompt(t, peer)

	if hostPrompt.Name != "Bob" {
		t.Errorf("host prompt names %q, want Bob", hostPrompt.Name)
	}
	if peerPrompt.Name != "Alice" {
		t.Errorf("peer prompt names %q, want Alice", peerPrompt.Name)
	}
	if hostPrompt.Peer != peerID.Public() {
		t.Errorf("host prompt carries the wrong Identity")
	}
	if peerPrompt.Peer != hostID.Public() {
		t.Errorf("peer prompt carries the wrong Identity")
	}
	want := identity.SecurityCodeFor(hostID.Public(), peerID.Public())
	if hostPrompt.Code != want || peerPrompt.Code != want {
		t.Errorf("Security Codes disagree: host %s, peer %s, want %s", hostPrompt.Code, peerPrompt.Code, want)
	}
	if hostPrompt.Changed || peerPrompt.Changed {
		t.Errorf("a first meeting reported a changed Security Code")
	}

	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, host, session.Connected)
	waitState(t, peer, session.Connected)
}

// TestWrongPasswordFailsSilently joins with a tampered Password: the guesser
// ends Failed without ever seeing the Host's name or Identity, and the Host
// carries on Hosting without a peep.
func TestWrongPasswordFailsSilently(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)

	bad := invite
	bad.Password[0] ^= 1

	guesser := newSession(t, newIdentity(t), "Eve", nil)
	if err := guesser.Join(t.Context(), bad.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	for {
		e, ok := nextEvent(t, guesser)
		if !ok {
			t.Fatalf("events closed before Failed was announced")
		}
		switch e := e.(type) {
		case session.VerifyPrompt:
			t.Fatalf("the guesser was shown a Security Code prompt: %#v", e)
		case session.StateChanged:
			if e.State == session.Failed {
				if e.Reason != session.ReasonHandshakeFailed {
					t.Errorf("Failed for %v, want %v", e.Reason, session.ReasonHandshakeFailed)
				}
				assertQuiet(t, host)
				return
			}
		}
	}
}

// TestLockedInviteRejectsThirdIdentity locks the Invite to the first
// authenticated Identity and turns a different Identity away with an
// encrypted rejection — while the first pair carries on undisturbed.
func TestLockedInviteRejectsThirdIdentity(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)

	peer := newSession(t, newIdentity(t), "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitPrompt(t, host)
	waitPrompt(t, peer)

	third := newSession(t, newIdentity(t), "Mallory", nil)
	if err := third.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("third Join: %v", err)
	}
	if sc := waitState(t, third, session.Failed); sc.Reason != session.ReasonRejected {
		t.Errorf("third Identity Failed for %v, want %v", sc.Reason, session.ReasonRejected)
	}

	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, host, session.Connected)
	waitState(t, peer, session.Connected)
}

// TestVerifyGateHoldsUntilResolved parks both sides in Verifying until each
// prompt is answered, and ends the Session when one side refuses.
func TestVerifyGateHoldsUntilResolved(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)

	peer := newSession(t, newIdentity(t), "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitPrompt(t, host)
	waitPrompt(t, peer)

	// Neither side may advance while both prompts stand.
	assertQuiet(t, host)
	assertQuiet(t, peer)

	// One side accepting resolves only its own gate.
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, peer, session.Connected)
	assertQuiet(t, host)

	// Refusal ends the Session on both sides.
	if err := host.Refuse(); err != nil {
		t.Fatalf("host Refuse: %v", err)
	}
	if sc := waitState(t, host, session.Disconnected); sc.Reason != session.ReasonRefused {
		t.Errorf("host Disconnected for %v, want %v", sc.Reason, session.ReasonRefused)
	}
	waitClosed(t, host)
	if sc := waitState(t, peer, session.Disconnected); sc.Reason != session.ReasonConnectionLost {
		t.Errorf("peer Disconnected for %v, want %v", sc.Reason, session.ReasonConnectionLost)
	}
	waitClosed(t, peer)
}

// TestIdentityChangeSurfacesChangedCode pins the peer's Display Name to an
// older Identity and expects the blocking prompt to say the code changed.
func TestIdentityChangeSurfacesChangedCode(t *testing.T) {
	previous := newIdentity(t)
	pins := session.NewMemoryPins()
	pins.Pin("Bob", previous.Public())

	host, invite := hostSession(t, newIdentity(t), "Alice", pins)

	current := newIdentity(t)
	peer := newSession(t, current, "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}

	hostPrompt := waitPrompt(t, host)
	if !hostPrompt.Changed {
		t.Errorf("a changed Identity did not surface the changed-code prompt")
	}
	if peerPrompt := waitPrompt(t, peer); peerPrompt.Changed {
		t.Errorf("the peer has no pin for Alice yet reported a change")
	}

	// Accepting the new Identity re-pins the name to it.
	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	if pinned, ok := pins.Pinned("Bob"); !ok || pinned != current.Public() {
		t.Errorf("accepting did not re-pin Bob to the new Identity")
	}
}

// TestJoinRefusesBadInvite reports a mangled Invite synchronously, leaving
// the Session Idle.
func TestJoinRefusesBadInvite(t *testing.T) {
	s := newSession(t, newIdentity(t), "Bob", nil)
	if err := s.Join(t.Context(), "not an invite"); err == nil {
		t.Fatalf("Join accepted a string that is not an Invite")
	}
	assertQuiet(t, s)
}
