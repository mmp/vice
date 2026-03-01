// cmd/updatesay/extract.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// ProcedureInfo tracks airports and full names (with numbers) for a SID/STAR base name.
type ProcedureInfo struct {
	Airports  map[string]struct{} // ICAO codes
	FullNames map[string]struct{} // Full names with numbers (e.g., "GEP1", "GEP2")
}

// NewProcedureInfo creates an initialized ProcedureInfo.
func NewProcedureInfo() *ProcedureInfo {
	return &ProcedureInfo{
		Airports:  make(map[string]struct{}),
		FullNames: make(map[string]struct{}),
	}
}

// ExtractFromAirports extracts fixes and SIDs from airport definitions.
// SIDs are stored with their associated airport ICAO codes and full names.
func ExtractFromAirports(airports map[string]*av.Airport, fixes map[string]struct{}, sids map[string]*ProcedureInfo) {
	for icao, airport := range airports {
		// DepartureRoutes: runway -> (exit -> route)
		for _, exitRoutes := range airport.DepartureRoutes {
			for _, route := range exitRoutes {
				if base := extractBaseName(route.SID); base != "" {
					if sids[base] == nil {
						sids[base] = NewProcedureInfo()
					}
					sids[base].Airports[icao] = struct{}{}
					if route.SID != "" {
						sids[base].FullNames[route.SID] = struct{}{}
					}
				}
				addFixesFromWaypoints(route.Waypoints, fixes)
			}
		}

		// Approaches: name -> Approach
		for _, approach := range airport.Approaches {
			for _, wps := range approach.Waypoints {
				addFixesFromWaypoints(wps, fixes)
			}
		}

		// Departures
		for _, dep := range airport.Departures {
			addFixesFromWaypoints(dep.RouteWaypoints, fixes)
		}

		// VFR Routes
		for _, route := range airport.VFR.Routes {
			addFixesFromWaypoints(route.Waypoints, fixes)
		}
	}
}

// ExtractFromInboundFlows extracts fixes and STARs from inbound flow definitions.
// STARs are stored with their associated airport ICAO codes and full names.
func ExtractFromInboundFlows(flows map[string]*av.InboundFlow, fixes map[string]struct{}, stars map[string]*ProcedureInfo) {
	for _, flow := range flows {
		for _, arrival := range flow.Arrivals {
			if base := extractBaseName(arrival.STAR); base != "" {
				if stars[base] == nil {
					stars[base] = NewProcedureInfo()
				}
				if arrival.STAR != "" {
					stars[base].FullNames[arrival.STAR] = struct{}{}
				}
				// Get airports from Airlines map
				for airportICAO := range arrival.Airlines {
					stars[base].Airports[airportICAO] = struct{}{}
				}
				// Also get airports from RunwayWaypoints
				for airportICAO := range arrival.RunwayWaypoints {
					stars[base].Airports[airportICAO] = struct{}{}
				}
			}
			addFixesFromWaypoints(arrival.Waypoints, fixes)

			// RunwayWaypoints: airport -> runway -> waypoints
			for _, runways := range arrival.RunwayWaypoints {
				for _, wps := range runways {
					addFixesFromWaypoints(wps, fixes)
				}
			}
		}

		for _, overflight := range flow.Overflights {
			addFixesFromWaypoints(overflight.Waypoints, fixes)
		}
	}
}

// ExtractFromReportingPoints extracts fixes from reporting points.
func ExtractFromReportingPoints(rps []av.ReportingPoint, fixes map[string]struct{}) {
	for _, rp := range rps {
		if isValidFix(rp.Fix) {
			fixes[rp.Fix] = struct{}{}
		}
	}
}

// AddFixesFromMap adds valid fixes from a map of fix names to locations.
func AddFixesFromMap(fixMap map[string]math.Point2LL, fixes map[string]struct{}) {
	for name := range fixMap {
		if isValidFix(name) {
			fixes[name] = struct{}{}
		}
	}
}

// ExtractFromDB extracts fixes and STARs from the aviation database.
// STARs are stored with their associated airport ICAO codes and full names.
func ExtractFromDB(fixes map[string]struct{}, stars map[string]*ProcedureInfo) {
	// Add all 5-letter fixes from the database
	for name := range av.DB.Fixes {
		if isValidFix(name) {
			fixes[name] = struct{}{}
		}
	}

	// Add all 3-letter navaids (VORs)
	for name := range av.DB.Navaids {
		if isValidFix(name) {
			fixes[name] = struct{}{}
		}
	}

	// Add STARs from all airports in the database
	for icao, airport := range av.DB.Airports {
		for starName := range airport.STARs {
			if base := extractBaseName(starName); base != "" {
				if stars[base] == nil {
					stars[base] = NewProcedureInfo()
				}
				stars[base].Airports[icao] = struct{}{}
				stars[base].FullNames[starName] = struct{}{}
			}
		}
	}
}

// GetVORPronunciations returns pronunciations for 3-letter VORs from the database.
// Returns a map of VOR identifier to its name (pronunciation).
func GetVORPronunciations(fixes map[string]struct{}, existing map[string]string) map[string]string {
	result := make(map[string]string)
	for name := range fixes {
		if len(name) == 3 {
			if _, exists := existing[name]; exists {
				continue // Already have pronunciation
			}
			if navaid, ok := av.DB.Navaids[name]; ok {
				result[name] = navaid.Name
			}
		}
	}
	return result
}

// SplitFixesByLength separates 3-letter VORs from 5-letter fixes.
func SplitFixesByLength(fixes map[string]struct{}) (vors, fiveLetterFixes map[string]struct{}) {
	vors = make(map[string]struct{})
	fiveLetterFixes = make(map[string]struct{})
	for name := range fixes {
		if len(name) == 3 {
			vors[name] = struct{}{}
		} else if len(name) == 5 {
			fiveLetterFixes[name] = struct{}{}
		}
	}
	return
}

// SplitByLength separates 3-letter items from longer ones.
// Returns (threeLetterItems, longerItems) with full procedure info.
func SplitByLength(items map[string]*ProcedureInfo) (threeLetter, longer map[string]*ProcedureInfo) {
	threeLetter = make(map[string]*ProcedureInfo)
	longer = make(map[string]*ProcedureInfo)
	for name, info := range items {
		if len(name) == 3 {
			threeLetter[name] = info
		} else {
			longer[name] = info
		}
	}
	return
}

// addFixesFromWaypoints extracts valid fixes from a waypoint array.
func addFixesFromWaypoints(wps av.WaypointArray, fixes map[string]struct{}) {
	for _, wp := range wps {
		if isValidFix(wp.Fix) {
			fixes[wp.Fix] = struct{}{}
		}
	}
}

// isAllAlpha checks if a string contains only A-Z letters.
func isAllAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// stripTrailingNumbers removes trailing digits from a string.
// e.g., "BRWRY7" -> "BRWRY"
func stripTrailingNumbers(s string) string {
	for len(s) > 0 && unicode.IsDigit(rune(s[len(s)-1])) {
		s = s[:len(s)-1]
	}
	return s
}

// isValidFix checks if a string is a valid fix name (3 or 5 letters, all alpha).
func isValidFix(s string) bool {
	if len(s) != 3 && len(s) != 5 {
		return false
	}
	return isAllAlpha(s)
}

// extractBaseName extracts the base name from a SID or STAR name.
// Strips trailing numbers and validates that the result is 3-5 letters.
func extractBaseName(name string) string {
	if name == "" {
		return ""
	}
	base := stripTrailingNumbers(name)
	if len(base) < 3 || len(base) > 5 {
		return ""
	}
	if !isAllAlpha(base) {
		return ""
	}
	return base
}
