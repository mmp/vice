// cmd/importfixpairs/importfixpairs_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package main

import (
	"encoding/json"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
)

// testDump is a miniature version of the three DMS window dumps: two fix
// pairs per flight type bucket plus wildcards, a single configuration plan,
// and a reassignment section whose columns continue in a second table.
const testDump = `
####################  Fix Pair Configurations (1 of 2)  ####################

******* FIXPAIRS *******
----------------------------------------------------------------------------------------------------------------------------
|#.    |fixpair_id |Facility Id |Terminal Sector |Entry Fix |Exit Fix |Flight Type |From Facility |Fix Pair Id |RNAV Route |
----------------------------------------------------------------------------------------------------------------------------
|1     |1000       |NNN         |N               |ARD       |EWR      |A           |ZCN           |            |Y          |
|2     |1001       |NNN         |N               |ARD       |~~~      |A           |ZCN           |            |           |
|3     |1002       |NNN         |N               |JFK       |~~~      |P           |              |            |           |
|4     |1003       |NNN         |N               |ZZZ       |~~~      |E           |ZCN           |            |           |
|5     |1004       |NNN         |N               |NTC       |~~~      |A           |ZCN           |            |           |
|6     |1005       |NNN         |N               |ANY       |~~~      |*           |ZCN           |            |           |
----------------------------------------------------------------------------------------------------------------------------

******* CONFIGURATION *******
----------------------------------------------------------------------
|#.    |Facility ID |Config ID |Config Name          |Strt Config ID |
--------============-==========---------------------------------------
|1     |NNN         |CP1       |CONFIGURATION 1      |Y              |
----------------------------------------------------------------------

******* FIXPAIRS_RNAV *******
------------------------------------------------------------------------
|#.    |fixpair_id |fix1 |fix2 |type_of_flight |Config ID |RNAV Region |
------------------------------------------------------------------------
|    |           |     |     |               |          |            |
------------------------------------------------------------------------

******* FIXPAIRS_TCP *******
------------------------------------------------------------------------
|#.    |fixpair_id |fix1 |fix2 |type_of_flight |Config ID |Logical TCP |
------------------------------------------------------------------------
|1     |1002       |JFK  |~~~  |P              |CP1       |2J          |
|2     |1000       |ARD  |EWR  |A              |CP1       |4P          |
|3     |1001       |ARD  |~~~  |A              |CP1       |4H          |
|4     |1003       |ZZZ  |~~~  |E              |CP1       |7S          |
|5     |1005       |ANY  |~~~  |*              |CP1       |1D          |
------------------------------------------------------------------------

******* FIXPAIR_REASSIGNMENT *******
-----------------------------------------------------------------------------------------------------------------------
|#.    |seq_id |Terminal Area I |Reassignment Ty |FP-Fix 1 |FP-Fix 2 |Lower Band |Upper Band |A/C Type Class |ACID    |
--------=======-================-================-=========-=========-===========-===========-===============-========-
|1     |1      |NNN             |TCP             |ARD      |EWR      |041        |999        |*              |*       |
|2     |0      |NNN             |TCP             |ARD      |EWR      |000        |040        |J              |Nd***** |
|3     |2      |NNN             |AHO             |ARD      |EWR      |000        |999        |*              |*       |
-----------------------------------------------------------------------------------------------------------------------

---------------------------------------------------------------------
|#.    |Type of Flight |Active Runway |Derived-Fix 1 |Derived-Fix 2 |
--------===============-==============-------------------------------
|1     |A              |*             |AR2           |EWR           |
|2     |A              |22L           |AR1           |*             |
|3     |A              |*             |ARX           |EWR           |
---------------------------------------------------------------------

;END
`

func testAdaptation(t *testing.T) (*adaptation, []string) {
	t.Helper()
	a, warnings, err := buildAdaptation(parseReport(testDump))
	if err != nil {
		t.Fatalf("buildAdaptation: %v", err)
	}
	return a, warnings
}

func TestParseReport(t *testing.T) {
	sections := parseReport(testDump)
	names := make(map[string]int)
	for _, s := range sections {
		names[s.name] = len(s.tables)
	}
	for name, tables := range map[string]int{
		"FIXPAIRS": 1, "CONFIGURATION": 1, "FIXPAIRS_RNAV": 1,
		"FIXPAIRS_TCP": 1, "FIXPAIR_REASSIGNMENT": 2, // continuation table
	} {
		if names[name] != tables {
			t.Errorf("section %s: %d tables, want %d", name, names[name], tables)
		}
	}
	for _, s := range sections {
		if s.name == "FIXPAIRS_RNAV" && len(s.tables[0].rows) != 0 {
			t.Errorf("all-blank placeholder row should be skipped, got %v", s.tables[0].rows)
		}
	}
}

func TestBuildAdaptation(t *testing.T) {
	a, warnings := testAdaptation(t)

	// Window-1 order within buckets; "~~~" becomes "*"; RNAV carried; the
	// "*"-type pair lands in all three buckets; the pair with no TCP (1004)
	// is dropped.
	wantArrival := [][2]string{{"ARD", "EWR"}, {"ARD", "*"}, {"ANY", "*"}}
	if len(a.arrival) != len(wantArrival) {
		t.Fatalf("arrival rows = %d, want %d", len(a.arrival), len(wantArrival))
	}
	for i, want := range wantArrival {
		if a.arrival[i].fixPair != want {
			t.Errorf("arrival[%d] = %v, want %v", i, a.arrival[i].fixPair, want)
		}
	}
	if !a.arrival[0].rnav || a.arrival[1].rnav {
		t.Errorf("rnav flags wrong: %v %v", a.arrival[0].rnav, a.arrival[1].rnav)
	}
	if len(a.departure) != 2 || a.departure[0].fixPair != [2]string{"JFK", "*"} || a.departure[1].fixPair != [2]string{"ANY", "*"} {
		t.Errorf("departure rows = %+v", a.departure)
	}
	if len(a.overflight) != 2 {
		t.Errorf("overflight rows = %+v", a.overflight)
	}

	// A single configuration plan keys the TCPs by "*" with no plans map.
	if a.plans != nil {
		t.Errorf("single-plan facility should have no plans map, got %v", a.plans)
	}
	if tcp := a.arrival[0].tcp["*"]; tcp != "4P" {
		t.Errorf(`arrival[0] tcp["*"] = %q, want 4P`, tcp)
	}

	// Reassignments are ordered by seq_id and joined with the continuation
	// table's columns by row; the AHO row is skipped with a warning.
	rows := a.reassignments
	if len(rows) != 2 {
		t.Fatalf("reassignment rows = %d, want 2", len(rows))
	}
	if !hasWarning(warnings, "non-TCP") {
		t.Errorf("expected a warning about the skipped AHO condition, got %v", warnings)
	}
	if rows[0].seq != 0 || rows[0].acClass != "J" || rows[0].acid != "Nd*****" ||
		rows[0].activeRunway != "22L" || rows[0].dfix1 != "AR1" || rows[0].dfix2 != "*" {
		t.Errorf("reassignment[0] = %+v", rows[0])
	}
	if rows[1].seq != 1 || rows[1].dfix1 != "AR2" || rows[1].activeRunway != "*" {
		t.Errorf("reassignment[1] = %+v", rows[1])
	}

	// The dropped pair and the referenced aircraft type class are warned about.
	if !hasWarning(warnings, "1004") || !hasWarning(warnings, "tcp_assignment_classes") {
		t.Errorf("expected warnings about dropped pair 1004 and type class J, got %v", warnings)
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

const testConfig = `{
  "name": "Test",
  "control_positions": {
    "2J": {"radio_name": "x", "frequency": 120000000},
    "4P": {"radio_name": "x", "frequency": 120100000},
    "4H": {"radio_name": "x", "frequency": 120200000},
    "1D": {"radio_name": "x", "frequency": 120300000}
  },
  "facility_adaptations": {
    "video_map_file": "foo.zst",
    "significant_points": {
      "ARD": {"location": "N040.00.00.000,W074.00.00.000"}
    }
  }
}`

func TestFilterTCPs(t *testing.T) {
	a, _ := testAdaptation(t)
	var cfg struct {
		ControlPositions map[string]any `json:"control_positions"`
	}
	if err := json.Unmarshal([]byte(testConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	positions := make(map[sim.TCP]*av.Controller)
	for tcp := range cfg.ControlPositions {
		positions[sim.TCP(tcp)] = &av.Controller{}
	}
	// 7S (the ZZZ overflight row and the ANY catch-all's only TCP for the
	// overflight bucket is 1D, so only the 7S row goes) is not a position.
	warnings := filterTCPs(a, positions)
	if !hasWarning(warnings, "7S") {
		t.Errorf("expected a warning about 7S, got %v", warnings)
	}
	for _, e := range a.overflight {
		if e.fixPair == [2]string{"ZZZ", "*"} {
			t.Errorf("ZZZ overflight row with unknown TCP 7S should have been dropped")
		}
	}
	if len(a.arrival) != 3 || len(a.departure) != 2 {
		t.Errorf("rows with known TCPs were disturbed: %d arrival, %d departure", len(a.arrival), len(a.departure))
	}
}

// testSigDump adapts ARONE (short name AR1) near the facility and a same-named
// point far away that the range check must reject for AR2.
const testSigDump = `
******* FIX *******
----------------------------------------------------------------------------------------------------------------------------
|#.    |Name    |Short Name |Description                    |Type |SIM Only |Display on SIM |Latitude |N/S |Longitude |E/W |
----------------------------------------------------------------------------------------------------------------------------
|1     |ARONE   |AR1        |ARD ONE LOW                    |OT   |N        |N              |400000   |N   |0740000   |W   |
|2     |ARTWO   |AR2        |ARD TWO HIGH                   |OT   |N        |N              |500000   |N   |1200000   |W   |
----------------------------------------------------------------------------------------------------------------------------
`

func TestResolveFixes(t *testing.T) {
	dump, err := parseSigPoints(parseReport(testSigDump))
	if err != nil {
		t.Fatal(err)
	}
	if len(dump) != 2 {
		t.Fatalf("parsed %d dump points, want 2", len(dump))
	}
	if got := dump[0].location.DMSString(); got != "N040.00.00.000,W074.00.00.000" {
		t.Errorf("ARONE location = %q", got)
	}

	fa := &sim.FacilityAdaptation{
		SignificantPoints: map[string]sim.SignificantPoint{"ARD": {}},
	}
	center, _ := math.ParseLatLong([]byte("N040.00.00.000,W074.00.00.000"))
	// ARD is already adapted; AR1 comes from the dump; AR2's dump point is out
	// of range; ZZZ resolves nowhere (av.DB is uninitialized in tests).
	sigs, airports, warnings := resolveFixes([]string{"AR1", "AR2", "ARD", "ZZZ"}, fa, dump, center, true, 200)
	if len(sigs) != 1 || !strings.Contains(sigs[0], `"ARONE"`) || !strings.Contains(sigs[0], `"short_name": "AR1"`) {
		t.Errorf("significant point entries = %v", sigs)
	}
	if len(airports) != 0 {
		t.Errorf("unexpected airport entries: %v", airports)
	}
	if !hasWarning(warnings, "AR2") || !hasWarning(warnings, "ZZZ") {
		t.Errorf("expected AR2 out-of-range and ZZZ underivable warnings, got %v", warnings)
	}
}

func TestInsertIntoAdaptationMember(t *testing.T) {
	// Merging into an existing member keeps its entries; a missing member is
	// created; repeating the merge with no new entries never happens (the
	// caller skips), so idempotency here is about validity.
	updated, err := insertIntoAdaptationMember([]byte(testConfig), "significant_points",
		[]string{`"ARONE": {"short_name": "AR1", "location": "N040.00.00.000,W074.00.00.000"}`})
	if err != nil {
		t.Fatal(err)
	}
	updated, err = insertIntoAdaptationMember(updated, "airports",
		[]string{`"EWR": {"location": "N040.41.33.000,W074.10.07.000"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(updated) {
		t.Fatalf("result is not valid JSON:\n%s", updated)
	}
	var cfg struct {
		FA struct {
			SignificantPoints map[string]json.RawMessage `json:"significant_points"`
			Airports          map[string]json.RawMessage `json:"airports"`
		} `json:"facility_adaptations"`
	}
	if err := json.Unmarshal(updated, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.FA.SignificantPoints) != 2 {
		t.Errorf("significant points = %v, want ARD + ARONE", cfg.FA.SignificantPoints)
	}
	if len(cfg.FA.Airports) != 1 {
		t.Errorf("airports = %v, want EWR", cfg.FA.Airports)
	}
}

func TestSpliceConfig(t *testing.T) {
	a, _ := testAdaptation(t)

	updated, _, err := spliceConfig([]byte(testConfig), a)
	if err != nil {
		t.Fatalf("spliceConfig: %v", err)
	}
	if !json.Valid(updated) {
		t.Fatalf("spliced config is not valid JSON:\n%s", updated)
	}
	var cfg struct {
		Name string `json:"name"`
		FA   struct {
			VideoMapFile string          `json:"video_map_file"`
			FixPairs     json.RawMessage `json:"fix_pair_configuration"`
		} `json:"facility_adaptations"`
	}
	if err := json.Unmarshal(updated, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Name != "Test" || cfg.FA.VideoMapFile != "foo.zst" {
		t.Errorf("existing config members were disturbed: %+v", cfg)
	}
	var fpc struct {
		Assignments map[string]json.RawMessage `json:"fix_pair_assignments"`
	}
	if err := json.Unmarshal(cfg.FA.FixPairs, &fpc); err != nil {
		t.Fatalf("fix_pair_configuration: %v", err)
	}
	if len(fpc.Assignments) != 3 {
		t.Errorf("expected 3 assignment buckets, got %v", fpc.Assignments)
	}

	// Re-splicing replaces the existing block and is idempotent.
	updated2, _, err := spliceConfig(updated, a)
	if err != nil {
		t.Fatalf("re-splice: %v", err)
	}
	if string(updated) != string(updated2) {
		t.Errorf("re-splicing is not idempotent:\n%s\nvs\n%s", updated, updated2)
	}
}
