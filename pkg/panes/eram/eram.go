package eram

import (
	"encoding/json"

	"github.com/mmp/vice/pkg/client"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
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

	Aircraft map[string]*AircraftState

	allVideoMaps []sim.VideoMap

	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string

	activeToolbarMenu int
	toolbarVisible    bool
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

	if p.Aircraft == nil {
		p.Aircraft = make(map[string]*AircraftState)
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
	// Process events

	// Tracks: get visible tracks (500nm?) and update them.

	ps := ep.currentPrefs()

	// draw the ERAMPane
	cb.ClearRGB(ps.Brightness.Background.ScaleRGB(renderer.RGB{0, 0, .506})) // Scale this eventually
	// process keyboard input (ie. commands)
	// ctr := UserCenter
	transforms := radar.GetScopeTransformations(ctx.PaneExtent, ctx.MagneticVariation, ctx.NmPerLongitude,
		ps.CurrentCenter, float32(ps.Range), 0)
	scopeExtend := ctx.PaneExtent

	// Following are the draw functions. They are listed in the best of my ability

	// Draw weather
	// Draw video maps
	// Draw routes
	// draw dcb
	ep.drawtoolbar(ctx, transforms, cb)
	cb.SetScissorBounds(scopeExtend, ctx.Platform.FramebufferSize()[1]/ctx.Platform.DisplaySize()[1])
	// Draw history
	// Get datablocks
	// Draw leader lines
	// Draw stingers (PTL lines)
	// Draw tracks
	// Draw datablocks
	// Draw QU /M lines (not sure where this goes)
	// Draw clock
	// Draw views
	// Draw long readout (output with no command line)
	ep.drawCommandInput(ctx, transforms, cb)
	// Draw TOOLBAR button/ menu.
	// The TOOLBAR tearoff is different from the toolbar (DCB). It overlaps the toolbar and tracks and everything else I've tried.
	ep.drawMasterMenu(ctx, cb)
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
