// eram/cmdops.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// ERAM operational commands

package eram

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/goforj/godump"
)

func registerOpsCommands() {
	// QQ - Interim altitude
	// QQ [ALT] [TRACK]: Set interim, procedure (P), or local interim (L) altitude
	// QQ L [TRACK]: Clear local interim altitude
	// QQ [TRACK]: Clear interim or procedure altitude
	registerCommand("QQ [ERAM_ALT_I] [TRACK]", handleInterimAltitude)
	registerCommand("QQ L [TRACK]", handleClearLocalInterimAltitude)
	registerCommand("QQ [TRACK]", handleClearInterimAltitude)

	// QZ - Assigned altitude
	// QZ [ALT] [TRACK]: Set assigned altitude
	// QZ [FLOOR]B[CEILING] [TRACK]: Set block altitude, (d)ddB(d)dd
	registerCommand("QZ [ERAM_ALT_A] [TRACK]", handleAssignedAltitude)
	registerCommand("QZ [ALT_BLOCK] [TRACK]", handleAssignedAltitudeBlock)
	// QZ VFR, VFR/[ALT], OTP, or OTP/[ALT] [TRACK]: Set VFR or VFR-on-top altitude
	registerCommand("QZ [ALT_VFR] [TRACK]", handleAssignedVFRAltitude)

	// QX - Drop track
	// QX [TRACK]
	registerCommand("QX [TRACK]", handleDropTrack)

	// QU - Direct to fix / Route display
	// QU: Clear all route displays
	// QU [TRACK]: Display 20 minutes of the route
	// QU /M [TRACK]: Display full route
	// QU [MINUTES] [TRACK] or QU [TRACK] [MINUTES]: Display route for specified minutes
	// QU [FIXES_AND_TRACK]: Amend route for track
	registerCommand("QU", handleClearRouteDisplay)
	registerCommand("QU [TRACK]", handleDefaultRouteDisplay)
	registerCommand("QU /M [TRACK]", handleMaxRouteDisplay)
	registerCommand("QU [MINUTES] [TRACK]|QU [TRACK] [MINUTES]", handleRouteDisplayMinutes)
	registerCommand("QU [FIXES_AND_TRACK]", handleAmendRoute)

	// QP - J rings
	// QP J [TRACK]: Toggle J ring
	// QP T [TRACK]: Toggle reduced J ring
	registerCommand("QP J [TRACK]", handleJRing)
	registerCommand("QP T [TRACK]", handleReducedJRing)

	// QP - Point outs
	// QP [SECTOR_ID] [TRACK]: Initiate point out
	// QP A [TRACK]: Acknowledge the track's one point out
	// QP A [SECTOR_ID_LIST] [TRACK]: Acknowledge the point outs from the given sectors
	// QP [TRACK]: Request or suppress the FDB of another sector's track
	registerCommand("QP A [TRACK]",
		func(ep *Scope, ctx *scope.Context, trk *sim.Track) error {
			return ep.acknowledgePointOuts(ctx, trk, nil)
		})
	registerCommand("QP A [SECTOR_ID_LIST] [TRACK]",
		func(ep *Scope, ctx *scope.Context, sectors []string, trk *sim.Track) error {
			return ep.acknowledgePointOuts(ctx, trk, sectors)
		})
	registerCommand("QP [SECTOR_ID] [TRACK]",
		func(ep *Scope, ctx *scope.Context, sector string, trk *sim.Track) error {
			return ep.pointOutTrack(ctx, trk, sector)
		})
	registerCommand("QP [TRACK]",
		func(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
			return ep.requestSuppressDatablock(ctx, trk)
		})

	// SG - Conflict alert suppression groups (4.2.14)
	// SG [A|D] [SUPP_GROUP] [TRACK]: Add the flight to the group or delete
	//   it; without A or D, delete it if it's in the group and otherwise
	//   add it
	// SG [SUPP_GROUP]: Display the group
	// SG A|D [SUPP_GROUP]: Subscribe to or unsubscribe from the group
	// SG [A] [TRACK_LIST]: Add a group of the flights; without A, a group
	//   that already has exactly those flights is deleted
	// The group forms come first so that an ID with no group is reported
	// as such rather than as an illegal flight of a flight list.
	registerCommand("SG [SUPP_GROUP] [TRACK]",
		func(ep *Scope, ctx *scope.Context, id int, trk *sim.Track) (CommandStatus, error) {
			return ep.enterGroupFlight(ctx, id, trk, toggleGroupFlight)
		})
	registerCommand("SG A [SUPP_GROUP] [TRACK]",
		func(ep *Scope, ctx *scope.Context, id int, trk *sim.Track) (CommandStatus, error) {
			return ep.enterGroupFlight(ctx, id, trk, addGroupFlight)
		})
	registerCommand("SG D [SUPP_GROUP] [TRACK]",
		func(ep *Scope, ctx *scope.Context, id int, trk *sim.Track) (CommandStatus, error) {
			return ep.enterGroupFlight(ctx, id, trk, deleteGroupFlight)
		})
	registerCommand("SG [SUPP_GROUP]|SG A [SUPP_GROUP]", (*Scope).displaySuppressionGroup)
	registerCommand("SG D [SUPP_GROUP]", (*Scope).unsubscribeSuppressionGroup)
	registerCommand("SG [TRACK_LIST]",
		func(ep *Scope, tracks []*sim.Track) (CommandStatus, error) {
			return ep.enterSuppressionGroup(tracks, false)
		})
	registerCommand("SG A [TRACK_LIST]",
		func(ep *Scope, tracks []*sim.Track) (CommandStatus, error) {
			return ep.enterSuppressionGroup(tracks, true)
		})

	// QL - Quicklook
	registerCommand("QL [SECTOR_ID_LIST]", handleToggleQuicklook)
	registerCommand("QL", handleDisableAllQuicklook)

	// QB - beacon code view codes
	registerCommand("QB [BCN_LIST]", handleBeaconCodeViewList)

	// QD - Altitude limits filters
	// QD [LOW]B[HIGH]: set both the target and the LDB filter
	registerCommand("QD [ALT_LIMITS]", handleAltitudeLimitsFilter)

	// QF - Flight Plan Display
	registerCommand("QF R [TRACK]", handleFlightPlanReadoutRA)
	registerCommand("QF [TRACK]", handleFlightPlanReadout)

	// QS - HSF (Heading / Speed-Mach / Free-text) scratchpad handling
	//   QS `<text> [TRACK]      - set free text (backtick is clear-weather symbol)
	//   QS <heading> [TRACK]    - set heading (1-4 chars; not validated)
	//   QS /<speedSpec> [TRACK] - set speed or mach (see HSF_SPEED parser)
	//   QS <heading>/<speedSpec> [TRACK] - set heading and speed or mach
	//   QS */ [TRACK]           - delete heading
	//   QS /* [TRACK]           - delete speed/mach
	//   QS * [TRACK]            - delete all HSF data
	//   QS [TRACK]              - toggle display of HSF data
	registerCommand("QS [HSF_TEXT] [TRACK]", handleQSFreeText)
	registerCommand("QS [HSF_SPEED] [TRACK]", handleQSSpeed)
	registerCommand("QS [HSF_HDG] [TRACK]", handleQSHeading)
	registerCommand("QS [HSF_HDG][HSF_SPEED] [TRACK]", handleQSHeadingSpeed)
	registerCommand("QS */ [TRACK]", handleQSDeleteHeading)
	registerCommand("QS /* [TRACK]", handleQSDeleteSpeed)
	registerCommand("QS * [TRACK]", handleQSDeleteAll)
	registerCommand("QS [TRACK]", handleQSToggleHSF)

	// MR - Map request (keyboard only)
	// MR: List available map groups
	// MR [GROUP]: Load map group
	registerCommand("MR", handleMapRequestList)
	registerCommand("MR [FIELD]", handleMapRequestLoad)

	// LA - track range - distance between points
	registerCommand("LA [LOC_SYM] [LOC_SYM]",
		func(ep *Scope, ctx *scope.Context, from math.Point2LL, to math.Point2LL) CommandStatus {
			return handleLALocLoc(ep, ctx, from, to, laOptions{})
		})
	registerCommand("LA [TRACK] [LOC_SYM]",
		func(ep *Scope, ctx *scope.Context, trk *sim.Track, to math.Point2LL) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk, to, laOptions{})
		})
	registerCommand("LA [TRACK] [TRACK]",
		func(ep *Scope, ctx *scope.Context, trk1, trk2 *sim.Track) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk1, ep.TrackState[trk2.ADSBCallsign].Track.Location, laOptions{})
		})
	registerCommand("LA [LOC_SYM] [LOC_SYM] [LA_OPTS]", handleLALocLoc)
	registerCommand("LA [TRACK] [LOC_SYM] [LA_OPTS]", handleLATrkLoc)
	registerCommand("LA [TRACK] [TRACK] [LA_OPTS]",
		func(ep *Scope, ctx *scope.Context, trk1, trk2 *sim.Track, opts laOptions) CommandStatus {
			return handleLATrkLoc(ep, ctx, trk1, ep.TrackState[trk2.ADSBCallsign].Track.Location, opts)
		})

	// LB - track range - distance between fix and track/location
	registerCommand("LB [FIX] [LOC_SYM]", handleLBFixLoc)
	registerCommand("LB [FIX] [TRACK]", handleLBFixTrk)
	registerCommand("LB [FIX]/[NUM] [TRACK]", handleLBFixSpeedTrk)

	// LC - speed adjustment to position a track over a fix at a specified UTC time
	registerCommand("LC [FIX]/[NUM] [TRACK]", handleLCFixTimeTrk)

	// LD - times at the fixes of a track's route through a fix
	registerCommand("LD [FIX] [TRACK]", handleLDFixTrk)

	// LE - speed adjustment to reach a fix on a track's route at a specified UTC time
	registerCommand("LE [FIX]/[NUM] [TRACK]", handleLEFixTimeTrk)

	// LF - CRR (Continuous Range Readout)
	// LF //FIX [LABEL]: Create new CRR group at fix location
	// LF //FIX [LABEL] [AIRCRAFT...]: Create new CRR group with up to 4 aircraft
	// LF {pos} [LABEL]: Create group at clicked position (click first, then label)
	// LF {pos} [LABEL] [AIRCRAFT...]: Create group at clicked position with up to 4 aircraft
	// LF [LABEL] [AIRCRAFT...]: Toggle membership for up to 4 aircraft
	// Aircraft tokens may be CIDs, ADSB callsigns, or beacon codes separated by
	// '/' and/or spaces, and/or clicks on tracks.
	registerCommand("LF [CRR_LOC] [CRR_LABEL] [TRACK_LIST]", handleCRRCreateWithAircraft)
	registerCommand("LF [CRR_LOC] [CRR_LABEL]", handleCRRCreate)
	registerCommand("LF [CRR_LOC]", handleCRRCreateAutoLabel)
	registerCommand("LF [LOC_SYM] [CRR_LABEL] [TRACK_LIST]", handleCRRCreateClicked)
	registerCommand("LF [LOC_SYM] [CRR_LABEL]",
		func(ep *Scope, ctx *scope.Context, loc math.Point2LL, label string) (CommandStatus, error) {
			return handleCRRCreateClicked(ep, ctx, loc, label, nil)
		})
	registerCommand("LF [CRR_LABEL] [LOC_SYM]", handleCRRWrongOrder) // LF LABEL {pos} - wrong order
	registerCommand("LF [CRR_LABEL] [TRACK_LIST]", handleCRRToggleMembership)
	// Error handlers for incomplete LF commands
	registerCommand("LF [CRR_LABEL]", handleCRRLabelOnly) // LF LABEL without click or aircraft
	registerCommand("LF", handleCRREmpty)                 // LF alone

	// AR - Add airport to ALTIM SET list
	// AR [ICAO]: Add 4-letter ICAO code (e.g., AR KMCO)
	// AR [IATA]: Add 3-letter IATA code, auto-prepends K (e.g., AR MCO → KMCO)
	registerCommand("AR [FIELD]", handleAltimAdd)

	// WR - Weather report commands
	// WR [ICAO]: Add 4-letter ICAO code to WX REPORT list (e.g., WR KMCO)
	// WR [IATA]: Add 3-letter IATA code, auto-prepends K (e.g., WR MCO → KMCO)
	// WR [STATION] [STATION] [STATION]: Add or remove up to three stations at once
	// WR R [ICAO]: Display station's weather report in Response Area (e.g., WR R KMCO)
	// WR R [IATA]: Display with 3-letter IATA code (e.g., WR R MCO → KMCO)
	registerCommand("WR R [FIELD]", handleWXReportDisplay)
	registerCommand("WR [ALL_TEXT]", handleWXReportAdd)

	// // - Toggle VCI (on-frequency indicator)
	// QN // [TRACK] or QZ // [TRACK], or // [TRACK] with QN implied; the space before
	// the track is optional (5.1.1.1, 2.15.1).
	registerCommand("//[TRACK]|// [TRACK]", handleToggleVCI)
	registerCommand("QN //[TRACK]|QN // [TRACK]|QZ //[TRACK]|QZ // [TRACK]", handleToggleVCI)

	// TG - Target gen commands
	// TG P: Toggle pause
	// TG [cmds]: Run commands on last used aircraft
	// TG [cmds][TRACK]: Run commands on clicked track
	registerCommand("TG P", handleTogglePause)
	registerCommand("TG [ALL_TEXT]", handleTargetGen)
	registerCommand("TG [ALL_TEXT][TRACK]", handleTargetGenClicked)
	registerCommand("TG[TRACK]", handleTargetGenEmptyClicked)

	// Default commands, which are QN commands entered without the QN (2.15.1).
	// Handoffs and data block offsets may also be entered with QN or QZ.
	// [TRACK]: Accept handoff / recall handoff / toggle FDB
	// [SECTOR_ID] [TRACK]: Initiate handoff
	// [1-9] [TRACK]: Leader line direction
	registerCommand("[TRACK]|QN [TRACK]", handleDefaultTrack)
	registerCommand("[SECTOR_ID] [TRACK]|QN [SECTOR_ID] [TRACK]|QZ [SECTOR_ID] [TRACK]",
		handleInitiateHandoff)
	registerCommand("[#] [TRACK]|QN [#] [TRACK]|QZ [#] [TRACK]", handleLeaderLinePosition)

	// Leader line length commands
	// /[0-3] [TRACK]: Set leader line length (0=no line, 1=normal, 2=2x, 3=3x)
	// [1-9]/[0-3] [TRACK]: Set leader line position and length
	registerCommand("/[NUM] [TRACK]|QN /[NUM] [TRACK]|QZ /[NUM] [TRACK]", handleLeaderLineLength)
	registerCommand("[NUM]/[NUM] [TRACK]|QN [NUM]/[NUM] [TRACK]|QZ [NUM]/[NUM] [TRACK]",
		handleLeaderLinePositionAndLength)

	// .DRAWROUTE - Custom command for drawing routes
	registerCommand(".DRAWROUTE", handleDrawRouteMode)

	// ? - Custom command to print aircraft state to stdout and copy it to the clipboard
	registerCommand("?[TRACK]|? [TRACK]", handlePrintAircraftState)
}

///////////////////////////////////////////////////////////////////////////
// QQ - Interim Altitude Handlers

// handleInterimAltitude enters an interim, procedure, or local interim
// altitude. An interim and a procedure altitude replace each other, and
// neither replaces a local interim altitude (8.4).
func handleInterimAltitude(ep *Scope, ctx *scope.Context, alt InterimAltitude, trk *sim.Track) (CommandStatus, error) {
	var spec sim.FlightPlanSpecifier
	if alt.Type == sim.InterimLocal {
		spec.LocalInterimAlt.Set(alt.Altitude)
	} else {
		spec.InterimAlt.Set(alt.Altitude)
		spec.InterimType.Set(alt.Type)
	}
	return modifyInterimAltitude(ep, ctx, trk, spec)
}

// handleClearInterimAltitude removes the interim or procedure altitude,
// leaving any local interim altitude.
func handleClearInterimAltitude(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	var spec sim.FlightPlanSpecifier
	spec.InterimAlt.Set(0)
	spec.InterimType.Set(sim.InterimNormal)
	return modifyInterimAltitude(ep, ctx, trk, spec)
}

// handleClearLocalInterimAltitude removes the local interim altitude, so
// that the data block again shows any interim or procedure altitude (8.4).
func handleClearLocalInterimAltitude(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	var spec sim.FlightPlanSpecifier
	spec.LocalInterimAlt.Set(0)
	return modifyInterimAltitude(ep, ctx, trk, spec)
}

// modifyInterimAltitude makes a QQ entry's change to the flight plan.
func modifyInterimAltitude(ep *Scope, ctx *scope.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	ep.modifyFlightPlan(ctx, trk, spec)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "INTERIM ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QZ - Assigned Altitude Handler

func handleAssignedAltitude(ep *Scope, ctx *scope.Context, alt int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	fp := sim.FlightPlanSpecifier{}
	fp.AssignedAltitude.Set(alt)

	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ASSIGNED ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

// handleAssignedAltitudeBlock amends the flight plan's assigned altitude to a
// block. Pilots have no block clearance to fly, so unlike a hard altitude,
// no climb or descent is issued to the aircraft.
func handleAssignedAltitudeBlock(ep *Scope, ctx *scope.Context, block [2]int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}
	if !validAltitudeBlock(block) {
		return CommandStatus{}, ErrIllegalValue
	}

	fp := sim.FlightPlanSpecifier{}
	fp.AltitudeBlock.Set([2]int{block[0] * 100, block[1] * 100})
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ASSIGNED ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

// handleAssignedVFRAltitude assigns a VFR or VFR-on-top altitude. Those
// aren't instructions a pilot is given, so no instruction is issued.
func handleAssignedVFRAltitude(ep *Scope, ctx *scope.Context, alt altitudeEntry, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var spec sim.FlightPlanSpecifier
	alt.setAssigned(&spec)
	ctx.Client.ModifyFlightPlan(trk.FlightPlan.ACID, spec, func(err error) { ep.displayError(err, ctx) })

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ASSIGNED ALT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QX - Drop Track Handler

func handleDropTrack(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	// Manual 6.1 lets a sector drop a track without track control, but in a
	// shared sim that would take tracks from other controllers.
	if !ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && !ctx.TCWIsPrivileged(ctx.UserTCW) {
		return CommandStatus{}, NewError("SECTOR %s HAS CONTROL\nDROP TRACK %s",
			sectorDisplayID(ctx, trk.FlightPlan.TrackingController), trk.ADSBCallsign)
	}

	ep.deleteFlightPlan(ctx, *trk)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "DROP TRACK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QU - Direct / Route Display Handlers

func handleClearRouteDisplay(ep *Scope) {
	clear(ep.aircraftFixCoordinates)
}

// displayRoute requests the track's route display for the given number of
// minutes, or for all of the route if minutes is -1. Requesting a displayed
// route again redraws it (2.11).
func displayRoute(ep *Scope, ctx *scope.Context, trk *sim.Track, minutes int) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	ep.getQULines(ctx, trk.FlightPlan.ACID, minutes)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "ROUTE DISPLAY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleDefaultRouteDisplay(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	return displayRoute(ep, ctx, trk, 20)
}

func handleMaxRouteDisplay(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	return displayRoute(ep, ctx, trk, -1)
}

// handleRouteDisplayMinutes displays the route for 1 to 99 minutes (2.11).
func handleRouteDisplayMinutes(ep *Scope, ctx *scope.Context, minutes int, trk *sim.Track) (CommandStatus, error) {
	if minutes < 1 || minutes > 99 {
		return CommandStatus{}, ErrIllegalValue
	}
	return displayRoute(ep, ctx, trk, minutes)
}

func handleAmendRoute(ep *Scope, ctx *scope.Context, fixesAndTrack fixesAndTrack) error {
	if fixesAndTrack.trk.FlightPlan == nil {
		return ErrIllegalACID
	}

	ep.flightPlanDirect(ctx, fixesAndTrack.trk, fixesAndTrack.fixes)
	return nil
}

///////////////////////////////////////////////////////////////////////////
// QP - J Ring Handlers

func handleJRing(ep *Scope, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayJRing = !state.DisplayJRing
	state.DisplayReducedJRing = false // clear reduced J ring

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "REQ/DELETE DRI", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleReducedJRing(ep *Scope, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]

	if !inReducedSeparationAirspace(state.Track.TransponderAltitude) {
		return CommandStatus{}, NewError("REJECT - %s NOT ELIGIBLE\nFOR REDUCED SEPARATION\nREQ/DELETE DRI %s", trk.FlightPlan.CID, trk.ADSBCallsign)
	}

	state.DisplayJRing = false // clear J ring
	state.DisplayReducedJRing = !state.DisplayReducedJRing

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "REQ/DELETE DRI", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// QL - Quicklook

func handleToggleQuicklook(ep *Scope, sectors []string) {
	for _, s := range sectors {
		if _, ok := ep.QuickLookSectors[s]; ok {
			delete(ep.QuickLookSectors, s)
		} else {
			ep.QuickLookSectors[s] = struct{}{}
		}
	}
}

func handleDisableAllQuicklook(ep *Scope) {
	clear(ep.QuickLookSectors)
}

///////////////////////////////////////////////////////////////////////////
// QD - Altitude Limits Filter Handlers

// handleAltitudeLimitsFilter sets both the target and the LDB altitude limits
// filter; there is no command that sets only one of them. Whether the toolbar
// displays them as one filter or two is left as it was.
func handleAltitudeLimitsFilter(ep *Scope, limits [2]int) error {
	if !validAltitudeBlock(limits) {
		return ErrInvalidAltitudeLimits
	}
	ps := ep.currentPrefs()
	ps.AltitudeLimits.Targets = limits
	ps.AltitudeLimits.LDBs = limits
	return nil
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

func handleLALocLoc(ep *Scope, ctx *scope.Context, from math.Point2LL, to math.Point2LL, opts laOptions) CommandStatus {
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(from, to, ctx.NmPerLongitude, ctx.MagneticVariation,
			opts.True, float32(opts.Speed), "FROM 1ST TB ENTRY"),
	}
}

func handleLATrkLoc(ep *Scope, ctx *scope.Context, trk *sim.Track, to math.Point2LL, opts laOptions) CommandStatus {
	rt := ep.TrackState[trk.ADSBCallsign].Track
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING"},
		responseArea: formatRangeBearing(rt.Location, to, ctx.NmPerLongitude, ctx.MagneticVariation,
			opts.True, util.Select(opts.Speed > 0, float32(opts.Speed), rt.Groundspeed), "FROM 1ST TB ENTRY"),
	}
}

///////////////////////////////////////////////////////////////////////////
// LB - Range/Bearing From Fix

func handleLBFixLoc(ep *Scope, ctx *scope.Context, fix string, from math.Point2LL) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrIllegalValue
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING/FIX"},
		responseArea: formatRangeBearing(from, fixPos, ctx.NmPerLongitude, ctx.MagneticVariation,
			false, 0, "FROM TB TO FIX "+fix),
	}, nil
}

func handleLBFixTrk(ep *Scope, ctx *scope.Context, fix string, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrIllegalValue
	}
	rt := ep.TrackState[trk.ADSBCallsign].Track
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING/FIX"},
		responseArea: formatRangeBearing(rt.Location, fixPos, ctx.NmPerLongitude, ctx.MagneticVariation,
			false, rt.Groundspeed, "FROM TB TO FIX "+fix),
	}, nil
}

func handleLBFixSpeedTrk(ep *Scope, ctx *scope.Context, fix string, speed int, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrIllegalValue
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RANGE/BEARING/FIX"},
		responseArea: formatRangeBearing(ep.TrackState[trk.ADSBCallsign].Track.Location, fixPos,
			ctx.NmPerLongitude, ctx.MagneticVariation, false, float32(speed), "FROM TB TO FIX "+fix),
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LC - Speed adjustment to arrive over a fix at a specified UTC time (HHMM)

// handleLCFixTimeTrk reads out the change to the track's ground speed that
// would put it over the fix at the given time (3.12.3).
func handleLCFixTimeTrk(ep *Scope, ctx *scope.Context, fix string, hhmm int, trk *sim.Track) (CommandStatus, error) {
	fixPos, ok := ctx.Client.State.Locate(fix)
	if !ok {
		return CommandStatus{}, ErrIllegalValue
	}
	rt := ep.TrackState[trk.ADSBCallsign].Track
	return speedAdjustmentReadout(ctx, "FIX/TIME", fix, math.NMDistance2LL(rt.Location, fixPos), hhmm, rt.Groundspeed)
}

// speedAdjustmentReadout reads out the change to a track's ground speed
// that would have it fly dist nm to fix by the UTC time hhmm, and its
// present ground speed; function names the command in the feedback.
func speedAdjustmentReadout(ctx *scope.Context, function, fix string, dist float32, hhmm int,
	groundspeed float32) (CommandStatus, error) {
	hh, mm := hhmm/100, hhmm%100
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return CommandStatus{}, ErrIllegalValue
	}

	now := ctx.Client.State.SimTime.UTC()
	target := time.Date(now.Time().Year(), now.Time().Month(), now.Time().Day(), hh, mm, 0, 0, time.UTC)
	if !target.After(now.Time()) {
		target = target.Add(24 * time.Hour)
	}
	dtHours := target.Sub(now.Time()).Hours()
	if dtHours <= 0 {
		return CommandStatus{}, ErrIllegalValue
	}

	required := int(math.Round(dist / float32(dtHours)))
	present := int(math.Round(groundspeed))

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", function},
		responseArea: []string{fmt.Sprintf("%s AT %02d%02dZ%+dK", fix, hh, mm, required-present),
			fmt.Sprintf("PRESENT SPEED %d", present)},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// LD and LE - Fix/time readouts along the route of flight

// routeDistances returns the fixes of a track's route from from, its
// present position, through fix, with the distance flown along the route
// to each of them.
func routeDistances(ctx *scope.Context, trk *sim.Track, from math.Point2LL, fix string) ([]string, []float32, error) {
	if trk.FlightPlan == nil {
		return nil, nil, ErrIllegalACID
	}
	n := slices.Index(trk.RouteFixes, fix)
	if n == -1 {
		return nil, nil, ErrIllegalValue
	}

	fixes := trk.RouteFixes[:n+1]
	dists := make([]float32, len(fixes))
	p, d := from, float32(0)
	for i, f := range fixes {
		loc, ok := ctx.Client.State.Locate(f)
		if !ok {
			return nil, nil, ErrIllegalValue
		}
		d += math.NMDistance2LL(p, loc)
		dists[i], p = d, loc
	}
	return fixes, dists, nil
}

// handleLDFixTrk reads out the time at which the track reaches each fix of
// its route through the given fix, flying the route at its ground speed
// (3.12.4).
func handleLDFixTrk(ep *Scope, ctx *scope.Context, fix string, trk *sim.Track) (CommandStatus, error) {
	rt := ep.TrackState[trk.ADSBCallsign].Track
	fixes, dists, err := routeDistances(ctx, trk, rt.Location, fix)
	if err != nil {
		return CommandStatus{}, err
	}
	if rt.Groundspeed < 1 {
		return CommandStatus{}, ErrIllegalValue
	}

	now := ctx.Client.State.SimTime.UTC().Time()
	var times []string
	for i, f := range fixes {
		eta := now.Add(time.Duration(float64(dists[i]/rt.Groundspeed) * float64(time.Hour)))
		times = append(times, fmt.Sprintf("%s AT %sZ", f, eta.Round(time.Minute).Format("1504")))
	}
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "RTE FIX/TME RO"},
		responseArea: times,
	}, nil
}

// handleLEFixTimeTrk reads out the change to the track's ground speed that
// would have it reach the fix at the given time flying its route (3.12.5).
func handleLEFixTimeTrk(ep *Scope, ctx *scope.Context, fix string, hhmm int, trk *sim.Track) (CommandStatus, error) {
	rt := ep.TrackState[trk.ADSBCallsign].Track
	_, dists, err := routeDistances(ctx, trk, rt.Location, fix)
	if err != nil {
		return CommandStatus{}, err
	}
	return speedAdjustmentReadout(ctx, "RTE SPEED ADJ", fix, dists[len(dists)-1], hhmm, rt.Groundspeed)
}

///////////////////////////////////////////////////////////////////////////
// // - Toggle VCI Handler

func handleToggleVCI(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.DisplayVCI = !state.DisplayVCI
	// The command expands a suppressed data block to an FDB (5.1.1.1).
	if ep.datablockType(ctx, *trk) != FullDatablock {
		state.EFDB = true
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "TOGGLE ON-FREQUENCY", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// TG - Target Gen Handlers

func handleTogglePause(ctx *scope.Context) {
	ctx.Client.ToggleSimPause()
}

func handleTargetGen(ep *Scope, ctx *scope.Context, cmd string) (CommandStatus, error) {
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
		return CommandStatus{}, ErrAmbiguousACID
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
		return CommandStatus{}, nil
	}

	return CommandStatus{}, ErrIllegalACID
}

func handleTargetGenClicked(ep *Scope, ctx *scope.Context, cmd string, trk *sim.Track) CommandStatus {
	if cmd != "" {
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
	}
	return CommandStatus{}
}

func handleTargetGenEmptyClicked(ep *Scope, trk *sim.Track) CommandStatus {
	// Just clicking on a track in TG mode - do nothing special
	return CommandStatus{}
}

///////////////////////////////////////////////////////////////////////////
// Default Command Handlers (keyboard and clicked)

func handleDefaultTrack(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.IsUnassociated() {
		return CommandStatus{}, ErrIllegalACID
	}

	if ctx.IsHandoffToUser(trk) {
		// Accept handoff
		acid := trk.FlightPlan.ACID
		ep.acceptHandoff(ctx, acid)
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "ACCEPT HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}

	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && trk.FlightPlan.HandoffController != "" {
		// Recall handoff
		acid := trk.FlightPlan.ACID
		ep.recallHandoff(ctx, acid)
		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "RECALL HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
		}, nil
	}

	// Toggle FDB display
	if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		return CommandStatus{}, NewError("USER ACTION NOT ALLOWED ON A\nCONTROLLER FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.EFDB = !state.EFDB
	state.DisplayJRing = false
	state.DisplayReducedJRing = false

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "FORCED DATA BLK", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleInitiateHandoff(ep *Scope, ctx *scope.Context, sector string, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	err := ep.handoffTrack(ctx, trk.FlightPlan.ACID, sector)
	if err != nil {
		return CommandStatus{}, err
	}

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "INITIATE HANDOFF", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

func handleLeaderLinePosition(ep *Scope, ctx *scope.Context, dir int, trk *sim.Track) (CommandStatus, error) {
	direction, ok := ep.numberToLLDirection(dir)
	if !ok {
		return CommandStatus{}, ErrIllegalValue
	}
	if ep.datablockType(ctx, *trk) != FullDatablock {
		if direction != math.East && direction != math.West {
			return CommandStatus{}, ErrIllegalValue
		}
	}

	ep.TrackState[trk.ADSBCallsign].LeaderLineDirection = &direction

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", offsetTrackID(trk)},
	}, nil
}

func (ep *Scope) numberToLLDirection(cmd int) (math.CardinalOrdinalDirection, bool) {
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

// offsetTrackID returns how datablock offset feedback identifies a track.
// Unpaired tracks have no CID, so they are identified by callsign alone.
func offsetTrackID(trk *sim.Track) string {
	if trk.FlightPlan == nil {
		return string(trk.ADSBCallsign)
	}
	return string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID
}

///////////////////////////////////////////////////////////////////////////
// Leader Line Length Handlers

func handleLeaderLineLength(ep *Scope, ctx *scope.Context, length int, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	} else if length < 0 || length > 3 {
		return CommandStatus{}, fmt.Errorf("REJECT - INVALID\nLDR LENGTH\n%s", offsetTrackID(trk))
	} else {
		ep.TrackState[trk.ADSBCallsign].LeaderLineLength = &length

		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", offsetTrackID(trk)},
		}, nil
	}
}

func handleLeaderLinePositionAndLength(ep *Scope, ctx *scope.Context, dir, length int, trk *sim.Track) (CommandStatus, error) {
	if length < 0 || length > 3 {
		return CommandStatus{}, fmt.Errorf("REJECT - INVALID\nLDR LENGTH\n%s", offsetTrackID(trk))
	} else {
		direction, ok := ep.numberToLLDirection(dir)
		if !ok {
			return CommandStatus{}, ErrIllegalValue
		}

		if ep.datablockType(ctx, *trk) != FullDatablock {
			if direction != math.East && direction != math.West {
				return CommandStatus{}, ErrIllegalValue
			}
		}

		ep.TrackState[trk.ADSBCallsign].LeaderLineDirection = &direction
		ep.TrackState[trk.ADSBCallsign].LeaderLineLength = &length

		return CommandStatus{
			feedbackArea: []string{"ACCEPT", "OFFSET DATA BLK", offsetTrackID(trk)},
		}, nil
	}
}

///////////////////////////////////////////////////////////////////////////
// ? - Custom command

func handlePrintAircraftState(ep *Scope, ctx *scope.Context, trk *sim.Track) {
	ads, err := ctx.Client.GetAircraftDisplayState(trk.ADSBCallsign)
	if err != nil {
		ctx.Lg.Error("print aircraft", slog.String("callsign", string(trk.ADSBCallsign)),
			slog.Any("err", err))
		return
	}
	s := ads.Spew + "\n\n\n" + godump.DumpStr(ep.TrackState[trk.ADSBCallsign])
	fmt.Println(s)
	ctx.Platform.GetClipboard().SetClipboard(util.StripANSI(s))
}

///////////////////////////////////////////////////////////////////////////
// QS - HSF (Heading / Speed-Mach / Free-text) Handlers

func qsFDBDataAcceptMsg(trk *sim.Track) []string {
	if trk == nil || trk.FlightPlan == nil {
		return nil
	}
	return []string{"ACCEPT", "FDB DATA", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID}
}

func handleQSToggleHSF(ep *Scope, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.HSFHide = !state.HSFHide
	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteHeading(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteSpeed(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSDeleteAll(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set("")
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)
	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSHeading(ep *Scope, ctx *scope.Context, heading string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(heading)
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSSpeed(ep *Scope, ctx *scope.Context, speed string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.SecondaryScratchpad.Set(speed)
	if isQSFreeText(trk.FlightPlan.Scratchpad) {
		fp.Scratchpad.Set("")
	}
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

// handleQSHeadingSpeed enters a heading and a speed together, as in
// QS 330/81 (Appendix B).
func handleQSHeadingSpeed(ep *Scope, ctx *scope.Context, heading, speed string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(heading)
	fp.SecondaryScratchpad.Set(speed)
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}

func handleQSFreeText(ep *Scope, ctx *scope.Context, freeText string, trk *sim.Track) (CommandStatus, error) {
	if trk == nil || trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	}

	var fp sim.FlightPlanSpecifier
	fp.Scratchpad.Set(sim.QSFreeTextIndicator + freeText)
	fp.SecondaryScratchpad.Set("")
	ep.modifyFlightPlan(ctx, trk, fp)

	return CommandStatus{feedbackArea: qsFDBDataAcceptMsg(trk)}, nil
}
