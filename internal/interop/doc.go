// Package interop is where dcc's two clients meet. Its tests put a window on
// one end of a real Session and a window or a terminal on the other, over a
// loopback Rendezvous with a real handshake, a real PeerConnection and a real
// DataChannel underneath — which is the only way to show that the claim both
// clients make, that neither has any networking of its own, actually holds.
//
// It has no code of its own, and nothing imports it. The clients' own tests
// stay where they belong: internal/cli and internal/gui are tested against
// fakes, quickly, without either of them dragging the other's dependencies
// into its test binary.
package interop
