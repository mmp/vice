// pkg/panes/stars/fonts.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"C"
	"image"
	"image/color"
	"runtime"

	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

func createFontAtlas(r renderer.Renderer, p platform.Platform) []*renderer.Font {
	// See stars-fonts.go (which is automatically-generated) for the
	// definition of starsFonts, which stores the bitmaps and additional
	// information about the glyphs in the STARS fonts.

	// We'll extract the font bitmaps into an atlas image; assume 1k x 1k for starters.
	res := 1024

	// Windows high DPI displays are different than Macs in that they
	// expose the actual pixel count.  So we need to scale the font atlas
	// accordingly. Here we just double up pixels since we want to maintain
	// the realistic chunkiness of the original fonts.
	doublePixels := runtime.GOOS == "windows" && p.DPIScale() > 1.5
	scale := 1

	if doublePixels {
		res *= 2
		scale = 2
	}

	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: res, Y: res}})
	x, y := 0, 0

	var newFonts []*renderer.Font

	addFontToAtlas := func(fontName string, sf STARSFont) {
		id := renderer.FontIdentifier{
			Name: fontName,
			Size: sf.Height,
		}

		f := renderer.MakeFont(scale*sf.Height, true /* mono */, id, nil)
		newFonts = append(newFonts, f)

		if y+scale*sf.Height >= res {
			panic("STARS font atlas texture too small")
		}

		for ch, glyph := range sf.Glyphs {
			if x+scale*glyph.StepX+1 > res {
				// Start a new line
				x = 0
				y += scale*sf.Height + 1
			}

			glyph.rasterize(atlas, x, y, scale)
			glyph.addToFont(ch, x, y, res, f, scale)

			x += scale*glyph.StepX + 1 /* pad */
		}

		// Start a new line after finishing a font.
		x = 0
		y += scale*sf.Height + 1
	}

	// Iterate over the fonts, create Font/Glyph objects for them, and copy
	// their bitmaps into the atlas image.
	for _, fontName := range util.SortedMapKeys(starsFonts) { // consistent order
		addFontToAtlas(fontName, starsFonts[fontName])
	}
	addFontToAtlas("STARS cursors", starsCursors)

	atlasId := r.CreateTextureFromImage(atlas, true /* nearest filter */)
	for _, font := range newFonts {
		font.TexId = atlasId
	}

	return newFonts
}

func (glyph STARSGlyph) rasterize(img *image.RGBA, x0, y0 int, scale int) {
	// STARSGlyphs store their bitmaps as an array of uint32s, where each
	// uint32 encodes a scanline and bits are set in it to indicate that
	// the corresponding pixel should be drawn; thus, there are no
	// intermediate values for anti-aliasing.
	for y, line := range glyph.Bitmap {
		for x := 0; x < glyph.Bounds[0]; x++ {
			// The high bit corresponds to the first pixel in the scanline,
			// so the bitmask is set up accordingly...
			mask := uint32(1 << (31 - x))
			if line&mask != 0 {
				on := color.RGBA{R: 255, G: 255, B: 255, A: 255}
				for dy := range scale {
					for dx := range scale {
						img.SetRGBA(x0+scale*x+dx, y0+scale*y+dy, on)
					}
				}
			}
		}
	}
}

func (glyph STARSGlyph) addToFont(ch, x, y, res int, f *renderer.Font, scale int) {
	g := &renderer.Glyph{
		X0:       0,
		X1:       float32(scale * glyph.Bounds[0]),
		Y0:       0,
		Y1:       float32(scale * glyph.Bounds[1]),
		U0:       float32(x) / float32(res),
		V0:       float32(y) / float32(res),
		U1:       (float32(x + scale*glyph.Bounds[0])) / float32(res),
		V1:       (float32(y + scale*glyph.Bounds[1])) / float32(res),
		AdvanceX: float32(scale * glyph.StepX),
		Visible:  true,
	}
	f.AddGlyph(ch, g)
}
