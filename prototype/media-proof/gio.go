package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/op"
	"gioui.org/op/paint"
	"github.com/thesyncim/govpx"
)

// gio: decode a pre-encoded VP8 stream at 30 fps in a goroutine, hand RGBA
// frames to a Gio window, and count how many the window actually presented.
func runGio(args []string) {
	fs := flag.NewFlagSet("gio", flag.ExitOnError)
	dur := fs.Duration("dur", 15*time.Second, "run time")
	w := fs.Int("w", 640, "width")
	h := fs.Int("h", 480, "height")
	fs.Parse(args)

	fmt.Println("encoding 90-frame test stream...")
	pkts, _, err := encodeAll(synthFrames(*w, *h, 90), *w, *h, 30, 1000, 1, -6)
	if err != nil {
		log.Fatal(err)
	}
	dec, err := govpx.NewVP8Decoder(govpx.DecoderOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var mu sync.Mutex
	front := image.NewRGBA(image.Rect(0, 0, *w, *h))
	back := image.NewRGBA(image.Rect(0, 0, *w, *h))
	var produced, presented, lateDecode, skipped int
	var presentedSeq, lastPresentedSeq int
	decS, frameS := &stats{}, &stats{}

	win := new(app.Window)
	win.Option(app.Title("dcc media proof — VP8 640x480 @30"), app.Size(1000, 750))

	go func() {
		tick := time.NewTicker(time.Second / 30)
		defer tick.Stop()
		i := 0
		start := time.Now()
		for range tick.C {
			if time.Since(start) > *dur {
				break
			}
			if time.Since(start) < time.Second { // warm-up: window creation, first upload
				mu.Lock()
				produced, presented, skipped, lateDecode, presentedSeq, lastPresentedSeq = 0, 0, 0, 0, 0, 0
				decS.d, frameS.d = nil, nil
				mu.Unlock()
			}
			t := time.Now()
			if i == 0 {
				dec.Reset()
			}
			if err := dec.Decode(pkts[i]); err != nil {
				log.Fatal("decode:", err)
			}
			fr, _ := dec.NextFrame()
			i420ToRGBA(fr, back)
			d := time.Since(t)
			decS.add(d)
			if d > 33*time.Millisecond {
				lateDecode++
			}
			mu.Lock()
			if produced > presentedSeq { // previous frame never shown
				skipped++
			}
			front, back = back, front
			produced++
			mu.Unlock()
			win.Invalidate()
			i = (i + 1) % len(pkts)
		}
		mu.Lock()
		fmt.Printf("\ndecode+convert per frame: %s\n", decS)
		fmt.Printf("frame events: %s\n", frameS)
		fmt.Printf("produced %d, presented %d, decode>33ms %d, frames overwritten before present %d over %s (%.1f fps presented)\n",
			produced, presented, lateDecode, skipped, *dur, float64(presented)/dur.Seconds())
		verdict("Gio 30 fps VP8 render without drops", skipped <= produced/100 && lateDecode == 0,
			fmt.Sprintf("%.1f fps presented, %d skipped, %d late decodes", float64(presented)/dur.Seconds(), skipped, lateDecode))
		mu.Unlock()
		os.Exit(0)
	}()

	go func() {
		var ops op.Ops
		var last time.Time
		for {
			switch e := win.Event().(type) {
			case app.DestroyEvent:
				os.Exit(0)
			case app.FrameEvent:
				if !last.IsZero() {
					frameS.add(time.Since(last))
				}
				last = time.Now()
				ops.Reset()
				mu.Lock()
				img := front
				if produced != lastPresentedSeq {
					presented++
					lastPresentedSeq = produced
					presentedSeq = produced
				}
				mu.Unlock()
				sx := float32(e.Size.X) / float32(img.Bounds().Dx())
				sy := float32(e.Size.Y) / float32(img.Bounds().Dy())
				if sy < sx {
					sx = sy
				}
				op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(sx, sx))).Add(&ops)
				paint.NewImageOp(img).Add(&ops) // one upload per frame
				paint.PaintOp{}.Add(&ops)
				e.Frame(&ops)
			}
		}
	}()
	app.Main()
}
