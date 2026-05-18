// math/heading.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"fmt"
)

///////////////////////////////////////////////////////////////////////////
// headings and directions

type MagneticHeading float32
type TrueHeading float32

// TurnDirection specifies the direction of a turn.
type TurnDirection int

const (
	TurnClosest TurnDirection = iota // default: turn the shortest direction
	TurnLeft
	TurnRight
)

func MagneticToTrue(h MagneticHeading, magneticVariation float32) TrueHeading {
	return TrueHeading(NormalizeHeading(float32(h) - magneticVariation))
}

func TrueToMagnetic(h TrueHeading, magneticVariation float32) MagneticHeading {
	return MagneticHeading(NormalizeHeading(float32(h) + magneticVariation))
}

type CardinalOrdinalDirection int

type HeadingT interface{ ~int | ~float32 }

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

// Heading2LL returns the true heading from the point |from| to the point
// |to| in degrees. The provided points should be in latitude-longitude
// coordinates.
func Heading2LL(from Point2LL, to Point2LL, nmPerLongitude float32) TrueHeading {
	v := Point2LL{to[0] - from[0], to[1] - from[1]}
	angle := Degrees(Atan2(v[0]*nmPerLongitude, v[1]*NMPerLatitude))
	return TrueHeading(NormalizeHeading(angle))
}

func VectorHeading(v [2]float32) TrueHeading {
	// Note that atan2() normally measures w.r.t. the +x axis and angles
	// are positive for counter-clockwise. We want to measure w.r.t. +y and
	// to have positive angles be clockwise. Happily, swapping the order of
	// values passed to atan2()--passing (x,y), gives what we want.
	return TrueHeading(NormalizeHeading(Degrees(Atan2(v[0], v[1]))))
}

func HeadingVector(hdg TrueHeading) [2]float32 {
	return SinCos(Radians(hdg))
}

// HeadingDifference returns the minimum difference between two
// headings. (i.e., the result is always in the range [0,180].)
// The result is a plain angular delta, not a heading.
func HeadingDifference[T HeadingT](a, b T) float32 {
	var d T
	if a > b {
		d = a - b
	} else {
		d = b - a
	}
	if d > T(180) {
		d = 360 - d
	}
	return float32(d)
}

// HeadingSignedTurn returns the signed angular delta to turn from cur to target.
// Positive is a right turn, negative is a left turn.
func HeadingSignedTurn[T HeadingT](cur, target T) float32 {
	rot := NormalizeHeading(180 - target)
	return float32(180 - NormalizeHeading(cur+rot)) // w.r.t. 180 target
}

func HeadingInTurnArc[T HeadingT](from, heading, to T, turn TurnDirection) bool {
	switch turn {
	case TurnLeft:
		total := NormalizeHeading(from - to)
		delta := NormalizeHeading(from - heading)
		return delta > 0 && delta < total
	case TurnRight:
		total := NormalizeHeading(to - from)
		delta := NormalizeHeading(heading - from)
		return delta > 0 && delta < total
	case TurnClosest:
		total := HeadingSignedTurn(from, to)
		delta := HeadingSignedTurn(from, heading)
		if total > 0 {
			return delta > 0 && delta < total
		}
		if total < 0 {
			return delta < 0 && delta > total
		}
		return false
	default:
		return false
	}
}

// compass converts a heading expressed into degrees into a string
// corresponding to the closest compass direction.
func Compass[T HeadingT](heading T) string {
	h := NormalizeHeading(float32(heading) + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"North", "Northeast", "East", "Southeast",
		"South", "Southwest", "West", "Northwest"}[idx]
}

// ShortCompass converts a heading expressed in degrees into an abbreviated
// string corresponding to the closest compass direction.
func ShortCompass[T HeadingT](heading T) string {
	h := NormalizeHeading(float32(heading) + 22.5) // now [0,45] is north, etc...
	idx := int(h / 45)
	return [...]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}[idx]
}

// headingAsHour converts a heading expressed in degrees into the closest
// "o'clock" value, with an integer result in the range [1,12].
func HeadingAsHour[T HeadingT](heading T) int {
	heading = NormalizeHeading(heading - 15)
	// now [0,30] is 1 o'clock, etc
	return 1 + int(heading/30)
}

// Reduces it to [0,360).
func NormalizeHeading[T HeadingT](h T) T {
	if h < 0 {
		return 360 - NormalizeHeading(-h)
	} else {
		// Don't bother calling out to Mod(); assume that we'll only be
		// slightly out of range at worst.
		for h >= 360 {
			h -= 360
		}
		return h
	}
}

func OppositeHeading[T HeadingT](h T) T {
	return NormalizeHeading(h + 180)
}

// OffsetHeading adds an angular delta to a heading and normalizes the result, preserving the
// heading's type.
func OffsetHeading[T ~float32, D ~float32 | ~int](h T, delta D) T {
	return T(NormalizeHeading(float32(h) + float32(delta)))
}

// IsHeadingBetween checks if heading h is between h1 and h2 (clockwise from h1 to h2).
// Returns true if h is in the clockwise arc from h1 to h2, inclusive of both endpoints.
func IsHeadingBetween[T HeadingT](h, h1, h2 T) bool {
	h = NormalizeHeading(h)
	h1 = NormalizeHeading(h1)
	h2 = NormalizeHeading(h2)

	if h1 <= h2 {
		return h >= h1 && h <= h2
	} else {
		return h >= h1 || h <= h2
	}
}
