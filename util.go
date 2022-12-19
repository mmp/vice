// util.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"golang.org/x/exp/constraints"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/klauspost/compress/zstd"
)

///////////////////////////////////////////////////////////////////////////
// decompression

var decoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))

// decompressZstd decompresses data that was compressed using zstd.
// There's no error handling to speak of, since this is currently only used
// for data that's baked into the vice binary, so any issues with that
// should be evident upon a first run.
func decompressZstd(s string) string {
	b, err := decoder.DecodeAll([]byte(s), nil)
	if err != nil {
		lg.ErrorfUp1("Error decompressing buffer")
	}
	return string(b)
}

///////////////////////////////////////////////////////////////////////////
// text

// wrapText wraps the provided text string to the given column limit, returning the
// wrapped string and the number of lines it became.  indent gives the amount to
// indent wrapped lines.  By default, lines that start with a space are assumed to be
// preformatted and are not wrapped; providing a true value for wrapAll overrides
// that behavior and causes them to be wrapped as well.
func wrapText(s string, columnLimit int, indent int, wrapAll bool) (string, int) {
	var accum, result strings.Builder

	var wrapLine bool
	column := 0
	lines := 1

	flush := func() {
		if wrapLine && column > columnLimit {
			result.WriteRune('\n')
			lines++
			for i := 0; i < indent; i++ {
				result.WriteRune(' ')
			}
			column = indent + accum.Len()
		}
		result.WriteString(accum.String())
		accum.Reset()
	}

	for _, ch := range s {
		// If wrapAll isn't enabled, then if the line starts with a space,
		// assume it is preformatted and pass it through unchanged.
		if column == 0 {
			wrapLine = wrapAll || ch != ' '
		}

		accum.WriteRune(ch)
		column++

		if ch == '\n' {
			flush()
			column = 0
			lines++
		} else if ch == ' ' {
			flush()
		}
	}

	flush()
	return result.String(), lines
}

// stopShouting turns text of the form "UNITED AIRLINES" to "United Airlines"
func stopShouting(orig string) string {
	var s strings.Builder
	wsLast := true
	for _, ch := range orig {
		if unicode.IsSpace(ch) {
			wsLast = true
		} else if unicode.IsLetter(ch) {
			if wsLast {
				// leave it alone
				wsLast = false
			} else {
				ch = unicode.ToLower(ch)
			}
		}

		// otherwise leave it alone

		s.WriteRune(ch)
	}
	return s.String()
}

// atof is a utility for parsing floating point values that sends errors to
// the logging system.
func atof(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		lg.ErrorfUp1("%s: error converting to float: %s", s, err)
		return 0
	} else {
		return v
	}
}

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

func sqr(v float32) float32 { return v * v }

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
// headings and directions

type CardinalOrdinalDirection int

const (
	North = iota
	NorthEast
	East
	SouthEast
	South
	SouthWest
	West
	NorthWest
)

func (co CardinalOrdinalDirection) Heading() float32 {
	return float32(co) * 45
}

func (co CardinalOrdinalDirection) ShortString() string {
	switch co {
	case North:
		return "N"
	case NorthEast:
		return "NE"
	case East:
		return "E"
	case SouthEast:
		return "SE"
	case South:
		return "S"
	case SouthWest:
		return "SW"
	case West:
		return "W"
	case NorthWest:
		return "NW"
	default:
		return "ERROR"
	}
}

// headingp2ll returns the heading from the point |from| to the point |to|
// in degrees.  The provided points should be in latitude-longitude
// coordinates and the provided magnetic correction is applied to the
// result.
func headingp2ll(from Point2LL, to Point2LL, magCorrection float32) float32 {
	v := Point2LL{to[0] - from[0], to[1] - from[1]}
	return headingv2ll(v, magCorrection)
}

// headingv2ll returns the heading in degrees corresponding to the provided
// vector expressed in latitude-longitude coordinates with the provided
// magnetic correction applied.
func headingv2ll(v Point2LL, magCorrection float32) float32 {
	// Note that atan2() normally measures w.r.t. the +x axis and angles
	// are positive for counter-clockwise. We want to measure w.r.t. +y and
	// to have positive angles be clockwise. Happily, swapping the order of
	// values passed to atan2()--passing (x,y), gives what we want.
	angle := degrees(atan2(v[0]*database.NmPerLongitude, v[1]*database.NmPerLatitude))
	angle += magCorrection
	for angle < 0 {
		angle += 360
	}
	return mod(angle, 360)
}

// headingDifference returns the minimum difference between two
// headings. (i.e., the result is always in the range [0,180].)
func headingDifference(a float32, b float32) float32 {
	var d float32
	if a > b {
		d = a - b
	} else {
		d = b - a
	}
	if d > 180 {
		d = 360 - d
	}
	return d
}

// compass converts a heading expressed into degrees into a string
// corresponding to the closest compass direction.
func compass(heading float32) string {
	h := mod(heading+22.5, 360) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"North", "Northeast", "East", "Southeast",
		"South", "Southwest", "West", "Northwest"}[idx]
}

// shortCompass converts a heading expressed in degrees into an abbreviated
// string corresponding to the closest compass direction.
func shortCompass(heading float32) string {
	h := mod(heading+22.5, 360) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[idx]
}

// headingAsHour converts a heading expressed in degrees into the closest
// "o'clock" value, with an integer result in the range [1,12].
func headingAsHour(heading float32) int {
	for heading < 0 {
		heading += 360
	}
	for heading > 360 {
		heading -= 360
	}

	heading -= 15
	if heading < 0 {
		heading += 360
	}
	// now [0,30] is 1 o'clock, etc
	return 1 + int(heading/30)
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

///////////////////////////////////////////////////////////////////////////
// RGB

type RGB struct {
	R, G, B float32
}

type RGBA struct {
	R, G, B, A float32
}

func lerpRGB(x float32, a, b RGB) RGB {
	return RGB{R: lerp(x, a.R, b.R), G: lerp(x, a.G, b.G), B: lerp(x, a.B, b.B)}
}

func (r RGB) Equals(other RGB) bool {
	return r.R == other.R && r.G == other.G && r.B == other.B
}

func (r RGB) Scale(v float32) RGB {
	return RGB{R: r.R * v, G: r.G * v, B: r.B * v}
}

// RGBFromHex converts a packed integer color value to an RGB where the low
// 8 bits give blue, the next 8 give green, and then the next 8 give red.
func RGBFromHex(c int) RGB {
	r, g, b := (c>>16)&255, (c>>8)&255, c&255
	return RGB{R: float32(r) / 255, G: float32(g) / 255, B: float32(b) / 255}
}

///////////////////////////////////////////////////////////////////////////
// Point2LL

const NauticalMilesToFeet = 6076.12
const FeetToNauticalMiles = 1 / NauticalMilesToFeet

// Point2LL represents a 2D point on the Earth in latitude-longitude.
// Important: 0 (x) is longitude, 1 (y) is latitude
type Point2LL [2]float32

// lat and long should be 4-long slices, e.g.: [42 7 12.68 N]
func Point2LLFromComponents(lat []string, long []string) Point2LL {
	latitude := atof(lat[0]) + atof(lat[1])/60. + atof(lat[2])/3600.
	if lat[3] == "S" {
		latitude = -latitude
	}
	longitude := atof(long[0]) + atof(long[1])/60. + atof(long[2])/3600.
	if long[3] == "W" {
		longitude = -longitude
	}

	return Point2LL{float32(longitude), float32(latitude)}
}

func Point2LLFromStrings(lat, long string) Point2LL {
	return Point2LL{float32(atof(long)), float32(atof(lat))}
}

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
// N039.51.39.243, W075.16.29.511
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
		s += ", E"
	} else {
		s += ", W"
	}
	s += format(abs(p[0]))

	return s
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

func scale2ll(a Point2LL, s float32) Point2LL {
	return Point2LL(scale2f(a, s))
}

func lerp2ll(x float32, a Point2LL, b Point2LL) Point2LL {
	return Point2LL(lerp2f(x, a, b))
}

func length2ll(v Point2LL) float32 {
	return length2f(v)
}

// nmdistance2ll returns the distance in nautical miles between two
// provided lat-long coordinates.
func nmdistance2ll(a Point2LL, b Point2LL) float32 {
	dlat := (a[1] - b[1]) * database.NmPerLatitude
	dlong := (a[0] - b[0]) * database.NmPerLongitude
	return sqrt(sqr(dlat) + sqr(dlong))
}

// nmlength2ll returns the length of a vector expressed in lat-long
// coordinates.
func nmlength2ll(a Point2LL) float32 {
	x := a[0] * database.NmPerLongitude
	y := a[1] * database.NmPerLatitude
	return sqrt(sqr(x) + sqr(y))
}

// nm2ll converts a point expressed in nautical mile coordinates to
// lat-long.
func nm2ll(p [2]float32) Point2LL {
	return Point2LL{p[0] / database.NmPerLongitude, p[1] / database.NmPerLatitude}
}

// ll2nm converts a point expressed in latitude-longitude coordinates to
// nautical mile coordinates; this is useful for example for reasoning
// about distances, since both axes then have the same measure.
func ll2nm(p Point2LL) [2]float32 {
	return [2]float32{p[0] * database.NmPerLongitude, p[1] * database.NmPerLatitude}
}

func normalize2ll(a Point2LL) Point2LL {
	l := length2ll(a)
	if l == 0 {
		return Point2LL{0, 0}
	}
	return scale2ll(a, 1/l)
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

///////////////////////////////////////////////////////////////////////////
// generics

// FlattenMap takes a map and returns separate slices corresponding to the
// keys and values stored in the map.  (The slices are ordered so that the
// i'th key corresponds to the i'th value, needless to say.)
func FlattenMap[K comparable, V any](m map[K]V) ([]K, []V) {
	keys := make([]K, 0, len(m))
	values := make([]V, 0, len(m))
	for k, v := range m {
		keys = append(keys, k)
		values = append(values, v)
	}
	return keys, values
}

// SortedMapKeys returns the keys of the given map, sorted from low to high.
func SortedMapKeys[K constraints.Ordered, V any](m map[K]V) []K {
	keys, _ := FlattenMap(m)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// SortedMapKeysPred returns the keys of the given map sorted using the
// provided predicate function which should perform a "less than"
// comparison of key values.
func SortedMapKeysPred[K comparable, V any](m map[K]V, pred func(a *K, b *K) bool) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return pred(&keys[i], &keys[j]) })
	return keys
}

// DuplicateMap returns a newly-allocated map that stores copies of all of
// the values in the given map.
func DuplicateMap[K comparable, V any](m map[K]V) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		mnew[k] = v
	}
	return mnew
}

// FilterMap returns a newly-allocated result that is the result of
// applying the given predicate function to all of the elements in the
// given map and only including those for which the predicate returned
// true.
func FilterMap[K comparable, V any](m map[K]V, pred func(K, V) bool) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		if pred(k, v) {
			mnew[k] = v
		}
	}
	return mnew
}

// ReduceMap applies the provided reduction function to the given map,
// starting with the provided initial value.  The update rule applied is
// result=reduce(key, value, result), where the initial value of result is
// given by the initial parameter.
func ReduceMap[K comparable, V any, R any](m map[K]V, reduce func(K, V, R) R, initial R) R {
	result := initial
	for k, v := range m {
		result = reduce(k, v, result)
	}
	return result
}

// DuplicateSlice returns a newly-allocated slice that is a copy of the
// provided one.
func DuplicateSlice[V any](s []V) []V {
	dupe := make([]V, len(s))
	copy(dupe, s)
	return dupe
}

// DeleteSliceElement deletes the i'th element of the given slice,
// returning the resulting slice.  Note that the provided slice s is
// modified!
func DeleteSliceElement[V any](s []V, i int) []V {
	// First move any subsequent elements down one position.
	if i+1 < len(s) {
		copy(s[i:], s[i+1:])
	}
	// And drop the now-unnecessary final element.
	return s[:len(s)-1]
}

// SliceEqual checks whether two slices are equal.
func SliceEqual[V comparable](a []V, b []V) bool {
	if len(a) != len(b) {
		return false
	}
	for i, f := range a {
		if f != b[i] {
			return false
		}
	}
	return true
}

// MapSlice returns the slice that is the result of applying the provided
// xform function to all of the elements of the given slice.
func MapSlice[F, T any](from []F, xform func(F) T) []T {
	var to []T
	for _, item := range from {
		to = append(to, xform(item))
	}
	return to
}

// FilterSlice applies the given filter function pred to the given slice,
// returning a new slice that only contains elements where pred returned
// true.
func FilterSlice[V any](s []V, pred func(V) bool) []V {
	var filtered []V
	for _, item := range s {
		if pred(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// Find returns the index of the first instance of the given value in the
// slice or -1 if it is not present.
func Find[V comparable](s []V, value V) int {
	for i, v := range s {
		if v == value {
			return i
		}
	}
	return -1
}

///////////////////////////////////////////////////////////////////////////
// TransientMap

// TransientMap represents a set of objects with a built-in expiry time in
// the future; after an item's time passes, it is automatically removed
// from the set.
type TransientMap[K comparable, V any] struct {
	m map[K]valueTime[V]
}

type valueTime[V any] struct {
	v V
	t time.Time
}

func NewTransientMap[K comparable, V any]() *TransientMap[K, V] {
	return &TransientMap[K, V]{m: make(map[K]valueTime[V])}
}

func (t *TransientMap[K, V]) flush() {
	now := time.Now()
	for k, vt := range t.m {
		if now.After(vt.t) {
			delete(t.m, k)
		}
	}
}

// Add adds a given value to the set; it will no longer be there after the
// specified duration has passed.
func (t *TransientMap[K, V]) Add(key K, value V, d time.Duration) {
	t.m[key] = valueTime[V]{v: value, t: time.Now().Add(d)}
}

// Get looks up the given key in the map and returns its value and a
// Boolean that indicates whether it was found.
func (t *TransientMap[K, V]) Get(key K) (V, bool) {
	t.flush()
	vt, ok := t.m[key]
	return vt.v, ok
}

// Delete deletes the item in the map with the given key, if present.
func (t *TransientMap[K, V]) Delete(key K) {
	delete(t.m, key)
}

///////////////////////////////////////////////////////////////////////////
// RingBuffer

// RingBuffer represents an array of no more than a given maximum number of
// items.  Once it has filled, old items are discarded to make way for new
// ones.
type RingBuffer[V any] struct {
	entries []V
	max     int
	index   int
}

func NewRingBuffer[V any](capacity int) *RingBuffer[V] {
	return &RingBuffer[V]{max: capacity}
}

// Add adds all of the provided values to the ring buffer.
func (r *RingBuffer[V]) Add(values ...V) {
	for _, v := range values {
		if len(r.entries) < r.max {
			// Append to the entries slice if it hasn't yet hit the limit.
			r.entries = append(r.entries, v)
		} else {
			// Otherwise treat r.entries as a ring buffer where
			// (r.index+1)%r.max is the oldest entry and successive newer
			// entries follow.
			r.entries[r.index%r.max] = v
		}
		r.index++
	}
}

// Size returns the total number of items stored in the ring buffer.
func (r *RingBuffer[V]) Size() int {
	return min(len(r.entries), r.max)
}

// Get returns the specified element of the ring buffer where the index i
// is between 0 and Size()-1 and 0 is the oldest element in the buffer.
func (r *RingBuffer[V]) Get(i int) V {
	return r.entries[(r.index+i)%len(r.entries)]
}
