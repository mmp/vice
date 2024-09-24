// pkg/math/latlong.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"encoding/json"
	"fmt"
	gomath "math"
	"regexp"
	"strconv"
)

const NMPerLatitude = 60

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
		v -= Floor(v)
		v *= 60
		s += fmt.Sprintf(".%02d", int(v))
		v -= Floor(v)
		v *= 60
		s += fmt.Sprintf(".%02d", int(v))
		v -= Floor(v)
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
	s += format(Abs(p[1]))

	if p[0] > 0 {
		s += ",E"
	} else {
		s += ",W"
	}
	s += format(Abs(p[0]))

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
			sgn := Sign(float32(d))
			d = Abs(d)
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

func Add2LL(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(Add2f(a, b))
}

func Mid2LL(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(Mid2f(a, b))
}

func Sub2LL(a Point2LL, b Point2LL) Point2LL {
	return Point2LL(Sub2f(a, b))
}

// NMDistance2ll returns the distance in nautical miles between two
// provided lat-long coordinates.
func NMDistance2LL(a Point2LL, b Point2LL) float32 {
	// https://www.movable-type.co.uk/scripts/latlong.html
	const R = 6371000 // metres
	rad := func(d float64) float64 { return float64(d) / 180 * gomath.Pi }
	lat1, lon1 := rad(float64(a[1])), rad(float64(a[0]))
	lat2, lon2 := rad(float64(b[1])), rad(float64(b[0]))
	dlat, dlon := lat2-lat1, lon2-lon1

	x := Sqr(gomath.Sin(dlat/2)) + gomath.Cos(lat1)*gomath.Cos(lat2)*Sqr(gomath.Sin(dlon/2))
	c := 2 * gomath.Atan2(gomath.Sqrt(x), gomath.Sqrt(1-x))
	dm := R * c // in metres

	return float32(dm * 0.000539957)
}

// NMLength2ll returns the length of a vector expressed in lat-long
// coordinates.
func NMLength2LL(a Point2LL, nmPerLongitude float32) float32 {
	x := a[0] * nmPerLongitude
	y := a[1] * NMPerLatitude
	return Sqrt(Sqr(x) + Sqr(y))
}

// NM2LL converts a point expressed in nautical mile coordinates to
// lat-long.
func NM2LL(p [2]float32, nmPerLongitude float32) Point2LL {
	return Point2LL{p[0] / nmPerLongitude, p[1] / NMPerLatitude}
}

// LL2NM converts a point expressed in latitude-longitude coordinates to
// nautical mile coordinates; this is useful for example for reasoning
// about distances, since both axes then have the same measure.
func LL2NM(p Point2LL, nmPerLongitude float32) [2]float32 {
	return [2]float32{p[0] * nmPerLongitude, p[1] * NMPerLatitude}
}

// Offset2LL returns the point at distance dist along the vector with heading hdg from
// the given point. It assumes a (locally) flat earth.
func Offset2LL(pll Point2LL, hdg float32, dist float32, nmPerLongitude float32) Point2LL {
	p := LL2NM(pll, nmPerLongitude)
	h := Radians(float32(hdg))
	v := [2]float32{Sin(h), Cos(h)}
	v = Scale2f(v, float32(dist))
	p = Add2f(p, v)
	return NM2LL(p, nmPerLongitude)
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

		if locr == nil {
			panic("Must provide location resolver via SetLocationResolver")
		}

		*p, err = locr.Resolve(string(b))
		return err
	}
}

var locr LocationResolver

type LocationResolver interface {
	Resolve(s string) (Point2LL, error)
}

func SetLocationResolver(r LocationResolver) {
	locr = r
}
