// renderer/builders_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import "testing"

// TestAddTextWholePixels checks that text drawn at a fractional position is
// snapped to whole pixels.  Bitmap glyph quads are a whole number of pixels
// in size, so a half-pixel origin would put their edges exactly on pixel
// centers, where the rasterizer's tie-break drops a row of the glyph.
func TestAddTextWholePixels(t *testing.T) {
	f := MakeFont(11, FontIdentifier{Name: "test", Size: 11})
	f.AddGlyph('A', &Glyph{X0: 0, Y0: 2, X1: 8, Y1: 11, AdvanceX: 10, Visible: true})

	var td TextDrawBuilder
	td.AddText("AA", [2]float32{10.25, 100.5}, TextStyle{Font: f})

	buffs, ok := td.regular[f.TexId]
	if !ok {
		t.Fatal("no glyphs emitted")
	}
	if len(buffs.p) != 8 {
		t.Fatalf("emitted %d vertices, expected 8", len(buffs.p))
	}
	for _, p := range buffs.p {
		for i, v := range p {
			if v != float32(int(v)) {
				t.Errorf("vertex %v is not on a whole pixel in %c", p, "xy"[i])
			}
		}
	}
}
