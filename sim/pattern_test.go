// sim/pattern_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

var patternTestTime = NewSimTime(time.Date(2026, time.August, 4, 15, 0, 0, 0, time.UTC))

// vfrArrival returns an arrival to the given airport whose next waypoint is
// in the given phase of a VFR arrival.
func vfrArrival(callsign string, airport av.ICAOAirportCode, phase uint8) *Aircraft {
	return &Aircraft{
		ADSBCallsign:   av.ADSBCallsign(callsign),
		ArrivalAirport: airport,
		Nav: nav.Nav{
			Instructions: nav.Instructions{
				Waypoints: av.WaypointArray{{Fix: "_wp", VFRPhase: phase}},
			},
		},
	}
}

// holdingArrival returns an arrival orbiting at the given airport since the
// given time, waiting for its turn to enter the pattern.
func holdingArrival(callsign string, airport av.ICAOAirportCode, since Time) *Aircraft {
	ac := vfrArrival(callsign, airport, av.VFRPhaseOrbit)
	ac.HoldingSince = since
	return ac
}

func makePatternSim(acs ...*Aircraft) *Sim {
	s := NewTestSim(testLogger())
	for _, ac := range acs {
		s.Aircraft[ac.ADSBCallsign] = ac
	}
	return s
}

func TestLongestHoldingArrival(t *testing.T) {
	first := holdingArrival("N111", "KFOK", patternTestTime)
	second := holdingArrival("N222", "KFOK", patternTestTime.Add(2*time.Minute))
	elsewhere := holdingArrival("N333", "KGON", patternTestTime.Add(-time.Hour))
	inbound := vfrArrival("N444", "KFOK", av.VFRPhaseStraightIn)
	s := makePatternSim(second, first, elsewhere, inbound)

	if got := s.longestHoldingArrival("KFOK"); got != first {
		t.Errorf("longest holder at KFOK is %v, expected N111", got)
	}
	if got := s.longestHoldingArrival("KGON"); got != elsewhere {
		t.Errorf("longest holder at KGON is %v, expected N333", got)
	}
	if got := s.longestHoldingArrival("KISP"); got != nil {
		t.Errorf("longest holder at KISP is %v, expected none", got)
	}
}

func TestLongestHoldingArrivalBreaksTiesByCallsign(t *testing.T) {
	first := holdingArrival("N111", "KFOK", patternTestTime)
	second := holdingArrival("N222", "KFOK", patternTestTime)
	s := makePatternSim(first, second)

	// Both started holding at the same time, so the result can't be left to
	// map iteration order.
	for range 10 {
		if got := s.longestHoldingArrival("KFOK"); got != first {
			t.Fatalf("longest holder is %v, expected N111", got)
		}
	}
}

func TestFreshArrivalsHoldBehindWaitingTraffic(t *testing.T) {
	// Nothing in the pattern and no one waiting: go on in.
	if s := makePatternSim(); s.arrivalsMustHold("KFOK") {
		t.Error("arrival had to hold at an airport with no traffic")
	}

	// Someone is already holding: a new arrival can't take the slot from
	// under it, even though the pattern is clear.
	s := makePatternSim(holdingArrival("N111", "KFOK", patternTestTime))
	if !s.arrivalsMustHold("KFOK") {
		t.Error("arrival jumped the queue ahead of an aircraft that was already holding")
	}

	// No one waiting, but there's no room to enter.
	s = makePatternSim(vfrArrival("N222", "KFOK", av.VFRPhaseDownwind))
	if !s.arrivalsMustHold("KFOK") {
		t.Error("arrival entered the pattern with traffic on the entry downwind")
	}
}

func TestHoldingArrivalsAdmittedInOrder(t *testing.T) {
	fokFirst := holdingArrival("N111", "KFOK", patternTestTime)
	fokSecond := holdingArrival("N222", "KFOK", patternTestTime.Add(time.Minute))
	gon := holdingArrival("N333", "KGON", patternTestTime.Add(time.Hour))
	hvn := holdingArrival("N444", "KHVN", patternTestTime)
	hvnBlocker := vfrArrival("N555", "KHVN", av.VFRPhaseDownwind)
	s := makePatternSim(fokFirst, fokSecond, gon, hvn, hvnBlocker)

	admit := s.holdingArrivalsToAdmit()

	// One per airport with room: the longest-waiting one at KFOK and the
	// only one at KGON. KHVN's pattern is occupied, so nobody gets in there.
	want := []*Aircraft{fokFirst, gon}
	if len(admit) != len(want) {
		t.Fatalf("admitting %d aircraft, expected %d", len(admit), len(want))
	}
	for i := range want {
		if admit[i] != want[i] {
			t.Errorf("admitting %s at index %d, expected %s", admit[i].ADSBCallsign, i,
				want[i].ADSBCallsign)
		}
	}
}

func TestPatternAircraftOnDownwindBlocksAdmission(t *testing.T) {
	holding := holdingArrival("N111", "KFOK", patternTestTime)
	patternAC := vfrArrival("N222", "KFOK", av.VFRPhaseDownwind)
	s := makePatternSim(holding, patternAC)
	s.PatternState["KFOK"] = &PatternState{
		Aircraft: []PatternAircraft{{ADSBCallsign: patternAC.ADSBCallsign, Phase: PatternDownwind}},
	}

	if admit := s.holdingArrivalsToAdmit(); len(admit) != 0 {
		t.Errorf("admitted %d aircraft with a pattern aircraft on downwind, expected none", len(admit))
	}

	// Once it turns base there's room for the holding aircraft to enter.
	s.PatternState["KFOK"].Aircraft[0].Phase = PatternBase
	patternAC.Nav.Waypoints[0].VFRPhase = av.VFRPhaseBase
	admit := s.holdingArrivalsToAdmit()
	if len(admit) != 1 || admit[0] != holding {
		t.Errorf("admitted %v, expected just N111", admit)
	}
}

// TestVFRRunwayIsLocked checks that an airport's VFR runway doesn't follow the
// wind: one chosen when the sim was created stays in use whatever the wind is
// now, and an airport without one has it chosen the first time it's needed and
// keeps it.
func TestVFRRunwayIsLocked(t *testing.T) {
	s := NewTestSim(testLogger())

	fcm := db.DB.Airports["KFCM"]
	wx := s.wxModel.Lookup(fcm.Location, float32(fcm.Elevation), s.State.SimTime.Time())
	windRwy, _ := fcm.SelectBestRunway(wx.WindDirection(), s.State.MagneticVariation)
	locked := fcm.Runways[slices.IndexFunc(fcm.Runways, func(r av.Runway) bool {
		_, ok := av.LookupOppositeRunway(db.Lookups{}, "KFCM", r.Id)
		return ok && r.Id != windRwy.Id
	})]
	s.State.VFRRunways["KFCM"] = locked

	rwy, opp, ok := s.vfrRunway("KFCM")
	wantOpp, _ := av.LookupOppositeRunway(db.Lookups{}, "KFCM", locked.Id)
	if !ok || rwy.Id != locked.Id || opp.Id != wantOpp.Id {
		t.Errorf("KFCM VFR runway is %s/%s (ok %v), want the locked %s/%s", rwy.Id, opp.Id, ok,
			locked.Id, wantOpp.Id)
	}

	rwy, _, ok = s.vfrRunway("KANE")
	if !ok {
		t.Fatal("KANE has no VFR runway")
	}
	if got, ok := s.State.VFRRunways["KANE"]; !ok || got.Id != rwy.Id {
		t.Errorf("KANE VFR runway %s wasn't locked; have %q", rwy.Id, got.Id)
	}
}

// flyPatternEntry flies an aircraft of the given type from where VFR
// arrivals hold at KBUU into the 45-degree entry for runway 29 drawn at the
// given scale. It reports whether the aircraft reached the runway in 30
// minutes and how far it turned after the last fix before it.
func flyPatternEntry(t *testing.T, aircraftType string, scale float32) (landed bool, finalTurn float32) {
	t.Helper()

	const airport = "KBUU"
	rwy, ok := av.LookupRunway(db.Lookups{}, airport, "29")
	if !ok {
		t.Fatal("no runway KBUU/29")
	}
	opp, ok := av.LookupOppositeRunway(db.Lookups{}, airport, "29")
	if !ok {
		t.Fatal("no runway opposite KBUU/29")
	}
	ap := db.DB.Airports[airport]
	nmPerLongitude := math.NMPerLongitudeAt(ap.Location)
	magneticVariation, err := db.DB.MagneticGrid.Lookup(ap.Location)
	if err != nil {
		t.Fatal(err)
	}
	perf, ok := db.DB.AircraftPerformance[aircraftType]
	if !ok {
		t.Fatalf("unknown aircraft type %q", aircraftType)
	}

	wps := generatePatternEntryWaypoints(rwy, opp, ap.Elevation, ap.Elevation+3000, scale, nmPerLongitude,
		magneticVariation)
	rightHdg := math.OffsetHeading(math.MagneticToTrue(rwy.Heading, magneticVariation), 90)
	start := math.Offset2LL(rwy.Threshold, rightHdg, vfrOrbitOffset, nmPerLongitude)
	speed := max(patternSpeed, perf.MinimumSpeed())
	altitude := float32(ap.Elevation + 1500)
	n := nav.Nav{
		Instructions: nav.Instructions{
			FixAssignments: make(map[string]nav.FixAssignment),
			Waypoints:      wps,
		},
		Perf:          perf,
		FinalAltitude: altitude,
		Delays:        nav.BriefPilotDelays,
		Rand:          vrand.Make(),
		FlightState: nav.FlightState{
			MagneticVariation:       magneticVariation,
			NmPerLongitude:          nmPerLongitude,
			Position:                start,
			Heading:                 math.TrueToMagnetic(math.Heading2LL(start, wps[0].Location, nmPerLongitude), magneticVariation),
			Altitude:                altitude,
			IAS:                     speed,
			GS:                      speed,
			ArrivalAirport:          av.Waypoint{Fix: airport, Location: ap.Location},
			ArrivalAirportLocation:  ap.Location,
			ArrivalAirportElevation: float32(ap.Elevation),
		},
	}

	simTime := nav.NewTime(patternTestTime.Time())
	for range 1800 {
		prev := n.FlightState.Heading
		wxs := wx.MakeStandardSampleForAltitude(n.FlightState.Altitude)
		result := n.UpdateWithWeather("TEST", wxs, nil, av.FlightRulesVFR, simTime, false)
		simTime = simTime.Add(time.Second)
		if n.Waypoints[0].Fix == "_pat_threshold" {
			finalTurn += math.HeadingDifference(prev, n.FlightState.Heading)
		}
		if result.PassedWaypoint != nil && result.PassedWaypoint.Fix == "_pat_threshold" {
			return true, finalTurn
		}
	}
	return false, finalTurn
}

// TestPatternEntryReachesRunway checks that aircraft from ones that fly the
// pattern at patternSpeed to jets that can't slow below 120 knots fly the
// 45-degree entry to the runway. Drawn at patternSpeed's size, the pattern
// put the threshold inside a jet's turn and the jet circled it.
func TestPatternEntryReachesRunway(t *testing.T) {
	for _, aircraftType := range []string{"C172", "C208", "C550", "C510", "E135", "E55P", "C750"} {
		t.Run(aircraftType, func(t *testing.T) {
			scale := patternScale(db.DB.AircraftPerformance[aircraftType])
			landed, finalTurn := flyPatternEntry(t, aircraftType, scale)
			if !landed {
				t.Fatalf("never reached the runway; turned %.0f degrees after the final fix", finalTurn)
			}
			if finalTurn >= 360 {
				t.Errorf("circled the runway, turning %.0f degrees after the final fix", finalTurn)
			}
		})
	}
}
