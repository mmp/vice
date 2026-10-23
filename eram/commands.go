// eram/commands.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"net/rpc"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

// What the commands do to flight plans and tracks: the calls they make on
// the client, and the ERAM errors their failures are reported as.

func (ep *Scope) deleteFlightPlan(ctx *scope.Context, trk sim.Track) {
	ctx.Client.DeleteFlightPlan(trk.FlightPlan.ACID, func(err error) {
		if err != nil {
			ep.displayError(err, ctx)
			return
		}
	})
}

func (ep *Scope) runAircraftCommands(ctx *scope.Context, callsign av.ADSBCallsign, cmds string) {
	ep.targetGenLastCallsign = callsign

	ctx.Client.RunAircraftCommands(client.AircraftCommandRequest{
		Callsign: callsign,
		Commands: cmds,
	}, func(err error, remaining string) {
		if err != nil {
			ep.displayError(err, ctx)
		}
	})
}

// Mainly used for ERAM assigned/ interm alts. May be used for actually changing routes.
func (ep *Scope) modifyFlightPlan(ctx *scope.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) {
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

	ctx.Client.ModifyFlightPlan(trk.FlightPlan.ACID, spec,
		func(err error) {
			if err != nil {
				ep.displayError(err, ctx)
			}
		})
	// Send aircraft commands if an ERAM command is entered
	if ep.DisableERAMtoRadio {
		return
	}
	// Only one of the altitudes will be set.
	if alt := spec.AssignedAltitude.Value + spec.InterimAlt.Value + spec.LocalInterimAlt.Value; alt > 0 {
		var cmd string
		state := ep.TrackState[trk.ADSBCallsign]
		if alt > int(state.Track.TransponderAltitude) {
			cmd = "C" + fmt.Sprint(alt/100)
		} else {
			cmd = "D" + fmt.Sprint(alt/100)
		}
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
	}
	// QS speeds are stored in the form of the speed commands.
	if spec.SecondaryScratchpad.IsSet && spec.SecondaryScratchpad.Value != "" {
		ep.runAircraftCommands(ctx, trk.ADSBCallsign, spec.SecondaryScratchpad.Value)
	}
}

func (ep *Scope) acceptHandoff(ctx *scope.Context, acid sim.ACID) {
	ctx.Client.AcceptHandoff(acid,
		func(err error) { ep.displayError(err, ctx) })
}

func (ep *Scope) recallHandoff(ctx *scope.Context, acid sim.ACID) {
	ctx.Client.CancelHandoff(acid,
		func(err error) { ep.displayError(err, ctx) })
}

func (ep *Scope) getQULines(ctx *scope.Context, acid sim.ACID, minutes int) {
	ctx.Client.SendRouteCoordinates(acid, minutes, func(err error) {
		if err != nil {
			ep.displayError(err, ctx)
		}
	})
}

func (ep *Scope) tgtGenDefaultCallsign(ctx *scope.Context) av.ADSBCallsign {
	if cs := ctx.Client.LastTTSCallsign(); cs != "" {
		// If TTS is active, return the last TTS transmitter.
		return cs
	}
	// Otherwise the most recent one the user selected.
	return ep.targetGenLastCallsign
}

func (ep *Scope) flightPlanDirect(ctx *scope.Context, trk *sim.Track, fixes []string) error {
	if trk.FlightPlan == nil {
		return ErrIllegalACID
	}

	issueDirect := !ep.DisableERAMtoRadio
	if issueDirect {
		ep.targetGenLastCallsign = trk.ADSBCallsign
	}
	ctx.Client.FlightPlanDirect(trk.FlightPlan.ACID, fixes, issueDirect,
		func(err error) {
			if err != nil {
				ep.displayError(err, ctx)
			} else {
				ep.feedbackArea.Success("ACCEPT REROUTE " + string(trk.ADSBCallsign) +
					"/" + trk.FlightPlan.CID)
			}
		})
	return nil
}

func (ep *Scope) handoffTrack(ctx *scope.Context, acid sim.ACID, controller string) error {
	control, err := ep.lookupControllerForID(ctx, controller)
	if err != nil {
		ep.displayError(err, ctx)
		return err
	}
	if control == nil {
		return ErrIllegalPosition
	}

	ctx.Client.HandoffTrack(acid, control.PositionId(),
		func(err error) { ep.displayError(err, ctx) })

	return nil
}

func (ep *Scope) pointOutTrack(ctx *scope.Context, trk *sim.Track, sector string) error {
	if trk.IsUnassociated() {
		return ErrIllegalACID
	} else if control, err := ep.lookupControllerForID(ctx, sector); err != nil {
		return err
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

// acknowledgePointOuts acknowledges the track's point outs to the user from
// the given sectors or, if none are given, its only one (5.1.5.11.2).
func (ep *Scope) acknowledgePointOuts(ctx *scope.Context, trk *sim.Track, sectors []string) error {
	from, err := ep.pointOutsToAcknowledge(ctx, trk, sectors)
	if err != nil {
		return err
	}
	for _, f := range from {
		ep.acknowledgePointOut(ctx, trk, f)
	}
	return nil
}

// pointOutsToAcknowledge returns the initiating sectors of the track's point
// outs to the user that QP A acknowledges: the given sectors or, if none are
// given, the sector of its only point out to the user (5.1.5.11.2).
func (ep *Scope) pointOutsToAcknowledge(ctx *scope.Context, trk *sim.Track, sectors []string) ([]sim.ControlPosition, error) {
	if trk.IsUnassociated() {
		return nil, ErrIllegalACID
	}

	fp := trk.FlightPlan
	inbound := ctx.InboundPointOuts(fp)
	if slices.ContainsFunc(inbound, func(po sim.PointOut) bool { return ctx.UserControlsPosition(po.FromController) }) {
		return nil, NewError("REJECT - %s AMBIGUOUS PO", ctx.UserPrimaryPosition())
	}
	if len(sectors) == 0 {
		switch len(inbound) {
		case 0:
			// An acknowledged point out moves from the flight plan's pending
			// point outs to its point out history, which records only the
			// receiver.
			if slices.ContainsFunc(fp.PointOutHistory, ctx.UserControlsPosition) {
				return nil, ErrNoUnacknowledgedPointOut
			}
			return nil, ErrPointOutNotFound
		case 1:
			return []sim.ControlPosition{inbound[0].FromController}, nil
		default:
			return nil, ErrSectorIDRequired
		}
	}

	var from []sim.ControlPosition
	for _, sector := range sectors {
		ctrl, err := ep.lookupControllerForID(ctx, sector)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(inbound, func(po sim.PointOut) bool { return po.FromController == ctrl.PositionId() }) {
			return nil, ErrPointOutNotFound
		}
		from = append(from, ctrl.PositionId())
	}
	return from, nil
}

// acknowledgePointOut acknowledges the track's point out to the user from
// the sector from.
func (ep *Scope) acknowledgePointOut(ctx *scope.Context, trk *sim.Track, from sim.ControlPosition) error {
	if trk.IsUnassociated() {
		return ErrIllegalACID
	}

	acid := trk.FlightPlan.ACID
	ctx.Client.AcknowledgePointOut(acid, from,
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

// requestSuppressDatablock switches another sector's track between an FDB
// and an LDB (Appendix B, Table 2-7); the FDB a point out to the user leaves
// is suppressed.
func (ep *Scope) requestSuppressDatablock(ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil {
		return CommandStatus{}, ErrIllegalACID
	} else if ctx.UserControlsPosition(trk.FlightPlan.TrackingController) {
		// The user's own tracks always have FDBs (5.1.5.2).
		return CommandStatus{}, NewError("USER ACTION NOT ALLOWED ON A\nCONTROLLER FLIGHT\nFORCED DATA BLK %s", trk.ADSBCallsign)
	} else if len(ctx.InboundPointOuts(trk.FlightPlan)) > 0 {
		// While a point out to the user is pending, its FDB stays.
		return CommandStatus{}, ErrIllegalUserAction
	}

	state := ep.TrackState[trk.ADSBCallsign]
	state.EFDB = !state.EFDB && !state.PointOutFDBLocked
	state.PointOutFDBLocked = false
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "FORCED DATA BLK", fmt.Sprintf("%s/%s", trk.ADSBCallsign, trk.FlightPlan.CID)},
	}, nil
}

func (ep *Scope) tryGetClosestTrack(ctx *scope.Context, mousePosition [2]float32, transforms scope.Transformations) (*sim.Track, float32) {
	var trk *sim.Track
	distance := float32(20)

	for i := range ep.visibleTracks {
		t := &ep.visibleTracks[i]
		if !ep.targetVisible(ctx, *t) {
			continue // a target the altitude limits filter hides can't be slewed
		}
		pw := transforms.WindowFromLatLongP(ep.TrackState[t.ADSBCallsign].Track.Location)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			trk = t
			distance = dist
		}
	}

	return trk, distance
}

func (ep *Scope) lookupControllerForID(ctx *scope.Context, controller string) (*av.Controller, error) {
	// Look at the length of the controller string passed in. If it's one character, ERAM would have to find which controller it goes to.
	// That is not here yet, so return an error.
	for _, control := range ctx.Client.State.Controllers {
		switch len(controller) {
		case 1: // Cannot do anything with single characters in ERAM yet. TODO: fix pairs
			return nil, ErrSectorNotActive
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
			return nil, ErrSectorNotActive
		}
	}
	return nil, ErrSectorNotActive
}

///////////////////////////////////////////////////////////////////////////
// Errors

type Error struct {
	error
}

func NewError(msg string, args ...any) *Error {
	return &Error{fmt.Errorf(msg, args...)}
}

var ( // TODO: Get actual error messages for this
	ErrCommandFormat         = NewError("FORMAT")
	ErrAmbiguousACID         = NewError("AMB ACID")
	ErrIllegalACID           = NewError("ILL CID")
	ErrIllegalPosition       = NewError("ILLEGAL POSITION")
	ErrIllegalValue          = NewError("ILLEGAL VALUE")
	ErrIllegalAirport        = NewError("ILLEGAL AIRPORT")
	ErrIllegalUserAction     = NewError("ILLEGAL USER ACTION")
	ErrInvalidAltitudeLimits = NewError("INVALID ALTITUDE LIMITS")
	ErrMapUnavailable        = NewError("MAP UNAVAILABLE")
	ErrMessageTooLong        = NewError("MESSAGE TOO LONG")
	ErrSectorNotActive       = NewError("SECTOR NOT ACTIVE")
)

// SG rejects (4.2.14)
var (
	ErrSuppressionGroupNotFound    = NewError("REJECT - GROUP NOT FOUND")
	ErrNoSuppressionGroupAvailable = NewError("REJECT - NO GROUP ID AVAILABLE")
)

// QP A rejects (5.1.5.11.2)
var (
	ErrNoUnacknowledgedPointOut = NewError("REJECT - NO UNACKNOWLEDGED PO")
	ErrPointOutNotFound         = NewError("REJECT - POINT OUT NOT FOUND")
	ErrSectorIDRequired         = NewError("REJECT - SECTOR ID REQUIRED")
)

var eramErrorRemap = map[error]*Error{
	av.ErrBadPoolSpecifier:           ErrIllegalValue,
	av.ErrInvalidAltitude:            ErrIllegalValue,
	av.ErrInvalidController:          ErrIllegalPosition,
	av.ErrInvalidFacility:            ErrIllegalACID,
	av.ErrInvalidHeading:             ErrIllegalValue,
	av.ErrNoAircraftForCallsign:      ErrIllegalACID,
	av.ErrNoController:               ErrSectorNotActive,
	av.ErrNoFlightPlan:               ErrIllegalACID,
	av.ErrNoMatchingFix:              ErrIllegalValue,
	av.ErrNoMoreAvailableSquawkCodes: ErrIllegalValue,
	av.ErrNoValidDepartureFound:      ErrIllegalUserAction,
	av.ErrNotBeingHandedOffToMe:      ErrIllegalUserAction,
	av.ErrNotPointedOutByMe:          ErrIllegalUserAction,
	av.ErrNotPointedOutToMe:          ErrPointOutNotFound,
	av.ErrOtherControllerHasTrack:    ErrIllegalUserAction,
	av.ErrUnknownAirport:             ErrIllegalAirport,
	av.ErrUnknownRunway:              ErrIllegalValue,

	nav.ErrClearedForUnexpectedApproach: ErrIllegalValue,
	nav.ErrFixIsTooFarAway:              ErrIllegalValue,
	nav.ErrFixNotInRoute:                ErrIllegalValue,
	nav.ErrInvalidApproach:              ErrIllegalValue,
	nav.ErrInvalidFix:                   ErrIllegalValue,
	nav.ErrNotClearedForApproach:        ErrIllegalValue,
	nav.ErrNotFlyingRoute:               ErrIllegalValue,
	nav.ErrUnableCommand:                ErrIllegalValue,
	nav.ErrUnknownApproach:              ErrIllegalValue,

	sim.ErrATPADisabled:                    ErrIllegalUserAction,
	sim.ErrAircraftAlreadyReleased:         ErrIllegalUserAction,
	sim.ErrBeaconMismatch:                  ErrIllegalValue,
	sim.ErrDuplicateACID:                   ErrIllegalACID,
	sim.ErrDuplicateBeacon:                 ErrIllegalValue,
	sim.ErrIllegalACID:                     ErrIllegalACID,
	sim.ErrIllegalACType:                   ErrIllegalValue,
	sim.ErrIllegalATIS:                     ErrIllegalValue,
	sim.ErrIllegalBeaconCode:               ErrIllegalValue,
	sim.ErrIllegalFunction:                 ErrIllegalUserAction,
	sim.ErrIllegalLine:                     ErrIllegalValue,
	sim.ErrIllegalPosition:                 ErrIllegalPosition,
	sim.ErrIllegalScratchpad:               ErrIllegalValue,
	sim.ErrInvalidAbbreviatedFP:            ErrCommandFormat,
	sim.ErrInvalidCommandSyntax:            ErrCommandFormat,
	sim.ErrInvalidDepartureController:      ErrIllegalUserAction,
	sim.ErrInvalidRestrictionAreaIndex:     ErrIllegalValue,
	sim.ErrInvalidVolumeId:                 ErrIllegalUserAction,
	sim.ErrNoACType:                        ErrIllegalValue,
	sim.ErrNoMatchingFlight:                ErrIllegalACID,
	sim.ErrNoMatchingFlightPlan:            ErrIllegalACID,
	sim.ErrNoRecentCommand:                 ErrIllegalUserAction,
	sim.ErrNoScratchpad:                    ErrIllegalValue,
	sim.ErrNoVFRAircraftForFlightFollowing: ErrIllegalACID,
	sim.ErrTCPAlreadyConsolidated:          ErrIllegalUserAction,
	sim.ErrTCPNotConsolidated:              ErrIllegalUserAction,
	sim.ErrTCWIsConsolidated:               ErrIllegalPosition,
	sim.ErrTCWNotFound:                     ErrIllegalPosition,
	sim.ErrTCWNotVacant:                    ErrIllegalPosition,
	sim.ErrTooManyRestrictionAreas:         ErrIllegalUserAction,
	sim.ErrTrackIsActive:                   ErrIllegalUserAction,
	sim.ErrIllegalTrackLocalFP:             ErrIllegalUserAction,
	sim.ErrTrackIsBeingHandedOff:           ErrIllegalUserAction,
	sim.ErrTrackIsNotActive:                ErrIllegalACID,
	sim.ErrUnknownAircraftType:             ErrIllegalValue,
	sim.ErrUnknownController:               ErrIllegalPosition,
	sim.ErrUnknownControllerFacility:       ErrIllegalPosition,
	sim.ErrFDAMIllegalArea:                 ErrIllegalUserAction,
	sim.ErrFDAMNoRegions:                   ErrIllegalUserAction,
	sim.ErrFDAMProcessingOff:               ErrIllegalUserAction,
	sim.ErrVolumeDisabled:                  ErrIllegalUserAction,
	sim.ErrRunwayNot25nm:                   ErrIllegalUserAction,
}

func GetError(e error, lg *log.Logger) *Error {
	if se, ok := e.(*Error); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = client.TryDecodeError(e)
	}

	if se, ok := eramErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetError", e)
	return ErrCommandFormat
}
