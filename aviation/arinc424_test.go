// pkg/aviation/arinc424_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"maps"
	gomath "math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
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
		gomath.Abs(float64(a.InboundCourse-b.InboundCourse)) < epsilon &&
		a.TurnDirection == b.TurnDirection &&
		gomath.Abs(float64(a.LegLengthNM-b.LegLengthNM)) < epsilon &&
		gomath.Abs(float64(a.LegMinutes-b.LegMinutes)) < epsilon &&
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

func TestParseARINC424StationDeclination(t *testing.T) {
	for _, tc := range []struct {
		subsection  byte
		field       string
		declination float32
		ok          bool
	}{
		{' ', "E0150", -15, true},
		{' ', "W0120", 12, true},
		{' ', "T0000", 0, true},  // oriented to true north
		{' ', "G0000", 0, false}, // oriented to grid north: no usable declination
		{'B', "W0120", 0, false}, // an NDB's record has its local variation here
	} {
		line := []byte(strings.Repeat(" ", 132))
		copy(line[0:], "SUSA")
		line[4] = 'D'
		line[5] = tc.subsection
		copy(line[13:], "SMO")
		line[21] = '1'
		copy(line[32:], "N34003700")
		copy(line[41:], "W118272400")
		copy(line[74:], tc.field)
		copy(line[93:], "SANTA MONICA")

		result := ParseARINC424(strings.NewReader(string(line) + "\r\n"))
		nav, ok := result.Navaids["SMO"]
		if !ok {
			t.Fatalf("%q: expected SMO navaid", tc.field)
		}
		if nav.HasDeclination != tc.ok || nav.Declination != tc.declination {
			t.Errorf("%q: expected declination %g, %v; got %g, %v", tc.field, tc.declination, tc.ok,
				nav.Declination, nav.HasDeclination)
		}
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
		h, ok := wp.HeadingAction()
		if !ok || h.Heading != tc.heading {
			t.Errorf("%s/%s: expected heading %d, got %+v", tc.star, wp.Fix, tc.heading, h)
		}
		if h.Track != tc.isTrack {
			t.Errorf("%s/%s: expected track %v, got %v", tc.star, wp.Fix, tc.isTrack, h.Track)
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
	if got := WaypointArray(wps).Encode(); got != "SHAMU/h135/@crs075 SARGS" {
		t.Fatalf("expected %q, got %q", "SHAMU/h135/@crs075 SARGS", got)
	}

	groups := wps[0].ActionGroups()
	if len(groups) != 1 {
		t.Fatalf("expected one action group at SHAMU, got %d", len(groups))
	}
	if hdg := groups[0].Actions.Heading; hdg.Heading != 135 || hdg.Track {
		t.Errorf("expected heading 135, got %+v", hdg)
	}
	if until := groups[0].Until; until.Type != WaypointActionCourse || until.Course != 75 {
		t.Errorf("expected a 075 course termination, got %+v", until)
	}
}

// The HUSKR transition of the KLNK ILS 18-Y flies a 199 heading off HUSKR to
// intercept the 177 localizer course to ESACO. Its LNK transition instead
// reverses course with an FC/CI pair: a 357 track from JUSAM for 5.3nm, then
// a right turn to 147 to intercept the localizer.
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

	if got := transition("HUSKR").Encode(); !strings.Contains(got, "HUSKR/iaf/h199/@crs177 ") {
		t.Errorf("expected HUSKR to intercept the 177 course, got %q", got)
	}

	want := "LNK/a3200+/iaf JUSAM/a3200+/t357/@d5.3/rt147/@crs177 ESACO/a3200+/if CLONE/a2900+/faf"
	if got := transition("LNK").Encode(); got != want {
		t.Errorf("LNK transition: expected %q, got %q", want, got)
	}
}

// FC/CI course reversals in the other two shapes the CIFP uses, and the
// feeder transitions that begin with an FC leg. KDDC ILS 14's FLACK
// transition ends at its CI, which intercepts the common route's course to
// RAVEN; the transition splices onto the common route there, skipping WEROM.
// KPMD VOR 25's PMD transition begins with the FC, so PMD is its first fix.
// PANC ILS 7L's ENA transition is the common case: an FC from the navaid
// collinear with the CF that follows it, flown as direct to the CF's fix.
func TestParseARINC424CourseReversal(t *testing.T) {
	lines := []string{
		"SUSAP KDDCK3FI14   AEARPP 010EARPPK3EA0E  A    IF                                             18000                 0 DS   655832207",
		"SUSAP KDDCK3FI14   AEARPP 020WEROMK3PC0EE BL   AF DDC K3      323301400763    D   + 04600                           0 DS   655842207",
		"SUSAP KDDCK3FI14   AFLACK 010FLACKK3EA0E       IF                                             18000                 0 NS   655852207",
		"SUSAP KDDCK3FI14   AFLACK 020DDC  K3D 0V  A    TF                                 + 04400                           0 NS   655862207",
		"SUSAP KDDCK3FI14   AFLACK 030OWENJK3PC0E       TF                                 + 04400                           0 NS   655872207",
		"SUSAP KDDCK3FI14   AFLACK 040OWENJK3PC0E       FC DDC K3      3560010030600063D   + 04400                           0 NS   655882301",
		"SUSAP KDDCK3FI14   AFLACK 050         0 E  L   CIY                    1760        + 04400                           0 NS   655892301",
		"SUSAP KDDCK3FI14   I      010WEROMK3PC0E  I    IF IDDCK3      32580201        PI  J 046000440018000                 0 NS   655922207",
		"SUSAP KDDCK3FI14   I      020RAVENK3PC0E  F    CF IDDCK3      3258006714600134PI  H 0440004400            DDC   K3D 0 NS   655932207",
		"SUSAP KDDCK3FI14   I      030RW14 K3PG0GY M    CF IDDCK3      3258001214600055PI    02623             -300          0 NS   655942207",
		"SUSAP KPMDK2FS25   APMD   010PMD  K2D 0V  A    FC PMD K2      0000000007000135D   + 05200     18000                 0 DS   905902204",
		"SUSAP KPMDK2FS25   APMD   020         0    R   CIY                    1751                                          0 DS   905912506",
		"SUSAP KPMDK2FS25   APMD   030CIVOKK2PC0EE BR   CFYPMD K2      0851012026510014D   + 05200                           0 DS   905922204",
		"SUSAP KPMDK2FS25   S      010CIVOKK2PC0E  I    IF PMD K2      08510120        D   + 05200     18000                 0 DS   905932204",
		"SUSAP KPMDK2FS25   S      011WUGITK2PC0E       CF PMD K2      0851007026510050D   + 04300                           0 DS   905942204",
		"SUSAP KPMDK2FS25   S      020THEROK2PC0E  F    CF PMD K2      0851004026510030D   + 04000                 PMD   K2D 0 DS   905952204",
		"SUSAP KPMDK2FS25   S      030EKOTYK2PC0EY M    CF PMD K2      0851000326510022D     02807             -304          0 DS   905972204",
		"SCANP PANCPAFI07L  AENA   010ENA  PAD 0V  A    FC ENA PA      0000000001140367D   + 02000     18000                 0 DS   105491202",
		"SCANP PANCPAFI07L  AENA   020AINKKPAPC0EE B    CF ITGNPA      2539016801140020PI  + 02000                           0 DS   105501802",
		"SCANP PANCPAFI07L  I      010AINKKPAPC0E  I    IF ITGNPA      25390168        PI  J 020000160018000                 0 DS   105511802",
		"SCANP PANCPAFI07L  I      011WUGSIPAPC0E       CF ITGNPA      2539012007400049PI  B 0330001600                      0 DS   105522310",
		"SCANP PANCPAFI07L  I      020WEBBIPAPC0E  F    CF ITGNPA      2539006407400056PI  H 0160001600            TED   PAD 0 DS   105531911",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	for _, tc := range []struct{ airport, approach, want string }{
		{"KDDC", "I14", "FLACK DDC/a4400+/iaf OWENJ/a4400+/t306/@d6.3/lt176/@crs146 RAVEN/a4400+/faf"},
		{"KPMD", "S25", "PMD/a5200+/iaf/t070/@d13.5/rt175/@crs265 CIVOK/a5200+/if WUGIT/a4300+ THERO/a4000+/faf"},
		{"PANC", "I7L", "ENA/a2000+/iaf AINKK/a2000+/if WUGSI/a1600-3300 WEBBI/a1600+/faf"},
	} {
		appr, ok := result.Airports[tc.airport].Approaches[tc.approach]
		if !ok {
			t.Fatalf("expected %s %s approach, got %v", tc.airport, tc.approach, result.Airports[tc.airport].Approaches)
		}
		first, _, _ := strings.Cut(tc.want, " ")
		first, _, _ = strings.Cut(first, "/")
		idx := slices.IndexFunc(appr.Waypoints, func(wps WaypointArray) bool { return wps[0].Fix == first })
		if idx == -1 {
			t.Errorf("%s %s: no transition starting at %s in %v", tc.airport, tc.approach, first, appr.Waypoints)
		} else if got := appr.Waypoints[idx].Encode(); got != tc.want {
			t.Errorf("%s %s: expected %q, got %q", tc.airport, tc.approach, tc.want, got)
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
		"4L":  "KJFK-22R/h044/@a520+ PONAE/flyover/h099",
		"4R":  "KJFK-22L/h044/@a520+/h099",
		"31L": "KJFK-13R/h314/@a520+/ld SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187",
		"31R": "KJFK-13L/h314/@a520+/ld SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187",
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
		runway, transition, exit, want string
	}{
		{"31L", "", "CANDR", "KJFK-13R/h314/@a520+/ld SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187 DEEZZ HEERO KURNL CANDR"},
		{"31L", "", "TOWIN", "KJFK-13R/h314/@a520+/ld SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187 DEEZZ HEERO KURNL CANDR TOWIN"},
		{"4R", "", "HEERO", "KJFK-22L/h044/@a520+/h099 DEEZZ HEERO"},
		{"4L", "", "SKORR", "KJFK-22R/h044/@a520+ PONAE/flyover/h099 DEEZZ HEERO"},
		{"31R", "", "SKORR", "KJFK-13L/h314/@a520+/ld SKORR/a2500+/s210-"},
		// An explicit transition is flown even though no fix of the route is the exit.
		{"4R", "TOWIN", "WHATE", "KJFK-22L/h044/@a520+/h099 DEEZZ HEERO KURNL CANDR TOWIN"},
		// An explicit transition still ends at the exit when the exit is on it.
		{"31L", "TOWIN", "KURNL", "KJFK-13R/h314/@a520+/ld SKORR/a2500+/s210- CESID/a2500+/s250- YNKEE/t187 DEEZZ HEERO KURNL"},
	} {
		wps, err := sid.Waypoints(tc.runway, tc.transition, tc.exit)
		if err != nil {
			t.Errorf("%s/%s: %v", tc.runway, tc.exit, err)
		} else if got := wps.Encode(); got != tc.want {
			t.Errorf("%s/%s: expected %q, got %q", tc.runway, tc.exit, tc.want, got)
		}
	}
	if _, err := sid.Waypoints("13L", "", "CANDR"); err == nil {
		t.Errorf("expected an error for a runway the SID doesn't serve")
	}
	if _, err := sid.Waypoints("31L", "CANDER", "CANDR"); err == nil {
		t.Errorf("expected an error for an unknown enroute transition")
	}
}

// SID legs to and along radials: DALLS1's headings to the LTJ 165 radial
// (VR) then a course to an altitude along it, DVT3's climb on the PXR 336
// radial from wherever the runway heading ends (FA after VA) and after a
// heading to intercept it (VI/FA), and FLOUT5's enroute transition that
// starts with a track from the fix it begins at (FC).
func TestParseARINC424SIDRadials(t *testing.T) {
	lines := []string{
		"SUSAP KDLSK1ADLS     0     050YHN45370968W121100579E015000247         1800018000C    MNAR    COLUMBIA GORGE RGNL/THE DALLES731961303",
		"SUSAP KDLSK1DDALLS11RW07  010         0        CA                     0690        + 00647     18000                        732021313",
		"SUSAP KDLSK1DDALLS11RW07  020         0        VR LTJ K1      1650    1200    D                                            732031313",
		"SUSAP KDLSK1DDALLS11RW07  030         0        CA                     1650        + 04000                                  732041313",
		"SUSAP KDLSK1DDALLS11RW07  040LTJ  K1D 0VE  L   DF                                                                          732052007",
		"SUSAP KDLSK1DDALLS11RW13  010         0        VR LTJ K1      1650    1296    D               18000                        732061313",
		"SUSAP KDLSK1DDALLS11RW13  020         0        CA                     1650        + 04000                                  732071313",
		"SUSAP KDLSK1DDALLS11RW13  030LTJ  K1D 0VE  L   DF                                                                          732082007",
		"SUSAP KDLSK1DDALLS11RW31  010         0        CA                     3060        + 00647     18000                        732091313",
		"SUSAP KDLSK1DDALLS11RW31  020         0    L   VRYLTJ K1      1650    1200    D                                            732101313",
		"SUSAP KDLSK1DDALLS11RW31  030         0        CA                     1650        + 04000                                  732111313",
		"SUSAP KDLSK1DDALLS11RW31  040LTJ  K1D 0VE  L   DF                                                                          732122007",
		"SUSAP KDLSK1GRW07    0046470730 N45371505W121103468               00212044050100D                                          732261303",
		"SUSAP KDLSK1GRW13    0050971300 N45372309W121102275               00211020050100D                                          732271313",
		"SUSAP KDLSK1GRW25    0046472530 N45371642W121093828               00243019643100I                                          732281702",
		"SUSAP KDLSK1GRW31    0050973100 N45364368W121094284               00239000050100D                                          732291313",
		"SUSAP KDVTK2ADVT     0     081YHN33411790W112045720E012001478         1800018000C    MNAR    PHOENIX DEER VALLEY           784940810",
		"SUSAP KDVTK2DDVT3  1RW07B 010         0        VA                     0740        + 02200     18000       PXR   K2D        785142308",
		"SUSAP KDVTK2DDVT3  1RW07B 020PXR  K2D 0V   L   FAYPXR K2      000000003360    D   + 04000                                  785152308",
		"SUSAP KDVTK2DDVT3  1RW07B 030PXR  K2D 0VE  L   DF                                                                          785162308",
		"SUSAP KDVTK2DDVT3  1RW25B 010         0        VA                     2540        + 01878     18000                        785172308",
		"SUSAP KDVTK2DDVT3  1RW25B 020         0    R   VIY                    0600                                                 785182308",
		"SUSAP KDVTK2DDVT3  1RW25B 030PXR  K2D 0V       FA PXR K2      000000003360    D   + 04000                                  785192308",
		"SUSAP KDVTK2DDVT3  1RW25B 040PXR  K2D 0VE  L   DF                                                                          785202308",
		"SUSAP KDVTK2GRW07L   0045000740 N33412098W112052203         +0414001455000051075V                                          786901705",
		"SUSAP KDVTK2GRW07R   0081960740 N33411322W112053597         +0411001445089842100R                                          786911705",
		"SUSAP KDVTK2GRW25L   0081962540 N33411761W112042062         +0420201475091640100R                                          786922302",
		"SUSAP KDVTK2GRW25R   0045002540 N33412407W112042890         +0420601477000048075V                                          786931612",
		"SUSAP KSBAK2ASBA     0     060YHN34253429W119502937E014000014         1800018000C    MNAR    SANTA BARBARA MUNI            093472309",
		"SUSAP KSBAK2DFLOUT53GVO   010FLOUTK2PC0E       FC GVO K2      1413017832100150D   + 06000     18000                        093881310",
		"SUSAP KSBAK2DFLOUT53GVO   020GVO  K2D 0VE      CF GVO K2      0000000032100030D                                            093890804",
	}
	result := ParseARINC424(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))

	for _, tc := range []struct {
		airport, sid, runway, want string
	}{
		{"KDLS", "DALLS1", "7", "KDLS-25/t069/@a647+/h120/@LTJ-R165/tLTJ-R165/@a4000+/ld LTJ"},
		{"KDLS", "DALLS1", "13", "KDLS-31/h130/@LTJ-R165/tLTJ-R165/@a4000+/ld LTJ"},
		{"KDLS", "DALLS1", "31", "KDLS-13/t306/@a647+/l120/@LTJ-R165/tLTJ-R165/@a4000+/ld LTJ"},
		{"KDVT", "DVT3", "7L", "KDVT-25R/h074/@a2200+/ltPXR-R336/@a4000+/ld PXR"},
		{"KDVT", "DVT3", "7R", "KDVT-25L/h074/@a2200+/ltPXR-R336/@a4000+/ld PXR"},
		{"KDVT", "DVT3", "25L", "KDVT-7R/h254/@a1878+/r060/@PXR-R336/tPXR-R336/@a4000+/ld PXR"},
		{"KDVT", "DVT3", "25R", "KDVT-7L/h254/@a1878+/r060/@PXR-R336/tPXR-R336/@a4000+/ld PXR"},
	} {
		sid, ok := result.Airports[tc.airport].SIDs[tc.sid]
		if !ok {
			t.Fatalf("expected %s %s SID, got %v", tc.airport, tc.sid, result.Airports[tc.airport].SIDs)
		}
		wps, ok := sid.RunwayTransitions[tc.runway]
		if !ok {
			t.Errorf("%s %s: no runway %s transition; have %v", tc.airport, tc.sid, tc.runway,
				slices.Sorted(maps.Keys(sid.RunwayTransitions)))
		} else if got := wps.Encode(); got != tc.want {
			t.Errorf("%s %s runway %s: expected %q, got %q", tc.airport, tc.sid, tc.runway, tc.want, got)
		}
	}

	flout5 := result.Airports["KSBA"].SIDs["FLOUT5"]
	if got, want := flout5.EnrouteTransitions["GVO"].Encode(), "FLOUT/t321/@d15.0 GVO"; got != want {
		t.Errorf("FLOUT5 GVO transition: expected %q, got %q", want, got)
	}
}

// Every route in the CIFP has to survive being encoded in the scenario
// waypoint syntax and parsed back, since that is how "vice -routes" shows
// them and how scenarios reproduce them.
func TestCIFPRoutesRoundTrip(t *testing.T) {
	InitDB()

	// Emptied of everything it held, a waypoint's Extra is no different
	// from no Extra at all.
	normalize := func(wps WaypointArray) WaypointArray {
		wps = wps.Clone()
		for i := range wps {
			wps[i].Location = math.Point2LL{}
			if e := wps[i].Extra; e != nil {
				if len(e.ActionGroups) == 0 {
					e.ActionGroups = nil
				}
				if reflect.DeepEqual(*e, WaypointExtra{}) {
					wps[i].Extra = nil
				}
			}
		}
		return wps
	}

	failures := 0
	check := func(label string, wps WaypointArray) {
		if len(wps) == 0 || failures >= 20 {
			return
		}
		encoded := wps.Encode()
		parsed, err := parseWaypoints(encoded)
		if err != nil {
			t.Errorf("%s: %q: %v", label, encoded, err)
			failures++
		} else if reencoded := parsed.Encode(); reencoded != encoded {
			t.Errorf("%s: %q re-encodes as %q", label, encoded, reencoded)
			failures++
		} else if a, b := normalize(wps), normalize(parsed); !reflect.DeepEqual(a, b) {
			t.Errorf("%s: %q parses back to different waypoints:\n%+v\n%+v", label, encoded, a, b)
			failures++
		}
	}

	for _, icao := range util.SortedMapKeys(DB.Airports) {
		ap := DB.Airports[icao]
		for name, sid := range ap.SIDs {
			for rwy, wps := range sid.RunwayTransitions {
				check(icao+" "+name+" runway "+rwy, wps)
			}
			check(icao+" "+name, sid.Common)
			for tr, wps := range sid.EnrouteTransitions {
				check(icao+" "+name+" "+tr, wps)
			}
		}
		for name, star := range ap.STARs {
			for tr, wps := range star.Transitions {
				check(icao+" "+name+" "+tr, wps)
			}
			for rwy, wps := range star.RunwayWaypoints {
				check(icao+" "+name+" runway "+rwy, wps)
			}
		}
		for name, appr := range ap.Approaches {
			for i, wps := range appr.Waypoints {
				check(fmt.Sprintf("%s %s #%d", icao, name, i), wps)
			}
		}
	}
}
