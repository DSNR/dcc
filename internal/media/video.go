package media

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"time"
)

// The shape of dcc's camera video. One stream at a conservative size and frame
// rate: enough to see a face on, cheap enough that a pure-Go encoder keeps up
// on one core's worth of work, and small enough to survive the relay. A shared
// screen is a different shape entirely — see screen.go.
const (
	// VideoWidth and VideoHeight are what the fake camera produces and what
	// a driver aims for. A real camera that will only do something else is
	// taken at its word — the encoder is built around whatever it gives, and
	// VP8 carries the frame size to the other side itself.
	VideoWidth  = 640
	VideoHeight = 480
	// VideoFPS is the frame rate the pipeline paces camera capture at.
	VideoFPS = 15
	// VideoFrameDuration is how much time one camera frame stands for.
	VideoFrameDuration = time.Second / VideoFPS
	// VideoBitrateKbps is the camera encoder's CBR target.
	VideoBitrateKbps = 600
)

// keyframeWait is how often the receiving side will ask for a keyframe. A
// stream that has lost its reference frames is unwatchable until one
// arrives, but asking on every undecodable frame would ask fifteen times a
// second.
const keyframeWait = time.Second

// Picture is one frame of video at the device boundary: 8-bit I420 — a full
// luma plane and two half-size chroma planes, each with its own stride, which
// is the shape both cameras and VP8 think in.
type Picture struct {
	// Width and Height are the visible size in pixels.
	Width, Height int
	// Y, U and V are the planes, each row YStride/UStride/VStride bytes
	// apart. U and V are (Width+1)/2 by (Height+1)/2.
	Y, U, V                   []byte
	YStride, UStride, VStride int
}

// Camera is a video source: successive frames, paced in real time the way a
// Source paces audio. The planes of a returned Picture belong to the Camera
// and stay valid only until the next Read or Close, so a caller that keeps a
// frame copies it.
type Camera interface {
	Read() (Picture, error)
	Close() error
}

// ErrNoCamera reports that this build has no camera driver for the platform
// it is running on — which on Windows is every build, until the send side
// lands there. A Call still carries the other side's video.
var ErrNoCamera = errors.New("media: no camera on this platform")

// VideoOptions configures one Call's video: the camera and the shared screen,
// which are two streams of the same shape and are configured the same way.
type VideoOptions struct {
	// Devices opens the camera and the screen. Nil means the real ones.
	Devices Devices
	// Camera and Screen configure the two streams. Both are required — a
	// Call always has both, even when nobody ever turns either on.
	Camera, Screen StreamOptions
}

// StreamOptions configures one of a Call's video streams, in both directions.
type StreamOptions struct {
	// Send takes one encoded frame, on the capture goroutine, once every
	// frame interval the stream is on for. Required.
	Send func(frame []byte, d time.Duration)
	// Frame takes one decoded frame of the other side's stream, on the
	// goroutine that delivered it. The image is the caller's to keep.
	// Required.
	Frame func(img *image.RGBA)
	// Preview takes one frame of this side's own stream, on the capture
	// goroutine, as the device delivered it and before anything is encoded —
	// which is what a local picture-in-picture paints. The Picture's planes
	// belong to the device and are overwritten by the next frame, so a
	// preview that keeps one converts it first: Picture.RGBA is that
	// conversion, and leaving it uncalled is what makes a preview nobody is
	// watching cost nothing but the call. Nil means no preview at all.
	Preview func(pic Picture)
	// NeedKeyframe asks the other side to send a keyframe, because nothing
	// arriving here can be decoded without one. Nil means never asking,
	// which leaves a stream that lost its reference frames black.
	NeedKeyframe func()
	// Stopped reports the device stopping on its own — a camera unplugged, a
	// display that went away — as opposed to being turned off. Whoever is
	// telling the other side what this side is sending needs to know: a
	// camera that has gone is off, and saying otherwise leaves them watching
	// a frozen frame.
	Stopped func()
}

// Video is one Call's video: this side's camera and shared screen on the way
// out, the other side's on the way in. It is created when a Call goes Active
// with both off — a Call that never turns either on never opens a device — and
// closed when the Call ends.
//
// The two streams are independent in every way that matters: each opens its
// own device, runs at its own frame rate, keeps its own encoder and decoder,
// and asks for and answers keyframes on its own. Neither ever waits behind the
// other.
type Video struct {
	camera *stream
	screen *stream
}

// StartVideo builds a Call's video pipeline. It touches no device: they are
// opened by Camera(true) and Screen(true) and closed the moment either is
// turned off, so the light beside a camera means what it says.
func StartVideo(opts VideoOptions) (*Video, error) {
	if opts.Camera.Send == nil || opts.Camera.Frame == nil ||
		opts.Screen.Send == nil || opts.Screen.Frame == nil {
		return nil, errors.New("media: video needs somewhere to send and show both streams")
	}
	devices := opts.Devices
	if devices == nil {
		devices = System()
	}
	return &Video{
		camera: newStream("camera", devices.Camera, VideoFPS, VideoBitrateKbps, opts.Camera),
		screen: newStream("screen", devices.Screen, ScreenFPS, ScreenBitrateKbps, opts.Screen),
	}, nil
}

// Camera turns this side's camera on or off. Turning it on opens the device
// and starts sending; turning it off releases the device entirely, which is
// the only camera-off a participant has any reason to trust. Asking for the
// state it is already in does nothing.
func (v *Video) Camera(on bool) error { return v.camera.set(on) }

// CameraOn reports whether this side's camera is open and being sent. A camera
// that died under the pipeline reads as off shortly afterwards: it releases
// itself the way turning it off would.
func (v *Video) CameraOn() bool { return v.camera.isOn() }

// PlayCamera decodes one received camera frame and hands the picture over.
func (v *Video) PlayCamera(frame []byte) { v.camera.play(frame) }

// ForceCameraKeyframe answers the other side asking for one on the camera
// stream: the next frame this side encodes is a keyframe, whatever the encoder
// had planned. It is the one thing that gets a receiver who joined late, or
// lost packets, a picture again.
func (v *Video) ForceCameraKeyframe() { v.camera.force.Store(true) }

// Screen starts or stops sharing this side's screen, on the same terms as the
// camera: starting opens the display, stopping releases it.
func (v *Video) Screen(on bool) error { return v.screen.set(on) }

// ScreenOn reports whether this side's screen is being shared.
func (v *Video) ScreenOn() bool { return v.screen.isOn() }

// PlayScreen decodes one received frame of the other side's shared screen.
func (v *Video) PlayScreen(frame []byte) { v.screen.play(frame) }

// ForceScreenKeyframe is ForceCameraKeyframe for the screen stream.
func (v *Video) ForceScreenKeyframe() { v.screen.force.Store(true) }

// Close turns both streams off and releases their decoders. It waits for their
// capture goroutines, so no Send callback runs after it returns. Closing twice
// is fine.
func (v *Video) Close() error {
	// Both, whatever the first one says: a screen left capturing because the
	// camera objected would outlive the Call.
	return errors.Join(v.camera.close(), v.screen.close())
}

// stream is one video stream, both ways: one device encoded and sent, and the
// other side's equivalent decoded and shown. The camera and the screen are the
// same machinery pointed at different devices, running at different rates and
// called different things when something goes wrong.
type stream struct {
	// name is what this stream is called in an error a participant reads.
	name string
	// open is the device boundary this stream's source is opened through.
	open func() (Camera, error)
	// fps and bitrate are the encoder's operating point, and fps also paces
	// what the wire is told each frame is worth.
	fps     int
	bitrate int

	send    func([]byte, time.Duration)
	frame   func(*image.RGBA)
	preview func(Picture)
	need    func()
	stopped func()

	mu sync.Mutex
	// closed and gone say the same thing, under different guards: the state
	// machine reads closed under mu, and play reads gone without it, because
	// the receiving side must never wait behind a device being turned off.
	closed bool
	gone   atomic.Bool
	// dev and enc are the running capture session, both nil when the stream
	// is off. done closes when the capture goroutine has stopped, so set(false)
	// and close can promise no Send outlives them.
	dev  Camera
	enc  *encoder
	done chan struct{}

	// The receiving side has its own lock: decoding a frame takes
	// milliseconds, and the device must not wait behind it.
	decMu sync.Mutex
	dec   *decoder
	asked time.Time

	// force asks the encoder for a keyframe on its next frame — the answer
	// to the other side's PLI. It is atomic rather than held under mu
	// because the capture goroutine must never wait on that lock: turning
	// the stream off holds it while waiting for the goroutine to finish.
	force atomic.Bool
}

// newStream builds one stream, off, with no device open.
func newStream(name string, open func() (Camera, error), fps, bitrate int, opts StreamOptions) *stream {
	return &stream{
		name:    name,
		open:    open,
		fps:     fps,
		bitrate: bitrate,
		send:    opts.Send,
		frame:   opts.Frame,
		preview: opts.Preview,
		need:    opts.NeedKeyframe,
		stopped: opts.Stopped,
	}
}

// frameDuration is how much time one of this stream's frames stands for.
func (s *stream) frameDuration() time.Duration {
	return time.Second / time.Duration(s.fps)
}

// set turns the stream on or off, opening or releasing its device. Asking for
// the state it is already in does nothing.
func (s *stream) set(on bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("media: the Call's video is closed")
	}
	if on == (s.dev != nil) {
		s.mu.Unlock()
		return nil
	}
	if !on {
		s.stopLocked()
		s.mu.Unlock()
		return nil
	}
	open := s.open
	s.mu.Unlock()

	// Opening the device happens off the lock: a camera takes a moment to
	// come alive, and the received stream must keep flowing meanwhile.
	dev, err := open()
	if err != nil {
		return fmt.Errorf("media: opening the %s: %w", s.name, err)
	}
	first, err := dev.Read()
	if err != nil {
		_ = dev.Close()
		return fmt.Errorf("media: reading from the %s: %w", s.name, err)
	}
	enc, err := newEncoder(first.Width, first.Height, s.fps, s.bitrate)
	if err != nil {
		_ = dev.Close()
		return err
	}

	s.mu.Lock()
	if s.closed || s.dev != nil {
		// Closed, or turned on twice at once, while the device was opening.
		closed := s.closed
		s.mu.Unlock()
		_ = dev.Close()
		_ = enc.close()
		if closed {
			return errors.New("media: the Call's video is closed")
		}
		return nil
	}
	s.dev, s.enc = dev, enc
	s.done = make(chan struct{})
	// A keyframe asked for while there was no device to answer with is not
	// owed on this one's first frame: every stream starts with a keyframe.
	s.force.Store(false)
	go s.capture(dev, enc, first, s.done)
	s.mu.Unlock()
	return nil
}

// isOn reports whether the stream's device is open and being sent.
func (s *stream) isOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev != nil
}

// play decodes one received frame and hands the picture over. A frame that
// cannot be decoded — the stream's keyframe was lost, or never arrived — asks
// the other side for a keyframe rather than dropping the stream.
func (s *stream) play(frame []byte) {
	if len(frame) == 0 || s.gone.Load() {
		return
	}

	s.decMu.Lock()
	if s.dec == nil {
		dec, err := newDecoder()
		if err != nil {
			s.decMu.Unlock()
			return
		}
		s.dec = dec
	}
	img, err := s.dec.decode(frame)
	ask := err != nil && time.Since(s.asked) > keyframeWait
	if ask {
		s.asked = time.Now()
	}
	s.decMu.Unlock()

	if img != nil {
		s.frame(img)
	}
	if ask && s.need != nil {
		s.need()
	}
}

// close turns the stream off and releases its decoder. It waits for the
// capture goroutine, so no Send callback runs after it returns.
func (s *stream) close() error {
	s.gone.Store(true)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.stopLocked()
	s.mu.Unlock()

	s.decMu.Lock()
	defer s.decMu.Unlock()
	if s.dec != nil {
		_ = s.dec.close()
		s.dec = nil
	}
	return nil
}

// deviceStopped tidies up after a device that stopped by itself: it is
// released like any other stream-off, and whoever announces this side's
// streams is told. A device that was turned off deliberately has already been
// cleared by then, so this finds nothing to do.
func (s *stream) deviceStopped() {
	s.mu.Lock()
	if s.dev == nil {
		s.mu.Unlock()
		return
	}
	s.stopLocked()
	s.mu.Unlock()
	if s.stopped != nil {
		s.stopped()
	}
}

// stopLocked releases the device and waits for the capture goroutine to
// notice. Closing the device is what unblocks a Read that is waiting for the
// next frame, so a camera that has stopped delivering cannot hold a Call's
// video hostage. The capture goroutine takes s.mu only between frames, so
// waiting for it under the lock is safe.
func (s *stream) stopLocked() {
	if s.dev == nil {
		return
	}
	_ = s.dev.Close()
	<-s.done
	s.dev, s.enc = nil, nil
	s.done = nil
}

// capture is the sending side's clock: the device delivers a frame, the
// encoder turns it into a VP8 frame, and it goes out. The device and the
// encoder belong to this goroutine for its lifetime, which is why they are
// passed in rather than read back off the stream.
func (s *stream) capture(dev Camera, enc *encoder, first Picture, done chan<- struct{}) {
	defer func() {
		_ = enc.close()
		// Whatever ended the loop, the device is no longer being sent. If it
		// was turned off, this finds nothing left to do; if it died under us,
		// it releases it and says so. Off this goroutine, because the lock it
		// needs is the one a set(false) holds while waiting for this
		// goroutine to finish.
		go s.deviceStopped()
		close(done)
	}()
	// The frame that sized the encoder is the first one sent — the device is
	// already running, and throwing it away would show a black frame.
	duration := s.frameDuration()
	pic, err := first, error(nil)
	for {
		// The preview goes first, so that turning a camera on shows this side
		// a picture of itself whether or not the encoder ever makes anything
		// of it.
		if s.preview != nil {
			s.preview(pic)
		}
		frame, encodeErr := enc.encode(pic, s.force.Swap(false))
		if encodeErr != nil {
			// The encoder has given up on this stream. The Call carries on
			// without it, as it does without a microphone.
			return
		}
		if len(frame) > 0 {
			s.send(frame, duration)
		}
		if pic, err = dev.Read(); err != nil {
			// The device is gone. Ending the Call over it would be worse.
			return
		}
	}
}
