// renderer/font.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"C"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/fs"
	gomath "math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/klauspost/compress/zstd"
	"github.com/mmp/IconFontCppHeaders"
)
import "iter"

// Font name constants
const (
	RobotoRegular        = "Roboto Regular"
	RobotoMono           = "Roboto Mono"
	RobotoMonoItalic     = "Roboto Mono Italic"
	FlightStripPrinter   = "Flight Strip Printer"
	LargeFontAwesomeOnly = "LargeFontAwesomeOnly"
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
	Ifont imgui.Font
	Id    FontIdentifier
	TexId uint32 // texture that holds the glyph texture atlas
	// isBitmapFont is true for fonts without imgui backing (e.g., STARS fonts)
	isBitmapFont bool
}

func MakeFont(size int, id FontIdentifier, ifont *imgui.Font) *Font {
	f := &Font{
		glyphs:       make(map[rune]*Glyph),
		Size:         size,
		Id:           id,
		isBitmapFont: ifont == nil,
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
	if f.isBitmapFont {
		// Bitmap fonts can't create new glyphs dynamically. Fall back to '?'.
		if g := f.lowGlyphs['?']; g != nil {
			return g
		}
		// Last resort: return a zero-width invisible glyph
		return &Glyph{}
	}

	baked := f.Ifont.FontBaked(float32(f.Size))
	ig := baked.FindGlyph(imgui.Wchar(ch))
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
	FontAwesomeIconBolt                = faUsedIcons["Bolt"]
	FontAwesomeIconBook                = faUsedIcons["Book"]
	FontAwesomeIconBug                 = faUsedIcons["Bug"]
	FontAwesomeIconCaretDown           = faUsedIcons["CaretDown"]
	FontAwesomeIconCaretRight          = faUsedIcons["CaretRight"]
	FontAwesomeIconCheckSquare         = faUsedIcons["CheckSquare"]
	FontAwesomeIconCloud               = faUsedIcons["Cloud"]
	FontAwesomeIconCloudRain           = faUsedIcons["CloudRain"]
	FontAwesomeIconCloudShowersHeavy   = faUsedIcons["CloudShowersHeavy"]
	FontAwesomeIconCloudSun            = faUsedIcons["CloudSun"]
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
	FontAwesomeIconMicrophone          = faUsedIcons["Microphone"]
	FontAwesomeIconMouse               = faUsedIcons["Mouse"]
	FontAwesomeIconPauseCircle         = faUsedIcons["PauseCircle"]
	FontAwesomeIconPlayCircle          = faUsedIcons["PlayCircle"]
	FontAwesomeIconQuestionCircle      = faUsedIcons["QuestionCircle"]
	FontAwesomeIconPlaneDeparture      = faUsedIcons["PlaneDeparture"]
	FontAwesomeIconRedo                = faUsedIcons["Redo"]
	FontAwesomeIconSmog                = faUsedIcons["Smog"]
	FontAwesomeIconSnowflake           = faUsedIcons["Snowflake"]
	FontAwesomeIconSquare              = faUsedIcons["Square"]
	FontAwesomeIconSun                 = faUsedIcons["Sun"]
	FontAwesomeIconTrash               = faUsedIcons["Trash"]
	FontAwesomeIconWind                = faUsedIcons["Wind"]
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
		"Bolt":                FontAwesomeString("Bolt"),
		"Book":                FontAwesomeString("Book"),
		"Bug":                 FontAwesomeString("Bug"),
		"CaretDown":           FontAwesomeString("CaretDown"),
		"CaretRight":          FontAwesomeString("CaretRight"),
		"CheckSquare":         FontAwesomeString("CheckSquare"),
		"Cloud":               FontAwesomeString("Cloud"),
		"CloudRain":           FontAwesomeString("CloudRain"),
		"CloudShowersHeavy":   FontAwesomeString("CloudShowersHeavy"),
		"CloudSun":            FontAwesomeString("CloudSun"),
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
		"Microphone":          FontAwesomeString("Microphone"),
		"Mouse":               FontAwesomeString("Mouse"),
		"PauseCircle":         FontAwesomeString("PauseCircle"),
		"PlayCircle":          FontAwesomeString("PlayCircle"),
		"QuestionCircle":      FontAwesomeString("QuestionCircle"),
		"PlaneDeparture":      FontAwesomeString("PlaneDeparture"),
		"Redo":                FontAwesomeString("Redo"),
		"Smog":                FontAwesomeString("Smog"),
		"Snowflake":           FontAwesomeString("Snowflake"),
		"Square":              FontAwesomeString("Square"),
		"Sun":                 FontAwesomeString("Sun"),
		"Trash":               FontAwesomeString("Trash"),
		"Wind":                FontAwesomeString("Wind"),
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
	faTTF := loadFont("Font Awesome 5 Free-Solid-900.otf.zst")
	fabrTTF := loadFont("Font Awesome 5 Brands-Regular-400.otf.zst")
	faGlyphRange := glyphRangeForIcons(faUsedIcons)
	faBrandsGlyphRange := glyphRangeForIcons(faBrandsUsedIcons)

	// Helper to calculate scaled pixel size for a given point size
	calcPixelSize := func(size int) float32 {
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
		return sp
	}

	// Helper to add TTF data to imgui
	addTTF := func(ttf []byte, sp float32, fconfig *imgui.FontConfig, r imgui.GlyphRange) *imgui.Font {
		ttfPinner.Pin(&ttf[0])
		return io.Fonts().AddFontFromMemoryTTFV(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)),
			sp, fconfig, r.Data())
	}

	// Helper to create a font with optional FontAwesome icons merged in
	createFontSize := func(ttf []byte, size int, name string) {
		sp := calcPixelSize(size)

		var ifont *imgui.Font
		if ttf != nil {
			ttfPinner.Pin(&ttf[0])
			defaultConfig := imgui.NewFontConfig()
			ifont = io.Fonts().AddFontFromMemoryTTFV(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)), sp, defaultConfig, nil)
		}

		config := imgui.NewFontConfig()
		if ttf != nil {
			config.SetMergeMode(true)
		}
		// Scale down the font size by an ad-hoc factor to (generally)
		// make the icon sizes match the font's character sizes.
		iconScale := float32(0.8)
		if ttf == nil {
			// For FontAwesome-only font, don't scale down
			iconScale = 1.0
			ifont = addTTF(faTTF, iconScale*sp, config, faGlyphRange)
		} else {
			addTTF(faTTF, iconScale*sp, config, faGlyphRange)
		}

		config.SetMergeMode(true)
		addTTF(fabrTTF, iconScale*sp, config, faBrandsGlyphRange)

		id := FontIdentifier{Name: name, Size: size}
		fonts[id] = MakeFont(int(sp), id, ifont)
	}

	for fn, name := range map[string]string{
		"Roboto-Regular.ttf.zst":          RobotoRegular,
		"RobotoMono-Medium.ttf.zst":       RobotoMono,
		"RobotoMono-MediumItalic.ttf.zst": RobotoMonoItalic,
		"Flight-Strip-Printer.ttf.zst":    FlightStripPrinter} {
		for _, size := range []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 16, 18, 20, 22, 24, 28} {
			createFontSize(loadFont(fn), size, name)
		}
	}
	// Add a large FontAwesome-only font for weather icons
	createFontSize(nil, 64, LargeFontAwesomeOnly)

	texData := io.Fonts().TexData()
	w, h, bpp := texData.Width(), texData.Height(), texData.BytesPerPixel()
	lg.Infof("Fonts texture: %dx%d, %d bpp, %.1f MB", w, h, bpp, float32(w*h*bpp)/(1024*1024))
	// texData.Pixels() returns a C pointer as uintptr; use unsafe.Add to
	// convert it without triggering go vet's uintptr-to-Pointer check.
	pixelsPtr := unsafe.Add(nil, texData.Pixels())

	var rgbaImage *image.RGBA
	if bpp == 4 {
		// Already RGBA32; use the pixel data directly.
		rgbaImage = &image.RGBA{
			Pix:    unsafe.Slice((*uint8)(pixelsPtr), 4*w*h),
			Stride: int(4 * w),
			Rect:   image.Rectangle{Max: image.Point{X: int(w), Y: int(h)}}}
	} else {
		// Alpha8 format (default): convert to RGBA32 (white text, varying alpha).
		alpha8 := unsafe.Slice((*uint8)(pixelsPtr), w*h)
		rgba32 := make([]uint8, 4*w*h)
		for i := range int(w * h) {
			rgba32[i*4+0] = 255
			rgba32[i*4+1] = 255
			rgba32[i*4+2] = 255
			rgba32[i*4+3] = alpha8[i]
		}
		rgbaImage = &image.RGBA{
			Pix:    rgba32,
			Stride: int(4 * w),
			Rect:   image.Rectangle{Max: image.Point{X: int(w), Y: int(h)}}}
	}
	atlasId := r.CreateTextureFromImage(rgbaImage, true /* nearest */)
	texData.SetTexID(imgui.TextureID(atlasId))
	texData.SetStatus(imgui.TextureStatusOK)

	// Patch up the texture id after the atlas was created.
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
	return GetFont(FontIdentifier{Name: RobotoRegular, Size: 14})
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

var fontsFS fs.StatFS

func init() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := filepath.Dir(path)
	if runtime.GOOS == "darwin" {
		dir = filepath.Clean(filepath.Join(dir, "..", "Resources"))
	}

	// Is there a "fonts" directory in the FS?
	check := func(fs fs.StatFS) bool {
		info, err := fs.Stat("fonts")
		return err == nil && info.IsDir()
	}

	fsys := os.DirFS(dir).(fs.StatFS)
	if check(fsys) {
		fontsFS = fsys
		return
	}

	dir, err = os.Getwd()
	if err != nil {
		panic(err)
	}

	// Try CWD as well the two directories above it.
	for range 3 {
		fsys, ok := os.DirFS(dir).(fs.StatFS)
		if !ok {
			panic("FS from DirFS is not a StatFS?")
		}

		if _, err := fsys.Stat("fonts"); err == nil { // got it
			fontsFS = fsys
			return
		}

		dir = filepath.Join(dir, "..")
	}

	panic("unable to find fonts")
}

func loadFont(name string) []byte {
	b, err := fs.ReadFile(fontsFS, "fonts/"+name)
	if err != nil {
		panic(err)
	}

	zr, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(0))
	if err != nil {
		panic(err)
	}

	b, err = io.ReadAll(zr)
	if err != nil {
		panic(err)
	}

	zr.Close()

	return b
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

func CreateBitmapFontAtlas(r Renderer, p platform.Platform, fontIter iter.Seq2[string, BitmapFont]) []*Font {
	xres, yres := 2048, 1024
	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: xres, Y: yres}})
	x, y := 0, 0

	var newFonts []*Font

	scale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

	for name, bf := range fontIter {
		id := FontIdentifier{
			Name: name,
			Size: bf.Height,
		}

		f := MakeFont(int(scale)*bf.Height, id, nil)
		newFonts = append(newFonts, f)

		if y+bf.Height >= yres {
			panic("Font atlas texture too small")
		}

		for ch, glyph := range bf.Glyphs {
			dx := glyph.Bounds[0] + 1 // pad
			if x+dx > xres {
				// Start a new line
				x = 0
				y += bf.Height + 1
			}

			glyph.rasterize(atlas, x, y)
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

func (glyph BitmapGlyph) rasterize(img *image.RGBA, x0, y0 int) {
	// BitmapGlyphs store their bitmaps as an array of uint32s, where each
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
		Visible:  true,
	}
	f.AddGlyph(ch, g)
}
