// traffic/timetable_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package traffic

import (
	"os"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"

	"testing/fstest"
)

func TestLoadTimetableCSV(t *testing.T) {
	input := `destination,time,callsign,cargo,aircraft_type,origin,ignored
KATL,14:05,dal1045,false,A321,KMSP,value
KMSP,14:17,FDX1412,yes,B763,KMEM,value
`

	flights, err := LoadTimetableCSV(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadTimetableCSV: %v", err)
	}
	if len(flights) != 2 {
		t.Fatalf("got %d flights, want 2", len(flights))
	}

	departure := flights[0]
	if departure.Callsign != "DAL1045" || departure.Origin != "KMSP" || departure.Destination != "KATL" ||
		departure.AircraftType != "A321" || departure.PublishedMinute != 14*60+5 || departure.Cargo {
		t.Errorf("unexpected departure: %#v", departure)
	}
	if got := departure.OperationAt("kmsp"); got != TimetableOperationDeparture {
		t.Errorf("departure OperationAt = %v, want %v", got, TimetableOperationDeparture)
	}

	arrival := flights[1]
	if !arrival.Cargo {
		t.Errorf("cargo = false, want true")
	}
	if got := arrival.OperationAt("KMSP"); got != TimetableOperationArrival {
		t.Errorf("arrival OperationAt = %v, want %v", got, TimetableOperationArrival)
	}
}

func TestLoadTimetableCSVValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty",
			input: "",
			want:  "empty",
		},
		{
			name:  "missing column",
			input: "callsign,origin,destination,time\nDAL1,KMSP,KATL,14:05\n",
			want:  `missing required "aircraft_type"`,
		},
		{
			name:  "bad time",
			input: "callsign,origin,destination,aircraft_type,time\nDAL1,KMSP,KATL,A321,25:05\n",
			want:  "invalid hour",
		},
		{
			name:  "bad cargo",
			input: "callsign,origin,destination,aircraft_type,time,cargo\nDAL1,KMSP,KATL,A321,14:05,maybe\n",
			want:  "must be true or false",
		},
		{
			name:  "no flights",
			input: "callsign,origin,destination,aircraft_type,time\n",
			want:  "contains no flights",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadTimetableCSV(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestTimetableFlightOperationAt(t *testing.T) {
	tests := []struct {
		name   string
		flight TimetableFlight
		want   TimetableOperation
	}{
		{"departure", TimetableFlight{Origin: "KMSP", Destination: "KATL"}, TimetableOperationDeparture},
		{"arrival", TimetableFlight{Origin: "KATL", Destination: "KMSP"}, TimetableOperationArrival},
		{"unrelated", TimetableFlight{Origin: "KATL", Destination: "KDTW"}, TimetableOperationUnknown},
		{"same airport", TimetableFlight{Origin: "KMSP", Destination: "KMSP"}, TimetableOperationUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.flight.OperationAt("KMSP"); got != test.want {
				t.Errorf("OperationAt = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateTimetable(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 600,
			},
		},
	}

	if err := validateTimetable(timetable); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateTimetableRejectsDuplicateRows(t *testing.T) {
	flight := TimetableFlight{
		Callsign:        "DAL100",
		Origin:          "KMSP",
		Destination:     "KORD",
		AircraftType:    "A320",
		PublishedMinute: 600,
	}

	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			flight,
			flight,
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected duplicate row validation error")
	}
}
func TestValidateTimetableAllowsCallsignReuse(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 600,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 780,
			},
		},
	}

	if err := validateTimetable(timetable); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
func TestValidateTimetableRejectsOverlappingCallsignReuse(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 9 * 60,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 9*60 + 20,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected overlapping callsign validation error")
	}
}
func TestValidateTimetableRejectsCrossMidnightCallsignOverlap(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL200",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 23*60 + 55,
			},
			{
				Callsign:        "DAL200",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 10,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected cross-midnight callsign validation error")
	}
}
func TestValidateTimetableRejectsUnknownOriginAirport(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL300",
				Origin:          "KZZZ",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 12 * 60,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected unknown origin airport validation error")
	}
}

func TestValidateTimetableRejectsUnknownDestinationAirport(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL301",
				Origin:          "KMSP",
				Destination:     "KZZZ",
				AircraftType:    "A320",
				PublishedMinute: 13 * 60,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected unknown destination airport validation error")
	}
}

func TestMain(m *testing.M) {
	av.InitDB()
	os.Exit(m.Run())
}

const validTimetableCSV = "callsign,origin,destination,aircraft_type,time,cargo\n" +
	"DAL1045,KMSP,KATL,A321,14:05,false\n" +
	"FDX1412,KMEM,KMSP,B763,14:17,true\n"

func TestLoadTimetableCatalog(t *testing.T) {
	filesystem := fstest.MapFS{
		"timetables/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
		"timetables/KMSP/timetables.json":    &fstest.MapFile{Data: []byte(`[]`)},
	}

	catalog, err := LoadTimetableCatalog(filesystem, "timetables")
	if err != nil {
		t.Fatalf("LoadTimetableCatalog: %v", err)
	}
	if len(catalog.Timetables) != 1 {
		t.Fatalf("got %d timetables, want 1", len(catalog.Timetables))
	}

	timetable := catalog.Timetables[0]
	if timetable.Airport != "KMSP" || timetable.ID != "summer_weekday" || timetable.Name != "Summer Weekday" {
		t.Fatalf("unexpected timetable metadata: %+v", timetable)
	}
	if timetable.Description != "" {
		t.Fatalf("unexpected optional metadata: %+v", timetable)
	}
	if len(timetable.Flights) != 2 {
		t.Fatalf("got %d flights, want 2", len(timetable.Flights))
	}
	if got := catalog.ForAirport("kmsp"); len(got) != 1 {
		t.Fatalf("ForAirport returned %d timetables, want 1", len(got))
	}
	if got := catalog.ForAirport("KORD"); len(got) != 0 {
		t.Fatalf("ForAirport returned %d KORD timetables, want 0", len(got))
	}

	summaries := catalog.SummariesForAirport("KMSP")
	if len(summaries) != 1 {
		t.Fatalf("SummariesForAirport returned %d timetables, want 1", len(summaries))
	}
	if summary := summaries[0]; summary.ID != timetable.ID ||
		summary.Name != timetable.Name ||
		summary.Airport != timetable.Airport ||
		summary.Description != timetable.Description {
		t.Fatalf("unexpected timetable summary: %+v", summary)
	}
}

func TestLoadTimetableCatalogMultipleAirportsAndSorting(t *testing.T) {
	filesystem := fstest.MapFS{
		"timetables/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
		"timetables/KMSP/cargo-heavy.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time,cargo\n" +
				"FDX1412,KMEM,KMSP,B763,14:17,true\n")},
		"timetables/KORD/evening_push.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time,cargo\n" +
				"UAL123,KORD,KLAX,B738,18:00,false\n")},
	}

	catalog, err := LoadTimetableCatalog(filesystem, "timetables")
	if err != nil {
		t.Fatalf("LoadTimetableCatalog: %v", err)
	}
	if len(catalog.Timetables) != 3 {
		t.Fatalf("got %d timetables, want 3", len(catalog.Timetables))
	}

	want := []struct {
		airport av.ICAOAirportCode
		id      string
		name    string
	}{
		{"KMSP", "cargo-heavy", "Cargo Heavy"},
		{"KMSP", "summer_weekday", "Summer Weekday"},
		{"KORD", "evening_push", "Evening Push"},
	}
	for i, expected := range want {
		got := catalog.Timetables[i]
		if got.Airport != expected.airport || got.ID != expected.id || got.Name != expected.name {
			t.Fatalf("timetable %d = %+v, want airport=%s id=%s name=%s", i, got, expected.airport, expected.id, expected.name)
		}
	}
}

func TestLoadTimetableCatalogValidation(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"flight does not serve airport": {
			"timetables/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(
				"callsign,origin,destination,aircraft_type,time\n" +
					"DAL1,KATL,KDTW,A321,12:00\n")},
		},
		"malformed CSV": {
			"timetables/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(
				"callsign,origin,destination,aircraft_type,time\n" +
					"DAL1,KMSP,KDTW,A321,not-a-time\n")},
		},
		"duplicate timetable name ignoring case": {
			"timetables/KMSP/Weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
			"timetables/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
		},
	}

	for name, filesystem := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadTimetableCatalog(filesystem, "timetables"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadTimetableCatalogIgnoresNestedCSV(t *testing.T) {
	filesystem := fstest.MapFS{
		"timetables/KMSP/other/weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
	}

	catalog, err := LoadTimetableCatalog(filesystem, "timetables")
	if err != nil {
		t.Fatalf("LoadTimetableCatalog: %v", err)
	}
	if len(catalog.Timetables) != 0 {
		t.Fatalf("got %d timetables, want 0", len(catalog.Timetables))
	}
}

func TestTimetableDisplayName(t *testing.T) {
	tests := map[string]string{
		"summer_weekday": "Summer Weekday",
		"cargo-heavy":    "Cargo Heavy",
		"MSP_AM_Rush":    "MSP AM Rush",
		"Weekend":        "Weekend",
	}
	for id, want := range tests {
		if got := timetableDisplayName(id); got != want {
			t.Errorf("timetableDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestTimetableCatalogFind(t *testing.T) {
	catalog := TimetableCatalog{
		Timetables: []Timetable{
			{
				ID:      "summer-weekday",
				Airport: "KMSP",
				Name:    "Summer Weekday",
			},
		},
	}

	timetable, ok := catalog.Find("kmsp", "summer-weekday")
	if !ok {
		t.Fatal("Find did not return the timetable")
	}
	if timetable.Name != "Summer Weekday" {
		t.Fatalf("Find returned %q, want Summer Weekday", timetable.Name)
	}
	if _, ok := catalog.Find("KMSP", "missing"); ok {
		t.Fatal("Find returned a missing timetable")
	}
}

func TestLoadTimetableCatalogMissingRoot(t *testing.T) {
	_, err := LoadTimetableCatalog(fstest.MapFS{}, "timetables")
	if err == nil || !strings.Contains(err.Error(), "load built-in timetables") {
		t.Fatalf("got error %v, want wrapped missing-root error", err)
	}
}

// Loading one airport's timetables must not depend on--or even look at--what
// other airports publish, so that the cost of finding one holds steady as
// timetables are added.
func TestLoadTimetableCatalogForAirport(t *testing.T) {
	filesystem := fstest.MapFS{
		"timetables/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(validTimetableCSV)},
		"timetables/KMSP/other/nested.csv":   &fstest.MapFile{Data: []byte(validTimetableCSV)},
		// Another airport's timetable, and one that wouldn't parse at all: if
		// either is read, the KMSP lookup below notices.
		"timetables/KATL/weekday.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time\nDAL2,KATL,KMSP,A321,09:00\n")},
		"timetables/KORD/broken.csv": &fstest.MapFile{Data: []byte("nonsense\n")},
	}

	catalog, err := LoadTimetableCatalogForAirport(filesystem, "timetables", "kmsp")
	if err != nil {
		t.Fatalf("LoadTimetableCatalogForAirport: %v", err)
	}
	if len(catalog.Timetables) != 1 {
		t.Fatalf("got %d timetables, want only KMSP's one: %+v", len(catalog.Timetables), catalog.Timetables)
	}
	if got := catalog.Timetables[0]; got.Airport != "KMSP" || got.ID != "summer_weekday" {
		t.Fatalf("got %+v, want the KMSP summer_weekday timetable", got)
	}

	// An airport that publishes nothing has no timetables, which is not an error.
	catalog, err = LoadTimetableCatalogForAirport(filesystem, "timetables", "KDEN")
	if err != nil {
		t.Fatalf("LoadTimetableCatalogForAirport for an airport with none: %v", err)
	}
	if len(catalog.Timetables) != 0 {
		t.Fatalf("got %d timetables for KDEN, want 0", len(catalog.Timetables))
	}

	// Reading everything, on the other hand, does have to report the bad file.
	if _, err := LoadTimetableCatalog(filesystem, "timetables"); err == nil {
		t.Error("loading every timetable ignored the malformed one")
	}
}
