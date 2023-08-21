// util.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	_ "embed"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/exp/constraints"
	"image"
	"image/color"
	"image/draw"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/MichaelTJones/pcg"
	"github.com/klauspost/compress/zstd"
)

const nmPerLatitude = 60

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

func isAllNumbers(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

var (
	//go:embed resources/nouns.txt
	nounsFile string
	nounList  []string

	//go:embed resources/adjectives.txt
	adjectivesFile string
	adjectiveList  []string
)

func getRandomAdjectiveNoun() string {
	if nounList == nil {
		nounList = strings.Split(nounsFile, "\n")
	}
	if adjectiveList == nil {
		adjectiveList = strings.Split(adjectivesFile, "\n")
	}

	return adjectiveList[rand.Intn(len(adjectiveList))] + "-" +
		nounList[rand.Intn(len(nounList))]
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

func ParseCardinalOrdinalDirection(s string) (CardinalOrdinalDirection, error) {
	switch s {
	case "N":
		return North, nil
	case "NE":
		return NorthEast, nil
	case "E":
		return East, nil
	case "SE":
		return SouthEast, nil
	case "S":
		return South, nil
	case "SW":
		return SouthWest, nil
	case "W":
		return West, nil
	case "NW":
		return NorthWest, nil
	}

	return CardinalOrdinalDirection(0), fmt.Errorf("invalid direction")
}

func nmPerLongitude(p Point2LL) float32 {
	return 45
	// WANT: return 60 * sin(radians(p[1]))
}

// headingp2ll returns the heading from the point |from| to the point |to|
// in degrees.  The provided points should be in latitude-longitude
// coordinates and the provided magnetic correction is applied to the
// result.
func headingp2ll(from Point2LL, to Point2LL, nmPerLongitude float32, magCorrection float32) float32 {
	v := Point2LL{to[0] - from[0], to[1] - from[1]}

	// Note that atan2() normally measures w.r.t. the +x axis and angles
	// are positive for counter-clockwise. We want to measure w.r.t. +y and
	// to have positive angles be clockwise. Happily, swapping the order of
	// values passed to atan2()--passing (x,y), gives what we want.
	angle := degrees(atan2(v[0]*nmPerLongitude, v[1]*nmPerLatitude))
	return NormalizeHeading(angle + magCorrection)
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
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"North", "Northeast", "East", "Southeast",
		"South", "Southwest", "West", "Northwest"}[idx]
}

// shortCompass converts a heading expressed in degrees into an abbreviated
// string corresponding to the closest compass direction.
func shortCompass(heading float32) string {
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[idx]
}

// headingAsHour converts a heading expressed in degrees into the closest
// "o'clock" value, with an integer result in the range [1,12].
func headingAsHour(heading float32) int {
	heading = NormalizeHeading(heading - 15)
	// now [0,30] is 1 o'clock, etc
	return 1 + int(heading/30)
}

// Reduces it to [0,360).
func NormalizeHeading(h float32) float32 {
	if h < 0 {
		return 360 - NormalizeHeading(-h)
	}
	return mod(h, 360)
}

func OppositeHeading(h float32) float32 {
	return NormalizeHeading(h + 180)
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
// generics

func Select[T any](sel bool, a, b T) T {
	if sel {
		return a
	} else {
		return b
	}
}

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

// ReduceSlice applies the provided reduction function to the given slice,
// starting with the provided initial value.  The update rule applied is
// result=reduce( value, result), where the initial value of result is
// given by the initial parameter.
func ReduceSlice[V any, R any](s []V, reduce func(V, R) R, initial R) R {
	result := initial
	for _, v := range s {
		result = reduce(v, result)
	}
	return result
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

// InsertSliceElement inserts the given value v at the index i in the slice
// s, moving all elements after i one place forward.
func InsertSliceElement[V any](s []V, i int, v V) []V {
	s = append(s, v) // just to grow the slice (unless i == len(s))
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
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

// Find returns the index of the first instance of the given value in the
// slice or -1 if it is not present.
func FindIf[V any](s []V, pred func(V) bool) int {
	for i, v := range s {
		if pred(v) {
			return i
		}
	}
	return -1
}

// AnySlice applies the given predicate to each element of the provided
// slice in sequence and returns true after the first element where the
// predicate returns true.  False is returned if the predicate always
// evaluates to false.
func AnySlice[V any](s []V, pred func(V) bool) bool {
	for _, v := range s {
		if pred(v) {
			return true
		}
	}
	return false
}

// Sample uniformly randomly samples an element of a non-empty slice.
func Sample[T any](slice []T) T {
	return slice[rand.Intn(len(slice))]
}

// SampleFiltered uniformly randomly samples a slice, returning the index
// of the sampled item, using provided predicate function to filter the
// items that may be sampled.  An index of -1 is returned if the slice is
// empty or the predicate returns false for all items.
func SampleFiltered[T any](slice []T, pred func(T) bool) int {
	idx := -1
	candidates := 0
	for i, v := range slice {
		if pred(v) {
			candidates++
			p := float32(1) / float32(candidates)
			if rand.Float32() < p {
				idx = i
			}
		}
	}
	return idx
}

// SampleWeighted randomly samples an element from the given slice with the
// probability of choosing each element proportional to the value returned
// by the provided callback.
func SampleWeighted[T any](slice []T, weight func(T) int) int {
	// Weighted reservoir sampling...
	idx := -1
	sumWt := 0
	for i, v := range slice {
		w := weight(v)
		if w == 0 {
			continue
		}

		sumWt += w
		p := float32(w) / float32(sumWt)
		if rand.Float32() < p {
			idx = i
		}
	}
	return idx
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

///////////////////////////////////////////////////////////////////////////
// Networking miscellany

func FetchURL(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var text []byte
	if text, err = io.ReadAll(response.Body); err != nil {
		return nil, err
	}

	return text, nil
}

///////////////////////////////////////////////////////////////////////////
// Image processing

func GenerateImagePyramid(img image.Image) []image.Image {
	var pyramid []image.Image

	// We always work with image.RGBA in the following..
	nx, ny := img.Bounds().Dx(), img.Bounds().Dy()
	prevLevel, ok := img.(*image.RGBA)
	if !ok {
		prevLevel = image.NewRGBA(image.Rect(0, 0, nx, ny))
		draw.Draw(prevLevel, prevLevel.Bounds(), img, img.Bounds().Min, draw.Src)
	}
	pyramid = append(pyramid, prevLevel)

	for nx != 1 || ny != 1 {
		ox, oy := nx, ny
		nx, ny = max(nx/2, 1), max(ny/2, 1)

		next := make([]uint8, nx*ny*4)
		lookup := func(x, y int) color.RGBA {
			if x > ox-1 {
				x = ox - 1
			}
			if y > oy-1 {
				y = oy - 1
			}
			offset := 4*x + prevLevel.Stride*y
			return color.RGBA{
				R: prevLevel.Pix[offset],
				G: prevLevel.Pix[offset+1],
				B: prevLevel.Pix[offset+2],
				A: prevLevel.Pix[offset+3]}
		}
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				v := [4]color.RGBA{lookup(2*x, 2*y), lookup(2*x+1, 2*y), lookup(2*x, 2*y+1), lookup(2*x+1, 2*y+1)}

				// living large with a box filter
				next[4*(x+y*nx)+0] = uint8((int(v[0].R) + int(v[1].R) + int(v[2].R) + int(v[3].R) + 2) / 4)
				next[4*(x+y*nx)+1] = uint8((int(v[0].G) + int(v[1].G) + int(v[2].G) + int(v[3].G) + 2) / 4)
				next[4*(x+y*nx)+2] = uint8((int(v[0].B) + int(v[1].B) + int(v[2].B) + int(v[3].B) + 2) / 4)
				next[4*(x+y*nx)+3] = uint8((int(v[0].A) + int(v[1].A) + int(v[2].A) + int(v[3].A) + 2) / 4)
			}
		}

		nextLevel := &image.RGBA{
			Pix:    next,
			Stride: 4 * nx,
			Rect:   image.Rectangle{Max: image.Point{X: nx, Y: ny}}}
		pyramid = append(pyramid, nextLevel)
		prevLevel = nextLevel
	}

	return pyramid
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

///////////////////////////////////////////////////////////////////////////
// JSON

// Unmarshal the bytes into the given type but go through some efforts to
// return useful error messages when the JSON is invalid...
func UnmarshalJSON[T any](b []byte, out *T) error {
	err := json.Unmarshal(b, out)
	if err == nil {
		return nil
	}

	decodeOffset := func(offset int64) (line, char int) {
		line, char = 1, 1
		for i := 0; i < int(offset) && i < len(b); i++ {
			if b[i] == '\n' {
				line++
				char = 1
			} else {
				char++
			}
		}
		return
	}

	switch jerr := err.(type) {
	case *json.SyntaxError:
		line, char := decodeOffset(jerr.Offset)
		return fmt.Errorf("Error at line %d, character %d: %v", line, char, jerr)

	case *json.UnmarshalTypeError:
		line, char := decodeOffset(jerr.Offset)
		return fmt.Errorf("Error at line %d, character %d: %s value for %s.%s invalid for type %s",
			line, char, jerr.Value, jerr.Struct, jerr.Field, jerr.Type.String())

	default:
		return err
	}
}

///////////////////////////////////////////////////////////////////////////

// ErrorLogger is a small utility class used to log errors when validating
// the parsed JSON scenarios. It tracks context about what is currently
// being validated and accumulates multiple errors, making it possible to
// log errors while still continuing validation.
type ErrorLogger struct {
	// Tracked via Push()/Pop() calls to remember what we're looking at if
	// an error is found.
	hierarchy []string
	// Actual error messages to report.
	errors []string
}

func (e *ErrorLogger) Push(s string) {
	e.hierarchy = append(e.hierarchy, s)
}

func (e *ErrorLogger) Pop() {
	e.hierarchy = e.hierarchy[:len(e.hierarchy)-1]
}

func (e *ErrorLogger) ErrorString(s string, args ...interface{}) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+fmt.Sprintf(s, args...))
}

func (e *ErrorLogger) Error(err error) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+err.Error())
}

func (e *ErrorLogger) HaveErrors() bool {
	return len(e.errors) > 0
}

func (e *ErrorLogger) PrintErrors() {
	for _, err := range e.errors {
		fmt.Fprintln(os.Stderr, err)
	}
}

///////////////////////////////////////////////////////////////////////////

func CheckJSONVsSchema[T any](contents []byte, e *ErrorLogger) {
	var items interface{}
	if err := UnmarshalJSON(contents, &items); err != nil {
		e.Error(err)
		return
	}

	var t T
	ty := reflect.TypeOf(t)
	checkJSONVsSchemaRecursive(items, ty, e)
}

func checkJSONVsSchemaRecursive(json interface{}, ty reflect.Type, e *ErrorLogger) {
	for ty.Kind() == reflect.Ptr {
		ty = ty.Elem()
	}

	switch ty.Kind() {
	case reflect.Array, reflect.Slice:
		if array, ok := json.([]interface{}); ok {
			for _, item := range array {
				checkJSONVsSchemaRecursive(item, ty.Elem(), e)
			}
		} else if _, ok := json.(string); ok {
			// Some things (e.g., WaypointArray, Point2LL) are array/slice
			// types but are JSON encoded as strings. We'll treat a string
			// value for an array/slice as ok as far as validation here.
		} else {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}

	case reflect.Map:
		if m, ok := json.(map[string]interface{}); ok {
			for k, v := range m {
				e.Push(k)
				checkJSONVsSchemaRecursive(v, ty.Elem(), e)
				e.Pop()
			}
		} else {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		}

	case reflect.Struct:
		if items, ok := json.(map[string]interface{}); !ok {
			e.ErrorString("unexpected data format provided for object: %s",
				reflect.TypeOf(json))
		} else {
			for item, values := range items {
				found := false
				for _, field := range reflect.VisibleFields(ty) {
					if j, ok := field.Tag.Lookup("json"); ok {
						for _, jf := range strings.Split(j, ",") {
							if item == jf {
								found = true
								e.Push(jf)
								checkJSONVsSchemaRecursive(values, field.Type, e)
								e.Pop()
								break
							}
						}
					}
				}
				if !found {
					e.ErrorString("The entry \"" + item + "\" is not an expected JSON object. Is it misspelled?")
				}
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// RPC/Networking stuff

// Straight out of net/rpc/server.go
type gobServerCodec struct {
	rwc    io.ReadWriteCloser
	dec    *gob.Decoder
	enc    *gob.Encoder
	encBuf *bufio.Writer
	closed bool
}

func (c *gobServerCodec) ReadRequestHeader(r *rpc.Request) error {
	return c.dec.Decode(r)
}

func (c *gobServerCodec) ReadRequestBody(body any) error {
	return c.dec.Decode(body)
}

func (c *gobServerCodec) WriteResponse(r *rpc.Response, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		if c.encBuf.Flush() == nil {
			// Gob couldn't encode the header. Should not happen, so if it does,
			// shut down the connection to signal that the connection is broken.
			lg.Printf("rpc: gob error encoding response: %v", err)
			c.Close()
		}
		return
	}
	if err = c.enc.Encode(body); err != nil {
		if c.encBuf.Flush() == nil {
			// Was a gob problem encoding the body but the header has been written.
			// Shut down the connection to signal that the connection is broken.
			lg.Printf("rpc: gob error encoding body: %v", err)
			c.Close()
		}
		return
	}
	return c.encBuf.Flush()
}

func (c *gobServerCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.rwc.Close()
}

func MakeGOBServerCodec(conn io.ReadWriteCloser) rpc.ServerCodec {
	buf := bufio.NewWriter(conn)
	return &gobServerCodec{
		rwc:    conn,
		dec:    gob.NewDecoder(conn),
		enc:    gob.NewEncoder(buf),
		encBuf: buf,
	}
}

type LoggingServerCodec struct {
	rpc.ServerCodec
	label string
}

func MakeLoggingServerCodec(label string, c rpc.ServerCodec) *LoggingServerCodec {
	return &LoggingServerCodec{ServerCodec: c, label: label}
}

func (c *LoggingServerCodec) ReadRequestHeader(r *rpc.Request) error {
	err := c.ServerCodec.ReadRequestHeader(r)
	lg.Printf("%s: RPC server receive request %s -> %v", c.label, r.ServiceMethod, err)
	return err
}

func (c *LoggingServerCodec) WriteResponse(r *rpc.Response, body any) error {
	err := c.ServerCodec.WriteResponse(r, body)
	lg.Printf("%s: RPC server send response %s -> %v", c.label, r.ServiceMethod, err)
	return err
}

// This from net/rpc/client.go...
type gobClientCodec struct {
	rwc    io.ReadWriteCloser
	dec    *gob.Decoder
	enc    *gob.Encoder
	encBuf *bufio.Writer
}

func (c *gobClientCodec) WriteRequest(r *rpc.Request, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		return
	}
	if err = c.enc.Encode(body); err != nil {
		return
	}
	return c.encBuf.Flush()
}

func (c *gobClientCodec) ReadResponseHeader(r *rpc.Response) error {
	return c.dec.Decode(r)
}

func (c *gobClientCodec) ReadResponseBody(body any) error {
	return c.dec.Decode(body)
}

func (c *gobClientCodec) Close() error {
	return c.rwc.Close()
}

func MakeGOBClientCodec(conn io.ReadWriteCloser) rpc.ClientCodec {
	encBuf := bufio.NewWriter(conn)
	return &gobClientCodec{conn, gob.NewDecoder(conn), gob.NewEncoder(encBuf), encBuf}
}

type LoggingClientCodec struct {
	rpc.ClientCodec
	label string
}

func MakeLoggingClientCodec(label string, c rpc.ClientCodec) *LoggingClientCodec {
	return &LoggingClientCodec{ClientCodec: c, label: label}
}

func (c *LoggingClientCodec) WriteRequest(r *rpc.Request, v any) error {
	err := c.ClientCodec.WriteRequest(r, v)
	lg.Printf("%s: RPC client send request %s -> %v", c.label, r.ServiceMethod, err)
	return err
}

func (c *LoggingClientCodec) ReadResponseHeader(r *rpc.Response) error {
	err := c.ClientCodec.ReadResponseHeader(r)
	lg.Printf("%s: RPC client receive response %s -> %v", c.label, r.ServiceMethod, err)
	return err
}

type CompressedConn struct {
	net.Conn
	r *zstd.Decoder
	w *zstd.Encoder
}

func MakeCompressedConn(c net.Conn) (*CompressedConn, error) {
	cc := &CompressedConn{Conn: c}
	var err error
	if cc.r, err = zstd.NewReader(c); err != nil {
		return nil, err
	}
	if cc.w, err = zstd.NewWriter(c); err != nil {
		return nil, err
	}
	return cc, nil
}

func (c *CompressedConn) Read(b []byte) (n int, err error) {
	n, err = c.r.Read(b)
	return
}

func (c *CompressedConn) Write(b []byte) (n int, err error) {
	n, err = c.w.Write(b)
	c.w.Flush()
	return
}

var RXTotal, TXTotal int64

type LoggingConn struct {
	net.Conn
	sent, received int
	mu             sync.Mutex
	start          time.Time
	lastReport     time.Time
}

func MakeLoggingConn(c net.Conn) *LoggingConn {
	return &LoggingConn{
		Conn:       c,
		start:      time.Now(),
		lastReport: time.Now(),
	}
}

func GetLoggedRPCBandwidth() (int64, int64) {
	return RXTotal, TXTotal
}

func (c *LoggingConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.received += n
	atomic.AddInt64(&RXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent += n
	atomic.AddInt64(&TXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) maybeReport() {
	if time.Since(c.lastReport) > 1*time.Minute {
		min := time.Since(c.start).Minutes()
		lg.Printf("%s: %d bytes read (%d/minute), %d bytes written (%d/minute)",
			c.Conn.RemoteAddr(), c.received, int(float64(c.received)/min),
			c.sent, int(float64(c.sent)/min))
		c.lastReport = time.Now()
	}
}

func isRPCServerError(err error) bool {
	_, ok := err.(rpc.ServerError)
	return ok || errors.Is(err, rpc.ErrShutdown)
}

type RPCClient struct {
	*rpc.Client
}

func (c *RPCClient) CallWithTimeout(serviceMethod string, args any, reply any) error {
	pc := &PendingCall{
		Call:      c.Go(serviceMethod, args, reply, nil),
		IssueTime: time.Now(),
	}

	select {
	case <-pc.Call.Done:
		return pc.Call.Error

	case <-time.After(5 * time.Second):
		return ErrRPCTimeout
	}
}

type PendingCall struct {
	Call                *rpc.Call
	IssueTime           time.Time
	OnSuccess           func(any)
	OnErr               func(error)
	haveWarnedNoUpdates bool
}

func (p *PendingCall) CheckFinished(eventStream *EventStream) bool {
	select {
	case c := <-p.Call.Done:
		if c.Error != nil {
			if p.OnErr != nil {
				p.OnErr(c.Error)
			} else {
				lg.Errorf("%v", c.Error)
			}
		} else {
			if p.haveWarnedNoUpdates {
				p.haveWarnedNoUpdates = false
				if eventStream != nil {
					eventStream.Post(Event{
						Type:    StatusMessageEvent,
						Message: "Server connection reestablished!",
					})
				} else {
					lg.Errorf("Server connection reesablished")
				}
			}
			if p.OnSuccess != nil {
				p.OnSuccess(c.Reply)
			}
		}
		return true

	default:
		if s := time.Since(p.IssueTime); s > 5*time.Second {
			p.haveWarnedNoUpdates = true
			if eventStream != nil {
				eventStream.Post(Event{
					Type:    StatusMessageEvent,
					Message: "No updates from server in over 5 seconds. Network may have disconnected.",
				})
			} else {
				lg.Errorf("No updates from server in over 5 seconds. Network may have disconnected.")
			}
		}
		return false
	}
}

///////////////////////////////////////////////////////////////////////////

func getResourcesFS() fs.StatFS {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}

	dir := filepath.Dir(path)
	if runtime.GOOS == "darwin" {
		dir = filepath.Clean(filepath.Join(dir, "..", "Resources"))
	} else {
		dir = filepath.Join(dir, "resources")
	}

	fsys, ok := os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	check := func(fs fs.StatFS) bool {
		_, errv := fsys.Stat("videomaps")
		_, errs := fsys.Stat("scenarios")
		return errv == nil && errs == nil
	}

	if check(fsys) {
		lg.Printf("%s: resources directory", dir)
		return fsys
	}

	// Try CWD (this is useful for development and debugging but shouldn't
	// be needed for release builds.
	lg.Printf("Trying CWD for resources FS")

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	dir = filepath.Join(wd, "resources")

	fsys, ok = os.DirFS(dir).(fs.StatFS)
	if !ok {
		panic("FS from DirFS is not a StatFS?")
	}

	if check(fsys) {
		return fsys
	}
	panic("unable to find videomaps in CWD")
}

// LoadResource loads the specified file from the resources directory, decompressing it if
// it is zstd compressed. It panics if the file is not found; missing resources are pretty
// much impossible to recover from.
func LoadResource(path string) []byte {
	b, err := fs.ReadFile(resourcesFS, path)
	if err != nil {
		panic(err)
	}

	if filepath.Ext(path) == ".zst" {
		return []byte(decompressZstd(string(b)))
	}

	return b
}
