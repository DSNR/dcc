package media_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/DSNR/dcc/internal/media"
)

// TestYUYVBecomesRGBA runs the camera's own conversion path — the YUYV a
// webcam delivers, through the I420 the encoder wants, to the RGBA a UI
// paints — and checks the colour survives it. Nothing here needs a camera,
// which is the point: the drivers are where the conversions are used, not
// where they can be exercised.
func TestYUYVBecomesRGBA(t *testing.T) {
	const width, height = 16, 8
	want := color.RGBA{R: 200, G: 40, B: 40, A: 0xFF}
	y, u, v := color.RGBToYCbCr(want.R, want.G, want.B)

	// YUYV: two luma samples share one pair of chroma samples.
	src := make([]byte, width*height*2)
	for i := 0; i+3 < len(src); i += 4 {
		src[i], src[i+1], src[i+2], src[i+3] = y, u, y, v
	}

	pic := media.NewPicture(width, height)
	if err := media.YUYVToI420(src, pic); err != nil {
		t.Fatalf("yuyvToI420: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	media.I420ToRGBA(pic, img)

	for at := 0; at+3 < len(img.Pix); at += 4 {
		got := color.RGBA{R: img.Pix[at], G: img.Pix[at+1], B: img.Pix[at+2], A: img.Pix[at+3]}
		if near(got.R, want.R) && near(got.G, want.G) && near(got.B, want.B) && got.A == 0xFF {
			continue
		}
		t.Fatalf("pixel %d came back as %v, want about %v", at/4, got, want)
	}
}

// TestYUYVTooShort checks a frame smaller than its own geometry is refused
// rather than read past — a driver that lies about its format should not take
// the process with it.
func TestYUYVTooShort(t *testing.T) {
	if err := media.YUYVToI420(make([]byte, 8), media.NewPicture(16, 8)); err == nil {
		t.Fatal("a short YUYV frame was accepted")
	}
}

// near allows the rounding a trip through YCbCr and back costs.
func near(got, want byte) bool {
	d := int(got) - int(want)
	return d < 3 && d > -3
}
