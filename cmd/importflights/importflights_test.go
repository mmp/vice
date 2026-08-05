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
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
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

func TestResolveEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name                string
		origins             []string
		destinations        []string
		route               []string
		origin, destination string
		ok                  bool
	}{
		{
			name: "both known", origins: []string{"KMSP"}, destinations: []string{"KSTL"},
			origin: "KMSP", destination: "KSTL", ok: true,
		},
		{
			name: "both known, route ignored", origins: []string{"KMSP"}, destinations: []string{"KSTL"},
			route: []string{"KORD", "KATL"}, origin: "KMSP", destination: "KSTL", ok: true,
		},
		{
			name: "origin picked from route", origins: []string{"KACT", "KAUS", "KBBD"},
			destinations: []string{"KMSP"}, route: []string{"KAUS", "KMSP"},
			origin: "KAUS", destination: "KMSP", ok: true,
		},
		{
			name: "leg picked from round trip", origins: []string{"KMSP"},
			destinations: []string{"KBOI", "KTWF"}, route: []string{"KMSP", "KBOI", "KMSP"},
			origin: "KMSP", destination: "KBOI", ok: true,
		},
		{
			name: "no track, single leg route", route: []string{"KMSP", "KSTL"},
			origin: "KMSP", destination: "KSTL", ok: true,
		},
		{
			name: "no track, ambiguous round trip", route: []string{"KMSP", "KAUS", "KMSP"},
		},
		{
			name: "ambiguous both ends, no route", origins: []string{"KACT", "KAUS"},
			destinations: []string{"KMSP", "KSTL"},
		},
		{
			name: "route disagrees with track", origins: []string{"KMSP"},
			destinations: []string{"KSTL", "KORD"}, route: []string{"KATL", "KMCO"},
		},
		{
			name: "nothing at all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin, destination, ok := resolveEndpoints(tc.origins, tc.destinations, tc.route)
			if ok != tc.ok {
				t.Fatalf("ok = %v, expected %v", ok, tc.ok)
			}
			if ok && (origin != tc.origin || destination != tc.destination) {
				t.Errorf("got %s-%s, expected %s-%s", origin, destination, tc.origin, tc.destination)
			}
		})
	}
}

// A type the aircraft database doesn't have is imported all the same and
// counted, so that the report says what to add to openscope-aircraft.json. A
// row with no type at all is skipped.
func TestUnknownAircraftTypes(t *testing.T) {
	imp := makeTestImporter("KMSP")
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
	for _, r := range imp.buckets[bucket{airport: "KMSP", departure: true}] {
		types[imp.symbols.string(r.acType)]++
	}
	if got := map[string]int{"B738": 1, "E45X": 1}; !maps.Equal(types, got) {
		t.Errorf("imported types %v, expected %v", types, got)
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

// makeTestImporter builds an importer for a single airport with no scenario or
// database lookups involved.
func makeTestImporter(airport string) *importer {
	return makeImporter(map[string]bool{airport: true}, map[string]bool{airport: true},
		map[string]av.FAAAirport{airport: {}, "KORD": {}},
		map[string]av.AircraftPerformance{"B738": {}}, map[string]av.Airline{"DAL": {}})
}

// A callsign whose prefix isn't in the airline database is counted so that the
// report can list what's missing from openscope-airlines.json.
func TestUnknownAirlineCallsigns(t *testing.T) {
	imp := makeTestImporter("KMSP")
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
			imp := makeTestImporter("KMSP")
			imp.add("KMSP", "KORD", "DAL1234", "B738", tc.utc, true)

			key := bucket{airport: "KMSP", departure: true}
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
	if got := flightDataFilename("N90"); got != "N90.flt" {
		t.Errorf("got %q, expected N90.flt", got)
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
func TestWriteFlightData(t *testing.T) {
	sym := makeSymbols()
	imp := &importer{symbols: sym, buckets: make(map[bucket][]record),
		daysPresent: make(map[string]map[string]bool)}

	april10 := av.FlightDataDayNumber(time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC))
	duplicate := record{callsign: sym.id("DAL1234"), other: sym.id("KSFO"), acType: sym.id("B737"),
		day: april10, minute: 6*60 + 30}
	arrival := record{callsign: sym.id("UAL99"), other: sym.id("KSFO"), acType: sym.id("B737"),
		day: april10, minute: 7 * 60}
	imp.buckets[bucket{airport: "KMSP", departure: true}] = []record{duplicate, duplicate}
	imp.buckets[bucket{airport: "KMSP"}] = []record{arrival}

	// A facility standing in for made-up airports, with a flight between two of
	// its donors so that both ends get renamed.
	borrowed := record{callsign: sym.id("UAL1"), other: sym.id("KTEB"), acType: sym.id("B737"),
		day: april10, minute: 8 * 60}
	imp.buckets[bucket{airport: "KEWR", departure: true}] = []record{borrowed}

	imp.daysPresent["2026-04"] = make(map[string]bool)
	for day := 1; day <= 30; day++ {
		imp.daysPresent["2026-04"][fmt.Sprintf("2026-04-%02d", day)] = true
	}

	facilities := map[string]*facility{
		"M98": {departures: map[string]bool{"KMSP": true}, arrivals: map[string]bool{"KMSP": true}},
		// A facility with no flights imported gets no file at all.
		"XYZ": {departures: map[string]bool{"KZZZ": true}, arrivals: map[string]bool{}},
		"AAC": {departures: map[string]bool{"KEWR": true}, arrivals: map[string]bool{},
			substituted: map[string]string{"KEWR": "KAAC", "KTEB": "4Y3"}},
	}

	dir := t.TempDir()
	if err := writeFlightData(dir, imp, facilities, 0.9, false); err != nil {
		t.Fatalf("writeFlightData: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "XYZ.flt")); !os.IsNotExist(err) {
		t.Errorf("wrote a file for a facility with no flights")
	}

	data, err := os.ReadFile(filepath.Join(dir, "M98.flt"))
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

	// The borrowed flight is written out under the made-up airports at both ends.
	data, err = os.ReadFile(filepath.Join(dir, "AAC.flt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	flights, err = av.DecodeFlights(data)
	if err != nil {
		t.Fatalf("DecodeFlights: %v", err)
	}
	expected = []av.Flight{
		{Airport: "KAAC", Callsign: "UAL1", Other: "4Y3", AircraftType: "B737",
			Day: april10, Minute: 8 * 60, Departure: true},
	}
	if !slices.Equal(flights, expected) {
		t.Errorf("got %+v, expected %+v", flights, expected)
	}
}

func TestRestrictFacilities(t *testing.T) {
	facilities := map[string]*facility{
		"N90": {departures: map[string]bool{"KJFK": true, "KLGA": true},
			arrivals: map[string]bool{"KJFK": true}},
		"A80": {departures: map[string]bool{"KATL": true}, arrivals: map[string]bool{"KATL": true}},
	}
	restrict(facilities, []string{"KJFK"})

	if len(facilities) != 1 || facilities["N90"] == nil {
		t.Fatalf("expected only N90 to survive, got %v", slices.Sorted(maps.Keys(facilities)))
	}
	if !slices.Equal(slices.Sorted(maps.Keys(facilities["N90"].departures)), []string{"KJFK"}) {
		t.Errorf("KLGA should have been dropped: %v", facilities["N90"].departures)
	}
}

func TestSubstituteAirports(t *testing.T) {
	f := makeFacility()
	f.add(f.departures, "KMSP") // a real airport is taken as it is
	f.add(f.departures, "KAAC") // a made-up one is swapped for its donor
	f.add(f.departures, "4Y3")  // and so is one with a three-character id
	f.add(f.departures, "X23")  // which is otherwise not in the source data

	if !slices.Equal(slices.Sorted(maps.Keys(f.departures)), []string{"KEWR", "KMSP", "KTEB"}) {
		t.Errorf("gathered %v", slices.Sorted(maps.Keys(f.departures)))
	}
	for donor, expected := range map[string]string{"KEWR": "KAAC", "KTEB": "4Y3"} {
		if got := f.name(donor); got != expected {
			t.Errorf("%s stands in for %q, expected %q", donor, got, expected)
		}
	}
	if got := f.name("KMSP"); got != "KMSP" {
		t.Errorf("KMSP was renamed to %q", got)
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
