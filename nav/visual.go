// nav/visual.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// Visual approaches: the reference approaches a non-charted visual is
// flown by, the descent profile laid along whichever of them the aircraft
// joins, the route to the final for a pilot who joins none of them, and the
// joins particular to a charted visual.

// clearedForVisualApproach reports whether the aircraft has been cleared for
// a visual approach, either one charted as a procedure or one synthesized for
// the runway.
func (nav *Nav) clearedForVisualApproach() bool {
	if !nav.Approach.Cleared || nav.Approach.Assigned == nil {
		return false
	}
	t := nav.Approach.Assigned.Type
	return t == av.VisualApproach || t == av.ChartedVisualApproach
}

// clearedForVisualOnHeading reports whether the aircraft has been cleared
// for a non-charted visual approach from a heading and has yet to join the
// course.
func (nav *Nav) clearedForVisualOnHeading() bool {
	if !nav.Approach.Cleared || nav.Approach.Assigned == nil || nav.Approach.Assigned.Type != av.VisualApproach {
		return false
	}
	_, onHeading := nav.AssignedHeading()
	return onHeading
}

// visualDescentFromHeading returns the target altitude, rate, and whether
// the descent is geometric for a visual cleared on a heading that has yet
// to join the course. Its path to the runway isn't known until then, so it
// is measured straight to the threshold, which is never longer: the
// aircraft holds its altitude until it is above a 3 degree path to the
// profile's 3 nm final anchor, then descends to cross that anchor.
func (nav *Nav) visualDescentFromHeading() (float32, float32, bool) {
	fs := &nav.FlightState
	anchor := fs.ArrivalAirportElevation + 900
	dist := math.NMDistance2LL(fs.Position, nav.Approach.Assigned.Threshold)
	// A little below the path counts as on it, so that an aircraft
	// following it doesn't stop and start.
	if fs.Altitude <= anchor || fs.Altitude < nav.visualProfileAltitude(dist)-50 {
		return fs.Altitude, 0, false
	}
	toAnchor := dist - 3
	if toAnchor <= 0 || fs.GS <= 0 {
		return anchor, MaximumRate, true
	}
	minutes := toAnchor / fs.GS * 60
	return anchor, (fs.Altitude - anchor) / minutes, true
}

// visualProfileAltitude returns the altitude of a visual approach's descent
// profile dist nm from the threshold: a 3 degree path down to 900 feet above
// the field at the 3 nm final.
func (nav *Nav) visualProfileAltitude(dist float32) float32 {
	return nav.FlightState.ArrivalAirportElevation + 900 + max(dist-3, 0)*ftPerNMOnGlideslope
}

// visualProfileBelow reports whether the aircraft, cleared for a visual, is
// where the visual's descent profile is below alt.
func (nav *Nav) visualProfileBelow(alt float32) bool {
	dist, ok := nav.approachDistanceRemaining()
	return ok && nav.visualProfileAltitude(dist) < alt
}

// selectVisualReferences picks the reference approaches whose geometry will
// seed a synthesized non-charted visual to runway. ILS and Localizer
// approaches are always included when present so direct-to-fix and at-fix
// intercepts against a localizer route work after EVA. The "visual-style"
// component is chosen by priority: VisualApproach if any are charted for the
// runway, else VOR, else NDB, else RNAV, else ChartedVisual. Returns an empty slice
// when no approaches at all are defined for the runway base.
func selectVisualReferences(airport *av.Airport, runway string) []*av.Approach {
	runwayBase := av.RunwayID(runway).Base()
	matches := func(match func(av.ApproachType) bool) []*av.Approach {
		var out []*av.Approach
		for ap := range util.SortedMapValues(airport.Approaches) {
			if av.RunwayID(ap.Runway).Base() == runwayBase && match(ap.Type) {
				out = append(out, ap)
			}
		}
		return out
	}

	refs := matches(av.ApproachType.HasLocalizer)
	for _, t := range []av.ApproachType{av.VisualApproach, av.VORApproach, av.NDBApproach, av.RNAVApproach, av.ChartedVisualApproach} {
		if extras := matches(func(at av.ApproachType) bool { return at == t }); len(extras) > 0 {
			refs = append(refs, extras...)
			break
		}
	}
	return refs
}

// visualATPAVolumeId returns the ATPA volume a visual approach to the runway
// flies in: the runway's own, or failing that the one of the first reference
// approach the visual is built from.
func visualATPAVolumeId(airport *av.Airport, runway string, references []*av.Approach) string {
	if _, ok := airport.ATPAVolumes[runway]; ok {
		return runway
	}
	for _, ref := range references {
		if ref.ATPAVolumeId != "" {
			return ref.ATPAVolumeId
		}
	}
	return ""
}

// stripProcedureTurns returns a deep copy of route with ProcedureTurn metadata
// cleared. Used when aggregating an ILS/Localizer reference's routes into a
// synthesized non-charted visual: a procedure turn would otherwise propagate
// into prepareForApproach / flyProcedureTurnIfNecessary.
func stripProcedureTurns(route av.WaypointArray) av.WaypointArray {
	out := route.Clone()
	for i := range out {
		if out[i].Extra != nil {
			out[i].Extra.ProcedureTurn = nil
		}
	}
	return out
}

// FollowTraffic describes a leader aircraft to sequence behind on a visual approach. Route (when
// non-empty) is the leader's own waypoints, used for tight in-trail spacing; Position alone is used
// as a join-point override on the reference approach.
type FollowTraffic struct {
	Position math.Point2LL
	Route    av.WaypointArray
}

// ClearedVisualApproach finalizes a non-charted visual approach clearance for
// the runway named on Nav.Approach.Assigned (a synthesized VisualApproach).
// Joining one of the visual's reference approaches, from the aircraft's
// heading or at a fix of its route, is an intercept like any other
// approach's; the visual's descent profile is laid along the reference from
// where the aircraft joins it. A pilot who joins no reference flies to the
// final themselves: behind the traffic they are following, or from the
// reference nearest them to a 3 nm final.
func (nav *Nav) ClearedVisualApproach(follow *FollowTraffic, lahsoRunway string) speech.CommandIntent {
	defer nav.record("ClearedVisualApproach", follow, lahsoRunway)()
	ap := nav.Approach.Assigned
	if ap == nil || ap.Type != av.VisualApproach {
		return speech.MakeUnableIntent("unable. We haven't been told to expect a visual approach")
	}
	intent := func(cancelHold bool) speech.CommandIntent {
		return speech.ClearedApproachIntent{
			Approach:    ap.FullName,
			CancelHold:  cancelHold,
			LAHSORunway: lahsoRunway,
		}
	}

	// Re-issued clearance on an already-cleared visual: just re-acknowledge,
	// don't recompute the route or altitude profile.
	if nav.Approach.Cleared {
		return intent(nav.applyClearedApproachState())
	}

	if follow == nil {
		if _, onHeading := nav.AssignedHeading(); onHeading {
			if _, ok := nav.selectInterceptCourse(wx.MakeStandardSampleForAltitude(nav.FlightState.Altitude)); ok {
				cancelHold := nav.applyClearedApproachState()
				nav.Approach.InterceptState = InitialHeading
				nav.Approach.NoPT = true
				// Until the aircraft is on the course, it descends by its
				// distance to the runway (visualDescentFromHeading).
				nav.releaseAltitudeForVisual()
				return intent(cancelHold)
			}
		} else if start, ok := nav.joinApproach(""); ok {
			cancelHold := nav.applyClearedApproachState()
			nav.Approach.InterceptState = NotIntercepting
			wps := nav.AssignedWaypoints()
			route := wps[start : len(wps)-1]
			nav.flyVisualRoute(slices.Concat(wps[:start],
				nav.visualRouteFromJoin(ap.Runway, nav.visualJoinAt(route, route[0].Location), nil)))
			return intent(cancelHold)
		}
	}

	wps := nav.visualRouteToFinal(follow)
	if wps == nil {
		return speech.MakeUnableIntent("unable, we can't make the visual approach from here")
	}
	cancelHold := nav.applyClearedApproachState()
	nav.flyVisualRoute(wps)
	return intent(cancelHold)
}

// visualRouteToFinal returns the route a pilot who joins no reference
// approach flies to the final: behind the traffic they are following, or
// from the point of a reference nearest them to the runway's 3 nm final;
// traffic that can't be followed from here, about to land or behind them,
// leaves them to find their own way in. It is nil if there is no such
// route.
func (nav *Nav) visualRouteToFinal(follow *FollowTraffic) []av.Waypoint {
	runway := nav.Approach.Assigned.Runway
	if follow != nil {
		if len(follow.Route) > 0 {
			if wps := nav.visualApproachRouteFollowingTraffic(runway, follow.Position, follow.Route); wps != nil {
				return wps
			}
		}
		if wps := nav.visualApproachRouteFromReferences(runway, &follow.Position, nav.Approach.VisualReferences); wps != nil {
			return wps
		}
	}
	return nav.visualApproachRouteFromReferences(runway, nil, nav.Approach.VisualReferences)
}

// flyVisualRoute puts the aircraft on wps, the route it flies the visual
// along, with the airport after it. The route carries the join and the
// descent profile, so a pending heading or direct goes, as do the altitude
// assignments the profile replaces.
func (nav *Nav) flyVisualRoute(wps []av.Waypoint) {
	nav.Waypoints = append(stripProcedureTurns(wps), nav.FlightState.ArrivalAirport)
	// A visual flown this way never has InterceptState=OnApproachCourse, so
	// OnApproach(), and thus MVAsApply / MSAW suppression, would otherwise
	// wait for the first route waypoint to be overflown.
	nav.Approach.PassedApproachFix = true
	nav.Heading = Heading{}
	nav.DeferredNavHeading = nil
	nav.releaseAltitudeForVisual()
}

// releaseAltitudeForVisual leaves the aircraft's altitude to the visual's
// descent profile. A controller-assigned descent is kept only when it sits
// above the profile's 3 nm final anchor (rwy.Elevation + 900), so "descend
// and maintain 3000" still completes at a normal rate; it ends once the
// aircraft gets there or the profile drops below it (crossAltitude).
func (nav *Nav) releaseAltitudeForVisual() {
	rwy, _ := nav.lookup().Runway(av.ICAOAirportCode(nav.FlightState.ArrivalAirport.Fix), nav.Approach.Assigned.Runway)
	profileFloor := float32(rwy.Elevation) + 900
	preserved := Altitude{}
	if a := nav.Altitude.Assigned; a != nil && *a < nav.FlightState.Altitude && *a >= profileFloor {
		preserved.Assigned = nav.Altitude.Assigned
		preserved.ActiveAssigned = nav.Altitude.ActiveAssigned
		preserved.ActivateAt = nav.Altitude.ActivateAt
	}
	nav.Altitude = preserved
}

// visualApproachRouteFollowingTraffic returns waypoints to sequence the
// aircraft tightly behind the leader, starting at the leader's current
// position and following its remaining route (minus the destination airport
// appended by nav.Waypoints). Returns nil when the geometry is infeasible:
// the leader is more than 90° off the follower's heading, or already inside
// 0.5 nm of its own threshold.
func (nav *Nav) visualApproachRouteFollowingTraffic(runway string, trafficPosition math.Point2LL,
	trafficRoute av.WaypointArray) []av.Waypoint {
	if len(trafficRoute) == 0 {
		return nil
	}

	// The leader's nav.Waypoints ends with the destination airport appended
	// after the threshold; drop the airport so the threshold is the route's
	// final point.
	trafficRoute = trafficRoute[:len(trafficRoute)-1]
	if len(trafficRoute) == 0 {
		return nil
	}

	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation
	bearingToTraffic := math.Heading2LL(nav.FlightState.Position, trafficPosition, nmPerLong)
	if math.HeadingDifference(bearingToTraffic, math.MagneticToTrue(nav.FlightState.Heading, magVar)) > 90 {
		return nil
	}
	if math.NMDistance2LLFast(trafficPosition, trafficRoute[len(trafficRoute)-1].Location, nmPerLong) <= 0.5 {
		return nil
	}

	join := av.Waypoint{
		Fix:      "_" + runway + "_FOLLOW_TRAFFIC",
		Location: trafficPosition,
	}
	join.SetOnApproach(true)

	return append(av.WaypointArray{join}, trafficRoute.Clone()...)
}

type visualApproachJoinPoint struct {
	route               av.WaypointArray
	segment             int
	segmentFraction     float32
	location            math.Point2LL
	distanceToThreshold float32
	lateralDistance     float32
	finalPoint          bool
	// From a downwind the aircraft flies a pattern to the join: on to
	// baseTurn, abeam the join, then the base leg to it. finalTurn marks a
	// join that is a point on the final rather than a fix. downwindEntry,
	// if set, is where an aircraft not on a downwind gets onto one first.
	baseTurn      math.Point2LL
	finalTurn     bool
	downwindEntry math.Point2LL
}

func (nav *Nav) projectOntoApproachRoutes(routes []av.WaypointArray, position math.Point2LL) (visualApproachJoinPoint, bool) {
	nmPerLong := nav.FlightState.NmPerLongitude
	posNM := math.LL2NM(position, nmPerLong)

	var best visualApproachJoinPoint
	found := false

	for _, route := range routes {
		if len(route) < 2 {
			continue
		}
		var tailDistance float32 // distance from route[i+1] to threshold
		for i := len(route) - 2; i >= 0; i-- {
			p0 := math.LL2NM(route[i].Location, nmPerLong)
			p1 := math.LL2NM(route[i+1].Location, nmPerLong)
			seg := math.Sub2f(p1, p0)
			segLen2 := math.Dot(seg, seg)
			if segLen2 == 0 {
				continue
			}

			t := math.Clamp(math.Dot(math.Sub2f(posNM, p0), seg)/segLen2, 0, 1)
			proj := math.Add2f(p0, math.Scale2f(seg, t))
			lateral := math.Distance2f(posNM, proj)
			distToThreshold := math.Distance2f(proj, p1) + tailDistance

			if !found || lateral < best.lateralDistance ||
				(lateral == best.lateralDistance && distToThreshold < best.distanceToThreshold) {
				best = visualApproachJoinPoint{
					route:               route,
					segment:             i,
					segmentFraction:     t,
					location:            math.NM2LL(proj, nmPerLong),
					distanceToThreshold: distToThreshold,
					lateralDistance:     lateral,
				}
				found = true
			}

			tailDistance += math.Distance2f(p0, p1)
		}
	}

	return best, found
}

// visualJoinAt returns the join of route at p, a point on it.
func (nav *Nav) visualJoinAt(route av.WaypointArray, p math.Point2LL) visualApproachJoinPoint {
	join, _ := nav.projectOntoApproachRoutes([]av.WaypointArray{route}, p)
	join.lateralDistance = 0
	join.finalPoint = join.distanceToThreshold <= 3.25
	return join
}

// visualRouteAt returns the route the visual is flown along from p, a
// point on course, to the threshold. On the final, that is the runway's
// extended centerline from p and the reference's fixes ahead on it; a
// reference whose own final is offset from the runway has its fixes
// before that joined only by its legs.
func (nav *Nav) visualRouteAt(course InterceptCourse, p math.Point2LL) []av.Waypoint {
	ap := nav.Approach.Assigned
	route := course.Route
	if course.Final {
		wps, idx := nav.finalWaypointsAhead(ap, p)
		nmPerLong, magVar := nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation
		out := math.Scale2f(math.HeadingVector(ap.FinalApproachCourse(nmPerLong, magVar)), -1)
		thr := math.LL2NM(ap.Threshold, nmPerLong)
		behind := math.Dot(math.Sub2f(math.LL2NM(p, nmPerLong), thr), out) + 1
		start := av.Waypoint{Fix: "_" + ap.Runway + "_EXTENDED", Location: math.NM2LL(math.Add2f(thr, math.Scale2f(out, behind)), nmPerLong)}
		route = slices.Concat(av.WaypointArray{start}, wps[idx:])
	}
	// The join is ahead of the aircraft, which may still be turning onto
	// the course, by as much as it takes to reach it without circling.
	join := nav.visualJoinAt(route, p)
	r := nav.currentTurnRadius()
	for range 8 {
		if math.HeadingDifference(math.Heading2LL(nav.FlightState.Position, join.location, nav.FlightState.NmPerLongitude),
			math.MagneticToTrue(nav.FlightState.Heading, nav.FlightState.MagneticVariation)) < 90 && nav.canTurnTo(join.location, r) {
			break
		}
		next, ok := visualRoutePointAtDistance(route, join.distanceToThreshold-r/2, nav.FlightState.NmPerLongitude)
		if !ok || next.distanceToThreshold <= 0 {
			break
		}
		next.finalPoint = next.distanceToThreshold <= 3.25
		join = next
	}
	return nav.visualRouteFromJoin(ap.Runway, join, nil)
}

func visualRoutePointAtDistance(route []av.Waypoint, distanceToThreshold float32, nmPerLong float32) (visualApproachJoinPoint, bool) {
	if len(route) < 2 {
		return visualApproachJoinPoint{}, false
	}

	dist := float32(0)
	for i := len(route) - 1; i > 0; i-- {
		p1 := math.LL2NM(route[i].Location, nmPerLong)
		p0 := math.LL2NM(route[i-1].Location, nmPerLong)
		segLen := math.Distance2f(p0, p1)
		if segLen == 0 {
			continue
		}
		if dist+segLen >= distanceToThreshold {
			t := (distanceToThreshold - dist) / segLen
			locNM := math.Lerp2f(t, p1, p0)
			return visualApproachJoinPoint{
				route:               route,
				segment:             i - 1,
				segmentFraction:     1 - t,
				location:            math.NM2LL(locNM, nmPerLong),
				distanceToThreshold: distanceToThreshold,
				finalPoint:          true,
			}, true
		}
		dist += segLen
	}

	return visualApproachJoinPoint{}, false
}

// selectVisualApproachRoute picks the join point and route across the
// supplied reference approaches for a pilot flying to the final themselves:
// where the traffic they are following is; from a downwind, a fix behind
// them to turn back to; or else their projection onto the laterally-closest
// reference. When the aircraft is pointed generally toward the runway the
// projection is promoted to the synthesized 3 nm final, skipping any
// intermediate route fixes; otherwise the projection is handed back so the
// route construction adds the dogleg fixes and the 3 nm final. It is nil if
// the projection would need a U-turn.
func (nav *Nav) selectVisualApproachRoute(followTraffic *math.Point2LL, references []*av.Approach) *visualApproachJoinPoint {
	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation
	pos := nav.FlightState.Position

	if followTraffic != nil {
		joinHeading := nav.FlightState.Heading
		if assignedHeading, ok := nav.AssignedHeading(); ok {
			joinHeading = assignedHeading
		}
		var best visualApproachJoinPoint
		var bestRef *av.Approach
		for _, ref := range references {
			tp, ok := nav.projectOntoApproachRoutes(ref.Waypoints, *followTraffic)
			if !ok {
				continue
			}
			if bestRef == nil || tp.lateralDistance < best.lateralDistance {
				best = tp
				bestRef = ref
			}
		}
		if bestRef == nil || best.distanceToThreshold <= 0.5 {
			return nil
		}
		bearingToJoin := math.Heading2LL(pos, *followTraffic, nmPerLong)
		if math.HeadingDifference(bearingToJoin, math.MagneticToTrue(joinHeading, magVar)) > 120 {
			return nil
		}
		return &best
	}

	// Flatten reference routes from all approaches into a single slice.
	var routes []av.WaypointArray
	for _, ref := range references {
		routes = append(routes, ref.Waypoints...)
	}

	tHdg := nav.intendedHeading()
	if join := nav.visualJoinBehind(routes, pos, tHdg, false); join != nil {
		return join
	}

	proj, ok := nav.projectOntoApproachRoutes(routes, pos)
	if !ok || proj.distanceToThreshold <= 0.5 {
		return nav.visualJoinByPattern(routes, tHdg)
	}
	// A join the aircraft has turned past, could only reach by circling,
	// or would arrive at with no room to line up, it comes around to
	// instead.
	headingDir := math.HeadingVector(tHdg)
	threshold := proj.route[len(proj.route)-1].Location
	thresholdDir := math.Normalize2f(math.Sub2f(math.LL2NM(threshold, nmPerLong), math.LL2NM(pos, nmPerLong)))
	if math.Dot(headingDir, thresholdDir) >= 0 {
		if finalPoint, ok := visualRoutePointAtDistance(proj.route, 3, nmPerLong); ok {
			if math.HeadingDifference(math.Heading2LL(pos, finalPoint.location, nmPerLong), tHdg) > 90 ||
				!nav.canFlyToJoin(finalPoint) {
				// Already inside the 3 nm final, the point is behind the
				// aircraft, and so is any chance of landing from here.
				if proj.distanceToThreshold <= 3 && proj.lateralDistance <= shortFinalLateral {
					return nil
				}
				return nav.visualJoinByPattern(routes, tHdg)
			}
			return &finalPoint
		}
	} else if math.HeadingDifference(math.Heading2LL(pos, proj.location, nmPerLong), tHdg) > 90 {
		return nav.visualJoinByPattern(routes, tHdg)
	}
	// Nor does the pilot fly out along a reference that carries them more
	// than legDetour farther than straight to the runway.
	direct := math.NMDistance2LL(nav.FlightState.Position, threshold)
	if !nav.canFlyToJoin(proj) ||
		math.NMDistance2LL(nav.FlightState.Position, proj.location)+proj.distanceToThreshold > direct+legDetour {
		return nav.visualJoinByPattern(routes, tHdg)
	}
	return &proj
}

// canFlyToJoin reports whether the aircraft can fly straight to join and
// on along its route: it can turn to the join without circling and lines
// up on the route after it.
func (nav *Nav) canFlyToJoin(join visualApproachJoinPoint) bool {
	fs := &nav.FlightState
	in := math.MagneticToTrue(fs.Heading, fs.MagneticVariation)
	if math.NMDistance2LL(fs.Position, join.location) > 0.01 {
		in = math.Heading2LL(fs.Position, join.location, fs.NmPerLongitude)
	}
	return nav.canTurnTo(join.location, nav.currentTurnRadius()) && nav.linesUpAt(join, in)
}

// linesUpAt reports whether an aircraft arriving at join on heading in
// lines up on its route from there. A turn onto the route through no more
// than a fly-by anticipates in full does; a sharper one, turning back
// toward the runway, needs room after it, a turn's width and a stabilized
// final.
func (nav *Nav) linesUpAt(join visualApproachJoinPoint, in math.TrueHeading) bool {
	if join.segment+1 >= len(join.route) {
		return true
	}
	out := math.Heading2LL(join.location, join.route[join.segment+1].Location, nav.FlightState.NmPerLongitude)
	return math.HeadingDifference(in, out) <= maxFlyByAngle ||
		join.distanceToThreshold >= 2*nav.currentTurnRadius()+nav.minimumFinal()
}

// currentTurnRadius returns the radius in nm of the aircraft's turns at its
// present altitude and speed.
func (nav *Nav) currentTurnRadius() float32 {
	return TurnRadius(nav.Perf, nav.TAS(wx.MakeStandardSampleForAltitude(nav.FlightState.Altitude).Temperature()))
}

// visualJoinByPattern returns the join for a pilot cleared for the visual
// who is neither on a downwind nor pointed toward the final, as when flying
// parallel to the runway, away from it, or beyond its far end. They get
// onto a downwind first: on the side of the final they are on, or the one
// they are crossing to, at least two turn radii out, and three turn radii
// farther out along it than they are, or farther if they would have to
// circle to reach that. From there they fly the pattern as from any
// downwind (visualJoinBehind). It is nil if the approach has no final
// course or the pattern has no join.
func (nav *Nav) visualJoinByPattern(routes []av.WaypointArray, hdg math.TrueHeading) *visualApproachJoinPoint {
	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong, magVar := fs.NmPerLongitude, fs.MagneticVariation
	line := ap.FinalApproachCourseLine(nmPerLong, magVar)
	if line[0] == line[1] {
		return nil
	}
	downwind := math.OppositeHeading(math.Heading2LL(line[0], line[1], nmPerLong))
	out := math.HeadingVector(downwind)
	left := [2]float32{-out[1], out[0]}
	r := nav.currentTurnRadius()

	thr := math.LL2NM(ap.Threshold, nmPerLong)
	rel := math.Sub2f(math.LL2NM(fs.Position, nmPerLong), thr)
	along, lateral := math.Dot(rel, out), math.Dot(rel, left)
	// The side is where the aircraft will be after a couple of turn radii
	// of flying on.
	side := float32(1)
	if lateral+2*r*math.Dot(math.HeadingVector(hdg), left) < 0 {
		side = -1
	}
	offset := side * max(side*lateral, 2*r)

	entryAt := func(along float32) math.Point2LL {
		return math.NM2LL(math.Add2f(thr, math.Add2f(math.Scale2f(out, along), math.Scale2f(left, offset))), nmPerLong)
	}
	entryAlong := along + 3*r
	entry := entryAt(entryAlong)
	for range 8 {
		if nav.canTurnTo(entry, r) {
			break
		}
		entryAlong += r
		entry = entryAt(entryAlong)
	}

	join := nav.visualJoinBehind(routes, entry, downwind, true)
	if join == nil {
		return nil
	}
	join.downwindEntry = entry
	return join
}

// shortFinalLateral is how far from the final, in nm, an aircraft within
// 3 nm of the threshold counts as on a short final.
const shortFinalLateral = 1.5

// ftPerNMOnGlideslope is the altitude a 3 degree glidepath loses per nm.
var ftPerNMOnGlideslope = math.Tan(math.Radians(float32(3))) * math.NauticalMilesToFeet

// How much farther out than the pattern needs a fix may be and still be
// joined instead of a point on the final; the least downwind left to fly
// for the turn onto base to be a waypoint of its own rather than made
// right away; and how far behind abeam a join may be and still be turned
// to right away.
const (
	downwindFixReach   = 2    // nm
	downwindMinLeg     = 0.5  // nm
	downwindAbeamSlack = 0.25 // nm
)

// downwindAngle is how far from the landing direction a heading has to be
// to count as a downwind even with the runway ahead of the beam.
const downwindAngle = 120 // degrees

// visualJoinBehind returns the join for a pilot flying away from the runway,
// as on a downwind, who has been cleared for the visual. They fly the
// pattern: on along the downwind to abeam the join, a base leg to it, and
// in from there. The join is abeam the aircraft unless the pattern has to
// be longer, for a stabilized final or to descend on the glidepath the
// whole way from where the aircraft is, in which case the downwind is
// extended. It is a fix of a reference when one lies within reach past
// that, taking the fix with the least flying to the threshold, and
// otherwise a point on the final itself. The pattern starts at from, flying
// hdg; that is where the aircraft is unless it gets onto a downwind first
// (visualJoinByPattern), in which case, with no join on the references, it
// joins the runway's extended centerline. It is nil when the pattern doesn't
// fly away from the runway or has no join ahead of it to turn base at.
func (nav *Nav) visualJoinBehind(routes []av.WaypointArray, from math.Point2LL, hdg math.TrueHeading,
	onDownwind bool) *visualApproachJoinPoint {
	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong, magVar := fs.NmPerLongitude, fs.MagneticVariation
	line := ap.FinalApproachCourseLine(nmPerLong, magVar)
	if line[0] == line[1] {
		return nil
	}
	landing := math.Heading2LL(line[0], line[1], nmPerLong)
	runwayAhead := math.HeadingDifference(hdg, math.Heading2LL(from, ap.Threshold, nmPerLong)) < 90
	if runwayAhead && math.HeadingDifference(hdg, landing) < downwindAngle {
		return nil
	}

	pos := math.LL2NM(from, nmPerLong)
	thr := math.LL2NM(ap.Threshold, nmPerLong)
	out := math.Scale2f(math.HeadingVector(landing), -1)
	alongOut := math.Dot(math.Sub2f(pos, thr), out)
	lateral := math.Abs(math.SignedPointLineDistance(pos, thr, math.Add2f(thr, out)))

	// The pattern from here is the rest of the downwind, the base leg and
	// the final; it has to be long enough to descend on the glidepath to
	// the threshold from the aircraft's altitude.
	rwy, _ := nav.lookup().Runway(av.ICAOAirportCode(fs.ArrivalAirport.Fix), ap.Runway)
	descent := (fs.Altitude - float32(rwy.Elevation+rwy.ThresholdCrossingHeight)) / ftPerNMOnGlideslope
	minRemaining := max(alongOut, 3, nav.minimumFinal(), (descent+alongOut-lateral)/2)
	hdgVec := math.HeadingVector(hdg)
	r := nav.currentTurnRadius()

	// pattern lays the downwind's end abeam the join into it and returns
	// the flying to the threshold by way of it; false if the join is
	// behind the aircraft along the downwind, so close abeam that the
	// turn toward it would circle, or one the base leg doesn't line up
	// from, as when the downwind converges on the final or runs along it.
	// A join the aircraft is already abeam is turned to right away.
	pattern := func(join *visualApproachJoinPoint, remaining float32) (float32, bool) {
		j := math.LL2NM(join.location, nmPerLong)
		downwind := math.Dot(math.Sub2f(j, pos), hdgVec)
		if downwind < -downwindAbeamSlack {
			return 0, false
		}
		turn := math.Add2f(pos, math.Scale2f(hdgVec, max(downwind, 0)))
		base := hdg
		if math.Distance2f(turn, j) > 0.01 {
			base = math.VectorHeading(math.Sub2f(j, turn))
		}
		if insideTurnCircle(turn, hdgVec, j, r) || !nav.linesUpAt(*join, base) {
			return 0, false
		}
		if downwind >= downwindMinLeg {
			join.baseTurn = math.NM2LL(turn, nmPerLong)
		}
		return max(downwind, 0) + math.Distance2f(turn, j) + remaining, true
	}

	var best *visualApproachJoinPoint
	var bestTrack float32
	for _, route := range routes {
		remaining := float32(0)
		for i := len(route) - 2; i >= 0; i-- {
			remaining += math.NMDistance2LL(route[i].Location, route[i+1].Location)
			if remaining < minRemaining || remaining > minRemaining+downwindFixReach {
				continue
			}
			join := nav.visualJoinAt(route, route[i].Location)
			if track, ok := pattern(&join, remaining); ok && (best == nil || track < bestTrack) {
				best, bestTrack = &join, track
			}
		}
	}
	if best != nil {
		return best
	}

	for _, route := range routes {
		pt, ok := visualRoutePointAtDistance(route, minRemaining, nmPerLong)
		if !ok {
			continue
		}
		p := math.LL2NM(pt.location, nmPerLong)
		if math.Abs(math.SignedPointLineDistance(p, thr, math.Add2f(thr, out))) >= av.FinalCourseLateralLeeway {
			continue
		}
		if _, ok := pattern(&pt, minRemaining); ok {
			pt.finalTurn = true
			pt.finalPoint = pt.distanceToThreshold <= 3.25
			return &pt
		}
	}

	// A visual's final is the runway's extended centerline, which runs out
	// as far as it needs to. From where the aircraft is, the references'
	// own way in is taken instead (selectVisualApproachRoute).
	if !onDownwind {
		return nil
	}
	i := slices.IndexFunc(routes, func(route av.WaypointArray) bool {
		return len(route) > 0 && math.NMDistance2LL(route[len(route)-1].Location, ap.Threshold) < 0.1
	})
	if i == -1 {
		return nil
	}
	extended := av.WaypointArray{
		{Fix: "_" + ap.Runway + "_EXTENDED", Location: math.NM2LL(math.Add2f(thr, math.Scale2f(out, minRemaining+1)), nmPerLong)},
		routes[i][len(routes[i])-1],
	}
	pt, _ := visualRoutePointAtDistance(extended, minRemaining, nmPerLong)
	if _, ok := pattern(&pt, minRemaining); !ok {
		return nil
	}
	pt.finalTurn = true
	pt.finalPoint = pt.distanceToThreshold <= 3.25
	return &pt
}

func (nav *Nav) visualApproachRouteFromReferences(runway string, followTraffic *math.Point2LL, references []*av.Approach) []av.Waypoint {
	joinPoint := nav.selectVisualApproachRoute(followTraffic, references)
	if joinPoint == nil || len(joinPoint.route) < 2 {
		return nil
	}
	return nav.visualRouteFromJoin(runway, *joinPoint, followTraffic)
}

// visualRouteFromJoin returns the route the visual is flown along from
// joinPoint on a reference route to the runway's threshold, carrying the
// descent profile: a 3 degree glidepath from the top of descent through a
// 3 nm final at 900 feet above the runway. followTraffic, if given, is
// where the traffic being followed is, which the join takes the place of.
func (nav *Nav) visualRouteFromJoin(runway string, joinPoint visualApproachJoinPoint, followTraffic *math.Point2LL) []av.Waypoint {
	nmPerLong := nav.FlightState.NmPerLongitude
	rwy, _ := nav.lookup().Runway(av.ICAOAirportCode(nav.FlightState.ArrivalAirport.Fix), runway)
	finalPoint, hasFinalPoint := visualRoutePointAtDistance(joinPoint.route, 3, nmPerLong)
	thresholdAlt := rwy.Elevation + rwy.ThresholdCrossingHeight
	final3nmAlt := float32(rwy.Elevation) + 900

	// Top-of-descent altitude: continue any in-progress controller-assigned descent (so "descend
	// and maintain 3000" still completes), otherwise hold current.  Reference Altitude.Assigned if
	// set so that we place the TOD for the altitude the aircraft is heading for, not where it is
	// currently.
	todAlt := nav.FlightState.Altitude
	if a := nav.Altitude.Assigned; a != nil && *a < todAlt {
		todAlt = *a
	}
	// Distance from threshold for a 3° glideslope crossing the threshold at TCH.
	todDist := (todAlt - float32(thresholdAlt)) / ftPerNMOnGlideslope

	join := av.Waypoint{Location: joinPoint.location}
	switch {
	case followTraffic != nil:
		join.Fix = "_" + runway + "_FOLLOW_TRAFFIC"
		join.Location = *followTraffic
	case joinPoint.finalTurn:
		join.Fix = "_" + runway + "_FINAL_TURN"
	case joinPoint.lateralDistance == 0 && joinPoint.segmentFraction == 0:
		// The join is at a fix of the route: fly the fix itself, so that
		// its restrictions come along rather than being displaced by a
		// bare join point.
		join = joinPoint.route[joinPoint.segment : joinPoint.segment+1].Clone()[0]
		join.SetFAF(false)
	case joinPoint.lateralDistance == 0:
		join.Fix = "_" + runway + "_INTERCEPT"
	default:
		join.Fix = "_" + runway + "_PROJECTION"
	}
	join.SetOnApproach(true)
	if joinPoint.finalPoint {
		join.Fix = "_" + runway + "_3NM_FINAL"
		join.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(final3nmAlt))
		join.SetFAF(true)
	}
	wps := []av.Waypoint{join}
	if !joinPoint.baseTurn.IsZero() {
		baseTurn := av.Waypoint{Fix: "_" + runway + "_BASE_TURN", Location: joinPoint.baseTurn}
		baseTurn.SetOnApproach(true)
		wps = []av.Waypoint{baseTurn, join}
	}
	if !joinPoint.downwindEntry.IsZero() {
		entry := av.Waypoint{Fix: "_" + runway + "_DOWNWIND", Location: joinPoint.downwindEntry}
		entry.SetOnApproach(true)
		wps = slices.Concat([]av.Waypoint{entry}, wps)
	}

	start := joinPoint.segment + 1
	if followTraffic != nil && joinPoint.segmentFraction <= 1e-4 &&
		math.NMDistance2LL(*followTraffic, joinPoint.route[joinPoint.segment].Location) > 0.05 {
		start = joinPoint.segment
	}
	if hasFinalPoint && joinPoint.distanceToThreshold > 3.25 {
		// Visual approaches use the same pure-geometric descent the post-FAF
		// path uses, so the FAF marker lives on the first waypoint that has a
		// glideslope-anchored altitude restriction: the join when the aircraft
		// is past natural TOD, otherwise _3NM_FINAL.
		joinIsFAF := todDist >= joinPoint.distanceToThreshold

		if joinIsFAF {
			glideAlt := float32(thresholdAlt) + ftPerNMOnGlideslope*joinPoint.distanceToThreshold
			wps[len(wps)-1].SetFAF(true)
			wps[len(wps)-1].SetAltitudeRestriction(av.MakeAtAltitudeRestriction(glideAlt))
		}

		// Reference dogleg fixes between join and 3-NM final, with FAF flags cleared (an
		// inherited FAF would prematurely flip nav.Approach.PassedFAF).
		intermediates := func(lo, hi int) []av.Waypoint {
			if lo >= hi {
				return nil
			}
			copied := joinPoint.route[lo:hi].Clone()
			for i := range copied {
				copied[i].SetFAF(false)
			}
			return copied
		}

		wps = append(wps, intermediates(start, finalPoint.segment+1)...)

		finalWp := av.Waypoint{Fix: "_" + runway + "_3NM_FINAL", Location: finalPoint.location}
		finalWp.SetOnApproach(true)
		finalWp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(final3nmAlt))
		if !joinIsFAF {
			finalWp.SetFAF(true)
		}
		wps = append(wps, finalWp)
		start = finalPoint.segment + 1
	}

	if start < len(joinPoint.route) {
		wps = append(wps, joinPoint.route[start:].Clone()...)
	}

	// A few things at the last waypoint.
	last := &wps[len(wps)-1]
	last.SetOnApproach(true)
	last.MergeActions(av.WaypointActions{Land: true})
	last.SetFlyOver(true)
	if last.AltitudeRestriction() == nil {
		last.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(thresholdAlt)))
	}

	// Only the threshold is flown over; a reference's fly-over fix, as
	// for its procedure turn, inside the aircraft's turn would be circled.
	for i := range wps {
		wps[i].SetOnApproach(true)
		wps[i].SetFlyOver(i == len(wps)-1)
	}
	return wps
}

// keepsArcOntoChartedVisual keeps an active arc that already joins the
// charted visual at its endpoint, splicing the approach in there, unless a
// newer instruction supersedes the arc. It reports whether it did.
func (nav *Nav) keepsArcOntoChartedVisual(joinFix string) bool {
	if nav.Heading.Arc == nil || nav.DeferredNavHeading != nil || joinFix != "" || len(nav.Waypoints) == 0 {
		return false
	}
	route, idx := approachRouteThrough(nav.Approach.Assigned, nav.Waypoints[0].Fix)
	if route == nil {
		return false
	}
	nav.Waypoints = nav.spliceApproachRoute(nav.Waypoints, 0, route[idx:])
	nav.Approach.PassedApproachFix = true
	return true
}

// chartedJoinMaxLandingOff is the most off the runway, in degrees, that a
// pilot flying a charted visual on their own makes for its threshold.
const chartedJoinMaxLandingOff = 20

// chartedVisualRouteFromHere returns the rest of the charted visual from the
// fix of it that a pilot who has the chart's landmarks in sight makes for
// on their own. A fix qualifies if it is ahead of the aircraft and it can
// turn to it without circling. One where the aircraft would turn back onto
// the chart, through more than 90 degrees, also needs the chart to leave
// room after it to line up: a turn's width and a stabilized final. The
// threshold itself qualifies only with the aircraft about lined up with the
// runway. The pilot joins each route at its first fix that qualifies and
// that leaves at most legDetour more to fly than the shortest way onto the
// chart, taking the route whose fix is closest to the nose. It is nil if no
// fix qualifies.
func (nav *Nav) chartedVisualRouteFromHere(wxs wx.Sample) av.WaypointArray {
	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong := fs.NmPerLongitude
	hdg := math.MagneticToTrue(fs.Heading, fs.MagneticVariation)
	landing := ap.FinalApproachCourse(nmPerLong, fs.MagneticVariation)
	r := TurnRadius(nav.Perf, nav.TAS(wxs.Temperature()))

	// remaining is the flying along the chart from route[i] to the runway.
	qualifies := func(route av.WaypointArray, i int, remaining float32) bool {
		bearing := math.Heading2LL(fs.Position, route[i].Location, nmPerLong)
		if math.HeadingDifference(bearing, hdg) > 90 || !nav.canTurnTo(route[i].Location, r) {
			return false
		}
		if i+1 == len(route) {
			return math.HeadingDifference(bearing, landing) <= chartedJoinMaxLandingOff
		}
		out := math.Heading2LL(route[i].Location, route[i+1].Location, nmPerLong)
		return math.HeadingDifference(bearing, out) <= 90 || remaining >= 2*r+nav.minimumFinal()
	}

	type join struct {
		route   av.WaypointArray // from the fix to the runway
		dist    float32          // to fly to the runway by way of the fix
		offNose float32
	}
	// Each route's qualifying fixes, in order along it.
	routeJoins := make([][]join, len(ap.Waypoints))
	shortest := math.Infinity
	for ri, route := range ap.Waypoints {
		var remaining float32
		for i, wp := range slices.Backward(route) {
			if i+1 < len(route) {
				remaining += math.NMDistance2LL(wp.Location, route[i+1].Location)
			}
			if !qualifies(route, i, remaining) {
				continue
			}
			dist := math.NMDistance2LL(fs.Position, wp.Location) + remaining
			offNose := math.HeadingDifference(math.Heading2LL(fs.Position, wp.Location, nmPerLong), hdg)
			routeJoins[ri] = append(routeJoins[ri], join{route: route[i:], dist: dist, offNose: offNose})
			shortest = min(shortest, dist)
		}
		slices.Reverse(routeJoins[ri])
	}

	var best *join
	for _, joins := range routeJoins {
		i := slices.IndexFunc(joins, func(j join) bool { return j.dist <= shortest+legDetour })
		if i != -1 && (best == nil || joins[i].offNose < best.offNose) {
			best = &joins[i]
		}
	}
	if best == nil {
		return nil
	}
	return best.route
}
