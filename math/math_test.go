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

func TestDMEDistance(t *testing.T) {
	a := Point2LL{-73, 40}
	b := Point2LL{-73, 40}
	if got := DMEDistance(a, NauticalMilesToFeet, b, 0); Abs(got-1) > 1e-5 {
		t.Fatalf("expected 1nm vertical DME, got %.6f", got)
	}

	b = Point2LL{a[0] + 3/NMPerLongitudeAt(a), a[1]}
	if got := DMEDistance(a, 4*NauticalMilesToFeet, b, 0); Abs(got-5) > 1e-2 {
		t.Fatalf("expected 3-4-5 DME distance, got %.6f", got)
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

func TestRaySegmentIntersect(t *testing.T) {
	type Test struct {
		name     string
		org      [2]float32
		dir      [2]float32
		p0, p1   [2]float32
		expected bool
		point    [2]float32
		rayT     float32
		segT     float32
	}

	tests := []Test{
		{
			name:     "ray crosses segment ahead",
			org:      [2]float32{0, 0},
			dir:      [2]float32{1, 0},
			p0:       [2]float32{2, -1},
			p1:       [2]float32{2, 1},
			expected: true,
			point:    [2]float32{2, 0},
			rayT:     2,
			segT:     0.5,
		},
		{
			name:     "segment is behind ray origin",
			org:      [2]float32{0, 0},
			dir:      [2]float32{1, 0},
			p0:       [2]float32{-2, -1},
			p1:       [2]float32{-2, 1},
			expected: false,
		},
		{
			name:     "line intersection falls outside segment",
			org:      [2]float32{0, 0},
			dir:      [2]float32{1, 0},
			p0:       [2]float32{2, 2},
			p1:       [2]float32{2, 3},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			point, rayT, segT, ok := RaySegmentIntersect(test.org, test.dir, test.p0, test.p1)
			if ok != test.expected {
				t.Fatalf("expected intersection %v, got %v", test.expected, ok)
			}
			if !ok {
				return
			}

			const tolerance = 1e-5
			if Abs(point[0]-test.point[0]) > tolerance || Abs(point[1]-test.point[1]) > tolerance {
				t.Fatalf("expected point %v, got %v", test.point, point)
			}
			if Abs(rayT-test.rayT) > tolerance {
				t.Fatalf("expected rayT %f, got %f", test.rayT, rayT)
			}
			if Abs(segT-test.segT) > tolerance {
				t.Fatalf("expected segT %f, got %f", test.segT, segT)
			}
		})
	}
}

func TestIntersectRayWithRoute(t *testing.T) {
	// Anchor at the equator so nmPerLongitude is ~60 (same scale as latitude).
	// A 0.01° step is ~0.6nm; these tests don't care about exact distances,
	// only counts and segment indices.
	route := []Point2LL{
		{-0.10, 0},
		{0.10, 0},
		{0.10, 0.20},
	}

	t.Run("straight route crossed once", func(t *testing.T) {
		// Ray from just above the first segment, heading south (180°), crosses segment 0.
		hits := IntersectRayWithRoute(Point2LL{0, 0.05}, 180, route)
		if len(hits) != 1 {
			t.Fatalf("expected 1 hit, got %d", len(hits))
		}
		if hits[0].Index != 0 {
			t.Errorf("expected segment 0, got %d", hits[0].Index)
		}
		if Abs(hits[0].SegT-0.5) > 1e-3 {
			t.Errorf("expected segT ~0.5, got %f", hits[0].SegT)
		}
	})

	t.Run("ray misses route", func(t *testing.T) {
		// Ray going north from far west — doesn't cross any segment.
		hits := IntersectRayWithRoute(Point2LL{-0.50, -0.10}, 0, route)
		if len(hits) != 0 {
			t.Errorf("expected 0 hits, got %d", len(hits))
		}
	})

	t.Run("ray crosses two segments (L-shape)", func(t *testing.T) {
		// Ray from southwest heading northeast should cross both segment 0 (horizontal)
		// and segment 1 (vertical).
		hits := IntersectRayWithRoute(Point2LL{-0.05, -0.05}, 45, route)
		if len(hits) != 2 {
			t.Fatalf("expected 2 hits, got %d", len(hits))
		}
		if hits[0].Index != 0 || hits[1].Index != 1 {
			t.Errorf("expected hits on segments 0 and 1, got %d and %d", hits[0].Index, hits[1].Index)
		}
	})

	t.Run("origin on segment endpoint", func(t *testing.T) {
		// Ray originating at a segment endpoint heading along the segment.
		hits := IntersectRayWithRoute(Point2LL{-0.10, 0}, 90, route)
		if len(hits) == 0 {
			t.Fatal("expected at least one hit")
		}
	})

	t.Run("too-short route returns nil", func(t *testing.T) {
		hits := IntersectRayWithRoute(Point2LL{0, 0}, 90, []Point2LL{{0, 0}})
		if hits != nil {
			t.Errorf("expected nil, got %v", hits)
		}
	})
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

func TestExtent2DIntersect(t *testing.T) {
	a := Extent2D{P0: [2]float32{0, 0}, P1: [2]float32{10, 10}}
	b := Extent2D{P0: [2]float32{5, 5}, P1: [2]float32{15, 15}}
	got := Intersect(a, b)
	want := Extent2D{P0: [2]float32{5, 5}, P1: [2]float32{10, 10}}
	if got != want {
		t.Errorf("overlap: got %+v, want %+v", got, want)
	}
	if got.IsEmpty() {
		t.Errorf("overlap: IsEmpty=true, want false")
	}

	// Disjoint along x: result has P1[0] < P0[0].
	c := Extent2D{P0: [2]float32{20, 0}, P1: [2]float32{30, 10}}
	if r := Intersect(a, c); !r.IsEmpty() {
		t.Errorf("disjoint x: IsEmpty=false (%+v), want true", r)
	}

	// Edge-touching counts as empty (zero area).
	d := Extent2D{P0: [2]float32{10, 0}, P1: [2]float32{20, 10}}
	if r := Intersect(a, d); !r.IsEmpty() {
		t.Errorf("edge-touch: IsEmpty=false (%+v), want true", r)
	}

	// One fully containing the other returns the inner.
	inner := Extent2D{P0: [2]float32{2, 2}, P1: [2]float32{4, 4}}
	if r := Intersect(a, inner); r != inner {
		t.Errorf("contained: got %+v, want %+v", r, inner)
	}
}
