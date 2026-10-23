// eram/drawview_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"image"
	"slices"
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

// fakeRenderer lets the ERAM bitmap fonts load without a GPU.
type fakeRenderer struct{ renderer.Renderer }

func (fakeRenderer) CreateTextureFromImage(image.Image, bool) uint32 { return 1 }

// wrappingRows returns n rows whose bodies each wrap to three lines of a
// 24-character column: two 11-character words fit on a line.
func wrappingRows(n int) []Row {
	var rows []Row
	for i := range n {
		w := strings.Repeat(string(rune('A'+i)), 11)
		rows = append(rows, Row{Body: strings.Repeat(w+" ", 6)})
	}
	return rows
}

func makeRowTestScope() *Scope {
	ep, _ := makeToolbarTestScope()
	font := scope.FindERAMFont(scope.CreateERAMFonts(fakeRenderer{}, 1), "EramText-11.pcf", 13)
	for i := range ep.systemFont {
		ep.systemFont[i] = font
	}
	return ep
}

// The first row's lines that are scrolled off are dropped and the rest are
// drawn as continuation lines; MaxLines counts the lines shown.
func TestRowListSkipLines(t *testing.T) {
	ep := makeRowTestScope()
	font := ep.ERAMFont(2)
	rows := wrappingRows(2)
	l := RowList{Font: font, LineHeight: lineHeight(font), Width: float32(26) * charWidth(font),
		Rows: rows, MaxLines: 4, SkipLines: 1}
	l.Measure()

	first, second := l.wrap(rows[0]), l.wrap(rows[1])
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("rows wrapped to %q and %q; want 3 lines each", first, second)
	}
	if l.visibleCount != 2 || !l.measured[0].continued || l.measured[1].continued ||
		!slices.Equal(l.measured[0].lines, first[1:]) || !slices.Equal(l.measured[1].lines, second[:2]) {
		t.Errorf("got %d rows shown, %+v; want the last 2 lines of %q and the first 2 of %q",
			l.visibleCount, l.measured, first, second)
	}
}

// A view's scroll offset counts the lines its rows wrap to, so that it
// scrolls to the end of the last row (4.2.20).
func TestViewRowSourceScrollsByLine(t *testing.T) {
	ep := makeRowTestScope()
	var scroll ViewScrollState
	shown := func(offset int) (maxOffset int, idx []int) {
		scroll.Offset = offset
		rows := wrappingRows(3)
		for i := range rows {
			rows[i].OnClick = func(RowClick) {}
		}
		v := View{ID: "wx", RowSource: &ViewRowSource{Rows: rows, FontIndex: 2, ContentChars: 24,
			MaxCols: 1, VisibleRows: 5, RowSpacing: RowSpacingAiry, ScrollState: &scroll}}
		ep.applyRowSource(&v, ep.ERAMFont(2))
		body := math.Extent2D{P1: [2]float32{v.Width, v.BodyHeight}}
		for _, a := range v.pickRows(body) {
			idx = append(idx, a.idx)
		}
		return v.Scroll.MaxOffset, idx
	}

	// Nine lines, five shown.
	for _, tc := range []struct {
		offset, clamped int
		idx             []int
	}{
		{0, 0, []int{0, 1}},
		{2, 2, []int{0, 1, 2}},
		{3, 3, []int{1, 2}},
		{100, 4, []int{1, 2}},
	} {
		maxOffset, idx := shown(tc.offset)
		if maxOffset != 4 || scroll.Offset != tc.clamped || !slices.Equal(idx, tc.idx) {
			t.Errorf("offset %d: got max offset %d, offset %d, rows %v; want 4, %d, %v",
				tc.offset, maxOffset, scroll.Offset, idx, tc.clamped, tc.idx)
		}
	}
}
