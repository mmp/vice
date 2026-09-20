// nav/procedures_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

func TestManeuverCompleteUntilAltitude(t *testing.T) {
	for _, tc := range []struct {
		name         string
		atOrAbove    bool
		notYet, done float32
	}{
		{"at or above", true, 499, 500},
		{"at or below", false, 2501, 2500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nav := &Nav{}
			mc := ManeuverComplete{Type: UntilAltitude, Altitude: int(tc.done), AtOrAbove: tc.atOrAbove}

			nav.FlightState.Altitude = tc.notYet
			if mc.Done(nav, Time{}, wx.Sample{}, 0) {
				t.Fatalf("expected altitude completion to wait at %.0f", tc.notYet)
			}
			nav.FlightState.Altitude = tc.done
			if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
				t.Fatalf("expected altitude completion at %.0f", tc.done)
			}
		})
	}
}

func TestManeuverCompleteUntilDME(t *testing.T) {
	const nmPerLongitude = 45
	dmeFix := math.Point2LL{-74, 40}
	at := func(nm float32) math.Point2LL { return math.Offset2LL(dmeFix, 90, nm, nmPerLongitude) }

	// The test's offsets are a little short of DMEDistance's slant range at
	// the true nm per degree of longitude, so the cases keep clear of 4nm.
	for _, tc := range []struct {
		name         string
		atOrAbove    bool
		notYet, done float32
	}{
		{"at or beyond", true, 3.5, 4.3},
		{"within", false, 4.5, 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nav := &Nav{FlightState: FlightState{Altitude: 3000}}
			mc := ManeuverComplete{
				Type:            UntilDME,
				DMEDistance:     4,
				DMEFix:          dmeFix,
				DMEFixElevation: 33,
				AtOrAbove:       tc.atOrAbove,
			}

			nav.FlightState.Position = at(tc.notYet)
			if mc.Done(nav, Time{}, wx.Sample{}, 0) {
				t.Fatalf("expected DME completion to wait at %.1fnm", tc.notYet)
			}
			nav.FlightState.Position = at(tc.done)
			if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
				t.Fatalf("expected DME completion at %.1fnm", tc.done)
			}
		})
	}
}

func TestRacetrackEntryManeuvers(t *testing.T) {
	fix := math.Point2LL{-75.109550, 40.880634}
	entryLeg := func(track math.MagneticHeading) LateralManeuver {
		return flyTrackForTime(track, 70)
	}

	for _, tc := range []struct {
		name    string
		entry   av.HoldEntry
		inbound math.MagneticHeading
		turn    av.TurnDirection
		want    []LateralManeuver
	}{
		{
			name:    "direct",
			entry:   av.HoldEntryDirect,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
			},
		},
		{
			name:    "right parallel",
			entry:   av.HoldEntryParallel,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(270, av.TurnClosest),
				flyTrackForTime(270, 70),
				turnToTrack(50, av.TurnLeft),
				flyTrackUntilIntercept(50, av.TurnRight, fix, 90),
				turnToTrack(90, av.TurnRight),
				flyTowardFix(fix),
			},
		},
		{
			name:    "right teardrop",
			entry:   av.HoldEntryTeardrop,
			inbound: 90,
			turn:    av.TurnRight,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(240, av.TurnClosest),
				flyTrackForTime(240, 70),
				turnToTrack(60, av.TurnRight),
				flyTrackUntilIntercept(60, av.TurnRight, fix, 90),
				turnToTrack(90, av.TurnRight),
				flyTowardFix(fix),
			},
		},
		{
			name:    "left parallel",
			entry:   av.HoldEntryParallel,
			inbound: 90,
			turn:    av.TurnLeft,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(270, av.TurnClosest),
				flyTrackForTime(270, 70),
				turnToTrack(130, av.TurnRight),
				flyTrackUntilIntercept(130, av.TurnLeft, fix, 90),
				turnToTrack(90, av.TurnLeft),
				flyTowardFix(fix),
			},
		},
		{
			name:    "left teardrop",
			entry:   av.HoldEntryTeardrop,
			inbound: 90,
			turn:    av.TurnLeft,
			want: []LateralManeuver{
				flyTowardFix(fix),
				turnToTrack(300, av.TurnClosest),
				flyTrackForTime(300, 70),
				turnToTrack(120, av.TurnLeft),
				flyTrackUntilIntercept(120, av.TurnLeft, fix, 90),
				turnToTrack(90, av.TurnLeft),
				flyTowardFix(fix),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := racetrackEntryManeuvers(tc.entry, fix, tc.inbound, tc.turn, entryLeg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unexpected maneuvers:\ngot:  %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

func TestManeuverCompleteUntilRadial(t *testing.T) {
	const nmPerLongitude = 45
	fix := math.Point2LL{-74, 40}
	nav := &Nav{FlightState: FlightState{NmPerLongitude: nmPerLongitude}}

	// With no magnetic variation the 090 radial runs due east from the fix.
	at := func(bearing math.TrueHeading) {
		nav.FlightState.Position = math.Offset2LL(fix, bearing, 3, nmPerLongitude)
	}
	mc := ManeuverComplete{Type: UntilRadial, Fix: fix, Radial: 90}

	at(45) // north of the radial
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion to wait before crossing")
	}
	at(135) // south of it, east of the fix
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion after crossing the radial")
	}

	// The reciprocal is the same line, but it isn't the radial.
	mc = ManeuverComplete{Type: UntilRadial, Fix: fix, Radial: 90}
	at(315)
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion to wait before crossing")
	}
	at(225)
	if mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("crossing the reciprocal of the radial shouldn't count")
	}
	at(135)
	if !mc.Done(nav, Time{}, wx.Sample{}, 0) {
		t.Fatal("expected radial completion after crossing the radial")
	}
}

// makePTFlight builds a FlightTest with a manually-constructed RNAV
// approach containing a procedure turn at the first waypoint. The route
// string should start with the PT fix and include PT modifiers (e.g.
// "/pt45", "/hilpt4.0nm"). All waypoints are marked as on-approach.
// Modeled on the RNAV 15R at KISP which has a HILPT at FORMU.
func makePTFlight(t *testing.T, routeStr string, alt, speed float32) *FlightTest {
	t.Helper()

	// Use KISP (Islip) for airport metadata; the RNAV 15R has a real
	// procedure turn at FORMU.
	arrAirport, ok := db.DB.Airports["KISP"]
	if !ok {
		t.Fatal("KISP not in database")
	}
	depAirport, ok := db.DB.Airports["KMCO"]
	if !ok {
		t.Fatal("KMCO not in database")
	}

	nmPerLong := math.NMPerLongitudeAt(arrAirport.Location)
	magVar, err := db.DB.MagneticGrid.Lookup(arrAirport.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}

	wps := parseRoute(t, routeStr, magVar)
	for i := range wps {
		wps[i].SetOnApproach(true)
	}

	// Build a minimal RNAV approach from the waypoints.
	ap := &av.Approach{
		Id:       "TEST",
		FullName: "RNAV TEST",
		Type:     av.RNAVApproach,
		Runway:   "15R",
		Waypoints: []av.WaypointArray{
			wps.Clone(),
		},
	}

	if rwy, ok := av.LookupRunway(db.Lookups{}, "KISP", "15R"); ok {
		ap.Threshold = rwy.Threshold
	}
	if opp, ok := av.LookupOppositeRunway(db.Lookups{}, "KISP", "15R"); ok {
		ap.OppositeThreshold = opp.Threshold
	}

	rng := rand.Make()
	rng.Seed(42)

	// Inbound course: PT fix (wps[0]) toward the next fix (wps[1]).
	// Position the aircraft on the outbound side of the PT fix, heading
	// inbound, to simulate arriving from a feeder route.
	var hdg math.MagneticHeading
	var startPos math.Point2LL
	if len(wps) > 1 {
		inboundTrue := math.Heading2LL(wps[0].Location, wps[1].Location, nmPerLong)
		outboundTrue := math.OppositeHeading(inboundTrue)
		startPos = math.Offset2LL(wps[0].Location, outboundTrue, 5, nmPerLong)
		hdg = math.TrueToMagnetic(inboundTrue, magVar)
	}

	navWps := make([]av.Waypoint, len(wps)+1)
	copy(navWps, wps)
	navWps[len(wps)] = av.Waypoint{
		Fix:      "KISP",
		Location: arrAirport.Location,
	}

	n := &Nav{
		Perf:           db.DB.AircraftPerformance["A320"],
		FinalAltitude:  alt,
		FixAssignments: make(map[string]FixAssignment),
		Rand:           rng,
		Waypoints:      navWps,
		FlightState: FlightState{
			MagneticVariation:         magVar,
			NmPerLongitude:            nmPerLong,
			Position:                  startPos,
			Heading:                   hdg,
			Altitude:                  alt,
			IAS:                       speed,
			GS:                        speed,
			DepartureAirportLocation:  depAirport.Location,
			DepartureAirportElevation: float32(depAirport.Elevation),
			ArrivalAirportLocation:    arrAirport.Location,
			ArrivalAirportElevation:   float32(arrAirport.Elevation),
			ArrivalAirport: av.Waypoint{
				Fix:      "KISP",
				Location: arrAirport.Location,
			},
		},
	}

	// Set up approach state: assigned and cleared.
	n.Approach = Approach{
		Assigned:   ap,
		AssignedId: "TEST",
		Cleared:    true,
	}

	fp := av.FlightPlan{
		Rules:            av.FlightRulesIFR,
		AircraftType:     "A320",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KISP",
		Altitude:         int(alt),
	}

	return &FlightTest{
		t:        t,
		nav:      n,
		fp:       fp,
		callsign: "TEST001",
		simTime:  NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)),
		maxTicks: 7200,
		weather:  func(a float32) wx.Sample { return wx.MakeStandardSampleForAltitude(a) },
	}
}

// TestStandard45ProcedureTurnCompletes verifies that a standard 45-degree
// procedure turn creates the correct maneuver sequence and completes
// without getting stuck (regression test for 26fe133c and 409198f5).
func TestStandard45ProcedureTurnCompletes(t *testing.T) {
	// FORMU is the IAF on the RNAV 15R at KISP. Use it as the PT fix
	// with an inbound course toward ZIVUX. The /pt45 modifier creates
	// a standard 45-degree PT.
	f := makePTFlight(t, "FORMU/pt45/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, nil, &f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	if len(f.nav.Heading.Maneuvers) != 7 {
		t.Fatalf("expected 7 maneuvers for standard 45 PT, got %d", len(f.nav.Heading.Maneuvers))
	}

	// Run the simulation and verify all maneuvers complete.
	f.AfterTicks(600, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			// PT completed successfully
		} else {
			t.Logf("tick 600: %d maneuvers remaining", len(f.nav.Heading.Maneuvers))
		}
	})

	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			t.Errorf("PT did not complete after 1200 ticks: %d maneuvers remain", len(f.nav.Heading.Maneuvers))
		}
	})

	f.Run()
}

// TestRacetrackPTCreatesManeuvers verifies that a racetrack procedure
// turn (HILPT) creates a maneuver sequence and completes
// (regression test for 0c45e6bc).
func TestRacetrackPTCreatesManeuvers(t *testing.T) {
	f := makePTFlight(t, "FORMU/hilpt4.0nm/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, nil, &f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	// Racetrack PTs create at least 4 maneuvers (varies by entry type).
	if len(f.nav.Heading.Maneuvers) < 4 {
		t.Fatalf("expected at least 4 maneuvers for racetrack PT, got %d", len(f.nav.Heading.Maneuvers))
	}

	// Run and verify completion.
	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) > 0 {
			t.Errorf("racetrack PT did not complete after 1200 ticks: %d maneuvers remain",
				len(f.nav.Heading.Maneuvers))
		}
	})

	f.Run()
}

// TestPTWaypointFlyover verifies that waypoints with procedure turns are
// treated as flyover points — the PT only triggers when ETA < 2s, not
// before (regression test for cf822df4).
func TestPTWaypointFlyover(t *testing.T) {
	magVar, err := db.DB.MagneticGrid.Lookup(db.DB.Airports["KISP"].Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}
	wps := parseRoute(t, "FORMU/pt45/flyover ZIVUX", magVar)
	for i := range wps {
		wps[i].SetOnApproach(true)
	}

	// Verify the PT fix has FlyOver set.
	if !wps[0].FlyOver() {
		t.Errorf("PT waypoint should have FlyOver flag set")
	}

	// Verify the PT data is present.
	if wps[0].ProcedureTurn() == nil {
		t.Errorf("PT waypoint should have ProcedureTurn data")
	}
	if wps[0].ProcedureTurn().Type != av.PTStandard45 {
		t.Errorf("expected PTStandard45, got %d", wps[0].ProcedureTurn().Type)
	}
}

// TestProcedureTurnDescendsToExitAltitude verifies that a procedure turn
// with ExitAltitude descends the aircraft during the inbound leg.
func TestProcedureTurnDescendsToExitAltitude(t *testing.T) {
	// Use /pta2000 to set exit altitude to 2000.
	f := makePTFlight(t, "FORMU/pt45/pta2000/flyover ZIVUX WENGA", 3000, 180)

	wxs := f.weather(f.nav.FlightState.Altitude)
	f.nav.UpdateWithWeather(f.callsign, wxs, nil, &f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)

	f.nav.flyProcedureTurnIfNecessary()

	if len(f.nav.Heading.Maneuvers) != 7 {
		t.Fatalf("expected 7 maneuvers, got %d", len(f.nav.Heading.Maneuvers))
	}

	// The last maneuver should have AssignAltitude set to 2000.
	lastManeuver := f.nav.Heading.Maneuvers[6]
	if lastManeuver.AssignAltitude == nil {
		t.Errorf("last maneuver should have AssignAltitude set")
	} else if *lastManeuver.AssignAltitude != 2000 {
		t.Errorf("expected exit altitude 2000, got %.0f", *lastManeuver.AssignAltitude)
	}

	// Run the simulation and verify altitude near 2000 after PT completes.
	f.AfterTicks(1200, func(f *FlightTest) {
		if len(f.nav.Heading.Maneuvers) == 0 {
			// PT completed; altitude should be near 2000.
			f.AssertAltitudeNear(2000, 500)
		}
	})

	f.Run()
}

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

	netaa, ok := db.DB.LookupWaypoint("NETAA")
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

func TestHoldTurningInboundDoesNotFlyAwayAfterOvershoot(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAYED/a18000 RACKI/a13000/ho PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  11900,
		InitialSpeed:     200,
	})

	f.nav.FlightState.Position = math.Point2LL{-75.117279, 40.971386}
	f.nav.FlightState.Heading = 64.807312
	f.nav.FlightState.Altitude = 7281.493652
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 254.992889
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 11, 54, 12, 0, time.UTC))
	fixLocation := math.Point2LL{-75.109550, 40.880634}
	initialDistance := math.NMDistance2LL(f.nav.FlightState.Position, fixLocation)

	f.nav.Heading = Heading{Hold: &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
		Maneuvers: []LateralManeuver{
			turnToTrack(122, av.TurnRight),
			flyTowardFix(fixLocation),
		},
	}}

	f.nav.UpdateWithWeather(f.callsign, wx.MakeStandardSampleForAltitude(f.nav.FlightState.Altitude),
		nil, &f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)
	if f.nav.Heading.Hold == nil {
		t.Fatal("hold unexpectedly ended")
	}
	wantStep := turnToTrack(122, av.TurnRight)
	if got := f.nav.Heading.Hold.currentStep(); got != wantStep.String() {
		t.Fatalf("inbound turn ended early while heading %.1f, step=%s",
			f.nav.FlightState.Heading, got)
	}

	for range 90 {
		f.nav.UpdateWithWeather(f.callsign, wx.MakeStandardSampleForAltitude(f.nav.FlightState.Altitude),
			nil, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)
	}

	hold := f.nav.Heading.Hold
	if hold == nil {
		t.Fatal("hold unexpectedly ended")
	}
	dist := math.NMDistance2LL(f.nav.FlightState.Position, fixLocation)
	if dist >= initialDistance {
		t.Fatalf("aircraft kept flying away from hold: step=%s heading=%.1f distance=%.1f initial=%.1f position=%v",
			hold.currentStep(), f.nav.FlightState.Heading, dist, initialDistance, f.nav.FlightState.Position)
	}
}

func TestHoldInboundTurnDistanceMatchesOutboundTurn(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  7281,
		InitialSpeed:     200,
	}).SetWind(230, 10) // SetWind takes wind-from direction; this makes the wind vector track 050.

	fixLocation := math.Point2LL{-75.109550, 40.880634}
	f.nav.FlightState.Position = fixLocation
	f.nav.FlightState.Heading = 122
	f.nav.FlightState.Altitude = 7281
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 255
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 11, 52, 0, 0, time.UTC))

	hold := &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
	}
	wxs := f.weather(f.nav.FlightState.Altitude)
	hold.Maneuvers = hold.circuitManeuvers(f.nav, wxs)
	f.nav.Heading = Heading{Hold: hold}

	var outboundTurnStart math.Point2LL
	var inboundTurnStart math.Point2LL
	var outboundTurnDistance float32
	var inboundTurnDistance float32
	outboundTurnStep, outboundLegStep, inboundTurnStep := holdCircuitStepStrings(t, hold)
	previousStep := hold.currentStep()

	for tick := range 300 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), nil, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		step := hold.currentStep()
		if tick == 0 {
			outboundTurnStart = fixLocation
		}
		if step != previousStep {
			t.Logf("tick=%d step %q -> %q hdg=%.1f gs=%.1f pos=%v distFix=%.2f",
				tick, previousStep, step, f.nav.FlightState.Heading, f.nav.FlightState.GS,
				f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, fixLocation))

			switch previousStep {
			case outboundTurnStep:
				outboundTurnDistance = math.NMDistance2LL(outboundTurnStart, f.nav.FlightState.Position)
			case outboundLegStep:
				inboundTurnStart = f.nav.FlightState.Position
			case inboundTurnStep:
				inboundTurnDistance = math.NMDistance2LL(inboundTurnStart, f.nav.FlightState.Position)
				t.Logf("outboundTurn=%.2f inboundTurn=%.2f ratio=%.2f pos=%v",
					outboundTurnDistance, inboundTurnDistance, inboundTurnDistance/outboundTurnDistance,
					f.nav.FlightState.Position)
				if inboundTurnDistance > outboundTurnDistance*1.35 {
					t.Fatalf("inbound turn distance %.2f nm is too large vs outbound %.2f nm", inboundTurnDistance, outboundTurnDistance)
				}
				return
			}
			previousStep = step
		}
	}

	t.Fatalf("inbound turn did not complete; step=%s heading=%.1f position=%v",
		hold.currentStep(), f.nav.FlightState.Heading, f.nav.FlightState.Position)
}

func TestFQM3HoldInboundTurnCompletesNearExpectedTrack(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAYED/a18000 RACKI/a13000 PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  11900,
		InitialSpeed:     200,
	}).SetWind(230, 10) // SetWind takes wind-from direction; this makes the wind vector track 050.

	f.nav.HoldAtFix(f.callsign, "PENNS", &av.Hold{
		Fix:             "PENNS",
		InboundCourse:   122,
		TurnDirection:   av.TurnRight,
		LegMinutes:      1,
		MinimumAltitude: 3100,
		MaximumAltitude: 17500,
		HoldingSpeed:    200,
	})

	var hold *FlyHold
	var previousStep string
	var outboundTurnStart math.Point2LL
	var inboundTurnStart math.Point2LL
	var outboundTurnDistance float32
	var inboundTurnDistance float32
	outboundTurnStep := ""
	outboundLegStep := ""
	inboundTurnStep := ""
	flyFixStep := "fly toward fix until fix"

	for tick := range 2000 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), nil, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		if f.nav.Heading.Hold != nil && hold == nil {
			hold = f.nav.Heading.Hold
			previousStep = hold.currentStep()
			t.Logf("hold started tick=%d step=%q hdg=%.1f pos=%v", tick, previousStep,
				f.nav.FlightState.Heading, f.nav.FlightState.Position)
		}
		if hold == nil {
			continue
		}

		step := hold.currentStep()
		if step == previousStep {
			continue
		}

		t.Logf("fqm3 tick=%d step %q -> %q hdg=%.1f gs=%.1f pos=%v distFix=%.2f",
			tick, previousStep, step, f.nav.FlightState.Heading, f.nav.FlightState.GS,
			f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, hold.FixLocation))

		switch previousStep {
		case flyFixStep:
			outboundTurnStart = f.nav.FlightState.Position
			outboundTurnStep = step
			_, _, inboundTurnStep = holdCircuitStepStrings(t, hold)
		case outboundTurnStep:
			outboundTurnDistance = math.NMDistance2LL(outboundTurnStart, f.nav.FlightState.Position)
			outboundLegStep = step
		case outboundLegStep:
			inboundTurnStart = f.nav.FlightState.Position
		case inboundTurnStep:
			inboundTurnDistance = math.NMDistance2LL(inboundTurnStart, f.nav.FlightState.Position)
			t.Logf("fqm3 outboundTurn=%.2f inboundTurn=%.2f ratio=%.2f pos=%v distFix=%.2f",
				outboundTurnDistance, inboundTurnDistance, inboundTurnDistance/outboundTurnDistance,
				f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, hold.FixLocation))
			if inboundTurnDistance > outboundTurnDistance*1.35 {
				t.Fatalf("inbound turn distance %.2f nm is too large vs outbound %.2f nm", inboundTurnDistance, outboundTurnDistance)
			}
			return
		}

		previousStep = step
	}

	t.Fatalf("FQM3 hold inbound turn did not complete")
}

func TestHoldOutboundHeadingBiasesUpwindFromInboundCorrection(t *testing.T) {
	nav := &Nav{}
	nav.FlightState.GS = 200
	nav.FlightState.MagneticVariation = 0

	hold := &FlyHold{Hold: av.Hold{InboundCourse: 90}}
	windNorth := wx.MakeSample(
		math.Scale2f(math.SinCos(math.Radians(math.TrueHeading(0))), 40.0/3600.0),
		15, -5, 1013)
	outbound := hold.outboundHeading(nav, windNorth)
	correction := math.HeadingSignedTurn(math.MagneticHeading(270), outbound)
	if correction > -25 || correction < -45 {
		t.Fatalf("expected strong left outbound correction, got outbound %.1f correction %.1f",
			outbound, correction)
	}

	windSouth := wx.MakeSample(
		math.Scale2f(math.SinCos(math.Radians(math.TrueHeading(180))), 40.0/3600.0),
		15, -5, 1013)
	outbound = hold.outboundHeading(nav, windSouth)
	correction = math.HeadingSignedTurn(math.MagneticHeading(270), outbound)
	if correction < 25 || correction > 45 {
		t.Fatalf("expected strong right outbound correction, got outbound %.1f correction %.1f",
			outbound, correction)
	}
}

func TestHoldInboundTurnCompletesAfterHalfCircuitWithStrongWind(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  7281,
		InitialSpeed:     200,
	}).SetWind(195, 45)

	fixLocation := math.Point2LL{-75.109550, 40.880634}
	f.nav.FlightState.Position = math.Point2LL{-75.10486, 40.87529}
	f.nav.FlightState.Heading = 176.6
	f.nav.FlightState.Altitude = 7281
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 202
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 4, 28, 40, 0, time.UTC))

	hold := &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
	}
	wxs := f.weather(f.nav.FlightState.Altitude)
	hold.Maneuvers = hold.circuitManeuvers(f.nav, wxs)
	f.nav.Heading = Heading{Hold: hold}

	outboundTurnStep, outboundLegStep, inboundTurnStep := holdCircuitStepStrings(t, hold)
	previousStep := hold.currentStep()

	outboundTurnStartTick := 0
	outboundTurnEndTick := -1
	inboundTurnStartTick := -1

	for tick := range 240 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), nil, &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		step := hold.currentStep()
		if step == previousStep {
			continue
		}

		switch previousStep {
		case outboundTurnStep:
			outboundTurnEndTick = tick
		case outboundLegStep:
			inboundTurnStartTick = tick
		case inboundTurnStep:
			if outboundTurnEndTick < 0 || inboundTurnStartTick < 0 {
				t.Fatalf("missing prior transition: outboundEnd=%d inboundStart=%d",
					outboundTurnEndTick, inboundTurnStartTick)
			}

			outboundTurnSeconds := outboundTurnEndTick - outboundTurnStartTick
			inboundTurnSeconds := tick - inboundTurnStartTick
			t.Logf("outboundTurn=%ds inboundTurn=%ds pos=%v distFix=%.2f",
				outboundTurnSeconds, inboundTurnSeconds, f.nav.FlightState.Position,
				math.NMDistance2LL(f.nav.FlightState.Position, fixLocation))

			if inboundTurnSeconds > 45 {
				t.Fatalf("inbound turn took %d seconds; want about 40 seconds for the partial inbound turn",
					inboundTurnSeconds)
			}
			return
		}

		previousStep = step
	}

	t.Fatalf("inbound turn did not complete; step=%s heading=%.1f position=%v",
		hold.currentStep(), f.nav.FlightState.Heading, f.nav.FlightState.Position)
}

func holdCircuitStepStrings(t *testing.T, hold *FlyHold) (string, string, string) {
	t.Helper()
	if len(hold.Maneuvers) < 3 {
		t.Fatalf("hold circuit has %d maneuvers, want at least 3", len(hold.Maneuvers))
	}
	return hold.Maneuvers[0].String(), hold.Maneuvers[1].String(), hold.Maneuvers[2].String()
}
