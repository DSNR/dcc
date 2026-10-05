package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameChars is the longest Display Name the protocol carries, counted in
// runes so that a name of emoji is measured the way its owner reads it.
const MaxNameChars = 64

// fingerprintHexLen is the length of a SHA-256 DTLS certificate fingerprint
// rendered the way the protocol carries it: lowercase hex, no colons.
const fingerprintHexLen = 64

// HostHello is the Host's Noise message 2 payload. It mints the Session and
// binds the Host's DTLS certificate to the authenticated handshake, which is
// what makes the WebRTC connection underneath it trustworthy.
type HostHello struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	DTLS      string `json:"dtls"`
}

// PeerHello is the Peer's Noise message 3 payload. A SessionID present is the
// Peer claiming to resume that Session, which the Host checks against the one
// it minted; absent, this is a fresh join.
type PeerHello struct {
	Name      string `json:"name"`
	DTLS      string `json:"dtls"`
	SessionID string `json:"session_id,omitempty"`
}

// EncodeHostHello renders the Host's handshake payload, trimming the name.
func EncodeHostHello(h HostHello) ([]byte, error) {
	h.Name = strings.TrimSpace(h.Name)
	if err := validateHello(h.SessionID, true, h.Name, h.DTLS); err != nil {
		return nil, fmt.Errorf("wire: encoding host hello: %w", err)
	}
	b, err := marshalObject(fmt.Sprintf(`{"v":%d`, Version), h)
	if err != nil {
		return nil, fmt.Errorf("wire: encoding host hello: %w", err)
	}
	return b, nil
}

// EncodePeerHello renders the Peer's handshake payload, trimming the name.
func EncodePeerHello(h PeerHello) ([]byte, error) {
	h.Name = strings.TrimSpace(h.Name)
	if err := validateHello(h.SessionID, false, h.Name, h.DTLS); err != nil {
		return nil, fmt.Errorf("wire: encoding peer hello: %w", err)
	}
	b, err := marshalObject(fmt.Sprintf(`{"v":%d`, Version), h)
	if err != nil {
		return nil, fmt.Errorf("wire: encoding peer hello: %w", err)
	}
	return b, nil
}

// DecodeHostHello parses the Host's handshake payload. Every failure is
// CloseConnection: a handshake that cannot be read leaves no Identity, no
// DTLS binding and no agreed version, so there is no Session to salvage.
func DecodeHostHello(b []byte) (HostHello, error) {
	var h HostHello
	if err := decodeHello(b, &h); err != nil {
		return HostHello{}, err
	}
	h.Name = strings.TrimSpace(h.Name)
	if err := validateHello(h.SessionID, true, h.Name, h.DTLS); err != nil {
		return HostHello{}, helloError(err)
	}
	return h, nil
}

// DecodePeerHello parses the Peer's handshake payload. As with the Host's,
// every failure is fatal to the connection.
func DecodePeerHello(b []byte) (PeerHello, error) {
	var h PeerHello
	if err := decodeHello(b, &h); err != nil {
		return PeerHello{}, err
	}
	h.Name = strings.TrimSpace(h.Name)
	if err := validateHello(h.SessionID, false, h.Name, h.DTLS); err != nil {
		return PeerHello{}, helloError(err)
	}
	return h, nil
}

// decodeHello checks the version and fills in payload, which must be a
// pointer to one of the hello structs.
func decodeHello(b []byte, payload any) error {
	var env struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return &DecodeError{Disposition: CloseConnection, Reason: "handshake payload is not a JSON object", Err: err}
	}
	var v int
	if len(env.V) == 0 || json.Unmarshal(env.V, &v) != nil || v != Version {
		return &DecodeError{Disposition: CloseConnection, Reason: fmt.Sprintf("handshake protocol version %s, want %d", versionString(env.V), Version)}
	}
	if err := json.Unmarshal(b, payload); err != nil {
		return &DecodeError{Disposition: CloseConnection, Reason: "malformed handshake payload", Err: err}
	}
	return nil
}

// helloError wraps a validation failure with the disposition every handshake
// problem carries.
func helloError(err error) error {
	return &DecodeError{Disposition: CloseConnection, Reason: "handshake payload: " + err.Error()}
}

// validateHello applies the rules shared by both payloads. The Host must name
// the Session it minted; for the Peer a Session id is the optional reconnect
// claim.
func validateHello(sessionID string, sessionRequired bool, name, dtls string) error {
	if sessionRequired || sessionID != "" {
		if err := validateID("session_id", sessionID); err != nil {
			return err
		}
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	return validateFingerprint(dtls)
}

// ValidateName reports whether a Display Name may be announced to a Peer. The
// UIs apply it to what the user types, so a name is refused while it can
// still be edited rather than at handshake time.
func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is empty")
	}
	if n := utf8.RuneCountInString(name); n > MaxNameChars {
		return fmt.Errorf("name is %d characters, over the %d character cap", n, MaxNameChars)
	}
	if !utf8.ValidString(name) {
		return errors.New("name is not valid UTF-8")
	}
	// Control characters would let a name disturb a terminal UI or forge line
	// breaks in a transcript.
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("name contains the control character %U", r)
		}
	}
	return nil
}

// validateFingerprint requires the one rendering of a SHA-256 certificate
// fingerprint the protocol carries, so that the two sides compare strings and
// never formats.
func validateFingerprint(dtls string) error {
	if dtls == "" {
		return errors.New("dtls is empty")
	}
	if len(dtls) != fingerprintHexLen {
		return fmt.Errorf("dtls is %d characters, want %d of lowercase hex", len(dtls), fingerprintHexLen)
	}
	for _, r := range dtls {
		if !('0' <= r && r <= '9') && !('a' <= r && r <= 'f') {
			return fmt.Errorf("dtls contains %q; want lowercase hex with no colons", r)
		}
	}
	return nil
}
