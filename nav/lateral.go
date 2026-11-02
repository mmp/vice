// nav/lateral.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"cmp"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) headingForTrack(hdg math.MagneticHeading, wxs wx.Sample) math.MagneticHeading {
	if !nav.IsAirborne() {
		// On the ground the aircraft neither crabs nor drifts with the wind,
		// so the track is flown as a heading; departures follow tracks from
		// the start of the takeoff roll.
		return hdg
	}

	// Convert magnetic track to true, then adjust for wind and convert back.
	trueHdg := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	v := math.SinCos(math.Radians(trueHdg))
	v = math.Scale2f(v, nav.FlightState.GS)
	return math.TrueToMagnetic(math.OffsetHeading(trueHdg, -wxs.Deflection(v)), nav.FlightState.MagneticVariation)
}

// updateHeading turns the aircraft for one second toward the heading its
// instructions call for: a controller's instruction that is due takes
// effect, guidance picks the heading, and the aircraft rolls and turns
// toward it.
func (nav *Nav) updateHeading(callsign string, wxs wx.Sample, simTime Time) {
	nav.activateDeferredHeading(simTime)

	fs := &nav.FlightState
	st := nav.targetHeading(callsign, wxs, simTime)
	rate := float32(StandardTurnRate)
	if st.flat {
		fs.BankAngle = 0
	} else {
		st.turn, rate = fs.roll(nav.Perf, st.heading, st.turn, nav.TAS(wxs.Temperature()))
	}

	NavLog(callsign, simTime, NavLogHeading, "target=%.0f current=%.0f diff=%.1f turn=%v rate=%.1f bank=%.1f",
		st.heading, fs.Heading, math.HeadingDifference(fs.Heading, st.heading), st.turn, rate, fs.BankAngle)

	fs.turn(st.heading, st.turn, rate)
}

// DepartOnCourse sends a departure direct to its exit fix and climbs it to
// the given cruise altitude—unless the route has /c or /d altitude actions
// at its waypoints, in which case those govern its altitude instead.
func (nav *Nav) DepartOnCourse(alt float32, exit string, simTime Time) {
	defer nav.record("DepartOnCourse", alt, exit, simTime)()
	if _, ok := nav.AssignedHeading(); !ok {
		// Don't do anything if they are not on a heading; let them fly the
		// regular route and don't (potentially) skip waypoints and go
		// straight to the exit; however, the altitude should be changed
		if !nav.RouteAltitudeActions {
			nav.climbToCruise(alt)
		}
		nav.Speed = Speed{}
		return
	}

	// Go ahead and put any deferred route changes into effect immediately.
	nav.Waypoints = nav.AssignedWaypoints()
	nav.DeferredNavHeading = nil

	// Make sure we are going direct to the exit.
	if idx := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == exit }); idx != -1 {
		nav.Waypoints = nav.Waypoints[idx:]
	}
	// The aircraft goes to the exit from wherever it is; it isn't on a leg
	// of the route that it could be turned back to.
	nav.PassedWaypoint = av.Waypoint{}
	if !nav.RouteAltitudeActions {
		nav.climbToCruise(alt)
	}
	nav.Speed = Speed{}
	nav.EnqueueOnCourse(simTime, 0)
}

// climbToCruise sends the aircraft up to the given cruise altitude. A
// controller-issued altitude cancels the published altitude restrictions along
// the route, so if the aircraft has one (even one that is still waiting on a
// speed change), cruise is assigned the same way. Otherwise the aircraft is
// still climbing to a clearance limit and any remaining restrictions continue
// to apply.
func (nav *Nav) climbToCruise(alt float32) {
	if nav.Altitude.Assigned != nil || nav.Altitude.AfterSpeed != nil {
		nav.setAssignedAltitude(alt)
	} else {
		nav.Altitude = Altitude{Cleared: &ClearedAltitude{Altitude: alt}}
	}
}

// activateDeferredHeading puts a controller's heading, direct, or hold into
// effect once the pilot's reaction time has passed.
func (nav *Nav) activateDeferredHeading(simTime Time) {
	dh := nav.DeferredNavHeading
	if dh == nil || !simTime.After(dh.Time) {
		return
	}

	// These may all be nil; whichever the instruction set takes effect now.
	nav.Heading = Heading{Assigned: dh.Heading, Turn: dh.Turn, Hold: dh.Hold, Maneuvers: dh.Maneuvers,
		Join: dh.Join}
	if len(dh.Waypoints) > 0 {
		// The new route starts from wherever the aircraft is; it isn't
		// on a leg of it.
		nav.Waypoints = dh.Waypoints
		nav.PassedWaypoint = av.Waypoint{}
	}
	// If the heading was assigned while the aircraft was descending on
	// a STAR/approach with no issued altitude, snapshot the current
	// altitude now (rather than at command-issue time) so the aircraft
	// holds where the pilot actually was when they turned. Re-check the
	// condition in case an altitude was issued during the deferred
	// window.
	if dh.SnapshotAltitudeOnEffect && !nav.hasIssuedAltitude() {
		nav.Altitude.Cleared = &ClearedAltitude{Altitude: nav.FlightState.Altitude, IsFloor: true}
	}
	nav.DeferredNavHeading = nil
}

// steering is the heading guidance asks for and which way to turn to it.
// A flat turn is flown at the standard rate without rolling into a bank,
// as maneuvers and holds are; otherwise the aircraft rolls into and out of
// the turn.
type steering struct {
	heading math.MagneticHeading
	turn    av.TurnDirection
	flat    bool
}

// targetHeading returns the heading to fly this second from whatever
// governs the aircraft laterally: an approach intercept, maneuvers, a
// hold, an assigned heading, a DME arc, or the next waypoint. It also
// advances that mode: maneuvers and holds step to their next legs, an
// intercept progresses, and a turn direction is dropped once the aircraft
// is on course.
func (nav *Nav) targetHeading(callsign string, wxs wx.Sample, simTime Time) steering {
	fs := &nav.FlightState
	st := steering{heading: fs.Heading, turn: av.TurnClosest}

	switch {
	case nav.interceptingFromHeading() && nav.Heading.Assigned != nil:
		st.heading, st.turn = nav.ApproachHeading(callsign, wxs, simTime)

	case len(nav.Heading.Maneuvers) > 0:
		result := nav.flyManeuvers(&nav.Heading.Maneuvers, wxs, simTime)
		if result.completed {
			// A heading assigned along with the maneuvers was for their
			// first leg; the aircraft now resumes its route.
			nav.Heading = Heading{}
		}
		return steering{heading: result.heading, turn: result.turn, flat: true}

	case nav.Heading.Hold != nil:
		hdg, turn := nav.Heading.Hold.GetHeading(callsign, nav, wxs, simTime)
		return steering{heading: hdg, turn: turn, flat: true}

	case nav.Heading.Assigned != nil:
		st.heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			st.turn = *nav.Heading.Turn
		}

	case nav.Heading.Arc != nil && nav.Heading.JoiningArc:
		st.heading = nav.Heading.Arc.InitialHeading
		if math.HeadingDifference(fs.Heading, st.heading) < 1 {
			nav.Heading.JoiningArc = false
		}

	default:
		// Either on an arc or to a waypoint. Figure out the point we're
		// heading to and then common code will handle wind correction,
		// etc...
		var pTarget math.Point2LL
		if arc := nav.Heading.Arc; arc != nil {
			// Work in nm coordinates
			pc := math.LL2NM(arc.Center, fs.NmPerLongitude)
			pac := math.LL2NM(fs.Position, fs.NmPerLongitude)
			v := math.Sub2f(pac, pc)
			// Heading from center to aircraft, which we assume to be more
			// or less on the arc already.
			angle := float32(math.VectorHeading(v)) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(util.Select(arc.Direction.IsClockwise(), 10, -10))
			p := math.Add2f(pc, math.Scale2f(math.SinCos(math.Radians(angle)), arc.Radius))
			pTarget = math.NM2LL(p, fs.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				return st // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
			if nav.Heading.Turn != nil {
				st.turn = *nav.Heading.Turn
			} else if t := nav.Waypoints[0].Turn(); t != av.TurnClosest {
				st.turn = t
			}
		}

		// No magnetic correction yet, just the raw geometric heading vector
		trueHdg := math.Heading2LL(fs.Position, pTarget, fs.NmPerLongitude)
		v := math.SinCos(math.Radians(trueHdg))
		v = math.Scale2f(v, fs.GS)

		if nav.IsAirborne() {
			trueHdg = math.OffsetHeading(trueHdg, -wxs.Deflection(v))
		}

		st.heading = math.TrueToMagnetic(trueHdg, fs.MagneticVariation)

		// A turn direction is for the turn onto the leg. Once the aircraft
		// is on course it corrects the closest way; otherwise a correction
		// to the other side, as for a wind shift, would be flown as a 360.
		if st.turn != av.TurnClosest && math.HeadingDifference(fs.Heading, st.heading) < 1 {
			closest := av.TurnClosest
			nav.Heading.Turn = &closest
			st.turn = closest
		}
	}
	return st
}

// updateWaypoints sequences the route: once the aircraft reaches the next
// waypoint, or the point where it starts its turn onto the leg beyond it,
// it passes the waypoint and takes up what follows.
func (nav *Nav) updateWaypoints(callsign string, wxs wx.Sample, simTime Time) UpdateResult {
	if len(nav.Waypoints) == 0 {
		return UpdateResult{}
	}

	wp := nav.Waypoints[0]
	dist := math.NMDistance2LLFast(nav.FlightState.Position, wp.Location, nav.FlightState.NmPerLongitude)
	NavLog(callsign, simTime, NavLogWaypoint, "next=%s dist=%.2fnm alt=%.0f", wp.Fix, dist, nav.FlightState.Altitude)

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	hdg, turn, track := nav.outboundCourse(wp)
	var passed bool
	if wp.FlyOver() {
		passed = nav.ETA(wp.Location) < 2
	} else {
		passed = nav.shouldTurnForOutbound(wp.Location, hdg, turn, track, wxs)
	}
	if !passed {
		return UpdateResult{}
	}

	nav.Heading.Turn = nil
	NavLog(callsign, simTime, NavLogWaypoint, "passed fix=%s hdg=%.0f->%.0f alt=%.0f", wp.Fix, nav.FlightState.Heading, hdg, nav.FlightState.Altitude)
	return nav.passWaypoint(callsign, wp, simTime)
}

// outboundCourse returns the heading the aircraft leaves wp on and which way
// it turns onto it, and whether it is a ground course or a heading that
// drifts with the wind.
func (nav *Nav) outboundCourse(wp av.Waypoint) (hdg math.MagneticHeading, turn av.TurnDirection, track bool) {
	fs := &nav.FlightState
	courseTo := func(p math.Point2LL) math.MagneticHeading {
		return math.TrueToMagnetic(math.Heading2LL(wp.Location, p, fs.NmPerLongitude), fs.MagneticVariation)
	}

	turn, track = av.TurnClosest, true
	nfa, assigned := nav.FixAssignments[wp.Fix]
	switch {
	case len(nav.Approach.AtFixClearedRoute) > 1 && nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix:
		hdg = courseTo(nav.Approach.AtFixClearedRoute[1].Location)
	case assigned && nfa.Depart.Heading != nil:
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
		track = false
		if nfa.Depart.Turn != nil {
			turn = *nfa.Depart.Turn
		}
	case assigned && nfa.Depart.Fix != nil:
		// depart fix direct
		hdg = courseTo(nfa.Depart.Fix.Location)
		if nfa.Depart.Turn != nil {
			turn = *nfa.Depart.Turn
		}
	default:
		if h, ok := wp.HeadingAction(); ok {
			// Leaving the next fix on the heading of its first action group.
			if h.PresentHeading {
				hdg = fs.Heading
			} else {
				hdg = math.MagneticHeading(h.Heading)
			}
			turn = h.Turn
			track = h.Track
		} else if wp.Arc() != nil {
			// Joining a DME arc after the heading
			hdg = wp.Arc().InitialHeading
		} else if len(nav.Waypoints) > 1 {
			// Otherwise, find the heading to the following fix.
			hdg = courseTo(nav.Waypoints[1].Location)
			turn = nav.Waypoints[1].Turn()
		} else {
			// No more waypoints (likely about to land), so just
			// plan to stay on the current heading.
			hdg = fs.Heading
		}
	}
	return
}

// passWaypoint takes up what follows wp, which the aircraft has just
// reached: an approach clearance or intercept that was to take effect
// there, the restrictions it carries past the fix, the controller's
// assignments at the fix, the heading or leg to fly next, and a procedure
// turn or hold coming up.
func (nav *Nav) passWaypoint(callsign string, wp av.Waypoint, simTime Time) UpdateResult {
	clearedAtFix := nav.clearApproachAtFix(wp)
	interceptedAtFix := nav.interceptApproachAtFix(wp)
	if nav.Heading.Arc != nil {
		nav.Heading = Heading{}
	}
	nav.passApproachFix(wp)
	nav.carryRestrictions(wp)

	var actionEvent *av.WaypointActionEvent
	if groups := wp.ActionGroups(); len(groups) > 0 {
		actionEvent = waypointActionEvent(wp, groups[0].Actions)
	}

	nav.applyFixAssignments(wp)
	nav.departFix(wp, clearedAtFix, interceptedAtFix)

	if wp.NoPT() {
		nav.Approach.NoPT = true
	}
	if wp.AirworkMinutes() > 0 {
		nav.Airwork = StartAirwork(wp)
	}

	nav.dropWaypoint(wp)
	if nav.Heading.Assigned == nil && len(nav.Heading.Maneuvers) == 0 {
		nav.flyProcedureTurnIfNecessary()
	}
	nav.holdAtNextFix(callsign)

	// Log the updated route after passing the waypoint
	LogRoute(callsign, simTime, nav.Waypoints)

	result := UpdateResult{PassedWaypoint: &wp}
	if actionEvent != nil {
		result.ActionEvents = append(result.ActionEvents, *actionEvent)
	}
	return result
}

// carryRestrictions keeps wp's restrictions in force past it: an aircraft
// that couldn't meet the altitude keeps trying, and a published speed is held
// until something replaces it. The controller's crossing altitude for the
// fix, if given, replaces the charted one after the fix as well as before.
func (nav *Nav) carryRestrictions(wp av.Waypoint) {
	if ar := cmp.Or(nav.controllerAltitudeRestriction(&wp), nav.chartedAltitudeRestriction(&wp)); ar != nil &&
		(!nav.Approach.Cleared || ar.Range[0] < nav.FlightState.Altitude) {
		// Don't climb if we're cleared approach and below the next
		// fix's altitude. Copy the value since the pointer into the
		// slice element could become stale if the slice is reallocated.
		arCopy := *ar
		nav.Altitude.Restriction = &arCopy
	}
	if sr := nav.chartedSpeedRestriction(&wp); sr != nil && !wp.OnSID() {
		// Carry on the speed restriction unless it's a SID
		srCopy := *sr
		nav.Speed.Restriction = &srCopy
	}
}

// applyFixAssignments puts into effect what the controller said to do after
// wp: a speed to fly or to stop flying, a crossing speed held past the fix,
// and an altitude to climb or descend to.
func (nav *Nav) applyFixAssignments(wp av.Waypoint) {
	nfa, ok := nav.FixAssignments[wp.Fix]
	if !ok {
		return
	}

	if nfa.Depart.Speed != nil {
		sr := *nfa.Depart.Speed
		nav.Speed = Speed{Assigned: &sr}
	} else if nfa.Depart.CancelSpeed {
		nav.Speed = Speed{}
	} else if nfa.Arrive.Speed != nil && !wp.OnSID() && !nav.hasIssuedSpeed() {
		// The controller's crossing speed is held past the fix as an
		// assigned speed, which a via clearance cancels, unless a
		// later speed instruction has replaced it.
		sr := *nfa.Arrive.Speed
		nav.Speed.Assigned = &sr
	}

	if nfa.Depart.Altitude != nil {
		nav.AssignAltitudeNow(*nfa.Depart.Altitude, false)
	}
}

// departFix sets how the aircraft leaves wp: on the controller's heading or
// direct to the controller's fix, on the heading or maneuvers of the
// waypoint's action groups, around its DME arc, or else along the route. A
// clearance or intercept that took effect at the fix has already set the
// course, so the action groups' heading is left alone then.
func (nav *Nav) departFix(wp av.Waypoint, clearedAtFix, interceptedAtFix bool) {
	var next *av.Waypoint
	if len(nav.Waypoints) > 1 {
		next = &nav.Waypoints[1]
	}

	nfa, assigned := nav.FixAssignments[wp.Fix]
	switch {
	case assigned && nfa.Depart.Heading != nil:
		hdg := *nfa.Depart.Heading
		nav.Heading = Heading{Assigned: &hdg}
	case assigned && nfa.Depart.Fix != nil:
		if wps, _, err := nav.directFixWaypoints(nfa.Depart.Fix.Fix); err == nil {
			// dropWaypoint takes the first waypoint off the route, so put
			// wp back at its front so that it comes off instead.
			nav.Waypoints = append([]av.Waypoint{wp}, wps...)
			nav.Heading.Turn = nfa.Depart.Turn // may be nil (TurnClosest)
		}
	default:
		if h := nav.actionGroupHeading(wp, next); h != nil && !clearedAtFix && !interceptedAtFix {
			nav.Heading = *h
		} else if wp.Arc() != nil && !interceptedAtFix {
			// Fly the DME arc
			nav.Heading = Heading{Arc: wp.Arc(), JoiningArc: true}
		}
	}
}

// dropWaypoint takes wp off the front of the route, remembering it as the
// start of the leg now being flown. The destination airport stays in the
// route; an aircraft passing it holds its heading.
func (nav *Nav) dropWaypoint(wp av.Waypoint) {
	if len(nav.Waypoints) == 1 {
		hdg := nav.FlightState.Heading
		nav.Heading = Heading{Assigned: &hdg}
		return
	}
	if !wp.SyntheticCrossing() {
		nav.PassedWaypoint = wp
	}
	nav.Waypoints = nav.Waypoints[1:]
}

// holdAtNextFix starts the hold the controller assigned at the next
// waypoint, if there is one.
func (nav *Nav) holdAtNextFix(callsign string) {
	if len(nav.Waypoints) == 0 {
		return
	}
	if nfa, ok := nav.FixAssignments[nav.Waypoints[0].Fix]; ok && nfa.Hold != nil {
		nav.Heading = Heading{Hold: nav.makeFlyHold(callsign, *nfa.Hold)}
	}
}

// actionGroupHeading returns how to fly a waypoint's action groups after
// passing it, or nil if they give no heading to fly. av.ActionGroupHeading
// decides which of the three it is, so that scenario validation can tell an
// assigned heading from a maneuver sequence the same way; an assigned heading
// is flown as a controller's would be, so the rest of nav treats it as one.
func (nav *Nav) actionGroupHeading(wp av.Waypoint, next *av.Waypoint) *Heading {
	h, kind := av.ActionGroupHeading(wp.ActionGroups())
	switch kind {
	case av.ActionGroupHeadingNone:
		return nil
	case av.ActionGroupHeadingAssigned:
		if h.PresentHeading {
			// Round to nearest 5 degrees
			hdg := math.MagneticHeading(5 * int((float32(nav.FlightState.Heading)+2.5)/5))
			hdg = math.NormalizeHeading(hdg)
			return &Heading{Assigned: &hdg}
		}
		hdg, turn := math.MagneticHeading(h.Heading), h.Turn
		return &Heading{Assigned: &hdg, Turn: &turn}
	}
	return &Heading{Maneuvers: nav.makeActionGroupManeuvers(wp, next)}
}

// courseTowardFix returns the course along the line through the aircraft in
// the given direction that leads to the fix: its reciprocal if the fix is
// behind, and the course itself otherwise.
func (fs *FlightState) courseTowardFix(course math.MagneticHeading, fix math.Point2LL) math.MagneticHeading {
	toFix := math.Sub2f(math.LL2NM(fix, fs.NmPerLongitude), math.LL2NM(fs.Position, fs.NmPerLongitude))
	if math.Dot(toFix, math.HeadingVector(math.MagneticToTrue(course, fs.MagneticVariation))) < 0 {
		return math.OppositeHeading(course)
	}
	return course
}

// makeActionGroupManeuvers translates a waypoint's action groups into the
// maneuvers that fly them. next is the following fix on the route, which an
// /@crs course termination joins; it is nil if there is none.
func (nav *Nav) makeActionGroupManeuvers(wp av.Waypoint, next *av.Waypoint) []LateralManeuver {
	groups := wp.ActionGroups()
	maneuvers := make([]LateralManeuver, 0, len(groups))
	for _, group := range groups {
		m := LateralManeuver{
			Waypoint: wp,
			Actions:  group.Actions,
		}
		heading := group.Actions.Heading
		if !heading.IsSet() {
			m.Heading = nav.FlightState.Heading
		} else {
			m.Turn = heading.Turn
			if heading.PresentHeading {
				m.Heading = nav.FlightState.Heading
			} else if heading.Track {
				m.Track = math.MagneticHeading(heading.Heading)
				m.TrackFrom, m.TrackFromFix, m.TrackVariation = heading.FixLocation, heading.Fix, heading.FixVariation
			} else {
				m.Heading = math.MagneticHeading(heading.Heading)
			}
		}

		switch group.Until.Type {
		case av.WaypointActionNoTermination:
			if heading.IsSet() {
				m.Until = ManeuverComplete{Type: UntilControllerIntervention}
			} else {
				// The group only carries actions for the sim; they fire when
				// the group is reached and the aircraft continues on its
				// route. Holding the present heading instead takes an
				// explicit /ph.
				m.Until = ManeuverComplete{Type: UntilImmediate}
			}
		case av.WaypointActionAltitude:
			m.Until = ManeuverComplete{
				Type:      UntilAltitude,
				Altitude:  group.Until.Altitude,
				AtOrAbove: group.Until.AtOrAbove,
			}
		case av.WaypointActionDME:
			m.Until = ManeuverComplete{
				Type:            UntilDME,
				DMEDistance:     group.Until.DMEDistance,
				DMEFix:          group.Until.DMEFixLocation,
				DMEFixElevation: group.Until.DMEFixElevation,
				AtOrAbove:       group.Until.AtOrAbove,
			}
		case av.WaypointActionCourse:
			if next == nil {
				// parseWaypoints rejects /@crs on the last waypoint, but the
				// route may since have been truncated; hold the heading
				// rather than intercepting a course through 0°N 0°E.
				m.Until = ManeuverComplete{Type: UntilControllerIntervention}
			} else {
				// A course given as a VOR's radial is referenced to the
				// station's declination and names only the line, since a
				// leg may fly a radial either inbound or outbound; the
				// intercept is flown in the sim's magnetic frame, in
				// whichever direction along the line leads to the fix.
				course := math.MagneticHeading(group.Until.Course)
				if group.Until.CourseFix != "" {
					course = math.TrueToMagnetic(math.MagneticToTrue(course, group.Until.CourseFixVariation),
						nav.FlightState.MagneticVariation)
					course = nav.FlightState.courseTowardFix(course, next.Location)
				}
				// The turn onto the course is always the short way around,
				// regardless of which way the aircraft turned to take up
				// the group's heading.
				m.Until = ManeuverComplete{
					Type:            UntilIntercept,
					Fix:             next.Location,
					InterceptCourse: course,
					InterceptTurn:   av.TurnClosest,
					InterceptFix:    next.Fix,
				}
			}
		case av.WaypointActionRadial:
			m.Until = ManeuverComplete{
				Type:            UntilRadial,
				Fix:             group.Until.RadialFixLocation,
				Radial:          math.MagneticHeading(group.Until.Radial),
				RadialVariation: group.Until.RadialFixVariation,
				RadialFix:       group.Until.RadialFix,
			}
		case av.WaypointActionDistance:
			m.Until = ManeuverComplete{Type: UntilDist, Dist: group.Until.Distance}
		default:
			panic("unhandled WaypointActionTerminationType")
		}
		maneuvers = append(maneuvers, m)
	}
	return maneuvers
}

// waypointActionEvent returns the event the sim acts on for a group's
// actions, if it has any beyond its heading.
func waypointActionEvent(wp av.Waypoint, actions av.WaypointActions) *av.WaypointActionEvent {
	if actions.HasSimActions() {
		return &av.WaypointActionEvent{Waypoint: wp, Actions: actions}
	}
	return nil
}

// turnPath is a closed-form model of an upcoming turn: an optional straight
// lead, a constant-rate circular arc through the full turn angle, and a
// straight tail, all displaced linearly by the wind. The leads stand in for
// the time spent rolling into and out of the bank, sized so that the model's
// duration and endpoint match the tick-by-tick roll that FlightState.roll flies.
type turnPath struct {
	start   [2]float32 // position at the start of the turn, nm coordinates
	center  [2]float32 // center of the arc, nm coordinates
	h0      float32    // initial true heading, degrees
	s       float32    // +1 for a right turn, -1 for left
	omega   float32    // turn rate on the arc, deg/s
	radius  float32    // arc radius, nm
	arcDeg  float32    // total heading change, degrees
	t1, t2  float32    // start and end times of the arc, seconds
	dur     float32    // total duration including the roll-out lead, seconds
	tasNMps float32    // true airspeed, nm/s
	wind    [2]float32 // wind velocity, nm/s
}

// resolveTurnDirection pins down which way the turn from the aircraft's
// current heading to hdg goes. The predicates resolve this from the raw
// course before crabbing the target into the wind: the crab angle can push
// a target near 180 degrees away past it, which would flip a "closest"
// turn to the wrong side.
func (nav *Nav) resolveTurnDirection(hdg math.MagneticHeading, turn av.TurnDirection) av.TurnDirection {
	if isTurnRight(float32(nav.FlightState.Heading), float32(hdg), turn) {
		return av.TurnRight
	}
	return av.TurnLeft
}

// predictTurnPath models the turn from the aircraft's current state to the
// given magnetic heading. The turn is flown in still air at the current TAS
// with the wind's displacement added on afterward, which matches how
// FlightState.advance integrates position.
func (nav *Nav) predictTurnPath(hdg math.MagneticHeading, turn av.TurnDirection, wxs wx.Sample) turnPath {
	fs := &nav.FlightState
	tasKts := nav.TAS(wxs.Temperature())

	// Steady-state turn rate in deg/s at the given bank angle, signed with
	// the bank, where positive bank is into the turn.
	rate := func(bank float32) float32 { return turnRateAtBank(bank, tasKts) }

	arcDeg := TurnAngle(fs.Heading, hdg, turn)
	s := float32(util.Select(isTurnRight(float32(fs.Heading), float32(hdg), turn), 1, -1))
	maxBank := nav.Perf.Turn.MaxBankAngle
	rollRate := nav.Perf.Turn.MaxBankRate

	entryBank := s * fs.BankAngle
	if len(nav.Heading.Maneuvers) > 0 || nav.Heading.Hold != nil {
		// Maneuvers and holds turn at a flat StandardTurnRate without
		// maintaining FlightState.BankAngle, so don't trust its stale value.
		entryBank = 0
	}

	// Step the bank from its entry value up to max bank and from max bank
	// back to level the same way FlightState.roll does each tick, totalling
	// the heading turned through and the ticks each phase takes.
	var hIn, hOut float32
	var kIn, kOut int
	for b := entryBank; b < maxBank; kIn++ {
		b = min(b+rollRate, maxBank)
		hIn += rate(b)
	}
	for b := maxBank; b > 0; b -= rollRate {
		hOut += rate(b)
		kOut++
	}

	tp := turnPath{
		start:   math.LL2NM(fs.Position, fs.NmPerLongitude),
		h0:      float32(math.MagneticToTrue(fs.Heading, fs.MagneticVariation)),
		s:       s,
		arcDeg:  arcDeg,
		tasNMps: tasKts / 3600,
	}
	tp.wind = nav.groundWind(wxs)

	if hIn+hOut <= arcDeg {
		// The full bank is reached: the arc turns at the max-bank rate, and
		// the straight leads make up the difference between the ticks the
		// roll phases take and the heading they turn through.
		tp.omega = rate(maxBank)
		tp.t1 = float32(kIn) - hIn/tp.omega
		tp.t2 = tp.t1 + arcDeg/tp.omega
		tp.dur = tp.t2 + float32(kOut) - hOut/tp.omega
	} else {
		// A short turn that never reaches max bank: walk the bank up until
		// rolling back to level would complete the turn, then model the
		// whole turn as a single uniform arc over those ticks.
		hTurn, k := float32(0), 0
		for b := entryBank; ; {
			var hDown float32
			kDown := 0
			for bb := b; bb > 0; bb -= rollRate {
				hDown += rate(bb)
				kDown++
			}
			if hTurn+hDown >= arcDeg || b >= maxBank {
				k += kDown
				break
			}
			b = min(b+rollRate, maxBank)
			hTurn += rate(b)
			k++
		}
		tp.omega = arcDeg / float32(max(k, 1))
		tp.t1 = 0
		tp.t2 = float32(max(k, 1))
		tp.dur = tp.t2
	}

	// updateHeading turns first and FlightState.advance then moves a full
	// second along the new heading, so over a tick the aircraft flies the
	// heading it will have at the tick's end—a half-tick ahead of the
	// continuous arc. Start the arc half a second early to match.
	tp.t1 -= 0.5
	tp.t2 -= 0.5

	tp.radius = tp.tasNMps / math.Radians(tp.omega)
	tp.center = math.Add2f(tp.start,
		math.Add2f(math.Scale2f(math.SinCos(math.Radians(tp.h0)), tp.tasNMps*tp.t1),
			math.Scale2f(perpRight(tp.h0), tp.s*tp.radius)))
	return tp
}

// position returns the predicted position t seconds into the turn, in nm
// coordinates.
func (tp *turnPath) position(t float32) [2]float32 {
	var p [2]float32
	switch {
	case t <= tp.t1:
		p = math.Add2f(tp.start, math.Scale2f(math.SinCos(math.Radians(tp.h0)), tp.tasNMps*t))
	case t <= tp.t2:
		p = math.Sub2f(tp.center, math.Scale2f(perpRight(tp.heading(t)), tp.s*tp.radius))
	default:
		h1 := tp.h0 + tp.s*tp.arcDeg
		rollout := math.Sub2f(tp.center, math.Scale2f(perpRight(h1), tp.s*tp.radius))
		p = math.Add2f(rollout, math.Scale2f(math.SinCos(math.Radians(h1)), tp.tasNMps*(t-tp.t2)))
	}
	return math.Add2f(p, math.Scale2f(tp.wind, t))
}

// heading returns the predicted true heading t seconds into the turn, in
// degrees; it may be outside [0,360).
func (tp *turnPath) heading(t float32) float32 {
	turned := math.Clamp((t-tp.t1)*tp.omega, 0, tp.arcDeg)
	return tp.h0 + tp.s*turned
}

// perpRight returns unit vector perpendicular right (clockwise 90°) to heading.
// Uses vice convention: heading 0°=North, 90°=East, direction=[sin,cos]
func perpRight(hdg float32) [2]float32 {
	rad := math.Radians(hdg)
	return [2]float32{math.Cos(rad), -math.Sin(rad)}
}

// isTurnRight determines if the turn from currentHdg to targetHdg should
// be a right turn, given the specified av.TurnDirection.
func isTurnRight(currentHdg, targetHdg float32, turn av.TurnDirection) bool {
	switch turn {
	case av.TurnRight:
		return true
	case av.TurnLeft:
		return false
	default: // TurnClosest
		diff := targetHdg - currentHdg
		if diff > 180 {
			diff -= 360
		} else if diff < -180 {
			diff += 360
		}
		return diff > 0
	}
}

const (
	turnToInterceptWait turnToInterceptResult = iota
	turnToInterceptTurn
	turnToInterceptCorrectableOvershoot
	turnToInterceptMajorOvershoot
)

type turnToInterceptResult int

// maxFlyByAngle is the largest course change flown as a pure fly-by. The
// fly-by tangency distance radius*tan(turnAngle/2) diverges as the course
// change approaches 180 degrees--no single turn rolls out on a reciprocal
// course--and past 100 degrees it carries the turn miles from the fix.
// Capping the angle caps the anticipation at tan(50) ~ 1.19 turn radii,
// equivalently capping how far inside the corner the path passes at
// sec(50)-1 ~ 0.56 radii.
const maxFlyByAngle = 100

// minPredictedTurn is the smallest turn, in degrees, whose start the path
// prediction sets. A smaller one leaves the aircraft within a few feet of
// the outbound course for seconds before the fix. That is about as finely
// as float32 positions are resolved, so whether the predicted path crosses
// the course comes down to rounding, which differs between CPUs.
const minPredictedTurn = 2

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial: when the turn's path, predicted in closed form, would cross it.
// track indicates the outbound is a ground course rather than a heading
// that drifts with the wind.
func (nav *Nav) shouldTurnForOutbound(p math.Point2LL, hdg math.MagneticHeading, turn av.TurnDirection, track bool, wxs wx.Sample) bool {
	eta := nav.ETA(p)

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Don't run the path prediction when the aircraft is clearly too far
	// out for the turn to begin: the fly-by tangency distance for the
	// turn, with the course change capped at maxFlyByAngle, plus ten
	// seconds of travel for rolling into and out of the bank. Below the
	// cap this only bounds when the prediction is worth running--the
	// prediction sets the turn point. Above it the prediction fires as
	// soon as the aircraft is inside the bound, so the bound itself sets
	// the turn point, keeping the turn near the fix.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	tas := nav.TAS(wxs.Temperature())
	radius := TurnRadius(nav.Perf, tas)
	anticipation := radius*math.Tan(math.Radians(min(turnAngle, maxFlyByAngle)/2)) + 10*nav.FlightState.GS/3600
	if math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude) > anticipation {
		return false
	}

	// Prespawn stops at that bound rather than paying for the prediction:
	// it runs 1800 steps per aircraft before anyone sees the scope, and the
	// bound is the same closed-form turn geometry the prediction refines --
	// above maxFlyByAngle it is already what sets the turn point.
	if nav.Prespawn {
		return true
	}

	// For a ground course, predict a turn to the heading that holds it as
	// a track; the aircraft's post-turn steering to the next fix is
	// wind-corrected the same way. A plain heading is flown as is and
	// drifts with the wind, and so does the predicted path.
	predHdg := hdg
	if track {
		predHdg = nav.headingForTrack(hdg, wxs)
	}
	if TurnAngle(nav.FlightState.Heading, predHdg, turn) < minPredictedTurn {
		// The ETA check above passes the fix.
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	hdgTrue := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0, math.SinCos(math.Radians(hdgTrue)))

	tp := nav.predictTurnPath(predHdg, nav.resolveTurnDirection(hdg, turn), wxs)

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position,
		nav.FlightState.NmPerLongitude), p0, p1)

	// Start the turn once the predicted path would reach the far side of
	// the outbound course.
	for t := float32(1); ; t++ {
		t = min(t, tp.dur)
		d := math.SignedPointLineDistance(tp.position(t), p0, p1)
		if math.Sign(d) != math.Sign(initialDist) {
			return true
		}
		if t == tp.dur {
			return false
		}
	}
}

// Given a point and a course through it, indicates when the aircraft should start the turn to join
// that course, classifying how the turn's path, predicted in closed form, would meet it. The
// returned bool is false if the aircraft is diverging from the course; the result then describes
// how far off the course the aircraft is pointed rather than how it would roll out on it. Callers
// that have already committed to joining use that to recover; ones still waiting for the intercept
// keep flying their heading.
func (nav *Nav) shouldTurnToIntercept(p0 math.Point2LL, hdg math.MagneticHeading, turn av.TurnDirection,
	wxs wx.Sample) (result turnToInterceptResult, reaches bool) {
	p0nm := math.LL2NM(p0, nav.FlightState.NmPerLongitude)
	hdgTrue := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0nm, math.SinCos(math.Radians(hdgTrue)))

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude), p0nm, p1)
	eta := math.Abs(initialDist) / nav.FlightState.GS * 3600 // in seconds
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if eta < 2 && turnAngle < 4 {
		// Just in case, start the turn; for larger turn angles, fall
		// through to see if this is correctable.
		return turnToInterceptTurn, true
	}

	// As above, don't consider starting the turn if we're far away.
	if turnAngle < eta {
		return turnToInterceptWait, true
	}

	// Allow heading tolerance to account for the crab angle needed in crosswind.
	// Base tolerance of 10 degrees plus the calculated crab angle.
	v := math.Scale2f(math.SinCos(math.Radians(hdgTrue)), nav.FlightState.GS)
	headingTolerance := 10 + math.Abs(wxs.Deflection(v))

	// The radial is a ground course, so predict a turn to the heading that
	// holds it as a track; otherwise in a crosswind the predicted path
	// rolls out and immediately drifts back off the radial.
	tp := nav.predictTurnPath(nav.headingForTrack(hdg, wxs), nav.resolveTurnDirection(hdg, turn), wxs)

	// Since the predicted path holds the radial's course as a track after
	// rolling out, its final distance from the radial is where the aircraft
	// would settle if it turned now.
	endDist := math.SignedPointLineDistance(tp.position(tp.dur), p0nm, p1)

	for t := float32(1); ; t++ {
		t = min(t, tp.dur)
		d := math.SignedPointLineDistance(tp.position(t), p0nm, p1)
		if math.Abs(d) < 0.02 || math.Sign(d) != math.Sign(initialDist) {
			// Just past the ideal turn point, the heading still to be
			// turned at the crossing rises steeply—about 15 degrees in the
			// tick after an exact tangency—so classifying by that heading
			// alone can skip right over "start the turn". A path that ends
			// up settled on the radial is a clean intercept no matter the
			// angle it first crossed at: settling within the capture
			// tolerance is a capture.
			if math.Abs(endDist) < courseCaptureTolerance {
				return turnToInterceptTurn, true
			}
			predicted := math.TrueToMagnetic(math.TrueHeading(math.NormalizeHeading(tp.heading(t))),
				nav.FlightState.MagneticVariation)
			delta := math.HeadingDifference(hdg, predicted)
			if delta < headingTolerance {
				return turnToInterceptTurn, true
			} else if delta < headingTolerance+30 {
				return turnToInterceptCorrectableOvershoot, true
			} else {
				return turnToInterceptMajorOvershoot, true
			}
		}
		if t == tp.dur {
			break
		}
	}

	// The predicted path rolls out without reaching the radial; it then
	// holds the radial's course as a track, so its distance from it no
	// longer changes. If it ends up farther from the radial than the
	// aircraft is now, the aircraft has overshot and is diverging.
	if math.Abs(endDist) > math.Abs(initialDist) {
		delta := math.HeadingDifference(hdg, nav.FlightState.Heading)
		if math.Abs(endDist) < 0.25 && delta < 30 {
			// Near enough the course and its heading to just take it up.
			return turnToInterceptTurn, true
		}
		if delta < headingTolerance+30 {
			return turnToInterceptCorrectableOvershoot, false
		}
		return turnToInterceptMajorOvershoot, false
	}
	return turnToInterceptWait, true
}

///////////////////////////////////////////////////////////////////////////

// TurnRadius returns the radius in nautical miles of a turn flown at the given
// true airspeed: a standard rate turn, or a wider one where holding that rate
// would take more bank than the type allows.
func TurnRadius(perf av.AircraftPerformance, tas float32) float32 {
	omega := min(StandardTurnRate, bankTurnRate(perf.Turn.MaxBankAngle, tas))
	return tas / 3600 / math.Radians(omega)
}

func TurnAngle(from, to math.MagneticHeading, turn av.TurnDirection) float32 {
	switch turn {
	case av.TurnLeft:
		return float32(math.NormalizeHeading(from - to))

	case av.TurnRight:
		return float32(math.NormalizeHeading(to - from))

	case av.TurnClosest:
		return math.Abs(math.HeadingDifference(from, to))

	default:
		panic("unhandled TurnDirection")
	}
}

// checkEarlyApproachRequest checks whether the aircraft is too high to
// comfortably descend to the FAF at 300 ft/nm or less.  If so, it sets
// RequestApproachClearance so the pilot asks early.
func (nav *Nav) checkEarlyApproachRequest(currentWp *av.Waypoint) {
	// Find the FAF in the remaining waypoints and compute the distance.
	var fafAlt float32
	var dist float32
	found := false
	prev := currentWp.Location
	for _, wp := range nav.Waypoints {
		d := math.NMDistance2LL(prev, wp.Location)
		dist += d
		prev = wp.Location
		if wp.FAF() {
			if ar := wp.AltitudeRestriction(); ar != nil {
				fafAlt = ar.Range[0]
			}
			found = true
			break
		}
	}
	if !found || fafAlt == 0 || dist < 0.1 {
		return
	}

	altToLose := nav.FlightState.Altitude - fafAlt
	if altToLose > 0 && altToLose/dist > 300 {
		nav.Approach.RequestApproachClearance = true
	}
}
