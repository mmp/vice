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

	DrawEverything   bool
	DrawRunways      bool
	DrawRegions      bool
	DrawLabels       bool
	DrawLowAirways   bool
	DrawHighAirways  bool
	DrawVORs         bool
	DrawVORNames     bool
	VORsToDraw       map[string]interface{}
	DrawNDBs         bool
	DrawNDBNames     bool
	NDBsToDraw       map[string]interface{}
	DrawFixes        bool
	DrawFixNames     bool
	FixesToDraw      map[string]interface{}
	DrawAirports     bool
	DrawAirportNames bool
	AirportsToDraw   map[string]interface{}

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
	RadarTracksDrawn int32

	DrawRangeIndicators bool
	RangeIndicatorStyle int
	RangeLimits         [NumRangeTypes]RangeLimits
	rangeWarnings       map[AircraftPair]interface{}

	AutoMIT         bool
	AutoMITAirports map[string]interface{}

	CRDAEnabled bool
	CRDAConfig  CRDAConfig

	DrawCompass bool

	DatablockFontIdentifier FontIdentifier
	datablockFont           *Font
	LabelFontIdentifier     FontIdentifier
	labelFont               *Font

	pointsDrawBuilder     PointsDrawBuilder
	linesDrawBuilder      ColoredLinesDrawBuilder
	thickLinesDrawBuilder ColoredLinesDrawBuilder
	llCommandBuffer       CommandBuffer // things using lat-long coordiantes for vertices
	textCommandBuffer     CommandBuffer // text, using window coordinates

	acSelectedByDatablock *Aircraft

	primaryButtonDoubleClicked bool
	primaryDragStart           [2]float32
	primaryDragEnd             [2]float32

	tdScratch TextDrawBuilder

	lastRangeNotificationPlayed time.Time

	// All of the aircraft in the world, each with additional information
	// carried along in an AircraftScopeState.
	aircraft map[*Aircraft]*AircraftScopeState
	// map from legit to their ghost, if present
	ghostAircraft map[*Aircraft]*Aircraft

	pointedOutAircraft *TransientMap[*Aircraft, string]

	// persistent state used in the ui
	vorsComboState, ndbsComboState      *ComboBoxState
	fixesComboState, airportsComboState *ComboBoxState
}

const (
	RangeIndicatorRings = iota
	RangeIndicatorLine
)

type AircraftScopeState struct {
	isGhost bool

	datablockAutomaticOffset [2]float32
	datablockManualOffset    [2]float32
	datablockText            [2]string
	datablockTextCurrent     bool
	datablockBounds          Extent2D // w.r.t. lower-left corner (so (0,0) p0 always)
}

// Takes aircraft position in window coordinates
func (t *AircraftScopeState) WindowDatablockBounds(p [2]float32) Extent2D {
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

func NewRadarScopePane(n string) *RadarScopePane {
	c := &RadarScopePane{ScopeName: n}

	c.PointSize = 5
	c.LineWidth = 1

	c.Center = database.defaultCenter
	c.MinAltitude = 0
	c.MaxAltitude = 60000
	c.Range = 15
	c.DataBlockFormat = DataBlockFormatGround
	c.DrawRegions = true
	c.DrawLabels = true

	c.VORsToDraw = make(map[string]interface{})
	c.NDBsToDraw = make(map[string]interface{})
	c.FixesToDraw = make(map[string]interface{})
	c.AirportsToDraw = make(map[string]interface{})

	c.GeoDrawSet = make(map[string]interface{})
	c.SIDDrawSet = make(map[string]interface{})
	c.STARDrawSet = make(map[string]interface{})
	c.ARTCCDrawSet = make(map[string]interface{})
	c.ARTCCLowDrawSet = make(map[string]interface{})
	c.ARTCCHighDrawSet = make(map[string]interface{})
	c.aircraft = make(map[*Aircraft]*AircraftScopeState)
	c.ghostAircraft = make(map[*Aircraft]*Aircraft)
	c.pointedOutAircraft = NewTransientMap[*Aircraft, string]()

	font := GetDefaultFont()
	c.DatablockFontIdentifier = font.id
	c.datablockFont = font
	c.labelFont = font
	c.LabelFontIdentifier = font.id

	c.CRDAConfig = NewCRDAConfig()

	c.AutoMITAirports = make(map[string]interface{})

	c.vorsComboState = NewComboBoxState(1)
	c.ndbsComboState = NewComboBoxState(1)
	c.fixesComboState = NewComboBoxState(1)
	c.airportsComboState = NewComboBoxState(1)

	return c
}

func (rs *RadarScopePane) Duplicate(nameAsCopy bool) Pane {
	dupe := &RadarScopePane{}
	*dupe = *rs // get the easy stuff
	if nameAsCopy {
		dupe.ScopeName += " Copy"
	}

	dupe.VORsToDraw = DuplicateMap(rs.VORsToDraw)
	dupe.NDBsToDraw = DuplicateMap(rs.NDBsToDraw)
	dupe.FixesToDraw = DuplicateMap(rs.FixesToDraw)
	dupe.AirportsToDraw = DuplicateMap(rs.AirportsToDraw)
	dupe.GeoDrawSet = DuplicateMap(rs.GeoDrawSet)
	dupe.SIDDrawSet = DuplicateMap(rs.SIDDrawSet)
	dupe.STARDrawSet = DuplicateMap(rs.STARDrawSet)
	dupe.ARTCCDrawSet = DuplicateMap(rs.ARTCCDrawSet)
	dupe.ARTCCLowDrawSet = DuplicateMap(rs.ARTCCLowDrawSet)
	dupe.ARTCCHighDrawSet = DuplicateMap(rs.ARTCCHighDrawSet)
	dupe.rangeWarnings = DuplicateMap(rs.rangeWarnings)

	dupe.aircraft = make(map[*Aircraft]*AircraftScopeState)
	for ac, tracked := range rs.aircraft {
		// NOTE: do not copy the TextDrawBuilder over, since we'd be aliasing
		// the slices.
		dupe.aircraft[ac] = &AircraftScopeState{
			isGhost:       tracked.isGhost,
			datablockText: tracked.datablockText}
	}

	dupe.ghostAircraft = make(map[*Aircraft]*Aircraft)
	for ac, gh := range rs.ghostAircraft {
		ghost := *gh // make a copy
		dupe.ghostAircraft[ac] = &ghost
	}
	dupe.pointedOutAircraft = NewTransientMap[*Aircraft, string]()

	dupe.AutoMITAirports = DuplicateMap(rs.AutoMITAirports)

	// don't share those slices...
	dupe.llCommandBuffer = CommandBuffer{}
	dupe.textCommandBuffer = CommandBuffer{}
	dupe.pointsDrawBuilder = PointsDrawBuilder{}
	dupe.linesDrawBuilder = ColoredLinesDrawBuilder{}
	dupe.thickLinesDrawBuilder = ColoredLinesDrawBuilder{}

	dupe.vorsComboState = NewComboBoxState(1)
	dupe.ndbsComboState = NewComboBoxState(1)
	dupe.fixesComboState = NewComboBoxState(1)
	dupe.airportsComboState = NewComboBoxState(1)

	return dupe
}

func (rs *RadarScopePane) getScratchTextDrawBuilder() *TextDrawBuilder {
	rs.tdScratch.Reset()
	return &rs.tdScratch
}

func (rs *RadarScopePane) Activate(cs *ColorScheme) {
	// Temporary: catch unset ones from old config files
	if rs.CRDAConfig.GlideslopeLateralSpread == 0 {
		rs.CRDAConfig = NewCRDAConfig()
	}
	if rs.GeoDrawSet == nil {
		rs.GeoDrawSet = make(map[string]interface{})
	}
	if rs.VORsToDraw == nil {
		rs.VORsToDraw = make(map[string]interface{})
	}
	if rs.NDBsToDraw == nil {
		rs.NDBsToDraw = make(map[string]interface{})
	}
	if rs.FixesToDraw == nil {
		rs.FixesToDraw = make(map[string]interface{})
	}
	if rs.AirportsToDraw == nil {
		rs.AirportsToDraw = make(map[string]interface{})
	}
	if rs.vorsComboState == nil {
		rs.vorsComboState = NewComboBoxState(1)
	}
	if rs.ndbsComboState == nil {
		rs.ndbsComboState = NewComboBoxState(1)
	}
	if rs.fixesComboState == nil {
		rs.fixesComboState = NewComboBoxState(1)
	}
	if rs.airportsComboState == nil {
		rs.airportsComboState = NewComboBoxState(1)
	}
	if rs.AutoMITAirports == nil {
		rs.AutoMITAirports = make(map[string]interface{})
	}
	if rs.pointedOutAircraft == nil {
		rs.pointedOutAircraft = NewTransientMap[*Aircraft, string]()
	}

	// Upgrade old files
	if rs.RadarTracksDrawn == 0 {
		rs.RadarTracksDrawn = 5
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
	rs.initializeAircraft()
}

func (rs *RadarScopePane) initializeAircraft() {
	// Reset and initialize all of these
	rs.aircraft = make(map[*Aircraft]*AircraftScopeState)
	rs.ghostAircraft = make(map[*Aircraft]*Aircraft)

	for _, ac := range server.GetAllAircraft() {
		rs.aircraft[ac] = &AircraftScopeState{}

		if rs.CRDAEnabled {
			if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
				rs.ghostAircraft[ac] = ghost
				rs.aircraft[ghost] = &AircraftScopeState{isGhost: true}
			}
		}
	}
}

func (rs *RadarScopePane) Deactivate() {
	// Drop all of them
	rs.aircraft = nil
	rs.ghostAircraft = nil

	// Free up this memory, FWIW
	rs.llCommandBuffer = CommandBuffer{}
	rs.textCommandBuffer = CommandBuffer{}
	rs.pointsDrawBuilder = PointsDrawBuilder{}
	rs.linesDrawBuilder = ColoredLinesDrawBuilder{}
	rs.thickLinesDrawBuilder = ColoredLinesDrawBuilder{}
}

func (rs *RadarScopePane) Name() string { return rs.ScopeName }

func (rs *RadarScopePane) DrawUI() {
	imgui.InputText("Name", &rs.ScopeName)
	if imgui.InputIntV("Minimum altitude", &rs.MinAltitude, 100, 1000, 0 /* flags */) {
		rs.initializeAircraft()
	}
	if imgui.InputIntV("Maximum altitude", &rs.MaxAltitude, 100, 1000, 0 /* flags */) {
		rs.initializeAircraft()
	}
	if imgui.CollapsingHeader("Aircraft rendering") {
		if rs.DataBlockFormat.DrawUI() {
			for _, state := range rs.aircraft {
				state.datablockTextCurrent = false
			}
		}
		imgui.SliderIntV("Tracks shown", &rs.RadarTracksDrawn, 1, 10, "%d", 0 /* flags */)
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
		if rs.AutoMIT {
			rs.AutoMITAirports = drawAirportSelector(rs.AutoMITAirports, "Arrival airports for auto MIT")
		}
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

		if imgui.Checkbox("Converging runway display aid (CRDA)", &rs.CRDAEnabled) {
			rs.initializeAircraft() // this is overkill, but nbd
		}
		if rs.CRDAEnabled {
			if rs.CRDAConfig.DrawUI() {
				rs.initializeAircraft()
			}
		}
	}
	if imgui.CollapsingHeader("Scope contents") {
		if *devmode {
			imgui.Checkbox("Draw everything", &rs.DrawEverything)
		}

		if imgui.BeginTable("drawbuttons", 5) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Checkbox("Regions", &rs.DrawRegions)
			imgui.TableNextColumn()
			imgui.Checkbox("Labels", &rs.DrawLabels)
			imgui.TableNextColumn()
			imgui.Checkbox("Low Airways", &rs.DrawLowAirways)
			imgui.TableNextColumn()
			imgui.Checkbox("High Airways", &rs.DrawHighAirways)
			imgui.TableNextColumn()
			imgui.Checkbox("Runways", &rs.DrawRunways)
			imgui.EndTable()
		}

		if imgui.BeginTable("voretal", 4) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("VORs")
			imgui.TableNextColumn()
			imgui.Text("NDBs")
			imgui.TableNextColumn()
			imgui.Text("Fixes")
			imgui.TableNextColumn()
			imgui.Text("Airports")

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Checkbox("Draw All##VORs", &rs.DrawVORs)
			imgui.SameLine()
			imgui.Checkbox("Show Names##VORs", &rs.DrawVORNames)
			imgui.TableNextColumn()
			imgui.Checkbox("Draw All##NDBs", &rs.DrawNDBs)
			imgui.SameLine()
			imgui.Checkbox("Show Names##NDBs", &rs.DrawNDBNames)
			imgui.TableNextColumn()
			imgui.Checkbox("Draw All##Fixes", &rs.DrawFixes)
			imgui.SameLine()
			imgui.Checkbox("Show Names##Fixes", &rs.DrawFixNames)
			imgui.TableNextColumn()
			imgui.Checkbox("Draw All##Airports", &rs.DrawAirports)
			imgui.SameLine()
			imgui.Checkbox("Show Names##Airports", &rs.DrawAirportNames)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			config := ComboBoxDisplayConfig{
				ColumnHeaders:    []string{"##name"},
				DrawHeaders:      false,
				SelectAllColumns: true,
				EntryNames:       []string{"##name"},
				InputFlags:       []imgui.InputTextFlags{imgui.InputTextFlagsCharsUppercase},
				TableFlags:       imgui.TableFlagsScrollY,
			}
			DrawComboBox(rs.vorsComboState, config, SortedMapKeys(rs.VORsToDraw), nil,
				/* valid */ func(entries []*string) bool {
					e := *entries[0]
					_, ok := database.VORs[e]
					return e != "" && ok
				},
				/* add */ func(entries []*string) {
					rs.VORsToDraw[*entries[0]] = nil
				},
				/* delete */ func(selected map[string]interface{}) {
					for k := range selected {
						delete(rs.VORsToDraw, k)
					}
				})
			imgui.TableNextColumn()
			DrawComboBox(rs.ndbsComboState, config, SortedMapKeys(rs.NDBsToDraw), nil,
				/* valid */ func(entries []*string) bool {
					e := *entries[0]
					_, ok := database.NDBs[e]
					return e != "" && ok
				},
				/* add */ func(entries []*string) {
					rs.NDBsToDraw[*entries[0]] = nil
				},
				/* delete */ func(selected map[string]interface{}) {
					for k := range selected {
						delete(rs.NDBsToDraw, k)
					}
				})
			imgui.TableNextColumn()
			DrawComboBox(rs.fixesComboState, config, SortedMapKeys(rs.FixesToDraw), nil,
				/* valid */ func(entries []*string) bool {
					e := *entries[0]
					_, ok := database.fixes[e]
					return e != "" && ok
				},
				/* add */ func(entries []*string) {
					rs.FixesToDraw[*entries[0]] = nil
				},
				/* delete */ func(selected map[string]interface{}) {
					for k := range selected {
						delete(rs.FixesToDraw, k)
					}
				})
			imgui.TableNextColumn()
			DrawComboBox(rs.airportsComboState, config, SortedMapKeys(rs.AirportsToDraw), nil,
				/* valid */ func(entries []*string) bool {
					e := *entries[0]
					_, ok := database.airports[e]
					return e != "" && ok
				},
				/* add */ func(entries []*string) {
					rs.AirportsToDraw[*entries[0]] = nil
				},
				/* delete */ func(selected map[string]interface{}) {
					for k := range selected {
						delete(rs.AirportsToDraw, k)
					}
				})

			imgui.EndTable()
		}

		if len(database.geos) > 0 && imgui.TreeNode("Geo") {
			for _, geo := range database.geos {
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

		sidStarHierarchy := func(title string, sidstar []StaticDrawable, drawSet map[string]interface{}) {
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
		sidStarHierarchy("SIDs", database.SIDs, rs.SIDDrawSet)
		sidStarHierarchy("STARs", database.STARs, rs.STARDrawSet)

		artccCheckboxes := func(name string, artcc []StaticDrawable, drawSet map[string]interface{}) {
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
		artccCheckboxes("ARTCC", database.ARTCC, rs.ARTCCDrawSet)
		artccCheckboxes("ARTCC Low", database.ARTCCLow, rs.ARTCCLowDrawSet)
		artccCheckboxes("ARTCC High", database.ARTCCHigh, rs.ARTCCHighDrawSet)
	}
}

func (rs *RadarScopePane) Update(updates *ControlUpdates) {
	if updates == nil {
		return
	}

	for ac := range updates.addedAircraft {
		rs.aircraft[ac] = &AircraftScopeState{}
		if rs.CRDAEnabled {
			if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
				rs.ghostAircraft[ac] = ghost
				rs.aircraft[ghost] = &AircraftScopeState{isGhost: true}
			}
		}
	}

	for ac := range updates.removedAircraft {
		if ghost, ok := rs.ghostAircraft[ac]; ok {
			delete(rs.aircraft, ghost)
		}
		delete(rs.aircraft, ac)
		delete(rs.ghostAircraft, ac)
	}

	for ac := range updates.modifiedAircraft {
		if rs.CRDAEnabled {
			// always start out by removing the old ghost
			if oldGhost, ok := rs.ghostAircraft[ac]; ok {
				delete(rs.aircraft, oldGhost)
				delete(rs.ghostAircraft, ac)
			}
		}

		if state, ok := rs.aircraft[ac]; !ok {
			rs.aircraft[ac] = &AircraftScopeState{}
		} else {
			state.datablockTextCurrent = false
		}

		// new ghost
		if rs.CRDAEnabled {
			if ghost := rs.CRDAConfig.GetGhost(ac); ghost != nil {
				rs.ghostAircraft[ac] = ghost
				rs.aircraft[ghost] = &AircraftScopeState{isGhost: true}
			}
		}
	}

	for ac, controller := range updates.pointOuts {
		rs.pointedOutAircraft.Add(ac, controller, 5*time.Second)
	}
}

func mul4v(m *mgl32.Mat4, v [2]float32) [2]float32 {
	return [2]float32{m[0]*v[0] + m[4]*v[1], m[1]*v[0] + m[5]*v[1]}
}

func mul4p(m *mgl32.Mat4, p [2]float32) [2]float32 {
	return add2f(mul4v(m, p), [2]float32{m[12], m[13]})
}

func (rs *RadarScopePane) Draw(ctx *PaneContext, cb *CommandBuffer) {
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
	td := rs.getScratchTextDrawBuilder()
	height := ctx.paneExtent.Height()
	td.AddText(rs.ScopeName, [2]float32{float32(rs.labelFont.size) / 2, height - float32(rs.labelFont.size)/2},
		TextStyle{font: rs.labelFont, color: ctx.cs.Text})
	td.GenerateCommands(&rs.textCommandBuffer)

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
	rs.drawRoute(ctx, latLongFromWindowV)
	rs.drawCRDARegions(ctx)

	// Mouse events last, so that the datablock bounds are current.
	rs.consumeMouseEvents(ctx, latLongFromWindowP, latLongFromWindowV, windowFromLatLongP)

	rs.pointsDrawBuilder.GenerateCommands(&rs.llCommandBuffer)
	rs.linesDrawBuilder.GenerateCommands(&rs.llCommandBuffer)
	rs.llCommandBuffer.LineWidth(3 * rs.LineWidth * ctx.highDPIScale)
	rs.thickLinesDrawBuilder.GenerateCommands(&rs.llCommandBuffer)

	cb.Call(rs.llCommandBuffer)
	cb.Call(rs.textCommandBuffer)
}

func (rs *RadarScopePane) getViewingMatrices(ctx *PaneContext) (latLongFromWindow mgl32.Mat4, ndcFromLatLong mgl32.Mat4) {
	// Translate to the center point
	ndcFromLatLong = mgl32.Translate3D(-rs.Center[0], -rs.Center[1], 0)

	// Scale based on range and nm per latitude / longitude
	sc := mgl32.Scale3D(database.NmPerLongitude/rs.Range, database.NmPerLatitude/rs.Range, 1)
	ndcFromLatLong = sc.Mul4(ndcFromLatLong)

	// Account for magnetic variation and any user-specified rotation
	rot := -radians(rs.RotationAngle + database.MagneticVariation)
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
	// Reset the slices so we can draw new lines and points
	rs.linesDrawBuilder.Reset()
	rs.thickLinesDrawBuilder.Reset()
	rs.pointsDrawBuilder.Reset()

	rs.llCommandBuffer.Reset()
	rs.llCommandBuffer.LoadProjectionMatrix(ndcFromLatLongMtx)
	rs.llCommandBuffer.LoadModelViewMatrix(mgl32.Ident4())
	rs.llCommandBuffer.PointSize(rs.PointSize)
	rs.llCommandBuffer.LineWidth(rs.LineWidth * ctx.highDPIScale)

	rs.textCommandBuffer.Reset()
	ctx.SetWindowCoordinateMatrices(&rs.textCommandBuffer)
	rs.textCommandBuffer.PointSize(rs.PointSize)
	rs.textCommandBuffer.LineWidth(rs.LineWidth * ctx.highDPIScale)
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
	rs.llCommandBuffer.LineWidth(rs.LineWidth * ctx.highDPIScale)

	if rs.DrawEverything || rs.DrawRunways {
		rs.llCommandBuffer.SetRGB(ctx.cs.Runway)
		rs.llCommandBuffer.Call(database.runwayCommandBuffer)
	}

	if rs.DrawEverything || rs.DrawRegions {
		for _, region := range database.regions {
			if Overlaps(region.bounds, viewBounds) {
				if region.name == "" {
					rs.llCommandBuffer.SetRGB(ctx.cs.Region)
				} else if rgb, ok := ctx.cs.DefinedColors[region.name]; ok {
					rs.llCommandBuffer.SetRGB(*rgb)
				} else if rgb, ok := database.sectorFileColors[region.name]; ok {
					rs.llCommandBuffer.SetRGB(rgb)
				} else {
					lg.Errorf("%s: defined color not found for region", region.name)
					rs.llCommandBuffer.SetRGB(RGB{0.5, 0.5, 0.5})
				}
				rs.llCommandBuffer.Call(region.cb)
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

	if rs.DrawEverything || rs.DrawVORs {
		square := [][2]float32{vtx(-1, -1), vtx(1, -1), vtx(1, 1), vtx(-1, 1)}
		for _, vor := range database.VORs {
			rs.linesDrawBuilder.AddPolyline(vor, ctx.cs.VOR, square)
		}
	}
	if rs.DrawEverything || rs.DrawNDBs {
		fliptri := [][2]float32{vtx(-1.5, 1.5), vtx(1.5, 1.5), vtx(0, -0.5)}
		for _, ndb := range database.NDBs {
			// flipped triangles
			rs.linesDrawBuilder.AddPolyline(ndb, ctx.cs.NDB, fliptri)
		}
	}

	if rs.DrawEverything || rs.DrawFixes {
		uptri := [][2]float32{vtx(-1.5, -0.5), vtx(1.5, -0.5), vtx(0, 1.5)}
		for _, fix := range database.fixes {
			// upward-pointing triangles
			rs.linesDrawBuilder.AddPolyline(fix, ctx.cs.Fix, uptri)
		}
	} else {
		uptri := [][2]float32{vtx(-1.5, -0.5), vtx(1.5, -0.5), vtx(0, 1.5)}
		for name := range rs.FixesToDraw {
			if loc, ok := database.fixes[name]; !ok {
				// May happen when a new sector file is loaded.
				//lg.Printf("%s: selected fix not found in sector file data!", loc)
			} else {
				rs.linesDrawBuilder.AddPolyline(loc, ctx.cs.Fix, uptri)
			}
		}
	}
	if rs.DrawEverything || rs.DrawAirports {
		square := [][2]float32{vtx(-1, -1), vtx(1, -1), vtx(1, 1), vtx(-1, 1)}
		for _, ap := range database.airports {
			rs.linesDrawBuilder.AddPolyline(ap, ctx.cs.Airport, square)
		}
	}

	drawARTCCLines := func(artcc []StaticDrawable, drawSet map[string]interface{}) {
		for _, artcc := range artcc {
			if _, draw := drawSet[artcc.name]; (draw || rs.DrawEverything) && Overlaps(artcc.bounds, viewBounds) {
				rs.llCommandBuffer.Call(artcc.cb)
			}
		}
	}
	rs.llCommandBuffer.SetRGB(ctx.cs.ARTCC)
	drawARTCCLines(database.ARTCC, rs.ARTCCDrawSet)
	drawARTCCLines(database.ARTCCLow, rs.ARTCCLowDrawSet)
	drawARTCCLines(database.ARTCCHigh, rs.ARTCCHighDrawSet)

	for _, sid := range database.SIDs {
		_, draw := rs.SIDDrawSet[sid.name]
		if (rs.DrawEverything || draw) && Overlaps(sid.bounds, viewBounds) {
			rs.llCommandBuffer.Call(sid.cb)
		}
	}
	for _, star := range database.STARs {
		_, draw := rs.STARDrawSet[star.name]
		if (rs.DrawEverything || draw) && Overlaps(star.bounds, viewBounds) {
			rs.llCommandBuffer.Call(star.cb)
		}
	}

	for _, geo := range database.geos {
		_, draw := rs.GeoDrawSet[geo.name]
		if (rs.DrawEverything || draw) && Overlaps(geo.bounds, viewBounds) {
			rs.llCommandBuffer.Call(geo.cb)
		}
	}

	drawAirwayLabels := func(labels []Label, color RGB) {
		td := rs.getScratchTextDrawBuilder()
		for _, label := range labels {
			textPos := windowFromLatLongP(label.p)
			if inWindow(textPos) {
				style := TextStyle{
					font:            rs.labelFont,
					color:           color,
					drawBackground:  true,
					backgroundColor: ctx.cs.Background}
				td.AddTextCentered(label.name, textPos, style)
			}
		}
		td.GenerateCommands(&rs.textCommandBuffer)
	}

	if rs.DrawEverything || rs.DrawLowAirways {
		rs.llCommandBuffer.SetRGB(ctx.cs.LowAirway)
		rs.llCommandBuffer.Call(database.lowAirwayCommandBuffer)
		drawAirwayLabels(database.lowAirwayLabels, ctx.cs.LowAirway)
	}
	if rs.DrawEverything || rs.DrawHighAirways {
		rs.llCommandBuffer.SetRGB(ctx.cs.HighAirway)
		rs.llCommandBuffer.Call(database.highAirwayCommandBuffer)
		drawAirwayLabels(database.highAirwayLabels, ctx.cs.HighAirway)
	}

	// Labels
	if rs.DrawEverything || rs.DrawLabels {
		td := rs.getScratchTextDrawBuilder()
		for _, label := range database.labels {
			if viewBounds.Inside(label.p) {
				style := TextStyle{font: rs.labelFont, color: label.color}
				td.AddTextCentered(label.name, windowFromLatLongP(label.p), style)
			}
		}
		td.GenerateCommands(&rs.textCommandBuffer)
	}

	// VOR, NDB, fix, and airport names
	const (
		DrawLeft = iota
		DrawRight
		DrawBelow
	)
	fixtext := func(name string, p Point2LL, color RGB, td *TextDrawBuilder, mode int) {
		var offset [2]float32
		switch mode {
		case DrawLeft:
			bx, _ := rs.labelFont.BoundText(name, 0)
			offset = [2]float32{float32(-5 - bx), 1 + float32(rs.labelFont.size/2)}
		case DrawRight:
			offset = [2]float32{7, 1 + float32(rs.labelFont.size/2)}
		case DrawBelow:
			offset = [2]float32{0, float32(-rs.labelFont.size)}
		}

		if viewBounds.Inside(p) {
			pw := add2f(windowFromLatLongP(p), offset)
			if inWindow(pw) {
				if mode == DrawBelow {
					td.AddTextCentered(name, pw, TextStyle{font: rs.labelFont, color: color})
				} else {
					td.AddText(name, pw, TextStyle{font: rs.labelFont, color: color})
				}
			}
		}
	}

	drawloc := func(drawEverything bool, selected map[string]interface{},
		items map[string]Point2LL, color RGB, td *TextDrawBuilder, mode int) {
		if drawEverything {
			for name, p := range items {
				fixtext(name, p, color, td, mode)
			}
		} else {
			for name := range selected {
				if p, ok := items[name]; !ok {
					// May happen when a new sector file is loaded
				} else {
					fixtext(name, p, color, td, mode)
				}
			}
		}
	}

	td := rs.getScratchTextDrawBuilder()
	if rs.DrawVORNames {
		drawloc(rs.DrawEverything || rs.DrawVORs, rs.VORsToDraw, database.VORs, ctx.cs.VOR, td, DrawLeft)
	}
	if rs.DrawNDBNames {
		drawloc(rs.DrawEverything || rs.DrawNDBs, rs.NDBsToDraw, database.NDBs, ctx.cs.NDB, td, DrawLeft)
	}
	if rs.DrawFixNames {
		drawloc(rs.DrawEverything || rs.DrawFixes, rs.FixesToDraw, database.fixes, ctx.cs.Fix, td, DrawRight)
	}
	if rs.DrawAirportNames {
		drawloc(rs.DrawEverything || rs.DrawAirports, rs.AirportsToDraw, database.airports, ctx.cs.Airport, td, DrawBelow)
	}
	td.GenerateCommands(&rs.textCommandBuffer)
}

func (rs *RadarScopePane) drawMIT(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32) {
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()

	annotatedLine := func(p0 Point2LL, p1 Point2LL, color RGB, text string) {
		// Center the text
		textPos := windowFromLatLongP(mid2ll(p0, p1))
		// Cull text based on center point
		if textPos[0] >= 0 && textPos[0] < width && textPos[1] >= 0 && textPos[1] < height {
			td := rs.getScratchTextDrawBuilder()
			style := TextStyle{font: rs.labelFont, color: color, drawBackground: true, backgroundColor: ctx.cs.Background}
			td.AddTextCentered(text, textPos, style)
			td.GenerateCommands(&rs.textCommandBuffer)
		}

		rs.linesDrawBuilder.AddLine(p0, p1, color)
	}

	// Don't do AutoMIT if a sequence has been manually specified
	if rs.AutoMIT && len(positionConfig.mit) == 0 {
		inTrail := func(front Arrival, back Arrival) bool {
			dalt := back.aircraft.Altitude() - front.aircraft.Altitude()
			backHeading := back.aircraft.Heading()
			angle := headingp2ll(back.aircraft.Position(), front.aircraft.Position(),
				database.MagneticVariation)
			diff := headingDifference(backHeading, angle)

			return diff < 150 && dalt < 3000
		}

		arr := getDistanceSortedArrivals(rs.AutoMITAirports)

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
	latLongFromWindowV func(p [2]float32) Point2LL, windowFromLatLongP func(p Point2LL) [2]float32,
	td *TextDrawBuilder) {
	px := float32(3) // TODO: make configurable?
	dx := latLongFromWindowV([2]float32{1, 0})
	dy := latLongFromWindowV([2]float32{0, 1})
	// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
	delta := func(p Point2LL, x, y float32) Point2LL {
		return add2ll(p, add2ll(scale2f(dx, x), scale2f(dy, y)))
	}

	// Draw tracks
	if ac.mode == Standby {
		rs.pointsDrawBuilder.AddPoint(p, color)
	} else if ac.squawk == Squawk(1200) {
		pxb := px * .7    // a little smaller
		sc := float32(.8) // leave a little space at the corners
		rs.linesDrawBuilder.AddLine(delta(p, -sc*pxb, -pxb), delta(p, sc*pxb, -pxb), color)
		rs.linesDrawBuilder.AddLine(delta(p, pxb, -sc*pxb), delta(p, pxb, sc*pxb), color)
		rs.linesDrawBuilder.AddLine(delta(p, sc*pxb, pxb), delta(p, -sc*pxb, pxb), color)
		rs.linesDrawBuilder.AddLine(delta(p, -pxb, sc*pxb), delta(p, -pxb, -sc*pxb), color)
	} else if controller := server.GetTrackingController(ac.Callsign()); controller != "" {
		ch := "?"
		if ctrl := server.GetController(controller); ctrl != nil {
			if pos := ctrl.GetPosition(); pos != nil {
				ch = pos.scope
			}
		}
		pw := windowFromLatLongP(p)
		td.AddTextCentered(ch, pw, TextStyle{font: rs.datablockFont, color: color})
		return
	} else {
		// diagonals
		diagPx := px * 0.707107 /* 1/sqrt(2) */
		rs.linesDrawBuilder.AddLine(delta(p, -diagPx, -diagPx), delta(p, diagPx, diagPx), color)
		rs.linesDrawBuilder.AddLine(delta(p, diagPx, -diagPx), delta(p, -diagPx, diagPx), color)
		// horizontal line
		rs.linesDrawBuilder.AddLine(delta(p, -px, 0), delta(p, px, 0), color)
	}
}

func (rs *RadarScopePane) drawTracks(ctx *PaneContext, latLongFromWindowV func(p [2]float32) Point2LL,
	windowFromLatLongP func(p Point2LL) [2]float32) {
	td := rs.getScratchTextDrawBuilder()
	now := server.CurrentTime()
	for ac, state := range rs.aircraft {
		if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
			continue
		}

		color := ctx.cs.Track
		if state.isGhost {
			color = ctx.cs.GhostDataBlock
		}

		// Draw in reverse order so that if it's not moving, more recent tracks (which will have
		// more contrast with the background), will be the ones that are visible.
		for i := rs.RadarTracksDrawn; i > 0; i-- {
			// blend the track color with the background color; more
			// background further into history but only a 50/50 blend
			// at the oldest track.
			// 1e-6 addition to avoid NaN with RadarTracksDrawn == 1.
			x := float32(i-1) / (1e-6 + float32(2*(rs.RadarTracksDrawn-1))) // 0 <= x <= 0.5
			trackColor := lerpRGB(x, color, ctx.cs.Background)

			rs.drawTrack(ac, ac.tracks[i-1].position, trackColor, latLongFromWindowV, windowFromLatLongP, td)
		}
	}
	td.GenerateCommands(&rs.textCommandBuffer)
}

func (rs *RadarScopePane) updateDatablockTextAndBounds(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32) {
	squawkCount := make(map[Squawk]int)
	for ac, state := range rs.aircraft {
		if !state.isGhost {
			squawkCount[ac.squawk]++
		}
	}
	now := server.CurrentTime()
	for ac, state := range rs.aircraft {
		if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
			continue
		}

		if !state.datablockTextCurrent {
			hopo := ""
			if controller := server.InboundHandoffController(ac.Callsign()); controller != "" {
				hopo += FontAwesomeIconArrowLeft + controller
			}
			if controller := server.OutboundHandoffController(ac.Callsign()); controller != "" {
				hopo += FontAwesomeIconArrowRight + controller
			}
			if controller, ok := rs.pointedOutAircraft.Get(ac); ok {
				hopo += FontAwesomeIconExclamationTriangle + controller
			}
			if hopo != "" {
				hopo = "\n" + hopo
			}

			state.datablockText[0] = rs.DataBlockFormat.Format(ac, squawkCount[ac.squawk] != 1, 0) + hopo
			state.datablockText[1] = rs.DataBlockFormat.Format(ac, squawkCount[ac.squawk] != 1, 1) + hopo
			state.datablockTextCurrent = true

			bx0, by0 := rs.datablockFont.BoundText(state.datablockText[0], -2)
			bx1, by1 := rs.datablockFont.BoundText(state.datablockText[1], -2)
			bx, by := max(float32(bx0), float32(bx1)), max(float32(by0), float32(by1))
			state.datablockBounds = Extent2D{p0: [2]float32{0, -by}, p1: [2]float32{bx, 0}}
		}
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
	offsetSelfOnly := func(ac *Aircraft, info *AircraftScopeState) [2]float32 {
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

	now := server.CurrentTime()
	if !rs.AutomaticDatablockLayout {
		// layout just wrt our own track; ignore everyone else
		for ac, state := range rs.aircraft {
			if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
				continue
			}

			if state.datablockManualOffset[0] != 0 || state.datablockManualOffset[1] != 0 {
				state.datablockAutomaticOffset = [2]float32{0, 0}
				continue
			}

			state.datablockAutomaticOffset = offsetSelfOnly(ac, state)
		}
		return
	} else {
		// Sort them by callsign so our iteration order is consistent
		// TODO: maybe sort by the ac pointer to be more fair across airlines?
		var aircraft []*Aircraft
		width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
		for ac := range rs.aircraft {
			if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
				continue
			}

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
			state := rs.aircraft[ac]
			if state.datablockManualOffset[0] != 0 || state.datablockManualOffset[1] != 0 {
				pw := windowFromLatLongP(ac.Position())
				b := state.WindowDatablockBounds(pw).Expand(5)
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
			state := rs.aircraft[ac]
			offset := offsetSelfOnly(ac, state)
			// TODO: we could do this incrementally a few pixels per frame
			// even if we could go all the way. Though then we would need
			// to consider all datablocks along the path...
			netOffset := sub2f(offset, state.datablockAutomaticOffset)

			pw := windowFromLatLongP(ac.Position())
			db := state.WindowDatablockBounds(pw).Expand(5).Offset(netOffset)
			if allowed(db) {
				placed[i] = true
				datablockBounds[i] = db
				state.datablockAutomaticOffset = offset
			}
		}

		// Third pass: all of the tricky ones...
		// FIXME: temporal stability?
		for i, ac := range aircraft {
			if placed[i] {
				continue
			}
			state := rs.aircraft[ac]

			if state.datablockAutomaticOffset[0] == 0 && state.datablockAutomaticOffset[1] == 0 {
				// First time seen: start with the ideal. Otherwise
				// start with whatever we ended up with last time.
				state.datablockAutomaticOffset = offsetSelfOnly(ac, state)
			}
		}

		// Initialize current datablockBounds for all of the unplaced aircraft
		for i, ac := range aircraft {
			if placed[i] {
				continue
			}
			state := rs.aircraft[ac]

			pw := windowFromLatLongP(ac.Position())
			datablockBounds[i] = state.WindowDatablockBounds(pw).Expand(5)
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

				state := rs.aircraft[ac]
				state.datablockAutomaticOffset = add2f(state.datablockAutomaticOffset, force)
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
				state := rs.aircraft[ac]
				goBack := sub2f(offsetSelfOnly(ac, state), state.datablockAutomaticOffset)
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
					state.datablockAutomaticOffset = add2f(state.datablockAutomaticOffset, force)
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

	callsign := ac.Callsign()
	if server.InboundHandoffController(callsign) != "" {
		return cs.HandingOffDataBlock
	}
	if server.OutboundHandoffController(callsign) != "" {
		return cs.HandingOffDataBlock
	}

	controller := server.GetTrackingController(callsign)
	if controller != "" && controller == server.Callsign() {
		return cs.TrackedDataBlock
	}

	return cs.UntrackedDataBlock
}

func (rs *RadarScopePane) drawDatablocks(ctx *PaneContext, windowFromLatLongP func(p Point2LL) [2]float32,
	latLongFromWindowP func(p [2]float32) Point2LL) {
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	paneBounds := Extent2D{p0: [2]float32{0, 0}, p1: [2]float32{width, height}}

	// Sort the aircraft so that they are always drawn in the same order
	// (go's map iterator randomization otherwise randomizes the order,
	// which can cause shimmering when datablocks overlap (especially if
	// one is selected). We'll go with alphabetical by callsign, with the
	// selected aircraft, if any, always drawn last.
	aircraft := SortedMapKeysPred(rs.aircraft, func(a **Aircraft, b **Aircraft) bool {
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
	td := rs.getScratchTextDrawBuilder()
	now := server.CurrentTime()
	actualNow := time.Now()
	for _, ac := range aircraft {
		if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
			continue
		}

		pac := windowFromLatLongP(ac.Position())
		state := rs.aircraft[ac]
		bbox := state.WindowDatablockBounds(pac)

		if !Overlaps(paneBounds, bbox) {
			continue
		}

		color := rs.datablockColor(ac, ctx.cs)

		// Draw characters starting at the upper left.
		flashCycle := actualNow.Second() & 1
		td.AddText(state.datablockText[flashCycle], [2]float32{bbox.p0[0], bbox.p1[1]},
			TextStyle{font: rs.datablockFont, color: color, lineSpacing: -2})

		// visualize bounds
		if false {
			var ld ColoredLinesDrawBuilder
			bx, by := rs.datablockFont.BoundText(state.datablockText[0], -2)
			ld.AddPolyline([2]float32{bbox.p0[0], bbox.p1[1]}, RGB{1, 0, 0},
				[][2]float32{[2]float32{float32(bx), 0},
					[2]float32{float32(bx), float32(-by)},
					[2]float32{float32(0), float32(-by)},
					[2]float32{float32(0), float32(0)}})
			ld.GenerateCommands(&rs.textCommandBuffer)
		}

		drawLine := rs.DataBlockFormat != DataBlockFormatNone

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
				rs.linesDrawBuilder.AddLine(ac.Position(), [2]float32{pll[0], pll[1]}, color)
			}
		}
	}
	td.GenerateCommands(&rs.textCommandBuffer)
}

func (rs *RadarScopePane) drawVectorLines(ctx *PaneContext, windowFromLatLongP func(Point2LL) [2]float32,
	latLongFromWindowP func([2]float32) Point2LL) {
	if !rs.DrawVectorLine {
		return
	}

	now := server.CurrentTime()
	for ac, state := range rs.aircraft {
		if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
			continue
		}

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
			if state.isGhost {
				rs.linesDrawBuilder.AddLine(start, end, ctx.cs.GhostDataBlock)
			} else {
				rs.linesDrawBuilder.AddLine(start, end, ctx.cs.Track)
			}
		}
	}
}

type Conflict struct {
	aircraft [2]*Aircraft
	limits   RangeLimits
}

func (rs *RadarScopePane) getConflicts() (warning []Conflict, violation []Conflict) {
	aircraft, state := FlattenMap(rs.aircraft)

	now := server.CurrentTime()
	for i, ac1 := range aircraft {
		if state[i].isGhost || ac1.LostTrack(now) ||
			ac1.Altitude() < int(rs.MinAltitude) || ac1.Altitude() > int(rs.MaxAltitude) {
			continue
		}

		for j := i + 1; j < len(aircraft); j++ {
			ac2 := aircraft[j]
			if state[j].isGhost || ac2.LostTrack(now) ||
				ac2.Altitude() < int(rs.MinAltitude) || ac2.Altitude() > int(rs.MaxAltitude) {
				continue
			}

			var r RangeLimits
			if ac1.flightPlan != nil && ac1.flightPlan.rules == IFR {
				if ac2.flightPlan != nil && ac2.flightPlan.rules == IFR {
					r = rs.RangeLimits[IFR_IFR]
				} else {
					r = rs.RangeLimits[IFR_VFR]
				}
			} else {
				if ac2.flightPlan != nil && ac2.flightPlan.rules == IFR {
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

	td := rs.getScratchTextDrawBuilder()
	for h := float32(5); h <= 360; h += 5 {
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
		rs.linesDrawBuilder.AddLine(pell, pill, ctx.cs.Compass)

		if int(h)%10 == 0 {
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

			td.AddText(string(label), pText, TextStyle{font: rs.labelFont, color: ctx.cs.Compass})
		}
	}
	td.GenerateCommands(&rs.textCommandBuffer)
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
		for _, w := range warnings {
			nsegs := 360
			xradius := w.limits.WarningLateral / database.NmPerLongitude
			yradius := w.limits.WarningLateral / database.NmPerLatitude
			rs.linesDrawBuilder.AddCircle(w.aircraft[0].Position(), xradius, yradius, nsegs, ctx.cs.Caution)
			rs.linesDrawBuilder.AddCircle(w.aircraft[1].Position(), xradius, yradius, nsegs, ctx.cs.Caution)
		}
		for _, v := range violations {
			nsegs := 360
			xradius := v.limits.ViolationLateral / database.NmPerLongitude
			yradius := v.limits.ViolationLateral / database.NmPerLatitude
			rs.linesDrawBuilder.AddCircle(v.aircraft[0].Position(), xradius, yradius, nsegs, ctx.cs.Error)
			rs.linesDrawBuilder.AddCircle(v.aircraft[1].Position(), xradius, yradius, nsegs, ctx.cs.Error)
		}

	case RangeIndicatorLine:
		annotatedLine := func(p0 Point2LL, p1 Point2LL, color RGB, text string) {
			textPos := windowFromLatLongP(mid2ll(p0, p1))
			td := rs.getScratchTextDrawBuilder()
			style := TextStyle{
				font:            rs.labelFont,
				color:           color,
				drawBackground:  true,
				backgroundColor: ctx.cs.Background}
			td.AddTextCentered(text, textPos, style)
			td.GenerateCommands(&rs.textCommandBuffer)
			rs.linesDrawBuilder.AddLine(p0, p1, color)
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
	rs.linesDrawBuilder.AddLine(p0, p1, ctx.cs.SelectedDataBlock)

	// distance between the two points in nm
	dist := nmdistance2ll(p0, p1)

	// heading and reciprocal
	hdg := int(headingp2ll(p0, p1, database.MagneticVariation) + 0.5)
	if hdg == 0 {
		hdg = 360
	}
	rhdg := hdg + 180
	if rhdg > 360 {
		rhdg -= 360
	}
	label := fmt.Sprintf(" %.1f nm \n%d / %d", dist, hdg, rhdg)
	td := rs.getScratchTextDrawBuilder()
	style := TextStyle{
		font:            rs.labelFont,
		color:           ctx.cs.SelectedDataBlock,
		drawBackground:  true,
		backgroundColor: ctx.cs.Background}
	textPos := mid2f(rs.primaryDragStart, rs.primaryDragEnd)
	td.AddTextCentered(label, textPos, style)
	td.GenerateCommands(&rs.textCommandBuffer)
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
		color = lerpRGB(x, ctx.cs.Background, color)
	}

	radius := float32(10)
	dx := latLongFromWindowV([2]float32{radius, 0})
	dy := latLongFromWindowV([2]float32{0, radius})
	rs.thickLinesDrawBuilder.AddCircle(p, length2ll(dx), length2ll(dy), 360, color)
}

func (rs *RadarScopePane) drawRoute(ctx *PaneContext, latLongFromWindowV func([2]float32) Point2LL) {
	remaining := time.Until(positionConfig.drawnRouteEndTime)
	if remaining < 0 {
		return
	}

	color := ctx.cs.Error
	fade := 1.5
	if sec := remaining.Seconds(); sec < fade {
		x := float32(sec / fade)
		color = lerpRGB(x, ctx.cs.Background, color)
	}

	var pPrev Point2LL
	for _, waypoint := range strings.Split(positionConfig.drawnRoute, " ") {
		if p, ok := database.Locate(waypoint); !ok {
			// no worries; most likely it's a SID, STAR, or airway..
		} else {
			if !pPrev.IsZero() {
				rs.thickLinesDrawBuilder.AddLine(pPrev, p, color)
			}
			pPrev = p
		}
	}
}

func (rs *RadarScopePane) consumeMouseEvents(ctx *PaneContext, latLongFromWindowP func([2]float32) Point2LL,
	latLongFromWindowV func([2]float32) Point2LL, windowFromLatLongP func(Point2LL) [2]float32) {
	if ctx.mouse == nil {
		return
	}

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
		}
	}

	// Consume mouse wheel
	if ctx.mouse.wheel[1] != 0 {
		scale := pow(1.05, ctx.mouse.wheel[1])

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

	if rs.acSelectedByDatablock != nil {
		if ctx.mouse.dragging[mouseButtonPrimary] {
			ac := rs.acSelectedByDatablock
			state := rs.aircraft[ac]
			state.datablockManualOffset =
				add2f(state.datablockAutomaticOffset, add2f(state.datablockManualOffset, ctx.mouse.dragDelta))
			state.datablockAutomaticOffset = [2]float32{0, 0}
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

		// Allow clicking on any track
		for ac := range rs.aircraft {
			pw := windowFromLatLongP(ac.Position())
			dist := distance2f(pw, ctx.mouse.pos)

			if dist < clickedDistance {
				clickedAircraft = ac
				clickedDistance = dist
			}
		}

		// And now check and see if we clicked on a datablock (TODO: check for held)
		now := server.CurrentTime()
		for ac, state := range rs.aircraft {
			if ac.LostTrack(now) || ac.Altitude() < int(rs.MinAltitude) || ac.Altitude() > int(rs.MaxAltitude) {
				continue
			}

			pw := windowFromLatLongP(ac.Position())
			db := state.WindowDatablockBounds(pw)
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
	for _, rwy := range database.runways[c.Airport] {
		if rwy.number == n {
			return &rwy
		}
	}
	return nil
}

func (c *CRDAConfig) getRunways() (ghostSource *Runway, ghostDestination *Runway) {
	for i, rwy := range database.runways[c.Airport] {
		if rwy.number == c.PrimaryRunway {
			ghostSource = &database.runways[c.Airport][i]
		}
		if rwy.number == c.SecondaryRunway {
			ghostDestination = &database.runways[c.Airport][i]
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

	airport, ok := database.FAA.airports[c.Airport]
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
	h := headingp2ll(ac.Position(), src.threshold, database.MagneticVariation)
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
	if runways, ok := database.runways[c.Airport]; !ok {
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
			//		rs.linesDrawBuilder.AddLine(src.threshold, src.end, RGB{0, 1, 0})
			//		rs.linesDrawBuilder.AddLine(dst.threshold, dst.end, RGB{0, 1, 0})
			rs.pointsDrawBuilder.AddPoint(p, RGB{1, 0, 0})
		}
	}

	src, _ := rs.CRDAConfig.getRunways()
	if src == nil {
		return
	}

	// we have the runway heading, but we want to go the opposite direction
	// and then +/- HeadingTolerance.
	rota := src.heading + 180 - rs.CRDAConfig.GlideslopeLateralSpread - database.MagneticVariation
	rotb := src.heading + 180 + rs.CRDAConfig.GlideslopeLateralSpread - database.MagneticVariation

	// Lay out the vectors in nm space, not lat-long
	sa, ca := sin(radians(rota)), cos(radians(rota))
	va := [2]float32{sa, ca}
	dist := float32(25)
	va = scale2f(va, dist)

	sb, cb := sin(radians(rotb)), cos(radians(rotb))
	vb := scale2f([2]float32{sb, cb}, dist)

	// Over to lat-long to draw the lines
	vall, vbll := nm2ll(va), nm2ll(vb)
	rs.linesDrawBuilder.AddLine(src.threshold, add2ll(src.threshold, vall), ctx.cs.Caution)
	rs.linesDrawBuilder.AddLine(src.threshold, add2ll(src.threshold, vbll), ctx.cs.Caution)
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
	speed := ac.GroundSpeed()
	fp := ac.flightPlan

	if fp == nil {
		return ac.squawk.String() + fmt.Sprintf(" %03d", alt100s)
	}

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
	if ac.voiceCapability == VoiceReceive {
		datablock.WriteString("/r")
	} else if ac.voiceCapability == VoiceText {
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

		dalt := ac.AltitudeChange()
		ascending, descending := dalt > 250, dalt < -250
		altAnnotation := " "
		if ac.tempAltitude != 0 && abs(ac.Altitude()-ac.tempAltitude) < 300 {
			altAnnotation = "T "
		} else if ac.flightPlan.altitude != 0 &&
			abs(ac.Altitude()-ac.flightPlan.altitude) < 300 {
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
		t := sqrt(sqr(rs.VectorLineExtent) / (sqr(h[1]*database.NmPerLatitude) + sqr(h[0]*database.NmPerLongitude)))
		return add2ll(ac.Position(), scale2ll(h, t))

	case VectorLineMinutes:
		// HeadingVector() comes back scaled for one minute in the future.
		vectorEnd := scale2ll(ac.HeadingVector(), rs.VectorLineExtent)
		return add2ll(ac.Position(), vectorEnd)

	default:
		lg.Printf("unexpected vector line mode: %d", rs.VectorLineMode)
		return Point2LL{}
	}
}
