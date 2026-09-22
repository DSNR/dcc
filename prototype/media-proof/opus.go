package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"time"

	"github.com/pion/opus"
)

const opusRate = 48000
const opusFrame = 960 // 20 ms @ 48 kHz

func sine(n int, hz float64, amp float64, phase0 int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * 32767 * math.Sin(2*math.Pi*hz*float64(i+phase0)/opusRate))
	}
	return out
}

func int16le(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func runOpus(args []string) {
	fs := flag.NewFlagSet("opus", flag.ExitOnError)
	kbps := fs.Int("kbps", 32, "bitrate kbps")
	secs := fs.Int("secs", 2, "seconds of tone")
	fs.Parse(args)

	enc, err := opus.NewEncoder(opus.WithSampleRate(opusRate), opus.WithChannels(1), opus.WithBitrate(*kbps*1000),
		opus.WithApplication(opus.ApplicationVoIP))
	if err != nil {
		panic(err)
	}
	dec, err := opus.NewDecoderWithOutput(opusRate, 1)
	if err != nil {
		panic(err)
	}
	src := sine(opusRate**secs, 440, 0.5, 0)
	var got []int16
	pkt := make([]byte, 4000)
	out := make([]int16, opusFrame*3)
	est, dst := &stats{}, &stats{}
	var bytesTotal int
	for i := 0; i+opusFrame <= len(src); i += opusFrame {
		t := time.Now()
		n, err := enc.Encode(int16le(src[i:i+opusFrame]), pkt)
		est.add(time.Since(t))
		if err != nil {
			fmt.Println("encode:", err)
			return
		}
		bytesTotal += n
		t = time.Now()
		m, err := dec.DecodeToInt16(pkt[:n], out)
		dst.add(time.Since(t))
		if err != nil {
			fmt.Println("decode:", err)
			return
		}
		got = append(got, out[:m]...)
	}
	fmt.Printf("pion/opus CELT mono 48k 20ms @ %d kbps: %d packets, avg %d bytes/pkt (%.1f kbps actual)\n",
		*kbps, len(src)/opusFrame, bytesTotal/(len(src)/opusFrame), float64(bytesTotal*8)/float64(*secs)/1000)
	fmt.Printf("  encode: %s\n  decode: %s\n", est, dst)

	// align (codec delay) and compute SNR on the steady-state middle.
	bestLag, bestSNR := 0, -1e9
	for lag := 0; lag < 2000; lag++ {
		snr := snrDB(src[opusRate/2:opusRate/2+opusRate], got[opusRate/2+lag:opusRate/2+lag+opusRate])
		if snr > bestSNR {
			bestSNR, bestLag = snr, lag
		}
	}
	fmt.Printf("  round-trip: lag %d samples (%.1f ms), SNR %.1f dB, decoded %d samples for %d in\n",
		bestLag, float64(bestLag)*1000/opusRate, bestSNR, len(got), len(src))
	verdict("Opus encode/decode round trip", bestSNR > 15, fmt.Sprintf("SNR %.1f dB (tone survives; >15 dB is fine for 32 kbps CELT)", bestSNR))
}

func snrDB(a, b []int16) float64 {
	var sig, noise float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		sig += x * x
		noise += (x - y) * (x - y)
	}
	if noise == 0 {
		return 200
	}
	return 10 * math.Log10(sig/noise)
}
