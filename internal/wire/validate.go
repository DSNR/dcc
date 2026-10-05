package wire

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

// validate checks the rules the JSON grammar can't express: which fields must
// be present, which enums are closed, and how long a body may be. Both
// directions run it, so the same schema is enforced on what we send and on
// what we accept.
func validate(f Frame) error {
	switch f := f.(type) {
	case Offer:
		return required("sdp", f.SDP)
	case Answer:
		return required("sdp", f.SDP)
	case ICE:
		return validateICE(f)
	case Rejected:
		if f.Reason != ReasonLocked {
			return fmt.Errorf("unknown reason %q", f.Reason)
		}
	case Text:
		if err := validateID("id", f.ID); err != nil {
			return err
		}
		return validateBody(f.Body)
	case Ack:
		return validateID("id", f.ID)
	case Bye:
	case Call:
		return validateID("call_id", f.CallID)
	case Accept:
		return validateID("call_id", f.CallID)
	case Hangup:
		return validateID("call_id", f.CallID)
	case Reject:
		if err := validateID("call_id", f.CallID); err != nil {
			return err
		}
		switch f.Reason {
		case ReasonBusy, ReasonDeclined, ReasonTimeout:
		default:
			return fmt.Errorf("unknown reason %q", f.Reason)
		}
	case Media:
	default:
		return fmt.Errorf("unknown frame type %T", f)
	}
	return nil
}

// validateICE allows the empty candidate that signals end-of-candidates, but
// insists a real candidate says which m-line it belongs to.
func validateICE(f ICE) error {
	if f.MLine < 0 {
		return fmt.Errorf("mline is %d", f.MLine)
	}
	if f.Candidate != "" && f.Mid == "" {
		return errors.New("a candidate must carry a mid")
	}
	return nil
}

// validateBody enforces the 16 KiB text cap. The cap is in bytes, not runes:
// it bounds what goes on the wire, not what the sender can say.
func validateBody(body string) error {
	if body == "" {
		return errors.New("body is empty")
	}
	if len(body) > MaxTextBytes {
		return fmt.Errorf("body is %d bytes, over the %d byte cap", len(body), MaxTextBytes)
	}
	if !utf8.ValidString(body) {
		return errors.New("body is not valid UTF-8")
	}
	return nil
}

// validateID requires the canonical hyphenated UUID form. The version is not
// checked: dcc mints UUIDv7 so that an id carries its own timestamp, but a
// receiver only ever treats an id as opaque, and rejecting a peer's
// well-formed id over its version buys nothing.
func validateID(field, id string) error {
	if id == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if len(id) != 36 || uuid.Validate(id) != nil {
		return fmt.Errorf("%s %q is not a UUID", field, id)
	}
	return nil
}

// required reports a missing mandatory string field.
func required(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", field)
	}
	return nil
}
