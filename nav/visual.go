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
)

// Visual approaches: the reference approaches a non-charted visual is
// flown by, the route synthesized for it when the aircraft is cleared, and
// joining a charted visual where the aircraft's instructions meet it.

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

// visualReferenceForFix returns the ILS or Localizer reference approach
// (from VisualReferences) that contains the given fix, or nil if none.
// Used under a synthesized visual to identify when the aircraft has been
// committed to an ILS-style route, so that a later cleared visual flies the
// localizer route rather than a synthesized curved descent.
func (nav *Nav) visualReferenceForFix(fix string) *av.Approach {
	for _, ref := range nav.Approach.VisualReferences {
		if !ref.Type.HasLocalizer() {
			continue
		}
		if route, _ := approachRouteThrough(ref, fix); route != nil {
			return ref
		}
	}
	return nil
}

// FollowTraffic describes a leader aircraft to sequence behind on a visual approach. Route (when
// non-empty) is the leader's own waypoints, used for tight in-trail spacing; Position alone is used
// as a join-point override on the reference approach.
type FollowTraffic struct {
	Position math.Point2LL
	Route    av.WaypointArray
}

// ClearedVisualApproach finalizes a non-charted visual approach clearance for
// the runway named on Nav.Approach.Assigned (a synthesized VisualApproach). It
// picks the route the aircraft will fly:
//
//   - If the aircraft has been committed to an ILS/Localizer reference (via
//     "direct {fix}" or "at {fix} intercept the localizer"), the reference's
//     own routes are installed so the localizer altitudes/centerline take
//     effect.
//   - Otherwise, when a leader's route is supplied via follow.Route, tight
//     in-trail sequencing along that route is attempted first.
//   - Otherwise, a synthetic descent profile (TOD/_3NM_FINAL anchors) is
//     constructed from the visual references.
func (nav *Nav) ClearedVisualApproach(follow *FollowTraffic, lahsoRunway string) speech.CommandIntent {
	defer nav.record("ClearedVisualApproach", follow, lahsoRunway)()
	ap := nav.Approach.Assigned
	if ap == nil || ap.Type != av.VisualApproach {
		return speech.MakeUnableIntent("unable. We haven't been told to expect a visual approach")
	}
	runway := ap.Runway

	// Re-issued clearance on an already-cleared visual: just re-acknowledge,
	// don't recompute the route or altitude profile.
	if nav.Approach.Cleared {
		cancelHold := nav.applyClearedApproachState()
		return speech.ClearedApproachIntent{
			Approach:    ap.FullName,
			CancelHold:  cancelHold,
			LAHSORunway: lahsoRunway,
		}
	}

	// Aircraft has been routed onto an ILS/Localizer reference; fly that.
	if ref := nav.Approach.InterceptedReference; ref != nil && ref.Type.HasLocalizer() {
		// Deep-copy + strip procedure turns: don't alias airport.Approaches[id].Waypoints,
		// and a cleared visual shouldn't fly the underlying ILS's PT.
		out := make([]av.WaypointArray, len(ref.Waypoints))
		for i, route := range ref.Waypoints {
			out[i] = stripProcedureTurns(route)
		}
		ap.Waypoints = out
		cancelHold := nav.applyClearedApproachState()
		// Vectoring since the last capture invalidates any prior intercept
		// progress; rearm the state machine so ApproachHeading recaptures
		// against the now-installed localizer geometry.
		if _, onHeading := nav.AssignedHeading(); onHeading {
			nav.Approach.InterceptState = InitialHeading
			nav.Approach.NoPT = true
		}
		if nav.Approach.PassedApproachFix || nav.Approach.InterceptState == OnApproachCourse {
			nav.clearAltitudeForApproach()
			if nav.Approach.InterceptState == OnApproachCourse {
				nav.Approach.NoPT = true
			}
		}
		return speech.ClearedApproachIntent{
			Approach:    ap.FullName,
			CancelHold:  cancelHold,
			LAHSORunway: lahsoRunway,
		}
	}

	// Otherwise, construct the route either from the leader (in-trail) or a
	// synthesized descent profile across the references.
	var wps []av.Waypoint
	if follow != nil && len(follow.Route) > 0 {
		wps = nav.visualApproachRouteFollowingTraffic(runway, follow.Position, follow.Route)
	}
	if wps == nil {
		var joinPos *math.Point2LL
		if follow != nil {
			joinPos = &follow.Position
		}
		wps = nav.visualApproachRouteFromReferences(runway, joinPos, nav.Approach.VisualReferences)
	}
	if wps == nil {
		return speech.MakeUnableIntent("unable, we don't know runway " + runway)
	}

	// The synthesized route is built from raw reference / leader waypoints,
	// which may still have procedure turns...
	wps = stripProcedureTurns(wps)

	cancelHold := nav.applyClearedApproachState()
	// Synthesized visuals never set InterceptState=OnApproachCourse, so OnApproach()
	// — and thus MVAsApply / MSAW suppression — would otherwise wait for the first
	// route waypoint to be overflown.
	nav.Approach.PassedApproachFix = true
	nav.Waypoints = append(wps, nav.FlightState.ArrivalAirport)

	// The controller's instruction — an assigned heading, or a direct the pilot
	// hasn't turned for yet — chose the join point above, so the precomputed
	// route now carries it and the heading nav state can go. Preserve a
	// controller-assigned descent only when it sits above the visual profile's
	// _3NM_FINAL anchor (rwy.Elevation + 900) so "descend and maintain 3000"
	// still completes before the aircraft holds for the TOD; passApproachFix
	// clears the assignment at each cleared-approach waypoint crossing after
	// that.
	nav.Heading = Heading{}
	nav.DeferredNavHeading = nil
	rwy, _ := nav.lookup().Runway(av.ICAOAirportCode(nav.FlightState.ArrivalAirport.Fix), runway)
	profileFloor := float32(rwy.Elevation) + 900
	preserved := Altitude{}
	if a := nav.Altitude.Assigned; a != nil && *a < nav.FlightState.Altitude && *a >= profileFloor {
		preserved.Assigned = nav.Altitude.Assigned
		preserved.ActiveAssigned = nav.Altitude.ActiveAssigned
		preserved.ActivateAt = nav.Altitude.ActivateAt
	}
	nav.Altitude = preserved

	return speech.ClearedApproachIntent{
		Approach:    ap.FullName,
		CancelHold:  cancelHold,
		LAHSORunway: lahsoRunway,
	}
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

// visualJoinFromInstructions finds where the aircraft's current instructions
// meet one of routes: at the next waypoint of its assigned route when that
// lies on one of them, otherwise at a forward intercept of the heading its
// latest instruction implies. Returns nil when neither yields a viable join.
func (nav *Nav) visualJoinFromInstructions(routes []av.WaypointArray) *visualApproachJoinPoint {
	nmPerLong := nav.FlightState.NmPerLongitude
	pos := nav.FlightState.Position

	// Stabilized-approach criterion: heavy/large/medium aircraft (CWT A-G)
	// shouldn't intercept within 3 nm of the threshold; small aircraft can.
	stabilizedRequired := len(nav.Perf.Category.CWT) > 0 &&
		nav.Perf.Category.CWT[0] >= 'A' && nav.Perf.Category.CWT[0] <= 'G'

	// If the aircraft's next waypoint lies on one of the routes, the route
	// the controller gave it is where it joins; no ray geometry needed. An
	// assigned heading supersedes a (stale) route, though. The next waypoint
	// is the first one far enough away to matter: the aircraft sequences
	// straight past any it is sitting on top of.
	if _, ok := nav.AssignedHeading(); !ok {
		wps := nav.AssignedWaypoints()
		far := func(wp av.Waypoint) bool {
			return math.NMDistance2LLFast(pos, wp.Location, nmPerLong) > 0.5
		}
		if i := slices.IndexFunc(wps, far); i != -1 {
			if proj, ok := nav.projectOntoApproachRoutes(routes, wps[i].Location); ok &&
				proj.lateralDistance <= 0.25 && proj.distanceToThreshold > 0.5 &&
				!(stabilizedRequired && proj.distanceToThreshold < 3) {
				// The instruction names a point on the route, so this is an
				// exact join there, not a projection from off to the side.
				proj.lateralDistance = 0
				proj.finalPoint = proj.distanceToThreshold <= 3.25
				return &proj
			}
		}
	}

	// Forward ray intercept along the instructed heading; take the closest viable hit.
	for _, hit := range nav.forwardRouteIntercepts(nav.intendedHeading(), routes) {
		route := routes[hit.RouteIndex]
		distToThreshold := math.NMDistance2LL(hit.Location, route[hit.Index+1].Location)
		for i := hit.Index + 1; i < len(route)-1; i++ {
			distToThreshold += math.NMDistance2LL(route[i].Location, route[i+1].Location)
		}

		if stabilizedRequired && distToThreshold < 3 {
			continue
		}
		return &visualApproachJoinPoint{
			route:               route,
			segment:             hit.Index,
			segmentFraction:     hit.SegT,
			location:            hit.Location,
			distanceToThreshold: distToThreshold,
			// When the intercept is at/inside the 3-NM final, treat the
			// join as the FAF — there's no separate stabilized segment
			// to insert between the join and the threshold.
			finalPoint: distToThreshold <= 3.25,
		}
	}
	return nil
}

// selectVisualApproachRoute picks the join point and route across the supplied reference
// approaches. The primary cases are a join at the next waypoint of the aircraft's assigned
// route or a forward heading-ray intercept; failing those, the aircraft is sent to a
// synthesized 3-NM final on the laterally-closest reference.
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

	if join := nav.visualJoinFromInstructions(routes); join != nil {
		return join
	}

	// No route or forward-heading join. Project onto the laterally-closest reference. If the
	// aircraft is pointed generally toward the runway, promote the join to the synthesized 3-NM
	// final (skipping any intermediate route fixes); otherwise hand back the projection so the
	// route construction layer adds the dogleg fixes and the 3-NM final.  If the projection would
	// require a U-turn (bearing > 90° from heading), give up and return unable.
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
	nmPerLong := nav.FlightState.NmPerLongitude

	joinPoint := nav.selectVisualApproachRoute(followTraffic, references)
	if joinPoint == nil || len(joinPoint.route) < 2 {
		return nil
	}

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

// prepareForChartedVisual sets up the aircraft to fly the assigned charted
// visual from where it joins one of the approach's routes. joinFix has the
// same meaning as in joinApproach.
func (nav *Nav) prepareForChartedVisual(joinFix string) speech.CommandIntent {
	// An active arc already joins the approach at its endpoint. Keep its
	// guidance until that fix is sequenced, unless a newer instruction supersedes it.
	if nav.Heading.Arc != nil && nav.DeferredNavHeading == nil && joinFix == "" && len(nav.Waypoints) > 0 {
		if route, idx := approachRouteThrough(nav.Approach.Assigned, nav.Waypoints[0].Fix); route != nil {
			nav.Waypoints = nav.spliceApproachRoute(nav.Waypoints, 0, route[idx:])
			nav.Approach.PassedApproachFix = true
			return nil
		}
	}

	routes := nav.Approach.Assigned.Waypoints
	pos := nav.FlightState.Position
	nmPerLong := nav.FlightState.NmPerLongitude

	// The shared visual join logic finds where the aircraft's instructions
	// meet the chart. Unlike an uncharted visual there is no projection
	// fallback past that: the published track has to be joined where it is
	// charted, so a pilot who isn't pointed at it answers unable.
	var wi av.WaypointArray
	if route, idx := approachRouteThrough(nav.Approach.Assigned, joinFix); route != nil {
		wi = route[idx+1:]
	} else if join := nav.visualJoinFromInstructions(routes); join != nil {
		if join.segmentFraction == 0 {
			// The join is the charted fix that starts the segment, or is
			// upstream of it; fly the fix itself so that its altitude and
			// speed restrictions come along rather than being displaced by a
			// bare intercept point.
			wi = join.route[join.segment:]
		} else {
			wi = append(av.WaypointArray{{Fix: "intercept", Location: join.location}},
				join.route[join.segment+1:]...)
		}
	} else {
		// No intercept. Fall back to each route's first waypoint whose
		// bearing is within 30° of the instructed heading — lets a pilot
		// already pointed at a chart waypoint join there directly. Where
		// several routes have one, the pilot takes the one closest to the
		// nose.
		hdg := nav.intendedHeading()
		offNose := func(wp av.Waypoint) float32 {
			return math.HeadingDifference(math.Heading2LL(pos, wp.Location, nmPerLong), hdg)
		}
		var minOffNose float32
		for _, route := range routes {
			i := slices.IndexFunc(route, func(wp av.Waypoint) bool { return offNose(wp) < 30 })
			if i != -1 && (wi == nil || offNose(route[i]) < minOffNose) {
				wi, minOffNose = route[i:], offNose(route[i])
			}
		}
	}

	if wi == nil {
		return speech.MakeUnableIntent("unable. We are not on course to intercept the approach")
	}

	nav.Waypoints = append(wi.Clone(), nav.FlightState.ArrivalAirport)
	nav.Heading = Heading{}
	nav.DeferredNavHeading = nil
	nav.Approach.PassedApproachFix = true // allow descent
	return nil
}
