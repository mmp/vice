// nav/fix.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
)

// Controller instructions that take effect at a fix on the route: crossing
// restrictions, a heading or a direct to fly after the fix, and a speed or
// altitude to take up past it. FixAssignments keys them by fix and
// passWaypoint puts them into effect; a crossing at a distance from a fix
// becomes a synthetic waypoint on the route instead.

type FixAssignment struct {
	Arrive struct {
		Altitude *av.AltitudeRestriction
		Speed    *av.SpeedRestriction
	}
	Depart struct {
		Fix         *av.Waypoint
		Heading     *math.MagneticHeading
		Turn        *av.TurnDirection
		Speed       *av.SpeedRestriction
		CancelSpeed bool // cancel speed restrictions when passing this fix
		Altitude    *float32
	}
	Hold *av.Hold
}

// applyFixAssignments puts into effect what the controller said to do after
// wp: a speed to fly or to stop flying, a crossing speed held past the fix,
// and an altitude to climb or descend to.
func (nav *Nav) applyFixAssignments(wp av.Waypoint) {
	nfa, ok := nav.FixAssignments[wp.Fix]
	if !ok {
		return
	}

	if nfa.Depart.Speed != nil {
		sr := *nfa.Depart.Speed
		nav.Speed = Speed{Assigned: &sr}
	} else if nfa.Depart.CancelSpeed {
		nav.Speed = Speed{}
	} else if nfa.Arrive.Speed != nil && !wp.OnSID() && !nav.hasIssuedSpeed() {
		// The controller's crossing speed is held past the fix as an
		// assigned speed, which a via clearance cancels, unless a
		// later speed instruction has replaced it.
		sr := *nfa.Arrive.Speed
		nav.Speed.Assigned = &sr
	}

	if nfa.Depart.Altitude != nil {
		nav.AssignAltitudeNow(*nfa.Depart.Altitude, false)
	}
}

func (nav *Nav) DepartFixDirect(fixa string, fixb string) speech.CommandIntent {
	defer nav.record("DepartFixDirect", fixa, fixb)()
	fa, fb := nav.fixPairInRoute(fixa, fixb)
	if fa == nil {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fixa)
	}
	if fb == nil {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route after {fix}", fixb, fixa)
	}

	nfa := nav.FixAssignments[fixa]
	nfa.Depart.Fix = fb
	nav.FixAssignments[fixa] = nfa

	return speech.NavigationIntent{
		Type:      speech.NavDepartFixDirect,
		Fix:       fixa,
		SecondFix: fixb,
	}
}

func (nav *Nav) DepartFixHeading(fix string, hdg math.MagneticHeading) speech.CommandIntent {
	defer nav.record("DepartFixHeading", fix, hdg)()
	if hdg <= 0 || hdg > 360 {
		return speech.MakeUnableIntent("unable. Heading {hdg} is invalid", hdg)
	}
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	nfa := nav.FixAssignments[fix]
	h := hdg
	nfa.Depart.Heading = &h
	nav.FixAssignments[fix] = nfa

	return speech.NavigationIntent{
		Type:    speech.NavDepartFixHeading,
		Fix:     fix,
		Heading: hdg,
	}
}

func (nav *Nav) AfterFixSpeed(fix string, sr *av.SpeedRestriction) speech.CommandIntent {
	defer nav.record("AfterFixSpeed", fix, sr)()
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	nfa := nav.FixAssignments[fix]
	nfa.Depart.Speed = sr
	nav.FixAssignments[fix] = nfa

	if _, exact := sr.ExactValue(); !exact {
		intent := rangeSpeedIntent(sr)
		intent.AfterFix = fix
		return intent
	}
	spd := sr.Speed()
	return speech.SpeedIntent{Speed: spd, Type: nav.speedChangeType(spd), AfterFix: fix}
}

func (nav *Nav) AssignCompoundSpeed(segments []speech.CompoundSpeedSegment, temp av.Temperature) speech.CommandIntent {
	defer nav.record("AssignCompoundSpeed", segments, temp)()
	// Validate all segments before applying any state changes.
	for _, seg := range segments {
		if intent, ok := nav.checkAssignedSpeed(seg.Speed.Speed(), temp); !ok {
			return intent
		}

		if seg.UntilFix != "" && !nav.fixInRoute(seg.UntilFix) {
			return speech.MakeUnableIntent("unable. {fix} isn't in our route", seg.UntilFix)
		}
	}

	// Apply: first segment sets the current speed, subsequent segments
	// set after-fix speed assignments.
	nav.clearAfterFixSpeeds()
	nav.Speed = Speed{Assigned: segments[0].Speed}

	for i := 1; i < len(segments); i++ {
		fix := segments[i-1].UntilFix
		nfa := nav.FixAssignments[fix]
		nfa.Depart.Speed = segments[i].Speed
		nav.FixAssignments[fix] = nfa
	}

	// If the last segment has an UntilFix (no trailing open-ended speed),
	// cancel speed restrictions when the aircraft passes that fix.
	if last := segments[len(segments)-1]; last.UntilFix != "" {
		nfa := nav.FixAssignments[last.UntilFix]
		nfa.Depart.CancelSpeed = true
		nav.FixAssignments[last.UntilFix] = nfa
	}

	return speech.CompoundSpeedIntent{Segments: segments}
}

func (nav *Nav) clearAfterFixSpeeds() {
	for fix, nfa := range nav.FixAssignments {
		if nfa.Depart.Speed != nil || nfa.Depart.CancelSpeed {
			nfa.Depart.Speed = nil
			nfa.Depart.CancelSpeed = false
			nav.FixAssignments[fix] = nfa
		}
	}
}

func (nav *Nav) AfterFixAltitude(fix string, alt float32) speech.CommandIntent {
	defer nav.record("AfterFixAltitude", fix, alt)()
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}
	if alt > nav.Perf.Ceiling {
		return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
	}

	nfa := nav.FixAssignments[fix]
	nfa.Depart.Altitude = &alt
	nav.FixAssignments[fix] = nfa

	var direction speech.AltitudeDirection
	if alt > nav.FlightState.Altitude {
		direction = speech.AltitudeClimb
	} else if alt < nav.FlightState.Altitude {
		direction = speech.AltitudeDescend
	} else {
		direction = speech.AltitudeMaintain
	}

	return speech.AltitudeIntent{
		Altitude:  alt,
		Direction: direction,
		AfterFix:  fix,
	}
}

func (nav *Nav) clearFixAltitudes() {
	for fix, nfa := range nav.FixAssignments {
		if nfa.Depart.Altitude != nil {
			nfa.Depart.Altitude = nil
			nav.FixAssignments[fix] = nfa
		}
	}
}

func (nav *Nav) CrossFixAt(fix string, ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	defer nav.record("CrossFixAt", fix, ar, sr, temp)()
	if !nav.fixInRoute(fix) {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	intent := speech.NavigationIntent{
		Type: speech.NavCrossFixAt,
		Fix:  fix,
	}

	nfa := nav.FixAssignments[fix]
	if ar != nil {
		nfa.Arrive.Altitude = ar
		intent.AltRestriction = ar
		// Delete other altitude restrictions
		nav.Altitude = Altitude{}
	}
	if sr != nil {
		nfa.Arrive.Speed = sr
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS(temp)
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		// Delete other speed restrictions
		nav.Speed = Speed{}
	}
	nav.FixAssignments[fix] = nfa

	return intent
}

// newSyntheticWaypoint builds a synthetic crossing waypoint at the given
// location. If inheritFrom is non-nil, OnSID/OnSTAR/OnApproach flags are
// copied from it so the waypoint participates in the same route phase as its
// neighbor.
func newSyntheticWaypoint(name string, loc math.Point2LL, inheritFrom *av.Waypoint) av.Waypoint {
	wp := av.Waypoint{Fix: name, Location: loc}
	wp.SetSyntheticCrossing(true)
	if inheritFrom != nil {
		wp.SetOnSID(inheritFrom.OnSID())
		wp.SetOnSTAR(inheritFrom.OnSTAR())
		wp.SetOnApproach(inheritFrom.OnApproach())
	}
	return wp
}

// clearMatchingSyntheticRestrictions walks wps and, for each waypoint where
// match(wp) returns true, clears altitude restrictions if ar != nil and speed
// restrictions if sr != nil. Waypoints left with no inline restrictions are
// removed entirely. Returns the (possibly shorter) slice; callers must
// refresh any indices into the route after calling.
func clearMatchingSyntheticRestrictions(wps []av.Waypoint, match func(*av.Waypoint) bool,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction) []av.Waypoint {
	for i := 0; i < len(wps); {
		wp := &wps[i]
		if match(wp) {
			if ar != nil {
				wp.ClearAltitudeRestriction()
			}
			if sr != nil {
				wp.ClearSpeedRestriction()
			}
			if !wp.HasAltitudeRestriction() && !wp.HasSpeedRestriction() {
				wps = slices.Delete(wps, i, i+1)
				continue
			}
		}
		i++
	}
	return wps
}

// applyRestrictionsToSyntheticWaypoint sets the supplied altitude and speed
// restrictions on wp (when non-nil), records them on intent, and clears the
// corresponding nav.Altitude / nav.Speed assignments so the synthetic
// crossing supersedes any prior controller instruction of the same type.
func (nav *Nav) applyRestrictionsToSyntheticWaypoint(wp *av.Waypoint,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature, intent *speech.NavigationIntent) {
	if ar != nil {
		wp.SetAltitudeRestriction(*ar)
		intent.AltRestriction = ar
		nav.Altitude = Altitude{}
	}

	if sr != nil {
		wp.SetSpeedRestriction(*sr)
		if sr.IsMach {
			intent.SpeedRestriction = sr
		} else {
			naturalIAS, _ := nav.targetAltitudeIAS(temp)
			s := nav.restrictedSpeed(sr, naturalIAS)
			intentSpeed := av.MakeAtSpeedRestriction(s)
			intent.SpeedRestriction = &intentSpeed
		}
		nav.Speed = Speed{}
	}
}

func (nav *Nav) CrossDistanceFromFixAt(fix string, dist float32, dir math.CardinalOrdinalDirection,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	defer nav.record("CrossDistanceFromFixAt", fix, dist, dir, ar, sr, temp)()
	routeWps, commitRoute := nav.editAssignedWaypoints()

	wps := routeWps
	idx := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	if idx == -1 {
		return speech.MakeUnableIntent("unable. {fix} isn't in our route", fix)
	}

	fixLoc := wps[idx].Location

	// Find the "real" prior waypoint (skip synthetic ones) to determine the segment.
	realPriorIdx := idx - 1
	for realPriorIdx >= 0 && wps[realPriorIdx].SyntheticCrossing() {
		realPriorIdx--
	}

	var priorLoc math.Point2LL
	var priorName string
	if realPriorIdx >= 0 {
		priorLoc = wps[realPriorIdx].Location
		priorName = wps[realPriorIdx].Fix
	} else {
		priorLoc = nav.FlightState.Position
	}

	// Direction validation uses the inbound magnetic course to the fix, since
	// the controller-issued direction is spoken relative to the magnetic compass.
	approachHeading := math.TrueToMagnetic(
		math.Heading2LL(fixLoc, priorLoc, nav.FlightState.NmPerLongitude),
		nav.FlightState.MagneticVariation,
	)
	if math.HeadingDifference(float32(approachHeading), dir.Heading()) > 45 {
		actualDir := math.Compass(approachHeading)
		return speech.MakeUnableIntent("unable. We're approaching {fix} from the "+actualDir, fix)
	}

	// Distance validation against the real segment.
	segLen := math.NMDistance2LL(priorLoc, fixLoc)
	if realPriorIdx >= 0 && dist >= segLen {
		return speech.MakeUnableIntent("unable. That's before {fix}", priorName)
	}
	if realPriorIdx < 0 && dist >= math.NMDistance2LL(nav.FlightState.Position, fixLoc) {
		return speech.MakeUnableIntent("unable. We're already closer to {fix}", fix)
	}

	// Compute synthetic waypoint via linear interpolation.
	t := 1 - dist/segLen
	syntheticLoc := math.Point2LL{
		math.Lerp(t, priorLoc[0], fixLoc[0]),
		math.Lerp(t, priorLoc[1], fixLoc[1]),
	}

	// 1. Remove or clear existing synthetic waypoints for this fix and restriction types.
	removePrefix := "_" + fix + "/"
	routeWps = clearMatchingSyntheticRestrictions(routeWps,
		func(wp *av.Waypoint) bool { return strings.HasPrefix(wp.Fix, removePrefix) },
		ar, sr)
	commitRoute(routeWps)

	// 2. Refresh index as waypoints might have shifted.
	wps = routeWps
	idx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })

	intent := speech.NavigationIntent{
		Type:      speech.NavCrossDistanceFromFixAt,
		Fix:       fix,
		Distance:  dist,
		Direction: dir,
	}

	// Helper to insert a synthetic waypoint in the correct order (descending distance from fix).
	insertOrdered := func(name string, loc math.Point2LL, d float32) {
		// Find the insertion index among synthetic waypoints for this fix.
		insertIdx := idx
		for insertIdx > 0 && strings.HasPrefix(wps[insertIdx-1].Fix, removePrefix) {
			if _, otherDist, _, ok := av.ParseSyntheticCrossingFix(wps[insertIdx-1].Fix); ok {
				if float32(otherDist) >= d {
					break // Current one is closer to fix than the one we are checking (or same distance).
				}
			}
			insertIdx--
		}

		// Inherit SID/STAR/Approach flags from the named target fix (wps[idx]),
		// not the insertion neighbor.
		var inherit *av.Waypoint
		if idx < len(wps) {
			inherit = &wps[idx]
		}
		routeWps = slices.Insert(routeWps, insertIdx, newSyntheticWaypoint(name, loc, inherit))
		commitRoute(routeWps)
		// Refresh wps and idx for subsequent operations.
		wps = routeWps
		idx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == fix })
	}

	name := fmt.Sprintf("_%s/%d%s", fix, int(dist), dir.ShortString())
	wpIdx := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == name })
	if wpIdx == -1 {
		insertOrdered(name, syntheticLoc, dist)
		wps = routeWps
		wpIdx = slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == name })
	}
	wp := &routeWps[wpIdx]
	wp.Location = syntheticLoc

	// 3. Apply new inline restrictions to the synthetic waypoint.
	nav.applyRestrictionsToSyntheticWaypoint(wp, ar, sr, temp, &intent)
	commitRoute(routeWps)

	return intent
}

// CrossDMEAt inserts a synthetic crossing restriction at a given DME from
// the runway threshold of the currently cleared visual approach. The
// synthetic waypoint is placed along the approach route by walking backwards
// from the threshold accumulating track miles; if dist exceeds the total
// route length, the point is extrapolated backwards along the first leg.
func (nav *Nav) CrossDMEAt(dist float32, ar *av.AltitudeRestriction, sr *av.SpeedRestriction, temp av.Temperature) speech.CommandIntent {
	defer nav.record("CrossDMEAt", dist, ar, sr, temp)()
	if dist <= 0 || dist > 30 {
		return speech.MakeUnableIntent("unable, that distance is out of range")
	}

	if !nav.clearedForVisualApproach() {
		return speech.MakeUnableIntent("unable, we're not cleared for a visual approach")
	}
	ap := nav.Approach.Assigned
	runway := ap.Runway

	routeWps, commitRoute := nav.editAssignedWaypoints()

	if len(routeWps) < 2 {
		return speech.MakeUnableIntent("unable")
	}

	// 1. Clear matching restriction categories on any existing synthetic DME
	// waypoints for this runway; drop any that end up with no restrictions.
	routeWps = clearMatchingSyntheticRestrictions(routeWps, func(wp *av.Waypoint) bool {
		wpRunway, _, ok := av.ParseSyntheticDMEFix(wp.Fix)
		return ok && wpRunway == runway
	}, ar, sr)
	commitRoute(routeWps)

	// Deletion above can leave the threshold alone (e.g., the aircraft has
	// passed all prior visual waypoints and the only remaining synthetic
	// was just removed); without at least two waypoints we have no route to
	// measure along.
	if len(routeWps) < 2 {
		return speech.MakeUnableIntent("unable")
	}

	// 2. Walk backward from the threshold, accumulating track miles, and
	// place the synthetic waypoint on the first leg that spans `dist`. If
	// `dist` exceeds the full route length, extrapolate past wp[0] along
	// the direction from wp[1] back to wp[0]. The final waypoint is the
	// arrival airport (appended after the threshold); skip the
	// threshold-to-airport leg so distances are measured from the threshold.
	thresholdIdx := len(routeWps) - 2
	if thresholdIdx < 1 {
		return speech.MakeUnableIntent("unable")
	}
	nmPerLong := nav.FlightState.NmPerLongitude
	var syntheticLoc math.Point2LL
	var insertIdx int
	var cum float32 // cumulative track miles from threshold to routeWps[i+1]
	placed := false
	for i := thresholdIdx - 1; i >= 0 && !placed; i-- {
		legLen := math.NMDistance2LLFast(routeWps[i].Location, routeWps[i+1].Location, nmPerLong)
		if dist <= cum+legLen {
			t := (dist - cum) / legLen
			syntheticLoc = math.Point2LL(math.Lerp2f(t, routeWps[i+1].Location, routeWps[i].Location))
			insertIdx = i + 1
			placed = true
		}
		cum += legLen
	}
	if !placed {
		wp0NM := math.LL2NM(routeWps[0].Location, nmPerLong)
		wp1NM := math.LL2NM(routeWps[1].Location, nmPerLong)
		dirBack := math.Normalize2f(math.Sub2f(wp0NM, wp1NM))
		syntheticLoc = math.NM2LL(math.Add2f(wp0NM, math.Scale2f(dirBack, dist-cum)), nmPerLong)
		insertIdx = 0
	}

	// 4. Apply restrictions to a new or existing synthetic DME waypoint.
	name := fmt.Sprintf("_%s_%dDME", runway, int(dist))
	existingIdx := slices.IndexFunc(routeWps, func(wp av.Waypoint) bool { return wp.Fix == name })
	var wp *av.Waypoint
	if existingIdx >= 0 {
		wp = &routeWps[existingIdx]
		wp.Location = syntheticLoc
	} else {
		// Inherit SID/STAR/Approach flags from the insertion neighbor — the
		// waypoint that will sit immediately after the new one.
		var inherit *av.Waypoint
		if insertIdx < len(routeWps) {
			inherit = &routeWps[insertIdx]
		}
		routeWps = slices.Insert(routeWps, insertIdx, newSyntheticWaypoint(name, syntheticLoc, inherit))
		wp = &routeWps[insertIdx]
	}

	intent := speech.NavigationIntent{
		Type:     speech.NavCrossDME,
		Fix:      name,
		Distance: dist,
	}

	nav.applyRestrictionsToSyntheticWaypoint(wp, ar, sr, temp, &intent)
	commitRoute(routeWps)

	return intent
}
