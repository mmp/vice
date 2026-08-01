// sim/spawn_departures.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
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

func (s *Sim) spawnDepartures() {
	now := s.State.SimTime

	for airport, runways := range s.DepartureState {
		for runway, depState := range runways {
			// Possibly spawn another aircraft, depending on how much time has
			// passed since the last one.
			if now.After(depState.NextIFRSpawn) {
				ac, delay, err := s.activeTrafficProvider().createIFRDeparture(s, airport, runway)
				if err != nil {
					s.lg.Warnf("unable to create IFR departure: %v", err)
				}
				if ac != nil && err == nil {
					s.addDepartureToPool(ac, runway, false /* not manual launch */)
				}
				depState.NextIFRSpawn = now.Add(max(time.Millisecond, delay))
			}
			if now.After(depState.NextVFRSpawn) {
				if ac, err := s.makeNewVFRDeparture(airport, runway); ac != nil && err == nil {
					s.addDepartureToPool(ac, runway, false /* not manual launch */)
					depState.NextVFRSpawn = now.Add(randomWait(depState.VFRSpawnRate, false, s.Rand))
				}
			}
		}
	}
}

func (s *Sim) updateDepartureSequence() {
	now := s.State.SimTime

	for airport, runways := range s.DepartureState {
		for depRunway, depState := range runways {
			depState.filterDeleted(s.Aircraft)
			s.processGateDepartures(depState, now)
			s.processHeldDepartures(depState, now)
			s.sequenceReleasedDepartures(depState, now)
			s.launchSequencedDeparture(depState, airport, depRunway, now)
		}
	}
}

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
		} else if s.State.LaunchConfig.DepartureMode == LaunchAutomatic {
			depState.ReleasedIFR = append(depState.ReleasedIFR, depState.Gate[i])
			depState.Gate = append(depState.Gate[:i], depState.Gate[i+1:]...)
		}
		break // only one per iteration
	}
}

func (s *Sim) processHeldDepartures(depState *RunwayLaunchState, now Time) {
	for i, held := range depState.Held {
		if now.Before(held.RequestReleaseTime) {
			break // FIFO
		}

		if !held.ReleaseRequested {
			if s.prespawnUncontrolledOnly {
				// Auto-release during prespawn - aircraft will be culled
				// when it reaches DepartureContactAltitude in updateState.
				ac := s.Aircraft[held.ADSBCallsign]
				ac.Released = true
				ac.ReleaseTime = now
			}
			depState.Held[i].ReleaseRequested = true
			depState.Held[i].ReleaseDelay = s.Rand.DurationRange(20*time.Second, 120*time.Second)
		}
	}

	if len(depState.Held) > 0 && depState.Held[0].ReleaseRequested {
		dep := depState.Held[0]
		ac := s.Aircraft[dep.ADSBCallsign]
		if ac.Released && now.After(ac.ReleaseTime.Add(dep.ReleaseDelay)) {
			depState.ReleasedIFR = append(depState.ReleasedIFR, depState.Held[0])
			depState.Held = depState.Held[1:]
		}
	}
}

func (s *Sim) sequenceReleasedDepartures(depState *RunwayLaunchState, now Time) {
	wait := func(dep DepartureAircraft) time.Duration {
		ac := s.Aircraft[dep.ADSBCallsign]
		return now.Sub(ac.ReleaseTime)
	}

	// Priority: IFRs waiting > 5 minutes
	longWait := util.FilterSeq2(slices.All(depState.ReleasedIFR),
		func(idx int, dep DepartureAircraft) bool { return wait(dep) > 5*time.Minute })
	if idx, ok := util.SeqMaxIndexFunc(longWait,
		func(idx int, dep DepartureAircraft) time.Duration { return wait(dep) }); ok {
		depState.Sequenced = append(depState.Sequenced, depState.ReleasedIFR[idx])
		depState.ReleasedIFR = append(depState.ReleasedIFR[:idx], depState.ReleasedIFR[idx+1:]...)
		return
	}

	if len(depState.Sequenced) == 0 || len(depState.ReleasedIFR) > 3 {
		if len(depState.ReleasedIFR) > 0 {
			if idx, ok := util.SeqMinIndexFunc(slices.All(depState.ReleasedIFR),
				func(idx int, dep DepartureAircraft) time.Duration {
					prevDep := depState.LastDeparture
					if prevDep == nil && len(depState.Sequenced) > 0 {
						prevDep = &depState.Sequenced[len(depState.Sequenced)-1]
					}
					if prevDep == nil {
						return time.Duration(0)
					}
					return s.launchInterval(*prevDep, dep, true)
				}); !ok {
				s.lg.Errorf("No IFR found by SeqMinIndexFunc!")
			} else {
				depState.Sequenced = append(depState.Sequenced, depState.ReleasedIFR[idx])
				depState.ReleasedIFR = append(depState.ReleasedIFR[:idx], depState.ReleasedIFR[idx+1:]...)
			}
		} else if len(depState.ReleasedVFR) > 0 {
			depState.Sequenced = append(depState.Sequenced, depState.ReleasedVFR[0])
			depState.ReleasedVFR = depState.ReleasedVFR[1:]
		}
	}
}

func (s *Sim) launchSequencedDeparture(depState *RunwayLaunchState, airport string, depRunway av.RunwayID, now Time) {
	if len(depState.Sequenced) == 0 {
		return
	}

	considerExit := len(depState.Sequenced) == 1
	if !s.canLaunch(depState, depState.Sequenced[0], considerExit, airport, depRunway) {
		return
	}

	dep := depState.Sequenced[0]
	ac := s.Aircraft[dep.ADSBCallsign]

	ac.WaitingForLaunch = false
	dep.LaunchTime = now
	depState.LastDeparture = &dep
	depState.Sequenced = depState.Sequenced[1:]

	for _, state := range s.samePavementRunways(airport, depRunway) {
		state.LastDeparture = &dep
	}
}

// samePavementRunways returns an iterator over all of the runways that
// share pavement with the given depRwy: these can come both from
// user-specified "departure_runways_as_one" but also from runways with
// dotted suffixes; we want to treat 4 and 4.AutoWest as one, for example.
// Note that the iterator will return the provided runway and may return the
// same runway multiple times. Merely-intersecting runways are not included;
// they are handled geometrically in canLaunch.
func (s *Sim) samePavementRunways(airport string, depRwy av.RunwayID) iter.Seq2[av.RunwayID, *RunwayLaunchState] {
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

// canLaunch checks whether we can go ahead and launch dep.
func (s *Sim) canLaunch(depState *RunwayLaunchState, dep DepartureAircraft, considerExit bool, airport string, runway av.RunwayID) bool {
	// Check if departures are held due to a go-around
	if s.State.SimTime.Before(depState.GoAroundHoldUntil) {
		return false
	}

	// Check if enough time has passed since the last departure
	if depState.LastDeparture != nil {
		elapsed := s.State.SimTime.Sub(depState.LastDeparture.LaunchTime)
		if elapsed < s.launchInterval(*depState.LastDeparture, dep, considerExit) {
			return false
		}
	}

	// Departures from intersecting runways: the full interval is only
	// needed if both aircraft are airborne before the intersection point;
	// otherwise it's enough for the previous departure to have passed it.
	for otherRwy, otherState := range s.DepartureState[airport] {
		if otherRwy.SameRunway(runway) || otherState.LastDeparture == nil {
			continue
		}
		prev := *otherState.LastDeparture
		pt, ok := av.RunwayIntersectionPoint(airport, runway, otherRwy, s.State.NmPerLongitude, 1)
		if !ok {
			continue // doesn't intersect
		}
		if s.State.SimTime.Sub(prev.LaunchTime) >= s.launchInterval(prev, dep, considerExit) {
			continue // full separation is satisfied regardless
		}
		bothAirborne := s.airborneBeforeIntersection(prev, airport, otherRwy, pt) &&
			s.airborneBeforeIntersection(dep, airport, runway, pt)
		if bothAirborne || !s.departureHasPassedPoint(prev, airport, otherRwy, pt) {
			return false
		}
	}

	// Check if we need to wait after a recent arrival's landing to
	// simulate its deceleration and vacating the runway (though skip this
	// check if both the last arrival and the departing aircraft are VFR.)
	depAc := s.Aircraft[dep.ADSBCallsign]
	if depAc.FlightPlan.Rules == av.FlightRulesIFR || depState.LastArrivalFlightRules == av.FlightRulesIFR {
		if elapsed := s.State.SimTime.Sub(depState.LastArrivalLandingTime); elapsed <= time.Minute {
			//fmt.Printf("holding %s due to recent arrival\n", dep.ADSBCallsign)
			return false
		}
	}

	// Check for imminent arrivals on this runway
	// Skip this check if both arriving and departing aircraft are VFR
	for _, ac := range s.Aircraft {
		if ac.Nav.Approach.Assigned != nil && ac.Nav.Approach.Assigned.Runway == runway.Base() {
			// Skip if both aircraft are VFR
			if ac.FlightPlan.Rules == av.FlightRulesVFR && depAc.FlightPlan.Rules == av.FlightRulesVFR {
				continue
			}

			if dist, err := ac.Nav.DistanceToEndOfApproach(); err == nil && dist < 2.0 {
				// Hold departure; the arrival's too close
				//fmt.Printf("holding %s due to imminent arrival of %s\n", dep.ADSBCallsign, ac.ADSBCallsign)
				return false
			}
		}
	}

	// Don't launch yet if a pattern aircraft is about to land or just departed.
	if s.patternConflictsWithLaunch(airport) {
		return false
	}

	return true
}

// runwayThresholdAndDirection returns the runway's threshold and its unit
// departure direction in nm coordinates.
func runwayThresholdAndDirection(airport string, rwy av.RunwayID, nmPerLongitude float32) ([2]float32, [2]float32, bool) {
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
func (s *Sim) airborneBeforeIntersection(dep DepartureAircraft, airport string, rwy av.RunwayID, pt math.Point2LL) bool {
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
func (s *Sim) departureHasPassedPoint(dep DepartureAircraft, airport string, rwy av.RunwayID, pt math.Point2LL) bool {
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

// launchInterval returns the amount of time we must wait before launching
// cur, if prev was the last aircraft launched.
func (s *Sim) launchInterval(prev, cur DepartureAircraft, considerExit bool) time.Duration {
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

	// When sequencing, penalize same-exit repeats. But when we have a
	// sequence and are launching, we'll let it roll.
	if considerExit && cac.FlightPlan.Exit == pac.FlightPlan.Exit {
		wait = max(wait, 3*time.Minute/2)
	}

	// Check for wake turbulence separation.
	wtDist := av.CWTDirectlyBehindSeparation(pac.CWT(), cac.CWT())
	if wtDist != 0 {
		// Assume '1 gives you 3.5'
		wait = max(wait, time.Duration(wtDist/3.5*float32(time.Minute)))
	}

	return wait
}

func (s *Sim) makeNewIFRDeparture(airport string, runway av.RunwayID) (ac *Aircraft, err error) {
	depState := s.DepartureState[airport][runway]
	if len(depState.Gate) >= 10 {
		// There's a backup; hold off on more.
		return
	}

	if depState.IFRSpawnRate == 0 {
		return
	}

	if rates, ok := s.State.LaunchConfig.DepartureRates[airport][runway]; ok {
		category, rateSum := sampleRateMap(rates, s.State.LaunchConfig.DepartureRateScale, s.Rand)
		if rateSum > 0 {
			ac, err = s.createIFRDepartureNoLock(airport, runway, category)

			if ac != nil && !ac.HoldForRelease {
				ac.ReleaseTime = s.State.SimTime
			}
		}
	}

	return
}

// vfrDestinationWeight returns the weight for sampling ap as the
// destination of a random VFR departure. Airports where arrivals are
// already backed up waiting to land are excluded so that we don't keep
// adding to the pile.
func (s *Sim) vfrDestinationWeight(ap string) float32 {
	if s.orbitingArrivals(ap) > 0 {
		return 0
	}
	return s.State.Airports[ap].VFRRateSum()
}

func (s *Sim) makeNewVFRDeparture(depart string, runway av.RunwayID) (ac *Aircraft, err error) {
	depState := s.DepartureState[depart][runway]
	if len(depState.ReleasedVFR) >= 5 || len(depState.Sequenced) >= 5 {
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

		if sampledRoute != nil && s.orbitingArrivals(sampledRoute.Destination) > 0 {
			// Arrivals are backed up at the route's destination; hold off
			// on this one and try again later.
			return
		}

		for range 5 {
			depState.VFRAttempts++

			if sampledRandoms != nil {
				// Sample destination airport: may be where we started from.
				arrive, ok := rand.SampleWeightedSeq(s.Rand, maps.Keys(s.State.DepartureAirports),
					s.vfrDestinationWeight)
				if !ok {
					s.lg.Errorf("%s: unable to sample VFR destination airport???", depart)
					continue
				}
				ac, _, err = s.createUncontrolledVFRDeparture(depart, arrive, sampledRandoms.Fleet, nil, s.State.SimTime)
			} else if sampledRoute != nil {
				ac, _, err = s.createUncontrolledVFRDeparture(depart, sampledRoute.Destination, sampledRoute.Fleet,
					sampledRoute.Waypoints, s.State.SimTime)
			}

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
	rls.Sequenced = s.cullDepartures(keep, rls.Sequenced)
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
	rls.Sequenced = util.FilterSliceInPlace(rls.Sequenced, haveAc)
}

func (rls *RunwayLaunchState) setIFRRate(s *Sim, r float32) {
	if r == rls.IFRSpawnRate {
		return
	}
	rls.IFRSpawnRate = r
	rls.NextIFRSpawn = s.State.SimTime.Add(randomInitialWait(r, s.Rand))
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

func (rls RunwayLaunchState) Dump(airport string, runway av.RunwayID, now Time) {
	callsign := func(dep DepartureAircraft) string {
		return string(dep.ADSBCallsign)
	}
	fmt.Printf("%s/%s: Gate %s Held %s Released IFR %s Released VFR %s Sequence %s\n", airport, runway,
		strings.Join(util.MapSlice(rls.Gate, callsign), ", "),
		strings.Join(util.MapSlice(rls.Held, callsign), ", "),
		strings.Join(util.MapSlice(rls.ReleasedIFR, callsign), ", "),
		strings.Join(util.MapSlice(rls.ReleasedVFR, callsign), ", "),
		strings.Join(util.MapSlice(rls.Sequenced, callsign), ", "))
	if rls.IFRSpawnRate > 0 {
		fmt.Printf("    next IFR in %s, rate %f\n", rls.NextIFRSpawn.Sub(now), rls.IFRSpawnRate)
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
	ap *av.Airport, exitRoute *av.ExitRoute, departureAirport, runway string) {

	if ap.DepartureController != "" && s.isVirtualController(ap.DepartureController) {
		// Virtual controller from airport; automatically release since there's no human.
		nasFp.TrackingController = TCP(ap.DepartureController)
		nasFp.OwningTCW = s.tcwForPosition(ap.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.ControllerFrequency = ControlPosition(ap.DepartureController)
		ac.HoldForRelease = false
		return
	}

	if exitRoute.DepartureController != "" && s.isVirtualController(exitRoute.DepartureController) {
		// Virtual controller from exit route; automatically release.
		nasFp.TrackingController = TCP(exitRoute.DepartureController)
		nasFp.OwningTCW = s.tcwForPosition(exitRoute.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.ControllerFrequency = ControlPosition(exitRoute.DepartureController)
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

func (s *Sim) CreateIFRDeparture(departureAirport string, runway av.RunwayID, category string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	ac, err := s.createIFRDepartureNoLock(departureAirport, runway, category)
	if err == nil {
		s.publish()
	}
	return ac, err
}

// createIFRDepartureNoLock creates an IFR departure aircraft from the specified airport/runway.
// It validates the airport and runway, selects a random departure route, samples an
// aircraft/airline, initializes the flight plan and navigation, builds the NAS flight
// plan, assigns controller (handling virtual vs human controllers), and registers with STARS.
func (s *Sim) createIFRDepartureNoLock(departureAirport string, runway av.RunwayID, category string) (*Aircraft, error) {
	ap, rwy, exitRoutes, err := s.departureConfiguration(departureAirport, runway, category)
	if err != nil {
		return nil, err
	}

	// Sample uniformly, minding the category, if specified. The scenario's own
	// generator needs airlines to fly; a departure without them is there for
	// published traffic.
	idx := rand.SampleFiltered(s.Rand, ap.Departures,
		func(d av.Departure) bool {
			_, ok := exitRoutes[d.Exit]
			return ok && len(d.Airlines) > 0 &&
				(rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
		})
	if idx == -1 {
		return nil, fmt.Errorf("%s/%s: unable to find a valid departure", departureAirport, rwy.Runway)
	}
	dep := &ap.Departures[idx]

	ac, err := filterAndSampleAircraft(s, dep.Airlines,
		func(al av.DepartureAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.DepartureAirline) (string, string) { return departureAirport, dep.Destination },
		fmt.Sprintf("departures at %q", departureAirport))
	if err != nil {
		return nil, err
	}

	return s.initializeIFRDepartureNoLock(ac, ap, departureAirport, runway, dep, exitRoutes, "")
}

// createPublishedIFRDepartureNoLock creates a departure using the published
// identity from a timetable or from historical flight data. Vice still resolves
// the runway, exit, SID, route, altitude, and controller assignment from the
// active scenario.
// The categories are the ones the scenario is launching from this runway; the
// one used is whichever gets the aircraft closest to where it really went,
// rather than one sampled by rate. Published traffic takes its share of each
// exit from the flights themselves.
func (s *Sim) createPublishedIFRDepartureNoLock(flight av.Flight, departureAirport string,
	runway av.RunwayID, categories []string, routedDestinations map[string][]string) (*Aircraft, error) {
	callsign := strings.ToUpper(strings.TrimSpace(flight.Callsign))
	if callsign == "" {
		return nil, fmt.Errorf("published departure callsign is empty")
	}

	if av.CallsignClashesWithExisting(s.currentCallsigns(), callsign, s.EnforceUniqueCallsignSuffix) {
		return nil, fmt.Errorf("published departure callsign %s is already in use", callsign)
	}

	aircraftType := normalizeAircraftType(flight.AircraftType)
	if _, ok := av.DB.AircraftPerformance[aircraftType]; !ok {
		return nil, fmt.Errorf(
			"aircraft type %s is not present in the performance database",
			aircraftType,
		)
	}

	placement, err := s.resolvePublishedDeparture(departureAirport, runway, categories,
		flight.Other, aircraftType, routedDestinations)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", flight.Callsign, err)
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(av.FlightRulesIFR, aircraftType, departureAirport, flight.Other)

	fmt.Printf("%s: departure %s->%s runway %s exit %s (%s)\n", callsign, departureAirport,
		flight.Other, runway, placement.dep.Exit, placement.how)

	return s.initializeIFRDepartureNoLock(ac, placement.ap, departureAirport, runway, placement.dep,
		placement.exitRoutes, placement.filedRoute)
}

func (s *Sim) departureConfiguration(departureAirport string, runway av.RunwayID,
	category string) (*av.Airport, *DepartureRunway, map[av.ExitID]*av.ExitRoute, error) {
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, nil, nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r DepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]
	return ap, rwy, ap.DepartureRoutes[rwy.Runway], nil
}

// errNoScenarioRoute means the scenario doesn't plausibly work a published
// flight: nothing it models goes anywhere near where the flight really went.
// Such a flight is dropped before it spawns rather than forced through an
// unrelated gate.
var errNoScenarioRoute = errors.New("no plausible route in this scenario")

// publishedDepartureMaxHeadingDifference bounds how far off in direction a
// substituted departure route may be. A scenario that models one gate of a
// busy airport should be handed the flights that plausibly leave through it,
// not every departure from the airport.
const publishedDepartureMaxHeadingDifference = 45 // degrees

// candidateDeparture is a scenario departure a published flight could fly,
// together with the runway category configuration it came from.
type candidateDeparture struct {
	ap         *av.Airport
	rwy        *DepartureRunway
	exitRoutes map[av.ExitID]*av.ExitRoute
	dep        *av.Departure
}

// compatibleDepartures collects the scenario departures the given runway
// categories can launch: the exit must have a route off the runway and belong
// to the category. An airport that gives no "departures" at all is authored for
// published traffic only, in which case each exit off the runway stands on its
// own: the flight brings its own destination and the route database says which
// exit it really leaves through.
func (s *Sim) compatibleDepartures(departureAirport string, runway av.RunwayID,
	categories []string) []candidateDeparture {
	var candidates []candidateDeparture
	for _, category := range categories {
		ap, rwy, exitRoutes, err := s.departureConfiguration(departureAirport, runway, category)
		if err != nil {
			continue
		}

		inCategory := func(exit av.ExitID) bool {
			return rwy.Category == "" || rwy.Category == ap.ExitCategories[exit]
		}

		if len(ap.Departures) == 0 {
			exits := util.FilterSlice(util.SortedMapKeys(exitRoutes), inCategory)
			// One backing array for the whole category, so the pointers stay
			// valid as the slice below grows.
			synthesized := make([]av.Departure, len(exits))
			for i, exit := range exits {
				synthesized[i] = av.Departure{Exit: exit}
				candidates = append(candidates, candidateDeparture{ap, rwy, exitRoutes, &synthesized[i]})
			}
			continue
		}

		for i := range ap.Departures {
			dep := &ap.Departures[i]
			if _, ok := exitRoutes[dep.Exit]; !ok {
				continue
			}
			if !inCategory(dep.Exit) {
				continue
			}
			candidates = append(candidates, candidateDeparture{ap, rwy, exitRoutes, dep})
		}
	}
	return candidates
}

// departurePlacement is the exit a published flight leaves through, the route it
// files if a real one for its own city pair is what found the exit, and how the
// choice was made, for reporting.
type departurePlacement struct {
	candidateDeparture
	filedRoute string
	how        string
}

func (c candidateDeparture) placement(filedRoute, how string) departurePlacement {
	return departurePlacement{candidateDeparture: c, filedRoute: filedRoute, how: how}
}

// resolvePublishedDeparture finds the scenario departure a published flight
// flies. An exact city pair modeled by the scenario wins outright, whatever the
// category rates say. Otherwise, if the route database knows how the pair is
// really flown and one of its routes leaves through a modeled exit, the flight
// follows that exit and files the real route. Failing both, the flight goes to
// the modeled destination nearest its real one, among those in the same general
// direction; failing that, out the exit a real route to the nearest airport the
// database does cover uses, since Vero Beach has no route from JFK but Orlando
// 66nm away leaves over WAVEY. Last, for an airport that names no destinations
// because it is authored for published traffic only, out the exit lying closest
// to the direction the flight is going. If nothing is in the right direction at
// all the scenario doesn't work this flight and errNoScenarioRoute says not to
// launch it.
func (s *Sim) resolvePublishedDeparture(departureAirport string, runway av.RunwayID,
	categories []string, destination string, aircraftType string,
	routedDestinations map[string][]string) (departurePlacement, error) {
	destination = normalizeAirportCode(destination)

	candidates := s.compatibleDepartures(departureAirport, runway, categories)
	if len(candidates) == 0 {
		return departurePlacement{}, fmt.Errorf("no compatible departure route for runway %s", runway)
	}

	for _, c := range candidates {
		if c.dep.Destination != "" && normalizeAirportCode(c.dep.Destination) == destination {
			return c.placement("", "own route"), nil
		}
	}

	engineType := engineTypeFor(aircraftType)
	if c, route, ok := departureForCityPair(departureAirport, destination, engineType, candidates); ok {
		return c.placement(route, "faa route via "+c.dep.Exit.Base()), nil
	}

	// Either the pair isn't in the route database or every route it has leaves
	// through an exit this scenario doesn't model: a scenario that works one
	// corner of an airport has no reason to model the gate a filed route uses.
	// Either way, substitute the modeled departure whose destination airport is
	// closest to the flight's real one. Heading still gates plausibility: from
	// JFK, every Florida destination is within a few degrees of Ocean City's
	// heading, but a route the scenario models in some other direction entirely
	// is no way to leave, however close its destination.
	origin, originOK := av.DB.Airports[departureAirport]
	trueAirport, trueOK := av.DB.Airports[destination]
	if !originOK || !trueOK {
		return departurePlacement{}, fmt.Errorf("no exact route to %s and airport coordinates are unavailable",
			destination)
	}
	trueHeading := math.Heading2LL(origin.Location, trueAirport.Location, s.State.NmPerLongitude)

	var best candidateDeparture
	bestDistance := float32(0)
	for _, c := range candidates {
		candidateAirport, ok := av.DB.Airports[normalizeAirportCode(c.dep.Destination)]
		if !ok {
			continue
		}
		heading := math.Heading2LL(origin.Location, candidateAirport.Location, s.State.NmPerLongitude)
		if math.HeadingDifference(trueHeading, heading) > publishedDepartureMaxHeadingDifference {
			continue
		}
		distance := math.NMDistance2LL(candidateAirport.Location, trueAirport.Location)
		if best.dep == nil || distance < bestDistance {
			best, bestDistance = c, distance
		}
	}
	if best.dep != nil {
		return best.placement("", "nearest route, to "+best.dep.Destination), nil
	}

	// The scenario models nowhere near where this flight is going, but the route
	// database may still know how its neighbors are left for. The flight files
	// its own route rather than that one, which goes somewhere else.
	for _, substitute := range substituteDestinations(origin.Location, trueAirport.Location,
		destination, routedDestinations[departureAirport], s.State.NmPerLongitude) {
		if c, _, ok := departureForCityPair(departureAirport, substitute, engineType, candidates); ok {
			return c.placement("", "nearest route, to "+substitute), nil
		}
	}

	// An airport authored for published traffic only names no destinations, so
	// there is nothing to be nearest to; the exits themselves say which way each
	// one leaves, which is what the destinations stood in for.
	if c, ok := exitTowardDestination(candidates, origin.Location, trueHeading,
		s.State.NmPerLongitude); ok {
		return c.placement("", "nearest gate"), nil
	}
	return departurePlacement{}, fmt.Errorf("%w: no modeled departure heads toward %s",
		errNoScenarioRoute, destination)
}

// departureForCityPair returns the modeled exit a real route for the pair leaves
// through, together with that route.
func departureForCityPair(departureAirport, destination, engineType string,
	candidates []candidateDeparture) (candidateDeparture, string, bool) {
	routes := av.DB.RoutesBetween(departureAirport, destination)
	for _, route := range eligibleAirportPairRoutes(routes, engineType) {
		if c, ok := departureForRoute(route, candidates); ok {
			return c, route.Route, true
		}
	}
	return candidateDeparture{}, "", false
}

// substituteDestinations returns the airports that could stand in for a
// destination the route database doesn't cover, nearest to it first. They must
// lie in the same direction as the real destination and be a small enough part
// of the trip to be a neighbor of it rather than merely the closest thing along
// the way.
func substituteDestinations(origin, trueDestination math.Point2LL, destination string,
	routed []string, nmPerLongitude float32) []string {
	trueHeading := math.Heading2LL(origin, trueDestination, nmPerLongitude)
	limit := publishedSubstituteFraction * math.NMDistance2LL(origin, trueDestination)

	type candidate struct {
		id       string
		distance float32
	}
	var candidates []candidate
	for _, id := range routed {
		ap, ok := av.DB.Airports[id]
		if !ok || id == destination {
			continue
		}
		distance := math.NMDistance2LL(ap.Location, trueDestination)
		if distance > limit {
			continue
		}
		if math.HeadingDifference(math.Heading2LL(origin, ap.Location, nmPerLongitude),
			trueHeading) > publishedDepartureMaxHeadingDifference {
			continue
		}
		candidates = append(candidates, candidate{id, distance})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.distance != b.distance {
			return cmp.Compare(a.distance, b.distance)
		}
		return strings.Compare(a.id, b.id)
	})

	substitutes := make([]string, len(candidates))
	for i, c := range candidates {
		substitutes[i] = c.id
	}
	return substitutes
}

// exitTowardDestination picks the candidate whose exit fix lies closest in
// direction to where the flight is really going. Only candidates without a
// destination of their own are considered: where a scenario states one, the
// nearest-destination substitution above is the better answer.
func exitTowardDestination(candidates []candidateDeparture, airport math.Point2LL,
	trueHeading math.TrueHeading, nmPerLongitude float32) (candidateDeparture, bool) {
	var best candidateDeparture
	bestDifference := float32(0)
	for _, c := range candidates {
		if c.dep.Destination != "" {
			continue
		}
		exit, ok := av.DB.LookupWaypoint(c.dep.Exit.Base())
		if !ok {
			continue
		}
		difference := math.HeadingDifference(trueHeading,
			math.Heading2LL(airport, exit, nmPerLongitude))
		if difference > publishedDepartureMaxHeadingDifference {
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

// departureForRoute finds the compatible scenario departure whose exit the
// preferred route leaves through: the first of the route's fixes that the
// scenario models, since that is the one the flight actually goes out over. A
// route names either the exit fix or the SID that reaches it--JFK to Las Vegas
// files "KJFK DEEZZ6 CANDR J60...", where DEEZZ6 is the SID for the DEEZZ
// exit--so both count. Where the route names neither, a coded departure route's
// own departure fix is the last thing to go on.
func departureForRoute(route av.AirportPairRoute, candidates []candidateDeparture) (candidateDeparture, bool) {
	fixes := enrouteFixes(route.Route)
	if route.DepartureFix != "" {
		fixes = append(fixes, route.DepartureFix)
	}
	for _, fix := range fixes {
		if i := slices.IndexFunc(candidates, func(c candidateDeparture) bool {
			if c.dep.Exit.Base() == fix {
				return true
			}
			exitRoute, ok := c.exitRoutes[c.dep.Exit]
			return ok && exitRoute.SID == fix
		}); i != -1 {
			return candidates[i], true
		}
	}
	return candidateDeparture{}, false
}

func (s *Sim) initializeIFRDepartureNoLock(ac *Aircraft, ap *av.Airport, departureAirport string,
	runway av.RunwayID, dep *av.Departure, exitRoutes map[av.ExitID]*av.ExitRoute,
	filedRoute string) (*Aircraft, error) {
	exitRoute := exitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, string(runway), *exitRoute, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}
	if filedRoute != "" {
		// The flight files its preferred route; within the facility it still
		// flies the scenario's departure geometry.
		ac.FlightPlan.Route = filedRoute
	}

	// Departures aren't immediately associated, but the STARSComputer will
	ac.ReportDepartureHeading = exitRoutesHaveVariedHeadings(exitRoutes)
	ac.ReportDepartureSID = exitRoutesHaveVariedSIDs(exitRoutes)

	shortExit := dep.Exit.Base()
	isTRACON := av.DB.IsTRACON(s.State.Facility)
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeDeparture)
	nasFp.Route = ac.FlightPlan.Route
	if len(ac.FlightPlan.DepartureAirport) == 4 {
		nasFp.EntryFix = ac.FlightPlan.DepartureAirport[1:]
	} else {
		nasFp.EntryFix = ac.FlightPlan.DepartureAirport
	}
	nasFp.ExitFix = shortExit
	if dep.Scratchpad != "" {
		nasFp.Scratchpad = dep.Scratchpad
	} else if sp1 := s.State.FacilityAdaptation.Datablocks.Scratchpad1; sp1.DisplayExitFix ||
		sp1.DisplayExitFix1 || sp1.DisplayExitGate || sp1.DisplayAltExitGate {
		// Don't set the scratchpad; it will be set automatically.
	} else if sp, ok := s.State.FacilityAdaptation.Scratchpads[string(dep.Exit)]; ok {
		nasFp.Scratchpad = sp
	} else {
		nasFp.Scratchpad = s.State.FacilityAdaptation.Scratchpads[shortExit]
	}
	nasFp.SecondaryScratchpad = dep.SecondaryScratchpad
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude
	nasFp.AssignedAltitude = util.Select(!isTRACON, ac.FlightPlan.Altitude, 0)
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && exitRoute.IsRNAV

	ac.HoldForRelease = (ap.HoldForRelease || exitRoute.HoldForRelease) && ac.FlightPlan.Rules == av.FlightRulesIFR // VFRs aren't held
	s.assignDepartureController(ac, &nasFp, ap, exitRoute, departureAirport, string(runway))

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// departure assignment above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp)
	// A fully-contained (internal) flight whose exit fix is a local-arrival
	// airport is reclassified as an arrival for display/processing. The initial
	// owner stays the departure controller assigned above; ownership is
	// deliberately not re-derived as an arrival.
	if nasFp.LocalArrival {
		nasFp.TypeOfFlight = av.FlightTypeArrival
	}
	s.applyAutoScratchpadAssignment(&nasFp)

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	// hold on to their flight plans for now.
	// Create a flight strip for departures
	if shouldCreateFlightStrip(&nasFp) {
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

// Note that this may fail without an error if it's having trouble finding a route.
func (s *Sim) CreateVFRDeparture(departureAirport string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for range 50 {
		// Sample destination airport: may be where we started from.
		arrive, ok := rand.SampleWeightedSeq(s.Rand, maps.Keys(s.State.DepartureAirports),
			s.vfrDestinationWeight)
		if !ok {
			return nil, nil
		}
		if ap, ok := s.State.Airports[departureAirport]; !ok || ap.VFRRateSum() == 0 {
			// This shouldn't happen...
			return nil, nil
		} else {
			ac, _, err := s.createUncontrolledVFRDeparture(departureAirport, arrive, ap.VFR.Randoms.Fleet, nil, s.State.SimTime)
			if ac != nil && err == nil {
				s.publish()
			}
			return ac, err
		}
	}
	return nil, nil
}

func departureGateDelay(ac *Aircraft, trafficSource TrafficSource, r *rand.Rand) time.Duration {
	if ac.FlightPlan.Rules != av.FlightRulesIFR {
		return 0
	}

	if trafficSource == TrafficSourceHistorical {
		// Historical departures are spawned flightSpawnLead ahead of the time
		// they actually took off, so the wait at the gate is what is left of
		// that after allowing for the taxi out.
		return flightSpawnLead - flightTaxiAllowance
	}

	if trafficSource == TrafficSourceTimetable {
		// A timetable's published departure times are pushback; taxi out takes a
		// while longer.
		return r.DurationRange(10*time.Minute, 21*time.Minute)
	}

	return 5 * time.Minute
}

func makeDepartureAircraft(ac *Aircraft, simTime Time, model *wx.Model, trafficSource TrafficSource,
	r *rand.Rand) DepartureAircraft {
	d := DepartureAircraft{
		ADSBCallsign:        ac.ADSBCallsign,
		SpawnTime:           simTime,
		ReadyDepartGateTime: simTime.Add(departureGateDelay(ac, trafficSource, r)),
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft.
	simAc := *ac
	start := ac.Position()
	d.MinSeparation = 120 * time.Second // just in case
	d.AirborneDistance = -1             // not airborne within the simulation horizon
	for i := range 120 {
		simAc.Update(model, simTime, nil, nil, nil /* lg */)
		if d.AirborneDistance < 0 && simAc.IsAirborne() {
			d.AirborneDistance = math.NMDistance2LL(start, simAc.Position())
		}
		// We need 6,000' and airborne, but we'll add a bit of slop
		if simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			break
		}
	}

	return d
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive, fleet string, routeWps []av.Waypoint, simTime Time) (*Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy, _, ok := s.currentVFRRunway(depart)
	if !ok {
		return nil, "", fmt.Errorf("%s: unable to find current VFR runway", depart)
	}

	ac, acType := s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, depart, arrive, s.lg)
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

	ac.FlightPlan.Altitude = PlausibleFinalAltitude(ac.FlightPlan, perf, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.Rand)

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
			if wp.Delete() {
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
				endWp.SetDelete(true)
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
