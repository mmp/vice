// sim/auto_scratchpad_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"io"
	"log/slog"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
)

func TestAutoScratchpadMatches(t *testing.T) {
	// Flight-plan constructor: fp(entry, exit, type, requested, assigned).
	fp := func(entry, exit string, ft av.TypeOfFlight, req, asg int) *NASFlightPlan {
		return &NASFlightPlan{EntryFix: entry, ExitFix: exit, TypeOfFlight: ft,
			RequestedAltitude: req * 100, AssignedAltitude: asg * 100}
	}
	vfrFp := fp("X", "Y", av.FlightTypeDeparture, 0, 0)
	vfrFp.Rules = av.FlightRulesVFR
	derivedFp := fp("PVA", "BOS", av.FlightTypeArrival, 0, 0)
	derivedFp.DerivedEntryFix = "ROB"
	// Base: plan CO1, entry LEENA, exit *, departure, altitude 230 RA.
	base := AutoScratchpadRow{ConfigPlan: "CO1", EntryFix: "LEENA", ExitFix: "*",
		FlightType: "P", Altitude: "230", AltitudeType: "RA"}
	anyRow := func(ft string) AutoScratchpadRow {
		return AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: ft, Altitude: "*"}
	}
	for _, tc := range []struct {
		name string
		r    AutoScratchpadRow
		plan string
		fp   *NASFlightPlan
		want bool
	}{
		{"exact RA departure at/below", base, "CO1", fp("LEENA", "SEEDS", av.FlightTypeDeparture, 200, 0), true},
		{"RA departure above ceiling", base, "CO1", fp("LEENA", "SEEDS", av.FlightTypeDeparture, 240, 0), false},
		{"wrong plan", base, "CO2", fp("LEENA", "SEEDS", av.FlightTypeDeparture, 200, 0), false},
		{"wrong entry", base, "CO1", fp("OTHER", "SEEDS", av.FlightTypeDeparture, 200, 0), false},
		{"wrong flight type", base, "CO1", fp("LEENA", "SEEDS", av.FlightTypeArrival, 200, 0), false},
		{"plan wildcard", anyRow("P"), "CO9", fp("X", "Y", av.FlightTypeDeparture, 100, 0), true},
		{"entry blank matches unassigned",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "", ExitFix: "*", FlightType: "A", Altitude: "*"},
			"CO1", fp("", "Y", av.FlightTypeArrival, 0, 0), true},
		{"entry blank vs assigned entry",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "", ExitFix: "*", FlightType: "A", Altitude: "*"},
			"CO1", fp("LEENA", "Y", av.FlightTypeArrival, 0, 0), false},
		{"entry star matches unassigned", anyRow("A"), "CO1", fp("", "Y", av.FlightTypeArrival, 0, 0), true},
		{"altitude R equals",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "P", Altitude: "150", AltitudeType: "R"},
			"CO1", fp("X", "Y", av.FlightTypeDeparture, 150, 0), true},
		{"altitude R not equal",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "P", Altitude: "150", AltitudeType: "R"},
			"CO1", fp("X", "Y", av.FlightTypeDeparture, 160, 0), false},
		{"RA arrival uses assigned",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "A", Altitude: "100", AltitudeType: "RA"},
			"CO1", fp("X", "Y", av.FlightTypeArrival, 300, 90), true},
		{"VFR altitude matches VFR flight",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "P", Altitude: "VFR"},
			"CO1", vfrFp, true},
		{"VFR altitude rejects IFR flight",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "P", Altitude: "VFR"},
			"CO1", fp("X", "Y", av.FlightTypeDeparture, 0, 0), false},
		// Derived fixes: a criterion naming either the derived or the actual
		// fix matches; blank means no fix at all, so a derived fix disqualifies.
		{"criterion on derived entry",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "ROB", ExitFix: "*", FlightType: "A", Altitude: "*"},
			"CO1", derivedFp, true},
		{"criterion on actual entry with derived set",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "PVA", ExitFix: "*", FlightType: "A", Altitude: "*"},
			"CO1", derivedFp, true},
		{"blank entry vs derived fix",
			AutoScratchpadRow{ConfigPlan: "*", EntryFix: "", ExitFix: "*", FlightType: "A", Altitude: "*"},
			"CO1", derivedFp, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.matches(tc.plan, tc.fp); got != tc.want {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}
func TestSortAutoScratchpad(t *testing.T) {
	// Deliberately unsorted: wildcards and higher altitudes first.
	rows := []AutoScratchpadRow{
		{ConfigPlan: "*", Altitude: "*", Scratchpad1: "wild"},
		{ConfigPlan: "CO1", EntryFix: "LEENA", Altitude: "200", AltitudeType: "RA", Scratchpad1: "ra200"},
		{ConfigPlan: "CO1", EntryFix: "LEENA", Altitude: "150", AltitudeType: "RA", Scratchpad1: "ra150"},
		{ConfigPlan: "CO1", EntryFix: "LEENA", Altitude: "150", AltitudeType: "R", Scratchpad1: "r150"},
		{ConfigPlan: "CO1", EntryFix: "*", Altitude: "*", Scratchpad1: "co1wild"},
	}
	sortAutoScratchpad(rows)
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Scratchpad1
	}
	// specific plan+entry, R before RA, lower altitude first; then entry "*",
	// then plan "*".
	want := []string{"r150", "ra150", "ra200", "co1wild", "wild"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
func TestSortAutoScratchpadEmptyConfigPlan(t *testing.T) {
	// wildcardMatch treats an empty ConfigPlan the same as "*" (unlike
	// EntryFix/ExitFix, where "" is the narrower "unassigned only" case), so
	// it must sort as a wildcard too, behind the plan-specific row.
	rows := []AutoScratchpadRow{
		{ConfigPlan: "", Altitude: "*", Scratchpad1: "empty"},
		{ConfigPlan: "CO1", Altitude: "*", Scratchpad1: "specific"},
	}
	sortAutoScratchpad(rows)
	got := []string{rows[0].Scratchpad1, rows[1].Scratchpad1}
	want2 := []string{"specific", "empty"}
	if got[0] != want2[0] || got[1] != want2[1] {
		t.Errorf("order = %v, want %v", got, want2)
	}
}
func TestApplyAutoScratchpadAssignment(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := NewTestSim(lg)
	s.State.ConfigurationId = "CO1"
	rows := []AutoScratchpadRow{
		{ConfigPlan: "CO1", EntryFix: "LEENA", ExitFix: "*", FlightType: "P", Altitude: "150", AltitudeType: "R", Scratchpad1: "AAA", Scratchpad2: "^"},
		{ConfigPlan: "*", EntryFix: "*", ExitFix: "*", FlightType: "P", Altitude: "*", Scratchpad1: "ZZZ"},
	}
	sortAutoScratchpad(rows)
	s.State.FacilityAdaptation.AutoScratchpadAssignment = rows
	// Matches the specific rule: scratchpad1 = AAA, scratchpad2 = delta.
	fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, EntryFix: "LEENA",
		RequestedAltitude: 15000}
	s.applyAutoScratchpadAssignment(fp)
	if fp.Scratchpad != "AAA" {
		t.Errorf("scratchpad1 = %q, want AAA", fp.Scratchpad)
	}
	if fp.SecondaryScratchpad != asaDeltaChar {
		t.Errorf("scratchpad2 = %q, want delta", fp.SecondaryScratchpad)
	}
	// An explicitly-set scratchpad is not overridden; the other still fills.
	fp2 := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, EntryFix: "LEENA",
		RequestedAltitude: 15000, Scratchpad: "USR"}
	s.applyAutoScratchpadAssignment(fp2)
	if fp2.Scratchpad != "USR" {
		t.Errorf("explicit scratchpad overridden: %q", fp2.Scratchpad)
	}
	if fp2.SecondaryScratchpad != asaDeltaChar {
		t.Errorf("scratchpad2 = %q, want delta", fp2.SecondaryScratchpad)
	}
	// No specific match -> falls through to the wildcard rule.
	fp3 := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, EntryFix: "OTHER",
		RequestedAltitude: 9000}
	s.applyAutoScratchpadAssignment(fp3)
	if fp3.Scratchpad != "ZZZ" {
		t.Errorf("wildcard scratchpad = %q, want ZZZ", fp3.Scratchpad)
	}
}
