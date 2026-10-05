package interop_test

import (
	"image/color"
	"testing"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/session"
)

// The tints the two sides' fake cameras and fake screens paint. Each is
// unmistakable for the others, which is what turns "the window is showing the
// terminal's camera, and its own camera beside it" into something a test can
// assert without eyes.
var (
	windowTint         = color.RGBA{R: 200, G: 40, B: 40, A: 0xFF}
	terminalTint       = color.RGBA{R: 40, G: 40, B: 200, A: 0xFF}
	terminalScreenTint = color.RGBA{R: 40, G: 190, B: 190, A: 0xFF}
)

// TestWindowCallsTerminal is the Call between the two clients, end to end: a
// window rings a terminal, the terminal answers, what each side turns on shows
// up on the other, the window paints the terminal's camera large with its own
// camera in the corner, a shared screen takes the large picture over while it
// runs, and hanging up leaves both sides connected for text.
func TestWindowCallsTerminal(t *testing.T) {
	window := newLive(t, "Ada", &media.Fake{Tint: windowTint})
	terminal := newTerminal(t, "Grace", &media.Fake{
		Tone:       1100,
		Tint:       terminalTint,
		ScreenTint: terminalScreenTint,
	})

	window.Host()
	s := waitFor(t, window, "the Invite", func(s gui.Screen) bool { return s.Invite != "" })
	terminal.submit("/connect " + s.Invite)
	terminal.see(t, "Security Code")
	waitFor(t, window, "the Host's Security Code", func(s gui.Screen) bool { return s.Prompt != nil })
	window.Accept()
	terminal.submit("yes")
	terminal.see(t, "Connected")
	waitFor(t, window, "the window to be able to send", func(s gui.Screen) bool { return s.Controls.Send })

	// The window rings, the terminal picks up, and both ends agree there is a
	// Call running.
	window.Call()
	terminal.see(t, "is calling")
	terminal.submit("/answer")
	terminal.waitForMedia(t, "the window's Call to go Active", func() bool {
		return window.Screen().Call.State == session.Active
	})
	terminal.see(t, "In a Call")

	// Each side's camera shows up on the other: as a state the other end can
	// read, and — in the window — as pictures it paints.
	terminal.submit("/camera on")
	terminal.waitFor(t, "the terminal's camera to be announced", func() bool {
		return window.Screen().Call.TheirCam
	})
	window.Camera(true)
	terminal.see(t, `"Ada" turned their camera on`)
	terminal.waitForMedia(t, "pictures in the window, both ways", func() bool {
		s := window.Screen()
		return s.Call.Large != nil && s.Call.Small != nil
	})

	s = window.Screen()
	if share := media.Tinted(s.Call.Large, terminalTint); share < 0.7 {
		t.Errorf("only %.0f%% of the large picture is the terminal's camera", share*100)
	}
	if share := media.Tinted(s.Call.Small, windowTint); share < 0.85 {
		t.Errorf("only %.0f%% of the picture-in-picture is the window's own camera", share*100)
	}
	if s.Call.Waiting != "" {
		t.Errorf("a Call with pictures in it still says %q", s.Call.Waiting)
	}

	// A shared screen takes the large picture over while it runs, and the
	// camera has it back when the share stops.
	terminal.submit("/share")
	terminal.waitForMedia(t, "the terminal's screen to fill the window", func() bool {
		s := window.Screen()
		return s.Call.TheirScreen && s.Call.Large != nil &&
			media.Tinted(s.Call.Large, terminalScreenTint) > 0.7
	})
	terminal.submit("/stopshare")
	terminal.waitForMedia(t, "the terminal's camera to have the picture back", func() bool {
		s := window.Screen()
		return !s.Call.TheirScreen && s.Call.Large != nil &&
			media.Tinted(s.Call.Large, terminalTint) > 0.7
	})

	// And the window can share its screen the other way, which the terminal
	// reads as a state whether or not it has a window to paint it in.
	window.Share(true)
	terminal.see(t, `"Ada" is sharing their screen`)
	window.Share(false)
	terminal.see(t, `"Ada" stopped sharing their screen`)

	// Muting the window is something the terminal can see, which is the whole
	// point of announcing it.
	window.Mute(true)
	terminal.see(t, `"Ada" muted their microphone`)

	// Hanging up from the window ends the Call at both ends and leaves the
	// Session up for text.
	window.Hangup()
	terminal.see(t, "The Call ended")
	terminal.waitFor(t, "the window to leave the Call", func() bool {
		s := window.Screen()
		return s.Call.State == session.NoCall && s.Call.Large == nil && s.Call.Small == nil
	})
	if sent := window.Send("still here 👋"); !sent {
		t.Fatal("the window would not send after the Call")
	}
	terminal.see(t, "still here 👋")
}
