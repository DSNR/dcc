package main

import (
	"flag"
	"fmt"
	"image"
	"runtime"
	"time"

	"github.com/thesyncim/govpx"
)

func newEncoder(w, h, fps, kbps, threads, cpuUsed int) (*govpx.VP8Encoder, error) {
	return govpx.NewVP8Encoder(govpx.EncoderOptions{
		Width: w, Height: h, FPS: fps, Threads: threads,
		RateControlMode: govpx.RateControlCBR, TargetBitrateKbps: kbps,
		Deadline: govpx.DeadlineRealtime, CpuUsed: cpuUsed,
		KeyFrameInterval: 3000, ErrorResilient: true, DropFrameAllowed: false,
	})
}

func encodeAll(frames []govpx.Image, w, h, fps, kbps, threads, cpuUsed int) ([][]byte, *stats, error) {
	enc, err := newEncoder(w, h, fps, kbps, threads, cpuUsed)
	if err != nil {
		return nil, nil, err
	}
	defer enc.Close()
	buf := make([]byte, 1<<20)
	var pkts [][]byte
	st := &stats{}
	for i, f := range frames {
		t := time.Now()
		res, err := enc.EncodeInto(buf, f, uint64(i), 1, 0)
		st.add(time.Since(t))
		if err != nil {
			return nil, nil, fmt.Errorf("frame %d: %w", i, err)
		}
		if !res.Dropped && len(res.Data) > 0 {
			pkts = append(pkts, append([]byte(nil), res.Data...))
		}
	}
	return pkts, st, nil
}

func runVP8(args []string) {
	fs := flag.NewFlagSet("vp8", flag.ExitOnError)
	w := fs.Int("w", 640, "width")
	h := fs.Int("h", 480, "height")
	n := fs.Int("n", 300, "frames")
	kbps := fs.Int("kbps", 1000, "target bitrate")
	fs.Parse(args)

	fmt.Printf("govpx VP8 %dx%d, %d frames, CBR %d kbps, realtime. GOMAXPROCS=%d %s/%s\n",
		*w, *h, *n, *kbps, runtime.GOMAXPROCS(0), runtime.GOOS, runtime.GOARCH)
	frames := synthFrames(*w, *h, *n)

	best := time.Duration(1 << 62)
	var pkts [][]byte
	for _, cfg := range [][2]int{{1, 0}, {1, -6}, {4, -6}} {
		p, st, err := encodeAll(frames, *w, *h, 30, *kbps, cfg[0], cfg[1])
		if err != nil {
			fmt.Println("encode error:", err)
			continue
		}
		var total int
		for _, x := range p {
			total += len(x)
		}
		fmt.Printf("  encode threads=%d cpuUsed=%d: %s  out %.0f kbps\n", cfg[0], cfg[1], st, float64(total*8*30)/float64(len(p))/1000)
		c := append([]time.Duration(nil), st.d...)
		var sum time.Duration
		for _, d := range c {
			sum += d
		}
		avg := sum / time.Duration(len(c))
		if avg < best {
			best, pkts = avg, p
		}
	}

	dec, err := govpx.NewVP8Decoder(govpx.DecoderOptions{})
	if err != nil {
		panic(err)
	}
	defer dec.Close()
	rgba := image.NewRGBA(image.Rect(0, 0, *w, *h))
	dst, cst := &stats{}, &stats{}
	for i, p := range pkts {
		t := time.Now()
		if err := dec.Decode(p); err != nil {
			fmt.Printf("decode error frame %d: %v\n", i, err)
			return
		}
		fr, ok := dec.NextFrame()
		dst.add(time.Since(t))
		if !ok {
			continue
		}
		t = time.Now()
		i420ToRGBA(fr, rgba)
		cst.add(time.Since(t))
	}
	fmt.Printf("  decode:      %s\n", dst)
	fmt.Printf("  i420->rgba:  %s\n", cst)
	verdict("VP8 encode < 20 ms/frame (best config)", best < 20*time.Millisecond, fmt.Sprintf("best avg %.2f ms", ms(best)))
}
