// server/errors.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"
)

var (
	ErrControllerAlreadySignedIn = errors.New("Controller with that callsign already signed in")
	ErrDuplicateSimName          = errors.New("A sim with that name already exists")
	ErrInvalidControllerToken    = errors.New("Invalid controller token")
	ErrInvalidPassword           = errors.New("Invalid password")
	ErrInvalidSimConfiguration   = errors.New("Invalid SimConfiguration")
	ErrInvalidTrafficSource      = errors.New("Traffic source is not available for this scenario")
	ErrNoNamedSim                = errors.New("No Sim with that name")
	ErrNoSimForControllerToken   = errors.New("No Sim running for controller token")
	ErrRPCTimeout                = errors.New("RPC call timed out")
	ErrRPCVersionMismatch        = errors.New("Client and server RPC versions don't match")
	ErrServerDisconnected        = errors.New("Server disconnected")
	ErrTCWAlreadyOccupied        = errors.New("TCW is already occupied")
)
