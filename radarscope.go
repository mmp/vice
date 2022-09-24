// radarscope.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/mmp/imgui-go/v4"
)

type RadarScopePane struct {
	ScopeName       string
	Center          Point2LL
	Range           float32
	DataBlockFormat DataBlockFormat
	PointSize       float32
	LineWidth       float32

	Everything       bool
	Runways          bool
	Regions          bool
	Labels           bool
	LowAirways       bool
	HighAirways      bool
	VORs             bool
	VORNames         bool
	SelectedVORs     map[string]interface{}
	NDBs             bool
	NDBNames         bool
	SelectedNDBs     map[string]interface{}
	Fixes            bool
	FixNames         bool
	SelectedFixes    map[string]interface{}
	Airports         bool
	AirportNames     bool
	SelectedAirports map[string]interface{}

	GeoDrawSet       map[string]interface{}
	SIDDrawSet       map[string]interface{}
	STARDrawSet      map[string]interface{}
	ARTCCDrawSet     map[string]interface{}
	ARTCCLowDrawSet  map[string]interface{}
	ARTCCHighDrawSet map[string]interface{}

	RotationAngle float32

	AutomaticDatablockLayout bool

	MinAltitude int32
	MaxAltitude int32

	DrawVectorLine   bool
	VectorLineExtent float32
	VectorLineMode   int

	DrawRangeIndicators bool
	RangeIndicatorStyle int
	RangeLimits         [NumRangeTypes]RangeLimits
	rangeWarnings       map[AircraftPair]interface{}

	AutoMIT bool

	CRDAEnabled bool
	CRDAConfig  CRDAConfig

	DrawCompass bool

	DatablockFontIdentifier FontIdentifier
	datablockFont           *Font
	LabelFontIdentifier     FontIdentifier
	labelFont               *Font

	pointsDrawable        PointsDrawable
	linesDrawable         LinesDrawable
	highlightedDrawable   LinesDrawable
	llDrawList            DrawList // things using lat-long coordiantes for vertices
	textDrawList          DrawList // text, using window coordinates, but not datablocks (since they are not managed using textDrawablePool)
	datablockTextDrawList DrawList // datablock text

	acSelectedByDatablock *Aircraft

	primaryButtonDoubleClicked bool
	primaryDragStart           [2]float32
	primaryDragEnd             [2]float32

	// We cache these across frames to preserve their slice allocations for
	// reuse.
	textDrawablePool []TextDrawable

	lastRangeNotificationPlayed time.Time

	// all of them
	activeAircraft map[*Aircraft]interface{}
	// the ones that have an active track and are in our altitude range
	trackedAircraft map[*Aircraft]*TrackedAircraft
	// map from legit to their ghost, if present
	ghostAircraft           map[*Aircraft]*Aircraft
	datablockUpdateAircraft map[*Aircraft]interface{}
}

const (
	RangeIndicatorRings = iota
	RangeIndicatorLine
)

type TrackedAircraft struct {
	isGhost bool

	datablockAutomaticOffset [2]float32
	datablockManualOffset    [2]float32
	datablockText            [2]string
	datablockTextCurrent     bool
	datablockBounds          Extent2D // w.r.t. lower-left corner (so (0,0) p0 always)

	textDrawable [2]TextDrawable
}

// Takes aircraft position in window coordinates
func (t *TrackedAircraft) WindowDatablockBounds(p [2]float32) Extent2D {
	db := t.datablockBounds.Offset(p)
	if t.datablockManualOffset[0] != 0 || t.datablockManualOffset[1] != 0 {
		return db.Offset(t.datablockManualOffset)
	} else {
		return db.Offset(t.datablockAutomaticOffset)
	}
}

const (
	VectorLineNM = iota
	VectorLineMinutes
)

var (
	// vertices of a circle at the origin. Last point repeats the first.
	circlePoints [][2]float32
)

func init() {
	for d := float32(0); d < 360; d++ {
		angle := radians(d)
		pt := [2]float32{sin(angle), cos(angle)}
		circlePoints = append(circlePoints, pt)
	}
}

func NewRadarScopePane(n string) *RadarScopePane {
	c := &RadarScopePane{ScopeName: n}

	c.PointSize = 5
	c.LineWidth = 1

	// FIXME: initial center based on sector file, etc...
	c.Center = world.defaultCenter
	c.MinAltitude = 0
	c.MaxAltitude = 60000
	c.Range = 15
	c.DataBlockFormat = DataBlockFormatGround
	c.Regions = true
	c.Labels = true
	c.GeoDrawSet = make(map[string]interface{})
	c.SIDDrawSet = make(map[string]interface{})
	c.STARDrawSet = make(map[string]interface{})
	c.ARTCCDrawSet = make(map[string]interface{})
	c.ARTCCLowDrawSet = make(map[string]interface{})
	c.ARTCCHighDrawSet = make(map[string]interface{})
	c.trackedAircraft = make(map[*Aircraft]*TrackedAircraft)
	c.activeAircraft = make(map[*Aircraft]interface{})
	c.ghostAircraft = make(map[*Aircraft]*Aircraft)
	c.datablockUpdateAircraft = make(map[*Aircraft]interface{})

	font := GetDefaultFont()
	c.DatablockFontIdentifier = font.id
	c.datablockFont = font
	c.labelFont = font
	c.LabelFontIdentifier = font.id

	c.CRDAConfig = NewCRDAConfig()

	return c
}

func (rs *RadarScopePane) Duplicate(nameAsCopy bool) Pane {
	dupe := &RadarScopePane{}
	*dupe = *rs // get the easy stuff
	if nameAsCopy {
		dupe.ScopeName += " Copy"
	}

	dupemap := func(m map[string]interface{}) map[string]interface{} {
		dupe := make(map[string]interface{})
		for k := range m {
			dupe[k] = nil
		}
		return dupe
	}
	dupe.SelectedVORs = dupemap(rs.SelectedVORs)
	dupe.SelectedNDBs = dupemap(rs.SelectedNDBs)
	dupe.SelectedFixes = dupemap(rs.SelectedFixes)
	dupe.SelectedAirports = dupemap(rs.SelectedAirports)
	dupe.GeoDrawSet = dupemap(rs.GeoDrawSet)
	dupe.SIDDrawSet = dupemap(rs.SIDDrawSet)
	dupe.STARDrawSet = dupemap(rs.STARDrawSet)
	dupe.ARTCCDrawSet = dupemap(rs.ARTCCDrawSet)
	dupe.ARTCCLowDrawSet = dupemap(rs.ARTCCLowDrawSet)
	dupe.ARTCCHighDrawSet = dupemap(rs.ARTCCHighDrawSet)

	dupe.activeAircraft = make(map[*Aircraft]interface{})
	for ac := range rs.activeAircraft {
		dupe.activeAircraft[ac] = nil
	}

	dupe.trackedAircraft = make(map[*Aircraft]*TrackedAircraft)
	for ac, tracked := range rs.trackedAircraft {
		// NOTE: do not copy the TextDrawable over, since we'd be aliasing
		// the slices.
		dupe.trackedAircraft[ac] = &TrackedAircraft{
			isGhost:       tracked.isGhost,
			datablockText: tracked.datablockText}
	}

	dupe.ghostAircraft = make(map[*Aircraft]*Aircraft)
	for ac, gh := range rs.ghostAircraft {
		ghost := *gh // make a copy
		dupe.ghostAircraft[ac] = &ghost
	}
	dupe.datablockUpdateAircraft = make(map[*Aircraft]interface{})

	// don't share those slices...
	dupe.llDrawList = DrawList{}
	dupe.textDrawList = DrawList{}
	dupe.datablockTextDrawList = DrawList{}
	dupe.textDrawablePool = nil
	dupe.pointsDrawable = PointsDrawable{}
	dupe.linesDrawable = LinesDrawable{}
	dupe.highlightedDrawable = LinesDrawable{}

	return dupe
}

func (rs *RadarScopePane) getTextDrawable() TextDrawable {
	if len(rs.textDrawablePool) == 0 {
		return TextDrawable{}
	}

	end := len(rs.textDrawablePool) - 1
	td := rs.textDrawablePool[end]
	td.Reset()
	rs.textDrawablePool = rs.textDrawablePool[:end]
	return td
}

func (rs *RadarScopePane) Activate(cs *ColorScheme) {
	// Temporary: catch unset ones from old config files
	if rs.CRDAConfig.GlideslopeLateralSpread == 0 {
		rs.CRDAConfig = NewCRDAConfig()
	}
	if rs.GeoDrawSet == nil {
		rs.GeoDrawSet = make(map[string]interface{})
	}

	if rs.datablockFont = GetFont(rs.DatablockFontIdentifier); rs.datablockFont == nil {
		rs.datablockFont = GetDefaultFont()
		rs.DatablockFontIdentifier = rs.datablockFont.id
	}
	if rs.labelFont = GetFont(rs.LabelFontIdentifier); rs.labelFont == nil {
		rs.labelFont = GetDefaultFont()
		rs.LabelFontIdentifier = rs.labelFont.id
	}

	// start tracking all of the active aircraft
	if rs.activeAircraft == nil {
		rs.activeAircraft = make(map[*Aircraft]interface{})
	}
	if rs.trackedAircraft == nil {
		rs.trackedAircraft = make(map[*Aircraft]*TrackedAircraft)
	}
	if rs.ghostAircraft == nil {
		rs.ghostAircraft = make(map[*Aircraft]*Aircraft)
	}
	if rs.datablockUpdateAircraft == nil {
		rs.datablockUpdateAircraft = make(map[*Aircraft]interface{})
	}
	for _, ac := range world.aircraft {
		rs.activeAircraft[ac] = nil
		if !ac.LostTrack() && ac.Altitude() >= int(rs.MinAltitude) && ac.Altitude() <= int(rs.MaxAltitude) {
			rs.trackedAircraft[ac] = &TrackedAircraft{}

			if rs.CRDAEnabled {
				if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
					rs.ghostAircraft[ac] = ghost
					rs.trackedAircraft[ghost] = &TrackedAircraft{isGhost: true}
				}
			}
		}
	}

}

func (rs *RadarScopePane) Deactivate() {
	// Drop all of them
	rs.activeAircraft = nil
	rs.trackedAircraft = nil
	rs.ghostAircraft = nil
	rs.datablockUpdateAircraft = nil

	// Free up this memory, FWIW
	rs.llDrawList = DrawList{}
	rs.textDrawList = DrawList{}
	rs.datablockTextDrawList = DrawList{}
	rs.textDrawablePool = nil
	rs.pointsDrawable = PointsDrawable{}
	rs.linesDrawable = LinesDrawable{}
	rs.highlightedDrawable = LinesDrawable{}
}

func (rs *RadarScopePane) Name() string { return rs.ScopeName }

func (rs *RadarScopePane) DrawUI() {
	imgui.InputText("Name", &rs.ScopeName)
	imgui.InputIntV("Minimum altitude", &rs.MinAltitude, 100, 1000, 0 /* flags */)
	imgui.InputIntV("Maximum altitude", &rs.MaxAltitude, 100, 1000, 0 /* flags */)
	if imgui.CollapsingHeader("Aircraft rendering") {
		if rs.DataBlockFormat.DrawUI() {
			for _, t := range rs.trackedAircraft {
				t.datablockTextCurrent = false
			}
		}
		imgui.Checkbox("Vector lines", &rs.DrawVectorLine)
		if rs.DrawVectorLine {
			imgui.SliderFloatV("Vector line extent", &rs.VectorLineExtent, 0.1, 10, "%.1f", 0)
			imgui.SameLine()
			imgui.RadioButtonInt("nm", &rs.VectorLineMode, VectorLineNM)
			imgui.SameLine()
			imgui.RadioButtonInt("minutes", &rs.VectorLineMode, VectorLineMinutes)
		}
		imgui.Checkbox("Automatic datablock layout", &rs.AutomaticDatablockLayout)
	}
	if imgui.CollapsingHeader("Scope appearance") {
		imgui.SliderFloatV("Rotation angle", &rs.RotationAngle, -90., 90., "%.0f", 0)
		imgui.SliderFloatV("Point size", &rs.PointSize, 0.1, 20., "%.0f", 0)
		imgui.SliderFloatV("Line width", &rs.LineWidth, 0.1, 10, "%.1f", 0)
		if newFont, changed := DrawFontPicker(&rs.DatablockFontIdentifier, "Datablock font"); changed {
			rs.datablockFont = newFont
		}
		if newFont, changed := DrawFontPicker(&rs.LabelFontIdentifier, "Label font"); changed {
			rs.labelFont = newFont
		}
	}
	if imgui.CollapsingHeader("Tools") {
		imgui.Checkbox("Automatic MIT lines for arrivals", &rs.AutoMIT)
		imgui.Checkbox("Draw compass directions at edges", &rs.DrawCompass)
		imgui.Checkbox("Range indicators", &rs.DrawRangeIndicators)
		if rs.DrawRangeIndicators {
			imgui.Text("Indicator")
			imgui.SameLine()
			imgui.RadioButtonInt("Rings", &rs.RangeIndicatorStyle, RangeIndicatorRings)
			imgui.SameLine()
			imgui.RadioButtonInt("Lines", &rs.RangeIndicatorStyle, RangeIndicatorLine)

			if imgui.BeginTable("RangeLimits", 4) {
				for i := range rs.RangeLimits {
					rules := RangeLimitFlightRules(i).String()
					imgui.TableNextColumn()
					imgui.Text(rules)
					imgui.TableNextColumn()
					imgui.Text("Warning")
					imgui.TableNextColumn()
					imgui.SliderFloatV("Lateral (nm)##warn"+rules, &rs.RangeLimits[i].WarningLateral,
						0, 10, "%.1f", 0)
					imgui.TableNextColumn()
					imgui.InputIntV("Vertical (feet)##warn"+rules, &rs.RangeLimits[i].WarningVertical, 100, 100, 0)

					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.TableNextColumn()
					imgui.Text("Violation")
					imgui.TableNextColumn()
					imgui.SliderFloatV("Lateral (nm)##viol"+rules, &rs.RangeLimits[i].ViolationLateral,
						0, 10, "%.1f", 0)
					imgui.TableNextColumn()
					imgui.InputIntV("Vertical (feet)##viol"+rules, &rs.RangeLimits[i].ViolationVertical, 100, 100, 0)
				}
				imgui.EndTable()
			}
		}

		updateGhosts := func() {
			// First, remove all existing ghosts.
			for _, ghost := range rs.ghostAircraft {
				delete(rs.trackedAircraft, ghost)
			}
			rs.ghostAircraft = make(map[*Aircraft]*Aircraft)

			if rs.CRDAEnabled {
				// And make new ones if CRDA is enabled...
				for ac := range rs.trackedAircraft {
					if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
						rs.ghostAircraft[ac] = ghost
						rs.trackedAircraft[ghost] = &TrackedAircraft{isGhost: true}
						rs.datablockUpdateAircraft[ghost] = nil
					}
				}
			}
		}

		if imgui.Checkbox("Converging runway display aid (CRDA)", &rs.CRDAEnabled) {
			updateGhosts()
		}
		if rs.CRDAEnabled {
			if rs.CRDAConfig.DrawUI() {
				updateGhosts()
			}
		}
	}
	if imgui.CollapsingHeader("Scope contents") {
		if *devmode {
			imgui.Checkbox("Draw everything", &rs.Everything)
		}

		idx := 0
		checkbox := func(s string, b *bool) {
			if idx%4 == 0 {
				imgui.TableNextRow()
			}
			imgui.TableSetColumnIndex(idx % 4)
			idx++
			imgui.Checkbox(s, b)
		}
		if imgui.BeginTable("drawbuttons", 4) {
			checkbox("Regions", &rs.Regions)
			checkbox("Labels", &rs.Labels)
			checkbox("VORs", &rs.VORs)
			checkbox("VOR Names", &rs.VORNames)
			checkbox("NDBs", &rs.NDBs)
			checkbox("NDB Names", &rs.NDBNames)
			checkbox("Fixes", &rs.Fixes)
			checkbox("Fix Names", &rs.FixNames)
			checkbox("Airports", &rs.Airports)
			checkbox("Airport Names", &rs.AirportNames)
			checkbox("Low Airways", &rs.LowAirways)
			checkbox("High Airways", &rs.HighAirways)
			checkbox("Runways", &rs.Runways)
			imgui.EndTable()
		}

		if len(world.geos) > 0 && imgui.TreeNode("Geo") {
			for _, geo := range world.geos {
				_, draw := rs.GeoDrawSet[geo.name]
				imgui.Checkbox(geo.name, &draw)
				if draw {
					rs.GeoDrawSet[geo.name] = nil
				} else {
					delete(rs.GeoDrawSet, geo.name)
				}
			}
			imgui.TreePop()
		}

		sidStarHierarchy := func(title string, sidstar []SidStar, drawSet map[string]interface{}) {
			if imgui.TreeNode(title) {
				depth := 1
				active := true
				for _, ss := range sidstar {
					if strings.HasPrefix(ss.name, "===") {
						if active && depth > 1 {
							imgui.TreePop()
							depth--
						}
						n := strings.TrimLeft(ss.name, "= ")
						n = strings.TrimRight(n, "= ")
						active = imgui.TreeNode(n)
						if active {
							depth++
						}
					} else if active {
						_, draw := drawSet[ss.name]
						imgui.Checkbox(ss.name, &draw)
						if draw {
							drawSet[ss.name] = nil
						} else {
							delete(drawSet, ss.name)
						}
					}
				}
				for depth > 0 {
					imgui.TreePop()
					depth--
				}
			}
		}
		sidStarHierarchy("SIDs", world.SIDs, rs.SIDDrawSet)
		sidStarHierarchy("STARs", world.STARs, rs.STARDrawSet)

		artccCheckboxes := func(name string, artcc []ARTCC, drawSet map[string]interface{}) {
			if len(artcc) > 0 && imgui.TreeNode(name) {
				for i, a := range artcc {
					_, draw := drawSet[a.name]
					imgui.Checkbox(artcc[i].name, &draw)
					if draw {
						drawSet[a.name] = nil
					} else {
						delete(drawSet, a.name)
					}
				}
				imgui.TreePop()
			}
		}
		artccCheckboxes("ARTCC", world.ARTCC, rs.ARTCCDrawSet)
		artccCheckboxes("ARTCC Low", world.ARTCCLow, rs.ARTCCLowDrawSet)
		artccCheckboxes("ARTCC High", world.ARTCCHigh, rs.ARTCCHighDrawSet)
	}
}

func (rs *RadarScopePane) Update(updates *WorldUpdates) {
	if updates == nil {
		return
	}

	track := func(ac *Aircraft) bool {
		return !ac.LostTrack() && ac.Altitude() >= int(rs.MinAltitude) &&
			ac.Altitude() <= int(rs.MaxAltitude)
	}

	for ac := range updates.addedAircraft {
		if _, ok := rs.activeAircraft[ac]; ok {
			lg.Printf("%s: supposedly new but already active?", ac.Callsign())
		} else {
			rs.activeAircraft[ac] = nil
			if track(ac) {
				rs.trackedAircraft[ac] = &TrackedAircraft{}
				rs.datablockUpdateAircraft[ac] = nil
				if rs.CRDAEnabled {
					if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
						rs.ghostAircraft[ac] = ghost
						rs.trackedAircraft[ghost] = &TrackedAircraft{isGhost: true}
					}
				}
			}
		}
	}
	for ac := range updates.removedAircraft {
		if _, ok := rs.activeAircraft[ac]; !ok {
			lg.Printf("%s: deleted but not active?", ac.Callsign())
		} else {
			if ghost, ok := rs.ghostAircraft[ac]; ok {
				delete(rs.trackedAircraft, ghost)
			}
			delete(rs.activeAircraft, ac)
			delete(rs.trackedAircraft, ac)
			delete(rs.ghostAircraft, ac)
		}
	}

	for ac := range updates.modifiedAircraft {
		if _, ok := rs.activeAircraft[ac]; !ok {
			lg.Printf("%s: modified but not yet active?", ac.Callsign())
			rs.activeAircraft[ac] = nil
		}

		if rs.CRDAEnabled {
			// always start out by removing the old ghost
			if oldGhost, ok := rs.ghostAircraft[ac]; ok {
				delete(rs.trackedAircraft, oldGhost)
				delete(rs.ghostAircraft, ac)
			}
		}

		if track(ac) {
			rs.datablockUpdateAircraft[ac] = nil
			if ta, ok := rs.trackedAircraft[ac]; !ok {
				rs.trackedAircraft[ac] = &TrackedAircraft{}
			} else {
				ta.datablockTextCurrent = false
			}

			// new ghost
			if rs.CRDAEnabled {
				if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
					rs.ghostAircraft[ac] = ghost
					rs.trackedAircraft[ghost] = &TrackedAircraft{isGhost: true}
					rs.datablockUpdateAircraft[ghost] = nil
				}
			}
		} else {
			delete(rs.trackedAircraft, ac)
		}
	}

	/*
		for ac := range rs.activeAircraft {
			if !track(ac) {
				delete(rs.activeAircraft, ac)
			}
		}
	*/

	// For debugging: check the world's view of the active aircraft and
	// make sure both are consistent...
	for _, ac := range world.aircraft {
		if _, ok := rs.activeAircraft[ac]; !ok && track(ac) {
			lg.Printf("%s: in world but not active?", ac.Callsign())
			rs.activeAircraft[ac] = nil
		}
	}
	for ac := range rs.activeAircraft {
		if _, ok := world.aircraft[ac.Callsign()]; !ok {
			lg.Printf("%s: in active but not world?", ac.Callsign())
			delete(rs.activeAircraft, ac)
		}
	}
}

func mul4v(m *mgl32.Mat4, v [2]float32) [2]float32 {
	return [2]float32{m[0]*v[0] + m[4]*v[1], m[1]*v[0] + m[5]*v[1]}
}

func mul4p(m *mgl32.Mat4, p [2]float32) [2]float32 {
	return add2f(mul4v(m, p), [2]float32{m[12], m[13]})
}

func (rs *RadarScopePane) Draw(ctx *PaneContext) []*DrawList {
	latLongFromWindowMtx, ndcFromLatLongMtx := rs.getViewingMatrices(ctx)
	windowFromLatLongMtx := latLongFromWindowMtx.Inv()

	rs.prepareForDraw(ctx, ndcFromLatLongMtx)

	windowFromLatLongP := func(p Point2LL) [2]float32 {
		return mul4p(&windowFromLatLongMtx, p)
	}
	latLongFromWindowP := func(p [2]float32) Point2LL {
		return mul4p(&latLongFromWindowMtx, p)
	}
	latLongFromWindowV := func(p [2]float32) Point2LL {
		return mul4v(&latLongFromWindowMtx, p)
	}

	// Title in upper-left corner
	td := rs.getTextDrawable()
	height := ctx.paneExtent.Height()
	td.AddText(rs.ScopeName, [2]float32{float32(rs.labelFont.size) / 2, height - float32(rs.labelFont.size)/2},
		TextStyle{font: rs.labelFont, color: ctx.cs.Text})
	rs.textDrawList.AddText(td)

	// Static geometry: SIDs/STARs, runways, ...
	rs.drawStatic(ctx, windowFromLatLongMtx, latLongFromWindowMtx)

	// Per-aircraft stuff: tracks, datablocks, vector lines, range rings, ...
	rs.drawTracks(ctx, latLongFromWindowV, windowFromLatLongP)
	rs.updateDatablockTextAndBounds(ctx, windowFromLatLongP)
	rs.layoutDatablocks(ctx, windowFromLatLongP)
	rs.drawDatablocks(ctx, windowFromLatLongP, latLongFromWindowP)
	rs.drawVectorLines(ctx, windowFromLatLongP, latLongFromWindowP)
	rs.drawRangeIndicators(ctx, windowFromLatLongP)
	rs.drawMIT(ctx, windowFromLatLongP)
	rs.drawCompass(ctx, windowFromLatLongP, latLongFromWindowP)
	rs.drawMeasuringLine(ctx, latLongFromWindowP)
	rs.drawHighlighted(ctx, latLongFromWindowV)
	rs.drawCRDARegions(ctx)

	// Mouse events last, so that the datablock bounds are current.
	rs.consumeMouseEvents(ctx, latLongFromWindowP, latLongFromWindowV, windowFromLatLongP)

	rs.llDrawList.AddPoints(rs.pointsDrawable)
	rs.llDrawList.AddLines(rs.linesDrawable)
	rs.llDrawList.AddLines(rs.highlightedDrawable)

	return []*DrawList{&rs.llDrawList, &rs.textDrawList, &rs.datablockTextDrawList}
}

func (rs *RadarScopePane) getViewingMatrices(ctx *PaneContext) (latLongFromWindow mgl32.Mat4, ndcFromLatLong mgl32.Mat4) {
	// Translate to the center point
	ndcFromLatLong = mgl32.Translate3D(-rs.Center[0], -rs.Center[1], 0)

	// Scale based on range and nm per latitude / longitude
	sc := mgl32.Scale3D(world.NmPerLongitude/rs.Range, world.NmPerLatitude/rs.Range, 1)
	ndcFromLatLong = sc.Mul4(ndcFromLatLong)

	// Account for magnetic variation and any user-specified rotation
	rot := -radians(rs.RotationAngle + world.MagneticVariation)
	magRot := mgl32.HomogRotate3DZ(rot)
	ndcFromLatLong = magRot.Mul4(ndcFromLatLong)

	// Final orthographic projection including the effect of the
	// window's aspect ratio.
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	aspect := width / height
	ortho := mgl32.Ortho2D(-aspect, aspect, -1, 1)
	ndcFromLatLong = ortho.Mul4(ndcFromLatLong)

	// FIXME: it's silly to have NDC at all involved here; we can compute
	// latlong from window much more directly.
	latLongFromNDC := ndcFromLatLong.Inv()
	ndcFromWindow := mgl32.Scale3D(2/width, 2/height, 1)
	ndcFromWindow = mgl32.Translate3D(-1, -1, 0).Mul4(ndcFromWindow)
	latLongFromWindow = latLongFromNDC.Mul4(ndcFromWindow)

	return
}

func (rs *RadarScopePane) prepareForDraw(ctx *PaneContext, ndcFromLatLongMtx mgl32.Mat4) {
	// Take our text slices back from our DrawList now that it's been drawn, in preparation for reuse this frame.
	rs.textDrawablePool = append(rs.textDrawablePool, rs.textDrawList.text...)
	rs.textDrawList.text = nil

	// Reset the slices so we can draw new lines and points
	rs.linesDrawable.Reset()
	rs.linesDrawable.width = rs.LineWidth * ctx.highDPIScale
	rs.highlightedDrawable.Reset()
	rs.highlightedDrawable.width = 3 * rs.LineWidth * ctx.highDPIScale
	rs.pointsDrawable.Reset()
	rs.pointsDrawable.size = rs.PointSize

	rs.llDrawList.Reset()
	rs.llDrawList.clear = true // so must return this one first!
	rs.llDrawList.clearColor = ctx.cs.Background
	rs.llDrawList.projection = ndcFromLatLongMtx
	rs.llDrawList.modelview = mgl32.Ident4()

	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()

	rs.textDrawList.Reset()
	rs.textDrawList.clear = false
	rs.textDrawList.UseWindowCoordiantes(width, height)

	rs.datablockTextDrawList.Reset()
	rs.datablockTextDrawList.clear = false
	rs.datablockTextDrawList.UseWindowCoordiantes(width, height)
}

func (rs *RadarScopePane) drawStatic(ctx *PaneContext, windowFromLatLongMtx mgl32.Mat4,
	latLongFromWindowMtx mgl32.Mat4) {
	windowFromLatLongP := func(p Point2LL) [2]float32 {
		return mul4p(&windowFromLatLongMtx, p)
	}
	latLongFromWindowP := func(p [2]float32) Point2LL {
		return mul4p(&latLongFromWindowMtx, p)
	}
	latLongFromWindowV := func(p [2]float32) Point2LL {
		return mul4v(&latLongFromWindowMtx, p)
	}

	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	inWindow := func(p [2]float32) bool {
		return p[0] >= 0 && p[0] < width && p[1] >= 0 && p[1] < height
	}

	// Compute bounds for culling; need all four corners for viewBounds due to rotation...
	p0 := latLongFromWindowP([2]float32{0, 0})
	p1 := latLongFromWindowP([2]float32{width, 0})
	p2 := latLongFromWindowP([2]float32{0, height})
	p3 := latLongFromWindowP([2]float32{width, height})
	viewBounds := Extent2DFromPoints([][2]float32{p0, p1, p2, p3})

	// shrink bounds for debugging culling
	/*
		dx := .1 * (rs.viewBounds.p1[0] - rs.viewBounds.p0[0])
		dy := .1 * (rs.viewBounds.p1[1] - rs.viewBounds.p0[1])
		rs.viewBounds.p0[0] += dx
		rs.viewBounds.p1[0] -= dx
		rs.viewBounds.p0[1] += dy
		rs.viewBounds.p1[1] -= dy
	*/

	// Renormalize line width for high-DPI displays
	lineWidth := rs.LineWidth * ctx.highDPIScale

	if rs.Everything || rs.Runways {
		rs.llDrawList.AddLinesWithWidth(world.runwayGeom, lineWidth)
	}

	if rs.Everything || rs.Regions {
		for i := range world.regionGeom {
			if Overlaps(world.regionGeom[i].bounds, viewBounds) {
				rs.llDrawList.AddTriangles(world.regionGeom[i].tris)
			}
		}
	}

	// Find offsets in lat-long space for 2 pixel steps in x and y in
	// window coordinates.
	dx := latLongFromWindowV([2]float32{2, 0})
	dy := latLongFromWindowV([2]float32{0, 2})
	// Lat-long vector for (x,y) window coordinates vector
	vtx := func(x, y float32) [2]float32 {
		return add2f(scale2f(dx, x), scale2f(dy, y))
	}

	if rs.Everything || rs.VORs {
		square := [][2]float32{vtx(-1, -1), vtx(1, -1), vtx(1, 1), vtx(-1, 1)}
		for _, vor := range world.VORs {
			rs.linesDrawable.AddPolyline(vor, ctx.cs.VOR, square)
		}
	}
	if rs.Everything || rs.NDBs {
		fliptri := [][2]float32{vtx(-1.5, 1.5), vtx(1.5, 1.5), vtx(0, -0.5)}
		for _, ndb := range world.NDBs {
			// flipped triangles
			rs.linesDrawable.AddPolyline(ndb, ctx.cs.NDB, fliptri)
		}
	}

	if rs.Everything || rs.Fixes {
		uptri := [][2]float32{vtx(-1.5, -0.5), vtx(1.5, -0.5), vtx(0, 1.5)}
		for _, fix := range world.fixes {
			// upward-pointing triangles
			rs.linesDrawable.AddPolyline(fix, ctx.cs.Fix, uptri)
		}
	} else {
		uptri := [][2]float32{vtx(-1.5, -0.5), vtx(1.5, -0.5), vtx(0, 1.5)}
		for name := range rs.SelectedFixes {
			if loc, ok := world.fixes[name]; !ok {
				// May happen when a new sector file is loaded.
				//lg.Printf("%s: selected fix not found in sector file data!", loc)
			} else {
				rs.linesDrawable.AddPolyline(loc, ctx.cs.Fix, uptri)
			}
		}
	}
	if rs.Everything || rs.Airports {
		square := [][2]float32{vtx(-1, -1), vtx(1, -1), vtx(1, 1), vtx(-1, 1)}
		for _, ap := range world.airports {
			rs.linesDrawable.AddPolyline(ap, ctx.cs.Airport, square)
		}
	}

	drawARTCCLines := func(artcc []ARTCC, drawSet map[string]interface{}) {
		for _, artcc := range artcc {
			if _, draw := drawSet[artcc.name]; draw || rs.Everything {
				rs.llDrawList.AddLinesWithWidth(artcc.lines, lineWidth)
			}
		}
	}
	drawARTCCLines(world.ARTCC, rs.ARTCCDrawSet)
	drawARTCCLines(world.ARTCCLow, rs.ARTCCLowDrawSet)
	drawARTCCLines(world.ARTCCHigh, rs.ARTCCHighDrawSet)

	for _, sid := range world.SIDs {
		_, draw := rs.SIDDrawSet[sid.name]
		if (rs.Everything || draw) && Overlaps(sid.bounds, viewBounds) {
			rs.llDrawList.AddLinesWithWidth(sid.lines, lineWidth)
		}
	}
	for _, star := range world.STARs {
		_, draw := rs.STARDrawSet[star.name]
		if (rs.Everything || draw) && Overlaps(star.bounds, viewBounds) {
			rs.llDrawList.AddLinesWithWidth(star.lines, lineWidth)
		}
	}

	for _, geo := range world.geos {
		_, draw := rs.GeoDrawSet[geo.name]
		if rs.Everything || draw {
			rs.llDrawList.AddLinesWithWidth(geo.lines, lineWidth)
		}
	}

	drawAirwayLabels := func(labels []Label, color RGB) {
		for _, label := range labels {
			textPos := windowFromLatLongP(label.p)
			if inWindow(textPos) {
				style := TextStyle{
					font:            rs.labelFont,
					color:           color,
					drawBackground:  true,
					backgroundColor: ctx.cs.Background}
				td := rs.getTextDrawable()
				td.AddTextCentered(label.name, textPos, style)
				rs.textDrawList.AddText(td)
			}
		}
	}

	if rs.Everything || rs.LowAirways {
		rs.llDrawList.AddLinesWithWidth(world.lowAirwayGeom, lineWidth)
		drawAirwayLabels(world.lowAirwayLabels, ctx.cs.LowAirway)
	}
	if rs.Everything || rs.HighAirways {
		rs.llDrawList.AddLinesWithWidth(world.highAirwayGeom, lineWidth)
		drawAirwayLabels(world.highAirwayLabels, ctx.cs.HighAirway)
	}

	// Labels
	textAtLatLong := func(s string, p Point2LL, color RGB, offset [2]float32) {
		pw := add2f(windowFromLatLongP(p), offset)

		if inWindow(pw) {
			td := rs.getTextDrawable()
			td.AddText(s, [2]float32{pw[0], pw[1]}, TextStyle{font: rs.labelFont, color: color})
			rs.textDrawList.AddText(td)
		}
	}

	if rs.Everything || rs.Labels {
		for _, label := range world.labels {
			textAtLatLong(label.name, label.p, label.color, [2]float32{-4, 6})
		}
	}

	// VORs, NDBs, fixes, and airports
	fixtext := func(name string, p Point2LL, color RGB) {
		if viewBounds.Inside(p) {
			textAtLatLong(name, p, color, [2]float32{5, 9})
		}
	}

	drawloc := func(drawAll bool, selected map[string]interface{},
		items map[string]Point2LL, color RGB) {
		if drawAll {
			for name, item := range items {
				fixtext(name, item, color)
			}
		} else {
			for name := range selected {
				if loc, ok := items[name]; !ok {
					// May happen when a new sector file is loaded
					//lg.Printf("%s: not present in sector file", name)
				} else {
					fixtext(name, loc, color)
				}
			}
		}
	}

	drawloc(rs.Everything || rs.VORNames, rs.SelectedVORs, world.VORs, ctx.cs.VOR)
	drawloc(rs.Everything || rs.NDBNames, rs.SelectedNDBs, world.NDBs, ctx.cs.NDB)
	drawloc(rs.Everything || rs.FixNames, rs.SelectedFixes, world.fixes, ctx.cs.Fix)
	drawloc(rs.Everything || rs.AirportNames, rs.SelectedAirports, world.airports, ctx.cs.Airport)
}

func (rs *RadarScopePane) drawMIT(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32) {
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()

	annotatedLine := func(p0 Point2LL, p1 Point2LL, color RGB, text string) {
		// Center the text
		textPos := windowFromLatLongP(mid2ll(p0, p1))
		// Cull text based on center point
		if textPos[0] >= 0 && textPos[0] < width && textPos[1] >= 0 && textPos[1] < height {
			td := rs.getTextDrawable()
			style := TextStyle{font: rs.labelFont, color: color, drawBackground: true, backgroundColor: ctx.cs.Background}
			td.AddTextCentered(text, textPos, style)
			rs.textDrawList.AddText(td)
		}

		rs.linesDrawable.AddLine(p0, p1, color)
	}

	// Don't do AutoMIT if a sequence has been manually specified
	if rs.AutoMIT && len(positionConfig.mit) == 0 {
		inTrail := func(front Arrival, back Arrival) bool {
			dalt := back.aircraft.Altitude() - front.aircraft.Altitude()
			backHeading := back.aircraft.Heading()
			angle := headingp2ll(back.aircraft.Position(), front.aircraft.Position(),
				world.MagneticVariation)
			diff := headingDifference(backHeading, angle)

			return diff < 150 && dalt < 3000
		}

		arr := getDistanceSortedArrivals()

		for i := 1; i < len(arr); i++ {
			ac := arr[i].aircraft

			var closest Arrival
			minDist := float32(20)
			var estDist float32
			closestSet := false

			// O(n^2). #yolo
			for j := 0; j < len(arr); j++ {
				ac2 := arr[j].aircraft
				dist := nmdistance2ll(ac.Position(), ac2.Position())

				if i == j || ac2.flightPlan.arrive != ac.flightPlan.arrive {
					continue
				}

				if dist < minDist && inTrail(arr[i], arr[j]) {
					minDist = dist
					estDist = EstimatedFutureDistance(ac2, ac, 30)
					closestSet = true
					closest = arr[j]
				}
			}
			if closestSet {
				p0 := ac.Position()
				p1 := closest.aircraft.Position()

				// Having done all this work, we'll ignore the result if
				// we're drawing a range warning for this aircraft pair...
				if _, ok := rs.rangeWarnings[AircraftPair{ac, closest.aircraft}]; ok {
					continue
				}

				text := fmt.Sprintf("%.1f (%.1f) nm", minDist, estDist)
				if minDist > 5 {
					annotatedLine(p0, p1, ctx.cs.Safe, text)
				} else if minDist > 3 {
					annotatedLine(p0, p1, ctx.cs.Caution, text)
				} else {
					annotatedLine(p0, p1, ctx.cs.Error, text)
				}
			}
		}
	} else {
		for i := 1; i < len(positionConfig.mit); i++ {
			front, trailing := positionConfig.mit[i-1], positionConfig.mit[i]

			// As above, don't draw if there's a range warning for these two
			if _, ok := rs.rangeWarnings[AircraftPair{front, trailing}]; ok {
				continue
			}

			pfront, ptrailing := front.Position(), trailing.Position()
			dist := nmdistance2ll(pfront, ptrailing)
			estDist := EstimatedFutureDistance(positionConfig.mit[i-1], positionConfig.mit[i], 30)
			text := fmt.Sprintf("%.1f (%.1f) nm", dist, estDist)
			if dist > 5 {
				annotatedLine(pfront, ptrailing, ctx.cs.Safe, text)
			} else if dist > 3 {
				annotatedLine(pfront, ptrailing, ctx.cs.Caution, text)
			} else {
				annotatedLine(pfront, ptrailing, ctx.cs.Error, text)
			}
		}
	}
}

func (rs *RadarScopePane) drawTrack(ac *Aircraft, p Point2LL, color RGB,
	latLongFromWindowV func(p [2]float32) Point2LL, windowFromLatLongP func(p Point2LL) [2]float32) {
	px := float32(3) // TODO: make configurable?
	dx := latLongFromWindowV([2]float32{1, 0})
	dy := latLongFromWindowV([2]float32{0, 1})
	// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
	delta := func(p Point2LL, x, y float32) Point2LL {
		return add2ll(p, add2ll(scale2f(dx, x), scale2f(dy, y)))
	}

	// Draw tracks
	if ac.mode == Standby {
		rs.pointsDrawable.AddPoint(p, color)
	} else if ac.squawk == Squawk(1200) {
		pxb := px * .7    // a little smaller
		sc := float32(.8) // leave a little space at the corners
		rs.linesDrawable.AddLine(delta(p, -sc*pxb, -pxb), delta(p, sc*pxb, -pxb), color)
		rs.linesDrawable.AddLine(delta(p, pxb, -sc*pxb), delta(p, pxb, sc*pxb), color)
		rs.linesDrawable.AddLine(delta(p, sc*pxb, pxb), delta(p, -sc*pxb, pxb), color)
		rs.linesDrawable.AddLine(delta(p, -pxb, sc*pxb), delta(p, -pxb, -sc*pxb), color)
	} else {
		if ac.trackingController != "" {
			if controller, ok := world.controllers[ac.trackingController]; !ok {
				// This happens briefly during handoffs...
				//lg.Printf("%s: a/c is tracked by %s but world has never heard of that controller?!",
				//ac.Callsign(), ac.trackingController)
			} else if controller.position == nil {
				// This seems to correspond to being unprimed.
				//lg.Printf("%s: nil Position for tracking controller?", ac.trackingController)
			} else {
				ch := controller.position.scope

				// Offset to center the character on the track.
				bx, by := rs.datablockFont.BoundText(ch, 0)
				pw := windowFromLatLongP(p)
				pw = add2f(pw, [2]float32{-float32(bx / 2), float32(by / 2)})

				td := rs.getTextDrawable()
				td.AddText(ch, pw, TextStyle{font: rs.datablockFont, color: color})
				rs.textDrawList.AddText(td)
				return
			}
		}
		// diagonals
		diagPx := px * 0.707107 /* 1/sqrt(2) */
		rs.linesDrawable.AddLine(delta(p, -diagPx, -diagPx), delta(p, diagPx, diagPx), color)
		rs.linesDrawable.AddLine(delta(p, diagPx, -diagPx), delta(p, -diagPx, diagPx), color)
		// horizontal line
		rs.linesDrawable.AddLine(delta(p, -px, 0), delta(p, px, 0), color)
	}
}

func (rs *RadarScopePane) drawTracks(ctx *PaneContext, latLongFromWindowV func(p [2]float32) Point2LL,
	windowFromLatLongP func(p Point2LL) [2]float32) {
	for ac := range rs.trackedAircraft {
		pastColor := ctx.cs.Track

		// draw the history first so that if it's not moving, we still get something bright
		// don't draw the full history
		for i := 1; i < 5; i++ {
			// blend it a bit with the background color each time
			pastColor.R = .8*pastColor.R + .2*ctx.cs.Background.R
			pastColor.G = .8*pastColor.G + .2*ctx.cs.Background.G
			pastColor.B = .8*pastColor.B + .2*ctx.cs.Background.B
			rs.drawTrack(ac, ac.tracks[i].position, pastColor, latLongFromWindowV, windowFromLatLongP)
		}
		rs.drawTrack(ac, ac.Position(), ctx.cs.Track, latLongFromWindowV, windowFromLatLongP)
	}
}

func (rs *RadarScopePane) updateDatablockTextAndBounds(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32) {
	squawkCount := make(map[Squawk]int)
	for ac, ta := range rs.trackedAircraft {
		if !ta.isGhost {
			squawkCount[ac.squawk]++
		}
	}
	for ac, ta := range rs.trackedAircraft {
		if !ta.datablockTextCurrent {
			ta.datablockText[0] = rs.DataBlockFormat.Format(ac, squawkCount[ac.squawk] != 1, 0)
			ta.datablockText[1] = rs.DataBlockFormat.Format(ac, squawkCount[ac.squawk] != 1, 1)
			ta.datablockTextCurrent = true
		}
	}

	// For now regenerate all text drawables; a number of issues still need
	// to be handled (see todo.txt)
	for _, ta := range rs.trackedAircraft {
		bx0, by0 := rs.datablockFont.BoundText(ta.datablockText[0], -2)
		bx1, by1 := rs.datablockFont.BoundText(ta.datablockText[1], -2)
		bx, by := max(float32(bx0), float32(bx1)), max(float32(by0), float32(by1))
		ta.datablockBounds = Extent2D{p0: [2]float32{0, -by}, p1: [2]float32{bx, 0}}
	}
}

// Pick a point on the edge of datablock bounds to be the one we want as
// close as possible to the track point; either take a corner or halfway
// along an edge, according to the aircraft's heading.  Don't connect on
// the right hand side since the text tends to be ragged and there's slop
// in the bounds there.
func datablockConnectP(bbox Extent2D, heading float32) ([2]float32, bool) {
	center := bbox.Center()

	heading += 15 // simplify logic for figuring out slices below
	if heading < 0 {
		heading += 360
	}
	if heading > 360 {
		heading -= 360
	}

	if heading < 30 { // northbound (30 deg slice)
		return [2]float32{bbox.p0[0], center[1]}, false
	} else if heading < 90 { // NE (60 deg slice)
		return [2]float32{bbox.p0[0], bbox.p1[1]}, true
	} else if heading < 120 { // E (30 deg slice)
		return [2]float32{center[0], bbox.p1[1]}, false
	} else if heading < 180 { // SE (90 deg slice)
		return [2]float32{bbox.p0[0], bbox.p0[1]}, true
	} else if heading < 210 { // S (30 deg slice)
		return [2]float32{bbox.p0[0], center[1]}, false
	} else if heading < 270 { // SW (60 deg slice)
		return [2]float32{bbox.p0[0], bbox.p1[1]}, true
	} else if heading < 300 { // W (30 deg slice)
		return [2]float32{center[0], bbox.p0[1]}, false
	} else { // NW (60 deg slice)
		return [2]float32{bbox.p0[0], bbox.p0[1]}, true
	}
}

func (rs *RadarScopePane) layoutDatablocks(ctx *PaneContext, windowFromLatLongP func(Point2LL) [2]float32) {
	offsetSelfOnly := func(ac *Aircraft, info *TrackedAircraft) [2]float32 {
		bbox := info.datablockBounds.Expand(5)

		// We want the heading w.r.t. the window
		heading := ac.Heading() + rs.RotationAngle
		pConnect, isCorner := datablockConnectP(bbox, heading)

		// Translate the datablock to put the (padded) connection point
		// at (0,0)
		v := scale2f(pConnect, -1)

		if !isCorner {
			// it's an edge midpoint, so add a little more slop
			v = add2f(v, scale2f(normalize2f(v), 3))
		}

		return v
	}

	if !rs.AutomaticDatablockLayout {
		// layout just wrt our own track; ignore everyone else
		for ac, info := range rs.trackedAircraft {
			if info.datablockManualOffset[0] != 0 || info.datablockManualOffset[1] != 0 {
				info.datablockAutomaticOffset = [2]float32{0, 0}
				continue
			}

			info.datablockAutomaticOffset = offsetSelfOnly(ac, info)
		}
		return
	} else {
		// Sort them by callsign so our iteration order is consistent
		// TODO: maybe sort by the ac pointer to be more fair across airlines?
		var aircraft []*Aircraft
		width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
		for ac := range rs.trackedAircraft {
			pw := windowFromLatLongP(ac.Position())
			// Is it on screen (or getting there?)
			if pw[0] > -100 && pw[0] < width+100 && pw[1] > -100 && pw[1] < height+100 {
				aircraft = append(aircraft, ac)
			}
		}
		sort.Slice(aircraft, func(i, j int) bool {
			return aircraft[i].Callsign() < aircraft[j].Callsign()
		})

		// TODO: expand(5) consistency, ... ?
		// Bounds of placed data blocks in window coordinates.
		// FIXME: placedBounds is slightly a misnomer...
		datablockBounds := make([]Extent2D, len(aircraft))
		placed := make([]bool, len(aircraft))

		// First pass: anyone who has a manual offset goes where they go,
		// period.
		for i, ac := range aircraft {
			info := rs.trackedAircraft[ac]
			if info.datablockManualOffset[0] != 0 || info.datablockManualOffset[1] != 0 {
				pw := windowFromLatLongP(ac.Position())
				b := info.WindowDatablockBounds(pw).Expand(5)
				datablockBounds[i] = b
				placed[i] = true
			}
		}

		// Second pass: anyone who can be placed without interfering with
		// already-placed ones gets to be in their happy place.
		allowed := func(b Extent2D) bool {
			for i, db := range datablockBounds {
				if placed[i] && Overlaps(b, db) {
					return false
				}
			}
			return true
		}
		for i, ac := range aircraft {
			if placed[i] {
				continue
			}
			info := rs.trackedAircraft[ac]
			offset := offsetSelfOnly(ac, info)
			// TODO: we could do this incrementally a few pixels per frame
			// even if we could go all the way. Though then we would need
			// to consider all datablocks along the path...
			netOffset := sub2f(offset, info.datablockAutomaticOffset)

			pw := windowFromLatLongP(ac.Position())
			db := info.WindowDatablockBounds(pw).Expand(5).Offset(netOffset)
			if allowed(db) {
				placed[i] = true
				datablockBounds[i] = db
				info.datablockAutomaticOffset = offset
			}
		}

		// Third pass: all of the tricky ones...
		// FIXME: temporal stability?
		for i, ac := range aircraft {
			if placed[i] {
				continue
			}
			info := rs.trackedAircraft[ac]

			if info.datablockAutomaticOffset[0] == 0 && info.datablockAutomaticOffset[1] == 0 {
				// First time seen: start with the ideal. Otherwise
				// start with whatever we ended up with last time.
				info.datablockAutomaticOffset = offsetSelfOnly(ac, info)
			}
		}

		// Initialize current datablockBounds for all of the unplaced aircraft
		for i, ac := range aircraft {
			if placed[i] {
				continue
			}
			info := rs.trackedAircraft[ac]

			pw := windowFromLatLongP(ac.Position())
			datablockBounds[i] = info.WindowDatablockBounds(pw).Expand(5)
		}

		// For any datablocks that would be invalid with their current
		// automatic offset, apply forces until they are ok.
		iterScale := float32(2)
		for iter := 0; iter < 20; iter++ {
			//			iterScale /= 2
			anyOverlap := false

			// Compute and apply forces to each datablock. Treat this as a
			// point repulsion/attraction problem.  Work entirely in window
			// coordinates.  Fruchterman and Reingold 91, ish...
			for i, ac := range aircraft {
				if placed[i] {
					continue
				}

				db := datablockBounds[i]

				// Repulse current aircraft datablock from other
				// datablocks.
				var force [2]float32
				for j, pb := range datablockBounds {
					if i == j || !Overlaps(db, pb) {
						continue
					}

					anyOverlap = true
					v := sub2f(db.Center(), pb.Center())
					force = add2f(force, normalize2f(v))
				}

				// TODO ? clamp, etc?
				force = scale2f(force, iterScale)
				maxlen := float32(32) // .1 * (width + height) / 2
				if length2f(force) > maxlen {
					force = scale2f(force, maxlen/length2f(force))
				}

				info := rs.trackedAircraft[ac]
				info.datablockAutomaticOffset = add2f(info.datablockAutomaticOffset, force)
				datablockBounds[i] = db
			}

			//lg.Printf("iter %d overlap %s", iter, anyOverlap)

			if !anyOverlap {
				//lg.Printf("no overlapping after %d iters", iter)
				//				break
			}
		}

		// Double check that everyone is non-overlapping. (For loop above
		// should probably have more iterations...)
		for i, ba := range datablockBounds {
			for j, bb := range datablockBounds {
				if i != j && Overlaps(ba, bb) {
					//lg.Printf("OVERLAP! %d %d - %+v %+v", i, j, ba, bb)
				}
			}
		}

		// We know all are ok; now pull everyone in along their attraction line.
		//for iter := 0; iter < 10; iter++ {
		for {
			anyMoved := false
			for i, ac := range aircraft {
				if placed[i] {
					continue
				}

				db := datablockBounds[i]
				// And attract our own datablock to the aircraft position.
				info := rs.trackedAircraft[ac]
				goBack := sub2f(offsetSelfOnly(ac, info), info.datablockAutomaticOffset)
				if length2f(goBack) < 1 {
					continue
				}
				force := normalize2f(goBack)

				allowed := func(idx int, b Extent2D) bool {
					for i, db := range datablockBounds {
						if i != idx && Overlaps(b, db) {
							return false
						}
					}
					return true
				}

				dbMoved := db.Offset(force)
				if allowed(i, dbMoved) {
					anyMoved = true
					datablockBounds[i] = dbMoved
					info.datablockAutomaticOffset = add2f(info.datablockAutomaticOffset, force)
				}
			}
			if !anyMoved {
				break
			}
		}
	}
}

func (rs *RadarScopePane) datablockColor(ac *Aircraft, cs *ColorScheme) RGB {
	// This is not super efficient, but let's assume there aren't tons of ghost aircraft...
	for _, ghost := range rs.ghostAircraft {
		if ac == ghost {
			return cs.GhostDataBlock
		}
	}

	if positionConfig.selectedAircraft == ac {
		return cs.SelectedDataBlock
	}

	if ac.trackingController == world.user.callsign {
		if ac.hoController != "" {
			return cs.HandingOffDataBlock
		}
		return cs.TrackedDataBlock
	}

	return cs.UntrackedDataBlock
}

func (rs *RadarScopePane) drawDatablocks(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32,
	latLongFromWindowP func(p [2]float32) Point2LL) {
	for ac := range rs.datablockUpdateAircraft {
		pac := windowFromLatLongP(ac.Position())

		color := rs.datablockColor(ac, ctx.cs)
		ta := rs.trackedAircraft[ac]
		db := ta.WindowDatablockBounds(pac)

		for d := 0; d < 2; d++ {
			ta.textDrawable[d].Reset()
			// Draw characters starting at the upper left.
			ta.textDrawable[d].AddText(ta.datablockText[d], [2]float32{db.p0[0], db.p1[1]},
				TextStyle{font: rs.datablockFont, color: color, lineSpacing: -2})
		}

		// visualize bounds
		if false {
			var ld LinesDrawable
			bx, by := rs.datablockFont.BoundText(ta.datablockText[0], -2)
			ld.AddPolyline([2]float32{db.p0[0], db.p1[1]}, RGB{1, 0, 0},
				[][2]float32{[2]float32{float32(bx), 0},
					[2]float32{float32(bx), float32(-by)},
					[2]float32{float32(0), float32(-by)},
					[2]float32{float32(0), float32(0)}})
			rs.datablockTextDrawList.lines = append(rs.datablockTextDrawList.lines, ld)
		}
	}
	if len(rs.datablockUpdateAircraft) > 0 {
		rs.datablockUpdateAircraft = make(map[*Aircraft]interface{})
	}

	flashCycle := time.Now().Second() & 1
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	paneBounds := Extent2D{p0: [2]float32{0, 0}, p1: [2]float32{width, height}}

	// Sort the aircraft so that they are always drawn in the same order
	// (go's map iterator randomization otherwise randomizes the order,
	// which can cause shimmering when datablocks overlap (especially if
	// one is selected). We'll go with alphabetical by callsign, with the
	// selected aircraft, if any, always drawn last.
	aircraft := SortedMapKeysPred(rs.trackedAircraft, func(a **Aircraft, b **Aircraft) bool {
		asel := *a == positionConfig.selectedAircraft
		bsel := *b == positionConfig.selectedAircraft
		if asel == bsel {
			// This is effectively that neither is selected; alphabetical
			return (*a).Callsign() < (*b).Callsign()
		} else {
			// Otherwise one of the two is; we want the selected one at the
			// end.
			return bsel
		}
	})
	for _, ac := range aircraft {
		ta := rs.trackedAircraft[ac]

		drawLine := rs.DataBlockFormat != DataBlockFormatNone

		pac := windowFromLatLongP(ac.Position())
		bbox := ta.WindowDatablockBounds(pac)
		if Overlaps(paneBounds, bbox) {
			rs.datablockTextDrawList.AddText(ta.textDrawable[flashCycle])

			// quantized clamp
			qclamp := func(x, a, b float32) float32 {
				if x < a {
					return a
				} else if x > b {
					return b
				}
				return (a + b) / 2
			}
			// the datablock has been moved, so let's make clear what it's connected to
			if drawLine {
				var ex, ey float32
				wp := windowFromLatLongP(ac.Position())
				if wp[1] < bbox.p0[1] {
					ex = qclamp(wp[0], bbox.p0[0], bbox.p1[0])
					ey = bbox.p0[1]
				} else if wp[1] > bbox.p1[1] {
					ex = qclamp(wp[0], bbox.p0[0], bbox.p1[0])
					ey = bbox.p1[1]
				} else if wp[0] < bbox.p0[0] {
					ex = bbox.p0[0]
					ey = qclamp(wp[1], bbox.p0[1], bbox.p1[1])
				} else if wp[0] > bbox.p1[0] {
					ex = bbox.p1[0]
					ey = qclamp(wp[1], bbox.p0[1], bbox.p1[1])
				} else {
					// inside...
					drawLine = false
				}

				if drawLine {
					color := rs.datablockColor(ac, ctx.cs)
					pll := latLongFromWindowP([2]float32{ex, ey})
					rs.linesDrawable.AddLine(ac.Position(), [2]float32{pll[0], pll[1]}, color)
				}
			}
		}
	}
}

func (rs *RadarScopePane) drawVectorLines(ctx *PaneContext, windowFromLatLongP func(Point2LL) [2]float32,
	latLongFromWindowP func([2]float32) Point2LL) {
	if !rs.DrawVectorLine {
		return
	}

	for ac := range rs.trackedAircraft {
		// Don't draw junk for the first few tracks until we have a good
		// sense of the heading.
		if ac.HaveHeading() {
			start, end := ac.Position(), rs.vectorLineEnd(ac)
			sw, ew := windowFromLatLongP(start), windowFromLatLongP(end)
			v := sub2f(ew, sw)
			if length2f(v) > 12 {
				// advance the start by 6px to make room for the track blip
				sw = add2f(sw, scale2f(normalize2f(v), 6))
				// It's a little annoying to be xforming back to latlong at
				// this point...
				start = latLongFromWindowP(sw)
			}
			rs.linesDrawable.AddLine(start, end, ctx.cs.Track)
		}
	}
}

type Conflict struct {
	aircraft [2]*Aircraft
	limits   RangeLimits
}

func (rs *RadarScopePane) getConflicts() (warning []Conflict, violation []Conflict) {
	aircraft, tracked := FlattenMap(rs.trackedAircraft)

	for i, ac1 := range aircraft {
		if tracked[i].isGhost {
			continue
		}

		for j := i + 1; j < len(aircraft); j++ {
			if tracked[j].isGhost {
				continue
			}

			ac2 := aircraft[j]

			var r RangeLimits
			if ac1.flightPlan.rules == IFR {
				if ac2.flightPlan.rules == IFR {
					r = rs.RangeLimits[IFR_IFR]
				} else {
					r = rs.RangeLimits[IFR_VFR]
				}
			} else {
				if ac2.flightPlan.rules == IFR {
					r = rs.RangeLimits[IFR_VFR]
				} else {
					r = rs.RangeLimits[VFR_VFR]
				}
			}

			ldist := nmdistance2ll(ac1.Position(), ac2.Position())
			vdist := int32(abs(ac1.Altitude() - ac2.Altitude()))
			if ldist < r.ViolationLateral && vdist < r.ViolationVertical {
				violation = append(violation,
					Conflict{aircraft: [2]*Aircraft{ac1, ac2}, limits: r})
			} else if ldist < r.WarningLateral && vdist < r.WarningVertical {
				warning = append(warning,
					Conflict{aircraft: [2]*Aircraft{ac1, ac2}, limits: r})
			}
		}
	}

	return
}

func (rs *RadarScopePane) drawCompass(ctx *PaneContext, windowFromLatLongP func(Point2LL) [2]float32,
	latLongFromWindowP func([2]float32) Point2LL) {
	if !rs.DrawCompass {
		return
	}

	var pw [2]float32
	if positionConfig.selectedAircraft != nil {
		pw = windowFromLatLongP(positionConfig.selectedAircraft.Position())
	} else {
		pw = windowFromLatLongP(rs.Center)
	}

	bounds := Extent2D{
		p0: [2]float32{0, 0},
		p1: [2]float32{ctx.paneExtent.Width(), ctx.paneExtent.Height()}}

	for h := float32(10); h <= 360; h += 10 {
		hr := h + rs.RotationAngle
		dir := [2]float32{sin(radians(hr)), cos(radians(hr))}
		isect, _, t := bounds.IntersectRay(pw, dir)
		if !isect {
			// Happens on initial launch w/o a sector file...
			//lg.Printf("no isect?! p %+v dir %+v bounds %+v", pw, dir, ctx.paneExtent)
			continue
		}
		pEdge := add2f(pw, scale2f(dir, t))
		pInset := add2f(pw, scale2f(dir, t-10))
		pell := latLongFromWindowP(pEdge)
		pill := latLongFromWindowP(pInset)
		rs.linesDrawable.AddLine(pell, pill, ctx.cs.Compass)

		if int(h)%30 == 0 {
			label := []byte{'0', '0', '0'}
			hi := int(h)
			for i := 2; i >= 0 && hi != 0; i-- {
				label[i] = byte('0' + hi%10)
				hi /= 10
			}

			bx, by := rs.labelFont.BoundText(string(label), 0)

			// Initial inset
			pText := add2f(pw, scale2f(dir, t-14))

			// Finer text positioning depends on which edge of the window
			// pane we're on; this is made more grungy because text drawing
			// is specified w.r.t. the position of the upper-left corner...
			if fabs(pEdge[0]) < .125 {
				// left edge
				pText[1] += float32(by) / 2
			} else if fabs(pEdge[0]-bounds.p1[0]) < .125 {
				// right edge
				pText[0] -= float32(bx)
				pText[1] += float32(by) / 2
			} else if fabs(pEdge[1]) < .125 {
				// bottom edge
				pText[0] -= float32(bx) / 2
				pText[1] += float32(by)
			} else if fabs(pEdge[1]-bounds.p1[1]) < .125 {
				// top edge
				pText[0] -= float32(bx) / 2
			} else {
				lg.Printf("Edge borkage! pEdge %+v, bounds %+v", pEdge, bounds)
			}

			td := rs.getTextDrawable()
			td.AddText(string(label), pText, TextStyle{font: rs.labelFont, color: ctx.cs.Compass})
			rs.textDrawList.AddText(td)
		}
	}
}

func (rs *RadarScopePane) drawRangeIndicators(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32) {
	if !rs.DrawRangeIndicators {
		return
	}

	warnings, violations := rs.getConflicts()

	// Reset it each frame
	rs.rangeWarnings = make(map[AircraftPair]interface{})
	for _, w := range warnings {
		rs.rangeWarnings[AircraftPair{w.aircraft[0], w.aircraft[1]}] = nil
		rs.rangeWarnings[AircraftPair{w.aircraft[1], w.aircraft[0]}] = nil
	}
	for _, v := range violations {
		rs.rangeWarnings[AircraftPair{v.aircraft[0], v.aircraft[1]}] = nil
		rs.rangeWarnings[AircraftPair{v.aircraft[1], v.aircraft[0]}] = nil
	}

	// Audio alert
	if len(violations) > 0 && time.Since(rs.lastRangeNotificationPlayed) > 3*time.Second {
		globalConfig.AudioSettings.HandleEvent(AudioEventConflictAlert)
		rs.lastRangeNotificationPlayed = time.Now()
	}

	switch rs.RangeIndicatorStyle {
	case RangeIndicatorRings:
		drawCircle := func(ap Point2LL, radius float32, color RGB) {
			latScale := radius / world.NmPerLatitude
			longScale := radius / world.NmPerLongitude
			for i := 0; i < len(circlePoints)-1; i++ {
				p0 := Point2LL{ap[0] + longScale*circlePoints[i][1], ap[1] + latScale*circlePoints[i][0]}
				p1 := Point2LL{ap[0] + longScale*circlePoints[i+1][1], ap[1] + latScale*circlePoints[i+1][0]}
				rs.linesDrawable.AddLine(p0, p1, color)
			}
		}

		for _, w := range warnings {
			drawCircle(w.aircraft[0].Position(), w.limits.WarningLateral, ctx.cs.Caution)
			drawCircle(w.aircraft[1].Position(), w.limits.WarningLateral, ctx.cs.Caution)
		}
		for _, v := range violations {
			drawCircle(v.aircraft[0].Position(), v.limits.ViolationLateral, ctx.cs.Error)
			drawCircle(v.aircraft[1].Position(), v.limits.ViolationLateral, ctx.cs.Error)
		}

	case RangeIndicatorLine:
		annotatedLine := func(p0 Point2LL, p1 Point2LL, color RGB, text string) {
			textPos := windowFromLatLongP(mid2ll(p0, p1))
			td := rs.getTextDrawable()
			style := TextStyle{
				font:            rs.labelFont,
				color:           color,
				drawBackground:  true,
				backgroundColor: ctx.cs.Background}
			td.AddTextCentered(text, textPos, style)
			rs.textDrawList.AddText(td)
			rs.linesDrawable.AddLine(p0, p1, color)
		}

		rangeText := func(ac0, ac1 *Aircraft) string {
			dist := nmdistance2ll(ac0.Position(), ac1.Position())
			dalt := (abs(ac0.Altitude()-ac1.Altitude()) + 50) / 100
			return fmt.Sprintf("%.1f %d", dist, dalt)
		}

		for _, w := range warnings {
			ac0, ac1 := w.aircraft[0], w.aircraft[1]
			annotatedLine(ac0.Position(), ac1.Position(), ctx.cs.Caution, rangeText(ac0, ac1))
		}
		for _, v := range violations {
			ac0, ac1 := v.aircraft[0], v.aircraft[1]
			annotatedLine(ac0.Position(), ac1.Position(), ctx.cs.Error, rangeText(ac0, ac1))
		}
	}
}

func (rs *RadarScopePane) drawMeasuringLine(ctx *PaneContext, latLongFromWindowP func([2]float32) Point2LL) {
	if !rs.primaryButtonDoubleClicked {
		return
	}

	p0 := latLongFromWindowP(rs.primaryDragStart)
	p1 := latLongFromWindowP(rs.primaryDragEnd)

	// TODO: separate color for this rather than piggybacking?
	rs.linesDrawable.AddLine(p0, p1, ctx.cs.SelectedDataBlock)

	// distance between the two points in nm
	dist := nmdistance2ll(p0, p1)

	// heading and reciprocal
	hdg := int(headingp2ll(p0, p1, world.MagneticVariation) + 0.5)
	if hdg == 0 {
		hdg = 360
	}
	rhdg := hdg + 180
	if rhdg > 360 {
		rhdg -= 360
	}
	label := fmt.Sprintf(" %.1f nm \n%d / %d", dist, hdg, rhdg)
	td := rs.getTextDrawable()
	style := TextStyle{
		font:            rs.labelFont,
		color:           ctx.cs.SelectedDataBlock,
		drawBackground:  true,
		backgroundColor: ctx.cs.Background}
	textPos := mid2f(rs.primaryDragStart, rs.primaryDragEnd)
	td.AddTextCentered(label, textPos, style)
	rs.textDrawList.AddText(td)

}

func (rs *RadarScopePane) drawHighlighted(ctx *PaneContext, latLongFromWindowV func([2]float32) Point2LL) {
	remaining := time.Until(positionConfig.highlightedLocationEndTime)
	if remaining < 0 {
		return
	}

	p := positionConfig.highlightedLocation
	color := ctx.cs.Error
	fade := 1.5
	if sec := remaining.Seconds(); sec < fade {
		x := float32(sec / fade)
		color.R = x*color.R + (1-x)*ctx.cs.Background.R
		color.G = x*color.G + (1-x)*ctx.cs.Background.G
		color.B = x*color.B + (1-x)*ctx.cs.Background.B
	}

	radius := float32(10)
	dx := latLongFromWindowV([2]float32{radius, 0})
	dy := latLongFromWindowV([2]float32{0, radius})
	for i := 0; i < len(circlePoints)-1; i++ {
		p0 := add2ll(p, add2ll(scale2ll(dx, circlePoints[i][0]), scale2ll(dy, circlePoints[i][1])))
		p1 := add2ll(p, add2ll(scale2ll(dx, circlePoints[i+1][0]), scale2ll(dy, circlePoints[i+1][1])))
		rs.highlightedDrawable.AddLine(p0, p1, color)
	}
}

func (rs *RadarScopePane) consumeMouseEvents(ctx *PaneContext, latLongFromWindowP func([2]float32) Point2LL,
	latLongFromWindowV func([2]float32) Point2LL, windowFromLatLongP func(Point2LL) [2]float32) {
	if ctx.mouse == nil {
		return
	}

	scopeMoved := false

	// Handle dragging the scope center
	if ctx.mouse.dragging[mouseButtonPrimary] && rs.primaryButtonDoubleClicked {
		rs.primaryDragEnd = add2f(rs.primaryDragEnd, ctx.mouse.dragDelta)
	} else if rs.primaryButtonDoubleClicked {
		rs.primaryButtonDoubleClicked = false
	}
	if ctx.mouse.dragging[mouseButtonSecondary] {
		delta := ctx.mouse.dragDelta
		if delta[0] != 0 || delta[1] != 0 {
			deltaLL := latLongFromWindowV(delta)
			rs.Center = sub2f(rs.Center, deltaLL)
			scopeMoved = true
		}
	}

	// Consume mouse wheel
	if ctx.mouse.wheel[1] != 0 {
		scale := pow(1.05, ctx.mouse.wheel[1])
		scopeMoved = true

		// We want to zoom in centered at the mouse position; this affects
		// the scope center after the zoom, so we'll find the
		// transformation that gives the new center position.
		mouseLL := latLongFromWindowP(ctx.mouse.pos)
		centerTransform := mgl32.Translate3D(-mouseLL[0], -mouseLL[1], 0)
		centerTransform = mgl32.Scale3D(scale, scale, 1).Mul4(centerTransform)
		centerTransform = mgl32.Translate3D(mouseLL[0], mouseLL[1], 0).Mul4(centerTransform)
		rs.Center = mul4p(&centerTransform, rs.Center)

		rs.Range *= scale
	}

	if scopeMoved {
		// All datablocks need to be redrawn (unfortunately...)
		for ac := range rs.trackedAircraft {
			rs.datablockUpdateAircraft[ac] = nil
		}
	}

	if rs.acSelectedByDatablock != nil {
		if ctx.mouse.dragging[mouseButtonPrimary] {
			ac := rs.acSelectedByDatablock
			rs.trackedAircraft[ac].datablockManualOffset =
				add2f(rs.trackedAircraft[ac].datablockAutomaticOffset,
					add2f(rs.trackedAircraft[ac].datablockManualOffset, ctx.mouse.dragDelta))
			rs.trackedAircraft[ac].datablockAutomaticOffset = [2]float32{0, 0}
		} else {
			rs.acSelectedByDatablock = nil
		}
	}

	// Update selected aircraft
	if ctx.mouse.doubleClicked[mouseButtonPrimary] {
		rs.primaryButtonDoubleClicked = true
		rs.primaryDragStart = ctx.mouse.pos
		rs.primaryDragEnd = rs.primaryDragStart
	}
	if ctx.mouse.clicked[mouseButtonPrimary] {
		var clickedAircraft *Aircraft
		clickedDistance := float32(20) // in pixels; don't consider anything farther away

		for ac := range rs.trackedAircraft {
			pw := windowFromLatLongP(ac.Position())
			dist := distance2f(pw, ctx.mouse.pos)

			if dist < clickedDistance {
				clickedAircraft = ac
				clickedDistance = dist
			}
		}

		// And now check and see if we clicked on a datablock (TODO: check for held)
		for ac, ta := range rs.trackedAircraft {
			pw := windowFromLatLongP(ac.Position())
			db := ta.WindowDatablockBounds(pw)
			if db.Inside(ctx.mouse.pos) {
				rs.acSelectedByDatablock = ac
				clickedAircraft = ac
				break
			}
		}

		positionConfig.NotifyAircraftSelected(clickedAircraft)
	}
}

///////////////////////////////////////////////////////////////////////////
// CRDA

type CRDAConfig struct {
	Airport                  string
	PrimaryRunway            string
	SecondaryRunway          string
	Mode                     int
	TieStaggerDistance       float32
	ShowGhostsOnPrimary      bool
	HeadingTolerance         float32
	GlideslopeLateralSpread  float32
	GlideslopeVerticalSpread float32
	GlideslopeAngle          float32
	ShowCRDARegions          bool
}

const (
	CRDAModeStagger = iota
	CRDAModeTie
)

func NewCRDAConfig() CRDAConfig {
	return CRDAConfig{
		Mode:                     CRDAModeStagger,
		TieStaggerDistance:       3,
		HeadingTolerance:         110,
		GlideslopeLateralSpread:  10,
		GlideslopeVerticalSpread: 10,
		GlideslopeAngle:          3}

}

func (c *CRDAConfig) getRunway(n string) *Runway {
	for _, rwy := range world.runways[c.Airport] {
		if rwy.number == n {
			return &rwy
		}
	}
	return nil
}

func (c *CRDAConfig) getRunways() (ghostSource *Runway, ghostDestination *Runway) {
	for i, rwy := range world.runways[c.Airport] {
		if rwy.number == c.PrimaryRunway {
			ghostSource = &world.runways[c.Airport][i]
		}
		if rwy.number == c.SecondaryRunway {
			ghostDestination = &world.runways[c.Airport][i]
		}
	}

	if c.ShowGhostsOnPrimary {
		ghostSource, ghostDestination = ghostDestination, ghostSource
	}

	return
}

func runwayIntersection(a *Runway, b *Runway) (Point2LL, bool) {
	p1, p2 := ll2nm(a.threshold), ll2nm(a.end)
	p3, p4 := ll2nm(b.threshold), ll2nm(b.end)
	p, ok := LineLineIntersect(p1, p2, p3, p4)

	centroid := mid2f(mid2f(p1, p2), mid2f(p3, p4))
	d := distance2f(centroid, p)
	if d > 30 {
		// more like parallel; we don't care about super far away intersections...
		ok = false
	}

	return nm2ll(p), ok
}

func (c *CRDAConfig) GetGhost(ac *Aircraft) *Aircraft {
	src, dst := c.getRunways()
	if src == nil || dst == nil {
		return nil
	}

	pIntersect, ok := runwayIntersection(src, dst)
	if !ok {
		lg.Printf("No intersection between runways??!?")
		return nil
	}

	airport, ok := world.FAA.airports[c.Airport]
	if !ok {
		lg.Printf("%s: airport unknown?!", c.Airport)
		return nil
	}

	if ac.GroundSpeed() > 350 {
		return nil
	}

	if headingDifference(ac.Heading(), src.heading) > c.HeadingTolerance {
		return nil
	}

	// Is it on the glideslope?
	// Laterally: compute the heading to the threshold and compare to the
	// glideslope's lateral spread.
	h := headingp2ll(ac.Position(), src.threshold, world.MagneticVariation)
	if fabs(h-src.heading) > c.GlideslopeLateralSpread {
		return nil
	}

	// Vertically: figure out the range of altitudes at the distance out.
	// First figure out the aircraft's height AGL.
	agl := ac.Altitude() - airport.elevation

	// Find the glideslope height at the aircraft's distance to the
	// threshold.
	// tan(glideslope angle) = height / threshold distance
	const nmToFeet = 6076.12
	thresholdDistance := nmToFeet * nmdistance2ll(ac.Position(), src.threshold)
	height := thresholdDistance * tan(radians(c.GlideslopeAngle))
	// Assume 100 feet at the threshold
	height += 100

	// Similarly, find the allowed altitude difference
	delta := thresholdDistance * tan(radians(c.GlideslopeVerticalSpread))

	if fabs(float32(agl)-height) > delta {
		return nil
	}

	// This aircraft gets a ghost.

	// This is a little wasteful, but we're going to copy the entire
	// Aircraft structure just to be sure we carry along everything we
	// might want to have available when drawing the track and
	// datablock for the ghost.
	ghost := *ac

	// Now we just need to update the track positions to be those for
	// the ghost. We'll again do this in nm space before going to
	// lat-long in the end.
	pi := ll2nm(pIntersect)
	for i, t := range ghost.tracks {
		// Vector from the intersection point to the track location
		v := sub2f(ll2nm(t.position), pi)

		// For tie mode, offset further by the specified distance.
		if c.Mode == CRDAModeTie {
			length := length2f(v)
			v = scale2f(v, (length+c.TieStaggerDistance)/length)
		}

		// Rotate it angle degrees clockwise
		angle := dst.heading - src.heading
		s, c := sin(radians(angle)), cos(radians(angle))
		vr := [2]float32{c*v[0] + s*v[1], -s*v[0] + c*v[1]}
		// Point along the other runway
		pr := add2f(pi, vr)

		// TODO: offset it as appropriate
		ghost.tracks[i].position = nm2ll(pr)
	}
	return &ghost
}

func (c *CRDAConfig) DrawUI() bool {
	updateGhosts := false

	flags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCharsNoBlank
	imgui.InputTextV("Airport", &c.Airport, flags, nil)
	if runways, ok := world.runways[c.Airport]; !ok {
		if c.Airport != "" {
			color := positionConfig.GetColorScheme().TextError
			imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
			imgui.Text("Airport unknown!")
			imgui.PopStyleColor()
		}
	} else {
		sort.Slice(runways, func(i, j int) bool { return runways[i].number < runways[j].number })

		primary, secondary := c.getRunway(c.PrimaryRunway), c.getRunway(c.SecondaryRunway)
		if imgui.BeginCombo("Primary runway", c.PrimaryRunway) {
			if imgui.SelectableV("(None)", c.PrimaryRunway == "", 0, imgui.Vec2{}) {
				updateGhosts = true
				c.PrimaryRunway = ""
			}
			for _, rwy := range runways {
				if secondary != nil {
					// Don't include the selected secondary runway
					if rwy.number == secondary.number {
						continue
					}
					// Only list intersecting runways
					if _, ok := runwayIntersection(&rwy, secondary); !ok {
						continue
					}
				}
				if imgui.SelectableV(rwy.number, rwy.number == c.PrimaryRunway, 0, imgui.Vec2{}) {
					updateGhosts = true
					c.PrimaryRunway = rwy.number
				}
			}
			imgui.EndCombo()
		}
		if imgui.BeginCombo("Secondary runway", c.SecondaryRunway) {
			// Note: this is the exact same logic for primary runways
			// above, just with the roles switched...
			if imgui.SelectableV("(None)", c.SecondaryRunway == "", 0, imgui.Vec2{}) {
				updateGhosts = true
				c.SecondaryRunway = ""
			}
			for _, rwy := range runways {
				if primary != nil {
					// Don't include the selected primary runway
					if rwy.number == primary.number {
						continue
					}
					// Only list intersecting runways
					if _, ok := runwayIntersection(&rwy, primary); !ok {
						continue
					}
				}
				if imgui.SelectableV(rwy.number, rwy.number == c.SecondaryRunway, 0, imgui.Vec2{}) {
					updateGhosts = true
					c.SecondaryRunway = rwy.number
				}
			}
			imgui.EndCombo()
		}
		if imgui.Checkbox("Ghosts on primary", &c.ShowGhostsOnPrimary) {
			updateGhosts = true
		}
		imgui.Text("Mode")
		imgui.SameLine()
		updateGhosts = imgui.RadioButtonInt("Stagger", &c.Mode, 0) || updateGhosts
		imgui.SameLine()
		updateGhosts = imgui.RadioButtonInt("Tie", &c.Mode, 1) || updateGhosts
		if c.Mode == CRDAModeTie {
			imgui.SameLine()
			updateGhosts = imgui.SliderFloatV("Tie stagger distance", &c.TieStaggerDistance, 0.1, 10, "%.1f", 0) ||
				updateGhosts
		}
		updateGhosts = imgui.SliderFloatV("Heading tolerance (deg)", &c.HeadingTolerance, 5, 180, "%.0f", 0) || updateGhosts
		updateGhosts = imgui.SliderFloatV("Glideslope angle (deg)", &c.GlideslopeAngle, 2, 5, "%.1f", 0) || updateGhosts
		updateGhosts = imgui.SliderFloatV("Glideslope lateral spread (deg)", &c.GlideslopeLateralSpread, 1, 20, "%.0f", 0) || updateGhosts
		updateGhosts = imgui.SliderFloatV("Glideslope vertical spread (deg)", &c.GlideslopeVerticalSpread, 1, 10, "%.1f", 0) || updateGhosts
		updateGhosts = imgui.Checkbox("Show CRDA regions", &c.ShowCRDARegions) || updateGhosts
	}

	return updateGhosts
}

func (rs *RadarScopePane) drawCRDARegions(ctx *PaneContext) {
	if !rs.CRDAConfig.ShowCRDARegions {
		return
	}

	// Find the intersection of the two runways.  Work in nm space, not lat-long
	if true {
		src, dst := rs.CRDAConfig.getRunways()
		if src != nil && dst != nil {
			p, ok := runwayIntersection(src, dst)
			if !ok {
				lg.Printf("no intersection between runways?!")
			}
			//		rs.linesDrawable.AddLine(src.threshold, src.end, RGB{0, 1, 0})
			//		rs.linesDrawable.AddLine(dst.threshold, dst.end, RGB{0, 1, 0})
			rs.pointsDrawable.AddPoint(p, RGB{1, 0, 0})
		}
	}

	src, _ := rs.CRDAConfig.getRunways()
	if src == nil {
		return
	}

	// we have the runway heading, but we want to go the opposite direction
	// and then +/- HeadingTolerance.
	rota := src.heading + 180 - rs.CRDAConfig.GlideslopeLateralSpread - world.MagneticVariation
	rotb := src.heading + 180 + rs.CRDAConfig.GlideslopeLateralSpread - world.MagneticVariation

	// Lay out the vectors in nm space, not lat-long
	sa, ca := sin(radians(rota)), cos(radians(rota))
	va := [2]float32{sa, ca}
	dist := float32(25)
	va = scale2f(va, dist)

	sb, cb := sin(radians(rotb)), cos(radians(rotb))
	vb := scale2f([2]float32{sb, cb}, dist)

	// Over to lat-long to draw the lines
	vall, vbll := nm2ll(va), nm2ll(vb)
	rs.linesDrawable.AddLine(src.threshold, add2ll(src.threshold, vall), ctx.cs.Caution)
	rs.linesDrawable.AddLine(src.threshold, add2ll(src.threshold, vbll), ctx.cs.Caution)
}

///////////////////////////////////////////////////////////////////////////
// DataBlockFormat

// Loosely patterened after https://vrc.rosscarlson.dev/docs/single_page.html#the_various_radar_modes
const (
	DataBlockFormatNone = iota
	DataBlockFormatSimple
	DataBlockFormatGround
	DataBlockFormatTower
	DataBlockFormatFull
	DataBlockFormatCount
)

type DataBlockFormat int

func (d DataBlockFormat) String() string {
	return [...]string{"None", "Simple", "Ground", "Tower", "Full"}[d]
}

func (d *DataBlockFormat) DrawUI() bool {
	changed := false
	if imgui.BeginCombo("Data block format", d.String()) {
		var i DataBlockFormat
		for ; i < DataBlockFormatCount; i++ {
			if imgui.SelectableV(DataBlockFormat(i).String(), i == *d, 0, imgui.Vec2{}) {
				*d = i
				changed = true
			}
		}
		imgui.EndCombo()
	}
	return changed
}

func (d DataBlockFormat) Format(ac *Aircraft, duplicateSquawk bool, flashcycle int) string {
	if d == DataBlockFormatNone {
		return ""
	}

	alt100s := (ac.Altitude() + 50) / 100
	speed := ac.tracks[0].groundspeed
	fp := ac.flightPlan
	actype := fp.TypeWithoutSuffix()
	if actype != "" {
		// So we can unconditionally print it..
		actype += " "
	}

	var datablock strings.Builder
	datablock.Grow(64)

	// All of the modes always start with the callsign and the voicce indicator
	datablock.WriteString(ac.Callsign())
	// Otherwise a 3 line datablock
	// Line 1: callsign and voice indicator
	if ac.voiceCapability == Receive {
		datablock.WriteString("/r")
	} else if ac.voiceCapability == Text {
		datablock.WriteString("/t")
	}

	switch d {
	case DataBlockFormatSimple:
		return datablock.String()

	case DataBlockFormatGround:
		datablock.WriteString("\n")
		// Line 2: a/c type and groundspeed
		datablock.WriteString(actype)

		// normally it's groundspeed next, unless there's a squawk
		// situation that we need to flag...
		if duplicateSquawk && ac.mode != Standby && ac.squawk != Squawk(1200) && ac.squawk != 0 && flashcycle&1 == 0 {
			datablock.WriteString("CODE")
		} else if !duplicateSquawk && ac.mode != Standby && ac.squawk != ac.assignedSquawk && flashcycle&1 == 0 {
			datablock.WriteString(ac.squawk.String())
		} else {
			datablock.WriteString(fmt.Sprintf("%02d", speed))
			if fp.rules == VFR {
				datablock.WriteString("V")
			}
		}
		return datablock.String()

	case DataBlockFormatTower:
		// Line 2: first flash is [alt speed/10]. If we don't have
		// destination and a/c type then just always show this rather than
		// flashing a blank line.
		datablock.WriteString("\n")
		if flashcycle&1 == 0 || (fp.arrive == "" && actype == "") {
			datablock.WriteString(fmt.Sprintf("%03d %02d", alt100s, (speed+5)/10))
			if fp.rules == VFR {
				datablock.WriteString("V")
			}
		} else {
			// Second flash normally alternates between scratchpad (or dest) and
			// filed altitude for the first thing, then has *[actype]
			if flashcycle&2 == 0 {
				if ac.scratchpad != "" {
					datablock.WriteString(ac.scratchpad)
				} else {
					datablock.WriteString(fp.arrive)
				}
			} else {
				// Second field is the altitude
				datablock.WriteString(fmt.Sprintf("%03d", fp.altitude/100))
			}

			datablock.WriteString("*")
			// Flag squawk issues
			if duplicateSquawk && ac.mode != Standby && ac.squawk != 0 && flashcycle&1 == 0 {
				datablock.WriteString("CODE")
			} else if !duplicateSquawk && ac.mode != Standby && ac.squawk != ac.assignedSquawk && flashcycle&1 == 0 {
				datablock.WriteString(ac.squawk.String())
			} else {
				datablock.WriteString(actype)
			}
		}
		return datablock.String()

	case DataBlockFormatFull:
		if ac.mode == Standby {
			return datablock.String()
		}

		ascending := (ac.tracks[0].altitude - ac.tracks[1].altitude) > 50
		descending := (ac.tracks[0].altitude - ac.tracks[1].altitude) < -50
		altAnnotation := " "
		if ac.tempAltitude != 0 && abs(ac.tracks[0].altitude-ac.tempAltitude) < 300 {
			altAnnotation = "T "
		} else if ac.flightPlan.altitude != 0 &&
			abs(ac.tracks[0].altitude-ac.flightPlan.altitude) < 300 {
			altAnnotation = "C "
		} else if ascending {
			altAnnotation = FontAwesomeIconArrowUp + " "
		} else if descending {
			altAnnotation = FontAwesomeIconArrowDown + " "
		}

		if ac.squawk == Squawk(1200) {
			// VFR
			datablock.WriteString(fmt.Sprintf(" %03d", alt100s))
			datablock.WriteString(altAnnotation)
			return datablock.String()
		}
		datablock.WriteString("\n")

		// Line 2: altitude, then scratchpad or temp/assigned altitude.
		datablock.WriteString(fmt.Sprintf("%03d", alt100s))
		datablock.WriteString(altAnnotation)
		// TODO: Here add level if at wrong alt...

		// Have already established it's not squawking standby.
		if duplicateSquawk && ac.squawk != 0 {
			if flashcycle&1 == 0 {
				datablock.WriteString("CODE")
			} else {
				datablock.WriteString(ac.squawk.String())
			}
		} else if ac.squawk != ac.assignedSquawk {
			// show what they are actually squawking
			datablock.WriteString(ac.squawk.String())
		} else {
			if flashcycle&1 == 0 {
				if ac.scratchpad != "" {
					datablock.WriteString(ac.scratchpad)
				} else if ac.tempAltitude != 0 {
					datablock.WriteString(fmt.Sprintf("%03dT", ac.tempAltitude/100))
				} else {
					datablock.WriteString(fmt.Sprintf("%03d", fp.altitude/100))
				}
			} else {
				if fp.arrive != "" {
					datablock.WriteString(fp.arrive)
				} else {
					datablock.WriteString("????")
				}
			}
		}
		datablock.WriteString("\n")

		// Line 3: a/c type and groundspeed
		datablock.WriteString(actype)
		datablock.WriteString(fmt.Sprintf("%03d", (speed+5)/10*10))
		if fp.rules == VFR {
			datablock.WriteString("V")
		}

		if ac.mode == Ident && flashcycle&1 == 0 {
			datablock.WriteString("ID")
		}

		return datablock.String()

	default:
		lg.Printf("%d: unhandled datablock format", d)
		return "ERROR"
	}
}

func (rs *RadarScopePane) vectorLineEnd(ac *Aircraft) Point2LL {
	switch rs.VectorLineMode {
	case VectorLineNM:
		// we want the vector length to be l=rs.VectorLineExtent.
		// we have a heading vector (hx, hy) and scale factors (sx, sy) due to lat/long compression.
		// we want a t to scale the heading by to have that length.
		// solve (sx t hx)^2 + (hy t hy)^2 = l^2 ->
		// t = sqrt(l^2 / ((sx hx)^2 + (sy hy)^2)
		h := ac.HeadingVector()
		t := sqrt(sqr(rs.VectorLineExtent) / (sqr(h[1]*world.NmPerLatitude) + sqr(h[0]*world.NmPerLongitude)))
		return add2ll(ac.Position(), scale2ll(h, t))

	case VectorLineMinutes:
		// In theory we get messages every 5s. So 12x the heading vector
		// should be one minute.  Probably better would be to use the
		// groundspeed...
		vectorEnd := ac.HeadingVector()
		return add2ll(ac.Position(), scale2ll(vectorEnd, 12*rs.VectorLineExtent))

	default:
		lg.Printf("unexpected vector line mode: %d", rs.VectorLineMode)
		return Point2LL{}
	}
}
