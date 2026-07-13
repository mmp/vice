// sim/schedule_catalog_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"testing/fstest"
)

func TestLoadBuiltInScheduleCatalog(t *testing.T) {
	filesystem := fstest.MapFS{
		"schedules/KMSP/schedule.json": &fstest.MapFile{Data: []byte(`{
			"airport": "kmsp",
			"schedules": [{
				"id": "summer_weekday",
				"name": "MSP Summer Weekday",
				"file": "summer_weekday.csv",
				"description": "Representative weekday operation",
				"timezone": "America/Chicago"
			}]
		}`)},
		"schedules/KMSP/summer_weekday.csv": &fstest.MapFile{Data: []byte(
			"callsign,origin,destination,aircraft_type,time,cargo\n" +
				"DAL1045,KMSP,KATL,A321,14:05,false\n" +
				"FDX1412,KMEM,KMSP,B763,14:17,true\n")},
	}

	catalog, err := LoadBuiltInScheduleCatalog(filesystem, "schedules")
	if err != nil {
		t.Fatalf("LoadBuiltInScheduleCatalog: %v", err)
	}
	if len(catalog.Schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(catalog.Schedules))
	}

	schedule := catalog.Schedules[0]
	if schedule.Airport != "KMSP" || schedule.ID != "summer_weekday" || schedule.Timezone != "America/Chicago" {
		t.Fatalf("unexpected schedule metadata: %+v", schedule)
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
	if summary := summaries[0]; summary.ID != schedule.ID || summary.Name != schedule.Name ||
		summary.Airport != schedule.Airport || summary.Description != schedule.Description ||
		summary.Timezone != schedule.Timezone {
		t.Fatalf("unexpected schedule summary: %+v", summary)
	}
}

func TestLoadBuiltInScheduleCatalogValidation(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"flight does not serve airport": {
			"schedules/KMSP/schedule.json": &fstest.MapFile{Data: []byte(`{
				"airport":"KMSP",
				"schedules":[{"id":"weekday","name":"Weekday","file":"weekday.csv","timezone":"America/Chicago"}]
			}`)},
			"schedules/KMSP/weekday.csv": &fstest.MapFile{Data: []byte(
				"callsign,origin,destination,aircraft_type,time\nDAL1,KATL,KDTW,A321,12:00\n")},
		},
		"missing CSV": {
			"schedules/KMSP/schedule.json": &fstest.MapFile{Data: []byte(`{
				"airport":"KMSP",
				"schedules":[{"id":"weekday","name":"Weekday","file":"weekday.csv","timezone":"America/Chicago"}]
			}`)},
		},
		"nested CSV path": {
			"schedules/KMSP/schedule.json": &fstest.MapFile{Data: []byte(`{
				"airport":"KMSP",
				"schedules":[{"id":"weekday","name":"Weekday","file":"other/weekday.csv","timezone":"America/Chicago"}]
			}`)},
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
