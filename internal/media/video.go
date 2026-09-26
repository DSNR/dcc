package media

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"time"
)

// The shape of dcc's video. One stream at a conservative size and frame rate:
// enough to see a face on, cheap enough that a pure-Go encoder keeps up on
// one core's worth of work, and small enough to survive the relay.
const (
	// VideoWidth and VideoHeight are what the fake camera produces and what
	// a driver aims for. A real camera that will only do something else is
	// taken at its word — the encoder is built around whatever it gives, and
	// VP8 carries the frame size to the other side itself.
	VideoWidth  = 640
	VideoHeight = 480
	// VideoFPS is the frame rate the pipeline paces capture at.
	VideoFPS = 15
	// VideoFrameDuration is how much time one frame stands for.
	VideoFrameDuration = time.Second / VideoFPS
	// VideoBitrateKbps is the encoder's CBR target.
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

// VideoOptions configures one Call's video.
type VideoOptions struct {
	// Devices opens the camera. Nil means the real one.
	Devices Devices
	// Send takes one encoded frame, on the capture goroutine, once every
	// VideoFrameDuration the camera is on for. Required.
	Send func(frame []byte, d time.Duration)
	// Frame takes one decoded frame of the other side's video, on the
	// goroutine that delivered it. The image is the caller's to keep.
	// Required.
	Frame func(img *image.RGBA)
	// NeedKeyframe asks the other side to send a keyframe, because nothing
	// arriving here can be decoded without one. Nil means never asking,
	// which leaves a stream that lost its reference frames black.
	NeedKeyframe func()
	// Stopped reports the camera stopping on its own — unplugged, or a driver
	// that gave up — as opposed to being turned off. Whoever is telling the
	// other side what this side is sending needs to know: a camera that has
	// gone is off, and saying otherwise leaves them watching a frozen frame.
	Stopped func()
}

// Video is one Call's video: the camera on the way out, the other side's
// frames on the way in. It is created when a Call goes Active with the camera
// off — a Call that never turns one on never opens the device — and closed
// when the Call ends.
type Video struct {
	send    func([]byte, time.Duration)
	frame   func(*image.RGBA)
	need    func()
	stopped func()

	// devices is the boundary the camera is opened through.
	devices Devices

	mu sync.Mutex
	// closed and gone say the same thing, under different guards: the state
	// machine reads closed under mu, and Play reads gone without it, because
	// the receiving side must never wait behind a camera being turned off.
	closed bool
	gone   atomic.Bool
	// cam and enc are the running capture session, both nil when the camera
	// is off. done closes when the capture goroutine has stopped, so
	// Camera(false) and Close can promise no Send outlives them.
	cam  Camera
	enc  *encoder
	done chan struct{}

	// The receiving side has its own lock: decoding a frame takes
	// milliseconds, and the camera must not wait behind it.
	decMu sync.Mutex
	dec   *decoder
	asked time.Time

	// force asks the encoder for a keyframe on its next frame — the answer
	// to the other side's PLI. It is atomic rather than held under v.mu
	// because the capture goroutine must never wait on that lock: turning
	// the camera off holds it while waiting for the goroutine to finish.
	force atomic.Bool
}

// StartVideo builds a Call's video pipeline. It touches no device: a camera
// is opened by Camera(true) and closed the moment it is turned off, so the
// light beside it means what it says.
func StartVideo(opts VideoOptions) (*Video, error) {
	if opts.Send == nil || opts.Frame == nil {
		return nil, errors.New("media: video needs somewhere to send and show frames")
	}
	devices := opts.Devices
	if devices == nil {
		devices = System()
	}
	return &Video{
		send:    opts.Send,
		frame:   opts.Frame,
		need:    opts.NeedKeyframe,
		stopped: opts.Stopped,
		devices: devices,
	}, nil
}

// Camera turns this side's camera on or off. Turning it on opens the device
// and starts sending; turning it off releases the device entirely, which is
// the only camera-off a participant has any reason to trust. Asking for the
// state it is already in does nothing.
func (v *Video) Camera(on bool) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return errors.New("media: the Call's video is closed")
	}
	if on == (v.cam != nil) {
		v.mu.Unlock()
		return nil
	}
	if !on {
		v.stopCameraLocked()
		v.mu.Unlock()
		return nil
	}
	devices := v.devices
	v.mu.Unlock()

	// Opening the device happens off the lock: a camera takes a moment to
	// come alive, and the received stream must keep flowing meanwhile.
	cam, err := devices.Camera()
	if err != nil {
		return fmt.Errorf("media: opening the camera: %w", err)
	}
	first, err := cam.Read()
	if err != nil {
		_ = cam.Close()
		return fmt.Errorf("media: reading from the camera: %w", err)
	}
	enc, err := newEncoder(first.Width, first.Height)
	if err != nil {
		_ = cam.Close()
		return err
	}

	v.mu.Lock()
	if v.closed || v.cam != nil {
		// Closed, or turned on twice at once, while the device was opening.
		closed := v.closed
		v.mu.Unlock()
		_ = cam.Close()
		_ = enc.close()
		if closed {
			return errors.New("media: the Call's video is closed")
		}
		return nil
	}
	v.cam, v.enc = cam, enc
	v.done = make(chan struct{})
	// A keyframe asked for while there was no camera to answer with is not
	// owed on this one's first frame: every stream starts with a keyframe.
	v.force.Store(false)
	go v.capture(cam, enc, first, v.done)
	v.mu.Unlock()
	return nil
}

// On reports whether this side's camera is open and being sent. A camera that
// died under the pipeline reads as off shortly afterwards: it releases itself
// the way turning it off would.
func (v *Video) On() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cam != nil
}

// ForceKeyframe answers the other side asking for one: the next frame this
// side encodes is a keyframe, whatever the encoder had planned. It is the one
// thing that gets a receiver who joined late, or lost packets, a picture
// again.
func (v *Video) ForceKeyframe() {
	v.force.Store(true)
}

// Play decodes one received frame and hands the picture over. A frame that
// cannot be decoded — the stream's keyframe was lost, or never arrived — asks
// the other side for a keyframe rather than dropping the stream.
func (v *Video) Play(frame []byte) {
	if len(frame) == 0 || v.gone.Load() {
		return
	}

	v.decMu.Lock()
	if v.dec == nil {
		dec, err := newDecoder()
		if err != nil {
			v.decMu.Unlock()
			return
		}
		v.dec = dec
	}
	img, err := v.dec.decode(frame)
	ask := err != nil && time.Since(v.asked) > keyframeWait
	if ask {
		v.asked = time.Now()
	}
	v.decMu.Unlock()

	if img != nil {
		v.frame(img)
	}
	if ask && v.need != nil {
		v.need()
	}
}

// Close turns the camera off and releases the decoder. It waits for the
// capture goroutine, so no Send callback runs after it returns. Closing twice
// is fine.
func (v *Video) Close() error {
	v.gone.Store(true)
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	v.stopCameraLocked()
	v.mu.Unlock()

	v.decMu.Lock()
	defer v.decMu.Unlock()
	if v.dec != nil {
		_ = v.dec.close()
		v.dec = nil
	}
	return nil
}

// cameraStopped tidies up after a camera that stopped by itself: the device is
// released like any other camera-off, and whoever announces this side's
// streams is told. A camera that was turned off deliberately has already been
// cleared by then, so this finds nothing to do.
func (v *Video) cameraStopped() {
	v.mu.Lock()
	if v.cam == nil {
		v.mu.Unlock()
		return
	}
	v.stopCameraLocked()
	v.mu.Unlock()
	if v.stopped != nil {
		v.stopped()
	}
}

// stopCameraLocked releases the device and waits for the capture goroutine to
// notice. Closing the Camera is what unblocks a Read that is waiting for the
// next frame, so a camera that has stopped delivering cannot hold a Call's
// video hostage. The capture goroutine takes v.mu only between frames, so
// waiting for it under the lock is safe.
func (v *Video) stopCameraLocked() {
	if v.cam == nil {
		return
	}
	_ = v.cam.Close()
	<-v.done
	v.cam, v.enc = nil, nil
	v.done = nil
}

// capture is the sending side's clock: the camera delivers a frame, the
// encoder turns it into a VP8 frame, and it goes out. The camera and the
// encoder belong to this goroutine for its lifetime, which is why they are
// passed in rather than read back off the Video.
func (v *Video) capture(cam Camera, enc *encoder, first Picture, done chan<- struct{}) {
	defer func() {
		_ = enc.close()
		// Whatever ended the loop, the camera is no longer being sent. If it
		// was turned off, this finds nothing left to do; if the device died
		// under us, it releases it and says so. Off this goroutine, because
		// the lock it needs is the one a Camera(false) holds while waiting
		// for this goroutine to finish.
		go v.cameraStopped()
		close(done)
	}()
	// The frame that sized the encoder is the first one sent — the device is
	// already running, and throwing it away would show a black frame.
	pic, err := first, error(nil)
	for {
		frame, encodeErr := enc.encode(pic, v.force.Swap(false))
		if encodeErr != nil {
			// The encoder has given up on this Call's camera. The Call
			// carries on without it, as it does without a microphone.
			return
		}
		if len(frame) > 0 {
			v.send(frame, VideoFrameDuration)
		}
		if pic, err = cam.Read(); err != nil {
			// The camera is gone. Ending the Call over it would be worse.
			return
		}
	}
}
