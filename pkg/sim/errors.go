package sim

import (
	"errors"

	av "github.com/mmp/vice/pkg/aviation"
)

var (
	ErrControllerAlreadySignedIn = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName          = errors.New("A sim with that name already exists")
	ErrInvalidCommandSyntax      = errors.New("Invalid command syntax")
	ErrInvalidControllerToken    = errors.New("Invalid controller token")
	ErrInvalidPassword           = errors.New("Invalid password")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrNotLaunchController       = errors.New("Not signed in as the launch controller")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrRestoringSavedState       = errors.New("Errors during state restoration")
)

var errorStringToError = map[string]error{
	av.ErrClearedForUnexpectedApproach.Error(): av.ErrClearedForUnexpectedApproach,
	av.ErrFixNotInRoute.Error():                av.ErrFixNotInRoute,
	av.ErrInvalidAltitude.Error():              av.ErrInvalidAltitude,
	av.ErrInvalidApproach.Error():              av.ErrInvalidApproach,
	ErrInvalidCommandSyntax.Error():            ErrInvalidCommandSyntax,
	av.ErrInvalidController.Error():            av.ErrInvalidController,
	av.ErrInvalidFacility.Error():              av.ErrInvalidFacility,
	av.ErrInvalidHeading.Error():               av.ErrInvalidHeading,
	av.ErrNoAircraftForCallsign.Error():        av.ErrNoAircraftForCallsign,
	av.ErrNoController.Error():                 av.ErrNoController,
	av.ErrNoFlightPlan.Error():                 av.ErrNoFlightPlan,
	av.ErrNoValidArrivalFound.Error():          av.ErrNoValidArrivalFound,
	av.ErrNotBeingHandedOffToMe.Error():        av.ErrNotBeingHandedOffToMe,
	av.ErrNotPointedOutToMe.Error():            av.ErrNotPointedOutToMe,
	av.ErrNotClearedForApproach.Error():        av.ErrNotClearedForApproach,
	av.ErrNotFlyingRoute.Error():               av.ErrNotFlyingRoute,
	av.ErrOtherControllerHasTrack.Error():      av.ErrOtherControllerHasTrack,
	av.ErrUnableCommand.Error():                av.ErrUnableCommand,
	av.ErrUnknownAircraftType.Error():          av.ErrUnknownAircraftType,
	av.ErrUnknownAirport.Error():               av.ErrUnknownAirport,
	av.ErrUnknownApproach.Error():              av.ErrUnknownApproach,
	av.ErrUnknownRunway.Error():                av.ErrUnknownRunway,
	ErrControllerAlreadySignedIn.Error():       ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():                ErrDuplicateSimName,
	ErrInvalidControllerToken.Error():          ErrInvalidControllerToken,
	ErrNoNamedSim.Error():                      ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():         ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                      ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():              ErrRPCVersionMismatch,
	ErrRestoringSavedState.Error():             ErrRestoringSavedState,
	ErrInvalidPassword.Error():                 ErrInvalidPassword,
}

func TryDecodeError(e error) error {
	if err, ok := errorStringToError[e.Error()]; ok {
		return err
	}
	return e
}
