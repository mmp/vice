// nav/speed.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) updateAirspeed(callsign string, alt float32, geometricDescent bool, fp *av.FlightPlan, wxs wx.Sample, arrivalMETAR *wx.METAR, simTime Time, bravo *av.AirspaceGrid) (float32, bool) {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.
	targetSpeed, targetRate := nav.TargetSpeed(alt, fp, wxs, arrivalMETAR, bravo)

	// Stay within the aircraft's capabilities
	targetSpeed = math.Clamp(targetSpeed, nav.Perf.Speed.Min, MaxIAS)

	NavLog(callsign, simTime, NavLogSpeed, "target=%.0f current=%.0f rate=%.1f", targetSpeed, nav.FlightState.IAS, targetRate)

	setSpeed := func(next float32) (float32, bool) {
		if nav.Altitude.AfterSpeed != nil &&
			(nav.Altitude.Assigned == nil || *nav.Altitude.Assigned == nav.FlightState.Altitude) {
			cur := nav.FlightState.IAS
			at := *nav.Altitude.AfterSpeedSpeed
			// Check if we've reached or are passing a speed assignment
			// after which an altitude assignment should be followed.
			if (cur > at && next <= at) || (cur < at && next >= at) {
				nav.enqueueAltitudeAfterSpeed(simTime)
			}
		}
		delta := next - nav.FlightState.IAS
		nav.FlightState.IAS = next

		slowingTo250 := targetSpeed == 250 && nav.FlightState.Altitude >= 10000
		return delta, slowingTo250
	}

	if !nav.FlightState.InitialDepartureClimb && alt > nav.FlightState.Altitude &&
		nav.Perf.Engine.AircraftType == "P" {
		// Climbing prop; bleed off speed.
		cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
		limit := (nav.v2() + cruiseIAS) * 0.5
		if nav.FlightState.IAS > limit {
			spd := max(nav.FlightState.IAS*.99, limit)
			return setSpeed(spd)
		}
	}

	if nav.Altitude.Rate == RateExpedite {
		// Don't accelerate or decelerate if we're expediting
		return 0, false
	}

	if nav.FlightState.IAS < targetSpeed {
		accel := nav.Perf.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, targetRate/60)
		if !nav.IsAirborne() {
			// Rough approximation of it being easier to accelerate on the
			// ground and when going slow than when going fast (and
			// airborne).
			if nav.FlightState.IAS < 40 {
				accel *= 3
			} else {
				accel *= 2
			}
		} else if nav.FlightState.Altitude < alt && !geometricDescent {
			// Reduce acceleration since also climbing, but only for
			// controller-assigned altitudes. Geometric climbs/descents
			// need full speed authority to meet restrictions.
			if nav.FlightState.InitialDepartureClimb {
				// But less so in the initial climb, assuming full power.
				accel *= 0.8
			} else {
				accel *= 0.6
			}
		}
		if nav.FlightState.Altitude >= 25000 {
			// Thinner air at high altitude makes acceleration harder
			accel *= 0.6
		}
		return setSpeed(min(targetSpeed, nav.FlightState.IAS+accel))
	} else if nav.FlightState.IAS > targetSpeed {
		decel := nav.Perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		if nav.FlightState.Altitude > alt && !geometricDescent {
			// Reduce deceleration since also descending, but only for
			// controller-assigned altitudes.
			decel *= 0.6
		}
		if nav.FlightState.Altitude >= 25000 {
			// Thinner air at high altitude makes deceleration harder
			decel *= 0.6
		}
		return setSpeed(max(targetSpeed, nav.FlightState.IAS-decel))
	} else {
		return 0, false
	}
}
func (nav *Nav) TargetSpeed(targetAltitude float32, fp *av.FlightPlan, wxs wx.Sample, arrivalMETAR *wx.METAR, bravo *av.AirspaceGrid) (float32, float32) {
	if nav.Airwork != nil {
		if spd, rate, ok := nav.Airwork.TargetSpeed(); ok {
			return spd, rate
		}
	}

	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute

	fd, err := nav.DistanceToEndOfApproach()
	if err == nil && fd < 5 {
		// Cancel speed restrictions inside 5 mile final
		nav.Speed = NavSpeed{}
	}

	// Controller assignments: these override anything else.
	if nav.Speed.MaintainSlowestPractical {
		return nav.Perf.Speed.Landing + 5, MaximumRate
	}
	if nav.Speed.MaintainMaximumForward {
		if nav.Approach.Cleared {
			// (We expect this to usually be the case.) Ad-hoc speed based
			// on V2, also assuming some flaps are out, so we don't just
			// want to return 250 knots here...
			cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
			return min(nav.v2()*1.6, min(250, cruiseIAS)), MaximumRate
		}
		return nav.targetAltitudeIAS()
	}
	if sr := nav.Speed.Assigned; sr != nil {
		if sr.IsMach {
			tas := av.MachToTAS(sr.Range[0], wxs.Temperature())
			return av.TASToIAS(tas, nav.FlightState.Altitude), MaximumRate
		}
		if _, exact := sr.ExactValue(); !exact {
			return math.Clamp(nav.FlightState.IAS, sr.Range[0], sr.Range[1]), MaximumRate
		}
		naturalIAS, _ := nav.targetAltitudeIAS()
		return nav.restrictedSpeed(sr, naturalIAS), MaximumRate
	}

	if hold := nav.Heading.Hold; hold != nil && nav.ETA(hold.FixLocation) < 180 /* slow 3 minutes out */ {
		return hold.Hold.Speed(nav.FlightState.Altitude), MaximumRate
	}

	// Manage the speed profile in the initial climb
	if nav.FlightState.InitialDepartureClimb {
		agl := nav.FlightState.Altitude - nav.FlightState.DepartureAirportElevation
		isJet := nav.Perf.Engine.AircraftType == "J"

		if (isJet && agl >= 5000) || (!isJet && agl >= 1500) {
			nav.FlightState.InitialDepartureClimb = false
		}

		var targetSpeed float32
		if nav.Perf.Engine.AircraftType == "J" { // jet
			if agl < 1500 {
				targetSpeed = 180
			} else {
				targetSpeed = 210
			}
		} else { // prop/turboprop
			if agl < 500 {
				targetSpeed = 1.1 * nav.v2()
			} else if agl < 1000 {
				targetSpeed = 1.2 * nav.v2()
			} else {
				targetSpeed = 1.3 * nav.v2()
			}
		}

		// Make sure we're not trying to go faster than we're able to
		cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)
		targetSpeed = min(targetSpeed, cruiseIAS)

		// And don't accelerate past any upcoming speed restrictions
		if nav.Speed.Restriction != nil {
			targetSpeed = nav.restrictedSpeed(nav.Speed.Restriction, targetSpeed)
		}
		if _, sr, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
			targetSpeed = nav.restrictedSpeed(sr, targetSpeed)
		}

		// However, don't let anything prevent us from taking off!
		targetSpeed = max(targetSpeed, nav.v2())

		return targetSpeed, 0.8 * maxAccel
	}

	pendingDecel := false
	if onSID, sr, fix, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		naturalIAS, _ := nav.targetAltitudeIAS()
		speed := nav.restrictedSpeed(sr, naturalIAS)
		if speed > nav.FlightState.IAS {
			// Accelerate immediately
			return speed, MaximumRate
		} else if onSID {
			// SID: comply immediately
			return speed, MaximumRate
		} else {
			// Geometric deceleration check: compute how fast we'd
			// need to decelerate to hit the target speed at the fix,
			// and start when that exceeds half our capability.
			dist, ok := nav.routeDistanceToFix(fix)
			eta := dist / nav.FlightState.GS * 3600 // seconds to fix
			if ok && eta > 0 {
				neededRate := (nav.FlightState.IAS - speed) / eta * 60 // kts per minute
				decelRate := nav.Perf.Rate.Decelerate * 30             // kts per minute (Decelerate is per 2s)
				if neededRate > decelRate/2 {
					return speed, MaximumRate
				}
			}
			// Not time to decelerate yet; fall through to let the
			// 250kt/10k rule and approach speed still apply, but
			// prevent the default altitude-based speed from
			// accelerating past the upcoming restriction.
			pendingDecel = true
		}
	}

	// Something from a previous waypoint; ignore it if we're cleared for the approach.
	if nav.Speed.Restriction != nil && !nav.Approach.Cleared {
		naturalIAS, _ := nav.targetAltitudeIAS()
		return nav.restrictedSpeed(nav.Speed.Restriction, naturalIAS), MaximumRate
	}

	// Regulatory requirement: slow to 250 kts when descending through 10,000 feet
	// This must be checked BEFORE approach-cleared logic, as it's a hard speed limit
	if nav.FlightState.Altitude >= 10000 && targetAltitude < 10000 && nav.FlightState.IAS > 250 {
		// Consider slowing to 250; estimate how long until we'll reach 10k
		dalt := nav.FlightState.Altitude - 10000
		salt := dalt / (nav.Perf.Rate.Descent / 60) // seconds until we reach 10k

		dspeed := nav.FlightState.IAS - 250
		sspeed := dspeed / (nav.Perf.Rate.Decelerate / 2) // seconds to decelerate to 250

		if salt <= sspeed {
			// Time to slow down
			return 250, MaximumRate
		} else {
			// Otherwise reduce in general but in any case don't speed up
			// again.
			ias, rate := nav.targetAltitudeIAS()
			return min(ias, nav.FlightState.IAS), rate
		}
	}

	// If fd != 0, we're flying an approach; absent controller speed
	// restrictions (and inside 5 DME), maintain approach speed on final
	// and only start transitioning to the landing reference speed in the
	// last half mile before touchdown.
	if nav.Speed.Assigned == nil && fd != 0 && fd < 10 {
		hdg := nav.Approach.Assigned.RunwayHeading(nav.FlightState.NmPerLongitude)
		var approachSpeed float32
		if arrivalMETAR != nil && arrivalMETAR.WindDir != nil { // METAR and non-VRB winds
			windSpeed := float32(arrivalMETAR.WindSpeed)
			windGust := windSpeed // default: no gust above steady wind
			if arrivalMETAR.WindGust != nil && *arrivalMETAR.WindGust > arrivalMETAR.WindSpeed {
				windGust = float32(*arrivalMETAR.WindGust)
			}
			approachSpeed = nav.Perf.ApproachSpeed(float32(*arrivalMETAR.WindDir), windSpeed, windGust, float32(hdg))
		} else {
			approachSpeed = nav.Perf.ApproachSpeed(float32(wxs.WindDirection()), wxs.WindSpeed(), 0, float32(hdg))
		}

		if fd < 0.5 {
			// Short final: slow down to landing speed.
			approachSpeed = math.Lerp(fd/0.5, nav.Perf.Speed.Landing, approachSpeed)
		}

		// Don't speed up after being been cleared to land.
		ias := min(approachSpeed, nav.FlightState.IAS)

		return ias, MaximumRate
	}

	if nav.Approach.Cleared || nav.Approach.MissedApproachIntercept || nav.Approach.ApproachClearanceCancelled {
		// Don't speed up if we're cleared (or recently were, before an overshoot recovery or
		// controller cancellation)
		return nav.FlightState.IAS, MaximumRate
	}

	// Nothing assigned by the controller or the route, so set a target
	// based on the aircraft's altitude.
	ias, rate := nav.targetAltitudeIAS()
	if fp != nil && fp.Rules == av.FlightRulesVFR &&
		av.UnderBravoShelf(bravo, nav.FlightState.Position, int(nav.FlightState.Altitude)) {
		ias = min(ias, 200)
	}
	if pendingDecel {
		ias = min(ias, nav.FlightState.IAS)
	}

	return ias, rate
}

// If the aircraft has reached an altitude where they transition to mach
func (nav *Nav) machTransition() bool {

	switch nav.Perf.Engine.AircraftType {
	case "J":
		return nav.FlightState.Altitude >= 27000
	// case "P":
	// 	return nav.FlightState.Altitude >= 5000
	// case "T":
	// 	return nav.FlightState.Altitude >= 3000
	default:
		return false // TODO: check if turboprops ever transition to mach
	}
}

// ETA returns the estimated time in seconds until the aircraft will arrive at `p`, assuming it is flying direct.
func (nav *Nav) ETA(p math.Point2LL) float32 {
	dist := math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude)
	return dist / nav.FlightState.GS * 3600 // seconds
}

// restrictedSpeed returns the speed a pilot would choose given a speed
// restriction, accounting for aircraft type. Heavy jets fly close to
// limits for fuel efficiency; lighter aircraft have more margin.
func (nav *Nav) restrictedSpeed(sr *av.SpeedRestriction, natural float32) float32 {
	lo, hi := sr.Range[0], sr.Range[1]

	// "At" restriction: exact compliance required.
	if lo == hi {
		return lo
	}

	// Margin from the boundary depends on aircraft weight class.
	// Heavies need speed for efficiency and fly close to limits.
	margin := float32(10)
	if nav.Perf.WeightClass == "H" || nav.Perf.WeightClass == "J" {
		margin = 5
	}

	// Shrink the allowed range by the margin on each real bound.
	innerLo := lo
	innerHi := hi
	if lo > 0 { // lo==0 means no floor
		innerLo = lo + margin
	}
	if hi < av.MaxRestrictionSpeed { // sentinel means no ceiling
		innerHi = hi - margin
	}

	// If margins overlap (very narrow range), just use the midpoint.
	if innerLo > innerHi {
		return (lo + hi) / 2
	}

	return math.Clamp(natural, innerLo, innerHi)
}

// Compute target airspeed for higher altitudes speed by lerping from 250
// to cruise speed based on altitude.
func (nav *Nav) targetAltitudeIAS() (float32, float32) {
	maxAccel := nav.Perf.Rate.Accelerate * 30 // per minute
	cruiseIAS := av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude)

	if nav.FlightState.Altitude <= 10000 {
		// 250kts under 10k.  We can assume a high acceleration rate for
		// departures when this kicks in at 1500' AGL given that VNav will
		// slow the rate of climb at that point until we reach the target
		// speed.
		return min(cruiseIAS, 250), 0.9 * maxAccel
	}

	x := math.Clamp((nav.FlightState.Altitude-10000)/(nav.Perf.Ceiling-10000), 0, 1)
	return math.Lerp(x, min(cruiseIAS, 280), cruiseIAS), 0.8 * maxAccel
}

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (onSID bool, sr *av.SpeedRestriction, fix string, ok bool) {
	if nav.Prespawn {
		return false, nil, "", false
	}

	// Explicit loop avoids slices.ContainsFunc which copies the large
	// Waypoint struct by value for each element via the closure.
	haveWaypointSpeedRestriction := false
	for i := range nav.Waypoints {
		if nav.Waypoints[i].SpeedRestriction() != nil {
			haveWaypointSpeedRestriction = true
			break
		}
	}

	// Skip all this work in the (common) case that it's unnecessary.
	if len(nav.FixAssignments) > 0 || haveWaypointSpeedRestriction {
		for i := range nav.Waypoints {
			wp := &nav.Waypoints[i]

			if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
				return wp.OnSID(), nfa.Arrive.Speed, wp.Fix, true
			}

			if wsr := wp.SpeedRestriction(); wsr != nil {
				return wp.OnSID(), wsr, wp.Fix, true
			}
		}
	}
	return false, nil, "", false
}

// AssignedSpeedFloor returns the lowest speed (in knots) the aircraft may fly
// given the current controller speed assignment, along with whether such an
// assignment is in force. It is used to decide whether an arrival should ask
// to slow down (issue #884); Mach assignments are ignored since they only
// apply far from the airport.
func (nav *Nav) AssignedSpeedFloor() (float32, bool) {
	if nav.Speed.MaintainMaximumForward {
		// "Maintain maximum forward speed" exceeds any distance-based gate.
		return av.MaxRestrictionSpeed, true
	}
	if sr := nav.Speed.Assigned; sr != nil && !sr.IsMach {
		return sr.Range[0], true
	}
	return 0, false
}

// SlowDownDistanceNM estimates the distance (in nm) the pilot would use when
// judging whether they are "getting close" to the runway for the purpose of
// requesting a speed reduction (issue #884), along with whether that estimate
// is meaningful.
//
// When flying a lateral route (not being vectored) it returns the remaining
// track miles along the route, which correctly accounts for downwind/base
// geometry. When being vectored it falls back to the straight-line distance to
// the assigned approach's runway threshold, but only when the aircraft is
// actually flying toward it (within 90 degrees); otherwise it returns
// ok=false, so no premature request is made while on a downwind or base leg
// where the aircraft can be close to the threshold but heading away from it.
func (nav *Nav) SlowDownDistanceNM() (float32, bool) {
	fs := &nav.FlightState

	if nav.Heading.Assigned == nil && len(nav.Waypoints) > 0 {
		// Sum the remaining track miles. We do not verify the final waypoint is
		// the runway threshold: a not-yet-cleared arrival on a full STAR yields a
		// large distance, which arrivalSpeedGate rejects (>20 nm) anyway.
		d := math.NMDistance2LLFast(fs.Position, nav.Waypoints[0].Location, fs.NmPerLongitude)
		for i := 0; i+1 < len(nav.Waypoints); i++ {
			d += math.NMDistance2LLFast(nav.Waypoints[i].Location, nav.Waypoints[i+1].Location,
				fs.NmPerLongitude)
		}
		return d, true
	}

	// Being vectored: we can only estimate distance if we know which runway
	// we're being taken to.
	if nav.Approach.Assigned == nil {
		return 0, false
	}
	threshold := nav.Approach.Assigned.Threshold
	bearing := math.Heading2LL(fs.Position, threshold, fs.NmPerLongitude)
	heading := math.MagneticToTrue(fs.Heading, fs.MagneticVariation)
	if math.HeadingDifference(heading, bearing) > 90 {
		return 0, false
	}
	return math.NMDistance2LLFast(fs.Position, threshold, fs.NmPerLongitude), true
}

// distanceToEndOfApproach returns the remaining distance to the last
// waypoint (usually runway threshold) of the currently assigned approach.
func (nav *Nav) DistanceToEndOfApproach() (float32, error) {
	if nav.Approach.Assigned == nil || !nav.Approach.Cleared {
		return 0, ErrNotClearedForApproach
	}

	if nav.Heading.Assigned != nil {
		// We're not currently on the route, so it's a little unclear. Rather than
		// guessing, we'll just error out and let callers decide how to handle this.
		return 0, ErrNotFlyingRoute
	}

	// Calculate flying distance to the airport
	if wp := nav.Waypoints; len(wp) == 0 {
		// This shouldn't ever happen; we should always have the
		// destination airport, but just in case...
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, nav.FlightState.ArrivalAirportLocation)
		return remainingDistance, nil
	} else {
		// Distance to the next fix plus sum of the distances between
		// remaining fixes.
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
		// Don't include the final waypoint, which should be the
		// destination airport.
		for i := 0; i < len(wp)-2; i++ {
			remainingDistance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
		}

		return remainingDistance, nil
	}
}
