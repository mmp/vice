// aviation/airspeed.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mmp/vice/math"
)

// The airspeed conversions follow the International Standard Atmosphere
// (ISA) and treat indicated airspeed as calibrated airspeed. Converting
// between IAS and Mach depends only on the static pressure, which is
// given by the aircraft's pressure altitude; converting between Mach and
// TAS depends only on the temperature, which sets the speed of sound. All
// of them assume subsonic flight.

const seaLevelSpeedOfSound = 661.4788 // knots, ISA

// pressureRatio returns the ratio of the ISA static pressure at the given
// altitude (in feet) to the sea level pressure.
func pressureRatio(alt float32) float32 {
	const tropopause = 36089.24 // feet
	if alt <= tropopause {
		return math.Pow(1-6.8755856e-6*alt, 5.2558797)
	}
	return 0.2233609 * math.FastExp(-4.806346e-5*(alt-tropopause))
}

// IASToMach returns the Mach number corresponding to the given indicated
// airspeed at the given altitude.
func IASToMach(ias, altitude float32) float32 {
	// Impact pressure relative to sea level static pressure, then relative
	// to the static pressure at altitude.
	qc := math.Pow(1+0.2*math.Sqr(ias/seaLevelSpeedOfSound), 3.5) - 1
	qc /= pressureRatio(altitude)
	return math.Sqrt(5 * (math.Pow(qc+1, 2./7) - 1))
}

// MachToIAS returns the indicated airspeed corresponding to the given Mach
// number at the given altitude.
func MachToIAS(mach, altitude float32) float32 {
	qc := math.Pow(1+0.2*mach*mach, 3.5) - 1
	qc *= pressureRatio(altitude)
	return seaLevelSpeedOfSound * math.Sqrt(5*(math.Pow(qc+1, 2./7)-1))
}

// speedOfSound returns the speed of sound in knots at the given
// temperature.
func speedOfSound(temp Temperature) float32 {
	// sqrt(ratio of specific heats (1.4) * gas constant for dry air (287 J/(kg*K)) * temperature in kelvin)
	// converted to knots (* 1.94384)
	return math.Sqrt(1.4*287*temp.Kelvin()) * 1.94384
}

func TASToMach(tas float32, temp Temperature) float32 {
	return tas / speedOfSound(temp)
}

func MachToTAS(mach float32, temp Temperature) float32 {
	return mach * speedOfSound(temp)
}

// IASToTAS returns the true airspeed corresponding to the given indicated
// airspeed at the given altitude and temperature.
func IASToTAS(ias, altitude float32, temp Temperature) float32 {
	return MachToTAS(IASToMach(ias, altitude), temp)
}

// TASToIAS returns the indicated airspeed corresponding to the given true
// airspeed at the given altitude and temperature.
func TASToIAS(tas, altitude float32, temp Temperature) float32 {
	return MachToIAS(TASToMach(tas, temp), altitude)
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
func (a Airspeed) IAS(altitude float32) float32 {
	if !a.IsMach {
		return a.Value
	}
	return MachToIAS(a.Value, altitude)
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
