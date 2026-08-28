// pkg/aviation/arinc424_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"maps"
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

// The KJFK DEEZZ6 leaves 31L/R on a heading to 520' and then via SKORR to
// YNKEE, where it ends in vectors; 4L/R climb to 520' before vectors. The
// common route and enroute transitions pick up at DEEZZ.
func TestParseARINC424SID(t *testing.T) {
	lines := []string{
		"SUSAP KJFKK6AJFK     0     145YHN40382374W073464329W013000013         1800018000C    MNAR    JOHN F KENNEDY INTL           300791912",
		"SUSAP KJFKK6DDEEZZ64RW04L 010         0        VA                     0438        + 00520     18000       KJFK  K6PA       301432603",
		"SUSAP KJFKK6DDEEZZ64RW04L 020PONAEK6PC0EY      DF                                                                          301442603",
		"SUSAP KJFKK6DDEEZZ64RW04L 030         0 E      VM                     0990                                                 301452603",
		"SUSAP KJFKK6DDEEZZ64RW04R 010         0        VA                     0438        + 00520     18000                        301462603",
		"SUSAP KJFKK6DDEEZZ64RW04R 020         0 E      VM                     0990                                                 301472603",
		"SUSAP KJFKK6DDEEZZ64RW31B 010         0        VA                     3138        + 00520     18000                        301482603",
		"SUSAP KJFKK6DDEEZZ64RW31B 020SKORRK6PC0E   L   DF                                 + 02500          210               -     301492603",
		"SUSAP KJFKK6DDEEZZ64RW31B 030CESIDK6PC0E       TF                                 + 02500          250               -     301502603",
		"SUSAP KJFKK6DDEEZZ64RW31B 040YNKEEK6PC0E       TF                                                                          301512603",
		"SUSAP KJFKK6DDEEZZ64RW31B 050YNKEEK6PC0EE      FM CCC K6      254205491870    D                                            301522603",
		"SUSAP KJFKK6DDEEZZ65      010DEEZZK6EA0E       IF                                             18000                        301532603",
		"SUSAP KJFKK6DDEEZZ65      020HEEROK6EA0EE      TF                                                                          301542603",
		"SUSAP KJFKK6DDEEZZ66CANDR 010HEEROK6EA0E       IF                                             18000                        301552603",
		"SUSAP KJFKK6DDEEZZ66CANDR 020KURNLK6EA0E       TF                                                                          301562603",
		"SUSAP KJFKK6DDEEZZ66CANDR 030CANDRK6EA0EE      TF                                                                          301572603",
		"SUSAP KJFKK6DDEEZZ66TOWIN 010HEEROK6EA0E       IF                                             18000                        301582603",
		"SUSAP KJFKK6DDEEZZ66TOWIN 020KURNLK6EA0E       TF                                                                          301592603",
		"SUSAP KJFKK6DDEEZZ66TOWIN 030CANDRK6EA0E       TF                                                                          301602603",
		"SUSAP KJFKK6DDEEZZ66TOWIN 040TOWINK6EA0EE      TF                                                                          301612603",
		"SUSAP KJFKK6GRW04L   0120790440 N40372318W073470505         -0028300012046057200IIHIQ1                                     305401709",
		"SUSAP KJFKK6GRW04R   0084000440 N40373154W073461324         -0028400012000053200IIJFK3                                     305412308",
		"SUSAP KJFKK6GRW13L   0100001340 N40392337W073471475         -0027900013090758200IITLK2                                     305422504",
		"SUSAP KJFKK6GRW13R   0145111340 N40384378W073483739         -0028100013204455200R                                          305432506",
		"SUSAP KJFKK6GRW22L   0084002240 N40384285W073451750         -0028300012000053200IIIWY3                                     305442308",
		"SUSAP KJFKK6GRW22R   0120792240 N40383276W073461069         -0028100013342459200IIJOC1                                     305452308",
		"SUSAP KJFKK6GRW31L   0145113140 N40375728W073465479         -0028100013326458200IIMOH1                                     305462308",
		"SUSAP KJFKK6GRW31R   0100003140 N40384260W073454483         -0028100013102742200IIRTH1                                     305472504",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	sid, ok := result.Airports["KJFK"].SIDs["DEEZZ6"]
	if !ok {
		t.Fatalf("expected KJFK DEEZZ6 SID, got %v", result.Airports["KJFK"].SIDs)
	}

	runways := map[string]string{
		"4L":  "KJFK-22R/h044@a520 PONAE/flyover/h099",
		"4R":  "KJFK-22L/h044@a520/h099",
		"31L": "KJFK-13R/h314@a520 SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187",
		"31R": "KJFK-13L/h314@a520 SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187",
	}
	if len(sid.RunwayTransitions) != len(runways) {
		t.Errorf("expected runway transitions for %v, got %v", slices.Sorted(maps.Keys(runways)),
			slices.Sorted(maps.Keys(sid.RunwayTransitions)))
	}
	for rwy, want := range runways {
		if got := sid.RunwayTransitions[rwy].Encode(); got != want {
			t.Errorf("runway %s: expected %q, got %q", rwy, want, got)
		}
	}
	if skorr := sid.RunwayTransitions["31L"][1]; skorr.Turn() != TurnLeft {
		t.Errorf("expected a left turn to SKORR, got %v", skorr.Turn())
	}

	if got := sid.Common.Encode(); got != "DEEZZ HEERO" {
		t.Errorf("expected common route DEEZZ HEERO, got %q", got)
	}
	if got := sid.EnrouteTransitions["TOWIN"].Encode(); got != "DEEZZ HEERO KURNL CANDR TOWIN" {
		t.Errorf("expected the TOWIN transition to start with the common route, got %q", got)
	}

	for _, tc := range []struct {
		runway, exit, want string
	}{
		{"31L", "CANDR", "KJFK-13R/h314@a520 SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187 DEEZZ HEERO KURNL CANDR"},
		{"31L", "TOWIN", "KJFK-13R/h314@a520 SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187 DEEZZ HEERO KURNL CANDR TOWIN"},
		{"4R", "HEERO", "KJFK-22L/h044@a520/h099 DEEZZ HEERO"},
		{"4L", "SKORR", "KJFK-22R/h044@a520 PONAE/flyover/h099 DEEZZ HEERO"},
		{"31R", "SKORR", "KJFK-13L/h314@a520 SKORR/a2500+/s210-"},
	} {
		wps, err := sid.Waypoints(tc.runway, tc.exit)
		if err != nil {
			t.Errorf("%s/%s: %v", tc.runway, tc.exit, err)
		} else if got := wps.Encode(); got != tc.want {
			t.Errorf("%s/%s: expected %q, got %q", tc.runway, tc.exit, tc.want, got)
		}
	}
	if _, err := sid.Waypoints("13L", "CANDR"); err == nil {
		t.Errorf("expected an error for a runway the SID doesn't serve")
	}
}
