// sim/spawn_departures.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

func (s *Sim) spawnDepartures() {
	now := s.State.SimTime

	for airport, runways := range s.DepartureState {
		for runway, depState := range runways {
			// Possibly spawn another aircraft, depending on how much time has
			// passed since the last one.
			if now.After(depState.NextIFRSpawn) {
				if ac, err := s.makeNewIFRDeparture(airport, runway); ac != nil && err == nil {
					s.addDepartureToPool(ac, runway, false /* not manual launch */)
					depState.NextIFRSpawn = now.Add(randomWait(depState.IFRSpawnRate, false, s.Rand))
				}
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

func (s *Sim) processGateDepartures(depState *RunwayLaunchState, now time.Time) {
	for i, dep := range depState.Gate {
		if now.Before(dep.ReadyDepartGateTime) {
			continue
		}

		ac := s.Aircraft[dep.ADSBCallsign]
		if ac.HoldForRelease {
			depState.Gate[i].RequestReleaseTime = now.Add(time.Duration(60+s.Rand.Intn(60)) * time.Second)
			s.STARSComputer.AddHeldDeparture(ac)
			depState.Held = append(depState.Held, depState.Gate[i])
			depState.Gate = append(depState.Gate[:i], depState.Gate[i+1:]...)
		} else if s.State.LaunchConfig.Mode == LaunchAutomatic {
			depState.ReleasedIFR = append(depState.ReleasedIFR, depState.Gate[i])
			depState.Gate = append(depState.Gate[:i], depState.Gate[i+1:]...)
		}
		break // only one per iteration
	}
}

func (s *Sim) processHeldDepartures(depState *RunwayLaunchState, now time.Time) {
	for i, held := range depState.Held {
		if now.Before(held.RequestReleaseTime) {
			break // FIFO
		}

		if !held.ReleaseRequested {
			if s.prespawnUncontrolledOnly {
				s.deleteAircraft(s.Aircraft[held.ADSBCallsign])
				depState.Held = append(depState.Held[:i], depState.Held[i+1:]...)
				return
			}
			depState.Held[i].ReleaseRequested = true
			depState.Held[i].ReleaseDelay = time.Duration(20+s.Rand.Intn(100)) * time.Second
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

func (s *Sim) sequenceReleasedDepartures(depState *RunwayLaunchState, now time.Time) {
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

func (s *Sim) launchSequencedDeparture(depState *RunwayLaunchState, airport, depRunway string, now time.Time) {
	if len(depState.Sequenced) == 0 {
		return
	}

	considerExit := len(depState.Sequenced) == 1
	if !s.canLaunch(depState, depState.Sequenced[0], considerExit, depRunway) {
		return
	}

	dep := depState.Sequenced[0]
	ac := s.Aircraft[dep.ADSBCallsign]

	if s.prespawnUncontrolledOnly && !s.initialControllerIsVirtual(ac) {
		depState.Sequenced = depState.Sequenced[1:]
		s.deleteAircraft(ac)
		return
	}

	ac.WaitingForLaunch = false
	dep.LaunchTime = now
	depState.LastDeparture = &dep
	depState.Sequenced = depState.Sequenced[1:]

	for _, state := range s.sameGroupRunways(airport, depRunway) {
		state.LastDeparture = &dep
	}
}

// intersectingRunways returns all runways that physically intersect the
// given runway up to one nm past the opposite threshold.
func (s *Sim) intersectingRunways(airport, depRwy string) []string {
	depRwy = av.TidyRunway(depRwy)

	// Get the departure runway info
	rwySegment := func(rwy string) (seg [2][2]float32, ok bool) {
		var r, opp av.Runway
		if r, ok = av.LookupRunway(airport, rwy); !ok {
			return
		}

		if opp, ok = av.LookupOppositeRunway(airport, rwy); !ok {
			return
		}

		// Line segment from departure threshold to 1nm past opposing threshold
		rp := math.LL2NM(r.Threshold, s.State.NmPerLongitude)
		op := math.LL2NM(opp.Threshold, s.State.NmPerLongitude)
		v := math.Normalize2f(math.Sub2f(op, rp))

		return [2][2]float32{rp, math.Add2f(op, v)}, true
	}

	depSeg, ok := rwySegment(depRwy)
	if !ok {
		return nil
	}

	// Check all other runways for intersection
	var intersecting []string
	if _, ok := s.State.Airports[airport]; ok {
		for _, otherRwy := range av.DB.Airports[airport].Runways {
			if av.TidyRunway(otherRwy.Id) == depRwy {
				continue // Skip the same runway
			}

			if othSeg, ok := rwySegment(otherRwy.Id); ok {
				if _, ok := math.SegmentSegmentIntersect(depSeg[0], depSeg[1], othSeg[0], othSeg[1]); ok {
					intersecting = append(intersecting, otherRwy.Id)
				}
			}
		}
	}

	return intersecting
}

// sameGroupRunways returns an iterator over all of the runways in the
// ~equivalence class with the given depRwy. Such equivalences can come
// both from user-specified "departure_runways_as_one" but also from
// runways with dotted suffixes; we want to treat 4 and 4.AutoWest as one,
// for example.  Note that the iterator will return the provided runway and
// may return the same runway multiple times.
func (s *Sim) sameGroupRunways(airport, depRwy string) iter.Seq2[string, *RunwayLaunchState] {
	depRwy = av.TidyRunway(depRwy)
	runwayState := s.DepartureState[airport]
	return func(yield func(string, *RunwayLaunchState) bool) {
		// First look at departure runways as one
		for _, group := range s.State.Airports[airport].DepartureRunwaysAsOne {
			groupRwys := strings.Split(group, ",")
			if slices.Contains(groupRwys, depRwy) {
				for rwy, state := range runwayState {
					if slices.Contains(groupRwys, av.TidyRunway(rwy)) {
						if !yield(rwy, state) {
							return
						}
					}
				}
				break
			}
		}

		// Also include intersecting runways.
		for _, intRwy := range s.intersectingRunways(airport, depRwy) {
			// We can't directly look up in the runwayState map due to runways like 28L.1
			// but instead have to check each one for a match.
			for rwy, state := range runwayState {
				if av.TidyRunway(intRwy) == av.TidyRunway(rwy) {
					if !yield(intRwy, state) {
						return
					}
				}
			}
		}

		// Now look for departing both e.g. "4" and "4.AutoWest"
		for rwy, state := range runwayState {
			if depRwy == av.TidyRunway(rwy) {
				if !yield(rwy, state) {
					return
				}
			}
		}
	}
}

// canLaunch checks whether we can go ahead and launch dep.
func (s *Sim) canLaunch(depState *RunwayLaunchState, dep DepartureAircraft, considerExit bool, runway string) bool {
	// Check if enough time has passed since the last departure
	if depState.LastDeparture != nil {
		elapsed := s.State.SimTime.Sub(depState.LastDeparture.LaunchTime)
		if elapsed < s.launchInterval(*depState.LastDeparture, dep, considerExit) {
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
		if ac.Nav.Approach.Assigned != nil && ac.Nav.Approach.Assigned.Runway == runway {
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

	return true
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

func (s *Sim) makeNewIFRDeparture(airport, runway string) (ac *Aircraft, err error) {
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

func (s *Sim) makeNewVFRDeparture(depart, runway string) (ac *Aircraft, err error) {
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
		rateSum := 0
		var sampledRandoms *av.VFRRandomsSpec
		var sampledRoute *av.VFRRouteSpec
		if ap.VFR.Randoms.Rate > 0 {
			rateSum = ap.VFR.Randoms.Rate
			sampledRandoms = &ap.VFR.Randoms
		}
		for _, route := range ap.VFR.Routes {
			if route.Rate > 0 {
				rateSum += route.Rate
				p := float32(route.Rate) / float32(rateSum)
				if s.Rand.Float32() < p {
					sampledRandoms = nil
					sampledRoute = &route
				}
			}
		}

		for range 5 {
			depState.VFRAttempts++

			if sampledRandoms != nil {
				// Sample destination airport: may be where we started from.
				arrive, ok := rand.SampleWeightedSeq(s.Rand, maps.Keys(s.State.DepartureAirports),
					func(ap string) int { return s.State.Airports[ap].VFRRateSum() })
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

func (rls RunwayLaunchState) Dump(airport string, runway string, now time.Time) {
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
		nasFp.ControllingController = TCP(ap.DepartureController)
		nasFp.OwningTCW = s.State.TCWForPosition(ap.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.HoldForRelease = false
		return
	}

	if exitRoute.DepartureController != "" && s.isVirtualController(exitRoute.DepartureController) {
		// Virtual controller from exit route; automatically release.
		nasFp.TrackingController = TCP(exitRoute.DepartureController)
		nasFp.ControllingController = TCP(exitRoute.DepartureController)
		nasFp.OwningTCW = s.State.TCWForPosition(exitRoute.DepartureController)
		nasFp.InboundHandoffController = TCP(exitRoute.HandoffController)
		ac.HoldForRelease = false
		return
	}

	// Human controller will be first
	pos := s.ScenarioRootPosition()
	if tcp := s.GetDepartureController(departureAirport, runway, exitRoute.SID); tcp != "" {
		pos = tcp
	}

	// Set altitude at which aircraft will contact departure control
	ac.DepartureContactAltitude = ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(s.Rand.Intn(500))
	ac.DepartureContactAltitude = min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))

	nasFp.TrackingController = pos
	nasFp.OwningTCW = s.State.TCWForPosition(pos)
	nasFp.InboundHandoffController = pos
}

func (s *Sim) CreateIFRDeparture(departureAirport, runway, category string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createIFRDepartureNoLock(departureAirport, runway, category)
}

// createIFRDepartureNoLock creates an IFR departure aircraft from the specified airport/runway.
// It validates the airport and runway, selects a random departure route, samples an
// aircraft/airline, initializes the flight plan and navigation, builds the NAS flight
// plan, assigns controller (handling virtual vs human controllers), and registers with STARS.
func (s *Sim) createIFRDepartureNoLock(departureAirport, runway, category string) (*Aircraft, error) {
	// Validate airport exists
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, av.ErrUnknownAirport
	}

	// Find the runway configuration
	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r DepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]

	exitRoutes := ap.DepartureRoutes[rwy.Runway]

	// Sample uniformly, minding the category, if specified
	idx = rand.SampleFiltered(s.Rand, ap.Departures,
		func(d av.Departure) bool {
			_, ok := exitRoutes[d.Exit] // make sure the runway handles the exit
			return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
		})
	if idx == -1 {
		// This shouldn't ever happen...
		return nil, fmt.Errorf("%s/%s: unable to find a valid departure", departureAirport, rwy.Runway)
	}
	dep := &ap.Departures[idx]

	airline := rand.SampleSlice(s.Rand, dep.Airlines)
	ac, acType := s.sampleAircraft(airline.AirlineSpecifier, departureAirport, dep.Destination, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft for airline %+v at %q", airline,
			departureAirport)
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, departureAirport, dep.Destination)

	exitRoute := exitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, runway, *exitRoute, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	shortExit, _, _ := strings.Cut(dep.Exit, ".") // chop any excess
	_, isTRACON := av.DB.TRACONs[s.State.Facility]
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeDeparture)
	nasFp.EntryFix = util.Select(len(ac.FlightPlan.DepartureAirport) == 4, ac.FlightPlan.DepartureAirport[1:],
		ac.FlightPlan.DepartureAirport)
	nasFp.ExitFix = shortExit
	nasFp.Scratchpad = util.Select(dep.Scratchpad != "", dep.Scratchpad, s.State.FacilityAdaptation.Scratchpads[dep.Exit])
	nasFp.SecondaryScratchpad = dep.SecondaryScratchpad
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude
	nasFp.AssignedAltitude = util.Select(!isTRACON, ac.FlightPlan.Altitude, 0)

	ac.HoldForRelease = ap.HoldForRelease && ac.FlightPlan.Rules == av.FlightRulesIFR // VFRs aren't held
	s.assignDepartureController(ac, &nasFp, ap, exitRoute, departureAirport, runway)

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	// hold on to their flight plans for now.
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
			func(ap string) int { return s.State.Airports[ap].VFRRateSum() })
		if !ok {
			return nil, nil
		}
		if ap, ok := s.State.Airports[departureAirport]; !ok || ap.VFRRateSum() == 0 {
			// This shouldn't happen...
			return nil, nil
		} else {
			ac, _, err := s.createUncontrolledVFRDeparture(departureAirport, arrive, ap.VFR.Randoms.Fleet, nil, s.State.SimTime)
			return ac, err
		}
	}
	return nil, nil
}

func makeDepartureAircraft(ac *Aircraft, simTime time.Time, model *wx.Model, r *rand.Rand) DepartureAircraft {
	d := DepartureAircraft{
		ADSBCallsign:        ac.ADSBCallsign,
		SpawnTime:           simTime,
		ReadyDepartGateTime: simTime.Add(5 * time.Minute),
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft.
	simAc := *ac
	start := ac.Position()
	d.MinSeparation = 120 * time.Second // just in case
	for i := range 120 {
		simAc.Update(model, simTime, nil, nil /* lg */)
		// We need 6,000' and airborne, but we'll add a bit of slop
		if simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			break
		}
	}

	return d
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive, fleet string, routeWps []av.Waypoint, simTime time.Time) (*Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy := s.State.VFRRunways[depart]

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
	// This doesnt need an 11 altitude

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return nil, "", fmt.Errorf("invalid aircraft type: no performance data %q", ac.FlightPlan.AircraftType)
	}

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	ac.FlightPlan.Altitude = PlausibleFinalAltitude(ac.FlightPlan, perf, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.Rand)

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist := float32(10 + s.Rand.Intn(20))
		hdg := float32(1 + s.Rand.Intn(360))
		v := [2]float32{dist * math.Sin(math.Radians(hdg)), dist * math.Cos(math.Radians(hdg))}
		dnm := math.LL2NM(depap.Location, s.State.NmPerLongitude)
		midnm := math.Add2f(dnm, v)
		mid = math.NM2LL(midnm, s.State.NmPerLongitude)
	}

	// This should be sufficient capacity to avoid reallocations / recopying in the following.
	wps := make([]av.Waypoint, 0, 20)

	wps = append(wps, av.Waypoint{Fix: "_dep_threshold", Location: rwy.Threshold})
	opp := math.Offset2LL(rwy.Threshold, rwy.Heading, 1 /* nm */, s.State.NmPerLongitude,
		s.State.MagneticVariation)
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg float32
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp, routeWps[0].Location, s.State.NmPerLongitude, s.State.MagneticVariation)
	} else {
		hdg = math.Heading2LL(opp, mid, s.State.NmPerLongitude, s.State.MagneticVariation)
	}
	turn := math.HeadingSignedTurn(rwy.Heading, hdg)
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

			// At for the first half, even if unattainable so that they climb
			var ar *av.AltitudeRestriction
			alt := float32(ac.FlightPlan.Altitude)
			if i < nsteps/2 {
				ar = &av.AltitudeRestriction{Range: [2]float32{alt, alt}}
			} else {
				if i < nsteps-1 {
					// at or below to be able to start descending
					ar = &av.AltitudeRestriction{Range: [2]float32{0, alt}}
				} else {
					// Last one--get down to the field
					ar = &av.AltitudeRestriction{
						Range: [2]float32{float32(arrap.Elevation) + 1500, float32(arrap.Elevation) + 2000}}
				}
			}

			wps = append(wps, av.Waypoint{
				Fix:                 "_route" + strconv.Itoa(i),
				Location:            pt,
				AltitudeRestriction: ar,
				Radius:              util.Select(i <= 1, 0.2*radius, radius),
			})

			if airwork && i == nsteps/2 {
				wps[len(wps)-1].AirworkRadius = 4 + s.Rand.Intn(4)
				wps[len(wps)-1].AirworkMinutes = 5 + s.Rand.Intn(15)
				wps[len(wps)-1].AltitudeRestriction.Range[0] -= 500
				wps[len(wps)-1].AltitudeRestriction.Range[1] += 2000
			}
		}
	}

	wps[len(wps)-1].Land = true

	if err := ac.InitializeVFRDeparture(s.State.Airports[depart], wps, randomizeAltitudeRange,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, simTime, s.lg); err != nil {
		return nil, "", err
	}

	if s.bravoAirspace == nil || s.charlieAirspace == nil {
		s.initializeAirspaceGrids()
	}

	// Check airspace violations
	simac := deep.MustCopy(*ac)
	for range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simac.Update(s.wxModel, simTime, s.bravoAirspace, nil); wp != nil && wp.Delete {
			return ac, rwy.Id, nil
		}
		if s.bravoAirspace.Inside(simac.Position(), int(simac.Altitude())) ||
			s.charlieAirspace.Inside(simac.Position(), int(simac.Altitude())) ||
			s.State.FacilityAdaptation.Filters.VFRInhibit.Inside(simac.Position(), int(simac.Altitude())) {
			return nil, "", ErrViolatedAirspace
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
}
