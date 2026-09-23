package signaling

import (
	"fmt"
	"time"

	"github.com/flynn/noise"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
)

// prologue binds both sides to this protocol generation. It is bumped only
// together with wire.Version, so two incompatible builds never complete a
// handshake at all.
const prologue = "dcc-noise-1"

// HandshakeTimeout is how long either side gives the whole handshake, from
// the WebSocket opening to the transport keys existing. A well-meaning peer
// finishes in a round trip or two; anything still unfinished after this is
// holding a connection slot for nothing.
const HandshakeTimeout = 10 * time.Second

// PingInterval is how often an authenticated connection pings, so that a
// dead Rendezvous path is noticed in seconds rather than at the TCP stack's
// leisure.
const PingInterval = 25 * time.Second

// MaxUnauthenticated caps how many connections may sit in the handshake at
// once. The cap is per Rendezvous and generous — a legitimate Session needs
// exactly one — and is what keeps a leaked URL from tying up the Host.
const MaxUnauthenticated = 4

// suite is the one cipher suite dcc speaks: Noise_XXpsk0_25519_ChaChaPoly_BLAKE2s
// per ADR 0001. XX because the Peer cannot know the Host's Identity key in
// advance; the Password at PSK position 0 — deliberately not the spec-listed
// psk3 — so a wrong Password fails at message 1 and the Host's Identity key
// is never sent to anyone holding only the URL.
var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// newHandshakeState builds the Noise handshake for one side.
func newHandshakeState(id identity.Identity, password rendezvous.Password, initiator bool) (*noise.HandshakeState, error) {
	private, public := id.Private(), id.Public()
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Pattern:               noise.HandshakeXX,
		Initiator:             initiator,
		Prologue:              []byte(prologue),
		PresharedKey:          password[:],
		PresharedKeyPlacement: 0,
		StaticKeypair:         noise.DHKey{Private: private[:], Public: public[:]},
	})
	if err != nil {
		return nil, fmt.Errorf("signaling: preparing the Noise handshake: %w", err)
	}
	return hs, nil
}

// peerKey reads the remote static key out of a completed handshake as the
// Identity it is.
func peerKey(hs *noise.HandshakeState) (identity.PublicKey, error) {
	raw := hs.PeerStatic()
	if len(raw) != identity.KeySize {
		return identity.PublicKey{}, fmt.Errorf("signaling: the peer's static key is %d bytes, want %d", len(raw), identity.KeySize)
	}
	var key identity.PublicKey
	copy(key[:], raw)
	return key, nil
}
