// aviation/airspeed.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mmp/vice/math"
)

func DensityRatioAtAltitude(alt float32) float32 {
	altm := alt * 0.3048 // altitude in meters

	// https://en.wikipedia.org/wiki/Barometric_formula#Density_equations
	const g0 = 9.80665    // gravitational constant, m/s^2
	const M_air = 0.02897 // molar mass of earth's air, kg/mol
	const R = 8.314463    // universal gas constant J/(mol K)
	const T_b = 288.15    // reference temperature at sea level, degrees K

	return math.FastExp(-g0 * M_air * altm / (R * T_b))
}

func IASToTAS(ias, altitude float32) float32 {
	return ias / math.Sqrt(DensityRatioAtAltitude(altitude))
}

func TASToIAS(tas, altitude float32) float32 {
	return tas * math.Sqrt(DensityRatioAtAltitude(altitude))
}

func TASToMach(tas float32, temp Temperature) float32 {
	// speed of sound = sqrt(ratio of specific heats (1.4) * gas constant for dry air (287 J/(kg*K)) * temperature in kelvin)
	// convert to knots (* 1.94384)
	sound := math.Sqrt(1.4*287*temp.Kelvin()) * 1.94384
	return tas / sound
}

func MachToTAS(mach float32, temp Temperature) float32 {
	sound := math.Sqrt(1.4*287*temp.Kelvin()) * 1.94384
	return mach * sound
}

///////////////////////////////////////////////////////////////////////////
// Airspeed

// Airspeed is a speed an aircraft is flying: either an indicated airspeed in
// knots or a Mach number, which is what an aircraft holds in the flight levels
// and what a scenario needs when its aircraft may spawn at a range of
// altitudes.
type Airspeed struct {
	Value  float32
	IsMach bool
}

func MakeIAS(ias float32) Airspeed { return Airspeed{Value: ias} }

func MakeMach(mach float32) Airspeed { return Airspeed{Value: mach, IsMach: true} }

func (a Airspeed) IsZero() bool { return a.Value == 0 }

// IAS returns the indicated airspeed to fly at the given altitude.
func (a Airspeed) IAS(altitude float32, temp Temperature) float32 {
	if !a.IsMach {
		return a.Value
	}
	return TASToIAS(MachToTAS(a.Value, temp), altitude)
}

func (a Airspeed) String() string {
	if a.IsMach {
		return fmt.Sprintf("M%02d", int(a.Value*100+.5))
	}
	return strconv.FormatFloat(float64(a.Value), 'f', -1, 32)
}

// MarshalJSON writes knots as a number and a Mach number as a string, the
// forms UnmarshalJSON and the scenario files use.
func (a Airspeed) MarshalJSON() ([]byte, error) {
	if a.IsMach {
		return json.Marshal(a.String())
	}
	return json.Marshal(a.Value)
}

// UnmarshalJSON accepts a plain number, taken as knots, or a string in the
// same form speed restrictions use, which allows "M85" for Mach 0.85.
func (a *Airspeed) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var ias float32
	if err := json.Unmarshal(b, &ias); err == nil {
		a.Value = ias
		return nil
	}

	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	sr, err := ParseSpeedRestriction(str)
	if err != nil {
		return err
	}
	v, exact := sr.ExactValue()
	if !exact {
		return fmt.Errorf("%s: must be a single speed, not a range", str)
	}
	a.Value, a.IsMach = v, sr.IsMach
	return nil
}

// CheckJSON implements util.JSONChecker so the JSON type checker accepts both
// the plain number and the string form.
func (a Airspeed) CheckJSON(json any) bool {
	switch json.(type) {
	case float64, string, nil:
		return true
	}
	return false
}
