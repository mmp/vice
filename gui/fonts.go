// gui/fonts.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gui

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/mmp/vice/log"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/klauspost/compress/zstd"
	"github.com/mmp/IconFontCppHeaders"
)

// Fonts names the TTF fonts that InitFonts bakes; pass one to GetFont.
var Fonts = struct {
	RobotoRegular        string
	RobotoBold           string
	RobotoItalic         string
	RobotoBoldItalic     string
	RobotoMono           string
	RobotoMonoItalic     string
	FlightStripPrinter   string
	LargeFontAwesomeOnly string
}{
	RobotoRegular:        "Roboto Regular",
	RobotoBold:           "Roboto Bold",
	RobotoItalic:         "Roboto Italic",
	RobotoBoldItalic:     "Roboto Bold Italic",
	RobotoMono:           "Roboto Mono",
	RobotoMonoItalic:     "Roboto Mono Italic",
	FlightStripPrinter:   "Flight Strip Printer",
	LargeFontAwesomeOnly: "LargeFontAwesomeOnly",
}

const (
	// referenceFontSize is the size the font sources are registered at.
	// Since imgui 1.92 a font is not tied to a size--glyphs are baked on
	// demand at whatever size it is pushed at--so this only fixes the
	// scale that merged sources are measured against.
	referenceFontSize = 16

	// iconSizeScale shrinks the FontAwesome icons merged into the text
	// fonts so that they come out roughly the size of a character.
	iconSizeScale = 0.8
)

var ttfPinner runtime.Pinner

// Font is one of the TTF fonts, baked by imgui at a single pixel size.
// imgui owns the glyphs and the atlas they live in, so drawing with a Font
// means pushing it onto imgui's font stack and using imgui's text calls.
type Font struct {
	ifont imgui.Font
	// Size is the size the font was baked at, in pixels.
	Size int
	Id   FontIdentifier
}

// FontIdentifier names a font by its name and its size in points.
type FontIdentifier struct {
	Name string
	Size int
}

// PushFont makes f current for subsequent imgui text, at the size it was
// baked at. Each call must be paired with a PopFont.
func PushFont(f *Font) {
	imgui.PushFont(&f.ifont, float32(f.Size))
}

// PopFont restores the font that was current before the matching PushFont.
func PopFont() {
	imgui.PopFont()
}

// Icons gives the UTF-8 encoding of each FontAwesome icon that is baked
// into the regular fonts.
var Icons = struct {
	ArrowDown           string
	ArrowLeft           string
	ArrowRight          string
	ArrowUp             string
	ArrowsAlt           string
	Bolt                string
	Book                string
	Bug                 string
	CaretDown           string
	CaretRight          string
	CheckSquare         string
	ClipboardList       string
	Cloud               string
	CloudRain           string
	CloudShowersHeavy   string
	CloudSun            string
	Cog                 string
	Comment             string
	CompressAlt         string
	Copyright           string
	Discord             string
	DrawPolygon         string
	ExclamationTriangle string
	ExpandAlt           string
	FastForward         string
	File                string
	Folder              string
	Github              string
	HandPointLeft       string
	Home                string
	InfoCircle          string
	Keyboard            string
	LevelUpAlt          string
	Lock                string
	Microphone          string
	Mouse               string
	PauseCircle         string
	PlayCircle          string
	QuestionCircle      string
	PlaneDeparture      string
	Redo                string
	Ruler               string
	Smog                string
	Snowflake           string
	Square              string
	StopCircle          string
	Sun                 string
	Thumbtack           string
	Trash               string
	Wind                string
}{
	ArrowDown:           faUsedIcons["ArrowDown"],
	ArrowLeft:           faUsedIcons["ArrowLeft"],
	ArrowRight:          faUsedIcons["ArrowRight"],
	ArrowUp:             faUsedIcons["ArrowUp"],
	ArrowsAlt:           faUsedIcons["ArrowsAlt"],
	Bolt:                faUsedIcons["Bolt"],
	Book:                faUsedIcons["Book"],
	Bug:                 faUsedIcons["Bug"],
	CaretDown:           faUsedIcons["CaretDown"],
	CaretRight:          faUsedIcons["CaretRight"],
	CheckSquare:         faUsedIcons["CheckSquare"],
	ClipboardList:       faUsedIcons["ClipboardList"],
	Cloud:               faUsedIcons["Cloud"],
	CloudRain:           faUsedIcons["CloudRain"],
	CloudShowersHeavy:   faUsedIcons["CloudShowersHeavy"],
	CloudSun:            faUsedIcons["CloudSun"],
	Cog:                 faUsedIcons["Cog"],
	Comment:             faUsedIcons["Comment"],
	CompressAlt:         faUsedIcons["CompressAlt"],
	Copyright:           faUsedIcons["Copyright"],
	Discord:             faBrandsUsedIcons["Discord"],
	DrawPolygon:         faUsedIcons["DrawPolygon"],
	ExclamationTriangle: faUsedIcons["ExclamationTriangle"],
	ExpandAlt:           faUsedIcons["ExpandAlt"],
	FastForward:         faUsedIcons["FastForward"],
	File:                faUsedIcons["File"],
	Folder:              faUsedIcons["Folder"],
	Github:              faBrandsUsedIcons["Github"],
	HandPointLeft:       faUsedIcons["HandPointLeft"],
	Home:                faUsedIcons["Home"],
	InfoCircle:          faUsedIcons["InfoCircle"],
	Keyboard:            faUsedIcons["Keyboard"],
	LevelUpAlt:          faUsedIcons["LevelUpAlt"],
	Lock:                faUsedIcons["Lock"],
	Microphone:          faUsedIcons["Microphone"],
	Mouse:               faUsedIcons["Mouse"],
	PauseCircle:         faUsedIcons["PauseCircle"],
	PlayCircle:          faUsedIcons["PlayCircle"],
	QuestionCircle:      faUsedIcons["QuestionCircle"],
	PlaneDeparture:      faUsedIcons["PlaneDeparture"],
	Redo:                faUsedIcons["Redo"],
	Ruler:               faUsedIcons["Ruler"],
	Smog:                faUsedIcons["Smog"],
	Snowflake:           faUsedIcons["Snowflake"],
	Square:              faUsedIcons["Square"],
	StopCircle:          faUsedIcons["StopCircle"],
	Sun:                 faUsedIcons["Sun"],
	Thumbtack:           faUsedIcons["Thumbtack"],
	Trash:               faUsedIcons["Trash"],
	Wind:                faUsedIcons["Wind"],
}

var (
	// imguiFonts holds the registered font for each family. imgui bakes
	// their glyphs on demand, so one registration covers every size.
	imguiFonts map[string]imgui.Font

	// fonts caches the Font for each (family, size) asked for so far;
	// GetFont mints them as they are needed.
	fonts map[FontIdentifier]*Font

	// fontDPIScale is the display scaling InitFonts was given; font sizes
	// are converted to pixels with it.
	fontDPIScale float32

	// This and the following faBrandsUsedIcons map are what drives
	// determining which icons are copied into regular fonts; see
	// InitFonts() below.
	faUsedIcons map[string]string = map[string]string{
		"ArrowDown":           fontAwesomeString("ArrowDown"),
		"ArrowLeft":           fontAwesomeString("ArrowLeft"),
		"ArrowRight":          fontAwesomeString("ArrowRight"),
		"ArrowUp":             fontAwesomeString("ArrowUp"),
		"ArrowsAlt":           fontAwesomeString("ArrowsAlt"),
		"Bolt":                fontAwesomeString("Bolt"),
		"Book":                fontAwesomeString("Book"),
		"Bug":                 fontAwesomeString("Bug"),
		"CaretDown":           fontAwesomeString("CaretDown"),
		"CaretRight":          fontAwesomeString("CaretRight"),
		"CheckSquare":         fontAwesomeString("CheckSquare"),
		"ClipboardList":       fontAwesomeString("ClipboardList"),
		"Cloud":               fontAwesomeString("Cloud"),
		"CloudRain":           fontAwesomeString("CloudRain"),
		"CloudShowersHeavy":   fontAwesomeString("CloudShowersHeavy"),
		"CloudSun":            fontAwesomeString("CloudSun"),
		"Comment":             fontAwesomeString("Comment"),
		"CompressAlt":         fontAwesomeString("CompressAlt"),
		"Cog":                 fontAwesomeString("Cog"),
		"Copyright":           fontAwesomeString("Copyright"),
		"DrawPolygon":         fontAwesomeString("DrawPolygon"),
		"ExclamationTriangle": fontAwesomeString("ExclamationTriangle"),
		"ExpandAlt":           fontAwesomeString("ExpandAlt"),
		"FastForward":         fontAwesomeString("FastForward"),
		"File":                fontAwesomeString("File"),
		"Folder":              fontAwesomeString("Folder"),
		"HandPointLeft":       fontAwesomeString("HandPointLeft"),
		"Home":                fontAwesomeString("Home"),
		"InfoCircle":          fontAwesomeString("InfoCircle"),
		"Keyboard":            fontAwesomeString("Keyboard"),
		"LevelUpAlt":          fontAwesomeString("LevelUpAlt"),
		"Lock":                fontAwesomeString("Lock"),
		"Microphone":          fontAwesomeString("Microphone"),
		"Mouse":               fontAwesomeString("Mouse"),
		"PauseCircle":         fontAwesomeString("PauseCircle"),
		"PlayCircle":          fontAwesomeString("PlayCircle"),
		"QuestionCircle":      fontAwesomeString("QuestionCircle"),
		"PlaneDeparture":      fontAwesomeString("PlaneDeparture"),
		"Redo":                fontAwesomeString("Redo"),
		"Ruler":               fontAwesomeString("Ruler"),
		"Smog":                fontAwesomeString("Smog"),
		"Snowflake":           fontAwesomeString("Snowflake"),
		"Square":              fontAwesomeString("Square"),
		"StopCircle":          fontAwesomeString("StopCircle"),
		"Sun":                 fontAwesomeString("Sun"),
		"Thumbtack":           fontAwesomeString("Thumbtack"),
		"Trash":               fontAwesomeString("Trash"),
		"Wind":                fontAwesomeString("Wind"),
	}
	faBrandsUsedIcons map[string]string = map[string]string{
		"Discord": fontAwesomeBrandsString("Discord"),
		"Github":  fontAwesomeBrandsString("Github"),
	}
)

// InitFonts loads the TTF fonts and registers them with imgui, which
// rasterizes their glyphs on demand into an atlas that it owns.
func InitFonts(dpiScale float32, lg *log.Logger) {
	initFontsFS()
	lg.Info("Starting to initialize fonts")
	fontDPIScale = dpiScale
	imguiFonts = make(map[string]imgui.Font)
	fonts = make(map[FontIdentifier]*Font)
	imio := imgui.CurrentIO()

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

	// Helper to add TTF data to imgui
	addTTF := func(ttf []byte, sp float32, fconfig *imgui.FontConfig, r imgui.GlyphRange) *imgui.Font {
		ttfPinner.Pin(&ttf[0])
		return imio.Fonts().AddFontFromMemoryTTFV(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)),
			sp, fconfig, r.Data())
	}

	// addFamily registers one font family with imgui, with the
	// FontAwesome icons merged into it so that they can be used inline in
	// its text. A nil ttf gives a font of the icons alone. A merged
	// source is scaled by the ratio of its size to the first source's, so
	// registering the icons at a fraction of referenceFontSize holds that
	// fraction at every size the font is later baked at.
	addFamily := func(ttf []byte, name string) {
		var ifont *imgui.Font
		iconScale := float32(iconSizeScale)
		if ttf != nil {
			ttfPinner.Pin(&ttf[0])
			ifont = imio.Fonts().AddFontFromMemoryTTFV(uintptr(unsafe.Pointer(&ttf[0])), int32(len(ttf)),
				referenceFontSize, imgui.NewFontConfig(), nil)
		} else {
			// There is no text here for the icons to match the size of.
			iconScale = 1
		}

		config := imgui.NewFontConfig()
		config.SetMergeMode(ifont != nil)
		icons := addTTF(faTTF, iconScale*referenceFontSize, config, faGlyphRange)
		if ifont == nil {
			ifont = icons
		}

		config.SetMergeMode(true)
		addTTF(fabrTTF, iconScale*referenceFontSize, config, faBrandsGlyphRange)

		imguiFonts[name] = *ifont
	}

	for fn, name := range map[string]string{
		"Roboto-Regular.ttf.zst":          Fonts.RobotoRegular,
		"Roboto-Bold.ttf.zst":             Fonts.RobotoBold,
		"Roboto-Italic.ttf.zst":           Fonts.RobotoItalic,
		"Roboto-BoldItalic.ttf.zst":       Fonts.RobotoBoldItalic,
		"RobotoMono-Medium.ttf.zst":       Fonts.RobotoMono,
		"RobotoMono-MediumItalic.ttf.zst": Fonts.RobotoMonoItalic,
		"Flight-Strip-Printer.ttf.zst":    Fonts.FlightStripPrinter} {
		addFamily(loadFont(fn), name)
	}
	// A font of FontAwesome icons alone, for the weather icons.
	addFamily(nil, Fonts.LargeFontAwesomeOnly)

	lg.Info("Finished initializing fonts")
}

func DrawFontSizeSelector(id *FontIdentifier) (newFont *Font, changed bool) {
	if imgui.BeginComboV(fmt.Sprintf("Font Size##%s", id.Name), strconv.Itoa(id.Size), imgui.ComboFlagsHeightLarge) {
		for _, size := range AvailableFontSizes(id.Name) {
			if imgui.SelectableBoolV(strconv.Itoa(size), id.Size == size, 0, imgui.Vec2{}) {
				id.Size = size
				newFont = GetFont(id.Name, size)
				changed = true
			}
		}
		imgui.EndCombo()
	}
	return
}

// GetFont returns the named font at the given size in points, or nil if
// there is no font with that name. imgui bakes glyphs on demand, so any
// size works, not just the ones AvailableFontSizes offers.
func GetFont(name string, size int) *Font {
	id := FontIdentifier{Name: name, Size: size}
	if f, ok := fonts[id]; ok {
		return f
	}

	ifont, ok := imguiFonts[name]
	if !ok {
		return nil
	}

	f := &Font{ifont: ifont, Size: int(calcPixelSize(size)), Id: id}
	fonts[id] = f
	return f
}

// calcPixelSize returns the size in pixels to bake a font of the given
// size in points at.
func calcPixelSize(size int) float32 {
	sp := float32(size)
	if runtime.GOOS == "windows" {
		if fontDPIScale > 1 {
			sp *= fontDPIScale
		} else {
			// Fix font sizes to account for Windows using 96dpi but
			// everyone else using 72...
			sp *= 96. / 72.
		}
		sp = float32(int(sp + 0.5))
	}
	return sp
}

func GetDefaultFont() *Font {
	return GetFont(Fonts.RobotoRegular, 14)
}

func fontAwesomeString(id string) string {
	s, ok := IconFontCppHeaders.FontAwesome5.Icons[id]
	if !ok {
		panic(fmt.Sprintf("%s: FA string unknown", id))
	}
	return s
}

func fontAwesomeBrandsString(id string) string {
	s, ok := IconFontCppHeaders.FontAwesome5Brands.Icons[id]
	if !ok {
		panic(fmt.Sprintf("%s: FA string unknown", id))
	}
	return s
}

// FixedFontSize returns a font size for the fixed-width font that is
// slightly larger than the given base size, by picking the size one slot
// ahead in the available sizes for the font.
func FixedFontSize(baseSize int) int {
	sizes := AvailableFontSizes(Fonts.RobotoMono)
	idx := slices.IndexFunc(sizes, func(s int) bool { return s >= baseSize })
	idx = min(idx+1, len(sizes)-1)
	return sizes[idx]
}

// offeredFontSizes are the sizes the font size selectors offer. Any size
// bakes on demand, so this is a menu of sensible choices rather than a
// limit on what GetFont accepts.
var offeredFontSizes = []int{6, 7, 8, 9, 10, 11, 12, 13, 14, 16, 18, 20, 22, 24, 28}

// AvailableFontSizes returns the sizes offered for the named font, or nil
// if there is no font with that name.
func AvailableFontSizes(name string) []int {
	if _, ok := imguiFonts[name]; !ok {
		return nil
	}
	return offeredFontSizes
}

var fontsFS fs.StatFS

func initFontsFS() {
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
