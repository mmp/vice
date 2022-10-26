// util_test.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
	"time"
)

func TestWrapText(t *testing.T) {
	input := "this is a test_with_a_long_line of stuff"
	expected := "this is \n  a \n  test_with_a_long_line \n  of \n  stuff"
	wrap, lines := wrapText(input, 8, 2, false)
	if wrap != expected {
		t.Errorf("wrapping gave %q; expected %q", wrap, expected)
	}
	if lines != 5 {
		t.Errorf("wrapping returned %d lines, expected 5", lines)
	}
}

func TestStopShouting(t *testing.T) {
	input := "UNITED AIRLINES (North America)"
	expected := "United Airlines (North America)"
	ss := stopShouting(input)
	if ss != expected {
		t.Errorf("Got %q, expected %q", ss, expected)
	}
}

func TestArgmin(t *testing.T) {
	if argmin(1) != 0 {
		t.Errorf("argmin single failed: %d", argmin(1))
	}
	if argmin(1, 2) != 0 {
		t.Errorf("argmin 1,2 failed: %d", argmin(1, 2))
	}
	if argmin(2, 1) != 1 {
		t.Errorf("argmin 2,1 failed: %d", argmin(2, 1))
	}
	if argmin(1, -3, 1, 10) != 1 {
		t.Errorf("argmin 1,-3,1,10 failed: %d", argmin(1, -3, 1, 10))
	}
}

func TestCompass(t *testing.T) {
	type ch struct {
		h     float32
		dir   string
		short string
		hour  int
	}

	for _, c := range []ch{ch{0, "North", "N", 12}, ch{22, "North", "N", 1}, ch{338, "North", "N", 11},
		ch{337, "Northwest", "NW", 11}, ch{95, "East", "E", 3}, ch{47, "Northeast", "NE", 2},
		ch{140, "Southeast", "SE", 5}, ch{170, "South", "S", 6}, ch{205, "Southwest", "SW", 7},
		ch{260, "West", "W", 9}} {
		if compass(c.h) != c.dir {
			t.Errorf("compass gave %s for %f; expected %s", compass(c.h), c.h, c.dir)
		}
		if shortCompass(c.h) != c.short {
			t.Errorf("shortCompass gave %s for %f; expected %s", shortCompass(c.h), c.h, c.short)
		}
		if headingAsHour(c.h) != c.hour {
			t.Errorf("headingAsHour gave %d for %f; expected %d", headingAsHour(c.h), c.h, c.hour)
		}
	}
}

func TestHeadingDifference(t *testing.T) {
	type hd struct {
		a, b, d float32
	}

	for _, h := range []hd{hd{10, 90, 80}, hd{350, 12, 22}, hd{340, 120, 140}, hd{-90, 80, 170},
		hd{40, 181, 141}, hd{-170, 160, 30}, hd{-120, -150, 30}} {
		if headingDifference(h.a, h.b) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.a, h.b,
				headingDifference(h.a, h.b), h.d)
		}
		if headingDifference(h.b, h.a) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.b, h.a,
				headingDifference(h.b, h.a), h.d)
		}
	}
}

func TestTransientMap(t *testing.T) {
	ts := NewTransientMap[int, int]()
	ts.Add(1, 10, 250*time.Millisecond)
	ts.Add(2, 20, 750*time.Millisecond)

	// Should have both
	if v, ok := ts.Get(1); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 10 {
		t.Errorf("transient set didn't return expected value")
	}
	if v, ok := ts.Get(2); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 20 {
		t.Errorf("transient set didn't return expected value")
	}

	// Note that after this point this test has the potential to be flaky,
	// if the thread is not scheduled for ~250+ms; it's possible that more
	// time will elapse than we think and thence some of the checks may not
	// add up...
	time.Sleep(500 * time.Millisecond)

	// Should just have 2
	if _, ok := ts.Get(1); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
	if v, ok := ts.Get(2); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 20 {
		t.Errorf("transient set didn't return expected value")
	}

	time.Sleep(250 * time.Millisecond)

	if _, ok := ts.Get(1); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
	if _, ok := ts.Get(2); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
}

func TestFilterSlice(t *testing.T) {
	a := []int{1, 2, 3, 4, 5}
	b := FilterSlice(a, func(i int) bool { return i%2 == 0 })
	if len(b) != 2 || b[0] != 2 || b[1] != 4 {
		t.Errorf("filter evens failed: %+v", b)
	}

	c := FilterSlice(a, func(i int) bool { return i >= 3 })
	if len(c) != 3 || c[0] != 3 || c[1] != 4 || c[2] != 5 {
		t.Errorf("filter >=3 failed: %+v", c)
	}
}
