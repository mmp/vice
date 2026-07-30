// sim/schedule_catalog_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"
	"testing/fstest"
)

const validScheduleCSV = "callsign,origin,destination,aircraft_type,time,cargo\n" +
	"DAL1045,KMSP,KATL,A321,14:05,false\n" +
	"FDX1412,KMEM,KMSP,B763,14:17,true\n"

func TestLoadBuiltInScheduleCatalog(t *testing.T) {
	filesystem := fstest.MapFS{
		"schedules/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(validScheduleCSV)},
		"schedules/KMSP/schedules.json":     &fstest.MapFile{Data: []byte(`[]`)},
	}

	catalog, err := LoadBuiltInScheduleCatalog(filesystem, "schedules")
	if err != nil {
		t.Fatalf("LoadBuiltInScheduleCatalog: %v", err)
	}
	if len(catalog.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(catalog.Schedules))
	}

	schedule := catalog.Schedules[0]
	if schedule.Airport != "KMSP" || schedule.ID != "summer_weekday" || schedule.Name != "Summer Weekday" {
		t.Fatalf("unexpected schedule metadata: %+v", schedule)
	}
	if schedule.Description != "" || schedule.Timezone != "" {
		t.Fatalf("unexpected optional metadata: %+v", schedule)
	}
	if len(schedule.Flights) != 2 {
		t.Fatalf("got %d flights, want 2", len(schedule.Flights))
	}
	if got := catalog.ForAirport("kmsp"); len(got) != 1 {
		t.Fatalf("ForAirport returned %d schedules, want 1", len(got))
	}
	if got := catalog.ForAirport("KORD"); len(got) != 0 {
		t.Fatalf("ForAirport returned %d KORD schedules, want 0", len(got))
	}

	summaries := catalog.SummariesForAirport("KMSP")
	if len(summaries) != 1 {
		t.Fatalf("SummariesForAirport returned %d schedules, want 1", len(summaries))
	}
	if summary := summaries[0]; summary.ID != schedule.ID ||
		summary.Name != schedule.Name ||
		summary.Airport != schedule.Airport ||
		summary.Description != schedule.Description ||
		summary.Timezone != schedule.Timezone {
		t.Fatalf("unexpected schedule summary: %+v", summary)
	}
}

func TestLoadBuiltInScheduleCatalogMultipleAirportsAndSorting(t *testing.T) {
	filesystem := fstest.MapFS{
		"schedules/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(validScheduleCSV)},
		"schedules/KMSP/cargo-heavy.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time,cargo\n" +
				"FDX1412,KMEM,KMSP,B763,14:17,true\n")},
		"schedules/KORD/evening_push.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time,cargo\n" +
				"UAL123,KORD,KLAX,B738,18:00,false\n")},
	}

	catalog, err := LoadBuiltInScheduleCatalog(filesystem, "schedules")
	if err != nil {
		t.Fatalf("LoadBuiltInScheduleCatalog: %v", err)
	}
	if len(catalog.Schedules) != 3 {
		t.Fatalf("got %d schedules, want 3", len(catalog.Schedules))
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
		got := catalog.Schedules[i]
		if got.Airport != expected.airport || got.ID != expected.id || got.Name != expected.name {
			t.Fatalf("schedule %d = %+v, want airport=%s id=%s name=%s", i, got, expected.airport, expected.id, expected.name)
		}
	}
}

func TestLoadBuiltInScheduleCatalogValidation(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"flight does not serve airport": {
			"schedules/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(
				"callsign,origin,destination,aircraft_type,time\n" +
					"DAL1,KATL,KDTW,A321,12:00\n")},
		},
		"malformed CSV": {
			"schedules/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(
				"callsign,origin,destination,aircraft_type,time\n" +
					"DAL1,KMSP,KDTW,A321,not-a-time\n")},
		},
		"duplicate schedule name ignoring case": {
			"schedules/KMSP/Weekday.csv": &fstest.MapFile{Data: []byte(validScheduleCSV)},
			"schedules/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(validScheduleCSV)},
		},
	}

	for name, filesystem := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadBuiltInScheduleCatalog(filesystem, "schedules"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadBuiltInScheduleCatalogIgnoresNestedCSV(t *testing.T) {
	filesystem := fstest.MapFS{
		"schedules/KMSP/other/weekday.csv": &fstest.MapFile{Data: []byte(validScheduleCSV)},
	}

	catalog, err := LoadBuiltInScheduleCatalog(filesystem, "schedules")
	if err != nil {
		t.Fatalf("LoadBuiltInScheduleCatalog: %v", err)
	}
	if len(catalog.Schedules) != 0 {
		t.Fatalf("got %d schedules, want 0", len(catalog.Schedules))
	}
}

func TestScheduleDisplayName(t *testing.T) {
	tests := map[string]string{
		"summer_weekday": "Summer Weekday",
		"cargo-heavy":    "Cargo Heavy",
		"MSP_AM_Rush":    "MSP AM Rush",
		"Weekend":        "Weekend",
	}
	for id, want := range tests {
		if got := scheduleDisplayName(id); got != want {
			t.Errorf("scheduleDisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestBuiltInScheduleCatalogFind(t *testing.T) {
	catalog := BuiltInScheduleCatalog{
		Schedules: []BuiltInSchedule{
			{
				ID:      "summer-weekday",
				Airport: "KMSP",
				Name:    "Summer Weekday",
			},
		},
	}

	schedule, ok := catalog.Find("kmsp", "summer-weekday")
	if !ok {
		t.Fatal("Find did not return the schedule")
	}
	if schedule.Name != "Summer Weekday" {
		t.Fatalf("Find returned %q, want Summer Weekday", schedule.Name)
	}
	if _, ok := catalog.Find("KMSP", "missing"); ok {
		t.Fatal("Find returned a missing schedule")
	}
}

func TestLoadBuiltInScheduleCatalogMissingRoot(t *testing.T) {
	_, err := LoadBuiltInScheduleCatalog(fstest.MapFS{}, "schedules")
	if err == nil || !strings.Contains(err.Error(), "load built-in schedules") {
		t.Fatalf("got error %v, want wrapped missing-root error", err)
	}
}
