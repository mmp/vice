// pkg/sim/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"

	av "github.com/mmp/vice/pkg/aviation"
)

var (
	ErrControllerAlreadySignedIn  = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName           = errors.New("A sim with that name already exists")
	ErrIllegalACID                = errors.New("Illegal ACID")
	ErrIllegalACType              = errors.New("Illegal aircraft type")
	ErrIllegalScratchpad          = errors.New("Illegal scratchpad")
	ErrInvalidAbbreviatedFP       = errors.New("Invalid abbreviated flight plan")
	ErrInvalidCommandSyntax       = errors.New("Invalid command syntax")
	ErrInvalidControllerToken     = errors.New("Invalid controller token")
	ErrInvalidPassword            = errors.New("Invalid password")
	ErrNoCoordinationFix          = errors.New("No coordination fix found")
	ErrNoMatchingFlight           = errors.New("No matching flight")
	ErrNoMoreAvailableSquawkCodes = errors.New("No more available squawk codes")
	ErrNoNamedSim                 = errors.New("No Sim with that name")
	ErrNoSimForControllerToken    = errors.New("No Sim running for controller token")
	ErrNotLaunchController        = errors.New("Not signed in as the launch controller")
	ErrRPCTimeout                 = errors.New("RPC call timed out")
	ErrRPCVersionMismatch         = errors.New("Client and server RPC versions don't match")
	ErrRestoringSavedState        = errors.New("Errors during state restoration")
	ErrServerDisconnected         = errors.New("Server disconnected")
	ErrUnknownFacility            = errors.New("Unknown facility (ARTCC/TRACON)")
	ErrUnknownControllerFacility  = errors.New("Unknown controller facility")
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

	ErrControllerAlreadySignedIn.Error():  ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():           ErrDuplicateSimName,
	ErrIllegalACID.Error():                ErrIllegalACID,
	ErrIllegalACType.Error():              ErrIllegalACType,
	ErrIllegalScratchpad.Error():          ErrIllegalScratchpad,
	ErrInvalidAbbreviatedFP.Error():       ErrInvalidAbbreviatedFP,
	ErrInvalidCommandSyntax.Error():       ErrInvalidCommandSyntax,
	ErrInvalidControllerToken.Error():     ErrInvalidControllerToken,
	ErrInvalidPassword.Error():            ErrInvalidPassword,
	ErrNoCoordinationFix.Error():          ErrNoCoordinationFix,
	ErrNoMatchingFlight.Error():           ErrNoMatchingFlight,
	ErrNoMoreAvailableSquawkCodes.Error(): ErrNoMoreAvailableSquawkCodes,
	ErrNoNamedSim.Error():                 ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():    ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                 ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():         ErrRPCVersionMismatch,
	ErrRestoringSavedState.Error():        ErrRestoringSavedState,
	ErrServerDisconnected.Error():         ErrServerDisconnected,
	ErrUnknownFacility.Error():            ErrUnknownFacility,
	ErrUnknownControllerFacility.Error():  ErrUnknownControllerFacility,
}

func TryDecodeError(e error) error {
	if err, ok := errorStringToError[e.Error()]; ok {
		return err
	}
	return e
}
