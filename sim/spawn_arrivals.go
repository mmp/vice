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
	if len(ac.FlightPlan.ArrivalAirport) == 4 {
		nasFp.ExitFix = ac.FlightPlan.ArrivalAirport[1:]
	} else {
		nasFp.ExitFix = ac.FlightPlan.ArrivalAirport
	}
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

// arrivalListingOrigin returns the arrival that names origin among the airlines
// it lands at arrivalAirport: the scenario says in so many words that this is
// how traffic from there comes in.
func arrivalListingOrigin(candidates []candidateArrival, arrivalAirport,
	origin string) (candidateArrival, bool) {
	arrivalAirport = normalizeAirportCode(arrivalAirport)
	origin = normalizeAirportCode(origin)

	for _, c := range candidates {
		for _, airline := range c.arr.Airlines[arrivalAirport] {
			if normalizeAirportCode(airline.Airport) == origin {
				return c, true
			}
		}
	}
	return candidateArrival{}, false
}

// arrivalForCityPair returns the arrival a real route for the city pair comes in
// on, together with that route so the flight can file it. The route database
// says how a pair is really flown--ORF to JFK arrives on the CAMRN5--so a
// scenario that works the STAR works the flight whether or not it happens to
// list Norfolk among its origins. The routes are tried in the order the pair is
// really flown, and within one the arrival matching furthest along it wins:
// that is where the flight enters the terminal area, while an earlier fix is
// only somewhere it passed on the way in.
func arrivalForCityPair(candidates []candidateArrival, arrivalAirport, origin,
	aircraftType string) (candidateArrival, string, bool) {
	arrivalAirport = normalizeAirportCode(arrivalAirport)
	origin = normalizeAirportCode(origin)

	routes := av.DB.RoutesBetween(origin, arrivalAirport)
	for _, route := range eligibleAirportPairRoutes(routes, engineTypeFor(aircraftType)) {
		fixes := enrouteFixes(route.Route)
		best, bestIndex := candidateArrival{}, -1
		for _, c := range candidates {
			for _, fix := range arrivalFixes(c.arr) {
				if i := slices.Index(fixes, fix); i > bestIndex {
					best, bestIndex = c, i
				}
			}
		}
		if bestIndex != -1 {
			return best, route.Route, true
		}
	}
	return candidateArrival{}, "", false
}

// arrivalFixes returns the points a real route would name if it came in on this
// arrival: the STAR it flies and the fixes it actually flies over. The arrival's
// "route" is not among them--that is the string shown on the flight strip rather
// than the route flown, and it ends at the airport, which every route into the
// airport also does.
func arrivalFixes(arr *av.Arrival) []string {
	var fixes []string
	// An arrival names its STAR only when it takes its waypoints from one;
	// otherwise the STAR its waypoints run along was worked out at load.
	if star := util.Select(arr.STAR != "", arr.STAR, arr.DerivedSTAR); star != "" {
		fixes = append(fixes, star)
	}
	for _, wp := range arr.Waypoints {
		// Waypoints synthesized during deserialization are prefixed with an
		// underscore and are no part of any filed route.
		if !strings.HasPrefix(wp.Fix, "_") {
			fixes = append(fixes, wp.Fix)
		}
	}
	return fixes
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
