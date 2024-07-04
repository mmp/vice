// errors.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
)

// Aviation-related
var (
	ErrFixNotInRoute           = errors.New("Fix not in aircraft's route")
	ErrInvalidAltitude         = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidCommandSyntax    = errors.New("Invalid command syntax")
	ErrInvalidController       = errors.New("Invalid controller")
	ErrInvalidFacility         = errors.New("Invalid facility")
	ErrInvalidHeading          = errors.New("Invalid heading")
	ErrNoAircraftForCallsign   = errors.New("No aircraft exists with specified callsign")
	ErrNoController            = errors.New("No controller with that callsign")
	ErrNotLaunchController     = errors.New("Not signed in as the launch controller")
	ErrNoFlightPlan            = errors.New("No flight plan has been filed for aircraft")
	ErrNoValidDepartureFound   = errors.New("Unable to find a valid departure")
	ErrNotBeingHandedOffToMe   = errors.New("Aircraft not being handed off to current controller")
	ErrNotPointedOutToMe       = errors.New("Aircraft not being pointed out to current controller")
	ErrOtherControllerHasTrack = errors.New("Another controller is already tracking the aircraft")
	ErrUnknownAirport          = errors.New("Unknown airport")
	ErrUnknownRunway           = errors.New("Unknown runway")
)

// Sim/server-related
var (
	ErrControllerAlreadySignedIn = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName          = errors.New("A sim with that name already exists")
	ErrInvalidControllerToken    = errors.New("Invalid controller token")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrRestoringSavedState       = errors.New("Errors during state restoration")
	ErrInvalidPassword           = errors.New("Invalid password")
)

var errorStringToError = map[string]error{
	av.ErrClearedForUnexpectedApproach.Error(): av.ErrClearedForUnexpectedApproach,
	ErrFixNotInRoute.Error():                   ErrFixNotInRoute,
	ErrInvalidAltitude.Error():                 ErrInvalidAltitude,
	av.ErrInvalidApproach.Error():              av.ErrInvalidApproach,
	ErrInvalidCommandSyntax.Error():            ErrInvalidCommandSyntax,
	ErrInvalidController.Error():               ErrInvalidController,
	ErrInvalidFacility.Error():                 ErrInvalidFacility,
	ErrInvalidHeading.Error():                  ErrInvalidHeading,
	ErrNoAircraftForCallsign.Error():           ErrNoAircraftForCallsign,
	ErrNoController.Error():                    ErrNoController,
	ErrNoFlightPlan.Error():                    ErrNoFlightPlan,
	av.ErrNoValidArrivalFound.Error():          av.ErrNoValidArrivalFound,
	ErrNotBeingHandedOffToMe.Error():           ErrNotBeingHandedOffToMe,
	ErrNotPointedOutToMe.Error():               ErrNotPointedOutToMe,
	av.ErrNotClearedForApproach.Error():        av.ErrNotClearedForApproach,
	av.ErrNotFlyingRoute.Error():               av.ErrNotFlyingRoute,
	ErrOtherControllerHasTrack.Error():         ErrOtherControllerHasTrack,
	av.ErrUnableCommand.Error():                av.ErrUnableCommand,
	av.ErrUnknownAircraftType.Error():          av.ErrUnknownAircraftType,
	ErrUnknownAirport.Error():                  ErrUnknownAirport,
	av.ErrUnknownApproach.Error():              av.ErrUnknownApproach,
	ErrUnknownRunway.Error():                   ErrUnknownRunway,
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

///////////////////////////////////////////////////////////////////////////
// STARS

type STARSError struct {
	error
}

func NewSTARSError(msg string) *STARSError {
	return &STARSError{errors.New(msg)}
}

var (
	ErrSTARSCommandFormat     = NewSTARSError("FORMAT")
	ErrSTARSDuplicateBeacon   = NewSTARSError("DUP BCN")
	ErrSTARSIllegalATIS       = NewSTARSError("ILL ATIS")
	ErrSTARSIllegalAirport    = NewSTARSError("ILL AIRPORT")
	ErrSTARSIllegalCode       = NewSTARSError("ILL CODE")
	ErrSTARSIllegalFix        = NewSTARSError("ILL FIX")
	ErrSTARSIllegalFlight     = NewSTARSError("ILL FLIGHT")
	ErrSTARSIllegalFunc       = NewSTARSError("ILL FUNC")
	ErrSTARSIllegalFunction   = NewSTARSError("ILL FNCT")
	ErrSTARSIllegalLine       = NewSTARSError("ILL LINE")
	ErrSTARSIllegalMap        = NewSTARSError("ILL MAP")
	ErrSTARSIllegalParam      = NewSTARSError("ILL PARAM")
	ErrSTARSIllegalPosition   = NewSTARSError("ILL POS")
	ErrSTARSIllegalRPC        = NewSTARSError("ILL RPC") // CRDA runway pair config
	ErrSTARSIllegalRunway     = NewSTARSError("ILL RWY")
	ErrSTARSIllegalScratchpad = NewSTARSError("ILL SCR")
	ErrSTARSIllegalSector     = NewSTARSError("ILL SECTOR")
	ErrSTARSIllegalText       = NewSTARSError("ILL TEXT")
	ErrSTARSIllegalTrack      = NewSTARSError("ILL TRK")
	ErrSTARSIllegalValue      = NewSTARSError("ILL VALUE")
	ErrSTARSNoFlight          = NewSTARSError("NO FLIGHT")
	ErrSTARSRangeLimit        = NewSTARSError("RANGE LIMIT")
)

var starsErrorRemap = map[error]*STARSError{
	av.ErrClearedForUnexpectedApproach: ErrSTARSIllegalValue,
	ErrFixNotInRoute:                   ErrSTARSIllegalFix,
	ErrInvalidAltitude:                 ErrSTARSIllegalValue,
	av.ErrInvalidApproach:              ErrSTARSIllegalValue,
	ErrInvalidCommandSyntax:            ErrSTARSCommandFormat,
	ErrInvalidController:               ErrSTARSIllegalPosition,
	ErrInvalidFacility:                 ErrSTARSIllegalTrack,
	ErrInvalidHeading:                  ErrSTARSIllegalValue,
	ErrNoAircraftForCallsign:           ErrSTARSNoFlight,
	ErrNoController:                    ErrSTARSIllegalSector,
	ErrNoFlightPlan:                    ErrSTARSIllegalFlight,
	ErrNotBeingHandedOffToMe:           ErrSTARSIllegalTrack,
	ErrNotPointedOutToMe:               ErrSTARSIllegalTrack,
	av.ErrNotClearedForApproach:        ErrSTARSIllegalValue,
	av.ErrNotFlyingRoute:               ErrSTARSIllegalValue,
	ErrOtherControllerHasTrack:         ErrSTARSIllegalTrack,
	av.ErrUnableCommand:                ErrSTARSIllegalValue,
	av.ErrUnknownAircraftType:          ErrSTARSIllegalParam,
	ErrUnknownAirport:                  ErrSTARSIllegalAirport,
	av.ErrUnknownApproach:              ErrSTARSIllegalValue,
	ErrUnknownRunway:                   ErrSTARSIllegalValue,
}

func GetSTARSError(e error) *STARSError {
	if se, ok := e.(*STARSError); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		if err, ok := errorStringToError[e.Error()]; ok {
			e = err
		}
	}

	if se, ok := starsErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetSTARSError", e)
	return ErrSTARSCommandFormat
}
