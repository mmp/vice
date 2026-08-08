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
	"KMSP": {Country: "US", Location: math.Point2LL{-93.22, 44.88}},
	"KORD": {Country: "US", Location: math.Point2LL{-87.90, 41.98}},
	"KEWR": {Country: "US", Location: math.Point2LL{-74.17, 40.69}},
	"KTEB": {Country: "US", Location: math.Point2LL{-74.06, 40.85}},
	"CYYZ": {Country: "CA", Location: math.Point2LL{-79.63, 43.68}},
	"KAAC": {Location: math.Point2LL{-95.68, 36.17}},
	"KBRT": {Location: math.Point2LL{-95.94, 36.45}},
	"KJKE": {Location: math.Point2LL{-95.62, 35.92}},
	"4Y3":  {Location: math.Point2LL{-95.35, 36.39}},
	"4V4":  {Location: math.Point2LL{-95.39, 35.98}},
}

// cellOf is where testAirports puts an airport's flights.
func cellOf(airport string) string { return av.FlightDataCell(testAirports[airport].Location) }

// makeTestImporter builds an importer over testAirports, with no resource
// loading involved.
func makeTestImporter(t *testing.T) *importer {
	t.Helper()
	imp, err := makeImporter(testAirports, map[string]av.AircraftPerformance{"B738": {}},
		map[string]av.Airline{"DAL": {}})
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
