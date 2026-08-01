// sim/traffic_provider.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
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
)

// PublishedArrivalMaxHeadingDifference is how far an origin may lie from the
// direction an arrival flies in from and still plausibly come in on it. Gates
// are rarely less than this far apart, so a smaller difference doesn't say the
// traffic would come in anywhere else.
const PublishedArrivalMaxHeadingDifference = 60 // degrees

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

	data, err := av.ReadFlightData(util.GetResourcesFS(), s.State.Facility)
	if err != nil || data == nil {
		s.lg.Errorf("%s: no historical flight data: %v", s.State.Facility, err)
		return
	}

	// Every airport the scenario generates IFR traffic at, not just the primary
	// one; departures and arrivals separately, since a scenario often departs
	// airports it doesn't land.
	departureAirports := make(map[string]bool)
	arrivalAirports := make(map[string]bool)
	lc := &s.State.LaunchConfig
	for airport := range lc.DepartureRates {
		departureAirports[airport] = true
	}
	for _, rates := range lc.InboundFlowRates {
		for airport := range rates {
			if airport != "overflights" {
				arrivalAirports[airport] = true
			}
		}
	}

	// Reach back to cover prespawn: the sim's clock starts PrespawnDuration
	// before the selected time, and it warms up by flying the traffic from that
	// half hour the same way the scenario's own generator would.
	start := s.StartTime.Time()
	flights, err := av.FlightsInWindow(data, departureAirports, arrivalAirports, av.DB.Airlines,
		start.Add(-PrespawnDuration), start.Add(historicalFlightWindow))
	if err != nil {
		s.lg.Errorf("%s historical flight data: %v", s.State.Facility, err)
		return
	}
	s.historicalFlights = flights
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

// normalizeAirportCode and normalizeAircraftType clean up the identifiers that
// arrive with a published flight. Both traffic sources need them: a timetable's
// come from hand-edited CSV and historical ones from an outside dataset.
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
	if perf, ok := av.DB.AircraftPerformance[normalizeAircraftType(aircraftType)]; ok {
		return perf.Engine.AircraftType
	}
	return ""
}

func normalizeAircraftType(value string) string {
	aircraftType := strings.ToUpper(strings.TrimSpace(value))

	switch aircraftType {
	case "B717":
		return "B712"
	default:
		return aircraftType
	}
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

	// discardedDepartures likewise counts the departures dropped for each city
	// pair the scenario has no plausible route for.
	discardedDepartures map[string]int
}

// newTimetableTrafficProvider prepares a built-in timetable's flights. A
// timetable is a daily cycle without dates, so each flight is anchored to the
// user-selected start time and the day plays out from there; flights in the
// half hour before the start fall in the prespawn window rather than wrapping
// to the end of the day. Published departure times are treated as pushback
// times, so departures spawn at them directly.
func newTimetableTrafficProvider(s *Sim, timetable Timetable, startMinute int,
	arrivalPercentage, departurePercentage int) *publishedTrafficProvider {
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
		published := s.StartTime.Add(time.Duration(minutes) * time.Minute).Time().UTC()

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

	return newPublishedTrafficProvider(s, flights, 0)
}

// newHistoricalTrafficProvider prepares the historical flights a sim was
// handed, in time order. Their published times are when they actually took off
// or touched down, so departures spawn flightSpawnLead ahead of them.
func newHistoricalTrafficProvider(s *Sim, flights []av.Flight, arrivalPercentage,
	departurePercentage int) *publishedTrafficProvider {
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
	return newPublishedTrafficProvider(s, kept, flightSpawnLead)
}

// newPublishedTrafficProvider builds the spawn queues from flights in time
// order; departures spawn departureSpawnLead ahead of their published times and
// arrivals flightSpawnLead ahead of theirs. Arrivals whose origin no inbound
// flow serves are rerouted to follow the nearest origin that one does; any that
// can't be placed at all are reported.
func newPublishedTrafficProvider(s *Sim, flights []av.Flight,
	departureSpawnLead time.Duration) *publishedTrafficProvider {
	p := &publishedTrafficProvider{routed: makeRoutedPairs()}

	rerouted := make(map[string]string)
	var unroutable []string
	missed := 0
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

		placement, ok := s.placeArrival(flight.Airport, flight.Other, flight.AircraftType,
			p.routed.originsByDestination)
		if !ok {
			unroutable = append(unroutable, flight.Other+"->"+flight.Airport)
			continue
		}
		if placement.substitute != "" {
			rerouted[flight.Other+"->"+flight.Airport] = placement.substitute
		}

		p.arrivals = append(p.arrivals, publishedFlight{flight: flight, spawn: spawn,
			placement: placement})
	}

	if len(flights) > 0 {
		lead := flightSpawnLead
		if flights[0].Departure {
			lead = departureSpawnLead
		}
		fmt.Printf("Published traffic: clock starts %s, first flight %s spawns %s\n",
			earliest.Time().Format("2006-01-02 15:04:05Z"),
			flights[0].Time().Format("2006-01-02 15:04:05Z"),
			flights[0].Time().Add(-lead).Format("2006-01-02 15:04:05Z"))
	}
	fmt.Printf("Published traffic: %d departures, %d arrivals to fly", len(p.departures), len(p.arrivals))
	if missed > 0 {
		fmt.Printf("; skipped %d flights already due before the clock starts", missed)
	}
	fmt.Printf("\n")

	if len(rerouted) > 0 {
		var pairs []string
		for from, to := range rerouted {
			pairs = append(pairs, from+" as "+to)
		}
		slices.Sort(pairs)
		fmt.Printf("Published arrivals with no route in this scenario are flying the nearest "+
			"airport that has one: %s\n", strings.Join(pairs, ", "))
	}
	if len(unroutable) > 0 {
		slices.Sort(unroutable)
		fmt.Printf("Dropped published arrivals with no route to fly: %s\n",
			strings.Join(slices.Compact(unroutable), ", "))
	}

	return p
}

// placeArrival decides how a published flight into arrivalAirport from origin is
// flown: the inbound flow and arrival that carry it, the route it files, and the
// airport standing in for its origin when the scenario has no way to fly it from
// where it really came from.
func (s *Sim) placeArrival(arrivalAirport, origin, aircraftType string,
	routedOrigins map[string][]string) (arrivalPlacement, bool) {
	candidates := s.candidateArrivals(arrivalAirport)
	if len(candidates) == 0 {
		return arrivalPlacement{}, false
	}

	// An arrival that names the origin is the scenario saying in so many words
	// that this is how traffic from there comes in.
	if c, ok := arrivalListingOrigin(candidates, arrivalAirport, origin); ok {
		return c.placement("", "", "own route"), true
	}

	// Failing that, the route database says how the pair is really flown.
	if c, route, ok := arrivalForCityPair(candidates, arrivalAirport, origin, aircraftType); ok {
		return c.placement(route, "", "faa route"), true
	}

	// Neither knows this origin, so fly the flight the way the nearest airport
	// one of them does know is flown: from JFK, Norfolk stands in for Kill Devil
	// Hills. Real traffic comes from far more airports than either source
	// covers, and arriving as one's neighbors do beats not arriving at all. The
	// flight files its own route rather than the substitute's, which starts
	// somewhere it has never been.
	if substitute, ok := s.nearestRoutedOrigin(candidates, arrivalAirport, origin, routedOrigins); ok {
		how := "nearest route, from " + substitute
		if c, ok := arrivalListingOrigin(candidates, arrivalAirport, substitute); ok {
			return c.placement("", substitute, how), true
		}
		if c, _, ok := arrivalForCityPair(candidates, arrivalAirport, substitute, aircraftType); ok {
			return c.placement("", substitute, how), true
		}
	}

	return s.arrivalFromDirection(candidates, arrivalAirport, origin)
}

func (c candidateArrival) placement(filedRoute, substitute, how string) arrivalPlacement {
	return arrivalPlacement{group: c.group, index: c.index, filedRoute: filedRoute,
		substitute: substitute, how: how}
}

// publishedArrivalHeadingTie is how close two arrivals' directions must be for
// which of them starts farther out to decide between them. Inside this they are
// the same way in as far as an approaching flight is concerned, and the one that
// starts farthest out is the gate: the closer one is a feeder that turns traffic
// onto a downwind inside the TRACON.
const publishedArrivalHeadingTie = 10 // degrees

// publishedSubstituteFraction is how much of the trip an airport drawn from the
// route database may be away from the airport it stands in for, at either end.
const publishedSubstituteFraction = 0.5

// arrivalFromDirection picks the arrival that comes in from closest to the
// direction the origin lies in. This is the last resort: a scenario that names
// no origins and whose traffic the route database doesn't cover says where its
// traffic comes from only through where its arrivals appear, so that geometry is
// all that is left to go on.
func (s *Sim) arrivalFromDirection(candidates []candidateArrival, arrivalAirport,
	origin string) (arrivalPlacement, bool) {
	ap, apOK := av.DB.Airports[normalizeAirportCode(arrivalAirport)]
	from, fromOK := av.DB.Airports[normalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return arrivalPlacement{}, false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	best, bestBucket, bestDistance := -1, 0, float32(0)
	for i, c := range candidates {
		if len(c.arr.Waypoints) == 0 {
			continue
		}
		spawn := c.arr.Waypoints[0].Location
		fromGate := math.Heading2LL(ap.Location, spawn, s.State.NmPerLongitude)
		difference := math.HeadingDifference(fromGate, toOrigin)
		if difference > PublishedArrivalMaxHeadingDifference {
			continue
		}
		// Quantizing the difference lets how far out an arrival starts decide
		// between ones pointing much the same way, without letting it override a
		// meaningfully better-aligned one.
		bucket := int(difference) / publishedArrivalHeadingTie
		distance := math.NMDistance2LL(ap.Location, spawn)
		if best == -1 || bucket < bestBucket || (bucket == bestBucket && distance > bestDistance) {
			best, bestBucket, bestDistance = i, bucket, distance
		}
	}
	if best == -1 {
		return arrivalPlacement{}, false
	}
	return candidates[best].placement("", "", "nearest gate"), true
}

// nearestRoutedOrigin finds the airport closest to origin that there is some way
// to fly this arrival airport from: either an arrival the scenario is running
// names it, or the route database has a route for the pair. The substitute must
// lie in the same direction as the real origin, or it would come in through the
// wrong gate however close it is.
func (s *Sim) nearestRoutedOrigin(candidates []candidateArrival, arrivalAirport, origin string,
	routedOrigins map[string][]string) (string, bool) {
	arrivalAirport = normalizeAirportCode(arrivalAirport)
	ap, apOK := av.DB.Airports[arrivalAirport]
	from, fromOK := av.DB.Airports[normalizeAirportCode(origin)]
	if !apOK || !fromOK {
		return "", false
	}
	toOrigin := math.GreatCircleHeading(ap.Location, from.Location)

	possible := make(map[string]bool)
	for _, c := range candidates {
		for _, airline := range c.arr.Airlines[arrivalAirport] {
			possible[normalizeAirportCode(airline.Airport)] = true
		}
	}

	// The route database covers thousands of airports, so searching it without
	// a bound turns up a nearest one for any origin at all: Bangor is the
	// closest airport with a JFK route to Zurich, and its only route there is a
	// terminal-en-route one. A stand-in says how traffic arrives only if it is a
	// neighbor of the real origin rather than merely the closest thing on the
	// way, so it has to be a small part of the trip. A scenario's own origins
	// are curated and stand without that test.
	limit := publishedSubstituteFraction * math.NMDistance2LL(from.Location, ap.Location)
	for _, routed := range routedOrigins[arrivalAirport] {
		if location, ok := av.DB.Airports[routed]; ok &&
			math.NMDistance2LL(from.Location, location.Location) <= limit {
			possible[routed] = true
		}
	}

	best, bestDistance := "", float32(0)
	for candidate := range possible {
		location, ok := av.DB.Airports[candidate]
		if !ok || candidate == arrivalAirport {
			continue
		}
		if math.HeadingDifference(math.GreatCircleHeading(ap.Location, location.Location),
			toOrigin) > PublishedArrivalMaxHeadingDifference {
			continue
		}
		if d := math.NMDistance2LL(from.Location, location.Location); best == "" || d < bestDistance {
			best, bestDistance = candidate, d
		}
	}
	return best, best != ""
}

// routedPairs indexes the route database both ways, so that finding a stand-in
// for a city pair it doesn't cover doesn't walk the whole thing per flight.
type routedPairs struct {
	originsByDestination map[string][]string
	destinationsByOrigin map[string][]string
}

func makeRoutedPairs() routedPairs {
	routed := routedPairs{
		originsByDestination: make(map[string][]string),
		destinationsByOrigin: make(map[string][]string),
	}
	for pair := range av.DB.AirportPairRoutes {
		from, to := normalizeAirportCode(pair.From), normalizeAirportCode(pair.To)
		routed.originsByDestination[to] = append(routed.originsByDestination[to], from)
		routed.destinationsByOrigin[from] = append(routed.destinationsByOrigin[from], to)
	}
	return routed
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
		pair := airport + "-" + published.flight.Other
		if p.discardedDepartures == nil {
			p.discardedDepartures = make(map[string]int)
		}
		if p.discardedDepartures[pair] == 0 {
			fmt.Printf("Dropping published departures %s to %s: %v\n", airport,
				published.flight.Other, err)
		}
		p.discardedDepartures[pair]++
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
		return nil, idleDelay, nil // Nothing left for this flow.
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
				fmt.Printf("Discarding published arrivals at %s: %s lands no traffic there\n",
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
	return idleDelay
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
			fmt.Printf("Traffic source: historical, but no flights were found for %s from %s\n",
				s.State.Facility, s.StartTime.Time().Format("2006-01-02 15:04Z"))
			s.trafficProvider = errorTrafficProvider{
				err: fmt.Errorf("no historical flight data for %s at this time", s.State.Facility)}
		} else {
			fmt.Printf("Traffic source: historical, %d flights found for %s from %s\n",
				len(s.historicalFlights), s.State.Facility,
				s.StartTime.Time().Format("2006-01-02 15:04Z"))
			s.trafficProvider = newHistoricalTrafficProvider(s, s.historicalFlights,
				lc.PublishedArrivalPercentage, lc.PublishedDeparturePercentage)
		}

	case TrafficSourceTimetable:
		catalog, err := LoadTimetableCatalog(util.GetResourcesFS(), "traffic/timetables")
		if err != nil {
			s.trafficProvider = errorTrafficProvider{err: err}
			return s.trafficProvider
		}
		timetable, ok := catalog.Find(s.State.PrimaryAirport, lc.TimetableID)
		if !ok {
			s.trafficProvider = errorTrafficProvider{err: fmt.Errorf("timetable %q not found for %s",
				lc.TimetableID, s.State.PrimaryAirport)}
			return s.trafficProvider
		}
		fmt.Printf("Traffic source: timetable %q for %s\n", timetable.Name, timetable.Airport)
		s.trafficProvider = newTimetableTrafficProvider(s, timetable, lc.TimetableStartMinute,
			lc.PublishedArrivalPercentage, lc.PublishedDeparturePercentage)

	default:
		fmt.Printf("Traffic source: scenario\n")
		s.trafficProvider = scenarioTrafficProvider{}
	}
	return s.trafficProvider
}
