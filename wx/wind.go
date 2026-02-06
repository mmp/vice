// wx/wind.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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

	if before, ok := strings.CutSuffix(speed, "+"); ok {
		// Minimum speed: "5+"
		val := before
		if _, err := strconv.Atoi(val); err != nil {
			return fmt.Errorf("invalid minimum speed: %w", err)
		}
	} else if before, ok := strings.CutSuffix(speed, "-"); ok {
		// Maximum speed: "5-"
		val := before
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

	if before, ok := strings.CutSuffix(speedSpec, "+"); ok {
		// Minimum speed
		val := before
		min, _ := strconv.Atoi(val)
		return windSpeed >= min
	} else if before, ok := strings.CutSuffix(speedSpec, "-"); ok {
		// Maximum speed
		val := before
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

// FlightRulesFilter represents user preference for VMC/IMC
type FlightRulesFilter int

const (
	FlightRulesAny FlightRulesFilter = iota
	FlightRulesVMC
	FlightRulesIMC
)

// GustFilter represents user preference for gusting winds
type GustFilter int

const (
	GustAny GustFilter = iota
	GustYes
	GustNo
)

// WeatherFilter defines UI-specified constraints for sampling weather data.
// All fields are optional; nil/zero means "don't care".
type WeatherFilter struct {
	FlightRules FlightRulesFilter

	// Ground wind speed range in knots (nil means no constraint)
	WindSpeedMin *int
	WindSpeedMax *int

	Gusting GustFilter

	// Ground wind direction range in magnetic degrees (nil means no constraint)
	// Uses the same wraparound logic as WindSpecifier
	WindDirMin *int
	WindDirMax *int

	// Temperature range in Celsius (nil means no constraint)
	TemperatureMin *int
	TemperatureMax *int

	// Winds aloft constraints (for time selection only)
	// WindsAloftSpeedMin/Max in knots (nil means no constraint)
	WindsAloftSpeedMin *int
	WindsAloftSpeedMax *int
	// WindsAloftDirMin/Max in magnetic degrees (nil means no constraint)
	WindsAloftDirMin *int
	WindsAloftDirMax *int
}

// IsEmpty returns true if no filter constraints are set
func (wf *WeatherFilter) IsEmpty() bool {
	return wf.FlightRules == FlightRulesAny &&
		wf.WindSpeedMin == nil && wf.WindSpeedMax == nil &&
		wf.Gusting == GustAny &&
		wf.WindDirMin == nil && wf.WindDirMax == nil &&
		wf.TemperatureMin == nil && wf.TemperatureMax == nil &&
		wf.WindsAloftSpeedMin == nil && wf.WindsAloftSpeedMax == nil &&
		wf.WindsAloftDirMin == nil && wf.WindsAloftDirMax == nil
}

// Matches checks if a METAR matches this weather filter
func (wf *WeatherFilter) Matches(metar METAR, magVar float32) bool {
	// Check flight rules
	switch wf.FlightRules {
	case FlightRulesVMC:
		if !metar.IsVMC() {
			return false
		}
	case FlightRulesIMC:
		if metar.IsVMC() {
			return false
		}
	}

	// Check wind speed
	if wf.WindSpeedMin != nil && metar.WindSpeed < *wf.WindSpeedMin {
		return false
	}
	if wf.WindSpeedMax != nil && metar.WindSpeed > *wf.WindSpeedMax {
		return false
	}

	// Check gusting
	switch wf.Gusting {
	case GustYes:
		if metar.WindGust == nil || *metar.WindGust == 0 {
			return false
		}
	case GustNo:
		if metar.WindGust != nil && *metar.WindGust > 0 {
			return false
		}
	}

	// Check wind direction
	if wf.WindDirMin != nil || wf.WindDirMax != nil {
		if metar.WindDir == nil {
			// Variable winds don't match a specific direction requirement
			return false
		}
		windMagnetic := math.NormalizeHeading(float32(*metar.WindDir) - magVar)

		minDir := 0
		if wf.WindDirMin != nil {
			minDir = *wf.WindDirMin
		}
		maxDir := 360
		if wf.WindDirMax != nil {
			maxDir = *wf.WindDirMax
		}

		if minDir <= maxDir {
			// Simple range, no wrap
			if windMagnetic < float32(minDir) || windMagnetic > float32(maxDir) {
				return false
			}
		} else {
			// Wraps through North
			if windMagnetic < float32(minDir) && windMagnetic > float32(maxDir) {
				return false
			}
		}
	}

	// Check temperature
	if wf.TemperatureMin != nil && metar.Temperature < float32(*wf.TemperatureMin) {
		return false
	}
	if wf.TemperatureMax != nil && metar.Temperature > float32(*wf.TemperatureMax) {
		return false
	}

	return true
}

// SampleMETARWithFilter randomly samples a METAR that matches the weather filter
func SampleMETARWithFilter(metar []METAR, intervals []util.TimeInterval, filter *WeatherFilter, magVar float32) *METAR {
	if filter == nil || filter.IsEmpty() {
		// No constraints, sample any METAR
		return SampleMatchingMETAR(metar, intervals, func(m METAR) bool { return true })
	}

	return SampleMatchingMETAR(metar, intervals, func(m METAR) bool {
		return filter.Matches(m, magVar)
	})
}

// MatchesWindsAloft checks if winds aloft at the specified altitude match the filter constraints.
// atmosByTime provides atmospheric data, t is the time to check, altitudeFeet is the altitude
// in feet, and magVar is the magnetic variation to convert from true to magnetic heading.
func (wf *WeatherFilter) MatchesWindsAloft(atmosByTime *AtmosByTime, t time.Time, altitudeFeet float32, magVar float32) bool {
	// If no winds aloft constraints, always match
	if wf.WindsAloftSpeedMin == nil && wf.WindsAloftSpeedMax == nil &&
		wf.WindsAloftDirMin == nil && wf.WindsAloftDirMax == nil {
		return true
	}

	// Get winds aloft data
	direction, speed, ok := atmosByTime.GetWindsAloftAtTime(t, altitudeFeet)
	if !ok {
		// No data available - treat as no match
		return false
	}

	// Convert true heading to magnetic
	windMagnetic := math.NormalizeHeading(direction - magVar)
	speedKnots := int(speed + 0.5)

	// Check speed constraints
	if wf.WindsAloftSpeedMin != nil && speedKnots < *wf.WindsAloftSpeedMin {
		return false
	}
	if wf.WindsAloftSpeedMax != nil && speedKnots > *wf.WindsAloftSpeedMax {
		return false
	}

	// Check direction constraints
	if wf.WindsAloftDirMin != nil || wf.WindsAloftDirMax != nil {
		minDir := 0
		if wf.WindsAloftDirMin != nil {
			minDir = *wf.WindsAloftDirMin
		}
		maxDir := 360
		if wf.WindsAloftDirMax != nil {
			maxDir = *wf.WindsAloftDirMax
		}

		if minDir <= maxDir {
			// Simple range, no wrap
			if windMagnetic < float32(minDir) || windMagnetic > float32(maxDir) {
				return false
			}
		} else {
			// Wraps through North
			if windMagnetic < float32(minDir) && windMagnetic > float32(maxDir) {
				return false
			}
		}
	}

	return true
}

// SampleWeatherWithFilter randomly samples a METAR that matches both ground wind filter (METAR)
// and winds aloft filter (atmospheric data at specified altitude).
// If atmosByTime is nil, only the ground wind filter is checked.
func SampleWeatherWithFilter(
	metar []METAR,
	atmosByTime *AtmosByTime,
	intervals []util.TimeInterval,
	filter *WeatherFilter,
	windsAloftAltitude float32, // feet AGL, e.g., 5000 for TRACON, 28000 for Center
	magVar float32,
) *METAR {
	if filter == nil || filter.IsEmpty() {
		// No constraints, sample any METAR
		return SampleMatchingMETAR(metar, intervals, func(m METAR) bool { return true })
	}

	return SampleMatchingMETAR(metar, intervals, func(m METAR) bool {
		// Check ground wind filter
		if !filter.Matches(m, magVar) {
			return false
		}

		// Check winds aloft filter
		if atmosByTime != nil {
			if !filter.MatchesWindsAloft(atmosByTime, m.Time, windsAloftAltitude, magVar) {
				return false
			}
		}

		return true
	})
}
