// stars/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"errors"
	"net/rpc"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
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
	ErrSTARSAmbiguousACID              = NewSTARSError("AMB ACID")
	ErrSTARSBeaconMismatch             = NewSTARSError("BCN MISMATCH")
	ErrSTARSCapacity                   = NewSTARSError("CAPACITY")
	ErrSTARSCapacityBeacon             = NewSTARSError("CAPACITY - BCN")
	ErrSTARSCommandFormat              = NewSTARSError("FORMAT")
	ErrSTARSDuplicateACID              = NewSTARSError("DUP NEW ID")
	ErrSTARSDuplicateBeacon            = NewSTARSError("DUP BCN")
	ErrSTARSDuplicateCommand           = NewSTARSError("DUP CMD")
	ErrSTARSIllegalACID                = NewSTARSError("ILL ACID")
	ErrSTARSIllegalACType              = NewSTARSError("ACTYPE NOT ADAPTED")
	ErrSTARSIllegalATIS                = NewSTARSError("ILL ATIS")
	ErrSTARSIllegalAirport             = NewSTARSError("ILL AIRPORT")
	ErrSTARSIllegalCode                = NewSTARSError("ILL CODE")
	ErrSTARSIllegalColor               = NewSTARSError("ILL COLOR")
	ErrSTARSIllegalFix                 = NewSTARSError("ILL FIX")
	ErrSTARSIllegalFlight              = NewSTARSError("ILL FLIGHT")
	ErrSTARSIllegalArea                = NewSTARSError("ILL AREA")
	ErrSTARSIllegalFunction            = NewSTARSError("ILL FUNC")
	ErrSTARSIllegalFunctionAlertActive = NewSTARSError("ILL FUNC - ALERT ACTIVE")
	ErrSTARSIllegalFunctionNoRegions   = NewSTARSError("ILL FNCT -\nNO REGIONS")
	ErrSTARSIllegalFunctionProcOff     = NewSTARSError("ILL FNCT -\nPROCESSING OFF")
	ErrSTARSIllegalGeoId               = NewSTARSError("ILL GEO ID")
	ErrSTARSIllegalGeoLoc              = NewSTARSError("ILL GEO LOC")
	ErrSTARSIllegalLine                = NewSTARSError("ILL LINE")
	ErrSTARSIllegalMap                 = NewSTARSError("ILL MAP")
	ErrSTARSIllegalParam               = NewSTARSError("ILL PARAM")
	ErrSTARSIllegalPosition            = NewSTARSError("ILL POS")
	ErrSTARSIllegalPrefset             = NewSTARSError("ILL PREFSET")
	ErrSTARSIllegalRPC                 = NewSTARSError("ILL RPC") // CRDA runway pair config
	ErrSTARSIllegalRange               = NewSTARSError("ILL RANGE")
	ErrSTARSIllegalRegion              = NewSTARSError("ILL REGION")
	ErrSTARSIllegalRunway              = NewSTARSError("ILL RWY")
	ErrSTARSIllegalScratchpad          = NewSTARSError("ILL SCR")
	ErrSTARSIllegalSector              = NewSTARSError("ILL SECTOR")
	ErrSTARSIllegalTCPDeconsolFirst    = NewSTARSError("ILL TCP - DECONSOL FIRST")
	ErrSTARSIllegalTCPNotConsolidated  = NewSTARSError("ILL TCP - NOT CONSOLIDATED")
	ErrSTARSIllegalTCW                 = NewSTARSError("ILL TCW")
	ErrSTARSIllegalText                = NewSTARSError("ILL TEXT")
	ErrSTARSIllegalTrack               = NewSTARSError("ILL TRK")
	ErrSTARSIllegalValue               = NewSTARSError("ILL VALUE")
	ErrSTARSMultipleFlights            = NewSTARSError("MULTIPLE FLIGHT")
	ErrSTARSNoFlight                   = NewSTARSError("NO FLIGHT")
	ErrSTARSNoTrack                    = NewSTARSError("NO TRK")
	ErrSTARSRangeLimit                 = NewSTARSError("RANGE LIMIT")
)

var starsErrorRemap = map[error]*STARSError{
	av.ErrBadPoolSpecifier:           ErrSTARSIllegalCode,
	av.ErrInvalidAltitude:            ErrSTARSIllegalValue,
	av.ErrInvalidController:          ErrSTARSIllegalPosition,
	av.ErrInvalidFacility:            ErrSTARSIllegalTrack,
	av.ErrInvalidHeading:             ErrSTARSIllegalValue,
	av.ErrNoAircraftForCallsign:      ErrSTARSNoFlight,
	av.ErrNoController:               ErrSTARSIllegalSector,
	av.ErrNoFlightPlan:               ErrSTARSIllegalFlight,
	av.ErrNoMoreAvailableSquawkCodes: ErrSTARSCapacityBeacon,
	av.ErrNoValidDepartureFound:      ErrSTARSIllegalFunction,
	av.ErrNotBeingHandedOffToMe:      ErrSTARSIllegalTrack,
	av.ErrNotPointedOutByMe:          ErrSTARSIllegalTrack,
	av.ErrNotPointedOutToMe:          ErrSTARSIllegalTrack,
	av.ErrOtherControllerHasTrack:    ErrSTARSIllegalTrack,
	av.ErrUnknownAirport:             ErrSTARSIllegalAirport,
	av.ErrUnknownRunway:              ErrSTARSIllegalValue,

	nav.ErrClearedForUnexpectedApproach: ErrSTARSIllegalValue,
	nav.ErrFixIsTooFarAway:              ErrSTARSIllegalFix,
	nav.ErrFixNotInRoute:                ErrSTARSIllegalFix,
	nav.ErrInvalidApproach:              ErrSTARSIllegalValue,
	nav.ErrInvalidFix:                   ErrSTARSIllegalFix,
	nav.ErrNotClearedForApproach:        ErrSTARSIllegalValue,
	nav.ErrNotFlyingRoute:               ErrSTARSIllegalValue,
	nav.ErrUnableCommand:                ErrSTARSIllegalValue,
	nav.ErrUnknownApproach:              ErrSTARSIllegalValue,

	sim.ErrATPADisabled:                    ErrSTARSIllegalFunction,
	sim.ErrAircraftAlreadyReleased:         ErrSTARSDuplicateCommand,
	sim.ErrBeaconMismatch:                  ErrSTARSBeaconMismatch,
	sim.ErrDuplicateACID:                   ErrSTARSDuplicateACID,
	sim.ErrDuplicateBeacon:                 ErrSTARSDuplicateBeacon,
	sim.ErrIllegalACID:                     ErrSTARSIllegalACID,
	sim.ErrIllegalACType:                   ErrSTARSIllegalACType,
	sim.ErrIllegalBeaconCode:               ErrSTARSIllegalCode,
	sim.ErrIllegalFunction:                 ErrSTARSIllegalFunction,
	sim.ErrIllegalPosition:                 ErrSTARSIllegalPosition,
	sim.ErrIllegalScratchpad:               ErrSTARSIllegalScratchpad,
	sim.ErrInvalidAbbreviatedFP:            ErrSTARSCommandFormat,
	sim.ErrInvalidDepartureController:      ErrSTARSIllegalFunction,
	sim.ErrInvalidRestrictionAreaIndex:     ErrSTARSIllegalGeoId,
	sim.ErrInvalidVolumeId:                 ErrSTARSIllegalFunction,
	sim.ErrNoMatchingFlight:                ErrSTARSNoFlight,
	sim.ErrNoMatchingFlightPlan:            ErrSTARSNoFlight,
	sim.ErrNoVFRAircraftForFlightFollowing: ErrSTARSNoFlight,
	sim.ErrNotLaunchController:             ErrSTARSIllegalTrack,
	sim.ErrTCPAlreadyConsolidated:          ErrSTARSIllegalTCPDeconsolFirst,
	sim.ErrTCPNotConsolidated:              ErrSTARSIllegalTCPNotConsolidated,
	sim.ErrTCWIsConsolidated:               ErrSTARSIllegalPosition,
	sim.ErrTCWNotFound:                     ErrSTARSIllegalTCW,
	sim.ErrTCWNotVacant:                    ErrSTARSIllegalPosition,
	sim.ErrTooManyRestrictionAreas:         ErrSTARSCapacity,
	sim.ErrTrackIsActive:                   ErrSTARSIllegalTrack,
	sim.ErrTrackIsBeingHandedOff:           ErrSTARSIllegalTrack,
	sim.ErrTrackIsNotActive:                ErrSTARSIllegalTrack,
	sim.ErrUnknownAircraftType:             ErrSTARSIllegalParam,
	sim.ErrUnknownController:               ErrSTARSIllegalPosition,
	sim.ErrUnknownControllerFacility:       ErrSTARSIllegalPosition,
	sim.ErrFDAMIllegalArea:                 ErrSTARSIllegalArea,
	sim.ErrFDAMNoRegions:                   ErrSTARSIllegalFunctionNoRegions,
	sim.ErrFDAMProcessingOff:               ErrSTARSIllegalFunctionProcOff,
	sim.ErrVolumeDisabled:                  ErrSTARSIllegalFunction,
	sim.ErrVolumeNot25nm:                   ErrSTARSIllegalFunction,

	server.ErrInvalidCommandSyntax: ErrSTARSCommandFormat,
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
