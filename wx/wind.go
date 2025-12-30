// wx/wind.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// WindSpecifier defines constraints for sampling weather data
type WindSpecifier struct {
	Direction   string `json:"direction,omitempty"`    // e.g., "30-90", "270", empty for any
	Speed       string `json:"speed,omitempty"`        // e.g., "5+", "5-", "5-15", empty for any
	FlightRules string `json:"flight_rules,omitempty"` // "VMC" or "IMC", empty defaults to "VMC"
}

// Validate checks if the wind specifier is valid
func (ws *WindSpecifier) Validate() error {
	if ws.Direction != "" {
		if err := validateDirection(ws.Direction); err != nil {
			return fmt.Errorf("invalid direction %q: %w", ws.Direction, err)
		}
	}

	if ws.Speed != "" {
		if err := validateSpeed(ws.Speed); err != nil {
			return fmt.Errorf("invalid speed %q: %w", ws.Speed, err)
		}
	}

	if ws.FlightRules != "" && ws.FlightRules != "VMC" && ws.FlightRules != "IMC" {
		return fmt.Errorf("invalid flight_rules %q: must be \"VMC\" or \"IMC\"", ws.FlightRules)
	}

	return nil
}

// validateDirection validates direction range strings like "30-90"
func validateDirection(dir string) error {
	// Range format: "min-max"
	parts := strings.Split(dir, "-")
	if len(parts) != 2 {
		return fmt.Errorf("direction range must be in format \"min-max\"")
	}

	min, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return fmt.Errorf("invalid minimum direction: %w", err)
	}
	max, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("invalid maximum direction: %w", err)
	}

	if min < 0 || min > 360 {
		return fmt.Errorf("minimum direction %d out of range [0, 360]", min)
	}
	if max < 0 || max > 360 {
		return fmt.Errorf("maximum direction %d out of range [0, 360]", max)
	}

	// Calculate range size
	rangeSize := (max - min + 360) % 360
	if rangeSize >= 180 {
		return fmt.Errorf("direction range must be < 180 degrees (got %d degrees from %d to %d)", rangeSize, min, max)
	}

	return nil
}

// validateSpeed validates speed strings like "5+", "5-", "5-15"
func validateSpeed(speed string) error {
	speed = strings.TrimSpace(speed)

	if strings.HasSuffix(speed, "+") {
		// Minimum speed: "5+"
		val := strings.TrimSuffix(speed, "+")
		if _, err := strconv.Atoi(val); err != nil {
			return fmt.Errorf("invalid minimum speed: %w", err)
		}
	} else if strings.HasSuffix(speed, "-") {
		// Maximum speed: "5-"
		val := strings.TrimSuffix(speed, "-")
		if _, err := strconv.Atoi(val); err != nil {
			return fmt.Errorf("invalid maximum speed: %w", err)
		}
	} else if strings.Contains(speed, "-") {
		// Range: "5-15"
		parts := strings.Split(speed, "-")
		if len(parts) != 2 {
			return fmt.Errorf("speed range must be in format \"min-max\"")
		}
		min, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return fmt.Errorf("invalid minimum speed: %w", err)
		}
		max, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return fmt.Errorf("invalid maximum speed: %w", err)
		}
		if min >= max {
			return fmt.Errorf("minimum speed %d must be less than maximum speed %d", min, max)
		}
	} else {
		return fmt.Errorf("speed must be in format \"N+\", \"N-\", or \"N-M\"")
	}

	return nil
}

// Matches checks if a METAR matches this wind specifier
func (ws *WindSpecifier) Matches(metar METAR, magVar float32) bool {
	// Check direction if specified
	if ws.Direction != "" {
		if metar.WindDir == nil {
			// Variable winds don't match a specific direction requirement
			return false
		}

		windMagnetic := float32(*metar.WindDir) - magVar
		if !matchesDirection(ws.Direction, windMagnetic) {
			return false
		}
	}

	if ws.Speed != "" {
		if !matchesSpeed(ws.Speed, metar.WindSpeed) {
			return false
		}
	}

	if ws.FlightRules != "" {
		if ws.FlightRules == "VMC" && !metar.IsVMC() {
			return false
		} else if ws.FlightRules == "IMC" && metar.IsVMC() {
			return false
		}
	}

	return true
}

// matchesDirection checks if a magnetic heading matches the direction specifier
func matchesDirection(dirSpec string, heading float32) bool {
	// Normalize heading to [0, 360)
	heading = math.NormalizeHeading(heading)

	// Range format
	parts := strings.Split(dirSpec, "-")
	min, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	max, _ := strconv.Atoi(strings.TrimSpace(parts[1]))

	if min <= max {
		// Simple range, no wrap
		return heading >= float32(min) && heading <= float32(max)
	} else {
		// Wraps through North
		return heading >= float32(min) || heading <= float32(max)
	}
}

// matchesSpeed checks if a wind speed matches the speed specifier
func matchesSpeed(speedSpec string, windSpeed int) bool {
	speedSpec = strings.TrimSpace(speedSpec)

	if strings.HasSuffix(speedSpec, "+") {
		// Minimum speed
		val := strings.TrimSuffix(speedSpec, "+")
		min, _ := strconv.Atoi(val)
		return windSpeed >= min
	} else if strings.HasSuffix(speedSpec, "-") {
		// Maximum speed
		val := strings.TrimSuffix(speedSpec, "-")
		max, _ := strconv.Atoi(val)
		return windSpeed <= max
	} else if strings.Contains(speedSpec, "-") {
		// Range
		parts := strings.Split(speedSpec, "-")
		min, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		max, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		return windSpeed >= min && windSpeed <= max
	}

	return false
}

// SampleMETARWithSpec randomly samples a METAR that matches the wind specifier
func SampleMETARWithSpec(metar []METAR, intervals []util.TimeInterval, spec *WindSpecifier, magVar float32) *METAR {
	if spec == nil || (spec.Direction == "" && spec.Speed == "" && spec.FlightRules == "") {
		// No constraints, use default sampling with no direction constraint
		return SampleMETAR(metar, intervals, 0)
	}

	// Filter METARs that match the specifier and sample from them
	return SampleMatchingMETAR(metar, intervals, func(m METAR) bool {
		return spec.Matches(m, magVar)
	})
}
