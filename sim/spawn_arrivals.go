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

const scheduledArrivalMinSpawnSeparationNM = 10

var errScheduledArrivalSpawnConflict = errors.New("scheduled arrival spawn point occupied")

func (s *Sim) scheduledArrivalSpawnConflict(candidate *Aircraft) bool {
	for _, existing := range s.Aircraft {
		if !existing.IsArrival() {
			continue
		}
		if math.NMDistance2LL(candidate.Position(), existing.Position()) <
			scheduledArrivalMinSpawnSeparationNM {
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

	for group, rates := range s.State.LaunchConfig.InboundFlowRates {
		if now.After(s.NextInboundSpawn[group]) {
			// Filter rates to only include types that are in automatic mode
			filteredRates := make(map[string]float32)
			for name, rate := range rates {
				if name == "overflights" {
					if s.State.LaunchConfig.OverflightMode == LaunchAutomatic {
						filteredRates[name] = rate
					}
				} else {
					if s.State.LaunchConfig.ArrivalMode == LaunchAutomatic {
						filteredRates[name] = rate
					}
				}
			}

			if len(filteredRates) == 0 {
				continue // Nothing automatic in this group
			}

			ac, delay, err := s.activeTrafficProvider().createInbound(s, group, filteredRates, pushActive)

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
	// Select a random arrival route that serves this airport
	arrivals := s.State.InboundFlows[group].Arrivals
	idx := rand.SampleFiltered(s.Rand, arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			group, arrivalAirport)
	}
	arr := arrivals[idx]

	airlines := arr.Airlines[arrivalAirport]
	if len(airlines) == 0 {
		return nil, fmt.Errorf("no airlines for arrival group %s airport %s", group, arrivalAirport)
	}

	ac, err := filterAndSampleAircraft(s, airlines,
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

	if _, isERAM := av.DB.ARTCCs[s.State.Facility]; isERAM {
		spawnAlt := ac.Nav.FlightState.Altitude
		if arr.AssignedAltitude > 0 {
			nasFp.AssignedAltitude = int(arr.AssignedAltitude)
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, spawnAlt); ok {
				nasFp.PerceivedAssigned = alt
			}
		} else {
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, spawnAlt); ok {
				nasFp.AssignedAltitude = alt
				nasFp.PerceivedAssigned = alt
			} else {
				nasFp.AssignedAltitude = int(spawnAlt)
			}
		}
	}

	s.maybeSetGoAround(ac, s.State.LaunchConfig.GoAroundRate)

	ac.WantsVisualApproach = s.Rand.Float32() < visualFieldProb
	if ac.WantsVisualApproach && s.Rand.Float32() < visualRequestProb {
		ac.VisualApproachRequestDistance = s.Rand.Float32Range(9, 16)
	}

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	if shouldCreateFlightStrip(&nasFp) &&
		!s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	return ac, s.associateAtSpawn(ac, nasFp)
}
func resolveScheduledArrival(arrivals []av.Arrival, arrivalAirport,
	origin string) (*av.Arrival, error) {
	arrivalAirport = normalizeScheduleCode(arrivalAirport)
	origin = normalizeScheduleCode(origin)

	for i := range arrivals {
		arr := &arrivals[i]
		for _, airline := range arr.Airlines[arrivalAirport] {
			if normalizeScheduleCode(airline.Airport) == origin {
				return arr, nil
			}
		}
	}

	return nil, fmt.Errorf("no arrival route from %s to %s", origin, arrivalAirport)
}

// createScheduledArrivalNoLock creates an arrival using the published
// callsign, aircraft type, origin, and destination. Vice continues to resolve
// the STAR, initial controller, altitude, and spawn geometry from the scenario.
func (s *Sim) createScheduledArrivalNoLock(flight ScheduledFlight, group,
	arrivalAirport string) (*Aircraft, error) {
	if flight.OperationAt(arrivalAirport) != ScheduleOperationArrival {
		return nil, fmt.Errorf("%s is not an arrival at %s",
			flight.Callsign, arrivalAirport)
	}

	inboundFlow, ok := s.State.InboundFlows[group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", group)
	}

	arr, err := resolveScheduledArrival(
		inboundFlow.Arrivals,
		arrivalAirport,
		flight.Origin,
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", flight.Callsign, err)
	}

	callsign := strings.ToUpper(strings.TrimSpace(flight.Callsign))
	if callsign == "" {
		return nil, fmt.Errorf("scheduled arrival callsign is empty")
	}
	if av.CallsignClashesWithExisting(
		s.currentCallsigns(),
		callsign,
		s.EnforceUniqueCallsignSuffix,
	) {
		return nil, fmt.Errorf(
			"scheduled arrival callsign %s is already in use",
			callsign,
		)
	}

	aircraftType := normalizeScheduledAircraftType(flight.AircraftType)
	if _, ok := av.DB.AircraftPerformance[aircraftType]; !ok {
		return nil, fmt.Errorf(
			"aircraft type %s is not present in the performance database",
			aircraftType,
		)
	}

	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(
		av.FlightRulesIFR,
		aircraftType,
		normalizeScheduleCode(flight.Origin),
		normalizeScheduleCode(flight.Destination),
	)

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], arr,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}
	if s.scheduledArrivalSpawnConflict(ac) {
		return nil, errScheduledArrivalSpawnConflict
	}

	return s.finalizeArrivalNoLock(ac, arr, group, arrivalAirport)
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
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && of.IsRNAV
	nasFp.TypeOfFlight = of.TypeOfFlight

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
