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
	// keyframeSeconds caps how long a receiver that missed the last keyframe
	// waits for the next one even if it never asks. It is in seconds because
	// the camera and the screen run at different frame rates.
	keyframeSeconds = 10
	// videoMacroblock is VP8's macroblock size, and the granularity every
	// frame handed to govpx is sized in. A frame that is not a whole number
	// of macroblocks in both directions — 1920x1080 is 67.5 of them tall —
	// has a bottom row and a right column of part-macroblocks, and govpx
	// reads those as if they were whole: sixteen rows into a source plane
	// that has eight rows left, which is an index out of range that takes
	// the process down with it. ADR 0005 has the details.
	videoMacroblock = 16
)

// encoder turns Pictures into VP8 frames. It is built around one stream's
// frame size, frame rate and bitrate, and belongs to the capture goroutine
// that drives it.
type encoder struct {
	enc *govpx.VP8Encoder
	// width and height are the size actually encoded: the device's frame
	// size brought down to whole macroblocks. Every frame is handed over
	// cropped to them, whatever size the device delivered.
	width, height int
	// buf is the output buffer every frame is encoded into, reused across
	// frames — fifteen frames a second is no place to be allocating. The
	// frame handed out aliases it until the next encode.
	buf []byte
	// frames counts encoded frames, which at a fixed frame rate is the
	// presentation timestamp in the encoder's own timebase.
	frames uint64
	// abandoned says the encoder panicked and is not to be used again. It
	// needs no lock: an encoder belongs to one capture goroutine.
	abandoned bool
}

// newEncoder builds the encoder for one stream's frame size, frame rate and
// bitrate — a camera's, or a shared screen's.
func newEncoder(width, height, fps, bitrateKbps int) (*encoder, error) {
	// Whole macroblocks only, whatever the device delivered — see
	// videoMacroblock. A shared screen is already scaled to a whole number
	// of them (screenSize); a camera that hands out something else has at
	// most fifteen rows and columns cropped off it, which nobody on the
	// other side can tell from the framing.
	w := width & ^(videoMacroblock - 1)
	h := height & ^(videoMacroblock - 1)
	if w < videoMacroblock || h < videoMacroblock {
		return nil, fmt.Errorf("media: a %dx%d frame is too small to encode", width, height)
	}
	enc, err := govpx.NewVP8Encoder(govpx.EncoderOptions{
		Width:             w,
		Height:            h,
		FPS:               fps,
		Threads:           videoThreads,
		Deadline:          govpx.DeadlineRealtime,
		CpuUsed:           videoCPUUsed,
		RateControlMode:   govpx.RateControlCBR,
		TargetBitrateKbps: bitrateKbps,
		KeyFrameInterval:  keyframeSeconds * fps,
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
	return &encoder{enc: enc, width: w, height: h, buf: make([]byte, w*h)}, nil
}

// encode turns one Picture into a VP8 frame, forcing a keyframe when the
// other side has asked for one. The returned frame aliases the encoder's own
// buffer and is empty when rate control dropped the frame.
func (e *encoder) encode(pic Picture, force bool) (frame []byte, err error) {
	if e.abandoned {
		return nil, errors.New("media: the VP8 encoder was abandoned after it panicked")
	}
	// govpx is a young encoder and reads a little past the planes it is
	// handed in places (ADR 0005). None of that is worth a dead process, so a
	// panic becomes an error here, and the capture goroutine treats it the
	// way it treats a device that died: the stream stops and the other side
	// is told this side is no longer sending. The encoder is abandoned rather
	// than retried, because the panic unwound it halfway through a frame and
	// its reference buffers no longer describe anything a receiver could
	// decode against — sharing again builds a fresh one, starting from a
	// keyframe.
	//
	// Be honest about the limit: this only catches a panic raised on this
	// goroutine. govpx's row workers have no recover of their own, so a panic
	// on one of those is unreachable from here and still fatal. That is why
	// the real fix is the macroblock-aligned frame size above, and this is
	// only the net under it.
	defer func() {
		if r := recover(); r != nil {
			e.abandoned = true
			frame, err = nil, fmt.Errorf("media: the VP8 encoder panicked: %v", r)
		}
	}()
	if force {
		e.enc.ForceKeyFrame()
	}
	// The encoder's own size, not the picture's: a frame wider or taller
	// than whole macroblocks is encoded as its top-left corner, using the
	// device's strides to step down it.
	result, err := e.enc.EncodeInto(e.buf, govpx.Image{
		Width:   e.width,
		Height:  e.height,
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

// rgba converts one decoded I420 frame to RGBA, which is what both UIs upload
// to the GPU.
func rgba(img govpx.Image) *image.RGBA {
	return Picture{
		Width:   img.Width,
		Height:  img.Height,
		Y:       img.Y,
		U:       img.U,
		V:       img.V,
		YStride: img.YStride,
		UStride: img.UStride,
		VStride: img.VStride,
	}.RGBA()
}
