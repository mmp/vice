// sim/schedule.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

const (
	// scenarioScheduleHorizon is how much scenario-generated traffic the
	// schedule holds ahead of the sim's clock; consumption extends it as the
	// clock approaches its end.
	scenarioScheduleHorizon = 8 * time.Hour

	// scheduleExtendLead is how far before the schedule runs out it is
	// extended.
	scheduleExtendLead = time.Hour

	// callsignReuseWindow is how close together two scheduled flights may not
	// share a callsign. Flights further apart than this are two different
	// real-world flights; deduplicating across the whole schedule would starve
	// airlines specified with a fixed callsign.
	callsignReuseWindow = 3 * time.Hour

	// parkedSpawnDelay pushes a published flight's spawn far enough out that
	// it never comes due while its direction's rate scale is zero. The
	// schedule is the only copy of the flights, so they are parked rather
	// than dropped: raising the scale restores their times from the data.
	parkedSpawnDelay = 365 * 24 * time.Hour
)

// FlightSchedule is the authoritative pregenerated queue of the IFR traffic a
// sim will fly, serialized with it. All traffic sources feed it: scenario
// traffic is sampled ahead over scenarioScheduleHorizon, published (timetable
// or historical) traffic is queued in full when the sim is created. The slices
// are kept sorted by (SpawnTime, Callsign).
type FlightSchedule struct {
	Departures  []ScheduledDeparture
	Arrivals    []ScheduledArrival
	Overflights []ScheduledOverflight

	// ScenarioGeneratedUntil is how far the scenario-sourced entries reach;
	// consumption extends the schedule when the clock nears it.
	ScenarioGeneratedUntil Time
}

// ScheduledFlight is the identity and timing every queued entry carries. It
// holds no Aircraft and no allocated resources (squawk, flight plan, list
// index): those are all created when the flight spawns, so discarding an entry
// frees nothing.
type ScheduledFlight struct {
	Callsign         string
	AircraftType     string
	DepartureAirport string
	ArrivalAirport   string
	Source           TrafficSource
	SpawnTime        Time

	// Published entries retain the flight data's Day/Minute so that a rate
	// scale change can rewrite SpawnTime in place; SpawnOffset survives such
	// rewrites and carries recycle shifts and manual-to-automatic rebases.
	Day         uint16
	Minute      int
	SpawnOffset time.Duration

	// Airline lets a scenario entry resample its callsign if it clashes when
	// the flight is created; zero for published entries, whose real callsigns
	// are not resampled.
	Airline av.AirlineSpecifier
}

// ScheduledDeparture is a queued IFR departure. Scenario entries carry their
// sampled runway, category, and departure route; published entries resolve
// their runway when they spawn, so Runway is empty and DepartureIndex unused.
type ScheduledDeparture struct {
	ScheduledFlight
	Runway         av.RunwayID
	Category       string
	DepartureIndex int // into State.Airports[DepartureAirport].Departures
}

// ScheduledArrival is a queued arrival. The flow and arrival index are
// resolved at generation for both sources; the remaining fields carry a
// published flight's placement.
type ScheduledArrival struct {
	ScheduledFlight
	Group string
	Index int // into State.InboundFlows[Group].Arrivals

	// FiledRoute, Substitute, Cruise, and How record how a published flight
	// was fitted into the scenario; see placeArrival.
	FiledRoute string
	Substitute string
	Cruise     CruiseLimits
	How        string

	// DropReason, when non-empty, marks a published arrival with no
	// placement; it stays queued so the drop is reported when its spawn time
	// comes around rather than in a heap at launch.
	DropReason string
}

// ScheduledOverflight is a queued overflight; overflights are rate-based
// scenario traffic under every traffic source.
type ScheduledOverflight struct {
	ScheduledFlight
	Group string
	Index int // into State.InboundFlows[Group].Overflights
}

func compareScheduledFlights(a, b ScheduledFlight) int {
	if c := a.SpawnTime.Compare(b.SpawnTime); c != 0 {
		return c
	}
	return strings.Compare(a.Callsign, b.Callsign)
}

func (fs *FlightSchedule) sortDepartures() {
	slices.SortStableFunc(fs.Departures, func(a, b ScheduledDeparture) int {
		return compareScheduledFlights(a.ScheduledFlight, b.ScheduledFlight)
	})
}

func (fs *FlightSchedule) sortArrivals() {
	slices.SortStableFunc(fs.Arrivals, func(a, b ScheduledArrival) int {
		return compareScheduledFlights(a.ScheduledFlight, b.ScheduledFlight)
	})
}

func (fs *FlightSchedule) sortOverflights() {
	slices.SortStableFunc(fs.Overflights, func(a, b ScheduledOverflight) int {
		return compareScheduledFlights(a.ScheduledFlight, b.ScheduledFlight)
	})
}

func (fs *FlightSchedule) sortEntries() {
	fs.sortDepartures()
	fs.sortArrivals()
	fs.sortOverflights()
}

// generateSchedule builds the schedule from scratch starting at the sim's
// current time, which for a new sim is the start of prespawn. It runs before
// the sim is shared, or with the sim's mutex held.
func (s *Sim) generateSchedule() {
	now := s.State.SimTime
	until := now.Add(scenarioScheduleHorizon)
	s.Schedule = FlightSchedule{ScenarioGeneratedUntil: until}

	if s.State.LaunchConfig.TrafficSource == TrafficSourceScenario {
		s.generateScenarioDepartures(now, until)
		s.generateScenarioArrivals(now, until)
	} else {
		s.generatePublishedFlights()
	}
	// Overflights are rate-based under every source: neither timetables nor
	// historical data cover them.
	s.generateScenarioOverflights(now, until)

	s.Schedule.sortEntries()
}

// extendSchedule generates the next chunk of scenario traffic when the sim's
// clock nears the end of what has been generated. Published entries were
// queued in full at creation, so only the scenario-sourced kinds extend.
// Each extended slice is cloned first: the generators append and the sorts
// permute in place, and a shallow copy of the Sim may be serialized outside
// its lock (see deleteScheduledEntry).
func (s *Sim) extendSchedule() {
	if s.State.SimTime.Before(s.Schedule.ScenarioGeneratedUntil.Add(-scheduleExtendLead)) {
		return
	}
	from := s.Schedule.ScenarioGeneratedUntil
	until := from.Add(scenarioScheduleHorizon)

	if s.State.LaunchConfig.TrafficSource == TrafficSourceScenario {
		s.Schedule.Departures = slices.Clone(s.Schedule.Departures)
		s.generateScenarioDepartures(from, until)
		s.Schedule.sortDepartures()

		s.Schedule.Arrivals = slices.Clone(s.Schedule.Arrivals)
		s.generateScenarioArrivals(from, until)
		s.Schedule.sortArrivals()
	}

	s.Schedule.Overflights = slices.Clone(s.Schedule.Overflights)
	s.generateScenarioOverflights(from, until)
	s.Schedule.sortOverflights()

	s.Schedule.ScenarioGeneratedUntil = until
}

// generateScenarioDepartures samples IFR departures per runway over [from,
// until) at the launch config's rates and appends them to the schedule.
func (s *Sim) generateScenarioDepartures(from, until Time) {
	lc := &s.State.LaunchConfig
	for _, airport := range util.SortedMapKeys(lc.DepartureRates) {
		for _, runway := range util.SortedMapKeys(lc.DepartureRates[airport]) {
			rates := lc.DepartureRates[airport][runway]
			rate := sumRateMap(rates, lc.DepartureRateScale)
			if rate == 0 {
				continue
			}
			skipped := 0
			for t := from.Add(randomInitialWait(rate, s.Rand)); t.Before(until); t = t.Add(randomWait(rate, false, s.Rand)) {
				category, _ := pickWeighted(rates, lc.DepartureRateScale, s.Rand)
				if entry, ok := s.sampleScenarioDeparture(airport, runway, category, t); ok {
					s.Schedule.Departures = append(s.Schedule.Departures, entry)
				} else {
					skipped++
				}
			}
			if skipped > 0 {
				s.lg.Warnf("%s/%s: unable to sample a valid departure for %d spawn slots",
					airport, runway, skipped)
			}
		}
	}
}

func (s *Sim) sampleScenarioDeparture(airport string, runway av.RunwayID, category string,
	t Time) (ScheduledDeparture, bool) {
	ap, rwy, exitRoutes, err := s.departureConfiguration(airport, runway, category)
	if err != nil {
		return ScheduledDeparture{}, false
	}

	// Sample uniformly, minding the category, if specified. The scenario's own
	// generator needs airlines to fly; a departure without them is there for
	// published traffic.
	idx := rand.SampleFiltered(s.Rand, ap.Departures,
		func(d av.Departure) bool {
			_, ok := exitRoutes[d.Exit]
			return ok && len(d.Airlines) > 0 &&
				(rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
		})
	if idx == -1 {
		return ScheduledDeparture{}, false
	}
	dep := &ap.Departures[idx]

	flight, ok := sampleScheduledAircraft(s, dep.Airlines,
		func(al av.DepartureAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.DepartureAirline) (string, string) { return airport, dep.Destination },
		s.scheduledClashCallsigns(t))
	if !ok {
		return ScheduledDeparture{}, false
	}

	// The airline decides the aircraft type, so only now can the routes it
	// flies be worked out; the exit may have none for it.
	routes := av.ExitRoutesForAircraft(ap.DepartureRoutes[runway], flight.AircraftType)
	if _, ok := routes[dep.Exit]; !ok {
		return ScheduledDeparture{}, false
	}

	flight.Source = TrafficSourceScenario
	flight.SpawnTime = t
	return ScheduledDeparture{
		ScheduledFlight: flight,
		Runway:          runway,
		Category:        category,
		DepartureIndex:  idx,
	}, true
}

// generateScenarioArrivals samples arrivals per inbound flow over [from,
// until) at the launch config's rates, compressing the waits inside arrival
// push windows the same way runtime spawning used to.
func (s *Sim) generateScenarioArrivals(from, until Time) {
	lc := &s.State.LaunchConfig
	pushes := s.scenarioPushWindows(from, until)
	inPush := func(t Time) bool {
		return slices.ContainsFunc(pushes, func(w pushWindow) bool {
			return !t.Before(w.start) && t.Before(w.end)
		})
	}

	for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
		rates := make(map[string]float32)
		for airport, rate := range lc.InboundFlowRates[group] {
			if airport != "overflights" {
				rates[airport] = rate
			}
		}
		rateSum := sumRateMap(rates, lc.InboundFlowRateScale)
		if rateSum == 0 {
			continue
		}
		skipped := 0
		for t := from.Add(randomInitialWait(rateSum, s.Rand)); t.Before(until); t = t.Add(randomWait(rateSum, inPush(t), s.Rand)) {
			airport, _ := pickWeighted(rates, lc.InboundFlowRateScale, s.Rand)
			if entry, ok := s.sampleScenarioArrival(group, airport, t); ok {
				s.Schedule.Arrivals = append(s.Schedule.Arrivals, entry)
			} else {
				skipped++
			}
		}
		if skipped > 0 {
			s.lg.Warnf("%s: unable to sample a valid arrival for %d spawn slots", group, skipped)
		}
	}
}

func (s *Sim) sampleScenarioArrival(group, airport string, t Time) (ScheduledArrival, bool) {
	flow, ok := s.State.InboundFlows[group]
	if !ok {
		return ScheduledArrival{}, false
	}

	// Select a random arrival route that serves this airport. The scenario's
	// own generator needs airlines to fly; a route that only lists the airport
	// is there for published traffic.
	idx := rand.SampleFiltered(s.Rand, flow.Arrivals, func(ar av.Arrival) bool {
		return len(ar.Airlines[airport]) > 0
	})
	if idx == -1 {
		return ScheduledArrival{}, false
	}
	arr := &flow.Arrivals[idx]

	flight, ok := sampleScheduledAircraft(s, arr.Airlines[airport],
		func(al av.ArrivalAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.ArrivalAirline) (string, string) { return al.Airport, airport },
		s.scheduledClashCallsigns(t))
	if !ok {
		return ScheduledArrival{}, false
	}

	flight.Source = TrafficSourceScenario
	flight.SpawnTime = t
	return ScheduledArrival{ScheduledFlight: flight, Group: group, Index: idx}, true
}

// generateScenarioOverflights samples overflights per flow over [from, until).
func (s *Sim) generateScenarioOverflights(from, until Time) {
	lc := &s.State.LaunchConfig
	for _, group := range util.SortedMapKeys(lc.InboundFlowRates) {
		rate := scaleRate(lc.InboundFlowRates[group]["overflights"], lc.InboundFlowRateScale)
		if rate == 0 {
			continue
		}
		skipped := 0
		for t := from.Add(randomInitialWait(rate, s.Rand)); t.Before(until); t = t.Add(randomWait(rate, false, s.Rand)) {
			if entry, ok := s.sampleScenarioOverflight(group, t); ok {
				s.Schedule.Overflights = append(s.Schedule.Overflights, entry)
			} else {
				skipped++
			}
		}
		if skipped > 0 {
			s.lg.Warnf("%s: unable to sample a valid overflight for %d spawn slots", group, skipped)
		}
	}
}

func (s *Sim) sampleScenarioOverflight(group string, t Time) (ScheduledOverflight, bool) {
	flow, ok := s.State.InboundFlows[group]
	if !ok || len(flow.Overflights) == 0 {
		return ScheduledOverflight{}, false
	}
	idx := s.Rand.Intn(len(flow.Overflights))
	of := &flow.Overflights[idx]
	if len(of.Airlines) == 0 {
		return ScheduledOverflight{}, false
	}

	flight, ok := sampleScheduledAircraft(s, of.Airlines,
		func(al av.OverflightAirline) av.AirlineSpecifier { return al.AirlineSpecifier },
		func(al av.OverflightAirline) (string, string) { return al.DepartureAirport, al.ArrivalAirport },
		s.scheduledClashCallsigns(t))
	if !ok {
		return ScheduledOverflight{}, false
	}

	flight.Source = TrafficSourceScenario
	flight.SpawnTime = t
	return ScheduledOverflight{ScheduledFlight: flight, Group: group, Index: idx}, true
}

// pushWindow is one arrival push, precomputed over the generation window.
type pushWindow struct {
	start, end Time
}

// scenarioPushWindows lays out the arrival pushes over [from, until),
// following the schedule the runtime push machine used to keep: the first
// push starts within the first push-frequency interval, and each subsequent
// one a jittered frequency after the last one ends.
func (s *Sim) scenarioPushWindows(from, until Time) []pushWindow {
	lc := &s.State.LaunchConfig
	if !lc.ArrivalPushes {
		return nil
	}
	freq := time.Duration(lc.ArrivalPushFrequencyMinutes) * time.Minute
	length := time.Duration(lc.ArrivalPushLengthMinutes) * time.Minute

	var windows []pushWindow
	start := from.Add(s.Rand.DurationRange(1*time.Minute, freq+1*time.Minute))
	for start.Before(until) {
		end := start.Add(length)
		windows = append(windows, pushWindow{start: start, end: end})
		start = end.Add(freq + s.Rand.DurationRange(-2*time.Minute, 2*time.Minute))
	}
	return windows
}

// scheduledClashCallsigns collects the callsigns a flight scheduled at t may
// not use: everything the sim is currently flying or holds a flight plan for,
// plus the scheduled flights within callsignReuseWindow of t.
func (s *Sim) scheduledClashCallsigns(t Time) []av.ADSBCallsign {
	callsigns := s.currentCallsigns()
	within := func(f *ScheduledFlight) bool {
		d := f.SpawnTime.Sub(t)
		return d < callsignReuseWindow && d > -callsignReuseWindow
	}
	for i := range s.Schedule.Departures {
		if within(&s.Schedule.Departures[i].ScheduledFlight) {
			callsigns = append(callsigns, av.ADSBCallsign(s.Schedule.Departures[i].Callsign))
		}
	}
	for i := range s.Schedule.Arrivals {
		if within(&s.Schedule.Arrivals[i].ScheduledFlight) {
			callsigns = append(callsigns, av.ADSBCallsign(s.Schedule.Arrivals[i].Callsign))
		}
	}
	for i := range s.Schedule.Overflights {
		if within(&s.Schedule.Overflights[i].ScheduledFlight) {
			callsigns = append(callsigns, av.ADSBCallsign(s.Schedule.Overflights[i].Callsign))
		}
	}
	return callsigns
}

// sampleScheduledAircraft samples an airline and from it an aircraft type and
// callsign for a scheduled flight, avoiding the given callsigns. It is the
// schedule-generation counterpart of filterAndSampleAircraft, returning the
// flight's identity rather than an Aircraft.
func sampleScheduledAircraft[T any](s *Sim, airlines []T, specifier func(T) av.AirlineSpecifier,
	airports func(T) (string, string), clash []av.ADSBCallsign) (ScheduledFlight, bool) {
	available := make([]T, 0, len(airlines))
	for _, al := range airlines {
		spec := specifier(al)
		if spec.Callsign == "" || !av.CallsignClashesWithExisting(clash, spec.Callsign, s.EnforceUniqueCallsignSuffix) {
			available = append(available, al)
		}
	}
	if len(available) == 0 {
		return ScheduledFlight{}, false
	}

	airline := rand.SampleSlice(s.Rand, available)
	spec := specifier(airline)
	dep, arr := airports(airline)
	var actype, callsign string
	if spec.Callsign != "" {
		callsign = strings.ToUpper(strings.TrimSpace(spec.Callsign))
		actype = spec.SampleAcType(s.Rand, dep, arr, s.lg)
	} else {
		actype, callsign = spec.SampleAcTypeAndCallsign(s.Rand, clash, s.EnforceUniqueCallsignSuffix, dep, arr, s.lg)
	}
	if actype == "" || callsign == "" {
		return ScheduledFlight{}, false
	}

	return ScheduledFlight{
		Callsign:         callsign,
		AircraftType:     actype,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
		Airline:          spec,
	}, true
}

// pickWeighted deterministically samples a key with probability proportional
// to its scaled rate, iterating the map in sorted order so that schedule
// generation is reproducible for a given random-number stream. It returns the
// scaled rate sum; the key is empty when the sum is zero.
func pickWeighted(rates map[string]float32, scale float32, r *rand.Rand) (string, float32) {
	var sum float32
	for _, rate := range rates {
		sum += scaleRate(rate, scale)
	}
	if sum == 0 {
		return "", 0
	}

	u := r.Float32Range(0, sum)
	var accum float32
	var picked string
	for _, key := range util.SortedMapKeys(rates) {
		rate := scaleRate(rates[key], scale)
		if rate == 0 {
			continue
		}
		picked = key
		accum += rate
		if u < accum {
			break
		}
	}
	return picked, sum
}

// generatePublishedFlights queues the published traffic a non-scenario source
// flies: the whole selected timetable day, or the historical flights over the
// sim's window. Missing data leaves the published portion of the schedule
// empty, with the reason logged.
func (s *Sim) generatePublishedFlights() {
	lc := &s.State.LaunchConfig

	switch lc.TrafficSource {
	case TrafficSourceHistorical:
		flights := s.readHistoricalFlights()
		if len(flights) == 0 {
			s.log("Traffic source: historical, but no flights were found for %s from %s",
				s.State.Facility, s.StartTime.Time().Format("2006-01-02 15:04Z"))
			return
		}
		s.log("Traffic source: historical, %d flights found for %s from %s",
			len(flights), s.State.Facility, s.StartTime.Time().Format("2006-01-02 15:04Z"))
		// Historical published times are when the aircraft actually took off,
		// so a departure needs the spawn lead to push back and taxi.
		s.schedulePublishedFlights(flights, flightSpawnLead)

	case TrafficSourceTimetable:
		catalog, err := LoadAirportTimetables(lc.TimetableAirport)
		if err != nil {
			s.log("Timetable traffic: %v", err)
			return
		}
		timetable, ok := catalog.Find(lc.TimetableAirport, lc.TimetableID)
		if !ok {
			s.log("Timetable traffic: timetable %q not found for %s", lc.TimetableID, lc.TimetableAirport)
			return
		}
		s.log("Traffic source: timetable %q for %s", timetable.Name, timetable.Airport)
		// A timetable's published departure times are pushback times, so
		// departures spawn at them directly.
		s.schedulePublishedFlights(timetableFlights(s.StartTime, timetable, lc), 0)
	}
}

// schedulePublishedFlights queues published flights in time order; departures
// spawn departureSpawnLead ahead of their published times and arrivals
// flightSpawnLead ahead of theirs. Arrivals the scenario has no way to fly
// stay in the queue with the reason, to be reported when their spawn times
// come around.
func (s *Sim) schedulePublishedFlights(flights []av.Flight, departureSpawnLead time.Duration) {
	routed := makeRoutedPairs()

	flights, rotorcraft := dropRotorcraft(flights)
	// One real flight must enter the sim once, however many records it left in
	// the data: the second aircraft to reach the runway under a callsign is
	// turned away, and there is no second callsign to give it.
	flights, repeated := dropRepeatedRecords(flights)
	flights, returned := dropReturnedLegs(flights)

	missed, unplaceable := 0, 0
	// Why the arrivals that can't be flown can't be, so that a scenario losing
	// a stream says which one rather than only how many.
	dropped := make(map[string]int)
	earliest := s.StartTime.Add(-PrespawnDuration)
	if s.State.SimTime.After(earliest) {
		earliest = s.State.SimTime
	}

	start := s.StartTime.Time()
	departureScale := math.Clamp(s.State.LaunchConfig.PublishedDepartureRateScale, 0, MaxPublishedRateScale)
	arrivalScale := math.Clamp(s.State.LaunchConfig.PublishedArrivalRateScale, 0, MaxPublishedRateScale)

	nDepartures, nArrivals := 0, 0
	for _, flight := range flights {
		lead, scale := flightSpawnLead, arrivalScale
		if flight.Departure {
			lead, scale = departureSpawnLead, departureScale
		}
		var spawn Time
		if scale == 0 {
			// This direction flies nothing for now.
			spawn = earliest.Add(parkedSpawnDelay)
		} else {
			spawn = NewSimTime(publishedTrafficTime(flight.Time(), start, scale).Add(-lead))
			// A flight whose spawn time has already gone by has been missed.
			// Prespawn rewinds the clock before the selected start time, so compare
			// against where the clock actually begins. Releasing a backlog all at
			// once would swamp the runways rather than fill them.
			if spawn.Before(earliest) {
				missed++
				continue
			}
		}

		sf := ScheduledFlight{
			Callsign:     flight.Callsign,
			AircraftType: flight.AircraftType,
			Source:       s.State.LaunchConfig.TrafficSource,
			SpawnTime:    spawn,
			Day:          flight.Day,
			Minute:       flight.Minute,
		}

		if flight.Departure {
			sf.DepartureAirport, sf.ArrivalAirport = flight.Airport, flight.Other
			s.Schedule.Departures = append(s.Schedule.Departures, ScheduledDeparture{ScheduledFlight: sf})
			nDepartures++
			continue
		}

		sf.DepartureAirport, sf.ArrivalAirport = flight.Other, flight.Airport
		placement, err := s.placeArrival(flight.Airport, flight.Other, flight.AircraftType, routed)
		entry := ScheduledArrival{
			ScheduledFlight: sf,
			Group:           placement.group,
			Index:           placement.index,
			FiledRoute:      placement.filedRoute,
			Substitute:      placement.substitute,
			Cruise:          placement.cruise,
			How:             placement.how,
		}
		if err != nil {
			unplaceable++
			dropped[err.Error()]++
			entry.DropReason = err.Error()
		}
		s.Schedule.Arrivals = append(s.Schedule.Arrivals, entry)
		nArrivals++
	}

	if len(flights) > 0 {
		lead, scale := flightSpawnLead, arrivalScale
		if flights[0].Departure {
			lead, scale = departureSpawnLead, departureScale
		}
		if scale > 0 {
			s.log("Published traffic: clock starts %s, first flight %s spawns %s",
				earliest.Time().Format("2006-01-02 15:04:05Z"),
				flights[0].Time().Format("2006-01-02 15:04:05Z"),
				publishedTrafficTime(flights[0].Time(), start, scale).Add(-lead).Format("2006-01-02 15:04:05Z"))
		}
	}
	summary := fmt.Sprintf("Published traffic: %d departures, %d arrivals to fly",
		nDepartures, nArrivals-unplaceable)
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
	for _, reason := range util.SortedMapKeys(dropped) {
		s.log("Unflyable arrivals: %d x %s", dropped[reason], reason)
	}
}

///////////////////////////////////////////////////////////////////////////
// Consumption

// deleteScheduledEntry removes entries[i] without disturbing the original
// backing array: a shallow copy of the Sim is marshaled outside its lock when
// a client saves, so schedule mutations always build fresh slices.
func deleteScheduledEntry[T any](entries []T, i int) []T {
	return append(entries[:i:i], entries[i+1:]...)
}

// spawnScheduledFlights creates the scheduled flights that have come due, one
// per flow per tick, for each kind whose launches are automatic.
func (s *Sim) spawnScheduledFlights() {
	s.spawnScheduledDepartures()
	s.spawnScheduledArrivals()
	s.spawnScheduledOverflights()
}

func (s *Sim) spawnScheduledDepartures() {
	if s.State.LaunchConfig.DepartureMode != LaunchAutomatic {
		return
	}
	now := s.State.SimTime
	spawned := make(map[string]bool) // airport/runway launched this tick

	for i := 0; i < len(s.Schedule.Departures); {
		e := s.Schedule.Departures[i]
		if e.SpawnTime.After(now) {
			break
		}

		if e.Source == TrafficSourceScenario {
			key := e.DepartureAirport + "/" + string(e.Runway)
			depState := s.DepartureState[e.DepartureAirport][e.Runway]
			if depState == nil {
				s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
				continue
			}
			// A backed-up gate defers the entry rather than losing it; it
			// spawns once the queue drains.
			if spawned[key] || len(depState.Gate) >= 10 {
				i++
				continue
			}
			ac, err := s.createScheduledIFRDeparture(e)
			if err != nil {
				s.lg.Warnf("%s: unable to create IFR departure: %v", e.Callsign, err)
				s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
				continue
			}
			ac.ReleaseTime = util.Select(ac.HoldForRelease, ac.ReleaseTime, now)
			s.addDepartureToPool(ac, e.Runway, false, e.Source)
			spawned[key] = true
			s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
			continue
		}

		runway, categories, _, err := s.resolveScheduledDepartureRunway(&e)
		if err != nil {
			if errors.Is(err, errNoDepartureRunwayEnabled) {
				// Nothing is launching from this airport right now; leave the
				// flight for when a flow is enabled.
				i++
				continue
			}
			// The flight is due and no runway the scenario is launching can
			// fly it.
			s.log("%s: dropped departure %s->%s (%s): %v", e.Callsign,
				e.DepartureAirport, e.ArrivalAirport, e.AircraftType, err)
			s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
			continue
		}
		key := e.DepartureAirport + "/" + string(runway)
		if spawned[key] {
			i++
			continue
		}

		ac, err := s.createPublishedIFRDeparture(e, runway, categories)
		if errors.Is(err, errCallsignInUse) {
			s.noteCallsignClash(e.Callsign, err)
		} else if err != nil {
			s.lg.Warnf("%s: unable to create published departure: %v", e.Callsign, err)
		} else {
			s.addDepartureToPool(ac, runway, false, e.Source)
			spawned[key] = true
		}
		s.Schedule.Departures = deleteScheduledEntry(s.Schedule.Departures, i)
	}
}

func (s *Sim) spawnScheduledArrivals() {
	if s.State.LaunchConfig.ArrivalMode != LaunchAutomatic {
		return
	}
	lc := &s.State.LaunchConfig
	now := s.State.SimTime
	spawned := make(map[string]bool) // flow group launched (or deferred) this tick

	for i := 0; i < len(s.Schedule.Arrivals); {
		e := s.Schedule.Arrivals[i]
		if e.SpawnTime.After(now) {
			break
		}

		if e.DropReason != "" {
			// No placement was found when the flight was queued; report the
			// drop now that it would have spawned.
			s.log("%s: dropped arrival %s->%s (%s): %s", e.Callsign,
				e.DepartureAirport, e.ArrivalAirport, e.AircraftType, e.DropReason)
			s.Schedule.Arrivals = deleteScheduledEntry(s.Schedule.Arrivals, i)
			continue
		}

		if e.Source != TrafficSourceScenario {
			if _, ok := lc.InboundFlowRates[e.Group][e.ArrivalAirport]; !ok ||
				!lc.InboundFlowEnabled[e.Group][e.ArrivalAirport] {
				// This scenario isn't landing traffic at that airport.
				s.discardPublishedArrival(e.ArrivalAirport, e.Group)
				s.Schedule.Arrivals = deleteScheduledEntry(s.Schedule.Arrivals, i)
				continue
			}
		}

		if spawned[e.Group] {
			i++
			continue
		}

		ac, err := s.createScheduledArrival(e)
		if errors.Is(err, errPublishedArrivalSpawnConflict) {
			// Leave this arrival where it is and retry next tick, so that it
			// keeps its place while the preceding one moves clear of the
			// spawn point; nothing else in the flow may jump ahead of it.
			spawned[e.Group] = true
			i++
			continue
		}
		if errors.Is(err, errCallsignInUse) {
			s.noteCallsignClash(e.Callsign, err)
		} else if err != nil {
			s.lg.Errorf("%s: unable to create arrival: %v", e.Callsign, err)
		} else {
			s.addAircraftNoLock(*ac)
			spawned[e.Group] = true
		}
		s.Schedule.Arrivals = deleteScheduledEntry(s.Schedule.Arrivals, i)
	}
}

func (s *Sim) spawnScheduledOverflights() {
	if s.State.LaunchConfig.OverflightMode != LaunchAutomatic {
		return
	}
	now := s.State.SimTime
	spawned := make(map[string]bool)

	for i := 0; i < len(s.Schedule.Overflights); {
		e := s.Schedule.Overflights[i]
		if e.SpawnTime.After(now) {
			break
		}
		if spawned[e.Group] {
			i++
			continue
		}

		ac, err := s.createScheduledOverflight(e)
		if err != nil {
			s.lg.Errorf("%s: unable to create overflight: %v", e.Callsign, err)
		} else {
			s.addAircraftNoLock(*ac)
			spawned[e.Group] = true
		}
		s.Schedule.Overflights = deleteScheduledEntry(s.Schedule.Overflights, i)
	}
}

// errNoDepartureRunwayEnabled says no runway at a published departure's
// airport has any enabled category, so the flight waits rather than being
// dropped.
var errNoDepartureRunwayEnabled = errors.New("no departure runway is enabled")

// resolveScheduledDepartureRunway finds the runway a published departure leaves from, along with
// the exit and route it flies there: of the runways the scenario is launching, the one whose gates
// suit the flight best, ties going to the first in sorted order.
func (s *Sim) resolveScheduledDepartureRunway(e *ScheduledDeparture) (av.RunwayID, []string,
	departureChoice, error) {
	lc := &s.State.LaunchConfig
	var best av.RunwayID
	var bestCategories []string
	var bestChoice departureChoice
	var fitErr error
	launching := false
	for _, runway := range util.SortedMapKeys(lc.DepartureEnabled[e.DepartureAirport]) {
		categories := lc.enabledDepartureCategories(e.DepartureAirport, runway)
		if len(categories) == 0 {
			continue
		}
		launching = true
		choice, err := s.findPublishedDeparture(e.DepartureAirport, runway, categories,
			e.ArrivalAirport, e.AircraftType, s.routedPairsIndex().destinationsByOrigin)
		if err != nil {
			fitErr = err
			continue
		}
		if best == "" || choice.fit < bestChoice.fit {
			best, bestCategories, bestChoice = runway, categories, choice
		}
	}
	if best != "" {
		return best, bestCategories, bestChoice, nil
	}
	if !launching {
		return "", nil, departureChoice{}, errNoDepartureRunwayEnabled
	}
	return "", nil, departureChoice{}, fitErr
}

// noteCallsignClash reports a published flight discarded because its callsign
// was in use, the first time each one turns up.
func (s *Sim) noteCallsignClash(callsign string, err error) {
	if s.discardedClashes == nil {
		s.discardedClashes = make(map[string]int)
	}
	if s.discardedClashes[callsign] == 0 {
		s.log("Dropping due to callsign clash %v", err)
	}
	s.discardedClashes[callsign]++
}

// discardPublishedArrival counts an arrival dropped because the scenario
// lands no traffic at its airport, reporting the first one so the reason is
// visible without a line per flight.
func (s *Sim) discardPublishedArrival(airport, group string) {
	if s.discardedArrivals == nil {
		s.discardedArrivals = make(map[string]int)
	}
	if s.discardedArrivals[airport] == 0 {
		s.log("Discarding published arrivals at %s: %s lands no traffic there", airport, group)
	}
	s.discardedArrivals[airport]++
}

// routedPairsIndex returns the cached route-database index, building it on
// first use.
func (s *Sim) routedPairsIndex() routedPairs {
	if s.routed == nil {
		routed := makeRoutedPairs()
		s.routed = &routed
	}
	return *s.routed
}

///////////////////////////////////////////////////////////////////////////
// Launch config changes

// applyScheduleConfigChanges reconciles the schedule with a new launch
// config: regenerating what a rate change invalidates, rewriting published
// spawn times under a new rate scale, and rebasing flows whose launches
// switch from manual back to automatic. Called with the new config already
// stored in s.State.LaunchConfig.
func (s *Sim) applyScheduleConfigChanges(old *LaunchConfig) {
	lc := &s.State.LaunchConfig

	if lc.TrafficSource != old.TrafficSource || lc.TimetableID != old.TimetableID ||
		lc.TimetableAirport != old.TimetableAirport || lc.TimetableStartMinute != old.TimetableStartMinute {
		// The source's flights changed wholesale, so rebuild from scratch;
		// recycle shifts on published entries belong to the old dataset, and
		// pending manual-launch flights may be its too.
		s.generateSchedule()
		s.clearPendingLaunches()
	} else {
		scenario := lc.TrafficSource == TrafficSourceScenario
		departureRatesChanged := lc.DepartureRateScale != old.DepartureRateScale ||
			!maps.EqualFunc(lc.DepartureRates, old.DepartureRates,
				func(a, b map[av.RunwayID]map[string]float32) bool {
					return maps.EqualFunc(a, b, maps.Equal)
				})
		if scenario && departureRatesChanged {
			s.regenerateScenarioDepartures()
		}

		inboundRatesChanged := lc.InboundFlowRateScale != old.InboundFlowRateScale ||
			!maps.EqualFunc(lc.InboundFlowRates, old.InboundFlowRates, maps.Equal)
		pushesChanged := lc.ArrivalPushes != old.ArrivalPushes ||
			lc.ArrivalPushFrequencyMinutes != old.ArrivalPushFrequencyMinutes ||
			lc.ArrivalPushLengthMinutes != old.ArrivalPushLengthMinutes
		if scenario && (inboundRatesChanged || pushesChanged) {
			s.regenerateScenarioArrivals()
		}
		if inboundRatesChanged {
			s.regenerateScenarioOverflights()
		}

		if lc.PublishedArrivalRateScale != old.PublishedArrivalRateScale ||
			lc.PublishedDepartureRateScale != old.PublishedDepartureRateScale {
			s.rewritePublishedSpawnTimes()
		}

		// Arrival placements bake in which flows are enabled, so toggling one
		// refits the queued published arrivals. Departures need nothing here:
		// their runway is resolved when they spawn.
		if !scenario && !maps.EqualFunc(lc.InboundFlowEnabled, old.InboundFlowEnabled, maps.Equal) {
			s.updatePublishedArrivalPlacements()
		}
	}

	// Time spent in manual mode passes the schedule by, so a kind switched
	// back to automatic pushes its entries later by that stretch: every
	// flight resumes as far from launch as it was when manual mode began,
	// rather than the backlog spawning at once. The discarded pending flights
	// held no resources.
	if old.DepartureMode == LaunchAutomatic && lc.DepartureMode == LaunchManual {
		s.DepartureManualSince = s.State.SimTime
	}
	if old.DepartureMode == LaunchManual && lc.DepartureMode == LaunchAutomatic {
		if d := s.State.SimTime.Sub(s.DepartureManualSince); !s.DepartureManualSince.IsZero() && d > 0 {
			s.Schedule.Departures = shiftScheduledLater(s.Schedule.Departures,
				func(e *ScheduledDeparture) *ScheduledFlight { return &e.ScheduledFlight },
				d, s.Schedule.ScenarioGeneratedUntil)
		}
		s.DepartureManualSince = Time{}
		s.PendingDepartures, s.PendingVFR, s.nextVFRSample = nil, nil, nil
	}
	if old.ArrivalMode == LaunchAutomatic && lc.ArrivalMode == LaunchManual {
		s.ArrivalManualSince = s.State.SimTime
	}
	if old.ArrivalMode == LaunchManual && lc.ArrivalMode == LaunchAutomatic {
		if d := s.State.SimTime.Sub(s.ArrivalManualSince); !s.ArrivalManualSince.IsZero() && d > 0 {
			s.Schedule.Arrivals = shiftScheduledLater(s.Schedule.Arrivals,
				func(e *ScheduledArrival) *ScheduledFlight { return &e.ScheduledFlight },
				d, s.Schedule.ScenarioGeneratedUntil)
		}
		s.ArrivalManualSince = Time{}
		s.PendingArrivals = nil
	}
	if old.OverflightMode == LaunchAutomatic && lc.OverflightMode == LaunchManual {
		s.OverflightManualSince = s.State.SimTime
	}
	if old.OverflightMode == LaunchManual && lc.OverflightMode == LaunchAutomatic {
		if d := s.State.SimTime.Sub(s.OverflightManualSince); !s.OverflightManualSince.IsZero() && d > 0 {
			s.Schedule.Overflights = shiftScheduledLater(s.Schedule.Overflights,
				func(e *ScheduledOverflight) *ScheduledFlight { return &e.ScheduledFlight },
				d, s.Schedule.ScenarioGeneratedUntil)
		}
		s.OverflightManualSince = Time{}
		s.PendingOverflights = nil
	}

	// A kind newly switched to manual fills its slots in this same update.
	s.refillPendingLaunches()
}

func (s *Sim) regenerateScenarioDepartures() {
	s.Schedule.Departures = util.FilterSlice(s.Schedule.Departures,
		func(e ScheduledDeparture) bool { return e.Source != TrafficSourceScenario })
	if from, until := s.State.SimTime, s.Schedule.ScenarioGeneratedUntil; from.Before(until) {
		s.generateScenarioDepartures(from, until)
	}
	s.Schedule.sortDepartures()
}

func (s *Sim) regenerateScenarioArrivals() {
	s.Schedule.Arrivals = util.FilterSlice(s.Schedule.Arrivals,
		func(e ScheduledArrival) bool { return e.Source != TrafficSourceScenario })
	if from, until := s.State.SimTime, s.Schedule.ScenarioGeneratedUntil; from.Before(until) {
		s.generateScenarioArrivals(from, until)
	}
	s.Schedule.sortArrivals()
}

func (s *Sim) regenerateScenarioOverflights() {
	s.Schedule.Overflights = nil
	if from, until := s.State.SimTime, s.Schedule.ScenarioGeneratedUntil; from.Before(until) {
		s.generateScenarioOverflights(from, until)
	}
	s.Schedule.sortOverflights()
}

// rewritePublishedSpawnTimes recomputes published entries' spawn times from
// their flight data times under the current rate scales, re-applying each
// entry's accumulated SpawnOffset. A direction at scale zero has its flights
// parked far in the future rather than dropped. Entries a faster scale moves
// into the past are dropped as missed--but only for kinds spawning
// automatically: a manual queue legitimately holds overdue heads.
func (s *Sim) rewritePublishedSpawnTimes() {
	lc := &s.State.LaunchConfig
	start := s.StartTime.Time()
	now := s.State.SimTime
	departureScale := math.Clamp(lc.PublishedDepartureRateScale, 0, MaxPublishedRateScale)
	arrivalScale := math.Clamp(lc.PublishedArrivalRateScale, 0, MaxPublishedRateScale)
	missed := 0

	rewrite := func(f *ScheduledFlight, scale float32, lead time.Duration, automatic bool) bool {
		if f.Source == TrafficSourceScenario {
			return true
		}
		if scale == 0 {
			// This direction flies nothing for now; the flight is parked
			// rather than dropped, since the schedule is the data's only
			// copy and raising the scale brings it back.
			f.SpawnTime = now.Add(parkedSpawnDelay)
			return true
		}
		published := av.Flight{Day: f.Day, Minute: f.Minute}.Time()
		spawn := NewSimTime(publishedTrafficTime(published, start, scale).Add(-lead)).Add(f.SpawnOffset)
		if automatic && spawn.Before(now) {
			missed++
			return false
		}
		f.SpawnTime = spawn
		return true
	}

	departures := make([]ScheduledDeparture, 0, len(s.Schedule.Departures))
	for _, e := range s.Schedule.Departures {
		lead := util.Select(e.Source == TrafficSourceHistorical, flightSpawnLead, time.Duration(0))
		if rewrite(&e.ScheduledFlight, departureScale, lead, lc.DepartureMode == LaunchAutomatic) {
			departures = append(departures, e)
		}
	}
	s.Schedule.Departures = departures

	arrivals := make([]ScheduledArrival, 0, len(s.Schedule.Arrivals))
	for _, e := range s.Schedule.Arrivals {
		if rewrite(&e.ScheduledFlight, arrivalScale, flightSpawnLead, lc.ArrivalMode == LaunchAutomatic) {
			arrivals = append(arrivals, e)
		}
	}
	s.Schedule.Arrivals = arrivals

	s.Schedule.sortDepartures()
	s.Schedule.sortArrivals()
	if missed > 0 {
		s.log("Rate scale change: dropped %d published flights now in the past", missed)
	}
}

// updatePublishedArrivalPlacements redoes the fit of every queued published
// arrival to the scenario's inbound flows after the enabled flows change:
// arrivals whose flow was switched off find another way in rather than being
// dropped, and a newly enabled flow picks up the flights that belong to it.
// Spawn times, and any recycle shifts they carry, stay as they are.
func (s *Sim) updatePublishedArrivalPlacements() {
	arrivals := slices.Clone(s.Schedule.Arrivals)
	for i := range arrivals {
		e := &arrivals[i]
		if e.Source == TrafficSourceScenario {
			continue
		}
		placement, err := s.placeArrival(e.ArrivalAirport, e.DepartureAirport, e.AircraftType,
			s.routedPairsIndex())
		e.Group, e.Index = placement.group, placement.index
		e.FiledRoute, e.Substitute, e.How = placement.filedRoute, placement.substitute, placement.how
		e.Cruise = placement.cruise
		if err != nil {
			e.DropReason = err.Error()
		} else {
			e.DropReason = ""
		}
	}
	s.Schedule.Arrivals = arrivals
}

// shiftScheduledLater pushes every entry later by d, carrying the shift in
// SpawnOffset so that a later published rate-scale rewrite keeps it. The
// uniform shift preserves the entries' order, so no re-sort is needed.
// Scenario entries pushed past the generation horizon are dropped: extension
// generates that stretch at the configured rates, and keeping them too would
// double the traffic there.
func shiftScheduledLater[T any](entries []T, flight func(*T) *ScheduledFlight,
	d time.Duration, horizon Time) []T {
	shifted := make([]T, 0, len(entries))
	for _, e := range entries {
		f := flight(&e)
		f.SpawnTime = f.SpawnTime.Add(d)
		f.SpawnOffset += d
		if f.Source == TrafficSourceScenario && f.SpawnTime.After(horizon) {
			continue
		}
		shifted = append(shifted, e)
	}
	return shifted
}

// readHistoricalFlights gathers the flights a scenario using historical
// traffic flies: those at its airports over the window starting at the
// selected time. Both ends reach as far as the fastest rate scale reads
// through the data, since the scale can be raised while the sim runs and this
// is the only place the flights are gathered.
func (s *Sim) readHistoricalFlights() []av.Flight {
	departureAirports, arrivalAirports := s.State.LaunchConfig.IFRAirports()
	flights, err := av.ReadFlightDataCells(util.GetResourcesFS(),
		av.FlightDataCells(departureAirports, arrivalAirports))
	if err != nil {
		s.lg.Errorf("%s historical flight data: %v", s.State.Facility, err)
		return nil
	}

	start := s.StartTime.Time()
	return av.SelectFlights(flights, departureAirports, arrivalAirports, av.DB.Airlines,
		start.Add(-MaxPublishedRateScale*PrespawnDuration),
		start.Add(MaxPublishedRateScale*historicalFlightWindow))
}
