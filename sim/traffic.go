// sim/traffic_provider.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// trafficProvider supplies automatically generated IFR aircraft to the
// simulation. It also controls when the next departure request should occur;
// scenario traffic uses a rate-based delay while timetable and historical
// traffic use the next flight's published time. createInbound's rates map
// arrival airports only: overflights aren't part of any traffic source's data,
// so the Sim generates them itself on a rate-based timer.
type trafficProvider interface {
	createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error)
	createInbound(s *Sim, group string, rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error)
}

// scenarioTrafficProvider generates traffic from the scenario's own
// definitions: the user sets the departure and arrival rates and the aircraft,
// routes, and spacing are sampled from what the scenario declares.
type scenarioTrafficProvider struct{}

func (scenarioTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error) {
	ac, err := s.makeNewIFRDeparture(airport, runway)
	depState := s.DepartureState[airport][runway]
	return ac, randomWait(depState.IFRSpawnRate, false, s.Rand), err
}

func (scenarioTrafficProvider) createInbound(s *Sim, group string,
	rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error) {
	airport, rateSum := sampleRateMap(
		rates,
		s.State.LaunchConfig.InboundFlowRateScale,
		s.Rand,
	)

	delay := randomWait(rateSum, pushActive, s.Rand)

	ac, err := s.createArrivalNoLock(group, airport)
	return ac, delay, err
}

const (
	// flightSpawnLead is how far ahead of a flight's published time it enters
	// the simulation. Historical published times are when the aircraft
	// actually took off or touched down, so a departure needs long enough to
	// push back and taxi and an arrival needs to already be inbound.
	flightSpawnLead = 15 * time.Minute

	// flightTaxiAllowance is how long before its takeoff time a historical
	// departure is ready to leave the gate, so that it reaches the runway
	// about when it really did.
	flightTaxiAllowance = 5 * time.Minute

	// idleDelay parks a spawn timer when there is nothing left to spawn.
	idleDelay = 365 * 24 * time.Hour

	// historicalFlightWindow is how much flight data a sim is launched with. A
	// sim running longer than this runs out of historical traffic.
	historicalFlightWindow = 8 * time.Hour

	// intraFacilityLegWindow bounds how long after a departure its arrival at
	// another of the facility's airports may be, so that a callsign flown twice
	// in one day isn't mistaken for a single leg.
	intraFacilityLegWindow = 3 * time.Hour

	// repeatedRecordWindow is how close two records of the same operation have
	// to be to be the same one recorded twice. An aircraft can't take off from
	// an airport twice this close together--it is still flying the first one--so
	// nothing this near is a second operation.
	repeatedRecordWindow = 30 * time.Minute
)

// publishedArrivalMaxHeadingDifference is how far an origin may lie from the
// direction an arrival flies in from and still plausibly come in on it. Gates
// are rarely less than this far apart, so a smaller difference doesn't say the
// traffic would come in anywhere else.
const publishedArrivalMaxHeadingDifference = 60 // degrees

var (
	// errNoSuitableArrival: no active arrival can carry the aircraft at all,
	// by class or by altitude.
	errNoSuitableArrival = errors.New("no arrival suits the aircraft")
	// errArrivalSTARInactive: the flight's real route ends with a STAR no
	// active arrival flies, so it is dropped rather than shoehorned onto a
	// flow it never flies.
	errArrivalSTARInactive = errors.New("no active arrival flies STAR")
	// errNoPlausibleArrival: no arrival comes in plausibly from the flight's
	// direction.
	errNoPlausibleArrival = errors.New("no plausible arrival to fly")
)

// loadHistoricalFlights gathers the flights a scenario using historical traffic
// flies: those at its airports over the window starting at the selected time.
// They are derived from the facility's flight data rather than saved with the
// sim, so this runs on restore as well as at launch; the provider skips
// whatever is already in the past, so a restored sim picks up where it left off.
func (s *Sim) loadHistoricalFlights() {
	s.historicalFlights = nil
	if s.State.LaunchConfig.TrafficSource != TrafficSourceHistorical {
		return
	}

	departureAirports, arrivalAirports := s.State.LaunchConfig.IFRAirports()
	flights, err := av.ReadFlightDataCells(util.GetResourcesFS(),
		av.FlightDataCells(departureAirports, arrivalAirports))
	if err != nil {
		s.lg.Errorf("%s historical flight data: %v", s.State.Facility, err)
		return
	}

	// Reach back to cover prespawn: the sim's clock starts PrespawnDuration
	// before the selected time, and it warms up by flying the traffic from that
	// half hour the same way the scenario's own generator would.
	start := s.StartTime.Time()
	s.historicalFlights = av.SelectFlights(flights, departureAirports, arrivalAirports, av.DB.Airlines,
		start.Add(-PrespawnDuration), start.Add(historicalFlightWindow))
}

// IFRAirports returns every airport the scenario generates IFR traffic at, departures and
// arrivals separately. It reads the rate maps, not the enable maps: which flows are switched on
// can change while a sim runs, so that is judged flight by flight at spawn.
func (lc *LaunchConfig) IFRAirports() (departures, arrivals map[string]bool) {
	departures = make(map[string]bool)
	arrivals = make(map[string]bool)
	for airport := range lc.DepartureRates {
		departures[airport] = true
	}
	for _, rates := range lc.InboundFlowRates {
		for airport := range rates {
			if airport != "overflights" {
				arrivals[airport] = true
			}
		}
	}
	return
}

func includeTimetableFlight(flight TimetableFlight, percentage int) bool {
	percentage = min(max(percentage, 0), 100)

	// Zero explicitly disables this direction, including cargo.
	if percentage == 0 {
		return false
	}

	if percentage == 100 || flight.Cargo {
		return true
	}

	// Use a stable hash so the same percentage consistently selects the same
	// flights each time the scenario is loaded.
	return int(stableFlightHash(flight.Callsign+flight.Origin+flight.Destination)%100) < percentage
}

// includeHistoricalFlight is includeTimetableFlight for historical flight data,
// which doesn't record which flights are cargo.
func includeHistoricalFlight(flight av.Flight, percentage int) bool {
	percentage = min(max(percentage, 0), 100)

	if percentage == 0 {
		return false
	}
	if percentage == 100 {
		return true
	}

	return int(stableFlightHash(flight.Callsign+flight.Airport+flight.Other)%100) < percentage
}

// normalizeAirportCode cleans up the airport identifiers that arrive with a
// published flight. Both traffic sources need it: a timetable's come from
// hand-edited CSV and historical ones from an outside dataset.
func normalizeAirportCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

// enrouteFixes returns the fixes a real route names between its endpoints. A
// route reads "ORIGIN ...fixes... DESTINATION", so its two ends are airport
// identifiers rather than points to match a scenario's exits or arrivals
// against: every route into JFK ends with "JFK".
func enrouteFixes(route string) []string {
	fixes := strings.Fields(route)
	if len(fixes) <= 2 {
		return nil
	}
	return fixes[1 : len(fixes)-1]
}

// engineTypeFor is how an aircraft is classified when choosing among a city
// pair's real routes: jets fly the high-altitude ones, everything else the low.
func engineTypeFor(aircraftType string) string {
	if perf, ok := av.DB.AircraftPerformance[aircraftType]; ok {
		return perf.Engine.AircraftType
	}
	return ""
}

// stableFlightHash hashes a flight's identity so the same percentage
// consistently selects the same flights each time the scenario is loaded.
func stableFlightHash(key string) uint32 {
	hash := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= 16777619
	}
	return hash
}

// publishedFlight is one flight waiting to enter the simulation, together with
// the time it should be spawned.
type publishedFlight struct {
	flight av.Flight
	spawn  Time

	// placement is how the arrival was fitted into the scenario. It is resolved
	// once, here: more than one flow can serve an origin and the flows are held
	// in a map, so resolving it per call would pick a different one each time
	// and no flow would ever agree that the arrival was its own.
	placement arrivalPlacement

	// dropReason is why no placement could be found for an arrival. The flight
	// keeps its place in the queue so that the drop is reported when its spawn
	// time comes around rather than in a heap at launch.
	dropReason error
}

// arrivalPlacement is the inbound flow and arrival a published flight comes in
// on, the route it files when the route database is what found it, the airport
// standing in for its origin if the scenario has no way to fly it from where it
// really came from, and how the choice was made, for reporting.
type arrivalPlacement struct {
	group      string
	index      int
	filedRoute string
	substitute string
	how        string
}

// candidateArrival is an arrival an inbound flow the scenario is running could
// bring a published flight in on. Only the flows it works count: putting an
// arrival on one it doesn't model would hand the controller traffic down a
// feeder nobody is working.
type candidateArrival struct {
	group string
	index int
	arr   *av.Arrival
}

// candidateArrivals gathers them in sorted flow order, so that a choice between
// equally good ones doesn't vary between runs.
func (s *Sim) candidateArrivals(arrivalAirport string) []candidateArrival {
	arrivalAirport = normalizeAirportCode(arrivalAirport)

	var candidates []candidateArrival
	for _, group := range util.SortedMapKeys(s.State.InboundFlows) {
		if !s.State.LaunchConfig.InboundFlowEnabled[group][arrivalAirport] {
			continue
		}
		arrivals := s.State.InboundFlows[group].Arrivals
		for i := range arrivals {
			if slices.Contains(arrivals[i].ServedAirports(), arrivalAirport) {
				candidates = append(candidates, candidateArrival{group, i, &arrivals[i]})
			}
		}
	}
	return candidates
}

// publishedTrafficProvider emits the flights a scenario was launched with--from
// a built-in timetable or from historical flight data--in published order.
// Departures are spawned at the gate ahead of their published time; arrivals
// are spawned ahead of their touchdown time.
type publishedTrafficProvider struct {
	departures []publishedFlight

	arrivals []publishedFlight

	// routed indexes the route database, which both directions consult to fit a
	// city pair it doesn't cover to one it does.
	routed routedPairs

	// discardedArrivals counts the arrivals dropped at each airport because the
	// scenario lands no traffic there. It is reported the first time an airport
	// turns up so the reason is visible without a line per flight.
	discardedArrivals map[string]int

	// discardedClashes likewise counts the flights dropped for each callsign
	// something else was already flying when they came due.
	discardedClashes map[string]int
}

// noteCallsignClash reports a published flight discarded because its callsign
// was in use, the first time each one turns up.
func (p *publishedTrafficProvider) noteCallsignClash(s *Sim, callsign string, err error) {
	if p.discardedClashes == nil {
		p.discardedClashes = make(map[string]int)
	}
	if p.discardedClashes[callsign] == 0 {
		s.log("Dropping due to callsign clash %v", err)
	}
	p.discardedClashes[callsign]++
}

// newTimetableTrafficProvider prepares a built-in timetable's flights. A
// timetable is a daily cycle without dates, so each flight is anchored to the
// user-selected start time and the day plays out from there; flights in the
// half hour before the start fall in the prespawn window rather than wrapping
// to the end of the day. Published departure times are treated as pushback
// times, so departures spawn at them directly.
func newTimetableTrafficProvider(s *Sim, timetable Timetable, startMinute int,
	arrivalPercentage, departurePercentage int) *publishedTrafficProvider {
	flights := timetableFlights(s.StartTime, timetable, startMinute, arrivalPercentage, departurePercentage)
	return newPublishedTrafficProvider(s, flights, 0)
}

// timetableFlights anchors a timetable's daily cycle to the sim's start time,
// giving each flight it will fly a date and a time like the historical data has.
func timetableFlights(startTime Time, timetable Timetable, startMinute int,
	arrivalPercentage, departurePercentage int) []av.Flight {
	const prespawnMinutes = initialSimSeconds / 60

	var flights []av.Flight
	for _, flight := range timetable.Flights {
		var departure bool
		switch flight.OperationAt(timetable.Airport) {
		case TimetableOperationDeparture:
			if !includeTimetableFlight(flight, departurePercentage) {
				continue
			}
			departure = true

		case TimetableOperationArrival:
			if !includeTimetableFlight(flight, arrivalPercentage) {
				continue
			}

		default:
			continue
		}

		minutes := (flight.PublishedMinute - startMinute + minutesPerTimetableDay) % minutesPerTimetableDay
		if minutes >= minutesPerTimetableDay-prespawnMinutes {
			minutes -= minutesPerTimetableDay
		}
		published := startTime.Add(time.Duration(minutes) * time.Minute).Time().UTC()

		other := flight.Destination
		if !departure {
			other = flight.Origin
		}
		flights = append(flights, av.Flight{
			Airport:      timetable.Airport,
			Callsign:     flight.Callsign,
			Other:        other,
			AircraftType: flight.AircraftType,
			Day:          av.FlightDataDayNumber(published),
			Minute:       published.Hour()*60 + published.Minute(),
			Departure:    departure,
		})
	}

	slices.SortStableFunc(flights, func(a, b av.Flight) int {
		if c := a.Time().Compare(b.Time()); c != 0 {
			return c
		}
		return strings.Compare(a.Callsign, b.Callsign)
	})

	return flights
}

// newHistoricalTrafficProvider prepares the historical flights a sim was
// handed, in time order. Their published times are when they actually took off
// or touched down, so departures spawn flightSpawnLead ahead of them.
func newHistoricalTrafficProvider(s *Sim, flights []av.Flight, arrivalPercentage,
	departurePercentage int) *publishedTrafficProvider {
	return newPublishedTrafficProvider(s,
		includedHistoricalFlights(flights, arrivalPercentage, departurePercentage), flightSpawnLead)
}

// includedHistoricalFlights returns the flights the requested percentages keep.
func includedHistoricalFlights(flights []av.Flight, arrivalPercentage,
	departurePercentage int) []av.Flight {
	kept := make([]av.Flight, 0, len(flights))
	for _, flight := range flights {
		percentage := arrivalPercentage
		if flight.Departure {
			percentage = departurePercentage
		}
		if includeHistoricalFlight(flight, percentage) {
			kept = append(kept, flight)
		}
	}
	return kept
}

const (
	// TrafficCountsWindow is how far past the selected start time the New Sim
	// dialog previews traffic, and TrafficCountsPad is how much further to
	// either side TrafficCounts reaches, so that the dialog's smoothing has
	// data past both edges of what it draws.
	TrafficCountsWindow = 2 * time.Hour
	TrafficCountsPad    = 30 * time.Minute

	// TrafficCountsMinutes is how many one-minute counts TrafficCounts returns.
	TrafficCountsMinutes = int((TrafficCountsWindow + 2*TrafficCountsPad) / time.Minute)
)

// trafficCountsSpan is the span of flight times TrafficCounts counts for a
// preview starting at start. Flights operate on whole minutes, so the span
// lines up with them however precise a start time the user picked.
func trafficCountsSpan(start time.Time) (first, last time.Time) {
	first = start.UTC().Truncate(time.Minute).Add(-TrafficCountsPad)
	return first, first.Add(time.Duration(TrafficCountsMinutes) * time.Minute)
}

// TrafficCounts counts, minute by minute, the departures and arrivals a published traffic source
// will fly over the window starting TrafficCountsPad before start. It filters by airport: an
// airport with no enabled, worked flow flies nothing. Finer than that it doesn't go--at spawn the
// provider resolves each flight against the scenario's routes and drops the few it can't fit, so
// these counts run slightly high, and disabling some but not all of an airport's flows doesn't
// move them. Nor can it count arrivals the scenario has no inbound flow to bring in, which
// requires a running sim, or overflights, which no source publishes--their rate in the launch
// config says all there is to say about them.
//
// historical is the facility's flights on and around the day previewed, however much of them the
// caller has on hand; the window and the scenario's airports are selected from it here.
func TrafficCounts(lc *LaunchConfig, start time.Time,
	historical []av.Flight) (departures, arrivals []uint16, err error) {
	first, last := trafficCountsSpan(start)
	start = first.Add(TrafficCountsPad)

	var flights []av.Flight
	switch lc.TrafficSource {
	case TrafficSourceTimetable:
		catalog, err := LoadAirportTimetables(lc.TimetableAirport)
		if err != nil {
			return nil, nil, err
		}
		timetable, ok := catalog.Find(lc.TimetableAirport, lc.TimetableID)
		if !ok {
			return nil, nil, fmt.Errorf("timetable %q not found for %s", lc.TimetableID, lc.TimetableAirport)
		}
		flights = timetableFlights(NewSimTime(start), timetable, lc.TimetableStartMinute,
			lc.PublishedArrivalPercentage, lc.PublishedDeparturePercentage)

	case TrafficSourceHistorical:
		departureAirports, arrivalAirports := lc.IFRAirports()
		selected := av.SelectFlights(historical, departureAirports, arrivalAirports,
			av.DB.Airlines, first, last)
		flights = includedHistoricalFlights(selected, lc.PublishedArrivalPercentage,
			lc.PublishedDeparturePercentage)

	default:
		return nil, nil, fmt.Errorf("%s traffic comes from the launch config's rates, "+
			"so there is nothing to preview", lc.TrafficSource)
	}

	flights, _ = dropRotorcraft(flights)
	flights, _ = dropRepeatedRecords(flights)
	flights, _ = dropReturnedLegs(flights)

	departures = make([]uint16, TrafficCountsMinutes)
	arrivals = make([]uint16, TrafficCountsMinutes)
	for _, flight := range flights {
		minute := int(flight.Time().Sub(first) / time.Minute)
		if minute < 0 || minute >= TrafficCountsMinutes {
			continue
		}
		if flight.Departure {
			if lc.departsAirport(flight.Airport) {
				departures[minute]++
			}
		} else if lc.landsAirport(flight.Airport) {
			arrivals[minute]++
		}
	}
	return departures, arrivals, nil
}

// departsAirport reports whether any of an airport's departure flows are both
// enabled and a human's to work, and landsAirport whether any inbound flow into
// it is. Published traffic leaves from and lands at the flows the user leaves
// on, so an airport with all of them off flies nothing; and traffic the
// scenario flies purely for realism is nothing the user will see, so a preview
// of what they are in for leaves it out.
func (lc *LaunchConfig) departsAirport(airport string) bool {
	for runway, categories := range lc.DepartureEnabled[airport] {
		for category, enabled := range categories {
			if enabled && !lc.DepartureIsBackground(airport, runway, category) {
				return true
			}
		}
	}
	return false
}

func (lc *LaunchConfig) landsAirport(airport string) bool {
	for flow, airports := range lc.InboundFlowEnabled {
		if airports[airport] && !lc.InboundFlowIsBackground(flow, airport) {
			return true
		}
	}
	return false
}

// MarkBackgroundTraffic records which of a scenario's traffic no human
// controller ever works. A scenario often flies a neighboring airport's
// operations so that the scope looks like the real thing, handled start to
// finish by virtual controllers; a departure position's scenario may land its
// airport the same way. That traffic is not what someone choosing a scenario is
// signing up for, so it is marked once, when the scenario catalogs are built,
// and left out of what we report they will see.
//
// It errs towards saying traffic is worked: leaving it in overstates the
// workload, whereas wrongly calling it background hides traffic the user will
// actually have to work.
func MarkBackgroundTraffic(airports map[string]*av.Airport, flows map[string]*av.InboundFlow,
	cc *ControllerConfiguration, controlPositions map[TCP]*av.Controller, lc *LaunchConfig) {
	bc := backgroundClassifier{cc: cc, controlPositions: controlPositions}

	for airport, runwayRates := range lc.DepartureRates {
		ap, ok := airports[airport]
		if !ok {
			continue
		}
		for runway, categoryRates := range runwayRates {
			for category := range categoryRates {
				if bc.departureIsBackground(ap, runway, category) {
					markDepartureBackground(lc, airport, runway, category)
				}
			}
		}
	}

	for flow, airportRates := range lc.InboundFlowRates {
		inboundFlow, ok := flows[flow]
		if !ok {
			continue
		}
		for airport := range airportRates {
			if bc.inboundIsBackground(inboundFlow, airport, cc.InboundAssignments[flow]) {
				markInboundBackground(lc, flow, airport)
			}
		}
	}
}

func markDepartureBackground(lc *LaunchConfig, airport string, runway av.RunwayID, category string) {
	if lc.DepartureBackground == nil {
		lc.DepartureBackground = make(map[string]map[av.RunwayID]map[string]bool)
	}
	if lc.DepartureBackground[airport] == nil {
		lc.DepartureBackground[airport] = make(map[av.RunwayID]map[string]bool)
	}
	if lc.DepartureBackground[airport][runway] == nil {
		lc.DepartureBackground[airport][runway] = make(map[string]bool)
	}
	lc.DepartureBackground[airport][runway][category] = true
}

func markInboundBackground(lc *LaunchConfig, flow, airport string) {
	if lc.InboundFlowBackground == nil {
		lc.InboundFlowBackground = make(map[string]map[string]bool)
	}
	if lc.InboundFlowBackground[flow] == nil {
		lc.InboundFlowBackground[flow] = make(map[string]bool)
	}
	lc.InboundFlowBackground[flow][airport] = true
}

// backgroundClassifier judges a scenario's traffic against its default
// consolidation and the facility's defined positions.
type backgroundClassifier struct {
	cc               *ControllerConfiguration
	controlPositions map[TCP]*av.Controller
}

// isHuman and isVirtual are not each other's negation: a position the facility
// doesn't define is neither, and the sim treats such a reference as no reason
// to keep a departure from its human controller. Both mirror
// Sim.isVirtualController.
func (bc backgroundClassifier) isHuman(pos av.ControlPosition) bool {
	return pos != "" && bc.cc.DefaultConsolidation.IsHumanPosition(pos)
}

func (bc backgroundClassifier) isVirtual(pos av.ControlPosition) bool {
	if _, ok := bc.controlPositions[TCP(pos)]; !ok {
		return false
	}
	return !bc.cc.DefaultConsolidation.IsHumanPosition(pos)
}

func (bc backgroundClassifier) reachesHuman(wps av.WaypointArray, atHandoff av.ControlPosition) bool {
	if wps.HasHumanHandoff() && bc.isHuman(atHandoff) {
		return true
	}
	return slices.ContainsFunc(wps.HandoffControllers(), bc.isHuman)
}

// departureIsBackground reports whether every route a departure flow may fly
// stays with virtual controllers the whole way. The category selects the exits
// the same way spawnDepartures does; a flow with no route to judge is left
// alone.
func (bc backgroundClassifier) departureIsBackground(ap *av.Airport, runway av.RunwayID, category string) bool {
	judged := 0
	for exit, route := range ap.DepartureRoutes[runway] {
		if category != "" && category != ap.ExitCategories[exit] {
			continue
		}
		judged++

		// Mirrors assignDepartureController: the departure is a human's from the
		// start unless the airport or the route gives it to a virtual controller.
		virtualStart := (ap.DepartureController != "" && bc.isVirtual(ap.DepartureController)) ||
			(route.DepartureController != "" && bc.isVirtual(route.DepartureController))
		if !virtualStart || bc.reachesHuman(route.Waypoints, route.HandoffController) {
			return false
		}
	}
	return judged > 0
}

// inboundIsBackground reports whether everything a flow brings to an airport
// stays with virtual controllers; airport is "overflights" for the flow's
// overflights, which serve no airport in particular.
func (bc backgroundClassifier) inboundIsBackground(flow *av.InboundFlow, airport string, assignment TCP) bool {
	handoff := av.ControlPosition(assignment)

	if airport == "overflights" {
		for _, of := range flow.Overflights {
			if bc.isHuman(of.InitialController) || bc.reachesHuman(of.Waypoints, handoff) {
				return false
			}
		}
		return len(flow.Overflights) > 0
	}

	judged := 0
	for _, arr := range flow.Arrivals {
		if !slices.Contains(arr.ServedAirports(), airport) {
			continue
		}
		judged++

		if bc.isHuman(arr.InitialController) || bc.reachesHuman(arr.Waypoints, handoff) {
			return false
		}
		for _, runwayWaypoints := range arr.RunwayWaypoints[airport] {
			if bc.reachesHuman(runwayWaypoints, handoff) {
				return false
			}
		}
	}
	return judged > 0
}

// dropRotorcraft removes the flights there is no way to fly. Helicopters are a
// fair share of the traffic at some facilities and the data records them like
// anything else, but everything Vice flies is flown as a fixed-wing aircraft,
// so they are left on the ground rather than flown as one.
func dropRotorcraft(flights []av.Flight) ([]av.Flight, int) {
	kept := make([]av.Flight, 0, len(flights))
	dropped := 0
	for _, flight := range flights {
		if engineTypeFor(flight.AircraftType) == "H" {
			dropped++
			continue
		}
		kept = append(kept, flight)
	}
	return kept, dropped
}

// dropRepeatedRecords removes a record of an operation that is already there.
// The historical data is derived from ADS-B tracks, and a flight whose track
// came in pieces is recorded once per piece, minutes apart and sometimes
// disagreeing about the airport at the other end. Only identical records are
// merged when the data is written, so the rest arrive here and would otherwise
// spawn as several aircraft sharing a callsign.
func dropRepeatedRecords(flights []av.Flight) ([]av.Flight, int) {
	// operation identifies what a record says the aircraft did, without the time
	// or the far-end airport: those are what the repeats disagree about.
	type operation struct {
		callsign, airport string
		departure         bool
	}

	last := make(map[operation]time.Time)
	kept := make([]av.Flight, 0, len(flights))
	dropped := 0
	for _, flight := range flights {
		key := operation{flight.Callsign, flight.Airport, flight.Departure}
		if previous, ok := last[key]; ok &&
			!flight.Time().After(previous.Add(repeatedRecordWindow)) {
			dropped++
			continue
		}
		last[key] = flight.Time()
		kept = append(kept, flight)
	}
	return kept, dropped
}

// dropReturnedLegs removes the arrival record of a flight between two airports
// the facility works. Such a flight is recorded twice, once at each end: a
// facility's flight data has to stand on its own, so a leg from one of its
// airports to another appears as a departure at the first and an arrival at the
// second. Flying both spawns two aircraft sharing a callsign, and since the leg
// is short the arrival comes due while the departure is still at the gate. The
// departure is the half flown: it starts where the aircraft really was.
//
// A departure is matched at most once, so an aircraft that flies from one of the
// facility's airports to another and back has each of its legs recognized rather
// than the first departure standing for both.
func dropReturnedLegs(flights []av.Flight) ([]av.Flight, int) {
	// leg identifies a flight by callsign and where it flew between, so that an
	// arrival can find the departure that recorded the same leg at its far end.
	type leg struct {
		callsign, from, to string
	}

	pending := make(map[leg][]time.Time)
	for _, flight := range flights {
		if flight.Departure {
			key := leg{flight.Callsign, flight.Airport, flight.Other}
			pending[key] = append(pending[key], flight.Time())
		}
	}

	kept := make([]av.Flight, 0, len(flights))
	dropped := 0
	for _, flight := range flights {
		if !flight.Departure {
			key := leg{flight.Callsign, flight.Other, flight.Airport}
			// Flights come in time order, so the oldest unmatched departure of
			// this leg is the one that ends here.
			if times := pending[key]; len(times) > 0 &&
				!flight.Time().Before(times[0]) &&
				!flight.Time().After(times[0].Add(intraFacilityLegWindow)) {
				pending[key] = times[1:]
				dropped++
				continue
			}
		}
		kept = append(kept, flight)
	}
	return kept, dropped
}

// newPublishedTrafficProvider builds the spawn queues from flights in time
// order; departures spawn departureSpawnLead ahead of their published times and
// arrivals flightSpawnLead ahead of theirs. Arrivals the scenario has no way to
// fly stay in the queue with the reason, to be reported when their spawn times
// come around.
func newPublishedTrafficProvider(s *Sim, flights []av.Flight,
	departureSpawnLead time.Duration) *publishedTrafficProvider {
	p := &publishedTrafficProvider{routed: makeRoutedPairs()}

	flights, rotorcraft := dropRotorcraft(flights)
	// One real flight must enter the sim once, however many records it left in
	// the data: the second aircraft to reach the runway under a callsign is
	// turned away, and there is no second callsign to give it.
	flights, repeated := dropRepeatedRecords(flights)
	flights, returned := dropReturnedLegs(flights)

	missed, unplaceable := 0, 0
	earliest := s.StartTime.Add(-PrespawnDuration)
	if s.State.SimTime.After(earliest) {
		earliest = s.State.SimTime
	}

	for _, flight := range flights {
		lead := flightSpawnLead
		if flight.Departure {
			lead = departureSpawnLead
		}
		spawn := NewSimTime(flight.Time().Add(-lead))
		// A flight whose spawn time has already gone by has been missed.
		// Prespawn rewinds the clock before the selected start time, so compare
		// against where the clock actually begins. Releasing a backlog all at
		// once would swamp the runways rather than fill them.
		if spawn.Before(earliest) {
			missed++
			continue
		}

		if flight.Departure {
			p.departures = append(p.departures, publishedFlight{flight: flight, spawn: spawn})
			continue
		}

		placement, err := s.placeArrival(flight.Airport, flight.Other, flight.AircraftType,
			p.routed)
		if err != nil {
			unplaceable++
		}
		p.arrivals = append(p.arrivals, publishedFlight{flight: flight, spawn: spawn,
			placement: placement, dropReason: err})
	}

	if len(flights) > 0 {
		lead := flightSpawnLead
		if flights[0].Departure {
			lead = departureSpawnLead
		}
		s.log("Published traffic: clock starts %s, first flight %s spawns %s",
			earliest.Time().Format("2006-01-02 15:04:05Z"),
			flights[0].Time().Format("2006-01-02 15:04:05Z"),
			flights[0].Time().Add(-lead).Format("2006-01-02 15:04:05Z"))
	}
	summary := fmt.Sprintf("Published traffic: %d departures, %d arrivals to fly",
		len(p.departures), len(p.arrivals)-unplaceable)
	if unplaceable > 0 {
		summary += fmt.Sprintf("; %d arrivals have no route to fly and will be dropped", unplaceable)
	}
	if missed > 0 {
		summary += fmt.Sprintf("; skipped %d flights already due before the clock starts", missed)
	}
	if rotorcraft > 0 {
		summary += fmt.Sprintf("; left out %d helicopter flights", rotorcraft)
	}
	if repeated > 0 {
		summary += fmt.Sprintf("; merged %d flights the data records more than once", repeated)
	}
	if returned > 0 {
		summary += fmt.Sprintf("; %d arrivals from another of the facility's airports are flying as departures", returned)
	}
	s.log("%s", summary)

	return p
}

// placeArrival decides how a published flight into arrivalAirport from origin
// is flown: the inbound flow and arrival that carry it, the route it files, and
// the airport standing in for its origin when neither the scenario nor the
// route database covers where it really came from.
func (s *Sim) placeArrival(arrivalAirport, origin, aircraftType string,
	routed routedPairs) (arrivalPlacement, error) {
	arrivalAirport = normalizeAirportCode(arrivalAirport)
	origin = normalizeAirportCode(origin)

	candidates := s.candidateArrivals(arrivalAirport)
	if len(candidates) == 0 {
		return arrivalPlacement{}, errNoPlausibleArrival
	}
	if len(suitableArrivals(candidates, aircraftType)) == 0 {
		return arrivalPlacement{}, errNoSuitableArrival
	}

	hour, hourKnown := s.localHour(arrivalAirport)
	scenarioRoutes := func(from string) []string {
		if ap, ok := s.State.Airports[arrivalAirport]; ok {
			return ap.TrafficRoutes.Arrivals[from].Routes(aircraftType)
		}
		return nil
	}
	scrapedRoutes := func(from string) []string {
		ordered := orderScrapedRoutes(av.DB.ScrapedRoutesBetween(from, arrivalAirport),
			aircraftType, hour, hourKnown)
		return util.MapSlice(ordered, func(r av.ScrapedRoute) string { return r.Route })
	}
	faaRoutes := func(from string) []string {
		eligible := eligibleAirportPairRoutes(av.DB.RoutesBetween(from, arrivalAirport),
			engineTypeFor(aircraftType))
		return util.MapSlice(eligible, func(r av.AirportPairRoute) string { return r.Route })
	}

	// The scenario says in so many words how traffic from this origin comes in,
	// or failing that the scraped filings and the route database say how the
	// pair is really flown. Either way the route is the flight's own, so
	// failing to fit it--its STAR isn't active in this configuration--drops the
	// flight rather than shoehorning it onto a flow it never flies: a scenario
	// working one gate of an airport shouldn't be handed every flight bound for
	// the others.
	if routes := scenarioRoutes(origin); len(routes) > 0 {
		c, route, err := matchArrivalRoutes(candidates, aircraftType, routes, arrivalAirport, origin)
		if err != nil {
			return arrivalPlacement{}, err
		}
		return c.placement(route, "", "scenario route"), nil
	}
	if scraped, faa := scrapedRoutes(origin), faaRoutes(origin); len(scraped)+len(faa) > 0 {
		c, route, err := matchArrivalRoutes(candidates, aircraftType,
			slices.Concat(scraped, faa), arrivalAirport, origin)
		if err != nil {
			return arrivalPlacement{}, err
		}
		how := util.Select(slices.Contains(scraped, route), "scraped route", "faa route")
		return c.placement(route, "", how), nil
	}

	// Neither knows this origin, so fly the flight the way the nearest airport
	// one of them does know is flown: from JFK, Norfolk stands in for Kill Devil
	// Hills. Real traffic comes from far more airports than either source
	// covers, and arriving as one's neighbors do beats not arriving at all. The
	// flight files its own route rather than the substitute's, which starts
	// somewhere it has never been.
	pool := slices.Clone(routed.originsByDestination[arrivalAirport])
	if ap, ok := s.State.Airports[arrivalAirport]; ok {
		for _, from := range util.SortedMapKeys(ap.TrafficRoutes.Arrivals) {
			if len(scenarioRoutes(from)) > 0 {
				pool = append(pool, from)
			}
		}
	}
	for _, substitute := range substituteAirports(arrivalAirport, origin, pool,
		publishedArrivalMaxHeadingDifference) {
		routes := slices.Concat(scenarioRoutes(substitute), scrapedRoutes(substitute), faaRoutes(substitute))
		if c, _, err := matchArrivalRoutes(candidates, aircraftType, routes, arrivalAirport, substitute); err == nil {
			return c.placement("", substitute, "nearest route, from "+substitute), nil
		}
	}

	// Nothing is routed anywhere near it; the gate nearest the great-circle arc
	// the flight actually flies is all that is left to go on.
	if c, ok := arrivalNearestArc(suitableArrivals(candidates, aircraftType),
		arrivalAirport, origin); ok {
		return c.placement("", "", "great-circle gate"), nil
	}
	return arrivalPlacement{}, errNoPlausibleArrival
}

func (c candidateArrival) placement(filedRoute, substitute, how string) arrivalPlacement {
	return arrivalPlacement{group: c.group, index: c.index, filedRoute: filedRoute,
		substitute: substitute, how: how}
}

// publishedSubstituteFraction is how much of the trip an airport drawn from the
// route database may be away from the airport it stands in for, at either end.
const publishedSubstituteFraction = 0.5

// substituteAirports returns the airports in the pool that could stand in for
// the far end of a trip from base, nearest the real one first. A stand-in must
// lie the same general way from base and be a small enough part of the trip to
// be a neighbor of the real airport rather than merely the closest thing along
// the way: Bangor is the closest airport with a JFK route to Zurich, but
// traffic from Zurich doesn't arrive the way Bangor's does.
func substituteAirports(base, real string, pool []string, maxHeadingDifference float32) []string {
	baseAirport, baseOK := av.DB.Airports[base]
	realAirport, realOK := av.DB.Airports[real]
	if !baseOK || !realOK {
		return nil
	}
	toReal := math.GreatCircleHeading(baseAirport.Location, realAirport.Location)
	limit := publishedSubstituteFraction * math.NMDistance2LL(baseAirport.Location, realAirport.Location)

	type candidate struct {
		id       string
		distance float32
	}
	var candidates []candidate
	seen := make(map[string]bool)
	for _, id := range pool {
		if seen[id] || id == real || id == base {
			continue
		}
		seen[id] = true
		ap, ok := av.DB.Airports[id]
		if !ok {
			continue
		}
		distance := math.NMDistance2LL(ap.Location, realAirport.Location)
		if distance > limit {
			continue
		}
		if math.HeadingDifference(math.GreatCircleHeading(baseAirport.Location, ap.Location),
			toReal) > maxHeadingDifference {
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
	return util.MapSlice(candidates, func(c candidate) string { return c.id })
}

// routedPairs indexes the route databases both ways, so that finding a
// stand-in for a city pair they don't cover doesn't walk the whole thing per
// flight.
type routedPairs struct {
	originsByDestination map[string][]string
	destinationsByOrigin map[string][]string
}

func makeRoutedPairs() routedPairs {
	routed := routedPairs{
		originsByDestination: make(map[string][]string),
		destinationsByOrigin: make(map[string][]string),
	}
	add := func(pair av.AirportPair) {
		from, to := normalizeAirportCode(pair.From), normalizeAirportCode(pair.To)
		routed.originsByDestination[to] = append(routed.originsByDestination[to], from)
		routed.destinationsByOrigin[from] = append(routed.destinationsByOrigin[from], to)
	}
	for pair := range av.DB.AirportPairRoutes {
		add(pair)
	}
	for pair := range av.DB.ScrapedRoutes {
		add(pair)
	}
	return routed
}

// localHour returns the hour of day at the airport at the sim's current time,
// if the airport's time zone is known.
func (s *Sim) localHour(airport string) (int, bool) {
	loc, ok := av.DB.AirportTimeZones[airport]
	if !ok {
		return 0, false
	}
	return s.State.SimTime.Time().In(loc).Hour(), true
}

// orderScrapedRoutes sorts a pair's scraped routes for one flight: first
// routes flown by this class of aircraft, then whichever was observed closest
// to this hour of the day, then ones filed at altitudes the aircraft can
// reach, most-filed first among equals. A noise-abatement route seen only at
// night thereby wins at night and loses by day. What the aircraft is outranks
// when it flies: hour coverage is spotty for rarely filed routes, but a route
// only ever seen flown by props is saying something about jets. The
// observations only ever reorder--a route unobserved at some hour may still
// be all a flight has.
//
// A scraped route's classes are a record of what happened to be seen, not a
// directive like the classes in a scenario's "traffic_routes". The class the
// aircraft is matched against widens until something was observed flying it:
// a heavy jet whose pair has no heavy-observed route follows the routes the
// other jets file, a prop with no prop-observed route follows the
// turboprops', and when nothing kindred was seen at all, whatever was will
// have to do.
func orderScrapedRoutes(routes []av.ScrapedRoute, aircraftType string, hour int,
	hourKnown bool) []av.ScrapedRoute {
	class := av.AircraftClassOf(aircraftType)
	family := av.AircraftClassProp | av.AircraftClassTurboprop
	if class&(av.AircraftClassHeavyJet|av.AircraftClassNonheavyJet) != 0 {
		family = av.AircraftClassHeavyJet | av.AircraftClassNonheavyJet
	}
	observed := func(c av.AircraftClass) bool {
		return slices.ContainsFunc(routes, func(r av.ScrapedRoute) bool { return r.Aircraft&c != 0 })
	}
	if class != 0 && !observed(class) {
		class = util.Select(observed(family), family, 0)
	}

	perf, perfOK := av.DB.AircraftPerformance[aircraftType]
	rank := func(r av.ScrapedRoute) [3]int {
		var n [3]int
		if class != 0 && r.Aircraft != 0 && r.Aircraft&class == 0 {
			n[0] = 1
		}
		if hourKnown {
			n[1] = hourDistance(r.Hours, hour)
		}
		if perfOK && float32(r.MinAltitude) > perf.Ceiling {
			n[2] = 1
		}
		return n
	}

	ordered := slices.Clone(routes)
	slices.SortStableFunc(ordered, func(a, b av.ScrapedRoute) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return slices.Compare(ra[:], rb[:])
		}
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return strings.Compare(a.Route, b.Route)
	})
	return ordered
}

// hourDistance is how far the hour lies, around the clock, from the route's
// nearest observed hour; zero when no hours were recorded at all, since
// nothing argues against the route then.
func hourDistance(h av.HourRanges, hour int) int {
	if h == 0 {
		return 0
	}
	for d := range 13 {
		if h.Contains(hour+d) || h.Contains(hour-d+24) {
			return d
		}
	}
	return 12
}

func (p *publishedTrafficProvider) createIFRDeparture(s *Sim, airport string,
	runway av.RunwayID) (*Aircraft, time.Duration, error) {
	// Find the next departure from this airport; the queue holds every airport
	// the facility works and each runway asks for its own.
	index := -1
	for i := range p.departures {
		if p.departures[i].flight.Airport == airport {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, idleDelay, nil
	}

	published := p.departures[index]
	if s.State.SimTime.Before(published.spawn) {
		return nil, published.spawn.Sub(s.State.SimTime), nil
	}

	// The enabled flows say which categories this runway is launching, and
	// nothing more: how many aircraft go where comes from the flights
	// themselves.
	var categories []string
	for category, enabled := range s.State.LaunchConfig.DepartureEnabled[airport][runway] {
		if enabled {
			categories = append(categories, category)
		}
	}
	if len(categories) == 0 {
		// This runway isn't launching anything in this scenario. Leave the
		// flight for another runway at the airport and check back rather than
		// holding it, in case a flow is enabled while the sim runs.
		return nil, time.Minute, nil
	}
	slices.Sort(categories)

	ac, err := s.createPublishedIFRDepartureNoLock(published.flight, airport, runway,
		categories, p.routed.destinationsByOrigin)
	p.departures = append(p.departures[:index], p.departures[index+1:]...)

	if errors.Is(err, errNoScenarioRoute) {
		// The scenario doesn't model where this flight went, so it isn't
		// launched. The queue is per-airport, so the flight isn't offered to
		// another runway; scenarios run one departure configuration per
		// airport, so nothing is lost.
		s.log("%s: dropped departure %s->%s (%s): %v", published.flight.Callsign,
			airport, published.flight.Other, published.flight.AircraftType, err)
		return nil, p.departureDelay(s, airport), nil
	}
	if errors.Is(err, errCallsignInUse) {
		p.noteCallsignClash(s, published.flight.Callsign, err)
		return nil, p.departureDelay(s, airport), nil
	}
	return ac, p.departureDelay(s, airport), err
}

// departureDelay is how long until the next departure from an airport is due.
func (p *publishedTrafficProvider) departureDelay(s *Sim, airport string) time.Duration {
	for i := range p.departures {
		if p.departures[i].flight.Airport == airport {
			return max(time.Millisecond, p.departures[i].spawn.Sub(s.State.SimTime))
		}
	}
	return idleDelay
}

func (p *publishedTrafficProvider) createInbound(s *Sim, group string,
	rates map[string]float32, _ bool) (*Aircraft, time.Duration, error) {
	index := p.nextArrivalFor(s, group, rates)
	if index < 0 {
		// Nothing left for this flow; come back if unplaceable arrivals still
		// wait to be reported.
		return nil, p.pendingDropDelay(s), nil
	}

	published := p.arrivals[index]
	if s.State.SimTime.Before(published.spawn) {
		return nil, published.spawn.Sub(s.State.SimTime), nil
	}

	ac, err := s.createPublishedArrivalNoLock(published.flight, published)
	if errors.Is(err, errPublishedArrivalSpawnConflict) {
		// Leave this arrival where it is and retry shortly, so that it keeps
		// its place while the preceding one moves clear of the spawn point.
		return nil, 5 * time.Second, nil
	}
	p.arrivals = append(p.arrivals[:index], p.arrivals[index+1:]...)

	if errors.Is(err, errCallsignInUse) {
		p.noteCallsignClash(s, published.flight.Callsign, err)
		return nil, p.arrivalDelay(s, group, rates), nil
	}
	return ac, p.arrivalDelay(s, group, rates), err
}

// nextArrivalFor returns the index of the next arrival this inbound flow should
// create, or -1 if it has none left. Each arrival belongs to exactly one flow,
// so a flow steps over the others' rather than waiting behind them. Arrivals the
// scenario can't fly are discarded here rather than held: leaving one at the
// head of the queue would stall every arrival behind it.
func (p *publishedTrafficProvider) nextArrivalFor(s *Sim, group string,
	rates map[string]float32) int {
	for i := 0; i < len(p.arrivals); {
		published := p.arrivals[i]

		if published.dropReason != nil {
			// No placement was found when the queue was built; report the
			// drop when the flight would have spawned, whichever flow's scan
			// gets there first.
			if s.State.SimTime.Before(published.spawn) {
				i++
				continue
			}
			s.log("%s: dropped arrival %s->%s (%s): %v", published.flight.Callsign,
				published.flight.Other, published.flight.Airport, published.flight.AircraftType,
				published.dropReason)
			p.arrivals = append(p.arrivals[:i], p.arrivals[i+1:]...)
			continue
		}

		if published.placement.group != group {
			i++
			continue
		}

		// The rates map is filtered upstream to hold only airports whose
		// arrivals are in automatic mode.
		_, automatic := rates[published.flight.Airport]
		if !automatic || !s.State.LaunchConfig.InboundFlowEnabled[group][published.flight.Airport] {
			// This scenario isn't landing traffic at that airport.
			airport := published.flight.Airport
			if p.discardedArrivals == nil {
				p.discardedArrivals = make(map[string]int)
			}
			if p.discardedArrivals[airport] == 0 {
				s.log("Discarding published arrivals at %s: %s lands no traffic there",
					airport, group)
			}
			p.discardedArrivals[airport]++
			p.arrivals = append(p.arrivals[:i], p.arrivals[i+1:]...)
			continue
		}
		return i
	}
	return -1
}

func (p *publishedTrafficProvider) arrivalDelay(s *Sim, group string,
	rates map[string]float32) time.Duration {
	if index := p.nextArrivalFor(s, group, rates); index >= 0 {
		return max(time.Millisecond, p.arrivals[index].spawn.Sub(s.State.SimTime))
	}
	return p.pendingDropDelay(s)
}

// pendingDropDelay is how long until the next arrival that couldn't be placed
// is due to be reported, so that an otherwise idle flow still comes back to
// say so.
func (p *publishedTrafficProvider) pendingDropDelay(s *Sim) time.Duration {
	delay := idleDelay
	for i := range p.arrivals {
		if p.arrivals[i].dropReason != nil {
			delay = min(delay, max(time.Millisecond, p.arrivals[i].spawn.Sub(s.State.SimTime)))
		}
	}
	return delay
}

type errorTrafficProvider struct{ err error }

func (p errorTrafficProvider) createIFRDeparture(_ *Sim, _ string, _ av.RunwayID) (*Aircraft, time.Duration, error) {
	return nil, time.Minute, p.err
}
func (p errorTrafficProvider) createInbound(_ *Sim, _ string,
	_ map[string]float32, _ bool) (*Aircraft, time.Duration, error) {
	return nil, time.Minute, p.err
}

func (s *Sim) activeTrafficProvider() trafficProvider {
	if s.trafficProvider != nil {
		return s.trafficProvider
	}

	lc := &s.State.LaunchConfig
	switch lc.TrafficSource {
	case TrafficSourceHistorical:
		if len(s.historicalFlights) == 0 {
			s.log("Traffic source: historical, but no flights were found for %s from %s",
				s.State.Facility, s.StartTime.Time().Format("2006-01-02 15:04Z"))
			s.trafficProvider = errorTrafficProvider{
				err: fmt.Errorf("no historical flight data for %s at this time", s.State.Facility)}
		} else {
			s.log("Traffic source: historical, %d flights found for %s from %s",
				len(s.historicalFlights), s.State.Facility,
				s.StartTime.Time().Format("2006-01-02 15:04Z"))
			s.trafficProvider = newHistoricalTrafficProvider(s, s.historicalFlights,
				lc.PublishedArrivalPercentage, lc.PublishedDeparturePercentage)
		}

	case TrafficSourceTimetable:
		catalog, err := LoadAirportTimetables(lc.TimetableAirport)
		if err != nil {
			s.trafficProvider = errorTrafficProvider{err: err}
			return s.trafficProvider
		}
		timetable, ok := catalog.Find(lc.TimetableAirport, lc.TimetableID)
		if !ok {
			s.trafficProvider = errorTrafficProvider{err: fmt.Errorf("timetable %q not found for %s",
				lc.TimetableID, lc.TimetableAirport)}
			return s.trafficProvider
		}
		s.log("Traffic source: timetable %q for %s", timetable.Name, timetable.Airport)
		s.trafficProvider = newTimetableTrafficProvider(s, timetable, lc.TimetableStartMinute,
			lc.PublishedArrivalPercentage, lc.PublishedDeparturePercentage)

	default:
		s.log("Traffic source: scenario")
		s.trafficProvider = scenarioTrafficProvider{}
	}
	return s.trafficProvider
}
