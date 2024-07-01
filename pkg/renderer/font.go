package renderer

import (
	gomath "math"

	"github.com/mmp/imgui-go/v4"
)

// Each loaded (font,size) combination is represented by (surprise) a Font.
type Font struct {
	// Glyphs for the commonly-used ASCII range can be looked up using a
	// directly-mapped array, for efficiency.
	lowGlyphs [128]*Glyph
	// The remaining glyphs (generally, the used FontAwesome icons, are
	// stored in a map.
	glyphs map[rune]*Glyph
	// Font size
	Size  int
	Mono  bool
	Ifont imgui.Font // may be unset if the font isn't used with imgui (e.g. the STARS fonts)
	Id    FontIdentifier
	TexId uint32 // texture that holds the glyph texture atlas
}

func MakeFont(size int, mono bool, id FontIdentifier, ifont *imgui.Font) *Font {
	f := &Font{
		glyphs: make(map[rune]*Glyph),
		Size:   size,
		Mono:   mono,
		Id:     id,
	}
	if ifont != nil {
		f.Ifont = *ifont
	}
	return f
}

// While the following could be found via the imgui.FontGlyph interface, cgo calls into C++ code are
// slow, especially if we do ~10 of them for each character drawn. So we cache the information we need
// to draw each one here.
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

// FontIdentifier is used for looking up
type FontIdentifier struct {
	Name string
	Size int
}

// Internal: lookup the glyph for a rune in imgui's font atlas and then
// copy over the necessary information into our Glyph structure.
func (f *Font) createGlyph(ch rune) *Glyph {
	ig := f.Ifont.FindGlyph(ch)
	return &Glyph{X0: ig.X0(), Y0: ig.Y0(), X1: ig.X1(), Y1: ig.Y1(),
		U0: ig.U0(), V0: ig.V0(), U1: ig.U1(), V1: ig.V1(),
		AdvanceX: ig.AdvanceX(), Visible: ig.Visible()}
}

func (f *Font) AddGlyph(ch int, g *Glyph) {
	if ch < 128 {
		f.lowGlyphs[ch] = g
	} else {
		f.glyphs[rune(ch)] = g
	}
}

// LookupGlyph returns the Glyph for the specified rune.
func (f *Font) LookupGlyph(ch rune) *Glyph {
	if int(ch) < len(f.lowGlyphs) {
		if g := f.lowGlyphs[ch]; g == nil {
			g = f.createGlyph(ch)
			f.lowGlyphs[ch] = g
			return g
		} else {
			return g
		}
	} else if g, ok := f.glyphs[ch]; !ok {
		g = f.createGlyph(ch)
		f.glyphs[ch] = g
		return g
	} else {
		return g
	}
}

// Returns the bound of the specified text in the given font, assuming the
// given pixel spacing between lines.
func (font *Font) BoundText(s string, spacing int) (int, int) {
	dy := font.Size + spacing
	py := dy
	var px, xmax float32
	for _, ch := range s {
		if ch == '\n' {
			px = 0
			py += dy
		} else {
			glyph := font.LookupGlyph(ch)
			px += glyph.AdvanceX
			if px > xmax {
				xmax = px
			}
		}
	}

	return int(gomath.Ceil(float64(xmax))), py
}
