// math_test.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"
)

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

func TestParseLatLong(t *testing.T) {
	type LL struct {
		str string
		pos Point2LL
	}
	latlongs := []LL{
		LL{str: "N40.37.58.400, W073.46.17.000", pos: Point2LL{-73.771385, 40.6328888}}, // JFK VOR
		LL{str: "N40.37.58.4,W073.46.17.000", pos: Point2LL{-73.771385, 40.6328888}},    // JFK VOR
		LL{str: "40.6328888, -73.771385", pos: Point2LL{-73.771385, 40.6328888}},        // JFK VOR
		LL{str: "+403758.400-0734617.000", pos: Point2LL{-73.7713928, 40.632885}}}       // JFK VOR

	for _, ll := range latlongs {
		p, err := ParseLatLong([]byte(ll.str))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", ll.str, err)
		}
		if p[0] != ll.pos[0] {
			t.Errorf("%s: got %.9g for latitude, expected %.9g", ll.str, p[0], ll.pos[0])
		}
		if p[1] != ll.pos[1] {
			t.Errorf("%s: got %.9g for longitude, expected %.9g", ll.str, p[1], ll.pos[1])
		}
	}

	for _, invalid := range []string{
		"E40.37.58.400, W073.46.17.000",
		"40.37.58.400, W073.46.17.000",
		"N40.37.58.400, -73.22",
		"N40.37.58.400, W073.46.17",
	} {
		if _, err := ParseLatLong([]byte(invalid)); err == nil {
			t.Errorf("%s: no error was returned for invalid latlong string!", invalid)
		}
	}
}

func TestSampleFiltered(t *testing.T) {
	if SampleFiltered([]int{}, func(int) bool { return true }) != -1 {
		t.Errorf("Returned non-zero for empty slice")
	}
	if SampleFiltered([]int{0, 1, 2, 3, 4}, func(int) bool { return false }) != -1 {
		t.Errorf("Returned non-zero for fully filtered")
	}
	if idx := SampleFiltered([]int{0, 1, 2, 3, 4}, func(v int) bool { return v == 3 }); idx != 3 {
		t.Errorf("Returned %d rather than 3 for filtered slice", idx)
	}

	var counts [5]int
	for i := 0; i < 9000; i++ {
		idx := SampleFiltered([]int{0, 1, 2, 3, 4}, func(v int) bool { return v&1 == 0 })
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
	counts := make([]int, len(a))

	n := 100000
	for i := 0; i < n; i++ {
		idx := SampleWeighted(a, func(v int) int { return v })
		counts[idx]++
	}

	sum := 0
	for _, v := range a {
		sum += v
	}

	for i, c := range counts {
		expected := a[i] * n / sum
		if a[0] == 0 && c != 0 {
			t.Errorf("Expected 0 samples for a[%d]. Got %d", i, c)
		} else if c < expected-300 || c > expected+300 {
			t.Errorf("Expected roughly %d samples for a[%d]=%d. Got %d", expected, i, a[i], c)
		}
	}
}

func TestPointInPolygon(t *testing.T) {
	type testCase struct {
		name     string
		point    Point2LL
		polygon  []Point2LL
		expected bool
	}

	testCases := []testCase{
		{
			name:     "PointInsideSimpleSquare",
			point:    Point2LL{1, 1},
			polygon:  []Point2LL{{0, 0}, {0, 2}, {2, 2}, {2, 0}},
			expected: true,
		},
		{
			name:     "PointToLeftOfQuad",
			point:    Point2LL{-.2, 0.2},
			polygon:  []Point2LL{{.01, 1}, {20, 2}, {20, -2}, {.01, -1}},
			expected: false,
		},
		{
			name:     "PointOutsideSimpleSquare",
			point:    Point2LL{3, 3},
			polygon:  []Point2LL{{0, 0}, {0, 2}, {2, 2}, {2, 0}},
			expected: false,
		},
		{
			name:     "PointByVertex",
			point:    Point2LL{-0.001, 0},
			polygon:  []Point2LL{{0, 0}, {0, 2}, {2, 2}, {2, 0}},
			expected: false,
		},
		{
			name:     "PointOnEdge",
			point:    Point2LL{1, -.001},
			polygon:  []Point2LL{{0, 0}, {0, 2}, {2, 2}, {2, 0}},
			expected: false,
		},
		{
			name:     "PointInsideComplexPolygon",
			point:    Point2LL{3, 3},
			polygon:  []Point2LL{{0, 0}, {0, 6}, {6, 6}, {6, 0}, {3, 3}},
			expected: true,
		},
		{
			name:     "PointOutsideComplexPolygon",
			point:    Point2LL{7, 7},
			polygon:  []Point2LL{{0, 0}, {0, 6}, {6, 6}, {6, 0}, {3, 3}},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := PointInPolygon(tc.point, tc.polygon)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v for point %v and polygon %v",
					tc.expected, result, tc.point, tc.polygon)
			}
		})
	}
}

func TestOppositeHeading(t *testing.T) {
	h := [][2]float32{{90, 270}, {1, 181}, {2, 182}, {350, 170}}
	for _, pair := range h {
		if OppositeHeading(pair[0]) != pair[1] {
			t.Errorf("opposite heading error: %f -> %f, expected %f",
				pair[0], OppositeHeading(pair[0]), pair[1])
		}
		if OppositeHeading(pair[1]) != pair[0] {
			t.Errorf("opposite heading error: %f -> %f, expected %f",
				pair[1], OppositeHeading(pair[1]), pair[0])
		}
	}
}

func TestNormalizeHeading(t *testing.T) {
	h := [][2]float32{{90, 90}, {360, 0}, {-10, 350}, {380, 20}, {-380, 340}}
	for _, pair := range h {
		if NormalizeHeading(pair[0]) != pair[1] {
			t.Errorf("normalize heading error: %f -> %f, expected %f",
				pair[0], NormalizeHeading(pair[0]), pair[1])
		}
	}
}

func TestPointSegmentDistance(t *testing.T) {
	refSampled := func(p, v, w [2]float32) float32 {
		const n = 16384
		dmin := float32(1e30)
		for i := 0; i < n; i++ {
			t := float32(i) / float32(n-1)
			pp := lerp2f(t, v, w)
			dmin = min(dmin, distance2f(pp, p))
		}
		return dmin
	}

	cases := []struct {
		p, v, w [2]float32
		dist    float32
	}{
		{p: [2]float32{1, 1}, v: [2]float32{0, 0}, w: [2]float32{2, 2}, dist: 0},
		{p: [2]float32{-2, -2}, v: [2]float32{-1, -1}, w: [2]float32{2, 2}, dist: 1.414214},
	}

	for _, c := range cases {
		ref := c.dist
		if ref < 0 {
			ref = refSampled(c.p, c.v, c.w)
		}

		d := PointSegmentDistance(c.p, c.v, c.w)
		if abs(d-ref) > .001 {
			t.Errorf("p %v v %v w %v expected %f got %f", c.p, c.v, c.w, ref, d)
		}
	}

	// Do some randoms
	for i := 0; i < 32; i++ {
		r := func() float32 { return -10 + 20*rand.Float32() }
		p := [2]float32{r(), r()}
		v := [2]float32{r(), r()}
		w := [2]float32{r(), r()}
		ref := refSampled(p, v, w)
		d := PointSegmentDistance(p, v, w)
		if abs(d-ref) > .001 {
			t.Errorf("p %v v %v w %v expected %f got %f", p, v, w, ref, d)
		}
	}
}
