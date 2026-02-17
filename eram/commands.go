package eram

import (
	"fmt"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

// toUpper uppercases the input but preserves the lowercase
// location symbol (w) so clicked locations display correctly in errors.
func toUpper(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if string(r) == locationSymbol {
			b.WriteRune(r)
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

type CommandMode int

const (
	CommandModeNone CommandMode = iota
	CommandModeDrawRoute
)

func (ep *ERAMPane) consumeMouseEvents(ctx *panes.Context, transforms radar.ScopeTransformations) {
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	ps := ep.currentPrefs()
	if (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonSecondary] ||
		ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
		ctx.KeyboardFocus.Take(ep)
	}
	if ep.mousePrimaryReleased(mouse) {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			mouseLatLong := transforms.LatLongFromWindowP(mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))

		}
	}
	if ep.mouseTertiaryReleased(mouse) {
		// Try execute a clicked command on the closest track.
		trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms)
		if trk != nil {
			status := ep.executeERAMClickedCommand(ctx, ep.Input, trk, transforms)
			ep.Input.Clear()
			if status.err != nil {
				ep.bigOutput.displayError(ep.currentPrefs(), status.err)
			} else if status.bigOutput != "" {
				ep.bigOutput.displaySuccess(ep.currentPrefs(), status.bigOutput)
			} else if status.output != "" {
				ep.smallOutput.Set(ps, status.output)
			}
		}
	}
	// try get closest track

	// pan

	if mouse.Dragging[platform.MouseButtonSecondary] {
		delta := mouse.DragDelta
		if delta[0] != 0 || delta[1] != 0 {
			deltaLL := transforms.LatLongFromWindowV(delta)
			ps.CurrentCenter = math.Sub2f(ps.CurrentCenter, deltaLL)
		}
	}

	if ep.mousePrimaryClicked(mouse) {
		if ep.commandMode == CommandModeDrawRoute {
			pos := transforms.LatLongFromWindowP(mouse.Pos)
			ep.drawRoutePoints = append(ep.drawRoutePoints, pos)
			var cb []string
			for _, p := range ep.drawRoutePoints {
				cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
			}
			ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))
			ep.smallOutput.Set(ps, fmt.Sprintf("DRAWROUTE: %d POINTS", len(ep.drawRoutePoints)))
		} else if ep.Input.String() != "" {
			pos := transforms.LatLongFromWindowP(mouse.Pos)
			ep.Input.AddLocation(ps, pos)
		}
	}

	// zoom
	if z := mouse.Wheel[1]; z != 0 {

		r := ps.Range
		ps.Range += func() float32 {
			var amt float32 = 10

			if r < 2 {
				amt = .25
			} else if r == 2 {
				if z > 0 {
					amt = 1
				} else {
					amt = .25
				}
			} else if r < 10 {
				amt = 1
			} else if r == 10 {
				if z > 0 {
					amt = 10
				} else {
					amt = 1
				}
			}
			return amt * z
		}()
		ps.Range = math.Clamp(ps.Range, .25, 1300) // 4-33

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

type CommandStatus struct {
	clear     bool
	output    string
	bigOutput string
	err       error
}

func (ep *ERAMPane) executeERAMCommand(ctx *panes.Context, cmdLine inputText) (status CommandStatus) {
	original := cmdLine.String()

	// Extract all embedded locations from clicking while typing
	var mousePositions [][2]float32
	for _, ic := range cmdLine {
		if string(ic.char) == locationSymbol {
			mousePositions = append(mousePositions, ic.location)
		}
	}
	hasClick := len(mousePositions) > 0

	// First try the new parser-based command system
	if newStatus, err, handled := ep.tryExecuteUserCommand(ctx, original, nil, hasClick, mousePositions, true, radar.ScopeTransformations{}); handled {
		status.clear = newStatus.clear
		status.output = newStatus.output
		status.bigOutput = newStatus.bigOutput
		status.err = err
		return
	}

	return
}

func (ep *ERAMPane) numberToLLDirection(ctx *panes.Context, cmd byte) math.CardinalOrdinalDirection {
	var dir math.CardinalOrdinalDirection
	switch cmd {
	case '1':
		dir = math.SouthWest
	case '2':
		dir = math.South
	case '3':
		dir = math.SouthEast
	case '4':
		dir = math.West
	case '5':
		dir = math.NorthEast
	case '6':
		dir = math.East
	case '7':
		dir = math.NorthWest
	case '8':
		dir = math.North
	case '9':
		dir = math.NorthEast
	}
	return dir
}

func (ep *ERAMPane) deleteFLightplan(ctx *panes.Context, trk sim.Track) {
	ctx.Client.DeleteFlightPlan(sim.ACID(trk.ADSBCallsign.String()), func(err error) {
		if err != nil {
			ep.bigOutput.displayError(ep.currentPrefs(), err)
			return
		}
	})
}

func (ep *ERAMPane) runAircraftCommands(ctx *panes.Context, callsign av.ADSBCallsign, cmds string) {
	ep.targetGenLastCallsign = callsign

	ctx.Client.RunAircraftCommands(client.AircraftCommandRequest{
		Callsign: callsign,
		Commands: cmds,
	}, func(errStr string, remaining string) {
		if errStr != "" {

			if err := server.TryDecodeErrorString(errStr); err != nil {
			}
		}
	})
}

// Mainly used for ERAM assigned/ interm alts. May be used for actually changing routes.
func (ep *ERAMPane) modifyFlightPlan(ctx *panes.Context, cid string, spec sim.FlightPlanSpecifier) {
	trk, ok := ctx.Client.State.GetTrackByFLID(cid)
	if !ok {
		ep.bigOutput.displayError(ep.currentPrefs(), ErrERAMIllegalACID)
		return
	}

	if trk.FlightPlan != nil {
		if spec.Scratchpad.IsSet {
			trk.FlightPlan.Scratchpad = spec.Scratchpad.Value
			if spec.Scratchpad.Value == "" {
				trk.FlightPlan.PriorScratchpad = ""
			}
		}
		if spec.SecondaryScratchpad.IsSet {
			trk.FlightPlan.SecondaryScratchpad = spec.SecondaryScratchpad.Value
			if spec.SecondaryScratchpad.Value == "" {
				trk.FlightPlan.PriorSecondaryScratchpad = ""
			}
		}
	}

	acid := sim.ACID(trk.ADSBCallsign)
	ctx.Client.ModifyFlightPlan(acid, spec,
		func(err error) {
			if err != nil {
				ep.bigOutput.displayError(ep.currentPrefs(), err)
			}
		})
	// Send aircraft commands if an ERAM command is entered
	if ep.DisableERAMtoRadio {
		return
	}
	if alt := spec.AssignedAltitude.Value + spec.InterimAlt.Value; alt > 0 { // Only one will be set
		var cmd string
		state := ep.TrackState[trk.ADSBCallsign]
		if alt > int(state.Track.TransponderAltitude) {
			cmd = "C" + fmt.Sprint(alt/100)
		} else {
			cmd = "D" + fmt.Sprint(alt/100)
		}
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
	}
}

func (ep *ERAMPane) getACIDFromCID(ctx *panes.Context, cid string) (sim.ACID, error) {
	trk, ok := ctx.Client.State.GetTrackByFLID(cid)
	if !ok {
		return "", ErrERAMIllegalACID
	}
	return sim.ACID(trk.ADSBCallsign.String()), nil
}

func (ep *ERAMPane) acceptHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcceptHandoff(acid,
		func(err error) { ep.bigOutput.displayError(ep.currentPrefs(), err) })
}

func (ep *ERAMPane) recallHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.CancelHandoff(acid,
		func(err error) { ep.bigOutput.displayError(ep.currentPrefs(), err) })
}

func (ep *ERAMPane) getQULines(ctx *panes.Context, acid sim.ACID, minutes int) {
	ctx.Client.SendRouteCoordinates(acid, minutes, func(err error) {
		if err != nil {
			ep.bigOutput.displayError(ep.currentPrefs(), err)
		}
	})
}

func (ep *ERAMPane) tgtGenDefaultCallsign(ctx *panes.Context) av.ADSBCallsign {
	if cs := ctx.Client.LastTTSCallsign(); cs != "" {
		// If TTS is active, return the last TTS transmitter.
		return cs
	}
	// Otherwise the most recent one the user selected.
	return ep.targetGenLastCallsign
}

func (ep *ERAMPane) flightPlanDirect(ctx *panes.Context, acid sim.ACID, fix string) error {
	ctx.Client.FlightPlanDirect(acid, fix, func(err error) {
		if err != nil {
			ep.bigOutput.displayError(ep.currentPrefs(), err)
		}
	})
	trk, _ := ctx.Client.State.GetTrackByACID(acid)
	if !ep.DisableERAMtoRadio && trk != nil {
		cmd := "D" + fix
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
	}
	return nil
}

// closestTrackToLL returns the closest track to the given lat/long within maxNm.
// Returns nil if no track is within that distance.
func (ep *ERAMPane) closestTrackToLL(ctx *panes.Context, loc math.Point2LL, maxNm float32) *sim.Track {
	var best *sim.Track
	bestDist := maxNm
	for _, t := range ctx.Client.State.Tracks {
		d := math.NMDistance2LL(t.Location, loc)
		if d <= bestDist {
			bestDist = d
			best = t
		}
	}
	return best
}

func (ep *ERAMPane) handoffTrack(ctx *panes.Context, acid sim.ACID, controller string) error {
	control, err := ep.lookupControllerForID(ctx, controller, acid)
	if err != nil {
		ep.bigOutput.displayError(ep.currentPrefs(), err)
		return err
	}
	if control == nil {
		return ErrERAMIllegalPosition
	}

	ctx.Client.HandoffTrack(acid, control.PositionId(),
		func(err error) { ep.bigOutput.displayError(ep.currentPrefs(), err) })

	return nil
}

func (ep *ERAMPane) tryGetClosestTrack(ctx *panes.Context, mousePosition [2]float32, transforms radar.ScopeTransformations) (*sim.Track, float32) {
	var trk *sim.Track
	distance := float32(20)

	for _, t := range ctx.Client.State.Tracks {
		pw := transforms.WindowFromLatLongP(t.Location)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			trk = t
			distance = dist
		}
	}

	return trk, distance
}

func (ep *ERAMPane) executeERAMClickedCommand(ctx *panes.Context, cmdLine inputText, trk *sim.Track, transforms radar.ScopeTransformations) (status CommandStatus) {
	if trk == nil {
		status.err = ErrERAMIllegalACID
		return
	}

	original := cmdLine.String()

	// Use the new parser-based command system for clicked commands
	if newStatus, err, handled := ep.tryExecuteUserCommand(ctx, original, trk, true, nil, false, transforms); handled {
		status.clear = newStatus.clear
		status.output = newStatus.output
		status.bigOutput = newStatus.bigOutput
		status.err = err
		return
	}

	// No fallback for clicked commands - all should be handled by the parser
	return
}

func (ep *ERAMPane) lookupControllerForID(ctx *panes.Context, controller string, acid sim.ACID) (*av.Controller, error) {
	// Look at the length of the controller string passed in. If it's one character, ERAM would have to find which controller it goes to.
	// That is not here yet, so return an error.
	if len(controller) == 1 {
		return nil, ErrERAMSectorNotActive
	}

	for _, control := range ctx.Client.State.Controllers {
		if control.ERAMID() == controller {
			return control, nil
		}
	}
	return nil, ErrERAMSectorNotActive
}
