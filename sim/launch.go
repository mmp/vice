// sim/launch.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// LaunchFlight identifies the flight a launch control slot is showing; the
// launch and recycle RPCs pass it back. A callsign alone is ambiguous: a
// turnaround flies it in both directions, and real traffic can fly it more
// than once a day, so the direction and the flight data's day and minute pin
// down which queued flight is meant. Day and Minute are zero for sampled
// scenario flights, which are unique by callsign. Runway is the departure
// slot's runway: a published flight may fit more than one, and it launches
// from the one whose slot was clicked.
type LaunchFlight struct {
	Callsign  av.ADSBCallsign
	Departure bool
	Runway    av.RunwayID
	Day       uint16
	Minute    int
}

// DepartureLaunchSlot is one departure slot in the launch control window,
// with the pending flight if one is ready; Callsign is empty when none is.
// Scenario slots are per (airport, runway, category); published traffic flies
// whatever category its data holds, so its slots are per runway with Category
// empty. VFR slots are per airport, marked by Rules.
type DepartureLaunchSlot struct {
	LaunchFlight
	Airport      string
	Category     string
	Rules        av.FlightRules
	AircraftType string
	Exit         string
	Destination  string
	Position     math.Point2LL
}

// InboundLaunchSlot is one arrival or overflight slot; Airport is
// "overflights" for overflight slots, matching InboundFlowRates.
type InboundLaunchSlot struct {
	LaunchFlight
	Group        string
	Airport      string
	AircraftType string
	Position     math.Point2LL
}

// refillPendingLaunches keeps a sampled flight ready in each manual launch
// slot that draws from the scenario's own traffic: scenario IFR slots, VFR
// slots, and overflight slots. Published slots need no filling--they show the
// schedule's queue heads. Pending flights are identities only; nothing is
// allocated until they launch, so they can be discarded freely.
func (s *Sim) refillPendingLaunches() {
	lc := &s.State.LaunchConfig
	now := s.State.SimTime

	if lc.DepartureMode == LaunchManual {
		if lc.TrafficSource == TrafficSourceScenario {
			if s.PendingDepartures == nil {
				s.PendingDepartures = make(map[string]*ScheduledDeparture)
			}
			for _, airport := range util.SortedMapKeys(lc.DepartureRates) {
				for _, runway := range util.SortedMapKeys(lc.DepartureRates[airport]) {
					for _, category := range util.SortedMapKeys(lc.DepartureRates[airport][runway]) {
						key := airport + "/" + string(runway) + "/" + category
						if s.PendingDepartures[key] == nil {
							if e, ok := s.sampleScenarioDeparture(airport, runway, category, now); ok {
								s.PendingDepartures[key] = &e
							}
						}
					}
				}
			}
		}

		if s.PendingVFR == nil {
			s.PendingVFR = make(map[string]*Aircraft)
		}
		// Initialized separately: PendingVFR is serialized and nextVFRSample,
		// a retry timer, is not, so a reloaded sim arrives with only the former.
		if s.nextVFRSample == nil {
			s.nextVFRSample = make(map[string]Time)
		}
		for _, airport := range util.SortedMapKeys(lc.VFRAirportRates) {
			if s.PendingVFR[airport] == nil && !now.Before(s.nextVFRSample[airport]) {
				if ac, err := s.sampleVFRDeparture(airport); err == nil && ac != nil {
					s.PendingVFR[airport] = ac
				} else {
					// VFR route sampling fails routinely; try again shortly
					// rather than every tick.
					s.nextVFRSample[airport] = now.Add(2 * time.Second)
				}
			}
		}
	}

	if lc.ArrivalMode == LaunchManual && lc.TrafficSource == TrafficSourceScenario {
		if s.PendingArrivals == nil {
			s.PendingArrivals = make(map[string]*ScheduledArrival)
		}
		for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
			for _, airport := range util.SortedMapKeys(lc.InboundFlowRates[group]) {
				if airport == "overflights" {
					continue
				}
				key := group + "/" + airport
				if s.PendingArrivals[key] == nil {
					if e, ok := s.sampleScenarioArrival(group, airport, now); ok {
						s.PendingArrivals[key] = &e
					}
				}
			}
		}
	}

	if lc.OverflightMode == LaunchManual {
		if s.PendingOverflights == nil {
			s.PendingOverflights = make(map[string]*ScheduledOverflight)
		}
		for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
			if _, ok := lc.InboundFlowRates[group]["overflights"]; !ok {
				continue
			}
			if s.PendingOverflights[group] == nil {
				if e, ok := s.sampleScenarioOverflight(group, now); ok {
					s.PendingOverflights[group] = &e
				}
			}
		}
	}
}

// currentLaunchSlots returns the launch control slots for the current
// publication generation, rebuilding them when the sim has changed since they
// were last built. Caller must hold s.mu.
func (s *Sim) currentLaunchSlots() ([]DepartureLaunchSlot, []InboundLaunchSlot) {
	if !s.launchSlotsBuilt || s.launchSlotGen != s.pubGen {
		s.launchDepartureSlots, s.launchInboundSlots = s.buildLaunchSlots()
		s.launchSlotGen, s.launchSlotsBuilt = s.pubGen, true
	}
	return s.launchDepartureSlots, s.launchInboundSlots
}

// buildLaunchSlots enumerates the launch control slots for whichever kinds
// are in manual mode and fills each with its pending flight: the sampled
// pending entry for scenario and VFR slots, the schedule's next queued flight
// for published slots.
func (s *Sim) buildLaunchSlots() ([]DepartureLaunchSlot, []InboundLaunchSlot) {
	lc := &s.State.LaunchConfig
	var departures []DepartureLaunchSlot
	var inbounds []InboundLaunchSlot

	if lc.DepartureMode == LaunchManual {
		if lc.TrafficSource == TrafficSourceScenario {
			for _, airport := range util.SortedMapKeys(lc.DepartureRates) {
				for _, runway := range util.SortedMapKeys(lc.DepartureRates[airport]) {
					for _, category := range util.SortedMapKeys(lc.DepartureRates[airport][runway]) {
						slot := DepartureLaunchSlot{
							LaunchFlight: LaunchFlight{Departure: true, Runway: runway},
							Airport:      airport,
							Category:     category,
							Rules:        av.FlightRulesIFR,
							Position:     runwayThresholdPosition(airport, runway),
						}
						if e := s.PendingDepartures[airport+"/"+string(runway)+"/"+category]; e != nil {
							slot.Callsign = av.ADSBCallsign(e.Callsign)
							slot.AircraftType = e.AircraftType
							if ap := s.State.Airports[airport]; ap != nil &&
								e.DepartureIndex >= 0 && e.DepartureIndex < len(ap.Departures) {
								slot.Exit = string(ap.Departures[e.DepartureIndex].Exit)
							}
						}
						departures = append(departures, slot)
					}
				}
			}
		} else {
			for _, airport := range util.SortedMapKeys(lc.DepartureEnabled) {
				for _, runway := range util.SortedMapKeys(lc.DepartureEnabled[airport]) {
					categories := lc.enabledDepartureCategories(airport, runway)
					if len(categories) == 0 {
						continue
					}
					slot := DepartureLaunchSlot{
						LaunchFlight: LaunchFlight{Departure: true, Runway: runway},
						Airport:      airport,
						Rules:        av.FlightRulesIFR,
						Position:     runwayThresholdPosition(airport, runway),
					}
					// The next queued flight this runway's gates fly; a flight
					// bound for another runway's gate is stepped over, as at
					// spawn.
					for i := range s.Schedule.Departures {
						e := &s.Schedule.Departures[i]
						if e.Source == TrafficSourceScenario || e.DepartureAirport != airport {
							continue
						}
						choice, err := s.findPublishedDeparture(airport, runway, categories,
							e.ArrivalAirport, e.AircraftType, s.routedPairsIndex().destinationsByOrigin)
						if err != nil {
							continue
						}
						slot.Callsign = av.ADSBCallsign(e.Callsign)
						slot.Day, slot.Minute = e.Day, e.Minute
						slot.AircraftType = e.AircraftType
						slot.Exit = string(choice.candidate.dep.Exit)
						break
					}
					departures = append(departures, slot)
				}
			}
		}

		for _, airport := range util.SortedMapKeys(lc.VFRAirportRates) {
			slot := DepartureLaunchSlot{
				LaunchFlight: LaunchFlight{Departure: true, Runway: av.RunwayID(s.State.VFRRunways[airport].Id)},
				Airport:      airport,
				Rules:        av.FlightRulesVFR,
			}
			if ac := s.PendingVFR[airport]; ac != nil {
				slot.Callsign = ac.ADSBCallsign
				slot.AircraftType = ac.FlightPlan.AircraftType
				slot.Destination = ac.FlightPlan.ArrivalAirport
				slot.Position = ac.Position()
			}
			departures = append(departures, slot)
		}
	}

	if lc.ArrivalMode == LaunchManual {
		scenario := lc.TrafficSource == TrafficSourceScenario
		for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
			for _, airport := range util.SortedMapKeys(lc.InboundFlowRates[group]) {
				if airport == "overflights" {
					continue
				}
				if !scenario && !lc.InboundFlowEnabled[group][airport] {
					continue
				}
				slot := InboundLaunchSlot{Group: group, Airport: airport}
				if scenario {
					if e := s.PendingArrivals[group+"/"+airport]; e != nil {
						slot.Callsign = av.ADSBCallsign(e.Callsign)
						slot.AircraftType = e.AircraftType
						slot.Position = s.arrivalSpawnPosition(e)
					}
				} else if i := slices.IndexFunc(s.Schedule.Arrivals, func(e ScheduledArrival) bool {
					return e.Group == group && e.ArrivalAirport == airport && e.DropReason == ""
				}); i != -1 {
					e := &s.Schedule.Arrivals[i]
					slot.Callsign = av.ADSBCallsign(e.Callsign)
					slot.Day, slot.Minute = e.Day, e.Minute
					slot.AircraftType = e.AircraftType
					slot.Position = s.arrivalSpawnPosition(e)
				}
				inbounds = append(inbounds, slot)
			}
		}
	}

	if lc.OverflightMode == LaunchManual {
		for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
			if _, ok := lc.InboundFlowRates[group]["overflights"]; !ok {
				continue
			}
			slot := InboundLaunchSlot{Group: group, Airport: "overflights"}
			if e := s.PendingOverflights[group]; e != nil {
				slot.Callsign = av.ADSBCallsign(e.Callsign)
				slot.AircraftType = e.AircraftType
				if flow, ok := s.State.InboundFlows[group]; ok &&
					e.Index >= 0 && e.Index < len(flow.Overflights) &&
					len(flow.Overflights[e.Index].Waypoints) > 0 {
					slot.Position = flow.Overflights[e.Index].Waypoints[0].Location
				}
			}
			inbounds = append(inbounds, slot)
		}
	}

	return departures, inbounds
}

func runwayThresholdPosition(airport string, runway av.RunwayID) math.Point2LL {
	if rwy, ok := av.LookupRunway(airport, runway.Base()); ok {
		return rwy.Threshold
	}
	return math.Point2LL{}
}

func (s *Sim) arrivalSpawnPosition(e *ScheduledArrival) math.Point2LL {
	if flow, ok := s.State.InboundFlows[e.Group]; ok &&
		e.Index >= 0 && e.Index < len(flow.Arrivals) &&
		len(flow.Arrivals[e.Index].Waypoints) > 0 {
		return flow.Arrivals[e.Index].Waypoints[0].Location
	}
	return math.Point2LL{}
}

// LaunchAircraft launches the flight a launch control slot is showing: the
// aircraft is created--resources and all--and enters the sim, and the slot
// refills. A published flight may launch ahead of its scheduled time.
func (s *Sim) LaunchAircraft(tcw TCW, flight LaunchFlight) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Refill the emptied slot in the same update, so it never shows blank.
	defer func() {
		s.refillPendingLaunches()
		s.publish()
	}()

	if flight.Departure {
		for key, ac := range s.PendingVFR {
			if ac.ADSBCallsign == flight.Callsign {
				delete(s.PendingVFR, key)
				s.addAircraftNoLock(*ac)
				return nil
			}
		}

		for key, e := range s.PendingDepartures {
			if av.ADSBCallsign(e.Callsign) == flight.Callsign {
				delete(s.PendingDepartures, key)
				ac, err := s.createScheduledIFRDeparture(*e)
				if err != nil {
					return err
				}
				s.launchDeparture(ac, e.Runway, e.Source)
				return nil
			}
		}

		if i := findScheduledPublished(s.Schedule.Departures,
			func(e *ScheduledDeparture) *ScheduledFlight { return &e.ScheduledFlight }, flight); i != -1 {
			e := s.Schedule.Departures[i]
			// The flight launches from the runway whose slot was clicked; it
			// may fit others too.
			categories := s.State.LaunchConfig.enabledDepartureCategories(e.DepartureAirport, flight.Runway)
			if len(categories) == 0 {
				return fmt.Errorf("%s/%s: runway is not launching departures",
					e.DepartureAirport, flight.Runway)
			}
			ac, err := s.createPublishedIFRDeparture(e, flight.Runway, categories)
			if err != nil {
				return err
			}
			s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
			s.launchDeparture(ac, flight.Runway, e.Source)
			return nil
		}

		return ErrNoMatchingFlight
	}

	for key, e := range s.PendingArrivals {
		if av.ADSBCallsign(e.Callsign) == flight.Callsign {
			delete(s.PendingArrivals, key)
			ac, err := s.createScheduledArrival(*e)
			if err != nil {
				return err
			}
			s.addAircraftNoLock(*ac)
			return nil
		}
	}

	for key, e := range s.PendingOverflights {
		if av.ADSBCallsign(e.Callsign) == flight.Callsign {
			delete(s.PendingOverflights, key)
			ac, err := s.createScheduledOverflight(*e)
			if err != nil {
				return err
			}
			s.addAircraftNoLock(*ac)
			return nil
		}
	}

	if i := findScheduledPublished(s.Schedule.Arrivals,
		func(e *ScheduledArrival) *ScheduledFlight { return &e.ScheduledFlight }, flight); i != -1 {
		ac, err := s.createScheduledArrival(s.Schedule.Arrivals[i])
		if err != nil {
			return err
		}
		s.Schedule.Arrivals = deleteScheduledEntry(s.Schedule.Arrivals, i)
		s.addAircraftNoLock(*ac)
		return nil
	}

	return ErrNoMatchingFlight
}

func (s *Sim) launchDeparture(ac *Aircraft, runway av.RunwayID, source TrafficSource) {
	if ac.HoldForRelease {
		s.addDepartureToPool(ac, runway, true /* manual launch */, source)
	} else {
		s.addAircraftNoLock(*ac)
	}
}

// RecycleLaunchAircraft discards the flight a launch control slot is showing
// so that the slot moves on to the next one. Nothing was allocated for it, so
// nothing is freed. Recycling a published flight removes it from the schedule
// and pulls the rest of its flow earlier by the gap it leaves, so that
// switching back to automatic launches doesn't sit through a hole.
func (s *Sim) RecycleLaunchAircraft(tcw TCW, flight LaunchFlight) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Refill the emptied slot in the same update, so it never shows blank.
	defer func() {
		s.refillPendingLaunches()
		s.publish()
	}()

	if flight.Departure {
		for key, ac := range s.PendingVFR {
			if ac.ADSBCallsign == flight.Callsign {
				delete(s.PendingVFR, key)
				return nil
			}
		}
		for key, e := range s.PendingDepartures {
			if av.ADSBCallsign(e.Callsign) == flight.Callsign {
				delete(s.PendingDepartures, key)
				return nil
			}
		}

		if i := findScheduledPublished(s.Schedule.Departures,
			func(e *ScheduledDeparture) *ScheduledFlight { return &e.ScheduledFlight }, flight); i != -1 {
			e := s.Schedule.Departures[i]
			// The recycled flight's flow is the clicked slot's runway: the
			// flights that runway's gates fly.
			categories := s.State.LaunchConfig.enabledDepartureCategories(e.DepartureAirport, flight.Runway)
			sameFlow := func(o *ScheduledDeparture) bool {
				if o.Source == TrafficSourceScenario || o.DepartureAirport != e.DepartureAirport {
					return false
				}
				if len(categories) == 0 {
					// The runway is no longer launching; treat the airport as one flow.
					return true
				}
				_, ferr := s.findPublishedDeparture(o.DepartureAirport, flight.Runway, categories,
					o.ArrivalAirport, o.AircraftType, s.routedPairsIndex().destinationsByOrigin)
				return ferr == nil
			}
			s.Schedule.Departures = removeScheduledAndShift(s.Schedule.Departures, i,
				func(o *ScheduledDeparture) *ScheduledFlight { return &o.ScheduledFlight }, sameFlow)
			s.Schedule.sortDepartures()
			return nil
		}

		return ErrNoMatchingFlight
	}

	for key, e := range s.PendingArrivals {
		if av.ADSBCallsign(e.Callsign) == flight.Callsign {
			delete(s.PendingArrivals, key)
			return nil
		}
	}
	for key, e := range s.PendingOverflights {
		if av.ADSBCallsign(e.Callsign) == flight.Callsign {
			delete(s.PendingOverflights, key)
			return nil
		}
	}

	if i := findScheduledPublished(s.Schedule.Arrivals,
		func(e *ScheduledArrival) *ScheduledFlight { return &e.ScheduledFlight }, flight); i != -1 {
		e := s.Schedule.Arrivals[i]
		s.Schedule.Arrivals = removeScheduledAndShift(s.Schedule.Arrivals, i,
			func(o *ScheduledArrival) *ScheduledFlight { return &o.ScheduledFlight },
			func(o *ScheduledArrival) bool {
				return o.Source != TrafficSourceScenario && o.Group == e.Group &&
					o.ArrivalAirport == e.ArrivalAirport
			})
		s.Schedule.sortArrivals()
		return nil
	}

	return ErrNoMatchingFlight
}

// removeScheduledAndShift removes entries[i] and pulls the later entries of
// its flow earlier by the gap between it and the next one, carrying the shift
// in SpawnOffset so a published rate-scale rewrite keeps it.
func removeScheduledAndShift[T any](entries []T, i int,
	flight func(*T) *ScheduledFlight, sameFlow func(*T) bool) []T {
	removed := flight(&entries[i]).SpawnTime
	var delta time.Duration
	for j := i + 1; j < len(entries); j++ {
		if sameFlow(&entries[j]) {
			delta = flight(&entries[j]).SpawnTime.Sub(removed)
			break
		}
	}

	kept := deleteScheduledEntry(entries, i)
	if delta > 0 {
		for j := i; j < len(kept); j++ {
			if sameFlow(&kept[j]) {
				f := flight(&kept[j])
				f.SpawnTime = f.SpawnTime.Add(-delta)
				f.SpawnOffset -= delta
			}
		}
	}
	return kept
}

// findScheduledPublished finds the published schedule entry a launch control
// slot is showing. The flight data's day and minute pick out the right one
// when the same callsign is flown more than once in the same direction; a
// slot gone stale--its flight launched or recycled by someone else--matches
// nothing rather than the wrong flight.
func findScheduledPublished[T any](entries []T, flightOf func(*T) *ScheduledFlight,
	flight LaunchFlight) int {
	return slices.IndexFunc(entries, func(e T) bool {
		f := flightOf(&e)
		return f.Source != TrafficSourceScenario && av.ADSBCallsign(f.Callsign) == flight.Callsign &&
			f.Day == flight.Day && f.Minute == flight.Minute
	})
}

// clearPendingLaunches discards every pending manual-launch flight; nothing
// was allocated for them. Called when launches switch back to automatic or
// the traffic source changes, so stale samples don't linger.
func (s *Sim) clearPendingLaunches() {
	s.PendingDepartures = nil
	s.PendingArrivals = nil
	s.PendingOverflights = nil
	s.PendingVFR = nil
	s.nextVFRSample = nil
}
