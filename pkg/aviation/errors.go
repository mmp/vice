// pkg/aviation/errors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import "errors"

var (
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
	ErrInvalidAltitude              = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidController            = errors.New("Invalid controller")
	ErrInvalidFacility              = errors.New("Invalid facility")
	ErrInvalidHeading               = errors.New("Invalid heading")
	ErrNoAircraftForCallsign        = errors.New("No aircraft exists with specified callsign")
	ErrNoController                 = errors.New("No controller with that callsign")
	ErrNoERAMFacility               = errors.New("No ERAM facility exists")
	ErrNoFlightPlan                 = errors.New("No flight plan has been filed for aircraft")
	ErrNoMatchingFix                = errors.New("No matching fix")
	ErrNoSTARSFacility              = errors.New("No STARS Facility in ERAM computer")
	ErrNotBeingHandedOffToMe        = errors.New("Aircraft not being handed off to current controller")
	ErrNotClearedForApproach        = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute               = errors.New("Aircraft is not currently flying its assigned route")
	ErrNotPointedOutToMe            = errors.New("Aircraft not being pointed out to current controller")
	ErrNoValidArrivalFound          = errors.New("Unable to find a valid arrival")
	ErrNoValidDepartureFound        = errors.New("Unable to find a valid departure")
	ErrOtherControllerHasTrack      = errors.New("Another controller is already tracking the aircraft")
	ErrUnableCommand                = errors.New("Unable")
	ErrUnknownAircraftType          = errors.New("Unknown aircraft type")
	ErrUnknownAirport               = errors.New("Unknown airport")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrUnknownRunway                = errors.New("Unknown runway")
)
