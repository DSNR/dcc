package gui

import (
	"context"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
)

// Session is the slice of *session.Session the desktop client drives.
// *session.Session satisfies it as it stands; the interface exists so that
// this package can be tested for what it shows and what it asks for, with a
// fake in place of a real Session.
//
// It is the text half of a Session — chat, which is what this window does.
// A Call's controls arrive with the work that adds them.
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
	// Close ends the Session and releases everything it holds.
	Close() error
}

// NewSession mints a fresh Session. A Session runs once — Invite to
// disconnect — so hosting or joining again needs a new one, which is why the
// Model holds the means to make one rather than a Session it was handed.
type NewSession func() (Session, error)

// Store is the slice of *storage.Store the desktop client reads history
// through. Like Session, it is an interface so that the tests can put a fake
// underneath what is shown.
type Store interface {
	// Conversations lists every stored Conversation.
	Conversations() ([]storage.Conversation, error)
	// Messages reads one Conversation back, oldest first.
	Messages(peer identity.PublicKey) ([]storage.Message, error)
	// Clear deletes one Conversation — this device's copy only.
	Clear(peer identity.PublicKey) error
}
