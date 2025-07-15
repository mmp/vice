// pkg/panes/stars/fonts.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"C"
	"image"
	"image/color"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

func (sp *STARSPane) initializeFonts(r renderer.Renderer, p platform.Platform) {
	fonts := createFontAtlas(r, p)
	get := func(name string, size int) *renderer.Font {
		idx := slices.IndexFunc(fonts, func(f *renderer.Font) bool { return f.Id.Name == name && f.Id.Size == size })
		if idx == -1 {
			panic(name + " size " + strconv.Itoa(size) + " not found in STARS fonts")
		}
		return fonts[idx]
	}

	sp.systemFontA[0] = get("sddCharFontSetASize0", 9)
	sp.systemFontA[1] = get("sddCharFontSetASize1", 14)
	sp.systemFontA[2] = get("sddCharFontSetASize2", 17)
	sp.systemFontA[3] = get("sddCharFontSetASize3", 20)
	sp.systemFontA[4] = get("sddCharFontSetASize4", 21)
	sp.systemFontA[5] = get("sddCharFontSetASize5", 23)
	sp.systemOutlineFontA[0] = get("sddCharOutlineFontSetASize0", 9)
	sp.systemOutlineFontA[1] = get("sddCharOutlineFontSetASize1", 14)
	sp.systemOutlineFontA[2] = get("sddCharOutlineFontSetASize2", 17)
	sp.systemOutlineFontA[3] = get("sddCharOutlineFontSetASize3", 20)
	sp.systemOutlineFontA[4] = get("sddCharOutlineFontSetASize4", 21)
	sp.systemOutlineFontA[5] = get("sddCharOutlineFontSetASize5", 23)
	sp.dcbFontA[0] = get("sddCharFontSetASize0", 9)
	sp.dcbFontA[1] = get("sddCharFontSetASize1", 14)
	sp.dcbFontA[2] = get("sddCharFontSetASize2", 17)

	sp.systemFontB[0] = get("sddCharFontSetBSize0", 11)
	sp.systemFontB[1] = get("sddCharFontSetBSize1", 12)
	sp.systemFontB[2] = get("sddCharFontSetBSize2", 15)
	sp.systemFontB[3] = get("sddCharFontSetBSize3", 16)
	sp.systemFontB[4] = get("sddCharFontSetBSize4", 18)
	sp.systemFontB[5] = get("sddCharFontSetBSize5", 19)
	sp.systemOutlineFontB[0] = get("sddCharOutlineFontSetBSize0", 11)
	sp.systemOutlineFontB[1] = get("sddCharOutlineFontSetBSize1", 12)
	sp.systemOutlineFontB[2] = get("sddCharOutlineFontSetBSize2", 15)
	sp.systemOutlineFontB[3] = get("sddCharOutlineFontSetBSize3", 16)
	sp.systemOutlineFontB[4] = get("sddCharOutlineFontSetBSize4", 18)
	sp.systemOutlineFontB[5] = get("sddCharOutlineFontSetBSize5", 19)
	sp.dcbFontB[0] = get("sddCharFontSetBSize0", 11)
	sp.dcbFontB[1] = get("sddCharFontSetBSize1", 12)
	sp.dcbFontB[2] = get("sddCharFontSetBSize2", 15)

	sp.cursorsFont = get("STARS cursors", 30)
}

func (sp *STARSPane) systemFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.systemFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.systemFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.systemFontA[idx]
	} else {
		return sp.systemFontB[idx]
	}
}

func (sp *STARSPane) systemOutlineFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.systemOutlineFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.systemOutlineFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.systemOutlineFontA[idx]
	} else {
		return sp.systemOutlineFontB[idx]
	}
}

func (sp *STARSPane) dcbFont(ctx *panes.Context, idx int) *renderer.Font {
	if sp.FontSelection == fontLegacy {
		return sp.dcbFontA[idx]
	} else if sp.FontSelection == fontARTS {
		return sp.dcbFontB[idx]
	} else if ctx.FacilityAdaptation.UseLegacyFont {
		return sp.dcbFontA[idx]
	} else {
		return sp.dcbFontB[idx]
	}
}

// The ∆ character in the STARS font isn't at the regular ∆ unicode rune,
// so patch it up.
func rewriteDelta(s string) string {
	s = strings.ReplaceAll(s, "Δ", STARSTriangleCharacter)
	return strings.ReplaceAll(s, "∆", STARSTriangleCharacter)
}

func createFontAtlas(r renderer.Renderer, p platform.Platform) []*renderer.Font {
	// See stars-fonts.go (which is automatically-generated) for the
	// definition of starsFonts, which stores the bitmaps and additional
	// information about the glyphs in the STARS fonts.

	xres, yres := 2048, 1024
	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: xres, Y: yres}})
	x, y := 0, 0

	var newFonts []*renderer.Font

	scale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

	addFontToAtlas := func(fontName string, sf STARSFont) {
		id := renderer.FontIdentifier{
			Name: fontName,
			Size: sf.Height,
		}

		f := renderer.MakeFont(int(scale)*sf.Height, true /* mono */, id, nil)
		newFonts = append(newFonts, f)

		if y+sf.Height >= yres {
			panic("STARS font atlas texture too small")
		}

		for ch, glyph := range sf.Glyphs {
			dx := glyph.Bounds[0] + 1 // pad
			if x+dx > xres {
				// Start a new line
				x = 0
				y += sf.Height + 1
			}

			glyph.rasterize(atlas, x, y)
			glyph.addToFont(ch, x, y, xres, yres, sf, f, scale)

			x += dx
		}

		// Start a new line after finishing a font.
		x = 0
		y += sf.Height + 1
	}

	// Iterate over the fonts, create Font/Glyph objects for them, and copy
	// their bitmaps into the atlas image.
	for _, fontName := range util.SortedMapKeys(starsFonts) { // consistent order
		addFontToAtlas(fontName, starsFonts[fontName])
	}

	// Patch up the cursors (which are missing Offset) values so that they
	// are centered at the point where they are drawn.
	for i := range starsCursors.Glyphs {
		g := &starsCursors.Glyphs[i]
		g.Offset[0] = -(g.Bounds[0] + 1) / 2
		g.Offset[1] = starsCursors.Height - (g.Bounds[1]+1)/2
	}
	addFontToAtlas("STARS cursors", starsCursors)

	atlasId := r.CreateTextureFromImage(atlas, true /* nearest filter */)
	for _, font := range newFonts {
		font.TexId = atlasId
	}

	return newFonts
}

func (glyph STARSGlyph) rasterize(img *image.RGBA, x0, y0 int) {
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
				img.SetRGBA(x0+x, y0+y, on)
			}
		}
	}
}

func (glyph STARSGlyph) addToFont(ch, x, y, xres, yres int, sf STARSFont, f *renderer.Font, scale float32) {
	g := &renderer.Glyph{
		// Vertex coordinates for the quad: shift based on the offset
		// associated with the glyph.  Also, count up from the bottom in y
		// rather than drawing from the top.
		X0: scale * float32(glyph.Offset[0]),
		X1: scale * float32(glyph.Offset[0]+glyph.Bounds[0]),
		Y0: scale * float32(sf.Height-glyph.Offset[1]-glyph.Bounds[1]),
		Y1: scale * float32(sf.Height-glyph.Offset[1]),

		// Texture coordinates: just the extent of where we rasterized the
		// glyph in the atlas, rescaled to [0,1].
		U0: float32(x) / float32(xres),
		V0: float32(y) / float32(yres),
		U1: (float32(x + glyph.Bounds[0])) / float32(xres),
		V1: (float32(y + glyph.Bounds[1])) / float32(yres),

		AdvanceX: scale * float32(glyph.StepX),
		Visible:  true,
	}
	f.AddGlyph(ch, g)
}
