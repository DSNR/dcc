package media

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// The shape of dcc's audio, at the device boundary and on the wire. One mono
// stream at telephone quality, cut into 20 ms frames: small enough that a
// lost one is inaudible, large enough that the RTP header is not most of the
// packet.
const (
	// SampleRate is the pipeline's sample rate in hertz. A driver whose
	// hardware disagrees resamples on its own side of the boundary.
	SampleRate = 8000
	// FrameDuration is how much audio one frame carries.
	FrameDuration = 20 * time.Millisecond
	// FrameSamples is FrameDuration's worth of samples — the length of every
	// PCM buffer that crosses the boundary.
	FrameSamples = SampleRate * int(FrameDuration) / int(time.Second)
)

// Source is a microphone: successive frames of signed 16-bit mono PCM at
// SampleRate. Read fills the whole buffer and blocks until it can — it is
// the pipeline's clock, so a Source must deliver in real time rather than as
// fast as it can.
type Source interface {
	Read(pcm []int16) error
	Close() error
}

// Sink is a speaker, taking frames in the same shape a Source produces them.
// Write must not block on the device: audio that cannot be played now is
// late, and dropping it keeps the Call from drifting.
type Sink interface {
	Write(pcm []int16) error
	Close() error
}

// Devices is the media-device boundary — the one place the operating
// system's sound card enters dcc. System() returns the real one; tests pass
// a Fake, which is what lets a Call be exercised end to end without one.
type Devices interface {
	// Capture opens the default microphone.
	Capture() (Source, error)
	// Playback opens the default speaker.
	Playback() (Sink, error)
}

// ErrNoDevices reports that this build has no driver for the platform it is
// running on. A Call still connects; it is simply silent on this side.
var ErrNoDevices = errors.New("media: no audio devices on this platform")

// AudioOptions configures one Call's audio.
type AudioOptions struct {
	// Devices opens the microphone and speaker. Nil means the real ones.
	Devices Devices
	// Send takes one encoded frame, on the capture goroutine, once every
	// FrameDuration. Required.
	Send func(payload []byte, d time.Duration)
	// Muted starts the microphone muted.
	Muted bool
}

// Audio is one Call's audio. It owns the goroutine that paces capture off
// the microphone, and decodes what the Call delivers into the speaker. It is
// created when a Call goes Active and closed when it ends.
type Audio struct {
	send func([]byte, time.Duration)

	mu     sync.Mutex
	closed bool
	muted  bool
	source Source
	sink   Sink
	// play is the scratch buffer Play decodes into, reused across frames
	// the way capture reuses its own — fifty frames a second is no place to
	// be allocating.
	play []int16
	// done closes when the capture goroutine has stopped, so Close can
	// promise no Send outlives it.
	done chan struct{}
}

// StartAudio opens the devices and starts capturing. A microphone that will
// not open is an error; a speaker that will not open is not — being unable
// to hear is a worse Call, not a failed one.
func StartAudio(opts AudioOptions) (*Audio, error) {
	if opts.Send == nil {
		return nil, errors.New("media: audio needs somewhere to send frames")
	}
	devices := opts.Devices
	if devices == nil {
		devices = System()
	}
	source, err := devices.Capture()
	if err != nil {
		return nil, fmt.Errorf("media: opening the microphone: %w", err)
	}
	sink, err := devices.Playback()
	if err != nil {
		sink = nil
	}
	a := &Audio{
		send:   opts.Send,
		muted:  opts.Muted,
		source: source,
		sink:   sink,
		play:   make([]int16, FrameSamples),
		done:   make(chan struct{}),
	}
	go a.capture()
	return a, nil
}

// Mute stops the microphone being sent. Capture keeps running underneath:
// the device stays open, so unmuting is instant and the frames that were
// dropped are simply never missed.
func (a *Audio) Mute(muted bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.muted = muted
}

// Muted reports whether the microphone is being sent.
func (a *Audio) Muted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.muted
}

// Play decodes one received frame into the speaker. A frame that arrives
// after Close, or with nothing in it, is dropped. Frames are played in the
// order they are handed over, which is the order the Call delivered them.
func (a *Audio) Play(payload []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.sink == nil || len(payload) == 0 {
		return
	}
	if len(payload) > len(a.play) {
		// A frame longer than this pipeline's own: the other side is
		// allowed a different frame size, so the buffer grows to it once.
		a.play = make([]int16, len(payload))
	}
	// A dropped frame is a click; a dropped Call is not. Playback errors are
	// this side's speaker misbehaving and say nothing about the connection.
	_ = a.sink.Write(Decode(payload, a.play))
}

// Close stops capture and releases both devices. It waits for the capture
// goroutine, so no Send callback runs after it returns. Closing twice is
// fine.
func (a *Audio) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	source, sink := a.source, a.sink
	a.mu.Unlock()

	// Closing the Source is what unblocks the capture goroutine's Read.
	err := source.Close()
	<-a.done
	if sink != nil {
		_ = sink.Close()
	}
	return err
}

// capture is the pipeline's clock: the microphone hands over one frame every
// FrameDuration, and each one is encoded and sent — unless the microphone is
// muted, in which case it is read and thrown away, which is what keeps
// unmuting instant.
func (a *Audio) capture() {
	defer close(a.done)
	pcm := make([]int16, FrameSamples)
	payload := make([]byte, FrameSamples)
	for {
		a.mu.Lock()
		source, closed := a.source, a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		if err := source.Read(pcm); err != nil {
			// The microphone is gone. The Call carries on without it rather
			// than ending over a sound card.
			return
		}
		a.mu.Lock()
		muted, closed := a.muted, a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		if muted {
			continue
		}
		a.send(Encode(pcm, payload), FrameDuration)
	}
}
