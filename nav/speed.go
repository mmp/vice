// nav/speed.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) updateAirspeed(callsign string, alt float32, fp *av.FlightPlan, wxs wx.Sample, simTime time.Time, bravo *av.AirspaceGrid) (float32, bool) {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.
	targetSpeed, targetRate := nav.TargetSpeed(alt, fp, wxs, bravo)

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
				nav.Altitude.Assigned = nav.Altitude.AfterSpeed
				nav.Altitude.Expedite = nav.Altitude.ExpediteAfterSpeed
				nav.Altitude.AfterSpeed = nil
				nav.Altitude.AfterSpeedSpeed = nil
				nav.Altitude.Restriction = nil
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

	if nav.Altitude.Expedite {
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
		} else if nav.Altitude.Assigned != nil && nav.FlightState.Altitude < *nav.Altitude.Assigned {
			// Reduce acceleration since also climbing
			if nav.FlightState.InitialDepartureClimb {
				// But less so in the initial climb, assuming full power.
				accel *= 0.8
			} else {
				accel *= 0.6
			}
		}
		return setSpeed(min(targetSpeed, nav.FlightState.IAS+accel))
	} else if nav.FlightState.IAS > targetSpeed {
		decel := nav.Perf.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		if nav.Altitude.Assigned != nil && nav.FlightState.Altitude > *nav.Altitude.Assigned {
			// Reduce deceleration since also descending
			decel *= 0.6
		}
		return setSpeed(max(targetSpeed, nav.FlightState.IAS-decel))
	} else {
		return 0, false
	}
}
func (nav *Nav) TargetSpeed(targetAltitude float32, fp *av.FlightPlan, wxs wx.Sample, bravo *av.AirspaceGrid) (float32, float32) {
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
	if nav.Speed.Assigned != nil {
		return *nav.Speed.Assigned, MaximumRate
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
			targetSpeed = min(targetSpeed, *nav.Speed.Restriction)
		}
		if _, speed, _, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
			targetSpeed = min(targetSpeed, speed)
		}

		// However, don't let anything prevent us from taking off!
		targetSpeed = max(targetSpeed, nav.v2())

		return targetSpeed, 0.8 * maxAccel
	}

	if wpOnSID, speed, eta, ok := nav.getUpcomingSpeedRestrictionWaypoint(); nav.Heading.Assigned == nil && ok {
		if eta < 5 { // includes unknown ETA case
			return speed, MaximumRate
		}

		if speed == nav.FlightState.IAS {
			// There already
			return speed, 0
		} else if speed > nav.FlightState.IAS {
			// accelerate immediately
			return speed, MaximumRate
		} else if wpOnSID {
			// don't accelerate past speed constraints on SIDs
			return speed, MaximumRate
		} else {
			// go slow on deceleration
			rate := (nav.FlightState.IAS - speed) / eta
			decel := nav.Perf.Rate.Decelerate / 2 // it's specified in per 2 seconds...
			if rate > decel/2 {
				// Start to decelerate.
				return speed, MaximumRate
			}
			// Otherwise fall through in case anything else applies.
		}
	}

	// Something from a previous waypoint; ignore it if we're cleared for the approach.
	if nav.Speed.Restriction != nil && !nav.Approach.Cleared {
		return *nav.Speed.Restriction, MaximumRate
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
		hdg := nav.Approach.Assigned.RunwayHeading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		approachSpeed := nav.Perf.ApproachSpeed(wxs.WindDirection(), wxs.WindSpeed(), 0 /* FIXME: GUST */, hdg)

		if fd < 0.5 {
			// Short final: slow down to landing speed.
			approachSpeed = math.Lerp(fd/0.5, nav.Perf.Speed.Landing, approachSpeed)
		}

		// Don't speed up after being been cleared to land.
		ias := min(approachSpeed, nav.FlightState.IAS)

		return ias, MaximumRate
	}

	if nav.Approach.Cleared {
		// Don't speed up if we're cleared and farther away
		return nav.FlightState.IAS, MaximumRate
	}

	// Nothing assigned by the controller or the route, so set a target
	// based on the aircraft's altitude.
	ias, rate := nav.targetAltitudeIAS()
	if fp != nil && fp.Rules == av.FlightRulesVFR &&
		av.UnderBravoShelf(bravo, nav.FlightState.Position, int(nav.FlightState.Altitude)) {
		ias = min(ias, 200)
	}

	return ias, rate
}

// ETA returns the estimated time in seconds until the aircraft will arrive at `p`, assuming it is flying direct.
func (nav *Nav) ETA(p math.Point2LL) float32 {
	dist := math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude)
	return dist / nav.FlightState.GS * 3600 // seconds
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

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (onSID bool, speed float32, eta float32, ok bool) {
	haveWaypointSpeedRestriction :=
		slices.ContainsFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Speed > 0 })

	// Skip all this work in the (common) case that it's unnecessary.
	if len(nav.FixAssignments) > 0 || haveWaypointSpeedRestriction {
		var eta float32
		for i := range nav.Waypoints {
			wp := &nav.Waypoints[i]
			if i == 0 {
				eta = float32(wp.ETA(nav.FlightState.Position, nav.FlightState.GS,
					nav.FlightState.NmPerLongitude).Seconds())
			} else {
				d := math.NMDistance2LLFast(wp.Location, nav.Waypoints[i-1].Location,
					nav.FlightState.NmPerLongitude)
				etaHours := d / nav.FlightState.GS
				eta += etaHours * 3600
			}

			spd := float32(wp.Speed)
			if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
				spd = *nfa.Arrive.Speed
			}

			if spd != 0 {
				return wp.OnSID, spd, eta, true
			}
		}
	}
	return false, 0, 0, false
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
