// stars.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Main missing features:
// Altitude alerts

package panes

import (
	"fmt"
	"log/slog"
	gomath "math"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/davecgh/go-spew/spew"
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

	STARSDCBButtonColor         = renderer.RGB{0, .4, 0}
	STARSDCBActiveButtonColor   = renderer.RGB{0, .8, 0}
	STARSDCBTextColor           = renderer.RGB{1, 1, 1}
	STARSDCBTextSelectedColor   = renderer.RGB{1, 1, 0}
	STARSDCBDisabledButtonColor = renderer.RGB{.4, .4, .4}
	STARSDCBDisabledTextColor   = renderer.RGB{.8, .8, .8}
)

const NumSTARSPreferenceSets = 32

type STARSPane struct {
	CurrentPreferenceSet  STARSPreferenceSet
	SelectedPreferenceSet int
	PreferenceSets        []STARSPreferenceSet

	systemMaps map[int]*av.VideoMap

	weatherRadar WeatherRadar

	systemFont        [6]*renderer.Font
	systemOutlineFont [6]*renderer.Font
	dcbFont           [3]*renderer.Font // 0, 1, 2 only

	fusedTrackVertices [][2]float32

	events *sim.EventsSubscription

	// All of the aircraft in the world, each with additional information
	// carried along in an STARSAircraftState.
	Aircraft map[string]*STARSAircraftState

	AircraftToIndex map[string]int // for use in lists
	IndexToAircraft map[int]string // map is sort of wasteful since it's dense, but...

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

	queryUnassociated *util.TransientMap[string, interface{}]

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

	lastTrackUpdate        time.Time
	lastHistoryTrackUpdate time.Time
	discardTracks          bool

	drawApproachAirspace  bool
	drawDepartureAirspace bool

	// The start of a RBL--one click received, waiting for the second.
	wipRBL *STARSRangeBearingLine

	audioEffects map[AudioType]int // to handle from Platform.AddPCM()
}

type AudioType int

// The types of events we may play audio for; note that not all are
// currently used.
const (
	AudioConflictAlert = iota
	AudioEmergencySquawk
	AudioMinimumSafeAltitudeWarning
	AudioModeCIntruder
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
		"Inbound Handoff",
		"Command Error",
		"Handoff Accepted",
	}[ae]
}

type STARSRangeBearingLine struct {
	P [2]struct {
		// If callsign is given, use that aircraft's position;
		// otherwise we have a fixed position.
		Loc      math.Point2LL
		Callsign string
	}
}

func (rbl STARSRangeBearingLine) GetPoints(ctx *PaneContext, visibleAircraft []*av.Aircraft, sp *STARSPane) (p0, p1 math.Point2LL) {
	// Each line endpoint may be specified either by an aircraft's
	// position or by a fixed position. We'll start with the fixed
	// position and then override it if there's a valid *Aircraft.
	p0, p1 = rbl.P[0].Loc, rbl.P[1].Loc
	if ac := ctx.ControlClient.Aircraft[rbl.P[0].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.ControlClient.SimTime) && slices.Contains(visibleAircraft, ac) {
			p0 = state.TrackPosition()
		}
	}
	if ac := ctx.ControlClient.Aircraft[rbl.P[1].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.ControlClient.SimTime) && slices.Contains(visibleAircraft, ac) {
			p1 = state.TrackPosition()
		}
	}
	return
}

type CAAircraft struct {
	Callsigns    [2]string // sorted alphabetically
	Acknowledged bool
	SoundEnd     time.Time
}

type QuickLookPosition struct {
	Callsign string
	Id       string
	Plus     bool
}

func (sp *STARSPane) parseQuickLookPositions(ctx *PaneContext, s string) ([]QuickLookPosition, string, error) {
	var positions []QuickLookPosition

	// per 6-94, this is "fun"
	// - in general the string is a list of TCPs / sector ids.
	// - each may have a plus at the end
	// - if a single character id is entered, then we prepend the number for
	//   the current controller's sector id. in that case a space is required
	//   before the next one, if any
	ids := strings.Fields(s)
	for i, id := range ids {
		plus := len(id) > 1 && id[len(id)-1] == '+'
		id = strings.TrimRight(id, "+")

		control := sp.lookupControllerForId(ctx, id, "")
		if control == nil || control.FacilityIdentifier != "" || control.Callsign == ctx.ControlClient.Callsign {
			return positions, strings.Join(ids[i:], " "), ErrSTARSCommandFormat
		} else {
			positions = append(positions, QuickLookPosition{
				Callsign: control.Callsign,
				Id:       control.SectorId,
				Plus:     plus,
			})
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
	av.ConvergingRunways
	ApproachRegions [2]*av.ApproachRegion
	Airport         string
	Index           int
}

type STARSDatablockFieldColors struct {
	Start, End int
	Color      renderer.RGB
}

type STARSDatablockLine struct {
	Text   string
	Colors []STARSDatablockFieldColors
}

func (s *STARSDatablockLine) RightJustify(n int) {
	if n > len(s.Text) {
		delta := n - len(s.Text)
		s.Text = fmt.Sprintf("%*c", delta, ' ') + s.Text
		// Keep the formatting aligned.
		for i := range s.Colors {
			s.Colors[i].Start += delta
			s.Colors[i].End += delta
		}
	}
}

type STARSDatablock struct {
	Lines [4]STARSDatablockLine
}

func (s *STARSDatablock) RightJustify(n int) {
	for i := range s.Lines {
		s.Lines[i].RightJustify(n)
	}
}

func (s *STARSDatablock) Duplicate() STARSDatablock {
	var sd STARSDatablock
	for i := range s.Lines {
		sd.Lines[i].Text = s.Lines[i].Text
		sd.Lines[i].Colors = util.DuplicateSlice(s.Lines[i].Colors)
	}
	return sd
}

func (s *STARSDatablock) BoundText(font *renderer.Font) (int, int) {
	text := ""
	for i, l := range s.Lines {
		text += l.Text
		if i+1 < len(s.Lines) {
			text += "\n"
		}
	}
	return font.BoundText(text, 0)
}

func (s *STARSDatablock) DrawText(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, baseColor renderer.RGB,
	brightness STARSBrightness) {
	style := renderer.TextStyle{
		Font:        font,
		Color:       brightness.ScaleRGB(baseColor),
		LineSpacing: 0}

	for _, line := range s.Lines {
		haveFormatting := len(line.Colors) > 0
		if haveFormatting {
			p0 := pt // save starting point

			// Gather spans of characters that have the same color
			spanColor := baseColor
			start, end := 0, 0

			flush := func(newColor renderer.RGB) {
				if end > start {
					style := renderer.TextStyle{
						Font:        font,
						Color:       brightness.ScaleRGB(spanColor),
						LineSpacing: 0}
					pt = td.AddText(line.Text[start:end], pt, style)
					start = end
				}
				spanColor = newColor
			}

			for ; end < len(line.Text); end++ {
				if line.Text[end] == ' ' {
					// let spaces ride regardless of style
					continue
				}
				// Does this character have a new color?
				chColor := baseColor
				for _, format := range line.Colors {
					if end >= format.Start && end < format.End {
						chColor = format.Color
						break
					}
				}
				if !spanColor.Equals(chColor) {
					flush(chColor)
				}
			}
			flush(spanColor)

			// newline from start so we maintain aligned columns.
			pt = td.AddText("\n", p0, style)
		} else {
			pt = td.AddText(line.Text+"\n", pt, style)
		}
	}
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

type CommandMode int

const (
	CommandModeNone = iota
	CommandModeInitiateControl
	CommandModeTerminateControl
	CommandModeHandOff
	CommandModeVFRPlan
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
	DCBMenuTPA
)

// DCBSpinner is an interface used to manage the various spinner types in
// the DCB menu. Since the various spinners generally have unique
// requirements and expectations about keyboard input, having this
// interface allows collecting all of that in the various implementations
// of the interface.
type DCBSpinner interface {
	// Label returns the text that should be shown in the DCB button.
	Label() string
	// Equal returns true if the provided spinner controls the same value
	// as this spinner.
	Equals(other DCBSpinner) bool
	// MouseWheel is called when the spinner is active and there is mouse
	// wheel input; implementations should update the underlying value
	// accordingly.
	MouseWheel(delta int)
	// KeyboardInput is called if the spinner is active and the user enters
	// text and presses enter; implementations should update the underlying
	// value accordingly.
	KeyboardInput(text string) error
}

type STARSAircraftState struct {
	// Independently of the track history, we store the most recent track
	// from the sensor as well as the previous one. This gives us the
	// freshest possible information for things like calculating headings,
	// rates of altitude change, etc.
	track         av.RadarTrack
	previousTrack av.RadarTrack

	// Radar track history is maintained with a ring buffer where
	// historyTracksIndex is the index of the next track to be written.
	// (Thus, historyTracksIndex==0 implies that there are no tracks.)
	// Changing to/from FUSED mode causes tracksIndex to be reset, thus
	// discarding previous tracks.
	historyTracks      [10]av.RadarTrack
	historyTracksIndex int

	DatablockType            DatablockType
	FullLDBEndTime           time.Time // If the LDB displays the groundspeed. When to stop
	DisplayRequestedAltitude *bool     // nil if unspecified

	IsSelected bool // middle click

	// Only drawn if non-zero
	JRingRadius              float32
	ConeLength               float32
	DisplayTPASize           *bool // unspecified->system default if nil
	DisplayATPAMonitor       *bool // unspecified->system default if nil
	DisplayATPAWarnAlert     *bool // unspecified->system default if nil
	IntrailDistance          float32
	ATPAStatus               ATPAStatus
	MinimumMIT               float32
	ATPALeadAircraftCallsign string
	POFlashingEndTime        time.Time

	// These are only set if a leader line direction was specified for this
	// aircraft individually:
	LeaderLineDirection       *math.CardinalOrdinalDirection
	GlobalLeaderLineDirection *math.CardinalOrdinalDirection
	UseGlobalLeaderLine       bool

	Ghost struct {
		PartialDatablock bool
		State            GhostState
	}

	displayPilotAltitude bool
	pilotAltitude        int

	DisplayReportedBeacon bool // note: only for unassociated
	DisplayPTL            bool
	DisableCAWarnings     bool

	MSAW             bool // minimum safe altitude warning
	DisableMSAW      bool
	InhibitMSAW      bool // only applies if in an alert. clear when alert is over?
	MSAWAcknowledged bool
	MSAWSoundEnd     time.Time

	FirstSeen           time.Time
	FirstRadarTrack     time.Time
	HaveEnteredAirspace bool

	CWTCategory string // cache this for performance

	IdentStart, IdentEnd    time.Time
	OutboundHandoffAccepted bool
	OutboundHandoffFlashEnd time.Time

	RDIndicatorEnd time.Time

	// This is a little messy: we maintain maps from callsign->sector id
	// for pointouts that track the global state of them. Here we track
	// just inbound pointouts to the current controller so that the first
	// click acks a point out but leaves it yellow and a second clears it
	// entirely.
	PointedOut bool
	ForceQL    bool
}

type ATPAStatus int

const (
	ATPAStatusUnset = iota
	ATPAStatusMonitor
	ATPAStatusWarning
	ATPAStatusAlert
)

type GhostState int

const (
	GhostStateRegular = iota
	GhostStateSuppressed
	GhostStateForced
)

func (s *STARSAircraftState) TrackAltitude() int {
	return s.track.Altitude
}

func (s *STARSAircraftState) TrackDeltaAltitude() int {
	if s.previousTrack.Position.IsZero() {
		// No previous track
		return 0
	}
	return s.track.Altitude - s.previousTrack.Altitude
}

func (s *STARSAircraftState) TrackPosition() math.Point2LL {
	return s.track.Position
}

func (s *STARSAircraftState) TrackGroundspeed() int {
	return s.track.Groundspeed
}

func (s *STARSAircraftState) HaveHeading() bool {
	return !s.previousTrack.Position.IsZero()
}

// Note that the vector returned by HeadingVector() is along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (s *STARSAircraftState) HeadingVector(nmPerLongitude, magneticVariation float32) math.Point2LL {
	if !s.HaveHeading() {
		return math.Point2LL{}
	}

	p0 := math.LL2NM(s.track.Position, nmPerLongitude)
	p1 := math.LL2NM(s.previousTrack.Position, nmPerLongitude)
	v := math.Sub2LL(p0, p1)
	v = math.Normalize2f(v)
	// v's length should be groundspeed / 60 nm.
	v = math.Scale2f(v, float32(s.TrackGroundspeed())/60) // hours to minutes
	return math.NM2LL(v, nmPerLongitude)
}

func (s *STARSAircraftState) TrackHeading(nmPerLongitude float32) float32 {
	if !s.HaveHeading() {
		return 0
	}
	return math.Heading2LL(s.previousTrack.Position, s.track.Position, nmPerLongitude, 0)
}

func (s *STARSAircraftState) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !s.track.Position.IsZero() && now.Sub(s.track.Time) > 30*time.Second
}

func (s *STARSAircraftState) Ident(now time.Time) bool {
	return !s.IdentStart.IsZero() && s.IdentStart.Before(now) && s.IdentEnd.After(now)
}

///////////////////////////////////////////////////////////////////////////
// STARSPreferenceSet

type STARSPreferenceSet struct {
	Name string

	DisplayDCB  bool
	DCBPosition int

	Center math.Point2LL
	Range  float32

	CurrentCenter math.Point2LL
	OffCenter     bool

	RangeRingsCenter math.Point2LL
	RangeRingRadius  int

	// TODO? cursor speed

	CurrentATIS string
	GIText      [9]string

	RadarTrackHistory int
	// 4-94: 0.5s increments via trackball but 0.1s increments allowed if
	// keyboard input.
	RadarTrackHistoryRate float32

	DisplayWeatherLevel [6]bool

	// If empty, then then MULTI or FUSED mode, depending on
	// FusedRadarMode.  The custom JSON name is so we don't get errors
	// parsing old configs, which stored this as an array...
	RadarSiteSelected string `json:"RadarSiteSelectedName"`
	FusedRadarMode    bool

	AudioEffectEnabled []bool

	// For tracked by the user
	LeaderLineDirection math.CardinalOrdinalDirection
	LeaderLineLength    int // 0-7
	// For tracked by other controllers
	ControllerLeaderLineDirections map[string]math.CardinalOrdinalDirection
	// If not specified in ControllerLeaderLineDirections...
	OtherControllerLeaderLineDirection *math.CardinalOrdinalDirection
	// Only set if specified by the user (and not used currently...)
	UnassociatedLeaderLineDirection *math.CardinalOrdinalDirection

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

	// DisplayUncorrelatedTargets bool // NOT USED

	DisableCAWarnings bool
	DisableMSAW       bool

	OverflightFullDatablocks bool
	AutomaticFDBOffset       bool

	DisplayTPASize               bool
	DisplayATPAInTrailDist       bool `json:"DisplayATPAIntrailDist"`
	DisplayATPAWarningAlertCones bool
	DisplayATPAMonitorCones      bool

	DisplayVideoMap  [sim.NumSTARSMaps]bool
	SystemMapVisible map[int]interface{}

	PTLLength      float32
	PTLOwn, PTLAll bool

	DisplayRequestedAltitude bool

	DwellMode DwellMode

	TopDownMode     bool
	GroundRangeMode bool

	Bookmarks [10]struct {
		Center      math.Point2LL
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
	VideoMapsGroupCurrent
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

func (sp *STARSPane) MakePreferenceSet(name string, ss *sim.State) STARSPreferenceSet {
	var ps STARSPreferenceSet

	ps.Name = name

	ps.DisplayDCB = true
	ps.DCBPosition = DCBPositionTop

	if ss != nil {
		ps.Center = ss.GetInitialCenter()
		ps.Range = ss.GetInitialRange()
	} else {
		ps.Center = math.Point2LL{73.475, 40.395} // JFK-ish
		ps.Range = 50
	}

	ps.CurrentCenter = ps.Center

	ps.RangeRingsCenter = ps.Center
	ps.RangeRingRadius = 5

	ps.RadarTrackHistory = 5
	ps.RadarTrackHistoryRate = 4.5

	ps.SystemMapVisible = make(map[int]interface{})

	ps.FusedRadarMode = true
	ps.LeaderLineDirection = math.North
	ps.LeaderLineLength = 1

	ps.AltitudeFilters.Unassociated = [2]int{100, 60000}
	ps.AltitudeFilters.Associated = [2]int{100, 60000}

	//ps.DisplayUncorrelatedTargets = true

	ps.DisplayTPASize = true
	ps.DisplayATPAWarningAlertCones = true

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

	for i := range ps.DisplayWeatherLevel {
		ps.DisplayWeatherLevel[i] = true
	}

	ps.CharSize.DCB = 1
	ps.CharSize.Datablocks = 1
	ps.CharSize.Lists = 1
	ps.CharSize.Tools = 1
	ps.CharSize.PositionSymbols = 0

	ps.PreviewAreaPosition = [2]float32{.05, .75}

	ps.SSAList.Position = [2]float32{.05, .9}
	ps.SSAList.Visible = true
	ps.SSAList.Filter.All = true

	ps.TABList.Position = [2]float32{.05, .65}
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

	ps.SignOnList.Position = [2]float32{.8, .9}
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
	dupe.SelectedBeaconCodes = util.DuplicateSlice(ps.SelectedBeaconCodes)
	dupe.CRDA.RunwayPairState = util.DuplicateSlice(ps.CRDA.RunwayPairState)
	dupe.SystemMapVisible = util.DuplicateMap(ps.SystemMapVisible)
	return dupe
}

func (ps *STARSPreferenceSet) Activate(sp *STARSPane) {
	// It should only take integer values but it's a float32 and we
	// previously didn't enforce this...
	ps.Range = float32(int(ps.Range))

	if ps.PTLAll { // both can't be set; we didn't enforce this previously...
		ps.PTLOwn = false
	}

	if ps.RadarTrackHistoryRate == 0 {
		ps.RadarTrackHistoryRate = 4.5 // upgrade from old
	}

	// Brightness goes in steps of 5 (similarly not enforced previously...)
	remapBrightness := func(b *STARSBrightness) {
		*b = (*b + 2) / 5 * 5
		*b = math.Clamp(*b, 0, 100)
	}
	remapBrightness(&ps.Brightness.DCB)
	remapBrightness(&ps.Brightness.BackgroundContrast)
	remapBrightness(&ps.Brightness.VideoGroupA)
	remapBrightness(&ps.Brightness.VideoGroupB)
	remapBrightness(&ps.Brightness.FullDatablocks)
	remapBrightness(&ps.Brightness.Lists)
	remapBrightness(&ps.Brightness.Positions)
	remapBrightness(&ps.Brightness.LimitedDatablocks)
	remapBrightness(&ps.Brightness.OtherTracks)
	remapBrightness(&ps.Brightness.Lines)
	remapBrightness(&ps.Brightness.RangeRings)
	remapBrightness(&ps.Brightness.Compass)
	remapBrightness(&ps.Brightness.BeaconSymbols)
	remapBrightness(&ps.Brightness.PrimarySymbols)
	remapBrightness(&ps.Brightness.History)
	remapBrightness(&ps.Brightness.Weather)
	remapBrightness(&ps.Brightness.WxContrast)

	for len(ps.AudioEffectEnabled) < AudioNumTypes {
		ps.AudioEffectEnabled = append(ps.AudioEffectEnabled, true)
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

// In CRC, whenever a tracked aircraft is slewed, it displays the callsign, squawk, and assigned squawk
func slewAircaft(ac *av.Aircraft) string {
	return fmt.Sprintf("%v %v %v", ac.Callsign, ac.Squawk, ac.AssignedSquawk)
}

// See STARS Operators Manual 5-184...
func (sp *STARSPane) flightPlanSTARS(controllers map[string]*av.Controller, ac *av.Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalFlight
	}

	fmtTime := func(t time.Time) string {
		return t.UTC().Format("1504")
	}

	// Common stuff
	owner := ""
	if ctrl, ok := controllers[ac.TrackingController]; ok {
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

type STARSCommandStatus struct {
	clear  bool
	output string
	err    error
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

func NewSTARSPane(ss sim.State) *STARSPane {
	sp := &STARSPane{
		SelectedPreferenceSet: -1,
	}
	sp.CurrentPreferenceSet = sp.MakePreferenceSet("", &ss)
	return sp
}

func (sp *STARSPane) Name() string { return "STARS" }

func (sp *STARSPane) Activate(ss *sim.State, r renderer.Renderer, p platform.Platform,
	eventStream *sim.EventStream, lg *log.Logger) {
	if sp.CurrentPreferenceSet.Range == 0 || sp.CurrentPreferenceSet.Center.IsZero() {
		// First launch after switching over to serializing the CurrentPreferenceSet...
		sp.CurrentPreferenceSet = sp.MakePreferenceSet("", ss)
	}
	sp.CurrentPreferenceSet.Activate(sp)

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
		sp.queryUnassociated = util.NewTransientMap[string, interface{}]()
	}

	sp.initializeFonts()
	sp.initializeAudio(p, lg)

	if ss != nil {
		sp.systemMaps = sp.makeSystemMaps(*ss)
	}

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

	imgui.Checkbox("Enable Sound Effects", &config.AudioEnabled)
	uiStartDisable(!config.AudioEnabled)
	// Not all of the ones available in the engine are used, so only offer these up:
	for _, i := range []AudioType{AudioConflictAlert, AudioInboundHandoff,
		AudioHandoffAccepted, AudioCommandError} {
		imgui.Text("  ")
		imgui.SameLine()
		if imgui.Checkbox(AudioType(i).String(), &ps.AudioEffectEnabled[i]) && ps.AudioEffectEnabled[i] {
			n := util.Select(i == AudioConflictAlert, 5, 1)
			for j := 0; j < n; j++ {
				sp.playOnce(p, i)
			}
		}
	}
	uiEndDisable(!config.AudioEnabled)
}

func (sp *STARSPane) CanTakeKeyboardFocus() bool { return true }

func (sp *STARSPane) processEvents(ctx *PaneContext) {
	// First handle changes in world.Aircraft
	for callsign, ac := range ctx.ControlClient.Aircraft {
		if _, ok := sp.Aircraft[callsign]; !ok {
			// First we've seen it; create the *STARSAircraftState for it
			sa := &STARSAircraftState{}
			if ac.TrackingController == ctx.ControlClient.Callsign || ac.ControllingController == ctx.ControlClient.Callsign {
				sa.DatablockType = FullDatablock
			}
			sa.GlobalLeaderLineDirection = ac.GlobalLeaderLineDirection
			sa.UseGlobalLeaderLine = sa.GlobalLeaderLineDirection != nil
			sa.FirstSeen = ctx.ControlClient.SimTime
			sa.CWTCategory = getCwtCategory(ctx, ac)

			sp.Aircraft[callsign] = sa
		}

		if ok, _ := av.SquawkIsSPC(ac.Squawk); ok {
			if _, ok := sp.HavePlayedSPCAlertSound[ac.Callsign]; !ok {
				sp.HavePlayedSPCAlertSound[ac.Callsign] = nil
			}
		}
	}

	// See if any aircraft we have state for have been removed
	for callsign := range sp.Aircraft {
		if _, ok := ctx.ControlClient.Aircraft[callsign]; !ok {
			delete(sp.Aircraft, callsign)
		}
	}

	// Filter out any removed aircraft from the CA list
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		_, a := ctx.ControlClient.Aircraft[ca.Callsigns[0]]
		_, b := ctx.ControlClient.Aircraft[ca.Callsigns[1]]
		return a && b
	})

	// In the following, note that we may see events that refer to aircraft
	// that no longer exist (e.g., due to deletion). Thus, this is a case
	// where we have to check our accesses to the sp.Aircraft map and not
	// crash if we don't find an entry for an aircraft we have an event
	// for.
	for _, event := range sp.events.Get() {
		switch event.Type {
		case sim.PointOutEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil {
					sp.InboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.InboundPointOuts[event.Callsign] = ""
				}
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}
			if event.FromController == ctx.ControlClient.Callsign {
				if ctrl := ctx.ControlClient.Controllers[event.ToController]; ctrl != nil {
					sp.OutboundPointOuts[event.Callsign] = ctrl.SectorId
				} else {
					sp.OutboundPointOuts[event.Callsign] = ""
				}
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}
		case sim.AcknowledgedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.OutboundPointOuts, event.Callsign)
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.ToController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
					if state, ok := sp.Aircraft[event.Callsign]; ok {
						state.PointedOut = true
						state.POFlashingEndTime = time.Now().Add(5 * time.Second)
					}
				}
			}
		case sim.RejectedPointOutEvent:
			if id, ok := sp.OutboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.FromController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.OutboundPointOuts, event.Callsign)
					sp.RejectedPointOuts[event.Callsign] = nil
				}
			}
			if id, ok := sp.InboundPointOuts[event.Callsign]; ok {
				if ctrl, ok := ctx.ControlClient.Controllers[event.ToController]; ok && ctrl != nil && ctrl.SectorId == id {
					delete(sp.InboundPointOuts, event.Callsign)
				}
			}
		case sim.InitiatedTrackEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					state.DatablockType = FullDatablock
				}
			}
		case sim.OfferedHandoffEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				sp.playOnce(ctx.Platform, AudioInboundHandoff)
			}
		case sim.AcceptedHandoffEvent:
			if event.FromController == ctx.ControlClient.Callsign && event.ToController != ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					state.OutboundHandoffAccepted = true
					state.OutboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
				}
			}
		case sim.AcceptedRedirectedHandoffEvent:
			if event.FromController == ctx.ControlClient.Callsign && event.ToController != ctx.ControlClient.Callsign {
				if state, ok := sp.Aircraft[event.Callsign]; ok {
					sp.playOnce(ctx.Platform, AudioHandoffAccepted)
					state.OutboundHandoffAccepted = true
					state.OutboundHandoffFlashEnd = time.Now().Add(10 * time.Second)
					state.RDIndicatorEnd = time.Now().Add(30 * time.Second)
					state.DatablockType = FullDatablock
				}
			}
		case sim.IdentEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.IdentStart = time.Now().Add(time.Duration(2+rand.Intn(3)) * time.Second)
				state.IdentEnd = state.IdentStart.Add(10 * time.Second)
			}
		case sim.SetGlobalLeaderLineEvent:
			if state, ok := sp.Aircraft[event.Callsign]; ok {
				state.GlobalLeaderLineDirection = event.LeaderLineDirection
				state.UseGlobalLeaderLine = state.GlobalLeaderLineDirection != nil
			}
		}
	}
}

func (sp *STARSPane) updateMSAWs(ctx *PaneContext) {
	// See if there are any MVA issues
	mvas := av.DB.MVAs[ctx.ControlClient.TRACON]
	for callsign, ac := range ctx.ControlClient.Aircraft {
		state := sp.Aircraft[callsign]
		if !ac.MVAsApply() {
			state.MSAW = false
			continue
		}

		warn := slices.ContainsFunc(mvas, func(mva av.MVA) bool {
			return state.track.Altitude < mva.MinimumLimit && mva.Inside(state.track.Position)
		})

		if !warn && state.InhibitMSAW {
			// The warning has cleared, so the inhibit is disabled (p.7-25)
			state.InhibitMSAW = false
		}
		if warn && !state.MSAW {
			// It's a new alert
			state.MSAWAcknowledged = false
			state.MSAWSoundEnd = time.Now().Add(5 * time.Second)
		}
		state.MSAW = warn
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
			*b = STARSBrightness(math.Min(*b*2, 100))
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
	if from < 17 {
		// Added DisplayWeatherLevel
		for i := range sp.CurrentPreferenceSet.DisplayWeatherLevel {
			sp.CurrentPreferenceSet.DisplayWeatherLevel[i] = true
		}
		for i := range sp.PreferenceSets {
			for j := range sp.PreferenceSets[i].DisplayWeatherLevel {
				sp.PreferenceSets[i].DisplayWeatherLevel[j] = true
			}
		}
	}
	if from < 18 {
		// ATPA; set defaults
		sp.CurrentPreferenceSet.DisplayATPAInTrailDist = true
		sp.CurrentPreferenceSet.DisplayATPAWarningAlertCones = true
		for i := range sp.PreferenceSets {
			sp.PreferenceSets[i].DisplayATPAInTrailDist = true
			sp.PreferenceSets[i].DisplayATPAWarningAlertCones = true
		}
	}
	if from < 21 {
		// System list offsets changed from updated handling of
		// transformation matrices with and without the DCB visible.
		update := func(ps *STARSPreferenceSet) {
			ps.CharSize.DCB = math.Max(0, ps.CharSize.DCB-1)
			ps.CharSize.Datablocks = math.Max(0, ps.CharSize.Datablocks-1)
			ps.CharSize.Lists = math.Max(0, ps.CharSize.Lists-1)
			ps.CharSize.Tools = math.Max(0, ps.CharSize.Tools-1)
			ps.CharSize.PositionSymbols = math.Max(0, ps.CharSize.PositionSymbols-1)

			if ps.DisplayDCB && ps.DCBPosition == DCBPositionTop {
				shift := func(y *float32) {
					*y = math.Max(0, *y-.05)
				}
				shift(&ps.SSAList.Position[1])
				shift(&ps.VFRList.Position[1])
				shift(&ps.TABList.Position[1])
				shift(&ps.AlertList.Position[1])
				shift(&ps.CoastList.Position[1])
				shift(&ps.SignOnList.Position[1])
				shift(&ps.VideoMapsList.Position[1])
				shift(&ps.CRDAStatusList.Position[1])
				for i := range ps.TowerLists {
					shift(&ps.TowerLists[i].Position[1])
				}
			}
		}
		update(&sp.CurrentPreferenceSet)
		for i := range sp.PreferenceSets {
			update(&sp.PreferenceSets[i])
		}
	}
	if from < 23 {
		// This should have been in the from < 21 case...
		update := func(ps *STARSPreferenceSet) {
			if ps.PreviewAreaPosition[0] == .05 && ps.PreviewAreaPosition[1] == .8 {
				ps.PreviewAreaPosition = [2]float32{.05, .75}
			}
		}
		update(&sp.CurrentPreferenceSet)
		for i := range sp.PreferenceSets {
			update(&sp.PreferenceSets[i])
		}
	}
}

func (sp *STARSPane) Draw(ctx *PaneContext, cb *renderer.CommandBuffer) {
	sp.processEvents(ctx)
	sp.updateRadarTracks(ctx)

	ps := sp.CurrentPreferenceSet

	// Clear to background color
	cb.ClearRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))

	sp.processKeyboardInput(ctx)

	transforms := GetScopeTransformations(ctx.PaneExtent, ctx.ControlClient.MagneticVariation, ctx.ControlClient.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range), 0)

	dpiScale := ctx.Platform.DPIScale()
	paneExtent := ctx.PaneExtent
	if ps.DisplayDCB {
		paneExtent = sp.DrawDCB(ctx, transforms, cb)

		// Update scissor for what's left and to protect the DCB (even
		// though this is apparently unrealistic, at least as far as radar
		// tracks go...)
		cb.SetScissorBounds(paneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

		if ctx.Mouse != nil {
			// The mouse position is provided in Pane coordinates, so that needs to be updated unless
			// the DCB is at the top, in which case it's unchanged.
			ms := *ctx.Mouse
			ctx.Mouse = &ms
			ctx.Mouse.Pos[0] += ctx.PaneExtent.P0[0] - paneExtent.P0[0]
			ctx.Mouse.Pos[1] += ctx.PaneExtent.P0[1] - paneExtent.P0[1]
		}
	}

	weatherBrightness := float32(ps.Brightness.Weather) / float32(100)
	weatherContrast := float32(ps.Brightness.WxContrast) / float32(100)
	sp.weatherRadar.Draw(ctx, weatherBrightness, weatherContrast, ps.DisplayWeatherLevel,
		transforms, cb)

	if ps.Brightness.RangeRings > 0 {
		color := ps.Brightness.RangeRings.ScaleRGB(STARSRangeRingColor)
		cb.LineWidth(1, dpiScale)
		DrawRangeRings(ctx, ps.RangeRingsCenter, float32(ps.RangeRingRadius), color, transforms, cb)
	}

	transforms.LoadWindowViewingMatrices(cb)

	// Maps
	cb.LineWidth(1, dpiScale)
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
		cb.LineWidth(1, dpiScale)
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
	sp.drawMouseCursor(ctx, paneExtent, transforms, cb)

	// Play the CA sound if any CAs or MSAWs are unacknowledged
	playAlertSound := !ps.DisableCAWarnings && slices.ContainsFunc(sp.CAAircraft,
		func(ca CAAircraft) bool {
			return !ca.Acknowledged && !sp.Aircraft[ca.Callsigns[0]].DisableCAWarnings &&
				!sp.Aircraft[ca.Callsigns[1]].DisableCAWarnings && ctx.Now.Before(ca.SoundEnd)
		})
	if !ps.DisableMSAW {
		for _, ac := range aircraft {
			state := sp.Aircraft[ac.Callsign]
			if state.MSAW && !state.MSAWAcknowledged && !state.InhibitMSAW && !state.DisableMSAW &&
				ctx.Now.Before(state.MSAWSoundEnd) {
				playAlertSound = true
				break
			}
		}
	}
	if playAlertSound {
		sp.startPlayContinuous(ctx.Platform, AudioConflictAlert)
	} else {
		sp.stopPlayContinuous(ctx.Platform, AudioConflictAlert)
	}

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

func (sp *STARSPane) updateRadarTracks(ctx *PaneContext) {
	// FIXME: all aircraft radar tracks are updated at the same time.
	now := ctx.ControlClient.SimTime
	if sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused {
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
		ac, ok := ctx.ControlClient.Aircraft[callsign]
		if !ok {
			ctx.Lg.Errorf("%s: not found in Aircraft?", callsign)
			continue
		}

		state.previousTrack = state.track
		state.track = av.RadarTrack{
			Position:    ac.Position(),
			Altitude:    int(ac.Altitude()),
			Groundspeed: int(ac.Nav.FlightState.GS),
			Time:        now,
		}
	}

	// Update low altitude alerts now that we have updated tracks
	sp.updateMSAWs(ctx)

	// History tracks are updated after a radar track update, only if
	// H_RATE seconds have elapsed (4-94).
	ps := &sp.CurrentPreferenceSet
	if now.Sub(sp.lastHistoryTrackUpdate).Seconds() >= float64(ps.RadarTrackHistoryRate) {
		sp.lastHistoryTrackUpdate = now
		for _, state := range sp.Aircraft {
			idx := state.historyTracksIndex % len(state.historyTracks)
			state.historyTracks[idx] = state.track
			state.historyTracksIndex++
		}
	}

	aircraft := sp.visibleAircraft(ctx)
	sort.Slice(aircraft, func(i, j int) bool {
		return aircraft[i].Callsign < aircraft[j].Callsign
	})

	sp.updateCAAircraft(ctx, aircraft)
	sp.updateInTrailDistance(ctx, aircraft)
}

func (sp *STARSPane) processKeyboardInput(ctx *PaneContext) {
	if !ctx.HaveFocus || ctx.Keyboard == nil {
		return
	}

	input := strings.ToUpper(ctx.Keyboard.Input)
	if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "" && len(input) > 0 {
		sp.multiFuncPrefix = string(input[0])
		input = input[1:]
	}
	sp.previewAreaInput += strings.Replace(input, "`", STARSTriangleCharacter, -1)

	ps := &sp.CurrentPreferenceSet

	if ctx.Keyboard.IsPressed(platform.KeyControl) && len(input) == 1 && unicode.IsDigit(rune(input[0])) {
		idx := byte(input[0]) - '0'
		// This test should be redundant given the IsDigit check, but just to be safe...
		if int(idx) < len(ps.Bookmarks) {
			if ctx.Keyboard.IsPressed(platform.KeyAlt) {
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

	for key := range ctx.Keyboard.Pressed {
		switch key {
		case platform.KeyBackspace:
			if len(sp.previewAreaInput) > 0 {
				// We need to be careful to deal with UTF8 for the triangle...
				r := []rune(sp.previewAreaInput)
				sp.previewAreaInput = string(r[:len(r)-1])
			} else {
				sp.multiFuncPrefix = ""
			}
		case platform.KeyEnd:
			sp.resetInputState()
			sp.commandMode = CommandModeMin
		case platform.KeyEnter:
			if status := sp.executeSTARSCommand(sp.previewAreaInput, ctx); status.err != nil {
				sp.displayError(status.err, ctx)
			} else {
				if status.clear {
					sp.resetInputState()
				}
				sp.previewAreaOutput = status.output
			}
		case platform.KeyEscape:
			sp.resetInputState()
			sp.activeDCBMenu = DCBMenuMain
			// Also disable any mouse capture from spinners, just in case
			// the user is mashing escape to get out of one.
			sp.disableMenuSpinner(ctx)
			sp.wipRBL = nil
		case platform.KeyF1:
			if ctx.Keyboard.IsPressed(platform.KeyControl) {
				// Recenter
				ps.Center = ctx.ControlClient.GetInitialCenter()
				ps.CurrentCenter = ps.Center
			}
		case platform.KeyF2:
			if ctx.Keyboard.IsPressed(platform.KeyControl) {
				if ps.DisplayDCB {
					sp.disableMenuSpinner(ctx)
					sp.activeDCBMenu = DCBMenuMaps
				}
				sp.resetInputState()
				sp.commandMode = CommandModeMaps
			}
		case platform.KeyF3:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activeDCBMenu = DCBMenuBrite
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeInitiateControl
			}
		case platform.KeyF4:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.activeDCBMenu = DCBMenuMain
				sp.activateMenuSpinner(MakeLeaderLineLengthSpinner(&ps.LeaderLineLength))
				sp.resetInputState()
				sp.commandMode = CommandModeLDR
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeTerminateControl
			}
		case platform.KeyF5:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activeDCBMenu = DCBMenuCharSize
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeHandOff
			}
		case platform.KeyF6:
			sp.resetInputState()
			sp.commandMode = CommandModeFlightData
		case platform.KeyF7:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
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
		case platform.KeyF8:
			if ctx.Keyboard.IsPressed(platform.KeyControl) {
				sp.disableMenuSpinner(ctx)
				ps.DisplayDCB = !ps.DisplayDCB
			}
		case platform.KeyF9:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activateMenuSpinner(MakeRangeRingRadiusSpinner(&ps.RangeRingRadius))
				sp.resetInputState()
				sp.commandMode = CommandModeRangeRings
			} else {
				sp.resetInputState()
				sp.commandMode = CommandModeVFRPlan
			}
		case platform.KeyF10:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.disableMenuSpinner(ctx)
				sp.activateMenuSpinner(MakeRadarRangeSpinner(&ps.Range))
				sp.resetInputState()
				sp.commandMode = CommandModeRange
			}
		case platform.KeyF11:
			if ctx.Keyboard.IsPressed(platform.KeyControl) && ps.DisplayDCB {
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
	ctx.Platform.EndCaptureMouse()
	sp.commandMode = CommandModeNone
}

func (sp *STARSPane) activateMenuSpinner(spinner DCBSpinner) {
	activeSpinner = spinner
}

func (sp *STARSPane) getAircraftIndex(ac *av.Aircraft) int {
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
	// If there's an active spinner, it gets keyboard input.
	if activeSpinner != nil {
		if err := activeSpinner.KeyboardInput(cmd); err != nil {
			status.err = err
		} else {
			// Clear the input area and disable the spinner's mouse capture
			// on success.
			status.clear = true
			sp.disableMenuSpinner(ctx)
		}
		return
	}

	lookupAircraft := func(callsign string, abbreviated bool) *av.Aircraft {
		if ac := ctx.ControlClient.AircraftFromPartialCallsign(callsign); ac != nil {
			return ac
		}

		// try to match squawk code
		for _, ac := range sp.visibleAircraft(ctx) {
			if ac.Squawk.String() == callsign {
				return ac
			}
		}

		if idx, err := strconv.Atoi(callsign); err == nil {
			if callsign, ok := sp.IndexToAircraft[idx]; ok {
				return ctx.ControlClient.Aircraft[callsign]
			}
		}

		return nil
	}
	lookupCallsign := func(callsign string, abbreivated bool) string {
		ac := lookupAircraft(callsign, abbreivated)
		if ac != nil {
			return ac.Callsign
		}
		return callsign
	}

	ps := &sp.CurrentPreferenceSet
	switch sp.commandMode {
	case CommandModeNone:
		switch cmd {
		case "*AE":
			// Enable ATPA warning/alert cones
			ps.DisplayATPAWarningAlertCones = true
			status.clear = true
			return

		case "*AI":
			// Inhibit ATPA warning/alert cones
			ps.DisplayATPAWarningAlertCones = false
			status.clear = true
			return

		case "*BE":
			// Enable ATPA monitor cones
			ps.DisplayATPAMonitorCones = true
			status.clear = true
			return

		case "*BI":
			// Inhibit ATPA monitor cones
			ps.DisplayATPAMonitorCones = false
			status.clear = true
			return

		case "*DE":
			// Enable ATPA in-trail distances
			ps.DisplayATPAInTrailDist = true
			status.clear = true
			return

		case "*DI":
			// Inhibit ATPA in-trail distances
			ps.DisplayATPAInTrailDist = false
			status.clear = true
			return

		case "*D+":
			// Toggle
			ps.DisplayTPASize = !ps.DisplayTPASize
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.output = util.Select(ps.DisplayTPASize, "TPA SIZE ON", "TPA SIZE OFF")
			status.clear = true
			return

		case "*D+E":
			// Enable
			ps.DisplayTPASize = true
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE ON"
			return

		case "*D+I":
			// Inhibit
			ps.DisplayTPASize = false
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE OFF"
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

		if len(cmd) > 5 && cmd[:2] == "**" { // Force QL
			// Manual 6-69
			cmd = cmd[2:]

			callsign, tcps, _ := strings.Cut(cmd, " ")
			aircraft := lookupAircraft(callsign, false)
			if aircraft == nil {
				status.err = ErrSTARSNoFlight
			} else {
				for _, tcp := range strings.Split(tcps, " ") {
					if tcp == "ALL" {
						var fac string
						for _, control := range ctx.ControlClient.Controllers {
							if control.Callsign == ctx.ControlClient.Callsign {
								fac = control.FacilityIdentifier
							}
						}
						for _, control := range ctx.ControlClient.Controllers {
							if !control.ERAMFacility && control.FacilityIdentifier == fac {
								sp.forceQL(ctx, aircraft.Callsign, control.Callsign)
							}
						}
					} else {
						control := sp.lookupControllerForId(ctx, tcp, aircraft.Callsign)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
							return
						}
						sp.forceQL(ctx, aircraft.Callsign, control.Callsign)
					}
				}
				status.clear = true
				return
			}
		}

		if len(cmd) >= 2 && cmd[:2] == "*T" {
			suffix := cmd[2:]
			if suffix == "" {
				// Remove all RBLs
				sp.wipRBL = nil
				sp.RangeBearingLines = nil
				status.clear = true
			} else if idx, err := strconv.Atoi(cmd[2:]); err == nil {
				// Delete specified rbl
				idx--
				if idx >= 0 && idx < len(sp.RangeBearingLines) {
					sp.RangeBearingLines = util.DeleteSliceElement(sp.RangeBearingLines, idx)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
			} else if p, ok := ctx.ControlClient.Locate(suffix); ok {
				// Fix name for first or second point of RBL
				if rbl := sp.wipRBL; rbl != nil {
					rbl.P[1].Loc = p
					sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
					sp.wipRBL = nil
					status.clear = true
				} else {
					sp.wipRBL = &STARSRangeBearingLine{}
					sp.wipRBL.P[0].Loc = p
					sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
					sp.previewAreaInput = "*T" // set up for the second point
				}
			} else {
				status.err = ErrSTARSIllegalFix
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
				if pos, ok := ctx.ControlClient.Locate(f[1]); ok {
					highlightedLocation = pos
					highlightedLocationEndTime = ctx.Now.Add(5 * time.Second)
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalFix
					return
				}
			}
		}
		if len(cmd) > 0 {
			if cmd == "ALL" {
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
				status.clear, sp.previewAreaInput, status.err = sp.updateQL(ctx, cmd)
				return
			}
		}

	case CommandModeInitiateControl:
		if ac := lookupAircraft(cmd, false); ac == nil {
			status.err = ErrSTARSNoFlight
		} else {
			sp.initiateTrack(ctx, ac.Callsign)
			status.clear = true
		}
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for callsign, ac := range ctx.ControlClient.Aircraft {
				if ac.TrackingController == ctx.ControlClient.Callsign {
					sp.dropTrack(ctx, callsign)
				}
			}
			status.clear = true
			return
		} else {
			sp.dropTrack(ctx, lookupCallsign(cmd, false))
			return
		}

	case CommandModeHandOff:
		if cmd != "" && string(cmd[0]) == "C" { // Enabling/ disabling automatic handoff processing
			// Manual 4-30
			if string(cmd[1]) == "X" {
				if string(cmd[2]) == "E" {
					sp.AirspaceAwareness.Interfacility = true
				} else if string(cmd[2]) == "I" {
					sp.AirspaceAwareness.Interfacility = false
				}
			} else if string(cmd[1]) == "T" {
				if string(cmd[2]) == "E" {
					sp.AirspaceAwareness.Intrafacility = true
				} else if string(cmd[2]) == "I" {
					sp.AirspaceAwareness.Intrafacility = false
				}
			}
			if string(cmd[1]) == "E" {
				sp.AirspaceAwareness.Intrafacility = true
				sp.AirspaceAwareness.Interfacility = true
			} else if string(cmd[1]) == "I" {
				sp.AirspaceAwareness.Intrafacility = false
				sp.AirspaceAwareness.Interfacility = false
			}
		}
		f := strings.Fields(cmd)
		switch len(f) {
		case 0:
			// Accept hand off of target closest to range rings center
			var closest *av.Aircraft
			var closestDistance float32
			for _, ac := range sp.visibleAircraft(ctx) {
				if ac.HandoffTrackController != ctx.ControlClient.Callsign {
					continue
				}

				state := sp.Aircraft[ac.Callsign]
				d := math.NMDistance2LL(ps.RangeRingsCenter, state.TrackPosition())
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
			sp.cancelHandoff(ctx, lookupCallsign(f[0], false))
			status.clear = true
			return
		case 2:
			if err := sp.handoffTrack(ctx, lookupCallsign(f[1], false), f[0]); err != nil {
				status.err = err
			} else {
				status.clear = true
			}
			return
		}

	case CommandModeVFRPlan:
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
				sfilt := util.FilterSlice(ps.SelectedBeaconCodes,
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
			} else if ac := lookupAircraft(cmd, false); ac != nil {
				// D(callsign)
				// Display flight plan
				status.output, status.err = sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac)
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
			if l := len(cmd); l == 1 {
				if dir, ok := numpadToDirection(cmd[0]); ok && dir != nil {
					// 4-97: tracked by me, '5' not allowed
					ps.LeaderLineDirection = *dir
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
			} else if l == 2 {
				if dir, ok := numpadToDirection(cmd[0]); ok && dir != nil && cmd[1] == 'U' {
					// 4-101: unassociated tracks; '5' is not allowed here.
					ps.UnassociatedLeaderLineDirection = dir
					status.clear = true
				} else if ok && cmd[1] == '*' {
					// 4-98: tracked by other controllers
					ps.OtherControllerLeaderLineDirection = dir
					// This also clears out any controller-specific assignments (4-98)
					clear(ps.ControllerLeaderLineDirections)
					status.clear = true
				} else if cmd == "5*" {
					// Remove setting for other controllers
					ps.OtherControllerLeaderLineDirection = nil
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
			} else if len(cmd) >= 3 {
				// 4-99: track owned by a specific TCP: L(tcp)(dir),(where
				// tcp has a space if it's given as a single character).
				tcp := strings.TrimSuffix(cmd[:2], " ")
				if controller := sp.lookupControllerForId(ctx, tcp, ""); controller != nil {
					if dir, ok := numpadToDirection(cmd[2]); ok {
						// Per-controller leaderline
						if ps.ControllerLeaderLineDirections == nil {
							ps.ControllerLeaderLineDirections = make(map[string]math.CardinalOrdinalDirection)
						}
						if dir != nil {
							ps.ControllerLeaderLineDirections[controller.Callsign] = *dir
						} else {
							delete(ps.ControllerLeaderLineDirections, controller.Callsign)
						}
						status.clear = true
						return
					}
				} else if num, acid, ok := strings.Cut(cmd, " "); ok {
					// L(#) (ACID) or L(##) (ACID)
					if ac := lookupAircraft(acid, false); ac != nil {
						if err := sp.setLeaderLine(ctx, ac, num); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
					} else {
						status.err = ErrSTARSNoFlight
					}
				} else {
					status.err = ErrSTARSIllegalPosition
				}
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return

		case "N":
			// CRDA...
			if len(sp.ConvergingRunways) == 0 {
				// These are all illegal if there are no CRDA runway pairs
				status.err = ErrSTARSIllegalFunction
				return
			}
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
			} else {
				// Given a string that starts with a runway identifier and then possibly has some extra text,
				// return the runway and the text as separate strings.
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

				// This function takes a string of the form "JFK 22LMORE"
				// or "22LMORE" and looks for the associated
				// CRDARunwayPairState and CRDARunwayState for an enabled
				// CRDA runway.  "MORE" represents arbitrary text *that may
				// contain spaces*.  If the airport is not specified, then
				// it must be possible to unambiguously determine the
				// airport given the runway. It returns:
				//
				// airport: the name of the associated airport
				// runway: the runway identifier
				// ps: CRDARunwayPairState for the runway
				// rs: CRDARunwayState for the runway
				// extra: any excess text after the runway identifier
				// err: ErrSTARSIllegalParam if there is no such enabled
				//   runway pair or if more than one matches when only a
				//   runway is specified.
				getRunwayState := func(s string) (airport string, runway string, ps *CRDARunwayPairState,
					rs *CRDARunwayState, extra string, err error) {
					if s[0] >= '0' && s[0] <= '9' {
						// It starts with a runway identifier. (We'll
						// assume CRDA isn't happening for airports
						// with names like '87N'..)
						runway, extra = getRunway(s)

						for i, pair := range sp.ConvergingRunways {
							pairState := &sp.CurrentPreferenceSet.CRDA.RunwayPairState[i]
							if !pairState.Enabled {
								continue
							}
							for j, pairRunway := range pair.Runways {
								if runway != pairRunway {
									continue
								}

								if ps != nil {
									// We found more than one match...
									err = ErrSTARSIllegalParam
									return
								}
								airport = pair.Airport
								ps, rs = pairState, &pairState.RunwayState[j]
							}
						}
						if ps == nil {
							err = ErrSTARSIllegalParam
						}
					} else {
						// Expect airport and then a space.
						var ok bool
						airport, extra, ok = strings.Cut(s, " ")
						if !ok {
							err = ErrSTARSIllegalParam
							return
						}

						runway, extra = getRunway(extra)
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport != airport {
								continue
							}

							pairState := &sp.CurrentPreferenceSet.CRDA.RunwayPairState[i]
							if !pairState.Enabled {
								continue
							}

							for j, pairRunway := range pair.Runways {
								if runway == pairRunway {
									ps, rs = pairState, &pairState.RunwayState[j]
									return
								}
							}
						}
						err = ErrSTARSIllegalParam
					}
					return
				}

				// Check these commands first; if we key off cmd[0]=='L' for example we end up issuing
				// an error if the user actually specified an airport starting with "L"...
				if ap, rwy, _, runwayState, extra, err := getRunwayState(cmd); err == nil {
					if extra == "E" || (extra == "" && !runwayState.Enabled) {
						// 6-23: enable ghosts for runway
						runwayState.Enabled = true
						status.output = ap + " " + rwy + " GHOSTING ENABLED"
						status.clear = true
						return
					} else if extra == "I" || (extra == "" && runwayState.Enabled) {
						// 6-23: disable ghosts for runway
						runwayState.Enabled = false
						status.output = ap + " " + rwy + " GHOSTING INHIBITED"
						// this also disables the runway's visualizations
						runwayState.DrawQualificationRegion = false
						runwayState.DrawCourseLines = false
						status.clear = true
						return
					} else if extra == " B" { // 6-31
						runwayState.DrawQualificationRegion = !runwayState.DrawQualificationRegion
						status.clear = true
						return
					} else if extra == " L" { // 6-32
						runwayState.DrawCourseLines = !runwayState.DrawCourseLines
						status.clear = true
						return
					}
				}
				if cmd[0] == 'L' {
					// 6-26: Set leader line direction: NL(airport) (runway)(1-9)
					// or: NL(runway)(1-9); runway must unambiguously define airport
					if _, _, _, runwayState, num, err := getRunwayState(cmd[1:]); err == nil {
						if len(num) == 1 {
							if dir, ok := numpadToDirection(num[0]); ok {
								runwayState.LeaderLineDirection = dir
								status.clear = true
								return
							}
						}
						status.err = ErrSTARSCommandFormat
						return
					}
				} else if cmd[0] == 'P' {
					// These commands either start with an airport and a
					// space or use the controller's default airport if
					// none is specified. None of the commands otherwise
					// allow spaces, so we can use the presence of a space
					// to determine if an airport was specified.
					airport, extra, ok := strings.Cut(cmd[1:], " ")
					if !ok {
						if ctrl, ok := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]; ok {
							airport = ctrl.DefaultAirport[1:] // drop leading "K"
							extra = cmd[1:]
						}
					}

					if index, err := strconv.Atoi(extra); err == nil {
						// 6-22: toggle ghosts for a runway pair
						// NP(airport )(idx) / NP(idx)
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport == airport && pair.Index == index {
								// TODO: we toggle each independently; is that correct?
								rps := &ps.CRDA.RunwayPairState[i]
								rps.RunwayState[0].Enabled = !rps.RunwayState[0].Enabled
								rps.RunwayState[1].Enabled = !rps.RunwayState[1].Enabled
								status.clear = true
								return
							}
						}
						status.err = ErrSTARSCommandFormat
						return
					} else {
						// 8-11: disable/set stagger or tie mode for a runway pair
						// NP(airport )(idx)(cmd) / NP(idx)(cmd)
						n := len(extra)
						if n < 2 || (extra[n-1] != 'S' && extra[n-1] != 'T' && extra[n-1] != 'D') {
							status.err = ErrSTARSCommandFormat
							return
						}
						index, err := strconv.Atoi(extra[:n-1])
						if err != nil {
							status.err = ErrSTARSIllegalRPC
							return
						}
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport != airport || pair.Index != index {
								continue
							}

							if extra[n-1] == 'D' {
								ps.CRDA.RunwayPairState[i].Enabled = false
								status.clear = true
								status.output = airport + " " + pair.getRunwaysString() + " INHIBITED"
								return
							} else {
								// Make sure neither of the runways involved is already enabled in
								// another pair.
								for j, pairState := range ps.CRDA.RunwayPairState {
									if !pairState.Enabled {
										continue
									}
									if sp.ConvergingRunways[j].Runways[0] == pair.Runways[0] ||
										sp.ConvergingRunways[j].Runways[0] == pair.Runways[1] ||
										sp.ConvergingRunways[j].Runways[1] == pair.Runways[0] ||
										sp.ConvergingRunways[j].Runways[1] == pair.Runways[1] {
										status.err = ErrSTARSIllegalRunway
										return
									}
								}

								if extra[n-1] == 'S' {
									ps.CRDA.RunwayPairState[i].Mode = CRDAModeStagger
								} else {
									ps.CRDA.RunwayPairState[i].Mode = CRDAModeTie
								}
								ps.CRDA.RunwayPairState[i].Enabled = true
								ps.CRDAStatusList.Visible = true
								status.output = airport + " " + pair.getRunwaysString() + " ENABLED"
								status.clear = true
								return
							}
						}
					}
				}
			}
			status.err = ErrSTARSIllegalParam
			return

		case "O":
			if len(cmd) > 2 {
				aircraft := lookupAircraft(cmd, false)
				if aircraft == nil {
					status.err = ErrSTARSCommandFormat
					return
				} else if aircraft.TrackingController == "" {
					status.err = ErrSTARSIllegalTrack
					return
				} else {
					status.output = strings.Join(aircraft.PointOutHistory, " ")
					status.clear = true
					return
				}
			}
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
						n = math.Clamp(n, 1, 100)
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
				status.clear, sp.previewAreaInput, status.err = sp.updateQL(ctx, cmd)
				return
			}

		case "R": // requested altitude: 6-107
			switch cmd {
			case "A": // toggle
				ps.DisplayRequestedAltitude = !ps.DisplayRequestedAltitude
				status.clear = true
				return
			case "AE": // enable
				ps.DisplayRequestedAltitude = true
				status.clear = true
				return
			case "AI": // inhibit
				ps.DisplayRequestedAltitude = false
				status.clear = true
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
						*lines = math.Clamp(n, 1, 100) // TODO: or error if out of range? (and below..)
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
			isSecondary := false
			if len(cmd) > 0 && cmd[0] == '+' {
				isSecondary = true
				cmd = cmd[1:]
			}

			f := strings.Fields(cmd)
			if len(f) == 1 {
				// Y callsign -> clear scratchpad and reported altitude
				// Y+ callsign -> secondary scratchpad..
				callsign := lookupCallsign(f[0], false)
				if state, ok := sp.Aircraft[callsign]; ok {
					state.pilotAltitude = 0
					if err := sp.setScratchpad(ctx, callsign, "", isSecondary, false); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt
				// as above, Y+ -> secondary scratchpad

				// Either pilot alt or scratchpad entry
				if ac := lookupAircraft(f[0], false); ac == nil {
					status.err = ErrSTARSNoFlight
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.Aircraft[ac.Callsign].pilotAltitude = alt * 100
				} else {
					if err := sp.setScratchpad(ctx, ac.Callsign, f[1], isSecondary, false); err != nil {
						status.err = err
					}
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
			callsign := lookupCallsign(f[0], false)
			status.err = ctx.ControlClient.SetSquawkAutomatic(callsign)
		} else if len(f) == 2 {
			if squawk, err := av.ParseSquawk(f[1]); err == nil {
				callsign := lookupCallsign(f[0], false)
				status.err = ctx.ControlClient.SetSquawk(callsign, squawk)
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
			if ac := lookupAircraft(cmd[2:], false); ac != nil {
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
		// FIXME? globalConfig.Save()
		return

	case CommandModeMaps:
		if cmd == "A" {
			// remove all maps
			for i := range ps.DisplayVideoMap {
				ps.DisplayVideoMap[i] = false
			}
			ps.SystemMapVisible = make(map[int]interface{})
			sp.activeDCBMenu = DCBMenuMain
			status.clear = true
			return
		} else if n := len(cmd); n > 0 {
			op := "T"            // toggle by default
			if cmd[n-1] == 'E' { // enable
				op = "E"
				cmd = cmd[:n-1]
			} else if cmd[n-1] == 'I' { // inhibit
				op = "T"
				cmd = cmd[:n-1]
			}

			videoMaps, _ := ctx.ControlClient.GetVideoMaps()

			if idx, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if idx <= 0 {
				status.err = ErrSTARSIllegalMap
			} else if mi := slices.IndexFunc(videoMaps, func(m av.VideoMap) bool { return m.Id == idx }); mi != -1 {
				if (ps.DisplayVideoMap[mi] && op == "T") || op == "I" {
					ps.DisplayVideoMap[mi] = false
				} else if (!ps.DisplayVideoMap[mi] && op == "T") || op == "E" {
					ps.DisplayVideoMap[mi] = true
				}
				sp.activeDCBMenu = DCBMenuMain
				status.clear = true
			} else if _, ok := sp.systemMaps[idx]; ok {
				if _, ok := ps.SystemMapVisible[idx]; (ok && op == "T") || op == "I" {
					delete(ps.SystemMapVisible, idx)
				} else if (!ok && op == "T") || op == "E" {
					ps.SystemMapVisible[idx] = nil
				}
				sp.activeDCBMenu = DCBMenuMain
				status.clear = true
			} else {
				status.err = ErrSTARSIllegalMap
			}
			status.clear = true
			return
		}

	case CommandModeLDR, CommandModeRangeRings, CommandModeRange:
		// There should always be an active spinner in these modes, which
		// is handled at the start of the method...

	case CommandModeSiteMenu:
		if cmd == "~" {
			ps.RadarSiteSelected = ""
			status.clear = true
			return
		} else if len(cmd) > 0 {
			// Index, character id, or name
			if i, err := strconv.Atoi(cmd); err == nil {
				if i < 0 || i >= len(ctx.ControlClient.RadarSites) {
					status.err = ErrSTARSIllegalValue
				} else {
					ps.RadarSiteSelected = util.SortedMapKeys(ctx.ControlClient.RadarSites)[i]
					status.clear = true
				}
				return
			}
			for id, rs := range ctx.ControlClient.RadarSites {
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

func (sp *STARSPane) updateQL(ctx *PaneContext, input string) (ok bool, previewInput string, err error) {
	positions, input, err := sp.parseQuickLookPositions(ctx, input)
	if err != nil {
		ok = false
		previewInput = input
		return
	}

	if len(positions) > 0 {
		ps := &sp.CurrentPreferenceSet
		ps.QuickLookAll = false

		for _, pos := range positions {
			// Toggle
			match := func(q QuickLookPosition) bool { return q.Id == pos.Id && q.Plus == pos.Plus }
			matchId := func(q QuickLookPosition) bool { return q.Id == pos.Id }
			if slices.ContainsFunc(ps.QuickLookPositions, match) {
				nomatch := func(q QuickLookPosition) bool { return !match(q) }
				ps.QuickLookPositions = util.FilterSlice(ps.QuickLookPositions, nomatch)
			} else if idx := slices.IndexFunc(ps.QuickLookPositions, matchId); idx != -1 {
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
		ok = true
	} else {
		previewInput = input
	}
	return
}

func (sp *STARSPane) setScratchpad(ctx *PaneContext, callsign string, contents string, isSecondary bool, isImplied bool) error {
	lc := len([]rune(contents))

	if ac, ok := ctx.ControlClient.Aircraft[callsign]; ok && ac != nil && ac.TrackingController == "" {
		// This is because /OK can be used for associated tracks that are
		// not owned by this TCP. But /OK cannot be used for unassociated
		// tracks. So might as well weed them out now.
		return ErrSTARSIllegalTrack
	}

	fac := ctx.ControlClient.STARSFacilityAdaptation
	if isSecondary {
		// 5-148: secondary is 1 to 3-maybe-4 characters
		if (fac.AllowLongScratchpad[1] && lc > 4) || (!fac.AllowLongScratchpad[1] && lc > 3) {
			return ErrSTARSCommandFormat
		}
	} else {
		// 5-148: primary is 2 to 3-maybe-4 characters
		if lc == 1 || (fac.AllowLongScratchpad[0] && lc > 4) || (!fac.AllowLongScratchpad[0] && lc > 3) {
			return ErrSTARSCommandFormat
		}
	}

	// Make sure it's only allowed characters
	allowedCharacters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./*" + STARSTriangleCharacter
	for _, letter := range contents {
		if !strings.ContainsRune(allowedCharacters, letter) {
			return ErrSTARSCommandFormat
		}
	}

	// It can't be three numerals
	if lc == 3 && contents[0] >= '0' && contents[0] <= '9' &&
		contents[1] >= '0' && contents[1] <= '9' &&
		contents[2] >= '0' && contents[2] <= '9' {
		return ErrSTARSCommandFormat
	}

	if !isSecondary && isImplied {
		// For the implied version (i.e., not [multifunc]Y), it also can't
		// match one of the TCPs
		if lc == 2 {
			for _, ctrl := range ctx.ControlClient.Controllers {
				if ctrl.FacilityIdentifier == "" && ctrl.SectorId == contents {
					return ErrSTARSCommandFormat
				}
			}
		}
	}

	// Certain specific strings aren't allowed in the first 3 characters
	illegalScratchpads := []string{"NAT", "CST", "AMB", "RDR", "ADB", "XXX"}
	if lc >= 3 && slices.Contains(illegalScratchpads, contents[:3]) {
		return ErrSTARSIllegalScratchpad
	}

	if isSecondary {
		ctx.ControlClient.SetSecondaryScratchpad(callsign, contents, nil,
			func(err error) { sp.displayError(err, ctx) })
	} else {
		ctx.ControlClient.SetScratchpad(callsign, contents, nil,
			func(err error) { sp.displayError(err, ctx) })
	}
	return nil
}

func (sp *STARSPane) setTemporaryAltitude(ctx *PaneContext, callsign string, alt int) {
	ctx.ControlClient.SetTemporaryAltitude(callsign, alt, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) setGlobalLeaderLine(ctx *PaneContext, callsign string, dir *math.CardinalOrdinalDirection) {
	state := sp.Aircraft[callsign]
	state.GlobalLeaderLineDirection = dir // hack for instant update
	state.UseGlobalLeaderLine = dir != nil

	ctx.ControlClient.SetGlobalLeaderLine(callsign, dir, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) initiateTrack(ctx *PaneContext, callsign string) {
	ctx.ControlClient.InitiateTrack(callsign,
		func(any) {
			if state, ok := sp.Aircraft[callsign]; ok {
				state.DatablockType = FullDatablock
			}
			if ac, ok := ctx.ControlClient.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac)
			}
		},
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) dropTrack(ctx *PaneContext, callsign string) {
	ctx.ControlClient.DropTrack(callsign, nil, func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acceptHandoff(ctx *PaneContext, callsign string) {
	ctx.ControlClient.AcceptHandoff(callsign,
		func(any) {
			if state, ok := sp.Aircraft[callsign]; ok {
				state.DatablockType = FullDatablock
			}
			if ac, ok := ctx.ControlClient.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac)
			}
		},
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) handoffTrack(ctx *PaneContext, callsign string, controller string) error {
	control := sp.lookupControllerForId(ctx, controller, callsign)
	if control == nil {
		return ErrSTARSIllegalPosition
	}

	ctx.ControlClient.HandoffTrack(callsign, control.Callsign, nil,
		func(err error) { sp.displayError(err, ctx) })

	return nil
}

// returns the controller responsible for the aircraft given its altitude
// and route.
func calculateAirspace(ctx *PaneContext, callsign string) (string, error) {
	ac := ctx.ControlClient.Aircraft[callsign]
	if ac == nil {
		return "", ErrSTARSIllegalFlight
	}

	for _, rules := range ctx.ControlClient.STARSFacilityAdaptation.AirspaceAwareness {
		for _, fix := range rules.Fix {
			// Does the fix in the rules match the route?
			if fix != "ALL" && !ac.RouteIncludesFix(fix) {
				continue
			}

			// Does the final altitude satisfy the altitude range, if specified?
			alt := rules.AltitudeRange
			if !(alt[0] == 0 && alt[1] == 0) /* none specified */ &&
				(ac.FlightPlan.Altitude < alt[0] || ac.FlightPlan.Altitude > alt[1]) {
				continue
			}

			// Finally make sure any aircraft type specified in the rules
			// in the matches.
			aircraftType := ac.AircraftPerformance().Engine.AircraftType
			if len(rules.AircraftType) == 0 || slices.Contains(rules.AircraftType, aircraftType) {
				return rules.ReceivingController, nil
			}
		}
	}

	return "", ErrSTARSIllegalPosition
}

func singleScope(ctx *PaneContext, facilityIdentifier string) *av.Controller {
	var controllersInFacility []*av.Controller
	for _, controller := range ctx.ControlClient.Controllers {
		if controller.FacilityIdentifier == facilityIdentifier {
			controllersInFacility = append(controllersInFacility, controller)
		}
	}
	if len(controllersInFacility) == 1 {
		return controllersInFacility[0]
	} else {
		return nil
	}
}

// Given a controller TCP id and optionally an aircraft callsign, returns
// the associated Controller.
func (sp *STARSPane) lookupControllerForId(ctx *PaneContext, id, callsign string) *av.Controller {
	haveTrianglePrefix := strings.HasPrefix(id, STARSTriangleCharacter)
	id = strings.TrimPrefix(id, STARSTriangleCharacter)

	lc := len(id)
	if lc == 0 {
		return nil
	}

	if haveTrianglePrefix {
		if lc == 1 {
			// Facility id where there's only one controller at that facility.
			return singleScope(ctx, id)
		} else if lc == 3 {
			// ∆N4P for example. Must be a different facility.
			for _, control := range ctx.ControlClient.Controllers {
				if control.SectorId == id[1:] && control.FacilityIdentifier == string(id[0]) {
					return control
				}
			}
		}
	} else if id == "C" {
		// ARTCC airspace-awareness; must have an aircraft callsign
		if callsign == "" {
			return nil
		}

		controlCallsign, err := calculateAirspace(ctx, callsign)
		if err != nil {
			return nil
		}
		if control, ok := ctx.ControlClient.Controllers[controlCallsign]; ok && control != nil {
			toCenter := control.ERAMFacility
			if toCenter || (id == control.FacilityIdentifier && !toCenter) {
				return control
			}
		}
	} else {
		// Non ARTCC airspace-awareness handoffs
		if lc == 1 { // Must be a same sector.
			userController := *ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]

			for _, control := range ctx.ControlClient.Controllers { // If the controller fac/ sector == userControllers fac/ sector its all good!
				if control.FacilityIdentifier == "" && // Same facility? (Facility ID will be "" if they are the same fac)
					control.SectorId[0] == userController.SectorId[0] && // Same Sector?
					string(control.SectorId[1]) == id { // The actual controller
					return control
				}
			}
		} else if lc == 2 {
			// Must be a same sector || same facility.
			for _, control := range ctx.ControlClient.Controllers {
				if control.SectorId == id && control.FacilityIdentifier == "" {
					return control
				}
			}
		}

		for _, control := range ctx.ControlClient.Controllers {
			if control.ERAMFacility && control.SectorId == id {
				return control
			}
		}
	}
	return nil
}

func (sp *STARSPane) setLeaderLine(ctx *PaneContext, ac *av.Aircraft, cmd string) error {
	state := sp.Aircraft[ac.Callsign]
	if len(cmd) == 1 {
		if dir, ok := numpadToDirection(cmd[0]); ok {
			state.LeaderLineDirection = dir
			if dir != nil {
				state.UseGlobalLeaderLine = false
			}
			return nil
		}
	} else if len(cmd) == 2 && cmd[0] == cmd[1] { // Global leader lines
		if ac.TrackingController != ctx.ControlClient.Callsign {
			return ErrSTARSIllegalTrack
		} else if dir, ok := numpadToDirection(cmd[0]); ok {
			sp.setGlobalLeaderLine(ctx, ac.Callsign, dir)
			return nil
		}
	}
	return ErrSTARSCommandFormat
}

func (sp *STARSPane) forceQL(ctx *PaneContext, callsign, controller string) {
	ctx.ControlClient.ForceQL(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) redirectHandoff(ctx *PaneContext, callsign, controller string) {
	ctx.ControlClient.RedirectHandoff(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acceptRedirectedHandoff(ctx *PaneContext, callsign string) {
	ctx.ControlClient.AcceptRedirectedHandoff(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) RemoveForceQL(ctx *PaneContext, callsign, controller string) {
	ctx.ControlClient.RemoveForceQL(callsign, controller, nil, nil) // Just a slew so the slew could be for other things
}

func (sp *STARSPane) pointOut(ctx *PaneContext, callsign string, controller string) {
	ctx.ControlClient.PointOut(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acknowledgePointOut(ctx *PaneContext, callsign string) {
	ctx.ControlClient.AcknowledgePointOut(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) cancelHandoff(ctx *PaneContext, callsign string) {
	ctx.ControlClient.CancelHandoff(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) executeSTARSClickedCommand(ctx *PaneContext, cmd string, mousePosition [2]float32,
	ghosts []*av.GhostAircraft, transforms ScopeTransformations) (status STARSCommandStatus) {
	// See if an aircraft was clicked
	ac, acDistance := sp.tryGetClosestAircraft(ctx, mousePosition, transforms)
	ghost, ghostDistance := sp.tryGetClosestGhost(ghosts, mousePosition, transforms)

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
				ac := ctx.ControlClient.Aircraft[ghost.Callsign]
				status.output, status.err = sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac)
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
			if cmd == "" {
				if time.Until(state.RDIndicatorEnd) > 0 {
					if state.OutboundHandoffAccepted {
						state.OutboundHandoffAccepted = false
						state.OutboundHandoffFlashEnd = ctx.Now
					}
					state.RDIndicatorEnd = time.Time{}
					status.clear = true
					return
				} else if ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign || ac.RedirectedHandoff.GetLastRedirector() == ctx.ControlClient.Callsign {
					sp.acceptRedirectedHandoff(ctx, ac.Callsign)
					status.clear = true
					return
				} else if ac.HandoffTrackController == ctx.ControlClient.Callsign && ac.RedirectedHandoff.RedirectedTo == "" {
					status.clear = true
					sp.acceptHandoff(ctx, ac.Callsign)
					return
				} else if slices.Contains(ac.ForceQLControllers, ctx.ControlClient.Callsign) {
					sp.RemoveForceQL(ctx, ac.Callsign, ctx.ControlClient.Callsign)
					status.clear = true
					return
				} else if slices.ContainsFunc(sp.CAAircraft, func(ca CAAircraft) bool {
					return (ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign) &&
						!ca.Acknowledged
				}) {
					// Acknowledged a CA
					for i, ca := range sp.CAAircraft {
						if ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign {
							status.clear = true
							sp.CAAircraft[i].Acknowledged = true
							return
						}
					}
				} else if state.MSAW && !state.MSAWAcknowledged {
					// Acknowledged a MSAW
					state.MSAWAcknowledged = true
				} else if ac.HandoffTrackController != "" && ac.HandoffTrackController != ctx.ControlClient.Callsign &&
					ac.TrackingController == ctx.ControlClient.Callsign {
					// cancel offered handoff offered
					status.clear = true
					sp.cancelHandoff(ctx, ac.Callsign)
					return
				} else if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
					// ack point out
					sp.acknowledgePointOut(ctx, ac.Callsign)
					status.clear = true
					return
				} else if state.PointedOut {
					state.PointedOut = false
					status.clear = true
					return
				} else if state.ForceQL {
					state.ForceQL = false
					status.clear = true
				} else if _, ok := sp.RejectedPointOuts[ac.Callsign]; ok {
					// ack rejected point out
					delete(sp.RejectedPointOuts, ac.Callsign)
					status.clear = true
					return
				} else if state.OutboundHandoffAccepted {
					// ack an accepted handoff
					status.clear = true
					state.OutboundHandoffAccepted = false
					state.OutboundHandoffFlashEnd = ctx.Now

					return
				} else if ctx.Keyboard != nil {
					_, ctrl := ctx.Keyboard.Pressed[platform.KeyControl]
					_, shift := ctx.Keyboard.Pressed[platform.KeyShift]
					if ctrl && shift {
						// initiate track, CRC style
						status.clear = true
						sp.initiateTrack(ctx, ac.Callsign)
						return
					}
				}
				if db := sp.datablockType(ctx, ac); db == LimitedDatablock && state.FullLDBEndTime.Before(ctx.Now) {
					state.FullLDBEndTime = ctx.Now.Add(5 * time.Second)
					// do not collapse datablock if user is tracking the aircraft
				} else if db == FullDatablock && ac.TrackingController != ctx.ControlClient.Callsign {
					state.DatablockType = PartialDatablock
				} else {
					state.DatablockType = FullDatablock
				}

				if ac.TrackingController == ctx.ControlClient.Callsign {
					status.output = slewAircaft(ac)
				}

			} else if cmd == "." {
				if err := sp.setScratchpad(ctx, ac.Callsign, "", false, true); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if cmd == "+" {
				if err := sp.setScratchpad(ctx, ac.Callsign, "", true, true); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if cmd == "*" {
				from := sp.Aircraft[ac.Callsign].TrackPosition()
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
					p := transforms.LatLongFromWindowP(pw)
					hdg := math.Heading2LL(from, p, ac.NmPerLongitude(), ac.MagneticVariation())
					dist := math.NMDistance2LL(from, p)

					status.output = fmt.Sprintf("%03d/%.2f", int(hdg+.5), dist)
					status.clear = true
					return
				}
				return
			} else if (unicode.IsDigit(rune(cmd[0])) && len(cmd) == 1) ||
				(len(cmd) == 2 && unicode.IsDigit(rune(cmd[1]))) {
				// 6-81: set locally, 6-101: set system wide
				if err := sp.setLeaderLine(ctx, ac, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if cmd == "?" {
				ctx.Lg.Info("print aircraft", slog.String("callsign", ac.Callsign),
					slog.Any("aircraft", ac))
				fmt.Println(spew.Sdump(ac) + "\n" + ac.Nav.FlightState.Summary())
				status.clear = true
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
				sp.wipRBL = &STARSRangeBearingLine{}
				sp.wipRBL.P[0].Callsign = ac.Callsign
				sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
				// Do not clear the input area to allow entering a fix for the second location
				return
			} else if av.StringIsSPC(cmd) {
				ctx.ControlClient.ToggleSPCOverride(ac.Callsign, cmd, nil,
					func(err error) { sp.displayError(err, ctx) })
				status.clear = true
				return
			} else if cmd == "UN" {
				ctx.ControlClient.RejectPointOut(ac.Callsign, nil,
					func(err error) { sp.displayError(err, ctx) })
				status.clear = true
				return
			} else if lc := len(cmd); lc >= 2 && cmd[0:2] == "**" { // Force QL. You need to specify a TCP unless otherwise specified in STARS config
				// STARS Manual 6-70 (On slew). Cannot go interfacility
				// TODO: Or can be used to accept a pointout as a handoff.

				if cmd == "**" { // Non specified TCP
					if ctx.ControlClient.STARSFacilityAdaptation.ForceQLToSelf && ac.TrackingController == ctx.ControlClient.Callsign {
						state.ForceQL = true
						status.clear = true
						return
					} else {
						status.err = ErrSTARSIllegalPosition
						return
					}
				} else {
					tcps := strings.Split(cmd[2:], " ")
					if len(tcps) > 0 && tcps[0] == "ALL" {
						// Force QL for all TCP
						// Find user fac
						for _, control := range ctx.ControlClient.Controllers {
							if control.Callsign == ctx.ControlClient.Callsign && !control.ERAMFacility {
								sp.forceQL(ctx, ac.Callsign, ctx.ControlClient.Callsign)
							}
						}
					}
					for _, tcp := range tcps {
						control := sp.lookupControllerForId(ctx, tcp, ac.Callsign)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
							return
						}
						sp.forceQL(ctx, ac.Callsign, control.Callsign)
					}
					status.clear = true
					return
				}

			} else if cmd == "*D+" {
				// TODO: this and the following two should give ILL FNCT if
				// there's no j-ring/[A]TPA cone being displayed for the
				// track (6-173).

				// toggle TPA size display
				if state.DisplayTPASize == nil {
					b := ps.DisplayTPASize // new variable; don't alias ps.DisplayTPASize!
					state.DisplayTPASize = &b
				}
				*state.DisplayTPASize = !*state.DisplayTPASize
				status.clear = true
				return
			} else if cmd == "*D+E" {
				// enable TPA size display
				b := true
				state.DisplayTPASize = &b
				status.clear = true
				return
			} else if cmd == "*D+I" {
				// inhibit TPA size display
				b := false
				state.DisplayTPASize = &b
				status.clear = true
				return
			} else if cmd == "*AE" {
				// Enable ATPA warning/alert cones for the track
				// TODO: for this and *AI and the two *B commands below, we
				// should issue an error if not IFR, not displaying FDB, or
				// not in ATPA approach volume (6-176).
				b := true
				state.DisplayATPAWarnAlert = &b
				status.clear = true
				return
			} else if cmd == "*AI" {
				// Inhibit ATPA warning/alert cones for the track
				b := false
				state.DisplayATPAWarnAlert = &b
				status.clear = true
				return
			} else if cmd == "*BE" {
				// Enable ATPA monitor cones for the track
				b := true
				state.DisplayATPAMonitor = &b
				status.clear = true
				return
			} else if cmd == "*BI" {
				// Inhibit ATPA monitor cones for the track
				b := false
				state.DisplayATPAMonitor = &b
				status.clear = true
				return
			} else if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
				state.pilotAltitude = alt * 100
				status.clear = true
				return
			} else if len(cmd) == 5 && cmd[:2] == "++" {
				if alt, err := strconv.Atoi(cmd[2:]); err == nil {
					status.err = amendFlightPlan(ctx, ac.Callsign, func(fp *av.FlightPlan) {
						fp.Altitude = alt * 100
					})
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return
			} else if len(cmd) >= 2 && cmd[0] == '+' {
				if alt, err := strconv.Atoi(cmd[1:]); err == nil {
					sp.setTemporaryAltitude(ctx, ac.Callsign, alt*100)
					status.clear = true
				} else {
					if err := sp.setScratchpad(ctx, ac.Callsign, cmd[1:], true, true); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
				return
			} else if cmd == ".ROUTE" {
				sp.drawRouteAircraft = ac.Callsign
				status.clear = true
				return
			} else if len(cmd) > 2 && cmd[:2] == "*J" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
						state.ConeLength = 0 // can't have both
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
						state.ConeLength = 0 // can't have both
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if len(cmd) > 2 && cmd[:2] == "*P" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.ConeLength = float32(r)
						state.JRingRadius = 0 // can't have both
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.ConeLength = float32(r)
						state.JRingRadius = 0 // can't have both
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if lc := len(cmd); lc >= 2 && cmd[lc-1] == '*' { // Some sort of pointout
				// First check for errors. (Manual 6-73)

				// Check if arrival
				for _, airport := range ctx.ControlClient.ArrivalAirports {
					if airport.Name == ac.FlightPlan.ArrivalAirport {
						status.err = ErrSTARSIllegalTrack
						return
					}
				}
				// Check if being handed off, pointed out or suspended (TODO suspended)
				if sp.OutboundPointOuts[ac.Callsign] != "" || sp.InboundPointOuts[ac.Callsign] != "" ||
					(ac.HandoffTrackController != "" && ac.HandoffTrackController != ctx.ControlClient.Callsign) {
					status.err = ErrSTARSIllegalTrack
					return
				}

				control := sp.lookupControllerForId(ctx, strings.TrimSuffix(cmd, "*"), ac.Callsign)
				if control == nil {
					status.err = ErrSTARSIllegalPosition
				} else {
					status.clear = true
					sp.pointOut(ctx, ac.Callsign, control.Callsign)
				}
				return

			} else if len(cmd) > 0 {
				// See if cmd works as a sector id; if so, make it a handoff.
				control := sp.lookupControllerForId(ctx, cmd, ac.Callsign)
				if control != nil {
					if ac.HandoffTrackController == ctx.ControlClient.Callsign || ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign { // Redirect
						if ac.RedirectedHandoff.ShouldFallbackToHandoff(ctx.ControlClient.Callsign, control.Callsign) {
							sp.Aircraft[ac.Callsign].DatablockType = PartialDatablock
						} else {
							sp.Aircraft[ac.Callsign].DatablockType = FullDatablock
						}
						sp.redirectHandoff(ctx, ac.Callsign, control.Callsign)
						status.clear = true
					} else if err := sp.handoffTrack(ctx, ac.Callsign, cmd); err == nil {
						status.clear = true
					} else {
						status.err = err
					}
				} else {
					// Try setting the scratchpad
					if err := sp.setScratchpad(ctx, ac.Callsign, cmd, false, true); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
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
				if err := sp.handoffTrack(ctx, ac.Callsign, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
			}
			return

		case CommandModeVFRPlan:
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
					status.output, status.err = sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac)
					if status.err == nil {
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "L": // Leader line
				if err := sp.setLeaderLine(ctx, ac, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
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
					clickedGhost := ghost != nil && ghostDistance < acDistance
					if clickedGhost {
						state.Ghost.State = GhostStateSuppressed
					} else if slices.ContainsFunc(ghosts, func(g *av.GhostAircraft) bool { return g.Callsign == ac.Callsign }) {
						state.Ghost.State = GhostStateRegular
					} else {
						status.err = ErrSTARSIllegalTrack
					}
				} else if cmd == "*" {
					clickedGhost := ghost != nil && ghostDistance < acDistance
					if clickedGhost {
						// 6-27: display track information in preview area (as an arrival)
						if fp, err := sp.flightPlanSTARS(ctx.ControlClient.Controllers, ac); err != nil {
							status.err = err
						} else {
							status.output = fp
							status.clear = true
						}
					} else {
						// 6-29: force/unforce ghost qualification
						if !slices.ContainsFunc(ghosts, func(g *av.GhostAircraft) bool { return g.Callsign == ac.Callsign }) {
							status.err = ErrSTARSIllegalTrack
						} else {
							// Is it inside an enabled approach region?
							for i, pairState := range ps.CRDA.RunwayPairState {
								if !pairState.Enabled {
									continue
								}
								for j, rwyState := range pairState.RunwayState {
									if !rwyState.Enabled {
										continue
									}
									region := sp.ConvergingRunways[i].ApproachRegions[j]
									if lat, _ := region.Inside(state.TrackPosition(), float32(state.TrackAltitude()),
										ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation); lat {
										// All good. Whew
										if state.Ghost.State == GhostStateForced {
											state.Ghost.State = GhostStateRegular
										} else {
											state.Ghost.State = GhostStateForced
										}
										status.clear = true
										return
									}
								}
							}
							status.err = ErrSTARSIllegalTrack
						}
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Q":
				if cmd == "" {
					if ac.TrackingController != ctx.ControlClient.Callsign && ac.ControllingController != ctx.ControlClient.Callsign {
						status.err = ErrSTARSIllegalTrack
					} else {
						status.clear = true
						state.InhibitMSAW = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "R":
				switch cmd {
				case "":
					if ps.PTLAll || (ps.PTLOwn && ac.TrackingController == ctx.ControlClient.Callsign) {
						status.err = ErrSTARSIllegalTrack // 6-13
					} else {
						state.DisplayPTL = !state.DisplayPTL
						status.clear = true
					}
					return
				case "A": // toggle requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						if state.DisplayRequestedAltitude == nil {
							b := ps.DisplayRequestedAltitude // inherit from system-wide
							state.DisplayRequestedAltitude = &b
						}
						*state.DisplayRequestedAltitude = !*state.DisplayRequestedAltitude
						status.clear = true
					}
					return
				case "AE": // enable requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						b := true
						state.DisplayRequestedAltitude = &b
						status.clear = true
					}
					return
				case "AI": // inhibit requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						b := false
						state.DisplayRequestedAltitude = &b
						status.clear = true
					}
					return
				}

			case "V":
				if cmd == "" {
					if ac.TrackingController != ctx.ControlClient.Callsign && ac.ControllingController != ctx.ControlClient.Callsign {
						status.err = ErrSTARSIllegalTrack
					} else {
						state.DisableMSAW = !state.DisableMSAW
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Y":
				isSecondary := false
				if len(cmd) > 0 && cmd[0] == '+' {
					isSecondary = true
					cmd = cmd[1:]
				}

				if cmd == "" {
					// Clear pilot reported altitude and scratchpad
					state.pilotAltitude = 0
					if err := sp.setScratchpad(ctx, ac.Callsign, "", isSecondary, false); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
					return
				} else {
					// Is it an altitude or a scratchpad update?
					if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
						state.pilotAltitude = alt * 100
						status.clear = true
					} else {
						if err := sp.setScratchpad(ctx, ac.Callsign, cmd, isSecondary, false); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
					}
					return
				}
			case "O": //Pointout history
				if ac.TrackingController != ctx.ControlClient.Callsign {
					status.err = ErrSTARSIllegalTrack
					return
				}

				status.output = strings.Join(ac.PointOutHistory, " ")
				status.clear = true
				return
			}

		case CommandModeFlightData:
			if cmd == "" {
				status.clear = true
				status.err = ctx.ControlClient.SetSquawkAutomatic(ac.Callsign)
				return
			} else {
				if squawk, err := av.ParseSquawk(cmd); err == nil {
					status.err = ctx.ControlClient.SetSquawk(ac.Callsign, squawk)
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
					if ac, _ := sp.tryGetClosestAircraft(ctx, pw, transforms); ac != nil {
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
			sp.wipRBL = &STARSRangeBearingLine{}
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
				v = math.Abs(v)
				d := int(v)
				v = 60 * (v - float32(d))
				return fmt.Sprintf("%d %.2f", d, v)
			}
			status.output = fmt.Sprintf("%s / %s", format(pll.Latitude()), format(pll.Longitude()))
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

// Returns the cardinal-ordinal direction associated with the numbpad keys,
// interpreting 5 as the center; (nil, true) is returned for '5' and
// (nil, false) is returned for an invalid key.
func numpadToDirection(key byte) (*math.CardinalOrdinalDirection, bool) {
	var dir math.CardinalOrdinalDirection
	switch key {
	case '1':
		dir = math.CardinalOrdinalDirection(math.SouthWest)
		return &dir, true
	case '2':
		dir = math.CardinalOrdinalDirection(math.South)
		return &dir, true
	case '3':
		dir = math.CardinalOrdinalDirection(math.SouthEast)
		return &dir, true
	case '4':
		dir = math.CardinalOrdinalDirection(math.West)
		return &dir, true
	case '5':
		return nil, true
	case '6':
		dir = math.CardinalOrdinalDirection(math.East)
		return &dir, true
	case '7':
		dir = math.CardinalOrdinalDirection(math.NorthWest)
		return &dir, true
	case '8':
		dir = math.CardinalOrdinalDirection(math.North)
		return &dir, true
	case '9':
		dir = math.CardinalOrdinalDirection(math.NorthEast)
		return &dir, true
	}

	return nil, false
}

func rblSecondClickHandler(ctx *PaneContext, sp *STARSPane) func([2]float32, ScopeTransformations) (status STARSCommandStatus) {
	return func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
		if sp.wipRBL == nil {
			// this shouldn't happen, but let's not crash if it does...
			return
		}

		rbl := *sp.wipRBL
		sp.wipRBL = nil
		if ac, _ := sp.tryGetClosestAircraft(ctx, pw, transforms); ac != nil {
			rbl.P[1].Callsign = ac.Callsign
		} else {
			rbl.P[1].Loc = transforms.LatLongFromWindowP(pw)
		}
		sp.RangeBearingLines = append(sp.RangeBearingLines, rbl)
		status.clear = true
		return
	}
}

func (sp *STARSPane) DrawDCB(ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) math.Extent2D {
	ps := &sp.CurrentPreferenceSet

	// Find a scale factor so that the buttons all fit in the window, if necessary
	const NumDCBSlots = 20
	// Sigh; on windows we want the button size in pixels on high DPI displays
	ds := util.Select(runtime.GOOS == "windows", ctx.Platform.DPIScale(), float32(1))
	var buttonScale float32
	// Scale based on width or height available depending on DCB position
	if ps.DCBPosition == DCBPositionTop || ps.DCBPosition == DCBPositionBottom {
		buttonScale = math.Min(ds, (ds*ctx.PaneExtent.Width()-4)/(NumDCBSlots*STARSButtonSize))
	} else {
		buttonScale = math.Min(ds, (ds*ctx.PaneExtent.Height()-4)/(NumDCBSlots*STARSButtonSize))
	}

	sp.StartDrawDCB(ctx, buttonScale, transforms, cb)

	switch sp.activeDCBMenu {
	case DCBMenuMain:
		sp.DrawDCBSpinner(ctx, MakeRadarRangeSpinner(&ps.Range), CommandModeRange,
			STARSButtonFull, buttonScale)
		sp.STARSPlaceButton(ctx, "PLACE\nCNTR", STARSButtonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.Center = transforms.LatLongFromWindowP(pw)
				ps.CurrentCenter = ps.Center
				sp.weatherRadar.UpdateCenter(ps.Center)
				status.clear = true
				return
			})
		ps.OffCenter = ps.CurrentCenter != ps.Center
		if STARSToggleButton(ctx, "OFF\nCNTR", &ps.OffCenter, STARSButtonHalfVertical, buttonScale) {
			ps.CurrentCenter = ps.Center
		}
		sp.DrawDCBSpinner(ctx, MakeRangeRingRadiusSpinner(&ps.RangeRingRadius), CommandModeRangeRings,
			STARSButtonFull, buttonScale)
		sp.STARSPlaceButton(ctx, "PLACE\nRR", STARSButtonHalfVertical, buttonScale,
			func(pw [2]float32, transforms ScopeTransformations) (status STARSCommandStatus) {
				ps.RangeRingsCenter = transforms.LatLongFromWindowP(pw)
				status.clear = true
				return
			})
		if STARSSelectButton(ctx, "RR\nCNTR", STARSButtonHalfVertical, buttonScale) {
			cw := [2]float32{ctx.PaneExtent.Width() / 2, ctx.PaneExtent.Height() / 2}
			ps.RangeRingsCenter = transforms.LatLongFromWindowP(cw)
		}
		if STARSSelectButton(ctx, "MAPS", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMaps
		}
		videoMaps, _ := ctx.ControlClient.GetVideoMaps()
		for i := 0; i < 6; i++ {
			// Maps are given left->right, top->down, but we draw the
			// buttons top->down, left->right, so the indexing is a little
			// funny.
			idx := util.Select(i&1 == 0, i/2, 3+i/2)
			m := videoMaps[idx]
			text := util.Select(m.Id == 0, "", fmt.Sprintf("%d\n%s", m.Id, m.Label))
			STARSToggleButton(ctx, text, &ps.DisplayVideoMap[idx], STARSButtonHalfVertical, buttonScale)
		}
		for i := range ps.DisplayWeatherLevel {
			STARSToggleButton(ctx, "WX"+strconv.Itoa(i), &ps.DisplayWeatherLevel[i], STARSButtonHalfHorizontal, buttonScale)
		}
		if STARSSelectButton(ctx, "BRITE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuBrite
		}
		sp.DrawDCBSpinner(ctx, MakeLeaderLineDirectionSpinner(&ps.LeaderLineDirection), CommandModeNone,
			STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeLeaderLineLengthSpinner(&ps.LeaderLineLength), CommandModeLDR,
			STARSButtonHalfVertical, buttonScale)

		if STARSSelectButton(ctx, "CHAR\nSIZE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuCharSize
		}
		STARSDisabledButton(ctx, "MODE\nFSL", STARSButtonFull, buttonScale)
		if STARSSelectButton(ctx, "PREF\n"+ps.Name, STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuPref
		}

		site := sp.radarSiteId(ctx.ControlClient.RadarSites)
		if len(ctx.ControlClient.RadarSites) == 0 {
			STARSDisabledButton(ctx, "SITE\n"+site, STARSButtonFull, buttonScale)
		} else {
			if STARSSelectButton(ctx, "SITE\n"+site, STARSButtonFull, buttonScale) {
				sp.activeDCBMenu = DCBMenuSite
			}
		}
		if STARSSelectButton(ctx, "SSA\nFILTER", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuSSAFilter
		}
		if STARSSelectButton(ctx, "GI TEXT\nFILTER", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuGITextFilter
		}
		if STARSSelectButton(ctx, "SHIFT", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuAux
		}

	case DCBMenuAux:
		STARSDisabledButton(ctx, "VOL\n10", STARSButtonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("HISTORY\n", &ps.RadarTrackHistory, 0, 10),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeHistoryRateSpinner(&ps.RadarTrackHistoryRate),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "CURSOR\nHOME", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "CSR SPD\n4", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "MAP\nUNCOR", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "UNCOR", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "BEACON\nMODE-2", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "RTQC", STARSButtonFull, buttonScale)
		STARSDisabledButton(ctx, "MCP", STARSButtonFull, buttonScale)
		top := ps.DCBPosition == DCBPositionTop
		if STARSToggleButton(ctx, "DCB\nTOP", &top, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionTop
		}
		left := ps.DCBPosition == DCBPositionLeft
		if STARSToggleButton(ctx, "DCB\nLEFT", &left, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionLeft
		}
		right := ps.DCBPosition == DCBPositionRight
		if STARSToggleButton(ctx, "DCB\nRIGHT", &right, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionRight
		}
		bottom := ps.DCBPosition == DCBPositionBottom
		if STARSToggleButton(ctx, "DCB\nBOTTOM", &bottom, STARSButtonHalfVertical, buttonScale) {
			ps.DCBPosition = DCBPositionBottom
		}
		sp.DrawDCBSpinner(ctx, MakePTLLengthSpinner(&ps.PTLLength), CommandModeNone, STARSButtonFull, buttonScale)
		if ps.PTLLength > 0 {
			if STARSToggleButton(ctx, "PTL OWN", &ps.PTLOwn, STARSButtonHalfVertical, buttonScale) && ps.PTLOwn {
				ps.PTLAll = false
			}
			if STARSToggleButton(ctx, "PTL ALL", &ps.PTLAll, STARSButtonHalfVertical, buttonScale) && ps.PTLAll {
				ps.PTLOwn = false
			}
		} else {
			STARSDisabledButton(ctx, "PTL OWN", STARSButtonHalfVertical, buttonScale)
			STARSDisabledButton(ctx, "PTL ALL", STARSButtonHalfVertical, buttonScale)

		}
		sp.DrawDCBSpinner(ctx, MakeDwellModeSpinner(&ps.DwellMode), CommandModeNone, STARSButtonFull, buttonScale)
		if STARSSelectButton(ctx, "TPA/\nATPA", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuTPA
		}
		if STARSSelectButton(ctx, "SHIFT", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuMaps:
		if STARSSelectButton(ctx, "DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}
		if STARSSelectButton(ctx, "CLR ALL", STARSButtonHalfVertical, buttonScale) {
			for i := range ps.DisplayVideoMap {
				ps.DisplayVideoMap[i] = false
			}
			ps.SystemMapVisible = make(map[int]interface{})
		}
		videoMaps, _ := ctx.ControlClient.GetVideoMaps()
		for i := 0; i < sim.NumSTARSMaps-6; i++ {
			// Indexing is tricky both because we are skipping the first 6
			// maps, which are shown in the main DCB, but also because we
			// draw top->down, left->right while the maps are specified
			// left->right, top->down...
			idx := util.Select(i&1 == 0, 6+i/2, 22+i/2)
			m := videoMaps[idx]
			text := util.Select(m.Id == 0, "", fmt.Sprintf("%d\n%s", m.Id, m.Label))
			STARSToggleButton(ctx, text, &ps.DisplayVideoMap[idx], STARSButtonHalfVertical, buttonScale)
		}

		geoMapsSelected := ps.VideoMapsList.Selection == VideoMapsGroupGeo && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "GEO\nMAPS", &geoMapsSelected, STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupGeo
			ps.VideoMapsList.Visible = geoMapsSelected
		}
		STARSDisabledButton(ctx, "AIRPORT", STARSButtonHalfVertical, buttonScale)
		sysProcSelected := ps.VideoMapsList.Selection == VideoMapsGroupSysProc && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "SYS\nPROC", &sysProcSelected, STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupSysProc
			ps.VideoMapsList.Visible = sysProcSelected
		}
		currentMapsSelected := ps.VideoMapsList.Selection == VideoMapsGroupCurrent && ps.VideoMapsList.Visible
		if STARSToggleButton(ctx, "CURRENT", &currentMapsSelected, STARSButtonHalfVertical, buttonScale) {
			ps.VideoMapsList.Selection = VideoMapsGroupCurrent
			ps.VideoMapsList.Visible = currentMapsSelected
		}

	case DCBMenuBrite:
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("DCB", &ps.Brightness.DCB, 25, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("BKC", &ps.Brightness.BackgroundContrast, 0, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("MPA", &ps.Brightness.VideoGroupA, 5, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("MPB", &ps.Brightness.VideoGroupB, 5, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("FDB", &ps.Brightness.FullDatablocks, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("LST", &ps.Brightness.Lists, 25, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("POS", &ps.Brightness.Positions, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("LDB", &ps.Brightness.LimitedDatablocks, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("OTH", &ps.Brightness.OtherTracks, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("TLS", &ps.Brightness.Lines, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("RR", &ps.Brightness.RangeRings, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("CMP", &ps.Brightness.Compass, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("BCN", &ps.Brightness.BeaconSymbols, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("PRI", &ps.Brightness.PrimarySymbols, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("HST", &ps.Brightness.History, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		// The STARS manual, p.4-74 actually says that weather can't go to OFF... FIXME?
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("WX", &ps.Brightness.Weather, 5, true),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeBrightnessSpinner("WXC", &ps.Brightness.WxContrast, 5, false),
			CommandModeNone, STARSButtonHalfVertical, buttonScale)
		if ps.Brightness.Weather != 0 {
			sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center, ctx.Renderer, ctx.Lg)
		} else {
			// Don't fetch weather maps if they're not going to be displayed.
			sp.weatherRadar.Deactivate()
		}
		if STARSSelectButton(ctx, "DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuCharSize:
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("DATA\nBLOCKS\n", &ps.CharSize.Datablocks, 0, 5),
			CommandModeNone, STARSButtonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("LISTS\n", &ps.CharSize.Lists, 0, 5),
			CommandModeNone, STARSButtonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("DCB\n", &ps.CharSize.DCB, 0, 2),
			CommandModeNone, STARSButtonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("TOOLS\n", &ps.CharSize.Tools, 0, 5),
			CommandModeNone, STARSButtonFull, buttonScale)
		sp.DrawDCBSpinner(ctx, MakeIntegerRangeSpinner("POS\n", &ps.CharSize.PositionSymbols, 0, 5),
			CommandModeNone, STARSButtonFull, buttonScale)
		if STARSSelectButton(ctx, "DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuPref:
		for i := range sp.PreferenceSets {
			text := fmt.Sprintf("%d\n%s", i+1, sp.PreferenceSets[i].Name)
			flags := STARSButtonHalfVertical
			if i == sp.SelectedPreferenceSet {
				flags = flags | STARSButtonSelected
			}
			if STARSSelectButton(ctx, text, flags, buttonScale) {
				// Make this one current
				sp.SelectedPreferenceSet = i
				sp.CurrentPreferenceSet = sp.PreferenceSets[i]
				sp.weatherRadar.Activate(sp.CurrentPreferenceSet.Center, ctx.Renderer, ctx.Lg)
			}
		}
		for i := len(sp.PreferenceSets); i < NumSTARSPreferenceSets; i++ {
			STARSDisabledButton(ctx, fmt.Sprintf("%d\n", i+1), STARSButtonHalfVertical, buttonScale)
		}

		if STARSSelectButton(ctx, "DEFAULT", STARSButtonHalfVertical, buttonScale) {
			sp.CurrentPreferenceSet = sp.MakePreferenceSet("", &ctx.ControlClient.State)
		}
		STARSDisabledButton(ctx, "FSSTARS", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "RESTORE", STARSButtonHalfVertical, buttonScale) {
			// TODO: restore settings in effect when entered the Pref sub-menu
		}

		validSelection := sp.SelectedPreferenceSet != -1 && sp.SelectedPreferenceSet < len(sp.PreferenceSets)
		if validSelection {
			if STARSSelectButton(ctx, "SAVE", STARSButtonHalfVertical, buttonScale) {
				sp.PreferenceSets[sp.SelectedPreferenceSet] = sp.CurrentPreferenceSet
				// FIXME? globalConfig.Save()
			}
		} else {
			STARSDisabledButton(ctx, "SAVE", STARSButtonHalfVertical, buttonScale)
		}
		STARSDisabledButton(ctx, "CHG PIN", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "SAVE AS", STARSButtonHalfVertical, buttonScale) {
			// A command mode handles prompting for the name and then saves
			// when enter is pressed.
			sp.commandMode = CommandModeSavePrefAs
		}
		if validSelection {
			if STARSSelectButton(ctx, "DELETE", STARSButtonHalfVertical, buttonScale) {
				sp.PreferenceSets = util.DeleteSliceElement(sp.PreferenceSets, sp.SelectedPreferenceSet)
			}
		} else {
			STARSDisabledButton(ctx, "DELETE", STARSButtonHalfVertical, buttonScale)
		}

		if STARSSelectButton(ctx, "DONE", STARSButtonHalfVertical, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSite:
		for _, id := range util.SortedMapKeys(ctx.ControlClient.RadarSites) {
			site := ctx.ControlClient.RadarSites[id]
			label := " " + site.Char + " " + "\n" + id
			selected := ps.RadarSiteSelected == id
			if STARSToggleButton(ctx, label, &selected, STARSButtonFull, buttonScale) {
				if selected {
					ps.RadarSiteSelected = id
				} else {
					ps.RadarSiteSelected = ""
				}
			}
		}
		// Fill extras with empty disabled buttons
		for i := len(ctx.ControlClient.RadarSites); i < 15; i++ {
			STARSDisabledButton(ctx, "", STARSButtonFull, buttonScale)
		}
		multi := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeMulti
		if STARSToggleButton(ctx, "MULTI", &multi, STARSButtonFull, buttonScale) && multi {
			ps.RadarSiteSelected = ""
			if ps.FusedRadarMode {
				sp.discardTracks = true
			}
			ps.FusedRadarMode = false
		}
		fused := sp.radarMode(ctx.ControlClient.RadarSites) == RadarModeFused
		if STARSToggleButton(ctx, "FUSED", &fused, STARSButtonFull, buttonScale) && fused {
			ps.RadarSiteSelected = ""
			ps.FusedRadarMode = true
			sp.discardTracks = true
		}
		if STARSSelectButton(ctx, "DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuSSAFilter:
		STARSToggleButton(ctx, "ALL", &ps.SSAList.Filter.All, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "WX", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "TIME", &ps.SSAList.Filter.Time, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "ALTSTG", &ps.SSAList.Filter.Altimeter, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "STATUS", &ps.SSAList.Filter.Status, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "PLAN", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "RADAR", &ps.SSAList.Filter.Radar, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "CODES", &ps.SSAList.Filter.Codes, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "SPC", &ps.SSAList.Filter.SpecialPurposeCodes, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "SYS OFF", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "RANGE", &ps.SSAList.Filter.Range, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "PTL", &ps.SSAList.Filter.PredictedTrackLines, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "ALT FIL", &ps.SSAList.Filter.AltitudeFilters, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "NAS I/F", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "AIRPORT", &ps.SSAList.Filter.AirportWeather, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "OP MODE", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton(ctx, "TT", STARSButtonHalfVertical, buttonScale)      // ?? TODO
		STARSDisabledButton(ctx, "WX HIST", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "QL", &ps.SSAList.Filter.QuickLookPositions, STARSButtonHalfVertical, buttonScale)
		STARSToggleButton(ctx, "TW OFF", &ps.SSAList.Filter.DisabledTerminal, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "CON/CPL", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSDisabledButton(ctx, "OFF IND", STARSButtonHalfVertical, buttonScale) // ?? TODO
		STARSToggleButton(ctx, "CRDA", &ps.SSAList.Filter.ActiveCRDAPairs, STARSButtonHalfVertical, buttonScale)
		STARSDisabledButton(ctx, "", STARSButtonHalfVertical, buttonScale)
		if STARSSelectButton(ctx, "DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuGITextFilter:
		STARSToggleButton(ctx, "MAIN", &ps.SSAList.Filter.Text.Main, STARSButtonHalfVertical, buttonScale)
		for i := range ps.SSAList.Filter.Text.GI {
			STARSToggleButton(ctx, fmt.Sprintf("GI %d", i+1), &ps.SSAList.Filter.Text.GI[i],
				STARSButtonHalfVertical, buttonScale)
		}
		if STARSSelectButton(ctx, "DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuMain
		}

	case DCBMenuTPA:
		onoff := func(b bool) string { return util.Select(b, "ENABLED", "INHIBTD") }
		if STARSSelectButton(ctx, "A/TPA\nMILEAGE\n"+onoff(ps.DisplayTPASize), STARSButtonFull, buttonScale) {
			ps.DisplayTPASize = !ps.DisplayTPASize
		}
		if STARSSelectButton(ctx, "INTRAIL\nDIST\n"+onoff(ps.DisplayATPAInTrailDist), STARSButtonFull, buttonScale) {
			ps.DisplayATPAInTrailDist = !ps.DisplayATPAInTrailDist
		}
		if STARSSelectButton(ctx, "ALERT\nCONES\n"+onoff(ps.DisplayATPAWarningAlertCones), STARSButtonFull, buttonScale) {
			ps.DisplayATPAWarningAlertCones = !ps.DisplayATPAWarningAlertCones
		}
		if STARSSelectButton(ctx, "MONITOR\nCONES\n"+onoff(ps.DisplayATPAMonitorCones), STARSButtonFull, buttonScale) {
			ps.DisplayATPAMonitorCones = !ps.DisplayATPAMonitorCones
		}
		if STARSSelectButton(ctx, "DONE", STARSButtonFull, buttonScale) {
			sp.activeDCBMenu = DCBMenuAux
		}
	}

	sp.EndDrawDCB()

	sz := starsButtonSize(STARSButtonFull, buttonScale)
	paneExtent := ctx.PaneExtent
	switch ps.DCBPosition {
	case DCBPositionTop:
		paneExtent.P1[1] -= sz[1]

	case DCBPositionLeft:
		paneExtent.P0[0] += sz[0]

	case DCBPositionRight:
		paneExtent.P1[0] -= sz[0]

	case DCBPositionBottom:
		paneExtent.P0[1] += sz[1]
	}

	return paneExtent
}

func (sp *STARSPane) drawSystemLists(aircraft []*av.Aircraft, ctx *PaneContext, paneExtent math.Extent2D,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.CurrentPreferenceSet

	transforms.LoadWindowViewingMatrices(cb)

	font := sp.systemFont[ps.CharSize.Lists]
	style := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}
	alertStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

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
	case CommandModeVFRPlan:
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

	formatMETAR := func(ap string, metar *av.METAR) string {
		alt := strings.TrimPrefix(metar.Altimeter, "A")
		if len(alt) == 4 {
			alt = alt[:2] + "." + alt[2:]
		}
		return stripK(ap) + " " + alt
	}

	if ps.SSAList.Visible {
		pw := normalizedToWindow(ps.SSAList.Position)
		x := pw[0]
		newline := func() {
			pw[0] = x
			pw[1] -= float32(font.Size)
		}

		// Inverted red triangle and green box...
		trid := renderer.GetColoredTrianglesDrawBuilder()
		defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
		ld := renderer.GetColoredLinesDrawBuilder()
		defer renderer.ReturnColoredLinesDrawBuilder(ld)

		pIndicator := math.Add2f(pw, [2]float32{5, 0})
		tv := math.EquilateralTriangleVertices(7)
		for i := range tv {
			tv[i] = math.Add2f(pIndicator, math.Scale2f(tv[i], -1))
		}
		trid.AddTriangle(tv[0], tv[1], tv[2], ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor))
		trid.GenerateCommands(cb)

		square := [][2]float32{[2]float32{-5, -5}, [2]float32{5, -5}, [2]float32{5, 5}, [2]float32{-5, 5}}
		square = util.MapSlice(square, func(p [2]float32) [2]float32 { return math.Add2f(p, pIndicator) })
		ld.AddLineLoop(ps.Brightness.Lists.ScaleRGB(STARSListColor), square)
		ld.GenerateCommands(cb)

		pw[1] -= 10

		filter := ps.SSAList.Filter
		if filter.All || filter.Time || filter.Altimeter {
			text := ""
			if filter.All || filter.Time {
				text += ctx.ControlClient.CurrentTime().UTC().Format("1504/05 ")
			}
			if filter.All || filter.Altimeter {
				if metar := ctx.ControlClient.METAR[ctx.ControlClient.PrimaryAirport]; metar != nil {
					text += formatMETAR(ctx.ControlClient.PrimaryAirport, metar)
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
				if ctx.ControlClient.Connected() {
					pw = td.AddText("OK/OK/NA ", pw, style)
				} else {
					pw = td.AddText("NA/NA/NA ", pw, alertStyle)
				}
			}
			if filter.All || filter.Radar {
				pw = td.AddText(sp.radarSiteId(ctx.ControlClient.RadarSites), pw, style)
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
			codes := make(map[string]interface{})
			for _, ac := range aircraft {
				for code := range ac.SPCOverrides {
					codes[code] = nil
				}
				if ok, code := av.SquawkIsSPC(ac.Squawk); ok {
					codes[code] = nil
				}
			}

			if len(codes) > 0 {
				td.AddText(strings.Join(util.SortedMapKeys(codes), " "), pw, alertStyle)
				newline()
			}
		}

		if filter.All || filter.Range || filter.PredictedTrackLines {
			text := ""
			if filter.All || filter.Range {
				text += fmt.Sprintf("%dNM ", int(ps.Range))
			}
			if (filter.All || filter.PredictedTrackLines) && ps.PTLLength > 0 {
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
			airports := util.SortedMapKeys(ctx.ControlClient.Airports)
			// Sort via 1. primary? 2. tower list index, 3. alphabetic
			sort.Slice(airports, func(i, j int) bool {
				if airports[i] == ctx.ControlClient.PrimaryAirport {
					return true
				} else if airports[j] == ctx.ControlClient.PrimaryAirport {
					return false
				} else {
					a, b := ctx.ControlClient.Airports[airports[i]], ctx.ControlClient.Airports[airports[j]]
					ai := util.Select(a.TowerListIndex != 0, a.TowerListIndex, 1000)
					bi := util.Select(b.TowerListIndex != 0, b.TowerListIndex, 1000)
					if ai != bi {
						return ai < bi
					}
				}
				return airports[i] < airports[j]
			})

			// 2-78: apparently it's limited to 6 airports; there are also
			// some nuances about automatically-entered versus manually
			// entered, stale entries, and a possible "*" for airports
			// where "instrument approach statistics are maintained".
			var altimeters []string
			for _, icao := range airports {
				if metar := ctx.ControlClient.METAR[icao]; metar != nil {
					altimeters = append(altimeters, formatMETAR(icao, metar))
				}
			}
			for len(altimeters) >= 3 {
				pw = td.AddText(strings.Join(altimeters[:3], " "), pw, style)
				altimeters = altimeters[3:]
				newline()
			}
			if len(altimeters) > 0 {
				pw = td.AddText(strings.Join(altimeters, " "), pw, style)
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
				pos := util.MapSlice(ps.QuickLookPositions,
					func(q QuickLookPosition) string {
						return q.Id + util.Select(q.Plus, "+", "")
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
				text += util.Select(crda.Mode == CRDAModeStagger, "S ", "T ")
				text += sp.ConvergingRunways[i].Airport + " "
				text += sp.ConvergingRunways[i].getRunwaysString()

				pw = td.AddText(text, pw, style)
				newline()
			}
		}
	}

	var text strings.Builder
	if ps.VFRList.Visible {
		text.Reset()
		vfr := make(map[int]*av.Aircraft)
		// Find all untracked av.VFR aircraft
		for _, ac := range aircraft {
			if ac.Squawk == av.Squawk(0o1200) && ac.TrackingController == "" {
				vfr[sp.getAircraftIndex(ac)] = ac
			}
		}

		text.WriteString("VFR LIST\n")
		if len(vfr) > ps.VFRList.Lines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.VFRList.Lines, len(vfr)))
		}
		for i, acIdx := range util.SortedMapKeys(vfr) {
			ac := vfr[acIdx]
			text.WriteString(fmt.Sprintf("%2d %-7s av.VFR\n", acIdx, ac.Callsign))

			// Limit to the user limit
			if i == ps.VFRList.Lines {
				break
			}
		}

		drawList(text.String(), ps.VFRList.Position)
	}

	if ps.TABList.Visible {
		text.Reset()
		dep := make(map[int]*av.Aircraft)
		// Untracked departures departing from one of our airports
		for _, ac := range aircraft {
			if fp := ac.FlightPlan; fp != nil && ac.TrackingController == "" {
				if ap := ctx.ControlClient.DepartureAirports[fp.DepartureAirport]; ap != nil {
					dep[sp.getAircraftIndex(ac)] = ac
					break
				}
			}
		}

		text.WriteString("FLIGHT PLAN\n")
		if len(dep) > ps.TABList.Lines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.TABList.Lines, len(dep)))
		}
		for i, acIdx := range util.SortedMapKeys(dep) {
			ac := dep[acIdx]
			text.WriteString(fmt.Sprintf("%2d %-7s %s\n", acIdx, ac.Callsign, ac.Squawk.String()))

			// Limit to the user limit
			if i == ps.TABList.Lines {
				break
			}
		}

		drawList(text.String(), ps.TABList.Position)
	}

	if ps.AlertList.Visible {
		text.Reset()
		var lists []string
		n := 0 // total number of aircraft in the mix
		if !ps.DisableMSAW {
			lists = append(lists, "LA")
			for _, ac := range aircraft {
				if sp.Aircraft[ac.Callsign].MSAW {
					n++
				}
			}
		}
		if !ps.DisableCAWarnings {
			lists = append(lists, "CA")
			n += len(sp.CAAircraft)
		}

		if len(lists) > 0 {
			text.WriteString(strings.Join(lists, "/") + "\n")
			if n > ps.AlertList.Lines {
				text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.AlertList.Lines, n))
			}

			// LA
			if !ps.DisableMSAW {
				for _, ac := range aircraft {
					if n == 0 {
						break
					}
					if sp.Aircraft[ac.Callsign].MSAW {
						text.WriteString(fmt.Sprintf("%-14s%03d LA\n", ac.Callsign, int((ac.Altitude()+50)/100)))
						n--
					}
				}
			}

			// CA
			if !ps.DisableCAWarnings {
				for _, pair := range sp.CAAircraft {
					if n == 0 {
						break
					}

					text.WriteString(fmt.Sprintf("%-17s CA\n", pair.Callsigns[0]+"*"+pair.Callsigns[1]))
					n--
				}
			}

			drawList(text.String(), ps.AlertList.Position)
		}
	}

	if ps.CoastList.Visible {
		text := "COAST/SUSPEND"
		// TODO
		drawList(text, ps.CoastList.Position)
	}

	if ps.VideoMapsList.Visible {
		text.Reset()
		format := func(m av.VideoMap, i int, vis bool) {
			text.WriteString(util.Select(vis, ">", " ") + " ")
			text.WriteString(fmt.Sprintf("%3d ", i))
			text.WriteString(fmt.Sprintf("%8s ", strings.ToUpper(m.Label)))
			text.WriteString(strings.ToUpper(m.Name) + "\n")
		}
		if ps.VideoMapsList.Selection == VideoMapsGroupGeo {
			text.WriteString("GEOGRAPHIC MAPS\n")
			videoMaps, _ := ctx.ControlClient.GetVideoMaps()
			for i, m := range videoMaps {
				format(m, m.Id, ps.DisplayVideoMap[i])
			}
		} else if ps.VideoMapsList.Selection == VideoMapsGroupSysProc {
			text.WriteString("PROCESSING AREAS\n")
			for _, index := range util.SortedMapKeys(sp.systemMaps) {
				_, vis := ps.SystemMapVisible[index]
				format(*sp.systemMaps[index], index, vis)
			}
		} else if ps.VideoMapsList.Selection == VideoMapsGroupCurrent {
			text.WriteString("MAPS\n")
			videoMaps, _ := ctx.ControlClient.GetVideoMaps()
			for i, vis := range ps.DisplayVideoMap {
				if vis {
					format(videoMaps[i], videoMaps[i].Id, vis)
				}
			}
		} else {
			ctx.Lg.Errorf("%d: unhandled VideoMapsList.Selection", ps.VideoMapsList.Selection)
		}

		drawList(text.String(), ps.VideoMapsList.Position)
	}

	if ps.CRDAStatusList.Visible {
		text.Reset()
		text.WriteString("CRDA STATUS\n")
		pairIndex := 0 // reset for each new airport
		currentAirport := ""
		var line strings.Builder
		for i, crda := range ps.CRDA.RunwayPairState {
			line.Reset()
			if !crda.Enabled {
				line.WriteString(" ")
			} else {
				line.WriteString(util.Select(crda.Mode == CRDAModeStagger, "S", "T"))
			}

			pair := sp.ConvergingRunways[i]
			ap := pair.Airport
			if ap != currentAirport {
				currentAirport = ap
				pairIndex = 1
			}

			line.WriteString(strconv.Itoa(pairIndex))
			line.WriteByte(' ')
			pairIndex++
			line.WriteString(ap + " ")
			line.WriteString(pair.getRunwaysString())
			if crda.Enabled {
				for line.Len() < 16 {
					line.WriteByte(' ')
				}
				ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
				line.WriteString(ctrl.SectorId)
			}
			line.WriteByte('\n')
			text.WriteString(line.String())
		}
		drawList(text.String(), ps.CRDAStatusList.Position)
	}

	// Figure out airport<-->tower list assignments. Sort the airports
	// according to their TowerListIndex, putting zero (i.e., unassigned)
	// indices at the end. Break ties alphabetically by airport name. The
	// first three then are assigned to the corresponding tower list.
	towerListAirports := util.SortedMapKeys(ctx.ControlClient.ArrivalAirports)
	sort.Slice(towerListAirports, func(a, b int) bool {
		ai := ctx.ControlClient.ArrivalAirports[towerListAirports[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := ctx.ControlClient.ArrivalAirports[towerListAirports[b]].TowerListIndex
		if bi == 0 {
			bi = 1000
		}
		if ai == bi {
			return a < b
		}
		return ai < bi
	})

	for i, tl := range ps.TowerLists {
		if !tl.Visible || i >= len(towerListAirports) {
			continue
		}

		text.Reset()
		ap := towerListAirports[i]
		loc := ctx.ControlClient.ArrivalAirports[ap].Location
		text.WriteString(stripK(ap) + " TOWER\n")
		m := make(map[float32]string)
		for _, ac := range aircraft {
			if ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == ap {
				dist := math.NMDistance2LL(loc, sp.Aircraft[ac.Callsign].TrackPosition())
				actype := ac.FlightPlan.TypeWithoutSuffix()
				actype = strings.TrimPrefix(actype, "H/")
				actype = strings.TrimPrefix(actype, "S/")
				// We'll punt on the chance that two aircraft have the
				// exact same distance to the airport...
				m[dist] = fmt.Sprintf("%-7s %s", ac.Callsign, actype)
			}
		}

		k := util.SortedMapKeys(m)
		if len(k) > tl.Lines {
			k = k[:tl.Lines]
		}

		for _, key := range k {
			text.WriteString(m[key] + "\n")
		}
		drawList(text.String(), tl.Position)
	}

	if ps.SignOnList.Visible {
		text.Reset()
		format := func(ctrl *av.Controller) {
			id := ctrl.SectorId
			if ctrl.FacilityIdentifier != "" && !ctrl.ERAMFacility {
				id = STARSTriangleCharacter + ctrl.FacilityIdentifier + id
			}
			text.WriteString(fmt.Sprintf("%4s", id) + " " + ctrl.Frequency.String() + " " +
				ctrl.Callsign + util.Select(ctrl.IsHuman, "*", "") + "\n")
		}

		// User first
		userCtrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
		if userCtrl != nil {
			format(userCtrl)
		}

		for _, callsign := range util.SortedMapKeys(ctx.ControlClient.Controllers) {
			if ctrl := ctx.ControlClient.Controllers[callsign]; ctrl != userCtrl {
				format(ctrl)
			}
		}

		drawList(text.String(), ps.SignOnList.Position)
	}

	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawCRDARegions(ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
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

func (sp *STARSPane) drawSelectedRoute(ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	if sp.drawRouteAircraft == "" {
		return
	}
	ac, ok := ctx.ControlClient.Aircraft[sp.drawRouteAircraft]
	if !ok {
		sp.drawRouteAircraft = ""
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	prev := ac.Position()
	for _, wp := range ac.Nav.Waypoints {
		ld.AddLine(prev, wp.Location)
		prev = wp.Location
	}

	ps := sp.CurrentPreferenceSet
	cb.LineWidth(3, ctx.Platform.DPIScale())
	cb.SetRGB(ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) datablockType(ctx *PaneContext, ac *av.Aircraft) DatablockType {
	state := sp.Aircraft[ac.Callsign]
	dt := state.DatablockType

	// TODO: when do we do a partial vs limited datablock?
	if ac.Squawk != ac.AssignedSquawk {
		dt = PartialDatablock
	}

	if ac.TrackingController == "" {
		dt = LimitedDatablock
	}

	if ac.TrackingController == ctx.ControlClient.Callsign {
		// it's under our control
		dt = FullDatablock
	}
	if ac.ForceQLControllers != nil && slices.Contains(ac.ForceQLControllers, ctx.ControlClient.Callsign) {
		dt = FullDatablock
	}

	if ac.HandoffTrackController == ctx.ControlClient.Callsign && ac.RedirectedHandoff.RedirectedTo == "" {
		// it's being handed off to us
		dt = FullDatablock
	}

	if sp.haveActiveWarnings(ctx, ac) {
		dt = FullDatablock
	}

	// Point outs are FDB until acked.
	if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
		dt = FullDatablock
	}
	if state.PointedOut {
		dt = FullDatablock
	}
	if state.ForceQL {
		dt = FullDatablock
	}
	if len(ac.RedirectedHandoff.Redirector) > 0 {
		if ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
			dt = FullDatablock
		}
	}

	if ac.RedirectedHandoff.OriginalOwner == ctx.ControlClient.Callsign {
		dt = FullDatablock
	}

	// Quicklook
	ps := sp.CurrentPreferenceSet
	if ps.QuickLookAll {
		dt = FullDatablock
	} else if slices.ContainsFunc(ps.QuickLookPositions,
		func(q QuickLookPosition) bool { return q.Callsign == ac.TrackingController }) {
		dt = FullDatablock
	}

	return dt
}

func (sp *STARSPane) drawTracks(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trackBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trackBuilder)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	// TODO: square icon if it's squawking a beacon code we're monitoring

	// Update cached command buffers for tracks
	sp.fusedTrackVertices = getTrackVertices(ctx, sp.getTrackSize(ctx, transforms))

	scale := util.Select(runtime.GOOS == "windows", ctx.Platform.DPIScale(), float32(1))

	now := ctx.ControlClient.SimTime
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		/* TODO: Having the scope char reflect who STARS thinks is tracking the target. This will probably take something like a map[string]struct{}
		in the World struct, which will contain all of the facility specific information. This is where local flight plans will be stored, and any other
		local information that a STARS facility may contain
		*/

		trackId := "*"
		if ac.TrackingController != "" {
			trackId = "?"
			if ctrl, ok := ctx.ControlClient.Controllers[ac.TrackingController]; ok && ctrl != nil {
				trackId = ctrl.Scope
			}
		}

		// "cheat" by using ac.Heading() if we don't yet have two radar tracks to compute the
		// heading with; this makes things look better when we first see a track or when
		// restarting a simulation...
		heading := util.Select(state.HaveHeading(),
			state.TrackHeading(ac.NmPerLongitude())+ac.MagneticVariation(), ac.Heading())

		sp.drawRadarTrack(ac, state, heading, ctx, transforms, trackId, trackBuilder,
			ld, trid, td, scale)
	}

	transforms.LoadWindowViewingMatrices(cb)
	trackBuilder.GenerateCommands(cb)

	transforms.LoadLatLongViewingMatrices(cb)
	trid.GenerateCommands(cb)
	cb.LineWidth(1, ctx.Platform.DPIScale())
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) getTrackSize(ctx *PaneContext, transforms ScopeTransformations) float32 {
	var size float32 = 13 // base track size
	e := transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)
	var distance float32 = 0.3623 // Around 2200 feet in nm
	if distance/e > 13 {
		size = distance / e
	}
	return size
}

func (sp *STARSPane) getGhostAircraft(aircraft []*av.Aircraft, ctx *PaneContext) []*av.GhostAircraft {
	var ghosts []*av.GhostAircraft
	ps := sp.CurrentPreferenceSet
	now := ctx.ControlClient.SimTime

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

			trackId := util.Select(pairState.Mode == CRDAModeStagger, sp.ConvergingRunways[i].StaggerSymbol,
				sp.ConvergingRunways[i].TieSymbol)

			offset := util.Select(pairState.Mode == CRDAModeTie, sp.ConvergingRunways[i].TieOffset, float32(0))

			for _, ac := range aircraft {
				state := sp.Aircraft[ac.Callsign]
				if state.LostTrack(now) {
					continue
				}

				// Create a ghost track if appropriate, add it to the
				// ghosts slice, and draw its radar track.
				force := state.Ghost.State == GhostStateForced || ps.CRDA.ForceAllGhosts
				heading := util.Select(state.HaveHeading(), state.TrackHeading(ac.NmPerLongitude()),
					ac.Heading())

				ghost := region.TryMakeGhost(ac.Callsign, state.track, heading, ac.Scratchpad, force,
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

func (sp *STARSPane) drawGhosts(ghosts []*av.GhostAircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.OtherTracks.ScaleRGB(STARSGhostColor)
	trackFont := sp.systemFont[ps.CharSize.PositionSymbols]
	trackStyle := renderer.TextStyle{Font: trackFont, Color: color, LineSpacing: 0}
	datablockFont := sp.systemFont[ps.CharSize.Datablocks]
	datablockStyle := renderer.TextStyle{Font: datablockFont, Color: color, LineSpacing: 0}

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
			datablockText += state.CWTCategory
		} else {
			// The full datablock ain't much more...
			datablockText = ghost.Callsign + "\n" + fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)
		}
		w, h := datablockFont.BoundText(datablockText, datablockStyle.LineSpacing)
		datablockOffset := sp.getDatablockOffset([2]float32{float32(w), float32(h)},
			ghost.LeaderLineDirection)

		// Draw datablock
		pac := transforms.WindowFromLatLongP(ghost.Position)
		pt := math.Add2f(datablockOffset, pac)
		td.AddText(datablockText, pt, datablockStyle)

		// Leader line
		v := sp.getLeaderLineVector(ghost.LeaderLineDirection)
		ld.AddLine(pac, math.Add2f(pac, v), color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRadarTrack(ac *av.Aircraft, state *STARSAircraftState, heading float32, ctx *PaneContext,
	transforms ScopeTransformations, trackId string, trackBuilder *renderer.ColoredTrianglesDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder, trid *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder, scale float32) {
	ps := sp.CurrentPreferenceSet
	// TODO: orient based on radar center if just one radar

	pos := state.TrackPosition()
	pw := transforms.WindowFromLatLongP(pos)
	// On high DPI windows displays we need to scale up the tracks

	primaryTargetBrightness := ps.Brightness.PrimarySymbols
	if primaryTargetBrightness > 0 {
		switch mode := sp.radarMode(ctx.ControlClient.RadarSites); mode {
		case RadarModeSingle:
			site := ctx.ControlClient.RadarSites[ps.RadarSiteSelected]
			primary, secondary, dist := site.CheckVisibility(pos, state.TrackAltitude())

			// Orient the box toward the radar
			h := math.Heading2LL(site.Position, pos, ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
			rot := math.Rotator2f(h)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}

			// Scale box based on distance from the radar; TODO: what exactly should this be?
			scale *= float32(math.Clamp(dist/40, .5, 1.5))
			for i := range box {
				box[i] = math.Scale2f(box[i], scale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
			if primary {
				// Draw a filled box
				trid.AddQuad(box[0], box[1], box[2], box[3], color)
			} else if secondary {
				// If it's just a secondary return, only draw the box outline.
				// TODO: is this 40nm, or secondary?
				ld.AddLineLoop(color, box[:])
			}

			// green line
			line := [2][2]float32{[2]float32{-16, 3}, [2]float32{16, 3}}
			for i := range line {
				line[i] = math.Add2f(rot(math.Scale2f(line[i], scale)), pw)
				line[i] = transforms.LatLongFromWindowP(line[i])
			}
			ld.AddLine(line[0], line[1], primaryTargetBrightness.ScaleRGB(renderer.RGB{R: .1, G: .8, B: .1}))

		case RadarModeMulti:
			primary, secondary, _ := sp.radarVisibility(ctx.ControlClient.RadarSites, pos, state.TrackAltitude())
			rot := math.Rotator2f(heading)

			// blue box: x +/-9 pixels, y +/-3 pixels
			box := [4][2]float32{[2]float32{-9, -3}, [2]float32{9, -3}, [2]float32{9, 3}, [2]float32{-9, 3}}
			for i := range box {
				box[i] = math.Scale2f(box[i], scale)
				box[i] = math.Add2f(rot(box[i]), pw)
				box[i] = transforms.LatLongFromWindowP(box[i])
			}

			color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
			if primary {
				// Draw a filled box
				trid.AddQuad(box[0], box[1], box[2], box[3], color)
			} else if secondary {
				// If it's just a secondary return, only draw the box outline.
				// TODO: is this 40nm, or secondary?
				ld.AddLineLoop(color, box[:])
			}

		case RadarModeFused:
			if ps.Brightness.PrimarySymbols > 0 {
				color := primaryTargetBrightness.ScaleRGB(STARSTrackBlockColor)
				drawTrack(trackBuilder, pw, sp.fusedTrackVertices, color)
			}
		}
	}

	// Draw main track symbol letter
	trackIdBrightness := ps.Brightness.Positions
	if trackIdBrightness > 0 {
		dt := sp.datablockType(ctx, ac)
		color, _ := sp.datablockColor(ctx, ac)
		if dt == PartialDatablock || dt == LimitedDatablock {
			trackIdBrightness = ps.Brightness.LimitedDatablocks
		}
		if trackId != "" {
			font := sp.systemFont[ps.CharSize.PositionSymbols]
			outlineFont := sp.systemOutlineFont[ps.CharSize.PositionSymbols]
			td.AddTextCentered(trackId, pw, renderer.TextStyle{Font: outlineFont, Color: renderer.RGB{}})
			td.AddTextCentered(trackId, pw, renderer.TextStyle{Font: font, Color: trackIdBrightness.ScaleRGB(color)})
		} else {
			// TODO: draw box if in range of squawks we have selected

			// diagonals
			dx := transforms.LatLongFromWindowV([2]float32{1, 0})
			dy := transforms.LatLongFromWindowV([2]float32{0, 1})
			// Returns lat-long point w.r.t. p with a window coordinates vector (x,y) added.
			delta := func(p math.Point2LL, x, y float32) math.Point2LL {
				return math.Add2LL(p, math.Add2LL(math.Scale2f(dx, x), math.Scale2f(dy, y)))
			}

			px := float32(3) * scale
			// diagonals
			diagPx := px * 0.707107                                                     /* 1/sqrt(2) */
			trackColor := trackIdBrightness.ScaleRGB(renderer.RGB{R: .1, G: .7, B: .1}) // TODO make a STARS... constant
			ld.AddLine(delta(pos, -diagPx, -diagPx), delta(pos, diagPx, diagPx), trackColor)
			ld.AddLine(delta(pos, diagPx, -diagPx), delta(pos, -diagPx, diagPx), trackColor)
			// horizontal line
			ld.AddLine(delta(pos, -px, 0), delta(pos, px, 0), trackColor)
			// vertical line
			ld.AddLine(delta(pos, 0, -px), delta(pos, 0, px), trackColor)
		}
	}
}

func drawTrack(ctd *renderer.ColoredTrianglesDrawBuilder, p [2]float32, vertices [][2]float32, color renderer.RGB) {
	for i := range vertices {
		v0, v1 := vertices[i], vertices[(i+1)%len(vertices)]
		ctd.AddTriangle(p, math.Add2f(p, v0), math.Add2f(p, v1), color)
	}
}

func getTrackVertices(ctx *PaneContext, diameter float32) [][2]float32 {
	// Figure out how many points to use to approximate the circle; use
	// more the bigger it is on the screen, but, sadly, not enough to get a
	// nice clean circle (matching real-world..)
	np := 8
	if diameter > 20 {
		np = util.Select(diameter <= 40, 16, 32)
	}

	// Prepare the points around the unit circle; rotate them by 1/2 their
	// angular spacing so that we have vertical and horizontal edges at the
	// sides (e.g., a octagon like a stop-sign with 8 points, rather than
	// having a vertex at the top of the circle.)
	rot := math.Rotator2f(360 / (2 * float32(np)))
	pts := util.MapSlice(math.CirclePoints(np), func(p [2]float32) [2]float32 { return rot(p) })

	// Scale the points based on the circle radius (and deal with the usual
	// Windows high-DPI borkage...)
	scale := util.Select(runtime.GOOS == "windows", ctx.Platform.DPIScale(), float32(1))
	radius := scale * float32(int(diameter/2+0.5)) // round to integer
	pts = util.MapSlice(pts, func(p [2]float32) [2]float32 { return math.Scale2f(p, radius) })

	return pts
}

func (sp *STARSPane) drawHistoryTrails(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ps := sp.CurrentPreferenceSet
	if ps.Brightness.History == 0 {
		// Don't draw if brightness == 0.
		return
	}

	historyBuilder := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(historyBuilder)

	const historyTrackDiameter = 8
	historyTrackVertices := getTrackVertices(ctx, historyTrackDiameter)

	now := ctx.ControlClient.CurrentTime()
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]

		if state.LostTrack(now) {
			continue
		}

		// Draw history from new to old
		for i := range ps.RadarTrackHistory {
			trackColorNum := math.Min(i, len(STARSTrackHistoryColors)-1)
			trackColor := ps.Brightness.History.ScaleRGB(STARSTrackHistoryColors[trackColorNum])

			if idx := (state.historyTracksIndex - 1 - i) % len(state.historyTracks); idx >= 0 {
				if p := state.historyTracks[idx].Position; !p.IsZero() {
					drawTrack(historyBuilder, transforms.WindowFromLatLongP(p), historyTrackVertices,
						trackColor)
				}
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	historyBuilder.GenerateCommands(cb)
}

func (sp *STARSPane) getDatablocks(ctx *PaneContext, ac *av.Aircraft) []STARSDatablock {
	now := ctx.ControlClient.CurrentTime()
	state := sp.Aircraft[ac.Callsign]
	if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
		return nil
	}

	dbs := sp.formatDatablocks(ctx, ac)

	// For Southern or Westerly directions the datablock text should be
	// right justified, since the leader line will be connecting on that
	// side.
	dir := sp.getLeaderLineDirection(ac, ctx)
	rightJustify := dir >= math.South
	if rightJustify {
		maxLen := 0
		for _, db := range dbs {
			for _, line := range db.Lines {
				maxLen = math.Max(maxLen, len(line.Text))
			}
		}
		for i := range dbs {
			dbs[i].RightJustify(maxLen)
		}
	}

	return dbs
}

func (sp *STARSPane) getDatablockOffset(textBounds [2]float32, leaderDir math.CardinalOrdinalDirection) [2]float32 {
	// To place the datablock, start with the vector for the leader line.
	drawOffset := sp.getLeaderLineVector(leaderDir)

	// And now fine-tune so that the leader line connects with the midpoint
	// of the line that includes the callsign.
	lineHeight := textBounds[1] / 4
	switch leaderDir {
	case math.North, math.NorthEast, math.East, math.SouthEast:
		drawOffset = math.Add2f(drawOffset, [2]float32{2, lineHeight * 3 / 2})
	case math.South, math.SouthWest, math.West, math.NorthWest:
		drawOffset = math.Add2f(drawOffset, [2]float32{-2 - textBounds[0], lineHeight * 3 / 2})
	}

	return drawOffset
}

func (sp *STARSPane) WarnOutsideAirspace(ctx *PaneContext, ac *av.Aircraft) (alts [][2]int, outside bool) {
	// Only report on ones that are tracked by us
	if ac.TrackingController != ctx.ControlClient.Callsign {
		return
	}

	if ac.OnApproach(false) {
		// No warnings once they're flying the approach
		return
	}

	state := sp.Aircraft[ac.Callsign]
	if ac.IsDeparture() {
		if len(ctx.ControlClient.DepartureAirspace) > 0 {
			inDepartureAirspace, depAlts := sim.InAirspace(ac.Position(), ac.Altitude(), ctx.ControlClient.DepartureAirspace)
			if !state.HaveEnteredAirspace {
				state.HaveEnteredAirspace = inDepartureAirspace
			} else {
				alts = depAlts
				outside = !inDepartureAirspace
			}
		}
	} else {
		if len(ctx.ControlClient.ApproachAirspace) > 0 {
			inApproachAirspace, depAlts := sim.InAirspace(ac.Position(), ac.Altitude(), ctx.ControlClient.ApproachAirspace)
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

func (sp *STARSPane) updateCAAircraft(ctx *PaneContext, aircraft []*av.Aircraft) {
	inCAVolumes := func(state *STARSAircraftState) bool {
		for _, vol := range ctx.ControlClient.InhibitCAVolumes() {
			if vol.Inside(state.TrackPosition(), state.TrackAltitude()) {
				return true
			}
		}
		return false
	}

	conflicting := func(callsigna, callsignb string) bool {
		sa, sb := sp.Aircraft[callsigna], sp.Aircraft[callsignb]
		if sa.DisableCAWarnings || sb.DisableCAWarnings {
			return false
		}
		if inCAVolumes(sa) || inCAVolumes(sb) {
			return false
		}
		return math.NMDistance2LL(sa.TrackPosition(), sb.TrackPosition()) <= LateralMinimum &&
			/*small slop for fp error*/
			math.Abs(sa.TrackAltitude()-sb.TrackAltitude()) <= VerticalMinimum-5 &&
			!sp.diverging(ctx.ControlClient.Aircraft[callsigna], ctx.ControlClient.Aircraft[callsignb])
	}

	// Remove ones that are no longer conflicting
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return conflicting(ca.Callsigns[0], ca.Callsigns[1])
	})

	// Remove ones that are no longer visible
	sp.CAAircraft = util.FilterSlice(sp.CAAircraft, func(ca CAAircraft) bool {
		return slices.ContainsFunc(aircraft, func(ac *av.Aircraft) bool { return ac.Callsign == ca.Callsigns[0] }) &&
			slices.ContainsFunc(aircraft, func(ac *av.Aircraft) bool { return ac.Callsign == ca.Callsigns[1] })
	})

	// Add new conflicts; by appending we keep them sorted by when they
	// were first detected...
	callsigns := util.MapSlice(aircraft, func(ac *av.Aircraft) string { return ac.Callsign })
	for i, callsign := range callsigns {
		for _, ocs := range callsigns[i+1:] {
			if conflicting(callsign, ocs) {
				if !slices.ContainsFunc(sp.CAAircraft, func(ca CAAircraft) bool {
					return callsign == ca.Callsigns[0] && ocs == ca.Callsigns[1]
				}) {
					sp.CAAircraft = append(sp.CAAircraft, CAAircraft{
						Callsigns: [2]string{callsign, ocs},
						SoundEnd:  ctx.Now.Add(5 * time.Second),
					})
				}
			}
		}
	}
}

func (sp *STARSPane) updateInTrailDistance(ctx *PaneContext, aircraft []*av.Aircraft) {
	// Zero out the previous distance
	for _, ac := range aircraft {
		sp.Aircraft[ac.Callsign].IntrailDistance = 0
		sp.Aircraft[ac.Callsign].MinimumMIT = 0
		sp.Aircraft[ac.Callsign].ATPAStatus = ATPAStatusUnset
		sp.Aircraft[ac.Callsign].ATPALeadAircraftCallsign = ""
	}

	// For simplicity, we always compute all of the necessary distances
	// here, regardless of things like both ps.DisplayATPAWarningAlertCones
	// and ps.DisplayATPAMonitorCones being disabled. Later, when it's time
	// to display things (or not), we account for both that as well as all
	// of the potential per-aircraft overrides. This does mean that
	// sometimes the work here is fully wasted.

	// We basically want to loop over each active volume and process all of
	// the aircraft inside it together. There's no direct way to iterate
	// over them, so we'll instead loop over aircraft and when we find one
	// that's inside a volume that hasn't been processed, process all
	// aircraft inside it and then mark the volume as completed.
	handledVolumes := make(map[string]interface{})

	for _, ac := range aircraft {
		vol := ac.ATPAVolume()
		if vol == nil {
			continue
		}
		if _, ok := handledVolumes[vol.Id]; ok {
			continue
		}

		// Get all aircraft on approach to this runway
		runwayAircraft := util.FilterSlice(aircraft, func(ac *av.Aircraft) bool {
			if v := ac.ATPAVolume(); v == nil || v.Id != vol.Id {
				return false
			}

			// Excluded scratchpad -> aircraft doesn't participate in the
			// party whatsoever.
			if ac.Scratchpad != "" && slices.Contains(vol.ExcludedScratchpads, ac.Scratchpad) {
				return false
			}

			state := sp.Aircraft[ac.Callsign]
			return vol.Inside(state.TrackPosition(), float32(state.TrackAltitude()),
				state.TrackHeading(ac.NmPerLongitude())+ac.MagneticVariation(),
				ac.NmPerLongitude(), ac.MagneticVariation())
		})

		// Sort by distance to threshold (there will be some redundant
		// lookups of STARSAircraft state et al. here, but it's
		// straightforward to implement it like this.)
		sort.Slice(runwayAircraft, func(i, j int) bool {
			pi := sp.Aircraft[runwayAircraft[i].Callsign].TrackPosition()
			pj := sp.Aircraft[runwayAircraft[j].Callsign].TrackPosition()
			return math.NMDistance2LL(pi, vol.Threshold) < math.NMDistance2LL(pj, vol.Threshold)
		})

		for i := range runwayAircraft {
			if i == 0 {
				// The first one doesn't have anyone in front...
				continue
			}
			leading, trailing := runwayAircraft[i-1], runwayAircraft[i]
			leadingState, trailingState := sp.Aircraft[leading.Callsign], sp.Aircraft[trailing.Callsign]
			trailingState.IntrailDistance =
				math.NMDistance2LL(leadingState.TrackPosition(), trailingState.TrackPosition())
			sp.checkInTrailCwtSeparation(ctx, trailing, leading)
		}
		handledVolumes[vol.Id] = nil
	}
}

type ModeledAircraft struct {
	callsign     string
	p            [2]float32 // nm coords
	v            [2]float32 // nm, normalized
	gs           float32
	alt          float32
	dalt         float32    // per second
	threshold    [2]float32 // nm
	landingSpeed float32
}

func MakeModeledAircraft(ac *av.Aircraft, state *STARSAircraftState, threshold math.Point2LL) ModeledAircraft {
	ma := ModeledAircraft{
		callsign:  ac.Callsign,
		p:         math.LL2NM(state.TrackPosition(), ac.NmPerLongitude()),
		gs:        float32(state.TrackGroundspeed()),
		alt:       float32(state.TrackAltitude()),
		dalt:      float32(state.TrackDeltaAltitude()),
		threshold: math.LL2NM(threshold, ac.NmPerLongitude()),
	}
	if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.BaseType()]; ok {
		ma.landingSpeed = perf.Speed.Landing
	} else {
		ma.landingSpeed = 120 // ....
	}
	ma.v = state.HeadingVector(ac.NmPerLongitude(), ac.MagneticVariation())
	ma.v = math.LL2NM(ma.v, ac.NmPerLongitude())
	ma.v = math.Normalize2f(ma.v)
	return ma
}

// estimated altitude s seconds in the future
func (ma *ModeledAircraft) EstimatedAltitude(s float32) float32 {
	// simple linear model
	return ma.alt + s*ma.dalt
}

// Return estimated position 1s in the future
func (ma *ModeledAircraft) NextPosition(p [2]float32) [2]float32 {
	gs := ma.gs // current speed
	td := math.Distance2f(p, ma.threshold)
	if td < 2 {
		gs = math.Min(gs, ma.landingSpeed)
	} else if td < 5 {
		t := (td - 2) / 3 // [0,1]
		// lerp from current speed down to landing speed
		gs = math.Lerp(t, ma.landingSpeed, gs)
	}

	gs /= 3600 // nm / second
	return math.Add2f(p, math.Scale2f(ma.v, gs))
}

func getCwtCategory(ctx *PaneContext, ac *av.Aircraft) string {
	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		ctx.Lg.Errorf("%s: unable to get performance model for %s", ac.Callsign, ac.FlightPlan.BaseType())
		return "NOWGT"
	}
	wc := perf.Category.CWT
	if len(wc) == 0 {
		ctx.Lg.Errorf("%s: no CWT category found for %s", ac.Callsign, ac.FlightPlan.BaseType())
		return "NOWGT"
	}

	switch wc {
	case "NOWGT":
		return "NOWGT"
	case "I":
		return "I"
	case "H":
		return "H"
	case "G":
		return "G"
	case "F":
		return "F"
	case "E":
		return "E"
	case "D":
		return "D"
	case "C":
		return "C"
	case "B":
		return "B"
	case "A":
		return "A"
	default:
		ctx.Lg.Errorf("%s: unexpected weight class \"%c\"", ac.Callsign, wc[0])
		return "NOWGT"
	}

}

func (sp *STARSPane) checkInTrailCwtSeparation(ctx *PaneContext, back, front *av.Aircraft) {
	cwtClass := func(ac *av.Aircraft) int {
		perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.BaseType()]
		if !ok {
			ctx.Lg.Errorf("%s: unable to get performance model for %s", ac.Callsign, ac.FlightPlan.BaseType())
			return 9
		}
		wc := perf.Category.CWT
		if len(wc) == 0 {
			ctx.Lg.Errorf("%s: no CWT category found for %s", ac.Callsign, ac.FlightPlan.BaseType())
			return 9
		}
		switch wc[0] {
		case 'I':
			return 0
		case 'H':
			return 1
		case 'G':
			return 2
		case 'F':
			return 3
		case 'E':
			return 4
		case 'D':
			return 5
		case 'C':
			return 6
		case 'B':
			return 7
		case 'A':
			return 8
		default:
			ctx.Lg.Errorf("%s: unexpected weight class \"%c\"", ac.Callsign, wc[0])
			return 9
		}
	}
	// 7110.126B TBL 5-5-2
	// 0 value means minimum radar separation
	cwtOnApproachLookUp := [10][10]float32{ // [front][back]
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 10},          // Behind I
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 10},          // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0, 10},          // Behind G
		{4, 0, 0, 0, 0, 0, 0, 0, 0, 10},          // Behind F
		{4, 0, 0, 0, 0, 0, 0, 0, 0, 10},          // Behind E
		{6, 6, 5, 5, 5, 4, 4, 3, 0, 10},          // Behind D
		{6, 5, 3.5, 3.5, 3.5, 0, 0, 0, 0, 10},    // Behind C
		{6, 5, 5, 5, 5, 4, 4, 3, 0, 10},          // Behind B
		{8, 8, 7, 7, 7, 6, 6, 5, 0, 10},          // Behind A
		{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}, // Behind NOWGT (No weight: 7110.762)
	}
	cwtSeparation := cwtOnApproachLookUp[cwtClass(front)][cwtClass(back)]

	state := sp.Aircraft[back.Callsign]
	vol := back.ATPAVolume()
	if cwtSeparation == 0 {
		cwtSeparation = float32(LateralMinimum)

		// 7110.126B replaces 7110.65Z 5-5-4(j), which is now 7110.65AA 5-5-4(i)
		// Reduced separation allowed 10 NM out (also enabled for the ATPA volume)
		if vol.Enable25nmApproach &&
			math.NMDistance2LL(vol.Threshold, state.TrackPosition()) < vol.Dist25nmApproach {

			// between aircraft established on the final approach course
			// Note 1: checked with OnExtendedCenterline since reduced separation probably
			// doesn't apply to approaches with curved final approach segment
			// Note 2: 0.2 NM is slightly less than full-scale deflection at 5 NM out
			if back.OnExtendedCenterline(.2) && front.OnExtendedCenterline(.2) {
				// Not-implemented: Required separation must exist prior to applying 2.5 NM separation (TBL 5-5-2)
				cwtSeparation = 2.5
			}
		}
	}

	state.MinimumMIT = cwtSeparation
	state.ATPALeadAircraftCallsign = front.Callsign
	state.ATPAStatus = ATPAStatusMonitor // baseline

	// If the aircraft's scratchpad is filtered, then it doesn't get
	// warnings or alerts but is still here for the aircraft behind it.
	if back.Scratchpad != "" && slices.Contains(vol.FilteredScratchpads, back.Scratchpad) {
		return
	}

	// front, back aircraft
	frontModel := MakeModeledAircraft(front, sp.Aircraft[front.Callsign], vol.Threshold)
	backModel := MakeModeledAircraft(back, state, vol.Threshold)

	// Will there be a MIT violation s seconds in the future?  (Note that
	// we don't include altitude separation here since what we need is
	// distance separation by the threshold...)
	frontPosition, backPosition := frontModel.p, backModel.p
	for s := 0; s < 45; s++ {
		frontPosition, backPosition = frontModel.NextPosition(frontPosition), backModel.NextPosition(backPosition)
		distance := math.Distance2f(frontPosition, backPosition)
		if distance < cwtSeparation { // no bueno
			if s <= 24 {
				// Error if conflict expected within 24 seconds (6-159).
				state.ATPAStatus = ATPAStatusAlert
				return
			} else {
				// Warning if conflict expected within 45 seconds (6-159).
				state.ATPAStatus = ATPAStatusWarning
				return
			}
		}
	}
}

func (sp *STARSPane) diverging(a, b *av.Aircraft) bool {
	sa, sb := sp.Aircraft[a.Callsign], sp.Aircraft[b.Callsign]

	pa := math.LL2NM(sa.TrackPosition(), a.NmPerLongitude())
	da := math.LL2NM(sa.HeadingVector(a.NmPerLongitude(), a.MagneticVariation()), a.NmPerLongitude())
	pb := math.LL2NM(sb.TrackPosition(), b.NmPerLongitude())
	db := math.LL2NM(sb.HeadingVector(b.NmPerLongitude(), b.MagneticVariation()), b.NmPerLongitude())

	pint, ok := math.LineLineIntersect(pa, math.Add2f(pa, da), pb, math.Add2f(pb, db))
	if !ok {
		// This generally happens at the start when we don't have a valid
		// track heading vector yet.
		return false
	}

	if math.Dot(da, math.Sub2f(pint, pa)) > 0 && math.Dot(db, math.Sub2f(pint, pb)) > 0 {
		// intersection is in front of one of them
		return false
	}

	// Intersection behind both; make sure headings are at least 15 degrees apart.
	if math.HeadingDifference(sa.TrackHeading(a.NmPerLongitude()), sb.TrackHeading(b.NmPerLongitude())) < 15 {
		return false
	}

	return true
}

func (sp *STARSPane) haveActiveWarnings(ctx *PaneContext, ac *av.Aircraft) bool {
	ps := sp.CurrentPreferenceSet
	state := sp.Aircraft[ac.Callsign]

	if state.MSAW && !state.InhibitMSAW && !state.DisableMSAW && !ps.DisableMSAW {
		return true
	}
	if ok, _ := av.SquawkIsSPC(ac.Squawk); ok {
		return true
	}
	if len(ac.SPCOverrides) > 0 {
		return true
	}
	if !ps.DisableCAWarnings && !state.DisableCAWarnings &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
			}) {
		return true
	}
	if _, outside := sp.WarnOutsideAirspace(ctx, ac); outside {
		return true
	}

	return false
}

func (sp *STARSPane) getWarnings(ctx *PaneContext, ac *av.Aircraft) []string {
	var warnings []string
	addWarning := func(w string) {
		if !slices.Contains(warnings, w) {
			warnings = append(warnings, w)
		}
	}

	ps := sp.CurrentPreferenceSet
	state := sp.Aircraft[ac.Callsign]

	if state.MSAW && !state.InhibitMSAW && !state.DisableMSAW && !ps.DisableMSAW {
		addWarning("LA")
	}
	if ok, code := av.SquawkIsSPC(ac.Squawk); ok {
		addWarning(code)
	}
	for code := range ac.SPCOverrides {
		addWarning(code)
	}
	if !ps.DisableCAWarnings && !state.DisableCAWarnings &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
			}) {
		addWarning("CA")
	}
	if alts, outside := sp.WarnOutsideAirspace(ctx, ac); outside {
		altStrs := ""
		for _, a := range alts {
			altStrs += fmt.Sprintf("/%d-%d", a[0]/100, a[1]/100)
		}
		addWarning("AS" + altStrs)
	}

	if len(warnings) > 1 {
		slices.Sort(warnings)
	}

	return warnings
}

func (sp *STARSPane) formatDatablocks(ctx *PaneContext, ac *av.Aircraft) []STARSDatablock {
	if ac.Mode == av.Standby {
		return nil
	}

	state := sp.Aircraft[ac.Callsign]

	warnings := sp.getWarnings(ctx, ac)

	// baseDB is what stays the same for all datablock variants
	baseDB := STARSDatablock{}
	baseDB.Lines[0].Text = strings.Join(warnings, "/") // want e.g., EM/LA if multiple things going on
	if len(warnings) > 0 {
		baseDB.Lines[0].Colors = append(baseDB.Lines[0].Colors,
			STARSDatablockFieldColors{
				Start: 0,
				End:   len(baseDB.Lines[0].Text),
				Color: STARSTextAlertColor,
			})
	}

	ty := sp.datablockType(ctx, ac)

	switch ty {
	case LimitedDatablock:
		db := baseDB.Duplicate()
		db.Lines[1].Text = fmt.Sprintf("%v", ac.Squawk)
		db.Lines[2].Text = fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if state.FullLDBEndTime.After(ctx.Now) {
			db.Lines[2].Text += fmt.Sprintf(" %02d", (state.TrackGroundspeed()+5)/10)
		}

		if state.Ident(ctx.Now) {
			// flash ID after squawk code
			start := len(db.Lines[1].Text)
			db.Lines[1].Text += "ID"

			// The text is the same but the "ID" is much dimmer for the flash.
			db2 := db.Duplicate()
			color, _ := sp.datablockColor(ctx, ac)
			db2.Lines[1].Colors = append(db2.Lines[1].Colors,
				STARSDatablockFieldColors{Start: start, End: start + 2, Color: color.Scale(0.3)})
			return []STARSDatablock{db, db2}
		} else {
			return []STARSDatablock{db}
		}

	case PartialDatablock:
		dbs := []STARSDatablock{baseDB.Duplicate(), baseDB.Duplicate()}

		if ac.Squawk != ac.AssignedSquawk {
			sq := ac.Squawk.String()
			if len(baseDB.Lines[0].Text) > 0 {
				dbs[0].Lines[0].Text += " "
				dbs[1].Lines[0].Text += " "
			}
			dbs[0].Lines[0].Text += sq
			dbs[1].Lines[0].Text += sq + "WHO"
		}

		if state.Ident(ctx.Now) {
			alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
			dbs[0].Lines[1].Text = alt + " ID"
			dbs[1].Lines[1].Text = alt + " ID"

			color, _ := sp.datablockColor(ctx, ac)
			dbs[1].Lines[1].Colors = append(dbs[1].Lines[1].Colors,
				STARSDatablockFieldColors{Start: 4, End: 6, Color: color.Scale(0.3)})

			return dbs
		}

		if fp := ac.FlightPlan; fp != nil && fp.Rules == av.VFR {
			as := fmt.Sprintf("%03d  %02d", (state.TrackAltitude()+50)/100, (state.TrackGroundspeed()+5)/10)
			dbs[0].Lines[1].Text = as
			dbs[1].Lines[1].Text = as
			return dbs
		}

		field2 := " "
		if ac.HandoffTrackController != "" {
			if ctrl := ctx.ControlClient.Controllers[ac.HandoffTrackController]; ctrl != nil {
				if ac.RedirectedHandoff.RedirectedTo != "" {
					if toctrl := ctx.ControlClient.Controllers[ac.RedirectedHandoff.RedirectedTo]; toctrl != nil {
						field2 = toctrl.SectorId[len(ctrl.SectorId)-1:]
					}
				} else {
					if ctrl.ERAMFacility { // Same facility
						field2 = "C"
					} else if ctrl.FacilityIdentifier == "" { // Enroute handoff
						field2 = ctrl.SectorId[len(ctrl.SectorId)-1:]
					} else { // Different facility
						field2 = ctrl.FacilityIdentifier
					}

				}
			}
		}

		field3 := ""
		if ac.FlightPlan.Rules == av.VFR {
			field3 += "V"
		} else if sp.isOverflight(ctx, ac) {
			field3 += "E"
		}
		field3 += state.CWTCategory

		// Field 1: alternate between altitude and either primary
		// scratchpad or destination airport.
		ap := ac.FlightPlan.ArrivalAirport
		if len(ap) == 4 {
			ap = ap[1:] // drop the leading K
		}
		alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		sp := fmt.Sprintf("%3s", ac.Scratchpad)

		field1 := [2]string{}
		field1[0] = alt
		if ac.Scratchpad != "" {
			field1[1] = sp
		} else if airport := ctx.ControlClient.Airports[ac.FlightPlan.ArrivalAirport]; airport != nil && !airport.OmitArrivalScratchpad {
			field1[1] = ap
		} else {
			field1[1] = alt
		}

		dbs[0].Lines[1].Text = field1[0] + field2 + field3
		dbs[1].Lines[1].Text = field1[1] + field2 + field3

		return dbs

	case FullDatablock:
		// Line 1: fields 1, 2, and 8 (surprisingly). Field 8 may be multiplexed.
		field1 := ac.Callsign

		field2 := ""
		if state.InhibitMSAW || state.DisableMSAW {
			if state.DisableCAWarnings {
				field2 = "+"
			} else {
				field2 = "*"
			}
		} else if state.DisableCAWarnings {
			field2 = STARSTriangleCharacter
		}

		field8 := []string{""}
		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok || state.PointedOut {
			field8 = []string{" PO"}
		} else if id, ok := sp.OutboundPointOuts[ac.Callsign]; ok {
			field8 = []string{" PO" + id}
		} else if _, ok := sp.RejectedPointOuts[ac.Callsign]; ok {
			field8 = []string{"", " UN"}
		} else if state.POFlashingEndTime.After(ctx.Now) {
			field8 = []string{"", " PO"}
		} else if ac.RedirectedHandoff.ShowRDIndicator(ctx.ControlClient.Callsign, state.RDIndicatorEnd) {
			field8 = []string{" RD"}
		}

		// Line 2: fields 3, 4, 5
		alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if state.LostTrack(ctx.ControlClient.SimTime) {
			alt = "CST"
		}
		// Build up field3 and field4 in tandem because 4 gets a "+" if 3
		// is displaying the secondary scratchpad.  Leave the empty string
		// as a placeholder in field 4 otherwise.
		field3 := []string{alt}
		field4 := []string{""}
		if !state.Ident(ctx.Now) {
			// Don't display these if they're identing: then it's just altitude and speed + "ID"
			if ac.Scratchpad != "" {
				field3 = append(field3, ac.Scratchpad)
				field4 = append(field4, "")
			}
			if ac.SecondaryScratchpad != "" {
				field3 = append(field3, ac.SecondaryScratchpad)
				field4 = append(field4, "+") // 2-67, "Field 4 Contents"
			}
			if len(field3) == 1 {
				if ap := ctx.ControlClient.Airports[ac.FlightPlan.ArrivalAirport]; ap != nil && !ap.OmitArrivalScratchpad {
					ap := ac.FlightPlan.ArrivalAirport
					if len(ap) == 4 {
						ap = ap[1:] // drop the leading K
					}
					field3 = append(field3, ap)
					field4 = append(field4, "")
				}
			}
		}

		// Fill in empty field4 entries.
		for i := range field4 {
			if field4[i] == "" && ac.HandoffTrackController != "" {
				if ctrl := ctx.ControlClient.Controllers[ac.HandoffTrackController]; ctrl != nil {
					if ac.RedirectedHandoff.RedirectedTo != "" {
						if toctrl := ctx.ControlClient.Controllers[ac.RedirectedHandoff.RedirectedTo]; toctrl != nil {
							field4 = append(field4, toctrl.SectorId[len(ctrl.SectorId)-1:])
						}
					} else {
						if ctrl.ERAMFacility { // Same facility
							field4 = append(field4, "C")
						} else if ctrl.FacilityIdentifier == "" { // Enroute handoff
							field4 = append(field4, ctrl.SectorId[len(ctrl.SectorId)-1:])
						} else { // Different facility
							field4 = append(field4, ctrl.FacilityIdentifier)
						}
					}
				}
			}
			for len(field4[i]) < 2 {
				field4[i] += " "
			}
		}

		speed := fmt.Sprintf("%02d", (state.TrackGroundspeed()+5)/10)

		field5 := []string{} // alternate speed and aircraft type
		var line5FieldColors *STARSDatablockFieldColors
		if state.Ident(ctx.Now) {
			// Speed is followed by ID when identing (2-67, field 5)
			field5 = append(field5, speed+"ID")
			field5 = append(field5, speed+"ID")
			color, _ := sp.datablockColor(ctx, ac)

			line5FieldColors = &STARSDatablockFieldColors{
				Start: len(speed) + 1,
				End:   len(speed) + 3,
				Color: color.Scale(0.3),
			}
		} else {
			acCategory := ""
			actype := ac.FlightPlan.TypeWithoutSuffix()
			if strings.Index(actype, "/") == 1 {
				actype = actype[2:]
			}
			modifier := ""
			if ac.FlightPlan.Rules == av.VFR {
				modifier += "V"
			} else if sp.isOverflight(ctx, ac) {
				modifier += "E"
			} else {
				modifier = " "
			}
			acCategory = modifier + state.CWTCategory

			field5 = append(field5, speed+acCategory)

			field5 = append(field5, actype)
			if (state.DisplayRequestedAltitude != nil && *state.DisplayRequestedAltitude) ||
				(state.DisplayRequestedAltitude == nil && sp.CurrentPreferenceSet.DisplayRequestedAltitude) {
				field5 = append(field5, fmt.Sprintf("R%03d", ac.FlightPlan.Altitude/100))
			}
		}
		for i := range field5 {
			if len(field5[i]) < 5 {
				field5[i] = fmt.Sprintf("%-5s", field5[i])
			}
		}

		field6 := ""
		var line3FieldColors *STARSDatablockFieldColors
		if state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert {
			field6 = "*TPA"
		} else if state.IntrailDistance != 0 && sp.CurrentPreferenceSet.DisplayATPAInTrailDist {
			field6 = fmt.Sprintf("%.2f", state.IntrailDistance)

			if state.ATPAStatus == ATPAStatusWarning {
				line3FieldColors = &STARSDatablockFieldColors{
					Start: 0,
					End:   len(field6),
					Color: STARSATPAWarningColor,
				}
			} else if state.ATPAStatus == ATPAStatusAlert {
				line3FieldColors = &STARSDatablockFieldColors{
					Start: 0,
					End:   len(field6),
					Color: STARSATPAAlertColor,
				}
			}
		}
		for len(field6) < 5 {
			field6 += " "
		}

		field7 := "    "
		if ac.TempAltitude != 0 {
			ta := (ac.TempAltitude + 50) / 100
			field7 = fmt.Sprintf("A%03d", ta)
		}
		line3 := field6 + "  " + field7

		// Now make some datablocks. Note that line 1 has already been set
		// in baseDB above.
		//
		// A number of the fields may be multiplexed; the total number of
		// unique datablock variations is the least common multiple of all
		// of their lengths.  and 8 may be time multiplexed, which
		// simplifies db creation here.
		dbs := []STARSDatablock{}
		n := math.LCM(math.LCM(len(field3), len(field4)), math.LCM(len(field5), len(field8)))
		for i := 0; i < n; i++ {
			db := baseDB.Duplicate()
			db.Lines[1].Text = field1 + field2 + field8[i%len(field8)]
			db.Lines[2].Text = field3[i%len(field3)] + field4[i%len(field4)] + field5[i%len(field5)]
			db.Lines[3].Text = line3
			if line3FieldColors != nil {
				db.Lines[3].Colors = append(db.Lines[3].Colors, *line3FieldColors)
			}
			if line5FieldColors != nil && i&1 == 1 {
				// Flash "ID" for identing
				fc := *line5FieldColors
				fc.Start += len(field3[i%len(field3)]) + len(field4)
				fc.End += len(field3[i%len(field3)]) + len(field4)
				db.Lines[2].Colors = append(db.Lines[2].Colors, fc)
			}
			dbs = append(dbs, db)
		}
		return dbs
	}

	return nil
}

func sameFacility(ctx *PaneContext, receiving string) bool {
	ca, oka := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
	cb, okb := ctx.ControlClient.Controllers[receiving]
	return oka && okb && ca.FacilityIdentifier == cb.FacilityIdentifier
}

func (sp *STARSPane) datablockColor(ctx *PaneContext, ac *av.Aircraft) (color renderer.RGB, brightness STARSBrightness) {
	ps := sp.CurrentPreferenceSet
	dt := sp.datablockType(ctx, ac)
	state := sp.Aircraft[ac.Callsign]
	brightness = util.Select(dt == PartialDatablock || dt == LimitedDatablock,
		ps.Brightness.LimitedDatablocks, ps.Brightness.FullDatablocks)

	if ac.Callsign == sp.dwellAircraft {
		brightness = STARSBrightness(100)
	}

	for _, controller := range ac.RedirectedHandoff.Redirector {
		if controller == ctx.ControlClient.Callsign && ac.RedirectedHandoff.RedirectedTo != ctx.ControlClient.Callsign {
			color = STARSUntrackedAircraftColor
		}
	}

	// Handle cases where it should flash
	if ctx.Now.Second()&1 == 0 { // one second cycle
		if _, pointOut := sp.InboundPointOuts[ac.Callsign]; pointOut {
			// point out
			brightness /= 3
		} else if state.OutboundHandoffAccepted && ctx.Now.Before(state.OutboundHandoffFlashEnd) {
			// we handed it off, it was accepted, but we haven't yet acknowledged
			brightness /= 3
		} else if (ac.HandoffTrackController == ctx.ControlClient.Callsign && !slices.Contains(ac.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign)) || // handing off to us
			ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
			brightness /= 3
		}
	}

	// Check if were the controller being ForceQL
	for _, control := range ac.ForceQLControllers {
		if control == ctx.ControlClient.Callsign {
			color = STARSInboundPointOutColor
			return
		}
	}

	if _, ok := sp.InboundPointOuts[ac.Callsign]; ok || state.PointedOut || state.ForceQL {
		// yellow for pointed out by someone else or uncleared after acknowledged.
		color = STARSInboundPointOutColor
	} else if state.IsSelected {
		// middle button selected
		color = STARSSelectedAircraftColor
	} else if ac.TrackingController == ctx.ControlClient.Callsign {
		// we own the track track
		color = STARSTrackedAircraftColor
	} else if ac.RedirectedHandoff.OriginalOwner == ctx.ControlClient.Callsign || ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
		color = STARSTrackedAircraftColor
	} else if ac.HandoffTrackController == ctx.ControlClient.Callsign &&
		!slices.Contains(ac.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign) {
		// flashing white if it's being handed off to us.
		color = STARSTrackedAircraftColor
	} else if state.OutboundHandoffAccepted {
		// we handed it off, it was accepted, but we haven't yet acknowledged
		color = STARSTrackedAircraftColor
	} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
		// quick look all plus
		color = STARSTrackedAircraftColor
	} else if slices.ContainsFunc(ps.QuickLookPositions,
		func(q QuickLookPosition) bool { return q.Callsign == ac.TrackingController && q.Plus }) {
		// individual quicklook plus controller
		color = STARSTrackedAircraftColor
	} else {
		// green otherwise
		color = STARSUntrackedAircraftColor
	}

	return
}

func (sp *STARSPane) drawLeaderLines(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	now := ctx.ControlClient.SimTime

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		dbs := sp.getDatablocks(ctx, ac)
		if len(dbs) == 0 {
			continue
		}

		baseColor, brightness := sp.datablockColor(ctx, ac)
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		v := sp.getLeaderLineVector(sp.getLeaderLineDirection(ac, ctx))
		ld.AddLine(pac, math.Add2f(pac, v), brightness.ScaleRGB(baseColor))
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.Platform.DPIScale())
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawDatablocks(aircraft []*av.Aircraft, ctx *PaneContext,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	now := ctx.ControlClient.SimTime
	realNow := ctx.Now // for flashing rate...
	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Datablocks]

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		dbs := sp.getDatablocks(ctx, ac)
		if len(dbs) == 0 {
			continue
		}

		color, brightness := sp.datablockColor(ctx, ac)
		if brightness == 0 {
			continue
		}

		// Compute the bounds of the datablock; always use the first one so
		// things don't jump around when it switches between multiple of
		// them.
		w, h := dbs[0].BoundText(font)
		datablockOffset := sp.getDatablockOffset([2]float32{float32(w), float32(h)},
			sp.getLeaderLineDirection(ac, ctx))

		// Draw characters starting at the upper left.
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		pt := math.Add2f(datablockOffset, pac)
		idx := (realNow.Second() / 2) % len(dbs) // 2 second cycle
		dbs[idx].DrawText(td, pt, font, color, brightness)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawPTLs(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.CurrentPreferenceSet

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	now := ctx.ControlClient.SimTime
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !state.HaveHeading() {
			continue
		}
		if !(state.DisplayPTL || ps.PTLAll || (ps.PTLOwn && ac.TrackingController == ctx.ControlClient.Callsign)) {
			continue
		}
		if ps.PTLLength == 0 {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(state.TrackGroundspeed()) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := state.TrackHeading(ac.NmPerLongitude())
		h := [2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}
		h = math.Scale2f(h, dist)
		end := math.Add2f(math.LL2NM(state.TrackPosition(), ac.NmPerLongitude()), h)

		ld.AddLine(state.TrackPosition(), math.NM2LL(end, ac.NmPerLongitude()), color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	now := ctx.ControlClient.SimTime
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Datablocks]
	color := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)

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
			radius := state.JRingRadius / transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)
			ld.AddCircle(pc, radius, nsegs, color)

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				// draw the ring size around 7.5 o'clock
				// vector from center to the circle there
				v := [2]float32{-.707106 * radius, -.707106 * radius} // -sqrt(2)/2
				// move up to make space for the text
				v[1] += float32(font.Size) + 3
				pt := math.Add2f(pc, v)
				textStyle := renderer.TextStyle{Font: font, Color: color}
				td.AddText(format(state.JRingRadius), pt, textStyle)
			}
		}
		atpaStatus := state.ATPAStatus // this may change

		// If warning/alert cones are inhibited but monitor cones are not,
		// we may still draw a monitor cone.
		if (atpaStatus == ATPAStatusWarning || atpaStatus == ATPAStatusAlert) &&
			(!ps.DisplayATPAWarningAlertCones || (state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert)) {
			atpaStatus = ATPAStatusMonitor
		}

		drawATPAMonitor := atpaStatus == ATPAStatusMonitor && ps.DisplayATPAMonitorCones &&
			(state.DisplayATPAMonitor == nil || *state.DisplayATPAMonitor) &&
			state.IntrailDistance-state.MinimumMIT <= 2 // monitor only if within 2nm of MIT requirement
		drawATPAWarning := atpaStatus == ATPAStatusWarning && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPAAlert := atpaStatus == ATPAStatusAlert && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPACone := drawATPAMonitor || drawATPAWarning || drawATPAAlert

		if state.HaveHeading() && (state.ConeLength > 0 || drawATPACone) {
			// Find the length of the cone in pixel coordinates)
			lengthNM := math.Max(state.ConeLength, state.MinimumMIT)
			length := lengthNM / transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)

			// Form a triangle; the end of the cone is 10 pixels wide
			pts := [3][2]float32{{0, 0}, {-5, length}, {5, length}}

			// Now we'll rotate the vertices so that it points in the
			// appropriate direction.
			var coneHeading float32
			if drawATPACone {
				// The cone is oriented to point toward the leading aircraft.
				if sfront, ok := sp.Aircraft[state.ATPALeadAircraftCallsign]; ok {
					coneHeading = math.Heading2LL(state.TrackPosition(), sfront.TrackPosition(),
						ac.NmPerLongitude(), ac.MagneticVariation())

				}
			} else {
				// The cone is oriented along the aircraft's heading.
				coneHeading = state.TrackHeading(ac.NmPerLongitude()) + ac.MagneticVariation()
			}
			rot := math.Rotator2f(coneHeading)
			for i := range pts {
				pts[i] = rot(pts[i])
			}

			coneColor := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)
			if atpaStatus == ATPAStatusWarning {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAWarningColor)
			} else if atpaStatus == ATPAStatusAlert {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAAlertColor)
			}

			// We've got what we need to draw a polyline with the
			// aircraft's position as an anchor.
			pw := transforms.WindowFromLatLongP(state.TrackPosition())
			for i := range pts {
				pts[i] = math.Add2f(pts[i], pw)
			}
			ld.AddLineLoop(coneColor, pts[:])

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				textStyle := renderer.TextStyle{Font: font, Color: coneColor}

				pCenter := math.Add2f(pw, rot(math.Scale2f([2]float32{0, 0.5}, length)))

				// Draw a quad in the background color behind the text
				text := format(lengthNM)
				bx, by := textStyle.Font.BoundText(" "+text+" ", 0)
				fbx, fby := float32(bx), float32(by+2)
				trid.AddQuad(math.Add2f(pCenter, [2]float32{-fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, fby / 2}),
					math.Add2f(pCenter, [2]float32{-fbx / 2, fby / 2}))

				td.AddTextCentered(text, pCenter, textStyle)
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	cb.SetRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// Draw all of the range-bearing lines that have been specified.
func (sp *STARSPane) drawRBLs(aircraft []*av.Aircraft, ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.CurrentPreferenceSet
	color := ps.Brightness.Lines.RGB() // check
	style := renderer.TextStyle{
		Font:  sp.systemFont[ps.CharSize.Tools],
		Color: color,
	}

	drawRBL := func(p0 math.Point2LL, p1 math.Point2LL, idx int, gs float32) {
		// Format the range-bearing line text for the two positions.
		hdg := math.Heading2LL(p0, p1, ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
		dist := math.NMDistance2LL(p0, p1)
		text := fmt.Sprintf("%3d/%.2f", int(hdg+.5), dist)
		if gs != 0 {
			// Add ETA in minutes
			eta := 60 * dist / gs
			text += fmt.Sprintf("/%d", int(eta+.5))
		}
		text += fmt.Sprintf("-%d", idx)

		// And draw the line and the text.
		pText := transforms.WindowFromLatLongP(math.Mid2LL(p0, p1))
		td.AddTextCentered(text, pText, style)
		ld.AddLine(p0, p1, color)
	}

	// Maybe draw a wip RBL with p1 as the mouse's position
	if sp.wipRBL != nil {
		wp := sp.wipRBL.P[0]
		if ctx.Mouse != nil {
			p1 := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			if wp.Callsign != "" {
				if ac := ctx.ControlClient.Aircraft[wp.Callsign]; ac != nil && sp.datablockVisible(ac, ctx) &&
					slices.Contains(aircraft, ac) {
					if state, ok := sp.Aircraft[wp.Callsign]; ok {
						drawRBL(state.TrackPosition(), p1, len(sp.RangeBearingLines)+1, ac.GS())
					}
				}
			} else {
				drawRBL(wp.Loc, p1, len(sp.RangeBearingLines)+1, 0)
			}
		}
	}

	for i, rbl := range sp.RangeBearingLines {
		if p0, p1 := rbl.GetPoints(ctx, aircraft, sp); !p0.IsZero() && !p1.IsZero() {
			gs := float32(0)

			// If one but not both are tracks, get the groundspeed so we
			// can display an ETA.
			if rbl.P[0].Callsign != "" {
				if rbl.P[1].Callsign == "" {
					if ac := ctx.ControlClient.Aircraft[rbl.P[0].Callsign]; ac != nil {
						gs = ac.GS()
					}
				}
			} else if rbl.P[1].Callsign != "" {
				if rbl.P[0].Callsign == "" {
					if ac := ctx.ControlClient.Aircraft[rbl.P[1].Callsign]; ac != nil {
						gs = ac.GS()
					}
				}
			}

			drawRBL(p0, p1, i+1, gs)
		}
	}

	// Remove stale ones that include aircraft that have landed, etc.
	sp.RangeBearingLines = util.FilterSlice(sp.RangeBearingLines, func(rbl STARSRangeBearingLine) bool {
		p0, p1 := rbl.GetPoints(ctx, aircraft, sp)
		return !p0.IsZero() && !p1.IsZero()
	})

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	cs0, cs1 := sp.MinSepAircraft[0], sp.MinSepAircraft[1]
	if cs0 == "" || cs1 == "" {
		// Two aircraft haven't been specified.
		return
	}
	ac0, ok0 := ctx.ControlClient.Aircraft[cs0]
	ac1, ok1 := ctx.ControlClient.Aircraft[cs1]
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

	// Go ahead and draw the minimum separation lines and text.
	p0ll, p1ll := s0.TrackPosition(), s1.TrackPosition()
	d0ll := s0.HeadingVector(ac0.NmPerLongitude(), ac0.MagneticVariation())
	d1ll := s1.HeadingVector(ac1.NmPerLongitude(), ac1.MagneticVariation())

	p0, d0 := math.LL2NM(p0ll, ac0.NmPerLongitude()), math.LL2NM(d0ll, ac0.NmPerLongitude())
	p1, d1 := math.LL2NM(p1ll, ac1.NmPerLongitude()), math.LL2NM(d1ll, ac1.NmPerLongitude())

	// Find the parametric distance along the respective rays of the
	// aircrafts' courses where they at at a minimum distance; this is
	// linearly extrapolating their positions.
	tmin := math.RayRayMinimumDistance(p0, d0, p1, d1)

	// If something blew up in RayRayMinimumDistance then just bail out here.
	if gomath.IsInf(float64(tmin), 0) || gomath.IsNaN(float64(tmin)) {
		return
	}

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	font := sp.systemFont[ps.CharSize.Tools]

	// Draw the separator lines (and triangles, if appropriate.)
	var pw0, pw1 [2]float32          // Window coordinates of the points of minimum approach
	var p0tmin, p1tmin math.Point2LL // Lat-long coordinates of the points of minimum approach
	if tmin < 0 {
		// The closest approach was in the past; just draw a line between
		// the two tracks and initialize the above coordinates.
		ld.AddLine(p0ll, p1ll, color)
		p0tmin, p1tmin = p0ll, p1ll
		pw0, pw1 = transforms.WindowFromLatLongP(p0ll), transforms.WindowFromLatLongP(p1ll)
	} else {
		// Closest approach in the future: draw a line from each track to
		// the minimum separation line as well as the minimum separation
		// line itself.
		p0tmin = math.NM2LL(math.Add2f(p0, math.Scale2f(d0, tmin)), ac0.NmPerLongitude())
		p1tmin = math.NM2LL(math.Add2f(p1, math.Scale2f(d1, tmin)), ac1.NmPerLongitude())
		ld.AddLine(p0ll, p0tmin, color)
		ld.AddLine(p0tmin, p1tmin, color)
		ld.AddLine(p1tmin, p1ll, color)

		// Draw filled triangles centered at p0tmin and p1tmin.
		pw0, pw1 = transforms.WindowFromLatLongP(p0tmin), transforms.WindowFromLatLongP(p1tmin)
		style := renderer.TextStyle{Font: font, Color: color}
		td.AddTextCentered(STARSFilledUpTriangle, pw0, style)
		td.AddTextCentered(STARSFilledUpTriangle, pw1, style)
	}

	// Draw the text for the minimum distance
	// Center the text along the minimum distance line
	pText := math.Mid2f(pw0, pw1)
	style := renderer.TextStyle{
		Font:            font,
		Color:           color,
		DrawBackground:  true,
		BackgroundColor: renderer.RGB{},
	}
	text := fmt.Sprintf("%.2fNM", math.NMDistance2LL(p0tmin, p1tmin))
	if tmin < 0 {
		text = "NO XING\n" + text
	}
	td.AddTextCentered(text, pText, style)

	// Add the corresponding drawing commands to the CommandBuffer.
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawAirspace(ctx *PaneContext, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ps := sp.CurrentPreferenceSet
	rgb := ps.Brightness.Lists.ScaleRGB(STARSListColor)

	drawSectors := func(volumes []sim.ControllerAirspaceVolume) {
		for _, v := range volumes {
			e := math.EmptyExtent2D()

			for _, pts := range v.Boundaries {
				for i := range pts {
					e = math.Union(e, pts[i])
					if i < len(pts)-1 {
						ld.AddLine(pts[i], pts[i+1], rgb)
					}
				}
			}

			center := e.Center()
			ps := sp.CurrentPreferenceSet
			style := renderer.TextStyle{
				Font:           sp.systemFont[ps.CharSize.Tools],
				Color:          rgb,
				DrawBackground: true, // default BackgroundColor is fine
			}
			alts := fmt.Sprintf("%d-%d", v.LowerLimit/100, v.UpperLimit/100)
			td.AddTextCentered(alts, transforms.WindowFromLatLongP(center), style)
		}
	}

	if sp.drawApproachAirspace {
		drawSectors(ctx.ControlClient.ApproachAirspace)
	}

	if sp.drawDepartureAirspace {
		drawSectors(ctx.ControlClient.DepartureAirspace)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) consumeMouseEvents(ctx *PaneContext, ghosts []*av.GhostAircraft,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	if ctx.Mouse == nil {
		return
	}

	mouse := ctx.Mouse
	ps := &sp.CurrentPreferenceSet

	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] && !ctx.HaveFocus {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			sp.events.PostEvent(sim.Event{Type: sim.TrackClickedEvent, Callsign: ac.Callsign})
		}
		ctx.KeyboardFocus.Take(sp)
		return
	}
	if (ctx.Mouse.Clicked[platform.MouseButtonSecondary] || ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
		ctx.KeyboardFocus.Take(sp)
	}

	if activeSpinner == nil && !sp.LockDisplay {
		// Handle dragging the scope center
		if mouse.Dragging[platform.MouseButtonSecondary] {
			delta := mouse.DragDelta
			if delta[0] != 0 || delta[1] != 0 {
				deltaLL := transforms.LatLongFromWindowV(delta)
				ps.CurrentCenter = math.Sub2f(ps.CurrentCenter, deltaLL)
			}
		}

		// Consume mouse wheel
		if mouse.Wheel[1] != 0 {
			r := ps.Range
			if _, ok := ctx.Keyboard.Pressed[platform.KeyControl]; ok {
				ps.Range += 3 * mouse.Wheel[1]
			} else {
				ps.Range += mouse.Wheel[1]
			}
			ps.Range = math.Clamp(ps.Range, 6, 256) // 4-33

			// We want to zoom in centered at the mouse position; this affects
			// the scope center after the zoom, so we'll find the
			// transformation that gives the new center position.
			mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
			scale := ps.Range / r
			centerTransform := math.Identity3x3().
				Translate(mouseLL[0], mouseLL[1]).
				Scale(scale, scale).
				Translate(-mouseLL[0], -mouseLL[1])

			ps.CurrentCenter = centerTransform.TransformPoint(ps.CurrentCenter)
		}
	}

	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		if ctx.Keyboard != nil && ctx.Keyboard.IsPressed(platform.KeyShift) && ctx.Keyboard.IsPressed(platform.KeyControl) {
			// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
			mouseLatLong := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			ctx.Platform.GetClipboard().SetText(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		if ctx.Keyboard != nil && ctx.Keyboard.IsPressed(platform.KeyControl) && !ctx.Keyboard.IsPressed(platform.KeyShift) { // There is a conflict between this and initating a track CRC-style,
			// so making sure that shift isn't being pressed would be a good idea.
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				if state := sp.Aircraft[ac.Callsign]; state != nil {
					state.IsSelected = !state.IsSelected
					return
				}
			}
		}

		// If a scope click handler has been registered, give it the click
		// and then clear it out.
		var status STARSCommandStatus
		if sp.scopeClickHandler != nil {
			status = sp.scopeClickHandler(ctx.Mouse.Pos, transforms)
		} else {
			status = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.Mouse.Pos, ghosts, transforms)
		}

		if status.err != nil {
			sp.displayError(status.err, ctx)
		} else {
			if status.clear {
				sp.resetInputState()
			}
			sp.previewAreaOutput = status.output
		}
	} else if ctx.Mouse.Clicked[platform.MouseButtonTertiary] {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			if state := sp.Aircraft[ac.Callsign]; state != nil {
				state.IsSelected = !state.IsSelected
			}
		}
	} else if !ctx.ControlClient.SimIsPaused {
		switch sp.CurrentPreferenceSet.DwellMode {
		case DwellModeOff:
			sp.dwellAircraft = ""

		case DwellModeOn:
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			} else {
				sp.dwellAircraft = ""
			}

		case DwellModeLock:
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			}
			// Otherwise leave sp.dwellAircraft as is
		}
	} else {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			td := renderer.GetTextDrawBuilder()
			defer renderer.ReturnTextDrawBuilder(td)

			ps := sp.CurrentPreferenceSet
			font := sp.systemFont[ps.CharSize.Datablocks]
			style := renderer.TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: 0}

			// Aircraft track position in window coordinates
			state := sp.Aircraft[ac.Callsign]
			pac := transforms.WindowFromLatLongP(state.TrackPosition())

			// Upper-left corner of where we start drawing the text
			pad := float32(5)
			ptext := math.Add2f([2]float32{2 * pad, 0}, pac)
			info := ac.NavSummary(ctx.Lg)
			td.AddText(info, ptext, style)

			// Draw an alpha-blended quad behind the text to make it more legible.
			trid := renderer.GetTrianglesDrawBuilder()
			defer renderer.ReturnTrianglesDrawBuilder(trid)
			bx, by := font.BoundText(info, style.LineSpacing)
			trid.AddQuad(math.Add2f(ptext, [2]float32{-pad, 0}),
				math.Add2f(ptext, [2]float32{float32(bx) + pad, 0}),
				math.Add2f(ptext, [2]float32{float32(bx) + pad, -float32(by) - pad}),
				math.Add2f(ptext, [2]float32{-pad, -float32(by) - pad}))

			// Get it all into the command buffer
			transforms.LoadWindowViewingMatrices(cb)
			cb.SetRGBA(renderer.RGBA{R: 0.25, G: 0.25, B: 0.25, A: 0.75})
			cb.Blend()
			trid.GenerateCommands(cb)
			cb.DisableBlend()
			td.GenerateCommands(cb)
		}
	}
}

func (sp *STARSPane) drawMouseCursor(ctx *PaneContext, paneExtent math.Extent2D, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	if ctx.Mouse == nil {
		return
	}

	// If the mouse is inside the scope, disable the standard mouse cursor
	// and draw a cross for the cursor; otherwise leave the default arrow
	// for the DCB.
	if ctx.Mouse.Pos[0] >= 0 && ctx.Mouse.Pos[0] < paneExtent.Width() &&
		ctx.Mouse.Pos[1] >= 0 && ctx.Mouse.Pos[1] < paneExtent.Height() {
		ctx.Mouse.SetCursor(imgui.MouseCursorNone)
		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		w := float32(7) * util.Select(runtime.GOOS == "windows", ctx.Platform.DPIScale(), float32(1))
		ld.AddLine(math.Add2f(ctx.Mouse.Pos, [2]float32{-w, 0}), math.Add2f(ctx.Mouse.Pos, [2]float32{w, 0}))
		ld.AddLine(math.Add2f(ctx.Mouse.Pos, [2]float32{0, -w}), math.Add2f(ctx.Mouse.Pos, [2]float32{0, w}))

		transforms.LoadWindowViewingMatrices(cb)
		// STARS Operators Manual 4-74: FDB brightness is used for the cursor
		ps := sp.CurrentPreferenceSet
		cb.SetRGB(ps.Brightness.FullDatablocks.RGB())
		ld.GenerateCommands(cb)
	} else {
		ctx.Mouse.SetCursor(imgui.MouseCursorArrow)
	}
}

///////////////////////////////////////////////////////////////////////////
// DCB menu on top

const STARSButtonSize = 76

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
		panic(fmt.Sprintf("unhandled starsButtonFlags %d", flags))
	}
}

var dcbDrawState struct {
	cb           *renderer.CommandBuffer
	mouse        *platform.MouseState
	mouseDownPos []float32
	cursor       [2]float32
	drawStartPos [2]float32
	style        renderer.TextStyle
	brightness   STARSBrightness
	position     int
}

func (sp *STARSPane) StartDrawDCB(ctx *PaneContext, buttonScale float32, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	dcbDrawState.cb = cb
	dcbDrawState.mouse = ctx.Mouse

	ps := sp.CurrentPreferenceSet
	dcbDrawState.brightness = ps.Brightness.DCB
	dcbDrawState.position = ps.DCBPosition
	switch dcbDrawState.position {
	case DCBPositionTop, DCBPositionLeft:
		dcbDrawState.drawStartPos = [2]float32{0, ctx.PaneExtent.Height()}

	case DCBPositionRight:
		sz := starsButtonSize(STARSButtonFull, buttonScale) // FIXME: there should be a better way to get the default
		dcbDrawState.drawStartPos = [2]float32{ctx.PaneExtent.Width() - sz[0], ctx.PaneExtent.Height()}

	case DCBPositionBottom:
		sz := starsButtonSize(STARSButtonFull, buttonScale)
		dcbDrawState.drawStartPos = [2]float32{0, sz[1]}
	}

	dcbDrawState.cursor = dcbDrawState.drawStartPos

	dcbDrawState.style = renderer.TextStyle{
		Font:        sp.dcbFont[ps.CharSize.DCB],
		Color:       renderer.RGB{1, 1, 1},
		LineSpacing: 0,
	}
	if dcbDrawState.style.Font == nil {
		ctx.Lg.Errorf("nil buttonFont??")
		dcbDrawState.style.Font = renderer.GetDefaultFont()
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.Platform.DPIScale())

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		dcbDrawState.mouseDownPos = ctx.Mouse.Pos[:]
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
		if mouse.Released[platform.MouseButtonPrimary] {
			dcbDrawState.mouseDownPos = nil
		}
	}
}

func drawDCBText(text string, td *renderer.TextDrawBuilder, buttonSize [2]float32, color renderer.RGB) {
	// Clean up the text
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}

	style := dcbDrawState.style
	style.Color = renderer.LerpRGB(.5, color, dcbDrawState.brightness.ScaleRGB(color))
	_, h := style.Font.BoundText(strings.Join(lines, "\n"), dcbDrawState.style.LineSpacing)

	slop := buttonSize[1] - float32(h) // todo: what if negative...
	y0 := dcbDrawState.cursor[1] - 1 - slop/2
	for _, line := range lines {
		lw, lh := style.Font.BoundText(line, style.LineSpacing)
		// Try to center the text, though if it's too big to fit in the
		// button then draw it starting from the left edge of the button so
		// that the trailing characters are the ones that are lost.
		x0 := dcbDrawState.cursor[0] + math.Max(1, (buttonSize[0]-float32(lw))/2)

		td.AddText(line, [2]float32{x0, y0}, style)
		y0 -= float32(lh)
	}
}

func drawDCBButton(ctx *PaneContext, text string, flags int, buttonScale float32, pushedIn bool, disabled bool) (math.Extent2D, bool) {
	ld := renderer.GetColoredLinesDrawBuilder()
	trid := renderer.GetColoredTrianglesDrawBuilder()
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	defer renderer.ReturnTextDrawBuilder(td)

	sz := starsButtonSize(flags, buttonScale)

	// Offset for spacing
	const delta = 1
	p0 := math.Add2f(dcbDrawState.cursor, [2]float32{delta, -delta})
	p1 := math.Add2f(p0, [2]float32{sz[0] - 2*delta, 0})
	p2 := math.Add2f(p1, [2]float32{0, -sz[1] + 2*delta})
	p3 := math.Add2f(p2, [2]float32{-sz[0] + 2*delta, 0})

	ext := math.Extent2DFromPoints([][2]float32{p0, p2})
	mouse := dcbDrawState.mouse
	mouseInside := mouse != nil && ext.Inside(mouse.Pos)
	mouseDownInside := dcbDrawState.mouseDownPos != nil &&
		ext.Inside([2]float32{dcbDrawState.mouseDownPos[0], dcbDrawState.mouseDownPos[1]})

	var buttonColor, textColor renderer.RGB
	if disabled {
		buttonColor = STARSDCBDisabledButtonColor
		textColor = STARSDCBDisabledTextColor
	}
	if !disabled {
		if mouseInside && mouseDownInside {
			pushedIn = !pushedIn
		}

		// Swap selected/regular color to indicate the tentative result
		buttonColor = util.Select(pushedIn, STARSDCBActiveButtonColor, STARSDCBButtonColor)
		textColor = util.Select(mouseInside, STARSDCBTextSelectedColor, STARSDCBTextColor)
	}
	buttonColor = dcbDrawState.brightness.ScaleRGB(buttonColor)
	//textColor = dcbDrawState.brightness.ScaleRGB(textColor)

	trid.AddQuad(p0, p1, p2, p3, buttonColor)
	drawDCBText(text, td, sz, textColor)

	if !disabled && pushedIn { //((selected && !mouseInside) || (!selected && mouseInside && mouse.Down[MouseButtonPrimary])) {
		// Depressed bevel scheme: darker top/left, highlight bottom/right
		ld.AddLine(p0, p1, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p0, p3, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p1, p2, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p2, p3, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
	} else {
		// Normal bevel scheme: highlight top and left, darker bottom and right
		ld.AddLine(p0, p1, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p0, p3, renderer.LerpRGB(.25, buttonColor, renderer.RGB{1, 1, 1}))
		ld.AddLine(p1, p2, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
		ld.AddLine(p2, p3, renderer.LerpRGB(.5, buttonColor, renderer.RGB{0, 0, 0}))
	}

	// Scissor to just the extent of the button. Note that we need to give
	// this in window coordinates, not our local pane coordinates, so
	// translating by ctx.PaneExtent.p0 is needed...
	winBase := math.Add2f(dcbDrawState.cursor, ctx.PaneExtent.P0)
	dcbDrawState.cb.SetScissorBounds(math.Extent2D{
		P0: [2]float32{winBase[0], winBase[1] - sz[1]},
		P1: [2]float32{winBase[0] + sz[0], winBase[1]},
	}, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])

	updateDCBCursor(flags, sz, ctx)

	// Text last!
	trid.GenerateCommands(dcbDrawState.cb)
	ld.GenerateCommands(dcbDrawState.cb)
	td.GenerateCommands(dcbDrawState.cb)

	if mouse != nil && mouseInside && mouse.Released[platform.MouseButtonPrimary] && mouseDownInside {
		return ext, true /* clicked and released */
	}
	return ext, false
}

func updateDCBCursor(flags int, sz [2]float32, ctx *PaneContext) {
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
			ctx.Lg.Errorf("unhandled starsButtonFlags %d", flags)
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
			ctx.Lg.Errorf("unhandled starsButtonFlags %d", flags)
			dcbDrawState.cursor[0] = dcbDrawState.drawStartPos[0]
			dcbDrawState.cursor[1] -= sz[0]
		}
	}
}

func STARSToggleButton(ctx *PaneContext, text string, state *bool, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, *state, false)

	if clicked {
		*state = !*state
	}

	return clicked
}

// TODO: think about implications of multiple STARSPanes being active
// at once w.r.t. this.  This probably should be a member variable,
// though we also need to think about focus capture; probably should
// force take it when a spinner is active..
var activeSpinner DCBSpinner

// DrawDCBSpinner draws the provided spinner at the current location in the
// DCB. It handles mouse capture (and release) and passing mouse wheel
// events to the spinner.
func (sp *STARSPane) DrawDCBSpinner(ctx *PaneContext, spinner DCBSpinner, commandMode CommandMode, flags int, buttonScale float32) {
	if activeSpinner != nil && spinner.Equals(activeSpinner) {
		// This spinner is active.
		buttonBounds, clicked := drawDCBButton(ctx, spinner.Label(), flags, buttonScale, true, false)
		// This is horrific and one of many ugly things about capturing the
		// mouse, but most of Panes' work is in the simplified space of a
		// pane coordinate system; here we need something in terms of
		// window coordinates, so need to both account for the viewport
		// call that lets us draw things oblivious to the menubar as well
		// as flip things in y.
		h := ctx.PaneExtent.Height() + ctx.MenuBarHeight
		buttonBounds.P0[1], buttonBounds.P1[1] = h-buttonBounds.P1[1], h-buttonBounds.P0[1]
		ctx.Platform.StartCaptureMouse(buttonBounds)

		if clicked {
			activeSpinner = nil
			ctx.Platform.EndCaptureMouse()
			sp.commandMode = CommandModeNone
		}

		if ctx.Mouse != nil && ctx.Mouse.Wheel[1] != 0 {
			delta := util.Select(ctx.Mouse.Wheel[1] > 0, -1, 1)
			spinner.MouseWheel(delta)
		}
	} else {
		// The spinner is not active; draw it (and check if it was clicked...)
		_, clicked := drawDCBButton(ctx, spinner.Label(), flags, buttonScale, false, false)
		if clicked {
			activeSpinner = spinner
			sp.resetInputState()
			sp.commandMode = commandMode
		}
	}
}

type DCBRadarRangeSpinner struct {
	r *float32
}

func MakeRadarRangeSpinner(r *float32) DCBSpinner {
	return &DCBRadarRangeSpinner{r}
}

func (s DCBRadarRangeSpinner) Label() string {
	return "RANGE\n" + strconv.Itoa(int(*s.r+0.5)) // print it as an int
}

func (s DCBRadarRangeSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBRadarRangeSpinner)
	return ok && r.r == s.r
}

func (s *DCBRadarRangeSpinner) MouseWheel(delta int) {
	*s.r = math.Clamp(*s.r+float32(delta), 6, 256)
}

func (s *DCBRadarRangeSpinner) KeyboardInput(text string) error {
	// 4-33
	if r, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if r < 6 || r > 256 {
		return ErrSTARSRangeLimit
	} else {
		// Input numbers are ints but we store a float (for smoother
		// stepping when the mouse wheel is used to zoom the scope).
		*s.r = float32(r)
		return nil
	}
}

// DCBIntegerRangeSpinner is a generic implementation of DCBSpinner for
// managing integers in steps of 1 within a given range.
type DCBIntegerRangeSpinner struct {
	text     string
	value    *int
	min, max int
}

func MakeIntegerRangeSpinner(t string, v *int, min, max int) DCBSpinner {
	return &DCBIntegerRangeSpinner{text: t, value: v, min: min, max: max}
}

func (s *DCBIntegerRangeSpinner) Label() string {
	return s.text + strconv.Itoa(*s.value)
}

func (s *DCBIntegerRangeSpinner) Equals(other DCBSpinner) bool {
	ir, ok := other.(*DCBIntegerRangeSpinner)
	return ok && ir.value == s.value
}

func (s *DCBIntegerRangeSpinner) MouseWheel(delta int) {
	*s.value = math.Clamp(*s.value+delta, s.min, s.max)
}

func (s *DCBIntegerRangeSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < s.min || v > s.max {
		return ErrSTARSRangeLimit
	} else {
		*s.value = v
		return nil
	}
}

// Leader lines are integers between 0 and 7 so the IntegerRangeSpinner
// fits.
func MakeLeaderLineLengthSpinner(l *int) DCBSpinner {
	return MakeIntegerRangeSpinner("LDR\n", l, 0, 7)
}

type DCBLeaderLineDirectionSpinner struct {
	d *math.CardinalOrdinalDirection
}

func MakeLeaderLineDirectionSpinner(dir *math.CardinalOrdinalDirection) DCBSpinner {
	return &DCBLeaderLineDirectionSpinner{dir}
}

func (s *DCBLeaderLineDirectionSpinner) Label() string {
	return "LDR DIR\n" + s.d.ShortString()
}

func (s *DCBLeaderLineDirectionSpinner) Equals(other DCBSpinner) bool {
	l, ok := other.(*DCBLeaderLineDirectionSpinner)
	return ok && l.d == s.d
}

func (s *DCBLeaderLineDirectionSpinner) MouseWheel(delta int) {
	// The CardinalOrdinalDirection enum goes clockwise, so adding one (mod
	// 8) goes forward, and subtracting 1 (mod 8) goes backwards.
	if delta < 0 {
		*s.d = math.CardinalOrdinalDirection((*s.d + 7) % 8)
	} else {
		*s.d = math.CardinalOrdinalDirection((*s.d + 1) % 8)
	}
}

func (s *DCBLeaderLineDirectionSpinner) KeyboardInput(text string) error {
	if len(text) > 1 {
		return ErrSTARSCommandFormat
	} else if dir, ok := numpadToDirection(text[0]); !ok || dir == nil /* entered 5 */ {
		return ErrSTARSCommandFormat
	} else {
		*s.d = *dir
		return nil
	}
}

type DCBHistoryRateSpinner struct {
	r *float32
}

func MakeHistoryRateSpinner(r *float32) DCBSpinner {
	return &DCBHistoryRateSpinner{r}
}

func (s *DCBHistoryRateSpinner) Label() string {
	return "H_RATE\n" + fmt.Sprintf("%.1f", *s.r)
}

func (s *DCBHistoryRateSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBHistoryRateSpinner)
	return ok && r.r == s.r
}

func (s *DCBHistoryRateSpinner) MouseWheel(delta int) {
	// 4-94 the spinner goes in steps of 0.5.
	if delta > 0 {
		*s.r = math.Clamp(*s.r+0.5, 0, 4.5)
	} else if delta < 0 {
		*s.r = math.Clamp(*s.r-0.5, 0, 4.5)
	}
}

func (s *DCBHistoryRateSpinner) KeyboardInput(text string) error {
	// 4-94: however, for keyboard input, values in the range 0-4.5 in
	// increments of 0.1 are allowed.

	// Simple specialized parser to make it easier to validate (versus
	// using strconv.ParseFloat and then having to verify it's a valid
	// value.)
	whole, frac, ok := strings.Cut(text, ".")
	if !ok {
		frac = "0"
	}

	// Make sure we have a single digit for the whole part and the
	// fractional part.
	if len(whole) != 1 || whole[0] < '0' || whole[0] > '9' {
		return ErrSTARSIllegalValue
	}
	if len(frac) != 1 || frac[0] < '0' || frac[0] > '9' {
		return ErrSTARSIllegalValue
	}

	// Convert it to a float
	if value := float32(whole[0]-'0') + float32(frac[0]-'0')/10; value > 4.5 {
		return ErrSTARSIllegalValue
	} else {
		*s.r = value
		return nil
	}
}

type DCBPTLLengthSpinner struct {
	l *float32
}

func MakePTLLengthSpinner(l *float32) DCBSpinner {
	return &DCBPTLLengthSpinner{l}
}

func (s *DCBPTLLengthSpinner) Label() string {
	return "PTL\nLNTH\n" + fmt.Sprintf("%.1f", *s.l)
}

func (s *DCBPTLLengthSpinner) Equals(other DCBSpinner) bool {
	p, ok := other.(*DCBPTLLengthSpinner)
	return ok && p.l == s.l
}

func (s *DCBPTLLengthSpinner) MouseWheel(delta int) {
	// 6-16: PTLs are between 0 and 5 minutes, specified in 0.5 minute
	// increments.
	if delta > 0 {
		*s.l = math.Min(*s.l+0.5, 5)
	} else if delta < 0 {
		*s.l = math.Max(*s.l-0.5, 0)
	}
}

func (s *DCBPTLLengthSpinner) KeyboardInput(text string) error {
	// Here we'll just parse it as a float and then validate it.
	if v, err := strconv.ParseFloat(text, 32); err != nil {
		return ErrSTARSCommandFormat
	} else if v < 0 || v > 5 {
		// out of range
		return ErrSTARSCommandFormat
	} else if float64(int(v)) != v && float64(int(v))+0.5 != v {
		// Not a whole number or a decimal x.5
		return ErrSTARSCommandFormat
	} else {
		*s.l = float32(v)
		return nil
	}
}

type DCBDwellModeSpinner struct {
	m *DwellMode
}

func MakeDwellModeSpinner(m *DwellMode) DCBSpinner {
	return &DCBDwellModeSpinner{m}
}

func (s *DCBDwellModeSpinner) Label() string {
	return "DWELL\n" + s.m.String()
}

func (s *DCBDwellModeSpinner) Equals(other DCBSpinner) bool {
	d, ok := other.(*DCBDwellModeSpinner)
	return ok && s.m == d.m
}

func (s *DCBDwellModeSpinner) MouseWheel(delta int) {
	if delta > 0 {
		// Cycle through the modes Off -> On -> Lock
		*s.m = [3]DwellMode{DwellModeOff: DwellModeOn,
			DwellModeOn:   DwellModeLock,
			DwellModeLock: DwellModeLock}[*s.m]
	} else if delta < 0 {
		// Cycle: Lock-> On -> Off
		*s.m = [3]DwellMode{DwellModeOff: DwellModeOff,
			DwellModeOn:   DwellModeOff,
			DwellModeLock: DwellModeOn}[*s.m]
	}
}

func (s *DCBDwellModeSpinner) KeyboardInput(text string) error {
	// 4-109
	switch text {
	case "0":
		*s.m = DwellModeOff
		return nil
	case "1":
		*s.m = DwellModeOn
		return nil
	case "2":
		*s.m = DwellModeLock
		return nil
	default:
		return ErrSTARSIllegalValue
	}
}

type DCBRangeRingRadiusSpinner struct {
	r *int
}

func MakeRangeRingRadiusSpinner(radius *int) DCBSpinner {
	return &DCBRangeRingRadiusSpinner{radius}
}

func (s *DCBRangeRingRadiusSpinner) Label() string {
	return "RR\n" + strconv.Itoa(*s.r)
}

func (s *DCBRangeRingRadiusSpinner) Equals(other DCBSpinner) bool {
	r, ok := other.(*DCBRangeRingRadiusSpinner)
	return ok && r.r == s.r
}

func (s *DCBRangeRingRadiusSpinner) MouseWheel(delta int) {
	// Range rings have 2, 5, 10, or 20 miles radii..
	if delta > 0 {
		switch *s.r {
		case 2:
			*s.r = 5
		case 5:
			*s.r = 10
		case 10:
			*s.r = 20
		}
	} else {
		switch *s.r {
		case 5:
			*s.r = 2
		case 10:
			*s.r = 5
		case 20:
			*s.r = 10
		}
	}
}

func (s *DCBRangeRingRadiusSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v != 2 && v != 5 && v != 10 && v != 20 {
		return ErrSTARSIllegalValue
	} else {
		*s.r = v
		return nil
	}
}

// DCBBrightnessSpinner handles spinners in the BRITE menu
type DCBBrightnessSpinner struct {
	text     string
	b        *STARSBrightness
	min      STARSBrightness
	allowOff bool
}

func MakeBrightnessSpinner(t string, b *STARSBrightness, min STARSBrightness, allowOff bool) DCBSpinner {
	return &DCBBrightnessSpinner{text: t, b: b, min: min, allowOff: allowOff}
}

func (s *DCBBrightnessSpinner) Label() string {
	return s.text + " " + util.Select(*s.b == 0, "OFF", fmt.Sprintf("%2d", int(*s.b)))
}

func (s *DCBBrightnessSpinner) Equals(other DCBSpinner) bool {
	b, ok := other.(*DCBBrightnessSpinner)
	return ok && b.b == s.b
}

func (s *DCBBrightnessSpinner) MouseWheel(delta int) {
	*s.b += STARSBrightness(5 * delta)
	if *s.b < s.min && s.allowOff {
		*s.b = STARSBrightness(0)
	} else {
		*s.b = STARSBrightness(math.Clamp(*s.b, s.min, 100))
	}
}

func (s *DCBBrightnessSpinner) KeyboardInput(text string) error {
	if v, err := strconv.Atoi(text); err != nil {
		return ErrSTARSCommandFormat
	} else if v < int(s.min) || v > 100 || (v == 0 && !s.allowOff) {
		return ErrSTARSIllegalValue
	} else {
		*s.b = STARSBrightness(v)
		return nil
	}
}

func STARSSelectButton(ctx *PaneContext, text string, flags int, buttonScale float32) bool {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, flags&STARSButtonSelected != 0, false)
	return clicked
}

func (sp *STARSPane) STARSPlaceButton(ctx *PaneContext, text string, flags int, buttonScale float32,
	callback func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus) {
	_, clicked := drawDCBButton(ctx, text, flags, buttonScale, text == sp.selectedPlaceButton, false)
	if clicked {
		sp.selectedPlaceButton = text
		sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) STARSCommandStatus {
			sp.selectedPlaceButton = ""
			return callback(pw, transforms)
		}
	}
}

func STARSDisabledButton(ctx *PaneContext, text string, flags int, buttonScale float32) {
	drawDCBButton(ctx, text, flags, buttonScale, false, true)
}

///////////////////////////////////////////////////////////////////////////
// STARSPane utility methods

// amendFlightPlan is a useful utility function for changing an entry in
// the flightplan; the provided callback function should make the update
// and the rest of the details are handled here.
func amendFlightPlan(ctx *PaneContext, callsign string, amend func(fp *av.FlightPlan)) error {
	if ac := ctx.ControlClient.Aircraft[callsign]; ac == nil {
		return av.ErrNoAircraftForCallsign
	} else {
		fp := util.Select(ac.FlightPlan != nil, ac.FlightPlan, &av.FlightPlan{})
		amend(fp)
		return ctx.ControlClient.AmendFlightPlan(callsign, *fp)
	}
}

func (sp *STARSPane) initializeFonts() {
	sp.systemFont[0] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize0", Size: 11})
	sp.systemFont[1] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize1", Size: 12})
	sp.systemFont[2] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize2", Size: 15})
	sp.systemFont[3] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize3", Size: 16})
	sp.systemFont[4] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize4", Size: 18})
	sp.systemFont[5] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize5", Size: 19})
	sp.systemOutlineFont[0] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize0", Size: 11})
	sp.systemOutlineFont[1] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize1", Size: 12})
	sp.systemOutlineFont[2] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize2", Size: 15})
	sp.systemOutlineFont[3] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize3", Size: 16})
	sp.systemOutlineFont[4] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize4", Size: 18})
	sp.systemOutlineFont[5] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharOutlineFontSetBSize5", Size: 19})
	sp.dcbFont[0] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize0", Size: 11})
	sp.dcbFont[1] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize1", Size: 12})
	sp.dcbFont[2] = renderer.GetFont(renderer.FontIdentifier{Name: "sddCharFontSetBSize2", Size: 15})
}

func (sp *STARSPane) resetInputState() {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.scopeClickHandler = nil
	sp.selectedPlaceButton = ""
}

func (sp *STARSPane) displayError(err error, ctx *PaneContext) {
	if err != nil { // it should be, but...
		sp.playOnce(ctx.Platform, AudioCommandError)
		sp.previewAreaOutput = GetSTARSError(err, ctx.Lg).Error()
	}
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

func (sp *STARSPane) radarVisibility(radarSites map[string]*av.RadarSite, pos math.Point2LL, alt int) (primary, secondary bool, distance float32) {
	ps := sp.CurrentPreferenceSet
	distance = 1e30
	single := sp.radarMode(radarSites) == RadarModeSingle
	for id, site := range radarSites {
		if single && ps.RadarSiteSelected != id {
			continue
		}

		if p, s, dist := site.CheckVisibility(pos, alt); p || s {
			primary = primary || p
			secondary = secondary || s
			distance = math.Min(distance, dist)
		}
	}

	return
}

func (sp *STARSPane) visibleAircraft(ctx *PaneContext) []*av.Aircraft {
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

				if sp.AutoTrackDepartures && ac.TrackingController == "" &&
					ctx.ControlClient.DepartureController(ac, ctx.Lg) == ctx.ControlClient.Callsign {
					ctx.ControlClient.InitiateTrack(callsign, nil, nil) // ignore error...
				}
			}
		}
	}

	return aircraft
}

func (sp *STARSPane) datablockVisible(ac *av.Aircraft, ctx *PaneContext) bool {
	af := sp.CurrentPreferenceSet.AltitudeFilters
	alt := sp.Aircraft[ac.Callsign].TrackAltitude()
	if ac.TrackingController == ctx.ControlClient.Callsign {
		// For owned datablocks
		return true
	} else if ac.HandoffTrackController == ctx.ControlClient.Callsign {
		// For recieving handoffs
		return true
	} else if ac.ControllingController == ctx.ControlClient.Callsign {
		// For non-greened handoffs
		return true
	} else if sp.Aircraft[ac.Callsign].PointedOut {
		// Pointouts: This is if its been accepted,
		// for an incoming pointout, it falls to the FDB check
		return true
	} else if ok, _ := av.SquawkIsSPC(ac.Squawk); ok {
		// Special purpose codes
		return true
	} else if sp.Aircraft[ac.Callsign].DatablockType == FullDatablock {
		// If FDB, may trump others but idc
		// This *should* be primarily doing CA and ATPA cones
		return true
	} else if sp.isOverflight(ctx, ac) && sp.CurrentPreferenceSet.OverflightFullDatablocks { //Need a f7 + e
		// Overflights
		return true
	} else if sp.CurrentPreferenceSet.QuickLookAll {
		// Quick look all
		return true
	} else if ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
		// Redirected to
		return true
	} else if slices.Contains(ac.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign) {
		// Had it but redirected it
		return true
	}

	// Quick Look Positions.
	for _, quickLookPositions := range sp.CurrentPreferenceSet.QuickLookPositions {
		if ac.TrackingController == quickLookPositions.Callsign {
			return true
		}
	}

	if !ac.IsAssociated() {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) getLeaderLineDirection(ac *av.Aircraft, ctx *PaneContext) math.CardinalOrdinalDirection {
	ps := sp.CurrentPreferenceSet
	state := sp.Aircraft[ac.Callsign]

	if state.UseGlobalLeaderLine {
		return *state.GlobalLeaderLineDirection
	} else if state.LeaderLineDirection != nil {
		// The direction was specified for the aircraft specifically
		return *state.LeaderLineDirection
	} else if ac.TrackingController == ctx.ControlClient.Callsign {
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
		return math.CardinalOrdinalDirection(math.North)
	}
}

func (sp *STARSPane) getLeaderLineVector(dir math.CardinalOrdinalDirection) [2]float32 {
	angle := dir.Heading()
	v := [2]float32{math.Sin(math.Radians(angle)), math.Cos(math.Radians(angle))}
	ps := sp.CurrentPreferenceSet
	return math.Scale2f(v, float32(10+10*ps.LeaderLineLength))
}

func (sp *STARSPane) isOverflight(ctx *PaneContext, ac *av.Aircraft) bool {
	return ac.FlightPlan != nil &&
		ctx.ControlClient.Airports[ac.FlightPlan.DepartureAirport] == nil &&
		ctx.ControlClient.Airports[ac.FlightPlan.ArrivalAirport] == nil
}

func (sp *STARSPane) tryGetClosestAircraft(ctx *PaneContext, mousePosition [2]float32, transforms ScopeTransformations) (*av.Aircraft, float32) {
	var ac *av.Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft(ctx) {
		pw := transforms.WindowFromLatLongP(sp.Aircraft[a.Callsign].TrackPosition())
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac, distance
}

func (sp *STARSPane) tryGetClosestGhost(ghosts []*av.GhostAircraft, mousePosition [2]float32, transforms ScopeTransformations) (*av.GhostAircraft, float32) {
	var ghost *av.GhostAircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, g := range ghosts {
		pw := transforms.WindowFromLatLongP(g.Position)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			ghost = g
			distance = dist
		}
	}

	return ghost, distance
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

		sp.audioEffects[AudioConflictAlert] = loadMP3("ca.mp3")
		sp.audioEffects[AudioEmergencySquawk] = loadMP3("emergency.mp3")
		sp.audioEffects[AudioMinimumSafeAltitudeWarning] = loadMP3("msaw.mp3")
		sp.audioEffects[AudioModeCIntruder] = loadMP3("intruder.mp3")
		sp.audioEffects[AudioInboundHandoff] = loadMP3("263124__pan14__sine-octaves-up-beep.mp3")
		sp.audioEffects[AudioCommandError] = loadMP3("426888__thisusernameis__beep4.mp3")
		sp.audioEffects[AudioHandoffAccepted] = loadMP3("321104__nsstudios__blip2.mp3")
	}
}

func (sp *STARSPane) playOnce(p platform.Platform, a AudioType) {
	if sp.CurrentPreferenceSet.AudioEffectEnabled[a] {
		p.PlayAudioOnce(sp.audioEffects[a])
	}
}

func (sp *STARSPane) startPlayContinuous(p platform.Platform, a AudioType) {
	if sp.CurrentPreferenceSet.AudioEffectEnabled[a] {
		p.StartPlayAudioContinuous(sp.audioEffects[a])
	}
}

func (sp *STARSPane) stopPlayContinuous(p platform.Platform, a AudioType) {
	p.StopPlayAudioContinuous(sp.audioEffects[a])
}

func (sp *STARSPane) drawScenarioRoutes(ctx *PaneContext, transforms ScopeTransformations, font *renderer.Font, color renderer.RGB,
	cb *renderer.CommandBuffer) {
	drawArrivals := ctx.ControlClient.ScopeDrawArrivals()
	drawApproaches := ctx.ControlClient.ScopeDrawApproaches()
	drawDepartures := ctx.ControlClient.ScopeDrawDepartures()

	if len(drawArrivals) == 0 && len(drawApproaches) == 0 && len(drawDepartures) == 0 {
		return
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	pd := renderer.GetTrianglesDrawBuilder() // for circles
	defer renderer.ReturnTrianglesDrawBuilder(pd)
	ldr := renderer.GetLinesDrawBuilder() // for restrictions--in window coords...
	defer renderer.ReturnLinesDrawBuilder(ldr)

	// Track which waypoints have been drawn so that we don't repeatedly
	// draw the same one.  (This is especially important since the
	// placement of the labels depends on the inbound/outbound segments,
	// which may be different for different uses of the waypoint...)
	drawnWaypoints := make(map[string]interface{})

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	// STARS
	if drawArrivals != nil {
		for _, name := range util.SortedMapKeys(ctx.ControlClient.ArrivalGroups) {
			if drawArrivals[name] == nil {
				continue
			}

			arrivals := ctx.ControlClient.ArrivalGroups[name]
			for i, arr := range arrivals {
				if drawArrivals == nil || !drawArrivals[name][i] {
					continue
				}

				drawWaypoints(ctx, arr.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr)

				// Draw runway-specific waypoints
				for _, ap := range util.SortedMapKeys(arr.RunwayWaypoints) {
					for _, rwy := range util.SortedMapKeys(arr.RunwayWaypoints[ap]) {
						wp := arr.RunwayWaypoints[ap][rwy]
						drawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr)

						if len(wp) > 1 {
							// Draw the runway number in the middle of the line
							// between the first two waypoints.
							pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
						} else if wp[0].Heading != 0 {
							// This should be the only other case... The heading arrow is drawn
							// up to 2nm out, so put the runway 1nm along its axis.
							a := math.Radians(float32(wp[0].Heading) - ctx.ControlClient.MagneticVariation)
							v := [2]float32{math.Sin(a), math.Cos(a)}
							pend := math.LL2NM(wp[0].Location, ctx.ControlClient.NmPerLongitude)
							pend = math.Add2f(pend, v)
							pell := math.NM2LL(pend, ctx.ControlClient.NmPerLongitude)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pell), style)
						}
					}
				}
			}
		}
	}

	// Approaches
	if drawApproaches != nil {
		for _, rwy := range ctx.ControlClient.ArrivalRunways {
			if drawApproaches[rwy.Airport] == nil {
				continue
			}
			ap := ctx.ControlClient.Airports[rwy.Airport]
			for _, name := range util.SortedMapKeys(ap.Approaches) {
				appr := ap.Approaches[name]
				if appr.Runway == rwy.Runway && drawApproaches[rwy.Airport][name] {
					for _, wp := range appr.Waypoints {
						drawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr)
					}
				}
			}
		}
	}

	// Departure routes
	if drawDepartures != nil {
		for _, name := range util.SortedMapKeys(ctx.ControlClient.Airports) {
			if drawDepartures[name] == nil {
				continue
			}

			ap := ctx.ControlClient.Airports[name]
			for _, rwy := range util.SortedMapKeys(ap.DepartureRoutes) {
				if drawDepartures[name][rwy] == nil {
					continue
				}

				exitRoutes := ap.DepartureRoutes[rwy]
				for _, exit := range util.SortedMapKeys(exitRoutes) {
					if drawDepartures[name][rwy][exit] {
						drawWaypoints(ctx, exitRoutes[exit].Waypoints, drawnWaypoints, transforms,
							td, style, ld, pd, ldr)
					}
				}
			}
		}
	}

	// And now finally update the command buffer with everything we've
	// drawn.
	cb.SetRGB(color)
	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(2, ctx.Platform.DPIScale())
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	pd.GenerateCommands(cb)
	td.GenerateCommands(cb)
	cb.LineWidth(1, ctx.Platform.DPIScale())
	ldr.GenerateCommands(cb)
}

// pt should return nm-based coordinates
func calculateOffset(font *renderer.Font, pt func(int) ([2]float32, bool)) [2]float32 {
	prev, pok := pt(-1)
	cur, _ := pt(0)
	next, nok := pt(1)

	vecAngle := func(p0, p1 [2]float32) float32 {
		v := math.Normalize2f(math.Sub2f(p1, p0))
		return math.Atan2(v[0], v[1])
	}

	const Pi = 3.1415926535
	angle := float32(0)
	if !pok {
		if !nok {
			// wtf?
		}
		// first point
		angle = vecAngle(cur, next)
	} else if !nok {
		// last point
		angle = vecAngle(prev, cur)
	} else {
		// have both prev and next
		angle = (vecAngle(prev, cur) + vecAngle(cur, next)) / 2 // ??
	}

	if angle < 0 {
		angle -= Pi / 2
	} else {
		angle += Pi / 2
	}

	offset := math.Scale2f([2]float32{math.Sin(angle), math.Cos(angle)}, 8)

	h := math.NormalizeHeading(math.Degrees(angle))
	if (h >= 160 && h < 200) || (h >= 340 || h < 20) {
		// Center(ish) the text if the line is more or less horizontal.
		offset[0] -= 2.5 * float32(font.Size)
	}
	return offset
}

func drawWaypoints(ctx *PaneContext, waypoints []av.Waypoint, drawnWaypoints map[string]interface{},
	transforms ScopeTransformations, td *renderer.TextDrawBuilder, style renderer.TextStyle,
	ld *renderer.LinesDrawBuilder, pd *renderer.TrianglesDrawBuilder, ldr *renderer.LinesDrawBuilder) {

	// Draw an arrow at the point p (in nm coordinates) pointing in the
	// direction given by the angle a.
	drawArrow := func(p [2]float32, a float32) {
		aa := a + math.Radians(180+30)
		pa := math.Add2f(p, math.Scale2f([2]float32{math.Sin(aa), math.Cos(aa)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.ControlClient.NmPerLongitude), math.NM2LL(pa, ctx.ControlClient.NmPerLongitude))

		ba := a - math.Radians(180+30)
		pb := math.Add2f(p, math.Scale2f([2]float32{math.Sin(ba), math.Cos(ba)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.ControlClient.NmPerLongitude), math.NM2LL(pb, ctx.ControlClient.NmPerLongitude))
	}

	for i, wp := range waypoints {
		if wp.Heading != 0 {
			// Don't draw a segment to the next waypoint (if there is one)
			// but instead draw an arrow showing the heading.
			a := math.Radians(float32(wp.Heading) - ctx.ControlClient.MagneticVariation)
			v := [2]float32{math.Sin(a), math.Cos(a)}
			v = math.Scale2f(v, 2)
			pend := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
			pend = math.Add2f(pend, v)

			// center line
			ld.AddLine(waypoints[i].Location, math.NM2LL(pend, ctx.ControlClient.NmPerLongitude))

			// arrowhead at the end
			drawArrow(pend, a)
		} else if i+1 < len(waypoints) {
			if wp.Arc != nil {
				// Draw DME arc. One subtlety is that although the arc's
				// radius should cause it to pass through the waypoint, it
				// may be slightly off due to error from using nm
				// coordinates and the approximation of a fixed nm per
				// longitude value.  So, we'll compute the radius to the
				// point in nm coordinates and store it in r0 and do the
				// same for the end point. Then we will interpolate those
				// radii along the arc.
				pc := math.LL2NM(wp.Arc.Center, ctx.ControlClient.NmPerLongitude)
				p0 := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
				r0 := math.Distance2f(p0, pc)
				v0 := math.Normalize2f(math.Sub2f(p0, pc))
				a0 := math.NormalizeHeading(math.Degrees(math.Atan2(v0[0], v0[1]))) // angle w.r.t. the arc center

				p1 := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
				r1 := math.Distance2f(p1, pc)
				v1 := math.Normalize2f(math.Sub2f(p1, pc))
				a1 := math.NormalizeHeading(math.Degrees(math.Atan2(v1[0], v1[1])))

				// Draw a segment every degree
				n := int(math.HeadingDifference(a0, a1))
				a := a0
				pprev := waypoints[i].Location
				for i := 1; i < n-1; i++ {
					if wp.Arc.Clockwise {
						a += 1
					} else {
						a -= 1
					}
					a = math.NormalizeHeading(a)
					r := math.Lerp(float32(i)/float32(n), r0, r1)
					v := math.Scale2f([2]float32{math.Sin(math.Radians(a)), math.Cos(math.Radians(a))}, r)
					pnext := math.NM2LL(math.Add2f(pc, v), ctx.ControlClient.NmPerLongitude)
					ld.AddLine(pprev, pnext)
					pprev = pnext

					if i == n/2 {
						// Draw an arrow at the midpoint showing the arc's direction
						drawArrow(math.Add2f(pc, v), util.Select(wp.Arc.Clockwise, math.Radians(a+90), math.Radians(a-90)))
					}
				}
				ld.AddLine(pprev, waypoints[i+1].Location)
			} else {
				// Regular segment between waypoints: draw the line
				ld.AddLine(waypoints[i].Location, waypoints[i+1].Location)

				if waypoints[i+1].ProcedureTurn == nil {
					// Draw an arrow indicating direction of flight along
					// the segment, unless the next waypoint has a
					// procedure turn. In that case, we'll let the PT draw
					// the arrow..
					p0 := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
					p1 := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
					v := math.Sub2f(p1, p0)
					drawArrow(math.Mid2f(p0, p1), math.Atan2(v[0], v[1]))
				}
			}
		}

		if pt := wp.ProcedureTurn; pt != nil {
			if i+1 >= len(waypoints) {
				ctx.Lg.Errorf("Expected another waypoint after the procedure turn?")
			} else {
				// In the following, we will generate points a canonical
				// racetrack vertically-oriented, with width 2, and with
				// the origin at the left side of the arc at the top.  The
				// toNM transformation takes that to nm coordinates which
				// we'll later transform to lat-long to draw on the scope.
				toNM := math.Identity3x3()

				pnm := math.LL2NM(wp.Location, ctx.ControlClient.NmPerLongitude)
				toNM = toNM.Translate(pnm[0], pnm[1])

				p1nm := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
				v := math.Sub2f(p1nm, pnm)
				hdg := math.Atan2(v[0], v[1])
				toNM = toNM.Rotate(-hdg)
				if !pt.RightTurns {
					toNM = toNM.Translate(-2, 0)
				}

				// FIXME: reuse the logic in nav.go to compute the leg lengths.
				len := float32(pt.NmLimit)
				if len == 0 {
					len = float32(pt.MinuteLimit * 3) // assume 180 GS...
				}
				if len == 0 {
					len = 4
				}

				var lines [][2][2]float32
				// Lines for the two sides
				lines = append(lines,
					[2][2]float32{
						toNM.TransformPoint([2]float32{0, 0}),
						toNM.TransformPoint([2]float32{0, -len})},
					[2][2]float32{
						toNM.TransformPoint([2]float32{2, 0}),
						toNM.TransformPoint([2]float32{2, -len})})

				// Arcs at each end; all of this is slightly simpler since
				// the width of the racetrack is 2, so the radius of the
				// arcs is 1...
				// previous top and bottom points
				prevt := toNM.TransformPoint([2]float32{0, 0})
				prevb := toNM.TransformPoint([2]float32{2, -len})
				for i := -90; i <= 90; i++ {
					v := [2]float32{math.Sin(math.Radians(float32(i))), math.Cos(math.Radians(float32(i)))}

					// top
					pt := math.Add2f([2]float32{1, 0}, v)
					pt = toNM.TransformPoint(pt)
					lines = append(lines, [2][2]float32{prevt, pt})
					prevt = pt

					// bottom
					pb := math.Sub2f([2]float32{1, -len}, v)
					pb = toNM.TransformPoint(pb)
					lines = append(lines, [2][2]float32{prevb, pb})
					prevb = pb
				}

				for _, l := range lines {
					l0, l1 := math.NM2LL(l[0], ctx.ControlClient.NmPerLongitude), math.NM2LL(l[1], ctx.ControlClient.NmPerLongitude)
					ld.AddLine(l0, l1)
				}

				drawArrow(toNM.TransformPoint([2]float32{0, -len / 2}), hdg)
				drawArrow(toNM.TransformPoint([2]float32{2, -len / 2}), hdg+math.Radians(180))
			}
		}

		drawName := wp.Fix[0] != '_'
		if _, err := math.ParseLatLong([]byte(wp.Fix)); err == nil {
			// Also don't draw names that are directly specified as latlongs.
			drawName = false
		}

		if _, ok := drawnWaypoints[wp.Fix]; ok {
			// And if we're given the same fix more than once (as may
			// happen with T-shaped RNAV arrivals for example), only draw
			// it once. We'll assume/hope that we're not seeing it with
			// different restrictions...
			continue
		}

		// Record that we have drawn this waypoint
		drawnWaypoints[wp.Fix] = nil

		// Draw a circle at the waypoint's location
		const pointRadius = 2.5
		const nSegments = 8
		pd.AddCircle(transforms.WindowFromLatLongP(wp.Location), pointRadius, nSegments)

		offset := calculateOffset(style.Font, func(j int) ([2]float32, bool) {
			idx := i + j
			if idx < 0 || idx >= len(waypoints) {
				return [2]float32{}, false
			}
			return math.LL2NM(waypoints[idx].Location, ctx.ControlClient.NmPerLongitude), true
		})

		// Draw the text for the waypoint, including fix name, any
		// properties, and altitude/speed restrictions.
		p := transforms.WindowFromLatLongP(wp.Location)
		p = math.Add2f(p, offset)
		if drawName {
			p = td.AddText(wp.Fix+"\n", p, style)
		}

		if wp.IAF || wp.IF || wp.FAF || wp.NoPT || wp.FlyOver {
			var s []string
			if wp.IAF {
				s = append(s, "IAF")
			}
			if wp.IF {
				s = append(s, "IF")
			}
			if wp.FAF {
				s = append(s, "FAF")
			}
			if wp.NoPT {
				s = append(s, "NoPT")
			}
			if wp.FlyOver {
				s = append(s, "FlyOver")
			}
			p = td.AddText(strings.Join(s, "/")+"\n", p, style)
		}

		if wp.Speed != 0 || wp.AltitudeRestriction != nil {
			p[1] -= 0.25 * float32(style.Font.Size) // extra space for lines above if needed

			if ar := wp.AltitudeRestriction; ar != nil {
				pt := p       // draw position for text
				var w float32 // max width of altitudes drawn
				if ar.Range[1] != 0 {
					// Upper altitude
					pp := td.AddText(av.FormatAltitude(ar.Range[1]), pt, style)
					w = pp[0] - pt[0]
					pt[1] -= float32(style.Font.Size)
				}
				if ar.Range[0] != 0 && ar.Range[0] != ar.Range[1] {
					// Lower altitude, if present and different than upper.
					pp := td.AddText(av.FormatAltitude(ar.Range[0]), pt, style)
					w = math.Max(w, pp[0]-pt[0])
					pt[1] -= float32(style.Font.Size)
				}

				// Now that we have w, we can draw lines the specify the
				// restrictions.
				if ar.Range[1] != 0 {
					// At or below (or at)
					ldr.AddLine([2]float32{p[0], p[1] + 2}, [2]float32{p[0] + w, p[1] + 2})
				}
				if ar.Range[0] != 0 {
					// At or above (or at)
					ldr.AddLine([2]float32{p[0], pt[1] - 2}, [2]float32{p[0] + w, pt[1] - 2})
				}

				// update text draw position so that speed restrictions are
				// drawn in a reasonable place; note that we maintain the
				// original p[1] regardless of how many lines were drawn
				// for altitude restrictions.
				p[0] += w + 4
			}

			if wp.Speed != 0 {
				p0 := p
				p1 := td.AddText(fmt.Sprintf("%dK", wp.Speed), p, style)
				p1[1] -= float32(style.Font.Size)

				// All speed restrictions are currently 'at'...
				ldr.AddLine([2]float32{p0[0], p0[1] + 2}, [2]float32{p1[0], p0[1] + 2})
				ldr.AddLine([2]float32{p0[0], p1[1] - 2}, [2]float32{p1[0], p1[1] - 2})
			}
		}
	}
}
