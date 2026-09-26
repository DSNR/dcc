package media

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sync"
	"time"
)

// ToneFrequency is the note a Fake microphone hums when it is not told
// otherwise — A above middle C, well inside a telephone band.
const ToneFrequency = 440.0

// TestPatternTint is the colour a Fake camera paints when it is not told
// otherwise: a mid green, far enough from grey that a lossy codec cannot
// lose it.
var TestPatternTint = color.RGBA{R: 40, G: 180, B: 60, A: 0xFF}

// Fake is the media-device boundary made of software: a tone generator where
// the microphone would be, a recorder where the speaker would be, and a test
// pattern where the camera would be. It is what lets a Call be driven end to
// end — rung, answered, heard, seen, muted, hung up — with no sound card or
// webcam anywhere in the test.
type Fake struct {
	// Tone is the frequency the fake microphone hums, in hertz. Zero means
	// ToneFrequency. Giving the two sides of a test different tones is what
	// makes "I heard them, not myself" a thing a test can assert.
	Tone float64
	// Tint is the colour the fake camera's test pattern is painted in. Zero
	// means TestPatternTint. It is the video half of the same trick: two
	// sides with different tints make "I am seeing them" assertable.
	Tint  color.RGBA
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

// Camera opens the fake camera.
func (f *Fake) Camera() (Camera, error) {
	tint := f.Tint
	if tint == (color.RGBA{}) {
		tint = TestPatternTint
	}
	return &pattern{
		tint: tint,
		pic:  NewPicture(VideoWidth, VideoHeight),
		done: make(chan struct{}),
		next: time.Now(),
	}, nil
}

// pattern is a fake camera: a flat tint with a bar sweeping across it,
// delivered in real time one frame every VideoFrameDuration. The tint is what
// a test recognises the sender by; the bar is what makes every frame differ
// from the last, so the encoder produces inter frames rather than an endless
// run of identical ones.
type pattern struct {
	tint  color.RGBA
	pic   Picture
	frame int
	next  time.Time
	done  chan struct{}
	once  sync.Once
}

// Read paints the next frame, sleeping until it is due.
func (p *pattern) Read() (Picture, error) {
	p.next = p.next.Add(VideoFrameDuration)
	select {
	case <-time.After(time.Until(p.next)):
	case <-p.done:
		return Picture{}, errors.New("media: the fake camera is closed")
	}
	p.pic.fill(p.tint)
	// The bar is a sixteenth of the frame wide and takes sixteen frames to
	// cross it, in luma only — a white stripe over whatever the tint is.
	width := p.pic.Width / 16
	start := (p.frame % 16) * width
	for row := range p.pic.Height {
		line := p.pic.Y[row*p.pic.YStride:]
		for i := start; i < start+width && i < p.pic.Width; i++ {
			line[i] = 235
		}
	}
	p.frame++
	return p.pic, nil
}

// Close stops the pattern, unblocking a Read that is waiting for the next
// frame.
func (p *pattern) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

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

// TintTolerance is how far a channel may drift and still count as the colour
// that was sent. VP8 at a Call's bitrate does not return a flat colour
// exactly, so nothing that looks at a decoded picture can ask for exactness.
const TintTolerance = 40

// Tinted is the share of a picture within TintTolerance of a colour — the
// video counterpart of Recorder.Power, and for the same reason: it turns
// "their camera arrived" into something a test can assert without eyes. Two
// Fakes with different tints make "I am seeing them, not myself" assertable.
func Tinted(img *image.RGBA, want color.RGBA) float64 {
	near := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		if channelNear(img.Pix[i], want.R) &&
			channelNear(img.Pix[i+1], want.G) &&
			channelNear(img.Pix[i+2], want.B) {
			near++
		}
	}
	bounds := img.Bounds()
	if bounds.Empty() {
		return 0
	}
	return float64(near) / float64(bounds.Dx()*bounds.Dy())
}

// channelNear is one colour channel within TintTolerance of another.
func channelNear(got, want byte) bool {
	d := int(got) - int(want)
	return d <= TintTolerance && d >= -TintTolerance
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
