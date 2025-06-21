package eram

import (
	"errors"
)

type ERAMError struct {
	error
}

func NewERAMError(msg string) *ERAMError {
	return &ERAMError{errors.New(msg)}
}

var (
	ErrCommandFormat = NewERAMError("Command format error")
)