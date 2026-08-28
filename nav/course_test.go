// nav/course_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
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
// degrees to the left of the direct course to WAVEY, with an @t termination
// giving a course to WAVEY 20 degrees to the right of it. The aircraft
// therefore starts well off the course and converges on it at 40 degrees.
func courseInterceptFlight(t *testing.T) (f *FlightTest, heading, course math.MagneticHeading) {
	t.Helper()

	direct := skorrWaveyCourse(t)
	heading = math.NormalizeHeading(direct - 20)
	course = math.NormalizeHeading(direct + 20)
	f = newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d@t%d WAVEY", int(heading), int(course)))
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

// With a crosswind, flying direct to the fix after the intercept must still
// hold the charted ground track.
func TestCourseToFixInterceptWithWind(t *testing.T) {
	f, heading, course := courseInterceptFlight(t)
	f.SetWind(float32(math.NormalizeHeading(course-90)), 40)
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}
