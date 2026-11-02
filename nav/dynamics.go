// nav/dynamics.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"log/slog"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// FlightState is the aircraft's physical state: where it is and how it is
// moving. The methods here are the flight model, which moves it one second
// at a time toward the targets that guidance chooses from the aircraft's
// Instructions. They know the aircraft's performance and the weather but
// nothing of what it has been told to do.
type FlightState struct {
	InitialDepartureClimb     bool
	DepartureAirportLocation  math.Point2LL
	DepartureAirportElevation float32
	ArrivalAirport            av.Waypoint
	ArrivalAirportLocation    math.Point2LL
	ArrivalAirportElevation   float32

	MagneticVariation float32
	NmPerLongitude    float32

	Position     math.Point2LL
	Heading      math.MagneticHeading
	Altitude     float32
	PrevAltitude float32
	IAS, GS      float32 // speeds...
	BankAngle    float32 // degrees
	AltitudeRate float32 // + -> climb, - -> descent
}

func (fs FlightState) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("initial_departure_climb", fs.InitialDepartureClimb),
		slog.Any("position", fs.Position),
		slog.Float64("heading", float64(fs.Heading)),
		slog.Float64("altitude", float64(fs.Altitude)),
		slog.Float64("ias", float64(fs.IAS)),
		slog.Float64("gs", float64(fs.GS)),
	)
}

// TAS returns the aircraft's true airspeed in knots.
func (fs *FlightState) TAS(temp av.Temperature) float32 {
	return av.IASToTAS(fs.IAS, fs.Altitude, temp)
}

// cruiseIAS returns the indicated airspeed that gives the aircraft's cruise
// TAS at its current altitude.
func (fs *FlightState) cruiseIAS(perf av.AircraftPerformance, temp av.Temperature) float32 {
	return av.TASToIAS(perf.Speed.CruiseTAS, fs.Altitude, temp)
}

// airborne reports whether the aircraft is flying rather than still on its
// takeoff roll. Only its speed says so; "on the ground" isn't modeled
// explicitly.
func (fs *FlightState) airborne(perf av.AircraftPerformance) bool {
	return fs.IAS >= v2(perf)
}

func v2(perf av.AircraftPerformance) float32 {
	if perf.Speed.V2 == 0 {
		// Unfortunately we don't always have V2 in the performance database, so approximate...
		return 0.95 * perf.Speed.Landing
	}
	return perf.Speed.V2
}

// stepIAS returns the indicated airspeed one second from now as the
// aircraft accelerates or decelerates toward target, no faster than rate
// knots per minute. Climbing to targetAltitude costs acceleration and
// descending costs deceleration, unless the descent is geometric and needs
// the speed authority to meet a restriction. An aircraft expediting a climb
// or descent holds its speed.
func (fs *FlightState) stepIAS(perf av.AircraftPerformance, target, rate, targetAltitude float32,
	geometricDescent, expediting bool, temp av.Temperature) float32 {
	if !fs.InitialDepartureClimb && targetAltitude > fs.Altitude && perf.Engine.AircraftType == "P" {
		// Climbing prop; bleed off speed.
		limit := (v2(perf) + fs.cruiseIAS(perf, temp)) * 0.5
		if fs.IAS > limit {
			return max(fs.IAS*.99, limit)
		}
	}

	if expediting {
		return fs.IAS
	}

	switch {
	case fs.IAS < target:
		accel := perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, rate/60)
		if !fs.airborne(perf) {
			// Rough approximation of it being easier to accelerate on the
			// ground and when going slow than when going fast (and
			// airborne).
			if fs.IAS < 40 {
				accel *= 3
			} else {
				accel *= 2
			}
		} else if fs.Altitude < targetAltitude && !geometricDescent {
			// Reduce acceleration since also climbing, but only for
			// controller-assigned altitudes. Geometric climbs/descents
			// need full speed authority to meet restrictions.
			if fs.InitialDepartureClimb {
				// But less so in the initial climb, assuming full power.
				accel *= 0.8
			} else {
				accel *= 0.6
			}
		}
		if fs.Altitude >= 25000 {
			// Thinner air at high altitude makes acceleration harder
			accel *= 0.6
		}
		return min(target, fs.IAS+accel)

	case fs.IAS > target:
		decel := perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, rate/60)
		if fs.Altitude > targetAltitude && !geometricDescent {
			// Reduce deceleration since also descending, but only for
			// controller-assigned altitudes.
			decel *= 0.6
		}
		if fs.Altitude >= 25000 {
			// Thinner air at high altitude makes deceleration harder
			decel *= 0.6
		}
		return max(target, fs.IAS-decel)

	default:
		return fs.IAS
	}
}

const rateMaxDeltaPercent = 0.075

// stepAltitude sets the aircraft's rate of climb or descent for this second
// and returns the altitude it reaches, no farther than target. targetRate
// caps the rate, in feet per minute, and qualifier is how hard the pilot has
// been told to climb or descend. deltaKts is the speed change made this same
// second, which costs climb rate, and descent rate unless the descent is
// geometric. Geometric descents and descents on a glidepath don't ease off
// as they near the target, since they have to meet it at a fix.
func (fs *FlightState) stepAltitude(perf av.AircraftPerformance, target, targetRate float32, geometricDescent bool,
	deltaKts float32, qualifier RateQualifier, onGlidepath bool, wxs wx.Sample) float32 {
	if math.Abs(target-fs.Altitude) < 3 {
		fs.AltitudeRate = 0
		return target
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := perf.Rate.Climb, perf.Rate.Descent

	climb *= atmosClimbFactor(perf, wxs)
	// Reduce rates from highest possible to be more realistic: a normal
	// climb takes 500 fpm off a strong climber's rate, and only an expedited
	// climb or descent ignores the target rate.
	if qualifier == RateNormal && climb >= 2500 && fs.Altitude > 5000 {
		climb -= 500
	}
	if qualifier != RateExpedite {
		climb = min(climb, targetRate)
		descent = min(descent, targetRate)
	}

	// Gradually transition to the target rate.
	maxRateChange := func(baseline float32) float32 {
		change := baseline * rateMaxDeltaPercent
		switch qualifier {
		case RateGood:
			change *= 1.5
		case RateExpedite:
			change *= 2
		}
		return change
	}
	approachRate := func(rate, maxChange float32) {
		diff := rate - fs.AltitudeRate
		if math.Abs(diff) <= maxChange {
			fs.AltitudeRate = rate
		} else if diff > 0 {
			fs.AltitudeRate += maxChange
		} else {
			fs.AltitudeRate -= maxChange
		}
	}

	const rateFadeAltDifference = 500
	if fs.Altitude < target {
		if deltaKts > 0 {
			// accelerating in the climb, so reduce climb rate; the scale
			// factor is w.r.t. the maximum acceleration possible.
			maxAccel := perf.Rate.Accelerate / 2
			climb *= math.Clamp(maxAccel-deltaKts, .25, 1)
		}

		if fs.InitialDepartureClimb {
			fs.AltitudeRate = climb
		} else {
			// Reduce climb rate as we approach target altitude
			altitudeRemaining := target - fs.Altitude
			if altitudeRemaining < rateFadeAltDifference {
				climb *= max(altitudeRemaining/rateFadeAltDifference, 0.25)
			}
			approachRate(climb, maxRateChange(perf.Rate.Climb))
		}
		return min(target, fs.Altitude+fs.AltitudeRate/60)
	}

	if deltaKts < 0 && !geometricDescent {
		// Reduce rate due to concurrent deceleration, but only for
		// controller-assigned altitudes. Geometric descents need the
		// full computed rate to meet the restriction at the fix.
		maxDecel := perf.Rate.Decelerate / 2
		descent *= math.Clamp(maxDecel+deltaKts, .25, 1)
	}

	// Reduce descent rate as we approach target altitude
	// BUT: Don't do this on geometric descents on arrivals or on final approach.
	altitudeRemaining := fs.Altitude - target
	if altitudeRemaining < rateFadeAltDifference && !geometricDescent {
		descent *= max(altitudeRemaining/rateFadeAltDifference, 0.25)
	}

	maxChange := maxRateChange(perf.Rate.Descent)
	// Aircraft on an exact glidepath (past the FAF, on a cleared
	// visual, or landing VFR) need immediate descent rate changes to
	// meet the runway altitude restriction.
	if onGlidepath {
		maxChange = math.Abs(-descent - fs.AltitudeRate)
	}
	approachRate(-descent, maxChange)
	return max(target, fs.Altitude+fs.AltitudeRate/60)
}

// StandardTurnRate is the rate of a standard rate turn in degrees per
// second, the fastest the aircraft turns.
const StandardTurnRate = 3

// bankTurnRate returns the rate of a coordinated turn at the given bank
// angle and true airspeed in knots, in degrees per second.
func bankTurnRate(bank, tas float32) float32 {
	return math.Degrees(9.81 * math.Tan(math.Radians(bank)) / (tas * 0.514444))
}

// turnRateAtBank returns the rate in degrees per second of a coordinated
// turn at the given bank angle and true airspeed in knots, signed with the
// bank and no faster than the standard rate.
func turnRateAtBank(bank, tas float32) float32 {
	return math.Clamp(bankTurnRate(bank, tas), -StandardTurnRate, StandardTurnRate)
}

// roll adjusts the bank for this second of the turn from the present
// heading to target in the given direction: rolling in up to the aircraft's
// maximum bank, and rolling out so as to come level on target. It returns
// which way and how fast, in degrees per second, the aircraft turns this
// second.
func (fs *FlightState) roll(perf av.AircraftPerformance, target math.MagneticHeading, turn av.TurnDirection,
	tas float32) (av.TurnDirection, float32) {
	// signed difference, negative is turn left
	headingDelta := func() float32 {
		diff := float32(target) - float32(fs.Heading)
		switch turn {
		case av.TurnLeft:
			if diff > 0 {
				return diff - 360 // force left turn
			}
			return diff // already left
		case av.TurnRight:
			if diff < 0 {
				return diff + 360 // force right turn
			}
			return diff // already right
		default:
			if diff > 180 {
				diff -= 360
			} else if diff < -180 {
				diff += 360
			}
			return diff
		}
	}()

	maxBankAngle := perf.Turn.MaxBankAngle
	maxRollRate := perf.Turn.MaxBankRate
	turnRate := func(bank float32) float32 { return turnRateAtBank(bank, tas) }

	// If we started leveling out now, how many more degrees would we turn through?
	var levelOutDelta float32
	if fs.BankAngle < 0 {
		for a := fs.BankAngle; a < 0; a += maxRollRate {
			levelOutDelta += turnRate(a)
		}
	} else {
		for a := fs.BankAngle; a > 0; a -= maxRollRate {
			levelOutDelta += turnRate(a)
		}
	}

	if headingDelta < 0 {
		// Turning left
		if levelOutDelta < headingDelta {
			fs.BankAngle += maxRollRate
		} else if fs.BankAngle > -maxBankAngle &&
			levelOutDelta+turnRate(fs.BankAngle-maxRollRate) > headingDelta {
			fs.BankAngle -= maxRollRate
		}
	} else {
		// Turning right
		if levelOutDelta > headingDelta {
			fs.BankAngle -= maxRollRate
		} else if fs.BankAngle < maxBankAngle &&
			levelOutDelta+turnRate(fs.BankAngle+maxRollRate) < headingDelta {
			fs.BankAngle += maxRollRate
		}
	}

	return util.Select(fs.BankAngle < 0, av.TurnLeft, av.TurnRight), math.Abs(turnRate(fs.BankAngle))
}

// turn turns the aircraft toward target this second, through no more than
// rate degrees in the given direction, and levels the wings once it is
// there.
func (fs *FlightState) turn(target math.MagneticHeading, turn av.TurnDirection, rate float32) {
	if fs.Heading == target {
		// BankAngle should be zero(ish) at this point but just to be sure.
		fs.BankAngle = 0
		return
	}
	if math.HeadingDifference(fs.Heading, target) < 1 {
		fs.Heading = target
		fs.BankAngle = 0
		return
	}

	var delta float32
	switch turn {
	case av.TurnLeft:
		angle := float32(math.NormalizeHeading(fs.Heading - target))
		delta = -min(angle, rate)
	case av.TurnRight:
		angle := float32(math.NormalizeHeading(target - fs.Heading))
		delta = min(angle, rate)
	case av.TurnClosest:
		delta = math.Clamp(math.HeadingSignedTurn(fs.Heading, target), -rate, rate)
	}
	fs.Heading = math.OffsetHeading(fs.Heading, delta)
}

// advance moves the aircraft one second along its heading at the given
// true airspeed, drifting with wind, and sets its ground speed.
func (fs *FlightState) advance(tas float32, wind [2]float32) {
	hdg := math.MagneticToTrue(fs.Heading, fs.MagneticVariation)
	flightVector := math.Scale2f(math.SinCos(math.Radians(hdg)), tas/3600)
	v := math.Add2f(flightVector, wind)

	p := math.Add2f(math.LL2NM(fs.Position, fs.NmPerLongitude), v)
	fs.Position = math.NM2LL(p, fs.NmPerLongitude)
	fs.GS = math.Length2f(v) * 3600
}

// atmosClimbFactor returns a factor in [0,1] to model the reduction in
// rate of climb depending on atmospheric conditions.
func atmosClimbFactor(perf av.AircraftPerformance, wxs wx.Sample) float32 {
	var tempCorrection, altCorrection float32
	switch perf.Engine.AircraftType {
	case "J":
		tempCorrection = -0.02 // -2% per °C above ISA
		altCorrection = -0.03  // -3% per 1000 ft
	case "T":
		tempCorrection = -0.015 // -1.5% per °C above ISA
		altCorrection = -0.04   // -4% per 1000 ft
	case "P":
		tempCorrection = -0.03 // -3% per °C above ISA
		altCorrection = -0.08  // -8% per 1000 ft
	default:
		// Default to turbofan if unknown
		tempCorrection = -0.02
		altCorrection = -0.03
	}

	// Temperature factor (only apply if above ISA)
	pressureAltitude := mbToPressureAltitude(wxs.Pressure())
	isaTemp := 15 - (2 * pressureAltitude / 1000)
	tempFactor := float32(1)
	tempDeviation := wxs.Temperature().Celsius() - isaTemp
	if tempDeviation > 0 {
		tempFactor = 1 + (tempCorrection * tempDeviation)
	}

	// Altitude factor
	densityAltitude := pressureAltitude + (120 * tempDeviation)
	altFactor := 1 + (altCorrection * densityAltitude / 1000)

	// Humidity factor
	var humidityFactor float32
	relativeHumidity := wxs.RelativeHumidity()
	switch {
	case relativeHumidity < 40:
		humidityFactor = 1 // No correction
	case relativeHumidity < 70:
		humidityFactor = 0.98 // -2% average
	case relativeHumidity < 90:
		humidityFactor = 0.97 // -3% average
	default:
		humidityFactor = 0.95 // -5% average
	}

	// Ad-hoc clamp and softening to reduce effect at high temperatures.
	// Clamp the product before sqrt: the linear altitude correction goes
	// negative above ~33,000' for jets, which would produce NaN from sqrt.
	return max(0.5, math.Sqrt(max(0, tempFactor*altFactor*humidityFactor)))
}

// mbToPressureAltitude converts pressure in millibars to pressure altitude in feet
// Using the standard atmosphere pressure-altitude relationship
func mbToPressureAltitude(mb float32) float32 {
	// Simplified pressure altitude calculation (more accurate version uses
	// the barometric formula).
	if mb > 226.32 {
		// Troposphere formula (valid up to 36,089 ft)
		return 145366.45 * (1 - math.Pow(mb/1013.25, 0.190284))
	} else {
		// Stratosphere formula
		return 36089.24 + (20806.0 * math.Log(226.32/mb))
	}
}
