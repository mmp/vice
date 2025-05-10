// pkg/panes/stars/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"errors"
	"net/rpc"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/server"
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
	ErrSTARSAmbiguousACID     = NewSTARSError("AMB ACID")
	ErrSTARSBeaconMismatch    = NewSTARSError("BCN MISMATCH")
	ErrSTARSCapacity          = NewSTARSError("CAPACITY")
	ErrSTARSCapacityBeacon    = NewSTARSError("CAPACITY - BCN")
	ErrSTARSCommandFormat     = NewSTARSError("FORMAT")
	ErrSTARSDuplicateACID     = NewSTARSError("DUP NEW ID")
	ErrSTARSDuplicateBeacon   = NewSTARSError("DUP BCN")
	ErrSTARSDuplicateCommand  = NewSTARSError("DUP CMD")
	ErrSTARSIllegalACID       = NewSTARSError("ILL ACID")
	ErrSTARSIllegalACType     = NewSTARSError("ACTYPE NOT\nADAPTED")
	ErrSTARSIllegalATIS       = NewSTARSError("ILL ATIS")
	ErrSTARSIllegalAirport    = NewSTARSError("ILL AIRPORT")
	ErrSTARSIllegalCode       = NewSTARSError("ILL CODE")
	ErrSTARSIllegalColor      = NewSTARSError("ILL COLOR")
	ErrSTARSIllegalFix        = NewSTARSError("ILL FIX")
	ErrSTARSIllegalFlight     = NewSTARSError("ILL FLIGHT")
	ErrSTARSIllegalFunction   = NewSTARSError("ILL FUNC")
	ErrSTARSIllegalGeoId      = NewSTARSError("ILL GEO ID")
	ErrSTARSIllegalGeoLoc     = NewSTARSError("ILL GEO LOC")
	ErrSTARSIllegalLine       = NewSTARSError("ILL LINE")
	ErrSTARSIllegalMap        = NewSTARSError("ILL MAP")
	ErrSTARSIllegalParam      = NewSTARSError("ILL PARAM")
	ErrSTARSIllegalPosition   = NewSTARSError("ILL POS")
	ErrSTARSIllegalPrefset    = NewSTARSError("ILL PREFSET")
	ErrSTARSIllegalRange      = NewSTARSError("ILL RANGE")
	ErrSTARSIllegalRegion     = NewSTARSError("ILL REGION")
	ErrSTARSIllegalRPC        = NewSTARSError("ILL RPC") // CRDA runway pair config
	ErrSTARSIllegalRunway     = NewSTARSError("ILL RWY")
	ErrSTARSIllegalScratchpad = NewSTARSError("ILL SCR")
	ErrSTARSIllegalSector     = NewSTARSError("ILL SECTOR")
	ErrSTARSIllegalText       = NewSTARSError("ILL TEXT")
	ErrSTARSIllegalTrack      = NewSTARSError("ILL TRK")
	ErrSTARSIllegalValue      = NewSTARSError("ILL VALUE")
	ErrSTARSMultipleFlights   = NewSTARSError("MULTIPLE FLIGHT")
	ErrSTARSNoFlight          = NewSTARSError("NO FLIGHT")
	ErrSTARSNoTrack           = NewSTARSError("NO TRK")
	ErrSTARSRangeLimit        = NewSTARSError("RANGE LIMIT")
)

var starsErrorRemap = map[error]*STARSError{
	av.ErrClearedForUnexpectedApproach: ErrSTARSIllegalValue,
	av.ErrFixNotInRoute:                ErrSTARSIllegalFix,
	av.ErrInvalidAltitude:              ErrSTARSIllegalValue,
	av.ErrInvalidApproach:              ErrSTARSIllegalValue,
	av.ErrInvalidController:            ErrSTARSIllegalPosition,
	av.ErrInvalidFacility:              ErrSTARSIllegalTrack,
	av.ErrInvalidFix:                   ErrSTARSIllegalFix,
	av.ErrInvalidHeading:               ErrSTARSIllegalValue,
	av.ErrNoAircraftForCallsign:        ErrSTARSNoFlight,
	av.ErrNoController:                 ErrSTARSIllegalSector,
	av.ErrNoFlightPlan:                 ErrSTARSIllegalFlight,
	av.ErrNoMoreAvailableSquawkCodes:   ErrSTARSCapacityBeacon,
	av.ErrNotBeingHandedOffToMe:        ErrSTARSIllegalTrack,
	av.ErrNotClearedForApproach:        ErrSTARSIllegalValue,
	av.ErrNotFlyingRoute:               ErrSTARSIllegalValue,
	av.ErrNotPointedOutByMe:            ErrSTARSIllegalTrack,
	av.ErrNotPointedOutToMe:            ErrSTARSIllegalTrack,
	av.ErrOtherControllerHasTrack:      ErrSTARSIllegalTrack,
	av.ErrUnableCommand:                ErrSTARSIllegalValue,
	av.ErrUnknownAircraftType:          ErrSTARSIllegalParam,
	av.ErrUnknownAirport:               ErrSTARSIllegalAirport,
	av.ErrUnknownApproach:              ErrSTARSIllegalValue,
	av.ErrUnknownRunway:                ErrSTARSIllegalValue,

	server.ErrInvalidCommandSyntax: ErrSTARSCommandFormat,

	sim.ErrAircraftAlreadyReleased:     ErrSTARSDuplicateCommand,
	sim.ErrBeaconMismatch:              ErrSTARSBeaconMismatch,
	sim.ErrDuplicateACID:               ErrSTARSDuplicateACID,
	sim.ErrDuplicateBeacon:             ErrSTARSDuplicateBeacon,
	sim.ErrIllegalACID:                 ErrSTARSIllegalACID,
	sim.ErrIllegalACType:               ErrSTARSIllegalACType,
	sim.ErrIllegalBeaconCode:           ErrSTARSIllegalCode,
	sim.ErrIllegalFunction:             ErrSTARSIllegalFunction,
	sim.ErrIllegalScratchpad:           ErrSTARSIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP:        ErrSTARSCommandFormat,
	sim.ErrInvalidDepartureController:  ErrSTARSIllegalFunction,
	sim.ErrInvalidRestrictionAreaIndex: ErrSTARSIllegalGeoId,
	sim.ErrNoMatchingFlight:            ErrSTARSNoFlight,
	sim.ErrNoMoreListIndices:           ErrSTARSCapacity,
	sim.ErrNotLaunchController:         ErrSTARSIllegalTrack,
	sim.ErrTooManyRestrictionAreas:     ErrSTARSCapacity,
	sim.ErrTrackIsActive:               ErrSTARSIllegalTrack,
	sim.ErrTrackIsBeingHandedOff:       ErrSTARSIllegalTrack,
	sim.ErrTrackIsNotActive:            ErrSTARSIllegalTrack,
	sim.ErrUnknownController:           ErrSTARSIllegalPosition,
	sim.ErrUnknownControllerFacility:   ErrSTARSIllegalPosition,
}

func GetSTARSError(e error, lg *log.Logger) *STARSError {
	if se, ok := e.(*STARSError); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = server.TryDecodeError(e)
	}

	if se, ok := starsErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetSTARSError", e)
	return ErrSTARSCommandFormat
}
