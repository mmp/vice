package eram

import (
	"maps"
	"runtime"
	"slices"
	"strconv"

	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
)

func (ep *ERAMPane) ERAMFont(size int) *renderer.Font {
	if runtime.GOOS == "darwin" {
		return ep.systemFont[10] // Smaller size for macOS
	}
	if size == 4 {
		size = 3 // Missing one font, so skip it for now
	}
	size -= 1
	return ep.systemFont[size]

}
func (ep *ERAMPane) ERAMToolbarFont() *renderer.Font {
	if runtime.GOOS == "darwin" {
		return ep.systemFont[0] // Smaller size for macOS
	}
	return ep.systemFont[1]
}

func (ep *ERAMPane) ERAMInputFont() *renderer.Font {
	return ep.systemFont[1]
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
	ep.systemFont[0] = get("EramText-9.pcf", 11)
	ep.systemFont[1] = get("EramText-11.pcf", 13)
	ep.systemFont[2] = get("EramText-14.pcf", 17)
	ep.systemFont[3] = get("EramText-16.pcf", 18)
	ep.systemFont[4] = get("EramText-16.pcf", 18)

	ep.systemFont[5] = get("EramTargets-16.pcf", 15)
	// ep.systemFont[6] = get("EramGeomap-16.pcf", 15)
	// ep.systemFont[7] = get("EramGeomap-18.pcf", 17)
	// ep.systemFont[8] = get("EramGeomap-20.pcf", 19)
	ep.systemFont[9] = get("EramTracks-16.pcf", 15)
	ep.systemFont[10] = get("EramText-8.pcf", 11)

}

func createFontAtlas(r renderer.Renderer, p platform.Platform) []*renderer.Font {
	return renderer.CreateBitmapFontAtlas(r, p, maps.All(eramFonts))
}
