// sim/inspect.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
)

// This is what a facility engineering tool asks about a scenario. Two of the
// three answers here are reports on the real-world data vice flies--which of a
// city pair's routes the scenario can fly, and what it would do with each
// flight a published traffic source holds. Both hang off CommonState rather
// than the Sim, so a tool holding the scenario state can run them against the
// route databases and flight data on its own disk; both come from the placement
// code the sim itself runs, so what they report is what would happen. The third
// records a launched flight from end to end, which does take the sim: something
// has to fly it.

///////////////////////////////////////////////////////////////////////////
// City pair routes

// RouteUse is what a scenario does with one real-world route: the exit a
// departure leaves through, the STAR an arrival comes in on and the inbound
// flows that fly it, or the reason the scenario can't fly the route at all.
type RouteUse struct {
	Exit    av.ExitID
	STAR    string
	Flows   []string
	Problem string
}

// PairRoute is one way a directed airport pair is flown, taken from the
// scenario's own "traffic_routes", the scraped filings, or the FAA route
// databases, together with what the scenario would do with it.
type PairRoute struct {
	Route        string
	Source       string // "scenario", "scraped", or the FAA database's route type
	Aircraft     string // the classes the route is limited to; empty for all of them
	RNAVRequired bool
	DepartureFix string
	Filings      int // scraped: how often the route was filed
	MinAltitude  int // scraped: the filed cruise altitudes
	MaxAltitude  int
	Hours        string // scraped: the local hours it was filed at

	DepartureUse RouteUse
	ArrivalUse   RouteUse
}

// PairRoutes is every route between one directed airport pair. IsDeparture and
// IsArrival say which ends of the pair the scenario flies, and so which of each
// route's two uses were filled in; Notes says why an end it does fly still has
// nothing to fly the routes on.
type PairRoutes struct {
	From, To    av.ICAOAirportCode
	IsDeparture bool
	IsArrival   bool
	Notes       []string
	Routes      []PairRoute
}

// RoutesForPair returns the ways a directed airport pair is really flown and
// what the scenario does with each of them.
func (ss *CommonState) RoutesForPair(from, to av.ICAOAirportCode) PairRoutes {
	from, to = traffic.NormalizeAirportCode(from), traffic.NormalizeAirportCode(to)
	pr := PairRoutes{From: from, To: to}

	// A pair the scenario flies an end of still needs something to fly the
	// routes on there: a runway it is launching, or a flow it is landing.
	departureAirports, arrivalAirports := ss.LaunchConfig.IFRAirports()
	var exits []candidateDeparture
	var arrivals []candidateArrival
	if departureAirports[from] {
		if exits = ss.modeledExits(from); len(exits) > 0 {
			pr.IsDeparture = true
		} else {
			pr.Notes = append(pr.Notes, "No departure runway at "+string(from)+" is enabled.")
		}
	}
	if arrivalAirports[to] {
		if arrivals = ss.candidateArrivals(to); len(arrivals) > 0 {
			pr.IsArrival = true
		} else {
			pr.Notes = append(pr.Notes, "No enabled inbound flow lands at "+string(to)+".")
		}
	}

	scenarioRoutes := func(routes av.TrafficRouteSet) {
		for _, r := range routes {
			pr.Routes = append(pr.Routes, PairRoute{Route: r.Route, Source: "scenario",
				Aircraft: r.Aircraft.String()})
		}
	}
	if ap, ok := ss.Airports[from]; ok {
		scenarioRoutes(ap.TrafficRoutes.Departures[to])
	}
	if ap, ok := ss.Airports[to]; ok {
		scenarioRoutes(ap.TrafficRoutes.Arrivals[from])
	}
	for _, r := range db.DB.ScrapedRoutesBetween(from, to) {
		pr.Routes = append(pr.Routes, PairRoute{Route: r.Route, Source: "scraped",
			Aircraft: r.Aircraft.String(), Filings: r.Count, MinAltitude: r.MinAltitude,
			MaxAltitude: r.MaxAltitude, Hours: r.Hours.String()})
	}
	for _, r := range db.DB.RoutesBetween(from, to) {
		pr.Routes = append(pr.Routes, PairRoute{Route: r.Route, Source: r.Type, Aircraft: r.Aircraft,
			RNAVRequired: r.RNAVRequired, DepartureFix: r.DepartureFix})
	}

	for i := range pr.Routes {
		r := &pr.Routes[i]
		if pr.IsDeparture {
			r.DepartureUse = departureRouteUse(from, to, r.Route, r.DepartureFix, exits)
		}
		if pr.IsArrival {
			r.ArrivalUse = arrivalRouteUse(to, from, r.Route, arrivals)
		}
	}
	return pr
}

// departureRouteUse says which of the scenario's exits a route out of one of
// its airports leaves through. Aircraft type has no say here: whether a route
// goes out a gate the scenario models is a question about the scenario, and a
// route the databases hold is flown by whatever really files it.
func departureRouteUse(from, to av.ICAOAirportCode, route, departureFix string,
	candidates []candidateDeparture) RouteUse {
	if c, ok := departureExit(route, from, to, departureFix, candidates); ok {
		return RouteUse{Exit: c.dep.Exit}
	}
	return RouteUse{Problem: "no modeled exit"}
}

// modeledExits collects a candidate departure for every exit the scenario's
// enabled departure flows at an airport model. It is compatibleDepartures
// without the aircraft type: where that picks the route a particular aircraft
// flies to each exit, this one asks only which exits are there at all.
func (ss *CommonState) modeledExits(airport av.ICAOAirportCode) []candidateDeparture {
	lc := &ss.LaunchConfig
	var candidates []candidateDeparture
	for _, runway := range util.SortedMapKeys(lc.DepartureEnabled[airport]) {
		for _, category := range lc.enabledDepartureCategories(airport, runway) {
			ap, rwy, allRoutes, err := ss.departureConfiguration(airport, runway, category)
			if err != nil {
				continue
			}
			exitRoutes := make(map[av.ExitID]*av.ExitRoute, len(allRoutes))
			for exit, routes := range allRoutes {
				if len(routes) > 0 && (rwy.Category == "" || rwy.Category == ap.ExitCategory(exit)) {
					exitRoutes[exit] = routes[0]
				}
			}
			exits := util.SortedMapKeys(exitRoutes)
			// One backing array per category, so the pointers stay valid as
			// the slice below grows.
			synthesized := make([]av.Departure, len(exits))
			for i, exit := range exits {
				synthesized[i] = av.Departure{Exit: exit}
				candidates = append(candidates, candidateDeparture{ap, rwy, exitRoutes, &synthesized[i]})
			}
		}
	}
	return candidates
}

// arrivalRouteUse says which of the scenario's inbound flows a route into one
// of its airports comes in on. A route that ends with a STAR belongs to the
// flows that fly it; one with no STAR--GA and terminal-en-route traffic--comes
// in through the gate nearest its origin. Which arrival of a flow ends up
// carrying a particular aircraft is settled at spawn, when there is an aircraft
// to settle it for; what matters here is that some flow flies the route at all.
func arrivalRouteUse(to, from av.ICAOAirportCode, route string, candidates []candidateArrival) RouteUse {
	star, _ := av.RouteSTAR(db.Lookups{}, route, to)
	if star == "" {
		if c, ok := nearestSpawnToOrigin(candidates, to, from); ok {
			return RouteUse{Flows: []string{c.group}}
		}
		return RouteUse{Problem: "nothing comes in from " + string(from)}
	}

	flows := make(map[string]bool)
	for _, c := range candidates {
		if slices.ContainsFunc(c.arr.ServedSTARs(), func(s string) bool {
			return av.ProcedureBase(s) == av.ProcedureBase(star)
		}) {
			flows[c.group] = true
		}
	}
	if len(flows) == 0 {
		return RouteUse{STAR: star, Problem: "no arrival flies the " + star}
	}
	return RouteUse{STAR: star, Flows: util.SortedMapKeys(flows)}
}

///////////////////////////////////////////////////////////////////////////
// Published traffic

// TrafficQuery selects the published traffic to report on: which source, when
// it starts, and which timetable when the source is one.
type TrafficQuery struct {
	Source           TrafficSource
	Start            time.Time
	TimetableAirport av.ICAOAirportCode
	TimetableID      string
}

// FlightOutcome is what becomes of a published flight in the scenario.
type FlightOutcome int

const (
	// FlightFlown: the scenario has a runway and exit, or an arrival, for it.
	FlightFlown FlightOutcome = iota
	// FlightWaiting: no departure flow at its airport is enabled, so it waits
	// in the queue instead of being dropped; enabling one would fly it.
	FlightWaiting
	// FlightDropped: nothing the scenario models can carry it.
	FlightDropped
)

// PublishedFlight is one flight of a published traffic source and what the
// scenario does with it: where it leaves from or comes in on, the route it
// files, and why it won't fly if it won't.
type PublishedFlight struct {
	Callsign     string
	AircraftType string
	Departure    bool
	From, To     av.ICAOAirportCode
	Published    time.Time

	Runway av.RunwayID // departures
	Exit   av.ExitID

	Flow         string // arrivals
	ArrivalIndex int
	STAR         string
	Substitute   av.ICAOAirportCode // the origin stood in for, if any

	Route   string
	How     string // how the route and the exit or arrival were chosen
	Outcome FlightOutcome
	Problem string // why the flight won't fly, when it won't
}

// PublishedTraffic is what a published traffic source would fly in the scenario
// over the window starting at the selected time.
type PublishedTraffic struct {
	Source  TrafficSource
	Start   time.Time
	End     time.Time
	Flights []PublishedFlight
	// Excluded is what the source holds that never reaches the scenario at all:
	// helicopters, second records of one operation, and the return legs of
	// trips within the facility.
	Excluded []string
}

// PublishedTrafficReport reports what the scenario would do with every flight a
// published traffic source holds for the given start time. Reading the flight
// data off disk is the slow part; it takes a fraction of a second.
func (ss *CommonState) PublishedTrafficReport(q TrafficQuery) (PublishedTraffic, error) {
	report := PublishedTraffic{Source: q.Source, Start: q.Start}
	var flights []traffic.Flight
	switch q.Source {
	case TrafficSourceHistorical:
		report.End = q.Start.Add(HistoricalFlightWindow)
		var err error
		if flights, err = ss.historicalFlights(q.Start, 1); err != nil {
			return PublishedTraffic{}, err
		}

	case TrafficSourceTimetable:
		catalog, err := traffic.LoadAirportTimetables(q.TimetableAirport)
		if err != nil {
			return PublishedTraffic{}, err
		}
		timetable, ok := catalog.Find(q.TimetableAirport, q.TimetableID)
		if !ok {
			return PublishedTraffic{}, fmt.Errorf("timetable %q not found for %s",
				q.TimetableID, q.TimetableAirport)
		}
		minute, err := TimetableStartMinute(q.Start, q.TimetableAirport)
		if err != nil {
			return PublishedTraffic{}, err
		}
		// A timetable is a day's worth of flights however fast a sim reads
		// through them, so the report shows the whole cycle at its own pace.
		lc := ss.LaunchConfig
		lc.TimetableStartMinute = minute
		lc.PublishedDepartureRateScale, lc.PublishedArrivalRateScale = 1, 1
		report.End = q.Start.Add(24 * time.Hour)
		flights = timetableFlights(NewSimTime(q.Start), timetable, &lc)

	default:
		return PublishedTraffic{}, fmt.Errorf("%s traffic comes from the launch config's rates, "+
			"so there are no published flights to report", q.Source)
	}

	flights, rotorcraft := dropRotorcraft(flights)
	flights, repeated := dropRepeatedRecords(flights)
	flights, returned := dropReturnedLegs(flights)
	note := func(n int, format string) {
		if n > 0 {
			report.Excluded = append(report.Excluded, fmt.Sprintf(format, n))
		}
	}
	note(rotorcraft, "%d helicopter flights")
	note(repeated, "%d flights the data records more than once")
	note(returned, "%d arrivals from another of the facility's airports, flying as departures")

	routed := makeRoutedPairs()
	report.Flights = make([]PublishedFlight, len(flights))
	for i, flight := range flights {
		pf := PublishedFlight{Callsign: flight.Callsign, AircraftType: flight.AircraftType,
			Departure: flight.Departure, Published: flight.Time()}
		if flight.Departure {
			pf.From, pf.To = flight.Airport, flight.Other
			ss.reportDeparture(&pf, routed)
		} else {
			pf.From, pf.To = flight.Other, flight.Airport
			ss.reportArrival(&pf, routed)
		}
		report.Flights[i] = pf
	}
	return report, nil
}

// reportDeparture fills in the runway, exit, and route a published departure
// would leave on, or why no runway the scenario is launching can fly it.
func (ss *CommonState) reportDeparture(pf *PublishedFlight, routed routedPairs) {
	e := ScheduledDeparture{ScheduledFlight: ScheduledFlight{
		Callsign:         pf.Callsign,
		AircraftType:     pf.AircraftType,
		DepartureAirport: pf.From,
		ArrivalAirport:   pf.To,
	}}
	runway, _, choice, err := ss.resolvePublishedDepartureRunway(&e, routed, nil)
	if err != nil {
		pf.Outcome = util.Select(errors.Is(err, errNoDepartureRunwayEnabled),
			FlightWaiting, FlightDropped)
		pf.Problem, pf.Route = err.Error(), choice.route
		return
	}
	placement := ss.placement(choice, pf.From, pf.To)
	pf.Runway, pf.Exit = runway, placement.dep.Exit
	pf.Route, pf.How = placement.dep.Route, placement.how
}

// reportArrival fills in the flow and arrival a published arrival would come in
// on, or why the scenario has no way to fly it.
func (ss *CommonState) reportArrival(pf *PublishedFlight, routed routedPairs) {
	placement, err := ss.placeArrival(pf.To, pf.From, pf.AircraftType, routed)
	if err != nil {
		pf.Outcome, pf.Problem, pf.Route = FlightDropped, err.Error(), placement.filedRoute
		return
	}
	pf.Flow, pf.ArrivalIndex = placement.group, placement.index
	pf.Route, pf.How, pf.Substitute = placement.filedRoute, placement.how, placement.substitute
	if flow, ok := ss.InboundFlows[placement.group]; ok && placement.index < len(flow.Arrivals) {
		pf.STAR = flow.Arrivals[placement.index].STAR
	}
}

///////////////////////////////////////////////////////////////////////////
// Flight recording

// maxRecordedFlightSeconds bounds how far a recording run drives the clock. A
// departure or arrival is flown in well under an hour; anything still airborne
// after this is going around in circles and the run stops rather than never
// returning.
const maxRecordedFlightSeconds = 4 * 60 * 60

// FlightSample is where an aircraft was at one second of a recorded flight.
type FlightSample struct {
	Location    math.Point2LL
	Altitude    float32
	Groundspeed float32
}

// FlightRecording is an aircraft's whole flight, from the second the run found
// it flying to the second the sim deleted it. The samples are one second apart
// starting at Start, so playback can index straight into them.
type FlightRecording struct {
	Callsign     av.ADSBCallsign
	AircraftType string
	Departure    bool

	DepartureAirport av.ICAOAirportCode
	ArrivalAirport   av.ICAOAirportCode
	SID              string
	STAR             string
	Route            string

	Start   Time
	Samples []FlightSample
}

// End is the moment the aircraft was last seen.
func (r FlightRecording) End() Time {
	return r.Start.Add(time.Duration(max(len(r.Samples)-1, 0)) * time.Second)
}

// FlightRecordings is what one recording run produced.
type FlightRecordings struct {
	Recordings []FlightRecording
	// Truncated says the run hit its limit with aircraft still flying, so
	// their recordings stop short of where they would have ended.
	Truncated bool
}

// RecordFlights runs the sim forward a second at a time until every aircraft it
// is flying has been deleted, recording where each one was at each second. It
// is how a tool watches a route be flown without having to sit through it: the
// whole flight is simulated at once and played back afterwards, which is also
// what makes going back in time possible when the sim itself only goes forward.
func (s *Sim) RecordFlights() FlightRecordings {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var result FlightRecordings
	recordings := make(map[av.ADSBCallsign]*FlightRecording)
	seconds := 0
	for ; len(s.Aircraft) > 0; seconds++ {
		if seconds == maxRecordedFlightSeconds {
			result.Truncated = true
			break
		}
		for _, callsign := range util.SortedMapKeys(s.Aircraft) {
			ac := s.Aircraft[callsign]
			r, ok := recordings[callsign]
			if !ok {
				r = s.newRecording(ac)
				recordings[callsign] = r
			}
			r.Samples = append(r.Samples, FlightSample{
				Location:    ac.Position(),
				Altitude:    ac.Altitude(),
				Groundspeed: ac.GS(),
			})
		}

		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}

	// The clock has jumped by however long the flights took; without this the
	// sim would try to make up all of it on its next update.
	s.updateTimeSlop = 0
	s.lastSimUpdateTime = time.Now()
	s.lastControlCommandTime = time.Now()
	s.publish()

	for _, callsign := range util.SortedMapKeys(recordings) {
		result.Recordings = append(result.Recordings, *recordings[callsign])
	}
	return result
}

func (s *Sim) newRecording(ac *Aircraft) *FlightRecording {
	return &FlightRecording{
		Callsign:         ac.ADSBCallsign,
		AircraftType:     ac.FlightPlan.AircraftType,
		Departure:        ac.IsDeparture(),
		DepartureAirport: ac.FlightPlan.DepartureAirport,
		ArrivalAirport:   ac.FlightPlan.ArrivalAirport,
		SID:              ac.SID,
		STAR:             ac.STAR,
		Route:            ac.FlightPlan.Route,
		Start:            s.State.SimTime,
	}
}
