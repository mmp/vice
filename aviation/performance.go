// aviation/performance.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import "github.com/mmp/vice/math"

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string  `json:"weightClass"`
	Ceiling     float32 `json:"ceiling"`
	Engine      struct {
		// AircraftType is "P" for piston, "T" for turboprop, "J" for jet, and
		// "H" for rotorcraft.
		AircraftType string `json:"type"`
	} `json:"engines"`
	Rate struct {
		Climb      float32 `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    float32 `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Category struct {
		SRS   int    `json:"srs"`
		LAHSO int    `json:"lahso"`
		CWT   string `json:"cwt"`
	}
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min        float32 `json:"min"`
		V2         float32 `json:"v2"`
		Landing    float32 `json:"landing"`
		CruiseTAS  float32 `json:"cruise"`
		CruiseMach float32 `json:"cruiseM"`
		MaxTAS     float32 `json:"max"`
		MaxMach    float32 `json:"maxM"`
	} `json:"speed"`
	Turn struct {
		MaxBankAngle float32 `json:"maxBankAngle"`
		MaxBankRate  float32 `json:"maxBankRate"`
	}
	Capacity struct {
		Passengers int `json:"passengers"`
		FuelPounds int `json:"fuel_pounds"`
	} `json:"capacity"`
}
type Airline struct {
	ICAO     string `json:"icao"`
	Name     string `json:"name"`
	Callsign struct {
		Name            string   `json:"name"`
		CallsignFormats []string `json:"callsignFormats"`
	} `json:"callsign"`
	JSONFleets map[string][][2]any `json:"fleets"`
	Fleets     map[string][]FleetAircraft
}
type FleetAircraft struct {
	ICAO  string
	Count int
}

func (ap AircraftPerformance) baseApproachSpeed() float32 {
	if ap.Speed.Landing > 0 {
		return ap.Speed.Landing + 5
	} else if ap.Speed.V2 > 0 {
		return 1.25 * ap.Speed.V2
	} else {
		return 120
	}
}
func (ap AircraftPerformance) ApproachSpeed(windDirection, windSpeed, windGust float32, runwayHeading float32) float32 {
	gustFactor := max(0, windGust-windSpeed)

	additive := float32(0)
	switch ap.Engine.AircraftType {
	case "J", "T":
		diff := math.HeadingDifference(windDirection, runwayHeading)
		headwind := max(0, float32(windSpeed)*math.Cos(math.Radians(diff)))
		additive = min(headwind/2+gustFactor, 20)
	case "P":
		additive = gustFactor / 2
	}

	return ap.baseApproachSpeed() + additive
}
