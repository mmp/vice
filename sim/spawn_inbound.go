// sim/spawn_inbound.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	gomath "math"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/util"
)

// createScheduledArrival creates the arrival a schedule entry describes, for
// both scenario and published entries. Vice resolves the STAR, initial
// controller, altitude, and spawn geometry from the scenario; the flow and
// arrival index were resolved when the entry was generated. All resource
// allocation--squawk, flight strip, flight plan, list index--happens here, and
// the flight counts as launched from its flow from here on.
func (s *Sim) createScheduledArrival(e ScheduledArrival) (*Aircraft, error) {
	inboundFlow, ok := s.State.InboundFlows[e.Group]
	if !ok {
		return nil, fmt.Errorf("unknown inbound flow %s", e.Group)
	}
	if e.Index < 0 || e.Index >= len(inboundFlow.Arrivals) {
		return nil, fmt.Errorf("%s: no arrival route in %s", e.Callsign, e.Group)
	}
	arr := &inboundFlow.Arrivals[e.Index]

	// The flight plan keeps the real origin even when another airport's route
	// is being flown.
	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "arrival")
	if err != nil {
		return nil, err
	}

	if err := ac.InitializeArrival(s.State.Airports[e.ArrivalAirport], arr, e.Cruise,
		s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.Rand, s.lg); err != nil {
		return nil, err
	}
	var filedRoute string
	if e.Source != TrafficSourceScenario {
		// The flight files the route the pair is really flown on; within the
		// facility it still flies the scenario's arrival geometry.
		filedRoute = e.FiledRoute
		s.log("%s: arrival %s->%s via %s %s (%s)", ac.ADSBCallsign, e.DepartureAirport, e.ArrivalAirport,
			e.Group, util.Select(arr.STAR == "", arr.FlightStripDisplayRoute, arr.STAR), e.How)
	}

	if err := s.finalizeArrival(ac, arr, filedRoute, e.Group); err != nil {
		return nil, err
	}
	s.recordArrivalLaunch(e.Group, ac)
	return ac, nil
}

// finalizeArrival builds the arrival's NAS flight plan with controller
// assignments and registers it with STARS. filedRoute, if set, is the route
// the flight files in place of the one the arrival displays.
func (s *Sim) finalizeArrival(ac *Aircraft, arr *av.Arrival, filedRoute string, group string) error {
	nasFp := s.initFlightPlan(ac, av.FlightTypeArrival)
	switch {
	case filedRoute != "":
		nasFp.Route = filedRoute
	case arr.FlightStripDisplayRoute != "":
		nasFp.Route = arr.FlightStripDisplayRoute
	case arr.STAR != "":
		nasFp.Route = "/. " + arr.STAR
	}
	nasFp.EntryFix = ""
	nasFp.ExitFix = db.AirportDisplayId(ac.ArrivalAirport)
	nasFp.TrackingController = arr.InitialController
	nasFp.OwningTCW = s.tcwForPosition(arr.InitialController)
	ac.ControllerFrequency = arr.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = arr.Scratchpad
	nasFp.SecondaryScratchpad = arr.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && arr.IsRNAV

	if db.DB.IsARTCC(s.State.Facility) {
		nasFp.setInboundERAMAltitudes(arr.Waypoints, arr.AssignedAltitude, arr.ClearedAltitude,
			ac.Nav.FlightState.Altitude)
		nasFp.applyERAMEntries(arr.ERAM)
	}

	// Pseudo-ERAM coordination derives the entry fix; the STARS fix-pair
	// pipeline then reassigns the pair and assigns the owning position,
	// overriding the inbound-flow default above.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, "")
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	ac.maybeSetGoAround(s.State.LaunchConfig.GoAroundRate, s.Rand)

	// Decide at creation whether this pilot will spontaneously report field in sight and, among
	// those, whether they will also request the visual approach. VisualRequestDistance, when set,
	// gates the request to the first tick inside that distance.
	ac.WantsVisualApproach = s.Rand.Float32() < visualFieldProb
	if ac.WantsVisualApproach && s.Rand.Float32() < visualRequestProb {
		ac.VisualApproachRequestDistance = s.Rand.Float32Range(9, 16)
	}

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return err
	}
	// Create a flight strip at the inbound handoff controller if it's a human position
	s.giveFlightStrip(&nasFp, nasFp.InboundHandoffController)

	return s.associateAtSpawn(ac, nasFp)
}

// Published arrivals come when their data says, so each inbound flow spaces
// them into a single stream the way a center delivers one. Like a string of
// aircraft joined by springs, each gap opens up to arrivalTrailNM miles in
// trail, closing up toward minArrivalTrailNM only as far as the arrivals due
// over the next arrivalLookahead need to launch within maxArrivalHold of
// their data times. minArrivalTrailNM is a hard floor: when the data has more
// arrivals than fit at that spacing, they launch more than maxArrivalHold
// late. Nothing outside the flow has a say in when its arrivals launch.
// Scenario arrivals are spaced by their flow's rate instead.
const (
	minArrivalTrailNM = 6
	arrivalTrailNM    = 10
	maxArrivalHold    = 3 * time.Minute
	arrivalLookahead  = 30 * time.Minute
)

// ArrivalLaunch is the last arrival launched from an inbound flow. Its true
// airspeed at the spawn point says how long it takes to open up the trail
// behind it.
type ArrivalLaunch struct {
	Time Time
	TAS  float32
}

func (l ArrivalLaunch) timeToFly(nm float32) time.Duration {
	return time.Duration(nm / l.TAS * float32(time.Hour))
}

// arrivalFlowSpaced reports whether the inbound flow may launch its next
// arrival. The flow's queued arrivals follow its last launch at an even
// spacing: the widest, up to arrivalTrailNM, that launches the kth of them no
// later than maxArrivalHold past its data time. minArrivalTrailNM is a hard
// floor: when even that can't keep them within maxArrivalHold, they go at
// minArrivalTrailNM and launch later.
func (s *Sim) arrivalFlowSpaced(group string) bool {
	last, ok := s.ArrivalLaunches[group]
	if !ok {
		return true
	}
	elapsed := s.State.SimTime.Sub(last.Time)
	minTrail := last.timeToFly(minArrivalTrailNM)
	if elapsed < minTrail {
		return false
	}
	lc := &s.State.LaunchConfig
	trail := last.timeToFly(arrivalTrailNM)
	horizon := s.State.SimTime.Add(arrivalLookahead)
	k := 0
	for _, e := range s.Schedule.Arrivals {
		if e.SpawnTime.After(horizon) {
			break
		}
		if e.Group == group && e.DropReason == "" && lc.landsArrivals(group, e.ArrivalAirport) {
			k++
			trail = min(trail, e.SpawnTime.Add(maxArrivalHold).Sub(last.Time)/time.Duration(k))
		}
	}
	return elapsed >= trail
}

// recordArrivalLaunch makes ac its inbound flow's last launch.
func (s *Sim) recordArrivalLaunch(group string, ac *Aircraft) {
	if s.ArrivalLaunches == nil {
		s.ArrivalLaunches = make(map[string]ArrivalLaunch)
	}
	s.ArrivalLaunches[group] = ArrivalLaunch{Time: s.State.SimTime, TAS: ac.TAS(s.temperatureAt(ac))}
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

	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "overflight")
	if err != nil {
		return nil, err
	}

	if err := ac.InitializeOverflight(of, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.wxModel, s.State.SimTime, s.Rand, s.lg); err != nil {
		return nil, err
	}

	return ac, s.finalizeOverflight(ac, of, e.Group)
}

// finalizeOverflight builds the overflight's NAS flight plan with
// controller assignments and registers it with STARS.
func (s *Sim) finalizeOverflight(ac *Aircraft, of *av.Overflight, group string) error {
	nasFp := s.initFlightPlan(ac, av.FlightTypeOverflight)
	nasFp.Route = of.Waypoints.RouteString()
	nasFp.EntryFix = "" // TODO
	nasFp.ExitFix = ""  // TODO
	nasFp.TrackingController = of.InitialController
	nasFp.OwningTCW = s.tcwForPosition(of.InitialController)
	ac.ControllerFrequency = of.InitialController
	nasFp.InboundHandoffController = s.InboundAssignments[group]
	nasFp.Scratchpad = of.Scratchpad
	nasFp.SecondaryScratchpad = of.SecondaryScratchpad
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && of.IsRNAV
	nasFp.TypeOfFlight = of.TypeOfFlight
	if db.DB.IsARTCC(s.State.Facility) {
		nasFp.setInboundERAMAltitudes(of.Waypoints, of.AssignedAltitude, 0, ac.Nav.FlightState.Altitude)
		nasFp.applyERAMEntries(of.ERAM)
	}

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// inbound-flow default above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, "")
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return err
	}

	// Create a flight strip at the inbound handoff controller if it's a human position
	s.giveFlightStrip(&nasFp, nasFp.InboundHandoffController)

	return s.associateAtSpawn(ac, nasFp)
}

// setInboundERAMAltitudes enters the hard altitude an inbound flight arrives
// with at an ERAM facility: the altitude the previous controller cleared it
// to. Conflict alert treats a level flight as free to go anywhere between its
// altitude and this one, so it has to match the clearance. In order of
// precedence, that is the assigned altitude, the "except maintain" altitude
// of a descend via, or the bottom of the route's restrictions; a flight with
// none of these holds its spawn altitude.
func (fp *FlightPlan) setInboundERAMAltitudes(wps av.WaypointArray, assigned, cleared, spawnAlt float32) {
	lowest, ok := findLowestWaypointAltitude(wps, spawnAlt)
	if ok {
		fp.PerceivedAssigned = lowest
	}
	switch {
	case assigned > 0:
		fp.AssignedAltitude = int(assigned)
	case cleared > 0:
		fp.AssignedAltitude = int(cleared)
	case ok:
		fp.AssignedAltitude = lowest
	default:
		fp.AssignedAltitude = int(spawnAlt)
	}
}

// findLowestWaypointAltitude finds the lowest altitude restriction target from
// the waypoints: the bottom of the procedure they make up, which is the
// altitude an ERAM data block shows for a flight descending it. Returns the
// altitude and true if found, or 0 and false if no restrictions exist.
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

// associateAtSpawn registers nasFp with the STARS computer and, if the
// tracking controller is virtual, immediately associates it with ac so it
// never appears in UnassociatedFlightPlans / the STARS FLIGHT PLAN list.
// External-facility-owned flight plans stay unassociated until the handoff
// into the facility completes.
func (s *Sim) associateAtSpawn(ac *Aircraft, nasFp FlightPlan) error {
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
