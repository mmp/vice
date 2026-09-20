// aviation/aviation.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mmp/vice/math"

	"github.com/vmihailenco/msgpack/v5"
)

// Temperature represents a temperature value with explicit unit safety.
// Internally stored as Celsius; use Celsius() or Kelvin() to access.
// JSON and msgpack serialize as a plain float in Celsius for wire compatibility.
type Temperature struct {
	celsius float32
}

func MakeTemperatureFromCelsius(c float32) Temperature {
	return Temperature{celsius: c}
}

func MakeTemperatureFromKelvin(k float32) Temperature {
	return Temperature{celsius: k - 273.15}
}

func (t Temperature) Celsius() float32 { return t.celsius }
func (t Temperature) Kelvin() float32  { return t.celsius + 273.15 }

func (t Temperature) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.celsius)
}

func (t *Temperature) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &t.celsius)
}

func (t Temperature) MarshalMsgpack() ([]byte, error) {
	return msgpack.Marshal(t.celsius)
}

func (t *Temperature) UnmarshalMsgpack(data []byte) error {
	return msgpack.Unmarshal(data, &t.celsius)
}

type RadarTrack struct {
	ADSBCallsign        ADSBCallsign
	Squawk              Squawk
	Mode                TransponderMode
	Ident               bool
	TrueAltitude        float32
	TransponderAltitude float32
	Location            math.Point2LL
	Heading             math.MagneticHeading
	Groundspeed         float32
	TypeOfFlight        TypeOfFlight
}

type ADSBCallsign string

func (c ADSBCallsign) String() string { return string(c) }

// Frequencies are scaled by 1000 and then stored in integers.
type Frequency int

func NewFrequency(f float32) Frequency {
	// 0.5 is key for handling rounding!
	return Frequency(f*1000 + 0.5)
}

func (f Frequency) String() string {
	s := fmt.Sprintf("%03d.%03d", f/1000, f%1000)
	for len(s) < 7 {
		s += "0"
	}
	return s
}

///////////////////////////////////////////////////////////////////////////

type FlightRules int

const (
	FlightRulesUnknown FlightRules = iota
	FlightRulesIFR
	FlightRulesVFR
	FlightRulesDVFR
	FlightRulesSVFR
)

func (f FlightRules) String() string {
	return [...]string{"Unknown", "IFR", "VFR", "DVFR", "SVFR"}[f]
}

// FlightPlan represents the flight plan from the perspective of the
// Aircraft: who they are, what they're doing, how they're going to get
// there.
type FlightPlan struct {
	Rules            FlightRules
	AircraftType     string
	CruiseSpeed      int
	DepartureAirport ICAOAirportCode
	DepartureRunway  string
	Altitude         int
	ArrivalAirport   ICAOAirportCode
	AlternateAirport ICAOAirportCode
	Exit             ExitID
	Route            string
	Remarks          string
}

func FormatAltitude(falt float32) string {
	alt := 100 * int(math.Floor(falt/100))
	if alt >= 18000 {
		return "FL" + strconv.Itoa(alt/100)
	}

	sign := ""
	if alt < 0 {
		sign, alt = "-", -alt
	}
	if alt < 1000 {
		return sign + strconv.Itoa(alt)
	}
	th, hu := alt/1000, alt%1000
	if hu == 0 {
		return sign + strconv.Itoa(th) + ",000"
	}
	return sign + fmt.Sprintf("%d,%03d", th, hu)
}

type TransponderMode int

const (
	TransponderModeStandby  TransponderMode = iota /* off */
	TransponderModeAltitude                        /* mode C */
	TransponderModeOn                              /* mode A */
)

func (t TransponderMode) String() string {
	return [...]string{"Standby", "Altitude", "On"}[t]
}
