package eram

import (
	"fmt"
	"strconv"
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
		// A middle-click is treated as "left-click + [Enter] at this position"; append the click to
		// the input and execute.
		pos := transforms.LatLongFromWindowP(mouse.Pos)
		var callsign av.ADSBCallsign
		if trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms); trk != nil {
			callsign = trk.ADSBCallsign
		}
		// Skip an empty middle-click on empty space: nothing to dispatch.
		if callsign != "" || ep.Input.String() != "" {
			ep.Input.AddLocation(pos, callsign)
			status, err := ep.executeERAMCommand(ctx, ep.Input)
			ep.Input.Clear()
			ep.applyCommandStatus(ctx, status, err)
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
			ep.responseArea = fmt.Sprintf("DRAWROUTE: %d POINTS", len(ep.drawRoutePoints))
		} else if ep.Input.String() != "" {
			pos := transforms.LatLongFromWindowP(mouse.Pos)
			var callsign av.ADSBCallsign
			if trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms); trk != nil {
				callsign = trk.ADSBCallsign
			}
			ep.Input.AddLocation(pos, callsign)
		}
	}

	// zoom
	if z := mouse.Wheel[1]; z != 0 {
		r := ps.Range
		ps.Range += func() float32 {
			var amt float32 = 20

			if r < 4 {
				amt = .5
			} else if r == 4 {
				if z > 0 {
					amt = 2
				} else {
					amt = .5
				}
			} else if r < 20 {
				amt = 2
			} else if r == 20 {
				if z > 0 {
					amt = 20
				} else {
					amt = 2
				}
			}
			return amt * z / 10
		}()
		ps.Range = math.Clamp(ps.Range, .5, 2600)

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
	clear        bool
	responseArea []string
	feedbackArea []string
}

func (ep *ERAMPane) executeERAMCommand(ctx *panes.Context, cmdLine inputText) (CommandStatus, error) {
	original := strings.TrimSpace(cmdLine.String())

	// Extract all embedded locations from clicking while typing, along with
	// the callsigns (if any) of tracks under each click resolved at click time.
	var mousePositions []math.Point2LL
	var trackCallsigns []av.ADSBCallsign
	for _, ic := range cmdLine {
		if string(ic.char) == locationSymbol {
			mousePositions = append(mousePositions, ic.location)
			trackCallsigns = append(trackCallsigns, ic.trackCallsign)
		}
	}

	if status, err, handled := ep.tryExecuteUserCommand(ctx, original, mousePositions, trackCallsigns); handled {
		return status, err
	}

	// No registered command matched the input; surface a generic syntax error so the user gets
	// feedback instead of silent no-op (e.g. "QP <FLID> <SECTOR>" with the args swapped).
	if original != "" {
		return CommandStatus{}, ErrCommandFormat
	}
	return CommandStatus{}, nil
}

func (ep *ERAMPane) deleteFLightplan(ctx *panes.Context, trk sim.Track) {
	ctx.Client.DeleteFlightPlan(sim.ACID(trk.ADSBCallsign.String()), func(err error) {
		if err != nil {
			ep.displayError(err, ctx)
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
				ep.displayError(err, ctx)
			} else {
				ep.displayError(ErrCommandFormat, ctx)
			}
		}
	})
}

// Mainly used for ERAM assigned/ interm alts. May be used for actually changing routes.
func (ep *ERAMPane) modifyFlightPlan(ctx *panes.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) {
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
				ep.displayError(err, ctx)
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
	if spec.SecondaryScratchpad.IsSet && len(spec.SecondaryScratchpad.Value) > 0 {
		if _, err := strconv.Atoi(spec.SecondaryScratchpad.Value[1:]); err == nil {
			ep.runAircraftCommands(ctx, trk.ADSBCallsign, spec.SecondaryScratchpad.Value)
		}
	}
}

func (ep *ERAMPane) acceptHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcceptHandoff(acid,
		func(err error) { ep.displayError(err, ctx) })
}

func (ep *ERAMPane) recallHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.CancelHandoff(acid,
		func(err error) { ep.displayError(err, ctx) })
}

func (ep *ERAMPane) getQULines(ctx *panes.Context, acid sim.ACID, minutes int) {
	ctx.Client.SendRouteCoordinates(acid, minutes, func(err error) {
		if err != nil {
			ep.displayError(err, ctx)
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
			ep.displayError(err, ctx)
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
	control, err := ep.lookupControllerForID(ctx, controller)
	if err != nil {
		ep.displayError(err, ctx)
		return err
	}
	if control == nil {
		return ErrERAMIllegalPosition
	}

	ctx.Client.HandoffTrack(acid, control.PositionId(),
		func(err error) { ep.displayError(err, ctx) })

	return nil
}

func (ep *ERAMPane) pointOutTrack(ctx *panes.Context, trk *sim.Track, sector string) error {
	if trk.IsUnassociated() {
		return ErrERAMIllegalACID
	} else if control, err := ep.lookupControllerForID(ctx, sector); err != nil {
		return err
	} else if !control.ERAMFacility || control.FacilityIdentifier != ctx.UserController().FacilityIdentifier {
		// Can only point out to controllers in the same facility (just ERAM?)
		return ErrERAMIllegalPosition
	} else {
		acid := trk.FlightPlan.ACID
		ctx.Client.PointOut(acid, control.PositionId(),
			func(err error) {
				if err == nil {
					if trk, _ := ctx.Client.State.GetTrackByACID(acid); trk != nil {
						ep.feedbackArea.Success("ACCEPT\nINITIATE POINT OUT\n" +
							string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID)
					}
				} else {
					ep.displayError(err, ctx)
				}
			})
		return nil
	}
}

func (ep *ERAMPane) acknowledgePointOut(ctx *panes.Context, trk *sim.Track) error {
	if trk.IsUnassociated() {
		return ErrERAMIllegalACID
	}

	acid := trk.FlightPlan.ACID
	ctx.Client.AcknowledgePointOut(acid,
		func(err error) {
			if err == nil {
				if trk, _ := ctx.Client.State.GetTrackByACID(acid); trk != nil {
					ep.feedbackArea.Success("ACCEPT\nACKNOWLEDGE POINT OUT\n" +
						string(acid) + "/" + trk.FlightPlan.CID)
				}
			} else {
				ep.displayError(err, ctx)
			}
		})
	return nil
}

func (ep *ERAMPane) clearPointOutLock(trk *sim.Track) (CommandStatus, error) {
	state := ep.TrackState[trk.ADSBCallsign]
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	} else if !state.PointOutFDBLocked {
		return CommandStatus{}, ErrIllegalUserAction
	} else {
		state.PointOutFDBLocked = false
		state.EFDB = false
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "FORCED DATA BLK", fmt.Sprintf("%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)},
		}, nil
	}
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

func (ep *ERAMPane) lookupControllerForID(ctx *panes.Context, controller string) (*av.Controller, error) {
	// Look at the length of the controller string passed in. If it's one character, ERAM would have to find which controller it goes to.
	// That is not here yet, so return an error.
	for _, control := range ctx.Client.State.Controllers {
		switch len(controller) {
		case 1: // Cannot do anything with single characters in ERAM yet. TODO: fix pairs
			return nil, ErrERAMSectorNotActive
		case 2: // Handing off to other sectors within the same ARTCC
			if control.FacilityIdentifier == "" {
				if control.Position == controller {
					return control, nil
				}
			}
		case 3: // Handing off to a TRACON with a single char stars ID or another ARTCC
			// Get full STARS ID
			var prefix string
			for _, id := range ctx.Client.State.HandoffIDs {
				if id.SingleCharStarsID == string(controller[0]) {
					prefix = id.StarsID
					break
				}
				if id.Prefix == string(controller[0]) {
					prefix = id.Prefix
					break
				}
			}

			if control.FacilityIdentifier == prefix && control.Position == controller[1:] {
				return control, nil
			}
		case 4: // Handing off to a TRACON with a two char stars ID
			// Get full STARS ID
			var prefix string
			for _, id := range ctx.Client.State.HandoffIDs {
				if id.TwoCharStarsID == controller[:2] {
					prefix = id.StarsID
					break
				}
			}

			if control.FacilityIdentifier == prefix && control.Position == controller[2:] {
				return control, nil
			}
		case 5: // Handing off to a TRACON with a full STARS
			if controller == control.ERAMID() {
				return control, nil
			}
		default: // Invalid input
			return nil, ErrERAMSectorNotActive
		}
	}
	return nil, ErrERAMSectorNotActive
}
