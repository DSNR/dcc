package media

import "sync"

// bufferSeconds is how much audio a driver's ring holds before it starts
// dropping the oldest. A second is far more than a Call ever needs; it
// exists so a stalled pipeline degrades into dropped frames rather than
// backpressure on the sound server.
const bufferSeconds = 1

// ring is a bounded FIFO of PCM samples, written by a driver's own callback
// goroutine and read by the pipeline. When it overflows it drops the oldest
// samples rather than blocking: a driver callback that waits on dcc is a
// glitch in everything else the machine is playing, and audio that has
// queued up past the cap is too late to be worth hearing anyway.
type ring struct {
	mu     sync.Mutex
	full   *sync.Cond
	buf    []int16
	limit  int
	closed bool
}

// newRing makes a ring holding at most limit samples.
func newRing(limit int) *ring {
	r := &ring{limit: limit}
	r.full = sync.NewCond(&r.mu)
	return r
}

// write adds samples, dropping the oldest to stay within the cap.
func (r *ring) write(pcm []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.buf = append(r.buf, pcm...)
	if over := len(r.buf) - r.limit; over > 0 {
		r.buf = r.buf[over:]
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
// silence. A speaker starved of audio should go quiet, not stall.
func (r *ring) fill(pcm []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := copy(pcm, r.buf)
	r.buf = r.buf[n:]
	clear(pcm[n:])
}

// close releases anyone waiting in read.
func (r *ring) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.full.Broadcast()
}
