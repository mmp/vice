package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestAssignedSpeedForSTT(t *testing.T) {
	sr := func(s av.SpeedRestriction) *av.SpeedRestriction { return &s }
	tests := []struct {
		name        string
		sr          *av.SpeedRestriction
		knots, mach int
	}{
		{"none", nil, 0, 0},
		{"exact knots", sr(av.MakeAtSpeedRestriction(210)), 210, 0},
		{"at-or-above", sr(av.MakeAtOrAboveSpeedRestriction(230)), 230, 0},
		{"at-or-below", sr(av.MakeAtOrBelowSpeedRestriction(180)), 180, 0},
		{"range", sr(av.MakeRangeSpeedRestriction(200, 250)), 200, 0},
		{"mach", sr(av.MakeMachRestriction(0.78)), 0, 78},
		{"mach rounds", sr(av.MakeMachRestriction(0.805)), 0, 81},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			knots, mach := assignedSpeedForSTT(tc.sr)
			if knots != tc.knots || mach != tc.mach {
				t.Errorf("got (knots=%d, mach=%d), want (knots=%d, mach=%d)", knots, mach, tc.knots, tc.mach)
			}
		})
	}
}
