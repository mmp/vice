// eram/cmdops.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// ERAM operational commands

package eram

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/sim"
)

func registerOpsCommands() {
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
	// QU [MINUTES] [FLID] or QU [MINUTES][SLEW]: Display route for specified minutes
	// QU [FIX] [FLID] or QU [FIX][SLEW]: Direct to fix
	registerCommand(CommandModeNone, "QU", handleClearRouteDisplay)
	registerCommand(CommandModeNone, "QU [FLID]|QU [SLEW]", handleDefaultRouteDisplay)
	registerCommand(CommandModeNone, "QU /M [FLID]|QU /M[SLEW]", handleMaxRouteDisplay)
	registerCommand(CommandModeNone, "QU [MINUTES] [FLID]|QU [MINUTES][SLEW]", handleRouteDisplayMinutes)
	registerCommand(CommandModeNone, "QU [FIX] [FLID]|QU [FIX][SLEW]", handleDirectToFix)

	// QP - J rings
	// Keyboard: QP J [FLID] or QP T [FLID]
	// Clicked: QP J[SLEW] or QP T[SLEW]
	registerCommand(CommandModeNone, "QP J [FLID]|QP J[SLEW]", handleJRing)
	registerCommand(CommandModeNone, "QP T [FLID]|QP T[SLEW]", handleReducedJRing)

	// QF - Flight Plan Display
	registerCommand(CommandModeNone, "QF [FLID]|QF[SLEW]", handleFlightPlanReadout)

	// QS - HSF (Heading / Speed-Mach / Free-text) scratchpad handling
	// Keyboard:
	//   QS `<text> <FLID>    - set free text (backtick is clear-weather symbol)
	//   QS <heading> <FLID>  - set heading (1-4 chars; not validated)
	//   QS /<speedSpec> <FLID> - set speed or mach (see HSF_SPEED parser)
	//   QS */ <FLID>         - delete heading
	//   QS /* <FLID>         - delete speed/mach
	//   QS * <FLID>          - delete all HSF data
	//   QS <FLID>            - toggle display of HSF data
	// Clicked: replace <FLID> with [SLEW]
	registerCommand(CommandModeNone, "QS [HSF_TEXT] [FLID]|QS [HSF_TEXT][SLEW]", handleQSFreeText)
	registerCommand(CommandModeNone, "QS [HSF_SPEED] [FLID]|QS [HSF_SPEED][SLEW]", handleQSSpeed)
	registerCommand(CommandModeNone, "QS [HSF_HDG] [FLID]|QS [HSF_HDG][SLEW]", handleQSHeading)
	registerCommand(CommandModeNone, "QS */ [FLID]|QS */[SLEW]", handleQSDeleteHeading)
	registerCommand(CommandModeNone, "QS /* [FLID]|QS /*[SLEW]", handleQSDeleteSpeed)
	registerCommand(CommandModeNone, "QS * [FLID]|QS *[SLEW]", handleQSDeleteAll)
	registerCommand(CommandModeNone, "QS [FLID]|QS[SLEW]", handleQSToggleHSF)

	// MR - Map request (keyboard only)
	// MR: List available map groups
	// MR [GROUP]: Load map group
	registerCommand(CommandModeNone, "MR", handleMapRequestList)
	registerCommand(CommandModeNone, "MR [FIELD]", handleMapRequestLoad)

	// LA
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM]", handleLALocLoc)
	registerCommand(CommandModeNone, "LA [FLID] [LOC_SYM]", handleLATrkLoc)
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM] /[NUM]", handleLALocLocSpeed)
	registerCommand(CommandModeNone, "LA [FLID] [LOC_SYM] /[NUM]", handleLATrkLocSpeed)
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM] T/[NUM]", handleLALocLocTrueSpeed)
	registerCommand(CommandModeNone, "LA [FLID] [LOC_SYM] T/[NUM]", handleLATrkLocTrueSpeed)
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM] T", handleLALocLocTrue)
	registerCommand(CommandModeNone, "LA [FLID] [LOC_SYM] T", handleLATrkLocTrue)

	// LB
	registerCommand(CommandModeNone, "LB [FIX] [LOC_SYM]", handleLBFixLoc)
	registerCommand(CommandModeNone, "LB [FIX] [FLID]", handleLBFixTrk)
	registerCommand(CommandModeNone, "LB [FIX]/[NUM] [FLID]", handleLBFixSpeedTrk)

	// LF - CRR (Continuous Range Readout)
	// LF //FIX [LABEL]: Create new CRR group at fix location
	// LF //FIX [LABEL] [AIRCRAFT]: Create new CRR group with aircraft
	// LF {pos} [LABEL]: Create group at clicked position (click first, then label)
	// LF [LABEL] [FLID]: Toggle aircraft membership in existing group
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL] [ALL_TEXT]", handleCRRCreateWithAircraft)
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL]", handleCRRCreate)
	registerCommand(CommandModeNone, "LF [CRR_LOC]", handleCRRCreateAutoLabel)
	registerCommand(CommandModeNone, "LF [LOC_SYM] [CRR_LABEL]", handleCRRAddClicked) // LF {pos} LABEL - valid order
	registerCommand(CommandModeNone, "LF [CRR_LABEL] [LOC_SYM]", handleCRRWrongOrder) // LF LABEL {pos} - wrong order
	registerCommand(CommandModeNone, "LF [CRR_LABEL] [ALL_TEXT]", handleCRRToggleMembership)
	// Error handlers for incomplete LF commands
	registerCommand(CommandModeNone, "LF [CRR_LABEL]", handleCRRLabelOnly) // LF LABEL without click or aircraft
	registerCommand(CommandModeNone, "LF", handleCRREmpty)                 // LF alone

	// AR - Add airport to ALTIM SET list
	// AR [ICAO]: Add 4-letter ICAO code (e.g., AR KMCO)
	// AR [IATA]: Add 3-letter IATA code, auto-prepends K (e.g., AR MCO → KMCO)
	registerCommand(CommandModeNone, "AR [FIELD]", handleAltimAdd)

	// WR - Weather report commands
	// WR [ICAO]: Add 4-letter ICAO code to WX REPORT list (e.g., WR KMCO)
	// WR [IATA]: Add 3-letter IATA code, auto-prepends K (e.g., WR MCO → KMCO)
	// WR R [ICAO]: Display station's weather report in Response Area (e.g., WR R KMCO)
	// WR R [IATA]: Display with 3-letter IATA code (e.g., WR R MCO → KMCO)
	registerCommand(CommandModeNone, "WR R [FIELD]", handleWXReportDisplay)
	registerCommand(CommandModeNone, "WR [FIELD]", handleWXReportAdd)

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

	// Leader line length commands
	// /[0-3] [FLID] or /[0-3][SLEW]: Set leader line length (0=no line, 1=normal, 2=2x, 3=3x)
	registerCommand(CommandModeNone, "/[NUM] [FLID]|/[NUM][SLEW]", handleLeaderLineLength)

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
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nINTERIM ALT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

func handleClearInterimAltitude(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	fp := sim.FlightPlanSpecifier{}
	fp.InterimAlt.Set(0)

	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

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
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

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

// Either displays the route for 20 minutes ahead or clears the route display
func handleDefaultRouteDisplay(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	// Check if the route is currently displayed
	if _, ok := ep.aircraftFixCoordinates[trk.ADSBCallsign.String()]; ok {
		delete(ep.aircraftFixCoordinates, trk.ADSBCallsign.String())
		return CommandStatus{
			bigOutput: fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID), // TODO: Find correct message
		}
	}
	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), 20)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

func handleMaxRouteDisplay(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), -1)

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nROUTE DISPLAY\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}
}

func handleRouteDisplayMinutes(ep *ERAMPane, ctx *panes.Context, minutes int, trk *sim.Track) CommandStatus {
	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), minutes)

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

	if state.Track.TransponderAltitude > 23000 {
		return CommandStatus{}, NewERAMError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
	}

	state.DisplayJRing = false // clear J ring
	state.DisplayReducedJRing = !state.DisplayReducedJRing

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nREQ/DELETE DRI\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QF - Flight Plan Readout Handlers

func handleFlightPlanReadout(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	fp := trk.FlightPlan
	if fp == nil {
		return CommandStatus{
			err: fmt.Errorf("REJECT - NO FLIGHT PLAN\nFLIGHT PLAN READOUT\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID), // TODO: find the correct error message
		}
	}
	/*
		The flight plan readout contains the following elements:

		The Zulu time
		The aircraft's CID
		The aircraft's ID
		The track's owning sector ID (in parentheses)
		The aircraft's type and equipment suffix
		The aircraft's assigned beacon code
		The aircraft's filed cruise speed (not in NASFlightPlan so 0 for now)
		The aircraft's assigned altitude
		The aircraft's route
		The aircraft's flight plan remarks (not in NASFlightPlan so nothing for now)
	*/
	zTime := ctx.Client.State.SimTime.Format("1504")
	rte := strings.TrimPrefix(fp.Route, "/. ")
	rte = strings.ReplaceAll(rte, " ", ".")
	rte += fmt.Sprintf(".%v", fp.ArrivalAirport)
	fmt.Printf("rte: %v route: %v\n", rte, fp.Route)
	return CommandStatus{
		output: fmt.Sprintf("%v\n%v %v(%v) %v %v 0 %v %v", zTime, fp.CID, fp.ACID, fp.TrackingController, fp.AircraftType, fp.AssignedSquawk, fp.AssignedAltitude, rte),
	}
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
// LA - Range/Bearing Between Two Points

// formatRangeBearing computes distance/bearing and returns (detail string for secondary output).
func formatRangeBearing(from, to math.Point2LL, nmPerLon, magVar float32, trueBrg bool, speed float32) string {
	dist := math.NMDistance2LL(from, to)

	var magCorr float32
	brgLabel := "MAG"
	if trueBrg {
		brgLabel = "TRUE"
	} else {
		magCorr = magVar
	}
	brg := math.Heading2LL(from, to, nmPerLon, magCorr)

	s := fmt.Sprintf("RANGE * %.1f NM\nBEARING * %03.0f DEG %s", dist, brg, brgLabel)
	if speed > 0 {
		time := (dist / speed) * 60
		s += fmt.Sprintf("\nGS * %.0f  TIME * %.1f", speed, time)
	}
	return s
}

func handleLALocLoc(ep *ERAMPane, ctx *panes.Context, pos1 [2]float32, pos2 [2]float32) CommandStatus {
	from := math.Point2LL{pos1[0], pos1[1]}
	to := math.Point2LL{pos2[0], pos2[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation, false, 0))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLATrkLoc(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, pos [2]float32) CommandStatus {
	to := math.Point2LL{pos[0], pos[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(trk.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation, false, trk.Groundspeed))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLALocLocSpeed(ep *ERAMPane, ctx *panes.Context, pos1 [2]float32, pos2 [2]float32, speed int) CommandStatus {
	from := math.Point2LL{pos1[0], pos1[1]}
	to := math.Point2LL{pos2[0], pos2[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation, false, float32(speed)))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLATrkLocSpeed(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, pos [2]float32, speed int) CommandStatus {
	to := math.Point2LL{pos[0], pos[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(trk.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation, false, float32(speed)))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLALocLocTrueSpeed(ep *ERAMPane, ctx *panes.Context, pos1 [2]float32, pos2 [2]float32, speed int) CommandStatus {
	from := math.Point2LL{pos1[0], pos1[1]}
	to := math.Point2LL{pos2[0], pos2[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation, true, float32(speed)))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLATrkLocTrueSpeed(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, pos [2]float32, speed int) CommandStatus {
	to := math.Point2LL{pos[0], pos[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(trk.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation, true, float32(speed)))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLALocLocTrue(ep *ERAMPane, ctx *panes.Context, pos1 [2]float32, pos2 [2]float32) CommandStatus {
	from := math.Point2LL{pos1[0], pos1[1]}
	to := math.Point2LL{pos2[0], pos2[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation, true, 0))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

func handleLATrkLocTrue(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, pos [2]float32) CommandStatus {
	to := math.Point2LL{pos[0], pos[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(trk.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation, true, 0))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}
}

///////////////////////////////////////////////////////////////////////////
// LB - Range/Bearing From Fix

func handleLBFixLoc(ep *ERAMPane, ctx *panes.Context, fix string, pos [2]float32) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	to := math.Point2LL{pos[0], pos[1]}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(fixPos, to, ctx.NmPerLongitude, ctx.MagneticVariation, false, 0))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}, nil
}

func handleLBFixTrk(ep *ERAMPane, ctx *panes.Context, fix string, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(fixPos, trk.Location, ctx.NmPerLongitude, ctx.MagneticVariation, false, trk.Groundspeed))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}, nil
}

func handleLBFixSpeedTrk(ep *ERAMPane, ctx *panes.Context, fix string, speed int, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	ep.smallOutput.Set(ep.currentPrefs(), formatRangeBearing(fixPos, trk.Location, ctx.NmPerLongitude, ctx.MagneticVariation, false, float32(speed)))
	return CommandStatus{bigOutput: "ACCEPT\nRANGE/BEARING"}, nil
}

///////////////////////////////////////////////////////////////////////////
// LF - CRR (Continuous Range Readout) Handlers

func handleCRRCreateWithAircraft(ep *ERAMPane, ctx *panes.Context, loc CRRLocation, label string, aircraftStr string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// Check if group already exists
	if ep.CRRGroups == nil {
		ep.CRRGroups = make(map[string]*CRRGroup)
	}
	if _, ok := ep.CRRGroups[label]; ok {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP LABEL\n ALREADY EXISTS\nCONT RANGE\nLF %s %s", loc.Token, label)
	}

	// Create group
	g := &CRRGroup{
		Label:    label,
		Location: loc.Location,
		Color:    ep.currentPrefs().CRR.SelectedColor,
		Aircraft: make(map[av.ADSBCallsign]struct{}),
	}
	ep.CRRGroups[label] = g

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

// handleCRRAddClicked handles LF {pos} LABEL - position comes first, then label
func handleCRRAddClicked(ep *ERAMPane, ctx *panes.Context, pos [2]float32, label string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", locationSymbol, strings.ToUpper(label))
	}

	// pos is already lat/long from the [LOC_SYM] parser
	loc := math.Point2LL{pos[0], pos[1]}

	// Check if group exists
	g := ep.CRRGroups[label]
	if g == nil {
		// Create new group at clicked position
		if ep.CRRGroups == nil {
			ep.CRRGroups = make(map[string]*CRRGroup)
		}
		g = &CRRGroup{
			Label:    label,
			Location: loc,
			Color:    ep.currentPrefs().CRR.SelectedColor,
			Aircraft: make(map[av.ADSBCallsign]struct{}),
		}
		ep.CRRGroups[label] = g

		return CommandStatus{
			bigOutput: fmt.Sprintf("ACCEPT\nCRR GROUP %s CREATED", label),
		}, nil
	}

	// Group exists - find nearest track and add to group
	nearest := ep.closestTrackToLL(ctx, loc, 5)
	if nearest == nil {
		return CommandStatus{}, NewERAMError("REJECT - NO TB FLIGHT ID\nCAPTURE\nCONT RANGE\nLF %s %s", locationSymbol, strings.ToUpper(label))
	}

	g.Aircraft[nearest.ADSBCallsign] = struct{}{}

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nCRR UPDATED %s", g.Label),
	}, nil
}

// handleCRRWrongOrder handles LF LABEL {pos} - wrong order error
func handleCRRWrongOrder(label string, pos [2]float32) (CommandStatus, error) {
	return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", strings.ToUpper(label), locationSymbol)
}

func handleCRRToggleMembership(ep *ERAMPane, ctx *panes.Context, label string, aircraftStr string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, ErrCommandFormat
	}

	// Find existing group
	g := ep.CRRGroups[label]
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

func handleCRRLabelOnly(label string) (CommandStatus, error) {
	// LF LABEL without click or aircraft - group not found
	return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s", strings.ToUpper(label))
}

func handleCRREmpty() (CommandStatus, error) {
	// LF alone - message too short
	return CommandStatus{}, NewERAMError("REJECT - MESSAGE TOO SHORT\nCONT RANGE\nLF")
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
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

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
	state.EFDB = !state.EFDB
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
	direction := ep.numberToLLDirection(dir)
	callsign := trk.ADSBCallsign
	dbType := ep.datablockType(ctx, *trk)

	if dbType != FullDatablock {
		if direction != math.CardinalOrdinalDirection(math.East) && direction != math.CardinalOrdinalDirection(math.West) {
			return CommandStatus{}, ErrERAMIllegalValue
		}
	}

	ep.TrackState[callsign].LeaderLineDirection = &direction

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nOFFSET DATA BLK\n%s/%s", callsign, trk.FlightPlan.CID),
	}, nil
}

func (ep *ERAMPane) numberToLLDirection(cmd int) math.CardinalOrdinalDirection {
	if ep.FlipNumericKeypad {
		// Inverted layout: 1=NW (top-left on physical numpad)
		switch cmd {
		case 1:
			return math.NorthWest
		case 2:
			return math.North
		case 3:
			return math.NorthEast
		case 4:
			return math.West
		case 5:
			return math.NorthEast
		case 6:
			return math.East
		case 7:
			return math.SouthWest
		case 8:
			return math.South
		case 9:
			return math.SouthEast
		default:
			return math.East
		}
	} else {
		// Default layout: 1=SW (bottom-left on physical numpad)
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
}

///////////////////////////////////////////////////////////////////////////
// Leader Line Length Handlers

func handleLeaderLineLength(ep *ERAMPane, ctx *panes.Context, length int, trk *sim.Track) CommandStatus {
	// Validate length is 0-3
	if length < 0 || length > 3 {
		return CommandStatus{
			err: fmt.Errorf("REJECT - INVALID\nLDR LENGTH\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
		}
	}

	// Update track state
	ep.TrackState[trk.ADSBCallsign].LeaderLineLength = length

	return CommandStatus{
		bigOutput: fmt.Sprintf("ACCEPT\nOFFSET DATA BLK\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID),
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

///////////////////////////////////////////////////////////////////////////
// QS - HSF (Heading / Speed-Mach / Free-text) Handlers

func qsFDBDataAcceptMsg(trk *sim.Track) string {
	if trk == nil || trk.FlightPlan == nil {
		return ""
	}
	return fmt.Sprintf("ACCEPT\nFDB DATA\n%s/%s", trk.ADSBCallsign.String(), trk.FlightPlan.CID)
}

func handleQSToggleHSF(ep *ERAMPane, trk *sim.Track) CommandStatus {
	if trk == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}
	state := ep.TrackState[trk.ADSBCallsign]
	if state == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}
	state.HSFHide = !state.HSFHide
	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSDeleteHeading(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSDeleteSpeed(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}
	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSDeleteAll(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}
	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)
	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSHeading(ep *ERAMPane, ctx *panes.Context, heading string, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(heading)
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSSpeed(ep *ERAMPane, ctx *panes.Context, speed string, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}

	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set(speed)
	if isQSFreeText(trk.FlightPlan.Scratchpad) {
		fp.Scratchpad.Set("")
	}
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func handleQSFreeText(ep *ERAMPane, ctx *panes.Context, freeText string, trk *sim.Track) CommandStatus {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{err: ErrERAMIllegalACID}
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(freeText)
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk.FlightPlan.CID, fp)

	return CommandStatus{clear: true, bigOutput: qsFDBDataAcceptMsg(trk)}
}

func isQSFreeText(s string) bool {
	return strings.HasPrefix(s, circleClear)
}

///////////////////////////////////////////////////////////////////////////
// AR - ALTIM SET Add Airport Handler

func handleAltimAdd(ep *ERAMPane, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - AR - MISSING AIRPORT")
	}

	// Convert 3-letter IATA to ICAO by prepending K (US convention).
	icao := airport
	if len(airport) == 3 {
		icao = "K" + airport
	}

	// Toggle: if already in the list, remove it.
	for i, existing := range ep.AltimSetAirports {
		if existing == icao {
			ep.AltimSetAirports = append(ep.AltimSetAirports[:i], ep.AltimSetAirports[i+1:]...)
			// Adjust scroll offset if needed after removal
			ps := ep.currentPrefs()
			maxOffset := len(ep.AltimSetAirports) - ps.AltimSet.Lines
			if maxOffset < 0 {
				maxOffset = 0
			}
			if ep.altimSetScrollOffset > maxOffset {
				ep.altimSetScrollOffset = maxOffset
			}
			return CommandStatus{bigOutput: "ACCEPT\nALTIMETER REQ"}, nil
		}
	}

	ep.AltimSetAirports = append([]string{icao}, ep.AltimSetAirports...)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.AltimSet.Visible = true

	return CommandStatus{bigOutput: "ACCEPT\nALTIMETER REQ"}, nil
}

// WR - WX REPORT Add Airport Handler

func handleWXReportAdd(ep *ERAMPane, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - WR - MISSING AIRPORT")
	}

	// Convert 3-letter IATA to ICAO by prepending K (US convention).
	icao := airport
	if len(airport) == 3 {
		icao = "K" + airport
	}

	// Toggle: if already in the list, remove it.
	for i, existing := range ep.WXReportStations {
		if existing == icao {
			ep.WXReportStations = append(ep.WXReportStations[:i], ep.WXReportStations[i+1:]...)
			// Adjust scroll offset if needed after removal
			ps := ep.currentPrefs()
			maxOffset := len(ep.WXReportStations) - ps.WX.Lines
			if maxOffset < 0 {
				maxOffset = 0
			}
			if ep.wxScrollOffset > maxOffset {
				ep.wxScrollOffset = maxOffset
			}
			return CommandStatus{bigOutput: "ACCEPT\nWEATHER STAT REQ"}, nil
		}
	}

	ep.WXReportStations = append([]string{icao}, ep.WXReportStations...)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.WX.Visible = true

	return CommandStatus{bigOutput: "ACCEPT\nWEATHER STAT REQ"}, nil
}

// WR R - WX REPORT Display (show METAR in Response Area)

func handleWXReportDisplay(ep *ERAMPane, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - WR R - MISSING AIRPORT")
	}

	// Convert 3-letter IATA to ICAO by prepending K (US convention).
	icao := airport
	if len(airport) == 3 {
		icao = "K" + airport
	}

	// Trigger fetch if not already done or stale
	if !ep.wxFetching[icao] {
		lastFetch, fetched := ep.wxLastFetch[icao]
		if !fetched || time.Since(lastFetch) > wxRefreshInterval {
			ep.wxFetching[icao] = true
			wxFetchMETAR(icao, ep.wxFetchCh)
		}
	}

	// Look for the METAR result
	result, hasResult := ep.wxMetars[icao]

	// Determine what to display
	var displayText string
	if hasResult {
		if result.err != nil {
			displayText = fmt.Sprintf("ERROR\n%s\nWEATHER REQUEST\n%s", icao, result.err.Error())
		} else {
			displayText = result.rawText
		}
	} else if ep.wxFetching[icao] {
		displayText = fmt.Sprintf("LOADING\n%s", icao)
	} else {
		displayText = fmt.Sprintf("NO DATA\n%s", icao)
	}

	ps := ep.currentPrefs()
	ep.smallOutput.Set(ps, displayText)

	return CommandStatus{}, nil
}
