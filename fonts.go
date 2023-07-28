// fonts.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"C"
	_ "embed"
	"fmt"
	"image"
	"math"
	"runtime"
	"sort"
	"unicode/utf8"
	"unsafe"

	"github.com/mmp/IconFontCppHeaders"
	"github.com/mmp/imgui-go/v4"
)

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
	FontAwesomeIconCopyright           = faUsedIcons["Copyright"]
	FontAwesomeIconDiscord             = faBrandsUsedIcons["Discord"]
	FontAwesomeIconExclamationTriangle = faUsedIcons["ExclamationTriangle"]
	FontAwesomeIconFile                = faUsedIcons["File"]
	FontAwesomeIconFolder              = faUsedIcons["Folder"]
	FontAwesomeIconGithub              = faBrandsUsedIcons["Github"]
	FontAwesomeIconHandPointLeft       = faUsedIcons["HandPointLeft"]
	FontAwesomeIconHome                = faUsedIcons["Home"]
	FontAwesomeIconInfoCircle          = faUsedIcons["InfoCircle"]
	FontAwesomeIconLevelUpAlt          = faUsedIcons["LevelUpAlt"]
	FontAwesomeIconLock                = faUsedIcons["Lock"]
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
		"Cog":                 FontAwesomeString("Cog"),
		"Copyright":           FontAwesomeString("Copyright"),
		"ExclamationTriangle": FontAwesomeString("ExclamationTriangle"),
		"File":                FontAwesomeString("File"),
		"Folder":              FontAwesomeString("Folder"),
		"HandPointLeft":       FontAwesomeString("HandPointLeft"),
		"Home":                FontAwesomeString("Home"),
		"InfoCircle":          FontAwesomeString("InfoCircle"),
		"LevelUpAlt":          FontAwesomeString("LevelUpAlt"),
		"Lock":                FontAwesomeString("Lock"),
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

	// Font data; they're all embedded in the executable as strings at
	// compile time, which saves us any worries about having trouble
	// finding them at runtime.

	//go:embed resources/Roboto-Regular.ttf.zst
	robotoRegularTTF string

	//go:embed resources/VT323-Regular.ttf.zst
	vt323RegularTTF string

	//go:embed resources/FixedDemiBold.otf.zst
	fixedDemiBoldOTF string

	//go:embed resources/Inconsolata-Semibold.ttf.zst
	inconsolataSemiBoldTTF string

	//go:embed resources/Flight-Strip-Printer.ttf.zst
	flightStripPrinterTTF string

	//go:embed resources/Inconsolata/static/Inconsolata_Condensed/Inconsolata_Condensed-Regular.ttf.zst
	inconsolataCondensedRegularTTF string

	//go:embed "resources/Font Awesome 5 Brands-Regular-400.otf.zst"
	fa5BrandsRegularTTF string
	//go:embed "resources/Font Awesome 5 Free-Regular-400.otf.zst"
	fa5RegularTTF string
	//go:embed "resources/Font Awesome 5 Free-Solid-900.otf.zst"
	fa5SolidTTF string

	//----go:embed "resources/ibm_ega_8x14.ttf.zst"
	//ibmEGA8x14 string
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
	size  int
	mono  bool
	ifont imgui.Font
	id    FontIdentifier
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
	ig := f.ifont.FindGlyph(ch)
	return &Glyph{X0: ig.X0(), Y0: ig.Y0(), X1: ig.X1(), Y1: ig.Y1(),
		U0: ig.U0(), V0: ig.V0(), U1: ig.U1(), V1: ig.V1(),
		AdvanceX: ig.AdvanceX(), Visible: ig.Visible()}
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
	dy := font.size + spacing
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

	return int(math.Ceil(float64(xmax))), py
}

// From imgui-go:
// unrealisticLargePointer is used to cast an arbitrary native pointer to a slice.
// Its value is chosen to fit into a 32bit architecture, and still be large
// enough to cover "any" data blob. Note that this value is in bytes.
const unrealisticLargePointer = 1 << 30

func ptrToUint16Slice(p unsafe.Pointer) []uint16 {
	return (*[unrealisticLargePointer / 2]uint16)(p)[:]
}

func fontsInit(r Renderer, platform Platform) {
	lg.Printf("Starting to initialize fonts")
	fonts = make(map[FontIdentifier]*Font)
	io := imgui.CurrentIO()

	// Given a map that specifies the icons used in an icon font, returns
	// an imgui.GlyphRanges that encompasses those icons.  This GlyphRanges
	// is then used shortly when the fonts are loaded.
	glyphRangeForIcons := func(icons map[string]string) imgui.GlyphRanges {
		// imgui represents such glyph ranges as an array of uint16s, where
		// each range is given by two successive values and where a value
		// of 0 denotes the end of the array.  We need to resort to malloc
		// for this array since imgui's AddFontFromMemoryTTF() function
		// holds on to its pointer.  (Thus, using a slice or go's new fails
		// unpredictably, since go's GC will happily reclaim the memory.)
		r := C.malloc(C.size_t(4*len(icons) + 2))
		ranges := ptrToUint16Slice(r)
		i := 0
		for _, str := range icons {
			unicode, _ := utf8.DecodeRuneInString(str)
			// The specified range is inclusive so we just double-up the
			// unicode value.
			ranges[i] = uint16(unicode)
			ranges[i+1] = uint16(unicode)
			i += 2
		}
		ranges[i] = 0
		return imgui.GlyphRanges(r)
	}

	// Decompress and get the glyph ranges for the Font Awesome fonts just once.
	faTTF := []byte(decompressZstd(fa5SolidTTF))
	faGlyphRange := glyphRangeForIcons(faUsedIcons)
	fabrTTF := []byte(decompressZstd(fa5BrandsRegularTTF))
	faBrandsGlyphRange := glyphRangeForIcons(faBrandsUsedIcons)

	add := func(ttfZstd string, mono bool, name string) {
		ttf := []byte(decompressZstd(ttfZstd))
		for _, size := range []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 16, 18, 20, 22, 24, 28} {
			sp := float32(size)
			if runtime.GOOS == "windows" {
				// Fix font sizes to account for Windows using 96dpi but
				// everyone else using 72...
				sp *= 96. / 72. * platform.DPIScale()
				sp = float32(int(sp + 0.5))
			}

			ifont := io.Fonts().AddFontFromMemoryTTFV(ttf, sp, imgui.DefaultFontConfig, imgui.EmptyGlyphRanges)

			config := imgui.NewFontConfig()
			config.SetMergeMode(true)
			// Scale down the font size by an ad-hoc factor to (generally)
			// make the icon sizes match the font's character sizes.
			io.Fonts().AddFontFromMemoryTTFV(faTTF, .8*sp, config, faGlyphRange)
			io.Fonts().AddFontFromMemoryTTFV(fabrTTF, .8*sp, config, faBrandsGlyphRange)

			id := FontIdentifier{Name: name, Size: size}
			fonts[id] = &Font{
				glyphs: make(map[rune]*Glyph),
				size:   int(sp),
				mono:   mono,
				ifont:  ifont,
				id:     id}
		}
	}

	add(robotoRegularTTF, false, "Roboto Regular")
	add(vt323RegularTTF, true, "VT323 Regular")
	add(fixedDemiBoldOTF, true, "Fixed Demi Bold")
	add(inconsolataSemiBoldTTF, true, "Inconsolata SemiBold")
	add(flightStripPrinterTTF, true, "Flight Strip Printer")
	add(inconsolataCondensedRegularTTF, true, "Inconsolata Condensed Regular")

	img := io.Fonts().TextureDataRGBA32()
	lg.Printf("Fonts texture used %.1f MB", float32(img.Width*img.Height*4)/(1024*1024))
	rgb8Image := &image.RGBA{
		Pix:    unsafe.Slice((*uint8)(img.Pixels), 4*img.Width*img.Height),
		Stride: 4 * img.Width,
		Rect:   image.Rectangle{Max: image.Point{X: img.Width, Y: img.Height}}}
	fontId := r.CreateTextureFromImage(rgb8Image)
	io.Fonts().SetTextureID(imgui.TextureID(fontId))

	lg.Printf("Finished initializing fonts")
}

// GetAllFonts returns a FontIdentifier slice that gives identifiers for
// all of the available fonts, sorted by font name and then within each
// name, by font size.
func GetAllFonts() []FontIdentifier {
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
	f := GetAllFonts()
	lastFontName := ""
	if imgui.BeginComboV(label+fmt.Sprintf("##%p", id), id.Name, imgui.ComboFlagsHeightLarge) {
		// Take advantage of the sort order returned by GetAllFonts()--that
		// all fonts of the same name come consecutively.
		for _, font := range f {
			if font.Name != lastFontName {
				lastFontName = font.Name
				// Use the 14pt version of the font in the combo box.
				displayFont := GetFont(FontIdentifier{Name: font.Name, Size: 14})
				imgui.PushFont(displayFont.ifont)
				if imgui.SelectableV(font.Name, id.Name == font.Name, 0, imgui.Vec2{}) {
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
	if imgui.BeginComboV(fmt.Sprintf("Font Size##%s", id.Name), fmt.Sprintf("%d", id.Size), imgui.ComboFlagsHeightLarge) {
		for _, font := range GetAllFonts() {
			if font.Name == id.Name {
				if imgui.SelectableV(fmt.Sprintf("%d", font.Size), id.Size == font.Size, 0, imgui.Vec2{}) {
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
		lg.Errorf("%s: FA string unknown", id)
	}
	return s
}

func FontAwesomeBrandsString(id string) string {
	s, ok := IconFontCppHeaders.FontAwesome5Brands.Icons[id]
	if !ok {
		lg.Errorf("%s: FA string unknown", id)
	}
	return s
}
