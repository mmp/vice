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
	ErrControllerAlreadySignedIn   = errors.New("Controller with that callsign already signed in")
	ErrIllegalACID                 = errors.New("Illegal ACID")
	ErrIllegalACType               = errors.New("Illegal aircraft type")
	ErrIllegalFunction             = errors.New("Illegal function")
	ErrIllegalScratchpad           = errors.New("Illegal scratchpad")
	ErrInvalidAbbreviatedFP        = errors.New("Invalid abbreviated flight plan")
	ErrInvalidControllerToken      = errors.New("Invalid controller token")
	ErrInvalidDepartureController  = errors.New("Invalid departure controller")
	ErrInvalidRestrictionAreaIndex = errors.New("Invalid restriction area index")
	ErrNoMatchingFlight            = errors.New("No matching flight")
	ErrNotLaunchController         = errors.New("Not signed in as the launch controller")
	ErrTooManyRestrictionAreas     = errors.New("Too many restriction areas specified")
	ErrUnknownController           = errors.New("Unknown controller")
	ErrUnknownControllerFacility   = errors.New("Unknown controller facility")
	ErrViolatedAirspace            = errors.New("Violated B/C airspace")
	ErrVFRSimTookTooLong           = errors.New("VFR simulation took too long")
)
