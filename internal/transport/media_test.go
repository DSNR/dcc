package transport_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/DSNR/dcc/internal/media"
	"github.com/DSNR/dcc/internal/transport"
)

// TestMediaFlowsBothWays negotiates a Call's media from one side only and
// proves the other side catches up on its own: both ends report media up,
// and an encoded frame written at either end arrives at the other.
func TestMediaFlowsBothWays(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	b := start(t, transport.Options{Certificate: certB, Remote: certA.Fingerprint()})
	connect(a, b)
	waitUp(t, a)
	waitUp(t, b)

	// Only the initiator asks. The responder makes its tracks off the offer,
	// which is what keeps the two accepts racing harmless.
	if err := a.tr.StartMedia(); err != nil {
		t.Fatalf("StartMedia: %v", err)
	}
	waitMedia(t, a)
	waitMedia(t, b)

	sendUntilHeard(t, a, b)
	sendUntilHeard(t, b, a)
}

// TestWriteAudioBeforeCall checks audio written with no Call negotiated is
// refused rather than silently dropped.
func TestWriteAudioBeforeCall(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	if err := a.tr.WriteAudio(make([]byte, media.FrameSamples), media.FrameDuration); err == nil {
		t.Fatal("audio was accepted with no Call to send it to")
	}
}

// waitMedia waits for one side to report the other's media arriving.
func waitMedia(t *testing.T, e *end) {
	t.Helper()
	select {
	case <-e.media:
	case err := <-e.down:
		t.Fatalf("Transport went down waiting for media: %v", err)
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for media to come up")
	}
}

// sendUntilHeard writes frames from one side until the other hears one. The
// repetition is deliberate: RTP is lossy by design, and the first frames of
// a stream may be written before the receiver has finished binding it.
func sendUntilHeard(t *testing.T, from, to *end) {
	t.Helper()
	payload := media.Encode(make([]int16, media.FrameSamples), make([]byte, media.FrameSamples))
	deadline := time.After(waitTimeout)
	for {
		if err := from.tr.WriteAudio(payload, media.FrameDuration); err != nil {
			t.Fatalf("WriteAudio: %v", err)
		}
		select {
		case got := <-to.audio:
			if len(got) != len(payload) {
				t.Fatalf("heard %d bytes, want %d", len(got), len(payload))
			}
			return
		case err := <-to.down:
			t.Fatalf("Transport went down waiting for audio: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for audio to arrive")
		case <-time.After(media.FrameDuration):
		}
	}
}

// TestVideoFlowsAndPLIComesBack proves the camera stream survives the trip:
// a frame written at one end arrives whole at the other — reassembled from
// however many RTP packets it took — and a receiver that asks for a keyframe
// is heard by the sender, which is the only way a stream that lost its
// reference frames ever recovers.
func TestVideoFlowsAndPLIComesBack(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	b := start(t, transport.Options{Certificate: certB, Remote: certA.Fingerprint()})
	connect(a, b)
	waitUp(t, a)
	waitUp(t, b)

	if err := a.tr.StartMedia(); err != nil {
		t.Fatalf("StartMedia: %v", err)
	}
	waitMedia(t, a)
	waitMedia(t, b)

	// Two packets' worth, so that reassembly is doing something.
	frame := make([]byte, 2*1200)
	for i := range frame {
		frame[i] = byte(i)
	}
	sendVideoUntilSeen(t, a, b, frame)

	// The side that is watching asks for a keyframe; the side that is
	// sending hears the ask.
	b.tr.RequestKeyframe()
	select {
	case <-a.keyfr:
	case err := <-a.down:
		t.Fatalf("Transport went down waiting for the PLI: %v", err)
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the keyframe request to arrive")
	}
}

// TestWriteVideoBeforeCall checks video written with no Call negotiated is
// refused rather than silently dropped.
func TestWriteVideoBeforeCall(t *testing.T) {
	certA, certB := newCertificate(t), newCertificate(t)
	a := start(t, transport.Options{Certificate: certA, Remote: certB.Fingerprint(), Initiator: true})
	if err := a.tr.WriteVideo([]byte{1, 2, 3}, media.VideoFrameDuration); err == nil {
		t.Fatal("video was accepted with no Call to send it to")
	}
}

// sendVideoUntilSeen writes frames from one side until the other sees one
// whole, for the same reason sendUntilHeard does: RTP loses what it likes,
// and the first frames of a stream may be written before the receiver has
// bound it.
func sendVideoUntilSeen(t *testing.T, from, to *end, frame []byte) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		if err := from.tr.WriteVideo(frame, media.VideoFrameDuration); err != nil {
			t.Fatalf("WriteVideo: %v", err)
		}
		select {
		case got := <-to.video:
			if !bytes.Equal(got, frame) {
				t.Fatalf("saw %d bytes, want the %d that were sent", len(got), len(frame))
			}
			return
		case err := <-to.down:
			t.Fatalf("Transport went down waiting for video: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for video to arrive")
		case <-time.After(media.VideoFrameDuration):
		}
	}
}
