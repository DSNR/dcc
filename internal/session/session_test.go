package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/transport"
)

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

// waitLink drains events until Connected's sub-status is announced.
func waitLink(t *testing.T, s *session.Session) session.LinkChanged {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before a LinkChanged")
		}
		if lc, isLink := e.(session.LinkChanged); isLink {
			return lc
		}
	}
}

// waitTextStatus drains events until the given message reports the given
// status. Earlier statuses on the way there are fine; a later one means the
// message skipped a stage, which fails the test.
func waitTextStatus(t *testing.T, s *session.Session, id string, want session.DeliveryStatus) {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before message %s was %v", id, want)
		}
		ts, isStatus := e.(session.TextStatus)
		if !isStatus || ts.ID != id {
			continue
		}
		if ts.Status > want {
			t.Fatalf("message %s reported %v, want %v", id, ts.Status, want)
		}
		if ts.Status == want {
			return
		}
	}
}

// waitText drains events until a message arrives.
func waitText(t *testing.T, s *session.Session) session.TextReceived {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before a TextReceived")
		}
		if tr, isText := e.(session.TextReceived); isText {
			return tr
		}
	}
}

// connectSessions runs the happy path to a live pair: Host, Join, both
// prompts accepted, both Connected.
func connectSessions(t *testing.T, host, peer *session.Session, invite rendezvous.Invite) {
	t.Helper()
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitPrompt(t, host)
	waitPrompt(t, peer)
	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, host, session.Connected)
	waitState(t, peer, session.Connected)
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
	// The refused side is told nothing — it sees its connection die, tries
	// to reconnect, and finds nothing where the Rendezvous was.
	if sc := waitState(t, peer, session.Failed); sc.Reason != session.ReasonRendezvousGone {
		t.Errorf("peer Failed for %v, want %v", sc.Reason, session.ReasonRendezvousGone)
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

// TestTextExchange sends text both ways across a live pair: each message
// walks pending → sent → delivered on its sender, arrives intact — emoji and
// all — on the other side, and both sides report a direct link.
func TestTextExchange(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	connectSessions(t, host, peer, invite)

	if lc := waitLink(t, host); lc.Link != transport.LinkDirect {
		t.Errorf("host link is %v, want %v", lc.Link, transport.LinkDirect)
	}
	if lc := waitLink(t, peer); lc.Link != transport.LinkDirect {
		t.Errorf("peer link is %v, want %v", lc.Link, transport.LinkDirect)
	}

	body := "hej Bob 👋 — line two\nşemsiye ☂️"
	id, err := host.SendText(body)
	if err != nil {
		t.Fatalf("host SendText: %v", err)
	}
	waitTextStatus(t, host, id, session.TextPending)
	waitTextStatus(t, host, id, session.TextSent)

	got := waitText(t, peer)
	if got.ID != id || got.Body != body {
		t.Errorf("peer received %#v, want id %s with the body that was sent", got, id)
	}
	if got.At.IsZero() || time.Since(got.At) > time.Minute {
		t.Errorf("peer stamped the message %v", got.At)
	}
	waitTextStatus(t, host, id, session.TextDelivered)

	reply, err := peer.SendText("hello Alice")
	if err != nil {
		t.Fatalf("peer SendText: %v", err)
	}
	if back := waitText(t, host); back.ID != reply || back.Body != "hello Alice" {
		t.Errorf("host received %#v, want the reply", back)
	}
	waitTextStatus(t, peer, reply, session.TextDelivered)
}

// TestTextHeldUntilAccepted has the Peer accept and speak while the Host's
// Security Code prompt still stands: nothing surfaces on the Host — and no
// ack reaches the Peer — until the Host accepts too.
func TestTextHeldUntilAccepted(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitPrompt(t, host)
	waitPrompt(t, peer)

	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, peer, session.Connected)

	id, err := peer.SendText("anyone there?")
	if err != nil {
		t.Fatalf("peer SendText: %v", err)
	}
	waitTextStatus(t, peer, id, session.TextSent)

	// The Host's prompt is unresolved: the message must not surface, and its
	// ack must not come back.
	assertQuiet(t, host)

	if err := host.Accept(); err != nil {
		t.Fatalf("host Accept: %v", err)
	}
	waitState(t, host, session.Connected)
	if got := waitText(t, host); got.ID != id {
		t.Errorf("host received %#v, want the held message", got)
	}
	waitTextStatus(t, peer, id, session.TextDelivered)
}

// TestUnackedTextFailsOnClose closes a Session that is still waiting on an
// ack: the message must be declared failed rather than silently forgotten.
func TestUnackedTextFailsOnClose(t *testing.T) {
	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	if err := peer.Join(t.Context(), invite.String()); err != nil {
		t.Fatalf("Join: %v", err)
	}
	waitPrompt(t, host)
	waitPrompt(t, peer)
	if err := peer.Accept(); err != nil {
		t.Fatalf("peer Accept: %v", err)
	}
	waitState(t, peer, session.Connected)

	// The Host never accepts, so no ack ever comes.
	id, err := peer.SendText("into the void")
	if err != nil {
		t.Fatalf("peer SendText: %v", err)
	}
	waitTextStatus(t, peer, id, session.TextSent)

	if err := peer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitTextStatus(t, peer, id, session.TextFailed)
	waitClosed(t, peer)
}

// TestSendTextRefusals covers what SendText turns away synchronously, with
// no TextStatus events: a Session that isn't Connected, an empty body, and a
// body over the 16 KiB cap.
func TestSendTextRefusals(t *testing.T) {
	s := newSession(t, newIdentity(t), "Bob", nil)
	if _, err := s.SendText("too early"); err == nil {
		t.Errorf("SendText succeeded on an Idle Session")
	}

	host, invite := hostSession(t, newIdentity(t), "Alice", nil)
	peer := newSession(t, newIdentity(t), "Bob", nil)
	connectSessions(t, host, peer, invite)

	if _, err := host.SendText(""); err == nil {
		t.Errorf("SendText accepted an empty body")
	}
	if _, err := host.SendText(strings.Repeat("a", 16*1024+1)); err == nil {
		t.Errorf("SendText accepted a body over the cap")
	}
	waitLink(t, host)
	assertQuiet(t, host)
}
