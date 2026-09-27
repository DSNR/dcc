package media_test

import (
	"sync"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
)

// sent collects what the pipeline handed to the Call.
type sent struct {
	mu     sync.Mutex
	frames [][]byte
}

func (s *sent) take(payload []byte, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, append([]byte(nil), payload...))
}

func (s *sent) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// waitFrames waits for the capture loop to produce n frames.
func waitFrames(t *testing.T, s *sent, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d frames after waiting; wanted %d", s.count(), n)
		}
		time.Sleep(media.FrameDuration)
	}
}

// TestAudioCaptures proves the fake microphone reaches the Call in whole
// frames, paced rather than dumped.
func TestAudioCaptures(t *testing.T) {
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: &media.Fake{}, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	defer audio.Close()

	waitFrames(t, &got, 3)
	got.mu.Lock()
	defer got.mu.Unlock()
	for i, frame := range got.frames {
		if len(frame) != media.PayloadBytes {
			t.Fatalf("frame %d is %d bytes, want %d", i, len(frame), media.PayloadBytes)
		}
	}
}

// TestAudioMuteStopsSending proves muting cuts the microphone off and
// unmuting brings it straight back — the device stays open throughout.
func TestAudioMuteStopsSending(t *testing.T) {
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: &media.Fake{}, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	defer audio.Close()

	waitFrames(t, &got, 2)
	audio.Mute(true)
	if !audio.Muted() {
		t.Fatal("Mute(true) did not take")
	}
	// Whatever was already in flight lands; after that, silence.
	time.Sleep(3 * media.FrameDuration)
	quiet := got.count()
	time.Sleep(5 * media.FrameDuration)
	if got.count() != quiet {
		t.Fatalf("a muted microphone sent %d more frames", got.count()-quiet)
	}

	audio.Mute(false)
	waitFrames(t, &got, quiet+2)
}

// TestAudioPlays proves what the Call delivers reaches the speaker as the
// tone it was: the recorder hears far more energy at the tone's own
// frequency than at one nobody sent.
func TestAudioPlays(t *testing.T) {
	const tone = 700.0
	devices := &media.Fake{Tone: tone}
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: devices, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	defer audio.Close()

	waitFrames(t, &got, 10)
	got.mu.Lock()
	frames := got.frames
	got.mu.Unlock()
	for i, frame := range frames {
		audio.Play(uint16(i), frame)
	}

	heard := devices.Heard()
	if at, off := heard.Power(tone), heard.Power(tone*2.5); at < 4*off {
		t.Fatalf("heard %.1f at %.0f Hz against %.1f at %.0f Hz — that is not the tone that was sent", at, tone, off, tone*2.5)
	}
}

// TestAudioCloseStopsSending proves Close waits for the capture goroutine, so
// a Call that has hung up cannot still be writing to a torn-down track.
func TestAudioCloseStopsSending(t *testing.T) {
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: &media.Fake{}, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	waitFrames(t, &got, 2)
	if err := audio.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after := got.count()
	time.Sleep(5 * media.FrameDuration)
	if got.count() != after {
		t.Fatalf("a closed pipeline sent %d more frames", got.count()-after)
	}
	if err := audio.Close(); err != nil {
		t.Fatalf("Close twice: %v", err)
	}
}

// TestAudioConcealsLoss proves a frame that never arrived is played as the
// silence it was. Closing the gap instead would spend the playout buffer, a
// frame of it per loss, and the Call would end up with nothing left to hide the
// next late frame behind.
func TestAudioConcealsLoss(t *testing.T) {
	devices := &media.Fake{}
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: devices, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	defer audio.Close()

	waitFrames(t, &got, 4)
	got.mu.Lock()
	frames := got.frames[:4]
	got.mu.Unlock()

	heard := devices.Heard()
	heard.Reset()
	// Frames 0 and 1 arrive, 2 is lost, 3 arrives.
	audio.Play(0, frames[0])
	audio.Play(1, frames[1])
	audio.Play(3, frames[3])
	if want := 4 * media.FrameSamples; len(heard.Samples()) != want {
		t.Fatalf("the speaker got %d samples for four frames of timeline, want %d",
			len(heard.Samples()), want)
	}
}

// TestAudioDropsLateFrames proves a frame that arrives after the gap it
// belonged to was already filled is thrown away. Playing it then would be
// audibly worse than the gap it was meant to repair.
func TestAudioDropsLateFrames(t *testing.T) {
	devices := &media.Fake{}
	var got sent
	audio, err := media.StartAudio(media.AudioOptions{Devices: devices, Send: got.take})
	if err != nil {
		t.Fatalf("StartAudio: %v", err)
	}
	defer audio.Close()

	waitFrames(t, &got, 4)
	got.mu.Lock()
	frames := got.frames[:4]
	got.mu.Unlock()

	heard := devices.Heard()
	heard.Reset()
	audio.Play(0, frames[0])
	audio.Play(2, frames[2])
	// Frame 1, arriving after 2 was played: too late to be worth hearing.
	audio.Play(1, frames[1])
	if want := 3 * media.FrameSamples; len(heard.Samples()) != want {
		t.Fatalf("the speaker got %d samples, want %d — the late frame was played anyway",
			len(heard.Samples()), want)
	}
}
