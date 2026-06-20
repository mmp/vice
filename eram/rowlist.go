// eram/rowlist.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
)

// RowList is a vertical-stack layout helper for a View body. The caller
// describes rows declaratively (badge, label, body text) and the helper
// handles wrapping, positioning, and per-row extents. Use Measure() to get
// the body height before constructing the View, then pass Draw() as the
// View.Body callback.

// Badge is an optional indicator on the left of a row: filled box with border.
type Badge struct {
	Width  float32
	Height float32
	Fill   renderer.RGB
	Border renderer.RGB
}

// Row is one row in a RowList. The layout has three logical regions:
//
//	[ Badge ][ Label ]  [ Body... wrapped to multiple lines ]
//	                    [ ...continuation aligned with Label start ]
//
// If Centered is true, Label is centered across the row width and Badge/Body
// are ignored (used for group-header rows).
// If AltText is set, it replaces Body as a single non-wrapping line (e.g. "-M-").
// If Body is empty (and no AltText), the row has just Badge + Label.
//
// If SpacerHeight > 0, the row renders nothing and just takes up that much
// vertical space (used for inter-group gaps in CRR list mode).
type Row struct {
	Badge *Badge
	Label string
	Body  string

	AltText  string
	Centered bool

	// ID is the selection identifier matched against RowList.SelectedID. It
	// is independent of Label so rows whose visible text lives entirely in
	// Body or AltText can still participate in selection.
	ID string

	Color renderer.RGB

	SpacerHeight float32

	// AfterDraw is called after the row content is drawn, with the row's
	// full extent and the baseline position where the body text starts.
	// Used by callers to add per-row decorations (e.g. AltimSet's altimeter
	// underline).
	AfterDraw func(extent math.Extent2D, bodyOrigin [2]float32, b *ViewBuilders)
}

// RowList is the vertical stack. Defaults: BadgeGap=4, LabelGap=3 spaces in
// Font, SidePad=4.
type RowList struct {
	Font       *renderer.Font
	LineHeight float32
	TopPad     float32 // above each row's content
	BottomGap  float32 // below each row's content

	// List-level padding applied once at the top/bottom of the whole list
	// (in addition to per-row TopPad/BottomGap). Use these when the visual
	// "padding" only belongs above the first row and below the last row.
	ListTopPad    float32
	ListBottomPad float32

	// Width is the wrap width for body text and is also the assumed render
	// width — Draw uses the body extent for placement but expects its width
	// to match this value, since wrapping happens at Measure time.
	Width float32

	BadgeGap float32
	LabelGap float32
	SidePad  float32

	Rows []Row

	// Skip discards the first N rows from rendering. Use for scroll offset.
	Skip int
	// MaxLines caps the total wrapped body lines rendered (after Skip). The
	// last visible row is truncated mid-content if needed; subsequent rows
	// are dropped. 0 = unlimited.
	MaxLines int

	// SelectedID: when non-empty, the visible row whose ID matches gets a
	// filled background (SelectedBgColor) and uses SelectedTextColor for its
	// label/body text. The badge is left untouched.
	SelectedID        string
	SelectedBgColor   renderer.RGB
	SelectedTextColor renderer.RGB

	// Populated by Measure().
	measured       []measuredRow
	visibleFirst   int
	visibleLast    int // exclusive
	visibleHeight  float32
	totalBodyLines int
}

type measuredRow struct {
	lines  []string // wrapped body; empty if AltText / Centered / no Body
	height float32
}

// Measure pre-wraps each row's body to fit Width and returns the height of
// the visible portion (after Skip + MaxLines). Must be called before Draw().
func (l *RowList) Measure() float32 {
	l.applyDefaults()
	l.measured = make([]measuredRow, len(l.Rows))

	// Wrap every row regardless of Skip — TotalLines must reflect all rows.
	l.totalBodyLines = 0
	for i, r := range l.Rows {
		if r.SpacerHeight > 0 {
			l.measured[i].height = r.SpacerHeight
			continue
		}
		nLines := 1
		if !r.Centered && r.AltText == "" && r.Body != "" {
			firstW, contW := l.bodyAvailWidths(r)
			lines := wrapWords(l.Font, r.Body, firstW, contW)
			if len(lines) == 0 {
				lines = []string{r.Body}
			}
			l.measured[i].lines = lines
			nLines = len(lines)
		}
		l.measured[i].height = l.TopPad + float32(nLines)*l.LineHeight + l.BottomGap
		l.totalBodyLines += nLines
	}

	// Determine visible window.
	l.visibleFirst = min(l.Skip, len(l.Rows))
	l.visibleLast = len(l.Rows)
	l.visibleHeight = l.ListTopPad + l.ListBottomPad
	if l.MaxLines <= 0 {
		for i := l.visibleFirst; i < l.visibleLast; i++ {
			l.visibleHeight += l.measured[i].height
		}
		return l.visibleHeight
	}

	used := 0
	for i := l.visibleFirst; i < len(l.Rows); i++ {
		if l.Rows[i].SpacerHeight > 0 {
			l.visibleHeight += l.measured[i].height
			continue
		}
		n := len(l.measured[i].lines)
		if n == 0 {
			n = 1
		}
		if used+n > l.MaxLines {
			keep := l.MaxLines - used
			if keep <= 0 {
				l.visibleLast = i
				return l.visibleHeight
			}
			if len(l.measured[i].lines) > keep {
				l.measured[i].lines = l.measured[i].lines[:keep]
			}
			l.measured[i].height = l.TopPad + float32(keep)*l.LineHeight + l.BottomGap
			l.visibleHeight += l.measured[i].height
			l.visibleLast = i + 1
			return l.visibleHeight
		}
		used += n
		l.visibleHeight += l.measured[i].height
	}
	return l.visibleHeight
}

// VisibleFirst returns the index of the first rendered row (= Skip, clamped).
func (l *RowList) VisibleFirst() int { return l.visibleFirst }

// RowTextLeft returns the X where a row's label/body text starts, given the
// row's left edge bodyX0. Used by callers to size selection hit-extents that
// exclude the badge area.
func (l *RowList) RowTextLeft(bodyX0 float32, row Row) float32 {
	l.applyDefaults()
	x := bodyX0 + l.SidePad
	if row.Badge != nil {
		x += row.Badge.Width + l.BadgeGap
	}
	return x
}

// Extents computes the same per-rendered-row extents Draw would return,
// without drawing. Measure must have been called first. Use to populate a
// View's selectable-items list before Body runs.
func (l *RowList) Extents(body math.Extent2D) []math.Extent2D {
	if l.measured == nil {
		panic("RowList.Extents: Measure must be called first")
	}
	visibleCount := l.visibleLast - l.visibleFirst
	extents := make([]math.Extent2D, visibleCount)
	rowTop := body.P1[1] - l.ListTopPad
	for i := l.visibleFirst; i < l.visibleLast; i++ {
		rowBottom := rowTop - l.measured[i].height
		extents[i-l.visibleFirst] = math.Extent2D{
			P0: [2]float32{body.P0[0], rowBottom},
			P1: [2]float32{body.P1[0], rowTop},
		}
		rowTop = rowBottom
	}
	return extents
}

// TotalLines returns the total wrapped body lines across ALL rows (ignoring
// Skip and MaxLines). Used by the caller to populate the View's scroll-bar
// item count.
func (l *RowList) TotalLines() int { return l.totalBodyLines }

// Draw renders the visible rows (post-Skip, post-MaxLines) into body. Returns
// per-rendered-row extents (P0 = bottom-left, P1 = top-right) for click hit-testing.
// Measure must have been called first.
func (l *RowList) Draw(body math.Extent2D, b *ViewBuilders) []math.Extent2D {
	if l.measured == nil {
		panic("RowList.Draw: Measure must be called first")
	}
	l.applyDefaults()

	by := l.Font.LayoutBounds("0", 0).Height()
	visibleCount := l.visibleLast - l.visibleFirst
	extents := make([]math.Extent2D, visibleCount)
	rowTop := body.P1[1] - l.ListTopPad

	for i := l.visibleFirst; i < l.visibleLast; i++ {
		r := l.Rows[i]
		m := l.measured[i]
		rowBottom := rowTop - m.height
		rowExtent := math.Extent2D{
			P0: [2]float32{body.P0[0], rowBottom},
			P1: [2]float32{body.P1[0], rowTop},
		}
		extents[i-l.visibleFirst] = rowExtent

		if r.SpacerHeight > 0 {
			rowTop = rowBottom
			continue
		}

		contentTop := rowTop - l.TopPad
		style := renderer.TextStyle{Font: l.Font, Color: r.Color}

		if r.Centered {
			tw := l.Font.LayoutBounds(r.Label, 0).Width()
			x := body.P0[0] + (body.P1[0]-body.P0[0])/2 - tw/2
			y := contentTop - l.LineHeight + by
			b.Td.AddText(r.Label, [2]float32{x, y}, style)
			if r.AfterDraw != nil {
				r.AfterDraw(rowExtent, [2]float32{x, y}, b)
			}
			rowTop = rowBottom
			continue
		}

		x := body.P0[0] + l.SidePad
		baseY := contentTop - l.LineHeight + by
		if r.Badge != nil {
			// Align the badge's vertical center with the text's ink center
			// rather than the layout cell — at larger fonts the cell has
			// noticeably more space below the glyph than the badge does, so
			// using cell metrics makes the text look low against the badge.
			inkExt := l.Font.InkBounds("0", 0)
			textInkCenter := baseY + (inkExt.P0[1]+inkExt.P1[1])/2
			badgeBottom := textInkCenter - r.Badge.Height/2
			badgeTop := textInkCenter + r.Badge.Height/2
			bp0 := [2]float32{x, badgeBottom}
			bp1 := [2]float32{x + r.Badge.Width, badgeBottom}
			bp2 := [2]float32{x + r.Badge.Width, badgeTop}
			bp3 := [2]float32{x, badgeTop}
			b.Trid.AddQuad(bp0, bp1, bp2, bp3, r.Badge.Fill)
			b.Ld.AddLine(bp0, bp1, r.Badge.Border)
			b.Ld.AddLine(bp1, bp2, r.Badge.Border)
			b.Ld.AddLine(bp2, bp3, r.Badge.Border)
			b.Ld.AddLine(bp3, bp0, r.Badge.Border)
			x += r.Badge.Width + l.BadgeGap
		}

		if l.SelectedID != "" && r.ID == l.SelectedID {
			selP0 := [2]float32{x, rowExtent.P1[1]}
			selP1 := [2]float32{body.P1[0], rowExtent.P1[1]}
			selP2 := [2]float32{body.P1[0], rowExtent.P0[1]}
			selP3 := [2]float32{x, rowExtent.P0[1]}
			b.Trid.AddQuad(selP0, selP1, selP2, selP3, l.SelectedBgColor)
			style.Color = l.SelectedTextColor
		}

		labelX := x
		if r.Label != "" {
			b.Td.AddText(r.Label, [2]float32{labelX, baseY}, style)
		}
		lw := l.Font.LayoutBounds(r.Label, 0).Width()
		bodyX := labelX + lw
		if r.Label != "" {
			bodyX += l.LabelGap
		}

		switch {
		case r.AltText != "":
			b.Td.AddText(r.AltText, [2]float32{bodyX, baseY}, style)
		case len(m.lines) > 0:
			for j, line := range m.lines {
				lx := bodyX
				if j > 0 {
					lx = labelX
				}
				b.Td.AddText(line, [2]float32{lx, baseY - float32(j)*l.LineHeight}, style)
			}
		}

		if r.AfterDraw != nil {
			r.AfterDraw(rowExtent, [2]float32{bodyX, baseY}, b)
		}

		rowTop = rowBottom
	}
	return extents
}

func (l *RowList) applyDefaults() {
	if l.BadgeGap == 0 {
		l.BadgeGap = 4
	}
	if l.SidePad == 0 {
		l.SidePad = 4
	}
	if l.LabelGap == 0 {
		spaceW := l.Font.LayoutBounds(" ", 0).Width()
		l.LabelGap = 3 * spaceW
	}
}

func (l *RowList) bodyAvailWidths(r Row) (firstW, contW float32) {
	x := l.SidePad
	if r.Badge != nil {
		x += r.Badge.Width + l.BadgeGap
	}
	contStart := x
	if r.Label != "" {
		lw := l.Font.LayoutBounds(r.Label, 0).Width()
		x += lw + l.LabelGap
	}
	firstStart := x
	return l.Width - firstStart - l.SidePad, l.Width - contStart - l.SidePad
}

// wrapWords word-wraps text so the first line fits firstWidth and continuation
// lines fit contWidth. Splits on whitespace; runs of multiple spaces collapse
// to one space between words.
func wrapWords(font *renderer.Font, text string, firstWidth, contWidth float32) []string {
	var lines []string
	var cur string
	isFirst := true
	for word := range strings.FieldsSeq(text) {
		availW := firstWidth
		if !isFirst {
			availW = contWidth
		}
		test := cur
		if test != "" {
			test += " "
		}
		test += word
		w := font.LayoutBounds(test, 0).Width()
		if w > availW && cur != "" {
			lines = append(lines, cur)
			cur = word
			isFirst = false
		} else {
			cur = test
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
