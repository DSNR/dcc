package media

import (
	"errors"
	"time"
)

// The shape of a shared screen. A screen is nothing like a camera: it is much
// larger, it barely changes from one frame to the next, and what is on it is
// usually text, which survives a low frame rate far better than it survives
// being blurred. So it is captured slowly, scaled to something a pure-Go
// encoder can keep up with, and given more bits than a face would need.
const (
	// ScreenFPS is the frame rate a shared screen is captured at.
	ScreenFPS = 10
	// ScreenFrameDuration is how much time one screen frame stands for.
	ScreenFrameDuration = time.Second / ScreenFPS
	// ScreenMaxWidth and ScreenMaxHeight bound what is encoded. A display
	// larger than this is scaled down to fit, keeping its aspect ratio: at
	// 4K the encoder would not make its frame deadline, and a viewer cannot
	// see 4K in a 640-pixel window anyway.
	ScreenMaxWidth  = 1920
	ScreenMaxHeight = 1080
	// ScreenBitrateKbps is the encoder's CBR target for a shared screen.
	// Higher than the camera's, because unreadable text is a useless share,
	// and affordable because ten near-identical frames a second compress far
	// better than fifteen changing ones.
	ScreenBitrateKbps = 1500
)

// Screen is a screen-capture source. It is a Camera in every respect a
// pipeline cares about — successive Pictures, paced in real time, each valid
// only until the next Read — and named apart because what it opens is a
// different thing entirely, and what a participant is told about it differs
// too.
type Screen = Camera

// ErrNoScreen reports that there is no screen here to share: a build with no
// capture driver for its platform, or a machine with no display attached. A
// Call carries on without it, and the other side is told this side is not
// sharing.
var ErrNoScreen = errors.New("media: no screen to share on this platform")

// screenSize is the size a display of the given size is encoded at: scaled
// down to fit inside ScreenMaxWidth by ScreenMaxHeight if it is larger,
// untouched if it is not, and rounded down to whole macroblocks either way.
// Whole macroblocks because that is all the encoder will take
// (videoMacroblock), and rounded here rather than cropped there because the
// capture scales the whole display into whatever size this returns: a
// participant sharing a 1080-row display gets all of it, imperceptibly
// squashed, instead of the bottom eight rows cut off.
func screenSize(width, height int) (int, int) {
	if width > ScreenMaxWidth {
		width, height = ScreenMaxWidth, height*ScreenMaxWidth/width
	}
	if height > ScreenMaxHeight {
		width, height = width*ScreenMaxHeight/height, ScreenMaxHeight
	}
	return width & ^(videoMacroblock - 1), height & ^(videoMacroblock - 1)
}
