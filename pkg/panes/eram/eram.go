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
)

var (
	ERAMPopupPaneBackgroundColor = renderer.RGB{R: 0, G: 0, B: 0}
	// ERAMBorderColor			 = renderer.RGB
	// ERAMButtonColor			 = renderer.RGB
	// ERAMToolbarColor			 = renderer.RGB

)

type ERAMPane struct {
	ERAMPreferenceSets map[string]*PrefrenceSet
	prefSet            *PrefrenceSet
	TrackState         map[av.ADSBCallsign]*TrackState

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

	// TODO: initialize fonts and audio

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
	// Draw video maps
	// Draw routes
	// draw dcb
	scopeExtent = ep.drawtoolbar(ctx, transforms, cb)
	cb.SetScissorBounds(scopeExtend, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	// Draw history
	dbs := ep.getAllDatablocks(ctx, tracks)
	ep.drawLeaderLines(ctx, tracks, dbs, transforms, cb)
	ep.drawPTLs(ctx, tracks, transforms, cb)
	ep.drawTracks(ctx, tracks, transforms, cb)
	ep.drawDatablocks(tracks, dbs, ctx, transforms, cb)
	// Draw QU /M lines (not sure where this goes)
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
}
func (ep *ERAMPane) Hide() bool {
	return false
}

func (ep *ERAMPane) LoadedSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the LoadedSim method to satisfy panes.Pane interface
}

func (ep *ERAMPane) ResetSim(client *client.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the ResetSim method to satisfy panes.Pane interface
}

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
			// ep.processCommandInput()
			ep.Input = ""
		case imgui.KeyEscape:
			// Clear the input
			ep.Input = ""
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

// func (sp *ERAMPane) drawVideoMaps(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
// 	ps := sp.currentPrefs()

// 	transforms.LoadLatLongViewingMatrices(cb)

// 	cb.LineWidth(1, ctx.DPIScale)
// 	var draw []sim.VideoMap
// 	for _, vm := range sp.allVideoMaps {
// 		if _, ok := ps.VideoMapVisible[vm.Id]; ok {
// 			draw = append(draw, vm)
// 		}
// 	}
// 	slices.SortFunc(draw, func(a, b sim.VideoMap) int { return a.Id - b.Id })

// 	for _, vm := range draw {
// 		cidx := math.Clamp(vm.Color-1, 0, numMapColors-1) // switch to 0-based indexing
// 		color := brite.ScaleRGB(mapColors[vm.Group][cidx])

// 		cb.SetRGB(color)
// 		cb.Call(vm.CommandBuffer)
// 	}
// }