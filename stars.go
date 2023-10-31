// stars.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Main missing features:
// Altitude alerts

package main

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"github.com/mmp/imgui-go/v4"
)

// IFR TRACON separation requirements
const LateralMinimum = 3
const VerticalMinimum = 1000

var (
	STARSBackgroundColor    = RGB{.2, .2, .2} // at 100 contrast
	STARSListColor          = RGB{.1, .9, .1}
	STARSTextAlertColor     = RGB{1, 0, 0}
	STARSMapColor           = RGB{.55, .55, .55}
	STARSCompassColor       = RGB{.55, .55, .55}
	STARSRangeRingColor     = RGB{.55, .55, .55}
	STARSTrackBlockColor    = RGB{0.12, 0.48, 1}
	STARSTrackHistoryColors = [5]RGB{
		RGB{.12, .31, .78},
		RGB{.28, .28, .67},
		RGB{.2, .2, .51},
		RGB{.16, .16, .43},
		RGB{.12, .12, .35},
	}
	STARSJRingConeColor         = RGB{.5, .5, 1}
	STARSTrackedAircraftColor   = RGB{1, 1, 1}
	STARSUntrackedAircraftColor = RGB{0, 1, 0}
	STARSInboundPointOutColor   = RGB{1, 1, 0}
	STARSGhostColor             = RGB{1, 1, 0}
	STARSSelectedAircraftColor  = RGB{0, 1, 1}

	STARSDCBButtonColor         = RGB{0, .4, 0}
	STARSDCBActiveButtonColor   = RGB{0, .8, 0}
	STARSDCBTextColor           = RGB{1, 1, 1}
	STARSDCBTextSelectedColor   = RGB{1, 1, 0}
	STARSDCBDisabledButtonColor = RGB{.4, .4, .4}
	STARSDCBDisabledTextColor   = RGB{.8, .8, .8}
)

const NumSTARSPreferenceSets = 32
const NumSTARSMaps = 28

type STARSPane struct {
	CurrentPreferenceSet  STARSPreferenceSet
	SelectedPreferenceSet int
	PreferenceSets        []STARSPreferenceSet

	SystemMaps map[int]*STARSMap

	weatherRadar WeatherRadar

	systemFont [6]*Font
	dcbFont    [3]*Font // 0, 1, 2 only

	events *EventsSubscription

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	Aircraft map[string]*STARSAircraftState

	AircraftToIndex map[string]int // for use in lists
	IndexToAircraft map[int]string // map is sort of wasteful since it's dense, but...

	// explicit JSON name to avoid errors during config deserialization for
	// backwards compatibility, since this used to be a
	// map[string]interface{}.
	AutoTrackDepartures bool `json:"autotrack_departures"`

	// callsign -> controller id
	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string
	RejectedPointOuts map[string]interface{}

	queryUnassociated *TransientMap[string, interface{}]

	RangeBearingLines []STARSRangeBearingLine
	MinSepAircraft    [2]string

	CAAircraft []CAAircraft

	// For CRDA
	ConvergingRunways []STARSConvergingRunways

	// Various UI state
	scopeClickHandler   func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus
	activeDCBMenu       int
	selectedPlaceButton string

	dwellAircraft     string
	drawRouteAircraft string

	commandMode       CommandMode
	multiFuncPrefix   string
	previewAreaOutput string
	previewAreaInput  string

	HavePlayedSPCAlertSound map[string]interface{}

	lastTrackUpdate time.Time
	discardTracks   bool

	LastCASoundTime time.Time

	drawApproachAirspace  bool
	drawDepartureAirspace bool

	// The start of a RBL--one click received, waiting for the second.
	wipRBL STARSRangeBearingLine
}

type STARSRangeBearingLine struct {
	P [2]struct {
		// If callsign is given, use that aircraft's position;
		// otherwise we have a fixed position.
		Loc      Point2LL
		Callsign string
	}
}

func (rbl STARSRangeBearingLine) GetPoints(ctx *PaneContext, aircraft []*Aircraft, sp *STARSPane) (p0, p1 Point2LL) {
	// Each line endpoint may be specified either by an aircraft's
	// position or by a fixed position. We'll start with the fixed
	// position and then override it if there's a valid *Aircraft.
	p0, p1 = rbl.P[0].Loc, rbl.P[1].Loc
	if ac := ctx.world.Aircraft[rbl.P[0].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.world.CurrentTime()) && Find(aircraft, ac) != -1 {
			p0 = state.TrackPosition()
		}
	}
	if ac := ctx.world.Aircraft[rbl.P[1].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.world.CurrentTime()) && Find(aircraft, ac) != -1 {
			p1 = state.TrackPosition()
		}
	}
	return
}

type CAAircraft struct {
	Callsigns    [2]string // sorted alphabetically
	Acknowledged bool
}

type QuickLookPosition struct {
	Callsign string
	Id       string
	Plus     bool
}

func parseQuickLookPositions(w *World, s string) ([]QuickLookPosition, string, error) {
	var positions []QuickLookPosition

	ctrl, ok := w.Controllers[w.Callsign]
	if !ok {
		lg.Errorf("%s: couldn't get *Controller for us?", w.Callsign)
		return nil, s, ErrSTARSIllegalPosition
	}
	num := string(ctrl.SectorId[0])

	idx := 0

	tryAddController := func(id string) bool {
		ctrl := w.GetController(id)
		if ctrl != nil {
			plus := idx < len(s) && s[idx] == '+'
			if plus {
				idx++
			}
			positions = append(positions, QuickLookPosition{
				Callsign: ctrl.Callsign,
				Id:       id,
				Plus:     plus,
			})
		}
		return ctrl != nil
	}

	// per 6-94, this is "fun"
	// - in general the string is a list of TCPs / sector ids.
	// - each may have a plus at the end
	// - if a single character id is entered, then we prepend the number for
	//   the current controller's sector id. in that case a space is required
	//   before the next one, if any
	id := ""
	for idx < len(s) {
		// Start work on a new controller id
		id = string(s[idx])
		idx++
		if id == " " {
			// Allow multiple spaces between ids
			continue
		}

		// Just a single character id (maybe with a plus), so prepend
		// our id number and see if it works.
		if tryAddController(num + id) {
			// Must have a space next (or be done)
			if idx < len(s) && s[idx] != ' ' {
				return positions, s[idx:], ErrSTARSIllegalParam
			}
		} else {
			// Multi-character position specification
			for idx < len(s) {
				// Loop precondition: we have a partial but invalid specification.
				id += string(s[idx])
				idx++

				if tryAddController(id) {
					id = ""
					break
				}
			}
			if id != "" {
				return positions, id, ErrSTARSIllegalPosition
			}
		}
	}

	return positions, "", nil
}

type CRDAMode int

const (
	CRDAModeStagger = iota
	CRDAModeTie
)

// this is read-only, stored in STARSPane for convenience
type STARSConvergingRunways struct {
	ConvergingRunways
	ApproachRegions [2]*ApproachRegion
	Airport         string
	Index           int
}

type CRDARunwayState struct {
	Enabled                 bool
	LeaderLineDirection     *CardinalOrdinalDirection // nil -> unset
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
	// Radar tracks are maintained as a ring buffer where tracksIndex is
	// the index of the next track to be written.  (Thus, tracksIndex==0
	// implies that there are no tracks.)  In FUSED mode, radar tracks are
	// updated once per second; otherwise they are updated once every 5
	// seconds. Changing to/from FUSED mode causes tracksIndex to be reset,
	// thus discarding previous tracks.
	tracks      [50]RadarTrack
	tracksIndex int

	DatablockType DatablockType

	IsSelected bool // middle click

	// Only drawn if non-zero
	JRingRadius    float32
	ConeLength     float32
	DisplayTPASize bool // flip this so that zero-init works here? (What is the default?)

	// This is only set if a leader line direction was specified for this
	// aircraft individually
	LeaderLineDirection *CardinalOrdinalDirection

	Ghost struct {
		PartialDatablock bool
		State            GhostState
	}

	displayPilotAltitude bool
	pilotAltitude        int

	DisplayReportedBeacon bool // note: only for unassociated
	DisplayPTL            bool
	DisableCAWarnings     bool
	DisableMSAW           bool
	InhibitMSAWAlert      bool // only applies if in an alert. clear when alert is over?

	SPCOverride string

	FirstSeen           time.Time
	FirstRadarTrack     time.Time
	HaveEnteredAirspace bool

	IdentEnd                time.Time
	OutboundHandoffAccepted bool
	OutboundHandoffFlashEnd time.Time
}

type GhostState int

const (
	GhostStateRegular = iota
	GhostStateSuppressed
	GhostStateForced
)

func (s *STARSAircraftState) TrackAltitude() int {
	idx := (s.tracksIndex - 1) % len(s.tracks)
	return s.tracks[idx].Altitude
}

func (s *STARSAircraftState) TrackPosition() Point2LL {
	idx := (s.tracksIndex - 1) % len(s.tracks)
	return s.tracks[idx].Position
}

func (s *STARSAircraftState) TrackGroundspeed() int {
	idx := (s.tracksIndex - 1) % len(s.tracks)
	return s.tracks[idx].Groundspeed
}

func (s *STARSAircraftState) HaveHeading() bool {
	return s.tracksIndex > 1
}

// Note that the vector returned by HeadingVector() is along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (s *STARSAircraftState) HeadingVector(nmPerLongitude, magneticVariation float32) Point2LL {
	if !s.HaveHeading() {
		return Point2LL{}
	}

	idx0, idx1 := (s.tracksIndex-1)%len(s.tracks), (s.tracksIndex-2)%len(s.tracks)

	p0 := ll2nm(s.tracks[idx0].Position, nmPerLongitude)
	p1 := ll2nm(s.tracks[idx1].Position, nmPerLongitude)
	v := sub2ll(p0, p1)
	v = normalize2f(v)
	// v's length should be groundspeed / 60 nm.
	v = scale2f(v, float32(s.TrackGroundspeed())/60) // hours to minutes
	return nm2ll(v, nmPerLongitude)
}

func (s *STARSAircraftState) TrackHeading(nmPerLongitude float32) float32 {
	if !s.HaveHeading() {
		return 0
	}
	idx0, idx1 := (s.tracksIndex-1)%len(s.tracks), (s.tracksIndex-2)%len(s.tracks)
	return headingp2ll(s.tracks[idx1].Position, s.tracks[idx0].Position, nmPerLongitude, 0)
}

func (s *STARSAircraftState) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	idx := (s.tracksIndex - 1) % len(s.tracks)
	return s.tracksIndex == 0 || now.Sub(s.tracks[idx].Time) > 30*time.Second
}

func (s *STARSAircraftState) Ident() bool {
	return !s.IdentEnd.IsZero() && s.IdentEnd.After(time.Now())
}

type STARSMap struct {
	Label         string        `json:"label"`
	Group         int           `json:"group"` // 0 -> A, 1 -> B
	Name          string        `json:"name"`
	CommandBuffer CommandBuffer `json:"command_buffer"`
}

///////////////////////////////////////////////////////////////////////////
// STARSPreferenceSet

type STARSPreferenceSet struct {
	Name string

	DisplayDCB  bool
	DCBPosition int

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

	// If empty, then then MULTI or FUSED mode, depending on
	// FusedRadarMode.  The custom JSON name is so we don't get errors
	// parsing old configs, which stored this as an array...
	RadarSiteSelected string `json:"RadarSiteSelectedName"`
	FusedRadarMode    bool

	// For tracked by the user
	LeaderLineDirection CardinalOrdinalDirection
	LeaderLineLength    int // 0-7
	// For tracked by other controllers
	ControllerLeaderLineDirections map[string]CardinalOrdinalDirection
	// If not specified in ControllerLeaderLineDirections...
	OtherControllerLeaderLineDirection *CardinalOrdinalDirection
	// Only set if specified by the user (and not used currently...)
	UnassociatedLeaderLineDirection *CardinalOrdinalDirection

	AltitudeFilters struct {
		Unassociated [2]int // low, high
		Associated   [2]int
	}

	QuickLookAll       bool
	QuickLookAllIsPlus bool
	QuickLookPositions []QuickLookPosition

	CRDA struct {
		Disabled bool
		// Has the same size and indexing as corresponding STARSPane
		// STARSConvergingRunways
		RunwayPairState []CRDARunwayPairState
		ForceAllGhosts  bool
	}

	DisplayLDBBeaconCodes bool // TODO: default?
	SelectedBeaconCodes   []string

	// TODO: review--should some of the below not be in prefs but be in STARSPane?

	DisplayUncorrelatedTargets bool

	DisableCAWarnings bool
	DisableMSAW       bool

	OverflightFullDatablocks bool
	AutomaticFDBOffset       bool

	DisplayTPASize bool

	VideoMapVisible  map[string]interface{}
	SystemMapVisible map[int]interface{}

	PTLLength      float32
	PTLOwn, PTLAll bool

	DwellMode DwellMode

	TopDownMode     bool
	GroundRangeMode bool

	Bookmarks [10]struct {
		Center      Point2LL
		Range       float32
		TopDownMode bool
	}

	Brightness struct {
		DCB                STARSBrightness
		BackgroundContrast STARSBrightness
		VideoGroupA        STARSBrightness
		VideoGroupB        STARSBrightness
		FullDatablocks     STARSBrightness
		Lists              STARSBrightness
		Positions          STARSBrightness
		LimitedDatablocks  STARSBrightness
		OtherTracks        STARSBrightness
		Lines              STARSBrightness
		RangeRings         STARSBrightness
		Compass            STARSBrightness
		BeaconSymbols      STARSBrightness
		PrimarySymbols     STARSBrightness
		History            STARSBrightness
		Weather            STARSBrightness
		WxContrast         STARSBrightness
	}

	CharSize struct {
		DCB             int
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
		Position  [2]float32
		Visible   bool
		Selection VideoMapsGroup
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

type VideoMapsGroup int

const (
	VideoMapsGroupGeo = iota
	VideoMapsGroupSysProc
)

func (ps *STARSPreferenceSet) ResetCRDAState(rwys []STARSConvergingRunways) {
	ps.CRDA.RunwayPairState = nil
	state := CRDARunwayPairState{}
	// The first runway is enabled by default
	state.RunwayState[0].Enabled = true
	for range rwys {
		ps.CRDA.RunwayPairState = append(ps.CRDA.RunwayPairState, state)
	}
}

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

const (
	DCBPositionTop = iota
	DCBPositionLeft
	DCBPositionRight
	DCBPositionBottom
)

func (sp *STARSPane) MakePreferenceSet(name string, w *World) STARSPreferenceSet {
	var ps STARSPreferenceSet

	ps.Name = name

	ps.DisplayDCB = true
	ps.DCBPosition = DCBPositionTop

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
	ps.SystemMapVisible = make(map[int]interface{})

	ps.LeaderLineDirection = North
	ps.LeaderLineLength = 1

	ps.AltitudeFilters.Unassociated = [2]int{100, 60000}
	ps.AltitudeFilters.Associated = [2]int{100, 60000}

	ps.DisplayUncorrelatedTargets = true

	ps.DisplayTPASize = true

	ps.PTLLength = 1

	ps.Brightness.DCB = 60
	ps.Brightness.BackgroundContrast = 0
	ps.Brightness.VideoGroupA = 50
	ps.Brightness.VideoGroupB = 40
	ps.Brightness.FullDatablocks = 80
	ps.Brightness.Lists = 80
	ps.Brightness.Positions = 80
	ps.Brightness.LimitedDatablocks = 80
	ps.Brightness.OtherTracks = 80
	ps.Brightness.Lines = 40
	ps.Brightness.RangeRings = 20
	ps.Brightness.Compass = 40
	ps.Brightness.BeaconSymbols = 55
	ps.Brightness.PrimarySymbols = 80
	ps.Brightness.History = 60
	ps.Brightness.Weather = 30
	ps.Brightness.WxContrast = 30

	ps.CharSize.DCB = 1
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

	ps.AlertList.Position = [2]float32{.8, .25}
	ps.AlertList.Lines = 5
	ps.AlertList.Visible = true

	ps.CoastList.Position = [2]float32{.8, .65}
	ps.CoastList.Lines = 5
	ps.CoastList.Visible = false

	ps.SignOnList.Position = [2]float32{.8, .95}
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

	ps.ResetCRDAState(sp.ConvergingRunways)

	return ps
}

func (ps *STARSPreferenceSet) Duplicate() STARSPreferenceSet {
	dupe := *ps
	dupe.SelectedBeaconCodes = DuplicateSlice(ps.SelectedBeaconCodes)
	dupe.CRDA.RunwayPairState = DuplicateSlice(ps.CRDA.RunwayPairState)
	dupe.VideoMapVisible = DuplicateMap(ps.VideoMapVisible)
	dupe.SystemMapVisible = DuplicateMap(ps.SystemMapVisible)
	return dupe
}

func (ps *STARSPreferenceSet) Activate(w *World) {
	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[string]interface{})
		if w != nil && len(w.STARSMaps) > 0 {
			ps.VideoMapVisible[w.STARSMaps[0].Name] = nil
		}
	}
	if ps.SystemMapVisible == nil {
		ps.SystemMapVisible = make(map[int]interface{})
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

// See STARS Operators Manual 5-184...
func (sp *STARSPane) flightPlanSTARS(w *World, ac *Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalFlight
	}

	fmtTime := func(t time.Time) string {
		return t.UTC().Format("1504")
	}

	// Common stuff
	owner := ""
	if ctrl := w.GetController(ac.TrackingController); ctrl != nil {
		owner = ctrl.SectorId
	}

	numType := ""
	if num, ok := sp.AircraftToIndex[ac.Callsign]; ok {
		numType += fmt.Sprintf("%d/", num)
	}
	numType += fp.AircraftType

	state := sp.Aircraft[ac.Callsign]

	result := ac.Callsign + " " // all start with aricraft id
	if ac.IsDeparture() {
		if state.FirstRadarTrack.IsZero() {
			// Proposed departure
			result += numType + " "
			result += ac.AssignedSquawk.String() + " " + owner + "\n"

			if len(fp.DepartureAirport) > 0 {
				result += fp.DepartureAirport[1:] + " "
			}
			result += ac.Scratchpad + " "
			result += "P" + fmtTime(state.FirstSeen) + " "
			result += "R" + fmt.Sprintf("%03d", fp.Altitude/100)
		} else {
			// Active departure
			result += ac.AssignedSquawk.String() + " "
			if len(fp.DepartureAirport) > 0 {
				result += fp.DepartureAirport[1:] + " "
			}
			result += "D" + fmtTime(state.FirstRadarTrack) + " "
			result += fmt.Sprintf("%03d", int(ac.Altitude())/100) + "\n"

			result += ac.Scratchpad + " "
			result += "R" + fmt.Sprintf("%03d", fp.Altitude/100) + " "

			result += numType
		}
	} else {
		// Format it as an arrival (we don't do overflights...)
		result += numType + " "
		result += ac.AssignedSquawk.String() + " "
		result += owner + " "
		result += fmt.Sprintf("%03d", int(ac.Altitude())/100) + "\n"

		// Use the last item in the route for the entry fix
		routeFields := strings.Fields(fp.Route)
		if n := len(routeFields); n > 0 {
			result += routeFields[n-1] + " "
		}
		result += "A" + fmtTime(state.FirstRadarTrack) + " "
		if len(fp.ArrivalAirport) > 0 {
			result += fp.ArrivalAirport[1:] + " "
		}
	}

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

func NewSTARSPane(w *World) *STARSPane {
	sp := &STARSPane{
		SelectedPreferenceSet: -1,
	}
	sp.CurrentPreferenceSet = sp.MakePreferenceSet("", w)
	return sp
}

func (sp *STARSPane) Name() string { return "STARS" }

func (sp *STARSPane) Activate(w *World, eventStream *EventStream) {
	if sp.CurrentPreferenceSet.Range == 0 || sp.CurrentPreferenceSet.Center.IsZero() {
		// First launch after switching over to serializing the CurrentPreferenceSet...
		sp.CurrentPreferenceSet = sp.MakePreferenceSet("", w)
	}
	sp.CurrentPreferenceSet.Activate(w)

	if sp.HavePlayedSPCAlertSound == nil {
		sp.HavePlayedSPCAlertSound = make(map[string]interface{})
	}
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
		sp.queryUnassociated = NewTransientMap[string, interface{}]()
	}

	sp.initializeFonts()

	if sp.Aircraft == nil {
		sp.Aircraft = make(map[string]*STARSAircraftState)
	}

	if sp.AircraftToIndex == nil {
		sp.AircraftToIndex = make(map[string]int)
	}
	if sp.IndexToAircraft == nil {
		sp.IndexToAircraft = make(map[int]string)
	}

	sp.events = eventStream.Subscribe()

	ps := sp.CurrentPreferenceSet
	if ps.Brightness.Weather != 0 {
		sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
	}

	sp.lastTrackUpdate = time.Time{} // force immediate update at start
}

func (sp *STARSPane) Deactivate() {
	// Drop all of them
	sp.Aircraft = nil

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
	ps.SystemMapVisible = make(map[int]interface{})

	sp.SystemMaps = makeSystemMaps(w)

	ps.CurrentATIS = ""
	for i := range ps.GIText {
		ps.GIText[i] = ""
	}
	ps.RadarSiteSelected = ""

	sp.ConvergingRunways = nil
	for _, name := range SortedMapKeys(w.Airports) {
		ap := w.Airports[name]
		for idx, pair := range ap.ConvergingRunways {
			sp.ConvergingRunways = append(sp.ConvergingRunways, STARSConvergingRunways{
				ConvergingRunways: pair,
				ApproachRegions: [2]*ApproachRegion{ap.ApproachRegions[pair.Runways[0]],
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
}

func makeSystemMaps(w *World) map[int]*STARSMap {
	maps := make(map[int]*STARSMap)

	// CA suppression filters
	csf := &STARSMap{
		Label: "ALLCASU",
		Name:  "ALL CA SUPPRESSION FILTERS",
	}
	for _, vol := range w.InhibitCAVolumes {
		vol.GenerateDrawCommands(&csf.CommandBuffer, w.NmPerLongitude)
	}
	maps[400] = csf

	// Radar maps
	radarIndex := 701
	for _, name := range SortedMapKeys(w.RadarSites) {
		sm := &STARSMap{
			Label: name + "RCM",
			Name:  name + " RADAR COVERAGE MAP",
		}

		site := w.RadarSites[name]
		ld := GetLinesDrawBuilder()
		ld.AddLatLongCircle(site.Position, w.NmPerLongitude, float32(site.PrimaryRange), 360)
		ld.AddLatLongCircle(site.Position, w.NmPerLongitude, float32(site.SecondaryRange), 360)
		ld.GenerateCommands(&sm.CommandBuffer)
		maps[radarIndex] = sm

		radarIndex++
		ReturnLinesDrawBuilder(ld)
	}

	return maps
}

func (sp *STARSPane) DrawUI() {
	imgui.Checkbox("Auto track departures", &sp.AutoTrackDepartures)
}

func (sp *STARSPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STARSPane) processEvents(w *World) {
	// First handle changes in world.Aircraft
	for callsign, ac := range w.Aircraft {
		if _, ok := sp.Aircraft[callsign]; !ok {
			// First we've seen it; create the *STARSAircraftState for it
			sa := &STARSAircraftState{}
			if ac.TrackingController == w.Callsign || ac.ControllingController == w.Callsign {
				sa.DatablockType = FullDatablock
			}
			sp.Aircraft[callsign] = sa

			sa.FirstSeen = w.CurrentTime()
		}

		if squawkingSPC(ac.Squawk) {
			if _, ok := sp.HavePlayedSPCAlertSound[ac.Callsign]; !ok {
				sp.HavePlayedSPCAlertSound[ac.Callsign] = nil
				//globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
			}
		}
	}

	// See if any aircraft we have state for have been removed
	for callsign := range sp.Aircraft {
		if _, ok := w.Aircraft[callsign]; !ok {
			delete(sp.Aircraft, callsign)
		}
	}

	// Filter out any removed aircraft from the CA list
	sp.CAAircraft = FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		_, a := w.Aircraft[ca.Callsigns[0]]
		_, b := w.Aircraft[ca.Callsigns[1]]
		return a && b
	})

	for _, event := range sp.events.Get() {
		switch event.Type {
		case PointOutEvent:
			if event.ToController == w.Callsign {
				if ctrl := w.GetController(event.FromController); ctrl != nil {
					sp.InboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.InboundPointOuts[event.Callsign] = ""
				}
			}
			if event.FromController == w.Callsign {
				if ctrl := w.GetController(event.ToController); ctrl != nil {
					sp.OutboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.OutboundPointOuts[event.Callsign] = ""
				}
			}

		case AcknowledgedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl := w.GetController(event.FromController); ctrl != nil && ctrl.SectorId == id {
					delete(sp.OutboundPointOuts, event.Callsign)
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl := w.GetController(event.ToController); ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
				}
			}

		case RejectedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl := w.GetController(event.FromController); ctrl != nil && ctrl.SectorId == id {
					delete(sp.OutboundPointOuts, event.Callsign)
					sp.RejectedPointOuts[event.Callsign] = nil
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl := w.GetController(event.ToController); ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
				}
			}

		case OfferedHandoffEvent:
			if event.ToController == w.Callsign {
				globalConfig.Audio.PlaySound(AudioEventInboundHandoff)
			}

		case AcceptedHandoffEvent:
			if event.FromController == w.Callsign && event.ToController != w.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; !ok {
					lg.Errorf("%s: have AcceptedHandoffEvent but missing STARS state?", event.Callsign)
				} else {
					state.OutboundHandoffAccepted = true
					state.OutboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
					globalConfig.Audio.PlaySound(AudioEventHandoffAccepted)
				}
			}

		case IdentEvent:
			if state, ok := sp.Aircraft[event.Callsign]; !ok {
				lg.Errorf("%s: have IdentEvent but missing STARS state?", event.Callsign)
			} else {
				state.IdentEnd = time.Now().Add(10 * time.Second)
			}
		}
	}
}

func (sp *STARSPane) Upgrade(from, to int) {
	if from < 8 {
		sp.CurrentPreferenceSet.Brightness.DCB = 60
		sp.CurrentPreferenceSet.CharSize.DCB = 1
		for i := range sp.PreferenceSets {
			sp.PreferenceSets[i].Brightness.DCB = 60
			sp.PreferenceSets[i].CharSize.DCB = 1
		}
	}
	if from < 9 {
		remap := func(b *STARSBrightness) {
			*b = STARSBrightness(min(*b*2, 100))
		}
		remap(&sp.CurrentPreferenceSet.Brightness.VideoGroupA)
		remap(&sp.CurrentPreferenceSet.Brightness.VideoGroupB)
		remap(&sp.CurrentPreferenceSet.Brightness.RangeRings)
		remap(&sp.CurrentPreferenceSet.Brightness.Compass)
		for i := range sp.PreferenceSets {
			remap(&sp.PreferenceSets[i].Brightness.VideoGroupA)
			remap(&sp.PreferenceSets[i].Brightness.VideoGroupB)
			remap(&sp.PreferenceSets[i].Brightness.RangeRings)
			remap(&sp.PreferenceSets[i].Brightness.Compass)
		}
	}
	if from < 12 {
		if sp.CurrentPreferenceSet.Brightness.DCB == 0 {
			sp.CurrentPreferenceSet.Brightness.DCB = 60
		}
		for i := range sp.PreferenceSets {
			if sp.PreferenceSets[i].Brightness.DCB == 0 {
				sp.PreferenceSets[i].Brightness.DCB = 60
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

	ps := sp.CurrentPreferenceSet

	// Clear to background color
	cb.ClearRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))

	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		wmTakeKeyboardFocus(sp, false)
	}
	sp.processKeyboardInput(ctx)

	transforms := GetScopeTransformations(ctx.paneExtent, ctx.world.MagneticVariation, ctx.world.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range), 0)

	paneExtent := ctx.paneExtent
	if ps.DisplayDCB {
		paneExtent = sp.DrawDCB(ctx, transforms, cb)

		// Update scissor and viewport for what's left and to protect the DCB.
		cb.SetDrawBounds(paneExtent)

		// Clean up for the updated paneExtent that accounts for the space the DCB took.
		transforms = GetScopeTransformations(paneExtent, ctx.world.MagneticVariation, ctx.world.NmPerLongitude,
			sp.CurrentPreferenceSet.CurrentCenter, float32(ps.Range), 0)
		if ctx.mouse != nil {
			// The mouse position is provided in Pane coordinates, so that needs to be updated unless
			// the DCB is at the top, in which case it's unchanged.
			ms := *ctx.mouse
			ctx.mouse = &ms
			ctx.mouse.Pos[0] += ctx.paneExtent.p0[0] - paneExtent.p0[0]
			ctx.mouse.Pos[1] += ctx.paneExtent.p0[1] - paneExtent.p0[1]
		}
	}

	weatherIntensity := float32(ps.Brightness.Weather) / float32(100)
	sp.weatherRadar.Draw(ctx, weatherIntensity, transforms, cb)

	color := ps.Brightness.RangeRings.ScaleRGB(STARSRangeRingColor)
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

		color := ps.Brightness.VideoGroupA.ScaleRGB(STARSMapColor)
		if vmap.Group == 1 {
			color = ps.Brightness.VideoGroupB.ScaleRGB(STARSMapColor)
		}
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(vmap.CommandBuffer)
	}

	for _, idx := range SortedMapKeys(ps.SystemMapVisible) {
		color := ps.Brightness.VideoGroupA.ScaleRGB(STARSMapColor)
		cb.SetRGB(color)
		transforms.LoadLatLongViewingMatrices(cb)
		cb.Call(sp.SystemMaps[idx].CommandBuffer)
	}

	ctx.world.DrawScenarioRoutes(transforms, sp.systemFont[ps.CharSize.Tools],
		ps.Brightness.Lists.ScaleRGB(STARSListColor), cb)

	sp.drawCRDARegions(ctx, transforms, cb)
	sp.drawSelectedRoute(ctx, transforms, cb)

	transforms.LoadWindowViewingMatrices(cb)

	if ps.Brightness.Compass > 0 {
		cb.LineWidth(1)
		cbright := ps.Brightness.Compass.ScaleRGB(STARSCompassColor)
		font := sp.systemFont[ps.CharSize.Tools]
		DrawCompass(ps.CurrentCenter, ctx, 0, font, cbright, paneExtent, transforms, cb)
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

	sp.drawSystemLists(aircraft, ctx, paneExtent, transforms, cb)

	// Tools before datablocks
	sp.drawPTLs(aircraft, ctx, transforms, cb)
	sp.drawRingsAndCones(aircraft, ctx, transforms, cb)
	sp.drawRBLs(aircraft, ctx, transforms, cb)
	sp.drawMinSep(ctx, transforms, cb)
	sp.drawAirspace(ctx, transforms, cb)

	DrawHighlighted(ctx, transforms, cb)

	sp.drawTracks(aircraft, ctx, transforms, cb)
	sp.drawDatablocks(aircraft, ctx, transforms, cb)

	ghosts := sp.getGhostAircraft(aircraft, ctx)
	sp.drawGhosts(ghosts, ctx, transforms, cb)
	sp.consumeMouseEvents(ctx, ghosts, transforms, cb)
	sp.drawMouseCursor(ctx, paneExtent, transforms, cb)

	// Do this at the end of drawing so that we hold on to the tracks we
	// have for rendering the current frame.
	if sp.discardTracks {
		for _, state := range sp.Aircraft {
			state.tracksIndex = 0
		}
		sp.lastTrackUpdate = time.Time{} // force update
		sp.discardTracks = false
	}
}

func (sp *STARSPane) updateRadarTracks(w *World) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	now := w.CurrentTime()
	if sp.radarMode(w) == RadarModeFused {
		if now.Sub(sp.lastTrackUpdate) < 1*time.Second {
			return
		}
	} else {
		if now.Sub(sp.lastTrackUpdate) < 5*time.Second {
			return
		}
	}
	sp.lastTrackUpdate = now

	for callsign, state := range sp.Aircraft {
		ac, ok := w.Aircraft[callsign]
		if !ok {
			lg.Errorf("%s: not found in World Aircraft?", callsign)
			continue
		}

		idx := state.tracksIndex % len(state.tracks)
		state.tracks[idx] = RadarTrack{
			Position:    ac.Position(),
			Altitude:    int(ac.Altitude()),
			Groundspeed: int(ac.Nav.FlightState.GS),
			Time:        now,
		}
		state.tracksIndex++
	}

	sp.updateCAAircraft(w)
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

	//lg.Infof("input \"%s\" ctl %v alt %v", input, ctx.keyboard.IsPressed(KeyControl), ctx.keyboard.IsPressed(KeyAlt))
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
			sp.wipRBL = STARSRangeBearingLine{}

		case KeyF1:
			if ctx.keyboard.IsPressed(KeyControl) {
				// Recenter
				ps.Center = ctx.world.Center
				ps.CurrentCenter = ps.Center
			}

		case KeyF2:
			if ctx.keyboard.IsPressed(KeyControl) {
				if ps.DisplayDCB {
					sp.disableMenuSpinner(ctx)
					sp.activeDCBMenu = DCBMenuMaps
				}
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
	if idx, ok := sp.AircraftToIndex[ac.Callsign]; ok {
		return idx
	} else {
		idx := len(sp.AircraftToIndex) + 1
		sp.AircraftToIndex[ac.Callsign] = idx
		sp.IndexToAircraft[idx] = ac.Callsign
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
			if callsign, ok := sp.IndexToAircraft[idx]; ok {
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
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = !state.DisplayTPASize
			}
			status.clear = true
			return

		case "*T":
			// Remove all RBLs
			sp.wipRBL = STARSRangeBearingLine{}
			sp.RangeBearingLines = nil
			status.clear = true
			return

		case "**J":
			// remove all j-rings
			for _, state := range sp.Aircraft {
				state.JRingRadius = 0
			}
			status.clear = true
			return

		case "**P":
			// remove all cones
			for _, state := range sp.Aircraft {
				state.ConeLength = 0
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

		case ".ROUTE":
			sp.drawRouteAircraft = ""
			status.clear = true
			return
		}

		if len(cmd) >= 3 && cmd[:2] == "*T" {
			// Delete specified rbl
			if idx, err := strconv.Atoi(cmd[2:]); err == nil {
				idx--
				if idx >= 0 && idx < len(sp.RangeBearingLines) {
					sp.RangeBearingLines = DeleteSliceElement(sp.RangeBearingLines, idx)
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
			if f[0] == ".AUTOTRACK" && len(f) == 2 {
				if f[1] == "NONE" {
					sp.AutoTrackDepartures = false
					status.clear = true
					return
				} else if f[1] == "ALL" {
					sp.AutoTrackDepartures = true
					status.clear = true
					return
				}
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
			} else if ac := lookupAircraft(f[0]); ac != nil && len(f) > 1 {
				acCmds := strings.Join(f[1:], " ")
				ctx.world.RunAircraftCommands(ac, acCmds,
					func(err error) {
						globalConfig.Audio.PlaySound(AudioEventCommandError)
						sp.previewAreaOutput = GetSTARSError(err).Error()
					})

				status.clear = true
				return
			}
		}

	case CommandModeInitiateControl:
		if ac := lookupAircraft(cmd); ac == nil {
			status.err = ErrSTARSNoFlight
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

				state := sp.Aircraft[ac.Callsign]
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
			if cmd == "E" {
				ps.DwellMode = DwellModeOn
				status.clear = true
			} else if cmd == "L" {
				ps.DwellMode = DwellModeLock
				status.clear = true
			} else if cmd == "I" { // inhibit
				ps.DwellMode = DwellModeOff
				status.clear = true
			} else if len(cmd) == 1 {
				// illegal value for dwell
				status.err = ErrSTARSIllegalValue
			} else if ac := lookupAircraft(cmd); ac != nil {
				// D(callsign)
				// Display flight plan
				status.output, status.err = sp.flightPlanSTARS(ctx.world, ac)
				if status.err == nil {
					status.clear = true
				}
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
			if l := len(cmd); l == 0 {
				status.err = ErrSTARSCommandFormat
				return
			} else if l == 1 {
				if dir, ok := numpadToDirection(cmd[0]); ok && dir != nil {
					// Tracked by me
					ps.LeaderLineDirection = *dir
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if l == 2 {
				if dir, ok := numpadToDirection(cmd[0]); ok && cmd[1] == 'U' {
					// Unassociated tracks
					ps.UnassociatedLeaderLineDirection = dir
					status.clear = true
				} else if ok && cmd[1] == '*' {
					// Tracked by other controllers
					ps.OtherControllerLeaderLineDirection = dir
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if f := strings.Fields(cmd); len(f) == 2 {
				// either L(id)(space)(dir) or L(dir)(space)(callsign)
				if len(f[0]) == 1 {
					// L(dir)(space)(callsign)
					if dir, ok := numpadToDirection(f[0][0]); ok {
						if ac := lookupAircraft(f[1]); ac != nil {
							sp.Aircraft[ac.Callsign].LeaderLineDirection = dir
							status.clear = true
						} else {
							status.err = ErrSTARSNoFlight
						}
					} else {
						status.err = ErrSTARSCommandFormat
					}
					return
				} else {
					// L(id)(space)(dir)
					if ctrl := ctx.world.GetController(f[0]); ctrl != nil {
						if dir, ok := numpadToDirection(f[1][0]); ok && len(f[1]) == 1 {
							// Per-controller leaderline
							if ps.ControllerLeaderLineDirections == nil {
								ps.ControllerLeaderLineDirections = make(map[string]CardinalOrdinalDirection)
							}
							if dir != nil {
								ps.ControllerLeaderLineDirections[ctrl.Callsign] = *dir
							} else {
								delete(ps.ControllerLeaderLineDirections, ctrl.Callsign)
							}
							status.clear = true
						} else {
							status.err = ErrSTARSCommandFormat
						}
					} else {
						status.err = ErrSTARSIllegalPosition
					}
					return
				}
			}

		case "N":
			// CRDA...
			if cmd == "" {
				// Toggle CRDA processing (on by default). Note that when
				// it is disabled we still hold on to CRDARunwayPairState array so
				// that we're back where we started if CRDA is reenabled.
				ps.CRDA.Disabled = !ps.CRDA.Disabled
				status.clear = true
				return
			} else if cmd == "*ALL" {
				ps.CRDA.ForceAllGhosts = !ps.CRDA.ForceAllGhosts
				status.clear = true
				return
			} else if n := len(cmd); n >= 5 {
				// All commands are at least 5 characters, so check that up front
				validAirport := func(ap string) bool {
					for _, pair := range sp.ConvergingRunways {
						if pair.Airport == ap {
							return true
						}
					}
					return false
				}

				getRunway := func(s string) (string, string) {
					i := 0
					for i < len(s) {
						ch := s[i]
						if ch >= '0' && ch <= '9' {
							i++
						} else if ch == 'L' || ch == 'R' || ch == 'C' {
							i++
							break
						} else {
							break
						}
					}
					return s[:i], s[i:]
				}

				getState := func(ap, rwy string) (*CRDARunwayPairState, *CRDARunwayState) {
					for i, pair := range sp.ConvergingRunways {
						if pair.Airport != ap {
							continue
						}

						pairState := &ps.CRDA.RunwayPairState[i]
						if !pairState.Enabled {
							continue
						}

						for j, pairRunway := range pair.Runways {
							if rwy == pairRunway {
								return pairState, &pairState.RunwayState[j]
							}
						}
					}
					return nil, nil
				}

				if cmd[0] == 'L' && validAirport(cmd[1:4]) {
					// Set leader line direction: NL<airport><runway><1-9>
					rwy, num := getRunway(cmd[4:])
					_, runwayState := getState(cmd[1:4], rwy)
					if len(num) == 1 {
						if dir, ok := numpadToDirection(num[0]); ok {
							runwayState.LeaderLineDirection = dir
							status.clear = true
							return
						}
					}
				} else if ap := cmd[:3]; validAirport(ap) {
					if cmd[n-1] == 'S' || cmd[n-1] == 'T' || cmd[n-1] == 'D' {
						// enable/disable a runway pair
						if index, err := strconv.Atoi(cmd[3 : n-1]); err == nil {
							for i, pair := range sp.ConvergingRunways {
								if pair.Airport == ap && pair.Index == index {
									if cmd[n-1] == 'D' {
										ps.CRDA.RunwayPairState[i].Enabled = false
										status.clear = true
										status.output = ap + " " + pair.getRunwaysString() + " INHIBITED"
										return
									} else {
										// Make sure neither of the runways involved is already enabled with
										// another pair.
										for j, pairState := range ps.CRDA.RunwayPairState {
											if !pairState.Enabled {
												continue
											}
											if sp.ConvergingRunways[j].Runways[0] == pair.Runways[0] ||
												sp.ConvergingRunways[j].Runways[0] == pair.Runways[1] ||
												sp.ConvergingRunways[j].Runways[1] == pair.Runways[0] ||
												sp.ConvergingRunways[j].Runways[1] == pair.Runways[1] {
												status.err = ErrSTARSIllegalParam
												return
											}
										}

										if cmd[n-1] == 'S' {
											ps.CRDA.RunwayPairState[i].Mode = CRDAModeStagger
										} else {
											ps.CRDA.RunwayPairState[i].Mode = CRDAModeTie
										}
										ps.CRDA.RunwayPairState[i].Enabled = true
										status.output = ap + " " + pair.getRunwaysString() + " ENABLED"
										status.clear = true
										return
									}
								}
							}
						}
					} else {
						// there should be a valid runway following the
						// airport
						rwy, extra := getRunway(cmd[3:])

						pairState, runwayState := getState(ap, rwy)
						if pairState != nil && runwayState != nil {
							switch extra {
							case "":
								// toggle ghosts for runway
								runwayState.Enabled = !runwayState.Enabled
								status.output = ap + " " + rwy + " GHOSTING " +
									Select(runwayState.Enabled, "ENABLED", "INHIBITED")
								if !runwayState.Enabled {
									runwayState.DrawQualificationRegion = false
									runwayState.DrawCourseLines = false
								}
								status.clear = true
								return

							case "E":
								// enable ghosts for runway
								runwayState.Enabled = true
								status.output = ap + " " + rwy + " GHOSTING ENABLED"
								status.clear = true
								return

							case "I":
								// disable ghosts for runway
								runwayState.Enabled = false
								status.output = ap + " " + rwy + " GHOSTING INHIBITED"
								// this also disables the runway's visualizations
								runwayState.DrawQualificationRegion = false
								runwayState.DrawCourseLines = false
								status.clear = true
								return

							case " B":
								runwayState.DrawQualificationRegion = !runwayState.DrawQualificationRegion
								status.clear = true
								return

							case " L":
								runwayState.DrawCourseLines = !runwayState.DrawCourseLines
								status.clear = true
								return
							}
						}
					}
				}

				status.err = ErrSTARSIllegalParam
				return
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

		case "Q": // quicklook
			if len(cmd) == 0 {
				// inhibit for all
				ps.QuickLookAll = false
				ps.QuickLookAllIsPlus = false
				ps.QuickLookPositions = nil
				status.clear = true
				return
			} else if cmd == "ALL" {
				if ps.QuickLookAll && ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = false
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else if cmd == "ALL+" {
				if ps.QuickLookAll && !ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = true
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else {
				positions, input, err := parseQuickLookPositions(ctx.world, cmd)
				if len(positions) > 0 {
					ps.QuickLookAll = false

					for _, pos := range positions {
						// Toggle
						match := func(q QuickLookPosition) bool { return q.Id == pos.Id && q.Plus == pos.Plus }
						matchId := func(q QuickLookPosition) bool { return q.Id == pos.Id }
						if idx := FindIf(ps.QuickLookPositions, match); idx != -1 {
							nomatch := func(q QuickLookPosition) bool { return !match(q) }
							ps.QuickLookPositions = FilterSlice(ps.QuickLookPositions, nomatch)
						} else if idx := FindIf(ps.QuickLookPositions, matchId); idx != -1 {
							// Toggle plus
							ps.QuickLookPositions[idx].Plus = !ps.QuickLookPositions[idx].Plus
						} else {
							ps.QuickLookPositions = append(ps.QuickLookPositions, pos)
						}
					}
					sort.Slice(ps.QuickLookPositions,
						func(i, j int) bool { return ps.QuickLookPositions[i].Id < ps.QuickLookPositions[j].Id })
				}

				if err == nil {
					status.clear = true
				} else {
					status.err = err
					sp.previewAreaInput = input
				}
				return
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
				if state, ok := sp.Aircraft[callsign]; ok {
					state.pilotAltitude = 0
					sp.setScratchpad(ctx, callsign, "")
					status.clear = true
				}
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt

				// Either pilot alt or scratchpad entry
				if ac := lookupAircraft(f[0]); ac == nil {
					status.err = ErrSTARSNoFlight
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.Aircraft[ac.Callsign].pilotAltitude = alt * 100
				} else {
					sp.setScratchpad(ctx, ac.Callsign, f[1])
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
			if ac := lookupAircraft(cmd[2:]); ac != nil {
				state := sp.Aircraft[ac.Callsign]
				state.DisableCAWarnings = !state.DisableCAWarnings
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
			sp.MinSepAircraft[0] = ""
			sp.MinSepAircraft[1] = ""
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
		if cmd == "A" {
			// remove all maps
			ps.VideoMapVisible = make(map[string]interface{})
			ps.SystemMapVisible = make(map[int]interface{})
			status.clear = true
			return
		} else if n := len(cmd); n >= 2 {
			op := "T"            // toggle by default
			if cmd[n-1] == 'E' { // enable
				op = "E"
				cmd = cmd[:n-1]
			} else if cmd[n-1] == 'I' { // inhibit
				op = "T"
				cmd = cmd[:n-1]
			}

			if idx, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if idx <= 0 {
				status.err = ErrSTARSIllegalMap
			} else if idx > len(ctx.world.STARSMaps) {
				// is it a system map?
				if _, ok := sp.SystemMaps[idx]; ok {
					if _, ok := ps.SystemMapVisible[idx]; (ok && op == "T") || op == "I" {
						delete(ps.SystemMapVisible, idx)
					} else if (!ok && op == "T") || op == "E" {
						ps.SystemMapVisible[idx] = nil
					}
					status.clear = true
					return
				}
				status.err = ErrSTARSIllegalMap
			} else {
				idx--
				name := ctx.world.STARSMaps[idx].Name
				if _, ok := ps.VideoMapVisible[name]; (ok && op == "T") || op == "I" {
					delete(ps.VideoMapVisible, name)
				} else if (!ok && op == "T") || op == "E" {
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
			if state, ok := sp.Aircraft[callsign]; ok {
				state.DatablockType = FullDatablock
			}
			if ac, ok := ctx.world.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = sp.flightPlanSTARS(ctx.world, ac)
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
			if state, ok := sp.Aircraft[callsign]; ok {
				state.DatablockType = FullDatablock
			}
			if ac, ok := ctx.world.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = sp.flightPlanSTARS(ctx.world, ac)
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

func (sp *STARSPane) acknowledgePointOut(ctx *PaneContext, callsign string) {
	ctx.world.AcknowledgePointOut(callsign, nil,
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
	ghosts []*GhostAircraft, transforms ScopeTransformations) (status STARSCommandStatus) {
	// See if an aircraft was clicked
	ac, acDistance := sp.tryGetClosestAircraft(ctx.world, mousePosition, transforms)
	ghost, ghostDistance := sp.tryGetClosestGhost(ghosts, mousePosition, transforms)

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

	// The only thing that can happen with a ghost is to switch between a full/partial
	// datablock. Note that if we found both an aircraft and a ghost and a command was entered,
	// we don't issue an error for a bad ghost command but
	if ghost != nil && ghostDistance < acDistance {
		if sp.commandMode == CommandModeNone && cmd == "" {
			state := sp.Aircraft[ghost.Callsign]
			state.Ghost.PartialDatablock = !state.Ghost.PartialDatablock
			status.clear = true
			return
		} else if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "N" {
			if cmd == "" {
				// Suppress ghost
				state := sp.Aircraft[ghost.Callsign]
				state.Ghost.State = GhostStateSuppressed
				status.clear = true
				return
			} else if cmd == "*" {
				// Display parent aircraft flight plan
				ac := ctx.world.Aircraft[ghost.Callsign]
				status.output, status.err = sp.flightPlanSTARS(ctx.world, ac)
				if status.err == nil {
					status.clear = true
				}
				return
			}
		}
	}

	if ac != nil {
		state := sp.Aircraft[ac.Callsign]

		switch sp.commandMode {
		case CommandModeNone:
			switch len(cmd) {
			case 0:
				if AnySlice(sp.CAAircraft, func(ca CAAircraft) bool {
					return (ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign) &&
						!ca.Acknowledged
				}) {
					// Acknowledged a CA
					for i, ca := range sp.CAAircraft {
						if ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign {
							sp.CAAircraft[i].Acknowledged = true
						}
					}
				} else if ac.HandoffTrackController == ctx.world.Callsign {
					// Accept inbound h/o
					status.clear = true
					sp.acceptHandoff(ctx, ac.Callsign)
					return
				} else if ac.HandoffTrackController != "" && ac.HandoffTrackController != ctx.world.Callsign &&
					ac.TrackingController == ctx.world.Callsign {
					// cancel offered handoff offered
					status.clear = true
					sp.cancelHandoff(ctx, ac.Callsign)
					return
				} else if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
					// ack point out
					sp.acknowledgePointOut(ctx, ac.Callsign)
					status.clear = true
					return
				} else if _, ok := sp.RejectedPointOuts[ac.Callsign]; ok {
					// ack rejected point out
					delete(sp.RejectedPointOuts, ac.Callsign)
					status.clear = true
					return
				} else if state.OutboundHandoffAccepted {
					// ack an accepted handoff, which we will treat as also
					// handing off control.
					state.OutboundHandoffAccepted = false
					state.OutboundHandoffFlashEnd = time.Now()
					sp.handoffControl(ctx, ac.Callsign)
				} else { //if ac.IsAssociated() {
					if state.DatablockType != FullDatablock {
						state.DatablockType = FullDatablock
						// do not collapse datablock if user is tracking the aircraft
					} else if ac.TrackingController != ctx.world.Callsign {
						state.DatablockType = PartialDatablock
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
					from := sp.Aircraft[ac.Callsign].TrackPosition()
					sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
						p := transforms.LatLongFromWindowP(pw)
						hdg := headingp2ll(from, p, ac.NmPerLongitude(), ac.MagneticVariation())
						dist := nmdistance2ll(from, p)

						status.output = fmt.Sprintf("%03d/%.2f", int(hdg+.5), dist)
						status.clear = true
						return
					}
					return
				} else if dir, ok := numpadToDirection(cmd[0]); ok {
					state.LeaderLineDirection = dir
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
					state.JRingRadius = 0
					status.clear = true
					return
				} else if cmd == "*P" {
					// remove cone for aircraft
					state.ConeLength = 0
					status.clear = true
					return
				} else if cmd == "*T" {
					// range bearing line
					sp.wipRBL = STARSRangeBearingLine{}
					sp.wipRBL.P[0].Callsign = ac.Callsign
					sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
					return
				} else if cmd == "HJ" || cmd == "RF" || cmd == "EM" || cmd == "MI" || cmd == "SI" {
					state.SPCOverride = cmd
					status.clear = true
					return
				} else if cmd == "UN" {
					ctx.world.RejectPointOut(ac.Callsign, nil, func(err error) {
						sp.previewAreaOutput = GetSTARSError(err).Error()
					})
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

			if cmd == ".ROUTE" {
				sp.drawRouteAircraft = ac.Callsign
				status.clear = true
				return
			}

			if len(cmd) > 2 && cmd[:2] == "*J" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
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
						state.ConeLength = float32(r)
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.ConeLength = float32(r)
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
					state.DisplayReportedBeacon = !state.DisplayReportedBeacon
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "D":
				if cmd == "" {
					status.output, status.err = sp.flightPlanSTARS(ctx.world, ac)
					if status.err == nil {
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "L":
				if len(cmd) == 1 {
					if dir, ok := numpadToDirection(cmd[0]); ok {
						state.LeaderLineDirection = dir
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
				if cmd == "" || cmd == "*" { // TODO: it's not clear what the difference should be
					if state.Ghost.State == GhostStateForced {
						state.Ghost.State = GhostStateRegular
					} else {
						state.Ghost.State = GhostStateForced
					}
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Q":
				if cmd == "" {
					status.clear = true
					state.InhibitMSAWAlert = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "R":
				if cmd == "" {
					state.DisplayPTL = !state.DisplayPTL
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "V":
				if cmd == "" {
					state.DisableMSAW = !state.DisableMSAW
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
				state := sp.Aircraft[ac.Callsign]
				state.DisableCAWarnings = !state.DisableCAWarnings
				status.clear = true
				// TODO: check should we set sp.commandMode = CommandMode
				// (applies here and also to others similar...)
				return
			}

		case CommandModeMin:
			if cmd == "" {
				sp.MinSepAircraft[0] = ac.Callsign
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
					if ac, _ := sp.tryGetClosestAircraft(ctx.world, pw, transforms); ac != nil {
						sp.MinSepAircraft[1] = ac.Callsign
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
			sp.wipRBL.P[0].Loc = transforms.LatLongFromWindowP(mousePosition)
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
			ps.SSAList.Visible = true
			status.clear = true
			return
		} else if cmd == "T" {
			ps.TABList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.TABList.Visible = true
			status.clear = true
			return
		} else if cmd == "TV" {
			ps.VFRList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.VFRList.Visible = true
			status.clear = true
			return
		} else if cmd == "TM" {
			ps.AlertList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.AlertList.Visible = true
			status.clear = true
			return
		} else if cmd == "TC" {
			ps.CoastList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.CoastList.Visible = true
			status.clear = true
			return
		} else if cmd == "TS" {
			ps.SignOnList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.SignOnList.Visible = true
			status.clear = true
			return
		} else if cmd == "TX" {
			ps.VideoMapsList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.VideoMapsList.Visible = true
			status.clear = true
			return
		} else if cmd == "TN" {
			ps.CRDAStatusList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.CRDAStatusList.Visible = true
			status.clear = true
			return
		} else if len(cmd) == 2 && cmd[0] == 'P' {
			if idx, err := strconv.Atoi(cmd[1:]); err == nil && idx > 0 && idx <= 3 {
				ps.TowerLists[idx-1].Position = transforms.NormalizedFromWindowP(mousePosition)
				ps.TowerLists[idx-1].Visible = true
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
		if ac, _ := sp.tryGetClosestAircraft(ctx.world, pw, transforms); ac != nil {
			rbl.P[1].Callsign = ac.Callsign
		} else {
			rbl.P[1].Loc = transforms.LatLongFromWindowP(pw)
		}
		sp.RangeBearingLines = append(sp.RangeBearingLines, rbl)
		status.clear = true
		return
	}
}

func (sp *STARSPane) DrawDCB(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) Extent2D {
	ps := &sp.CurrentPreferenceSet

	// Find a scale factor so that the buttons all fit in the window, if necessary
	const NumDCBSlots = 20
	// Sigh; on windows we want the button size in pixels on high DPI displays
	ds := Select(runtime.GOOS == "windows", ctx.platform.DPIScale(), float32(1))
	var buttonScale float32
	// Scale based on width or height available depending on DCB position
	if ps.DCBPosition == DCBPositionTop || ps.DCBPosition == DCBPositionBottom {
		buttonScale = min(ds, (ds*ctx.paneExtent.Width()-4)/(NumDCBSlots*STARSButtonSize))
	} else {
		buttonScale = min(ds, (ds*ctx.paneExtent.Height()-4)/(NumDCBSlots*STARSButtonSize))
	}

	sp.StartDrawDCB(ctx, buttonScale, transforms, cb)

	switch sp.activeDCBMenu {
	case DCBMenuMain:
		STARSCallbackSpinner(ctx, "RANGE\n", &ps.Range,
			func(v float32) string { return strconv.Itoa(int(v)) },
			func(v, delta float32) float32 {
				if delta > 0 {
					v++
				} else if delta < 0 {
					v--
				}
				return clamp(v, 6, 256)
			}, STARSButtonFull, buttonScale)
		sp.STARSPlaceButton("PLACE\nCNTR", STARSButtonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			})
		ps.OffCenter = ps.CurrentCenter != ps.Center
		if STARSToggleButton("OFF\nCNTR", &ps.OffCenter, STARSButtonHalfVertical, buttonScale) {
			ps.CurrentCenter = ps.Center
		}
		STARSCallbackSpinner(ctx, "RR\n", &ps.RangeRingRadius,
			func(v int) string { return strconv.Itoa(v) },
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
		sp.STARSPlaceButton("PLACE\nRR", STARSButtonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				status.clear = true
				return
			})
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
			STARSDisabledButton("WX"+strconv.Itoa(i), STARSButtonHalfHorizontal, buttonScale)

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
			func(v int) string { return strconv.Itoa(v) },
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
		top := ps.DCBPosition == DCBPositionTop
		if STARSToggleButton("DCB\nTOP", &top, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionTop
		}
		left := ps.DCBPosition == DCBPositionLeft
		if STARSToggleButton("DCB\nLEFT", &left, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionLeft
		}
		right := ps.DCBPosition == DCBPositionRight
		if STARSToggleButton("DCB\nRIGHT", &right, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionRight
		}
		bottom := ps.DCBPosition == DCBPositionBottom
		if STARSToggleButton("DCB\nBOTTOM", &bottom, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionBottom
		}
		STARSFloatSpinner(ctx, "PTL\nLNTH\n", &ps.PTLLength, 0.1, 20, STARSButtonFull, buttonScale)
		STARSToggleButton("PTL OWN", &ps.PTLOwn, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton("PTL ALL", &ps.PTLAll, STARSButtonHalfVertical, buttonScale)
		STARSCallbackSpinner(ctx, "DWELL\n", &ps.DwellMode,
			func(mode DwellMode) string { return mode.String() },
			func(mode DwellMode, delta float32) DwellMode {
				if delta > 0 {
					return [3]DwellMode{DwellModeLock, DwellModeLock, DwellModeOn}[mode]
				} else if delta < 0 {
					return [3]DwellMode{DwellModeOff, DwellModeOn, DwellModeOff}[mode]
				}
				return mode
			},
			STARSButtonFull, buttonScale)
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
			ps.SystemMapVisible = make(map[int]interface{})
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

		geoMapsSelected := ps.VideoMapsList.Selection == VideoMapsGroupGeo
		if STARSToggleButton("GEO\nMAPS", &geoMapsSelected, STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupGeo
			ps.VideoMapsList.Visible = true
		}
		STARSDisabledButton("AIRPORT", STARSButtonHalfVertical, buttonScale)
		sysProcSelected := ps.VideoMapsList.Selection == VideoMapsGroupSysProc
		if STARSToggleButton("SYS\nPROC", &sysProcSelected, STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupSysProc
			ps.VideoMapsList.Visible = true
		}
		STARSToggleButton("CURRENT", &ps.VideoMapsList.Visible, STARSButtonHalfVertical, buttonScale)

	case DCBMenuBrite:
		STARSBrightnessSpinner(ctx, "DCB ", &ps.Brightness.DCB, 25, false, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "BKC ", &ps.Brightness.BackgroundContrast, 0, false, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "MPA ", &ps.Brightness.VideoGroupA, 5, false, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "MPB ", &ps.Brightness.VideoGroupB, 5, false, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "FDB ", &ps.Brightness.FullDatablocks, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "LST ", &ps.Brightness.Lists, 25, false, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "POS ", &ps.Brightness.Positions, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "LDB ", &ps.Brightness.LimitedDatablocks, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "OTH ", &ps.Brightness.OtherTracks, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "TLS ", &ps.Brightness.Lines, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "RR ", &ps.Brightness.RangeRings, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "CMP ", &ps.Brightness.Compass, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "BCN ", &ps.Brightness.BeaconSymbols, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "PRI ", &ps.Brightness.PrimarySymbols, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "HST ", &ps.Brightness.History, 5, true, STARSButtonHalfVertical, buttonScale)
		// The STARS manual, p.4-74 actually says that weather can't go to OFF... FIXME?
		STARSBrightnessSpinner(ctx, "WX ", &ps.Brightness.Weather, 5, true, STARSButtonHalfVertical, buttonScale)
		STARSBrightnessSpinner(ctx, "WXC ", &ps.Brightness.WxContrast, 5, false, STARSButtonHalfVertical, buttonScale)
		if ps.Brightness.Weather != 0 {
			sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center)
		} else {
			// Don't fetch weather maps if they're not going to be displayed.
			sp.weatherRadar.Deactivate()
		}
		if STARSSelectButton("DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuCharSize:
		STARSIntSpinner(ctx, "DATA\nBLOCKS\n", &ps.CharSize.Datablocks, 0, 5, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "LISTS\n", &ps.CharSize.Lists, 0, 5, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "DCB\n", &ps.CharSize.DCB, 0, 2, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "TOOLS\n", &ps.CharSize.Tools, 0, 5, STARSButtonFull, buttonScale)
		STARSIntSpinner(ctx, "POS\n", &ps.CharSize.PositionSymbols, 0, 5, STARSButtonFull, buttonScale)
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
			sp.CurrentPreferenceSet = sp.MakePreferenceSet("", ctx.world)
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
		// Fill extras with empty disabled buttons
		for i := len(ctx.world.RadarSites); i < 15; i++ {
			STARSDisabledButton("", STARSButtonFull, buttonScale)
		}
		multi := sp.radarMode(ctx.world) == RadarModeMulti
		if STARSToggleButton("MULTI", &multi, STARSButtonFull, buttonScale) && multi {
			ps.RadarSiteSelected = ""
			if ps.FusedRadarMode {
				sp.discardTracks = true
			}
			ps.FusedRadarMode = false
		}
		fused := sp.radarMode(ctx.world) == RadarModeFused
		if STARSToggleButton("FUSED", &fused, STARSButtonFull, buttonScale) && fused {
			ps.RadarSiteSelected = ""
			ps.FusedRadarMode = true
			sp.discardTracks = true
		}
		if STARSSelectButton("DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSSAFilter:
		STARSToggleButton("ALL", &ps.SSAList.Filter.All, STARSButtonHalfVertical, buttonScale)
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

	sz := starsButtonSize(STARSButtonFull, buttonScale)
	paneExtent := ctx.paneExtent
	switch ps.DCBPosition {
	case DCBPositionTop:
		paneExtent.p1[1] -= sz[1]

	case DCBPositionLeft:
		paneExtent.p0[0] += sz[0]

	case DCBPositionRight:
		paneExtent.p1[0] -= sz[0]

	case DCBPositionBottom:
		paneExtent.p0[1] += sz[1]
	}

	return paneExtent
}

func (sp *STARSPane) drawSystemLists(aircraft []*Aircraft, ctx *PaneContext, paneExtent Extent2D,
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
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
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
				state := sp.Aircraft[ac.Callsign]
				if ac.Squawk == Squawk(0o7500) || state.SPCOverride == "HJ" {
					hj = true
				} else if ac.Squawk == Squawk(0o7600) || state.SPCOverride == "RF" {
					rf = true
				} else if ac.Squawk == Squawk(0o7700) || state.SPCOverride == "EM" {
					em = true
				} else if ac.Squawk == Squawk(0o7777) || state.SPCOverride == "MI" {
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

		if (filter.All || filter.QuickLookPositions) && (ps.QuickLookAll || len(ps.QuickLookPositions) > 0) {
			if ps.QuickLookAll {
				if ps.QuickLookAllIsPlus {
					pw = td.AddText("QL: ALL+", pw, style)
				} else {
					pw = td.AddText("QL: ALL", pw, style)
				}
			} else {
				pos := MapSlice(ps.QuickLookPositions,
					func(q QuickLookPosition) string {
						return q.Id + Select(q.Plus, "+", "")
					})
				pw = td.AddText("QL: "+strings.Join(pos, " "), pw, style)
			}
			newline()
		}

		if filter.All || filter.DisabledTerminal {
			var disabled []string
			if ps.DisableCAWarnings {
				disabled = append(disabled, "CA")
			}
			if ps.CRDA.Disabled {
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

		if (filter.All || filter.ActiveCRDAPairs) && !ps.CRDA.Disabled {
			for i, crda := range ps.CRDA.RunwayPairState {
				if !crda.Enabled {
					continue
				}

				text := "*"
				text += Select(crda.Mode == CRDAModeStagger, "S ", "T ")
				text += sp.ConvergingRunways[i].Airport + " "
				text += sp.ConvergingRunways[i].getRunwaysString()

				pw = td.AddText(text, pw, style)
				newline()
			}
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
		text := "LA/CA/MCI\n"
		if len(sp.CAAircraft) > ps.AlertList.Lines {
			text += fmt.Sprintf("MORE: %d/%d\n", ps.AlertList.Lines, len(sp.CAAircraft))
		}
		for i, pair := range sp.CAAircraft {
			text += pair.Callsigns[0] + "*" + pair.Callsigns[1] + " CA\n"
			if i+1 == ps.AlertList.Lines {
				// No need to add more...
				break
			}
		}
		drawList(text, ps.AlertList.Position)
	}

	if ps.CoastList.Visible {
		text := "COAST/SUSPEND"
		// TODO
		drawList(text, ps.CoastList.Position)
	}

	if ps.VideoMapsList.Visible {
		text := ""
		format := func(m STARSMap, i int, vis bool) string {
			text := Select(vis, ">", " ") + " "
			text += fmt.Sprintf("%3d ", i)
			text += fmt.Sprintf("%8s ", strings.ToUpper(m.Label))
			text += strings.ToUpper(m.Name) + "\n"
			return text
		}
		if ps.VideoMapsList.Selection == VideoMapsGroupGeo {
			text += "GEOGRAPHIC MAPS\n"
			for i, m := range ctx.world.STARSMaps {
				_, vis := ps.VideoMapVisible[m.Name]
				text += format(m, i+1, vis) // 1-based indexing
			}
		} else if ps.VideoMapsList.Selection == VideoMapsGroupSysProc {
			text += "PROCESSING AREAS\n"
			for _, index := range SortedMapKeys(sp.SystemMaps) {
				_, vis := ps.SystemMapVisible[index]
				text += format(*sp.SystemMaps[index], index, vis)
			}
		} else {
			lg.Errorf("%d: unhandled VideoMapsList.Selection", ps.VideoMapsList.Selection)
		}

		drawList(text, ps.VideoMapsList.Position)
	}

	if ps.CRDAStatusList.Visible {
		text := "CRDA STATUS\n"
		pairIndex := 0 // reset for each new airport
		currentAirport := ""
		for i, crda := range ps.CRDA.RunwayPairState {
			line := ""
			if !crda.Enabled {
				line += " "
			} else {
				line += Select(crda.Mode == CRDAModeStagger, "S", "T")
			}

			pair := sp.ConvergingRunways[i]
			ap := pair.Airport
			if ap != currentAirport {
				currentAirport = ap
				pairIndex = 1
			}

			line += fmt.Sprintf("%d ", pairIndex)
			pairIndex++
			line += ap + " "
			line += pair.getRunwaysString()
			if crda.Enabled {
				for len(line) < 16 {
					line += " "
				}
				ctrl := ctx.world.Controllers[ctx.world.Callsign]
				line += ctrl.SectorId
			}
			text += line + "\n"
		}
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
						dist := nmdistance2ll(ap.Location, sp.Aircraft[ac.Callsign].TrackPosition())
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

func (sp *STARSPane) drawCRDARegions(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	transforms.LoadLatLongViewingMatrices(cb)

	ps := sp.CurrentPreferenceSet
	for i, state := range ps.CRDA.RunwayPairState {
		if !state.Enabled {
			continue
		}
		for j, rwyState := range state.RunwayState {
			if !rwyState.Enabled {
				continue
			}

			if rwyState.DrawCourseLines {
				region := sp.ConvergingRunways[i].ApproachRegions[j]
				line, _ := region.GetLateralGeometry(ctx.world.NmPerLongitude, ctx.world.MagneticVariation)

				ld := GetLinesDrawBuilder()
				cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor))
				ld.AddLine(line[0], line[1])

				ld.GenerateCommands(cb)
				ReturnLinesDrawBuilder(ld)
			}

			if rwyState.DrawQualificationRegion {
				region := sp.ConvergingRunways[i].ApproachRegions[j]
				_, quad := region.GetLateralGeometry(ctx.world.NmPerLongitude, ctx.world.MagneticVariation)

				ld := GetLinesDrawBuilder()
				cb.SetRGB(ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor))
				ld.AddPolyline([2]float32{0, 0}, [][2]float32{quad[0], quad[1], quad[2], quad[3]})

				ld.GenerateCommands(cb)
				ReturnLinesDrawBuilder(ld)
			}
		}
	}
}

func (sp *STARSPane) drawSelectedRoute(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	if sp.drawRouteAircraft == "" {
		return
	}
	ac, ok := ctx.world.Aircraft[sp.drawRouteAircraft]
	if !ok {
		sp.drawRouteAircraft = ""
		return
	}

	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)

	prev := ac.Position()
	for _, wp := range ac.Nav.Waypoints {
		ld.AddLine(prev, wp.Location)
		prev = wp.Location
	}

	ps := sp.CurrentPreferenceSet
	cb.LineWidth(3)
	cb.SetRGB(ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) DatablockType(ctx *PaneContext, ac *Aircraft) DatablockType {
	state := sp.Aircraft[ac.Callsign]
	dt := state.DatablockType

	// TODO: when do we do a partial vs limited datablock?
	if ac.Squawk != ac.AssignedSquawk {
		dt = PartialDatablock
	}

	if ac.TrackingController == ctx.world.Callsign || ac.ControllingController == ctx.world.Callsign {
		// it's under our control
		dt = FullDatablock
	}

	if ac.HandoffTrackController == ctx.world.Callsign {
		// it's being handed off to us
		dt = FullDatablock
	}

	// Point outs are FDB until acked.
	if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
		dt = FullDatablock
	}

	// Quicklook
	ps := sp.CurrentPreferenceSet
	if ps.QuickLookAll {
		dt = FullDatablock
	} else if idx := FindIf(ps.QuickLookPositions,
		func(q QuickLookPosition) bool { return q.Callsign == ac.TrackingController }); idx != -1 {
		dt = FullDatablock
	}

	return dt
}

func (sp *STARSPane) drawTracks(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	pd := PointsDrawBuilder{}
	pd2 := PointsDrawBuilder{}
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	trid := GetColoredTrianglesDrawBuilder()
	defer ReturnColoredTrianglesDrawBuilder(trid)
	// TODO: square icon if it's squawking a beacon code we're monitoring

	ps := sp.CurrentPreferenceSet

	now := ctx.world.CurrentTime()
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		brightness := ps.Brightness.Positions

		dt := sp.DatablockType(ctx, ac)

		if dt == PartialDatablock || dt == LimitedDatablock {
			brightness = ps.Brightness.LimitedDatablocks
		}

		trackId := ""
		if ac.TrackingController != "" {
			trackId = "?"
			if ctrl := ctx.world.GetController(ac.TrackingController); ctrl != nil {
				trackId = ctrl.Scope
			}
		}

		// "cheat" by using ac.Heading() if we don't yet have two radar tracks to compute the
		// heading with; this makes things look better when we first see a track or when
		// restarting a simulation...
		heading := Select(state.HaveHeading(),
			state.TrackHeading(ac.NmPerLongitude())+ac.MagneticVariation(), ac.Heading())

		sp.drawRadarTrack(state, heading, ctx, transforms, brightness, STARSTrackBlockColor,
			trackId, &pd, &pd2, ld, trid, td)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	cb.PointSize(5)
	pd.GenerateCommands(cb)
	cb.PointSize(12) // bigger points for fused mode primary tracks
	pd2.GenerateCommands(cb)
	trid.GenerateCommands(cb)
	cb.LineWidth(1)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) getGhostAircraft(aircraft []*Aircraft, ctx *PaneContext) []*GhostAircraft {
	var ghosts []*GhostAircraft
	ps := sp.CurrentPreferenceSet
	now := ctx.world.CurrentTime()

	for i, pairState := range ps.CRDA.RunwayPairState {
		if !pairState.Enabled {
			continue
		}
		for j, rwyState := range pairState.RunwayState {
			if !rwyState.Enabled {
				continue
			}

			// Leader line direction comes from the scenario configuration, unless it
			// has been overridden for the runway via <multifunc>NL.
			leaderDirection := sp.ConvergingRunways[i].LeaderDirections[j]
			if rwyState.LeaderLineDirection != nil {
				leaderDirection = *rwyState.LeaderLineDirection
			}

			runwayIntersection := sp.ConvergingRunways[i].RunwayIntersection
			region := sp.ConvergingRunways[i].ApproachRegions[j]
			otherRegion := sp.ConvergingRunways[i].ApproachRegions[(j+1)%2]

			trackId := Select(pairState.Mode == CRDAModeStagger, sp.ConvergingRunways[i].StaggerSymbol,
				sp.ConvergingRunways[i].TieSymbol)
			offset := Select(pairState.Mode == CRDAModeTie, sp.ConvergingRunways[i].TieOffset, float32(0))

			for _, ac := range aircraft {
				state := sp.Aircraft[ac.Callsign]
				if state.LostTrack(now) {
					continue
				}

				// Create a ghost track if appropriate, add it to the
				// ghosts slice, and draw its radar track.
				force := state.Ghost.State == GhostStateForced || ps.CRDA.ForceAllGhosts
				heading := Select(state.HaveHeading(), state.TrackHeading(ac.NmPerLongitude()),
					ac.Heading())
				idx := (state.tracksIndex - 1) % len(state.tracks)
				ghost := region.TryMakeGhost(ac.Callsign, state.tracks[idx], heading, ac.Scratchpad, force,
					offset, leaderDirection, runwayIntersection, ac.NmPerLongitude(), ac.MagneticVariation(),
					otherRegion)
				if ghost != nil {
					ghost.TrackId = trackId
					ghosts = append(ghosts, ghost)
				}
			}
		}
	}

	return ghosts
}

func (sp *STARSPane) drawGhosts(ghosts []*GhostAircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor)
	trackFont := sp.systemFont[ps.CharSize.PositionSymbols]
	trackStyle := TextStyle{Font: trackFont, Color: color, DropShadow: true, LineSpacing: 0}
	datablockFont := sp.systemFont[ps.CharSize.Datablocks]
	datablockStyle := TextStyle{Font: datablockFont, Color: color, DropShadow: true, LineSpacing: 0}

	for _, ghost := range ghosts {
		state := sp.Aircraft[ghost.Callsign]

		if state.Ghost.State == GhostStateSuppressed {
			continue
		}

		// The track is just the single character..
		pw := transforms.WindowFromLatLongP(ghost.Position)
		td.AddTextCentered(ghost.TrackId, pw, trackStyle)

		var datablockText string
		if state.Ghost.PartialDatablock {
			// Partial datablock is just airspeed and then aircraft type if it's ~heavy.
			datablockText = fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)

			fp := ctx.world.Aircraft[ghost.Callsign].FlightPlan
			fields := strings.Split(fp.AircraftType, "/")
			if len(fields) > 1 && (fields[0] == "H" || fields[0] == "J" || fields[0] == "S") {
				datablockText += fields[0]
			}
		} else {
			// The full datablock ain't much more...
			datablockText = ghost.Callsign + "\n" + fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)
		}
		w, h := datablockFont.BoundText(datablockText, datablockStyle.LineSpacing)
		datablockOffset := sp.getDatablockOffset([2]float32{float32(w), float32(h)},
			ghost.LeaderLineDirection)

		// Draw datablock
		pac := transforms.WindowFromLatLongP(ghost.Position)
		pt := add2f(datablockOffset, pac)
		td.AddText(datablockText, pt, datablockStyle)

		// Leader line
		v := sp.getLeaderLineVector(ghost.LeaderLineDirection)
		p0 := add2f(pac, scale2f(normalize2f(v), float32(2+trackFont.size/2)))
		p1 := add2f(pac, v)
		ld.AddLine(p0, p1, color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRadarTrack(state *STARSAircraftState, heading float32, ctx *PaneContext,
	transforms ScopeTransformations, brightness STARSBrightness, trackColor RGB, trackId string,
	pd *PointsDrawBuilder, pd2 *PointsDrawBuilder, ld *ColoredLinesDrawBuilder,
	trid *ColoredTrianglesDrawBuilder, td *TextDrawBuilder) {
	ps := sp.CurrentPreferenceSet
	// TODO: orient based on radar center if just one radar

	pos := state.TrackPosition()
	pw := transforms.WindowFromLatLongP(pos)

	// On high DPI windows displays we need to scale up the tracks
	scale := Select(runtime.GOOS == "windows", ctx.platform.DPIScale(), float32(1))

	switch mode := sp.radarMode(ctx.world); mode {
	case RadarModeSingle:
		site := ctx.world.RadarSites[ps.RadarSiteSelected]
		primary, secondary, dist := site.CheckVisibility(ctx.world, pos, state.TrackAltitude())

		// Orient the box toward the radar
		h := headingp2ll(site.Position, pos, ctx.world.NmPerLongitude, ctx.world.MagneticVariation)
		rot := rotator2f(h)

		// blue box: x +/-9 pixels, y +/-3 pixels
		box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}

		// Scale box based on distance from the radar; TODO: what exactly should this be?
		scale *= float32(clamp(dist/40, .5, 1.5))
		for i := range box {
			box[i] = scale2f(box[i], scale)
			box[i] = add2f(rot(box[i]), pw)
			box[i] = transforms.LatLongFromWindowP(box[i])
		}

		color := brightness.ScaleRGB(STARSTrackBlockColor)
		if primary {
			// Draw a filled box
			trid.AddQuad(box[0], box[1], box[2], box[3], color)
		} else if secondary {
			// If it's just a secondary return, only draw the box outline.
			// TODO: is this 40nm, or secondary?
			ld.AddPolyline([2]float32{}, color, box[:])
		}

		// green line
		line := [2][2]float32{[2]float32{-16, -3}, [2]float32{16, -3}}
		for i := range line {
			line[i] = add2f(rot(scale2f(line[i], scale)), pw)
			line[i] = transforms.LatLongFromWindowP(line[i])
		}
		ld.AddLine(line[0], line[1], brightness.ScaleRGB(RGB{R: .1, G: .8, B: .1}))

	case RadarModeMulti:
		primary, secondary, _ := sp.radarVisibility(ctx.world, pos, state.TrackAltitude())
		rot := rotator2f(heading)

		// blue box: x +/-9 pixels, y +/-3 pixels
		box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}
		for i := range box {
			box[i] = scale2f(box[i], scale)
			box[i] = add2f(rot(box[i]), pw)
			box[i] = transforms.LatLongFromWindowP(box[i])
		}

		color := brightness.ScaleRGB(STARSTrackBlockColor)
		if primary {
			// Draw a filled box
			trid.AddQuad(box[0], box[1], box[2], box[3], color)
		} else if secondary {
			// If it's just a secondary return, only draw the box outline.
			// TODO: is this 40nm, or secondary?
			ld.AddPolyline([2]float32{}, color, box[:])
		}

	case RadarModeFused:
		color := brightness.ScaleRGB(STARSTrackBlockColor)
		pd2.AddPoint(pos, color)
	}

	// Draw main track symbol letter
	if trackId != "" {
		font := sp.systemFont[ps.CharSize.PositionSymbols]
		td.AddTextCentered(trackId, pw, TextStyle{Font: font, Color: brightness.RGB(), DropShadow: true})
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

	// Draw history in reverse order so that if it's not moving, more
	// recent tracks (which will have more contrast with the background),
	// will be the ones that are visible.
	n := ps.RadarTrackHistory
	for i := n; i >= 1; i-- {
		trackColorNum := min(i, len(STARSTrackHistoryColors)-1)
		trackColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColors[trackColorNum])

		idx := (state.tracksIndex - 1 -
			Select(sp.radarMode(ctx.world) == RadarModeFused, 5, 1)*i) % len(state.tracks)
		if idx < 0 {
			continue
		}

		p := state.tracks[idx].Position
		if !p.IsZero() {
			pd.AddPoint(p, trackColor)
		}
	}
}

func (sp *STARSPane) getDatablockText(ctx *PaneContext, ac *Aircraft) (errText string, text [2][]string) {
	now := ctx.world.CurrentTime()
	state := sp.Aircraft[ac.Callsign]
	if state.LostTrack(now) || !sp.datablockVisible(ac) {
		return
	}

	errText, text = sp.formatDatablock(ctx, ac)

	// For westerly directions the datablock text should be right
	// justified, since the leader line will be connecting on that side.
	dir := sp.getLeaderLineDirection(ac, ctx.world)
	rightJustify := dir > South
	if rightJustify {
		maxLen := 0
		for i := 0; i < 2; i++ {
			for j := range text[i] {
				text[i][j] = strings.TrimSpace(text[i][j])
				maxLen = max(maxLen, len(text[i][j]))
			}
		}

		justify := func(s string) string {
			if len(s) == maxLen {
				return s
			}
			return fmt.Sprintf("%*c", maxLen-len(s), ' ') + s
		}
		for i := 0; i < 2; i++ {
			text[i][0] = justify(text[i][0])
		}
	}

	return
}

func (sp *STARSPane) getDatablockOffset(textBounds [2]float32, leaderDir CardinalOrdinalDirection) [2]float32 {
	// To place the datablock, start with the vector for the leader line.
	drawOffset := sp.getLeaderLineVector(leaderDir)

	// And now fine-tune so that e.g., for East, the datablock is
	// vertically aligned with the track line. (And similarly for other
	// directions...)
	switch leaderDir {
	case North:
		drawOffset = add2f(drawOffset, [2]float32{0, textBounds[1]})
	case NorthEast, East, SouthEast:
		drawOffset = add2f(drawOffset, [2]float32{0, textBounds[1] / 2})
	case South:
		drawOffset = add2f(drawOffset, [2]float32{0, 0})
	case SouthWest, West, NorthWest:
		drawOffset = add2f(drawOffset, [2]float32{-textBounds[0], textBounds[1] / 2})
	}

	return drawOffset
}

func (sp *STARSPane) OutsideAirspace(ctx *PaneContext, ac *Aircraft) (alts [][2]int, outside bool) {
	// Only report on ones that are tracked by us
	if ac.TrackingController != ctx.world.Callsign {
		return
	}

	state := sp.Aircraft[ac.Callsign]
	if ac.IsDeparture() {
		if len(ctx.world.DepartureAirspace) > 0 {
			inDepartureAirspace, depAlts := InAirspace(ac.Position(), ac.Altitude(), ctx.world.DepartureAirspace)
			if !state.HaveEnteredAirspace {
				state.HaveEnteredAirspace = inDepartureAirspace
			} else {
				alts = depAlts
				outside = !inDepartureAirspace
			}
		}
	} else {
		if len(ctx.world.ApproachAirspace) > 0 {
			inApproachAirspace, depAlts := InAirspace(ac.Position(), ac.Altitude(), ctx.world.ApproachAirspace)
			if !state.HaveEnteredAirspace {
				state.HaveEnteredAirspace = inApproachAirspace
			} else {
				alts = depAlts
				outside = !inApproachAirspace
			}
		}
	}
	return
}

func (sp *STARSPane) updateCAAircraft(w *World) {
	aircraft := sp.visibleAircraft(w)
	sort.Slice(aircraft, func(i, j int) bool {
		return aircraft[i].Callsign < aircraft[j].Callsign
	})

	inCAVolumes := func(state *STARSAircraftState) bool {
		for _, vol := range w.InhibitCAVolumes {
			if vol.Inside(state.TrackPosition(), state.TrackAltitude()) {
				return true
			}
		}
		return false
	}

	conflicting := func(callsigna, callsignb string) bool {
		sa, sb := sp.Aircraft[callsigna], sp.Aircraft[callsignb]
		if inCAVolumes(sa) || inCAVolumes(sb) {
			return false
		}
		return nmdistance2ll(sa.TrackPosition(), sb.TrackPosition()) <= LateralMinimum &&
			/*small slop for fp error*/
			abs(sa.TrackAltitude()-sb.TrackAltitude()) <= VerticalMinimum-5
	}

	// Remove ones that are no longer conflicting
	sp.CAAircraft = FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return conflicting(ca.Callsigns[0], ca.Callsigns[1])
	})

	// Remove ones that are no longer visible
	sp.CAAircraft = FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return AnySlice(aircraft, func(ac *Aircraft) bool { return ac.Callsign == ca.Callsigns[0] }) &&
			AnySlice(aircraft, func(ac *Aircraft) bool { return ac.Callsign == ca.Callsigns[1] })
	})

	// Add new conflicts; by appending we keep them sorted by when they
	// were first detected...
	callsigns := MapSlice(aircraft, func(ac *Aircraft) string { return ac.Callsign })
	for i, callsign := range callsigns {
		for _, ocs := range callsigns[i+1:] {
			if conflicting(callsign, ocs) {
				if !AnySlice(sp.CAAircraft, func(ca CAAircraft) bool {
					return callsign == ca.Callsigns[0] && ocs == ca.Callsigns[1]
				}) {
					sp.CAAircraft = append(sp.CAAircraft, CAAircraft{
						Callsigns: [2]string{callsign, ocs},
					})
				}
			}
		}
	}

	// Play the sound if any are unacknowledged and it has been 2s since the last sound
	if now := w.CurrentTime(); now.Sub(sp.LastCASoundTime) > 2*time.Second {
		if AnySlice(sp.CAAircraft, func(ca CAAircraft) bool { return !ca.Acknowledged }) {
			globalConfig.Audio.PlaySound(AudioEventConflictAlert)
			sp.LastCASoundTime = now
		}
	}
}

func (sp *STARSPane) formatDatablock(ctx *PaneContext, ac *Aircraft) (errblock string, mainblock [2][]string) {
	state := sp.Aircraft[ac.Callsign]

	var errs []string
	if ac.Squawk == Squawk(0o7500) || state.SPCOverride == "HJ" {
		errs = append(errs, "HJ")
	} else if ac.Squawk == Squawk(0o7600) || state.SPCOverride == "RF" {
		errs = append(errs, "RF")
	} else if ac.Squawk == Squawk(0o7700) || state.SPCOverride == "EM" {
		errs = append(errs, "EM")
	} else if ac.Squawk == Squawk(0o7777) || state.SPCOverride == "MI" {
		errs = append(errs, "MI")
	}
	if AnySlice(sp.CAAircraft,
		func(ca CAAircraft) bool { return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign }) {
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

	ty := sp.DatablockType(ctx, ac)

	switch ty {
	case LimitedDatablock:
		mainblock[0] = append(mainblock[0], "TODO LIMITED DATABLOCK")
		mainblock[1] = append(mainblock[1], "TODO LIMITED DATABLOCK")

	case PartialDatablock:
		// STARS Operators Manual 2-69
		if ac.Squawk != ac.AssignedSquawk {
			sq := ac.Squawk.String()
			mainblock[0] = append(mainblock[0], sq)
			mainblock[1] = append(mainblock[1], sq+"WHO")
		}
		actype := ac.FlightPlan.TypeWithoutSuffix()
		var suffix string
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
			suffix += "H"
		} else if strings.HasPrefix(actype, "S/") || strings.HasPrefix(actype, "J/") {
			suffix += "J"
		}

		ident := Select(state.Ident(), "ID", "")

		ho := " "
		if ac.HandoffTrackController != "" {
			if ctrl := ctx.world.GetController(ac.HandoffTrackController); ctrl != nil {
				ho = ctrl.SectorId[len(ctrl.SectorId)-1:]
			}
		}

		if fp := ac.FlightPlan; fp != nil && fp.Rules == IFR {
			// Alternate between altitude and either scratchpad or destination airport.
			mainblock[0] = append(mainblock[0], fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)+ho+suffix+ident)
			if ac.Scratchpad != "" {
				mainblock[1] = append(mainblock[1], fmt.Sprintf("%3s", ac.Scratchpad)+ho+suffix+ident)
			} else {
				ap := fp.ArrivalAirport
				if len(ap) == 4 {
					ap = ap[1:] // drop the leading K
				}
				mainblock[1] = append(mainblock[1], fmt.Sprintf("%3s", ap)+ho+suffix+ident)
			}
		} else {
			// VFR
			as := fmt.Sprintf("%03d  %02d", (state.TrackAltitude()+50)/100, (state.TrackGroundspeed()+5)/10)
			mainblock[0] = append(mainblock[0], as+ident)
			mainblock[1] = append(mainblock[1], as+ident)
		}
		return

	case FullDatablock:
		// First line; the same for both.
		cs := ac.Callsign
		// TODO: draw triangle after callsign if conflict alerts inhibited
		// TODO: space then asterisk after callsign if MSAW inhibited

		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
			cs += " PO"
		}
		if id, ok := sp.OutboundPointOuts[ac.Callsign]; ok {
			cs += " PO" + id
		}
		if _, ok := sp.RejectedPointOuts[ac.Callsign]; ok {
			cs += " UN"
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

		actype := ac.FlightPlan.TypeWithoutSuffix()
		var suffix string
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

		if state.Ident() {
			// Speed is followed by ID when identing (2-67, field 5)
			mainblock[0] = append(mainblock[0], alt+ho+speed+"ID")
		} else {
			mainblock[0] = append(mainblock[0], alt+ho+speed+suffix)
		}

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
	state := sp.Aircraft[ac.Callsign]

	if ac.Callsign == sp.dwellAircraft {
		br = STARSBrightness(100)
	}

	// Handle cases where it should flash
	now := time.Now()
	if now.Second()&1 == 0 { // one second cycle
		if state.Ident() || // ident
			ac.HandoffTrackController == w.Callsign || // handing off to us
			// we handed it off, it was accepted, but we haven't yet acknowledged
			(state.OutboundHandoffAccepted && now.Before(state.OutboundHandoffFlashEnd)) {
			br /= 3
		}
	}

	if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
		// yellow for pointed out by someone else
		return br.ScaleRGB(STARSInboundPointOutColor)
	} else if ac.TrackingController == w.Callsign {
		// white if we are tracking, unless it's selected
		if state.IsSelected {
			return br.ScaleRGB(STARSSelectedAircraftColor)
		} else {
			return br.ScaleRGB(STARSTrackedAircraftColor)
		}
	} else if ac.HandoffTrackController == w.Callsign {
		// flashing white if it's being handed off to us.
		return br.ScaleRGB(STARSTrackedAircraftColor)
	} else if state.OutboundHandoffAccepted {
		// we handed it off, it was accepted, but we haven't yet acknowledged
		return br.ScaleRGB(STARSTrackedAircraftColor)
	} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
		// quick look all plus
		return br.ScaleRGB(STARSTrackedAircraftColor)
	} else if idx := FindIf(ps.QuickLookPositions,
		func(q QuickLookPosition) bool { return q.Callsign == ac.TrackingController && q.Plus }); idx != -1 {
		// individual quicklook plus controller
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
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac) {
			continue
		}

		errText, datablockText := sp.getDatablockText(ctx, ac)

		color := sp.datablockColor(ctx.world, ac)
		style := TextStyle{Font: font, Color: color, DropShadow: true, LineSpacing: 0}
		currentDatablockText := datablockText[(realNow.Second()/2)&1] // 2 second cycle

		// Compute the bounds of the datablock; always use the first one so
		// things don't jump around when it switches between them.
		var boundsText []string
		if errText != "" {
			boundsText = append(boundsText, errText)
		}
		boundsText = append(boundsText, datablockText[0]...)
		w, h := font.BoundText(strings.Join(boundsText, "\n"), style.LineSpacing)
		datablockOffset := sp.getDatablockOffset([2]float32{float32(w), float32(h)},
			sp.getLeaderLineDirection(ac, ctx.world))

		// Draw characters starting at the upper left.
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		pt := add2f(datablockOffset, pac)
		if errText != "" {
			errorStyle := TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSTextAlertColor),
				LineSpacing: 0}
			pt = td.AddText(errText+"\n", pt, errorStyle)
		}
		td.AddText(strings.Join(currentDatablockText, "\n"), pt, style)

		// Leader line
		v := sp.getLeaderLineVector(sp.getLeaderLineDirection(ac, ctx.world))
		p0 := add2f(pac, scale2f(normalize2f(v), float32(2+font.size/2)))
		p1 := add2f(pac, v)
		ld.AddLine(p0, p1, color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	cb.LineWidth(1)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawPTLs(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ps := sp.CurrentPreferenceSet

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	now := ctx.world.CurrentTime()
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !state.HaveHeading() {
			continue
		}
		if !(state.DisplayPTL || ps.PTLAll || (ps.PTLOwn && ac.TrackingController == ctx.world.Callsign)) {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(state.TrackGroundspeed()) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := state.TrackHeading(ac.NmPerLongitude())
		h := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		h = scale2f(h, dist)
		end := add2f(ll2nm(state.TrackPosition(), ac.NmPerLongitude()), h)

		ld.AddLine(state.TrackPosition(), nm2ll(end, ac.NmPerLongitude()), color)
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
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) {
			continue
		}

		// Format a radius/length for printing, ditching the ".0" if it's
		// an integer value.
		format := func(v float32) string {
			if v == float32(int(v)) {
				return strconv.Itoa(int(v))
			} else {
				return fmt.Sprintf("%.1f", v)
			}
		}

		if state.JRingRadius > 0 {
			const nsegs = 360
			pc := transforms.WindowFromLatLongP(state.TrackPosition())
			radius := state.JRingRadius / transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
			ld.AddCircle(pc, radius, nsegs, color)

			if ps.DisplayTPASize || state.DisplayTPASize {
				// draw the ring size around 7.5 o'clock
				// vector from center to the circle there
				v := [2]float32{-.707106 * radius, -.707106 * radius} // -sqrt(2)/2
				// move up to make space for the text
				v[1] += float32(font.size) + 3
				pt := add2f(pc, v)
				td.AddText(format(state.JRingRadius), pt, textStyle)
			}
		}

		if state.ConeLength > 0 && state.HaveHeading() {
			// We'll draw in window coordinates. First figure out the
			// coordinates of the vertices of the cone triangle. We'll
			// start with a canonical triangle in nm coordinates, going one
			// unit up the +y axis with a small spread in x.
			v := [4][2]float32{[2]float32{0, 0}, [2]float32{-.04, 1}, [2]float32{.04, 1}}

			// Now we want to get that triangle in window coordinates...
			length := state.ConeLength / transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
			rot := rotator2f(state.TrackHeading(ac.NmPerLongitude()) + ac.MagneticVariation())
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

			if ps.DisplayTPASize || state.DisplayTPASize {
				ptext := add2f(pw, rot(scale2f([2]float32{0, 0.5}, length)))
				td.AddTextCentered(format(state.ConeLength), ptext, textStyle)
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// Draw all of the range-bearing lines that have been specified.
func (sp *STARSPane) drawRBLs(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
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
	wp := sp.wipRBL.P[0]
	if ctx.mouse != nil && (!wp.Loc.IsZero() || wp.Callsign != "") {
		p1 := transforms.LatLongFromWindowP(ctx.mouse.Pos)
		if wp.Callsign != "" {
			if ac := ctx.world.Aircraft[wp.Callsign]; ac != nil && sp.datablockVisible(ac) &&
				Find(aircraft, ac) != -1 {
				if state, ok := sp.Aircraft[wp.Callsign]; ok {
					drawRBL(state.TrackPosition(), p1, len(sp.RangeBearingLines)+1)
				}
			}
		} else {
			drawRBL(wp.Loc, p1, len(sp.RangeBearingLines)+1)
		}
	}

	for i, rbl := range sp.RangeBearingLines {
		if p0, p1 := rbl.GetPoints(ctx, aircraft, sp); !p0.IsZero() && !p1.IsZero() {
			drawRBL(p0, p1, i+1)
		}
	}
	sp.RangeBearingLines = FilterSlice(sp.RangeBearingLines, func(rbl STARSRangeBearingLine) bool {
		p0, p1 := rbl.GetPoints(ctx, aircraft, sp)
		return !p0.IsZero() && !p1.IsZero()
	})

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	cs0, cs1 := sp.MinSepAircraft[0], sp.MinSepAircraft[1]
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

	s0, ok0 := sp.Aircraft[ac0.Callsign]
	s1, ok1 := sp.Aircraft[ac1.Callsign]
	if !ok0 || !ok1 {
		return
	}
	DrawMinimumSeparationLine(s0.TrackPosition(),
		s0.HeadingVector(ac0.NmPerLongitude(), ac0.MagneticVariation()),
		s1.TrackPosition(),
		s1.HeadingVector(ac1.NmPerLongitude(), ac1.MagneticVariation()),
		ac0.NmPerLongitude(), color, RGB{}, sp.systemFont[ps.CharSize.Tools],
		ctx, transforms, cb)
}

func (sp *STARSPane) drawAirspace(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	ps := sp.CurrentPreferenceSet
	rgb := ps.Brightness.Lists.ScaleRGB(STARSListColor)

	drawSectors := func(volumes []ControllerAirspaceVolume) {
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

func (sp *STARSPane) consumeMouseEvents(ctx *PaneContext, ghosts []*GhostAircraft,
	transforms ScopeTransformations, cb *CommandBuffer) {
	if ctx.mouse == nil {
		return
	}

	mouse := ctx.mouse
	ps := &sp.CurrentPreferenceSet
	if activeSpinner == nil {
		// Handle dragging the scope center
		if mouse.Dragging[MouseButtonSecondary] {
			delta := mouse.DragDelta
			if delta[0] != 0 || delta[1] != 0 {
				deltaLL := transforms.LatLongFromWindowV(delta)
				ps.CurrentCenter = sub2f(ps.CurrentCenter, deltaLL)
			}
		}

		// Consume mouse wheel
		if mouse.Wheel[1] != 0 {
			r := ps.Range
			if _, ok := ctx.keyboard.Pressed[KeyControl]; ok {
				ps.Range += 3 * mouse.Wheel[1]
			} else {
				ps.Range += mouse.Wheel[1]
			}
			ps.Range = clamp(ps.Range, 6, 512) // 4-33

			// We want to zoom in centered at the mouse position; this affects
			// the scope center after the zoom, so we'll find the
			// transformation that gives the new center position.
			mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
			scale := ps.Range / r
			centerTransform := Identity3x3().
				Translate(mouseLL[0], mouseLL[1]).
				Scale(scale, scale).
				Translate(-mouseLL[0], -mouseLL[1])

			ps.CurrentCenter = centerTransform.TransformPoint(ps.CurrentCenter)
		}
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
			status = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.mouse.Pos, ghosts, transforms)
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
		if ac, _ := sp.tryGetClosestAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
			if state := sp.Aircraft[ac.Callsign]; state != nil {
				state.IsSelected = !state.IsSelected
			}
		}
	} else if !ctx.world.SimIsPaused {
		switch sp.CurrentPreferenceSet.DwellMode {
		case DwellModeOff:
			sp.dwellAircraft = ""

		case DwellModeOn:
			if ac, _ := sp.tryGetClosestAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			} else {
				sp.dwellAircraft = ""
			}

		case DwellModeLock:
			if ac, _ := sp.tryGetClosestAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			}
			// Otherwise leave sp.dwellAircraft as is
		}
	} else {
		if ac, _ := sp.tryGetClosestAircraft(ctx.world, ctx.mouse.Pos, transforms); ac != nil {
			td := GetTextDrawBuilder()
			defer ReturnTextDrawBuilder(td)

			ps := sp.CurrentPreferenceSet
			font := sp.systemFont[ps.CharSize.Datablocks]
			style := TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: 0}

			// Aircraft track position in window coordinates
			state := sp.Aircraft[ac.Callsign]
			pac := transforms.WindowFromLatLongP(state.TrackPosition())

			// Upper-left corner of where we start drawing the text
			pad := float32(5)
			ptext := add2f([2]float32{2 * pad, 0}, pac)
			info := ac.NavSummary()
			td.AddText(info, ptext, style)

			// Draw an alpha-blended quad behind the text to make it more legible.
			trid := GetTrianglesDrawBuilder()
			defer ReturnTrianglesDrawBuilder(trid)
			bx, by := font.BoundText(info, style.LineSpacing)
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

func (sp *STARSPane) drawMouseCursor(ctx *PaneContext, paneExtent Extent2D, transforms ScopeTransformations,
	cb *CommandBuffer) {
	if ctx.mouse == nil {
		return
	}

	// If the mouse is inside the scope, disable the standard mouse cursor
	// and draw a cross for the cursor; otherwise leave the default arrow
	// for the DCB.
	if ctx.mouse.Pos[0] >= 0 && ctx.mouse.Pos[0] < paneExtent.Width() &&
		ctx.mouse.Pos[1] >= 0 && ctx.mouse.Pos[1] < paneExtent.Height() {
		ctx.mouse.SetCursor(imgui.MouseCursorNone)
		ld := GetLinesDrawBuilder()
		defer ReturnLinesDrawBuilder(ld)

		w := float32(7) * Select(runtime.GOOS == "windows", ctx.platform.DPIScale(), float32(1))
		ld.AddLine(add2f(ctx.mouse.Pos, [2]float32{-w, 0}), add2f(ctx.mouse.Pos, [2]float32{w, 0}))
		ld.AddLine(add2f(ctx.mouse.Pos, [2]float32{0, -w}), add2f(ctx.mouse.Pos, [2]float32{0, w}))

		transforms.LoadWindowViewingMatrices(cb)
		// STARS Operators Manual 4-74: FDB brightness is used for the cursor
		ps := sp.CurrentPreferenceSet
		cb.SetRGB(ps.Brightness.FullDatablocks.RGB())
		ld.GenerateCommands(cb)
	} else {
		ctx.mouse.SetCursor(imgui.MouseCursorArrow)
	}
}

///////////////////////////////////////////////////////////////////////////
// DCB menu on top

const STARSButtonSize = 70

const (
	STARSButtonFull = 1 << iota
	STARSButtonHalfVertical
	STARSButtonHalfHorizontal
	STARSButtonSelected
)

func starsButtonSize(flags int, scale float32) [2]float32 {
	bs := func(s float32) float32 { return float32(int(s*STARSButtonSize + 0.5)) }

	if (flags & STARSButtonFull) != 0 {
		return [2]float32{bs(scale), bs(scale)}
	} else if (flags & STARSButtonHalfVertical) != 0 {
		return [2]float32{bs(scale), bs(scale / 2)}
	} else if (flags & STARSButtonHalfHorizontal) != 0 {
		return [2]float32{bs(scale / 2), bs(scale)}
	} else {
		lg.Errorf("unhandled starsButtonFlags %d", flags)
		return [2]float32{bs(scale), bs(scale)}
	}
}

var dcbDrawState struct {
	cb           *CommandBuffer
	mouse        *MouseState
	mouseDownPos []float32
	cursor       [2]float32
	drawStartPos [2]float32
	style        TextStyle
	brightness   STARSBrightness
	position     int
}

func (sp *STARSPane) StartDrawDCB(ctx *PaneContext, buttonScale float32, transforms ScopeTransformations,
	cb *CommandBuffer) {
	dcbDrawState.cb = cb
	dcbDrawState.mouse = ctx.mouse

	ps := sp.CurrentPreferenceSet
	dcbDrawState.brightness = ps.Brightness.DCB
	dcbDrawState.position = ps.DCBPosition
	switch dcbDrawState.position {
	case DCBPositionTop, DCBPositionLeft:
		dcbDrawState.drawStartPos = [2]float32{0, ctx.paneExtent.Height()}

	case DCBPositionRight:
		sz := starsButtonSize(STARSButtonFull, buttonScale) // FIXME: there should be a better way to get the default
		dcbDrawState.drawStartPos = [2]float32{ctx.paneExtent.Width() - sz[0], ctx.paneExtent.Height()}

	case DCBPositionBottom:
		sz := starsButtonSize(STARSButtonFull, buttonScale)
		dcbDrawState.drawStartPos = [2]float32{0, sz[1]}
	}

	dcbDrawState.cursor = dcbDrawState.drawStartPos

	dcbDrawState.style = TextStyle{
		Font:        sp.dcbFont[ps.CharSize.DCB],
		Color:       RGB{1, 1, 1},
		LineSpacing: 0,
		// DropShadow: true, // ????
		// DropShadowColor: RGB{0,0,0}, // ????
	}
	if dcbDrawState.style.Font == nil {
		lg.Errorf("nil buttonFont??")
		dcbDrawState.style.Font = GetDefaultFont()
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1)

	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		dcbDrawState.mouseDownPos = ctx.mouse.Pos[:]
	}

	/*
		imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{.7, .7, .7, 1})
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.Vec4{.075, .075, .075, 1})
		imgui.PushStyleColor(imgui.StyleColorButtonHovered, imgui.Vec4{.3, .3, .3, 1})
		imgui.PushStyleColor(imgui.StyleColorButtonActive, imgui.Vec4{0, .2, 0, 1})
	*/
}

func (sp *STARSPane) EndDrawDCB() {
	// Clear out the scissor et al...
	dcbDrawState.cb.ResetState()

	if mouse := dcbDrawState.mouse; mouse != nil {
		if mouse.Released[MouseButtonPrimary] {
			dcbDrawState.mouseDownPos = nil
		}
	}
}

func drawDCBText(text string, td *TextDrawBuilder, buttonSize [2]float32, color RGB) {
	// Clean up the text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}

	style := dcbDrawState.style
	style.Color = lerpRGB(.5, color, dcbDrawState.brightness.ScaleRGB(color))
	_, h := style.Font.BoundText(strings.Join(lines, "\n"), dcbDrawState.style.LineSpacing)

	slop := buttonSize[1] - float32(h) // todo: what if negative...
	y0 := dcbDrawState.cursor[1] - slop/2
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		x0 := dcbDrawState.cursor[0] + (buttonSize[0]-float32(lw))/2

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh)
	}
}

func drawDCBButton(text string, flags int, buttonScale float32, pushedIn bool, disabled bool) (Extent2D, bool) {
	ld := GetColoredLinesDrawBuilder()
	trid := GetColoredTrianglesDrawBuilder()
	td := GetTextDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	defer ReturnColoredTrianglesDrawBuilder(trid)
	defer ReturnTextDrawBuilder(td)

	sz := starsButtonSize(flags, buttonScale)

	// Offset for spacing
	const delta = 1
	p0 := add2f(dcbDrawState.cursor, [2]float32{delta, -delta})
	p1 := add2f(p0, [2]float32{sz[0] - 2*delta, 0})
	p2 := add2f(p1, [2]float32{0, -sz[1] + 2*delta})
	p3 := add2f(p2, [2]float32{-sz[0] + 2*delta, 0})

	ext := Extent2DFromPoints([][2]float32{p0, p2})
	mouse := dcbDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := dcbDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{dcbDrawState.mouseDownPos[0], dcbDrawState.mouseDownPos[1]})

	var buttonColor, textColor RGB
	if disabled {
		buttonColor = STARSDCBDisabledButtonColor
		textColor = STARSDCBDisabledTextColor
	}
	if !disabled {
		if mouseInside && mouseDownInside {
			pushedIn = !pushedIn
		}

		// Swap selected/regular color to indicate the tentative result
		buttonColor = Select(pushedIn, STARSDCBActiveButtonColor, STARSDCBButtonColor)
		textColor = Select(mouseInside, STARSDCBTextSelectedColor, STARSDCBTextColor)
	}
	buttonColor = dcbDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = dcbDrawState.brightness.ScaleRGB(textColor)

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawDCBText(text, td, sz, textColor)

	if !disabled && pushedIn { //((selected && !mouseInside) || (!selected && mouseInside && mouse.Down[MouseButtonPrimary])) {
		// Depressed bevel scheme: darker top/left, highlight bottom/right
		ld.AddLine(p0, p1, lerpRGB(.5, buttonColor, RGB{0, 0, 0}))
		ld.AddLine(p0, p3, lerpRGB(.5, buttonColor, RGB{0, 0, 0}))
		ld.AddLine(p1, p2, lerpRGB(.25, buttonColor, RGB{1, 1, 1}))
		ld.AddLine(p2, p3, lerpRGB(.25, buttonColor, RGB{1, 1, 1}))
	} else {
		// Normal bevel scheme: highlight top and left, darker bottom and right
		ld.AddLine(p0, p1, lerpRGB(.25, buttonColor, RGB{1, 1, 1}))
		ld.AddLine(p0, p3, lerpRGB(.25, buttonColor, RGB{1, 1, 1}))
		ld.AddLine(p1, p2, lerpRGB(.5, buttonColor, RGB{0, 0, 0}))
		ld.AddLine(p2, p3, lerpRGB(.5, buttonColor, RGB{0, 0, 0}))
	}

	updateDCBCursor(flags, sz)

	// FIXME: Attempt at scissoring when drawing buttons--breaks for half
	// height buttons--needs to be w.r.t. window coordinates (I think).
	/*
		highDPIScale := platform.DPIScale()
		x0, y0 := int(highDPIScale*p0[0]), int(highDPIScale*p0[1])
		w, h := int(highDPIScale*sz.X), int(highDPIScale*sz.Y)
		dcbDrawState.cb.Scissor(x0, y0, w, h)
	*/

	// Text last!
	trid.GenerateCommands(dcbDrawState.cb)
	ld.GenerateCommands(dcbDrawState.cb)
	td.GenerateCommands(dcbDrawState.cb)

	if mouse != nil && mouseInside && mouse.Released[MouseButtonPrimary] && mouseDownInside {
		return ext, true /* clicked and released */
	}
	return ext, false
}

func updateDCBCursor(flags int, sz [2]float32) {
	if dcbDrawState.position == DCBPositionTop || dcbDrawState.position == DCBPositionBottom {
		// Drawing left to right
		if (flags&STARSButtonFull) != 0 || (flags&STARSButtonHalfHorizontal) != 0 {
			// For full height buttons, always go to the next column
			dcbDrawState.cursor[0] += sz[0]
			dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
		} else if (flags & STARSButtonHalfVertical) != 0 {
			if dcbDrawState.cursor[1] == dcbDrawState.drawStartPos[1] {
				// Room for another half-height button below
				dcbDrawState.cursor[1] -= sz[1]
			} else {
				// On to the next column
				dcbDrawState.cursor[0] += sz[0]
				dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
			}
		} else {
			lg.Errorf("unhandled starsButtonFlags %d", flags)
			dcbDrawState.cursor[0] += sz[0]
			dcbDrawState.cursor[1] = dcbDrawState.drawStartPos[1]
		}
	} else {
		// Drawing top to bottom
		if (flags&STARSButtonFull) != 0 || (flags&STARSButtonHalfVertical) != 0 {
			// For full width buttons, always go to the next row
			dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
			dcbDrawState.cursor[1] -= sz[1]
		} else if (flags & STARSButtonHalfHorizontal) != 0 {
			if dcbDrawState.cursor[0] == dcbDrawState.drawStartPos[0] {
				// Room for another half-width button to the right
				dcbDrawState.cursor[0] += sz[0]
			} else {
				// On to the next row
				dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
				dcbDrawState.cursor[1] -= sz[1]
			}
		} else {
			lg.Errorf("unhandled starsButtonFlags %d", flags)
			dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
			dcbDrawState.cursor[1] -= sz[0]
		}
	}
}

func STARSToggleButton(text string, state *bool, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(text, flags, buttonScale, *state, false)

	if clicked {
		*state = !*state
	}

	return clicked
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
		func(v int) string { return strconv.Itoa(v) },
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

	if activeSpinner == unsafe.Pointer(value) {
		buttonBounds, clicked := drawDCBButton(text, flags, buttonScale, true, false)
		// This is horrific and one of many ugly things about capturing the
		// mouse, but most of Panes' work is in the simplified space of a
		// pane coordinate system; here we need something in terms of
		// window coordinates, so need to both account for the viewport
		// call that lets us draw things oblivious to the menubar as well
		// as flip things in y.
		h := ctx.paneExtent.Height() + ui.menuBarHeight
		buttonBounds.p0[1], buttonBounds.p1[1] = h-buttonBounds.p1[1], h-buttonBounds.p0[1]
		ctx.platform.StartCaptureMouse(buttonBounds)

		if clicked {
			activeSpinner = nil
			ctx.platform.EndCaptureMouse()
		}

		if ctx.mouse != nil {
			*value = callback(*value, -ctx.mouse.Wheel[1])
		}
	} else {
		_, clicked := drawDCBButton(text, flags, buttonScale, false, false)
		if clicked {
			activeSpinner = unsafe.Pointer(value)
		}
	}
}

func STARSFloatSpinner(ctx *PaneContext, text string, value *float32, min float32, max float32, flags int, buttonScale float32) {
	STARSCallbackSpinner(ctx, text, value, func(f float32) string { return fmt.Sprintf("%.1f", *value) },
		func(v float32, delta float32) float32 {
			return clamp(v+delta/10, min, max)
		}, flags, buttonScale)
}

func STARSBrightnessSpinner(ctx *PaneContext, text string, b *STARSBrightness, min STARSBrightness, allowOff bool,
	flags int, buttonScale float32) {
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
				if b == 0 && allowOff {
					return STARSBrightness(min)
				} else {
					b++
					return STARSBrightness(clamp(b, min, 100))
				}
			} else if delta < 0 {
				if b == min && allowOff {
					return STARSBrightness(0)
				} else {
					b--
					return STARSBrightness(clamp(b, min, 100))
				}
			} else {
				return b
			}
		}, flags, buttonScale)
}

func STARSSelectButton(text string, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(text, flags, buttonScale, flags&STARSButtonSelected != 0, false)
	return clicked
}

func (sp *STARSPane) STARSPlaceButton(text string, flags int, buttonScale float32,
	callback func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus) {
	_, clicked := drawDCBButton(text, flags, buttonScale, text == sp.selectedPlaceButton, false)
	if clicked {
		sp.selectedPlaceButton = text
		sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus {
			sp.selectedPlaceButton = ""
			return callback(pw, transforms)
		}
	}
}

func STARSDisabledButton(text string, flags int, buttonScale float32) {
	drawDCBButton(text, flags, buttonScale, false, true)
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

func (sp *STARSPane) initializeFonts() {
	init := func(fonts []*Font, name string, sizes []int) {
		for i, sz := range sizes {
			id := FontIdentifier{Name: name, Size: sz}
			fonts[i] = GetFont(id)
			if fonts[i] == nil {
				lg.Errorf("Font not found for %+v", id)
				fonts[i] = GetDefaultFont()
			}
		}
	}

	init(sp.systemFont[:], "Fixed Demi Bold", []int{9, 11, 12, 13, 14, 16})
	init(sp.dcbFont[:], "Inconsolata SemiBold", []int{10, 12, 14})
}

func (sp *STARSPane) resetInputState() {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.scopeClickHandler = nil
	sp.selectedPlaceButton = ""
}

const (
	RadarModeSingle = iota
	RadarModeMulti
	RadarModeFused
)

func (sp *STARSPane) radarMode(w *World) int {
	ps := sp.CurrentPreferenceSet
	if _, ok := w.RadarSites[ps.RadarSiteSelected]; ps.RadarSiteSelected != "" && ok {
		return RadarModeSingle
	} else if ps.FusedRadarMode {
		return RadarModeFused
	} else {
		return RadarModeMulti
	}
}

func (sp *STARSPane) radarVisibility(w *World, pos Point2LL, alt int) (primary, secondary bool, distance float32) {
	ps := sp.CurrentPreferenceSet
	distance = 1e30
	single := sp.radarMode(w) == RadarModeSingle
	for id, site := range w.RadarSites {
		if single && ps.RadarSiteSelected != id {
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
	single := sp.radarMode(w) == RadarModeSingle
	now := w.CurrentTime()
	for callsign, state := range sp.Aircraft {
		ac, ok := w.Aircraft[callsign]
		if !ok {
			continue
		}
		// This includes the case of a spawned aircraft for which we don't
		// yet have a radar track.
		if state.LostTrack(now) {
			continue
		}

		for id, site := range w.RadarSites {
			if single && ps.RadarSiteSelected != id {
				continue
			}

			if p, s, _ := site.CheckVisibility(w, state.TrackPosition(), state.TrackAltitude()); p || s {
				aircraft = append(aircraft, ac)

				// Is this the first we've seen it?
				if state.FirstRadarTrack.IsZero() {
					state.FirstRadarTrack = now

					if sp.AutoTrackDepartures && ac.TrackingController == "" &&
						w.DepartureController(ac) == w.Callsign {
						w.InitiateTrack(callsign, nil, nil) // ignore error...
					}
				}

				break
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) datablockVisible(ac *Aircraft) bool {
	af := sp.CurrentPreferenceSet.AltitudeFilters
	alt := sp.Aircraft[ac.Callsign].TrackAltitude()
	if !ac.IsAssociated() {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) getLeaderLineDirection(ac *Aircraft, w *World) CardinalOrdinalDirection {
	ps := sp.CurrentPreferenceSet

	if lld := sp.Aircraft[ac.Callsign].LeaderLineDirection; lld != nil {
		// The direction was specified for the aircraft specifically
		return *lld
	} else if ac.TrackingController == w.Callsign {
		// Tracked by us
		return ps.LeaderLineDirection
	} else if dir, ok := ps.ControllerLeaderLineDirections[ac.TrackingController]; ok {
		// Tracked by another controller for whom a direction was specified
		return dir
	} else if ps.OtherControllerLeaderLineDirection != nil {
		// Tracked by another controller without a per-controller direction specified
		return *ps.OtherControllerLeaderLineDirection
	} else {
		// TODO: should this case have a user-specifiable default?
		return CardinalOrdinalDirection(North)
	}
}

func (sp *STARSPane) getLeaderLineVector(dir CardinalOrdinalDirection) [2]float32 {
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

func (sp *STARSPane) tryGetClosestAircraft(w *World, mousePosition [2]float32, transforms ScopeTransformations) (*Aircraft, float32) {
	var ac *Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft(w) {
		pw := transforms.WindowFromLatLongP(sp.Aircraft[a.Callsign].TrackPosition())
		dist := distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac, distance
}

func (sp *STARSPane) tryGetClosestGhost(ghosts []*GhostAircraft, mousePosition [2]float32, transforms ScopeTransformations) (*GhostAircraft, float32) {
	var ghost *GhostAircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, g := range ghosts {
		pw := transforms.WindowFromLatLongP(g.Position)
		dist := distance2f(pw, mousePosition)
		if dist < distance {
			ghost = g
			distance = dist
		}
	}

	return ghost, distance
}

func (sp *STARSPane) radarSiteId(w *World) string {
	switch sp.radarMode(w) {
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
