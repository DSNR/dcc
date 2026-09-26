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

// TestWindowShowsFrames opens a window, paints a second of video into it and
// closes it again — the manual check that the toolkit, the GPU and this
// package's own close path all still work together on this machine.
func TestWindowShowsFrames(t *testing.T) {
	if os.Getenv(smokeEnv) == "" {
		t.Skipf("set %s=1 to open a real window", smokeEnv)
	}
	frames := make(chan *image.RGBA, 1)
	failed := make(chan error, 1)
	w := window.Open(window.Options{
		Title:  "dcc — video window test",
		Frames: frames,
		Failed: func(err error) { failed <- err },
	})

	tint := color.RGBA{R: 200, G: 40, B: 40, A: 0xFF}
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = tint.R, tint.G, tint.B, tint.A
	}
	for range 20 {
		select {
		case frames <- img:
		case err := <-failed:
			t.Fatalf("the window failed: %v", err)
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
