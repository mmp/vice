// aircraft.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"reflect"
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

	// Who has the radar track
	TrackingController string
	// Who has control of the aircraft; may not be the same as
	// TrackingController, e.g. after an aircraft has been flashed but
	// before they have been instructed to contact the new tracking
	// controller.
	ControllingController string

	InboundHandoffController  string
	OutboundHandoffController string

	Performance AircraftPerformance
	Strip       FlightStrip
	Waypoints   []Waypoint

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...

	IsDeparture bool
	NoPT        bool

	Nav NAVState

	// Set for arrivals, if there are runway-specific waypoints.
	ArrivalRunwayWaypoints map[string]WaypointArray

	Approach            *Approach // if assigned
	ApproachCleared     bool
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

func (ac *Aircraft) AddFutureNavCommand(cmd FutureNavCommand) {
	if ac.Nav.FutureCommands == nil {
		ac.Nav.FutureCommands = make(map[FutureNavCommand]interface{})
	}
	for other := range ac.Nav.FutureCommands {
		if reflect.TypeOf(cmd) == reflect.TypeOf(other) {
			delete(ac.Nav.FutureCommands, other)
		}
	}
	ac.Nav.FutureCommands[cmd] = nil
}

func (ac *Aircraft) HaveAssignedSpeed() bool {
	if _, ok := ac.Nav.S.(*MaintainSpeed); ok {
		return true
	}
	for cmd := range ac.Nav.FutureCommands {
		if reflect.TypeOf(cmd) == reflect.TypeOf(&SpeedAfterAltitude{}) {
			return true
		}
	}

	return false
}

func (ac *Aircraft) Update() {
	ac.updateAirspeed()
	ac.updateAltitude()
	ac.updateHeading()
	ac.updatePositionAndGS()
	if ac.Nav.L.PassesWaypoints() {
		ac.updateWaypoints()
	}

	for cmd := range ac.Nav.FutureCommands {
		if cmd.Evaluate(ac) {
			delete(ac.Nav.FutureCommands, cmd)
		}
	}
}

func (ac *Aircraft) GoAround(sim *Sim) {
	ac.Nav.L = &FlyHeading{Heading: ac.Heading}

	spd := ac.Performance.Speed
	targetSpeed := min(1.6*spd.Landing, 0.8*spd.Cruise)
	ac.Nav.S = &MaintainSpeed{IAS: targetSpeed}

	if ap, ok := database.Airports[ac.FlightPlan.ArrivalAirport]; ok {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(1000 * ((ap.Elevation + 2500) / 1000))}
	} else {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(1000 * ((int(ac.Altitude) + 2500) / 1000))}
	}

	ac.Approach = nil
	ac.ApproachCleared = false
	ac.Waypoints = nil
	ac.NoPT = false

	// If it was handed off to tower, hand it back to us
	if ac.TrackingController != "" && ac.TrackingController != sim.Callsign() {
		ac.InboundHandoffController = sim.Callsign()
		globalConfig.Audio.PlaySound(AudioEventInboundHandoff)
	}

	// Nuke all of them...
	ac.Nav.FutureCommands = make(map[FutureNavCommand]interface{})
}

func (ac *Aircraft) AssignAltitude(altitude int) (string, error) {
	var response string
	if float32(altitude) > ac.Altitude {
		response = fmt.Sprintf("climb and maintain %d", altitude)
	} else if float32(altitude) == ac.Altitude {
		response = fmt.Sprintf("maintain %d", altitude)
	} else {
		response = fmt.Sprintf("descend and maintain %d", altitude)
	}

	if spd, ok := ac.Nav.S.(*MaintainSpeed); ok && spd.IAS != ac.IAS {
		ac.AddFutureNavCommand(&AltitudeAfterSpeed{
			FromAbove: ac.IAS > spd.IAS,
			IAS:       spd.IAS,
			Altitude:  float32(altitude),
		})
		response = fmt.Sprintf("at %.0f knots, ", spd.IAS) + response
	} else {
		ac.Nav.V = &MaintainAltitude{Altitude: float32(altitude)}
	}

	return response, nil
}

func (ac *Aircraft) AssignSpeed(speed int) (response string, err error) {
	if speed == 0 {
		response = "cancel speed restrictions"
		ac.Nav.S = &FlyRoute{}
		return
	} else if float32(speed) < ac.Performance.Speed.Landing {
		response = fmt.Sprintf("unable--our minimum speed is %.0f knots", ac.Performance.Speed.Landing)
		err = ErrUnableCommand
		return
	} else if float32(speed) > ac.Performance.Speed.Max {
		response = fmt.Sprintf("unable--our maximum speed is %.0f knots", ac.Performance.Speed.Max)
		err = ErrUnableCommand
		return
	}

	if alt, _ := ac.Nav.V.GetAltitude(ac); alt != ac.Altitude {
		response = fmt.Sprintf("at %.0f feet, maintain %d knots",
			alt, speed)
		ac.AddFutureNavCommand(&SpeedAfterAltitude{
			Altitude:  alt,
			FromAbove: alt < ac.Altitude,
			IAS:       float32(speed),
		})
	} else {
		response = fmt.Sprintf("maintain %d knots", speed)
		ac.Nav.S = &MaintainSpeed{IAS: float32(speed)}

		if ac.ApproachCleared {
			response += " until 5 mile final"
		}
	}
	return
}

func (ac *Aircraft) AssignHeading(heading int, turn int) (response string, err error) {
	// A 0 heading shouldn't be specified, but at least cause the
	// aircraft to do what is intended, since 0 represents an
	// unassigned heading.
	if heading == 0 {
		heading = 360
	}

	// Only cancel approach clearance if the aircraft wasn't on a
	// heading and now we're giving them one.
	if _, ok := ac.Nav.L.(*FlyHeading); !ok {
		ac.CancelApproachClearance()
	}

	if turn > 0 {
		response = fmt.Sprintf("turn right heading %d", heading)
		ac.Nav.L = &FlyHeading{Heading: float32(heading), Turn: TurnRight}
	} else if turn == 0 {
		response = fmt.Sprintf("fly heading %d", heading)
		ac.Nav.L = &FlyHeading{Heading: float32(heading)}
	} else {
		response = fmt.Sprintf("turn left heading %d", heading)
		ac.Nav.L = &FlyHeading{Heading: float32(heading), Turn: TurnLeft}
	}

	ac.NoPT = false

	return
}

func (ac *Aircraft) TurnLeft(deg int) (string, error) {
	heading := ac.Heading
	if fh, ok := ac.Nav.L.(*FlyHeading); ok {
		heading = fh.Heading
	} else {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		ac.CancelApproachClearance()
	}

	heading -= float32(deg)
	if heading <= 0 {
		heading += 360
	}
	ac.Nav.L = &FlyHeading{Heading: heading}
	ac.NoPT = false

	return fmt.Sprintf("turn %d degrees left", deg), nil
}

func (ac *Aircraft) TurnRight(deg int) (string, error) {
	heading := ac.Heading
	if fh, ok := ac.Nav.L.(*FlyHeading); ok {
		heading = fh.Heading
	} else {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		ac.CancelApproachClearance()
	}

	heading += float32(deg)
	if heading > 360 {
		heading -= 360
	}
	ac.Nav.L = &FlyHeading{Heading: heading}
	ac.NoPT = false

	return fmt.Sprintf("turn %d degrees right", deg), nil
}

func (ac *Aircraft) DirectFix(fix string) (string, error) {
	fix = strings.ToUpper(fix)

	// Look for the fix in the waypoints in the flight plan.
	found := false
	for i, wp := range ac.Waypoints {
		if fix == wp.Fix {
			ac.Waypoints = ac.Waypoints[i:]
			found = true
			break
		}
	}

	if !found && ac.Approach != nil {
		for _, route := range ac.Approach.Waypoints {
			for _, wp := range route {
				if wp.Fix == fix {
					ac.Waypoints = []Waypoint{wp}
					found = true
					break
				}
			}
		}
	}

	if found {
		if !ac.flyProcedureTurnIfNecessary() {
			ac.Nav.L = &FlyRoute{}
		}
		ac.NoPT = false

		for cmd := range ac.Nav.FutureCommands {
			switch reflect.TypeOf(cmd) {
			case reflect.TypeOf(&HoldLocalizerAfterIntercept{}):
				delete(ac.Nav.FutureCommands, cmd)
			case reflect.TypeOf(&TurnToInterceptLocalizer{}):
				delete(ac.Nav.FutureCommands, cmd)
			}
		}
		return "direct " + fix, nil
	} else {
		return "", fmt.Errorf("%s: fix not found in route", fix)
	}
}

func (ac *Aircraft) flyProcedureTurnIfNecessary() bool {
	wp := ac.Waypoints
	if !ac.ApproachCleared || len(wp) == 0 || wp[0].ProcedureTurn == nil || ac.NoPT {
		return false
	}

	if wp[0].ProcedureTurn.Entry180NoPT {
		inboundHeading := headingp2ll(wp[0].Location, wp[1].Location,
			scenarioGroup.MagneticVariation)
		acFixHeading := headingp2ll(ac.Position, wp[0].Location,
			scenarioGroup.MagneticVariation)
		lg.Errorf("%s: ac %.1f inbound %.1f diff %.1f", ac.Callsign,
			acFixHeading, inboundHeading,
			headingDifference(acFixHeading, inboundHeading))

		if headingDifference(acFixHeading, inboundHeading) < 90 {
			return false
		}
	}

	lnav, vnav := MakeFlyProcedureTurn(ac, ac.Waypoints)
	if lnav != nil {
		ac.Nav.L = lnav
	}
	if vnav != nil {
		ac.Nav.V = vnav
	}
	return true
}

func (ac *Aircraft) ExpectApproach(ap *Approach) (string, error) {
	ac.Approach = ap

	if wp, ok := ac.ArrivalRunwayWaypoints[ap.Runway]; ok && len(wp) > 0 {
		// Splice the runway-specific waypoints in with the aircraft's
		// current waypoints...
		idx := FindIf(ac.Waypoints, func(w Waypoint) bool {
			return w.Fix == wp[0].Fix
		})
		if idx == -1 {
			lg.Errorf("%s: Aircraft waypoints %s don't match up with arrival runway waypoints %s",
				ac.Callsign, spew.Sdump(ac.Waypoints), spew.Sdump(wp))
			// Assume that it has (hopefully recently) passed the last fix
			// and that patching in the rest will work out..
			ac.Waypoints = DuplicateSlice(wp[1:])
		} else {
			ac.Waypoints = ac.Waypoints[:idx]
			ac.Waypoints = append(ac.Waypoints, wp...)
		}
	}

	return "we'll expect the " + ap.FullName + " approach", nil
}

func (ac *Aircraft) ClearedApproach(ap *Approach) (response string, err error) {
	return ac.clearedApproach(ap, false)
}

func (ac *Aircraft) ClearedStraightInApproach(ap *Approach) (response string, err error) {
	return ac.clearedApproach(ap, true)
}

func (ac *Aircraft) clearedApproach(ap *Approach, straightIn bool) (response string, err error) {
	if ac.Approach == nil {
		// allow it anyway...
		response = "you never told us to expect an approach, but ok, cleared " + ap.FullName
		ac.Approach = ap
	}
	if ac.Approach.FullName != ap.FullName {
		response = "but you cleared us for the " + ac.Approach.FullName + " approach..."
		err = ErrClearedForUnexpectedApproach
		return
	}
	if ac.ApproachCleared && ac.NoPT == straightIn {
		response = "you already cleared us for the " + ap.FullName + " approach..."
		return
	}

	directApproachFix := false
	var remainingApproachWaypoints []Waypoint
	if _, ok := ac.Nav.L.(*FlyRoute); ok && len(ac.Waypoints) > 0 {
		// Is the aircraft cleared direct to a waypoint on the approach?
		for _, approach := range ap.Waypoints {
			for i, wp := range approach {
				if wp.Fix == ac.Waypoints[0].Fix {
					directApproachFix = true
					if i+1 < len(approach) {
						remainingApproachWaypoints = approach[i+1:]
					}
					break
				}
			}
		}
	}

	if ac.Approach.Type == ILSApproach {
		if directApproachFix {
			if remainingApproachWaypoints != nil {
				ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
			}
		} else if _, ok := ac.Nav.L.(*FlyHeading); ok {
			ac.AddFutureNavCommand(&TurnToInterceptLocalizer{})
		} else {
			response = "we need either direct or a heading to intercept"
			return
		}
		// If the aircraft is on a heading, there's nothing more to do for
		// now; keep flying the heading and after we intercept we'll add
		// the rest of the waypoints to the aircraft's waypoints array.
	} else {
		// RNAV
		if !directApproachFix {
			response = "we need direct to a fix on the approach..."
			return
		}

		if remainingApproachWaypoints != nil {
			ac.Waypoints = append(ac.Waypoints, remainingApproachWaypoints...)
		}

		// LNav is already known to be FlyRoute; leave SNav and VNav as is
		// until the aircraft reaches the next fix; at that point those
		// will respectively switch to FinalApproachSpeed and FlyRoute.
	}

	ac.NoPT = straightIn
	if _, ok := ac.Nav.L.(*FlyHeading); ok {
		// No procedure turn if it intercepts via a heading
		ac.NoPT = true
	}

	// Cleared approach also cancels speed restrictions, but let's not do
	// that.
	ac.ApproachCleared = true
	ac.AddFutureNavCommand(&ApproachSpeedAt5DME{})

	ac.flyProcedureTurnIfNecessary()

	if straightIn {
		response += "cleared straight in " + ap.FullName + " approach"
	} else {
		response += "cleared " + ap.FullName + " approach"
	}
	return
}

func (ac *Aircraft) CancelApproachClearance() {
	ac.ApproachCleared = false

	for cmd := range ac.Nav.FutureCommands {
		switch reflect.TypeOf(cmd) {
		case reflect.TypeOf(&HoldLocalizerAfterIntercept{}):
			delete(ac.Nav.FutureCommands, cmd)
		case reflect.TypeOf(&TurnToInterceptLocalizer{}):
			delete(ac.Nav.FutureCommands, cmd)
		case reflect.TypeOf(&ApproachSpeedAt5DME{}):
			delete(ac.Nav.FutureCommands, cmd)
		}
	}
}

func (ac *Aircraft) updateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance

	targetSpeed, targetRate := ac.Nav.S.GetSpeed(ac)

	// Stay within the aircraft's capabilities
	targetSpeed = clamp(targetSpeed, perf.Speed.Min, perf.Speed.Max)

	if ac.IAS < targetSpeed {
		accel := ac.Performance.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		accel = min(accel, targetRate/60)
		ac.IAS = min(targetSpeed, ac.IAS+accel)
	} else if ac.IAS > targetSpeed {
		decel := ac.Performance.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		decel = min(decel, targetRate/60)
		ac.IAS = max(targetSpeed, ac.IAS-decel)
	}
}

func (ac *Aircraft) updateAltitude() {
	targetAltitude, targetRate := ac.Nav.V.GetAltitude(ac)

	if abs(targetAltitude-ac.Altitude) < 3 {
		ac.Altitude = targetAltitude
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := ac.Performance.Rate.Climb, ac.Performance.Rate.Descent

	// For high performing aircraft, reduce climb rate after 5,000'
	if climb >= 2500 && ac.Altitude > 5000 {
		climb -= 500
	}
	if ac.Altitude < 10000 {
		// Have a slower baseline rate of descent on approach
		descent = min(descent, 2000)
		// And reduce it based on airspeed as well
		descent *= min(ac.IAS/250, 1)
	}
	climb = min(climb, targetRate)
	descent = min(descent, targetRate)

	if ac.Altitude < targetAltitude {
		// Simple model: we just update altitude based on the rated climb
		// rate; does not account for simultaneous acceleration, etc...
		ac.Altitude = min(targetAltitude, ac.Altitude+climb/60)
	} else if ac.Altitude > targetAltitude {
		// Similarly, descent modeling doesn't account for airspeed or
		// acceleration/deceleration...
		ac.Altitude = max(targetAltitude, ac.Altitude-descent/60)
	}
}

func (ac *Aircraft) updateHeading() {
	targetHeading, turnDirection, turnRate := ac.Nav.L.GetHeading(ac)

	if abs(ac.Heading-targetHeading) < 1 {
		ac.Heading = targetHeading
		return
	}

	var turn float32
	switch turnDirection {
	case TurnLeft:
		angle := ac.Heading - targetHeading
		if angle < 0 {
			angle += 360
		}
		angle = min(angle, turnRate)
		turn = -angle

	case TurnRight:
		angle := targetHeading - ac.Heading
		if angle < 0 {
			angle += 360
		}
		angle = min(angle, turnRate)
		turn = angle

	case TurnClosest:
		// Figure out which way is closest: first find the angle to rotate
		// the target heading by so that it's aligned with 180
		// degrees. This lets us not worry about the complexities of the
		// wrap around at 0/360..
		rot := 180 - targetHeading
		if rot < 0 {
			rot += 360
		}
		cur := mod(ac.Heading+rot, 360) // w.r.t. 180 target
		turn = clamp(180-cur, -turnRate, turnRate)
	}

	// Finally, do the turn.
	ac.Heading += turn
	if ac.Heading < 0 {
		ac.Heading += 360
	} else if ac.Heading > 360 {
		ac.Heading -= 360
	}
}

func (ac *Aircraft) updatePositionAndGS() {
	// Update position given current heading
	prev := ac.Position
	hdg := ac.Heading - scenarioGroup.MagneticVariation
	v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}

	// Compute ground speed: TAS, modified for wind.
	GS := ac.TAS() / 3600
	airborne := ac.IAS >= 1.1*ac.Performance.Speed.Min
	if airborne {
		windVector := sim.GetWindVector(ac.Position, ac.Altitude)
		delta := windVector[0]*v[0] + windVector[1]*v[1]
		GS += delta
	}

	// Finally update position and groundspeed.
	newPos := add2f(ll2nm(ac.Position), scale2f(v, GS))
	ac.Position = nm2ll(newPos)
	ac.GS = distance2f(ll2nm(prev), newPos) * 3600
}

func (ac *Aircraft) updateWaypoints() {
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
		hdg = headingp2ll(wp.Location, ac.Waypoints[1].Location, scenarioGroup.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = ac.Heading
	}

	if ac.ShouldTurnForOutbound(wp.Location, hdg, TurnClosest) {
		// Execute any commands associated with the waypoint
		ac.RunWaypointCommands(wp)

		if ac.ApproachCleared {
			// The aircraft has made it to the approach fix they
			// were cleared to.
			ac.Nav.V = &FlyRoute{}

			// If no speed was assigned, go ahead and start slowing down.
			if !ac.HaveAssignedSpeed() {
				ac.Nav.S = &FinalApproachSpeed{}
			}
		}

		if ac.Waypoints[0].Heading != 0 {
			// We have an outbound heading
			ac.Nav.L = &FlyHeading{Heading: float32(wp.Heading)}
			ac.Waypoints = ac.Waypoints[1:]
		} else {
			ac.Waypoints = ac.Waypoints[1:]
			ac.flyProcedureTurnIfNecessary()
		}

		//lg.Printf("%s", spew.Sdump(ac))
	}
}

func (ac *Aircraft) RunWaypointCommands(wp Waypoint) {
	if wp.Handoff {
		ac.InboundHandoffController = sim.Callsign()
		globalConfig.Audio.PlaySound(AudioEventInboundHandoff)
	}
	if wp.Delete {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	if wp.NoPT {
		ac.NoPT = true
	}
}

func (ac *Aircraft) ShouldTurnForOutbound(p Point2LL, hdg float32, turn TurnMethod) bool {
	dist := nmdistance2ll(ac.Position, p)
	eta := dist / ac.GS * 3600 // in seconds

	// TODO: it's not clear that the logic here works sense for turns of
	// >180 degrees...
	var turnAngle float32
	switch turn {
	case TurnLeft:
		turnAngle = ac.Heading - hdg

	case TurnRight:
		turnAngle = hdg - ac.Heading

	case TurnClosest:
		turnAngle = abs(headingDifference(ac.Heading, hdg))
	}

	if turnAngle < 0 {
		turnAngle += 360
	}

	// lg.Printf("dist %.2f eta %.1f angle %.1f", dist, eta, turnAngle)

	// Assuming 3 degree/second turns, we might start to turn to the
	// heading leaving the waypoint when turnAngle/3==eta, though we'd turn
	// too early then since turning starts to put us in the new direction
	// away from the fix.  An ad-hoc angle/5 generally seems to work well
	// instead. Also checking against 2 seconds ensures that we don't miss
	// fixes where there's little to no turn...
	return eta < max(2, turnAngle/3/2)
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept(ish) the radial.
func (ac *Aircraft) ShouldTurnToIntercept(p0 Point2LL, hdg float32) bool {
	p0 = ll2nm(p0)
	p1 := add2f(p0, [2]float32{sin(radians(hdg - scenarioGroup.MagneticVariation)),
		cos(radians(hdg - scenarioGroup.MagneticVariation))})

	ap0 := ll2nm(ac.Position)
	acHdg := ac.Heading - scenarioGroup.MagneticVariation
	acHeadingVector := [2]float32{sin(radians(acHdg)), cos(radians(acHdg))}
	ap1 := add2f(ap0, acHeadingVector)

	// Find the intersection of aircraft's path with the line
	isect, ok := LineLineIntersect(p0, p1, ap0, ap1)
	if !ok {
		lg.Errorf("no intersect!")
		return false // better luck next time...
	}

	// Is the intersection behind the aircraft? (This can happen if it
	// has flown through the localizer.) Ignore it if so.
	v := sub2f(isect, ap0)

	if v[0]*acHeadingVector[0]+v[1]*acHeadingVector[1] < 0 {
		lg.Errorf("%s: localizer intersection is behind us...", ac.Callsign)
		return false
	}

	return ac.ShouldTurnForOutbound(nm2ll(isect), hdg, TurnClosest)
}
