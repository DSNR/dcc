// Package media is the Call pipeline: capturing the microphone, the camera and
// the screen, encoding all three for the wire, and playing back what arrives.
// Every device sits behind the Devices boundary, so a Call can be driven end to
// end by a fake microphone and test-pattern camera and screen in a test with no
// sound card, webcam or display anywhere near it.
//
// Audio is G.722 at 16 kHz (ADR 0005); both video streams are VP8 through
// govpx's pure-Go encoder (ADR 0002), which is why the pipeline paces itself at
// a conservative size and frame rate. The microphone stays open while muted, so
// unmuting is instant; the camera and the screen are released the moment they
// are turned off, because the light beside a camera should mean something and
// nobody should have to wonder whether their desktop is still being watched.
package media
