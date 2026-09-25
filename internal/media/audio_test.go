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
		if len(frame) != media.FrameSamples {
			t.Fatalf("frame %d is %d bytes, want %d", i, len(frame), media.FrameSamples)
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
	for _, frame := range frames {
		audio.Play(frame)
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
