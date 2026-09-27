package media

import (
	"math/rand"
	"testing"
)

// playing is what a speaker heard: how many of the samples handed to it were
// the silence the ring had to invent, and how deep the buffer was left.
type playing struct {
	silence, played, level int
}

// speaker drains r in chunks the way a sound server does, while frames land on
// a schedule of their own — jittery, sometimes lost, against a playback clock
// that is not quite the sender's. It reports what was heard after the first two
// seconds, so that the cushion the Call starts with is not counted as a
// dropout.
func speaker(r *ring, jitterMs, lossRate, driftPPM float64, chunk, seconds int, conceal bool) playing {
	rnd := rand.New(rand.NewSource(7))
	frames := seconds * 50
	type packet struct {
		at   float64
		seq  int
		lost bool
	}
	packets := make([]packet, frames)
	for k := range packets {
		spike := 0.0
		if rnd.Float64() < 0.02 {
			spike = jitterMs * 3
		}
		packets[k] = packet{
			at:   float64(20*k) + rnd.ExpFloat64()*jitterMs + spike,
			seq:  k,
			lost: rnd.Float64() < lossRate,
		}
	}
	out := make([]int16, chunk)
	var heard playing
	next, expect := 0, 0
	perChunk := 1000 * float64(chunk) / SampleRate * (1 + driftPPM/1e6)
	for request := 0; request < frames*FrameSamples/chunk; request++ {
		now := perChunk * float64(request)
		for next < frames && packets[next].at <= now {
			p := packets[next]
			next++
			if p.lost {
				continue
			}
			if conceal {
				for range p.seq - expect {
					r.write(make([]int16, FrameSamples))
				}
			}
			expect = p.seq + 1
			frame := make([]int16, FrameSamples)
			for i := range frame {
				frame[i] = 1000
			}
			r.write(frame)
		}
		r.fill(out)
		if now < 2000 {
			continue
		}
		heard.played += chunk
		for _, s := range out {
			if s == 0 {
				heard.silence++
			}
		}
	}
	r.mu.Lock()
	heard.level = len(r.buf)
	r.mu.Unlock()
	return heard
}

func newTestPlayout() *ring {
	return newPlayout(playoutTarget*FrameSamples, playoutFrames*FrameSamples)
}

// TestRingCapturePaces proves the microphone side hands over whole frames in
// order and stops when the device does.
func TestRingCapturePaces(t *testing.T) {
	r := newRing(captureFrames * FrameSamples)
	for i := range 3 {
		frame := make([]int16, FrameSamples)
		for j := range frame {
			frame[j] = int16(i + 1)
		}
		r.write(frame)
	}
	got := make([]int16, FrameSamples)
	for i := range 3 {
		if !r.read(got) {
			t.Fatalf("frame %d did not arrive", i)
		}
		if got[0] != int16(i+1) || got[FrameSamples-1] != int16(i+1) {
			t.Fatalf("frame %d came back as %d", i, got[0])
		}
	}
	r.close()
	if r.read(got) {
		t.Fatal("a closed ring still handed over a frame")
	}
}

// TestRingCaptureDropsOldest proves a microphone whose audio nobody is reading
// drops what has gone stale rather than growing without bound: a Call is worth
// the newest audio, never a backlog of it.
func TestRingCaptureDropsOldest(t *testing.T) {
	r := newRing(captureFrames * FrameSamples)
	for i := range captureFrames * 3 {
		frame := make([]int16, FrameSamples)
		for j := range frame {
			frame[j] = int16(i)
		}
		r.write(frame)
	}
	r.mu.Lock()
	level := len(r.buf)
	r.mu.Unlock()
	if level != captureFrames*FrameSamples {
		t.Fatalf("the ring holds %d samples, want its cap of %d", level, captureFrames*FrameSamples)
	}
	got := make([]int16, FrameSamples)
	r.read(got)
	if want := int16(captureFrames * 2); got[0] != want {
		t.Fatalf("the oldest frame left is %d, want %d — the wrong end was dropped", got[0], want)
	}
}

// TestRingPlayoutHoldsBack proves a Call starts with a cushion: the speaker
// hears silence until the ring has its target in hand, which is what keeps the
// first late frame of a Call from being the first click of it.
func TestRingPlayoutHoldsBack(t *testing.T) {
	r := newTestPlayout()
	out := make([]int16, FrameSamples)
	frame := make([]int16, FrameSamples)
	for i := range frame {
		frame[i] = 1000
	}
	for range playoutTarget - 1 {
		r.write(frame)
		r.fill(out)
		if out[0] != 0 {
			t.Fatal("playback started before the buffer had its cushion")
		}
	}
	r.write(frame)
	r.write(frame)
	r.fill(out)
	if out[0] != 1000 {
		t.Fatal("playback did not start once the cushion was there")
	}
}

// TestRingPlayoutBoundsLatency proves the playout buffer cannot grow into a
// delay: a sender whose clock runs faster than this side's speaker, or a burst
// that arrives all at once, is capped rather than queued behind a conversation.
func TestRingPlayoutBoundsLatency(t *testing.T) {
	r := newTestPlayout()
	frame := make([]int16, FrameSamples)
	for i := range frame {
		frame[i] = 1000
	}
	for range playoutFrames * 4 {
		r.write(frame)
	}
	r.mu.Lock()
	level := len(r.buf)
	r.mu.Unlock()
	if level > playoutFrames*FrameSamples {
		t.Fatalf("the playout buffer holds %d ms, more than its cap of %d ms",
			1000*level/SampleRate, 1000*playoutFrames*FrameSamples/SampleRate)
	}
}

// TestRingPlayoutSurvivesJitter is the quality claim as a number: a speaker
// draining the ring against a network with 10 ms of jitter hears silence for
// well under a tenth of a percent of a five minute Call, and settles at a
// latency measured in tens of milliseconds rather than hundreds.
func TestRingPlayoutSurvivesJitter(t *testing.T) {
	for _, chunk := range []int{FrameSamples / 2, 2 * FrameSamples} {
		heard := speaker(newTestPlayout(), 10, 0, 0, chunk, 300, true)
		if share := float64(heard.silence) / float64(heard.played); share > 0.001 {
			t.Errorf("chunks of %d: %.3f%% of the Call was invented silence, want under 0.1%%",
				chunk, 100*share)
		}
		if latency := 1000 * heard.level / SampleRate; latency > 200 {
			t.Errorf("chunks of %d: the buffer settled at %d ms, want under 200", chunk, latency)
		}
	}
}

// TestRingPlayoutTrimsSlack proves a buffer that got deep during a bad patch
// gives the latency back once the network calms down, rather than carrying it
// for the rest of the Call.
func TestRingPlayoutTrimsSlack(t *testing.T) {
	r := newTestPlayout()
	frame := make([]int16, FrameSamples)
	for i := range frame {
		frame[i] = 1000
	}
	// A burst leaves the buffer far deeper than it needs to be.
	for range playoutFrames {
		r.write(frame)
	}
	out := make([]int16, FrameSamples)
	// A steady stretch: one frame in, one frame out, never running dry.
	for range playoutSettle/FrameSamples + 2 {
		r.write(frame)
		r.fill(out)
		if out[0] != 1000 {
			t.Fatal("the speaker heard silence from a buffer that was never empty")
		}
	}
	r.mu.Lock()
	level := len(r.buf)
	r.mu.Unlock()
	if level > playoutTarget*FrameSamples {
		t.Fatalf("the buffer is still %d ms deep after a calm stretch, want no more than %d ms",
			1000*level/SampleRate, 1000*playoutTarget*FrameSamples/SampleRate)
	}
}

// TestRingPlayoutConcealKeepsTiming proves why the receive side plays a lost
// frame as silence instead of closing the gap: closing it spends the buffer,
// one frame of latency per loss, until there is none left to hide the next late
// frame behind.
func TestRingPlayoutConcealKeepsTiming(t *testing.T) {
	const chunk = 2 * FrameSamples
	closed := speaker(newTestPlayout(), 5, 0.02, 0, chunk, 120, false)
	concealed := speaker(newTestPlayout(), 5, 0.02, 0, chunk, 120, true)
	if concealed.level <= closed.level {
		t.Errorf("concealing loss left %d ms of buffer and closing the gap left %d ms; concealing should keep more",
			1000*concealed.level/SampleRate, 1000*closed.level/SampleRate)
	}
}
