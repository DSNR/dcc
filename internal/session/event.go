package session

import (
	"fmt"
	"sync"
	"time"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/transport"
)

// Event is what a Session tells its UI. The concrete types are the structs
// below; a UI switches on them. Events arrive in the order they happened,
// and the channel closing is the Session saying it is over for good.
type Event interface {
	event()
}

// StateChanged announces every transition. Reason is meaningful only when
// State is Disconnected or Failed.
type StateChanged struct {
	State  State
	Reason Reason
}

func (StateChanged) event() {}

// InviteReady carries the Invite once the Rendezvous is reachable. It
// follows the transition to Hosting.
type InviteReady struct {
	Invite rendezvous.Invite
}

func (InviteReady) event() {}

// VerifyPrompt is the blocking Security Code prompt, emitted on entering
// Verifying. The Session holds there until Accept or Refuse resolves it; a
// later StateChanged that leaves Verifying withdraws the prompt.
type VerifyPrompt struct {
	// Code is the Security Code to compare out-of-band.
	Code identity.SecurityCode
	// Name is the Display Name the other side announced — a label, not
	// proof.
	Name string
	// Peer is the Identity the handshake actually authenticated.
	Peer identity.PublicKey
	// Changed reports that Name was previously accepted with a different
	// Identity, which turns this prompt into the "Security Code changed"
	// warning.
	Changed bool
}

func (VerifyPrompt) event() {}

// LinkChanged carries Connected's sub-status: whether content is flowing
// peer to peer or through a relay. It follows the transition to Connected,
// and would arrive again if the route changed under a live Session.
type LinkChanged struct {
	Link transport.Link
}

func (LinkChanged) event() {}

// TextReceived is one chat message from the Peer. At is when it arrived
// here; the send time travels inside the UUIDv7 ID.
type TextReceived struct {
	ID   string
	Body string
	At   time.Time
}

func (TextReceived) event() {}

// TextStatus moves one sent message through its life: TextPending when
// SendText queues it, TextSent once the DataChannel has it, TextDelivered
// when the Peer's ack arrives — or TextFailed, from anywhere, if the Session
// ends first.
type TextStatus struct {
	ID     string
	Status DeliveryStatus
}

func (TextStatus) event() {}

// DeliveryStatus is where one sent message stands.
type DeliveryStatus int

const (
	// TextPending: accepted by SendText, not yet handed to the transport.
	TextPending DeliveryStatus = iota + 1
	// TextSent: on its way, ordered and reliable, but not yet acknowledged.
	TextSent
	// TextDelivered: the Peer's application acknowledged it.
	TextDelivered
	// TextFailed: the Session ended, or the transport refused it, before
	// the acknowledgement came. Terminal.
	TextFailed
)

// String implements fmt.Stringer.
func (d DeliveryStatus) String() string {
	switch d {
	case TextPending:
		return "pending"
	case TextSent:
		return "sent"
	case TextDelivered:
		return "delivered"
	case TextFailed:
		return "failed"
	}
	return fmt.Sprintf("DeliveryStatus(%d)", int(d))
}

// eventQueue delivers events to the UI without ever making the Session wait
// on it: emit appends and returns, and a pump goroutine feeds the channel as
// fast as the UI drains it.
type eventQueue struct {
	out chan Event

	mu     sync.Mutex
	cond   *sync.Cond
	items  []Event
	closed bool
}

func newEventQueue() *eventQueue {
	q := &eventQueue{out: make(chan Event)}
	q.cond = sync.NewCond(&q.mu)
	go q.pump()
	return q
}

// emit queues one event. After close it quietly does nothing, so a straggler
// from a dying goroutine is harmless.
func (q *eventQueue) emit(e Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, e)
	q.cond.Signal()
}

// close ends the stream once everything already queued has been delivered.
func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Signal()
	q.mu.Unlock()
}

// pump moves events from the queue to the channel, closing it when the queue
// is closed and drained.
func (q *eventQueue) pump() {
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 {
			q.mu.Unlock()
			close(q.out)
			return
		}
		e := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		q.out <- e
	}
}
