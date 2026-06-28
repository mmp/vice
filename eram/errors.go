package eram

import (
	"errors"
	"fmt"
	"net/rpc"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

type ERAMError struct {
	error
}

func NewERAMError(msg string, args ...any) *ERAMError {
	return &ERAMError{errors.New(fmt.Sprintf(msg, args...))}
}

var ( // TODO: Get actual error messages for this
	ErrCommandFormat       = NewERAMError("FORMAT")
	ErrERAMAmbiguousACID   = NewERAMError("AMB ACID")
	ErrERAMIllegalACID     = NewERAMError("ILL CID")
	ErrERAMIllegalPosition = NewERAMError("ILLEGAL POSITION")
	ErrERAMIllegalValue    = NewERAMError("ILLEGAL VALUE")
	ErrERAMIllegalAirport  = NewERAMError("ILLEGAL AIRPORT")
	ErrIllegalUserAction   = NewERAMError("ILLEGAL USER ACTION")
	ErrERAMMapUnavailable  = NewERAMError("MAP UNAVAILABLE")
	ErrERAMMessageTooLong  = NewERAMError("MESSAGE TOO LONG")
	ErrERAMSectorNotActive = NewERAMError("SECTOR NOT ACTIVE")
)

var eramErrorRemap = map[error]*ERAMError{
	av.ErrBadPoolSpecifier:           ErrERAMIllegalValue,
	av.ErrInvalidAltitude:            ErrERAMIllegalValue,
	av.ErrInvalidController:          ErrERAMIllegalPosition,
	av.ErrInvalidFacility:            ErrERAMIllegalACID,
	av.ErrInvalidHeading:             ErrERAMIllegalValue,
	av.ErrNoAircraftForCallsign:      ErrERAMIllegalACID,
	av.ErrNoController:               ErrERAMSectorNotActive,
	av.ErrNoFlightPlan:               ErrERAMIllegalACID,
	av.ErrNoMatchingFix:              ErrERAMIllegalValue,
	av.ErrNoMoreAvailableSquawkCodes: ErrERAMIllegalValue,
	av.ErrNoValidDepartureFound:      ErrIllegalUserAction,
	av.ErrNotBeingHandedOffToMe:      ErrIllegalUserAction,
	av.ErrNotPointedOutByMe:          ErrIllegalUserAction,
	av.ErrNotPointedOutToMe:          ErrERAMIllegalACID,
	av.ErrOtherControllerHasTrack:    ErrIllegalUserAction,
	av.ErrUnknownAirport:             ErrERAMIllegalAirport,
	av.ErrUnknownRunway:              ErrERAMIllegalValue,

	nav.ErrClearedForUnexpectedApproach: ErrERAMIllegalValue,
	nav.ErrFixIsTooFarAway:              ErrERAMIllegalValue,
	nav.ErrFixNotInRoute:                ErrERAMIllegalValue,
	nav.ErrInvalidApproach:              ErrERAMIllegalValue,
	nav.ErrInvalidFix:                   ErrERAMIllegalValue,
	nav.ErrNotClearedForApproach:        ErrERAMIllegalValue,
	nav.ErrNotFlyingRoute:               ErrERAMIllegalValue,
	nav.ErrUnableCommand:                ErrERAMIllegalValue,
	nav.ErrUnknownApproach:              ErrERAMIllegalValue,

	sim.ErrATPADisabled:                    ErrIllegalUserAction,
	sim.ErrAircraftAlreadyReleased:         ErrIllegalUserAction,
	sim.ErrBeaconMismatch:                  ErrERAMIllegalValue,
	sim.ErrDuplicateACID:                   ErrERAMIllegalACID,
	sim.ErrDuplicateBeacon:                 ErrERAMIllegalValue,
	sim.ErrIllegalACID:                     ErrERAMIllegalACID,
	sim.ErrIllegalACType:                   ErrERAMIllegalValue,
	sim.ErrIllegalATIS:                     ErrERAMIllegalValue,
	sim.ErrIllegalBeaconCode:               ErrERAMIllegalValue,
	sim.ErrIllegalFunction:                 ErrIllegalUserAction,
	sim.ErrIllegalLine:                     ErrERAMIllegalValue,
	sim.ErrIllegalPosition:                 ErrERAMIllegalPosition,
	sim.ErrIllegalScratchpad:               ErrERAMIllegalValue,
	sim.ErrInvalidAbbreviatedFP:            ErrCommandFormat,
	sim.ErrInvalidDepartureController:      ErrIllegalUserAction,
	sim.ErrInvalidRestrictionAreaIndex:     ErrERAMIllegalValue,
	sim.ErrInvalidVolumeId:                 ErrIllegalUserAction,
	sim.ErrNoACType:                        ErrERAMIllegalValue,
	sim.ErrNoMatchingFlight:                ErrERAMIllegalACID,
	sim.ErrNoMatchingFlightPlan:            ErrERAMIllegalACID,
	sim.ErrNoScratchpad:                    ErrERAMIllegalValue,
	sim.ErrNoVFRAircraftForFlightFollowing: ErrERAMIllegalACID,
	sim.ErrNotLaunchController:             ErrIllegalUserAction,
	sim.ErrTCPAlreadyConsolidated:          ErrIllegalUserAction,
	sim.ErrTCPNotConsolidated:              ErrIllegalUserAction,
	sim.ErrTCWIsConsolidated:               ErrERAMIllegalPosition,
	sim.ErrTCWNotFound:                     ErrERAMIllegalPosition,
	sim.ErrTCWNotVacant:                    ErrERAMIllegalPosition,
	sim.ErrTooManyRestrictionAreas:         ErrIllegalUserAction,
	sim.ErrTrackIsActive:                   ErrIllegalUserAction,
	sim.ErrIllegalTrackLocalFP:             ErrIllegalUserAction,
	sim.ErrTrackIsBeingHandedOff:           ErrIllegalUserAction,
	sim.ErrTrackIsNotActive:                ErrERAMIllegalACID,
	sim.ErrUnknownAircraftType:             ErrERAMIllegalValue,
	sim.ErrUnknownController:               ErrERAMIllegalPosition,
	sim.ErrUnknownControllerFacility:       ErrERAMIllegalPosition,
	sim.ErrFDAMIllegalArea:                 ErrIllegalUserAction,
	sim.ErrFDAMNoRegions:                   ErrIllegalUserAction,
	sim.ErrFDAMProcessingOff:               ErrIllegalUserAction,
	sim.ErrVolumeDisabled:                  ErrIllegalUserAction,
	sim.ErrVolumeNot25nm:                   ErrIllegalUserAction,

	server.ErrInvalidCommandSyntax: ErrCommandFormat,
}

func GetERAMError(e error, lg *log.Logger) *ERAMError {
	if se, ok := e.(*ERAMError); ok {
		return se
	}

	if _, ok := e.(rpc.ServerError); ok {
		e = server.TryDecodeError(e)
	}

	if se, ok := eramErrorRemap[e]; ok {
		return se
	}

	lg.Errorf("%v: unexpected error passed to GetERAMError", e)
	return ErrCommandFormat
}

func (ep *ERAMPane) displayError(err error, ctx *panes.Context) {
	if err != nil {
		ep.feedbackArea.Error(GetERAMError(err, ctx.Lg))
	}
}

// applyCommandStatus routes a CommandStatus to the feedback/response areas:
// an error overrides everything; otherwise non-empty feedback and response
// lines are joined with newlines and shown.
func (ep *ERAMPane) applyCommandStatus(ctx *panes.Context, status CommandStatus, err error) {
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
