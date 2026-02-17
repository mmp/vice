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

// GoAroundWithProcedure initiates a go-around with a defined procedure.
// The runwayEndWP waypoint should have Location (opposite threshold), FlyOver,
// Heading (outbound), AltitudeRestriction, and GoAroundContactController set.
func (nav *Nav) GoAroundWithProcedure(altitude float32, runwayEndWP av.Waypoint) {
	nav.DeferredNavHeading = nil
	nav.Speed = NavSpeed{}
	nav.Approach = NavApproach{}
	nav.Altitude = NavAltitude{Assigned: &altitude}
	nav.Waypoints = av.WaypointArray{runwayEndWP, nav.FlightState.ArrivalAirport}
}

func (nav *Nav) AssignAltitude(alt float32, afterSpeed bool) av.CommandIntent {
	if alt > nav.Perf.Ceiling {
		return av.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}

	var direction av.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = av.AltitudeClimb
	} else if alt == nav.FlightState.Altitude {
		direction = av.AltitudeMaintain
	} else {
		direction = av.AltitudeDescend
	}

	intent := av.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
	}

	if afterSpeed && nav.Speed.Assigned != nil && *nav.Speed.Assigned != nav.FlightState.IAS {
		nav.Altitude.AfterSpeed = &alt
		spd := *nav.Speed.Assigned
		nav.Altitude.AfterSpeedSpeed = &spd
		intent.AfterSpeed = &spd
	} else {
		nav.Altitude = NavAltitude{Assigned: &alt}
	}

	return intent
}

func (nav *Nav) AssignSpeed(speed float32, afterAltitude bool) av.CommandIntent {
	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if speed == 0 {
		nav.Speed = NavSpeed{}
		return av.SpeedIntent{Type: av.SpeedCancel}
	} else if float32(speed) < nav.Perf.Speed.Landing {
		return av.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if float32(speed) > maxIAS {
		return av.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS)
	} else if nav.Approach.Cleared {
		// TODO: make sure we're not within 5 miles...
		nav.Speed = NavSpeed{Assigned: &speed}
		return av.SpeedIntent{Speed: speed, Type: av.SpeedUntilFinal}
	} else if afterAltitude && nav.Altitude.Assigned != nil &&
		*nav.Altitude.Assigned != nav.FlightState.Altitude {
		nav.Speed.AfterAltitude = &speed
		alt := *nav.Altitude.Assigned
		nav.Speed.AfterAltitudeAltitude = &alt
		return av.SpeedIntent{Speed: speed, AfterAltitude: &alt, Type: av.SpeedAssign}
	} else {
		nav.Speed = NavSpeed{Assigned: &speed}
		if speed < nav.FlightState.IAS {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedReduce}
		} else if speed > nav.FlightState.IAS {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedIncrease}
		} else {
			return av.SpeedIntent{Speed: speed, Type: av.SpeedAssign}
		}
	}
}

func (nav *Nav) AssignSpeedUntil(speed float32, until *av.SpeedUntil) av.CommandIntent {
	maxIAS := av.TASToIAS(nav.Perf.Speed.MaxTAS, nav.FlightState.Altitude)
	maxIAS = 10 * float32(int((maxIAS+5)/10)) // round to 10s

	if float32(speed) < nav.Perf.Speed.Landing {
		return av.MakeUnableIntent("unable. Our minimum speed is {spd}", nav.Perf.Speed.Landing)
	} else if float32(speed) > maxIAS {
		return av.MakeUnableIntent("unable. Our maximum speed is {spd}", maxIAS)
	}

	nav.Speed = NavSpeed{Assigned: &speed}
	return av.SpeedIntent{Speed: speed, Type: av.SpeedUntilFinal, Until: until}
}

func (nav *Nav) MaintainSlowestPractical() av.CommandIntent {
	nav.Speed = NavSpeed{MaintainSlowestPractical: true}
	return av.SpeedIntent{Type: av.SpeedSlowestPractical}
}

func (nav *Nav) MaintainMaximumForward() av.CommandIntent {
	nav.Speed = NavSpeed{MaintainMaximumForward: true}
	return av.SpeedIntent{Type: av.SpeedMaximumForward}
}

func (nav *Nav) MaintainPresentSpeed() av.CommandIntent {
	// Capture current indicated airspeed and assign it, rounded to nearest 10
	currentSpeed := nav.FlightState.IAS
	speed := float32(int((currentSpeed+5)/10) * 10)
	nav.Speed = NavSpeed{Assigned: &speed}
	return av.SpeedIntent{Speed: speed, Type: av.SpeedPresentSpeed}
}

func (nav *Nav) SaySpeed() av.CommandIntent {
	currentSpeed := nav.FlightState.IAS
	intent := av.ReportSpeedIntent{Current: currentSpeed}
	if nav.Speed.Assigned != nil {
		intent.Assigned = nav.Speed.Assigned
	}
	return intent
}

func (nav *Nav) SayHeading() av.CommandIntent {
	currentHeading := nav.FlightState.Heading
	intent := av.ReportHeadingIntent{Current: currentHeading}
	if nav.Heading.Assigned != nil {
		intent.Assigned = nav.Heading.Assigned
	}
	return intent
}

func (nav *Nav) SayAltitude() av.CommandIntent {
	currentAltitude := nav.FlightState.Altitude
	intent := av.ReportAltitudeIntent{Current: currentAltitude}
	if nav.Altitude.Assigned != nil {
		intent.Assigned = nav.Altitude.Assigned
		if *nav.Altitude.Assigned < currentAltitude {
			intent.Direction = av.AltitudeDescend
		} else if *nav.Altitude.Assigned > currentAltitude {
			intent.Direction = av.AltitudeClimb
		} else {
			intent.Direction = av.AltitudeMaintain
		}
	}
	return intent
}

func (nav *Nav) ExpediteDescent() av.CommandIntent {
	alt, _ := nav.TargetAltitude()
	if alt >= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return av.AltitudeIntent{
				Direction:  av.AltitudeDescend,
				Altitude:   *nav.Altitude.AfterSpeed,
				AfterSpeed: nav.Altitude.AfterSpeedSpeed,
			}
		} else {
			return av.MakeUnableIntent("unable. We're not descending")
		}
	} else if nav.Altitude.Expedite {
		return av.AltitudeIntent{
			Direction:         av.AltitudeDescend,
			Altitude:          alt,
			AlreadyExpediting: true,
		}
	} else {
		nav.Altitude.Expedite = true
		return av.AltitudeIntent{
			Direction: av.AltitudeDescend,
			Altitude:  alt,
			Expedite:  true,
		}
	}
}

func (nav *Nav) ExpediteClimb() av.CommandIntent {
	alt, _ := nav.TargetAltitude()
	if alt <= nav.FlightState.Altitude {
		if nav.Altitude.AfterSpeed != nil {
			nav.Altitude.ExpediteAfterSpeed = true
			return av.AltitudeIntent{
				Direction:  av.AltitudeClimb,
				Altitude:   *nav.Altitude.AfterSpeed,
				AfterSpeed: nav.Altitude.AfterSpeedSpeed,
			}
		} else {
			return av.MakeUnableIntent("unable. We're not climbing")
		}
	} else if nav.Altitude.Expedite {
		return av.AltitudeIntent{
			Direction:         av.AltitudeClimb,
			Altitude:          alt,
			AlreadyExpediting: true,
		}
	} else {
		nav.Altitude.Expedite = true
		return av.AltitudeIntent{
			Direction: av.AltitudeClimb,
			Altitude:  alt,
			Expedite:  true,
		}
	}
}

func (nav *Nav) AssignHeading(hdg float32, turn av.TurnDirection, simTime time.Time) av.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnableIntent("unable. {hdg} isn't a valid heading", hdg)
	}

	cancelHold := nav.Heading.Hold != nil
	nav.assignHeading(hdg, turn, simTime)

	intent := av.HeadingIntent{
		Heading:    hdg,
		Type:       av.HeadingAssign,
		CancelHold: cancelHold,
	}

	switch turn {
	case av.TurnClosest:
		intent.Turn = av.HeadingTurnClosest
	case av.TurnRight:
		intent.Turn = av.HeadingTurnToRight
	case av.TurnLeft:
		intent.Turn = av.HeadingTurnToLeft
	default:
		panic(fmt.Sprintf("%d: unhandled turn type", turn))
	}

	return intent
}

func (nav *Nav) assignHeading(hdg float32, turn av.TurnDirection, simTime time.Time) {
	if _, ok := nav.AssignedHeading(); !ok {
		// Only cancel approach clearance if the aircraft wasn't on a
		// heading and now we're giving them one.
		nav.Approach.Cleared = false

		// MVAs are back in the mix
		nav.Approach.PassedApproachFix = false

		// If an arrival is given a heading off of a route with altitude
		// constraints, set its cleared altitude to its current altitude
		// for now.
		if len(nav.Waypoints) > 0 && (nav.Waypoints[0].OnSTAR() || nav.Waypoints[0].OnApproach()) && nav.Altitude.Assigned == nil {
			if _, ok := nav.getWaypointAltitudeConstraint(); ok {
				// Don't take a direct pointer to nav.FlightState.Altitude!
				alt := nav.FlightState.Altitude
				nav.Altitude.Cleared = &alt
			}
		}
	}

	// Don't carry this from a waypoint we may have previously passed.
	nav.Approach.NoPT = false
	nav.EnqueueHeading(hdg, turn, simTime)
}

func (nav *Nav) FlyPresentHeading(simTime time.Time) av.CommandIntent {
	nav.assignHeading(nav.FlightState.Heading, av.TurnClosest, simTime)
	return av.HeadingIntent{
		Heading: nav.FlightState.Heading,
		Type:    av.HeadingPresent,
	}
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

type waypointSource int

const (
	waypointSourceRoute    waypointSource = iota // fix found on the STAR/route
	waypointSourceApproach                       // fix found on the assigned approach
	waypointSourceOther                          // global fix lookup
)

func (nav *Nav) directFixWaypoints(fix string) ([]av.Waypoint, waypointSource, error) {
	// Check the route first; when a fix exists on both the STAR/route
	// and the approach, we want the route waypoints so the aircraft
	// doesn't start flying the approach without being cleared.
	routeWps := nav.AssignedWaypoints()
	for i, wp := range routeWps {
		if fix == wp.Fix {
			return routeWps[i:], waypointSourceRoute, nil
		}
	}

	// Check the approach (if any).
	if ap := nav.Approach.Assigned; ap != nil {
		// This is a little hacky, but... Because of the way we currently
		// interpret ARINC424 files, fixes with procedure turns have no
		// procedure turn for routes with /nopt from the previous fix.
		// Therefore, if we are going direct to a fix that has a procedure
		// turn, we can't take the first matching route but have to keep
		// looking for it in case another route has it with a PT...
		var apWps []av.Waypoint
		for _, route := range ap.Waypoints {
			for i, wp := range route {
				if wp.Fix == fix {
					apWps = append(route[i:], nav.FlightState.ArrivalAirport)
					if wp.ProcedureTurn() != nil {
						return apWps, waypointSourceApproach, nil
					}
				}
			}
		}
		if apWps != nil {
			return apWps, waypointSourceApproach, nil
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
			return nil, waypointSourceOther, ErrFixIsTooFarAway
		}

		return []av.Waypoint{
			{
				Fix:      fix,
				Location: p,
			},
			nav.FlightState.ArrivalAirport,
		}, waypointSourceOther, nil
	}

	return nil, waypointSourceOther, ErrInvalidFix
}

func (nav *Nav) DirectFix(fix string, simTime time.Time) av.CommandIntent {
	if wps, source, err := nav.directFixWaypoints(fix); err == nil {
		if hold := nav.Heading.Hold; hold != nil {
			// We'll finish our lap and then depart the holding fix direct to the fix
			hold.Cancel = true
			nfa := NavFixAssignment{}
			nfa.Depart.Fix = &wps[0]
			nav.FixAssignments[hold.Hold.Fix] = nfa
			if source == waypointSourceApproach && !nav.Approach.Cleared {
				nav.Approach.InterceptState = OnApproachCourse
			}
			return av.NavigationIntent{
				Type:      av.NavDirectFixFromHold,
				Fix:       hold.Hold.Fix,
				SecondFix: fix,
			}
		} else {
			nav.EnqueueDirectFix(wps, simTime)
			nav.Approach.NoPT = false
			if source == waypointSourceApproach && !nav.Approach.Cleared {
				// The waypoints came from the approach but the aircraft
				// hasn't been cleared; track the approach course laterally
				// but gate altitude constraints via InterceptedButNotCleared().
				nav.Approach.InterceptState = OnApproachCourse
			} else {
				nav.Approach.InterceptState = NotIntercepting
			}
			return av.NavigationIntent{
				Type: av.NavDirectFix,
				Fix:  fix,
			}
		}
	} else if err == ErrFixIsTooFarAway {
		return av.MakeUnableIntent("unable. {fix} is too far away to go direct", fix)
	} else {
		return av.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
}

func (nav *Nav) HoldAtFix(callsign string, fix string, hold *av.Hold) av.CommandIntent {
	if _, ok := av.DB.LookupWaypoint(fix); !ok {
		return av.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	} else if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
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
			return av.MakeUnableIntent("unable. no published hold at {fix}", fix)
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
	var legLength string
	if h.LegLengthNM > 0 {
		legLength = fmt.Sprintf("%g mile", h.LegLengthNM)
	} else {
		legLength = fmt.Sprintf("%g minute", h.LegMinutes)
	}

	return av.NavigationIntent{
		Type:          av.NavHold,
		Fix:           h.Fix,
		HoldDirection: turnDir,
		HoldLegLength: legLength,
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

func (nav *Nav) DepartFixDirect(fixa string, fixb string) av.CommandIntent {
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return av.MakeUnableIntent("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return av.NavigationIntent{
		Type:      av.NavDepartFixDirect,
		Fix:       fixa,
		SecondFix: fixb,
	}
}

func (nav *Nav) DepartFixHeading(fix string, hdg float32) av.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return av.MakeUnableIntent("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	nfa := nav.FixAssignments[fix]
	h := float32(hdg)
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return av.NavigationIntent{
		Type:    av.NavDepartFixHeading,
		Fix:     fix,
		Heading: hdg,
	}
}

func (nav *Nav) CrossFixAt(fix string, ar *av.AltitudeRestriction, speed int) av.CommandIntent {
	if !nav.fixInRoute(fix) {
		return av.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	intent := av.NavigationIntent{
		Type: av.NavCrossFixAt,
		Fix:  fix,
	}

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		intent.AltRestriction = ar
		// Delete other altitude restrictions
		nav.Altitude = NavAltitude{}
	}
	if speed != 0 {
		s := float32(speed)
		nfa.Arrive.Speed = &s
		intent.Speed = &s
		// Delete other speed restrictions
		nav.Speed = NavSpeed{}
	}
	nav.FixAssignments[fix] = nfa

	return intent
}

func (nav *Nav) CancelApproachClearance() av.CommandIntent {
	if !nav.Approach.Cleared {
		return av.MakeUnableIntent("unable. we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false

	return av.ApproachIntent{Type: av.ApproachCancel}
}

func (nav *Nav) ClimbViaSID(simTime time.Time) av.CommandIntent {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSID() {
		return av.MakeUnableIntent("unable. We're not flying a departure procedure")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse(simTime)
	return av.ProcedureIntent{Type: av.ProcedureClimbViaSID}
}

func (nav *Nav) DescendViaSTAR(simTime time.Time) av.CommandIntent {
	if wps := nav.AssignedWaypoints(); len(wps) == 0 || !wps[0].OnSTAR() {
		return av.MakeUnableIntent("unable. We're not on a STAR")
	}

	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}
	nav.EnqueueOnCourse(simTime)
	return av.ProcedureIntent{Type: av.ProcedureDescendViaSTAR}
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

func (nav *Nav) ResumeOwnNavigation() av.CommandIntent {
	if nav.Heading.Assigned == nil {
		// This is a weird response but keeping the original behavior
		return av.MakeUnableIntent("unable. I don't think you ever put us on a heading...")
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
	return av.NavigationIntent{Type: av.NavResumeOwnNav}
}

func (nav *Nav) AltitudeOurDiscretion() av.CommandIntent {
	if nav.Altitude.Assigned == nil {
		return av.MakeUnableIntent("unable. You never assigned us an altitude...")
	}

	nav.Altitude = NavAltitude{}
	alt := nav.FinalAltitude
	nav.Altitude.Cleared = &alt

	return av.NavigationIntent{Type: av.NavAltitudeDiscretion}
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}
