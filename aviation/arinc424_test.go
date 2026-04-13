// pkg/aviation/arinc424_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"math"
	"strings"
	"testing"
)

// TestParseHoldingPattern tests the parsing of holding pattern records from actual CIFP data.
// Holds are embedded in procedure waypoints with HF, HA, or HM path terminators.
func TestParseHoldingPattern(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		procName string // procedure name to pass to extractHoldsFromSSA
		wantHold Hold
		wantOk   bool
	}{
		{
			name:     "KJFK ILS 04R missed approach hold at DPK (HM, time-based)",
			line:     "SUSAP KJFKK6FI04R  I      070DPK  K6D 0VE  L   HM                     2581T010    + 04000                           0 NS   300201709",
			procName: "I04R",
			wantHold: Hold{
				Fix:             "DPK",
				InboundCourse:   258.1,
				TurnDirection:   TurnLeft,
				LegLengthNM:     0,
				LegMinutes:      1.0,
				MinimumAltitude: 4000,
				MaximumAltitude: 0,
				HoldingSpeed:    0,
				Procedure:       "I04R",
			},
			wantOk: true,
		},
		{
			name:     "KJFK ILS 04L missed approach hold at DUFFY (HM, time-based, right turn)",
			line:     "SUSAP KJFKK6FI04L  I      060DUFFYK6PC0EE  L   HM                     2420T010    + 03000                           0 NS   300131310",
			procName: "I04L",
			wantHold: Hold{
				Fix:             "DUFFY",
				InboundCourse:   242.0,
				TurnDirection:   TurnLeft,
				LegLengthNM:     0,
				LegMinutes:      1.0,
				MinimumAltitude: 3000,
				MaximumAltitude: 0,
				HoldingSpeed:    0,
				Procedure:       "I04L",
			},
			wantOk: true,
		},
		{
			name:     "invalid record - not HF/HA/HM terminator",
			line:     "SUSAP KJFKK6FI04R  I      060DPK  K6D 0VY      CF DPK K6      0000000004100080D   + 04000                           0 NS   300191212",
			procName: "I04R",
			wantHold: Hold{},
			wantOk:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create properly formatted 134-byte line (132 chars + \r + \n)
			line := make([]byte, 134)
			copy(line, tt.line)
			// Pad with spaces up to column 132
			for i := len(tt.line); i < 132; i++ {
				line[i] = ' '
			}
			line[132] = '\r'
			line[133] = '\n'

			// Parse the SSA record and extract holds
			rec := parseSSA(line)
			gotHold, gotOk := extractHoldsFromSSA(rec, tt.procName, "IAP")

			if gotOk != tt.wantOk {
				t.Errorf("extractHoldsFromSSA() ok = %v, want %v", gotOk, tt.wantOk)
			}

			if gotOk && !holdsEqual(gotHold, tt.wantHold) {
				t.Errorf("extractHoldsFromSSA() mismatch\ngot:  %+v\nwant: %+v", gotHold, tt.wantHold)
			}
		})
	}
}

func holdsEqual(a, b Hold) bool {
	const epsilon = 0.01
	return a.Fix == b.Fix &&
		math.Abs(float64(a.InboundCourse-b.InboundCourse)) < epsilon &&
		a.TurnDirection == b.TurnDirection &&
		math.Abs(float64(a.LegLengthNM-b.LegLengthNM)) < epsilon &&
		math.Abs(float64(a.LegMinutes-b.LegMinutes)) < epsilon &&
		a.MinimumAltitude == b.MinimumAltitude &&
		a.MaximumAltitude == b.MaximumAltitude &&
		a.HoldingSpeed == b.HoldingSpeed &&
		a.Procedure == b.Procedure
}

func TestParseARINC424LocalizerNavaid(t *testing.T) {
	line := []byte(strings.Repeat(" ", 132))
	copy(line[0:], "SUSA")
	line[4] = 'P'
	copy(line[6:], "KEWR")
	copy(line[10:], "K6")
	line[12] = 'I'
	copy(line[13:], "IEZA")
	line[21] = '1' // continuation record number: primary record
	copy(line[32:], "N40414355")
	copy(line[41:], "W074094163")
	result := ParseARINC424(strings.NewReader(string(line) + "\r\n"))

	nav, ok := result.Navaids["IEZA"]
	if !ok {
		t.Fatal("expected IEZA localizer navaid")
	}
	if nav.Type != "LOC" {
		t.Fatalf("expected LOC navaid type, got %q", nav.Type)
	}
	if nav.Location.IsZero() {
		t.Fatal("expected IEZA localizer location")
	}
}
