// stars.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Main missing features:
// Altitude alerts
// CDRA (can build off of CRDAConfig, however)
// Quicklook

package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

var (
	STARSBackgroundColor         = RGB{0, 0, 0}
	STARSListColor               = RGB{.1, .9, .1}
	STARSTextAlertColor          = RGB{1, .1, .1}
	STARSTrackBlockColor         = RGB{0.1, 0.4, 1}
	STARSTrackHistoryColor       = RGB{.2, 0, 1}
	STARSJRingConeColor          = RGB{.5, .5, 1}
	STARSTrackedAircraftColor    = RGB{1, 1, 1}
	STARSUntrackedAircraftColor  = RGB{.1, .9, .1}
	STARSPointedOutAircraftColor = RGB{.9, .9, .1}
	STARSSelectedAircraftColor   = RGB{.1, .9, .9}

	ErrSTARSIllegalParam  = errors.New("ILL PARAM")
	ErrSTARSIllegalTrack  = errors.New("ILL TRK")
	ErrSTARSCommandFormat = errors.New("FORMAT")
)

const NumSTARSPreferenceSets = 32
const NumSTARSMaps = 28

type STARSPane struct {
	CurrentPreferenceSet  STARSPreferenceSet
	SelectedPreferenceSet int
	PreferenceSets        []STARSPreferenceSet

	Facility STARSFacility

	weatherRadar WeatherRadar

	systemFont [6]*Font

	eventsId EventSubscriberId

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	aircraft map[*Aircraft]*STARSAircraftState
	// map from legit to their ghost, if present
	ghostAircraft map[*Aircraft]*Aircraft

	aircraftToIndex map[*Aircraft]int // for use in lists
	indexToAircraft map[int]*Aircraft // map is sort of wasteful since it's dense, but...

	AutoTrackDepartures map[string]interface{}

	pointedOutAircraft *TransientMap[*Aircraft, string]
	queryUnassociated  *TransientMap[*Aircraft, interface{}]

	rangeBearingLines []STARSRangeBearingLine
	minSepAircraft    [2]*Aircraft

	// Various UI state
	scopeClickHandler func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus
	activeDCBMenu     int

	commandMode       CommandMode
	multiFuncPrefix   string
	previewAreaOutput string
	previewAreaInput  string

	havePlayedSPCAlertSound map[*Aircraft]interface{}

	lastCASoundTime time.Time

	drawApproachAirspace  bool
	drawDepartureAirspace bool
}

type STARSRangeBearingLine struct {
	p [2]struct {
		// If ac is non-nil, use its position, otherwise we have a fixed position.
		loc Point2LL
		ac  *Aircraft
	}
}

type CommandMode int

const (
	CommandModeNone = iota
	CommandModeInitiateControl
	CommandModeTerminateControl
	CommandModeHandOff
	CommandModeVP
	CommandModeMultiFunc
	CommandModeFlightData
	CommandModeCollisionAlert
	CommandModeMin
	CommandModeSavePrefAs
	CommandModeMaps
	CommandModeLDR
	CommandModeRangeRings
	CommandModeRange
	CommandModeSiteMenu
)

const (
	DCBMenuMain = iota
	DCBMenuAux
	DCBMenuMaps
	DCBMenuBrite
	DCBMenuCharSize
	DCBMenuPref
	DCBMenuSite
	DCBMenuSSAFilter
	DCBMenuGITextFilter
)

type STARSAircraftState struct {
	isGhost       bool
	suppressGhost bool // for ghost only

	forceGhost bool // for non-ghost only

	datablockType DatablockType

	datablockErrText    string
	datablockText       [2][]string
	datablockDrawOffset [2]float32

	isSelected bool // middle click

	// Only drawn if non-zero
	jRingRadius    float32
	coneLength     float32
	displayTPASize bool // flip this so that zero-init works here? (What is the default?)

	leaderLineDirection *CardinalOrdinalDirection // nil -> unset

	displayPilotAltitude bool
	pilotAltitude        int

	displayReportedBeacon bool // note: only for unassociated
	displayPTL            bool
	disableCAWarnings     bool
	disableMSAW           bool
	inhibitMSAWAlert      bool // only applies if in an alert. clear when alert is over?

	spcOverride string

	outboundHandoffAccepted bool
	outboundHandoffFlashEnd time.Time
}

///////////////////////////////////////////////////////////////////////////
// STARSFacility and related

type STARSFacility struct {
	CA struct {
		LateralMinimum  float32
		VerticalMinimum int32
		Floor           int32
	}
	CRDAConfig CRDAConfig

	// TODO: transition alt -> show pressure altitude above
	// TODO: RNAV patterns
	// TODO: automatic scratchpad stuff
}

type STARSMap struct {
	Label string `json:"label"`
	Group int    `json:"group"` // 0 -> A, 1 -> B
	Name  string `json:"name"`
	cb    CommandBuffer
}

func MakeDefaultFacility() STARSFacility {
	var f STARSFacility

	f.CA.LateralMinimum = 3
	f.CA.VerticalMinimum = 1000
	f.CA.Floor = 500
	f.CRDAConfig = NewCRDAConfig()

	return f
}

///////////////////////////////////////////////////////////////////////////
// STARSPreferenceSet

type STARSPreferenceSet struct {
	Name string

	DisplayDCB bool

	Center Point2LL
	Range  float32

	CurrentCenter Point2LL
	OffCenter     bool

	RangeRingsCenter Point2LL
	RangeRingRadius  int

	// TODO? cursor speed

	CurrentATIS string
	GIText      [9]string

	RadarTrackHistory int

	WeatherIntensity [6]bool

	// If empty, then then MULTI mode.  The custom JSON name is so we don't
	// get errors parsing old configs, which stored this as an array...
	RadarSiteSelected string `json:"RadarSiteSelectedName"`

	LeaderLineDirection CardinalOrdinalDirection
	LeaderLineLength    int // 0-7

	ListSelectedMaps bool // TODO: show this list, e.g.:
	// CURRENT MAPS
	// >  2 EAST   PHILADELPHIA MAP EAST
	// >  ...

	AltitudeFilters struct {
		Unassociated [2]int // low, high
		Associated   [2]int
	}

	DisableCRDA bool

	DisplayLDBBeaconCodes bool // TODO: default?
	SelectedBeaconCodes   []string

	// TODO: review--should some of the below not be in prefs but be in STARSPane?

	DisplayUncorrelatedTargets bool

	DisableCAWarnings bool
	DisableMSAW       bool

	OverflightFullDatablocks bool
	AutomaticFDBOffset       bool

	DisplayTPASize bool

	VideoMapVisible map[string]interface{}

	PTLLength      float32
	PTLOwn, PTLAll bool

	TopDownMode     bool
	GroundRangeMode bool

	Bookmarks [10]struct {
		Center      Point2LL
		Range       float32
		TopDownMode bool
	}

	Brightness struct {
		VideoGroupA       STARSBrightness
		VideoGroupB       STARSBrightness
		FullDatablocks    STARSBrightness
		Lists             STARSBrightness
		Positions         STARSBrightness
		LimitedDatablocks STARSBrightness
		OtherTracks       STARSBrightness
		Lines             STARSBrightness
		RangeRings        STARSBrightness
		Compass           STARSBrightness
		BeaconSymbols     STARSBrightness
		PrimarySymbols    STARSBrightness
		History           STARSBrightness
	}

	CharSize struct {
		Datablocks      int
		Lists           int
		Tools           int
		PositionSymbols int
	}

	PreviewAreaPosition [2]float32

	SSAList struct {
		Position [2]float32
		Visible  bool
		Filter   struct {
			All                 bool
			Time                bool
			Altimeter           bool
			Status              bool
			Radar               bool
			Codes               bool
			SpecialPurposeCodes bool
			Range               bool
			PredictedTrackLines bool
			AltitudeFilters     bool
			AirportWeather      bool
			QuickLookPositions  bool
			DisabledTerminal    bool
			ActiveCRDAPairs     bool

			Text struct {
				Main bool
				GI   [9]bool
			}
		}
	}
	VFRList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	TABList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	AlertList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	CoastList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	SignOnList struct {
		Position [2]float32
		Visible  bool
	}
	VideoMapsList struct {
		Position [2]float32
		Visible  bool
	}
	CRDAStatusList struct {
		Position [2]float32
		Visible  bool
	}
	TowerLists [3]struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
}

func MakePreferenceSet(name string, facility STARSFacility) STARSPreferenceSet {
	var ps STARSPreferenceSet

	ps.Name = name

	ps.DisplayDCB = true

	ps.Center = scenarioGroup.Center
	ps.Range = scenarioGroup.Range

	ps.CurrentCenter = ps.Center

	ps.RangeRingsCenter = ps.Center
	ps.RangeRingRadius = 5

	ps.RadarTrackHistory = 5

	ps.VideoMapVisible = make(map[string]interface{})
	if len(scenarioGroup.STARSMaps) > 0 {
		ps.VideoMapVisible[scenarioGroup.STARSMaps[0].Name] = nil
	}
	ps.LeaderLineDirection = North
	ps.LeaderLineLength = 1

	ps.AltitudeFilters.Unassociated = [2]int{100, 60000}
	ps.AltitudeFilters.Associated = [2]int{100, 60000}

	ps.DisplayUncorrelatedTargets = true

	ps.DisplayTPASize = true

	ps.PTLLength = 1

	ps.Brightness.VideoGroupA = 50
	ps.Brightness.VideoGroupB = 40
	ps.Brightness.FullDatablocks = 80
	ps.Brightness.Lists = 80
	ps.Brightness.Positions = 80
	ps.Brightness.LimitedDatablocks = 80
	ps.Brightness.OtherTracks = 80
	ps.Brightness.Lines = 40
	ps.Brightness.RangeRings = 10
	ps.Brightness.Compass = 30
	ps.Brightness.BeaconSymbols = 55
	ps.Brightness.PrimarySymbols = 80
	ps.Brightness.History = 60

	ps.CharSize.Datablocks = 1
	ps.CharSize.Lists = 1
	ps.CharSize.Tools = 1
	ps.CharSize.PositionSymbols = 0

	ps.PreviewAreaPosition = [2]float32{.05, .8}

	ps.SSAList.Position = [2]float32{.05, .95}
	ps.SSAList.Visible = true
	ps.SSAList.Filter.All = true

	ps.TABList.Position = [2]float32{.05, .7}
	ps.TABList.Lines = 5
	ps.TABList.Visible = true

	ps.VFRList.Position = [2]float32{.05, .2}
	ps.VFRList.Lines = 5
	ps.VFRList.Visible = true

	ps.AlertList.Position = [2]float32{.85, .25}
	ps.AlertList.Lines = 5
	ps.AlertList.Visible = true

	ps.CoastList.Position = [2]float32{.85, .65}
	ps.CoastList.Lines = 5
	ps.CoastList.Visible = false

	ps.SignOnList.Position = [2]float32{.85, .95}
	ps.SignOnList.Visible = true

	ps.VideoMapsList.Position = [2]float32{.85, .5}
	ps.VideoMapsList.Visible = false

	ps.CRDAStatusList.Position = [2]float32{.05, .7}

	ps.TowerLists[0].Position = [2]float32{.05, .5}
	ps.TowerLists[0].Lines = 5
	ps.TowerLists[0].Visible = true

	ps.TowerLists[1].Position = [2]float32{.05, .8}
	ps.TowerLists[1].Lines = 5

	ps.TowerLists[2].Position = [2]float32{.05, .9}
	ps.TowerLists[2].Lines = 5

	return ps
}

func (ps *STARSPreferenceSet) Duplicate() STARSPreferenceSet {
	dupe := *ps
	dupe.SelectedBeaconCodes = DuplicateSlice(ps.SelectedBeaconCodes)
	dupe.VideoMapVisible = DuplicateMap(ps.VideoMapVisible)
	return dupe
}

func (ps *STARSPreferenceSet) Activate() {
	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[string]interface{})
		if len(scenarioGroup.STARSMaps) > 0 {
			ps.VideoMapVisible[scenarioGroup.STARSMaps[0].Name] = nil
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Utility types and methods

type DatablockType int

const (
	PartialDatablock = iota
	LimitedDatablock
	FullDatablock
)

func flightPlanSTARS(ac *Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalTrack // ??
	}

	// AAL1416 B738/L squawk controller id
	// (start of route) (alt 100s)
	result := ac.Callsign + " " + fp.AircraftType + " " + ac.AssignedSquawk.String() + " "
	if ctrl := sim.GetController(ac.TrackingController); ctrl != nil {
		result += ctrl.SectorId
	}
	result += "\n"

	// Display the first two items in the route
	routeFields := strings.Fields(fp.Route)
	if len(routeFields) > 2 {
		routeFields = routeFields[:2]
	}
	result += strings.Join(routeFields, " ") + " "

	result += fmt.Sprintf("%d", fp.Altitude/100)

	return result, nil
}

func squawkingSPC(squawk Squawk) bool {
	return squawk == Squawk(0o7500) || squawk == Squawk(0o7600) ||
		squawk == Squawk(0o7700) || squawk == Squawk(0o7777)
}

type STARSCommandStatus struct {
	clear  bool
	output string
	err    error
}

type STARSBrightness int

func (b STARSBrightness) RGB() RGB {
	v := float32(b) / 100
	return RGB{v, v, v}
}

func (b STARSBrightness) ScaleRGB(r RGB) RGB {
	return r.Scale(float32(b) / 100)
}

///////////////////////////////////////////////////////////////////////////
// STARSPane proper

// Takes aircraft position in window coordinates
func NewSTARSPane() *STARSPane {
	sp := &STARSPane{
		Facility:              MakeDefaultFacility(),
		SelectedPreferenceSet: -1,
	}
	sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility)
	return sp
}

func (sp *STARSPane) Name() string { return "STARS" }

func (sp *STARSPane) Activate() {
	if sp.CurrentPreferenceSet.Range == 0 || sp.CurrentPreferenceSet.Center.IsZero() {
		// First launch after switching over to serializing the CurrentPreferenceSet...
		sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility)
	}

	sp.CurrentPreferenceSet.Activate()

	if sp.havePlayedSPCAlertSound == nil {
		sp.havePlayedSPCAlertSound = make(map[*Aircraft]interface{})
	}
	if sp.pointedOutAircraft == nil {
		sp.pointedOutAircraft = NewTransientMap[*Aircraft, string]()
	}
	if sp.queryUnassociated == nil {
		sp.queryUnassociated = NewTransientMap[*Aircraft, interface{}]()
	}

	sp.initializeSystemFonts()

	sp.aircraftToIndex = make(map[*Aircraft]int)
	sp.indexToAircraft = make(map[int]*Aircraft)

	if sp.AutoTrackDepartures == nil {
		sp.AutoTrackDepartures = make(map[string]interface{})
	}

	sp.eventsId = eventStream.Subscribe()

	ps := sp.CurrentPreferenceSet
	if Find(ps.WeatherIntensity[:], true) != -1 {
		sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
	}

	// start tracking all of the active aircraft
	sp.initializeAircraft()
}

func (sp *STARSPane) Deactivate() {
	// Drop all of them
	sp.aircraft = nil
	sp.ghostAircraft = nil

	eventStream.Unsubscribe(sp.eventsId)
	sp.eventsId = InvalidEventSubscriberId

	sp.weatherRadar.Deactivate()
}

func (sp *STARSPane) ResetScenarioGroup() {
	ps := &sp.CurrentPreferenceSet

	ps.Center = scenarioGroup.Center
	ps.Range = scenarioGroup.Range
	ps.CurrentCenter = ps.Center
	ps.RangeRingsCenter = ps.Center

	ps.VideoMapVisible = make(map[string]interface{})
	if len(scenarioGroup.STARSMaps) > 0 {
		ps.VideoMapVisible[scenarioGroup.STARSMaps[0].Name] = nil
	}
}

func (sp *STARSPane) ResetScenario(s *Scenario) {
	// Make the scenario's default video map be visible
	ps := &sp.CurrentPreferenceSet
	ps.VideoMapVisible = make(map[string]interface{})
	ps.VideoMapVisible[s.DefaultMap] = nil

	ps.CurrentATIS = ""
	for i := range ps.GIText {
		ps.GIText[i] = ""
	}
	ps.RadarSiteSelected = ""
}

func (sp *STARSPane) DrawUI() {
	sp.AutoTrackDepartures, _ = drawAirportSelector(sp.AutoTrackDepartures, "Auto track departure airports")

	/*
		if newFont, changed := DrawFontPicker(&sp.LabelFontIdentifier, "Label font"); changed {
			sp.labelFont = newFont
		}
	*/

	if imgui.CollapsingHeader("Collision alerts") {
		imgui.SliderFloatV("Lateral minimum (nm)", &sp.Facility.CA.LateralMinimum, 0, 10, "%.1f", 0)
		imgui.InputIntV("Vertical minimum (feet)", &sp.Facility.CA.VerticalMinimum, 100, 100, 0)
		imgui.InputIntV("Altitude floor (feet)", &sp.Facility.CA.Floor, 100, 100, 0)
	}

	/*
		if imgui.CollapsingHeader("CRDA") {
			sp.Facility.CRDAConfig.DrawUI()
		}
	*/
}

func (sp *STARSPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STARSPane) processEvents(es *EventStream) {
	ps := sp.CurrentPreferenceSet

	for _, event := range es.Get(sp.eventsId) {
		switch v := event.(type) {
		case *AddedAircraftEvent:
			sa := &STARSAircraftState{}
			cs := sim.Callsign()
			if v.ac.TrackingController == cs || v.ac.ControllingController == cs {
				sa.datablockType = FullDatablock
			}
			sp.aircraft[v.ac] = sa

			if fp := v.ac.FlightPlan; fp != nil {
				if v.ac.TrackingController == "" {
					if _, ok := sp.AutoTrackDepartures[fp.DepartureAirport]; ok {
						sim.InitiateTrack(v.ac.Callsign) // ignore error...
						sp.aircraft[v.ac].datablockType = FullDatablock
					}
				}
			}

			if !ps.DisableCRDA {
				if ghost := sp.Facility.CRDAConfig.GetGhost(v.ac); ghost != nil {
					sp.ghostAircraft[v.ac] = ghost
					sp.aircraft[ghost] = &STARSAircraftState{
						// TODO: other defaults?
						isGhost:        true,
						displayTPASize: ps.DisplayTPASize,
					}
				}
			}
			if squawkingSPC(v.ac.Squawk) {
				if _, ok := sp.havePlayedSPCAlertSound[v.ac]; !ok {
					sp.havePlayedSPCAlertSound[v.ac] = nil
					//globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
				}
			}

		case *RemovedAircraftEvent:
			if ghost, ok := sp.ghostAircraft[v.ac]; ok {
				delete(sp.aircraft, ghost)
			}
			delete(sp.aircraft, v.ac)
			delete(sp.ghostAircraft, v.ac)

		case *ModifiedAircraftEvent:
			if squawkingSPC(v.ac.Squawk) {
				if _, ok := sp.havePlayedSPCAlertSound[v.ac]; !ok {
					sp.havePlayedSPCAlertSound[v.ac] = nil
					//globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
				}
			}

			if !ps.DisableCRDA {
				// always start out by removing the old ghost
				if oldGhost, ok := sp.ghostAircraft[v.ac]; ok {
					delete(sp.aircraft, oldGhost)
					delete(sp.ghostAircraft, v.ac)
				}
			}

			if _, ok := sp.aircraft[v.ac]; !ok {
				sp.aircraft[v.ac] = &STARSAircraftState{}
			}

			// new ghost
			if !ps.DisableCRDA {
				if ghost := sp.Facility.CRDAConfig.GetGhost(v.ac); ghost != nil {
					sp.ghostAircraft[v.ac] = ghost
					sp.aircraft[ghost] = &STARSAircraftState{isGhost: true}
				}
			}

		case *PointOutEvent:
			sp.pointedOutAircraft.Add(v.ac, v.controller, 10*time.Second)

		case *AcceptedHandoffEvent:
			// Note that we only want to do this if we were the handing-off
			// from controller, but that info isn't available to us
			// currently. For the purposes of vice/Sim, that's fine...
			if v.controller != sim.Callsign() {
				state := sp.aircraft[v.ac]
				state.outboundHandoffAccepted = true
				state.outboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
			}
		}
	}
}

func (sp *STARSPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	sp.processEvents(ctx.events)

	cb.ClearRGB(RGB{}) // clear to black, regardless of the color scheme

	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		wmTakeKeyboardFocus(sp, false)
	}
	sp.processKeyboardInput(ctx)

	transforms := GetScopeTransformations(ctx, sp.CurrentPreferenceSet.CurrentCenter,
		float32(sp.CurrentPreferenceSet.Range), 0)
	ps := sp.CurrentPreferenceSet

	drawBounds := Extent2D{
		p0: [2]float32{0, 0},
		p1: [2]float32{ctx.paneExtent.Width(), ctx.paneExtent.Height()}}

	if ps.DisplayDCB {
		sp.DrawDCB(ctx, transforms)

		drawBounds.p1[1] -= STARSButtonHeight

		// scissor so we can't draw in the DCB area
		paneRemaining := ctx.paneExtent
		paneRemaining.p1[1] -= STARSButtonHeight
		fbPaneExtent := paneRemaining.Scale(dpiScale(platform))
		cb.Scissor(int(fbPaneExtent.p0[0]), int(fbPaneExtent.p0[1]),
			int(fbPaneExtent.Width()+.5), int(fbPaneExtent.Height()+.5))
	}

	weatherIntensity := float32(0)
	for i, set := range ps.WeatherIntensity {
		if set {
			weatherIntensity = float32(i) / float32(len(ps.WeatherIntensity)-1)
		}
	}
	if weatherIntensity != 0 {
		sp.weatherRadar.Draw(weatherIntensity, transforms, cb)
	}

	color := ps.Brightness.RangeRings.RGB()
	cb.LineWidth(1)
	DrawRangeRings(ps.RangeRingsCenter, float32(ps.RangeRingRadius), color, transforms, cb)

	transforms.LoadWindowViewingMatrices(cb)

	// Maps
	cb.PointSize(5)
	cb.LineWidth(1)
	for _, vmap := range scenarioGroup.STARSMaps {
		if _, ok := ps.VideoMapVisible[vmap.Name]; !ok {
			continue
		}

		color := ps.Brightness.VideoGroupA.RGB()
		if vmap.Group == 1 {
			color = ps.Brightness.VideoGroupB.RGB()
		}
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(vmap.cb)
	}

	transforms.LoadWindowViewingMatrices(cb)

	if ps.Brightness.Compass > 0 {
		cb.LineWidth(1)
		cbright := ps.Brightness.Compass.RGB()
		font := sp.systemFont[ps.CharSize.Tools]
		DrawCompass(ps.CurrentCenter, ctx, 0, font, cbright, drawBounds, transforms, cb)
	}

	// Per-aircraft stuff: tracks, datablocks, vector lines, range rings, ...
	// Sort the aircraft so that they are always drawn in the same order
	// (go's map iterator randomization otherwise randomizes the order,
	// which can cause shimmering when datablocks overlap (especially if
	// one is selected). We'll go with alphabetical by callsign, with the
	// selected aircraft, if any, always drawn last.
	aircraft := sp.visibleAircraft()
	sort.Slice(aircraft, func(i, j int) bool {
		return aircraft[i].Callsign < aircraft[j].Callsign
	})

	sp.drawSystemLists(aircraft, ctx, transforms, cb)

	sp.Facility.CRDAConfig.DrawRegions(ctx, transforms, cb)

	// Tools before datablocks
	sp.drawPTLs(aircraft, ctx, transforms, cb)
	sp.drawRingsAndCones(aircraft, ctx, transforms, cb)
	sp.drawRBLs(ctx, transforms, cb)
	sp.drawMinSep(ctx, transforms, cb)
	sp.drawCARings(ctx, transforms, cb)
	sp.drawAirspace(ctx, transforms, cb)

	DrawHighlighted(ctx, transforms, cb)

	sp.drawTracks(aircraft, ctx, transforms, cb)
	sp.updateDatablockTextAndPosition(aircraft)
	sp.drawDatablocks(aircraft, ctx, transforms, cb)
	sp.consumeMouseEvents(ctx, transforms, cb)
}

func (sp *STARSPane) processKeyboardInput(ctx *PaneContext) {
	if !ctx.haveFocus || ctx.keyboard == nil {
		return
	}

	input := strings.ToUpper(ctx.keyboard.Input)
	if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "" && len(input) > 0 {
		sp.multiFuncPrefix = string(input[0])
		input = input[1:]
	}
	sp.previewAreaInput += input

	ps := &sp.CurrentPreferenceSet

	//lg.Printf("input \"%s\" ctl %v alt %v", input, ctx.keyboard.IsPressed(KeyControl), ctx.keyboard.IsPressed(KeyAlt))
	if ctx.keyboard.IsPressed(KeyControl) && len(input) == 1 && unicode.IsDigit(rune(input[0])) {
		idx := byte(input[0]) - '0'
		// This test should be redundant given the IsDigit check, but just to be safe...
		if int(idx) < len(ps.Bookmarks) {
			if ctx.keyboard.IsPressed(KeyAlt) {
				// Record bookmark
				ps.Bookmarks[idx].Center = ps.CurrentCenter
				ps.Bookmarks[idx].Range = ps.Range
				ps.Bookmarks[idx].TopDownMode = ps.TopDownMode
			} else {
				// Recall bookmark
				ps.Center = ps.Bookmarks[idx].Center
				ps.CurrentCenter = ps.Bookmarks[idx].Center
				ps.Range = ps.Bookmarks[idx].Range
				ps.TopDownMode = ps.Bookmarks[idx].TopDownMode
			}
		}
	}

	for key := range ctx.keyboard.Pressed {
		switch key {
		case KeyBackspace:
			if len(sp.previewAreaInput) > 0 {
				sp.previewAreaInput = sp.previewAreaInput[:len(sp.previewAreaInput)-1]
			} else {
				sp.multiFuncPrefix = ""
			}

		case KeyEnd:
			sp.resetInputState()
			sp.commandMode = CommandModeMin

		case KeyEnter:
			status := sp.executeSTARSCommand(sp.previewAreaInput)
			if status.err != nil {
				// TODO: rewrite errors returned by the ATCServer to e.g.,
				// ILL TRK, etc.
				sp.previewAreaOutput = status.err.Error()
			} else {
				if status.clear {
					sp.resetInputState()
				}
				sp.previewAreaOutput = status.output
			}

		case KeyEscape:
			sp.resetInputState()
			sp.activeDCBMenu = DCBMenuMain
			// Also disable any mouse capture from spinners, just in case
			// the user is mashing escape to get out of one.
			sp.disableMenuSpinner()

		case KeyF1:
			if ctx.keyboard.IsPressed(KeyControl) {
				// Recenter
				ps.Center = scenarioGroup.Center
				ps.CurrentCenter = ps.Center
			}

		case KeyF2:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activeDCBMenu = DCBMenuMaps
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeMaps
			}

		case KeyF3:
			sp.resetInputState()
			sp.commandMode = CommandModeInitiateControl

		case KeyF4:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activeDCBMenu = DCBMenuBrite
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeTerminateControl
			}

		case KeyF5:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.activeDCBMenu = DCBMenuMain
				sp.activateMenuSpinner(unsafe.Pointer(&ps.LeaderLineLength))
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeHandOff
			}

		case KeyF6:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activeDCBMenu = DCBMenuCharSize
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeVP
			}

		case KeyF7:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				if sp.activeDCBMenu == DCBMenuMain {
					sp.activeDCBMenu = DCBMenuAux
				} else {
					sp.activeDCBMenu = DCBMenuMain
				}
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeMultiFunc
			}

		case KeyF8:
			if ctx.keyboard.IsPressed(KeyControl) {
				sp.disableMenuSpinner()
				ps.DisplayDCB = !ps.DisplayDCB
			}

		case KeyF9:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activateMenuSpinner(unsafe.Pointer(&ps.RangeRingRadius))
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeFlightData
			}

		case KeyF10:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activateMenuSpinner(unsafe.Pointer(&ps.Range))
			}

		case KeyF11:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activeDCBMenu = DCBMenuSite
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeCollisionAlert
			}
		}
	}
}

func (sp *STARSPane) disableMenuSpinner() {
	activeSpinner = nil
	platform.EndCaptureMouse()
}

func (sp *STARSPane) activateMenuSpinner(ptr unsafe.Pointer) {
	activeSpinner = ptr
}

func (sp *STARSPane) getAircraftIndex(ac *Aircraft) int {
	if idx, ok := sp.aircraftToIndex[ac]; ok {
		return idx
	} else {
		idx := len(sp.aircraftToIndex) + 1
		sp.aircraftToIndex[ac] = idx
		sp.indexToAircraft[idx] = ac
		return idx
	}
}

func (sp *STARSPane) executeSTARSCommand(cmd string) (status STARSCommandStatus) {
	lookupAircraft := func(callsign string) *Aircraft {
		if ac := sim.GetAircraft(callsign); ac != nil {
			return ac
		}

		// try to match squawk code
		for _, ac := range sp.visibleAircraft() {
			if ac.Squawk.String() == callsign {
				return ac
			}
		}

		if idx, err := strconv.Atoi(callsign); err == nil {
			if ac, ok := sp.indexToAircraft[idx]; ok {
				return ac
			}
		}

		return nil
	}
	lookupCallsign := func(callsign string) string {
		ac := lookupAircraft(callsign)
		if ac != nil {
			return ac.Callsign
		}
		return callsign
	}

	ps := &sp.CurrentPreferenceSet

	switch sp.commandMode {
	case CommandModeNone:
		switch cmd {
		case "*D+":
			ps.DisplayTPASize = !ps.DisplayTPASize
			// TODO: check that toggling all is the expected behavior
			for _, state := range sp.aircraft {
				state.displayTPASize = !state.displayTPASize
			}
			status.clear = true
			return

		case "*T":
			// Remove all RBLs
			sp.rangeBearingLines = nil
			status.clear = true
			return

		case "**J":
			// remove all j-rings
			for _, state := range sp.aircraft {
				state.jRingRadius = 0
			}
			status.clear = true
			return

		case "**P":
			// remove all cones
			for _, state := range sp.aircraft {
				state.coneLength = 0
			}
			status.clear = true
			return

		case "DA":
			sp.drawApproachAirspace = !sp.drawApproachAirspace
			status.clear = true
			return

		case "DD":
			sp.drawDepartureAirspace = !sp.drawDepartureAirspace
			status.clear = true
			return
		}

		if len(cmd) >= 3 && cmd[:2] == "*T" {
			// Delete specified rbl
			if idx, err := strconv.Atoi(cmd[2:]); err == nil {
				idx--
				if idx >= 0 && idx < len(sp.rangeBearingLines) {
					sp.rangeBearingLines = DeleteSliceElement(sp.rangeBearingLines, idx)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
			} else {
				status.err = ErrSTARSIllegalParam
			}
			return
		}

		f := strings.Fields(cmd)
		if len(f) > 1 {
			if f[0] == ".AUTOTRACK" {
				for _, airport := range f[1:] {
					if airport == "NONE" {
						sp.AutoTrackDepartures = make(map[string]interface{})
					} else if airport == "ALL" {
						sp.AutoTrackDepartures = make(map[string]interface{})
						for name := range scenarioGroup.Airports {
							sp.AutoTrackDepartures[name] = nil
						}
					} else {
						// See if it's in the facility
						if _, ok := scenarioGroup.Airports[airport]; ok {
							sp.AutoTrackDepartures[airport] = nil
						} else {
							status.err = ErrSTARSIllegalParam
							return
						}
					}
				}
				status.clear = true
				return
			} else if f[0] == ".FIND" {
				if pos, ok := scenarioGroup.Locate(f[1]); ok {
					globalConfig.highlightedLocation = pos
					globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}
			}
		}

	case CommandModeInitiateControl:
		if ac := lookupAircraft(cmd); ac == nil {
			status.err = ErrSTARSIllegalTrack // error code?
		} else {
			status.err = sim.InitiateTrack(lookupCallsign(cmd))
			status.clear = true
			state := sp.aircraft[ac]
			state.datablockType = FullDatablock
			// Display flight plan
			if status.err == nil {
				status.output, _ = flightPlanSTARS(ac)
			}
		}
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for ac := range sp.aircraft {
				if ac.TrackingController == sim.Callsign() {
					status.err = sim.DropTrack(ac.Callsign)
				}
			}
			status.clear = true
			return
		} else {
			status.err = sim.DropTrack(lookupCallsign(cmd))
			status.clear = true
			return
		}

	case CommandModeHandOff:
		f := strings.Fields(cmd)
		switch len(f) {
		case 0:
			// Accept hand off of target closest to range rings center
			var closest *Aircraft
			var closestDistance float32
			for _, ac := range sp.visibleAircraft() {
				d := nmdistance2ll(ps.RangeRingsCenter, ac.TrackPosition())
				if closest == nil || d < closestDistance {
					closest = ac
					closestDistance = d
				}
			}

			if closest != nil {
				status.err = sim.AcceptHandoff(closest.Callsign)
				// Display flight plan
				if status.err == nil {
					status.output, _ = flightPlanSTARS(closest)
				}
			}
			status.clear = true
			return
		case 1:
			status.err = sim.CancelHandoff(lookupCallsign(f[0]))
			status.clear = true
			return
		case 2:
			status.err = sim.Handoff(lookupCallsign(f[1]), f[0])
			status.clear = true
			return
		}

	case CommandModeVP:
		// TODO
		status.err = ErrSTARSCommandFormat
		return

	case CommandModeMultiFunc:
		switch sp.multiFuncPrefix {
		case "B":
			validBeacon := func(s string) bool {
				for ch := range s {
					if !(ch == '0' || ch == '1' || ch == '2' || ch == '3' ||
						ch == '4' || ch == '5' || ch == '6' || ch == '7') {
						return false
					}
				}
				return true
			}
			toggleBeacon := func(code string) {
				sfilt := FilterSlice(ps.SelectedBeaconCodes,
					func(c string) bool { return c == code })
				if len(sfilt) < len(ps.SelectedBeaconCodes) {
					// it was in there, so we'll toggle it off
					ps.SelectedBeaconCodes = sfilt
				} else {
					ps.SelectedBeaconCodes = append(ps.SelectedBeaconCodes, code)
				}
			}

			if cmd == "" {
				// B -> for unassociated track, toggle display of beacon code in LDB
				ps.DisplayLDBBeaconCodes = !ps.DisplayLDBBeaconCodes
				status.clear = true
				return
			} else if cmd == "E" {
				// BE -> enable display of beacon code in ldbs
				ps.DisplayLDBBeaconCodes = true
				status.clear = true
				return
			} else if cmd == "I" {
				// BI -> inhibit display of beacon code in ldbs
				ps.DisplayLDBBeaconCodes = false
				status.clear = true
				return
			} else if len(cmd) == 2 && validBeacon(cmd) {
				// B[0-7][0-7] -> toggle select beacon code block
				toggleBeacon(cmd)
				status.clear = true
				return
			} else if len(cmd) == 4 && validBeacon(cmd) {
				// B[0-7][0-7][0-7][0-7] -> toggle select discrete beacon code
				toggleBeacon(cmd)
				status.clear = true
				return
			}

		case "D":
			// D(callsign)
			if ac := sim.GetAircraft(lookupCallsign(cmd)); ac != nil {
				// Display flight plan
				status.output, status.err = flightPlanSTARS(ac)
			} else {
				status.err = ErrSTARSIllegalTrack // error code?
			}
			return

		case "E":
			if cmd == "" {
				ps.OverflightFullDatablocks = !ps.OverflightFullDatablocks
				status.clear = true
				return
			}

		case "F":
			// altitude filters
			af := &ps.AltitudeFilters
			if cmd == "" {
				// F -> display current in preview area
				status.output = fmt.Sprintf("%03d %03d\n%03d %03d",
					af.Unassociated[0]/100, af.Unassociated[1]/100,
					af.Associated[0]/100, af.Associated[1]/100)
				status.clear = true
				return
			} else if cmd[0] == 'C' {
				// FC(low associated)(high associated)
				if len(cmd[1:]) != 6 {
					status.err = ErrSTARSCommandFormat
				} else if digits, err := strconv.Atoi(cmd[1:]); err == nil {
					// TODO: validation?
					// The first three digits give the low altitude in 100s of feet
					af.Associated[0] = (digits / 1000) * 100
					// And the last three give the high altitude in 100s of feet
					af.Associated[1] = (digits % 1000) * 100
				} else {
					status.err = ErrSTARSIllegalParam
				}
				status.clear = true
				return
			} else {
				// F(low unassociated)(high unassociated) (low associated)(high associated)
				if len(cmd) != 13 {
					status.err = ErrSTARSCommandFormat
				} else {
					unassoc, assoc := cmd[0:6], cmd[7:13]
					if digits, err := strconv.Atoi(unassoc); err == nil {
						// TODO: more validation?
						af.Unassociated[0] = (digits / 1000) * 100
						// And the last three give the high altitude in 100s of feet
						af.Unassociated[1] = (digits % 1000) * 100

						if digits, err := strconv.Atoi(assoc); err == nil {
							// TODO: more validation?
							af.Associated[0] = (digits / 1000) * 100
							// And the last three give the high altitude in 100s of feet
							af.Associated[1] = (digits % 1000) * 100
						} else {
							status.err = ErrSTARSIllegalParam
						}
					} else {
						status.err = ErrSTARSIllegalParam
					}
				}
				status.clear = true
				return
			}

		case "I":
			if cmd == "*" {
				// I* clears the status area(?!)
				status.clear = true
				return
			}

		case "L":
			// leader lines
			setLLDir := func(dir *CardinalOrdinalDirection, pred func(*Aircraft) bool) {
				for ac, state := range sp.aircraft {
					if pred(ac) {
						state.leaderLineDirection = dir
					}
				}
			}
			switch len(cmd) {
			case 0:
				status.err = ErrSTARSCommandFormat
				return

			case 1:
				if dir, ok := numpadToDirection(cmd[0]); ok {
					// Tracked by me
					me := sim.Callsign()
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == me })
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case 2:
				if dir, ok := numpadToDirection(cmd[0]); ok && cmd[1] == 'U' {
					// FIXME: should be unassociated tracks
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == "" })
					status.clear = true
				} else if ok && cmd[1] == '*' {
					// Tracked by other controllers
					me := sim.Callsign()
					setLLDir(dir, func(ac *Aircraft) bool {
						return ac.TrackingController != "" &&
							ac.TrackingController != me
					})
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case 4:
				// L(id)(space)(dir)
				status.err = ErrSTARSCommandFormat // set preemptively; clear on success
				for _, ctrl := range sim.GetAllControllers() {
					if ctrl.SectorId == cmd[:2] {
						if dir, ok := numpadToDirection(cmd[3]); ok && cmd[2] == ' ' {
							setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == ctrl.Callsign })
							status.clear = true
							status.err = nil
						}
					}
				}
				return

			default:
				// L(dir)(space)(callsign)
				if dir, ok := numpadToDirection(cmd[0]); ok && cmd[1] == ' ' {
					// We know len(cmd) >= 3 given the above cases...
					callsign := lookupCallsign(cmd[2:])
					if ac := sim.GetAircraft(callsign); ac != nil {
						sp.aircraft[ac].leaderLineDirection = dir
						status.clear = true
						return
					}
				}
				status.err = ErrSTARSCommandFormat
				return

			}

		case "N":
			// CRDA...
			if cmd == "" {
				// TODO: toggle CRDA processing (on by default)
			}

		case "O":
			if cmd == "" {
				ps.AutomaticFDBOffset = !ps.AutomaticFDBOffset
				status.clear = true
				return
			} else if cmd == "E" {
				ps.AutomaticFDBOffset = true
				status.clear = true
				return
			} else if cmd == "I" {
				ps.AutomaticFDBOffset = true
				status.clear = true
				return
			}

		case "P":
			updateTowerList := func(idx int) {
				if len(cmd[1:]) == 0 {
					ps.TowerLists[idx].Visible = !ps.TowerLists[idx].Visible
					status.clear = true
				} else {
					if n, err := strconv.Atoi(cmd[1:]); err == nil {
						n = clamp(n, 1, 100)
						ps.TowerLists[idx].Lines = n
					} else {
						status.err = ErrSTARSIllegalParam
					}
					status.clear = true
				}
			}

			if len(cmd) == 1 {
				switch cmd[0] {
				case '1':
					updateTowerList(0)
					return
				case '2':
					updateTowerList(1)
					return
				case '3':
					updateTowerList(2)
					return
				}
			}

		case "S":
			switch len(cmd) {
			case 0:
				// S -> clear atis, first line of text
				ps.CurrentATIS = ""
				ps.GIText[0] = ""
				status.clear = true
				return

			case 1:
				if cmd[0] == '*' {
					// S* -> clear atis
					ps.CurrentATIS = ""
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' {
					// S[1-9] -> clear corresponding line of text
					idx := cmd[0] - '1'
					ps.GIText[idx] = ""
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis) -> set atis code
					ps.CurrentATIS = string(cmd[0])
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}

			default:
				if len(cmd) == 2 && cmd[0] >= 'A' && cmd[0] <= 'Z' && cmd[1] == '*' {
					// S(atis)* -> set atis, delete first line of text
					ps.CurrentATIS = string(cmd[0])
					ps.GIText[0] = ""
					status.clear = true
					return
				} else if cmd[0] == '*' {
					// S*(text) -> clear atis, set first line of gi text
					ps.CurrentATIS = ""
					ps.GIText[0] = cmd[1:]
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' && cmd[1] == ' ' {
					// S[1-9](spc)(text) -> set corresponding line of GI text
					idx := cmd[0] - '1'
					ps.GIText[idx] = cmd[2:]
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis)(text) -> set atis and first line of GI text
					ps.CurrentATIS = string(cmd[0])
					ps.GIText[0] = cmd[1:]
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}

			}

		case "T":
			updateList := func(cmd string, visible *bool, lines *int) {
				if cmd == "" {
					*visible = !*visible
				} else if lines != nil {
					if n, err := strconv.Atoi(cmd); err == nil {
						*lines = clamp(n, 1, 100) // TODO: or error if out of range? (and below..)
					} else {
						status.err = ErrSTARSIllegalParam
					}
				}
				status.clear = true
			}

			if len(cmd) == 0 {
				updateList("", &ps.TABList.Visible, &ps.TABList.Lines)
				return
			} else {
				switch cmd[0] {
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					updateList(cmd, &ps.TABList.Visible, &ps.TABList.Lines)
					return
				case 'V':
					updateList(cmd[1:], &ps.VFRList.Visible, &ps.VFRList.Lines)
					return
				case 'M':
					updateList(cmd[1:], &ps.AlertList.Visible, &ps.AlertList.Lines)
					return
				case 'C':
					updateList(cmd[1:], &ps.CoastList.Visible, &ps.CoastList.Lines)
					return
				case 'S':
					updateList(cmd[1:], &ps.SignOnList.Visible, nil)
					return
				case 'X':
					updateList(cmd[1:], &ps.VideoMapsList.Visible, nil)
					return
				case 'N':
					updateList(cmd[1:], &ps.CRDAStatusList.Visible, nil)
					return
				}
			}

		case "V":
			switch cmd {
			case "MI":
				ps.DisableMSAW = true
				status.clear = true
				return
			case "ME":
				ps.DisableMSAW = false
				status.clear = true
				return
			}

		case "Y":
			f := strings.Fields(cmd)
			if len(f) == 1 {
				// Y callsign -> clear scratchpad and reported altitude
				// Clear pilot alt. and scratchpad
				callsign := lookupCallsign(f[0])
				if ac := sim.GetAircraft(callsign); ac != nil {
					sp.aircraft[ac].pilotAltitude = 0
				}
				status.err = sim.SetScratchpad(callsign, "")
				status.clear = true
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt

				callsign := lookupCallsign(f[0])
				// Either pilot alt or scratchpad entry
				if ac := sim.GetAircraft(callsign); ac == nil {
					status.err = ErrSTARSIllegalTrack
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.aircraft[ac].pilotAltitude = alt * 100
				} else {
					status.err = sim.SetScratchpad(callsign, f[1])
				}
				status.clear = true
				return
			}

		case "Z":
			if cmd == "A" {
				// TODO: test audible alarm
				status.clear = true
				return
			}
			status.err = ErrSTARSCommandFormat
			return

		case "9":
			if cmd == "" {
				ps.GroundRangeMode = !ps.GroundRangeMode
			} else {
				status.err = ErrSTARSCommandFormat
			}
			status.clear = true
			return
		}

	case CommandModeFlightData:
		f := strings.Fields(cmd)
		if len(f) == 1 {
			callsign := lookupCallsign(f[0])
			status.err = sim.SetSquawkAutomatic(callsign)
		} else if len(f) == 2 {
			if squawk, err := ParseSquawk(f[1]); err == nil {
				callsign := lookupCallsign(f[0])
				status.err = sim.SetSquawk(callsign, squawk)
			} else {
				status.err = ErrSTARSIllegalParam
			}
		} else {
			status.err = ErrSTARSCommandFormat
		}
		status.clear = true
		return

	case CommandModeCollisionAlert:
		if len(cmd) > 3 && cmd[:2] == "K " {
			callsign := lookupCallsign(cmd[2:])
			if ac := sim.GetAircraft(callsign); ac != nil {
				state := sp.aircraft[ac]
				state.disableCAWarnings = !state.disableCAWarnings
			} else {
				status.err = ErrSTARSIllegalParam
			}
			status.clear = true
			return
		} else if cmd == "AI" {
			ps.DisableCAWarnings = true
			status.clear = true
			return
		} else if cmd == "AE" {
			ps.DisableCAWarnings = false
			status.clear = true
			return
		}

	case CommandModeMin:
		if cmd == "" {
			// Clear min sep
			sp.minSepAircraft[0] = nil
			sp.minSepAircraft[1] = nil
			status.clear = true
		} else {
			status.err = ErrSTARSCommandFormat
		}
		return

	case CommandModeSavePrefAs:
		psave := sp.CurrentPreferenceSet.Duplicate()
		psave.Name = cmd
		sp.PreferenceSets = append(sp.PreferenceSets, psave)
		sp.SelectedPreferenceSet = len(sp.PreferenceSets) - 1
		status.clear = true
		globalConfig.Save()
		return

	case CommandModeMaps:
		if len(cmd) > 0 {
			if m, err := strconv.Atoi(cmd); err == nil && m > 0 && m < len(scenarioGroup.STARSMaps) {
				m--
				name := scenarioGroup.STARSMaps[m].Name
				if _, ok := ps.VideoMapVisible[name]; ok {
					delete(ps.VideoMapVisible, name)
				} else {
					ps.VideoMapVisible[name] = nil
				}
				return
			} else {
				status.err = ErrSTARSCommandFormat
			}
			status.clear = true
			return
		}

	case CommandModeLDR:
		if len(cmd) > 0 {
			if r, err := strconv.Atoi(cmd); err == nil {
				ps.Range = clamp(float32(r), 0, 7)
			} else {
				status.err = ErrSTARSIllegalParam
			}
			status.clear = true
			return
		}

	case CommandModeRangeRings:
		// TODO: what if user presses enter?
		switch cmd {
		case "2":
			ps.RangeRingRadius = 2
		case "5":
			ps.RangeRingRadius = 5
		case "10":
			ps.RangeRingRadius = 10
		case "20":
			ps.RangeRingRadius = 20
		default:
			status.err = ErrSTARSIllegalParam
		}
		status.clear = true
		return

	case CommandModeRange:
		if len(cmd) > 0 {
			if r, err := strconv.Atoi(cmd); err == nil {
				ps.Range = clamp(float32(r), 6, 256)
			} else {
				status.err = ErrSTARSIllegalParam
			}
			status.clear = true
			return
		}

	case CommandModeSiteMenu:
		if cmd == "~" {
			ps.RadarSiteSelected = ""
			status.clear = true
			return
		} else if len(cmd) > 0 {
			// Index, character id, or name
			if i, err := strconv.Atoi(cmd); err == nil && i >= 0 && i < len(scenarioGroup.RadarSites) {
				ps.RadarSiteSelected = SortedMapKeys(scenarioGroup.RadarSites)[i]
				status.clear = true
				return
			}
			for id, rs := range scenarioGroup.RadarSites {
				if cmd == rs.Char || cmd == id {
					ps.RadarSiteSelected = id
					status.clear = true
				}
				return
			}
			status.clear = true
			status.err = ErrSTARSIllegalParam
			return
		}
	}

	status.err = ErrSTARSCommandFormat
	return
}

func (sp *STARSPane) executeSTARSClickedCommand(cmd string, mousePosition [2]float32,
	transforms ScopeTransformations) (status STARSCommandStatus) {
	// See if an aircraft was clicked
	ac := sp.tryGetClickedAircraft(mousePosition, transforms)

	isControllerId := func(id string) bool {
		// FIXME: check--this is likely to be pretty slow, relatively
		// speaking...
		for _, ctrl := range sim.GetAllControllers() {
			if ctrl.SectorId == id {
				return true
			}
		}
		return false
	}

	ps := &sp.CurrentPreferenceSet

	if ac != nil {
		state := sp.aircraft[ac]

		switch sp.commandMode {
		case CommandModeNone:
			switch len(cmd) {
			case 0:
				if ac.InboundHandoffController != "" {
					// Accept inbound h/o
					status.clear = true
					status.err = sim.AcceptHandoff(ac.Callsign)
					state.datablockType = FullDatablock
					// Display flight plan
					if status.err == nil {
						status.output, _ = flightPlanSTARS(ac)
					}
					return
				} else if ac.OutboundHandoffController != "" {
					// cancel offered handoff offered
					status.clear = true
					status.err = sim.CancelHandoff(ac.Callsign)
					return
				} else if _, ok := sp.pointedOutAircraft.Get(ac); ok {
					// ack point out
					sp.pointedOutAircraft.Delete(ac)
					status.clear = true
					return
				} else if state.outboundHandoffAccepted {
					// ack accepted handoff by other controller
					state.outboundHandoffAccepted = false
					state.outboundHandoffFlashEnd = time.Now()
					eventStream.Post(&AckedHandoffEvent{ac: ac})
				} else { //if ac.IsAssociated() {
					if state.datablockType != FullDatablock {
						state.datablockType = FullDatablock
						// do not collapse datablock if user is tracking the aircraft
					} else if ac.TrackingController != sim.Callsign() {
						state.datablockType = PartialDatablock
					}
				}
			//} else {
			//sp.queryUnassociated.Add(ac, nil, 5*time.Second)
			//}
			// TODO: ack SPC alert

			case 1:
				if cmd == "." {
					status.clear = true
					status.err = sim.SetScratchpad(ac.Callsign, "")
					return
				} else if cmd == "U" {
					status.clear = true
					status.err = sim.RejectHandoff(ac.Callsign)
					return
				} else if cmd == "*" {
					from := ac.TrackPosition()
					sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
						p := transforms.LatLongFromWindowP(pw)
						hdg := headingp2ll(from, p, scenarioGroup.MagneticVariation)
						dist := nmdistance2ll(from, p)

						status.output = fmt.Sprintf("%03d/%.2f", int(hdg+.5), dist)
						status.clear = true
						return
					}
					return
				} else if dir, ok := numpadToDirection(cmd[0]); ok {
					state.leaderLineDirection = dir
					status.clear = true
					return
				}

			case 2:
				if isControllerId(cmd) {
					status.clear = true
					status.err = sim.Handoff(ac.Callsign, cmd)
					return
				} else if cmd == "*J" {
					// remove j-ring for aircraft
					state.jRingRadius = 0
					status.clear = true
					return
				} else if cmd == "*P" {
					// remove cone for aircraft
					state.coneLength = 0
					status.clear = true
					return
				} else if cmd == "*T" {
					// range bearing line
					var rbl STARSRangeBearingLine
					rbl.p[0].ac = ac
					sp.scopeClickHandler = rblSecondClickHandler(sp, rbl)
					return
				} else if cmd == "HJ" || cmd == "RF" || cmd == "EM" || cmd == "MI" || cmd == "SI" {
					state.spcOverride = cmd
					status.clear = true
					return
				}

			case 3:
				if isControllerId(cmd) {
					status.clear = true
					status.err = sim.Handoff(ac.Callsign, cmd)
					return
				} else if cmd == "*D+" {
					ps.DisplayTPASize = !ps.DisplayTPASize
					status.clear = true
					return
				} else if cmd[2] == '*' && isControllerId(cmd[:2]) {
					status.clear = true
					status.err = sim.PointOut(ac.Callsign, cmd[:2])
					return
				} else {
					if alt, err := strconv.Atoi(cmd); err == nil {
						state.pilotAltitude = alt * 100
						status.clear = true
						return
					}
				}

			case 4:
				if cmd[0] == '+' {
					if alt, err := strconv.Atoi(cmd[1:]); err == nil {
						status.clear = true
						status.err = sim.SetTemporaryAltitude(ac.Callsign, alt*100)
					} else {
						status.err = ErrSTARSIllegalParam
					}
					return
				} else {
					// HACK: disable this for training command mode...
					/*
						status.clear = true
						status.err = amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
							fp.AircraftType = strings.TrimRight(cmd, "*")
						})
						return
					*/
				}

			case 5:
				switch cmd[:2] {
				case "++":
					if alt, err := strconv.Atoi(cmd[2:]); err == nil {
						status.err = amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
							fp.Altitude = alt * 100
						})
						status.clear = true
					} else {
						status.err = ErrSTARSIllegalParam
					}
					return
				}
			}

			if len(cmd) > 2 && cmd[:2] == "*J" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					state.jRingRadius = clamp(float32(r), 1, 30)
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					state.jRingRadius = clamp(float32(r), 1, 30)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			}
			if len(cmd) > 2 && cmd[:2] == "*P" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					state.coneLength = clamp(float32(r), 1, 30)
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					state.coneLength = clamp(float32(r), 1, 30)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			}

			if len(cmd) > 0 {
				remaining, err := sim.RunAircraftCommands(ac, cmd)
				if err != nil {
					if err == ErrInvalidAltitude || err == ErrInvalidHeading {
						status.err = ErrSTARSIllegalParam
					} else {
						status.err = ErrSTARSCommandFormat
					}

					// Leave the unexecuted commands for editing, etc.
					globalConfig.Audio.PlaySound(AudioEventCommandError)
					sp.previewAreaInput = strings.Join(remaining, " ")
				} else {
					status.clear = true
				}
				return
			}

		case CommandModeInitiateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			status.err = sim.InitiateTrack(ac.Callsign)
			state.datablockType = FullDatablock
			if status.err == nil {
				// Display flight plan
				status.output, _ = flightPlanSTARS(ac)
			}
			return

		case CommandModeTerminateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			status.err = sim.DropTrack(ac.Callsign)
			return

		case CommandModeHandOff:
			if cmd == "" {
				status.clear = true
				status.err = sim.CancelHandoff(ac.Callsign)
			} else {
				status.clear = true
				status.err = sim.Handoff(ac.Callsign, cmd)
			}
			return

		case CommandModeVP:
			// TODO: implement
			status.err = ErrSTARSCommandFormat
			return

		case CommandModeMultiFunc:
			switch sp.multiFuncPrefix {
			case "B":
				if cmd == "" {
					state.displayReportedBeacon = !state.displayReportedBeacon
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "D":
				if cmd == "" {
					status.output, status.err = flightPlanSTARS(ac)
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "L":
				if len(cmd) == 1 {
					if dir, ok := numpadToDirection(cmd[0]); ok {
						state.leaderLineDirection = dir
						status.clear = true
						return
					}
				}
				status.err = ErrSTARSCommandFormat
				return

			case "M":
				if cmd == "" {
					state.displayPilotAltitude = !state.displayPilotAltitude
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "N":
				// CRDA
				if cmd == "" {
					if state.isGhost {
						state.suppressGhost = true
					} else {
						state.forceGhost = true
					}
				} else if cmd == "*" {
					if state.isGhost {
						// TODO: display parent track info for slewed ghost
					} else {
						state.forceGhost = true // ?? redundant with cmd == ""?
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Q":
				if cmd == "" {
					status.clear = true
					state.inhibitMSAWAlert = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "R":
				if cmd == "" {
					state.displayPTL = !state.displayPTL
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "V":
				if cmd == "" {
					state.disableMSAW = !state.disableMSAW
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Y":
				if cmd == "" {
					// Clear pilot reported altitude and scratchpad
					state.pilotAltitude = 0
					status.clear = true
					status.err = sim.SetScratchpad(ac.Callsign, "")
					return
				} else {
					// Is it an altitude or a scratchpad update?
					if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
						state.pilotAltitude = alt * 100
						status.clear = true
					} else {
						status.clear = true
						status.err = sim.SetScratchpad(ac.Callsign, cmd)
					}
					return
				}
			}

		case CommandModeFlightData:
			if cmd == "" {
				status.clear = true
				status.err = sim.SetSquawkAutomatic(ac.Callsign)
				return
			} else {
				if squawk, err := ParseSquawk(cmd); err == nil {
					status.err = sim.SetSquawk(ac.Callsign, squawk)
				} else {
					status.err = ErrSTARSIllegalParam
				}
				status.clear = true
				return
			}

		case CommandModeCollisionAlert:
			if cmd == "K" {
				state := sp.aircraft[ac]
				state.disableCAWarnings = !state.disableCAWarnings
				status.clear = true
				// TODO: check should we set sp.commandMode = CommandMode
				// (applies here and also to others similar...)
				return
			}

		case CommandModeMin:
			if cmd == "" {
				sp.minSepAircraft[0] = ac
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
					if ac := sp.tryGetClickedAircraft(pw, transforms); ac != nil {
						sp.minSepAircraft[1] = ac
						status.clear = true
					} else {
						status.err = ErrSTARSIllegalTrack
					}
					return
				}
			} else {
				status.err = ErrSTARSCommandFormat
				return
			}
		}
	}

	// No aircraft selected
	if sp.commandMode == CommandModeNone {
		if cmd == "*T" {
			var rbl STARSRangeBearingLine
			rbl.p[0].loc = transforms.LatLongFromWindowP(mousePosition)
			sp.scopeClickHandler = rblSecondClickHandler(sp, rbl)
			return
		}
	}

	if sp.commandMode == CommandModeMultiFunc {
		cmd = sp.multiFuncPrefix + cmd
		if cmd == "D*" {
			pll := transforms.LatLongFromWindowP(mousePosition)
			format := func(v float32) string {
				d := int(v)
				v = 60 * (v - float32(d))
				m := int(v)
				v = 60 * (v - float32(d))
				s := int(v)
				return fmt.Sprintf("%3d %02d.%02d", d, m, s)
			}
			status.output = fmt.Sprintf("%s %s", format(pll.Longitude()), format(pll.Latitude()))
			status.clear = true
			return
		} else if cmd == "P" {
			ps.PreviewAreaPosition = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "S" {
			ps.SSAList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "T" {
			ps.TABList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TV" {
			ps.VFRList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TM" {
			ps.AlertList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TC" {
			ps.CoastList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TS" {
			ps.SignOnList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TX" {
			ps.VideoMapsList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "TN" {
			ps.CRDAStatusList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if len(cmd) == 2 && cmd[0] == 'P' {
			if idx, err := strconv.Atoi(cmd[1:]); err == nil && idx > 0 && idx <= 3 {
				ps.TowerLists[idx-1].Position = transforms.NormalizedFromWindowP(mousePosition)
				status.clear = true
				return
			}
		}
	}

	if cmd != "" {
		status.err = ErrSTARSCommandFormat
	}
	return
}

func numpadToDirection(key byte) (*CardinalOrdinalDirection, bool) {
	var dir CardinalOrdinalDirection
	switch key {
	case '1':
		dir = CardinalOrdinalDirection(SouthWest)
		return &dir, true
	case '2':
		dir = CardinalOrdinalDirection(South)
		return &dir, true
	case '3':
		dir = CardinalOrdinalDirection(SouthEast)
		return &dir, true
	case '4':
		dir = CardinalOrdinalDirection(West)
		return &dir, true
	case '5':
		return nil, true
	case '6':
		dir = CardinalOrdinalDirection(East)
		return &dir, true
	case '7':
		dir = CardinalOrdinalDirection(NorthWest)
		return &dir, true
	case '8':
		dir = CardinalOrdinalDirection(North)
		return &dir, true
	case '9':
		dir = CardinalOrdinalDirection(NorthEast)
		return &dir, true
	}
	return nil, false
}

func rblSecondClickHandler(sp *STARSPane,
	rbl STARSRangeBearingLine) func([2]float32, ScopeTransformations) (status STARSCommandStatus) {
	return func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
		rbl.p[1].ac = sp.tryGetClickedAircraft(pw, transforms)
		rbl.p[1].loc = transforms.LatLongFromWindowP(pw)
		sp.rangeBearingLines = append(sp.rangeBearingLines, rbl)
		status.clear = true
		return
	}
}

func (sp *STARSPane) DrawDCB(ctx *PaneContext, transforms ScopeTransformations) {
	sp.StartDrawDCB(ctx)

	ps := &sp.CurrentPreferenceSet

	switch sp.activeDCBMenu {
	case DCBMenuMain:
		STARSCallbackSpinner("RANGE\n", &ps.Range,
			func(v float32) string { return fmt.Sprintf("%d", int(v)) },
			func(v, delta float32) float32 {
				if delta > 0 {
					v++
				} else if delta < 0 {
					v--
				}
				return clamp(v, 6, 256)
			}, STARSButtonFull)
		if STARSSelectButton("PLACE\nCNTR", STARSButtonHalfVertical) {
			sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			}
		}
		ps.OffCenter = ps.CurrentCenter != ps.Center
		if STARSToggleButton("OFF\nCNTR", &ps.OffCenter, STARSButtonHalfVertical) {
			ps.CurrentCenter = ps.Center
		}
		STARSCallbackSpinner("RR\n", &ps.RangeRingRadius,
			func(v int) string { return fmt.Sprintf("%d", v) },
			func(v int, delta float32) int {
				di := 0
				if delta > 0 {
					di = 1
				} else if delta < 0 {
					di = -1
				}

				valid := []int{2, 5, 10, 20}
				for i := range valid {
					if v == valid[i] {
						i = clamp(i+di, 0, len(valid)-1)
						return valid[i]
					}
				}
				lg.Errorf("%d: invalid value for RR spinner", v)
				return valid[0]
			}, STARSButtonFull)
		if STARSSelectButton("PLACE\nRR", STARSButtonHalfVertical) {
			sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				status.clear = true
				return
			}
		}
		if STARSSelectButton("RR\nCNTR", STARSButtonHalfVertical) {
			cw := [2]float32{ctx.paneExtent.Width() / 2, ctx.paneExtent.Height() / 2}
			ps.RangeRingsCenter = transforms.LatLongFromWindowP(cw)
		}
		if STARSSelectButton("MAPS", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMaps
		}
		for i := 0; i < 6; i++ {
			if i >= len(scenarioGroup.STARSMaps) {
				STARSDisabledButton(fmt.Sprintf(" %d\n", i+1), STARSButtonHalfVertical)
			} else {
				text := fmt.Sprintf(" %d\n%s", i+1, scenarioGroup.STARSMaps[i].Label)
				name := scenarioGroup.STARSMaps[i].Name
				_, visible := ps.VideoMapVisible[name]
				if STARSToggleButton(text, &visible, STARSButtonHalfVertical) {
					if visible {
						ps.VideoMapVisible[name] = nil
					} else {
						delete(ps.VideoMapVisible, name)
					}
				}
			}
		}
		for i := range ps.WeatherIntensity {
			if STARSToggleButton("WX"+fmt.Sprintf("%d", i), &ps.WeatherIntensity[i],
				STARSButtonHalfHorizontal) {
				if ps.WeatherIntensity[i] {
					// turn off others
					for j := range ps.WeatherIntensity {
						if i != j {
							ps.WeatherIntensity[j] = false
						}
					}
				}
				if Find(ps.WeatherIntensity[:], true) != -1 {
					sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
				} else {
					// Don't fetch weather maps if they're not going to be displayed.
					sp.weatherRadar.Deactivate()
				}
			}
		}
		if STARSSelectButton("BRITE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuBrite
		}
		STARSCallbackSpinner("LDR DIR\n   ", &ps.LeaderLineDirection,
			func(d CardinalOrdinalDirection) string { return d.ShortString() },
			func(d CardinalOrdinalDirection, delta float32) CardinalOrdinalDirection {
				if delta == 0 {
					return d
				} else if delta < 0 {
					return CardinalOrdinalDirection((d + 7) % 8)
				} else {
					return CardinalOrdinalDirection((d + 1) % 8)
				}
			}, STARSButtonHalfVertical)
		STARSCallbackSpinner("LDR\n ", &ps.LeaderLineLength,
			func(v int) string { return fmt.Sprintf("%d", v) },
			func(v int, delta float32) int {
				if delta == 0 {
					return v
				} else if delta < 0 {
					return max(0, v-1)
				} else {
					return min(7, v+1)
				}
			}, STARSButtonHalfVertical)

		if STARSSelectButton("CHAR\nSIZE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuCharSize
		}
		STARSDisabledButton("MODE\nFSL", STARSButtonFull)
		if STARSSelectButton("PREF\n"+ps.Name, STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuPref
		}

		site := sp.radarSiteId()
		if STARSSelectButton("SITE\n"+site, STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuSite
		}
		if STARSSelectButton("SSA\nFILTER", STARSButtonHalfVertical) {
			sp.activeDCBMenu = DCBMenuSSAFilter
		}
		if STARSSelectButton("GI TEXT\nFILTER", STARSButtonHalfVertical) {
			sp.activeDCBMenu = DCBMenuGITextFilter
		}
		if STARSSelectButton("SHIFT", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuAux
		}

	case DCBMenuAux:
		STARSDisabledButton("VOL\n10", STARSButtonFull)
		STARSIntSpinner("HISTORY\n", &ps.RadarTrackHistory, 0, 5, STARSButtonFull)
		STARSDisabledButton("CURSOR\nHOME", STARSButtonFull)
		STARSDisabledButton("CSR SPD\n4", STARSButtonFull)
		STARSDisabledButton("MAP\nUNCOR", STARSButtonFull)
		STARSToggleButton("UNCOR", &ps.DisplayUncorrelatedTargets, STARSButtonFull)
		STARSDisabledButton("BEACON\nMODE-2", STARSButtonFull)
		STARSDisabledButton("RTQC", STARSButtonFull)
		STARSDisabledButton("MCP", STARSButtonFull)
		STARSDisabledButton("DCP\nTOP", STARSButtonHalfVertical)
		STARSDisabledButton("DCP\nLEFT", STARSButtonHalfVertical)
		STARSDisabledButton("DCP\nRIGHT", STARSButtonHalfVertical)
		STARSDisabledButton("DCP\nBOTTOM", STARSButtonHalfVertical)
		STARSFloatSpinner("PTL\nLNTH\n", &ps.PTLLength, 0.1, 20, STARSButtonFull)
		STARSToggleButton("PTL OWN", &ps.PTLOwn, STARSButtonHalfVertical)
		STARSToggleButton("PTL ALL", &ps.PTLAll, STARSButtonHalfVertical)
		if STARSSelectButton("SHIFT", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuMaps:
		STARSDisabledButton("MAPS", STARSButtonFull)
		if STARSSelectButton("DONE", STARSButtonHalfVertical) {
			sp.activeDCBMenu = DCBMenuMain
		}
		if STARSSelectButton("CLR ALL", STARSButtonHalfVertical) {
			ps.VideoMapVisible = make(map[string]interface{})
		}
		for i := 0; i < NumSTARSMaps; i++ {
			if i >= len(scenarioGroup.STARSMaps) {
				STARSDisabledButton(fmt.Sprintf(" %d", i+1), STARSButtonHalfVertical)
			} else {
				name := scenarioGroup.STARSMaps[i].Name
				_, visible := ps.VideoMapVisible[name]
				if STARSToggleButton(scenarioGroup.STARSMaps[i].Label, &visible, STARSButtonHalfVertical) {
					if visible {
						ps.VideoMapVisible[name] = nil
					} else {
						delete(ps.VideoMapVisible, name)
					}
				}
			}
		}
		STARSToggleButton("GEO\nMAPS", &ps.VideoMapsList.Visible, STARSButtonHalfVertical)
		STARSDisabledButton("DANGER\nAREAS", STARSButtonHalfVertical)
		if STARSSelectButton("SYS\nPROC", STARSButtonHalfVertical) {
			// TODO--this is a toggle that displays a "PROCESSING AREAS"
			// list in the top middle of the screen.
		}
		STARSToggleButton("CURRENT", &ps.ListSelectedMaps, STARSButtonHalfVertical)

	case DCBMenuBrite:
		STARSDisabledButton("BRITE", STARSButtonFull)
		STARSDisabledButton("DCB 100", STARSButtonHalfVertical)
		STARSDisabledButton("BKC 100", STARSButtonHalfVertical)
		STARSBrightnessSpinner("MPA ", &ps.Brightness.VideoGroupA, STARSButtonHalfVertical)
		STARSBrightnessSpinner("MPB ", &ps.Brightness.VideoGroupB, STARSButtonHalfVertical)
		STARSBrightnessSpinner("FDB ", &ps.Brightness.FullDatablocks, STARSButtonHalfVertical)
		STARSBrightnessSpinner("LST ", &ps.Brightness.Lists, STARSButtonHalfVertical)
		STARSBrightnessSpinner("POS ", &ps.Brightness.Positions, STARSButtonHalfVertical)
		STARSBrightnessSpinner("LDB ", &ps.Brightness.LimitedDatablocks, STARSButtonHalfVertical)
		STARSBrightnessSpinner("OTH ", &ps.Brightness.OtherTracks, STARSButtonHalfVertical)
		STARSBrightnessSpinner("TLS ", &ps.Brightness.Lines, STARSButtonHalfVertical)
		STARSBrightnessSpinner("RR ", &ps.Brightness.RangeRings, STARSButtonHalfVertical)
		STARSBrightnessSpinner("CMP ", &ps.Brightness.Compass, STARSButtonHalfVertical)
		STARSBrightnessSpinner("BCN ", &ps.Brightness.BeaconSymbols, STARSButtonHalfVertical)
		STARSBrightnessSpinner("PRI ", &ps.Brightness.PrimarySymbols, STARSButtonHalfVertical)
		STARSBrightnessSpinner("HST ", &ps.Brightness.History, STARSButtonHalfVertical)
		STARSDisabledButton("WX 100", STARSButtonHalfVertical)
		STARSDisabledButton("WXC 100", STARSButtonHalfVertical)
		STARSDisabledButton("", STARSButtonHalfVertical)
		if STARSSelectButton("DONE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuCharSize:
		STARSDisabledButton("BRITE", STARSButtonFull)
		STARSIntSpinner(" DATA\nBLOCKS\n  ", &ps.CharSize.Datablocks, 0, 5, STARSButtonFull)
		STARSIntSpinner("LISTS\n  ", &ps.CharSize.Lists, 0, 5, STARSButtonFull)
		STARSDisabledButton("DCB\n 1", STARSButtonFull)
		STARSIntSpinner("TOOLS\n  ", &ps.CharSize.Tools, 0, 5, STARSButtonFull)
		STARSIntSpinner("POS\n ", &ps.CharSize.PositionSymbols, 0, 5, STARSButtonFull)
		if STARSSelectButton("DONE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuPref:
		for i := range sp.PreferenceSets {
			text := fmt.Sprintf("%d\n%s", i+1, sp.PreferenceSets[i].Name)
			flags := STARSButtonHalfVertical
			if i == sp.SelectedPreferenceSet {
				flags = flags | STARSButtonSelected
			}
			if STARSSelectButton(text, flags) {
				// Make this one current
				sp.SelectedPreferenceSet = i
				sp.CurrentPreferenceSet = sp.PreferenceSets[i]
			}
		}
		for i := len(sp.PreferenceSets); i < NumSTARSPreferenceSets; i++ {
			STARSDisabledButton(fmt.Sprintf("%d\n", i+1), STARSButtonHalfVertical)
		}

		if STARSSelectButton("DEFAULT", STARSButtonHalfVertical) {
			sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility)
		}
		STARSDisabledButton("FSSTARS", STARSButtonHalfVertical)
		if STARSSelectButton("RESTORE", STARSButtonHalfVertical) {
			// TODO: restore settings in effect when entered the Pref sub-menu
		}

		validSelection := sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets)
		if validSelection {
			if STARSSelectButton("SAVE", STARSButtonHalfVertical) {
				sp.PreferenceSets[sp.SelectedPreferenceSet] = sp.CurrentPreferenceSet
				globalConfig.Save()
			}
		} else {
			STARSDisabledButton("SAVE", STARSButtonHalfVertical)
		}
		STARSDisabledButton("CHG PIN", STARSButtonHalfVertical)
		if STARSSelectButton("SAVE AS", STARSButtonHalfVertical) {
			// A command mode handles prompting for the name and then saves
			// when enter is pressed.
			sp.commandMode = CommandModeSavePrefAs
		}
		if validSelection {
			if STARSSelectButton("DELETE", STARSButtonHalfVertical) {
				sp.PreferenceSets = DeleteSliceElement(sp.PreferenceSets, sp.SelectedPreferenceSet)
			}
		} else {
			STARSDisabledButton("DELETE", STARSButtonHalfVertical)
		}

		if STARSSelectButton("DONE", STARSButtonHalfVertical) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSite:
		for _, id := range SortedMapKeys(scenarioGroup.RadarSites) {
			site := scenarioGroup.RadarSites[id]
			label := " " + site.Char + " " + "\n" + id
			selected := ps.RadarSiteSelected == id
			if STARSToggleButton(label, &selected, STARSButtonFull) {
				if selected {
					ps.RadarSiteSelected = id
				} else {
					ps.RadarSiteSelected = ""
				}
			}
		}
		multi := sp.multiRadarMode()
		if STARSToggleButton("MULTI", &multi, STARSButtonFull) && multi {
			ps.RadarSiteSelected = ""
		}
		if STARSSelectButton("DONE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSSAFilter:
		STARSToggleButton("All", &ps.SSAList.Filter.All, STARSButtonHalfVertical)
		STARSDisabledButton("WX", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("TIME", &ps.SSAList.Filter.Time, STARSButtonHalfVertical)
		STARSToggleButton("ALTSTG", &ps.SSAList.Filter.Altimeter, STARSButtonHalfVertical)
		STARSToggleButton("STATUS", &ps.SSAList.Filter.Status, STARSButtonHalfVertical)
		STARSDisabledButton("PLAN", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("RADAR", &ps.SSAList.Filter.Radar, STARSButtonHalfVertical)
		STARSToggleButton("CODES", &ps.SSAList.Filter.Codes, STARSButtonHalfVertical)
		STARSToggleButton("SPC", &ps.SSAList.Filter.SpecialPurposeCodes, STARSButtonHalfVertical)
		STARSDisabledButton("SYS OFF", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("RANGE", &ps.SSAList.Filter.Range, STARSButtonHalfVertical)
		STARSToggleButton("PTL", &ps.SSAList.Filter.PredictedTrackLines, STARSButtonHalfVertical)
		STARSToggleButton("ALT FIL", &ps.SSAList.Filter.AltitudeFilters, STARSButtonHalfVertical)
		STARSDisabledButton("NAS I/F", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("AIRPORT", &ps.SSAList.Filter.AirportWeather, STARSButtonHalfVertical)
		STARSDisabledButton("OP MODE", STARSButtonHalfVertical) // ?? TODO
		STARSDisabledButton("TT", STARSButtonHalfVertical)      // ?? TODO
		STARSDisabledButton("WX HIST", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("QL", &ps.SSAList.Filter.QuickLookPositions, STARSButtonHalfVertical)
		STARSToggleButton("TW OFF", &ps.SSAList.Filter.DisabledTerminal, STARSButtonHalfVertical)
		STARSDisabledButton("CON/CPL", STARSButtonHalfVertical) // ?? TODO
		STARSDisabledButton("OFF IND", STARSButtonHalfVertical) // ?? TODO
		STARSToggleButton("CRDA", &ps.SSAList.Filter.ActiveCRDAPairs, STARSButtonHalfVertical)
		STARSDisabledButton("", STARSButtonHalfVertical)
		if STARSSelectButton("DONE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuGITextFilter:
		STARSToggleButton("MAIN", &ps.SSAList.Filter.Text.Main, STARSButtonHalfVertical)
		for i := range ps.SSAList.Filter.Text.GI {
			STARSToggleButton(fmt.Sprintf("GI %d", i+1), &ps.SSAList.Filter.Text.GI[i],
				STARSButtonHalfVertical)
		}
		if STARSSelectButton("DONE", STARSButtonFull) {
			sp.activeDCBMenu = DCBMenuMain
		}
	}

	sp.EndDrawDCB()
}

func (sp *STARSPane) drawSystemLists(aircraft []*Aircraft, ctx *PaneContext,
	transforms ScopeTransformations, cb *CommandBuffer) {
	for name := range scenarioGroup.Airports {
		sim.AddAirportForWeather(name)
	}

	ps := sp.CurrentPreferenceSet

	transforms.LoadWindowViewingMatrices(cb)

	font := sp.systemFont[ps.CharSize.Lists]
	style := TextStyle{
		Font:       font,
		Color:      ps.Brightness.Lists.ScaleRGB(STARSListColor),
		DropShadow: true,
	}
	alertStyle := TextStyle{
		Font:       font,
		Color:      ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor),
		DropShadow: true,
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	normalizedToWindow := func(p [2]float32) [2]float32 {
		if ps.DisplayDCB {
			return [2]float32{p[0] * ctx.paneExtent.Width(), p[1] * (ctx.paneExtent.Height() - STARSButtonHeight)}
		} else {
			return [2]float32{p[0] * ctx.paneExtent.Width(), p[1] * ctx.paneExtent.Height()}
		}
	}
	drawList := func(text string, p [2]float32) {
		if text != "" {
			pw := normalizedToWindow(p)
			td.AddText(text, pw, style)
		}
	}

	// Do the preview area while we're at it
	pt := sp.previewAreaOutput + "\n"
	switch sp.commandMode {
	case CommandModeInitiateControl:
		pt += "IC\n"
	case CommandModeTerminateControl:
		pt += "TC\n"
	case CommandModeHandOff:
		pt += "HD\n"
	case CommandModeVP:
		pt += "VP\n"
	case CommandModeMultiFunc:
		pt += "F" + sp.multiFuncPrefix + "\n"
	case CommandModeFlightData:
		pt += "DA\n"
	case CommandModeCollisionAlert:
		pt += "CA\n"
	case CommandModeMin:
		pt += "MIN\n"
	case CommandModeMaps:
		pt += "MAP\n"
	case CommandModeSavePrefAs:
		pt += "SAVE AS\n"
	case CommandModeLDR:
		pt += "LLL\n"
	case CommandModeRangeRings:
		pt += "RR\n"
	case CommandModeRange:
		pt += "RANGE\n"
	case CommandModeSiteMenu:
		pt += "SITE\n"
	}
	pt += strings.Join(strings.Fields(sp.previewAreaInput), "\n") // spaces are rendered as newlines
	drawList(pt, ps.PreviewAreaPosition)

	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	formatMETAR := func(ap string, metar *METAR) string {
		alt := strings.TrimPrefix(metar.Altimeter, "A")
		if len(alt) == 4 {
			alt = alt[:2] + "." + alt[2:]
		}
		wind := strings.TrimSuffix(metar.Wind, "KT")
		return stripK(ap) + " " + alt + " " + wind
	}

	if ps.SSAList.Visible {
		pw := normalizedToWindow(ps.SSAList.Position)
		x := pw[0]
		newline := func() {
			pw[0] = x
			pw[1] -= float32(font.size)
		}

		// Inverted red triangle and green box...
		trid := GetColoredTrianglesDrawBuilder()
		defer ReturnColoredTrianglesDrawBuilder(trid)
		ld := GetColoredLinesDrawBuilder()
		defer ReturnColoredLinesDrawBuilder(ld)

		pIndicator := add2f(pw, [2]float32{5, 0})
		tv := EquilateralTriangleVertices(7)
		for i := range tv {
			tv[i] = add2f(pIndicator, scale2f(tv[i], -1))
		}
		trid.AddTriangle(tv[0], tv[1], tv[2], ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor))
		trid.GenerateCommands(cb)

		square := [4][2]float32{[2]float32{-5, -5}, [2]float32{5, -5}, [2]float32{5, 5}, [2]float32{-5, 5}}
		ld.AddPolyline(pIndicator, ps.Brightness.Lists.ScaleRGB(STARSListColor), square[:])
		ld.GenerateCommands(cb)

		pw[1] -= 10

		filter := ps.SSAList.Filter
		if filter.All || filter.Time || filter.Altimeter {
			text := ""
			if filter.All || filter.Time {
				text += sim.CurrentTime().UTC().Format("1504/05 ")
			}
			if filter.All || filter.Altimeter {
				if metar := sim.GetMETAR(scenarioGroup.PrimaryAirport); metar != nil {
					text += formatMETAR(scenarioGroup.PrimaryAirport, metar)
				}
			}
			td.AddText(text, pw, style)
			newline()
		}

		// ATIS and GI text always, apparently
		if ps.CurrentATIS != "" {
			pw = td.AddText(ps.CurrentATIS+" "+ps.GIText[0], pw, style)
			newline()
		} else if ps.GIText[0] != "" {
			pw = td.AddText(ps.GIText[0], pw, style)
			newline()
		}
		for i := 1; i < len(ps.GIText); i++ {
			if txt := ps.GIText[i]; txt != "" {
				pw = td.AddText(txt, pw, style)
				newline()
			}
		}

		if filter.All || filter.Status || filter.Radar {
			if filter.All || filter.Status {
				if sim.Connected() {
					pw = td.AddText("OK/OK/NA ", pw, style)
				} else {
					pw = td.AddText("NA/NA/NA ", pw, alertStyle)
				}
			}
			if filter.All || filter.Radar {
				pw = td.AddText(sp.radarSiteId(), pw, style)
			}
			newline()
		}

		if filter.All || filter.Codes {
			if len(ps.SelectedBeaconCodes) > 0 {
				pw = td.AddText(strings.Join(ps.SelectedBeaconCodes, " "), pw, style)
				newline()
			}
		}

		if filter.All || filter.SpecialPurposeCodes {
			// Special purpose codes listed in red, if anyone is squawking
			// those.
			var hj, rf, em, mi bool
			for _, ac := range aircraft {
				state := sp.aircraft[ac]
				if ac.Squawk == Squawk(0o7500) || state.spcOverride == "HJ" {
					hj = true
				} else if ac.Squawk == Squawk(0o7600) || state.spcOverride == "RF" {
					rf = true
				} else if ac.Squawk == Squawk(0o7700) || state.spcOverride == "EM" {
					em = true
				} else if ac.Squawk == Squawk(0o7777) || state.spcOverride == "MI" {
					mi = true
				}
			}

			var codes []string
			if hj {
				codes = append(codes, "HJ")
			}
			if rf {
				codes = append(codes, "RF")
			}
			if em {
				codes = append(codes, "EM")
			}
			if mi {
				codes = append(codes, "MI")
			}
			if len(codes) > 0 {
				td.AddText(strings.Join(codes, " "), pw, alertStyle)
				newline()
			}
		}

		if filter.All || filter.Range || filter.PredictedTrackLines {
			text := ""
			if filter.All || filter.Range {
				text += fmt.Sprintf("%dNM ", int(ps.Range))
			}
			if filter.All || filter.PredictedTrackLines {
				text += fmt.Sprintf("PTL: %.1f", ps.PTLLength)
			}
			pw = td.AddText(text, pw, style)
			newline()
		}

		if filter.All || filter.AltitudeFilters {
			af := ps.AltitudeFilters
			text := fmt.Sprintf("%03d %03d U %03d %03d A",
				af.Unassociated[0]/100, af.Unassociated[1]/100,
				af.Associated[0]/100, af.Associated[1]/100)
			pw = td.AddText(text, pw, style)
			newline()
		}

		if filter.All || filter.AirportWeather {
			var lines []string
			airports, _ := FlattenMap(scenarioGroup.Airports)
			// Sort via 1. primary? 2. tower list index, 3. alphabetic
			sort.Slice(airports, func(i, j int) bool {
				a, b := scenarioGroup.Airports[airports[i]], scenarioGroup.Airports[airports[j]]
				if airports[i] == scenarioGroup.PrimaryAirport {
					return true
				} else if airports[j] == scenarioGroup.PrimaryAirport {
					return false
				} else if a.TowerListIndex != 0 && b.TowerListIndex == 0 {
					return true
				} else if b.TowerListIndex != 0 && a.TowerListIndex == 0 {
					return false
				}
				return airports[i] < airports[j]
			})

			for _, icao := range airports {
				if metar := sim.GetMETAR(icao); metar != nil {
					lines = append(lines, formatMETAR(icao, metar))
				}
			}
			if len(lines) > 0 {
				pw = td.AddText(strings.Join(lines, "\n"), pw, style)
				newline()
			}
		}

		if filter.All || filter.QuickLookPositions {
			// TODO
			// QL: ALL, etc, depending on quicklook setting (or a specific controller)
		}

		if filter.All || filter.DisabledTerminal {
			var disabled []string
			if ps.DisableCAWarnings {
				disabled = append(disabled, "CA")
			}
			if ps.DisableCRDA {
				disabled = append(disabled, "CRDA")
			}
			if ps.DisableMSAW {
				disabled = append(disabled, "MSAW")
			}
			// TODO: others?
			if len(disabled) > 0 {
				text := "TW OFF: " + strings.Join(disabled, " ")
				pw = td.AddText(text, pw, style)
				newline()
			}
		}

		if filter.All || filter.ActiveCRDAPairs {
			// TODO
		}
	}

	if ps.VFRList.Visible {
		vfr := make(map[int]*Aircraft)
		// Find all untracked VFR aircraft
		for _, ac := range aircraft {
			if ac.Squawk == Squawk(0o1200) && ac.TrackingController == "" {
				vfr[sp.getAircraftIndex(ac)] = ac
			}
		}

		text := "VFR LIST\n"
		if len(vfr) > ps.VFRList.Lines {
			text += fmt.Sprintf("MORE: %d/%d\n", ps.VFRList.Lines, len(vfr))
		}
		for i, acIdx := range SortedMapKeys(vfr) {
			ac := vfr[acIdx]
			text += fmt.Sprintf("%2d %-7s VFR\n", acIdx, ac.Callsign)

			// Limit to the user limit
			if i == ps.VFRList.Lines {
				break
			}
		}

		drawList(text, ps.VFRList.Position)
	}

	if ps.TABList.Visible {
		dep := make(map[int]*Aircraft)
		// Untracked departures departing from one of our airports
		for _, ac := range aircraft {
			if fp := ac.FlightPlan; fp != nil && ac.TrackingController == "" {
				for ap := range scenarioGroup.Airports {
					if fp.DepartureAirport == ap {
						dep[sp.getAircraftIndex(ac)] = ac
						break
					}
				}
			}
		}

		text := "FLIGHT PLAN\n"
		if len(dep) > ps.TABList.Lines {
			text += fmt.Sprintf("MORE: %d/%d\n", ps.TABList.Lines, len(dep))
		}
		for i, acIdx := range SortedMapKeys(dep) {
			ac := dep[acIdx]
			text += fmt.Sprintf("%2d %-7s %s\n", acIdx, ac.Callsign, ac.Squawk.String())

			// Limit to the user limit
			if i == ps.TABList.Lines {
				break
			}
		}

		drawList(text, ps.TABList.Position)
	}

	if ps.AlertList.Visible {
		text := "LA/CA/MCI"
		// TODO
		drawList(text, ps.AlertList.Position)
	}

	if ps.CoastList.Visible {
		text := "COAST/SUSPEND"
		// TODO
		drawList(text, ps.CoastList.Position)
	}

	if ps.VideoMapsList.Visible {
		text := "GEOGRAPHIC MAPS"
		// TODO
		drawList(text, ps.VideoMapsList.Position)
	}

	if ps.CRDAStatusList.Visible {
		text := "CRDA STATUS"
		// TODO
		drawList(text, ps.CRDAStatusList.Position)
	}

	for i, tl := range ps.TowerLists {
		if !tl.Visible {
			continue
		}

		for name, ap := range scenarioGroup.Airports {
			if ap.TowerListIndex == i+1 {
				text := stripK(name) + " TOWER\n"
				m := make(map[float32]string)
				for _, ac := range aircraft {
					if ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == name {
						dist := nmdistance2ll(ap.Location, ac.TrackPosition())
						actype := ac.FlightPlan.TypeWithoutSuffix()
						actype = strings.TrimPrefix(actype, "H/")
						actype = strings.TrimPrefix(actype, "S/")
						// We'll punt on the chance that two aircraft have the
						// exact same distance to the airport...
						m[dist] = fmt.Sprintf("%-7s %s", ac.Callsign, actype)
					}
				}

				k := SortedMapKeys(m)
				if len(k) > tl.Lines {
					k = k[:tl.Lines]
				}

				for _, key := range k {
					text += m[key] + "\n"
				}
				drawList(text, tl.Position)
			}
		}
	}

	if ps.SignOnList.Visible {
		format := func(ctrl *Controller, requirePosition bool) string {
			return fmt.Sprintf("%3s", ctrl.SectorId) + " " + ctrl.Frequency.String() + " " + ctrl.Callsign
		}

		// User first
		text := ""
		userCtrl := sim.GetController(sim.Callsign())
		if userCtrl != nil {
			text += format(userCtrl, false) + "\n"
		}

		ctrl := sim.GetAllControllers()
		sort.Slice(ctrl, func(i, j int) bool { return ctrl[i].Callsign < ctrl[j].Callsign })
		for _, c := range ctrl {
			if c != userCtrl {
				if ctext := format(c, true); ctext != "" {
					text += ctext + "\n"
				}
			}
		}

		drawList(text, ps.SignOnList.Position)
	}

	td.GenerateCommands(cb)
}

func (sp *STARSPane) datablockType(ac *Aircraft) DatablockType {
	state := sp.aircraft[ac]
	dt := state.datablockType

	// TODO: when do we do a partial vs limited datablock?
	if ac.Squawk != ac.AssignedSquawk {
		dt = PartialDatablock
	}

	if ac.InboundHandoffController == sim.Callsign() {
		// it's being handed off to us
		dt = FullDatablock
	}

	return dt
}

func (sp *STARSPane) drawTracks(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	pd := PointsDrawBuilder{}
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	trid := GetColoredTrianglesDrawBuilder()
	defer ReturnColoredTrianglesDrawBuilder(trid)

	// TODO: square icon if it's squawking a beacon code we're monitoring

	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.PositionSymbols]

	now := sim.CurrentTime()
	for _, ac := range aircraft {
		if ac.LostTrack(now) {
			continue
		}

		brightness := ps.Brightness.Positions

		dt := sp.datablockType(ac)

		if dt == PartialDatablock || dt == LimitedDatablock {
			brightness = ps.Brightness.LimitedDatablocks
		}

		pos := ac.TrackPosition()
		pw := transforms.WindowFromLatLongP(pos)
		// TODO: orient based on radar center if just one radar
		orientation := ac.TrackHeading()
		if math.IsNaN(float64(orientation)) {
			orientation = 0
		}
		rot := rotator2f(orientation)

		// blue box: x +/-9 pixels, y +/-3 pixels
		// TODO: size based on distance to radar, if not MULTI
		box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}
		for i := range box {
			box[i] = add2f(rot(box[i]), pw)
			box[i] = transforms.LatLongFromWindowP(box[i])
		}
		color := brightness.ScaleRGB(STARSTrackBlockColor)
		primary, secondary, _ := sp.radarVisibility(ac.TrackPosition(), ac.TrackAltitude())
		if primary {
			// Draw a filled box
			trid.AddQuad(box[0], box[1], box[2], box[3], color)
		} else if secondary {
			// If it's just a secondary return, only draw the box outline.
			// TODO: is this 40nm, or secondary?
			ld.AddPolyline([2]float32{}, color, box[:])
		}

		if !sp.multiRadarMode() {
			// green line
			// TODO: size based on distance to radar
			line := [2][2]float32{[2]float32{-16, -3}, [2]float32{16, -3}}
			for i := range line {
				line[i] = add2f(rot(line[i]), pw)
				line[i] = transforms.LatLongFromWindowP(line[i])
			}
			ld.AddLine(line[0], line[1], brightness.ScaleRGB(RGB{R: .1, G: .8, B: .1}))
		}

		state := sp.aircraft[ac]
		if state.isGhost {
			// TODO: handle
			// color = ctx.cs.GhostDatablock
		}

		// Draw main track symbol letter
		if ac.TrackingController != "" {
			ch := "?"
			if ctrl := sim.GetController(ac.TrackingController); ctrl != nil {
				ch = ctrl.Scope
			}
			td.AddTextCentered(ch, pw, TextStyle{Font: font, Color: brightness.RGB(), DropShadow: true})
		} else {
			// TODO: draw box if in range of squawks we have selected

			// diagonals
			dx := transforms.LatLongFromWindowV([2]float32{1, 0})
			dy := transforms.LatLongFromWindowV([2]float32{0, 1})
			// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
			delta := func(p Point2LL, x, y float32) Point2LL {
				return add2ll(p, add2ll(scale2f(dx, x), scale2f(dy, y)))
			}

			px := float32(3)
			// diagonals
			diagPx := px * 0.707107                                     /* 1/sqrt(2) */
			trackColor := brightness.ScaleRGB(RGB{R: .1, G: .7, B: .1}) // TODO make a STARS... constant
			ld.AddLine(delta(pos, -diagPx, -diagPx), delta(pos, diagPx, diagPx), trackColor)
			ld.AddLine(delta(pos, diagPx, -diagPx), delta(pos, -diagPx, diagPx), trackColor)
			// horizontal line
			ld.AddLine(delta(pos, -px, 0), delta(pos, px, 0), trackColor)
			// vertical line
			ld.AddLine(delta(pos, 0, -px), delta(pos, 0, px), trackColor)
		}

		// Draw in reverse order so that if it's not moving, more recent tracks (which will have
		// more contrast with the background), will be the ones that are visible.
		histColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColor)
		n := ps.RadarTrackHistory
		for i := n; i > 1; i-- {
			// blend the track color with the background color; more
			// background further into history but only a 50/50 blend
			// at the oldest track.
			// 1e-6 addition to avoid NaN with RadarTrackHistory == 1.
			x := float32(i-1) / (1e-6 + float32(2*(n-1))) // 0 <= x <= 0.5
			trackColor := lerpRGB(x, histColor, STARSBackgroundColor)

			p := ac.Tracks[i-1].Position

			pd.AddPoint(p, trackColor)
		}
	}

	transforms.LoadLatLongViewingMatrices(cb)
	cb.PointSize(5)
	pd.GenerateCommands(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) updateDatablockTextAndPosition(aircraft []*Aircraft) {
	now := sim.CurrentTime()
	font := sp.systemFont[sp.CurrentPreferenceSet.CharSize.Datablocks]

	for _, ac := range aircraft {
		if ac.LostTrack(now) || !sp.datablockVisible(ac) {
			continue
		}

		state := sp.aircraft[ac]
		state.datablockErrText, state.datablockText = sp.formatDatablock(ac)

		// For westerly directions the datablock text should be right
		// justified, since the leader line will be connecting on that
		// side.
		dir := sp.getLeaderLineDirection(ac)
		rightJustify := dir > South
		if rightJustify {
			maxLen := 0
			for i := 0; i < 2; i++ {
				for j := range state.datablockText[i] {
					state.datablockText[i][j] = strings.TrimSpace(state.datablockText[i][j])
					maxLen = max(maxLen, len(state.datablockText[i][j]))
				}
			}

			justify := func(s string) string {
				if len(s) == maxLen {
					return s
				}
				return fmt.Sprintf("%*c", maxLen-len(s), ' ') + s
			}
			for i := 0; i < 2; i++ {
				for j := range state.datablockText[i] {
					state.datablockText[i][j] = justify(state.datablockText[i][j])
				}
			}
		}

		// Compute the bounds of the datablock; it's fine to use just one of them here,.
		var text []string
		if state.datablockErrText != "" {
			text = append(text, state.datablockErrText)
		}
		text = append(text, state.datablockText[0]...)
		w, h := font.BoundText(strings.Join(text, "\n"), -2)

		// To place the datablock, start with the vector for the leader line.
		state.datablockDrawOffset = sp.getLeaderLineVector(ac)

		// And now fine-tune so that e.g., for East, the datablock is
		// vertically aligned with the track line. (And similarly for other
		// directions...)
		bw, bh := float32(w), float32(h)
		switch dir {
		case North:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{-bw / 2, bh})
		case NorthEast, East, SouthEast:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{0, bh / 2})
		case South:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{-bw / 2, 0})
		case SouthWest, West, NorthWest:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{-bw, bh / 2})
		}
	}

}

func (sp *STARSPane) OutsideAirspace(ac *Aircraft) (alts [][2]int, outside bool) {
	// Only report on ones that are tracked by us
	if ac.TrackingController != sim.Callsign() {
		return
	}

	if _, ok := sim.DepartureAirports()[ac.FlightPlan.DepartureAirport]; ok {
		if len(sim.Scenario.DepartureAirspace) > 0 {
			inDepartureAirspace, depAlts := InAirspace(ac.Position, ac.Altitude, sim.Scenario.DepartureAirspace)
			if !ac.HaveEnteredAirspace {
				ac.HaveEnteredAirspace = inDepartureAirspace
			} else {
				alts = depAlts
				outside = !inDepartureAirspace
			}
		}
	} else if _, ok := sim.ArrivalAirports()[ac.FlightPlan.ArrivalAirport]; ok {
		if len(sim.Scenario.ApproachAirspace) > 0 {
			inApproachAirspace, depAlts := InAirspace(ac.Position, ac.Altitude, sim.Scenario.ApproachAirspace)
			if !ac.HaveEnteredAirspace {
				ac.HaveEnteredAirspace = inApproachAirspace
			} else {
				alts = depAlts
				outside = !inApproachAirspace
			}
		}
	} else {
		lg.Errorf("%s: neither a departure nor an arrival??!? %s", ac.Callsign, spew.Sdump(ac.FlightPlan))
	}
	return
}

func (sp *STARSPane) IsCAActive(ac *Aircraft) bool {
	if ac.TrackAltitude() < int(sp.Facility.CA.Floor) {
		return false
	}

	for other := range sp.aircraft {
		if other == ac || other.TrackAltitude() < int(sp.Facility.CA.Floor) {
			continue
		}

		// No conflict alerts with aircraft established on different
		// approaches to the same airport within 3 miles of the airport
		if ac.FlightPlan.ArrivalAirport == other.FlightPlan.ArrivalAirport &&
			ac.ApproachId != "" && other.ApproachId != "" &&
			ac.ApproachId != other.ApproachId {
			d, err := ac.FinalApproachDistance()
			od, oerr := other.FinalApproachDistance()
			if err == nil && oerr == nil && (d < 3 || od < 3) {
				continue
			}
		}

		// No conflict alerts with another aircraft on an approach if we're
		// departing (assume <1000' and no assigned approach implies this)
		if ac.Approach == nil && ac.Altitude < 1000 && other.Approach != nil {
			continue
		}
		// Converse of the above
		if ac.Approach != nil && other.Altitude < 1000 && other.Approach == nil {
			continue
		}

		if nmdistance2ll(ac.TrackPosition(), other.TrackPosition()) <= sp.Facility.CA.LateralMinimum &&
			abs(ac.TrackAltitude()-other.TrackAltitude()) <= int(sp.Facility.CA.VerticalMinimum-50 /*small slop for fp error*/) {
			return true
		}
	}
	return false
}

func (sp *STARSPane) formatDatablock(ac *Aircraft) (errblock string, mainblock [2][]string) {
	state := sp.aircraft[ac]

	var errs []string
	if ac.Squawk == Squawk(0o7500) || state.spcOverride == "HJ" {
		errs = append(errs, "HJ")
	} else if ac.Squawk == Squawk(0o7600) || state.spcOverride == "RF" {
		errs = append(errs, "RF")
	} else if ac.Squawk == Squawk(0o7700) || state.spcOverride == "EM" {
		errs = append(errs, "EM")
	} else if ac.Squawk == Squawk(0o7777) || state.spcOverride == "MI" {
		errs = append(errs, "MI")
	} else if ac.Squawk == Squawk(0o1236) {
		errs = append(errs, "SA")
	}
	if sp.IsCAActive(ac) {
		errs = append(errs, "CA")
	}
	if alts, outside := sp.OutsideAirspace(ac); outside {
		altStrs := ""
		for _, a := range alts {
			altStrs += fmt.Sprintf("/%d-%d", a[0]/100, a[1]/100)
		}
		errs = append(errs, "AS"+altStrs)
	}
	// TODO: LA
	errblock = strings.Join(errs, "/") // want e.g., EM/LA if multiple things going on

	if ac.Mode == Standby {
		return
	}

	ty := sp.datablockType(ac)

	switch ty {
	case LimitedDatablock:
		mainblock[0] = append(mainblock[0], "TODO LIMITED DATABLOCK")
		mainblock[1] = append(mainblock[1], "TODO LIMITED DATABLOCK")

	case PartialDatablock:
		if ac.Squawk != ac.AssignedSquawk {
			sq := ac.Squawk.String()
			mainblock[0] = append(mainblock[0], sq)
			mainblock[1] = append(mainblock[1], sq+"WHO")
		}

		// Unassociated with LDB should be 2 lines: squawk, altitude--unless
		// beacon codes are inhibited in LDBs.

		if fp := ac.FlightPlan; fp != nil && fp.Rules == IFR {
			// Alternate between altitude and either scratchpad or destination airport.
			mainblock[0] = append(mainblock[0], fmt.Sprintf("%03d", (ac.TrackAltitude()+50)/100))
			if ac.Scratchpad != "" {
				mainblock[1] = append(mainblock[1], ac.Scratchpad)
			} else {
				mainblock[1] = append(mainblock[1], fp.ArrivalAirport)
			}
		} else {
			as := fmt.Sprintf("%03d  %02d", (ac.TrackAltitude()+50)/100, (ac.TrackGroundspeed()+5)/10)
			mainblock[0] = append(mainblock[0], as)
			mainblock[1] = append(mainblock[1], as)
		}
		return

	case FullDatablock:
		// First line; the same for both.
		cs := ac.Callsign
		// TODO: draw triangle after callsign if conflict alerts inhibited
		// TODO: space then asterisk after callsign if MSAW inhibited

		if ac.Mode == Ident {
			cs += " ID"
		}
		if _, ok := sp.pointedOutAircraft.Get(ac); ok {
			cs += " PO"
		}
		mainblock[0] = append(mainblock[0], cs)
		mainblock[1] = append(mainblock[1], cs)

		// Second line of the non-error datablock
		ho := "  "
		if ac.InboundHandoffController != "" {
			if ctrl := sim.GetController(ac.InboundHandoffController); ctrl != nil {
				ho = ctrl.SectorId
			}
		} else if ac.OutboundHandoffController != "" {
			if ctrl := sim.GetController(ac.OutboundHandoffController); ctrl != nil {
				ho = ctrl.SectorId
			}
		}

		// Altitude and speed: mainblock[0]
		alt := fmt.Sprintf("%03d", (ac.TrackAltitude()+50)/100)
		if ac.LostTrack(sim.CurrentTime()) {
			alt = "CST"
		}
		speed := fmt.Sprintf("%02d", (ac.TrackGroundspeed()+5)/10)
		// TODO: pilot reported altitude. Asterisk after alt when showing.
		mainblock[0] = append(mainblock[0], alt+ho+speed)

		// mainblock[1]
		arrscr := ac.FlightPlan.ArrivalAirport
		if ac.Scratchpad != "" {
			arrscr = ac.Scratchpad
		}

		actype := ac.FlightPlan.TypeWithoutSuffix()
		suffix := ""
		if sp.isOverflight(ac) {
			suffix += "E"
		}
		if ac.FlightPlan.Rules == VFR {
			suffix += "V"
		} else if actype == "B757" {
			suffix += " F"
		} else if strings.HasPrefix(actype, "H/") {
			actype = strings.TrimPrefix(actype, "H/")
			suffix += " H"
		} else if strings.HasPrefix(actype, "S/") {
			actype = strings.TrimPrefix(actype, "S/")
			suffix += " J"
		} else if strings.HasPrefix(actype, "J/") {
			actype = strings.TrimPrefix(actype, "J/")
			suffix += " J"
		}

		mainblock[1] = append(mainblock[1], arrscr+ho+actype+suffix)
	}

	if ac.TempAltitude != 0 {
		ta := (ac.TempAltitude + 50) / 100
		tastr := fmt.Sprintf("     A%03d", ta)
		mainblock[0] = append(mainblock[0], tastr)
		mainblock[1] = append(mainblock[1], tastr)
	}

	return
}

func (sp *STARSPane) datablockColor(ac *Aircraft) RGB {
	// TODO: when do we use Brightness.LimitedDatablocks?
	ps := sp.CurrentPreferenceSet
	br := ps.Brightness.FullDatablocks
	state := sp.aircraft[ac]

	if _, ok := sp.pointedOutAircraft.Get(ac); ok {
		// yellow for pointed out
		return br.ScaleRGB(STARSPointedOutAircraftColor)
	} else if ac.TrackingController == sim.Callsign() {
		// white if we are tracking, unless it's selected
		if state.isSelected {
			return br.ScaleRGB(STARSSelectedAircraftColor)
		} else {
			return br.ScaleRGB(STARSTrackedAircraftColor)
		}
	} else if ac.InboundHandoffController == sim.Callsign() {
		// flashing white if it's being handed off to us.
		if time.Now().Second()&1 == 0 { // TODO: is a one second cycle right?
			br /= 3
		}
		return br.ScaleRGB(STARSTrackedAircraftColor)
	} else if state.outboundHandoffAccepted {
		// we handed it off, it was accepted, but we haven't yet acknowledged
		now := time.Now()
		if now.Before(state.outboundHandoffFlashEnd) && now.Second()&1 == 0 { // TODO: is a one second cycle right?
			// flash for 10 seconds after accept
			br /= 3
		}
		return br.ScaleRGB(STARSTrackedAircraftColor)
	}

	// green otherwise
	return br.ScaleRGB(STARSUntrackedAircraftColor)
}

func (sp *STARSPane) drawDatablocks(aircraft []*Aircraft, ctx *PaneContext,
	transforms ScopeTransformations, cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	now := sim.CurrentTime()
	realNow := time.Now() // for flashing rate...
	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Datablocks]

	for _, ac := range aircraft {
		if ac.LostTrack(now) || !sp.datablockVisible(ac) {
			continue
		}

		// TODO: blink for pointed out and squawk ident
		// or for inbound handoff
		if ac.Mode == Ident {
		}
		if _, ok := sp.pointedOutAircraft.Get(ac); ok {
		}

		state := sp.aircraft[ac]

		color := sp.datablockColor(ac)
		style := TextStyle{Font: font, Color: color, DropShadow: true, LineSpacing: -2}
		dbText := state.datablockText[(realNow.Second()/2)&1] // 2 second cycle

		// Draw characters starting at the upper left.
		pac := transforms.WindowFromLatLongP(ac.TrackPosition())
		pt := add2f(state.datablockDrawOffset, pac)
		if state.datablockErrText != "" {
			errorStyle := TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSTextAlertColor),
				LineSpacing: -2}
			pt = td.AddText(state.datablockErrText+"\n", pt, errorStyle)
		}
		td.AddText(strings.Join(dbText, "\n"), pt, style)

		// Leader line
		v := sp.getLeaderLineVector(ac)
		p0, p1 := add2f(pac, scale2f(v, .05)), add2f(pac, v)
		ld.AddLine(p0, p1, color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawPTLs(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ps := sp.CurrentPreferenceSet

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	now := sim.CurrentTime()
	for _, ac := range aircraft {
		if ac.LostTrack(now) || !ac.HaveHeading() {
			continue
		}
		state := sp.aircraft[ac]
		if !(state.displayPTL || ps.PTLAll || (ps.PTLOwn && ac.TrackingController == sim.Callsign())) {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(ac.TrackGroundspeed()) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := ac.TrackHeading() - scenarioGroup.MagneticVariation
		h := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		h = scale2f(h, dist)
		end := add2ll(ac.TrackPosition(), nm2ll(h))

		ld.AddLine(ac.TrackPosition(), end, color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	now := sim.CurrentTime()
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Tools]
	color := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)
	textStyle := TextStyle{Font: font, DrawBackground: true, Color: color}

	for _, ac := range aircraft {
		if ac.LostTrack(now) {
			continue
		}

		// Format a radius/length for printing, ditching the ".0" if it's
		// an integer value.
		format := func(v float32) string {
			if v == float32(int(v)) {
				return fmt.Sprintf("%d", int(v))
			} else {
				return fmt.Sprintf("%.1f", v)
			}
		}

		state := sp.aircraft[ac]
		if state.jRingRadius > 0 {
			const nsegs = 360
			pc := transforms.WindowFromLatLongP(ac.TrackPosition())
			radius := state.jRingRadius / transforms.PixelDistanceNM()
			ld.AddCircle(pc, radius, nsegs, color)

			if ps.DisplayTPASize || state.displayTPASize {
				// draw the ring size around 7.5 o'clock
				// vector from center to the circle there
				v := [2]float32{-.707106 * radius, -.707106 * radius} // -sqrt(2)/2
				// move up to make space for the text
				v[1] += float32(font.size) + 3
				pt := add2f(pc, v)
				td.AddText(format(state.jRingRadius), pt, textStyle)
			}
		}

		if state.coneLength > 0 && ac.HaveHeading() {
			// We'll draw in window coordinates. First figure out the
			// coordinates of the vertices of the cone triangle. We'll
			// start with a canonical triangle in nm coordinates, going one
			// unit up the +y axis with a small spread in x.
			v := [4][2]float32{[2]float32{0, 0}, [2]float32{-.04, 1}, [2]float32{.04, 1}}

			// Now we want to get that triangle in window coordinates...
			length := state.coneLength / transforms.PixelDistanceNM()
			rot := rotator2f(ac.TrackHeading())
			for i := range v {
				// First scale it to make it the desired length in nautical
				// miles; while we're at it, we'll convert that over to
				// window coordinates.
				v[i] = scale2f(v[i], length)
				// Now we just need to rotate it from the +y axis to be
				// aligned with the aircraft's heading.
				v[i] = rot(v[i])
			}

			// We've got what we need to draw a polyline with the
			// aircraft's position as an anchor.
			pw := transforms.WindowFromLatLongP(ac.TrackPosition())
			ld.AddPolyline(pw, color, v[:])

			if ps.DisplayTPASize || state.displayTPASize {
				ptext := add2f(pw, rot(scale2f([2]float32{0, 0.5}, length)))
				td.AddTextCentered(format(state.coneLength), ptext, textStyle)
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// Draw all of the range-bearing lines that have been specified.
func (sp *STARSPane) drawRBLs(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.Lines.RGB() // check
	style := TextStyle{
		Font:           sp.systemFont[ps.CharSize.Tools],
		Color:          color,
		DrawBackground: true, // default BackgroundColor is fine
	}

	for i, rbl := range sp.rangeBearingLines {
		// Each line endpoint may be specified either by an aircraft's
		// position or by a fixed position. We'll start with the fixed
		// position and then override it if there's a valid *Aircraft.
		p0, p1 := rbl.p[0].loc, rbl.p[1].loc
		if ac := rbl.p[0].ac; ac != nil {
			if ac.LostTrack(sim.CurrentTime()) || !sp.datablockVisible(ac) {
				continue
			}
			if _, ok := sp.aircraft[ac]; !ok {
				continue
			}
			p0 = ac.TrackPosition()
		}
		if ac := rbl.p[1].ac; ac != nil {
			if ac.LostTrack(sim.CurrentTime()) || !sp.datablockVisible(ac) {
				continue
			}
			if _, ok := sp.aircraft[ac]; !ok {
				continue
			}
			p1 = ac.TrackPosition()
		}

		// Format the range-bearing line text for the two positions.
		hdg := headingp2ll(p0, p1, scenarioGroup.MagneticVariation)
		dist := nmdistance2ll(p0, p1)
		text := fmt.Sprintf("%d/%.2f-%d", int(hdg+.5), dist, i+1)

		// And draw the line and the text.
		pText := transforms.WindowFromLatLongP(mid2ll(p0, p1))
		td.AddTextCentered(text, pText, style)
		ld.AddLine(p0, p1, color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ac0, ac1 := sp.minSepAircraft[0], sp.minSepAircraft[1]
	if ac0 == nil || ac1 == nil {
		// Two aircraft haven't been specified.
		return
	}

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.Lines.RGB()

	DrawMinimumSeparationLine(ac0, ac1, color, RGB{}, sp.systemFont[ps.CharSize.Tools],
		ctx, transforms, cb)
}

func (sp *STARSPane) drawCARings(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)

	for ac := range sp.aircraft {
		if !sp.IsCAActive(ac) {
			continue
		}

		pc := transforms.WindowFromLatLongP(ac.TrackPosition())
		radius := sp.Facility.CA.LateralMinimum / transforms.PixelDistanceNM()
		ld.AddCircle(pc, radius, 360 /* nsegs */)

		if time.Since(sp.lastCASoundTime) > 2*time.Second {
			globalConfig.Audio.PlaySound(AudioEventConflictAlert)
			sp.lastCASoundTime = time.Now()
		}
	}

	cb.LineWidth(1)
	ps := sp.CurrentPreferenceSet
	cb.SetRGB(ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
}

func (sp *STARSPane) drawAirspace(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	ps := sp.CurrentPreferenceSet
	rgb := ps.Brightness.Lists.ScaleRGB(STARSListColor)

	drawSectors := func(volumes []AirspaceVolume) {
		for _, v := range volumes {
			e := EmptyExtent2D()

			for _, pts := range v.Boundaries {
				for i := range pts {
					e = Union(e, pts[i])
					if i < len(pts)-1 {
						ld.AddLine(pts[i], pts[i+1], rgb)
					}
				}
			}

			center := e.Center()
			ps := sp.CurrentPreferenceSet
			style := TextStyle{
				Font:           sp.systemFont[ps.CharSize.Tools],
				Color:          rgb,
				DrawBackground: true, // default BackgroundColor is fine
			}
			alts := fmt.Sprintf("%d-%d", v.LowerLimit/100, v.UpperLimit/100)
			td.AddTextCentered(alts, transforms.WindowFromLatLongP(center), style)
		}
	}

	if sp.drawApproachAirspace {
		drawSectors(sim.Scenario.ApproachAirspace)
	}

	if sp.drawDepartureAirspace {
		drawSectors(sim.Scenario.DepartureAirspace)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) consumeMouseEvents(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	if ctx.mouse == nil {
		return
	}

	if activeSpinner == nil {
		UpdateScopePosition(ctx.mouse, MouseButtonSecondary, transforms,
			&sp.CurrentPreferenceSet.CurrentCenter, &sp.CurrentPreferenceSet.Range)
	}

	if ctx.mouse.Clicked[MouseButtonPrimary] {
		if ctx.keyboard != nil && ctx.keyboard.IsPressed(KeyShift) && ctx.keyboard.IsPressed(KeyControl) {
			// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
			mouseLatLong := transforms.LatLongFromWindowP(ctx.mouse.Pos)
			platform.GetClipboard().SetText(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		// If a scope click handler has been registered, give it the click
		// and then clear it out.
		var status STARSCommandStatus
		if sp.scopeClickHandler != nil {
			status = sp.scopeClickHandler(ctx.mouse.Pos, transforms)
		} else {
			status = sp.executeSTARSClickedCommand(sp.previewAreaInput, ctx.mouse.Pos, transforms)
		}

		if status.err != nil {
			// TODO: as above, rewrite server errors to be cryptic STARS errors...
			sp.previewAreaOutput = status.err.Error()
		} else {
			if status.clear {
				sp.resetInputState()
			}
			sp.previewAreaOutput = status.output
		}
	} else if ctx.mouse.Clicked[MouseButtonTertiary] {
		if ac := sp.tryGetClickedAircraft(ctx.mouse.Pos, transforms); ac != nil {
			if state := sp.aircraft[ac]; state != nil {
				state.isSelected = !state.isSelected
			}
		}
	} else if sim.Paused {
		if ac := sp.tryGetClickedAircraft(ctx.mouse.Pos, transforms); ac != nil {
			var info []string
			if ac.IsDeparture {
				info = append(info, "Departure")
			} else {
				info = append(info, "Arrival")
			}
			info = append(info, ac.Nav.Summary(ac))

			if ac.Approach != nil {
				if ac.ApproachCleared {
					info = append(info, "Cleared "+ac.Approach.FullName+" approach")
				} else {
					info = append(info, "Expecting "+ac.Approach.FullName+" approach")
				}
				if ac.NoPT {
					info = append(info, "Straight in approach")
				}
			}

			info = FilterSlice(info, func(s string) bool { return s != "" })
			infoLines := strings.Join(info, "\n")

			td := GetTextDrawBuilder()
			defer ReturnTextDrawBuilder(td)

			ps := sp.CurrentPreferenceSet
			font := sp.systemFont[ps.CharSize.Datablocks]
			style := TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: -2}

			// Aircraft track position in window coordinates
			pac := transforms.WindowFromLatLongP(ac.TrackPosition())

			// Upper-left corner of where we start drawing the text
			pad := float32(5)
			ptext := add2f([2]float32{2 * pad, 0}, pac)
			td.AddText(infoLines, ptext, style)

			// Draw an alpha-blended quad behind the text to make it more legible.
			trid := GetTrianglesDrawBuilder()
			defer ReturnTrianglesDrawBuilder(trid)
			bx, by := font.BoundText(infoLines, style.LineSpacing)
			trid.AddQuad(add2f(ptext, [2]float32{-pad, 0}),
				add2f(ptext, [2]float32{float32(bx) + pad, 0}),
				add2f(ptext, [2]float32{float32(bx) + pad, -float32(by) - pad}),
				add2f(ptext, [2]float32{-pad, -float32(by) - pad}))

			// Get it all into the command buffer
			transforms.LoadWindowViewingMatrices(cb)
			cb.SetRGBA(RGBA{R: 0.25, G: 0.25, B: 0.25, A: 0.75})
			cb.Blend()
			trid.GenerateCommands(cb)
			cb.DisableBlend()
			td.GenerateCommands(cb)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// DCB menu on top

const STARSButtonWidth = 70
const STARSButtonHeight = 70

var (
	starsBarWindowPos imgui.Vec2
)

const (
	STARSButtonFull = 1 << iota
	STARSButtonHalfVertical
	STARSButtonHalfHorizontal
	STARSButtonSelected
)

func starsButtonSize(flags int) imgui.Vec2 {
	if (flags & STARSButtonFull) != 0 {
		return imgui.Vec2{X: STARSButtonWidth, Y: STARSButtonHeight}
	} else if (flags & STARSButtonHalfVertical) != 0 {
		return imgui.Vec2{X: STARSButtonWidth, Y: STARSButtonHeight / 2}
	} else if (flags & STARSButtonHalfHorizontal) != 0 {
		return imgui.Vec2{X: STARSButtonWidth / 2, Y: STARSButtonHeight}
	} else {
		lg.Errorf("unhandled starsButtonFlags %d", flags)
		return imgui.Vec2{X: STARSButtonWidth, Y: STARSButtonHeight}
	}
}

func (sp *STARSPane) StartDrawDCB(ctx *PaneContext) {
	var flags imgui.WindowFlags
	flags = imgui.WindowFlagsNoDecoration
	flags |= imgui.WindowFlagsNoSavedSettings
	flags |= imgui.WindowFlagsNoNav
	flags |= imgui.WindowFlagsNoResize
	flags |= imgui.WindowFlagsNoScrollWithMouse
	flags |= imgui.WindowFlagsNoBackground

	starsBarWindowPos = imgui.Vec2{
		X: ctx.paneExtent.p0[0],
		Y: float32(platform.WindowSize()[1]) - ctx.paneExtent.p1[1] + 1}
	imgui.SetNextWindowPosV(starsBarWindowPos, imgui.ConditionAlways, imgui.Vec2{})
	imgui.SetNextWindowSize(imgui.Vec2{ctx.paneExtent.Width() - 2, STARSButtonHeight})
	imgui.BeginV(fmt.Sprintf("STARS Button Bar##%p", sp), nil, flags)

	//	imgui.WindowDrawList().AddRectFilledV(imgui.Vec2{}, imgui.Vec2{X: ctx.paneExtent.Width() - 2, Y: STARSButtonHeight},
	//		0xff0000ff, 1, 0)

	buttonFont := GetFont(FontIdentifier{Name: "Inconsolata Condensed Regular", Size: globalConfig.DCBFontSize})
	if buttonFont == nil {
		lg.Errorf("nil buttonFont??")
		buttonFont = GetDefaultFont()
	}

	imgui.PushFont(buttonFont.ifont)

	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, imgui.Vec2{1, 0})
	imgui.PushStyleVarVec2(imgui.StyleVarFramePadding, imgui.Vec2{1, 1})
	imgui.PushStyleVarFloat(imgui.StyleVarFrameRounding, 0.) // squared off buttons
	imgui.PushStyleVarFloat(imgui.StyleVarWindowBorderSize, 0)
	imgui.PushStyleVarFloat(imgui.StyleVarWindowRounding, 0)
	imgui.PushStyleVarVec2(imgui.StyleVarWindowPadding, imgui.Vec2{0, 0})

	imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .7, .7, 1})
	imgui.PushStyleColor(imgui.StyleColorButton, imgui.Vec4{.075, .075, .075, 1})
	imgui.PushStyleColor(imgui.StyleColorButtonHovered, imgui.Vec4{.3, .3, .3, 1})
	imgui.PushStyleColor(imgui.StyleColorButtonActive, imgui.Vec4{0, .2, 0, 1})

	imgui.SetCursorPos(imgui.Vec2{-1, 0})
}

func (sp *STARSPane) EndDrawDCB() {
	imgui.PopStyleVarV(6)
	imgui.PopStyleColorV(4)
	imgui.PopFont()
	imgui.End()
}

func updateImguiCursor(flags int, pos imgui.Vec2) {
	if (flags&STARSButtonFull) != 0 || (flags&STARSButtonHalfHorizontal) != 0 {
		imgui.SameLine()
	} else if (flags & STARSButtonHalfVertical) != 0 {
		if pos.Y == 0 {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X, Y: STARSButtonHeight / 2})
		} else {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X + STARSButtonWidth, Y: 0})
		}
	} else {
		lg.Errorf("unhandled starsButtonFlags %d", flags)
	}
}

func STARSToggleButton(text string, state *bool, flags int) (clicked bool) {
	startPos := imgui.CursorPos()
	if *state {
		imgui.PushID(text) // TODO why: comes from Middleton's Draw() method
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorButtonActive))
		imgui.ButtonV(text, starsButtonSize(flags))
		if imgui.IsItemClicked() {
			*state = false
			clicked = true
		}
		imgui.PopStyleColorV(1)
		imgui.PopID()
	} else if imgui.ButtonV(text, starsButtonSize(flags)) {
		*state = true
		clicked = true
	}
	updateImguiCursor(flags, startPos)
	return
}

var (
	// TODO: think about implications of multiple STARSPanes being active
	// at once w.r.t. this.  This probably should be a member variable,
	// though we also need to think about focus capture; probably should
	// force take it when a spinner is active..
	activeSpinner unsafe.Pointer
)

func STARSIntSpinner(text string, value *int, min int, max int, flags int) {
	STARSCallbackSpinner[int](text, value,
		func(v int) string { return fmt.Sprintf("%d", v) },
		func(v int, delta float32) int {
			di := 0
			if delta > 0 {
				di = 1
			} else if delta < 0 {
				di = -1
			}
			return clamp(v+di, min, max)
		}, flags)
}

func STARSCallbackSpinner[V any](text string, value *V, print func(v V) string,
	callback func(v V, delta float32) V, flags int) {
	text += print(*value)

	pos := imgui.CursorPos()
	buttonSize := starsButtonSize(flags)
	if activeSpinner == unsafe.Pointer(value) {
		buttonBounds := Extent2D{
			p0: [2]float32{pos.X, pos.Y},
			p1: [2]float32{pos.X + buttonSize.X, pos.Y + buttonSize.Y}}
		buttonBounds.p0 = add2f(buttonBounds.p0, [2]float32{starsBarWindowPos.X, starsBarWindowPos.Y})
		buttonBounds.p1 = add2f(buttonBounds.p1, [2]float32{starsBarWindowPos.X, starsBarWindowPos.Y})
		platform.StartCaptureMouse(buttonBounds)

		imgui.PushID(text) // TODO why: comes from ModalButtonSet Draw() method
		h := imgui.CurrentStyle().Color(imgui.StyleColorButtonActive)
		imgui.PushStyleColor(imgui.StyleColorButton, h)
		imgui.ButtonV(text, buttonSize)
		if imgui.IsItemClicked() {
			activeSpinner = nil
			platform.EndCaptureMouse()
		}

		_, wy := imgui.CurrentIO().MouseWheel()
		*value = callback(*value, wy)

		imgui.PopStyleColorV(1)
		imgui.PopID()
	} else if imgui.ButtonV(text, buttonSize) {
		activeSpinner = unsafe.Pointer(value)
	}
	updateImguiCursor(flags, pos)
}

func STARSFloatSpinner(text string, value *float32, min float32, max float32, flags int) {
	STARSCallbackSpinner(text, value, func(f float32) string { return fmt.Sprintf("%.1f", *value) },
		func(v float32, delta float32) float32 {
			return clamp(v+delta/10, min, max)
		}, flags)
}

func STARSBrightnessSpinner(text string, b *STARSBrightness, flags int) {
	STARSCallbackSpinner(text, b,
		func(b STARSBrightness) string {
			if b == 0 {
				return "OFF"
			} else {
				return fmt.Sprintf("%2d", int(b))
			}
		},
		func(b STARSBrightness, delta float32) STARSBrightness {
			if delta > 0 {
				return min(b+1, 100)
			} else if delta < 0 {
				return max(0, b-1)
			} else {
				return b
			}
		}, flags)
}

func STARSSelectButton(text string, flags int) bool {
	pos := imgui.CursorPos()
	if flags&STARSButtonSelected != 0 {
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorButtonActive))
	}
	result := imgui.ButtonV(text, starsButtonSize(flags))
	if flags&STARSButtonSelected != 0 {
		imgui.PopStyleColorV(1)
	}
	updateImguiCursor(flags, pos)
	return result
}

func STARSDisabledButton(text string, flags int) {
	pos := imgui.CursorPos()
	imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
	imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.3, .3, .3, 1})
	imgui.ButtonV(text, starsButtonSize(flags))
	imgui.PopStyleColorV(1)
	imgui.PopItemFlag()
	updateImguiCursor(flags, pos)
}

///////////////////////////////////////////////////////////////////////////
// STARSPane utility methods

// amendFlightPlan is a useful utility function for changing an entry in
// the flightplan; the provided callback function should make the update
// and the rest of the details are handled here.
func amendFlightPlan(callsign string, amend func(fp *FlightPlan)) error {
	if ac := sim.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		fp := Select(ac.FlightPlan != nil, ac.FlightPlan, &FlightPlan{})
		amend(fp)
		return sim.AmendFlightPlan(callsign, *fp)
	}
}

func (sp *STARSPane) initializeSystemFonts() {
	for i, sz := range []int{16, 18, 20, 22, 24, 28} {
		id := FontIdentifier{Name: "VT323 Regular", Size: sz}
		sp.systemFont[i] = GetFont(id)
		if sp.systemFont[i] == nil {
			lg.Errorf("Font not found for %+v", id)
			sp.systemFont[i] = GetDefaultFont()
		}
	}
}

func (sp *STARSPane) initializeAircraft() {
	// Reset and initialize all of these
	sp.aircraft = make(map[*Aircraft]*STARSAircraftState)
	sp.ghostAircraft = make(map[*Aircraft]*Aircraft)

	ps := sp.CurrentPreferenceSet
	for _, ac := range sim.GetAllAircraft() {
		sp.aircraft[ac] = &STARSAircraftState{}

		if !ps.DisableCRDA {
			if ghost := sp.Facility.CRDAConfig.GetGhost(ac); ghost != nil {
				sp.ghostAircraft[ac] = ghost
				sp.aircraft[ghost] = &STARSAircraftState{
					isGhost:        true,
					displayTPASize: ps.DisplayTPASize,
				}
			}
		}
	}
}

func (sp *STARSPane) resetInputState() {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.scopeClickHandler = nil
}

func (sp *STARSPane) multiRadarMode() bool {
	ps := sp.CurrentPreferenceSet
	_, ok := scenarioGroup.RadarSites[ps.RadarSiteSelected]
	return ps.RadarSiteSelected == "" || !ok
}

func (sp *STARSPane) radarVisibility(pos Point2LL, alt int) (primary, secondary bool, distance float32) {
	ps := sp.CurrentPreferenceSet
	distance = 1e30
	multi := sp.multiRadarMode()
	for id, site := range scenarioGroup.RadarSites {
		if !multi && ps.RadarSiteSelected != id {
			continue
		}

		if p, s, dist := site.CheckVisibility(pos, alt); p || s {
			primary = primary || p
			secondary = secondary || s
			distance = min(distance, dist)
		}
	}

	return
}

func (sp *STARSPane) visibleAircraft() []*Aircraft {
	var aircraft []*Aircraft
	ps := sp.CurrentPreferenceSet
	multi := sp.multiRadarMode()

	for ac := range sp.aircraft {
		// Is it on the ground?
		if ac.FlightPlan != nil {
			if ap, ok := scenarioGroup.Airports[ac.FlightPlan.DepartureAirport]; ok {
				if int(ac.Altitude)-ap.Elevation < 100 && nmdistance2ll(ac.Position, ap.Location) < 2 {
					continue
				}
			}
			if ap, ok := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport]; ok {
				if int(ac.Altitude)-ap.Elevation < 100 && nmdistance2ll(ac.Position, ap.Location) < 2 {
					continue
				}
			}
		}

		for id, site := range scenarioGroup.RadarSites {
			if !multi && ps.RadarSiteSelected != id {
				continue
			}
			if p, s, _ := site.CheckVisibility(ac.TrackPosition(), ac.TrackAltitude()); p || s {
				aircraft = append(aircraft, ac)
				break
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) datablockVisible(ac *Aircraft) bool {
	af := sp.CurrentPreferenceSet.AltitudeFilters
	alt := ac.TrackAltitude()
	if !ac.IsAssociated() {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) getLeaderLineDirection(ac *Aircraft) CardinalOrdinalDirection {
	if lld := sp.aircraft[ac].leaderLineDirection; lld != nil {
		return *lld
	} else {
		return sp.CurrentPreferenceSet.LeaderLineDirection
	}
}

func (sp *STARSPane) getLeaderLineVector(ac *Aircraft) [2]float32 {
	dir := sp.getLeaderLineDirection(ac)
	angle := dir.Heading()
	v := [2]float32{sin(radians(angle)), cos(radians(angle))}
	ps := sp.CurrentPreferenceSet
	return scale2f(v, float32(10+10*ps.LeaderLineLength))
}

func (sp *STARSPane) isOverflight(ac *Aircraft) bool {
	if ac.FlightPlan == nil {
		return false
	}
	_, dep := scenarioGroup.Airports[ac.FlightPlan.DepartureAirport]
	_, arr := scenarioGroup.Airports[ac.FlightPlan.ArrivalAirport]
	return dep || arr
}

func (sp *STARSPane) tryGetClickedAircraft(mousePosition [2]float32, transforms ScopeTransformations) *Aircraft {
	var ac *Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft() {
		pw := transforms.WindowFromLatLongP(a.TrackPosition())
		dist := distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac
}

func (sp *STARSPane) radarSiteId() string {
	ps := sp.CurrentPreferenceSet
	if _, ok := scenarioGroup.RadarSites[ps.RadarSiteSelected]; ok && ps.RadarSiteSelected != "" {
		return ps.RadarSiteSelected
	}
	return "MULTI"
}
