// nav/intercept.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/wx"
)

// Joining the approach course from a heading: the intercept the controller
// sets up, the turn onto the course and its capture, and the recovery or
// the request for vectors when the aircraft overshoots.

type InterceptState int

const (
	NotIntercepting InterceptState = iota
	InitialHeading
	TurningToJoin
	OnApproachCourse
)

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
	// Under a synthesized visual, "intercept" commits the aircraft to fly
	// the localizer route when an ILS/Localizer reference is available.
	if ap.Type == av.VisualApproach && nav.Approach.InterceptedReference == nil {
		for _, ref := range nav.Approach.VisualReferences {
			if ref.Type.HasLocalizer() {
				nav.Approach.InterceptedReference = ref
				break
			}
		}
	}
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
	if nav.Approach.AssignedId == "" {
		return speech.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned
	if ap == nil {
		return speech.MakeUnableIntent("unable. We were never told to expect an approach")
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

	nav.Approach.InterceptedReference = nav.visualReferenceForFix(fix)
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

func (nav *Nav) ApproachHeading(callsign string, wxs wx.Sample, simTime Time) (heading math.MagneticHeading, turn av.TurnDirection) {
	// Baseline
	heading, turn = *nav.Heading.Assigned, av.TurnClosest

	ap := nav.Approach.Assigned
	hasLocalizer := nav.Approach.HasLocalizer()

	// Determine the course and line to intercept.
	var courseTrue math.TrueHeading
	var courseLine [2]math.Point2LL
	var interceptWaypoints av.WaypointArray // waypoints from intercept point forward
	if hasLocalizer {
		courseTrue = ap.FinalApproachCourse(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		courseLine = ap.FinalApproachCourseLine(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	} else if nav.Approach.InterceptState == TurningToJoin {
		// Already committed to a segment; use cached values.
		courseLine = nav.Approach.InterceptCourseLine
		courseTrue = math.Heading2LL(courseLine[0], courseLine[1], nav.FlightState.NmPerLongitude)
		interceptWaypoints = nav.Approach.InterceptWaypoints
	} else {
		// For non-ILS approaches, find the approach segment that the
		// aircraft's ground track will cross.
		var ok bool
		courseTrue, courseLine, interceptWaypoints, ok = nav.findInterceptSegment(ap, nav.groundTrack(wxs))
		if !ok {
			nav.approachOvershootRequestVectors()
			return
		}
	}

	switch nav.Approach.InterceptState {
	case InitialHeading:
		assignedMag, _ := nav.AssignedHeading() // use the deferred heading for the following
		assignedTrue := math.MagneticToTrue(assignedMag, nav.FlightState.MagneticVariation)
		if d := math.HeadingDifference(courseTrue, assignedTrue); d > 45 {
			// Too big an intercept angle; request vectors
			nav.approachOvershootRequestVectors()
			return
		}
		// If the aircraft is still turning to the assigned heading, don't
		// reject the intercept from the stale physical heading. However, if
		// the final approach course lies inside the active turn to the assigned
		// intercept heading, allow capture before rolling all the way out on
		// that heading.
		hdgMag := math.TrueToMagnetic(courseTrue, nav.FlightState.MagneticVariation)
		turningToAssigned := math.HeadingDifference(nav.FlightState.Heading, assignedMag) > 5
		if turningToAssigned {
			assignedTurn := av.TurnClosest
			if nav.Heading.Turn != nil {
				assignedTurn = *nav.Heading.Turn
			}
			if !math.HeadingInTurnArc(nav.FlightState.Heading, hdgMag, assignedMag, math.TurnDirection(assignedTurn)) {
				return
			}
		}
		// Whether the aircraft reaches the course doesn't matter here; having
		// committed to the approach it recovers onto it either way.
		turnResult, _ := nav.shouldTurnToIntercept(courseLine[0], hdgMag, av.TurnClosest, wxs)
		switch turnResult {
		case turnToInterceptWait:
			// Still too far; keep flying the assigned heading.

		case turnToInterceptTurn:
			if hasLocalizer && !nav.approachRecoveryFeasible(ap) {
				nav.approachOvershootRequestVectors()
				return
			}
			nav.Approach.InterceptState = TurningToJoin
			if !hasLocalizer {
				nav.Approach.InterceptCourseLine = courseLine
				nav.Approach.InterceptWaypoints = interceptWaypoints
			}
			nav.Heading = Heading{Assigned: &hdgMag}
			nav.DeferredNavHeading = nil
			nav.Waypoints = []av.Waypoint{nav.FlightState.ArrivalAirport}

		case turnToInterceptCorrectableOvershoot:
			if hasLocalizer && !nav.approachRecoveryFeasible(ap) {
				nav.approachOvershootRequestVectors()
				return
			}
			// Shallow out to a 20 degree intercept from whichever side of
			// the course the aircraft is on. Taking the side from the
			// heading difference instead would be right only once the
			// aircraft had crossed; before that it aims 20 degrees to the
			// far side of the course and flies away from it.
			recoveryTrue := math.OffsetHeading(courseTrue,
				math.Copysign(20, -nav.signedCourseLineDistance(courseLine)))
			recoveryHdg := math.TrueToMagnetic(recoveryTrue, nav.FlightState.MagneticVariation)
			nav.Approach.InterceptState = TurningToJoin
			if !hasLocalizer {
				nav.Approach.InterceptCourseLine = courseLine
				nav.Approach.InterceptWaypoints = interceptWaypoints
			}
			nav.Heading = Heading{Assigned: &recoveryHdg}
			nav.DeferredNavHeading = nil
			nav.Waypoints = []av.Waypoint{nav.FlightState.ArrivalAirport}

		case turnToInterceptMajorOvershoot:
			if turningToAssigned {
				return
			}
			nav.approachOvershootRequestVectors()
		}
		return

	case TurningToJoin:
		// we've turned to intercept. have we intercepted?
		if !nav.onCourseLine(courseLine, courseCaptureTolerance) {
			// Apply wind correction to track the approach course, not just
			// fly the course heading. Without this, strong crosswind would
			// blow the aircraft off the course.
			heading = nav.headingForTrack(*nav.Heading.Assigned, wxs)
			NavLog(callsign, simTime, NavLogApproach, "TurningToJoin: not on course, flying wind-corrected hdg %.0f (course hdg %.0f)", heading, *nav.Heading.Assigned)
			// Once settled on the heading the aircraft either closes on the
			// course or never will; nothing after this reconsiders the
			// intercept, so ask for new vectors rather than flying away from
			// the approach without a word.
			if math.HeadingDifference(nav.FlightState.Heading, heading) < 1 &&
				!nav.closingOnCourseLine(courseLine, wxs) {
				NavLog(callsign, simTime, NavLogApproach, "TurningToJoin: not closing on the course; requesting vectors")
				nav.approachOvershootRequestVectors()
			}
			return
		}
		NavLog(callsign, simTime, NavLogApproach, "TurningToJoin->OnApproachCourse: established on approach course")

		// We're established on the approach course. Figure out which
		// fixes are still ahead and add them to the aircraft's waypoints.
		if hasLocalizer {
			nav.Waypoints = append(nav.localizerWaypointsAhead(ap, nav.FlightState.Position).Clone(),
				nav.FlightState.ArrivalAirport)
		} else {
			nav.Waypoints = append(interceptWaypoints.Clone(),
				nav.FlightState.ArrivalAirport)
		}

		// Ignore the approach altitude constraints if the aircraft is only
		// intercepting but isn't cleared.
		if nav.Approach.Cleared {
			nav.clearAltitudeForApproach()
		}
		// As with the heading assignment above under the InitialHeading
		// case, do this immediately.
		nav.Heading = Heading{}
		nav.Approach.InterceptState = OnApproachCourse

		// If we have intercepted the approach course, we don't do procedure turns.
		nav.Approach.NoPT = true

		return
	}

	return
}

// approachRecoveryFeasible returns true if the aircraft's current position
// allows a turn back to the localizer: it must not be too close to the FAF
// and must still be close enough laterally for a stable recovery.
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

// localizerWaypointsAhead returns the approach's waypoints from the first
// fix on the final approach course that is ahead of p, measured along the
// course, through the end of the approach.
func (nav *Nav) localizerWaypointsAhead(ap *av.Approach, p math.Point2LL) av.WaypointArray {
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
	return wps[idx:]
}

// waypointsAfterIntercept returns the approach waypoints that an aircraft
// flying hdg will follow once it joins the approach course: the ones that
// ApproachHeading puts in its route at the capture. It returns nil if hdg
// doesn't join the course.
func (nav *Nav) waypointsAfterIntercept(hdg math.MagneticHeading) []av.Waypoint {
	ap := nav.Approach.Assigned
	nmPerLong, magVar := nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation
	hdgTrue := math.MagneticToTrue(hdg, magVar)

	if !nav.Approach.HasLocalizer() {
		if nav.Approach.InterceptState == TurningToJoin {
			return nav.Approach.InterceptWaypoints
		}
		_, _, wps, _ := nav.findInterceptSegment(ap, hdgTrue)
		return wps
	}

	courseTrue := ap.FinalApproachCourse(nmPerLong, magVar)
	pos := math.LL2NM(nav.FlightState.Position, nmPerLong)
	join := pos
	// In its turn onto the localizer the aircraft is assigned the course
	// itself as its heading, which never meets the course; it rolls out
	// about abeam of where it is now.
	inCaptureTurn := nav.Approach.InterceptState == TurningToJoin &&
		math.HeadingDifference(hdgTrue, courseTrue) < 1
	if !inCaptureTurn {
		line := ap.FinalApproachCourseLine(nmPerLong, magVar)
		dir := math.HeadingVector(hdgTrue)
		p, ok := math.LineLineIntersect(pos, math.Add2f(pos, dir), math.LL2NM(line[0], nmPerLong),
			math.LL2NM(line[1], nmPerLong))
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
	return nav.localizerWaypointsAhead(ap, math.NM2LL(join, nmPerLong))
}

// groundTrack returns the aircraft's present true course over the ground.
func (nav *Nav) groundTrack(wxs wx.Sample) math.TrueHeading {
	hdgTrue := math.MagneticToTrue(nav.FlightState.Heading, nav.FlightState.MagneticVariation)
	TAS := nav.TAS(wxs.Temperature()) / 3600
	flightVec := math.Scale2f(math.SinCos(math.Radians(hdgTrue)), TAS)
	return math.VectorHeading(math.Add2f(flightVec, wxs.WindVec()))
}

// findInterceptSegment finds the approach segment that a ray from the
// aircraft along the given true track will cross. Returns the segment
// course, the two endpoints defining the line, the remaining waypoints from
// the intercept point forward, and whether a valid segment was found.
func (nav *Nav) findInterceptSegment(ap *av.Approach, track math.TrueHeading) (math.TrueHeading, [2]math.Point2LL, []av.Waypoint, bool) {
	hit, ok := av.ClosestRayRouteIntersection(nav.FlightState.Position, track, ap.Waypoints)
	if !ok {
		return 0, [2]math.Point2LL{}, nil, false
	}

	route := ap.Waypoints[hit.RouteIndex]
	nmPerLong := nav.FlightState.NmPerLongitude
	course := math.Heading2LL(route[hit.Index].Location, route[hit.Index+1].Location, nmPerLong)
	line := [2]math.Point2LL{route[hit.Index].Location, route[hit.Index+1].Location}
	return course, line, route[hit.Index+1:], true
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
	fs := &nav.FlightState

	// The ground velocity, as the flight model integrates it: the heading
	// flown at TAS, displaced by the wind.
	v := math.Scale2f(math.HeadingVector(math.MagneticToTrue(fs.Heading, fs.MagneticVariation)),
		nav.TAS(wxs.Temperature())/3600)
	v = math.Add2f(v, nav.groundWind(wxs))

	// The track has to be angled back toward the side of the course the
	// aircraft is on, and by enough to actually get there.
	course := math.Heading2LL(line[0], line[1], fs.NmPerLongitude)
	angle := math.HeadingSignedTurn(course, math.VectorHeading(v))
	return angle*nav.signedCourseLineDistance(line) < 0 && math.Abs(angle) >= minInterceptAngle
}

// OnExtendedCenterline checks if the flight position is less than
// maxNmDeviation from the assigned approach's final approach course.
func (nav *Nav) OnExtendedCenterline(maxNmDeviation float32) bool {
	approach := nav.Approach.Assigned
	if approach == nil {
		return false
	}

	cl := approach.FinalApproachCourseLine(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
	return nav.onCourseLine(cl, maxNmDeviation)
}
