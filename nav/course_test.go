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

// courseInterceptFlight sets up an aircraft leaving SKORR on a heading 20
// degrees to the left of the direct course to WAVEY, with an @t termination
// giving a course to WAVEY 20 degrees to the right of it. The aircraft
// therefore starts well off the course and converges on it at 40 degrees.
func courseInterceptFlight(t *testing.T) (f *FlightTest, heading, course math.MagneticHeading) {
	t.Helper()

	skorr, ok := av.DB.LookupWaypoint("SKORR")
	if !ok {
		t.Fatal("SKORR not found")
	}
	wavey, ok := av.DB.LookupWaypoint("WAVEY")
	if !ok {
		t.Fatal("WAVEY not found")
	}

	// Match what NewArrivalFlight uses for the aircraft's flight state.
	kjfk := av.DB.Airports["KJFK"]
	nmPerLongitude := math.NMPerLongitudeAt(kjfk.Location)
	magneticVariation, err := av.DB.MagneticGrid.Lookup(kjfk.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}

	direct := math.TrueToMagnetic(math.Heading2LL(skorr, wavey, nmPerLongitude), magneticVariation)
	heading = math.NormalizeHeading(direct - 20)
	course = math.NormalizeHeading(direct + 20)

	f = NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        fmt.Sprintf("SKORR/h%d@t%d WAVEY", int(heading), int(course)),
		DepartureAirport: "KJFK",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})
	return
}

// courseOffset returns the aircraft's perpendicular distance in nm from the
// line through WAVEY along the given course.
func courseOffset(f *FlightTest, course math.MagneticHeading) float32 {
	wavey, _ := av.DB.LookupWaypoint("WAVEY")
	nmPerLongitude := f.nav.FlightState.NmPerLongitude
	p0 := math.LL2NM(wavey, nmPerLongitude)
	trueCourse := math.MagneticToTrue(course, f.nav.FlightState.MagneticVariation)
	p1 := math.Add2f(p0, math.SinCos(math.Radians(trueCourse)))
	p := math.LL2NM(f.nav.FlightState.Position, nmPerLongitude)
	return math.SignedPointLineDistance(p, p0, p1)
}

// checkCourseIntercept runs the flight and verifies that the aircraft holds
// its heading, joins the course rather than turning direct to the fix
// immediately, and then tracks the course in to it.
func checkCourseIntercept(t *testing.T, f *FlightTest, heading, course math.MagneticHeading) {
	t.Helper()

	initialOffset := courseOffset(f, course)
	if math.Abs(initialOffset) < 5 {
		t.Fatalf("aircraft starts only %.1fnm off the %03d course; nothing to intercept",
			math.Abs(initialOffset), int(course))
	}

	// The @t termination is met when the maneuver flying the heading
	// completes and the aircraft goes direct to the fix.
	flyingHeading, joinTick := false, -1
	var maxOffsetOnCourse float32
	f.BeforeFix("WAVEY", func(f *FlightTest) {
		if !flyingHeading {
			flyingHeading = len(f.nav.Heading.Maneuvers) > 0
		} else if joinTick == -1 {
			if len(f.nav.Heading.Maneuvers) == 0 {
				joinTick = f.tick
			}
		} else if f.tick > joinTick+60 { // once the turn onto the course is done
			maxOffsetOnCourse = max(maxOffsetOnCourse, math.Abs(courseOffset(f, course)))
		}
	})

	// Well before the intercept, the aircraft should still be flying the
	// heading it was given rather than heading for the fix.
	f.AfterTicks(60, func(f *FlightTest) {
		f.AssertHeadingNear(float32(heading), 2)

		// What Nav.Summary reports for the aircraft while it waits.
		want := fmt.Sprintf("fly heading %03d until intercept %03d", int(heading), int(course))
		if got := f.nav.Heading.Maneuvers[0].String(); got != want {
			t.Errorf("maneuver summary is %q, want %q", got, want)
		}
	})

	f.Run()

	if joinTick == -1 {
		t.Fatalf("aircraft never joined the %03d course to WAVEY", int(course))
	}
	if joinTick < 60 {
		t.Errorf("joined the course at tick %d; expected it to fly the heading first", joinTick)
	}
	if maxOffsetOnCourse == 0 {
		t.Fatal("joined the course too close to WAVEY to check that it tracked it")
	}
	if maxOffsetOnCourse > 0.5 {
		t.Errorf("aircraft strayed %.2fnm from the %03d course after joining it",
			maxOffsetOnCourse, int(course))
	}
}

func TestCourseToFixIntercept(t *testing.T) {
	f, heading, course := courseInterceptFlight(t)
	checkCourseIntercept(t, f, heading, course)
}

// With a crosswind, flying direct to the fix after the intercept must still
// hold the charted ground track.
func TestCourseToFixInterceptWithWind(t *testing.T) {
	f, heading, course := courseInterceptFlight(t)
	f.SetWind(float32(math.NormalizeHeading(course-90)), 40)
	checkCourseIntercept(t, f, heading, course)
}
