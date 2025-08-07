// pkg/math/transcendentals_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"math"
	"testing"
)

func relativeError(actual, expected float32) float32 {
	if expected == 0 {
		if actual == 0 {
			return 0
		}
		return Abs(actual)
	}
	return Abs((actual - expected) / expected)
}

func absoluteError(actual, expected float32) float32 {
	return Abs(actual - expected)
}

func TestSinCos(t *testing.T) {
	testRanges := []struct {
		name      string
		min, max  float32
		tolerance float32
	}{
		{"[-10,10]", -10, 10, 1e-4},
		{"[-100,100]", -100, 100, 1e-4},
	}

	for _, tr := range testRanges {
		t.Run(tr.name, func(t *testing.T) {
			maxSinErr := float32(0)
			maxCosErr := float32(0)
			worstSinX := float32(0)
			worstCosX := float32(0)

			// Test at regular intervals
			step := (tr.max - tr.min) / 10000
			for x := tr.min; x <= tr.max; x += step {
				sincos := SinCos(x)
				sin, cos := sincos[0], sincos[1]

				expectedSin := float32(math.Sin(float64(x)))
				expectedCos := float32(math.Cos(float64(x)))

				// Calculate absolute error
				sinErr := absoluteError(sin, expectedSin)
				cosErr := absoluteError(cos, expectedCos)

				if sinErr > maxSinErr {
					maxSinErr = sinErr
					worstSinX = x
				}
				if cosErr > maxCosErr {
					maxCosErr = cosErr
					worstCosX = x
				}

				if sinErr > tr.tolerance {
					t.Errorf("sin(%v): got %v, expected %v, error %v%% (exceeds %v%%)",
						x, sin, expectedSin, sinErr*100, tr.tolerance*100)
				}
				if cosErr > tr.tolerance {
					t.Errorf("cos(%v): got %v, expected %v, error %v%% (exceeds %v%%)",
						x, cos, expectedCos, cosErr*100, tr.tolerance*100)
				}
			}

			t.Logf("Max sin error in %s: %v at x=%v", tr.name, maxSinErr*100, worstSinX)
			t.Logf("Max cos error in %s: %v at x=%v", tr.name, maxCosErr*100, worstCosX)
		})
	}
}

func TestTan(t *testing.T) {
	minX := float32(-math.Pi / 4)
	maxX := float32(math.Pi / 4)
	tolerance := float32(0.001) // 0.1%

	maxErr := float32(0)
	worstX := float32(0)

	// Test at regular intervals
	step := (maxX - minX) / 10000
	for x := minX; x <= maxX; x += step {
		actual := Tan(x)
		expected := float32(math.Tan(float64(x)))

		err := absoluteError(actual, expected)
		if err > maxErr {
			maxErr = err
			worstX = x
		}

		if err > tolerance {
			t.Errorf("Tan(%v): got %v, expected %v, error %v%% (exceeds %v%%)",
				x, actual, expected, err*100, tolerance*100)
		}
	}

	t.Logf("Max error in [-π/4,π/4]: %v at x=%v", maxErr*100, worstX)
}

func TestExp(t *testing.T) {
	minX := float32(-10)
	maxX := float32(10)
	tolerance := float32(0.001) // 0.1%

	maxErr := float32(0)
	worstX := float32(0)

	// Test at regular intervals
	step := (maxX - minX) / 10000
	for x := minX; x <= maxX; x += step {
		actual := FastExp(x)
		expected := float32(math.Exp(float64(x)))

		err := relativeError(actual, expected)
		if err > maxErr {
			maxErr = err
			worstX = x
		}

		if err > tolerance {
			t.Errorf("FastExp(%v): got %v, expected %v, error %v%% (exceeds %v%%)",
				x, actual, expected, err*100, tolerance*100)
		}
	}

	t.Logf("Max error in [-10,10]: %v%% at x=%v", maxErr*100, worstX)
}

func TestAtan(t *testing.T) {
	type testCase struct {
		name     string
		input    float64
		expected float64
		delta    float64
	}

	testCases := []testCase{
		{
			name:     "Atan(0)",
			input:    0,
			expected: 0,
			delta:    1e-10,
		},
		{
			name:     "Atan(1)",
			input:    1,
			expected: math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan(-1)",
			input:    -1,
			expected: -math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan(sqrt(3))",
			input:    math.Sqrt(3),
			expected: math.Pi / 3,
			delta:    1e-10,
		},
		{
			name:     "Atan(1/sqrt(3))",
			input:    1 / math.Sqrt(3),
			expected: math.Pi / 6,
			delta:    1e-10,
		},
		{
			name:     "Atan(inf)",
			input:    math.Inf(1),
			expected: math.Pi / 2,
			delta:    1e-10,
		},
		{
			name:     "Atan(-inf)",
			input:    math.Inf(-1),
			expected: -math.Pi / 2,
			delta:    1e-10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := math.Atan(tc.input)
			if math.Abs(result-tc.expected) > tc.delta {
				t.Errorf("Atan(%v) = %v, expected %v (delta %v)", tc.input, result, tc.expected, tc.delta)
			}
		})
	}

	t.Run("Atan(NaN)", func(t *testing.T) {
		result := math.Atan(math.NaN())
		if !math.IsNaN(result) {
			t.Errorf("Atan(NaN) = %v, expected NaN", result)
		}
	})

	// Test accuracy over a range of values
	t.Run("Atan accuracy over range", func(t *testing.T) {
		minX := float32(-10)
		maxX := float32(10)
		tolerance := float32(1e-5) // 0.001% error

		maxErr := float32(0)
		worstX := float32(0)

		// Test at regular intervals
		step := (maxX - minX) / 10000
		for x := minX; x <= maxX; x += step {
			actual := Atan(x)
			expected := float32(math.Atan(float64(x)))

			err := absoluteError(actual, expected)
			if err > maxErr {
				maxErr = err
				worstX = x
			}

			if err > tolerance {
				t.Errorf("Atan(%v): got %v, expected %v, error %v (exceeds tolerance %v)",
					x, actual, expected, err, tolerance)
			}
		}

		t.Logf("Max error in [-10,10]: %v at x=%v", maxErr, worstX)

		// Test extreme values
		extremeValues := []float32{-1000, -100, 100, 1000}
		for _, x := range extremeValues {
			actual := Atan(x)
			expected := float32(math.Atan(float64(x)))
			err := absoluteError(actual, expected)
			if err > tolerance {
				t.Errorf("Atan(%v): got %v, expected %v, error %v (exceeds tolerance %v)",
					x, actual, expected, err, tolerance)
			}
		}
	})
}

func TestAtan2(t *testing.T) {
	type testCase struct {
		name     string
		y, x     float64
		expected float64
		delta    float64
	}

	testCases := []testCase{
		{
			name:     "Atan2(0, 1)",
			y:        0,
			x:        1,
			expected: 0,
			delta:    1e-10,
		},
		{
			name:     "Atan2(1, 0)",
			y:        1,
			x:        0,
			expected: math.Pi / 2,
			delta:    1e-10,
		},
		{
			name:     "Atan2(0, -1)",
			y:        0,
			x:        -1,
			expected: math.Pi,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-1, 0)",
			y:        -1,
			x:        0,
			expected: -math.Pi / 2,
			delta:    1e-10,
		},
		{
			name:     "Atan2(1, 1)",
			y:        1,
			x:        1,
			expected: math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(1, -1)",
			y:        1,
			x:        -1,
			expected: 3 * math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-1, -1)",
			y:        -1,
			x:        -1,
			expected: -3 * math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-1, 1)",
			y:        -1,
			x:        1,
			expected: -math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(sqrt(3), 1)",
			y:        math.Sqrt(3),
			x:        1,
			expected: math.Pi / 3,
			delta:    1e-10,
		},
		{
			name:     "Atan2(1, sqrt(3))",
			y:        1,
			x:        math.Sqrt(3),
			expected: math.Pi / 6,
			delta:    1e-10,
		},
		{
			name:     "Atan2(0, 0)",
			y:        0,
			x:        0,
			expected: 0,
			delta:    1e-10,
		},
		{
			name:     "Atan2(+0, -0)",
			y:        0,
			x:        math.Copysign(0, -1),
			expected: math.Pi,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-0, -0)",
			y:        math.Copysign(0, -1),
			x:        math.Copysign(0, -1),
			expected: -math.Pi,
			delta:    1e-10,
		},
		{
			name:     "Atan2(inf, inf)",
			y:        math.Inf(1),
			x:        math.Inf(1),
			expected: math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(inf, -inf)",
			y:        math.Inf(1),
			x:        math.Inf(-1),
			expected: 3 * math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-inf, -inf)",
			y:        math.Inf(-1),
			x:        math.Inf(-1),
			expected: -3 * math.Pi / 4,
			delta:    1e-10,
		},
		{
			name:     "Atan2(-inf, inf)",
			y:        math.Inf(-1),
			x:        math.Inf(1),
			expected: -math.Pi / 4,
			delta:    1e-10,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := math.Atan2(tc.y, tc.x)
			if math.Abs(result-tc.expected) > tc.delta {
				t.Errorf("Atan2(%v, %v) = %v, expected %v (delta %v)", tc.y, tc.x, result, tc.expected, tc.delta)
			}
		})
	}

	t.Run("Atan2(NaN, x)", func(t *testing.T) {
		result := math.Atan2(math.NaN(), 1)
		if !math.IsNaN(result) {
			t.Errorf("Atan2(NaN, 1) = %v, expected NaN", result)
		}
	})

	t.Run("Atan2(y, NaN)", func(t *testing.T) {
		result := math.Atan2(1, math.NaN())
		if !math.IsNaN(result) {
			t.Errorf("Atan2(1, NaN) = %v, expected NaN", result)
		}
	})

	// Test accuracy over a range of values
	t.Run("Atan2 accuracy over range", func(t *testing.T) {
		tolerance := float32(1e-5) // 0.001% error

		maxErr := float32(0)
		worstY := float32(0)
		worstX := float32(0)

		// Test over a grid of (y, x) values
		testRange := float32(10)
		step := testRange / 50 // 100x100 grid
		for y := -testRange; y <= testRange; y += step {
			for x := -testRange; x <= testRange; x += step {
				// Skip very small values near origin to avoid numerical issues
				if Abs(x) < 0.001 && Abs(y) < 0.001 {
					continue
				}

				actual := Atan2(y, x)
				expected := float32(math.Atan2(float64(y), float64(x)))

				err := absoluteError(actual, expected)
				// Handle angle wraparound at ±π
				if err > Pi {
					err = 2*Pi - err
				}

				if err > maxErr {
					maxErr = err
					worstY = y
					worstX = x
				}

				if err > tolerance {
					t.Errorf("Atan2(%v, %v): got %v, expected %v, error %v (exceeds tolerance %v)",
						y, x, actual, expected, err, tolerance)
				}
			}
		}

		t.Logf("Max error in [-10,10]x[-10,10]: %v at (y=%v, x=%v)", maxErr, worstY, worstX)

		// Test some specific challenging cases
		challengingCases := [][2]float32{
			{1, 0},       // y-axis positive
			{-1, 0},      // y-axis negative
			{0, -1},      // x-axis negative
			{1, 1e-10},   // near y-axis
			{1e-10, -1},  // near negative x-axis
			{-1, -1e-10}, // near negative y-axis
		}

		for _, tc := range challengingCases {
			y, x := tc[0], tc[1]
			actual := Atan2(y, x)
			expected := float32(math.Atan2(float64(y), float64(x)))
			err := absoluteError(actual, expected)
			// Handle angle wraparound at ±π
			if err > Pi {
				err = 2*Pi - err
			}
			if err > tolerance {
				t.Errorf("Atan2(%v, %v): got %v, expected %v, error %v (exceeds tolerance %v)",
					y, x, actual, expected, err, tolerance)
			}
		}
	})
}
