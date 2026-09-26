package media

import (
	"errors"
	"fmt"
	"image"

	"github.com/thesyncim/govpx"
)

// The video codec is VP8 — RTP payload type 96, 90 kHz clock, the codec every
// WebRTC stack has spoken since the beginning. ADR 0002 records why it is
// govpx's pure-Go encoder rather than libvpx behind cgo.
const (
	// PayloadTypeVP8 is the dynamic RTP payload type dcc negotiates VP8 on.
	PayloadTypeVP8 = 96
	// VideoClockRate is VP8's RTP clock rate.
	VideoClockRate = 90000
)

// The encoder's operating point, measured in prototype #16: four threads and
// a speed preset of -6 encode a frame in a few milliseconds, which is what
// makes a pure-Go VP8 sender viable at all.
const (
	videoThreads = 4
	videoCPUUsed = -6
	// videoKeyframeInterval caps how long a receiver that missed the last
	// keyframe waits for the next one even if it never asks.
	videoKeyframeInterval = 10 * VideoFPS
)

// encoder turns Pictures into VP8 frames. It is built around one camera's
// frame size and belongs to the capture goroutine that drives it.
type encoder struct {
	enc *govpx.VP8Encoder
	// buf is the output buffer every frame is encoded into, reused across
	// frames — fifteen frames a second is no place to be allocating. The
	// frame handed out aliases it until the next encode.
	buf []byte
	// frames counts encoded frames, which at a fixed frame rate is the
	// presentation timestamp in the encoder's own timebase.
	frames uint64
}

// newEncoder builds the encoder for a camera of the given size.
func newEncoder(width, height int) (*encoder, error) {
	enc, err := govpx.NewVP8Encoder(govpx.EncoderOptions{
		Width:             width,
		Height:            height,
		FPS:               VideoFPS,
		Threads:           videoThreads,
		Deadline:          govpx.DeadlineRealtime,
		CpuUsed:           videoCPUUsed,
		RateControlMode:   govpx.RateControlCBR,
		TargetBitrateKbps: VideoBitrateKbps,
		KeyFrameInterval:  videoKeyframeInterval,
		// A frame that references one that was lost shows as smeared
		// rubbish until the next keyframe; error resilience is what keeps a
		// lossy path watchable between them.
		ErrorResilient: true,
	})
	if err != nil {
		return nil, fmt.Errorf("media: building the VP8 encoder: %w", err)
	}
	// One frame's worth of output at the target bitrate is a few kilobytes;
	// a keyframe is far larger, and this is sized so neither has to grow it.
	return &encoder{enc: enc, buf: make([]byte, width*height)}, nil
}

// encode turns one Picture into a VP8 frame, forcing a keyframe when the
// other side has asked for one. The returned frame aliases the encoder's own
// buffer and is empty when rate control dropped the frame.
func (e *encoder) encode(pic Picture, force bool) ([]byte, error) {
	if force {
		e.enc.ForceKeyFrame()
	}
	result, err := e.enc.EncodeInto(e.buf, govpx.Image{
		Width:   pic.Width,
		Height:  pic.Height,
		Y:       pic.Y,
		U:       pic.U,
		V:       pic.V,
		YStride: pic.YStride,
		UStride: pic.UStride,
		VStride: pic.VStride,
	}, e.frames, 1, 0)
	if err != nil {
		return nil, fmt.Errorf("media: encoding a frame: %w", err)
	}
	e.frames++
	if result.Dropped {
		return nil, nil
	}
	return result.Data, nil
}

// close releases the encoder.
func (e *encoder) close() error { return e.enc.Close() }

// decoder turns received VP8 frames into pictures the UIs can paint.
type decoder struct {
	dec *govpx.VP8Decoder
}

// newDecoder builds the receiving side's decoder, which is created the first
// time a frame arrives — a Call where nobody turns a camera on never builds
// one.
func newDecoder() (*decoder, error) {
	dec, err := govpx.NewVP8Decoder(govpx.DecoderOptions{
		Threads: videoThreads,
		// Concealment is what a frame that arrived with holes in it looks
		// like when it is shown anyway, which beats showing nothing.
		ErrorConcealment: true,
	})
	if err != nil {
		return nil, fmt.Errorf("media: building the VP8 decoder: %w", err)
	}
	return &decoder{dec: dec}, nil
}

// decode turns one VP8 frame into RGBA. A frame that arrives before the
// stream's first keyframe — or after the one it referenced was lost — cannot
// be decoded, and the error is the caller's cue to ask for a keyframe. A
// frame the codec swallowed without producing a picture is neither.
func (d *decoder) decode(frame []byte) (*image.RGBA, error) {
	if err := d.dec.Decode(frame); err != nil {
		if errors.Is(err, govpx.ErrNeedKeyFrame) {
			return nil, err
		}
		return nil, fmt.Errorf("media: decoding a frame: %w", err)
	}
	img, ok := d.dec.NextFrame()
	if !ok {
		return nil, nil
	}
	return rgba(img), nil
}

// close releases the decoder.
func (d *decoder) close() error { return d.dec.Close() }

// rgba converts one decoded I420 frame to RGBA. The image is freshly
// allocated on every frame: the decoder's own planes are overwritten by the
// next frame, and a UI holding a picture that changes underneath it would
// tear. RGBA is what both UIs upload to the GPU.
func rgba(img govpx.Image) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, img.Width, img.Height))
	I420ToRGBA(Picture{
		Width:   img.Width,
		Height:  img.Height,
		Y:       img.Y,
		U:       img.U,
		V:       img.V,
		YStride: img.YStride,
		UStride: img.UStride,
		VStride: img.VStride,
	}, out)
	return out
}
