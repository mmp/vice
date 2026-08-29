// nav/radial_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// radialFlight sets up an arrival at SKORR heading direct to WAVEY, along
// with the magnetic course between the two.
func radialFlight(t *testing.T) (*FlightTest, math.MagneticHeading) {
	t.Helper()
	direct := skorrWaveyCourse(t)
	return newSkorrWaveyFlight(t, "SKORR WAVEY SHIPP"), direct
}

func distanceToFix(f *FlightTest, fix string) float32 {
	p, _ := av.DB.LookupWaypoint(fix)
	return math.NMDistance2LL(f.nav.FlightState.Position, p)
}

func assertNotUnable(t *testing.T, intent av.CommandIntent) {
	t.Helper()
	if intent == nil {
		t.Fatal("no intent returned")
	} else if _, unable := intent.(av.UnableIntent); unable {
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
	f.nav.Heading = NavHeading{Assigned: &hdg}

	const radial = 100
	course := math.OppositeHeading(math.MagneticHeading(radial))
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

	kjfk := av.DB.Airports["KJFK"]
	f.nav.FlightState.Position = kjfk.Location
	f.nav.FlightState.Heading = 310 // just off runway 31L
	f.nav.Heading = NavHeading{}
	rbv, ok := av.DB.LookupWaypoint("RBV")
	if !ok {
		t.Fatal("RBV not found")
	}
	f.nav.Waypoints = []av.Waypoint{{Fix: "RBV", Location: rbv}}

	f.nav.AssignHeading(180, av.TurnLeft, f.simTime, 0)
	assertNotUnable(t, f.InterceptRadial("RBV", 65, false))

	course := math.OppositeHeading(math.MagneticHeading(65))
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

	kjfk := av.DB.Airports["KJFK"]
	f.nav.FlightState.Position = kjfk.Location
	f.nav.FlightState.Heading = 310 // just off runway 31L
	f.nav.Heading = NavHeading{}

	heading := f.nav.AssignHeading(180, av.TurnLeft, f.simTime, 0)
	intent := f.InterceptRadial("RBV", 45, false)
	AssertUnable(t, intent)

	// The left turn stands; only the intercept was refused.
	if dh := f.nav.DeferredNavHeading; dh == nil || dh.Heading == nil || *dh.Heading != 180 {
		t.Error("the heading assignment was lost along with the refused intercept")
	}
	// The refusal has to be scoped to the intercept; a bare "unable" after the
	// turn reads as refusing the turn itself.
	readback := av.RenderIntents([]av.CommandIntent{heading, intent}, f.nav.Rand).Written(f.nav.Rand)
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
