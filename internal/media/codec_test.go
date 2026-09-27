package media_test

import (
	"math"
	"testing"

	"github.com/DSNR/dcc/internal/media"
)

// roundTrip runs pcm through the codec a frame at a time, the way a Call does,
// and returns what came back out.
func roundTrip(t *testing.T, pcm []int16) []int16 {
	t.Helper()
	enc, dec := media.NewEncoder(), media.NewDecoder()
	payload := make([]byte, media.PayloadBytes)
	frame := make([]int16, media.FrameSamples)
	out := make([]int16, 0, len(pcm))
	for f := 0; f+media.FrameSamples <= len(pcm); f += media.FrameSamples {
		encoded := enc.Encode(pcm[f:f+media.FrameSamples], payload)
		if len(encoded) != media.PayloadBytes {
			t.Fatalf("encoded %d bytes, want %d", len(encoded), media.PayloadBytes)
		}
		decoded := dec.Decode(encoded, frame)
		if len(decoded) != media.FrameSamples {
			t.Fatalf("decoded %d samples, want %d", len(decoded), media.FrameSamples)
		}
		out = append(out, decoded...)
	}
	return out
}

// tone is amplitude at freq, a second of it.
func tone(freq float64, amplitude float64) []int16 {
	pcm := make([]int16, media.SampleRate)
	for i := range pcm {
		pcm[i] = int16(amplitude * math.Sin(2*math.Pi*freq*float64(i)/media.SampleRate))
	}
	return pcm
}

// signalToNoise is how far the codec's error sits below the signal, in
// decibels, at the delay that lines the two up: the codec's filters delay what
// they carry, and a delay is not an error.
func signalToNoise(t *testing.T, want, got []int16) float64 {
	t.Helper()
	best := math.Inf(-1)
	for delay := range 48 {
		var signal, noise float64
		// The first samples are the filters filling up, which is not what is
		// being measured.
		for i := 400; i < len(want) && i+delay < len(got); i++ {
			d := float64(want[i]) - float64(got[i+delay])
			signal += float64(want[i]) * float64(want[i])
			noise += d * d
		}
		if noise == 0 {
			return math.Inf(1)
		}
		best = math.Max(best, 10*math.Log10(signal/noise))
	}
	return best
}

// TestCodecSilence pins the one round trip that has to be quiet: silence in,
// silence out. G.722's quantiser has no exact zero — its smallest step either
// side of the prediction is what silence codes as — so the bar is inaudible
// rather than identical, which is 60 dB below anything anyone speaks at.
func TestCodecSilence(t *testing.T) {
	for i, got := range roundTrip(t, make([]int16, media.FrameSamples*10)) {
		if got > 32 || got < -32 {
			t.Fatalf("silence came back as %d at sample %d", got, i)
		}
	}
}

// TestCodecRoundTrip runs a sine through the codec at each end of the band and
// checks what comes back is the same wave. G.722 spends six bits a sample on
// everything below 4 kHz and two on everything above, so the two halves of the
// band are held to different bars — the high one is coarse by design, not by
// accident.
func TestCodecRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		freq, floor float64
	}{
		{300, 30},
		{1000, 30},
		{2000, 30},
		{3000, 30},
		{3400, 25},
		// Above 4 kHz is the band µ-law at 8 kHz could not carry at all: the
		// whole point of the codec change is that these are audible.
		{5000, 15},
		{6500, 15},
	} {
		want := tone(tc.freq, 11000)
		if snr := signalToNoise(t, want, roundTrip(t, want)); snr < tc.floor {
			t.Errorf("%.0f Hz came back %.1f dB above the noise, want at least %.0f", tc.freq, snr, tc.floor)
		}
	}
}

// TestCodecWideband is the quality claim the codec change exists for, as a
// number: a tone in the 4–8 kHz octave survives the round trip, where the
// 8 kHz µ-law this replaced could not represent it at all — it would have come
// back as its own alias, or as nothing.
func TestCodecWideband(t *testing.T) {
	const freq = 6000
	got := roundTrip(t, tone(freq, 11000))
	// Goertzel at the tone against a frequency nobody sent: the tone is still
	// there, and it is still that tone.
	heard := &media.Recorder{}
	if err := heard.Write(got); err != nil {
		t.Fatalf("Write: %v", err)
	}
	at, off := heard.Power(freq), heard.Power(freq/2)
	if at < 10*off {
		t.Fatalf("heard %.1f at %.0f Hz against %.1f at %.0f Hz — the wideband half of the band did not survive",
			at, float64(freq), off, freq/2.0)
	}
}

// TestCodecExtremes checks the ends of the range clamp rather than wrap — a
// wrapped full-scale sample is a loud click — and that a frame of them still
// comes back as the wave it was.
func TestCodecExtremes(t *testing.T) {
	want := tone(400, 32767)
	got := roundTrip(t, want)
	if snr := signalToNoise(t, want, got); snr < 25 {
		t.Errorf("a full-scale tone came back %.1f dB above the noise, want at least 25", snr)
	}
	var peak int16
	for _, s := range got {
		if s > peak {
			peak = s
		}
	}
	if peak < 30000 {
		t.Errorf("a full-scale tone peaked at %d, nowhere near full scale", peak)
	}
}

// TestCodecStreamState proves the codec is a stream and not a series of
// frames: encoding the same audio in one Encoder's lifetime gives the same
// bytes every time, and a fresh Encoder starts over. A codec that leaked
// state between Calls would sound wrong for the first few frames of the
// second one.
func TestCodecStreamState(t *testing.T) {
	pcm := tone(1000, 11000)[:media.FrameSamples*4]
	first := roundTrip(t, pcm)
	second := roundTrip(t, pcm)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("a fresh Encoder and Decoder disagreed at sample %d: %d against %d", i, first[i], second[i])
		}
	}
}

// TestCodecConceal proves a concealed frame is silence of the right length —
// what the receive side plays where a frame never arrived.
func TestCodecConceal(t *testing.T) {
	dec := media.NewDecoder()
	got := dec.Conceal(make([]int16, media.FrameSamples))
	if len(got) != media.FrameSamples {
		t.Fatalf("concealed %d samples, want %d", len(got), media.FrameSamples)
	}
	for i, s := range got {
		if s != 0 {
			t.Fatalf("concealment is not silent: %d at sample %d", s, i)
		}
	}
}
