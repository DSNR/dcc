package signaling_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/signaling"
	"github.com/DSNR/dcc/internal/wire"
)

// TestServerCapsUnauthenticatedConnections parks the maximum number of
// connections in the handshake — connected, saying nothing — and expects the
// next one to be refused at the door.
func TestServerCapsUnauthenticatedConnections(t *testing.T) {
	id, reset, err := identity.Load(t.TempDir())
	if err != nil || reset != nil {
		t.Fatalf("identity.Load: %v (reset %v)", err, reset)
	}
	password, err := rendezvous.NewPassword()
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	hello := wire.HostHello{
		SessionID: uuid.Must(uuid.NewV7()).String(),
		Name:      "Alice",
		DTLS:      strings.Repeat("2f", 32),
	}
	server := signaling.NewServer(id, password, hello, func(c *signaling.Conn, _ wire.PeerHello) {
		c.Close()
	})
	ts := httptest.NewServer(server)
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	for i := range signaling.MaxUnauthenticated {
		ws, _, err := websocket.Dial(t.Context(), url, nil)
		if err != nil {
			t.Fatalf("stalled connection %d: %v", i+1, err)
		}
		defer ws.CloseNow()
	}

	if ws, _, err := websocket.Dial(t.Context(), url, nil); err == nil {
		ws.CloseNow()
		t.Fatalf("connection %d was let in past the cap of %d",
			signaling.MaxUnauthenticated+1, signaling.MaxUnauthenticated)
	}
}
