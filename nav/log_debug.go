//go:build navlog

// nav/log_debug.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
)

// Navigation logging configuration
var (
	navlogEnabled    bool
	navlogCategories map[string]bool
	navlogCallsign   string // filter to only log this callsign (empty = log all)
)

// InitNavLog initializes the navigation logging system
func InitNavLog(enabled bool, categories string, callsign string) {
	navlogEnabled = enabled
	navlogCategories = make(map[string]bool)
	navlogCallsign = strings.TrimSpace(callsign)

	if !enabled {
		return
	}

	// Parse categories
	if categories == "" || categories == "all" {
		// Enable all categories
		navlogCategories[NavLogState] = true
		navlogCategories[NavLogWaypoint] = true
		navlogCategories[NavLogAltitude] = true
		navlogCategories[NavLogSpeed] = true
		navlogCategories[NavLogHeading] = true
		navlogCategories[NavLogApproach] = true
		navlogCategories[NavLogCommand] = true
		navlogCategories[NavLogRoute] = true
		navlogCategories[NavLogHold] = true
	} else {
		// Enable specified categories
		cats := strings.Split(categories, ",")
		for _, cat := range cats {
			cat = strings.TrimSpace(cat)
			navlogCategories[cat] = true
		}
	}
}

// NavLog logs a message with timestamp, callsign, and category
func NavLog(callsign string, simTime time.Time, category string, format string, args ...interface{}) {
	if !navlogEnabled {
		return
	}

	if !navlogCategories[category] {
		return
	}

	// Filter by callsign if specified
	if navlogCallsign != "" && navlogCallsign != callsign {
		return
	}

	// Format: [HH:MM:SS] [callsign] [category] message
	timeStr := simTime.Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] [%s] [%s] %s\n", timeStr, callsign, category, message)
}

// NavLogEnabled returns whether navigation logging is enabled for a given category
func NavLogEnabled(category string) bool {
	return navlogEnabled && navlogCategories[category]
}

// LogRoute logs the current route for an aircraft
func LogRoute(callsign string, simTime time.Time, waypoints av.WaypointArray) {
	if !navlogEnabled || !navlogCategories[NavLogRoute] {
		return
	}

	// Filter by callsign if specified
	if navlogCallsign != "" && navlogCallsign != callsign {
		return
	}

	route := waypoints.RouteString()
	if route == "" {
		route = "(no route)"
	}

	timeStr := simTime.Format("15:04:05")
	fmt.Printf("[%s] [%s] [%s] route: %s\n", timeStr, callsign, NavLogRoute, route)
}
