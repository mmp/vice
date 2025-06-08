// pkg/sim/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
)

var (
	ErrAircraftAlreadyReleased         = errors.New("Aircraft already released")
	ErrBeaconMismatch                  = errors.New("Beacon code mismatch")
	ErrControllerAlreadySignedIn       = errors.New("Controller with that callsign already signed in")
	ErrClearedForUnexpectedApproach    = errors.New("Cleared for unexpected approach")
	ErrDuplicateACID                   = errors.New("Duplicate ACID")
	ErrDuplicateBeacon                 = errors.New("Duplicate beacon code")
	ErrFixIsTooFarAway                 = errors.New("Fix is too far away")
	ErrFixNotInRoute                   = errors.New("Fix not in aircraft's route")
	ErrIllegalACID                     = errors.New("Illegal ACID")
	ErrIllegalACType                   = errors.New("Illegal aircraft type")
	ErrIllegalBeaconCode               = errors.New("Illegal beacon code")
	ErrIllegalFunction                 = errors.New("Illegal function")
	ErrIllegalScratchpad               = errors.New("Illegal scratchpad")
	ErrInvalidAbbreviatedFP            = errors.New("Invalid abbreviated flight plan")
	ErrInvalidApproach                 = errors.New("Invalid approach")
	ErrInvalidDepartureController      = errors.New("Invalid departure controller")
	ErrInvalidFix                      = errors.New("Invalid fix")
	ErrInvalidRestrictionAreaIndex     = errors.New("Invalid restriction area index")
	ErrNoMatchingFlight                = errors.New("No matching flight")
	ErrNoMatchingFlightPlan            = errors.New("No matching flight plan")
	ErrNoMoreListIndices               = errors.New("No more list indices")
	ErrNoVFRAircraftForFlightFollowing = errors.New("No VFR aircraft available for flight following")
	ErrNotClearedForApproach           = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute                  = errors.New("Aircraft is not currently flying its assigned route")
	ErrNotLaunchController             = errors.New("Not signed in as the launch controller")
	ErrTooManyRestrictionAreas         = errors.New("Too many restriction areas specified")
	ErrTrackIsActive                   = errors.New("Track is already active")
	ErrTrackIsBeingHandedOff           = errors.New("Track is currently being handed off")
	ErrTrackIsNotActive                = errors.New("Track is not active")
	ErrUnableCommand                   = errors.New("Unable")
	ErrUnknownAircraftType             = errors.New("Unknown aircraft type")
	ErrUnknownApproach                 = errors.New("Unknown approach")
	ErrUnknownController               = errors.New("Unknown controller")
	ErrUnknownControllerFacility       = errors.New("Unknown controller facility")
	ErrVFRSimTookTooLong               = errors.New("VFR simulation took too long")
	ErrViolatedAirspace                = errors.New("Violated B/C airspace")
)
