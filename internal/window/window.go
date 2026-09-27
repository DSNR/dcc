// Package window is dcc's video window: the other side's Call video, in a
// desktop window of its own, opened in-process by whichever UI wants one.
// dcc-cli opens one when a Call goes Active and closes it when the Call ends,
// which is what keeps the terminal a terminal.
//
// Gio needs the process's main goroutine for its own event loop, so a program
// that opens a window here must call app.Main from main — see cmd/dcc-cli.
package window

import (
	"image"
	"image/color"
	"sync"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
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
	// Failed reports a window that could not be opened or that died — no
	// display, no GPU, a compositor that went away. Nil says nothing.
	Failed func(error)
}

// Window is one open video window. It paints whatever arrives on Frames — or
// on Screen, while a share is running — and stops when it is closed, from here
// or by the person clicking the close box.
//
// It shows the other side's video, full window. The local picture-in-picture
// ADR 0003 describes is the GUI's, and arrives with it.
type Window struct {
	win *app.Window
	// stop is closed by Close; done closes when the window's event loop has
	// finished, whichever side ended it.
	stop chan struct{}
	done chan struct{}
	once sync.Once

	mu sync.Mutex
	// camera and screen are the newest frame of each stream; sharing picks
	// which of them the window is for.
	camera  *image.RGBA
	screen  *image.RGBA
	sharing bool
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
	go w.feed(opts.Frames, false)
	go w.feed(opts.Screen, true)
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

// Close takes the window down. It returns at once — the window closes on its
// own goroutine — and closing twice, or closing one the participant has
// already closed, is fine.
func (w *Window) Close() {
	w.once.Do(func() { close(w.stop) })
}

// Closed closes when the window is gone, whether it was closed from here or
// by the participant.
func (w *Window) Closed() <-chan struct{} { return w.done }

// feed keeps the newest frame of one stream and asks for a repaint. Only the
// newest is kept: a window that fell behind should show the current picture,
// not catch up through stale ones. One of these runs per stream, so neither
// ever waits behind the other.
func (w *Window) feed(frames <-chan *image.RGBA, screen bool) {
	for {
		select {
		case img := <-frames:
			w.mu.Lock()
			if screen {
				w.screen = img
			} else {
				w.camera = img
			}
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
			// Black behind the picture, so a frame that does not fill the
			// window is letterboxed rather than showing whatever was there.
			paint.Fill(gtx.Ops, color.NRGBA{A: 0xFF})
			w.mu.Lock()
			img := w.camera
			if w.sharing && w.screen != nil {
				img = w.screen
			}
			w.mu.Unlock()
			if img != nil {
				widget.Image{
					Src:      paint.NewImageOp(img),
					Fit:      widget.Contain,
					Position: layout.Center,
					Scale:    1 / gtx.Metric.PxPerDp,
				}.Layout(gtx)
			}
			e.Frame(gtx.Ops)
		}
	}
}
