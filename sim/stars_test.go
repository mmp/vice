// sim/stars_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"testing"
)

// TestAirspaceAwarenessController verifies how a rule is matched against a
// flight plan: a rule names a fix in full while the flight plan carries the
// fix's 3-character id, so the two are compared after shortening the rule's
// name. Altitude and aircraft type gate the match, and a later rule is
// considered when an earlier one is only a partial match.
func TestAirspaceAwarenessController(t *testing.T) {
	fa := FacilityAdaptation{
		SignificantPoints: map[string]SignificantPoint{"BOSOX": {ShortName: "BOX"}, "ROBUCC": {}},
		AirspaceAwareness: []AirspaceAwareness{
			{Fix: []string{"BOSOX"}, AltitudeRange: [2]int{11000, 99000}, ReceivingController: "C18"},
			{Fix: []string{"ROBUCC"}, AircraftType: []string{"J"}, ReceivingController: "C37"},
			{Fix: []string{"ALL"}, ReceivingController: "1Z"},
		},
	}

	for _, tc := range []struct {
		name string
		fp   NASFlightPlan
		want string
	}{
		{"adapted fix matches by its short name",
			NASFlightPlan{ExitFix: "BOX", RequestedAltitude: 20000, AircraftType: "B738"}, "C18"},
		{"a plan carrying the rule's full name does not match it",
			NASFlightPlan{ExitFix: "BOSOX", RequestedAltitude: 20000, AircraftType: "B738"}, "1Z"},
		{"altitude below the range falls through",
			NASFlightPlan{ExitFix: "BOX", RequestedAltitude: 8000, AircraftType: "B738"}, "1Z"},
		{"short name defaults to the first three characters",
			NASFlightPlan{ExitFix: "ROB", AircraftType: "B738"}, "C37"},
		{"aircraft type gates the match",
			NASFlightPlan{ExitFix: "ROB", AircraftType: "C172"}, "1Z"},
		{"an unadapted fix takes the wildcard",
			NASFlightPlan{ExitFix: "PVD", RequestedAltitude: 20000, AircraftType: "B738"}, "1Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tcp, ok := fa.AirspaceAwarenessController("", &tc.fp); !ok || tcp != tc.want {
				t.Errorf("got %q ok=%v, want %q/true", tcp, ok, tc.want)
			}
		})
	}

	// With no wildcard rule adapted, a flight nothing names goes unmatched and
	// the C handoff is rejected rather than sent somewhere arbitrary.
	noWildcard := FacilityAdaptation{
		SignificantPoints: fa.SignificantPoints,
		AirspaceAwareness: fa.AirspaceAwareness[:1],
	}
	fp := NASFlightPlan{ExitFix: "PVD", RequestedAltitude: 20000, AircraftType: "B738"}
	if tcp, ok := noWildcard.AirspaceAwarenessController("", &fp); ok {
		t.Errorf("unmatched fix returned %q, want no match", tcp)
	}
}
