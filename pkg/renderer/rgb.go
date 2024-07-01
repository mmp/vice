package renderer

import (
	"image"
	"image/color"
	"image/draw"

	"github.com/mmp/vice/pkg/math"
)

///////////////////////////////////////////////////////////////////////////
// RGB

type RGB struct {
	R, G, B float32
}

type RGBA struct {
	R, G, B, A float32
}

func LerpRGB(x float32, a, b RGB) RGB {
	return RGB{R: math.Lerp(x, a.R, b.R), G: math.Lerp(x, a.G, b.G), B: math.Lerp(x, a.B, b.B)}
}

func (r RGB) Equals(other RGB) bool {
	return r.R == other.R && r.G == other.G && r.B == other.B
}

func (r RGB) Scale(v float32) RGB {
	return RGB{R: r.R * v, G: r.G * v, B: r.B * v}
}

// RGBFromHex converts a packed integer color value to an RGB where the low
// 8 bits give blue, the next 8 give green, and then the next 8 give red.
func RGBFromHex(c int) RGB {
	r, g, b := (c>>16)&255, (c>>8)&255, c&255
	return RGB{R: float32(r) / 255, G: float32(g) / 255, B: float32(b) / 255}
}

///////////////////////////////////////////////////////////////////////////
// Image processing

func GenerateImagePyramid(img image.Image) []image.Image {
	var pyramid []image.Image

	// We always work with image.RGBA in the following..
	nx, ny := img.Bounds().Dx(), img.Bounds().Dy()
	prevLevel, ok := img.(*image.RGBA)
	if !ok {
		prevLevel = image.NewRGBA(image.Rect(0, 0, nx, ny))
		draw.Draw(prevLevel, prevLevel.Bounds(), img, img.Bounds().Min, draw.Src)
	}
	pyramid = append(pyramid, prevLevel)

	for nx != 1 || ny != 1 {
		ox, oy := nx, ny
		nx, ny = math.Max(nx/2, 1), math.Max(ny/2, 1)

		next := make([]uint8, nx*ny*4)
		lookup := func(x, y int) color.RGBA {
			if x > ox-1 {
				x = ox - 1
			}
			if y > oy-1 {
				y = oy - 1
			}
			offset := 4*x + prevLevel.Stride*y
			return color.RGBA{
				R: prevLevel.Pix[offset],
				G: prevLevel.Pix[offset+1],
				B: prevLevel.Pix[offset+2],
				A: prevLevel.Pix[offset+3]}
		}
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				v := [4]color.RGBA{lookup(2*x, 2*y), lookup(2*x+1, 2*y), lookup(2*x, 2*y+1), lookup(2*x+1, 2*y+1)}

				// living large with a box filter
				next[4*(x+y*nx)+0] = uint8((int(v[0].R) + int(v[1].R) + int(v[2].R) + int(v[3].R) + 2) / 4)
				next[4*(x+y*nx)+1] = uint8((int(v[0].G) + int(v[1].G) + int(v[2].G) + int(v[3].G) + 2) / 4)
				next[4*(x+y*nx)+2] = uint8((int(v[0].B) + int(v[1].B) + int(v[2].B) + int(v[3].B) + 2) / 4)
				next[4*(x+y*nx)+3] = uint8((int(v[0].A) + int(v[1].A) + int(v[2].A) + int(v[3].A) + 2) / 4)
			}
		}

		nextLevel := &image.RGBA{
			Pix:    next,
			Stride: 4 * nx,
			Rect:   image.Rectangle{Max: image.Point{X: nx, Y: ny}}}
		pyramid = append(pyramid, nextLevel)
		prevLevel = nextLevel
	}

	return pyramid
}
