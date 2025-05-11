// pkg/math/core.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	gomath "math"

	"golang.org/x/exp/constraints"
)

// Degrees converts an angle expressed in degrees to radians
func Degrees(r float32) float32 {
	return r * 180 / gomath.Pi
}

// Radians converts an angle expressed in radians to degrees
func Radians(d float32) float32 {
	return d / 180 * gomath.Pi
}

func Pi() float32 {
	return float32(gomath.Pi)
}

// A number of utility functions for evaluating transcendentals and the like follow;
// since we mostly use float32, it's handy to be able to call these directly rather than
// with all of the casts that are required when using the math package.

func Sin(a float32) float32 {
	return float32(gomath.Sin(float64(a)))
}

func SafeASin(a float32) float32 {
	return float32(gomath.Asin(float64(Clamp(a, -1, 1))))
}

func SafeACos(a float32) float32 {
	return float32(gomath.Acos(float64(Clamp(a, -1, 1))))
}

func Cos(a float32) float32 {
	return float32(gomath.Cos(float64(a)))
}

func Tan(a float32) float32 {
	return float32(gomath.Tan(float64(a)))
}

func Atan2(y, x float32) float32 {
	return float32(gomath.Atan2(float64(y), float64(x)))
}

func Sqrt(a float32) float32 {
	return float32(gomath.Sqrt(float64(a)))
}

func Mod(a, b float32) float32 {
	return float32(gomath.Mod(float64(a), float64(b)))
}

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

func Abs[V constraints.Integer | constraints.Float](x V) V {
	if x < 0 {
		return -x
	}
	return x
}

func Min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func Max[T constraints.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Pow(a, b float32) float32 {
	return float32(gomath.Pow(float64(a), float64(b)))
}

func Exp(x float32) float32 {
	return float32(gomath.Exp(float64(x)))
}

func Sqr[V constraints.Integer | constraints.Float](v V) V { return v * v }

func Clamp[T constraints.Ordered](x T, low T, high T) T {
	if x < low {
		return low
	} else if x > high {
		return high
	}
	return x
}

func Lerp(x, a, b float32) float32 {
	return (1-x)*a + x*b
}

// greatest common divisor
func GCD(a, b int) int {
	for b != 0 {
		t := b
		b = a % b
		a = t
	}
	return a
}

// least common multiple
func LCM(a, b int) int {
	return a / GCD(a, b) * b
}
