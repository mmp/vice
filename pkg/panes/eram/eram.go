package eram

import (
	"encoding/json"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/log"
)
var (
	ERAMPopupPaneBackgroundColor = renderer.RGB{R: 0, G: 0, B: 0}
	// ERAMBorderColor			 = renderer.RGB
	// ERAMButtonColor			 = renderer.RGB
	// ERAMToolbarColor			 = renderer.RGB

)

type ERAMPane struct {
	ERAMPreferenceSets map[string]*PrefrenceSet
	prefSet *PrefrenceSet

	Aircraft map[string]*AircraftState

	allVideoMaps []av.VideoMap
	
	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string

	// These colors can be changed (in terms of brightness)
	ERAMBackgroundColor renderer.RGB // 0,0,.48
	activeToolbarMenu int 

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

	// Colors
	p.ERAMBackgroundColor = renderer.RGB{R: 0, G: 0, B: 0.48}

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
	ps := ep.currentPrefs()
	// draw the ERAMPane
	cb.ClearRGB(ep.ERAMBackgroundColor) // Scale this eventually 

	transforms := GetScopeTransformations(ctx.PaneExtent, ctx.ControlClient.MagneticVariation, ctx.ControlClient.NmPerLongitude, 
	ps.CurrentCenter, float32(ps.Range), 0)
	// scopeExtend := ctx.PaneExtent

	// draw dcb
	ep.drawtoolbar(ctx, transforms, cb)

	
}
func (ep *ERAMPane) Hide() bool {
	return false
}

func (ep *ERAMPane) LoadedSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the LoadedSim method to satisfy panes.Pane interface
}

func (ep *ERAMPane) ResetSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the ResetSim method to satisfy panes.Pane interface
}

type ERAMBrightness int 