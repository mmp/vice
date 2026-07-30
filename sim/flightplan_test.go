// sim/flightplan_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"io"
	"log/slog"
	"testing"

	"github.com/mmp/vice/log"
)

func TestNASFlightPlanUpdateClearsDerivedFix(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := NewTestSim(lg)

	fp := &NASFlightPlan{EntryFix: "PVA", ExitFix: "BOS", DerivedEntryFix: "ROB", DerivedExitFix: "LGA"}

	// Re-affirming the same fixes leaves the derived substitutions in place.
	var spec FlightPlanSpecifier
	spec.EntryFix.Set("PVA")
	spec.ExitFix.Set("BOS")
	if err := fp.Update(spec, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedEntryFix != "ROB" || fp.DerivedExitFix != "LGA" {
		t.Errorf("unchanged fixes should not clear derived fixes: got entry=%q exit=%q", fp.DerivedEntryFix, fp.DerivedExitFix)
	}

	// Changing the entry fix clears only the derived entry fix.
	var spec2 FlightPlanSpecifier
	spec2.EntryFix.Set("XYZ")
	if err := fp.Update(spec2, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedEntryFix != "" {
		t.Errorf("DerivedEntryFix = %q, want cleared after entry fix change", fp.DerivedEntryFix)
	}
	if fp.DerivedExitFix != "LGA" {
		t.Errorf("DerivedExitFix = %q, want unchanged", fp.DerivedExitFix)
	}

	// Changing the exit fix clears the derived exit fix.
	var spec3 FlightPlanSpecifier
	spec3.ExitFix.Set("PVD")
	if err := fp.Update(spec3, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedExitFix != "" {
		t.Errorf("DerivedExitFix = %q, want cleared after exit fix change", fp.DerivedExitFix)
	}
}
