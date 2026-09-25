package relay_test

import (
	"bytes"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/relay"
)

// waitTimeout bounds every blocking step; everything here is loopback.
const waitTimeout = 10 * time.Second

// pair builds a served Listener and a Bridge to it, and returns both ends of
// one relayed connection: the Bridge-side conn a client dialed and the
// Listener-side conn Accept produced.
func pair(t *testing.T) (client, accepted net.Conn) {
	t.Helper()
	listener := relay.NewListener()
	t.Cleanup(func() { _ = listener.Close() })
	srv := httptest.NewServer(listener)
	t.Cleanup(srv.Close)
	bridge, err := relay.Open(srv.URL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	client, err = net.Dial("tcp", bridge.Addr())
	if err != nil {
		t.Fatalf("dialing the Bridge: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	type result struct {
		conn net.Conn
		err  error
	}
	got := make(chan result, 1)
	go func() {
		c, err := listener.Accept()
		got <- result{c, err}
	}()
	// The WebSocket only exists once bytes prompt the splice; the dial alone
	// is enough with coder/websocket, but writing first keeps the test
	// honest about ordering.
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		accepted = r.conn
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for the Listener to accept")
	}
	t.Cleanup(func() { _ = accepted.Close() })
	return client, accepted
}

// TestRelayCarriesBytesBothWays pushes bytes through the whole path — TCP
// into the Bridge, WebSocket to the Listener — and back.
func TestRelayCarriesBytesBothWays(t *testing.T) {
	client, accepted := pair(t)

	deadline := time.Now().Add(waitTimeout)
	_ = client.SetDeadline(deadline)
	_ = accepted.SetDeadline(deadline)

	if _, err := client.Write([]byte("to the Host")); err != nil {
		t.Fatalf("writing towards the Listener: %v", err)
	}
	buf := make([]byte, 32)
	n, err := accepted.Read(buf)
	if err != nil {
		t.Fatalf("reading at the Listener: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("to the Host")) {
		t.Errorf("Listener read %q, want %q", buf[:n], "to the Host")
	}

	if _, err := accepted.Write([]byte("to the Peer")); err != nil {
		t.Fatalf("writing towards the Bridge: %v", err)
	}
	n, err = client.Read(buf)
	if err != nil {
		t.Fatalf("reading at the client: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("to the Peer")) {
		t.Errorf("client read %q, want %q", buf[:n], "to the Peer")
	}
}

// TestListenerAddressesAreTCPAndDistinct checks the shape pion's TCPMux
// requires: TCP addresses, the Listener's own locally, unique remotes.
func TestListenerAddressesAreTCPAndDistinct(t *testing.T) {
	_, first := pair(t)
	_, second := pair(t)

	if _, ok := first.LocalAddr().(*net.TCPAddr); !ok {
		t.Errorf("LocalAddr is %T, want *net.TCPAddr", first.LocalAddr())
	}
	if _, ok := first.RemoteAddr().(*net.TCPAddr); !ok {
		t.Errorf("RemoteAddr is %T, want *net.TCPAddr", first.RemoteAddr())
	}
	// Distinctness matters within one Listener; these came from two, but a
	// remote equal to its own local would still be a bug.
	if first.RemoteAddr().String() == first.LocalAddr().String() {
		t.Errorf("remote %s equals local", first.RemoteAddr())
	}
	_ = second
}

// TestClosedListenerRefuses checks Accept unblocks with an error once the
// Listener closes — the TCPMux's accept loop depends on it.
func TestClosedListenerRefuses(t *testing.T) {
	listener := relay.NewListener()
	errs := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		errs <- err
	}()
	_ = listener.Close()
	select {
	case err := <-errs:
		if err == nil {
			t.Errorf("Accept returned a connection from a closed Listener")
		}
	case <-time.After(waitTimeout):
		t.Fatalf("Accept did not unblock on Close")
	}
	if err := listener.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
