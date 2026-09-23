// Package wire is the canonical definition of the dcc protocol's message
// schema. Every frame that crosses the Rendezvous WebSocket or the WebRTC
// DataChannel is encoded and decoded here, and nowhere else.
//
// A frame is a flat JSON object carrying a type discriminator and the
// protocol version — {"t":"text","v":1,...} — with the remaining fields
// determined by the type. The golden files under testdata/golden are the
// schema's source of truth; they are hand-maintained from the spec rather
// than regenerated from this code.
package wire

import "fmt"

// Version is the protocol-wide version carried on every frame and in both
// Noise handshake payloads. It is bumped only for incompatible changes, and
// then only together with the Noise prologue; adding a frame type or an
// optional field does not bump it.
const Version = 1

// The limits the protocol places on a frame's size.
const (
	// MaxFrameSize is the largest decoded frame either path will accept.
	MaxFrameSize = 64 * 1024
	// MaxTextBytes is the largest Text body, leaving ample room inside
	// MaxFrameSize for the rest of the frame.
	MaxTextBytes = 16 * 1024
)

// Type is a frame's type discriminator — the value of its "t" field.
type Type string

// The frame types carried on the Rendezvous WebSocket.
const (
	TypeOffer    Type = "offer"
	TypeAnswer   Type = "answer"
	TypeICE      Type = "ice"
	TypeRejected Type = "rejected"
)

// The frame types carried on the WebRTC DataChannel.
const (
	TypeText   Type = "text"
	TypeAck    Type = "ack"
	TypeBye    Type = "bye"
	TypeCall   Type = "call"
	TypeAccept Type = "accept"
	TypeReject Type = "reject"
	TypeHangup Type = "hangup"
	TypeMedia  Type = "media"
)

// Path is the transport a frame travelled over. The two paths carry disjoint
// sets of frame types, and differ in how they treat an oversized frame.
type Path int

const (
	// PathSignal is the Noise-encrypted WebSocket at the Rendezvous, which
	// carries Signaling only.
	PathSignal Path = iota + 1
	// PathData is the WebRTC DataChannel, which carries text and control.
	PathData
)

// String implements fmt.Stringer.
func (p Path) String() string {
	switch p {
	case PathSignal:
		return "signal"
	case PathData:
		return "data"
	}
	return fmt.Sprintf("Path(%d)", int(p))
}

// Frame is one decoded protocol message. The concrete types are the structs
// below; a receiver switches on them.
type Frame interface {
	// Type reports the frame's "t" value.
	Type() Type
}

// Offer carries an SDP offer. The same frame is reused to renegotiate when a
// Call starts.
type Offer struct {
	SDP string `json:"sdp"`
}

// Type implements Frame.
func (Offer) Type() Type { return TypeOffer }

// Answer carries an SDP answer.
type Answer struct {
	SDP string `json:"sdp"`
}

// Type implements Frame.
func (Answer) Type() Type { return TypeAnswer }

// ICE carries one trickled ICE candidate. An empty Candidate means
// end-of-candidates rather than a malformed candidate.
type ICE struct {
	Candidate string `json:"candidate"`
	Mid       string `json:"mid"`
	MLine     int    `json:"mline"`
}

// Type implements Frame.
func (ICE) Type() Type { return TypeICE }

// Rejected tells a Peer the Host will not accept it — the Invite is already
// locked to another Identity.
type Rejected struct {
	Reason RejectedReason `json:"reason"`
}

// Type implements Frame.
func (Rejected) Type() Type { return TypeRejected }

// RejectedReason says why the Host refused the connection.
type RejectedReason string

// ReasonLocked: the Invite is locked to the first authenticated Identity.
const ReasonLocked RejectedReason = "locked"

// Text is a chat message. ID is a UUIDv7, which carries the send time, so no
// timestamp travels with the frame — the receiver records arrival itself.
type Text struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// Type implements Frame.
func (Text) Type() Type { return TypeText }

// Ack acknowledges a Text at the application level; receiving one is what
// marks a message delivered.
type Ack struct {
	ID string `json:"id"`
}

// Type implements Frame.
func (Ack) Type() Type { return TypeAck }

// Bye announces a deliberate disconnect, so the other side can distinguish it
// from a dropped connection.
type Bye struct{}

// Type implements Frame.
func (Bye) Type() Type { return TypeBye }

// Call invites the other participant to a Call.
type Call struct {
	CallID string `json:"call_id"`
}

// Type implements Frame.
func (Call) Type() Type { return TypeCall }

// Accept answers a Call invitation. CallID lets a late Accept for a Call that
// has already timed out be discarded.
type Accept struct {
	CallID string `json:"call_id"`
}

// Type implements Frame.
func (Accept) Type() Type { return TypeAccept }

// Reject declines a Call invitation.
type Reject struct {
	CallID string       `json:"call_id"`
	Reason RejectReason `json:"reason"`
}

// Type implements Frame.
func (Reject) Type() Type { return TypeReject }

// RejectReason says why a Call invitation was declined.
type RejectReason string

// The reasons a Call may be rejected.
const (
	// ReasonBusy: the callee is already in a Call.
	ReasonBusy RejectReason = "busy"
	// ReasonDeclined: the callee chose not to answer.
	ReasonDeclined RejectReason = "declined"
	// ReasonTimeout: the invitation rang out unanswered.
	ReasonTimeout RejectReason = "timeout"
)

// Hangup ends an Active Call, leaving the Session up for text.
type Hangup struct {
	CallID string `json:"call_id"`
}

// Type implements Frame.
func (Hangup) Type() Type { return TypeHangup }

// Media announces which of the sender's streams are live. It carries no
// call_id and is meaningful only while a Call is Active.
type Media struct {
	Mic    bool `json:"mic"`
	Cam    bool `json:"cam"`
	Screen bool `json:"screen"`
}

// Type implements Frame.
func (Media) Type() Type { return TypeMedia }
