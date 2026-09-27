package media_test

import (
	"os"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
)

// TestScreenSize checks what a display is scaled to on its way to the encoder:
// small ones untouched, large ones brought inside the cap with their shape
// intact, and every result even, because I420's chroma planes are half-size in
// both directions.
func TestScreenSize(t *testing.T) {
	for _, c := range []struct {
		name                  string
		width, height         int
		wantWidth, wantHeight int
	}{
		{"a laptop is left alone", 1366, 768, 1366, 768},
		{"exactly the cap is left alone", 1920, 1080, 1920, 1080},
		{"4K comes down to the cap", 3840, 2160, 1920, 1080},
		{"a tall display comes down by its height", 1920, 1200, 1728, 1080},
		{"an ultrawide comes down by its width", 3440, 1440, 1920, 802},
		{"an odd size is rounded even", 1367, 769, 1366, 768},
	} {
		t.Run(c.name, func(t *testing.T) {
			width, height := media.ScreenSize(c.width, c.height)
			if width != c.wantWidth || height != c.wantHeight {
				t.Errorf("a %dx%d display encodes at %dx%d, want %dx%d",
					c.width, c.height, width, height, c.wantWidth, c.wantHeight)
			}
			if width%2 != 0 || height%2 != 0 {
				t.Errorf("%dx%d is not an even size", width, height)
			}
			if width > media.ScreenMaxWidth || height > media.ScreenMaxHeight {
				t.Errorf("%dx%d is outside the %dx%d cap",
					width, height, media.ScreenMaxWidth, media.ScreenMaxHeight)
			}
		})
	}
}

// smokeEnv opts in to the one test here that needs a real display. Capturing a
// screen is not something CI has, and it should not silently fail there either.
// Set it to check the capture path on this machine:
//
//	DCC_SCREEN=1 go test ./internal/media/ -run TestRealScreenCapture
const smokeEnv = "DCC_SCREEN"

// TestRealScreenCapture takes a few frames off the actual display — the manual
// check that this machine's capture driver, the scaler and the frame pacing
// work together. It asserts only what is true of any desktop: frames arrive, at
// a size inside the cap, at roughly the rate they were asked for.
func TestRealScreenCapture(t *testing.T) {
	if os.Getenv(smokeEnv) == "" {
		t.Skipf("set %s=1 to capture this machine's screen", smokeEnv)
	}
	var got seen
	video, err := media.StartVideo(screenOnly(nil, &got))
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer video.Close()

	if err := video.Screen(true); err != nil {
		t.Fatalf("Screen(true): %v", err)
	}
	started := time.Now()
	waitStream(t, &got, 5, media.ScreenFrameDuration)
	if took := time.Since(started); took < 3*media.ScreenFrameDuration {
		t.Errorf("five frames took %v, which is faster than the frame rate asked for", took)
	}
	if err := video.Screen(false); err != nil {
		t.Fatalf("Screen(false): %v", err)
	}
}
