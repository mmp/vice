package eram

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
)

// DatablockFieldID identifies a datablock field for outline lookup.
type DatablockFieldID string

const (
	DBFieldMain         DatablockFieldID = "main"
	DBFieldCallsign     DatablockFieldID = "callsign"
	DBFieldVCI          DatablockFieldID = "vci"
	DBFieldAltitude     DatablockFieldID = "altitude"
	DBFieldCID          DatablockFieldID = "cid"
	DBFieldHandoffSpeed DatablockFieldID = "handoff_speed"
	DBFieldSpeed        DatablockFieldID = "speed"
	DBFieldLine4        DatablockFieldID = "line4"
	DBFieldLine4Heading DatablockFieldID = "line4_heading"
	DBFieldLine4Speed   DatablockFieldID = "line4_speed"
	DBFieldPointOut     DatablockFieldID = "pointout"
)

// DatablockFieldSpec describes a field location in line/column space (0-based).
type DatablockFieldSpec struct {
	Line int
	Col  int
	Cols int
}

// DatablockLayout captures the layout metrics for a rendered datablock.
type DatablockLayout struct {
	Anchor      [2]float32
	CharWidth   float32
	LineHeight  float32
	LineSpacing float32
}

func (ep *ERAMPane) datablockInteractions(ctx *panes.Context, tracks []sim.Track, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	// While a popup is open, clicks and hovers inside it belong to the
	// popup, not to any datablock beneath it.
	if ep.popup != nil && ep.popupExtent.Inside(mouse.Pos) {
		return
	}
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if ep.datablockType(ctx, trk) != FullDatablock {
			continue
		}
		db, ok := ep.FullDatablockOutlines(ctx, trk, transforms)
		if !ok {
			continue
		}
		// Outline the hovered main box (yellow) so its hit region is
		// visible for verification.
		if ext := db.Fields[DBFieldMain]; ext.Inside(mouse.Pos) {
			ep.drawOutlineRectangle(ld, ext, colors.yellow)
		}
		if db.Fields[DBFieldCallsign].Inside(mouse.Pos) {
			// TODO: check what this does
		}
		if db.Fields[DBFieldVCI].Inside(mouse.Pos) {
			state.HoverVCI = true
			if ep.mousePrimaryClicked(mouse) {
				status, err := handleToggleVCI(ep, &trk)
				ep.applyCommandStatus(ctx, status, err)
			}
		} else {
			state.HoverVCI = false
		}
		if db.Fields[DBFieldPointOut].Inside(mouse.Pos) {
			if ep.pointOutIndicatorActive(&trk) {
				if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
					ep.handlePointOutIndicatorClick(ctx, trk, db.Fields[DBFieldMain])
					mouse.Clicked = [platform.MouseButtonCount]bool{}
				}
			}
		}
		// Field clicks that open the datablock menus.
		if ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
			opened := true
			switch {
			case db.Fields[DBFieldAltitude].Inside(mouse.Pos):
				ep.openAltitudeMenu(ctx, &trk, db.Fields[DBFieldMain])
			case db.Fields[DBFieldCID].Inside(mouse.Pos),
				db.Fields[DBFieldLine4Heading].Inside(mouse.Pos):
				ep.openHeadingMenu(ctx, &trk, db.Fields[DBFieldMain])
			case db.Fields[DBFieldSpeed].Inside(mouse.Pos),
				db.Fields[DBFieldLine4Speed].Inside(mouse.Pos):
				ep.openSpeedMenu(ctx, &trk, db.Fields[DBFieldMain])
			case db.Fields[DBFieldLine4].Inside(mouse.Pos):
				ep.openFreeTextMenu(ctx, &trk, db.Fields[DBFieldMain])
			default:
				opened = false
			}
			if opened {
				mouse.Clicked = [platform.MouseButtonCount]bool{}
			}
		}
		if db.Fields[DBFieldMain].Inside(mouse.Pos) {
			// remove
		}
	}
	ld.GenerateCommands(cb)
}

// p0 -> top left, p2 -> bottom right
func (ep *ERAMPane) drawOutlineRectangle(ld *renderer.ColoredLinesDrawBuilder, extent math.Extent2D, color renderer.RGB) {
	p0 := extent.P0
	p2 := extent.P1
	p1 := math.Add2f(p0, [2]float32{extent.Width(), 0})
	p3 := math.Add2f(p2, [2]float32{-extent.Width(), 0})
	ld.AddLine(p0, p1, color)
	ld.AddLine(p1, p2, color)
	ld.AddLine(p2, p3, color)
	ld.AddLine(p3, p0, color)
}

// LineExtent returns the outline for a full line of the specified width.
func (l DatablockLayout) LineExtent(line, cols int) math.Extent2D {
	top := l.lineTop(line)
	x0 := l.Anchor[0] + l.lineShift(line)
	x1 := x0 + l.CharWidth*float32(cols)
	y0 := top - l.LineHeight
	return math.Extent2D{P0: [2]float32{x0, y0}, P1: [2]float32{x1, top}}
}

// FieldExtent returns the outline for the specified field.
func (l DatablockLayout) FieldExtent(spec DatablockFieldSpec) math.Extent2D {
	top := l.lineTop(spec.Line)
	x0 := l.Anchor[0] + l.lineShift(spec.Line) + l.CharWidth*float32(spec.Col)
	x1 := x0 + l.CharWidth*float32(spec.Cols)
	y0 := top - l.LineHeight
	return math.Extent2D{P0: [2]float32{x0, y0}, P1: [2]float32{x1, top}}
}

func (l DatablockLayout) lineTop(line int) float32 {
	return l.Anchor[1] + l.LineHeight - float32(line)*l.LineHeight*l.LineSpacing
}

func (l DatablockLayout) lineShift(line int) float32 {
	if line == 2 || line == 3 {
		return -l.CharWidth * dbLineOffsetScale
	}
	return 0
}

// DatablockOutlines provides field and line outlines for a datablock.
type DatablockOutlines struct {
	Layout DatablockLayout
	Fields map[DatablockFieldID]math.Extent2D
	Lines  map[int]math.Extent2D
}

const (
	dbLineSpacing     = 1.4
	dbLineOffsetScale = 2
	dbOutlinePadding  = 2
	dbOutlineYOffset  = -2
)

// dbFieldSpan returns the column span [start, start+n) of the visible
// (non-space) characters in the field, relative to the field's first
// character. ok is false if the field has no visible characters.
func dbFieldSpan(f []dbChar) (start, n int, ok bool) {
	first, last := -1, -1
	for i, ch := range f {
		if ch.ch != 0 && ch.ch != ' ' {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 {
		return 0, 0, false
	}
	return first, last - first + 1, true
}

// dbFieldRuns returns the {start, length} column span of each contiguous run
// of visible (non-space) characters in the field.
func dbFieldRuns(f []dbChar) [][2]int {
	var runs [][2]int
	start := -1
	for i, ch := range f {
		visible := ch.ch != 0 && ch.ch != ' '
		if visible && start == -1 {
			start = i
		} else if !visible && start != -1 {
			runs = append(runs, [2]int{start, i - start})
			start = -1
		}
	}
	if start != -1 {
		runs = append(runs, [2]int{start, len(f) - start})
	}
	return runs
}

// FullDatablockOutlines returns outlines for the ERAM full datablock fields.
// Field extents are derived from the datablock's actual contents so they
// track what is drawn rather than each field's maximum width.
func (ep *ERAMPane) FullDatablockOutlines(ctx *panes.Context, trk sim.Track,
	transforms radar.ScopeTransformations) (DatablockOutlines, bool) {
	if ep.datablockType(ctx, trk) != FullDatablock {
		return DatablockOutlines{}, false
	}
	anchor, ok := ep.fullDatablockAnchor(ctx, trk, transforms)
	if !ok {
		return DatablockOutlines{}, false
	}

	ps := ep.currentPrefs()
	font := ep.ERAMFont(ps.FDBSize)
	if font == nil {
		return DatablockOutlines{}, false
	}

	fdb := ep.buildFullDatablock(ctx, trk)
	if fdb == nil {
		return DatablockOutlines{}, false
	}

	layout := DatablockLayout{
		Anchor:      [2]float32{anchor[0], anchor[1] + dbOutlineYOffset},
		CharWidth:   dbCharWidth(font),
		LineHeight:  float32(font.Size),
		LineSpacing: dbLineSpacing,
	}

	outlines := DatablockOutlines{
		Layout: layout,
		Fields: make(map[DatablockFieldID]math.Extent2D, 9),
		Lines:  make(map[int]math.Extent2D, 5),
	}

	var lines [5]dbLine
	fullDatablockLines(fdb, &lines)
	lineLengths := make(map[int]int, len(lines))
	for i, line := range lines {
		if l := line.Len(); l > 0 {
			lineLengths[i] = l
			outlines.Lines[i] = layout.LineExtent(i, l)
		}
	}

	setField := func(id DatablockFieldID, line, col int, f []dbChar) {
		if start, n, ok := dbFieldSpan(f); ok {
			outlines.Fields[id] = layout.FieldExtent(DatablockFieldSpec{Line: line, Col: col + start, Cols: n})
		} else {
			outlines.Fields[id] = math.EmptyExtent2D()
		}
	}
	setField(DBFieldPointOut, 0, 0, fdb.line0[:])
	setField(DBFieldCallsign, 1, 0, fdb.line1[:])
	// The VCI cell is a fixed hover-to-reveal zone, so it needs an extent
	// even when nothing is currently drawn there.
	outlines.Fields[DBFieldVCI] = layout.FieldExtent(DatablockFieldSpec{Line: 2, Col: 0, Cols: 2})
	setField(DBFieldAltitude, 2, 2, fdb.line2[:])
	setField(DBFieldCID, 3, 2, fdb.fieldD[:])
	// Line 3 draws field E immediately after the trailing-chopped CID, not
	// at fieldD's maximum width.
	setField(DBFieldSpeed, 3, 2+len(dbChopTrailing(fdb.fieldD[:])), fdb.fieldE[:])
	outlines.Fields[DBFieldHandoffSpeed] = outlines.Fields[DBFieldSpeed]
	setField(DBFieldLine4, 4, 0, fdb.line4[:])
	if _, ok := lineLengths[4]; !ok {
		// Line 4 is empty: keep a click zone as wide as the datablock so
		// the free-form text menu can still be opened to create text.
		line1Len, line2Len, line3Len, _ := fullDatablockMainLengths(lineLengths)
		if n := max(line1Len, line2Len, line3Len); n > 0 {
			outlines.Fields[DBFieldLine4] = layout.LineExtent(4, n)
		}
	}

	// Line-4 heading and speed pick areas: when HSF data is displayed, two
	// visible runs are heading then speed; a single run is whichever of the
	// two is assigned. Free-form text opens its menu via DBFieldLine4.
	outlines.Fields[DBFieldLine4Heading] = math.EmptyExtent2D()
	outlines.Fields[DBFieldLine4Speed] = math.EmptyExtent2D()
	if fp := trk.FlightPlan; fp != nil && hsfDataExists(fp) && !isQSFreeTextScratchpad(fp.Scratchpad) {
		if state := ep.TrackState[trk.ADSBCallsign]; state != nil && !state.HSFHide {
			runExtent := func(r [2]int) math.Extent2D {
				return layout.FieldExtent(DatablockFieldSpec{Line: 4, Col: r[0], Cols: r[1]})
			}
			runs := dbFieldRuns(fdb.line4[:])
			if len(runs) == 2 {
				outlines.Fields[DBFieldLine4Heading] = runExtent(runs[0])
				outlines.Fields[DBFieldLine4Speed] = runExtent(runs[1])
			} else if len(runs) == 1 {
				if fp.Scratchpad != "" {
					outlines.Fields[DBFieldLine4Heading] = runExtent(runs[0])
				} else {
					outlines.Fields[DBFieldLine4Speed] = runExtent(runs[0])
				}
			}
		}
	}

	main := math.EmptyExtent2D()
	line1Len, line2Len, line3Len, line4Len := fullDatablockMainLengths(lineLengths)
	if line1Len > 0 {
		main = extentUnion(main, layout.FieldExtent(DatablockFieldSpec{Line: 1, Col: 0, Cols: line1Len}))
	}
	if line2Len > 0 {
		main = extentUnion(main, layout.FieldExtent(DatablockFieldSpec{Line: 2, Col: 2, Cols: line2Len}))
	}
	if line3Len > 0 {
		main = extentUnion(main, layout.FieldExtent(DatablockFieldSpec{Line: 3, Col: 2, Cols: line3Len}))
	}
	if line4Len > 0 {
		main = extentUnion(main, layout.FieldExtent(DatablockFieldSpec{Line: 4, Col: 0, Cols: line4Len}))
	}
	outlines.Fields[DBFieldMain] = padExtent(main, dbOutlinePadding)

	return outlines, true
}

func (ep *ERAMPane) fullDatablockAnchor(ctx *panes.Context, trk sim.Track,
	transforms radar.ScopeTransformations) ([2]float32, bool) {
	if ep.TrackState[trk.ADSBCallsign] == nil {
		return [2]float32{}, false
	}
	end, _ := ep.datablockAnchor(ctx, trk, FullDatablock, transforms)
	return end, true
}

// buildFullDatablock formats the track's full datablock so extents can be
// derived from its actual contents.
func (ep *ERAMPane) buildFullDatablock(ctx *panes.Context, trk sim.Track) *fullDatablock {
	ps := ep.currentPrefs()
	color := ps.Brightness.FDB.ScaleRGB(colors.yellow)
	db := ep.getDatablock(ctx, trk, FullDatablock, color)
	fdb, _ := db.(*fullDatablock)
	return fdb
}

func fullDatablockLines(db *fullDatablock, out *[5]dbLine) {
	out[0] = dbMakeLine(dbChopTrailing(db.line0[:]))
	out[1] = dbMakeLine(dbChopTrailing(db.line1[:]))
	out[2] = dbMakeLine(db.vci[:], dbChopTrailing(db.line2[:]))
	out[3] = dbMakeLine(db.col1[:], dbChopTrailing(db.fieldD[:]), dbChopTrailing(db.fieldE[:]))
	out[4] = dbMakeLine(dbChopTrailing(db.line4[:]))
}

func fullDatablockMainLengths(lineLengths map[int]int) (int, int, int, int) {
	line1 := lineLengths[1]
	line2 := max(lineLengths[2]-2, 0)
	line3 := max(lineLengths[3]-2, 0)
	line4 := lineLengths[4]
	return line1, line2, line3, line4
}

func dbCharWidth(font *renderer.Font) float32 {
	if font == nil {
		return 0
	}
	glyph := font.LookupGlyph(' ')
	if glyph == nil {
		return 0
	}
	return glyph.AdvanceX
}

func extentUnion(a, b math.Extent2D) math.Extent2D {
	a = math.Union(a, b.P0)
	a = math.Union(a, b.P1)
	return a
}

func padExtent(ext math.Extent2D, padding float32) math.Extent2D {
	if ext.P0[0] > ext.P1[0] || ext.P0[1] > ext.P1[1] {
		return ext
	}
	ext.P0[0] -= padding
	ext.P0[1] -= padding
	ext.P1[0] += padding
	ext.P1[1] += padding
	return ext
}
