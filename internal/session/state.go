package session

import "fmt"

// State is where a Session is in its life. Every transition is announced as
// a StateChanged event; UIs render the current state and nothing else.
type State int

const (
	// Idle: no Session yet. Host or Join leaves it.
	Idle State = iota
	// Hosting: the Rendezvous is up and the Invite is out. Only a Host is
	// ever here, and never twice — one Session per Invite.
	Hosting
	// Connecting: the Peer is dialing the Rendezvous and running the
	// handshake.
	Connecting
	// Verifying: the handshake authenticated the other side, and the
	// Security Code prompt is standing. Nothing advances until Accept or
	// Refuse resolves it.
	Verifying
	// Connected: both this side's prompt is resolved and the connection is
	// live.
	Connected
	// Reconnecting: the connection dropped and is being re-established
	// within the ReconnectBudget — the Peer redialing, the Host waiting at
	// its Rendezvous. Messages written here are queued and resent.
	Reconnecting
	// Disconnected: the Session ended — deliberately, or because the
	// connection was lost. Terminal.
	Disconnected
	// Failed: the Session never got going, for the Reason on the event.
	// Terminal.
	Failed
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case Idle:
		return "Idle"
	case Hosting:
		return "Hosting"
	case Connecting:
		return "Connecting"
	case Verifying:
		return "Verifying"
	case Connected:
		return "Connected"
	case Reconnecting:
		return "Reconnecting"
	case Disconnected:
		return "Disconnected"
	case Failed:
		return "Failed"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Reason says why a Session reached Disconnected or Failed; it is ReasonNone
// on every other transition.
type Reason int

const (
	// ReasonNone: the transition needs no explaining.
	ReasonNone Reason = iota
	// ReasonRefused: this side refused the Security Code prompt.
	ReasonRefused
	// ReasonRejected: the Host turned this side away — the Invite is locked
	// to another Identity.
	ReasonRejected
	// ReasonHandshakeFailed: the handshake never completed. A wrong Password
	// looks exactly like this, by design.
	ReasonHandshakeFailed
	// ReasonConnectionLost: the connection died under an established Session.
	ReasonConnectionLost
	// ReasonTransportFailed: the WebRTC transport never came up — the offer
	// or ICE timed out, or the DTLS certificate wasn't the one the handshake
	// bound.
	ReasonTransportFailed
	// ReasonRendezvousGone: a reconnect found nothing where the Rendezvous
	// used to be. The Session is unrecoverable — a fresh Invite is the only
	// way forward.
	ReasonRendezvousGone
)

// String implements fmt.Stringer.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonRefused:
		return "refused"
	case ReasonRejected:
		return "rejected"
	case ReasonHandshakeFailed:
		return "handshake failed"
	case ReasonConnectionLost:
		return "connection lost"
	case ReasonTransportFailed:
		return "transport failed"
	case ReasonRendezvousGone:
		return "rendezvous gone"
	}
	return fmt.Sprintf("Reason(%d)", int(r))
}
