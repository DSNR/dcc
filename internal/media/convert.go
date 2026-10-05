package media

import (
	"fmt"
	"image"
	"image/color"
)

// The pixel formats that meet at the video boundary. Cameras hand out YUYV —
// two pixels sharing one pair of chroma samples — VP8 wants I420, and both
// UIs paint RGBA. The conversions between them live here rather than in the
// drivers, and are exported for the same reason the audio codec is: they are
// part of the boundary a camera driver is written against, and they can be
// tested without a camera in front of them.

// NewPicture allocates an I420 Picture of the given size, planes packed with
// no padding. A camera driver converting into one starts here.
func NewPicture(width, height int) Picture {
	chromaW, chromaH := (width+1)/2, (height+1)/2
	return Picture{
		Width:   width,
		Height:  height,
		Y:       make([]byte, width*height),
		U:       make([]byte, chromaW*chromaH),
		V:       make([]byte, chromaW*chromaH),
		YStride: width,
		UStride: chromaW,
		VStride: chromaW,
	}
}

// fill paints the whole Picture one colour, which is what a test pattern is
// mostly made of.
func (p Picture) fill(c color.RGBA) {
	y, u, v := color.RGBToYCbCr(c.R, c.G, c.B)
	for row := range p.Height {
		line := p.Y[row*p.YStride:]
		for i := range p.Width {
			line[i] = y
		}
	}
	for row := range (p.Height + 1) / 2 {
		uLine, vLine := p.U[row*p.UStride:], p.V[row*p.VStride:]
		for i := range (p.Width + 1) / 2 {
			uLine[i], vLine[i] = u, v
		}
	}
}

// YUYVToI420 converts one YUYV (a.k.a. YUY2) camera frame into dst. YUYV
// carries full-rate luma and half-rate chroma on every line; I420 wants
// chroma at half the line count too, so every second line's is dropped —
// which is what every webcam pipeline does.
func YUYVToI420(src []byte, dst Picture) error {
	if want := dst.Width * dst.Height * 2; len(src) < want {
		return fmt.Errorf("media: a %dx%d YUYV frame needs %d bytes, got %d", dst.Width, dst.Height, want, len(src))
	}
	for row := range dst.Height {
		line := src[row*dst.Width*2:]
		luma := dst.Y[row*dst.YStride:]
		for i := range dst.Width {
			luma[i] = line[i*2]
		}
		if row%2 != 0 {
			continue
		}
		u, v := dst.U[(row/2)*dst.UStride:], dst.V[(row/2)*dst.VStride:]
		for i := range (dst.Width + 1) / 2 {
			u[i] = line[i*4+1]
			v[i] = line[i*4+3]
		}
	}
	return nil
}

// I420ToRGBA paints one I420 picture into an RGBA image of the same size,
// which is the shape both UIs hand to the GPU.
func I420ToRGBA(pic Picture, dst *image.RGBA) {
	for row := range pic.Height {
		luma := pic.Y[row*pic.YStride:]
		u := pic.U[(row/2)*pic.UStride:]
		v := pic.V[(row/2)*pic.VStride:]
		out := dst.Pix[row*dst.Stride:]
		for i := range pic.Width {
			r, g, b := color.YCbCrToRGB(luma[i], u[i/2], v[i/2])
			px := out[i*4:]
			px[0], px[1], px[2], px[3] = r, g, b, 0xFF
		}
	}
}

// RGBA is one picture as an RGBA image of its own, which is the shape both UIs
// paint — a decoded frame on the way in, a captured one on the way to a local
// preview. It allocates a frame at a time: the planes it reads are overwritten
// by the next frame, and a UI holding a picture that changed underneath it
// would tear.
func (p Picture) RGBA() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, p.Width, p.Height))
	I420ToRGBA(p, img)
	return img
}

// RGBAToI420 converts one RGBA image into dst, sampling to fit: dst may be
// smaller than the source, in which case pixels are taken at even intervals
// across it. That is what lets a screen far larger than anything a camera
// produces be shared at a size a pure-Go encoder can keep up with.
func RGBAToI420(src *image.RGBA, dst Picture) error {
	bounds := src.Bounds()
	if bounds.Empty() || dst.Width <= 0 || dst.Height <= 0 {
		return fmt.Errorf("media: cannot fit a %v image into a %dx%d picture",
			bounds, dst.Width, dst.Height)
	}
	width, height := bounds.Dx(), bounds.Dy()
	for row := range dst.Height {
		line := src.Pix[src.PixOffset(bounds.Min.X, bounds.Min.Y+row*height/dst.Height):]
		luma := dst.Y[row*dst.YStride:]
		// Chroma is half-resolution in both directions, so only every second
		// row writes it, and only every second pixel within that row.
		var u, v []byte
		if row%2 == 0 {
			u, v = dst.U[(row/2)*dst.UStride:], dst.V[(row/2)*dst.VStride:]
		}
		for i := range dst.Width {
			px := line[(i*width/dst.Width)*4:]
			y, cb, cr := color.RGBToYCbCr(px[0], px[1], px[2])
			luma[i] = y
			if u != nil && i%2 == 0 {
				u[i/2], v[i/2] = cb, cr
			}
		}
	}
	return nil
}
