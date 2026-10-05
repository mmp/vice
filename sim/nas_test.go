// sim/nas_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"testing"

	"github.com/mmp/vice/math"
)

// An FDAM region's TCP-specific leader direction goes on the flight plan for
// each of its pointout TCPs when a track enters the region. It comes off when
// the track leaves unless the region retains it.
func TestFDAMLeaderLineDirections(t *testing.T) {
	nw := math.NorthWest
	inside := map[ControlPosition]math.CardinalOrdinalDirection{"1B": nw, "1C": nw}

	for _, retain := range []bool{false, true} {
		s := newHandoffTestSim()
		ac, fp := newHandoffTestTrack(s, "TST400")
		region := FDAMRegion{
			AirspaceVolume:                   bigCircle(),
			NewTCPSpecificLeaderDirection:    &nw,
			PointoutTCPs:                     []ControlPosition{"1B", "1C"},
			RetainTCPSpecificLeaderDirection: retain,
		}
		region.Id = "F1"
		s.State.FacilityAdaptation.Filters.FDAM = FDAMRegions{region}

		s.processFDAMRegions(ac)
		if !maps.Equal(fp.FDAMLeaderLineDirections, inside) {
			t.Errorf("retain=%v: inside the region, directions = %v, want %v", retain,
				fp.FDAMLeaderLineDirections, inside)
		}

		// Move the region far away so that the track leaves it.
		s.State.FacilityAdaptation.Filters.FDAM[0].Center.Point2LL = math.Point2LL{10, 10}
		s.processFDAMRegions(ac)
		want := inside
		if !retain {
			want = nil
		}
		if !maps.Equal(fp.FDAMLeaderLineDirections, want) {
			t.Errorf("retain=%v: after leaving the region, directions = %v, want %v", retain,
				fp.FDAMLeaderLineDirections, want)
		}
	}
}
