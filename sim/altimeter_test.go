// sim/altimeter_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func TestAltimBiasFeet(t *testing.T) {
	tests := []struct {
		name              string
		nearestActualInHg float32
		pilotInHg         float32
		want              float32
	}{
		{"zero pilot short-circuits", 30.05, 0, 0},
		{"equal values give zero bias", 30.05, 30.05, 0},
		{"pilot too low yields positive bias", 30.10, 30.00, 100},
		{"pilot too high yields negative bias", 30.00, 30.10, -100},
		{"realistic small mismatch", 30.05, 30.03, 20},
		{"large geographic delta", 30.20, 29.80, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := altimBiasFeet(tc.nearestActualInHg, tc.pilotInHg)
			if math.Abs(got-tc.want) > 0.01 {
				t.Errorf("altimBiasFeet(%v, %v) = %v, want %v",
					tc.nearestActualInHg, tc.pilotInHg, got, tc.want)
			}
		})
	}
}

func TestNearestActualAltimEmptyMap(t *testing.T) {
	s := &Sim{State: &CommonState{DynamicState: DynamicState{METAR: map[string]wx.METAR{}}}}
	got := s.nearestActualAltim(math.Point2LL{-73.78, 40.64}) // KJFK area
	if got != 0 {
		t.Errorf("nearestActualAltim with empty map = %v, want 0", got)
	}
}
