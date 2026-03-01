// pkg/math/math_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"math"
	"testing"

	"github.com/mmp/vice/rand"
)

func TestParseLatLong(t *testing.T) {
	type LL struct {
		str string
		pos Point2LL
	}
	latlongs := []LL{
		{str: "N40.37.58.400, W073.46.17.000", pos: Point2LL{-73.771385, 40.6328888}}, // JFK VOR
		{str: "N40.37.58.4,W073.46.17.000", pos: Point2LL{-73.771385, 40.6328888}},    // JFK VOR
		{str: "40.6328888, -73.771385", pos: Point2LL{-73.771385, 40.6328888}},        // JFK VOR
		{str: "+403758.400-0734617.000", pos: Point2LL{-73.7713928, 40.632885}},       // JFK VOR
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
		{str: "4037N/07346W", pos: Point2LL{-73.76666667, 40.616667}},
		{str: "1234S/12016E", pos: Point2LL{120.2666667, -12.5666667}},
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
		for i := range n {
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
	for range 32 {
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

func TestSplitSelfIntersectingPolygon(t *testing.T) {
	approxEqual := func(a, b [2]float32) bool {
		const eps = 1e-4
		return Abs(a[0]-b[0]) < eps && Abs(a[1]-b[1]) < eps
	}

	polyContains := func(poly []Point2LL, pt [2]float32) bool {
		for _, v := range poly {
			if approxEqual(v, pt) {
				return true
			}
		}
		return false
	}

	t.Run("simple triangle", func(t *testing.T) {
		tri := []Point2LL{{0, 0}, {1, 0}, {0, 1}}
		result := SplitSelfIntersectingPolygon(tri)
		if len(result) != 1 {
			t.Fatalf("expected 1 polygon, got %d", len(result))
		}
		if len(result[0]) != 3 {
			t.Fatalf("expected 3 vertices, got %d", len(result[0]))
		}
	})

	t.Run("simple square", func(t *testing.T) {
		sq := []Point2LL{{0, 0}, {2, 0}, {2, 2}, {0, 2}}
		result := SplitSelfIntersectingPolygon(sq)
		if len(result) != 1 {
			t.Fatalf("expected 1 polygon, got %d", len(result))
		}
		if len(result[0]) != 4 {
			t.Fatalf("expected 4 vertices, got %d", len(result[0]))
		}
	})

	t.Run("bowtie", func(t *testing.T) {
		// Edges (0,0)-(2,2) and (2,0)-(0,2) cross at (1,1).
		bowtie := []Point2LL{{0, 0}, {2, 2}, {2, 0}, {0, 2}}
		result := SplitSelfIntersectingPolygon(bowtie)
		if len(result) != 2 {
			t.Fatalf("expected 2 polygons, got %d", len(result))
		}
		// Both sub-polygons should be triangles containing (1,1).
		for i, poly := range result {
			if len(poly) != 3 {
				t.Errorf("polygon %d: expected 3 vertices, got %d", i, len(poly))
			}
			if !polyContains(poly, [2]float32{1, 1}) {
				t.Errorf("polygon %d: expected intersection point (1,1)", i)
			}
		}
	})

	t.Run("figure-eight 5 vertices", func(t *testing.T) {
		// Pentagon-like shape with one crossing: edges (1,0)-(0,2) and (2,2)-(0,1) cross.
		poly := []Point2LL{{0, 0}, {1, 0}, {0, 2}, {2, 2}, {0, 1}}
		result := SplitSelfIntersectingPolygon(poly)
		if len(result) < 2 {
			t.Fatalf("expected at least 2 polygons, got %d", len(result))
		}
		// Total vertex count across all sub-polygons should exceed the
		// original because the intersection point is added to both halves.
		totalVerts := 0
		for _, p := range result {
			totalVerts += len(p)
		}
		if totalVerts < len(poly)+1 {
			t.Errorf("expected more total vertices than original (%d), got %d", len(poly), totalVerts)
		}
	})
}

func TestSignBit(t *testing.T) {
	for _, v := range []float32{-1, 0, -0, 1, 55, -125.2} {
		if SignBit(v) != math.Signbit(float64(v)) {
			t.Errorf("%f: got %v for sign bit; expected %v", v, SignBit(v), math.Signbit(float64(v)))
		}
	}
}
