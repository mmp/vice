// stars/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"errors"
	"net/rpc"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/sim"
)

///////////////////////////////////////////////////////////////////////////
// STARS

type Error struct {
	error
}

func NewError(msg string) *Error {
	return &Error{errors.New(msg)}
}

var (
	ErrAmbiguousACID              = NewError("AMB ACID")
	ErrBeaconMismatch             = NewError("BCN MISMATCH")
	ErrCapacity                   = NewError("CAPACITY")
	ErrCapacityBeacon             = NewError("CAPACITY - BCN")
	ErrCommandFormat              = NewError("FORMAT")
	ErrDuplicateACID              = NewError("DUP NEW ID")
	ErrDuplicateBeacon            = NewError("DUP BCN")
	ErrDuplicateCommand           = NewError("DUP CMD")
	ErrIllegalACID                = NewError("ILL ACID")
	ErrIllegalACType              = NewError("ACTYPE NOT ADAPTED")
	ErrIllegalATIS                = NewError("ILL ATIS")
	ErrIllegalAirport             = NewError("ILL AIRPORT")
	ErrIllegalCode                = NewError("ILL CODE")
	ErrIllegalColor               = NewError("ILL COLOR")
	ErrIllegalFix                 = NewError("ILL FIX")
	ErrIllegalFlight              = NewError("ILL FLIGHT")
	ErrIllegalArea                = NewError("ILL AREA")
	ErrIllegalFunction            = NewError("ILL FUNC")
	ErrIllegalFunctionAlertActive = NewError("ILL FUNC - ALERT ACTIVE")
	ErrIllegalFunctionNoRegions   = NewError("ILL FNCT -\nNO REGIONS")
	ErrIllegalFunctionProcOff     = NewError("ILL FNCT -\nPROCESSING OFF")
	ErrIllegalGeoId               = NewError("ILL GEO ID")
	ErrIllegalGeoLoc              = NewError("ILL GEO LOC")
	ErrIllegalLine                = NewError("ILL LINE")
	ErrIllegalMap                 = NewError("ILL MAP")
	ErrIllegalParam               = NewError("ILL PARAM")
	ErrIllegalPosition            = NewError("ILL POS")
	ErrIllegalPrefset             = NewError("ILL PREFSET")
	ErrIllegalRPC                 = NewError("ILL RPC") // CRDA runway pair config
	ErrIllegalRange               = NewError("ILL RANGE")
	ErrIllegalRegion              = NewError("ILL REGION")
	ErrIllegalRunway              = NewError("ILL RWY")
	ErrIllegalScratchpad          = NewError("ILL SCR")
	ErrIllegalSector              = NewError("ILL SECTOR")
	ErrIllegalTCPDeconsolFirst    = NewError("ILL TCP - DECONSOL FIRST")
	ErrIllegalTCPNotConsolidated  = NewError("ILL TCP - NOT CONSOLIDATED")
	ErrIllegalTCW                 = NewError("ILL TCW")
	ErrIllegalText                = NewError("ILL TEXT")
	ErrIllegalTrack               = NewError("ILL TRK")
	ErrIllegalTrackLocalFP        = NewError("ILL TRK - LCL FP")
	ErrIllegalValue               = NewError("ILL VALUE")
	ErrMultipleFlights            = NewError("MULTIPLE FLIGHT")
	ErrNoACType                   = NewError("NO ACTYP")
	ErrNoFlight                   = NewError("NO FLIGHT")
	ErrNoScratchpad               = NewError("NO SCR")
	ErrNoTrack                    = NewError("NO TRK")
	ErrRangeLimit                 = NewError("RANGE LIMIT")
)

var starsErrorRemap = map[error]*Error{
	av.ErrBadPoolSpecifier:           ErrIllegalCode,
	av.ErrInvalidAltitude:            ErrIllegalValue,
	av.ErrInvalidController:          ErrIllegalPosition,
	av.ErrInvalidFacility:            ErrIllegalTrack,
	av.ErrInvalidHeading:             ErrIllegalValue,
	av.ErrNoAircraftForCallsign:      ErrNoFlight,
	av.ErrNoController:               ErrIllegalSector,
	av.ErrNoFlightPlan:               ErrIllegalFlight,
	av.ErrNoMoreAvailableSquawkCodes: ErrCapacityBeacon,
	av.ErrNoValidDepartureFound:      ErrIllegalFunction,
	av.ErrNotBeingHandedOffToMe:      ErrIllegalTrack,
	av.ErrNotPointedOutByMe:          ErrIllegalTrack,
	av.ErrNotPointedOutToMe:          ErrIllegalTrack,
	av.ErrOtherControllerHasTrack:    ErrIllegalTrack,
	av.ErrUnknownAirport:             ErrIllegalAirport,
	av.ErrUnknownRunway:              ErrIllegalValue,

	nav.ErrClearedForUnexpectedApproach: ErrIllegalValue,
	nav.ErrFixIsTooFarAway:              ErrIllegalFix,
	nav.ErrFixNotInRoute:                ErrIllegalFix,
	nav.ErrInvalidApproach:              ErrIllegalValue,
	nav.ErrInvalidFix:                   ErrIllegalFix,
	nav.ErrNotClearedForApproach:        ErrIllegalValue,
	nav.ErrNotFlyingRoute:               ErrIllegalValue,
	nav.ErrUnableCommand:                ErrIllegalValue,
	nav.ErrUnknownApproach:              ErrIllegalValue,

	sim.ErrATPADisabled:                    ErrIllegalFunction,
	sim.ErrAircraftAlreadyReleased:         ErrDuplicateCommand,
	sim.ErrBeaconMismatch:                  ErrBeaconMismatch,
	sim.ErrDuplicateACID:                   ErrDuplicateACID,
	sim.ErrDuplicateBeacon:                 ErrDuplicateBeacon,
	sim.ErrIllegalACID:                     ErrIllegalACID,
	sim.ErrIllegalACType:                   ErrIllegalACType,
	sim.ErrIllegalATIS:                     ErrIllegalATIS,
	sim.ErrIllegalBeaconCode:               ErrIllegalCode,
	sim.ErrIllegalFunction:                 ErrIllegalFunction,
	sim.ErrIllegalLine:                     ErrIllegalLine,
	sim.ErrIllegalPosition:                 ErrIllegalPosition,
	sim.ErrIllegalScratchpad:               ErrIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP:            ErrCommandFormat,
	sim.ErrInvalidCommandSyntax:            ErrCommandFormat,
	sim.ErrInvalidDepartureController:      ErrIllegalFunction,
	sim.ErrInvalidRestrictionAreaIndex:     ErrIllegalGeoId,
	sim.ErrInvalidVolumeId:                 ErrIllegalFunction,
	sim.ErrNoACType:                        ErrNoACType,
	sim.ErrNoMatchingFlight:                ErrNoFlight,
	sim.ErrNoMatchingFlightPlan:            ErrNoFlight,
	sim.ErrNoRecentCommand:                 ErrIllegalFunction,
	sim.ErrNoScratchpad:                    ErrNoScratchpad,
	sim.ErrNoVFRAircraftForFlightFollowing: ErrNoFlight,
	sim.ErrTCPAlreadyConsolidated:          ErrIllegalTCPDeconsolFirst,
	sim.ErrTCPNotConsolidated:              ErrIllegalTCPNotConsolidated,
	sim.ErrTCWIsConsolidated:               ErrIllegalPosition,
	sim.ErrTCWNotFound:                     ErrIllegalTCW,
	sim.ErrTCWNotVacant:                    ErrIllegalPosition,
	sim.ErrTooManyRestrictionAreas:         ErrCapacity,
	sim.ErrTrackHasActivePointOut:          ErrIllegalTrack,
	sim.ErrTrackIsActive:                   ErrIllegalTrack,
	sim.ErrIllegalTrackLocalFP:             ErrIllegalTrackLocalFP,
	sim.ErrTrackIsBeingHandedOff:           ErrIllegalTrack,
	sim.ErrTrackIsNotActive:                ErrIllegalTrack,
	sim.ErrUnknownAircraftType:             ErrIllegalParam,
	sim.ErrUnknownController:               ErrIllegalPosition,
	sim.ErrUnknownControllerFacility:       ErrIllegalPosition,
	sim.ErrFDAMIllegalArea:                 ErrIllegalArea,
	sim.ErrFDAMNoRegions:                   ErrIllegalFunctionNoRegions,
	sim.ErrFDAMProcessingOff:               ErrIllegalFunctionProcOff,
	sim.ErrVolumeDisabled:                  ErrIllegalFunction,
	sim.ErrVolumeNot25nm:                   ErrIllegalFunction,
}

func GetError(e error, lg *log.Logger) *Error {
	if se, ok := e.(*Error); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = client.TryDecodeError(e)
	}

	if se, ok := starsErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetError", e)
	return ErrCommandFormat
}
