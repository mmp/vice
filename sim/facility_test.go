// sim/facility_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"io"
	"reflect"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/util"

	"log/slog"
)

func TestNASFlightPlanUpdateClearsDerivedFix(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := NewTestSim(lg)

	fp := &NASFlightPlan{EntryFix: "PVA", ExitFix: "BOS", DerivedEntryFix: "ROB", DerivedExitFix: "LGA"}

	// Re-affirming the same fixes leaves the derived substitutions in place.
	var spec FlightPlanSpecifier
	spec.EntryFix.Set("PVA")
	spec.ExitFix.Set("BOS")
	if err := fp.Update(spec, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedEntryFix != "ROB" || fp.DerivedExitFix != "LGA" {
		t.Errorf("unchanged fixes should not clear derived fixes: got entry=%q exit=%q", fp.DerivedEntryFix, fp.DerivedExitFix)
	}

	// Changing the entry fix clears only the derived entry fix.
	var spec2 FlightPlanSpecifier
	spec2.EntryFix.Set("XYZ")
	if err := fp.Update(spec2, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedEntryFix != "" {
		t.Errorf("DerivedEntryFix = %q, want cleared after entry fix change", fp.DerivedEntryFix)
	}
	if fp.DerivedExitFix != "LGA" {
		t.Errorf("DerivedExitFix = %q, want unchanged", fp.DerivedExitFix)
	}

	// Changing the exit fix clears the derived exit fix.
	var spec3 FlightPlanSpecifier
	spec3.ExitFix.Set("PVD")
	if err := fp.Update(spec3, s); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if fp.DerivedExitFix != "" {
		t.Errorf("DerivedExitFix = %q, want cleared after exit fix change", fp.DerivedExitFix)
	}
}

func TestPositionConsolidationValidate(t *testing.T) {
	cp := map[TCP]*av.Controller{"1R": {}, "1M": {}, "1L": {}, "1D": {}}
	countErrors := func(pc PositionConsolidation) int {
		var e util.ErrorLogger
		pc.Validate(cp, &e)
		n := 0
		for range e.Errors() {
			n++
		}
		return n
	}
	// Valid single-rooted tree.
	if n := countErrors(PositionConsolidation{"1R": {"1M", "1L"}, "1D": {"1R"}}); n != 0 {
		t.Errorf("valid consolidation reported %d errors", n)
	}
	// Unknown child position.
	if n := countErrors(PositionConsolidation{"1R": {"9Z"}}); n == 0 {
		t.Errorf("expected error for unknown child position")
	}
	// Cycle: 1R -> 1M -> 1R.
	if n := countErrors(PositionConsolidation{"1R": {"1M"}, "1M": {"1R"}}); n == 0 {
		t.Errorf("expected error for cycle")
	}
	// A position that is a child of two parents.
	if n := countErrors(PositionConsolidation{"1R": {"1M"}, "1D": {"1M", "1R"}}); n == 0 {
		t.Errorf("expected error for multi-parent child")
	}
}

// validateCoordinationLists runs the STARS adaptation validation over a set of
// coordination lists and returns the accumulated error text.
func validateCoordinationLists(lists []CoordinationList) string {
	fc := &FacilityConfig{ControlPositions: map[TCP]*av.Controller{"1M": {}, "1L": {}}}
	fc.FacilityAdaptation.Lists.Coordination = lists
	var e util.ErrorLogger
	fc.validateSTARSAdaptation(&e)
	return e.String()
}
func TestCoordinationListOwnerTCP(t *testing.T) {
	// Two owner-scoped lists for one airport: valid split.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "M", Id: "BM", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "1M"},
		{Name: "L", Id: "BL", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "1L"},
	}); strings.Contains(out, "would appear in both") || strings.Contains(out, "multiple \"lists.coordination\" entries for") {
		t.Errorf("distinct owner-scoped lists should be valid, got:\n%s", out)
	}
	// Catch-all + owner-scoped for the same airport: valid (catch-all = remainder).
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "all", Id: "BA", Airports: []av.ICAOAirportCode{"KBOS"}},
		{Name: "M", Id: "BM", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "1M"},
	}); strings.Contains(out, "multiple") {
		t.Errorf("catch-all + owner-scoped should be valid (remainder), got:\n%s", out)
	}
	// Two catch-all lists for the same airport: error.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "a1", Id: "A1", Airports: []av.ICAOAirportCode{"KBOS"}},
		{Name: "a2", Id: "A2", Airports: []av.ICAOAirportCode{"KBOS"}},
	}); !strings.Contains(out, "multiple catch-all") {
		t.Errorf("two catch-all lists should error, got:\n%s", out)
	}
	// Two lists with the same (airport, owner_tcp): overlap error.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "M1", Id: "B1", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "1M"},
		{Name: "M2", Id: "B2", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "1M"},
	}); !strings.Contains(out, `multiple "lists.coordination" entries for "owner_tcp"`) {
		t.Errorf("duplicate (airport, owner_tcp) should error, got:\n%s", out)
	}
	// owner_tcp not a known control position.
	if out := validateCoordinationLists([]CoordinationList{
		{Name: "X", Id: "BX", Airports: []av.ICAOAirportCode{"KBOS"}, OwnerTCP: "9Z"},
	}); !strings.Contains(out, `"owner_tcp" "9Z" is not in "control_positions"`) {
		t.Errorf("unknown owner_tcp should error, got:\n%s", out)
	}
}

// TestAirspaceAwarenessController verifies how a rule is matched against a
// flight plan: a rule names a fix in full while the flight plan carries the
// fix's 3-character id, so the two are compared after shortening the rule's
// name. Altitude and aircraft type gate the match, and a later rule is
// considered when an earlier one is only a partial match.
func TestAirspaceAwarenessController(t *testing.T) {
	fa := FacilityAdaptation{
		SignificantPoints: map[string]SignificantPoint{"BOSOX": {ShortName: "BOX"}, "ROBUCC": {}},
		AirspaceAwareness: []AirspaceAwareness{
			{Fix: []string{"BOSOX"}, AltitudeRange: [2]int{11000, 99000}, ReceivingController: "C18"},
			{Fix: []string{"ROBUCC"}, AircraftType: []string{"J"}, ReceivingController: "C37"},
			{Fix: []string{"ALL"}, ReceivingController: "1Z"},
		},
	}

	for _, tc := range []struct {
		name string
		fp   NASFlightPlan
		want string
	}{
		{"adapted fix matches by its short name",
			NASFlightPlan{ExitFix: "BOX", RequestedAltitude: 20000, AircraftType: "B738"}, "C18"},
		{"a plan carrying the rule's full name does not match it",
			NASFlightPlan{ExitFix: "BOSOX", RequestedAltitude: 20000, AircraftType: "B738"}, "1Z"},
		{"altitude below the range falls through",
			NASFlightPlan{ExitFix: "BOX", RequestedAltitude: 8000, AircraftType: "B738"}, "1Z"},
		{"short name defaults to the first three characters",
			NASFlightPlan{ExitFix: "ROB", AircraftType: "B738"}, "C37"},
		{"aircraft type gates the match",
			NASFlightPlan{ExitFix: "ROB", AircraftType: "C172"}, "1Z"},
		{"an unadapted fix takes the wildcard",
			NASFlightPlan{ExitFix: "PVD", RequestedAltitude: 20000, AircraftType: "B738"}, "1Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tcp, ok := fa.AirspaceAwarenessController("", &tc.fp); !ok || tcp != tc.want {
				t.Errorf("got %q ok=%v, want %q/true", tcp, ok, tc.want)
			}
		})
	}

	// With no wildcard rule adapted, a flight nothing names goes unmatched and
	// the C handoff is rejected rather than sent somewhere arbitrary.
	noWildcard := FacilityAdaptation{
		SignificantPoints: fa.SignificantPoints,
		AirspaceAwareness: fa.AirspaceAwareness[:1],
	}
	fp := NASFlightPlan{ExitFix: "PVD", RequestedAltitude: 20000, AircraftType: "B738"}
	if tcp, ok := noWildcard.AirspaceAwarenessController("", &fp); ok {
		t.Errorf("unmatched fix returned %q, want no match", tcp)
	}
}

// TestDeriveERAMFixPairFullyContained verifies that a departure whose
// destination is a local facility airport (an internal / fully-contained
// flight) gets the destination airport as its exit fix instead of a
// route/zone-based boundary partition.
func TestDeriveERAMFixPairFullyContained(t *testing.T) {
	s := NewTestSim(testLogger())
	s.State.ERAMCoordination = &enroute.Coordination{Coord: &enroute.ArtsCoordEntry{}}
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{"KVPC": {}, "KCPP": {}}
	// Internal departure KVPC -> KCPP (both local): exit fix = destination (K
	// stripped), route/zone skipped.
	ac := &Aircraft{TypeOfFlight: av.FlightTypeDeparture,
		FlightPlan: av.FlightPlan{ArrivalAirport: "KCPP"}}
	fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, ExitFix: "SOONE"}
	res := s.deriveERAMFixPair(fp, ac)
	if !res.OK || res.Fix != "CPP" {
		t.Errorf("fully-contained: got fix=%q ok=%v, want CPP/true", res.Fix, res.OK)
	}
	if fp.ExitFix != "CPP" {
		t.Errorf("ExitFix = %q, want CPP", fp.ExitFix)
	}
	// LocalArrival is flagged for the caller to reclassify, but deriveERAMFixPair
	// itself leaves TypeOfFlight as Departure so the departure fix-pair owner is
	// used before the reclassification happens.
	if !fp.LocalArrival {
		t.Error("LocalArrival not set for internal flight")
	}
	if fp.TypeOfFlight != av.FlightTypeDeparture {
		t.Errorf("deriveERAMFixPair should leave TypeOfFlight=Departure, got %v", fp.TypeOfFlight)
	}
}

// TestDeriveERAMFixPairNormalizesFix verifies that a coordination fix an
// arts_coordination rule names in full is stored on the flight plan as the
// 3-character id flight plans carry, so that the fix pairs, adapted fix
// criteria, and airspace awareness rules matching against it can.
func TestDeriveERAMFixPairNormalizesFix(t *testing.T) {
	coordSim := func(points map[string]SignificantPoint, fix string) *Sim {
		s := NewTestSim(testLogger())
		s.State.ERAMCoordination = &enroute.Coordination{
			Coord: &enroute.ArtsCoordEntry{
				RouteBased: []enroute.RouteRule{{Type: "string", ID: fix, DefaultFix: fix}},
			},
		}
		s.State.FacilityAdaptation = FacilityAdaptation{SignificantPoints: points}
		return s
	}

	for _, tc := range []struct {
		name   string
		points map[string]SignificantPoint
		fix    string
		want   string
	}{
		{"adapted short name", map[string]SignificantPoint{"BOSOX": {ShortName: "BOX"}}, "BOSOX", "BOX"},
		{"short name defaults to the first three characters",
			map[string]SignificantPoint{"ROBUCC": {}}, "ROBUCC", "ROB"},
		{"unadapted fix is left alone", nil, "ROBUCC", "ROBUCC"},
		{"already a 3-character id", map[string]SignificantPoint{"PVD": {}}, "PVD", "PVD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := coordSim(tc.points, tc.fix)

			ac := &Aircraft{TypeOfFlight: av.FlightTypeDeparture,
				FlightPlan: av.FlightPlan{DepartureAirport: "KBOS", ArrivalAirport: "KORD"}}
			fp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, Route: tc.fix}
			if res := s.deriveERAMFixPair(fp, ac); !res.OK || res.Fix != tc.want {
				t.Errorf("departure: got fix=%q ok=%v, want %q/true", res.Fix, res.OK, tc.want)
			}
			if fp.ExitFix != tc.want {
				t.Errorf("departure ExitFix = %q, want %q", fp.ExitFix, tc.want)
			}

			// An arrival's coordination fix lands on the entry side and is
			// normalized the same way.
			arrAc := &Aircraft{TypeOfFlight: av.FlightTypeArrival,
				FlightPlan: av.FlightPlan{DepartureAirport: "KORD", ArrivalAirport: "KBOS"}}
			arrFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, Route: tc.fix}
			s.deriveERAMFixPair(arrFp, arrAc)
			if arrFp.EntryFix != tc.want {
				t.Errorf("arrival EntryFix = %q, want %q", arrFp.EntryFix, tc.want)
			}
		})
	}
}

// TestAssignedLevelForCoord verifies that ARTS coordination's "Assigned"
// altitude compares against the flight's actual operational level rather than
// always falling back to filed cruise, per fixPairLevel, when
// NASFlightPlan.AssignedAltitude hasn't been set yet (as for a TRACON-facility
// spawn).
func TestAssignedLevelForCoord(t *testing.T) {
	// Departures are unaffected: same as fixPairLevel (requested altitude).
	depFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeDeparture, RequestedAltitude: 24000, AssignedAltitude: 10000}
	if got := assignedLevelForCoord(depFp, &Aircraft{}); got != 240 {
		t.Errorf("departure = %d, want 240 (fixPairLevel unchanged)", got)
	}

	// Arrival with AssignedAltitude already known (an ERAM-facility spawn) uses it directly.
	arrFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, AssignedAltitude: 11000, RequestedAltitude: 35000}
	if got := assignedLevelForCoord(arrFp, &Aircraft{}); got != 110 {
		t.Errorf("arrival with AssignedAltitude = %d, want 110", got)
	}

	// Arrival with no AssignedAltitude (a TRACON-facility spawn) but a
	// waypoint altitude restriction derives the nav/route level instead of
	// falling back to filed cruise.
	wp := av.Waypoint{Fix: "FIXXX"}
	wp.SetAltitudeRestriction(av.MakeAtOrBelowAltitudeRestriction(8000))
	noAssignedFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeArrival, RequestedAltitude: 35000}
	ac := &Aircraft{Nav: nav.Nav{Waypoints: av.WaypointArray{wp}, FlightState: nav.FlightState{Altitude: 12000}}}
	if got := assignedLevelForCoord(noAssignedFp, ac); got != 80 {
		t.Errorf("arrival with restriction, no AssignedAltitude = %d, want 80 (nav/route derived)", got)
	}

	// No AssignedAltitude and no restrictions falls back to fixPairLevel (cruise).
	noDataFp := &NASFlightPlan{TypeOfFlight: av.FlightTypeOverflight, RequestedAltitude: 24000}
	if got := assignedLevelForCoord(noDataFp, &Aircraft{}); got != 240 {
		t.Errorf("overflight no data = %d, want 240 (fixPairLevel fallback)", got)
	}
}

// TestRestoreERAMCoordinationGeometry verifies that Activate's restore step
// re-derives ZoneArea.Center from the text it was written as, for a value
// that reaches the sim carrying only that text: ParseGeometry runs at
// scenario-group load time, not when a saved sim is restored.
func TestRestoreERAMCoordinationGeometry(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Nil coordination, and a coordination with no resolved entry, are no-ops.
	restoreERAMCoordinationGeometry(nil, lg)
	restoreERAMCoordinationGeometry(&enroute.Coordination{}, lg)

	ec := &enroute.Coordination{
		ComputerID: "BOA",
		Coord: &enroute.ArtsCoordEntry{
			ZoneBased: []enroute.ZoneArea{{AreaID: "Z1",
				Center: av.ScenarioPoint2LL{String: "N043.33.30.000,W069.30.00.000"}}},
		},
	}
	restoreERAMCoordinationGeometry(ec, lg)
	if ec.Coord.ZoneBased[0].Center.IsZero() {
		t.Error("zone area Center should be parsed from the text it was written as, not left at zero")
	}
}

// fixLocator knows one fix and nothing else.
type fixLocator struct {
	enroute.DBLocator
	fix string
	pos math.Point2LL
}

func (fl fixLocator) Locate(s string) (math.Point2LL, bool) {
	if s == fl.fix {
		return fl.pos, true
	}
	return math.Point2LL{}, false
}

// A filter region is a piece of airspace whose center is usually given by the
// name of a fix, which is only resolved when the adaptation is finalized. A
// list that finalizing overlooks leaves its regions centered at (0,0), where
// they quietly cover nothing. The lists are found by reflection so that one
// added later is covered here without being remembered.
func TestFacilityAdaptationFinalizesEveryFilterList(t *testing.T) {
	var fa FacilityAdaptation
	filters := reflect.ValueOf(&fa.Filters).Elem()

	var lists []string
	for i := range filters.NumField() {
		field := filters.Field(i)
		if field.Type() != reflect.TypeFor[FilterRegions]() {
			continue
		}
		name := filters.Type().Field(i).Name
		lists = append(lists, name)
		field.Set(reflect.ValueOf(FilterRegions{{
			AirspaceVolume: av.AirspaceVolume{
				Id:     name,
				Type:   av.AirspaceVolumeCircle,
				Radius: 5,
				Center: av.ScenarioPoint2LL{String: "FIXAA"},
			},
		}}))
	}
	if len(lists) == 0 {
		t.Fatal("no lists of filter regions found in the adaptation")
	}

	loc := fixLocator{fix: "FIXAA", pos: math.Point2LL{-73.78, 40.64}}
	var e util.ErrorLogger
	fa.Finalize(loc, &e)

	for _, name := range lists {
		center := filters.FieldByName(name).Index(0).FieldByName("Center").FieldByName("Point2LL").Interface()
		if center != loc.pos {
			t.Errorf("filters %q: center is %v, want %v", name, center, loc.pos)
		}
	}
}
