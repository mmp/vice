// scope/assets_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// TestSystemMaps checks that each adapted filter kind contributes an "all"
// map followed by one per region, that the ids follow the STARS numbering
// convention, and that the maps whose geometry comes from the aviation
// database show up.
func TestSystemMaps(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the aviation database")
	}
	db.InitDB()

	region := func(id, description string, center math.Point2LL) sim.FilterRegion {
		return sim.FilterRegion{
			AirspaceVolume: av.AirspaceVolume{
				Id:          id,
				Description: description,
				Type:        av.AirspaceVolumeCircle,
				Center:      av.ScenarioPoint2LL{Point2LL: center},
				Radius:      5,
			},
		}
	}

	// JFK, near enough to New York's class B for it to be picked up.
	center := math.Point2LL{-73.7786, 40.6398}
	var fa sim.FacilityAdaptation
	fa.Filters.InhibitCA = sim.FilterRegions{
		region("noca1", "kjfk noca", center),
		region("noca2", "klga noca", math.Point2LL{-73.8726, 40.7772}),
	}
	fa.Filters.VFRInhibit = sim.FilterRegions{region("vfri", "vfr inhibit", center)}
	fa.RadarSites = map[string]*av.RadarSite{
		"JFK": {Position: av.ScenarioPoint2LL{Point2LL: center}, PrimaryRange: 60, SecondaryRange: 120},
	}

	maps := SystemMaps(SystemMapSpec{
		Facility:          "N90",
		Center:            center,
		NmPerLongitude:    45.7,
		MagneticVariation: -13,
		Adaptation:        &fa,
		Airports:          map[av.ICAOAirportCode]*av.Airport{},
		ArrivalAirports:   map[av.ICAOAirportCode]any{},
	})

	byLabel := make(map[string]Map)
	for _, m := range maps {
		if _, ok := byLabel[m.Label]; ok {
			t.Errorf("%s: duplicate system map label", m.Label)
		}
		byLabel[m.Label] = m
		if m.Category != VideoMapProcessingAreas {
			t.Errorf("%s: category %d, expected %d", m.Label, m.Category, VideoMapProcessingAreas)
		}
	}

	// An "all" map plus one per region, in that order and with consecutive ids.
	for _, want := range []string{"CASU", "NOCA1", "NOCA2", "VFRINH", "VFRI"} {
		if _, ok := byLabel[want]; !ok {
			t.Errorf("%s: expected system map not generated", want)
		}
	}
	if casu, noca1 := byLabel["CASU"], byLabel["NOCA1"]; casu.Id != 700 || noca1.Id != 701 {
		t.Errorf("filter region ids are %d and %d, expected 700 and 701", casu.Id, noca1.Id)
	}
	if got := byLabel["NOCA1"].Name; got != "KJFK NOCA" {
		t.Errorf("region map name is %q, expected the uppercased description", got)
	}

	if m, ok := byLabel["N90 MVA"]; !ok {
		t.Error("no MVA map generated")
	} else if len(db.DB.MVAs["N90"]) > 0 && len(m.CommandBuffer.Buf) == 0 {
		t.Error("MVA map has no geometry though N90 has MVAs")
	}

	if m, ok := byLabel["JFKRCM"]; !ok {
		t.Error("no radar coverage map generated")
	} else if m.Id != 801 {
		t.Errorf("radar coverage map id is %d, expected 801", m.Id)
	}

	// New York's class B is well within 75nm of JFK.
	if !slices.ContainsFunc(maps, func(m Map) bool { return m.Name == "NEW YORK CLASS B" }) {
		t.Error("nearby class B airspace was not generated")
	}
}

// TestBitmapFontDigitWidths checks that the digits of each text font are
// drawn to a common width.  The fonts use tabular figures so that columns of
// numbers line up, and a digit that is narrower than its neighbours means the
// PCF conversion produced a malformed glyph: the 8 in EramText-9.pcf was two
// pixels too narrow, which made data block lines ending in 8 look truncated.
// 1 is excluded since it is legitimately narrow in every size.
func TestBitmapFontDigitWidths(t *testing.T) {
	fonts, err := renderer.LoadBitmapFonts(util.LoadFontBytes("eram-bitmaps.msgpack.zst"))
	if err != nil {
		t.Fatal(err)
	}
	for name, bf := range fonts {
		widths := make(map[int][]string)
		for ch := '0'; ch <= '9'; ch++ {
			if ch != '1' && int(ch) < len(bf.Glyphs) && bf.Glyphs[ch].StepX != 0 {
				w := bf.Glyphs[ch].Bounds[0]
				widths[w] = append(widths[w], string(ch))
			}
		}
		if len(widths) > 1 {
			t.Errorf("%s: digits are drawn to %d different widths: %v", name, len(widths), widths)
		}
	}
}
