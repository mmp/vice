// errors.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"net/rpc"
	"os"
	"strings"
)

// Aviation-related
var (
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
	ErrInvalidAltitude              = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidCommandSyntax         = errors.New("Invalid command syntax")
	ErrInvalidHeading               = errors.New("Invalid heading")
	ErrNoAircraftForCallsign        = errors.New("No aircraft exists with specified callsign")
	ErrNoController                 = errors.New("No controller with that callsign")
	ErrNotLaunchController          = errors.New("Not signed in as the launch controller")
	ErrNoFlightPlan                 = errors.New("No flight plan has been filed for aircraft")
	ErrNoValidArrivalFound          = errors.New("Unable to find a valid arrival")
	ErrNoValidDepartureFound        = errors.New("Unable to find a valid departure")
	ErrNotBeingHandedOffToMe        = errors.New("Aircraft not being handed off to current controller")
	ErrNotPointedOutToMe            = errors.New("Aircraft not being pointed out to current controller")
	ErrNotClearedForApproach        = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute               = errors.New("Aircraft is not currently flying its assigned route")
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
	ErrInvalidPassword           = errors.New("Invalid password")
)

var errorStringToError = map[string]error{
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
	ErrNotPointedOutToMe.Error():            ErrNotPointedOutToMe,
	ErrNotClearedForApproach.Error():        ErrNotClearedForApproach,
	ErrNotFlyingRoute.Error():               ErrNotFlyingRoute,
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
	ErrInvalidPassword.Error():              ErrInvalidPassword,
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
)

var starsErrorRemap = map[error]*STARSError{
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
	ErrNotPointedOutToMe:            ErrSTARSIllegalTrack,
	ErrNotClearedForApproach:        ErrSTARSIllegalValue,
	ErrNotFlyingRoute:               ErrSTARSIllegalValue,
	ErrOtherControllerHasTrack:      ErrSTARSIllegalTrack,
	ErrUnableCommand:                ErrSTARSIllegalValue,
	ErrUnknownAircraftType:          ErrSTARSIllegalParam,
	ErrUnknownAirport:               ErrSTARSIllegalAirport,
	ErrUnknownApproach:              ErrSTARSIllegalValue,
	ErrUnknownRunway:                ErrSTARSIllegalValue,
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

///////////////////////////////////////////////////////////////////////////

// ErrorLogger is a small utility class used to log errors when validating
// the parsed JSON scenarios. It tracks context about what is currently
// being validated and accumulates multiple errors, making it possible to
// log errors while still continuing validation.
type ErrorLogger struct {
	// Tracked via Push()/Pop() calls to remember what we're looking at if
	// an error is found.
	hierarchy []string
	// Actual error messages to report.
	errors []string
}

func (e *ErrorLogger) Push(s string) {
	e.hierarchy = append(e.hierarchy, s)
}

func (e *ErrorLogger) Pop() {
	e.hierarchy = e.hierarchy[:len(e.hierarchy)-1]
}

func (e *ErrorLogger) ErrorString(s string, args ...interface{}) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+fmt.Sprintf(s, args...))
}

func (e *ErrorLogger) Error(err error) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+err.Error())
}

func (e *ErrorLogger) HaveErrors() bool {
	return len(e.errors) > 0
}

func (e *ErrorLogger) PrintErrors(lg *Logger) {
	// Two loops so they aren't interleaved with logging to stdout
	if lg != nil {
		for _, err := range e.errors {
			lg.Errorf("%+v", err)
		}
	}
	for _, err := range e.errors {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (e *ErrorLogger) String() string {
	return strings.Join(e.errors, "\n")
}
