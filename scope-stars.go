// scope-stars.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Main missing features:
// Collision alerts
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

	"github.com/mmp/imgui-go/v4"
)

var (
	STARSBackgroundColor         = RGB{0, 0, 0}
	STARSListColor               = RGB{.1, .9, .1}
	STARSTextAlertColor          = RGB{.7, .1, .1}
	STARSTrackBlockColor         = RGB{0.1, 0.4, 1}
	STARSTrackHistoryColor       = RGB{.2, 0, 1}
	STARSJRingConeColor          = RGB{.2, .2, 1}
	STARSTrackedAircraftColor    = RGB{1, 1, 1}
	STARSUntrackedAircraftColor  = RGB{.1, .9, .1}
	STARSPointedOutAircraftColor = RGB{.9, .9, .1}
	STARSSelectedAircraftColor   = RGB{.1, .9, .9}

	ErrSTARSIllegalParam  = errors.New("ILL PARAM")
	ErrSTARSIllegalTrack  = errors.New("ILL TRK")
	ErrSTARSCommandFormat = errors.New("FORMAT")
)

const NumSTARSPreferenceSets = 32
const NumSTARSRadarSites = 16
const NumSTARSMaps = 28

type STARSPane struct {
	ScopeName string

	currentPreferenceSet  STARSPreferenceSet
	SelectedPreferenceSet int
	PreferenceSets        []STARSPreferenceSet

	Facility STARSFacility

	weatherRadar WeatherRadar

	systemFont          [6]*Font
	LabelFontIdentifier FontIdentifier
	labelFont           *Font
	UIFontIdentifier    FontIdentifier
	uiFont              *Font

	eventsId EventSubscriberId

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	aircraft map[*Aircraft]*STARSAircraftState
	// map from legit to their ghost, if present
	ghostAircraft map[*Aircraft]*Aircraft

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

	selectedMapIndex        int
	havePlayedSPCAlertSound map[*Aircraft]interface{}

	lastCASoundTime time.Time
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
	CommandModeMapsMenu
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

	partialDatablockIfAssociated bool

	datablockErrText    string
	datablockText       [2][]string
	datablockDrawOffset [2]float32

	// Only drawn if non-zero
	jRingRadius    float32
	coneLength     float32
	displayTPASize bool // flip this so that zero-init works here? (What is the default?)

	leaderLineDirection int // 0 -> unset. Otherwise 1 +

	displayPilotAltitude bool
	pilotAltitude        int

	displayReportedBeacon bool // note: only for unassociated
	displayPTL            bool
	disableCAWarnings     bool
	disableMSAW           bool
	inhibitMSAWAlert      bool // only applies if in an alert. clear when alert is over?

	spcOverride string
}

///////////////////////////////////////////////////////////////////////////
// STARSFacility and related

type STARSFacility struct {
	Center     Point2LL // from sector file... / config
	Airports   []STARSAirport
	RadarSites []STARSRadarSite
	Maps       []STARSMap
	CA         struct {
		LateralMinimum  float32
		VerticalMinimum int32
		Floor           int32
	}
	CRDAConfig CRDAConfig

	// TODO: transition alt -> show pressure altitude above
	// TODO: RNAV patterns
	// TODO: automatic scratchpad stuff
}

type STARSAirport struct {
	ICAOCode       string
	Range          int32 // nm distance when arrival is added to tower list
	IncludeInSSA   bool  // whether airport's weather is included in the SSA
	TowerListIndex int   // 0 -> none. i.e., indexing is off by 1 vs go arrays, but matches STARS lists
}

type STARSRadarSite struct {
	Char     string
	Id       string
	Position string

	Elevation      int32
	PrimaryRange   int32
	SecondaryRange int32
	SlopeAngle     float32
	SilenceAngle   float32
}

type STARSMap struct {
	Name  string
	Group int // 0 -> A, 1 -> B
	Draw  *StaticDrawConfig
}

func MakeDefaultFacility() STARSFacility {
	var f STARSFacility

	f.Center = database.defaultCenter

	f.Airports = append(f.Airports,
		STARSAirport{
			ICAOCode:     database.defaultAirport,
			Range:        60,
			IncludeInSSA: true})

	f.CA.LateralMinimum = 3
	f.CA.VerticalMinimum = 1000
	f.CA.Floor = 500
	f.CRDAConfig = NewCRDAConfig()

	return f
}

func (rs *STARSRadarSite) Valid() bool {
	_, ok := database.Locate(rs.Position)
	return rs.Char != "" && rs.Id != "" && rs.Position != "" && ok
}

func (rs *STARSRadarSite) CheckVisibility(p Point2LL, altitude int) (primary, secondary bool, distance float32) {
	// Check altitude first; this is a quick first cull that
	// e.g. takes care of everyone on the ground.
	if altitude < int(rs.Elevation) {
		return
	}

	pRadar, ok := database.Locate(rs.Position)
	if !ok {
		// Really, this method shouldn't be called if the site is invalid,
		// but if it is, there's not much else we can do.
		return
	}

	// Time to check the angles; we'll do all of this in nm coordinates,
	// since that's how we check the range anyway.
	p = ll2nm(p)
	palt := float32(altitude) * FeetToNauticalMiles
	pRadar = ll2nm(pRadar)
	ralt := float32(rs.Elevation) * FeetToNauticalMiles

	dxy := sub2f(p, pRadar)
	dalt := palt - ralt
	distance = sqrt(sqr(dxy[0]) + sqr(dxy[1]) + sqr(dalt))

	// If we normalize the vector from the radar site to the aircraft, then
	// the z (altitude) component gives the cosine of the angle with the
	// "up" direction; in turn, we can check that against the two angles.
	cosAngle := dalt / distance
	// if angle < silence angle, we can't see it, but the test flips since
	// we're testing cosines.
	// FIXME: it's annoying to be repeatedly computing these cosines here...
	if cosAngle > cos(radians(rs.SilenceAngle)) {
		// inside the cone of silence
		return
	}
	// similarly, if angle > 90-slope angle, we can't see it, but again the
	// test flips.
	if cosAngle < cos(radians(90-rs.SlopeAngle)) {
		// below the slope angle
		return
	}

	primary = distance <= float32(rs.PrimaryRange)
	secondary = !primary && distance <= float32(rs.SecondaryRange)
	return
}

func MakeSTARSMap() STARSMap {
	return STARSMap{Draw: NewStaticDrawConfig()}
}

///////////////////////////////////////////////////////////////////////////
// STARSPreferenceSet

type STARSPreferenceSet struct {
	Name string

	DisplayDCB bool

	Center Point2LL
	Range  float32

	currentCenter Point2LL
	offCenter     bool

	RangeRingsCenter Point2LL
	RangeRingRadius  int

	// TODO? cursor speed

	// Note: not saved across sessions
	currentATIS string
	giText      [9]string

	RadarTrackHistory int

	WeatherIntensity [6]bool

	// No more than one can be true at a time. If all are false, then MULTI
	RadarSiteSelected []bool

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

	MapVisible []bool

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

	ps.Center = facility.Center
	ps.Range = 50

	ps.currentCenter = ps.Center

	ps.RangeRingsCenter = ps.Center
	ps.RangeRingRadius = 5

	ps.RadarTrackHistory = 5

	ps.WeatherIntensity[1] = true

	ps.RadarSiteSelected = make([]bool, len(facility.RadarSites))
	ps.MapVisible = make([]bool, len(facility.Maps))

	ps.LeaderLineDirection = North
	ps.LeaderLineLength = 3

	ps.AltitudeFilters.Unassociated = [2]int{100, 60000}
	ps.AltitudeFilters.Associated = [2]int{100, 60000}

	ps.DisplayUncorrelatedTargets = true

	ps.DisplayTPASize = true

	ps.PTLLength = 3

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

	ps.PreviewAreaPosition = [2]float32{.05, .6}

	ps.SSAList.Position = [2]float32{.05, .95}
	ps.SSAList.Visible = true
	ps.SSAList.Filter.All = true

	ps.TABList.Position = [2]float32{.85, .8}
	ps.TABList.Lines = 5
	ps.TABList.Visible = true

	ps.VFRList.Position = [2]float32{.85, .4}
	ps.VFRList.Lines = 5
	ps.VFRList.Visible = true

	ps.AlertList.Position = [2]float32{.85, .25}
	ps.AlertList.Lines = 5
	ps.AlertList.Visible = true

	ps.CoastList.Position = [2]float32{.85, .65}
	ps.CoastList.Lines = 5
	ps.CoastList.Visible = true

	ps.SignOnList.Position = [2]float32{.85, .95}
	ps.SignOnList.Visible = true

	ps.VideoMapsList.Position = [2]float32{.85, .5}
	ps.VideoMapsList.Visible = true

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
	dupe.RadarSiteSelected = DuplicateSlice(ps.RadarSiteSelected)
	dupe.SelectedBeaconCodes = DuplicateSlice(ps.SelectedBeaconCodes)
	dupe.MapVisible = DuplicateSlice(ps.MapVisible)
	return dupe
}

func (ps *STARSPreferenceSet) Activate() {
	ps.currentCenter = ps.Center
}

func (ps *STARSPreferenceSet) multiRadarMode() bool {
	for _, v := range ps.RadarSiteSelected {
		if v {
			return false
		}
	}
	return true
}

///////////////////////////////////////////////////////////////////////////
// Utility types and methods

type DatablockType int

const (
	FullDatablock = iota
	PartialDatablock
	LimitedDatablock
)

func flightPlanSTARS(ac *Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalTrack // ??
	}

	// AAL1416 B738/L squawk controller id
	// (start of route) (alt 100s)
	result := ac.Callsign + " " + fp.AircraftType + " " + ac.AssignedSquawk.String() + " "
	if ctrl := server.GetController(ac.TrackingController); ctrl != nil {
		if pos := ctrl.GetPosition(); pos != nil {
			result += pos.SectorId
		}
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
func NewSTARSPane(n string) *STARSPane {
	uiFont := GetFont(FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 12})
	return &STARSPane{
		ScopeName:             n,
		Facility:              MakeDefaultFacility(),
		SelectedPreferenceSet: -1,
		UIFontIdentifier:      uiFont.id,
	}
}

func (sp *STARSPane) Duplicate(nameAsCopy bool) Pane {
	dupe := &STARSPane{}
	*dupe = *sp // get the easy stuff
	if nameAsCopy {
		dupe.ScopeName += " Copy"
	}

	// Facility
	dupe.Facility.Airports = DuplicateSlice(sp.Facility.Airports)
	for i := range sp.Facility.Maps {
		dupe.Facility.Maps[i].Draw = sp.Facility.Maps[i].Draw.Duplicate()
	}

	dupe.PreferenceSets = make([]STARSPreferenceSet, len(sp.PreferenceSets))
	for i := range sp.PreferenceSets {
		dupe.PreferenceSets[i] = sp.PreferenceSets[i].Duplicate()
	}

	// Internal state
	dupe.aircraft = make(map[*Aircraft]*STARSAircraftState)
	for ac, tracked := range sp.aircraft {
		dupe.aircraft[ac] = &STARSAircraftState{}
		*dupe.aircraft[ac] = *tracked
	}

	dupe.ghostAircraft = make(map[*Aircraft]*Aircraft)
	for ac, gh := range sp.ghostAircraft {
		ghost := *gh // make a copy
		dupe.ghostAircraft[ac] = &ghost
	}

	dupe.havePlayedSPCAlertSound = DuplicateMap(sp.havePlayedSPCAlertSound)

	dupe.pointedOutAircraft = NewTransientMap[*Aircraft, string]()
	dupe.queryUnassociated = NewTransientMap[*Aircraft, interface{}]()

	dupe.eventsId = eventStream.Subscribe()

	return dupe
}

func (sp *STARSPane) Activate() {
	if sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets) {
		sp.currentPreferenceSet = sp.PreferenceSets[sp.SelectedPreferenceSet]
	} else {
		sp.currentPreferenceSet = MakePreferenceSet("", sp.Facility)
	}
	sp.currentPreferenceSet.Activate()

	for i := range sp.Facility.Maps {
		sp.Facility.Maps[i].Draw.Activate()
	}

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

	if sp.labelFont = GetFont(sp.LabelFontIdentifier); sp.labelFont == nil {
		sp.labelFont = GetDefaultFont()
		sp.LabelFontIdentifier = sp.labelFont.id
	}
	if sp.uiFont = GetFont(sp.UIFontIdentifier); sp.uiFont == nil {
		sp.uiFont = GetDefaultFont()
		sp.UIFontIdentifier = sp.uiFont.id
	}

	sp.eventsId = eventStream.Subscribe()

	sp.weatherRadar.Activate(sp.currentPreferenceSet.Center)

	// start tracking all of the active aircraft
	sp.initializeAircraft()
}

func (sp *STARSPane) Deactivate() {
	for i := range sp.Facility.Maps {
		sp.Facility.Maps[i].Draw.Deactivate()
	}

	// Drop all of them
	sp.aircraft = nil
	sp.ghostAircraft = nil

	eventStream.Unsubscribe(sp.eventsId)
	sp.eventsId = InvalidEventSubscriberId

	sp.weatherRadar.Deactivate()
}

func (sp *STARSPane) Name() string { return sp.ScopeName }

func (sp *STARSPane) DrawUI() {
	imgui.InputText("Name", &sp.ScopeName)

	if newFont, changed := DrawFontPicker(&sp.LabelFontIdentifier, "Label font"); changed {
		sp.labelFont = newFont
	}

	errorExclamationTriangle := func() {
		color := positionConfig.GetColorScheme().TextError
		imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
		imgui.Text(FontAwesomeIconExclamationTriangle)
		imgui.PopStyleColor()
	}

	if imgui.CollapsingHeader("Airports") {
		tableFlags := imgui.TableFlagsBorders | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchSame
		if imgui.BeginTableV("airports", 8, tableFlags, imgui.Vec2{800, 0}, 0) {
			imgui.TableSetupColumn("ICAO code")
			imgui.TableSetupColumn("Range")
			imgui.TableSetupColumn("In SSA")
			imgui.TableSetupColumn("List 1\n")
			imgui.TableSetupColumn("List 2\n")
			imgui.TableSetupColumn("List 3\n")
			imgui.TableSetupColumn("No list\n")
			imgui.TableSetupColumn("##trash")

			imgui.TableHeadersRow()

			deleteIndex := -1
			for i := range sp.Facility.Airports {
				ap := &sp.Facility.Airports[i]
				imgui.PushID(fmt.Sprintf("%d", i))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				flags := imgui.InputTextFlagsCharsNoBlank | imgui.InputTextFlagsCharsUppercase
				imgui.InputTextV("##ICAO", &ap.ICAOCode, flags, nil)
				if ap.ICAOCode != "" {
					invalid := false
					for j := 0; j < i; j++ {
						if sp.Facility.Airports[j].ICAOCode == ap.ICAOCode {
							invalid = true
						}
					}
					if _, ok := database.Locate(ap.ICAOCode); !ok {
						invalid = true
					}

					if invalid {
						imgui.SameLine()
						errorExclamationTriangle()
					}
				}

				imgui.TableNextColumn()
				imgui.InputIntV("##pr", &ap.Range, 0, 0, 0)

				imgui.TableNextColumn()
				imgui.Checkbox("##check", &ap.IncludeInSSA)

				for list := 1; list <= 3; list++ {
					imgui.TableNextColumn()
					if imgui.RadioButtonInt(fmt.Sprintf("##list%d", list), &ap.TowerListIndex, list) {
						for j := range sp.Facility.Airports {
							if j != i && sp.Facility.Airports[j].TowerListIndex == list {
								sp.Facility.Airports[j].TowerListIndex = 0
							}
						}
					}
				}

				imgui.TableNextColumn()
				imgui.RadioButtonInt("##list0", &ap.TowerListIndex, 0)

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconTrash) {
					deleteIndex = i
				}

				imgui.PopID()
			}

			imgui.EndTable()

			if deleteIndex != -1 {
				sp.Facility.Airports = DeleteSliceElement(sp.Facility.Airports, deleteIndex)
			}
			if imgui.Button("Add airport") {
				sp.Facility.Airports = append(sp.Facility.Airports, STARSAirport{})
			}
		}
	}

	if imgui.CollapsingHeader("Radar sites") {
		tableFlags := imgui.TableFlagsBorders | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchSame
		deleteIndex := -1
		if imgui.BeginTableV("sites", 9, tableFlags, imgui.Vec2{900, 0}, 0) {
			imgui.TableSetupColumn("Char")
			imgui.TableSetupColumn("Id")
			imgui.TableSetupColumn("Position")
			imgui.TableSetupColumn("Elevation")
			imgui.TableSetupColumn("Primary range")
			imgui.TableSetupColumn("Secondary range")
			imgui.TableSetupColumn("Slope angle")
			imgui.TableSetupColumn("Silence angle")
			imgui.TableSetupColumn("##trash")

			imgui.TableHeadersRow()

			for i := range sp.Facility.RadarSites {
				site := &sp.Facility.RadarSites[i]
				imgui.PushID(fmt.Sprintf("%d", i))
				flags := imgui.InputTextFlagsCharsNoBlank | imgui.InputTextFlagsCharsUppercase

				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.InputTextV("##Char", &site.Char, flags, nil)
				if len(site.Char) > 1 {
					site.Char = site.Char[:1]
				}
				// Indicate an error if a Char is repeated
				if site.Char != "" {
					for j, other := range sp.Facility.RadarSites {
						if j != i && other.Char == site.Char {
							imgui.SameLine()
							errorExclamationTriangle()
							break
						}
					}
				}

				imgui.TableNextColumn()
				imgui.InputTextV("##Id", &site.Id, flags, nil)
				if len(site.Id) > 3 {
					site.Id = site.Id[:3]
				}
				// Indicate an error if an Id is repeated
				if site.Id != "" {
					for j, other := range sp.Facility.RadarSites {
						if j != i && other.Id == site.Id {
							imgui.SameLine()
							errorExclamationTriangle()
							break
						}
					}
				}

				imgui.TableNextColumn()
				imgui.InputTextV("##position", &site.Position, flags, nil)
				if _, ok := database.Locate(site.Position); site.Position != "" && !ok {
					imgui.SameLine()
					errorExclamationTriangle()
				}

				imgui.TableNextColumn()
				imgui.InputIntV("##elev", &site.Elevation, 0, 0, 0)

				imgui.TableNextColumn()
				imgui.InputIntV("##pr", &site.PrimaryRange, 0, 0, 0)

				imgui.TableNextColumn()
				imgui.InputIntV("##sr", &site.SecondaryRange, 0, 0, 0)

				imgui.TableNextColumn()
				imgui.SliderFloat("##slope", &site.SlopeAngle, 0, 90)

				imgui.TableNextColumn()
				imgui.SliderFloat("##silence", &site.SilenceAngle, 0, 90)

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconTrash) {
					deleteIndex = i
				}

				imgui.PopID()
			}
			imgui.EndTable()

			if deleteIndex != -1 {
				sp.Facility.RadarSites = DeleteSliceElement(sp.Facility.RadarSites, deleteIndex)
				sp.currentPreferenceSet.RadarSiteSelected =
					DeleteSliceElement(sp.currentPreferenceSet.RadarSiteSelected, deleteIndex)
				for i := range sp.PreferenceSets {
					sp.PreferenceSets[i].RadarSiteSelected =
						DeleteSliceElement(sp.PreferenceSets[i].RadarSiteSelected, deleteIndex)
				}
			}
			if len(sp.Facility.RadarSites) < NumSTARSRadarSites && imgui.Button("Add radar site") {
				sp.Facility.RadarSites = append(sp.Facility.RadarSites, STARSRadarSite{})
				sp.currentPreferenceSet.RadarSiteSelected =
					append(sp.currentPreferenceSet.RadarSiteSelected, false)
				for i := range sp.PreferenceSets {
					sp.PreferenceSets[i].RadarSiteSelected =
						append(sp.PreferenceSets[i].RadarSiteSelected, false)
				}
			}
		}
	}

	if imgui.CollapsingHeader("Maps") {
		selected := ""
		if sp.selectedMapIndex < len(sp.Facility.Maps) {
			selected = sp.Facility.Maps[sp.selectedMapIndex].Name
		}

		if imgui.BeginCombo("##MapName", selected) {
			for i, m := range sp.Facility.Maps {
				if imgui.SelectableV(m.Name, i == sp.selectedMapIndex, 0, imgui.Vec2{}) {
					sp.selectedMapIndex = i
				}
			}

			if len(sp.Facility.Maps) < NumSTARSMaps && imgui.Selectable("New...##new") {
				sp.Facility.Maps = append(sp.Facility.Maps, MakeSTARSMap())
				sp.selectedMapIndex = len(sp.Facility.Maps) - 1
				sp.currentPreferenceSet.MapVisible =
					append(sp.currentPreferenceSet.MapVisible, false)
				for i := range sp.PreferenceSets {
					sp.PreferenceSets[i].MapVisible =
						append(sp.PreferenceSets[i].MapVisible, false)
				}
			}

			imgui.EndCombo()
		}

		if sp.selectedMapIndex < len(sp.Facility.Maps) {
			m := &sp.Facility.Maps[sp.selectedMapIndex]
			imgui.InputTextV("Name##map", &m.Name, imgui.InputTextFlagsCharsUppercase, nil)
			imgui.RadioButtonInt("A", &m.Group, 0)
			imgui.SameLine()
			imgui.RadioButtonInt("B", &m.Group, 1)
			imgui.SameLine()
			imgui.Text("Draw group")
			m.Draw.DrawUI()
			imgui.Separator()
		}
	}

	if imgui.CollapsingHeader("Collision alerts") {
		imgui.SliderFloatV("Lateral minimum (nm)", &sp.Facility.CA.LateralMinimum, 0, 10, "%.1f", 0)
		imgui.InputIntV("Vertical minimum (feet)", &sp.Facility.CA.VerticalMinimum, 100, 100, 0)
		imgui.InputIntV("Altitude floor (feet)", &sp.Facility.CA.Floor, 100, 100, 0)
	}

	if imgui.CollapsingHeader("CRDA") {
		sp.Facility.CRDAConfig.DrawUI()
	}
}

func (sp *STARSPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STARSPane) processEvents(es *EventStream) {
	ps := sp.currentPreferenceSet

	for _, event := range es.Get(sp.eventsId) {
		switch v := event.(type) {
		case *AddedAircraftEvent:
			sp.aircraft[v.ac] = &STARSAircraftState{}
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
					globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
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
					globalConfig.AudioSettings.HandleEvent(AudioEventAlert)
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

	transforms := GetScopeTransformations(ctx, sp.currentPreferenceSet.currentCenter,
		float32(sp.currentPreferenceSet.Range), 0)
	ps := sp.currentPreferenceSet

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
	sp.weatherRadar.Draw(weatherIntensity, transforms, cb)

	color := ps.Brightness.RangeRings.RGB()
	cb.LineWidth(1)
	DrawRangeRings(ps.RangeRingsCenter, float32(ps.RangeRingRadius), color, transforms, cb)

	transforms.LoadWindowViewingMatrices(cb)

	// Maps
	cb.PointSize(5)
	cb.LineWidth(1)
	for i, vis := range ps.MapVisible {
		if vis {
			color := ps.Brightness.VideoGroupA.RGB()
			if sp.Facility.Maps[i].Group == 1 {
				color = ps.Brightness.VideoGroupB.RGB()
			}
			cb.LineWidth(1)
			sp.Facility.Maps[i].Draw.Draw(ctx, sp.labelFont, &color, transforms, cb)
		}
	}

	if ps.Brightness.Compass > 0 {
		cb.LineWidth(1)
		cbright := ps.Brightness.Compass.RGB()
		DrawCompass(ps.currentCenter, ctx, 0, sp.labelFont, cbright, drawBounds, transforms, cb)
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

	DrawHighlighted(ctx, transforms, cb)

	sp.drawTracks(aircraft, ctx, transforms, cb)
	sp.updateDatablockTextAndPosition(aircraft)
	sp.drawDatablocks(aircraft, ctx, transforms, cb)
	sp.consumeMouseEvents(ctx, transforms)
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

	ps := &sp.currentPreferenceSet

	//lg.Printf("input \"%s\" ctl %v alt %v", input, ctx.keyboard.IsPressed(KeyControl), ctx.keyboard.IsPressed(KeyAlt))
	if ctx.keyboard.IsPressed(KeyControl) && len(input) == 1 && unicode.IsDigit(rune(input[0])) {
		idx := byte(input[0]) - '0'
		// This test should be redundant given the IsDigit check, but just to be safe...
		if int(idx) < len(ps.Bookmarks) {
			if ctx.keyboard.IsPressed(KeyAlt) {
				// Record bookmark
				ps.Bookmarks[idx].Center = ps.currentCenter
				ps.Bookmarks[idx].Range = ps.Range
				ps.Bookmarks[idx].TopDownMode = ps.TopDownMode
			} else {
				// Recall bookmark
				ps.Center = ps.Bookmarks[idx].Center
				ps.currentCenter = ps.Bookmarks[idx].Center
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
				ps.Center = sp.Facility.Center
				ps.currentCenter = ps.Center
			}

		case KeyF2:
			if ctx.keyboard.IsPressed(KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner()
				sp.activeDCBMenu = DCBMenuMaps
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

func (sp *STARSPane) executeSTARSCommand(cmd string) (status STARSCommandStatus) {
	lookupCallsign := func(callsign string) string {
		if server.GetAircraft(callsign) != nil {
			return callsign
		}
		// try to match squawk code
		for _, ac := range sp.visibleAircraft() {
			if ac.Squawk.String() == callsign {
				return ac.Callsign
			}
		}

		// TODO: handle list number

		return callsign
	}

	ps := &sp.currentPreferenceSet

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

	case CommandModeInitiateControl:
		status.err = server.InitiateTrack(lookupCallsign(cmd))
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for ac := range sp.aircraft {
				if ac.TrackingController == server.Callsign() {
					status.err = server.DropTrack(ac.Callsign)
				}
			}
			status.clear = true
			return
		} else {
			status.err = server.DropTrack(lookupCallsign(cmd))
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
				d := nmdistance2ll(ps.RangeRingsCenter, ac.Position())
				if closest == nil || d < closestDistance {
					closest = ac
					closestDistance = d
				}
			}

			if closest != nil {
				status.err = server.AcceptHandoff(closest.Callsign)
			}
			status.clear = true
			return
		case 1:
			status.err = server.CancelHandoff(lookupCallsign(f[0]))
			status.clear = true
			return
		case 2:
			status.err = server.Handoff(lookupCallsign(f[1]), f[0])
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
			if ac := server.GetAircraft(lookupCallsign(cmd)); ac != nil {
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
			lldir := func(cmd byte) (int, error) {
				if cmd >= '1' && cmd <= '9' {
					return int(cmd - '0'), nil
				} else {
					return 0, ErrSTARSIllegalParam
				}
			}

			setLLDir := func(dir int, pred func(*Aircraft) bool) {
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
				if dir, err := lldir(cmd[0]); err == nil {
					// Tracked by me
					me := server.Callsign()
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == me })
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case 2:
				dir, err := lldir(cmd[0])
				if err == nil && cmd[1] == 'U' {
					// FIXME: should be unassociated tracks
					setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == "" })
					status.clear = true
				} else if err == nil && cmd[1] == '*' {
					// Tracked by other controllers
					me := server.Callsign()
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
				for _, ctrl := range server.GetAllControllers() {
					if pos := ctrl.GetPosition(); pos != nil && pos.SectorId == cmd[:2] {
						dir, err := lldir(cmd[3])
						if cmd[2] == ' ' && err == nil {
							setLLDir(dir, func(ac *Aircraft) bool { return ac.TrackingController == ctrl.Callsign })
							status.clear = true
							status.err = nil
						}
					}
				}
				return

			default:
				// L(dir)(space)(callsign)
				if dir, err := lldir(cmd[0]); err == nil && cmd[1] == ' ' {
					// We know len(cmd) >= 3 given the above cases...
					callsign := lookupCallsign(cmd[2:])
					if ac := server.GetAircraft(callsign); ac != nil {
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

		case "S":
			switch len(cmd) {
			case 0:
				// S -> clear atis, first line of text
				ps.currentATIS = ""
				ps.giText[0] = ""
				status.clear = true
				return

			case 1:
				if cmd[0] == '*' {
					// S* -> clear atis
					ps.currentATIS = ""
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' {
					// S[1-9] -> clear corresponding line of text
					idx := cmd[0] - '1'
					ps.giText[idx] = ""
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis) -> set atis code
					ps.currentATIS = string(cmd[0])
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}

			default:
				if len(cmd) == 2 && cmd[0] >= 'A' && cmd[0] <= 'Z' && cmd[1] == '*' {
					// S(atis)* -> set atis, delete first line of text
					ps.currentATIS = string(cmd[0])
					ps.giText[0] = ""
					status.clear = true
					return
				} else if cmd[0] == '*' {
					// S*(text) -> clear atis, set first line of gi text
					ps.currentATIS = ""
					ps.giText[0] = cmd[1:]
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' && cmd[1] == ' ' {
					// S[1-9](spc)(text) -> set corresponding line of GI text
					idx := cmd[0] - '1'
					ps.giText[idx] = cmd[2:]
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis)(text) -> set atis and first line of GI text
					ps.currentATIS = string(cmd[0])
					ps.giText[0] = cmd[1:]
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
				if ac := server.GetAircraft(callsign); ac != nil {
					sp.aircraft[ac].pilotAltitude = 0
				}
				status.err = server.SetScratchpad(callsign, "")
				status.clear = true
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt

				callsign := lookupCallsign(f[0])
				// Either pilot alt or scratchpad entry
				if ac := server.GetAircraft(callsign); ac == nil {
					status.err = ErrSTARSIllegalTrack
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.aircraft[ac].pilotAltitude = alt * 100
				} else {
					status.err = server.SetScratchpad(callsign, f[1])
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
			status.err = server.SetSquawkAutomatic(callsign)
		} else if len(f) == 2 {
			if squawk, err := ParseSquawk(f[1]); err == nil {
				callsign := lookupCallsign(f[0])
				status.err = server.SetSquawk(callsign, squawk)
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
			if ac := server.GetAircraft(callsign); ac != nil {
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
		psave := sp.currentPreferenceSet.Duplicate()
		psave.Name = cmd
		sp.PreferenceSets = append(sp.PreferenceSets, psave)
		status.clear = true
		return

	case CommandModeMapsMenu:
		if len(cmd) > 0 {
			if m, err := strconv.Atoi(cmd); err == nil && m >= 0 && m < len(ps.MapVisible) {
				ps.MapVisible[m] = !ps.MapVisible[m]
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
			for i := range ps.RadarSiteSelected {
				ps.RadarSiteSelected[i] = false
			}
			status.clear = true
			return
		} else {
			// Index, character id, or name
			if i, err := strconv.Atoi(cmd); err == nil && i >= 0 && i < NumSTARSRadarSites {
				for j := range ps.RadarSiteSelected {
					ps.RadarSiteSelected[i] = (i == j)
				}
				status.clear = true
				return
			}
			for i, rs := range sp.Facility.RadarSites {
				if cmd == rs.Char || cmd == rs.Id {
					for j := range ps.RadarSiteSelected {
						ps.RadarSiteSelected[i] = (i == j)
					}
					status.clear = true
					return
				}
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
		for _, ctrl := range server.GetAllControllers() {
			if pos := ctrl.GetPosition(); pos != nil && pos.SectorId == id {
				return true
			}
		}
		return false
	}

	ps := &sp.currentPreferenceSet

	if ac != nil {
		state := sp.aircraft[ac]

		switch sp.commandMode {
		case CommandModeNone:
			switch len(cmd) {
			case 0:
				if ac.InboundHandoffController != "" {
					// Accept inbound h/o
					status.clear = true
					status.err = server.AcceptHandoff(ac.Callsign)
					return
				} else if ac.OutboundHandoffController != "" {
					// cancel offered handoff offered
					status.clear = true
					status.err = server.CancelHandoff(ac.Callsign)
					return
				} else if _, ok := sp.pointedOutAircraft.Get(ac); ok {
					// ack point out
					sp.pointedOutAircraft.Delete(ac)
					status.clear = true
					return
				} else if ac.TrackingController == server.Callsign() {
					// TODO: display reported and assigned beacon codes for owned track (where?)
				} else if ac.IsAssociated() {
					state.partialDatablockIfAssociated = !state.partialDatablockIfAssociated
				} else {
					sp.queryUnassociated.Add(ac, nil, 5*time.Second)
				}
				// TODO: ack SPC alert

			case 1:
				if cmd == "." {
					status.clear = true
					status.err = server.SetScratchpad(ac.Callsign, "")
					return
				} else if cmd == "U" {
					status.clear = true
					status.err = server.RejectHandoff(ac.Callsign)
					return
				} else if cmd == "*" {
					from := ac.Position()
					sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
						p := transforms.LatLongFromWindowP(pw)
						hdg := headingp2ll(from, p, database.MagneticVariation)
						dist := nmdistance2ll(from, p)

						status.output = fmt.Sprintf("%03d/%.2f", int(hdg+.5), dist)
						status.clear = true
						return
					}
					return
				} else if dir, err := strconv.Atoi(cmd); err == nil && dir != 0 {
					state.leaderLineDirection = dir
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case 2:
				if isControllerId(cmd) {
					status.clear = true
					status.err = server.Handoff(ac.Callsign, cmd)
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
				if cmd == "*D+" {
					ps.DisplayTPASize = !ps.DisplayTPASize
					status.clear = true
					return
				} else if cmd[2] == '*' && isControllerId(cmd[:2]) {
					status.clear = true
					status.err = server.PointOut(ac.Callsign, cmd[:2])
					return
				} else {
					if alt, err := strconv.Atoi(cmd); err == nil {
						state.pilotAltitude = alt * 100
						status.clear = true
						return
					}
					// Don't worry about an error here since we still have
					// e.g., *J and *P below...
				}

			case 4:
				if cmd[0] == '+' {
					if alt, err := strconv.Atoi(cmd[1:]); err == nil {
						status.clear = true
						status.err = server.SetTemporaryAltitude(ac.Callsign, alt*100)
					} else {
						status.err = ErrSTARSIllegalParam
					}
					return
				} else {
					status.clear = true
					status.err = amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
						fp.AircraftType = strings.TrimRight(cmd, "*")
					})
					return
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
				lg.Printf("cone cmd %s :2 %s", cmd, cmd[2:])
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

		case CommandModeInitiateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			status.err = server.InitiateTrack(ac.Callsign)
			return

		case CommandModeTerminateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			status.err = server.DropTrack(ac.Callsign)
			return

		case CommandModeHandOff:
			if cmd == "" {
				status.clear = true
				status.err = server.CancelHandoff(ac.Callsign)
			} else {
				status.clear = true
				status.err = server.Handoff(ac.Callsign, cmd)
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
				if dir, err := strconv.Atoi(cmd); err == nil && dir != 0 && len(cmd) == 1 {
					state.leaderLineDirection = dir
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
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
					status.err = server.SetScratchpad(ac.Callsign, "")
					return
				} else {
					// Is it an altitude or a scratchpad update?
					if alt, err := strconv.Atoi(cmd[1:]); err == nil && len(cmd[1:]) == 3 {
						state.pilotAltitude = alt * 100
						status.clear = true
					} else {
						status.clear = true
						status.err = server.SetScratchpad(ac.Callsign, cmd[1:])
					}
					return
				}
			}

		case CommandModeFlightData:
			if cmd == "" {
				status.clear = true
				status.err = server.SetSquawkAutomatic(ac.Callsign)
				return
			} else if cmd == "V" {
				status.clear = true
				status.err = server.SetVoiceType(ac.Callsign, VoiceFull)
				return
			} else if cmd == "R" {
				status.clear = true
				status.err = server.SetVoiceType(ac.Callsign, VoiceReceive)
				return
			} else if cmd == "T" {
				status.clear = true
				status.err = server.SetVoiceType(ac.Callsign, VoiceText)
				return
			} else {
				if squawk, err := ParseSquawk(cmd); err == nil {
					status.err = server.SetSquawk(ac.Callsign, squawk)
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

	ps := &sp.currentPreferenceSet

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
				ps.currentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			}
		}
		ps.offCenter = ps.currentCenter != ps.Center
		if STARSToggleButton("OFF\nCNTR", &ps.offCenter, STARSButtonHalfVertical) {
			ps.currentCenter = ps.Center
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
			if i >= len(ps.MapVisible) {
				STARSDisabledButton(fmt.Sprintf(" %d\n", i+1), STARSButtonHalfVertical)
			} else {
				name := fmt.Sprintf(" %d\n%s", i+1, sp.Facility.Maps[i].Name)
				STARSToggleButton(name, &ps.MapVisible[i], STARSButtonHalfVertical)
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
			for i := range ps.MapVisible {
				ps.MapVisible[i] = false
			}
		}
		for i := 0; i < NumSTARSMaps; i++ {
			if i >= len(sp.Facility.Maps) {
				STARSDisabledButton(fmt.Sprintf(" %d", i+1), STARSButtonHalfVertical)
			} else {
				STARSToggleButton(sp.Facility.Maps[i].Name, &ps.MapVisible[i], STARSButtonHalfVertical)
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
			if STARSSelectButton(text, STARSButtonHalfVertical) {
				// Make this one current
				sp.SelectedPreferenceSet = i
				sp.currentPreferenceSet = sp.PreferenceSets[i]
			}
		}
		for i := len(sp.PreferenceSets); i < NumSTARSPreferenceSets; i++ {
			STARSDisabledButton(fmt.Sprintf("%d\n", i+1), STARSButtonHalfVertical)
		}

		if STARSSelectButton("DEFAULT", STARSButtonHalfVertical) {
			sp.currentPreferenceSet = MakePreferenceSet("", sp.Facility)
		}
		STARSDisabledButton("FSSTARS", STARSButtonHalfVertical)
		if STARSSelectButton("RESTORE", STARSButtonHalfVertical) {
			// TODO: restore settings in effect when entered the Pref sub-menu
		}

		validSelection := sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets)
		if validSelection {
			if STARSSelectButton("SAVE", STARSButtonHalfVertical) {
				sp.PreferenceSets[sp.SelectedPreferenceSet] = sp.currentPreferenceSet
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
		for i, site := range sp.Facility.RadarSites {
			if site.Char == "" || site.Id == "" || site.Position == "" {
				STARSDisabledButton("", STARSButtonFull)
			} else if _, ok := database.Locate(site.Position); !ok {
				STARSDisabledButton("", STARSButtonFull)
			} else {
				label := " " + site.Char + " " + "\n" + site.Id
				if STARSToggleButton(label, &ps.RadarSiteSelected[i], STARSButtonFull) && ps.RadarSiteSelected[i] {
					// Deselect all the other options
					for j := range ps.RadarSiteSelected {
						if i != j {
							ps.RadarSiteSelected[j] = false
						}
					}
				}
			}
		}
		multi := ps.multiRadarMode()
		if STARSToggleButton("MULTI", &multi, STARSButtonFull) && multi {
			for i := range ps.RadarSiteSelected {
				ps.RadarSiteSelected[i] = false
			}
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
	for _, ap := range sp.Facility.Airports {
		if ap.TowerListIndex != 0 {
			server.AddAirportForWeather(ap.ICAOCode)
		}
	}

	ps := sp.currentPreferenceSet

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
	case CommandModeMapsMenu:
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
				text += server.CurrentTime().UTC().Format("1504/05 ")
			}
			if filter.All || filter.Altimeter {
				// Primary airport altimeter
				for _, ap := range sp.Facility.Airports {
					if ap.TowerListIndex == 1 {
						if metar := server.GetMETAR(ap.ICAOCode); metar != nil {
							text += formatMETAR(ap.ICAOCode, metar)
						}
					}
				}
			}
			td.AddText(text, pw, style)
			newline()
		}

		// ATIS and GI text always, apparently
		if ps.currentATIS != "" {
			pw = td.AddText(ps.currentATIS+" "+ps.giText[0], pw, style)
			newline()
		} else if ps.giText[0] != "" {
			pw = td.AddText(ps.giText[0], pw, style)
			newline()
		}
		for i := 1; i < len(ps.giText); i++ {
			if txt := ps.giText[i]; txt != "" {
				pw = td.AddText(txt, pw, style)
				newline()
			}
		}

		if filter.All || filter.Status || filter.Radar {
			if filter.All || filter.Status {
				if server.Connected() {
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
			for _, ap := range sp.Facility.Airports {
				if metar := server.GetMETAR(ap.ICAOCode); ap.IncludeInSSA && metar != nil {
					lines = append(lines, formatMETAR(ap.ICAOCode, metar))
				}
			}
			if len(lines) > 0 {
				sort.Strings(lines)
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
		var vfr []string
		// Find all untracked VFR aircraft
		for _, ac := range aircraft {
			if ac.Squawk == Squawk(0o1200) && ac.TrackingController == "" {
				vfr = append(vfr, ac.Callsign)
			}
		}
		sort.Strings(vfr)

		// Limit to the maximum the user set
		if len(vfr) > ps.VFRList.Lines {
			vfr = vfr[:ps.VFRList.Lines]
		}
		for i, callsign := range vfr {
			vfr[i] = fmt.Sprintf("%2d %-7s VFR", i+1, callsign)
		}

		text := "VFR LIST\n" + strings.Join(vfr, "\n")
		drawList(text, ps.VFRList.Position)
	}

	if ps.TABList.Visible {
		var dep []string
		// Untracked departures departing from one of our airports
		for _, ac := range aircraft {
			if fp := ac.FlightPlan; fp != nil && ac.TrackingController == "" {
				for _, ap := range sp.Facility.Airports {
					if fp.DepartureAirport == ap.ICAOCode {
						dep = append(dep, fmt.Sprintf("%-7s %s", ac.Callsign, ac.Squawk.String()))
						break
					}
				}
			}
		}
		sort.Strings(dep)
		// Limit to the user limit
		if len(dep) > ps.TABList.Lines {
			dep = dep[:ps.TABList.Lines]
		}
		for i, departure := range dep {
			dep[i] = fmt.Sprintf("%2d %s", i+1, departure)
		}
		text := "FLIGHT PLAN\n" + strings.Join(dep, "\n")
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

		for _, ap := range sp.Facility.Airports {
			if ap.TowerListIndex == i+1 {
				p, ok := database.Locate(ap.ICAOCode)
				if !ok {
					// what now? should validate in the UI anyway...
				}

				text := stripK(ap.ICAOCode) + " TOWER\n"
				m := make(map[float32]string)
				for _, ac := range aircraft {
					if !ac.OnGround() && ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == ap.ICAOCode {
						dist := nmdistance2ll(p, ac.Position())
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
			id := ""
			if pos := ctrl.GetPosition(); pos != nil {
				id = pos.SectorId
			} else if requirePosition {
				return ""
			}
			return fmt.Sprintf("%3s", id) + " " + ctrl.Frequency.String() + " " + ctrl.Callsign
		}

		// User first
		text := ""
		userCtrl := server.GetController(server.Callsign())
		if userCtrl != nil {
			text += format(userCtrl, false) + "\n"
		}

		ctrl := server.GetAllControllers()
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

	ps := sp.currentPreferenceSet
	font := sp.systemFont[ps.CharSize.PositionSymbols]

	now := server.CurrentTime()
	for _, ac := range aircraft {
		if ac.LostTrack(now) {
			continue
		}

		brightness := ps.Brightness.Positions
		if dt := sp.datablockType(ac); dt == PartialDatablock || dt == LimitedDatablock {
			brightness = ps.Brightness.LimitedDatablocks
		}

		pos := ac.Position()
		pw := transforms.WindowFromLatLongP(pos)
		// TODO: orient based on radar center if just one radar
		orientation := ac.Heading()
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
		if _, secondary, _ := sp.radarVisibility(ac.Position(), ac.Altitude()); secondary {
			// If it's just a secondary return, only draw the box outline.
			// TODO: is this 40nm, or secondary?
			ld.AddPolyline([2]float32{}, color, box[:])
		} else {
			// Draw a filled box
			trid.AddQuad(box[0], box[1], box[2], box[3], color)
		}

		if !ps.multiRadarMode() {
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
			if ctrl := server.GetController(ac.TrackingController); ctrl != nil {
				if pos := ctrl.GetPosition(); pos != nil {
					ch = pos.Scope
				}
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
	now := server.CurrentTime()
	font := sp.systemFont[sp.currentPreferenceSet.CharSize.Datablocks]

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

func (sp *STARSPane) datablockType(ac *Aircraft) DatablockType {
	state := sp.aircraft[ac]

	if _, queryUnassociated := sp.queryUnassociated.Get(ac); queryUnassociated {
		return FullDatablock
	}

	// TODO: when do we do a partial vs limited datablock?
	if ac.Squawk != ac.AssignedSquawk {
		return PartialDatablock
	}
	if !ac.IsAssociated() || state.partialDatablockIfAssociated {
		return PartialDatablock
	}

	return FullDatablock
}

func (sp *STARSPane) IsCAActive(ac *Aircraft) bool {
	if ac.Altitude() < int(sp.Facility.CA.Floor) {
		return false
	}

	for other := range sp.aircraft {
		if other == ac || other.Altitude() < int(sp.Facility.CA.Floor) {
			continue
		}
		if nmdistance2ll(ac.Position(), other.Position()) <= sp.Facility.CA.LateralMinimum &&
			abs(ac.Altitude()-other.Altitude()) <= int(sp.Facility.CA.VerticalMinimum+50 /*small slop for fp error*/) {
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
	// TODO: LA
	errblock = strings.Join(errs, "/") // want e.g., EM/LA if multiple things going on

	if ac.Mode == Standby {
		return
	}

	switch sp.datablockType(ac) {
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
		as := fmt.Sprintf("%03d  %02d", (ac.Altitude()+50)/100, (ac.Groundspeed()+5)/10)
		mainblock[0] = append(mainblock[0], as)
		mainblock[1] = append(mainblock[1], as)
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
			if ctrl := server.GetController(ac.InboundHandoffController); ctrl != nil {
				if pos := ctrl.GetPosition(); pos != nil {
					ho = pos.SectorId
				}
			}
		} else if ac.OutboundHandoffController != "" {
			if ctrl := server.GetController(ac.OutboundHandoffController); ctrl != nil {
				if pos := ctrl.GetPosition(); pos != nil {
					ho = pos.SectorId
				}
			}
		}

		// Altitude and speed: mainblock[0]
		alt := fmt.Sprintf("%03d", (ac.Altitude()+50)/100)
		if ac.LostTrack(server.CurrentTime()) {
			alt = "CST"
		}
		speed := fmt.Sprintf("%02d", (ac.Groundspeed()+5)/10)
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
		} else if strings.HasPrefix(actype, "S/") || strings.HasPrefix(actype, "J/") {
			actype = strings.TrimPrefix(actype, "S/")
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
	// This is not super efficient, but let's assume there aren't tons of ghost aircraft...
	/*
		for _, ghost := range sp.ghostAircraft {
			if ac == ghost {
				return cs.GhostDatablock
			}
		}
	*/

	// TODO: when do we use Brightness.LimitedDatablocks?
	ps := sp.currentPreferenceSet
	br := ps.Brightness.FullDatablocks

	if _, ok := sp.pointedOutAircraft.Get(ac); ok {
		// yellow for pointed out
		return br.ScaleRGB(STARSPointedOutAircraftColor)
	} else if ac == positionConfig.selectedAircraft {
		return br.ScaleRGB(STARSSelectedAircraftColor)
	} else if ac.TrackingController == server.Callsign() {
		// white if are tracking
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

	now := server.CurrentTime()
	realNow := time.Now() // for flashing rate...
	ps := sp.currentPreferenceSet
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
		pac := transforms.WindowFromLatLongP(ac.Position())
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
	ps := sp.currentPreferenceSet

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	now := server.CurrentTime()
	for _, ac := range aircraft {
		if ac.LostTrack(now) || !ac.HaveHeading() {
			continue
		}
		state := sp.aircraft[ac]
		if !(state.displayPTL || ps.PTLAll || (ps.PTLOwn && ac.TrackingController == server.Callsign())) {
			continue
		}

		// we want the vector length to be l=ps.PTLLength.
		// we have a heading vector (hx, hy) and scale factors (sx, sy) due to lat/long compression.
		// we want a t to scale the heading by to have that length.
		// solve (sx t hx)^2 + (hy t hy)^2 = l^2 ->
		// t = sqrt(l^2 / ((sx hx)^2 + (sy hy)^2)
		h := ac.HeadingVector()
		t := sqrt(sqr(ps.PTLLength) / (sqr(h[1]*database.NmPerLatitude) + sqr(h[0]*database.NmPerLongitude)))
		end := add2ll(ac.Position(), scale2ll(h, t))

		ld.AddLine(ac.Position(), end, color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(aircraft []*Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *CommandBuffer) {
	now := server.CurrentTime()
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	ps := sp.currentPreferenceSet
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
			pc := transforms.WindowFromLatLongP(ac.Position())
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
			rot := rotator2f(ac.Heading())
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
			pw := transforms.WindowFromLatLongP(ac.Position())
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

	ps := sp.currentPreferenceSet
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
		if rbl.p[0].ac != nil {
			p0 = rbl.p[0].ac.Position()
		}
		if rbl.p[1].ac != nil {
			p1 = rbl.p[1].ac.Position()
		}

		// Format the range-bearing line text for the two positions.
		hdg := headingp2ll(p0, p1, database.MagneticVariation)
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

	ps := sp.currentPreferenceSet
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

		pc := transforms.WindowFromLatLongP(ac.Position())
		radius := sp.Facility.CA.LateralMinimum / transforms.PixelDistanceNM()
		ld.AddCircle(pc, radius, 360 /* nsegs */)

		if time.Since(sp.lastCASoundTime) > 2*time.Second {
			globalConfig.AudioSettings.HandleEvent(AudioEventConflictAlert)
			sp.lastCASoundTime = time.Now()
		}
	}

	cb.LineWidth(1)
	ps := sp.currentPreferenceSet
	cb.SetRGB(ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
}

func (sp *STARSPane) consumeMouseEvents(ctx *PaneContext, transforms ScopeTransformations) {
	if ctx.mouse == nil {
		return
	}

	if activeSpinner == nil {
		UpdateScopePosition(ctx.mouse, MouseButtonSecondary, transforms,
			&sp.currentPreferenceSet.currentCenter, &sp.currentPreferenceSet.Range)
	}

	if ctx.mouse.Clicked[MouseButtonPrimary] {
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
	}

	if ctx.mouse.Clicked[MouseButtonTertiary] {
		if ac := sp.tryGetClickedAircraft(ctx.mouse.Pos, transforms); ac != nil {
			eventStream.Post(&SelectedAircraftEvent{ac: ac})
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// DCB menu on top

const STARSButtonWidth = 50
const STARSButtonHeight = 50

var (
	starsBarWindowPos imgui.Vec2
)

const (
	STARSButtonFull = iota
	STARSButtonHalfVertical
	STARSButtonHalfHorizontal
)

func starsButtonSize(flags int) imgui.Vec2 {
	switch flags {
	case STARSButtonFull:
		return imgui.Vec2{X: STARSButtonWidth, Y: STARSButtonHeight}
	case STARSButtonHalfVertical:
		return imgui.Vec2{X: STARSButtonWidth, Y: STARSButtonHeight / 2}
	case STARSButtonHalfHorizontal:
		return imgui.Vec2{X: STARSButtonWidth / 2, Y: STARSButtonHeight}
	default:
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

	imgui.PushFont(sp.uiFont.ifont)

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
	switch flags {
	case STARSButtonFull, STARSButtonHalfHorizontal:
		imgui.SameLine()

	case STARSButtonHalfVertical:
		if pos.Y == 0 {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X, Y: STARSButtonHeight / 2})
		} else {
			imgui.SetCursorPos(imgui.Vec2{X: pos.X + STARSButtonWidth, Y: 0})
		}

	default:
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
	result := imgui.ButtonV(text, starsButtonSize(flags))
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

func (sp *STARSPane) initializeSystemFonts() {
	for i, sz := range []int{14, 16, 18, 20, 22, 24} {
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

	ps := sp.currentPreferenceSet
	for _, ac := range server.GetAllAircraft() {
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

func (sp *STARSPane) noRadarDefined() bool {
	for _, site := range sp.Facility.RadarSites {
		if site.Valid() {
			return false
		}
	}
	return true
}

func (sp *STARSPane) radarVisibility(pos Point2LL, alt int) (primary, secondary bool, distance float32) {
	if sp.noRadarDefined() {
		return true, false, 0 // yolo
	}

	ps := sp.currentPreferenceSet
	multi := ps.multiRadarMode()
	distance = 1e30
	for i, sel := range ps.RadarSiteSelected {
		if !sel && !multi {
			continue
		}

		site := sp.Facility.RadarSites[i]
		if !site.Valid() {
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
	if sp.noRadarDefined() {
		ac, _ := FlattenMap(sp.aircraft)
		return ac
	}

	var aircraft []*Aircraft
	ps := sp.currentPreferenceSet
	multi := ps.multiRadarMode()

	for ac := range sp.aircraft {
		for i, sel := range ps.RadarSiteSelected {
			if !sel && !multi {
				continue
			}
			site := sp.Facility.RadarSites[i]
			if site.Valid() {
				if p, s, _ := site.CheckVisibility(ac.Position(), ac.Altitude()); p || s {
					aircraft = append(aircraft, ac)
				}
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) datablockVisible(ac *Aircraft) bool {
	af := sp.currentPreferenceSet.AltitudeFilters
	alt := ac.Altitude()
	if !ac.IsAssociated() {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) getLeaderLineDirection(ac *Aircraft) CardinalOrdinalDirection {
	if lld := sp.aircraft[ac].leaderLineDirection; lld != 0 {
		// these are offset +1 so that we can use default-initialized 0
		// for unset.
		return CardinalOrdinalDirection(lld - 1)
	}
	return sp.currentPreferenceSet.LeaderLineDirection
}

func (sp *STARSPane) getLeaderLineVector(ac *Aircraft) [2]float32 {
	dir := sp.getLeaderLineDirection(ac)
	angle := dir.Heading()
	v := [2]float32{sin(radians(angle)), cos(radians(angle))}
	ps := sp.currentPreferenceSet
	return scale2f(v, float32(10+10*ps.LeaderLineLength))
}

func (sp *STARSPane) isOverflight(ac *Aircraft) bool {
	if ac.FlightPlan == nil {
		return false
	}
	return FindIf(sp.Facility.Airports,
		func(ap STARSAirport) bool { return ap.ICAOCode == ac.FlightPlan.DepartureAirport }) == -1 &&
		FindIf(sp.Facility.Airports,
			func(ap STARSAirport) bool { return ap.ICAOCode == ac.FlightPlan.ArrivalAirport }) == -1
}

func (sp *STARSPane) tryGetClickedAircraft(mousePosition [2]float32, transforms ScopeTransformations) *Aircraft {
	var ac *Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft() {
		pw := transforms.WindowFromLatLongP(a.Position())
		dist := distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac
}

func (sp *STARSPane) radarSiteId() string {
	for i, sel := range sp.currentPreferenceSet.RadarSiteSelected {
		if sel {
			return sp.Facility.RadarSites[i].Id
		}
	}
	return "MULTI"
}
