package session_test

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
)

// The two tones the fake microphones hum. Giving each side its own is what
// makes "I heard them" a thing a test can tell apart from "I heard myself".
const (
	hostTone = 440.0
	peerTone = 1100.0
)

// The two tints the fake cameras paint, for the same reason: a picture that
// is mostly the host's red cannot have come from the peer's blue camera.
var (
	hostTint = color.RGBA{R: 200, G: 40, B: 40, A: 0xFF}
	peerTint = color.RGBA{R: 40, G: 40, B: 200, A: 0xFF}
)

// And the two the fake screens paint, which are nothing like either camera's:
// a picture of a shared screen must be tellable from a picture of a face, on
// top of being tellable from the other side's.
var (
	hostScreenTint = color.RGBA{R: 200, G: 190, B: 40, A: 0xFF}
	peerScreenTint = color.RGBA{R: 40, G: 190, B: 190, A: 0xFF}
)

// callPair brings up a Connected pair whose Calls run on fake devices, and
// returns both Sessions with the fakes their audio lands in.
func callPair(t *testing.T, ring time.Duration) (host, peer *session.Session, hostFake, peerFake *media.Fake) {
	t.Helper()
	hostFake = &media.Fake{Tone: hostTone, Tint: hostTint, ScreenTint: hostScreenTint}
	peerFake = &media.Fake{Tone: peerTone, Tint: peerTint, ScreenTint: peerScreenTint}
	host = newCallSession(t, "Alice", hostFake, ring)
	peer = newCallSession(t, "Bob", peerFake, ring)

	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	connectSessions(t, host, peer, waitInvite(t, host))
	return host, peer, hostFake, peerFake
}

func newCallSession(t *testing.T, name string, devices media.Devices, ring time.Duration) *session.Session {
	t.Helper()
	s, err := session.New(session.Options{
		Identity: newIdentity(t),
		Name:     name,
		Tunnel:   rendezvous.Loopback{},
		Devices:  devices,
		RingWait: ring,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// waitCall drains events until the Call reaches the wanted state.
func waitCall(t *testing.T, s *session.Session, want session.CallState) session.CallChanged {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the Call was %v", want)
		}
		if cc, isCall := e.(session.CallChanged); isCall && cc.State == want {
			return cc
		}
	}
}

// waitMic drains events until the other side announces its microphone in the
// wanted state.
func waitMic(t *testing.T, s *session.Session, want bool) {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the other side's mic was %v", want)
		}
		if mc, isMedia := e.(session.MediaChanged); isMedia && mc.Mic == want {
			return
		}
	}
}

// waitCam drains events until the other side announces its camera in the
// wanted state.
func waitCam(t *testing.T, s *session.Session, want bool) {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the other side's camera was %v", want)
		}
		if mc, isMedia := e.(session.MediaChanged); isMedia && mc.Cam == want {
			return
		}
	}
}

// waitShare drains events until the other side announces its screen share in
// the wanted state.
func waitShare(t *testing.T, s *session.Session, want bool) {
	t.Helper()
	for {
		e, ok := nextEvent(t, s)
		if !ok {
			t.Fatalf("events closed before the other side's share was %v", want)
		}
		if mc, isMedia := e.(session.MediaChanged); isMedia && mc.Screen == want {
			return
		}
	}
}

// heard reports whether a fake speaker is dominated by the given tone.
func heard(f *media.Fake, tone float64) bool {
	return f.Heard().Power(tone) > 4*f.Heard().Power(tone*2.3)
}

// TestCallIsRungAnsweredAndHungUp is the whole happy path of a Call: it
// rings, it is answered, both sides hear the other's tone and nothing else,
// muting one side shows up on the other, hanging up returns both to text,
// and text still crosses afterwards.
func TestCallIsRungAnsweredAndHungUp(t *testing.T) {
	host, peer, hostFake, peerFake := callPair(t, 0)

	id, err := host.Call()
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := waitCall(t, host, session.Ringing); got.CallID != id {
		t.Fatalf("host is ringing call %s, want %s", got.CallID, id)
	}
	if got := waitCall(t, peer, session.Incoming); got.CallID != id {
		t.Fatalf("peer was rung for call %s, want %s", got.CallID, id)
	}

	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	// Long enough for a second's worth of frames to cross and be played.
	time.Sleep(time.Second)
	if !heard(peerFake, hostTone) {
		t.Errorf("the peer did not hear the host's tone")
	}
	if !heard(hostFake, peerTone) {
		t.Errorf("the host did not hear the peer's tone")
	}

	if err := host.Mute(true); err != nil {
		t.Fatalf("Mute: %v", err)
	}
	if !host.Muted() {
		t.Error("the host does not consider itself muted")
	}
	waitMic(t, peer, false)

	peerFake.Heard().Reset()
	time.Sleep(500 * time.Millisecond)
	if heard(peerFake, hostTone) {
		t.Error("the peer is still hearing a muted host")
	}

	if err := host.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	if got := waitCall(t, host, session.NoCall); got.Reason != session.CallEnded {
		t.Errorf("host ended the Call as %v, want %v", got.Reason, session.CallEnded)
	}
	if got := waitCall(t, peer, session.NoCall); got.Reason != session.CallEnded {
		t.Errorf("peer ended the Call as %v, want %v", got.Reason, session.CallEnded)
	}

	// The Session is still a Session.
	sent, err := host.SendText("still here")
	if err != nil {
		t.Fatalf("SendText after the Call: %v", err)
	}
	if got := waitText(t, peer); got.Body != "still here" {
		t.Fatalf("peer received %q after the Call", got.Body)
	}
	waitTextStatus(t, host, sent, session.TextDelivered)
}

// TestCallRejected checks a declined Call ends at both ends, with the reason
// the person gave.
func TestCallRejected(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if got := waitCall(t, peer, session.NoCall); got.Reason != session.CallDeclined {
		t.Errorf("peer ended the Call as %v, want %v", got.Reason, session.CallDeclined)
	}
	if got := waitCall(t, host, session.NoCall); got.Reason != session.CallDeclined {
		t.Errorf("host ended the Call as %v, want %v", got.Reason, session.CallDeclined)
	}
}

// TestCallTimesOut leaves a Call unanswered and checks both sides give up on
// it — the rung side saying so on the wire, the calling side counting for
// itself.
func TestCallTimesOut(t *testing.T) {
	host, peer, _, _ := callPair(t, 300*time.Millisecond)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if got := waitCall(t, peer, session.NoCall); got.Reason != session.CallTimedOut {
		t.Errorf("peer ended the Call as %v, want %v", got.Reason, session.CallTimedOut)
	}
	if got := waitCall(t, host, session.NoCall); got.Reason != session.CallTimedOut {
		t.Errorf("host ended the Call as %v, want %v", got.Reason, session.CallTimedOut)
	}
	// A Call that has timed out can be placed again.
	if _, err := host.Call(); err != nil {
		t.Fatalf("Call again: %v", err)
	}
}

// TestCallBusy checks a second Call arriving during the first is refused
// without disturbing it.
func TestCallBusy(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)

	if _, err := peer.Call(); err == nil {
		t.Fatal("the peer placed a Call while one was already ringing at it")
	}
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)
}

// TestCallDropsOnReconnect checks a Call does not quietly survive the
// connection being rebuilt underneath it.
func TestCallDropsOnReconnect(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	host.Sever()
	if got := waitCall(t, host, session.NoCall); got.Reason != session.CallLost {
		t.Errorf("host ended the Call as %v, want %v", got.Reason, session.CallLost)
	}
	if got := waitCall(t, peer, session.NoCall); got.Reason != session.CallLost {
		t.Errorf("peer ended the Call as %v, want %v", got.Reason, session.CallLost)
	}
	waitState(t, host, session.Connected)
	waitState(t, peer, session.Connected)
}

// TestSecondCall checks a Session that has already carried a Call can carry
// another: the transceivers are negotiated once and reused, so the second
// Call must come up without renegotiating anything.
func TestSecondCall(t *testing.T) {
	host, peer, _, peerFake := callPair(t, 0)

	for round := range 2 {
		if _, err := host.Call(); err != nil {
			t.Fatalf("round %d Call: %v", round, err)
		}
		waitCall(t, peer, session.Incoming)
		if err := peer.Answer(); err != nil {
			t.Fatalf("round %d Answer: %v", round, err)
		}
		waitCall(t, host, session.Active)
		waitCall(t, peer, session.Active)

		peerFake.Heard().Reset()
		deadline := time.Now().Add(5 * time.Second)
		for !heard(peerFake, hostTone) {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the peer never heard the host", round)
			}
			time.Sleep(20 * time.Millisecond)
		}

		if err := host.Hangup(); err != nil {
			t.Fatalf("round %d Hangup: %v", round, err)
		}
		waitCall(t, host, session.NoCall)
		waitCall(t, peer, session.NoCall)
	}
}

// deafDevices has no sound card, no camera and no display at all, the way a headless
// machine does not.
type deafDevices struct{}

func (deafDevices) Capture() (media.Source, error) { return nil, media.ErrNoDevices }
func (deafDevices) Playback() (media.Sink, error)  { return nil, media.ErrNoDevices }
func (deafDevices) Camera() (media.Camera, error)  { return nil, media.ErrNoCamera }
func (deafDevices) Screen() (media.Screen, error)  { return nil, media.ErrNoScreen }

// TestCallWithoutDevices checks a machine with no microphone still holds a
// Call — it simply cannot speak, and is refused the pretence of unmuting.
func TestCallWithoutDevices(t *testing.T) {
	host := newCallSession(t, "Alice", deafDevices{}, 0)
	peer := newCallSession(t, "Bob", &media.Fake{Tone: peerTone}, 0)
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	connectSessions(t, host, peer, waitInvite(t, host))

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	// The other side is told this one is not speaking, and turning on a
	// microphone or a camera that does not exist is refused rather than
	// announced.
	waitMic(t, peer, false)
	if err := host.Mute(false); err == nil {
		t.Fatal("a Session with no microphone unmuted one")
	}
	if err := host.Camera(true); err == nil {
		t.Fatal("a Session with no camera turned one on")
	}
	if host.CameraOn() {
		t.Error("a camera that would not open reads as on")
	}
}

// TestCallNeedsAConnectedSession checks the Call commands refuse where there
// is nothing to call.
func TestCallNeedsAConnectedSession(t *testing.T) {
	s := newCallSession(t, "Alice", &media.Fake{}, 0)
	if _, err := s.Call(); err == nil {
		t.Error("an Idle Session placed a Call")
	}
	if err := s.Answer(); err == nil {
		t.Error("an Idle Session answered a Call")
	}
	if err := s.Hangup(); err == nil {
		t.Error("an Idle Session hung up a Call")
	}
	if err := s.Mute(true); err == nil {
		t.Error("an Idle Session muted a Call")
	}
}

// TestCallCarriesVideo is the camera's whole path: turned on inside an Active
// Call it shows up on the other side — as a media state the other end can see
// and as pictures it can paint — and turned off it stops, both the sending and
// the saying so.
func TestCallCarriesVideo(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	// A Call starts with both cameras off: nobody's camera comes on because
	// a Call did.
	if host.CameraOn() {
		t.Error("the host's camera is on before anyone turned it on")
	}
	if img := frame(peer, 500*time.Millisecond); img != nil {
		t.Error("the peer is seeing video from a camera nobody turned on")
	}

	if err := host.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	if !host.CameraOn() {
		t.Error("the host's camera is off after being turned on")
	}
	waitCam(t, peer, true)

	img := frame(peer, 10*time.Second)
	if img == nil {
		t.Fatal("the peer never saw the host's camera")
	}
	if share := media.Tinted(img, hostTint); share < 0.7 {
		t.Errorf("only %.0f%% of the picture is the host's tint", share*100)
	}
	if share := media.Tinted(img, peerTint); share > 0.1 {
		t.Errorf("%.0f%% of the picture is the peer's own tint", share*100)
	}

	if err := host.Camera(false); err != nil {
		t.Fatalf("Camera(false): %v", err)
	}
	if host.CameraOn() {
		t.Error("the host's camera is on after being turned off")
	}
	waitCam(t, peer, false)

	// Whatever was already in flight arrives; after that, nothing.
	drainFrames(peer, time.Second)
	if img := frame(peer, time.Second); img != nil {
		t.Error("the peer is still seeing a camera that was turned off")
	}

	// Hanging up leaves the Session up for text, camera or no camera.
	if err := host.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	waitCall(t, host, session.NoCall)
	waitCall(t, peer, session.NoCall)
	if err := host.Camera(true); err == nil {
		t.Fatal("a camera was turned on with no Call to turn it on in")
	}
}

// frame waits up to wait for one decoded picture, or reports none arriving.
func frame(s *session.Session, wait time.Duration) *image.RGBA {
	select {
	case img := <-s.Frames():
		return img
	case <-time.After(wait):
		return nil
	}
}

// drainFrames throws away whatever arrives for a while, so that an assertion
// about nothing arriving afterwards means something.
func drainFrames(s *session.Session, wait time.Duration) {
	deadline := time.After(wait)
	for {
		select {
		case <-s.Frames():
		case <-deadline:
			return
		}
	}
}

// TestCallShowsThisSideItsOwnCamera is the local preview's path: a camera
// turned on inside a Call is put in front of the side sending it, in its own
// tint and at its own size, and stops being when the camera goes off. It is
// what a picture-in-picture paints, and it never leaves the machine.
func TestCallShowsThisSideItsOwnCamera(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	// A Call with no camera on shows this side nothing of itself.
	if img := localFrame(host, 500*time.Millisecond); img != nil {
		t.Error("the host is seeing itself with no camera turned on")
	}

	if err := host.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	img := localFrame(host, 10*time.Second)
	if img == nil {
		t.Fatal("the host never saw its own camera")
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != media.VideoWidth || h != media.VideoHeight {
		t.Errorf("the local picture is %dx%d, want %dx%d", w, h, media.VideoWidth, media.VideoHeight)
	}
	// Nothing is encoded on the way to a local picture, so its own tint
	// arrives almost exactly and the other side's not at all.
	if share := media.Tinted(img, hostTint); share < 0.85 {
		t.Errorf("only %.0f%% of the local picture is the host's own tint", share*100)
	}
	if share := media.Tinted(img, peerTint); share > 0.1 {
		t.Errorf("%.0f%% of the local picture is the peer's tint", share*100)
	}

	if err := host.Camera(false); err != nil {
		t.Fatalf("Camera(false): %v", err)
	}
	drainLocal(host, time.Second)
	if img := localFrame(host, time.Second); img != nil {
		t.Error("the host is still seeing a camera it turned off")
	}
}

// localFrame waits up to wait for one picture of this side's own camera.
func localFrame(s *session.Session, wait time.Duration) *image.RGBA {
	select {
	case img := <-s.LocalFrames():
		return img
	case <-time.After(wait):
		return nil
	}
}

// drainLocal throws away whatever local pictures arrive for a while, so that
// an assertion about nothing arriving afterwards means something.
func drainLocal(s *session.Session, wait time.Duration) {
	deadline := time.After(wait)
	for {
		select {
		case <-s.LocalFrames():
		case <-deadline:
			return
		}
	}
}

// TestCallCarriesAScreenShare is the screen's whole path: shared inside an
// Active Call it shows up on the other side — as a media state they can see and
// as pictures they can paint — it arrives alongside the camera rather than in
// place of it, and stopping the share stops both the sending and the saying so.
func TestCallCarriesAScreenShare(t *testing.T) {
	host, peer, _, _ := callPair(t, 0)

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)
	waitCall(t, peer, session.Active)

	// A Call starts with nobody sharing anything.
	if host.Sharing() {
		t.Error("the host is sharing a screen before anyone asked it to")
	}
	if img := screenFrame(peer, 500*time.Millisecond); img != nil {
		t.Error("the peer is seeing a screen nobody shared")
	}

	// The camera goes on first, so that the share has something to arrive
	// beside: the two streams must not be mistaken for each other.
	if err := host.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	waitCam(t, peer, true)

	if err := host.Share(true); err != nil {
		t.Fatalf("Share(true): %v", err)
	}
	if !host.Sharing() {
		t.Error("the host is not sharing after starting a share")
	}
	waitShare(t, peer, true)

	shared := screenFrame(peer, 20*time.Second)
	if shared == nil {
		t.Fatal("the peer never saw the host's screen")
	}
	if share := media.Tinted(shared, hostScreenTint); share < 0.7 {
		t.Errorf("only %.0f%% of the picture is the host's screen tint", share*100)
	}
	if share := media.Tinted(shared, hostTint); share > 0.1 {
		t.Errorf("%.0f%% of the shared screen is the host's camera", share*100)
	}

	// And the camera is still arriving on its own channel, unbothered.
	seen := frame(peer, 20*time.Second)
	if seen == nil {
		t.Fatal("the host's camera stopped arriving once a screen was shared")
	}
	if share := media.Tinted(seen, hostTint); share < 0.7 {
		t.Errorf("only %.0f%% of the camera picture is the host's camera tint", share*100)
	}

	if err := host.Share(false); err != nil {
		t.Fatalf("Share(false): %v", err)
	}
	if host.Sharing() {
		t.Error("the host is still sharing after stopping")
	}
	waitShare(t, peer, false)

	// Whatever was already in flight arrives; after that, nothing.
	drainScreenFrames(peer, time.Second)
	if img := screenFrame(peer, time.Second); img != nil {
		t.Error("the peer is still seeing a screen that stopped being shared")
	}
	// The camera survived the share ending.
	if !host.CameraOn() {
		t.Error("stopping the share turned the host's camera off")
	}

	// Hanging up leaves the Session up for text, and there is nothing left to
	// share into.
	if err := host.Hangup(); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	waitCall(t, host, session.NoCall)
	waitCall(t, peer, session.NoCall)
	if err := host.Share(true); err == nil {
		t.Fatal("a screen was shared with no Call to share it into")
	}
}

// TestShareWithoutADisplay checks a machine with nothing to share says so and
// keeps saying so, rather than announcing a share the other side would sit
// waiting on.
func TestShareWithoutADisplay(t *testing.T) {
	host := newCallSession(t, "Alice", deafDevices{}, 0)
	peer := newCallSession(t, "Bob", &media.Fake{}, 0)
	if err := host.Host(t.Context()); err != nil {
		t.Fatalf("Host: %v", err)
	}
	waitState(t, host, session.Hosting)
	connectSessions(t, host, peer, waitInvite(t, host))

	if _, err := host.Call(); err != nil {
		t.Fatalf("Call: %v", err)
	}
	waitCall(t, peer, session.Incoming)
	if err := peer.Answer(); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	waitCall(t, host, session.Active)

	if err := host.Share(true); err == nil {
		t.Fatal("a machine with no display shared one")
	}
	if host.Sharing() {
		t.Error("a share that would not start reads as sharing")
	}
	// And again, now that the Session knows there is nothing here.
	if err := host.Share(true); err == nil {
		t.Fatal("a machine with no display shared one on the second ask")
	}
}

// screenFrame waits up to wait for one decoded picture of the other side's
// shared screen, or reports none arriving.
func screenFrame(s *session.Session, wait time.Duration) *image.RGBA {
	select {
	case img := <-s.ScreenFrames():
		return img
	case <-time.After(wait):
		return nil
	}
}

// drainScreenFrames throws away whatever arrives for a while, so that an
// assertion about nothing arriving afterwards means something.
func drainScreenFrames(s *session.Session, wait time.Duration) {
	deadline := time.After(wait)
	for {
		select {
		case <-s.ScreenFrames():
		case <-deadline:
			return
		}
	}
}
