package transport_test

import (
	"bytes"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/DSNR/dcc/internal/relay"
	"github.com/DSNR/dcc/internal/transport"
	"github.com/DSNR/dcc/internal/wire"
)

// tap is a loopback listener that pipes every connection to target while
// recording each direction's bytes — everything the relay carries, seen the
// way the relay sees it.
type tap struct {
	listener net.Listener

	mu       sync.Mutex
	recorded bytes.Buffer
}

func newTap(t *testing.T, target string) *tap {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tap listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	tp := &tap{listener: listener}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go tp.splice(c, target)
		}
	}()
	return tp
}

func (tp *tap) splice(c net.Conn, target string) {
	out, err := net.Dial("tcp", target)
	if err != nil {
		_ = c.Close()
		return
	}
	done := make(chan struct{}, 2)
	pump := func(dst net.Conn, src net.Conn) {
		_, _ = io.Copy(io.MultiWriter(dst, tp), src)
		done <- struct{}{}
	}
	go pump(out, c)
	go pump(c, out)
	<-done
	_ = c.Close()
	_ = out.Close()
	<-done
}

// Write records one chunk of relayed bytes; it never fails, so recording
// never breaks the pipe it observes.
func (tp *tap) Write(p []byte) (int, error) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.recorded.Write(p)
}

func (tp *tap) bytes() []byte {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return bytes.Clone(tp.recorded.Bytes())
}

// TestRelayOnlyConnectsRelayed blocks every direct path and checks the
// Session's transport still comes up — through the relay, reported as
// relayed, carrying frames — and that the bytes crossing the relay never
// contain the plaintext they deliver.
func TestRelayOnlyConnectsRelayed(t *testing.T) {
	hostCert, peerCert := newCertificate(t), newCertificate(t)

	listener := relay.NewListener()
	srv := httptest.NewServer(listener)
	t.Cleanup(srv.Close)
	bridge, err := relay.Open(srv.URL)
	if err != nil {
		t.Fatalf("relay.Open: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	tp := newTap(t, bridge.Addr())

	host := start(t, transport.Options{
		Certificate:   hostCert,
		Remote:        peerCert.Fingerprint(),
		RelayListener: listener,
		RelayOnly:     true,
	})
	peer := start(t, transport.Options{
		Certificate: peerCert,
		Remote:      hostCert.Fingerprint(),
		Initiator:   true,
		RelayAddr:   tp.listener.Addr().String(),
		RelayOnly:   true,
	})
	connect(host, peer)

	if link := waitUp(t, host); link != transport.LinkRelayed {
		t.Errorf("host came up %v, want %v", link, transport.LinkRelayed)
	}
	if link := waitUp(t, peer); link != transport.LinkRelayed {
		t.Errorf("peer came up %v, want %v", link, transport.LinkRelayed)
	}

	secret := "the relay must never see this sentence"
	if err := peer.tr.Send(wire.Text{ID: uuid.Must(uuid.NewV7()).String(), Body: secret}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	f := waitFrame(t, host)
	text, ok := f.(wire.Text)
	if !ok {
		t.Fatalf("host received %T, want wire.Text", f)
	}
	if text.Body != secret {
		t.Errorf("host received %q, want %q", text.Body, secret)
	}

	if relayed := tp.bytes(); len(relayed) == 0 {
		t.Errorf("nothing crossed the relay, so the message went some other way")
	} else if bytes.Contains(relayed, []byte(secret)) {
		t.Errorf("the message's plaintext crossed the relay")
	}
}
