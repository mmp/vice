package eram

import (
	"strings"
	"unicode"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
)

type CommandMode int

func (ep *ERAMPane) consumeMouseEvents(ctx *panes.Context, transforms radar.ScopeTransformations) {
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	if (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonSecondary] ||
		ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
		ctx.KeyboardFocus.Take(ep)
	}
	if mouse.Released[platform.MouseButtonPrimary] {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			mouseLatLong := transforms.LatLongFromWindowP(mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))

		}
	}
	ps := ep.currentPrefs()
	// try get closest track

	// pan

	if mouse.Dragging[platform.MouseButtonSecondary] {
		delta := mouse.DragDelta
		if delta[0] != 0 || delta[1] != 0 {
			deltaLL := transforms.LatLongFromWindowV(delta)
			ps.CurrentCenter = math.Sub2f(ps.CurrentCenter, deltaLL)
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

func (ep *ERAMPane) executeERAMCommand(ctx *panes.Context, cmd string) (status CommandStatus) {
	// TG will be the prefix for radio commands. TODO: Tab and semicolo (or comma) adds TG
	// Shift + tab locks TG
	var prefix string
	var original string = cmd
	if len(cmd) > 3 { //  trim a prefix
		prefix = cmd[:2]
		cmd = strings.TrimPrefix(cmd, prefix+" ")
	}

	switch prefix {
	case "QP": // J rings, point out
		fields := strings.Fields(cmd)
		if len(fields) == 1 { // ack fdb after po

		} else if len(fields) == 2 { // J ring or accept a pointout
			switch fields[0] {
			case "A": // Accept a point out
			case "J": // J ring
				trk, ok := ctx.Client.State.GetTrackByCID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				state.DisplayJRing = !state.DisplayJRing
				state.DisplayReducedJRing = false // clear reduced J ring
			case "T": // reduced J ring
				trk, ok := ctx.Client.State.GetTrackByCID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				if state.track.TransponderAltitude > 23000 {
					status.err = ErrERAMIllegalValue // maybe change this error to "not eligible?"
					return
				}
				state.DisplayJRing = false // clear J ring
				state.DisplayReducedJRing = !state.DisplayReducedJRing
			}
		} else if len(fields) == 3 { // initiate a po

		}
	case "QU": // direct, qu lines
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			clear(ep.aircraftFixCoordinates)
		} else if len(fields) == 2 && fields[0] == "/M" { // minutes will come later
			cid := fields[1]
			acid, err := ep.getACIDFromCID(ctx, cid)
			if err != nil {
				status.err = err
				return
			}
			ep.getQULines(ctx, acid)
		}
	case "QQ": // interim altitude
		// first field is the altitude, second is the CID.
		fields := strings.Split(cmd, " ")
		if len(fields) != 2 {
			status.err = ErrCommandFormat
			return
		}
		fp, err := parseOneFlightPlan("ALT_I", fields[0], nil) // should anything go in place of the nil?
		if err != nil {
			status.err = err
			return
		}
		ep.modifyFlightPlan(ctx, fields[1], fp)
	case "QZ": // Assigned, OTP, and block altitudes
		fields := strings.Split(cmd, " ")
		if len(fields) != 2 {
			status.err = ErrCommandFormat
			return
		}
		fp, err := parseOneFlightPlan("ALT_A", fields[0], nil) // should anything go in place of the nil?
		if err != nil {
			status.err = err
			return
		}
		ep.modifyFlightPlan(ctx, fields[1], fp)

	case "TG":
		// Special cases for non-control commands.
		if cmd == "" {
			return
		}
		if cmd == "P" {
			ctx.Client.ToggleSimPause()
			status.clear = true
			return
		}

		// Otherwise looks like an actual control instruction .
		suffix, cmds, ok := strings.Cut(cmd, " ")
		if !ok {
			suffix = string(ep.tgtGenDefaultCallsign(ctx))
			cmds = cmd
		}

		matching := radar.TracksFromACIDSuffix(ctx, suffix)
		if len(matching) > 1 {
			status.err = ErrERAMAmbiguousACID
			return
		}

		var trk *sim.Track
		if len(matching) == 1 {
			trk = matching[0]
		} else if len(matching) == 0 && ep.tgtGenDefaultCallsign(ctx) != "" {
			// If a valid callsign wasn't given, try the last callsign used.
			trk, _ = ctx.GetTrackByCallsign(ep.tgtGenDefaultCallsign(ctx))
			// But now we're going to run all of the given input as commands.
			cmds = cmd
		}

		if trk != nil {
			ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmds)
			status.clear = true
		} else {
			status.err = ErrERAMIllegalACID
		}
		return
	default: // Leader lines, accepting/ recalling HOs and whatever else goes in here
		fields := strings.Fields(original) // use the origional, uncut, command for this
		switch len(fields) {
		case 1:
			cmd := fields[0]
			if trk, ok := ctx.Client.State.GetTrackByCID(cmd); ok {
				// Accept handoff
				acid := sim.ACID(trk.ADSBCallsign.String())
				ep.acceptHandoff(ctx, acid)
				status.clear = true
			} else {
				status.err = ErrERAMIllegalACID
			}
		case 2:
			if len(fields[0]) == 1 && unicode.IsDigit(rune(original[0])) { // leader line & handoffs
				dir := ep.numberToLLDirection(ctx, original[0])
				// get callsign from fp
				trk, ok := ctx.Client.State.GetTrackByCID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				callsign := trk.ADSBCallsign
				dbType := ep.datablockType(ctx, *trk)
				if dbType != FullDatablock {
					if dir != math.CardinalOrdinalDirection(math.East) && dir != math.CardinalOrdinalDirection(math.West) {
						status.err = ErrERAMIllegalValue // get actual error
						return
					}
				}
				ep.TrackState[callsign].leaderLineDirection = &dir
			}
		}
	}
	return
}

func (ep *ERAMPane) displayError(err error, ctx *panes.Context) {
	if err != nil {
		ep.bigOutput = err.Error()
	}
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

func (ep *ERAMPane) runAircraftCommands(ctx *panes.Context, callsign av.ADSBCallsign, cmds string) {
	ep.targetGenLastCallsign = callsign

	ctx.Client.RunAircraftCommands(callsign, cmds,
		func(errStr string, remaining string) {
			if errStr != "" {

				if err := server.TryDecodeErrorString(errStr); err != nil {
					err = GetERAMError(err, ctx.Lg)
					ep.displayError(err, ctx)
				} else {
					ep.displayError(ErrCommandFormat, ctx)
				}
			}
		})
}

// Mainly used for ERAM assigned/ interm alts. May be used for actually changing routes.
func (ep *ERAMPane) modifyFlightPlan(ctx *panes.Context, cid string, spec sim.STARSFlightPlanSpecifier) {
	acid, err := ep.getACIDFromCID(ctx, cid)
	if err != nil {
		ep.displayError(err, ctx)
		return
	}
	ctx.Client.ModifyFlightPlan(acid, spec,
		func(err error) {
			if err != nil {
				ep.displayError(err, ctx)
			}
		})
}

func (ep *ERAMPane) getACIDFromCID(ctx *panes.Context, cid string) (sim.ACID, error) {
	trk, ok := ctx.Client.State.GetTrackByCID(cid)
	if !ok {
		return "", ErrERAMIllegalACID
	}
	return sim.ACID(trk.ADSBCallsign.String()), nil
}

func (ep *ERAMPane) acceptHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcceptHandoff(acid,
		func(err error) { ep.displayError(err, ctx) })
}

func (ep *ERAMPane) tgtGenDefaultCallsign(ctx *panes.Context) av.ADSBCallsign {
	if cs := ctx.Client.LastTTSCallsign(); cs != "" {
		// If TTS is active, return the last TTS transmitter.
		return cs
	}
	// Otherwise the most recent one the user selected.
	return ep.targetGenLastCallsign
}

func (ep *ERAMPane) getQULines(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.GetQULines(acid, func(err error) {
		if err != nil {
			ep.displayError(err, ctx)
		}
	})
}
