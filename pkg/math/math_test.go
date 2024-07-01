// math_test.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"testing"

	"github.com/mmp/vice/pkg/rand"
)

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
			result := PointInPolygon2LL(tc.point, tc.polygon)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v for point %v and polygon %v",
					tc.expected, result, tc.point, tc.polygon)
			}
		})
	}
}

func TestPointSegmentDistance(t *testing.T) {
	refSampled := func(p, v, w [2]float32) float32 {
		const n = 16384
		dmin := float32(1e30)
		for i := 0; i < n; i++ {
			t := float32(i) / float32(n-1)
			pp := Lerp2f(t, v, w)
			dmin = Min(dmin, Distance2f(pp, p))
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
		if Abs(d-ref) > .001 {
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
		if Abs(d-ref) > .001 {
			t.Errorf("p %v v %v w %v expected %f got %f", p, v, w, ref, d)
		}
	}
}

func TestGCD(t *testing.T) {
	type Test struct {
		a, b   int
		result int
	}
	for _, test := range []Test{
		Test{a: 9, b: 3, result: 3},
		Test{a: 36, b: 2, result: 2},
		Test{a: 18, b: 36, result: 18},
		Test{a: 2, b: 8, result: 2},
		Test{a: 13, b: 31, result: 1},
	} {
		g := GCD(test.a, test.b)
		if g != test.result {
			t.Errorf("incorrect gcd for (%d,%d): wanted %d, got %d", test.a, test.b, test.result, g)
		}
		if test.a%test.result != 0 || test.b%test.result != 0 {
			t.Errorf("bogus test case for (%d,%d): expected result %d is not a valid divisor",
				test.a, test.b, test.result)
		}
	}
}

func TestLCM(t *testing.T) {
	type Test struct {
		a, b   int
		result int
	}
	for _, test := range []Test{
		Test{a: 9, b: 3, result: 9},
		Test{a: 3, b: 2, result: 6},
		Test{a: 5, b: 7, result: 35},
		Test{a: 1, b: 11, result: 11},
		Test{a: 12, b: 12, result: 12},
	} {
		g := LCM(test.a, test.b)
		if g != test.result {
			t.Errorf("incorrect lcm for (%d,%d): wanted %d, got %d", test.a, test.b, test.result, g)
		}
		if test.result%test.a != 0 || test.result%test.b != 0 {
			t.Errorf("bogus test case for (%d,%d): expected result %d is not a valid multiple",
				test.a, test.b, test.result)
		}
	}
}
