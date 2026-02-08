// pkg/sim/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
)

var (
	ErrAircraftAlreadyReleased         = errors.New("Aircraft already released")
	ErrATPADisabled                    = errors.New("ATPA is disabled system-wide")
	ErrBeaconMismatch                  = errors.New("Beacon code mismatch")
	ErrControllerAlreadySignedIn       = errors.New("Controller with that callsign already signed in")
	ErrDuplicateACID                   = errors.New("Duplicate ACID")
	ErrDuplicateBeacon                 = errors.New("Duplicate beacon code")
	ErrIllegalACID                     = errors.New("Illegal ACID")
	ErrIllegalACType                   = errors.New("Illegal aircraft type")
	ErrIllegalBeaconCode               = errors.New("Illegal beacon code")
	ErrIllegalFunction                 = errors.New("Illegal function")
	ErrIllegalPosition                 = errors.New("Illegal position")
	ErrIllegalScratchpad               = errors.New("Illegal scratchpad")
	ErrInvalidAbbreviatedFP            = errors.New("Invalid abbreviated flight plan")
	ErrInvalidDepartureController      = errors.New("Invalid departure controller")
	ErrInvalidRestrictionAreaIndex     = errors.New("Invalid restriction area index")
	ErrInvalidVolumeId                 = errors.New("Invalid ATPA volume ID")
	ErrNoMatchingFlight                = errors.New("No matching flight")
	ErrNoMatchingFlightPlan            = errors.New("No matching flight plan")
	ErrNoRecentCommand                 = errors.New("No recent command to roll back")
	ErrNoVFRAircraftForFlightFollowing = errors.New("No VFR aircraft available for flight following")
	ErrNotLaunchController             = errors.New("Not signed in as the launch controller")
	ErrTCPAlreadyConsolidated          = errors.New("TCP already consolidated - deconsolidate first")
	ErrTCPNotConsolidated              = errors.New("TCP is not consolidated")
	ErrTCWIsConsolidated               = errors.New("receiving TCW is a consolidated position")
	ErrTCWNotFound                     = errors.New("TCW not found")
	ErrTCWNotVacant                    = errors.New("receiving TCW has an associated TCP")
	ErrTooManyRestrictionAreas         = errors.New("Too many restriction areas specified")
	ErrTrackIsActive                   = errors.New("Track is already active")
	ErrTrackIsBeingHandedOff           = errors.New("Track is currently being handed off")
	ErrTrackIsNotActive                = errors.New("Track is not active")
	ErrUnknownAircraftType             = errors.New("Unknown aircraft type")
	ErrUnknownController               = errors.New("Unknown controller")
	ErrUnknownControllerFacility       = errors.New("Unknown controller facility")
	ErrVFRBelowMVA                     = errors.New("VFR aircraft below MVA")
	ErrVFRSimTookTooLong               = errors.New("VFR simulation took too long")
	ErrViolatedAirspace                = errors.New("Violated B/C airspace")
	ErrVolumeDisabled                  = errors.New("ATPA volume is disabled")
	ErrVolumeNot25nm                   = errors.New("ATPA volume not adapted for 2.5nm separation")
)
