package eram

import (
	"errors"
	"fmt"
	"net/rpc"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
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
	ErrERAMIllegalACID     = NewERAMError("ILLEGAL ACID")
	ErrERAMIllegalPosition = NewERAMError("ILLEGAL POSITION")
	ErrERAMIllegalValue    = NewERAMError("ILLEGAL VALUE")
	ErrERAMIllegalAirport  = NewERAMError("ILLEGAL AIRPORT")
	ErrIllegalUserAction   = NewERAMError("ILLEGAL USER ACTION")
	ErrERAMMapUnavailable  = NewERAMError("MAP UNAVAILABLE")
	ErrERAMMessageTooLong  = NewERAMError("MESSAGE TOO LONG")
	ErrERAMSectorNotActive = NewERAMError("SECTOR NOT ACTIVE")
)

var eramErrorRemap = map[error]*ERAMError{}

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
