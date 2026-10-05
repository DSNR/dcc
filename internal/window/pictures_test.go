package window

import (
	"image"
	"testing"

	"gioui.org/app"
)

// Which picture the window is for, without a window. A real window needs a
// real display and a real camera; the choice it makes between three streams
// does not, so every state is reached by hand here — including the ones a
// participant would need a spare camera and a second screen to produce.

// TestTheOtherSideFillsTheWindow checks the large picture is their camera, and
// their shared screen instead while they are sharing one.
func TestTheOtherSideFillsTheWindow(t *testing.T) {
	theirs, theirScreen := frame(640, 480), frame(1280, 720)
	w := &Window{win: new(app.Window)}

	if large, _ := w.pictures(); large != nil {
		t.Error("the window has a picture before either stream delivered one")
	}

	w.arrives(w.keepCameraLocked, theirs)
	if large, _ := w.pictures(); large != theirs {
		t.Error("their camera does not fill the window")
	}

	// A share with no picture yet leaves their face up rather than blacking
	// the window out while the first screen frame arrives.
	w.Sharing(true)
	if large, _ := w.pictures(); large != theirs {
		t.Error("their camera went away before their screen arrived")
	}
	w.arrives(w.keepScreenLocked, theirScreen)
	if large, _ := w.pictures(); large != theirScreen {
		t.Error("their screen does not take the window over while they share it")
	}
	w.Sharing(false)
	if large, _ := w.pictures(); large != theirs {
		t.Error("their camera does not have the window back")
	}
}

// TestTheThumbnailIsOnlyThereWhileTheCameraIs checks this side's own picture is
// in the corner exactly while its camera is on — and that a camera coming back
// on shows what it sees now, not the face it went off on.
func TestTheThumbnailIsOnlyThereWhileTheCameraIs(t *testing.T) {
	first, second := frame(320, 240), frame(320, 240)
	w := &Window{win: new(app.Window)}

	// A picture that arrived before anyone said the camera was on is not
	// painted: the Call's own camera state is what decides, not a frame.
	w.arrives(w.keepLocalLocked, first)
	if _, small := w.pictures(); small != nil {
		t.Error("there is a thumbnail with the camera off")
	}

	w.Camera(true)
	w.arrives(w.keepLocalLocked, first)
	if _, small := w.pictures(); small != first {
		t.Error("this side's camera is not in the corner with the camera on")
	}

	w.Camera(false)
	if _, small := w.pictures(); small != nil {
		t.Error("the thumbnail stayed after the camera was turned off")
	}
	w.Camera(true)
	if _, small := w.pictures(); small != nil {
		t.Error("the camera came back on showing the picture it went off with")
	}
	w.arrives(w.keepLocalLocked, second)
	if _, small := w.pictures(); small != second {
		t.Error("the camera coming back on does not show what it sees now")
	}
}

// arrives is one frame arriving on a stream, by the same door feed uses: the
// stream's own keeper, with the window's lock held.
func (w *Window) arrives(keep func(*image.RGBA), img *image.RGBA) {
	w.mu.Lock()
	defer w.mu.Unlock()
	keep(img)
}

// frame is one video frame of a given size, its content beside the point.
func frame(w, h int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, w, h))
}
