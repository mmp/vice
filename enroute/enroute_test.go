// enroute/enroute_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package enroute

import (
	"os"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

func TestMain(m *testing.M) {
	av.InitDB()
	os.Exit(m.Run())
}

func TestMatchTokens(t *testing.T) {
	for _, tc := range []struct {
		patterns []string
		value    string
		want     bool
	}{
		{nil, "jet", true},                                 // empty = any
		{[]string{"jet"}, "jet", true},                     // include match
		{[]string{"jet"}, "prop", false},                   // include miss
		{[]string{"!conventional"}, "rnav", true},          // exclude, not matched
		{[]string{"!conventional"}, "conventional", false}, // exclude, matched
		{[]string{"jet", "turboprop"}, "turboprop", true},  // any include
		{[]string{"jet", "!B738"}, "jet", true},            // include hit, exclude miss
	} {
		if got := matchTokens(tc.patterns, tc.value); got != tc.want {
			t.Errorf("matchTokens(%v, %q) = %v, want %v", tc.patterns, tc.value, got, tc.want)
		}
	}
}
func TestBearingInSector(t *testing.T) {
	for _, tc := range []struct {
		b, start, end int
		want          bool
	}{
		{10, 340, 50, true},   // wraps through 0
		{350, 340, 50, true},  // wraps, before 360
		{200, 340, 50, false}, // outside the wrap sector
		{200, 160, 250, true}, // normal sector
		{150, 160, 250, false},
	} {
		if got := bearingInSector(tc.b, tc.start, tc.end); got != tc.want {
			t.Errorf("bearingInSector(%d, %d, %d) = %v, want %v", tc.b, tc.start, tc.end, got, tc.want)
		}
	}
}
func TestParseBoundary(t *testing.T) {
	pts, err := parseBoundary("N043.33.30.000,W069.30.00.000 N043.06.30.000,W069.30.00.000")
	if err != nil {
		t.Fatalf("parseBoundary: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("parseBoundary returned %d points, want 2", len(pts))
	}
	if _, err := parseBoundary("not-a-latlong"); err == nil {
		t.Errorf("parseBoundary should have errored on garbage input")
	}
}

// TestAirwayDirection verifies that route-rule matching respects the airway
// direction: a directional ("down"/"up") rule only fires when the flight
// traverses the airway that way, while "both"/"" matches either direction.
func TestAirwayDirection(t *testing.T) {
	// Canonical airway ZZ99: A -> B -> C (defined order == "down").
	av.DB.Airways["ZZ99"] = []av.Airway{{Name: "ZZ99",
		Fixes: []av.AirwayFix{{Fix: "A"}, {Fix: "B"}, {Fix: "C"}}}}
	defer delete(av.DB.Airways, "ZZ99")
	wp := func(fix string) av.Waypoint {
		return av.Waypoint{Fix: fix, Extra: &av.WaypointExtra{Airway: "ZZ99"}}
	}
	down := []av.Waypoint{wp("A"), wp("B"), wp("C")}
	up := []av.Waypoint{wp("C"), wp("B"), wp("A")}
	if d := flightAirwayDirection(down, "ZZ99"); d != "down" {
		t.Errorf("A->B->C direction = %q, want down", d)
	}
	if d := flightAirwayDirection(up, "ZZ99"); d != "up" {
		t.Errorf("C->B->A direction = %q, want up", d)
	}
	ruleDown := RouteRule{Type: "airway", ID: "ZZ99", Direction: "down"}
	ruleBoth := RouteRule{Type: "airway", ID: "ZZ99", Direction: "both"}
	if !routeRuleMatches(ruleDown, down, "A B C") {
		t.Error("down rule should match a down-flying flight")
	}
	if routeRuleMatches(ruleDown, up, "C B A") {
		t.Error("down rule should NOT match an up-flying flight")
	}
	if !routeRuleMatches(ruleBoth, up, "C B A") {
		t.Error("both rule should match either direction")
	}
	if !routeRuleMatches(ruleBoth, down, "A B C") {
		t.Error("both rule should match either direction")
	}
}

func TestCoordFixAltitudeKind(t *testing.T) {
	// Level overflight at FL350; FIXA is on the route, FIXX is not.
	traj := &Trajectory{
		Waypoints:  []av.Waypoint{{Fix: "FIXA", Location: math.Point2LL{-82, 28}}},
		FlightType: av.FlightTypeOverflight,
		cumDist:    []float32{0},
		cruiseAlt:  35000,
		fieldElev:  35000,
	}
	const assigned = 90 // FL090 assigned, in hundreds
	for _, tc := range []struct {
		name string
		fix  string
		kind string
		want int
	}{
		{"default (unset) uses assigned", "FIXA", "", assigned},
		{"Assigned uses assigned", "FIXA", "Assigned", assigned},
		{"case-insensitive assigned", "FIXA", "assigned", assigned},
		{"Trajectory uses trajectory altitude at fix", "FIXA", "Trajectory", 350},
		{"Trajectory off-route falls back to cruise", "FIXX", "Trajectory", 350},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coordFixAltitude(traj, tc.fix, tc.kind, assigned); got != tc.want {
				t.Errorf("coordFixAltitude = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestProcedureMatches(t *testing.T) {
	for _, tc := range []struct {
		route, proc string
		want        bool
	}{
		{"ROBUC3 BOS", "ROBUC#", true},  // # version wildcard
		{"ROBUC3 BOS", "ROBUC3", true},  // exact
		{"ROBUC3 BOS", "JFUND#", false}, // wrong procedure
		{"GDM DCT BOS", "GDM", true},
		{"GDM DCT BOS", "", true}, // empty matches
	} {
		if got := procedureMatches(tc.route, tc.proc); got != tc.want {
			t.Errorf("procedureMatches(%q, %q) = %v, want %v", tc.route, tc.proc, got, tc.want)
		}
	}
}
func TestRestrictionApplies(t *testing.T) {
	r := Restriction{
		FlightType:          "arrival",
		Procedure:           "ROBUC#",
		AltitudeRestriction: "14000-",
		ArrivalAirports:     []string{"KBOS"},
	}
	r.Aircraft.Type = []string{"!B738"} // exclude 738s
	jet := Attrs{Engine: "jet", ACType: "A320"}
	b738 := Attrs{Engine: "jet", ACType: "B738"}
	cases := []struct {
		name    string
		ft      av.TypeOfFlight
		route   string
		airport string
		attrs   Attrs
		want    bool
	}{
		{"matching ROBUC3 arrival", av.FlightTypeArrival, "ROBUC3 BOS", "KBOS", jet, true},
		{"wrong flight type", av.FlightTypeDeparture, "ROBUC3 BOS", "KBOS", jet, false},
		{"wrong procedure", av.FlightTypeArrival, "JFUND2 BOS", "KBOS", jet, false},
		{"wrong airport", av.FlightTypeArrival, "ROBUC3 BOS", "KMHT", jet, false},
		{"excluded aircraft type", av.FlightTypeArrival, "ROBUC3 BOS", "KBOS", b738, false},
	}
	for _, c := range cases {
		if got := restrictionApplies(r, c.ft, c.route, c.airport, c.attrs); got != c.want {
			t.Errorf("%s: restrictionApplies = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRestrictionCapsCoordination shows the downstream effect: a "ROBUC# arrivals
// at/below 14000" restriction caps the trajectory so a high jet's altitude at PVA
// no longer meets the PVA [FL180+] criterion, and coordination picks ROB instead.
func TestRestrictionCapsCoordination(t *testing.T) {
	coord := bosCoordination()
	// High jet on ROBUC3: without the restriction, PVA is the coordination fix.
	traj := makeArrivalTrajectory([]struct {
		fix string
		alt float32
	}{{"PVA", 24000}, {"ROB", 12000}, {"BOS", 100}}, 100)
	attrs := Attrs{Engine: "jet", ACType: "A320", DestAirport: "KBOS"}
	if res := DeriveCoordinationFix(coord, traj, attrs, av.FlightTypeArrival, "ROBUC3 BOS"); res.Fix != "PVA" {
		t.Fatalf("baseline: coordination fix = %q, want PVA", res.Fix)
	}
	// Apply the at/below-14000 restriction; PVA now models at 14000 (=FL140).
	r := Restriction{FlightType: "arrival", Procedure: "ROBUC#", AltitudeRestriction: "14000-", ArrivalAirports: []string{"KBOS"}}
	traj.applyRestriction(r, av.FlightTypeArrival, "ROBUC3 BOS", "KBOS", attrs)
	if alt, _ := traj.AltitudeAtFix("PVA"); alt != 14000 {
		t.Errorf("after restriction, altitude at PVA = %d ft, want 14000", alt)
	}
	res := DeriveCoordinationFix(coord, traj, attrs, av.FlightTypeArrival, "ROBUC3 BOS")
	t.Logf("restricted ROBUC3 high jet -> rule=%s fix=%s", res.Rule, res.Fix)
	if res.Fix != "ROB" {
		t.Errorf("after restriction: coordination fix = %q, want ROB", res.Fix)
	}
}
func TestRouteContainsSequence(t *testing.T) {
	for _, tc := range []struct {
		route, id string
		want      bool
	}{
		{"KSRQ TUBBA CURSD KSRQ", "TUBBA CURSD", true}, // adapted route as contiguous run
		{"TUBBA DCT CURSD", "TUBBA CURSD", false},      // not contiguous
		{"ABC DEF GHI", "DEF", true},                   // single token
		{"ABC DEF GHI", "XYZ", false},                  // absent
		{"TUBBA CURSD", "TUBBA CURSD", true},           // exact
		{"CURSD", "TUBBA CURSD", false},                // route shorter than id
	} {
		if got := routeContainsSequence(tc.route, tc.id); got != tc.want {
			t.Errorf("routeContainsSequence(%q, %q) = %v, want %v", tc.route, tc.id, got, tc.want)
		}
	}
}

// bosCoordination builds a representative A90 (BOA) coordination entry.
func bosCoordination() *ArtsCoordEntry {
	return &ArtsCoordEntry{
		RouteBased: []RouteRule{
			{
				Type: "star", ID: "ROBUC3", AltitudeKind: "Trajectory",
				Fixes: []CoordFix{
					{Fix: "PVA", Altitude: [2]int{180, 999}, Engine: []string{"jet"}},
					{Fix: "ROB", Altitude: [2]int{0, 179}},
				},
				DefaultFix: "WOO",
			},
		},
		ZoneBased: []ZoneArea{
			{
				AreaID: "PRIMARY",
				// Boston Logan, roughly.
				CenterStr: "N042.21.35.960,W071.00.19.061",
				Center:    math.Point2LL{-71.0053, 42.3600},
				Arrival: []ZoneEntry{
					{Bearings: "340,050", DefaultFix: "NNN"},
					{Bearings: "160,250", DefaultFix: "SSS"},
				},
			},
		},
	}
}

// makeArrivalTrajectory builds a descending arrival trajectory through the given
// (fix, altitude-ft) route points, ending at the arrival field.
func makeArrivalTrajectory(points []struct {
	fix string
	alt float32
}, fieldElev float32) *Trajectory {
	t := &Trajectory{FlightType: av.FlightTypeArrival, fieldElev: fieldElev, nmPerLong: 47}
	// Space the waypoints 10 nm apart along a line so cumDist is monotonic.
	for i, p := range points {
		wp := av.Waypoint{Fix: p.fix, Location: math.Point2LL{-71 - float32(i)*0.2, 42}}
		wp.SetOnSTAR(true) // these are STAR arrivals
		t.Waypoints = append(t.Waypoints, wp)
		t.cumDist = append(t.cumDist, float32(i)*10)
	}
	t.totalDist = float32(len(points)-1) * 10
	t.cruiseAlt = points[0].alt
	// Choose a descent gradient that reproduces the requested endpoint altitudes
	// (linear from cruise at the start down to fieldElev at the end).
	if t.totalDist > 0 {
		t.gradient = (t.cruiseAlt - fieldElev) / t.totalDist
	}
	return t
}

// TestDeriveCoordinationFix drives route-based selection with fix criteria
// (altitude at the fix, engine class) and the zone-based bearing fallback over
// a few representative flights. The STARS side — reassignment of the derived
// pair and the owning-position lookup — is covered by the sim package's
// fix-pair tests.
func TestDeriveCoordinationFix(t *testing.T) {
	coord := bosCoordination()
	hi := makeArrivalTrajectory([]struct {
		fix string
		alt float32
	}{{"PVA", 24000}, {"ROB", 12000}, {"BOS", 100}}, 100)
	lo := makeArrivalTrajectory([]struct {
		fix string
		alt float32
	}{{"PVA", 9000}, {"ROB", 4000}, {"BOS", 100}}, 100)

	// High jet on ROBUC3: matches the PVA criterion (>=FL180, jet).
	res := DeriveCoordinationFix(coord, hi, Attrs{Engine: "jet", Nav: "rnav", ACType: "TEST"},
		av.FlightTypeArrival, "ROBUC3 BOS")
	if !res.OK || res.Fix != "PVA" {
		t.Errorf("high jet: got (%q, ok=%v), want PVA", res.Fix, res.OK)
	}
	// Low prop on ROBUC3: PVA criterion excludes non-jet and altitude -> ROB.
	res = DeriveCoordinationFix(coord, lo, Attrs{Engine: "prop", Nav: "rnav", ACType: "TEST"},
		av.FlightTypeArrival, "ROBUC3 BOS")
	if !res.OK || res.Fix != "ROB" {
		t.Errorf("low prop: got (%q, ok=%v), want ROB", res.Fix, res.OK)
	}

	// Zone-based fallback: a flight not on any route_based rule falls to the
	// bearing sector from center. A flight entering from the north (bearing ~0)
	// hits the "340,050" sector -> NNN.
	zoneTraj := &Trajectory{
		FlightType: av.FlightTypeArrival,
		Waypoints:  []av.Waypoint{{Fix: "GDM", Location: math.Point2LL{-71.0053, 43.0}}}, // due north of center
		cumDist:    []float32{0},
		cruiseAlt:  11000,
		fieldElev:  100,
		nmPerLong:  47,
	}
	res = DeriveCoordinationFix(coord, zoneTraj, Attrs{Engine: "jet"}, av.FlightTypeArrival, "GDM BOS")
	t.Logf("north arrival (zone) rule=%s fix=%s", res.Rule, res.Fix)
	if !res.OK || res.Fix != "NNN" {
		t.Errorf("zone arrival from north: got (%q, ok=%v), want NNN", res.Fix, res.OK)
	}
}
