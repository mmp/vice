package eram

import (
	"image"
	"image/color"
	"runtime"
	"slices"
	"strconv"

	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

func (ep *ERAMPane) ERAMFont() *renderer.Font {
	// if runtime.GOOS == "darwin" {
	// 	return renderer.GetFont(renderer.FontIdentifier{Name: "ERAMv102", Size: 12}) // use regular fonts rather than bitmap to avoid quality and size issues.
	// }
	return ep.systemFont[3]

}
func (ep *ERAMPane) ERAMToolbarFont() *renderer.Font {
	return renderer.GetFont(renderer.FontIdentifier{Name: "ERAMv102", Size: 10})
}
func (ep *ERAMPane) ERAMInputFont() *renderer.Font {
	return renderer.GetFont(renderer.FontIdentifier{Name: "ERAMv102", Size: 13})
}

func (ep *ERAMPane) initializeFonts(r renderer.Renderer, p platform.Platform) {
	fonts := createFontAtlas(r, p)
	get := func(name string, size int) *renderer.Font {
		idx := slices.IndexFunc(fonts, func(f *renderer.Font) bool { return f.Id.Name == name && f.Id.Size == size })
		if idx == -1 {
			panic(name + " size " + strconv.Itoa(size) + " not found in ERAM fonts")
		}
		return fonts[idx]
	}

	// TODO: Find the fifth ERAM text size. 
	ep.systemFont[0] = get("EramText-8.pcf", 11)
	ep.systemFont[1] = get("EramText-11.pcf", 13)
	ep.systemFont[2] = get("EramText-14.pcf", 17)
	ep.systemFont[3] = get("EramText-16.pcf", 18)
	ep.systemFont[4] = get("EramTargets-16.pcf", 15)
	ep.systemFont[5] = get("EramGeomap-16.pcf", 15)
	ep.systemFont[6] = get("EramGeomap-18.pcf", 17)
	ep.systemFont[7] = get("EramGeomap-20.pcf", 19)
	ep.systemFont[8] = get("EramTracks-16.pcf", 15)

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

	addFontToAtlas := func(fontName string, sf ERAMFont) {
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
	for _, fontName := range util.SortedMapKeys(eramFonts) { // consistent order
		addFontToAtlas(fontName, eramFonts[fontName])
	}

	atlasId := r.CreateTextureFromImage(atlas, true /* nearest filter */)
	for _, font := range newFonts {
		font.TexId = atlasId
	}

	return newFonts
}

func (glyph ERAMGlyph) rasterize(img *image.RGBA, x0, y0 int) {
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

func (glyph ERAMGlyph) addToFont(ch, x, y, xres, yres int, sf ERAMFont, f *renderer.Font, scale float32) {
	minHeight := float32(1.1) / scale // just enough to guarantee a draw
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
	if g.Y1-g.Y0 < minHeight {
		g.Y1 = g.Y0 + minHeight // ensure that the glyph is visible
	}
	f.AddGlyph(ch, g)
}
