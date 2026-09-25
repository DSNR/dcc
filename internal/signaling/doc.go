// Package signaling runs the Noise handshake and the SDP/ICE exchange over
// the Rendezvous WebSocket. The fallback relay rides the same Rendezvous on
// its own WebSockets; package relay owns that path.
package signaling
