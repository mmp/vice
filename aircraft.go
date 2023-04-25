// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"time"

	"github.com/davecgh/go-spew/spew"
)

type Aircraft struct {
	Callsign       string
	Scratchpad     string
	AssignedSquawk Squawk // from ATC
	Squawk         Squawk // actually squawking
	Mode           TransponderMode
	TempAltitude   int
	FlightPlan     *FlightPlan

	Tracks [10]RadarTrack

	TrackingController        string
	InboundHandoffController  string
	OutboundHandoffController string

	Performance AircraftPerformance
	Strip       FlightStrip
	Waypoints   []Waypoint

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...

	// The following are for controller-assigned altitudes, speeds, and
	// headings.  Values of 0 indicate no assignment.
	AssignedAltitude int
	AssignedSpeed    int
	AssignedHeading  int
	TurnDirection    int

	// If the controller directs "descend and maintain <ALT>, then reduce
	// speed to <SPD>", then the altitude is stored in AssignedAltitude and
	// the speed is stored in AssignedSpeedAfterAltitude.  Then after the
	// altitude is reached, the speed restriction in AssignedSpeed is set
	// (and the flight model will start paying attention to it.)
	AssignedSpeedAfterAltitude int
	AssignedAltitudeAfterSpeed int

	// These are for altitudes/speeds to meet at the next fix; unlike
	// controller-assigned ones, where we try to get there as quickly as
	// the aircraft is capable of, these we try to get to exactly at the
	// fix.
	CrossingAltitude int
	CrossingSpeed    int

	Approach            *Approach // if assigned
	ClearedApproach     bool
	OnFinal             bool
	HaveEnteredAirspace bool
}

func (a *Aircraft) TrackAltitude() int {
	return a.Tracks[0].Altitude
}

// Reported in feet per minute
func (a *Aircraft) AltitudeChange() int {
	if a.Tracks[0].Position.IsZero() || a.Tracks[1].Position.IsZero() {
		return 0
	}

	dt := a.Tracks[0].Time.Sub(a.Tracks[1].Time)
	return int(float64(a.Tracks[0].Altitude-a.Tracks[1].Altitude) / dt.Minutes())
}

func (ac *Aircraft) TAS() float32 {
	// Simple model for the increase in TAS as a function of altitude: 2%
	// additional TAS on top of IAS for each 1000 feet.
	return ac.IAS * (1 + .02*ac.Altitude/1000)
}

// Returns the estimated time in which the aircraft will reach the next fix
// in its waypoints, assuming it is flying direct to it at its current
// speed.
func (ac *Aircraft) NextFixETA() (time.Duration, bool) {
	if len(ac.Waypoints) == 0 {
		return 0, false
	}
	return ac.Waypoints[0].ETA(ac.Position, ac.GS), true
}

func (a *Aircraft) HaveTrack() bool {
	return a.TrackPosition()[0] != 0 || a.TrackPosition()[1] != 0
}

func (a *Aircraft) TrackPosition() Point2LL {
	return a.Tracks[0].Position
}

func (a *Aircraft) TrackGroundspeed() int {
	return a.Tracks[0].Groundspeed
}

// Note: returned value includes the magnetic correction
func (a *Aircraft) TrackHeading() float32 {
	return a.Tracks[0].Heading + scenarioGroup.MagneticVariation
}

// Perhaps confusingly, the vector returned by HeadingVector() is not
// aligned with the reported heading but is instead along the aircraft's
// extrapolated path.  Thus, it includes the effect of wind.  The returned
// vector is scaled so that it represents where it is expected to be one
// minute in the future.
func (a *Aircraft) HeadingVector() Point2LL {
	var v [2]float32
	if !a.HaveHeading() {
		v = [2]float32{cos(radians(a.TrackHeading())), sin(radians(a.TrackHeading()))}
	} else {
		p0, p1 := a.Tracks[0].Position, a.Tracks[1].Position
		v = sub2ll(p0, p1)
	}

	nm := nmlength2ll(v)
	// v's length should be groundspeed / 60 nm.
	return scale2ll(v, float32(a.TrackGroundspeed())/(60*nm))
}

func (a *Aircraft) HaveHeading() bool {
	return !a.Tracks[0].Position.IsZero() && !a.Tracks[1].Position.IsZero()
}

func (a *Aircraft) HeadingTo(p Point2LL) float32 {
	return headingp2ll(a.TrackPosition(), p, scenarioGroup.MagneticVariation)
}

func (a *Aircraft) LostTrack(now time.Time) bool {
	// Only return true if we have at least one valid track from the past
	// but haven't heard from the aircraft recently.
	return !a.Tracks[0].Position.IsZero() && now.Sub(a.Tracks[0].Time) > 30*time.Second
}

func (a *Aircraft) AddTrack(t RadarTrack) {
	// Move everthing forward one to make space for the new one. We could
	// be clever and use a circular buffer to skip the copies, though at
	// the cost of more painful indexing elsewhere...
	copy(a.Tracks[1:], a.Tracks[:len(a.Tracks)-1])
	a.Tracks[0] = t
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (ac *Aircraft) WaypointUpdate(wp Waypoint) {
	// Now handle any altitude/speed restriction at the next waypoint.
	if wp.Altitude != 0 {
		if ac.AssignedAltitude == 0 {
			// TODO: we should probably distinguish between controller-assigned
			// altitude and assigned due to a previous crossing restriction,
			// since controller assigned should take precedence over
			// everything, which it doesn't currently...
			ac.CrossingAltitude = wp.Altitude
		} else if ac.ClearedApproach && ac.AssignedAltitude >= wp.Altitude {
			ac.AssignedAltitude = 0
			ac.CrossingAltitude = wp.Altitude
		}
	}

	// Don't assign the crossing speed if the aircraft has an assigned
	// speed now or in the future.
	if wp.Speed != 0 && ac.AssignedSpeed == 0 && ac.AssignedSpeedAfterAltitude == 0 {
		ac.CrossingSpeed = wp.Speed
	}

	ac.AssignedHeading = 0
	ac.TurnDirection = 0

	if ac.ClearedApproach {
		// The aircraft has made it to the approach fix they
		// were cleared to.
		//lg.Errorf("%s: on final...", ac.Callsign)
		ac.OnFinal = true
	}

	lg.Printf("%s: waypoint update for %s: cross alt %d cross speed %d", ac.Callsign,
		wp.Fix, wp.Altitude, wp.Speed)

	lg.Printf("%s", spew.Sdump(ac))
}

func (ac *Aircraft) Update() {
	ac.updateAirspeed()
	ac.updateAltitude()
	ac.updateHeading()
	ac.updatePositionAndGS()
	ac.updateWaypoints()
}

func (ac *Aircraft) GoAround() {
	ac.AssignedHeading = int(ac.Heading)
	ac.AssignedSpeed = 0

	if ap, ok := database.Airports[ac.FlightPlan.ArrivalAirport]; ok {
		ac.AssignedAltitude = 1000 * ((ap.Elevation + 2500) / 1000)
	} else {
		ac.AssignedAltitude = 1000 * ((int(ac.Altitude) + 2500) / 1000)
	}

	ac.Approach = nil
	ac.ClearedApproach = false
	ac.OnFinal = false

	ac.Waypoints = nil // so it isn't deleted from the sim
}

func (ac *Aircraft) updateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance
	var targetSpeed int

	// Slow down on final approach
	if ac.OnFinal {
		if airportPos, ok := scenarioGroup.Locate(ac.FlightPlan.ArrivalAirport); ok {
			airportDist := nmdistance2ll(ac.Position, airportPos)
			if airportDist < 1 {
				targetSpeed = perf.Speed.Landing
			} else if airportDist < 5 || (airportDist < 10 && ac.AssignedSpeed == 0) {
				// Ignore speed restrictions if the aircraft is within 5
				// miles; otherwise start slowing down if it hasn't been
				// given a speed restriction.

				// Expected speed at 10 DME, without further direction.
				approachSpeed := min(210, perf.Speed.Cruise)
				landingSpeed := perf.Speed.Landing
				targetSpeed = int(lerp((airportDist-1)/9, float32(landingSpeed), float32(approachSpeed)))
			}

			// However, don't accelerate if the aircraft is already under
			// the target speed.
			targetSpeed = min(targetSpeed, int(ac.IAS))
			//lg.Errorf("airport dist %f -> target speed %d", airportDist, targetSpeed)
		} else {
			lg.Errorf("%s: arrival airport %s not known to tracon?!", ac.Callsign,
				ac.FlightPlan.ArrivalAirport)
		}
	}

	if targetSpeed == 0 && ac.AssignedSpeed != 0 {
		// Use the controller-assigned speed, but only as far as the
		// aircraft's capabilities.
		targetSpeed = clamp(ac.AssignedSpeed, perf.Speed.Min, perf.Speed.Max)
	}

	if targetSpeed == 0 && ac.CrossingSpeed != 0 {
		if eta, ok := ac.NextFixETA(); ok {
			cs := float32(ac.CrossingSpeed)
			if ac.IAS+1 < cs {
				accel := (cs - ac.IAS) / float32(eta.Seconds()) * 1.25
				accel = min(accel, ac.Performance.Rate.Accelerate/2)
				ac.IAS = min(cs, ac.IAS+accel)
			} else if ac.IAS-1 > cs {
				decel := (ac.IAS - cs) / float32(eta.Seconds()) * 0.75
				decel = min(decel, ac.Performance.Rate.Decelerate/2)
				ac.IAS = max(cs, ac.IAS-decel)
				//lg.Errorf("dist %f eta %s ias %f crossing %f decel %f", dist, eta, ac.IAS, cs, decel)
			}
			return
		} else {
			//lg.Errorf("unable to get crossing fix eta... %s", spew.Sdump(ac))
			targetSpeed = ac.CrossingSpeed
		}
	}

	if targetSpeed == 0 {
		// But obey 250kts under 10,000'
		if ac.Altitude < 10000 {
			targetSpeed = min(ac.Performance.Speed.Cruise, 250)
		} else {
			// Assume climbing or descending
			targetSpeed = ac.Performance.Speed.Cruise * 7 / 10
		}
	}

	// Finally, adjust IAS subject to the capabilities of the aircraft.
	if ac.IAS+1 < float32(targetSpeed) {
		accel := ac.Performance.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
	} else if ac.IAS-1 > float32(targetSpeed) {
		decel := ac.Performance.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
	} else {
		// at the requested speed
		if ac.AssignedAltitudeAfterSpeed != 0 {
			ac.AssignedAltitude = ac.AssignedAltitudeAfterSpeed
			ac.AssignedAltitudeAfterSpeed = 0
		}
	}
}

var etaWarnings map[string]interface{} = make(map[string]interface{})

func (ac *Aircraft) updateAltitude() {
	// Climb or descend, but only if it's going fast enough to be
	// airborne.  (Assume no stalls in flight.)
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if !airborne {
		return
	}

	if ac.AssignedAltitude == 0 && ac.CrossingAltitude == 0 {
		// No altitude assignment, so... just stay where we are
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := float32(ac.Performance.Rate.Climb), float32(ac.Performance.Rate.Descent)

	// For high performing aircraft, reduce climb rate after 5,000'
	if climb >= 2500 && ac.Altitude > 5000 {
		climb -= 500
	}
	if ac.Altitude < 10000 {
		// Have a slower baseline rate of descent on approach
		descent = min(descent, 1500)
		// And reduce it based on airspeed as well
		descent *= min(ac.IAS/250, 1)
	}

	if ac.AssignedAltitude != 0 {
		// Controller-assigned altitude takes precedence over a crossing
		// altitude.

		if ac.Altitude < float32(ac.AssignedAltitude) {
			// Simple model: we just update altitude based on the rated climb
			// rate; does not account for simultaneous acceleration, etc...
			ac.Altitude = min(float32(ac.AssignedAltitude), ac.Altitude+climb/60)
		} else if ac.Altitude > float32(ac.AssignedAltitude) {
			// Similarly, descent modeling doesn't account for airspeed or
			// acceleration/deceleration...
			ac.Altitude = max(float32(ac.AssignedAltitude), ac.Altitude-descent/60)
		}

		// If we've reached the assigned altitude and have a speed ready
		// for after that, then make that our current assigned speed.
		if abs(ac.Altitude-float32(ac.AssignedAltitude)) < .1 {
			ac.Altitude = float32(ac.AssignedAltitude)
			ac.AssignedAltitude = 0
			if ac.AssignedSpeedAfterAltitude != 0 {
				ac.AssignedSpeed = ac.AssignedSpeedAfterAltitude
				ac.AssignedSpeedAfterAltitude = 0
			}
		}
	} else if ac.CrossingAltitude != 0 && (!ac.ClearedApproach || ac.OnFinal) {
		// We have a crossing altitude, but we ignore it if the aircraft is
		// below the next crossing altitude, has been cleared for the
		// approach, but hasn't yet joined the final approach course.
		// (i.e., don't climb in that case!)
		eta, ok := ac.NextFixETA()
		if !ok {
			w := fmt.Sprintf("%s: unable to get fix eta for crossing alt %d", ac.Callsign, ac.CrossingAltitude)
			if _, ok := etaWarnings[w]; !ok {
				etaWarnings[w] = nil
				lg.Printf("%s", w)
			}
			return
		}

		if ac.CrossingAltitude > int(ac.Altitude) {
			// Need to climb.  Figure out rate of climb that would get us
			// there when we reach the fix (ft/min).
			rate := (float32(ac.CrossingAltitude) - ac.Altitude) / float32(eta.Minutes())

			// But we can't climb faster than the aircraft is capable of.
			ac.Altitude += min(rate, climb) / 60
		} else {
			// Need to descend; same logic as the climb case.
			rate := (ac.Altitude - float32(ac.CrossingAltitude)) / float32(eta.Minutes())
			ac.Altitude -= min(rate, descent) / 60
			//lg.Errorf("dist %f eta %f alt %f crossing %d eta %f -> rate %f ft/min -> delta %f",
			//dist, eta, ac.Altitude, ac.CrossingAltitude, eta, rate, min(rate, descent)/60)
		}
	}
}

func (ac *Aircraft) updateHeading() {
	// Figure out the heading; if the route is empty, just leave it
	// on its current heading...
	targetHeading := ac.Heading
	turn := float32(0)

	// Are we intercepting a localizer? Possibly turn to join it.
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		ap.Type == ILSApproach &&
		ac.AssignedHeading != 0 &&
		ac.AssignedHeading != ap.Heading() &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 40 /* allow quite some slop... */ {
		// Estimate time to intercept.  Do this using nm coordinates
		loc := ap.Line()
		loc[0], loc[1] = ll2nm(loc[0]), ll2nm(loc[1])

		pos := ll2nm(ac.Position)
		hdg := ac.Heading - scenarioGroup.MagneticVariation
		headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		pos1 := add2f(pos, headingVector)

		// Intersection of aircraft's path with the localizer
		isect, ok := LineLineIntersect(loc[0], loc[1], pos, pos1)
		if !ok {
			lg.Errorf("no intersect!")
			return // better luck next time...
		}

		// Is the intersection behind the aircraft? (This can happen if it
		// has flown through the localizer.) Ignore it if so.
		v := sub2f(isect, pos)
		if v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
			lg.Errorf("%s: localizer intersection is behind us...", ac.Callsign)
		} else {
			// Find eta to the intercept and the turn required to align with
			// the localizer.
			dist := distance2f(pos, isect)
			eta := dist / ac.GS * 3600 // in seconds
			turn := abs(headingDifference(hdg, float32(ap.Heading())-scenarioGroup.MagneticVariation))
			//lg.Errorf("dist %f, eta %f, turn %f", dist, eta, turn)

			// Assuming 3 degree/second turns, then we might start to turn to
			// intercept when the eta until intercept is 1/3 the number of
			// degrees to cover.  However... the aircraft approaches the
			// localizer more slowly as it turns, so we'll add another 1/2
			// fudge factor, which seems to account for that reasonably well.
			if eta < turn/3/2 {
				lg.Printf("%s: assigned approach heading! %d", ac.Callsign, ap.Heading())
				ac.AssignedHeading = ap.Heading()
				ac.TurnDirection = 0
				// Just in case.. Thus we will be ready to pick up the
				// approach waypoints once we capture.
				ac.Waypoints = nil
			}
		}
	}

	// Otherwise, if the controller has assigned a heading, then no matter
	// what, that's what we will turn to.
	if ac.AssignedHeading != 0 {
		targetHeading = float32(ac.AssignedHeading)
		if ac.TurnDirection != 0 {
			// If the controller specified a left or right turn, then
			// compute the full turn angle. We'll do no more than 3 degrees
			// of that.
			if ac.TurnDirection < 0 { // left
				angle := ac.Heading - targetHeading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = -angle
			} else if ac.TurnDirection > 0 { // right
				angle := targetHeading - ac.Heading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = angle
			}
		}
	} else if len(ac.Waypoints) > 0 {
		// Our desired heading is the heading to get to the next waypoint.
		targetHeading = headingp2ll(ac.Position, ac.Waypoints[0].Location,
			scenarioGroup.MagneticVariation)
	} else {
		// And otherwise we're flying off into the void...
		return
	}

	// A turn direction wasn't specified, so figure out which way is
	// closest.
	if turn == 0 {
		// First find the angle to rotate the target heading by so that
		// it's aligned with 180 degrees. This lets us not worry about the
		// complexities of the wrap around at 0/360..
		rot := 180 - targetHeading
		if rot < 0 {
			rot += 360
		}
		cur := mod(ac.Heading+rot, 360) // w.r.t. 180 target
		turn = clamp(180-cur, -3, 3)    // max 3 degrees / second
	}

	// Finally, do the turn.
	if ac.Heading != targetHeading {
		ac.Heading += turn
		if ac.Heading < 0 {
			ac.Heading += 360
		} else if ac.Heading > 360 {
			ac.Heading -= 360
		}
	}
}

func (ac *Aircraft) updatePositionAndGS() {
	// Update position given current heading
	prev := ac.Position
	hdg := ac.Heading - scenarioGroup.MagneticVariation
	v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
	// First use TAS to get a first whack at the new position.
	newPos := add2f(ll2nm(ac.Position), scale2f(v, ac.TAS()/3600))

	// Now add wind...
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if airborne {
		windVector := sim.GetWindVector(ac.Position, ac.Altitude)
		newPos = add2f(newPos, ll2nm(windVector))
	}

	if ap := ac.Approach; ap != nil && ac.OnFinal && ac.Approach.Type == ILSApproach {
		// Nudge the aircraft to stay on the localizer if it's close.
		loc := ap.Line()
		// But if it's too far away, leave it where it is; this case can in
		// particular happen if it's been given direct to a fix that's not
		// on the localizer.
		if dist := SignedPointLineDistance(newPos, ll2nm(loc[0]), ll2nm(loc[1])); abs(dist) < .3 {
			v := normalize2f(sub2f(ll2nm(loc[1]), ll2nm(loc[0])))
			vperp := [2]float32{v[1], -v[0]}
			//lg.Printf("dist %f: %v - %v -> %v", dist, newPos, scale2f(vperp, dist), sub2f(newPos, scale2f(vperp, dist)))
			newPos = sub2f(newPos, scale2f(vperp, dist))
			//lg.Printf(" -> dist %f", SignedPointLineDistance(newPos, ll2nm(loc[0]), ll2nm(loc[1])))
		}
	}

	// Finally update position and groundspeed.
	ac.Position = nm2ll(newPos)
	ac.GS = distance2f(ll2nm(prev), newPos) * 3600
}

func (ac *Aircraft) updateWaypoints() {
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		!ac.OnFinal &&
		len(ac.Waypoints) == 0 &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 2 &&
		ac.Approach.Type == ILSApproach {
		// Have we intercepted the localizer?
		loc := ap.Line()
		dist := PointLineDistance(ll2nm(ac.Position), ll2nm(loc[0]), ll2nm(loc[1]))

		if dist < .2 {
			// we'll call that good enough. Now we need to figure out which
			// fixes in the approach are still ahead and then add them to
			// the aircraft's waypoints; we find the aircraft's distance to
			// the runway threshold and taking any fixes that are closer
			// than that distance.
			n := len(ap.Waypoints[0])
			threshold := ll2nm(ap.Waypoints[0][n-1].Location)
			thresholdDistance := distance2f(ll2nm(ac.Position), threshold)
			lg.Printf("%s: intercepted the localizer @ %.2fnm!", ac.Callsign, thresholdDistance)

			ac.Waypoints = nil
			for _, wp := range ap.Waypoints[0] {
				if distance2f(ll2nm(wp.Location), threshold) < thresholdDistance {
					lg.Printf("%s: %s: adding future waypoint...", ac.Callsign, wp.Fix)
					ac.Waypoints = append(ac.Waypoints, wp)
				} else if ac.Waypoints != nil {
					// We consider the waypoints from far away to near (and
					// so in the end we want a contiguous set of them
					// starting from the runway threshold). Any time we
					// find a waypoint that is farther away than the
					// aircraft, we preemptively clear out the aircraft's
					// waypoints; in this way if, for example, an IAF is
					// somehow closer to the airport than the aircraft,
					// then we won't include it in the aircraft's upcoming
					// waypoints.
					lg.Printf("%s: clearing those waypoints...", ac.Callsign)
					ac.Waypoints = nil
				}
			}

			ac.AssignedHeading = 0
			ac.AssignedAltitude = 0
			ac.AssignedAltitudeAfterSpeed = 0
			ac.OnFinal = true
			if len(ac.Waypoints) > 0 {
				ac.WaypointUpdate(ac.Waypoints[0])
			}
		}
		return
	}

	if len(ac.Waypoints) == 0 || ac.AssignedHeading != 0 {
		return
	}

	wp := ac.Waypoints[0]

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if len(ac.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = headingp2ll(wp.Location, ac.Waypoints[1].Location, scenarioGroup.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = ac.Heading
	}

	eta := wp.ETA(ac.Position, ac.GS)
	turnAngle := abs(headingDifference(hdg, ac.Heading))

	// Assuming 3 degree/second turns, we might start to turn to the
	// heading leaving the waypoint when turnAngle/3==eta, though we'd turn
	// too early then since turning starts to put us in the new direction
	// away from the fix.  An ad-hoc angle/5 generally seems to work well
	// instead. Also checking against 2 seconds ensures that we don't miss
	// fixes where there's little to no turn...
	if s := float32(eta.Seconds()); s < max(2, turnAngle/5) {
		// Execute any commands associated with the waypoint
		ac.RunWaypointCommands(wp.Commands)

		if ac.Waypoints[0].Heading != 0 {
			// We have an outbound heading
			ac.AssignedHeading = wp.Heading
			ac.TurnDirection = 0
			// The aircraft won't head to the next waypoint until the
			// assigned heading is cleared, though...
			ac.Waypoints = ac.Waypoints[1:]
		} else {
			ac.Waypoints = ac.Waypoints[1:]

			if len(ac.Waypoints) > 0 {
				ac.WaypointUpdate(ac.Waypoints[0])
			}
		}

		lg.Printf("%s", spew.Sdump(ac))
	}
}

func (ac *Aircraft) RunWaypointCommands(cmds []WaypointCommand) {
	for _, cmd := range cmds {
		switch cmd {
		case WaypointCommandHandoff:
			// Handoff to the user's position?
			ac.InboundHandoffController = sim.Callsign()
			globalConfig.Audio.PlaySound(AudioEventInboundHandoff)

		case WaypointCommandDelete:
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
		}
	}
}
