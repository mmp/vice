// pkg/math/core.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"math"
	gomath "math"

	"golang.org/x/exp/constraints"
)

// Mathematical Constants
const (
	Pi         = gomath.Pi
	InvPi      = 0.31830988618379067154
	Inv2Pi     = 0.15915494309189533577
	Inv4Pi     = 0.07957747154594766788
	PiOver2    = 1.57079632679489661923
	PiOver4    = 0.78539816339744830961
	FourOverPi = 1.27323949337005615234375
	Sqrt2      = 1.41421356237309504880
)

var Infinity float32 = float32(math.Inf(1))

// FloatToBits converts a float32 to its bit representation
func FloatToBits(f float32) uint32 {
	return math.Float32bits(f)
}

// BitsToFloat converts a bit representation to float32
func BitsToFloat(ui uint32) float32 {
	return math.Float32frombits(ui)
}

// Exponent returns the exponent of a float32
func Exponent(v float32) int {
	return int(FloatToBits(v)>>23) - 127
}

// Significand returns the significand of a float32
func Significand(v float32) int {
	return int(FloatToBits(v) & ((1 << 23) - 1))
}

// Degrees converts an angle expressed in degrees to radians
func Degrees(r float32) float32 {
	return r * 180 / Pi
}

// Radians converts an angle expressed in radians to degrees
func Radians(d float32) float32 {
	return d / 180 * Pi
}

func Sqrt(a float32) float32 {
	return float32(gomath.Sqrt(float64(a)))
}

func Mod(a, b float32) float32 {
	return float32(gomath.Mod(float64(a), float64(b)))
}

// Sign returns 1 if v > 0, -1 if v < 0, or 0 if v == 0
func Sign(v float32) float32 {
	if v > 0 {
		return 1
	} else if v < 0 {
		return -1
	}
	return 0
}

func Floor(v float32) float32 {
	return float32(gomath.Floor(float64(v)))
}

func Ceil(v float32) float32 {
	return float32(gomath.Ceil(float64(v)))
}

// Abs returns the absolute value of x
func Abs[V constraints.Integer | constraints.Float](x V) V {
	if x < 0 {
		return -x
	}
	return x
}

func Pow(a, b float32) float32 {
	return float32(gomath.Pow(float64(a), float64(b)))
}

func Sqr[V constraints.Integer | constraints.Float](v V) V { return v * v }

// Clamp restricts x to the range [low, high]
func Clamp[T constraints.Ordered](x T, low T, high T) T {
	if x < low {
		return low
	} else if x > high {
		return high
	}
	return x
}

// Lerp performs linear interpolation between a and b using factor x in [0,1]
func Lerp(x, a, b float32) float32 {
	return (1-x)*a + x*b
}

// GCD calculates the greatest common divisor of a and b
func GCD(a, b int) int {
	for b != 0 {
		t := b
		b = a % b
		a = t
	}
	return a
}

// LCM calculates the least common multiple of a and b
func LCM(a, b int) int {
	return a / GCD(a, b) * b
}
