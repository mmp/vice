// nav/alt.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

const MaximumRate = 100000
const rateMaxDeltaPercent = 0.075

func (nav *Nav) activatePendingAltitude(simTime Time) {
	if nav.Altitude.ActivateAt.IsZero() || simTime.Before(nav.Altitude.ActivateAt) {
		return
	}
	nav.Altitude.ActiveAssigned = nav.Altitude.Assigned
	nav.Altitude.ActivateAt = Time{}
}

func (nav *Nav) activeAssignedAltitude() *float32 {
	if !nav.Altitude.ActivateAt.IsZero() {
		return nav.Altitude.ActiveAssigned
	}
	if nav.Altitude.ActiveAssigned != nil {
		return nav.Altitude.ActiveAssigned
	}
	return nav.Altitude.Assigned
}

func (nav *Nav) updateAltitude(callsign string, targetAltitude, targetRate float32, geometricDescent bool, deltaKts float32, slowingTo250 bool, wxs wx.Sample, simTime Time) {
	nav.FlightState.PrevAltitude = nav.FlightState.Altitude

	NavLog(callsign, simTime, NavLogAltitude, "target=%.0f current=%.0f rate=%.0f targetRate=%.0f rate_qual=%d slowingTo250=%v",
		targetAltitude, nav.FlightState.Altitude, nav.FlightState.AltitudeRate, targetRate, nav.Altitude.Rate, slowingTo250)

	if targetAltitude == nav.FlightState.Altitude {
		if nav.IsAirborne() && nav.FlightState.InitialDepartureClimb {
			nav.FlightState.InitialDepartureClimb = false
		}
		nav.FlightState.AltitudeRate = 0
		if nav.Altitude.ActivateAt.IsZero() {
			nav.Altitude.Rate = RateNormal
			nav.Altitude.RateThrough = nil
		}
		return
	}

	// Wrap altitude setting in a lambda so we can detect when we pass
	// through an altitude for "at alt, reduce speed" sort of assignments.
	setAltitude := func(next float32) {
		if nav.Speed.AfterAltitude != nil &&
			(nav.Speed.Assigned == nil || nav.Speed.Assigned.Satisfied(nav.FlightState.IAS)) {
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
			if sr := nav.Speed.Assigned; sr != nil {
				// Cancel if the minimum required speed exceeds 250kts.
				// For "at or below 280" the floor is 0, so the pilot
				// can comply at 250 — don't cancel.
				// For "at or above 260", they can't comply at 250.
				lo := sr.Range[0]
				if lo == 0 {
					lo = sr.Range[1]
				}
				if lo > 250 {
					nav.Speed.Assigned = nil
					next = 10000
				}
			}
			if sr := nav.Speed.Restriction; sr != nil {
				lo := sr.Range[0]
				if lo == 0 {
					lo = sr.Range[1]
				}
				if lo > 250 {
					nav.Speed.Restriction = nil
					next = 10000
				}
			}

			if slowingTo250 {
				// Keep it at 10k until we're done slowing
				next = 10000
			}
		}

		if nav.Altitude.RateThrough != nil {
			cur := nav.FlightState.Altitude
			at := *nav.Altitude.RateThrough
			if (cur > at && next <= at) || (cur < at && next >= at) {
				nav.Altitude.Rate = RateNormal
				nav.Altitude.RateThrough = nil
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

	atmosFactor := nav.atmosClimbFactor(wxs)
	climb *= atmosFactor
	// Reduce rates from highest possible to be more realistic.
	switch nav.Altitude.Rate {
	case RateNormal:
		if climb >= 2500 && nav.FlightState.Altitude > 5000 {
			climb -= 500
		}
		climb = min(climb, targetRate)
		descent = min(descent, targetRate)
	case RateGood:
		// Skip the 500fpm reduction, but still cap at targetRate
		climb = min(climb, targetRate)
		descent = min(descent, targetRate)
	case RateExpedite:
		// No reduction
	}

	NavLog(callsign, simTime, NavLogAltitude, "atmosFactor=%.3f climb=%.0f descent=%.0f pressure=%.1f temp=%.1f",
		atmosFactor, climb, descent, wxs.Pressure(), wxs.Temperature().Celsius())

	const rateFadeAltDifference = 500
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
			switch nav.Altitude.Rate {
			case RateGood:
				maxRateChange *= 1.5
			case RateExpedite:
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
		if deltaKts < 0 && !geometricDescent {
			// Reduce rate due to concurrent deceleration, but only for
			// controller-assigned altitudes. Geometric descents need the
			// full computed rate to meet the restriction at the fix.
			max := nav.Perf.Rate.Decelerate / 2
			s := math.Clamp(max - -deltaKts, .25, 1)
			descent *= s
		}

		// Reduce descent rate as we approach target altitude
		// BUT: Don't do this on geometric descents on arrivals or on final approach.
		altitudeRemaining := nav.FlightState.Altitude - targetAltitude
		if altitudeRemaining < rateFadeAltDifference && !geometricDescent {
			descent *= max(altitudeRemaining/rateFadeAltDifference, 0.25)
		}

		// Gradually transition to the target descent rate
		maxRateChange := nav.Perf.Rate.Descent * rateMaxDeltaPercent
		switch nav.Altitude.Rate {
		case RateGood:
			maxRateChange *= 1.5
		case RateExpedite:
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

// TargetAltitude returns the target altitude, the rate to use (ft/min),
// and whether the descent is geometric (following a computed glidepath to a fix).
func (nav *Nav) TargetAltitude() (float32, float32, bool) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetAltitude()
	}

	// Stay on the ground if we're still on the takeoff roll.
	rate := float32(MaximumRate)
	if nav.FlightState.InitialDepartureClimb && !nav.IsAirborne() {
		rate = 0 // still return the desired altitude, just no oomph to get there.
	}

	// Controller-assigned altitude overrides everything else
	if alt := nav.activeAssignedAltitude(); alt != nil {
		return *alt, rate, false
	}

	if target, ok := nav.findAltitudeTarget(); ok {
		if nav.FlightState.Altitude < target.altitude {
			// Climbing: start immediately
			alt := target.altitude
			if nav.Altitude.Cleared != nil {
				alt = min(alt, *nav.Altitude.Cleared)
			}
			return alt, rate, false
		} else {
			// Descending: compute geometric descent rate
			dist, ok := nav.routeDistanceToFix(target.fix)
			eta := dist / nav.FlightState.GS * 3600
			if ok && eta > 0 {
				geometricRate := (nav.FlightState.Altitude - target.altitude) / eta * 60

				if nav.Approach.PassedFAF {
					return target.altitude, geometricRate, true // exact glideslope
				}

				descent := nav.Perf.Rate.Descent

				// Dynamic safety factor: accounts for ramp-up deficit where
				// updateAltitude takes ~13 ticks to reach full rate, causing
				// ~descent/9 ft of lost altitude change. Scale the factor
				// inversely with descent size so small descents get more margin.
				altDiff := nav.FlightState.Altitude - target.altitude
				rampUpSec := float32(1) / rateMaxDeltaPercent
				rampUpDeficit := descent * rampUpSec / (2 * 60)
				safetyFactor := min(float32(1)+rampUpDeficit/max(altDiff, 100), 1.5)

				// The AltitudeRate check fixes a case where we enter an oscillatory state of "start
				// descent" / "no not yet" and end up descending too late to meet the restriction.
				if geometricRate > descent/2 || nav.FlightState.AltitudeRate < -50 {
					// Start continuous descent with safety margin
					return target.altitude, min(geometricRate*safetyFactor, descent), true
				}
			}
			// Not time yet
			if ar := nav.Altitude.Restriction; ar != nil {
				return ar.TargetAltitude(nav.FlightState.Altitude), MaximumRate, false
			}
			if nav.Altitude.Cleared != nil && *nav.Altitude.Cleared < nav.FlightState.Altitude {
				return *nav.Altitude.Cleared, MaximumRate, false
			}
			return nav.FlightState.Altitude, 0, false
		}
	}

	if nav.Altitude.Cleared != nil {
		return min(*nav.Altitude.Cleared, nav.FinalAltitude), rate, false
	}

	if ar := nav.Altitude.Restriction; ar != nil {
		return ar.TargetAltitude(nav.FlightState.Altitude), rate, false
	}

	// Baseline: stay where we are
	return nav.FlightState.Altitude, 0, false
}

// routeDistanceToFix computes the distance in nm along the waypoint route
// from the aircraft's current position to the named fix. Returns ok=false
// if the fix is not found in the waypoints.
func (nav *Nav) routeDistanceToFix(fix string) (float32, bool) {
	if len(nav.Waypoints) == 0 {
		return 0, false
	}
	d := math.NMDistance2LLFast(nav.FlightState.Position, nav.Waypoints[0].Location,
		nav.FlightState.NmPerLongitude)
	for i := range nav.Waypoints {
		if nav.Waypoints[i].Fix == fix {
			return d, true
		}
		if i+1 < len(nav.Waypoints) {
			d += math.NMDistance2LLFast(nav.Waypoints[i].Location, nav.Waypoints[i+1].Location,
				nav.FlightState.NmPerLongitude)
		}
	}
	return 0, false
}

// altitudeTarget holds the result of scanning waypoints for an altitude target.
type altitudeTarget struct {
	altitude float32
	fix      string
}

func (nav *Nav) controllerAltitudeRestriction(wp *av.Waypoint) *av.AltitudeRestriction {
	if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
		return nfa.Arrive.Altitude
	}
	if wp.SyntheticCrossing() {
		return wp.AltitudeRestriction()
	}
	return nil
}

func (nav *Nav) hasControllerAltitudeRestriction(wps []av.Waypoint) bool {
	for i := range wps {
		if nav.controllerAltitudeRestriction(&wps[i]) != nil {
			return true
		}
	}
	return false
}

// findAltitudeTarget scans waypoints to determine the target altitude and
// fix, using the reverse-walk logic to satisfy all downstream altitude
// constraints. Returns the target and true if found.
func (nav *Nav) findAltitudeTarget() (altitudeTarget, bool) {
	if nav.Heading.Assigned != nil {
		// ignore what's going on with the fixes
		return altitudeTarget{}, false
	}

	// Don't consider restrictions after an upcoming hold
	wps := nav.Waypoints
	for i := range wps {
		if hold := nav.Heading.Hold; hold != nil && hold.Hold.Fix == wps[i].Fix {
			wps = wps[:i+1]
			break
		}
		if nfa, ok := nav.FixAssignments[wps[i].Fix]; ok && nfa.Hold != nil {
			wps = wps[:i+1]
			break
		}
	}

	if nav.Prespawn {
		// Simplified forward scan for prespawn: find the first waypoint
		// with an actionable restriction. Skips FixAssignment lookups
		// and the reverse walk with ClampRange for intermediate waypoints.
		for i := range wps {
			ar := wps[i].AltitudeRestriction()
			if ar == nil || ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
				continue
			}
			alt := util.Select(ar.Range[1] != av.MaxAltitude, ar.Range[1], nav.FinalAltitude)
			return altitudeTarget{altitude: alt, fix: wps[i].Fix}, true
		}
		return altitudeTarget{}, false
	}

	interceptedButNotCleared := nav.InterceptedButNotCleared()
	if interceptedButNotCleared && !nav.hasControllerAltitudeRestriction(wps) {
		// Track the uncleared approach laterally, but don't descend to charted
		// approach restrictions until the aircraft is actually cleared.
		return altitudeTarget{}, false
	}

	haveFixAssignments := len(nav.FixAssignments) > 0
	getChartedRestriction := func(i int) *av.AltitudeRestriction {
		wp := &wps[i]

		if ar := nav.controllerAltitudeRestriction(wp); ar != nil {
			return ar
		}
		if interceptedButNotCleared && wp.OnApproach() {
			return nil
		}

		if haveFixAssignments {
			if ar := wp.AltitudeRestriction(); ar != nil {
				// If the controller has given 'cross [wp] at [alt]' for a
				// future waypoint, ignore the charted altitude restriction.
				// Explicit loop avoids slices.ContainsFunc which copies the
				// large Waypoint struct by value for each element via the closure.
				for j := i + 1; j < len(wps); j++ {
					if fa, ok := nav.FixAssignments[wps[j].Fix]; ok && fa.Arrive.Altitude != nil {
						return nil
					}
				}
				return ar
			}
			return nil
		}
		// Fast path: no fix assignments, just return the charted restriction.
		return wp.AltitudeRestriction()
	}

	// On a cleared approach, "at or above X" means the aircraft should
	// target X (the floor) to maintain a normal descent profile, not
	// stay high because the restriction is technically satisfied.
	getRestriction := func(i int) *av.AltitudeRestriction {
		ar := getChartedRestriction(i)
		if ar != nil && nav.Approach.Cleared && wps[i].OnApproach() &&
			ar.Range[1] == av.MaxAltitude && ar.Range[0] > 0 {
			adjusted := av.MakeAtAltitudeRestriction(ar.Range[0])
			return &adjusted
		}
		return ar
	}

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft.
	lastWp := -1
	for i := len(wps) - 1; i >= 0; i-- {
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
		return altitudeTarget{}, false
	}

	descending := nav.FlightState.Altitude > getRestriction(lastWp).TargetAltitude(nav.FlightState.Altitude)

	// Use a large rate estimate for the reverse walk to compute feasible
	// altitude ranges.
	altRate := util.Select(descending, nav.Perf.Rate.Descent, nav.Perf.Rate.Climb)

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := getRestriction(lastWp).Range

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	fix := wps[lastWp].Fix // first one with an alt restriction
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += math.NMDistance2LLFast(wps[i+1].Location, wps[i].Location,
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

		fix = wps[i].Fix

		// TODO: account for decreasing GS with altitude?
		// TODO: incorporate a simple wind model in GS?
		eta := sumDist / nav.FlightState.GS * 3600 // seconds

		// Maximum change in altitude possible before reaching this
		// waypoint. Subtract the ramp-up deficit: the first ~13s to reach
		// full rate achieve roughly half the altitude change vs full rate.
		dalt := altRate * eta / 60
		rampUpSec := float32(1) / rateMaxDeltaPercent
		rampUpDeficit := altRate * min(rampUpSec, eta) / (2 * 60)
		dalt = max(0, dalt-rampUpDeficit)

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
		var feasible bool
		altRange, feasible = restr.ClampRange(possibleRange)
		if !feasible {
			// Can't satisfy all constraints; target the earliest
			// unsatisfiable one (altRange and fix were already updated).
			break
		}

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Prefer to be higher rather than low; deal with "at or above" here as well.
	alt := util.Select(altRange[1] != av.MaxAltitude, altRange[1], nav.FinalAltitude)

	// But leave arrivals at their current altitude if it's acceptable;
	// don't climb just because we can.
	if descending {
		ar := av.AltitudeRestriction{NavigationRestriction: av.NavigationRestriction{Range: altRange}}
		if ar.Target(nav.FlightState.Altitude) == nav.FlightState.Altitude {
			alt = nav.FlightState.Altitude
		}
	} else {
		// For climbing aircraft, if the constraint is "at" (not "at or above"),
		// target that specific altitude instead of climbing higher
		if altRange[0] != 0 && altRange[0] == altRange[1] {
			alt = altRange[0]
		}
	}

	return altitudeTarget{altitude: alt, fix: fix}, true
}

// clearAltitudeForApproach resets altitude state when an approach-cleared
// aircraft transitions to following approach restrictions. If the controller
// assigned a descent altitude that the aircraft hasn't yet reached, it is
// preserved as a Cleared altitude so the descent continues.
func (nav *Nav) clearAltitudeForApproach() {
	var cleared *float32
	if nav.Altitude.Assigned != nil && *nav.Altitude.Assigned < nav.FlightState.Altitude {
		alt := *nav.Altitude.Assigned
		cleared = &alt
	} else if nav.Altitude.Cleared != nil && *nav.Altitude.Cleared < nav.FlightState.Altitude {
		cleared = nav.Altitude.Cleared
	}
	nav.Altitude = NavAltitude{Cleared: cleared}
}
