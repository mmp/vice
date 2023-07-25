// stars.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Main missing features:
// Altitude alerts
// CDRA (can build off of CRDAConfig, however)
// Quicklook

package main

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"github.com/mmp/imgui-go/v4"
)

var (
	STARSBackgroundColor         = RGB{0, 0, 0}
	STARSListColor               = RGB{.1, .9, .1}
	STARSTextAlertColor          = RGB{1, .1, .1}
	STARSTrackBlockColor         = RGB{0.12, 0.48, 1}
	STARSTrackHistoryColors      [5]RGB
	STARSJRingConeColor          = RGB{.5, .5, 1}
	STARSTrackedAircraftColor    = RGB{1, 1, 1}
	STARSUntrackedAircraftColor  = RGB{.1, .9, .1}
	STARSPointedOutAircraftColor = RGB{1, 1, 0}
	STARSSelectedAircraftColor   = RGB{0, 1, 1}
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

	events *EventsSubscription

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	aircraft map[string]*STARSAircraftState
	// map from legit to their ghost, if present
	// ghostAircraft map[string]*Aircraft

	aircraftToIndex map[string]int // for use in lists
	indexToAircraft map[int]string // map is sort of wasteful since it's dense, but...

	AutoTrackDepartures map[string]interface{}

	pointedOutAircraft map[string]struct{}
	queryUnassociated  *TransientMap[string, interface{}]

	rangeBearingLines []STARSRangeBearingLine
	minSepAircraft    [2]string

	// Various UI state
	scopeClickHandler func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus
	activeDCBMenu     int

	commandMode       CommandMode
	multiFuncPrefix   string
	previewAreaOutput string
	previewAreaInput  string

	havePlayedSPCAlertSound map[string]interface{}

	lastTrackUpdate time.Time
	lastCASoundTime time.Time

	drawApproachAirspace  bool
	drawDepartureAirspace bool

	// The start of a RBL--one click received, waiting for the second.
	wipRBL STARSRangeBearingLine
}

type STARSRangeBearingLine struct {
	p [2]struct {
		// If callsign is given, use that aircraft's position;
		// otherwise we have a fixed position.
		loc      Point2LL
		callsign string
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
	tracks [10]RadarTrack

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

func (s *STARSAircraftState) TrackAltitude() int {
	return s.tracks[0].Altitude
}

func (s *STARSAircraftState) TrackPosition() Point2LL {
	return s.tracks[0].Position
}

func (s *STARSAircraftState) TrackGroundspeed() int {
	return s.tracks[0].Groundspeed
}

func (s *STARSAircraftState) HaveHeading() bool {
	return !s.tracks[0].Position.IsZero() && !s.tracks[1].Position.IsZero()
}

// Perhaps confusingly, the vector returned by HeadingVector() is not
// aligned with the reported heading but is instead along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (s *STARSAircraftState) HeadingVector(nmPerLongitude, magneticVariation float32) Point2LL {
	var v [2]float32
	if !s.HaveHeading() {
		v = [2]float32{cos(radians(s.TrackHeading(magneticVariation))),
			sin(radians(s.TrackHeading(magneticVariation)))}
	} else {
		p0, p1 := s.tracks[0].Position, s.tracks[1].Position
		v = sub2ll(p0, p1)
	}

	nm := nmlength2ll(v, nmPerLongitude)
	// v's length should be groundspeed / 60 nm.
	return scale2ll(v, float32(s.TrackGroundspeed())/(60*nm))
}

// Note: returned value includes the magnetic correction
func (s *STARSAircraftState) TrackHeading(magneticVariation float32) float32 {
	return s.tracks[0].Heading + magneticVariation
}

func (s *STARSAircraftState) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !s.tracks[0].Position.IsZero() && now.Sub(s.tracks[0].Time) > 30*time.Second
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
	Label         string        `json:"label"`
	Group         int           `json:"group"` // 0 -> A, 1 -> B
	Name          string        `json:"name"`
	CommandBuffer CommandBuffer `json:"command_buffer"`
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
		Weather           STARSBrightness
		WxContrast        STARSBrightness
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

func MakePreferenceSet(name string, facility STARSFacility, w *World) STARSPreferenceSet {
	var ps STARSPreferenceSet

	ps.Name = name

	ps.DisplayDCB = true

	if w != nil {
		ps.Center = w.Center
		ps.Range = w.Range
	} else {
		ps.Center = Point2LL{73.475, 40.395} // JFK-ish
		ps.Range = 50
	}

	ps.CurrentCenter = ps.Center

	ps.RangeRingsCenter = ps.Center
	ps.RangeRingRadius = 5

	ps.RadarTrackHistory = 5

	ps.VideoMapVisible = make(map[string]interface{})
	if w != nil && len(w.STARSMaps) > 0 {
		ps.VideoMapVisible[w.STARSMaps[0].Name] = nil
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
	ps.Brightness.Weather = 30
	ps.Brightness.WxContrast = 30

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

func (ps *STARSPreferenceSet) Activate(w *World) {
	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[string]interface{})
		if w != nil && len(w.STARSMaps) > 0 {
			ps.VideoMapVisible[w.STARSMaps[0].Name] = nil
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

func flightPlanSTARS(w *World, ac *Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalFlight
	}

	// AAL1416 B738/L squawk controller id
	// (start of route) (alt 100s)
	result := ac.Callsign + " " + fp.AircraftType + " " + ac.AssignedSquawk.String() + " "
	if ctrl := w.GetController(ac.TrackingController); ctrl != nil {
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
func NewSTARSPane(w *World) *STARSPane {
	sp := &STARSPane{
		Facility:              MakeDefaultFacility(),
		SelectedPreferenceSet: -1,
	}
	sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility, w)
	return sp
}

func (sp *STARSPane) Name() string { return "STARS" }

func (sp *STARSPane) Activate(w *World, eventStream *EventStream) {
	if sp.CurrentPreferenceSet.Range == 0 || sp.CurrentPreferenceSet.Center.IsZero() {
		// First launch after switching over to serializing the CurrentPreferenceSet...
		sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility, w)
	}
	STARSTrackHistoryColors[0] = RGB{.12, .31, .78}
	STARSTrackHistoryColors[1] = RGB{.28, .28, .67}
	STARSTrackHistoryColors[2] = RGB{.2, .2, .51}
	STARSTrackHistoryColors[3] = RGB{.16, .16, .43}
	STARSTrackHistoryColors[4] = RGB{.12, .12, .35}
	sp.CurrentPreferenceSet.Activate(w)

	if sp.havePlayedSPCAlertSound == nil {
		sp.havePlayedSPCAlertSound = make(map[string]interface{})
	}
	if sp.pointedOutAircraft == nil {
		sp.pointedOutAircraft = make(map[string]struct{})
	}
	if sp.queryUnassociated == nil {
		sp.queryUnassociated = NewTransientMap[string, interface{}]()
	}

	sp.initializeSystemFonts()

	sp.aircraftToIndex = make(map[string]int)
	sp.indexToAircraft = make(map[int]string)

	if sp.AutoTrackDepartures == nil {
		sp.AutoTrackDepartures = make(map[string]interface{})
	}

	sp.events = eventStream.Subscribe()

	ps := sp.CurrentPreferenceSet
	if ps.Brightness.Weather != 0 {
		sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
	}

	// start tracking all of the active aircraft
	sp.initializeAircraft(w)
	sp.lastTrackUpdate = time.Time{} // force immediate update at start
}

func (sp *STARSPane) Deactivate() {
	// Drop all of them
	sp.aircraft = nil
	//sp.ghostAircraft = nil

	sp.events.Unsubscribe()
	sp.events = nil

	sp.weatherRadar.Deactivate()
}

func (sp *STARSPane) ResetWorld(w *World) {
	ps := &sp.CurrentPreferenceSet

	ps.Center = w.Center
	ps.Range = w.Range
	ps.CurrentCenter = ps.Center
	ps.RangeRingsCenter = ps.Center

	ps.VideoMapVisible = make(map[string]interface{})
	// Make the scenario's default video map be visible
	ps.VideoMapVisible[w.DefaultMap] = nil

	ps.CurrentATIS = ""
	for i := range ps.GIText {
		ps.GIText[i] = ""
	}
	ps.RadarSiteSelected = ""

	sp.lastTrackUpdate = time.Time{} // force update
}

func (sp *STARSPane) DrawUI() {
	sp.AutoTrackDepartures, _ = drawAirportSelector(sp.AutoTrackDepartures, "Auto track departure airports")

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

func (sp *STARSPane) processEvents(w *World) {
	// First handle changes in world.Aircraft
	for callsign, ac := range w.Aircraft {
		if _, ok := sp.aircraft[callsign]; !ok {
			// First we've seen it; create the *STARSAircraftState for it
			sa := &STARSAircraftState{}
			if ac.TrackingController == w.Callsign || ac.ControllingController == w.Callsign {
				sa.datablockType = FullDatablock
			}
			sp.aircraft[callsign] = sa

			if fp := ac.FlightPlan; fp != nil {
				if _, ok := sp.AutoTrackDepartures[fp.DepartureAirport]; ok && ac.TrackingController == "" {
					// We'd like to auto-track this departure, but first
					// check that we are the departure controller.

					departureController := w.PrimaryController // default
					// See if the departure controller is signed in
					for cs, mc := range w.MultiControllers {
						if _, ok := w.Controllers[cs]; ok && mc.Departure {
							departureController = cs
						}
					}

					if w.Callsign == departureController {
						w.InitiateTrack(callsign, nil, nil) // ignore error...
						sp.aircraft[callsign].datablockType = FullDatablock
					}
				}
			}

			/*
				if !ps.DisableCRDA {
					if ghost := sp.Facility.CRDAConfig.GetGhost(ac); ghost != nil {
						sp.ghostAircraft[ac.Callsign] = ghost
						sp.aircraft[ghost] = &STARSAircraftState{
							// TODO: other defaults?
							isGhost:        true,
							displayTPASize: ps.DisplayTPASize,
						}
					}
				}
			*/
		}

		if squawkingSPC(ac.Squawk) {
			if _, ok := sp.havePlayedSPCAlertSound[ac.Callsign]; !ok {
				sp.havePlayedSPCAlertSound[ac.Callsign] = nil
				//globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
			}
		}
	}

	// See if any aircraft we have state for have been removed
	for callsign := range sp.aircraft {
		if _, ok := w.Aircraft[callsign]; !ok {
			delete(sp.aircraft, callsign)
			// delete ghost a/c
		}
	}

	for _, event := range sp.events.Get() {
		switch event.Type {
		case PointOutEvent:
			if event.ToController == w.Callsign {
				sp.pointedOutAircraft[event.Callsign] = struct{}{}
			}

		case OfferedHandoffEvent:
			if event.ToController == w.Callsign {
				globalConfig.Audio.PlaySound(AudioEventInboundHandoff)
			}

		case AcceptedHandoffEvent:
			if event.FromController == w.Callsign && event.ToController != w.Callsign {
				if state, ok := sp.aircraft[event.Callsign]; !ok {
					lg.Errorf("%s: have AcceptedHandoffEvent but missing STARS state?", event.Callsign)
				} else {
					state.outboundHandoffAccepted = true
					state.outboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
					globalConfig.Audio.PlaySound(AudioEventHandoffAccepted)
				}
			}
		}
	}
}

func (sp *STARSPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	sp.processEvents(ctx.world)
	sp.updateRadarTracks(ctx.world)

	if ctx.world.STARSInputOverride != "" {
		sp.previewAreaInput = ctx.world.STARSInputOverride
		ctx.world.STARSInputOverride = ""
	}

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
		fbPaneExtent := paneRemaining.Scale(ctx.platform.DPIScale())
		cb.Scissor(int(fbPaneExtent.p0[0]), int(fbPaneExtent.p0[1]),
			int(fbPaneExtent.Width()+.5), int(fbPaneExtent.Height()+.5))
	}

	weatherIntensity := float32(ps.Brightness.Weather) / float32(100)
	sp.weatherRadar.Draw(ctx, weatherIntensity, transforms, cb)

	color := ps.Brightness.RangeRings.RGB()
	cb.LineWidth(1)
	DrawRangeRings(ctx, ps.RangeRingsCenter, float32(ps.RangeRingRadius), color, transforms, cb)

	transforms.LoadWindowViewingMatrices(cb)

	// Maps
	cb.PointSize(5)
	cb.LineWidth(1)
	for _, vmap := range ctx.world.STARSMaps {
		if _, ok := ps.VideoMapVisible[vmap.Name]; !ok {
			continue
		}

		color := ps.Brightness.VideoGroupA.RGB()
		if vmap.Group == 1 {
			color = ps.Brightness.VideoGroupB.RGB()
		}
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(vmap.CommandBuffer)
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
	aircraft := sp.visibleAircraft(ctx.world)
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
	sp.updateDatablockTextAndPosition(ctx, aircraft)
	sp.drawDatablocks(aircraft, ctx, transforms, cb)
	sp.consumeMouseEvents(ctx, transforms, cb)
}

func (sp *STARSPane) updateRadarTracks(w *World) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	now := w.CurrentTime()
	if now.Sub(sp.lastTrackUpdate) < 5*time.Second {
		return
	}
	sp.lastTrackUpdate = now

	for callsign, state := range sp.aircraft {
		ac, ok := w.Aircraft[callsign]
		if !ok {
			lg.Errorf("%s: not found in World Aircraft?", callsign)
			continue
		}

		// Move everthing forward one to make space for the new one. We could
		// be clever and use a circular buffer to skip the copies, though at
		// the cost of more painful indexing elsewhere...
		copy(state.tracks[1:], state.tracks[:len(state.tracks)-1])
		state.tracks[0] = RadarTrack{
			Position:    ac.Position,
			Altitude:    int(ac.Altitude),
			Groundspeed: int(ac.GS),
			Heading:     ac.Heading,
			Time:        now,
		}
	}
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
			if status := sp.executeSTARSCommand(sp.previewAreaInput, ctx); status.err != nil {
				sp.previewAreaOutput = GetSTARSError(status.err).Error()
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
			sp.disableMenuSpinner(ctx)

		case KeyF1:
			if ctx.keyboard.IsPressed(KeyControl) {
				// Recenter
				ps.Center = ctx.world.Center
				ps.CurrentCenter = ps.Center
			}

		case KeyF2:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
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
				sp.disableMenuSpinner(ctx)
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
				sp.disableMenuSpinner(ctx)
				sp.activeDCBMenu = DCBMenuCharSize
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeVP
			}

		case KeyF7:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
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
				sp.disableMenuSpinner(ctx)
				ps.DisplayDCB = !ps.DisplayDCB
			}

		case KeyF9:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activateMenuSpinner(unsafe.Pointer(&ps.RangeRingRadius))
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeFlightData
			}

		case KeyF10:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activateMenuSpinner(unsafe.Pointer(&ps.Range))
			}

		case KeyF11:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activeDCBMenu = DCBMenuSite
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeCollisionAlert
			}
		}
	}
}

func (sp *STARSPane) disableMenuSpinner(ctx *PaneContext) {
	activeSpinner = nil
	ctx.platform.EndCaptureMouse()
}

func (sp *STARSPane) activateMenuSpinner(ptr unsafe.Pointer) {
	activeSpinner = ptr
}

func (sp *STARSPane) getAircraftIndex(ac *Aircraft) int {
	if idx, ok := sp.aircraftToIndex[ac.Callsign]; ok {
		return idx
	} else {
		idx := len(sp.aircraftToIndex) + 1
		sp.aircraftToIndex[ac.Callsign] = idx
		sp.indexToAircraft[idx] = ac.Callsign
		return idx
	}
}

func (sp *STARSPane) executeSTARSCommand(cmd string, ctx *PaneContext) (status STARSCommandStatus) {
	lookupAircraft := func(callsign string) *Aircraft {
		if ac := ctx.world.GetAircraft(callsign); ac != nil {
			return ac
		}

		// try to match squawk code
		for _, ac := range sp.visibleAircraft(ctx.world) {
			if ac.Squawk.String() == callsign {
				return ac
			}
		}

		if idx, err := strconv.Atoi(callsign); err == nil {
			if callsign, ok := sp.indexToAircraft[idx]; ok {
				return ctx.world.Aircraft[callsign]
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
			sp.wipRBL = STARSRangeBearingLine{}
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
						for name := range ctx.world.DepartureAirports {
							sp.AutoTrackDepartures[name] = nil
						}
					} else {
						// See if it's in the facility
						if ctx.world.DepartureAirports[airport] != nil {
							sp.AutoTrackDepartures[airport] = nil
						} else {
							status.err = ErrSTARSIllegalAirport
							return
						}
					}
				}
				status.clear = true
				return
			} else if f[0] == ".FIND" {
				if pos, ok := ctx.world.Locate(f[1]); ok {
					globalConfig.highlightedLocation = pos
					globalConfig.highlightedLocationEndTime = time.Now().Add(5 * time.Second)
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalFix
					return
				}
			}
		}

	case CommandModeInitiateControl:
		if ac := lookupAircraft(cmd); ac == nil {
			status.err = ErrSTARSIllegalTrack // error code?
		} else {
			sp.initiateTrack(ctx, ac.Callsign)
			status.clear = true
		}
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for callsign, ac := range ctx.world.Aircraft {
				if ac.TrackingController == ctx.world.Callsign {
					sp.dropTrack(ctx, callsign)
				}
			}
			status.clear = true
			return
		} else {
			sp.dropTrack(ctx, lookupCallsign(cmd))
			return
		}

	case CommandModeHandOff:
		f := strings.Fields(cmd)
		switch len(f) {
		case 0:
			// Accept hand off of target closest to range rings center
			var closest *Aircraft
			var closestDistance float32
			for _, ac := range sp.visibleAircraft(ctx.world) {
				if ac.HandoffTrackController != ctx.world.Callsign {
					continue
				}

				state := sp.aircraft[ac.Callsign]
				d := nmdistance2ll(ps.RangeRingsCenter, state.TrackPosition())
				if closest == nil || d < closestDistance {
					closest = ac
					closestDistance = d
				}
			}

			if closest != nil {
				sp.acceptHandoff(ctx, closest.Callsign)
			}
			status.clear = true
			return
		case 1:
			sp.cancelHandoff(ctx, lookupCallsign(f[0]))
			status.clear = true
			return
		case 2:
			sp.handoffTrack(ctx, lookupCallsign(f[1]), f[0])
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
			if ac := ctx.world.GetAircraft(lookupCallsign(cmd)); ac != nil {
				// Display flight plan
				status.output, status.err = flightPlanSTARS(ctx.world, ac)
			} else {
				status.err = ErrSTARSNoFlight
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
				for callsign, ac := range ctx.world.Aircraft {
					if pred(ac) {
						state := sp.aircraft[callsign]
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
					me := ctx.world.Callsign
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == me })
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return

			case 2:
				if dir, ok := numpadToDirection(cmd[0]); ok && cmd[1] == 'U' {
					// FIXME: should be unassociated tracks
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == "" })
					status.clear = true
				} else if ok && cmd[1] == '*' {
					// Tracked by other controllers
					me := ctx.world.Callsign
					setLLDir(dir, func(ac *Aircraft) bool {
						return ac.TrackingController != "" &&
							ac.TrackingController != me
					})
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return

			case 4:
				// L(id)(space)(dir)
				status.err = ErrSTARSCommandFormat // set preemptively; clear on success
				for _, ctrl := range ctx.world.GetAllControllers() {
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
					if ac := ctx.world.GetAircraft(callsign); ac != nil {
						sp.aircraft[ac.Callsign].leaderLineDirection = dir
						status.clear = true
						return
					} else {
						status.err = ErrSTARSNoFlight
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
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
				callsign := lookupCallsign(f[0])
				if state, ok := sp.aircraft[callsign]; ok {
					state.pilotAltitude = 0
					sp.setScratchpad(ctx, callsign, "")
					status.clear = true
				}
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt

				callsign := lookupCallsign(f[0])
				// Either pilot alt or scratchpad entry
				if ac := ctx.world.GetAircraft(callsign); ac == nil {
					status.err = ErrSTARSNoFlight
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.aircraft[callsign].pilotAltitude = alt * 100
				} else {
					sp.setScratchpad(ctx, callsign, f[1])
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
			status.err = ctx.world.SetSquawkAutomatic(callsign)
		} else if len(f) == 2 {
			if squawk, err := ParseSquawk(f[1]); err == nil {
				callsign := lookupCallsign(f[0])
				status.err = ctx.world.SetSquawk(callsign, squawk)
			} else {
				status.err = ErrSTARSIllegalCode
			}
		} else {
			status.err = ErrSTARSCommandFormat
		}
		status.clear = true
		return

	case CommandModeCollisionAlert:
		if len(cmd) > 3 && cmd[:2] == "K " {
			callsign := lookupCallsign(cmd[2:])
			if ac := ctx.world.GetAircraft(callsign); ac != nil {
				state := sp.aircraft[callsign]
				state.disableCAWarnings = !state.disableCAWarnings
			} else {
				status.err = ErrSTARSNoFlight
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
			sp.minSepAircraft[0] = ""
			sp.minSepAircraft[1] = ""
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
			if m, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if m <= 0 || m > len(ctx.world.STARSMaps) {
				status.err = ErrSTARSIllegalValue
			} else {
				m--
				name := ctx.world.STARSMaps[m].Name
				if _, ok := ps.VideoMapVisible[name]; ok {
					delete(ps.VideoMapVisible, name)
				} else {
					ps.VideoMapVisible[name] = nil
				}
			}
			status.clear = true
			return
		}

	case CommandModeLDR:
		if len(cmd) > 0 {
			if r, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if r < 0 || r > 7 {
				status.err = ErrSTARSIllegalValue
			} else {
				ps.Range = float32(r)
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
			status.err = ErrSTARSIllegalValue
		}
		status.clear = true
		return

	case CommandModeRange:
		if len(cmd) > 0 {
			if r, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if r < 6 || r > 256 {
				status.err = ErrSTARSIllegalValue
			} else {
				ps.Range = float32(r)
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
			if i, err := strconv.Atoi(cmd); err == nil {
				if i < 0 || i >= len(ctx.world.RadarSites) {
					status.err = ErrSTARSIllegalValue
				} else {
					ps.RadarSiteSelected = SortedMapKeys(ctx.world.RadarSites)[i]
					status.clear = true
				}
				return
			}
			for id, rs := range ctx.world.RadarSites {
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

func (sp *STARSPane) setScratchpad(ctx *PaneContext, callsign string, contents string) {
	ctx.world.SetScratchpad(callsign, contents, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) setTemporaryAltitude(ctx *PaneContext, callsign string, alt int) {
	ctx.world.SetTemporaryAltitude(callsign, alt, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) initiateTrack(ctx *PaneContext, callsign string) {
	ctx.world.InitiateTrack(callsign,
		func(any) {
			if state, ok := sp.aircraft[callsign]; ok {
				state.datablockType = FullDatablock
			}
			if ac, ok := ctx.world.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = flightPlanSTARS(ctx.world, ac)
			}
		},
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) dropTrack(ctx *PaneContext, callsign string) {
	ctx.world.DropTrack(callsign, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) acceptHandoff(ctx *PaneContext, callsign string) {
	ctx.world.AcceptHandoff(callsign,
		func(any) {
			if state, ok := sp.aircraft[callsign]; ok {
				state.datablockType = FullDatablock
			}
			if ac, ok := ctx.world.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = flightPlanSTARS(ctx.world, ac)
			}
		},
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) handoffTrack(ctx *PaneContext, callsign string, controller string) {
	ctx.world.HandoffTrack(callsign, controller, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) handoffControl(ctx *PaneContext, callsign string) {
	ctx.world.HandoffControl(callsign, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) pointOut(ctx *PaneContext, callsign string, controller string) {
	ctx.world.PointOut(callsign, controller, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) cancelHandoff(ctx *PaneContext, callsign string) {
	ctx.world.CancelHandoff(callsign, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) rejectHandoff(ctx *PaneContext, callsign string) {
	ctx.world.RejectHandoff(callsign, nil,
		func(err error) {
			sp.previewAreaOutput = GetSTARSError(err).Error()
		})
}

func (sp *STARSPane) executeSTARSClickedCommand(ctx *PaneContext, cmd string, mousePosition [2]float32,
	transforms ScopeTransformations) (status STARSCommandStatus) {
	// See if an aircraft was clicked
	ac := sp.tryGetClickedAircraft(ctx.world, mousePosition, transforms)

	isControllerId := func(id string) bool {
		// FIXME: check--this is likely to be pretty slow, relatively
		// speaking...
		for _, ctrl := range ctx.world.GetAllControllers() {
			if ctrl.SectorId == id {
				return true
			}
		}
		return false
	}

	ps := &sp.CurrentPreferenceSet

	if ac != nil {
		state := sp.aircraft[ac.Callsign]

		switch sp.commandMode {
		case CommandModeNone:
			switch len(cmd) {
			case 0:
				if ac.HandoffTrackController == ctx.world.Callsign {
					// Accept inbound h/o
					status.clear = true
					sp.acceptHandoff(ctx, ac.Callsign)
					return
				} else if ac.HandoffTrackController != ctx.world.Callsign &&
					ac.TrackingController == ctx.world.Callsign {
					// cancel offered handoff offered
					status.clear = true
					sp.cancelHandoff(ctx, ac.Callsign)
					return
				} else if _, ok := sp.pointedOutAircraft[ac.Callsign]; ok {
					// ack point out
					delete(sp.pointedOutAircraft, ac.Callsign)
					status.clear = true
					return
				} else if state.outboundHandoffAccepted {
					// ack an accepted handoff, which we will treat as also
					// handing off control.
					state.outboundHandoffAccepted = false
					state.outboundHandoffFlashEnd = time.Now()
					sp.handoffControl(ctx, ac.Callsign)
				} else { //if ac.IsAssociated() {
					if state.datablockType != FullDatablock {
						state.datablockType = FullDatablock
						// do not collapse datablock if user is tracking the aircraft
					} else if ac.TrackingController != ctx.world.Callsign {
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
					sp.setScratchpad(ctx, ac.Callsign, "")
					return
				} else if cmd == "U" {
					status.clear = true
					sp.rejectHandoff(ctx, ac.Callsign)
					return
				} else if cmd == "*" {
					from := sp.aircraft[ac.Callsign].TrackPosition()
					sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
						p := transforms.LatLongFromWindowP(pw)
						hdg := headingp2ll(from, p, ac.NmPerLongitude, ac.MagneticVariation)
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
				} else if cmd == "?" {
					ctx.world.PrintInfo(ac)
					status.clear = true
					return
				} else if cmd == "X" {
					ctx.world.DeleteAircraft(ac, func(e error) {
						status.err = ErrSTARSIllegalTrack
					})
					status.clear = true
					return
				}

			case 2:
				if isControllerId(cmd) {
					status.clear = true
					sp.handoffTrack(ctx, ac.Callsign, cmd)
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
					sp.wipRBL = STARSRangeBearingLine{}
					sp.wipRBL.p[0].callsign = ac.Callsign
					sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
					return
				} else if cmd == "HJ" || cmd == "RF" || cmd == "EM" || cmd == "MI" || cmd == "SI" {
					state.spcOverride = cmd
					status.clear = true
					return
				}

			case 3:
				if isControllerId(cmd) {
					status.clear = true
					sp.handoffTrack(ctx, ac.Callsign, cmd)
					return
				} else if cmd == "*D+" {
					ps.DisplayTPASize = !ps.DisplayTPASize
					status.clear = true
					return
				} else if alt, err := strconv.Atoi(cmd); err == nil {
					state.pilotAltitude = alt * 100
					status.clear = true
					return
				}

			case 4:
				if cmd[0] == '+' {
					if alt, err := strconv.Atoi(cmd[1:]); err == nil {
						status.clear = true
						sp.setTemporaryAltitude(ctx, ac.Callsign, alt*100)
					} else {
						status.err = ErrSTARSCommandFormat
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
						status.err = amendFlightPlan(ctx.world, ac.Callsign, func(fp *FlightPlan) {
							fp.Altitude = alt * 100
						})
						status.clear = true
					} else {
						status.err = ErrSTARSCommandFormat
					}
					return
				}
			}

			if len(cmd) > 2 && cmd[:2] == "*J" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.jRingRadius = float32(r)
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.jRingRadius = float32(r)
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			}
			if len(cmd) > 2 && cmd[:2] == "*P" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.coneLength = float32(r)
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.coneLength = float32(r)
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			}
			if lc := len(cmd); lc > 2 && cmd[lc-1] == '*' && isControllerId(cmd[:lc-1]) {
				status.clear = true
				sp.pointOut(ctx, ac.Callsign, cmd[:lc-1])
				return
			}

			if len(cmd) > 0 {
				ctx.world.RunAircraftCommands(ac, cmd,
					func(err error) {
						globalConfig.Audio.PlaySound(AudioEventCommandError)
						sp.previewAreaOutput = GetSTARSError(err).Error()
					})

				status.clear = true
				return
			}

		case CommandModeInitiateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			sp.initiateTrack(ctx, ac.Callsign)
			return

		case CommandModeTerminateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			sp.dropTrack(ctx, ac.Callsign)
			return

		case CommandModeHandOff:
			if cmd == "" {
				status.clear = true
				sp.cancelHandoff(ctx, ac.Callsign)
			} else {
				status.clear = true
				sp.handoffTrack(ctx, ac.Callsign, cmd)
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
					status.output, status.err = flightPlanSTARS(ctx.world, ac)
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
					sp.setScratchpad(ctx, ac.Callsign, "")
					return
				} else {
					// Is it an altitude or a scratchpad update?
					if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
						state.pilotAltitude = alt * 100
						status.clear = true
					} else {
						status.clear = true
						sp.setScratchpad(ctx, ac.Callsign, cmd)
					}
					return
				}
			}

		case CommandModeFlightData:
			if cmd == "" {
				status.clear = true
				status.err = ctx.world.SetSquawkAutomatic(ac.Callsign)
				return
			} else {
				if squawk, err := ParseSquawk(cmd); err == nil {
					status.err = ctx.world.SetSquawk(ac.Callsign, squawk)
				} else {
					status.err = ErrSTARSIllegalParam
				}
				status.clear = true
				return
			}

		case CommandModeCollisionAlert:
			if cmd == "K" {
				state := sp.aircraft[ac.Callsign]
				state.disableCAWarnings = !state.disableCAWarnings
				status.clear = true
				// TODO: check should we set sp.commandMode = CommandMode
				// (applies here and also to others similar...)
				return
			}

		case CommandModeMin:
			if cmd == "" {
				sp.minSepAircraft[0] = ac.Callsign
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
					if ac := sp.tryGetClickedAircraft(ctx.world, pw, transforms); ac != nil {
						sp.minSepAircraft[1] = ac.Callsign
						status.clear = true
					} else {
						status.err = ErrSTARSNoFlight
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
			sp.wipRBL = STARSRangeBearingLine{}
			sp.wipRBL.p[0].loc = transforms.LatLongFromWindowP(mousePosition)
			sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
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

func rblSecondClickHandler(ctx *PaneContext, sp *STARSPane) func([2]float32, ScopeTransformations) (status STARSCommandStatus) {
	return func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
		rbl := sp.wipRBL
		sp.wipRBL = STARSRangeBearingLine{}
		if ac := sp.tryGetClickedAircraft(ctx.world, pw, transforms); ac != nil {
			rbl.p[1].callsign = ac.Callsign
		} else {
			rbl.p[1].loc = transforms.LatLongFromWindowP(pw)
		}
		sp.rangeBearingLines = append(sp.rangeBearingLines, rbl)
		status.clear = true
		return
	}
}

func (sp *STARSPane) DrawDCB(ctx *PaneContext, transforms ScopeTransformations) {
	// Find a scale factor so that the buttons all fit in the window, if necessary
	const NumDCBSlots = 19
	// Sigh; on windows we want the button size in pixels on high DPI displays
	ds := Select(runtime.GOOS == "windows", ctx.platform.DPIScale(), float32(1))
	buttonScale := min(ds, (ds*ctx.paneExtent.Width()-4)/(NumDCBSlots*STARSButtonWidth))

	sp.StartDrawDCB(ctx, buttonScale)

	ps := &sp.CurrentPreferenceSet

	switch sp.activeDCBMenu {
	case DCBMenuMain:
		STARSCallbackSpinner(ctx, "RANGE\n", &ps.Range,
			func(v float32) string { return fmt.Sprintf("%d", int(v)) },
			func(v, delta float32) float32 {
				if delta > 0 {
					v++
				} else if delta < 0 {
					v--
				}
				return clamp(v, 6, 256)
			}, STARSButtonFull, buttonScale)
		if STARSSelectButton("PLACE\nCNTR", STARSButtonHalfVertical, buttonScale) {
			sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			}
		}
		ps.OffCenter = ps.CurrentCenter != ps.Center
		if STARSToggleButton("OFF\nCNTR", &ps.OffCenter, STARSButtonHalfVertical, buttonScale) {
			ps.CurrentCenter = ps.Center
		}
		STARSCallbackSpinner(ctx, "RR\n", &ps.RangeRingRadius,
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
			}, STARSButtonFull, buttonScale)
		if STARSSelectButton("PLACE\nRR", STARSButtonHalfVertical, buttonScale) {
			sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				status.clear = true
				return
			}
		}
		if STARSSelectButton("RR\nCNTR", STARSButtonHalfVertical, buttonScale) {
			cw := [2]float32{ctx.paneExtent.Width() / 2, ctx.paneExtent.Height() / 2}
			ps.RangeRingsCenter = transforms.LatLongFromWindowP(cw)
		}
		if STARSSelectButton("MAPS", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMaps
		}
		for i := 0; i < 6; i++ {
			if i >= len(ctx.world.STARSMaps) {
				STARSDisabledButton(fmt.Sprintf(" %d\n", i+1), STARSButtonHalfVertical, buttonScale)
			} else {
				text := fmt.Sprintf(" %d\n%s", i+1, ctx.world.STARSMaps[i].Label)
				name := ctx.world.STARSMaps[i].Name
				_, visible := ps.VideoMapVisible[name]
				if STARSToggleButton(text, &visible, STARSButtonHalfVertical, buttonScale) {
					if visible {
						ps.VideoMapVisible[name] = nil
					} else {
						delete(ps.VideoMapVisible, name)
					}
				}
			}
		}
		for i := range ps.WeatherIntensity {
			STARSDisabledButton("WX"+fmt.Sprintf("%d", i), STARSButtonHalfHorizontal, buttonScale)

		}
		if STARSSelectButton("BRITE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuBrite
		}
		STARSCallbackSpinner(ctx, "LDR DIR\n   ", &ps.LeaderLineDirection,
			func(d CardinalOrdinalDirection) string { return d.ShortString() },
			func(d CardinalOrdinalDirection, delta float32) CardinalOrdinalDirection {
				if delta == 0 {
					return d
				} else if delta < 0 {
					return CardinalOrdinalDirection((d + 7) % 8)
				} else {
					return CardinalOrdinalDirection((d + 1) % 8)
				}
			}, STARSButtonHalfVertical, buttonScale)
		STARSCallbackSpinner(ctx, "LDR\n ", &ps.LeaderLineLength,
			func(v int) string { return fmt.Sprintf("%d", v) },
			func(v int, delta float32) int {
				if delta == 0 {
					return v
				} else if delta < 0 {
					return max(0, v-1)
				} else {
					return min(7, v+1)
				}
			}, STARSButtonHalfVertical, buttonScale)

		if STARSSelectButton("CHAR\nSIZE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuCharSize
		}
		STARSDisabledButton("MODE\nFSL", STARSButtonFull, buttonScale)
		if STARSSelectButton("PREF\n"+ps.Name, STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuPref
		}

		site := sp.radarSiteId(ctx.world)
		if STARSSelectButton("SITE\n"+site, STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuSite
		}
		if STARSSelectButton("SSA\nFILTER", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuSSAFilter
		}
		if STARSSelectButton("GI TEXT\nFILTER", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuGITextFilter
		}
		if STARSSelectButton("SHIFT", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuAux
		}

	case DCBMenuAux:
		STARSDisabledButton("VOL\n10", STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "HISTORY\n", &ps.RadarTrackHistory, 0, 10, STARSButtonFull, buttonScale)
		STARSDisabledButton("CURSOR\nHOME", STARSButtonFull, buttonScale)
		STARSDisabledButton("CSR SPD\n4", STARSButtonFull, buttonScale)
		STARSDisabledButton("MAP\nUNCOR", STARSButtonFull, buttonScale)
		STARSToggleButton("UNCOR", &ps.DisplayUncorrelatedTargets, STARSButtonFull, buttonScale)
		STARSDisabledButton("BEACON\nMODE-2", STARSButtonFull, buttonScale)
		STARSDisabledButton("RTQC", STARSButtonFull, buttonScale)
		STARSDisabledButton("MCP", STARSButtonFull, buttonScale)
		STARSDisabledButton("DCP\nTOP", STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("DCP\nLEFT", STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("DCP\nRIGHT", STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("DCP\nBOTTOM", STARSButtonHalfVertical, buttonScale)
		STARSFloatSpinner(ctx, "PTL\nLNTH\n", &ps.PTLLength, 0.1, 20, STARSButtonFull, buttonScale)
		STARSToggleButton("PTL OWN", &ps.PTLOwn, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("PTL ALL", &ps.PTLAll, STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("SHIFT", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuMaps:
		STARSDisabledButton("MAPS", STARSButtonFull, buttonScale)
		if STARSSelectButton("DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}
		if STARSSelectButton("CLR ALL", STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapVisible = make(map[string]interface{})
		}
		for i := 0; i < NumSTARSMaps; i++ {
			if i >= len(ctx.world.STARSMaps) {
				STARSDisabledButton(fmt.Sprintf(" %d", i+1), STARSButtonHalfVertical, buttonScale)
			} else {
				name := ctx.world.STARSMaps[i].Name
				_, visible := ps.VideoMapVisible[name]
				if STARSToggleButton(ctx.world.STARSMaps[i].Label, &visible, STARSButtonHalfVertical, buttonScale) {
					if visible {
						ps.VideoMapVisible[name] = nil
					} else {
						delete(ps.VideoMapVisible, name)
					}
				}
			}
		}
		STARSToggleButton("GEO\nMAPS", &ps.VideoMapsList.Visible, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("DANGER\nAREAS", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("SYS\nPROC", STARSButtonHalfVertical, buttonScale) {
			// TODO--this is a toggle that displays a "PROCESSING AREAS"
			// list in the top middle of the screen.
		}
		STARSToggleButton("CURRENT", &ps.ListSelectedMaps, STARSButtonHalfVertical, buttonScale)

	case DCBMenuBrite:
		STARSDisabledButton("BRITE", STARSButtonFull, buttonScale)
		STARSDisabledButton("DCB 100", STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("BKC 100", STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "MPA ", &ps.Brightness.VideoGroupA, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "MPB ", &ps.Brightness.VideoGroupB, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "FDB ", &ps.Brightness.FullDatablocks, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "LST ", &ps.Brightness.Lists, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "POS ", &ps.Brightness.Positions, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "LDB ", &ps.Brightness.LimitedDatablocks, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "OTH ", &ps.Brightness.OtherTracks, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "TLS ", &ps.Brightness.Lines, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "RR ", &ps.Brightness.RangeRings, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "CMP ", &ps.Brightness.Compass, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "BCN ", &ps.Brightness.BeaconSymbols, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "PRI ", &ps.Brightness.PrimarySymbols, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "HST ", &ps.Brightness.History, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "WX", &ps.Brightness.Weather, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "WXC", &ps.Brightness.WxContrast, STARSButtonHalfVertical, buttonScale)
		if ps.Brightness.Weather != 0 {
			sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
		} else {
			// Don't fetch weather maps if they're not going to be displayed.
			sp.weatherRadar.Deactivate()
		}
		STARSDisabledButton("", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuCharSize:
		STARSDisabledButton("BRITE", STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, " DATA\nBLOCKS\n  ", &ps.CharSize.Datablocks, 0, 5, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "LISTS\n  ", &ps.CharSize.Lists, 0, 5, STARSButtonFull, buttonScale)
		STARSDisabledButton("DCB\n 1", STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "TOOLS\n  ", &ps.CharSize.Tools, 0, 5, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "POS\n ", &ps.CharSize.PositionSymbols, 0, 5, STARSButtonFull, buttonScale)
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuPref:
		for i := range sp.PreferenceSets {
			text := fmt.Sprintf("%d\n%s", i+1, sp.PreferenceSets[i].Name)
			flags := STARSButtonHalfVertical
			if i == sp.SelectedPreferenceSet {
				flags = flags | STARSButtonSelected
			}
			if STARSSelectButton(text, flags, buttonScale) {
				// Make this one current
				sp.SelectedPreferenceSet = i
				sp.CurrentPreferenceSet = sp.PreferenceSets[i]
			}
		}
		for i := len(sp.PreferenceSets); i < NumSTARSPreferenceSets; i++ {
			STARSDisabledButton(fmt.Sprintf("%d\n", i+1), STARSButtonHalfVertical, buttonScale)
		}

		if STARSSelectButton("DEFAULT", STARSButtonHalfVertical, buttonScale) {
			sp.CurrentPreferenceSet = MakePreferenceSet("", sp.Facility, ctx.world)
		}
		STARSDisabledButton("FSSTARS", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("RESTORE", STARSButtonHalfVertical, buttonScale) {
			// TODO: restore settings in effect when entered the Pref sub-menu
		}

		validSelection := sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets)
		if validSelection {
			if STARSSelectButton("SAVE", STARSButtonHalfVertical, buttonScale) {
				sp.PreferenceSets[sp.SelectedPreferenceSet] = sp.CurrentPreferenceSet
				globalConfig.Save()
			}
		} else {
			STARSDisabledButton("SAVE", STARSButtonHalfVertical, buttonScale)
		}
		STARSDisabledButton("CHG PIN", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("SAVE AS", STARSButtonHalfVertical, buttonScale) {
			// A command mode handles prompting for the name and then saves
			// when enter is pressed.
			sp.commandMode = CommandModeSavePrefAs
		}
		if validSelection {
			if STARSSelectButton("DELETE", STARSButtonHalfVertical, buttonScale) {
				sp.PreferenceSets = DeleteSliceElement(sp.PreferenceSets, sp.SelectedPreferenceSet)
			}
		} else {
			STARSDisabledButton("DELETE", STARSButtonHalfVertical, buttonScale)
		}

		if STARSSelectButton("DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSite:
		for _, id := range SortedMapKeys(ctx.world.RadarSites) {
			site := ctx.world.RadarSites[id]
			label := " " + site.Char + " " + "\n" + id
			selected := ps.RadarSiteSelected == id
			if STARSToggleButton(label, &selected, STARSButtonFull, buttonScale) {
				if selected {
					ps.RadarSiteSelected = id
				} else {
					ps.RadarSiteSelected = ""
				}
			}
		}
		multi := sp.multiRadarMode(ctx.world)
		if STARSToggleButton("MULTI", &multi, STARSButtonFull, buttonScale) && multi {
			ps.RadarSiteSelected = ""
		}
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSSAFilter:
		STARSToggleButton("All", &ps.SSAList.Filter.All, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("WX", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("TIME", &ps.SSAList.Filter.Time, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("ALTSTG", &ps.SSAList.Filter.Altimeter, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("STATUS", &ps.SSAList.Filter.Status, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("PLAN", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("RADAR", &ps.SSAList.Filter.Radar, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("CODES", &ps.SSAList.Filter.Codes, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("SPC", &ps.SSAList.Filter.SpecialPurposeCodes, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("SYS OFF", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("RANGE", &ps.SSAList.Filter.Range, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("PTL", &ps.SSAList.Filter.PredictedTrackLines, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("ALT FIL", &ps.SSAList.Filter.AltitudeFilters, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("NAS I/F", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("AIRPORT", &ps.SSAList.Filter.AirportWeather, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("OP MODE", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton("TT", STARSButtonHalfVertical, buttonScale)      // ?? TODO
		STARSDisabledButton("WX HIST", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("QL", &ps.SSAList.Filter.QuickLookPositions, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("TW OFF", &ps.SSAList.Filter.DisabledTerminal, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("CON/CPL", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton("OFF IND", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton("CRDA", &ps.SSAList.Filter.ActiveCRDAPairs, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton("", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuGITextFilter:
		STARSToggleButton("MAIN", &ps.SSAList.Filter.Text.Main, STARSButtonHalfVertical, buttonScale)
		for i := range ps.SSAList.Filter.Text.GI {
			STARSToggleButton(fmt.Sprintf("GI %d", i+1), &ps.SSAList.Filter.Text.GI[i],
				STARSButtonHalfVertical, buttonScale)
		}
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}
	}

	sp.EndDrawDCB()
}

func (sp *STARSPane) drawSystemLists(aircraft []*Aircraft, ctx *PaneContext,
	transforms ScopeTransformations, cb *CommandBuffer) {
	for name := range ctx.world.AllAirports() {
		ctx.world.AddAirportForWeather(name)
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
				text += ctx.world.CurrentTime().UTC().Format("1504/05 ")
			}
			if filter.All || filter.Altimeter {
				if metar := ctx.world.GetMETAR(ctx.world.PrimaryAirport); metar != nil {
					text += formatMETAR(ctx.world.PrimaryAirport, metar)
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
				if ctx.world.Connected() {
					pw = td.AddText("OK/OK/NA ", pw, style)
				} else {
					pw = td.AddText("NA/NA/NA ", pw, alertStyle)
				}
			}
			if filter.All || filter.Radar {
				pw = td.AddText(sp.radarSiteId(ctx.world), pw, style)
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
				state := sp.aircraft[ac.Callsign]
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
			airports, _ := FlattenMap(ctx.world.AllAirports())
			// Sort via 1. primary? 2. tower list index, 3. alphabetic
			sort.Slice(airports, func(i, j int) bool {
				if airports[i] == ctx.world.PrimaryAirport {
					return true
				} else if airports[j] == ctx.world.PrimaryAirport {
					return false
				} else {
					a, b := ctx.world.GetAirport(airports[i]), ctx.world.GetAirport(airports[j])
					if a.TowerListIndex != 0 && b.TowerListIndex == 0 {
						return true
					} else if b.TowerListIndex != 0 && a.TowerListIndex == 0 {
						return false
					}
				}
				return airports[i] < airports[j]
			})

			for _, icao := range airports {
				if metar := ctx.world.GetMETAR(icao); metar != nil {
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
				if ap := ctx.world.DepartureAirports[fp.DepartureAirport]; ap != nil {
					dep[sp.getAircraftIndex(ac)] = ac
					break
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

		for name, ap := range ctx.world.AllAirports() {
			if ap.TowerListIndex == i+1 {
				text := stripK(name) + " TOWER\n"
				m := make(map[float32]string)
				for _, ac := range aircraft {
					if ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == name {
						dist := nmdistance2ll(ap.Location, sp.aircraft[ac.Callsign].TrackPosition())
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
		format := func(ctrl *Controller) string {
			return fmt.Sprintf("%3s", ctrl.SectorId) + " " + ctrl.Frequency.String() + " " +
				ctrl.Callsign + Select(ctrl.IsHuman, "*", "") + "\n"
		}

		// User first
		text := ""
		userCtrl := ctx.world.GetController(ctx.world.Callsign)
		if userCtrl != nil {
			text += format(userCtrl)
		}

		for _, callsign := range SortedMapKeys(ctx.world.GetAllControllers()) {
			ctrl := ctx.world.GetController(callsign)
			if ctrl != userCtrl {
				text += format(ctrl)
			}
		}

		drawList(text, ps.SignOnList.Position)
	}

	td.GenerateCommands(cb)
}

func (sp *STARSPane) datablockType(ctx *PaneContext, ac *Aircraft) DatablockType {
	state := sp.aircraft[ac.Callsign]
	dt := state.datablockType

	// TODO: when do we do a partial vs limited datablock?
	if ac.Squawk != ac.AssignedSquawk {
		dt = PartialDatablock
	}

	if ac.HandoffTrackController == ctx.world.Callsign {
		// it's being handed off to us
		dt = FullDatablock
	}

	// Point outs are FDB until acked.
	if _, ok := sp.pointedOutAircraft[ac.Callsign]; ok {
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

	now := ctx.world.CurrentTime()
	for _, ac := range aircraft {
		state := sp.aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		brightness := ps.Brightness.Positions

		dt := sp.datablockType(ctx, ac)

		if dt == PartialDatablock || dt == LimitedDatablock {
			brightness = ps.Brightness.LimitedDatablocks
		}

		pos := state.TrackPosition()
		pw := transforms.WindowFromLatLongP(pos)
		// TODO: orient based on radar center if just one radar
		orientation := state.TrackHeading(0)
		if math.IsNaN(float64(orientation)) {
			orientation = 0
		}
		rot := rotator2f(orientation)

		// On high DPI windows displays we need to scale up the tracks
		scale := Select(runtime.GOOS == "windows", ctx.platform.DPIScale(), float32(1))

		// blue box: x +/-9 pixels, y +/-3 pixels
		// TODO: size based on distance to radar, if not MULTI
		box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}
		for i := range box {
			box[i] = add2f(rot(scale2f(box[i], scale)), pw)
			box[i] = transforms.LatLongFromWindowP(box[i])
		}
		color := brightness.ScaleRGB(STARSTrackBlockColor)
		primary, secondary, _ := sp.radarVisibility(ctx.world, state.TrackPosition(), state.TrackAltitude())
		if primary {
			// Draw a filled box
			trid.AddQuad(box[0], box[1], box[2], box[3], color)
		} else if secondary {
			// If it's just a secondary return, only draw the box outline.
			// TODO: is this 40nm, or secondary?
			ld.AddPolyline([2]float32{}, color, box[:])
		}

		if !sp.multiRadarMode(ctx.world) {
			// green line
			// TODO: size based on distance to radar
			line := [2][2]float32{[2]float32{-16, -3}, [2]float32{16, -3}}
			for i := range line {
				line[i] = add2f(rot(scale2f(line[i], scale)), pw)
				line[i] = transforms.LatLongFromWindowP(line[i])
			}
			ld.AddLine(line[0], line[1], brightness.ScaleRGB(RGB{R: .1, G: .8, B: .1}))
		}

		if state.isGhost {
			// TODO: handle
			// color = ctx.cs.GhostDatablock
		}

		// Draw main track symbol letter
		if ac.TrackingController != "" {
			ch := "?"
			if ctrl := ctx.world.GetController(ac.TrackingController); ctrl != nil {
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

			px := float32(3) * scale
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
		n := ps.RadarTrackHistory
		for i := n; i > 1; i-- {
			trackColorNum := len(STARSTrackHistoryColors) - 1
			if i-1 < trackColorNum {
				trackColorNum = i - 1
			}
			trackColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColors[trackColorNum])
			p := state.tracks[i-1].Position

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

func (sp *STARSPane) updateDatablockTextAndPosition(ctx *PaneContext, aircraft []*Aircraft) {
	now := ctx.world.CurrentTime()
	font := sp.systemFont[sp.CurrentPreferenceSet.CharSize.Datablocks]

	for _, ac := range aircraft {
		state := sp.aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac) {
			continue
		}

		state.datablockErrText, state.datablockText = sp.formatDatablock(ctx, ac)

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
				state.datablockText[i][0] = justify(state.datablockText[i][0])
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
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{0, bh})
		case NorthEast, East, SouthEast:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{0, bh / 2})
		case South:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{0, 0})
		case SouthWest, West, NorthWest:
			state.datablockDrawOffset = add2f(state.datablockDrawOffset, [2]float32{-bw, bh / 2})
		}
	}

}

func (sp *STARSPane) OutsideAirspace(ctx *PaneContext, ac *Aircraft) (alts [][2]int, outside bool) {
	// Only report on ones that are tracked by us
	if ac.TrackingController != ctx.world.Callsign {
		return
	}

	if ac.IsDeparture {
		if len(ctx.world.DepartureAirspace) > 0 {
			inDepartureAirspace, depAlts := InAirspace(ac.Position, ac.Altitude, ctx.world.DepartureAirspace)
			if !ac.HaveEnteredAirspace {
				ac.HaveEnteredAirspace = inDepartureAirspace
			} else {
				alts = depAlts
				outside = !inDepartureAirspace
			}
		}
	} else {
		if len(ctx.world.ApproachAirspace) > 0 {
			inApproachAirspace, depAlts := InAirspace(ac.Position, ac.Altitude, ctx.world.ApproachAirspace)
			if !ac.HaveEnteredAirspace {
				ac.HaveEnteredAirspace = inApproachAirspace
			} else {
				alts = depAlts
				outside = !inApproachAirspace
			}
		}
	}
	return
}

func (sp *STARSPane) IsCAActive(ctx *PaneContext, ac *Aircraft) bool {
	state := sp.aircraft[ac.Callsign]
	if state.TrackAltitude() < int(sp.Facility.CA.Floor) {
		return false
	}

	for ocs, other := range ctx.world.Aircraft {
		if ocs == ac.Callsign {
			continue
		}
		ostate := sp.aircraft[ocs]
		if ostate.TrackAltitude() < int(sp.Facility.CA.Floor) {
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

		if nmdistance2ll(state.TrackPosition(), ostate.TrackPosition()) <= sp.Facility.CA.LateralMinimum &&
			abs(state.TrackAltitude()-ostate.TrackAltitude()) <= int(sp.Facility.CA.VerticalMinimum-50 /*small slop for fp error*/) {
			return true
		}
	}
	return false
}

func (sp *STARSPane) formatDatablock(ctx *PaneContext, ac *Aircraft) (errblock string, mainblock [2][]string) {
	state := sp.aircraft[ac.Callsign]

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
	if sp.IsCAActive(ctx, ac) {
		errs = append(errs, "CA")
	}
	if alts, outside := sp.OutsideAirspace(ctx, ac); outside {
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

	ty := sp.datablockType(ctx, ac)

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
		actype := ac.FlightPlan.TypeWithoutSuffix()
		suffix := "  "
		if ac.FlightPlan.Rules == VFR {
			suffix = "V"
		} else if sp.isOverflight(ctx, ac) {
			suffix = "E"
		} else {
			suffix = " "
		}
		if actype == "B757" {
			suffix += "F"
		} else if strings.HasPrefix(actype, "H/") {
			actype = strings.TrimPrefix(actype, "H/")
			suffix += "H"
		} else if strings.HasPrefix(actype, "S/") {
			actype = strings.TrimPrefix(actype, "S/")
			suffix += "J"
		} else if strings.HasPrefix(actype, "J/") {
			actype = strings.TrimPrefix(actype, "J/")
			suffix += "J"
		}

		// Unassociated with LDB should be 2 lines: squawk, altitude--unless
		// beacon codes are inhibited in LDBs.

		if fp := ac.FlightPlan; fp != nil && fp.Rules == IFR {
			// Alternate between altitude and either scratchpad or destination airport.
			mainblock[0] = append(mainblock[0], fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)+suffix)
			if ac.Scratchpad != "" {
				mainblock[1] = append(mainblock[1], ac.Scratchpad+suffix)
			} else {
				mainblock[1] = append(mainblock[1], fp.ArrivalAirport+suffix)
			}
		} else {
			as := fmt.Sprintf("%03d  %02d", (state.TrackAltitude()+50)/100, (state.TrackGroundspeed()+5)/10)
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
		if _, ok := sp.pointedOutAircraft[ac.Callsign]; ok {
			cs += " PO"
		}
		mainblock[0] = append(mainblock[0], cs)
		mainblock[1] = append(mainblock[1], cs)

		// Second line of the non-error datablock
		ho := " "
		if ac.HandoffTrackController != "" {
			if ctrl := ctx.world.GetController(ac.HandoffTrackController); ctrl != nil {
				ho = ctrl.SectorId[len(ctrl.SectorId)-1:]
			}
		}

		// Altitude and speed: mainblock[0]
		alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if state.LostTrack(ctx.world.CurrentTime()) {
			alt = "CST"
		}
		speed := fmt.Sprintf("%02d", (state.TrackGroundspeed()+5)/10)
		// TODO: pilot reported altitude. Asterisk after alt when showing.
		actype := ac.FlightPlan.TypeWithoutSuffix()
		suffix := "  "
		if ac.FlightPlan.Rules == VFR {
			suffix = "V"
		} else if sp.isOverflight(ctx, ac) {
			suffix = "E"
		} else {
			suffix = " "
		}
		if actype == "B757" {
			suffix += "F"
		} else if strings.HasPrefix(actype, "H/") {
			actype = strings.TrimPrefix(actype, "H/")
			suffix += "H"
		} else if strings.HasPrefix(actype, "S/") {
			actype = strings.TrimPrefix(actype, "S/")
			suffix += "J"
		} else if strings.HasPrefix(actype, "J/") {
			actype = strings.TrimPrefix(actype, "J/")
			suffix += "J"
		}
		mainblock[0] = append(mainblock[0], alt+ho+speed+suffix)

		// mainblock[1]
		arrscr := ac.FlightPlan.ArrivalAirport
		if ac.Scratchpad != "" {
			arrscr = ac.Scratchpad
		}

		mainblock[1] = append(mainblock[1], arrscr+ho+actype)
	}

	if ac.TempAltitude != 0 {
		ta := (ac.TempAltitude + 50) / 100
		tastr := fmt.Sprintf("     A%03d", ta)
		mainblock[0] = append(mainblock[0], tastr)
		mainblock[1] = append(mainblock[1], tastr)
	}

	return
}

func (sp *STARSPane) datablockColor(w *World, ac *Aircraft) RGB {
	// TODO: when do we use Brightness.LimitedDatablocks?
	ps := sp.CurrentPreferenceSet
	br := ps.Brightness.FullDatablocks
	state := sp.aircraft[ac.Callsign]

	if _, ok := sp.pointedOutAircraft[ac.Callsign]; ok {
		// yellow for pointed out
		return br.ScaleRGB(STARSPointedOutAircraftColor)
	} else if ac.TrackingController == w.Callsign {
		// white if we are tracking, unless it's selected
		if state.isSelected {
			return br.ScaleRGB(STARSSelectedAircraftColor)
		} else {
			return br.ScaleRGB(STARSTrackedAircraftColor)
		}
	} else if ac.HandoffTrackController == w.Callsign {
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

	now := ctx.world.CurrentTime()
	realNow := time.Now() // for flashing rate...
	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Datablocks]

	for _, ac := range aircraft {
		state := sp.aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac) {
			continue
		}

		color := sp.datablockColor(ctx.world, ac)
		style := TextStyle{Font: font, Color: color, DropShadow: true, LineSpacing: 0}
		dbText := state.datablockText[(realNow.Second()/2)&1] // 2 second cycle

		// Draw characters starting at the upper left.
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		pt := add2f(state.datablockDrawOffset, pac)
		if state.datablockErrText != "" {
			errorStyle := TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSTextAlertColor),
				LineSpacing: 0}
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

	now := ctx.world.CurrentTime()
	for _, ac := range aircraft {
		state := sp.aircraft[ac.Callsign]
		if state.LostTrack(now) || !state.HaveHeading() {
			continue
		}
		if !(state.displayPTL || ps.PTLAll || (ps.PTLOwn && ac.TrackingController == ctx.world.Callsign)) {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(state.TrackGroundspeed()) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := state.TrackHeading(-ac.MagneticVariation)
		h := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		h = scale2f(h, dist)
		end := add2f(ll2nm(state.TrackPosition(), ac.NmPerLongitude), h)

		ld.AddLine(state.TrackPosition(), nm2ll(end, ac.NmPerLongitude), color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	now := ctx.world.CurrentTime()
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Tools]
	color := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)
	textStyle := TextStyle{Font: font, DrawBackground: true, Color: color}

	for _, ac := range aircraft {
		state := sp.aircraft[ac.Callsign]
		if state.LostTrack(now) {
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

		if state.jRingRadius > 0 {
			const nsegs = 360
			pc := transforms.WindowFromLatLongP(state.TrackPosition())
			radius := state.jRingRadius / transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
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

		if state.coneLength > 0 && state.HaveHeading() {
			// We'll draw in window coordinates. First figure out the
			// coordinates of the vertices of the cone triangle. We'll
			// start with a canonical triangle in nm coordinates, going one
			// unit up the +y axis with a small spread in x.
			v := [4][2]float32{[2]float32{0, 0}, [2]float32{-.04, 1}, [2]float32{.04, 1}}

			// Now we want to get that triangle in window coordinates...
			length := state.coneLength / transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
			rot := rotator2f(state.TrackHeading(ac.MagneticVariation))
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
			pw := transforms.WindowFromLatLongP(state.TrackPosition())
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

	drawRBL := func(p0 Point2LL, p1 Point2LL, idx int) {
		// Format the range-bearing line text for the two positions.
		hdg := headingp2ll(p0, p1, ctx.world.NmPerLongitude, ctx.world.MagneticVariation)
		dist := nmdistance2ll(p0, p1)
		text := fmt.Sprintf("%d/%.2f-%d", int(hdg+.5), dist, idx)

		// And draw the line and the text.
		pText := transforms.WindowFromLatLongP(mid2ll(p0, p1))
		td.AddTextCentered(text, pText, style)
		ld.AddLine(p0, p1, color)
	}

	// Maybe draw a wip RBL with p1 as the mouse's position
	wp := sp.wipRBL.p[0]
	if ctx.mouse != nil && (!wp.loc.IsZero() || wp.callsign != "") {
		p1 := transforms.LatLongFromWindowP(ctx.mouse.Pos)
		if wp.callsign != "" {
			if ac := ctx.world.Aircraft[wp.callsign]; ac != nil && sp.datablockVisible(ac) {
				if state, ok := sp.aircraft[wp.callsign]; ok {
					drawRBL(state.TrackPosition(), p1, len(sp.rangeBearingLines)+1)
				}
			}
		} else {
			drawRBL(wp.loc, p1, len(sp.rangeBearingLines)+1)
		}
	}

	for i, rbl := range sp.rangeBearingLines {
		// Each line endpoint may be specified either by an aircraft's
		// position or by a fixed position. We'll start with the fixed
		// position and then override it if there's a valid *Aircraft.
		p0, p1 := rbl.p[0].loc, rbl.p[1].loc
		if ac := ctx.world.Aircraft[rbl.p[0].callsign]; ac != nil {
			state, ok := sp.aircraft[ac.Callsign]
			if !ok || state.LostTrack(ctx.world.CurrentTime()) || !sp.datablockVisible(ac) {
				continue
			}
			p0 = state.TrackPosition()
		}
		if ac := ctx.world.Aircraft[rbl.p[1].callsign]; ac != nil {
			state, ok := sp.aircraft[ac.Callsign]
			if !ok || state.LostTrack(ctx.world.CurrentTime()) || !sp.datablockVisible(ac) {
				continue
			}
			p1 = state.TrackPosition()
		}

		drawRBL(p0, p1, i+1)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	cs0, cs1 := sp.minSepAircraft[0], sp.minSepAircraft[1]
	if cs0 == "" || cs1 == "" {
		// Two aircraft haven't been specified.
		return
	}
	ac0, ok0 := ctx.world.Aircraft[cs0]
	ac1, ok1 := ctx.world.Aircraft[cs1]
	if !ok0 || !ok1 {
		// Missing aircraft
		return
	}

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.Lines.RGB()

	s0, ok0 := sp.aircraft[ac0.Callsign]
	s1, ok1 := sp.aircraft[ac1.Callsign]
	if !ok0 || !ok1 {
		return
	}
	DrawMinimumSeparationLine(s0.TrackPosition(), s0.HeadingVector(ac0.NmPerLongitude, ac0.MagneticVariation),
		s1.TrackPosition(), s1.HeadingVector(ac1.NmPerLongitude, ac1.MagneticVariation),
		color, RGB{}, sp.systemFont[ps.CharSize.Tools], ctx, transforms, cb)
}

func (sp *STARSPane) drawCARings(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)

	for callsign := range sp.aircraft {
		ac, ok := ctx.world.Aircraft[callsign]
		if !ok || (ok && !sp.IsCAActive(ctx, ac)) {
			continue
		}
		state := sp.aircraft[callsign]

		pc := transforms.WindowFromLatLongP(state.TrackPosition())
		radius := sp.Facility.CA.LateralMinimum / transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
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
		drawSectors(ctx.world.ApproachAirspace)
	}

	if sp.drawDepartureAirspace {
		drawSectors(ctx.world.DepartureAirspace)
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
			ctx.platform.GetClipboard().SetText(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		// If a scope click handler has been registered, give it the click
		// and then clear it out.
		var status STARSCommandStatus
		if sp.scopeClickHandler != nil {
			status = sp.scopeClickHandler(ctx.mouse.Pos, transforms)
		} else {
			status = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.mouse.Pos, transforms)
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
		if ac := sp.tryGetClickedAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
			if state := sp.aircraft[ac.Callsign]; state != nil {
				state.isSelected = !state.isSelected
			}
		}
	} else if ctx.world.SimIsPaused {
		if ac := sp.tryGetClickedAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
			var info []string
			if ac.IsDeparture {
				info = append(info, "Departure")
			} else {
				info = append(info, "Arrival")
			}
			info = append(info, ac.Nav.Summary(ac))

			if ap, _ := ac.getApproach(ac.ApproachId, ctx.world); ap != nil {
				if ac.ApproachCleared {
					info = append(info, "Cleared "+ap.FullName+" approach")
				} else {
					info = append(info, "Expecting "+ap.FullName+" approach")
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
			state := sp.aircraft[ac.Callsign]
			pac := transforms.WindowFromLatLongP(state.TrackPosition())

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

func starsButtonSize(flags int, scale float32) imgui.Vec2 {
	if (flags & STARSButtonFull) != 0 {
		return imgui.Vec2{X: scale * STARSButtonWidth, Y: scale * STARSButtonHeight}
	} else if (flags & STARSButtonHalfVertical) != 0 {
		return imgui.Vec2{X: scale * STARSButtonWidth, Y: scale * (STARSButtonHeight / 2)}
	} else if (flags & STARSButtonHalfHorizontal) != 0 {
		return imgui.Vec2{X: scale * (STARSButtonWidth / 2), Y: scale * STARSButtonHeight}
	} else {
		lg.Errorf("unhandled starsButtonFlags %d", flags)
		return imgui.Vec2{X: scale * STARSButtonWidth, Y: scale * STARSButtonHeight}
	}
}

func (sp *STARSPane) StartDrawDCB(ctx *PaneContext, scale float32) {
	var flags imgui.WindowFlags
	flags = imgui.WindowFlagsNoDecoration
	flags |= imgui.WindowFlagsNoSavedSettings
	flags |= imgui.WindowFlagsNoNav
	flags |= imgui.WindowFlagsNoResize
	flags |= imgui.WindowFlagsNoScrollWithMouse
	flags |= imgui.WindowFlagsNoBackground

	starsBarWindowPos = imgui.Vec2{
		X: ctx.paneExtent.p0[0],
		Y: float32(ctx.platform.WindowSize()[1]) - ctx.paneExtent.p1[1] + 1}
	imgui.SetNextWindowPosV(starsBarWindowPos, imgui.ConditionAlways, imgui.Vec2{})
	imgui.SetNextWindowSize(imgui.Vec2{ctx.paneExtent.Width() - 2, scale * STARSButtonHeight})
	imgui.BeginV(fmt.Sprintf("STARS Button Bar##%p", sp), nil, flags)

	//	imgui.WindowDrawList().AddRectFilledV(imgui.Vec2{}, imgui.Vec2{X: ctx.paneExtent.Width() - 2, Y: STARSButtonHeight},
	//		0xff0000ff, 1, 0)

	buttonFont := GetFont(FontIdentifier{Name: "Fixed Demi Bold", Size: globalConfig.DCBFontSize})
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

func updateImguiCursor(flags int, pos imgui.Vec2, buttonScale float32) {
	if (flags&STARSButtonFull) != 0 || (flags&STARSButtonHalfHorizontal) != 0 {
		imgui.SameLine()
	} else if (flags & STARSButtonHalfVertical) != 0 {
		if pos.Y == 0 {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X, Y: buttonScale * (STARSButtonHeight / 2)})
		} else {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X + buttonScale*STARSButtonWidth, Y: 0})
		}
	} else {
		lg.Errorf("unhandled starsButtonFlags %d", flags)
	}
}

func STARSToggleButton(text string, state *bool, flags int, buttonScale float32) (clicked bool) {
	startPos := imgui.CursorPos()
	if *state {
		imgui.PushID(text) // TODO why: comes from Middleton's Draw() method
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorButtonActive))
		imgui.ButtonV(text, starsButtonSize(flags, buttonScale))
		if imgui.IsItemClicked() {
			*state = false
			clicked = true
		}
		imgui.PopStyleColorV(1)
		imgui.PopID()
	} else if imgui.ButtonV(text, starsButtonSize(flags, buttonScale)) {
		*state = true
		clicked = true
	}
	updateImguiCursor(flags, startPos, buttonScale)
	return
}

var (
	// TODO: think about implications of multiple STARSPanes being active
	// at once w.r.t. this.  This probably should be a member variable,
	// though we also need to think about focus capture; probably should
	// force take it when a spinner is active..
	activeSpinner unsafe.Pointer
)

func STARSIntSpinner(ctx *PaneContext, text string, value *int, min int, max int, flags int, buttonScale float32) {
	STARSCallbackSpinner[int](ctx, text, value,
		func(v int) string { return fmt.Sprintf("%d", v) },
		func(v int, delta float32) int {
			di := 0
			if delta > 0 {
				di = 1
			} else if delta < 0 {
				di = -1
			}
			return clamp(v+di, min, max)
		}, flags, buttonScale)
}

func STARSCallbackSpinner[V any](ctx *PaneContext, text string, value *V, print func(v V) string,
	callback func(v V, delta float32) V, flags int, buttonScale float32) {
	text += print(*value)

	pos := imgui.CursorPos()
	buttonSize := starsButtonSize(flags, buttonScale)
	if activeSpinner == unsafe.Pointer(value) {
		buttonBounds := Extent2D{
			p0: [2]float32{pos.X, pos.Y},
			p1: [2]float32{pos.X + buttonSize.X, pos.Y + buttonSize.Y}}
		buttonBounds.p0 = add2f(buttonBounds.p0, [2]float32{starsBarWindowPos.X, starsBarWindowPos.Y})
		buttonBounds.p1 = add2f(buttonBounds.p1, [2]float32{starsBarWindowPos.X, starsBarWindowPos.Y})
		ctx.platform.StartCaptureMouse(buttonBounds)

		imgui.PushID(text) // TODO why: comes from ModalButtonSet Draw() method
		h := imgui.CurrentStyle().Color(imgui.StyleColorButtonActive)
		imgui.PushStyleColor(imgui.StyleColorButton, h)
		imgui.ButtonV(text, buttonSize)
		if imgui.IsItemClicked() {
			activeSpinner = nil
			ctx.platform.EndCaptureMouse()
		}

		_, wy := imgui.CurrentIO().MouseWheel()
		*value = callback(*value, wy)

		imgui.PopStyleColorV(1)
		imgui.PopID()
	} else if imgui.ButtonV(text, buttonSize) {
		activeSpinner = unsafe.Pointer(value)
	}
	updateImguiCursor(flags, pos, buttonScale)
}

func STARSFloatSpinner(ctx *PaneContext, text string, value *float32, min float32, max float32, flags int, buttonScale float32) {
	STARSCallbackSpinner(ctx, text, value, func(f float32) string { return fmt.Sprintf("%.1f", *value) },
		func(v float32, delta float32) float32 {
			return clamp(v+delta/10, min, max)
		}, flags, buttonScale)
}

func STARSBrightnessSpinner(ctx *PaneContext, text string, b *STARSBrightness, flags int, buttonScale float32) {
	STARSCallbackSpinner(ctx, text, b,
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
		}, flags, buttonScale)
}

func STARSSelectButton(text string, flags int, buttonScale float32) bool {
	pos := imgui.CursorPos()
	if flags&STARSButtonSelected != 0 {
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorButtonActive))
	}
	result := imgui.ButtonV(text, starsButtonSize(flags, buttonScale))
	if flags&STARSButtonSelected != 0 {
		imgui.PopStyleColorV(1)
	}
	updateImguiCursor(flags, pos, buttonScale)
	return result
}

func STARSDisabledButton(text string, flags int, buttonScale float32) {
	pos := imgui.CursorPos()
	imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
	imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.3, .3, .3, 1})
	imgui.ButtonV(text, starsButtonSize(flags, buttonScale))
	imgui.PopStyleColorV(1)
	imgui.PopItemFlag()
	updateImguiCursor(flags, pos, buttonScale)
}

///////////////////////////////////////////////////////////////////////////
// STARSPane utility methods

// amendFlightPlan is a useful utility function for changing an entry in
// the flightplan; the provided callback function should make the update
// and the rest of the details are handled here.
func amendFlightPlan(w *World, callsign string, amend func(fp *FlightPlan)) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		fp := Select(ac.FlightPlan != nil, ac.FlightPlan, &FlightPlan{})
		amend(fp)
		return w.AmendFlightPlan(callsign, *fp)
	}
}

func (sp *STARSPane) initializeSystemFonts() {
	for i, sz := range []int{9, 11, 12, 13, 14, 16} {
		id := FontIdentifier{Name: "Fixed Demi Bold", Size: sz}
		sp.systemFont[i] = GetFont(id)
		if sp.systemFont[i] == nil {
			lg.Errorf("Font not found for %+v", id)
			sp.systemFont[i] = GetDefaultFont()
		}
	}
}

func (sp *STARSPane) initializeAircraft(w *World) {
	// Reset and initialize all of these
	sp.aircraft = make(map[string]*STARSAircraftState)
	//sp.ghostAircraft = make(map[*Aircraft]*Aircraft)

	if w != nil {
		for _, ac := range w.GetAllAircraft() {
			sa := &STARSAircraftState{}
			sp.aircraft[ac.Callsign] = sa
			if ac.TrackingController == w.Callsign || ac.ControllingController == w.Callsign {
				sa.datablockType = FullDatablock
			}

			/*
				if !ps.DisableCRDA {
					if ghost := sp.Facility.CRDAConfig.GetGhost(ac); ghost != nil {
						sp.ghostAircraft[ac] = ghost
						sp.aircraft[ghost] = &STARSAircraftState{
							isGhost:        true,
							displayTPASize: ps.DisplayTPASize,
						}
					}
				}
			*/
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

func (sp *STARSPane) multiRadarMode(w *World) bool {
	ps := sp.CurrentPreferenceSet
	_, ok := w.RadarSites[ps.RadarSiteSelected]
	return ps.RadarSiteSelected == "" || !ok
}

func (sp *STARSPane) radarVisibility(w *World, pos Point2LL, alt int) (primary, secondary bool, distance float32) {
	ps := sp.CurrentPreferenceSet
	distance = 1e30
	multi := sp.multiRadarMode(w)
	for id, site := range w.RadarSites {
		if !multi && ps.RadarSiteSelected != id {
			continue
		}

		if p, s, dist := site.CheckVisibility(w, pos, alt); p || s {
			primary = primary || p
			secondary = secondary || s
			distance = min(distance, dist)
		}
	}

	return
}

func (sp *STARSPane) visibleAircraft(w *World) []*Aircraft {
	var aircraft []*Aircraft
	ps := sp.CurrentPreferenceSet
	multi := sp.multiRadarMode(w)

	for callsign := range sp.aircraft {
		ac, ok := w.Aircraft[callsign]
		if !ok {
			continue
		}
		state, ok := sp.aircraft[callsign]
		if !ok {
			continue
		}

		// Is it on the ground?
		if ac.FlightPlan != nil {
			if ap := w.GetAirport(ac.FlightPlan.DepartureAirport); ap != nil {
				if int(ac.Altitude)-ap.Elevation < 100 && nmdistance2ll(ac.Position, ap.Location) < 2 {
					continue
				}
			}
			if ap := w.GetAirport(ac.FlightPlan.ArrivalAirport); ap != nil {
				if int(ac.Altitude)-ap.Elevation < 100 && nmdistance2ll(ac.Position, ap.Location) < 2 {
					continue
				}
			}
		}

		for id, site := range w.RadarSites {
			if !multi && ps.RadarSiteSelected != id {
				continue
			}

			if p, s, _ := site.CheckVisibility(w, state.TrackPosition(), state.TrackAltitude()); p || s {
				aircraft = append(aircraft, ac)
				break
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) datablockVisible(ac *Aircraft) bool {
	af := sp.CurrentPreferenceSet.AltitudeFilters
	alt := sp.aircraft[ac.Callsign].TrackAltitude()
	if !ac.IsAssociated() {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) getLeaderLineDirection(ac *Aircraft) CardinalOrdinalDirection {
	if lld := sp.aircraft[ac.Callsign].leaderLineDirection; lld != nil {
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

func (sp *STARSPane) isOverflight(ctx *PaneContext, ac *Aircraft) bool {
	return ac.FlightPlan != nil &&
		(ctx.world.GetAirport(ac.FlightPlan.DepartureAirport) == nil &&
			ctx.world.GetAirport(ac.FlightPlan.ArrivalAirport) == nil)
}

func (sp *STARSPane) tryGetClickedAircraft(w *World, mousePosition [2]float32, transforms ScopeTransformations) *Aircraft {
	var ac *Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft(w) {
		pw := transforms.WindowFromLatLongP(sp.aircraft[a.Callsign].TrackPosition())
		dist := distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac
}

func (sp *STARSPane) radarSiteId(w *World) string {
	ps := sp.CurrentPreferenceSet
	if _, ok := w.RadarSites[ps.RadarSiteSelected]; ok && ps.RadarSiteSelected != "" {
		return ps.RadarSiteSelected
	}
	return "MULTI"
}
