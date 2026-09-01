// sim/spawn_arrivals.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"maps"
	gomath "math"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
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
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	ac.maybeSetGoAround(s.State.LaunchConfig.GoAroundRate, s.Rand)

	// Decide at creation whether this pilot will spontaneously report field in sight and, among
	// those, whether they will also request the visual approach. VisualRequestDistance, when set,
	// gates the request to the first tick inside that distance.
	ac.WantsVisualApproach = s.Rand.Float32() < visualFieldProb
	if ac.WantsVisualApproach && s.Rand.Float32() < visualRequestProb {
		ac.VisualApproachRequestDistance = s.Rand.Float32Range(9, 16)
	}

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp); err != nil {
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

// createScheduledArrival creates the arrival a schedule entry describes, for
// both scenario and published entries. Vice resolves the STAR, initial
// controller, altitude, and spawn geometry from the scenario; the flow and
// arrival index were resolved when the entry was generated. All resource
// allocation--squawk, flight strip, flight plan, list index--happens here.
func (s *Sim) createScheduledArrival(e ScheduledArrival) (*Aircraft, error) {
	inboundFlow, ok := s.State.InboundFlows[e.Group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", e.Group)
	}
	if e.Index < 0 || e.Index >= len(inboundFlow.Arrivals) {
		return nil, fmt.Errorf("%s: no arrival route in %s", e.Callsign, e.Group)
	}
	arr := &inboundFlow.Arrivals[e.Index]

	published := e.Source != TrafficSourceScenario
	callsign, err := s.resolveScheduledCallsign(&e.ScheduledFlight, "arrival")
	if err != nil {
		return nil, err
	}

	if _, ok := av.DB.AircraftPerformance[e.AircraftType]; !ok {
		return nil, fmt.Errorf(
			"aircraft type %s is not present in the performance database",
			e.AircraftType,
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
		e.AircraftType,
		normalizeAirportCode(e.DepartureAirport),
		normalizeAirportCode(e.ArrivalAirport),
	)

	if err := ac.InitializeArrival(s.State.Airports[e.ArrivalAirport], arr, e.Cruise,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}
	if published {
		if s.publishedArrivalSpawnConflict(ac) {
			return nil, errPublishedArrivalSpawnConflict
		}
		if e.FiledRoute != "" {
			// The flight files the route the pair is really flown on; within the
			// facility it still flies the scenario's arrival geometry.
			ac.FlightPlan.Route = e.FiledRoute
		}
		s.log("%s: arrival %s->%s via %s %s (%s)", callsign, e.DepartureAirport, e.ArrivalAirport,
			e.Group, util.Select(arr.STAR == "", arr.FlightStripDisplayRoute, arr.STAR), e.How)
	}

	return s.finalizeArrivalNoLock(ac, arr, e.Group, e.ArrivalAirport)
}

// resolveScheduledCallsign checks a schedule entry's callsign against what the
// sim is currently flying when the flight is finally created. A scenario
// entry whose randomly generated callsign has since been taken draws a new one
// from its airline; a published flight's callsign is the real one and can't be
// resampled, so the clash is an error.
func (s *Sim) resolveScheduledCallsign(f *ScheduledFlight, kind string) (string, error) {
	callsign := strings.ToUpper(strings.TrimSpace(f.Callsign))
	if callsign == "" {
		return "", fmt.Errorf("%s callsign is empty", kind)
	}
	if !av.CallsignClashesWithExisting(s.currentCallsigns(), callsign, s.EnforceUniqueCallsignSuffix) {
		return callsign, nil
	}
	if f.Source != TrafficSourceScenario || f.Airline.Callsign != "" {
		return "", fmt.Errorf("%s %s: %w", kind, callsign, errCallsignInUse)
	}
	_, callsign = f.Airline.SampleAcTypeAndCallsign(s.Rand, s.currentCallsigns(),
		s.EnforceUniqueCallsignSuffix, f.DepartureAirport, f.ArrivalAirport, s.lg)
	if callsign == "" {
		return "", fmt.Errorf("%s %s: %w", kind, f.Callsign, errCallsignInUse)
	}
	return callsign, nil
}

func (s *Sim) currentCallsigns() []av.ADSBCallsign {
	callsigns := slices.Collect(maps.Keys(s.Aircraft))
	for _, fp := range s.STARSComputer.FlightPlans {
		callsigns = append(callsigns, av.ADSBCallsign(fp.ACID))
	}
	// The manual launch slots' pending flights hold their callsigns too:
	// slots are looked up by callsign, so no two may share one.
	for _, e := range s.PendingDepartures {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, e := range s.PendingArrivals {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, e := range s.PendingOverflights {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, ac := range s.PendingVFR {
		callsigns = append(callsigns, ac.ADSBCallsign)
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

// createScheduledOverflight creates the overflight a schedule entry describes;
// the overflight route was sampled when the entry was generated.
func (s *Sim) createScheduledOverflight(e ScheduledOverflight) (*Aircraft, error) {
	flow, ok := s.State.InboundFlows[e.Group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", e.Group)
	}
	if e.Index < 0 || e.Index >= len(flow.Overflights) {
		return nil, fmt.Errorf("%s: no overflight route in %s", e.Callsign, e.Group)
	}
	of := &flow.Overflights[e.Index]

	callsign, err := s.resolveScheduledCallsign(&e.ScheduledFlight, "overflight")
	if err != nil {
		return nil, err
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(av.FlightRulesIFR, e.AircraftType, e.DepartureAirport, e.ArrivalAirport)

	if err := ac.InitializeOverflight(of, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}

	return ac, s.finalizeOverflightNoLock(ac, of, e.Group)
}

// finalizeOverflightNoLock builds the overflight's NAS flight plan with
// controller assignments and registers it with STARS.
func (s *Sim) finalizeOverflightNoLock(ac *Aircraft, of *av.Overflight, group string) error {
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
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp); err != nil {
		return err
	}

	// Create a flight strip at the inbound handoff controller if it's a human position
	if shouldCreateFlightStrip(&nasFp) && !s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	return s.associateAtSpawn(ac, nasFp)
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
