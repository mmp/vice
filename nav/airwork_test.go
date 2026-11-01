// nav/airwork_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// airworkArea is the practice area the tests fly in: 6nm around a point
// over terrain 500' high, between 1,000' and 7,000'.
var airworkArea = Airwork{
	Radius:   6,
	Center:   math.Point2LL{-73.5, 41.5},
	AltRange: [2]float32{1000, 7000},
	Terrain:  500,
}

// makeAirworkNav returns a Nav for an aircraft of the given type in the
// middle of airworkArea at cruise speed.
func makeAirworkNav(t *testing.T, acType string, alt float32, seed uint64) *Nav {
	t.Helper()
	perf, ok := db.DB.AircraftPerformance[acType]
	if !ok {
		t.Fatalf("%s: no performance data", acType)
	}
	ias := av.TASToIAS(perf.Speed.CruiseTAS, alt, wx.MakeStandardSampleForAltitude(alt).Temperature())
	return &Nav{
		Perf:           perf,
		FixAssignments: make(map[string]FixAssignment),
		Rand:           rand.New(seed),
		FlightState: FlightState{
			MagneticVariation: -13,
			NmPerLongitude:    math.NMPerLongitudeAt(airworkArea.Center),
			Position:          airworkArea.Center,
			Heading:           math.MagneticHeading(rand.New(seed).Float32Range(0, 360)),
			Altitude:          alt,
			IAS:               min(ias, 250),
			GS:                min(ias, 250),
		},
	}
}

// airworkWeather returns standard weather with wind from the given
// direction at the given speed.
func airworkWeather(alt, from, kts float32) wx.Sample {
	std := wx.MakeStandardSampleForAltitude(alt)
	v := math.Scale2f(math.HeadingVector(math.TrueHeading(from+180)), kts/3600)
	return wx.MakeSample(v, std.Temperature().Celsius(), std.Dewpoint().Celsius(), std.Pressure())
}

// flyAirwork flies the nav's airwork until it ends, calling check after
// every tick. It fails the test if the airwork goes on for longer than
// maxSeconds.
func flyAirwork(t *testing.T, nav *Nav, windFrom, windKts float32, maxSeconds int, check func(prev FlightState)) int {
	t.Helper()
	simTime := NewTime(time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC))
	for s := range maxSeconds {
		if nav.Airwork == nil {
			return s
		}
		prev := nav.FlightState
		wxs := airworkWeather(nav.FlightState.Altitude, windFrom, windKts)
		nav.UpdateWithWeather("N123", wxs, nil, av.FlightRulesVFR, simTime, false)
		simTime = simTime.Add(time.Second)
		check(prev)
	}
	t.Fatalf("airwork still going after %d seconds", maxSeconds)
	return maxSeconds
}

// TestAirworkManeuvers flies every maneuver in the aircraft that practice
// it, in calm and in wind, and checks that each keeps its heights above the
// terrain, stays within the area's altitudes and near the area, flies
// speeds and rates that the aircraft can, and ends back at cruise.
func TestAirworkManeuvers(t *testing.T) {
	for _, acType := range []string{"C172", "SR22", "PA44", "C550"} {
		for _, windKts := range []float32{0, 25} {
			for m := range NumAirworkManeuvers {
				for seed := range uint64(4) {
					nav := makeAirworkNav(t, acType, 4500, seed)
					if !m.practicedIn(nav.Perf) {
						continue
					}
					name := fmt.Sprintf("%s/%s/wind%.0f/%d", acType, m, windKts, seed)
					t.Run(name, func(t *testing.T) {
						testAirworkManeuver(t, nav, m, 270, windKts)
					})
				}
			}
		}
	}
}

func testAirworkManeuver(t *testing.T, nav *Nav, m AirworkManeuver, windFrom, windKts float32) {
	aw := airworkArea
	aw.Radius *= AirworkAreaScale(nav.Perf)
	aw.RemainingSteps = 1 // just this maneuver
	nav.Airwork = &aw
	start := nav.FlightState
	aw.planManeuver(m, nav, airworkWeather(start.Altitude, windFrom, windKts))

	l := m.limits(nav.Perf)
	floor := max(aw.Terrain+l.minAGL, aw.AltRange[0])
	ceiling := max(start.Altitude, min(aw.Terrain+l.maxAGL, aw.AltRange[1]))
	vs := nav.Perf.Speed.Min
	// Climbs in chandelles and descents in stall breaks reach 2,000-3,000
	// feet per minute in light aircraft; emergency descents are faster.
	maxRate := max(3000, 2.2*nav.Perf.Rate.Descent)

	var lowest, maxDist float32 = start.Altitude, 0
	seconds := flyAirwork(t, nav, windFrom, windKts, 30*60, func(prev FlightState) {
		fs := nav.FlightState
		lowest = min(lowest, fs.Altitude)
		maxDist = max(maxDist, math.NMDistance2LL(fs.Position, aw.Center))
		if fs.Altitude < floor-1 || fs.Altitude > ceiling+1 {
			t.Fatalf("altitude %.0f outside %.0f-%.0f", fs.Altitude, floor, ceiling)
		}
		if fs.IAS < vs-0.5 || fs.IAS > nav.maxIAS(wx.MakeStandardSampleForAltitude(fs.Altitude).Temperature()) {
			t.Fatalf("IAS %.1f outside %.0f-max", fs.IAS, vs)
		}
		if r := math.Abs(fs.Altitude-prev.Altitude) * 60; r > maxRate {
			t.Fatalf("vertical rate %.0f fpm at %.0f'", r, fs.Altitude)
		}
		if r := math.HeadingDifference(fs.Heading, prev.Heading); r > 30 {
			t.Fatalf("turned %.0f degrees in a second", r)
		}
	})

	if seconds > 20*60 {
		t.Errorf("took %d seconds", seconds)
	}
	if maxDist > aw.Radius+1 {
		t.Errorf("went %.1fnm from the center of a %.0fnm area", maxDist, aw.Radius)
	}
	cruise := av.TASToIAS(nav.Perf.Speed.CruiseTAS, nav.FlightState.Altitude,
		wx.MakeStandardSampleForAltitude(nav.FlightState.Altitude).Temperature())
	if math.Abs(nav.FlightState.IAS-min(cruise, 250)) > 2 {
		t.Errorf("finished at %.0f knots rather than cruise %.0f", nav.FlightState.IAS, cruise)
	}
	t.Logf("%d seconds, lowest %.0f' (%.0f AGL), farthest %.1fnm", seconds, lowest, lowest-aw.Terrain, maxDist)
}

// TestAirworkHeights checks maneuvers against the heights the FAA gives
// for them.
func TestAirworkHeights(t *testing.T) {
	c172 := db.DB.AircraftPerformance["C172"]
	pa44 := db.DB.AircraftPerformance["PA44"]
	for _, tc := range []struct {
		m         AirworkManeuver
		perf      av.AircraftPerformance
		lowestAGL float32
	}{
		{AirworkPowerOffStall, c172, 1500},
		{AirworkPowerOnStall, c172, 1500},
		{AirworkSlowFlight, c172, 1500},
		{AirworkPowerOffStall, pa44, 3000},
		{AirworkSlowFlight, pa44, 3000},
		{AirworkAcceleratedStall, c172, 3000},
		{AirworkSteepSpiral, c172, 1500},
		{AirworkRectangularCourse, c172, 600},
		{AirworkSTurns, c172, 600},
		{AirworkTurnsAroundPoint, c172, 600},
	} {
		if l := tc.m.limits(tc.perf); l.minAGL != tc.lowestAGL {
			t.Errorf("%s in a %s: lowest %.0f AGL, expected %.0f", tc.m, tc.perf.ICAO, l.minAGL, tc.lowestAGL)
		}
	}

	// The ground reference maneuvers are flown 600-1,000' above the
	// ground, so an area whose floor is higher has none of them.
	aw := airworkArea
	aw.AltRange[0] = aw.Terrain + 1500
	for _, m := range []AirworkManeuver{AirworkRectangularCourse, AirworkSTurns, AirworkTurnsAroundPoint} {
		if _, _, ok := aw.entryWindow(m.limits(c172)); ok {
			t.Errorf("%s fits in %.0f-%.0f over %.0f terrain", m, aw.AltRange[0], aw.AltRange[1], aw.Terrain)
		}
	}
}

// TestAirworkNoRoom checks that an area whose altitudes leave no room for
// any maneuver over its terrain still keeps clear of the ground.
func TestAirworkNoRoom(t *testing.T) {
	nav := makeAirworkNav(t, "C172", 4500, 2)
	aw := airworkArea
	aw.AltRange = [2]float32{1400, 1400}
	aw.Terrain = 1800
	aw.RemainingSteps = 10 * 60
	nav.Airwork = &aw
	if m := aw.chooseManeuver(nav); m != AirworkTurns {
		t.Errorf("chose %s with no room for it", m)
	}
	flyAirwork(t, nav, 0, 0, 30*60, func(FlightState) {
		if nav.FlightState.Altitude < aw.Terrain+1000-1 {
			t.Fatalf("down to %.0f' over %.0f' terrain", nav.FlightState.Altitude, aw.Terrain)
		}
	})
}

// TestAirworkSession flies a route through a waypoint calling for 20
// minutes of airwork and checks that the aircraft practices several
// maneuvers there and then continues along its route.
func TestAirworkSession(t *testing.T) {
	for _, acType := range []string{"C172", "PA44"} {
		t.Run(acType, func(t *testing.T) {
			nav := makeAirworkNav(t, acType, 4500, 1)
			wp := av.Waypoint{Fix: "_airwork", Location: airworkArea.Center}
			wp.SetAltitudeRestriction(av.MakeRangeAltitudeRestriction(airworkArea.AltRange[0], airworkArea.AltRange[1]))
			e := wp.InitExtra()
			e.AirworkRadius, e.AirworkMinutes, e.AirworkTerrain = 6, 20, 500
			next := math.Offset2LL(airworkArea.Center, 90, 15, nav.FlightState.NmPerLongitude)
			nav.Waypoints = []av.Waypoint{wp, {Fix: "_next", Location: next}}
			nav.Airwork = StartAirwork(wp)

			maneuvers := make(map[AirworkManeuver]bool)
			seconds := flyAirwork(t, nav, 200, 15, 40*60, func(FlightState) {
				if nav.Airwork != nil {
					maneuvers[nav.Airwork.Maneuver] = true
				}
				if nav.FlightState.Altitude < airworkArea.Terrain+500 {
					t.Fatalf("down to %.0f'", nav.FlightState.Altitude)
				}
			})
			if seconds < 20*60 {
				t.Errorf("airwork ended after %d seconds", seconds)
			}
			if len(maneuvers) < 3 {
				t.Errorf("only practiced %v", maneuvers)
			}
			if len(nav.Waypoints) == 0 || nav.Waypoints[0].Fix != "_airwork" && nav.Waypoints[0].Fix != "_next" {
				t.Errorf("not back on the route: %v", nav.Waypoints)
			}
			t.Logf("%d seconds, %d maneuvers: %v", seconds, len(maneuvers), maneuvers)
		})
	}
}

// TestAirworkJSON checks that a Nav in the middle of a maneuver encodes
// and decodes to the same thing, as traces and the client need.
func TestAirworkJSON(t *testing.T) {
	nav := makeAirworkNav(t, "C172", 4500, 3)
	aw := airworkArea
	aw.RemainingSteps = 600
	nav.Airwork = &aw
	aw.planManeuver(AirworkSTurns, nav, airworkWeather(4500, 270, 20))
	simTime := NewTime(time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC))
	for range 400 {
		nav.UpdateWithWeather("N123", airworkWeather(nav.FlightState.Altitude, 270, 20), nil, av.FlightRulesVFR,
			simTime, false)
	}
	if !aw.Progress.Started || aw.Segments[0].Kind != AirworkCircle {
		t.Fatalf("expected to be circling: %+v", aw.Segments)
	}

	b, err := json.Marshal(nav.Airwork)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Airwork
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if b2, _ := json.Marshal(decoded); !bytes.Equal(b, b2) {
		t.Errorf("round trip changed it:\n%s\n%s", b, b2)
	}
}
