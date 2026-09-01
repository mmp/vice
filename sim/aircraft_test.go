// sim/aircraft_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
)

// 1 degree of latitude is ~60 NM, so waypoints are placed along the meridian
// at known distances from (0, 0).
func wp(fix string, latDeg float32) av.Waypoint {
	return av.Waypoint{Fix: fix, Location: math.Point2LL{0, latDeg}}
}

// The aircraft sits at (0, 0), thousands of miles from both KJFK and KBOS,
// so neither airport appears in these fixes; see
// TestGetSTTFixes_AirportsOnlyWhenNear.
func makeAircraftForSTTFixes(wps []av.Waypoint) *Aircraft {
	return &Aircraft{
		FlightPlan: av.FlightPlan{
			DepartureAirport: "KJFK",
			ArrivalAirport:   "KBOS",
		},
		Nav: nav.Nav{
			FlightState: nav.FlightState{Position: math.Point2LL{0, 0}},
			Waypoints:   wps,
		},
	}
}

// sidWp returns a waypoint flagged as being on a SID, as
// Airport.PostDeserialize does for departure exit route waypoints.
func sidWp(fix string, latDeg float32) av.Waypoint {
	w := wp(fix, latDeg)
	w.SetOnSID(true)
	return w
}

func TestGetSTTFixes_STARS_Departure(t *testing.T) {
	wps := []av.Waypoint{
		sidWp("SIDAA", 0.5), // ~30 NM
		sidWp("SIDBB", 1.6), // ~96 NM
		wp("EXITF", 2.0),    // ~120 NM — the exit fix, not part of the SID
		wp("NEARE", 2.4),    // ~144 NM — enroute, near enough to go direct
		wp("FAREN", 3.0),    // ~180 NM — enroute, too far away
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.FlightPlan.Exit = "EXITF"

	got := ac.GetSTTFixes(false)
	want := []string{"SIDAA", "SIDBB", "EXITF", "NEARE"}
	if !equalStrings(got, want) {
		t.Errorf("STARS departure: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_DepartureFarSIDAndExit(t *testing.T) {
	// The SID's waypoints and the exit fix are included however far away
	// they are; only the enroute fixes past the exit are distance-limited.
	wps := []av.Waypoint{
		sidWp("SIDAA", 3.0), // ~180 NM
		wp("EXITF", 4.0),    // ~240 NM
		wp("ENRTE", 5.0),    // ~300 NM — not included
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.FlightPlan.Exit = "EXITF"

	got := ac.GetSTTFixes(false)
	want := []string{"SIDAA", "EXITF"}
	if !equalStrings(got, want) {
		t.Errorf("STARS departure, distant SID: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_ArrivalFullRoute(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 0.5),  // ~30 NM
		wp("MIDDL", 2.0), // ~120 NM
		wp("FAR", 5.0),   // ~300 NM — still included
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeArrival

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "MIDDL", "FAR"}
	if !equalStrings(got, want) {
		t.Errorf("STARS arrival: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_ArrivalExpectedApproach(t *testing.T) {
	appr := &av.Approach{
		Waypoints: []av.WaypointArray{{wp("TRANS", 1.5), wp("FINAL", 0.9)}},
	}

	// Told to expect the approach but not yet on it: all of the approach's
	// fixes are included, without duplicating ones shared with the route.
	ac := makeAircraftForSTTFixes([]av.Waypoint{wp("NEAR", 0.5), wp("TRANS", 1.5)})
	ac.TypeOfFlight = av.FlightTypeArrival
	ac.Nav.Approach.Assigned = appr

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "TRANS", "FINAL"}
	if !equalStrings(got, want) {
		t.Errorf("expecting approach: got %v, want %v", got, want)
	}

	// Once the aircraft has joined the approach, the remaining route
	// carries the remaining approach fixes, so nothing more is added.
	onAppr := wp("FINAL", 0.9)
	onAppr.SetOnApproach(true)
	ac = makeAircraftForSTTFixes([]av.Waypoint{onAppr})
	ac.TypeOfFlight = av.FlightTypeArrival
	ac.Nav.Approach.Assigned = appr

	got = ac.GetSTTFixes(false)
	want = []string{"FINAL"}
	if !equalStrings(got, want) {
		t.Errorf("joined approach: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_Overflight120NM(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 1.0),  // ~60 NM
		wp("MIDDL", 1.9), // ~114 NM
		wp("FAR", 2.5),   // ~150 NM — culled
		wp("FARTH", 3.0),
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeOverflight

	got := ac.GetSTTFixes(false)
	want := []string{"NEAR", "MIDDL"}
	if !equalStrings(got, want) {
		t.Errorf("STARS overflight: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_STARS_OverflightFirstFixAlwaysIncluded(t *testing.T) {
	wps := []av.Waypoint{
		wp("FIRST", 2.5), // ~150 NM — included anyway since it's the first
		wp("SECND", 3.0), // ~180 NM — culled
	}
	ac := makeAircraftForSTTFixes(wps)
	ac.TypeOfFlight = av.FlightTypeOverflight

	got := ac.GetSTTFixes(false)
	want := []string{"FIRST"}
	if !equalStrings(got, want) {
		t.Errorf("STARS overflight: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_ERAM_Allows300NMAndCapsAt5(t *testing.T) {
	wps := []av.Waypoint{
		wp("ALPHA", 0.5), // 30 NM
		wp("BRAVO", 1.0), // 60 NM
		wp("CHARL", 1.5), // 90 NM
		wp("DELTA", 2.5), // 150 NM
		wp("ECHO", 3.5),  // 210 NM
		wp("FOXTR", 4.0), // 240 NM — in range but exceeds count
		wp("GOLF", 6.0),  // 360 NM — beyond range
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(true)
	want := []string{"ALPHA", "BRAVO", "CHARL", "DELTA", "ECHO"}
	if !equalStrings(got, want) {
		t.Errorf("ERAM: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_ERAM_CullsBeyond300NM(t *testing.T) {
	wps := []av.Waypoint{
		wp("NEAR", 2.0), // 120 NM
		wp("MID", 4.0),  // 240 NM
		wp("FAR", 6.0),  // 360 NM — beyond 300
		wp("FARTHER", 7.0),
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(true)
	want := []string{"NEAR", "MID"}
	if !equalStrings(got, want) {
		t.Errorf("ERAM: got %v, want %v", got, want)
	}
}

func TestGetSTTFixes_SkipsInternalAndShortFixes(t *testing.T) {
	wps := []av.Waypoint{
		wp("_INT", 0.2),   // internal, underscore-prefixed
		wp("AB", 0.3),     // too short
		wp("OK", 0.4),     // too short (len 2)
		wp("GOOD", 0.5),   // valid
		wp("LONGER", 1.0), // length 6, too long
	}
	ac := makeAircraftForSTTFixes(wps)

	got := ac.GetSTTFixes(false)
	want := []string{"GOOD"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// An airport is worth naming only when the aircraft is near it: a Teterboro
// departure is never sent direct to Orlando, and carrying it costs a slot in
// the fix vocabulary and in the whisper prompt. The same rule applies to both
// airports whatever the type of flight.
func TestGetSTTFixes_AirportsOnlyWhenNear(t *testing.T) {
	jfk, ok := av.DB.LookupAirport("KJFK")
	if !ok {
		t.Fatal("KJFK not in the database")
	}

	for _, tc := range []struct {
		name               string
		departure, arrival string
		want               []string
	}{
		{"both near", "KJFK", "KLGA", []string{"KLGA", "KJFK", "GOOD"}},
		{"distant destination", "KJFK", "KMCO", []string{"KJFK", "GOOD"}},
		{"distant origin", "KMCO", "KLGA", []string{"KLGA", "GOOD"}},
		{"both distant", "KMCO", "KSFO", []string{"GOOD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ac := makeAircraftForSTTFixes([]av.Waypoint{{Fix: "GOOD", Location: jfk.Location}})
			ac.Nav.FlightState.Position = jfk.Location
			ac.FlightPlan.DepartureAirport = tc.departure
			ac.FlightPlan.ArrivalAirport = tc.arrival
			ac.TypeOfFlight = av.FlightTypeArrival

			if got := ac.GetSTTFixes(false); !equalStrings(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAltitudeRangeAbove(t *testing.T) {
	for _, tc := range []struct {
		a     altitudeRange
		floor int
		want  altitudeRange
	}{
		{altitudeRange{5000, 41000}, 24000, altitudeRange{24000, 41000}},
		{altitudeRange{30000, 41000}, 24000, altitudeRange{30000, 41000}},
		// An aircraft that can't reach the restriction goes as high as it can.
		{altitudeRange{5000, 12500}, 24000, altitudeRange{12500, 12500}},
	} {
		if got := tc.a.above(tc.floor); got != tc.want {
			t.Errorf("%v.above(%d) = %v, want %v", tc.a, tc.floor, got, tc.want)
		}
	}
}

func TestAltitudeRangeNarrowedTo(t *testing.T) {
	for _, tc := range []struct{ a, b, want altitudeRange }{
		{altitudeRange{5000, 41000}, altitudeRange{24000, 37000}, altitudeRange{24000, 37000}},
		{altitudeRange{24000, 41000}, altitudeRange{6000, 37000}, altitudeRange{24000, 37000}},
		// A band the range can't be reconciled with is evidence gone wrong,
		// and is left out rather than obeyed.
		{altitudeRange{2958, 41000}, altitudeRange{2400, 2400}, altitudeRange{2958, 41000}},
		{altitudeRange{4181, 12500}, altitudeRange{33000, 37000}, altitudeRange{4181, 12500}},
	} {
		if got := tc.a.narrowedTo(tc.b); got != tc.want {
			t.Errorf("%v.narrowedTo(%v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAltitudeRangeBiasedTo(t *testing.T) {
	for _, tc := range []struct{ a, b, want altitudeRange }{
		// Already inside, the band is taken as it is.
		{altitudeRange{25000, 39000}, altitudeRange{31000, 37000}, altitudeRange{31000, 37000}},
		// Below the range, it slides up rather than collapsing onto 24,000.
		{altitudeRange{24000, 37000}, altitudeRange{16000, 24000}, altitudeRange{24000, 32000}},
		{altitudeRange{26000, 38000}, altitudeRange{25000, 31000}, altitudeRange{26000, 32000}},
		// Above the range, it slides down.
		{altitudeRange{8000, 14000}, altitudeRange{34000, 38000}, altitudeRange{10000, 14000}},
		// Wider than the range, what is left is the range.
		{altitudeRange{30000, 34000}, altitudeRange{16000, 24000}, altitudeRange{30000, 34000}},
		// A range already narrowed onto one altitude stays there.
		{altitudeRange{24000, 24000}, altitudeRange{16000, 24000}, altitudeRange{24000, 24000}},
	} {
		if got := tc.a.biasedTo(tc.b); got != tc.want {
			t.Errorf("%v.biasedTo(%v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAltitudeRangeSample(t *testing.T) {
	r := rand.Make()
	for _, tc := range []struct {
		a       altitudeRange
		course  math.MagneticHeading
		ceiling int
		want    []int
	}{
		{altitudeRange{24000, 32000}, 45, 41000, []int{25000, 27000, 29000, 31000}},
		{altitudeRange{24000, 32000}, 270, 41000, []int{24000, 26000, 28000, 30000, 32000}},
		// The rule turns over at 180 degrees, not past it.
		{altitudeRange{24000, 25000}, 179, 41000, []int{25000}},
		{altitudeRange{24000, 25000}, 180, 41000, []int{24000}},
		// The range holds no altitude of the required parity, so the nearest
		// one above it is filed.
		{altitudeRange{18000, 18000}, 45, 41000, []int{19000}},
		// ...unless that is over the aircraft's ceiling, when it goes below.
		{altitudeRange{18000, 18000}, 45, 18000, []int{17000}},
	} {
		seen := make(map[int]bool)
		for range 200 {
			alt := tc.a.sample(r, tc.course, tc.ceiling)
			if !slices.Contains(tc.want, alt) {
				t.Fatalf("%v course %v: sampled %d, want one of %v", tc.a, tc.course, alt, tc.want)
			}
			seen[alt] = true
		}
		if len(seen) != len(tc.want) {
			t.Errorf("%v course %v: sampled %d of the %d altitudes in %v", tc.a, tc.course,
				len(seen), len(tc.want), tc.want)
		}
	}
}

func TestPlausibleCruiseBand(t *testing.T) {
	av.InitDB()
	for _, tc := range []struct {
		from, to, acType string
		want             altitudeRange
	}{
		// KSNA-KLAS is 197nm, KLAX-KPHX 321nm, KLAX-KSFO 293nm.
		{"KSNA", "KLAS", "B738", altitudeRange{16000, 24000}},
		{"KSNA", "KLAS", "DH8D", altitudeRange{12000, 14000}},
		{"KSNA", "KLAS", "C172", altitudeRange{6000, 9000}},
		{"KLAX", "KPHX", "B738", altitudeRange{31000, 37000}},
		{"KLAX", "KSFO", "B738", altitudeRange{25000, 31000}},
		// Without both airports there is no distance to go on.
		{"KLAX", "ZZZZ", "B738", altitudeRange{34000, 38000}},
	} {
		fp := av.FlightPlan{Rules: av.FlightRulesIFR, AircraftType: tc.acType,
			DepartureAirport: tc.from, ArrivalAirport: tc.to}
		got := plausibleCruiseBand(fp, av.DB.AircraftPerformance[tc.acType])
		if got != tc.want {
			t.Errorf("%s-%s %s: band = %v, want %v", tc.from, tc.to, tc.acType, got, tc.want)
		}
	}
}

func TestFiledCruiseAltitude(t *testing.T) {
	av.InitDB()
	r := rand.Make()
	for _, tc := range []struct {
		from, to, acType string
		limits           CruiseLimits
		want             []int
	}{
		// The scraped band reaches down to 6,000, but the FINZZ3 requires
		// 16,000 and the RNDRZ4 24,000.
		{"KSNA", "KLAS", "E75L", CruiseLimits{Floor: 24000, Low: 6000, High: 37000},
			[]int{25000, 27000, 29000, 31000}},
		// The route's floor is above the lowest altitude ever filed on it.
		{"KLAX", "KPHX", "E75L", CruiseLimits{Floor: 25000, Low: 23000, High: 39000},
			[]int{31000, 33000, 35000, 37000}},
		// Nothing known about the route: the trip alone decides. KDEN sits at
		// 5,434 feet, which is no reason to file above a jet's ceiling.
		{"KDEN", "KJFK", "B738", CruiseLimits{}, []int{35000, 37000}},
		// A short hop out of Denver can't cruise below the terrain.
		{"KDEN", "KCOS", "C172", CruiseLimits{}, []int{10000}},
		// A scraped band the aircraft can't reach says nothing about where it
		// goes, so the trip alone decides.
		{"KSNA", "KLAS", "C172", CruiseLimits{Low: 33000, High: 37000}, []int{7000, 9000}},
	} {
		perf := av.DB.AircraftPerformance[tc.acType]
		fp := av.FlightPlan{Rules: av.FlightRulesIFR, AircraftType: tc.acType,
			DepartureAirport: tc.from, ArrivalAirport: tc.to}
		seen := make(map[int]bool)
		for range 400 {
			alt := FiledCruiseAltitude(fp, perf, tc.limits, 60, 12, r)
			if !slices.Contains(tc.want, alt) {
				t.Fatalf("%s-%s %s: filed %d, want one of %v", tc.from, tc.to, tc.acType, alt, tc.want)
			}
			seen[alt] = true
		}
		if len(seen) != len(tc.want) {
			t.Errorf("%s-%s %s: filed %d of the %d altitudes in %v", tc.from, tc.to, tc.acType,
				len(seen), len(tc.want), tc.want)
		}
	}
}
