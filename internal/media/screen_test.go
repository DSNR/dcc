package media_test

import (
	"os"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
)

// TestScreenSize checks what a display is scaled to on its way to the encoder:
// large ones brought inside the cap with their shape intact, and every result
// a whole number of macroblocks, which is the only frame size the encoder can
// be trusted with (ADR 0006).
func TestScreenSize(t *testing.T) {
	for _, c := range []struct {
		name                  string
		width, height         int
		wantWidth, wantHeight int
	}{
		{"a laptop keeps its width", 1366, 768, 1360, 768},
		{"the cap comes down to whole macroblocks", 1920, 1080, 1920, 1072},
		{"4K comes down to the cap", 3840, 2160, 1920, 1072},
		{"a tall display comes down by its height", 1920, 1200, 1728, 1072},
		{"an ultrawide comes down by its width", 3440, 1440, 1920, 800},
		{"an odd size is rounded down", 1367, 769, 1360, 768},
		{"a display with nothing to encode comes back as nothing", 10, 10, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			width, height := media.ScreenSize(c.width, c.height)
			if width != c.wantWidth || height != c.wantHeight {
				t.Errorf("a %dx%d display encodes at %dx%d, want %dx%d",
					c.width, c.height, width, height, c.wantWidth, c.wantHeight)
			}
			if width%16 != 0 || height%16 != 0 {
				t.Errorf("%dx%d is not a whole number of macroblocks", width, height)
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

// desktop paints one frame of something shaped like a shared screen: a flat
// background with sharp-edged text that never moves, and one window that does,
// kept in the top half so the bottom of the frame stands perfectly still the
// way the bottom of a desktop does. Still macroblocks are the point — the
// encoder heuristic that used to crash dcc only looks at a macroblock that has
// coded still for thirty frames.
func desktop(pic media.Picture, frame int) {
	for row := range pic.Height {
		line := pic.Y[row*pic.YStride : row*pic.YStride+pic.Width]
		for col := range line {
			line[col] = 235
		}
		if row%24 >= 6 && row%24 < 18 {
			for col := 8; col+7 < pic.Width-8; col += 11 {
				for k := range 7 {
					line[col+k] = 25
				}
			}
		}
	}
	for i := range pic.U {
		pic.U[i], pic.V[i] = 128, 128
	}
	width, height := pic.Width/4, pic.Height/4
	left, top := pic.Width/16+(frame*3)%(pic.Width/2), pic.Height/8
	for row := top; row < top+height; row++ {
		line := pic.Y[row*pic.YStride:]
		for col := left; col < left+width; col++ {
			line[col] = byte(60 + (row+col+frame*3)%120)
		}
	}
}

// interFrameRun is how many frames a run has to be to get anywhere near the
// crash this is about: the encoder only tests a macroblock for the artifact it
// tripped over once that macroblock has coded still for thirty frames, so a
// run of a dozen proves nothing. At ten frames a second this is the four and a
// half seconds of sharing that used to kill dcc.
const interFrameRun = 45

// TestScreenEncodesALongRunOfInterFrames is the regression test for the crash
// ADR 0006 is about: sharing a screen took the whole process down a few seconds
// in, inside govpx, reading sixteen rows of a macroblock row that only had
// eight. Both sizes here are deliberately not whole numbers of macroblocks —
// which is what a display, or a camera, is free to hand over — so what is being
// proved is that the encoder crops what it is given rather than trusting it.
func TestScreenEncodesALongRunOfInterFrames(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
		slow          bool
	}{
		{"a frame size the encoder has to crop", 328, 200, false},
		{"the 1920x1080 display that crashed", media.ScreenMaxWidth, media.ScreenMaxHeight, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.slow && testing.Short() {
				t.Skip("encoding a run of 1080p frames is too slow for -short")
			}
			frames, err := media.EncodeFrames(c.width, c.height,
				media.ScreenFPS, media.ScreenBitrateKbps, interFrameRun, desktop)
			if err != nil {
				t.Fatalf("encoding %d frames of a %dx%d screen: %v",
					len(frames), c.width, c.height, err)
			}
			if len(frames) != interFrameRun {
				t.Fatalf("encoded %d frames, want %d", len(frames), interFrameRun)
			}
			if len(frames[0]) == 0 {
				t.Fatal("the stream's first frame is empty")
			}
			// Rate control is allowed to drop a frame; dropping most of a
			// still desktop's frames would mean nothing was being encoded at
			// all, and the run proved nothing.
			sent := 0
			for _, frame := range frames {
				if len(frame) > 0 {
					sent++
				}
			}
			if sent*3 < interFrameRun*2 {
				t.Fatalf("only %d of %d frames carried anything", sent, interFrameRun)
			}

			// And what came out is a stream: it decodes, at the size the
			// encoder cropped to rather than the size it was handed.
			var got seen
			video, err := media.StartVideo(screenOnly(&media.Fake{}, &got))
			if err != nil {
				t.Fatalf("StartVideo: %v", err)
			}
			defer video.Close()
			for _, frame := range frames {
				video.PlayScreen(frame)
			}
			shown := got.pictures()
			if len(shown) != sent {
				t.Fatalf("decoded %d pictures from %d frames", len(shown), sent)
			}
			wantWidth, wantHeight := c.width&^15, c.height&^15
			if w, h := shown[0].Bounds().Dx(), shown[0].Bounds().Dy(); w != wantWidth || h != wantHeight {
				t.Errorf("a %dx%d frame arrived as %dx%d, want it cropped to %dx%d",
					c.width, c.height, w, h, wantWidth, wantHeight)
			}
		})
	}
}
