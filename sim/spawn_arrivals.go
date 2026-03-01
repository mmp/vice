// sim/spawn_arrivals.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	gomath "math"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

func (s *Sim) spawnArrivalsAndOverflights() {
	now := s.State.SimTime

	if !s.NextPushStart.IsZero() && now.After(s.NextPushStart) {
		// party time
		s.PushEnd = now.Add(time.Duration(s.State.LaunchConfig.ArrivalPushLengthMinutes) * time.Minute)
		s.lg.Debug("arrival push starting", slog.Time("end_time", s.PushEnd))
		s.NextPushStart = time.Time{}
	}
	if !s.PushEnd.IsZero() && now.After(s.PushEnd) {
		// end push
		m := -2 + s.Rand.Intn(4) + s.State.LaunchConfig.ArrivalPushFrequencyMinutes
		s.NextPushStart = now.Add(time.Duration(m) * time.Minute)
		s.lg.Debug("arrival push ending", slog.Time("next_start", s.NextPushStart))
		s.PushEnd = time.Time{}
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

			flow, rateSum := sampleRateMap(filteredRates, s.State.LaunchConfig.InboundFlowRateScale, s.Rand)

			var ac *Aircraft
			var err error
			if flow == "overflights" {
				ac, err = s.createOverflightNoLock(group)
			} else {
				ac, err = s.createArrivalNoLock(group, flow)
			}

			if err != nil {
				s.lg.Errorf("create inbound error: %v", err)
			} else if ac != nil {
				s.addAircraftNoLock(*ac)
				s.NextInboundSpawn[group] = now.Add(randomWait(rateSum, pushActive, s.Rand))
			}
		}
	}
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createArrivalNoLock(arrivalGroup, arrivalAirport)
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

	var ac *Aircraft
	var acType string
	var departureAirport string
	for range 50 {
		airline := rand.SampleSlice(s.Rand, airlines)
		departureAirport = airline.Airport
		if airline.Callsign != "" {
			ac, acType = s.sampleAircraftWithAirlineCallsign(airline.AirlineSpecifier, departureAirport, arrivalAirport, s.lg)
		} else {
			ac, acType = s.sampleAircraft(airline.AirlineSpecifier, departureAirport, arrivalAirport, s.lg)
		}
		if ac != nil {
			break
		}
	}
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft for arrivals to %q", arrivalAirport)
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, departureAirport, arrivalAirport)

	err := ac.InitializeArrival(s.State.Airports[arrivalAirport], &arr,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	nasFp := s.initNASFlightPlan(ac, av.FlightTypeArrival)
	nasFp.Route = ac.FlightPlan.Route
	nasFp.EntryFix = "" // TODO
	nasFp.ExitFix = util.Select(len(ac.FlightPlan.ArrivalAirport) == 4, ac.FlightPlan.ArrivalAirport[1:], ac.FlightPlan.ArrivalAirport)
	nasFp.TrackingController = arr.InitialController
	nasFp.OwningTCW = s.tcwForPosition(arr.InitialController)
	ac.ControllerFrequency = arr.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = arr.Scratchpad
	nasFp.SecondaryScratchpad = arr.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.DisplayRNAVSymbol && arr.IsRNAV

	// For ERAM, set AssignedAltitude and derive PerceivedAssigned from waypoint restrictions.
	if _, isERAM := av.DB.ARTCCs[s.State.Facility]; isERAM {
		if arr.AssignedAltitude > 0 {
			nasFp.AssignedAltitude = int(arr.AssignedAltitude)
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, arr.InitialAltitude); ok {
				nasFp.PerceivedAssigned = alt
			}
		} else {
			// Try to derive from waypoint restrictions
			if alt, ok := findLowestWaypointAltitude(arr.Waypoints, arr.InitialAltitude); ok {
				nasFp.AssignedAltitude = alt
				nasFp.PerceivedAssigned = alt
			} else {
				nasFp.AssignedAltitude = int(arr.InitialAltitude)
			}
		}
	}

	s.maybeSetGoAround(ac, s.State.LaunchConfig.GoAroundRate)

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Create a flight strip at the inbound handoff controller if it's a human position
	if shouldCreateFlightStrip(&nasFp) && !s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	_, err = s.STARSComputer.CreateFlightPlan(nasFp)

	return ac, err
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
	d := 0.1 + 0.6*s.Rand.Float32()
	ac.GoAroundDistance = &d
}

func (s *Sim) CreateOverflight(group string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createOverflightNoLock(group)
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

	var ac *Aircraft
	var acType string
	var departureAirport string
	var arrivalAirport string
	for range 50 {
		airline := rand.SampleSlice(s.Rand, of.Airlines)
		departureAirport = airline.DepartureAirport
		arrivalAirport = airline.ArrivalAirport
		if airline.Callsign != "" {
			ac, acType = s.sampleAircraftWithAirlineCallsign(airline.AirlineSpecifier, departureAirport, arrivalAirport, s.lg)
		} else {
			ac, acType = s.sampleAircraft(airline.AirlineSpecifier, departureAirport, arrivalAirport, s.lg)
		}
		if ac != nil {
			break
		}
	}
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft for overflight in %q", group)
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, departureAirport, arrivalAirport)

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
	nasFp.RNAV = s.State.FacilityAdaptation.DisplayRNAVSymbol && of.IsRNAV

	if err := s.assignSquawk(ac, &nasFp); err != nil {
		return nil, err
	}

	// Create a flight strip at the inbound handoff controller if it's a human position
	if shouldCreateFlightStrip(&nasFp) && !s.isVirtualController(nasFp.InboundHandoffController) {
		s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
	}

	_, err := s.STARSComputer.CreateFlightPlan(nasFp)

	return ac, err
}
