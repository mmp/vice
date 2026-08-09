// sim/spawn_arrivals.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	gomath "math"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

const publishedArrivalMinSpawnSeparationNM = 10

var errPublishedArrivalSpawnConflict = errors.New("published arrival spawn point occupied")

// errCallsignInUse means another aircraft is already flying a published flight's
// callsign: the inbound leg of a turnaround that hasn't landed yet, most often.
// A published callsign is the real one and can't be resampled, so the flight is
// discarded rather than flown under a different one.
var errCallsignInUse = errors.New("callsign is already in use")

func (s *Sim) publishedArrivalSpawnConflict(candidate *Aircraft) bool {
	for _, existing := range s.Aircraft {
		if !existing.IsArrival() {
			continue
		}
		if math.NMDistance2LL(candidate.Position(), existing.Position()) <
			publishedArrivalMinSpawnSeparationNM {
			return true
		}
	}
	return false
}

func (s *Sim) spawnArrivalsAndOverflights() {
	now := s.State.SimTime

	if !s.NextPushStart.IsZero() && now.After(s.NextPushStart) {
		// party time
		s.PushEnd = now.Add(time.Duration(s.State.LaunchConfig.ArrivalPushLengthMinutes) * time.Minute)
		s.lg.Debug("arrival push starting", slog.Time("end_time", s.PushEnd.Time()))
		s.NextPushStart = Time{}
	}
	if !s.PushEnd.IsZero() && now.After(s.PushEnd) {
		// end push
		center := time.Duration(s.State.LaunchConfig.ArrivalPushFrequencyMinutes) * time.Minute
		s.NextPushStart = now.Add(center + s.Rand.DurationRange(-2*time.Minute, 2*time.Minute))
		s.lg.Debug("arrival push ending", slog.Time("next_start", s.NextPushStart.Time()))
		s.PushEnd = Time{}
	}

	pushActive := now.Before(s.PushEnd)

	lc := &s.State.LaunchConfig
	for group, rates := range lc.InboundFlowRates {
		// Overflights spawn on their own rate-based timer regardless of the
		// traffic source: timetables and historical data cover arrivals and
		// departures only.
		if rate := rates["overflights"]; rate > 0 && lc.OverflightMode == LaunchAutomatic &&
			now.After(s.NextOverflightSpawn[group]) {
			ac, err := s.createOverflightNoLock(group)
			if err != nil {
				s.lg.Errorf("create overflight error: %v", err)
			} else if ac != nil {
				s.addAircraftNoLock(*ac)
			}
			s.NextOverflightSpawn[group] =
				now.Add(randomWait(scaleRate(rate, lc.InboundFlowRateScale), false, s.Rand))
		}

		if lc.ArrivalMode == LaunchAutomatic && now.After(s.NextInboundSpawn[group]) {
			arrivalRates := make(map[string]float32)
			for airport, rate := range rates {
				if airport != "overflights" {
					arrivalRates[airport] = rate
				}
			}
			if len(arrivalRates) == 0 {
				// This flow only has overflights.
				s.NextInboundSpawn[group] = now.Add(idleDelay)
				continue
			}

			ac, delay, err := s.activeTrafficProvider().createInbound(s, group, arrivalRates, pushActive)

			if err != nil {
				s.lg.Errorf("create inbound error: %v", err)
			}
			if ac != nil && err == nil {
				s.addAircraftNoLock(*ac)
			}
			s.NextInboundSpawn[group] = now.Add(max(time.Millisecond, delay))
		}
	}
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	ac, err := s.createArrivalNoLock(arrivalGroup, arrivalAirport)
	if err == nil {
		s.publish()
	}
	return ac, err
}

// createArrivalNoLock creates an arrival aircraft from the specified inbound flow group.
// It selects a random arrival route to the airport, samples an aircraft/airline,
// initializes the flight plan and navigation, builds the NAS flight plan with
// controller assignments, optionally sets up a go-around, and registers with STARS.
func (s *Sim) createArrivalNoLock(group string, arrivalAirport string) (*Aircraft, error) {
	// Select a random arrival route that serves this airport. The scenario's
	// own generator needs airlines to fly; a route that only lists the airport
	// is there for published traffic.
	arrivals := s.State.InboundFlows[group].Arrivals
	idx := rand.SampleFiltered(s.Rand, arrivals, func(ar av.Arrival) bool {
		return len(ar.Airlines[arrivalAirport]) > 0
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			group, arrivalAirport)
	}
	arr := arrivals[idx]

	ac, err := filterAndSampleAircraft(s, arr.Airlines[arrivalAirport],
		func(al av.ArrivalAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.ArrivalAirline) (string, string) { return al.Airport, arrivalAirport },
		fmt.Sprintf("arrivals to %q", arrivalAirport))
	if err != nil {
		return nil, err
	}

	return s.initializeArrivalNoLock(ac, &arr, group, arrivalAirport)
}
func (s *Sim) initializeArrivalNoLock(ac *Aircraft, arr *av.Arrival, group string,
	arrivalAirport string) (*Aircraft, error) {
	err := ac.InitializeArrival(s.State.Airports[arrivalAirport], arr,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	return s.finalizeArrivalNoLock(ac, arr, group, arrivalAirport)
}

func (s *Sim) finalizeArrivalNoLock(ac *Aircraft, arr *av.Arrival, group string,
	arrivalAirport string) (*Aircraft, error) {
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeArrival)
	nasFp.Route = ac.FlightPlan.Route
	nasFp.EntryFix = ""
	nasFp.ExitFix = av.TrimICAOPrefix(ac.FlightPlan.ArrivalAirport)
	nasFp.TrackingController = arr.InitialController
	nasFp.OwningTCW = s.tcwForPosition(arr.InitialController)
	ac.ControllerFrequency = arr.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = arr.Scratchpad
	nasFp.SecondaryScratchpad = arr.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && arr.IsRNAV
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude

	// For ERAM, set AssignedAltitude and derive PerceivedAssigned from waypoint restrictions.
	if _, isERAM := av.DB.ARTCCs[s.State.Facility]; isERAM {
		spawnAlt := ac.Nav.FlightState.Altitude
		if arr.AssignedAltitude > 0 {
			nasFp.AssignedAltitude = int(arr.AssignedAltitude)
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, spawnAlt); ok {
				nasFp.PerceivedAssigned = alt
			}
		} else {
			// Try to derive from waypoint restrictions
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, spawnAlt); ok {
				nasFp.AssignedAltitude = alt
				nasFp.PerceivedAssigned = alt
			} else {
				nasFp.AssignedAltitude = int(spawnAlt)
			}
		}
	}

	// Pseudo-ERAM coordination derives the entry fix; the STARS fix-pair
	// pipeline then reassigns the pair and assigns the owning position,
	// overriding the inbound-flow default above.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, ac)
	s.applyAutoScratchpadAssignment(&nasFp)

	s.maybeSetGoAround(ac, s.State.LaunchConfig.GoAroundRate)

	// Decide at creation whether this pilot will spontaneously report field in sight and, among
	// those, whether they will also request the visual approach. VisualRequestDistance, when set,
	// gates the request to the first tick inside that distance.
	ac.WantsVisualApproach = s.Rand.Float32() < visualFieldProb
	if ac.WantsVisualApproach && s.Rand.Float32() < visualRequestProb {
		ac.VisualApproachRequestDistance = s.Rand.Float32Range(9, 16)
	}

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}
	// Create a flight strip at the inbound handoff controller if it's a human position
	if shouldCreateFlightStrip(&nasFp) &&
		!s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	return ac, s.associateAtSpawn(ac, nasFp)
}

// suitableArrivals filters the candidates to those the aircraft can fly: the
// arrival's aircraft classes and its altitudes both have to admit it.
func suitableArrivals(candidates []candidateArrival, aircraftType string) []candidateArrival {
	perf, ok := av.DB.AircraftPerformance[aircraftType]
	return util.FilterSlice(candidates, func(c candidateArrival) bool {
		if !c.arr.Aircraft.Matches(aircraftType) {
			return false
		}
		return !ok || arrivalWithinCeiling(c.arr, perf)
	})
}

// arrivalWithinCeiling reports whether the aircraft can fly the arrival's
// altitudes: the lowest one it may spawn at has to be within its ceiling. The
// arrival's cruise altitudes have no say--they fill in the filed altitude on
// the flight strip and are never flown.
func arrivalWithinCeiling(arr *av.Arrival, perf av.AircraftPerformance) bool {
	if len(arr.InitialAltitudes) > 0 {
		return float32(slices.Min(arr.InitialAltitudes)) <= perf.Ceiling
	}
	if len(arr.Waypoints) > 0 {
		if wp := arr.Waypoints[0]; wp.HasAltitudeRestriction() && wp.AltRestriction.Range[0] > perf.Ceiling {
			return false
		}
	}
	return true
}

// matchArrivalRoutes matches each route in turn against the candidates,
// returning the first one of them can fly. The first route's failure is the
// one reported: it is the preferred way the pair is flown.
func matchArrivalRoutes(candidates []candidateArrival, aircraftType string, routes []string,
	arrivalAirport, origin string) (candidateArrival, string, error) {
	var firstErr error
	for _, route := range routes {
		c, err := matchArrivalRoute(candidates, aircraftType, route, arrivalAirport, origin)
		if err == nil {
			return c, route, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = errNoPlausibleArrival
	}
	return candidateArrival{}, "", firstErr
}

// matchArrivalRoute finds the candidate arrival a filed route into the airport
// comes in on. A route that ends with a STAR belongs to the arrivals that take
// that STAR's traffic, and the CIFP transition the route joins it at says which
// of them the flight actually reaches. A route with no STAR--GA and
// terminal-en-route traffic--comes in through the gate nearest its origin.
// Suitability is judged here rather than up front so that the errors can tell
// an inactive STAR apart from active arrivals that don't admit the aircraft.
func matchArrivalRoute(candidates []candidateArrival, aircraftType, route, arrivalAirport,
	origin string) (candidateArrival, error) {
	star, entry := av.RouteSTAR(route, normalizeAirportCode(arrivalAirport))
	if star == "" {
		suitable := suitableArrivals(candidates, aircraftType)
		if len(suitable) == 0 {
			return candidateArrival{}, errNoSuitableArrival
		}
		if c, ok := nearestSpawnToOrigin(suitable, arrivalAirport, origin); ok {
			return c, nil
		}
		return candidateArrival{}, errNoPlausibleArrival
	}

	matching := util.FilterSlice(candidates, func(c candidateArrival) bool {
		return slices.ContainsFunc(c.arr.ServedSTARs(), func(s string) bool {
			return av.ProcedureBase(s) == av.ProcedureBase(star)
		})
	})
	if len(matching) == 0 {
		return candidateArrival{}, fmt.Errorf("%w %s into %s", errArrivalSTARInactive,
			star, arrivalAirport)
	}
	matching = suitableArrivals(matching, aircraftType)
	if len(matching) == 0 {
		return candidateArrival{}, fmt.Errorf("%w among those flying the %s",
			errNoSuitableArrival, star)
	}
	if len(matching) == 1 {
		return matching[0], nil
	}

	// Several arrivals fly the STAR; walking the CIFP transition the route
	// enters through says which of them the flight reaches, the one joined
	// soonest after the entry fix winning: that is the gate, while a later
	// join is a feeder it would only pass on the way in.
	cifp := av.DB.Airports[normalizeAirportCode(arrivalAirport)].STARs[star]
	if entry != "" {
		best, bestJoin := -1, 0
		for _, name := range util.SortedMapKeys(cifp.Transitions) {
			wps := cifp.Transitions[name]
			entryIndex := slices.IndexFunc(wps, func(wp av.Waypoint) bool { return wp.Fix == entry })
			if entryIndex == -1 {
				continue
			}
			for i, c := range matching {
				fixes := arrivalWaypointFixes(c.arr)
				join := slices.IndexFunc(wps[entryIndex:],
					func(wp av.Waypoint) bool { return fixes[wp.Fix] })
				if join != -1 && (best == -1 || join < bestJoin) {
					best, bestJoin = i, join
				}
			}
		}
		if best != -1 {
			return matching[best], nil
		}
	}

	// The entry fix is unknown or on no charted transition; the route's own
	// fixes are the next best evidence, the arrival matching furthest along
	// it winning: that is where the flight enters the terminal area, while an
	// earlier fix is only somewhere it passed on the way in.
	best, bestIndex := -1, -1
	fixes := enrouteFixes(route)
	for i, c := range matching {
		for fix := range arrivalWaypointFixes(c.arr) {
			if j := slices.Index(fixes, fix); j > bestIndex {
				best, bestIndex = i, j
			}
		}
	}
	if best != -1 {
		return matching[best], nil
	}

	// Nothing on the route pins it down; the gate nearest the great circle
	// from the origin is the most plausible.
	if c, ok := arrivalNearestArc(matching, arrivalAirport, origin); ok {
		return c, nil
	}
	return matching[0], nil
}

// arrivalWaypointFixes is the set of real fixes the arrival flies over.
// Waypoints synthesized during deserialization are prefixed with an underscore
// and are no part of any charted route.
func arrivalWaypointFixes(arr *av.Arrival) map[string]bool {
	fixes := make(map[string]bool)
	for _, wp := range arr.Waypoints {
		if !strings.HasPrefix(wp.Fix, "_") {
			fixes[wp.Fix] = true
		}
	}
	return fixes
}

// nearestSpawnToOrigin picks the arrival whose spawn point lies nearest the
// origin, gated by heading so the flight doesn't come in through a gate
// pointing somewhere else entirely.
func nearestSpawnToOrigin(candidates []candidateArrival, arrivalAirport,
	origin string) (candidateArrival, bool) {
	ap, apOK := av.DB.Airports[normalizeAirportCode(arrivalAirport)]
	from, fromOK := av.DB.Airports[normalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return candidateArrival{}, false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	best, bestDistance := -1, float32(0)
	for i, c := range candidates {
		if len(c.arr.Waypoints) == 0 {
			continue
		}
		spawn := c.arr.Waypoints[0].Location
		if math.HeadingDifference(math.GreatCircleHeading(ap.Location, spawn),
			toOrigin) > publishedArrivalMaxHeadingDifference {
			continue
		}
		if d := math.NMDistance2LL(spawn, from.Location); best == -1 || d < bestDistance {
			best, bestDistance = i, d
		}
	}
	if best == -1 {
		return candidateArrival{}, false
	}
	return candidates[best], true
}

// arrivalNearestArc is the last resort when no route covers the pair, foreign
// origins mostly: the gate nearest the great circle the flight actually flies,
// among those pointing plausibly toward its origin at all. With one gate
// active a bare minimum-distance pick would take any flight from anywhere.
func arrivalNearestArc(candidates []candidateArrival, arrivalAirport,
	origin string) (candidateArrival, bool) {
	ap, apOK := av.DB.Airports[normalizeAirportCode(arrivalAirport)]
	from, fromOK := av.DB.Airports[normalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return candidateArrival{}, false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	best, bestDistance := -1, float32(0)
	for i, c := range candidates {
		if len(c.arr.Waypoints) == 0 {
			continue
		}
		spawn := c.arr.Waypoints[0].Location
		if math.HeadingDifference(math.GreatCircleHeading(ap.Location, spawn),
			toOrigin) > publishedArrivalMaxHeadingDifference {
			continue
		}
		d := math.NMDistanceToSegment2LL(spawn, from.Location, ap.Location)
		if best == -1 || d < bestDistance {
			best, bestDistance = i, d
		}
	}
	if best == -1 {
		return candidateArrival{}, false
	}
	return candidates[best], true
}

// createPublishedArrivalNoLock creates an arrival using the published
// callsign, aircraft type, origin, and destination. Vice continues to resolve
// the STAR, initial controller, altitude, and spawn geometry from the scenario.
// The arrival flown and the route filed were resolved when the flight was
// queued; filedRoute is empty when the arrival's own route is flown.
func (s *Sim) createPublishedArrivalNoLock(flight av.Flight, published publishedFlight) (*Aircraft, error) {
	arrivalAirport := flight.Airport

	placement := published.placement
	inboundFlow, ok := s.State.InboundFlows[placement.group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", placement.group)
	}
	if placement.index < 0 || placement.index >= len(inboundFlow.Arrivals) {
		return nil, fmt.Errorf("%s: no arrival route in %s", flight.Callsign, placement.group)
	}
	arr := &inboundFlow.Arrivals[placement.index]

	callsign := strings.ToUpper(strings.TrimSpace(flight.Callsign))
	if callsign == "" {
		return nil, fmt.Errorf("published arrival callsign is empty")
	}
	if av.CallsignClashesWithExisting(
		s.currentCallsigns(),
		callsign,
		s.EnforceUniqueCallsignSuffix,
	) {
		return nil, fmt.Errorf("published arrival %s: %w", callsign, errCallsignInUse)
	}

	if _, ok := av.DB.AircraftPerformance[flight.AircraftType]; !ok {
		return nil, fmt.Errorf(
			"aircraft type %s is not present in the performance database",
			flight.AircraftType,
		)
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	// The flight plan keeps the real origin even when another airport's route
	// is being flown.
	ac.InitializeFlightPlan(
		av.FlightRulesIFR,
		flight.AircraftType,
		normalizeAirportCode(flight.Other),
		normalizeAirportCode(flight.Airport),
	)

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], arr,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}
	if s.publishedArrivalSpawnConflict(ac) {
		return nil, errPublishedArrivalSpawnConflict
	}
	if placement.filedRoute != "" {
		// The flight files the route the pair is really flown on; within the
		// facility it still flies the scenario's arrival geometry.
		ac.FlightPlan.Route = placement.filedRoute
	}

	s.log("%s: arrival %s->%s via %s %s (%s)", callsign, flight.Other, arrivalAirport,
		placement.group, util.Select(arr.STAR == "", arr.FlightStripDisplayRoute, arr.STAR), placement.how)

	return s.finalizeArrivalNoLock(ac, arr, placement.group, arrivalAirport)
}

func (s *Sim) currentCallsigns() []av.ADSBCallsign {
	callsigns := slices.Collect(maps.Keys(s.Aircraft))
	for _, fp := range s.STARSComputer.FlightPlans {
		callsigns = append(callsigns, av.ADSBCallsign(fp.ACID))
	}
	return callsigns
}

func (s *Sim) sampleAircraft(al av.AirlineSpecifier, departureAirport, arrivalAirport string, lg *log.Logger) (*Aircraft, string) {
	// Collect all currently in-use or soon-to-be in-use callsigns.
	callsigns := s.currentCallsigns()

	actype, callsign := al.SampleAcTypeAndCallsign(s.Rand, callsigns, s.EnforceUniqueCallsignSuffix, departureAirport, arrivalAirport, lg)

	if actype == "" {
		return nil, ""
	}

	return &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}, actype
}
func (s *Sim) sampleAircraftWithAirlineCallsign(al av.AirlineSpecifier, departureAirport, arrivalAirport string, lg *log.Logger) (*Aircraft, string) {
	callsign := strings.ToUpper(strings.TrimSpace(al.Callsign))
	if callsign == "" {
		return nil, ""
	}

	callsigns := s.currentCallsigns()
	if av.CallsignClashesWithExisting(callsigns, callsign, s.EnforceUniqueCallsignSuffix) {
		return nil, ""
	}

	actype := al.SampleAcType(s.Rand, departureAirport, arrivalAirport, lg)
	if actype == "" {
		return nil, ""
	}

	return &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}, actype
}

// Generic function to sample an aicraft given the callsigns given and current callsigns
func filterAndSampleAircraft[T any](s *Sim, airlines []T, specifier func(T) av.AirlineSpecifier,
	airports func(T) (string, string), errContext string) (*Aircraft, error) {
	callsigns := s.currentCallsigns()
	available := make([]T, 0, len(airlines))
	for _, al := range airlines {
		spec := specifier(al)
		if spec.Callsign == "" || !av.CallsignClashesWithExisting(callsigns, spec.Callsign, s.EnforceUniqueCallsignSuffix) {
			available = append(available, al)
		}
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("unable to sample a valid aircraft for %s", errContext)
	}

	airline := rand.SampleSlice(s.Rand, available)
	spec := specifier(airline)
	dep, arr := airports(airline)
	var ac *Aircraft
	var acType string
	if spec.Callsign != "" {
		ac, acType = s.sampleAircraftWithAirlineCallsign(spec, dep, arr, s.lg)
	} else {
		ac, acType = s.sampleAircraft(spec, dep, arr, s.lg)
	}
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft for %s", errContext)
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, dep, arr)
	return ac, nil
}

// assignSquawk allocates an enroute squawk code and assigns it to both the
// aircraft and NAS flight plan.
func (s *Sim) assignSquawk(ac *Aircraft, nasFp *NASFlightPlan) error {
	sq, err := s.ERAMComputer.CreateSquawk()
	if err != nil {
		return err
	}
	ac.Squawk = sq
	nasFp.AssignedSquawk = sq
	return nil
}

// initNASFlightPlan creates a NASFlightPlan with common fields pre-populated.
// Callers must set type-specific fields (EntryFix, ExitFix, controller
// assignments, scratchpads, altitudes, etc.) after calling this function.
func (s *Sim) initNASFlightPlan(ac *Aircraft, flightType av.TypeOfFlight) NASFlightPlan {
	return NASFlightPlan{
		ACID:             ACID(ac.ADSBCallsign),
		ArrivalAirport:   ac.FlightPlan.ArrivalAirport,
		CoordinationTime: getAircraftTime(s.State.SimTime, s.Rand),
		PlanType:         RemoteEnroute,
		Rules:            av.FlightRulesIFR,
		TypeOfFlight:     flightType,
		AircraftCount:    1,
		AircraftType:     ac.FlightPlan.AircraftType,
		CWTCategory:      av.DB.AircraftPerformance[ac.FlightPlan.AircraftType].Category.CWT,
	}
}

// findLowestWaypointAltitude finds the lowest altitude restriction target from
// the waypoints, used to set PerceivedAssigned altitude for ERAM facilities.
// Returns the altitude and true if found, or 0 and false if no restrictions exist.
func findLowestWaypointAltitude(wps av.WaypointArray, initialAlt float32) (int, bool) {
	lowestAlt := gomath.MaxInt
	for _, wp := range wps {
		if wp.AltitudeRestriction() == nil {
			continue
		}
		if target := int(wp.AltitudeRestriction().TargetAltitude(initialAlt)); target < lowestAlt {
			lowestAlt = target
		}
	}
	if lowestAlt == gomath.MaxInt {
		return 0, false
	}
	return lowestAlt, true
}

// maybeSetGoAround determines if an arrival should attempt a go-around and
// sets the GoAroundDistance if so. Go-arounds only occur for IFR aircraft
// that will be handed off to a human controller (checked via HumanHandoff
// waypoint), subject to the configured GoAroundRate probability.
func (s *Sim) maybeSetGoAround(ac *Aircraft, goAroundRate float32) {
	if ac.FlightPlan.Rules != av.FlightRulesIFR {
		return // VFRs don't go around since they aren't talking to us
	}
	if s.Rand.Float32() >= goAroundRate {
		return // Random chance didn't trigger
	}
	// Only allow go-around if there's human controller involvement
	if !slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff() }) {
		return
	}
	d := s.Rand.Float32Range(0.1, 0.7)
	ac.GoAroundDistance = &d
}

func (s *Sim) CreateOverflight(group string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	ac, err := s.createOverflightNoLock(group)
	if err == nil {
		s.publish()
	}
	return ac, err
}

// createOverflightNoLock creates an overflight aircraft from the specified inbound flow group.
// It selects a random overflight route, samples an aircraft/airline, initializes the
// flight plan and navigation, builds the NAS flight plan with controller assignments,
// and registers with STARS.
func (s *Sim) createOverflightNoLock(group string) (*Aircraft, error) {
	// Select a random overflight from the group
	overflights := s.State.InboundFlows[group].Overflights
	of := rand.SampleSlice(s.Rand, overflights)

	if len(of.Airlines) == 0 {
		return nil, fmt.Errorf("no airlines for overflights in %q", group)
	}

	ac, err := filterAndSampleAircraft(s, of.Airlines,
		func(al av.OverflightAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.OverflightAirline) (string, string) { return al.DepartureAirport, al.ArrivalAirport },
		fmt.Sprintf("overflight in %q", group))
	if err != nil {
		return nil, err
	}

	if err := ac.InitializeOverflight(&of, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}

	isTRACON := av.DB.IsTRACON(s.State.Facility)
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeOverflight)
	nasFp.Route = ac.FlightPlan.Route
	nasFp.EntryFix = "" // TODO
	nasFp.ExitFix = ""  // TODO
	nasFp.TrackingController = of.InitialController
	nasFp.OwningTCW = s.tcwForPosition(of.InitialController)
	ac.ControllerFrequency = of.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = of.Scratchpad
	nasFp.SecondaryScratchpad = of.SecondaryScratchpad
	nasFp.AssignedAltitude = util.Select(!isTRACON, int(of.AssignedAltitude), 0)
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && of.IsRNAV
	nasFp.TypeOfFlight = of.TypeOfFlight

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// inbound-flow default above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, ac)
	s.applyAutoScratchpadAssignment(&nasFp)

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Create a flight strip at the inbound handoff controller if it's a human position
	if shouldCreateFlightStrip(&nasFp) && !s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	return ac, s.associateAtSpawn(ac, nasFp)
}

// associateAtSpawn registers nasFp with the STARS computer and, if the
// tracking controller is virtual, immediately associates it with ac so it
// never appears in UnassociatedFlightPlans / the STARS FLIGHT PLAN list.
// External-facility-owned flight plans stay unassociated until the handoff
// into the facility completes.
func (s *Sim) associateAtSpawn(ac *Aircraft, nasFp NASFlightPlan) error {
	created, err := s.STARSComputer.CreateFlightPlan(nasFp)
	if err != nil {
		return err
	}
	if !s.isVirtualController(created.TrackingController) {
		return nil
	}
	fp := s.STARSComputer.takeFlightPlanByACID(created.ACID)
	if fp == nil {
		return nil
	}
	if s.State.IsLocalController(fp.TrackingController) {
		fp.LastLocalController = fp.TrackingController
	}
	ac.AssociateFlightPlan(fp)
	s.eventStream.Post(Event{
		Type: FlightPlanAssociatedEvent,
		ACID: fp.ACID,
	})
	return nil
}
