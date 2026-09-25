package signaling

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"

	"github.com/coder/websocket"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/wire"
)

// ErrRendezvousGone marks a dial that failed because nothing is answering
// where the Rendezvous used to be — the connection was refused, the hostname
// no longer resolves, or something that is not a Rendezvous answered. A
// reconnecting Peer stops retrying on this: no amount of patience brings a
// dead Quick Tunnel back.
var ErrRendezvousGone = errors.New("signaling: the Rendezvous is gone")

// DialOptions is what the Peer brings to a handshake.
type DialOptions struct {
	// Identity is this side's persistent keypair — the Noise static key.
	Identity identity.Identity
	// Password is the Invite's secret, mixed in as the PSK and never sent.
	Password rendezvous.Password
	// Hello is this side's message 3 payload. SessionID stays empty on a
	// fresh join; set, it is the claim to be resuming that Session.
	Hello wire.PeerHello
}

// Dial connects to a Rendezvous's Signaling WebSocket and runs the Noise
// handshake as the initiator. It returns the authenticated connection and
// the Host's hello — the minted session_id, the Host's Display Name and its
// DTLS fingerprint.
//
// The whole handshake lives inside HandshakeTimeout, on top of whatever
// deadline ctx already carries. A wrong Password surfaces as the Host
// closing the connection without a word, because that is all the Host does.
func Dial(ctx context.Context, url string, opts DialOptions) (*Conn, wire.HostHello, error) {
	ctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()

	ws, resp, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		if rendezvousGone(resp, err) {
			return nil, wire.HostHello{}, fmt.Errorf("dialing %s: %w: %w", url, ErrRendezvousGone, err)
		}
		return nil, wire.HostHello{}, fmt.Errorf("signaling: dialing %s: %w", url, err)
	}
	ws.SetReadLimit(readLimit)

	conn, hello, err := initiate(ctx, ws, opts)
	if err != nil {
		_ = ws.CloseNow()
		return nil, wire.HostHello{}, err
	}
	return conn, hello, nil
}

// rendezvousGone distinguishes a Rendezvous that is provably not there —
// the dial was answered, just not with a WebSocket upgrade, or refused
// outright — from a path that is merely dark, which a reconnecting Peer
// should keep trying through.
func rendezvousGone(resp *http.Response, err error) bool {
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		// Something answered, and it was not a Rendezvous. A dead Quick
		// Tunnel looks like this: Cloudflare's edge serves an error page.
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

// initiate runs the initiator's side of the handshake over an open WebSocket.
func initiate(ctx context.Context, ws *websocket.Conn, opts DialOptions) (*Conn, wire.HostHello, error) {
	hs, err := newHandshakeState(opts.Identity, opts.Password, true)
	if err != nil {
		return nil, wire.HostHello{}, err
	}

	// Message 1: psk, e. Its empty payload still carries an AEAD tag under a
	// key the Password is mixed into, which is what lets the Host refuse a
	// wrong Password before revealing anything.
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: writing handshake message 1: %w", err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, msg1); err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: sending handshake message 1: %w", err)
	}

	// Message 2: the Host's ephemeral and static keys, and its hello.
	_, msg2, err := ws.Read(ctx)
	if err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: waiting for handshake message 2: %w", err)
	}
	payload, _, _, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: reading handshake message 2: %w", err)
	}
	hostHello, err := wire.DecodeHostHello(payload)
	if err != nil {
		return nil, wire.HostHello{}, err
	}

	// Message 3: our static key and our hello; the transport keys fall out.
	peerPayload, err := wire.EncodePeerHello(opts.Hello)
	if err != nil {
		return nil, wire.HostHello{}, err
	}
	msg3, sendCS, recvCS, err := hs.WriteMessage(nil, peerPayload)
	if err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: writing handshake message 3: %w", err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, msg3); err != nil {
		return nil, wire.HostHello{}, fmt.Errorf("signaling: sending handshake message 3: %w", err)
	}

	host, err := peerKey(hs)
	if err != nil {
		return nil, wire.HostHello{}, err
	}
	return newConn(ws, sendCS, recvCS, host), hostHello, nil
}
