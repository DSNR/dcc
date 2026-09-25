package media

import (
	"errors"
	"fmt"

	"github.com/jfreymuth/pulse"
)

// System is PulseAudio, which every desktop Linux runs — directly, or as
// PipeWire's Pulse server. There is no ALSA path: dcc wants a mixer and a
// device that other applications can share, which is what a sound server is
// for.
func System() Devices { return pulseDevices{} }

// pulseDevices opens one PulseAudio client per stream. A client is a socket
// and a goroutine, and tying its life to the stream's means closing the
// stream closes everything it holds.
type pulseDevices struct{}

// Capture opens the default source as mono at SampleRate. PulseAudio
// resamples and downmixes on its side, so the pipeline never sees the
// hardware's real shape.
func (pulseDevices) Capture() (Source, error) {
	client, err := pulse.NewClient(pulse.ClientApplicationName("dcc"))
	if err != nil {
		return nil, fmt.Errorf("media: connecting to PulseAudio: %w", err)
	}
	buf := newRing(SampleRate * bufferSeconds)
	stream, err := client.NewRecord(
		pulse.Int16Writer(func(pcm []int16) (int, error) {
			buf.write(pcm)
			return len(pcm), nil
		}),
		pulse.RecordSampleRate(SampleRate),
		pulse.RecordLatency(FrameDuration.Seconds()),
		pulse.RecordMediaName("dcc call"),
	)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("media: opening the microphone: %w", err)
	}
	stream.Start()
	return &pulseSource{client: client, stream: stream, buf: buf}, nil
}

// Playback opens the default sink in the same shape.
func (pulseDevices) Playback() (Sink, error) {
	client, err := pulse.NewClient(pulse.ClientApplicationName("dcc"))
	if err != nil {
		return nil, fmt.Errorf("media: connecting to PulseAudio: %w", err)
	}
	buf := newRing(SampleRate * bufferSeconds)
	stream, err := client.NewPlayback(
		pulse.Int16Reader(func(pcm []int16) (int, error) {
			// Always a full buffer: short reads read as the end of the
			// stream, and silence is what an empty ring sounds like.
			buf.fill(pcm)
			return len(pcm), nil
		}),
		pulse.PlaybackSampleRate(SampleRate),
		pulse.PlaybackLatency(4*FrameDuration.Seconds()),
		pulse.PlaybackMediaName("dcc call"),
	)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("media: opening the speaker: %w", err)
	}
	stream.Start()
	return &pulseSink{client: client, stream: stream, buf: buf}, nil
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
