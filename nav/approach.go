// nav/approach.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/wx"
)

// Approach is the approach the aircraft has been told to expect and how far it
// has gotten with it.
type Approach struct {
	// What was assigned: the approach, its ATPA volume, and for a visual the
	// approaches it is flown by reference to and the one it has been
	// committed to.
	Assigned         *av.Approach
	AssignedId       string
	ATPAVolume       *av.ATPAVolume
	VisualReferences []*av.Approach // Reference approaches for non-charted visual approach

	// The clearance. Abandoned is set when the approach fell through, by an
	// intercept the aircraft couldn't make or a clearance the controller
	// cancelled; the aircraft keeps its speed as if it were still on it.
	Cleared         bool
	StandbyApproach bool // suppress repeated approach clearance requests
	NoPT            bool
	Abandoned       bool

	// Instructions that take effect at a fix.
	AtFixClearedRoute []av.Waypoint
	AtFixInterceptFix string // fix where aircraft should intercept the localizer

	// Joining the approach course from a heading.
	InterceptState InterceptState
	Intercept      InterceptCourse // the course being joined, once the turn onto it has started

	// Progress along the approach.
	PassedApproachFix bool // have we passed a fix on the approach yet?
	PassedFAF         bool
}

// EffectivelyCleared reports whether the aircraft has been cleared for the
// approach, either immediately or via an "at fix" clearance that hasn't yet
// triggered at the fix.
func (na *Approach) EffectivelyCleared() bool {
	return na.Cleared || na.AtFixClearedRoute != nil
}

// HasLocalizer reports whether the approach is flown on a localizer: its own,
// or for a visual one of its references'.
func (na *Approach) HasLocalizer() bool {
	if na.Assigned == nil {
		return false
	}
	return na.Assigned.Type.HasLocalizer() ||
		slices.ContainsFunc(na.VisualReferences, func(ap *av.Approach) bool { return ap.Type.HasLocalizer() })
}

func (nav *Nav) getApproach(airport *av.Airport, id string) (*av.Approach, []*av.Approach, error) {
	if id == "" {
		return nil, nil, ErrInvalidApproach
	}

	if runway, visual := strings.CutPrefix(id, "_VIS"); visual {
		arrICAO := av.ICAOAirportCode(nav.FlightState.ArrivalAirport.Fix)
		rwy, ok := nav.lookup().Runway(arrICAO, runway)
		if !ok {
			return nil, nil, ErrInvalidApproach
		}
		opp, ok := nav.lookup().OppositeRunway(arrICAO, runway)
		if !ok {
			opp.Threshold = rwy.Threshold
		}

		references := selectVisualReferences(airport, runway)
		if len(references) == 0 {
			return nil, nil, ErrInvalidApproach
		}

		var allApproachRoutes []av.WaypointArray
		for _, ref := range references {
			for _, route := range ref.Waypoints {
				allApproachRoutes = append(allApproachRoutes, stripProcedureTurns(route))
			}
		}

		return &av.Approach{
			Id:                "_VIS" + runway,
			FullName:          "Visual Approach Runway " + runway,
			Type:              av.VisualApproach,
			Runway:            runway,
			Threshold:         rwy.Threshold,
			OppositeThreshold: opp.Threshold,
			Waypoints:         allApproachRoutes,
			ATPAVolumeId:      visualATPAVolumeId(airport, runway, references),
		}, references, nil
	}

	appr, ok := airport.Approaches[id]
	if !ok {
		return nil, nil, ErrUnknownApproach
	}
	var refs []*av.Approach
	if appr.Type == av.VisualApproach {
		// Named visual approach: its own waypoints are the route the
		// pilot will fly, so they serve as the sole reference for
		// ClearedVisualApproach's route synthesis.
		refs = []*av.Approach{appr}
	}
	return appr, refs, nil
}

// ExpectedApproach is what ExpectApproach needs to know of an approach, as
// ResolveApproach finds it at the airport.
type ExpectedApproach struct {
	Approach *av.Approach
	// References are the approaches a visual approach is flown by
	// reference to; see selectVisualReferences.
	References []*av.Approach
	ATPAVolume *av.ATPAVolume
}

// ResolveApproach finds the approach that approach names at the airport, with
// or without a "/LAHSO" runway suffix, for ExpectApproach. If the airport has
// no such approach, it returns the pilot's "unable" instead.
func (nav *Nav) ResolveApproach(airport *av.Airport, approach string) (ExpectedApproach, speech.CommandIntent) {
	id, _, _ := strings.Cut(approach, "/LAHSO")
	ap, refs, err := nav.getApproach(airport, id)
	if err != nil {
		if rwy, visual := strings.CutPrefix(id, "_VIS"); visual {
			return ExpectedApproach{}, speech.MakeUnableIntent("unable, we don't know runway " + rwy)
		}
		return ExpectedApproach{}, speech.MakeUnableIntent("unable. We don't know the {appr} approach.", id)
	}
	return ExpectedApproach{Approach: ap, References: refs, ATPAVolume: airport.ATPAVolumes[ap.ATPAVolumeId]}, nil
}

// ExpectApproach tells the pilot to expect the approach ResolveApproach
// found for the name. runwayWaypoints gives the route from the arrival the
// aircraft is flying to each runway, if the arrival has them.
func (nav *Nav) ExpectApproach(approach string, ea ExpectedApproach, runwayWaypoints map[string]av.WaypointArray) speech.CommandIntent {
	defer nav.record("ExpectApproach", approach, ea, runwayWaypoints)()
	id, lahsoRunway, _ := strings.Cut(approach, "/LAHSO")

	if lahsoRunway != "" {
		if _, ok := nav.lookup().Runway(av.ICAOAirportCode(nav.FlightState.ArrivalAirport.Fix), lahsoRunway); !ok {
			return speech.MakeUnableIntent("unable, we don't know that hold-short runway")
		}
	}

	ap := ea.Approach
	if id == nav.Approach.AssignedId && nav.Approach.Assigned != nil {
		nav.Approach.StandbyApproach = true
		return speech.ApproachIntent{
			Type:         speech.ApproachExpect,
			ApproachName: ap.FullName,
			LAHSORunway:  lahsoRunway,
		}
	}

	nav.Approach = Approach{
		Assigned:         ap,
		AssignedId:       id,
		ATPAVolume:       ea.ATPAVolume,
		VisualReferences: ea.References,
	}

	if waypoints := runwayWaypoints[ap.Runway]; len(waypoints) > 0 {
		if len(nav.Waypoints) == 0 {
			// Nothing left on our route; this shouldn't ever happen but
			// just in case patch the runway waypoints in there and hope it
			// works out.
			nav.Waypoints = append(waypoints[1:].Clone(), nav.FlightState.ArrivalAirport)
		} else {
			// Try to splice the runway-specific waypoints in with the
			// aircraft's current waypoints. This overwrites the deferred
			// route if a controller instruction is pending; arguably we'd
			// like to defer the route change itself but don't have a way to
			// do that that preserves the current assigned heading, etc.
			navwps := nav.AssignedWaypoints()
			found := false
			for i, wp := range waypoints {
				idx := slices.IndexFunc(navwps, func(w av.Waypoint) bool { return w.Fix == wp.Fix })
				if idx == -1 {
					continue
				}
				nav.setAssignedWaypoints(nav.spliceApproachRoute(navwps, idx, waypoints[i:]))
				found = true
				break
			}

			if !found && !nav.hasDeferredRoute() {
				// Most likely they were told to expect one runway, then
				// given a different one, but after they passed the common
				// set of waypoints on the arrival.  We'll replace the
				// waypoints but leave them on their current heading; then
				// it's over to the controller to either vector them or
				// send them direct somewhere reasonable... A pending direct
				// is newer than these waypoints, though, so when there is
				// one it is kept instead.
				nav.Waypoints = append(waypoints.Clone(), nav.FlightState.ArrivalAirport)

				if _, ok := nav.AssignedHeading(); !ok {
					hdg := nav.FlightState.Heading
					nav.Heading = Heading{Assigned: &hdg}
					nav.DeferredNavHeading = nil
				}
			}
		}
	}

	return speech.ApproachIntent{
		Type:         speech.ApproachExpect,
		ApproachName: ap.FullName,
		LAHSORunway:  lahsoRunway,
	}
}

// approachRouteThrough returns the approach transition route that includes fix
// and the fix's index in it; the route is nil if ap is nil, fix is empty, or no
// transition includes it. A transition where the fix has a procedure turn wins:
// because of the way we currently interpret ARINC424 files, fixes with
// procedure turns have no procedure turn for routes with /nopt from the
// previous fix, so a fix may appear in several transitions both with and
// without one.
func approachRouteThrough(ap *av.Approach, fix string) (av.WaypointArray, int) {
	if ap == nil || fix == "" {
		return nil, 0
	}
	var best av.WaypointArray
	var bestIdx int
	for _, route := range ap.Waypoints {
		idx := slices.IndexFunc(route, func(wp av.Waypoint) bool { return wp.Fix == fix })
		if idx == -1 {
			continue
		}
		if route[idx].ProcedureTurn() != nil {
			return route, idx
		}
		if best == nil {
			best, bestIdx = route, idx
		}
	}
	return best, bestIdx
}

// spliceApproachRoute returns the route that follows navwps up to navwps[idx],
// where it joins apwps, which starts at that same fix, and then continues to
// the arrival airport. The approach's waypoint takes over at the shared fix
// but keeps the route's own actions there. The result shares nothing with the
// approach, which the aircraft does not own.
func (nav *Nav) spliceApproachRoute(navwps []av.Waypoint, idx int, apwps av.WaypointArray) av.WaypointArray {
	wps := slices.Concat(navwps[:idx], apwps.Clone(), av.WaypointArray{nav.FlightState.ArrivalAirport})
	wps[idx] = apwps[0].CarryOverActions(navwps[idx])
	return wps
}

// joinApproach splices the assigned approach into the aircraft's route where
// the two meet, returning the index in the route of the first approach
// waypoint and whether it found such a fix. joinFix, if non-empty, names a
// fix the aircraft has just crossed and so dropped from its route; the
// approach is joined there, picking up after it.
func (nav *Nav) joinApproach(joinFix string) (int, bool) {
	ap := nav.Approach.Assigned

	if route, idx := approachRouteThrough(ap, joinFix); route != nil {
		nav.setAssignedWaypoints(slices.Concat(route[idx+1:].Clone(),
			av.WaypointArray{nav.FlightState.ArrivalAirport}))
		// Having crossed the fix counts as passing an approach fix, which
		// lets a clearance start the descent right away.
		nav.Approach.PassedApproachFix = true
		return 0, true
	}

	navwps := nav.AssignedWaypoints()
	for i, wp := range navwps {
		if route, idx := approachRouteThrough(ap, wp.Fix); route != nil {
			nav.setAssignedWaypoints(nav.spliceApproachRoute(navwps, i, route[idx:]))
			return i, true
		}
	}
	return 0, false
}

// prepareForApproach sets up the aircraft to fly the assigned approach, either
// by splicing it into the route or by leaving the aircraft on a heading to
// intercept. joinFix has the same meaning as in joinApproach.
func (nav *Nav) prepareForApproach(straightIn bool, joinFix string) speech.CommandIntent {
	if nav.Approach.AssignedId == "" {
		return speech.MakeUnableIntent("unable. you never told us to expect an approach")
	}

	ap := nav.Approach.Assigned
	charted := ap.Type == av.ChartedVisualApproach
	if charted && nav.keepsArcOntoChartedVisual(joinFix) {
		return nil
	}

	_, assignedHeading := nav.AssignedHeading()
	if assignedHeading {
		// Keep flying the heading; the aircraft joins the approach where
		// the heading meets one of its courses and takes up the rest of
		// the route there. Only a charted visual answers now if it never
		// will: an instrument approach's pilot asks for vectors then.
		if charted {
			course, ok := nav.selectInterceptCourse(wx.MakeStandardSampleForAltitude(nav.FlightState.Altitude))
			if !ok {
				return nav.flyChartedVisualAhead()
			}
			// Until the aircraft is on the leg, the chart from where it
			// is expected to join gives it its descent.
			join := av.Waypoint{Fix: "intercept", Location: course.Join}
			nav.Waypoints = slices.Concat([]av.Waypoint{join}, course.Route[course.Index+1:].Clone(),
				[]av.Waypoint{nav.FlightState.ArrivalAirport})
			nav.Approach.PassedApproachFix = true // allow descent
		}
		nav.Approach.InterceptState = InitialHeading
	} else if _, ok := nav.joinApproach(joinFix); ok {
		// The aircraft is going direct to an approach fix; clear any
		// OnApproachCourse state that DirectFix may have set (which
		// is used to gate altitude before clearance). Without this,
		// ClearedApproach incorrectly sets NoPT when it sees
		// OnApproachCourse, skipping the procedure turn.
		nav.Approach.InterceptState = NotIntercepting
		if charted {
			nav.Approach.PassedApproachFix = true // allow descent
		}
	} else if charted {
		if unable := nav.flyChartedVisualAhead(); unable != nil {
			return unable
		}
	} else {
		return speech.MakeUnableIntent("unable. We need either direct or a heading to intercept")
	}

	// No procedure turn if it intercepts via a heading or we're coming off a hold.
	nav.Approach.NoPT = straightIn || assignedHeading || nav.Heading.Hold != nil

	return nil
}

// flyChartedVisualAhead sends a pilot whose instructions meet none of the
// charted visual's legs to the fix of the chart they are pointed at, if there
// is one; otherwise it returns their unable.
func (nav *Nav) flyChartedVisualAhead() speech.CommandIntent {
	route := nav.chartedVisualRouteAhead()
	if route == nil {
		return speech.MakeUnableIntent("unable. We are not on course to intercept the approach")
	}
	nav.Waypoints = append(route.Clone(), nav.FlightState.ArrivalAirport)
	nav.Heading = Heading{}
	nav.DeferredNavHeading = nil
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.PassedApproachFix = true // allow descent
	return nil
}

// ClearedApproach issues an approach clearance. joinFix has the same meaning
// as in joinApproach and is empty for a controller-issued clearance.
func (nav *Nav) ClearedApproach(approach string, traffic *FollowTraffic, simTime Time, delayReduction time.Duration,
	straightIn bool, joinFix string) speech.CommandIntent {
	defer nav.record("ClearedApproach", approach, traffic, simTime, delayReduction, straightIn, joinFix)()
	ap := nav.Approach.Assigned
	if ap == nil {
		return speech.MakeUnableIntent("unable. We haven't been told to expect an approach")
	}

	id, lahsoRunway, _ := strings.Cut(approach, "/LAHSO")
	if id != "" && nav.Approach.AssignedId != id {
		return speech.MakeUnableIntent("unable. We were told to expect the {appr} approach.", ap.FullName)
	}

	if ap.Type == av.VisualApproach {
		return nav.ClearedVisualApproach(traffic, lahsoRunway)
	}

	if nav.Approach.Cleared {
		cancelHold := nav.applyClearedApproachState()
		if straightIn {
			nav.Approach.NoPT = true
		}
		return speech.ClearedApproachIntent{
			Approach:    ap.FullName,
			StraightIn:  straightIn,
			CancelHold:  cancelHold,
			LAHSORunway: lahsoRunway,
		}
	}

	// InterceptState == OnApproachCourse also marks an aircraft sent direct
	// to an approach fix while uncleared (DirectFix), where NoPT is left
	// false so the fix's procedure turn still flies by default. Only the
	// pairing with NoPT already true distinguishes a genuine localizer
	// capture, which prepareForApproach is about to overwrite below.
	establishedNoPT := nav.Approach.InterceptState == OnApproachCourse && nav.Approach.NoPT

	if intent := nav.prepareForApproach(straightIn, joinFix); intent != nil {
		return intent
	}

	cancelHold := nav.applyClearedApproachState()
	if nav.Approach.PassedApproachFix {
		// We've already passed an approach fix, so allow it to start descending.
		nav.clearAltitudeForApproach()
	} else if establishedNoPT {
		// First intercepted then cleared or otherwise passed an
		// approach fix, so allow it to start descending.
		nav.clearAltitudeForApproach()
		// No procedure turn needed if we were vectored to intercept.
		nav.Approach.NoPT = true
	}

	// A pilot who has been cleared for the approach is quick to act on a
	// pending heading or direct; the clearance never delays one.
	if dh := nav.DeferredNavHeading; dh != nil {
		d := max(nav.drawDelay(nav.Delays.PendingApproachCleared)-delayReduction, 0)
		if t := simTime.Add(d); t.Before(dh.Time) {
			dh.Time = t
		}
	}

	nav.flyProcedureTurnIfNecessary()

	return speech.ClearedApproachIntent{
		Approach:    ap.FullName,
		StraightIn:  straightIn,
		CancelHold:  cancelHold,
		LAHSORunway: lahsoRunway,
	}
}

// applyClearedApproachState performs the nav-state reset common to every
// approach clearance: cancel any hold, clear speed restrictions, end a descend
// via floor, mark the approach as cleared and no longer standby. Returns true
// iff the aircraft was in a hold that is now being cancelled.
func (nav *Nav) applyClearedApproachState() (cancelHold bool) {
	if hold := nav.Heading.Hold; hold != nil {
		hold.Cancel = true
		cancelHold = true
	}
	nav.Approach.Cleared = true
	nav.Approach.StandbyApproach = false
	nav.Approach.Abandoned = false
	nav.Speed = Speed{}
	// The approach's altitudes may take the aircraft below a descend via
	// floor.
	if c := nav.Altitude.Cleared; c != nil {
		nav.Altitude.Cleared = &ClearedAltitude{Altitude: c.Altitude}
	}
	return
}

func (nav *Nav) CancelApproachClearance() speech.CommandIntent {
	defer nav.record("CancelApproachClearance")()
	if !nav.Approach.Cleared {
		return speech.MakeUnableIntent("unable. we're not currently cleared for an approach")
	}

	nav.Approach.Cleared = false
	nav.Approach.InterceptState = NotIntercepting
	nav.Approach.NoPT = false
	nav.Approach.Abandoned = true

	return speech.ApproachIntent{Type: speech.ApproachCancel}
}

// AtFixCleared clears the aircraft for the approach once it passes fix. An
// aircraft flying its route must have the fix on it. One on a heading must
// be intercepting the approach course and join it before the fix; it keeps
// its altitude on the course until the clearance takes effect there.
func (nav *Nav) AtFixCleared(fix, id string, straightIn bool) speech.CommandIntent {
	defer nav.record("AtFixCleared", fix, id, straightIn)()
	ap := nav.Approach.Assigned
	if ap == nil {
		return speech.MakeUnableIntent("unable. you never told us to expect an approach")
	}
	if id != "" && nav.Approach.AssignedId != id {
		return speech.MakeUnableIntent("unable. We were told to expect the {appr} approach.", ap.FullName)
	}

	route, idx := approachRouteThrough(ap, fix)
	if route == nil {
		return speech.MakeUnableIntent("unable. {fix} is not on the {appr} approach", fix, ap.FullName)
	}
	if nav.Approach.Cleared {
		return speech.MakeUnableIntent("we're already cleared for the {appr} approach", ap.FullName)
	}

	isFix := func(wp av.Waypoint) bool { return wp.Fix == fix }
	if _, onHeading := nav.AssignedHeading(); onHeading {
		if !nav.interceptingFromHeading() {
			return speech.MakeUnableIntent("unable. We're on a heading")
		}
		if !slices.ContainsFunc(nav.waypointsAfterIntercept(), isFix) {
			return speech.MakeUnableIntent("unable. We won't cross {fix} on this intercept", fix)
		}
	} else if !slices.ContainsFunc(nav.AssignedWaypoints(), isFix) {
		return speech.MakeUnableIntent("unable. {fix} is not in our route", fix)
	}

	nav.Approach.AtFixClearedRoute = route[idx:].Clone()
	if straightIn && len(nav.Approach.AtFixClearedRoute) > 0 {
		nav.Approach.AtFixClearedRoute[0].SetNoPT(true)
	}

	return speech.ApproachIntent{
		Type:         speech.ApproachAtFixCleared,
		ApproachName: ap.FullName,
		Fix:          fix,
		StraightIn:   straightIn,
	}
}

// clearApproachAtFix puts an "at fix, cleared approach" clearance into
// effect if wp is its fix: the aircraft is cleared and flies the approach
// from there. It reports whether it did.
func (nav *Nav) clearApproachAtFix(wp av.Waypoint) bool {
	route := nav.Approach.AtFixClearedRoute
	if route == nil || route[0].Fix != wp.Fix {
		return false
	}

	nav.Approach.Cleared = true
	nav.Speed = Speed{}
	if wp.NoPT() || route[0].NoPT() {
		nav.Approach.NoPT = true
	}
	if ap := nav.Approach.Assigned; ap.Type == av.VisualApproach {
		// The visual's profile runs from the fix.
		route = nav.visualRouteFromJoin(ap.Runway, nav.visualJoinAt(route, route[0].Location), nil)
	}
	nav.Waypoints = append(route, nav.FlightState.ArrivalAirport)
	nav.Approach.AtFixClearedRoute = nil
	nav.Approach.StandbyApproach = false
	nav.flyProcedureTurnIfNecessary()
	return true
}

// passApproachFix is what passing wp means for the approach. A cleared
// aircraft has made it to the approach fix it was cleared to and may start
// down the approach's altitudes. One established on the course without a
// clearance goes around at the FAF, asks for the clearance at the IF, and
// asks early at an earlier fix if it is too high to make the FAF.
func (nav *Nav) passApproachFix(wp av.Waypoint) {
	if nav.Approach.Cleared {
		nav.clearAltitudeForApproach()
		nav.Approach.PassedApproachFix = true
		if wp.FAF() {
			nav.Approach.PassedFAF = true
		}
	} else if wp.OnApproach() {
		// Overflew an approach fix but haven't been cleared yet.
		nav.Approach.PassedApproachFix = true
	}

	if wp.FAF() && nav.InterceptedButNotCleared() {
		// At the FAF without clearance, go around.
		nav.Requests.GoAround = true
	} else if nav.InterceptedButNotCleared() && !nav.Approach.StandbyApproach {
		if wp.IF() {
			// At the IF, the pilot asks for approach clearance.
			nav.Requests.ApproachClearance = true
		} else if wp.OnApproach() {
			// At an intermediate approach fix, check if the altitude
			// makes descent to the FAF challenging (>300 ft/nm).
			nav.checkEarlyApproachRequest(&wp)
		}
	}
}

// checkEarlyApproachRequest checks whether the aircraft is too high to
// comfortably descend to the FAF at 300 ft/nm or less.  If so, it sets
// the pilot's request for approach clearance so that it asks early.
func (nav *Nav) checkEarlyApproachRequest(currentWp *av.Waypoint) {
	// Find the FAF in the remaining waypoints and compute the distance.
	var fafAlt float32
	var dist float32
	found := false
	prev := currentWp.Location
	for _, wp := range nav.Waypoints {
		d := math.NMDistance2LL(prev, wp.Location)
		dist += d
		prev = wp.Location
		if wp.FAF() {
			if ar := wp.AltitudeRestriction(); ar != nil {
				fafAlt = ar.Range[0]
			}
			found = true
			break
		}
	}
	if !found || fafAlt == 0 || dist < 0.1 {
		return
	}

	altToLose := nav.FlightState.Altitude - fafAlt
	if altToLose > 0 && altToLose/dist > 300 {
		nav.Requests.ApproachClearance = true
	}
}

func (nav *Nav) OnApproach(checkAltitude bool) bool {
	if !nav.Approach.Cleared {
		return false
	}

	// A visual approach clearance lets the pilot descend on their own
	// navigation, so they are on the approach from the clearance; on any
	// other, a vector takes them off it until they are established.
	if _, assigned := nav.AssignedHeading(); assigned && !nav.clearedForVisualApproach() {
		return false
	}

	// The aircraft either must have passed a fix on the approach or be on
	// the localizer and also be above any upcoming altitude restrictions.
	if !nav.Approach.PassedApproachFix && nav.Approach.InterceptState != OnApproachCourse {
		return false
	}

	if !checkAltitude {
		return true
	}

	for _, wp := range nav.Waypoints {
		// Ignore controller-assigned "cross FIX at ALT" for this
		if r := wp.AltitudeRestriction(); r != nil {
			return nav.FlightState.Altitude >= r.TargetAltitude(nav.FlightState.Altitude)
		}
	}
	return false
}

// DistanceToEndOfApproach returns the remaining distance to the last
// waypoint (usually runway threshold) of the currently assigned approach.
func (nav *Nav) DistanceToEndOfApproach() (float32, error) {
	if nav.Approach.Assigned == nil || !nav.Approach.Cleared {
		return 0, ErrNotClearedForApproach
	}

	if nav.Heading.Assigned != nil {
		// We're not currently on the route, so it's a little unclear. Rather than
		// guessing, we'll just error out and let callers decide how to handle this.
		return 0, ErrNotFlyingRoute
	}

	// Calculate flying distance to the airport
	if wp := nav.Waypoints; len(wp) == 0 {
		// This shouldn't ever happen; we should always have the
		// destination airport, but just in case...
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, nav.FlightState.ArrivalAirportLocation)
		return remainingDistance, nil
	} else {
		// Distance to the next fix plus sum of the distances between
		// remaining fixes.
		remainingDistance := math.NMDistance2LL(nav.FlightState.Position, wp[0].Location)
		// Don't include the final waypoint, which should be the
		// destination airport.
		for i := 0; i < len(wp)-2; i++ {
			remainingDistance += math.NMDistance2LL(wp[i].Location, wp[i+1].Location)
		}

		return remainingDistance, nil
	}
}
