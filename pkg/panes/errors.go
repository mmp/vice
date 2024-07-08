// errors.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"errors"
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/sim"
)

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
	ErrSTARSIllegalACID       = NewSTARSError("ILL ACID")
	ErrSTARSIllegalACType     = NewSTARSError("ACTYPE NOT\nADAPTED")
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
	av.ErrFixNotInRoute:                ErrSTARSIllegalFix,
	av.ErrInvalidAltitude:              ErrSTARSIllegalValue,
	av.ErrInvalidApproach:              ErrSTARSIllegalValue,
	sim.ErrInvalidCommandSyntax:        ErrSTARSCommandFormat,
	av.ErrInvalidController:            ErrSTARSIllegalPosition,
	av.ErrInvalidFacility:              ErrSTARSIllegalTrack,
	av.ErrInvalidHeading:               ErrSTARSIllegalValue,
	av.ErrNoAircraftForCallsign:        ErrSTARSNoFlight,
	av.ErrNoController:                 ErrSTARSIllegalSector,
	av.ErrNoFlightPlan:                 ErrSTARSIllegalFlight,
	av.ErrNotBeingHandedOffToMe:        ErrSTARSIllegalTrack,
	av.ErrNotPointedOutToMe:            ErrSTARSIllegalTrack,
	av.ErrNotClearedForApproach:        ErrSTARSIllegalValue,
	av.ErrNotFlyingRoute:               ErrSTARSIllegalValue,
	av.ErrOtherControllerHasTrack:      ErrSTARSIllegalTrack,
	av.ErrUnableCommand:                ErrSTARSIllegalValue,
	av.ErrUnknownAircraftType:          ErrSTARSIllegalParam,
	av.ErrUnknownAirport:               ErrSTARSIllegalAirport,
	av.ErrUnknownApproach:              ErrSTARSIllegalValue,
	av.ErrUnknownRunway:                ErrSTARSIllegalValue,
}

func GetSTARSError(e error, lg *log.Logger) *STARSError {
	if se, ok := e.(*STARSError); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = sim.TryDecodeError(e)
	}

	if se, ok := starsErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetSTARSError", e)
	return ErrSTARSCommandFormat
}
