// nav/slowdown_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func TestAssignedSpeedFloor(t *testing.T) {
	mkRange := func(lo, hi float32) *av.SpeedRestriction {
		return &av.SpeedRestriction{NavigationRestriction: av.NavigationRestriction{Range: [2]float32{lo, hi}}}
	}

	cases := []struct {
		name      string
		speed     NavSpeed
		wantFloor float32
		wantOK    bool
	}{
		{"none", NavSpeed{}, 0, false},
		{"exact", NavSpeed{Assigned: mkRange(250, 250)}, 250, true},
		{"range uses floor", NavSpeed{Assigned: mkRange(210, 250)}, 210, true},
		{"mach ignored", NavSpeed{Assigned: &av.SpeedRestriction{
			NavigationRestriction: av.NavigationRestriction{Range: [2]float32{0.74, 0.74}}, IsMach: true}}, 0, false},
		{"max forward", NavSpeed{MaintainMaximumForward: true}, av.MaxRestrictionSpeed, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &Nav{Speed: c.speed}
			floor, ok := n.AssignedSpeedFloor()
			if ok != c.wantOK || (ok && floor != c.wantFloor) {
				t.Errorf("AssignedSpeedFloor() = %v, %v; want %v, %v", floor, ok, c.wantFloor, c.wantOK)
			}
		})
	}
}

func TestSlowDownDistanceNM(t *testing.T) {
	const nmPerLong = 60
	pt := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, nmPerLong) }

	t.Run("route sums remaining legs", func(t *testing.T) {
		n := &Nav{
			FlightState: FlightState{Position: pt(0, 0), NmPerLongitude: nmPerLong},
			Waypoints: av.WaypointArray{
				{Fix: "WP0", Location: pt(0, 5)},
				{Fix: "WP1", Location: pt(0, 15)},
			},
		}
		d, ok := n.SlowDownDistanceNM()
		if !ok || math.Abs(d-15) > 0.1 {
			t.Errorf("SlowDownDistanceNM() = %.2f, %v; want ~15, true", d, ok)
		}
	})

	// Being vectored: route branch is disabled regardless of waypoints.
	vectored := func(acHeading math.MagneticHeading) *Nav {
		h := math.MagneticHeading(123) // any non-nil assigned heading
		return &Nav{
			FlightState: FlightState{Position: pt(0, 0), Heading: acHeading, NmPerLongitude: nmPerLong},
			Heading:     NavHeading{Assigned: &h},
			Approach:    NavApproach{Assigned: &av.Approach{Threshold: pt(0, 10)}},
		}
	}

	t.Run("vectored toward threshold", func(t *testing.T) {
		d, ok := vectored(0 /* north, toward threshold */).SlowDownDistanceNM()
		if !ok || math.Abs(d-10) > 0.1 {
			t.Errorf("SlowDownDistanceNM() = %.2f, %v; want ~10, true", d, ok)
		}
	})

	t.Run("vectored away from threshold", func(t *testing.T) {
		if d, ok := vectored(180 /* south, away */).SlowDownDistanceNM(); ok {
			t.Errorf("SlowDownDistanceNM() = %.2f, %v; want not-ok while heading away", d, ok)
		}
	})

	t.Run("vectored without assigned approach", func(t *testing.T) {
		h := math.MagneticHeading(90)
		n := &Nav{
			FlightState: FlightState{Position: pt(0, 0), NmPerLongitude: nmPerLong},
			Heading:     NavHeading{Assigned: &h},
		}
		if d, ok := n.SlowDownDistanceNM(); ok {
			t.Errorf("SlowDownDistanceNM() = %.2f, %v; want not-ok with no runway reference", d, ok)
		}
	})
}
