package wire

import (
	"errors"
	"fmt"
)

// Disposition is what a receiver must do about a frame it could not accept.
// Decoding a frame fails for three quite different reasons, and conflating
// them either kills Sessions that should survive or keeps alive connections
// that can no longer be trusted.
type Disposition int

const (
	// IgnoreFrame: the frame is well-formed but means nothing to us — a type
	// a newer peer knows and we don't. Log at debug and carry on.
	IgnoreFrame Disposition = iota + 1
	// DropFrame: the frame is malformed. Discard it, log it, and leave the
	// Session up; one bad message is not a bad connection.
	DropFrame
	// CloseConnection: the frame was load-bearing and we cannot honour it —
	// a broken offer or answer, or a version we don't speak. There is no
	// recovering the connection from here.
	CloseConnection
)

// String implements fmt.Stringer.
func (d Disposition) String() string {
	switch d {
	case IgnoreFrame:
		return "ignore"
	case DropFrame:
		return "drop"
	case CloseConnection:
		return "close"
	}
	return fmt.Sprintf("Disposition(%d)", int(d))
}

// DecodeError reports a frame Decode would not accept, and carries the
// Disposition saying what to do about it.
type DecodeError struct {
	// Disposition is the receiver's obligation.
	Disposition Disposition
	// Type is the frame's "t" value, empty when it could not be read.
	Type Type
	// Reason describes the problem in the protocol's own terms.
	Reason string
	// Err is the underlying cause, if the problem came from elsewhere.
	Err error
}

// Error implements error.
func (e *DecodeError) Error() string {
	subject := "frame"
	if e.Type != "" {
		subject = string(e.Type)
	}
	if e.Err != nil {
		return fmt.Sprintf("wire: %s: %s: %v (%s)", subject, e.Reason, e.Err, e.Disposition)
	}
	return fmt.Sprintf("wire: %s: %s (%s)", subject, e.Reason, e.Disposition)
}

// Unwrap implements errors.Unwrap.
func (e *DecodeError) Unwrap() error { return e.Err }

// DispositionOf reports what to do about an error from Decode. An error from
// anywhere else is treated as a malformed frame, the conservative choice: the
// frame is discarded but the Session survives. It returns zero for a nil
// error.
func DispositionOf(err error) Disposition {
	if err == nil {
		return 0
	}
	var de *DecodeError
	if errors.As(err, &de) {
		return de.Disposition
	}
	return DropFrame
}
