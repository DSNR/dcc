package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pion/webrtc/v4"
)

// Certificate is one Session's DTLS identity. It is minted fresh for every
// Session, so the fingerprint the handshake binds never identifies anyone
// across Sessions — the persistent Identity does that.
type Certificate struct {
	cert        webrtc.Certificate
	fingerprint string
}

// NewCertificate mints a fresh DTLS certificate.
func NewCertificate() (Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, fmt.Errorf("transport: generating a certificate key: %w", err)
	}
	cert, err := webrtc.GenerateCertificate(key)
	if err != nil {
		return Certificate{}, fmt.Errorf("transport: minting a certificate: %w", err)
	}
	fps, err := cert.GetFingerprints()
	if err != nil {
		return Certificate{}, fmt.Errorf("transport: fingerprinting the certificate: %w", err)
	}
	for _, fp := range fps {
		if fp.Algorithm == "sha-256" {
			return Certificate{cert: *cert, fingerprint: strings.ReplaceAll(fp.Value, ":", "")}, nil
		}
	}
	return Certificate{}, fmt.Errorf("transport: the certificate has no sha-256 fingerprint")
}

// Fingerprint is the SHA-256 certificate fingerprint the way the protocol
// carries it — lowercase hex, no colons — ready for a handshake hello.
func (c Certificate) Fingerprint() string { return c.fingerprint }

// fingerprintDER renders a raw DER certificate the same way, for comparing a
// remote certificate against the fingerprint its handshake bound.
func fingerprintDER(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
