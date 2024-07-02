// fonts.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"C"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"sort"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/IconFontCppHeaders"
	"github.com/mmp/imgui-go/v4"
	"github.com/nfnt/resize"
)
import "github.com/mmp/vice/pkg/platform"

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
	fonts map[renderer.FontIdentifier]*renderer.Font

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

// From imgui-go:
// unrealisticLargePointer is used to cast an arbitrary native pointer to a slice.
// Its value is chosen to fit into a 32bit architecture, and still be large
// enough to cover "any" data blob. Note that this value is in bytes.
const unrealisticLargePointer = 1 << 30

func ptrToUint16Slice(p unsafe.Pointer) []uint16 {
	return (*[unrealisticLargePointer / 2]uint16)(p)[:]
}

func fontsInit(r renderer.Renderer, p platform.Platform) {
	lg.Info("Starting to initialize fonts")
	fonts = make(map[renderer.FontIdentifier]*renderer.Font)
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
	faTTF := LoadResource("fonts/Font Awesome 5 Free-Solid-900.otf.zst")
	fabrTTF := LoadResource("fonts/Font Awesome 5 Brands-Regular-400.otf.zst")
	faGlyphRange := glyphRangeForIcons(faUsedIcons)
	faBrandsGlyphRange := glyphRangeForIcons(faBrandsUsedIcons)

	add := func(filename string, mono bool, name string) {
		ttf := LoadResource("fonts/" + filename)
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

			ifont := io.Fonts().AddFontFromMemoryTTFV(ttf, sp, imgui.DefaultFontConfig, imgui.EmptyGlyphRanges)

			config := imgui.NewFontConfig()
			config.SetMergeMode(true)
			// Scale down the font size by an ad-hoc factor to (generally)
			// make the icon sizes match the font's character sizes.
			io.Fonts().AddFontFromMemoryTTFV(faTTF, .8*sp, config, faGlyphRange)
			io.Fonts().AddFontFromMemoryTTFV(fabrTTF, .8*sp, config, faBrandsGlyphRange)

			id := renderer.FontIdentifier{Name: name, Size: size}
			fonts[id] = renderer.MakeFont(int(sp), mono, id, &ifont)
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

	img := io.Fonts().TextureDataRGBA32()
	lg.Infof("Fonts texture used %.1f MB", float32(img.Width*img.Height*4)/(1024*1024))
	rgb8Image := &image.RGBA{
		Pix:    unsafe.Slice((*uint8)(img.Pixels), 4*img.Width*img.Height),
		Stride: 4 * img.Width,
		Rect:   image.Rectangle{Max: image.Point{X: img.Width, Y: img.Height}}}
	atlasId := r.CreateTextureFromImage(rgb8Image, true /* nearest */)
	io.Fonts().SetTextureID(imgui.TextureID(atlasId))

	// Patch up the texture id after the atlas was created with the
	// TextureDataRGBA32 call above.
	for _, font := range fonts {
		font.TexId = atlasId
	}

	// The STARS fonts are bitmaps and don't come in via TTF files so get
	// handled specially.
	initializeSTARSFonts(r, p)

	lg.Info("Finished initializing fonts")
}

// GetAllFonts returns a FontIdentifier slice that gives identifiers for
// all of the available fonts, sorted by font name and then within each
// name, by font size.
func GetAllFonts() []renderer.FontIdentifier {
	var fs []renderer.FontIdentifier
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

func DrawFontPicker(id *renderer.FontIdentifier, label string) (newFont *renderer.Font, changed bool) {
	f := GetAllFonts()
	lastFontName := ""
	if imgui.BeginComboV(label+fmt.Sprintf("##%p", id), id.Name, imgui.ComboFlagsHeightLarge) {
		// Take advantage of the sort order returned by GetAllFonts()--that
		// all fonts of the same name come consecutively.
		for _, font := range f {
			if _, ok := starsFonts[font.Name]; ok {
				// Don't offer up the STARS fonts.
				continue
			}

			if font.Name != lastFontName {
				lastFontName = font.Name
				// Use the 14pt version of the font in the combo box.
				displayFont := GetFont(renderer.FontIdentifier{Name: font.Name, Size: 14})
				imgui.PushFont(displayFont.Ifont)
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

func DrawFontSizeSelector(id *renderer.FontIdentifier) (newFont *renderer.Font, changed bool) {
	if imgui.BeginComboV(fmt.Sprintf("Font Size##%s", id.Name), strconv.Itoa(id.Size), imgui.ComboFlagsHeightLarge) {
		for _, font := range GetAllFonts() {
			if font.Name == id.Name {
				if imgui.SelectableV(strconv.Itoa(font.Size), id.Size == font.Size, 0, imgui.Vec2{}) {
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

func GetFont(id renderer.FontIdentifier) *renderer.Font {
	if font, ok := fonts[id]; ok {
		return font
	} else {
		return nil
	}
}

func GetDefaultFont() *renderer.Font {
	return GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: 14})
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

func initializeSTARSFonts(r renderer.Renderer, p platform.Platform) {
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

	if doublePixels {
		res *= 2
		for name, sf := range starsFonts {
			sf.Width *= 2
			sf.Height *= 2

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
			starsFonts[name] = sf
		}
	}

	atlas := image.NewRGBA(image.Rectangle{Max: image.Point{X: res, Y: res}})

	var newFonts []*renderer.Font

	// Iterate over the fonts, create Font/Glyph objects for them, and copy
	// their bitmaps into the atlas image.
	x, y := 0, 0
	for _, fontName := range util.SortedMapKeys(starsFonts) { // consistent order
		sf := starsFonts[fontName]
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

	atlasId := r.CreateTextureFromImage(atlas, true /* nearest filter */)
	for _, font := range newFonts {
		font.TexId = atlasId
		fonts[font.Id] = font // add them to the global table
	}
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
