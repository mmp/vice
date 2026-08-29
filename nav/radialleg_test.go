// nav/radialleg_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

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
