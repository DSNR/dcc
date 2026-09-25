// Package media is the Call pipeline: capturing the microphone, encoding it
// for the wire, and playing back what arrives. Capture and playback sit
// behind the Devices boundary, so a Call can be driven end to end by a fake
// microphone in a test with no sound card anywhere near it.
//
// Video and screen capture arrive with the work that gives them something to
// do; the boundary is shaped to take them.
package media
