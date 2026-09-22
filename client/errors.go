// client/errors.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"errors"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

// Errors lose their type when they go over net/rpc, so the client maps the
// message strings it gets back to the original error values.
var errorStringToError = map[string]error{
	av.ErrBadPoolSpecifier.Error():           av.ErrBadPoolSpecifier,
	av.ErrInvalidAltitude.Error():            av.ErrInvalidAltitude,
	av.ErrInvalidController.Error():          av.ErrInvalidController,
	av.ErrInvalidFacility.Error():            av.ErrInvalidFacility,
	av.ErrInvalidHeading.Error():             av.ErrInvalidHeading,
	av.ErrNoAircraftForCallsign.Error():      av.ErrNoAircraftForCallsign,
	av.ErrNoController.Error():               av.ErrNoController,
	av.ErrNoCoordinationFix.Error():          av.ErrNoCoordinationFix,
	av.ErrNoERAMFacility.Error():             av.ErrNoERAMFacility,
	av.ErrNoFlightPlan.Error():               av.ErrNoFlightPlan,
	av.ErrNoMatchingFix.Error():              av.ErrNoMatchingFix,
	av.ErrNoMoreAvailableSquawkCodes.Error(): av.ErrNoMoreAvailableSquawkCodes,
	av.ErrNoSTARSFacility.Error():            av.ErrNoSTARSFacility,
	av.ErrNoValidArrivalFound.Error():        av.ErrNoValidArrivalFound,
	av.ErrNoValidDepartureFound.Error():      av.ErrNoValidDepartureFound,
	av.ErrNotBeingHandedOffToMe.Error():      av.ErrNotBeingHandedOffToMe,
	av.ErrNotPointedOutByMe.Error():          av.ErrNotPointedOutByMe,
	av.ErrNotPointedOutToMe.Error():          av.ErrNotPointedOutToMe,
	av.ErrOtherControllerHasTrack.Error():    av.ErrOtherControllerHasTrack,
	av.ErrUnknownAirport.Error():             av.ErrUnknownAirport,
	av.ErrUnknownRunway.Error():              av.ErrUnknownRunway,

	nav.ErrClearedForUnexpectedApproach.Error(): nav.ErrClearedForUnexpectedApproach,
	nav.ErrFixIsTooFarAway.Error():              nav.ErrFixIsTooFarAway,
	nav.ErrFixNotInRoute.Error():                nav.ErrFixNotInRoute,
	nav.ErrInvalidApproach.Error():              nav.ErrInvalidApproach,
	nav.ErrInvalidFix.Error():                   nav.ErrInvalidFix,
	nav.ErrNotClearedForApproach.Error():        nav.ErrNotClearedForApproach,
	nav.ErrNotFlyingRoute.Error():               nav.ErrNotFlyingRoute,
	nav.ErrUnableCommand.Error():                nav.ErrUnableCommand,
	nav.ErrUnknownApproach.Error():              nav.ErrUnknownApproach,

	sim.ErrATPADisabled.Error():                    sim.ErrATPADisabled,
	sim.ErrFDAMIllegalArea.Error():                 sim.ErrFDAMIllegalArea,
	sim.ErrFDAMNoRegions.Error():                   sim.ErrFDAMNoRegions,
	sim.ErrFDAMProcessingOff.Error():               sim.ErrFDAMProcessingOff,
	sim.ErrAircraftAlreadyReleased.Error():         sim.ErrAircraftAlreadyReleased,
	sim.ErrBeaconMismatch.Error():                  sim.ErrBeaconMismatch,
	sim.ErrControllerAlreadySignedIn.Error():       sim.ErrControllerAlreadySignedIn,
	sim.ErrDuplicateACID.Error():                   sim.ErrDuplicateACID,
	sim.ErrDuplicateBeacon.Error():                 sim.ErrDuplicateBeacon,
	sim.ErrIllegalACID.Error():                     sim.ErrIllegalACID,
	sim.ErrIllegalACType.Error():                   sim.ErrIllegalACType,
	sim.ErrIllegalATIS.Error():                     sim.ErrIllegalATIS,
	sim.ErrIllegalBeaconCode.Error():               sim.ErrIllegalBeaconCode,
	sim.ErrIllegalFunction.Error():                 sim.ErrIllegalFunction,
	sim.ErrIllegalLine.Error():                     sim.ErrIllegalLine,
	sim.ErrIllegalPosition.Error():                 sim.ErrIllegalPosition,
	sim.ErrIllegalTrackLocalFP.Error():             sim.ErrIllegalTrackLocalFP,
	sim.ErrIllegalScratchpad.Error():               sim.ErrIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP.Error():            sim.ErrInvalidAbbreviatedFP,
	sim.ErrInvalidCommandSyntax.Error():            sim.ErrInvalidCommandSyntax,
	sim.ErrInvalidDepartureController.Error():      sim.ErrInvalidDepartureController,
	sim.ErrInvalidRestrictionAreaIndex.Error():     sim.ErrInvalidRestrictionAreaIndex,
	sim.ErrInvalidVolumeId.Error():                 sim.ErrInvalidVolumeId,
	sim.ErrNoACType.Error():                        sim.ErrNoACType,
	sim.ErrNoMatchingFlight.Error():                sim.ErrNoMatchingFlight,
	sim.ErrNoMatchingFlightPlan.Error():            sim.ErrNoMatchingFlightPlan,
	sim.ErrNoRecentCommand.Error():                 sim.ErrNoRecentCommand,
	sim.ErrNoScratchpad.Error():                    sim.ErrNoScratchpad,
	sim.ErrNoVFRAircraftForFlightFollowing.Error(): sim.ErrNoVFRAircraftForFlightFollowing,
	sim.ErrTCPAlreadyConsolidated.Error():          sim.ErrTCPAlreadyConsolidated,
	sim.ErrTCPNotConsolidated.Error():              sim.ErrTCPNotConsolidated,
	sim.ErrTCWIsConsolidated.Error():               sim.ErrTCWIsConsolidated,
	sim.ErrTCWNotFound.Error():                     sim.ErrTCWNotFound,
	sim.ErrTCWNotVacant.Error():                    sim.ErrTCWNotVacant,
	sim.ErrTooManyRestrictionAreas.Error():         sim.ErrTooManyRestrictionAreas,
	sim.ErrTrackHasActivePointOut.Error():          sim.ErrTrackHasActivePointOut,
	sim.ErrTrackIsActive.Error():                   sim.ErrTrackIsActive,
	sim.ErrTrackIsBeingHandedOff.Error():           sim.ErrTrackIsBeingHandedOff,
	sim.ErrTrackIsNotActive.Error():                sim.ErrTrackIsNotActive,
	sim.ErrUnknownAircraftType.Error():             sim.ErrUnknownAircraftType,
	sim.ErrUnknownController.Error():               sim.ErrUnknownController,
	sim.ErrUnknownControllerFacility.Error():       sim.ErrUnknownControllerFacility,
	sim.ErrVFRSimTookTooLong.Error():               sim.ErrVFRSimTookTooLong,
	sim.ErrViolatedAirspace.Error():                sim.ErrViolatedAirspace,
	sim.ErrVolumeDisabled.Error():                  sim.ErrVolumeDisabled,
	sim.ErrVolumeNot25nm.Error():                   sim.ErrVolumeNot25nm,

	server.ErrControllerAlreadySignedIn.Error(): server.ErrControllerAlreadySignedIn,
	server.ErrDuplicateSimName.Error():          server.ErrDuplicateSimName,
	server.ErrInvalidControllerToken.Error():    server.ErrInvalidControllerToken,
	server.ErrInvalidPassword.Error():           server.ErrInvalidPassword,
	server.ErrInvalidSimConfiguration.Error():   server.ErrInvalidSimConfiguration,
	server.ErrInvalidTrafficSource.Error():      server.ErrInvalidTrafficSource,
	server.ErrNoNamedSim.Error():                server.ErrNoNamedSim,
	server.ErrNoSimForControllerToken.Error():   server.ErrNoSimForControllerToken,
	server.ErrRPCTimeout.Error():                server.ErrRPCTimeout,
	server.ErrRPCVersionMismatch.Error():        server.ErrRPCVersionMismatch,
	server.ErrServerDisconnected.Error():        server.ErrServerDisconnected,
	server.ErrTCWAlreadyOccupied.Error():        server.ErrTCWAlreadyOccupied,
}

func TryDecodeError(e error) error {
	if e == nil {
		return e
	}
	if err, ok := errorStringToError[e.Error()]; ok {
		return err
	}
	return e
}

// DecodeErrorMessage turns an error message carried in an RPC reply back into
// an error, recovering the original value when it is one we know about.
func DecodeErrorMessage(msg string) error {
	if err, ok := errorStringToError[msg]; ok {
		return err
	}
	return errors.New(msg)
}
