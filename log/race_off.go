// log/race_off.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !race

package log

// RaceEnabled is false when the race detector is not active.
const RaceEnabled = false
