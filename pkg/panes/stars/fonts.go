// pkg/panes/stars/fonts.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"C"
	"image"
	"image/color"
	"runtime"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/nfnt/resize"
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

	doubleSTARSFont := func(sf STARSFont) STARSFont {
		for i := range sf.Glyphs {
			g := &sf.Glyphs[i]
			g.StepX *= 2
			g.Bounds[0] *= 2
			g.Bounds[1] *= 2

			// Generate a new bitmap with 2x as many
			// pixels. Fortunately the original bitmaps are all under
			// 16 pixels wide, so they will still fit in an uint32.
			var bitmap []uint32
			for _, line := range g.Bitmap {
				if line&0xffff != 0 {
					panic("not enough room in 32 bits")
				}

				// Horizontal doubling: double all of the set bits in
				// the line.
				var newLine uint32
				for b := 0; b < 32; b++ {
					// 0b_abcdefghijklmnop0000000000000000 ->
					// 0b_aabbccddeeffgghhiijjkkllmmnnoopp
					if line&(1<<(b/2+16)) != 0 {
						newLine |= 1 << b
					}
				}

				// Vertical doubling: add the line twice to the bitmap.
				bitmap = append(bitmap, newLine, newLine)
			}
			g.Bitmap = bitmap
		}
		return sf
	}

	if doublePixels {
		res *= 2
		for name, sf := range starsFonts {
			starsFonts[name] = doubleSTARSFont(sf)
		}
		starsCursors = doubleSTARSFont(starsCursors)
	}

	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: res, Y: res}})
	x, y := 0, 0

	var newFonts []*renderer.Font

	addFontToAtlas := func(fontName string, sf STARSFont) {
		id := renderer.FontIdentifier{
			Name: fontName,
			Size: util.Select(doublePixels, sf.Height/2, sf.Height),
		}

		f := renderer.MakeFont(sf.Height, true /* mono */, id, nil)
		newFonts = append(newFonts, f)

		if y+sf.Height >= res {
			panic("STARS font atlas texture too small")
		}

		for ch, glyph := range sf.Glyphs {
			if x+glyph.StepX+1 > res {
				// Start a new line
				x = 0
				y += sf.Height + 1
			}

			glyph.rasterize(atlas, x, y)
			glyph.addToFont(ch, x, y, res, f)

			x += glyph.StepX + 1 /* pad */
		}

		// Start a new line after finishing a font.
		x = 0
		y += sf.Height + 1

		if fontName == "sddCharFontSetBSize0" || fontName == "sddCharOutlineFontSetBSize0" {
			// Make a downscaled version of the smallest one for font size
			// 0 (which we don't seem to have a bitmap for...) Note that we
			// arguably should do this once in a preprocess and then encode
			// the result in starsFonts/starsOutlineFonts, but this doesn't
			// take too long and for now at least makes it easier to tweak
			// some of the details.
			sf.PointSize = 7
			const delta = 2
			sf.Width -= delta
			sf.Height -= delta

			id := renderer.FontIdentifier{
				Name: fontName,
				Size: util.Select(doublePixels, sf.Height/2, sf.Height),
			}
			f := renderer.MakeFont(sf.Height, true /* mono */, id, nil)
			newFonts = append(newFonts, f)

			for ch, glyph := range sf.Glyphs {
				if x+glyph.StepX+1 > res {
					// Start a new line in the atlas
					x = 0
					y += sf.Height + 1
				}

				// Rasterize each glyph into its own (small) image, which
				// we will then downscale. We could probably do this more
				// efficiently by putting them all into an image, zooming
				// that, and then copying it into the main font atlas, but
				// this way we don't have to worry about boundary
				// conditions and pixels spilling into other glyphs due to
				// the filter extent...
				img := image.NewRGBA(image.Rectangle{Max: image.Point{X: glyph.Bounds[0], Y: glyph.Bounds[1]}})

				glyph.rasterize(img, 0, 0)

				imgResized := resize.Resize(uint(glyph.Bounds[0]-delta), uint(glyph.Bounds[1]-delta), img, resize.MitchellNetravali)

				// Update the STARSGlyph for the zoom.
				glyph.Bounds[0] -= delta
				glyph.Bounds[1] -= delta
				glyph.StepX -= delta

				// Copy its pixels into the atlas.
				for yy := 0; yy < glyph.Bounds[1]; yy++ {
					for xx := 0; xx < glyph.Bounds[0]; xx++ {
						c := imgResized.At(xx, yy)
						r, g, b, a := c.RGBA()

						// The Mitchell-Netravali filter gives us a nicely
						// anti-aliased result, but we want something a
						// little more chunky to match the other STARS
						// fonts.  Therefore, we'll make a few adjustments
						// to the pixel values to try to get a result more
						// like that.
						sharpen := func(v uint32) uint16 {
							f := float32(v) / 0xffff
							// The sqrt pushes values toward up
							f = math.Sqrt(f)
							// And now we threshold to zero-out the smaller
							// values completely.
							if f < .6 {
								f = 0
							}
							// One last sqrt for more chunky.
							f = math.Sqrt(f)
							return uint16(math.Min(0xffff, f*0xffff))
						}

						sr, sg, sb, sa := sharpen(r), sharpen(g), sharpen(b), sharpen(a)
						atlas.Set(x+xx, y+yy, color.RGBA64{R: sr, G: sg, B: sb, A: sa})
					}
				}

				glyph.addToFont(ch, x, y, res, f)
				x += glyph.StepX + 1 /* pad */
			}

			x = 0
			y += sf.Height + 1
		}
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

func (glyph STARSGlyph) rasterize(img *image.RGBA, dx, dy int) {
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
				img.SetRGBA(x+dx, y+dy, on)
			}
		}
	}
}

func (glyph STARSGlyph) addToFont(ch, x, y, res int, f *renderer.Font) {
	g := &renderer.Glyph{
		X0:       0,
		X1:       float32(glyph.Bounds[0]),
		Y0:       0,
		Y1:       float32(glyph.Bounds[1]),
		U0:       float32(x) / float32(res),
		V0:       float32(y) / float32(res),
		U1:       (float32(x + glyph.Bounds[0])) / float32(res),
		V1:       (float32(y + glyph.Bounds[1])) / float32(res),
		AdvanceX: float32(glyph.StepX),
		Visible:  true,
	}
	f.AddGlyph(ch, g)
}
