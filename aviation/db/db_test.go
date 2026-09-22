// aviation/db/db_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"testing"

	av "github.com/mmp/vice/aviation"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

func TestAirportTimeZone(t *testing.T) {
	InitDB()

	// Airports where a guess from longitude alone, or from the state the
	// airport is in, gets the zone wrong.
	for _, tc := range []struct {
		airport av.ICAOAirportCode
		want    string
	}{
		{"KJFK", "America/New_York"},
		{"KATL", "America/New_York"},
		{"KTRI", "America/New_York"}, // east Tennessee, not Central
		{"KLSF", "America/New_York"}, // Fort Benning, hard against the Central line
		{"KEUF", "America/Chicago"},  // Eufaula, the Alabama side of the same line
		{"KVPS", "America/Chicago"},  // the Florida panhandle
		{"KGYY", "America/Chicago"},  // northwest Indiana
		{"KORD", "America/Chicago"},
		{"KMSP", "America/Chicago"},
		{"KDEN", "America/Denver"},
		{"KBOI", "America/Boise"},
		{"KPHX", "America/Phoenix"}, // no daylight saving time
		{"KLAX", "America/Los_Angeles"},
		{"KSDM", "America/Los_Angeles"}, // on the Mexican border
		{"KDTW", "America/Detroit"},
		{"KIND", "America/Indiana/Indianapolis"},
		{"PANC", "America/Anchorage"},
		{"PHNL", "Pacific/Honolulu"},
	} {
		loc, ok := DB.AirportTimeZone(tc.airport)
		if !ok {
			t.Errorf("%s: no time zone", tc.airport)
		} else if loc.String() != tc.want {
			t.Errorf("%s: got %s, want %s", tc.airport, loc, tc.want)
		}
	}

	if _, ok := DB.AirportTimeZone("XXXX"); ok {
		t.Errorf("unknown airport returned a time zone")
	}
}

// TestCIFPAirportsAreInAirportsDatabase catches the signature of an airport the
// FAA has re-identified: the CIFP picks the new identifier up on the next AIRAC
// cycle, while airports.csv.zst goes on listing the old one, and the merge in
// doInitDB leaves the new one with no name and no country. That in turn makes
// FAAControlled false, drops the airport's flights on import, and has the
// airport spelled out rather than said on the radio -- all silently. Refresh
// airports.csv.zst from https://ourairports.com/data/, and add the change to
// RenamedAirports so the historical data still finds the airport.
func TestCIFPAirportsAreInAirportsDatabase(t *testing.T) {
	InitDB()

	_, custom := parseAirports()

	for _, id := range util.SortedMapKeys(DB.Airports) {
		ap := DB.Airports[id]
		// Runways only come from the CIFP or from custom_airports.json, whose
		// made-up airports have no name or country by design. Only the
		// contiguous US uses four-character K identifiers; elsewhere in the
		// FAA's regions the CIFP mixes ICAO ids with local ones like TX15
		// that ourairports reasonably has no gps_code for.
		if len(ap.Runways) == 0 || len(id) != 4 || id[0] != 'K' {
			continue
		}
		if _, ok := custom[id]; ok {
			continue
		}
		if ap.Name == "" || ap.Country == "" {
			t.Errorf("%s: in the CIFP but not the airports database (name %q, country %q)", id, ap.Name, ap.Country)
		}
	}
}

// TestRunwayThresholdsMatchHeadings catches a runway whose Threshold is the end
// the aircraft rolls toward rather than the end it lands on. Nothing else does:
// LookupOppositeRunway pairs runways by name, so a reversed pair still resolves,
// and ExitRoute.initialize's "first fix is behind the aircraft" check turns
// itself off when the heading and the geometry disagree by more than 45
// degrees--which is exactly what a reversed pair looks like. Downstream, the
// takeoff roll direction, the approach's runway heading, and the landing
// waypoint all come out reciprocal.
//
// The comparison is a true bearing against a magnetic heading, so the tolerance
// has to absorb the magnetic variation. Applying a modern variation instead
// would be worse rather than tighter: the FAA's declared variation at remote
// Alaskan fields is decades stale--the CIFP still carries 30 degrees east at
// PACR and L20, against about 17 today--and their published runway bearings
// follow the stale value, so the WMM grid manufactures ten degrees of
// disagreement at a few hundred runway ends there. The worst legitimate case in
// the database is 30 degrees; a reversed pair is 180.
func TestRunwayThresholdsMatchHeadings(t *testing.T) {
	InitDB()

	for _, id := range util.SortedMapKeys(DB.Airports) {
		for _, rwy := range DB.Airports[id].Runways {
			opp, ok := av.LookupOppositeRunway(Lookups{}, id, rwy.Id)
			if !ok {
				continue
			}
			brg := math.Heading2LL(rwy.Threshold, opp.Threshold, math.NMPerLongitudeAt(rwy.Threshold))
			if d := math.HeadingDifference(float32(brg), float32(rwy.Heading)); d > 45 {
				t.Errorf("%s: runway %s has heading %.0f but its threshold bears %.0f toward runway %s's; "+
					"off by %.0f degrees, so the thresholds are probably at the wrong ends",
					id, rwy.Id, float32(rwy.Heading), float32(brg), opp.Id, d)
			}
		}
	}
}
