// util/util_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"testing"
)

func TestObjectArena(t *testing.T) {
	var a ObjectArena[int]

	for range 10 {
		seen := make(map[*int]any)
		for i := range 100 {
			p := a.AllocClear()
			if _, ok := seen[p]; ok {
				t.Errorf("%p: pointer returned twice!", p)
			}
			seen[p] = nil

			if *p != 0 {
				t.Errorf("%p = %d, expected 0", p, *p)
			}
			*p = i
		}

		if a.Cap() > 200 {
			t.Errorf("Capacity growing too fast: now %d", a.Cap())
		}

		a.Reset()
	}
}
