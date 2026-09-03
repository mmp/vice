// nav/course_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

// skorrWaveyCourse returns the magnetic course from SKORR to WAVEY, using the
// flight state that NewArrivalFlight sets up for a KJFK arrival.
func skorrWaveyCourse(t *testing.T) math.MagneticHeading {
	t.Helper()

	skorr, ok := av.DB.LookupWaypoint("SKORR")
	if !ok {
		t.Fatal("SKORR not found")
	}
	wavey, ok := av.DB.LookupWaypoint("WAVEY")
	if !ok {
		t.Fatal("WAVEY not found")
	}

	kjfk := av.DB.Airports["KJFK"]
	nmPerLongitude := math.NMPerLongitudeAt(kjfk.Location)
	magneticVariation, err := av.DB.MagneticGrid.Lookup(kjfk.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}
	return math.TrueToMagnetic(math.Heading2LL(skorr, wavey, nmPerLongitude), magneticVariation)
}

// newSkorrWaveyFlight sets up a KJFK arrival at 10,000 feet starting at
// SKORR and flying the given route.
func newSkorrWaveyFlight(t *testing.T, waypoints string) *FlightTest {
	t.Helper()
	return NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        waypoints,
		DepartureAirport: "KJFK",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})
}

// courseInterceptFlight sets up an aircraft leaving SKORR on a heading 20
// degrees to the left of the direct course to WAVEY, with a /@crs trigger
// giving a course to WAVEY 20 degrees to the right of it. The aircraft
// therefore starts well off the course and converges on it at 40 degrees.
func courseInterceptFlight(t *testing.T) (f *FlightTest, heading, course math.MagneticHeading) {
	t.Helper()

	direct := skorrWaveyCourse(t)
	heading = math.NormalizeHeading(direct - 20)
	course = math.NormalizeHeading(direct + 20)
	f = newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@crs%d WAVEY", int(heading), int(course)))
	return
}

// courseOffset returns the aircraft's perpendicular distance in nm from the
// line through fix along the given magnetic course.
func courseOffset(f *FlightTest, fix string, course math.MagneticHeading) float32 {
	p, _ := av.DB.LookupWaypoint(fix)
	nmPerLongitude := f.nav.FlightState.NmPerLongitude
	p0 := math.LL2NM(p, nmPerLongitude)
	trueCourse := math.MagneticToTrue(course, f.nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0, math.SinCos(math.Radians(trueCourse)))
	return math.SignedPointLineDistance(math.LL2NM(f.nav.FlightState.Position, nmPerLongitude), p0, p1)
}

// checkJoinsCourse runs the flight and verifies that the aircraft holds its
// heading, joins the given course to fix rather than turning direct to it
// immediately, and then tracks the course in to it.
func checkJoinsCourse(t *testing.T, f *FlightTest, fix string, heading, course math.MagneticHeading) {
	t.Helper()

	if offset := math.Abs(courseOffset(f, fix, course)); offset < 5 {
		t.Fatalf("aircraft starts only %.1fnm off the %03d course; nothing to intercept", offset, int(course))
	}

	// The maneuver flying the heading completes when it is time to turn onto
	// the course, and the aircraft then goes direct to the fix.
	flyingHeading, joinTick := false, -1
	var maxOffsetOnCourse float32
	f.BeforeFix(fix, func(f *FlightTest) {
		if !flyingHeading {
			flyingHeading = len(f.nav.Heading.Maneuvers) > 0
		} else if joinTick == -1 {
			if len(f.nav.Heading.Maneuvers) == 0 {
				joinTick = f.tick
			}
		} else if f.tick > joinTick+60 { // once the turn onto the course is done
			maxOffsetOnCourse = max(maxOffsetOnCourse, math.Abs(courseOffset(f, fix, course)))
		}
	})

	// Well before the intercept, the aircraft should still be flying the
	// heading it was given rather than heading for the fix.
	f.AfterTicks(60, func(f *FlightTest) {
		f.AssertHeadingNear(float32(heading), 2)

		// What Nav.Summary reports for the aircraft while it waits.
		want := fmt.Sprintf("fly heading %03d until intercept %03d course to %s", int(heading), int(course), fix)
		if got := f.nav.Heading.Maneuvers[0].String(); got != want {
			t.Errorf("maneuver summary is %q, want %q", got, want)
		}
	})

	f.Run()

	if joinTick == -1 {
		t.Fatalf("aircraft never joined the %03d course to %s", int(course), fix)
	}
	if joinTick < 60 {
		t.Errorf("joined the course at tick %d; expected it to fly the heading first", joinTick)
	}
	if maxOffsetOnCourse == 0 {
		t.Fatalf("joined the course too close to %s to check that it tracked it", fix)
	}
	if maxOffsetOnCourse > 0.5 {
		t.Errorf("aircraft strayed %.2fnm from the %03d course after joining it",
			maxOffsetOnCourse, int(course))
	}
}

func TestCourseToFixIntercept(t *testing.T) {
	f, heading, course := courseInterceptFlight(t)
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}

// A course that is a VOR's radial is referenced to the station's declination
// rather than to the area's variation. Declinated 5 degrees west of the area,
// the station's radials read 5 degrees higher than the area's bearings of the
// same lines, so this flies the same course as TestCourseToFixIntercept.
func TestCourseToFixInterceptStationDeclination(t *testing.T) {
	vor := declinatedVOR(t, 5)
	direct := skorrWaveyCourse(t)
	heading := math.NormalizeHeading(direct - 20)
	course := math.MagneticHeading(int(math.NormalizeHeading(direct + 20)))
	f := newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@crs%s-R%d WAVEY", int(heading), vor,
		int(math.NormalizeHeading(course+5))))
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}

// A leg may fly a radial inbound, toward the station: the radial names the
// line and the fix the direction along it, so giving the reciprocal of the
// course flies the same intercept as TestCourseToFixInterceptStationDeclination.
func TestCourseToFixInterceptInboundRadial(t *testing.T) {
	vor := declinatedVOR(t, 5)
	direct := skorrWaveyCourse(t)
	heading := math.NormalizeHeading(direct - 20)
	course := math.MagneticHeading(int(math.NormalizeHeading(direct + 20)))
	radial := math.OppositeHeading(math.NormalizeHeading(course + 5))
	f := newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@crs%s-R%d WAVEY", int(heading), vor, int(radial)))
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}

// The SUMMA2 departure's runway 16L transition from the CIFP,
// KSEA-34R/h164/@crsSEA-R161 NEVJO: the charted heading is the runway's and
// the course to NEVJO is the SEA 161 radial, which the extended centerline
// lies along--the departure end is 400 feet east of it. The aircraft joins
// the course from where it is rather than turning to a 45 degree intercept
// to close those few hundred feet.
func TestCourseInterceptSummaRunway16L(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		// The KSEA-34R runway fix as a lat-long, since the test locator only
		// resolves database fixes.
		Waypoints:        "N047.25.52.220,W122.18.28.940/h164/@crsSEA-R161 NEVJO",
		DepartureAirport: "KSEA",
		ArrivalAirport:   "KSEA",
		AircraftType:     "A320",
		InitialAltitude:  500,
		InitialSpeed:     170,
		ClearedAltitude:  10000,
	})
	f.nav.FlightState.Heading = 164
	f.nav.FlightState.InitialDepartureClimb = true
	f.nav.FinalAltitude = 10000

	var maxTurn float32
	f.BeforeFix("NEVJO", func(f *FlightTest) {
		maxTurn = max(maxTurn, math.HeadingDifference(f.nav.FlightState.Heading, 164))
	})
	passed := false
	f.AtFix("NEVJO", func(f *FlightTest) { passed = true })
	f.Run()

	if maxTurn > 5 {
		t.Errorf("aircraft turned %.0f degrees off the runway heading before NEVJO; "+
			"expected it to stay on the course", maxTurn)
	}
	if !passed {
		t.Error("aircraft never crossed NEVJO")
	}
}

// With a crosswind, flying direct to the fix after the intercept must still
// hold the charted ground track.
func TestCourseToFixInterceptWithWind(t *testing.T) {
	f, heading, course := courseInterceptFlight(t)
	f.SetWind(float32(math.NormalizeHeading(course-90)), 40)
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}

// A heading 40 degrees to the right of a course the aircraft is already
// right of diverges from it; after taking up the heading the aircraft must
// turn back to meet the course at 45 degrees.
func TestCourseInterceptFromDivergingHeading(t *testing.T) {
	direct := skorrWaveyCourse(t)
	heading := math.NormalizeHeading(direct + 40)
	course := math.NormalizeHeading(direct + 20)
	f := newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@crs%d WAVEY", int(heading), int(course)))
	checkJoinsCourse(t, f, "WAVEY", math.NormalizeHeading(course-45), course)
}

// A heading that converges on the course at only a few degrees from miles
// away would cross it far beyond WAVEY; the aircraft turns to a 45 degree
// intercept rather than dragging in at a glancing angle.
func TestCourseInterceptFromShallowHeading(t *testing.T) {
	direct := skorrWaveyCourse(t)
	heading := math.NormalizeHeading(direct + 15)
	course := math.NormalizeHeading(direct + 20)
	f := newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@crs%d WAVEY", int(heading), int(course)))
	checkJoinsCourse(t, f, "WAVEY", math.NormalizeHeading(course-45), course)
}

// The ELMOO9 departure's runway 26 transition from the CIFP,
// KBUR-8/t259/@a1178+/l113/@crs095 ELMOO: the left turn to 113 rolls out
// south of the 095 course to ELMOO pointing away from it. The aircraft must
// continue the turn around to the 45 degree intercept rather than settling
// on 113, then roll out on the course and cross ELMOO.
func TestCourseInterceptElmooRunway26(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		// The KBUR-8 runway fix as a lat-long, since the test locator only
		// resolves database fixes.
		Waypoints:        "N034.11.52.480,W118.22.08.910/t259/@a1178+/l113/@crs095 ELMOO/a4000+",
		DepartureAirport: "KBUR",
		ArrivalAirport:   "KLAX",
		AircraftType:     "A320",
		InitialAltitude:  800,
		InitialSpeed:     160,
		ClearedAltitude:  5000,
	})
	f.nav.FlightState.Heading = 259
	f.nav.FlightState.InitialDepartureClimb = true
	f.nav.FinalAltitude = 5000

	const course = 95
	held113, sawIntercept, flying := 0, false, false
	joinTick := -1
	var maxOffsetOnCourse float32
	f.BeforeFix("ELMOO", func(f *FlightTest) {
		if math.HeadingDifference(f.nav.FlightState.Heading, 113) < 1 {
			held113++
		}
		if len(f.nav.Heading.Maneuvers) > 0 &&
			f.nav.Heading.Maneuvers[0].String() == "fly heading 050 until intercept 095 course to ELMOO" {
			sawIntercept = true
		}
		if !flying {
			flying = len(f.nav.Heading.Maneuvers) > 0
		} else if joinTick == -1 {
			if len(f.nav.Heading.Maneuvers) == 0 {
				joinTick = f.tick
			}
		} else if f.tick > joinTick+60 {
			maxOffsetOnCourse = max(maxOffsetOnCourse, math.Abs(courseOffset(f, "ELMOO", course)))
		}
	})
	passed := false
	f.AtFix("ELMOO", func(f *FlightTest) { passed = true })
	f.Run()

	if held113 > 5 {
		t.Errorf("aircraft settled on heading 113 for %d ticks instead of continuing to the intercept", held113)
	}
	if !sawIntercept {
		t.Errorf("aircraft never turned to the 45 degree intercept of the 095 course")
	}
	if joinTick == -1 {
		t.Fatal("aircraft never joined the 095 course to ELMOO")
	}
	if maxOffsetOnCourse > 0.5 {
		t.Errorf("aircraft strayed %.2fnm from the course after joining it", maxOffsetOnCourse)
	}
	if !passed {
		t.Errorf("aircraft never crossed ELMOO")
	}
}

// A departure tracks the runway centerline from the runway's midpoint until
// it is 400' above the field, and only then turns on course. It must hold
// the centerline in a crosswind without crabbing while it is still rolling.
func TestDepartureTracksCenterlineToFourHundred(t *testing.T) {
	// A heavy on a long runway is still rolling at the runway's midpoint.
	const icao, runway, acType = "KBOS", "33L", "B744"
	r, ok := av.LookupRunway(icao, runway)
	rend, ok2 := av.LookupOppositeRunway(icao, runway)
	if !ok || !ok2 {
		t.Fatalf("no runway %s %s", icao, runway)
	}
	ap := av.DB.Airports[icao]
	nmPerLongitude := math.NMPerLongitudeAt(ap.Location)
	magneticVariation, err := av.DB.MagneticGrid.Lookup(ap.Location)
	if err != nil {
		t.Fatal(err)
	}
	course := math.TrueToMagnetic(math.Heading2LL(r.Threshold, rend.Threshold, nmPerLongitude), magneticVariation)

	// The waypoints ExitRoute.initialize gives a departure: the threshold,
	// the runway's midpoint holding the centerline to 400' above the field,
	// and then a fix well off to the right of the runway.
	mid := av.Waypoint{Fix: runway + "-mid", Location: math.Lerp2f(0.5, r.Threshold, rend.Threshold)}
	mid.InitExtra().ActionGroups = []av.WaypointActionGroup{
		{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{
				Heading: int16(math.Round(float32(math.NormalizeHeading(course)))), Track: true}},
			Until: av.WaypointActionTermination{Type: av.WaypointActionAltitude,
				Altitude: ap.Elevation + 400, AtOrAbove: true},
		},
	}
	exit := math.Offset2LL(r.Threshold, math.NormalizeHeading(math.Heading2LL(r.Threshold, rend.Threshold,
		nmPerLongitude)+45), 15, nmPerLongitude)
	wps := []av.Waypoint{{Fix: runway, Location: r.Threshold}, mid, {Fix: "EXITF", Location: exit}}

	fp := av.FlightPlan{Rules: av.FlightRulesIFR, AircraftType: acType, DepartureAirport: icao,
		ArrivalAirport: icao, Altitude: 8000}
	perf, ok := av.DB.AircraftPerformance[fp.AircraftType]
	if !ok {
		t.Fatalf("no performance for %s", fp.AircraftType)
	}
	simTime := NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))
	n := MakeDepartureNav("TEST001", fp, perf, 0, 5000, wps, false, nmPerLongitude, magneticVariation,
		nil, simTime, nil)
	if n == nil {
		t.Fatal("no nav")
	}

	// A 25 knot crosswind from the right of the runway.
	std := wx.MakeStandardSampleForAltitude(float32(ap.Elevation))
	v := math.SinCos(math.Radians(math.MagneticToTrue(course, magneticVariation) - 90))
	wxs := wx.MakeSample([2]float32{v[0] * 25 / 3600, v[1] * 25 / 3600}, std.Temperature().Celsius(),
		std.Dewpoint().Celsius(), std.Pressure())

	t0 := math.LL2NM(r.Threshold, nmPerLongitude)
	dir := math.Normalize2f(math.Sub2f(math.LL2NM(rend.Threshold, nmPerLongitude), t0))
	perp := [2]float32{dir[1], -dir[0]}
	offset := func() float32 {
		return math.Abs(math.Dot(math.Sub2f(math.LL2NM(n.FlightState.Position, nmPerLongitude), t0), perp))
	}

	var maxOffsetRolling, maxOffsetLow, aglTurned float32
	tracking, turned, rolledTracking := false, false, false
	for range 200 {
		n.UpdateWithWeather("TEST001", wxs, nil, &fp, simTime, nil)
		simTime = simTime.Add(time.Second)
		if !n.IsAirborne() {
			maxOffsetRolling = max(maxOffsetRolling, offset())
			rolledTracking = rolledTracking || len(n.Heading.Maneuvers) > 0
		}
		agl := n.FlightState.Altitude - float32(ap.Elevation)
		if agl < 400 {
			maxOffsetLow = max(maxOffsetLow, offset())
		}
		// The track is flown until 400' above the field, at which point the
		// maneuver ends and the aircraft navigates to the exit fix.
		if len(n.Heading.Maneuvers) > 0 {
			tracking = true
		} else if tracking && !turned {
			turned, aglTurned = true, agl
		}
	}

	if !rolledTracking {
		t.Error("aircraft was airborne before the runway's midpoint; the takeoff roll isn't being tested")
	}
	if maxOffsetRolling > 100*math.FeetToNauticalMiles {
		t.Errorf("drifted %.0f' off the centerline during the takeoff roll",
			maxOffsetRolling/math.FeetToNauticalMiles)
	}
	if maxOffsetLow > 300*math.FeetToNauticalMiles {
		t.Errorf("drifted %.0f' off the extended centerline below 400' AGL",
			maxOffsetLow/math.FeetToNauticalMiles)
	}
	if !turned {
		t.Error("never turned toward the exit fix after reaching 400' AGL")
	} else if aglTurned < 400 || aglTurned > 500 {
		t.Errorf("turned on course at %.0f' AGL, expected right at 400'", aglTurned)
	}
}
