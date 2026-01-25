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
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if ep.datablockType(ctx, trk) != FullDatablock {
			continue
		}
		db, ok := ep.FullDatablockOutlines(ctx, trk, transforms)
		if !ok {
			continue
		}
		if db.Fields[DBFieldMain].Inside(mouse.Pos) {
			ep.drawOutlineRectangle(ld, db.Fields[DBFieldMain], ERAMYellow)
		}
		if db.Fields[DBFieldCallsign].Inside(mouse.Pos) {
			// TODO: check what this does
		}
		if db.Fields[DBFieldVCI].Inside(mouse.Pos) {
			state.HoverVCI = true
			// check if clicked down on the VCI field to activate it
			if mouse.Clicked[platform.MouseButtonPrimary] {
				var input inputText
				input.Set(ep.currentPrefs(), "//")
				status := ep.executeERAMClickedCommand(ctx, input, &trk, transforms)
				ep.bigOutput.displaySuccess(ep.currentPrefs(), status.bigOutput)
			}
		} else {
			state.HoverVCI = false
		}
		if db.Fields[DBFieldAltitude].Inside(mouse.Pos) {
			// open altitude input
		}
		if db.Fields[DBFieldCID].Inside(mouse.Pos) {
			// turn menu
		}
		if db.Fields[DBFieldMain].Inside(mouse.Pos) {
			// remove
		}
		if db.Fields[DBFieldSpeed].Inside(mouse.Pos) {
			// speed menu
		}
		if db.Fields[DBFieldLine4].Inside(mouse.Pos) {
			// TODO: check if this does anything
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

var fullDatablockLineCols = map[int]int{
	0: 16,
	1: 16,
	2: 18, // vci (2) + line2 (16)
	3: 18, // col1 (2) + fieldD (8) + fieldE (8)
	4: 16,
}

var fullDatablockFieldSpecs = map[DatablockFieldID]DatablockFieldSpec{
	DBFieldCallsign:     {Line: 1, Col: 0, Cols: 16},
	DBFieldVCI:          {Line: 2, Col: 0, Cols: 2},
	DBFieldAltitude:     {Line: 2, Col: 2, Cols: 16},
	DBFieldCID:          {Line: 3, Col: 2, Cols: 8},
	DBFieldHandoffSpeed: {Line: 3, Col: 10, Cols: 8},
	DBFieldSpeed:        {Line: 3, Col: 10, Cols: 8},
	DBFieldLine4:        {Line: 4, Col: 0, Cols: 16},
}

// FullDatablockFieldSpec returns the built-in field spec, if defined.
func FullDatablockFieldSpec(id DatablockFieldID) (DatablockFieldSpec, bool) {
	spec, ok := fullDatablockFieldSpecs[id]
	return spec, ok
}

// FullDatablockFieldSpecs returns a copy of the built-in field specs map.
func FullDatablockFieldSpecs() map[DatablockFieldID]DatablockFieldSpec {
	specs := make(map[DatablockFieldID]DatablockFieldSpec, len(fullDatablockFieldSpecs))
	for id, spec := range fullDatablockFieldSpecs {
		specs[id] = spec
	}
	return specs
}

// FullDatablockOutlines returns outlines for the ERAM full datablock fields.
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

	layout := DatablockLayout{
		Anchor:      [2]float32{anchor[0], anchor[1] + dbOutlineYOffset},
		CharWidth:   dbCharWidth(font),
		LineHeight:  float32(font.Size),
		LineSpacing: dbLineSpacing,
	}

	outlines := DatablockOutlines{
		Layout: layout,
		Fields: make(map[DatablockFieldID]math.Extent2D, len(fullDatablockFieldSpecs)+1),
		Lines:  make(map[int]math.Extent2D, len(fullDatablockLineCols)),
	}

	lineLengths := ep.fullDatablockLineLengths(ctx, trk)
	for line, cols := range fullDatablockLineCols {
		if lineLengths != nil {
			if l, ok := lineLengths[line]; ok && l > 0 {
				outlines.Lines[line] = layout.LineExtent(line, l)
			}
			continue
		}
		outlines.Lines[line] = layout.LineExtent(line, cols)
	}

	for id, spec := range fullDatablockFieldSpecs {
		outlines.Fields[id] = layout.FieldExtent(spec)
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
	state := ep.TrackState[trk.ADSBCallsign]
	if state == nil {
		return [2]float32{}, false
	}
	start := transforms.WindowFromLatLongP(state.Track.Location)
	dir := ep.leaderLineDirection(ctx, trk)
	offset := datablockOffset(*dir)
	vector := ep.leaderLineVector(*dir)
	vector[0] += float32(offset[0]) * ctx.DrawPixelScale
	vector[1] += float32(offset[1]) * ctx.DrawPixelScale
	end := math.Add2f(start, math.Scale2f(vector, ctx.DrawPixelScale))
	return end, true
}

func (ep *ERAMPane) fullDatablockLineLengths(ctx *panes.Context, trk sim.Track) map[int]int {
	ps := ep.currentPrefs()
	color := ps.Brightness.FDB.ScaleRGB(ERAMYellow)
	db := ep.getDatablock(ctx, trk, FullDatablock, color)
	fdb, ok := db.(*fullDatablock)
	if !ok || fdb == nil {
		return nil
	}

	lines := fullDatablockLines(fdb)
	lengths := make(map[int]int, len(lines))
	for i, line := range lines {
		if l := line.Len(); l > 0 {
			lengths[i] = l
		}
	}
	return lengths
}

func fullDatablockLines(db *fullDatablock) []dbLine {
	return []dbLine{
		dbMakeLine(dbChopTrailing(db.line0[:])),
		dbMakeLine(dbChopTrailing(db.line1[:])),
		dbMakeLine(db.vci[:], dbChopTrailing(db.line2[:])),
		dbMakeLine(db.col1[:], dbChopTrailing(db.fieldD[:]), dbChopTrailing(db.fieldE[:])),
		dbMakeLine(dbChopTrailing(db.line4[:])),
	}
}

func fullDatablockMainLengths(lineLengths map[int]int) (int, int, int, int) {
	if lineLengths == nil {
		return fullDatablockLineCols[1], 16, 16, fullDatablockLineCols[4]
	}
	line1 := lineLengths[1]
	line2 := intMax(lineLengths[2]-2, 0)
	line3 := intMax(lineLengths[3]-2, 0)
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

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
