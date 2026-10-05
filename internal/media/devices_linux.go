package media

import (
	"errors"
	"fmt"

	"github.com/jfreymuth/pulse"
)

// System on Linux is PulseAudio for sound and V4L2 for the camera.
// PulseAudio is what every desktop Linux runs — directly, or as PipeWire's
// Pulse server. There is no ALSA path: dcc wants a mixer and a device that
// other applications can share, which is what a sound server is for. The
// camera side is in camera_linux.go.
func System() Devices { return linuxDevices{} }

// Screen implements Devices, sharing the primary display.
func (linuxDevices) Screen() (Screen, error) { return openScreen() }

// linuxDevices opens one PulseAudio client per audio stream. A client is a
// socket and a goroutine, and tying its life to the stream's means closing
// the stream closes everything it holds.
type linuxDevices struct{}

// Capture opens the default source as mono at SampleRate. PulseAudio
// resamples and downmixes on its side, so the pipeline never sees the
// hardware's real shape.
func (linuxDevices) Capture() (Source, error) {
	client, err := pulse.NewClient(pulse.ClientApplicationName("dcc"))
	if err != nil {
		return nil, fmt.Errorf("media: connecting to PulseAudio: %w", err)
	}
	buf := newRing(captureFrames * FrameSamples)
	stream, err := client.NewRecord(
		pulse.Int16Writer(func(pcm []int16) (int, error) {
			buf.write(pcm)
			return len(pcm), nil
		}),
		pulse.RecordMono,
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(FrameDuration.Seconds()),
		pulse.RecordMediaName("dcc call"),
	)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("media: opening the microphone: %w", err)
	}
	// PulseAudio is free to answer with a format of its own choosing. It
	// never has, but if it ever did the Call would play back at the wrong
	// speed, which is a stranger thing to debug than a refused microphone.
	if err := checkFormat("microphone", stream.SampleRate(), stream.Channels()); err != nil {
		stream.Close()
		client.Close()
		return nil, err
	}
	stream.Start()
	return &pulseSource{client: client, stream: stream, buf: buf}, nil
}

// Playback opens the default sink in the same shape.
func (linuxDevices) Playback() (Sink, error) {
	client, err := pulse.NewClient(pulse.ClientApplicationName("dcc"))
	if err != nil {
		return nil, fmt.Errorf("media: connecting to PulseAudio: %w", err)
	}
	buf := newPlayout(playoutTarget*FrameSamples, playoutFrames*FrameSamples)
	stream, err := client.NewPlayback(
		pulse.Int16Reader(func(pcm []int16) (int, error) {
			// Always a full buffer: short reads read as the end of the
			// stream, and silence is what an empty ring sounds like.
			buf.fill(pcm)
			return len(pcm), nil
		}),
		pulse.PlaybackMono,
		pulse.PlaybackSampleRate(SampleRate),
		// Two frames, not four: the ring in front of this is the Call's
		// jitter buffer, and a sound server that asks for more than half of
		// it at a time would starve it on every read.
		pulse.PlaybackLatency(2*FrameDuration.Seconds()),
		pulse.PlaybackMediaName("dcc call"),
	)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("media: opening the speaker: %w", err)
	}
	if err := checkFormat("speaker", stream.SampleRate(), stream.Channels()); err != nil {
		stream.Close()
		client.Close()
		return nil, err
	}
	stream.Start()
	return &pulseSink{client: client, stream: stream, buf: buf}, nil
}

// checkFormat reports a stream the sound server did not open in the shape the
// pipeline asked for.
func checkFormat(what string, rate, channels int) error {
	if rate != SampleRate || channels != 1 {
		return fmt.Errorf("media: PulseAudio opened the %s at %d Hz in %d channels, not %d Hz mono",
			what, rate, channels, SampleRate)
	}
	return nil
}

// pulseSource is a running record stream behind the Source boundary.
type pulseSource struct {
	client *pulse.Client
	stream *pulse.RecordStream
	buf    *ring
}

// Read implements Source, waiting for the sound server to deliver a frame.
func (s *pulseSource) Read(pcm []int16) error {
	if !s.buf.read(pcm) {
		if err := s.stream.Error(); err != nil {
			return fmt.Errorf("media: the microphone stream failed: %w", err)
		}
		return errors.New("media: the microphone is closed")
	}
	return nil
}

// Close implements Source.
func (s *pulseSource) Close() error {
	s.buf.close()
	s.stream.Close()
	s.client.Close()
	return nil
}

// pulseSink is a running playback stream behind the Sink boundary.
type pulseSink struct {
	client *pulse.Client
	stream *pulse.PlaybackStream
	buf    *ring
}

// Write implements Sink, handing the frame to the sound server's next read.
func (s *pulseSink) Write(pcm []int16) error {
	s.buf.write(pcm)
	return nil
}

// Close implements Sink.
func (s *pulseSink) Close() error {
	s.buf.close()
	s.stream.Close()
	s.client.Close()
	return nil
}
