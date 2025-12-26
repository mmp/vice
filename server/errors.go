// server/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/sim"
)

var (
	ErrControllerAlreadySignedIn = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName          = errors.New("A sim with that name already exists")
	ErrInvalidCommandSyntax      = errors.New("Invalid command syntax")
	ErrInvalidControllerToken    = errors.New("Invalid controller token")
	ErrInvalidPassword           = errors.New("Invalid password")
	ErrInvalidSimConfiguration   = errors.New("Invalid SimConfiguration")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrServerDisconnected        = errors.New("Server disconnected")
	ErrTCWAlreadyOccupied        = errors.New("TCW is already occupied")
	ErrWeatherUnavailable        = errors.New("Unable to reach weather server")
)

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
	sim.ErrAircraftAlreadyReleased.Error():         sim.ErrAircraftAlreadyReleased,
	sim.ErrBeaconMismatch.Error():                  sim.ErrBeaconMismatch,
	sim.ErrControllerAlreadySignedIn.Error():       sim.ErrControllerAlreadySignedIn,
	sim.ErrDuplicateACID.Error():                   sim.ErrDuplicateACID,
	sim.ErrDuplicateBeacon.Error():                 sim.ErrDuplicateBeacon,
	sim.ErrIllegalACID.Error():                     sim.ErrIllegalACID,
	sim.ErrIllegalACType.Error():                   sim.ErrIllegalACType,
	sim.ErrIllegalBeaconCode.Error():               sim.ErrIllegalBeaconCode,
	sim.ErrIllegalFunction.Error():                 sim.ErrIllegalFunction,
	sim.ErrIllegalScratchpad.Error():               sim.ErrIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP.Error():            sim.ErrInvalidAbbreviatedFP,
	sim.ErrInvalidDepartureController.Error():      sim.ErrInvalidDepartureController,
	sim.ErrInvalidRestrictionAreaIndex.Error():     sim.ErrInvalidRestrictionAreaIndex,
	sim.ErrInvalidVolumeId.Error():                 sim.ErrInvalidVolumeId,
	sim.ErrNoMatchingFlight.Error():                sim.ErrNoMatchingFlight,
	sim.ErrNoMatchingFlightPlan.Error():            sim.ErrNoMatchingFlightPlan,
	sim.ErrNoVFRAircraftForFlightFollowing.Error(): sim.ErrNoVFRAircraftForFlightFollowing,
	sim.ErrNotLaunchController.Error():             sim.ErrNotLaunchController,
	sim.ErrTCPAlreadyConsolidated.Error():          sim.ErrTCPAlreadyConsolidated,
	sim.ErrTCPNotConsolidated.Error():              sim.ErrTCPNotConsolidated,
	sim.ErrTCWIsConsolidated.Error():               sim.ErrTCWIsConsolidated,
	sim.ErrTCWNotFound.Error():                     sim.ErrTCWNotFound,
	sim.ErrTCWNotVacant.Error():                    sim.ErrTCWNotVacant,
	sim.ErrTooManyRestrictionAreas.Error():         sim.ErrTooManyRestrictionAreas,
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

	ErrControllerAlreadySignedIn.Error(): ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():          ErrDuplicateSimName,
	ErrInvalidCommandSyntax.Error():      ErrInvalidCommandSyntax,
	ErrInvalidControllerToken.Error():    ErrInvalidControllerToken,
	ErrInvalidPassword.Error():           ErrInvalidPassword,
	ErrInvalidSimConfiguration.Error():   ErrInvalidSimConfiguration,
	ErrNoNamedSim.Error():                ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():   ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():        ErrRPCVersionMismatch,
	ErrServerDisconnected.Error():        ErrServerDisconnected,
	ErrTCWAlreadyOccupied.Error():        ErrTCWAlreadyOccupied,
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

func TryDecodeErrorString(s string) error {
	if err, ok := errorStringToError[s]; ok {
		return err
	}
	return nil
}
