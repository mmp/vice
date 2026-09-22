// eram/errors.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"errors"
	"fmt"
	"net/rpc"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

type Error struct {
	error
}

func NewError(msg string, args ...any) *Error {
	return &Error{errors.New(fmt.Sprintf(msg, args...))}
}

var ( // TODO: Get actual error messages for this
	ErrCommandFormat     = NewError("FORMAT")
	ErrAmbiguousACID     = NewError("AMB ACID")
	ErrIllegalACID       = NewError("ILL CID")
	ErrIllegalPosition   = NewError("ILLEGAL POSITION")
	ErrIllegalValue      = NewError("ILLEGAL VALUE")
	ErrIllegalAirport    = NewError("ILLEGAL AIRPORT")
	ErrIllegalUserAction = NewError("ILLEGAL USER ACTION")
	ErrMapUnavailable    = NewError("MAP UNAVAILABLE")
	ErrMessageTooLong    = NewError("MESSAGE TOO LONG")
	ErrSectorNotActive   = NewError("SECTOR NOT ACTIVE")
)

var eramErrorRemap = map[error]*Error{
	av.ErrBadPoolSpecifier:           ErrIllegalValue,
	av.ErrInvalidAltitude:            ErrIllegalValue,
	av.ErrInvalidController:          ErrIllegalPosition,
	av.ErrInvalidFacility:            ErrIllegalACID,
	av.ErrInvalidHeading:             ErrIllegalValue,
	av.ErrNoAircraftForCallsign:      ErrIllegalACID,
	av.ErrNoController:               ErrSectorNotActive,
	av.ErrNoFlightPlan:               ErrIllegalACID,
	av.ErrNoMatchingFix:              ErrIllegalValue,
	av.ErrNoMoreAvailableSquawkCodes: ErrIllegalValue,
	av.ErrNoValidDepartureFound:      ErrIllegalUserAction,
	av.ErrNotBeingHandedOffToMe:      ErrIllegalUserAction,
	av.ErrNotPointedOutByMe:          ErrIllegalUserAction,
	av.ErrNotPointedOutToMe:          ErrIllegalACID,
	av.ErrOtherControllerHasTrack:    ErrIllegalUserAction,
	av.ErrUnknownAirport:             ErrIllegalAirport,
	av.ErrUnknownRunway:              ErrIllegalValue,

	nav.ErrClearedForUnexpectedApproach: ErrIllegalValue,
	nav.ErrFixIsTooFarAway:              ErrIllegalValue,
	nav.ErrFixNotInRoute:                ErrIllegalValue,
	nav.ErrInvalidApproach:              ErrIllegalValue,
	nav.ErrInvalidFix:                   ErrIllegalValue,
	nav.ErrNotClearedForApproach:        ErrIllegalValue,
	nav.ErrNotFlyingRoute:               ErrIllegalValue,
	nav.ErrUnableCommand:                ErrIllegalValue,
	nav.ErrUnknownApproach:              ErrIllegalValue,

	sim.ErrATPADisabled:                    ErrIllegalUserAction,
	sim.ErrAircraftAlreadyReleased:         ErrIllegalUserAction,
	sim.ErrBeaconMismatch:                  ErrIllegalValue,
	sim.ErrDuplicateACID:                   ErrIllegalACID,
	sim.ErrDuplicateBeacon:                 ErrIllegalValue,
	sim.ErrIllegalACID:                     ErrIllegalACID,
	sim.ErrIllegalACType:                   ErrIllegalValue,
	sim.ErrIllegalATIS:                     ErrIllegalValue,
	sim.ErrIllegalBeaconCode:               ErrIllegalValue,
	sim.ErrIllegalFunction:                 ErrIllegalUserAction,
	sim.ErrIllegalLine:                     ErrIllegalValue,
	sim.ErrIllegalPosition:                 ErrIllegalPosition,
	sim.ErrIllegalScratchpad:               ErrIllegalValue,
	sim.ErrInvalidAbbreviatedFP:            ErrCommandFormat,
	sim.ErrInvalidCommandSyntax:            ErrCommandFormat,
	sim.ErrInvalidDepartureController:      ErrIllegalUserAction,
	sim.ErrInvalidRestrictionAreaIndex:     ErrIllegalValue,
	sim.ErrInvalidVolumeId:                 ErrIllegalUserAction,
	sim.ErrNoACType:                        ErrIllegalValue,
	sim.ErrNoMatchingFlight:                ErrIllegalACID,
	sim.ErrNoMatchingFlightPlan:            ErrIllegalACID,
	sim.ErrNoRecentCommand:                 ErrIllegalUserAction,
	sim.ErrNoScratchpad:                    ErrIllegalValue,
	sim.ErrNoVFRAircraftForFlightFollowing: ErrIllegalACID,
	sim.ErrTCPAlreadyConsolidated:          ErrIllegalUserAction,
	sim.ErrTCPNotConsolidated:              ErrIllegalUserAction,
	sim.ErrTCWIsConsolidated:               ErrIllegalPosition,
	sim.ErrTCWNotFound:                     ErrIllegalPosition,
	sim.ErrTCWNotVacant:                    ErrIllegalPosition,
	sim.ErrTooManyRestrictionAreas:         ErrIllegalUserAction,
	sim.ErrTrackIsActive:                   ErrIllegalUserAction,
	sim.ErrIllegalTrackLocalFP:             ErrIllegalUserAction,
	sim.ErrTrackIsBeingHandedOff:           ErrIllegalUserAction,
	sim.ErrTrackIsNotActive:                ErrIllegalACID,
	sim.ErrUnknownAircraftType:             ErrIllegalValue,
	sim.ErrUnknownController:               ErrIllegalPosition,
	sim.ErrUnknownControllerFacility:       ErrIllegalPosition,
	sim.ErrFDAMIllegalArea:                 ErrIllegalUserAction,
	sim.ErrFDAMNoRegions:                   ErrIllegalUserAction,
	sim.ErrFDAMProcessingOff:               ErrIllegalUserAction,
	sim.ErrVolumeDisabled:                  ErrIllegalUserAction,
	sim.ErrVolumeNot25nm:                   ErrIllegalUserAction,
}

func GetError(e error, lg *log.Logger) *Error {
	if se, ok := e.(*Error); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = client.TryDecodeError(e)
	}

	if se, ok := eramErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetError", e)
	return ErrCommandFormat
}

func (ep *Pane) displayError(err error, ctx *scope.Context) {
	if err != nil {
		ep.feedbackArea.Error(GetError(err, ctx.Lg))
	}
}

// applyCommandStatus routes a CommandStatus to the feedback/response areas:
// an error overrides everything; otherwise non-empty feedback and response
// lines are joined with newlines and shown.
func (ep *Pane) applyCommandStatus(ctx *scope.Context, status CommandStatus, err error) {
	if err != nil {
		ep.displayError(err, ctx)
		return
	}

	if len(status.feedbackArea) > 0 {
		ep.feedbackArea.Success(strings.Join(status.feedbackArea, "\n"))
	}
	if len(status.responseArea) > 0 {
		ep.responseArea = formatInput(strings.Join(status.responseArea, "\n"))
	}
}
