package eram

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/AllenDang/cimgui-go/imgui"
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/client"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
	"golang.org/x/exp/slices"
)

var (
	ERAMPopupPaneBackgroundColor = renderer.RGB{R: 0, G: 0, B: 0}
	ERAMYellow                   = renderer.RGB{R: .894, G: .894}
)

const numMapColors = 8

var mapColors [2][numMapColors]renderer.RGB = [2][numMapColors]renderer.RGB{
	[numMapColors]renderer.RGB{ // Group A
		renderer.RGBFromUInt8(140, 140, 140),
		renderer.RGBFromUInt8(0, 255, 255),
		renderer.RGBFromUInt8(255, 0, 255),
		renderer.RGBFromUInt8(238, 201, 0),
		renderer.RGBFromUInt8(238, 106, 80),
		renderer.RGBFromUInt8(162, 205, 90),
		renderer.RGBFromUInt8(218, 165, 32),
		renderer.RGBFromUInt8(72, 118, 255),
	},
	[numMapColors]renderer.RGB{ // Group B
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
	ERAMPreferenceSets map[string]*PrefrenceSet
	prefSet            *PrefrenceSet
	TrackState         map[av.ADSBCallsign]*TrackState

	events *sim.EventsSubscription

	systemFont [10]*renderer.Font

	allVideoMaps []sim.VideoMap

	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string

	// Output and input text for the command line interface.
	smallOutput string
	bigOutput   string
	Input       string

	activeToolbarMenu int
	toolbarVisible    bool

	lastTrackUpdate time.Time

	fdbArena util.ObjectArena[fullDatablock]
	ldbArena util.ObjectArena[limitedDatablock]

	repositionLargeInput  bool
	repositionSmallOutput bool
	timeSinceRepo         time.Time

	velocityTime int // 0, 1, 4, or 8 minutes

	dbLastAlternateTime time.Time // Alternates every 6 seconds
	dbAlternate         bool

	targetGenLastCallsign av.ADSBCallsign

	aircraftFixCoordinates map[string]aircraftFixCoordinates
}

func NewERAMPane() *ERAMPane {
	return &ERAMPane{}
}

func (p *ERAMPane) Activate(r renderer.Renderer, pl platform.Platform, es *sim.EventStream, log *log.Logger) {
	// Activate maps
	if p.InboundPointOuts == nil {
		p.InboundPointOuts = make(map[string]string)
	}
	if p.OutboundPointOuts == nil {
		p.OutboundPointOuts = make(map[string]string)
	}

	if p.TrackState == nil {
		p.TrackState = make(map[av.ADSBCallsign]*TrackState)
	}

	if p.aircraftFixCoordinates == nil {
		p.aircraftFixCoordinates = make(map[string]aircraftFixCoordinates)
	}

	p.events = es.Subscribe()

	// TODO: initialize fonts and audio
	p.initializeFonts(r, pl)

	// Activate weather radar, events
	p.prefSet = &PrefrenceSet{}
	p.prefSet.Current = *p.initPrefsForLoadedSim()
}

func init() {
	panes.RegisterUnmarshalPane("ERAMPane", func(d []byte) (panes.Pane, error) {
		var p ERAMPane
		err := json.Unmarshal(d, &p)
		return &p, err
	})
}
func (ep *ERAMPane) CanTakeKeyboardFocus() bool { return true }

func (ep *ERAMPane) Draw(ctx *panes.Context, cb *renderer.CommandBuffer) {
	ep.processEvents(ctx)

	// Tracks: get visible tracks (500nm?) and update them.
	scopeExtent := ctx.PaneExtent
	ps := ep.currentPrefs()

	tracks := ep.visibleTracks(ctx)
	ep.updateRadarTracks(ctx, tracks)

	// draw the ERAMPane
	cb.ClearRGB(ps.Brightness.Background.ScaleRGB(renderer.RGB{0, 0, .506})) // Scale this eventually
	ep.processKeyboardInput(ctx)
	// ctr := UserCenter
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, ctx.MagneticVariation, ctx.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range), 0)
	scopeExtend := ctx.PaneExtent

	// Following are the draw functions. They are listed in the best of my ability

	// Draw weather
	ep.drawVideoMaps(ctx, transforms, cb)
	// Draw routes
	// draw dcb
	scopeExtent = ep.drawtoolbar(ctx, transforms, cb)
	cb.SetScissorBounds(scopeExtend, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	ep.drawHistoryTracks(ctx, tracks, transforms, cb)
	dbs := ep.getAllDatablocks(ctx, tracks)
	ep.drawLeaderLines(ctx, tracks, dbs, transforms, cb)
	ep.drawPTLs(ctx, tracks, transforms, cb)
	ep.drawTargets(ctx, tracks, transforms, cb)
	ep.drawTracks(ctx, tracks, transforms, cb)
	ep.drawDatablocks(tracks, dbs, ctx, transforms, cb)
	ep.drawJRings(ctx, tracks, transforms, cb)
	ep.drawQULines(ctx, transforms, cb)
	// Draw clock
	// Draw views
	// Draw long readout (output with no command line)
	ep.drawCommandInput(ctx, transforms, cb)
	// Draw TOOLBAR button/ menu.
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
func (ep *ERAMPane) Hide() bool {
	return false
}

func (ep *ERAMPane) LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	ep.makeMaps(client, ss, lg)
}

func (ep *ERAMPane) ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	ep.makeMaps(client, ss, lg)
}

// Custom text characters. Some of these are not for all fonts. Size 11 has everything.
const upArrow string = "t"
const downArrow string = "u"
const scratchpadArrow string = "v"
const vci string = " x"
const circleClear string = "y"
const circleFilled string = "z"
const insertCursor string = "o"
const checkMark string = "r"
const xMark string = "s"
const thickUpArrow string = "p"
const thickDownArrow string = "q"

func (ep *ERAMPane) processKeyboardInput(ctx *panes.Context) {
	if !ctx.HaveFocus || ctx.Keyboard == nil {
		return
	}
	input := strings.ToUpper(ctx.Keyboard.Input)
	ep.Input += input
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if len(ep.Input) > 0 {
				ep.Input = ep.Input[:len(ep.Input)-1]
			}
		case imgui.KeyEnter:
			// Process the command
			status := ep.executeERAMCommand(ctx, ep.Input)
			ep.Input = ""
			if status.err != nil {
				ep.bigOutput = status.err.Error()
			}
		case imgui.KeyEscape:
			// Clear the input
			if ep.repositionLargeInput || ep.repositionSmallOutput {
				ep.repositionLargeInput = false
				ep.repositionSmallOutput = false
			} else {
				ep.Input = ""
				ep.bigOutput = ""
			}
		case imgui.KeyTab:
			ep.Input = "TG "
		case imgui.KeyPageUp: // velocity vector *2
			if ep.velocityTime == 0 {
				ep.velocityTime = 1
			} else if ep.velocityTime < 8 {
				ep.velocityTime *= 2
			}
		case imgui.KeyPageDown: // velocity vector /2
			if ep.velocityTime > 0 {
				ep.velocityTime /= 2
			} else {
				ep.velocityTime = 0
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
		renderer.RGB{R: 1, G: 0, B: 0})        // Solid red

	// Draw text
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	td.AddTextCentered(text, [2]float32{width / 2, textY}, renderer.TextStyle{
		Font:  font,
		Color: renderer.RGB{R: 1, G: 1, B: 1},
	})

	// Apply transformations and draw
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, 0, 0, [2]float32{}, 0, 0)
	transforms.LoadWindowViewingMatrices(cb)
	quad.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (ep *ERAMPane) drawVideoMaps(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	transforms.LoadLatLongViewingMatrices(cb)

	cb.LineWidth(1, ctx.DPIScale)
	var draw []sim.VideoMap
	for _, vm := range ep.allVideoMaps {
		if _, ok := ps.VideoMapVisible[vm.Id]; ok {
			draw = append(draw, vm)
		}
	}
	slices.SortFunc(draw, func(a, b sim.VideoMap) int { return a.Id - b.Id })

	for _, vm := range draw {
		cidx := math.Clamp(vm.Color-1, 0, numMapColors-1)
		color := mapColors[vm.Group][cidx] // TODO: change this out for custom brightnesses.

		cb.SetRGB(color)
		cb.Call(vm.CommandBuffer)
	}
}

func (ep *ERAMPane) makeMaps(client *client.ControlClient, ss sim.State, lg *log.Logger) {
	vmf, err := ep.getVideoMapLibrary(ss, client)
	if err != nil {
		lg.Errorf("%v", err)
		return
	}
	ep.allVideoMaps = vmf.Maps

	ps := ep.currentPrefs()
	for k := range ps.VideoMapVisible {
		delete(ps.VideoMapVisible, k)
	}
	for _, name := range ss.ControllerDefaultVideoMaps {
		if idx := slices.IndexFunc(ep.allVideoMaps, func(v sim.VideoMap) bool { return v.Name == name }); idx != -1 {
			ps.VideoMapVisible[ep.allVideoMaps[idx].Id] = nil
		}
	}
}

func (ep *ERAMPane) getVideoMapLibrary(ss sim.State, client *client.ControlClient) (*sim.VideoMapLibrary, error) {
	filename := ss.STARSFacilityAdaptation.VideoMapFile
	if ml, err := sim.HashCheckLoadVideoMap(filename, ss.VideoMapLibraryHash); err == nil {
		return ml, nil
	}
	return client.GetVideoMapLibrary(filename)
}
