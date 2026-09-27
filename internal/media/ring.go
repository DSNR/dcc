package media

import "sync"

// How much audio a driver's ring holds, in frames.
//
// A microphone's ring only has to cover the gap between the sound server
// delivering and the capture loop reading, which is one frame; the cap exists
// so that a stalled pipeline degrades into dropped frames rather than
// backpressure on the sound server, and it is deliberately short because
// audio that has queued up behind a stall is too old to be worth sending.
//
// A speaker's ring is the Call's jitter buffer, and its size is the whole
// reason this file is interesting. Frames leave the other side every 20 ms
// and arrive whenever the network says; a speaker that plays each one the
// moment it lands has nothing to play when one is late, and silence dropped
// into the middle of a sentence is the click people describe as bad audio.
// So playback holds playoutTarget frames back before it starts, every arrival
// earlier than that is absorbed instead of heard, and the depth it settles at
// is bounded at both ends: deep enough to hide the network, never so deep that
// a Call carries a conversation's worth of delay.
const (
	captureFrames = 12
	// playoutTarget is the cushion playback starts with and trims back to
	// when the network allows; playoutFrames is the hard cap, past which the
	// oldest audio is dropped rather than queued behind a conversation.
	playoutTarget = 4
	playoutFrames = 20
	// playoutSettle is how long the speaker must go without running dry
	// before the buffer hands back the slack it has proved it does not need.
	playoutSettle = 10 * SampleRate
)

// ring is a bounded FIFO of PCM samples, written by a driver's own callback
// goroutine and read by the pipeline. When it overflows it drops the oldest
// samples rather than blocking: a driver callback that waits on dcc is a
// glitch in everything else the machine is playing, and audio that has queued
// up past the cap is too late to be worth hearing anyway.
type ring struct {
	mu   sync.Mutex
	full *sync.Cond
	buf  []int16
	// limit is the most the ring will hold. target is how much a playout ring
	// holds back before it plays anything and the depth it trims itself back
	// to, and is zero for a capture ring, which has nothing to hide jitter
	// from.
	limit, target int
	// settled counts the samples played since the last time the ring ran
	// dry, and low is the least the ring has held over that stretch — the
	// slack it has proved it does not need, which is the only latency it is
	// safe to hand back.
	settled, low int
	// priming is a playout ring that has not started yet, holding its first
	// frames back until it has a cushion. Only the start of a Call primes:
	// once audio is playing, a short silence where a late frame should have
	// been is its own repair, and stopping to rebuild would cost more silence
	// than it saved.
	priming bool
	closed  bool
}

// newRing makes a capture ring holding at most limit samples.
func newRing(limit int) *ring {
	r := &ring{limit: limit}
	r.full = sync.NewCond(&r.mu)
	return r
}

// newPlayout makes a speaker's ring: the same FIFO with target samples of
// jitter buffer in front of it, holding at most limit.
func newPlayout(target, limit int) *ring {
	r := newRing(limit)
	r.target, r.priming, r.low = target, true, target
	return r
}

// write adds samples, dropping the oldest to stay within the cap. A playout
// ring that has run that far ahead of its speaker is drifting — the two
// machines' clocks are not the same clock — so it drops back to its target
// rather than to the cap, which trades one audible step for a steady trickle
// of them.
func (r *ring) write(pcm []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.buf = append(r.buf, pcm...)
	if len(r.buf) > r.limit {
		keep := r.limit
		if r.target > 0 {
			keep = r.target
		}
		r.buf = r.buf[len(r.buf)-keep:]
	}
	r.full.Broadcast()
}

// read fills pcm completely, waiting for the driver to deliver. It returns
// false once the ring is closed and drained, which is how a Source's Read
// learns the device has gone.
func (r *ring) read(pcm []int16) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.buf) < len(pcm) && !r.closed {
		r.full.Wait()
	}
	if len(r.buf) < len(pcm) {
		return false
	}
	copy(pcm, r.buf[:len(pcm)])
	r.buf = r.buf[len(pcm):]
	return true
}

// fill takes up to len(pcm) samples without waiting, padding the rest with
// silence: a speaker starved of audio should go quiet, not stall. A playout
// ring stays quiet until it has its target in hand, so that a Call starts with
// a cushion rather than earning one through clicks.
func (r *ring) fill(pcm []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.priming {
		want := r.target
		if len(pcm) > want {
			want = len(pcm)
		}
		if len(r.buf) < want {
			clear(pcm)
			return
		}
		r.priming = false
	}
	n := copy(pcm, r.buf)
	r.buf = r.buf[n:]
	clear(pcm[n:])
	if r.target == 0 {
		return
	}
	if n < len(pcm) {
		// Ran dry. The silence just padded in is itself the repair: the
		// speaker's demand for this moment has been met without spending any
		// audio, so the frames still arriving queue up behind it and the
		// buffer comes back that much deeper. Waiting for a target instead
		// would trade this click for a much longer gap.
		r.settled, r.low = 0, 0
		return
	}
	r.settled += n
	r.low = min(r.low, len(r.buf))
	if r.settled < playoutSettle {
		return
	}
	// Ten seconds without running dry: skip whatever the buffer has proved it
	// does not need, keeping target in hand as the margin the next late frame
	// will want. A ring that has dipped below target over that stretch has
	// proved nothing and keeps everything. That is how a Call whose
	// other side runs a slightly faster clock, or one that padded its way to a
	// deep buffer through a bad patch, gets its latency back instead of
	// carrying it for good.
	if drop := min(len(r.buf)-r.target, r.low-r.target); drop > 0 {
		r.buf = r.buf[drop:]
	}
	r.settled, r.low = 0, len(r.buf)
}

// close releases anyone waiting in read.
func (r *ring) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.full.Broadcast()
}
