// eram/eram.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
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

	"github.com/AllenDang/cimgui-go/imgui"
)

// Color palette for ERAM rendering. Sub-structs group colors by where they
// are used: popup rows, menu chrome, floating views, scroll bars, the
// toolbar, badges, CRR, the point-out popup, and the pause-overlay. Same
// RGB values intentionally appear under several names when used in distinct
// places, so each call site can read with local context.
type (
	popupPalette struct {
		backgroundBlack, backgroundGrey, backgroundGreen, text renderer.RGB
	}
	menuPalette struct {
		titleBackground, titleText, titleLeftBorder, outerBorder renderer.RGB
		buttonOutline, buttonHoverOutline                        renderer.RGB
		rowDimOutline, rowHoverOutline                           renderer.RGB
		selectedItem                                             renderer.RGB
		currentValue                                             renderer.RGB
		scrollArrow, scrollDimArrow                              renderer.RGB
		tearoffOutline                                           renderer.RGB
	}
	viewPalette struct {
		text, clockText, opaqueBackground, border renderer.RGB
		buttonOutline, hoveredOutline             renderer.RGB
	}
	scrollPalette struct {
		background, border, arrow renderer.RGB
	}
	// toolbarPalette colors the toolbar buttons by their kind; see
	// toolbarButtonKind.
	toolbarPalette struct {
		background, submenuBackground renderer.RGB
		menuButton, menuOpen          renderer.RGB
		toggleButton, toggleOn        renderer.RGB
		incDecButton                  renderer.RGB
		commandButton, commandActive  renderer.RGB
		tearoffBar, tearoffBarTorn    renderer.RGB
		text, outline, hoveredOutline renderer.RGB
		deletionMark                  renderer.RGB
		prefSetCommand                renderer.RGB // SAVE and DELETE in the PREFSET menu
		dockGrid, dockTarget          renderer.RGB // a toolbar's slots while a button is dragged over it
		disabledText                  renderer.RGB
	}
	badgePalette struct {
		fill, torn, border renderer.RGB
	}
	crrPalette struct {
		panelButtonBackground, panelBorder, swatchHighlight renderer.RGB
		green, yellow, magenta, cyan, white, amber          renderer.RGB
	}
	pointOutPalette struct {
		cyan, white renderer.RGB
	}
	pauseOverlayPalette struct {
		background, text renderer.RGB
	}
	// saaPalette colors the special activity airspace by its status
	// (4.2.18.1).
	saaPalette struct {
		hot, cold renderer.RGB
	}
)

var colors = struct {
	// Main ERAM yellow used for datablocks, leader lines, J-rings, the
	// point-out indicator, and outline rectangles.
	yellow renderer.RGB

	// The non-ADS-B indicator's coral.
	coral renderer.RGB

	// Input-area status indicators.
	// TODO: Find the actual ERAM red and green values for these.
	errorRed, successGreen renderer.RGB

	// ERAM scope clear color.
	scopeBackground renderer.RGB

	// Default video map line color (used when a BCG slot has no group color).
	videoMapBase renderer.RGB

	// Data block VCI (Voice Channel Indicator) green.
	vciGreen renderer.RGB

	popup        popupPalette
	menu         menuPalette
	view         viewPalette
	scroll       scrollPalette
	toolbar      toolbarPalette
	badge        badgePalette
	crr          crrPalette
	pointOut     pointOutPalette
	pauseOverlay pauseOverlayPalette
	saa          saaPalette

	// The annotation colors, and the white of an annotation being created
	// or edited (3.1).
	annotation      [numAnnotationColors]renderer.RGB
	annotationFrame renderer.RGB
}{
	yellow:          renderer.RGB{R: .894, G: .894},
	coral:           renderer.RGBFromHex(0xFF99CC),
	errorRed:        renderer.RGB{R: 1, G: 0, B: 0},
	successGreen:    renderer.RGB{R: 0, G: 1, B: 0},
	scopeBackground: renderer.RGB{R: 0, G: 0, B: .506},
	videoMapBase:    renderer.RGB{R: .953, G: .953, B: .953},
	vciGreen:        renderer.RGB{R: 0.01, G: 1, B: 0.05},

	popup: popupPalette{
		backgroundBlack: renderer.RGB{R: 0, G: 0, B: 0},
		backgroundGrey:  renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0},
		backgroundGreen: renderer.RGB{R: 0, G: 157.0 / 255.0, B: 0},
		text:            renderer.RGB{R: .85, G: .85, B: .85},
	},
	menu: menuPalette{
		titleBackground:    renderer.RGB{R: .78, G: .78, B: .78},
		titleText:          renderer.RGB{R: 1, G: 1, B: 1},
		titleLeftBorder:    renderer.RGB{R: 0, G: 0, B: 0},
		outerBorder:        renderer.RGB{R: 213.0 / 255.0, G: 213.0 / 255.0, B: 213.0 / 255.0},
		buttonOutline:      renderer.RGB{R: .38, G: .38, B: .38},
		buttonHoverOutline: renderer.RGB{R: .953, G: .953, B: .953},
		rowDimOutline:      renderer.RGB{R: .195, G: .195, B: .195},
		rowHoverOutline:    renderer.RGB{R: .624, G: .624, B: .624},
		selectedItem:       renderer.RGB{R: .3, G: .3, B: .6},
		currentValue:       renderer.RGB{R: .78, G: .46, B: .42}, // coral shading for the current value in datablock menus
		scrollArrow:        renderer.RGB{R: .7, G: .7, B: .7},
		scrollDimArrow:     renderer.RGB{R: .3, G: .3, B: .3},
		tearoffOutline:     renderer.RGB{R: 1, G: .761, B: 0},
	},
	view: viewPalette{
		text:             renderer.RGB{R: .85, G: .85, B: .85},
		clockText:        renderer.RGB{R: 1, G: 1, B: 1},
		opaqueBackground: renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0},
		border:           renderer.RGB{R: .914, G: .914, B: .914},
		buttonOutline:    renderer.RGB{R: .38, G: .38, B: .38},
		hoveredOutline:   renderer.RGB{R: .953, G: .953, B: .953},
	},
	scroll: scrollPalette{
		background: renderer.RGB{R: 0, G: 0, B: 0},
		border:     renderer.RGB{R: 0.5, G: 0.5, B: 0.5},
		arrow:      renderer.RGB{R: 145.0 / 255.0, G: 145.0 / 255.0, B: 145.0 / 255.0},
	},
	toolbar: toolbarPalette{
		background:        renderer.RGB{R: .78, G: .78, B: .78},
		submenuBackground: renderer.RGB{R: .404, G: .404, B: .404},
		menuButton:        renderer.RGB{R: 0, G: 0, B: .867},
		menuOpen:          renderer.RGB{R: .556, G: .141, B: .141},
		toggleButton:      renderer.RGB{R: 0, G: 0, B: 0},
		toggleOn:          renderer.RGB{R: .78, G: .78, B: .78},
		incDecButton:      renderer.RGB{R: 0, G: .804, B: 0},
		commandButton:     renderer.RGB{R: 0, G: .804, B: .843},
		commandActive:     renderer.RGB{R: .867, G: .429, B: .267},
		tearoffBar:        renderer.RGB{R: 1, G: 1, B: .576},
		tearoffBarTorn:    renderer.RGB{R: .7, G: .7, B: .7},
		text:              renderer.RGB{R: .953, G: .953, B: .953},
		outline:           renderer.RGB{R: .38, G: .38, B: .38},
		hoveredOutline:    renderer.RGB{R: .953, G: .953, B: .953},
		deletionMark:      renderer.RGB{R: .953, G: .953, B: .953},
		prefSetCommand:    renderer.RGB{R: .173, G: .467, B: .475},
		dockGrid:          renderer.RGB{R: 0, G: .5, B: 0},
		dockTarget:        renderer.RGB{R: 0, G: 1, B: 0},
		disabledText:      renderer.RGB{R: .5, G: .5, B: .5},
	},
	badge: badgePalette{
		fill:   renderer.RGB{R: 159.0 / 255.0, G: 163.0 / 255.0, B: 9.0 / 255.0},
		torn:   renderer.RGB{R: 132.0 / 255.0, G: 132.0 / 255.0, B: 132.0 / 255.0},
		border: renderer.RGB{R: .5, G: .5, B: .5},
	},
	crr: crrPalette{
		panelButtonBackground: renderer.RGB{R: 0, G: 0, B: 0},
		panelBorder:           renderer.RGB{R: .6, G: .6, B: .6},
		swatchHighlight:       renderer.RGB{R: 1, G: 1, B: 1},
		green:                 renderer.RGB{R: .1, G: .9, B: .1},
		yellow:                renderer.RGBFromHex(0xB7B513),
		magenta:               renderer.RGBFromHex(0xB000B0),
		cyan:                  renderer.RGB{R: 0, G: .8, B: .8},
		white:                 renderer.RGB{R: .85, G: .85, B: .85},
		amber:                 renderer.RGB{R: .9, G: .7, B: .2},
	},
	pointOut: pointOutPalette{
		cyan:  renderer.RGB{R: 0, G: 1, B: 1},
		white: renderer.RGB{R: 1, G: 1, B: 1},
	},
	pauseOverlay: pauseOverlayPalette{
		background: renderer.RGB{R: 1, G: 0, B: 0},
		text:       renderer.RGB{R: 1, G: 1, B: 1},
	},
	saa: saaPalette{
		hot:  renderer.RGB{R: 1, G: .57, B: .3},
		cold: renderer.RGB{R: 1, G: 1, B: 1},
	},
	annotation: [numAnnotationColors]renderer.RGB{
		AnnotationGreen:  {R: .1, G: .9, B: .1},
		AnnotationCoral:  renderer.RGBFromHex(0xFF99CC),
		AnnotationWhite:  {R: .9, G: .9, B: .9},
		AnnotationYellow: {R: .894, G: .894},
	},
	annotationFrame: renderer.RGB{R: 1, G: 1, B: 1},
}

type Scope struct {
	ERAMPreferenceSets map[string]*PreferenceSet `json:"PreferenceSets,omitempty"`
	prefSet            *PreferenceSet
	TrackState         map[av.ADSBCallsign]*TrackState `json:"TrackState,omitempty"`

	DisableERAMtoRadio bool
	FlipNumericKeypad  bool

	systemFont [11]*renderer.Font

	allVideoMaps  []videomaps.ERAMMap
	bcgNames      []string // current group's bcgMenu; index-stable, may include empty slots
	videoMapLabel string
	// baseVideoMap is the current group's own always-displayed geometry.
	baseVideoMap videomaps.ERAMMap

	// initials are the operating initials of the controller signed in, and
	// position the position they are signed in to.
	initials string
	position string

	// eramCursors are made from cursorImages; both are keyed by base name
	// ("Eram1", "EramDeletion", ...). cursorBrightness is the BRIGHT menu's
	// CURSOR setting they were made at, so that they are made again when
	// it changes.
	eramCursors      map[string]platform.Cursor
	cursorImages     map[string]cursorImage
	cursorBrightness scope.Brightness

	cursorOverrideSelection string
	cursorOverrideUntil     time.Time
	cursorRollbackSelection string // Cursor to use after temporary cursor expires

	// AckedPointOuts holds the receivers who have acknowledged the user's
	// point outs; they are shown as a white "A" until the user dismisses them.
	AckedPointOuts map[sim.ACID][]sim.ControlPosition

	QuickLookSectors map[string]struct{}

	// via QB ...
	AddedBeaconCodes []av.Squawk

	// Output and input text for the command line interface.
	responseArea string
	feedbackArea feedbackMessage
	input        inputText

	// raScroll is how far the RA has been scrolled through raScrolledText,
	// the response it was scrolled in; a new response starts at the top.
	raScroll       ViewScrollState
	raScrolledText string

	toolbar     toolbarState
	holdRepeat  pressRepeat // paces held toolbar buttons and popup menu rows
	alarm       alarm
	altLimits   altitudeLimitsEntry
	rangeView   rangeViewState
	prefSetMenu prefSetMenuState
	// viewExtents collects the views drawn in the frame, for the toolbar
	// engine to yield to the next frame.
	viewExtents []math.Extent2D

	// Short-term conflict alert state; recomputed every caUpdateInterval.
	caPairs            []CAPair
	lastConflictUpdate time.Time
	lastRadarUpdate    sim.Time
	// SuppressionGroups are the conflict alert suppression groups, in ID
	// order.
	SuppressionGroups []SuppressionGroup `json:"SuppressionGroups,omitempty"`

	// Annotations are the graphic annotations drawn with the DRAW menu,
	// oldest first.
	Annotations []Annotation `json:"Annotations,omitempty"`
	drawMenu    drawMenuState

	fdbArena util.ObjectArena[fullDatablock]
	ldbArena util.ObjectArena[limitedDatablock]

	// dwellTrack is the track whose data block the cursor dwells on, if
	// any. dwellPressPos is where the cursor was when a trackball button
	// was last pressed, if dwellPressed: dwell emphasis returns once the
	// cursor moves from there (5.1.5.8).
	dwellTrack    av.ADSBCallsign
	dwellPressed  bool
	dwellPressPos [2]float32

	// Scope-wide drag-to-reposition state. Only one view can be repositioned
	// at a time; the active view is identified by its View.ID.
	viewRepo ViewRepoState

	VelocityTime int // 0, 1, 4, or 8 minutes

	dbLastAlternateTime time.Time // Alternates every 6 seconds
	dbAlternate         bool

	targetGenLastCallsign av.ADSBCallsign

	aircraftFixCoordinates map[sim.ACID]aircraftFixCoordinates

	// ScenarioRoutes draws the scenario's routes chosen in the info window.
	ScenarioRoutes scope.RouteDrawer

	// At most one floating pop-up menu is open at a time; nil = none open.
	popup popup

	// popupExtent is the on-screen rectangle of the active popup, refreshed
	// each frame by popup.draw. Used by DrawView to suppress view click
	// handling when the cursor is inside the popup, so the popup always wins
	// the click. Valid only while popup != nil.
	popupExtent math.Extent2D

	// CFR state (session): the flights whose readouts are in the view,
	// oldest first, whether SHOW FIELDS is on, and when each was added.
	CFREntries    []sim.ACID `json:"CFREntries,omitempty"`
	CFRShowFields bool       `json:"CFRShowFields,omitempty"`
	cfrAdded      map[sim.ACID]time.Time

	// SAA state: the special activity airspace of the sim's ARTCC, the
	// one whose label is being torn off, the torn-off labels' extents as
	// drawn, and the SAA FILTER view's page.
	saas          []saa
	saaTearingOff string
	saaTearOffs   map[string]math.Extent2D
	saaScroll     ViewScrollState

	// FLIGHT EVENT state: the entries the user deleted, with the ACIDs
	// they had, whether it had entries last frame, and the view's scroll
	// and selection.
	FELDeleted    map[av.ADSBCallsign]sim.ACID `json:"FELDeleted,omitempty"`
	felHadEntries bool
	felScroll     ViewScrollState
	felSelect     ViewSelectionState

	// DEPT LIST state: the flights the user deleted from the view and the
	// view's scroll.
	DeptListDeleted map[sim.ACID]struct{} `json:"DeptListDeleted,omitempty"`
	deptScroll      ViewScrollState

	// CRR state (session)
	CRRGroups        map[string]*CRRGroup `json:"CRRGroups,omitempty"`
	crrFixRects      map[string]math.Extent2D
	crrLabelRects    map[string]math.Extent2D
	crrAircraftRects map[string]map[av.ADSBCallsign]math.Extent2D
	crrScroll        ViewScrollState

	// ALTIM SET state (session)
	AltimSetAirports []av.ICAOAirportCode `json:"AltimSetAirports,omitempty"`
	altimSetScroll   ViewScrollState
	altimSetSelect   ViewSelectionState

	// WX window state (session)
	WXReportStations []av.ICAOAirportCode `json:"WXReportStations,omitempty"`
	wxScroll         ViewScrollState
	wxSelect         ViewSelectionState

	// The view entries torn off onto the scope, as drawn this frame, and
	// the one being torn off or moved.
	placedTearOffs []placedTearOff
	entryDrag      *entryDrag

	// Check list view toggle state (session): the check lists' items that
	// are highlighted ("checked off"), by their text.
	PosCheckToggled   map[string]bool `json:"PosCheckToggled,omitempty"`
	EmergCheckToggled map[string]bool `json:"EmergCheckToggled,omitempty"`

	weatherRadar scope.WeatherRadar
	nexrad       nexradCBs

	// drawingRoute is set while .DRAWROUTE takes clicks as the points of
	// drawRoutePoints.
	drawingRoute    bool
	drawRoutePoints []math.Point2LL

	// Per-frame scratch buffers, reused across Draw calls to avoid
	// allocations.
	visibleTracks           []sim.Track
	fdbIdx, ldbIdx, eldbIdx []int
}

func NewScope() *Scope {
	InitCommands()
	return &Scope{}
}

func (ep *Scope) Activate(r renderer.Renderer, pl platform.Platform, log *log.Logger) {
	// Activate maps
	if ep.AckedPointOuts == nil {
		ep.AckedPointOuts = make(map[sim.ACID][]sim.ControlPosition)
	}
	if ep.QuickLookSectors == nil {
		ep.QuickLookSectors = make(map[string]struct{})
	}

	if ep.TrackState == nil {
		ep.TrackState = make(map[av.ADSBCallsign]*TrackState)
	}

	if ep.aircraftFixCoordinates == nil {
		ep.aircraftFixCoordinates = make(map[sim.ACID]aircraftFixCoordinates)
	}
	if ep.CRRGroups == nil {
		ep.CRRGroups = make(map[string]*CRRGroup)
	}
	if ep.crrFixRects == nil {
		ep.crrFixRects = make(map[string]math.Extent2D)
	}
	if ep.crrLabelRects == nil {
		ep.crrLabelRects = make(map[string]math.Extent2D)
	}
	if ep.crrAircraftRects == nil {
		ep.crrAircraftRects = make(map[string]map[av.ADSBCallsign]math.Extent2D)
	}
	if ep.PosCheckToggled == nil {
		ep.PosCheckToggled = make(map[string]bool)
	}
	if ep.EmergCheckToggled == nil {
		ep.EmergCheckToggled = make(map[string]bool)
	}
	// ViewSelectionState's zero-value Selected is 0 (i.e. "row 0 selected").
	// Initialize to -1 so no row is selected at startup.
	ep.altimSetSelect.Selected = -1
	ep.wxSelect.Selected = -1
	ep.felSelect.Selected = -1
	ep.toolbar.control.id = toolbarControlID

	ep.initializeFonts(r, pl)
	ep.loadCursors(pl, log)
	ep.loadAlarm(pl, log)

	if ep.prefSet == nil {
		ep.prefSet = &PreferenceSet{}
	}
	if ep.ERAMPreferenceSets == nil {
		ep.ERAMPreferenceSets = make(map[string]*PreferenceSet)
	}
	ep.ScenarioRoutes.Activate()
}

func (ep *Scope) Draw(ctx *scope.Context, cb *renderer.CommandBuffer) {
	ep.processEvents(ctx)
	ep.updateCursorOverride(ctx)

	ps := ep.currentPrefs()

	ep.updateRadarTracks(ctx)
	ep.updateVisibleTracks(ctx)
	tracks := ep.visibleTracks
	ep.pruneSuppressionGroups(ctx)
	ep.updateConflictAlerts(ctx, tracks)

	cb.ClearRGB(ps.Brightness.Background.ScaleRGB(colors.scopeBackground))
	ep.processKeyboardInput(ctx)
	// ps.Range is the vertical extent of the scope in NM (matching the
	// real-ERAM RANGE label); GetTransformations wants the half-height.
	// ERAM scopes are always true north up, hence no rotation.
	transforms := scope.GetTransformations(ctx.DrawExtent, ctx.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range)/2, 0)

	ep.drawMaps(ctx, transforms, cb)
	// An entry being torn off takes the click that places it. The Circle
	// Location Text Box is over the toolbar, so it takes its clicks next;
	// the annotations are under it.
	ep.placeEntryDrag(ctx, transforms)
	ep.handleCircleLocationMouse(ctx)
	ep.handleToolbarInput(ctx)
	ep.handleAnnotationMouse(ctx, transforms)
	cb.SetScissorBounds(ctx.DrawExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.drawAnnotations(ctx, transforms, cb)
	ep.drawTracksAndDatablocks(ctx, tracks, transforms, cb)
	ep.drawCRRFixes(ctx, transforms, cb)
	ep.drawCRRDistances(ctx, tracks, transforms, cb)
	ep.drawJRings(ctx, tracks, transforms, cb)
	ep.drawQULines(ctx, transforms, cb)

	// Draw toolbar and menus on top of the scope
	cb.SetScissorBounds(ctx.DrawExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.drawToolbar(ctx, transforms, cb)
	ep.drawEntryTearOffs(ctx, transforms, cb)

	// Draw floating windows after toolbar so they render on top and appear in the same
	// frame the toolbar button is clicked (toolbar sets Visible=true before this runs).
	ep.drawViews(ctx, tracks, transforms, cb)

	ep.drawTearoffs(ctx, transforms, cb)
	ep.drawCircleLocationBox(ctx, transforms, cb)
	if ctx.Mouse != nil {
		ep.handleEntryTearOffMouse(ctx)
		ep.handleSAAMouse(ctx, transforms)
		ep.consumeMouseEvents(ctx, transforms)
		ep.drawPausedAircraftState(ctx, transforms, cb)
	}

	ep.drawBacklight(ctx, cb)
	scope.DrawPauseOverlay(ctx, cb, ep.systemFont[3], colors.pauseOverlay.background, colors.pauseOverlay.text)
}

func (ep *Scope) Upgrade(from, to int) {
	for _, ps := range ep.ERAMPreferenceSets {
		if ps != nil {
			ps.Upgrade(from, to)
		}
	}
}

func (ep *Scope) LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	ep.ensurePrefSetForSim(client.State)
	ep.makeMaps(client, lg)
	ep.makeSAAs(client.State)
	ep.initials = client.SessionStats.Initials
	ep.position = string(client.State.PrimaryPositionForTCW(client.State.UserTCW))
	ep.caPairs = nil
	ep.lastConflictUpdate = time.Time{}
	ep.lastRadarUpdate = sim.Time{}
}

func (ep *Scope) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	ep.resetPrefsForNewSim(client.State)
	ep.makeMaps(client, lg)
	ep.makeSAAs(client.State)
	ep.initials = client.SessionStats.Initials
	ep.position = string(client.State.PrimaryPositionForTCW(client.State.UserTCW))
	ep.caPairs = nil
	ep.lastConflictUpdate = time.Time{}
	ep.lastRadarUpdate = sim.Time{}
	ep.SuppressionGroups = nil
	ep.Annotations = nil
	ep.cancelAnnotationFunction()

	ep.ScenarioRoutes.Clear()

	ep.drawingRoute, ep.drawRoutePoints = false, nil

	ep.input.Clear()
	ep.responseArea = ""
	ep.feedbackArea.Clear()
	ep.popup = nil
	ep.rangeView = rangeViewState{}
	ep.prefSetMenu = prefSetMenuState{}

	// Everything about the previous sim's aircraft, sectors, and airports
	// goes with it. Track state in particular must not carry over: a
	// callsign that recurs in the new sim would otherwise inherit the old
	// aircraft's leader line, history trail, and datablock state.
	clear(ep.TrackState)
	clear(ep.AckedPointOuts)
	clear(ep.QuickLookSectors)
	ep.AddedBeaconCodes = nil
	clear(ep.aircraftFixCoordinates)
	ep.targetGenLastCallsign = ""
	clear(ep.PosCheckToggled)
	clear(ep.EmergCheckToggled)
	clear(ep.CRRGroups)
	ep.crrScroll = ViewScrollState{}
	ep.CFREntries = nil
	clear(ep.cfrAdded)
	ep.CFRShowFields = false
	ep.saaTearingOff = ""
	ep.saaScroll = ViewScrollState{}
	clear(ep.FELDeleted)
	ep.felHadEntries = false
	ep.felScroll = ViewScrollState{}
	ep.felSelect = ViewSelectionState{Selected: -1}
	clear(ep.DeptListDeleted)
	ep.deptScroll = ViewScrollState{}
	// The stations' tear-offs go with them.
	ps := ep.currentPrefs()
	ep.AltimSetAirports, ps.AltimSet.TearOffs = nil, nil
	ep.altimSetScroll = ViewScrollState{}
	ep.altimSetSelect = ViewSelectionState{Selected: -1}
	ep.WXReportStations, ps.WX.TearOffs = nil, nil
	ep.wxScroll = ViewScrollState{}
	ep.wxSelect = ViewSelectionState{Selected: -1}

	ep.weatherRadar.Reset(lg)
}

var _ scope.UIDrawer = (*Scope)(nil)

func (ep *Scope) DisplayName() string { return "ERAM" }

func (ep *Scope) DrawUI(p platform.Platform, config *platform.Config) {
	imgui.Checkbox("Disable ERAM to Radio Commands", &ep.DisableERAMtoRadio)
	imgui.Checkbox("Invert numeric keypad", &ep.FlipNumericKeypad)
	if ep.prefSet == nil {
		return
	}
	ps := ep.currentPrefs()
	imgui.Checkbox("Right button acts as left button, left as middle", &ps.UseRightClick)
}

func (ep *Scope) DrawInfo(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	ep.ScenarioRoutes.DrawUI(c, lg)
	scope.DrawTowerListsUI(c)
	scope.DrawAirspaceAwarenessUI(c)
	ps := ep.currentPrefs()
	ep.ScenarioRoutes.DrawHoldsUI(ps.CurrentCenter, ps.Range)
}

// drawPausedAircraftState shows the sim's state for the aircraft under the
// mouse while the sim is paused.
func (ep *Scope) drawPausedAircraftState(ctx *scope.Context, transforms scope.Transformations,
	cb *renderer.CommandBuffer) {
	if !ctx.Client.State.Paused {
		return
	}
	trk, _ := ep.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms)
	if trk == nil {
		return
	}

	ps := ep.currentPrefs()
	style := renderer.TextStyle{
		Font:  ep.ERAMFont(ps.FDBSize),
		Color: ps.Brightness.FDB.ScaleRGB(colors.yellow),
	}
	pw := transforms.WindowFromLatLongP(ep.TrackState[trk.ADSBCallsign].Track.Location)
	scope.DrawPausedAircraftState(ctx, trk, pw, style, transforms, cb)
}

// drawBacklight dims everything drawn according to BRIGHT's BCKLGHT, the
// display's master brightness (table 3-3).
func (ep *Scope) drawBacklight(ctx *scope.Context, cb *renderer.CommandBuffer) {
	b := ep.currentPrefs().Brightness.Backlight
	if b >= 100 {
		return
	}
	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)
	w, h := ctx.DrawExtent.Width(), ctx.DrawExtent.Height()
	tb.AddQuad([2]float32{0, 0}, [2]float32{w, 0}, [2]float32{w, h}, [2]float32{0, h})

	transforms := scope.GetTransformations(ctx.DrawExtent, 0, [2]float32{}, 0, 0)
	transforms.LoadWindowViewingMatrices(cb)
	cb.Blend()
	cb.SetRGBA(renderer.RGBA{A: 1 - float32(b)/100})
	tb.GenerateCommands(cb)
	cb.DisableBlend()
}
