// nav/join.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"cmp"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// JoinKind identifies what an aircraft flying a heading has been told to
// join.
type JoinKind int

const (
	JoinRadial JoinKind = iota
	JoinAirway
	JoinSID
	JoinSTAR
)

// RouteJoin records what an aircraft flying a heading has been told to
// join. It is kept alongside the maneuvers that make the join so that a new
// heading given "to join" can recompute the intercept from it, and it is
// dropped along with them when the aircraft is given anything else to do.
type RouteJoin struct {
	Kind JoinKind
	// Name is the airway or procedure, or the fix a radial is of.
	Name     string
	Radial   math.MagneticHeading // JoinRadial
	Outbound bool                 // JoinRadial
}

// intent returns the readback of the instruction to make the join.
func (j RouteJoin) intent() speech.NavigationIntent {
	switch j.Kind {
	case JoinRadial:
		return speech.NavigationIntent{Type: speech.NavInterceptRadial, Fix: j.Name, Radial: j.Radial,
			Outbound: j.Outbound}
	case JoinAirway:
		return speech.NavigationIntent{Type: speech.NavJoinAirway, Airway: j.Name}
	case JoinSID:
		return speech.NavigationIntent{Type: speech.NavResumeSID, Procedure: j.Name}
	default:
		return speech.NavigationIntent{Type: speech.NavResumeSTAR, Procedure: j.Name}
	}
}

// phrase returns how a response names what is being joined, as a phrase
// format and its arguments.
func (j RouteJoin) phrase() (string, []any) {
	switch j.Kind {
	case JoinRadial:
		return "the {fix} {hdg} radial", []any{j.Name, j.Radial}
	case JoinAirway:
		return "{airway}", []any{j.Name}
	case JoinSID:
		return "the {sid}", []any{j.Name}
	default:
		return "the {star}", []any{j.Name}
	}
}

// JoinAirway has the aircraft fly its assigned heading until it intercepts
// the part of its route that follows the airway, then continue along the
// route from there.
func (nav *Nav) JoinAirway(airway string, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	if _, ok := db.DB.Airways[airway]; !ok {
		return speech.MakeUnableIntent("unable. {airway} isn't a valid airway", airway)
	}
	return nav.joinFromAssignedHeading(RouteJoin{Kind: JoinAirway, Name: airway}, simTime, delayReduction)
}

// ResumeSID has the aircraft fly its assigned heading until it intercepts
// its SID, then continue along it.
func (nav *Nav) ResumeSID(sid string, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	if sid == "" {
		return speech.MakeUnableIntent("unable. we're not on a SID")
	}
	return nav.joinFromAssignedHeading(RouteJoin{Kind: JoinSID, Name: sid}, simTime, delayReduction)
}

// ResumeSTAR has the aircraft fly its assigned heading until it intercepts
// its STAR, then continue along it.
func (nav *Nav) ResumeSTAR(star string, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	if star == "" {
		return speech.MakeUnableIntent("unable. we're not on a STAR")
	}
	return nav.joinFromAssignedHeading(RouteJoin{Kind: JoinSTAR, Name: star}, simTime, delayReduction)
}

// AssignHeadingToJoin gives the aircraft a new heading from which to make
// the join it was previously told to make.
func (nav *Nav) AssignHeadingToJoin(hdg math.MagneticHeading, turn av.TurnDirection, simTime Time,
	delayReduction time.Duration) speech.CommandIntent {
	if hdg <= 0 || hdg > 360 {
		return speech.MakeUnableIntent("unable. {hdg} isn't a valid heading", hdg)
	}
	join := nav.pendingJoin()
	if join == nil {
		return speech.MakeUnableIntent("unable. we weren't told to join anything")
	}

	cancelHold := nav.Heading.Hold != nil
	if unable := nav.startJoin(*join, hdg, turn, simTime, delayReduction); unable != nil {
		return unable
	}
	joinIntent := join.intent()
	return speech.HeadingIntent{
		Heading:    hdg,
		Type:       speech.HeadingAssign,
		Turn:       headingTurn(turn),
		CancelHold: cancelHold,
		Join:       &joinIntent,
	}
}

// pendingJoin returns the join the aircraft is flying its heading to make,
// if any. A pending instruction that isn't a join, such as a direct fix,
// cancels one that was in progress.
func (nav *Nav) pendingJoin() *RouteJoin {
	if dh := nav.DeferredNavHeading; dh != nil {
		return dh.Join
	}
	return nav.Heading.Join
}

// headingToJoinFrom returns the heading the aircraft will be flying when it
// makes a join: the one it has been assigned, whether or not the pilot has
// started the turn to it, and its turn direction.
func (nav *Nav) headingToJoinFrom() (math.MagneticHeading, av.TurnDirection, bool) {
	turn := av.TurnClosest
	if dh := nav.DeferredNavHeading; dh != nil && dh.Heading != nil {
		if dh.Turn != nil {
			turn = *dh.Turn
		}
		return *dh.Heading, turn, true
	}
	if nav.Heading.Assigned != nil {
		if nav.Heading.Turn != nil {
			turn = *nav.Heading.Turn
		}
		return *nav.Heading.Assigned, turn, true
	}
	return 0, turn, false
}

// joinFromAssignedHeading makes the join from the aircraft's assigned
// heading, returning the join's readback or an unable response.
func (nav *Nav) joinFromAssignedHeading(join RouteJoin, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	hdg, turn, ok := nav.headingToJoinFrom()
	if !ok {
		return speech.MakeUnableIntent("unable. we're not on a heading")
	}
	if unable := nav.startJoin(join, hdg, turn, simTime, delayReduction); unable != nil {
		return unable
	}
	return join.intent()
}

// startJoin assigns hdg, with the join's maneuvers taking effect along with
// it after the pilot reaction delay. It returns an unable response if the
// join can't be made from hdg.
func (nav *Nav) startJoin(join RouteJoin, hdg math.MagneticHeading, turn av.TurnDirection, simTime Time,
	delayReduction time.Duration) speech.CommandIntent {
	var maneuvers []LateralManeuver
	var route []av.Waypoint
	var unable speech.CommandIntent
	if join.Kind == JoinRadial {
		maneuvers, route, unable = nav.radialJoinManeuvers(join, hdg, turn)
	} else {
		maneuvers, unable = nav.routeJoinManeuvers(join, hdg, turn)
	}
	if unable != nil {
		return unable
	}
	// A route already pending, which the join was computed against, stays
	// pending along with the new heading.
	if route == nil && nav.hasDeferredRoute() {
		route = nav.DeferredNavHeading.Waypoints
	}

	// Assign the heading for its approach and altitude side effects and its
	// pilot reaction delay; the maneuvers, and a new route if the join
	// leaves the old one, take effect along with it.
	snapshotAltitude := nav.DeferredNavHeading != nil && nav.DeferredNavHeading.SnapshotAltitudeOnEffect
	nav.assignHeading(hdg, turn, simTime, delayReduction)
	nav.Approach.InterceptState = NotIntercepting
	dh := nav.DeferredNavHeading
	dh.SnapshotAltitudeOnEffect = dh.SnapshotAltitudeOnEffect || snapshotAltitude
	dh.Maneuvers = maneuvers
	dh.Join = &join
	dh.Waypoints = route
	return nil
}

// routeJoinManeuvers returns the maneuver that flies hdg until it
// intercepts the leg ahead of the airway or procedure being joined and then
// takes up the route from that leg's end, or an unable response if the
// aircraft's route has none of it or hdg crosses none of its legs.
func (nav *Nav) routeJoinManeuvers(join RouteJoin, hdg math.MagneticHeading, turn av.TurnDirection) ([]LateralManeuver, speech.CommandIntent) {
	var onLeg func(a, b av.Waypoint) bool
	switch join.Kind {
	case JoinAirway:
		onLeg = airwayLeg(db.DB.Airways[join.Name])
	case JoinSID:
		onLeg = procedureLeg(av.Waypoint.OnSID)
	case JoinSTAR:
		onLeg = procedureLeg(av.Waypoint.OnSTAR)
	}

	wps := nav.AssignedWaypoints()
	// The leg the aircraft was vectored off of runs from the waypoint it
	// last passed, no longer in the route, to the next one; the aircraft
	// can be turned back to join it short of that waypoint.
	if nav.PassedWaypoint.Fix != "" && !nav.hasDeferredRoute() {
		wps = append([]av.Waypoint{nav.PassedWaypoint}, wps...)
	}
	stretches := joinableStretches(wps, onLeg)
	target, args := join.phrase()
	if len(stretches) == 0 {
		return nil, speech.MakeUnableIntent("unable. "+target+" isn't in our route", args...)
	}
	routes := util.MapSlice(stretches, func(s [2]int) av.WaypointArray { return wps[s[0]:s[1]] })

	fs := &nav.FlightState
	hits := nav.forwardRouteIntercepts(math.MagneticToTrue(hdg, fs.MagneticVariation), routes)
	// A crossing at the aircraft's own position is the leg it is leaving,
	// not one it is joining.
	hits = util.FilterSlice(hits, func(hit av.RouteRayIntersection) bool {
		return math.NMDistance2LL(fs.Position, hit.Location) > 0.1
	})
	if len(hits) == 0 {
		return nil, speech.MakeUnableIntent("unable. heading {hdg} won't intercept "+target,
			append([]any{hdg}, args...)...)
	}

	// The leg joined runs between the route fixes on either side of the
	// crossed segment; a crossing point between them may be replaced by a
	// later restriction, so the join is anchored to the fix beyond it.
	hit := hits[0]
	stretch := stretches[hit.RouteIndex]
	start, end := stretch[0]+hit.Index, stretch[0]+hit.Index+1
	for wps[start].SyntheticCrossing() {
		start--
	}
	for wps[end].SyntheticCrossing() {
		end++
	}
	fix := wps[end]
	course := math.TrueToMagnetic(math.Heading2LL(wps[start].Location, fix.Location, fs.NmPerLongitude),
		fs.MagneticVariation)
	m := flyHeadingUntilIntercept(hdg, turn, fix.Location, course)
	m.Until.InterceptTurn = av.TurnClosest
	m.Until.InterceptFix = fix.Fix
	m.ResumeFix = fix.Fix
	return []LateralManeuver{m}, nil
}

// joinableStretches returns the runs of consecutive route waypoints, as
// [start, end) index pairs, whose legs are all on the route being joined. A
// leg runs between two route fixes; a crossing point that a restriction
// added between them lies on the leg and is carried along with it.
func joinableStretches(wps []av.Waypoint, onLeg func(a, b av.Waypoint) bool) [][2]int {
	var stretches [][2]int
	for i := range wps {
		if wps[i].SyntheticCrossing() {
			continue
		}
		k := i + 1
		for k < len(wps) && wps[k].SyntheticCrossing() {
			k++
		}
		if k == len(wps) {
			break
		}
		if !onLeg(wps[i], wps[k]) {
			continue
		}
		if n := len(stretches); n > 0 && stretches[n-1][1] == i+1 {
			stretches[n-1][1] = k + 1
		} else {
			stretches = append(stretches, [2]int{i, k + 1})
		}
	}
	return stretches
}

// airwayLeg returns a predicate reporting whether the leg between two
// waypoints follows the airway: whether they are adjacent fixes of it, in
// either direction. This is so whether the route named the airway or just
// filed its fixes.
func airwayLeg(airways []av.Airway) func(a, b av.Waypoint) bool {
	return func(a, b av.Waypoint) bool {
		if a.Location.IsZero() || b.Location.IsZero() {
			return false
		}
		return slices.ContainsFunc(airways, func(aw av.Airway) bool {
			ia := slices.IndexFunc(aw.Fixes, func(f av.AirwayFix) bool { return f.Fix == a.Fix })
			ib := slices.IndexFunc(aw.Fixes, func(f av.AirwayFix) bool { return f.Fix == b.Fix })
			return ia != -1 && ib != -1 && (ib == ia+1 || ib == ia-1)
		})
	}
}

// procedureLeg returns a predicate reporting whether the leg between two
// waypoints is a course along the procedure that onProcedure identifies. A
// leg flown on a heading from the first waypoint or around an arc isn't a
// course between the two that a heading can intercept.
func procedureLeg(onProcedure func(av.Waypoint) bool) func(a, b av.Waypoint) bool {
	return func(a, b av.Waypoint) bool {
		if !onProcedure(a) || !onProcedure(b) || a.Location.IsZero() || b.Location.IsZero() {
			return false
		}
		_, heading := a.HeadingAction()
		return !heading && a.Arc() == nil
	}
}

// forwardRouteIntercepts returns where a ray from the aircraft's position
// along hdg crosses routes, nearest first, leaving out crossings that would
// join a segment against its direction of flight.
func (nav *Nav) forwardRouteIntercepts(hdg math.TrueHeading, routes []av.WaypointArray) []av.RouteRayIntersection {
	pos, nmPerLong := nav.FlightState.Position, nav.FlightState.NmPerLongitude
	hits := util.FilterSlice(av.IntersectRayWithRoutes(pos, hdg, routes),
		func(hit av.RouteRayIntersection) bool {
			route := routes[hit.RouteIndex]
			segHdg := math.Heading2LL(route[hit.Index].Location, route[hit.Index+1].Location, nmPerLong)
			return math.HeadingDifference(hdg, segHdg) <= 90
		})
	slices.SortStableFunc(hits, func(a, b av.RouteRayIntersection) int {
		return cmp.Compare(math.NMDistance2LL(pos, a.Location), math.NMDistance2LL(pos, b.Location))
	})
	return hits
}
