package media

// ScreenSize exposes the sizing a display goes through on its way to the
// encoder. It is not part of the boundary — a caller shares a screen and gets
// whatever size the display turned out to be — but it is the one decision in
// the screen path that a test can pin down without a display in front of it.
func ScreenSize(width, height int) (int, int) { return screenSize(width, height) }
