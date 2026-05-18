// sim/track_ghost.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

// GhostState is the STARS CRDA ghost-track display state for an aircraft.
// It lives in sim because it is carried on per-ACID annotations that are
// owned and synchronized by the server; stars/ re-exports it as an alias
// for convenience.
type GhostState int

const (
	GhostStateRegular GhostState = iota
	GhostStateSuppressed
	GhostStateForced
)
