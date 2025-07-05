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
	ErrInvalidControllerToken    = errors.New("Invalid controller token")
	ErrInvalidPassword           = errors.New("Invalid password")
	ErrInvalidSSimConfiguration  = errors.New("Invalid SimConfiguration")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrServerDisconnected        = errors.New("Server disconnected")
)

var errorStringToError = map[string]error{
	av.ErrInvalidAltitude.Error():         av.ErrInvalidAltitude,
	av.ErrInvalidController.Error():       av.ErrInvalidController,
	av.ErrInvalidFacility.Error():         av.ErrInvalidFacility,
	av.ErrInvalidHeading.Error():          av.ErrInvalidHeading,
	av.ErrNoAircraftForCallsign.Error():   av.ErrNoAircraftForCallsign,
	av.ErrNoController.Error():            av.ErrNoController,
	av.ErrNoCoordinationFix.Error():       av.ErrNoCoordinationFix,
	av.ErrNoERAMFacility.Error():          av.ErrNoERAMFacility,
	av.ErrNoFlightPlan.Error():            av.ErrNoFlightPlan,
	av.ErrNoSTARSFacility.Error():         av.ErrNoSTARSFacility,
	av.ErrNoValidArrivalFound.Error():     av.ErrNoValidArrivalFound,
	av.ErrNotBeingHandedOffToMe.Error():   av.ErrNotBeingHandedOffToMe,
	av.ErrNotPointedOutToMe.Error():       av.ErrNotPointedOutToMe,
	av.ErrOtherControllerHasTrack.Error(): av.ErrOtherControllerHasTrack,
	av.ErrUnknownAirport.Error():          av.ErrUnknownAirport,
	av.ErrUnknownRunway.Error():           av.ErrUnknownRunway,

	sim.ErrAircraftAlreadyReleased.Error():      sim.ErrAircraftAlreadyReleased,
	sim.ErrBeaconMismatch.Error():               sim.ErrBeaconMismatch,
	sim.ErrClearedForUnexpectedApproach.Error(): sim.ErrClearedForUnexpectedApproach,
	sim.ErrControllerAlreadySignedIn.Error():    sim.ErrControllerAlreadySignedIn,
	sim.ErrDuplicateACID.Error():                sim.ErrDuplicateACID,
	sim.ErrDuplicateBeacon.Error():              sim.ErrDuplicateBeacon,
	sim.ErrFixNotInRoute.Error():                sim.ErrFixNotInRoute,
	sim.ErrIllegalACID.Error():                  sim.ErrIllegalACID,
	sim.ErrIllegalACType.Error():                sim.ErrIllegalACType,
	sim.ErrIllegalBeaconCode.Error():            sim.ErrIllegalBeaconCode,
	sim.ErrIllegalFunction.Error():              sim.ErrIllegalFunction,
	sim.ErrIllegalScratchpad.Error():            sim.ErrIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP.Error():         sim.ErrInvalidAbbreviatedFP,
	sim.ErrInvalidApproach.Error():              sim.ErrInvalidApproach,
	sim.ErrInvalidDepartureController.Error():   sim.ErrInvalidDepartureController,
	sim.ErrInvalidFix.Error():                   sim.ErrInvalidFix,
	sim.ErrInvalidRestrictionAreaIndex.Error():  sim.ErrInvalidRestrictionAreaIndex,
	sim.ErrNoMatchingFlight.Error():             sim.ErrNoMatchingFlight,
	sim.ErrNoMoreListIndices.Error():            sim.ErrNoMoreListIndices,
	sim.ErrNotClearedForApproach.Error():        sim.ErrNotClearedForApproach,
	sim.ErrNotFlyingRoute.Error():               sim.ErrNotFlyingRoute,
	sim.ErrNotLaunchController.Error():          sim.ErrNotLaunchController,
	sim.ErrNotFlowController.Error():            sim.ErrNotFlowController,
	sim.ErrTooManyRestrictionAreas.Error():      sim.ErrTooManyRestrictionAreas,
	sim.ErrTrackIsActive.Error():                sim.ErrTrackIsActive,
	sim.ErrTrackIsBeingHandedOff.Error():        sim.ErrTrackIsBeingHandedOff,
	sim.ErrTrackIsNotActive.Error():             sim.ErrTrackIsNotActive,
	sim.ErrUnableCommand.Error():                sim.ErrUnableCommand,
	sim.ErrUnknownAircraftType.Error():          sim.ErrUnknownAircraftType,
	sim.ErrUnknownApproach.Error():              sim.ErrUnknownApproach,
	sim.ErrUnknownController.Error():            sim.ErrUnknownController,
	sim.ErrUnknownControllerFacility.Error():    sim.ErrUnknownControllerFacility,
	sim.ErrVFRSimTookTooLong.Error():            sim.ErrVFRSimTookTooLong,
	sim.ErrViolatedAirspace.Error():             sim.ErrViolatedAirspace,

	ErrControllerAlreadySignedIn.Error(): ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():          ErrDuplicateSimName,
	ErrInvalidCommandSyntax.Error():      ErrInvalidCommandSyntax,
	ErrInvalidControllerToken.Error():    ErrInvalidControllerToken,
	ErrInvalidPassword.Error():           ErrInvalidPassword,
	ErrInvalidSSimConfiguration.Error():  ErrInvalidSSimConfiguration,
	ErrNoNamedSim.Error():                ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():   ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():        ErrRPCVersionMismatch,
	ErrServerDisconnected.Error():        ErrServerDisconnected,
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
