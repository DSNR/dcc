// Package rendezvous manages the Cloudflare Quick Tunnel the Host exposes —
// launching cloudflared, scraping its URL, waiting for readiness and tearing
// it down — and produces and parses the Invite.
//
// Start hands back a Rendezvous whose Invite is good the moment it is
// returned: a Quick Tunnel logs its URL a second or so before it has an edge
// connection to serve it with, and an Invite handed over in that gap fails
// for the Peer in a way that looks exactly like a wrong Password. Nothing
// here retries a tunnel that dies — the Session owns that decision — but
// everything here reports why, because a Host whose Rendezvous will not open
// has only cloudflared's last line to go on.
//
// The Tunnel interface is the seam: publishing a local listener at a public
// address is the one thing in dcc that needs another process, a Cloudflare
// account's worth of luck and a working internet connection. Loopback
// implements it by publishing nothing at all, which is how the Session tests
// run a real Rendezvous — real listener, real Invite, real WebSocket — with
// none of that.
//
// The Password never leaves this machine. It lives in the Invite's fragment,
// which a browser does not send, and it reaches the wire only as the Noise
// pre-shared key mixed into a handshake, never as anything transmitted.
package rendezvous
