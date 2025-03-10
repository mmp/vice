// pkg/server/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/sim"
)

var (
	ErrControllerAlreadySignedIn = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName          = errors.New("A sim with that name already exists")
	ErrInvalidCommandSyntax      = errors.New("Invalid command syntax")
	ErrInvalidPassword           = errors.New("Invalid password")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrServerDisconnected        = errors.New("Server disconnected")
)

var errorStringToError = map[string]error{
	av.ErrClearedForUnexpectedApproach.Error(): av.ErrClearedForUnexpectedApproach,
	av.ErrFixNotInRoute.Error():                av.ErrFixNotInRoute,
	av.ErrInvalidAltitude.Error():              av.ErrInvalidAltitude,
	av.ErrInvalidApproach.Error():              av.ErrInvalidApproach,
	av.ErrInvalidController.Error():            av.ErrInvalidController,
	av.ErrInvalidFacility.Error():              av.ErrInvalidFacility,
	av.ErrInvalidHeading.Error():               av.ErrInvalidHeading,
	av.ErrNoAircraftForCallsign.Error():        av.ErrNoAircraftForCallsign,
	av.ErrNoController.Error():                 av.ErrNoController,
	av.ErrNoCoordinationFix.Error():            av.ErrNoCoordinationFix,
	av.ErrNoERAMFacility.Error():               av.ErrNoERAMFacility,
	av.ErrNoFlightPlan.Error():                 av.ErrNoFlightPlan,
	av.ErrNoSTARSFacility.Error():              av.ErrNoSTARSFacility,
	av.ErrNoValidArrivalFound.Error():          av.ErrNoValidArrivalFound,
	av.ErrNotBeingHandedOffToMe.Error():        av.ErrNotBeingHandedOffToMe,
	av.ErrNotClearedForApproach.Error():        av.ErrNotClearedForApproach,
	av.ErrNotFlyingRoute.Error():               av.ErrNotFlyingRoute,
	av.ErrNotPointedOutToMe.Error():            av.ErrNotPointedOutToMe,
	av.ErrOtherControllerHasTrack.Error():      av.ErrOtherControllerHasTrack,
	av.ErrUnableCommand.Error():                av.ErrUnableCommand,
	av.ErrUnknownAircraftType.Error():          av.ErrUnknownAircraftType,
	av.ErrUnknownAirport.Error():               av.ErrUnknownAirport,
	av.ErrUnknownApproach.Error():              av.ErrUnknownApproach,
	av.ErrUnknownRunway.Error():                av.ErrUnknownRunway,

	sim.ErrAircraftAlreadyReleased.Error():     sim.ErrAircraftAlreadyReleased,
	sim.ErrBeaconMismatch.Error():              sim.ErrBeaconMismatch,
	sim.ErrControllerAlreadySignedIn.Error():   sim.ErrControllerAlreadySignedIn,
	sim.ErrIllegalACID.Error():                 sim.ErrIllegalACID,
	sim.ErrIllegalACType.Error():               sim.ErrIllegalACType,
	sim.ErrIllegalFunction.Error():             sim.ErrIllegalFunction,
	sim.ErrIllegalScratchpad.Error():           sim.ErrIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP.Error():        sim.ErrInvalidAbbreviatedFP,
	sim.ErrInvalidControllerToken.Error():      sim.ErrInvalidControllerToken,
	sim.ErrInvalidDepartureController.Error():  sim.ErrInvalidDepartureController,
	sim.ErrInvalidRestrictionAreaIndex.Error(): sim.ErrInvalidRestrictionAreaIndex,
	sim.ErrNoMatchingFlight.Error():            sim.ErrNoMatchingFlight,
	sim.ErrNotLaunchController.Error():         sim.ErrNotLaunchController,
	sim.ErrTooManyRestrictionAreas.Error():     sim.ErrTooManyRestrictionAreas,
	sim.ErrUnknownController.Error():           sim.ErrUnknownController,
	sim.ErrUnknownControllerFacility.Error():   sim.ErrUnknownControllerFacility,
	sim.ErrViolatedAirspace.Error():            sim.ErrViolatedAirspace,
	sim.ErrVFRSimTookTooLong.Error():           sim.ErrVFRSimTookTooLong,

	ErrControllerAlreadySignedIn.Error(): ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():          ErrDuplicateSimName,
	ErrInvalidCommandSyntax.Error():      ErrInvalidCommandSyntax,
	ErrInvalidPassword.Error():           ErrInvalidPassword,
	ErrNoNamedSim.Error():                ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():   ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():        ErrRPCVersionMismatch,
	ErrServerDisconnected.Error():        ErrServerDisconnected,
}

func TryDecodeError(e error) error {
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
