package transport_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DSNR/dcc/internal/transport"
	"github.com/DSNR/dcc/internal/wire"
)

// waitTimeout bounds every wait for an expected callback. Localhost ICE
// settles in milliseconds; hitting this means something is wedged.
const waitTimeout = 20 * time.Second

// end is one side of a Transport pair, with its callbacks turned into
// channels a test can wait on.
type end struct {
	tr     *transport.Transport
	toPeer chan wire.Frame
	up     chan transport.Link
	frames chan wire.Frame
	down   chan error
}

func newCertificate(t *testing.T) transport.Certificate {
	t.Helper()
	cert, err := transport.NewCertificate()
	if err != nil {
		t.Fatalf("NewCertificate: %v", err)
	}
	return cert
}

// start builds one Transport whose outgoing Signaling frames queue in toPeer
// until connect wires them to the other side.
func start(t *testing.T, opts transport.Options) *end {
	t.Helper()
	e := &end{
		toPeer: make(chan wire.Frame, 64),
		up:     make(chan transport.Link, 1),
		frames: make(chan wire.Frame, 64),
		down:   make(chan error, 1),
	}
	opts.Signal = func(f wire.Frame) { e.toPeer <- f }
	opts.Up = func(l transport.Link) { e.up <- l }
	opts.Frame = func(f wire.Frame) { e.frames <- f }
	opts.Down = func(err error) { e.down <- err }
	tr, err := transport.Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	e.tr = tr
	t.Cleanup(func() { _ = tr.Close() })
	return e
}

// connect starts pumping each side's queued Signaling frames into the other,
// as the Session's readLoop would.
func connect(a, b *end) {
	go func() {
		for f := range a.toPeer {
			b.tr.HandleSignal(f)
		}
	}()
	go func() {
		for f := range b.toPeer {
			a.tr.HandleSignal(f)
		}
	}()
}

func waitUp(t *testing.T, e *end) transport.Link {
	t.Helper()
	select {
	case l := <-e.up:
		return l
	case err := <-e.down:
		t.Fatalf("Transport went down instead of up: %v", err)
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for the Transport to come up")
	}
	return 0
}

func waitDown(t *testing.T, e *end) error {
	t.Helper()
	select {
	case err := <-e.down:
		return err
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for the Transport to go down")
		return nil
	}
}

func waitFrame(t *testing.T, e *end) wire.Frame {
	t.Helper()
	select {
	case f := <-e.frames:
		return f
	case err := <-e.down:
		t.Fatalf("Transport went down while waiting for a frame: %v", err)
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for a frame")
	}
	return nil
}

// TestCertificateFingerprint checks a minted Certificate renders its
// fingerprint the one way the protocol carries it.
func TestCertificateFingerprint(t *testing.T) {
	fp := newCertificate(t).Fingerprint()
	if len(fp) != 64 {
		t.Errorf("fingerprint is %d characters, want 64", len(fp))
	}
	if fp != strings.ToLower(fp) || strings.Contains(fp, ":") {
		t.Errorf("fingerprint %q is not lowercase colon-free hex", fp)
	}
}

// TestTransportsConnectAndExchange drives two Transports over real pion on
// localhost: both come Up direct, a text frame crosses one way and its ack
// crosses back, and closing one side takes the other down.
func TestTransportsConnectAndExchange(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	b := start(t, transport.Options{Certificate: certB, Remote: certA.Fingerprint()})
	connect(a, b)

	if link := waitUp(t, a); link != transport.LinkDirect {
		t.Errorf("initiator came up %v, want %v", link, transport.LinkDirect)
	}
	if link := waitUp(t, b); link != transport.LinkDirect {
		t.Errorf("responder came up %v, want %v", link, transport.LinkDirect)
	}

	id := uuid.Must(uuid.NewV7()).String()
	if err := a.tr.Send(wire.Text{ID: id, Body: "hello over DTLS 🎉"}); err != nil {
		t.Fatalf("Send text: %v", err)
	}
	text, ok := waitFrame(t, b).(wire.Text)
	if !ok || text.ID != id || text.Body != "hello over DTLS 🎉" {
		t.Fatalf("responder received %#v, want the text that was sent", text)
	}

	if err := b.tr.Send(wire.Ack{ID: id}); err != nil {
		t.Fatalf("Send ack: %v", err)
	}
	if ack, ok := waitFrame(t, a).(wire.Ack); !ok || ack.ID != id {
		t.Fatalf("initiator received %#v, want the ack", ack)
	}

	if err := a.tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitDown(t, b)
}

// TestFingerprintMismatch gives the responder a Remote fingerprint the
// initiator's certificate cannot hash to: the responder must go down without
// ever coming up.
func TestFingerprintMismatch(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	b := start(t, transport.Options{Certificate: certB, Remote: strings.Repeat("00", 32)})
	connect(a, b)

	err := waitDown(t, b)
	if !strings.Contains(err.Error(), "handshake bound") {
		t.Errorf("responder went down with %q, want the fingerprint mismatch", err)
	}
	select {
	case l := <-b.up:
		t.Errorf("responder came up %v despite the mismatch", l)
	default:
	}
}

// TestOfferTimeout leaves a responder waiting for an offer that never comes.
func TestOfferTimeout(t *testing.T) {
	cert := newCertificate(t)
	b := start(t, transport.Options{Certificate: cert, Remote: strings.Repeat("11", 32), OfferWait: 100 * time.Millisecond})
	err := waitDown(t, b)
	if !strings.Contains(err.Error(), "no offer") {
		t.Errorf("responder went down with %q, want the offer timeout", err)
	}
}
