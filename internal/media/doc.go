// Package media is the Call pipeline: capturing the microphone and the camera,
// encoding both for the wire, and playing back what arrives. Every device sits
// behind the Devices boundary, so a Call can be driven end to end by a fake
// microphone and a test-pattern camera in a test with no sound card or webcam
// anywhere near it.
//
// Audio is G.711 µ-law at 8 kHz (ADR 0004); video is VP8 through govpx's
// pure-Go encoder (ADR 0002), which is why the pipeline paces itself at a
// conservative size and frame rate. The microphone stays open while muted, so
// unmuting is instant; the camera is released the moment it is turned off,
// because the light beside it should mean something.
//
// Screen capture arrives with the work that shares one; the boundary is shaped
// to take it.
package media
