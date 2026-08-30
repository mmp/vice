// nav/distanceleg_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// Tests for the /@d trigger: a heading or track flown for a distance.

// A /@d trigger ends its action once the aircraft is the given distance from
// where the action took effect. With nothing after it, the aircraft then
// goes direct to the next fix, as a SID's track from a fix for a distance
// does.
func TestDistanceFlownTrigger(t *testing.T) {
	heading := math.NormalizeHeading(skorrWaveyCourse(t) - 40)
	f := newSkorrWaveyFlight(t, fmt.Sprintf("SKORR/h%d/@d5.0 WAVEY", int(heading)))

	var start math.Point2LL
	startTick, endTick := -1, -1
	f.BeforeFix("WAVEY", func(f *FlightTest) {
		if m := f.nav.Heading.Maneuvers; len(m) > 0 {
			if startTick == -1 {
				start, startTick = f.nav.FlightState.Position, f.tick
				want := fmt.Sprintf("fly heading %03d for 5.0nm", int(heading))
				if got := m[0].String(); got != want {
					t.Errorf("maneuver summary is %q, want %q", got, want)
				}
			}
			if f.tick > startTick+30 { // once the turn onto the heading is done
				f.AssertHeadingNear(float32(heading), 2)
			}
		} else if startTick != -1 && endTick == -1 {
			endTick = f.tick
			if d := math.NMDistance2LL(start, f.nav.FlightState.Position); math.Abs(d-5) > 0.25 {
				t.Errorf("tick %d: the heading ended %.2fnm from where it began; expected 5nm", f.tick, d)
			}
		}
	})
	f.AtFix("WAVEY", func(f *FlightTest) {})
	f.Run()

	if endTick == -1 {
		t.Fatal("the heading was never flown for its distance")
	}
}

// KPBF ILS 18's PBF transition as the CIFP codes it: a 338 track from NETAA
// for 7.9nm, then a left turn to 208 to intercept the 178 localizer course
// to REYLO. The reversal is part of the transition's path rather than a
// procedure turn, so it is flown as the route says, with no approach
// clearance involved. The speed is one a jet actually flies it at: at 250
// knots the turn radius is such that a continuous left turn from 338
// reaches the localizer, and the intercept fires before 208 is ever flown.
func TestCourseReversalLegs(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "PBF/s180 NETAA/t338/@d7.9/lt208/@crs178 REYLO TUKER",
		DepartureAirport: "KJFK",
		ArrivalAirport:   "KPBF",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     200,
	})

	netaa, ok := av.DB.LookupWaypoint("NETAA")
	if !ok {
		t.Fatal("NETAA not found")
	}

	var summaries []string
	netaaTick, reverseTick, joinTick := -1, -1, -1
	f.AtFix("NETAA", func(f *FlightTest) { netaaTick = f.tick })
	f.BetweenFixes("NETAA", "REYLO", func(f *FlightTest) {
		m := f.nav.Heading.Maneuvers
		if len(m) > 0 {
			if s := m[0].String(); len(summaries) == 0 || summaries[len(summaries)-1] != s {
				summaries = append(summaries, s)
			}
		}
		switch {
		case len(m) == 2: // the outbound track
			if f.tick > netaaTick+30 {
				f.AssertHeadingNear(338, 2)
			}
		case len(m) == 1: // the turn back to intercept the localizer
			if reverseTick == -1 {
				reverseTick = f.tick
				if d := math.NMDistance2LL(netaa, f.nav.FlightState.Position); math.Abs(d-7.9) > 0.5 {
					t.Errorf("tick %d: reversed course %.2fnm from NETAA; expected 7.9nm", f.tick, d)
				}
			} else if f.tick > reverseTick+60 {
				f.AssertHeadingNear(208, 2)
			}
		case reverseTick != -1: // joined the course to REYLO
			if joinTick == -1 {
				joinTick = f.tick
			} else if f.tick > joinTick+60 {
				if offset := math.Abs(courseOffset(f, "REYLO", 178)); offset > 0.5 {
					t.Errorf("tick %d: aircraft is %.2fnm off the 178 course after joining it", f.tick, offset)
				}
			}
		}
	})
	f.AtFix("REYLO", func(f *FlightTest) { f.AssertHeadingNear(178, 5) })
	f.AtFix("TUKER", func(f *FlightTest) {})
	f.Run()

	want := []string{"fly track 338 for 7.9nm", "fly track 208 until intercept 178 course to REYLO"}
	if !slices.Equal(summaries, want) {
		t.Errorf("maneuvers flown were %q, want %q", summaries, want)
	}
	if joinTick == -1 {
		t.Fatal("aircraft never joined the 178 course to REYLO")
	}
}
