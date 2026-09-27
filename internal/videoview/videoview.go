// Package videoview is a Call's picture, laid out the one way dcc lays it out:
// the other side filling the area — their shared screen while they are sharing
// one, their camera otherwise — with this side's own camera as a thumbnail in
// the corner over them, so that what the other person is being shown is always
// in front of the person sending it.
//
// Both of dcc's windows draw it from here: dcc-cli's separate video window in
// internal/window, and dcc-gui's call view in internal/chatwindow. ADR 0003
// asks for one layout across the two, and one layout is one piece of code.
package videoview

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// bg is what sits behind a picture, so that a frame which does not fill the
// area is letterboxed rather than showing whatever was there.
var bg = color.NRGBA{A: 0xFF}

// inset is how far the thumbnail sits from the corner it is tucked into.
const inset = unit.Dp(8)

// Layout draws large, with small as a thumbnail over it. Either may be
// missing: empty is what fills the area when there is no large picture — a
// line about what the Call is waiting for, or nothing at all where a window
// says it with a black rectangle — and a nil small is a camera nobody has
// turned on, which shows no thumbnail.
func Layout(gtx layout.Context, large, small *image.RGBA, empty layout.Widget) layout.Dimensions {
	return layout.Stack{Alignment: layout.SE}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			paint.Fill(gtx.Ops, bg)
			switch {
			case large != nil:
				return picture(gtx, large)
			case empty != nil:
				return empty(gtx)
			}
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if small == nil {
				return layout.Dimensions{}
			}
			return layout.UniformInset(inset).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Exact(thumbSize(gtx.Constraints.Max, small))
				return picture(gtx, small)
			})
		}),
	)
}

// picture paints one frame, one ImageOp per frame, letterboxed inside whatever
// space it was given: a picture is as big as it can be without being stretched
// into the wrong shape.
func picture(gtx layout.Context, img *image.RGBA) layout.Dimensions {
	return widget.Image{
		Src:      paint.NewImageOp(img),
		Fit:      widget.Contain,
		Position: layout.Center,
		Scale:    1 / gtx.Metric.PxPerDp,
	}.Layout(gtx)
}

// thumbSize is how big the thumbnail is: a quarter of the area across, in the
// picture's own shape, and never bigger than the space it sits in — a
// thumbnail that grew to fill a small window would be hiding the person it is
// a thumbnail beside.
func thumbSize(space image.Point, img *image.RGBA) image.Point {
	bounds := img.Bounds()
	width := min(max(space.X/4, 1), space.X)
	height := width * bounds.Dy() / max(bounds.Dx(), 1)
	if height > space.Y {
		height = space.Y
		width = min(height*bounds.Dx()/max(bounds.Dy(), 1), space.X)
	}
	return image.Pt(width, max(height, 1))
}
