// pkg/aviation/path_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	gomath "math"
	"testing"

	"github.com/mmp/vice/math"
)

func TestPathFromReferenceLine(t *testing.T) {
	// Create a path from a reference line: heading 360 (due north), length 10nm
	// Reference point at origin in NM coords
	refPoint := math.NM2LL([2]float32{0, 0}, 60)
	path := PathFromReferenceLine(refPoint, 360, 10, 60, 0)

	if gomath.Abs(float64(path.Length-10)) > 0.01 {
		t.Errorf("expected path length 10, got %f", path.Length)
	}
	if len(path.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(path.Segments))
	}
}

func TestPathPointAtDistance(t *testing.T) {
	// Straight path from (0,0) to (10,0) — heading east
	path := Path{
		Segments: []PathSegment{{
			P0:        [2]float32{0, 0},
			P1:        [2]float32{10, 0},
			StartDist: 0,
			Length:    10,
		}},
		Length: 10,
	}

	// Midpoint
	pt, hdg := path.PointAtDistance(5)
	if gomath.Abs(float64(pt[0]-5)) > 0.01 || gomath.Abs(float64(pt[1])) > 0.01 {
		t.Errorf("midpoint: expected (5,0), got (%f,%f)", pt[0], pt[1])
	}
	// Heading should be 90 degrees (east)
	if gomath.Abs(float64(hdg-90)) > 0.5 {
		t.Errorf("midpoint heading: expected ~90, got %f", hdg)
	}

	// Beyond path end
	pt, _ = path.PointAtDistance(15)
	if gomath.Abs(float64(pt[0]-15)) > 0.01 || gomath.Abs(float64(pt[1])) > 0.01 {
		t.Errorf("beyond end: expected (15,0), got (%f,%f)", pt[0], pt[1])
	}

	// Before path start
	pt, _ = path.PointAtDistance(-3)
	if gomath.Abs(float64(pt[0]+3)) > 0.01 || gomath.Abs(float64(pt[1])) > 0.01 {
		t.Errorf("before start: expected (-3,0), got (%f,%f)", pt[0], pt[1])
	}
}

func TestPathProject(t *testing.T) {
	// Straight path from (0,0) to (10,0) — heading east
	path := Path{
		Segments: []PathSegment{{
			P0:        [2]float32{0, 0},
			P1:        [2]float32{10, 0},
			StartDist: 0,
			Length:    10,
		}},
		Length: 10,
	}

	// Project a point directly on the path
	d, perp, hdg := path.Project([2]float32{5, 0})
	if gomath.Abs(float64(d-5)) > 0.01 {
		t.Errorf("on-path distance: expected 5, got %f", d)
	}
	if gomath.Abs(float64(perp)) > 0.01 {
		t.Errorf("on-path perp: expected 0, got %f", perp)
	}
	if gomath.Abs(float64(hdg-90)) > 0.5 {
		t.Errorf("on-path heading: expected ~90, got %f", hdg)
	}

	// Project a point 2nm to the left of the path (north, since path goes east)
	d, perp, _ = path.Project([2]float32{5, 2})
	if gomath.Abs(float64(d-5)) > 0.01 {
		t.Errorf("left offset distance: expected 5, got %f", d)
	}
	if gomath.Abs(float64(perp-2)) > 0.01 {
		t.Errorf("left offset perp: expected 2, got %f", perp)
	}

	// Project a point 3nm to the right (south)
	d, perp, _ = path.Project([2]float32{5, -3})
	if gomath.Abs(float64(d-5)) > 0.01 {
		t.Errorf("right offset distance: expected 5, got %f", d)
	}
	if gomath.Abs(float64(perp-(-3))) > 0.01 {
		t.Errorf("right offset perp: expected -3, got %f", perp)
	}
}

func TestPathPolyline(t *testing.T) {
	path := Path{
		Segments: []PathSegment{{
			P0:        [2]float32{0, 0},
			P1:        [2]float32{10, 0},
			StartDist: 0,
			Length:    10,
		}},
		Length: 10,
	}

	poly := path.Polyline()
	if len(poly) != 2 {
		t.Errorf("expected 2 points in polyline, got %d", len(poly))
	}
}

func TestPathArcSegment(t *testing.T) {
	// Arc: quarter circle, center at (0,0), radius 5, from east to north (CCW)
	startAngle := float32(0)             // east
	sweep := float32(gomath.Pi / 2)      // 90 degrees CCW
	arcLen := float32(5 * gomath.Pi / 2) // radius * |sweep|

	path := Path{
		Segments: []PathSegment{{
			P0: [2]float32{5, 0}, // east point on circle
			P1: [2]float32{0, 5}, // north point on circle
			Arc: &PathArc{
				Center:     [2]float32{0, 0},
				Radius:     5,
				StartAngle: startAngle,
				Sweep:      sweep,
			},
			StartDist: 0,
			Length:    arcLen,
		}},
		Length: arcLen,
	}

	// Point at start
	pt, _ := path.PointAtDistance(0)
	if gomath.Abs(float64(pt[0]-5)) > 0.01 || gomath.Abs(float64(pt[1])) > 0.01 {
		t.Errorf("arc start: expected (5,0), got (%f,%f)", pt[0], pt[1])
	}

	// Point at end
	pt, _ = path.PointAtDistance(arcLen)
	if gomath.Abs(float64(pt[0])) > 0.01 || gomath.Abs(float64(pt[1]-5)) > 0.01 {
		t.Errorf("arc end: expected (0,5), got (%f,%f)", pt[0], pt[1])
	}

	// Point at midpoint (45 degrees)
	pt, _ = path.PointAtDistance(arcLen / 2)
	expected := float32(5 * gomath.Cos(gomath.Pi/4))
	if gomath.Abs(float64(pt[0]-expected)) > 0.05 || gomath.Abs(float64(pt[1]-expected)) > 0.05 {
		t.Errorf("arc midpoint: expected (%f,%f), got (%f,%f)", expected, expected, pt[0], pt[1])
	}

	// Polyline should have many points for arc
	poly := path.Polyline()
	if len(poly) < 45 { // ~90 degree arc sampled at ~1 degree
		t.Errorf("expected at least 45 polyline points for 90-degree arc, got %d", len(poly))
	}
}

func TestPathMultiSegment(t *testing.T) {
	// Two straight Segments: (0,0)→(5,0)→(5,5)
	path := Path{
		Segments: []PathSegment{
			{
				P0:        [2]float32{0, 0},
				P1:        [2]float32{5, 0},
				StartDist: 0,
				Length:    5,
			},
			{
				P0:        [2]float32{5, 0},
				P1:        [2]float32{5, 5},
				StartDist: 5,
				Length:    5,
			},
		},
		Length: 10,
	}

	// Point at distance 7 (2nm into second segment)
	pt, hdg := path.PointAtDistance(7)
	if gomath.Abs(float64(pt[0]-5)) > 0.01 || gomath.Abs(float64(pt[1]-2)) > 0.01 {
		t.Errorf("multi-seg d=7: expected (5,2), got (%f,%f)", pt[0], pt[1])
	}
	// Second segment goes north
	if gomath.Abs(float64(hdg-0)) > 0.5 && gomath.Abs(float64(hdg-360)) > 0.5 {
		t.Errorf("multi-seg d=7 heading: expected ~0/360 (north), got %f", hdg)
	}

	// Project point near second segment
	d, _, _ := path.Project([2]float32{5, 3})
	if gomath.Abs(float64(d-8)) > 0.01 {
		t.Errorf("project onto second segment: expected d=8, got %f", d)
	}
}
