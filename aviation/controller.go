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
	Position           string    // This is the key in the controllers map in JSON
	RadioName          string    `json:"radio_name"`
	Frequency          Frequency `json:"frequency"`
	SectorID           string    `json:"sector_id"`     // e.g. N56, 2J, ...
	Scope              string    `json:"scope_char"`    // Optional. If unset, facility id is used for external, last char of sector id for local.
	FacilityIdentifier string    `json:"facility_id"`   // For example the "N" in "N4P" showing the N90 TRACON
	ERAMFacility       bool      `json:"eram_facility"` // To weed out N56 and N4P being the same fac
	Facility           string    `json:"facility"`      // So we can get the STARS facility from a controller
	Area               int       `json:"-"`             // Auto-derived from first digit of SectorID (e.g., "1A" -> area 1)
}

func (c Controller) IsExternal() bool {
	return c.ERAMFacility || c.FacilityIdentifier != ""
}

func (c Controller) PositionId() ControlPosition {
	if c.ERAMFacility {
		return ControlPosition(c.SectorID)
	}
	return ControlPosition(c.FacilityIdentifier + c.SectorID)
}

func (c Controller) ERAMID() string { // For display
	if c.ERAMFacility {
		return c.SectorID // Already includes the facility letter
	}
	return c.FacilityIdentifier + c.SectorID
}
