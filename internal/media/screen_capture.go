//go:build linux || windows

package media

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/kbinani/screenshot"
)

// Screen capture is kbinani/screenshot — XGB on Linux, GDI on Windows, pure Go
// on both, so sharing a screen costs the build nothing (ADR 0002). It is the
// one capture path dcc has on Windows that a camera does not: a Windows
// participant who cannot be seen can still show what they are looking at.
//
// On Linux it reads the X server. Under a Wayland compositor there is nothing
// for it to read but what XWayland knows about, which is not the desktop —
// sharing a screen there wants a portal, and that is not MVP.

// primaryDisplay is the display dcc shares. A machine with several gets its
// primary one shared whole; choosing between them is a picker, and dcc has no
// pickers in MVP.
const primaryDisplay = 0

// openScreen starts capturing the primary display, or says why it cannot.
func openScreen() (Screen, error) {
	if screenshot.NumActiveDisplays() < 1 {
		return nil, fmt.Errorf("media: no display is attached: %w", ErrNoScreen)
	}
	bounds := screenshot.GetDisplayBounds(primaryDisplay)
	width, height := screenSize(bounds.Dx(), bounds.Dy())
	// A display too small to make one macroblock of comes back as nothing at
	// all from screenSize, and there is no encoding that.
	if width < videoMacroblock || height < videoMacroblock {
		return nil, fmt.Errorf("media: the display is %v: %w", bounds, ErrNoScreen)
	}
	// One capture before the boundary is crossed, so that a display which
	// cannot be read at all is reported by the thing that opened it rather
	// than by a stream that dies a tenth of a second later.
	if _, err := screenshot.CaptureRect(bounds); err != nil {
		return nil, fmt.Errorf("media: capturing the screen: %w", err)
	}
	return &screenGrab{
		bounds: bounds,
		pic:    NewPicture(width, height),
		next:   time.Now(),
		done:   make(chan struct{}),
	}, nil
}

// screenGrab is the primary display behind the Screen boundary: one capture
// every ScreenFrameDuration, scaled into the I420 the encoder wants.
type screenGrab struct {
	bounds image.Rectangle
	// pic is the frame every capture is scaled into, reused across frames —
	// which is why the boundary says a Picture is only good until the next
	// Read.
	pic  Picture
	next time.Time
	done chan struct{}
	once sync.Once
}

// Read implements Screen, waiting until the next frame is due and then taking
// it. Capture and encode together can overrun the frame interval on a large
// display; when they do the clock restarts from now rather than accumulating
// debt it would then try to make up by capturing flat out.
func (s *screenGrab) Read() (Picture, error) {
	s.next = s.next.Add(ScreenFrameDuration)
	if wait := time.Until(s.next); wait > 0 {
		select {
		case <-time.After(wait):
		case <-s.done:
			return Picture{}, errors.New("media: the screen share is closed")
		}
	} else {
		s.next = time.Now()
		select {
		case <-s.done:
			return Picture{}, errors.New("media: the screen share is closed")
		default:
		}
	}
	img, err := screenshot.CaptureRect(s.bounds)
	if err != nil {
		return Picture{}, fmt.Errorf("media: capturing the screen: %w", err)
	}
	if err := RGBAToI420(img, s.pic); err != nil {
		return Picture{}, err
	}
	return s.pic, nil
}

// Close implements Screen, stopping the capture and unblocking a Read that is
// waiting for the next frame. Closing twice is fine.
func (s *screenGrab) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
