// pkg/aviation/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import "errors"

var (
	ErrInvalidAltitude            = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidController          = errors.New("Invalid controller")
	ErrInvalidFacility            = errors.New("Invalid facility")
	ErrInvalidHeading             = errors.New("Invalid heading")
	ErrInvalidSquawkCode          = errors.New("Invalid squawk code")
	ErrNoAircraftForCallsign      = errors.New("No aircraft exists with specified callsign")
	ErrNoController               = errors.New("No controller with that callsign")
	ErrNoCoordinationFix          = errors.New("No coordination fix found")
	ErrNoERAMFacility             = errors.New("No ERAM facility exists")
	ErrNoFlightPlan               = errors.New("No flight plan has been filed for aircraft")
	ErrNoMatchingFix              = errors.New("No matching fix")
	ErrNoMoreAvailableSquawkCodes = errors.New("No more available squawk codes")
	ErrNoSTARSFacility            = errors.New("No STARS Facility in ERAM computer")
	ErrNoValidArrivalFound        = errors.New("Unable to find a valid arrival")
	ErrNoValidDepartureFound      = errors.New("Unable to find a valid departure")
	ErrNotBeingHandedOffToMe      = errors.New("Aircraft not being handed off to current controller")
	ErrNotPointedOutByMe          = errors.New("Aircraft not being pointed out by current controller")
	ErrNotPointedOutToMe          = errors.New("Aircraft not being pointed out to current controller")
	ErrOtherControllerHasTrack    = errors.New("Another controller is already tracking the aircraft")
	ErrSquawkCodeAlreadyAssigned  = errors.New("Squawk code has already been assigned")
	ErrSquawkCodeNotManagedByPool = errors.New("Squawk code is not managed by this pool")
	ErrSquawkCodeUnassigned       = errors.New("Squawk code has not been assigned")
	ErrUnknownAirport             = errors.New("Unknown airport")
	ErrUnknownRunway              = errors.New("Unknown runway")
)
