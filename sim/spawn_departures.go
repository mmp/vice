// sim/spawn_departures.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

// How low below the MVA a VFR can be
const vfrMVABuffer = 1000

// Max altitude for VFR aircraft (below Class A airspace at 18,000')
const maxVFRAltitude = 17500

// exitRoutesHaveVariedHeadings returns true if the given exit routes have
// different final headings. This is used to determine whether departures
// should report their heading when checking in with departure control.
func exitRoutesHaveVariedHeadings(exitRoutes map[av.ExitID]*av.ExitRoute) bool {
	var firstHeading int
	first := true
	for _, route := range exitRoutes {
		hdg := route.FinalHeading()
		if hdg == 0 {
			continue
		}
		if first {
			firstHeading = hdg
			first = false
		} else if hdg != firstHeading {
			return true
		}
	}
	return false
}

// exitRoutesHaveVariedSIDs returns true if the given exit routes have
// different SID names. This is used to determine whether departures
// should report their SID when checking in with departure control.
func exitRoutesHaveVariedSIDs(exitRoutes map[av.ExitID]*av.ExitRoute) bool {
	var firstSID string
	first := true
	for _, route := range exitRoutes {
		sid := route.SID
		if sid == "" {
			continue
		}
		if first {
			firstSID = sid
			first = false
		} else if sid != firstSID {
			return true
		}
	}
	return false
}

// spawnVFRDepartures spawns rate-based VFR departures. VFR traffic isn't part
// of the pregenerated schedule: its destinations depend on live arrival
// congestion and its routes on the wind-selected runway.
func (s *Sim) spawnVFRDepartures() {
	if s.State.LaunchConfig.DepartureMode != LaunchAutomatic {
		return
	}
	now := s.State.SimTime

	for airport, runways := range util.SortedMap(s.DepartureState) {
		for runway, depState := range util.SortedMap(runways) {
			if now.After(depState.NextVFRSpawn) {
				ac, err := s.makeNewVFRDeparture(airport, runway)
				launched := ac != nil && err == nil
				if launched {
					s.addDepartureToPool(ac, runway, 0 /* no wait at the gate */)
				}
				// Also skip the slot if there was nowhere to send the
				// aircraft; otherwise we'd try again every second for as
				// long as arrivals are backed up.
				if launched || errors.Is(err, errNoVFRDestination) {
					depState.NextVFRSpawn = now.Add(randomWait(depState.VFRSpawnRate, false, s.Rand))
				}
			}
		}
	}
}

func (s *Sim) updateDepartureQueues() {
	now := s.State.SimTime

	for airport, runways := range util.SortedMap(s.DepartureState) {
		for depRunway, depState := range util.SortedMap(runways) {
			depState.filterDeleted(s.Aircraft)
			s.processGateDepartures(depState, now)
			s.processHeldDepartures(depState, now)
			s.launchNextDeparture(depState, airport, depRunway, now)
		}
	}
}

// maxHoldingShort bounds how many departures wait for the runway at a time;
// the rest stay at the gate. A controller sequences the handful of aircraft
// holding short, not the whole morning's departures.
const maxHoldingShort = 8

// maxGateDepartures bounds how many departures wait at an airport's gates
// for a runway. Past it the schedule holds its entries back rather than
// filling the field with aircraft that will not get out.
const maxGateDepartures = 10

func (s *Sim) processGateDepartures(depState *RunwayLaunchState, now Time) {
	for i, dep := range depState.Gate {
		if now.Before(dep.ReadyDepartGateTime) {
			continue
		}

		ac := s.Aircraft[dep.ADSBCallsign]
		if ac.HoldForRelease {
			depState.Gate[i].RequestReleaseTime = now.Add(s.Rand.DurationRange(60*time.Second, 120*time.Second))
			s.STARSComputer.AddHeldDeparture(ac)
			depState.Held = append(depState.Held, depState.Gate[i])
			depState.Gate = append(depState.Gate[:i], depState.Gate[i+1:]...)
		} else if s.State.LaunchConfig.DepartureMode == LaunchAutomatic &&
			len(depState.ReleasedIFR) < maxHoldingShort {
			depState.Gate[i].QueuedTime = now
			depState.ReleasedIFR = append(depState.ReleasedIFR, depState.Gate[i])
			depState.Gate = append(depState.Gate[:i], depState.Gate[i+1:]...)
		}
		break // only one per iteration
	}
}

func (s *Sim) processHeldDepartures(depState *RunwayLaunchState, now Time) {
	if s.prespawnUncontrolledOnly {
		s.cullHeldDepartures(depState, now)
		return
	}

	for i, held := range depState.Held {
		if now.Before(held.RequestReleaseTime) {
			break // FIFO
		}

		if !held.ReleaseRequested {
			depState.Held[i].ReleaseRequested = true
			depState.Held[i].ReleaseDelay = s.Rand.DurationRange(20*time.Second, 120*time.Second)
		}
	}

	if len(depState.Held) > 0 && depState.Held[0].ReleaseRequested {
		dep := depState.Held[0]
		ac := s.Aircraft[dep.ADSBCallsign]
		if ac.Released && now.After(ac.ReleaseTime.Add(dep.ReleaseDelay)) {
			depState.Held[0].QueuedTime = now
			depState.ReleasedIFR = append(depState.ReleasedIFR, depState.Held[0])
			depState.Held = depState.Held[1:]
		}
	}
}

// cullHeldDepartures deletes the departures that have reached the point of
// asking for a release while the sim is warming itself up. There is nobody
// to ask, and a release handed out here would still be in force when the
// user takes over, so they go the way of a handoff to a human who hasn't
// signed on yet. Nothing is lost by it: a departure that still holds for
// release has a human departure controller, so updateState deletes it a few
// hundred feet after takeoff regardless.
func (s *Sim) cullHeldDepartures(depState *RunwayLaunchState, now Time) {
	for len(depState.Held) > 0 {
		dep := depState.Held[0]
		if now.Before(dep.RequestReleaseTime) {
			break // FIFO
		}
		s.deleteAircraft(s.Aircraft[dep.ADSBCallsign])
		depState.Held = depState.Held[1:]
	}
}

// departureQueuePriority is how long a departure can hold short before it
// goes next whether or not it fits behind the ones already gone; a gate in
// constant use would otherwise strand it.
const departureQueuePriority = 5 * time.Minute

// launchNextDeparture launches one of the departures holding short of the
// runway, if any of them can go. Leaving the choice until the runway is
// free lets it account for what the airport's other runways have just
// launched: a departure over a gate one of them has just used waits, and
// one going out a different gate goes ahead of it.
func (s *Sim) launchNextDeparture(depState *RunwayLaunchState, airport av.ICAOAirportCode,
	depRunway av.RunwayID, now Time) {
	// IFRs go first; VFRs wait for a gap in them.
	queue, rules := &depState.ReleasedIFR, av.FlightRulesIFR
	if len(*queue) == 0 {
		queue, rules = &depState.ReleasedVFR, av.FlightRulesVFR
	}
	if len(*queue) == 0 || !s.runwayAvailable(depState, airport, depRunway, rules) {
		return
	}

	idx, ok := s.nextDeparture(*queue, depState, airport, depRunway, now)
	if !ok {
		return
	}

	dep := (*queue)[idx]
	*queue = util.DeleteSliceElement(*queue, idx)

	ac := s.Aircraft[dep.ADSBCallsign]
	ac.WaitingForLaunch = false
	dep.LaunchTime = now
	depState.LastDeparture = &dep

	for _, state := range s.samePavementRunways(airport, depRunway) {
		state.LastDeparture = &dep
	}
	if exit := ac.FlightPlan.Exit; exit != "" {
		if s.LastExitLaunch[airport] == nil {
			s.LastExitLaunch[airport] = make(map[av.ExitID]Time)
		}
		s.LastExitLaunch[airport][exit] = now
	}
}

// nextDeparture returns the index in queue of the departure to launch now.
// The one that has been holding short longest and is clear of the
// departures already gone goes; one that has waited past
// departureQueuePriority is next regardless, the runway holding for it
// rather than passing it over again.
func (s *Sim) nextDeparture(queue []DepartureAircraft, depState *RunwayLaunchState,
	airport av.ICAOAirportCode, depRunway av.RunwayID, now Time) (int, bool) {
	held := func(_ int, dep DepartureAircraft) time.Duration { return now.Sub(dep.QueuedTime) }

	waited := util.FilterSeq2(slices.All(queue), func(idx int, dep DepartureAircraft) bool {
		return held(idx, dep) > departureQueuePriority
	})
	if idx, ok := util.SeqMaxIndexFunc(waited, held); ok {
		return idx, s.departureSpaced(depState, queue[idx], airport, depRunway)
	}

	ready := util.FilterSeq2(slices.All(queue), func(_ int, dep DepartureAircraft) bool {
		return s.departureSpaced(depState, dep, airport, depRunway)
	})
	return util.SeqMaxIndexFunc(ready, held)
}

// samePavementRunways returns an iterator over all of the runways that
// share pavement with the given depRwy: these can come both from
// user-specified "departure_runways_as_one" but also from runways with
// dotted suffixes; we want to treat 4 and 4.AutoWest as one, for example.
// Note that the iterator will return the provided runway and may return the
// same runway multiple times. Merely-intersecting runways are not included;
// they are handled geometrically in departureSpaced.
func (s *Sim) samePavementRunways(airport av.ICAOAirportCode, depRwy av.RunwayID) iter.Seq2[av.RunwayID, *RunwayLaunchState] {
	depRwyBase := depRwy.Base()
	runwayState := s.DepartureState[airport]
	return func(yield func(av.RunwayID, *RunwayLaunchState) bool) {
		// First look at departure runways as one
		for _, group := range s.State.Airports[airport].DepartureRunwaysAsOne {
			groupRwys := strings.Split(group, ",")
			if slices.Contains(groupRwys, depRwyBase) {
				for rwy, state := range runwayState {
					if slices.Contains(groupRwys, rwy.Base()) {
						if !yield(rwy, state) {
							return
						}
					}
				}
				break
			}
		}

		// Now look for departing both e.g. "4" and "4.AutoWest"
		for rwy, state := range runwayState {
			if depRwyBase == rwy.Base() {
				if !yield(rwy, state) {
					return
				}
			}
		}
	}
}

// runwayAvailable reports whether the runway can take a departure now,
// whichever one goes: no hold after a go-around, no arrival that has just
// landed or is about to, and no pattern traffic in the way. rules are those
// of the departure that would go.
func (s *Sim) runwayAvailable(depState *RunwayLaunchState, airport av.ICAOAirportCode,
	runway av.RunwayID, rules av.FlightRules) bool {
	// Check if departures are held due to a go-around
	if s.State.SimTime.Before(depState.GoAroundHoldUntil) {
		return false
	}

	// Check if we need to wait after a recent arrival's landing to
	// simulate its deceleration and vacating the runway (though skip this
	// check if both the last arrival and the departing aircraft are VFR.)
	if rules == av.FlightRulesIFR || depState.LastArrivalFlightRules == av.FlightRulesIFR {
		if elapsed := s.State.SimTime.Sub(depState.LastArrivalLandingTime); elapsed <= time.Minute {
			return false
		}
	}

	// Check for imminent arrivals on this runway
	// Skip this check if both arriving and departing aircraft are VFR
	for _, ac := range s.Aircraft {
		if ac.Nav.Approach.Assigned != nil && ac.Nav.Approach.Assigned.Runway == runway.Base() {
			// Skip if both aircraft are VFR
			if ac.FlightPlan.Rules == av.FlightRulesVFR && rules == av.FlightRulesVFR {
				continue
			}

			if dist, err := ac.Nav.DistanceToEndOfApproach(); err == nil && dist < 2.0 {
				// Hold departure; the arrival's too close
				return false
			}
		}
	}

	// Don't launch yet if a pattern aircraft is about to land or just departed.
	return !s.patternConflictsWithLaunch(airport)
}

// sameExitSeparation is how long after a departure has gone out over an
// exit the next one out the same exit may start its takeoff roll.
const sameExitSeparation = 90 * time.Second

// departureSpaced reports whether dep is clear of the departures that have
// already gone: the interval behind the last one off its own runway, the
// gate it goes out over, and the runways that cross its own.
func (s *Sim) departureSpaced(depState *RunwayLaunchState, dep DepartureAircraft,
	airport av.ICAOAirportCode, runway av.RunwayID) bool {
	now := s.State.SimTime

	// Going out a gate is a property of the airport, so this holds a
	// departure behind an earlier one over the same exit whichever runway
	// flew it.
	if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok && ac.FlightPlan.Exit != "" {
		if t, ok := s.LastExitLaunch[airport][ac.FlightPlan.Exit]; ok && now.Sub(t) < sameExitSeparation {
			return false
		}
	}

	// Check if enough time has passed since the last departure
	if depState.LastDeparture != nil &&
		now.Sub(depState.LastDeparture.LaunchTime) < s.sameRunwayLaunchInterval(*depState.LastDeparture, dep) {
		return false
	}

	// Check for conflicts with the last departure from each other runway,
	// both where the runways themselves intersect and where the two
	// aircraft's initial flight paths cross.
	for otherRwy, otherState := range s.DepartureState[airport] {
		if otherRwy.SameRunway(runway) || otherState.LastDeparture == nil {
			continue
		}
		prev := *otherState.LastDeparture
		if pt, ok := av.RunwayIntersectionPoint(airport, runway, otherRwy, s.State.NmPerLongitude, 1); ok {
			if s.holdForRunwayIntersection(prev, dep, pt, airport, runway, otherRwy) {
				return false
			}
		} else if (depState.LastDeparture == nil || prev.ADSBCallsign != depState.LastDeparture.ADSBCallsign) &&
			s.holdForCrossingDeparture(prev, dep) {
			// Note that if prev is also our own runway's last departure (via
			// "departure_runways_as_one"), the launch interval check above
			// has already handled it.
			return false
		}
	}

	return true
}

// holdForRunwayIntersection reports whether dep must wait because prev, a
// recent departure from an intersecting runway, is not yet clear of it. The
// full launch interval is only needed if both aircraft are airborne before
// the intersection point; otherwise it's enough for the previous departure
// to have passed it.
func (s *Sim) holdForRunwayIntersection(prev, dep DepartureAircraft, pt math.Point2LL,
	airport av.ICAOAirportCode, runway, otherRwy av.RunwayID) bool {
	if s.State.SimTime.Sub(prev.LaunchTime) >= s.launchInterval(prev, dep) {
		return false // full separation is satisfied regardless
	}
	bothAirborne := s.airborneBeforeIntersection(prev, airport, otherRwy, pt) &&
		s.airborneBeforeIntersection(dep, airport, runway, pt)
	return bothAirborne || !s.departureHasPassedPoint(prev, airport, otherRwy, pt)
}

// crossingSeparation is the minimum time by which two departures whose
// initial flight paths cross must be separated at the crossing point.
const crossingSeparation = 30 * time.Second

// holdForCrossingDeparture reports whether dep must wait because its
// initial flight path crosses that of prev, a recent departure from another
// runway, too close in time at a crossing point.
func (s *Sim) holdForCrossingDeparture(prev, dep DepartureAircraft) bool {
	if _, ok := s.Aircraft[prev.ADSBCallsign]; !ok {
		return false
	}
	elapsed := s.State.SimTime.Sub(prev.LaunchTime)
	return slices.ContainsFunc(math.IntersectPolylines(prev.LaunchPath, dep.LaunchPath),
		func(c math.SegmentCrossing) bool {
			// Time from now until each aircraft reaches the crossing point
			// (negative if prev has already passed it); the paths are
			// sampled at one-second intervals starting at the takeoff roll.
			prevCrosses := time.Duration(float32(time.Second)*c.TA) - elapsed
			depCrosses := time.Duration(float32(time.Second) * c.TB)
			return (depCrosses - prevCrosses).Abs() < crossingSeparation
		})
}

// runwayThresholdAndDirection returns the runway's threshold and its unit
// departure direction in nm coordinates.
func runwayThresholdAndDirection(airport av.ICAOAirportCode, rwy av.RunwayID, nmPerLongitude float32) ([2]float32, [2]float32, bool) {
	runway, ok := av.LookupRunway(airport, rwy.Base())
	if !ok {
		return [2]float32{}, [2]float32{}, false
	}
	opp, ok := av.LookupOppositeRunway(airport, rwy.Base())
	if !ok {
		return [2]float32{}, [2]float32{}, false
	}
	t := math.LL2NM(runway.Threshold, nmPerLongitude)
	o := math.LL2NM(opp.Threshold, nmPerLongitude)
	return t, math.Normalize2f(math.Sub2f(o, t)), true
}

// airborneBeforeIntersection reports whether the departure lifts off at or
// before pt, a point on its departure runway's centerline.
func (s *Sim) airborneBeforeIntersection(dep DepartureAircraft, airport av.ICAOAirportCode, rwy av.RunwayID, pt math.Point2LL) bool {
	if dep.AirborneDistance < 0 {
		// It didn't get airborne within the horizon of the takeoff-roll simulation.
		return false
	}
	threshold, dir, ok := runwayThresholdAndDirection(airport, rwy, s.State.NmPerLongitude)
	if !ok {
		return true // shouldn't happen; be conservative and require the full interval
	}
	// Signed distance from the threshold to the intersection point along
	// the runway; negative if pt is behind the threshold, in which case the
	// aircraft is never on the ground there.
	d := math.Dot(math.Sub2f(math.LL2NM(pt, s.State.NmPerLongitude), threshold), dir)
	return dep.AirborneDistance <= d
}

// departureHasPassedPoint reports whether the previously-launched departure
// has progressed past pt along its departure runway's direction.
func (s *Sim) departureHasPassedPoint(dep DepartureAircraft, airport av.ICAOAirportCode, rwy av.RunwayID, pt math.Point2LL) bool {
	ac, ok := s.Aircraft[dep.ADSBCallsign]
	if !ok {
		return true // it's been deleted, so it's long gone
	}
	_, dir, ok := runwayThresholdAndDirection(airport, rwy, s.State.NmPerLongitude)
	if !ok {
		return false
	}
	d := math.Dot(math.Sub2f(math.LL2NM(ac.Position(), s.State.NmPerLongitude), math.LL2NM(pt, s.State.NmPerLongitude)), dir)
	const buffer = 0.05 // nm past the intersection: aircraft length plus some slop
	return d > buffer
}

// launchInterval returns the base amount of time--runway occupancy plus
// wake turbulence--we must wait before launching cur, if prev was the last
// aircraft launched from the same pavement. Same-course in-trail separation
// for same-pavement pairs is layered on in sameRunwayLaunchInterval.
// Spacing for the gate cur goes out over is handled separately, in
// departureSpaced: it doesn't depend on which runway flew prev.
func (s *Sim) launchInterval(prev, cur DepartureAircraft) time.Duration {
	cac, cok := s.Aircraft[cur.ADSBCallsign]
	pac, pok := s.Aircraft[prev.ADSBCallsign]

	if !cok || !pok {
		// Presumably the last launch has already landed or otherwise been
		// deleted.
		s.lg.Debugf("Sim launchInterval missing an aircraft %q: %v / %q: %v", cur.ADSBCallsign, cok,
			prev.ADSBCallsign, pok)
		return 0
	}

	// Start with 6,000' and airborne for the launch delay.
	wait := prev.MinSeparation

	// Check for wake turbulence separation.
	wtDist := av.CWTDirectlyBehindSeparation(pac.CWT(), cac.CWT())
	if wtDist != 0 {
		// Assume '1 gives you 3.5'
		wait = max(wait, time.Duration(wtDist/3.5*float32(time.Minute)))
	}

	return wait
}

// sameCourseSeparation is the separation that must exist between successive
// departures off the same pavement at the moment the trailing one lifts off
// when their climbout courses do not diverge (7110.65 5-8-3).
const sameCourseSeparation = 3.0 // nm

// minDivergentCourseAngle is the angle by which successive departures'
// climbout courses must differ to count as diverging (7110.65 5-8-3),
// exempting the trailing one from in-trail separation at liftoff. The
// regulation says 15 degrees; measuring courses from the simulated paths
// can blur a charted split by a degree, so a little slop keeps procedures
// designed with exactly 15 degrees of divergence from being
// mischaracterized.
const minDivergentCourseAngle = 14 // degrees

// climboutCourseDelay is how long after liftoff the climbout course is
// measured from, allowing the initial turn off runway heading to finish.
const climboutCourseDelay = 45 * time.Second

// sameRunwayLaunchInterval returns how long after prev began its takeoff
// roll cur may begin its own when prev was the last departure off cur's
// pavement: the basic launch interval, extended for an IFR pair whose
// climbout courses don't diverge so that sameCourseSeparation will exist
// between them when cur lifts off.
func (s *Sim) sameRunwayLaunchInterval(prev, cur DepartureAircraft) time.Duration {
	wait := s.launchInterval(prev, cur)

	pac, pok := s.Aircraft[prev.ADSBCallsign]
	cac, cok := s.Aircraft[cur.ADSBCallsign]
	if !pok || !cok || pac.FlightPlan.Rules != av.FlightRulesIFR || cac.FlightPlan.Rules != av.FlightRulesIFR {
		return wait // visual separation covers a pair involving a VFR
	}
	if climboutCoursesDiverge(prev, cur, s.State.NmPerLongitude) {
		return wait
	}
	if delay, ok := sameCourseLaunchDelay(prev, cur); ok {
		wait = max(wait, delay)
	}
	return wait
}

// climboutCourse returns the departure's ground track once established on
// its climbout, measured from shortly after liftoff--past the initial turn
// off runway heading--to the end of its precomputed launch path; ok is
// false if the path is too short past liftoff to measure it.
func climboutCourse(dep DepartureAircraft, nmPerLongitude float32) (math.TrueHeading, bool) {
	i0 := int((dep.AirborneTime + climboutCourseDelay) / time.Second)
	i1 := len(dep.LaunchPath) - 1
	if dep.AirborneTime == 0 || i1-i0 < 15 {
		return 0, false
	}
	return math.Heading2LL(dep.LaunchPath[i0], dep.LaunchPath[i1], nmPerLongitude), true
}

// climboutCoursesDiverge reports whether the two departures' climbout
// courses diverge by at least minDivergentCourseAngle; if either course
// can't be measured it conservatively reports false.
func climboutCoursesDiverge(prev, cur DepartureAircraft, nmPerLongitude float32) bool {
	ph, pok := climboutCourse(prev, nmPerLongitude)
	ch, cok := climboutCourse(cur, nmPerLongitude)
	return pok && cok && math.HeadingDifference(ph, ch) >= minDivergentCourseAngle
}

// sameCourseLaunchDelay returns how long after prev began its takeoff roll
// cur must wait to begin its own so that prev will be sameCourseSeparation
// away at the moment cur lifts off. Launch path samples are at one-second
// intervals from the start of the takeoff roll, so index k is prev's
// position k seconds after prev's LaunchTime.
func sameCourseLaunchDelay(prev, cur DepartureAircraft) (time.Duration, bool) {
	ja := int(cur.AirborneTime / time.Second)
	if cur.AirborneTime == 0 || ja >= len(cur.LaunchPath) || len(prev.LaunchPath) < 2 {
		return 0, false
	}
	liftoff := cur.LaunchPath[ja]

	if k := slices.IndexFunc(prev.LaunchPath, func(p math.Point2LL) bool {
		return math.NMDistance2LL(p, liftoff) >= sameCourseSeparation
	}); k != -1 {
		return time.Duration(max(0, k-ja)) * time.Second, true
	}

	// prev doesn't open sameCourseSeparation within its path's horizon;
	// extrapolate at its final groundspeed. Since the pair is in trail,
	// prev is flying essentially straight away from cur's liftoff point.
	n := len(prev.LaunchPath)
	step := math.NMDistance2LL(prev.LaunchPath[n-2], prev.LaunchPath[n-1]) // nm per second
	if step == 0 {
		return 0, false
	}
	short := sameCourseSeparation - math.NMDistance2LL(prev.LaunchPath[n-1], liftoff)
	reach := float32(n-1) + short/step
	return time.Duration(math.Ceil(reach-float32(ja))) * time.Second, true
}

// errNoVFRDestination is returned when arrivals are backed up at every
// airport that takes VFR traffic, leaving nowhere to send a VFR departure
// at the moment.
var errNoVFRDestination = errors.New("no VFR destination airport is accepting arrivals")

// vfrDestinationWeights gives the weight for sampling each airport as the
// destination of a random VFR departure. Airports where arrivals are already
// backed up waiting to land weigh nothing, so that we don't keep adding to
// the pile. Which those are takes a pass over every aircraft in the sim, so
// they are counted once for all the airports rather than once per airport.
func (s *Sim) vfrDestinationWeights() map[av.ICAOAirportCode]float32 {
	orbiting := make(map[av.ICAOAirportCode]int)
	for _, ac := range s.Aircraft {
		if isHoldingArrival(ac) {
			orbiting[ac.FlightPlan.ArrivalAirport]++
		}
	}

	weights := make(map[av.ICAOAirportCode]float32, len(s.State.DepartureAirports))
	for ap := range s.State.DepartureAirports {
		if orbiting[ap] == 0 {
			weights[ap] = s.State.Airports[ap].VFRRateSum()
		}
	}
	return weights
}

func (s *Sim) makeNewVFRDeparture(depart av.ICAOAirportCode, runway av.RunwayID) (ac *Aircraft, err error) {
	depState := s.DepartureState[depart][runway]
	if len(depState.ReleasedVFR) >= 5 || len(depState.ReleasedIFR) >= maxHoldingShort {
		// There's a backup; hold off on more.
		return
	}

	if depState.VFRSpawnRate == 0 {
		return
	}

	// Don't waste time trying to find a valid launch if it's been
	// near-impossible to find valid routes.
	if depState.VFRAttempts < 400 ||
		(depState.VFRSuccesses > 0 && depState.VFRAttempts/depState.VFRSuccesses < 200) {
		ap := s.State.Airports[depart]

		// Sample among the randoms and the routes
		var rateSum float32
		var sampledRandoms *av.VFRRandomsSpec
		var sampledRoute *av.VFRRouteSpec
		if ap.VFR.Randoms.Rate > 0 {
			rateSum = ap.VFR.Randoms.Rate
			sampledRandoms = &ap.VFR.Randoms
		}
		for _, route := range ap.VFR.Routes {
			if route.Rate > 0 {
				rateSum += route.Rate
				p := route.Rate / rateSum
				if s.Rand.Float32() < p {
					sampledRandoms = nil
					sampledRoute = &route
				}
			}
		}

		if sampledRandoms == nil && sampledRoute == nil {
			// Nothing with a nonzero rate to sample from.
			return
		}

		if sampledRoute != nil && s.orbitingArrivals(sampledRoute.Destination) > 0 {
			// Arrivals are backed up at the route's destination; hold off
			// on this one and try again later.
			return nil, errNoVFRDestination
		}

		// The candidates a destination is sampled from don't change over the
		// attempts below: a failed one leaves no trace in the sim and a
		// successful one returns before another sample is drawn.
		var destinations []av.ICAOAirportCode
		var destinationWeights map[av.ICAOAirportCode]float32
		if sampledRandoms != nil {
			destinations = util.SortedMapKeys(s.State.DepartureAirports)
			destinationWeights = s.vfrDestinationWeights()
		}
		callsigns := s.currentCallsigns()

		for range 5 {
			var arrive av.ICAOAirportCode
			var fleet string
			var routeWps []av.Waypoint
			if sampledRandoms != nil {
				// Sample destination airport: may be where we started from.
				dest, ok := rand.SampleWeightedSeq(s.Rand, slices.Values(destinations),
					func(ap av.ICAOAirportCode) float32 { return destinationWeights[ap] })
				if !ok {
					// Arrivals are backed up at every airport that takes
					// VFR traffic; wait for one of them to clear.
					return nil, errNoVFRDestination
				}
				arrive, fleet = dest, sampledRandoms.Fleet
			} else {
				arrive, fleet, routeWps = sampledRoute.Destination, sampledRoute.Fleet, sampledRoute.Waypoints
			}

			// Only count attempts where we actually went looking for a
			// route; the circuit breaker above is about routes that can't
			// be found, not about destinations being busy.
			depState.VFRAttempts++
			ac, _, err = s.createUncontrolledVFRDeparture(depart, arrive, fleet, routeWps, callsigns, s.State.SimTime)

			if err == nil && ac != nil {
				ac.ReleaseTime = s.State.SimTime
				depState.VFRSuccesses++
				return
			}
		}
		return nil, ErrViolatedAirspace
	}
	return
}

func (s *Sim) cullDepartures(keep int, d []DepartureAircraft) []DepartureAircraft {
	if len(d) < keep {
		return d
	}

	for _, dep := range d[keep:] {
		if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok {
			s.deleteAircraft(ac)
		}
	}
	return d[:keep]
}

func (rls *RunwayLaunchState) cullDepartures(s *Sim) {
	keep := int(rls.IFRSpawnRate+rls.VFRSpawnRate) / 6
	rls.Gate = s.cullDepartures(keep, rls.Gate)
	rls.Held = s.cullDepartures(keep, rls.Held)
	rls.ReleasedIFR = s.cullDepartures(keep, rls.ReleasedIFR)
	rls.ReleasedVFR = s.cullDepartures(keep, rls.ReleasedVFR)
}

func (rls *RunwayLaunchState) filterDeleted(aircraft map[av.ADSBCallsign]*Aircraft) {
	haveAc := func(dep DepartureAircraft) bool {
		_, ok := aircraft[dep.ADSBCallsign]
		return ok
	}
	rls.Gate = util.FilterSliceInPlace(rls.Gate, haveAc)
	rls.Held = util.FilterSliceInPlace(rls.Held, haveAc)
	rls.ReleasedIFR = util.FilterSliceInPlace(rls.ReleasedIFR, haveAc)
	rls.ReleasedVFR = util.FilterSliceInPlace(rls.ReleasedVFR, haveAc)
}

func (rls *RunwayLaunchState) setIFRRate(s *Sim, r float32) {
	if r == rls.IFRSpawnRate {
		return
	}
	rls.IFRSpawnRate = r
	rls.cullDepartures(s)
}

func (rls *RunwayLaunchState) setVFRRate(s *Sim, r float32) {
	if r == rls.VFRSpawnRate {
		return
	}
	rls.VFRSpawnRate = r
	rls.NextVFRSpawn = s.State.SimTime.Add(randomInitialWait(r, s.Rand))
	rls.cullDepartures(s)
}

func (rls RunwayLaunchState) Dump(airport av.ICAOAirportCode, runway av.RunwayID, now Time) {
	callsign := func(dep DepartureAircraft) string {
		return string(dep.ADSBCallsign)
	}
	fmt.Printf("%s/%s: Gate %s Held %s Released IFR %s Released VFR %s\n", airport, runway,
		strings.Join(util.MapSlice(rls.Gate, callsign), ", "),
		strings.Join(util.MapSlice(rls.Held, callsign), ", "),
		strings.Join(util.MapSlice(rls.ReleasedIFR, callsign), ", "),
		strings.Join(util.MapSlice(rls.ReleasedVFR, callsign), ", "))
	if rls.IFRSpawnRate > 0 {
		fmt.Printf("    IFR rate %f\n", rls.IFRSpawnRate)
	}
	if rls.VFRSpawnRate > 0 {
		fmt.Printf("    next VFR in %s, rate %f\n", rls.NextVFRSpawn.Sub(now), rls.VFRSpawnRate)
	}
}

// assignDepartureController sets up controller assignments for a departure.
// It handles three cases:
// 1. Airport has a virtual departure controller -> auto-release, use airport controller
// 2. Exit route has a virtual departure controller -> auto-release, use exit route controller
// 3. Human controller -> set contact altitude, use human controller position
func (s *Sim) assignDepartureController(ac *Aircraft, nasFp *NASFlightPlan,
	ap *av.Airport, exitRoute *av.ExitRoute, departureAirport av.ICAOAirportCode, runway string) {

	// Departures that start with a virtual controller are already on its
	// frequency, so they never check in with a departure controller; -1 keeps
	// them from being mistaken for ones waiting on a /tc point.
	if ap.DepartureController != "" && s.isVirtualController(ap.DepartureController) {
		// Virtual controller from airport; automatically release since there's no human.
		nasFp.TrackingController = TCP(ap.DepartureController)
		nasFp.OwningTCW = s.tcwForPosition(ap.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.ControllerFrequency = ControlPosition(ap.DepartureController)
		ac.DepartureContactAltitude = -1
		ac.HoldForRelease = false
		return
	}

	if exitRoute.DepartureController != "" && s.isVirtualController(exitRoute.DepartureController) {
		// Virtual controller from exit route; automatically release.
		nasFp.TrackingController = TCP(exitRoute.DepartureController)
		nasFp.OwningTCW = s.tcwForPosition(exitRoute.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.ControllerFrequency = ControlPosition(exitRoute.DepartureController)
		ac.DepartureContactAltitude = -1
		ac.HoldForRelease = false
		return
	}

	// Human controller will be first
	pos := s.scenarioRootPosition()
	if tcp := s.GetDepartureController(departureAirport, runway, exitRoute.SID); tcp != "" {
		pos = tcp
	}

	// Set altitude at which aircraft will contact departure control
	if exitRoute.WaitToContactDeparture {
		ac.DepartureContactAltitude = 0
	} else {
		ac.DepartureContactAltitude = ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(s.Rand.Intn(500))
		ac.DepartureContactAltitude = min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))
	}

	nasFp.TrackingController = pos
	nasFp.OwningTCW = s.tcwForPosition(pos)
	nasFp.InboundHandoffController = pos
}

// createScenarioIFRDeparture creates the scenario IFR departure a schedule
// entry describes; the runway, category, departure route, and identity were
// all sampled when the entry was generated. All resource allocation--squawk,
// flight strip, flight plan, list index--happens here.
func (s *Sim) createScenarioIFRDeparture(e ScheduledDeparture) (*Aircraft, error) {
	ap, rwy, exitRoutes, err := s.State.departureConfiguration(e.DepartureAirport, e.Runway, e.Category)
	if err != nil {
		return nil, err
	}
	if e.DepartureIndex < 0 || e.DepartureIndex >= len(ap.Departures) {
		return nil, fmt.Errorf("%s/%s: no departure at index %d", e.DepartureAirport, rwy.Runway,
			e.DepartureIndex)
	}
	dep := &ap.Departures[e.DepartureIndex]

	callsign, err := s.resolveScheduledCallsign(&e.ScheduledFlight, "departure")
	if err != nil {
		return nil, err
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(av.FlightRulesIFR, e.AircraftType, e.DepartureAirport, dep.Destination)

	routes := av.ExitRoutesForAircraft(exitRoutes, e.AircraftType)
	if _, ok := routes[dep.Exit]; !ok {
		return nil, fmt.Errorf("%s/%s: no route to %s for a %s", e.DepartureAirport, rwy.Runway,
			dep.Exit, e.AircraftType)
	}

	return s.initializeIFRDepartureNoLock(ac, ap, e.DepartureAirport, e.Runway, dep,
		CruiseLimits{}, routes)
}

// createPublishedIFRDeparture creates a departure using the published identity
// from a timetable or from historical flight data. Vice still resolves the
// exit, SID, route, altitude, and controller assignment from the active
// scenario.
// The categories are the ones the scenario is launching from this runway; the
// one used is whichever gets the aircraft closest to where it really went,
// rather than one sampled by rate. Published traffic takes its share of each
// exit from the flights themselves.
func (s *Sim) createPublishedIFRDeparture(e ScheduledDeparture, runway av.RunwayID,
	categories []string) (*Aircraft, error) {
	callsign := strings.ToUpper(strings.TrimSpace(e.Callsign))
	if callsign == "" {
		return nil, fmt.Errorf("published departure callsign is empty")
	}

	if av.CallsignClashesWithExisting(s.currentCallsigns(), callsign, s.EnforceUniqueCallsignSuffix) {
		return nil, fmt.Errorf("published departure %s: %w", callsign, errCallsignInUse)
	}

	if _, ok := av.DB.AircraftPerformance[e.AircraftType]; !ok {
		return nil, fmt.Errorf(
			"aircraft type %s is not present in the performance database",
			e.AircraftType,
		)
	}

	placement, err := s.State.resolvePublishedDeparture(e.DepartureAirport, runway, categories,
		e.ArrivalAirport, e.AircraftType, s.routedPairsIndex().destinationsByOrigin)
	if err != nil {
		return nil, err
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(av.FlightRulesIFR, e.AircraftType, e.DepartureAirport, e.ArrivalAirport)

	s.log("%s: departure %s->%s runway %s exit %s (%s)", callsign, e.DepartureAirport,
		e.ArrivalAirport, runway, placement.dep.Exit, placement.how)

	return s.initializeIFRDepartureNoLock(ac, placement.ap, e.DepartureAirport, runway, &placement.dep,
		placement.cruise, placement.exitRoutes)
}

func (ss *CommonState) departureConfiguration(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	category string) (*av.Airport, *DepartureRunway, map[av.ExitID]av.ExitRoutes, error) {
	ap := ss.Airports[departureAirport]
	if ap == nil {
		return nil, nil, nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(ss.DepartureRunways,
		func(r DepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, nil, av.ErrUnknownRunway
	}
	rwy := &ss.DepartureRunways[idx]
	return ap, rwy, ap.DepartureRoutes[rwy.Runway], nil
}

// errNoScenarioRoute means a runway can't plausibly work a published flight:
// no gate it launches goes anywhere near where the flight really went. Another
// runway the scenario is launching from may still fly it; a flight none of them
// can is dropped before it spawns rather than forced through an unrelated gate.
var errNoScenarioRoute = errors.New("no plausible route in this scenario")

// publishedDepartureMaxHeadingDifference bounds how far off in direction a
// substituted departure route may be. A scenario that models one gate of a
// busy airport should be handed the flights that plausibly leave through it,
// not every departure from the airport.
const publishedDepartureMaxHeadingDifference = 45 // degrees

// publishedSubstituteMaxExitHeadingDifference bounds the gate a borrowed route
// may go out. It is far wider than the limit on the substitute airport itself,
// since a gate sits twenty or thirty miles out and the turn onto course comes
// later: JFK's Florida traffic leaves over WAVEY, 60 degrees off the direct
// line. What it rules out is setting off in the other direction entirely.
const publishedSubstituteMaxExitHeadingDifference = 90 // degrees

// candidateDeparture is a scenario departure a published flight could fly,
// together with the runway category configuration it came from.
type candidateDeparture struct {
	ap         *av.Airport
	rwy        *DepartureRunway
	exitRoutes map[av.ExitID]*av.ExitRoute
	dep        *av.Departure
}

// compatibleDepartures collects the exits the given runway categories can
// launch the aircraft type out of, one candidate per exit: published traffic
// brings its own destination, and the routes say which exit it really leaves
// through. The scenario's "departures" have no say here; they belong to its
// own generator.
func (ss *CommonState) compatibleDepartures(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, aircraftType string) []candidateDeparture {
	var candidates []candidateDeparture
	for _, category := range categories {
		ap, rwy, allRoutes, err := ss.departureConfiguration(departureAirport, runway, category)
		if err != nil {
			continue
		}
		exitRoutes := av.ExitRoutesForAircraft(allRoutes, aircraftType)

		inCategory := func(exit av.ExitID) bool {
			return rwy.Category == "" || rwy.Category == ap.ExitCategory(exit)
		}

		exits := util.FilterSlice(util.SortedMapKeys(exitRoutes), inCategory)
		// One backing array for the whole category, so the pointers stay
		// valid as the slice below grows.
		synthesized := make([]av.Departure, len(exits))
		for i, exit := range exits {
			synthesized[i] = av.Departure{Exit: exit}
			candidates = append(candidates, candidateDeparture{ap, rwy, exitRoutes, &synthesized[i]})
		}
	}
	return candidates
}

// departureFit ranks how well a runway's gates suit a published flight, best
// first: a runway that flies the flight's own route is a better place to launch
// it from than one that only has a gate pointing the same general way.
type departureFit int

const (
	fitScenarioRoute departureFit = iota // the scenario's route for the city pair
	fitRealRoute                         // a scraped filing or FAA route for the pair
	fitNeighborRoute                     // the route to the nearest routed destination
	fitNearestGate                       // nothing routed; the gate heading the right way
)

// departureChoice is the exit a published flight leaves through, the real-world
// route that found it if one did, how well the runway's gates fit the flight,
// and how the choice was made, for reporting. Finding it costs only database
// lookups, so a runway can ask whether it works a flight without paying to turn
// the route into waypoints. A choice that comes back with an error carries only
// the route the flight would have filed, so that the failure can be read.
type departureChoice struct {
	candidate candidateDeparture
	route     string
	cruise    CruiseLimits
	fit       departureFit
	how       string
}

// departurePlacement is the departure a published flight flies and how the
// choice was made, for reporting. Its departure is a resolved copy of the
// candidate's: one authored by the scenario carries its own route, while one
// synthesized for an airport that names no departures gets the route here.
type departurePlacement struct {
	ap         *av.Airport
	rwy        *DepartureRunway
	exitRoutes map[av.ExitID]*av.ExitRoute
	dep        av.Departure
	cruise     CruiseLimits
	how        string
}

// placement resolves the choice into the departure the flight flies. A choice
// with no route falls back to flying to its exit fix, since without that its
// route ends with the scenario's vector off the runway and it would head
// straight for its destination from wherever that leaves it.
func (ss *CommonState) placement(choice departureChoice, departureAirport, destination av.ICAOAirportCode) departurePlacement {
	c := choice.candidate
	p := departurePlacement{ap: c.ap, rwy: c.rwy, exitRoutes: c.exitRoutes, dep: *c.dep,
		cruise: choice.cruise, how: choice.how}
	if choice.route != "" {
		p.cruise.Floor = av.RouteAltitudeFloor(choice.route, departureAirport, destination)
	}

	exitRoute := c.exitRoutes[c.dep.Exit]
	if choice.route != "" {
		p.dep.Route = departureRoute(choice.route, departureAirport, c.dep.Exit, exitRoute)
		p.dep.RouteWaypoints = dropFlownPrefix(ss.departureRouteWaypoints(p.dep.Route),
			exitRoute.Waypoints)
	} else {
		p.dep.Route = string(c.dep.Exit.Base())
		p.dep.RouteWaypoints = ss.departureRouteWaypoints(p.dep.Route)
	}
	return p
}

// departureRoute is the part of a filed route a departure flies and files:
// everything past the tokens naming the origin airport, in full--the fixes
// between the airport and the exit are flown, not trimmed away. A leading SID
// token drops out whichever SID it names, since it's the scenario's exit
// route that flies the fixes off the runway and its SID that goes on the
// flight plan; the exit fix is put in front only when neither the route nor
// the exit route reaches it, so that "direct on course" still goes out over
// the gate--JFK to Cleveland files "KJFK DEEZZ6 CANDR J60...", and with the
// DEEZZ6 exit route authored as plain vectors, DEEZZ has to lead the route
// itself.
func departureRoute(route string, departureAirport av.ICAOAirportCode, exit av.ExitID, exitRoute *av.ExitRoute) string {
	fields := av.TrimDepartureAirportTokens(strings.Fields(route), departureAirport)
	if len(fields) > 0 && av.TokenNamesProcedure(fields[0]) {
		fields = fields[1:]
	}

	fix := exit.Base()
	onExitRoute := slices.ContainsFunc(exitRoute.Waypoints,
		func(wp av.Waypoint) bool { return wp.Fix == fix })
	if !slices.Contains(fields, fix) && !onExitRoute {
		fields = append([]string{fix}, fields...)
	}
	return strings.Join(fields, " ")
}

// dropFlownPrefix removes the leading route waypoints the exit route already
// flies: the route resumes after the last fix they share, so an exit route
// that ends at the exit fix doesn't send the aircraft back to a fix behind it.
// The fix the exit route ends at is the exception: the route keeps its own
// copy, which av.SpliceRoutes merges into the exit route's, so that the airway
// the flight leaves the fix on comes along.
func dropFlownPrefix(routeWps, exitWps av.WaypointArray) av.WaypointArray {
	for i, exitWp := range slices.Backward(exitWps) {
		for j, routeWp := range slices.Backward(routeWps) {
			if routeWp.Fix == exitWp.Fix {
				return routeWps[j+util.Select(i == len(exitWps)-1, 0, 1):]
			}
		}
	}
	return routeWps
}

// departureRouteWaypoints locates the fixes of a departure's enroute route,
// stopping at the point where the sim lets the aircraft go: the fixes past
// there are never flown and every one of them is sent to the clients on every
// update. Fixes it can't place--SID and STAR names, radial/DME fixes--drop out.
func (ss *CommonState) departureRouteWaypoints(route string) av.WaypointArray {
	wps := av.RouteWaypoints(route).InitializeLocations(ss, ss.NmPerLongitude,
		ss.MagneticVariation, true /* allowSlop */, nil)

	cull := ss.cullDistance()
	if i := slices.IndexFunc(wps, func(wp av.Waypoint) bool {
		return math.NMDistance2LL(wp.Location, ss.Center) > cull
	}); i != -1 {
		wps = wps[:i+1] // keep the first one past it so the aircraft flies out on course
	}
	return wps
}

// resolvePublishedDeparture finds the departure a published flight flies off a
// runway, ready to be handed to the aircraft.
func (ss *CommonState) resolvePublishedDeparture(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, destination av.ICAOAirportCode, aircraftType string,
	routedDestinations map[av.ICAOAirportCode][]av.ICAOAirportCode) (departurePlacement, error) {
	departureAirport = normalizeAirportCode(departureAirport)
	choice, err := ss.findPublishedDeparture(departureAirport, runway, categories, destination,
		aircraftType, routedDestinations)
	if err != nil {
		return departurePlacement{}, err
	}
	return ss.placement(choice, departureAirport, destination), nil
}

// findPublishedDeparture finds the scenario exit and route a published
// flight flies. A route the scenario gives for the city pair wins outright;
// otherwise, if the route database knows how the pair is really flown and one
// of its routes leaves through a modeled exit, the flight follows that exit
// and files the real route. Failing both, the flight flies the way its
// nearest routed neighbor is left for--minus that route's tail, which belongs
// to the neighbor--since Vero Beach has no route from JFK but Orlando 66nm
// away leaves over WAVEY. Last of all it goes out the exit lying closest to
// the direction it is going. If nothing is in the right direction at all the
// runway doesn't work this flight and errNoScenarioRoute says not to launch it
// from here.
func (ss *CommonState) findPublishedDeparture(departureAirport av.ICAOAirportCode, runway av.RunwayID,
	categories []string, destination av.ICAOAirportCode, aircraftType string,
	routedDestinations map[av.ICAOAirportCode][]av.ICAOAirportCode) (departureChoice, error) {
	departureAirport = normalizeAirportCode(departureAirport)
	destination = normalizeAirportCode(destination)

	candidates := ss.compatibleDepartures(departureAirport, runway, categories, aircraftType)
	if len(candidates) == 0 {
		return departureChoice{}, fmt.Errorf("no compatible departure route for runway %s and a %s",
			runway, aircraftType)
	}

	scenarioRoutes := func(to av.ICAOAirportCode) []string {
		if ap, ok := ss.Airports[departureAirport]; ok {
			return ap.TrafficRoutes.Departures[to].Routes(aircraftType)
		}
		return nil
	}
	hour, hourKnown := ss.localHour(departureAirport)

	scenario := scenarioRoutes(destination)
	real := realDepartureRoutes(departureAirport, destination, aircraftType, hour, hourKnown)
	// The route the flight would file, kept aside so that a flight no runway
	// can work still reports the route that couldn't be fitted: that is what
	// says why it couldn't be.
	filed := ""
	if len(scenario) > 0 {
		filed = scenario[0]
	} else if len(real) > 0 {
		filed = real[0].route
	}

	for _, route := range scenario {
		if c, ok := departureExit(route, departureAirport, destination, "", candidates); ok {
			return departureChoice{candidate: c, route: route, fit: fitScenarioRoute,
				how: "scenario route via " + c.dep.Exit.Base()}, nil
		}
	}
	for _, r := range real {
		if c, ok := departureExit(r.route, departureAirport, destination, r.departureFix, candidates); ok {
			// The scraped filings say what altitudes the route is really
			// flown at; file within them when the aircraft can.
			return departureChoice{candidate: c, route: r.route,
				cruise: CruiseLimits{Low: r.minAltitude, High: r.maxAltitude},
				fit:    fitRealRoute,
				how:    r.how + " via " + c.dep.Exit.Base()}, nil
		}
	}

	// Either the pair has no route or every route it has leaves through an
	// exit this configuration doesn't model: a scenario that works one corner
	// of an airport has no reason to model the gate a filed route uses. Fly
	// the way the nearest destination that does have a workable route is left
	// for, keeping that route only as far as it is this flight's own: the
	// trailing airport and STAR belong to the neighbor. Heading and distance
	// gate plausibility--a route the scenario models in some other direction
	// entirely is no way to leave, however close its destination.
	pool := slices.Clone(routedDestinations[departureAirport])
	if ap, ok := ss.Airports[departureAirport]; ok {
		for _, to := range util.SortedMapKeys(ap.TrafficRoutes.Departures) {
			if len(scenarioRoutes(to)) > 0 {
				pool = append(pool, to)
			}
		}
	}

	origin, originOK := av.DB.Airports[departureAirport]
	trueAirport, trueOK := av.DB.Airports[destination]
	if !originOK || !trueOK {
		return departureChoice{route: filed}, fmt.Errorf(
			"no route to %s and airport coordinates are unavailable", destination)
	}
	trueHeading := math.GreatCircleHeading(origin.Location, trueAirport.Location)

	// A borrowed route is only as good as the gate it goes out: the neighbor
	// lies the right way, but nothing so far says its route leaves that way.
	// Birmingham stands in for Atlanta from Minneapolis, yet one of its routes
	// sets off up the northeast gate.
	towardDestination := func(c candidateDeparture) bool {
		difference, ok := exitHeadingDifference(c, origin.Location, trueHeading, ss.NmPerLongitude)
		return !ok || difference <= publishedSubstituteMaxExitHeadingDifference
	}
	for _, substitute := range substituteAirports(departureAirport, destination, pool,
		publishedDepartureMaxHeadingDifference) {
		for _, route := range scenarioRoutes(substitute) {
			if c, ok := departureExit(route, departureAirport, substitute, "", candidates); ok &&
				towardDestination(c) {
				return departureChoice{candidate: c, route: stripSubstituteTail(route, substitute),
					fit: fitNeighborRoute, how: "nearest route, to " + string(substitute)}, nil
			}
		}
		for _, r := range realDepartureRoutes(departureAirport, substitute, aircraftType, hour, hourKnown) {
			if c, ok := departureExit(r.route, departureAirport, substitute, r.departureFix, candidates); ok &&
				towardDestination(c) {
				return departureChoice{candidate: c, route: stripSubstituteTail(r.route, substitute),
					fit: fitNeighborRoute, how: "nearest route, to " + string(substitute)}, nil
			}
		}
	}

	// Nothing is routed anywhere near where this flight is going; the exits
	// themselves say which way each one leaves.
	if c, ok := exitTowardDestination(candidates, origin.Location, trueHeading,
		ss.NmPerLongitude); ok {
		return departureChoice{candidate: c, fit: fitNearestGate, how: "nearest gate"}, nil
	}
	return departureChoice{route: filed}, fmt.Errorf("%w: no modeled departure heads toward %s",
		errNoScenarioRoute, destination)
}

// stripSubstituteTail removes the parts of a borrowed route that belong to its
// own destination rather than the flight's: the trailing airport token and the
// STAR ahead of it.
func stripSubstituteTail(route string, substitute av.ICAOAirportCode) string {
	fields := av.TrimDestinationAirportTokens(strings.Fields(route), substitute)
	if n := len(fields); n > 0 {
		last := fields[n-1]
		if c := last[len(last)-1]; c >= '0' && c <= '9' {
			if _, ok := av.DB.Airways[last]; !ok {
				fields = fields[:n-1]
			}
		}
	}
	return strings.Join(fields, " ")
}

// realRoute is one way a city pair is really flown, from the scraped recent
// filings or from the FAA databases.
type realRoute struct {
	route        string
	departureFix string // a coded departure route names its own
	how          string
	minAltitude  int // the scraped filed altitudes, when known
	maxAltitude  int
}

// realDepartureRoutes returns the ways the pair is really flown: recently
// scraped filings first, ordered for the aircraft and the hour of day,
// followed by the FAA databases' routes.
func realDepartureRoutes(from, to av.ICAOAirportCode, aircraftType string, hour int, hourKnown bool) []realRoute {
	var routes []realRoute
	for _, r := range orderScrapedRoutes(av.DB.ScrapedRoutesBetween(from, to),
		aircraftType, hour, hourKnown) {
		routes = append(routes, realRoute{route: r.Route, how: "scraped route",
			minAltitude: r.MinAltitude, maxAltitude: r.MaxAltitude})
	}
	for _, r := range eligibleAirportPairRoutes(av.DB.RoutesBetween(from, to),
		engineTypeFor(aircraftType)) {
		routes = append(routes, realRoute{route: r.Route, departureFix: r.DepartureFix,
			how: "faa route"})
	}
	return routes
}

// exitHeadingDifference is how far a candidate's exit fix lies from the
// direction the flight is really going. A fix the database can't place gets no
// say either way.
func exitHeadingDifference(c candidateDeparture, airport math.Point2LL,
	trueHeading math.TrueHeading, nmPerLongitude float32) (float32, bool) {
	exit, ok := av.DB.LookupWaypoint(c.dep.Exit.Base())
	if !ok {
		return 0, false
	}
	return math.HeadingDifference(trueHeading, math.Heading2LL(airport, exit, nmPerLongitude)), true
}

// exitTowardDestination picks the candidate whose exit fix lies closest in
// direction to where the flight is really going.
func exitTowardDestination(candidates []candidateDeparture, airport math.Point2LL,
	trueHeading math.TrueHeading, nmPerLongitude float32) (candidateDeparture, bool) {
	var best candidateDeparture
	bestDifference := float32(0)
	for _, c := range candidates {
		difference, ok := exitHeadingDifference(c, airport, trueHeading, nmPerLongitude)
		if !ok || difference > publishedDepartureMaxHeadingDifference {
			continue
		}
		if best.dep == nil || difference < bestDifference {
			best, bestDifference = c, difference
		}
	}
	return best, best.dep != nil
}

// eligibleAirportPairRoutes filters the FAA preferred routes for a city pair to
// the ones the aircraft can fly and orders them by preference: jets take
// high-altitude routes first, everything else low-altitude ones.
func eligibleAirportPairRoutes(routes []av.AirportPairRoute, engineType string) []av.AirportPairRoute {
	eligible := func(r av.AirportPairRoute) bool {
		switch engineType {
		case "P": // pistons fly conventional, non-jet routes
			return !r.RNAVRequired && r.Aircraft != "jet"
		case "J":
			return r.Aircraft != "prop"
		default: // turboprops and anything unknown
			return r.Aircraft != "jet"
		}
	}

	var ordered []av.AirportPairRoute
	lowFirst := engineType != "J"
	for _, low := range []bool{lowFirst, !lowFirst} {
		for _, r := range routes {
			if r.LowAltitude() == low && eligible(r) {
				ordered = append(ordered, r)
			}
		}
	}
	return ordered
}

// departureExit finds the compatible departure whose exit a filed route
// leaves through: the first of the route's fixes the scenario models, since
// that is the one the flight actually goes out over. Failing that, the
// route's first fix tells where the flight rejoins its own navigation on a
// SID the exits fly, and the exit behind that fix on the charted path is the
// gate it goes out over--JFK to Las Vegas files "KJFK DEEZZ6 CANDR J60...",
// resuming at CANDR with the DEEZZ exit behind it. When the route leaves the
// SID before reaching any exit, the exit ahead stands in. A coded departure
// route's own departure fix is the last thing to go on. The filed SID's name
// is never consulted: it may not be the SID the scenario flies for the gate.
func departureExit(route string, departureAirport, destination av.ICAOAirportCode, departureFix string,
	candidates []candidateDeparture) (candidateDeparture, bool) {
	wps := av.TrimDepartureAirportWaypoints(av.RouteWaypoints(route), departureAirport)
	wps = av.TrimDestinationAirportWaypoints(wps, destination)
	if departureFix != "" {
		wps = append(wps, av.Waypoint{Fix: departureFix})
	}

	for _, wp := range wps {
		if i := slices.IndexFunc(candidates, func(c candidateDeparture) bool {
			return c.dep.Exit.Base() == wp.Fix
		}); i != -1 {
			return candidates[i], true
		}
	}

	// No exit fix on the route; find where its first fix joins the SIDs the
	// exits fly.
	exitCandidates := make(map[string]candidateDeparture)
	exitBases := make(map[string]bool)
	var sids []string
	for _, c := range candidates {
		base := c.dep.Exit.Base()
		if _, ok := exitCandidates[base]; !ok {
			exitCandidates[base] = c
			exitBases[base] = true
		}
		if exitRoute, ok := c.exitRoutes[c.dep.Exit]; ok && exitRoute.SID != "" {
			name, _, _ := strings.Cut(exitRoute.SID, ".")
			if !slices.Contains(sids, name) {
				sids = append(sids, name)
			}
		}
	}

	behind, ahead := av.SIDPathExits(departureAirport, wps, exitBases, sids)
	matches := util.Select(len(behind) > 0, behind, ahead)
	if len(matches) == 0 {
		return candidateDeparture{}, false
	}
	if len(matches) == 1 {
		return exitCandidates[matches[0]], true
	}

	// Several paths' exits could stand in; the one nearest the route's
	// first locatable fix is the one the flight leaves through.
	for _, wp := range wps {
		routeFix, ok := av.DB.LookupWaypoint(wp.Fix)
		if !ok {
			continue
		}
		best, bestDistance := "", float32(0)
		for _, m := range matches {
			exit, ok := av.DB.LookupWaypoint(m)
			if !ok {
				continue
			}
			if d := math.NMDistance2LL(exit, routeFix); best == "" || d < bestDistance {
				best, bestDistance = m, d
			}
		}
		if best != "" {
			return exitCandidates[best], true
		}
		break
	}
	return exitCandidates[matches[0]], true
}

func (s *Sim) initializeIFRDepartureNoLock(ac *Aircraft, ap *av.Airport, departureAirport av.ICAOAirportCode,
	runway av.RunwayID, dep *av.Departure, cruise CruiseLimits,
	exitRoutes map[av.ExitID]*av.ExitRoute) (*Aircraft, error) {
	exitRoute := exitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, string(runway), *exitRoute, cruise,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	ac.ReportDepartureHeading = exitRoutesHaveVariedHeadings(exitRoutes)
	ac.ReportDepartureSID = exitRoutesHaveVariedSIDs(exitRoutes)

	shortExit := dep.Exit.Base()
	isTRACON := av.DB.IsTRACON(s.State.Facility)
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeDeparture)
	nasFp.Route = ac.FlightPlan.Route
	nasFp.EntryFix = av.AirportDisplayId(ac.FlightPlan.DepartureAirport)
	// The flight plan carries the exit's 3-character fix id when one is
	// adapted; fix-pair endpoints and adapted fix criteria match against it.
	nasFp.ExitFix = s.State.FacilityAdaptation.FixPairFixID(shortExit)
	nasFp.SecondaryScratchpad = dep.SecondaryScratchpad
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude
	nasFp.AssignedAltitude = util.Select(!isTRACON, ac.FlightPlan.Altitude, 0)
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && exitRoute.IsRNAV

	ac.HoldForRelease = (ap.HoldForRelease || exitRoute.HoldForRelease) && ac.FlightPlan.Rules == av.FlightRulesIFR // VFRs aren't held
	s.assignDepartureController(ac, &nasFp, ap, exitRoute, departureAirport, string(runway))

	// Adapted scratchpads are per-area, so this must follow the controller assignment above.
	if dep.Scratchpad != "" {
		nasFp.Scratchpad = dep.Scratchpad
	} else if sp1 := s.State.FacilityAdaptation.Datablocks.Scratchpad1; sp1.DisplayExitFix ||
		sp1.DisplayExitFix1 || sp1.DisplayExitGate || sp1.DisplayAltExitGate {
		// Don't set the scratchpad; it will be set automatically.
	} else {
		nasFp.Scratchpad = s.State.FacilityAdaptation.ScratchpadForExit(dep.Exit,
			s.areaForTCP(nasFp.TrackingController))
	}

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// departure assignment above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, ac)
	// A fully-contained (internal) flight whose exit fix is a local-arrival
	// airport is reclassified as an arrival for display/processing. The initial
	// owner stays the departure controller assigned above; ownership is
	// deliberately not re-derived as an arrival.
	if nasFp.LocalArrival {
		nasFp.TypeOfFlight = av.FlightTypeArrival
	}
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	// hold on to their flight plans for now.
	// Create a flight strip for departures
	printStrips := ap.PrintDepartureStrips == nil || *ap.PrintDepartureStrips
	if printStrips && shouldCreateFlightStrip(&nasFp) {
		if s.isVirtualController(nasFp.TrackingController) {
			// Virtual controller: strip goes to the handoff target
			if !s.isVirtualController(nasFp.InboundHandoffController) {
				s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
			}
		} else {
			// Human controller: strip goes to the tracking controller
			s.initFlightStrip(&nasFp, nasFp.TrackingController)
		}
	}

	_, err = s.STARSComputer.CreateFlightPlan(nasFp)
	return ac, err
}

// sampleVFRDeparture samples a VFR departure from the given airport for a
// manual launch slot. Note that it may fail without an error if it's having
// trouble finding a route.
func (s *Sim) sampleVFRDeparture(departureAirport av.ICAOAirportCode) (*Aircraft, error) {
	// Sample destination airport: may be where we started from.
	weights := s.vfrDestinationWeights()
	arrive, ok := rand.SampleWeightedSeq(s.Rand, slices.Values(util.SortedMapKeys(s.State.DepartureAirports)),
		func(ap av.ICAOAirportCode) float32 { return weights[ap] })
	if !ok {
		// Arrivals are backed up everywhere, but a controller asked for this
		// aircraft, so send it somewhere anyway.
		arrive, ok = rand.SampleWeightedSeq(s.Rand, slices.Values(util.SortedMapKeys(s.State.DepartureAirports)),
			func(ap av.ICAOAirportCode) float32 { return s.State.Airports[ap].VFRRateSum() })
		if !ok {
			return nil, nil
		}
	}

	ap, ok := s.State.Airports[departureAirport]
	if !ok || ap.VFRRateSum() == 0 {
		// This shouldn't happen...
		return nil, nil
	}

	ac, _, err := s.createUncontrolledVFRDeparture(departureAirport, arrive, ap.VFR.Randoms.Fleet, nil,
		s.currentCallsigns(), s.State.SimTime)
	return ac, err
}

func makeDepartureAircraft(ac *Aircraft, simTime Time, gateDelay time.Duration) DepartureAircraft {
	d := DepartureAircraft{
		ADSBCallsign:        ac.ADSBCallsign,
		SpawnTime:           simTime,
		ReadyDepartGateTime: simTime.Add(gateDelay),
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft and to
	// record the aircraft's initial flight path. The simulation uses calm
	// wind so that the courses measured from the paths reflect the charted
	// departure procedures: controllers judge divergence from what's
	// charted, and wind drift varying with each aircraft's spawn time and
	// speed would otherwise blur it.
	model := wx.MakeCalmModel()
	simAc := *ac
	start := ac.Position()
	const nsteps = 120
	d.MinSeparation = nsteps * time.Second // just in case
	d.AirborneDistance = -1                // not airborne within the simulation horizon
	d.LaunchPath = make([]math.Point2LL, 0, nsteps+1)
	d.LaunchPath = append(d.LaunchPath, start)
	minSepSet := false
	for i := range nsteps {
		simAc.Update(model, simTime, nil, nil, nil /* lg */)
		d.LaunchPath = append(d.LaunchPath, simAc.Position())
		if d.AirborneDistance < 0 && simAc.IsAirborne() {
			d.AirborneDistance = math.NMDistance2LL(start, simAc.Position())
			d.AirborneTime = time.Duration(i+1) * time.Second
		}
		// We need 6,000' and airborne, but we'll add a bit of slop
		if !minSepSet && simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			minSepSet = true
		}
	}

	return d
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive av.ICAOAirportCode, fleet string, routeWps []av.Waypoint,
	callsigns []av.ADSBCallsign, simTime Time) (*Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy, _, ok := s.currentVFRRunway(depart)
	if !ok {
		return nil, "", fmt.Errorf("%s: unable to find current VFR runway", depart)
	}

	ac, acType := s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, depart, arrive, callsigns, s.lg)
	if ac == nil {
		return nil, "", fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.FlightRulesVFR
	ac.Squawk = 0o1200
	if r := s.Rand.Float32(); r < .02 {
		ac.Mode = av.TransponderModeOn // mode-A
	} else if r < .03 {
		ac.Mode = av.TransponderModeStandby // flat out off
	}
	ac.InitializeFlightPlan(rules, acType, depart, arrive)

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return nil, "", fmt.Errorf("invalid aircraft type: no performance data %q", ac.FlightPlan.AircraftType)
	}

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	ac.FlightPlan.Altitude = FiledCruiseAltitude(ac.FlightPlan, perf, CruiseLimits{},
		s.State.NmPerLongitude, s.State.MagneticVariation, s.Rand)

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist := float32(s.Rand.IntRange(10, 30))
		// Bias heading to within ±90° of the departure runway heading so
		// the aircraft flies away from the airport before sightseeing,
		// rather than immediately looping back over the field.
		hdg := rwy.Heading + math.MagneticHeading(s.Rand.IntRange(-90, 90))
		v := [2]float32{dist * math.Sin(math.Radians(hdg)), dist * math.Cos(math.Radians(hdg))}
		dnm := math.LL2NM(depap.Location, s.State.NmPerLongitude)
		midnm := math.Add2f(dnm, v)
		mid = math.NM2LL(midnm, s.State.NmPerLongitude)
	}

	// This should be sufficient capacity to avoid reallocations / recopying in the following.
	wps := make([]av.Waypoint, 0, 20)

	wps = append(wps, av.Waypoint{Fix: "_dep_threshold", Location: rwy.Threshold})
	opp := math.Offset2LL(rwy.Threshold, math.MagneticToTrue(rwy.Heading, s.State.MagneticVariation), 1 /* nm */, s.State.NmPerLongitude)
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg math.TrueHeading
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp, routeWps[0].Location, s.State.NmPerLongitude)
	} else {
		hdg = math.Heading2LL(opp, mid, s.State.NmPerLongitude)
	}
	turn := math.HeadingSignedTurn(math.MagneticToTrue(rwy.Heading, s.State.MagneticVariation), hdg)
	if turn < -120 {
		// left downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, 1.5))
	} else if turn > 120 {
		// right downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, -1.5))
	}

	var randomizeAltitudeRange bool
	if len(routeWps) > 0 {
		wps = append(wps, routeWps...)
		randomizeAltitudeRange = true
	} else {
		randomizeAltitudeRange = false
		depEnd := wps[len(wps)-1].Location

		radius := .15 * dist

		airwork := func() bool {
			if depart == arrive {
				return s.Rand.Intn(3) == 0
			}
			return s.Rand.Intn(10) == 0
		}()

		const nsteps = 10
		for i := 1; i < nsteps-1; i++ { // skip first one and last one
			t := float32(i) / nsteps

			pt := func() math.Point2LL {
				if i <= nsteps/2 {
					return math.Lerp2f(2*t, depEnd, mid)
				} else {
					return math.Lerp2f(2*t-1, mid, arrap.Location)
				}
			}()

			var ar av.AltitudeRestriction
			alt := float32(ac.FlightPlan.Altitude)
			if i < nsteps/2 {
				// At or above for the first half, even if unattainable so that they climb
				ar = av.MakeAtOrAboveAltitudeRestriction(alt)
			} else {
				if i < nsteps-1 {
					// at or below to be able to start descending
					ar = av.MakeAtOrBelowAltitudeRestriction(alt)
				} else {
					// Last one--get down to the field
					ar = av.MakeRangeAltitudeRestriction(float32(arrap.Elevation)+1500, float32(arrap.Elevation)+2000)
				}
			}

			wp := av.Waypoint{
				Fix:      "_route" + strconv.Itoa(i),
				Location: pt,
			}
			wp.SetAltitudeRestriction(ar)
			wp.InitExtra().Radius = util.Select(i <= 1, 0.2*radius, radius)
			wps = append(wps, wp)

			if airwork && i == nsteps/2 {
				w := &wps[len(wps)-1]
				extra := w.InitExtra()
				extra.AirworkRadius = int8(s.Rand.IntRange(4, 8))
				extra.AirworkMinutes = int8(s.Rand.IntRange(5, 20))
				w.AltRestriction.Range[0] -= 500
				w.AltRestriction.Range[1] = min(w.AltRestriction.Range[1]+2000, maxVFRAltitude)
			}
		}
	}

	// Initialize grids if needed (must be done before adjustRouteForMVA)
	if s.bravoAirspace == nil || s.charlieAirspace == nil || s.mvaGrid == nil {
		s.initializeAirspaceGrids()
	}

	// Adjust route for MVA requirements
	wps = s.adjustRouteForMVA(string(ac.ADSBCallsign), wps)

	wps[len(wps)-1].SetSequenceVFRLanding(true)

	if err := ac.InitializeVFRDeparture(s.State.Airports[depart], wps, randomizeAltitudeRange,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, simTime, s.lg); err != nil {
		return nil, "", err
	}

	// Deep-copy only Nav (not the full Aircraft) to avoid copying
	// maps, pointers, and fields unused during route validation.
	simNav := deep.MustCopy(ac.Nav)
	simNav.Prespawn = true
	simFP := ac.FlightPlan
	prespawnWxs := s.wxModel.Lookup(simNav.FlightState.Position,
		simNav.FlightState.Altitude, simTime.Time())
	for i := range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simNav.UpdateWithWeather("", prespawnWxs, nil, &simFP,
			simTime.NavTime(), nil).PassedWaypoint; wp != nil {
			if wp.HasDeleteAction() {
				return ac, rwy.Id, nil
			}
			if wp.SequenceVFRLanding() {
				// Generate descent waypoints so prespawn validates the
				// descent from cruise altitude through any bravo/charlie
				// airspace down to pattern altitude.
				arrAP, ok := av.DB.Airports[ac.FlightPlan.ArrivalAirport]
				if !ok {
					return ac, rwy.Id, nil
				}
				patternAlt := float32(arrAP.Elevation + 1000)
				pos := simNav.FlightState.Position
				alt := simNav.FlightState.Altitude
				dest := arrAP.Location
				mid := math.Point2LL(math.Lerp2f(0.5, pos, dest))
				midAlt := (alt + patternAlt) / 2

				var descentWps []av.Waypoint
				descentWps = append(descentWps, av.Waypoint{
					Fix:      "_descent_mid",
					Location: mid,
				})
				descentWps[0].SetAltitudeRestriction(av.MakeAtAltitudeRestriction(midAlt))
				descentWps[0].SetSpeedRestriction(av.MakeAtSpeedRestriction(90))

				endWp := av.Waypoint{
					Fix:      "_descent_end",
					Location: dest,
				}
				endWp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(patternAlt))
				endWp.SetSpeedRestriction(av.MakeAtSpeedRestriction(70))
				endWp.MergeActions(av.WaypointActions{Delete: true})
				descentWps = append(descentWps, endWp)

				simNav.Waypoints = descentWps
				simNav.Heading = nav.NavHeading{}
				continue
			}
		}

		if (i % 4) != 0 {
			continue
		}
		pos := simNav.FlightState.Position
		alt := int(simNav.FlightState.Altitude)
		if s.bravoAirspace.Inside(pos, alt) ||
			s.charlieAirspace.Inside(pos, alt) ||
			s.State.FacilityAdaptation.Filters.VFRInhibit.Inside(pos, alt) {
			return nil, "", ErrViolatedAirspace
		}
		// Check MVA violation: aircraft must stay at or above MVA - 1000'.
		// Skip when within 3nm of departure airport or 5nm of arrival airport.
		distFromDeparture := math.NMDistance2LL(pos, simNav.FlightState.DepartureAirportLocation)
		distToArrival := math.NMDistance2LL(pos, simNav.FlightState.ArrivalAirportLocation)
		if distFromDeparture > 3 && distToArrival > 5 {
			if mva := s.mvaGrid.GetMVA(pos); mva > 0 && simNav.FlightState.Altitude < float32(mva-vfrMVABuffer) {
				// Find which waypoint we're heading toward
				wpIdx := -1
				var wpName string
				for j, wp := range simNav.Waypoints {
					wpIdx = j
					wpName = wp.Fix
					break
				}
				nav.NavLog(string(ac.ADSBCallsign), simTime.NavTime(), "state",
					"rejected at %.0f' (MVA %d, need %d) heading to wp %d %q, pos %v, %.1fnm from dep, %.1fnm from arr",
					simNav.FlightState.Altitude, mva, mva-vfrMVABuffer, wpIdx, wpName, pos, distFromDeparture, distToArrival)
				return nil, "", ErrVFRBelowMVA
			}
		}
	}

	//s.lg.Infof("%s: %s/%s aircraft not finished after 3 hours of sim time",		ac.ADSBCallsign, depart, arrive)

	return nil, "", ErrVFRSimTookTooLong
}

func (s *Sim) initializeAirspaceGrids() {
	initAirspace := func(a map[string][]av.AirspaceVolume) *av.AirspaceGrid {
		var vols []*av.AirspaceVolume
		for volslice := range maps.Values(a) {
			for _, v := range volslice {
				vols = append(vols, &v)
			}
		}
		return av.MakeAirspaceGrid(vols)
	}
	s.bravoAirspace = initAirspace(av.DB.BravoAirspace)
	s.charlieAirspace = initAirspace(av.DB.CharlieAirspace)
	s.mvaGrid = av.MakeMVAGrid(av.DB.MVAs[s.State.Facility])
}

// adjustRouteForMVA modifies the waypoint altitude restrictions to ensure
// the aircraft stays above MVA - vfrMVABuffer along the route.
func (s *Sim) adjustRouteForMVA(callsign string, wps []av.Waypoint) []av.Waypoint {
	if s.mvaGrid == nil || len(wps) < 2 {
		return wps
	}

	result := make([]av.Waypoint, 0, len(wps)*2)
	mvaWpNum := 0

	for i, wp := range wps {
		if i > 0 {
			// Sample between previous waypoint and this one to look for MVA transitions.
			prevWp := wps[i-1]
			dist := math.NMDistance2LL(prevWp.Location, wp.Location)
			nSamples := max(1, int(dist+0.5))

			prevMVA := s.mvaGrid.GetMVA(prevWp.Location)
			prevPos := prevWp.Location

			for j := range nSamples {
				// Sample between waypoints, not at them
				t := float32(j+1) / float32(nSamples+1)
				pos := math.Lerp2f(t, prevWp.Location, wp.Location)
				mva := s.mvaGrid.GetMVA(pos)

				if mva != prevMVA && mva > 0 && prevMVA > 0 {
					// MVA changed - insert a waypoint
					// Higher MVA: insert a new waypoint with an altitude restriction at the
					// previous sample position so that we can record "at or above" there with the
					// hopes that the aircraft will be able to reach it.
					// Lower MVA: insert at the current position to indicate that a descent may be
					// possible.
					pNew := util.Select(mva > prevMVA, prevPos, pos)

					minAlt := min(float32(mva-vfrMVABuffer), maxVFRAltitude)
					mvaWpNum++
					mvaWp := av.Waypoint{
						Fix:      fmt.Sprintf("_mva%d@%.0f", mvaWpNum, minAlt),
						Location: pNew,
					}
					mvaWp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(minAlt))
					result = append(result, mvaWp)
				}

				prevMVA = mva
				prevPos = pos
			}
		}

		// Apply MVA constraints to this waypoint and add it
		if mva := s.mvaGrid.GetMVA(wp.Location); mva > 0 {
			minAlt := min(float32(mva-vfrMVABuffer), maxVFRAltitude)
			if wp.AltitudeRestriction() == nil {
				wp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(minAlt))
			} else {
				if wp.AltRestriction.Range[0] < minAlt {
					wp.AltRestriction.Range[0] = minAlt
				}
				if wp.AltRestriction.Range[1] != av.MaxAltitude && wp.AltRestriction.Range[1] < wp.AltRestriction.Range[0] {
					wp.AltRestriction.Range[1] = wp.AltRestriction.Range[0] + 1000
				}
			}
		}
		result = append(result, wp)
	}

	return result
}
