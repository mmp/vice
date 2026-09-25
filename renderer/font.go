// renderer/font.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"fmt"
	"image"
	"image/color"
	"iter"
	"runtime"

	"github.com/vmihailenco/msgpack/v5"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// Font is a set of glyphs rasterized into a texture atlas and drawn with a
// TextDrawBuilder. All of a Font's glyphs are added when its atlas is
// built, so it is read-only once CreateBitmapFontAtlas has returned it.
type Font struct {
	// Glyphs for the commonly-used ASCII range can be looked up using a
	// directly-mapped array, for efficiency.
	lowGlyphs [128]*Glyph
	// The remaining glyphs are stored in a map.
	glyphs map[rune]*Glyph
	// Size is the font's height in pixels.
	Size  int
	Id    FontIdentifier
	TexId uint32 // texture that holds the glyph texture atlas
}

func MakeFont(size int, id FontIdentifier) *Font {
	return &Font{
		glyphs: make(map[rune]*Glyph),
		Size:   size,
		Id:     id,
	}
}

// Glyph holds everything needed to draw one character: where its quad goes
// relative to the pen position and where its pixels are in the font's
// texture atlas.
type Glyph struct {
	// Vertex positions for the quad to draw
	X0, Y0, X1, Y1 float32
	// Texture coordinates in the font atlas
	U0, V0, U1, V1 float32
	// Distance to advance in x after the character.
	AdvanceX float32
	// Is it a visible character (i.e., not space, tab, CR, ...)
	Visible bool
}

func (g *Glyph) Width() float32 {
	return g.X1 - g.X0
}

func (g *Glyph) Height() float32 {
	return g.Y1 - g.Y0
}

// FontIdentifier names a font by its name and its size in points.
type FontIdentifier struct {
	Name string
	Size int
}

func (f *Font) AddGlyph(ch int, g *Glyph) {
	if ch < 128 {
		f.lowGlyphs[ch] = g
	} else {
		f.glyphs[rune(ch)] = g
	}
}

// missingGlyph is returned for a rune that the font has no glyph for and
// that it has no '?' to fall back to; it draws nothing.
var missingGlyph Glyph

// LookupGlyph returns the Glyph for the specified rune, falling back to the
// font's '?' glyph for runes that its bitmap didn't include.
func (f *Font) LookupGlyph(ch rune) *Glyph {
	if int(ch) < len(f.lowGlyphs) {
		if g := f.lowGlyphs[ch]; g != nil {
			return g
		}
	} else if g, ok := f.glyphs[ch]; ok {
		return g
	}

	if g := f.lowGlyphs['?']; g != nil {
		return g
	}
	return &missingGlyph
}

// LayoutBounds returns the cell-metric extent of s in the same coordinate
// space that AddText uses: origin at the drawing position, x grows right,
// y grows up. Width is the accumulated AdvanceX (including the trailing
// advance past the last glyph); height is (font.Size + spacing) per line.
// Use for layout: row stacking, column widths, hit-test extents, clipping.
func (f *Font) LayoutBounds(s string, spacing int) math.Extent2D {
	dy := float32(f.Size + spacing)
	px, xmax := float32(0), float32(0)
	lines := 1
	for _, ch := range s {
		if ch == '\n' {
			px = 0
			lines++
		} else {
			glyph := f.LookupGlyph(ch)
			px += glyph.AdvanceX
			if px > xmax {
				xmax = px
			}
		}
	}
	return math.Extent2D{P0: [2]float32{0, -dy * float32(lines)}, P1: [2]float32{xmax, 0}}
}

// InkBounds returns the bounding box of the rasterized pixels of s in the
// same coordinate frame as LayoutBounds and AddText. Trailing AdvanceX past
// the last visible glyph and empty padding inside glyph cells are excluded.
// Returns an empty extent (IsEmpty() == true) for whitespace-only strings.
// Use for visual centering and tight background boxes; use LayoutBounds
// for layout.
func (f *Font) InkBounds(s string, spacing int) math.Extent2D {
	ext := math.EmptyExtent2D()
	dy := float32(f.Size + spacing)
	px, py := float32(0), float32(0)
	for _, ch := range s {
		if ch == '\n' {
			px = 0
			py -= dy
			continue
		}
		glyph := f.LookupGlyph(ch)
		if glyph.Visible {
			ext = math.Union(ext, [2]float32{px + glyph.X0, py - glyph.Y1})
			ext = math.Union(ext, [2]float32{px + glyph.X1, py - glyph.Y0})
		}
		px += glyph.AdvanceX
	}
	return ext
}

///////////////////////////////////////////////////////////////////////////
// Bitmap fonts

type BitmapFont struct {
	PointSize     int
	Width, Height int
	Glyphs        []BitmapGlyph
}

type BitmapGlyph struct {
	Name   string
	StepX  int
	Bounds [2]int
	Offset [2]int
	Bitmap []uint32
}

func CreateBitmapFontAtlas(r Renderer, dpiScale float32, fontIter iter.Seq2[string, BitmapFont]) []*Font {
	xres, yres := 2048, 1024
	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: xres, Y: yres}})
	x, y := 0, 0

	var newFonts []*Font

	scale := util.Select(runtime.GOOS == "windows", dpiScale, float32(1))

	for name, bf := range fontIter {
		id := FontIdentifier{
			Name: name,
			Size: bf.Height,
		}

		f := MakeFont(int(scale)*bf.Height, id)
		newFonts = append(newFonts, f)

		if y+bf.Height >= yres {
			panic("Font atlas texture too small")
		}

		for ch, glyph := range bf.Glyphs {
			glyph = glyph.trimmed()
			dx := glyph.Bounds[0] + 1 // pad
			if x+dx > xres {
				// Start a new line
				x = 0
				y += bf.Height + 1
			}

			glyph.Rasterize(atlas, x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			glyph.addToFont(ch, x, y, xres, yres, bf, f, scale)

			x += dx
		}

		// Start a new line after finishing a font.
		x = 0
		y += bf.Height + 1
	}

	atlasId := r.CreateTextureFromImage(atlas, true /* nearest filter */)
	for _, font := range newFonts {
		font.TexId = atlasId
	}

	return newFonts
}

// Rasterize writes the glyph's set bits into img at (x0, y0) using c.
// BitmapGlyphs store their bitmaps as an array of uint32s, where each uint32
// encodes a scanline and bits are set in it to indicate that the corresponding
// pixel should be drawn; thus, there are no intermediate values for
// anti-aliasing.
func (glyph BitmapGlyph) Rasterize(img *image.RGBA, x0, y0 int, c color.RGBA) {
	for y, line := range glyph.Bitmap {
		for x := range glyph.Bounds[0] {
			// The high bit corresponds to the first pixel in the scanline.
			if line&(1<<uint(31-x)) != 0 {
				img.SetRGBA(x0+x, y0+y, c)
			}
		}
	}
}

// trimmed returns the glyph with its blank margins removed, keeping its set
// pixels at the same place in the character cell. Some fonts (e.g., the STARS
// ARTS fonts) store each glyph as a full padded cell; trimming them makes each
// Glyph's quad bound its ink, which in turn keeps Font.InkBounds tight.
func (glyph BitmapGlyph) trimmed() BitmapGlyph {
	x0, y0, x1, y1 := glyph.Bounds[0], glyph.Bounds[1], 0, 0
	for y, line := range glyph.Bitmap {
		for x := range glyph.Bounds[0] {
			if line&(1<<uint(31-x)) != 0 {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x+1), max(y1, y+1)
			}
		}
	}
	if x1 == 0 {
		return BitmapGlyph{Name: glyph.Name, StepX: glyph.StepX}
	}

	glyph.Offset[0] += x0
	glyph.Offset[1] += glyph.Bounds[1] - y1
	glyph.Bounds = [2]int{x1 - x0, y1 - y0}
	glyph.Bitmap = util.MapSlice(glyph.Bitmap[y0:y1], func(line uint32) uint32 { return line << uint(x0) })
	return glyph
}

func (glyph BitmapGlyph) addToFont(ch, x, y, xres, yres int, bf BitmapFont, f *Font, scale float32) {
	g := &Glyph{
		// Vertex coordinates for the quad: shift based on the offset
		// associated with the glyph.  Also, count up from the bottom in y
		// rather than drawing from the top.
		X0: scale * float32(glyph.Offset[0]),
		X1: scale * float32(glyph.Offset[0]+glyph.Bounds[0]),
		Y0: scale * float32(bf.Height-glyph.Offset[1]-glyph.Bounds[1]),
		Y1: scale * float32(bf.Height-glyph.Offset[1]),

		// Texture coordinates: just the extent of where we rasterized the
		// glyph in the atlas, rescaled to [0,1].
		U0: float32(x) / float32(xres),
		V0: float32(y) / float32(yres),
		U1: (float32(x + glyph.Bounds[0])) / float32(xres),
		V1: (float32(y + glyph.Bounds[1])) / float32(yres),

		AdvanceX: scale * float32(glyph.StepX),
		Visible:  glyph.Bounds[0] > 0 && glyph.Bounds[1] > 0,
	}
	f.AddGlyph(ch, g)
}

// LoadBitmapFonts reads a set of bitmap fonts from the form
// EncodeBitmapFonts writes. The STARS and ERAM scope fonts are shipped as
// data files in fonts/ rather than as generated Go source; they were
// originally converted from the scopes' PCF font files.
func LoadBitmapFonts(b []byte) (map[string]BitmapFont, error) {
	var fonts map[string]BitmapFont
	if err := msgpack.Unmarshal(b, &fonts); err != nil {
		return nil, fmt.Errorf("decoding bitmap fonts: %w", err)
	}
	return fonts, nil
}

// EncodeBitmapFonts writes fonts in the form LoadBitmapFonts reads; it is
// what a tool that converts PCF fonts for vice emits.
func EncodeBitmapFonts(fonts map[string]BitmapFont) ([]byte, error) {
	return msgpack.Marshal(fonts)
}
