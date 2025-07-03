// pkg/renderer/font.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"C"
	"fmt"
	"image"
	gomath "math"
	"runtime"
	"sort"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/IconFontCppHeaders"
)

var ttfPinner runtime.Pinner

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
	Ifont imgui.Font
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

func (g *Glyph) Width() float32 {
	return g.X1 - g.X0
}

func (g *Glyph) Height() float32 {
	return g.Y1 - g.Y0
}

// FontIdentifier is used for looking up
type FontIdentifier struct {
	Name string
	Size int
}

// Internal: lookup the glyph for a rune in imgui's font atlas and then
// copy over the necessary information into our Glyph structure.
func (f *Font) createGlyph(ch rune) *Glyph {
	ig := f.Ifont.FindGlyph(imgui.Wchar(ch))
	g := &Glyph{X0: ig.X0(), Y0: ig.Y0(), X1: ig.X1(), Y1: ig.Y1(),
		U0: ig.U0(), V0: ig.V0(), U1: ig.U1(), V1: ig.V1(),
		AdvanceX: ig.AdvanceX(), Visible: ig.Visible() != 0}
	return g
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

// imgui lets us to embed icons within regular fonts which makes it
// possible to use them directly in text without changing to the icon
// font. However, we have a fair number of fonts and sizes.  Thereore, so
// save space, we require that the used icons be tracked in fonts.go so
// that only they need to be copied into all of the regular fonts.
// Given that, code elsewhere uses the following variables to get the
// string encoding that gives the corresponding icon.
var (
	FontAwesomeIconArrowDown           = faUsedIcons["ArrowDown"]
	FontAwesomeIconArrowLeft           = faUsedIcons["ArrowLeft"]
	FontAwesomeIconArrowRight          = faUsedIcons["ArrowRight"]
	FontAwesomeIconArrowUp             = faUsedIcons["ArrowUp"]
	FontAwesomeIconBook                = faUsedIcons["Book"]
	FontAwesomeIconBug                 = faUsedIcons["Bug"]
	FontAwesomeIconCaretDown           = faUsedIcons["CaretDown"]
	FontAwesomeIconCaretRight          = faUsedIcons["CaretRight"]
	FontAwesomeIconCheckSquare         = faUsedIcons["CheckSquare"]
	FontAwesomeIconCog                 = faUsedIcons["Cog"]
	FontAwesomeIconCompressAlt         = faUsedIcons["CompressAlt"]
	FontAwesomeIconCopyright           = faUsedIcons["Copyright"]
	FontAwesomeIconDiscord             = faBrandsUsedIcons["Discord"]
	FontAwesomeIconExclamationTriangle = faUsedIcons["ExclamationTriangle"]
	FontAwesomeIconExpandAlt           = faUsedIcons["ExpandAlt"]
	FontAwesomeIconFastForward         = faUsedIcons["FastForward"]
	FontAwesomeIconFile                = faUsedIcons["File"]
	FontAwesomeIconFolder              = faUsedIcons["Folder"]
	FontAwesomeIconGithub              = faBrandsUsedIcons["Github"]
	FontAwesomeIconHandPointLeft       = faUsedIcons["HandPointLeft"]
	FontAwesomeIconHome                = faUsedIcons["Home"]
	FontAwesomeIconInfoCircle          = faUsedIcons["InfoCircle"]
	FontAwesomeIconKeyboard            = faUsedIcons["Keyboard"]
	FontAwesomeIconLevelUpAlt          = faUsedIcons["LevelUpAlt"]
	FontAwesomeIconLock                = faUsedIcons["Lock"]
	FontAwesomeIconMouse               = faUsedIcons["Mouse"]
	FontAwesomeIconPauseCircle         = faUsedIcons["PauseCircle"]
	FontAwesomeIconPlayCircle          = faUsedIcons["PlayCircle"]
	FontAwesomeIconQuestionCircle      = faUsedIcons["QuestionCircle"]
	FontAwesomeIconPlaneDeparture      = faUsedIcons["PlaneDeparture"]
	FontAwesomeIconRedo                = faUsedIcons["Redo"]
	FontAwesomeIconSquare              = faUsedIcons["Square"]
	FontAwesomeIconTrash               = faUsedIcons["Trash"]
)

var (
	// All of the available fonts.
	fonts map[FontIdentifier]*Font

	// This and the following faBrandsUsedIcons map are what drives
	// determining which icons are copied into regular fonts; see
	// InitializeFonts() below.
	faUsedIcons map[string]string = map[string]string{
		"ArrowDown":           FontAwesomeString("ArrowDown"),
		"ArrowLeft":           FontAwesomeString("ArrowLeft"),
		"ArrowRight":          FontAwesomeString("ArrowRight"),
		"ArrowUp":             FontAwesomeString("ArrowUp"),
		"Book":                FontAwesomeString("Book"),
		"Bug":                 FontAwesomeString("Bug"),
		"CaretDown":           FontAwesomeString("CaretDown"),
		"CaretRight":          FontAwesomeString("CaretRight"),
		"CheckSquare":         FontAwesomeString("CheckSquare"),
		"CompressAlt":         FontAwesomeString("CompressAlt"),
		"Cog":                 FontAwesomeString("Cog"),
		"Copyright":           FontAwesomeString("Copyright"),
		"ExclamationTriangle": FontAwesomeString("ExclamationTriangle"),
		"ExpandAlt":           FontAwesomeString("ExpandAlt"),
		"FastForward":         FontAwesomeString("FastForward"),
		"File":                FontAwesomeString("File"),
		"Folder":              FontAwesomeString("Folder"),
		"HandPointLeft":       FontAwesomeString("HandPointLeft"),
		"Home":                FontAwesomeString("Home"),
		"InfoCircle":          FontAwesomeString("InfoCircle"),
		"Keyboard":            FontAwesomeString("Keyboard"),
		"LevelUpAlt":          FontAwesomeString("LevelUpAlt"),
		"Lock":                FontAwesomeString("Lock"),
		"Mouse":               FontAwesomeString("Mouse"),
		"PauseCircle":         FontAwesomeString("PauseCircle"),
		"PlayCircle":          FontAwesomeString("PlayCircle"),
		"QuestionCircle":      FontAwesomeString("QuestionCircle"),
		"PlaneDeparture":      FontAwesomeString("PlaneDeparture"),
		"Redo":                FontAwesomeString("Redo"),
		"Square":              FontAwesomeString("Square"),
		"Trash":               FontAwesomeString("Trash"),
	}
	faBrandsUsedIcons map[string]string = map[string]string{
		"Discord": FontAwesomeBrandsString("Discord"),
		"Github":  FontAwesomeBrandsString("Github"),
	}
)

func FontsInit(r Renderer, p platform.Platform) {
	lg.Info("Starting to initialize fonts")
	fonts = make(map[FontIdentifier]*Font)
	io := imgui.CurrentIO()

	// Given a map that specifies the icons used in an icon font, returns
	// an imgui.GlyphRanges that encompasses those icons.  This GlyphRanges
	// is then used shortly when the fonts are loaded.
	glyphRangeForIcons := func(icons map[string]string) imgui.GlyphRange {
		builder := imgui.NewFontGlyphRangesBuilder()
		builder.AddChar(imgui.Wchar(0x2191))
		builder.AddChar(imgui.Wchar(0x2193))
		for _, str := range icons {
			unicode, _ := utf8.DecodeRuneInString(str)
			builder.AddChar(imgui.Wchar(unicode))
		}
		r := imgui.NewGlyphRange()
		builder.BuildRanges(r)
		return r
	}

	// Decompress and get the glyph ranges for the Font Awesome fonts just once.
	faTTF := util.LoadResourceBytes("fonts/Font Awesome 5 Free-Solid-900.otf.zst")
	fabrTTF := util.LoadResourceBytes("fonts/Font Awesome 5 Brands-Regular-400.otf.zst")
	faGlyphRange := glyphRangeForIcons(faUsedIcons)
	faBrandsGlyphRange := glyphRangeForIcons(faBrandsUsedIcons)

	add := func(filename string, mono bool, name string) {
		ttf := util.LoadResourceBytes("fonts/" + filename)
		for _, size := range []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 16, 18, 20, 22, 24, 28} {
			sp := float32(size)
			if runtime.GOOS == "windows" {
				if dpis := p.DPIScale(); dpis > 1 {
					sp *= p.DPIScale()
				} else {
					// Fix font sizes to account for Windows using 96dpi but
					// everyone else using 72...
					sp *= 96. / 72.
				}
				sp = float32(int(sp + 0.5))
			}

			addTTF := func(ttf []byte, sp float32, fconfig *imgui.FontConfig, r imgui.GlyphRange) *imgui.Font {
				ttfPinner.Pin(&ttf[0])
				return io.Fonts().AddFontFromMemoryTTFV(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)),
					sp, fconfig, r.Data())
			}

			ttfPinner.Pin(&ttf[0])
			ifont := io.Fonts().AddFontFromMemoryTTF(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)), sp)

			config := imgui.NewFontConfig()
			config.SetMergeMode(true)
			// Scale down the font size by an ad-hoc factor to (generally)
			// make the icon sizes match the font's character sizes.
			addTTF(faTTF, .8*sp, config, faGlyphRange)
			addTTF(fabrTTF, .8*sp, config, faBrandsGlyphRange)

			id := FontIdentifier{Name: name, Size: size}
			fonts[id] = MakeFont(int(sp), mono, id, ifont)
		}
	}

	add("Roboto-Regular.ttf.zst", false, "Roboto Regular")
	add("RobotoMono-Medium.ttf.zst", false, "Roboto Mono")
	add("RobotoMono-MediumItalic.ttf.zst", false, "Roboto Mono Italic")
	add("VT323-Regular.ttf.zst", true, "VT323 Regular")
	add("FixedDemiBold.otf.zst", true, "Fixed Demi Bold")
	add("Inconsolata-SemiBold.ttf.zst", true, "Inconsolata SemiBold")
	add("Flight-Strip-Printer.ttf.zst", true, "Flight Strip Printer")
	add("Inconsolata_Condensed-Regular.ttf.zst", true, "Inconsolata Condensed Regular")
	add("ERAM.ttf.zst", true, "ERAMv102")

	pixels, w, h, bpp := io.Fonts().GetTextureDataAsRGBA32()
	lg.Infof("Fonts texture used %.1f MB", float32(w*h*bpp)/(1024*1024))
	rgb8Image := &image.RGBA{
		Pix:    unsafe.Slice((*uint8)(pixels), bpp*w*h),
		Stride: int(4 * w),
		Rect:   image.Rectangle{Max: image.Point{X: int(w), Y: int(h)}}}
	atlasId := r.CreateTextureFromImage(rgb8Image, true /* nearest */)
	io.Fonts().SetTexID(imgui.TextureID(atlasId))

	// Patch up the texture id after the atlas was created with the
	// TextureDataRGBA32 call above.
	for _, font := range fonts {
		font.TexId = atlasId
	}

	lg.Info("Finished initializing fonts")
}

// getAllFonts returns a FontIdentifier slice that gives identifiers for
// all of the available fonts, sorted by font name and then within each
// name, by font size.
func getAllFonts() []FontIdentifier {
	var fs []FontIdentifier
	for f := range fonts {
		fs = append(fs, f)
	}

	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Name == fs[j].Name {
			return fs[i].Size < fs[j].Size
		}
		return fs[i].Name < fs[j].Name
	})

	return fs
}

func DrawFontPicker(id *FontIdentifier, label string) (newFont *Font, changed bool) {
	f := getAllFonts()
	lastFontName := ""
	if imgui.BeginComboV(label+fmt.Sprintf("##%p", id), id.Name, imgui.ComboFlagsHeightLarge) {
		// Take advantage of the sort order returned by getAllFonts()--that
		// all fonts of the same name come consecutively.
		for _, font := range f {
			if font.Name != lastFontName {
				lastFontName = font.Name
				// Use the 14pt version of the font in the combo box.
				displayFont := GetFont(FontIdentifier{Name: font.Name, Size: 14})
				imgui.PushFont(&displayFont.Ifont)
				if imgui.SelectableBoolV(font.Name, id.Name == font.Name, 0, imgui.Vec2{}) {
					id.Name = font.Name
					changed = true
					newFont = GetFont(*id)
				}
				imgui.PopFont()
			}
		}
		imgui.EndCombo()
	}

	if nf, ch := DrawFontSizeSelector(id); ch {
		changed = true
		newFont = nf
	}

	return
}

func DrawFontSizeSelector(id *FontIdentifier) (newFont *Font, changed bool) {
	if imgui.BeginComboV(fmt.Sprintf("Font Size##%s", id.Name), strconv.Itoa(id.Size), imgui.ComboFlagsHeightLarge) {
		for _, font := range getAllFonts() {
			if font.Name == id.Name {
				if imgui.SelectableBoolV(strconv.Itoa(font.Size), id.Size == font.Size, 0, imgui.Vec2{}) {
					id.Size = font.Size
					newFont = GetFont(font)
					changed = true
				}
			}
		}
		imgui.EndCombo()
	}
	return
}

func GetFont(id FontIdentifier) *Font {
	if font, ok := fonts[id]; ok {
		return font
	} else {
		return nil
	}
}

func GetDefaultFont() *Font {
	return GetFont(FontIdentifier{Name: "Roboto Regular", Size: 14})
}

func FontAwesomeString(id string) string {
	s, ok := IconFontCppHeaders.FontAwesome5.Icons[id]
	if !ok {
		panic(fmt.Sprintf("%s: FA string unknown", id))
	}
	return s
}

func FontAwesomeBrandsString(id string) string {
	s, ok := IconFontCppHeaders.FontAwesome5Brands.Icons[id]
	if !ok {
		panic(fmt.Sprintf("%s: FA string unknown", id))
	}
	return s
}

func AvailableFontSizes(name string) []int {
	sizes := make(map[int]interface{})
	for fontid := range fonts {
		if fontid.Name == name {
			sizes[fontid.Size] = nil
		}
	}
	return util.SortedMapKeys(sizes)
}
