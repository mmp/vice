// pkg/sim/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
)

var (
	ErrAircraftAlreadyReleased     = errors.New("Aircraft already released")
	ErrBeaconMismatch              = errors.New("Beacon code mismatch")
	ErrDuplicateACID               = errors.New("Duplicate ACID")
	ErrDuplicateBeacon             = errors.New("Duplicate beacon code")
	ErrControllerAlreadySignedIn   = errors.New("Controller with that callsign already signed in")
	ErrIllegalACID                 = errors.New("Illegal ACID")
	ErrIllegalACType               = errors.New("Illegal aircraft type")
	ErrIllegalFunction             = errors.New("Illegal function")
	ErrIllegalScratchpad           = errors.New("Illegal scratchpad")
	ErrInvalidAbbreviatedFP        = errors.New("Invalid abbreviated flight plan")
	ErrInvalidDepartureController  = errors.New("Invalid departure controller")
	ErrInvalidRestrictionAreaIndex = errors.New("Invalid restriction area index")
	ErrNoMatchingFlightPlan        = errors.New("No matching flight plan")
	ErrNoMatchingFlight            = errors.New("No matching flight")
	ErrNoMoreListIndices           = errors.New("No more list indices")
	ErrNotLaunchController         = errors.New("Not signed in as the launch controller")
	ErrTooManyRestrictionAreas     = errors.New("Too many restriction areas specified")
	ErrTrackIsActive               = errors.New("Track is already active")
	ErrTrackIsNotActive            = errors.New("Track is not active")
	ErrTrackIsBeingHandedOff       = errors.New("Track is currently being handed off")
	ErrUnknownController           = errors.New("Unknown controller")
	ErrUnknownControllerFacility   = errors.New("Unknown controller facility")
	ErrViolatedAirspace            = errors.New("Violated B/C airspace")
	ErrVFRSimTookTooLong           = errors.New("VFR simulation took too long")
)
