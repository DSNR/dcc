package cli

import "image"

// The video window, at arm's length. The terminal interface decides when a
// window should be up — a Call is Active — and nothing more: what a window
// actually is comes in through Options, so that the Gio dependency lives in
// cmd/dcc-cli and a test can watch the window open and close without one
// appearing on anybody's screen.

// OpenVideo opens a video window. cmd/dcc-cli passes internal/window's; a nil
// one means this terminal shows no video at all.
type OpenVideo func(VideoOptions) VideoWindow

// VideoOptions is what a window needs to know.
type VideoOptions struct {
	// Title is the window's title — whose video this is.
	Title string
	// Frames is the video to paint, newest frame only.
	Frames <-chan *image.RGBA
	// Failed reports a window that could not be opened or that died.
	Failed func(error)
}

// VideoWindow is one open video window, as the terminal interface sees it: it
// can be closed, and it says when it has been.
type VideoWindow interface {
	// Close takes the window down. It returns at once, and closing twice — or
	// closing one the participant has already closed — is fine.
	Close()
	// Closed closes when the window is gone, however it went.
	Closed() <-chan struct{}
}
