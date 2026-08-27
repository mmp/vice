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

// A VI leg is a heading flown until the course of the following CF leg to its
// fix is intercepted, at which point the aircraft goes direct to that fix.
// KSAN SHAMU1 leaves SHAMU on a 135 heading and joins the 075 course to SARGS
// rather than turning direct to it.
func TestParseARINC424CourseIntercept(t *testing.T) {
	lines := []string{
		"SUSAP KSANK2ESHAMU13RW09  010SHAMUK2PC0E       IF                                             18000                        081981310",
		"SUSAP KSANK2ESHAMU13RW09  020         0        VI                     1350                                                 081990804",
		"SUSAP KSANK2ESHAMU13RW09  030SARGSK2EA0EE      CF MZB K2      2550008007500055D                                            082002103",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	wps := result.Airports["KSAN"].STARs["SHAMU1"].RunwayWaypoints["9"]
	if got := WaypointArray(wps).Encode(); got != "SHAMU/h135@t75 SARGS" {
		t.Fatalf("expected %q, got %q", "SHAMU/h135@t75 SARGS", got)
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 1 {
		t.Fatalf("expected one action group at SHAMU, got %d", len(groups))
	}
	if hdg := groups[0].Actions.Heading; hdg == nil {
		t.Error("expected a heading action at SHAMU")
	} else if hdg.Heading != 135 || hdg.Track {
		t.Errorf("expected heading 135, got %+v", *hdg)
	}
	if until := groups[0].Until; until.Type != WaypointActionCourse || until.Course != 75 {
		t.Errorf("expected a 075 course termination, got %+v", until)
	}
}

// The HUSKR transition of the KLNK ILS 18-Y flies a 199 heading off HUSKR to
// intercept the 177 localizer course to ESACO. Its LNK transition instead
// reverses course with an FC/CI pair, which remains a procedure turn.
func TestParseARINC424ApproachCourseIntercept(t *testing.T) {
	lines := []string{
		"SUSAP KLNKK3FI18-Y AHUSKR 010HUSKRK3EA0E  A    IF                                             18000                 0 NS   439802110",
		"SUSAP KLNKK3FI18-Y AHUSKR 020         0        VI                     1990        + 03200                           0 NS   439812110",
		"SUSAP KLNKK3FI18-Y AHUSKR 030ESACOK3PC0EE B    CF IOCZK3      3573011017730055PI  + 03200                           0 NS   439822110",
		"SUSAP KLNKK3FI18-Y ALNK   010LNK  K3D 0V  A    IF                                 + 03200     18000                 0 NS   439832110",
		"SUSAP KLNKK3FI18-Y ALNK   020JUSAMK3PC0E       TF                                 + 03200                           0 NS   439842110",
		"SUSAP KLNKK3FI18-Y ALNK   030JUSAMK3PC0E       FC LNK K3      3208012035700053D   + 03200                           0 NS   439852301",
		"SUSAP KLNKK3FI18-Y ALNK   040         0    R   CIY                    1470                                          0 NS   439862301",
		"SUSAP KLNKK3FI18-Y ALNK   050ESACOK3PC0EE B    CF IOCZK3      3573011017730072PI  + 03200                           0 NS   439872110",
		"SUSAP KLNKK3FI18-Y I      010ESACOK3PC0E  I    IF IOCZK3      35730110        PI  J 032000290018000                 0 NS   439882110",
		"SUSAP KLNKK3FI18-Y I      020CLONEK3PC0E  F    CF IOCZK3      3573007217700038PI  H 0290002837        -300LNK   K3D 0 NS   439892110",
		"SUSAP KLNKK3FI18-Y I      030RW18 K3PG0GY M    CF IOCZK3      3573002317700049PI    01250             -300          0 NS   439902110",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	appr, ok := result.Airports["KLNK"].Approaches["IY18"]
	if !ok {
		t.Fatalf("expected KLNK IY18 approach, got %v", result.Airports["KLNK"].Approaches)
	}

	transition := func(fix string) WaypointArray {
		idx := slices.IndexFunc(appr.Waypoints, func(wps WaypointArray) bool { return wps[0].Fix == fix })
		if idx == -1 {
			t.Fatalf("no %s transition in %v", fix, appr.Waypoints)
		}
		return appr.Waypoints[idx]
	}

	if got := transition("HUSKR").Encode(); !strings.Contains(got, "HUSKR/iaf/h199@t177 ") {
		t.Errorf("expected HUSKR to intercept the 177 course, got %q", got)
	}

	lnk := transition("LNK")
	idx := slices.IndexFunc(lnk, func(wp Waypoint) bool { return wp.Fix == "JUSAM" })
	if idx == -1 {
		t.Fatalf("no JUSAM in the LNK transition %q", lnk.Encode())
	}
	if pt := lnk[idx].ProcedureTurn(); pt == nil {
		t.Errorf("expected a procedure turn at JUSAM, got %q", lnk.Encode())
	} else if pt.Type != PTStandard45 || !pt.RightTurns {
		t.Errorf("expected a right-turn 45 procedure turn at JUSAM, got %+v", *pt)
	}
	if len(lnk[idx].ActionGroups()) != 0 {
		t.Errorf("expected no action groups at JUSAM, got %q", lnk.Encode())
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
