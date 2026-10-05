package signaling

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/wire"
)

// readLimit bounds an incoming WebSocket message: the largest frame the wire
// allows, plus the Noise transport's authentication tag and a little slack.
const readLimit = wire.MaxFrameSize + 128

// pingWait bounds one ping round trip. A pong that takes longer than this
// on a path meant for Signaling is a path that is gone.
const pingWait = 10 * time.Second

// Conn is an authenticated Signaling connection: the Rendezvous WebSocket
// after the Noise handshake, carrying PathSignal wire frames under the
// handshake's transport keys. It pings every PingInterval and closes itself
// when a pong stops coming back.
type Conn struct {
	ws   *websocket.Conn
	peer identity.PublicKey

	sendMu sync.Mutex
	send   *noise.CipherState

	// recv is used by Recv only, which a Conn's owner calls from a single
	// goroutine; the Noise nonce sequence tolerates nothing else.
	recv *noise.CipherState

	stopPing context.CancelFunc
	closed   sync.Once
}

// newConn wraps a WebSocket whose handshake just completed and starts its
// ping loop.
func newConn(ws *websocket.Conn, send, recv *noise.CipherState, peer identity.PublicKey) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{ws: ws, peer: peer, send: send, recv: recv, stopPing: cancel}
	go c.ping(ctx)
	return c
}

// Peer reports the Identity the handshake authenticated on the other side.
func (c *Conn) Peer() identity.PublicKey { return c.peer }

// Send encrypts one frame and writes it as one binary WebSocket message.
func (c *Conn) Send(ctx context.Context, f wire.Frame) error {
	b, err := wire.Encode(f)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	ciphertext, err := c.send.Encrypt(nil, nil, b)
	if err != nil {
		return fmt.Errorf("signaling: encrypting a %s frame: %w", f.Type(), err)
	}
	return c.ws.Write(ctx, websocket.MessageBinary, ciphertext)
}

// Recv returns the next frame the other side sent. Frames whose disposition
// is ignore or drop are consumed here — the caller only ever sees frames it
// must act on. Any error means the connection is finished: a failed
// decryption or a fatally malformed frame closes it on the way out.
func (c *Conn) Recv(ctx context.Context) (wire.Frame, error) {
	for {
		_, ciphertext, err := c.ws.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("signaling: reading the WebSocket: %w", err)
		}
		b, err := c.recv.Decrypt(nil, nil, ciphertext)
		if err != nil {
			// A message the transport keys reject is not the other side
			// talking; nothing after it can be trusted either.
			c.Close()
			return nil, fmt.Errorf("signaling: decrypting a frame: %w", err)
		}
		f, err := wire.Decode(wire.PathSignal, b)
		if err != nil {
			switch wire.DispositionOf(err) {
			case wire.IgnoreFrame, wire.DropFrame:
				continue
			}
			c.Close()
			return nil, err
		}
		return f, nil
	}
}

// Close tears the connection down without ceremony and unblocks any pending
// Recv. Closing twice is fine.
func (c *Conn) Close() error {
	var err error
	c.closed.Do(func() {
		c.stopPing()
		err = c.ws.CloseNow()
	})
	return err
}

// ping keeps the WebSocket demonstrably alive, closing the connection the
// first time a pong fails to arrive so the owner's Recv reports it.
func (c *Conn) ping(ctx context.Context) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, pingWait)
			err := c.ws.Ping(pingCtx)
			cancel()
			if err != nil {
				c.Close()
				return
			}
		}
	}
}
