// Package window is dcc's video window: the Call's video, in a desktop window
// of its own, opened in-process by whichever UI wants one.
// dcc-cli opens one when a Call goes Active and closes it when the Call ends,
// which is what keeps the terminal a terminal.
//
// Gio needs the process's main goroutine for its own event loop, so a program
// that opens a window here must call app.Main from main — see cmd/dcc-cli.
package window

import (
	"image"
	"sync"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/DSNR/dcc/internal/videoview"
)

// The window's starting size, in device-independent pixels — the frame size
// dcc sends, so a picture arrives unscaled on a one-to-one display.
const (
	startWidth  = 640
	startHeight = 480
)

// Options configures one video window.
type Options struct {
	// Title is the window's title, which is where the participant reads whose
	// video they are looking at.
	Title string
	// Frames is the video to show, newest frame only: the channel a Session
	// hands out. Nil shows a black window.
	Frames <-chan *image.RGBA
	// Screen is the other side's shared screen, on the same terms. It takes
	// the window over while Sharing is on, because what somebody is pointing
	// at is what the other person needs to see; their face is the thing that
	// can wait.
	Screen <-chan *image.RGBA
	// Local is this side's own camera, on the same terms again — the
	// thumbnail in the corner, painted while Camera is on. Nil, or a camera
	// nobody turned on, shows no thumbnail.
	Local <-chan *image.RGBA
	// Failed reports a window that could not be opened or that died — no
	// display, no GPU, a compositor that went away. Nil says nothing.
	Failed func(error)
}

// Window is one open video window. It paints whatever arrives on Frames — or
// on Screen, while a share is running — with this side's own camera as a
// thumbnail over it, and stops when it is closed, from here or by the person
// clicking the close box.
//
// The layout is the one internal/chatwindow's call view draws, as ADR 0003
// asks: the other side large, this side in the corner, so that what the other
// person is being shown is always in front of the person sending it.
type Window struct {
	win *app.Window
	// stop is closed by Close; done closes when the window's event loop has
	// finished, whichever side ended it.
	stop chan struct{}
	done chan struct{}
	once sync.Once

	mu sync.Mutex
	// camera and screen are the newest frame of the other side's two streams;
	// sharing picks which of them the window is for. local is the newest frame
	// of this side's own camera, painted over whichever it is while cameraOn.
	camera   *image.RGBA
	screen   *image.RGBA
	local    *image.RGBA
	sharing  bool
	cameraOn bool
}

// Open puts a window on the screen and starts painting. It returns
// immediately: the window comes up on its own goroutines, and a window that
// cannot be created is reported through Options.Failed rather than here,
// because that is how the toolkit reports it.
func Open(opts Options) *Window {
	w := &Window{
		win:  new(app.Window),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	w.win.Option(
		app.Title(opts.Title),
		app.Size(unit.Dp(startWidth), unit.Dp(startHeight)),
	)
	go w.feed(opts.Frames, w.keepCameraLocked)
	go w.feed(opts.Screen, w.keepScreenLocked)
	go w.feed(opts.Local, w.keepLocalLocked)
	go w.paint(opts.Failed)
	return w
}

// Sharing says whether the other side is sharing their screen, which is what
// decides whether this window is showing their screen or their camera. It
// returns at once and may be called before either stream has delivered
// anything.
func (w *Window) Sharing(on bool) {
	w.mu.Lock()
	changed := w.sharing != on
	w.sharing = on
	w.mu.Unlock()
	if changed {
		w.win.Invalidate()
	}
}

// Camera says whether this side's camera is on, which is what decides whether
// the thumbnail is in the corner: a camera that has been turned off takes its
// last picture with it rather than leaving a frozen face on the screen. It
// returns at once and may be called before any local frame has arrived.
func (w *Window) Camera(on bool) {
	w.mu.Lock()
	changed := w.cameraOn != on
	w.cameraOn = on
	if !on {
		// The picture goes with the camera, so that turning it on again shows
		// what the camera sees now rather than what it saw when it went off.
		w.local = nil
	}
	w.mu.Unlock()
	if changed {
		w.win.Invalidate()
	}
}

// Close takes the window down. It returns at once — the window closes on its
// own goroutine — and closing twice, or closing one the participant has
// already closed, is fine.
func (w *Window) Close() {
	w.once.Do(func() { close(w.stop) })
}

// Closed closes when the window is gone, whether it was closed from here or
// by the participant.
func (w *Window) Closed() <-chan struct{} { return w.done }

// keepCameraLocked, keepScreenLocked and keepLocalLocked each hold on to one
// stream's newest frame. They are what feed hands a frame to, so they are
// called with the window's lock held.
func (w *Window) keepCameraLocked(img *image.RGBA) { w.camera = img }

func (w *Window) keepScreenLocked(img *image.RGBA) { w.screen = img }

func (w *Window) keepLocalLocked(img *image.RGBA) {
	// A picture captured before the camera was turned off can still be in
	// flight; keeping it would put a stale face back in the corner the next
	// time the camera comes on.
	if !w.cameraOn {
		return
	}
	w.local = img
}

// feed hands the newest frame of one stream to whichever keeper it belongs to
// and asks for a repaint. Only the newest is kept: a window that fell behind
// should show the current picture, not catch up through stale ones. One of
// these runs per stream, so none ever waits behind another.
func (w *Window) feed(frames <-chan *image.RGBA, keep func(*image.RGBA)) {
	for {
		select {
		case img := <-frames:
			w.mu.Lock()
			keep(img)
			w.mu.Unlock()
			w.win.Invalidate()
		case <-w.stop:
			// Asking the window to close is what ends the paint loop, which
			// is what closes done. The invalidate behind it is what makes an
			// idle window notice: on some backends the request is only read
			// on the way through the next frame.
			w.win.Perform(system.ActionClose)
			w.win.Invalidate()
			return
		case <-w.done:
			return
		}
	}
}

// paint is the window's event loop: one frame drawn for every FrameEvent, and
// done when the window is destroyed.
func (w *Window) paint(failed func(error)) {
	defer close(w.done)
	var ops op.Ops
	for {
		switch e := w.win.Event().(type) {
		case app.DestroyEvent:
			if e.Err != nil && failed != nil {
				failed(e.Err)
			}
			return
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			large, small := w.pictures()
			// Nothing is said where a picture is missing: a window with no
			// picture in it is black, which is the truth about a Call whose
			// cameras are all off.
			videoview.Layout(gtx, large, small, nil)
			e.Frame(gtx.Ops)
		}
	}
}

// pictures is what to draw this frame: the other side large — their shared
// screen while they are sharing one, their camera otherwise — and this side's
// own camera as the thumbnail, while it is on.
func (w *Window) pictures() (large, small *image.RGBA) {
	w.mu.Lock()
	defer w.mu.Unlock()
	large = w.camera
	if w.sharing && w.screen != nil {
		large = w.screen
	}
	if w.cameraOn {
		small = w.local
	}
	return large, small
}
