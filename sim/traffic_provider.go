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
// traffic use the next flight's published time.
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
	flow, rateSum := sampleRateMap(
		rates,
		s.State.LaunchConfig.InboundFlowRateScale,
		s.Rand,
	)

	delay := randomWait(rateSum, pushActive, s.Rand)

	if flow == "overflights" {
		ac, err := s.createOverflightNoLock(group)
		return ac, delay, err
	}

	ac, err := s.createArrivalNoLock(group, flow)
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
)

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

	// origin is the airport whose arrival route this flight follows. It is the
	// flight's own origin unless the scenario has no route from there, in which
	// case it is the nearest origin that does have one.
	origin string

	// group is the inbound flow the arrival is flown by. It is resolved once,
	// here: more than one flow can serve an origin and the flows are held in a
	// map, so resolving it per call would pick a different one each time and no
	// flow would ever agree that the arrival was its own.
	group string

	// substituted records that origin isn't the flight's own origin.
	substituted bool
}

// publishedTrafficProvider emits the flights a scenario was launched with--from
// a built-in timetable or from historical flight data--in published order.
// Departures are spawned at the gate ahead of their published time; arrivals
// are spawned ahead of their touchdown time.
type publishedTrafficProvider struct {
	departures []publishedFlight

	arrivals []publishedFlight

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
		published := s.startTime.Add(time.Duration(minutes) * time.Minute).Time().UTC()

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
	p := &publishedTrafficProvider{}

	rerouted := make(map[string]string)
	var unroutable []string
	missed := 0
	earliest := s.startTime.Add(-PrespawnDuration)
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

		origin, substituted := flight.Other, false
		group, ok := s.inboundFlowForArrival(flight.Airport, origin)
		if !ok {
			substitute, ok := s.nearestRoutedOrigin(flight.Airport, origin)
			if !ok {
				unroutable = append(unroutable, origin+"->"+flight.Airport)
				continue
			}
			rerouted[origin+"->"+flight.Airport] = substitute
			origin, substituted = substitute, true
			if group, ok = s.inboundFlowForArrival(flight.Airport, origin); !ok {
				unroutable = append(unroutable, origin+"->"+flight.Airport)
				continue
			}
		}

		p.arrivals = append(p.arrivals, publishedFlight{flight: flight, spawn: spawn,
			origin: origin, group: group, substituted: substituted})
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

// inboundFlowForArrival returns the scenario inbound flow that carries traffic
// from origin to arrivalAirport. More than one flow can serve an origin, so
// prefer one the scenario is actually landing traffic through; the flows are
// held in a map, so the choice is also sorted to keep it from varying between
// runs.
func (s *Sim) inboundFlowForArrival(arrivalAirport, origin string) (string, bool) {
	var candidates []string
	for group, inboundFlow := range s.State.InboundFlows {
		if _, err := resolvePublishedArrival(inboundFlow.Arrivals, arrivalAirport, origin); err == nil {
			candidates = append(candidates, group)
		}
	}
	slices.Sort(candidates)

	for _, group := range candidates {
		if s.State.LaunchConfig.InboundFlowEnabled[group][arrivalAirport] {
			return group, true
		}
	}
	if len(candidates) > 0 {
		return candidates[0], true
	}
	return "", false
}

// nearestRoutedOrigin finds the airport closest to origin that the scenario has
// an arrival route from. Real traffic comes from far more airports than a
// scenario names, so this lets a flight from an unlisted airport arrive the way
// its neighbors do rather than not at all.
func (s *Sim) nearestRoutedOrigin(arrivalAirport, origin string) (string, bool) {
	from, ok := av.DB.Airports[origin]
	if !ok {
		return "", false
	}

	best, bestDistance := "", float32(0)
	for _, inboundFlow := range s.State.InboundFlows {
		for _, arrival := range inboundFlow.Arrivals {
			for _, airline := range arrival.Airlines[arrivalAirport] {
				candidate, ok := av.DB.Airports[airline.Airport]
				if !ok {
					continue
				}
				d := math.NMDistance2LL(from.Location, candidate.Location)
				if best == "" || d < bestDistance {
					best, bestDistance = airline.Airport, d
				}
			}
		}
	}
	return best, best != ""
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

	ac, err := s.createPublishedIFRDepartureNoLock(published.flight, airport, runway, categories)
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
			fmt.Printf("Dropping published departures %s to %s: no plausible route in this scenario\n",
				airport, published.flight.Other)
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
	rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error) {
	index := p.nextArrivalFor(s, group, rates)
	if index < 0 {
		// Nothing left for this flow. Timetables and historical data provide
		// arrivals and departures only, so let the scenario keep generating
		// overflights when they are enabled.
		if _, ok := rates["overflights"]; ok {
			return scenarioTrafficProvider{}.createInbound(s, group,
				map[string]float32{"overflights": rates["overflights"]}, pushActive)
		}
		return nil, idleDelay, nil
	}

	published := p.arrivals[index]
	if s.State.SimTime.Before(published.spawn) {
		return nil, published.spawn.Sub(s.State.SimTime), nil
	}

	ac, err := s.createPublishedArrivalNoLock(published.flight, published.origin,
		group, published.substituted)
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
		if published.group != group {
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
			fmt.Printf("Traffic source: historical, but no flights were provided\n")
			s.trafficProvider = errorTrafficProvider{
				err: errors.New("no historical flight data was provided for this scenario")}
		} else {
			fmt.Printf("Traffic source: historical, %d flights provided from %s\n", len(s.historicalFlights),
				s.startTime.Time().Format("2006-01-02 15:04Z"))
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
