// nav/lateral.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) headingForTrack(hdg math.MagneticHeading, wxs wx.Sample) math.MagneticHeading {
	// Convert magnetic track to true, then adjust for wind and convert back.
	trueHdg := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	v := math.SinCos(math.Radians(trueHdg))
	v = math.Scale2f(v, nav.FlightState.GS)
	return math.TrueToMagnetic(math.OffsetHeading(trueHdg, -wxs.Deflection(v)), nav.FlightState.MagneticVariation)
}

func (nav *Nav) updateHeading(callsign string, wxs wx.Sample, simTime Time) {
	targetHeading, turnDirection, turnRate := nav.TargetHeading(callsign, wxs, simTime)

	headingDiff := math.HeadingDifference(nav.FlightState.Heading, targetHeading)
	NavLog(callsign, simTime, NavLogHeading, "target=%.0f current=%.0f diff=%.1f turn=%v rate=%.1f bank=%.1f",
		targetHeading, nav.FlightState.Heading, headingDiff, turnDirection, turnRate, nav.FlightState.BankAngle)

	if nav.FlightState.Heading == targetHeading {
		// BankAngle should be zero(ish) at this point but just to be sure.
		nav.FlightState.BankAngle = 0
		return
	}
	if headingDiff < 1 {
		nav.FlightState.Heading = targetHeading
		nav.FlightState.BankAngle = 0
		return
	}

	var turn float32
	switch turnDirection {
	case av.TurnLeft:
		angle := float32(math.NormalizeHeading(nav.FlightState.Heading - targetHeading))
		angle = min(angle, turnRate)
		turn = -angle
	case av.TurnRight:
		angle := float32(math.NormalizeHeading(targetHeading - nav.FlightState.Heading))
		angle = min(angle, turnRate)
		turn = angle
	case av.TurnClosest:
		turn = math.HeadingSignedTurn(nav.FlightState.Heading, targetHeading)
		turn = math.Clamp(turn, -turnRate, turnRate)
	}

	// Finally, do the turn.
	nav.FlightState.Heading = math.OffsetHeading(nav.FlightState.Heading, turn)
}

func (nav *Nav) updatePositionAndGS(wxs wx.Sample) {
	// Calculate offset vector based on heading and current TAS.
	hdg := math.MagneticToTrue(nav.FlightState.Heading, nav.FlightState.MagneticVariation)
	TAS := nav.TAS(wxs.Temperature()) / 3600
	flightVector := math.Scale2f(math.SinCos(math.Radians(hdg)), TAS)

	// Further offset based on the wind
	var windVector [2]float32
	if nav.IsAirborne() {
		windVector = wxs.WindVec()
	}

	// Update the aircraft's state
	p := math.Add2f(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.Add2f(flightVector, windVector))

	nav.FlightState.Position = math.NM2LL(p, nav.FlightState.NmPerLongitude)
	nav.FlightState.GS = math.Length2f(math.Add2f(flightVector, windVector)) * 3600
}

// DepartOnCourse sends a departure direct to its exit fix and climbs it to
// the given cruise altitude—unless the route has /c or /d altitude actions
// at its waypoints, in which case those govern its altitude instead.
func (nav *Nav) DepartOnCourse(alt float32, exit string, simTime Time) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Don't do anything if they are not on a heading; let them fly the
		// regular route and don't (potentially) skip waypoints and go
		// straight to the exit; however, the altitude should be changed
		if !nav.RouteAltitudeActions {
			nav.climbToCruise(alt)
		}
		nav.Speed = NavSpeed{}
		return
	}

	// Go ahead and put any deferred route changes into effect immediately.
	nav.Waypoints = nav.AssignedWaypoints()
	nav.DeferredNavHeading = nil

	// Make sure we are going direct to the exit.
	if idx := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == exit }); idx != -1 {
		nav.Waypoints = nav.Waypoints[idx:]
	}
	if !nav.RouteAltitudeActions {
		nav.climbToCruise(alt)
	}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse(simTime)
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
		nav.Altitude = NavAltitude{Cleared: &alt}
	}
}

func (nav *Nav) Check(lg *log.Logger) {
	check := func(waypoints []av.Waypoint, what string) {
		for _, wp := range waypoints {
			if wp.Location.IsZero() {
				lg.Errorf("zero waypoint location for %s in %s", wp.Fix, what)
			}
		}
	}

	check(nav.Waypoints, "waypoints")
	if nav.Approach.Assigned != nil {
		for i, waypoints := range nav.Approach.Assigned.Waypoints {
			check(waypoints, fmt.Sprintf("approach %d waypoints", i))
		}
	}
}

func (nav *Nav) Update(callsign string, model *wx.Model, fp *av.FlightPlan, arrivalMETAR *wx.METAR, simTime Time,
	bravo *av.AirspaceGrid) UpdateResult {
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime.Time())
	return nav.UpdateWithWeather(callsign, wxs, arrivalMETAR, fp, simTime, bravo)
}

// UpdateWithWeather is a helper for simulations that use pre-fetched weather.
// arrivalMETAR, if non-nil, is used for the approach speed wind additive.
func (nav *Nav) UpdateWithWeather(callsign string, wxs wx.Sample, arrivalMETAR *wx.METAR, fp *av.FlightPlan, simTime Time, bravo *av.AirspaceGrid) UpdateResult {
	nav.PendingWaypointActionEvents = nil
	nav.activatePendingAltitude(simTime)

	// Log current state every tick
	NavLog(callsign, simTime, NavLogState, "pos=%.4f,%.4f alt=%.0f hdg=%.0f ias=%.0f gs=%.0f bank=%.1f rate=%.0f",
		nav.FlightState.Position[0], nav.FlightState.Position[1],
		nav.FlightState.Altitude, nav.FlightState.Heading,
		nav.FlightState.IAS, nav.FlightState.GS,
		nav.FlightState.BankAngle, nav.FlightState.AltitudeRate)

	targetAltitude, altitudeRate, geometricDescent := nav.TargetAltitude()
	deltaKts, slowingTo250 := nav.updateAirspeed(callsign, targetAltitude, geometricDescent, fp, wxs, arrivalMETAR, simTime, bravo)
	nav.updateAltitude(callsign, targetAltitude, altitudeRate, geometricDescent, deltaKts, slowingTo250, wxs, simTime)
	nav.updateHeading(callsign, wxs, simTime)
	nav.updatePositionAndGS(wxs)
	if nav.Airwork != nil && !nav.Airwork.Update(nav) {
		nav.Airwork = nil // Done.
	}

	result := UpdateResult{ActionEvents: nav.PendingWaypointActionEvents}
	if nav.Airwork == nil && nav.Heading.Assigned == nil &&
		nav.Heading.Hold == nil && len(nav.Heading.Maneuvers) == 0 {
		result = nav.updateWaypoints(callsign, wxs, fp, simTime)
		result.ActionEvents = append(nav.PendingWaypointActionEvents, result.ActionEvents...)
		return result
	}

	return result
}

func (nav *Nav) TargetHeading(callsign string, wxs wx.Sample, simTime Time) (heading math.MagneticHeading, turn av.TurnDirection, rate float32) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetHeading()
	}

	// Is it time to start following a heading or direct to a fix recently issued by the controller?
	if dh := nav.DeferredNavHeading; dh != nil && simTime.After(dh.Time) {
		// These may all be nil; whichever the instruction set takes effect now.
		nav.Heading = NavHeading{Assigned: dh.Heading, Turn: dh.Turn, Hold: dh.Hold, Maneuvers: dh.Maneuvers}
		if len(dh.Waypoints) > 0 {
			nav.Waypoints = dh.Waypoints
		}
		// If the heading was assigned while the aircraft was descending on
		// a STAR/approach with no assigned altitude, snapshot the current
		// altitude now (rather than at command-issue time) so the aircraft
		// holds where the pilot actually was when they turned. Re-check the
		// conditions in case an altitude was assigned during the deferred
		// window.
		if dh.SnapshotAltitudeOnEffect &&
			nav.Altitude.Assigned == nil && nav.Altitude.AfterSpeed == nil {
			alt := nav.FlightState.Altitude
			nav.Altitude.Cleared = &alt
		}
		nav.DeferredNavHeading = nil
	}

	heading, turn = nav.FlightState.Heading, av.TurnClosest

	// nav.Heading.Assigned may still be nil pending a deferred turn
	if (nav.Approach.InterceptState == InitialHeading ||
		nav.Approach.InterceptState == TurningToJoin) && nav.Heading.Assigned != nil {
		heading, turn = nav.ApproachHeading(callsign, wxs, simTime)
	} else if len(nav.Heading.Maneuvers) > 0 {
		// Maneuvers turn at a flat rate without banking, as holds do; a
		// stale bank from before would otherwise have to be unwound when
		// the maneuvers end, which predictTurnPath doesn't model.
		nav.FlightState.BankAngle = 0
		result := nav.flyManeuvers(&nav.Heading.Maneuvers, wxs, simTime)
		if result.completed {
			// A heading assigned along with the maneuvers was for their
			// first leg; the aircraft now resumes its route.
			nav.Heading = NavHeading{}
		}
		return result.heading, result.turn, result.rate
	} else if nav.Heading.Hold != nil {
		nav.FlightState.BankAngle = 0
		return nav.Heading.Hold.GetHeading(callsign, nav, wxs, simTime)
	} else if nav.Heading.Assigned != nil {
		heading = *nav.Heading.Assigned
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
	} else if arc := nav.Heading.Arc; arc != nil && nav.Heading.JoiningArc {
		heading = nav.Heading.Arc.InitialHeading
		if math.HeadingDifference(nav.FlightState.Heading, heading) < 1 {
			nav.Heading.JoiningArc = false
		}
	} else {
		// Either on an arc or to a waypoint. Figure out the point we're
		// heading to and then common code will handle wind correction,
		// etc...
		var pTarget math.Point2LL

		if arc := nav.Heading.Arc; arc != nil {
			// Work in nm coordinates
			pc := math.LL2NM(arc.Center, nav.FlightState.NmPerLongitude)
			pac := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
			v := math.Sub2f(pac, pc)
			// Heading from center to aircraft, which we assume to be more
			// or less on the arc already.
			angle := float32(math.VectorHeading(v)) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(util.Select(arc.Direction.IsClockwise(), 10, -10))
			p := math.Add2f(pc, math.Scale2f(math.SinCos(math.Radians(angle)), arc.Radius))
			pTarget = math.NM2LL(p, nav.FlightState.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				return // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
			if nav.Heading.Turn != nil {
				turn = *nav.Heading.Turn
			} else if t := nav.Waypoints[0].Turn(); t != av.TurnClosest {
				turn = t
			}
		}

		// No magnetic correction yet, just the raw geometric heading vector
		trueHdg := math.Heading2LL(nav.FlightState.Position, pTarget, nav.FlightState.NmPerLongitude)
		v := math.SinCos(math.Radians(trueHdg))
		v = math.Scale2f(v, nav.FlightState.GS)

		if nav.IsAirborne() {
			trueHdg = math.OffsetHeading(trueHdg, -wxs.Deflection(v))
		}

		heading = math.TrueToMagnetic(trueHdg, nav.FlightState.MagneticVariation)
	}

	// We have a heading and a direction; now figure out if we need to
	// adjust the bank and then how far we turn this tick.

	// signed difference, negative is turn left
	headingDelta := func() float32 {
		diff := float32(heading) - float32(nav.FlightState.Heading)
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

	// Note that turnRate is signed.
	maxBankAngle := nav.Perf.Turn.MaxBankAngle
	maxRollRate := nav.Perf.Turn.MaxBankRate
	tasMS := nav.TAS(wxs.Temperature()) * 0.514444
	turnRate := func(bankAngle float32) float32 {
		if bankAngle == 0 {
			return 0
		}
		bankRad := math.Radians(bankAngle)
		rate := math.Degrees(9.81 * math.Tan(bankRad) / tasMS)
		// The rate is signed, so clamp rather than min: min() would leave
		// left turns uncapped, letting slow aircraft turn left at 4+ deg/s
		// but right at the standard rate.
		return math.Clamp(rate, -3, 3)
	}

	// If we started leveling out now, how many more degrees would we turn through?
	var levelOutDelta float32
	if nav.FlightState.BankAngle < 0 {
		for a := nav.FlightState.BankAngle; a < 0; a += maxRollRate {
			levelOutDelta += turnRate(a)
		}
	} else {
		for a := nav.FlightState.BankAngle; a > 0; a -= maxRollRate {
			levelOutDelta += turnRate(a)
		}
	}

	//fmt.Printf("hdg %.1f for %.1f max rate %.1f cur bank %.1f levelout delta %.1f, heading delta %.1f\n",
	//nav.FlightState.Heading, heading, maxTurnRate, nav.FlightState.BankAngle, levelOutDelta, headingDelta)

	if headingDelta < 0 {
		// Turning left
		if levelOutDelta < headingDelta {
			//fmt.Printf("  leveling\n")
			nav.FlightState.BankAngle += maxRollRate
		} else if nav.FlightState.BankAngle > -maxBankAngle &&
			levelOutDelta+turnRate(nav.FlightState.BankAngle-maxRollRate) > headingDelta {
			//fmt.Printf("  increasing left bank\n")
			nav.FlightState.BankAngle -= maxRollRate
		}
	} else {
		// Turning right
		if levelOutDelta > headingDelta {
			//fmt.Printf("  leveling\n")
			nav.FlightState.BankAngle -= maxRollRate
		} else if nav.FlightState.BankAngle < maxBankAngle &&
			levelOutDelta+turnRate(nav.FlightState.BankAngle+maxRollRate) < headingDelta {
			//fmt.Printf("  increasing right bank\n")
			nav.FlightState.BankAngle += maxRollRate
		}
	}

	turn = util.Select(nav.FlightState.BankAngle < 0, av.TurnLeft, av.TurnRight)

	rate = math.Abs(turnRate(nav.FlightState.BankAngle))

	return
}

func (nav *Nav) updateWaypoints(callsign string, wxs wx.Sample, fp *av.FlightPlan, simTime Time) UpdateResult {
	if len(nav.Waypoints) == 0 {
		return UpdateResult{}
	}

	wp := &nav.Waypoints[0]
	dist := math.NMDistance2LLFast(nav.FlightState.Position, wp.Location, nav.FlightState.NmPerLongitude)
	NavLog(callsign, simTime, NavLogWaypoint, "next=%s dist=%.2fnm alt=%.0f", wp.Fix, dist, nav.FlightState.Altitude)

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading and which way to turn onto it.
	var hdg math.MagneticHeading
	turn := av.TurnClosest
	if len(nav.Approach.AtFixClearedRoute) > 1 &&
		nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix {
		hdg = math.TrueToMagnetic(math.Heading2LL(wp.Location, nav.Approach.AtFixClearedRoute[1].Location,
			nav.FlightState.NmPerLongitude), nav.FlightState.MagneticVariation)
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
		if nfa.Depart.Turn != nil {
			turn = *nfa.Depart.Turn
		}
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
		// depart fix direct
		hdg = math.TrueToMagnetic(math.Heading2LL(wp.Location, nfa.Depart.Fix.Location,
			nav.FlightState.NmPerLongitude), nav.FlightState.MagneticVariation)
		if nfa.Depart.Turn != nil {
			turn = *nfa.Depart.Turn
		}
	} else if h, ok := wp.HeadingAction(); ok {
		// Leaving the next fix on the heading of its first action group.
		if h.PresentHeading {
			hdg = nav.FlightState.Heading
		} else {
			hdg = math.MagneticHeading(h.Heading)
		}
		turn = h.Turn
	} else if wp.Arc() != nil {
		// Joining a DME arc after the heading
		hdg = wp.Arc().InitialHeading
	} else if len(nav.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = math.TrueToMagnetic(math.Heading2LL(wp.Location, nav.Waypoints[1].Location,
			nav.FlightState.NmPerLongitude), nav.FlightState.MagneticVariation)
		turn = nav.Waypoints[1].Turn()
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = nav.FlightState.Heading
	}

	passedWaypoint := false
	if wp.FlyOver() || nav.Prespawn {
		// We treat all wps as flyover during the prespawn phase; precise
		// fly-by turns don't matter before the sim starts.
		passedWaypoint = nav.ETA(wp.Location) < 2
	} else {
		passedWaypoint = nav.shouldTurnForOutbound(wp.Location, hdg, turn, wxs)
	}

	if passedWaypoint {
		nav.Heading.Turn = nil
		NavLog(callsign, simTime, NavLogWaypoint, "passed fix=%s hdg=%.0f->%.0f alt=%.0f", wp.Fix, nav.FlightState.Heading, hdg, nav.FlightState.Altitude)

		clearedAtFix := nav.Approach.AtFixClearedRoute != nil && nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix
		if clearedAtFix {
			nav.Approach.Cleared = true
			nav.Speed = NavSpeed{}
			if wp.NoPT() || nav.Approach.AtFixClearedRoute[0].NoPT() {
				nav.Approach.NoPT = true
			}
			nav.Waypoints = append(nav.Approach.AtFixClearedRoute, nav.FlightState.ArrivalAirport)
			nav.Approach.AtFixClearedRoute = nil
			nav.Approach.StandbyApproach = false
			nav.flyProcedureTurnIfNecessary()
		}
		interceptedAtFix := false
		// Check if this is an "at fix intercept" fix
		if nav.Approach.AtFixInterceptFix == wp.Fix && nav.Approach.Assigned != nil {
			// Start intercepting the localizer. prepareForApproach handles
			// both cases: if on a heading, it sets InterceptState = InitialHeading;
			// if direct to approach fix, it splices the routes.
			_, assignedHeading := nav.AssignedHeading()
			if nav.prepareForApproach(false) == nil {
				if !assignedHeading {
					nav.Approach.InterceptState = OnApproachCourse
					nav.Approach.NoPT = true
				}
				interceptedAtFix = true
			}
			nav.Approach.AtFixInterceptFix = "" // Clear so we don't trigger again
		}
		if nav.Heading.Arc != nil {
			nav.Heading = NavHeading{}
		}

		if groups := wp.ActionGroups(); len(groups) > 0 && groups[0].Actions.ClearApproach {
			if fp != nil {
				_ = nav.ClearedApproach(nav.Approach.AssignedId, nil, simTime, false)
			}
		}

		if wp.InterceptApproach() {
			if fp != nil {
				_ = nav.InterceptApproach(fp.ArrivalAirport, nil)
			}
		}

		if nav.Approach.Cleared {
			// The aircraft has made it to the approach fix they
			// were cleared to, so they can start to descend.
			nav.clearAltitudeForApproach()
			nav.Approach.PassedApproachFix = true
			if wp.FAF() {
				nav.Approach.PassedFAF = true
			}
		} else if wp.OnApproach() {
			// Overflew an approach fix but haven't been cleared yet.
			nav.Approach.PassedApproachFix = true
		}

		if wp.FAF() && nav.InterceptedButNotCleared() {
			// At the FAF without clearance, go around.
			nav.Approach.GoAroundNoApproachClearance = true
		} else if nav.InterceptedButNotCleared() && !nav.Approach.StandbyApproach {
			if wp.IF() {
				// At the IF, the pilot asks for approach clearance.
				nav.Approach.RequestApproachClearance = true
			} else if wp.OnApproach() {
				// At an intermediate approach fix, check if the altitude
				// makes descent to the FAF challenging (>300 ft/nm).
				nav.checkEarlyApproachRequest(wp)
			}
		}

		if wp.AltitudeRestriction() != nil && !nav.InterceptedButNotCleared() &&
			(!nav.Approach.Cleared || wp.AltitudeRestriction().Range[0] < nav.FlightState.Altitude) {
			// Don't climb if we're cleared approach and below the next
			// fix's altitude. Copy the value since the pointer into the
			// slice element could become stale if the slice is reallocated.
			ar := *wp.AltitudeRestriction()
			nav.Altitude.Restriction = &ar
		}
		if sr := wp.SpeedRestriction(); sr != nil && !wp.OnSID() {
			// Carry on the speed restriction unless it's a SID
			srCopy := *sr
			nav.Speed.Restriction = &srCopy
		}

		var actionEvent *av.WaypointActionEvent
		if groups := wp.ActionGroups(); len(groups) > 0 {
			actionEvent = nav.activateWaypointActions(wp.Fix, groups[0].Actions)
		}

		if nfa, ok := nav.FixAssignments[wp.Fix]; ok {
			if nfa.Depart.Speed != nil {
				sr := *nfa.Depart.Speed
				nav.Speed = NavSpeed{Assigned: &sr}
			} else if nfa.Depart.CancelSpeed {
				nav.Speed = NavSpeed{}
			}
		}

		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Altitude != nil {
			nav.assignAltitudeNow(*nfa.Depart.Altitude, false)
		}

		skipWaypointNavigation := clearedAtFix || interceptedAtFix
		var next *av.Waypoint
		if len(nav.Waypoints) > 1 {
			next = &nav.Waypoints[1]
		}
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
			// Controller-assigned heading
			hdg := *nfa.Depart.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
			if wps, _, err := nav.directFixWaypoints(nfa.Depart.Fix.Fix); err == nil {
				// Hacky: below we peel off the current waypoint, so re-add
				// it here so everything works out.
				nav.Waypoints = append([]av.Waypoint{*wp}, wps...)
				nav.Heading.Turn = nfa.Depart.Turn // may be nil (TurnClosest)
			}
		} else if h := nav.actionGroupHeading(wp.Fix, wp.ActionGroups(), next); h != nil && !skipWaypointNavigation {
			nav.Heading = *h
		} else if wp.Arc() != nil && !interceptedAtFix {
			// Fly the DME arc
			nav.Heading = NavHeading{Arc: wp.Arc(), JoiningArc: true}
		}

		if wp.NoPT() {
			nav.Approach.NoPT = true
		}

		if wp.AirworkMinutes() > 0 {
			nav.Airwork = StartAirwork(*wp, *nav)
		}

		// Remove the waypoint from the route unless it's the destination
		// airport, which we leave in any case.
		if len(nav.Waypoints) == 1 {
			// Passing the airport; leave it in the route but make sure
			// we're on a heading.
			hdg := nav.FlightState.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else {
			nav.Waypoints = nav.Waypoints[1:]
		}

		if nav.Heading.Assigned == nil && len(nav.Heading.Maneuvers) == 0 {
			nav.flyProcedureTurnIfNecessary()
		}

		if len(nav.Waypoints) > 0 {
			// Is there a hold coming up at the next waypoint?
			if nfa, ok := nav.FixAssignments[nav.Waypoints[0].Fix]; ok && nfa.Hold != nil {
				nav.Heading = NavHeading{Hold: nav.makeFlyHold(callsign, *nfa.Hold)}
			}
		}

		// Log the updated route after passing the waypoint
		LogRoute(callsign, simTime, nav.Waypoints)

		result := UpdateResult{PassedWaypoint: wp}
		if actionEvent != nil {
			result.ActionEvents = append(result.ActionEvents, *actionEvent)
		}
		return result
	}
	return UpdateResult{}
}

// actionGroupHeading returns how to fly a waypoint's action groups after
// passing it, or nil if they give no heading to fly. A lone open-ended
// heading is flown as an assigned heading, as a controller's would be, so
// that the rest of nav treats it as one; anything more is a maneuver
// sequence.
func (nav *Nav) actionGroupHeading(fix string, groups []av.WaypointActionGroup, next *av.Waypoint) *NavHeading {
	if len(groups) == 0 {
		return nil
	}
	if len(groups) == 1 && groups[0].Until.Type == av.WaypointActionNoTermination {
		h := groups[0].Actions.Heading
		switch {
		case !h.IsSet():
			return nil
		case h.PresentHeading:
			// Round to nearest 5 degrees
			hdg := math.MagneticHeading(5 * int((float32(nav.FlightState.Heading)+2.5)/5))
			hdg = math.NormalizeHeading(hdg)
			return &NavHeading{Assigned: &hdg}
		case !h.Track:
			hdg, turn := math.MagneticHeading(h.Heading), h.Turn
			return &NavHeading{Assigned: &hdg, Turn: &turn}
		}
	}
	return &NavHeading{Maneuvers: nav.makeActionGroupManeuvers(fix, groups, next)}
}

// makeActionGroupManeuvers translates a waypoint's action groups into the
// maneuvers that fly them. next is the following fix on the route, which an
// /@crs course termination joins; it is nil if there is none.
func (nav *Nav) makeActionGroupManeuvers(fix string, groups []av.WaypointActionGroup,
	next *av.Waypoint) []LateralManeuver {
	maneuvers := make([]LateralManeuver, 0, len(groups))
	for _, group := range groups {
		m := LateralManeuver{
			Fix:     fix,
			Actions: group.Actions,
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
				m.TrackFrom, m.TrackFromFix = heading.FixLocation, heading.Fix
			} else {
				m.Heading = math.MagneticHeading(heading.Heading)
			}
		}

		switch group.Until.Type {
		case av.WaypointActionNoTermination:
			m.Until = ManeuverComplete{Type: UntilControllerIntervention}
		case av.WaypointActionAltitude:
			m.Until = ManeuverComplete{Type: UntilAltitude, Altitude: group.Until.Altitude}
		case av.WaypointActionDME:
			m.Until = ManeuverComplete{
				Type:            UntilDME,
				DMEDistance:     group.Until.DMEDistance,
				DMEFix:          group.Until.DMEFixLocation,
				DMEFixElevation: group.Until.DMEFixElevation,
			}
		case av.WaypointActionCourse:
			if next == nil {
				// parseWaypoints rejects /@crs on the last waypoint, but the
				// route may since have been truncated; hold the heading
				// rather than intercepting a course through 0°N 0°E.
				m.Until = ManeuverComplete{Type: UntilControllerIntervention}
			} else {
				// The turn onto the course is always the short way around,
				// regardless of which way the aircraft turned to take up
				// the group's heading.
				m.Until = ManeuverComplete{
					Type:            UntilIntercept,
					Fix:             next.Location,
					InterceptCourse: math.MagneticHeading(group.Until.Course),
					InterceptTurn:   av.TurnClosest,
					InterceptFix:    next.Fix,
				}
			}
		case av.WaypointActionRadial:
			m.Until = ManeuverComplete{
				Type:      UntilRadial,
				Fix:       group.Until.RadialFixLocation,
				Radial:    math.MagneticHeading(group.Until.Radial),
				RadialFix: group.Until.RadialFix,
			}
		default:
			panic("unhandled WaypointActionTerminationType")
		}
		maneuvers = append(maneuvers, m)
	}
	return maneuvers
}

func (nav *Nav) activateWaypointActions(fix string, actions av.WaypointActions) *av.WaypointActionEvent {
	if actions.ClimbAltitude != 0 {
		nav.assignAltitudeNow(float32(actions.ClimbAltitude*100), false)
	} else if actions.DescendAltitude != 0 {
		nav.assignAltitudeNow(float32(actions.DescendAltitude*100), false)
	}
	if actions.HasSimActions() {
		return &av.WaypointActionEvent{Fix: fix, Actions: actions}
	}
	return nil
}

// turnPath is a closed-form model of an upcoming turn: an optional straight
// lead, a constant-rate circular arc through the full turn angle, and a
// straight tail, all displaced linearly by the wind. The leads stand in for
// the time spent rolling into and out of the bank, sized so that the model's
// duration and endpoint match the tick-by-tick roll that TargetHeading flies.
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
// updatePositionAndGS integrates position.
func (nav *Nav) predictTurnPath(hdg math.MagneticHeading, turn av.TurnDirection, wxs wx.Sample) turnPath {
	fs := &nav.FlightState
	tasKts := nav.TAS(wxs.Temperature())
	tasMS := tasKts * 0.514444

	// Steady-state turn rate in deg/s at the given bank angle, signed with
	// the bank, where positive bank is into the turn; the same 3 deg/s cap
	// as TargetHeading.
	rate := func(bank float32) float32 {
		r := min(math.Degrees(9.81*math.Tan(math.Radians(math.Abs(bank)))/tasMS), 3)
		return util.Select(bank < 0, -r, r)
	}

	arcDeg := TurnAngle(fs.Heading, hdg, turn)
	s := float32(util.Select(isTurnRight(float32(fs.Heading), float32(hdg), turn), 1, -1))
	maxBank := nav.Perf.Turn.MaxBankAngle
	rollRate := nav.Perf.Turn.MaxBankRate

	entryBank := s * fs.BankAngle
	if len(nav.Heading.Maneuvers) > 0 || nav.Heading.Hold != nil {
		// Maneuvers turn at a flat StandardTurnRate without maintaining
		// FlightState.BankAngle, so don't trust its stale value.
		entryBank = 0
	}

	// Step the bank from its entry value up to max bank and from max bank
	// back to level the same way TargetHeading does each tick, totalling
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
	if nav.IsAirborne() {
		tp.wind = wxs.WindVec()
	}

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

	// updateHeading turns first and updatePositionAndGS then moves a full
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

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial: when the turn's path, predicted in closed form, would cross it.
func (nav *Nav) shouldTurnForOutbound(p math.Point2LL, hdg math.MagneticHeading, turn av.TurnDirection, wxs wx.Sample) bool {
	eta := nav.ETA(p)

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Alternatively, if we're far away w.r.t. the needed turn, don't even
	// consider it. This is both for performance but also so that we don't
	// make tiny turns miles away from fixes in some cases.
	// The bound is turnAngle/2 seconds of travel, widened where the
	// aircraft's actual turn radius needs more anticipation than that: the
	// radius grows with the square of TAS, and a fast jet's 90 degree
	// fly-by begins more than 6nm out. The widening caps the course change
	// at 100 degrees since beyond that the fly-by anticipation distance
	// grows without bound and the turn would cut miles inside the fix.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	tas := nav.TAS(wxs.Temperature())
	omega := min(3, math.Degrees(9.81*math.Tan(math.Radians(nav.Perf.Turn.MaxBankAngle))/(tas*0.514444)))
	radius := tas / 3600 / math.Radians(omega)
	lead := max(radius*math.Tan(math.Radians(min(turnAngle, 100)/2))+10*nav.FlightState.GS/3600,
		turnAngle/2*nav.FlightState.GS/3600)
	if math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude) > lead {
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	hdgTrue := math.MagneticToTrue(hdg, nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0, math.SinCos(math.Radians(hdgTrue)))

	// The radial is a ground course, so predict a turn to the heading that
	// holds it as a track; the aircraft's post-turn steering to the next
	// fix is wind-corrected the same way. The airborne check matters since
	// departures follow waypoints from the start of the takeoff roll, and
	// on the ground the aircraft neither crabs nor drifts with the wind.
	target := hdg
	if nav.IsAirborne() {
		target = nav.headingForTrack(hdg, wxs)
	}
	tp := nav.predictTurnPath(target, nav.resolveTurnDirection(hdg, turn), wxs)

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
			// angle it first crossed at.
			if math.Abs(endDist) < 0.1 {
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

const StandardTurnRate = 3

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
