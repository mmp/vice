// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"strings"
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

	// These are for altitudes/speeds to meet at the next fix; unlike
	// controller-assigned ones, where we try to get there as quickly as
	// the aircraft is capable of, these we try to get to exactly at the
	// fix.
	CrossingAltitude int
	CrossingSpeed    int

	Approach        *Approach // if assigned
	ClearedApproach bool
	OnFinal         bool
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

func (a *Aircraft) InterpolatedPosition(t float32) Point2LL {
	// Return the first valid one; this makes things cleaner at the start when
	// we don't have a full set of track history.
	pos := func(idx int) Point2LL {
		if idx >= len(a.Tracks) {
			// Linearly extrapolate the last two. (We don't expect to be
			// doing this often...)
			steps := 1 + idx - len(a.Tracks)
			last := len(a.Tracks) - 1
			v := sub2ll(a.Tracks[last].Position, a.Tracks[last-1].Position)
			return add2ll(a.Tracks[last].Position, scale2ll(v, float32(steps)))
		}
		for idx > 0 {
			if !a.Tracks[idx].Position.IsZero() {
				break
			}
			idx--
		}
		return a.Tracks[idx].Position
	}

	if t < 0 {
		// interpolate past tracks

		t /= -5
		idx := int(t)
		dt := t - float32(idx)

		return lerp2ll(dt, pos(idx), pos(idx+1))
	} else {
		// extrapolate from last track. fit a parabola a t^2 + b t ^ c = x_i
		// to the last three tracks, with associated times assumed to be
		// 0, -5, and -10. We immediately have c=x_0 and are left with:
		// 25 a - 5 b + x0 = x1
		// 100 a - 10 b + x0 = x2
		// Solving gives a = (x0 - 2 x1 + x2) / 50, b = (3 x0 - 4x1 + x2) / 10
		fit := func(x0, x1, x2 float32) (a, b, c float32) {
			a = (x0 - 2*x1 + x2) / 50
			b = (3*x0 - 4*x1 + x2) / 10
			c = x0
			return
		}
		longa, longb, longc := fit(pos(0).Longitude(), pos(1).Longitude(), pos(2).Longitude())
		lata, latb, latc := fit(pos(0).Latitude(), pos(1).Latitude(), pos(2).Latitude())

		return Point2LL{longa*t*t + longb*t + longc, lata*t*t + latb*t + latc}
	}
}

func (a *Aircraft) TrackGroundspeed() int {
	return a.Tracks[0].Groundspeed
}

// Note: returned value includes the magnetic correction
func (a *Aircraft) TrackHeading() float32 {
	return a.Tracks[0].Heading + database.MagneticVariation
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

func (a *Aircraft) ExtrapolatedHeadingVector(lag float32) Point2LL {
	if !a.HaveHeading() {
		return Point2LL{}
	}
	t := float32(time.Since(a.Tracks[0].Time).Seconds()) - lag
	return sub2ll(a.InterpolatedPosition(t+.5), a.InterpolatedPosition(t-0.5))
}

func (a *Aircraft) HeadingTo(p Point2LL) float32 {
	return headingp2ll(a.TrackPosition(), p, database.MagneticVariation)
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

func (a *Aircraft) Telephony() string {
	// FIXME: this doesn't handle trailing characters: DAL42E
	cs := strings.TrimRight(a.Callsign, "0123456789")
	if sign, ok := database.callsigns[cs]; ok {
		return sign.Telephony
	} else {
		return ""
	}
}

func (a *Aircraft) IsAssociated() bool {
	return a.FlightPlan != nil && a.Squawk == a.AssignedSquawk && a.Mode == Charlie
}

func (a *Aircraft) OnGround() bool {
	if a.GS < 40 {
		return true
	}

	if fp := a.FlightPlan; fp != nil {
		for _, airport := range [2]string{fp.DepartureAirport, fp.ArrivalAirport} {
			if ap, ok := database.FAA.airports[airport]; ok {
				heightAGL := abs(a.TrackAltitude() - ap.Elevation)
				return heightAGL < 100
			}
		}
	}
	// Didn't know the airports. We could be more fancy and find the
	// closest airport in the sector file and then use its elevation,
	// though it's not clear that is worth the work.
	return false
}

func (ac *Aircraft) WaypointUpdate(wp Waypoint) {
	if *devmode {
		lg.Printf("Waypoint update. wp %s ac %s", spew.Sdump(wp), spew.Sdump(ac))
	}

	// Now handle any altitude/speed restriction at the next waypoint.
	if wp.Altitude != 0 {
		// TODO: we should probably distinguish between controller-assigned
		// altitude and assigned due to a previous crossing restriction,
		// since controller assigned should take precedence over
		// everything, which it doesn't currently...
		ac.CrossingAltitude = wp.Altitude
		ac.AssignedAltitude = 0
	}
	if wp.Speed != 0 {
		ac.CrossingSpeed = wp.Speed
		ac.AssignedSpeed = 0
	}

	ac.AssignedHeading = 0
	ac.TurnDirection = 0

	if ac.ClearedApproach {
		// The aircraft has made it to the approach fix they
		// were cleared to.
		//lg.Errorf("%s: on final...", ac.Callsign)
		ac.OnFinal = true
	}
}

func (ac *Aircraft) Update() {
	ac.updateAirspeed()
	ac.updateAltitude()
	ac.updateHeading()
	ac.updatePositionAndGS()
	ac.updateWaypoints()
}

func (ac *Aircraft) updateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance
	var targetSpeed int

	// Slow down on final approach
	if ac.OnFinal {
		if airportPos, ok := database.Locate(ac.FlightPlan.ArrivalAirport); ok {
			airportDist := nmdistance2ll(ac.Position, airportPos)
			if airportDist < 1 {
				targetSpeed = perf.Speed.Landing
			} else if airportDist < 5 || (airportDist < 10 && ac.AssignedSpeed == 0) {
				// Ignore speed restrictions if the aircraft is within 5
				// miles; otherwise start slowing down if it hasn't't been
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
		}
	}

	if targetSpeed == 0 && ac.AssignedSpeed != 0 {
		// Use the controller-assigned speed, but only as far as the
		// aircraft's capabilities.
		targetSpeed = clamp(ac.AssignedSpeed, perf.Speed.Min, perf.Speed.Max)
	}

	if targetSpeed == 0 && ac.CrossingSpeed != 0 {
		eta, ok := ac.NextFixETA()
		if !ok {
			lg.Errorf("unable to get crossing fix eta... %s", spew.Sdump(ac))
			return
		}

		cs := float32(ac.CrossingSpeed)
		if ac.IAS < cs {
			accel := (cs - ac.IAS) / float32(eta.Seconds()) * 1.25
			accel = min(accel, ac.Performance.Rate.Accelerate/2)
			ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
		} else if ac.IAS > cs {
			decel := (ac.IAS - cs) / float32(eta.Seconds()) * 0.75
			decel = min(decel, ac.Performance.Rate.Decelerate/2)
			ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
			//lg.Errorf("dist %f eta %s ias %f crossing %f decel %f", dist, eta, ac.IAS, cs, decel)
		}

		return
	}

	if targetSpeed == 0 {
		targetSpeed = perf.Speed.Cruise

		// But obey 250kts under 10,000'
		if ac.Altitude < 10000 {
			targetSpeed = min(targetSpeed, 250)
		}
	}

	// Finally, adjust IAS subject to the capabilities of the aircraft.
	if ac.IAS+1 < float32(targetSpeed) {
		accel := ac.Performance.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
	} else if ac.IAS-1 > float32(targetSpeed) {
		decel := ac.Performance.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
	}
}

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
	} else if !ac.ClearedApproach || ac.OnFinal {
		// We have a crossing altitude, but ignore it if the aircraft is
		// below the next crossing altitude, has been cleared for the
		// approach, but hasn't yet joined the final approach course.
		// (i.e., don't climb then!)
		eta, ok := ac.NextFixETA()
		if !ok {
			lg.Errorf("unable to get crossing fix eta... %s", spew.Sdump(ac))
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
		hdg := ac.Heading - database.MagneticVariation
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
			lg.Errorf("behind us...")
		} else {
			// Find eta to the intercept and the turn required to align with
			// the localizer.
			dist := distance2f(pos, isect)
			eta := dist / ac.GS * 3600 // in seconds
			turn := abs(headingDifference(hdg, float32(ap.Heading())-database.MagneticVariation))
			//lg.Errorf("dist %f, eta %f, turn %f", dist, eta, turn)

			// Assuming 3 degree/second turns, then we might start to turn to
			// intercept when the eta until intercept is 1/3 the number of
			// degrees to cover.  However... the aircraft approaches the
			// localizer more slowly as it turns, so we'll add another 1/2
			// fudge factor, which seems to account for that reasonably well.
			if eta < turn/3/2 {
				lg.Errorf("assigned approach heading! %d", ap.Heading())
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
			database.MagneticVariation)
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
	hdg := ac.Heading - database.MagneticVariation
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
			lg.Errorf("intercepted the localizer @ %.2fnm!", thresholdDistance)

			ac.Waypoints = nil
			for _, wp := range ap.Waypoints[0] {
				if distance2f(ll2nm(wp.Location), threshold) < thresholdDistance {
					lg.Errorf("%s: adding future waypoint...", wp.Fix)
					ac.Waypoints = append(ac.Waypoints, wp)
				} else {
					// We consider the waypoints from far away to near (and
					// so in the end we want a contiguous set of them
					// starting from the runway threshold). Any time we
					// find a waypoint that is farther away than the
					// aircraft, we preemptively clear out the aircraft's
					// waypoints; in this way if, for example, an IAF is
					// somehow closer to the airport than the aircraft,
					// then we won't include it in the aircraft's upcoming
					// waypoints.
					lg.Errorf("clearing those waypoints...")
					ac.Waypoints = nil
				}
			}

			ac.AssignedHeading = 0
			ac.AssignedAltitude = 0
			ac.OnFinal = true
		}
		return
	}

	if len(ac.Waypoints) == 0 {
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
		hdg = headingp2ll(wp.Location, ac.Waypoints[1].Location, database.MagneticVariation)
	}

	eta := wp.ETA(ac.Position, ac.GS)
	turn := abs(headingDifference(hdg, ac.Heading))
	//lg.Errorf("%s: dist to %s %.2fnm, eta %s, next hdg %.1f turn %.1f, go: %v",
	// ac.Callsign, wp.Fix, dist, eta, hdg, turn, eta < turn/3/2)

	// We'll wrap things up for the upcoming waypoint if we're within 2
	// seconds of reaching it or if the aircraft has to turn to a new
	// direction and the time to turn to the outbound heading is 1/6 of the
	// number of degrees the turn will be.  The first test ensures that we
	// don't fly over the waypoint in the case where there is no turn
	// (e.g. when we're established on the localizer) and the latter test
	// assumes a 3 degree/second turn and then adds a 1/2 factor to account
	// for the arc of the turn.  (Ad hoc, but it seems to work well enough.)
	if s := float32(eta.Seconds()); s < 2 || (hdg != 0 && s < turn/3/2) {
		// Execute any commands associated with the waypoint
		ac.RunWaypointCommands(wp.Commands)

		// For starters, convert a previous crossing restriction to a current
		// assignment.  Clear out the previous crossing restriction.
		if ac.AssignedAltitude == 0 {
			ac.AssignedAltitude = ac.CrossingAltitude
		}
		ac.CrossingAltitude = 0

		if ac.AssignedSpeed == 0 {
			ac.AssignedSpeed = ac.CrossingSpeed
		}
		ac.CrossingSpeed = 0

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
	}
}

func (ac *Aircraft) RunWaypointCommands(cmds []WaypointCommand) {
	for _, cmd := range cmds {
		switch cmd {
		case WaypointCommandHandoff:
			// Handoff to the user's position?
			ac.InboundHandoffController = sim.Callsign()
			eventStream.Post(&OfferedHandoffEvent{controller: ac.TrackingController, ac: ac})

		case WaypointCommandDelete:
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
			return
		}
	}
}
