// sim/departure_routes_test.go
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
	"github.com/mmp/vice/util"
)

// publishedDepartureSim builds a Sim whose test airport KORG has the NORTH and
// EAST exits off runway 30L in the "jet" category.
func publishedDepartureSim() *Sim {
	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{
		"KORG": {
			ExitCategories: map[av.ExitID]string{"NORTH": "jet", "EAST": "jet"},
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"30L": {"NORTH": {{}}, "EAST": {{}}},
			},
		},
	}
	s.State.DepartureRunways = []DepartureRunway{
		{Airport: "KORG", Runway: "30L", Category: "jet"},
	}
	return s
}

// seedTestAirports adds airports to db.DB for the duration of the test: the
// origin at the origin, KTGT due east, KEAS nearly so, KFAR due east but far
// past KTGT, KNOR due north, KSOU due south, and KSOS a near neighbor of KSOU.
func seedTestAirports(t *testing.T) {
	codes := []av.ICAOAirportCode{"KORG", "KTGT", "KEAS", "KFAR", "KNOR", "KSOU", "KSOS"}
	original := make(map[av.ICAOAirportCode]db.Airport)
	for _, code := range codes {
		if airport, ok := db.DB.Airports[code]; ok {
			original[code] = airport
		}
	}
	t.Cleanup(func() {
		for _, code := range codes {
			if airport, ok := original[code]; ok {
				db.DB.Airports[code] = airport
			} else {
				delete(db.DB.Airports, code)
			}
		}
	})

	nm := func(x, y float32) math.Point2LL {
		return math.NM2LL([2]float32{x, y}, testNmPerLongitude)
	}
	db.DB.Airports["KORG"] = db.Airport{Id: "KORG", Location: nm(0, 0)}
	db.DB.Airports["KTGT"] = db.Airport{Id: "KTGT", Location: nm(100, 0)}
	db.DB.Airports["KEAS"] = db.Airport{Id: "KEAS", Location: nm(80, 10)}
	db.DB.Airports["KFAR"] = db.Airport{Id: "KFAR", Location: nm(300, 0)}
	db.DB.Airports["KNOR"] = db.Airport{Id: "KNOR", Location: nm(0, 80)}
	db.DB.Airports["KSOU"] = db.Airport{Id: "KSOU", Location: nm(0, -80)}
	db.DB.Airports["KSOS"] = db.Airport{Id: "KSOS", Location: nm(10, -75)}
}

// seedTestRoutes replaces the route database entries from KORG to the given
// airport for the duration of the test.
func seedTestRoutes(t *testing.T, to av.ICAOAirportCode, routes []db.AirportPairRoute) {
	pair := db.AirportPair{From: "KORG", To: to}
	original, hadOriginal := db.DB.AirportPairRoutes[pair]
	t.Cleanup(func() {
		if hadOriginal {
			db.DB.AirportPairRoutes[pair] = original
		} else {
			delete(db.DB.AirportPairRoutes, pair)
		}
	})
	db.DB.AirportPairRoutes[pair] = routes
}

// seedTestScrapedRoutes replaces the scraped route database entries for the
// city pair for the duration of the test.
func seedTestScrapedRoutes(t *testing.T, from, to av.ICAOAirportCode, routes []av.ScrapedRoute) {
	pair := db.AirportPair{From: from, To: to}
	original, hadOriginal := db.DB.ScrapedRoutes[pair]
	t.Cleanup(func() {
		if hadOriginal {
			db.DB.ScrapedRoutes[pair] = original
		} else {
			delete(db.DB.ScrapedRoutes, pair)
		}
	})
	db.DB.ScrapedRoutes[pair] = routes
}

// A route the scenario gives for the city pair beats the route database and
// the geometry: it says in so many words how the pair is flown.
func TestResolvePublishedDepartureScenarioRoute(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()
	s.State.Airports["KORG"].TrafficRoutes = av.TrafficRoutes{
		Departures: map[av.ICAOAirportCode]av.TrafficRouteSet{
			// Direction alone would pick EAST; the scenario says NORTH.
			"KTGT": {av.TrafficRoute{Route: "NORTH J111 KTGT"}},
		},
	}

	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
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

// A published flight takes the scratchpad of the scenario's departure to the
// same destination out the same exit, exits comparing by their fix; one for
// an exit the flight's runway launches wins over another configuration's.
func TestDepartureScratchpad(t *testing.T) {
	ap := &av.Airport{Departures: []av.Departure{
		{Exit: "EAST.J.30L", Destination: "KTGT", Scratchpad: "TGT"},
		{Exit: "NORTH.E", Destination: "KNOR", Scratchpad: "NOE"},
		{Exit: "NORTH", Destination: "KNOR", Scratchpad: "NOR"},
		{Exit: "NORTH", Destination: "KSOU", SecondaryScratchpad: "S"},
	}}
	exitRoutes := map[av.ExitID]*av.ExitRoute{"NORTH": {}, "EAST": {}}
	for _, tc := range []struct {
		exit        av.ExitID
		destination av.ICAOAirportCode
		want        string
	}{
		{"EAST", "KTGT", "TGT"},  // EAST.J.30L is the EAST exit
		{"EAST", "KEAS", ""},     // nothing to KEAS
		{"NORTH", "KTGT", ""},    // KTGT's departure leaves over EAST
		{"NORTH", "KNOR", "NOR"}, // the runway launches NORTH, not NORTH.E
		{"NORTH", "KSOU", ""},    // no scratchpad to take
	} {
		if sp := departureScratchpad(ap, exitRoutes, tc.exit, tc.destination); sp != tc.want {
			t.Errorf("%s to %s: scratchpad %q, want %q", tc.exit, tc.destination, sp, tc.want)
		}
	}
}

// The scenario's departure to the same destination out the same exit lends a
// published flight its scratchpad, but nothing else: the flight flies the
// route placement found and files the altitude its real route does.
func TestResolvePublishedDepartureTakesScenarioScratchpad(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()
	s.State.Airports["KORG"].Departures = []av.Departure{
		{Exit: "NORTH.J", Destination: "KTGT", Scratchpad: "NTG", Altitudes: []int{7000}, Route: "NORTH KTGT"},
	}
	s.State.Airports["KORG"].TrafficRoutes = av.TrafficRoutes{
		Departures: map[av.ICAOAirportCode]av.TrafficRouteSet{
			"KTGT": {av.TrafficRoute{Route: "NORTH J111 KTGT"}},
		},
	}

	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Scratchpad != "NTG" {
		t.Errorf("scratchpad = %q, want the scenario departure's NTG", placement.dep.Scratchpad)
	}
	if placement.dep.Route != "NORTH J111 KTGT" || len(placement.dep.Altitudes) > 0 {
		t.Errorf("route %q altitudes %v, want the traffic route and no altitude from the scenario",
			placement.dep.Route, placement.dep.Altitudes)
	}
}

// When the FAA route database knows how a city pair is flown, the flight
// takes the modeled exit its real route passes through and files that route.
func TestResolvePublishedDepartureUsesRouteDatabase(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	seedTestRoutes(t, "KTGT", []db.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H"},
	})
	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
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
	seedTestRoutes(t, "KTGT", []db.AirportPairRoute{
		{Route: "KORG ZZZZZ J111 KTGT", DepartureFix: "NORTH", Type: "CDR"},
	})
	placement, err = s.State.resolvePublishedDeparture("KORG", "30L",
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
	seedTestRoutes(t, "KTGT", []db.AirportPairRoute{
		{Route: "KORG WSSST J22 KTGT", Type: "H"},
	})
	placement, err = s.State.resolvePublishedDeparture("KORG", "30L",
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

	seedTestRoutes(t, "KTGT", []db.AirportPairRoute{
		{Route: "KORG NORTH J111 KTGT", Type: "H", RNAVRequired: true},
	})

	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
		[]string{"jet"}, "KTGT", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture for a jet: %v", err)
	}
	if placement.dep.Exit != "NORTH" || placement.dep.Route != "NORTH J111 KTGT" {
		t.Errorf("jet got exit %q route %q, want the RNAV database route via NORTH",
			placement.dep.Exit, placement.dep.Route)
	}

	placement, err = s.State.resolvePublishedDeparture("KORG", "30L",
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
	db.InitDB()

	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{
		"KJFK": {
			ExitCategories: map[av.ExitID]string{"WAVEY": "Water", "RBV": "Southwest"},
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"22R": {"WAVEY": {{}}, "RBV": {{}}},
			},
		},
	}
	s.State.DepartureRunways = []DepartureRunway{
		{Airport: "KJFK", Runway: "22R", Category: "Water", DefaultRate: 8},
		{Airport: "KJFK", Runway: "22R", Category: "Southwest", DefaultRate: 5},
	}

	// Water is listed first and carries the larger rate; neither should matter:
	// the real KJFK->KATL routes leave over RBV.
	placement, err := s.State.resolvePublishedDeparture("KJFK", "22R",
		[]string{"Water", "Southwest"}, "KATL", "B738", nil)
	if err != nil {
		t.Fatalf("resolvePublishedDeparture: %v", err)
	}
	if placement.dep.Exit != "RBV" {
		t.Errorf("Atlanta departure got exit %q, expected RBV", placement.dep.Exit)
	}
}

// seedTestExits adds the NORTH, EAST, and EASTN fixes to db.DB for the
// duration of the test: north and east of KORG, and one between the two.
func seedTestExits(t *testing.T) {
	fixes := []string{"NORTH", "EAST", "EASTN"}
	original := make(map[string]db.Fix)
	for _, fix := range fixes {
		if f, ok := db.DB.Fixes[fix]; ok {
			original[fix] = f
		}
	}
	t.Cleanup(func() {
		for _, fix := range fixes {
			if f, ok := original[fix]; ok {
				db.DB.Fixes[fix] = f
			} else {
				delete(db.DB.Fixes, fix)
			}
		}
	})

	nm := func(x, y float32) math.Point2LL {
		return math.NM2LL([2]float32{x, y}, testNmPerLongitude)
	}
	db.DB.Fixes["NORTH"] = db.Fix{Id: "NORTH", Location: nm(0, 20)}
	db.DB.Fixes["EAST"] = db.Fix{Id: "EAST", Location: nm(20, 0)}
	db.DB.Fixes["EASTN"] = db.Fix{Id: "EASTN", Location: nm(20, 10)}
}

func TestCompatibleDeparturesSynthesizesPerExit(t *testing.T) {
	s := publishedDepartureSim()

	candidates := s.State.compatibleDepartures("KORG", "30L", []string{"jet"}, "B738")
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
	db.InitDB()

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
		candidates := s.State.compatibleDepartures("KORG", "30L", []string{"jet"}, tc.aircraftType)
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
		destination av.ICAOAirportCode
		exit        av.ExitID
	}{{"KTGT", "EAST"}, {"KNOR", "NORTH"}} {
		placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
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
	_, err := s.State.resolvePublishedDeparture("KORG", "30L", []string{"jet"}, "KSOU", "B738", nil)
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

// A route often names no modeled exit at all. JFK to Las Vegas is the real
// case: the scenario models the DEEZZ exit and filings resume at CANDR,
// which lies past DEEZZ on the DEEZZ6's charted path. The exit comes from
// walking that path back from the route's first fix--never from the filed
// SID token, which may not be the SID the scenario flies for the gate--so a
// stale revision on either side matches all the same.
func TestDepartureExitJoinsTheSIDPath(t *testing.T) {
	db.InitDB()

	for _, sid := range []string{"DEEZZ6", "DEEZZ5" /* stale in the scenario */} {
		departures := []av.Departure{{Exit: "DEEZZ"}}
		exitRoutes := map[av.ExitID]*av.ExitRoute{"DEEZZ": {SID: sid}}
		candidates := []candidateDeparture{{exitRoutes: exitRoutes, dep: &departures[0]}}

		for _, route := range []string{
			"KJFK DEEZZ6 CANDR J60 DJB CPONE JOT J60 HVE GGAPP CHOWW4 KLAS",
			"KJFK DEEZZ5 CANDR J60 DJB CPONE JOT J60 HVE GGAPP CHOWW4 KLAS", // stale in the filing
		} {
			c, ok := departureExit(route, "KJFK", "KLAS", "CANDR", candidates)
			if !ok {
				t.Fatalf("sid %s: %s: departureExit found no exit", sid, route)
			}
			if c.dep.Exit != "DEEZZ" {
				t.Errorf("sid %s: %s: left through %q, expected DEEZZ", sid, route, c.dep.Exit)
			}
		}

		// A first fix on no charted path of the SID matches nothing.
		if _, ok := departureExit("KJFK DEEZZ6 SLT J70 KLAS", "KJFK", "KLAS", "", candidates); ok {
			t.Errorf("sid %s: matched an exit for a route that never joins the SID", sid)
		}
	}

	// A route that leaves the SID before reaching any modeled exit takes the
	// exit ahead of it: with CANDR the exit, a filing resuming at HEERO
	// still goes out through the CANDR gate.
	departures := []av.Departure{{Exit: "CANDR"}}
	exitRoutes := map[av.ExitID]*av.ExitRoute{"CANDR": {SID: "DEEZZ6"}}
	candidates := []candidateDeparture{{exitRoutes: exitRoutes, dep: &departures[0]}}
	c, ok := departureExit("KJFK DEEZZ6 HEERO V489 SAX KMMU", "KJFK", "KMMU", "", candidates)
	if !ok {
		t.Fatal("departureExit found no exit ahead of HEERO")
	}
	if c.dep.Exit != "CANDR" {
		t.Errorf("left through %q, expected CANDR", c.dep.Exit)
	}
}

// The identifiers at a route's ends are airports, not fixes to leave through,
// and neither is the origin's id behind the SID token--the OGG in PHOG's
// "MAUI5 OGG LNY ..." filings--unless it leads onto an airway: then it is the
// airport's VOR, the airway's entry, and the flight goes out over it.
func TestDepartureExitIgnoresTheAirportIdentifiers(t *testing.T) {
	db.InitDB()

	departures := []av.Departure{{Exit: "BOS"}, {Exit: "JFK"}}
	candidates := []candidateDeparture{{dep: &departures[0]}, {dep: &departures[1]}}

	if _, ok := departureExit("JFK MERIT ROBUC3 BOS", "KJFK", "KBOS", "", candidates); ok {
		t.Error("matched an exit named after one of the route's airports")
	}

	departures = []av.Departure{{Exit: "OGG"}, {Exit: "LNY"}}
	candidates = []candidateDeparture{{dep: &departures[0]}, {dep: &departures[1]}}

	if c, ok := departureExit("MAUI5 OGG LNY JULLE5", "PHOG", "PHNL", "", candidates); !ok {
		t.Error("MAUI5 OGG LNY JULLE5: departureExit found no exit")
	} else if c.dep.Exit != "LNY" {
		t.Errorf("MAUI5 OGG LNY JULLE5: left through %q, expected LNY", c.dep.Exit)
	}
	if c, ok := departureExit("OGG V16 NAPUA LIH", "PHOG", "PHLI", "", candidates); !ok {
		t.Error("OGG V16 NAPUA LIH: departureExit found no exit")
	} else if c.dep.Exit != "OGG" {
		t.Errorf("OGG V16 NAPUA LIH: left through %q, expected OGG", c.dep.Exit)
	}
}

// A destination the route database doesn't cover leaves through the exit a
// route to its nearest neighbor uses: Vero Beach has no route from JFK, but
// Orlando 66nm away goes out over WAVEY, which is the Florida gate. The flight
// flies the neighbor's route only as far as it is its own: the trailing
// airport and STAR belong to the neighbor.
func TestResolvePublishedDepartureSubstitutesANearbyDestination(t *testing.T) {
	db.InitDB()

	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = 45
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{
		"KJFK": {
			ExitCategories: map[av.ExitID]string{"WAVEY": "Water", "COATE": "North"},
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"22R": {"WAVEY": {{SID: "JFK5"}}, "COATE": {{SID: "JFK5"}}},
			},
		},
	}
	s.State.DepartureRunways = []DepartureRunway{
		{Airport: "KJFK", Runway: "22R", Category: "Water"},
		{Airport: "KJFK", Runway: "22R", Category: "North"},
	}

	placement, err := s.State.resolvePublishedDeparture("KJFK", "22R", []string{"Water", "North"},
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
	seedTestRoutes(t, "KSOS", []db.AirportPairRoute{{Route: "KORG NORTH J1 KSOS", Type: "H"}})
	s := publishedDepartureSim()

	_, err := s.State.resolvePublishedDeparture("KORG", "30L", []string{"jet"}, "KSOU", "B738",
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
	seedTestRoutes(t, "KTGT", []db.AirportPairRoute{{Route: "KORG NORTH J1 KTGT", Type: "H"}})
	s := publishedDepartureSim()

	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
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
	db.InitDB()

	vectors := av.WaypointArray{{Fix: "KATL-26L"}}
	for _, tc := range []struct {
		name      string
		route     string
		airport   av.ICAOAirportCode
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
		{
			// The origin's id sits behind the SID token--OGG is PHOG's
			// identifier as well as its VOR--and isn't flown: the flight
			// joins the route at LNY rather than turning back to the field.
			name:      "airport id behind the SID token",
			route:     "MAUI5 OGG LNY JULLE5",
			airport:   "PHOG",
			exit:      "LNY",
			exitRoute: av.ExitRoute{SID: "MAUI5", Waypoints: vectors},
			want:      "LNY JULLE5",
		},
		{
			// ...unless it leads onto an airway: then it is the OGG VOR, the
			// airway's entry fix, and dropping it would lose the airway.
			name:      "airport id enters an airway",
			route:     "OGG V16 NAPUA LIH",
			airport:   "PHOG",
			exit:      "OGG",
			exitRoute: av.ExitRoute{SID: "MAUI5", Waypoints: vectors},
			want:      "OGG V16 NAPUA LIH",
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
			// back to a fix behind it. The exit fix itself stays: the splice
			// merges the two copies of it.
			name:  "exit route ends at the exit",
			route: []string{"ELVAE", "NECCK", "WHITE", "CRPLR"},
			exit:  []string{"KEWR-4L", "NECCK", "WHITE"},
			want:  []string{"WHITE", "CRPLR"},
		},
		{
			// "ATL CUTTN HANKO ..." names the fix the exit route already ends
			// at, so the route keeps it and the two are merged.
			name:  "route repeats the exit route's last fix",
			route: []string{"CUTTN", "HANKO", "MEM"},
			exit:  []string{"KATL-26L", "BDODD", "CUTTN"},
			want:  []string{"CUTTN", "HANKO", "MEM"},
		},
		{
			// An exit route that carries on past the exit fix has nothing to
			// merge at its end, so the route resumes past the fix they share.
			name:  "exit route flies past the exit",
			route: []string{"WHITE", "CRPLR"},
			exit:  []string{"KEWR-4L", "NECCK", "WHITE", "PANZE"},
			want:  []string{"CRPLR"},
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
	wps := s.State.departureRouteWaypoints("NORTH EAST KTGT KFAR")

	var got []string
	for _, wp := range wps {
		got = append(got, wp.Fix)
	}
	if want := []string{"NORTH", "EAST", "KTGT", "KFAR"}; !slices.Equal(got, want) {
		// KFAR is kept: the aircraft is still flying toward it when it goes.
		t.Errorf("route waypoints = %v, want %v", got, want)
	}

	db.DB.Airports["KOUT"] = db.Airport{Id: "KOUT",
		Location: math.NM2LL([2]float32{500, 0}, testNmPerLongitude)}
	t.Cleanup(func() { delete(db.DB.Airports, "KOUT") })

	wps = s.State.departureRouteWaypoints("NORTH KFAR KOUT")
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
	db.InitDB()

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

	target := db.DB.Airports["KTGT"]
	crossing := av.Waypoint{Fix: "MISN"}
	crossing.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(24000))
	target.STARs = map[string]av.STAR{
		"TGTR4": {Transitions: map[string]av.WaypointArray{"MISN": {crossing}}},
	}
	db.DB.Airports["KTGT"] = target

	s := publishedDepartureSim()
	placement, err := s.State.resolvePublishedDeparture("KORG", "30L",
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

func TestResolveScheduledDepartureRunwaySplitsByRate(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)
	s := publishedDepartureSim()

	// A second runway flies the same gates, so every flight fits both; the
	// scenario launches five times as many departures off 30L as off 30R.
	s.State.Airports["KORG"].DepartureRoutes["30R"] = map[av.ExitID]av.ExitRoutes{"NORTH": {{}}, "EAST": {{}}}
	s.State.DepartureRunways = append(s.State.DepartureRunways,
		DepartureRunway{Airport: "KORG", Runway: "30R", Category: "jet"})
	lc := &s.State.LaunchConfig
	lc.DepartureEnabled = map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool{
		"KORG": {"30L": {"jet": true}, "30R": {"jet": true}},
	}
	lc.DepartureRates = map[av.ICAOAirportCode]map[av.RunwayID]map[string]float32{
		"KORG": {"30L": {"jet": 10}, "30R": {"jet": 2}},
	}
	s.DepartureState["KORG"] = map[av.RunwayID]*RunwayLaunchState{
		"30L": {PublishedDepartures: make(map[string]int)},
		"30R": {PublishedDepartures: make(map[string]int)},
	}

	counts := make(map[av.RunwayID]int)
	for range 12 {
		e := ScheduledDeparture{ScheduledFlight: ScheduledFlight{
			DepartureAirport: "KORG", ArrivalAirport: "KNOR", AircraftType: "B738"}}
		runway, _, choice, err := s.State.resolvePublishedDepartureRunway(&e, s.routedPairsIndex(),
			s.DepartureState["KORG"])
		if err != nil {
			t.Fatalf("resolvePublishedDepartureRunway: %v", err)
		}
		counts[runway]++
		s.DepartureState["KORG"][runway].PublishedDepartures[choice.candidate.rwy.Category]++
	}

	if counts["30L"] != 10 || counts["30R"] != 2 {
		t.Errorf("runway split = %v, want 10 off 30L and 2 off 30R", counts)
	}
}

func TestResolveScheduledDepartureRunwaySplitsByCategory(t *testing.T) {
	seedTestAirports(t)
	seedTestExits(t)

	// 30L launches only the north gate; 30R launches both, at the same rate
	// north as 30L. The south flights it has to take shouldn't cost it its
	// half of the north ones.
	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{
		"KORG": {
			ExitCategories: map[av.ExitID]string{"NORTH": "north", "EAST": "south"},
			DepartureRoutes: map[av.RunwayID]map[av.ExitID]av.ExitRoutes{
				"30L": {"NORTH": {{}}},
				"30R": {"NORTH": {{}}, "EAST": {{}}},
			},
		},
	}
	s.State.DepartureRunways = []DepartureRunway{
		{Airport: "KORG", Runway: "30L", Category: "north"},
		{Airport: "KORG", Runway: "30R", Category: "north"},
		{Airport: "KORG", Runway: "30R", Category: "south"},
	}
	lc := &s.State.LaunchConfig
	lc.DepartureEnabled = map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool{
		"KORG": {"30L": {"north": true}, "30R": {"north": true, "south": true}},
	}
	lc.DepartureRates = map[av.ICAOAirportCode]map[av.RunwayID]map[string]float32{
		"KORG": {"30L": {"north": 10}, "30R": {"north": 10, "south": 10}},
	}
	s.DepartureState["KORG"] = map[av.RunwayID]*RunwayLaunchState{
		"30L": {PublishedDepartures: make(map[string]int)},
		"30R": {PublishedDepartures: make(map[string]int)},
	}

	counts := make(map[av.RunwayID]map[string]int)
	for i := range 16 {
		destination := av.ICAOAirportCode("KNOR")
		if i%2 == 1 {
			destination = "KTGT"
		}
		e := ScheduledDeparture{ScheduledFlight: ScheduledFlight{
			DepartureAirport: "KORG", ArrivalAirport: destination, AircraftType: "B738"}}
		runway, _, choice, err := s.State.resolvePublishedDepartureRunway(&e, s.routedPairsIndex(),
			s.DepartureState["KORG"])
		if err != nil {
			t.Fatalf("resolvePublishedDepartureRunway to %s: %v", destination, err)
		}
		category := choice.candidate.rwy.Category
		if counts[runway] == nil {
			counts[runway] = make(map[string]int)
		}
		counts[runway][category]++
		s.DepartureState["KORG"][runway].PublishedDepartures[category]++
	}

	if counts["30L"]["north"] != 4 || counts["30R"]["north"] != 4 {
		t.Errorf("north split = %d off 30L, %d off 30R; want 4 each",
			counts["30L"]["north"], counts["30R"]["north"])
	}
	if counts["30R"]["south"] != 8 || counts["30L"]["south"] != 0 {
		t.Errorf("south split = %d off 30L, %d off 30R; want all 8 off 30R",
			counts["30L"]["south"], counts["30R"]["south"])
	}
}

func TestDepartureSplitRestartsOnConfigChange(t *testing.T) {
	s := NewTestSim(testLogger())
	s.DepartureState["KORG"] = map[av.RunwayID]*RunwayLaunchState{
		"30L": {PublishedDepartures: map[string]int{"jet": 120}},
		"30R": {PublishedDepartures: make(map[string]int)},
	}
	s.State.LaunchConfig.TrafficSource = TrafficSourceHistorical
	s.State.LaunchConfig.DepartureEnabled = map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool{
		"KORG": {"30L": {"jet": true}},
	}

	old := s.State.LaunchConfig
	if s.applyScheduleConfigChanges(&old, nil); s.DepartureState["KORG"]["30L"].PublishedDepartures["jet"] != 120 {
		t.Error("applyScheduleConfigChanges: an unchanged config shouldn't restart the split")
	}

	// 30R starts launching too. Without a restart every flight would go to
	// it until its count caught up with 30L's morning of departures.
	s.State.LaunchConfig.DepartureEnabled = map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool{
		"KORG": {"30L": {"jet": true}, "30R": {"jet": true}},
	}
	s.applyScheduleConfigChanges(&old, nil)
	if n := s.DepartureState["KORG"]["30L"].PublishedDepartures["jet"]; n != 0 {
		t.Errorf("30L has taken %d published departures, want the count restarted at 0", n)
	}
}
