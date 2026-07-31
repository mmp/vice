// sim/spawn_departures_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

const testNmPerLongitude = 60

// installIntersectingRunwayFixture installs a synthetic airport "XTST" into
// av.DB with (in nm coordinates): runway 9/27 running east from (0,0) to
// (2,0); runway 36/18 running north from (1,-1) to (1,1), crossing 9 at
// (1,0); runway 8/26 parallel to 9, 5nm north; and runway 1/19 running
// north from (2.8,0.3) to (2.8,2), crossing 9's extended centerline 0.8nm
// past its east end.
func installIntersectingRunwayFixture(t *testing.T) {
	t.Helper()

	const airport = "XTST"
	orig, ok := av.DB.Airports[airport]
	t.Cleanup(func() {
		if ok {
			av.DB.Airports[airport] = orig
		} else {
			delete(av.DB.Airports, airport)
		}
	})

	nm := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, testNmPerLongitude) }
	av.DB.Airports[airport] = av.FAAAirport{
		Id: airport,
		Runways: []av.Runway{
			{Id: "9", Threshold: nm(0, 0), Heading: 90},
			{Id: "27", Threshold: nm(2, 0), Heading: 270},
			{Id: "36", Threshold: nm(1, -1), Heading: 360},
			{Id: "18", Threshold: nm(1, 1), Heading: 180},
			{Id: "8", Threshold: nm(0, 5), Heading: 90},
			{Id: "26", Threshold: nm(2, 5), Heading: 270},
			{Id: "1", Threshold: nm(2.8, 0.3), Heading: 10},
			{Id: "19", Threshold: nm(2.8, 2), Heading: 190},
		},
	}
}

func TestRunwayIntersectionPoint(t *testing.T) {
	installIntersectingRunwayFixture(t)

	pt, ok := av.RunwayIntersectionPoint("XTST", "9", "36", testNmPerLongitude, 0)
	if !ok {
		t.Fatal("no intersection found for crossing runways 9/36")
	}
	if p := math.LL2NM(pt, testNmPerLongitude); math.Abs(p[0]-1) > 0.01 || math.Abs(p[1]) > 0.01 {
		t.Errorf("intersection point = %v, want (1, 0)", p)
	}

	// Dotted suffixes resolve to the physical runways.
	if _, ok := av.RunwayIntersectionPoint("XTST", "9.All", "36.West", testNmPerLongitude, 0); !ok {
		t.Error("no intersection found with dotted-suffix runway IDs")
	}

	// Same runway, opposite direction, and parallel runways don't intersect.
	for _, pair := range [][2]av.RunwayID{{"9", "9"}, {"9", "27"}, {"9", "8"}} {
		if _, ok := av.RunwayIntersectionPoint("XTST", pair[0], pair[1], testNmPerLongitude, 1); ok {
			t.Errorf("unexpected intersection for %s/%s", pair[0], pair[1])
		}
	}

	// Runway 1 crosses 9's extended centerline 0.8nm past its end, so it
	// only counts as intersecting with enough slop.
	if _, ok := av.RunwayIntersectionPoint("XTST", "9", "1", testNmPerLongitude, 0.5); ok {
		t.Error("unexpected intersection for 9/1 with 0.5nm slop")
	}
	if _, ok := av.RunwayIntersectionPoint("XTST", "9", "1", testNmPerLongitude, 1); !ok {
		t.Error("no intersection found for 9/1 with 1nm slop")
	}
}

func TestIntersectingRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	rwys := av.IntersectingRunways("XTST", "9", testNmPerLongitude, 0)
	for _, want := range []string{"36", "18"} {
		if !slices.Contains(rwys, want) {
			t.Errorf("IntersectingRunways = %v, missing %q", rwys, want)
		}
	}
	for _, notWant := range []string{"9", "27", "8", "26", "1", "19"} {
		if slices.Contains(rwys, notWant) {
			t.Errorf("IntersectingRunways = %v, shouldn't include %q", rwys, notWant)
		}
	}
}

func TestDepartureIntersectionHelpers(t *testing.T) {
	installIntersectingRunwayFixture(t)

	s := &Sim{
		State:    &CommonState{},
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),
	}
	s.State.NmPerLongitude = testNmPerLongitude

	pt, ok := av.RunwayIntersectionPoint("XTST", "9", "36", testNmPerLongitude, 0)
	if !ok {
		t.Fatal("no intersection found for crossing runways 9/36")
	}

	// The intersection is 1nm down runway 9.
	for _, c := range []struct {
		dist float32
		want bool
	}{{0.5, true}, {1.5, false}, {-1, false}} {
		dep := DepartureAircraft{AirborneDistance: c.dist}
		if got := s.airborneBeforeIntersection(dep, "XTST", "9", pt); got != c.want {
			t.Errorf("airborneBeforeIntersection(AirborneDistance %v) = %v, want %v", c.dist, got, c.want)
		}
	}

	// A point behind the threshold is never crossed on the ground.
	behind := math.NM2LL([2]float32{-0.5, 0}, testNmPerLongitude)
	if s.airborneBeforeIntersection(DepartureAircraft{AirborneDistance: 0.1}, "XTST", "9", behind) {
		t.Error("airborneBeforeIntersection: point behind the threshold")
	}

	ac := &Aircraft{ADSBCallsign: "TST1"}
	s.Aircraft["TST1"] = ac
	dep := DepartureAircraft{ADSBCallsign: "TST1"}

	ac.Nav.FlightState.Position = math.NM2LL([2]float32{0.5, 0}, testNmPerLongitude)
	if s.departureHasPassedPoint(dep, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: aircraft is short of the intersection")
	}
	ac.Nav.FlightState.Position = math.NM2LL([2]float32{1.2, 0}, testNmPerLongitude)
	if !s.departureHasPassedPoint(dep, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: aircraft is past the intersection")
	}
	if !s.departureHasPassedPoint(DepartureAircraft{ADSBCallsign: "GONE"}, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: deleted aircraft should count as passed")
	}
}

func TestCanLaunchIntersectingRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	prevAc := &Aircraft{ADSBCallsign: "PRV1", FlightPlan: av.FlightPlan{AircraftType: "B738", Rules: av.FlightRulesIFR}}
	depAc := &Aircraft{ADSBCallsign: "DEP1", FlightPlan: av.FlightPlan{AircraftType: "B738", Rules: av.FlightRulesIFR}}

	rwy9, rwy36, rwy8 := &RunwayLaunchState{}, &RunwayLaunchState{}, &RunwayLaunchState{}

	s := &Sim{
		lg:       log.New(true, "error", t.TempDir()),
		State:    &CommonState{},
		Aircraft: map[av.ADSBCallsign]*Aircraft{"PRV1": prevAc, "DEP1": depAc},
		DepartureState: map[string]map[av.RunwayID]*RunwayLaunchState{
			"XTST": {"9": rwy9, "36": rwy36, "8": rwy8},
		},
	}
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.SimTime = now

	// PRV1 just launched on runway 9; it lifts off past the intersection
	// with runway 36, so it crosses it on the ground. MinSeparation is
	// larger than any possible wake turbulence wait so that it determines
	// the full interval.
	prev := DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: now, MinSeparation: 5 * time.Minute, AirborneDistance: 1.5}
	rwy9.LastDeparture = &prev
	prevAc.Nav.FlightState.Position = math.NM2LL([2]float32{0.3, 0}, testNmPerLongitude)

	dep := DepartureAircraft{ADSBCallsign: "DEP1", MinSeparation: time.Minute, AirborneDistance: 0.5}

	s.State.SimTime = now.Add(10 * time.Second)
	if s.canLaunch(rwy36, dep, false, "XTST", "36") {
		t.Error("canLaunch: leader hasn't passed the intersection yet")
	}

	// Once the leader is past the intersection, the departure may go even
	// though the full interval hasn't elapsed.
	prevAc.Nav.FlightState.Position = math.NM2LL([2]float32{1.2, 0}, testNmPerLongitude)
	if !s.canLaunch(rwy36, dep, false, "XTST", "36") {
		t.Error("canLaunch: leader passed the intersection on the ground")
	}

	// If both aircraft are airborne before the intersection, the full
	// interval applies even after the leader has passed it.
	prev.AirborneDistance = 0.5
	if s.canLaunch(rwy36, dep, false, "XTST", "36") {
		t.Error("canLaunch: both airborne before the intersection; full interval required")
	}
	s.State.SimTime = now.Add(5*time.Minute + time.Second)
	if !s.canLaunch(rwy36, dep, false, "XTST", "36") {
		t.Error("canLaunch: full interval has elapsed")
	}

	// Departures on the parallel runway aren't coupled at all.
	rwy9.LastDeparture = nil
	rwy8.LastDeparture = &DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: s.State.SimTime,
		MinSeparation: 5 * time.Minute, AirborneDistance: 1.5}
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: departure on a parallel runway shouldn't couple")
	}
}

func TestSamePavementRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	s := &Sim{
		State: &CommonState{Airports: map[string]*av.Airport{"XTST": {}}},
		DepartureState: map[string]map[av.RunwayID]*RunwayLaunchState{
			"XTST": {
				"9":       &RunwayLaunchState{},
				"9.North": &RunwayLaunchState{},
				"36":      &RunwayLaunchState{},
				"8":       &RunwayLaunchState{},
			},
		},
	}
	s.State.NmPerLongitude = testNmPerLongitude

	var got []av.RunwayID
	for rwy := range s.samePavementRunways("XTST", "9") {
		got = append(got, rwy)
	}
	for _, want := range []av.RunwayID{"9", "9.North"} {
		if !slices.Contains(got, want) {
			t.Errorf("samePavementRunways = %v, missing %q", got, want)
		}
	}
	// Intersecting runways no longer share the same-pavement group.
	if slices.Contains(got, av.RunwayID("36")) {
		t.Errorf("samePavementRunways = %v, shouldn't include intersecting runway 36", got)
	}
}

// publishedDepartureSim builds a Sim whose test airport KORG has departures to
// KNOR via the NORTH exit and to the given eastern destination via EAST, both
// off runway 30L in the "jet" category.
func publishedDepartureSim(eastDestination string) *Sim {
	return &Sim{State: &CommonState{
		NmPerLongitude: testNmPerLongitude,
		Airports: map[string]*av.Airport{
			"KORG": {
				Departures: []av.Departure{
					{Exit: "NORTH", Destination: "KNOR"},
					{Exit: "EAST", Destination: eastDestination},
				},
				ExitCategories: map[av.ExitID]string{"NORTH": "jet", "EAST": "jet"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]*av.ExitRoute{
					"30L": {"NORTH": {}, "EAST": {}},
				},
			},
		},
		DepartureRunways: []DepartureRunway{
			{Airport: "KORG", Runway: "30L", Category: "jet"},
		},
	}}
}

// seedTestAirports adds airports to av.DB for the duration of the test: the
// origin at the origin, KTGT due east, KEAS nearly so, KFAR due east but far
// past KTGT, KNOR due north, and KSOU due south.
func seedTestAirports(t *testing.T) {
	codes := []string{"KORG", "KTGT", "KEAS", "KFAR", "KNOR", "KSOU"}
	original := make(map[string]av.FAAAirport)
	for _, code := range codes {
		if airport, ok := av.DB.Airports[code]; ok {
			original[code] = airport
		}
	}
	t.Cleanup(func() {
		for _, code := range codes {
			if airport, ok := original[code]; ok {
				av.DB.Airports[code] = airport
			} else {
				delete(av.DB.Airports, code)
			}
		}
	})

	nm := func(x, y float32) math.Point2LL {
		return math.NM2LL([2]float32{x, y}, testNmPerLongitude)
	}
	av.DB.Airports["KORG"] = av.FAAAirport{Id: "KORG", Location: nm(0, 0)}
	av.DB.Airports["KTGT"] = av.FAAAirport{Id: "KTGT", Location: nm(100, 0)}
	av.DB.Airports["KEAS"] = av.FAAAirport{Id: "KEAS", Location: nm(80, 10)}
	av.DB.Airports["KFAR"] = av.FAAAirport{Id: "KFAR", Location: nm(300, 0)}
	av.DB.Airports["KNOR"] = av.FAAAirport{Id: "KNOR", Location: nm(0, 80)}
	av.DB.Airports["KSOU"] = av.FAAAirport{Id: "KSOU", Location: nm(0, -80)}
}

// seedTestRoutes replaces the route database entries for KORG->KTGT for the
// duration of the test.
func seedTestRoutes(t *testing.T, routes []av.AirportPairRoute) {
	pair := av.AirportPair{From: "KORG", To: "KTGT"}
	original, hadOriginal := av.DB.AirportPairRoutes[pair]
	t.Cleanup(func() {
		if hadOriginal {
			av.DB.AirportPairRoutes[pair] = original
		} else {
			delete(av.DB.AirportPairRoutes, pair)
		}
	})
	av.DB.AirportPairRoutes[pair] = routes
}

func TestResolvePublishedDepartureExactDestination(t *testing.T) {
	s := publishedDepartureSim("KTGT")

	_, _, _, dep, filedRoute, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if dep.Destination != "KTGT" {
		t.Fatalf("destination = %q, want KTGT", dep.Destination)
	}
	if filedRoute != "" {
		t.Fatalf("filed route = %q, want the scenario departure's own route", filedRoute)
	}
}

func TestResolvePublishedDepartureByProximity(t *testing.T) {
	seedTestAirports(t)
	s := publishedDepartureSim("KEAS")

	_, _, _, dep, filedRoute, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if dep.Destination != "KEAS" {
		t.Fatalf("destination = %q, want nearby KEAS", dep.Destination)
	}
	if filedRoute != "" {
		t.Fatalf("filed route = %q, want the scenario departure's own route", filedRoute)
	}

	// A destination dead on the flight's heading but far beyond it loses to
	// one nearby: from JFK, Florida is within a few degrees of Ocean City,
	// but a KOXB flight should follow a Norfolk route, not an Orlando one.
	ap := s.State.Airports["KORG"]
	ap.Departures = append(ap.Departures, av.Departure{Exit: "FARWY", Destination: "KFAR"})
	ap.ExitCategories["FARWY"] = "jet"
	ap.DepartureRoutes["30L"]["FARWY"] = &av.ExitRoute{}

	_, _, _, dep, _, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if dep.Destination != "KEAS" {
		t.Fatalf("destination = %q, want KEAS over on-heading but distant KFAR", dep.Destination)
	}
}

// A flight whose real destination no modeled route heads anywhere near isn't
// forced through an unrelated gate; it just isn't launched.
func TestResolvePublishedDepartureDropsFarDestinations(t *testing.T) {
	seedTestAirports(t)
	s := publishedDepartureSim("KEAS")

	_, _, _, _, _, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KSOU", "B738")
	if !errors.Is(err, errNoScenarioRoute) {
		t.Fatalf("resolvePublishedDeparture to KSOU: err = %v, want errNoScenarioRoute", err)
	}
}

// When the FAA route database knows how a city pair is flown, the flight
// takes the modeled exit its real route passes through and files that route.
func TestResolvePublishedDepartureUsesRouteDatabase(t *testing.T) {
	seedTestAirports(t)
	s := publishedDepartureSim("KEAS")

	seedTestRoutes(t, []av.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H"},
	})
	_, _, _, dep, filedRoute, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	// Direction alone would pick EAST/KEAS; the filed route says NORTH.
	if dep.Exit != "NORTH" {
		t.Errorf("exit = %q, want NORTH from the route database", dep.Exit)
	}
	if filedRoute != "KORG NORTH J111 KTGT" {
		t.Errorf("filed route = %q, want the database route", filedRoute)
	}

	// A CDR names its departure fix explicitly even when the route string
	// doesn't include the exit.
	seedTestRoutes(t, []av.AirportPairRoute{
		{Route: "KORG ZZZZZ J111 KTGT", DepartureFix: "NORTH", Type: "CDR"},
	})
	_, _, _, dep, filedRoute, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if dep.Exit != "NORTH" || filedRoute != "KORG ZZZZZ J111 KTGT" {
		t.Errorf("got exit %q route %q, want NORTH via the CDR departure fix",
			dep.Exit, filedRoute)
	}

	// If every real route leaves through an exit the scenario doesn't model,
	// the scenario doesn't work this flight.
	seedTestRoutes(t, []av.AirportPairRoute{
		{Route: "KORG WSSST J22 KTGT", Type: "H"},
	})
	_, _, _, _, _, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if !errors.Is(err, errNoScenarioRoute) {
		t.Fatalf("unmodeled exits: err = %v, want errNoScenarioRoute", err)
	}
}

// A piston can't fly a route that needs RNAV; with no eligible database route
// it falls back to the directional match.
func TestResolvePublishedDepartureRNAVGating(t *testing.T) {
	seedTestAirports(t)
	s := publishedDepartureSim("KEAS")

	seedTestRoutes(t, []av.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H", RNAVRequired: true},
	})

	_, _, _, dep, filedRoute, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture for a jet: %v", err)
	}
	if dep.Exit != "NORTH" || filedRoute == "" {
		t.Errorf("jet got exit %q route %q, want the RNAV database route via NORTH",
			dep.Exit, filedRoute)
	}

	_, _, _, dep, filedRoute, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "C172")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture for a piston: %v", err)
	}
	if dep.Destination != "KEAS" || filedRoute != "" {
		t.Errorf("piston got %q route %q, want the directional fallback to KEAS",
			dep.Destination, filedRoute)
	}
}

// A flight must fly the route to where it really went, whatever the category
// rates say. KJFK is the case that caught this: its only KATL departure exits
// at RBV in the "Southwest" category, which carries the smallest rate of the
// four on 22R, so picking a category by rate sent most Atlanta flights out over
// the water instead.
func TestResolvePublishedDepartureIgnoresRates(t *testing.T) {
	s := &Sim{State: &CommonState{
		NmPerLongitude: testNmPerLongitude,
		Airports: map[string]*av.Airport{
			"KJFK": {
				Departures: []av.Departure{
					{Exit: "WAVEY", Destination: "KMCO"},
					{Exit: "RBV", Destination: "KATL"},
				},
				ExitCategories: map[av.ExitID]string{"WAVEY": "Water", "RBV": "Southwest"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]*av.ExitRoute{
					"22R": {"WAVEY": {}, "RBV": {}},
				},
			},
		},
		DepartureRunways: []DepartureRunway{
			{Airport: "KJFK", Runway: "22R", Category: "Water", DefaultRate: 8},
			{Airport: "KJFK", Runway: "22R", Category: "Southwest", DefaultRate: 5},
		},
	}}

	// Water is listed first and carries the larger rate; neither should matter.
	_, _, _, dep, _, err := s.resolvePublishedDeparture("KJFK", "22R",
		[]string{"Water", "Southwest"}, "KATL", "B738")
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if dep.Exit != "RBV" || dep.Destination != "KATL" {
		t.Errorf("Atlanta departure got exit %q to %q, expected RBV to KATL",
			dep.Exit, dep.Destination)
	}
}
