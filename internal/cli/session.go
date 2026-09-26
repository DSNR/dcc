package cli

import (
	"context"
	"image"

	"github.com/DSNR/dcc/internal/session"
)

// Session is the slice of *session.Session that the terminal interface
// drives. *session.Session satisfies it as it stands; the interface exists so
// that this package can be tested for what it shows and what it asks for,
// with a fake in place of a real Session.
type Session interface {
	// Events is the Session's one output, closing when the Session is over
	// for good.
	Events() <-chan session.Event
	// Host opens a Rendezvous and waits for a Peer. It blocks while
	// cloudflared comes alive, so it never runs on the UI's goroutine.
	Host(ctx context.Context) error
	// Join connects to an Invite, refusing a string that is not one.
	Join(ctx context.Context, invite string) error
	// Accept and Refuse resolve the standing Security Code prompt.
	Accept() error
	Refuse() error
	// SendText sends one message and returns the id its statuses arrive
	// against.
	SendText(body string) (string, error)
	// Call rings the other person and returns the new Call's id.
	Call() (string, error)
	// Answer picks up the Call ringing here.
	Answer() error
	// Reject turns down the Call ringing here.
	Reject() error
	// Hangup ends the Call, leaving the Session up for text.
	Hangup() error
	// Mute stops or resumes sending this side's microphone.
	Mute(muted bool) error
	// Muted reports whether this side's microphone is being sent.
	Muted() bool
	// Camera turns this side's camera on or off. It opens or releases the
	// device, so it blocks and never runs on the UI's goroutine.
	Camera(on bool) error
	// CameraOn reports whether this side's camera is being sent.
	CameraOn() bool
	// Frames is the other side's decoded video, newest frame only — what the
	// video window paints.
	Frames() <-chan *image.RGBA
	// Close ends the Session and releases everything it holds.
	Close() error
}

// NewSession mints a fresh Session. A Session runs once — Invite to
// disconnect — so hosting or joining again needs a new one, which is why the
// Model holds the means to make one rather than a Session it was handed.
type NewSession func() (Session, error)
