// nav/approaches.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"log/slog"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (nav *Nav) ApproachHeading(callsign string, wxs wx.Sample, simTime time.Time) (heading float32, turn av.TurnDirection) {
	// Baseline
	heading, turn = *nav.Heading.Assigned, av.TurnClosest

	ap := nav.Approach.Assigned

	switch nav.Approach.InterceptState {
	case InitialHeading:
		// On a heading. Is it time to turn?  Allow a lot of slop, but just
		// fly through the localizer if it's too sharp an intercept
		hdg := ap.RunwayHeading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		if d := math.HeadingDifference(hdg, nav.FlightState.Heading); d > 45 {
			NavLog(callsign, simTime, NavLogApproach, "InitialHeading: intercept angle %.1f too sharp, continuing heading %.0f", d, nav.FlightState.Heading)
			return
		}

		loc := ap.ExtendedCenterline(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if nav.shouldTurnToIntercept(loc[0], hdg, av.TurnClosest, wxs) {
			NavLog(callsign, simTime, NavLogApproach, "InitialHeading->TurningToJoin: turning to intercept runway hdg %.0f", hdg)
			nav.Approach.InterceptState = TurningToJoin
			// The autopilot is doing this, so start the turn immediately;
			// don't use EnqueueHeading. However, leave any deferred
			// heading/direct fix in place, as it represents a controller
			// command that should still be followed.
			nav.Heading = NavHeading{Assigned: &hdg}
			// Just in case.. Thus we will be ready to pick up the
			// approach waypoints once we capture.
			nav.Waypoints = []av.Waypoint{nav.FlightState.ArrivalAirport}
		} else {
			NavLog(callsign, simTime, NavLogApproach, "InitialHeading: not yet time to turn, acft hdg %.0f rwy hdg %.0f", nav.FlightState.Heading, hdg)
		}
		return

	case TurningToJoin:
		// we've turned to intercept. have we intercepted?
		if !nav.OnExtendedCenterline(.2) {
			// Apply wind correction to track the localizer course, not just
			// fly the runway heading. Without this, strong crosswind would
			// blow the aircraft off the localizer.
			hdgTrue := *nav.Heading.Assigned - nav.FlightState.MagneticVariation
			heading = nav.headingForTrack(hdgTrue, wxs)
			NavLog(callsign, simTime, NavLogApproach, "TurningToJoin: not on centerline, flying wind-corrected hdg %.0f (rwy hdg %.0f)", heading, *nav.Heading.Assigned)
			return
		}
		NavLog(callsign, simTime, NavLogApproach, "TurningToJoin->OnApproachCourse: established on localizer")

		// we'll call that good enough. Now we need to figure out which
		// fixes in the approach are still ahead and then add them to
		// the aircraft's waypoints.
		apHeading := ap.RunwayHeading(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		wps, idx := ap.FAFSegment(nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
		for idx > 0 {
			prev := wps[idx-1]
			hdg := math.Heading2LL(prev.Location, wps[idx].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

			if math.HeadingDifference(hdg, apHeading) > 5 { // not on the final approach course
				break
			}

			acToWpHeading := math.Heading2LL(nav.FlightState.Position, wps[idx].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)
			acToPrevHeading := math.Heading2LL(nav.FlightState.Position, wps[idx-1].Location,
				nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

			da := math.Mod(acToWpHeading-nav.FlightState.Heading+360, 360)
			db := math.Mod(acToPrevHeading-nav.FlightState.Heading+360, 360)
			if (da < 180 && db > 180) || (da > 180 && db < 180) {
				// prev and current are on different sides of the current
				// heading, so don't take the prev so we don't turn away
				// from where we should be going.
				break
			}
			idx--
		}
		nav.Waypoints = append(util.DuplicateSlice(wps[idx:]), nav.FlightState.ArrivalAirport)
		// Ignore the approach altitude constraints if the aircraft is only
		// intercepting but isn't cleared.
		if nav.Approach.Cleared {
			nav.Altitude = NavAltitude{}
		}
		// As with the heading assignment above under the InitialHeading
		// case, do this immediately.
		nav.Heading = NavHeading{}
		nav.Approach.InterceptState = OnApproachCourse

		// If we have intercepted the approach course, we don't do procedure turns.
		nav.Approach.NoPT = true

		return
	}

	return
}
func (nav *Nav) getApproach(airport *av.Airport, id string, lg *log.Logger) (*av.Approach, error) {
	if id == "" {
		return nil, ErrInvalidApproach
	}

	for name, appr := range airport.Approaches {
		if name == id {
			return appr, nil
		}
	}
	return nil, ErrUnknownApproach
}

func (nav *Nav) ExpectApproach(airport *av.Airport, id string, runwayWaypoints map[string]av.WaypointArray,
	lahsoRunway string, lg *log.Logger) av.CommandIntent {
	ap, err := nav.getApproach(airport, id, lg)
	if err != nil {
		return av.MakeUnableIntent("unable. We don't know the {appr} approach.", id)
	}

	if id == nav.Approach.AssignedId && nav.Approach.Assigned != nil {
		nav.Approach.StandbyApproach = true
		return av.ApproachIntent{
			Type:         av.ApproachExpect,
			ApproachName: ap.FullName,
			LAHSORunway:  lahsoRunway,
		}
	}

	nav.Approach.Assigned = ap
	nav.Approach.AssignedId = id
	nav.Approach.ATPAVolume = airport.ATPAVolumes[ap.Runway]

	if waypoints := runwayWaypoints[ap.Runway]; len(waypoints) > 0 {
		if len(nav.Waypoints) == 0 {
			// Nothing left on our route; this shouldn't ever happen but
			// just in case patch the runway waypoints in there and hope it
			// works out.
			nav.Waypoints = append(util.DuplicateSlice(waypoints[1:]), nav.FlightState.ArrivalAirport)
		} else {
			// Try to splice the runway-specific waypoints in with the
			// aircraft's current waypoints...
			found := false
			for i, wp := range waypoints {
				navwp := nav.AssignedWaypoints()
				if idx := slices.IndexFunc(navwp, func(w av.Waypoint) bool { return w.Fix == wp.Fix }); idx != -1 {
					// This is a little messy: there are a handful of
					// modifiers we would like to carry over if they are
					// set though in general the waypoint from the approach
					// takes priority for things like altitude, speed, etc.
					nopt := navwp[idx].NoPT()
					humanHandoff := navwp[idx].HumanHandoff()
					tcpHandoff := navwp[idx].HandoffController()
					clearapp := navwp[idx].ClearApproach()

					// Keep the waypoints up to but not including the match.
					navwp = navwp[:idx]
					// Add the approach waypoints; take the matching one from there.
					navwp = append(navwp, waypoints[i:]...)
					// And add the destination airport again at the end.
					navwp = append(navwp, nav.FlightState.ArrivalAirport)

					navwp[idx].SetNoPT(nopt)
					navwp[idx].SetHumanHandoff(humanHandoff)
					navwp[idx].InitExtra().HandoffController = tcpHandoff
					navwp[idx].SetClearApproach(clearapp)

					// Update the deferred waypoints if present (as they're
					// what we got from AssignedWaypoints() above) and
					// otherwise the regular ones. Arguably we'd like to
					// defer the route change but don't have a way to do
					// that that preserves the current assigned heading, etc.
					if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
						dh.Waypoints = navwp
					} else {
						nav.Waypoints = navwp
					}

					found = true
					break
				}
			}

			if !found {
				// Most likely they were told to expect one runway, then
				// given a different one, but after they passed the common
				// set of waypoints on the arrival.  We'll replace the
				// waypoints but leave them on their current heading; then
				// it's over to the controller to either vector them or
				// send them direct somewhere reasonable...
				lg.Info("aircraft waypoints don't match up with arrival runway waypoints. splicing...",
					slog.Any("aircraft", nav.Waypoints),
					slog.Any("runway", waypoints))
				nav.Waypoints = append(util.DuplicateSlice(waypoints), nav.FlightState.ArrivalAirport)

				hdg := nav.FlightState.Heading
				nav.Heading = NavHeading{Assigned: &hdg}
				nav.DeferredNavHeading = nil
			}
		}
	}

	return av.ApproachIntent{
		Type:         av.ApproachExpect,
		ApproachName: ap.FullName,
		LAHSORunway:  lahsoRunway,
	}
}

func (nav *Nav) InterceptApproach(airport string, lg *log.Logger) av.CommandIntent {
	if nav.Approach.AssignedId == "" {
		return av.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	if _, onHeading := nav.AssignedHeading(); !onHeading {
		wps := nav.AssignedWaypoints()
		if len(wps) == 0 || !wps[0].OnApproach() {
			return av.MakeUnableIntent("unable. we have to be on a heading or direct to an approach fix to intercept")
		}
	}

	if intent := nav.prepareForApproach(false); intent != nil {
		return intent
	}

	ap := nav.Approach.Assigned
	if ap.Type == av.ILSApproach || ap.Type == av.LocalizerApproach {
		return av.ApproachIntent{
			Type:         av.ApproachIntercept,
			ApproachName: ap.FullName,
		}
	}
	return av.ApproachIntent{
		Type:         av.ApproachJoin,
		ApproachName: ap.FullName,
	}
}

func (nav *Nav) AtFixCleared(fix, id string) av.CommandIntent {
	if nav.Approach.AssignedId == "" {
		return av.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned
	if ap == nil {
		return av.MakeUnableIntent("unable. We were never told to expect an approach")
	}
	if id != "" && nav.Approach.AssignedId != id {
		return av.MakeUnableIntent("unable. We were told to expect the {appr} approach.", ap.FullName)
	}

	if !slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return wp.Fix == fix }) {
		return av.MakeUnableIntent("unable. {fix} is not in our route", fix)
	}
	nav.Approach.AtFixClearedRoute = nil
	for _, route := range ap.Waypoints {
		for i, wp := range route {
			if wp.Fix == fix {
				nav.Approach.AtFixClearedRoute = util.DuplicateSlice(route[i:])
			}
		}
	}

	return av.ApproachIntent{
		Type:         av.ApproachAtFixCleared,
		ApproachName: ap.FullName,
		Fix:          fix,
	}
}

func (nav *Nav) AtFixIntercept(fix, airport string, lg *log.Logger) av.CommandIntent {
	if nav.Approach.AssignedId == "" {
		return av.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned
	if ap == nil {
		return av.MakeUnableIntent("unable. We were never told to expect an approach")
	}

	if !slices.ContainsFunc(nav.AssignedWaypoints(), func(wp av.Waypoint) bool { return wp.Fix == fix }) {
		return av.MakeUnableIntent("unable. {fix} is not in our route", fix)
	}

	// Store the fix where the aircraft should intercept
	nav.Approach.AtFixInterceptFix = fix

	return av.ApproachIntent{
		Type:         av.ApproachAtFixIntercept,
		ApproachName: ap.FullName,
		Fix:          fix,
	}
}

func (nav *Nav) prepareForApproach(straightIn bool) av.CommandIntent {
	if nav.Approach.AssignedId == "" {
		return av.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned

	// Charted visual is special in all sorts of ways
	if ap.Type == av.ChartedVisualApproach {
		return nav.prepareForChartedVisual()
	}

	directApproachFix := false
	_, assignedHeading := nav.AssignedHeading()
	if !assignedHeading {
		// See if any of the waypoints in our route connect to the approach
		navwps := nav.AssignedWaypoints()
	outer:
		for i, wp := range navwps {
			for _, app := range ap.Waypoints {
				if idx := slices.IndexFunc(app, func(awp av.Waypoint) bool { return wp.Fix == awp.Fix }); idx != -1 {
					// Splice the routes
					directApproachFix = true
					navwps = append(navwps[:i], app[idx:]...)
					navwps = append(navwps, nav.FlightState.ArrivalAirport)

					if dh := nav.DeferredNavHeading; dh != nil && len(dh.Waypoints) > 0 {
						dh.Waypoints = navwps
					} else {
						nav.Waypoints = navwps
					}
					break outer
				}
			}
		}
	}

	if directApproachFix {
		// all good
	} else if assignedHeading {
		nav.Approach.InterceptState = InitialHeading
	} else {
		return av.MakeUnableIntent("unable. We need either direct or a heading to intercept")
	}
	// If the aircraft is on a heading, there's nothing more to do for
	// now; keep flying the heading and after we intercept we'll add
	// the rest of the waypoints to the aircraft's waypoints array.

	// No procedure turn if it intercepts via a heading or we're coming off a hold.
	nav.Approach.NoPT = straightIn || assignedHeading || nav.Heading.Hold != nil

	return nil
}

func (nav *Nav) prepareForChartedVisual() av.CommandIntent {
	// Airport PostDeserialize() checks that there is just a single set of
	// waypoints for charted visual approaches.
	wp := nav.Approach.Assigned.Waypoints[0]

	// First try to find the first (if any) waypoint along the approach
	// that is within 15 degrees of the aircraft's current heading.
	intercept := -1
	for i := range wp {
		h := math.Heading2LL(nav.FlightState.Position, wp[i].Location,
			nav.FlightState.NmPerLongitude, nav.FlightState.MagneticVariation)

		if math.HeadingDifference(h, nav.FlightState.Heading) < 30 {
			intercept = i
			break
		}
	}

	// Also check for intercepting a segment of the approach. There are two
	// cases:
	// 1. If we found a waypoint intercept above, then we are only
	//    interested in the segment from that waypoint to the subsequent
	//    one; we will take that if we find it (so the aircraft can stay
	//    on its present heading) but will not take a later one (so that it
	//    gets on the approach sooner rather than later.)
	// 2. If no waypoint intercept is found, we will take the first
	//    intercept with an approach segment. This case should be unusual
	//    but may come into play when an aircraft is very close to the
	//    approach route and no waypoints are close to its current course.

	// Work in nm coordinates
	pac0 := math.LL2NM(nav.FlightState.Position, nav.FlightState.NmPerLongitude)
	// Find a second point along its current course (note: ignoring wind)
	hdg := nav.FlightState.Heading - nav.FlightState.MagneticVariation
	dir := math.SinCos(math.Radians(hdg))
	pac1 := math.Add2f(pac0, dir)

	checkSegment := func(i int) *av.Waypoint {
		if i+1 == len(wp) {
			return nil
		}
		pl0 := math.LL2NM(wp[i].Location, nav.FlightState.NmPerLongitude)
		pl1 := math.LL2NM(wp[i+1].Location, nav.FlightState.NmPerLongitude)

		if pi, ok := math.LineLineIntersect(pac0, pac1, pl0, pl1); ok {
			// We only want intersections along the segment from pl0 to pl1
			// and not along the infinite line they define, so this is a
			// hacky check to limit to that.
			if math.Extent2DFromPoints([][2]float32{pl0, pl1}).Inside(pi) {
				return &av.Waypoint{
					Fix:      "intercept",
					Location: math.NM2LL(pi, nav.FlightState.NmPerLongitude),
				}
			}
		}
		return nil
	}

	// wi will store the route the aircraft will fly if it is going to join
	// the approach.
	var wi []av.Waypoint

	if intercept == -1 { // check all of the segments
		for i := range wp {
			if w := checkSegment(i); w != nil {
				// Take the first one that works
				wi = append([]av.Waypoint{*w}, wp[i+1:]...)
				break
			}
		}
	} else {
		// Just check the segment after the waypoint we're considering
		if w := checkSegment(intercept); w != nil {
			wi = append([]av.Waypoint{*w}, wp[intercept+1:]...)
		} else {
			// No problem if it doesn't intersect that segment; just start
			// the route from that waypoint.
			wi = wp[intercept:]
		}
	}

	if wi == nil {
		return av.MakeUnableIntent("unable. We are not on course to intercept the approach")
	}

	// Update the route and go direct to the intercept point.
	nav.Waypoints = append(wi, nav.FlightState.ArrivalAirport)
	nav.Heading = NavHeading{}
	nav.DeferredNavHeading = nil
	return nil
}

func (nav *Nav) ClearedApproach(airport string, id string, straightIn bool, simTime time.Time) (av.CommandIntent, bool) {
	ap := nav.Approach.Assigned
	if ap == nil {
		return av.MakeUnableIntent("unable. We haven't been told to expect an approach"), false
	}
	if id != "" && nav.Approach.AssignedId != id {
		return av.MakeUnableIntent("unable. We were told to expect the {appr} approach.", ap.FullName), false
	}

	if intent := nav.prepareForApproach(straightIn); intent != nil {
		return intent, false
	}

	nav.Approach.Cleared = true
	nav.Approach.StandbyApproach = false
	if nav.Approach.PassedApproachFix {
		// We've already passed an approach fix, so allow it to start descending.
		nav.Altitude = NavAltitude{}
	} else if nav.Approach.InterceptState == OnApproachCourse {
		// First intercepted then cleared or otherwise passed an
		// approach fix, so allow it to start descending.
		nav.Altitude = NavAltitude{}
		// No procedure turn needed if we were vectored to intercept.
		nav.Approach.NoPT = true
	}
	// Cleared approach also cancels speed restrictions.
	nav.Speed = NavSpeed{}

	// Follow LNAV instructions more quickly given an approach clearance;
	// assume that at this point they are expecting them and ready to dial things in.
	if dh := nav.DeferredNavHeading; dh != nil {
		if dh.Time.Sub(simTime) > 6*time.Second {
			dh.Time = simTime.Add(time.Duration((3 + 3*nav.Rand.Float32()) * float32(time.Second)))
		}
	}

	nav.flyProcedureTurnIfNecessary()

	cancelHold := nav.Heading.Hold != nil
	if nav.Heading.Hold != nil {
		nav.Heading.Hold.Cancel = true
	}

	return av.ClearedApproachIntent{
		Approach:   ap.FullName,
		StraightIn: straightIn,
		CancelHold: cancelHold,
	}, true
}

// buildDirectVisualWaypoints returns a route for a visual approach to
// the given runway. When the aircraft is roughly aligned with the
// extended centerline (within ~1.5nm cross-track), it produces a
// 2-waypoint path: 3nm final then threshold. When offset further, it
// inserts a base-turn waypoint at the aircraft's lateral offset so the
// pilot flies a realistic base-to-final turn. Returns nil if the
// runway is not found or if the first waypoint is behind the aircraft
// (meaning it should go around instead).
func (nav *Nav) buildDirectVisualWaypoints(runway string) []av.Waypoint {
	rwy, ok := av.LookupRunway(nav.FlightState.ArrivalAirport.Fix, runway)
	if !ok {
		return nil
	}

	nmPerLong := nav.FlightState.NmPerLongitude
	magVar := nav.FlightState.MagneticVariation

	alt := rwy.Elevation + rwy.ThresholdCrossingHeight
	threshold := math.Offset2LL(rwy.Threshold, rwy.Heading, rwy.DisplacedThresholdDistance,
		nmPerLong, magVar)

	reciprocal := math.NormalizeHeading(rwy.Heading + 180)
	final3nm := math.Offset2LL(threshold, reciprocal, 3, nmPerLong, magVar)

	// Work in nm-space to compute cross-track offset from extended centerline.
	thresholdNM := math.LL2NM(threshold, nmPerLong)
	outboundNM := math.LL2NM(final3nm, nmPerLong)
	acNM := math.LL2NM(nav.FlightState.Position, nmPerLong)

	// Signed distance: negative = left of outbound direction, positive = right.
	crossTrack := math.SignedPointLineDistance(acNM, thresholdNM, outboundNM)

	// Glideslope intercept altitude at 3nm (~3° slope ≈ 955ft AGL, rounded to 900).
	final3nmAlt := float32(rwy.Elevation) + 900

	var wps []av.Waypoint

	if math.Abs(crossTrack) > 1.5 {
		// Aircraft is offset from the centerline. Insert a base-turn
		// waypoint so the pilot flies a realistic turn onto final.
		// Place it along the centerline at 4.5nm from threshold, then
		// offset perpendicular by the cross-track distance.
		centerlineDir := math.Normalize2f(math.Sub2f(outboundNM, thresholdNM))
		baseOnCenterlineNM := math.Add2f(thresholdNM, math.Scale2f(centerlineDir, 4.5))

		// Perpendicular-left direction (90° CCW rotation).
		perpLeft := [2]float32{-centerlineDir[1], centerlineDir[0]}
		// Negate crossTrack because SignedPointLineDistance returns
		// positive for points to the right of the outbound direction
		// in nm-space, but perpLeft points left.
		baseNM := math.Add2f(baseOnCenterlineNM, math.Scale2f(perpLeft, -crossTrack))
		baseLoc := math.NM2LL(baseNM, nmPerLong)

		// Go-around check: if the first waypoint is behind the aircraft,
		// it can't set up a stable approach.
		bearingToBase := math.Heading2LL(nav.FlightState.Position, baseLoc, nmPerLong, magVar)
		if math.HeadingDifference(bearingToBase, nav.FlightState.Heading) > 90 {
			return nil
		}

		base := av.Waypoint{
			Fix:      "_" + runway + "_BASE",
			Location: baseLoc,
		}
		base.SetOnApproach(true)
		wps = append(wps, base)
	} else {
		// Roughly aligned. Go-around check on the 3nm final point.
		bearingTo3nm := math.Heading2LL(nav.FlightState.Position, final3nm, nmPerLong, magVar)
		if math.HeadingDifference(bearingTo3nm, nav.FlightState.Heading) > 90 {
			return nil
		}
	}

	finalWp := av.Waypoint{
		Fix:      "_" + runway + "_3NM_FINAL",
		Location: final3nm,
	}
	finalWp.SetOnApproach(true)
	finalWp.SetAltitudeRestriction(av.AltitudeRestriction{Range: [2]float32{final3nmAlt, final3nmAlt}})

	thresholdWp := av.Waypoint{
		Fix:      "_" + runway + "_THRESHOLD",
		Location: threshold,
	}
	thresholdWp.SetOnApproach(true)
	thresholdWp.SetLand(true)
	thresholdWp.SetFlyOver(true)
	thresholdWp.SetAltitudeRestriction(av.AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}})

	wps = append(wps, finalWp, thresholdWp)

	return wps
}

// ClearedDirectVisual sets up the aircraft to fly a visual approach to
// the runway threshold. The aircraft flies a 3nm final aligned with the
// runway heading to the threshold. Returns (intent, true) on success.
// Returns (nil, false) if the approach can't be set up (runway unknown
// or aircraft too close for a stable approach) — the caller should
// trigger a go-around.
func (nav *Nav) ClearedDirectVisual(runway string, simTime time.Time) (av.CommandIntent, bool) {
	wi := nav.buildDirectVisualWaypoints(runway)
	if wi == nil {
		return nil, false
	}

	// Cancel hold before clearing nav state.
	cancelHold := nav.Heading.Hold != nil
	if nav.Heading.Hold != nil {
		nav.Heading.Hold.Cancel = true
	}

	nav.Waypoints = wi
	nav.Heading = NavHeading{}
	nav.DeferredNavHeading = nil

	// Synthesize an Approach so downstream consumers (go-around,
	// spacing checks, landing bookkeeping, departure scheduling) have
	// the runway data they need.
	rwy, _ := av.LookupRunway(nav.FlightState.ArrivalAirport.Fix, runway)
	opp, _ := av.LookupOppositeRunway(nav.FlightState.ArrivalAirport.Fix, runway)
	nav.Approach.Assigned = &av.Approach{
		Id:                "V" + runway,
		FullName:          "Visual Approach Runway " + runway,
		Runway:            runway,
		Threshold:         rwy.Threshold,
		OppositeThreshold: opp.Threshold,
	}
	nav.Approach.AssignedId = "V" + runway

	// Mark as cleared and allow descent.
	nav.Approach.Cleared = true
	nav.Altitude = NavAltitude{}
	nav.Speed = NavSpeed{}

	return av.ClearedApproachIntent{
		Approach:   "Visual Approach Runway " + runway,
		CancelHold: cancelHold,
	}, true
}
