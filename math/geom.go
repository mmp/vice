// pkg/math/geom.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	gomath "math"
	"sort"
)

///////////////////////////////////////////////////////////////////////////
// Extent2D

// Extent2D represents a 2D bounding box with the two vertices at its
// opposite minimum and maximum corners.
type Extent2D struct {
	P0, P1 [2]float32
}

// EmptyExtent2D returns an Extent2D representing an empty bounding box.
func EmptyExtent2D() Extent2D {
	// Degenerate bounds
	return Extent2D{P0: [2]float32{1e30, 1e30}, P1: [2]float32{-1e30, -1e30}}
}

// Extent2DFromPoints returns an Extent2D that bounds all of the provided
// points.
func Extent2DFromPoints(pts [][2]float32) Extent2D {
	e := EmptyExtent2D()
	for _, p := range pts {
		for d := 0; d < 2; d++ {
			if p[d] < e.P0[d] {
				e.P0[d] = p[d]
			}
			if p[d] > e.P1[d] {
				e.P1[d] = p[d]
			}
		}
	}
	return e
}

// Extent2DFromP2LLs returns an Extent2D that bounds all of the provided
// points.
func Extent2DFromP2LLs(pts []Point2LL) Extent2D {
	e := EmptyExtent2D()
	for _, p := range pts {
		for d := 0; d < 2; d++ {
			if p[d] < e.P0[d] {
				e.P0[d] = p[d]
			}
			if p[d] > e.P1[d] {
				e.P1[d] = p[d]
			}
		}
	}
	return e
}

func (e Extent2D) Width() float32 {
	return e.P1[0] - e.P0[0]
}

func (e Extent2D) Height() float32 {
	return e.P1[1] - e.P0[1]
}

func (e Extent2D) Center() [2]float32 {
	return [2]float32{(e.P0[0] + e.P1[0]) / 2, (e.P0[1] + e.P1[1]) / 2}
}

// Expand expands the extent by the given distance in all directions.
func (e Extent2D) Expand(d float32) Extent2D {
	return Extent2D{
		P0: [2]float32{e.P0[0] - d, e.P0[1] - d},
		P1: [2]float32{e.P1[0] + d, e.P1[1] + d}}
}

func (e Extent2D) Inside(p [2]float32) bool {
	return p[0] >= e.P0[0] && p[0] <= e.P1[0] && p[1] >= e.P0[1] && p[1] <= e.P1[1]
}

// Overlaps returns true if the two provided Extent2Ds overlap.
func Overlaps(a Extent2D, b Extent2D) bool {
	x := (a.P1[0] >= b.P0[0]) && (a.P0[0] <= b.P1[0])
	y := (a.P1[1] >= b.P0[1]) && (a.P0[1] <= b.P1[1])
	return x && y
}

func Union(e Extent2D, p [2]float32) Extent2D {
	e.P0[0] = min(e.P0[0], p[0])
	e.P0[1] = min(e.P0[1], p[1])
	e.P1[0] = max(e.P1[0], p[0])
	e.P1[1] = max(e.P1[1], p[1])
	return e
}

// ClosestPointInBox returns the closest point to p that is inside the
// Extent2D.  (If p is already inside it, then it is returned.)
func (e Extent2D) ClosestPointInBox(p [2]float32) [2]float32 {
	return [2]float32{Clamp(p[0], e.P0[0], e.P1[0]), Clamp(p[1], e.P0[1], e.P1[1])}
}

// IntersectRay find the intersections of the ray with given origin and
// direction with the Extent2D.  The returned Boolean value indicates
// whether an intersection was found.  If true, the two returned
// floating-point values give the parametric distances along the ray where
// the intersections occurred.
func (e Extent2D) IntersectRay(org, dir [2]float32) (bool, float32, float32) {
	t0, t1 := float32(0), float32(1e30)
	tx0 := (e.P0[0] - org[0]) / dir[0]
	tx1 := (e.P1[0] - org[0]) / dir[0]
	tx0, tx1 = min(tx0, tx1), max(tx0, tx1)
	t0 = max(t0, tx0)
	t1 = min(t1, tx1)

	ty0 := (e.P0[1] - org[1]) / dir[1]
	ty1 := (e.P1[1] - org[1]) / dir[1]
	ty0, ty1 = min(ty0, ty1), max(ty0, ty1)
	t0 = max(t0, ty0)
	t1 = min(t1, ty1)

	return t0 < t1, t0, t1
}

func (e Extent2D) Offset(p [2]float32) Extent2D {
	return Extent2D{P0: Add2f(e.P0, p), P1: Add2f(e.P1, p)}
}

func (e Extent2D) Scale(s float32) Extent2D {
	return Extent2D{P0: Scale2f(e.P0, s), P1: Scale2f(e.P1, s)}
}

func (e Extent2D) Lerp(p [2]float32) [2]float32 {
	return [2]float32{Lerp(p[0], e.P0[0], e.P1[0]), Lerp(p[1], e.P0[1], e.P1[1])}
}

///////////////////////////////////////////////////////////////////////////
// Geometry

// LineLineIntersect returns the intersection point of the two lines
// specified by the vertices (p1f, p2f) and (p3f, p4f).  An additional
// returned Boolean value indicates whether a valid intersection was found.
// (There's no intersection for parallel lines, and none may be found in
// cases with tricky numerics.)
func LineLineIntersect(p1f, p2f, p3f, p4f [2]float32) ([2]float32, bool) {
	// It's important to do this in float64, given differences of
	// similar-ish values...
	p1 := [2]float64{float64(p1f[0]), float64(p1f[1])}
	p2 := [2]float64{float64(p2f[0]), float64(p2f[1])}
	p3 := [2]float64{float64(p3f[0]), float64(p3f[1])}
	p4 := [2]float64{float64(p4f[0]), float64(p4f[1])}

	d12 := [2]float64{p1[0] - p2[0], p1[1] - p2[1]}
	d34 := [2]float64{p3[0] - p4[0], p3[1] - p4[1]}
	denom := d12[0]*d34[1] - d12[1]*d34[0]
	if gomath.Abs(denom) < 1e-5 { // TODO: threshold?
		return [2]float32{}, false
	}
	numx := (p1[0]*p2[1]-p1[1]*p2[0])*(p3[0]-p4[0]) - (p1[0]-p2[0])*(p3[0]*p4[1]-p3[1]*p4[0])
	numy := (p1[0]*p2[1]-p1[1]*p2[0])*(p3[1]-p4[1]) - (p1[1]-p2[1])*(p3[0]*p4[1]-p3[1]*p4[0])

	return [2]float32{float32(numx / denom), float32(numy / denom)}, true
}

// SegmentSegmentIntersect returns the intersection point of the two line segments
// specified by the vertices (p1, p2) and (p3, p4). An additional returned Boolean
// value indicates whether a valid intersection was found within both segments.
func SegmentSegmentIntersect(p1, p2, p3, p4 [2]float32) ([2]float32, bool) {
	// First check if the infinite lines intersect
	p, ok := LineLineIntersect(p1, p2, p3, p4)
	if !ok {
		return [2]float32{}, false
	}

	// See if the intersection point is within the bounding boxes of both segments.
	b0 := Extent2DFromPoints([][2]float32{p1, p2})
	b1 := Extent2DFromPoints([][2]float32{p3, p4})

	return p, b0.Inside(p) && b1.Inside(p)
}

// RayRayMinimumDistance takes two rays p0+d0*t and p1+d1*t and returns the
// value of t where their distance is minimized.
func RayRayMinimumDistance(p0, d0, p1, d1 [2]float32) float32 {
	/*
			Mathematica:
				f[t_] := {ax, ay} + {bx, by} * t
				g[t_] := {cx, cy} + {dx, dy} * t
				d2 = Dot[f[t] - g[t], f[t] - g[t]]
				Solve[D[d2, t] == 0, t]
				CForm[...]
		Then substitute ax -> p0[0], etc.
	*/
	t := (d0[0]*p1[0] + d0[1]*p1[1] - p1[0]*d1[0] + p0[0]*(-d0[0]+d1[0]) - p1[1]*d1[1] + p0[1]*(-d0[1]+d1[1])) /
		((d0[0] * d0[0]) + (d0[1] * d0[1]) - 2*d0[0]*d1[0] + (d1[0] * d1[0]) - 2*d0[1]*d1[1] + (d1[1] * d1[1]))

	return t
}

// SignedPointLineDistance returns the signed distance from the point p to
// the infinite line defined by (p0, p1) where points to the right of the
// line have negative distances.
func SignedPointLineDistance(p, p0, p1 [2]float32) float32 {
	// https://en.wikipedia.org/wiki/Distance_from_a_point_to_a_line
	dx, dy := p1[0]-p0[0], p1[1]-p0[1]
	sq := dx*dx + dy*dy
	if sq == 0 {
		return float32(gomath.Inf(1))
	}
	return (dx*(p0[1]-p[1]) - dy*(p0[0]-p[0])) / Sqrt(sq)
}

// PointLineDistance returns the minimum distance from the point p to the infinite line defined by (p0, p1).
func PointLineDistance(p, p0, p1 [2]float32) float32 {
	return Abs(SignedPointLineDistance(p, p0, p1))
}

// Return minimum distance between line segment vw and point p
// https://stackoverflow.com/a/1501725
func PointSegmentDistance(p, v, w [2]float32) float32 {
	l := Sub2f(v, w)
	l2 := Dot(l, l)
	if l2 == 0 {
		return Length2f(Sub2f(p, v))
	}
	t := Clamp(Dot(Sub2f(p, v), Sub2f(w, v))/l2, 0, 1)
	proj := Add2f(v, Scale2f(Sub2f(w, v), t))
	return Distance2f(p, proj)
}

// ClosestPointOnLine returns the closest point on the (infinite) line to
// the given point p.
func ClosestPointOnLine(line [2][2]float32, p [2]float32) [2]float32 {
	x1, y1 := line[0][0], line[0][1]
	x2, y2 := line[1][0], line[1][1]

	t := (((p[0] - x1) * (x2 - x1)) + ((p[1] - y1) * (y2 - y1))) / ((x2-x1)*(x2-x1) + (y2-y1)*(y2-y1))

	return [2]float32{Lerp(t, x1, x2), Lerp(t, y1, y2)}
}

// Returns the vertex coordinates of an equilateral triangle centered at
// the origin with specified height.
func EquilateralTriangleVertices(height float32) [3][2]float32 {
	const InvSqrt3 = 0.577350269189626
	return [3][2]float32{
		[2]float32{0, height * 2 / 3},
		[2]float32{height * InvSqrt3, -height / 3},
		[2]float32{-height * InvSqrt3, -height / 3},
	}
}

// PointInPolygon checks whether the given point is inside the given polygon;
// it assumes that the last vertex does not repeat the first one, and so includes
// the edge from pts[len(pts)-1] to pts[0] in its test.
func PointInPolygon(p [2]float32, pts [][2]float32) bool {
	inside := false
	for i := 0; i < len(pts); i++ {
		p0, p1 := pts[i], pts[(i+1)%len(pts)]
		if (p0[1] <= p[1] && p[1] < p1[1]) || (p1[1] <= p[1] && p[1] < p0[1]) {
			x := p0[0] + (p[1]-p0[1])*(p1[0]-p0[0])/(p1[1]-p0[1])
			if x > p[0] {
				inside = !inside
			}
		}
	}
	return inside
}

func PointInPolygon2LL(p Point2LL, pts []Point2LL) bool {
	inside := false
	for i := 0; i < len(pts); i++ {
		p0, p1 := pts[i], pts[(i+1)%len(pts)]
		if (p0[1] <= p[1] && p[1] < p1[1]) || (p1[1] <= p[1] && p[1] < p0[1]) {
			x := p0[0] + (p[1]-p0[1])*(p1[0]-p0[0])/(p1[1]-p0[1])
			if x > p[0] {
				inside = !inside
			}
		}
	}
	return inside
}

var (
	// So that we can efficiently draw circles with various tessellations,
	// circlePoints caches vertex positions of a unit circle at the origin
	// for specified tessellation rates.
	circlePoints map[int][][2]float32
)

// CirclePoints returns the vertices for a unit circle at the origin
// with the given number of segments; it creates the vertex slice if this
// tessellation rate hasn't been seen before and otherwise returns a
// preexisting one.
func CirclePoints(nsegs int) [][2]float32 {
	if circlePoints == nil {
		circlePoints = make(map[int][][2]float32)
	}
	if _, ok := circlePoints[nsegs]; !ok {
		// Evaluate the vertices of the circle to initialize a new slice.
		var pts [][2]float32
		for d := 0; d < nsegs; d++ {
			angle := Radians(float32(d) / float32(nsegs) * 360)
			pt := [2]float32{Sin(angle), Cos(angle)}
			pts = append(pts, pt)
		}
		circlePoints[nsegs] = pts
	}

	// One way or another, it's now available in the map.
	return circlePoints[nsegs]
}

// https://en.wikibooks.org/wiki/Algorithm_Implementation/Geometry/Convex_hull/Monotone_chain
func ConvexHull(points [][2]float32) [][2]float32 {
	n := len(points)
	if n <= 1 {
		return append([][2]float32{}, points...)
	}

	sort.Slice(points, func(i, j int) bool {
		if points[i][0] == points[j][0] {
			return points[i][1] < points[j][1]
		}
		return points[i][0] < points[j][0]
	})

	cross := func(o, a, b [2]float32) float32 {
		return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
	}

	lower := make([][2]float32, 0, n)
	for _, p := range points {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 0 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}

	upper := make([][2]float32, 0, n)
	for i := n - 1; i >= 0; i-- {
		p := points[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 0 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}

	return append(lower[:len(lower)-1], upper[:len(upper)-1]...)
}
