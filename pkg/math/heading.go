// pkg/math/heading.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"fmt"
)

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

// Heading2ll returns the heading from the point |from| to the point |to|
// in degrees.  The provided points should be in latitude-longitude
// coordinates and the provided magnetic correction is applied to the
// result.
func Heading2LL(from Point2LL, to Point2LL, nmPerLongitude float32, magCorrection float32) float32 {
	v := Point2LL{to[0] - from[0], to[1] - from[1]}

	// Note that atan2() normally measures w.r.t. the +x axis and angles
	// are positive for counter-clockwise. We want to measure w.r.t. +y and
	// to have positive angles be clockwise. Happily, swapping the order of
	// values passed to atan2()--passing (x,y), gives what we want.
	angle := Degrees(Atan2(v[0]*nmPerLongitude, v[1]*NMPerLatitude))
	return NormalizeHeading(angle + magCorrection)
}

// HeadingDifference returns the minimum difference between two
// headings. (i.e., the result is always in the range [0,180].)
func HeadingDifference(a float32, b float32) float32 {
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

// Figure out which way is closest: first find the angle to rotate the
// target heading by so that it's aligned with 180 degrees. This lets us
// not worry about the complexities of the wrap around at 0/360..
func HeadingSignedTurn(cur, target float32) float32 {
	rot := NormalizeHeading(180 - target)
	return 180 - NormalizeHeading(cur+rot) // w.r.t. 180 target
}

// compass converts a heading expressed into degrees into a string
// corresponding to the closest compass direction.
func Compass(heading float32) string {
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"North", "Northeast", "East", "Southeast",
		"South", "Southwest", "West", "Northwest"}[idx]
}

// ShortCompass converts a heading expressed in degrees into an abbreviated
// string corresponding to the closest compass direction.
func ShortCompass(heading float32) string {
	h := NormalizeHeading(heading + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[idx]
}

// headingAsHour converts a heading expressed in degrees into the closest
// "o'clock" value, with an integer result in the range [1,12].
func HeadingAsHour(heading float32) int {
	heading = NormalizeHeading(heading - 15)
	// now [0,30] is 1 o'clock, etc
	return 1 + int(heading/30)
}

// Reduces it to [0,360).
func NormalizeHeading(h float32) float32 {
	if h < 0 {
		return 360 - NormalizeHeading(-h)
	}
	return Mod(h, 360)
}

func OppositeHeading(h float32) float32 {
	return NormalizeHeading(h + 180)
}
