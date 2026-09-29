// sim/spawn_departures.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

type RunwayLaunchState struct {
	IFRSpawnRate float32
	VFRSpawnRate float32

	// NextVFRSpawn is when to create the runway's next VFR departure, based
	// on its VFR rate; IFR departures come from the schedule. The actual
	// time an aircraft is launched may be later, e.g. if we need longer for
	// wake turbulence separation, etc.
	NextVFRSpawn Time

	// Aircraft follow the following flows:
	// VFR: ReleasedVFR -> launched
	// IFR no release: Gate -> ReleasedIFR -> launched
	// IFR release required: Gate -> Held -> ReleasedIFR -> launched

	// At the gate, flight plan filed (if IFR), not yet ready to go
	Gate []DepartureAircraft
	// Ready to go, in hold for release purgatory.
	Held []DepartureAircraft
	// Holding short, waiting their turn; the next to launch is chosen from
	// these each time the runway is free.
	ReleasedIFR []DepartureAircraft
	ReleasedVFR []DepartureAircraft

	// PublishedDepartures counts the published flights sent to this runway,
	// by the category they go out in, so that runways whose gates suit a
	// flight equally well split it in proportion to their rates for that
	// category. The counts are cleared whenever the rates or the runways
	// launching change, since a share only means something against the
	// rates that produced it.
	PublishedDepartures map[string]int

	LastDeparture          *DepartureAircraft
	LastArrivalLandingTime Time           // when the last arrival landed on this runway
	LastArrivalFlightRules av.FlightRules // flight rules of the last arrival that landed

	// GoAroundHoldUntil is the time until which departures should be held
	// after a go-around. Departures auto-resume after this time.
	GoAroundHoldUntil Time

	VFRAttempts  int
	VFRSuccesses int
}

// DepartureAircraft represents a departing aircraft, either still on the
// ground or recently-launched.
type DepartureAircraft struct {
	ADSBCallsign  av.ADSBCallsign
	MinSeparation time.Duration // How long after takeoff it will be at ~6000' and airborne
	// AirborneDistance is the estimated distance in nm from the departure
	// point at which the aircraft lifts off; negative if it wasn't airborne
	// within the horizon of the takeoff-roll simulation.
	AirborneDistance float32
	// AirborneTime is the estimated time after the start of the takeoff
	// roll at which the aircraft lifts off; zero if it wasn't airborne
	// within the horizon of the takeoff-roll simulation.
	AirborneTime time.Duration
	LaunchPath   []math.Point2LL // position at 1s intervals after the takeoff roll starts
	SpawnTime    Time            // when it was first spawned
	QueuedTime   Time            // when it joined the runway's queue of departures holding short
	LaunchTime   Time            // when it was actually launched; used for wake turbulence separation, etc.

	// When they're ready to leave the gate
	ReadyDepartGateTime Time

	// HFR-only.
	ReleaseRequested   bool
	ReleaseDelay       time.Duration // minimum wait after release before the takeoff roll
	RequestReleaseTime Time
}

// initDepartureState builds the per-runway departure state and the VFR and
// pattern spawn timers. IFR departures, arrivals, and overflights need no
// timers: they come from the schedule.
func (s *Sim) initDepartureState(now Time) {
	// Randomize the next VFR spawn time; may be before or after the current
	// time.
	randomDelay := func(rate float32) Time {
		if rate == 0 {
			return now.Add(maxSpawnWait)
		}
		avgWait := 3600 / rate
		delta := s.Rand.Float32Range(-avgWait/2, avgWait/2)
		return now.Add(time.Duration(delta * float32(time.Second)))
	}

	for _, name := range util.SortedMapKeys(s.State.DepartureAirports) {
		s.DepartureState[name] = make(map[av.RunwayID]*RunwayLaunchState)

		if runwayRates, ok := s.State.LaunchConfig.DepartureRates[name]; ok {
			for rwy, rate := range runwayRates {
				s.DepartureState[name][rwy] = &RunwayLaunchState{
					IFRSpawnRate:        sumRateMap(rate, s.State.LaunchConfig.DepartureRateScale),
					PublishedDepartures: make(map[string]int),
				}
			}
		}

		ap := s.State.Airports[name]
		if vfrRate := ap.VFRRateSum(); vfrRate > 0 {
			rwy := s.State.VFRRunways[name]
			state, ok := s.DepartureState[name][av.RunwayID(rwy.Id)]
			if !ok {
				state = &RunwayLaunchState{PublishedDepartures: make(map[string]int)}
				s.DepartureState[name][av.RunwayID(rwy.Id)] = state
			}
			state.VFRSpawnRate = scaleRate(vfrRate, s.State.LaunchConfig.VFRDepartureRateScale)
			state.NextVFRSpawn = randomDelay(state.VFRSpawnRate)

			// Initialize pattern state for airports with VFR activity,
			// but not at airports that also have IFR departures or arrivals.
			_, hasIFRDepartures := s.State.LaunchConfig.DepartureRates[name]
			_, hasIFRArrivals := s.State.ArrivalAirports[name]
			if !hasIFRDepartures && !hasIFRArrivals {
				s.PatternState[name] = &PatternState{
					NextSpawn: now.Add(randomWait(s.State.LaunchConfig.patternSpawnRate(), false, s.Rand)),
				}
			}
		}
	}
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

	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "departure")
	if err != nil {
		return nil, err
	}

	routes := av.ExitRoutesForAircraft(db.Lookups{}, exitRoutes, e.AircraftType)
	if _, ok := routes[dep.Exit]; !ok {
		return nil, fmt.Errorf("%s/%s: no route to %s for a %s", e.DepartureAirport, rwy.Runway,
			dep.Exit, e.AircraftType)
	}

	return ac, s.initializeIFRDeparture(ac, ap, e.DepartureAirport, e.Runway, dep,
		CruiseLimits{}, routes)
}

// createPublishedIFRDeparture creates a departure using the published identity
// from a timetable or from historical flight data. Vice still resolves the
// exit, SID, route, altitude, and controller assignment from the active
// scenario.
// The categories are the ones the scenario is launching from this runway; the
// one used is whichever gets the aircraft closest to where it really went,
// rather than one sampled by rate. Published traffic takes its share of each
// exit from the flights themselves, and the flight counts toward the runway's
// share of the airport's published departures from here on.
func (s *Sim) createPublishedIFRDeparture(e ScheduledDeparture, runway av.RunwayID,
	categories []string) (*Aircraft, error) {
	ac, err := s.newScheduledAircraft(&e.ScheduledFlight, "departure")
	if err != nil {
		return nil, err
	}

	placement, err := s.State.resolvePublishedDeparture(e.DepartureAirport, runway, categories,
		e.ArrivalAirport, e.AircraftType, s.routedPairsIndex().destinationsByOrigin)
	if err != nil {
		return nil, err
	}

	s.log("%s: departure %s->%s runway %s exit %s (%s)", ac.ADSBCallsign, e.DepartureAirport,
		e.ArrivalAirport, runway, placement.dep.Exit, placement.how)

	if err := s.initializeIFRDeparture(ac, placement.ap, e.DepartureAirport, runway, &placement.dep,
		placement.cruise, placement.exitRoutes); err != nil {
		return nil, err
	}
	if depState := s.DepartureState[e.DepartureAirport][runway]; depState != nil {
		depState.PublishedDepartures[placement.rwy.Category]++
	}
	return ac, nil
}

func (s *Sim) initializeIFRDeparture(ac *Aircraft, ap *av.Airport, departureAirport av.ICAOAirportCode,
	runway av.RunwayID, dep *av.Departure, cruise CruiseLimits,
	exitRoutes map[av.ExitID]*av.ExitRoute) error {
	exitRoute := exitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, *exitRoute, cruise, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.Rand, s.lg)
	if err != nil {
		return err
	}

	ac.ReportDepartureHeading = exitRoutesHaveVariedHeadings(exitRoutes)
	ac.ReportDepartureSID = exitRoutesHaveVariedSIDs(exitRoutes)

	shortExit := dep.Exit.Base()
	isTRACON := db.DB.IsTRACON(s.State.Facility)
	nasFp := s.initFlightPlan(ac, av.FlightTypeDeparture)
	if exitRoute.SID != "" {
		nasFp.Route = exitRoute.SID + " " + dep.Route
	} else {
		nasFp.Route = dep.Route
	}
	nasFp.EntryFix = db.AirportDisplayId(ac.DepartureAirport)
	// The flight plan carries the exit's 3-character fix id when one is
	// adapted; fix-pair endpoints and adapted fix criteria match against it.
	nasFp.ExitFix = s.State.FacilityAdaptation.FixPairFixID(shortExit)
	nasFp.SecondaryScratchpad = dep.SecondaryScratchpad
	nasFp.AssignedAltitude = util.Select(!isTRACON, ac.CruiseAltitude, 0)
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && exitRoute.IsRNAV

	ac.HoldForRelease = ap.HoldForRelease || exitRoute.HoldForRelease
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

	if db.DB.IsARTCC(s.State.Facility) {
		// The departure levels off at the exit route's altitude until it is
		// climbed further, so the data block needs it as an interim altitude
		// for conflict alert to know where the climb stops.
		alt := util.Select(exitRoute.AssignedAltitude != 0, exitRoute.AssignedAltitude, exitRoute.ClearedAltitude)
		s.recordVirtualAltitudeEntry(&nasFp, min(alt, ac.CruiseAltitude), true)
		nasFp.applyERAMEntries(exitRoute.ERAM)
	}

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// departure assignment above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, string(runway))
	// A fully-contained (internal) flight whose exit fix is a local-arrival
	// airport is reclassified as an arrival for display/processing. The initial
	// owner stays the departure controller assigned above; ownership is
	// deliberately not re-derived as an arrival.
	if nasFp.LocalArrival {
		nasFp.TypeOfFlight = av.FlightTypeArrival
	}
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return err
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
	return err
}

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

// assignDepartureController sets up controller assignments for a departure.
// It handles two cases:
//  1. The airport or, failing that, the exit route has a virtual departure
//     controller -> auto-release, use that controller
//  2. Human controller -> set contact altitude, use human controller position
func (s *Sim) assignDepartureController(ac *Aircraft, nasFp *FlightPlan,
	ap *av.Airport, exitRoute *av.ExitRoute, departureAirport av.ICAOAirportCode, runway string) {

	// Departures that start with a virtual controller are already on its
	// frequency, so they never check in with a departure controller; -1 keeps
	// them from being mistaken for ones waiting on a /tc point. There's no
	// human to release them, so they're released automatically.
	for _, ctrl := range []ControlPosition{ap.DepartureController, exitRoute.DepartureController} {
		if ctrl != "" && s.isVirtualController(ctrl) {
			nasFp.TrackingController = ctrl
			nasFp.OwningTCW = s.tcwForPosition(ctrl)
			nasFp.InboundHandoffController = exitRoute.HandoffController
			ac.ControllerFrequency = ctrl
			ac.DepartureContactAltitude = -1
			ac.HoldForRelease = false
			return
		}
	}

	// Human controller will be first
	pos := s.ScenarioRootPosition()
	if tcp := s.GetDepartureController(departureAirport, runway, exitRoute.SID); tcp != "" {
		pos = tcp
	}

	// Set altitude at which aircraft will contact departure control
	if exitRoute.WaitToContactDeparture {
		ac.DepartureContactAltitude = 0
	} else {
		ac.DepartureContactAltitude = ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(s.Rand.Intn(500))
		ac.DepartureContactAltitude = min(ac.DepartureContactAltitude, float32(ac.CruiseAltitude))
	}

	nasFp.TrackingController = pos
	nasFp.OwningTCW = s.tcwForPosition(pos)
	nasFp.InboundHandoffController = pos
}

func (s *Sim) addDepartureToPool(ac *Aircraft, runway av.RunwayID, gateDelay time.Duration) {
	depac := makeDepartureAircraft(ac, s.State.SimTime, gateDelay)

	ac.WaitingForLaunch = true
	s.addAircraft(*ac)

	// The journey begins...
	depState := s.DepartureState[ac.DepartureAirport][runway]
	if ac.FlightRules == av.FlightRulesIFR {
		// IFRs spend some time at the gate to give them a chance to appear
		// in the FLIGHT PLAN list.
		depState.Gate = append(depState.Gate, depac)
	} else {
		// VFRs can go straight to the queue.
		depac.QueuedTime = s.State.SimTime
		depState.ReleasedVFR = append(depState.ReleasedVFR, depac)
	}
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
		s.deleteAircraft(s.Aircraft[dep.ADSBCallsign], DeletePrespawn)
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
	if exit := ac.Exit; exit != "" {
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

	// Hold for an arrival on short final, unless both it and the departure
	// are VFR.
	if util.SeqContainsFunc(maps.Values(s.Aircraft), func(ac *Aircraft) bool {
		bothVFR := ac.FlightRules == av.FlightRulesVFR && rules == av.FlightRulesVFR
		return !bothVFR && ac.onShortFinal(runway.Base())
	}) {
		return false
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
	if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok && ac.Exit != "" {
		if t, ok := s.LastExitLaunch[airport][ac.Exit]; ok && now.Sub(t) < sameExitSeparation {
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
		if pt, ok := av.RunwayIntersectionPoint(db.Lookups{}, airport, runway, otherRwy, s.State.NmPerLongitude, 1); ok {
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
	runway, ok := av.LookupRunway(db.Lookups{}, airport, rwy.Base())
	if !ok {
		return [2]float32{}, [2]float32{}, false
	}
	opp, ok := av.LookupOppositeRunway(db.Lookups{}, airport, rwy.Base())
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
	if !pok || !cok || pac.FlightRules != av.FlightRulesIFR || cac.FlightRules != av.FlightRulesIFR {
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

func (s *Sim) cullDepartures(keep int, d []DepartureAircraft) []DepartureAircraft {
	if len(d) < keep {
		return d
	}

	for _, dep := range d[keep:] {
		if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok {
			s.deleteAircraft(ac, DeleteRateChange)
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
