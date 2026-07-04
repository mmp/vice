// eram/cmdops.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// ERAM operational commands

package eram

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func registerOpsCommands() {
	// QQ - Interim altitude
	// QQ [ALT] [TRACK]: Set interim altitude
	// QQ [TRACK]: Clear interim altitude
	registerCommand(CommandModeNone, "QQ [ERAM_ALT_I] [TRACK]", handleInterimAltitude)
	registerCommand(CommandModeNone, "QQ [TRACK]", handleClearInterimAltitude)

	// QZ - Assigned altitude
	// QZ [ALT] [TRACK]: Set assigned altitude
	registerCommand(CommandModeNone, "QZ [ERAM_ALT_A] [TRACK]", handleAssignedAltitude)

	// QX - Drop track
	// QX [TRACK]
	registerCommand(CommandModeNone, "QX [TRACK]", handleDropTrack)

	// QU - Direct to fix / Route display
	// QU: Clear all route displays
	// QU [TRACK]: Toggle 20-minute route display
	// QU /M [TRACK]: Display full route
	// QU [MINUTES] [TRACK]: Display route for specified minutes
	// QU [FIX] [TRACK]: Direct to fix
	registerCommand(CommandModeNone, "QU", handleClearRouteDisplay)
	registerCommand(CommandModeNone, "QU [TRACK]", handleDefaultRouteDisplay)
	registerCommand(CommandModeNone, "QU /M [TRACK]", handleMaxRouteDisplay)
	registerCommand(CommandModeNone, "QU [MINUTES] [TRACK]", handleRouteDisplayMinutes)
	registerCommand(CommandModeNone, "QU [FIX] [TRACK]", handleDirectToFix)

	// QP - J rings
	// QP J [TRACK]: Toggle J ring
	// QP T [TRACK]: Toggle reduced J ring
	registerCommand(CommandModeNone, "QP J [TRACK]", handleJRing)
	registerCommand(CommandModeNone, "QP T [TRACK]", handleReducedJRing)

	// QP - Point outs
	// QP [SECTOR_ID] [TRACK]: Initiate point out
	// QP A [TRACK]: Acknowledge point out
	// QP [TRACK]: Clear the post-point-out FDB lock (FDB -> LDB)
	registerCommand(CommandModeNone, "QP A [TRACK]",
		func(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) error {
			return ep.acknowledgePointOut(ctx, trk)
		})
	registerCommand(CommandModeNone, "QP [SECTOR_ID] [TRACK]",
		func(ep *ERAMPane, ctx *panes.Context, sector string, trk *sim.Track) error {
			return ep.pointOutTrack(ctx, trk, sector)
		})
	registerCommand(CommandModeNone, "QP [TRACK]",
		func(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
			return ep.clearPointOutLock(trk)
		})

	// QL - Quicklook
	registerCommand(CommandModeNone, "QL [SECTOR_ID_LIST]", handleToggleQuicklook)
	registerCommand(CommandModeNone, "QL", handleDisableAllQuicklook)

	// QB - beacon code view codes
	registerCommand(CommandModeNone, "QB [BCN_LIST]", handleBeaconCodeViewList)

	// QF - Flight Plan Display
	registerCommand(CommandModeNone, "QF [TRACK]", handleFlightPlanReadout)

	// QS - HSF (Heading / Speed-Mach / Free-text) scratchpad handling
	//   QS `<text> [TRACK]      - set free text (backtick is clear-weather symbol)
	//   QS <heading> [TRACK]    - set heading (1-4 chars; not validated)
	//   QS /<speedSpec> [TRACK] - set speed or mach (see HSF_SPEED parser)
	//   QS */ [TRACK]           - delete heading
	//   QS /* [TRACK]           - delete speed/mach
	//   QS * [TRACK]            - delete all HSF data
	//   QS [TRACK]              - toggle display of HSF data
	registerCommand(CommandModeNone, "QS [HSF_TEXT] [TRACK]", handleQSFreeText)
	registerCommand(CommandModeNone, "QS [HSF_SPEED] [TRACK]", handleQSSpeed)
	registerCommand(CommandModeNone, "QS [HSF_HDG] [TRACK]", handleQSHeading)
	registerCommand(CommandModeNone, "QS */ [TRACK]", handleQSDeleteHeading)
	registerCommand(CommandModeNone, "QS /* [TRACK]", handleQSDeleteSpeed)
	registerCommand(CommandModeNone, "QS * [TRACK]", handleQSDeleteAll)
	registerCommand(CommandModeNone, "QS [TRACK]", handleQSToggleHSF)

	// MR - Map request (keyboard only)
	// MR: List available map groups
	// MR [GROUP]: Load map group
	registerCommand(CommandModeNone, "MR", handleMapRequestList)
	registerCommand(CommandModeNone, "MR [FIELD]", handleMapRequestLoad)

	// LA - track range - distance between points
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM]",
		func(ep *ERAMPane, ctx *panes.Context, from math.Point2LL, to math.Point2LL) CommandStatus {
			return handleLALocLoc(ep, ctx, from, to, laOptions{})
		})
	registerCommand(CommandModeNone, "LA [TRACK] [LOC_SYM]",
		func(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, to math.Point2LL) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk, to, laOptions{})
		})
	registerCommand(CommandModeNone, "LA [TRACK] [TRACK]",
		func(ep *ERAMPane, ctx *panes.Context, trk1, trk2 *sim.Track) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk1, trk2.Location, laOptions{})
		})
	registerCommand(CommandModeNone, "LA [LOC_SYM] [LOC_SYM] [LA_OPTS]", handleLALocLoc)
	registerCommand(CommandModeNone, "LA [TRACK] [LOC_SYM] [LA_OPTS]", handleLATrkLoc)
	registerCommand(CommandModeNone, "LA [TRACK] [TRACK] [LA_OPTS]",
		func(ep *ERAMPane, ctx *panes.Context, trk1, trk2 *sim.Track, opts laOptions) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk1, trk2.Location, opts)
		})

	// LB - track range - distance between fix and track/location
	registerCommand(CommandModeNone, "LB [FIX] [LOC_SYM]", handleLBFixLoc)
	registerCommand(CommandModeNone, "LB [FIX] [TRACK]", handleLBFixTrk)
	registerCommand(CommandModeNone, "LB [FIX]/[NUM] [TRACK]", handleLBFixSpeedTrk)

	// LC - speed adjustment to position a track over a fix at a specified UTC time
	registerCommand(CommandModeNone, "LC [FIX]/[NUM] [TRACK]", handleLCFixTimeTrk)

	// LF - CRR (Continuous Range Readout)
	// LF //FIX [LABEL]: Create new CRR group at fix location
	// LF //FIX [LABEL] [AIRCRAFT...]: Create new CRR group with up to 4 aircraft
	// LF {pos} [LABEL]: Create group at clicked position (click first, then label)
	// LF [LABEL] [AIRCRAFT...]: Toggle membership for up to 4 aircraft
	// Aircraft tokens may be CIDs, ADSB callsigns, or beacon codes separated by
	// '/' and/or spaces, and/or clicks on tracks.
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL] [TRACK_LIST]", handleCRRCreateWithAircraft)
	registerCommand(CommandModeNone, "LF [CRR_LOC] [CRR_LABEL]", handleCRRCreate)
	registerCommand(CommandModeNone, "LF [CRR_LOC]", handleCRRCreateAutoLabel)
	registerCommand(CommandModeNone, "LF [LOC_SYM] [CRR_LABEL]", handleCRRAddClicked) // LF {pos} LABEL - valid order
	registerCommand(CommandModeNone, "LF [CRR_LABEL] [LOC_SYM]", handleCRRWrongOrder) // LF LABEL {pos} - wrong order
	registerCommand(CommandModeNone, "LF [CRR_LABEL] [TRACK_LIST]", handleCRRToggleMembership)
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
	// //[TRACK] or // [TRACK]
	registerCommand(CommandModeNone, "//[TRACK]|// [TRACK]", handleToggleVCI)

	// TG - Target gen commands
	// TG P: Toggle pause
	// TG [cmds]: Run commands on last used aircraft
	// TG [cmds][TRACK]: Run commands on clicked track
	registerCommand(CommandModeNone, "TG P", handleTogglePause)
	registerCommand(CommandModeNone, "TG [ALL_TEXT]", handleTargetGen)
	registerCommand(CommandModeNone, "TG [ALL_TEXT][TRACK]", handleTargetGenClicked)
	registerCommand(CommandModeNone, "TG[TRACK]", handleTargetGenEmptyClicked)

	// Default commands (no prefix)
	// [TRACK]: Accept handoff / recall handoff / toggle FDB
	// [SECTOR_ID] [TRACK]: Initiate handoff
	// [1-9] [TRACK]: Leader line direction
	registerCommand(CommandModeNone, "[TRACK]", handleDefaultTrack)
	registerCommand(CommandModeNone, "[SECTOR_ID] [TRACK]", handleInitiateHandoff)
	registerCommand(CommandModeNone, "[#] [TRACK]", handleLeaderLinePosition)

	// Leader line length commands
	// /[0-3] [TRACK]: Set leader line length (0=no line, 1=normal, 2=2x, 3=3x)
	// [1-9]/[0-3] [TRACK]: Set leader line position and length
	registerCommand(CommandModeNone, "/[NUM] [TRACK]", handleLeaderLineLength)
	registerCommand(CommandModeNone, "[NUM]/[NUM] [TRACK]", handleLeaderLinePositionAndLength)

	// .DRAWROUTE - Custom command for drawing routes
	registerCommand(CommandModeNone, ".DRAWROUTE", handleDrawRouteMode)

	// Commands available in draw route mode
	registerCommand(CommandModeDrawRoute, "[POS]", handleDrawRoutePoint)
}

///////////////////////////////////////////////////////////////////////////
// QQ - Interim Altitude Handlers

func handleInterimAltitude(ep *ERAMPane, ctx *panes.Context, alt InterimAltitude, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	fp := sim.FlightPlanSpecifier{}
	fp.InterimAlt.Set(alt.Altitude)
	fp.InterimType.Set(alt.Type)

	ep.modifyFlightPlan(ctx, trk, fp)
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "INTERIM ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleClearInterimAltitude(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	fp := sim.FlightPlanSpecifier{}
	fp.InterimAlt.Set(0)
	fp.InterimType.Set(sim.InterimNormal)

	ep.modifyFlightPlan(ctx, trk, fp)
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "INTERIM ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QZ - Assigned Altitude Handler

func handleAssignedAltitude(ep *ERAMPane, ctx *panes.Context, alt int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	fp := sim.FlightPlanSpecifier{}
	fp.AssignedAltitude.Set(alt)

	ep.modifyFlightPlan(ctx, trk, fp)
	state := ep.TrackState[trk.ADSBCallsign]
	if state != nil {
		state.ReachedAltitude = false
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ASSIGNED ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QX - Drop Track Handler

func handleDropTrack(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	if !ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		return CommandStatus{}, ErrERAMIllegalACID // TODO: proper "NO CONTROL" error
	}

	ep.deleteFLightplan(ctx, *trk)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "DROP TRACK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QU - Direct / Route Display Handlers

func handleClearRouteDisplay(ep *ERAMPane) {
	clear(ep.aircraftFixCoordinates)
}

// Either displays the route for 20 minutes ahead or clears the route display
func handleDefaultRouteDisplay(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	// Check if the route is currently displayed
	if _, ok := ep.aircraftFixCoordinates[trk.ADSBCallsign.String()]; ok {
		delete(ep.aircraftFixCoordinates, trk.ADSBCallsign.String())
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "ROUTE DISPLAY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID}, // TODO: Find correct message
		}, nil
	}
	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), 20)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ROUTE DISPLAY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleMaxRouteDisplay(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), -1)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ROUTE DISPLAY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleRouteDisplayMinutes(ep *ERAMPane, ctx *panes.Context, minutes int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	ep.getQULines(ctx, sim.ACID(trk.ADSBCallsign), minutes)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ROUTE DISPLAY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleDirectToFix(ep *ERAMPane, ctx *panes.Context, fix string, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	ep.flightPlanDirect(ctx, sim.ACID(trk.ADSBCallsign), fix)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "REROUTE", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QP - J Ring Handlers

func handleJRing(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayJRing = !state.DisplayJRing
	state.DisplayReducedJRing = false // clear reduced J ring

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "REQ/DELETE DRI", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleReducedJRing(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]

	if state.Track.TransponderAltitude > 23000 {
		return CommandStatus{}, NewERAMError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
	}

	state.DisplayJRing = false // clear J ring
	state.DisplayReducedJRing = !state.DisplayReducedJRing

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "REQ/DELETE DRI", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QL - Quicklook

func handleToggleQuicklook(ep *ERAMPane, sectors []string) {
	for _, s := range sectors {
		if _, ok := ep.QuickLookSectors[s]; ok {
			delete(ep.QuickLookSectors, s)
		} else {
			ep.QuickLookSectors[s] = struct{}{}
		}
	}
}

func handleDisableAllQuicklook(ep *ERAMPane) {
	clear(ep.QuickLookSectors)
}

///////////////////////////////////////////////////////////////////////////
// QB - beacon code view lists

func handleBeaconCodeViewList(ep *ERAMPane, codes []av.Squawk) error {
	for _, c := range codes {
		if idx := slices.Index(ep.AddedBeaconCodes, c); idx != -1 {
			ep.AddedBeaconCodes = slices.Delete(ep.AddedBeaconCodes, idx, idx+1)
		} else {
			ep.AddedBeaconCodes = append(ep.AddedBeaconCodes, c)
		}
	}

	// TODO: should there be an error if any of the codes are not owned by us?

	return nil
}

///////////////////////////////////////////////////////////////////////////
// QF - Flight Plan Readout Handlers

func handleFlightPlanReadout(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	fp := trk.FlightPlan
	if fp == nil {
		// TODO: find the correct error message
		return CommandStatus{}, fmt.Errorf("REJECT - NO FLIGHT PLAN\nFLIGHT PLAN READOUT\n%s", trk.ADSBCallsign)
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
		The aircraft's assigned altitude (in 100s of feet)
		The aircraft's route
		The aircraft's flight plan remarks (not in NASFlightPlan so nothing for now)
	*/
	zTime := ctx.Client.State.SimTime.Format("1504")
	rte := strings.TrimPrefix(fp.Route, "/. ")
	rte = strings.ReplaceAll(rte, " ", ".")
	rte += "." + fp.ArrivalAirport
	return CommandStatus{
		responseArea: []string{
			zTime,
			fmt.Sprintf("%v %v(%v) %v %v 0 %v %v", fp.CID, fp.ACID, fp.TrackingController,
				fp.AircraftType, fp.AssignedSquawk, fp.AssignedAltitude/100, rte),
		},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// MR - Map Request Handlers

func handleMapRequestList(ep *ERAMPane, ctx *panes.Context) (CommandStatus, error) {
	vmf, err := ctx.Client.LoadVideoMapLibrary(ctx.Client.State.ControllerVideoMapFile)
	if err != nil {
		return CommandStatus{}, err
	}

	visibleNames := slices.Collect(maps.Keys(vmf.ERAMMapGroups))
	return CommandStatus{
		responseArea: []string{fmt.Sprintf("AVAILABLE GEOMAPS: %s", strings.Join(visibleNames, " "))},
	}, nil
}

func handleMapRequestLoad(ep *ERAMPane, ctx *panes.Context, groupName string) (CommandStatus, error) {
	vmf, err := ctx.Client.LoadVideoMapLibrary(ctx.Client.State.ControllerVideoMapFile)
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

	ep.videoMapLabel = maps.LabelLine1 + "\n" + maps.LabelLine2
	ep.allVideoMaps = maps.Maps
	ep.bcgNames = maps.BCGNames

	for _, name := range maps.BCGNames {
		if name == "" {
			continue
		}
		if ps.VideoMapBrightness[name] == 0 {
			ps.VideoMapBrightness[name] = 12
		}
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "MAP REQUEST", ps.VideoMapGroup},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LA - Range/Bearing Between Two Points

// formatRangeBearing computes distance/bearing and returns (detail string for secondary output).
// fromLabel is the third line (e.g. "FROM 1ST TB ENTRY" for LA, "FROM TB TO FIX KLAX" for LB).
func formatRangeBearing(from, to math.Point2LL, nmPerLon, magVar float32, trueBrg bool, speed float32, fromLabel string) []string {
	dist := math.NMDistance2LL(from, to)

	brgLabel := util.Select(trueBrg, "TRUE", "MAG")
	trueBearing := math.Heading2LL(from, to, nmPerLon)
	brg := util.Select(trueBrg, float32(trueBearing), float32(math.TrueToMagnetic(trueBearing, magVar)))

	var lines []string
	lines = append(lines, fmt.Sprintf("RANGE * %.1f NM", dist))
	lines = append(lines, fmt.Sprintf("BEARING * %03.0f DEG %s", brg, brgLabel))
	if fromLabel != "" {
		lines = append(lines, fromLabel)
	}
	if speed > 0 {
		minutes := (dist / speed) * 60
		lines = append(lines, fmt.Sprintf("AT %.0f KTS %.0f MIN", speed, minutes))
	}
	return lines
}

func handleLALocLoc(ep *ERAMPane, ctx *panes.Context, from math.Point2LL, to math.Point2LL, opts laOptions) CommandStatus {
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation,
			opts.True, float32(opts.Speed), "FROM 1ST TB ENTRY"),
	}
}

func handleLATrkLoc(ep *ERAMPane, ctx *panes.Context, trk *sim.Track, to math.Point2LL, opts laOptions) CommandStatus {
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(trk.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation,
			opts.True, util.Select(opts.Speed > 0, float32(opts.Speed), trk.Groundspeed), "FROM 1ST TB ENTRY"),
	}
}

///////////////////////////////////////////////////////////////////////////
// LB - Range/Bearing From Fix

func handleLBFixLoc(ep *ERAMPane, ctx *panes.Context, fix string, from math.Point2LL) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(from, fixPos, ctx.NmPerLongitude, ctx.MagneticVariation,
			false, 0, "FROM TB TO FIX "+fix),
	}, nil

}

func handleLBFixTrk(ep *ERAMPane, ctx *panes.Context, fix string, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(trk.Location, fixPos, ctx.NmPerLongitude, ctx.MagneticVariation,
			false, trk.Groundspeed, "FROM TB TO FIX "+fix),
	}, nil
}

func handleLBFixSpeedTrk(ep *ERAMPane, ctx *panes.Context, fix string, speed int, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(trk.Location, fixPos, ctx.NmPerLongitude, ctx.MagneticVariation,
			false, float32(speed), "FROM TB TO FIX "+fix),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LC - Speed adjustment to arrive over a fix at a specified UTC time (HHMM)

func handleLCFixTimeTrk(ep *ERAMPane, ctx *panes.Context, fix string, hhmm int, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}

	hh, mm := hhmm/100, hhmm%100
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return CommandStatus{}, ErrERAMIllegalValue
	}

	now := ctx.Client.State.SimTime.UTC()
	target := time.Date(now.Time().Year(), now.Time().Month(), now.Time().Day(), hh, mm, 0, 0, time.UTC)
	if !target.After(now.Time()) {
		target = target.Add(24 * time.Hour)
	}
	dtHours := target.Sub(now.Time()).Hours()
	if dtHours <= 0 {
		return CommandStatus{}, ErrERAMIllegalValue
	}

	dist := math.NMDistance2LL(trk.Location, fixPos)
	reqGS := int(math.Round(dist / float32(dtHours)))

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: []string{fmt.Sprintf("%s AT %02d%02dZ+%dK", fix, hh, mm, reqGS),
			fmt.Sprintf("CURRENT SPEED %.0fK", trk.Groundspeed)},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LF - CRR (Continuous Range Readout) Handlers

func handleCRRCreateWithAircraft(ep *ERAMPane, ctx *panes.Context, loc CRRLocation, label string, tracks []*sim.Track) (CommandStatus, error) {
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

	// All tracks must be associated
	for _, trk := range tracks {
		if !trk.IsAssociated() {
			return CommandStatus{}, ErrERAMIllegalACID
		}
	}

	// Create group
	g := &CRRGroup{
		Label:    label,
		Location: loc.Location,
		Color:    ep.currentPrefs().CRR.SelectedColor,
		Aircraft: make(map[av.ADSBCallsign]struct{}),
	}
	ep.CRRGroups[label] = g

	for _, trk := range tracks {
		g.Aircraft[trk.ADSBCallsign] = struct{}{}
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR GROUP %s CREATED", label)},
	}, nil
}

func handleCRRCreate(ep *ERAMPane, ctx *panes.Context, loc CRRLocation, label string) (CommandStatus, error) {
	return handleCRRCreateWithAircraft(ep, ctx, loc, label, nil)
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
func handleCRRAddClicked(ep *ERAMPane, ctx *panes.Context, loc math.Point2LL, label string) (CommandStatus, error) {
	// Validate label
	if !validCRRLabel(label) {
		return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", locationSymbol, strings.ToUpper(label))
	}

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
			feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR GROUP %s CREATED", label)},
		}, nil
	}

	// Group exists - find nearest track and add to group
	nearest := ep.closestTrackToLL(ctx, loc, 5)
	if nearest == nil {
		return CommandStatus{}, NewERAMError("REJECT - NO TB FLIGHT ID\nCAPTURE\nCONT RANGE\nLF %s %s", locationSymbol, strings.ToUpper(label))
	}

	g.Aircraft[nearest.ADSBCallsign] = struct{}{}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR UPDATED %s", g.Label)},
	}, nil
}

// handleCRRWrongOrder handles LF LABEL {pos} - wrong order error
func handleCRRWrongOrder(label string, pos math.Point2LL) (CommandStatus, error) {
	return CommandStatus{}, NewERAMError("REJECT - CRR - GROUP NOT\nFOUND\nCONT RANGE\nLF %s %s", strings.ToUpper(label), locationSymbol)
}

func handleCRRToggleMembership(ep *ERAMPane, label string, tracks []*sim.Track) (CommandStatus, error) {
	if !validCRRLabel(label) {
		return CommandStatus{}, ErrCommandFormat
	}

	// Find existing group
	g := ep.CRRGroups[label]
	if g == nil {
		return CommandStatus{}, ErrCommandFormat
	}

	// Toggle membership for each aircraft
	for _, trk := range tracks {
		if !trk.IsAssociated() {
			return CommandStatus{}, ErrERAMIllegalACID
		}

		cs := trk.ADSBCallsign
		if _, ok := g.Aircraft[cs]; ok {
			delete(g.Aircraft, cs)
		} else {
			g.Aircraft[cs] = struct{}{}
		}
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", fmt.Sprintf("CRR UPDATED %s", g.Label)},
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

func handleToggleVCI(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayVCI = !state.DisplayVCI

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "TOGGLE ON-FREQUENCY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
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
	if trk.IsUnassociated() {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	if ctx.IsHandoffToUser(trk) {
		// Accept handoff
		acid := sim.ACID(trk.ADSBCallsign.String())
		ep.acceptHandoff(ctx, acid)
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "ACCEPT HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}

	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && trk.FlightPlan.HandoffController != "" {
		// Recall handoff
		acid := sim.ACID(trk.ADSBCallsign.String())
		ep.recallHandoff(ctx, acid)
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "RECALL HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}

	// Toggle FDB display
	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		return CommandStatus{}, NewERAMError("USER ACTION NOT ALLOWED ON A\nCONTROLLER FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.EFDB = !state.EFDB
	state.DisplayJRing = false
	state.DisplayReducedJRing = false

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "FORCED DATA BLK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleInitiateHandoff(ep *ERAMPane, ctx *panes.Context, sector string, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	acid := sim.ACID(trk.ADSBCallsign)
	err := ep.handoffTrack(ctx, acid, sector)
	if err != nil {
		return CommandStatus{}, err
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "INITIATE HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleLeaderLinePosition(ep *ERAMPane, ctx *panes.Context, dir int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	direction, ok := ep.numberToLLDirection(dir)
	if !ok {
		return CommandStatus{}, ErrERAMIllegalValue
	}
	if ep.datablockType(ctx, *trk) != FullDatablock {
		if direction != math.East && direction != math.West {
			return CommandStatus{}, ErrERAMIllegalValue
		}
	}

	ep.TrackState[trk.ADSBCallsign].LeaderLineDirection = &direction

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func (ep *ERAMPane) numberToLLDirection(cmd int) (math.CardinalOrdinalDirection, bool) {
	if ep.FlipNumericKeypad {
		// Inverted layout: 1=NW (top-left on physical numpad)
		switch cmd {
		case 1:
			return math.NorthWest, true
		case 2:
			return math.North, true
		case 3:
			return math.NorthEast, true
		case 4:
			return math.West, true
		case 5:
			return math.NorthEast, true
		case 6:
			return math.East, true
		case 7:
			return math.SouthWest, true
		case 8:
			return math.South, true
		case 9:
			return math.SouthEast, true
		default:
			return 0, false
		}
	} else {
		// Default layout: 1=SW (bottom-left on physical numpad)
		switch cmd {
		case 1:
			return math.SouthWest, true
		case 2:
			return math.South, true
		case 3:
			return math.SouthEast, true
		case 4:
			return math.West, true
		case 5:
			return math.NorthEast, true
		case 6:
			return math.East, true
		case 7:
			return math.NorthWest, true
		case 8:
			return math.North, true
		case 9:
			return math.NorthEast, true
		default:
			return 0, false
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Leader Line Length Handlers

func handleLeaderLineLength(ep *ERAMPane, ctx *panes.Context, length int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	} else if length < 0 || length > 3 {
		return CommandStatus{}, fmt.Errorf("REJECT - INVALID\nLDR LENGTH\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	} else {
		ep.TrackState[trk.ADSBCallsign].LeaderLineLength = length

		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}
}

func handleLeaderLinePositionAndLength(ep *ERAMPane, ctx *panes.Context, dir, length int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	} else if length < 0 || length > 3 {
		return CommandStatus{}, fmt.Errorf("REJECT - INVALID\nLDR LENGTH\n%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)
	} else {
		direction, ok := ep.numberToLLDirection(dir)
		if !ok {
			return CommandStatus{}, ErrERAMIllegalValue
		}

		if ep.datablockType(ctx, *trk) != FullDatablock {
			if direction != math.East && direction != math.West {
				return CommandStatus{}, ErrERAMIllegalValue
			}
		}

		ep.TrackState[trk.ADSBCallsign].LeaderLineDirection = &direction
		ep.TrackState[trk.ADSBCallsign].LeaderLineLength = length

		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}
}

///////////////////////////////////////////////////////////////////////////
// .DRAWROUTE - Custom command

func handleDrawRouteMode(ep *ERAMPane, ctx *panes.Context) CommandStatus {
	ep.commandMode = CommandModeDrawRoute
	ep.drawRoutePoints = nil
	ep.responseArea = "DRAWROUTE"
	return CommandStatus{clear: true}
}

func handleDrawRoutePoint(ep *ERAMPane, ctx *panes.Context, pos math.Point2LL) CommandStatus {
	ep.drawRoutePoints = append(ep.drawRoutePoints, pos)

	var cb []string
	for _, p := range ep.drawRoutePoints {
		cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
	}
	ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))

	ep.responseArea = fmt.Sprintf("DRAWROUTE: %d POINTS", len(ep.drawRoutePoints))

	return CommandStatus{}
}

///////////////////////////////////////////////////////////////////////////
// Helper function to check if a character is a digit

func isDigit(r rune) bool {
	return unicode.IsDigit(r)
}

///////////////////////////////////////////////////////////////////////////
// QS - HSF (Heading / Speed-Mach / Free-text) Handlers

func qsFDBDataAcceptMsg(trk *sim.Track) []string {
	if trk == nil || trk.FlightPlan == nil {
		return nil
	}
	return []string{"ACCEPT", "FDB DATA", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID}
}

func handleQSToggleHSF(ep *ERAMPane, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.HSFHide = !state.HSFHide
	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteHeading(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteSpeed(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteAll(ep *ERAMPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSHeading(ep *ERAMPane, ctx *panes.Context, heading string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(heading)
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSSpeed(ep *ERAMPane, ctx *panes.Context, speed string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set(speed)
	if isQSFreeText(trk.FlightPlan.Scratchpad) {
		fp.Scratchpad.Set("")
	}
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSFreeText(ep *ERAMPane, ctx *panes.Context, freeText string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrERAMIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(freeText)
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{clear: true, feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func isQSFreeText(s string) bool {
	return strings.HasPrefix(s, circleClear)
}

func lookupCommandAirport(airport string) (string, bool) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if airport == "" || (len(airport) != 3 && len(airport) != 4) {
		return "", false
	}
	if ap, ok := av.DB.LookupAirport(airport); ok {
		return ap.Id, true
	}
	return "", false
}

///////////////////////////////////////////////////////////////////////////
// AR - ALTIM SET Add Airport Handler

func handleAltimAdd(ep *ERAMPane, ctx *panes.Context, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - AR - MISSING AIRPORT")
	}
	if len(airport) != 3 && len(airport) != 4 {
		return CommandStatus{}, NewERAMError("REJECT - AR - INVALID AIRPORT CODE")
	}

	// Confirm airport exists in the database
	icao, ok := lookupCommandAirport(airport)
	if !ok {
		return CommandStatus{}, NewERAMError("REJECT - AR - UNKNOWN AIRPORT")
	}

	// Toggle: if already in the list, remove it.
	if i := slices.Index(ep.AltimSetAirports, icao); i >= 0 {
		ep.AltimSetAirports = slices.Delete(ep.AltimSetAirports, i, i+1)
		// Adjust scroll offset if needed after removal
		ps := ep.currentPrefs()
		maxOffset := len(ep.AltimSetAirports) - ps.AltimSet.Lines
		if maxOffset < 0 {
			maxOffset = 0
		}
		if ep.altimSetScroll.Offset > maxOffset {
			ep.altimSetScroll.Offset = maxOffset
		}
		return CommandStatus{feedbackArea: []string{"ACCEPT", "ALTIMETER REQ"}}, nil
	}

	ep.AltimSetAirports = slices.Insert(ep.AltimSetAirports, 0, icao)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.AltimSet.Visible = true

	requestMETARIfMissing(ctx, icao)

	return CommandStatus{feedbackArea: []string{"ACCEPT", "ALTIMETER REQ"}}, nil
}

// WR - WX REPORT Add Airport Handler

func handleWXReportAdd(ep *ERAMPane, ctx *panes.Context, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - WR - MISSING AIRPORT")
	}
	if len(airport) != 3 && len(airport) != 4 {
		return CommandStatus{}, NewERAMError("REJECT - WR - INVALID AIRPORT CODE")
	}

	// Confirm airport exists in the database
	icao, ok := lookupCommandAirport(airport)
	if !ok {
		return CommandStatus{}, NewERAMError("REJECT - WR - UNKNOWN AIRPORT")
	}
	if i := slices.Index(ep.WXReportStations, icao); i >= 0 {
		ep.WXReportStations = slices.Delete(ep.WXReportStations, i, i+1)
		// Adjust scroll offset if needed after removal
		ps := ep.currentPrefs()
		maxOffset := len(ep.WXReportStations) - ps.WX.Lines
		if maxOffset < 0 {
			maxOffset = 0
		}
		if ep.wxScroll.Offset > maxOffset {
			ep.wxScroll.Offset = maxOffset
		}
		return CommandStatus{feedbackArea: []string{"ACCEPT", "WEATHER STAT REQ"}}, nil
	}

	ep.WXReportStations = slices.Insert(ep.WXReportStations, 0, icao)

	// Make the window visible when the first airport is added.
	ps := ep.currentPrefs()
	ps.WX.Visible = true

	requestMETARIfMissing(ctx, icao)

	return CommandStatus{feedbackArea: []string{"ACCEPT", "WEATHER STAT REQ"}}, nil
}

// requestMETARIfMissing asks the server to start supplying METAR for icao
// when the client has not yet seen any METAR for that airport. The RPC
// is fire-and-forget: if the server rejects the airport the row keeps
// showing "-M-", matching the behavior for known airports without
// bundled data.
func requestMETARIfMissing(ctx *panes.Context, icao string) {
	if _, ok := ctx.Client.State.METAR[icao]; !ok {
		ctx.Client.AddMETARAirport(icao)
	}
}

// WR R - WX REPORT Display (show METAR in Response Area)

func handleWXReportDisplay(ep *ERAMPane, ctx *panes.Context, airport string) (CommandStatus, error) {
	airport = strings.ToUpper(strings.TrimSpace(airport))
	if len(airport) == 0 {
		return CommandStatus{}, NewERAMError("REJECT - WR R - MISSING AIRPORT")
	}
	if len(airport) != 3 && len(airport) != 4 {
		return CommandStatus{}, NewERAMError("REJECT - WR R - INVALID AIRPORT CODE")
	}

	// Confirm airport exists in the database
	icao, ok := lookupCommandAirport(airport)
	if !ok {
		return CommandStatus{}, NewERAMError("REJECT - WR R - UNKNOWN AIRPORT")
	}

	// Get METAR from the pre-populated wx system
	metar, hasMetar := ctx.Client.State.METAR[icao]

	// Determine what to display
	var displayText string
	if hasMetar && metar.Raw != "" {
		displayText = metar.Raw
	} else {
		displayText = fmt.Sprintf("NO DATA\n%s", icao)
	}

	ep.responseArea = formatInput(displayText)

	return CommandStatus{}, nil
}
