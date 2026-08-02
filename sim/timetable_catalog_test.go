// sim/timetable_catalog_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"
	"testing/fstest"
)

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
		airport string
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
