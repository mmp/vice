// pkg/rand/rand_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package rand

import (
	"math/rand/v2"
	"testing"
)

func TestPermutationElement(t *testing.T) {
	for _, n := range []int{8, 31, 10523} {
		for _, h := range []uint32{0, 0xff, 0xfeedface} {
			m := make(map[int]int)

			for i := 0; i < n; i++ {
				perm := PermutationElement(i, n, h)
				if _, ok := m[perm]; ok {
					t.Errorf("%d: appeared multiple times", perm)
				}
				m[perm] = i
			}
		}
	}
}

func TestRandomPermute(t *testing.T) {
	for _, n := range []int{0, 1, 5, 11, 42} {
		s := make([]int, n)
		for i := range n {
			s[i] = i
		}
		got := make([]bool, n)

		seed := rand.Uint32()
		for i, v := range PermuteSlice(s, seed) {
			if i != v {
				t.Errorf("mismatch index/value: %d/%d slice %+v", i, v, s)
			}
			if got[i] {
				t.Errorf("got %d repeatedly, slice %+v", i, s)
			}
			got[i] = true
		}
		for i, g := range got {
			if !g {
				t.Errorf("never got index %d", i)
			}
		}
	}
}

func TestSampleFiltered(t *testing.T) {
	r := Make()
	if SampleFiltered(r, []int{}, func(int) bool { return true }) != -1 {
		t.Errorf("Returned non-zero for empty slice")
	}
	if SampleFiltered(r, []int{0, 1, 2, 3, 4}, func(int) bool { return false }) != -1 {
		t.Errorf("Returned non-zero for fully filtered")
	}
	if idx := SampleFiltered(r, []int{0, 1, 2, 3, 4}, func(v int) bool { return v == 3 }); idx != 3 {
		t.Errorf("Returned %d rather than 3 for filtered slice", idx)
	}

	var counts [5]int
	for i := 0; i < 9000; i++ {
		idx := SampleFiltered(r, []int{0, 1, 2, 3, 4}, func(v int) bool { return v&1 == 0 })
		counts[idx]++
	}
	if counts[1] != 0 || counts[3] != 0 {
		t.Errorf("Incorrectly sampled odd items. Counts: %+v", counts)
	}

	slop := 100
	if counts[0] < 3000-slop || counts[0] > 3000+slop ||
		counts[2] < 3000-slop || counts[2] > 3000+slop ||
		counts[4] < 3000-slop || counts[4] > 3000+slop {
		t.Errorf("Didn't find roughly 3000 samples for the even items. Counts: %+v", counts)
	}
}

func TestSampleWeighted(t *testing.T) {
	a := []int{1, 2, 3, 4, 5, 0, 10, 13}
	counts := make(map[int]int)

	n := 100000
	r := Make()
	for i := 0; i < n; i++ {
		v, ok := SampleWeighted(r, a, func(v int) int { return v })
		if !ok {
			t.Errorf("Unexpected failure of SampleWeighted")
		} else {
			counts[v]++
		}
	}

	sum := 0
	for _, v := range a {
		sum += v
	}

	for _, v := range a {
		expected := v * n / sum
		c := counts[v]
		if v == 0 && c != 0 {
			t.Errorf("Expected 0 samples for 0. Got %d", c)
		} else if c < expected-300 || c > expected+300 {
			t.Errorf("Expected roughly %d samples for %d. Got %d [%v]", expected, v, c, counts)
		}
	}
}
