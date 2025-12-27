// client/state.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"github.com/mmp/vice/server"
)

// SimState is the client's view of simulation state.
// It embeds server.SimState, providing access to all its fields and methods.
type SimState struct {
	server.SimState
}
