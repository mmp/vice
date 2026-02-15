// sim/facility_config.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"sort"

	av "github.com/mmp/vice/aviation"
)

// FacilityConfig represents an external facility configuration file that
// contains control positions, STARS configuration, handoff topology, and
// fix pair definitions for a facility (TRACON or ARTCC).
type FacilityConfig struct {
	ControlPositions   map[TCP]*av.Controller `json:"control_positions"`
	FacilityAdaptation FacilityAdaptation     `json:"stars_config"`
	HandoffTopology    *HandoffTopology       `json:"handoff_topology"`
	FixPairs           []FixPairDefinition    `json:"fix_pairs"`
}

// HandoffTopology describes the handoff relationships between a facility
// and its neighbors. This data is sourced from handoffs_output.json.
type HandoffTopology struct {
	HandoffIDs            []HandoffID `json:"handoff_ids"`
	NeighboringFacilities []string    `json:"neighboring_facilities"`
}

// HandoffID maps a neighboring facility to its STARS identifiers at
// various lengths. These are used to resolve cross-facility sector IDs
// in /ho route values and to prefix neighbor controllers when loaded
// into this facility's controller set. Centers not listed in
// handoff_ids default to prefix "C".
type HandoffID struct {
	ID                string `json:"id"`
	Prefix            string `json:"prefix,omitempty"`
	ERAMPrefix        string `json:"eram_prefix,omitempty"`
	SingleCharStarsID string `json:"single_char_stars_id,omitempty"`
	TwoCharStarsID    string `json:"two_char_stars_id,omitempty"`
	StarsID           string `json:"stars_id,omitempty"`
}

// FixPairDefinition defines a fixed pair (entry fix, exit fix) with
// optional constraints. Fix pairs are used in TRACON facility configs
// to provide fine-grained routing rules for aircraft assignment.
type FixPairDefinition struct {
	EntryFix      string `json:"entry_fix"`                // Entry fix name, empty = wildcard
	ExitFix       string `json:"exit_fix"`                 // Exit fix name, empty = wildcard
	FlightType    string `json:"flight_type,omitempty"`    // "A" (arrival), "P" (departure), "E" (overflight), empty = any
	AltitudeRange [2]int `json:"altitude_range,omitempty"` // [floor, ceiling] in feet; [0,0] = no constraint
	Priority      int    `json:"priority"`                 // Lower number = higher priority; must be unique per config
}

// FixPairAssignment maps a fix pair definition (by index) to a controller
// TCP for a specific configuration. These are stored in
// ControllerAssignments alongside inbound/departure assignments.
type FixPairAssignment struct {
	FixPairIndex int `json:"fix_pair_index"` // Index into FacilityConfig.FixPairs
	TCP          TCP `json:"tcp"`            // Controller assigned to handle this fix pair
	Priority     int `json:"priority"`       // Priority for deterministic matching
}

// MatchFixPair finds the highest-priority fix pair that matches the given
// aircraft parameters. Returns the index into the FixPairs slice and true
// if a match is found, or -1 and false otherwise.
func MatchFixPair(fixPairs []FixPairDefinition, entryFix, exitFix string, flightType av.TypeOfFlight, altitude int) (int, bool) {
	// Sort by priority (lower = higher priority)
	type indexedPair struct {
		index int
		pair  FixPairDefinition
	}
	sorted := make([]indexedPair, len(fixPairs))
	for i, fp := range fixPairs {
		sorted[i] = indexedPair{index: i, pair: fp}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].pair.Priority < sorted[j].pair.Priority
	})

	for _, ip := range sorted {
		fp := ip.pair

		// Entry fix match (empty = wildcard)
		if fp.EntryFix != "" && fp.EntryFix != entryFix {
			continue
		}

		// Exit fix match (empty = wildcard)
		if fp.ExitFix != "" && fp.ExitFix != exitFix {
			continue
		}

		// Flight type match (empty = any)
		if fp.FlightType != "" {
			var ftStr string
			switch flightType {
			case av.FlightTypeArrival:
				ftStr = "A"
			case av.FlightTypeDeparture:
				ftStr = "P"
			case av.FlightTypeOverflight:
				ftStr = "E"
			}
			if fp.FlightType != ftStr {
				continue
			}
		}

		// Altitude range match ([0,0] = no constraint)
		if fp.AltitudeRange[0] != 0 || fp.AltitudeRange[1] != 0 {
			if altitude < fp.AltitudeRange[0] || altitude > fp.AltitudeRange[1] {
				continue
			}
		}

		return ip.index, true
	}

	return -1, false
}
