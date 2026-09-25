package session

import (
	"time"

	"github.com/DSNR/dcc/internal/identity"
)

// History persists the Conversation as it happens, keyed by the Peer's
// Identity. The Session writes through it at the two moments the spec fixes:
// an outgoing message is kept before the transport gets it, and an incoming
// one before its ack goes back — so nothing either side believes was said
// can be lost to a crash. The Peer's Display Name rides along so a
// Conversation created here carries a label, not just a key. The storage
// package implements it on the messages table; a nil Options.History keeps
// nothing, which is what tests want.
type History interface {
	// Outgoing keeps a message this side is about to send. An error stops
	// the send: a message that cannot be kept is not said.
	Outgoing(peer identity.PublicKey, name, id, body string, at time.Time) error
	// Incoming keeps a message the Peer sent, before it is acknowledged or
	// shown. fresh is false when id has been kept before — a resend after a
	// reconnect — which is acknowledged again but not shown again.
	Incoming(peer identity.PublicKey, name, id, body string, at time.Time) (fresh bool, err error)
	// Status records where a kept message's delivery now stands.
	Status(id string, status DeliveryStatus) error
}
