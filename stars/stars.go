// stars/stars.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"image"
	"os"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
	"github.com/mmp/vice/wx"
)

// IFR TRACON separation requirements
const LateralMinimum = 3
const VerticalMinimum = 1000

// STARS ∆ is character 0x80 in the font
const STARSTriangleCharacter = string(rune(0x80))

// Filled upward-pointing triangle
const STARSFilledUpTriangle = string(rune(0x1e))

const TabListEntries = 100
const TabListUnassignedIndex = -1

type Pane struct {
	TRACONPreferenceSets map[string]*PreferenceSet
	prefSet              *PreferenceSet

	// These are the current prefs from the prior representation; we read
	// them back in if they're there to use to bootstrap the new
	// representation.
	// TODO: remove this at some point in the future.
	OldPrefsCurrentPreferenceSet  *Preferences  `json:"CurrentPreferenceSet,omitempty"`
	OldPrefsSelectedPreferenceSet *int          `json:"SelectedPreferenceSet,omitempty"`
	OldPrefsPreferenceSets        []Preferences `json:"PreferenceSets,omitempty"`

	allVideoMaps []scope.Map
	dcbVideoMaps []*scope.Map

	weatherRadar scope.WeatherRadar

	targetGenLastCallsign av.ADSBCallsign

	visibleTracks []sim.Track

	mvaGrid *av.MVAGrid

	Monitor string
	Colors  MonitorColors

	// Which weather history snapshot to draw: this is always 0 unless the
	// 'display weather history' command was entered.
	wxHistoryDraw int
	// Time at which to step to the next history snapshot (5s intervals).
	wxNextHistoryStepTime sim.Time

	systemFontA, systemFontB               [6]*renderer.Font
	systemOutlineFontA, systemOutlineFontB [6]*renderer.Font
	dcbFontA, dcbFontB                     [3]*renderer.Font // 0, 1, 2 only

	// crossCursors are OS-managed "+" cursors, one per CharSize.Datablocks
	// value (clamped to [0,4]); built on demand by crossCursor and rebuilt
	// when fg/bg change.
	crossCursors     [5]platform.Cursor
	crossFg, crossBg renderer.RGB

	hideMouseCursor bool // set after auto-home and the cursor is repositioned

	fusedTrackVertices [][2]float32

	// Preferences that were active when we entered the PREF menu.
	RestorePreferences       *Preferences
	RestorePreferencesNumber *int

	// It seems like this should be based on ACID but then we also need
	// state for unassociated tracks, so... ?
	TrackState map[av.ADSBCallsign]*TrackState

	LockDisplay bool

	// DCBScaleToFit, when true, sizes DCB buttons to exactly fill the pane's
	// main axis instead of using the platform DPI. The button size is based
	// on the maximum slot count across menus so the bar's cross-axis
	// thickness doesn't change between menus, and scrolling is disabled.
	DCBScaleToFit bool

	// a/c callsign -> controllers
	PointOuts         map[sim.ACID]PointOutControllers
	RejectedPointOuts map[sim.ACID]any
	ForceQLACIDs      map[sim.ACID]any

	CoastSuspendIndex int // Next index to assign

	// Hold for release callsigns we have seen but not released. (We need
	// to track this since auto release only applies to new ones seen after
	// it is enabled.)
	ReleaseRequests map[av.ADSBCallsign]any

	// Periodically updated in processEvents
	DuplicateBeacons map[av.Squawk]any

	queryUnassociated *util.TransientMap[av.ADSBCallsign, any]

	RangeBearingLines []RangeBearingLine
	MinSepAircraft    [2]av.ADSBCallsign

	CAAircraft  []CAAircraft
	MCIAircraft []CAAircraft

	// For CRDA
	CRDAPairs []CRDAPair

	// Various UI state
	FlipNumericKeypad             bool
	DisplayOutsideAirspaceWarning bool
	TgtGenKey                     byte

	FontSelection int32

	DisplayBeaconCode        av.Squawk
	DisplayBeaconCodeEndTime sim.Time

	OverrideDisplayRequestedAltitude *bool

	// When VFR flight plans were first seen (used for sorting in VFR list)
	VFRFPFirstSeen map[sim.ACID]sim.Time

	transientCommandHandlers []userCommand // handlers for next keyboard Enter or scope click
	activeSpinner            dcbSpinner

	savedMousePosition [2]float32
	accumMouseDeltaY   float32

	dwellAircraft     av.ADSBCallsign
	drawRouteAircraft av.ADSBCallsign
	showListFrames    bool

	// For 4.9.27 list moving
	movingList       string
	movingListBounds math.Extent2D
	movingListOffset [2]float32 // offset from cursor to list position when move started

	drawRoutePoints []math.Point2LL

	commandMode       CommandMode
	multiFuncPrefix   string
	previewAreaOutput string
	previewAreaInput  string
	dcbShowAux        bool

	// dcbScroll is the horizontal/vertical scroll offset (in pane-local
	// pixels) applied to the DCB when the pane is too narrow to show every
	// button at the fixed button size. Clamped to [0, content - visible]
	// each frame; mouse wheel over the DCB bar adjusts it.
	dcbScroll float32

	// dcbContentSize is the main-axis extent (in pane-local pixels) that
	// the previous frame's DCB drawing actually used. Lets dcbMaxScroll
	// stop at the last real button so the user can't scroll into empty bar.
	dcbContentSize float32

	// dcbLastMenu is the DCB layout that was rendered last frame. drawDCB
	// resets dcbScroll when this changes so a scroll offset from a wider
	// menu doesn't carry into a narrower submenu.
	dcbLastMenu dcbMenuID

	lastTrackUpdate        sim.Time
	lastHistoryTrackUpdate sim.Time
	discardTracks          bool

	LastATIS   [10]string
	LastGIText [10]string
	FlashATIS  [10]bool
	// Suppresses flashing when this pane later receives the state update for
	// an ATIS/GIText change that originated locally.
	pendingATISGITextUpdate [10]struct {
		ExpectedATIS   string
		ExpectedGIText string
		Valid          bool
	}

	// The start of a RBL--one click received, waiting for the second.
	wipRBL *RangeBearingLine

	audioEffects     map[AudioType]int // to handle from Platform.AddPCM()
	testAudioEndTime time.Time

	highlightedLocation        math.Point2LL
	highlightedLocationEndTime sim.Time

	// Built-in screenshots / video captures
	capture struct {
		enabled          bool
		specifyingRegion bool
		haveRegion       bool
		region           [2][2]float32
		doStill          bool
		doVideo          bool
		video            struct {
			frameCh   chan *image.RGBA
			lastFrame time.Time
		}
	}

	// An in-progress restriction area.
	wipRestrictionArea           *av.RestrictionArea
	wipRestrictionAreaMousePos   [2]float32 // last click position while defining it
	wipRestrictionAreaMouseMoved bool       // has moved since last click

	// We won't waste the space to serialize these but reconstruct them on load.
	significantPoints map[string]sim.SignificantPoint
	// Store them redundantly in a slice so we can sort them and then
	// search in a consistent order (when we have to do an exhaustive
	// search).
	significantPointsSlice []sim.SignificantPoint

	showVFRAirports    bool
	showTRACONBoundary bool
	scopeDraw          struct {
		scope.RouteDrawer
		holds    map[string]av.Hold // fix name -> Hold
		allHolds bool
	}

	// Instrument Flight Procedure (SIDs, STARs, IAPs etc) Helpers
	IFPHelpers struct {
		ArrivalsColor    *[3]float32
		ApproachesColor  *[3]float32
		DeparturesColor  *[3]float32
		OverflightsColor *[3]float32
		AirspaceColor    *[3]float32
		HoldsColor       *[3]float32
	}

	atmosGrid             *wx.AtmosGrid
	windDrawAltitudeIndex int

	// We keep a pool of each type so that we don't need to allocate a new
	// object each time we generate a datablock.
	fdbArena util.ObjectArena[fullDatablock]
	pdbArena util.ObjectArena[partialDatablock]
	ldbArena util.ObjectArena[limitedDatablock]
	sdbArena util.ObjectArena[suspendedDatablock]

	// Reused across frames by getAllDatablocks; cleared at the start of each call.
	datablocks map[av.ADSBCallsign]datablock
}

func (sp *Pane) notePendingATISGITextUpdate(ctx *scope.Context, line int, atis, text *string) {
	update := &sp.pendingATISGITextUpdate[line]
	update.ExpectedATIS = ctx.Client.State.ATIS[line]
	update.ExpectedGIText = ctx.Client.State.GIText[line]
	// nil means "leave unchanged", so start from the current state and only
	// override the fields this command is modifying.
	if atis != nil {
		update.ExpectedATIS = *atis
	}
	if text != nil {
		update.ExpectedGIText = *text
	}
	update.Valid = true
}

func (sp *Pane) clearPendingATISGITextUpdate(line int) {
	sp.pendingATISGITextUpdate[line] = struct {
		ExpectedATIS   string
		ExpectedGIText string
		Valid          bool
	}{}
}

type PointOutControllers struct {
	From, To sim.TCP
}

const (
	fontDefault = iota
	fontLegacy
	fontARTS
)

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

// Used both for CAs and MCIs.
type CAAircraft struct {
	ADSBCallsigns [2]av.ADSBCallsign // sorted alphabetically
	Acknowledged  bool
	SoundEnd      sim.Time
	Start         sim.Time
}

type CRDAMode int

const (
	CRDAModeStagger = iota
	CRDAModeTie
)

// this is read-only, stored in STARSPane for convenience
type CRDAPair struct {
	av.CRDAPair
	Source  *av.CRDARegion
	Ghost   *av.CRDARegion
	Airport av.FAAAirportCode
	Index   int
}

type CRDARunwayState struct {
	Enabled                 bool
	Airport                 av.FAAAirportCode
	Region                  string
	LeaderLineDirection     *math.CardinalOrdinalDirection // nil -> unset
	DrawCourseLines         bool
	DrawQualificationRegion bool
}

// stores the per-preference set state for each STARSCRDAPair
type CRDARunwayPairState struct {
	Enabled     bool
	Mode        CRDAMode
	SourceState CRDARunwayState
	GhostState  CRDARunwayState
}

func (c *CRDAPair) getRegionsString() string {
	return c.SourceRegion + "/" + c.GhostRegion
}

// VideoMapsGroup is the category of video maps the MAPS list is showing;
// its values are the scope.VideoMap* category constants.
type VideoMapsGroup int

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

///////////////////////////////////////////////////////////////////////////
// Monitor colors

func NewPane() *Pane {
	InitCommands()
	return &Pane{}
}

func (sp *Pane) Activate(r renderer.Renderer, p platform.Platform, lg *log.Logger) {
	if sp.PointOuts == nil {
		sp.PointOuts = make(map[sim.ACID]PointOutControllers)
	}
	if sp.RejectedPointOuts == nil {
		sp.RejectedPointOuts = make(map[sim.ACID]any)
	}
	if sp.queryUnassociated == nil {
		sp.queryUnassociated = util.NewTransientMap[av.ADSBCallsign, any]()
	}
	if sp.TRACONPreferenceSets == nil {
		sp.TRACONPreferenceSets = make(map[string]*PreferenceSet)
	}

	sp.initializeFonts(r, p)
	sp.initializeAudio(p, lg)

	if sp.TrackState == nil {
		sp.TrackState = make(map[av.ADSBCallsign]*TrackState)
	}
	if sp.VFRFPFirstSeen == nil {
		sp.VFRFPFirstSeen = make(map[sim.ACID]sim.Time)
	}

	sp.lastTrackUpdate = sim.Time{} // force immediate update at start
	sp.lastHistoryTrackUpdate = sim.Time{}

	if sp.TgtGenKey == 0 {
		sp.TgtGenKey = ';'
	}

	sp.Colors = monitorColorSets["legacy"]
	sp.Monitor = "legacy"

	if sp.IFPHelpers.ApproachesColor == nil {
		sp.IFPHelpers.ApproachesColor = &[3]float32{.1, .9, .1}
	}

	if sp.IFPHelpers.ArrivalsColor == nil {
		sp.IFPHelpers.ArrivalsColor = &[3]float32{.1, .9, .1}
	}

	if sp.IFPHelpers.DeparturesColor == nil {
		sp.IFPHelpers.DeparturesColor = &[3]float32{.1, .9, .1}
	}

	if sp.IFPHelpers.OverflightsColor == nil {
		sp.IFPHelpers.OverflightsColor = &[3]float32{.1, .9, .1}
	}

	if sp.IFPHelpers.AirspaceColor == nil {
		sp.IFPHelpers.AirspaceColor = &[3]float32{.1, .9, .1}
	}

	if sp.IFPHelpers.HoldsColor == nil {
		sp.IFPHelpers.HoldsColor = &[3]float32{.9, .9, .1} // Yellow
	}

	sp.capture.enabled = os.Getenv("VICE_CAPTURE") != ""
}

// displayRequestedAltitude returns whether requested altitude should be
// displayed in full data blocks, honoring the controller's override of the
// adapted setting if they have made one.
func (sp *Pane) displayRequestedAltitude(ctx *scope.Context) bool {
	if sp.OverrideDisplayRequestedAltitude != nil {
		return *sp.OverrideDisplayRequestedAltitude
	}
	return ctx.FacilityAdaptation.Datablocks.FDB.DisplayRequestedAltitude
}

func (sp *Pane) LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	sp.OverrideDisplayRequestedAltitude = nil

	sp.initPrefsForLoadedSim(client.State, pl)

	sp.makeMaps(client, lg)
	sp.makeSignificantPoints(client.State)

	sp.mvaGrid = av.MakeMVAGrid(av.DB.MVAs[client.State.Facility])

	var ok bool
	if sp.Colors, ok = monitorColorSets[client.State.FacilityAdaptation.Monitor]; !ok {
		// This should have been caught during validation of the FacilityAdaptation...
		sp.Monitor = "legacy"
		sp.Colors = monitorColorSets[sp.Monitor]
	}
}

func (sp *Pane) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	sp.CRDAPairs = nil
	for name, ap := range util.SortedMap(client.State.Airports) {
		for idx, pair := range ap.CRDAPairs {
			sp.CRDAPairs = append(sp.CRDAPairs, CRDAPair{
				CRDAPair: pair,
				Source:   ap.CRDARegions[pair.SourceRegion],
				Ghost:    ap.CRDARegions[pair.GhostRegion],
				Airport:  av.FAAAirportCode(av.AirportDisplayId(name)),
				Index:    idx + 1, // 1-based
			})
		}
	}
	clear(sp.VFRFPFirstSeen)

	sp.resetInputState(pl)

	// Update maps before resetting the prefs since we may rewrite some map
	// ids and we want to use the right ones when we're enabling the
	// default maps.
	sp.makeMaps(client, lg)
	sp.makeSignificantPoints(client.State)

	sp.resetPrefsForNewSim(client.State, pl)

	var ok bool
	if sp.Colors, ok = monitorColorSets[client.State.FacilityAdaptation.Monitor]; !ok {
		// This should have been caught during validation of the FacilityAdaptation...
		sp.Monitor = "legacy"
		sp.Colors = monitorColorSets[sp.Monitor]
	}

	sp.showVFRAirports = false
	sp.showTRACONBoundary = false
	sp.drawRoutePoints = nil
	sp.showListFrames = false
	sp.activeSpinner = nil
	sp.OverrideDisplayRequestedAltitude = nil
	sp.CoastSuspendIndex = 0
	sp.DisplayBeaconCode = 0
	sp.DisplayBeaconCodeEndTime = sim.Time{}
	sp.wxHistoryDraw = 0
	sp.wxNextHistoryStepTime = sim.Time{}
	clear(sp.DuplicateBeacons)
	clear(sp.ReleaseRequests)
	clear(sp.PointOuts)
	clear(sp.RejectedPointOuts)
	clear(sp.ForceQLACIDs)
	sp.RangeBearingLines = nil
	sp.MinSepAircraft = [2]av.ADSBCallsign{}
	sp.LastATIS = client.State.ATIS
	sp.LastGIText = client.State.GIText
	sp.FlashATIS = [10]bool{}
	sp.highlightedLocationEndTime = sim.Time{}

	sp.lastTrackUpdate = sim.Time{} // force update
	sp.lastHistoryTrackUpdate = sim.Time{}

	sp.atmosGrid = nil
	sp.mvaGrid = av.MakeMVAGrid(av.DB.MVAs[client.State.Facility])

	sp.weatherRadar.Reset(lg)

	// nil these out rather than clearing them so that they are rebuilt
	// from scratch.
	sp.scopeDraw.Clear()
	sp.scopeDraw.holds = nil
}

func (sp *Pane) makeMaps(client *client.ControlClient, lg *log.Logger) {
	sp.allVideoMaps = nil
	usedIds := make(map[int]any)

	// addMap inserts a scope.Map into allVideoMaps, probing forward
	// through STARS Id space [1, 1000) for a free slot starting at the
	// map's Id. Maps with Id == 0 are appended without claiming
	// a slot (they have no DCB Id and are unreachable via the [NUM]
	// command; they still show up in the MAPS list if they carry a label).
	addMap := func(vm scope.Map) {
		if vm.Id == 0 {
			sp.allVideoMaps = append(sp.allVideoMaps, vm)
			return
		}
		for i := range 999 {
			// See if id is available
			id := (vm.Id + i) % 1000
			if id == 0 {
				continue // 0 isn't a valid DCB Id
			}
			if _, ok := usedIds[id]; !ok {
				vm.Id = id
				sp.allVideoMaps = append(sp.allVideoMaps, vm)
				usedIds[id] = nil
				return
			}
		}
		// Unable to find a free slot!
	}

	ss := client.State
	vmf, err := client.LoadVideoMapLibrary(ss.ControllerVideoMapFile)
	if err != nil {
		lg.Errorf("%v", err)
	}

	// First grab the video maps needed for the DCB
	var dcbMaps []videomaps.STARSMap
	addedNames := make(map[string]bool)
	for _, name := range client.State.ControllerVideoMaps {
		if m, ok := vmf.Maps[name]; ok && !addedNames[name] {
			dcbMaps = append(dcbMaps, m)
			addedNames[name] = true
		}
	}
	for _, vm := range scope.BuildMaps(dcbMaps) {
		addMap(vm)
	}

	// Then add every other library map. Maps colliding with a DCB-held
	// Id get probe-reassigned so they stay reachable via [NUM] and
	// visible in the MAPS list. Iterate by sorted name so the warning
	// (and the choice of which map keeps a collided Id) is deterministic
	// across runs.
	var additionalMaps []videomaps.STARSMap
	for name, vm := range util.SortedMap(vmf.Maps) {
		if !addedNames[name] {
			additionalMaps = append(additionalMaps, vm)
		}
	}
	for _, vm := range scope.BuildMaps(additionalMaps) {
		addMap(vm)
	}

	for _, vm := range scope.SystemMaps(scope.SystemMapSpec{
		Facility:          ss.Facility,
		Center:            ss.Center,
		NmPerLongitude:    ss.NmPerLongitude,
		MagneticVariation: ss.MagneticVariation,
		Adaptation:        &ss.FacilityAdaptation,
		Airports:          ss.Airports,
		ArrivalAirports:   ss.ArrivalAirports,
	}) {
		addMap(vm)
	}

	// Start with the video maps associated with the Sim.
	sp.dcbVideoMaps = nil
	for _, name := range client.State.ControllerVideoMaps {
		if idx := slices.IndexFunc(sp.allVideoMaps, func(v scope.Map) bool { return v.Name == name }); idx != -1 && name != "" {
			sp.dcbVideoMaps = append(sp.dcbVideoMaps, &sp.allVideoMaps[idx])
		} else {
			sp.dcbVideoMaps = append(sp.dcbVideoMaps, nil)
		}
	}
}

func (sp *Pane) CanTakeKeyboardFocus() bool { return true }

func (sp *Pane) Upgrade(from, to int) {
	for _, prefs := range sp.TRACONPreferenceSets {
		prefs.Upgrade(from, to)
	}
	if sp.OldPrefsCurrentPreferenceSet != nil {
		sp.OldPrefsCurrentPreferenceSet.Upgrade(from, to)
	}
	if sp.OldPrefsSelectedPreferenceSet != nil && (*sp.OldPrefsSelectedPreferenceSet < 0 || *sp.OldPrefsSelectedPreferenceSet >= numSavedPreferenceSets) {
		sp.OldPrefsSelectedPreferenceSet = nil
	}
	for i := range sp.OldPrefsPreferenceSets {
		sp.OldPrefsPreferenceSets[i].Upgrade(from, to)
	}
}

func (sp *Pane) Draw(ctx *scope.Context, cb *renderer.CommandBuffer) {
	sp.processEvents(ctx)
	sp.updateVisibleTracks(ctx)

	sp.updateRadarTracks(ctx)
	sp.autoReleaseDepartures(ctx)

	ps := sp.currentPrefs()

	// Clear to background color
	cb.ClearRGB(ps.Brightness.BackgroundContrast.ScaleRGB(sp.Colors.Background))

	sp.processKeyboardInput(ctx)

	ctr := util.Select(ps.UseUserCenter, ps.UserCenter, ps.DefaultCenter)
	transforms := scope.GetTransformations(ctx.PaneExtent, ctx.NmPerLongitude, ctr, float32(ps.Range),
		ctx.MagneticVariation)

	scopeExtent := ctx.PaneExtent
	if ps.DisplayDCB {
		scopeExtent = sp.drawDCB(ctx, transforms, cb)

		// Update scissor for what's left and to protect the DCB (even
		// though this is apparently unrealistic, at least as far as radar
		// tracks go...)
		cb.SetScissorBounds(scopeExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	}

	sp.drawWX(ctx, transforms, cb)

	sp.drawRangeRings(ctx, transforms, cb)

	sp.drawTRACONBoundary(ctx, transforms, cb)

	sp.drawVideoMaps(ctx, transforms, cb)

	sp.drawScenarioRoutes(ctx, transforms, sp.systemFont(ctx, ps.CharSize.Tools), cb)

	sp.drawCRDARegions(ctx, transforms, cb)
	sp.drawSelectedRoute(ctx, transforms, cb)
	sp.drawPlotPoints(ctx, transforms, cb)
	sp.drawWind(ctx, transforms, cb)

	sp.drawCompass(ctx, scopeExtent, transforms, cb)

	sp.drawRestrictionAreas(ctx, transforms, cb)

	sp.drawSystemLists(ctx, ctx.PaneExtent, transforms, cb)

	sp.drawHistoryTrails(ctx, transforms, cb)

	sp.drawPTLs(ctx, transforms, cb)
	sp.drawRingsAndCones(ctx, transforms, cb)
	sp.drawRBLs(ctx, transforms, cb)
	sp.drawMinSep(ctx, transforms, cb)

	sp.drawHighlighted(ctx, transforms, cb)
	sp.drawVFRAirports(ctx, transforms, cb)

	dbs := sp.getAllDatablocks(ctx)
	sp.drawLeaderLines(ctx, dbs, transforms, cb)
	sp.drawTracks(ctx, transforms, cb)
	sp.drawDatablocks(dbs, ctx, transforms, cb)

	ghosts := sp.getGhostTracks(ctx)
	sp.drawGhosts(ctx, ghosts, transforms, cb)

	if ctx.Mouse != nil {
		// Is the mouse over the DCB or over the regular STARS scope? Note that
		// we need to offset the mouse position to be w.r.t. window coordinates
		// to match scopeExtent.
		mouseOverDCB := !scopeExtent.Inside(math.Add2f(ctx.Mouse.Pos, ctx.PaneExtent.P0))
		if !mouseOverDCB {
			// DCB buttons handle their own click checks, etc.
			sp.consumeMouseEvents(ctx, ghosts, transforms, cb)
		}
		sp.drawMouseCursor(ctx, mouseOverDCB)
	}
	sp.handleCapture(ctx, transforms, cb)

	sp.updateAudio(ctx)

	// Do this at the end of drawing so that we hold on to the tracks we
	// have for rendering the current frame.
	if sp.discardTracks {
		for _, state := range sp.TrackState {
			state.historyTracksIndex = 0
		}
		sp.lastTrackUpdate = sim.Time{} // force update
		sp.lastHistoryTrackUpdate = sim.Time{}
		sp.discardTracks = false
	}

	sp.drawPauseOverlay(ctx, cb)
}

func (sp *Pane) drawPauseOverlay(ctx *scope.Context, cb *renderer.CommandBuffer) {
	if !ctx.Client.State.Paused {
		return
	}

	text := "SIMULATION PAUSED"
	font := sp.systemFontA[3] // Largest font

	// Get pane width
	width := ctx.PaneExtent.Width()
	height := ctx.PaneExtent.Height()

	// Fixed position from top
	topOffset := height - 140
	textY := topOffset + 30      // Text will be 30px below top (in middle of background quad)
	quadTop := topOffset + 45    // Background extends 15px above text
	quadBottom := topOffset + 15 // Background extends 15px below text

	// Draw background quad (fixed width of 360px centered horizontally)
	quad := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(quad)
	quad.AddQuad(
		[2]float32{width/2 - 180, quadTop},    // Left-top
		[2]float32{width/2 + 180, quadTop},    // Right-top
		[2]float32{width/2 + 180, quadBottom}, // Right-bottom
		[2]float32{width/2 - 180, quadBottom}, // Left-bottom
		sp.Colors.TextAlert)

	// Draw text
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	td.AddTextCentered(text, [2]float32{width / 2, textY}, renderer.TextStyle{
		Font:  font,
		Color: renderer.RGB{R: 1, G: 1, B: 1},
	})

	// Apply transformations and draw
	transforms := scope.GetTransformations(ctx.PaneExtent, 0, [2]float32{}, 0, 0)
	transforms.LoadWindowViewingMatrices(cb)
	quad.GenerateCommands(cb)
	td.GenerateCommands(cb)
}
