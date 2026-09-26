package cli_test

import (
	"testing"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/session"
)

// TestCallControlsReachTheSession checks each Call command reaches the
// Session, and that the screen says what the Call is doing at each step.
func TestCallControlsReachTheSession(t *testing.T) {
	h, f := connected(t)

	h.submit("/call")
	h.until("the Call to be placed", func() bool {
		calls, _, _, _ := f.callCounts()
		return calls == 1
	})
	h.mustSee("Calling")
	f.emit(session.CallChanged{State: session.Ringing, CallID: testCallID})

	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.mustSee("In a Call")

	h.submit("/mute")
	h.until("the microphone to be muted", func() bool { return f.Muted() })
	h.mustSee("Microphone muted")

	h.submit("/unmute")
	h.until("the microphone to be live", func() bool { return !f.Muted() })
	h.mustSee("Microphone live")

	h.submit("/hangup")
	h.until("the Call to be hung up", func() bool {
		_, _, _, hangups := f.callCounts()
		return hangups == 1
	})
	f.emit(session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	h.mustSee("The Call ended")
}

// TestIncomingCallIsAnsweredOrRejected checks a ringing Call is put in front
// of the person with both ways out of it, and that each reaches the Session.
func TestIncomingCallIsAnsweredOrRejected(t *testing.T) {
	h, f := connected(t)

	f.emit(session.CallChanged{State: session.Incoming, CallID: testCallID})
	h.mustSee("is calling", "/answer", "/reject")

	h.submit("/answer")
	h.until("the Call to be answered", func() bool {
		_, answers, _, _ := f.callCounts()
		return answers == 1
	})

	f.emit(session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	f.emit(session.CallChanged{State: session.Incoming, CallID: testCallID})
	h.submit("/reject")
	h.until("the Call to be rejected", func() bool {
		_, _, rejects, _ := f.callCounts()
		return rejects == 1
	})
}

// TestCallCommandsRefuseWithoutACall checks the controls say so rather than
// reaching a Session that has nothing to do with them.
func TestCallCommandsRefuseWithoutACall(t *testing.T) {
	h, f := connected(t)

	h.submit("/answer")
	h.mustSee("There is no Call to answer.")
	h.submit("/reject")
	h.mustSee("There is no Call to reject.")
	h.submit("/hangup")
	h.mustSee("There is no Call to hang up.")
	h.submit("/mute")
	h.mustSee("There is no Call to mute.")

	h.settle()
	if calls, answers, rejects, hangups := f.callCounts(); calls+answers+rejects+hangups != 0 {
		t.Fatalf("the Session was asked to do something: %d calls, %d answers, %d rejects, %d hangups",
			calls, answers, rejects, hangups)
	}
}

// TestCallNeedsAConnection checks calling with nothing connected says so.
func TestCallNeedsAConnection(t *testing.T) {
	f := newFake()
	h := newHarness(t, cli.Options{Name: "scott", New: oneSession(f)})
	h.submit("/call")
	h.mustSee("There is nobody to call")
}

// TestRemoteMuteIsShown checks the other side muting is said out loud —
// deliberate silence and a broken microphone sound identical otherwise.
func TestRemoteMuteIsShown(t *testing.T) {
	h, f := connected(t)
	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.mustSee("In a Call")

	f.emit(session.MediaChanged{Mic: false})
	h.mustSee(`"Alice" muted their microphone`)

	f.emit(session.MediaChanged{Mic: true})
	h.mustSee(`"Alice" unmuted`)
}

// testCallID stands in for a minted Call id; the TUI only ever passes it
// through.
const testCallID = "00000000-0000-7000-8000-000000000001"

// TestCameraControlsReachTheSession checks /camera on and /camera off reach
// the Session and say what happened, and that a camera command with nothing
// sensible after it explains itself instead.
func TestCameraControlsReachTheSession(t *testing.T) {
	h, f := connected(t)
	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.mustSee("In a Call")

	h.submit("/camera on")
	h.until("the camera to be turned on", func() bool { return f.CameraOn() })
	h.mustSee("Camera on")

	h.submit("/camera off")
	h.until("the camera to be turned off", func() bool { return !f.CameraOn() })
	h.mustSee("Camera off")

	h.submit("/camera")
	h.mustSee("/camera on — or /camera off.")
	h.settle()
	if got := f.cameraCalls(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("the Session was asked for %v, want on then off", got)
	}
}

// TestCameraNeedsACall checks turning a camera on outside a Call says so
// rather than reaching a Session that has nothing to point it at.
func TestCameraNeedsACall(t *testing.T) {
	h, f := connected(t)
	h.submit("/camera on")
	h.mustSee("There is no Call to turn a camera on in.")
	h.settle()
	if got := f.cameraCalls(); len(got) != 0 {
		t.Fatalf("the Session was asked for %v with no Call", got)
	}
}

// TestRemoteCameraIsShown checks the other side's camera coming on and going
// off is said out loud — a black video window and a camera nobody turned on
// look identical otherwise.
func TestRemoteCameraIsShown(t *testing.T) {
	h, f := connected(t)
	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.mustSee("In a Call")

	f.emit(session.MediaChanged{Mic: true, Cam: true})
	h.mustSee(`"Alice" turned their camera on`)

	f.emit(session.MediaChanged{Mic: true, Cam: false})
	h.mustSee(`"Alice" turned their camera off`)
}

// TestVideoWindowOpensWithTheCall checks the video window is up for exactly as
// long as the Call is: opened when it goes Active, closed when it ends, and
// the terminal carrying on either way.
func TestVideoWindowOpensWithTheCall(t *testing.T) {
	h, f, windows := connectedWithVideo(t, nil)
	if opened, _ := windows.counts(); opened != 0 {
		t.Fatalf("%d video windows opened before there was a Call", opened)
	}

	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.until("the video window to open", func() bool {
		opened, _ := windows.counts()
		return opened == 1
	})
	if got := windows.title(); got != "dcc — Alice" {
		t.Errorf("the window is titled %q", got)
	}

	f.emit(session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	h.until("the video window to close", func() bool {
		_, closed := windows.counts()
		return closed == 1
	})
	h.mustSee("The Call ended")

	// The terminal is still a terminal.
	h.submit("still here")
	h.until("the message to be sent", func() bool { return len(f.texts()) == 1 })
}

// TestVideoWindowClosedByHand checks closing the video window mid-Call is
// noticed rather than leaving the terminal thinking it still has one — the
// Call carries on, and so does the terminal.
func TestVideoWindowClosedByHand(t *testing.T) {
	h, f, windows := connectedWithVideo(t, nil)
	f.emit(session.CallChanged{State: session.Active, CallID: testCallID})
	h.until("the video window to open", func() bool {
		opened, _ := windows.counts()
		return opened == 1
	})

	// The participant clicks the window's close box.
	windows.Close()
	h.mustSee("The video window is closed")

	// The Call is still a Call.
	h.submit("/mute")
	h.until("the microphone to be muted", func() bool { return f.Muted() })
}
