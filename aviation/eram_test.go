// aviation/eram_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"strings"
	"testing"

	"github.com/mmp/vice/util"
)

func TestERAMEntriesCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries ERAMEntries
		expect  string // the error expected, or "" if the entries are valid
	}{
		{name: "empty"},
		{name: "interim altitude", entries: ERAMEntries{InterimAltitude: 17000}},
		{name: "procedure altitude",
			entries: ERAMEntries{InterimAltitude: 17000, InterimType: "procedure"}},
		{name: "speed", entries: ERAMEntries{Speed: 280}},
		{name: "mach", entries: ERAMEntries{Mach: 78}},
		{name: "heading", entries: ERAMEntries{Heading: 310}},
		{name: "free text", entries: ERAMEntries{FreeText: "VIA J121"}, expect: "must be A-Z"},
		{name: "altitude in hundreds", entries: ERAMEntries{InterimAltitude: 170},
			expect: "must be given in feet"},
		{name: "altitude off a hundred", entries: ERAMEntries{AssignedAltitude: 17050},
			expect: "multiple of 100"},
		{name: "unknown interim type", entries: ERAMEntries{InterimAltitude: 17000, InterimType: "procedural"},
			expect: `unknown "interim_type"`},
		{name: "interim type without an altitude", entries: ERAMEntries{InterimType: "local"},
			expect: `without an "interim_altitude"`},
		{name: "heading and free text", entries: ERAMEntries{Heading: 310, FreeText: "HDG"},
			expect: `both "heading" and "free_text"`},
		{name: "heading out of range", entries: ERAMEntries{Heading: 400}, expect: `"heading"`},
		{name: "long free text", entries: ERAMEntries{FreeText: "TOOMUCHTEXT"},
			expect: "at most 8 characters"},
		{name: "speed and mach", entries: ERAMEntries{Speed: 280, Mach: 78},
			expect: `both "speed" and "mach"`},
		{name: "speed in mach", entries: ERAMEntries{Speed: 78}, expect: `"speed"`},
		{name: "mach in hundreds", entries: ERAMEntries{Mach: 780}, expect: `"mach"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &util.ErrorLogger{}
			tc.entries.Check(e)

			if tc.expect == "" {
				if e.HaveErrors() {
					t.Errorf("expected no errors, got %s", e.String())
				}
			} else if !strings.Contains(e.String(), tc.expect) {
				t.Errorf("expected an error mentioning %q, got %s", tc.expect, e.String())
			}
		})
	}
}

func TestERAMEntriesCheckInbound(t *testing.T) {
	for _, tc := range []struct {
		name                string
		entries             ERAMEntries
		assignedAltitude    float32
		scratchpad          string
		secondaryScratchpad string
		expect              string
	}{
		{name: "no conflict", entries: ERAMEntries{InterimAltitude: 17000},
			assignedAltitude: 35000, scratchpad: "JFK"},
		{name: "two assigned altitudes", entries: ERAMEntries{AssignedAltitude: 17000},
			assignedAltitude: 35000, expect: `must not also be given under "eram"`},
		{name: "heading over a scratchpad", entries: ERAMEntries{Heading: 310},
			scratchpad: "JFK", expect: "same data block field"},
		{name: "speed over a secondary scratchpad", entries: ERAMEntries{Speed: 280},
			secondaryScratchpad: "JFK", expect: "same data block field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &util.ErrorLogger{}
			tc.entries.CheckInbound(tc.assignedAltitude, tc.scratchpad, tc.secondaryScratchpad, e)

			if tc.expect == "" {
				if e.HaveErrors() {
					t.Errorf("expected no errors, got %s", e.String())
				}
			} else if !strings.Contains(e.String(), tc.expect) {
				t.Errorf("expected an error mentioning %q, got %s", tc.expect, e.String())
			}
		})
	}
}
