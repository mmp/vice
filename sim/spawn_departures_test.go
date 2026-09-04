// sim/spawn_departures_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
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

	// Nor are they coupled when both fly straight out, with launch paths
	// recorded.
	rwy8.LastDeparture.LaunchPath = makeTestLaunchPath(0, 5, 0.1, 0, 120)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 120)
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: straight-out departure on a parallel runway shouldn't couple")
	}
}

// makeTestLaunchPath returns a fabricated departure path of n one-second
// samples starting at (x, y) in nm coordinates, moving by (dx, dy) each
// second.
func makeTestLaunchPath(x, y, dx, dy float32, n int) []math.Point2LL {
	path := make([]math.Point2LL, n)
	for i := range path {
		path[i] = math.NM2LL([2]float32{x + float32(i)*dx, y + float32(i)*dy}, testNmPerLongitude)
	}
	return path
}

func TestHoldForCrossingDeparture(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	prevAc := &Aircraft{ADSBCallsign: "PRV1", FlightPlan: av.FlightPlan{AircraftType: "B738", Rules: av.FlightRulesIFR}}
	depAc := &Aircraft{ADSBCallsign: "DEP1", FlightPlan: av.FlightPlan{AircraftType: "B738", Rules: av.FlightRulesIFR}}

	rwy8, rwy9 := &RunwayLaunchState{}, &RunwayLaunchState{}

	s := &Sim{
		lg:       log.New(true, "error", t.TempDir()),
		State:    &CommonState{},
		Aircraft: map[av.ADSBCallsign]*Aircraft{"PRV1": prevAc, "DEP1": depAc},
		DepartureState: map[string]map[av.RunwayID]*RunwayLaunchState{
			"XTST": {"8": rwy8, "9": rwy9},
		},
	}
	s.State.NmPerLongitude = testNmPerLongitude

	// PRV1 departed the parallel runway 8 and turns so that its path
	// crosses DEP1's straight-out path from runway 9 at (5, 0), 50 seconds
	// into each aircraft's departure. The runways don't physically
	// intersect, so only the crossing-path check applies.
	prev := DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: now, MinSeparation: time.Minute,
		LaunchPath: makeTestLaunchPath(0, 5, 0.1, -0.1, 120)}
	rwy8.LastDeparture = &prev
	dep := DepartureAircraft{ADSBCallsign: "DEP1", MinSeparation: time.Minute,
		LaunchPath: makeTestLaunchPath(0, 0, 0.1, 0, 120)}

	// They would reach the crossing point 10 seconds apart.
	s.State.SimTime = now.Add(10 * time.Second)
	if s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: crossing departure from the parallel runway is too close in time")
	}

	// 40 seconds apart is more than crossingSeparation.
	s.State.SimTime = now.Add(40 * time.Second)
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: crossing departure from the parallel runway is well ahead")
	}

	// The window is symmetric: if the new departure will be through the
	// crossing point well before the earlier one arrives, it may go.
	s.State.SimTime = now.Add(10 * time.Second)
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.05, -0.05, 120) // crosses at 100 seconds
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.25, 0, 120)      // crosses at 20 seconds
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: departure crosses well ahead of the earlier one's arrival")
	}

	// Straight-out paths from parallel runways don't cross.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.1, 0, 120)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 120)
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: straight-out parallel departures shouldn't couple")
	}

	// A deleted leader is long gone.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.1, -0.1, 120)
	delete(s.Aircraft, "PRV1")
	if s.holdForCrossingDeparture(prev, dep) {
		t.Error("holdForCrossingDeparture: deleted leader shouldn't hold the departure")
	}
	s.Aircraft["PRV1"] = prevAc

	// If the other runway's last departure is also our own runway's (via
	// "departure_runways_as_one"), the crossing check doesn't apply; the
	// own-runway launch interval already covers it.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.05, -0.05, 120) // crosses at 100 seconds
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.15, 0, 120)      // crosses at ~33 seconds
	s.State.SimTime = now.Add(80 * time.Second)                  // past MinSeparation; within crossingSeparation at the crossing
	rwy9.LastDeparture = &prev
	if !s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: shared last departure shouldn't be held for crossing paths")
	}
	rwy9.LastDeparture = nil
	if s.canLaunch(rwy9, dep, false, "XTST", "9") {
		t.Error("canLaunch: the same geometry should hold when the last departure isn't shared")
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

// publishedDepartureSim builds a Sim whose test airport KORG has the NORTH and
// EAST exits off runway 30L in the "jet" category.
func publishedDepartureSim() *Sim {
	return &Sim{State: &CommonState{
		NmPerLongitude: testNmPerLongitude,
		Airports: map[string]*av.Airport{
			"KORG": {
				ExitCategories: map[av.ExitID]string{"NORTH": "jet", "EAST": "jet"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
					"30L": {"NORTH": {{}}, "EAST": {{}}},
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
// past KTGT, KNOR due north, KSOU due south, and KSOS a near neighbor of KSOU.
func seedTestAirports(t *testing.T) {
	codes := []string{"KORG", "KTGT", "KEAS", "KFAR", "KNOR", "KSOU", "KSOS"}
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
	av.DB.Airports["KSOS"] = av.FAAAirport{Id: "KSOS", Location: nm(10, -75)}
}

// seedTestRoutes replaces the route database entries from KORG to the given
// airport for the duration of the test.
func seedTestRoutes(t *testing.T, to string, routes []av.AirportPairRoute) {
	pair := av.AirportPair{From: "KORG", To: to}
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

// seedTestScrapedRoutes replaces the scraped route database entries for the
// city pair for the duration of the test.
func seedTestScrapedRoutes(t *testing.T, from, to string, routes []av.ScrapedRoute) {
	pair := av.AirportPair{From: from, To: to}
	original, hadOriginal := av.DB.ScrapedRoutes[pair]
	t.Cleanup(func() {
		if hadOriginal {
			av.DB.ScrapedRoutes[pair] = original
		} else {
			delete(av.DB.ScrapedRoutes, pair)
		}
	})
	av.DB.ScrapedRoutes[pair] = routes
}

// A route the scenario gives for the city pair beats the route database and
// the geometry: it says in so many words how the pair is flown.
func TestResolvePublishedDepartureScenarioRoute(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()
	s.State.Airports["KORG"].TrafficRoutes = av.TrafficRoutes{
		Departures: map[string]av.TrafficRouteSet{
			// Direction alone would pick EAST; the scenario says NORTH.
			"KTGT": {av.TrafficRoute{Route: "NORTH J111 KTGT"}},
		},
	}

	placement, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Exit != "NORTH" {
		t.Errorf("exit = %q, want NORTH from the scenario route", placement.dep.Exit)
	}
	if placement.dep.Route != "NORTH J111 KTGT" {
		t.Errorf("route = %q, want the scenario route", placement.dep.Route)
	}
	if placement.how != "scenario route via NORTH" {
		t.Errorf("how = %q, want the scenario route to decide", placement.how)
	}
}

// When the FAA route database knows how a city pair is flown, the flight
// takes the modeled exit its real route passes through and files that route.
func TestResolvePublishedDepartureUsesRouteDatabase(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H"},
	})
	placement, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	// Direction alone would pick EAST/KEAS; the filed route says NORTH.
	if placement.dep.Exit != "NORTH" {
		t.Errorf("exit = %q, want NORTH from the route database", placement.dep.Exit)
	}
	if placement.dep.Route != "NORTH J111 KTGT" {
		t.Errorf("route = %q, want the database route from the exit fix on", placement.dep.Route)
	}

	// A CDR names its departure fix explicitly even when the route string
	// doesn't include the exit.
	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{
		{Route: "KORG ZZZZZ J111 KTGT", DepartureFix: "NORTH", Type: "CDR"},
	})
	placement, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Exit != "NORTH" || placement.dep.Route != "NORTH ZZZZZ J111 KTGT" {
		t.Errorf("got exit %q route %q, want NORTH leading the CDR's route",
			placement.dep.Exit, placement.dep.Route)
	}

	// If every real route leaves through an exit the scenario doesn't model,
	// fall back to the exit lying closest to the flight's direction rather
	// than dropping it: a scenario that works one corner of an airport has no
	// reason to model the gate a filed route happens to use.
	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{
		{Route: "KORG WSSST J22 KTGT", Type: "H"},
	})
	placement, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("unmodeled exits: %v", err)
	}
	if placement.dep.Exit != "EAST" {
		t.Errorf("exit = %q, want the directional fallback EAST", placement.dep.Exit)
	}
	if placement.dep.Route != "EAST" {
		t.Errorf("route = %q, want the exit fix alone, with no database route", placement.dep.Route)
	}
}

// A piston can't fly a route that needs RNAV; with no eligible database route
// it falls back to the directional match.
func TestResolvePublishedDepartureRNAVGating(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H", RNAVRequired: true},
	})

	placement, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture for a jet: %v", err)
	}
	if placement.dep.Exit != "NORTH" || placement.dep.Route != "NORTH J111 KTGT" {
		t.Errorf("jet got exit %q route %q, want the RNAV database route via NORTH",
			placement.dep.Exit, placement.dep.Route)
	}

	placement, err = s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "C172", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture for a piston: %v", err)
	}
	if placement.dep.Exit != "EAST" || placement.dep.Route != "EAST" {
		t.Errorf("piston got exit %q route %q, want the directional fallback out EAST",
			placement.dep.Exit, placement.dep.Route)
	}
}

// A flight must fly the route to where it really went, whatever the category
// rates say. KJFK is the case that caught this: Atlanta departures exit at RBV
// in the "Southwest" category, which carries the smallest rate of the four on
// 22R, so picking a category by rate sent most Atlanta flights out over the
// water instead.
func TestResolvePublishedDepartureIgnoresRates(t *testing.T) {
	av.InitDB()

	s := &Sim{State: &CommonState{
		NmPerLongitude: testNmPerLongitude,
		Airports: map[string]*av.Airport{
			"KJFK": {
				ExitCategories: map[av.ExitID]string{"WAVEY": "Water", "RBV": "Southwest"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
					"22R": {"WAVEY": {{}}, "RBV": {{}}},
				},
			},
		},
		DepartureRunways: []DepartureRunway{
			{Airport: "KJFK", Runway: "22R", Category: "Water", DefaultRate: 8},
			{Airport: "KJFK", Runway: "22R", Category: "Southwest", DefaultRate: 5},
		},
	}}

	// Water is listed first and carries the larger rate; neither should matter:
	// the real KJFK->KATL routes leave over RBV.
	placement, err := s.resolvePublishedDeparture("KJFK", "22R",
		[]string{"Water", "Southwest"}, "KATL", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Exit != "RBV" {
		t.Errorf("Atlanta departure got exit %q, expected RBV", placement.dep.Exit)
	}
}

// seedTestExits adds the NORTH, EAST, and EASTN fixes to av.DB for the
// duration of the test: north and east of KORG, and one between the two.
func seedTestExits(t *testing.T) {
	fixes := []string{"NORTH", "EAST", "EASTN"}
	original := make(map[string]av.Fix)
	for _, fix := range fixes {
		if f, ok := av.DB.Fixes[fix]; ok {
			original[fix] = f
		}
	}
	t.Cleanup(func() {
		for _, fix := range fixes {
			if f, ok := original[fix]; ok {
				av.DB.Fixes[fix] = f
			} else {
				delete(av.DB.Fixes, fix)
			}
		}
	})

	nm := func(x, y float32) math.Point2LL {
		return math.NM2LL([2]float32{x, y}, testNmPerLongitude)
	}
	av.DB.Fixes["NORTH"] = av.Fix{Id: "NORTH", Location: nm(0, 20)}
	av.DB.Fixes["EAST"] = av.Fix{Id: "EAST", Location: nm(20, 0)}
	av.DB.Fixes["EASTN"] = av.Fix{Id: "EASTN", Location: nm(20, 10)}
}

func TestCompatibleDeparturesSynthesizesPerExit(t *testing.T) {
	s := publishedDepartureSim()

	candidates := s.compatibleDepartures("KORG", "30L", []string{"jet"}, "B738")
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want one per exit off the runway", len(candidates))
	}
	exits := []av.ExitID{candidates[0].dep.Exit, candidates[1].dep.Exit}
	for _, want := range []av.ExitID{"NORTH", "EAST"} {
		if !slices.Contains(exits, want) {
			t.Errorf("candidate exits %v, missing %q", exits, want)
		}
	}

	// The synthesized departures are distinct, not repeated views of one entry.
	if candidates[0].dep == candidates[1].dep {
		t.Error("both candidates point at the same departure")
	}
}

// An exit whose routes are all for other aircraft is no way for this one to
// leave, so it isn't offered as a candidate.
func TestCompatibleDeparturesMindsAircraftClasses(t *testing.T) {
	av.InitDB()

	s := publishedDepartureSim()
	s.State.Airports["KORG"].DepartureRoutes["30L"] = map[av.ExitID]av.ExitRoutes{
		"NORTH": {{Aircraft: av.AircraftClassProp | av.AircraftClassTurboprop}},
		"EAST":  {{Aircraft: av.AircraftClassProp}, {}},
	}

	for _, tc := range []struct {
		aircraftType string
		exits        []av.ExitID
	}{
		{"C172", []av.ExitID{"NORTH", "EAST"}},
		{"B738", []av.ExitID{"EAST"}}, // only the catch-all route takes jets
	} {
		candidates := s.compatibleDepartures("KORG", "30L", []string{"jet"}, tc.aircraftType)
		exits := util.MapSlice(candidates, func(c candidateDeparture) av.ExitID { return c.dep.Exit })
		slices.Sort(exits)
		want := slices.Sorted(slices.Values(tc.exits))
		if !slices.Equal(exits, want) {
			t.Errorf("%s: candidate exits %v, want %v", tc.aircraftType, exits, want)
		}
	}
}

// With no route to go on, a published departure leaves by the exit lying
// closest to the direction it is really going.
func TestResolvePublishedDepartureByExitDirection(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	for _, tc := range []struct {
		destination string
		exit        av.ExitID
	}{{"KTGT", "EAST"}, {"KNOR", "NORTH"}} {
		placement, err := s.resolvePublishedDeparture("KORG", "30L",
			[]string{"jet"}, tc.destination, "B738", nil)
		if err != nil {
			t.Fatalf("resolvePublishedDeparture to %s: %v", tc.destination, err)
		}
		if placement.dep.Exit != tc.exit {
			t.Errorf("departure to %s got exit %q, want %q", tc.destination, placement.dep.Exit, tc.exit)
		}
		if placement.dep.Route != string(tc.exit) {
			t.Errorf("departure to %s got route %q, want the exit fix alone",
				tc.destination, placement.dep.Route)
		}
	}

	// Nothing heads south, so a southbound flight isn't launched at all.
	_, err := s.resolvePublishedDeparture("KORG", "30L", []string{"jet"}, "KSOU", "B738", nil)
	if !errors.Is(err, errNoScenarioRoute) {
		t.Errorf("resolvePublishedDeparture to KSOU: err = %v, want errNoScenarioRoute", err)
	}
}

// A route naming more than one modeled exit leaves through the first of them;
// the others are only fixes further along the way. "JFK DIXIE T438 ARD PNE" is
// the real case: it goes out over DIXIE, whatever order the scenario happens to
// list its exits in.
func TestDepartureExitTakesTheFirstAlongTheRoute(t *testing.T) {
	departures := []av.Departure{{Exit: "ARD"}, {Exit: "DIXIE"}}
	candidates := []candidateDeparture{{dep: &departures[0]}, {dep: &departures[1]}}

	c, ok := departureExit("JFK DIXIE T438 ARD PNE", "KJFK", "KPNE", "", candidates)
	if !ok {
		t.Fatal("departureExit found no exit on the route")
	}
	if c.dep.Exit != "DIXIE" {
		t.Errorf("left through %q, expected DIXIE", c.dep.Exit)
	}
}

// A route names the SID that reaches an exit as often as the exit fix itself.
// JFK to Las Vegas is the real case: the scenario models the DEEZZ exit and the
// route names DEEZZ6, the SID that gets there. A stale revision in the filed
// route--DEEZZ5 against the scenario's DEEZZ6--matches all the same.
func TestDepartureExitMatchesTheSID(t *testing.T) {
	departures := []av.Departure{{Exit: "DEEZZ"}}
	exitRoutes := map[av.ExitID]*av.ExitRoute{"DEEZZ": {SID: "DEEZZ6"}}
	candidates := []candidateDeparture{{exitRoutes: exitRoutes, dep: &departures[0]}}

	for _, route := range []string{
		"KJFK DEEZZ6 CANDR J60 DJB CPONE JOT J60 HVE GGAPP CHOWW4 KLAS",
		"KJFK DEEZZ5 CANDR J60 DJB CPONE JOT J60 HVE GGAPP CHOWW4 KLAS",
	} {
		c, ok := departureExit(route, "KJFK", "KLAS", "CANDR", candidates)
		if !ok {
			t.Fatalf("%s: departureExit didn't match the SID", route)
		}
		if c.dep.Exit != "DEEZZ" {
			t.Errorf("%s: left through %q, expected DEEZZ", route, c.dep.Exit)
		}
	}
}

// The identifiers at a route's ends are airports, not fixes to leave through.
func TestDepartureExitIgnoresTheAirportIdentifiers(t *testing.T) {
	departures := []av.Departure{{Exit: "BOS"}, {Exit: "JFK"}}
	candidates := []candidateDeparture{{dep: &departures[0]}, {dep: &departures[1]}}

	if _, ok := departureExit("JFK MERIT ROBUC3 BOS", "KJFK", "KBOS", "", candidates); ok {
		t.Error("matched an exit named after one of the route's airports")
	}
}

// A destination the route database doesn't cover leaves through the exit a
// route to its nearest neighbor uses: Vero Beach has no route from JFK, but
// Orlando 66nm away goes out over WAVEY, which is the Florida gate. The flight
// flies the neighbor's route only as far as it is its own: the trailing
// airport and STAR belong to the neighbor.
func TestResolvePublishedDepartureSubstitutesANearbyDestination(t *testing.T) {
	av.InitDB()

	s := &Sim{State: &CommonState{
		NmPerLongitude: 45,
		Airports: map[string]*av.Airport{
			"KJFK": {
				ExitCategories: map[av.ExitID]string{"WAVEY": "Water", "COATE": "North"},
				DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
					"22R": {"WAVEY": {{SID: "JFK5"}}, "COATE": {{SID: "JFK5"}}},
				},
			},
		},
		DepartureRunways: []DepartureRunway{
			{Airport: "KJFK", Runway: "22R", Category: "Water"},
			{Airport: "KJFK", Runway: "22R", Category: "North"},
		},
	}}

	placement, err := s.resolvePublishedDeparture("KJFK", "22R", []string{"Water", "North"},
		"KVRB", "B738", makeRoutedPairs().destinationsByOrigin)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture to KVRB: %v", err)
	}
	if placement.dep.Exit != "WAVEY" {
		t.Errorf("left through %q, expected WAVEY", placement.dep.Exit)
	}
	fields := strings.Fields(placement.dep.Route)
	if len(fields) < 2 || fields[0] != "WAVEY" {
		t.Errorf("route = %q, want the borrowed route from WAVEY on", placement.dep.Route)
	}
	if last := fields[len(fields)-1]; last[0] == 'K' && len(last) == 4 ||
		last[len(last)-1] >= '0' && last[len(last)-1] <= '9' {
		t.Errorf("route = %q, want the neighbor's airport and STAR stripped from its tail",
			placement.dep.Route)
	}
}

// A neighbor's route is only worth borrowing if it leaves the way the flight
// is going. Birmingham stands in for Atlanta out of Minneapolis, but one of its
// routes sets off up the northeast gate, which is no way to reach either.
func TestResolvePublishedDepartureRefusesABorrowedWrongWayGate(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	// KSOS is a near neighbor of KSOU, but the only way it is left for goes
	// the other way entirely.
	seedTestRoutes(t, "KSOS", []av.AirportPairRoute{{Route: "KORG NORTH J1 KSOS", Type: "H"}})
	s := publishedDepartureSim()

	_, err := s.resolvePublishedDeparture("KORG", "30L", []string{"jet"}, "KSOU", "B738",
		makeRoutedPairs().destinationsByOrigin)
	if !errors.Is(err, errNoScenarioRoute) {
		t.Errorf("resolvePublishedDeparture to KSOU: err = %v, want errNoScenarioRoute", err)
	}
}

// The route database wins over direction when it knows the city pair, and the
// route's waypoints are located so the aircraft actually flies them.
func TestResolvePublishedDepartureLocatesRouteWaypoints(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	seedTestRoutes(t, "KTGT", []av.AirportPairRoute{{Route: "KORG NORTH J1 KTGT", Type: "H"}})
	s := publishedDepartureSim()

	placement, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Exit != "NORTH" {
		t.Errorf("exit = %q, want the NORTH exit the real route uses", placement.dep.Exit)
	}
	if placement.dep.Route != "NORTH J1 KTGT" {
		t.Errorf("route = %q, want the real route from the exit fix on", placement.dep.Route)
	}
	// Without waypoints for it, the route is only something to display: the
	// aircraft would fly the scenario's vector off the runway and then head
	// straight for its destination, never crossing its exit.
	if len(placement.dep.RouteWaypoints) == 0 || placement.dep.RouteWaypoints[0].Fix != "NORTH" {
		t.Errorf("route waypoints = %v, want them to start at the exit fix",
			placement.dep.RouteWaypoints)
	}
	for _, wp := range placement.dep.RouteWaypoints {
		if wp.Location.IsZero() {
			t.Errorf("route waypoint %q has no location", wp.Fix)
		}
	}
}

func TestDepartureRoute(t *testing.T) {
	vectors := av.WaypointArray{{Fix: "KATL-26L"}}
	for _, tc := range []struct {
		name      string
		route     string
		airport   string
		exit      av.ExitID
		exitRoute av.ExitRoute
		want      string
	}{
		{
			name:      "route names the exit",
			route:     "EWR BIGGY Q75 TEUFL BAAMF DADES2 TPA",
			airport:   "KEWR",
			exit:      "BIGGY",
			exitRoute: av.ExitRoute{SID: "EWR5", Waypoints: vectors},
			want:      "BIGGY Q75 TEUFL BAAMF DADES2 TPA",
		},
		{
			// The fixes between the airport and the exit are flown, not
			// trimmed away.
			name:      "route reaches the exit through earlier fixes",
			route:     "EWR ELVAE NECCK WHITE Q409 CRPLR",
			airport:   "KEWR",
			exit:      "WHITE",
			exitRoute: av.ExitRoute{SID: "PORTT4", Waypoints: vectors},
			want:      "ELVAE NECCK WHITE Q409 CRPLR",
		},
		{
			// The exit route's waypoints fly the SID's fixes to CUTTN; the
			// route continues from the transition fix after the SID token.
			name:    "SID flown by the exit route",
			route:   "KATL CUTTN2 HANKO MEM RZC",
			airport: "KATL",
			exit:    "HANKO",
			exitRoute: av.ExitRoute{SID: "CUTTN2",
				Waypoints: av.WaypointArray{{Fix: "KATL-26L"}, {Fix: "BDODD"}, {Fix: "CUTTN"}}},
			want: "HANKO MEM RZC",
		},
		{
			// The filed SID isn't the one the exit route flies: BOS files the
			// RNAV SSOXS7 where the scenario put the flight on vectors off the
			// runway. It drops out just the same and LOGAN4 goes on the flight
			// plan; two SIDs must not end up on the route.
			name:    "filed SID isn't the one the exit route flies",
			route:   "BOS SSOXS7 SSOXS Q167 ZIZZI DEALE3 DCA",
			airport: "KBOS",
			exit:    "SSOXS.P.14.22/15",
			exitRoute: av.ExitRoute{SID: "LOGAN4",
				Waypoints: av.WaypointArray{{Fix: "KBOS-22L"}}},
			want: "SSOXS Q167 ZIZZI DEALE3 DCA",
		},
		{
			name:      "stale SID revision in the filed route",
			route:     "KATL CUTTN1 HANKO MEM RZC",
			airport:   "KATL",
			exit:      "HANKO",
			exitRoute: av.ExitRoute{SID: "CUTTN2", Waypoints: vectors},
			want:      "HANKO MEM RZC",
		},
		{
			// The exit route is plain vectors and the route resumes past the
			// exit, so the exit fix has to lead the route itself: "direct on
			// course" must still go out over the gate.
			name:      "route names the SID that reaches the exit",
			route:     "KJFK DEEZZ6 CANDR J60 DJB CHOWW4 KLAS",
			airport:   "KJFK",
			exit:      "DEEZZ",
			exitRoute: av.ExitRoute{SID: "DEEZZ6", Waypoints: vectors},
			want:      "DEEZZ CANDR J60 DJB CHOWW4 KLAS",
		},
		{
			// No injection when the exit route's own waypoints reach the exit.
			name:    "exit flown by the exit route",
			route:   "KJFK DEEZZ6 CANDR J60",
			airport: "KJFK",
			exit:    "DEEZZ",
			exitRoute: av.ExitRoute{SID: "DEEZZ6",
				Waypoints: av.WaypointArray{{Fix: "SKORR"}, {Fix: "DEEZZ"}}},
			want: "CANDR J60",
		},
		{
			name:      "coded departure route names its exit separately",
			route:     "KORG ZZZZZ J111 KTGT",
			airport:   "KORG",
			exit:      "NORTH",
			exitRoute: av.ExitRoute{},
			want:      "NORTH ZZZZZ J111 KTGT",
		},
		{
			name:      "exit suffixed for a scenario variant",
			route:     "EWR BIGGY Q75 TPA",
			airport:   "KEWR",
			exit:      "BIGGY.P",
			exitRoute: av.ExitRoute{SID: "EWR5"},
			want:      "BIGGY Q75 TPA",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := departureRoute(tc.route, tc.airport, tc.exit, &tc.exitRoute); got != tc.want {
				t.Errorf("departureRoute = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDropFlownPrefix(t *testing.T) {
	wps := func(fixes ...string) av.WaypointArray {
		var wa av.WaypointArray
		for _, f := range fixes {
			wa = append(wa, av.Waypoint{Fix: f})
		}
		return wa
	}

	for _, tc := range []struct {
		name  string
		route []string
		exit  []string
		want  []string
	}{
		{
			// A vector-only exit route shares nothing; the full route stands,
			// early SID fixes included.
			name:  "vectors only",
			route: []string{"ELVAE", "NECCK", "WHITE", "CRPLR"},
			exit:  []string{"KEWR-4L", "KEWR-4L-mid"},
			want:  []string{"ELVAE", "NECCK", "WHITE", "CRPLR"},
		},
		{
			// An exit route ending at the exit fix must not send the aircraft
			// back to a fix behind it.
			name:  "exit route ends at the exit",
			route: []string{"ELVAE", "NECCK", "WHITE", "CRPLR"},
			exit:  []string{"KEWR-4L", "NECCK", "WHITE"},
			want:  []string{"CRPLR"},
		},
		{
			// "ATL CUTTN HANKO ..." names the fix the exit route already ends
			// at; the route resumes past it.
			name:  "route repeats the exit route's last fix",
			route: []string{"CUTTN", "HANKO", "MEM"},
			exit:  []string{"KATL-26L", "BDODD", "CUTTN"},
			want:  []string{"HANKO", "MEM"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dropFlownPrefix(wps(tc.route...), wps(tc.exit...))
			var fixes []string
			for _, wp := range got {
				fixes = append(fixes, wp.Fix)
			}
			if !slices.Equal(fixes, tc.want) {
				t.Errorf("dropFlownPrefix = %v, want %v", fixes, tc.want)
			}
		})
	}
}

// Route waypoints stop where the sim lets the aircraft go: the ones past there
// are never flown and every one of them is sent to the clients each update.
func TestDepartureRouteWaypointsStopAtTheCullDistance(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	// KFAR is 300nm out, past the 200nm at which a TRACON's aircraft are culled.
	wps := s.departureRouteWaypoints("NORTH EAST KTGT KFAR")

	var got []string
	for _, wp := range wps {
		got = append(got, wp.Fix)
	}
	if want := []string{"NORTH", "EAST", "KTGT", "KFAR"}; !slices.Equal(got, want) {
		// KFAR is kept: the aircraft is still flying toward it when it goes.
		t.Errorf("route waypoints = %v, want %v", got, want)
	}

	av.DB.Airports["KOUT"] = av.FAAAirport{Id: "KOUT",
		Location: math.NM2LL([2]float32{500, 0}, testNmPerLongitude)}
	t.Cleanup(func() { delete(av.DB.Airports, "KOUT") })

	wps = s.departureRouteWaypoints("NORTH KFAR KOUT")
	got = nil
	for _, wp := range wps {
		got = append(got, wp.Fix)
	}
	if want := []string{"NORTH", "KFAR"}; !slices.Equal(got, want) {
		t.Errorf("route waypoints = %v, want %v: nothing past the first one out of range", got, want)
	}
}

// A route seen filed only at night--noise abatement, typically--wins at
// night and loses by day; observed aircraft classes steer prop traffic onto
// the routes props really file.
func TestOrderScrapedRoutes(t *testing.T) {
	av.InitDB()

	var night av.HourRanges
	for _, hour := range []int{22, 23, 0, 1, 2, 3, 4, 5} {
		night.Add(hour)
	}
	var day av.HourRanges
	for hour := 6; hour <= 21; hour++ {
		day.Add(hour)
	}

	var sparse av.HourRanges
	for _, hour := range []int{0, 5, 9} {
		sparse.Add(hour)
	}

	routes := []av.ScrapedRoute{
		{Route: "TRUKN2 DEDHD RBL LMT HAWKZ8", Count: 116, Hours: day,
			Aircraft: av.AircraftClassHeavyJet | av.AircraftClassNonheavyJet},
		{Route: "NIITE4 MOGEE RBL LMT HAWKZ8", Count: 20, Hours: night,
			Aircraft: av.AircraftClassHeavyJet | av.AircraftClassNonheavyJet},
		{Route: "PORTE3 ORRCA SHAZM", Count: 8, Aircraft: av.AircraftClassTurboprop,
			MinAltitude: 16000, MaxAltitude: 20000, Hours: sparse},
	}

	first := func(hour int, acType string) string {
		return orderScrapedRoutes(routes, acType, hour, true)[0].Route
	}
	if got := first(14, "B738"); !strings.HasPrefix(got, "TRUKN2") {
		t.Errorf("daytime jet got %q, want the TRUKN2", got)
	}
	if got := first(2, "B738"); !strings.HasPrefix(got, "NIITE4") {
		t.Errorf("nighttime jet got %q, want the night-observed NIITE4", got)
	}
	// The turboprop route was observed at only a few scattered hours, but the
	// hours compare only among routes of matching class: it still comes first
	// at an hour no one sampled it at.
	if got := first(14, "DH8D"); !strings.HasPrefix(got, "PORTE3") {
		t.Errorf("turboprop got %q, want the turboprop-observed PORTE3", got)
	}
	// A prop has no prop-observed route; it follows the turboprops, not the
	// far more-filed jets.
	if got := first(14, "C172"); !strings.HasPrefix(got, "PORTE3") {
		t.Errorf("prop got %q, want the turboprop-observed PORTE3", got)
	}

	// With no time zone known, the hours have no say and the most-filed
	// suitable route stands.
	if got := orderScrapedRoutes(routes, "B738", 0, false)[0].Route; !strings.HasPrefix(got, "TRUKN2") {
		t.Errorf("unknown hour got %q, want the most-filed TRUKN2", got)
	}

	// The scraped classes record what was seen, not what may match: a heavy
	// jet whose pair has no heavy-observed route follows the other jets, not
	// the more-filed turboprop route.
	observed := []av.ScrapedRoute{
		{Route: "JETRT J1 FIXES", Count: 10, Aircraft: av.AircraftClassNonheavyJet},
		{Route: "TPRPT V1 FIXES", Count: 20, Aircraft: av.AircraftClassTurboprop},
	}
	if got := orderScrapedRoutes(observed, "B77W", 0, false)[0].Route; got != "JETRT J1 FIXES" {
		t.Errorf("heavy jet got %q, want the jet-observed route", got)
	}
	// With a heavy-observed route present, the heavy takes it.
	observed = append(observed, av.ScrapedRoute{Route: "HEVYT J2 FIXES", Count: 5,
		Aircraft: av.AircraftClassHeavyJet})
	if got := orderScrapedRoutes(observed, "B77W", 0, false)[0].Route; got != "HEVYT J2 FIXES" {
		t.Errorf("heavy jet got %q, want the heavy-observed route", got)
	}
}

// The scraped filings say what altitudes the pair is really flown at and the
// procedures its route names say what it has to clear; both reach the flight
// instead of the lowest altitude anyone was ever seen at.
func TestResolvePublishedDepartureCruiseLimits(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	seedTestScrapedRoutes(t, "KORG", "KTGT", []av.ScrapedRoute{
		{Route: "EAST MISN TGTR4", Count: 100, MinAltitude: 6000, MaxAltitude: 37000},
	})

	target := av.DB.Airports["KTGT"]
	crossing := av.Waypoint{Fix: "MISN"}
	crossing.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(24000))
	target.STARs = map[string]av.STAR{
		"TGTR4": {Transitions: map[string]av.WaypointArray{"MISN": {crossing}}},
	}
	av.DB.Airports["KTGT"] = target

	s := publishedDepartureSim()
	placement, err := s.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	want := CruiseLimits{Floor: 24000, Low: 6000, High: 37000}
	if placement.cruise != want {
		t.Errorf("cruise = %+v, want %+v", placement.cruise, want)
	}
	// The route's floor is the only thing keeping the flight out of the
	// bottom of the band the scraper recorded.
	if placement.dep.Altitudes != nil {
		t.Errorf("altitudes = %v, want the limits to decide rather than a menu",
			placement.dep.Altitudes)
	}
}
