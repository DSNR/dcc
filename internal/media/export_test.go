package media

// ScreenSize exposes the sizing a display goes through on its way to the
// encoder. It is not part of the boundary — a caller shares a screen and gets
// whatever size the display turned out to be — but it is the one decision in
// the screen path that a test can pin down without a display in front of it.
func ScreenSize(width, height int) (int, int) { return screenSize(width, height) }

// EncodeFrames exposes the sending side of a stream with no device in front of
// it: it builds the encoder a stream of the given shape would build, paints
// count frames through paint, and returns what came out. It is how a test puts
// a long run of inter frames through the codec — long enough for the encoder's
// still-macroblock heuristics to come into play — at a frame size no display
// this machine has needs to be.
func EncodeFrames(width, height, fps, bitrateKbps, count int, paint func(pic Picture, frame int)) ([][]byte, error) {
	enc, err := newEncoder(width, height, fps, bitrateKbps)
	if err != nil {
		return nil, err
	}
	defer func() { _ = enc.close() }()
	pic := NewPicture(width, height)
	frames := make([][]byte, 0, count)
	for i := range count {
		paint(pic, i)
		frame, err := enc.encode(pic, false)
		if err != nil {
			return frames, err
		}
		frames = append(frames, append([]byte(nil), frame...))
	}
	return frames, nil
}
