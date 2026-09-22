package main

import (
	"flag"
	"fmt"
	"image"
	"runtime"
	"time"

	"github.com/kbinani/screenshot"
	"golang.org/x/image/draw"
)

// captureScaled grabs display 0 and downscales to targetW wide (aspect kept).
func captureScaled(targetW int, dst *image.RGBA) (*image.RGBA, time.Duration, time.Duration, error) {
	t := time.Now()
	shot, err := screenshot.CaptureDisplay(0)
	if err != nil {
		return nil, 0, 0, err
	}
	capD := time.Since(t)
	t = time.Now()
	th := shot.Bounds().Dy() * targetW / shot.Bounds().Dx()
	if dst == nil || dst.Bounds().Dx() != targetW || dst.Bounds().Dy() != th {
		dst = image.NewRGBA(image.Rect(0, 0, targetW, th))
	}
	if *useXDraw {
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), shot, shot.Bounds(), draw.Src, nil)
	} else {
		box2(dst, shot)
	}
	return dst, capD, time.Since(t), nil
}

var useXDraw = new(bool)

// box2 samples a 2x2 block at the mapped source position for every dst
// pixel (a cheap area filter; fine for any ratio >= 1).
func box2(dst, src *image.RGBA) {
	w, h := dst.Bounds().Dx(), dst.Bounds().Dy()
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	xs := make([]int, w)
	for x := range xs {
		xs[x] = min(x*sw/w, sw-2) * 4
	}
	for y := 0; y < h; y++ {
		sy := min(y*sh/h, sh-2)
		r0 := src.Pix[sy*src.Stride:]
		r1 := src.Pix[(sy+1)*src.Stride:]
		d := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			i := xs[x]
			o := x * 4
			d[o] = byte((int(r0[i]) + int(r0[i+4]) + int(r1[i]) + int(r1[i+4])) >> 2)
			d[o+1] = byte((int(r0[i+1]) + int(r0[i+5]) + int(r1[i+1]) + int(r1[i+5])) >> 2)
			d[o+2] = byte((int(r0[i+2]) + int(r0[i+6]) + int(r1[i+2]) + int(r1[i+6])) >> 2)
			d[o+3] = 255
		}
	}
}

func runScreen(args []string) {
	fs := flag.NewFlagSet("screen", flag.ExitOnError)
	dur := fs.Duration("dur", 10*time.Second, "run time")
	fps := fs.Int("fps", 10, "capture rate")
	width := fs.Int("w", 1280, "downscale width")
	useXDraw = fs.Bool("xdraw", false, "use x/image/draw ApproxBiLinear instead of the 2x2 box sampler")
	fs.Parse(args)

	n := screenshot.NumActiveDisplays()
	fmt.Printf("kbinani/screenshot on %s: %d display(s)", runtime.GOOS, n)
	if n == 0 {
		fmt.Println(" — none found")
		verdict("screen capture", false, "no displays")
		return
	}
	b := screenshot.GetDisplayBounds(0)
	fmt.Printf(", display 0 = %dx%d\n", b.Dx(), b.Dy())

	var dst *image.RGBA
	capS, sclS := &stats{}, &stats{}
	var busy time.Duration
	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(*fps))
	defer tick.Stop()
	for time.Since(start) < *dur {
		<-tick.C
		var c, s time.Duration
		var err error
		dst, c, s, err = captureScaled(*width, dst)
		if err != nil {
			fmt.Println("capture error:", err)
			verdict("screen capture", false, err.Error())
			return
		}
		capS.add(c)
		sclS.add(s)
		busy += c + s
	}
	wall := time.Since(start)
	pct := 100 * float64(busy) / float64(wall)
	fmt.Printf("  capture:   %s\n  downscale: %s\n  busy %.1f%% of one core at %d fps -> %dx%d\n",
		capS, sclS, pct, *fps, dst.Bounds().Dx(), dst.Bounds().Dy())
	verdict("screen 10 fps capture+downscale < 30% core", pct < 30, fmt.Sprintf("%.1f%%", pct))
}
