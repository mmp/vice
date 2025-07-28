// pkg/math/math_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	gomath "math"
	"testing"

	"github.com/mmp/vice/pkg/rand"
)

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
		if Compass(c.h) != c.dir {
			t.Errorf("compass gave %s for %f; expected %s", Compass(c.h), c.h, c.dir)
		}
		if ShortCompass(c.h) != c.short {
			t.Errorf("shortCompass gave %s for %f; expected %s", ShortCompass(c.h), c.h, c.short)
		}
		if HeadingAsHour(c.h) != c.hour {
			t.Errorf("headingAsHour gave %d for %f; expected %d", HeadingAsHour(c.h), c.h, c.hour)
		}
	}
}

func TestHeadingDifference(t *testing.T) {
	type hd struct {
		a, b, d float32
	}

	for _, h := range []hd{hd{10, 90, 80}, hd{350, 12, 22}, hd{340, 120, 140}, hd{-90, 80, 170},
		hd{40, 181, 141}, hd{-170, 160, 30}, hd{-120, -150, 30}} {
		if HeadingDifference(h.a, h.b) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.a, h.b,
				HeadingDifference(h.a, h.b), h.d)
		}
		if HeadingDifference(h.b, h.a) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.b, h.a,
				HeadingDifference(h.b, h.a), h.d)
		}
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

func TestHeadingSignedTurn(t *testing.T) {
	turns := [][3]float32{{10, 90, 80}, {10, 350, -20}, {120, 10, -110}, {120, 270, 150}}
	for _, turn := range turns {
		if result := HeadingSignedTurn(turn[0], turn[1]); result != turn[2] {
			t.Errorf("HeadingSignedTurn(%f, %f) = %f; expected %f", turn[0], turn[1], result, turn[2])
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
		LL{str: "+403758.400-0734617.000", pos: Point2LL{-73.7713928, 40.632885}},       // JFK VOR
	}

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

	for _, ll := range []LL{
		LL{str: "4037N/07346W", pos: Point2LL{-73.76666667, 40.616667}},
		LL{str: "1234S/12016E", pos: Point2LL{120.2666667, -12.5666667}},
	} {
		p, err := ParseLatLong([]byte(ll.str))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", ll.str, err)
		}
		if Abs(p[0]-ll.pos[0]) > 1e-5 {
			t.Errorf("%s: got %.9g for latitude, expected %.9g", ll.str, p[0], ll.pos[0])
		}
		if Abs(p[1]-ll.pos[1]) > 1e-5 {
			t.Errorf("%s: got %.9g for longitude, expected %.9g", ll.str, p[1], ll.pos[1])
		}
	}

	for _, invalid := range []string{
		"E40.37.58.400, W073.46.17.000",
		"40.37.58.400, W073.46.17.000",
		"N40.37.58.400, -73.22",
		"N40.37.58.400, W073.46.17",
		"40632N/12345W",
		"632N/12345W",
		"4062N/12435W",
		"4062N/01245X",
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
			dmin = min(dmin, Distance2f(pp, p))
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
	r := rand.Make()
	for i := 0; i < 32; i++ {
		r := func() float32 { return -10 + 20*r.Float32() }
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

func TestSegmentSegmentIntersect(t *testing.T) {
	type Test struct {
		name     string
		p1, p2   [2]float32
		p3, p4   [2]float32
		expected bool
		point    [2]float32 // expected intersection point if intersects
	}

	tests := []Test{
		{
			name:     "intersecting segments",
			p1:       [2]float32{0, 0},
			p2:       [2]float32{4, 4},
			p3:       [2]float32{0, 4},
			p4:       [2]float32{4, 0},
			expected: true,
			point:    [2]float32{2, 2},
		},
		{
			name:     "non-intersecting segments",
			p1:       [2]float32{0, 0},
			p2:       [2]float32{1, 1},
			p3:       [2]float32{2, 2},
			p4:       [2]float32{3, 3},
			expected: false,
		},
		{
			name:     "parallel segments",
			p1:       [2]float32{0, 0},
			p2:       [2]float32{2, 0},
			p3:       [2]float32{0, 1},
			p4:       [2]float32{2, 1},
			expected: false,
		},
		{
			name:     "segments that would intersect if extended",
			p1:       [2]float32{0, 0},
			p2:       [2]float32{1, 0},
			p3:       [2]float32{2, -1},
			p4:       [2]float32{2, 1},
			expected: false,
		},
		{
			name:     "runway intersection example",
			p1:       [2]float32{0, 0},  // Runway 1 start
			p2:       [2]float32{10, 0}, // Runway 1 end
			p3:       [2]float32{5, -5}, // Runway 2 start
			p4:       [2]float32{5, 5},  // Runway 2 end
			expected: true,
			point:    [2]float32{5, 0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			point, intersects := SegmentSegmentIntersect(test.p1, test.p2, test.p3, test.p4)

			if intersects != test.expected {
				t.Errorf("Expected intersection %v, got %v", test.expected, intersects)
			}

			if test.expected && intersects {
				const tolerance = 1e-5
				if Abs(point[0]-test.point[0]) > tolerance || Abs(point[1]-test.point[1]) > tolerance {
					t.Errorf("Expected intersection point %v, got %v", test.point, point)
				}
			}
		})
	}
}

func TestSignBit(t *testing.T) {
	for _, v := range []float32{-1, 0, -0, 1, 55, -125.2} {
		if SignBit(v) != gomath.Signbit(float64(v)) {
			t.Errorf("%f: got %v for sign bit; expected %v", v, SignBit(v), gomath.Signbit(float64(v)))
		}
	}
}
