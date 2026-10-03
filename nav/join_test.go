// nav/join_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
)

// joinTestFixes adds fixes east of KJFK to the database for the join tests:
// QQB, QQC, and QQD in a line running east, published as the airway V999,
// and QQS 6nm north of the line and 10nm west of QQB. It returns a function
// giving the location of the point x nm east and y nm north of QQB.
func joinTestFixes(t *testing.T) func(x, y float32) math.Point2LL {
	t.Helper()

	kjfk := db.DB.Airports["KJFK"].Location
	nmPerLong := math.NMPerLongitudeAt(kjfk)
	origin := math.Add2f(math.LL2NM(kjfk, nmPerLong), [2]float32{20, 0})
	at := func(x, y float32) math.Point2LL {
		return math.NM2LL(math.Add2f(origin, [2]float32{x, y}), nmPerLong)
	}

	fixes := map[string]math.Point2LL{"QQS": at(-10, 6), "QQB": at(0, 0), "QQC": at(10, 0), "QQD": at(20, 0)}
	for id, p := range fixes {
		if _, ok := db.DB.LookupWaypoint(id); ok {
			t.Fatalf("%s is already in the database", id)
		}
		db.DB.Navaids[id] = db.Navaid{Id: id, Type: "VOR", Location: p}
	}
	if _, ok := db.DB.Airways["V999"]; ok {
		t.Fatal("V999 is already in the database")
	}
	db.DB.Airways["V999"] = []av.Airway{{Name: "V999", Fixes: []av.AirwayFix{{Fix: "QQB"}, {Fix: "QQC"}, {Fix: "QQD"}}}}
	t.Cleanup(func() {
		for id := range fixes {
			delete(db.DB.Navaids, id)
		}
		delete(db.DB.Airways, "V999")
	})
	return at
}

// joinFlight sets up a KJFK arrival at QQS flying the given route through
// the join test fixes, with the STAR flags set if onSTAR. It returns the
// flight, a function giving the magnetic heading from the aircraft's
// current position to the point x nm east and y nm north of QQB, and the
// magnetic course of the line of fixes.
func joinFlight(t *testing.T, route string, onSTAR bool) (*FlightTest, func(x, y float32) math.MagneticHeading, math.MagneticHeading) {
	t.Helper()

	at := joinTestFixes(t)
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        route,
		DepartureAirport: "KJFK",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
		OnSTAR:           onSTAR,
	})
	fs := &f.nav.FlightState
	headingTo := func(x, y float32) math.MagneticHeading {
		return math.TrueToMagnetic(math.Heading2LL(fs.Position, at(x, y), fs.NmPerLongitude), fs.MagneticVariation)
	}
	// Rounded, as checkJoinsCourse compares it with the maneuver's summary
	// of it; the line it then measures offsets from is a fraction of a
	// degree off, which is nothing over the distances involved.
	course := math.TrueToMagnetic(math.Heading2LL(at(0, 0), at(10, 0), fs.NmPerLongitude), fs.MagneticVariation)
	return f, headingTo, math.MagneticHeading(math.Round(float32(course)))
}

func assertUnable(t *testing.T, intent speech.CommandIntent, want string) {
	t.Helper()
	if u, ok := intent.(speech.UnableIntent); !ok {
		t.Errorf("got %v, want an unable response saying %q", intent, want)
	} else if !strings.Contains(u.Message, want) {
		t.Errorf("got unable %q, want it to say %q", u.Message, want)
	}
}

// Told to join V999 from a heading that crosses the leg from QQB to QQC,
// the aircraft flies the heading until it is time to turn onto the leg,
// then continues along the route from QQC.
func TestJoinAirway(t *testing.T) {
	f, headingTo, course := joinFlight(t, "QQS QQB QQC QQD", false)
	heading := headingTo(5, 0)

	f.AfterTicks(1, func(f *FlightTest) {
		// The pilot hasn't started the turn yet, so the join is computed
		// from the heading they will fly.
		f.AssignHeading(int(heading), av.TurnClosest)
		if f.nav.Heading.Assigned != nil {
			t.Fatal("heading took effect immediately")
		}
		intent := f.nav.JoinAirway("V999", f.simTime, 0)
		assertNotUnable(t, intent)
		if ni, ok := intent.(speech.NavigationIntent); !ok || ni.Type != speech.NavJoinAirway || ni.Airway != "V999" {
			t.Errorf("got %+v, want a readback of joining V999", intent)
		}
		if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 || dh.Join == nil {
			t.Fatal("the join wasn't queued along with the heading")
		}
	})
	// The route is left alone until the aircraft is on the airway.
	f.AfterTicks(60, func(f *FlightTest) {
		if f.nav.Waypoints[0].Fix != "QQB" {
			t.Errorf("route was cut to %s before the join", f.nav.Waypoints[0].Fix)
		}
		if j := f.nav.Heading.Join; j == nil || j.Kind != JoinAirway || j.Name != "V999" {
			t.Errorf("join in progress is %+v, want V999", j)
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {
		if f.nav.Heading.Join != nil || len(f.nav.Heading.Maneuvers) > 0 {
			t.Error("join still in progress after reaching QQC")
		}
		if slices.Contains(f.passed, "QQB") {
			t.Error("aircraft went back to QQB after joining beyond it")
		}
	})
	f.AtFix("QQD", func(f *FlightTest) {})
	checkJoinsCourse(t, f, "QQC", heading, course)
}

func TestJoinAirwayUnable(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		assertUnable(t, f.nav.JoinAirway("V999", f.simTime, 0), "not on a heading")

		// Crossing the line of fixes west of QQB, before the airway.
		f.AssignHeading(int(headingTo(-5, 0)), av.TurnClosest)
		assertUnable(t, f.nav.JoinAirway("V999", f.simTime, 0), "won't intercept")

		// Diverging from it.
		f.AssignHeading(int(headingTo(5, 10)), av.TurnClosest)
		assertUnable(t, f.nav.JoinAirway("V999", f.simTime, 0), "won't intercept")

		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertUnable(t, f.nav.JoinAirway("V998", f.simTime, 0), "isn't a valid airway")
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	})
	f.Run()
}

// A route that has fixes of the airway but doesn't follow it between them
// doesn't have the airway to join.
func TestJoinAirwayNotInRoute(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertUnable(t, f.nav.JoinAirway("V999", f.simTime, 0), "isn't in our route")
	})
	f.Run()
}

// A heading given after the join, without "to join", cancels it and leaves
// the route as it was.
func TestJoinAirwayCancelledByHeading(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	})
	f.AfterTicks(30, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 || f.nav.Heading.Join == nil {
			t.Fatal("join isn't in progress")
		}
		f.AssignHeading(int(headingTo(15, 0)), av.TurnClosest)
	})
	f.AfterTicks(60, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 || f.nav.Heading.Join != nil {
			t.Error("the new heading didn't cancel the join")
		}
		if f.nav.Waypoints[0].Fix != "QQB" {
			t.Errorf("route starts at %s after the cancelled join, want QQB", f.nav.Waypoints[0].Fix)
		}
		assertUnable(t, f.nav.AssignHeadingToJoin(headingTo(15, 0), av.TurnClosest, f.simTime, 0),
			"weren't told to join")
	})
	f.Run()
}

// "Make it a (heading) to join": the join is recomputed from the new
// heading, here one crossing the next leg out.
func TestHeadingToJoin(t *testing.T) {
	f, headingTo, course := joinFlight(t, "QQS QQB QQC QQD", false)
	var heading math.MagneticHeading

	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	})
	f.AfterTicks(30, func(f *FlightTest) {
		// The new heading is a few degrees to the left of the first one.
		heading = headingTo(15, 0)
		intent := f.nav.AssignHeadingToJoin(heading, av.TurnLeft, f.simTime, 0)
		assertNotUnable(t, intent)
		hi, ok := intent.(speech.HeadingIntent)
		if !ok || hi.Heading != heading || hi.Turn != speech.HeadingTurnToLeft || hi.Join == nil ||
			hi.Join.Type != speech.NavJoinAirway || hi.Join.Airway != "V999" {
			t.Errorf("got %+v, want a readback of turning left to %03d to join V999", intent, int(heading))
		}
	})
	f.AfterTicks(90, func(f *FlightTest) {
		f.AssertHeadingNear(float32(heading), 2)
		want := fmt.Sprintf("fly heading %03d until intercept %03d course to QQD", int(heading), int(course))
		if len(f.nav.Heading.Maneuvers) == 0 {
			t.Fatal("no join in progress")
		} else if got := f.nav.Heading.Maneuvers[0].String(); got != want {
			t.Errorf("maneuver summary is %q, want %q", got, want)
		}
	})
	f.AtFix("QQD", func(f *FlightTest) {
		if slices.Contains(f.passed, "QQC") {
			t.Error("aircraft went back to QQC after joining beyond it")
		}
	})
	f.Run()
}

// Vectored off its STAR a couple of miles along the first leg and then told
// to resume it from a heading that crosses the leg from QQB to QQC.
func TestResumeSTAR(t *testing.T) {
	f, headingTo, course := joinFlight(t, "QQS QQB QQC QQD", true)
	heading := headingTo(5, 0)

	f.AfterTicks(30, func(f *FlightTest) {
		assertUnable(t, f.nav.ResumeSTAR("", f.simTime, 0), "not on a STAR")

		f.AssignHeading(int(heading), av.TurnClosest)
		intent := f.nav.ResumeSTAR("CAMRN4", f.simTime, 0)
		assertNotUnable(t, intent)
		if ni, ok := intent.(speech.NavigationIntent); !ok || ni.Type != speech.NavResumeSTAR || ni.Procedure != "CAMRN4" {
			t.Errorf("got %+v, want a readback of resuming the CAMRN4", intent)
		}
	})
	f.AtFix("QQD", func(f *FlightTest) {})
	checkJoinsCourse(t, f, "QQC", heading, course)
}

// A departure's SID is joined the same way; a route with none of it has
// nothing to resume.
func TestResumeSID(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertUnable(t, f.nav.ResumeSID("", f.simTime, 0), "not on a SID")
		assertUnable(t, f.nav.ResumeSID("SKORR5", f.simTime, 0), "isn't in our route")

		for i := range f.nav.Waypoints {
			f.nav.Waypoints[i].SetOnSID(true)
		}
		intent := f.nav.ResumeSID("SKORR5", f.simTime, 0)
		assertNotUnable(t, intent)
		if ni, ok := intent.(speech.NavigationIntent); !ok || ni.Type != speech.NavResumeSID || ni.Procedure != "SKORR5" {
			t.Errorf("got %+v, want a readback of resuming the SKORR5", intent)
		}
		if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 || dh.Maneuvers[0].Until.InterceptFix != "QQC" {
			t.Errorf("queued maneuvers %+v, want an intercept of the course to QQC", dh)
		}
	})
	f.Run()
}

// A departure sent direct to its exit has no leg behind it to rejoin: the
// fix it last passed and the exit are both on the SID but aren't a leg of
// it.
func TestDepartOnCourseClearsLegOrigin(t *testing.T) {
	f := newDepartureOnSID(t, ArrivalConfig{InitialAltitude: 2500, ClearedAltitude: 5000})
	f.StepUntil("passed TTAPS", func() bool { return f.nav.PassedWaypoint.Fix == "TTAPS" })
	f.AssignHeading(int(math.NormalizeHeading(f.nav.FlightState.Heading+30)), av.TurnClosest)
	f.Step(30)

	f.nav.DepartOnCourse(35000, "LLA", f.simTime)
	if f.nav.PassedWaypoint.Fix != "" {
		t.Errorf("leg origin is %q after departing on course, want none", f.nav.PassedWaypoint.Fix)
	}
	f.Step(30)
	if f.nav.Waypoints[0].Fix != "LLA" {
		t.Fatalf("route starts at %s, want LLA", f.nav.Waypoints[0].Fix)
	}

	f.AssignHeading(int(math.NormalizeHeading(f.nav.FlightState.Heading+30)), av.TurnClosest)
	assertUnable(t, f.nav.ResumeSID("TEST1", f.simTime, 0), "isn't in our route")
}

// A radial intercept leaves the route alone until the aircraft is on the
// radial, so that a vector before then doesn't leave it cut down.
func TestInterceptRadialKeepsRouteUntilJoined(t *testing.T) {
	direct := skorrWaveyCourse(t)
	f := newSkorrWaveyFlight(t, "SKORR CAMRN WAVEY SHIPP")

	heading := math.NormalizeHeading(direct - 20)
	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		assertNotUnable(t, f.InterceptRadial("WAVEY", int(radial), false))
	})
	f.AfterTicks(60, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			t.Fatal("no intercept in progress")
		}
		if f.nav.Waypoints[0].Fix == "WAVEY" {
			t.Error("route was cut to WAVEY before the intercept")
		}
	})
	f.AtFix("WAVEY", func(f *FlightTest) {
		if slices.Contains(f.passed, "CAMRN") {
			t.Error("aircraft went to CAMRN rather than joining the radial to WAVEY")
		}
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != "SHIPP" {
			t.Errorf("after WAVEY the route is %q, want SHIPP next", f.nav.Waypoints.Encode())
		}
	})
	f.Run()
}

// Vectored off the leg it was flying after passing QQB, the aircraft can be
// turned back to join that leg short of QQC even though QQB is no longer in
// its route.
func TestJoinAirwayAfterPassingFix(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)

	vectoredAt := -1
	f.AtFix("QQB", func(f *FlightTest) {
		// Off to the left of the leg to QQC.
		f.AssignHeading(int(headingTo(5, 3)), av.TurnClosest)
		vectoredAt = f.tick
	})
	f.BeforeFix("QQC", func(f *FlightTest) {
		if vectoredAt < 0 || f.tick != vectoredAt+90 {
			return
		}
		if f.nav.PassedWaypoint.Fix != "QQB" {
			t.Fatalf("passed waypoint is %q, want QQB", f.nav.PassedWaypoint.Fix)
		}
		// Back toward the leg, crossing it a mile short of QQC.
		f.AssignHeading(int(headingTo(9, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
		if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 ||
			dh.Maneuvers[0].Until.InterceptFix != "QQC" {
			t.Fatalf("queued maneuvers %+v, want an intercept of the leg to QQC", dh)
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != "QQD" {
			t.Errorf("after QQC the route is %q, want QQD next", f.nav.Waypoints.Encode())
		}
	})
	f.Run()
}

// A crossing restriction issued while a join is pending is kept: the
// aircraft takes up the route as it is when it joins, not as it was when it
// was told to.
func TestJoinKeepsCrossingIssuedDuringJoin(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	})

	// One crossing on the leg being joined, ahead of where the aircraft
	// joins it, and one on the leg after.
	var crossings []string
	f.AfterTicks(30, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			t.Fatal("join isn't in progress")
		}
		ar := av.MakeAtAltitudeRestriction(9000)
		assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQC", 3, math.West, &ar, nil, f.temp()))
		ar = av.MakeAtAltitudeRestriction(8000)
		assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQD", 2, math.West, &ar, nil, f.temp()))
		for _, wp := range f.nav.Waypoints {
			if wp.SyntheticCrossing() {
				crossings = append(crossings, wp.Fix)
			}
		}
		if len(crossings) != 2 {
			t.Fatalf("crossings added to the route: %v, want two", crossings)
		}
	})
	// Once the join completes, the route starts with the crossing on the
	// joined leg.
	joining := false
	f.BeforeFix("QQC", func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			joining = true
		} else if joining {
			if first := f.nav.Waypoints[0].Fix; first != crossings[0] {
				t.Fatalf("after joining, the route starts at %s, want %s", first, crossings[0])
			}
			joining = false
		}
	})
	f.AtFix("QQD", func(f *FlightTest) {
		for _, crossing := range crossings {
			if !slices.Contains(f.passed, crossing) {
				t.Errorf("aircraft never crossed %s; it passed %v", crossing, f.passed)
			}
		}
		f.AssertAltitudeNear(8000, 300)
	})
	f.Run()
}

// A crossing point added on an airway leg doesn't break the leg up: it is
// still joinable, and the crossing is kept if the aircraft joins short of
// it and dropped if it joins beyond it.
func TestJoinAirwayLegWithCrossing(t *testing.T) {
	for _, tc := range []struct {
		name          string
		x             float32 // where the heading crosses the leg, nm east of QQB
		keepsCrossing bool
	}{
		{"joins short of the crossing", 4, true},
		{"joins beyond the crossing", 8.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)

			// Three miles west of QQC, on the leg from QQB.
			ar := av.MakeAtAltitudeRestriction(8000)
			assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQC", 3, math.West, &ar, nil, f.temp()))
			i := slices.IndexFunc(f.nav.Waypoints, av.Waypoint.SyntheticCrossing)
			if i == -1 {
				t.Fatal("the crossing wasn't added to the route")
			}
			crossing := f.nav.Waypoints[i].Fix

			f.AssignHeading(int(headingTo(tc.x, 0)), av.TurnClosest)
			assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))

			// The first waypoint of the route once the join completes.
			joining, first := false, ""
			f.BeforeFix("QQC", func(f *FlightTest) {
				if len(f.nav.Heading.Maneuvers) > 0 {
					joining = true
				} else if joining && first == "" {
					first = f.nav.Waypoints[0].Fix
				}
			})
			f.AtFix("QQC", func(f *FlightTest) {})
			f.Run()

			want := "QQC"
			if tc.keepsCrossing {
				want = crossing
			}
			if first != want {
				t.Errorf("after joining, the route started at %s, want %s", first, want)
			}
			if crossed := slices.Contains(f.passed, crossing); crossed != tc.keepsCrossing {
				t.Errorf("crossed %s: %v, want %v", crossing, crossed, tc.keepsCrossing)
			}
		})
	}
}

// A radial intercept to a fix outside the route replaces the route along
// with the heading. A new heading given to make the same intercept before
// the pilot has turned keeps that pending route.
func TestInterceptRadialPendingRouteSurvivesHeadingToJoin(t *testing.T) {
	direct := skorrWaveyCourse(t)
	f := newSkorrWaveyFlight(t, "SKORR SHIPP")

	heading := math.NormalizeHeading(direct - 20)
	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		assertNotUnable(t, f.InterceptRadial("WAVEY", int(radial), false))
		if !f.nav.hasDeferredRoute() {
			t.Fatal("no route is pending for the intercept of a fix outside the route")
		}
		assertNotUnable(t, f.nav.AssignHeadingToJoin(math.NormalizeHeading(heading+5), av.TurnClosest, f.simTime, 0))
		if !f.nav.hasDeferredRoute() {
			t.Fatal("the new heading dropped the pending route")
		}
	})
	f.AtFix("WAVEY", func(f *FlightTest) {
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != "KJFK" {
			t.Errorf("after WAVEY the route is %q, want KJFK next", f.nav.Waypoints.Encode())
		}
	})
	f.Run()
}

// The join is anchored to the route fix at the end of the leg rather than
// to a crossing point on it, which a later restriction may replace.
func TestJoinSurvivesCrossingReplacement(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)

	ar := av.MakeAtAltitudeRestriction(8000)
	assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQC", 3, math.West, &ar, nil, f.temp()))
	// Crossing the leg short of the crossing point, so the intercept is of
	// the segment that ends at it.
	f.AssignHeading(int(headingTo(4, 0)), av.TurnClosest)
	assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 || dh.Maneuvers[0].ResumeFix != "QQC" {
		t.Fatalf("queued maneuvers %+v, want the join anchored to QQC", dh)
	}

	var replacement string
	f.AfterTicks(30, func(f *FlightTest) {
		ar := av.MakeAtAltitudeRestriction(7000)
		assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQC", 2, math.West, &ar, nil, f.temp()))
		i := slices.IndexFunc(f.nav.Waypoints, av.Waypoint.SyntheticCrossing)
		if i == -1 {
			t.Fatal("the replacement crossing wasn't added to the route")
		}
		replacement = f.nav.Waypoints[i].Fix
	})
	joining := false
	f.BeforeFix("QQC", func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			joining = true
		} else if joining {
			if first := f.nav.Waypoints[0].Fix; first != replacement {
				t.Fatalf("after joining, the route starts at %s, want %s", first, replacement)
			}
			joining = false
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {
		if slices.Contains(f.passed, "QQB") {
			t.Error("aircraft went back to QQB after joining beyond it")
		}
		if !slices.Contains(f.passed, replacement) {
			t.Errorf("aircraft never crossed %s; it passed %v", replacement, f.passed)
		}
	})
	f.Run()
}

// A crossing point on the route's leg into the fix isn't on a radial that
// comes in from another direction, so intercepting the radial drops it
// rather than turning to it afterward.
func TestInterceptRadialDropsCrossingOffTheRadial(t *testing.T) {
	f, direct := radialFlight(t)

	// Three miles short of WAVEY on the leg from SKORR.
	skorr, _ := db.DB.LookupWaypoint("SKORR")
	wavey, _ := db.DB.LookupWaypoint("WAVEY")
	fs := &f.nav.FlightState
	inbound := math.TrueToMagnetic(math.Heading2LL(wavey, skorr, fs.NmPerLongitude), fs.MagneticVariation)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(inbound))
	if err != nil {
		t.Fatal(err)
	}
	ar := av.MakeAtAltitudeRestriction(8000)
	assertNotUnable(t, f.nav.CrossDistanceFromFixAt("WAVEY", 3, dir, &ar, nil, f.temp()))
	i := slices.IndexFunc(f.nav.Waypoints, av.Waypoint.SyntheticCrossing)
	if i == -1 {
		t.Fatal("the crossing wasn't added to the route")
	}
	crossing := f.nav.Waypoints[i].Fix

	heading := math.NormalizeHeading(direct - 20)
	radial := math.OppositeHeading(math.NormalizeHeading(direct + 20))
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(heading), av.TurnClosest)
		assertNotUnable(t, f.InterceptRadial("WAVEY", int(radial), false))
	})
	joining := false
	f.BeforeFix("WAVEY", func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			joining = true
		} else if joining {
			if first := f.nav.Waypoints[0].Fix; first != "WAVEY" {
				t.Fatalf("after intercepting the radial, the route starts at %s, want WAVEY", first)
			}
			joining = false
		}
	})
	f.AtFix("WAVEY", func(f *FlightTest) {
		if slices.Contains(f.passed, crossing) {
			t.Errorf("aircraft flew to %s, which is off the radial", crossing)
		}
	})
	f.Run()
}

// Having joined the leg from QQB to QQC partway along, the aircraft can be
// vectored off it and told to join it again, though QQB was never in its
// route from then on.
func TestJoinAirwayLegAgainAfterJoining(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	f.AfterTicks(1, func(f *FlightTest) {
		f.AssignHeading(int(headingTo(2, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
	})

	joining, joinedAt := false, -1
	f.BeforeFix("QQC", func(f *FlightTest) {
		if joinedAt == -1 {
			if len(f.nav.Heading.Maneuvers) > 0 {
				joining = true
			} else if joining {
				joinedAt = f.tick
				if f.nav.PassedWaypoint.Fix != "QQB" {
					t.Fatalf("passed waypoint after joining is %q, want QQB", f.nav.PassedWaypoint.Fix)
				}
			}
			return
		}
		switch f.tick {
		case joinedAt + 20:
			// Off to the left of the leg again.
			f.AssignHeading(int(headingTo(6, 3)), av.TurnClosest)
		case joinedAt + 60:
			// And back onto it, short of QQC.
			f.AssignHeading(int(headingTo(8.5, 0)), av.TurnClosest)
			assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
			if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 ||
				dh.Maneuvers[0].Until.InterceptFix != "QQC" {
				t.Fatalf("queued maneuvers %+v, want an intercept of the leg to QQC", dh)
			}
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {})
	f.AtFix("QQD", func(f *FlightTest) {})
	f.Run()
}

// Passing a crossing point on the leg from QQB to QQC doesn't make it the
// leg's origin: vectored off after it, the aircraft can still rejoin the
// leg.
func TestJoinAirwayLegAfterPassingCrossing(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)

	ar := av.MakeAtAltitudeRestriction(8000)
	assertNotUnable(t, f.nav.CrossDistanceFromFixAt("QQC", 5, math.West, &ar, nil, f.temp()))
	i := slices.IndexFunc(f.nav.Waypoints, av.Waypoint.SyntheticCrossing)
	if i == -1 {
		t.Fatal("the crossing wasn't added to the route")
	}
	crossing := f.nav.Waypoints[i].Fix

	crossedAt := -1
	f.AtFix(crossing, func(f *FlightTest) {
		if f.nav.PassedWaypoint.Fix != "QQB" {
			t.Fatalf("passed waypoint after the crossing is %q, want QQB", f.nav.PassedWaypoint.Fix)
		}
		// Off to the left of the leg.
		f.AssignHeading(int(headingTo(7, 3)), av.TurnClosest)
		crossedAt = f.tick
	})
	f.BeforeFix("QQC", func(f *FlightTest) {
		if crossedAt < 0 || f.tick != crossedAt+40 {
			return
		}
		// Back onto the leg, a mile short of QQC.
		f.AssignHeading(int(headingTo(9, 0)), av.TurnClosest)
		assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
		if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 ||
			dh.Maneuvers[0].Until.InterceptFix != "QQC" {
			t.Fatalf("queued maneuvers %+v, want an intercept of the leg to QQC", dh)
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {})
	f.Run()
}

func TestViaClearancePreservesJoin(t *testing.T) {
	for _, sid := range []bool{false, true} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("SID=%v/active=%v", sid, active), func(t *testing.T) {
				route := "QQS QQB/a8000 QQC/a6000 QQD/a4000"
				if sid {
					route = "QQS QQB/a10000 QQC/a12000 QQD/a16000"
				}
				f, headingTo, course := joinFlight(t, route, !sid)
				if sid {
					f.nav.FinalAltitude = 20000
					for i := range f.nav.Waypoints {
						f.nav.Waypoints[i].SetOnSID(true)
					}
				}
				heading := headingTo(2, 0)
				f.AfterTicks(1, func(f *FlightTest) {
					f.AssignHeading(int(heading), av.TurnClosest)
					if sid {
						assertNotUnable(t, f.nav.ResumeSID("TEST1", f.simTime, 0))
					} else {
						assertNotUnable(t, f.nav.ResumeSTAR("TEST1", f.simTime, 0))
					}
				})
				viaTick := 1
				if active {
					viaTick = 30
				}
				f.AfterTicks(viaTick, func(f *FlightTest) {
					if active && f.nav.Heading.Join == nil {
						t.Fatal("join has not taken effect")
					}
					if sid {
						assertNotUnable(t, f.nav.ClimbViaSID(nil, f.simTime, 0))
					} else {
						assertNotUnable(t, f.nav.DescendViaSTAR(nil, f.simTime, 0))
					}
					if f.nav.pendingJoin() == nil {
						t.Fatal("via clearance cancelled the join")
					}
					if dh := f.nav.DeferredNavHeading; dh != nil && dh.SnapshotAltitudeOnEffect {
						t.Error("via clearance kept the pending altitude floor capture")
					}
				})
				f.AtFix("QQC", func(f *FlightTest) {
					if slices.Contains(f.passed, "QQB") {
						t.Error("aircraft went back to QQB instead of intercepting the leg")
					}
					if sid {
						if f.nav.FlightState.Altitude <= 11000 {
							t.Error("aircraft did not resume climbing via the SID")
						}
					} else if f.nav.Altitude.Cleared != nil || f.nav.FlightState.Altitude >= 9000 {
						t.Error("aircraft did not resume descending via the STAR")
					}
				})
				checkJoinsCourse(t, f, "QQC", heading, course)
			})
		}
	}
}

func TestJoinPreservesPendingAltitudeCapture(t *testing.T) {
	for _, newHeading := range []bool{false, true} {
		t.Run(fmt.Sprintf("newHeading=%v", newHeading), func(t *testing.T) {
			f, headingTo, _ := joinFlight(t, "QQS QQB/a8000 QQC/a6000 QQD/a4000", true)
			f.Step(30)
			f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
			if dh := f.nav.DeferredNavHeading; dh == nil || !dh.SnapshotAltitudeOnEffect {
				t.Fatal("heading did not schedule altitude capture")
			}
			assertNotUnable(t, f.nav.ResumeSTAR("TEST1", f.simTime, 0))
			if newHeading {
				assertNotUnable(t, f.nav.AssignHeadingToJoin(headingTo(6, 0), av.TurnClosest, f.simTime, 0))
			}
			if !f.nav.DeferredNavHeading.SnapshotAltitudeOnEffect {
				t.Fatal("join dropped the pending altitude capture")
			}
			if f.nav.Altitude.Cleared != nil {
				t.Fatal("altitude captured before the heading took effect")
			}
			for f.nav.DeferredNavHeading != nil {
				f.Step(1)
			}
			cleared := f.nav.Altitude.Cleared
			if cleared == nil || !cleared.IsFloor {
				t.Fatal("heading took effect without capturing an altitude floor")
			}
			if cleared.Altitude != f.nav.FlightState.Altitude {
				t.Errorf("captured altitude %.0f, want %.0f", cleared.Altitude, f.nav.FlightState.Altitude)
			}
			f.AtFix("QQC", func(f *FlightTest) {
				f.AssertAltitudeNear(cleared.Altitude, 100)
			})
			f.Run()
		})
	}
}

// A /dvs route action at a fix passed while a heading is pending is the
// earlier controller's clearance, which the vector is off of, so unlike a
// via clearance issued during a join it doesn't cancel the altitude floor
// the heading captures when it takes effect.
func TestRouteViaActionKeepsPendingAltitudeCapture(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB/a8000 QQC/a6000 QQD/a4000", true)
	f.Step(30)
	f.AssignHeading(int(headingTo(5, 0)), av.TurnClosest)
	if dh := f.nav.DeferredNavHeading; dh == nil || !dh.SnapshotAltitudeOnEffect {
		t.Fatal("heading did not schedule altitude capture")
	}
	if !f.nav.DescendViaSTARAtPassedFix(nil) {
		t.Fatal("the descend via route action did not apply")
	}
	if !f.nav.DeferredNavHeading.SnapshotAltitudeOnEffect {
		t.Fatal("the route action dropped the pending altitude capture")
	}
	for f.nav.DeferredNavHeading != nil {
		f.Step(1)
	}
	if cleared := f.nav.Altitude.Cleared; cleared == nil || !cleared.IsFloor {
		t.Fatal("heading took effect without capturing an altitude floor")
	}
}

func TestJoinAirwayAfterDirectRollback(t *testing.T) {
	f, headingTo, _ := joinFlight(t, "QQS QQB QQC QQD", false)
	vectoredAt := -1
	var snap Snapshot
	f.AtFix("QQB", func(f *FlightTest) {
		f.AssignHeading(int(headingTo(5, 3)), av.TurnClosest)
		vectoredAt = f.tick
	})
	f.BeforeFix("QQC", func(f *FlightTest) {
		if vectoredAt < 0 {
			return
		}
		switch f.tick {
		case vectoredAt + 30:
			snap = f.nav.TakeSnapshot()
			assertNotUnable(t, f.nav.DirectFix("QQD", av.TurnClosest, f.simTime, 0))
		case vectoredAt + 60:
			if f.nav.DeferredNavHeading != nil || f.nav.PassedWaypoint.Fix != "" {
				t.Fatal("direct instruction has not taken effect and cleared the leg origin")
			}
			f.nav.RestoreSnapshot(snap)
			if f.nav.PassedWaypoint.Fix != "QQB" {
				t.Errorf("restored leg origin is %q, want QQB", f.nav.PassedWaypoint.Fix)
			}
			f.AssignHeading(int(headingTo(9, 0)), av.TurnClosest)
			assertNotUnable(t, f.nav.JoinAirway("V999", f.simTime, 0))
			if dh := f.nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 ||
				dh.Maneuvers[0].Until.InterceptFix != "QQC" {
				t.Fatalf("queued maneuvers %+v, want an intercept of the leg to QQC", dh)
			}
		}
	})
	f.AtFix("QQC", func(f *FlightTest) {})
	f.Run()
}
