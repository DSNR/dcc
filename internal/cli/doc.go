// Package cli is dcc's terminal interface: a Bubble Tea Model over the
// session package's one API, with no networking, no crypto and no protocol
// knowledge of its own. Keystrokes and Session events go in, one rendered
// frame comes out — so what a participant sees is testable without a
// Rendezvous, a handshake or a PeerConnection underneath it.
package cli
