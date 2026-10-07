// sim/spawn_departures_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

const testNmPerLongitude = 60

// installIntersectingRunwayFixture installs a synthetic airport "XTST" into
// db.DB with (in nm coordinates): runway 9/27 running east from (0,0) to
// (2,0); runway 36/18 running north from (1,-1) to (1,1), crossing 9 at
// (1,0); runway 8/26 parallel to 9, 5nm north; and runway 1/19 running
// north from (2.8,0.3) to (2.8,2), crossing 9's extended centerline 0.8nm
// past its east end.
func installIntersectingRunwayFixture(t *testing.T) {
	t.Helper()

	const airport = "XTST"
	orig, ok := db.DB.Airports[airport]
	t.Cleanup(func() {
		if ok {
			db.DB.Airports[airport] = orig
		} else {
			delete(db.DB.Airports, airport)
		}
	})

	nm := func(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, testNmPerLongitude) }
	db.DB.Airports[airport] = db.Airport{
		Id: airport,
		Runways: []av.Runway{
			{Id: "9", Threshold: nm(0, 0), Heading: 90},
			{Id: "27", Threshold: nm(2, 0), Heading: 270},
			{Id: "36", Threshold: nm(1, -1), Heading: 360},
			{Id: "18", Threshold: nm(1, 1), Heading: 180},
			{Id: "8", Threshold: nm(0, 5), Heading: 90},
			{Id: "26", Threshold: nm(2, 5), Heading: 270},
			{Id: "1", Threshold: nm(2.8, 0.3), Heading: 10},
			{Id: "19", Threshold: nm(2.8, 2), Heading: 190},
		},
	}
}

func TestRunwayIntersectionPoint(t *testing.T) {
	installIntersectingRunwayFixture(t)

	pt, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", "9", "36", testNmPerLongitude, 0)
	if !ok {
		t.Fatal("no intersection found for crossing runways 9/36")
	}
	if p := math.LL2NM(pt, testNmPerLongitude); math.Abs(p[0]-1) > 0.01 || math.Abs(p[1]) > 0.01 {
		t.Errorf("intersection point = %v, want (1, 0)", p)
	}

	// Dotted suffixes resolve to the physical runways.
	if _, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", "9.All", "36.West", testNmPerLongitude, 0); !ok {
		t.Error("no intersection found with dotted-suffix runway IDs")
	}

	// Same runway, opposite direction, and parallel runways don't intersect.
	for _, pair := range [][2]av.RunwayID{{"9", "9"}, {"9", "27"}, {"9", "8"}} {
		if _, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", pair[0], pair[1], testNmPerLongitude, 1); ok {
			t.Errorf("unexpected intersection for %s/%s", pair[0], pair[1])
		}
	}

	// Runway 1 crosses 9's extended centerline 0.8nm past its end, so it
	// only counts as intersecting with enough slop.
	if _, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", "9", "1", testNmPerLongitude, 0.5); ok {
		t.Error("unexpected intersection for 9/1 with 0.5nm slop")
	}
	if _, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", "9", "1", testNmPerLongitude, 1); !ok {
		t.Error("no intersection found for 9/1 with 1nm slop")
	}
}

func TestIntersectingRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	rwys := av.IntersectingRunways(db.Lookups{}, "XTST", "9", testNmPerLongitude, 0)
	for _, want := range []string{"36", "18"} {
		if !slices.Contains(rwys, want) {
			t.Errorf("IntersectingRunways = %v, missing %q", rwys, want)
		}
	}
	for _, notWant := range []string{"9", "27", "8", "26", "1", "19"} {
		if slices.Contains(rwys, notWant) {
			t.Errorf("IntersectingRunways = %v, shouldn't include %q", rwys, notWant)
		}
	}
}

func TestDepartureIntersectionHelpers(t *testing.T) {
	installIntersectingRunwayFixture(t)

	s := NewTestSim(testLogger())
	s.State.NmPerLongitude = testNmPerLongitude

	pt, ok := av.RunwayIntersectionPoint(db.Lookups{}, "XTST", "9", "36", testNmPerLongitude, 0)
	if !ok {
		t.Fatal("no intersection found for crossing runways 9/36")
	}

	// The intersection is 1nm down runway 9.
	for _, c := range []struct {
		dist float32
		want bool
	}{{0.5, true}, {1.5, false}, {-1, false}} {
		dep := DepartureAircraft{AirborneDistance: c.dist}
		if got := s.airborneBeforeIntersection(dep, "XTST", "9", pt); got != c.want {
			t.Errorf("airborneBeforeIntersection(AirborneDistance %v) = %v, want %v", c.dist, got, c.want)
		}
	}

	// A point behind the threshold is never crossed on the ground.
	behind := math.NM2LL([2]float32{-0.5, 0}, testNmPerLongitude)
	if s.airborneBeforeIntersection(DepartureAircraft{AirborneDistance: 0.1}, "XTST", "9", behind) {
		t.Error("airborneBeforeIntersection: point behind the threshold")
	}

	ac := &Aircraft{ADSBCallsign: "TST1"}
	s.Aircraft["TST1"] = ac
	dep := DepartureAircraft{ADSBCallsign: "TST1"}

	ac.Nav.FlightState.Position = math.NM2LL([2]float32{0.5, 0}, testNmPerLongitude)
	if s.departureHasPassedPoint(dep, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: aircraft is short of the intersection")
	}
	ac.Nav.FlightState.Position = math.NM2LL([2]float32{1.2, 0}, testNmPerLongitude)
	if !s.departureHasPassedPoint(dep, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: aircraft is past the intersection")
	}
	if !s.departureHasPassedPoint(DepartureAircraft{ADSBCallsign: "GONE"}, "XTST", "9", pt) {
		t.Error("departureHasPassedPoint: deleted aircraft should count as passed")
	}
}

func TestDepartureSpacedIntersectingRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	prevAc := &Aircraft{ADSBCallsign: "PRV1", AircraftType: "B738", FlightRules: av.FlightRulesIFR}
	depAc := &Aircraft{ADSBCallsign: "DEP1", AircraftType: "B738", FlightRules: av.FlightRulesIFR}

	rwy9, rwy36, rwy8 := &RunwayLaunchState{}, &RunwayLaunchState{}, &RunwayLaunchState{}

	s := NewTestSim(log.New(true, "error", t.TempDir()))
	s.Aircraft = map[av.ADSBCallsign]*Aircraft{"PRV1": prevAc, "DEP1": depAc}
	s.DepartureState = map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState{
		"XTST": {"9": rwy9, "36": rwy36, "8": rwy8},
	}
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.SimTime = now

	// PRV1 just launched on runway 9; it lifts off past the intersection
	// with runway 36, so it crosses it on the ground. MinSeparation is
	// larger than any possible wake turbulence wait so that it determines
	// the full interval.
	prev := DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: now, MinSeparation: 5 * time.Minute, AirborneDistance: 1.5}
	rwy9.LastDeparture = &prev
	prevAc.Nav.FlightState.Position = math.NM2LL([2]float32{0.3, 0}, testNmPerLongitude)

	dep := DepartureAircraft{ADSBCallsign: "DEP1", MinSeparation: time.Minute, AirborneDistance: 0.5}

	s.State.SimTime = now.Add(10 * time.Second)
	if s.departureSpaced(rwy36, dep, "XTST", "36") {
		t.Error("departureSpaced: leader hasn't passed the intersection yet")
	}

	// Once the leader is past the intersection, the departure may go even
	// though the full interval hasn't elapsed.
	prevAc.Nav.FlightState.Position = math.NM2LL([2]float32{1.2, 0}, testNmPerLongitude)
	if !s.departureSpaced(rwy36, dep, "XTST", "36") {
		t.Error("departureSpaced: leader passed the intersection on the ground")
	}

	// If both aircraft are airborne before the intersection, the full
	// interval applies even after the leader has passed it.
	prev.AirborneDistance = 0.5
	if s.departureSpaced(rwy36, dep, "XTST", "36") {
		t.Error("departureSpaced: both airborne before the intersection; full interval required")
	}
	s.State.SimTime = now.Add(5*time.Minute + time.Second)
	if !s.departureSpaced(rwy36, dep, "XTST", "36") {
		t.Error("departureSpaced: full interval has elapsed")
	}

	// Departures on the parallel runway aren't coupled at all.
	rwy9.LastDeparture = nil
	rwy8.LastDeparture = &DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: s.State.SimTime,
		MinSeparation: 5 * time.Minute, AirborneDistance: 1.5}
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: departure on a parallel runway shouldn't couple")
	}

	// Nor are they coupled when both fly straight out, with launch paths
	// recorded.
	rwy8.LastDeparture.LaunchPath = makeTestLaunchPath(0, 5, 0.1, 0, 120)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 120)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: straight-out departure on a parallel runway shouldn't couple")
	}
}

// makeTestLaunchPath returns a fabricated departure path of n one-second
// samples starting at (x, y) in nm coordinates, moving by (dx, dy) each
// second.
func makeTestLaunchPath(x, y, dx, dy float32, n int) []math.Point2LL {
	path := make([]math.Point2LL, n)
	for i := range path {
		path[i] = math.NM2LL([2]float32{x + float32(i)*dx, y + float32(i)*dy}, testNmPerLongitude)
	}
	return path
}

func TestHoldForCrossingDeparture(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	prevAc := &Aircraft{ADSBCallsign: "PRV1", AircraftType: "B738", FlightRules: av.FlightRulesIFR}
	depAc := &Aircraft{ADSBCallsign: "DEP1", AircraftType: "B738", FlightRules: av.FlightRulesIFR}

	rwy8, rwy9 := &RunwayLaunchState{}, &RunwayLaunchState{}

	s := NewTestSim(log.New(true, "error", t.TempDir()))
	s.Aircraft = map[av.ADSBCallsign]*Aircraft{"PRV1": prevAc, "DEP1": depAc}
	s.DepartureState = map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState{
		"XTST": {"8": rwy8, "9": rwy9},
	}
	s.State.NmPerLongitude = testNmPerLongitude

	// PRV1 departed the parallel runway 8 and turns so that its path
	// crosses DEP1's straight-out path from runway 9 at (5, 0), 50 seconds
	// into each aircraft's departure. The runways don't physically
	// intersect, so only the crossing-path check applies.
	prev := DepartureAircraft{ADSBCallsign: "PRV1", LaunchTime: now, MinSeparation: time.Minute,
		LaunchPath: makeTestLaunchPath(0, 5, 0.1, -0.1, 120)}
	rwy8.LastDeparture = &prev
	dep := DepartureAircraft{ADSBCallsign: "DEP1", MinSeparation: time.Minute,
		LaunchPath: makeTestLaunchPath(0, 0, 0.1, 0, 120)}

	// They would reach the crossing point 10 seconds apart.
	s.State.SimTime = now.Add(10 * time.Second)
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: crossing departure from the parallel runway is too close in time")
	}

	// 40 seconds apart is more than crossingSeparation.
	s.State.SimTime = now.Add(40 * time.Second)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: crossing departure from the parallel runway is well ahead")
	}

	// The window is symmetric: if the new departure will be through the
	// crossing point well before the earlier one arrives, it may go.
	s.State.SimTime = now.Add(10 * time.Second)
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.05, -0.05, 120) // crosses at 100 seconds
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.25, 0, 120)      // crosses at 20 seconds
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: departure crosses well ahead of the earlier one's arrival")
	}

	// Straight-out paths from parallel runways don't cross.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.1, 0, 120)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 120)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: straight-out parallel departures shouldn't couple")
	}

	// A deleted leader is long gone.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.1, -0.1, 120)
	delete(s.Aircraft, "PRV1")
	if s.holdForCrossingDeparture(prev, dep) {
		t.Error("holdForCrossingDeparture: deleted leader shouldn't hold the departure")
	}
	s.Aircraft["PRV1"] = prevAc

	// If the other runway's last departure is also our own runway's (via
	// "departure_runways_as_one"), the crossing check doesn't apply; the
	// own-runway launch interval already covers it.
	prev.LaunchPath = makeTestLaunchPath(0, 5, 0.05, -0.05, 120) // crosses at 100 seconds
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.15, 0, 120)      // crosses at ~33 seconds
	s.State.SimTime = now.Add(80 * time.Second)                  // past MinSeparation; within crossingSeparation at the crossing
	rwy9.LastDeparture = &prev
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: shared last departure shouldn't be held for crossing paths")
	}
	rwy9.LastDeparture = nil
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: the same geometry should hold when the last departure isn't shared")
	}
}

func TestSameRunwaySameCourseHold(t *testing.T) {
	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	// PRV1 and DEP1 both fly straight out east at 0.05 nm/s (180 knots),
	// lifting off 20 seconds after brake release, 1 nm down the runway.
	prev := stageDeparture(s, "PRV1", "ODI", now)
	prev.LaunchTime = now
	prev.MinSeparation = 30 * time.Second
	prev.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0, 121)
	prev.AirborneTime = 20 * time.Second

	dep := stageDeparture(s, "DEP1", "ALO", now)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0, 121)
	dep.AirborneTime = 20 * time.Second

	// PRV1 is 3 nm from DEP1's liftoff point (1, 0) once it reaches (4, 0),
	// 80 seconds in, so DEP1 must wait 60 seconds to lift off 20 seconds
	// after starting its roll.
	want := 60 * time.Second
	if base := s.launchInterval(prev, dep); base >= want {
		t.Fatalf("launchInterval = %v; must be under %v for the test to be meaningful", base, want)
	}
	interval := s.sameRunwayLaunchInterval(prev, dep)
	if interval < want || interval > want+time.Second {
		t.Errorf("sameRunwayLaunchInterval = %v, want ~%v", interval, want)
	}

	rwy9.LastDeparture = &prev
	s.State.SimTime = now.Add(interval - time.Second)
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: same-course departure released before 3 nm existed")
	}
	s.State.SimTime = now.Add(interval)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: same-course departure held past the required interval")
	}
}

func TestSameRunwayDivergingCoursesExempt(t *testing.T) {
	now := NewSimTime(time.Now())
	s, _, _ := departureQueueSim(now)

	prev := stageDeparture(s, "PRV1", "ODI", now)
	prev.LaunchTime = now
	prev.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0, 121) // due east
	prev.AirborneTime = 20 * time.Second

	// DEP1 climbs out about 30 degrees left of PRV1's course.
	dep := stageDeparture(s, "DEP1", "ALO", now)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0.03, 121)
	dep.AirborneTime = 20 * time.Second

	if got, want := s.sameRunwayLaunchInterval(prev, dep), s.launchInterval(prev, dep); got != want {
		t.Errorf("sameRunwayLaunchInterval = %v for diverging courses, want the base interval %v", got, want)
	}
}

func TestSameRunwayWakeAlwaysApplies(t *testing.T) {
	now := NewSimTime(time.Now())
	s, _, _ := departureQueueSim(now)

	// A heavy leader, with both aircraft fast enough that the 3 nm delay is
	// small compared to the wake turbulence interval.
	prev := stageDeparture(s, "PRV1", "ODI", now)
	s.Aircraft["PRV1"].AircraftType = "B744"
	prev.LaunchTime = now
	prev.MinSeparation = 10 * time.Second
	prev.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 121)
	prev.AirborneTime = 10 * time.Second

	dep := stageDeparture(s, "DEP1", "ALO", now)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0, 121)
	dep.AirborneTime = 10 * time.Second

	wtDist := av.CWTDirectlyBehindSeparation(s.Aircraft["PRV1"].CWT(), s.Aircraft["DEP1"].CWT())
	if wtDist == 0 {
		t.Fatal("expected wake turbulence separation behind a B744")
	}
	want := time.Duration(wtDist / 3.5 * float32(time.Minute))
	if got := s.sameRunwayLaunchInterval(prev, dep); got != want {
		t.Errorf("sameRunwayLaunchInterval = %v, want the wake interval %v", got, want)
	}

	// Diverging courses don't waive wake separation.
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.1, 0.06, 121)
	if got := s.sameRunwayLaunchInterval(prev, dep); got != want {
		t.Errorf("sameRunwayLaunchInterval = %v for diverging courses, want the wake interval %v", got, want)
	}
}

func TestSameRunwayLaunchIntervalGuards(t *testing.T) {
	now := NewSimTime(time.Now())
	s, _, _ := departureQueueSim(now)

	prev := stageDeparture(s, "PRV1", "ODI", now)
	prev.LaunchTime = now
	prev.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0, 121)
	prev.AirborneTime = 20 * time.Second

	dep := stageDeparture(s, "DEP1", "ALO", now)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.05, 0, 121)
	dep.AirborneTime = 20 * time.Second

	base := s.launchInterval(prev, dep)

	// Liftoff time unknown for the follower.
	noLiftoff := dep
	noLiftoff.AirborneTime = 0
	if got := s.sameRunwayLaunchInterval(prev, noLiftoff); got != base {
		t.Errorf("sameRunwayLaunchInterval = %v with unknown liftoff time, want %v", got, base)
	}

	// No launch path for the leader.
	noPath := prev
	noPath.LaunchPath = nil
	if got := s.sameRunwayLaunchInterval(noPath, dep); got != base {
		t.Errorf("sameRunwayLaunchInterval = %v with no leader path, want %v", got, base)
	}

	// A pair involving a VFR is separated visually.
	s.Aircraft["PRV1"].FlightRules = av.FlightRulesVFR
	if got := s.sameRunwayLaunchInterval(prev, dep); got != base {
		t.Errorf("sameRunwayLaunchInterval = %v with a VFR leader, want %v", got, base)
	}
}

func TestSameRunwaySlowLeaderExtrapolates(t *testing.T) {
	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	// A slow leader at 0.02 nm/s ends its 120 second path 2.4 nm out, only
	// 2 nm from the follower's liftoff point at (0.4, 0), so the delay
	// comes from extrapolating at its final speed: 3 nm exist 170 seconds
	// in, and the follower lifts off 20 seconds after starting to roll.
	prev := stageDeparture(s, "PRV1", "ODI", now)
	prev.LaunchTime = now
	prev.LaunchPath = makeTestLaunchPath(0, 0, 0.02, 0, 121)
	prev.AirborneTime = 20 * time.Second

	dep := stageDeparture(s, "DEP1", "ALO", now)
	dep.LaunchPath = makeTestLaunchPath(0, 0, 0.02, 0, 121)
	dep.AirborneTime = 20 * time.Second

	want := 150 * time.Second
	if base := s.launchInterval(prev, dep); base >= want {
		t.Fatalf("launchInterval = %v; must be under %v for the test to be meaningful", base, want)
	}
	interval := s.sameRunwayLaunchInterval(prev, dep)
	if interval < want-time.Second || interval > want+time.Second {
		t.Errorf("sameRunwayLaunchInterval = %v, want ~%v", interval, want)
	}

	rwy9.LastDeparture = &prev
	s.State.SimTime = now.Add(interval - time.Second)
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: released before the extrapolated 3 nm exists")
	}
	s.State.SimTime = now.Add(interval)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: held past the extrapolated interval")
	}
}

func TestSamePavementRunways(t *testing.T) {
	installIntersectingRunwayFixture(t)

	s := NewTestSim(testLogger())
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{"XTST": {}}
	s.DepartureState = map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState{
		"XTST": {
			"9":       &RunwayLaunchState{},
			"9.North": &RunwayLaunchState{},
			"36":      &RunwayLaunchState{},
			"8":       &RunwayLaunchState{},
		},
	}
	s.State.NmPerLongitude = testNmPerLongitude

	var got []av.RunwayID
	for rwy := range s.samePavementRunways("XTST", "9") {
		got = append(got, rwy)
	}
	for _, want := range []av.RunwayID{"9", "9.North"} {
		if !slices.Contains(got, want) {
			t.Errorf("samePavementRunways = %v, missing %q", got, want)
		}
	}
	// Intersecting runways no longer share the same-pavement group.
	if slices.Contains(got, av.RunwayID("36")) {
		t.Errorf("samePavementRunways = %v, shouldn't include intersecting runway 36", got)
	}
}

// departureQueueSim returns a sim with the synthetic XTST airport's runways
// 9 and 8 ready to launch departures.
func departureQueueSim(now Time) (*Sim, *RunwayLaunchState, *RunwayLaunchState) {
	s := NewTestSim(testLogger())
	s.STARSComputer = makeSTARSComputer("TEST")
	s.State.NmPerLongitude = testNmPerLongitude
	s.State.SimTime = now
	s.State.Airports = map[av.ICAOAirportCode]*av.Airport{"XTST": {}}

	rwy9, rwy8 := &RunwayLaunchState{}, &RunwayLaunchState{}
	s.DepartureState = map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState{
		"XTST": {"9": rwy9, "8": rwy8},
	}
	return s, rwy9, rwy8
}

// stageDeparture registers an IFR departure going out over exit with the sim
// and returns the queue entry for it, holding short since queued.
func stageDeparture(s *Sim, callsign av.ADSBCallsign, exit av.ExitID, queued Time) DepartureAircraft {
	s.Aircraft[callsign] = &Aircraft{
		ADSBCallsign: callsign,
		AircraftType: "B738",
		FlightRules:  av.FlightRulesIFR,
		Exit:         exit,
	}
	return DepartureAircraft{ADSBCallsign: callsign, QueuedTime: queued, MinSeparation: time.Minute}
}

func TestDepartureSpacedByExit(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	s, rwy9, rwy8 := departureQueueSim(now)

	// Runway 8 launched a FOO 30 seconds ago and a BAR since, so its last
	// departure says nothing about the gate; the airport's record of it
	// does.
	s.LastExitLaunch["XTST"] = map[av.ExitID]Time{"FOO": now.Add(-30 * time.Second)}
	rwy8.LastDeparture = &DepartureAircraft{ADSBCallsign: "BAR0", LaunchTime: now}

	dep := stageDeparture(s, "FOO1", "FOO", now)
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: another runway has just used the gate")
	}
	if other := stageDeparture(s, "BAZ1", "BAZ", now); !s.departureSpaced(rwy9, other, "XTST", "9") {
		t.Error("departureSpaced: a different gate is clear")
	}

	s.State.SimTime = now.Add(sameExitSeparation)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: the gate is clear again")
	}

	// The interval behind the runway's own last departure still applies
	// when it is longer than the gate's.
	prev := stageDeparture(s, "PRV1", "BAZ", now)
	prev.LaunchTime = s.State.SimTime
	prev.MinSeparation = 3 * time.Minute
	rwy9.LastDeparture = &prev
	if s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: the launch interval behind the last departure hasn't elapsed")
	}
	s.State.SimTime = s.State.SimTime.Add(3 * time.Minute)
	if !s.departureSpaced(rwy9, dep, "XTST", "9") {
		t.Error("departureSpaced: the launch interval has elapsed")
	}
}

func TestNextDepartureSpreadsGates(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	// A departure has just gone out over FOO, so the FOO holding short waits
	// and the BAR behind it goes first even though it has waited less.
	s.LastExitLaunch["XTST"] = map[av.ExitID]Time{"FOO": now}
	queue := []DepartureAircraft{
		stageDeparture(s, "FOO1", "FOO", now.Add(-time.Minute)),
		stageDeparture(s, "BAR1", "BAR", now.Add(-30*time.Second)),
	}

	if idx, ok := s.nextDeparture(queue, rwy9, "XTST", "9", now); !ok || idx != 1 {
		t.Errorf("nextDeparture = %d, %v; want the BAR departure at index 1", idx, ok)
	}

	// Once the gate is clear the FOO goes, having held short longer.
	s.State.SimTime = now.Add(sameExitSeparation)
	if idx, ok := s.nextDeparture(queue, rwy9, "XTST", "9", s.State.SimTime); !ok || idx != 0 {
		t.Errorf("nextDeparture = %d, %v; want the FOO departure at index 0", idx, ok)
	}
}

func TestNextDepartureHoldsForALongWait(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	s.LastExitLaunch["XTST"] = map[av.ExitID]Time{"FOO": now}
	queue := []DepartureAircraft{
		stageDeparture(s, "FOO1", "FOO", now.Add(-departureQueuePriority-time.Minute)),
		stageDeparture(s, "BAR1", "BAR", now),
	}

	// FOO1 has held short past the priority, so the runway waits for its
	// gate rather than passing it over again.
	if _, ok := s.nextDeparture(queue, rwy9, "XTST", "9", now); ok {
		t.Error("nextDeparture: a departure past the priority should go next, not be passed over")
	}

	// Under the priority, the departure that can go goes.
	queue[0].QueuedTime = now.Add(-time.Minute)
	if idx, ok := s.nextDeparture(queue, rwy9, "XTST", "9", now); !ok || idx != 1 {
		t.Errorf("nextDeparture = %d, %v; want the BAR departure at index 1", idx, ok)
	}
}

func TestLaunchNextDeparture(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	s.LastExitLaunch["XTST"] = map[av.ExitID]Time{"FOO": now}
	rwy9.ReleasedIFR = []DepartureAircraft{
		stageDeparture(s, "FOO1", "FOO", now.Add(-time.Minute)),
		stageDeparture(s, "BAR1", "BAR", now),
	}

	s.launchNextDeparture(rwy9, "XTST", "9", now)

	if len(rwy9.ReleasedIFR) != 1 || rwy9.ReleasedIFR[0].ADSBCallsign != "FOO1" {
		t.Errorf("ReleasedIFR = %v, want the held FOO departure alone", rwy9.ReleasedIFR)
	}
	if rwy9.LastDeparture == nil || rwy9.LastDeparture.ADSBCallsign != "BAR1" {
		t.Errorf("LastDeparture = %v, want BAR1", rwy9.LastDeparture)
	}
	if rwy9.LastDeparture != nil && rwy9.LastDeparture.LaunchTime != now {
		t.Error("launchNextDeparture: didn't record the launch time")
	}
	if s.LastExitLaunch["XTST"]["BAR"] != now {
		t.Error("launchNextDeparture: didn't record the gate it went out over")
	}
	if s.Aircraft["BAR1"].WaitingForLaunch {
		t.Error("launchNextDeparture: the launched aircraft is still waiting")
	}

	// The gate FOO1 goes out over is still in use, so nothing launches.
	s.launchNextDeparture(rwy9, "XTST", "9", now.Add(time.Second))
	if len(rwy9.ReleasedIFR) != 1 {
		t.Error("launchNextDeparture: launched a departure out a gate just used")
	}
}

func TestProcessGateDeparturesBoundsTheQueue(t *testing.T) {
	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)

	for i := range maxHoldingShort {
		cs := av.ADSBCallsign("HLD" + string(rune('A'+i)))
		rwy9.ReleasedIFR = append(rwy9.ReleasedIFR, stageDeparture(s, cs, "FOO", now))
	}
	rwy9.Gate = []DepartureAircraft{stageDeparture(s, "GAT1", "BAR", Time{})}

	s.processGateDepartures(rwy9, now)
	if len(rwy9.Gate) != 1 {
		t.Error("processGateDepartures: a full queue should leave the departure at the gate")
	}

	rwy9.ReleasedIFR = rwy9.ReleasedIFR[:maxHoldingShort-1]
	s.processGateDepartures(rwy9, now)
	if len(rwy9.Gate) != 0 || len(rwy9.ReleasedIFR) != maxHoldingShort {
		t.Errorf("processGateDepartures: gate %d, queue %d; want 0 and %d",
			len(rwy9.Gate), len(rwy9.ReleasedIFR), maxHoldingShort)
	}
	if rwy9.ReleasedIFR[maxHoldingShort-1].QueuedTime != now {
		t.Error("processGateDepartures: didn't record when the departure joined the queue")
	}
}

func TestProcessHeldDeparturesCullsDuringPrespawn(t *testing.T) {
	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)
	s.STARSComputer = makeSTARSComputer("TEST")

	hold := func(callsign av.ADSBCallsign, request Time) DepartureAircraft {
		dep := stageDeparture(s, callsign, "FOO", Time{})
		dep.RequestReleaseTime = request
		ac := s.Aircraft[callsign]
		ac.HoldForRelease = true
		s.STARSComputer.AddHeldDeparture(ac)
		return dep
	}

	// HLD1 is ready to ask for its release; HLD2 won't be for another minute.
	rwy9.Held = []DepartureAircraft{hold("HLD1", now), hold("HLD2", now.Add(time.Minute))}

	s.prespawnUncontrolledOnly = true
	s.processHeldDepartures(rwy9, now)

	if len(rwy9.Held) != 1 || rwy9.Held[0].ADSBCallsign != "HLD2" {
		t.Errorf("processHeldDepartures: held %v; want HLD2 alone",
			util.MapSlice(rwy9.Held, func(dep DepartureAircraft) av.ADSBCallsign { return dep.ADSBCallsign }))
	}
	if _, ok := s.Aircraft["HLD1"]; ok {
		t.Error("processHeldDepartures: didn't delete the departure with nobody to release it")
	}
	if len(s.STARSComputer.HoldForRelease) != 1 {
		t.Errorf("processHeldDepartures: %d in the release list; want 1",
			len(s.STARSComputer.HoldForRelease))
	}

	// Once the user is in charge, a departure waits for a release rather
	// than getting one from the sim.
	s.prespawnUncontrolledOnly = false
	now = now.Add(time.Minute)
	s.State.SimTime = now
	s.processHeldDepartures(rwy9, now)

	if len(rwy9.Held) != 1 || len(rwy9.ReleasedIFR) != 0 {
		t.Errorf("processHeldDepartures: held %d, holding short %d; want 1 and 0",
			len(rwy9.Held), len(rwy9.ReleasedIFR))
	}
	if !rwy9.Held[0].ReleaseRequested {
		t.Error("processHeldDepartures: didn't ask for a release")
	}
	if s.Aircraft["HLD2"].Released {
		t.Error("processHeldDepartures: released a departure the controller didn't")
	}
}
