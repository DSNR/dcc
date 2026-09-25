package media

import (
	"errors"
	"math"
	"sync"
	"time"
)

// ToneFrequency is the note a Fake microphone hums when it is not told
// otherwise — A above middle C, well inside a telephone band.
const ToneFrequency = 440.0

// Fake is the media-device boundary made of software: a tone generator where
// the microphone would be, a recorder where the speaker would be. It is what
// lets a Call be driven end to end — rung, answered, heard, muted, hung up —
// with no sound card anywhere in the test.
type Fake struct {
	// Tone is the frequency the fake microphone hums, in hertz. Zero means
	// ToneFrequency. Giving the two sides of a test different tones is what
	// makes "I heard them, not myself" a thing a test can assert.
	Tone  float64
	mu    sync.Mutex
	heard *Recorder
}

// Heard is everything written to the fake speaker — the same Recorder every
// time, whether or not a Call has opened it yet, so a test can hold onto it
// across a Call that opens and closes its devices.
func (f *Fake) Heard() *Recorder {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.heard == nil {
		f.heard = &Recorder{}
	}
	return f.heard
}

// Capture opens the fake microphone.
func (f *Fake) Capture() (Source, error) {
	freq := f.Tone
	if freq == 0 {
		freq = ToneFrequency
	}
	return &tone{freq: freq, done: make(chan struct{}), next: time.Now()}, nil
}

// Playback opens the fake speaker.
func (f *Fake) Playback() (Sink, error) { return f.Heard(), nil }

// tone is a fake microphone: a sine wave delivered in real time, one frame
// every FrameDuration, because the capture loop is paced by its microphone
// and a Source that ran flat out would flood the Call.
type tone struct {
	freq  float64
	phase float64
	next  time.Time
	done  chan struct{}
	once  sync.Once
}

// Read fills the frame, sleeping until the frame is due.
func (t *tone) Read(pcm []int16) error {
	t.next = t.next.Add(FrameDuration)
	select {
	case <-time.After(time.Until(t.next)):
	case <-t.done:
		return errors.New("media: the fake microphone is closed")
	}
	step := 2 * math.Pi * t.freq / SampleRate
	for i := range pcm {
		pcm[i] = int16(12000 * math.Sin(t.phase))
		t.phase += step
	}
	return nil
}

// Close stops the tone, unblocking a Read that is waiting for the next frame.
func (t *tone) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

// Recorder is a fake speaker: it keeps every sample it is given, so a test
// can ask what this side actually heard.
type Recorder struct {
	mu      sync.Mutex
	samples []int16
}

// Write implements Sink.
func (r *Recorder) Write(pcm []int16) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, pcm...)
	return nil
}

// Close implements Sink. A Recorder keeps what it heard across a Call ending,
// which is the whole point of it.
func (r *Recorder) Close() error { return nil }

// Samples is everything heard so far.
func (r *Recorder) Samples() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int16(nil), r.samples...)
}

// Reset forgets everything heard so far, so a test can say "and from here
// on, silence".
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = nil
}

// Power is how much of what was heard is at freq, by Goertzel's filter,
// normalised by the number of samples. Comparing the power at the other
// side's tone against the power at a frequency nobody is sending is what
// turns "audio arrived" into something a test can assert without ears.
func (r *Recorder) Power(freq float64) float64 {
	samples := r.Samples()
	if len(samples) == 0 {
		return 0
	}
	coeff := 2 * math.Cos(2*math.Pi*freq/SampleRate)
	var s0, s1, s2 float64
	for _, sample := range samples {
		s0 = float64(sample) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	power := s1*s1 + s2*s2 - coeff*s1*s2
	return math.Sqrt(math.Max(power, 0)) / float64(len(samples))
}
