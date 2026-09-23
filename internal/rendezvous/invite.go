package rendezvous

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net/url"
	"strings"
)

// PasswordSize is the Password's length in bytes. 256 bits is exactly what
// Noise wants at PSK position 0, so the Password is mixed in as the PSK
// itself — there is no KDF between the Invite and the handshake, and so
// nothing in between to get wrong.
const PasswordSize = 32

// inviteVersion is the character the fragment opens with. It is not the
// protocol version: it versions the Invite's own shape, so that a later build
// can put something else after the '#' — a Host fingerprint, a key to gate on
// — and this build will say it cannot read it instead of guessing.
const inviteVersion = '1'

// passwordEncoding renders the Password as base32 without padding: 52
// characters, no case distinction to lose down a phone line and no '=' for a
// messenger to swallow. Decoding wants the upper-case alphabet, so parsing
// shifts case and printing shifts it back.
var passwordEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Password is the secret the Invite carries, minted fresh for each Rendezvous
// and held only in the Host's memory for as long as that Rendezvous lives. It
// is never transmitted — it is mixed into the Noise handshake as the
// pre-shared key, so a Peer with the wrong one simply fails to complete a
// handshake and learns nothing from the attempt.
type Password [PasswordSize]byte

// NewPassword mints one.
func NewPassword() (Password, error) {
	var p Password
	if _, err := rand.Read(p[:]); err != nil {
		return Password{}, fmt.Errorf("rendezvous: reading randomness for the Password: %w", err)
	}
	return p, nil
}

// String renders the Password as the Invite carries it. It belongs in an
// Invite and nowhere else: not in a log line, not in a URL anything makes a
// request to.
func (p Password) String() string {
	return strings.ToLower(passwordEncoding.EncodeToString(p[:]))
}

// ParsePassword reads a rendered Password back, in either case.
func ParsePassword(s string) (Password, error) {
	raw, err := passwordEncoding.DecodeString(strings.ToUpper(s))
	if err != nil {
		return Password{}, fmt.Errorf("rendezvous: the Invite's password is not base32: %w", err)
	}
	if len(raw) != PasswordSize {
		return Password{}, fmt.Errorf("rendezvous: the Invite's password is %d bytes, want %d", len(raw), PasswordSize)
	}
	var p Password
	copy(p[:], raw)
	return p, nil
}

// Invite is everything a Peer needs to join a Session: where the Rendezvous
// is, and the Password that gets through it. It is one string, handed over
// whatever channel the two people already trust.
type Invite struct {
	// URL is the Rendezvous's base URL — scheme and host, no path. In
	// ordinary use that is https://<host>.trycloudflare.com; under test it is
	// the http://127.0.0.1:<port> of a local listener standing in for one.
	URL string
	// Password gates joining this Rendezvous.
	Password Password
}

// String renders the Invite canonically, which is the only way it is ever
// written down. The Password sits in the fragment: fragments are not sent
// with a request, so a recipient who pastes an Invite into a browser hands
// Cloudflare the hostname and nothing else.
func (i Invite) String() string {
	return i.URL + "/#" + string(inviteVersion) + i.Password.String()
}

// SignalURL is the WebSocket address the Peer dials to begin Signaling. It
// carries no Password, by construction.
func (i Invite) SignalURL() string {
	switch {
	case strings.HasPrefix(i.URL, "https://"):
		return "wss://" + strings.TrimPrefix(i.URL, "https://") + PathSignal
	case strings.HasPrefix(i.URL, "http://"):
		return "ws://" + strings.TrimPrefix(i.URL, "http://") + PathSignal
	}
	return i.URL + PathSignal
}

// ParseInvite reads an Invite a person pasted. It is deliberately forgiving of
// what messengers and browsers do to a URL on its way across — a missing
// scheme, a shouted hostname, whitespace around the edges — and unforgiving
// about anything that would change where the Peer ends up connecting or with
// what secret.
func ParseInvite(s string) (Invite, error) {
	base, fragment, ok := strings.Cut(strings.TrimSpace(s), "#")
	if !ok {
		return Invite{}, fmt.Errorf("rendezvous: %q is not an Invite: it has no #password", s)
	}

	endpoint, err := parseEndpoint(base)
	if err != nil {
		return Invite{}, err
	}
	if fragment == "" {
		return Invite{}, fmt.Errorf("rendezvous: the Invite carries no password after its #")
	}
	if version := fragment[0]; version != inviteVersion {
		return Invite{}, fmt.Errorf("rendezvous: this build reads version %c Invites, and that one is version %q",
			inviteVersion, string(version))
	}
	password, err := ParsePassword(fragment[1:])
	if err != nil {
		return Invite{}, err
	}
	return Invite{URL: endpoint, Password: password}, nil
}

// parseEndpoint normalises the part of an Invite before the '#' into a base
// URL. Anything beyond a scheme and a host is refused rather than dropped: a
// path or a query in an Invite means the string is not the one the Host
// produced, and quietly ignoring the difference is how a Peer ends up
// connecting somewhere it did not mean to.
func parseEndpoint(base string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("rendezvous: the Invite names no Rendezvous")
	}
	// A bare hostname is what is left when a messenger strips the scheme, and
	// the Rendezvous is always https unless it is a local one under test.
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("rendezvous: the Invite's address is not a URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("rendezvous: the Invite's address is %s, and a Rendezvous is reached over https", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("rendezvous: the Invite names no Rendezvous host")
	}
	if u.User != nil {
		return "", fmt.Errorf("rendezvous: the Invite's address carries credentials, which a Rendezvous never uses")
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("rendezvous: the Invite's address carries a query, which a Rendezvous never uses")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("rendezvous: the Invite's address carries the path %q, and a Rendezvous is a host on its own", u.Path)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
