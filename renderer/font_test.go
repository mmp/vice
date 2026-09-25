// renderer/font_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package renderer

import (
	"slices"
	"testing"

	"github.com/mmp/vice/math"
)

func makeBitmap(rows ...string) []uint32 {
	var bitmap []uint32
	for _, row := range rows {
		var line uint32
		for x, c := range row {
			if c == '#' {
				line |= 1 << uint(31-x)
			}
		}
		bitmap = append(bitmap, line)
	}
	return bitmap
}

// TestTrimmedGlyphInk checks that a glyph stored as a padded cell is trimmed
// to its set pixels without moving them within the cell, so that InkBounds
// bounds the drawn pixels, and that a blank glyph has no ink.
func TestTrimmedGlyphInk(t *testing.T) {
	bf := BitmapFont{Height: 6}
	cell := BitmapGlyph{StepX: 5, Bounds: [2]int{5, 6}, Bitmap: makeBitmap(
		".....",
		".##..",
		".#.#.",
		".##..",
		".....",
		".....")}
	blank := BitmapGlyph{StepX: 5, Bounds: [2]int{5, 6}, Bitmap: makeBitmap("", "", "", "", "", "")}

	trimmed := cell.trimmed()
	if want := makeBitmap("##.", "#.#", "##."); !slices.Equal(trimmed.Bitmap, want) {
		t.Errorf("trimmed bitmap %b, expected %b", trimmed.Bitmap, want)
	}

	f := MakeFont(bf.Height, FontIdentifier{Name: "test", Size: bf.Height})
	trimmed.addToFont('D', 0, 0, 64, 64, bf, f, 1)
	blank.trimmed().addToFont(' ', 0, 0, 64, 64, bf, f, 1)

	g := f.LookupGlyph('D')
	if g.X0 != 1 || g.X1 != 4 || g.Y0 != 1 || g.Y1 != 4 || !g.Visible {
		t.Errorf("glyph quad (%v,%v)-(%v,%v) visible %v, expected (1,1)-(4,4) visible",
			g.X0, g.Y0, g.X1, g.Y1, g.Visible)
	}
	if g.AdvanceX != 5 {
		t.Errorf("AdvanceX %v, expected 5", g.AdvanceX)
	}

	want := math.Extent2D{P0: [2]float32{1, -4}, P1: [2]float32{4, -1}}
	if ink := f.InkBounds("D", 0); ink != want {
		t.Errorf("InkBounds(\"D\") = %v, expected %v", ink, want)
	}
	wantSpaced := math.Extent2D{P0: [2]float32{6, -4}, P1: [2]float32{9, -1}}
	if ink := f.InkBounds(" D ", 0); ink != wantSpaced {
		t.Errorf("InkBounds(\" D \") = %v, expected %v", ink, wantSpaced)
	}
	if g := f.LookupGlyph(' '); g.Visible {
		t.Error("blank glyph is visible")
	}
	if !f.InkBounds(" ", 0).IsEmpty() {
		t.Error("InkBounds of a blank glyph is not empty")
	}
}
