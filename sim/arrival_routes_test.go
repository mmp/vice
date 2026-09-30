// sim/arrival_routes_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
)

// testCandidates makes the arrivals into candidates the way candidateArrivals
// would, all in one inbound flow.
func testCandidates(arrivals []av.Arrival) []candidateArrival {
	candidates := make([]candidateArrival, len(arrivals))
	for i := range arrivals {
		candidates[i] = candidateArrival{group: "TEST", index: i, arr: &arrivals[i]}
	}
	return candidates
}

// placeArrivalTestSim builds a Sim landing the given arrivals at the airport in
// one "TEST" inbound flow.
func placeArrivalTestSim(airport av.ICAOAirportCode, arrivals []av.Arrival, ap *av.Airport) *Sim {
	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = 45
	s.State.InboundFlows = map[string]*av.InboundFlow{"TEST": {Arrivals: arrivals}}
	s.State.LaunchConfig.InboundFlowEnabled = map[string]map[string]bool{"TEST": {string(airport): true}}
	if ap != nil {
		s.State.Airports = map[av.ICAOAirportCode]*av.Airport{airport: ap}
	}
	return s
}

// The route database says how a city pair is really flown--KORF to KJFK
// arrives on the CAMRN--which is enough to place a published arrival on the
// STAR the scenario works even though nothing names the origin.
func TestPlaceArrivalFilesTheRealRoute(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STAR: "LENDY8", Airports: []av.ICAOAirportCode{"KJFK"}},
		{STAR: "CAMRN5", Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	s := placeArrivalTestSim("KJFK", arrivals, nil)

	placement, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KORF->KJFK: %v", err)
	}
	if placement.index != 1 {
		t.Errorf("placed on arrival %d, expected the CAMRN5 at 1", placement.index)
	}
	if !strings.Contains(placement.filedRoute, "CAMRN") {
		t.Errorf("filed route %q doesn't mention the CAMRN", placement.filedRoute)
	}
}

// A route whose STAR no active arrival flies drops the flight rather than
// shoehorning it onto another flow: a scenario working one gate of an airport
// must not be handed every flight bound for the others.
func TestPlaceArrivalDropsInactiveSTAR(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STAR: "PARCH4", Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	s := placeArrivalTestSim("KJFK", arrivals, nil)

	// KORF to KJFK really arrives on the CAMRN, which this scenario doesn't
	// work.
	_, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if !errors.Is(err, errArrivalSTARInactive) {
		t.Errorf("placeArrival = %v, expected the flight dropped for its inactive STAR", err)
	}
}

// A final takes traffic from every STAR that funnels into it: once a flight is
// on the final, which STAR it flew is no part of the work left. An arrival
// naming its feeds takes them all, without flying any of them itself.
func TestPlaceArrivalTakesSTARFeeds(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STARFeeds: []string{"PARCH4"}, Airports: []av.ICAOAirportCode{"KJFK"}},
		{STARFeeds: []string{"CAMRN5", "LENDY8"}, Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	s := placeArrivalTestSim("KJFK", arrivals, nil)

	// KORF to KJFK really arrives on the CAMRN.
	placement, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KORF->KJFK: %v", err)
	}
	if placement.index != 1 {
		t.Errorf("placed on arrival %d, expected the final fed by the CAMRN at 1", placement.index)
	}
}

// The feeds say which STARs an arrival takes and no more; a flight off one it
// doesn't name is still dropped.
func TestPlaceArrivalDropsUnfedSTAR(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STARFeeds: []string{"PARCH4", "LENDY8"}, Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	s := placeArrivalTestSim("KJFK", arrivals, nil)

	_, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if !errors.Is(err, errArrivalSTARInactive) {
		t.Errorf("placeArrival = %v, expected the flight dropped for its unfed STAR", err)
	}
}

// A route the scenario gives for the city pair beats whatever the route
// database says: KORF to KJFK really arrives on the CAMRN, but the scenario
// says in so many words to fly it in on the PARCH.
func TestPlaceArrivalPrefersScenarioRoute(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STAR: "PARCH4", Airports: []av.ICAOAirportCode{"KJFK"}},
		{STAR: "CAMRN5", Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	ap := &av.Airport{TrafficRoutes: av.TrafficRoutes{
		Arrivals: map[av.ICAOAirportCode]av.TrafficRouteSet{
			"KORF": {av.TrafficRoute{Route: "ORF SIE PARCH4"}},
		},
	}}
	s := placeArrivalTestSim("KJFK", arrivals, ap)

	placement, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KORF->KJFK: %v", err)
	}
	if placement.index != 0 {
		t.Errorf("placed on arrival %d, expected the scenario route's PARCH4 at 0", placement.index)
	}
	if placement.how != "scenario route" {
		t.Errorf("how = %q, expected the scenario route to decide", placement.how)
	}
	if placement.filedRoute != "ORF SIE PARCH4" {
		t.Errorf("filed route = %q, expected the scenario's", placement.filedRoute)
	}
}

// An origin no route covers is flown the way the nearest airport that one does
// cover is flown: Kill Devil Hills has no route to JFK, but Norfolk 59nm away
// arrives on the CAMRN5, and that is the gate a flight from the Outer Banks
// should come in through.
func TestPlaceArrivalSubstitutesANearbyRoutedOrigin(t *testing.T) {
	db.InitDB()

	arrivals := []av.Arrival{
		{STAR: "CAMRN5", Airports: []av.ICAOAirportCode{"KJFK"}},
	}
	s := placeArrivalTestSim("KJFK", arrivals, nil)

	placement, err := s.State.placeArrival("KJFK", "KFFA", "B738", makeRoutedPairs())
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KFFA->KJFK: %v", err)
	}
	if placement.index != 0 {
		t.Errorf("placed on arrival %d, expected the CAMRN5 at 0", placement.index)
	}
	if placement.substitute != "KORF" {
		t.Errorf("substitute origin = %q, expected KORF", placement.substitute)
	}
	// Norfolk's route says which gate to use and nothing more; the flight came
	// from Kill Devil Hills and must not file a route starting at ORF.
	if placement.filedRoute != "" {
		t.Errorf("filed route = %q, want the substitute's route left unfiled", placement.filedRoute)
	}

	// Zurich's nearest airport with a JFK route is Bangor, which is no neighbor
	// of it and no statement of how traffic from Europe arrives. That falls
	// through to the great-circle geometry, with no origin standing in.
	placement, err = s.State.placeArrival("KJFK", "LSZH", "B738", makeRoutedPairs())
	if err == nil && placement.substitute != "" {
		t.Errorf("substituted %q for Zurich, expected no stand-in that far off",
			placement.substitute)
	}
}

// An arrival with no route anywhere near its origin comes in through the gate
// nearest the great circle it actually flies--but only a gate pointing
// plausibly toward the origin; anything else drops the flight.
func TestPlaceArrivalUsesTheGreatCircleGate(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{Airports: map[av.ICAOAirportCode]db.Airport{
		"KTST": {Id: "KTST", Location: math.Point2LL{-93.2, 44.9}},
		"KFAR": {Id: "KFAR", Location: math.Point2LL{-103.0, 44.0}}, // west of KTST
		"KSTH": {Id: "KSTH", Location: math.Point2LL{-93.2, 38.0}},  // south of KTST
	}}
	t.Cleanup(func() { db.DB = oldDB })

	arrivals := []av.Arrival{
		{Airports: []av.ICAOAirportCode{"KTST"}, Description: "north gate",
			Waypoints: av.WaypointArray{{Fix: "NORTH", Location: math.Point2LL{-94.0, 46.5}}}},
		{Airports: []av.ICAOAirportCode{"KTST"}, Description: "west gate",
			Waypoints: av.WaypointArray{{Fix: "WESTG", Location: math.Point2LL{-95.0, 44.6}}}},
	}
	s := placeArrivalTestSim("KTST", arrivals, nil)

	placement, err := s.State.placeArrival("KTST", "KFAR", "B738", routedPairs{})
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KFAR->KTST: %v", err)
	}
	if placement.index != 1 {
		t.Errorf("placed on arrival %d, expected the west gate at 1", placement.index)
	}
	if placement.how != "great-circle gate" {
		t.Errorf("how = %q, expected the great-circle gate to decide", placement.how)
	}

	// Nothing comes in from the south, so a southern flight is dropped rather
	// than handed to whichever gate happens to be least wrong.
	if _, err := s.State.placeArrival("KTST", "KSTH", "B738", routedPairs{}); !errors.Is(err, errNoPlausibleArrival) {
		t.Errorf("placeArrival from the south = %v, expected the flight dropped", err)
	}
}

// A published arrival files within the band the pair's recent filings were
// seen at, and no lower than the STAR it comes in on requires.
func TestPlaceArrivalCarriesCruiseLimits(t *testing.T) {
	crossing := av.Waypoint{Fix: "MISN"}
	crossing.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(21000))

	oldDB := db.DB
	db.DB = &db.StaticDatabase{
		Airports: map[av.ICAOAirportCode]db.Airport{
			"KTST": {Id: "KTST", Location: math.Point2LL{-93.2, 44.9}, STARs: map[string]av.STAR{
				"TSTR4": {Transitions: map[string]av.WaypointArray{"MISN": {crossing}}},
			}},
			"KFAR": {Id: "KFAR", Location: math.Point2LL{-103.0, 44.0}},
		},
		ScrapedRoutes: map[db.AirportPair][]av.ScrapedRoute{
			{From: "KFAR", To: "KTST"}: {{Route: "MISN TSTR4", Count: 100,
				MinAltitude: 8000, MaxAltitude: 34000}},
		},
	}
	t.Cleanup(func() { db.DB = oldDB })

	s := placeArrivalTestSim("KTST", []av.Arrival{{STAR: "TSTR4", Airports: []av.ICAOAirportCode{"KTST"}}}, nil)
	placement, err := s.State.placeArrival("KTST", "KFAR", "B738", routedPairs{})
	if err != nil {
		t.Fatalf("placeArrival found no way to fly KFAR->KTST: %v", err)
	}
	want := CruiseLimits{Floor: 21000, Low: 8000, High: 34000}
	if placement.cruise != want {
		t.Errorf("cruise = %+v, want %+v", placement.cruise, want)
	}
}

// The CIFP transition a route joins a STAR at says which of the STAR's
// arrivals the flight reaches: entering LUCKI1 at TTRUE leads through MOMAR,
// not HEELX.
func TestMatchArrivalRouteWalksTheTransition(t *testing.T) {
	star := func(fixes ...string) av.WaypointArray {
		var wps av.WaypointArray
		for _, f := range fixes {
			wps = append(wps, av.Waypoint{Fix: f})
		}
		return wps
	}
	oldDB := db.DB
	db.DB = &db.StaticDatabase{Airports: map[av.ICAOAirportCode]db.Airport{
		"KTST": {Id: "KTST", STARs: map[string]av.STAR{
			"LUCKI1": {Transitions: map[string]av.WaypointArray{
				"TTRUE": star("TTRUE", "WESTF", "MOMAR", "LUCKI"),
				"SOFII": star("SOFII", "EASTF", "HEELX", "LUCKI"),
			}},
		}},
	}}
	t.Cleanup(func() { db.DB = oldDB })

	arrivals := []av.Arrival{
		{STAR: "LUCKI1", Airports: []av.ICAOAirportCode{"KTST"},
			Waypoints: av.WaypointArray{{Fix: "MOMAR"}, {Fix: "LUCKI"}}},
		{STAR: "LUCKI1", Airports: []av.ICAOAirportCode{"KTST"},
			Waypoints: av.WaypointArray{{Fix: "HEELX"}, {Fix: "LUCKI"}}},
	}
	candidates := testCandidates(arrivals)

	for _, tc := range []struct {
		route string
		want  int
	}{
		// Both arrivals fly through LUCKI, but the one joined soonest after
		// the entry fix is the flow the flight is on.
		{"KORD PWE TTRUE LUCKI1 KTST", 0},
		{"KORD PWE SOFII LUCKI1 KTST", 1},
	} {
		c, err := matchArrivalRoute(candidates, "B738", tc.route, "KTST", "KORD")
		if err != nil {
			t.Errorf("%s: %v", tc.route, err)
		} else if c.index != tc.want {
			t.Errorf("%s: matched arrival %d, expected %d", tc.route, c.index, tc.want)
		}
	}

	// A route filing a stale revision of the STAR still matches it.
	if c, err := matchArrivalRoute(candidates, "B738", "KORD TTRUE LUCKI2 KTST", "KTST", "KORD"); err != nil {
		t.Errorf("stale revision: %v", err)
	} else if c.index != 0 {
		t.Errorf("stale revision matched arrival %d, expected 0", c.index)
	}
}

// suitableArrivals rules out the arrivals an aircraft can't fly, whether by
// the arrival's own aircraft classes or by its altitudes against the
// aircraft's ceiling.
func TestSuitableArrivals(t *testing.T) {
	db.InitDB()

	jets := av.AircraftClassHeavyJet | av.AircraftClassNonheavyJet
	arrivals := []av.Arrival{
		{Description: "any"},
		{Description: "jets only", Aircraft: jets},
		{Description: "high", InitialAltitudes: []int{30000}},
		// The filed cruise is flight-strip decoration and must not exclude
		// anything, whatever the aircraft's ceiling.
		{Description: "high filed cruise", CruiseAltitudes: []int{39000}},
	}
	candidates := testCandidates(arrivals)

	indices := func(cs []candidateArrival) []int {
		var idx []int
		for _, c := range cs {
			idx = append(idx, c.index)
		}
		return idx
	}

	if got := indices(suitableArrivals(candidates, "B738")); len(got) != 4 {
		t.Errorf("B738 suits %v, expected all four", got)
	}
	// A Skyhawk is neither a jet nor able to reach FL300, but the filed
	// cruise says nothing about what it can fly.
	if got := indices(suitableArrivals(candidates, "C172")); !slices.Equal(got, []int{0, 3}) {
		t.Errorf("C172 suits %v, expected the unrestricted and filed-cruise arrivals", got)
	}
}

func TestHistoricalArrivalRouting(t *testing.T) {
	db.InitDB()
	arrivals := []av.Arrival{{STAR: "OLD1", Airports: []av.ICAOAirportCode{"KJFK"},
		Waypoints: av.WaypointArray{{Fix: "GATE", Location: math.Point2LL{-74, 39}}}}}
	s := placeArrivalTestSim("KJFK", arrivals, nil)
	s.State.HistoricalScenario = true
	p, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if err != nil || p.how != "historical great-circle gate" || p.filedRoute != "" {
		t.Fatalf("historical placement: %+v, %v", p, err)
	}
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{"KJFK": {TrafficRoutes: av.TrafficRoutes{Arrivals: map[av.ICAOAirportCode]av.TrafficRouteSet{"KORF": {{Route: "OLD1"}}}}}}
	p, err = s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs())
	if err != nil || p.how != "scenario route" || p.filedRoute != "OLD1" {
		t.Fatalf("explicit placement: %+v, %v", p, err)
	}
	delete(s.State.Airports, "KJFK")
	s.State.InboundFlows["TEST"].Arrivals[0].InitialAltitudes = []int{99999}
	if _, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs()); !errors.Is(err, errNoSuitableArrival) {
		t.Fatalf("unsuitable altitude: %v", err)
	}
	s.State.InboundFlows["TEST"].Arrivals[0].InitialAltitudes = nil
	s.State.LaunchConfig.InboundFlowEnabled["TEST"]["KJFK"] = false
	if _, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs()); !errors.Is(err, errFlowDisabled) {
		t.Fatalf("disabled flow: %v", err)
	}
}

func TestHistoricalArrivalOverrides(t *testing.T) {
	db.InitDB()
	arrivals := []av.Arrival{
		{STAR: "OLD1", Airports: []av.ICAOAirportCode{"KJFK"}, InitialAltitudes: []int{99999}},
		{STAR: "OTHER1", Airports: []av.ICAOAirportCode{"KJFK"}, Waypoints: av.WaypointArray{{Fix: "GATE", Location: math.Point2LL{-74, 39}}}},
	}
	candidates := testCandidates(arrivals)
	p, route, err := matchHistoricalArrivalRoutes(candidates, candidates, "B738", []string{"OLD1", "OTHER1 KJFK"}, "KJFK", "KORF")
	if err != nil || p.index != 1 || route != "OTHER1 KJFK" {
		t.Fatalf("alternative route: %+v %q %v", p, route, err)
	}
	p, route, err = matchHistoricalArrivalRoutes(candidates, candidates, "B738", []string{"DIRECT", "OTHER1"}, "KJFK", "KORF")
	if err != nil || route != "DIRECT" {
		t.Fatalf("route order: %+v %q %v", p, route, err)
	}
	arrivals[0].InitialAltitudes = nil
	s := placeArrivalTestSim("KJFK", arrivals[1:], &av.Airport{TrafficRoutes: av.TrafficRoutes{Arrivals: map[av.ICAOAirportCode]av.TrafficRouteSet{"KORF": {{Route: "OLD1"}}}}})
	s.State.HistoricalScenario = true
	s.State.InboundFlows["DISABLED"] = &av.InboundFlow{Arrivals: arrivals[:1]}
	s.State.LaunchConfig.InboundFlowEnabled["DISABLED"] = map[string]bool{"KJFK": false}
	if _, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs()); !errors.Is(err, errFlowDisabled) {
		t.Fatalf("explicit disabled STAR: %v", err)
	}
}

func TestHistoricalArrivalDirectionAndClass(t *testing.T) {
	db.InitDB()
	arrivals := []av.Arrival{{STAR: "OLD1", Airports: []av.ICAOAirportCode{"KJFK"}, Waypoints: av.WaypointArray{{Fix: "NORTH", Location: math.Point2LL{-73.8, 43}}}}}
	s := placeArrivalTestSim("KJFK", arrivals, nil)
	s.State.HistoricalScenario = true
	if _, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs()); !errors.Is(err, errNoPlausibleArrival) {
		t.Fatalf("opposite direction: %v", err)
	}
	s.State.InboundFlows["TEST"].Arrivals[0].Aircraft = av.AircraftClassProp
	if _, err := s.State.placeArrival("KJFK", "KORF", "B738", makeRoutedPairs()); !errors.Is(err, errNoSuitableArrival) {
		t.Fatalf("aircraft class: %v", err)
	}
}
