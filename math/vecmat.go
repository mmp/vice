// pkg/math/vecmat.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import gomath "math"

///////////////////////////////////////////////////////////////////////////
// point 2f

// Various useful functions for arithmetic with 2D points/vectors.
// Names are brief in order to avoid clutter when they're used.

// a+b
func Add2f(a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{a[0] + b[0], a[1] + b[1]}
}

// midpoint of a and b
func Mid2f(a [2]float32, b [2]float32) [2]float32 {
	return Scale2f(Add2f(a, b), 0.5)
}

// a-b
func Sub2f(a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{a[0] - b[0], a[1] - b[1]}
}

// a*s
func Scale2f(a [2]float32, s float32) [2]float32 {
	return [2]float32{s * a[0], s * a[1]}
}

func Dot(a, b [2]float32) float32 {
	return a[0]*b[0] + a[1]*b[1]
}

// Linearly interpolate x of the way between a and b. x==0 corresponds to
// a, x==1 corresponds to b, etc.
func Lerp2f(x float32, a [2]float32, b [2]float32) [2]float32 {
	return [2]float32{(1-x)*a[0] + x*b[0], (1-x)*a[1] + x*b[1]}
}

// Length of v
func Length2f(v [2]float32) float32 {
	return Sqrt(v[0]*v[0] + v[1]*v[1])
}

// Distance between two points
func Distance2f(a [2]float32, b [2]float32) float32 {
	return Length2f(Sub2f(a, b))
}

// Normalizes the given vector.
func Normalize2f(a [2]float32) [2]float32 {
	l := Length2f(a)
	if l == 0 {
		return [2]float32{0, 0}
	}
	return Scale2f(a, 1/l)
}

// rotator2f returns a function that rotates points by the specified angle
// (given in degrees).
func Rotator2f(angle float32) func([2]float32) [2]float32 {
	s, c := Sin(Radians(angle)), Cos(Radians(angle))
	return func(p [2]float32) [2]float32 {
		return [2]float32{c*p[0] + s*p[1], -s*p[0] + c*p[1]}
	}
}

// Equivalent to acos(Dot(a, b)), but more numerically stable.
// via http://www.plunk.org/~hatch/rightway.html
func AngleBetween(v1, v2 [2]float32) float32 {
	asin := func(a float32) float32 {
		return float32(gomath.Asin(float64(Clamp(a, -1, 1))))
	}

	if Dot(v1, v2) < 0 {
		return gomath.Pi - 2*asin(Length2f(Add2f(v1, v2))/2)
	} else {
		return 2 * asin(Length2f(Sub2f(v2, v1))/2)
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
	s, c := Sin(theta), Cos(theta)
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
