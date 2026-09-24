// sim/eram_entries_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

// TestApplyERAMEntries checks that the data block entries a scenario gives a
// flight are stored the way the ERAM commands that make them store theirs.
func TestApplyERAMEntries(t *testing.T) {
	// The data block fields the entries fill in.
	type fields struct {
		assignedAltitude    int
		interimAltitude     int
		interimType         InterimAltType
		scratchpad          string
		secondaryScratchpad string
	}

	for _, tc := range []struct {
		name     string
		entries  *av.ERAMEntries
		expected fields
	}{
		{
			name:     "no entries",
			expected: fields{assignedAltitude: 35000},
		},
		{
			name:     "assigned altitude",
			entries:  &av.ERAMEntries{AssignedAltitude: 24000},
			expected: fields{assignedAltitude: 24000},
		},
		{
			name:    "interim altitude",
			entries: &av.ERAMEntries{InterimAltitude: 17000},
			expected: fields{assignedAltitude: 35000, interimAltitude: 17000,
				interimType: InterimNormal},
		},
		{
			name:    "procedure altitude",
			entries: &av.ERAMEntries{InterimAltitude: 17000, InterimType: "procedure"},
			expected: fields{assignedAltitude: 35000, interimAltitude: 17000,
				interimType: InterimProcedure},
		},
		{
			name:    "local interim altitude",
			entries: &av.ERAMEntries{InterimAltitude: 17000, InterimType: "local"},
			expected: fields{assignedAltitude: 35000, interimAltitude: 17000,
				interimType: InterimLocal},
		},
		{
			name:     "heading",
			entries:  &av.ERAMEntries{Heading: 90},
			expected: fields{assignedAltitude: 35000, scratchpad: "090"},
		},
		{
			name:     "free text",
			entries:  &av.ERAMEntries{FreeText: "VIAJ121"},
			expected: fields{assignedAltitude: 35000, scratchpad: "`VIAJ121"},
		},
		{
			name:     "speed",
			entries:  &av.ERAMEntries{Speed: 280},
			expected: fields{assignedAltitude: 35000, secondaryScratchpad: "S280"},
		},
		{
			name:     "mach",
			entries:  &av.ERAMEntries{Mach: 78},
			expected: fields{assignedAltitude: 35000, secondaryScratchpad: "M78"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := NASFlightPlan{AssignedAltitude: 35000}
			fp.applyERAMEntries(tc.entries)

			got := fields{fp.AssignedAltitude, fp.InterimAlt, fp.InterimType, fp.Scratchpad,
				fp.SecondaryScratchpad}
			if got != tc.expected {
				t.Errorf("got %+v, expected %+v", got, tc.expected)
			}
		})
	}
}
