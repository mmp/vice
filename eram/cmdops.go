// eram/cmdops.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// ERAM operational commands

package eram

import (
	"fmt"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/sim"
)

func init() {
	// QQ - Interim altitude
	// Keyboard: QQ [ALT] [FLID] or QQ [FLID] to clear
	// Clicked: QQ [ALT][SLEW] or QQ[SLEW] to clear
	registerCommand(CommandModeNone, "QQ [ERAM_ALT_I] [FLID]|QQ [ERAM_ALT_I][SLEW]", handleInterimAltitude)
	registerCommand(CommandModeNone, "QQ [FLID]|QQ[SLEW]", handleClearInterimAltitude)

	// QZ - Assigned altitude
	// Keyboard: QZ [ALT] [FLID]
	// Clicked: QZ [ALT][SLEW]
	registerCommand(CommandModeNone, "QZ [ERAM_ALT_A] [FLID]|QZ [ERAM_ALT_A][SLEW]", handleAssignedAltitude)

	// QX - Drop track
	// Keyboard: QX [FLID]
	// Clicked: QX[SLEW]
	registerCommand(CommandModeNone, "QX [FLID]|QX[SLEW]", handleDropTrack)

	// QU - Direct to fix / Route display
	// QU: Clear all route displays
	// QU /M [FLID] or QU /M[SLEW]: Display route
	// QU [FIX] [FLID] or QU [FIX][SLEW]: Direct to fix
	registerCommand(CommandModeNone, "QU", handleClearRouteDisplay)
	registerCommand(CommandModeNone, "QU /M [FLID]|QU /M[SLEW]", handleRouteDisplay)
	registerCommand(CommandModeNone, "QU [FIX] [FLID]|QU [FIX][SLEW]", handleDirectToFix)

	// QP - J rings
	// Keyboard: QP J [FLID] or QP T [FLID]
	// Clicked: QP J[SLEW] or QP T[SLEW]
	registerCommand(CommandModeNone, "QP J [FLID]|QP J[SLEW]", handleJRing)
	registerCommand(CommandModeNone, "QP T [FLID]|QP T[SLEW]", handleReducedJRing)

	// MR - Map request (keyboard only)
	// MR: List available map groups
	// MR [GROUP]: Load map group
	registerCommand(CommandModeNone, "MR", handleMapRequestList)
	registerCommand(CommandModeNone, "MR [FIELD]", handleMapRequestLoad)

	// LF - CRR (Continuous Range Readout)
	// LF //FIX [LABEL]: Create new CRR group at fix location
	// LF //FIX [LABEL] [AIRCRAFT]: Create new CRR group with aircraft
	// LF [LABEL][SLEW]: Add nearest aircraft to existing group at clicked position
	// LF [LABEL] [FLID]: Toggle aircraft membership in existing group
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL] [ALL_TEXT]", handleCRRCreateWithAircraft)
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL]", handleCRRCreate)
	registerCommand(CommandModeNone, "LF [CRR_LOC]", handleCRRCreateAutoLabel)
	registerCommand(CommandModeNone, "LF [CRR_LABEL][SLEW]", handleCRRAddClicked)
	registerCommand(CommandModeNone, "LF [CRR_LABEL] [ALL_TEXT]", handleCRRToggleMembership)

	// // - Toggle VCI (on-frequency indicator)
	// Keyboard: //[FLID] or // [FLID]
	// Clicked: //[SLEW]
	registerCommand(CommandModeNone, "//[FLID]|// [FLID]|//[SLEW]", handleToggleVCI)

	// TG - Target gen commands
	// TG P: Toggle pause
	// TG [cmds]: Run commands on last used aircraft
	// TG [cmds][SLEW]: Run commands on clicked track
	registerCommand(CommandModeNone, "TG P", handleTogglePause)
	registerCommand(CommandModeNone, "TG [ALL_TEXT]", handleTargetGen)
	registerCommand(CommandModeNone, "TG [ALL_TEXT][SLEW]", handleTargetGenClicked)
	registerCommand(CommandModeNone, "TG[SLEW]", handleTargetGenEmptyClicked)

	// Default commands (no prefix)
	// [FLID] or [SLEW]: Accept handoff / recall handoff / toggle FDB
	// [SECTOR_ID] [FLID] or [SECTOR_ID][SLEW]: Initiate handoff
	// [#] [FLID] or [#][SLEW]: Leader line direction
	registerCommand(CommandModeNone, "[FLID]|[SLEW]", handleDefaultTrack)
	registerCommand(CommandModeNone, "[SECTOR_ID] [FLID]|[SECTOR_ID][SLEW]", handleInitiateHandoff)
	registerCommand(CommandModeNone, "[#] [FLID]|[#][SLEW]", handleLeaderLine)

	// .DRAWROUTE - Custom command for drawing routes
	registerCommand(CommandModeNone, ".DRAWROUTE", handleDrawRouteMode)

	// Commands available in draw route mode
	registerCommand(CommandModeDrawRoute, "[POS]", handleDrawRoutePoint)
}

///////////////////////////////////////////////////////////////////////////
// QQ - Interim Altitude Handlers

func handleInterimAltitude(ep *ERAMPane, ctx *panes.Context, alt InterimAltitude, trk *sim.Track) (CommandStatus, error) {
	fp := sim.FlightPlanSpecifier{}
	fp.InterimAlt.Set(alt.Altitude)
	if alt.Type != "" {
		fp.InterimType.Set(alt.Type)
	}

	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

func handleClearInterimAltitude(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	fp := sim.FlightPlanSpecifier{}
	fp.InterimAlt.Set(0)

	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

///////////////////////////////////////////////////////////////////////////
// QZ - Assigned Altitude Handler

func handleAssignedAltitude(ep *ERAMPane, ctx *panes.Context, alt int, trk *sim.Track) CommandStatus {
	fp := sim.FlightPlanSpecifier{}
	fp.AssignedAltitude.Set(alt)

	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nASSIGNED ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

///////////////////////////////////////////////////////////////////////////
// QX - Drop Track Handler

func handleDropTrack(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if !ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		return CommandStatus{}, ErrERAMIllegalACID // TODO: proper "NO CONTROL" error
	}

	ep.deleteFLightplan(ctx, *trk)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nDROP TRACK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QU - Direct / Route Display Handlers

func handleClearRouteDisplay(ep *ERAMPane) {
	clear(ep.aircraftFixCoordinates)
}

func handleRouteDisplay(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign))

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

func handleDirectToFix(ep *ERAMPane, ctx *panes.Context, fix string, trk *sim.Track) CommandStatus {
	ep.flightPlanDirect(ctx, sim.ACID(trk.ADSBCallsign), fix)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nREROUTE\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

///////////////////////////////////////////////////////////////////////////
// QP - J Ring Handlers

func handleJRing(ep *ERAMPane, trk *sim.Track) CommandStatus {
	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayJRing = !state.DisplayJRing
	state.DisplayReducedJRing = false // clear reduced J ring

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

func handleReducedJRing(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
	state := ep.TrackState[trk.ADSBCallsign]

	if state.track.TransponderAltitude > 23000 {
		return CommandStatus{}, NewERAMError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
	}

	state.DisplayJRing = false // clear J ring
	state.DisplayReducedJRing = !state.DisplayReducedJRing

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// MR - Map Request Handlers

func handleMapRequestList(ep *ERAMPane, ctx *panes.Context) (CommandStatus, error) {
	vmf, err := ep.getVideoMapLibrary(ctx.Client.State, ctx.Client)
	if err != nil {
		return CommandStatus{}, err
	}

	var visibleNames []string
	for groups := range vmf.ERAMMapGroups {
		visibleNames = append(visibleNames, groups)
	}

	return CommandStatus{
		output: fmt.Sprintf("AVAILABLE GEOMAPS: %s", strings.Join(visibleNames, " ")),
	}, nil
}

func handleMapRequestLoad(ep *ERAMPane, ctx *panes.Context, groupName string) (CommandStatus, error) {
	vmf, err := ep.getVideoMapLibrary(ctx.Client.State, ctx.Client)
	if err != nil {
		return CommandStatus{}, err
	}

	maps, ok := vmf.ERAMMapGroups[groupName]
	if !ok {
		return CommandStatus{}, ErrERAMMapUnavailable
	}

	ps := ep.currentPrefs()
	ps.VideoMapGroup = groupName

	// Get rid of all visible maps
	ps.VideoMapVisible = make(map[string]interface{})

	ep.videoMapLabel = fmt.Sprintf("%s\n%s", maps.LabelLine1, maps.LabelLine2)
	ep.allVideoMaps = radar.BuildERAMClientVideoMaps(maps.Maps)

	for _, eramMap := range maps.Maps {
		if ps.VideoMapBrightness[eramMap.BcgName] == 0 {
			ps.VideoMapBrightness[eramMap.BcgName] = 12
		}
	}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nMAP REQUEST\n%v", ps.VideoMapGroup),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LF - CRR (Continuous Range Readout) Handlers

func handleCRRCreateWithAircraft(ep *ERAMPane, ctx *panes.Context, loc CRRLocation, label string, aircraftStr string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// Check if group already exists
	if ep.crrGroups == nil {
		ep.crrGroups = make(map[string]*CRRGroup)
	}
	if _, ok := ep.crrGroups[label]; ok {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP LABEL\n ALREADY EXISTS\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// Create group
	g := &CRRGroup{
		Label:    label,
		Location: loc.Location,
		Color:    ep.currentPrefs().CRR.SelectedColor,
		Aircraft: make(map[av.ADSBCallsign]struct{}),
	}
	ep.crrGroups[label] = g

	// Add aircraft if specified
	if aircraftStr != "" {
		for _, cs := range resolveAircraftTokens(ctx, aircraftStr) {
			g.Aircraft[cs] = struct{}{}
		}
	}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nCRR GROUP %s CREATED", label),
	}, nil
}

func handleCRRCreate(ep *ERAMPane, ctx *panes.Context, loc CRRLocation, label string) (CommandStatus, error) {
	return handleCRRCreateWithAircraft(ep, ctx, loc, label, "")
}

func handleCRRCreateAutoLabel(ep *ERAMPane, ctx *panes.Context, loc CRRLocation) (CommandStatus, error) {
	// Auto-derive label from fix name if it's a valid fix
	label := loc.Token
	// Check if it's a valid fix name that can be used as a label
	if _, ok := ctx.Client.State.Locate(label); ok && validCRRLabel(label) {
		return handleCRRCreate(ep, ctx, loc, label)
	}
	return CommandStatus{}, NewERAMError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\nLF //%s", loc.Token)
}

func handleCRRAddClicked(ep *ERAMPane, ctx *panes.Context, label string, pos [2]float32, transforms radar.ScopeTransformations) (CommandStatus, error) {
	// Find existing group
	g := ep.crrGroups[label]
	if g == nil {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s", strings.ToUpper(label))
	}

	// Convert window position to lat/long
	loc := transforms.LatLongFromWindowP(pos)

	// Find nearest track to clicked position
	nearest := ep.closestTrackToLL(ctx, loc, 5)
	if nearest == nil {
		return CommandStatus{}, NewERAMError("REJECT - NO TB FLIGHT ID\nCAPTURE\nCONT RANGE\nLF %s", strings.ToUpper(label))
	}

	g.Aircraft[nearest.ADSBCallsign] = struct{}{}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nCRR UPDATED %s", g.Label),
	}, nil
}

func handleCRRToggleMembership(ep *ERAMPane, ctx *panes.Context, label string, aircraftStr string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, ErrCommandFormat
	}

	// Find existing group
	g := ep.crrGroups[label]
	if g == nil {
		return CommandStatus{}, ErrCommandFormat
	}

	// Toggle membership for each aircraft
	for _, cs := range resolveAircraftTokens(ctx, aircraftStr) {
		if _, ok := g.Aircraft[cs]; ok {
			delete(g.Aircraft, cs)
		} else {
			g.Aircraft[cs] = struct{}{}
		}
	}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nCRR UPDATED %s", g.Label),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// // - Toggle VCI Handler

func handleToggleVCI(ep *ERAMPane, trk *sim.Track) CommandStatus {
	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayVCI = !state.DisplayVCI

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nTOGGLE ON-FREQUENCY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

///////////////////////////////////////////////////////////////////////////
// TG - Target Gen Handlers

func handleTogglePause(ctx *panes.Context) {
	ctx.Client.ToggleSimPause()
}

func handleTargetGen(ep *ERAMPane, ctx *panes.Context, cmd string) (CommandStatus, error) {
	if cmd == "" {
		return CommandStatus{}, nil
	}

	// Parse callsign suffix and commands
	suffix, cmds, ok := strings.Cut(cmd, " ")
	if !ok {
		suffix = string(ep.tgtGenDefaultCallsign(ctx))
		cmds = cmd
	}

	matching := ctx.TracksFromACIDSuffix(suffix)
	if len(matching) > 1 {
		return CommandStatus{}, ErrERAMAmbiguousACID
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
		return CommandStatus{clear: true}, nil
	}

	return CommandStatus{}, ErrERAMIllegalACID
}

func handleTargetGenClicked(ep *ERAMPane, ctx *panes.Context, cmd string, trk *sim.Track) CommandStatus {
	if cmd != "" {
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
	}
	return CommandStatus{clear: true}
}

func handleTargetGenEmptyClicked(ep *ERAMPane, trk *sim.Track) CommandStatus {
	// Just clicking on a track in TG mode - do nothing special
	return CommandStatus{}
}

///////////////////////////////////////////////////////////////////////////
// Default Command Handlers (keyboard and clicked)

func handleDefaultTrack(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if ctx.IsHandoffToUser(trk) {
		// Accept handoff
		acid := sim.ACID(trk.ADSBCallsign.String())
		ep.acceptHandoff(ctx, acid)
		return CommandStatus{
			bigOutput: fmt.Sprintf("ACCEPT\nACCEPT HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
		}, nil
	}

	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && trk.FlightPlan.HandoffController != "" {
		// Recall handoff
		acid := sim.ACID(trk.ADSBCallsign.String())
		ep.recallHandoff(ctx, acid)
		return CommandStatus{
			bigOutput: fmt.Sprintf("ACCEPT\nRECALL HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
		}, nil
	}

	// Toggle FDB display
	if !trk.IsAssociated() {
		return CommandStatus{}, ErrCommandFormat
	}

	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		return CommandStatus{}, NewERAMError("USER ACTION NOT ALLOWED ON A\nCONTROLLER FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.eFDB = !state.eFDB
	state.DisplayJRing = false
	state.DisplayReducedJRing = false

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nFORCED DATA BLK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

func handleInitiateHandoff(ep *ERAMPane, ctx *panes.Context, sector string, trk *sim.Track) (CommandStatus, error) {
	acid := sim.ACID(trk.ADSBCallsign)
	err := ep.handoffTrack(ctx, acid, sector)
	if err != nil {
		return CommandStatus{}, err
	}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nINITIATE HANDOFF\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

func handleLeaderLine(ep *ERAMPane, ctx *panes.Context, dir int, trk *sim.Track) (CommandStatus, error) {
	direction := numberToLLDirection(dir)
	callsign := trk.ADSBCallsign
	dbType := ep.datablockType(ctx, *trk)

	if dbType != FullDatablock {
		if direction != math.CardinalOrdinalDirection(math.East) && direction != math.CardinalOrdinalDirection(math.West) {
			return CommandStatus{}, ErrERAMIllegalValue
		}
	}

	ep.TrackState[callsign].leaderLineDirection = &direction

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nOFFSET DATA BLK\n%s/%s", callsign, trk.FlightPlan.CID),
	}, nil
}

func numberToLLDirection(cmd int) math.CardinalOrdinalDirection {
	switch cmd {
	case 1:
		return math.SouthWest
	case 2:
		return math.South
	case 3:
		return math.SouthEast
	case 4:
		return math.West
	case 5:
		return math.NorthEast
	case 6:
		return math.East
	case 7:
		return math.NorthWest
	case 8:
		return math.North
	case 9:
		return math.NorthEast
	default:
		return math.East
	}
}

///////////////////////////////////////////////////////////////////////////
// .DRAWROUTE - Custom command

func handleDrawRouteMode(ep *ERAMPane, ctx *panes.Context) CommandStatus {
	ep.commandMode = CommandModeDrawRoute
	ep.drawRoutePoints = nil
	ps := ep.currentPrefs()
	ep.smallOutput.Set(ps, "DRAWROUTE")
	return CommandStatus{clear: true}
}

func handleDrawRoutePoint(ep *ERAMPane, ctx *panes.Context, pos [2]float32) CommandStatus {
	// Convert window position to lat/long
	posLL := math.Point2LL{pos[0], pos[1]}
	ep.drawRoutePoints = append(ep.drawRoutePoints, posLL)

	var cb []string
	for _, p := range ep.drawRoutePoints {
		cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
	}
	ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))

	ps := ep.currentPrefs()
	ep.smallOutput.Set(ps, fmt.Sprintf("DRAWROUTE: %d POINTS", len(ep.drawRoutePoints)))

	return CommandStatus{}
}

///////////////////////////////////////////////////////////////////////////
// Helper function to check if a character is a digit

func isDigit(r rune) bool {
	return unicode.IsDigit(r)
}
