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
			if course, ok := nav.selectInterceptCourse(wx.MakeStandardSampleForAltitude(nav.FlightState.Altitude)); ok {
				cancelHold := nav.applyClearedApproachState()
				nav.Approach.InterceptState = InitialHeading
				nav.Approach.NoPT = true
				// Until the aircraft is on the course, the profile from
				// where it is expected to join gives it its descent.
				if !course.Join.IsZero() {
					nav.Waypoints = append(nav.visualRouteAt(course, course.Join), nav.FlightState.ArrivalAirport)
				}
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
		return speech.MakeUnableIntent("unable, we don't know runway " + ap.Runway)
	}
	cancelHold := nav.applyClearedApproachState()
	nav.flyVisualRoute(wps)
	return intent(cancelHold)
}

// visualRouteToFinal returns the route a pilot who joins no reference
// approach flies to the final: behind the traffic they are following, or
// from the point of a reference nearest them to the runway's 3 nm final. It
// is nil if there is no such route.
func (nav *Nav) visualRouteToFinal(follow *FollowTraffic) []av.Waypoint {
	runway := nav.Approach.Assigned.Runway
	if follow != nil && len(follow.Route) > 0 {
		if wps := nav.visualApproachRouteFollowingTraffic(runway, follow.Position, follow.Route); wps != nil {
			return wps
		}
	}
	var joinPos *math.Point2LL
	if follow != nil {
		joinPos = &follow.Position
	}
	return nav.visualApproachRouteFromReferences(runway, joinPos, nav.Approach.VisualReferences)
}

// flyVisualRoute puts the aircraft on wps, the route it flies the visual
// along, with the airport after it. The route carries the join and the
// descent profile, so a pending heading or direct goes. A
// controller-assigned descent is kept only when it sits above the profile's
// 3 nm final anchor (rwy.Elevation + 900), so "descend and maintain 3000"
// still completes before the aircraft holds for the TOD; passApproachFix
// clears the assignment at each cleared-approach waypoint crossing after
// that.
func (nav *Nav) flyVisualRoute(wps []av.Waypoint) {
	nav.Waypoints = append(stripProcedureTurns(wps), nav.FlightState.ArrivalAirport)
	// A visual flown this way never has InterceptState=OnApproachCourse, so
	// OnApproach(), and thus MVAsApply / MSAW suppression, would otherwise
	// wait for the first route waypoint to be overflown.
	nav.Approach.PassedApproachFix = true
	nav.Heading = Heading{}
	nav.DeferredNavHeading = nil

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
// point on course, to the threshold.
func (nav *Nav) visualRouteAt(course InterceptCourse, p math.Point2LL) []av.Waypoint {
	ap := nav.Approach.Assigned
	route := course.Route
	if course.Final {
		route, _ = nav.finalWaypointsAhead(ap, p)
	}
	return nav.visualRouteFromJoin(ap.Runway, nav.visualJoinAt(route, p), nil)
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
// where the traffic they are following is, or else their projection onto the
// laterally-closest reference. When the aircraft is pointed generally toward
// the runway the join is promoted to the synthesized 3 nm final, skipping
// any intermediate route fixes; otherwise the projection is handed back so
// the route construction adds the dogleg fixes and the 3 nm final. It is nil
// if the projection would need a U-turn.
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

	proj, ok := nav.projectOntoApproachRoutes(routes, pos)
	if !ok || proj.distanceToThreshold <= 0.5 {
		return nil
	}
	tHdg := nav.intendedHeading()
	headingDir := math.HeadingVector(tHdg)
	threshold := proj.route[len(proj.route)-1].Location
	thresholdDir := math.Normalize2f(math.Sub2f(math.LL2NM(threshold, nmPerLong), math.LL2NM(pos, nmPerLong)))
	if math.Dot(headingDir, thresholdDir) >= 0 {
		if finalPoint, ok := visualRoutePointAtDistance(proj.route, 3, nmPerLong); ok {
			return &finalPoint
		}
		return &proj
	}
	bearingToProj := math.Heading2LL(pos, proj.location, nmPerLong)
	if math.HeadingDifference(bearingToProj, tHdg) > 90 {
		return nil
	}
	return &proj
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
	ftPerNMOnGlideslope := math.Tan(math.Radians(float32(3))) * math.NauticalMilesToFeet
	todDist := (todAlt - float32(thresholdAlt)) / ftPerNMOnGlideslope

	join := av.Waypoint{Location: joinPoint.location}
	switch {
	case followTraffic != nil:
		join.Fix = "_" + runway + "_FOLLOW_TRAFFIC"
		join.Location = *followTraffic
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
			wps[0].SetFAF(true)
			wps[0].SetAltitudeRestriction(av.MakeAtAltitudeRestriction(glideAlt))
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

	for i := range wps {
		wps[i].SetOnApproach(true)
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

// chartedVisualRouteAhead returns the charted visual's route from the first
// fix of one of its routes that is within 30 degrees of the aircraft's nose,
// taking the one closest to it. This is the join of a pilot pointed at a
// fix of the chart whose heading intercepts none of its legs. It is nil if
// there is no such fix.
func (nav *Nav) chartedVisualRouteAhead() av.WaypointArray {
	pos := nav.FlightState.Position
	nmPerLong := nav.FlightState.NmPerLongitude
	hdg := nav.intendedHeading()
	offNose := func(wp av.Waypoint) float32 {
		return math.HeadingDifference(math.Heading2LL(pos, wp.Location, nmPerLong), hdg)
	}
	var wi av.WaypointArray
	var minOffNose float32
	for _, route := range nav.Approach.Assigned.Waypoints {
		i := slices.IndexFunc(route, func(wp av.Waypoint) bool { return offNose(wp) < 30 })
		if i != -1 && (wi == nil || offNose(route[i]) < minOffNose) {
			wi, minOffNose = route[i:], offNose(route[i])
		}
	}
	return wi
}
