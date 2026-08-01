package sim

import (
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
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

func TestArrivalListingOrigin(t *testing.T) {
	arrivals := []av.Arrival{
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						AirlineSpecifier: av.AirlineSpecifier{
							ICAO: "DAL",
						},
						Airport: "KORD",
					},
				},
			},
		},
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						AirlineSpecifier: av.AirlineSpecifier{
							ICAO: "UAL",
						},
						Airport: "KDEN",
					},
				},
			},
		},
	}

	c, ok := arrivalListingOrigin(testCandidates(arrivals), "KMSP", "KDEN")
	if !ok {
		t.Fatal("arrivalListingOrigin didn't find an arrival from KDEN")
	}
	if c.index != 1 {
		t.Fatalf("arrivalListingOrigin returned arrival %d, expected 1", c.index)
	}
}

func TestArrivalListingOriginRejectsUnknownOrigin(t *testing.T) {
	arrivals := []av.Arrival{
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						Airport: "KORD",
					},
				},
			},
		},
	}

	if _, ok := arrivalListingOrigin(testCandidates(arrivals), "KMSP", "KLAX"); ok {
		t.Fatal("arrivalListingOrigin accepted an unknown origin")
	}
}

// The route database says how a city pair is really flown, which is enough to
// place a published arrival on a STAR the scenario works even when no arrival
// lists the origin.
func TestArrivalForCityPair(t *testing.T) {
	av.InitDB()

	arrivals := []av.Arrival{
		{STAR: "LENDY8", Airports: []string{"KJFK"}},
		{STAR: "CAMRN5", Airports: []string{"KJFK"}},
	}

	c, route, ok := arrivalForCityPair(testCandidates(arrivals), "KJFK", "KORF", "B738")
	if !ok {
		t.Fatal("arrivalForCityPair found no route from KORF to KJFK")
	}
	if c.arr.STAR != "CAMRN5" {
		t.Errorf("arrivalForCityPair picked %s, expected CAMRN5", c.arr.STAR)
	}
	if !strings.Contains(route, "CAMRN5") {
		t.Errorf("filed route %q doesn't mention CAMRN5", route)
	}
}

// The route into the terminal area is the match furthest along the route; an
// earlier fix is only somewhere the flight passed on the way in. KORF to KJFK
// is "ORF HOLND1 KALDA Q108 SIE CAMRN5 JFK", so an arrival picking traffic up
// at KALDA loses to the one flying the CAMRN5.
func TestArrivalForCityPairPrefersTheLatestFix(t *testing.T) {
	av.InitDB()

	arrivals := []av.Arrival{
		{Airports: []string{"KJFK"}, Waypoints: av.WaypointArray{{Fix: "KALDA"}, {Fix: "SIE"}}},
		{STAR: "CAMRN5", Airports: []string{"KJFK"}},
	}

	c, _, ok := arrivalForCityPair(testCandidates(arrivals), "KJFK", "KORF", "B738")
	if !ok {
		t.Fatal("arrivalForCityPair found no route from KORF to KJFK")
	}
	if c.index != 1 {
		t.Errorf("picked arrival %d, expected the CAMRN5 at 1", c.index)
	}
}

// The destination identifier ends every route into an airport, so an arrival
// that happens to fly over the airport's own navaid must not match on it.
func TestArrivalForCityPairIgnoresTheAirportIdentifier(t *testing.T) {
	av.InitDB()

	arrivals := []av.Arrival{
		{Airports: []string{"KJFK"}, Waypoints: av.WaypointArray{{Fix: "JFK"}}},
	}

	if _, _, ok := arrivalForCityPair(testCandidates(arrivals), "KJFK", "KORF", "B738"); ok {
		t.Error("matched on the destination identifier at the end of the route")
	}
}

// An origin no route covers is flown the way the nearest airport that one does
// cover is flown: Kill Devil Hills has no route to JFK, but Norfolk 59nm away
// arrives on the CAMRN5, and that is the gate a flight from the Outer Banks
// should come in through rather than whichever downwind happens to point that
// way.
func TestPlaceArrivalSubstitutesANearbyRoutedOrigin(t *testing.T) {
	av.InitDB()

	arrivals := []av.Arrival{
		// A feeder that turns local traffic onto a downwind: it points at Kill
		// Devil Hills more precisely than the gate does, and must still lose.
		{Airports: []string{"KJFK"}, Description: "downwind",
			Waypoints: av.WaypointArray{{Fix: "DPK"}}},
		{STAR: "CAMRN5", Airports: []string{"KJFK"}},
	}
	s := &Sim{State: &CommonState{
		NmPerLongitude: 45,
		InboundFlows:   map[string]*av.InboundFlow{"TEST": {Arrivals: arrivals}},
		DynamicState: DynamicState{LaunchConfig: LaunchConfig{
			InboundFlowEnabled: map[string]map[string]bool{"TEST": {"KJFK": true}}}},
	}}

	placement, ok := s.placeArrival("KJFK", "KFFA", "B738", makeRoutedPairs().originsByDestination)
	if !ok {
		t.Fatal("placeArrival found no way to fly KFFA->KJFK")
	}
	if placement.index != 1 {
		t.Errorf("placed on arrival %d, expected the CAMRN5 at 1", placement.index)
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
	// through to the geometry instead, which has the CAMRN5 pointing the wrong
	// way and so declines to fly it at all here.
	placement, ok = s.placeArrival("KJFK", "LSZH", "B738", makeRoutedPairs().originsByDestination)
	if ok && placement.substitute != "" {
		t.Errorf("substituted %q for Zurich, expected no stand-in that far off",
			placement.substitute)
	}
}

// A flight at an airport the scenario lands no traffic at must not hold up the
// arrivals behind it: the queue is shared across every airport a facility
// works, so one stuck at the head would stall all of them.
func TestZeroRateArrivalsDoNotBlock(t *testing.T) {
	day := av.FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC))
	arrival := func(airport, callsign, group string) publishedFlight {
		return publishedFlight{
			flight: av.Flight{Airport: airport, Callsign: callsign, Other: "KATL",
				AircraftType: "B738", Day: day, Minute: 8 * 60},
			placement: arrivalPlacement{group: group},
		}
	}

	p := &publishedTrafficProvider{arrivals: []publishedFlight{
		arrival("KFRG", "DAL1", "PUCKY1"), // PUCKY1 lands nothing at KFRG
		arrival("KFRG", "DAL2", "PUCKY1"),
		arrival("KJFK", "DAL3", "PUCKY1"), // but it does at KJFK
		arrival("KJFK", "DAL4", "CAMRN5"), // and this one belongs to another flow
	}}

	s := &Sim{State: &CommonState{DynamicState: DynamicState{
		LaunchConfig: LaunchConfig{InboundFlowEnabled: map[string]map[string]bool{
			"PUCKY1": {"KFRG": false, "KJFK": true},
			"CAMRN5": {"KJFK": true},
		}}}}}
	rates := map[string]float32{"KFRG": 0, "KJFK": 12}

	index := p.nextArrivalFor(s, "PUCKY1", rates)
	if index < 0 {
		t.Fatal("PUCKY1 found nothing to fly")
	}
	if got := p.arrivals[index].flight.Callsign; got != "DAL3" {
		t.Errorf("PUCKY1 picked %s, expected DAL3 with the KFRG arrivals discarded", got)
	}
	if p.discardedArrivals["KFRG"] != 2 {
		t.Errorf("discarded %v, expected both KFRG arrivals", p.discardedArrivals)
	}

	// The other flow steps over PUCKY1's rather than queueing behind it.
	if index := p.nextArrivalFor(s, "CAMRN5", rates); index < 0 ||
		p.arrivals[index].flight.Callsign != "DAL4" {
		t.Errorf("CAMRN5 did not find its own arrival")
	}
}
