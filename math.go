// math.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/MichaelTJones/pcg"
	"golang.org/x/exp/constraints"
)

const nmPerLatitude = 60

///////////////////////////////////////////////////////////////////////////
// core math

// argmin returns the index of the minimum value of a number of float32s.
func argmin(v ...float32) int {
	minval := v[0]
	minidx := 0
	for i, val := range v {
		if val < minval {
			minidx = i
			minval = val
		}
	}
	return minidx
}

// degrees converts an angle expressed in degrees to radians
func degrees(r float32) float32 {
	return r * 180 / math.Pi
}

// radians converts an angle expressed in radians to degrees
func radians(d float32) float32 {
	return d / 180 * math.Pi
}

// A number of utility functions for evaluating transcendentals and the like follow;
// since we mostly use float32, it's handy to be able to call these directly rather than
// with all of the casts that are required when using the math package.
func sin(a float32) float32 {
	return float32(math.Sin(float64(a)))
}

func cos(a float32) float32 {
	return float32(math.Cos(float64(a)))
}

func tan(a float32) float32 {
	return float32(math.Tan(float64(a)))
}

func atan2(y, x float32) float32 {
	return float32(math.Atan2(float64(y), float64(x)))
}

func sqrt(a float32) float32 {
	return float32(math.Sqrt(float64(a)))
}

func mod(a, b float32) float32 {
	return float32(math.Mod(float64(a), float64(b)))
}

func sign(v float32) float32 {
	if v > 0 {
		return 1
	} else if v < 0 {
		return -1
	}
	return 0
}

func floor(v float32) float32 {
	return float32(math.Floor(float64(v)))
}

func ceil(v float32) float32 {
	return float32(math.Ceil(float64(v)))
}

func abs[V constraints.Integer | constraints.Float](x V) V {
	if x < 0 {
		return -x
	}
	return x
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func max[T constraints.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func pow(a, b float32) float32 {
	return float32(math.Pow(float64(a), float64(b)))
}

func sqr[V constraints.Integer | constraints.Float](v V) V { return v * v }

func clamp[T constraints.Ordered](x T, low T, high T) T {
	if x < low {
		return low
	} else if x > high {
		return high
	}
	return x
}

func lerp(x, a, b float32) float32 {
	return (1-x)*a + x*b
}

///////////////////////////////////////////////////////////////////////////
// Extent2D

// Extent2D represents a 2D bounding box with the two vertices at its
// opposite minimum and maximum corners.
type Extent2D struct {
	p0, p1 [2]float32
}

// EmptyExtent2D returns an Extent2D representing an empty bounding box.
func EmptyExtent2D() Extent2D {
	// Degenerate bounds
	return Extent2D{p0: [2]float32{1e30, 1e30}, p1: [2]float32{-1e30, -1e30}}
}

// Extent2DFromPoints returns an Extent2D that bounds all of the provided
// points.
func Extent2DFromPoints(pts [][2]float32) Extent2D {
	e := EmptyExtent2D()
	for _, p := range pts {
		for d := 0; d < 2; d++ {
			if p[d] < e.p0[d] {
				e.p0[d] = p[d]
			}
			if p[d] > e.p1[d] {
				e.p1[d] = p[d]
			}
		}
	}
	return e
}

func (e Extent2D) Width() float32 {
	return e.p1[0] - e.p0[0]
}

func (e Extent2D) Height() float32 {
	return e.p1[1] - e.p0[1]
}

func (e Extent2D) Center() [2]float32 {
	return [2]float32{(e.p0[0] + e.p1[0]) / 2, (e.p0[1] + e.p1[1]) / 2}
}

// Expand expands the extent by the given distance in all directions.
func (e Extent2D) Expand(d float32) Extent2D {
	return Extent2D{
		p0: [2]float32{e.p0[0] - d, e.p0[1] - d},
		p1: [2]float32{e.p1[0] + d, e.p1[1] + d}}
}

func (e Extent2D) Inside(p [2]float32) bool {
	return p[0] >= e.p0[0] && p[0] <= e.p1[0] && p[1] >= e.p0[1] && p[1] <= e.p1[1]
}

// Overlaps returns true if the two provided Extent2Ds overlap.
func Overlaps(a Extent2D, b Extent2D) bool {
	x := (a.p1[0] >= b.p0[0]) && (a.p0[0] <= b.p1[0])
	y := (a.p1[1] >= b.p0[1]) && (a.p0[1] <= b.p1[1])
	return x && y
}

func Union(e Extent2D, p [2]float32) Extent2D {
	e.p0[0] = min(e.p0[0], p[0])
	e.p0[1] = min(e.p0[1], p[1])
	e.p1[0] = max(e.p1[0], p[0])
	e.p1[1] = max(e.p1[1], p[1])
	return e
}

// ClosestPointInBox returns the closest point to p that is inside the
// Extent2D.  (If p is already inside it, then it is returned.)
func (e Extent2D) ClosestPointInBox(p [2]float32) [2]float32 {
	return [2]float32{clamp(p[0], e.p0[0], e.p1[0]), clamp(p[1], e.p0[1], e.p1[1])}
}

// IntersectRay find the intersections of the ray with given origin and
// direction with the Extent2D.  The returned Boolean value indicates
// whether an intersection was found.  If true, the two returned
// floating-point values give the parametric distances along the ray where
// the intersections occurred.
func (e Extent2D) IntersectRay(org, dir [2]float32) (bool, float32, float32) {
	t0, t1 := float32(0), float32(1e30)
	tx0 := (e.p0[0] - org[0]) / dir[0]
	tx1 := (e.p1[0] - org[0]) / dir[0]
	tx0, tx1 = min(tx0, tx1), max(tx0, tx1)
	t0 = max(t0, tx0)
	t1 = min(t1, tx1)

	ty0 := (e.p0[1] - org[1]) / dir[1]
	ty1 := (e.p1[1] - org[1]) / dir[1]
	ty0, ty1 = min(ty0, ty1), max(ty0, ty1)
	t0 = max(t0, ty0)
	t1 = min(t1, ty1)

	return t0 < t1, t0, t1
}

func (e Extent2D) Offset(p [2]float32) Extent2D {
	return Extent2D{p0: add2f(e.p0, p), p1: add2f(e.p1, p)}
}

func (e Extent2D) Scale(s float32) Extent2D {
	return Extent2D{p0: scale2f(e.p0, s), p1: scale2f(e.p1, s)}
}

func (e Extent2D) Lerp(p [2]float32) [2]float32 {
	return [2]float32{lerp(p[0], e.p0[0], e.p1[0]), lerp(p[1], e.p0[1], e.p1[1])}
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
	if math.Abs(denom) < 1e-5 { // TODO: threshold?
		return [2]float32{}, false
	}
	numx := (p1[0]*p2[1]-p1[1]*p2[0])*(p3[0]-p4[0]) - (p1[0]-p2[0])*(p3[0]*p4[1]-p3[1]*p4[0])
	numy := (p1[0]*p2[1]-p1[1]*p2[0])*(p3[1]-p4[1]) - (p1[1]-p2[1])*(p3[0]*p4[1]-p3[1]*p4[0])

	return [2]float32{float32(numx / denom), float32(numy / denom)}, true
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
		return float32(math.Inf(1))
	}
	return (dx*(p0[1]-p[1]) - dy*(p0[0]-p[0])) / sqrt(sq)
}

// PointLineDistance returns the minimum distance from the point p to the infinite line defined by (p0, p1).
func PointLineDistance(p, p0, p1 [2]float32) float32 {
	return abs(SignedPointLineDistance(p, p0, p1))
}

// Return minimum distance between line segment vw and point p
// https://stackoverflow.com/a/1501725
func PointSegmentDistance(p, v, w [2]float32) float32 {
	l := sub2f(v, w)
	l2 := dot(l, l)
	if l2 == 0 {
		return length2f(sub2f(p, v))
	}
	t := clamp(dot(sub2f(p, v), sub2f(w, v))/l2, 0, 1)
	proj := add2f(v, scale2f(sub2f(w, v), t))
	return distance2f(p, proj)
}

// ClosestPointOnLine returns the closest point on the (infinite) line to
// the given point p.
func ClosestPointOnLine(line [2][2]float32, p [2]float32) [2]float32 {
	x1, y1 := line[0][0], line[0][1]
	x2, y2 := line[1][0], line[1][1]

	t := (((p[0] - x1) * (x2 - x1)) + ((p[1] - y1) * (y2 - y1))) / ((x2-x1)*(x2-x1) + (y2-y1)*(y2-y1))

	return [2]float32{lerp(t, x1, x2), lerp(t, y1, y2)}
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
func PointInPolygon(p Point2LL, pts []Point2LL) bool {
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

///////////////////////////////////////////////////////////////////////////
// Point2LL

const NauticalMilesToFeet = 6076.12
const FeetToNauticalMiles = 1 / NauticalMilesToFeet

// Point2LL represents a 2D point on the Earth in latitude-longitude.
// Important: 0 (x) is longitude, 1 (y) is latitude
type Point2LL [2]float32

func (p Point2LL) Longitude() float32 {
	return p[0]
}

func (p Point2LL) Latitude() float32 {
	return p[1]
}

// DDString returns the position in decimal degrees, e.g.:
// (39.860901, -75.274864)
func (p Point2LL) DDString() string {
	return fmt.Sprintf("(%f, %f)", p[1], p[0]) // latitude, longitude
}

// DMSString returns the position in degrees minutes, seconds, e.g.
// N039.51.39.243,W075.16.29.511
func (p Point2LL) DMSString() string {
	format := func(v float32) string {
		s := fmt.Sprintf("%03d", int(v))
		v -= floor(v)
		v *= 60
		s += fmt.Sprintf(".%02d", int(v))
		v -= floor(v)
		v *= 60
		s += fmt.Sprintf(".%02d", int(v))
		v -= floor(v)
		v *= 1000
		s += fmt.Sprintf(".%03d", int(v))
		return s
	}

	var s string
	if p[1] > 0 {
		s = "N"
	} else {
		s = "S"
	}
	s += format(abs(p[1]))

	if p[0] > 0 {
		s += ",E"
	} else {
		s += ",W"
	}
	s += format(abs(p[0]))

	return s
}

var (
	// pair of floats (no exponents)
	reWaypointFloat = regexp.MustCompile(`^(\-?[0-9]+\.[0-9]+), *(\-?[0-9]+\.[0-9]+)`)
	// https://en.wikipedia.org/wiki/ISO_6709#String_expression_(Annex_H)
	// e.g. +403527.580-0734452.955
	reISO6709H = regexp.MustCompile(`^([-+][0-9][0-9])([0-9][0-9])([0-9][0-9])\.([0-9][0-9][0-9])([-+][0-9][0-9][0-9])([0-9][0-9])([0-9][0-9])\.([0-9][0-9][0-9])`)
)

// Parse waypoints of the form "N40.37.58.400, W073.46.17.000".  Previously
// we would match the following regexp and then peel apart the pieces but
// profiling showed that the majority of the time spent parsing the current
// video maps was spent in this function. Thus, a specialized
// implementation that is about 12x faster and reduces overall time spent
// parsing video maps at startup (including zstd decompression and JSON
// parsing) from ~2.5s to about 0.75s.
//
// For posterity, said regexp:
// reWaypointDotted = regexp.MustCompile(`^([NS][0-9]+\.[0-9]+\.[0-9]+\.[0-9]+), *([EW][0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
func tryParseWaypointDotted(b []byte) (Point2LL, bool) {
	if len(b) == 0 || (b[0] != 'N' && b[0] != 'S') {
		return Point2LL{}, false
	}
	negateLatitude := b[0] == 'S'

	// Skip over the N/S and parse the four dotted numbers following it
	b = b[1:]
	latitude, n, ok := tryParseWaypointNumbers(b)
	if !ok {
		return Point2LL{}, false
	}
	if negateLatitude {
		latitude = -latitude
	}
	// Skip what's been processed
	b = b[n:]

	// Skip comma
	if len(b) == 0 || b[0] != ',' {
		return Point2LL{}, false
	}
	b = b[1:]

	// Skip optional space
	if len(b) > 0 && b[0] == ' ' {
		b = b[1:]
	}

	// Onward to E/W
	if len(b) == 0 || (b[0] != 'E' && b[0] != 'W') {
		return Point2LL{}, false
	}
	negateLongitude := b[0] == 'W'

	// Skip over E/W and parse its four dotted numbers.
	b = b[1:]
	longitude, _, ok := tryParseWaypointNumbers(b)
	if !ok {
		return Point2LL{}, false
	}
	if negateLongitude {
		longitude = -longitude
	}

	return Point2LL{longitude, latitude}, true
}

// Efficient function parse a latlong of the form aaa.bbb.ccc.ddd and
// return the corresponding float32. Returns the latlong, the number of
// bytes of b consumed, and a bool indicating success or failure.
func tryParseWaypointNumbers(b []byte) (float32, int, bool) {
	n := 0
	var ll float64

	// Scan to the end of the current number group; return
	// the number of bytes it uses.
	scan := func(b []byte) int {
		for i, v := range b {
			if v == '.' || v == ',' {
				return i
			}
		}
		return len(b)
	}

	for i := 0; i < 4; i++ {
		end := scan(b)
		if end == 0 {
			return 0, 0, false
		}

		value := 0
		for _, ch := range b[:end] {
			if ch < '0' || ch > '9' {
				return 0, 0, false
			}
			value *= 10
			value += int(ch - '0')
		}
		if i == 3 {
			// Treat the last set of digits as a decimal, so that
			// Nxx.yy.zz.1 is handled like Nxx.yy.zz.100.
			for j := end; j < 3; j++ {
				value *= 10
			}
		}

		scales := [4]float64{1, 60, 3600, 3600000}
		ll += float64(value) / scales[i]
		n += end
		b = b[end:]

		if i < 3 {
			if len(b) == 0 {
				return 0, 0, false
			}
			b = b[1:]
			n++
		}
	}

	return float32(ll), n, true
}

func ParseLatLong(llstr []byte) (Point2LL, error) {
	// First try to match e.g. "N040.44.21.753,W075.41.55.347". Try this
	// first and by hand rather than with a regexp, since the video map
	// files are full of these.
	if p, ok := tryParseWaypointDotted(llstr); ok {
		return p, nil
	} else if strs := reWaypointFloat.FindStringSubmatch(string(llstr)); len(strs) == 3 {
		if l, err := strconv.ParseFloat(strs[1], 32); err != nil {
			return Point2LL{}, err
		} else {
			p[1] = float32(l)
		}
		if l, err := strconv.ParseFloat(strs[2], 32); err != nil {
			return Point2LL{}, err
		} else {
			p[0] = float32(l)
		}
		return p, nil
	} else if strs := reISO6709H.FindStringSubmatch(string(llstr)); len(strs) == 9 {
		parse := func(deg, min, sec, frac string) (float32, error) {
			d, err := strconv.Atoi(deg)
			if err != nil {
				return 0, err
			}
			m, err := strconv.Atoi(min)
			if err != nil {
				return 0, err
			}
			s, err := strconv.Atoi(sec)
			if err != nil {
				return 0, err
			}
			f, err := strconv.Atoi(frac)
			if err != nil {
				return 0, err
			}
			sgn := sign(float32(d))
			d = abs(d)
			return sgn * (float32(d) + float32(m)/60 + float32(s)/3600 + float32(f)/3600000), nil
		}

		var err error
		p[1], err = parse(strs[1], strs[2], strs[3], strs[4])
		if err != nil {
			return Point2LL{}, err
		}
		p[0], err = parse(strs[5], strs[6], strs[7], strs[8])
		if err != nil {
			return Point2LL{}, err
		}
		return p, nil
	} else {
		return Point2LL{}, fmt.Errorf("%s: invalid latlong string", llstr)
	}
}

func (p Point2LL) IsZero() bool {
	return p[0] == 0 && p[1] == 0
}

func add2ll(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(add2f(a, b))
}

func mid2ll(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(mid2f(a, b))
}

func sub2ll(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(sub2f(a, b))
}

// nmdistance2ll returns the distance in nautical miles between two
// provided lat-long coordinates.
func nmdistance2ll(a Point2LL, b Point2LL) float32 {
	// https://www.movable-type.co.uk/scripts/latlong.html
	const R = 6371000 // metres
	rad := func(d float64) float64 { return float64(d) / 180 * math.Pi }
	lat1, lon1 := rad(float64(a[1])), rad(float64(a[0]))
	lat2, lon2 := rad(float64(b[1])), rad(float64(b[0]))
	dlat, dlon := lat2-lat1, lon2-lon1

	x := sqr(math.Sin(dlat/2)) + math.Cos(lat1)*math.Cos(lat2)*sqr(math.Sin(dlon/2))
	c := 2 * math.Atan2(math.Sqrt(x), math.Sqrt(1-x))
	dm := R * c // in metres

	return float32(dm * 0.000539957)
}

// nmlength2ll returns the length of a vector expressed in lat-long
// coordinates.
func nmlength2ll(a Point2LL, nmPerLongitude float32) float32 {
	x := a[0] * nmPerLongitude
	y := a[1] * nmPerLatitude
	return sqrt(sqr(x) + sqr(y))
}

// nm2ll converts a point expressed in nautical mile coordinates to
// lat-long.
func nm2ll(p [2]float32, nmPerLongitude float32) Point2LL {
	return Point2LL{p[0] / nmPerLongitude, p[1] / nmPerLatitude}
}

// ll2nm converts a point expressed in latitude-longitude coordinates to
// nautical mile coordinates; this is useful for example for reasoning
// about distances, since both axes then have the same measure.
func ll2nm(p Point2LL, nmPerLongitude float32) [2]float32 {
	return [2]float32{p[0] * nmPerLongitude, p[1] * nmPerLatitude}
}

// Store Point2LLs as strings is JSON, for compactness/friendliness...
func (p Point2LL) MarshalJSON() ([]byte, error) {
	return []byte("\"" + p.DMSString() + "\""), nil
}

func (p *Point2LL) UnmarshalJSON(b []byte) error {
	if b[0] == '[' {
		// Backwards compatibility for arrays of two floats...
		var pt [2]float32
		err := json.Unmarshal(b, &pt)
		if err == nil {
			*p = pt
		}
		return err
	} else {
		n := len(b)
		// Remove the quotes before parsing
		b = b[1 : n-1]
		pt, err := ParseLatLong(b)
		if err == nil {
			*p = pt
			return nil
		}

		s := string(b)
		if n, ok := database.Navaids[s]; ok {
			*p = n.Location
			return nil
		} else if n, ok := database.Airports[s]; ok {
			*p = n.Location
			return nil
		} else if f, ok := database.Fixes[s]; ok {
			*p = f.Location
			return nil
		} else {
			return err
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// point 2f

// Various useful functions for arithmetic with 2D points/vectors.
// Names are brief in order to avoid clutter when they're used.

// a+b
func add2f(a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{a[0] + b[0], a[1] + b[1]}
}

// midpoint of a and b
func mid2f(a [2]float32, b [2]float32) [2]float32 {
	return scale2f(add2f(a, b), 0.5)
}

// a-b
func sub2f(a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{a[0] - b[0], a[1] - b[1]}
}

// a*s
func scale2f(a [2]float32, s float32) [2]float32 {
	return [2]float32{s * a[0], s * a[1]}
}

func dot(a, b [2]float32) float32 {
	return a[0]*b[0] + a[1]*b[1]
}

// Linearly interpolate x of the way between a and b. x==0 corresponds to
// a, x==1 corresponds to b, etc.
func lerp2f(x float32, a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{(1-x)*a[0] + x*b[0], (1-x)*a[1] + x*b[1]}
}

// Length of v
func length2f(v [2]float32) float32 {
	return sqrt(v[0]*v[0] + v[1]*v[1])
}

// Distance between two points
func distance2f(a [2]float32, b [2]float32) float32 {
	return length2f(sub2f(a, b))
}

// Normalizes the given vector.
func normalize2f(a [2]float32) [2]float32 {
	l := length2f(a)
	if l == 0 {
		return [2]float32{0, 0}
	}
	return scale2f(a, 1/l)
}

// rotator2f returns a function that rotates points by the specified angle
// (given in degrees).
func rotator2f(angle float32) func([2]float32) [2]float32 {
	s, c := sin(radians(angle)), cos(radians(angle))
	return func(p [2]float32) [2]float32 {
		return [2]float32{c*p[0] + s*p[1], -s*p[0] + c*p[1]}
	}
}

// Equivalent to acos(Dot(a, b)), but more numerically stable.
// via http://www.plunk.org/~hatch/rightway.html
func angleBetween(v1, v2 [2]float32) float32 {
	asin := func(a float32) float32 {
		return float32(math.Asin(float64(clamp(a, -1, 1))))
	}

	if dot(v1, v2) < 0 {
		return math.Pi - 2*asin(length2f(add2f(v1, v2))/2)
	} else {
		return 2 * asin(length2f(sub2f(v2, v1))/2)
	}
}

///////////////////////////////////////////////////////////////////////////
// 3x3 matrix

type Matrix3 [3][3]float32

func MakeMatrix3(m00, m01, m02, m10, m11, m12, m20, m21, m22 float32) Matrix3 {
	return [3][3]float32{
		[3]float32{m00, m01, m02},
		[3]float32{m10, m11, m12},
		[3]float32{m20, m21, m22}}
}

func Identity3x3() Matrix3 {
	var m Matrix3
	m[0][0] = 1
	m[1][1] = 1
	m[2][2] = 1
	return m
}

func (m Matrix3) PostMultiply(m2 Matrix3) Matrix3 {
	var result Matrix3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			result[i][j] = m[i][0]*m2[0][j] + m[i][1]*m2[1][j] + m[i][2]*m2[2][j]
		}
	}
	return result
}

func (m Matrix3) Scale(x, y float32) Matrix3 {
	return m.PostMultiply(MakeMatrix3(x, 0, 0, 0, y, 0, 0, 0, 1))
}

func (m Matrix3) Translate(x, y float32) Matrix3 {
	return m.PostMultiply(MakeMatrix3(1, 0, x, 0, 1, y, 0, 0, 1))
}

func (m Matrix3) Ortho(x0, x1, y0, y1 float32) Matrix3 {
	return m.PostMultiply(MakeMatrix3(
		2/(x1-x0), 0, -(x0+x1)/(x1-x0),
		0, 2/(y1-y0), -(y0+y1)/(y1-y0),
		0, 0, 1))
}

func (m Matrix3) Rotate(theta float32) Matrix3 {
	s, c := sin(theta), cos(theta)
	return m.PostMultiply(MakeMatrix3(c, -s, 0, s, c, 0, 0, 0, 1))
}

func (m Matrix3) Determinant() float32 {
	minor12 := m[1][1]*m[2][2] - m[1][2]*m[2][1]
	minor02 := m[1][0]*m[2][2] - m[1][2]*m[2][0]
	minor01 := m[1][0]*m[2][1] - m[1][1]*m[2][0]
	return m[0][2]*minor01 + (m[0][0]*minor12 - m[0][1]*minor02)
}

func (m Matrix3) Inverse() Matrix3 {
	invDet := 1 / m.Determinant()
	var r Matrix3
	r[0][0] = invDet * (m[1][1]*m[2][2] - m[1][2]*m[2][1])
	r[1][0] = invDet * (m[1][2]*m[2][0] - m[1][0]*m[2][2])
	r[2][0] = invDet * (m[1][0]*m[2][1] - m[1][1]*m[2][0])
	r[0][1] = invDet * (m[0][2]*m[2][1] - m[0][1]*m[2][2])
	r[1][1] = invDet * (m[0][0]*m[2][2] - m[0][2]*m[2][0])
	r[2][1] = invDet * (m[0][1]*m[2][0] - m[0][0]*m[2][1])
	r[0][2] = invDet * (m[0][1]*m[1][2] - m[0][2]*m[1][1])
	r[1][2] = invDet * (m[0][2]*m[1][0] - m[0][0]*m[1][2])
	r[2][2] = invDet * (m[0][0]*m[1][1] - m[0][1]*m[1][0])
	return r
}

func (m Matrix3) TransformPoint(p [2]float32) [2]float32 {
	return [2]float32{
		m[0][0]*p[0] + m[0][1]*p[1] + m[0][2],
		m[1][0]*p[0] + m[1][1]*p[1] + m[1][2],
	}
}

func (m Matrix3) TransformVector(p [2]float32) [2]float32 {
	return [2]float32{
		m[0][0]*p[0] + m[0][1]*p[1],
		m[1][0]*p[0] + m[1][1]*p[1],
	}
}

///////////////////////////////////////////////////////////////////////////
// Random numbers.

type Rand struct {
	r *pcg.PCG32
}

// Drop-in replacement for the subset of math/rand that we use...
var rand Rand

func init() {
	rand.r = pcg.NewPCG32()
}

func (r *Rand) Seed(s int64) {
	r.r.Seed(uint64(s), 0xda3e39cb94b95bdb)
}

func (r *Rand) Intn(n int) int {
	return int(r.r.Bounded(uint32(n)))
}

func (r *Rand) Int31n(n int32) int32 {
	return int32(r.r.Bounded(uint32(n)))
}

func (r *Rand) Float32() float32 {
	return float32(r.r.Random()) / (1<<32 - 1)
}
