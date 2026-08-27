// pkg/aviation/arinc424_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"math"
	"slices"
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

func TestParseARINC424DMENavaidElevation(t *testing.T) {
	line := []byte(strings.Repeat(" ", 132))
	copy(line[0:], "SUSA")
	line[4] = 'D'
	line[5] = ' '
	copy(line[13:], "IEZA")
	line[21] = '1'
	copy(line[51:], "IEZA")
	copy(line[55:], "N40414355")
	copy(line[64:], "W074094163")
	copy(line[79:], "00033")
	copy(line[93:], "NEWARK LIBERTY INTL")

	result := ParseARINC424(strings.NewReader(string(line) + "\r\n"))

	nav, ok := result.Navaids["IEZA"]
	if !ok {
		t.Fatal("expected IEZA DME navaid")
	}
	if nav.Type != "DME" {
		t.Fatalf("expected DME navaid type, got %q", nav.Type)
	}
	if !nav.HasDME {
		t.Fatal("expected IEZA DME data")
	}
	if !nav.HasDMEElevation {
		t.Fatal("expected IEZA DME elevation")
	}
	if nav.DMEElevation != 33 {
		t.Fatalf("expected IEZA DME elevation 33, got %d", nav.DMEElevation)
	}
	if nav.DMELocation.IsZero() {
		t.Fatal("expected IEZA DME location")
	}

	db := StaticDatabase{Navaids: result.Navaids}
	location, elevation, ok := db.LookupDME("ieza")
	if !ok {
		t.Fatal("expected IEZA DME lookup")
	}
	if location.IsZero() {
		t.Fatal("expected IEZA DME lookup location")
	}
	if elevation != 33 {
		t.Fatalf("expected IEZA DME lookup elevation 33, got %d", elevation)
	}
}

func TestParseARINC424LocalizerDoesNotOverwriteDME(t *testing.T) {
	dme := []byte(strings.Repeat(" ", 132))
	copy(dme[0:], "SUSA")
	dme[4] = 'D'
	dme[5] = ' '
	copy(dme[13:], "IEZA")
	dme[21] = '1'
	copy(dme[51:], "IEZA")
	copy(dme[55:], "N40414355")
	copy(dme[64:], "W074094163")
	copy(dme[79:], "00033")

	loc := []byte(strings.Repeat(" ", 132))
	copy(loc[0:], "SUSA")
	loc[4] = 'P'
	copy(loc[6:], "KEWR")
	copy(loc[10:], "K6")
	loc[12] = 'I'
	copy(loc[13:], "IEZA")
	loc[21] = '1'
	copy(loc[32:], "N40400000")
	copy(loc[41:], "W074000000")

	result := ParseARINC424(strings.NewReader(string(dme) + "\r\n" + string(loc) + "\r\n"))

	nav, ok := result.Navaids["IEZA"]
	if !ok {
		t.Fatal("expected IEZA navaid")
	}
	if nav.Type != "DME" {
		t.Fatalf("expected localizer record to preserve DME navaid type, got %q", nav.Type)
	}
	if !nav.HasDMEElevation || nav.DMEElevation != 33 {
		t.Fatalf("expected preserved DME elevation 33, got %d", nav.DMEElevation)
	}
}

// An FM leg is a course from the fix, which the aircraft flies as a ground
// track; a VM leg is a magnetic heading. Both are the last leg of a runway
// transition here: KIAH NNCEE2 ends with a 267 course off HOWLN and KIAH
// OHIIO4 with a 265 heading off PNUUT.
func TestParseARINC424CourseVersusHeadingLegs(t *testing.T) {
	lines := []string{
		"SUSAP KIAHK4ENNCEE26RW09  010BEDLMK4PC0E       IF                                 + 10000     18000250                     218872312",
		"SUSAP KIAHK4ENNCEE26RW09  060HOWLNK4PC0EY      TF                                   06000          210                     218922312",
		"SUSAP KIAHK4ENNCEE26RW09  070HOWLNK4PC0EE      FM AEX K4      239717392672    D                                            218932312",
		"SUSAP KIAHK4EOHIIO43RW09  010PNUUTK4EA0E       IF                                             18000                        219071707",
		"SUSAP KIAHK4EOHIIO43RW09  020KIAH K4PA0AE      VM                     2650                                                 219081707",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	for _, tc := range []struct {
		star    string
		fix     string
		heading int16
		isTrack bool
	}{
		{star: "NNCEE2", fix: "HOWLN", heading: 267, isTrack: true},
		{star: "OHIIO4", fix: "PNUUT", heading: 265, isTrack: false},
	} {
		wps := result.Airports["KIAH"].STARs[tc.star].RunwayWaypoints["9"]
		if len(wps) == 0 {
			t.Errorf("%s: no runway 9 waypoints", tc.star)
			continue
		}
		wp := wps[len(wps)-1]
		if wp.Fix != tc.fix {
			t.Errorf("%s: expected last fix %q, got %q", tc.star, tc.fix, wp.Fix)
		}
		if wp.Heading != tc.heading {
			t.Errorf("%s/%s: expected heading %d, got %d", tc.star, wp.Fix, tc.heading, wp.Heading)
		}
		if wp.HeadingIsTrack() != tc.isTrack {
			t.Errorf("%s/%s: expected HeadingIsTrack %v, got %v", tc.star, wp.Fix, tc.isTrack,
				wp.HeadingIsTrack())
		}
	}
}

// RF legs give the arc's center fix and its radius in thousandths of a
// nautical mile; both are used rather than searching for a circle that
// matches the leg's length.
func TestParseARINC424ConstantRadiusArc(t *testing.T) {
	lines := []string{
		"SUSAP KIAHK4FH09-Y AHOWLN 010HOWLNK4PC0E  B    IF                                   06000     18000210              A-FS   222171406",
		"SUSAP KIAHK4FH09-Y AHOWLN 020TEXXNK4PC0E    010TF                                 + 06000                           A FS   222181406",
		"SUSAP KIAHK4FH09-Y AHOWLN 030HHOOGK4PC0E   R010RF       0025402672    34520035    + 04900                 CFBSR K4PCA FS   222191406",
		"SUSAP KIAHK4FH09-Y AHOWLN 040SAYNOK4PC0EE  R010RF       0025403452    08690045    + 03000                 CFBSR K4PCA FS   222201406",
		"SUSAP KIAHK4FH09-Y H      010SAYNOK4PC0E  I    IF                                 + 03000     18000                 A FS   222261406",
		"SUSAP KIAHK4FH09-Y H      020HYWAYK4PC1E  F 010TF                                 + 02000                 RW09  K4PGA FS   222271406",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	appr, ok := result.Airports["KIAH"].Approaches["RY9"]
	if !ok {
		t.Fatalf("expected KIAH RY9 approach, got %v", result.Airports["KIAH"].Approaches)
	}
	var arc *DMEArc
	for _, wps := range appr.Waypoints {
		if idx := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.Fix == "TEXXN" }); idx != -1 {
			arc = wps[idx].Arc()
		}
	}
	if arc == nil {
		t.Fatalf("expected an arc at TEXXN in %v", appr.Waypoints)
	}
	if arc.Fix != "CFBSR" {
		t.Errorf("expected arc center fix CFBSR, got %q", arc.Fix)
	}
	if arc.Radius != 2.54 {
		t.Errorf("expected arc radius 2.54, got %f", arc.Radius)
	}
	if arc.Direction != DMEArcDirectionClockwise {
		t.Errorf("expected clockwise arc, got %v", arc.Direction)
	}
}
