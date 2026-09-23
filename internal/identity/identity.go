package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeySize is the length of every key this package deals in: the X25519
// Identity keypair's halves and the database key.
const KeySize = 32

// PublicKey is an Identity's public half — an X25519 point. It is also the
// participant id the protocol uses, base64-encoded; there is no separate id.
type PublicKey [KeySize]byte

// String renders the key the way the protocol carries it.
func (k PublicKey) String() string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// ParsePublicKey reads a participant id back into a key.
func ParsePublicKey(s string) (PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return PublicKey{}, fmt.Errorf("identity: public key is not base64: %w", err)
	}
	if len(b) != KeySize {
		return PublicKey{}, fmt.Errorf("identity: public key is %d bytes, want %d", len(b), KeySize)
	}
	var k PublicKey
	copy(k[:], b)
	return k, nil
}

// Identity is one install's persistent local keypair, plus the key its
// database is encrypted under. Its secrets are unexported so that neither a
// log line nor an accidental json.Marshal can spill them; Private and DBKey
// are the deliberate way out.
type Identity struct {
	private [KeySize]byte
	public  PublicKey
	dbKey   [KeySize]byte
}

// Public reports the Identity public key — what the Peer pins and what the
// Security Code is derived from.
func (id Identity) Public() PublicKey { return id.public }

// Private reports the X25519 static private key the Noise handshake uses. It
// is already clamped per RFC 7748, so deriving the public key from it yields
// Public again.
func (id Identity) Private() [KeySize]byte { return id.private }

// DBKey reports the key the storage package encrypts sensitive fields under.
// It is independent of the Identity key so that neither use constrains the
// other, and it travels in the same file so that one backup restores both
// recognition and readable history.
func (id Identity) DBKey() [KeySize]byte { return id.dbKey }

// SecurityCode derives the code this Identity and peer compare out-of-band.
func (id Identity) SecurityCode(peer PublicKey) SecurityCode {
	return SecurityCodeFor(id.public, peer)
}

// String implements fmt.Stringer with the public half only, so that printing
// an Identity anywhere is safe.
func (id Identity) String() string {
	return "identity " + id.public.String()
}

// generate mints a fresh Identity: an X25519 keypair and an unrelated random
// database key.
func generate() (Identity, error) {
	var id Identity
	if _, err := rand.Read(id.private[:]); err != nil {
		return Identity{}, fmt.Errorf("identity: reading randomness: %w", err)
	}
	if _, err := rand.Read(id.dbKey[:]); err != nil {
		return Identity{}, fmt.Errorf("identity: reading randomness: %w", err)
	}
	clamp(&id.private)
	pub, err := publicFor(id.private)
	if err != nil {
		return Identity{}, err
	}
	id.public = pub
	return id, nil
}

// clamp applies RFC 7748's scalar clamping before the key is stored, so that
// what is on disk is already canonical: every library that later loads it
// derives the same public key, whether or not it clamps for itself.
func clamp(k *[KeySize]byte) {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}

// publicFor derives the public half of an X25519 key.
func publicFor(private [KeySize]byte) (PublicKey, error) {
	b, err := curve25519.X25519(private[:], curve25519.Basepoint)
	if err != nil {
		// X25519 refuses a scalar that yields a low-order point, which a
		// clamped random key never is — so this means the key is not one we
		// generated.
		return PublicKey{}, fmt.Errorf("identity: %w: %w", errUnusableKey, err)
	}
	var k PublicKey
	copy(k[:], b)
	return k, nil
}

// errUnusableKey marks a stored key that parses but is not a usable X25519
// scalar, which the loader treats as damage like any other.
var errUnusableKey = errors.New("identity key is not a usable X25519 key")
