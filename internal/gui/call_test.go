package gui_test

import (
	"image"
	"testing"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/session"
)

// TestCallControlsReachTheSession checks each Call control reaches the Session
// and that the window says what the Call is doing at every step.
func TestCallControlsReachTheSession(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	m.Host()
	f.connect(t, m, "Alice")

	if !m.Screen().Controls.Call {
		t.Fatal("a connected Session cannot place a Call")
	}
	m.Call()
	waitFor(t, m, "the Call to be placed", func(gui.Screen) bool {
		calls, _, _, _ := f.callCounts()
		return calls == 1
	})
	waitSaid(t, m, "Calling")

	f.post(t, session.CallChanged{State: session.Ringing, CallID: testCallID})
	waitFor(t, m, "hanging up to become available", func(s gui.Screen) bool { return s.Controls.Hangup })

	f.post(t, session.CallChanged{State: session.Active, CallID: testCallID})
	s := waitFor(t, m, "the Call's own controls", func(s gui.Screen) bool {
		return s.Controls.Mute && s.Controls.Camera && s.Controls.Share
	})
	if s.Controls.Call {
		t.Error("a Call can be placed while one is already running")
	}

	m.Mute(true)
	waitFor(t, m, "the microphone to be muted", func(s gui.Screen) bool { return s.Call.Muted })
	if !f.Muted() {
		t.Error("the Session was not asked to mute")
	}
	m.Mute(false)
	waitFor(t, m, "the microphone to be live", func(s gui.Screen) bool { return !s.Call.Muted })

	m.Camera(true)
	waitFor(t, m, "the camera to come on", func(s gui.Screen) bool { return s.Call.CameraOn })
	waitSaid(t, m, "Camera on")
	m.Camera(false)
	waitFor(t, m, "the camera to go off", func(s gui.Screen) bool { return !s.Call.CameraOn })

	m.Share(true)
	waitFor(t, m, "the screen to be shared", func(s gui.Screen) bool { return s.Call.Sharing })
	waitSaid(t, m, "Sharing your whole screen")
	m.Share(false)
	waitFor(t, m, "the share to stop", func(s gui.Screen) bool { return !s.Call.Sharing })

	if got := f.cameraAsked(); len(got) != 2 || !got[0] || got[1] {
		t.Errorf("the Session was asked for cameras %v, want on then off", got)
	}
	if got := f.shareAsked(); len(got) != 2 || !got[0] || got[1] {
		t.Errorf("the Session was asked for shares %v, want on then off", got)
	}

	m.Hangup()
	waitFor(t, m, "the Call to be hung up", func(gui.Screen) bool {
		_, _, _, hangups := f.callCounts()
		return hangups == 1
	})
	f.post(t, session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	waitSaid(t, m, "The Call ended")
	waitFor(t, m, "the controls to go back to text", func(s gui.Screen) bool {
		return s.Controls.Call && !s.Controls.Hangup && !s.Controls.Mute
	})
}

// TestIncomingCallIsAnsweredOrRejected checks a ringing Call is put in front of
// the participant with both ways out of it, and that each reaches the Session.
func TestIncomingCallIsAnsweredOrRejected(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	m.Host()
	f.connect(t, m, "Alice")

	f.post(t, session.CallChanged{State: session.Incoming, CallID: testCallID})
	waitFor(t, m, "the two ways out of a ringing Call", func(s gui.Screen) bool {
		return s.Controls.Answer && s.Controls.Reject
	})
	waitSaid(t, m, "is calling")

	m.Answer()
	waitFor(t, m, "the Call to be answered", func(gui.Screen) bool {
		_, answers, _, _ := f.callCounts()
		return answers == 1
	})

	f.post(t, session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	f.post(t, session.CallChanged{State: session.Incoming, CallID: testCallID})
	waitFor(t, m, "a second ringing Call", func(s gui.Screen) bool { return s.Controls.Reject })
	m.Reject()
	waitFor(t, m, "the Call to be rejected", func(gui.Screen) bool {
		_, _, rejects, _ := f.callCounts()
		return rejects == 1
	})
}

// TestCallControlsAreDeadWithoutACall checks the Call's own controls are not
// live while there is no Call, and that pressing one anyway reaches nothing.
func TestCallControlsAreDeadWithoutACall(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	m.Host()
	f.connect(t, m, "Alice")

	s := m.Screen()
	if s.Controls.Answer || s.Controls.Reject || s.Controls.Hangup ||
		s.Controls.Mute || s.Controls.Camera || s.Controls.Share {
		t.Fatalf("a Session with no Call offers %+v", s.Controls)
	}

	m.Answer()
	m.Reject()
	m.Hangup()
	m.Mute(true)
	m.Camera(true)
	m.Share(true)
	waitSaid(t, m, "There is no Call")

	if calls, answers, rejects, hangups := f.callCounts(); calls+answers+rejects+hangups != 0 {
		t.Errorf("the Session was asked to do something: %d calls, %d answers, %d rejects, %d hangups",
			calls, answers, rejects, hangups)
	}
	if got := f.cameraAsked(); len(got) != 0 {
		t.Errorf("the Session was asked for a camera %v with no Call to point it at", got)
	}
	if got := f.shareAsked(); len(got) != 0 {
		t.Errorf("the Session was asked to share %v with no Call to share into", got)
	}
}

// TestCallNeedsAConnection checks calling with nothing connected says so.
func TestCallNeedsAConnection(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	if m.Screen().Controls.Call {
		t.Fatal("a Call can be placed with nobody to call")
	}
	m.Call()
	waitSaid(t, m, "There is nobody to call")
	if calls, _, _, _ := f.callCounts(); calls != 0 {
		t.Error("a Session was asked to call with nobody on the other end")
	}
}

// TestRemoteMediaStateIsShown checks what the other side has turned on is both
// said out loud and on display: deliberate silence and a broken microphone look
// identical otherwise, and so do a camera nobody turned on and a black picture.
func TestRemoteMediaStateIsShown(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	inCall(t, m, f, "Alice")

	f.post(t, session.MediaChanged{Mic: false})
	waitSaid(t, m, `"Alice" muted their microphone`)
	waitFor(t, m, "their microphone to read as muted", func(s gui.Screen) bool { return !s.Call.TheirMic })

	f.post(t, session.MediaChanged{Mic: true, Cam: true})
	waitSaid(t, m, `"Alice" turned their camera on`)
	waitFor(t, m, "their camera to read as on", func(s gui.Screen) bool {
		return s.Call.TheirCam && s.Call.TheirMic
	})

	f.post(t, session.MediaChanged{Mic: true, Cam: true, Screen: true})
	waitSaid(t, m, `"Alice" is sharing their screen`)
	waitFor(t, m, "their share to read as running", func(s gui.Screen) bool { return s.Call.TheirScreen })

	f.post(t, session.MediaChanged{Mic: true})
	waitSaid(t, m, `"Alice" stopped sharing their screen`)
	waitFor(t, m, "their streams to read as off", func(s gui.Screen) bool {
		return !s.Call.TheirScreen && !s.Call.TheirCam
	})
}

// TestVideoIsRemoteLargeAndLocalSmall is the picture-in-picture: the other
// side's camera fills the video area, this side's own sits in the corner, and a
// share takes the large picture over while it runs — which is what somebody
// pointing at something needs.
func TestVideoIsRemoteLargeAndLocalSmall(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	inCall(t, m, f, "Alice")

	// A Call with nobody's camera on has no picture, and says why rather than
	// showing a black rectangle nobody can explain.
	s := m.Screen()
	if s.Call.Large != nil || s.Call.Small != nil {
		t.Fatal("there are pictures in a Call where no camera is on")
	}
	if s.Call.Waiting == "" {
		t.Error("a Call with no picture says nothing about why")
	}

	theirs := picture(640, 480)
	f.post(t, session.MediaChanged{Mic: true, Cam: true})
	f.deliver(t, f.frames, theirs)
	waitFor(t, m, "their camera to fill the video area", func(s gui.Screen) bool {
		return s.Call.Large == theirs
	})
	if got := m.Screen().Call.Waiting; got != "" {
		t.Errorf("a Call with a picture still says %q", got)
	}

	mine := picture(320, 240)
	m.Camera(true)
	f.deliver(t, f.local, mine)
	waitFor(t, m, "this side's own camera in the corner", func(s gui.Screen) bool {
		return s.Call.Small == mine && s.Call.Large == theirs
	})

	// Their share takes the large picture over, and their camera comes back to
	// it when they stop.
	theirScreen := picture(1280, 720)
	f.post(t, session.MediaChanged{Mic: true, Cam: true, Screen: true})
	f.deliver(t, f.screens, theirScreen)
	waitFor(t, m, "their screen to take the video area over", func(s gui.Screen) bool {
		return s.Call.Large == theirScreen
	})
	f.post(t, session.MediaChanged{Mic: true, Cam: true})
	waitFor(t, m, "their camera to have the video area back", func(s gui.Screen) bool {
		return s.Call.Large == theirs
	})

	// A camera turned off takes its picture with it, either side's.
	m.Camera(false)
	waitFor(t, m, "the corner to empty", func(s gui.Screen) bool { return s.Call.Small == nil })
	f.post(t, session.MediaChanged{Mic: true})
	waitFor(t, m, "the video area to empty", func(s gui.Screen) bool {
		return s.Call.Large == nil && s.Call.Waiting != ""
	})
}

// TestVideoKeepsTheNewestPicture checks the window paints what is happening
// now: pictures arriving faster than frames are drawn leave the newest on
// screen rather than a queue of stale ones, which is what lets 30 fps of video
// through a window that is painting at its own rate.
func TestVideoKeepsTheNewestPicture(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	inCall(t, m, f, "Alice")
	f.post(t, session.MediaChanged{Mic: true, Cam: true})

	var last *image.RGBA
	for range 30 {
		last = picture(640, 480)
		f.deliver(t, f.frames, last)
	}
	waitFor(t, m, "the newest picture to be the one on screen", func(s gui.Screen) bool {
		return s.Call.Large == last
	})
}

// TestVideoStopsWithTheCall checks the pictures go when the Call does: a
// hung-up Call leaves nothing on screen, and nothing that arrives afterwards
// puts anything back.
func TestVideoStopsWithTheCall(t *testing.T) {
	m, f := newModel(t, gui.Options{})
	inCall(t, m, f, "Alice")
	f.post(t, session.MediaChanged{Mic: true, Cam: true})
	f.deliver(t, f.frames, picture(640, 480))
	waitFor(t, m, "a picture to arrive", func(s gui.Screen) bool { return s.Call.Large != nil })

	f.post(t, session.CallChanged{State: session.NoCall, Reason: session.CallEnded})
	waitFor(t, m, "the Call's pictures to go with it", func(s gui.Screen) bool {
		return s.Call.State == session.NoCall && s.Call.Large == nil && s.Call.Small == nil
	})

	f.deliver(t, f.frames, picture(640, 480))
	if got := m.Screen().Call.Large; got != nil {
		t.Error("a picture arriving after the Call ended is on screen")
	}
}

// inCall takes a Model from nothing to an Active Call, which is where the media
// controls and the video live.
func inCall(t *testing.T, m *gui.Model, f *fakeSession, name string) {
	t.Helper()
	m.Host()
	f.connect(t, m, name)
	f.post(t, session.CallChanged{State: session.Active, CallID: testCallID})
	waitFor(t, m, "the Call to go Active", func(s gui.Screen) bool {
		return s.Call.State == session.Active
	})
}

// picture is one frame of video, distinguishable from every other by its
// address alone — which is all an assertion about which picture is on screen
// needs.
func picture(width, height int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, width, height))
}
