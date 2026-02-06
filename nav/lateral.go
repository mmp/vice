// nav/lateral.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) headingForTrack(hdg float32, wxs wx.Sample) float32 {
	// Update heading for wind and magnetic variation.
	v := math.SinCos(math.Radians(hdg))
	v = math.Scale2f(v, nav.FlightState.GS)
	hdg -= wxs.Deflection(v)
	return math.NormalizeHeading(hdg + nav.FlightState.MagneticVariation)
}

func (nav *Nav) updateHeading(callsign string, wxs wx.Sample, simTime time.Time) {
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
	case TurnLeft:
		angle := math.NormalizeHeading(nav.FlightState.Heading - targetHeading)
		angle = min(angle, turnRate)
		turn = -angle
	case TurnRight:
		angle := math.NormalizeHeading(targetHeading - nav.FlightState.Heading)
		angle = min(angle, turnRate)
		turn = angle
	case TurnClosest:
		turn = math.HeadingSignedTurn(nav.FlightState.Heading, targetHeading)
		turn = math.Clamp(turn, -turnRate, turnRate)
	}

	// Finally, do the turn.
	nav.FlightState.Heading = math.NormalizeHeading(nav.FlightState.Heading + turn)
}

func (nav *Nav) updatePositionAndGS(wxs wx.Sample) {
	// Calculate offset vector based on heading and current TAS.
	hdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	TAS := nav.TAS() / 3600
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

func (nav *Nav) DepartOnCourse(alt float32, exit string) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Don't do anything if they are not on a heading; let them fly the
		// regular route and don't (potentially) skip waypoints and go
		// straight to the exit; however, the altitude should be changed
		nav.Altitude = NavAltitude{Assigned: &alt}
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
	nav.Altitude = NavAltitude{Assigned: &alt}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse()
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

// returns passed waypoint if any
func (nav *Nav) Update(callsign string, model *wx.Model, fp *av.FlightPlan, simTime time.Time, bravo *av.AirspaceGrid) *av.Waypoint {
	// Perform single weather lookup at the start
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime)
	return nav.UpdateWithWeather(callsign, wxs, fp, simTime, bravo)
}

// UpdateWithWeather is a helper for simulations that use pre-fetched weather
func (nav *Nav) UpdateWithWeather(callsign string, wxs wx.Sample, fp *av.FlightPlan, simTime time.Time, bravo *av.AirspaceGrid) *av.Waypoint {
	// Log current state every tick
	NavLog(callsign, simTime, NavLogState, "pos=%.4f,%.4f alt=%.0f hdg=%.0f ias=%.0f gs=%.0f bank=%.1f rate=%.0f",
		nav.FlightState.Position[0], nav.FlightState.Position[1],
		nav.FlightState.Altitude, nav.FlightState.Heading,
		nav.FlightState.IAS, nav.FlightState.GS,
		nav.FlightState.BankAngle, nav.FlightState.AltitudeRate)

	targetAltitude, altitudeRate := nav.TargetAltitude()
	deltaKts, slowingTo250 := nav.updateAirspeed(callsign, targetAltitude, fp, wxs, simTime, bravo)
	nav.updateAltitude(callsign, targetAltitude, altitudeRate, deltaKts, slowingTo250, wxs, simTime)
	nav.updateHeading(callsign, wxs, simTime)
	nav.updatePositionAndGS(wxs)
	if nav.Airwork != nil && !nav.Airwork.Update(nav) {
		nav.Airwork = nil // Done.
	}

	if nav.Airwork == nil && nav.Heading.Assigned == nil && nav.Heading.Hold == nil {
		return nav.updateWaypoints(callsign, wxs, fp, simTime)
	}

	return nil
}

func (nav *Nav) TargetHeading(callsign string, wxs wx.Sample, simTime time.Time) (heading float32, turn TurnMethod, rate float32) {
	if nav.Airwork != nil {
		return nav.Airwork.TargetHeading()
	}

	// Is it time to start following a heading or direct to a fix recently issued by the controller?
	if dh := nav.DeferredNavHeading; dh != nil && time.Now().After(dh.Time) {
		nav.Heading = NavHeading{Assigned: dh.Heading, Turn: dh.Turn, Hold: dh.Hold} // these may be nil
		if len(dh.Waypoints) > 0 {
			nav.Waypoints = dh.Waypoints
		}
		nav.DeferredNavHeading = nil
	}

	heading, turn = nav.FlightState.Heading, TurnClosest

	// nav.Heading.Assigned may still be nil pending a deferred turn
	if (nav.Approach.InterceptState == InitialHeading ||
		nav.Approach.InterceptState == TurningToJoin) && nav.Heading.Assigned != nil {
		heading, turn = nav.ApproachHeading(callsign, wxs, simTime)
	} else if nav.Heading.RacetrackPT != nil {
		nav.FlightState.BankAngle = 0
		return nav.Heading.RacetrackPT.GetHeading(nav, wxs)
	} else if nav.Heading.Standard45PT != nil {
		nav.FlightState.BankAngle = 0
		return nav.Heading.Standard45PT.GetHeading(nav, wxs)
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
			angle := math.VectorHeading(v) // x, y, as elsewhere..

			// Choose a point a bit farther ahead on the arc
			angle += float32(util.Select(arc.Clockwise, 10, -10))
			p := math.Add2f(pc, math.Scale2f(math.SinCos(math.Radians(angle)), arc.Radius))
			pTarget = math.NM2LL(p, nav.FlightState.NmPerLongitude)
		} else {
			if len(nav.Waypoints) == 0 {
				return // fly present heading...
			}

			pTarget = nav.Waypoints[0].Location
		}

		// No magnetic correction yet, just the raw geometric heading vector
		hdg := math.Heading2LL(nav.FlightState.Position, pTarget, nav.FlightState.NmPerLongitude, 0)
		v := math.SinCos(math.Radians(hdg))
		v = math.Scale2f(v, nav.FlightState.GS)

		if nav.IsAirborne() {
			hdg -= wxs.Deflection(v)
		}

		// Incorporate magnetic variation in the final heading
		hdg += nav.FlightState.MagneticVariation

		heading = math.NormalizeHeading(hdg)
	}

	// We have a heading and a direction; now figure out if we need to
	// adjust the bank and then how far we turn this tick.

	// signed difference, negative is turn left
	headingDelta := func() float32 {
		switch turn {
		case TurnLeft:
			diff := heading - nav.FlightState.Heading
			if diff > 0 {
				return diff - 360 // force left turn
			}
			return diff // already left
		case TurnRight:
			diff := heading - nav.FlightState.Heading
			if diff < 0 {
				return diff + 360 // force right turn
			}
			return diff // already right
		default:
			diff := heading - nav.FlightState.Heading
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
	tasMS := nav.TAS() * 0.514444
	turnRate := func(bankAngle float32) float32 {
		if bankAngle == 0 {
			return 0
		}
		bankRad := math.Radians(bankAngle)
		rate := math.Degrees(9.81 * math.Tan(bankRad) / tasMS)
		return min(rate, 3)
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

	turn = util.Select(nav.FlightState.BankAngle < 0, TurnLeft, TurnRight)

	rate = math.Abs(turnRate(nav.FlightState.BankAngle))

	return
}
func (nav *Nav) updateWaypoints(callsign string, wxs wx.Sample, fp *av.FlightPlan, simTime time.Time) *av.Waypoint {
	if len(nav.Waypoints) == 0 {
		return nil
	}

	wp := &nav.Waypoints[0]
	dist := math.NMDistance2LLFast(nav.FlightState.Position, wp.Location, nav.FlightState.NmPerLongitude)
	NavLog(callsign, simTime, NavLogWaypoint, "next=%s dist=%.2fnm alt=%.0f", wp.Fix, dist, nav.FlightState.Altitude)

	// Are we nearly at the fix and is it time to turn for the outbound heading?
	// First, figure out the outbound heading.
	var hdg float32
	if len(nav.Approach.AtFixClearedRoute) > 1 &&
		nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix {
		hdg = math.Heading2LL(wp.Location, nav.Approach.AtFixClearedRoute[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
		// controller assigned heading at the fix.
		hdg = *nfa.Depart.Heading
	} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
		// depart fix direct
		hdg = math.Heading2LL(wp.Location, nfa.Depart.Fix.Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if wp.Heading != 0 {
		// Leaving the next fix on a specified heading.
		hdg = float32(wp.Heading)
	} else if wp.PresentHeading {
		hdg = nav.FlightState.Heading
	} else if wp.Arc != nil {
		// Joining a DME arc after the heading
		hdg = wp.Arc.InitialHeading
	} else if len(nav.Waypoints) > 1 {
		// Otherwise, find the heading to the following fix.
		hdg = math.Heading2LL(wp.Location, nav.Waypoints[1].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else {
		// No more waypoints (likely about to land), so just
		// plan to stay on the current heading.
		hdg = nav.FlightState.Heading
	}

	passedWaypoint := false
	if wp.FlyOver {
		passedWaypoint = nav.ETA(wp.Location) < 2
	} else {
		passedWaypoint = nav.shouldTurnForOutbound(wp.Location, hdg, TurnClosest, wxs)
	}

	if passedWaypoint {
		NavLog(callsign, simTime, NavLogWaypoint, "passed fix=%s hdg=%.0f->%.0f alt=%.0f", wp.Fix, nav.FlightState.Heading, hdg, nav.FlightState.Altitude)

		clearedAtFix := nav.Approach.AtFixClearedRoute != nil && nav.Approach.AtFixClearedRoute[0].Fix == wp.Fix
		if clearedAtFix {
			nav.Approach.Cleared = true
			nav.Speed = NavSpeed{}
			if wp.NoPT || nav.Approach.AtFixClearedRoute[0].NoPT {
				nav.Approach.NoPT = true
			}
			nav.Waypoints = append(nav.Approach.AtFixClearedRoute, nav.FlightState.ArrivalAirport)
		}
		// Check if this is an "at fix intercept" fix
		if nav.Approach.AtFixInterceptFix == wp.Fix && nav.Approach.Assigned != nil {
			// Start intercepting the localizer. prepareForApproach handles
			// both cases: if on a heading, it sets InterceptState = InitialHeading;
			// if direct to approach fix, it splices the routes.
			nav.prepareForApproach(false)
			nav.Approach.AtFixInterceptFix = "" // Clear so we don't trigger again
		}
		if nav.Heading.Arc != nil {
			nav.Heading = NavHeading{}
		}

		if wp.ClearApproach {
			if fp != nil {
				nav.ClearedApproach(fp.ArrivalAirport, nav.Approach.AssignedId, false)
			}
		}

		if nav.Approach.Cleared {
			// The aircraft has made it to the approach fix they
			// were cleared to, so they can start to descend.
			nav.Altitude = NavAltitude{}
			nav.Approach.PassedApproachFix = true
			if wp.FAF {
				nav.Approach.PassedFAF = true
			}
		} else if wp.OnApproach {
			// Overflew an approach fix but haven't been cleared yet.
			nav.Approach.PassedApproachFix = true
		}

		if wp.AltitudeRestriction != nil && !nav.InterceptedButNotCleared() &&
			(!nav.Approach.Cleared || wp.AltitudeRestriction.Range[0] < nav.FlightState.Altitude) {
			// Don't climb if we're cleared approach and below the next
			// fix's altitude.
			nav.Altitude.Restriction = wp.AltitudeRestriction
		}
		if wp.Speed != 0 && !wp.OnSID {
			// Carry on the speed restriction unless it's a SID
			spd := float32(wp.Speed)
			nav.Speed.Restriction = &spd
		}

		if wp.ClimbAltitude != nil {
			alt := float32(*wp.ClimbAltitude)
			nav.AssignAltitude(alt, false)
		} else if wp.DescendAltitude != nil {
			alt := float32(*wp.DescendAltitude)
			nav.AssignAltitude(alt, false)
		}

		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Heading != nil {
			// Controller-assigned heading
			hdg := *nfa.Depart.Heading
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Depart.Fix != nil {
			if wps, err := nav.directFixWaypoints(nfa.Depart.Fix.Fix); err == nil {
				// Hacky: below we peel off the current waypoint, so re-add
				// it here so everything works out.
				nav.Waypoints = append([]av.Waypoint{*wp}, wps...)
			}
		} else if wp.Heading != 0 && !clearedAtFix {
			// We have an outbound heading
			hdg := float32(wp.Heading)
			turn := TurnMethod(wp.Turn)
			nav.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
		} else if wp.PresentHeading && !clearedAtFix {
			// Round to nearest 5 degrees
			hdg := float32(5 * int((nav.FlightState.Heading+2.5)/5))
			hdg = math.NormalizeHeading(hdg)
			nav.Heading = NavHeading{Assigned: &hdg}
		} else if wp.Arc != nil {
			// Fly the DME arc
			nav.Heading = NavHeading{Arc: wp.Arc, JoiningArc: true}
		}

		if wp.NoPT {
			nav.Approach.NoPT = true
		}

		if wp.AirworkMinutes > 0 {
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

		if nav.Heading.Assigned == nil {
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

		return wp
	}
	return nil
}

// turnRateAndRadius calculates the steady-state turn rate and radius
// based on aircraft performance and current state.
// Returns turnRate in deg/s and radius in nm.
func (nav *Nav) turnRateAndRadius() (turnRate, radius float32) {
	TAS_ms := nav.TAS() * 0.514444
	bankRad := math.Radians(nav.Perf.Turn.MaxBankAngle)
	turnRate = min(math.Degrees(9.81*math.Tan(bankRad)/TAS_ms), 3.0)
	// R = V / ω where V is in nm/s and ω is in rad/s
	turnRateRad := math.Radians(turnRate)
	if turnRateRad > 0 {
		radius = (nav.FlightState.GS / 3600) / turnRateRad
	}
	return
}

// rollLeadDistance returns distance in nm the aircraft travels during roll-in
func (nav *Nav) rollLeadDistance() float32 {
	rollTime := nav.Perf.Turn.MaxBankAngle / nav.Perf.Turn.MaxBankRate
	return (nav.FlightState.GS / 3600) * rollTime
}

// perpRight returns unit vector perpendicular right (clockwise 90°) to heading.
// Uses vice convention: heading 0°=North, 90°=East, direction=[sin,cos]
func perpRight(hdg float32) [2]float32 {
	rad := math.Radians(hdg)
	return [2]float32{math.Cos(rad), -math.Sin(rad)}
}

// perpLeft returns unit vector perpendicular left (counter-clockwise 90°)
func perpLeft(hdg float32) [2]float32 {
	rad := math.Radians(hdg)
	return [2]float32{-math.Cos(rad), math.Sin(rad)}
}

// rolloutPosition calculates where aircraft would end up after completing
// a turn from currentHdg to targetHdg, starting at currentPos.
// All headings are true (not magnetic). Returns position in nm coordinates.
func rolloutPosition(currentPos [2]float32, currentHdg, targetHdg float32,
	radius float32, turnRight bool) [2]float32 {
	var center, rollout [2]float32
	if turnRight {
		perp := perpRight(currentHdg)
		center = math.Add2f(currentPos, math.Scale2f(perp, radius))
		perp = perpLeft(targetHdg)
		rollout = math.Add2f(center, math.Scale2f(perp, radius))
	} else {
		perp := perpLeft(currentHdg)
		center = math.Add2f(currentPos, math.Scale2f(perp, radius))
		perp = perpRight(targetHdg)
		rollout = math.Add2f(center, math.Scale2f(perp, radius))
	}
	return rollout
}

// isTurnRight determines if the turn from currentHdg to targetHdg should
// be a right turn, given the specified TurnMethod.
func isTurnRight(currentHdg, targetHdg float32, turn TurnMethod) bool {
	switch turn {
	case TurnRight:
		return true
	case TurnLeft:
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

// Given a fix location and an outbound heading, returns true when the
// aircraft should start the turn to outbound to intercept the outbound
// radial.
func (nav *Nav) shouldTurnForOutbound(p math.Point2LL, hdg float32, turn TurnMethod, wxs wx.Sample) bool {
	eta := nav.ETA(p)

	// Always start the turn if we've almost passed the fix.
	if eta < 2 {
		return true
	}

	// Alternatively, if we're far away w.r.t. the needed turn, don't even
	// consider it. This is both for performance but also so that we don't
	// make tiny turns miles away from fixes in some cases.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	// Get two points that give the line of the outbound course.
	p0 := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	hm := hdg - nav.FlightState.MagneticVariation
	p1 := math.Add2f(p0, math.SinCos(math.Radians(hm)))

	// Make a ghost aircraft to use to simulate the turn.
	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.DeferredNavHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position,
		nav2.FlightState.NmPerLongitude),
		p0, p1)

	// Don't simulate the turn longer than it will take to do it.
	n := int(1 + turnAngle/3)
	for range n {
		nav2.UpdateWithWeather("", wxs, nil, time.Time{}, nil)
		curDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position,
			nav2.FlightState.NmPerLongitude),
			p0, p1)

		if math.Sign(initialDist) != math.Sign(curDist) {
			// Aircraft is on the other side of the line than it started on.
			return true
		}
	}
	return false
}

// Given a point and a radial, returns true when the aircraft should
// start turning to intercept the radial.
func (nav *Nav) shouldTurnToIntercept(p0 math.Point2LL, hdg float32, turn TurnMethod, wxs wx.Sample) bool {
	p0nm := math.LL2NM(p0, nav.FlightState.NmPerLongitude)
	p1 := math.Add2f(p0nm, math.SinCos(math.Radians(hdg-nav.FlightState.MagneticVariation)))

	initialDist := math.SignedPointLineDistance(math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude), p0nm, p1)
	eta := math.Abs(initialDist) / nav.FlightState.GS * 3600 // in seconds
	if eta < 2 {
		// Just in case, start the turn
		return true
	}

	// As above, don't consider starting the turn if we're far away.
	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle < eta {
		return false
	}

	// Calculate the expected crab angle needed for wind correction.
	// The aircraft's heading will differ from the track by this amount.
	hdgTrue := hdg - nav.FlightState.MagneticVariation
	v := math.SinCos(math.Radians(hdgTrue))
	v = math.Scale2f(v, nav.FlightState.GS)
	crabAngle := math.Abs(wxs.Deflection(v))

	nav2 := *nav
	nav2.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
	nav2.DeferredNavHeading = nil
	nav2.Approach.InterceptState = NotIntercepting // avoid recursive calls..

	n := int(1 + turnAngle)
	for range n {
		nav2.UpdateWithWeather("", wxs, nil, time.Time{}, nil)
		curDist := math.SignedPointLineDistance(math.LL2NM(nav2.FlightState.Position, nav2.FlightState.NmPerLongitude), p0nm, p1)

		// Allow heading tolerance to account for the crab angle needed in crosswind.
		// Base tolerance of 10 degrees plus the calculated crab angle.
		headingTolerance := 10 + crabAngle
		if (math.Abs(curDist) < 0.02 || math.Sign(initialDist) != math.Sign(curDist)) && math.Abs(curDist) < .25 && math.HeadingDifference(hdg, nav2.FlightState.Heading) < headingTolerance {
			return true
		}
	}
	return false
}

// Analytical version of shouldTurnForOutbound using geometry rather than
// simulation. Currently unused - kept for future integration.
func (nav *Nav) shouldTurnForOutboundAnalytical(p math.Point2LL, hdg float32, turn TurnMethod) bool {
	eta := nav.ETA(p)
	if eta < 2 {
		return true
	}

	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle/2 < eta {
		return false
	}

	_, radius := nav.turnRateAndRadius()
	if radius == 0 {
		return false
	}

	currentPos := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
	currentHdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	targetHdg := hdg - nav.FlightState.MagneticVariation

	turnRight := isTurnRight(currentHdg, targetHdg, turn)
	rollout := rolloutPosition(currentPos, currentHdg, targetHdg, radius, turnRight)

	fixPos := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	lineDir := math.SinCos(math.Radians(targetHdg))
	lineEnd := math.Add2f(fixPos, lineDir)

	dist := math.SignedPointLineDistance(rollout, fixPos, lineEnd)
	threshold := max(nav.rollLeadDistance()*0.5, 0.1)

	return math.Abs(dist) < threshold
}

// Analytical version of shouldTurnToIntercept using geometry rather than
// simulation. Currently unused - kept for future integration.
func (nav *Nav) shouldTurnToInterceptAnalytical(p0 math.Point2LL, hdg float32, turn TurnMethod) bool {
	lineOrigin := math.LL2NM(p0, nav.FlightState.NmPerLongitude)
	targetHdgTrue := hdg - nav.FlightState.MagneticVariation
	lineDir := math.SinCos(math.Radians(targetHdgTrue))
	lineEnd := math.Add2f(lineOrigin, lineDir)

	currentPos := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
	initialDist := math.SignedPointLineDistance(currentPos, lineOrigin, lineEnd)

	eta := math.Abs(initialDist) / nav.FlightState.GS * 3600
	if eta < 2 {
		return true
	}

	turnAngle := TurnAngle(nav.FlightState.Heading, hdg, turn)
	if turnAngle < eta {
		return false
	}

	_, radius := nav.turnRateAndRadius()
	if radius == 0 {
		return false
	}

	currentHdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	turnRight := isTurnRight(currentHdg, targetHdgTrue, turn)
	rollout := rolloutPosition(currentPos, currentHdg, targetHdgTrue, radius, turnRight)
	rolloutDist := math.SignedPointLineDistance(rollout, lineOrigin, lineEnd)

	if math.Abs(rolloutDist) > 0.25 {
		return false
	}

	threshold := max(nav.rollLeadDistance()*0.5, 0.02)
	return math.Abs(rolloutDist) < threshold ||
		(math.Sign(initialDist) != math.Sign(rolloutDist))
}

// Suppress staticcheck warnings for intentionally unused analytical functions.
// These are kept for future integration testing.
var (
	_ = (*Nav).turnRateAndRadius
	_ = (*Nav).rollLeadDistance
	_ = perpRight
	_ = perpLeft
	_ = rolloutPosition
	_ = isTurnRight
	_ = (*Nav).shouldTurnForOutboundAnalytical
	_ = (*Nav).shouldTurnToInterceptAnalytical
)

///////////////////////////////////////////////////////////////////////////

type TurnMethod int

const (
	TurnClosest TurnMethod = iota // default
	TurnLeft
	TurnRight
)

func (t TurnMethod) String() string {
	return []string{"closest", "left", "right"}[t]
}

const StandardTurnRate = 3

func TurnAngle(from, to float32, turn TurnMethod) float32 {
	switch turn {
	case TurnLeft:
		return math.NormalizeHeading(from - to)

	case TurnRight:
		return math.NormalizeHeading(to - from)

	case TurnClosest:
		return math.Abs(math.HeadingDifference(from, to))

	default:
		panic("unhandled TurnMethod")
	}
}
