// nav/restriction.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"cmp"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// The route's crossing restrictions: which of the published and
// controller-issued altitude and speed restrictions apply to the aircraft,
// the altitude and speed they call for, how they carry past a fix, and the
// via clearances that put a procedure's restrictions in force.

func (nav *Nav) controllerAltitudeRestriction(wp *av.Waypoint) *av.AltitudeRestriction {
	if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Altitude != nil {
		return nfa.Arrive.Altitude
	}
	if wp.SyntheticCrossing() {
		return wp.AltitudeRestriction()
	}
	return nil
}

// chartedAltitudeRestriction returns wp's published altitude restriction,
// or nil if it doesn't apply to the aircraft. An approach's restrictions
// apply only once the aircraft is cleared for it, and an arrival climbs
// only when a controller tells it to, so one above it doesn't apply.
func (nav *Nav) chartedAltitudeRestriction(wp *av.Waypoint) *av.AltitudeRestriction {
	if wp.OnApproach() && !nav.Approach.Cleared {
		return nil
	}
	ar := wp.AltitudeRestriction()
	if ar != nil && wp.OnSTAR() && !wp.SyntheticCrossing() && ar.Range[0] > nav.FlightState.Altitude {
		return nil
	}
	return ar
}

// chartedSpeedRestriction returns wp's inline speed restriction. Published
// approach restrictions require approach clearance; synthetic controller
// crossings apply regardless of clearance.
func (nav *Nav) chartedSpeedRestriction(wp *av.Waypoint) *av.SpeedRestriction {
	if wp.OnApproach() && !nav.Approach.Cleared && !wp.SyntheticCrossing() {
		return nil
	}
	return wp.SpeedRestriction()
}

// carryRestrictions keeps wp's restrictions in force past it: an aircraft
// that couldn't meet the altitude keeps trying, and a published speed is held
// until something replaces it. The controller's crossing altitude for the
// fix, if given, replaces the charted one after the fix as well as before.
func (nav *Nav) carryRestrictions(wp av.Waypoint) {
	if ar := cmp.Or(nav.controllerAltitudeRestriction(&wp), nav.chartedAltitudeRestriction(&wp)); ar != nil &&
		(!nav.Approach.Cleared || ar.Range[0] < nav.FlightState.Altitude) {
		// Don't climb if we're cleared approach and below the next
		// fix's altitude. Copy the value since the pointer into the
		// slice element could become stale if the slice is reallocated.
		arCopy := *ar
		nav.Altitude.Restriction = &arCopy
	}
	if sr := nav.chartedSpeedRestriction(&wp); sr != nil && !wp.OnSID() {
		// Carry on the speed restriction unless it's a SID
		srCopy := *sr
		nav.Speed.Restriction = &srCopy
	}
}

// altitudeTarget holds the result of scanning waypoints for an altitude target.
type altitudeTarget struct {
	altitude float32
	fix      string
	// level is set when the restrictions ahead require exactly the current
	// altitude at fix, so the aircraft stays level until it gets there.
	level bool
}

// findAltitudeTarget scans waypoints to determine the target altitude and
// fix, using the reverse-walk logic to satisfy all downstream altitude
// constraints. Returns the target and true if found.
func (nav *Nav) findAltitudeTarget() (altitudeTarget, bool) {
	// On a heading the route's fixes don't apply, except that a pilot
	// cleared for a visual approach descends toward where the heading is
	// expected to join it.
	if nav.Heading.Assigned != nil && !nav.clearedForVisualApproach() {
		return altitudeTarget{}, false
	}

	// Don't consider restrictions after an upcoming hold
	wps := nav.Waypoints
	for i := range wps {
		if hold := nav.Heading.Hold; hold != nil && hold.Hold.Fix == wps[i].Fix {
			wps = wps[:i+1]
			break
		}
		if nfa, ok := nav.FixAssignments[wps[i].Fix]; ok && nfa.Hold != nil {
			wps = wps[:i+1]
			break
		}
	}

	if nav.Prespawn {
		// Simplified forward scan for prespawn: find the first waypoint
		// with an actionable restriction. Skips FixAssignment lookups
		// and the reverse walk with ClampRange for intermediate waypoints.
		for i := range wps {
			ar := nav.chartedAltitudeRestriction(&wps[i])
			if ar == nil {
				continue
			}
			if ar.TargetAltitude(nav.FlightState.Altitude) == nav.FlightState.Altitude {
				if _, ok := ar.ExactValue(); ok {
					return altitudeTarget{altitude: nav.FlightState.Altitude, fix: wps[i].Fix, level: true}, true
				}
				continue
			}
			alt := util.Select(ar.Range[1] != av.MaxAltitude, ar.Range[1], nav.FinalAltitude)
			return altitudeTarget{altitude: alt, fix: wps[i].Fix}, true
		}
		return altitudeTarget{}, false
	}

	haveFixAssignments := len(nav.FixAssignments) > 0
	getChartedRestriction := func(i int) *av.AltitudeRestriction {
		wp := &wps[i]
		if ar := nav.controllerAltitudeRestriction(wp); ar != nil {
			return ar
		}
		ar := nav.chartedAltitudeRestriction(wp)
		if ar != nil && haveFixAssignments {
			// If the controller has given 'cross [wp] at [alt]' for a
			// future waypoint, ignore the charted altitude restriction.
			// Explicit loop avoids slices.ContainsFunc which copies the
			// large Waypoint struct by value for each element via the closure.
			for j := i + 1; j < len(wps); j++ {
				if fa, ok := nav.FixAssignments[wps[j].Fix]; ok && fa.Arrive.Altitude != nil {
					return nil
				}
			}
		}
		return ar
	}

	// On a cleared approach, "at or above X" means the aircraft should
	// target X (the floor) to maintain a normal descent profile, not
	// stay high because the restriction is technically satisfied.
	getRestriction := func(i int) *av.AltitudeRestriction {
		ar := getChartedRestriction(i)
		if ar != nil && nav.Approach.Cleared && wps[i].OnApproach() &&
			ar.Range[1] == av.MaxAltitude && ar.Range[0] > 0 {
			adjusted := av.MakeAtAltitudeRestriction(ar.Range[0])
			return &adjusted
		}
		return ar
	}

	// Find the *last* waypoint that has an altitude restriction that
	// applies to the aircraft. Along the way, note the nearest met
	// restriction that requires exactly the current altitude.
	lastWp, levelWp := -1, -1
	for i := range slices.Backward(wps) {
		r := getRestriction(i)
		if r == nil {
			continue
		}
		// Skip restrictions that don't apply (e.g. "at or above" if we're
		// already above.) I think(?) we would actually bail out and return
		// nil if we find one that doesn't apply, under the principle that
		// we should also already be meeting any restrictions that are
		// before it, but this seems less risky.
		if r.TargetAltitude(nav.FlightState.Altitude) != nav.FlightState.Altitude {
			lastWp = i
			break
		}
		if _, ok := r.ExactValue(); ok {
			levelWp = i
		}
	}
	if lastWp == -1 {
		if levelWp != -1 {
			// Everything ahead is met, but the aircraft still has to cross
			// this fix at its current altitude rather than leave for its
			// cleared altitude.
			return altitudeTarget{altitude: nav.FlightState.Altitude, fix: wps[levelWp].Fix, level: true}, true
		}
		// No applicable altitude restrictions found, so nothing to do here.
		return altitudeTarget{}, false
	}

	last := getRestriction(lastWp)
	descending := nav.FlightState.Altitude > last.TargetAltitude(nav.FlightState.Altitude)

	// Use a large rate estimate for the reverse walk to compute feasible
	// altitude ranges.
	altRate := util.Select(descending, nav.Perf.Rate.Descent, nav.Perf.Rate.Climb)

	// altRange is the range of altitudes that the aircraft may be in and
	// successfully meet all of the restrictions. It will be updated
	// incrementally working backwards from the last altitude restriction.
	altRange := last.Range

	// Sum of distances in nm since the last waypoint with an altitude
	// restriction.
	sumDist := float32(0)

	// Loop over waypoints in reverse starting at the one before the last
	// one with a waypoint restriction.
	fix := wps[lastWp].Fix // first one with an alt restriction
	for i := lastWp - 1; i >= 0; i-- {
		sumDist += math.NMDistance2LLFast(wps[i+1].Location, wps[i].Location,
			nav.FlightState.NmPerLongitude)

		// Does this one have a relevant altitude restriction?
		restr := getRestriction(i)
		if restr == nil {
			continue
		}

		// Ignore it if the aircraft is cleared for the approach and is below it.
		// TODO: I think this can be 'break' rather than continue...
		if nav.Approach.Cleared && nav.FlightState.Altitude < restr.Range[0] {
			continue
		}

		fix = wps[i].Fix

		// Maximum change in altitude possible before reaching this
		// waypoint. The aircraft flies these legs consecutively, so it's
		// already at rate by the time it reaches them; the spool-up at the
		// start of the descent is handled by TargetAltitude's safety factor.
		// Coincident waypoints allow no change at all; handling them here
		// also avoids 0/0 on a departure's first tick, when GS is 0.
		var dalt float32
		if sumDist > 0 {
			// TODO: account for decreasing GS with altitude?
			// TODO: incorporate a simple wind model in GS?
			eta := sumDist / nav.FlightState.GS * 3600 // seconds
			dalt = altRate * eta / 60
		}

		// possibleRange is altitude range the aircraft could have at this
		// waypoint, given its performance characteristics and assuming it
		// will meet the constraint at the subsequent waypoint with an
		// altitude restriction.
		//
		// Note that dalt only applies to one limit, since the aircraft can
		// always maintain its current altitude between waypoints; which
		// limit depends on whether it is climbing or descending (but then
		// which one and the addition/subtraction are confusingly backwards
		// since we're going through waypoints in reverse order...)
		possibleRange := altRange
		if !descending {
			possibleRange[0] -= dalt
		} else {
			possibleRange[1] += dalt
		}

		// Limit the possible range according to the restriction at the
		// current waypoint. A leg that's too steep to fly doesn't excuse
		// the restrictions before it; ClampRange anchors altRange on this
		// restriction in that case, so keep walking back to find the
		// earliest restriction that binds.
		altRange, _ = restr.ClampRange(possibleRange)

		// Reset this so we compute the right eta next time we have a
		// waypoint with an altitude restriction.
		sumDist = 0
	}

	// Prefer to be higher rather than low; deal with "at or above" here as well.
	alt := util.Select(altRange[1] != av.MaxAltitude, altRange[1], nav.FinalAltitude)

	// But leave arrivals at their current altitude if it's acceptable;
	// don't climb just because we can.
	if descending {
		ar := av.AltitudeRestriction{NavigationRestriction: av.NavigationRestriction{Range: altRange}}
		if ar.Target(nav.FlightState.Altitude) == nav.FlightState.Altitude {
			alt = nav.FlightState.Altitude
		}
	} else {
		// For climbing aircraft, if the constraint is "at" (not "at or above"),
		// target that specific altitude instead of climbing higher
		if altRange[0] != 0 && altRange[0] == altRange[1] {
			alt = altRange[0]
		}
	}

	level := altRange[0] == altRange[1] && altRange[0] == nav.FlightState.Altitude
	return altitudeTarget{altitude: alt, fix: fix, level: level}, true
}

// LowestProcedureAltitude returns the lowest applicable published altitude
// target on the assigned route, or false if there are no applicable restrictions.
func (nav *Nav) LowestProcedureAltitude() (float32, bool) {
	var lowest float32
	found := false
	wps := nav.AssignedWaypoints()
	for i := range wps {
		if ar := nav.chartedAltitudeRestriction(&wps[i]); ar != nil {
			alt := ar.TargetAltitude(nav.FlightState.Altitude)
			if !found || alt < lowest {
				lowest = alt
				found = true
			}
		}
	}
	return lowest, found
}

func (nav *Nav) getUpcomingSpeedRestrictionWaypoint() (onSID bool, sr *av.SpeedRestriction, fix string, ok bool) {
	if nav.Prespawn {
		return false, nil, "", false
	}

	for i := range nav.Waypoints {
		wp := &nav.Waypoints[i]
		if nfa, ok := nav.FixAssignments[wp.Fix]; ok && nfa.Arrive.Speed != nil {
			return wp.OnSID(), nfa.Arrive.Speed, wp.Fix, true
		}
		if wsr := nav.chartedSpeedRestriction(wp); wsr != nil {
			return wp.OnSID(), wsr, wp.Fix, true
		}
	}
	return false, nil, "", false
}

// restrictedSpeed returns the speed a pilot would choose given a speed
// restriction, accounting for aircraft type. Heavy jets fly close to
// limits for fuel efficiency; lighter aircraft have more margin.
func (nav *Nav) restrictedSpeed(sr *av.SpeedRestriction, natural float32) float32 {
	ias := sr.IAS(nav.FlightState.Altitude)
	lo, hi := ias.Range[0], ias.Range[1]

	// "At" restriction: exact compliance required.
	if lo == hi {
		return lo
	}

	// Margin from the boundary depends on aircraft weight class.
	// Heavies need speed for efficiency and fly close to limits.
	margin := float32(10)
	if nav.Perf.WeightClass == "H" || nav.Perf.WeightClass == "J" {
		margin = 5
	}

	// Shrink the allowed range by the margin on each real bound.
	innerLo := lo
	innerHi := hi
	if lo > 0 { // lo==0 means no floor
		innerLo = lo + margin
	}
	if hi < av.MaxRestrictionSpeed { // sentinel means no ceiling
		innerHi = hi - margin
	}

	// If margins overlap (very narrow range), just use the midpoint.
	if innerLo > innerHi {
		return (lo + hi) / 2
	}

	return math.Clamp(natural, innerLo, innerHi)
}

// ClimbViaSID is "climb via SID", with "except maintain exceptAlt" if it is
// non-nil. Otherwise the aircraft climbs to its cruise altitude: the
// scenario's cleared altitude for a departure is an earlier controller's
// "except maintain", and cruise is the only top altitude it gives a SID.
func (nav *Nav) ClimbViaSID(exceptAlt *float32, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("ClimbViaSID", exceptAlt, simTime, delayReduction)()
	intent := speech.ProcedureIntent{Type: speech.ProcedureClimbViaSID, ExceptAltitude: exceptAlt}
	if exceptAlt != nil {
		if *exceptAlt > nav.Perf.Ceiling {
			return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
		}
		if *exceptAlt > nav.FinalAltitude {
			return speech.MakeUnableIntent("unable. We're only filed up to {alt}", nav.FinalAltitude)
		}
		if alt := nav.FlightState.Altitude; alt-*exceptAlt >= 1000 {
			intent.CurrentAltitude = &alt
		}
	}
	if !nav.ClimbViaSIDAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not flying a departure procedure")
	}

	nav.resumeProcedureCourse(simTime, delayReduction)
	return intent
}

// DescendViaSTAR is "descend via STAR", with "except maintain exceptAlt" if
// it is non-nil; otherwise the aircraft descends to the STAR's last
// restriction.
func (nav *Nav) DescendViaSTAR(exceptAlt *float32, simTime Time, delayReduction time.Duration) speech.CommandIntent {
	defer nav.record("DescendViaSTAR", exceptAlt, simTime, delayReduction)()
	intent := speech.ProcedureIntent{Type: speech.ProcedureDescendViaSTAR, ExceptAltitude: exceptAlt}
	if exceptAlt != nil {
		if *exceptAlt > nav.Perf.Ceiling {
			return speech.MakeUnableIntent("unable. That altitude is above our ceiling.")
		}
		alt := nav.FlightState.Altitude
		if *exceptAlt-alt >= 5000 {
			return speech.MakeUnableIntent("unable. We're already at {alt}", alt)
		}
		if *exceptAlt-alt >= 1000 {
			intent.CurrentAltitude = &alt
		}
	}
	if !nav.DescendViaSTARAtPassedFix(exceptAlt) {
		return speech.MakeUnableIntent("unable. We're not on a STAR")
	}

	nav.resumeProcedureCourse(simTime, delayReduction)
	return intent
}

// resumeProcedureCourse puts the aircraft back on course after a via
// clearance. One flying a heading to rejoin the procedure keeps doing so;
// the clearance is a new altitude, so the heading no longer captures an
// altitude floor when it takes effect.
func (nav *Nav) resumeProcedureCourse(simTime Time, delayReduction time.Duration) {
	if nav.pendingJoin() == nil {
		nav.enqueueOnCourse(simTime, delayReduction)
	} else if dh := nav.DeferredNavHeading; dh != nil {
		dh.SnapshotAltitudeOnEffect = false
	}
}

// ClimbViaSIDAtPassedFix carries out a /cvs or /cv route action at the fix
// the aircraft just passed. The aircraft is already flying its route, so
// unlike ClimbViaSID it isn't put back on course. It returns false,
// changing nothing, if no fix ahead is on a SID. An excepted altitude below
// the aircraft is descended to, as instructed.
func (nav *Nav) ClimbViaSIDAtPassedFix(exceptAlt *float32) bool {
	defer nav.record("ClimbViaSIDAtPassedFix", exceptAlt)()
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSID) {
		return false
	}

	ceiling := nav.FinalAltitude
	if exceptAlt != nil {
		ceiling = *exceptAlt
	}
	nav.flyProcedureRestrictions(&ClearedAltitude{Altitude: ceiling})
	return true
}

// DescendViaSTARAtPassedFix is the /dvs and /dv counterpart of
// ClimbViaSIDAtPassedFix; an excepted altitude above the aircraft is climbed
// back to.
func (nav *Nav) DescendViaSTARAtPassedFix(exceptAlt *float32) bool {
	defer nav.record("DescendViaSTARAtPassedFix", exceptAlt)()
	if !slices.ContainsFunc(nav.AssignedWaypoints(), av.Waypoint.OnSTAR) {
		return false
	}

	var floor *ClearedAltitude
	if exceptAlt != nil {
		floor = &ClearedAltitude{Altitude: *exceptAlt, IsFloor: true}
	}
	nav.flyProcedureRestrictions(floor)
	return true
}

// flyProcedureRestrictions cancels the assigned altitude and speed so that
// the restrictions of the procedure ahead govern, limited by cleared if it
// is non-nil. A published speed restriction the aircraft is already holding
// stays in effect: a via clearance cancels only assigned speeds (7110.65
// 5-7-1).
func (nav *Nav) flyProcedureRestrictions(cleared *ClearedAltitude) {
	nav.Altitude = Altitude{Cleared: cleared}
	nav.Speed = Speed{Restriction: nav.Speed.Restriction}
	nav.clearAfterFixSpeeds()
}
