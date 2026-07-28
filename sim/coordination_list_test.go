// sim/coordination_list_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// validateCoordinationLists runs the STARS adaptation validation over a set of
// coordination lists and returns the accumulated error text.
func validateCoordinationLists(lists []CoordinationList) string {
	fc := &FacilityConfig{ControlPositions: map[TCP]*av.Controller{"1M": {}, "1L": {}}}
	fc.FacilityAdaptation.Lists.Coordination = lists
	var e util.ErrorLogger
	fc.validateSTARSAdaptation(&e)
	return e.String()
}
func TestCoordinationListOwnerTCP(t *testing.T) {
	// Two owner-scoped lists for one airport: valid split.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "M", Id: "BM", Airports: []string{"KBOS"}, OwnerTCP: "1M"},
		{Name: "L", Id: "BL", Airports: []string{"KBOS"}, OwnerTCP: "1L"},
	}); strings.Contains(out, "would appear in both") || strings.Contains(out, "multiple \"lists.coordination\" entries for") {
		t.Errorf("distinct owner-scoped lists should be valid, got:\n%s", out)
	}
	// Catch-all + owner-scoped for the same airport: valid (catch-all = remainder).
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "all", Id: "BA", Airports: []string{"KBOS"}},
		{Name: "M", Id: "BM", Airports: []string{"KBOS"}, OwnerTCP: "1M"},
	}); strings.Contains(out, "multiple") {
		t.Errorf("catch-all + owner-scoped should be valid (remainder), got:\n%s", out)
	}
	// Two catch-all lists for the same airport: error.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "a1", Id: "A1", Airports: []string{"KBOS"}},
		{Name: "a2", Id: "A2", Airports: []string{"KBOS"}},
	}); !strings.Contains(out, "multiple catch-all") {
		t.Errorf("two catch-all lists should error, got:\n%s", out)
	}
	// Two lists with the same (airport, owner_tcp): overlap error.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "M1", Id: "B1", Airports: []string{"KBOS"}, OwnerTCP: "1M"},
		{Name: "M2", Id: "B2", Airports: []string{"KBOS"}, OwnerTCP: "1M"},
	}); !strings.Contains(out, `multiple "lists.coordination" entries for "owner_tcp"`) {
		t.Errorf("duplicate (airport, owner_tcp) should error, got:\n%s", out)
	}
	// owner_tcp not a known control position.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "X", Id: "BX", Airports: []string{"KBOS"}, OwnerTCP: "9Z"},
	}); !strings.Contains(out, `"owner_tcp" "9Z" is not in "control_positions"`) {
		t.Errorf("unknown owner_tcp should error, got:\n%s", out)
	}
}
