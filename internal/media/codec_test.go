package media_test

import (
	"math"
	"testing"

	"github.com/DSNR/dcc/internal/media"
)

// TestCodecSilence pins the one round trip that has to be exact: silence in,
// silence out. A codec that adds a DC offset would be audible on every Call.
func TestCodecSilence(t *testing.T) {
	pcm := make([]int16, media.FrameSamples)
	payload := media.Encode(pcm, make([]byte, media.FrameSamples))
	if len(payload) != media.FrameSamples {
		t.Fatalf("encoded %d bytes, want %d", len(payload), media.FrameSamples)
	}
	for i, got := range media.Decode(payload, make([]int16, len(payload))) {
		if got != 0 {
			t.Fatalf("silence came back as %d at sample %d", got, i)
		}
	}
}

// TestCodecRoundTrip runs a sine through the codec and checks what comes
// back is the same wave: µ-law is lossy, so the bar is the quantisation
// error it is specified to have, not equality.
func TestCodecRoundTrip(t *testing.T) {
	const amplitude = 12000
	pcm := make([]int16, media.FrameSamples)
	for i := range pcm {
		pcm[i] = int16(amplitude * math.Sin(2*math.Pi*440*float64(i)/media.SampleRate))
	}

	got := media.Decode(media.Encode(pcm, make([]byte, len(pcm))), make([]int16, len(pcm)))
	if len(got) != len(pcm) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(pcm))
	}
	var worst float64
	for i := range pcm {
		if (pcm[i] < 0) != (got[i] < 0) && pcm[i] != 0 && got[i] != 0 {
			t.Fatalf("sample %d flipped sign: %d became %d", i, pcm[i], got[i])
		}
		worst = math.Max(worst, math.Abs(float64(got[i]-pcm[i])))
	}
	// µ-law's step at this amplitude is a few hundred; anything near the
	// signal itself means the segment maths is wrong.
	if worst > amplitude/16 {
		t.Fatalf("worst error is %.0f, more than the codec is allowed", worst)
	}
}

// TestCodecExtremes checks the ends of the range clamp rather than wrap — a
// wrapped full-scale sample is a loud click.
func TestCodecExtremes(t *testing.T) {
	for _, sample := range []int16{math.MinInt16, math.MaxInt16} {
		got := media.Decode(media.Encode([]int16{sample}, make([]byte, 1)), make([]int16, 1))[0]
		if (sample < 0) != (got < 0) {
			t.Fatalf("%d came back as %d, on the wrong side of zero", sample, got)
		}
		if math.Abs(float64(got)) < 30000 {
			t.Fatalf("%d came back as %d, nowhere near full scale", sample, got)
		}
	}
}
