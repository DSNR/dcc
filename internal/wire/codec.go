package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Encode renders a frame as the bytes to put on the wire. A frame that
// violates the schema is refused here rather than sent, so the sender learns
// about it instead of the receiver.
func Encode(f Frame) ([]byte, error) {
	if f == nil {
		return nil, errors.New("wire: encoding a nil frame")
	}
	t := f.Type()
	if err := validate(f); err != nil {
		return nil, fmt.Errorf("wire: encoding %s: %w", t, err)
	}
	out, err := marshalObject(fmt.Sprintf(`{"t":%q,"v":%d`, string(t), Version), f)
	if err != nil {
		return nil, fmt.Errorf("wire: encoding %s: %w", t, err)
	}
	if len(out) > MaxFrameSize {
		return nil, fmt.Errorf("wire: encoding %s: frame is %d bytes, over the %d byte cap", t, len(out), MaxFrameSize)
	}
	return out, nil
}

// marshalObject renders fields as a JSON object and splices head — an opening
// brace plus the fields that must come first — in front of them. Marshalling
// a struct always yields an object, so body[0] is '{' and dropping it leaves
// exactly the remaining fields.
//
// HTML escaping is off. Nothing here is read by a browser, and leaving it on
// would spend six bytes on every < > or & — enough for a body that is legal
// at the 16 KiB text cap to breach the 64 KiB frame cap.
func marshalObject(head string, fields any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fields); err != nil {
		return nil, err
	}
	body := bytes.TrimRight(buf.Bytes(), "\n")
	if string(body) == "{}" {
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), body[1:]...), nil
}

// frameSpec is what the decoder knows about one frame type: the path it may
// arrive on, whether a malformed one is survivable, and how to build it.
type frameSpec struct {
	path Path
	// fatal marks a frame the connection cannot continue without. Only the
	// SDP exchange qualifies: Signaling has no way to proceed past a broken
	// offer or answer, whereas any other frame can simply be dropped.
	fatal  bool
	decode func([]byte) (Frame, error)
}

// frameSpecs enumerates every type in the protocol. A type absent from this
// table is unknown, whatever else the frame contains.
var frameSpecs = map[Type]frameSpec{
	TypeOffer:    {path: PathSignal, fatal: true, decode: decodeAs[Offer]},
	TypeAnswer:   {path: PathSignal, fatal: true, decode: decodeAs[Answer]},
	TypeICE:      {path: PathSignal, decode: decodeICE},
	TypeRejected: {path: PathSignal, decode: decodeAs[Rejected]},

	TypeText:   {path: PathData, decode: decodeAs[Text]},
	TypeAck:    {path: PathData, decode: decodeAs[Ack]},
	TypeBye:    {path: PathData, decode: decodeAs[Bye]},
	TypeCall:   {path: PathData, decode: decodeAs[Call]},
	TypeAccept: {path: PathData, decode: decodeAs[Accept]},
	TypeReject: {path: PathData, decode: decodeAs[Reject]},
	TypeHangup: {path: PathData, decode: decodeAs[Hangup]},
	TypeMedia:  {path: PathData, decode: decodeMedia},
}

// Decode parses one frame received on the given path. On failure the error is
// a *DecodeError whose Disposition says whether to ignore the frame, drop it,
// or close the connection; DispositionOf reads it back out.
func Decode(p Path, b []byte) (Frame, error) {
	// The size check comes first: an oversized frame is never parsed. On the
	// DataChannel that is one lost message, but nothing that large is ever
	// legitimate Signaling, so on the WebSocket it ends the connection.
	if len(b) > MaxFrameSize {
		d := DropFrame
		if p == PathSignal {
			d = CloseConnection
		}
		return nil, &DecodeError{Disposition: d, Reason: fmt.Sprintf("frame is %d bytes, over the %d byte cap", len(b), MaxFrameSize)}
	}

	// "v" is read as raw JSON rather than an int so that a version which is
	// present but not an integer is still reported as a version problem,
	// instead of failing the whole envelope as unparseable.
	var env struct {
		T Type            `json:"t"`
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, &DecodeError{Disposition: DropFrame, Reason: "not a JSON object", Err: err}
	}

	// An unknown type is checked before the version, because a frame we can't
	// identify is one we can't act on either way — and a newer peer may have
	// added it without a version bump.
	spec, known := frameSpecs[env.T]
	if !known {
		return nil, &DecodeError{Disposition: IgnoreFrame, Type: env.T, Reason: "unknown type"}
	}
	if spec.path != p {
		return nil, &DecodeError{Disposition: IgnoreFrame, Type: env.T, Reason: fmt.Sprintf("not carried on the %s path", p)}
	}

	// The version is protocol-wide, so a known type at anything other than
	// our version means the two sides no longer agree on what these bytes
	// mean, and there is nothing to salvage.
	var v int
	if len(env.V) == 0 || json.Unmarshal(env.V, &v) != nil || v != Version {
		return nil, &DecodeError{Disposition: CloseConnection, Type: env.T, Reason: fmt.Sprintf("protocol version %s, want %d", versionString(env.V), Version)}
	}

	bad := DropFrame
	if spec.fatal {
		bad = CloseConnection
	}
	f, err := spec.decode(b)
	if err != nil {
		return nil, &DecodeError{Disposition: bad, Type: env.T, Reason: "malformed", Err: err}
	}
	if err := validate(f); err != nil {
		return nil, &DecodeError{Disposition: bad, Type: env.T, Reason: err.Error()}
	}
	return f, nil
}

// versionString renders a frame's version for an error message, including
// when the frame carried none.
func versionString(v json.RawMessage) string {
	if len(v) == 0 {
		return "missing"
	}
	return string(v)
}

// decodeAs unmarshals a frame's fields into its concrete type. Fields the
// type doesn't declare — including the envelope's own "t" and "v" — are
// ignored, which is what lets the protocol grow without a version bump.
func decodeAs[T Frame](b []byte) (Frame, error) {
	var f T
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f, nil
}

// decodeICE exists because mline is an int whose zero value is also a valid
// mline, so an absent field can only be told from a present one by decoding
// into a pointer.
func decodeICE(b []byte) (Frame, error) {
	var raw struct {
		Candidate *string `json:"candidate"`
		Mid       *string `json:"mid"`
		MLine     *int    `json:"mline"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw.Candidate == nil || raw.Mid == nil || raw.MLine == nil {
		return nil, errors.New("candidate, mid and mline are all required")
	}
	return ICE{Candidate: *raw.Candidate, Mid: *raw.Mid, MLine: *raw.MLine}, nil
}

// decodeMedia exists for the same reason as decodeICE: a missing bool and a
// false one are indistinguishable once decoded, and Media is a state message
// where the difference matters.
func decodeMedia(b []byte) (Frame, error) {
	var raw struct {
		Mic    *bool `json:"mic"`
		Cam    *bool `json:"cam"`
		Screen *bool `json:"screen"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw.Mic == nil || raw.Cam == nil || raw.Screen == nil {
		return nil, errors.New("mic, cam and screen are all required")
	}
	return Media{Mic: *raw.Mic, Cam: *raw.Cam, Screen: *raw.Screen}, nil
}
