package videoview

import (
	"fmt"
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// The Call's picture, laid out without a window. Gio will draw into an op.Ops
// with no display and no GPU underneath it, so every combination of pictures a
// window can be holding is drawn here — which is what catches a panic or an
// impossible constraint in a state that is awkward to reach by hand, such as a
// thumbnail in a window smaller than the thumbnail.

// TestLaysOutEveryPicture draws each combination of large picture, thumbnail
// and empty area, at a usual window size and at a cramped one.
func TestLaysOutEveryPicture(t *testing.T) {
	theirs := frame(640, 480)
	mine := frame(320, 240)
	sizes := []image.Point{
		{X: 640, Y: 480},
		// Smaller than the thumbnail's own frame, which is where a layout that
		// only ever ran at a sensible size comes apart.
		{X: 80, Y: 60},
	}
	// A window that says nothing where a picture would be, and one that says
	// what it is waiting for, are both laid out.
	said := func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X/2, 20)}
	}
	shows := []struct {
		name         string
		large, small *image.RGBA
		empty        layout.Widget
	}{
		{name: "nothing"},
		{name: "nothing, said out loud", empty: said},
		{name: "their picture", large: theirs},
		{name: "their picture and mine", large: theirs, small: mine},
		{name: "mine alone", small: mine},
		{name: "mine alone, over a line about it", small: mine, empty: said},
	}
	for _, show := range shows {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s at %dx%d", show.name, size.X, size.Y), func(t *testing.T) {
				dims := Layout(newContext(size), show.large, show.small, show.empty)
				if dims.Size.X > size.X || dims.Size.Y > size.Y {
					t.Errorf("the window laid out %v in a %v frame", dims.Size, size)
				}
			})
		}
	}
}

// TestThumbnailIsASmallCornerOfTheWindow checks the thumbnail keeps its
// picture's shape, stays a corner of the window rather than most of it, and
// never asks for more room than there is.
func TestThumbnailIsASmallCornerOfTheWindow(t *testing.T) {
	space := image.Pt(640, 480)
	got := thumbSize(space, frame(320, 240))
	if got.X != space.X/4 {
		t.Errorf("the thumbnail is %d wide in a window of %d", got.X, space.X)
	}
	if want := got.X * 240 / 320; got.Y != want {
		t.Errorf("the thumbnail is %v, which is not the picture's shape", got)
	}

	// A tall picture in a shallow window gives way on height, not on shape.
	space = image.Pt(400, 40)
	got = thumbSize(space, frame(320, 960))
	if got.X > space.X || got.Y > space.Y {
		t.Errorf("the thumbnail is %v in a window of %v", got, space)
	}
	if got.X < 1 || got.Y < 1 {
		t.Errorf("the thumbnail is %v, which is nothing at all", got)
	}
}

// frame is one video frame of a given size, its content beside the point.
func frame(w, h int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, w, h))
}

// newContext is a frame to lay out into: a size, and no input router, which
// is enough to draw but not to click.
func newContext(size image.Point) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(size),
	}
}
