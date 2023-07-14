// errors.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"net/rpc"
)

// Aviation-related
var (
	ErrArrivalAirportUnknown        = errors.New("Arrival airport unknown")
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
	ErrInvalidAltitude              = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidCommandSyntax         = errors.New("Invalid command syntax")
	ErrInvalidHeading               = errors.New("Invalid heading")
	ErrNoAircraftForCallsign        = errors.New("No aircraft exists with specified callsign")
	ErrNoController                 = errors.New("No controller with that callsign")
	ErrNoFlightPlan                 = errors.New("No flight plan has been filed for aircraft")
	ErrNoValidDepartureFound        = errors.New("Unable to find a valid departure")
	ErrNotBeingHandedOffToMe        = errors.New("Aircraft not being handed off to current controller")
	ErrOtherControllerHasTrack      = errors.New("Another controller is already tracking the aircraft")
	ErrUnableCommand                = errors.New("Unable")
	ErrUnknownAircraftType          = errors.New("Unknown aircraft type")
	ErrUnknownAirport               = errors.New("Unknown airport")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrUnknownRunway                = errors.New("Unknown runway")
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
)

var errorStringToError = map[string]error{
	ErrArrivalAirportUnknown.Error():        ErrArrivalAirportUnknown,
	ErrClearedForUnexpectedApproach.Error(): ErrClearedForUnexpectedApproach,
	ErrFixNotInRoute.Error():                ErrFixNotInRoute,
	ErrInvalidAltitude.Error():              ErrInvalidAltitude,
	ErrInvalidApproach.Error():              ErrInvalidApproach,
	ErrInvalidCommandSyntax.Error():         ErrInvalidCommandSyntax,
	ErrInvalidHeading.Error():               ErrInvalidHeading,
	ErrNoAircraftForCallsign.Error():        ErrNoAircraftForCallsign,
	ErrNoController.Error():                 ErrNoController,
	ErrNoFlightPlan.Error():                 ErrNoFlightPlan,
	ErrNoValidDepartureFound.Error():        ErrNoValidDepartureFound,
	ErrNotBeingHandedOffToMe.Error():        ErrNotBeingHandedOffToMe,
	ErrOtherControllerHasTrack.Error():      ErrOtherControllerHasTrack,
	ErrUnableCommand.Error():                ErrUnableCommand,
	ErrUnknownAircraftType.Error():          ErrUnknownAircraftType,
	ErrUnknownAirport.Error():               ErrUnknownAirport,
	ErrUnknownApproach.Error():              ErrUnknownApproach,
	ErrUnknownRunway.Error():                ErrUnknownRunway,
	ErrControllerAlreadySignedIn.Error():    ErrControllerAlreadySignedIn,
	ErrDuplicateSimName.Error():             ErrDuplicateSimName,
	ErrInvalidControllerToken.Error():       ErrInvalidControllerToken,
	ErrNoNamedSim.Error():                   ErrNoNamedSim,
	ErrNoSimForControllerToken.Error():      ErrNoSimForControllerToken,
	ErrRPCTimeout.Error():                   ErrRPCTimeout,
	ErrRPCVersionMismatch.Error():           ErrRPCVersionMismatch,
	ErrRestoringSavedState.Error():          ErrRestoringSavedState,
}

///////////////////////////////////////////////////////////////////////////
// STARS

var (
	ErrSTARSCommandFormat     = errors.New("FORMAT")
	ErrSTARSDuplicateBeacon   = errors.New("DUP BCN")
	ErrSTARSIllegalATIS       = errors.New("ILL ATIS")
	ErrSTARSIllegalAirport    = errors.New("ILL AIRPORT")
	ErrSTARSIllegalCode       = errors.New("ILL CODE")
	ErrSTARSIllegalFix        = errors.New("ILL FIX")
	ErrSTARSIllegalFlight     = errors.New("ILL FLIGHT")
	ErrSTARSIllegalLine       = errors.New("ILL LINE")
	ErrSTARSIllegalParam      = errors.New("ILL PARAM")
	ErrSTARSIllegalScratchpad = errors.New("ILL SCR")
	ErrSTARSIllegalSector     = errors.New("ILL SECTOR")
	ErrSTARSIllegalText       = errors.New("ILL TEXT")
	ErrSTARSIllegalTrack      = errors.New("ILL TRK")
	ErrSTARSIllegalValue      = errors.New("ILL VALUE")
	ErrSTARSNoFlight          = errors.New("NO FLIGHT")
)

var starsErrorRemap = map[error]error{
	ErrArrivalAirportUnknown:        ErrSTARSIllegalAirport,
	ErrClearedForUnexpectedApproach: ErrSTARSIllegalValue,
	ErrFixNotInRoute:                ErrSTARSIllegalFix,
	ErrInvalidAltitude:              ErrSTARSIllegalValue,
	ErrInvalidApproach:              ErrSTARSIllegalValue,
	ErrInvalidCommandSyntax:         ErrSTARSCommandFormat,
	ErrInvalidHeading:               ErrSTARSIllegalValue,
	ErrNoAircraftForCallsign:        ErrSTARSNoFlight,
	ErrNoController:                 ErrSTARSIllegalSector,
	ErrNoFlightPlan:                 ErrSTARSIllegalFlight,
	ErrNotBeingHandedOffToMe:        ErrSTARSIllegalTrack,
	ErrOtherControllerHasTrack:      ErrSTARSIllegalTrack,
	ErrUnableCommand:                ErrSTARSIllegalValue,
	ErrUnknownAircraftType:          ErrSTARSIllegalParam,
	ErrUnknownAirport:               ErrSTARSIllegalAirport,
	ErrUnknownApproach:              ErrSTARSIllegalValue,
	ErrUnknownRunway:                ErrSTARSIllegalValue,
}

func GetSTARSError(e error) error {
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
