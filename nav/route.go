// nav/route.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// The route: the waypoints the aircraft flies and how it sequences them,
// the controller's instructions that change the route, and distances
// along it.

// AssignedWaypoints returns the route that should be flown following a
// controller instruction. If an instruction has been issued but the delay
// hasn't passed, these are different than the waypoints currently being
// used for navigation.
func (nav *Nav) AssignedWaypoints() []av.Waypoint {
	if nav.hasDeferredRoute() {
		return nav.DeferredNavHeading.Waypoints
	}
	return nav.Waypoints
}

// setAssignedWaypoints replaces the route returned by AssignedWaypoints.
func (nav *Nav) setAssignedWaypoints(wps []av.Waypoint) {
	if nav.hasDeferredRoute() {
		nav.DeferredNavHeading.Waypoints = wps
	} else {
		nav.Waypoints = wps
	}
}

// FlyRoute replaces the aircraft's route with wps and has it fly the route
// from where it is, dropping any heading it was flying.
func (nav *Nav) FlyRoute(wps []av.Waypoint) {
	defer nav.record("FlyRoute", wps)()
	nav.Waypoints = wps
	nav.Heading = Heading{}
}

// FlyFromRunway has an aircraft that is back on the runway after a
// touch-and-go take off again along wps, with its speed and altitude managed
// as a departure's are.
func (nav *Nav) FlyFromRunway(wps []av.Waypoint) {
	defer nav.record("FlyFromRunway", wps)()
	nav.Waypoints = wps
	nav.Heading = Heading{}
	nav.FlightState.InitialDepartureClimb = true
}

// AmendRoute replaces the waypoints of the assigned route before the one at
// fix with wps, as a flight plan amendment does. It reports false, leaving
// the route alone, if fix isn't on it.
func (nav *Nav) AmendRoute(wps []av.Waypoint, fix string) bool {
	defer nav.record("AmendRoute", wps, fix)()
	route := nav.AssignedWaypoints()
	idx := slices.IndexFunc(route, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if idx == -1 {
		return false
	}
	nav.setAssignedWaypoints(slices.Concat(wps, route[idx:]))
	return true
}

// knowsFix reports whether fix is a fix the pilot can find: one in the
// database or one on the aircraft's route or assigned approach.
func (nav *Nav) knowsFix(fix string) bool {
	_, ok := nav.lookup().Waypoint(fix)
	return ok || nav.fixInRoute(fix)
}

// fixInRoute reports whether fix is on the aircraft's assigned route or its
// assigned approach.
func (nav *Nav) fixInRoute(fix string) bool {
	if slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return fix == wp.Fix }) {
		return true
	}
	route, _ := approachRouteThrough(nav.Approach.Assigned, fix)
	return route != nil
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
	if route, idx := approachRouteThrough(nav.Approach.Assigned, fix); route != nil {
		return slices.Concat(route[idx:].Clone(), av.WaypointArray{nav.FlightState.ArrivalAirport}),
			waypointSourceApproach, nil
	}

	// See if it's a random fix not in the flight plan. It may name an
	// airport by either of its ids.
	p, ok := nav.lookup().Waypoint(fix)
	if !ok {
		p, _, ok = nav.lookup().Airport(fix)
	}
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

func (nav *Nav) ExpectDirect(fix string) speech.CommandIntent {
	defer nav.record("ExpectDirect", fix)()
	if !nav.knowsFix(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}
	nav.ExpectedDirectFix = fix
	return nil
}

func (nav *Nav) DirectFix(fix string, turn av.TurnDirection, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("DirectFix", fix, turn, simTime, delayReduction)()
	wps, source, err := nav.directFixWaypoints(fix)
	if err == ErrFixIsTooFarAway {
		return speech.MakeUnableIntent("unable. {fix} is too far away to go direct", fix)
	} else if err != nil {
		return speech.MakeUnableIntent("unable. {fix} isn't a valid fix", fix)
	}

	// Waypoints from the approach before the clearance: the aircraft tracks
	// the approach course laterally, but its restrictions don't apply until
	// the clearance.
	fromApproach := source == waypointSourceApproach && !nav.Approach.Cleared

	if hold := nav.Heading.Hold; hold != nil {
		// We'll finish our lap and then depart the holding fix direct to the fix
		hold.Cancel = true
		nfa := FixAssignment{}
		nfa.Depart.Fix = &wps[0]
		if turn != av.TurnClosest {
			nfa.Depart.Turn = &turn
		}
		nav.FixAssignments[hold.Hold.Fix] = nfa
		if fromApproach {
			nav.Approach.InterceptState = OnApproachCourse
		}
		return speech.NavigationIntent{
			Type:      speech.NavDirectFixFromHold,
			Fix:       hold.Hold.Fix,
			SecondFix: fix,
			Turn:      turn,
		}
	}

	nav.enqueueDirectFix(wps, turn, simTime, delayReduction)
	nav.Approach.NoPT = false
	nav.Approach.InterceptState = util.Select(fromApproach, OnApproachCourse, NotIntercepting)
	return speech.NavigationIntent{
		Type: speech.NavDirectFix,
		Fix:  fix,
		Turn: turn,
	}
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
	nav.enqueueOnCourse(simTime, 0)
}

func (nav *Nav) ResumeOwnNavigation() speech.CommandIntent {
	defer nav.record("ResumeOwnNavigation")()
	if nav.Heading.Assigned == nil {
		// This is a weird response but keeping the original behavior
		return speech.MakeUnableIntent("unable. I don't think you ever put us on a heading...")
	}

	nav.Heading = Heading{}
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
		if origin, ok := legOriginBefore(nav.Waypoints, startIdx); ok {
			nav.PassedWaypoint = origin
		}
		nav.Waypoints = nav.Waypoints[startIdx:]
	}
	return speech.NavigationIntent{Type: speech.NavResumeOwnNav}
}

// GoAroundWithProcedure initiates a go-around with a defined procedure.
// The runwayEndWP waypoint should have Location (opposite threshold), FlyOver,
// and AltitudeRestriction set, and an action group with the outbound heading
// and the GoAroundContactController.
func (nav *Nav) GoAroundWithProcedure(altitude float32, runwayEndWP av.Waypoint) {
	defer nav.record("GoAroundWithProcedure", altitude, runwayEndWP)()
	nav.DeferredNavHeading = nil
	nav.Speed = Speed{}
	nav.Approach = Approach{}
	nav.Requests = PilotRequests{}
	nav.setAssignedAltitude(altitude)
	nav.Waypoints = av.WaypointArray{runwayEndWP, nav.FlightState.ArrivalAirport}
}

func (nav *Nav) DivertToAirport(airport av.ICAOAirportCode) {
	defer nav.record("DivertToAirport", airport)()
	location, elevation, ok := nav.lookup().Airport(string(airport))
	if !ok {
		return
	}

	wp := av.Waypoint{
		Fix:      string(airport),
		Location: location,
	}
	nav.Waypoints = []av.Waypoint{wp}

	nav.FlightState.ArrivalAirport = wp
	nav.FlightState.ArrivalAirportLocation = location
	nav.FlightState.ArrivalAirportElevation = elevation
}

// ETA returns the estimated time in seconds until the aircraft will arrive at `p`, assuming it is flying direct.
func (nav *Nav) ETA(p math.Point2LL) float32 {
	dist := math.NMDistance2LLFast(nav.FlightState.Position, p, nav.FlightState.NmPerLongitude)
	return dist / nav.FlightState.GS * 3600 // seconds
}

// routeDistanceToFix computes the distance in nm along the waypoint route
// from the aircraft's current position to the named fix. Returns ok=false
// if the fix is not found in the waypoints.
func (nav *Nav) routeDistanceToFix(fix string) (float32, bool) {
	if len(nav.Waypoints) == 0 {
		return 0, false
	}
	d := math.NMDistance2LLFast(nav.FlightState.Position, nav.Waypoints[0].Location,
		nav.FlightState.NmPerLongitude)
	for i := range nav.Waypoints {
		if nav.Waypoints[i].Fix == fix {
			return d, true
		}
		if i+1 < len(nav.Waypoints) {
			d += math.NMDistance2LLFast(nav.Waypoints[i].Location, nav.Waypoints[i+1].Location,
				nav.FlightState.NmPerLongitude)
		}
	}
	return 0, false
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
	if !passed && !nav.missedUnreachableFix(wp, wxs) {
		return UpdateResult{}
	}

	nav.Heading.Turn = nil
	NavLog(callsign, simTime, NavLogWaypoint, "passed fix=%s hdg=%.0f->%.0f alt=%.0f", wp.Fix, nav.FlightState.Heading, hdg, nav.FlightState.Altitude)
	return nav.passWaypoint(callsign, wp, simTime)
}

// missedUnreachableFix reports whether wp, a fix the aircraft must reach
// (one it flies over or the route's last), is one it has turned toward as
// far as it can and missed: the fix is abeam or behind and inside the circle
// the aircraft flies turning toward it, so only a loop would reach it. The
// aircraft goes on without it rather than fly one. Approaches and VFR
// landings line the aircraft up with a runway and are left to do so.
func (nav *Nav) missedUnreachableFix(wp av.Waypoint, wxs wx.Sample) bool {
	if (!wp.FlyOver() && len(nav.Waypoints) > 1) || wp.OnApproach() || wp.VFRPhase != av.VFRPhaseNone ||
		!nav.IsAirborne() {
		return false
	}

	fs := &nav.FlightState
	bearing := math.TrueToMagnetic(math.Heading2LL(fs.Position, wp.Location, fs.NmPerLongitude), fs.MagneticVariation)
	if math.HeadingDifference(fs.Heading, bearing) < 90 {
		return false
	}

	// The circle's center is a turn radius off the side the fix is on.
	r := TurnRadius(nav.Perf, nav.TAS(wxs.Temperature()))
	side := float32(util.Select(math.HeadingSignedTurn(fs.Heading, bearing) > 0, 90, -90))
	toCenter := math.OffsetHeading(math.MagneticToTrue(fs.Heading, fs.MagneticVariation), side)
	center := math.Offset2LL(fs.Position, toCenter, r, fs.NmPerLongitude)
	// Within a couple of seconds of the circle, the turn passes close
	// enough to the fix to reach it.
	return math.NMDistance2LL(center, wp.Location) < r-2*fs.GS/3600
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

// legOriginBefore returns the last route fix before index i, skipping the
// crossing points that restrictions add between fixes.
func legOriginBefore(wps []av.Waypoint, i int) (av.Waypoint, bool) {
	for i--; i >= 0; i-- {
		if !wps[i].SyntheticCrossing() {
			return wps[i], true
		}
	}
	return av.Waypoint{}, false
}

// resumeRouteAt takes up the route at the named fix after a maneuver that
// joined the given course to it. The waypoints before the fix are dropped,
// except for crossing points on that course still ahead of the aircraft,
// which is now on it; a crossing on the route's own leg into the fix is
// off a radial that comes in from another direction. The fix's leg origin
// becomes the waypoint passed, so the leg can be rejoined if the aircraft
// is vectored off it again. A route that no longer has the fix, having
// been replaced in the meantime, is left as it is.
func (nav *Nav) resumeRouteAt(fix string, course math.MagneticHeading) {
	wps := nav.Waypoints
	i := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if i == -1 {
		return
	}

	fs := &nav.FlightState
	p := math.LL2NM(fs.Position, fs.NmPerLongitude)
	f := math.LL2NM(wps[i].Location, fs.NmPerLongitude)
	dir := math.HeadingVector(math.MagneticToTrue(course, fs.MagneticVariation))
	onCourseAhead := func(wp av.Waypoint) bool {
		q := math.LL2NM(wp.Location, fs.NmPerLongitude)
		return math.Dot(math.Sub2f(q, p), dir) > 0 &&
			math.Abs(math.SignedPointLineDistance(q, f, math.Add2f(f, dir))) < 0.25
	}
	for i > 0 && wps[i-1].SyntheticCrossing() && onCourseAhead(wps[i-1]) {
		i--
	}
	if origin, ok := legOriginBefore(wps, i); ok {
		nav.PassedWaypoint = origin
	}
	nav.Waypoints = wps[i:]
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
