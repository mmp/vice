// nav/intercept.go
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

// Joining the approach course from a heading: the course the heading takes
// the aircraft onto, the intercept the controller sets up, the turn onto
// the course and its capture, and the recovery or the request for vectors
// when the aircraft overshoots.

type InterceptState int

const (
	NotIntercepting InterceptState = iota
	InitialHeading
	TurningToJoin
	OnApproachCourse
)

// InterceptCourse is a course the aircraft joins from a heading: the final
// approach course, or a leg of one of the approach's routes.
type InterceptCourse struct {
	Line  [2]math.Point2LL // two points along the course, in the direction of flight
	Final bool             // the final approach course rather than a leg
	// Reach is how far back from the end of its line the final extends; a
	// leg runs between its line's points.
	Reach float32
	// Route is the route the leg is on and Index the leg's position in it.
	// The final's route is found once the aircraft is on it.
	Route av.WaypointArray
	Index int
	// Join is where the aircraft's path was predicted to meet the course
	// when the course was chosen; it is zero if the path doesn't meet it
	// and the aircraft is to be turned back onto it instead.
	Join math.Point2LL
}

func (c InterceptCourse) course(nmPerLong float32) math.TrueHeading {
	return math.Heading2LL(c.Line[0], c.Line[1], nmPerLong)
}

// extent returns the segment of the course that can be joined, in nm
// coordinates: the final from as far back as it reaches to its end, or the
// leg.
func (c InterceptCourse) extent(nmPerLong float32) (p0, p1 [2]float32) {
	p0, p1 = math.LL2NM(c.Line[0], nmPerLong), math.LL2NM(c.Line[1], nmPerLong)
	if c.Final {
		p0 = math.Sub2f(p1, math.Scale2f(math.HeadingVector(c.course(nmPerLong)), c.Reach))
	}
	return
}

// distanceToThreshold returns how far the aircraft flies from p, a point on
// the course, to the threshold: along the final, or along the rest of the
// leg's route.
func (c InterceptCourse) distanceToThreshold(p [2]float32, threshold math.Point2LL, nmPerLong float32) float32 {
	if c.Final {
		dir := math.HeadingVector(c.course(nmPerLong))
		return math.Dot(math.Sub2f(math.LL2NM(threshold, nmPerLong), p), dir)
	}
	d := math.Distance2f(p, math.LL2NM(c.Line[1], nmPerLong))
	for i := c.Index + 1; i < len(c.Route)-1; i++ {
		d += math.NMDistance2LL(c.Route[i].Location, c.Route[i+1].Location)
	}
	return d
}

// interceptingFromHeading reports whether the aircraft's heading is taking
// it to join the approach course.
func (nav *Nav) interceptingFromHeading() bool {
	return nav.Approach.InterceptState == InitialHeading || nav.Approach.InterceptState == TurningToJoin
}

func (nav *Nav) InterceptedButNotCleared() bool {
	return nav.Approach.InterceptState == OnApproachCourse && !nav.Approach.Cleared
}

func (nav *Nav) InterceptApproach(joinFix string) speech.CommandIntent {
	defer nav.record("InterceptApproach", joinFix)()
	if intent := nav.prepareForApproach(false, joinFix); intent != nil {
		return intent
	}

	ap := nav.Approach.Assigned
	if nav.Approach.HasLocalizer() {
		return speech.ApproachIntent{
			Type:         speech.ApproachIntercept,
			ApproachName: ap.FullName,
			HasLocalizer: true,
		}
	}
	return speech.ApproachIntent{
		Type:         speech.ApproachJoin,
		ApproachName: ap.FullName,
	}
}

// AtFixIntercept has the aircraft join the approach course once it passes
// fix, which must be on the route it is flying. One on a heading never
// passes the fix.
func (nav *Nav) AtFixIntercept(fix string) speech.CommandIntent {
	defer nav.record("AtFixIntercept", fix)()
	ap := nav.Approach.Assigned
	if ap == nil {
		return speech.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	// If the fix isn't on the approach, there will be nothing to join when
	// we get there.
	if route, _ := approachRouteThrough(ap, fix); route == nil {
		return speech.MakeUnableIntent("unable. {fix} is not on the {appr} approach", fix, ap.FullName)
	}
	if nav.Approach.Cleared {
		return speech.MakeUnableIntent("we're already cleared for the {appr} approach", ap.FullName)
	}
	if _, onHeading := nav.AssignedHeading(); onHeading {
		if nav.interceptingFromHeading() {
			return speech.MakeUnableIntent("we're already intercepting")
		}
		return speech.MakeUnableIntent("unable. We're on a heading")
	}
	if !slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return wp.Fix == fix }) {
		return speech.MakeUnableIntent("unable. {fix} is not in our route", fix)
	}

	nav.Approach.AtFixInterceptFix = fix

	return speech.ApproachIntent{
		Type:         speech.ApproachAtFixIntercept,
		ApproachName: ap.FullName,
		Fix:          fix,
		HasLocalizer: nav.Approach.HasLocalizer(),
	}
}

// interceptApproachAtFix starts an "at fix, intercept the approach"
// instruction if wp is its fix. prepareForApproach handles both cases: on a
// heading, it leaves the aircraft on it to intercept; direct to an approach
// fix, it splices the approach into the route, and the aircraft is then
// established on the approach course. It reports whether the aircraft
// joined the approach.
func (nav *Nav) interceptApproachAtFix(wp av.Waypoint) bool {
	if nav.Approach.AtFixInterceptFix != wp.Fix || nav.Approach.Assigned == nil {
		return false
	}
	nav.Approach.AtFixInterceptFix = "" // Clear so we don't trigger again

	_, assignedHeading := nav.AssignedHeading()
	if nav.prepareForApproach(false, "") != nil {
		return false
	}
	if !assignedHeading {
		nav.Approach.InterceptState = OnApproachCourse
		nav.Approach.NoPT = true
	}
	return true
}

// interceptCandidates returns the courses of the assigned approach that a
// heading can join: its final approach course, and each leg of its routes
// that is not a stretch of the final. A charted visual's track to the
// runway is its legs, so it has no separate final. A localizer reaches as
// far as it can be received and a visual's final is the runway's extended
// centerline, so those finals run out indefinitely; another approach's
// final ends at its farthest fix. The final comes first.
func (nav *Nav) interceptCandidates() []InterceptCourse {
	ap := nav.Approach.Assigned
	nmPerLong, magVar := nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation

	var courses []InterceptCourse
	alongFinal := func(a, b math.Point2LL) bool { return false }
	if line := ap.FinalApproachCourseLine(nmPerLong, magVar); ap.Type != av.ChartedVisualApproach && line[0] != line[1] {
		p1 := math.LL2NM(line[1], nmPerLong)
		dir := math.HeadingVector(math.Heading2LL(line[0], line[1], nmPerLong))
		alongFinal = func(a, b math.Point2LL) bool { return av.LegAlongCourse(line, a, b, nmPerLong) }

		reach := float32(100)
		if !ap.Type.HasLocalizer() && ap.Type != av.VisualApproach {
			reach = 0
			for _, route := range ap.Waypoints {
				for i := range len(route) - 1 {
					if alongFinal(route[i].Location, route[i+1].Location) {
						reach = max(reach, math.Dot(math.Sub2f(p1, math.LL2NM(route[i].Location, nmPerLong)), dir))
					}
				}
			}
		}
		courses = append(courses, InterceptCourse{Line: line, Final: true, Reach: reach})
	}

	for _, route := range ap.Waypoints {
		for i := range len(route) - 1 {
			a, b := route[i], route[i+1]
			if alongFinal(a.Location, b.Location) {
				continue
			}
			// A leg flown on a heading from its first fix or around an
			// arc isn't a course between the two that a heading can
			// intercept.
			if _, heading := a.HeadingAction(); heading || a.Arc() != nil {
				continue
			}
			courses = append(courses, InterceptCourse{Line: [2]math.Point2LL{a.Location, b.Location}, Route: route, Index: i})
		}
	}
	return courses
}

// The widest angles at which a heading joins the final approach course and
// a leg. Controllers vector to intercept a final at 30 degrees or less, and
// much beyond 45 the aircraft flies through it before it can turn onto it.
// A leg is joined wherever the heading crosses it going its way, as a
// charted turn onto it might.
const (
	maxFinalInterceptAngle = 45
	maxLegInterceptAngle   = 90
)

// legDetour is how much farther than the straight-line distance to the
// threshold a leg may carry the aircraft and still be the leg its heading
// was meant to join. A vector across a transition that leads back out
// isn't an intercept of it.
const legDetour = 3 // nm

// minimumFinal is how far from the threshold a visual approach has to be
// joined: a stabilized final of 3 nm for all but the smallest aircraft
// (CWT A-G). An instrument approach is intercepted wherever the controller
// vectors it.
func (nav *Nav) minimumFinal() float32 {
	ap := nav.Approach.Assigned
	if ap.Type != av.VisualApproach && ap.Type != av.ChartedVisualApproach {
		return 0
	}
	cwt := nav.Perf.Category.CWT
	if len(cwt) > 0 && cwt[0] >= 'A' && cwt[0] <= 'G' {
		return 3
	}
	return 0
}

// pathCrossing returns where a path from origin along dir meets the segment
// from p0 to p1 and how far along the path that is. A path already on the
// segment, within the capture tolerance, meets it where it starts.
func pathCrossing(origin, dir, p0, p1 [2]float32) ([2]float32, float32, bool) {
	seg := math.Sub2f(p1, p0)
	segLen2 := math.Dot(seg, seg)
	if segLen2 == 0 {
		return [2]float32{}, 0, false
	}
	if t := math.Dot(math.Sub2f(origin, p0), seg) / segLen2; t >= 0 && t <= 1 &&
		math.Abs(math.SignedPointLineDistance(origin, p0, p1)) < courseCaptureTolerance {
		return origin, 0, true
	}
	p, dist, _, ok := math.RaySegmentIntersect(origin, dir, p0, p1)
	return p, dist, ok
}

// selectInterceptCourse picks the course the aircraft's assigned heading
// takes it onto, predicting its path as the turn to the heading, if it is
// still turning, and then the heading itself. The final approach course is
// taken whenever the heading is within maxFinalInterceptAngle of it and the
// path meets it, even through a leg on the way, since a vector toward the
// final is meant to intercept the final; it is also taken when the aircraft
// is close enough to be turned back onto it, as after an overshoot.
// Otherwise the first leg the path meets is taken. The path meets a course
// where it crosses it going the course's way, or where the turn passes
// through the course's heading if the turn reaches the course; a visual also
// has to meet it far enough out for a stabilized final. It reports false if
// the heading meets no course.
func (nav *Nav) selectInterceptCourse(wxs wx.Sample) (InterceptCourse, bool) {
	hdg, turn, ok := nav.headingToJoinFrom()
	if !ok {
		return InterceptCourse{}, false
	}
	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong, magVar := fs.NmPerLongitude, fs.MagneticVariation
	hdgTrue := math.MagneticToTrue(hdg, magVar)

	// Where the heading is established, how far the aircraft flies getting
	// there, and the track it then makes over the ground.
	origin := math.LL2NM(fs.Position, nmPerLong)
	flown := float32(0)
	turning := math.HeadingDifference(fs.Heading, hdg) > 5
	var tp turnPath
	if turning {
		tp = nav.predictTurnPath(hdg, turn, wxs)
		origin = tp.position(tp.dur)
		flown = tp.tasNMps * tp.dur
	}
	track := math.Normalize2f(math.Add2f(
		math.Scale2f(math.HeadingVector(hdgTrue), nav.TAS(wxs.Temperature())/3600), nav.groundWind(wxs)))
	minFinal := nav.minimumFinal()
	direct := math.NMDistance2LL(fs.Position, ap.Threshold)

	var best InterceptCourse
	var bestDist float32
	found := false
	for _, c := range nav.interceptCandidates() {
		courseTrue := c.course(nmPerLong)
		maxAngle := float32(util.Select(c.Final, maxFinalInterceptAngle, maxLegInterceptAngle))
		if math.HeadingDifference(courseTrue, hdgTrue) > maxAngle {
			continue
		}
		courseMag := math.TrueToMagnetic(courseTrue, magVar)
		p0, p1 := c.extent(nmPerLong)

		var at [2]float32
		var dist float32
		met := false
		if turning && math.HeadingInTurnArc(fs.Heading, courseMag, hdg, math.TurnDirection(turn)) &&
			tp.reachesLine(p0, p1) {
			// The turn passes through the course's heading and reaches
			// the course, so the aircraft may roll out on the course
			// partway through it, about abeam of where it is now.
			seg := math.Sub2f(p1, p0)
			if t := math.Dot(math.Sub2f(origin, p0), seg) / math.Dot(seg, seg); t >= 0 && t <= 1 {
				at = math.Add2f(p0, math.Scale2f(seg, t))
				dist = flown*TurnAngle(fs.Heading, courseMag, turn)/TurnAngle(fs.Heading, hdg, turn) +
					math.Distance2f(origin, at)
				met = true
			}
		}
		if !met {
			var d float32
			if at, d, met = pathCrossing(origin, track, p0, p1); met {
				dist = flown + d
			}
		}
		if met {
			remaining := c.distanceToThreshold(at, ap.Threshold, nmPerLong)
			if remaining <= minFinal || (!c.Final && remaining > direct+legDetour) {
				continue
			}
			c.Join = math.NM2LL(at, nmPerLong)
		} else if !c.Final || !nav.approachRecoveryFeasible(ap) {
			continue
		}

		if c.Final {
			return c, true
		}
		if !found || dist < bestDist {
			best, bestDist, found = c, dist, true
		}
	}
	return best, found
}

func (nav *Nav) ApproachHeading(callsign string, wxs wx.Sample, simTime Time) (heading math.MagneticHeading, turn av.TurnDirection) {
	// Baseline
	heading, turn = *nav.Heading.Assigned, av.TurnClosest

	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong, magVar := fs.NmPerLongitude, fs.MagneticVariation

	switch nav.Approach.InterceptState {
	case InitialHeading:
		// While the aircraft is still turning to the assigned heading, the
		// intercept isn't given up on from the stale physical heading.
		assignedMag, assignedTurn, _ := nav.headingToJoinFrom()
		turningToAssigned := math.HeadingDifference(fs.Heading, assignedMag) > 5

		course, ok := nav.selectInterceptCourse(wxs)
		if !ok {
			if !turningToAssigned {
				nav.missIntercept()
			}
			return
		}
		courseTrue := course.course(nmPerLong)
		hdgMag := math.TrueToMagnetic(courseTrue, magVar)

		// If the course lies inside the active turn to the assigned
		// intercept heading, allow capture before rolling all the way out
		// on that heading.
		if turningToAssigned && !math.HeadingInTurnArc(fs.Heading, hdgMag, assignedMag, math.TurnDirection(assignedTurn)) {
			return
		}
		// Whether the aircraft reaches the course doesn't matter here; having
		// committed to the approach it recovers onto it either way.
		turnResult, _ := nav.shouldTurnToIntercept(course.Line[0], hdgMag, av.TurnClosest, wxs)
		switch turnResult {
		case turnToInterceptWait:
			// Still too far; keep flying the assigned heading.

		case turnToInterceptTurn, turnToInterceptCorrectableOvershoot:
			// A turn that captures the final is flown however close to the
			// FAF the controller set it up; one that overshoots is
			// recovered from only where there is room to.
			if course.Final && turnResult == turnToInterceptCorrectableOvershoot && !nav.approachRecoveryFeasible(ap) {
				nav.missIntercept()
				return
			}
			joinHdg := hdgMag
			if turnResult == turnToInterceptCorrectableOvershoot {
				// Shallow out to a 20 degree intercept from whichever side of
				// the course the aircraft is on. Taking the side from the
				// heading difference instead would be right only once the
				// aircraft had crossed; before that it aims 20 degrees to the
				// far side of the course and flies away from it.
				recoveryTrue := math.OffsetHeading(courseTrue,
					math.Copysign(20, -nav.signedCourseLineDistance(course.Line)))
				joinHdg = math.TrueToMagnetic(recoveryTrue, magVar)
			}
			nav.Approach.InterceptState = TurningToJoin
			nav.Approach.Intercept = course
			nav.Heading = Heading{Assigned: &joinHdg}
			nav.DeferredNavHeading = nil
			nav.Waypoints = []av.Waypoint{fs.ArrivalAirport}

		case turnToInterceptMajorOvershoot:
			if turningToAssigned {
				return
			}
			nav.missIntercept()
		}
		return

	case TurningToJoin:
		course := nav.Approach.Intercept
		// we've turned to intercept. have we intercepted?
		if !nav.onCourseLine(course.Line, courseCaptureTolerance) {
			// Apply wind correction to track the approach course, not just
			// fly the course heading. Without this, strong crosswind would
			// blow the aircraft off the course.
			heading = nav.headingForTrack(*nav.Heading.Assigned, wxs)
			NavLog(callsign, simTime, NavLogApproach, "TurningToJoin: not on course, flying wind-corrected hdg %.0f (course hdg %.0f)", heading, *nav.Heading.Assigned)
			// Once settled on the heading the aircraft either closes on the
			// course or never will; nothing after this reconsiders the
			// intercept, so ask for new vectors rather than flying away from
			// the approach without a word.
			if math.HeadingDifference(fs.Heading, heading) < 1 &&
				!nav.closingOnCourseLine(course.Line, wxs) {
				NavLog(callsign, simTime, NavLogApproach, "TurningToJoin: not closing on the course; requesting vectors")
				nav.approachOvershootRequestVectors()
			}
			return
		}
		NavLog(callsign, simTime, NavLogApproach, "TurningToJoin->OnApproachCourse: established on approach course")

		// We're established on the approach course. Figure out which
		// fixes are still ahead and add them to the aircraft's waypoints.
		nav.Heading = Heading{}
		nav.Approach.InterceptState = OnApproachCourse
		// If we have intercepted the approach course, we don't do procedure turns.
		nav.Approach.NoPT = true
		if nav.Approach.Cleared && ap.Type == av.VisualApproach {
			// The visual's profile runs from where the aircraft joined.
			nav.flyVisualRoute(nav.visualRouteAt(course, fs.Position))
			return
		}
		nav.Waypoints = append(nav.routeAt(course, fs.Position).Clone(), fs.ArrivalAirport)
		// Ignore the approach altitude constraints if the aircraft is only
		// intercepting but isn't cleared.
		if nav.Approach.Cleared {
			nav.clearAltitudeForApproach()
		}
		return
	}

	return
}

// routeAt returns the rest of the approach route once the aircraft is on
// course at p: the fixes ahead on the final, or the route beyond the leg.
func (nav *Nav) routeAt(course InterceptCourse, p math.Point2LL) av.WaypointArray {
	if course.Final {
		route, idx := nav.finalWaypointsAhead(nav.Approach.Assigned, p)
		return route[idx:]
	}
	return course.Route[course.Index+1:]
}

// missIntercept is what happens when the heading won't take the aircraft
// onto any of the approach's courses. On a visual the pilot flies to the
// final themselves; otherwise they ask for vectors.
func (nav *Nav) missIntercept() {
	if nav.Approach.Assigned.Type == av.VisualApproach && nav.Approach.Cleared {
		if wps := nav.visualRouteToFinal(nil); wps != nil {
			nav.Approach.InterceptState = NotIntercepting
			nav.flyVisualRoute(wps)
			return
		}
	}
	nav.approachOvershootRequestVectors()
}

// approachRecoveryFeasible returns true if the aircraft's current position
// allows a turn back to the final approach course: it must not be too close
// to the FAF and must still be close enough laterally for a stable recovery.
func (nav *Nav) approachRecoveryFeasible(ap *av.Approach) bool {
	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation
	pos := nav.FlightState.Position

	// Check proximity to the FAF: need at least 2nm along the course.
	wps, fafIdx := ap.FAFSegment(nmPerLong, magVar)
	if wps != nil {
		fafDist := math.NMDistance2LLFast(pos, wps[fafIdx].Location, nmPerLong)
		if fafDist < 2 {
			return false
		}
	}

	// Check if within a practical localizer recovery envelope. This is not
	// CDI full-scale deflection; it is the area where a vectored aircraft
	// that crossed final can still be given a corrective intercept without
	// immediately asking ATC for new vectors. The previous 2° cone was too
	// narrow and rejected ordinary intercept recoveries several miles out.
	cl := ap.FinalApproachCourseLine(nmPerLong, magVar)
	posNM := math.LL2NM(pos, nmPerLong)
	cl0NM := math.LL2NM(cl[0], nmPerLong)
	cl1NM := math.LL2NM(cl[1], nmPerLong)
	lateralOffset := math.Abs(math.SignedPointLineDistance(posNM, cl0NM, cl1NM))
	distFromThreshold := math.NMDistance2LLFast(pos, ap.Threshold, nmPerLong)
	recoveryHalfWidth := max(float32(0.8), distFromThreshold*math.Tan(math.Radians(float32(5))))
	return lateralOffset <= recoveryHalfWidth
}

// approachOvershootRequestVectors cancels the approach and flags the
// pilot to request new vectors from ATC.
func (nav *Nav) approachOvershootRequestVectors() {
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.Cleared = false
	nav.Approach.Abandoned = true
	nav.Requests.Vectors = true
}

// finalWaypointsAhead returns the approach route along the final approach
// course and the index in it of the first fix on the final that is ahead
// of p, measured along the course.
func (nav *Nav) finalWaypointsAhead(ap *av.Approach, p math.Point2LL) (av.WaypointArray, int) {
	apHeading := ap.FinalApproachCourse(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	wps, idx := ap.FAFSegment(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

	// The aircraft joins wherever the intercept brings it, which may
	// be abeam of or past one of the approach's fixes. Flying to one
	// of those would turn it straight back off the course it just
	// captured, so measure along the course: having joined within
	// the capture tolerance of it, a fix less than that far ahead is
	// where the aircraft already is.
	pos := math.LL2NM(p, nav.FlightState.NmPerLongitude)
	courseVec := math.HeadingVector(apHeading)
	ahead := func(wp av.Waypoint) bool {
		d := math.Sub2f(math.LL2NM(wp.Location, nav.FlightState.NmPerLongitude), pos)
		return math.Dot(d, courseVec) > courseCaptureTolerance
	}

	for idx > 0 {
		prev := wps[idx-1]
		hdg := math.Heading2LL(prev.Location, wps[idx].Location,
			nav.FlightState.NmPerLongitude)

		if math.HeadingDifference(hdg, apHeading) > 5 { // not on the final approach course
			break
		}
		if !ahead(prev) {
			break
		}
		idx--
	}
	for idx < len(wps)-1 && !ahead(wps[idx]) {
		idx++
	}
	return wps, idx
}

// waypointsAfterIntercept returns the approach waypoints that the aircraft
// will follow once its heading joins the approach course: the ones that
// ApproachHeading puts in its route at the capture. It returns nil if the
// heading doesn't join the course.
func (nav *Nav) waypointsAfterIntercept() []av.Waypoint {
	ap := nav.Approach.Assigned
	fs := &nav.FlightState
	nmPerLong := fs.NmPerLongitude

	course := nav.Approach.Intercept
	if nav.Approach.InterceptState != TurningToJoin {
		var ok bool
		if course, ok = nav.selectInterceptCourse(wx.MakeStandardSampleForAltitude(fs.Altitude)); !ok {
			return nil
		}
	}
	if !course.Final {
		return course.Route[course.Index+1:]
	}

	courseTrue := course.course(nmPerLong)
	pos := math.LL2NM(fs.Position, nmPerLong)
	join := pos
	// In its turn onto the final the aircraft is assigned the course itself
	// as its heading, which never meets the course; it rolls out about
	// abeam of where it is now.
	hdg, _, _ := nav.headingToJoinFrom()
	hdgTrue := math.MagneticToTrue(hdg, fs.MagneticVariation)
	inCaptureTurn := nav.Approach.InterceptState == TurningToJoin && math.HeadingDifference(hdgTrue, courseTrue) < 1
	if !inCaptureTurn {
		dir := math.HeadingVector(hdgTrue)
		p, ok := math.LineLineIntersect(pos, math.Add2f(pos, dir), math.LL2NM(course.Line[0], nmPerLong),
			math.LL2NM(course.Line[1], nmPerLong))
		if !ok || math.Dot(math.Sub2f(p, pos), dir) < 0 {
			return nil
		}
		join = p
	}
	// The aircraft rolls out on the course somewhat past where its heading
	// meets it or where it starts its turn onto it; a fix within this
	// distance of that point may be behind it by the time it captures the
	// course.
	const rollOutMargin = 1 // nm
	join = math.Add2f(join, math.Scale2f(math.HeadingVector(courseTrue), rollOutMargin))
	wps, idx := nav.finalWaypointsAhead(ap, math.NM2LL(join, nmPerLong))
	return wps[idx:]
}

// groundVelocity returns the aircraft's velocity over the ground in nm per
// second, as the flight model integrates it: the heading flown at TAS,
// displaced by the wind once airborne.
func (nav *Nav) groundVelocity(wxs wx.Sample) [2]float32 {
	fs := &nav.FlightState
	v := math.Scale2f(math.HeadingVector(math.MagneticToTrue(fs.Heading, fs.MagneticVariation)),
		nav.TAS(wxs.Temperature())/3600)
	return math.Add2f(v, nav.groundWind(wxs))
}

// minInterceptAngle is the shallowest angle at which a track counts as
// converging on a course; anything less only creeps toward it.
const minInterceptAngle = 10 // degrees

// onCourseLine checks if the flight position is less than maxNmDeviation
// from the infinite line defined by the two given points.
func (nav *Nav) onCourseLine(line [2]math.Point2LL, maxNmDeviation float32) bool {
	return math.Abs(nav.signedCourseLineDistance(line)) < maxNmDeviation
}

// signedCourseLineDistance returns the flight position's distance in nm from
// the infinite line defined by the two given points; it is positive when the
// aircraft is to the right of the line's direction.
func (nav *Nav) signedCourseLineDistance(line [2]math.Point2LL) float32 {
	return math.SignedPointLineDistance(
		math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude),
		math.LL2NM(line[0], nav.FlightState.NmPerLongitude),
		math.LL2NM(line[1], nav.FlightState.NmPerLongitude))
}

// closingOnCourseLine reports whether the aircraft's ground track is
// converging on the infinite line defined by the two given points at more
// than a glancing angle.
func (nav *Nav) closingOnCourseLine(line [2]math.Point2LL, wxs wx.Sample) bool {
	v := nav.groundVelocity(wxs)

	// The track has to be angled back toward the side of the course the
	// aircraft is on, and by enough to actually get there.
	course := math.Heading2LL(line[0], line[1], nav.FlightState.NmPerLongitude)
	angle := math.HeadingSignedTurn(course, math.VectorHeading(v))
	return angle*nav.signedCourseLineDistance(line) < 0 && math.Abs(angle) >= minInterceptAngle
}
