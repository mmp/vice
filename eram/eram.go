package eram

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

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
	toolbarPalette struct {
		background, submenuBackground                        renderer.RGB
		button, tearoffButton, tearoffDisabled, activeButton renderer.RGB
		text                                                 renderer.RGB
		unsupportedButton, disabledButton                    renderer.RGB
		outline, hoveredOutline                              renderer.RGB
		greenButton, grayButton, blackButton                 renderer.RGB
		vectorGreen, deleteTearoff                           renderer.RGB
	}
	badgePalette struct {
		fill, border renderer.RGB
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
)

var colors = struct {
	// Main ERAM yellow used for datablocks, leader lines, J-rings, the
	// point-out indicator, and outline rectangles.
	yellow renderer.RGB

	// Input-area status indicators.
	// TODO: Find the actual ERAM red and green values for these.
	errorRed, successGreen renderer.RGB

	// ERAM scope clear color.
	scopeBackground renderer.RGB

	// Default video map line color (used when a BCG slot has no group color).
	videoMapBase renderer.RGB

	// Data block VCI (Voice Channel Indicator) green.
	vciGreen renderer.RGB

	// Debug overlay for the draw-route command.
	drawRoute renderer.RGB

	popup        popupPalette
	menu         menuPalette
	view         viewPalette
	scroll       scrollPalette
	toolbar      toolbarPalette
	badge        badgePalette
	crr          crrPalette
	pointOut     pointOutPalette
	pauseOverlay pauseOverlayPalette
}{
	yellow:          renderer.RGB{R: .894, G: .894},
	errorRed:        renderer.RGB{R: 1, G: 0, B: 0},
	successGreen:    renderer.RGB{R: 0, G: 1, B: 0},
	scopeBackground: renderer.RGB{R: 0, G: 0, B: .506},
	videoMapBase:    renderer.RGB{R: .953, G: .953, B: .953},
	vciGreen:        renderer.RGB{R: 0.01, G: 1, B: 0.05},
	drawRoute:       renderer.RGB{R: 1, G: .3, B: .3},

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
		button:            renderer.RGB{R: 0, G: 0, B: .867},
		tearoffButton:     renderer.RGB{R: 1, G: 1, B: .576},
		tearoffDisabled:   renderer.RGB{R: .7, G: .7, B: .7},
		activeButton:      renderer.RGB{R: .906, G: .616, B: .6},
		text:              renderer.RGB{R: .953, G: .953, B: .953},
		unsupportedButton: renderer.RGB{R: .4, G: .4, B: .4},
		disabledButton:    renderer.RGB{R: 0, G: .173 / 2, B: 0},
		outline:           renderer.RGB{R: .38, G: .38, B: .38},
		hoveredOutline:    renderer.RGB{R: .953, G: .953, B: .953},
		greenButton:       renderer.RGB{R: 0, G: .804, B: 0},
		grayButton:        renderer.RGB{R: .78, G: .78, B: .78},
		blackButton:       renderer.RGB{R: 0, G: 0, B: 0},
		vectorGreen:       renderer.RGB{R: 0, G: .82, B: 0},
		deleteTearoff:     renderer.RGB{R: 0, G: .804, B: .843},
	},
	badge: badgePalette{
		fill:   renderer.RGB{R: 159.0 / 255.0, G: 163.0 / 255.0, B: 9.0 / 255.0},
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
}

const numMapColors = 8

var mapColors [2][numMapColors]renderer.RGB = [2][numMapColors]renderer.RGB{
	{ // Group A
		renderer.RGBFromUInt8(140, 140, 140),
		renderer.RGBFromUInt8(0, 255, 255),
		renderer.RGBFromUInt8(255, 0, 255),
		renderer.RGBFromUInt8(238, 201, 0),
		renderer.RGBFromUInt8(238, 106, 80),
		renderer.RGBFromUInt8(162, 205, 90),
		renderer.RGBFromUInt8(218, 165, 32),
		renderer.RGBFromUInt8(72, 118, 255),
	},
	{ // Group B
		renderer.RGBFromUInt8(140, 140, 140),
		renderer.RGBFromUInt8(132, 112, 255),
		renderer.RGBFromUInt8(118, 238, 198),
		renderer.RGBFromUInt8(237, 145, 33),
		renderer.RGBFromUInt8(218, 112, 214),
		renderer.RGBFromUInt8(238, 180, 180),
		renderer.RGBFromUInt8(50, 205, 50),
		renderer.RGBFromUInt8(255, 106, 106),
	},
}

type ERAMPane struct {
	ERAMPreferenceSets map[string]*PrefrenceSet        `json:"PreferenceSets,omitempty"`
	prefSet            *PrefrenceSet                   `json:"-"`
	tempSavedNames     [numSavedPreferenceSets]string  `json:"-"`
	TrackState         map[av.ADSBCallsign]*TrackState `json:"TrackState,omitempty"`

	DisableERAMtoRadio bool `json:"-"`
	FlipNumericKeypad  bool

	events *sim.EventsSubscription `json:"-"`

	systemFont [11]*renderer.Font `json:"-"`

	allVideoMaps    []av.ERAMMap `json:"-"`
	bcgNames        []string     `json:"-"` // current group's bcgMenu; index-stable, may include empty slots
	videoMapLabel   string       `json:"-"`
	currentFacility string       `json:"-"`

	eramCursorSelection string           `json:"-"`
	eramCursorPath      string           `json:"-"`
	eramCursor          *platform.Cursor `json:"-"`
	eramCursorLoadErr   string           `json:"-"`

	cursorOverrideSelection string    `json:"-"`
	cursorOverrideUntil     time.Time `json:"-"`
	cursorRollbackSelection string    `json:"-"` // Cursor to use after temporary cursor expires

	InboundPointOuts  map[sim.ACID][]sim.ControlPosition
	OutboundPointOuts map[sim.ACID][]outboundPointOut

	QuickLookSectors map[string]struct{}

	// via QB ...
	AddedBeaconCodes []av.Squawk

	// Output and input text for the command line interface.
	responseArea string          `json:"-"`
	feedbackArea feedbackMessage `json:"-"`
	Input        inputText       `json:"-"`

	activeToolbarMenu int  `json:"-"`
	toolbarVisible    bool `json:"-"`

	lastTrackUpdate time.Time `json:"-"`

	fdbArena util.ObjectArena[fullDatablock]    `json:"-"`
	ldbArena util.ObjectArena[limitedDatablock] `json:"-"`

	// Pane-wide drag-to-reposition state. Only one view can be repositioned
	// at a time; the active view is identified by its View.ID.
	viewRepo ViewRepoState `json:"-"`

	tearoffInProgress        string                   `json:"-"` // Button name being torn off
	tearoffIsReposition      bool                     `json:"-"` // Repositioning existing vs new tearoff
	tearoffStart             time.Time                `json:"-"` // Debounce timer
	tearoffDragOffset        [2]float32               `json:"-"` // Mouse offset from button corner
	deleteTearoffMode        bool                     `json:"-"` // Delete mode active
	tearoffMenus             map[string]int           `json:"-"` // torn-off menu button name -> menu state
	tearoffMenuOpened        map[string]time.Time     `json:"-"` // debounce open clicks per menu
	tearoffMenuLightToolbar  map[string][4][2]float32 `json:"-"` // cached menu backgrounds for tearoffs
	tearoffMenuLightToolbar2 map[string][4][2]float32 `json:"-"` // cached secondary backgrounds (MAP BRIGHT)
	tearoffMenuOrder         []string                 `json:"-"` // draw/input order for tearoff menus (oldest -> newest)

	VelocityTime int // 0, 1, 4, or 8 minutes

	dbLastAlternateTime time.Time `json:"-"` // Alternates every 6 seconds
	dbAlternate         bool      `json:"-"`

	targetGenLastCallsign av.ADSBCallsign `json:"-"`

	aircraftFixCoordinates map[string]aircraftFixCoordinates `json:"-"`

	prefrencesVisible bool `json:"-"`

	scopeDraw struct {
		arrivals    map[string]map[int]bool                 // group->index
		approaches  map[string]map[string]bool              // airport->approach
		departures  map[string]map[string]map[string]bool   // airport->runway->exit
		overflights map[string]map[int]bool                 // group->index
		airspace    map[sim.ControlPosition]map[string]bool // ctrl -> volume name
	}

	IFPHelpers struct {
		ArrivalsColor    *[3]float32
		ApproachesColor  *[3]float32
		DeparturesColor  *[3]float32
		OverflightsColor *[3]float32
		AirspaceColor    *[3]float32
	}

	// At most one floating pop-up menu is open at a time; nil = none open.
	popup popup `json:"-"`

	// popupExtent is the on-screen rectangle of the active popup, refreshed
	// each frame by popup.draw. Used by DrawView to suppress view click
	// handling when the cursor is inside the popup, so the popup always wins
	// the click. Valid only while popup != nil.
	popupExtent math.Extent2D `json:"-"`

	// CRR state (session)
	CRRGroups        map[string]*CRRGroup                         `json:"CRRGroups,omitempty"`
	crrFixRects      map[string]math.Extent2D                     `json:"-"`
	crrLabelRects    map[string]math.Extent2D                     `json:"-"`
	crrAircraftRects map[string]map[av.ADSBCallsign]math.Extent2D `json:"-"`

	// ALTIM SET state (session)
	AltimSetAirports []string           `json:"AltimSetAirports,omitempty"`
	altimSetScroll   ViewScrollState    `json:"-"`
	altimSetSelect   ViewSelectionState `json:"-"`

	// WX window state (session)
	WXReportStations []string           `json:"WXReportStations,omitempty"`
	wxScroll         ViewScrollState    `json:"-"`
	wxSelect         ViewSelectionState `json:"-"`

	// Check list view toggle state (session). Each slice is parallel to
	// checkListItems[…], with true = row is highlighted ("checked off").
	posCheckToggled   []bool `json:"-"`
	emergCheckToggled []bool `json:"-"`

	commandMode       CommandMode     `json:"-"`
	drawRouteAircraft av.ADSBCallsign `json:"-"`
	drawRoutePoints   []math.Point2LL `json:"-"`

	// Per-frame scratch buffers, reused across Draw calls to avoid
	// allocations.
	visibleTracks           []sim.Track `json:"-"`
	fdbIdx, ldbIdx, eldbIdx []int       `json:"-"`
}

func NewERAMPane() *ERAMPane {
	InitCommands()
	return &ERAMPane{}
}

func (ep *ERAMPane) Activate(r renderer.Renderer, pl platform.Platform, es *sim.EventStream, log *log.Logger) {
	// Activate maps
	if ep.InboundPointOuts == nil {
		ep.InboundPointOuts = make(map[sim.ACID][]sim.ControlPosition)
	}
	if ep.OutboundPointOuts == nil {
		ep.OutboundPointOuts = make(map[sim.ACID][]outboundPointOut)
	}
	if ep.QuickLookSectors == nil {
		ep.QuickLookSectors = make(map[string]struct{})
	}

	if ep.TrackState == nil {
		ep.TrackState = make(map[av.ADSBCallsign]*TrackState)
	}

	if ep.aircraftFixCoordinates == nil {
		ep.aircraftFixCoordinates = make(map[string]aircraftFixCoordinates)
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
	if ep.posCheckToggled == nil {
		ep.posCheckToggled = make([]bool, len(checkListItems[checkListPos]))
	}
	if ep.emergCheckToggled == nil {
		ep.emergCheckToggled = make([]bool, len(checkListItems[checkListEmerg]))
	}
	// ViewSelectionState's zero-value Selected is 0 (i.e. "row 0 selected").
	// Initialize to -1 so no row is selected at startup.
	ep.altimSetSelect.Selected = -1
	ep.wxSelect.Selected = -1

	ep.events = es.Subscribe()

	// TODO: initialize fonts and audio
	ep.initializeFonts(r, pl)

	// Activate weather radar, events
	if ep.prefSet == nil {
		ep.prefSet = &PrefrenceSet{}
	}
	if ep.ERAMPreferenceSets == nil {
		ep.ERAMPreferenceSets = make(map[string]*PrefrenceSet)
	}
	if ep.IFPHelpers.ApproachesColor == nil {
		ep.IFPHelpers.ApproachesColor = &[3]float32{.1, .9, .1}
	}

	if ep.IFPHelpers.ArrivalsColor == nil {
		ep.IFPHelpers.ArrivalsColor = &[3]float32{.1, .9, .1}
	}

	if ep.IFPHelpers.DeparturesColor == nil {
		ep.IFPHelpers.DeparturesColor = &[3]float32{.1, .9, .1}
	}

	if ep.IFPHelpers.OverflightsColor == nil {
		ep.IFPHelpers.OverflightsColor = &[3]float32{.1, .9, .1}
	}

	if ep.IFPHelpers.AirspaceColor == nil {
		ep.IFPHelpers.AirspaceColor = &[3]float32{.1, .9, .1}
	}
}

func (ep *ERAMPane) CanTakeKeyboardFocus() bool { return true }

func (ep *ERAMPane) updateCursorOverride(ctx *panes.Context) {
	if ep.prefSet == nil {
		return
	}

	desiredCursor := ""
	if ep.cursorOverrideSelection != "" {
		if !ep.cursorOverrideUntil.IsZero() && time.Now().After(ep.cursorOverrideUntil) {
			// Temporary cursor is over. Check the rollback cursor (if it exists).
			if ep.cursorRollbackSelection != "" { // If there is a rollback selected
				ep.cursorOverrideSelection = ep.cursorRollbackSelection
				ep.cursorOverrideUntil = time.Time{} // Keep indefinitely until changed
				ep.cursorRollbackSelection = ""      // Clear rollback after using it
				desiredCursor = ep.cursorOverrideSelection
			} else { // No rollback cursor. Default to the cursor selected in the CURSOR menu.
				ep.cursorOverrideSelection = ""
				ep.cursorOverrideUntil = time.Time{}
			}
		} else {
			desiredCursor = ep.cursorOverrideSelection
		}
	}

	if desiredCursor == "" {
		cursorSize := ep.currentPrefs().CursorSize
		if cursorSize > 0 {
			desiredCursor = fmt.Sprintf("Eram%d", cursorSize)
		} else {
			ep.eramCursorSelection = ""
			ep.eramCursorPath = ""
			ep.eramCursor = nil
			ep.eramCursorLoadErr = ""
			return
		}
	}

	if desiredCursor != ep.eramCursorSelection {
		ep.eramCursorSelection = desiredCursor
		ep.eramCursorPath = ""
		ep.eramCursor = nil
		ep.eramCursorLoadErr = ""
		resolvedPath, err := ep.resolveCursorPath(desiredCursor)
		if err != nil {
			ep.eramCursorLoadErr = err.Error()
			if ctx.Lg != nil {
				ctx.Lg.Warnf("ERAM cursor %q: %v", desiredCursor, err)
			}
			return
		}
		cursor, err := ctx.Platform.LoadCursorFromFile(resolvedPath)
		if err != nil {
			ep.eramCursorLoadErr = err.Error()
			if ctx.Lg != nil {
				ctx.Lg.Warnf("ERAM cursor %q: %v", resolvedPath, err)
			}
			return
		}
		ep.eramCursorPath = resolvedPath
		ep.eramCursor = cursor
	}

	if ctx.Mouse != nil && ep.eramCursor != nil {
		ctx.Platform.SetCursorOverride(ep.eramCursor)
	}
}

func (ep *ERAMPane) Draw(ctx *panes.Context, cb *renderer.CommandBuffer) {
	ep.processEvents(ctx)
	ep.updateCursorOverride(ctx)

	ps := ep.currentPrefs()

	ep.updateVisibleTracks(ctx)
	tracks := ep.visibleTracks
	ep.updateRadarTracks(ctx, tracks)

	// draw the ERAMPane
	cb.ClearRGB(ps.Brightness.Background.ScaleRGB(colors.scopeBackground)) // Scale this eventually
	ep.processKeyboardInput(ctx)
	// ctr := UserCenter
	// ps.Range is the vertical extent of the scope in NM (matching the
	// real-ERAM RANGE label); GetScopeTransformations wants the half-width.
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, ctx.MagneticVariation, ctx.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range)/2, 0)

	// Following are the draw functions. They are listed in the best of my ability

	// Draw weather
	ep.drawVideoMaps(ctx, transforms, cb)
	ep.drawScenarioRoutes(ctx, transforms, renderer.GetDefaultFont(), cb)
	ep.drawPlotPoints(ctx, transforms, cb)
	// Handle button tearoff placement BEFORE drawing toolbar (so placement click isn't consumed)
	ep.handleTearoffPlacement(ctx)
	ep.handleTornOffButtonsInput(ctx)
	scopeExtent := ctx.PaneExtent
	if ps.DisplayToolbar {
		scale := ep.toolbarButtonScale(ctx)
		sz := buttonSize(buttonFull, scale)
		scopeExtent.P1[1] -= sz[1]
	}
	cb.SetScissorBounds(scopeExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.drawHistoryTracks(ctx, tracks, transforms, cb)
	dbs := ep.getAllDatablocks(ctx, tracks)
	ep.drawLeaderLines(ctx, tracks, dbs, transforms, cb)
	ep.drawPTLs(ctx, tracks, transforms, cb)
	ep.drawTargets(ctx, tracks, transforms, cb)
	ep.drawTracks(ctx, tracks, transforms, cb)
	ep.drawDatablocks(tracks, dbs, ctx, transforms, cb)
	ep.datablockInteractions(ctx, tracks, transforms, cb)
	ep.drawCRRFixes(ctx, transforms, cb)
	ep.drawCRRDistances(ctx, tracks, transforms, cb)
	ep.drawJRings(ctx, tracks, transforms, cb)
	ep.drawQULines(ctx, transforms, cb)

	// Draw toolbar and menus on top of the scope
	cb.SetScissorBounds(ctx.PaneExtent, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.drawtoolbar(ctx, transforms, cb)

	// Draw floating windows after toolbar so they render on top and appear in the same
	// frame the toolbar button is clicked (toolbar sets Visible=true before this runs).
	ep.startDrawCommandInput(ctx, transforms, cb)
	ep.drawResponseArea(ctx, transforms, cb)
	ep.drawMessageCompositionArea(ctx, transforms, cb)
	ep.drawAltimSetView(ctx, transforms, cb)
	ep.drawWXView(ctx, transforms, cb)
	ep.drawBeaconCodeView(ctx, transforms, cb)
	ep.drawTimeView(ctx, transforms, cb)
	ep.drawCheckListView(ctx, transforms, cb)
	ep.drawCRRView(ctx, tracks, transforms, cb)
	// Draw the active floating pop-up (if any) on top of its host view.
	if ep.popup != nil {
		ep.popup.draw(ep, ctx, transforms, cb)
	}

	// Tear-offs
	ep.drawTornOffButtons(ctx, transforms, cb)
	ep.drawTearoffMenus(ctx, transforms, cb)
	ep.drawTearoffPreview(ctx, transforms, cb)

	// The TOOLBAR tearoff is different from the toolbar (DCB). It overlaps the toolbar and tracks and everything else I've tried.
	ep.drawMasterMenu(ctx, cb)
	if ctx.Mouse != nil {
		mouseOverToolbar := !scopeExtent.Inside(math.Add2f(ctx.Mouse.Pos, ctx.PaneExtent.P0))
		if !mouseOverToolbar || !ep.toolbarVisible {
			ep.consumeMouseEvents(ctx, transforms)
		}
	}

	// handleCapture
	// updateAudio
	ep.drawPauseOverlay(ctx, cb)
}

func (ep *ERAMPane) Upgrade(from, to int) {
	for _, ps := range ep.ERAMPreferenceSets {
		if ps != nil {
			ps.Upgrade(from, to)
		}
	}
}

func (ep *ERAMPane) LoadedSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	ep.ensurePrefSetForSim(client.State)
	ep.makeMaps(client, lg)
	ep.lastTrackUpdate = time.Time{}
}

func (ep *ERAMPane) ResetSim(client *client.ControlClient, pl platform.Platform, lg *log.Logger) {
	ep.ensurePrefSetForSim(client.State)
	ep.makeMaps(client, lg)
	ep.lastTrackUpdate = time.Time{}

	ep.scopeDraw.arrivals = nil
	ep.scopeDraw.approaches = nil
	ep.scopeDraw.departures = nil
	ep.scopeDraw.overflights = nil
	ep.scopeDraw.airspace = nil

	ep.commandMode = CommandModeNone
	ep.drawRoutePoints = nil
	ep.drawRouteAircraft = ""
}

// ensurePrefSetForSim initializes the ERAM preference set if needed and
// resets transient fields for a newly-loaded or reset Sim. Called from
// both LoadedSim and ResetSim so that preferences are ready before use.
func (ep *ERAMPane) ensurePrefSetForSim(ss client.SimState) {
	// Ensure map of saved preference sets exists
	if ep.ERAMPreferenceSets == nil {
		ep.ERAMPreferenceSets = make(map[string]*PrefrenceSet)
	}

	key := ss.Facility
	ep.currentFacility = key

	// Retrieve or create the preference set for this facility
	if ps, ok := ep.ERAMPreferenceSets[key]; ok && ps != nil {
		ep.prefSet = ps
	} else {
		ep.prefSet = &PrefrenceSet{Current: *ep.initPrefsForLoadedSim(ss)}
		ep.ERAMPreferenceSets[key] = ep.prefSet
	}

	// Ensure map fields exist
	if ep.prefSet.Current.VideoMapVisible == nil {
		ep.prefSet.Current.VideoMapVisible = make(map[string]interface{})
	}
	if ep.prefSet.Current.VideoMapBrightness == nil {
		ep.prefSet.Current.VideoMapBrightness = make(map[string]radar.Brightness)
	}

	// Update sim-dependent fields if they aren't set
	if ep.prefSet.Current.CurrentCenter.IsZero() {
		ep.prefSet.Current.Center = ss.GetInitialCenter()
		ep.prefSet.Current.CurrentCenter = ep.prefSet.Current.Center
	}
	if ep.prefSet.Current.VideoMapGroup == "" {
		ep.prefSet.Current.VideoMapGroup = ss.ScenarioDefaultVideoGroup
	}
	if ep.prefSet.Current.ARTCC == "" {
		ep.prefSet.Current.ARTCC = ss.Facility
	}
	if ep.prefSet.Current.Range == 0 {
		if r := ss.GetInitialRange(); r != 0 {
			ep.prefSet.Current.Range = r
		} else {
			ep.prefSet.Current.Range = makeDefaultPreferences().Range
		}
	}

	def := makeDefaultPreferences()
	if ep.prefSet.Current.MCA.Position == ([2]float32{}) {
		ep.prefSet.Current.MCA.Position = def.MCA.Position
	}
	if ep.prefSet.Current.RA.Position == ([2]float32{}) {
		ep.prefSet.Current.RA.Position = def.RA.Position
	}

	if ep.prefSet.Current.CursorSize == 0 {
		ep.prefSet.Current.CursorSize = def.CursorSize
	}

	// Fill in CRR defaults if this preference set was created before CRR existed
	if ep.prefSet.Current.CRR.ColorBright == nil {
		ep.prefSet.Current.CRR.ColorBright = def.CRR.ColorBright
		ep.prefSet.Current.CRR.Font = def.CRR.Font
		ep.prefSet.Current.CRR.Lines = def.CRR.Lines
		ep.prefSet.Current.CRR.Bright = def.CRR.Bright
		ep.prefSet.Current.CRR.Position = def.CRR.Position
		ep.prefSet.Current.CRR.Visible = def.CRR.Visible
	}

	// Fill in ALTIM SET defaults if this preference set was created before ALTIM SET existed
	needsAltimSetDefaults := ep.prefSet.Current.AltimSet.Position == ([2]float32{})
	if needsAltimSetDefaults {
		ep.prefSet.Current.AltimSet.Position = def.AltimSet.Position
		ep.prefSet.Current.AltimSet.ShowBorder = def.AltimSet.ShowBorder
		ep.prefSet.Current.AltimSet.ShowIndicators = def.AltimSet.ShowIndicators
		ep.prefSet.Current.AltimSet.Lines = def.AltimSet.Lines
		ep.prefSet.Current.AltimSet.Col = def.AltimSet.Col
		ep.prefSet.Current.AltimSet.Font = def.AltimSet.Font
		ep.prefSet.Current.AltimSet.Bright = def.AltimSet.Bright
	}

	// Fill in WX defaults if this preference set was created before WX existed
	needsWXDefaults := ep.prefSet.Current.WX.Position == ([2]float32{})
	if needsWXDefaults {
		ep.prefSet.Current.WX.Position = def.WX.Position
		ep.prefSet.Current.WX.ShowBorder = def.WX.ShowBorder
		ep.prefSet.Current.WX.Lines = def.WX.Lines
		ep.prefSet.Current.WX.Font = def.WX.Font
		ep.prefSet.Current.WX.Bright = def.WX.Bright
	}

	// Fill in CheckList defaults if this preference set was created before
	// the check list view existed (Position zero is the marker).
	if ep.prefSet.Current.CheckList.Position == ([2]float32{}) {
		ep.prefSet.Current.CheckList.Position = def.CheckList.Position
		ep.prefSet.Current.CheckList.ShowBorder = def.CheckList.ShowBorder
		ep.prefSet.Current.CheckList.Lines = def.CheckList.Lines
		ep.prefSet.Current.CheckList.Font = def.CheckList.Font
		ep.prefSet.Current.CheckList.Highlight = def.CheckList.Highlight
		ep.prefSet.Current.CheckList.Text = def.CheckList.Text
	}
}

// Custom text characters. Some of these are not for all fonts. Size 11 has everything.
const insertCursor string = "o"
const thickUpArrow string = "p"
const thickDownArrow string = "q"
const checkMark string = "r"
const xMark string = "s"
const upArrow string = "t"
const downArrow string = "u"
const scratchpadArrow string = "v"
const locationSymbol string = "w"
const vci string = " x"
const circleClear string = "y"
const circleFilled string = "z"

type inputChar struct {
	char          rune
	location      math.Point2LL
	trackCallsign av.ADSBCallsign // set on 'locationSymbol' chars added by AddLocation when the click landed on a track
}

type inputText []inputChar

func (inp *inputText) Set(str string) {
	inp.Clear()
	for _, char := range formatInput(str) {
		*inp = append(*inp, inputChar{char: char})
	}
}

func (inp *inputText) AddLocation(location math.Point2LL, callsign av.ADSBCallsign) {
	// Trim trailing whitespace inputChars in place so prior chars (and the
	// click locations they carry) are preserved.
	for len(*inp) > 0 && unicode.IsSpace((*inp)[len(*inp)-1].char) {
		*inp = (*inp)[:len(*inp)-1]
	}
	for _, char := range formatInput(" " + locationSymbol + " ") {
		*inp = append(*inp, inputChar{char: char, location: location, trackCallsign: callsign})
	}
}

// No formatting needed
func (inp *inputText) AddBasic(str string) {
	for _, char := range formatInput(str) {
		*inp = append(*inp, inputChar{char: char})
	}
}

func formatInput(str string) string {
	output := strings.ReplaceAll(str, "`", circleClear)
	output = strings.ReplaceAll(output, "~", circleFilled)
	return output
}

func (inp *inputText) DeleteOne() {
	if len(*inp) > 0 {
		*inp = (*inp)[:len(*inp)-1]
	}
}

func (inp *inputText) Clear() {
	*inp = (*inp)[:0]
}

func (inp inputText) String() string {
	var sb strings.Builder
	for _, ic := range inp {
		sb.WriteString(string(ic.char))
	}
	return sb.String()
}

// feedbackMessage is the MCA feedback area's content: either nothing, a
// success message (green check + text), or an error message (red x + text).
// The text is stored with formatInput already applied so render-time work is
// just wrapping + drawing.
type feedbackMessage struct {
	kind feedbackKind
	msg  string
}

type feedbackKind int

const (
	feedbackNone feedbackKind = iota
	feedbackSuccess
	feedbackError
)

func (m *feedbackMessage) Success(str string) {
	m.kind = feedbackSuccess
	m.msg = formatInput(str)
}

func (m *feedbackMessage) Error(err error) {
	if err == nil {
		return
	}
	m.kind = feedbackError
	m.msg = formatInput(toUpper(err.Error()))
}

func (m *feedbackMessage) Clear() {
	*m = feedbackMessage{}
}

// AFAIK, you can only type white, regular characters in the input (apart from the location symbols)
func (ep *ERAMPane) processKeyboardInput(ctx *panes.Context) {
	if !ctx.HaveFocus || ctx.Keyboard == nil {
		return
	}
	ps := ep.currentPrefs()
	keyboardInput := strings.ToUpper(ctx.Keyboard.Input)
	ep.Input.AddBasic(keyboardInput)
	input := ep.Input.String()
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyG: // debugging
			if ctx.Keyboard.KeyControl() && ctx.Keyboard.KeyShift() && ctx.Mouse != nil {
				big := ctx.Mouse.Pos
				big[1] -= 38
				ps.MCA.Position = big
				ps.RA.Position = [2]float32{big[0] + 390, big[1]}
			}
		case imgui.KeyBackspace:
			if ep.commandMode == CommandModeDrawRoute {
				if n := len(ep.drawRoutePoints); n > 0 {
					ep.drawRoutePoints = ep.drawRoutePoints[:n-1]
					if len(ep.drawRoutePoints) > 0 {
						var cb []string
						for _, p := range ep.drawRoutePoints {
							cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
						}
						ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))
						ep.responseArea = fmt.Sprintf("DRAWROUTE: %d POINTS", len(ep.drawRoutePoints))
					} else {
						ep.responseArea = "DRAWROUTE"
					}
				}
			} else if len(ep.Input) > 0 {
				ep.Input = ep.Input[:len(ep.Input)-1]
			}
		case imgui.KeyEnter:
			// Cancel any in-progress view drag so an incidental click in the
			// MCA body doesn't leave the drag preview following the cursor
			// after the command completes.
			ep.viewRepo.Cancel()
			// Process the command
			status, err := ep.executeERAMCommand(ctx, ep.Input)
			ep.Input.Clear()
			ep.applyCommandStatus(ctx, status, err)
		case imgui.KeyEscape:
			if _, ok := ep.popup.(*deleteEntryPopup); ok {
				ep.popup = nil
				break
			}
			if ep.tearoffInProgress != "" || ep.deleteTearoffMode {
				if ep.tearoffInProgress != "" {
					ep.tearoffInProgress = ""
					ep.tearoffIsReposition = false
					ctx.Platform.EndCaptureMouse()
				}
				if ep.deleteTearoffMode {
					ep.deleteTearoffMode = false
					ep.ClearTemporaryCursor()
				}
				break
			}
			// Cancel any in-progress drag.
			anyActive := ep.viewRepo.activeID != ""
			if anyActive {
				ep.viewRepo.Cancel()
			}
			if !anyActive {
				if ep.commandMode == CommandModeDrawRoute {
					ep.commandMode = CommandModeNone
					ep.drawRoutePoints = nil
					ep.responseArea = ""
				}
				ep.Input.Clear()
				ep.feedbackArea.Clear()
			}
		case imgui.KeyTab:
			if input == "" {
				ep.Input.Set("TG ")
			}
		case imgui.KeyPageUp: // velocity vector *2
			if ep.VelocityTime == 0 {
				ep.VelocityTime = 1
			} else if ep.VelocityTime < 8 {
				ep.VelocityTime *= 2
			}
		case imgui.KeyPageDown: // velocity vector /2
			if ep.VelocityTime > 0 {
				ep.VelocityTime /= 2
			} else {
				ep.VelocityTime = 0
			}
		}
	}
}

func (ep *ERAMPane) drawPauseOverlay(ctx *panes.Context, cb *renderer.CommandBuffer) {
	if !ctx.Client.State.Paused {
		return
	}

	text := "SIMULATION PAUSED"
	font := ep.systemFont[3] // better font pls

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
		colors.pauseOverlay.background)

	// Draw text
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	td.AddTextCentered(text, [2]float32{width / 2, textY}, renderer.TextStyle{
		Font:  font,
		Color: colors.pauseOverlay.text,
	})

	// Apply transformations and draw
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, 0, 0, [2]float32{}, 0, 0)
	transforms.LoadWindowViewingMatrices(cb)
	quad.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (ep *ERAMPane) drawVideoMaps(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	// Precompute a BCGIndex → RGB lookup table once per frame so the hot
	// path becomes an array index. The 0 slot and any out-of-range or
	// empty-named slot stay at the zero value (black/invisible), matching
	// the previous defensive bcgColor closure.
	var bcgRGB [256]renderer.RGB
	base := colors.videoMapBase
	for i, name := range ep.bcgNames {
		if name == "" {
			continue
		}
		bcgRGB[i+1] = ps.VideoMapBrightness[name].ScaleRGB(base)
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	var solidLineBuf [][2]float32 // reuse across all lines/maps

	for _, vm := range ep.allVideoMaps {
		if _, ok := ps.VideoMapVisible[combine(vm.LabelLine1, vm.LabelLine2, " ")]; !ok {
			continue
		}

		for _, line := range vm.Lines {
			color := bcgRGB[line.BCGIndex]
			if line.Style == av.LineStyleSolid {
				solidLineBuf = solidLineBuf[:0]
				for _, p := range line.Points {
					solidLineBuf = append(solidLineBuf, transforms.WindowFromLatLongP(p))
				}
				ld.AddLineStrip(solidLineBuf, color)
			} else {
				pattern := dashPatternPixels(line.Style)
				if pattern == nil {
					continue
				}
				for i := 0; i+1 < len(line.Points); i++ {
					p0 := transforms.WindowFromLatLongP(line.Points[i])
					p1 := transforms.WindowFromLatLongP(line.Points[i+1])
					ld.AddDashPattern(p0, p1, pattern, color)
				}
			}
		}

		for _, s := range vm.Symbols {
			if font := ep.ERAMGeomapFont(int(s.Size)); font != nil {
				color := bcgRGB[s.BCGIndex]
				pw := transforms.WindowFromLatLongP(s.P)
				td.AddTextCentered(string(symbolGlyphIndex[s.Style]), pw,
					renderer.TextStyle{Font: font, Color: color})
			}
		}

		for _, l := range vm.Labels {
			if font := ep.ERAMFont(int(l.Size)); font != nil {
				color := bcgRGB[l.BCGIndex]
				pw := transforms.WindowFromLatLongP(l.P)
				pw[0] += float32(l.XOffset)
				pw[1] += float32(l.YOffset)
				style := renderer.TextStyle{Font: font, Color: color}
				if l.Opaque {
					style.DrawBackground = true
					style.BackgroundColor = renderer.RGB{}
				}
				td.AddText(l.Text, pw, style)
				if l.Underline {
					ext := font.LayoutBounds(l.Text, 0)
					// In window space y grows upward; AddText takes the upper-left corner, so the
					// baseline is at pw[1] - h.  Drop one more pixel so the underline sits just
					// below.
					y := pw[1] - ext.Height() - 1
					ld.AddLine([2]float32{pw[0], y}, [2]float32{pw[0] + ext.Width(), y}, color)
				}
			}
		}
	}

	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// Dash patterns in window-space pixels.
var (
	shortDashedPattern       = []float32{10, 14}
	longDashedPattern        = []float32{24, 24}
	longDashShortDashPattern = []float32{24, 11, 12, 12}
)

func dashPatternPixels(s av.LineStyle) []float32 {
	switch s {
	case av.LineStyleShortDashed:
		return shortDashedPattern
	case av.LineStyleLongDashed:
		return longDashedPattern
	case av.LineStyleLongDashShortDash:
		return longDashShortDashPattern
	default:
		return nil
	}
}

// symbolGlyphIndex maps each SymbolStyle to the unicode codepoint of its
// glyph in the EramGeomap-{16,18,20}.pcf bitmap fonts.
var symbolGlyphIndex = map[av.SymbolStyle]rune{
	av.SymbolStyleVOR:                 0x0B,
	av.SymbolStyleNDB:                 0x0B,
	av.SymbolStyleTACAN:               0x0F,
	av.SymbolStyleVOR_TACAN:           0x00,
	av.SymbolStyleDME:                 0x04,
	av.SymbolStyleRNAV:                0x09,
	av.SymbolStyleRNAVOnlyWaypoint:    0x07,
	av.SymbolStyleAirport:             0x0D,
	av.SymbolStyleSatelliteAirport:    0x02,
	av.SymbolStyleEmergencyAirport:    0x04,
	av.SymbolStyleHeliport:            0x0B,
	av.SymbolStyleOtherWaypoints:      0x0C,
	av.SymbolStyleAirwayIntersections: 0x09,
	av.SymbolStyleIAF:                 0x0D,
	av.SymbolStyleObstruction1:        0x00,
	av.SymbolStyleObstruction2:        0x06,
	av.SymbolStyleNuclear:             0x03,
	av.SymbolStyleRadar:               0x05,
}

func (ep *ERAMPane) makeMaps(client *client.ControlClient, lg *log.Logger) {
	ss := client.State
	ps := ep.currentPrefs()
	vmf, err := client.LoadVideoMapLibrary(ss.ControllerVideoMapFile)
	if err != nil {
		lg.Errorf("%v", err)
		return
	}

	maps := vmf.ERAMMapGroups[ep.currentPrefs().VideoMapGroup]

	for _, name := range maps.BCGNames {
		if name == "" {
			continue
		}
		if _, ok := ps.VideoMapBrightness[name]; !ok { // Only set default if missing
			ps.VideoMapBrightness[name] = 12
		}
	}

	ep.allVideoMaps = maps.Maps
	ep.bcgNames = maps.BCGNames

	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[string]interface{})
	}

	ep.videoMapLabel = combine(maps.LabelLine1, maps.LabelLine2, "\n")

	for _, name := range client.State.ControllerDefaultVideoMaps {
		if slices.ContainsFunc(ep.allVideoMaps, func(v av.ERAMMap) bool { return combine(v.LabelLine1, v.LabelLine2, " ") == name }) {
			ps.VideoMapVisible[name] = nil
		}
	}
}

func combine(x, y, sep string) string {
	x = strings.TrimSpace(x)
	y = strings.TrimSpace(y)

	if x == "" {
		return y
	}
	if y == "" {
		return x
	}
	return x + sep + y
}

// Mouse button helpers:
// When UseRightClick is set, logical primary = physical right button click, logical tertiary = physical left button click.
func (ep *ERAMPane) mousePrimaryClicked(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Clicked[platform.MouseButtonSecondary]
	}
	return m.Clicked[platform.MouseButtonPrimary]
}

func (ep *ERAMPane) mousePrimaryDown(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Down[platform.MouseButtonSecondary]
	}
	return m.Down[platform.MouseButtonPrimary]
}

func (ep *ERAMPane) mousePrimaryReleased(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Released[platform.MouseButtonSecondary]
	}
	return m.Released[platform.MouseButtonPrimary]
}

func (ep *ERAMPane) mouseTertiaryClicked(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Clicked[platform.MouseButtonPrimary]
	}
	return m.Clicked[platform.MouseButtonTertiary]
}

func (ep *ERAMPane) mouseTertiaryDown(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Down[platform.MouseButtonPrimary]
	}
	return m.Down[platform.MouseButtonTertiary]
}

func (ep *ERAMPane) mouseTertiaryReleased(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Released[platform.MouseButtonPrimary]
	}
	return m.Released[platform.MouseButtonTertiary]
}

// clearMousePrimaryConsumed clears the physical button used for logical primary so the click is not processed again.
func (ep *ERAMPane) clearMousePrimaryConsumed(m *platform.MouseState) {
	if m == nil {
		return
	}
	if ep.currentPrefs().UseRightClick {
		m.Clicked[platform.MouseButtonSecondary] = false
	} else {
		m.Clicked[platform.MouseButtonPrimary] = false
	}
}

// clearMouseTertiaryConsumed clears the physical button used for logical tertiary.
func (ep *ERAMPane) clearMouseTertiaryConsumed(m *platform.MouseState) {
	if m == nil {
		return
	}
	if ep.currentPrefs().UseRightClick {
		m.Clicked[platform.MouseButtonPrimary] = false
	} else {
		m.Clicked[platform.MouseButtonTertiary] = false
	}
}

// consumeMouseClick clears BOTH primary and tertiary consumed flags. Most
// View click handlers want this: the click should not fall through to any
// later handler whether it was logically primary or tertiary.
func (ep *ERAMPane) consumeMouseClick(m *platform.MouseState) {
	ep.clearMousePrimaryConsumed(m)
	ep.clearMouseTertiaryConsumed(m)
}
