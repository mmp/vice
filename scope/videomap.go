// scope/videomap.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

// Map extends videomaps.STARSMap with client-side rendering state. The
// CommandBuffer holds commands to draw the solid-line geometry; dashed
// lines, symbols, and labels are kept on the embedded STARSMap and drawn
// separately at draw time so their stipple pattern / glyph / text can
// account for scope scale and display DPI.
type Map struct {
	videomaps.STARSMap
	CommandBuffer renderer.CommandBuffer
}

// BuildMaps converts []videomaps.STARSMap to Maps, generating CommandBuffers for
// the solid-line portion.
func BuildMaps(maps []videomaps.STARSMap) []Map {
	if len(maps) == 0 {
		return nil
	}

	out := make([]Map, len(maps))
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, m := range maps {
		out[i] = Map{STARSMap: m}

		ld.Reset()
		hasSolid := false
		for _, line := range m.Lines {
			if line.Style != videomaps.LineStyleSolid {
				continue // dashed lines drawn separately at draw time
			}
			fl := util.MapSlice(line.Points, func(p math.Point2LL) [2]float32 { return p })
			ld.AddLineStrip(fl)
			hasSolid = true
		}
		if hasSolid {
			ld.GenerateCommands(&out[i].CommandBuffer)
		}
	}

	return out
}

// Video map categories, as stored in videomaps.STARSMap.Category. VideoMapCurrent
// is not a category a map carries; it selects the maps currently displayed.
const (
	VideoMapNoCategory = iota - 1
	VideoMapGeographicMaps
	VideoMapControlledAirspace
	VideoMapRunwayExtensions
	VideoMapDangerAreas
	VideoMapAerodromes
	VideoMapGeneralAviation
	VideoMapSIDsSTARs
	VideoMapMilitary
	VideoMapGeographicPoints
	VideoMapProcessingAreas
	VideoMapCurrent
	VideoMapNumCategories
)

// VideoMapCategoryNames gives the full name of each category, as the STARS
// MAPS list displays it.
var VideoMapCategoryNames = [VideoMapNumCategories]string{
	VideoMapGeographicMaps:     "GEOGRAPHIC MAPS",
	VideoMapControlledAirspace: "CONTROLLED AIRSPACE",
	VideoMapRunwayExtensions:   "RUNWAY EXTENSIONS",
	VideoMapDangerAreas:        "DANGER AREAS",
	VideoMapAerodromes:         "AERODROMES",
	VideoMapGeneralAviation:    "GENERAL AVIATION",
	VideoMapSIDsSTARs:          "SIDS/STARS",
	VideoMapMilitary:           "MILITARY",
	VideoMapGeographicPoints:   "GEOGRAPHIC POINTS",
	VideoMapProcessingAreas:    "PROCESSING AREAS",
	VideoMapCurrent:            "MAPS",
}

// MapFonts supplies the fonts used to draw a video map's symbols and
// labels. Both are selected by the size byte the map carries for each
// feature. The symbol font's glyphs must be laid out as the EramGeomap
// bitmap fonts are; see symbolGlyphIndex.
type MapFonts interface {
	MapSymbolFont(size int) *renderer.Font
	MapLabelFont(size int) *renderer.Font
}

// DrawMapFeatures draws a video map's lines, symbols, and labels in window
// coordinates. bcgRGB gives the color for each BCG index; index 0 and any
// index without an adapted BCG stay black and so draw nothing. solidLineBuf
// is scratch space the caller keeps across maps to avoid reallocating it
// for every line.
func DrawMapFeatures(lines []videomaps.Line, symbols []videomaps.Symbol, labels []videomaps.Label,
	bcgRGB *[256]renderer.RGB, fonts MapFonts, transforms Transformations,
	ld *renderer.ColoredLinesDrawBuilder, td *renderer.TextDrawBuilder, solidLineBuf *[][2]float32) {
	for _, line := range lines {
		color := bcgRGB[line.BCGIndex]
		if line.Style == videomaps.LineStyleSolid {
			*solidLineBuf = (*solidLineBuf)[:0]
			for _, p := range line.Points {
				*solidLineBuf = append(*solidLineBuf, transforms.WindowFromLatLongP(p))
			}
			ld.AddLineStrip(*solidLineBuf, color)
		} else {
			pattern := dashPatternPixels(line.Style)
			if pattern == nil {
				continue
			}
			for i := 0; i+1 < len(line.Points); i++ {
				p0 := transforms.WindowFromLatLongP(line.Points[i])
				p1 := transforms.WindowFromLatLongP(line.Points[i+1])
				ld.AddDashPattern(p0, p1, pattern, color)
			}
		}
	}

	for _, s := range symbols {
		if font := fonts.MapSymbolFont(int(s.Size)); font != nil {
			color := bcgRGB[s.BCGIndex]
			pw := transforms.WindowFromLatLongP(s.P)
			td.AddTextCentered(string(SymbolGlyphIndex[s.Style]), pw,
				renderer.TextStyle{Font: font, Color: color})
		}
	}

	for _, l := range labels {
		if font := fonts.MapLabelFont(int(l.Size)); font != nil {
			color := bcgRGB[l.BCGIndex]
			pw := transforms.WindowFromLatLongP(l.P)
			pw[0] += float32(l.XOffset)
			pw[1] += float32(l.YOffset)
			style := renderer.TextStyle{Font: font, Color: color}
			if l.Opaque {
				style.DrawBackground = true
				style.BackgroundColor = renderer.RGB{}
			}
			td.AddText(l.Text, pw, style)
			if l.Underline {
				ext := font.LayoutBounds(l.Text, 0)
				// In window space y grows upward; AddText takes the upper-left corner, so the
				// baseline is at pw[1] - h.  Drop one more pixel so the underline sits just
				// below.
				y := pw[1] - ext.Height() - 1
				ld.AddLine([2]float32{pw[0], y}, [2]float32{pw[0] + ext.Width(), y}, color)
			}
		}
	}
}

// Dash patterns in window-space pixels. These and SymbolGlyphIndex are
// exported and mutable so that the debug UI in the ERAM settings window can
// tune them live and print the result to paste back here.
var (
	ShortDashedPattern       = []float32{10, 14}
	LongDashedPattern        = []float32{24, 24}
	LongDashShortDashPattern = []float32{24, 11, 12, 12}
)

func dashPatternPixels(s videomaps.LineStyle) []float32 {
	switch s {
	case videomaps.LineStyleShortDashed:
		return ShortDashedPattern
	case videomaps.LineStyleLongDashed:
		return LongDashedPattern
	case videomaps.LineStyleLongDashShortDash:
		return LongDashShortDashPattern
	default:
		return nil
	}
}

// SymbolGlyphIndex maps each SymbolStyle to the unicode codepoint of its
// glyph in the EramGeomap-{16,18,20}.pcf bitmap fonts.
var SymbolGlyphIndex = map[videomaps.SymbolStyle]rune{
	videomaps.SymbolStyleVOR:                 0x0B,
	videomaps.SymbolStyleNDB:                 0x0B,
	videomaps.SymbolStyleTACAN:               0x0F,
	videomaps.SymbolStyleVOR_TACAN:           0x00,
	videomaps.SymbolStyleDME:                 0x04,
	videomaps.SymbolStyleRNAV:                0x09,
	videomaps.SymbolStyleRNAVOnlyWaypoint:    0x07,
	videomaps.SymbolStyleAirport:             0x0D,
	videomaps.SymbolStyleSatelliteAirport:    0x02,
	videomaps.SymbolStyleEmergencyAirport:    0x04,
	videomaps.SymbolStyleHeliport:            0x0B,
	videomaps.SymbolStyleOtherWaypoints:      0x0C,
	videomaps.SymbolStyleAirwayIntersections: 0x09,
	videomaps.SymbolStyleIAF:                 0x0D,
	videomaps.SymbolStyleObstruction1:        0x00,
	videomaps.SymbolStyleObstruction2:        0x06,
	videomaps.SymbolStyleNuclear:             0x03,
	videomaps.SymbolStyleRadar:               0x05,
}
