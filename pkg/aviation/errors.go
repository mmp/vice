package aviation

import "errors"

var (
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrNotClearedForApproach        = errors.New("Aircraft has not been cleared for an approach")
	ErrNotFlyingRoute               = errors.New("Aircraft is not currently flying its assigned route")
	ErrNoValidArrivalFound          = errors.New("Unable to find a valid arrival")
	ErrUnableCommand                = errors.New("Unable")
	ErrUnknownAircraftType          = errors.New("Unknown aircraft type")
	ErrUnknownApproach              = errors.New("Unknown approach")
)
