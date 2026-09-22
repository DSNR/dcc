// PROTOTYPE — throwaway harness for dcc issue #16 (pure-Go media proof).
// Not production code. See README.md.
package main

import (
	"fmt"
	"image"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/thesyncim/govpx"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: mediaproof <vp8|gio|opus|screen|audio|browser> [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "vp8":
		runVP8(args)
	case "gio":
		runGio(args)
	case "opus":
		runOpus(args)
	case "screen":
		runScreen(args)
	case "audio":
		runAudio(args)
	case "browser":
		runBrowser(args)
	default:
		fmt.Println("unknown command", cmd)
		os.Exit(2)
	}
}

// ---- synthetic I420 test content -------------------------------------------

func newI420(w, h int) govpx.Image {
	cw, ch := (w+1)/2, (h+1)/2
	return govpx.Image{Width: w, Height: h,
		Y: make([]byte, w*h), U: make([]byte, cw*ch), V: make([]byte, cw*ch),
		YStride: w, UStride: cw, VStride: cw}
}

// synthFrames builds n frames of "talking head"-ish content: scrolling
// gradient, moving blocks, mild noise so the encoder does real work.
func synthFrames(w, h, n int) []govpx.Image {
	rng := rand.New(rand.NewSource(1))
	out := make([]govpx.Image, n)
	for i := range out {
		img := newI420(w, h)
		bx, by := (i*7)%(w-80), (i*5)%(h-80)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := byte((x+y+i*3)/4) + byte(rng.Intn(6))
				if x >= bx && x < bx+80 && y >= by && y < by+80 {
					v = 235
				}
				img.Y[y*w+x] = v
			}
		}
		cw := img.UStride
		for y := 0; y < (h+1)/2; y++ {
			for x := 0; x < cw; x++ {
				img.U[y*cw+x] = byte(128 + (x*255/cw-128)/2 + i)
				img.V[y*cw+x] = byte(128 + (y*255/((h+1)/2)-128)/2 - i)
			}
		}
		out[i] = img
	}
	return out
}

// ---- I420 <-> RGBA (BT.601 limited range, integer) --------------------------

func i420ToRGBA(src govpx.Image, dst *image.RGBA) {
	w, h := src.Width, src.Height
	if dst.Rect.Dx() != w || dst.Rect.Dy() != h {
		*dst = *image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		yrow := src.Y[y*src.YStride:]
		urow := src.U[(y/2)*src.UStride:]
		vrow := src.V[(y/2)*src.VStride:]
		drow := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			c := (int(yrow[x]) - 16) * 298
			d := int(urow[x/2]) - 128
			e := int(vrow[x/2]) - 128
			r := (c + 409*e + 128) >> 8
			g := (c - 100*d - 208*e + 128) >> 8
			b := (c + 516*d + 128) >> 8
			drow[x*4] = clamp(r)
			drow[x*4+1] = clamp(g)
			drow[x*4+2] = clamp(b)
			drow[x*4+3] = 255
		}
	}
}

func rgbaToI420(src *image.RGBA, dst govpx.Image) {
	w, h := dst.Width, dst.Height
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := src.Pix[y*src.Stride+x*4:]
			r, g, b := int(p[0]), int(p[1]), int(p[2])
			dst.Y[y*dst.YStride+x] = byte(((66*r + 129*g + 25*b + 128) >> 8) + 16)
			if y%2 == 0 && x%2 == 0 {
				dst.U[(y/2)*dst.UStride+x/2] = byte(((-38*r - 74*g + 112*b + 128) >> 8) + 128)
				dst.V[(y/2)*dst.VStride+x/2] = byte(((112*r - 94*g - 18*b + 128) >> 8) + 128)
			}
		}
	}
}

func clamp(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// ---- timing stats -----------------------------------------------------------

type stats struct{ d []time.Duration }

func (s *stats) add(d time.Duration) { s.d = append(s.d, d) }
func (s *stats) String() string {
	if len(s.d) == 0 {
		return "n/a"
	}
	c := append([]time.Duration(nil), s.d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	var sum time.Duration
	for _, d := range c {
		sum += d
	}
	return fmt.Sprintf("avg %.2fms  p50 %.2fms  p95 %.2fms  max %.2fms  (n=%d)",
		ms(sum/time.Duration(len(c))), ms(c[len(c)/2]), ms(c[len(c)*95/100]), ms(c[len(c)-1]), len(c))
}
func ms(d time.Duration) float64 { return float64(d) / 1e6 }

func verdict(name string, pass bool, detail string) {
	tag := "PASS"
	if !pass {
		tag = "FAIL"
	}
	fmt.Printf("\n[%s] %s — %s\n", tag, name, detail)
}
