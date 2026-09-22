// nav/log_release.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !navlog

package nav

import (
	av "github.com/mmp/vice/aviation"
)

// InitNavLog is a no-op in release builds
func InitNavLog(enabled bool, categories string, callsign string) {}

// NavLog is a no-op in release builds
func NavLog(callsign string, simTime Time, category string, format string, args ...any) {
}

// NavLogEnabled always returns false in release builds
func NavLogEnabled(category string) bool { return false }

// LogRoute is a no-op in release builds
func LogRoute(callsign string, simTime Time, waypoints av.WaypointArray) {}
