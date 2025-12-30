// nav/commands.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

func (nav *Nav) GoAround() *av.RadioTransmission {
	hdg := nav.FlightState.Heading
	nav.Heading = NavHeading{Assigned: &hdg}
	nav.DeferredNavHeading = nil

	nav.Speed = NavSpeed{}

	alt := float32(1000 * int((nav.FlightState.ArrivalAirportElevation+2500)/1000))
	nav.Altitude = NavAltitude{Assigned: &alt}

	nav.Approach = NavApproach{}
	// Keep the destination airport at the end of the route.
	nav.Waypoints = []av.Waypoint{nav.FlightState.ArrivalAirport}

	return av.MakeReadbackTransmission("[going around|on the go]")
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool) *av.RadioTransmission {
	if alt > nav.Perf.Ceiling {
		return av.MakeUnexpectedTransmission("unable. That altitude is above our ceiling.")
	}

	var response *av.RadioTransmission
	if alt > nav.FlightState.Altitude {
		response = av.MakeReadbackTransmission("[climb-and-maintain|up to|] {alt}", alt)
	} else if alt == nav.FlightState.Altitude {
		response = av.MakeReadbackTransmission("[maintain|we'll keep it at|] {alt}", alt)
	} else {
		response = av.MakeReadbackTransmission("[descend-and-maintain|down to|] {alt}", alt)
	}

	if afterSpeed && nav.Speed.Assigned != nil && *nav.Speed.Assigned != nav.FlightState.IAS {
		nav.Altitude.AfterSpeed = &alt
		spd := *nav.Speed.Assigned
		nav.Altitude.AfterSpeedSpeed = &spd

		rspeed := av.MakeReadbackTransmission("at {spd}", *nav.Speed.Assigned)
		rspeed.Merge(response)
		response = rspeed
	} else {
		nav.Altitude = NavAltitude{Assigned: &alt}
	}
	return response
}

func (nav *Nav) AssignSpeed(speed float32, afterAltitude bool) *av.RadioTransmission {
	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if speed == 0 {
		nav.Speed = NavSpeed{}
		return av.MakeReadbackTransmission("cancel speed restrictions")
	} else if float32(speed) < nav.Perf.Speed.Landing {
		return av.MakeReadbackTransmission("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if float32(speed) > maxIAS {
		return av.MakeReadbackTransmission("unable. Our maximum speed is {spd}", maxIAS)
	} else if nav.Approach.Cleared {
		// TODO: make sure we're not within 5 miles...
		nav.Speed = NavSpeed{Assigned: &speed}
		return av.MakeReadbackTransmission("{spd} until 5 mile final", speed)
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		nav.Speed.AfterAltitude = &speed
		alt := *nav.Altitude.Assigned
		nav.Speed.AfterAltitudeAltitude = &alt

		return av.MakeReadbackTransmission("[at {alt} maintain {spd}|at {alt} {spd}|{alt} then {spd}]", alt, speed)
	} else {
		nav.Speed = NavSpeed{Assigned: &speed}
		if speed < nav.FlightState.IAS {
			return av.MakeReadbackTransmission("[reduce to {spd}|speed {spd}|slow to {spd}|{spd}]",
				speed)
		} else if speed > nav.FlightState.IAS {
			return av.MakeReadbackTransmission("[increase to {spd}|speed {spd}|maintain {spd}|{spd}]", speed)
		} else {
			return av.MakeReadbackTransmission("[maintain {spd}|keep it at {spd}|we'll stay at {spd}|{spd}]", speed)
		}
	}
}

func (nav *Nav) MaintainSlowestPractical() *av.RadioTransmission {
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}
	return av.MakeReadbackTransmission("[slowest practical speed|slowing as much as we can]")
}

func (nav *Nav) MaintainMaximumForward() *av.RadioTransmission {
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	return av.MakeReadbackTransmission("[maximum forward speed|maintaining maximum forward speed]")
}

func (nav *Nav) SaySpeed() *av.RadioTransmission {
	currentSpeed := nav.FlightState.IAS

	if nav.Speed.Assigned != nil {
		assignedSpeed := *nav.Speed.Assigned
		if assignedSpeed < currentSpeed {
			return av.MakeReadbackTransmission("[at {spd} slowing to {spd}|at {spd} down to {spd}]", currentSpeed, assignedSpeed)
		} else if assignedSpeed > currentSpeed {
			return av.MakeReadbackTransmission("at {spd} speeding up to {spd}", currentSpeed, assignedSpeed)
		} else {
			return av.MakeReadbackTransmission("[maintaining {spd}|at {spd}]", currentSpeed)
		}
	} else {
		return av.MakeReadbackTransmission("[maintaining {spd}|at {spd}]", currentSpeed)
	}
}

func (nav *Nav) SayHeading() *av.RadioTransmission {
	currentHeading := nav.FlightState.Heading

	if nav.Heading.Assigned != nil {
		assignedHeading := *nav.Heading.Assigned
		if assignedHeading != currentHeading {
			return av.MakeReadbackTransmission("[heading {hdg}|{hdg}]", currentHeading, assignedHeading)
		} else {
			return av.MakeReadbackTransmission("heading {hdg}", currentHeading)
		}
	} else {
		return av.MakeReadbackTransmission("heading {hdg}", currentHeading)
	}
}

func (nav *Nav) SayAltitude() *av.RadioTransmission {
	currentAltitude := nav.FlightState.Altitude

	if nav.Altitude.Assigned != nil {
		assignedAltitude := *nav.Altitude.Assigned
		if assignedAltitude < currentAltitude {
			return av.MakeReadbackTransmission("[at {alt} descending to {alt}|at {alt} and descending]",
				currentAltitude, assignedAltitude)
		} else if assignedAltitude > currentAltitude {
			return av.MakeReadbackTransmission("at {alt} climbing to {alt}", currentAltitude, assignedAltitude)
		} else {
			return av.MakeReadbackTransmission("[maintaining {alt}|at {alt}]", currentAltitude)
		}
	} else {
		return av.MakeReadbackTransmission("maintaining {alt}", currentAltitude)
	}
}

func (nav *Nav) ExpediteDescent() *av.RadioTransmission {
	alt, _ := nav.TargetAltitude()
	if alt >= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return av.MakeReadbackTransmission("[expediting down to|expedite to] {alt} once we're at {spd}",
				*nav.Altitude.AfterSpeed, *nav.Altitude.AfterSpeedSpeed)
		} else {
			return av.MakeUnexpectedTransmission("unable. We're not descending")
		}
	} else if nav.Altitude.Expedite {
		return av.MakeReadbackTransmission("[we're already expediting|that's our best rate]")
	} else {
		nav.Altitude.Expedite = true
		return av.MakeReadbackTransmission("[expediting down to|expedite] {alt}", alt)
	}
}

func (nav *Nav) ExpediteClimb() *av.RadioTransmission {
	alt, _ := nav.TargetAltitude()
	if alt <= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return av.MakeReadbackTransmission("[expediting up to|expedite to] {alt} once we're at {spd}",
				*nav.Altitude.AfterSpeed, *nav.Altitude.AfterSpeedSpeed)
		} else {
			return av.MakeUnexpectedTransmission("unable. We're not climbing")
		}
	} else if nav.Altitude.Expedite {
		return av.MakeReadbackTransmission("[we're already expediting|that's our best rate]")
	} else {
		nav.Altitude.Expedite = true
		return av.MakeReadbackTransmission("[expediting up to|expedite] {alt}", alt)
	}
}

func (nav *Nav) AssignHeading(hdg float32, turn TurnMethod) *av.RadioTransmission {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnexpectedTransmission("unable. {hdg} isn't a valid heading", hdg)
	}

	cancelHold := util.Select(nav.Heading.Hold != nil, "cancel the hold, ", "")

	nav.assignHeading(hdg, turn)

	switch turn {
	case TurnClosest:
		return av.MakeReadbackTransmission(cancelHold+"[heading|fly heading] {hdg}", hdg)
	case TurnRight:
		return av.MakeReadbackTransmission(cancelHold+"[right heading|right|turn right] {hdg}", hdg)
	case TurnLeft:
		return av.MakeReadbackTransmission(cancelHold+"[left heading|left|turn left] {hdg}", hdg)
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}
}

func (nav *Nav) assignHeading(hdg float32, turn TurnMethod) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false

		// If an arrival is given a heading off of a route with altitude
		// constraints, set its cleared altitude to its current altitude
		// for now.
		if len(nav.Waypoints) > 0 && (nav.Waypoints[0].OnSTAR || nav.Waypoints[0].OnApproach) && nav.Altitude.Assigned == nil {
			if _, ok := nav.getWaypointAltitudeConstraint(); ok {
				// Don't take a direct pointer to nav.FlightState.Altitude!
				alt := nav.FlightState.Altitude
				nav.Altitude.Cleared = &alt
			}
		}
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(hdg, turn)
}

func (nav *Nav) FlyPresentHeading() *av.RadioTransmission {
	nav.assignHeading(nav.FlightState.Heading, TurnClosest)
	return av.MakeReadbackTransmission("[fly present heading|present heading]")
}

func (nav *Nav) fixInRoute(fix string) bool {
	if slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return fix == wp.Fix }) {
		return true
	}

	if ap := nav.Approach.Assigned; ap != nil {
		for _, route := range ap.Waypoints {
			for i := range route {
				if fix == route[i].Fix {
					return true
				}
			}
		}
	}
	return false
}

func (nav *Nav) fixPairInRoute(fixa, fixb string) (fa *av.Waypoint, fb *av.Waypoint) {
	find := func(f string, wp []av.Waypoint) int {
		return slices.IndexFunc(wp, func(wp av.Waypoint) bool { return wp.Fix == f })
	}

	var apWaypoints []av.WaypointArray
	if nav.Approach.Assigned != nil {
		apWaypoints = nav.Approach.Assigned.Waypoints
	}

	wps := nav.AssignedWaypoints()
	if ia := find(fixa, wps); ia != -1 {
		// First fix is in the current route
		fa = &wps[ia]
		if ib := find(fixb, wps[ia:]); ib != -1 {
			// As is the second, and after the first
			fb = &wps[ia+ib]
			return
		}
		for _, wp := range apWaypoints {
			if idx := find(fixb, wp); idx != -1 {
				fb = &wp[idx]
				return
			}
		}
	} else {
		// Check the approaches
		for _, wp := range apWaypoints {
			if ia := find(fixa, wp); ia != -1 {
				fa = &wp[ia]
				if ib := find(fixb, wp[ia:]); ib != -1 {
					fb = &wp[ia+ib]
					return
				}
			}
		}
	}
	return
}

func (nav *Nav) directFixWaypoints(fix string) ([]av.Waypoint, error) {
	// Check the approach (if any) first; this way if the current route
	// ends with a fix that happens to be on the approach, we pick up the
	// rest of the approach fixes rather than forgetting about them.
	if ap := nav.Approach.Assigned; ap != nil {
		// This is a little hacky, but... Because of the way we currently
		// interpret ARINC424 files, fixes with procedure turns have no
		// procedure turn for routes with /nopt from the previous fix.
		// Therefore, if we are going direct to a fix that has a procedure
		// turn, we can't take the first matching route but have to keep
		// looking for it in case another route has it with a PT...
		var wps []av.Waypoint
		for _, route := range ap.Waypoints {
			for i, wp := range route {
				if wp.Fix == fix {
					wps = append(route[i:], nav.FlightState.ArrivalAirport)
					if wp.ProcedureTurn != nil {
						return wps, nil
					}
				}
			}
		}
		if wps != nil {
			return wps, nil
		}
	}

	// Look for the fix in the waypoints in the flight plan.
	wps := nav.AssignedWaypoints()
	for i, wp := range wps {
		if fix == wp.Fix {
			return wps[i:], nil
		}
	}

	// See if it's a random fix not in the flight plan.
	p, ok := func() (math.Point2LL, bool) {
		if p, ok := av.DB.LookupWaypoint(fix); ok {
			return p, true
		} else if ap, ok := av.DB.Airports[fix]; ok {
			return ap.Location, true
		} else if ap, ok := av.DB.Airports["K"+fix]; len(fix) == 3 && ok {
			return ap.Location, true
		}
		return math.Point2LL{}, false
	}()
	if ok {
		// Ignore ones that are >150nm away under the assumption that it's
		// a typo in that case.
		if math.NMDistance2LL(p, nav.FlightState.Position) > 150 {
			return nil, ErrFixIsTooFarAway
		}

		return []av.Waypoint{
			av.Waypoint{
				Fix:      fix,
				Location: p,
			},
			nav.FlightState.ArrivalAirport,
		}, nil
	}

	return nil, ErrInvalidFix
}

func (nav *Nav) DirectFix(fix string) *av.RadioTransmission {
	if wps, err := nav.directFixWaypoints(fix); err == nil {
		if hold := nav.Heading.Hold; hold != nil {
			// We'll finish our lap and then depart the holding fix direct to the fix
			hold.Cancel = true
			nfa := NavFixAssignment{}
			nfa.Depart.Fix = &wps[0]
			nav.FixAssignments[hold.Hold.Fix] = nfa
			return av.MakeReadbackTransmission("cancel the hold and depart {fix} direct {fix}", hold.Hold.Fix, fix)
		} else {
			nav.EnqueueDirectFix(wps)
			nav.Approach.NoPT = false
			nav.Approach.InterceptState = NotIntercepting
			return av.MakeReadbackTransmission("direct {fix}", fix)
		}
	} else if err == ErrFixIsTooFarAway {
		return av.MakeUnexpectedTransmission("unable. {fix} is too far away to go direct", fix)
	} else {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't a valid fix", fix)
	}
}

func (nav *Nav) HoldAtFix(callsign string, fix string, hold *av.Hold) *av.RadioTransmission {
	if _, ok := av.DB.LookupWaypoint(fix); !ok {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't a valid fix", fix)
	} else if !nav.fixInRoute(fix) {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't in our route")
	}

	// Use controller-specified hold if provided, otherwise look up published hold
	var h av.Hold
	if hold != nil {
		// Controller-specified hold
		h = *hold
	} else {
		// Published hold
		holds, ok := av.DB.EnrouteHolds[fix]
		if !ok || len(holds) == 0 {
			return av.MakeUnexpectedTransmission("unable. no published hold at {fix}", fix)
		}
		h = holds[0]
	}

	if len(nav.Waypoints) > 0 && nav.Waypoints[0].Fix == h.Fix && nav.Heading.Assigned == nil {
		// We're already direct to it for the next fix; get started.
		nav.Heading = NavHeading{Hold: nav.makeFlyHold(callsign, h)}
	} else if nav.DeferredNavHeading != nil && len(nav.DeferredNavHeading.Waypoints) > 0 &&
		nav.DeferredNavHeading.Waypoints[0].Fix == h.Fix {
		nav.DeferredNavHeading.Hold = nav.makeFlyHold(callsign, h)
	} else {
		// It's a later fix. Queue it up; we'll return to it when it's the next waypoint upcoming.
		// Clobber any altitude or heading assignments associated with the fix.
		nav.FixAssignments[h.Fix] = NavFixAssignment{Hold: &h}
	}

	// These seem prudent to clear at this point.
	nav.Approach.Cleared = false
	nav.Approach.PassedApproachFix = false

	turnDir := util.Select(h.TurnDirection == av.TurnRight, "right", "left")
	if h.LegLengthNM > 0 {
		return av.MakeReadbackTransmission("hold "+turnDir+" of {fix}, {num} mile legs", h.Fix, h.LegLengthNM)
	} else {
		return av.MakeReadbackTransmission("hold "+turnDir+" of {fix}, {num} minute legs", h.Fix, h.LegMinutes)
	}
}

func (nav *Nav) makeFlyHold(callsign string, hold av.Hold) *FlyHold {
	// Calculate heading from aircraft's current position to fix
	pHold, _ := av.DB.LookupWaypoint(hold.Fix)
	hdg := math.Heading2LL(nav.FlightState.Position, pHold, nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

	NavLog(callsign, time.Time{}, NavLogHold, "makeFlyHold: headingToFix=%.1f hold_inbound=%.1f turn=%s -> %s",
		hdg, hold.InboundCourse, hold.TurnDirection, hold.Entry(hdg).String())

	return &FlyHold{
		Hold:        hold,
		FixLocation: pHold,
		State:       HoldStateApproaching,
		Entry:       hold.Entry(hdg),
	}
}

func (nav *Nav) DepartFixDirect(fixa string, fixb string) *av.RadioTransmission {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return av.MakeReadbackTransmission("depart {fix} direct {fix}", fixa, fixb)
}

func (nav *Nav) DepartFixHeading(fix string, hdg float32) *av.RadioTransmission {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnexpectedTransmission("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return av.MakeUnexpectedTransmission("unable. {fix} isn't in our route")
	}

	nfa := nav.FixAssignments[fix]
	h := float32(hdg)
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return av.MakeReadbackTransmission("depart {fix} heading {hdg}", fix, hdg)
}

func (nav *Nav) CrossFixAt(fix string, ar *av.AltitudeRestriction, speed int) *av.RadioTransmission {
	if !nav.fixInRoute(fix) {
		return av.MakeUnexpectedTransmission("unable. " + fix + " isn't in our route")
	}

	pt := av.MakeReadbackTransmission("cross {fix}", fix)

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		pt.Merge(av.MakeReadbackTransmission("{altrest}", ar))
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if speed != 0 {
		s := float32(speed)
		nfa.Arrive.Speed = &s
		pt.Add("at {spd}", s)
		// Delete other speed restrictions
		nav.Speed = NavSpeed{}
	}
	nav.FixAssignments[fix] = nfa

	return pt
}

func (nav *Nav) CancelApproachClearance() *av.RadioTransmission {
	if !nav.Approach.Cleared {
		return av.MakeUnexpectedTransmission("we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false

	return av.MakeReadbackTransmission("cancel approach clearance.")
}

func (nav *Nav) ClimbViaSID() *av.RadioTransmission {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSID {
		return av.MakeUnexpectedTransmission("unable. We're not flying a departure procedure")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse()
	return av.MakeReadbackTransmission("climb via the SID")
}

func (nav *Nav) DescendViaSTAR() *av.RadioTransmission {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSTAR {
		return av.MakeUnexpectedTransmission("unable. We're not on a STAR")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse()
	return av.MakeReadbackTransmission("descend via the STAR")
}

func (nav *Nav) DistanceAlongRoute(fix string) (float32, error) {
	if nav.Heading.Assigned != nil {
		return 0, ErrNotFlyingRoute
	}
	if len(nav.Waypoints) == 0 {
		return 0, nil
	} else {
		index := slices.IndexFunc(nav.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == fix })
		if index == -1 {
			return 0, ErrFixNotInRoute
		}
		wp := nav.Waypoints[:index+1]
		distance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
		for i := 0; i < len(wp)-1; i++ {
			distance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
		}
		return distance, nil
	}
}

func (nav *Nav) ResumeOwnNavigation() *av.RadioTransmission {
	if nav.Heading.Assigned == nil {
		return av.MakeReadbackTransmission("I don't think you ever put us on a heading...")
	}

	nav.Heading = NavHeading{}
	nav.Waypoints = nav.AssignedWaypoints() // just take any deferred ones immediately.
	nav.DeferredNavHeading = nil

	if len(nav.Waypoints) > 1 {
		// Find the route segment we're closest to then go direct to the
		// end of it.  In some cases for the first segment maybe it's
		// preferable to go to the first fix but it's a little unclear what
		// the criteria should be.
		minDist := float32(1000000)
		startIdx := 0
		pac := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
		for i := 0; i < len(nav.Waypoints)-1; i++ {
			wp0, wp1 := nav.Waypoints[i], nav.Waypoints[i+1]
			p0 := math.LL2NM(wp0.Location, nav.FlightState.NmPerLongitude)
			p1 := math.LL2NM(wp1.Location, nav.FlightState.NmPerLongitude)
			if d := math.PointSegmentDistance(pac, p0, p1); d < minDist {
				minDist = d
				startIdx = i + 1
			}
		}
		nav.Waypoints = nav.Waypoints[startIdx:]
	}
	return av.MakeReadbackTransmission("[own navigation|resuming own navigation]")
}

func (nav *Nav) AltitudeOurDiscretion() *av.RadioTransmission {
	if nav.Altitude.Assigned == nil {
		return av.MakeReadbackTransmission("You never assigned us an altitude...")
	}

	nav.Altitude = NavAltitude{}
	alt := nav.FinalAltitude
	nav.Altitude.Cleared = &alt

	return av.MakeReadbackTransmission("[altitude our discretion|altitude our discretion, maintain VFR]")
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}

///////////////////////////////////////////////////////////////////////////
// Procedure turns
