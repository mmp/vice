// plugin.go

package main

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

type PluginPane struct {
	ExecutablePath string
	Arguments      string

	PluginName string

	fileSelect *FileSelectDialogBox

	canTakeKeyboardFocus bool

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	decoder *json.Decoder
	encoder *json.Encoder

	eventsId  EventSubscriberId
	firstDraw bool
}

func NewPluginPane(name string) *PluginPane {
	return &PluginPane{PluginName: name}
}

func (pp *PluginPane) Name() string {
	return "Plugin: " + pp.PluginName
}

func (pp *PluginPane) Duplicate(nameAsCopy bool) Pane {
	return &PluginPane{
		ExecutablePath: pp.ExecutablePath,
		PluginName:     pp.PluginName + " Copy",
		eventsId:       eventStream.Subscribe(),
	}
}

func (pp *PluginPane) launch() {
	if pp.ExecutablePath == "" {
		return
	}

	pp.cmd = exec.Command(pp.ExecutablePath, strings.Fields(pp.Arguments)...)
	var err error
	if pp.stdin, err = pp.cmd.StdinPipe(); err != nil {
		lg.Errorf("%+v", err)
		// TODO: cleanup
	} else {
		pp.encoder = json.NewEncoder(pp.stdin)
	}

	if pp.stdout, err = pp.cmd.StdoutPipe(); err != nil {
		lg.Errorf("%+v", err)
		// TODO: cleanup
	} else {
		pp.decoder = json.NewDecoder(pp.stdout)
	}

	if err := pp.cmd.Start(); err != nil {
		lg.Errorf("%+v", err) // TODO: dialog box or whatever
		pp.stdin = nil
		pp.encoder = nil
		pp.stdout = nil
		pp.decoder = nil
		pp.cmd = nil
	}
}

func (pp *PluginPane) Activate() {
	pp.launch()
	if pp.eventsId == InvalidEventSubscriberId {
		pp.eventsId = eventStream.Subscribe()
	}
	pp.firstDraw = true

	pp.fileSelect = NewFileSelectDialogBox("Select plugin executable", nil, "",
		func(s string) {
			pp.kill()
			pp.ExecutablePath = s
			pp.launch()
		})
}

func (pp *PluginPane) kill() {
	if pp.cmd == nil {
		return
	}

	pp.stdin.Close()
	pp.stdin = nil
	pp.stdout.Close()
	pp.stdout = nil
	pp.decoder = nil

	if err := pp.cmd.Process.Kill(); err != nil {
		lg.Errorf("%+v", err) // TODO: dialog box or whatever
	}
	pp.cmd = nil
}

func (pp *PluginPane) Deactivate() {
	pp.kill()

	eventStream.Unsubscribe(pp.eventsId)
	pp.eventsId = InvalidEventSubscriberId
}

func (pp *PluginPane) CanTakeKeyboardFocus() bool { return pp.canTakeKeyboardFocus }

type PluginUpdate struct {
	Context struct {
		WindowResolution [2]int
		Mouse            *MouseState
		Keyboard         *KeyboardState
		HaveFocus        bool

		NmPerLatitude     float32
		NmPerLongitude    float32
		MagneticVariation float32

		CurrentTime time.Time
	}
	Aircraft           []*Aircraft
	RemovedAircraft    []string
	Controllers        []*Controller
	RemovedControllers []string
}

type DrawCommand interface {
	Type() string
}

type ClearWindowDrawCommand struct {
	Color RGB
}

type LinesDrawCommand struct {
	Color           RGB
	VertexPositions [][2]float32
}

type PluginResponse struct {
	DrawCommands         []DrawCommand
	Errors               []string
	CanTakeKeyboardFocus bool
	TakeFocus            bool
}

func (pp *PluginPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	if pp.cmd == nil {
		return
	}

	var update PluginUpdate

	update.Context.WindowResolution[0] = int(ctx.paneExtent.Width())
	update.Context.WindowResolution[1] = int(ctx.paneExtent.Height())
	update.Context.Mouse = ctx.mouse
	update.Context.Keyboard = ctx.keyboard
	update.Context.HaveFocus = ctx.haveFocus

	update.Context.NmPerLatitude = database.NmPerLatitude
	update.Context.NmPerLongitude = database.NmPerLongitude
	update.Context.MagneticVariation = database.MagneticVariation

	update.Context.CurrentTime = server.CurrentTime()

	// Always do drain the event stream, even the first time when we're going to send everything.
	updatedAircraft := make(map[string]*Aircraft)
	removedAircraft := make(map[string]interface{})
	updatedControllers := make(map[string]*Controller)
	removedControllers := make(map[string]interface{})

	for _, event := range ctx.events.Get(pp.eventsId) {
		switch e := event.(type) {
		case *AddedAircraftEvent:
			updatedAircraft[e.ac.Callsign] = e.ac

		case *ModifiedAircraftEvent:
			updatedAircraft[e.ac.Callsign] = e.ac

		case *RemovedAircraftEvent:
			removedAircraft[e.ac.Callsign] = nil

		case *AddedControllerEvent:
			updatedControllers[e.Controller.Callsign] = e.Controller

		case *RemovedControllerEvent:
			removedControllers[e.Controller.Callsign] = nil

		case *ReceivedMETAREvent:
			// TODO: note also we need to send the current initial...
			// (may need some more ATCControl interface support...)

		case *ReceivedATISEvent:
			// TODO: note also we need to send the current initial...

		}
	}

	if pp.firstDraw {
		update.Aircraft = server.GetAllAircraft()
		update.Controllers = server.GetAllControllers()
		pp.firstDraw = false
	} else {
		for callsign := range removedAircraft {
			delete(updatedAircraft, callsign)
		}
		_, update.Aircraft = FlattenMap(updatedAircraft)

		for callsign := range removedControllers {
			delete(updatedControllers, callsign)
		}
		_, update.Controllers = FlattenMap(updatedControllers)
	}

	if err := pp.encoder.Encode(update); err != nil {
		lg.Errorf("encode %+v", err)
	} else {
		var resp PluginResponse
		if err := pp.decoder.Decode(&resp); err != nil {
			lg.Errorf("decode %+v", err)
		} else {
			lg.Printf("Got %+v", resp)
		}

		pp.canTakeKeyboardFocus = resp.CanTakeKeyboardFocus
		if resp.TakeFocus {
			wmTakeKeyboardFocus(pp, false)
		}
	}
}

func (pp *PluginPane) DrawUI() {
	imgui.Text("Executable: " + pp.ExecutablePath)
	imgui.SameLine()
	if imgui.Button("Select...") {
		pp.fileSelect.Activate()
	}
	pp.fileSelect.Draw()
	imgui.InputText("Arguments", &pp.Arguments)
}
