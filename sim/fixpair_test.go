// sim/fixpair_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
)

func TestMatchACIDPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern, acid string
		want          bool
	}{
		{"*", "AAL123", true},
		{"", "AAL123", true},
		{"AAL***", "AAL123", true},  // literal prefix + any
		{"AALddd", "AAL123", true},  // digit class
		{"AALddd", "AAL12A", false}, // last not a digit
		{"aaaddd", "AAL123", true},  // letter+digit classes
		{"aaaddd", "AA1123", false}, // third not a letter
		{"AAL123", "AAL123", true},  // exact literal
		{"AAL124", "AAL123", false}, // literal mismatch
		{"AAL12", "AAL123", false},  // length mismatch
		{"DAL*", "DAL9", true},      // uppercase D is literal, not digit-meta
		{"dAL9", "9AL9", true},      // lowercase d = digit class
		{"dAL9", "AAL9", false},     // first not a digit
		// A trailing run of '*'s matches zero or more characters.
		{"Nd*****", "N123AB", true},    // shorter than the pattern
		{"Nd*****", "N1", true},        // trailing stars all match nothing
		{"Nd*****", "N", false},        // fixed prefix unsatisfied
		{"FDX****", "FDX9", true},      // 4-char callsign vs 7-char pattern
		{"FDX****", "FDX12345", false}, // longer than the pattern
		{"AAL*23", "AAL123", true},     // mid-pattern '*' is a single character
		{"AAL*23", "AAL23", false},     // ...and cannot match nothing
	} {
		if got := matchACIDPattern(tc.pattern, tc.acid); got != tc.want {
			t.Errorf("matchACIDPattern(%q, %q) = %v, want %v", tc.pattern, tc.acid, got, tc.want)
		}
	}
}
func TestLevelBandUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want LevelBand
	}{
		{`"*"`, LevelBand{Any: true}},
		{`""`, LevelBand{Any: true}},
		{`"VFR"`, LevelBand{VFR: true}},
		{`"otp"`, LevelBand{OTP: true}},
		{`[0,180]`, LevelBand{HasRange: true, Range: [2]int{0, 180}}},
	} {
		var lb LevelBand
		if err := json.Unmarshal([]byte(tc.in), &lb); err != nil {
			t.Errorf("Unmarshal(%s): %v", tc.in, err)
			continue
		}
		if lb != tc.want {
			t.Errorf("Unmarshal(%s) = %+v, want %+v", tc.in, lb, tc.want)
		}
	}
	var lb LevelBand
	if err := json.Unmarshal([]byte(`"nonsense"`), &lb); err == nil {
		t.Errorf("Unmarshal of invalid level_band should have errored")
	}
}
func TestLevelBandMatches(t *testing.T) {
	for _, tc := range []struct {
		lb   LevelBand
		in   FixPairInput
		want bool
	}{
		{LevelBand{Any: true}, FixPairInput{Level: 350}, true},
		{LevelBand{}, FixPairInput{Level: 350}, true}, // unspecified = any
		{LevelBand{HasRange: true, Range: [2]int{0, 180}}, FixPairInput{Level: 120}, true},
		{LevelBand{HasRange: true, Range: [2]int{0, 180}}, FixPairInput{Level: 181}, false},
		{LevelBand{VFR: true}, FixPairInput{VFR: true}, true},
		{LevelBand{VFR: true}, FixPairInput{VFR: false}, false},
		{LevelBand{OTP: true}, FixPairInput{OTP: true}, true},
	} {
		if got := tc.lb.matches(tc.in); got != tc.want {
			t.Errorf("LevelBand %+v matches %+v = %v, want %v", tc.lb, tc.in, got, tc.want)
		}
	}
}
func TestFixPairReassign(t *testing.T) {
	cfg := &FixPairConfiguration{
		Reassignments: []FixPairReassignment{
			// Low arrivals from PVA get rewritten to ROB->BOS.
			{
				FpFixPair: [2]string{"PVA", "*"}, TypeOfFlight: "A",
				AltBands:       LevelBand{HasRange: true, Range: [2]int{0, 180}},
				DerivedFixPair: [2]string{"ROB", "BOS"},
			},
			// A specific callsign keeps its entry but changes exit.
			{
				ACID:           "AALddd",
				DerivedFixPair: [2]string{"*", "BOS"},
			},
		},
	}
	for _, tc := range []struct {
		name string
		in   FixPairInput
		want FixPair
	}{
		{
			"low PVA arrival reassigned",
			FixPairInput{Entry: "PVA", Exit: "KBOS", FlightType: av.FlightTypeArrival, Level: 120},
			FixPair{Entry: "ROB", Exit: "BOS"},
		},
		{
			"high PVA arrival not reassigned by first rule",
			FixPairInput{Entry: "PVA", Exit: "KBOS", FlightType: av.FlightTypeArrival, Level: 350, ACID: "UAL55"},
			FixPair{Entry: "PVA", Exit: "KBOS"},
		},
		{
			"AAL callsign keeps entry, derives exit",
			FixPairInput{Entry: "GDM", Exit: "KBOS", FlightType: av.FlightTypeArrival, Level: 350, ACID: "AAL123"},
			FixPair{Entry: "GDM", Exit: "BOS"},
		},
		{
			"first match wins (low AAL PVA hits rule 1 not rule 2)",
			FixPairInput{Entry: "PVA", Exit: "KBOS", FlightType: av.FlightTypeArrival, Level: 100, ACID: "AAL123"},
			FixPair{Entry: "ROB", Exit: "BOS"},
		},
	} {
		if got := cfg.Reassign(tc.in, nil); got != tc.want {
			t.Errorf("%s: Reassign = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	// Nil config is a no-op passthrough.
	var nilCfg *FixPairConfiguration
	if got := nilCfg.Reassign(FixPairInput{Entry: "A", Exit: "B"}, nil); got != (FixPair{Entry: "A", Exit: "B"}) {
		t.Errorf("nil Reassign changed the pair: %+v", got)
	}
}
func TestMatchACTypeClass(t *testing.T) {
	classes := map[string][]string{
		"J": {"A320", "B738"},
		"T": {"PC12", "SF34"},
	}
	for _, tc := range []struct {
		field, acType string
		want          bool
	}{
		{"*", "A320", true},
		{"", "C172", true},
		{"J", "A320", true},  // class member
		{"J", "C172", false}, // not in the class
		{"T", "PC12", true},
		{"T", "A320", false},
		{"X", "A320", false}, // undefined class matches nothing
	} {
		if got := matchACTypeClass(classes, tc.field, tc.acType); got != tc.want {
			t.Errorf("matchACTypeClass(%q,%q) = %v, want %v", tc.field, tc.acType, got, tc.want)
		}
	}
	// Wired through a reassignment: a J-class rule fires for a member type only.
	cfg := &FixPairConfiguration{Reassignments: []FixPairReassignment{
		{FpFixPair: [2]string{"SRQ", "*"}, ACType: "J", DerivedFixPair: [2]string{"*", "BLR"}},
	}}
	if got := cfg.Reassign(FixPairInput{Entry: "SRQ", Exit: "*", ACType: "A320"}, classes); got.Exit != "BLR" {
		t.Errorf("A320 SRQ should reassign exit to BLR, got %+v", got)
	}
	if got := cfg.Reassign(FixPairInput{Entry: "SRQ", Exit: "*", ACType: "C172"}, classes); got.Exit == "BLR" {
		t.Errorf("C172 SRQ should not match the J-class rule, got %+v", got)
	}
}
func TestFixPairActiveRunway(t *testing.T) {
	cfg := &FixPairConfiguration{Reassignments: []FixPairReassignment{
		{FpFixPair: [2]string{"PVA", "*"}, ActiveRunway: "27", DerivedFixPair: [2]string{"ROB", "*"}},
	}}
	on27 := FixPairInput{Entry: "PVA", Exit: "BOS", ActiveRunways: []string{"27", "22L"}}
	if got := cfg.Reassign(on27, nil); got.Entry != "ROB" {
		t.Errorf("active runway 27 should reassign entry to ROB, got %+v", got)
	}
	on4 := FixPairInput{Entry: "PVA", Exit: "BOS", ActiveRunways: []string{"4R", "9"}}
	if got := cfg.Reassign(on4, nil); got.Entry != "PVA" {
		t.Errorf("runway 27 rule should not fire on a 4R/9 config, got %+v", got)
	}
}
func TestFixPairLevel(t *testing.T) {
	// Departures use the requested altitude; arrivals/overflights the assigned
	// altitude, each falling back to the other when unset.
	dep := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, RequestedAltitude: 24000, AssignedAltitude: 10000}
	if lvl := fixPairLevel(dep); lvl != 240 {
		t.Errorf("departure level = %d, want 240 (requested)", lvl)
	}
	arr := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, RequestedAltitude: 35000, AssignedAltitude: 11000}
	if lvl := fixPairLevel(arr); lvl != 110 {
		t.Errorf("arrival level = %d, want 110 (assigned)", lvl)
	}
	ovf := &NASFlightPlan{TypeOfFlight: av.FlightTypeOverflight, RequestedAltitude: 24000}
	if lvl := fixPairLevel(ovf); lvl != 240 {
		t.Errorf("overflight level = %d, want 240 (requested fallback)", lvl)
	}
}
func TestResolveEndpoint(t *testing.T) {
	fa := &FacilityAdaptation{
		SignificantPoints: map[string]SignificantPoint{
			"ROBUCC": {ShortName: "ROB"},
			"PVA":    {},
		},
		Airports: map[string]*FixPairAirport{"BOS": {}},
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"*", true},
		{"", true},
		{"PVA", true},     // 3-char significant point by name
		{"ROB", true},     // longer point's short name
		{"BOS", true},     // airport
		{"ROBUCC", false}, // full name longer than a flight plan fix carries
		{"ZZZ", false},
	} {
		if got := fa.resolveEndpoint(tc.id); got != tc.want {
			t.Errorf("resolveEndpoint(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
func TestFixPairAssignTCP(t *testing.T) {
	cfg := &FixPairConfiguration{
		Plans: map[string]string{"CNE": "Northeast flow", "CSW": "Southwest flow"},
		Assignments: FixPairAssignments{
			Arrival: []FixPairAssignmentRow{
				{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"CNE": "1M", "CSW": "1M"}},
				{FixPair: [2]string{"PVA", "BOS"}, TCP: map[string]TCP{"CNE": "1L", "CSW": "1U"}},
				{FixPair: [2]string{"*", "BOS"}, TCP: map[string]TCP{"CNE": "1D", "CSW": "1D"}}, // catch-all
			},
			Departure: []FixPairAssignmentRow{
				{FixPair: [2]string{"BOS", "*"}, TCP: map[string]TCP{"CNE": "1D", "CSW": "1D"}},
			},
		},
	}
	for _, tc := range []struct {
		name    string
		pair    FixPair
		ft      av.TypeOfFlight
		plan    string
		wantTCP TCP
		wantOK  bool
	}{
		{"ROB/BOS arrival on CNE", FixPair{"ROB", "BOS"}, av.FlightTypeArrival, "CNE", "1M", true},
		{"PVA/BOS arrival on CSW (plan differs)", FixPair{"PVA", "BOS"}, av.FlightTypeArrival, "CSW", "1U", true},
		{"unknown entry falls to catch-all", FixPair{"ZZZ", "BOS"}, av.FlightTypeArrival, "CNE", "1D", true},
		{"departure BOS->anywhere", FixPair{"BOS", "PVD"}, av.FlightTypeDeparture, "CNE", "1D", true},
		{"no overflight rows", FixPair{"NNN", "BOS"}, av.FlightTypeOverflight, "CNE", "", false},
		{"exit mismatch, no row", FixPair{"ROB", "LGA"}, av.FlightTypeArrival, "CNE", "", false},
		{"unknown plan yields no tcp", FixPair{"ROB", "BOS"}, av.FlightTypeArrival, "XXX", "", false},
	} {
		gotTCP, gotOK := cfg.AssignTCP(tc.pair, tc.ft, tc.plan)
		if gotTCP != tc.wantTCP || gotOK != tc.wantOK {
			t.Errorf("%s: AssignTCP = (%q, %v), want (%q, %v)", tc.name, gotTCP, gotOK, tc.wantTCP, tc.wantOK)
		}
	}
}
func TestFixPairValidate(t *testing.T) {
	fa := &FacilityAdaptation{
		SignificantPoints:    map[string]SignificantPoint{"ROB": {}, "PVA": {}},
		Airports:             map[string]*FixPairAirport{"BOS": {}},
		Configurations:       map[string]*FacilityConfiguration{"CNE": {}},
		TCPAssignmentClasses: map[string][]string{"J": {"A320"}},
	}
	ctrl := map[TCP]*av.Controller{"1M": {}, "1D": {}}
	validCount := func(c *FixPairConfiguration) int {
		var e util.ErrorLogger
		c.validate(fa, ctrl, &e)
		n := 0
		for range e.Errors() {
			n++
		}
		return n
	}
	// A clean configuration produces no errors.
	good := &FixPairConfiguration{
		Plans: map[string]string{"CNE": "flow"},
		Reassignments: []FixPairReassignment{
			{FpFixPair: [2]string{"PVA", "*"}, TypeOfFlight: "A", ACType: "J",
				ActiveRunway: "22L", DerivedFixPair: [2]string{"ROB", "BOS"}},
		},
		Assignments: FixPairAssignments{
			Arrival: []FixPairAssignmentRow{{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"CNE": "1M"}}},
		},
	}
	if n := validCount(good); n != 0 {
		t.Errorf("valid config reported %d errors", n)
	}
	// Each of these introduces exactly one problem.
	bad := &FixPairConfiguration{
		Plans: map[string]string{"CNE": "flow", "ZZZ": "not a config id"}, // plan not in Configurations
		Reassignments: []FixPairReassignment{
			{TypeOfFlight: "Z"},                      // bad type_of_flight
			{FpFixPair: [2]string{"NOPE", "*"}},      // unknown fp fix
			{DerivedFixPair: [2]string{"*", "GONE"}}, // unknown derived fix
			{ACType: "X"},                            // undefined aircraft type class
			{ActiveRunway: "27Q"},                    // bad runway id
		},
		Assignments: FixPairAssignments{
			Arrival: []FixPairAssignmentRow{
				{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"CNE": "9Z"}}, // tcp not a control position
				{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"XXX": "1M"}}, // plan not in Plans
				{FixPair: [2]string{"ZZZ", "BOS"}, TCP: map[string]TCP{"CNE": "1D"}}, // unknown entry endpoint
				{FixPair: [2]string{"ROB", "BOS"}},                                   // no tcp assignments
			},
		},
	}
	if n := validCount(bad); n != 10 {
		t.Errorf("bad config reported %d errors, want 10", n)
	}
}
func TestLevelBandRoundTrip(t *testing.T) {
	for _, lb := range []LevelBand{
		{Any: true}, {VFR: true}, {OTP: true},
		{HasRange: true, Range: [2]int{0, 100}},
		{HasRange: true, Range: [2]int{50, 999}},
	} {
		b, err := json.Marshal(lb)
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", lb, err)
		}
		var got LevelBand
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("Unmarshal(%s): %v", b, err)
			continue
		}
		if got != lb {
			t.Errorf("round-trip %+v -> %s -> %+v", lb, b, got)
		}
	}
}
func TestAssignTCPStarFallback(t *testing.T) {
	cfg := &FixPairConfiguration{Assignments: FixPairAssignments{
		Arrival: []FixPairAssignmentRow{
			{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"CO1": "1M", "*": "1D"}},
		},
	}}
	// Active plan not in the tcp map falls back to "*".
	if tcp, ok := cfg.AssignTCP(FixPair{"ROB", "BOS"}, av.FlightTypeArrival, "EAR"); !ok || tcp != "1D" {
		t.Errorf("EAR (not a key) should fall back to *: got %q, %v", tcp, ok)
	}
	// Exact plan match still wins over "*".
	if tcp, _ := cfg.AssignTCP(FixPair{"ROB", "BOS"}, av.FlightTypeArrival, "CO1"); tcp != "1M" {
		t.Errorf("CO1 should match exactly: got %q", tcp)
	}
}

// TestApplyFixPairAssignment exercises the sim-side consumption of the fix-pair
// table: applyFixPairAssignment reads the facility's
// FixPairConfiguration and the active plan (ConfigurationId) and sets the owning
// position. It also verifies a "*"/"*" catch-all row assigns every arrival,
// including ones whose coordination fix is not otherwise in the table.
func TestApplyFixPairAssignment(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := NewTestSim(lg)
	s.State.ConfigurationId = "CNE"
	s.State.FacilityAdaptation.FixPairConfiguration = &FixPairConfiguration{
		Plans: map[string]string{"CNE": "Northeast", "CSW": "Southwest"},
		Assignments: FixPairAssignments{
			Arrival: []FixPairAssignmentRow{
				{FixPair: [2]string{"PVA", "BOS"}, TCP: map[string]TCP{"CNE": "1L", "CSW": "1U"}},
				{FixPair: [2]string{"*", "*"}, TCP: map[string]TCP{"CNE": "1U", "CSW": "1U"}}, // catch-all
			},
		},
	}
	for _, tc := range []struct {
		name  string
		entry string
		want  TCP
	}{
		{"specific row wins", "PVA", "1L"},
		{"catch-all for unlisted fix", "ZZZ", "1U"},
		{"catch-all even with empty entry", "", "1U"},
	} {
		fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, EntryFix: tc.entry, ExitFix: "BOS"}
		if !s.applyFixPairAssignment(fp) {
			t.Errorf("%s: applyFixPairAssignment returned false", tc.name)
			continue
		}
		if fp.InboundHandoffController != tc.want {
			t.Errorf("%s: InboundHandoffController = %q, want %q", tc.name, fp.InboundHandoffController, tc.want)
		}
	}
	// Departures: the fix-pair sets the OWNER (TrackingController/OwningTCW) when
	// the departure is owned by a human position, not just the handoff target.
	s.ControlPositions = map[TCP]*av.Controller{"1D": {}, "1L": {}, "9V": {}}
	s.ScenarioDefaultConsolidation = PositionConsolidation{"1D": {"1L"}} // 9V is virtual (not in tree)
	s.State.FacilityAdaptation.FixPairConfiguration.Assignments.Departure = []FixPairAssignmentRow{
		{FixPair: [2]string{"BOS", "*"}, TCP: map[string]TCP{"CNE": "1L"}},
	}
	// Human-owned departure: owner becomes the fix-pair position.
	dep := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, EntryFix: "BOS", ExitFix: "ORW", TrackingController: "1D"}
	if !s.applyFixPairAssignment(dep) {
		t.Errorf("human departure: applyFixPairAssignment returned false")
	}
	if dep.TrackingController != "1L" || dep.InboundHandoffController != "1L" {
		t.Errorf("human departure: owner=%q handoff=%q, want both 1L", dep.TrackingController, dep.InboundHandoffController)
	}
	// Virtual (auto-release) departure: owner stays virtual; only handoff set.
	vdep := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, EntryFix: "BOS", ExitFix: "ORW", TrackingController: "9V"}
	if !s.applyFixPairAssignment(vdep) {
		t.Errorf("virtual departure: applyFixPairAssignment returned false")
	}
	if vdep.TrackingController != "9V" {
		t.Errorf("virtual departure: owner = %q, want unchanged 9V", vdep.TrackingController)
	}
	if vdep.InboundHandoffController != "1L" {
		t.Errorf("virtual departure: handoff = %q, want 1L", vdep.InboundHandoffController)
	}
	// Reassignment derives a pair without overwriting the flight plan's actual
	// fixes; the derived pair drives the table match and the row's RNAV flag.
	s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol = true
	s.State.FacilityAdaptation.FixPairConfiguration = &FixPairConfiguration{
		Reassignments: []FixPairReassignment{
			{FpFixPair: [2]string{"PVA", "*"}, DerivedFixPair: [2]string{"ROB", "*"}},
		},
		Assignments: FixPairAssignments{
			Arrival: []FixPairAssignmentRow{
				{FixPair: [2]string{"ROB", "BOS"}, TCP: map[string]TCP{"*": "1M"}, RNAV: true},
			},
		},
	}
	rfp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, EntryFix: "PVA", ExitFix: "BOS"}
	if !s.applyFixPairAssignment(rfp) {
		t.Errorf("reassigned arrival: applyFixPairAssignment returned false")
	}
	if rfp.EntryFix != "PVA" || rfp.DerivedEntryFix != "ROB" {
		t.Errorf("entry = %q derived = %q, want actual PVA with derived ROB", rfp.EntryFix, rfp.DerivedEntryFix)
	}
	if rfp.DerivedExitFix != "" {
		t.Errorf("derived exit = %q, want unset (no substitution)", rfp.DerivedExitFix)
	}
	if rfp.InboundHandoffController != "1M" {
		t.Errorf("reassigned arrival owner = %q, want 1M", rfp.InboundHandoffController)
	}
	if !rfp.RNAV {
		t.Errorf("RNAV fix-pair row should mark the flight RNAV")
	}

	// No fix_pair_configuration adapted: the pipeline is a no-op (keeps whatever
	// the inbound-flow default set), so shipped facilities are unaffected.
	s.State.FacilityAdaptation.FixPairConfiguration = nil
	fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, EntryFix: "PVA", ExitFix: "BOS",
		InboundHandoffController: "9Z"}
	if s.applyFixPairAssignment(fp) {
		t.Errorf("applyFixPairAssignment should be a no-op with no config")
	}
	if fp.InboundHandoffController != "9Z" {
		t.Errorf("no-op case clobbered InboundHandoffController: %q", fp.InboundHandoffController)
	}
}
