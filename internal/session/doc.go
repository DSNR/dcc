// Package session owns the Session state machine: commands go in, events
// come out, and both UIs are thin consumers of that one API. It holds the
// lifecycle (Idle, Hosting, Connecting, Verifying, Connected, Reconnecting,
// Disconnected, Failed) and drives the packages below it.
package session
