// pkg/panes/stars/stars.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
	"github.com/tosone/minimp3"
)

// IFR TRACON separation requirements
const LateralMinimum = 3
const VerticalMinimum = 1000

// STARS ∆ is character 0x80 in the font
const STARSTriangleCharacter = string(rune(0x80))

// Filled upward-pointing triangle
const STARSFilledUpTriangle = string(rune(0x1e))

var (
	STARSBackgroundColor    = renderer.RGB{.2, .2, .2} // at 100 contrast
	STARSListColor          = renderer.RGB{.1, .9, .1}
	STARSTextAlertColor     = renderer.RGB{1, 0, 0}
	STARSMapColor           = renderer.RGB{.55, .55, .55}
	STARSCompassColor       = renderer.RGB{.55, .55, .55}
	STARSRangeRingColor     = renderer.RGB{.55, .55, .55}
	STARSTrackBlockColor    = renderer.RGB{0.12, 0.48, 1}
	STARSTrackHistoryColors = [5]renderer.RGB{
		renderer.RGB{.12, .31, .78},
		renderer.RGB{.28, .28, .67},
		renderer.RGB{.2, .2, .51},
		renderer.RGB{.16, .16, .43},
		renderer.RGB{.12, .12, .35},
	}
	STARSJRingConeColor         = renderer.RGB{.5, .5, 1}
	STARSTrackedAircraftColor   = renderer.RGB{1, 1, 1}
	STARSUntrackedAircraftColor = renderer.RGB{0, 1, 0}
	STARSInboundPointOutColor   = renderer.RGB{1, 1, 0}
	STARSGhostColor             = renderer.RGB{1, 1, 0}
	STARSSelectedAircraftColor  = renderer.RGB{0, 1, 1}

	STARSATPAWarningColor = renderer.RGB{1, 1, 0}
	STARSATPAAlertColor   = renderer.RGB{1, .215, 0}
)

const NumPreferenceSets = 32

type STARSPane struct {
	CurrentPreferenceSet  PreferenceSet
	SelectedPreferenceSet int
	PreferenceSets        []PreferenceSet

	systemMaps map[int]*av.VideoMap

	weatherRadar WeatherRadar

	systemFont        [6]*renderer.Font
	systemOutlineFont [6]*renderer.Font
	dcbFont           [3]*renderer.Font // 0, 1, 2 only
	cursorsFont       *renderer.Font

	fusedTrackVertices [][2]float32

	events *sim.EventsSubscription

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	Aircraft map[string]*AircraftState

	AircraftToIndex   map[string]int     // for use in lists
	IndexToAircraft   map[int]string     // map is sort of wasteful since it's dense, but...
	UnsupportedTracks map[av.Squawk]bool // visible or not

	// explicit JSON name to avoid errors during config deserialization for
	// backwards compatibility, since this used to be a
	// map[string]interface{}.
	AutoTrackDepartures bool `json:"autotrack_departures"`
	LockDisplay         bool
	AirspaceAwareness   struct {
		Interfacility bool
		Intrafacility bool
	}

	// callsign -> controller id
	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string
	RejectedPointOuts map[string]interface{}
	ForceQLAircraft   []string

	queryUnassociated *util.TransientMap[string, interface{}]

	RangeBearingLines []STARSRangeBearingLine
	MinSepAircraft    [2]string

	CAAircraft []CAAircraft

	// For CRDA
	ConvergingRunways []STARSConvergingRunways

	// Various UI state
	scopeClickHandler   func(pw [2]float32, transforms ScopeTransformations) CommandStatus
	activeDCBMenu       int
	selectedPlaceButton string

	dwellAircraft     string
	drawRouteAircraft string

	commandMode       CommandMode
	multiFuncPrefix   string
	previewAreaOutput string
	previewAreaInput  string

	lastTrackUpdate        time.Time
	lastHistoryTrackUpdate time.Time
	discardTracks          bool

	drawApproachAirspace  bool
	drawDepartureAirspace bool

	// The start of a RBL--one click received, waiting for the second.
	wipRBL *STARSRangeBearingLine

	audioEffects map[AudioType]int // to handle from Platform.AddPCM()
}

func init() {
	panes.RegisterUnmarshalPane("STARSPane", func(d []byte) (panes.Pane, error) {
		var p STARSPane
		err := json.Unmarshal(d, &p)
		return &p, err
	})
}

type AudioType int

// The types of events we may play audio for.
const (
	AudioConflictAlert = iota
	AudioSquawkSPC
	AudioMinimumSafeAltitudeWarning
	AudioModeCIntruder
	AudioTest
	AudioInboundHandoff
	AudioCommandError
	AudioHandoffAccepted
	AudioNumTypes
)

func (ae AudioType) String() string {
	return [...]string{
		"Conflict Alert",
		"Emergency Squawk Code",
		"Minimum Safe Altitude Warning",
		"Mode C Intruder",
		"Test",
		"Inbound Handoff",
		"Command Error",
		"Handoff Accepted",
	}[ae]
}

type CAAircraft struct {
	Callsigns    [2]string // sorted alphabetically
	Acknowledged bool
	SoundEnd     time.Time
}

type CRDAMode int

const (
	CRDAModeStagger = iota
	CRDAModeTie
)

// this is read-only, stored in STARSPane for convenience
type STARSConvergingRunways struct {
	av.ConvergingRunways
	ApproachRegions [2]*av.ApproachRegion
	Airport         string
	Index           int
}

type CRDARunwayState struct {
	Enabled                 bool
	LeaderLineDirection     *math.CardinalOrdinalDirection // nil -> unset
	DrawCourseLines         bool
	DrawQualificationRegion bool
}

// stores the per-preference set state for each STARSConvergingRunways
type CRDARunwayPairState struct {
	Enabled     bool
	Mode        CRDAMode
	RunwayState [2]CRDARunwayState
}

func (c *STARSConvergingRunways) getRunwaysString() string {
	return c.Runways[0] + "/" + c.Runways[1]
}

type VideoMapsGroup int

const (
	VideoMapsGroupGeo = iota
	VideoMapsGroupSysProc
	VideoMapsGroupCurrent
)

type DwellMode int

const (
	// Make 0 be "on" so zero-initialization gives "on"
	DwellModeOn = iota
	DwellModeLock
	DwellModeOff
)

func (d DwellMode) String() string {
	switch d {
	case DwellModeOn:
		return "ON"

	case DwellModeLock:
		return "LOCK"

	case DwellModeOff:
		return "OFF"

	default:
		return "unhandled DwellMode"
	}
}

type STARSBrightness int

func (b STARSBrightness) RGB() renderer.RGB {
	v := float32(b) / 100
	return renderer.RGB{v, v, v}
}

func (b STARSBrightness) ScaleRGB(r renderer.RGB) renderer.RGB {
	return r.Scale(float32(b) / 100)
}

///////////////////////////////////////////////////////////////////////////
// STARSPane proper

func NewSTARSPane(ss *sim.State) *STARSPane {
	sp := &STARSPane{
		SelectedPreferenceSet: -1,
	}
	sp.CurrentPreferenceSet = sp.MakePreferenceSet("", ss)
	return sp
}

func (sp *STARSPane) DisplayName() string { return "STARS" }

func (sp *STARSPane) Hide() bool { return false }

func (sp *STARSPane) Activate(ss *sim.State, r renderer.Renderer, p platform.Platform,
	eventStream *sim.EventStream, lg *log.Logger) {
	if sp.CurrentPreferenceSet.Range == 0 || sp.CurrentPreferenceSet.Center.IsZero() {
		// First launch after switching over to serializing the CurrentPreferenceSet...
		sp.CurrentPreferenceSet = sp.MakePreferenceSet("", ss)
	}
	sp.CurrentPreferenceSet.Activate(p, sp)

	if sp.InboundPointOuts == nil {
		sp.InboundPointOuts = make(map[string]string)
	}
	if sp.OutboundPointOuts == nil {
		sp.OutboundPointOuts = make(map[string]string)
	}
	if sp.RejectedPointOuts == nil {
		sp.RejectedPointOuts = make(map[string]interface{})
	}
	if sp.queryUnassociated == nil {
		sp.queryUnassociated = util.NewTransientMap[string, interface{}]()
	}

	sp.initializeFonts(r, p)
	sp.initializeAudio(p, lg)

	if ss != nil {
		sp.systemMaps = sp.makeSystemMaps(*ss)
	}

	if sp.Aircraft == nil {
		sp.Aircraft = make(map[string]*AircraftState)
	}

	if sp.AircraftToIndex == nil {
		sp.AircraftToIndex = make(map[string]int)
	}
	if sp.IndexToAircraft == nil {
		sp.IndexToAircraft = make(map[int]string)
	}
	if sp.UnsupportedTracks == nil {
		sp.UnsupportedTracks = make(map[av.Squawk]bool)
	}

	sp.events = eventStream.Subscribe()

	ps := sp.CurrentPreferenceSet
	if ps.Brightness.Weather != 0 {
		sp.weatherRadar.Activate(ps.Center, r, lg)
	}

	sp.lastTrackUpdate = time.Time{} // force immediate update at start
	sp.lastHistoryTrackUpdate = time.Time{}
}

func (sp *STARSPane) Deactivate() {
	// Drop all of them
	sp.Aircraft = nil

	sp.events.Unsubscribe()
	sp.events = nil

	sp.weatherRadar.Deactivate()
}

func (sp *STARSPane) Reset(ss sim.State, lg *log.Logger) {
	ps := &sp.CurrentPreferenceSet

	ps.Center = ss.GetInitialCenter()
	ps.Range = ss.GetInitialRange()
	ps.CurrentCenter = ps.Center
	ps.RangeRingsCenter = ps.Center

	videoMaps, defaultVideoMaps := ss.GetVideoMaps()
	clear(ps.DisplayVideoMap[:])
	// Make the scenario's default video maps visible
	for _, dm := range defaultVideoMaps {
		if idx := slices.IndexFunc(videoMaps, func(m av.VideoMap) bool { return m.Name == dm }); idx != -1 {
			ps.DisplayVideoMap[idx] = true
		} else {
			lg.Errorf("%s: \"default_map\" not found in \"stars_maps\"", dm)
		}
	}
	ps.SystemMapVisible = make(map[int]interface{})

	sp.systemMaps = sp.makeSystemMaps(ss)

	ps.CurrentATIS = ""
	for i := range ps.GIText {
		ps.GIText[i] = ""
	}
	ps.RadarSiteSelected = ""

	sp.ConvergingRunways = nil
	for _, name := range util.SortedMapKeys(ss.Airports) {
		ap := ss.Airports[name]
		for idx, pair := range ap.ConvergingRunways {
			sp.ConvergingRunways = append(sp.ConvergingRunways, STARSConvergingRunways{
				ConvergingRunways: pair,
				ApproachRegions: [2]*av.ApproachRegion{ap.ApproachRegions[pair.Runways[0]],
					ap.ApproachRegions[pair.Runways[1]]},
				Airport: name[1:], // drop the leading "K"
				Index:   idx + 1,  // 1-based
			})
		}
	}

	ps.ResetCRDAState(sp.ConvergingRunways)
	for i := range sp.PreferenceSets {
		sp.PreferenceSets[i].ResetCRDAState(sp.ConvergingRunways)
	}

	sp.lastTrackUpdate = time.Time{} // force update
	sp.lastHistoryTrackUpdate = time.Time{}
}

func (sp *STARSPane) makeSystemMaps(ss sim.State) map[int]*av.VideoMap {
	maps := make(map[int]*av.VideoMap)

	// CA suppression filters
	csf := &av.VideoMap{
		Label: "ALLCASU",
		Name:  "ALL CA SUPPRESSION FILTERS",
	}
	for _, vol := range ss.InhibitCAVolumes() {
		vol.GenerateDrawCommands(&csf.CommandBuffer, ss.NmPerLongitude)
	}
	maps[700] = csf

	// MVAs
	mvas := &av.VideoMap{
		Label: ss.TRACON + " MVA",
		Name:  "ALL MINIMUM VECTORING ALTITUDES",
	}
	ld := renderer.GetLinesDrawBuilder()
	for _, mva := range av.DB.MVAs[ss.TRACON] {
		ld.AddLineLoop(mva.ExteriorRing)
		p := math.Extent2DFromPoints(mva.ExteriorRing).Center()
		ld.AddNumber(p, 0.005, fmt.Sprintf("%d", mva.MinimumLimit/100))
	}
	ld.GenerateCommands(&mvas.CommandBuffer)
	renderer.ReturnLinesDrawBuilder(ld)
	maps[701] = mvas

	// Radar maps
	radarIndex := 801
	for _, name := range util.SortedMapKeys(ss.RadarSites) {
		sm := &av.VideoMap{
			Label: name + "RCM",
			Name:  name + " RADAR COVERAGE MAP",
		}

		site := ss.RadarSites[name]
		ld := renderer.GetLinesDrawBuilder()
		ld.AddLatLongCircle(site.Position, ss.NmPerLongitude, float32(site.PrimaryRange), 360)
		ld.AddLatLongCircle(site.Position, ss.NmPerLongitude, float32(site.SecondaryRange), 360)
		ld.GenerateCommands(&sm.CommandBuffer)
		maps[radarIndex] = sm

		radarIndex++
		renderer.ReturnLinesDrawBuilder(ld)
	}

	// ATPA approach volumes
	atpaIndex := 901
	for _, name := range util.SortedMapKeys(ss.ArrivalAirports) {
		ap := ss.ArrivalAirports[name]
		for _, rwy := range util.SortedMapKeys(ap.ATPAVolumes) {
			vol := ap.ATPAVolumes[rwy]

			sm := &av.VideoMap{
				Label: name + rwy + " VOL",
				Name:  name + rwy + " ATPA APPROACH VOLUME",
			}

			ld := renderer.GetLinesDrawBuilder()
			rect := vol.GetRect(ss.NmPerLongitude, ss.MagneticVariation)
			for i := range rect {
				ld.AddLine(rect[i], rect[(i+1)%len(rect)])
			}
			ld.GenerateCommands(&sm.CommandBuffer)

			maps[atpaIndex] = sm
			atpaIndex++
			renderer.ReturnLinesDrawBuilder(ld)
		}
	}

	return maps
}

func (sp *STARSPane) DrawUI(p platform.Platform, config *platform.Config) {
	ps := &sp.CurrentPreferenceSet

	imgui.Checkbox("Auto track departures", &sp.AutoTrackDepartures)

	imgui.Checkbox("Lock display", &sp.LockDisplay)

	imgui.Checkbox("Enable Additional Sound Effects", &config.AudioEnabled)

	if !config.AudioEnabled {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}

	// Only offer the non-standard ones to globally disable.
	for _, i := range []AudioType{AudioInboundHandoff, AudioHandoffAccepted} {
		imgui.Text("  ")
		imgui.SameLine()
		if imgui.Checkbox(AudioType(i).String(), &ps.AudioEffectEnabled[i]) && ps.AudioEffectEnabled[i] {
			sp.playOnce(p, i)
		}
	}

	if !config.AudioEnabled {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}

func (sp *STARSPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STARSPane) Upgrade(from, to int) {
	sp.CurrentPreferenceSet.Upgrade(from, to)
	for i := range sp.PreferenceSets {
		sp.PreferenceSets[i].Upgrade(from, to)
	}
}

func (sp *STARSPane) Draw(ctx *panes.Context, cb *renderer.CommandBuffer) {
	sp.processEvents(ctx)
	sp.updateRadarTracks(ctx)

	ps := sp.CurrentPreferenceSet

	// Clear to background color
	cb.ClearRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))

	sp.processKeyboardInput(ctx)

	transforms := GetScopeTransformations(ctx.PaneExtent, ctx.ControlClient.MagneticVariation, ctx.ControlClient.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range), 0)

	scopeExtent := ctx.PaneExtent
	if ps.DisplayDCB {
		scopeExtent = sp.DrawDCB(ctx, transforms, cb)

		// Update scissor for what's left and to protect the DCB (even
		// though this is apparently unrealistic, at least as far as radar
		// tracks go...)
		cb.SetScissorBounds(scopeExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	}

	weatherBrightness := float32(ps.Brightness.Weather) / float32(100)
	weatherContrast := float32(ps.Brightness.WxContrast) / float32(100)
	sp.weatherRadar.Draw(ctx, weatherBrightness, weatherContrast, ps.DisplayWeatherLevel,
		transforms, cb)

	if ps.Brightness.RangeRings > 0 {
		color := ps.Brightness.RangeRings.ScaleRGB(STARSRangeRingColor)
		cb.LineWidth(1, ctx.DrawPixelScale)
		DrawRangeRings(ctx, ps.RangeRingsCenter, float32(ps.RangeRingRadius), color, transforms, cb)
	}

	transforms.LoadWindowViewingMatrices(cb)

	// Maps
	cb.LineWidth(1, ctx.DrawPixelScale)
	videoMaps, _ := ctx.ControlClient.GetVideoMaps()
	for i, disp := range ps.DisplayVideoMap {
		if !disp {
			continue
		}

		vmap := videoMaps[i]
		color := ps.Brightness.VideoGroupA.ScaleRGB(STARSMapColor)
		if vmap.Group == 1 {
			color = ps.Brightness.VideoGroupB.ScaleRGB(STARSMapColor)
		}
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(vmap.CommandBuffer)
	}

	for _, idx := range util.SortedMapKeys(ps.SystemMapVisible) {
		color := ps.Brightness.VideoGroupA.ScaleRGB(STARSMapColor)
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(sp.systemMaps[idx].CommandBuffer)
	}

	sp.drawScenarioRoutes(ctx, transforms, sp.systemFont[ps.CharSize.Tools],
		ps.Brightness.Lists.ScaleRGB(STARSListColor), cb)

	sp.drawCRDARegions(ctx, transforms, cb)
	sp.drawSelectedRoute(ctx, transforms, cb)

	transforms.LoadWindowViewingMatrices(cb)

	if ps.Brightness.Compass > 0 {
		cb.LineWidth(1, ctx.DrawPixelScale)
		cbright := ps.Brightness.Compass.ScaleRGB(STARSCompassColor)
		font := sp.systemFont[ps.CharSize.Tools]
		DrawCompass(ps.CurrentCenter, ctx, 0, font, cbright, scopeExtent, transforms, cb)
	}

	// Per-aircraft stuff: tracks, datablocks, vector lines, range rings, ...
	// Sort the aircraft so that they are always drawn in the same order
	// (go's map iterator randomization otherwise randomizes the order,
	// which can cause shimmering when datablocks overlap (especially if
	// one is selected). We'll go with alphabetical by callsign, with the
	// selected aircraft, if any, always drawn last.
	aircraft := sp.visibleAircraft(ctx)
	sort.Slice(aircraft, func(i, j int) bool {
		return aircraft[i].Callsign < aircraft[j].Callsign
	})

	sp.drawSystemLists(aircraft, ctx, ctx.PaneExtent, transforms, cb)

	sp.drawHistoryTrails(aircraft, ctx, transforms, cb)

	sp.drawPTLs(aircraft, ctx, transforms, cb)
	sp.drawRingsAndCones(aircraft, ctx, transforms, cb)
	sp.drawRBLs(aircraft, ctx, transforms, cb)
	sp.drawMinSep(ctx, transforms, cb)
	sp.drawAirspace(ctx, transforms, cb)

	DrawHighlighted(ctx, transforms, cb)

	sp.drawLeaderLines(aircraft, ctx, transforms, cb)
	sp.drawTracks(aircraft, ctx, transforms, cb)
	sp.drawDatablocks(aircraft, ctx, transforms, cb)

	ghosts := sp.getGhostAircraft(aircraft, ctx)
	sp.drawGhosts(ghosts, ctx, transforms, cb)
	sp.consumeMouseEvents(ctx, ghosts, transforms, cb)
	if ctx.Mouse != nil {
		sp.drawMouseCursor(ctx, scopeExtent, transforms, cb)
	}

	sp.updateAudio(ctx, aircraft)

	// Do this at the end of drawing so that we hold on to the tracks we
	// have for rendering the current frame.
	if sp.discardTracks {
		for _, state := range sp.Aircraft {
			state.historyTracksIndex = 0
		}
		sp.lastTrackUpdate = time.Time{} // force update
		sp.lastHistoryTrackUpdate = time.Time{}
		sp.discardTracks = false
	}
}

func (sp *STARSPane) drawCRDARegions(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	transforms.LoadLatLongViewingMatrices(cb)

	ps := sp.CurrentPreferenceSet
	for i, state := range ps.CRDA.RunwayPairState {
		for j, rwyState := range state.RunwayState {
			if rwyState.DrawCourseLines {
				region := sp.ConvergingRunways[i].ApproachRegions[j]
				line, _ := region.GetLateralGeometry(ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)

				ld := renderer.GetLinesDrawBuilder()
				cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor))
				ld.AddLine(line[0], line[1])

				ld.GenerateCommands(cb)
				renderer.ReturnLinesDrawBuilder(ld)
			}

			if rwyState.DrawQualificationRegion {
				region := sp.ConvergingRunways[i].ApproachRegions[j]
				_, quad := region.GetLateralGeometry(ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)

				ld := renderer.GetLinesDrawBuilder()
				cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor))
				ld.AddLineLoop([][2]float32{quad[0], quad[1], quad[2], quad[3]})

				ld.GenerateCommands(cb)
				renderer.ReturnLinesDrawBuilder(ld)
			}
		}
	}
}

func (sp *STARSPane) drawMouseCursor(ctx *panes.Context, scopeExtent math.Extent2D, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	// Is the mouse over the DCB or over the regular STARS scope? Note that
	// we need to offset the mouse position to be w.r.t. window coordinates
	// to match scopeExtent.
	mouseOverDCB := !scopeExtent.Inside(math.Add2f(ctx.Mouse.Pos, ctx.PaneExtent.P0))
	if mouseOverDCB {
		return
	}

	ctx.Mouse.SetCursor(imgui.MouseCursorNone)

	// STARS Operators Manual 4-74: FDB brightness is used for the cursor
	ps := sp.CurrentPreferenceSet
	cursorStyle := renderer.TextStyle{Font: sp.cursorsFont, Color: ps.Brightness.FullDatablocks.RGB()}
	background := ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor)
	bgStyle := renderer.TextStyle{Font: sp.cursorsFont, Color: background}

	draw := func(idx int, style renderer.TextStyle) {
		g := sp.cursorsFont.LookupGlyph(rune(idx))
		p := math.Add2f(ctx.Mouse.Pos, [2]float32{-g.Width() / 2, g.Height() / 2})
		td.AddText(string(byte(idx)), p, style)
	}
	// The STARS "+" cursors start at 0 in the STARS cursors font,
	// ordered by size. There is no cursor for size 5, so we'll use 4 for that.
	// The second of the two is the background one
	// that establishes a mask.
	idx := 2 * min(4, ps.CharSize.Datablocks)
	draw(idx+1, bgStyle)
	draw(idx, cursorStyle)

	cb.SetDrawBounds(ctx.PaneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) initializeFonts(r renderer.Renderer, p platform.Platform) {
	fonts := createFontAtlas(r, p)
	get := func(name string, size int) *renderer.Font {
		idx := slices.IndexFunc(fonts, func(f *renderer.Font) bool { return f.Id.Name == name && f.Id.Size == size })
		if idx == -1 {
			panic(name + " size " + strconv.Itoa(size) + " not found in STARS fonts")
		}
		return fonts[idx]
	}

	sp.systemFont[0] = get("sddCharFontSetBSize0", 11)
	sp.systemFont[1] = get("sddCharFontSetBSize1", 12)
	sp.systemFont[2] = get("sddCharFontSetBSize2", 15)
	sp.systemFont[3] = get("sddCharFontSetBSize3", 16)
	sp.systemFont[4] = get("sddCharFontSetBSize4", 18)
	sp.systemFont[5] = get("sddCharFontSetBSize5", 19)
	sp.systemOutlineFont[0] = get("sddCharOutlineFontSetBSize0", 11)
	sp.systemOutlineFont[1] = get("sddCharOutlineFontSetBSize1", 12)
	sp.systemOutlineFont[2] = get("sddCharOutlineFontSetBSize2", 15)
	sp.systemOutlineFont[3] = get("sddCharOutlineFontSetBSize3", 16)
	sp.systemOutlineFont[4] = get("sddCharOutlineFontSetBSize4", 18)
	sp.systemOutlineFont[5] = get("sddCharOutlineFontSetBSize5", 19)
	sp.dcbFont[0] = get("sddCharFontSetBSize0", 11)
	sp.dcbFont[1] = get("sddCharFontSetBSize1", 12)
	sp.dcbFont[2] = get("sddCharFontSetBSize2", 15)
	sp.cursorsFont = get("STARS cursors", 30)
}

const (
	RadarModeSingle = iota
	RadarModeMulti
	RadarModeFused
)

func (sp *STARSPane) radarMode(radarSites map[string]*av.RadarSite) int {
	if len(radarSites) == 0 {
		// Straight-up fused mode if none are specified.
		return RadarModeFused
	}

	ps := sp.CurrentPreferenceSet
	if _, ok := radarSites[ps.RadarSiteSelected]; ps.RadarSiteSelected != "" && ok {
		return RadarModeSingle
	} else if ps.FusedRadarMode {
		return RadarModeFused
	} else {
		return RadarModeMulti
	}
}

func (sp *STARSPane) visibleAircraft(ctx *panes.Context) []*av.Aircraft {
	var aircraft []*av.Aircraft
	ps := sp.CurrentPreferenceSet
	single := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeSingle
	now := ctx.ControlClient.SimTime
	for callsign, state := range sp.Aircraft {
		ac, ok := ctx.ControlClient.Aircraft[callsign]
		if !ok {
			continue
		}
		// This includes the case of a spawned aircraft for which we don't
		// yet have a radar track.
		if state.LostTrack(now) {
			continue
		}

		visible := false

		if sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused {
			// visible unless if it's almost on the ground
			alt := float32(state.TrackAltitude())
			visible = (ac.IsDeparture() && alt > ac.DepartureAirportElevation()+100) ||
				(!ac.IsDeparture() && alt > ac.ArrivalAirportElevation()+100)
		} else {
			// Otherwise see if any of the radars can see it
			for id, site := range ctx.ControlClient.RadarSites {
				if single && ps.RadarSiteSelected != id {
					continue
				}

				if p, s, _ := site.CheckVisibility(state.TrackPosition(), state.TrackAltitude()); p || s {
					visible = true
				}
			}
		}

		if visible {
			aircraft = append(aircraft, ac)

			// Is this the first we've seen it?
			if state.FirstRadarTrack.IsZero() {
				state.FirstRadarTrack = now

				trk := sp.getTrack(ctx, ac)
				if sp.AutoTrackDepartures && trk != nil && trk.TrackOwner == "" &&
					ctx.ControlClient.DepartureController(ac, ctx.Lg) == ctx.ControlClient.Callsign {
					starsFP := sim.MakeSTARSFlightPlan(ac.FlightPlan)
					ctx.ControlClient.InitiateTrack(callsign, starsFP, nil, nil) // ignore error...
				}
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) radarSiteId(radarSites map[string]*av.RadarSite) string {
	switch sp.radarMode(radarSites) {
	case RadarModeSingle:
		return sp.CurrentPreferenceSet.RadarSiteSelected
	case RadarModeMulti:
		return "MULTI"
	case RadarModeFused:
		return "FUSED"
	default:
		return "UNKNOWN"
	}
}

func (sp *STARSPane) initializeAudio(p platform.Platform, lg *log.Logger) {
	if sp.audioEffects == nil {
		sp.audioEffects = make(map[AudioType]int)

		loadMP3 := func(filename string) int {
			dec, pcm, err := minimp3.DecodeFull(util.LoadResource("audio/" + filename))
			if err != nil {
				lg.Errorf("%s: unable to decode mp3: %v", filename, err)
			}
			if dec.Channels != 1 {
				lg.Errorf("expected 1 channel, got %d", dec.Channels)
			}

			idx, err := p.AddPCM(pcm, dec.SampleRate)
			if err != nil {
				lg.Errorf("%s: %v", filename, err)
			}
			return idx
		}

		sp.audioEffects[AudioConflictAlert] = loadMP3("CA_1000ms.mp3")
		sp.audioEffects[AudioSquawkSPC] = loadMP3("SPC_700ms.mp3")
		sp.audioEffects[AudioMinimumSafeAltitudeWarning] = loadMP3("MSAW_1000ms.mp3")
		sp.audioEffects[AudioModeCIntruder] = loadMP3("MCI_1000ms.mp3")
		sp.audioEffects[AudioTest] = loadMP3("TEST_250ms.mp3")
		sp.audioEffects[AudioInboundHandoff] = loadMP3("263124__pan14__sine-octaves-up-beep.mp3")
		sp.audioEffects[AudioCommandError] = loadMP3("ERROR.mp3")
		sp.audioEffects[AudioHandoffAccepted] = loadMP3("321104__nsstudios__blip2.mp3")
	}
}

func (sp *STARSPane) playOnce(p platform.Platform, a AudioType) {
	if sp.CurrentPreferenceSet.AudioEffectEnabled[a] {
		p.PlayAudioOnce(sp.audioEffects[a])
	}
}

const AlertAudioDuration = 5 * time.Second

func (sp *STARSPane) updateAudio(ctx *panes.Context, aircraft []*av.Aircraft) {
	ps := &sp.CurrentPreferenceSet

	updateContinuous := func(play bool, effect AudioType) {
		if ps.AudioEffectEnabled[effect] && play {
			ctx.Platform.StartPlayAudioContinuous(sp.audioEffects[effect])
		} else {
			ctx.Platform.StopPlayAudioContinuous(sp.audioEffects[effect])
		}
	}

	// Play the CA sound if any CAs or MSAWs are unacknowledged
	playCASound := !ps.DisableCAWarnings && slices.ContainsFunc(sp.CAAircraft,
		func(ca CAAircraft) bool {
			return !ca.Acknowledged && !sp.Aircraft[ca.Callsigns[0]].DisableCAWarnings &&
				!sp.Aircraft[ca.Callsigns[1]].DisableCAWarnings && ctx.Now.Before(ca.SoundEnd)
		})
	updateContinuous(playCASound, AudioConflictAlert)

	playMSAWSound := !ps.DisableMSAW && func() bool {
		for _, ac := range aircraft {
			state := sp.Aircraft[ac.Callsign]
			if state.MSAW && !state.MSAWAcknowledged && !state.InhibitMSAW && !state.DisableMSAW &&
				ctx.Now.Before(state.MSAWSoundEnd) {
				return true
			}
		}
		return false
	}()
	updateContinuous(playMSAWSound, AudioMinimumSafeAltitudeWarning)

	// 2-100: play sound if:
	// - There is an unacknowledged SPC in a track's datablock
	// - [todo]: track is unassociated or is associated and was displaying FDB
	// - [todo]: if unassociated, is on-screen or within an adapted distance
	playSPCSound := func() bool {
		for _, ac := range aircraft {
			state := sp.Aircraft[ac.Callsign]
			ok, _ := av.SquawkIsSPC(ac.Squawk)
			if (ok || len(ac.SPCOverrides) > 0) && !state.SPCAcknowledged &&
				ctx.Now.Before(state.SPCSoundEnd) {
				return true
			}
		}
		return false
	}()
	updateContinuous(playSPCSound, AudioSquawkSPC)
}
