// radar/fonts.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"maps"
	"slices"
	"strconv"

	"github.com/mmp/vice/renderer"
)

// CreateERAMFonts bakes the ERAM PCF bitmap fonts into a texture atlas and
// returns them. The ERAM scope draws everything with them; video maps carry
// their symbol and label sizes in terms of them, so anything else drawing a
// video map faithfully needs them too.
func CreateERAMFonts(r renderer.Renderer, dpiScale float32) []*renderer.Font {
	return renderer.CreateBitmapFontAtlas(r, dpiScale, maps.All(eramBitmapFonts))
}

// FindERAMFont returns the named font at the given size from the result of
// CreateERAMFonts. The names are the PCF filenames, e.g. "EramGeomap-16.pcf".
// It panics if there is no such font, since the callers ask for fonts that
// are known at compile time to be in the atlas.
func FindERAMFont(fonts []*renderer.Font, name string, size int) *renderer.Font {
	idx := slices.IndexFunc(fonts, func(f *renderer.Font) bool { return f.Id.Name == name && f.Id.Size == size })
	if idx == -1 {
		panic(name + " size " + strconv.Itoa(size) + " not found in ERAM fonts")
	}
	return fonts[idx]
}
