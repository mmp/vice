package eram

import (
	"fmt"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
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
	if mouse.Released[platform.MouseButtonTertiary] {
		// Try execute a clicked command on the closest track.
		trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms)
		if trk != nil {
			status := ep.executeERAMClickedCommand(ctx, ep.Input, trk)
			ep.Input.Clear()
			if status.err != nil {
				ep.bigOutput.displayError(ep.currentPrefs(), status.err)
			} else if status.bigOutput != "" {
				ep.bigOutput.displaySuccess(ep.currentPrefs(), status.bigOutput)
			}
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

	if mouse.Clicked[platform.MouseButtonPrimary] {
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
	// TG will be the prefix for radio commands. TODO: Tab and semicolo (or comma) adds TG
	// Shift + tab locks TG
	original := cmdLine.String()

	fieldsFull := strings.Fields(original)
	if len(fieldsFull) == 0 {
		return
	}
	prefix := fieldsFull[0]
	cmd := strings.Join(fieldsFull[1:], " ")
	if strings.HasPrefix(original, "//") {
		cmd = strings.TrimPrefix(original, "//")
		cmd = strings.TrimSpace(cmd)
		prefix = "//"
	}

	switch prefix {
	case ".DRAWROUTE":
		ep.commandMode = CommandModeDrawRoute
		ep.drawRoutePoints = nil
		ps := ep.currentPrefs()
		ep.smallOutput.Set(ps, "DRAWROUTE")
		status.clear = true
		return
	case "MR": // Map request
		fields := strings.Fields(cmd)
		if len(fields) > 1 {
			status.err = ErrERAMMessageTooLong
			return
		}
		vmf, err := ep.getVideoMapLibrary(ctx.Client.State.State, ctx.Client)
		if err != nil {
			status.err = err
			return
		}
		switch len(fields) {
		case 0:
			// Get all map names and print it out in the big output
			visibleNames := []string{}
			for groups := range vmf.ERAMMapGroups {
				visibleNames = append(visibleNames, groups)
			}
			status.output = fmt.Sprintf("AVAILABLE GEOMAPS: %s", strings.Join(visibleNames, " "))
			return
		case 1:
			groupName := fields[0]

			maps, ok := vmf.ERAMMapGroups[groupName]
			if !ok {
				status.err = ErrERAMMapUnavailable
				return
			}
			ps := ep.currentPrefs()
			ps.VideoMapGroup = groupName

			// Get rid of all visible maps
			ps.VideoMapVisible = make(map[string]interface{})

			ep.videoMapLabel = fmt.Sprintf("%s\n%s", maps.LabelLine1, maps.LabelLine2)
			ep.allVideoMaps = radar.BuildERAMClientVideoMaps(maps.Maps)
			status.bigOutput = fmt.Sprintf("ACCEPT\nMAP REQUEST\n%v", ps.VideoMapGroup)
			for _, eramMap := range maps.Maps {
				if ps.VideoMapBrightness[eramMap.BcgName] == 0 { // If the brightness is not set, default it to 12
					ps.VideoMapBrightness[eramMap.BcgName] = 12
				}
			}
		}
	case "QP": // J rings, point out
		fields := strings.Fields(cmd)
		if len(fields) == 1 { // ack fdb after po

		} else if len(fields) == 2 { // J ring or accept a pointout
			switch fields[0] {
			case "A": // Accept a point out
			case "J": // J ring
				trk, ok := ctx.Client.State.GetTrackByFLID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				state.DisplayJRing = !state.DisplayJRing
				state.DisplayReducedJRing = false // clear reduced J ring
				status.bigOutput = fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			case "T": // reduced J ring
				trk, ok := ctx.Client.State.GetTrackByFLID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				if state.track.TransponderAltitude > 23000 {
					status.err = NewERAMError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
					return
				}
				state.DisplayJRing = false // clear J ring
				state.DisplayReducedJRing = !state.DisplayReducedJRing
				status.bigOutput = fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			default: // init a pointout
				// <sector ID><FLID>
				if len(fields) != 2 {
					status.err = ErrCommandFormat
					return
				}
			}
		}
	case "QU": // direct, qu lines
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			clear(ep.aircraftFixCoordinates)
		} else if len(fields) == 2 && fields[0] == "/M" { // minutes will come later
			cid := fields[1]
			trk, ok := ctx.Client.State.GetTrackByFLID(cid)
			if !ok {
				status.err = ErrERAMIllegalACID
				return
			}
			status.bigOutput = fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign))
		} else if unicode.IsDigit(rune(fields[0][0])) && len(fields) == 2 { // TODO:For minutes to a fix
			status.err = ErrCommandFormat
			return
		} else if len(fields) == 2 { // Direct a fix
			// <fix> <FLID>
			fix := fields[0]
			flid := fields[1]

			trk, ok := ctx.Client.State.GetTrackByFLID(flid)
			if !ok {
				status.err = ErrERAMIllegalACID
				return
			}
			status.bigOutput = fmt.Sprintf("ACCEPT\nREROUTE\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			ep.flightPlanDirect(ctx, sim.ACID(trk.ADSBCallsign), fix)

		}
	case "QQ": // interim altitude
		// first field is the altitude, second is the CID.
		fields := strings.Split(cmd, " ")
		var trk *sim.Track
		var fp sim.FlightPlanSpecifier
		if len(fields) == 1 {
			var ok bool
			trk, ok = ctx.Client.State.GetTrackByFLID(fields[0])
			if !ok {
				status.err = ErrERAMIllegalACID
				return
			}
			fp = sim.FlightPlanSpecifier{}
			fp.InterimAlt.Set(0)
			ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
			status.bigOutput = fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		} else if len(fields) == 2 {
			var err error
			var ok bool
			fp, err = parseOneFlightPlan("ALT_I", fields[0], nil) // should anything go in place of the nil?
			if err != nil {
				status.err = err
				return
			}
			trk, ok = ctx.Client.State.GetTrackByFLID(fields[1])
			if !ok {
				status.err = ErrERAMIllegalACID
				return
			}
		}

		ep.modifyFlightPlan(ctx, string(trk.ADSBCallsign), fp)
		status.bigOutput = fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	case "QX": // drop track
		fields := strings.Fields(cmd)
		if len(fields) != 1 {
			status.err = ErrCommandFormat
			return
		}
		trk, ok := ctx.Client.State.GetTrackByFLID(fields[0])
		if !ok {
			status.err = ErrERAMIllegalACID
			return
		}
		if !ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
			status.err = ErrERAMIllegalACID // change error to NO CONTROL
			return
		}
		ep.deleteFLightplan(ctx, *trk)
		status.bigOutput = fmt.Sprintf("ACCEPT\nDROP TRACK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	case "QZ": // Assigned, OTP, and block altitudes
		fields := strings.Split(cmd, " ")
		if len(fields) != 2 {
			status.err = ErrCommandFormat
			return
		}
		trk, ok := ctx.Client.State.GetTrackByFLID(fields[1])
		if !ok {
			status.err = ErrERAMIllegalACID
			return
		}
		fp, err := parseOneFlightPlan("ALT_A", fields[0], nil) // should anything go in place of the nil?
		if err != nil {
			status.err = err
			return
		}
		status.bigOutput = fmt.Sprintf("ACCEPT\nASSIGNED ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		ep.modifyFlightPlan(ctx, fields[1], fp)
	case "LF":
		// CRR
		// LF <location> <label> <acid>
		// LF <label> <acid>
		// Location may be entered with mouse or keyboard
		originalFields := strings.Fields(original)
		if len(originalFields) < 2 {
			status.err = ErrCommandFormat
			return
		}
		// Try location from embedded click
		loc, haveLoc := tryExtractLocation(cmdLine)
		args := strings.Fields(cmd)
		var label string
		var aircraftStr string

		clickedLoc := haveLoc
		if haveLoc {
			if len(args) == 0 {
				status.err = NewERAMError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\n%s", strings.TrimSpace(original))
				return
			}
			// If first token is the location symbol, skip it and use the next token as label
			if args[0] == locationSymbol {
				if len(args) >= 2 {
					label = strings.ToUpper(args[1])
					if len(args) >= 3 {
						aircraftStr = strings.Join(args[2:], " ")
					}
					// keep clickedLoc = true
					goto haveClickedLabel
				}
				status.err = NewERAMError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\n%s", strings.TrimSpace(original))
				return
			}
			if strings.HasPrefix(args[0], "//") {
				// Typed location : LF //FIX <label> <aircraft>
				clickedLoc = false
				if len(args) >= 2 {
					label = strings.ToUpper(args[1])
					if len(args) >= 3 {
						aircraftStr = strings.Join(args[2:], " ")
					}
				}
			} else {
				// Clicked location: LF <label> <aircraft>
				label = strings.ToUpper(args[0])
				if len(args) >= 2 {
					aircraftStr = strings.Join(args[1:], " ")
				}
			}
		haveClickedLabel:
		} else if len(args) >= 1 && strings.HasPrefix(args[0], "//") {
			if l, ok := parseCRRLocation(ctx, args[0]); ok {
				loc = l
				haveLoc = true
				clickedLoc = false
				if len(args) >= 2 {
					label = strings.ToUpper(args[1])
				}
				if len(args) >= 3 {
					aircraftStr = args[2]
				}
			}
		}
		if haveLoc {
			// Label is mandatory for clicked locations and FRDs; only auto-derive for  fixes
			if label == "" {
				if len(args) >= 1 && strings.HasPrefix(args[0], "//") {
					token := strings.TrimPrefix(args[0], "//")
					token = strings.ToUpper(token)
					// Only auto-derive label if it's a  fix
					if _, ok := ctx.Client.State.Locate(token); ok && validCRRLabel(token) {
						label = token
					}
				}
			}
			if !validCRRLabel(label) {
				msg := "REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\n%s"
				if label == "" {
					msg = "REJECT - MESSAGE TOO SHORT\nCONT RANGE\n%s"
				}
				status.err = NewERAMError(msg, strings.TrimSpace(original))
				return
			}

			// Clicked locations: must assign to existing group near clicked position; do not create
			if clickedLoc {
				g := ep.crrGroups[label]
				if g == nil {
					status.err = NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\n%s", toUpper(original))
					return
				}
				nearest := ep.closestTrackToLL(ctx, loc, 5)
				if nearest == nil {
					status.err = NewERAMError("REJECT - NO TB FLIGHT ID\nCAPTURE\nCONT RANGE\n%s", toUpper(original))
					return
				}
				g.Aircraft[nearest.ADSBCallsign] = struct{}{}
				status.bigOutput = fmt.Sprintf("ACCEPT\nCRR UPDATED %s", g.Label)
				return
			}

			// Typed locations: create new group (with validation above)
			if ep.crrGroups == nil {
				ep.crrGroups = make(map[string]*CRRGroup)
			}
			// Create group; if it already exists, reject
			g, ok := ep.crrGroups[label]
			if ok {
				status.err = NewERAMError("REJECT - CRR - GROUP LABEL\n ALREADY EXISTS\nCONT RANGE\n%s", toUpper(original))
				return
			}
			g = &CRRGroup{
				Label:    label,
				Location: loc,
				Color:    ep.currentPrefs().CRR.SelectedColor,
				Aircraft: make(map[av.ADSBCallsign]struct{}),
			}
			ep.crrGroups[label] = g
			status.bigOutput = fmt.Sprintf("ACCEPT\nCRR GROUP %s CREATED", label)
			if aircraftStr != "" {
				for _, cs := range resolveAircraftTokens(ctx, aircraftStr) {
					g.Aircraft[cs] = struct{}{}
				}
			}
			return
		}
		// No location: LF <label> <acids> toggles membership
		if len(args) >= 2 {
			label = strings.ToUpper(args[0])
			if !validCRRLabel(label) {
				status.err = ErrCommandFormat
				return
			}
			g := ep.crrGroups[label]
			if g == nil {
				status.err = ErrCommandFormat
				return
			}
			for _, cs := range resolveAircraftTokens(ctx, args[1]) {
				if _, ok := g.Aircraft[cs]; ok {
					delete(g.Aircraft, cs)
				} else {
					g.Aircraft[cs] = struct{}{}
				}
			}
			status.bigOutput = fmt.Sprintf("ACCEPT\nCRR UPDATED %s", label)
			return
		}
		status.err = ErrCommandFormat
		return
	case "//":
		cmd = strings.TrimPrefix(cmd, "//") // In case user types //FLID. The space is also acceptable
		trk, ok := ctx.Client.State.GetTrackByFLID(cmd)
		if !ok {
			status.err = ErrERAMIllegalACID
			return
		}
		state := ep.TrackState[trk.ADSBCallsign]
		state.DisplayVCI = !state.DisplayVCI
		status.bigOutput = fmt.Sprintf("ACCEPT\nTOGGLE ON-FREQUENCY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		return
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

		matching := ctx.TracksFromACIDSuffix(suffix)
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
	default: // Leader lines,  HOs and whatever else goes in here
		fields := strings.Fields(original) // use the origional, uncut, command for this
		switch len(fields) {
		case 1:
			cmd := fields[0]
			if trk, ok := ctx.Client.State.GetTrackByFLID(cmd); ok && ctx.IsHandoffToUser(trk) {
				// Accept handoff
				acid := sim.ACID(trk.ADSBCallsign.String())
				status.bigOutput = fmt.Sprintf("ACCEPT\nACCEPT HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				ep.acceptHandoff(ctx, acid)
			} else if ok && ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && trk.FlightPlan.HandoffController != "" { // Recall handoff
				acid := sim.ACID(trk.ADSBCallsign.String())
				status.bigOutput = fmt.Sprintf("ACCEPT\nRECALL HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				ep.recallHandoff(ctx, acid)
			} else { // Change to LDB or FDB
				trk, ok := ctx.Client.State.GetTrackByFLID(cmd)
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				if !trk.IsAssociated() {
					status.err = ErrCommandFormat
					return
				}
				// Toggle FDB display (only for non-owned tracks)
				if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
					status.err = NewERAMError("USER ACTION NOT ALLOWED ON A\nCONTROLLER FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				state.eFDB = !state.eFDB
				status.bigOutput = fmt.Sprintf("ACCEPT\nFORCED DATA BLK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				state.DisplayJRing = false
				state.DisplayReducedJRing = false
			}
		case 2: // leader line & handoffs
			if len(fields[0]) == 1 && unicode.IsDigit(rune(original[0])) { // leader line
				dir := ep.numberToLLDirection(ctx, original[0])
				// get callsign from fp
				trk, ok := ctx.Client.State.GetTrackByFLID(fields[1])
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
				status.bigOutput = fmt.Sprintf("ACCEPT\nOFFSET DATA BLK\n%s/%s", callsign, trk.FlightPlan.CID)
			} else { // handoffs
				trk, ok := ctx.Client.State.GetTrackByFLID(fields[1])
				if !ok {
					status.err = ErrERAMIllegalACID
					return
				}
				acid := sim.ACID(trk.ADSBCallsign)
				sector := fields[0]
				err := ep.handoffTrack(ctx, acid, sector)
				if err != nil {
					status.err = err
					return
				}
				status.bigOutput = fmt.Sprintf("ACCEPT\nINITIATE HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			}
		}
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

	ctx.Client.RunAircraftCommands(callsign, cmds, false, false,
		func(errStr string, remaining string) {
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
		if alt > int(state.track.TransponderAltitude) {
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

func (ep *ERAMPane) getQULines(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.SendRouteCoordinates(acid, func(err error) {
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

func (ep *ERAMPane) executeERAMClickedCommand(ctx *panes.Context, cmdLine inputText, trk *sim.Track) (status CommandStatus) {
	if trk == nil {
		status.err = ErrERAMIllegalACID
		return
	}

	original := cmdLine.String()

	fieldsFull := strings.Fields(original)
	var prefix, cmd string
	if len(fieldsFull) > 0 {
		prefix = fieldsFull[0]
		cmd = strings.Join(fieldsFull[1:], " ")
	}

	switch prefix {
	case "QP":
		fields := strings.Fields(cmd)
		if len(fields) == 1 {
			switch fields[0] {
			case "J":
				state := ep.TrackState[trk.ADSBCallsign]
				state.DisplayJRing = !state.DisplayJRing
				state.DisplayReducedJRing = false
				status.bigOutput = fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			case "T":
				state := ep.TrackState[trk.ADSBCallsign]
				if state.track.TransponderAltitude > 23000 {
					status.err = NewERAMError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
					return
				}
				state.DisplayJRing = false
				state.DisplayReducedJRing = !state.DisplayReducedJRing
				status.bigOutput = fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			default:
				status.err = ErrCommandFormat
			}
		}
	case "QU":
		fields := strings.Fields(cmd)
		if len(fields) == 1 {
			if fields[0] == "/M" {
				status.bigOutput = fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID) // if an error is returned from here it should be replaced by the callback
				ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign))
			} else {
				status.bigOutput = fmt.Sprintf("ACCEPT\nREROUTE\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				ep.flightPlanDirect(ctx, sim.ACID(trk.ADSBCallsign), fields[0])
				// TODO: Draw QU lunes after this CMD
			}
		}
	case "QQ": // TODO: Check for proper controller. Same format as the other dont have control error
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			fp := sim.FlightPlanSpecifier{}
			fp.InterimAlt.Set(0)
			ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
			status.bigOutput = fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		} else if len(fields) == 1 {
			fp, err := parseOneFlightPlan("ALT_I", fields[0], nil)
			if err != nil {
				status.err = err
				return
			}
			ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
			status.bigOutput = fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		} else {
			status.err = ErrCommandFormat
		}
	case "QX":
		if !ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
			status.err = ErrERAMIllegalACID
			// REJECT - NOT YOUR CONTROL \n DROP TRACK \n <cmd>
			// There is also a seperate one if the tracking controller is a center:
			// REJECT - SECTOR <##> HAS \n CONTROL \n DROP TRACK \n <cmd>
			return
		}
		if cmd != "" {
			status.err = ErrCommandFormat
			return
			// REJECT - <cid> FORMAT \n DROP TRACK \n <cmd>
		}
		ep.deleteFLightplan(ctx, *trk)
		status.bigOutput = fmt.Sprintf("ACCEPT\nDROP TRACK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	case "QZ":
		fields := strings.Fields(cmd)
		if len(fields) == 1 {
			fp, err := parseOneFlightPlan("ALT_A", fields[0], nil)
			if err != nil {
				status.err = err
				return
			}
			ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
			status.bigOutput = fmt.Sprintf("ACCEPT\nASSIGNED ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
		} else {
			status.err = ErrCommandFormat
		}
	case "//":
		state := ep.TrackState[trk.ADSBCallsign]
		state.DisplayVCI = !state.DisplayVCI
		status.bigOutput = fmt.Sprintf("ACCEPT\nTOGGLE ON-FREQUENCY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	case "TG":
		if cmd == "" {
			return
		}
		if cmd == "P" {
			ctx.Client.ToggleSimPause()
			status.clear = true
			return
		}
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
		status.clear = true
	default:
		fields := strings.Fields(original)
		switch len(fields) {
		case 0:
			if ctx.IsHandoffToUser(trk) {
				acid := sim.ACID(trk.ADSBCallsign.String())
				status.bigOutput = fmt.Sprintf("ACCEPT\nACCEPT HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				ep.acceptHandoff(ctx, acid)
				return
			} else if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && trk.FlightPlan.HandoffController != "" {
				acid := sim.ACID(trk.ADSBCallsign.String())
				status.bigOutput = fmt.Sprintf("ACCEPT\nRECALL HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				ep.recallHandoff(ctx, acid)
				return
			} else {
				if !trk.IsAssociated() {
					status.err = ErrCommandFormat
					return
				}
				if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
					status.err = NewERAMError("USER ACTION NOT ALLOWED ON A\nCONTROLLED FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
					return
				}
				state := ep.TrackState[trk.ADSBCallsign]
				state.eFDB = !state.eFDB
				status.bigOutput = fmt.Sprintf("ACCEPT\nFORCED DATA BLK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
				state.DisplayJRing = false
				state.DisplayReducedJRing = false
				return
			}
		case 1:
			if len(fields[0]) == 1 && unicode.IsDigit(rune(fields[0][0])) {
				dir := ep.numberToLLDirection(ctx, fields[0][0])
				callsign := trk.ADSBCallsign
				dbType := ep.datablockType(ctx, *trk)
				if dbType != FullDatablock {
					if dir != math.CardinalOrdinalDirection(math.East) && dir != math.CardinalOrdinalDirection(math.West) {
						status.err = ErrERAMIllegalValue
						return
					}
				}
				ep.TrackState[callsign].leaderLineDirection = &dir
				status.bigOutput = fmt.Sprintf("ACCEPT\nOFFSET DATA BLK\n%s/%s", callsign, trk.FlightPlan.CID)
			} else {
				acid := sim.ACID(trk.ADSBCallsign)
				err := ep.handoffTrack(ctx, acid, fields[0])
				if err != nil {
					status.err = err
					return
				}
				status.bigOutput = fmt.Sprintf("ACCEPT\nINITIATE HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
			}
		}
	}
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
