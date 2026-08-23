// sim/traffic.go
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

	// historicalFlightWindow is how much of the sim's clock a sim is launched
	// with flight data for. A sim running longer than this runs out of
	// historical traffic.
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

// publishedTrafficTime is when a flight published at t operates in a sim that
// started at start: its offset from the start, drawn in by the rate scale. That
// is how a rate scale of two flies twice the traffic--every flight in the data
// still flies, the sim just reads through the day twice as fast.
func publishedTrafficTime(t, start time.Time, scale float32) time.Time {
	return start.Add(time.Duration(float64(t.Sub(start)) / float64(scale)))
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
			if slices.Contains(arrivals[i].Airports, arrivalAirport) {
				candidates = append(candidates, candidateArrival{group, i, &arrivals[i]})
			}
		}
	}
	return candidates
}

// timetableFlights anchors a timetable's daily cycle to the sim's start time,
// giving each flight it will fly a date and a time like the historical data has.
// The times are the timetable's own; the rate scale draws them in when the
// flights are queued, so it only comes in here to say how much of the cycle's
// tail wraps around into the prespawn window.
func timetableFlights(startTime Time, timetable Timetable, lc *LaunchConfig) []av.Flight {
	const prespawnMinutes = initialSimSeconds / 60
	departureScale := math.Clamp(lc.PublishedDepartureRateScale, 0, MaxPublishedRateScale)
	arrivalScale := math.Clamp(lc.PublishedArrivalRateScale, 0, MaxPublishedRateScale)

	var flights []av.Flight
	for _, flight := range timetable.Flights {
		operation := flight.OperationAt(timetable.Airport)
		if operation == TimetableOperationUnknown {
			continue
		}
		departure := operation == TimetableOperationDeparture
		scale := arrivalScale
		if departure {
			scale = departureScale
		}

		minutes := (flight.PublishedMinute - lc.TimetableStartMinute + minutesPerTimetableDay) % minutesPerTimetableDay
		if minutes >= minutesPerTimetableDay-int(prespawnMinutes*scale) {
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

// trafficCountsSpan is the span of the sim's clock TrafficCounts counts over
// for a preview starting at start, and first is the minute its counts begin at.
// Flights operate on whole minutes, so the span lines up with them however
// precise a start time the user picked.
func trafficCountsSpan(start time.Time) (first, last time.Time) {
	first = start.UTC().Truncate(time.Minute).Add(-TrafficCountsPad)
	return first, first.Add(time.Duration(TrafficCountsMinutes) * time.Minute)
}

// TrafficCounts counts, minute by minute, the departures and arrivals a published traffic source
// will fly over the window starting TrafficCountsPad before start. The minutes are the sim's, so
// the rate scales apply here as they do in the sim: flying at twice the rate reads through twice as
// much of the data over the same stretch of clock. It filters by airport: an
// airport with no enabled, worked flow flies nothing. Finer than that it doesn't go--at spawn the
// provider resolves each flight against the scenario's routes and drops the few it can't fit, so
// these counts run slightly high, and disabling some but not all of an airport's flows doesn't
// move them. Nor can it count arrivals the scenario has no inbound flow to bring in, which
// requires a running sim, or overflights, which no source publishes--their rate in the launch
// config says all there is to say about them.
//
// operations is the same traffic broken out by airport, departures and arrivals together, so a
// caller can see where the traffic goes as well as when it comes.
//
// historical is the facility's flights on and around the day previewed, however much of them the
// caller has on hand; the window and the scenario's airports are selected from it here.
func TrafficCounts(lc *LaunchConfig, start time.Time,
	historical []av.Flight) (departures, arrivals []uint16, operations map[string]int, err error) {
	first, last := trafficCountsSpan(start)
	start = first.Add(TrafficCountsPad)
	departureScale := math.Clamp(lc.PublishedDepartureRateScale, 0, MaxPublishedRateScale)
	arrivalScale := math.Clamp(lc.PublishedArrivalRateScale, 0, MaxPublishedRateScale)

	var flights []av.Flight
	switch lc.TrafficSource {
	case TrafficSourceTimetable:
		catalog, err := LoadAirportTimetables(lc.TimetableAirport)
		if err != nil {
			return nil, nil, nil, err
		}
		timetable, ok := catalog.Find(lc.TimetableAirport, lc.TimetableID)
		if !ok {
			return nil, nil, nil, fmt.Errorf("timetable %q not found for %s", lc.TimetableID, lc.TimetableAirport)
		}
		flights = timetableFlights(NewSimTime(start), timetable, lc)

	case TrafficSourceHistorical:
		// The window is on the sim's clock, so the flights that land in it are
		// the ones the faster of the two scales reaches: the counts and the
		// cleanup filters that follow all work from the times in the data.
		scale := max(departureScale, arrivalScale)
		departureAirports, arrivalAirports := lc.IFRAirports()
		flights = av.SelectFlights(historical, departureAirports, arrivalAirports, av.DB.Airlines,
			start.Add(time.Duration(float64(first.Sub(start))*float64(scale))),
			start.Add(time.Duration(float64(last.Sub(start))*float64(scale))))

	default:
		return nil, nil, nil, fmt.Errorf("%s traffic comes from the launch config's rates, "+
			"so there is nothing to preview", lc.TrafficSource)
	}

	flights, _ = dropRotorcraft(flights)
	flights, _ = dropRepeatedRecords(flights)
	flights, _ = dropReturnedLegs(flights)

	departures = make([]uint16, TrafficCountsMinutes)
	arrivals = make([]uint16, TrafficCountsMinutes)
	operations = make(map[string]int)
	for _, flight := range flights {
		scale := arrivalScale
		if flight.Departure {
			scale = departureScale
		}
		if scale == 0 {
			continue
		}
		minute := int(publishedTrafficTime(flight.Time(), start, scale).Sub(first) / time.Minute)
		if minute < 0 || minute >= TrafficCountsMinutes {
			continue
		}
		if flight.Departure {
			if lc.departsAirport(flight.Airport) {
				departures[minute]++
				operations[flight.Airport]++
			}
		} else if lc.landsAirport(flight.Airport) {
			arrivals[minute]++
			operations[flight.Airport]++
		}
	}
	return departures, arrivals, operations, nil
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
	for exit, routes := range ap.DepartureRoutes[runway] {
		if category != "" && category != ap.ExitCategories[exit] {
			continue
		}

		for _, route := range routes {
			judged++

			// Mirrors assignDepartureController: the departure is a human's from the
			// start unless the airport or the route gives it to a virtual controller.
			virtualStart := (ap.DepartureController != "" && bc.isVirtual(ap.DepartureController)) ||
				(route.DepartureController != "" && bc.isVirtual(route.DepartureController))
			if !virtualStart || bc.reachesHuman(route.Waypoints, route.HandoffController) {
				return false
			}
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
		if !slices.Contains(arr.Airports, airport) {
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
	loc, ok := av.DB.AirportTimeZone(airport)
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

// enabledDepartureCategories returns the categories the scenario is launching
// from a runway, and nothing more: how many aircraft go where comes from the
// published flights themselves.
func (lc *LaunchConfig) enabledDepartureCategories(airport string, runway av.RunwayID) []string {
	var categories []string
	for category, enabled := range lc.DepartureEnabled[airport][runway] {
		if enabled {
			categories = append(categories, category)
		}
	}
	slices.Sort(categories)
	return categories
}
