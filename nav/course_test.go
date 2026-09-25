// nav/course_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// skorrWaveyCourse returns the magnetic course from SKORR to WAVEY, using the
// flight state that NewArrivalFlight sets up for a KJFK arrival.
func skorrWaveyCourse(t *testing.T) math.MagneticHeading {
	t.Helper()

	skorr, ok := db.DB.LookupWaypoint("SKORR")
	if !ok {
		t.Fatal("SKORR not found")
	}
	wavey, ok := db.DB.LookupWaypoint("WAVEY")
	if !ok {
		t.Fatal("WAVEY not found")
	}

	kjfk := db.DB.Airports["KJFK"]
	nmPerLongitude := math.NMPerLongitudeAt(kjfk.Location)
	magneticVariation, err := db.DB.MagneticGrid.Lookup(kjfk.Location)
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
	p, _ := db.DB.LookupWaypoint(fix)
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
	r, ok := av.LookupRunway(db.Lookups{}, icao, runway)
	rend, ok2 := av.LookupOppositeRunway(db.Lookups{}, icao, runway)
	if !ok || !ok2 {
		t.Fatalf("no runway %s %s", icao, runway)
	}
	ap := db.DB.Airports[icao]
	nmPerLongitude := math.NMPerLongitudeAt(ap.Location)
	magneticVariation, err := db.DB.MagneticGrid.Lookup(ap.Location)
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
	perf, ok := db.DB.AircraftPerformance[fp.AircraftType]
	if !ok {
		t.Fatalf("no performance for %s", fp.AircraftType)
	}
	simTime := NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))
	n := MakeDepartureNav("TEST001", fp, perf, 0, 5000, wps, false, nmPerLongitude, magneticVariation,
		nil, simTime, rand.New(42), nil)
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

// centerlineDeparture is a departure rolling on KBOS 33L whose
// runway-midpoint waypoint carries the centerline-to-400' group and then a
// test's own action groups, with the fix EXITF off to the right of the
// runway, in calm air.
type centerlineDeparture struct {
	nav       *Nav
	fp        av.FlightPlan
	elevation int
	simTime   Time
	wxs       wx.Sample
}

func makeCenterlineDeparture(t *testing.T, groups []av.WaypointActionGroup) centerlineDeparture {
	t.Helper()
	const icao, runway, acType = "KBOS", "33L", "B744"
	r, ok := av.LookupRunway(db.Lookups{}, icao, runway)
	rend, ok2 := av.LookupOppositeRunway(db.Lookups{}, icao, runway)
	if !ok || !ok2 {
		t.Fatalf("no runway %s %s", icao, runway)
	}
	ap := db.DB.Airports[icao]
	nmPerLongitude := math.NMPerLongitudeAt(ap.Location)
	magneticVariation, err := db.DB.MagneticGrid.Lookup(ap.Location)
	if err != nil {
		t.Fatal(err)
	}
	course := math.TrueToMagnetic(math.Heading2LL(r.Threshold, rend.Threshold, nmPerLongitude), magneticVariation)

	mid := av.Waypoint{Fix: runway + "-mid", Location: math.Lerp2f(0.5, r.Threshold, rend.Threshold)}
	mid.InitExtra().ActionGroups = append([]av.WaypointActionGroup{
		{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{
				Heading: int16(math.Round(float32(math.NormalizeHeading(course)))), Track: true}},
			Until: av.WaypointActionTermination{Type: av.WaypointActionAltitude,
				Altitude: ap.Elevation + 400, AtOrAbove: true},
		},
	}, groups...)
	exit := math.Offset2LL(r.Threshold, math.NormalizeHeading(math.Heading2LL(r.Threshold, rend.Threshold,
		nmPerLongitude)+45), 15, nmPerLongitude)
	wps := []av.Waypoint{{Fix: runway, Location: r.Threshold}, mid, {Fix: "EXITF", Location: exit}}

	fp := av.FlightPlan{Rules: av.FlightRulesIFR, AircraftType: acType, DepartureAirport: icao,
		ArrivalAirport: icao, Altitude: 8000}
	perf, ok := db.DB.AircraftPerformance[fp.AircraftType]
	if !ok {
		t.Fatalf("no performance for %s", fp.AircraftType)
	}
	simTime := NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC))
	n := MakeDepartureNav("TEST001", fp, perf, 0, 5000, wps, false, nmPerLongitude, magneticVariation,
		nil, simTime, rand.New(42), nil)
	if n == nil {
		t.Fatal("no nav")
	}

	std := wx.MakeStandardSampleForAltitude(float32(ap.Elevation))
	wxs := wx.MakeSample([2]float32{0, 0}, std.Temperature().Celsius(), std.Dewpoint().Celsius(), std.Pressure())
	return centerlineDeparture{nav: n, fp: fp, elevation: ap.Elevation, simTime: simTime, wxs: wxs}
}

// An absorbed departure-end action group that only carries sim actions (like
// /ho) fires once the aircraft is 400' above the field and the aircraft then
// continues on its route rather than holding its heading.
func TestDepartureEventActionsFireAtFourHundred(t *testing.T) {
	d := makeCenterlineDeparture(t, []av.WaypointActionGroup{
		{Actions: av.WaypointActions{HumanHandoff: true}},
	})

	var aglHandoff, aglResumed float32
	maneuvering, resumed, handoff := false, false, false
	for range 200 {
		result := d.nav.UpdateWithWeather("TEST001", d.wxs, nil, &d.fp, d.simTime, nil)
		d.simTime = d.simTime.Add(time.Second)
		agl := d.nav.FlightState.Altitude - float32(d.elevation)
		if slices.ContainsFunc(result.ActionEvents,
			func(e av.WaypointActionEvent) bool { return e.Actions.HumanHandoff }) {
			handoff, aglHandoff = true, agl
		}
		if len(d.nav.Heading.Maneuvers) > 0 {
			maneuvering = true
		} else if maneuvering && !resumed {
			resumed, aglResumed = true, agl
		}
	}

	if !handoff {
		t.Error("handoff action never fired")
	} else if aglHandoff < 400 || aglHandoff > 500 {
		t.Errorf("handoff fired at %.0f' AGL, expected right at 400'", aglHandoff)
	}
	if !resumed {
		t.Error("never resumed the route after the handoff")
	} else if aglResumed < 400 || aglResumed > 600 {
		t.Errorf("resumed the route at %.0f' AGL, expected right after 400'", aglResumed)
	}
}

// Sim actions behind an explicit heading leg's trigger, as in
// FIX/h270/@a2000+/ho, fire when the trigger is met and the aircraft then
// continues on its route.
func TestDepartureDelayedEventActionResumesRoute(t *testing.T) {
	elevation := db.DB.Airports["KBOS"].Elevation
	d := makeCenterlineDeparture(t, []av.WaypointActionGroup{
		{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 270}},
			Until: av.WaypointActionTermination{Type: av.WaypointActionAltitude,
				Altitude: elevation + 2000, AtOrAbove: true},
		},
		{Actions: av.WaypointActions{HumanHandoff: true}},
	})

	var aglHandoff, aglResumed float32
	maneuvering, resumed, handoff := false, false, false
	for range 300 {
		result := d.nav.UpdateWithWeather("TEST001", d.wxs, nil, &d.fp, d.simTime, nil)
		d.simTime = d.simTime.Add(time.Second)
		agl := d.nav.FlightState.Altitude - float32(d.elevation)
		if slices.ContainsFunc(result.ActionEvents,
			func(e av.WaypointActionEvent) bool { return e.Actions.HumanHandoff }) {
			handoff, aglHandoff = true, agl
		}
		if len(d.nav.Heading.Maneuvers) > 0 {
			maneuvering = true
		} else if maneuvering && !resumed {
			resumed, aglResumed = true, agl
		}
	}

	if !handoff {
		t.Error("handoff action never fired")
	} else if aglHandoff < 2000 || aglHandoff > 2200 {
		t.Errorf("handoff fired at %.0f' AGL, expected right at 2000'", aglHandoff)
	}
	if !resumed {
		t.Error("never resumed the route after the handoff")
	} else if aglResumed < 2000 || aglResumed > 2300 {
		t.Errorf("resumed the route at %.0f' AGL, expected right after 2000'", aglResumed)
	}
}

// /delete rides in the action group it is written in, so a trigger holds it
// off the way it does any other action: FIX/h270/@a2000+/delete removes the
// aircraft on climbing through 2,000', not on passing the fix.
func TestDelayedDeleteActionFiresAtItsTrigger(t *testing.T) {
	elevation := db.DB.Airports["KBOS"].Elevation
	d := makeCenterlineDeparture(t, []av.WaypointActionGroup{
		{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 270}},
			Until: av.WaypointActionTermination{Type: av.WaypointActionAltitude,
				Altitude: elevation + 2000, AtOrAbove: true},
		},
		{Actions: av.WaypointActions{Delete: true}},
	})

	var aglDelete float32
	deleted := false
	for range 300 {
		result := d.nav.UpdateWithWeather("TEST001", d.wxs, nil, &d.fp, d.simTime, nil)
		d.simTime = d.simTime.Add(time.Second)
		if !deleted && slices.ContainsFunc(result.ActionEvents,
			func(e av.WaypointActionEvent) bool { return e.Actions.Delete }) {
			deleted, aglDelete = true, d.nav.FlightState.Altitude-float32(d.elevation)
		}
	}

	if !deleted {
		t.Error("delete action never fired")
	} else if aglDelete < 2000 || aglDelete > 2200 {
		t.Errorf("delete fired at %.0f' AGL, expected right at 2000'", aglDelete)
	}
}

// A final group that gives a heading, as in FIX/h270/@a2000+/r055, is flown
// until controller intervention; the aircraft does not resume its route on
// its own.
func TestDepartureTrailingHeadingHeldUntilIntervention(t *testing.T) {
	elevation := db.DB.Airports["KBOS"].Elevation
	d := makeCenterlineDeparture(t, []av.WaypointActionGroup{
		{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 270}},
			Until: av.WaypointActionTermination{Type: av.WaypointActionAltitude,
				Altitude: elevation + 2000, AtOrAbove: true},
		},
		{Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 55, Turn: av.TurnRight}}},
	})

	for range 300 {
		d.nav.UpdateWithWeather("TEST001", d.wxs, nil, &d.fp, d.simTime, nil)
		d.simTime = d.simTime.Add(time.Second)
	}

	if len(d.nav.Heading.Maneuvers) == 0 {
		t.Error("maneuvers ended; expected the final heading to be held until controller intervention")
	}
	if math.HeadingDifference(d.nav.FlightState.Heading, 55) > 1 {
		t.Errorf("flying heading %.0f, expected to hold 055", d.nav.FlightState.Heading)
	}
}

// radialFlight sets up an arrival at SKORR heading direct to WAVEY, along
// with the magnetic course between the two.
func radialFlight(t *testing.T) (*FlightTest, math.MagneticHeading) {
	t.Helper()
	direct := skorrWaveyCourse(t)
	return newSkorrWaveyFlight(t, "SKORR WAVEY SHIPP"), direct
}

// inboundRadialCourse returns the course, in the area's magnetic frame, of
// flying the given radial of a fix inbound. A VOR's radials are referenced to
// its station declination, so the course differs from the radial's reciprocal
// by the declination's offset from the area's variation.
func inboundRadialCourse(f *FlightTest, fix string, radial math.MagneticHeading) math.MagneticHeading {
	trueBearing := math.MagneticToTrue(radial, f.nav.radialVariation(fix))
	return math.OppositeHeading(math.TrueToMagnetic(trueBearing, f.nav.FlightState.MagneticVariation))
}

func distanceToFix(f *FlightTest, fix string) float32 {
	p, _ := db.DB.LookupWaypoint(fix)
	return math.NMDistance2LL(f.nav.FlightState.Position, p)
}

func assertNotUnable(t *testing.T, intent speech.CommandIntent) {
	t.Helper()
	if intent == nil {
		t.Fatal("no intent returned")
	} else if _, unable := intent.(speech.UnableIntent); unable {
		t.Fatalf("unable: %v", intent)
	}
}

// checkInbound vectors the aircraft 20 degrees left of the direct course to
// WAVEY and has it intercept the radial whose reciprocal is 20 degrees right
// of that course, so that it joins the same course to WAVEY that the /@crs
// tests do.
func checkInbound(t *testing.T, f *FlightTest, direct math.MagneticHeading) {
	t.Helper()

	heading := math.NormalizeHeading(direct - 20)
	course := math.NormalizeHeading(direct + 20)
	radial := math.OppositeHeading(course)

	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		assertNotUnable(t, f.InterceptRadial("WAVEY", int(radial), false))
	})
	checkJoinsCourse(t, f, "WAVEY", heading, course)
}

func TestInterceptRadialInbound(t *testing.T) {
	f, direct := radialFlight(t)
	checkInbound(t, f, direct)
}

func TestInterceptRadialInboundWithWind(t *testing.T) {
	f, direct := radialFlight(t)
	f.SetWind(float32(math.NormalizeHeading(direct+20-90)), 40)
	checkInbound(t, f, direct)
}

// The radial a controller gives is the navaid's, referenced to its station
// declination: a station declinated 5 degrees west of the area's variation
// has its radials read 5 degrees higher than the area's bearings of the
// same lines, so intercepting its reciprocal-of-(course+5) radial joins the
// same course inbound as checkInbound does.
func TestInterceptRadialStationDeclination(t *testing.T) {
	vor := declinatedVOR(t, 5)
	f, direct := radialFlight(t)

	heading := math.NormalizeHeading(direct - 20)
	course := math.NormalizeHeading(direct + 20)
	radial := math.NormalizeHeading(math.OppositeHeading(course) + 5)

	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		assertNotUnable(t, f.InterceptRadial(vor, int(radial), false))
	})
	checkJoinsCourse(t, f, vor, heading, course)
}

// After intercepting a radial inbound the aircraft resumes the route from the
// fix rather than stopping there.
func TestInterceptRadialInboundResumesRoute(t *testing.T) {
	f, direct := radialFlight(t)

	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(math.NormalizeHeading(direct-20)), av.TurnClosest)
		f.InterceptRadial("WAVEY", int(radial), false)
	})
	f.AtFix("WAVEY", func(f *FlightTest) {
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != "SHIPP" {
			t.Errorf("after WAVEY the route is %q, want SHIPP next", f.nav.Waypoints.Encode())
		}
	})
	f.Run()
}

// interceptOutbound vectors the aircraft 50 degrees right of the direct course
// to WAVEY and has it join the radial 90 degrees right of that course
// outbound, returning the radial.
func interceptOutbound(f *FlightTest, direct math.MagneticHeading) math.MagneticHeading {
	heading := math.NormalizeHeading(direct + 50)
	radial := math.NormalizeHeading(direct + 90)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		f.InterceptRadial("WAVEY", int(radial), true)
	})
	return radial
}

// checkOutbound verifies that the aircraft crosses the radial beyond WAVEY,
// turns onto it, and then tracks it away from the fix rather than turning
// back toward it.
func checkOutbound(t *testing.T, f *FlightTest, direct math.MagneticHeading) {
	t.Helper()

	radial := interceptOutbound(f, direct)

	// The aircraft never passes a fix here — tracking the radial takes it away
	// from everything — so check the invariants at intervals rather than with
	// a BeforeFix.
	var joinDist float32
	f.AfterTicks(900, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) != 1 {
			t.Fatalf("aircraft has %d maneuvers left; it should have joined the radial by now",
				len(f.nav.Heading.Maneuvers))
		}
		joinDist = distanceToFix(f, "WAVEY")
		if joinDist < 1 {
			t.Errorf("joined the radial %.1fnm from WAVEY; expected it to cross beyond the fix", joinDist)
		}
	})
	for _, tick := range []int{1100, 1300} {
		f.AfterTicks(tick, func(f *FlightTest) {
			if len(f.nav.Heading.Maneuvers) == 0 {
				t.Fatalf("tick %d: outbound maneuvers ended; the aircraft should track the radial "+
					"until told otherwise", f.tick)
			}
			if offset := math.Abs(courseOffset(f, "WAVEY", radial)); offset > 1 {
				t.Errorf("tick %d: aircraft strayed %.2fnm from the %03d radial", f.tick, offset, int(radial))
			}
			f.AssertHeadingNear(float32(radial), 15)
			if d := distanceToFix(f, "WAVEY"); d <= joinDist {
				t.Errorf("tick %d: aircraft is %.1fnm from WAVEY having joined at %.1fnm; "+
					"it should be tracking away from the fix", f.tick, d, joinDist)
			}
		})
	}

	f.Run()
}

func TestInterceptRadialOutbound(t *testing.T) {
	f, direct := radialFlight(t)
	checkOutbound(t, f, direct)
}

func TestInterceptRadialOutboundWithWind(t *testing.T) {
	f, direct := radialFlight(t)
	f.SetWind(float32(math.NormalizeHeading(direct)), 40)
	checkOutbound(t, f, direct)
}

// Tracking a radial outbound only ends when the controller says so, and
// "resume own navigation" is the usual way to say it: it has to be accepted
// and take the aircraft back to its route.
func TestInterceptRadialOutboundResumeOwnNavigation(t *testing.T) {
	f, direct := radialFlight(t)
	interceptOutbound(f, direct)

	f.AfterTicks(900, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) != 1 {
			t.Fatalf("aircraft has %d maneuvers left; it should be tracking the radial by now",
				len(f.nav.Heading.Maneuvers))
		}
		assertNotUnable(t, f.nav.ResumeOwnNavigation())
		if len(f.nav.Heading.Maneuvers) != 0 || f.nav.Heading.Assigned != nil {
			t.Error("aircraft is still on the radial after resuming own navigation")
		}
	})
	f.AfterTicks(1200, func(f *FlightTest) {
		bearing := math.TrueToMagnetic(math.Heading2LL(f.nav.FlightState.Position, f.nav.Waypoints[0].Location,
			f.nav.FlightState.NmPerLongitude), f.nav.FlightState.MagneticVariation)
		f.AssertHeadingNear(float32(bearing), 10)
	})
	f.Run()
}

// A subsequent heading assignment cancels an intercept in progress.
func TestInterceptRadialCancelledByHeading(t *testing.T) {
	f, direct := radialFlight(t)

	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(math.NormalizeHeading(direct-20)), av.TurnClosest)
		f.InterceptRadial("WAVEY", int(radial), false)
	})
	f.AfterTicks(60, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			t.Fatal("expected the intercept maneuver to be in progress")
		}
		f.AssignHeading(int(math.NormalizeHeading(direct+90)), av.TurnClosest)
	})
	f.AfterTicks(120, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) != 0 {
			t.Errorf("intercept maneuver survived a new heading assignment: %v",
				f.nav.Heading.Maneuvers[0].String())
		}
	})
	f.AfterTicks(240, func(f *FlightTest) {
		f.AssertHeadingNear(float32(math.NormalizeHeading(direct+90)), 5)
	})
	f.Run()
}

// The intercept flies the heading the controller assigned, whether or not the
// pilot has started following it yet.
func TestInterceptRadialUsesAssignedHeading(t *testing.T) {
	f, direct := radialFlight(t)

	hdg := math.MagneticHeading(int(math.NormalizeHeading(direct - 20)))
	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))

	check := func(when string) {
		t.Helper()
		if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 {
			t.Fatalf("%s: no maneuvers were queued", when)
		} else if got := dh.Maneuvers[0].Heading; got != hdg {
			t.Errorf("%s: maneuver flies heading %.0f, want the assigned %.0f", when, got, hdg)
		}
	}

	// Issued in the same transmission, while the turn is still deferred.
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(hdg), av.TurnClosest)
		f.InterceptRadial("WAVEY", int(radial), false)
		check("deferred heading")
	})
	// And again once the aircraft is established on the heading.
	f.AfterTicks(60, func(f *FlightTest) {
		f.InterceptRadial("WAVEY", int(radial), false)
		check("active heading")
	})
	f.Run()
}

// A JFK departure vectored south and told to intercept a radial that crosses
// its track nearly at right angles. The aircraft can't roll out on the course
// without overshooting, which used to mean it was never told to turn at all
// and flew straight through the radial.
func TestInterceptRadialSteepAngle(t *testing.T) {
	f, _ := radialFlight(t)

	f.nav.FlightState.Position = math.Point2LL{-73.866806, 40.516140}
	f.nav.FlightState.Heading = 180
	hdg := math.MagneticHeading(180)
	f.nav.Heading = Heading{Assigned: &hdg}

	const radial = 100
	course := inboundRadialCourse(f, "RBV", radial)
	assertNotUnable(t, f.InterceptRadial("RBV", radial, false))

	joinTick, maxOvershoot, maxOffsetOnCourse := -1, float32(0), float32(0)
	f.BeforeFix("RBV", func(f *FlightTest) {
		offset := courseOffset(f, "RBV", course)
		if joinTick == -1 {
			if len(f.nav.Heading.Maneuvers) == 0 && f.nav.DeferredNavHeading == nil {
				joinTick = f.tick
			}
		} else if f.tick > joinTick+90 { // once the turn onto the course is done
			maxOffsetOnCourse = max(maxOffsetOnCourse, math.Abs(offset))
		}
		// The aircraft starts on the positive side; how far past the course
		// does it get carried before it comes back?
		maxOvershoot = max(maxOvershoot, -offset)
	})
	f.Run()

	if joinTick == -1 {
		t.Fatal("aircraft flew through the radial without ever turning to join it")
	}
	if maxOvershoot > 3 {
		t.Errorf("aircraft went %.1fnm through the radial before coming back", maxOvershoot)
	}
	if maxOffsetOnCourse > 0.5 {
		t.Errorf("aircraft strayed %.2fnm from the course after joining it", maxOffsetOnCourse)
	}
}

// "L180 IRBV/065" right off runway 31L, from a position essentially on the
// RBV 064 radial: a tight intercept that comes up while the aircraft is still
// turning onto the assigned heading. The maneuver used to complete on the
// first tick — before the aircraft had flown any of the heading — and send it
// direct to the fix on a converging course instead of joining the radial.
func TestInterceptRadialDuringTurn(t *testing.T) {
	f, _ := radialFlight(t)

	kjfk := db.DB.Airports["KJFK"]
	f.nav.FlightState.Position = kjfk.Location
	f.nav.FlightState.Heading = 310 // just off runway 31L
	f.nav.Heading = Heading{}
	rbv, ok := db.DB.LookupWaypoint("RBV")
	if !ok {
		t.Fatal("RBV not found")
	}
	f.nav.Waypoints = []av.Waypoint{{Fix: "RBV", Location: rbv}}

	f.nav.AssignHeading(180, av.TurnLeft, f.simTime, 0)
	assertNotUnable(t, f.InterceptRadial("RBV", 65, false))

	course := inboundRadialCourse(f, "RBV", 65)
	f.AfterTicks(10, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			t.Fatal("intercept finished while the aircraft was still turning onto the heading")
		}
		f.AssertHeadingNear(280, 40) // still swinging left through the turn
	})

	// The turn has to be led so the aircraft rolls out on the radial: it must
	// not be started while the aircraft is still heading away from it, and it
	// must not be left so late that the aircraft crosses first.
	joined := false
	var joinOffset, worstAfterJoin, pastCourse float32
	f.BeforeFix("RBV", func(f *FlightTest) {
		offset := courseOffset(f, "RBV", course)
		if !joined {
			if len(f.nav.Heading.Maneuvers) == 0 && f.nav.DeferredNavHeading == nil {
				joined, joinOffset, worstAfterJoin = true, math.Abs(offset), math.Abs(offset)
			}
			return
		}
		worstAfterJoin = max(worstAfterJoin, math.Abs(offset))
		pastCourse = min(pastCourse, offset)
	})
	f.AfterTicks(300, func(f *FlightTest) {
		if !joined {
			t.Fatal("aircraft never joined the radial")
		}
		if worstAfterJoin > joinOffset+0.1 {
			t.Errorf("aircraft started the turn %.2fnm from the radial but then drifted out to %.2fnm; "+
				"it was still heading away from it", joinOffset, worstAfterJoin)
		}
		if -pastCourse > 0.5 {
			t.Errorf("aircraft flew %.2fnm past the radial before rolling out on it", -pastCourse)
		}
		if offset := math.Abs(courseOffset(f, "RBV", course)); offset > 0.5 {
			t.Errorf("aircraft is %.2fnm off the radial after joining it", offset)
		}
		f.AssertHeadingNear(float32(course), 5)
	})
	f.Run()
}

// "L180 IRBV/045" to a JFK departure: radials 045 and 225 are the two ends of
// one line through RBV, and a southbound track from JFK is on the RBV 064
// radial diverging from it, so the aircraft can never comply. It used to
// accept the instruction and fly the heading indefinitely without a word. The
// turn must still be accepted, and the refusal has to name the radial the
// aircraft is actually on so the controller can pick a usable one.
func TestInterceptRadialUnreachable(t *testing.T) {
	f, _ := radialFlight(t)

	kjfk := db.DB.Airports["KJFK"]
	f.nav.FlightState.Position = kjfk.Location
	f.nav.FlightState.Heading = 310 // just off runway 31L
	f.nav.Heading = Heading{}

	heading := f.nav.AssignHeading(180, av.TurnLeft, f.simTime, 0)
	intent := f.InterceptRadial("RBV", 45, false)
	AssertUnable(t, intent)

	// The left turn stands; only the intercept was refused.
	if dh := f.nav.DeferredNavHeading; dh == nil || dh.Heading == nil || *dh.Heading != 180 {
		t.Error("the heading assignment was lost along with the refused intercept")
	}
	// The refusal has to be scoped to the intercept; a bare "unable" after the
	// turn reads as refusing the turn itself.
	readback := writtenForTest(t, speech.RenderIntents([]speech.CommandIntent{heading, intent}, f.nav.Rand), f.nav.Rand)
	if want := "turn left 180, unable to intercept the Robbinsville 045 radial"; readback != want {
		t.Errorf("readback %q, want %q", readback, want)
	}

	AssertUnable(t, f.InterceptRadial("RBV", 225, false))
	AssertUnable(t, f.InterceptRadial("RBV", 45, true))

	// A radial the aircraft will actually cross is accepted.
	assertNotUnable(t, f.InterceptRadial("RBV", 120, false))
}

func TestInterceptRadialUnable(t *testing.T) {
	f, direct := radialFlight(t)

	AssertUnable(t, f.InterceptRadial("WAVEY", 0, false))
	AssertUnable(t, f.InterceptRadial("WAVEY", 361, false))
	AssertUnable(t, f.InterceptRadial("NOSUCHFIX", 90, false))

	// A heading parallel to the course never intercepts it.
	f.AssignHeading(int(direct), av.TurnClosest)
	AssertUnable(t, f.InterceptRadial("WAVEY", int(math.OppositeHeading(direct)), false))
}

// Tests for the SID legs that end at a radial (/@NAVAID-R) or track one
// (/t<course><navaid>).

// radialLegFlight sets up an arrival leaving SKORR on the route that the
// given function builds from the magnetic bearing from WAVEY to SKORR, so
// that WAVEY radials can be laid out relative to the aircraft's start. The
// bearing is a whole number of degrees, as the radials in routes are.
func radialLegFlight(t *testing.T, route func(bearing math.MagneticHeading) string) *FlightTest {
	t.Helper()
	bearing := math.MagneticHeading(int(math.OppositeHeading(skorrWaveyCourse(t))))
	return newSkorrWaveyFlight(t, route(bearing))
}

// everyTick runs check on each of the first n ticks. The aircraft never
// passes a fix in these tests, so BeforeFix can't drive the checks.
func everyTick(f *FlightTest, n int, check func(f *FlightTest)) {
	for tick := 1; tick <= n; tick++ {
		f.AfterTicks(tick, check)
	}
}

// groundTrack returns the aircraft's magnetic ground track over the last
// tick, which unlike its heading doesn't include the crab angle in a
// crosswind. prev is updated to the current position.
func groundTrack(f *FlightTest, prev *math.Point2LL) math.MagneticHeading {
	fs := f.nav.FlightState
	track := math.TrueToMagnetic(math.Heading2LL(*prev, fs.Position, fs.NmPerLongitude), fs.MagneticVariation)
	*prev = fs.Position
	return track
}

// checkTracksRadial verifies that the aircraft joins the WAVEY radial by
// tick joinBy at the latest and then stays on it, tracking away from the fix,
// through tick end. It returns the tick at which the aircraft was first
// established on the radial.
func checkTracksRadial(t *testing.T, f *FlightTest, radial math.MagneticHeading, joinBy, end int) int {
	t.Helper()

	joinTick := -1
	var lastDist float32
	prev := f.nav.FlightState.Position
	everyTick(f, end, func(f *FlightTest) {
		offset := math.Abs(courseOffset(f, "WAVEY", radial))
		track := groundTrack(f, &prev)
		if joinTick == -1 {
			if offset < 0.3 && math.HeadingDifference(track, radial) < 5 {
				joinTick, lastDist = f.tick, distanceToFix(f, "WAVEY")
			}
			return
		}
		// A turn onto the radial from a steep angle carries the aircraft a
		// few tenths of a mile through it before it settles.
		if limit := float32(util.Select(f.tick > joinTick+120, 0.3, 0.75)); offset > limit {
			t.Errorf("tick %d: aircraft is %.2fnm off the %03d radial after joining it", f.tick, offset, int(radial))
		}
		if d := distanceToFix(f, "WAVEY"); d < lastDist {
			t.Errorf("tick %d: aircraft is closing on WAVEY; it should track the radial away from it", f.tick)
		} else {
			lastDist = d
		}
	})
	f.Run()

	if joinTick == -1 {
		t.Fatalf("aircraft never joined the %03d radial", int(radial))
	}
	if joinTick > joinBy {
		t.Errorf("aircraft joined the radial at tick %d; expected it by tick %d", joinTick, joinBy)
	}
	return joinTick
}

// A heading flown until crossing a radial, and then the radial tracked
// outbound: the heading leg has to end at the crossing, not before it and
// not once the aircraft is well past it.
func TestRadialTermination(t *testing.T) {
	var heading, radial math.MagneticHeading
	f := radialLegFlight(t, func(bearing math.MagneticHeading) string {
		radial = math.NormalizeHeading(bearing + 20)
		heading = math.NormalizeHeading(bearing + 65) // converges on the radial at 45 degrees
		return fmt.Sprintf("SKORR/h%d/@WAVEY-R%d/tWAVEY-R%d", int(heading), int(radial), int(radial))
	})
	checkRadialTermination(t, f, heading, radial)
}

// declinatedVOR adds a VOR at WAVEY's location to the database for the
// test's duration, with a station declination the given number of degrees
// west of the area's variation, and returns its id.
func declinatedVOR(t *testing.T, degrees float32) string {
	t.Helper()
	const id = "QQQ"
	if _, ok := db.DB.Navaids[id]; ok {
		t.Fatalf("%s is already in the database", id)
	}
	wavey, _ := db.DB.LookupWaypoint("WAVEY")
	variation, err := db.DB.MagneticGrid.Lookup(db.DB.Airports["KJFK"].Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}
	db.DB.Navaids[id] = db.Navaid{Id: id, Type: "VOR", Location: wavey,
		Declination: variation + degrees, HasDeclination: true}
	t.Cleanup(func() { delete(db.DB.Navaids, id) })
	return id
}

// A VOR's radials are referenced to its station declination rather than to
// the area's variation. Declinated 5 degrees west of the area, the station's
// radials read 5 degrees higher than the area's bearings of the same lines,
// so the route here is flown along the same lines as TestRadialTermination's.
func TestRadialTerminationStationDeclination(t *testing.T) {
	vor := declinatedVOR(t, 5)
	var heading, radial math.MagneticHeading
	f := radialLegFlight(t, func(bearing math.MagneticHeading) string {
		radial = math.NormalizeHeading(bearing + 20)
		heading = math.NormalizeHeading(bearing + 65)
		r := int(math.NormalizeHeading(radial + 5))
		return fmt.Sprintf("SKORR/h%d/@%s-R%d/t%s-R%d", int(heading), vor, r, vor, r)
	})
	checkRadialTermination(t, f, heading, radial)
}

// checkRadialTermination verifies that the heading leg ends where the
// aircraft crosses the WAVEY radial, given as the area's bearing, and that
// the aircraft then tracks the radial outbound.
func checkRadialTermination(t *testing.T, f *FlightTest, heading, radial math.MagneticHeading) {
	t.Helper()

	crossTick := -1
	var crossOffset float32
	f.AfterTicks(90, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) != 2 {
			t.Fatalf("expected the aircraft to still be flying the heading at tick 90; maneuvers %v",
				f.nav.Heading.Maneuvers)
		}
		f.AssertHeadingNear(float32(heading), 3)
	})
	everyTick(f, 1200, func(f *FlightTest) {
		if crossTick == -1 && len(f.nav.Heading.Maneuvers) == 1 {
			crossTick, crossOffset = f.tick, math.Abs(courseOffset(f, "WAVEY", radial))
		}
	})
	joinTick := checkTracksRadial(t, f, radial, 1200, 1200)

	if crossTick == -1 {
		t.Fatal("aircraft never crossed the radial")
	}
	if crossOffset > 0.25 {
		t.Errorf("the heading leg ended %.2fnm from the radial", crossOffset)
	}
	if joinTick < crossTick {
		t.Errorf("aircraft was established on the radial at tick %d, before the heading leg ended at tick %d",
			joinTick, crossTick)
	}
}

// A radial extends from its navaid in one direction only: crossing the line
// on the reciprocal's side of the navaid doesn't end the leg.
func TestRadialTerminationIgnoresReciprocal(t *testing.T) {
	f := radialLegFlight(t, func(bearing math.MagneticHeading) string {
		// The aircraft crosses the line of the bearing+20 radial; the radial
		// named is that line's other half.
		radial := math.NormalizeHeading(bearing + 200)
		heading := math.NormalizeHeading(bearing + 65)
		return fmt.Sprintf("SKORR/h%d/@WAVEY-R%d/tWAVEY-R%d", int(heading), int(radial), int(radial))
	})
	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) != 2 {
			t.Errorf("crossing the reciprocal of the radial ended the heading leg; maneuvers %v",
				f.nav.Heading.Maneuvers)
		}
	})
	f.Run()
}

// radialTrackFlight sets up an aircraft at SKORR tracking a WAVEY radial that
// passes well to one side of it, returning the flight, the radial, and the
// aircraft's initial distance from it.
func radialTrackFlight(t *testing.T) (f *FlightTest, radial math.MagneticHeading, offset float32) {
	t.Helper()
	f = radialLegFlight(t, func(bearing math.MagneticHeading) string {
		radial = math.NormalizeHeading(bearing + 20)
		return fmt.Sprintf("SKORR/tWAVEY-R%d", int(radial))
	})
	offset = math.Abs(courseOffset(f, "WAVEY", radial))
	if offset < 3 {
		t.Fatalf("aircraft starts only %.1fnm off the %03d radial; nothing to join", offset, int(radial))
	}
	return
}

// checkJoinsRadial verifies that the aircraft converges on the radial at no
// more than 45 degrees to it and then tracks it.
func checkJoinsRadial(t *testing.T, f *FlightTest, radial math.MagneticHeading, offset float32) {
	t.Helper()

	// At 45 degrees or less to the radial, joining it takes at least as long
	// as flying the initial offset, at roughly 4nm a minute; allow the turn
	// onto the intercept and the rollout onto the radial on top of that.
	joinBy := int(offset/4*60) + 240
	prev := f.nav.FlightState.Position
	everyTick(f, joinBy, func(f *FlightTest) {
		track := groundTrack(f, &prev)
		if f.tick > 60 && math.Abs(courseOffset(f, "WAVEY", radial)) > 2 {
			if diff := math.HeadingDifference(track, radial); diff > 50 {
				t.Errorf("tick %d: aircraft is %.0f degrees off the radial's course while joining it", f.tick, diff)
			}
		}
	})
	checkTracksRadial(t, f, radial, joinBy, joinBy+600)
}

func TestRadialTrackJoin(t *testing.T) {
	f, radial, offset := radialTrackFlight(t)
	checkJoinsRadial(t, f, radial, offset)
}

func TestRadialTrackJoinWithWind(t *testing.T) {
	f, radial, offset := radialTrackFlight(t)
	f.SetWind(float32(math.NormalizeHeading(radial+90)), 40)
	checkJoinsRadial(t, f, radial, offset)
}

// A radial that leads away from the aircraft's side of the navaid is joined
// at the navaid: the aircraft goes there first and then follows it outbound.
func TestRadialTrackFromBehindNavaid(t *testing.T) {
	var radial math.MagneticHeading
	f := radialLegFlight(t, func(bearing math.MagneticHeading) string {
		radial = math.NormalizeHeading(bearing + 210)
		return fmt.Sprintf("SKORR/tWAVEY-R%d", int(radial))
	})

	closest := distanceToFix(f, "WAVEY")
	everyTick(f, 1500, func(f *FlightTest) {
		closest = min(closest, distanceToFix(f, "WAVEY"))
	})
	checkTracksRadial(t, f, radial, 1500, 1500)

	if closest > 0.5 {
		t.Errorf("aircraft came no closer than %.1fnm to WAVEY; it should have gone there to join the radial", closest)
	}
}
