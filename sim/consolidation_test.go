// sim/consolidation_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

func TestPositionConsolidationValidate(t *testing.T) {
	cp := map[TCP]*av.Controller{"1R": {}, "1M": {}, "1L": {}, "1D": {}}
	countErrors := func(pc PositionConsolidation) int {
		var e util.ErrorLogger
		pc.Validate(cp, &e)
		n := 0
		for range e.Errors() {
			n++
		}
		return n
	}
	// Valid single-rooted tree.
	if n := countErrors(PositionConsolidation{"1R": {"1M", "1L"}, "1D": {"1R"}}); n != 0 {
		t.Errorf("valid consolidation reported %d errors", n)
	}
	// Unknown child position.
	if n := countErrors(PositionConsolidation{"1R": {"9Z"}}); n == 0 {
		t.Errorf("expected error for unknown child position")
	}
	// Cycle: 1R -> 1M -> 1R.
	if n := countErrors(PositionConsolidation{"1R": {"1M"}, "1M": {"1R"}}); n == 0 {
		t.Errorf("expected error for cycle")
	}
	// A position that is a child of two parents.
	if n := countErrors(PositionConsolidation{"1R": {"1M"}, "1D": {"1M", "1R"}}); n == 0 {
		t.Errorf("expected error for multi-parent child")
	}
}
