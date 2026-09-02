// cmd/importflights/importflights_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func TestParseAirportList(t *testing.T) {
	for _, tc := range []struct {
		value    string
		expected []string
	}{
		{"['KMSP']", []string{"KMSP"}},
		{"['KACT', 'KAUS', 'KBBD']", []string{"KACT", "KAUS", "KBBD"}},
		{`["KMSP", "KATL"]`, []string{"KMSP", "KATL"}},
		{"-", nil},
		{"", nil},
		{"[]", nil},
		{"['KMSP', 'X23']", []string{"KMSP"}}, // three-character ids aren't ICAO codes
	} {
		if got := parseAirportList(tc.value); !slices.Equal(got, tc.expected) {
			t.Errorf("parseAirportList(%q) = %v, expected %v", tc.value, got, tc.expected)
		}
	}
}

func TestParseRoute(t *testing.T) {
	for _, tc := range []struct {
		value    string
		expected []string
	}{
		{"KMSP-KSTL", []string{"KMSP", "KSTL"}},
		{"KMSP-KAUS-KMSP", []string{"KMSP", "KAUS", "KMSP"}},
		{"-", nil},
		{"", nil},
		{"KMSP-", []string{"KMSP"}},
	} {
		if got := parseRoute(tc.value); !slices.Equal(got, tc.expected) {
			t.Errorf("parseRoute(%q) = %v, expected %v", tc.value, got, tc.expected)
		}
	}
}

// at and over are the two ways an end of a track comes out: the aircraft was at
// the airport, or the airport was merely the nearest of several candidates.
func at(airport string) endpoint   { return endpoint{airport: airport, atAirport: true} }
func over(airport string) endpoint { return endpoint{airport: airport} }

func TestResolveEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name                string
		origin, destination trackEnd
		route               []string
		from, to            endpoint
	}{
		{
			name: "both known", origin: noTrack("KMSP"), destination: noTrack("KSTL"),
			from: at("KMSP"), to: at("KSTL"),
		},
		{
			name: "both known, itinerary ignored", origin: noTrack("KMSP"),
			destination: noTrack("KSTL"), route: []string{"KORD", "KATL"},
			from: at("KMSP"), to: at("KSTL"),
		},
		{
			name: "origin picked from itinerary", origin: noTrack("KACT", "KAUS", "KBBD"),
			destination: noTrack("KMSP"), route: []string{"KAUS", "KMSP"},
			from: at("KAUS"), to: at("KMSP"),
		},
		{
			name: "leg picked from round trip", origin: noTrack("KMSP"),
			destination: noTrack("KBOI", "KTWF"), route: []string{"KMSP", "KBOI", "KMSP"},
			from: at("KMSP"), to: at("KBOI"),
		},
		{
			name: "no track, single leg itinerary", route: []string{"KMSP", "KSTL"},
			from: at("KMSP"), to: at("KSTL"),
		},
		{
			name: "no track, ambiguous round trip", route: []string{"KMSP", "KAUS", "KMSP"},
		},
		{
			name:   "ambiguous both ends, nothing to place them with",
			origin: noTrack("KACT", "KAUS"), destination: noTrack("KMSP", "KSTL"),
		},
		{
			// The origin is certain even though the itinerary is about some
			// other flight, so the departure survives; nothing says which of
			// the two destinations it flew to.
			name: "itinerary disagrees with the track", origin: noTrack("KMSP"),
			destination: noTrack("KSTL", "KORD"), route: []string{"KATL", "KMCO"},
			from: at("KMSP"),
		},
		{
			name: "nothing at all",
		},
		{
			// The flight this whole change is about: a general aviation
			// departure from a cluster of airports, with no itinerary to say
			// which one.
			name:   "clustered origin, on the ground",
			origin: onGround(vanNuys, "KBUR", "KVNY", "KWHP"), destination: noTrack("KMSP"),
			from: at("KVNY"), to: at("KMSP"),
		},
		{
			name:   "clustered origin, low over the field",
			origin: aloft(vanNuys, 1500, "KBUR", "KVNY", "KWHP"), destination: noTrack("KMSP"),
			from: at("KVNY"), to: at("KMSP"),
		},
		{
			// High enough that the track could have started anywhere, so Van
			// Nuys is only good enough to be the far end of someone else's
			// flight.
			name:   "clustered origin, at altitude",
			origin: aloft(vanNuys, 6000, "KBUR", "KVNY", "KWHP"), destination: noTrack("KMSP"),
			from: over("KVNY"), to: at("KMSP"),
		},
		{
			// Without the itinerary coming first this would resolve to Van
			// Nuys, fail the guard, and lose a Burbank departure we have today.
			name:   "itinerary outranks the nearest airport",
			origin: aloft(vanNuys, 6000, "KBUR", "KVNY", "KWHP"), destination: noTrack("KLAS"),
			route: []string{"KBUR", "KLAS"}, from: at("KBUR"), to: at("KLAS"),
		},
		{
			name:        "both ends ambiguous, each placed on its own",
			origin:      onGround(vanNuys, "KBUR", "KVNY", "KWHP"),
			destination: onGround(minneapolis, "KMSP", "KORD"),
			from:        at("KVNY"), to: at("KMSP"),
		},
		{
			name:   "a candidate the airport database doesn't have is passed over",
			origin: onGround(vanNuys, "KZZZ", "KVNY"), destination: noTrack("KMSP"),
			from: at("KVNY"), to: at("KMSP"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := resolveEndpoints(tc.origin, tc.destination, tc.route, testAirports)
			if from != tc.from || to != tc.to {
				t.Errorf("got %+v -> %+v, expected %+v -> %+v", from, to, tc.from, tc.to)
			}
		})
	}
}

func TestParseTrackEnd(t *testing.T) {
	// A track end the source data has nothing for blanks every one of its
	// columns together.
	if e := parseTrackEnd("-", "-", "-", "-"); e.candidates != nil || e.hasPosition ||
		e.hasHeight || e.onGround {
		t.Errorf("an empty track end came out as %+v", e)
	}

	e := parseTrackEnd("['KVNY']", "34.21", "-118.49", "ground")
	if !slices.Equal(e.candidates, []string{"KVNY"}) {
		t.Errorf("candidates = %v", e.candidates)
	}
	if !e.hasPosition || e.position != (math.Point2LL{-118.49, 34.21}) {
		t.Errorf("position = %v, %v", e.position, e.hasPosition)
	}
	if !e.onGround || e.hasHeight {
		t.Errorf("an aircraft on the ground came out as %+v", e)
	}

	if e := parseTrackEnd("['KVNY']", "34.21", "-118.49", "5675"); !e.hasHeight ||
		e.height != 5675 || e.onGround {
		t.Errorf("an airborne aircraft came out as %+v", e)
	}

	// strconv reads "nan" as a number, and a NaN latitude would poison every
	// distance it was measured against.
	if v, ok := parseFloat("nan"); ok {
		t.Errorf(`parseFloat("nan") = %v, %v; expected it to be refused`, v, ok)
	}
	if e := parseTrackEnd("['KVNY']", "nan", "nan", "nan"); e.hasPosition || e.hasHeight {
		t.Errorf("a track end of NaNs came out as %+v", e)
	}
}

// A type the aircraft database doesn't have is imported all the same and
// counted, so that the report says what to add to openscope-aircraft.json. A
// row with no type at all is skipped.
func TestUnknownAircraftTypes(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		Callsign:            "DAL88",
		AircraftType:        " b738 ",
		OriginTime:          "2026-03-30 14:04:59",
		OriginAirports:      "['KMSP']",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['KORD']",
		Route:               "-",
	}
	imp.processRow(&row)
	row.AircraftType = "E45X"
	imp.processRow(&row)
	row.AircraftType = "-"
	imp.processRow(&row)

	if got := imp.unknownTypes["E45X"]; got != 1 {
		t.Errorf("E45X counted %d times, expected 1", got)
	}
	if _, ok := imp.unknownTypes["B738"]; ok {
		t.Errorf("B738 is in the aircraft database but was counted as unknown")
	}
	if imp.missingAircraftType != 1 {
		t.Errorf("missingAircraftType = %d, expected 1", imp.missingAircraftType)
	}

	types := make(map[string]int)
	for _, r := range imp.buckets[bucket{cell: cellOf("KMSP"), departure: true}] {
		types[imp.symbols.string(r.acType)]++
	}
	if got := map[string]int{"B738": 1, "E45X": 1}; !maps.Equal(types, got) {
		t.Errorf("imported types %v, expected %v", types, got)
	}
}

// The source data resolves a track's aircraft type from the registration and
// gets it wrong for the occasional airframe, so a flight that a callsign
// otherwise flies with a jet is sometimes recorded with a piston single or a
// helicopter. Those records take the type the callsign otherwise flies.
func TestRepairSuspectTypes(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		OriginTime:          "2026-03-30 14:04:59",
		OriginAirports:      "['KMSP']",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['KORD']",
		Route:               "-",
	}
	fly := func(callsign, aircraftType string, n int) {
		row.Callsign, row.AircraftType = callsign, aircraftType
		for range n {
			imp.processRow(&row)
		}
	}
	fly("DAL88", "B738", 3)
	fly("DAL88", "C172", 1)
	fly("DAL88", "B06", 1)
	// A type the aircraft database doesn't have is as suspect as a piston or a
	// helicopter: nothing an airline flies is missing from it.
	fly("DAL88", "ERCO", 1)
	// The callsign flies the piston single, so it keeps it, and so does the
	// jet it flew the once.
	fly("DAL99", "C172", 2)
	fly("DAL99", "B738", 1)
	// A callsign whose prefix isn't an airline's is one no sim flies.
	fly("ZZZ11", "B738", 2)
	fly("ZZZ11", "C172", 1)

	imp.repairSuspectTypes()

	types := make(map[string]map[string]int)
	for _, r := range imp.buckets[bucket{cell: cellOf("KMSP"), departure: true}] {
		callsign := imp.symbols.string(r.callsign)
		if types[callsign] == nil {
			types[callsign] = make(map[string]int)
		}
		types[callsign][imp.symbols.string(r.acType)]++
	}

	for callsign, expected := range map[string]map[string]int{
		"DAL88": {"B738": 6},
		"DAL99": {"C172": 2, "B738": 1},
		"ZZZ11": {"B738": 2, "C172": 1},
	} {
		if !maps.Equal(types[callsign], expected) {
			t.Errorf("%s flew %v, expected %v", callsign, types[callsign], expected)
		}
	}

	// Twice each: every row files a departure at KMSP and an arrival at KORD.
	if expected := map[string]int64{"C172": 2, "B06": 2, "ERCO": 2}; !maps.Equal(imp.repairedTypes, expected) {
		t.Errorf("repaired %v, expected %v", imp.repairedTypes, expected)
	}
}

func TestParseTime(t *testing.T) {
	got, ok := parseTime("2026-03-30 02:04:09")
	if !ok {
		t.Fatalf("failed to parse timestamp")
	}
	if expected := time.Date(2026, time.March, 30, 2, 4, 9, 0, time.UTC); !got.Equal(expected) {
		t.Errorf("got %v, expected %v", got, expected)
	}

	if _, ok := parseTime("-"); ok {
		t.Errorf(`parsed "-" as a time`)
	}
}

// testAirports is an airport database with just enough in it to import: a
// couple of US airports, one abroad, and the made-up ones the Academy flies,
// which carry no country of their own just as custom_airports.json leaves them.
var testAirports = map[string]av.FAAAirport{
	"KMSP": {Country: "US", Elevation: 841, Location: math.Point2LL{-93.22, 44.88}},
	"KORD": {Country: "US", Elevation: 672, Location: math.Point2LL{-87.90, 41.98}},
	"KEWR": {Country: "US", Elevation: 18, Location: math.Point2LL{-74.17, 40.69}},
	"KTEB": {Country: "US", Elevation: 9, Location: math.Point2LL{-74.06, 40.85}},
	"KLAS": {Country: "US", Elevation: 2181, Location: math.Point2LL{-115.15, 36.08}},
	// Van Nuys, Burbank and Whiteman sit within a few miles of each other, so a
	// track at any of them lists all three. This is the cluster whose general
	// aviation departures used to be thrown away for want of an itinerary.
	"KVNY": {Country: "US", Elevation: 802, Location: math.Point2LL{-118.49, 34.21}},
	"KBUR": {Country: "US", Elevation: 778, Location: math.Point2LL{-118.36, 34.20}},
	"KWHP": {Country: "US", Elevation: 1003, Location: math.Point2LL{-118.41, 34.26}},
	"CYYZ": {Country: "CA", Elevation: 569, Location: math.Point2LL{-79.63, 43.68}},
	"KAAC": {Location: math.Point2LL{-95.68, 36.17}},
	"KBRT": {Location: math.Point2LL{-95.94, 36.45}},
	"KJKE": {Location: math.Point2LL{-95.62, 35.92}},
	"4Y3":  {Location: math.Point2LL{-95.35, 36.39}},
	"4V4":  {Location: math.Point2LL{-95.39, 35.98}},
}

// The Los Angeles cluster's fields, for tracks that begin or end at them.
var (
	vanNuys     = testAirports["KVNY"].Location
	minneapolis = testAirports["KMSP"].Location
)

// onGround builds a track end for an aircraft the source data saw on the ground
// at p, near the given candidate airports.
func onGround(p math.Point2LL, candidates ...string) trackEnd {
	return trackEnd{candidates: candidates, position: p, hasPosition: true, onGround: true}
}

// aloft builds a track end for an aircraft seen at p, the given number of feet
// above sea level.
func aloft(p math.Point2LL, feet float32, candidates ...string) trackEnd {
	return trackEnd{candidates: candidates, position: p, hasPosition: true,
		height: feet, hasHeight: true}
}

// noTrack builds a track end the source data gives no position for.
func noTrack(candidates ...string) trackEnd {
	return trackEnd{candidates: candidates}
}

// testPerformance is an aircraft database holding just the types the tests fly,
// each with the engine class that repairSuspectTypes works from.
var testPerformance = map[string]av.AircraftPerformance{
	"B738": performanceWithEngine("J"),
	"C172": performanceWithEngine("P"),
	"B06":  performanceWithEngine("H"),
}

func performanceWithEngine(class string) av.AircraftPerformance {
	var perf av.AircraftPerformance
	perf.Engine.AircraftType = class
	return perf
}

// cellOf is where testAirports puts an airport's flights.
func cellOf(airport string) string { return av.FlightDataCell(testAirports[airport].Location) }

// makeTestImporter builds an importer over testAirports, with no resource
// loading involved.
func makeTestImporter(t *testing.T) *importer {
	t.Helper()
	imp, err := makeImporter(testAirports, testPerformance, map[string]av.Airline{"DAL": {}})
	if err != nil {
		t.Fatalf("makeImporter: %v", err)
	}
	return imp
}

// A callsign whose prefix isn't in the airline database is counted so that the
// report can list what's missing from openscope-airlines.json.
func TestUnknownAirlineCallsigns(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		Callsign:            "ZZZ123",
		AircraftType:        "B738",
		OriginTime:          "2026-03-30 14:04:59",
		OriginAirports:      "['KMSP']",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['KORD']",
		Route:               "-",
	}
	imp.processRow(&row)
	row.Callsign = "DAL88"
	imp.processRow(&row)

	if got := imp.unknownAirlines["ZZZ"]; got != 1 {
		t.Errorf("ZZZ counted %d times, expected 1", got)
	}
	if _, ok := imp.unknownAirlines["DAL"]; ok {
		t.Errorf("DAL is in the airline database but was counted as unknown")
	}
}

// The times in the source data are UTC and that is how they are recorded, so a
// record is just the timestamp split into a day and a minute of the day.
func TestRecordTime(t *testing.T) {
	for _, tc := range []struct {
		name     string
		utc      string
		date     string
		expected uint16
	}{
		{
			// The seconds are dropped rather than rounded.
			name: "afternoon", utc: "2026-03-30 14:04:59", date: "2026-03-30",
			expected: 14*60 + 4,
		},
		{
			name: "midnight", utc: "2026-01-01 00:00:00", date: "2026-01-01",
			expected: 0,
		},
		{
			name: "last minute of the year", utc: "2025-12-31 23:59:00", date: "2025-12-31",
			expected: 23*60 + 59,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp := makeTestImporter(t)
			imp.add(cellOf("KMSP"), "KMSP", "KORD", "DAL1234", "B738", tc.utc, true)

			key := bucket{cell: cellOf("KMSP"), departure: true}
			records := imp.buckets[key]
			if len(records) != 1 {
				t.Fatalf("expected one record in %v, got %d (buckets %v)", key, len(records), imp.buckets)
			}
			if got := av.FlightDataDate(records[0].day).Format("2006-01-02"); got != tc.date {
				t.Errorf("got date %s, expected %s", got, tc.date)
			}
			if records[0].minute != tc.expected {
				t.Errorf("got minute %d, expected %d", records[0].minute, tc.expected)
			}
		})
	}
}
func TestFlightDataFilename(t *testing.T) {
	if got := flightDataFilename("N40W074"); got != "N40W074.flt" {
		t.Errorf("got %q, expected N40W074.flt", got)
	}
}
func TestDaysInMonth(t *testing.T) {
	for _, tc := range []struct {
		year     int
		month    time.Month
		expected int
	}{
		{2026, time.January, 31},
		{2026, time.February, 28},
		{2024, time.February, 29},
		{2026, time.April, 30},
	} {
		if got := daysInMonth(tc.year, tc.month); got != tc.expected {
			t.Errorf("%d-%02d: got %d, expected %d", tc.year, tc.month, got, tc.expected)
		}
	}
}

// Only the airports the FAA controls get flights of their own; everywhere else
// is at most the far end of somebody else's.
func TestOnlyFAAAirportsAreImported(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		Callsign:            "DAL88",
		AircraftType:        "B738",
		OriginTime:          "2026-03-30 14:04:59",
		OriginAirports:      "['KMSP']",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['CYYZ']",
		Route:               "-",
	}
	imp.processRow(&row)

	// The departure from KMSP is kept; the arrival at Toronto is not.
	if n := len(imp.buckets[bucket{cell: cellOf("KMSP"), departure: true}]); n != 1 {
		t.Errorf("KMSP has %d departures, expected 1", n)
	}
	if n := len(imp.buckets[bucket{cell: cellOf("CYYZ")}]); n != 0 {
		t.Errorf("CYYZ has %d arrivals, expected none", n)
	}

	// Neither end ours is nothing to import at all.
	row.OriginAirports = "['CYYZ']"
	row.DestinationAirports = "['CYUL']"
	imp.processRow(&row)
	if imp.notFAAAirport != 1 {
		t.Errorf("notFAAAirport = %d, expected 1", imp.notFAAAirport)
	}
}

// A flight whose origin could be any of a cluster of airports still records the
// landing at the destination it names outright: losing the takeoff is no reason
// to lose the landing with it.
func TestAmbiguousOriginStillArrives(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		Callsign: "DAL88", AircraftType: "B738",
		OriginTime:     "2026-03-30 14:04:59",
		OriginAirports: "['KBUR', 'KVNY', 'KWHP']",
		OriginLatitude: "34.21", OriginLongitude: "-118.49", OriginAltitude: "6000",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['KMSP']",
		DestinationLatitude: "44.88", DestinationLongitude: "-93.22",
		DestinationAltitude: "ground",
		Route:               "nan",
	}
	imp.processRow(&row)

	if n := len(imp.buckets[bucket{cell: cellOf("KVNY"), departure: true}]); n != 0 {
		t.Errorf("KVNY has %d departures; the track was too high to say it was there", n)
	}
	arrivals := imp.buckets[bucket{cell: cellOf("KMSP")}]
	if len(arrivals) != 1 {
		t.Fatalf("KMSP has %d arrivals, expected 1", len(arrivals))
	}
	if got := imp.symbols.string(arrivals[0].other); got != "KVNY" {
		t.Errorf("arrived from %q, expected KVNY", got)
	}
	if imp.notAtItsAirport != 1 {
		t.Errorf("notAtItsAirport = %d, expected 1", imp.notAtItsAirport)
	}
}

// The same flight seen on the ground at Van Nuys is recorded as departing it.
// This is the traffic the import used to throw away for want of an itinerary.
func TestDepartureFromClusteredAirport(t *testing.T) {
	imp := makeTestImporter(t)
	row := flightRow{
		Callsign: "DAL88", AircraftType: "B738",
		OriginTime:     "2026-03-30 14:04:59",
		OriginAirports: "['KBUR', 'KVNY', 'KWHP']",
		OriginLatitude: "34.21", OriginLongitude: "-118.49", OriginAltitude: "ground",
		DestinationTime:     "2026-03-30 16:00:00",
		DestinationAirports: "['KMSP']",
		DestinationLatitude: "44.88", DestinationLongitude: "-93.22",
		DestinationAltitude: "ground",
		Route:               "nan",
	}
	imp.processRow(&row)

	departures := imp.buckets[bucket{cell: cellOf("KVNY"), departure: true}]
	if len(departures) != 1 {
		t.Fatalf("KVNY has %d departures, expected 1", len(departures))
	}
	if got := imp.symbols.string(departures[0].airport); got != "KVNY" {
		t.Errorf("departed %q, expected KVNY", got)
	}
	if imp.notAtItsAirport != 0 {
		t.Errorf("notAtItsAirport = %d, expected none", imp.notAtItsAirport)
	}
}

// Each way an endpoint can come up short has its own counter, so that an import
// report says which one cost the traffic.
func TestSkipCounters(t *testing.T) {
	base := flightRow{
		Callsign: "DAL88", AircraftType: "B738",
		OriginTime:      "2026-03-30 14:04:59",
		DestinationTime: "2026-03-30 16:00:00",
		Route:           "nan",
	}
	blank := func(row *flightRow) {
		row.OriginAirports, row.OriginLatitude = "-", "-"
		row.OriginLongitude, row.OriginAltitude = "-", "-"
		row.DestinationAirports, row.DestinationLatitude = "-", "-"
		row.DestinationLongitude, row.DestinationAltitude = "-", "-"
	}

	// Neither end of the track can be placed.
	imp := makeTestImporter(t)
	row := base
	blank(&row)
	imp.processRow(&row)
	if imp.noEndpoints != 1 {
		t.Errorf("noEndpoints = %d, expected 1", imp.noEndpoints)
	}

	// The destination is ours and certain, but nothing says where the flight
	// came from, so there is no arrival to record.
	imp = makeTestImporter(t)
	row = base
	blank(&row)
	row.DestinationAirports = "['KMSP']"
	row.DestinationLatitude, row.DestinationLongitude = "44.88", "-93.22"
	row.DestinationAltitude = "ground"
	imp.processRow(&row)
	if imp.noFarEndpoint != 1 {
		t.Errorf("noFarEndpoint = %d, expected 1", imp.noFarEndpoint)
	}
	if n := len(imp.buckets[bucket{cell: cellOf("KMSP")}]); n != 0 {
		t.Errorf("KMSP has %d arrivals, expected none", n)
	}

}

// A made-up airport is filed alongside the real one whose traffic it borrows,
// in its own cell, and a hop between two donors arrives as one between their
// stand-ins.
func TestSubstituteAirports(t *testing.T) {
	imp := makeTestImporter(t)
	imp.add(cellOf("KEWR"), "KEWR", "KTEB", "UAL1", "B738", "2026-04-10 08:00:00", true)

	newark := imp.buckets[bucket{cell: cellOf("KEWR"), departure: true}]
	if len(newark) != 1 || imp.symbols.string(newark[0].airport) != "KEWR" ||
		imp.symbols.string(newark[0].other) != "KTEB" {
		t.Errorf("the real flight was not filed as it was flown: %+v", newark)
	}

	academy := imp.buckets[bucket{cell: cellOf("KAAC"), departure: true}]
	if len(academy) != 1 {
		t.Fatalf("the Academy has %d flights, expected 1", len(academy))
	}
	if got := imp.symbols.string(academy[0].airport); got != "KAAC" {
		t.Errorf("filed under %q, expected KAAC", got)
	}
	if got := imp.symbols.string(academy[0].other); got != "4Y3" {
		t.Errorf("bound for %q, expected 4Y3", got)
	}

	// A flight to somewhere with no stand-in keeps the real airport at the far
	// end: only the Academy's own airports are made up.
	imp.add(cellOf("KEWR"), "KEWR", "KORD", "UAL2", "B738", "2026-04-10 09:00:00", true)
	academy = imp.buckets[bucket{cell: cellOf("KAAC"), departure: true}]
	if len(academy) != 2 || imp.symbols.string(academy[1].other) != "KORD" {
		t.Errorf("bound for %q, expected KORD", imp.symbols.string(academy[1].other))
	}
}

func TestWriteFlightData(t *testing.T) {
	imp := makeTestImporter(t)
	sym := imp.symbols

	april10 := av.FlightDataDayNumber(time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	duplicate := record{airport: sym.id("KMSP"), callsign: sym.id("DAL1234"), other: sym.id("KSFO"),
		acType: sym.id("B737"), day: april10, minute: 6*60 + 30}
	arrival := record{airport: sym.id("KMSP"), callsign: sym.id("UAL99"), other: sym.id("KSFO"),
		acType: sym.id("B737"), day: april10, minute: 7 * 60}
	imp.buckets[bucket{cell: cellOf("KMSP"), departure: true}] = []record{duplicate, duplicate}
	imp.buckets[bucket{cell: cellOf("KMSP")}] = []record{arrival}

	// A flight in a month the source data barely covers, which leaves its cell
	// with nothing and so no file at all.
	sparse := record{airport: sym.id("KORD"), callsign: sym.id("AAL1"), other: sym.id("KSFO"),
		acType: sym.id("B737"), minute: 6 * 60,
		day: av.FlightDataDayNumber(time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC))}
	imp.buckets[bucket{cell: cellOf("KORD"), departure: true}] = []record{sparse}

	imp.daysPresent["2026-03"] = map[string]bool{"2026-03-31": true}
	imp.daysPresent["2026-04"] = make(map[string]bool)
	for day := 1; day <= 30; day++ {
		imp.daysPresent["2026-04"][fmt.Sprintf("2026-04-%02d", day)] = true
	}

	// A file left over from an earlier import that this one has nothing for.
	dir := t.TempDir()
	stale := filepath.Join(dir, "N90"+av.FlightDataExtension)
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFlightData(dir, imp, 0.9, false); err != nil {
		t.Fatalf("writeFlightData: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a file this import has no flights for was left behind")
	}
	if _, err := os.Stat(filepath.Join(dir, cellOf("KORD")+av.FlightDataExtension)); !os.IsNotExist(err) {
		t.Errorf("wrote a file for a cell whose only flights were dropped")
	}

	data, err := os.ReadFile(filepath.Join(dir, cellOf("KMSP")+av.FlightDataExtension))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	flights, err := av.DecodeFlights(data)
	if err != nil {
		t.Fatalf("DecodeFlights: %v", err)
	}

	// One departure, with its duplicate dropped, and one arrival.
	expected := []av.Flight{
		{Airport: "KMSP", Callsign: "DAL1234", Other: "KSFO", AircraftType: "B737",
			Day: april10, Minute: 6*60 + 30, Departure: true},
		{Airport: "KMSP", Callsign: "UAL99", Other: "KSFO", AircraftType: "B737",
			Day: april10, Minute: 7 * 60},
	}
	if !slices.Equal(flights, expected) {
		t.Errorf("got %+v, expected %+v", flights, expected)
	}

	metadata, err := os.ReadFile(filepath.Join(dir, av.FlightDataMetadataName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(metadata), "2026-04-10T06:30:00Z") {
		t.Errorf("metadata doesn't cover the flights that were written: %s", metadata)
	}
}

func TestDropSparseFlights(t *testing.T) {
	flights := []av.Flight{
		{Callsign: "DAL1", Day: av.FlightDataDayNumber(
			time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC))},
		{Callsign: "DAL2", Day: av.FlightDataDayNumber(
			time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))},
	}

	daysPresent := map[string]map[string]bool{"2026-03": {"2026-03-31": true}, "2026-04": {}}
	for day := 1; day <= 30; day++ {
		daysPresent["2026-04"][fmt.Sprintf("2026-04-%02d", day)] = true
	}

	kept, dropped := dropSparseFlights(flights, coveredMonths(daysPresent, 0.9))
	if len(kept) != 1 || kept[0].Callsign != "DAL2" {
		t.Errorf("kept %+v, expected only the April flight", kept)
	}
	if dropped["2026-03"] != 1 {
		t.Errorf("dropped = %v, expected one March flight", dropped)
	}
}
