// aviation/controller.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

// ControlPosition identifies a controller position in either STARS or ERAM.
// For STARS, this is the TCP (Terminal Control Position) like "2K" or "4P".
// For ERAM, this is the sector identifier like "N56" or "W05".
// This is the generic type used throughout the codebase for any controller position.
type ControlPosition string

type Controller struct {
	Position           string    // This is the key in the controllers map in JSON (now the sector ID, e.g. "1N", "4P")
	Callsign           string    `json:"callsign,omitempty"` // Human-readable callsign (e.g. "PHL_NA_APP")
	RadioName          string    `json:"radio_name"`
	Frequency          Frequency `json:"frequency"`
	Scope              string    `json:"scope_char"`    // Optional. If unset, facility id is used for external, last char of position for local.
	FacilityIdentifier string    `json:"-"`             // Set programmatically by loadNeighborControllers (e.g. "N" in "N4P")
	ERAMFacility       bool      `json:"eram_facility"` // To weed out N56 and N4P being the same fac
	Facility           string    `json:"facility"`      // So we can get the STARS facility from a controller
	Area               int       `json:"-"`             // Auto-derived from first digit of Position (e.g., "1A" -> area 1)
}

func (c Controller) IsExternal() bool {
	return c.ERAMFacility || c.FacilityIdentifier != ""
}

func (c Controller) PositionId() ControlPosition {
	return ControlPosition(c.FacilityIdentifier + c.Position)
}

func (c Controller) ERAMID() string { // For display
	return c.FacilityIdentifier + c.Position
}
