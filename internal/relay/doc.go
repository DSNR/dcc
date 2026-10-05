// Package relay is the fallback path for a Session whose direct routes are
// blocked: ICE-TCP spliced through WebSockets at the Rendezvous's /v1/relay.
// The Host feeds a Listener of WebSocket-backed connections to pion's TCPMux;
// the Peer runs a loopback Bridge that turns the ICE agent's TCP dials into
// WebSockets to the same endpoint. Everything that crosses is DTLS
// ciphertext under keys the Noise handshake authenticated end to end, so the
// Rendezvous relays bytes it can never read.
package relay
