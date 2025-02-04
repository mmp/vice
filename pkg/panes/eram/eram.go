package eram

import (
	"encoding/json"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/log"
)


type ERAMPane struct {
	Aircraft map[string]*AircraftState

	allVideoMaps []av.VideoMap
	
	InboundPointOuts  map[string]string
	OutboundPointOuts map[string]string

	// These colors can be changed (in terms of brightness)
	ERAMBackgroundColor renderer.RGB // find out what the colors are
	ERAMPaneBackgroundColor renderer.RGB // the background of the popup windows (eg. wx window or the command entry window)

}

func (p *ERAMPane) Activate(r renderer.Renderer, pl platform.Platform, es *sim.EventStream, log *log.Logger) {
    // implement the Activate method to satisfy panes.Pane interface
    if p.InboundPointOuts == nil {
        p.InboundPointOuts = make(map[string]string)
    }
    if p.OutboundPointOuts == nil {
        p.OutboundPointOuts = make(map[string]string)
    }
    // TODO: initialize fonts and audio

    if p.Aircraft == nil {
        p.Aircraft = make(map[string]*AircraftState)
    }

    // Activate weather radar, events
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
	// draw the ERAMPane
}
func (ep *ERAMPane) Hide() bool {
	return false
}

func (ep *ERAMPane) LoadedSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the LoadedSim method to satisfy panes.Pane interface
}

func (ep *ERAMPane) ResetSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	// implement the ResetSim method to satisfy panes.Pane interface
}