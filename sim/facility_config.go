// sim/facility_config.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// FacilityConfig represents an external facility configuration file that
// contains control positions, STARS configuration, handoff IDs, and
// fix pair definitions for a facility (TRACON or ARTCC).
type FacilityConfig struct {
	ControlPositions   map[TCP]*av.Controller `json:"control_positions"`
	FacilityAdaptation FacilityAdaptation     `json:"config"`
	HandoffIDs         []HandoffID            `json:"handoff_ids"`
	FixPairs           []FixPairDefinition    `json:"fix_pairs"`
}

// HandoffID maps a neighboring facility to its STARS identifiers at
// various lengths. These are used to resolve cross-facility sector IDs
// in /ho route values and to prefix neighbor controllers when loaded
// into this facility's controller set. Centers not listed in
// handoff_ids default to prefix "C".
type HandoffID struct {
	ID                string `json:"id"`
	Prefix            string `json:"prefix,omitempty"`
	SingleCharStarsID string `json:"single_char_stars_id,omitempty"`
	TwoCharStarsID    string `json:"two_char_stars_id,omitempty"`
	StarsID           string `json:"stars_id,omitempty"`
	FieldEFormat      string `json:"field_e_format,omitempty"`
	FieldELetter      string `json:"field_e_letter,omitempty"`
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
// FacilityConfiguration alongside inbound/departure assignments.
type FixPairAssignment struct {
	TCP      TCP `json:"tcp"`      // Controller assigned to handle this fix pair
	Priority int `json:"priority"` // Priority for deterministic matching
}

// PostDeserialize validates the facility config right after JSON
// deserialization. The facility name is the base filename without
// extension (e.g. "N90", "ZDC"). Errors are accumulated in e.
func (fc *FacilityConfig) PostDeserialize(facility string, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	// Determine whether this facility is an ARTCC.
	_, isARTCC := av.DB.ARTCCs[facility]

	// Set ERAMFacility on all controllers if this is an ARTCC config.
	if isARTCC {
		for _, ctrl := range fc.ControlPositions {
			ctrl.ERAMFacility = true
		}
	}

	// Populate Position from the map key. Controller.Position has no
	// JSON tag so it's empty after unmarshal; the canonical position is
	// the map key (e.g. "08", "1N").
	for position, ctrl := range fc.ControlPositions {
		if ctrl.Position == "" {
			ctrl.Position = string(position)
		}
	}

	e.Push("facility " + facility)
	defer e.Pop()

	// Validate control positions (before auto-deriving Area, so
	// redundancy checks only flag user-defined values).
	for position, ctrl := range fc.ControlPositions {
		e.Push("Controller " + string(position))

		if ctrl.Frequency < 118000 || ctrl.Frequency > 138000 {
			e.ErrorString("invalid frequency: %6.3f", float32(ctrl.Frequency)/1000)
		}
		if ctrl.RadioName == "" {
			e.ErrorString(`no "radio_name" specified`)
		}

		if !ctrl.ERAMFacility && strings.HasSuffix(strings.ToLower(ctrl.RadioName), "center") {
			e.ErrorString(`radio name ends in "center" but this is not an ARTCC facility`)
		}
		if ctrl.ERAMFacility {
			if _, err := strconv.Atoi(ctrl.Position); err != nil {
				e.ErrorString("center controller position must be a 2-digit number: %s", ctrl.Position)
			}
		} else {
			if len(position) != 2 {
				e.ErrorString("controller position must be a 2 characters")
			}
		}

		if ctrl.Scope != "" {
			if ctrl.FacilityIdentifier == ctrl.Scope {
				e.ErrorString(`"scope_char" is redundant since it matches "facility_id"`)
			}
			if !ctrl.ERAMFacility && ctrl.FacilityIdentifier == "" && len(ctrl.Position) > 0 &&
				ctrl.Scope == string(ctrl.Position[len(ctrl.Position)-1]) {
				e.ErrorString(`"scope_char" is redundant since it matches the last character of position`)
			}
		}
		if len(ctrl.Scope) > 1 {
			e.ErrorString(`"scope_char" may only be a single character`)
		}

		if ctrl.Area != "" && !ctrl.ERAMFacility && len(ctrl.Position) > 0 &&
			ctrl.Position[0] >= '0' && ctrl.Position[0] <= '9' &&
			ctrl.Area == string(ctrl.Position[0]) {
			e.ErrorString(`"area" is redundant since it matches the first digit of position`)
		}

		e.Pop()
	}

	// Note: Area auto-derivation for TRACON controllers is done in
	// rewriteControllers, not here. PostDeserialize may be called
	// multiple times on cached configs, so it must not mutate.

	// Validate handoff IDs.
	seenIDs := make(map[string]bool)
	for _, hid := range fc.HandoffIDs {
		e.Push(fmt.Sprintf("handoff_id %q", hid.ID))

		if hid.ID == "" {
			e.ErrorString(`"id" must not be empty`)
			e.Pop()
			continue
		}
		if seenIDs[hid.ID] {
			e.ErrorString("duplicate handoff id %q", hid.ID)
		}
		seenIDs[hid.ID] = true

		hasPrefix := hid.Prefix != ""
		hasStarsID := hid.StarsID != "" || hid.SingleCharStarsID != "" || hid.TwoCharStarsID != "" || hid.FieldEFormat != "" || hid.FieldELetter != ""

		// XOR: exactly one identification scheme.
		if hasPrefix && hasStarsID {
			e.ErrorString(`cannot specify both "prefix" and stars_id fields`)
		} else if !hasPrefix && !hasStarsID {
			e.ErrorString(`must specify either "prefix" or "stars_id"`)
		}

		_, neighborIsARTCC := av.DB.ARTCCs[hid.ID]

		if neighborIsARTCC {
			// ARTCC neighbors always use a letter prefix.
			if !hasPrefix {
				e.ErrorString(`ARTCC neighbor must use "prefix"`)
			} else if len(hid.Prefix) != 1 || hid.Prefix[0] < 'A' || hid.Prefix[0] > 'Z' {
				e.ErrorString("ARTCC prefix must be a single letter A-Z, got %q", hid.Prefix)
			}
		} else if isARTCC {
			// Primary is ARTCC, neighbor is TRACON: must use stars_id mode.
			if hasPrefix {
				e.ErrorString(`TRACON neighbor of ARTCC must use "stars_id", not "prefix"`)
			}
			if hid.StarsID == "" {
				e.ErrorString(`"stars_id" is required for TRACON neighbor of ARTCC`)
			}
			if len(hid.StarsID) != 3 {
				e.ErrorString(`"stars_id" must be exactly 3 characters for "FullStarsIdOnly"`)
			}
			// Validate field lengths when set.
			if hid.SingleCharStarsID != "" && len(hid.SingleCharStarsID) != 1 {
				e.ErrorString(`"single_char_stars_id" must be exactly 1 character`)
			}
			if hid.TwoCharStarsID != "" && len(hid.TwoCharStarsID) != 2 {
				e.ErrorString(`"two_char_stars_id" must be exactly 2 characters`)
			}
			if hid.FieldELetter != "" && len(hid.FieldELetter) != 1 {
				e.ErrorString(`"field_e_letter" must be exactly 1 character`)
			}

			// validate field e format
			switch hid.FieldEFormat {
			case "OneLetterAndSubset":
				if hid.FieldELetter == "" && hid.SingleCharStarsID == "" {
					e.ErrorString(`"field_e_letter" or "single_char_stars_id" is required for "OneLetterAndSubset"`)
				}
			case "TwoLetters":
				if hid.TwoCharStarsID == "" {
					e.ErrorString(`"two_char_stars_id" is required for "TwoLetters"`)
				}
			case "TwoLettersAndSubset":
				if hid.TwoCharStarsID == "" {
					e.ErrorString(`"two_char_stars_id" is required for "TwoLettersAndSubset"`)
				}
			case "OneLetterAndStarsIdOnly":
				if hid.FieldELetter == "" {
					e.ErrorString(`"field_e_letter" is required for "OneLetterAndStarsIdOnly"`)
				}
			case "FullStarsIdOnly":
			case "":
				e.ErrorString(`"field_e_format" is required for TRACON neighbor of ARTCC`)
			default:
				e.ErrorString(`invalid field_e_format "%q"`, hid.FieldEFormat)
			}
		} else {
			// Primary is TRACON, neighbor is TRACON: must use a digit prefix.
			if hasPrefix {
				if len(hid.Prefix) != 1 || hid.Prefix[0] < '1' || hid.Prefix[0] > '9' {
					e.ErrorString("TRACON prefix must be a single digit 1-9, got %q", hid.Prefix)
				}
			}
			if hasStarsID {
				e.ErrorString("TRACON neighbor of TRACON must not use stars_id fields")
			}
		}

		e.Pop()
	}

	// Fix pair validation (TODO)
	// - priority uniqueness
	// - flight_type in {"A", "P", "E", ""}
	// - altitude_range floor <= ceiling

	fc.validateAdaptation(isARTCC, e)
}

func (fc *FacilityConfig) validateAdaptation(isARTCC bool, e *util.ErrorLogger) {
	fa := &fc.FacilityAdaptation
	e.Push("config")
	defer e.Pop()

	// Validate configurations (facility configurations).
	if fa.Configurations == nil {
		e.ErrorString(`must provide "configurations"`)
	}
	for configId, config := range fa.Configurations {
		e.Push("configurations: " + configId)

		if len(configId) > 3 {
			e.ErrorString("configuration id %q must be at most 3 characters", configId)
		}

		for flow, tcp := range config.InboundAssignments {
			if _, ok := fc.ControlPositions[tcp]; !ok {
				e.ErrorString(`inbound_assignments: flow %q assigns to %q which is not in "control_positions"`, flow, tcp)
			}
		}
		for spec, tcp := range config.DepartureAssignments {
			if _, ok := fc.ControlPositions[tcp]; !ok {
				e.ErrorString(`departure_assignments: %q assigns to %q which is not in "control_positions"`, spec, tcp)
			}
		}

		for parent, children := range config.DefaultConsolidation {
			if _, ok := fc.ControlPositions[parent]; !ok {
				e.ErrorString(`default_consolidation: parent %q is not in "control_positions"`, parent)
			}
			for _, child := range children {
				if _, ok := fc.ControlPositions[child]; !ok {
					e.ErrorString(`default_consolidation: child %q (under %q) is not in "control_positions"`, child, parent)
				}
			}
		}

		// Resolve scratchpad leader line direction strings to native directions.
		if len(config.ScratchpadLeaderLineDirectionStrings) > 0 {
			config.ScratchpadLeaderLineDirections = make(map[string]math.CardinalOrdinalDirection,
				len(config.ScratchpadLeaderLineDirectionStrings))
			for sp, dirStr := range config.ScratchpadLeaderLineDirectionStrings {
				if !fa.CheckScratchpad(sp) {
					e.ErrorString("scratchpad_leader_line_directions: invalid scratchpad %q", sp)
					continue
				}
				dir, err := math.ParseCardinalOrdinalDirection(dirStr)
				if err != nil {
					e.ErrorString("scratchpad_leader_line_directions: invalid direction %q for scratchpad %q", dirStr, sp)
					continue
				}
				config.ScratchpadLeaderLineDirections[sp] = dir
			}
		}

		e.Pop()
	}

	// Top-level beacon code blocks.
	if fa.MonitoredBeaconCodeBlocksString == nil {
		fa.MonitoredBeaconCodeBlocks = []av.Squawk{0o12} // 12xx block by default
	} else {
		for bl := range strings.SplitSeq(*fa.MonitoredBeaconCodeBlocksString, ",") {
			bl = strings.TrimSpace(bl)
			if code, err := av.ParseSquawkOrBlock(bl); err != nil {
				e.ErrorString(`invalid beacon code %q in "beacon_code_blocks": %v`, bl, err)
			} else {
				fa.MonitoredBeaconCodeBlocks = append(fa.MonitoredBeaconCodeBlocks, code)
			}
		}
	}

	if isARTCC {
		fc.validateERAMAdaptation(e)
	} else {
		fc.validateSTARSAdaptation(e)
	}
}

func (fc *FacilityConfig) validateSTARSAdaptation(e *util.ErrorLogger) {
	fa := &fc.FacilityAdaptation

	// Video map labels must reference known stars_maps.
	for m := range fa.VideoMapLabels {
		if !slices.Contains(fa.VideoMapNames, m) {
			e.ErrorString(`video map %q in "map_labels" is not in "stars_maps"`, m)
		}
	}

	// controller_configs TCP existence.
	if len(fa.ControllerConfigs) > 0 {
		for tcp := range fa.ControllerConfigs {
			if ctrl, ok := fc.ControlPositions[TCP(tcp)]; !ok {
				e.ErrorString(`Control position %q in "controller_configs" not defined in "control_positions"`, tcp)
			} else if ctrl.IsExternal() {
				e.ErrorString(`Control position %q in "controller_configs" is external and not in this TRACON.`, tcp)
			}
		}
	} else if len(fa.VideoMapNames) == 0 {
		e.ErrorString(`Must specify either "controller_configs" or "stars_maps"`)
	}

	if len(fa.VideoMapNames) == 0 {
		if len(fa.ControllerConfigs) == 0 {
			e.ErrorString(`must provide one of "stars_maps" or "controller_configs" with "video_maps" in "config"`)
		}
		var err error
		fa.ControllerConfigs, err = util.CommaKeyExpand(fa.ControllerConfigs)
		if err != nil {
			e.Error(err)
		}
	}

	if fa.Range == 0 {
		fa.Range = 50
	}
	if fa.HandoffAcceptFlashDuration == 0 {
		fa.HandoffAcceptFlashDuration = 5
	}

	// PDB mutual exclusion.
	if fa.PDB.SplitGSAndCWT && fa.PDB.ShowAircraftType {
		e.ErrorString(`Both "split_gs_and_cwt" and "show_aircraft_type" cannot be specified for "pdb" adaption.`)
	}
	if fa.PDB.SplitGSAndCWT && fa.PDB.HideGroundspeed {
		e.ErrorString(`Both "split_gs_and_cwt" and "hide_gs" cannot be specified for "pdb" adaption.`)
	}
	if fa.PDB.DisplayCustomSPCs && len(fa.CustomSPCs) == 0 {
		e.ErrorString(`"display_custom_spcs" was set but none were defined in "custom_spcs".`)
	}

	// Scratchpad1 mutual exclusion.
	disp := make(map[string]any)
	if fa.Scratchpad1.DisplayExitFix {
		disp["display_exit_fix"] = nil
	}
	if fa.Scratchpad1.DisplayExitFix1 {
		disp["display_exit_fix_1"] = nil
	}
	if fa.Scratchpad1.DisplayExitGate {
		disp["display_exit_gate"] = nil
	}
	if fa.Scratchpad1.DisplayAltExitGate {
		disp["display_alternate_exit_gate"] = nil
	}
	if len(disp) > 1 {
		d := util.SortedMapKeys(disp)
		d = util.MapSlice(d, func(s string) string { return `"` + s + `"` })
		e.ErrorString(`Cannot specify %s for "scratchpad1"`, strings.Join(d, " and "))
	}

	// Custom SPCs.
	for _, spc := range fa.CustomSPCs {
		if len(spc) != 2 || spc[0] < 'A' || spc[0] > 'Z' || spc[1] < 'A' || spc[1] > 'Z' {
			e.ErrorString(`Invalid "custom_spcs" code %q: must be two characters between A-Z`, spc)
		}
		if av.StringIsSPC(spc) {
			e.ErrorString("%q is a standard SPC already", spc)
		}
	}

	// Untracked position symbol overrides.
	if fa.UntrackedPositionSymbolOverrides.CodeRangesString != "" {
		e.Push("untracked_position_symbol_overrides")
		for c := range strings.SplitSeq(fa.UntrackedPositionSymbolOverrides.CodeRangesString, ",") {
			low, high, ok := strings.Cut(c, "-")

			var err error
			var r [2]av.Squawk
			r[0], err = av.ParseSquawk(low)
			if err != nil {
				e.ErrorString(`invalid beacon code %q in "beacon_codes": %v`, low, err)
			} else if ok {
				r[1], err = av.ParseSquawk(high)
				if err != nil {
					e.ErrorString(`invalid beacon code %q in "beacon_codes": %v`, high, err)
				} else if r[0] > r[1] {
					e.ErrorString("first code %q in range must be less than or equal to second %q", low, high)
				}
			} else {
				r[1] = r[0]
			}
			fa.UntrackedPositionSymbolOverrides.CodeRanges = append(fa.UntrackedPositionSymbolOverrides.CodeRanges, r)
		}

		if len(fa.UntrackedPositionSymbolOverrides.Symbol) == 0 {
			e.ErrorString(`"symbol" must be provided if "untracked_position_symbol_overrides" is specified`)
		} else if len(fa.UntrackedPositionSymbolOverrides.Symbol) > 1 {
			e.ErrorString(`only one character may be provided for "symbol"`)
		}
		e.Pop()
	}

	// Coordination lists: name/id required, id uniqueness.
	seenIds := make(map[string][]string)
	for _, list := range fa.CoordinationLists {
		e.Push(`"coordination_lists" ` + list.Name)

		if list.Name == "" {
			e.ErrorString(`"name" must be specified for coordination list.`)
		}
		if list.Id == "" {
			e.ErrorString(`"id" must be specified for coordination list.`)
		}
		if len(list.Airports) == 0 {
			e.ErrorString(`At least one airport must be specified in "airports" for coordination list.`)
		}

		seenIds[list.Id] = append(seenIds[list.Id], list.Name)

		e.Pop()
	}
	for id, groups := range seenIds {
		if len(groups) > 1 {
			e.ErrorString(`Multiple "coordination_lists" are using id %q: %s`, id, strings.Join(groups, ", "))
		}
	}

	// Restriction areas: non-spatial checks.
	e.Push(`"restriction_areas"`)
	if len(fa.RestrictionAreas) > av.MaxRestrictionAreas {
		e.ErrorString("No more than %d restriction areas may be specified; %d were given.",
			av.MaxRestrictionAreas, len(fa.RestrictionAreas))
	}
	for idx := range fa.RestrictionAreas {
		ra := &fa.RestrictionAreas[idx]

		if ra.Title == "" {
			e.ErrorString(`Must define "title" for restriction area.`)
		}
		for i := range 2 {
			if len(ra.Text[i]) > 32 {
				e.ErrorString(`Maximum of 32 characters per line in "text": line %d: %q (%d)`,
					i, ra.Text, len(ra.Text[i]))
			}
		}
		if ra.Color < 0 || ra.Color > 8 {
			e.ErrorString(`"color" must be between 1 and 8 (inclusive).`)
		}
		if ra.Shaded && ra.CircleRadius == 0 && len(ra.Vertices) == 0 && len(ra.VerticesUser) == 0 {
			e.ErrorString(`"shaded" cannot be specified without "circle_radius" or "vertices".`)
		}
	}
	e.Pop()
}

func (fc *FacilityConfig) validateERAMAdaptation(e *util.ErrorLogger) {
	fa := &fc.FacilityAdaptation

	// Validate area configs if present.
	if len(fa.AreaConfigs) > 0 {
		usedAreas := make(map[string]bool)
		for _, ctrl := range fc.ControlPositions {
			if ctrl.Area != "" {
				usedAreas[ctrl.Area] = true
			}
		}
		for areaNum := range fa.AreaConfigs {
			if !usedAreas[areaNum] {
				e.ErrorString("area_configs: area %s has no controllers assigned to it", areaNum)
			}
		}
	}
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
