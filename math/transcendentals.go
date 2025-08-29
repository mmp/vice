// pkg/math/transcendentals.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// https://github.com/golang/go/issues/45915: "For graphics it was common a
// while ago to use tables instead of invoking a function every time you
// needed a Sin/Cos anyway, so having 32-bit versions would still not be the
// optimal answer"
// 🤯🤯🤯 for 1990s-era graphics expertise causing golang to have a fp64-only math library.
//
// So here we are, taking some advantage of the fact that we don't need to
// support the full domain with high accuracy if we have to go there
// ourselves.

package math

import (
	gomath "math"
)

func Sin(x float32) float32 {
	return SinCos(x)[0]
}

func Cos(x float32) float32 {
	return SinCos(x)[1]
}

// SinCos computes sin(x) and cos(x) simultaneously for a single float32 value
// Ported from syrah/FixedVectorMath.h:152, which is via Abramowitz and Stegun.
func SinCos(xFull float32) [2]float32 {
	const piOverTwo = float32(1.57079637050628662109375)
	const twoOverPi = float32(0.636619746685028076171875)

	scaled := xFull * twoOverPi
	kReal := float32(gomath.Floor(float64(scaled)))
	k := int(kReal)

	// Reduced range version of x
	x := xFull - kReal*piOverTwo
	kMod4 := k & 3
	cosUsecos := kMod4 == 0 || kMod4 == 2
	sinUsecos := kMod4 == 1 || kMod4 == 3
	sinFlipsign := kMod4 > 1
	cosFlipsign := kMod4 == 1 || kMod4 == 2

	const sinC2 = -0.16666667163372039794921875
	const sinC4 = 8.333347737789154052734375e-3
	const sinC6 = -1.9842604524455964565277099609375e-4
	const sinC8 = 2.760012648650445044040679931640625e-6
	const sinC10 = -2.50293279435709337121807038784027099609375e-8

	const cosC2 = -0.5
	const cosC4 = 4.166664183139801025390625e-2
	const cosC6 = -1.388833043165504932403564453125e-3
	const cosC8 = 2.47562347794882953166961669921875e-5
	const cosC10 = -2.59630184018533327616751194000244140625e-7

	x2 := x * x

	// Compute sin formula using Horner's method
	sinFormula := x2*sinC10 + sinC8
	sinFormula = x2*sinFormula + sinC6
	sinFormula = x2*sinFormula + sinC4
	sinFormula = x2*sinFormula + sinC2
	sinFormula = x2*sinFormula + 1
	sinFormula *= x

	// Compute cos formula using Horner's method
	cosFormula := x2*cosC10 + cosC8
	cosFormula = x2*cosFormula + cosC6
	cosFormula = x2*cosFormula + cosC4
	cosFormula = x2*cosFormula + cosC2
	cosFormula = x2*cosFormula + 1

	// Select appropriate formula for sin and cos
	var sin, cos float32
	if sinUsecos {
		sin = cosFormula
	} else {
		sin = sinFormula
	}

	if cosUsecos {
		cos = cosFormula
	} else {
		cos = sinFormula
	}

	// Apply sign flips
	if sinFlipsign {
		sin = -sin
	}

	if cosFlipsign {
		cos = -cos
	}

	return [2]float32{sin, cos}
}

// EvaluatePolynomial evaluates a polynomial using Horner's method
// The coefficients are provided from lowest degree to highest
func evaluatePolynomial(t float32, coeffs ...float32) float32 {
	if len(coeffs) == 0 {
		return 0
	}
	if len(coeffs) == 1 {
		return coeffs[0]
	}

	result := coeffs[len(coeffs)-1]
	for i := len(coeffs) - 2; i >= 0; i-- {
		result = t*result + coeffs[i]
	}
	return result
}

// FastExp computes e^x using a fast approximation Based on
// https://stackoverflow.com/a/10792321, translated from pbrt-v4's FastExp.
func FastExp(x float32) float32 {
	// Compute x' such that e^x = 2^{x'}
	xp := x * 1.442695041

	// Find integer and fractional components of x'
	fxp := Floor(xp)
	f := xp - fxp
	i := int(fxp)

	// Evaluate polynomial approximation of 2^f
	twoToF := evaluatePolynomial(f, 1.0, 0.695556856, 0.226173572, 0.0781455737)

	// Scale 2^f by 2^i and return final result
	exponent := Exponent(twoToF) + i
	if exponent < -126 {
		return 0
	}
	if exponent > 127 {
		return Infinity
	}

	bits := FloatToBits(twoToF)
	bits &= 0b10000000011111111111111111111111
	bits |= uint32(exponent+127) << 23
	return BitsToFloat(bits)
}

func SafeASin(a float32) float32 {
	return float32(gomath.Asin(float64(Clamp(a, -1, 1))))
}

func SafeACos(a float32) float32 {
	return float32(gomath.Acos(float64(Clamp(a, -1, 1))))
}

// Tan computes tan(x) for a single float32 value, via syrah/FixedVectorMath.h:206
func Tan(xFull float32) float32 {
	// Handle negative x
	xLt0 := xFull < 0
	var y float32
	if xLt0 {
		y = -xFull
	} else {
		y = xFull
	}

	scaled := y * FourOverPi
	kReal := Floor(scaled)
	k := int(kReal)

	x := y - kReal*PiOver4

	// if k & 1, x -= Pi/4
	if (k & 1) != 0 {
		x -= PiOver4
	}

	// if k & 3 == (0 or 3) let z = tan_In...(y) otherwise z = -cot_In0To...
	kMod4 := k & 3
	useCotan := kMod4 == 1 || kMod4 == 2

	const tanC2 = 0.33333075046539306640625
	const tanC4 = 0.13339905440807342529296875
	const tanC6 = 5.3348250687122344970703125e-2
	const tanC8 = 2.46033705770969390869140625e-2
	const tanC10 = 2.892402000725269317626953125e-3
	const tanC12 = 9.500005282461643218994140625e-3

	const cotC2 = -0.3333333432674407958984375
	const cotC4 = -2.222204394638538360595703125e-2
	const cotC6 = -2.11752182804048061370849609375e-3
	const cotC8 = -2.0846328698098659515380859375e-4
	const cotC10 = -2.548247357481159269809722900390625e-5
	const cotC12 = -3.5257363606433500535786151885986328125e-7

	x2 := x * x
	var z float32

	if useCotan {
		// Compute cotangent using Horner's method
		cotVal := x2*cotC12 + cotC10
		cotVal = x2*cotVal + cotC8
		cotVal = x2*cotVal + cotC6
		cotVal = x2*cotVal + cotC4
		cotVal = x2*cotVal + cotC2
		cotVal = x2*cotVal + 1
		// The equation is for x * cot(x) but we need -x * cot(x) for the tan part.
		cotVal /= -x
		z = cotVal
	} else {
		// Compute tangent using Horner's method
		tanVal := x2*tanC12 + tanC10
		tanVal = x2*tanVal + tanC8
		tanVal = x2*tanVal + tanC6
		tanVal = x2*tanVal + tanC4
		tanVal = x2*tanVal + tanC2
		tanVal = x2*tanVal + 1
		// Equation was for tan(x)/x
		tanVal *= x
		z = tanVal
	}

	// Apply sign flip for negative input
	if xLt0 {
		return -z
	}
	return z
}

// Atan computes atan(x) for a single float32 value
// Ported from syrah/FixedVectorMath.h:289
func Atan(xFull float32) float32 {
	// atan(-x) = -atan(x) (so flip from negative to positive first)
	// if x > 1 -> atan(x) = Pi/2 - atan(1/x)
	xNeg := xFull < 0
	var xFlipped float32
	if xNeg {
		xFlipped = -xFull
	} else {
		xFlipped = xFull
	}

	xGt1 := xFlipped > 1.0
	var x float32
	if xGt1 {
		x = 1.0 / xFlipped
	} else {
		x = xFlipped
	}

	// These coefficients approximate atan(x)/x
	const atanC0 = 0.99999988079071044921875
	const atanC2 = -0.3333191573619842529296875
	const atanC4 = 0.199689209461212158203125
	const atanC6 = -0.14015688002109527587890625
	const atanC8 = 9.905083477497100830078125e-2
	const atanC10 = -5.93664981424808502197265625e-2
	const atanC12 = 2.417283318936824798583984375e-2
	const atanC14 = -4.6721356920897960662841796875e-3

	x2 := x * x
	result := x2*atanC14 + atanC12
	result = x2*result + atanC10
	result = x2*result + atanC8
	result = x2*result + atanC6
	result = x2*result + atanC4
	result = x2*result + atanC2
	result = x2*result + atanC0
	result *= x

	if xGt1 {
		result = PiOver2 - result
	}

	if xNeg {
		result = -result
	}

	return result
}

// Atan2 computes atan2(y, x) for single float32 values
// Ported from syrah/FixedVectorMath.h:327
func Atan2(y, x float32) float32 {
	// atan2(y, x) =
	//
	// atan2(y > 0, x = +-0) ->  Pi/2
	// atan2(y < 0, x = +-0) -> -Pi/2
	// atan2(y = +-0, x < +0) -> +-Pi
	// atan2(y = +-0, x >= +0) -> +-0
	//
	// atan2(y >= 0, x < 0) ->  Pi + atan(y/x)
	// atan2(y <  0, x < 0) -> -Pi + atan(y/x)
	// atan2(y, x > 0) -> atan(y/x)

	// Handle special cases for x = 0
	if x == 0 {
		if y > 0 {
			return PiOver2
		} else if y < 0 {
			return -PiOver2
		} else {
			// y = 0, x = 0 is technically undefined, but return 0
			return 0
		}
	}

	// Handle special cases for y = 0
	if y == 0 {
		if x < 0 {
			if SignBit(y) {
				return -Pi
			}
			return Pi
		}
		return y // preserves sign of zero
	}

	yOverX := y / x
	atanArg := Atan(yOverX)

	var offset float32
	if x < 0 {
		if y < 0 {
			offset = -Pi
		} else {
			offset = Pi
		}
	} else {
		offset = 0
	}

	return offset + atanArg
}
