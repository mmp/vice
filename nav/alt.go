// nav/alt.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

const MaximumRate = 100000

func (nav *Nav) updateAltitude(callsign string, targetAltitude, targetRate float32, deltaKts float32, slowingTo250 bool, wxs wx.Sample, simTime time.Time) {
	nav.FlightState.PrevAltitude = nav.FlightState.Altitude

	NavLog(callsign, simTime, NavLogAltitude, "target=%.0f current=%.0f rate=%.0f targetRate=%.0f expedite=%v slowingTo250=%v",
		targetAltitude, nav.FlightState.Altitude, nav.FlightState.AltitudeRate, targetRate, nav.Altitude.Expedite, slowingTo250)

	if targetAltitude == nav.FlightState.Altitude {
		if nav.IsAirborne() && nav.FlightState.InitialDepartureClimb {
			nav.FlightState.InitialDepartureClimb = false
		}
		nav.FlightState.AltitudeRate = 0
		nav.Altitude.Expedite = false
		return
	}

	// Wrap altitude setting in a lambda so we can detect when we pass
	// through an altitude for "at alt, reduce speed" sort of assignments.
	setAltitude := func(next float32) {
		if nav.Speed.AfterAltitude != nil &&
			(nav.Speed.Assigned == nil || *nav.Speed.Assigned == nav.FlightState.IAS) {
			cur := nav.FlightState.Altitude
			at := *nav.Speed.AfterAltitudeAltitude
			if (cur > at && next <= at) || (cur < at && next >= at) {
				// Reached or passed the altitude, now go for speed
				nav.Speed.Assigned = nav.Speed.AfterAltitude
				nav.Speed.AfterAltitude = nil
				nav.Speed.AfterAltitudeAltitude = nil
			}
		}

		if nav.FlightState.Altitude >= 10000 && next < 10000 {
			// Descending through 10,000'
			if nav.Speed.Assigned != nil && *nav.Speed.Assigned > 250 {
				// Cancel any speed assignments >250kts when we are ready
				// to descend below 10,000'
				nav.Speed.Assigned = nil
				next = 10000
			}
			if nav.Speed.Restriction != nil && *nav.Speed.Restriction > 250 {
				// clear any speed restrictions >250kts we are carrying
				// from a previous waypoint.
				nav.Speed.Restriction = nil
				next = 10000
			}

			if slowingTo250 {
				// Keep it at 10k until we're done slowing
				next = 10000
			}
		}

		nav.FlightState.Altitude = next
	}

	if math.Abs(targetAltitude-nav.FlightState.Altitude) < 3 {
		setAltitude(targetAltitude)
		nav.FlightState.AltitudeRate = 0
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := nav.Perf.Rate.Climb, nav.Perf.Rate.Descent

	climb *= nav.atmosClimbFactor(wxs)
	// Reduce rates from highest possible to be more realistic.
	if !nav.Altitude.Expedite {
		// For high performing aircraft, reduce climb rate after 5,000'
		if climb >= 2500 && nav.FlightState.Altitude > 5000 {
			climb -= 500
		}
		climb = min(climb, targetRate)
		descent = min(descent, targetRate)
	}

	const rateFadeAltDifference = 500
	const rateMaxDeltaPercent = 0.075
	if nav.FlightState.Altitude < targetAltitude {
		if deltaKts > 0 {
			// accelerating in the climb, so reduce climb rate; the scale
			// factor is w.r.t. the maximum acceleration possible.
			max := nav.Perf.Rate.Accelerate / 2
			s := math.Clamp(max-deltaKts, .25, 1)
			climb *= s
		}

		if nav.FlightState.InitialDepartureClimb {
			nav.FlightState.AltitudeRate = climb
		} else {
			// Reduce climb rate as we approach target altitude
			altitudeRemaining := targetAltitude - nav.FlightState.Altitude
			if altitudeRemaining < rateFadeAltDifference {
				climb *= max(altitudeRemaining/rateFadeAltDifference, 0.25)
			}

			// Gradually transition to the target climb rate
			maxRateChange := nav.Perf.Rate.Climb * rateMaxDeltaPercent
			if nav.Altitude.Expedite {
				maxRateChange *= 2
			}
			rateDiff := climb - nav.FlightState.AltitudeRate
			if math.Abs(rateDiff) <= maxRateChange {
				nav.FlightState.AltitudeRate = climb
			} else if rateDiff > 0 {
				nav.FlightState.AltitudeRate += maxRateChange
			} else {
				nav.FlightState.AltitudeRate -= maxRateChange
			}
		}

		setAltitude(min(targetAltitude, nav.FlightState.Altitude+nav.FlightState.AltitudeRate/60))
	} else if nav.FlightState.Altitude > targetAltitude {
		if deltaKts < 0 {
			// Reduce rate due to concurrent deceleration
			max := nav.Perf.Rate.Decelerate / 2
			s := math.Clamp(max - -deltaKts, .25, 1)
			descent *= s
		}

		// Reduce descent rate as we approach target altitude
		// BUT: Don't do this on final approach! Aircraft need to maintain glidepath to runway
		altitudeRemaining := nav.FlightState.Altitude - targetAltitude
		if altitudeRemaining < rateFadeAltDifference && !nav.Approach.PassedFAF {
			descent *= max(altitudeRemaining/rateFadeAltDifference, 0.25)
		}

		// Gradually transition to the target descent rate
		maxRateChange := nav.Perf.Rate.Descent * rateMaxDeltaPercent
		if nav.Altitude.Expedite {
			maxRateChange *= 2
		}

		// After passing the FAF on approach, allow immediate descent rate changes
		// to ensure aircraft can meet the runway altitude restriction
		if nav.Approach.PassedFAF {
			maxRateChange = math.Abs(-descent - nav.FlightState.AltitudeRate)
		}

		rateDiff := -descent - nav.FlightState.AltitudeRate
		if math.Abs(rateDiff) <= maxRateChange {
			nav.FlightState.AltitudeRate = -descent
		} else if rateDiff > 0 {
			nav.FlightState.AltitudeRate += maxRateChange
		} else {
			nav.FlightState.AltitudeRate -= maxRateChange
		}

		setAltitude(max(targetAltitude, nav.FlightState.Altitude+nav.FlightState.AltitudeRate/60))
	}
}

// atmosClimbFactor returns a factor in [0,1] to model the reduction in
// rate of climb depending on atmospheric conditions.
func (nav *Nav) atmosClimbFactor(wxs wx.Sample) float32 {
	var tempCorrection, altCorrection float32
	switch nav.Perf.Engine.AircraftType {
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
	tempDeviation := wxs.Temperature() - isaTemp
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

	// If f is NaN, the factors might have issues, but we'll just return it

	// Ad-hoc clamp and softening to reduce effect at high temperatures.
	return max(0.5, math.Sqrt(tempFactor*altFactor*humidityFactor))
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

func (nav *Nav) TargetAltitude() (float32, float32) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetAltitude()
	}

	// Stay on the ground if we're still on the takeoff roll.
	rate := float32(MaximumRate)
	if nav.FlightState.InitialDepartureClimb && !nav.IsAirborne() {
		rate = 0 // still return the desired altitude, just no oomph to get there.
	}

	// Ugly to be digging into heading here, but anyway...
	if nav.Heading.RacetrackPT != nil {
		if alt, ok := nav.Heading.RacetrackPT.GetAltitude(nav); ok {
			return alt, rate
		}
	}

	// Controller-assigned altitude overrides everything else
	if nav.Altitude.Assigned != nil {
		return *nav.Altitude.Assigned, rate
	}

	if c, ok := nav.getWaypointAltitudeConstraint(); ok && !nav.flyingPT() {
		if c.ETA < 5 || nav.FlightState.Altitude < c.Altitude {
			// Always climb as soon as we can
			alt := c.Altitude
			if nav.Altitude.Cleared != nil {
				alt = min(alt, *nav.Altitude.Cleared)
			}
			return alt, rate
		} else {
			// Descending
			rate = (nav.FlightState.Altitude - c.Altitude) / c.ETA
			rate *= 60 // feet per minute

			descent := nav.Perf.Rate.Descent
			if nav.FlightState.Altitude < 10000 && !nav.Altitude.Expedite {
				// And reduce it based on airspeed as well
				descent *= min(nav.FlightState.IAS/250, 1)
				if descent > 2000 {
					// Reduce descent rate on approach
					descent = 2000
				}
			}

			if nav.Approach.PassedFAF {
				// After the FAF, try to go down linearly
				return c.Altitude, rate
			} else if rate > descent/2 {
				// Don't start the descent until (more or less) it's
				// necessary. (But then go a little faster than we think we
				// need to, to be safe.)
				return c.Altitude, rate * 1.5
			} else {
				// Stay where we are for now.
				return nav.FlightState.Altitude, 0
			}
		}
	}

	if nav.Altitude.Cleared != nil {
		return min(*nav.Altitude.Cleared, nav.FinalAltitude), rate
	}

	if ar := nav.Altitude.Restriction; ar != nil {
		return ar.TargetAltitude(nav.FlightState.Altitude), rate
	}

	// Baseline: stay where we are
	return nav.FlightState.Altitude, 0
}

func (nav *Nav) flyingPT() bool {
	return (nav.Heading.RacetrackPT != nil && nav.Heading.RacetrackPT.State != PTStateApproaching) ||
		(nav.Heading.Standard45PT != nil && nav.Heading.Standard45PT.State != PT45StateApproaching)
}

type WaypointCrossingConstraint struct {
	Altitude float32
	Fix      string  // where we're trying to readh Altitude
	ETA      float32 // seconds
}

// getWaypointAltitudeConstraint looks at the waypoint altitude
// restrictions in the aircraft's upcoming route and determines the
// altitude at which it will cross the next waypoint with a crossing
// restriction. It balances the general principle of preferring to be at
// higher altitudes (speed, efficiency) with the aircraft's performance and
// subsequent altitude restrictions--e.g., sometimes it needs to be lower
// than it would otherwise at one waypoint in order to make a restriction
// at a subsequent waypoint.
func (nav *Nav) getWaypointAltitudeConstraint() (WaypointCrossingConstraint, bool) {
	if nav.Heading.Assigned != nil {
		// ignore what's going on with the fixes
		return WaypointCrossingConstraint{}, false
	}

	if nav.InterceptedButNotCleared() {
		// Assuming this must be an altitude constraint on the approach,
		// we'll ignore it until the aircraft has been cleared for the
		// approach.
		return WaypointCrossingConstraint{}, false
	}

	if nav.Prespawn {
		// Simplified altitude constraint for prespawn: find the first
		// upcoming waypoint with a restriction that needs action and
		// target it directly. Skips the full backwards walk with
		// ClampRange/NMDistance2LLFast for intermediate waypoints.
		var d float32
		for i := range nav.Waypoints {
			if i == 0 {
				d = math.NMDistance2LLFast(nav.FlightState.Position, nav.Waypoints[0].Location,
					nav.FlightState.NmPerLongitude)
			} else {
				d += math.NMDistance2LLFast(nav.Waypoints[i-1].Location, nav.Waypoints[i].Location,
					nav.FlightState.NmPerLongitude)
			}
			ar := nav.Waypoints[i].AltitudeRestriction()
			if ar == nil || ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
				continue
			}
			// Same altitude selection as the full algorithm: prefer
			// upper bound if set, otherwise use FinalAltitude (cruise).
			alt := util.Select(ar.Range[1] != 0, ar.Range[1], nav.FinalAltitude)
			return WaypointCrossingConstraint{
				Altitude: alt,
				ETA:      d / nav.FlightState.GS * 3600,
				Fix:      nav.Waypoints[i].Fix,
			}, true
		}
		return WaypointCrossingConstraint{}, false
	}

	haveFixAssignments := len(nav.FixAssignments) > 0
	getRestriction := func(i int) *av.AltitudeRestriction {
		if haveFixAssignments {
			wp := &nav.Waypoints[i]
			// Return any controller-assigned constraint in preference to a
			// charted one.
			if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
				return nfa.Arrive.Altitude
			}
			if ar := nav.Waypoints[i].AltitudeRestriction(); ar != nil {
				// If the controller has given 'cross [wp] at [alt]' for a
				// future waypoint, ignore the charted altitude restriction.
				// Explicit loop avoids slices.ContainsFunc which copies the
				// large Waypoint struct by value for each element via the closure.
				for j := i + 1; j < len(nav.Waypoints); j++ {
					if fa, ok := nav.FixAssignments[nav.Waypoints[j].Fix]; ok && fa.Arrive.Altitude != nil {
						return nil
					}
				}
				return ar
			}
			return nil
		}
		// Fast path: no fix assignments, just return the charted restriction.
		return nav.Waypoints[i].AltitudeRestriction()
	}

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft.
	lastWp := -1
	for i := len(nav.Waypoints) - 1; i >= 0; i-- {
		// Skip restrictions that don't apply (e.g. "at or above" if we're
		// already above.) I think(?) we would actually bail out and return
		// nil if we find one that doesn't apply, under the principle that
		// we should also already be meeting any restrictions that are
		// before it, but this seems less risky.
		if r := getRestriction(i); r != nil &&
			r.TargetAltitude(nav.FlightState.Altitude) != nav.FlightState.Altitude {
			lastWp = i
			break
		}
	}
	if lastWp == -1 {
		// No applicable altitude restrictions found, so nothing to do here.
		return WaypointCrossingConstraint{}, false
	}

	// Figure out what climb/descent rate we will use for modeling the
	// flight path.
	var altRate float32
	descending := nav.FlightState.Altitude > getRestriction(lastWp).TargetAltitude(nav.FlightState.Altitude)
	if descending {
		altRate = nav.Perf.Rate.Descent
		// This unfortunately mirrors logic in the updateAltitude() method.
		// It would be nice to unify the nav modeling and the aircraft's
		// flight modeling to eliminate this...
		if nav.FlightState.Altitude < 10000 {
			altRate = min(altRate, 2000)
			altRate *= min(nav.FlightState.IAS/250, 1)
		}
		// Reduce the expected rate by a fudge factor to try to account for
		// slowing down at lower altitudes, speed reductions on approach,
		// and the fact that aircraft cut corners at turns rather than
		// going the longer way and overflying fixes.
		altRate *= 0.7
	} else {
		// This also mirrors logic in updateAltitude() and has its own
		// fudge factor, though a smaller one. Note that it doesn't include
		// a model for pausing the climb at 10k feet to accelerate, though
		// at that point we're likely leaving the TRACON airspace anyway...
		altRate = 0.9 * util.Select(nav.Perf.Rate.Climb > 2500, nav.Perf.Rate.Climb-500, nav.Perf.Rate.Climb)
	}

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := getRestriction(lastWp).Range

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	fix := nav.Waypoints[lastWp].Fix // first one with an alt restriction
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += math.NMDistance2LLFast(nav.Waypoints[i+1].Location, nav.Waypoints[i].Location,
			nav.FlightState.NmPerLongitude)

		// Does this one have a relevant altitude restriction?
		restr := getRestriction(i)
		if restr == nil {
			continue
		}

		// Ignore it if the aircraft is cleared for the approach and is below it.
		// TODO: I think this can be 'break' rather than continue...
		if nav.Approach.Cleared && nav.FlightState.Altitude < restr.Range[0] {
			continue
		}

		fix = nav.Waypoints[i].Fix

		// TODO: account for decreasing GS with altitude?
		// TODO: incorporate a simple wind model in GS?
		eta := sumDist / nav.FlightState.GS * 3600 // seconds

		// Maximum change in altitude possible before reaching this
		// waypoint.
		dalt := altRate * eta / 60

		// possibleRange is altitude range the aircraft could have at this
		// waypoint, given its performance characteristics and assuming it
		// will meet the constraint at the subsequent waypoint with an
		// altitude restriction.
		//
		// Note that dalt only applies to one limit, since the aircraft can
		// always maintain its current altitude between waypoints; which
		// limit depends on whether it is climbing or descending (but then
		// which one and the addition/subtraction are confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := altRange
		if !descending {
			possibleRange[0] -= dalt
		} else {
			possibleRange[1] += dalt
		}

		// Limit the possible range according to the restriction at the
		// current waypoint.
		altRange, _ = restr.ClampRange(possibleRange)

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Add the distance to the first waypoint to get the total distance
	// (and then the ETA) between the aircraft and the first waypoint with
	// an altitude restriction.
	d := sumDist + math.NMDistance2LLFast(nav.FlightState.Position, nav.Waypoints[0].Location,
		nav.FlightState.NmPerLongitude)
	eta := d / nav.FlightState.GS * 3600 // seconds

	// Prefer to be higher rather than low; deal with "at or above" here as well.
	alt := util.Select(altRange[1] != 0, altRange[1], nav.FinalAltitude)

	// But leave arrivals at their current altitude if it's acceptable;
	// don't climb just because we can.
	if descending {
		ar := av.AltitudeRestriction{Range: altRange}
		if ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
			alt = nav.FlightState.Altitude
		}
	} else {
		// For climbing aircraft, if the constraint is "at" (not "at or above"),
		// target that specific altitude instead of climbing higher
		if altRange[0] != 0 && altRange[0] == altRange[1] {
			alt = altRange[0]
		}
	}

	return WaypointCrossingConstraint{
		Altitude: alt,
		ETA:      eta,
		Fix:      fix,
	}, true
}
