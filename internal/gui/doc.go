// Package gui is dcc's desktop client, minus the pixels: everything the chat
// window knows and everything a participant can do to it, over the session
// package's one API — with no networking, no crypto and no protocol knowledge
// of its own. That is the same relationship to the core that internal/cli
// has, so the two clients cannot drift apart on anything that matters.
//
// Model is the client; Screen is one snapshot of what should be on display.
// internal/chatwindow paints a Screen in Gio and calls Model's methods, and
// nothing else — so what a participant sees and what a click does are
// testable here without a display, and without the cgo and display headers a
// windowing toolkit needs on Linux.
package gui
