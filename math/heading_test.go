// math/heading_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package math

import (
	"math"
	"testing"
)

func TestIsHeadingBetween(t *testing.T) {
	tests := []struct {
		name     string
		h        float32
		h1       float32
		h2       float32
		expected bool
	}{
		// Simple cases without wraparound
		{"middle of range", 45, 0, 90, true},
		{"at start", 0, 0, 90, true},
		{"at end", 90, 0, 90, true},
		{"before range", 350, 0, 90, false},
		{"after range", 100, 0, 90, false},

		// Wraparound cases (h1 > h2)
		{"wraparound middle", 10, 350, 20, true},
		{"wraparound at 0", 0, 350, 20, true},
		{"wraparound at 360", 360, 350, 20, true},
		{"wraparound start", 350, 350, 20, true},
		{"wraparound end", 20, 350, 20, true},
		{"wraparound outside", 100, 350, 20, false},
		{"wraparound outside 2", 200, 350, 20, false},

		// Edge cases
		{"same start and end", 45, 45, 45, true},
		{"heading equals start", 180, 180, 270, true},
		{"heading equals end", 270, 180, 270, true},

		// Full circle
		{"full circle 1", 100, 0, 359, true},
		{"full circle 2", 200, 0, 359, true},
		{"full circle 3", 350, 0, 359, true},

		// Real-world holding pattern examples
		// LEFT turn hold: inbound 041, outbound 221
		// Teardrop sector: 221 to 291
		{"LEFT hold teardrop start", 221, 221, 291, true},
		{"LEFT hold teardrop middle", 261.5, 221, 291, true},
		{"LEFT hold teardrop end", 291, 221, 291, true},
		{"LEFT hold outside teardrop", 300, 221, 291, false},

		// RIGHT turn hold: inbound 360, outbound 180
		// Parallel sector: 180 to 290
		{"RIGHT hold parallel start", 180, 180, 290, true},
		{"RIGHT hold parallel middle", 235, 180, 290, true},
		{"RIGHT hold parallel end", 290, 180, 290, true},
		{"RIGHT hold outside parallel", 300, 180, 290, false},

		// Negative heading (should normalize)
		{"negative heading", -10, 350, 20, true},
		{"negative h1", 10, -10, 20, true},
		{"negative h2", 350, 340, -10, true}, // -10 normalizes to 350

		// Greater than 360 (should normalize)
		{"heading > 360", 370, 350, 20, true},
		{"h1 > 360", 10, 710, 20, true},
		{"h2 > 360", 10, 350, 380, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHeadingBetween(tt.h, tt.h1, tt.h2)
			if result != tt.expected {
				t.Errorf("IsHeadingBetween(%v, %v, %v) = %v, expected %v",
					tt.h, tt.h1, tt.h2, result, tt.expected)
			}
		})
	}
}

func TestCompass(t *testing.T) {
	type ch struct {
		h     float32
		dir   string
		short string
		hour  int
	}

	for _, c := range []ch{ch{0, "North", "N", 12}, ch{22, "North", "N", 1}, ch{338, "North", "N", 11},
		ch{337, "Northwest", "NW", 11}, ch{95, "East", "E", 3}, ch{47, "Northeast", "NE", 2},
		ch{140, "Southeast", "SE", 5}, ch{170, "South", "S", 6}, ch{205, "Southwest", "SW", 7},
		ch{260, "West", "W", 9}} {
		if Compass(c.h) != c.dir {
			t.Errorf("compass gave %s for %f; expected %s", Compass(c.h), c.h, c.dir)
		}
		if ShortCompass(c.h) != c.short {
			t.Errorf("shortCompass gave %s for %f; expected %s", ShortCompass(c.h), c.h, c.short)
		}
		if HeadingAsHour(c.h) != c.hour {
			t.Errorf("headingAsHour gave %d for %f; expected %d", HeadingAsHour(c.h), c.h, c.hour)
		}
	}
}

func TestHeadingDifference(t *testing.T) {
	type hd struct {
		a, b, d float32
	}

	for _, h := range []hd{hd{10, 90, 80}, hd{350, 12, 22}, hd{340, 120, 140}, hd{-90, 80, 170},
		hd{40, 181, 141}, hd{-170, 160, 30}, hd{-120, -150, 30}} {
		if HeadingDifference(h.a, h.b) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.a, h.b,
				HeadingDifference(h.a, h.b), h.d)
		}
		if HeadingDifference(h.b, h.a) != h.d {
			t.Errorf("headingDifference(%f, %f) -> %f, expected %f", h.b, h.a,
				HeadingDifference(h.b, h.a), h.d)
		}
	}
}

func TestOppositeHeading(t *testing.T) {
	h := [][2]float32{{90, 270}, {1, 181}, {2, 182}, {350, 170}}
	for _, pair := range h {
		if OppositeHeading(pair[0]) != pair[1] {
			t.Errorf("opposite heading error: %f -> %f, expected %f",
				pair[0], OppositeHeading(pair[0]), pair[1])
		}
		if OppositeHeading(pair[1]) != pair[0] {
			t.Errorf("opposite heading error: %f -> %f, expected %f",
				pair[1], OppositeHeading(pair[1]), pair[0])
		}
	}
}

func TestNormalizeHeading(t *testing.T) {
	h := [][2]float32{{90, 90}, {360, 0}, {-10, 350}, {380, 20}, {-380, 340}}
	for _, pair := range h {
		if NormalizeHeading(pair[0]) != pair[1] {
			t.Errorf("normalize heading error: %f -> %f, expected %f",
				pair[0], NormalizeHeading(pair[0]), pair[1])
		}
	}
}

func TestHeadingSignedTurn(t *testing.T) {
	turns := [][3]float32{{10, 90, 80}, {10, 350, -20}, {120, 10, -110}, {120, 270, 150}}
	for _, turn := range turns {
		if result := HeadingSignedTurn(turn[0], turn[1]); result != turn[2] {
			t.Errorf("HeadingSignedTurn(%f, %f) = %f; expected %f", turn[0], turn[1], result, turn[2])
		}
	}
}

func TestCardinalOrdinalDirection(t *testing.T) {
	tests := []struct {
		dir      CardinalOrdinalDirection
		heading  float32
		short    string
		parsable string
	}{
		{North, 0, "N", "N"},
		{NorthEast, 45, "NE", "NE"},
		{East, 90, "E", "E"},
		{SouthEast, 135, "SE", "SE"},
		{South, 180, "S", "S"},
		{SouthWest, 225, "SW", "SW"},
		{West, 270, "W", "W"},
		{NorthWest, 315, "NW", "NW"},
	}

	for _, tt := range tests {
		if tt.dir.Heading() != tt.heading {
			t.Errorf("%v.Heading() = %f, expected %f", tt.dir, tt.dir.Heading(), tt.heading)
		}
		if tt.dir.ShortString() != tt.short {
			t.Errorf("%v.ShortString() = %s, expected %s", tt.dir, tt.dir.ShortString(), tt.short)
		}
	}
}

func TestParseCardinalOrdinalDirection(t *testing.T) {
	tests := []struct {
		str      string
		expected CardinalOrdinalDirection
		wantErr  bool
	}{
		{"N", North, false},
		{"NE", NorthEast, false},
		{"E", East, false},
		{"SE", SouthEast, false},
		{"S", South, false},
		{"SW", SouthWest, false},
		{"W", West, false},
		{"NW", NorthWest, false},
		{"invalid", North, true},
		{"", North, true},
		{"n", North, true}, // case sensitive
	}

	for _, tt := range tests {
		result, err := ParseCardinalOrdinalDirection(tt.str)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseCardinalOrdinalDirection(%q) expected error, got nil", tt.str)
			}
		} else {
			if err != nil {
				t.Errorf("ParseCardinalOrdinalDirection(%q) unexpected error: %v", tt.str, err)
			}
			if result != tt.expected {
				t.Errorf("ParseCardinalOrdinalDirection(%q) = %v, expected %v", tt.str, result, tt.expected)
			}
		}
	}
}

func TestHeading2LL(t *testing.T) {
	tests := []struct {
		name           string
		from           Point2LL
		to             Point2LL
		nmPerLongitude float32
		magCorrection  float32
		expected       float32
		tolerance      float32
	}{
		{
			name:           "north",
			from:           Point2LL{-73, 40},
			to:             Point2LL{-73, 41},
			nmPerLongitude: 50,
			magCorrection:  0,
			expected:       0,
			tolerance:      0.1,
		},
		{
			name:           "east",
			from:           Point2LL{-73, 40},
			to:             Point2LL{-72, 40},
			nmPerLongitude: 50,
			magCorrection:  0,
			expected:       90,
			tolerance:      0.1,
		},
		{
			name:           "south",
			from:           Point2LL{-73, 41},
			to:             Point2LL{-73, 40},
			nmPerLongitude: 50,
			magCorrection:  0,
			expected:       180,
			tolerance:      0.1,
		},
		{
			name:           "west",
			from:           Point2LL{-72, 40},
			to:             Point2LL{-73, 40},
			nmPerLongitude: 50,
			magCorrection:  0,
			expected:       270,
			tolerance:      0.1,
		},
		{
			name:           "with magnetic correction",
			from:           Point2LL{-73, 40},
			to:             Point2LL{-73, 41},
			nmPerLongitude: 50,
			magCorrection:  10,
			expected:       10,
			tolerance:      0.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Heading2LL(tt.from, tt.to, tt.nmPerLongitude, tt.magCorrection)
			if Abs(result-tt.expected) > tt.tolerance {
				t.Errorf("Heading2LL() = %f, expected %f", result, tt.expected)
			}
		})
	}
}

func TestVectorHeading(t *testing.T) {
	tests := []struct {
		name      string
		vector    [2]float32
		expected  float32
		tolerance float32
	}{
		{"north", [2]float32{0, 1}, 0, 0.01},
		{"northeast", [2]float32{1, 1}, 45, 0.01},
		{"east", [2]float32{1, 0}, 90, 0.01},
		{"southeast", [2]float32{1, -1}, 135, 0.01},
		{"south", [2]float32{0, -1}, 180, 0.01},
		{"southwest", [2]float32{-1, -1}, 225, 0.01},
		{"west", [2]float32{-1, 0}, 270, 0.01},
		{"northwest", [2]float32{-1, 1}, 315, 0.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := VectorHeading(tt.vector)
			if Abs(result-tt.expected) > tt.tolerance {
				t.Errorf("VectorHeading(%v) = %f, expected %f", tt.vector, result, tt.expected)
			}
		})
	}
}

func TestHeadingVector(t *testing.T) {
	tests := []struct {
		name      string
		heading   float32
		tolerance float32
	}{
		{"north", 0, 0.01},
		{"northeast", 45, 0.01},
		{"east", 90, 0.01},
		{"southeast", 135, 0.01},
		{"south", 180, 0.01},
		{"southwest", 225, 0.01},
		{"west", 270, 0.01},
		{"northwest", 315, 0.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HeadingVector(tt.heading)
			// Check that the vector points in the right direction
			calculatedHeading := VectorHeading(result)
			if Abs(calculatedHeading-tt.heading) > tt.tolerance {
				t.Errorf("HeadingVector(%f) produced vector with heading %f", tt.heading, calculatedHeading)
			}
			// Check that it's a unit vector
			length := math.Sqrt(float64(result[0]*result[0] + result[1]*result[1]))
			if math.Abs(length-1.0) > 0.01 {
				t.Errorf("HeadingVector(%f) produced vector with length %f, expected 1.0", tt.heading, length)
			}
		})
	}
}
