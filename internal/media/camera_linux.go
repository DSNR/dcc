package media

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/blackjack/webcam"
)

// The camera on Linux is V4L2 through blackjack/webcam — ioctls and mmap, no
// cgo, so the Linux build stays a plain cross-compile like everything else.
const (
	// cameraGlob is where V4L2 devices appear. The first one that streams a
	// format dcc understands is the camera; there is no picker in MVP.
	cameraGlob = "/dev/video*"
	// cameraBuffers is how many mmap buffers the driver keeps. Enough to
	// absorb a slow encode, few enough that a frame is never old.
	cameraBuffers = 4
	// cameraWait bounds one wait for a frame, in milliseconds. It is short
	// so that closing the camera is noticed promptly: a Read that is waiting
	// on a device nobody is watching should end, not linger.
	cameraWait = 200
)

// The pixel formats dcc accepts from a camera, in the order it asks for them.
// YUYV is what practically every webcam offers at this size; YU12 is I420
// itself, which needs no conversion at all.
const (
	pixelFormatYUYV webcam.PixelFormat = 0x56595559 // 'YUYV'
	pixelFormatYU12 webcam.PixelFormat = 0x32315559 // 'YU12'
)

// Camera opens the first V4L2 device that will stream a format dcc
// understands at the size it wants.
func (linuxDevices) Camera() (Camera, error) {
	paths, err := filepath.Glob(cameraGlob)
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("media: no camera at %s: %w", cameraGlob, ErrNoCamera)
	}
	sort.Strings(paths)
	var failures []error
	for _, path := range paths {
		cam, err := openCamera(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		return cam, nil
	}
	return nil, fmt.Errorf("media: no camera would stream video: %w", errors.Join(failures...))
}

// openCamera starts one device streaming, or says why it would not.
func openCamera(path string) (*v4l2Camera, error) {
	device, err := webcam.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	supported := device.GetSupportedFormats()
	for _, format := range []webcam.PixelFormat{pixelFormatYUYV, pixelFormatYU12} {
		if _, ok := supported[format]; !ok {
			continue
		}
		// The driver answers with the size it will actually give, which may
		// not be the one asked for. Whatever it is becomes the Call's frame
		// size: VP8 carries it, and the other side follows.
		chosen, width, height, err := device.SetImageFormat(format, VideoWidth, VideoHeight)
		if err != nil || chosen != format {
			continue
		}
		// A framerate the device will not do is not fatal — the pipeline is
		// paced by the camera either way.
		_ = device.SetFramerate(VideoFPS)
		if err := device.SetBufferCount(cameraBuffers); err != nil {
			continue
		}
		if err := device.StartStreaming(); err != nil {
			continue
		}
		return &v4l2Camera{
			device: device,
			format: format,
			pic:    NewPicture(int(width), int(height)),
		}, nil
	}
	_ = device.Close()
	return nil, fmt.Errorf("%s: no format dcc understands: %w", path, ErrNoCamera)
}

// v4l2Camera is a streaming V4L2 device behind the Camera boundary.
type v4l2Camera struct {
	device *webcam.Webcam
	format webcam.PixelFormat
	// pic is the I420 frame every capture is converted into, reused across
	// frames — which is why the boundary says a Picture is only good until
	// the next Read.
	pic Picture

	mu     sync.Mutex
	closed bool
}

// Read implements Camera, waiting for the device's next frame. A wait that
// times out is not an error: cameras drop frames, and the next one is along.
// Waiting in bounded steps is also what lets Close in — the lock is released
// between them, and the device is only ever touched under it.
func (c *v4l2Camera) Read() (Picture, error) {
	for {
		pic, ok, err := c.step()
		if err != nil {
			return Picture{}, err
		}
		if ok {
			return pic, nil
		}
	}
}

// step waits up to cameraWait for one frame, reporting no frame rather than an
// error when the device simply had none ready.
func (c *v4l2Camera) step() (Picture, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Picture{}, false, errors.New("media: the camera is closed")
	}
	if err := c.device.WaitForFrame(cameraWait); err != nil {
		var timeout *webcam.Timeout
		if errors.As(err, &timeout) {
			return Picture{}, false, nil
		}
		return Picture{}, false, fmt.Errorf("media: waiting for a camera frame: %w", err)
	}
	frame, err := c.device.ReadFrame()
	if err != nil {
		return Picture{}, false, fmt.Errorf("media: reading a camera frame: %w", err)
	}
	if len(frame) == 0 {
		return Picture{}, false, nil
	}
	if err := c.convert(frame); err != nil {
		return Picture{}, false, err
	}
	return c.pic, true, nil
}

// convert turns one captured frame into the I420 the encoder wants. A device
// already delivering I420 is copied rather than aliased: the driver reuses
// its mmap buffers, and the encoder reads the frame after ReadFrame has
// handed the buffer back.
func (c *v4l2Camera) convert(frame []byte) error {
	if c.format == pixelFormatYUYV {
		return YUYVToI420(frame, c.pic)
	}
	want := len(c.pic.Y) + len(c.pic.U) + len(c.pic.V)
	if len(frame) < want {
		return fmt.Errorf("media: a %dx%d I420 frame needs %d bytes, got %d",
			c.pic.Width, c.pic.Height, want, len(frame))
	}
	copy(c.pic.Y, frame)
	copy(c.pic.U, frame[len(c.pic.Y):])
	copy(c.pic.V, frame[len(c.pic.Y)+len(c.pic.U):])
	return nil
}

// Close implements Camera, releasing the device. A Read waiting for a frame
// notices within cameraWait, which is also how long Close may have to wait
// for its turn at the device. Closing twice is fine.
func (c *v4l2Camera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	_ = c.device.StopStreaming()
	return c.device.Close()
}
