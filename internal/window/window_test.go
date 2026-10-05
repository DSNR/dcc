package window_test

import (
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/window"
)

// smokeEnv opts in to the one test here. A video window is a real window on a
// real display: it cannot be asserted about in CI, and it should not appear
// unbidden on the screen of someone running the suite. Set it to watch the
// window work:
//
//	DCC_WINDOW=1 go test -tags novulkan ./internal/window/
const smokeEnv = "DCC_WINDOW"

// TestWindowShowsFrames opens a window, paints a second of video into it —
// the other side large, this side as a thumbnail for the middle of it — and
// closes it again: the manual check that the toolkit, the GPU and this
// package's own close path all still work together on this machine.
func TestWindowShowsFrames(t *testing.T) {
	if os.Getenv(smokeEnv) == "" {
		t.Skipf("set %s=1 to open a real window", smokeEnv)
	}
	frames := make(chan *image.RGBA, 1)
	local := make(chan *image.RGBA, 1)
	failed := make(chan error, 1)
	w := window.Open(window.Options{
		Title:  "dcc — video window test",
		Frames: frames,
		Local:  local,
		Failed: func(err error) { failed <- err },
	})

	theirs := filled(640, 480, color.RGBA{R: 200, G: 40, B: 40, A: 0xFF})
	mine := filled(320, 240, color.RGBA{R: 40, G: 90, B: 200, A: 0xFF})
	for i := range 20 {
		// The thumbnail is up for the middle of the run only, so that both a
		// window with one and a window without one are seen.
		switch i {
		case 5:
			w.Camera(true)
		case 15:
			w.Camera(false)
		}
		select {
		case frames <- theirs:
		case err := <-failed:
			t.Fatalf("the window failed: %v", err)
		default:
		}
		select {
		case local <- mine:
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}

	w.Close()
	select {
	case <-w.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the window did not close when it was asked to")
	}
}

// filled is one frame of a flat colour, which is enough to see a picture
// arrive, letterbox and go away again.
func filled(w, h int, tint color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = tint.R, tint.G, tint.B, tint.A
	}
	return img
}
