package media_test

import (
	"errors"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
)

// seen collects what a video pipeline produced: the encoded frames it sent,
// and the pictures it decoded.
type seen struct {
	mu     sync.Mutex
	frames [][]byte
	shown  []*image.RGBA
	wanted int
}

func (s *seen) take(frame []byte, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, append([]byte(nil), frame...))
}

func (s *seen) show(img *image.RGBA) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = append(s.shown, img)
}

func (s *seen) needKeyframe() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wanted++
}

func (s *seen) sent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *seen) pictures() []*image.RGBA {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*image.RGBA(nil), s.shown...)
}

func (s *seen) asked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wanted
}

func (s *seen) encoded() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.frames...)
}

// waitVideo waits for the capture loop to produce n frames.
func waitVideo(t *testing.T, s *seen, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for s.sent() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d frames after waiting; wanted %d", s.sent(), n)
		}
		time.Sleep(media.VideoFrameDuration)
	}
}

// TestVideoCameraOnAndOff proves the camera is only running while it is on:
// nothing is sent before, frames flow while it is, and they stop when it goes
// off — which is also when the device is released.
func TestVideoCameraOnAndOff(t *testing.T) {
	var got seen
	video, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{}, Send: got.take, Frame: got.show,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer video.Close()

	if video.On() {
		t.Fatal("the camera is on before anyone turned it on")
	}
	time.Sleep(3 * media.VideoFrameDuration)
	if got.sent() != 0 {
		t.Fatalf("%d frames were sent with the camera off", got.sent())
	}

	if err := video.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	if !video.On() {
		t.Fatal("the camera is off after being turned on")
	}
	waitVideo(t, &got, 3)

	if err := video.Camera(false); err != nil {
		t.Fatalf("Camera(false): %v", err)
	}
	if video.On() {
		t.Fatal("the camera is on after being turned off")
	}
	quiet := got.sent()
	time.Sleep(5 * media.VideoFrameDuration)
	if got.sent() != quiet {
		t.Fatalf("a camera that is off sent %d more frames", got.sent()-quiet)
	}

	// And back on again, since a Call is allowed more than one mind-change.
	if err := video.Camera(true); err != nil {
		t.Fatalf("Camera(true) again: %v", err)
	}
	waitVideo(t, &got, quiet+2)
}

// TestVideoRoundTrip proves what one side's camera sends is what the other
// side sees: the pictures that come out the far end are mostly the tint the
// fake camera painted, which is a thing no other tint could produce.
func TestVideoRoundTrip(t *testing.T) {
	tint := color.RGBA{R: 200, G: 40, B: 40, A: 0xFF}
	var sender, receiver seen
	out, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{Tint: tint}, Send: sender.take, Frame: sender.show,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer out.Close()
	in, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{}, Send: receiver.take, Frame: receiver.show,
		NeedKeyframe: receiver.needKeyframe,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer in.Close()

	if err := out.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	waitVideo(t, &sender, 4)
	for _, frame := range sender.encoded() {
		in.Play(frame)
	}

	shown := receiver.pictures()
	if len(shown) == 0 {
		t.Fatal("nothing was decoded from a camera that sent frames")
	}
	first := shown[0]
	if first.Bounds().Dx() != media.VideoWidth || first.Bounds().Dy() != media.VideoHeight {
		t.Fatalf("decoded a %v picture, want %dx%d", first.Bounds(), media.VideoWidth, media.VideoHeight)
	}
	if share := media.Tinted(first, tint); share < 0.7 {
		t.Fatalf("only %.0f%% of the picture is the tint that was sent", share*100)
	}
	if share := media.Tinted(first, media.TestPatternTint); share > 0.1 {
		t.Fatalf("%.0f%% of the picture is this side's own tint", share*100)
	}
	if got := receiver.asked(); got != 0 {
		t.Fatalf("a stream that started with a keyframe was asked for %d", got)
	}
}

// TestVideoAsksForAKeyframe proves a receiver handed frames that reference
// pictures it never saw asks for a keyframe rather than showing nothing
// forever — and asks once, not fifteen times a second.
func TestVideoAsksForAKeyframe(t *testing.T) {
	var sender, receiver seen
	out, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{}, Send: sender.take, Frame: sender.show,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer out.Close()
	in, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{}, Send: receiver.take, Frame: receiver.show,
		NeedKeyframe: receiver.needKeyframe,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer in.Close()

	if err := out.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	waitVideo(t, &sender, 4)
	// Everything but the keyframe the stream opened with.
	for _, frame := range sender.encoded()[1:] {
		in.Play(frame)
	}
	if got := receiver.asked(); got != 1 {
		t.Fatalf("asked for %d keyframes, want exactly 1", got)
	}
	if shown := receiver.pictures(); len(shown) != 0 {
		t.Fatalf("decoded %d pictures with no keyframe to decode against", len(shown))
	}

	// The answer to that ask is a keyframe, whatever the encoder had planned.
	out.ForceKeyframe()
	before := sender.sent()
	waitVideo(t, &sender, before+2)
	for _, frame := range sender.encoded()[before:] {
		in.Play(frame)
	}
	if shown := receiver.pictures(); len(shown) == 0 {
		t.Fatal("a forced keyframe did not get the receiver a picture")
	}
}

// TestVideoCloseStopsSending proves Close waits for the capture goroutine, so
// a Call that has hung up cannot still be writing to a torn-down track.
func TestVideoCloseStopsSending(t *testing.T) {
	var got seen
	video, err := media.StartVideo(media.VideoOptions{
		Devices: &media.Fake{}, Send: got.take, Frame: got.show,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	if err := video.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	waitVideo(t, &got, 2)
	if err := video.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after := got.sent()
	time.Sleep(5 * media.VideoFrameDuration)
	if got.sent() != after {
		t.Fatalf("a closed pipeline sent %d more frames", got.sent()-after)
	}
	if err := video.Close(); err != nil {
		t.Fatalf("Close twice: %v", err)
	}
	if err := video.Camera(true); err == nil {
		t.Fatal("a closed pipeline opened a camera")
	}
}

// blindDevices has no camera, the way Windows and a headless machine do not.
type blindDevices struct{}

func (blindDevices) Capture() (media.Source, error) { return nil, media.ErrNoDevices }
func (blindDevices) Playback() (media.Sink, error)  { return nil, media.ErrNoDevices }
func (blindDevices) Camera() (media.Camera, error)  { return nil, media.ErrNoCamera }

// TestVideoWithoutACamera proves a machine with no camera still runs a Call's
// video — it simply cannot send any, and is told so rather than pretending.
func TestVideoWithoutACamera(t *testing.T) {
	var got seen
	video, err := media.StartVideo(media.VideoOptions{
		Devices: blindDevices{}, Send: got.take, Frame: got.show,
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer video.Close()
	if err := video.Camera(true); err == nil {
		t.Fatal("a machine with no camera turned one on")
	}
	if video.On() {
		t.Fatal("a camera that would not open reads as on")
	}
}

// dyingCamera delivers a few frames and then gives up, the way an unplugged
// webcam does.
type dyingCamera struct {
	mu    sync.Mutex
	left  int
	shape media.Picture
}

func (c *dyingCamera) Read() (media.Picture, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		return media.Picture{}, errors.New("the camera was unplugged")
	}
	c.left--
	time.Sleep(media.VideoFrameDuration)
	return c.shape, nil
}

func (c *dyingCamera) Close() error { return nil }

// dyingDevices hands out one camera that dies after a few frames.
type dyingDevices struct{ camera *dyingCamera }

func (dyingDevices) Capture() (media.Source, error) { return nil, media.ErrNoDevices }
func (dyingDevices) Playback() (media.Sink, error)  { return nil, media.ErrNoDevices }
func (d dyingDevices) Camera() (media.Camera, error) {
	return d.camera, nil
}

// TestVideoCameraThatDiesSaysSo proves a camera that goes away mid-Call is
// treated as off rather than left looking live: the device is released and
// whoever tells the other side what this side is sending is told.
func TestVideoCameraThatDiesSaysSo(t *testing.T) {
	var got seen
	stopped := make(chan struct{}, 1)
	devices := dyingDevices{camera: &dyingCamera{left: 3, shape: media.NewPicture(media.VideoWidth, media.VideoHeight)}}
	video, err := media.StartVideo(media.VideoOptions{
		Devices: devices, Send: got.take, Frame: got.show,
		Stopped: func() { stopped <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	defer video.Close()

	if err := video.Camera(true); err != nil {
		t.Fatalf("Camera(true): %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("a camera that died was never reported as stopped")
	}
	if video.On() {
		t.Error("a camera that died still reads as on")
	}
}
