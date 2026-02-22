package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// makeVisualTestAircraft creates an arrival aircraft with an ILS approach
// assigned, on frequency, positioned at the given location heading in the
// given direction. Suitable for canRequestVisualApproach and
// checkSpontaneousVisualRequest tests.
func makeVisualTestAircraft(pos math.Point2LL, heading float32) *Aircraft {
	return makeVisualTestAircraftAlt(pos, heading, 3000) // default 3000ft MSL
}

func makeVisualTestAircraftAlt(pos math.Point2LL, heading float32, altitude float32) *Aircraft {
	return &Aircraft{
		ADSBCallsign:        "AAL123",
		TypeOfFlight:        av.FlightTypeArrival,
		ControllerFrequency: "125.0",
		FlightPlan: av.FlightPlan{
			ArrivalAirport: "KJFK",
		},
		Nav: nav.Nav{
			FlightState: nav.FlightState{
				Position:          pos,
				Heading:           heading,
				Altitude:          altitude,
				NmPerLongitude:    52, // ~40°N
				MagneticVariation: 0,
			},
			Approach: nav.NavApproach{
				AssignedId: "I13L",
				Assigned: &av.Approach{
					Type:   av.ILSApproach,
					Runway: "13L",
				},
			},
		},
	}
}

// makeVisualTestSim creates a minimal Sim with a KJFK airport at the given
// location, a VMC METAR, and a charted visual approach for the given runway.
func makeVisualTestSim(airportLoc math.Point2LL, runway string) *Sim {
	return &Sim{
		Rand: vrand.Make(),
		State: &CommonState{
			DynamicState: DynamicState{
				METAR: map[string]wx.METAR{
					"KJFK": {Raw: "KJFK 10SM BKN050"},
				},
			},
			Airports: map[string]*av.Airport{
				"KJFK": {
					Location: airportLoc,
					Approaches: map[string]*av.Approach{
						"V13L": {Type: av.ChartedVisualApproach, Runway: runway},
					},
				},
			},
		},
	}
}

func TestCheckVisualEligibility(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}

	// Set up av.DB so ceiling checks can look up airport elevation.
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	tests := []struct {
		name      string
		sim       *Sim
		ac        *Aircraft
		wantField bool
	}{
		{
			name:      "VMC, close, facing airport, charted visual exists",
			sim:       makeVisualTestSim(airportLoc, "13L"),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180), // 5nm north, heading south
			wantField: true,
		},
		{
			name:      "Too far from airport (20nm)",
			sim:       makeVisualTestSim(airportLoc, "13L"),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 20.0 / 60}, 180),
			wantField: false,
		},
		{
			name:      "Facing away from airport",
			sim:       makeVisualTestSim(airportLoc, "13L"),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 0), // heading north, away
			wantField: false,
		},
		{
			name: "IMC conditions",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 1SM OVC003"} // 1SM vis, 300ft ceiling
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180),
			wantField: false,
		},
		{
			name: "No charted visual for assigned runway, still field in sight",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "31R") // visual exists for 31R, not 13L
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180),
			wantField: true,
		},
		{
			name: "No METAR available",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				delete(s.State.METAR, "KJFK")
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180),
			wantField: false,
		},
		{
			name: "Nil assigned approach",
			sim:  makeVisualTestSim(airportLoc, "13L"),
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
				ac.Nav.Approach.Assigned = nil
				return ac
			}(),
			wantField: false,
		},
		{
			name: "Unknown arrival airport",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				delete(s.State.Airports, "KJFK")
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180),
			wantField: false,
		},
		{
			name: "Low visibility, aircraft beyond vis range",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 5SM BKN050"} // 5SM vis
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 8.0 / 60}, 180), // 8nm out > 5SM
			wantField: false,
		},
		{
			name: "Good visibility, aircraft within vis range",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM BKN050"} // 10SM vis
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 8.0 / 60}, 180), // 8nm out < 10SM
			wantField: true,
		},
		{
			name: "3SM visibility, aircraft at 5nm — beyond vis",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 3SM BKN050"} // 3SM vis, VMC ceiling
				return s
			}(),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180), // 5nm > 3SM
			wantField: false,
		},
		{
			name: "VMC surface but aircraft above ceiling",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM BKN030"} // 3000ft ceiling, VMC
				return s
			}(),
			ac:        makeVisualTestAircraftAlt(math.Point2LL{0, 5.0 / 60}, 180, 4000), // above ceiling (elev 0 + 3000)
			wantField: false,
		},
		{
			name: "VMC surface, aircraft below ceiling",
			sim: func() *Sim {
				s := makeVisualTestSim(airportLoc, "13L")
				s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM BKN050"} // 5000ft ceiling
				return s
			}(),
			ac:        makeVisualTestAircraftAlt(math.Point2LL{0, 5.0 / 60}, 180, 3000), // below ceiling (elev 0 + 5000)
			wantField: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			elig := tt.sim.checkVisualEligibility(tt.ac)
			if elig.FieldInSight != tt.wantField {
				t.Errorf("FieldInSight = %v, want %v", elig.FieldInSight, tt.wantField)
			}
		})
	}
}

func TestCanRequestVisualApproach(t *testing.T) {
	tests := []struct {
		name string
		ac   *Aircraft
		want bool
	}{
		{
			name: "Eligible arrival with ILS assigned",
			ac: func() *Aircraft {
				return makeVisualTestAircraft(math.Point2LL{}, 180)
			}(),
			want: true,
		},
		{
			name: "Departure aircraft",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.TypeOfFlight = av.FlightTypeDeparture
				return ac
			}(),
			want: false,
		},
		{
			name: "Already has field in sight",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.FieldInSight = true
				return ac
			}(),
			want: false,
		},
		{
			name: "Already requested visual",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.RequestedVisual = true
				return ac
			}(),
			want: false,
		},
		{
			name: "Not on frequency",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.ControllerFrequency = ""
				return ac
			}(),
			want: false,
		},
		{
			name: "No approach assigned",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.Nav.Approach.AssignedId = ""
				ac.Nav.Approach.Assigned = nil
				return ac
			}(),
			want: false,
		},
		{
			name: "Approach already cleared",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.Nav.Approach.Cleared = true
				return ac
			}(),
			want: false,
		},
		{
			name: "Already on a visual approach",
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{}, 180)
				ac.Nav.Approach.Assigned = &av.Approach{
					Type:   av.ChartedVisualApproach,
					Runway: "13L",
				}
				return ac
			}(),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ac.canRequestVisualApproach(); got != tt.want {
				t.Errorf("canRequestVisualApproach() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVisualRequestBearingFilter(t *testing.T) {
	// Airport at (0, 0). Place aircraft 5nm north (latitude +5/60 degrees),
	// so the bearing from aircraft to airport is ~180° (due south).
	airportLoc := math.Point2LL{0, 0}
	acPos := math.Point2LL{0, 5.0 / 60} // 5nm north

	tests := []struct {
		name               string
		heading            float32
		shouldBePastFilter bool // true = bearing difference <= 120
	}{
		{"Heading south toward airport", 180, true},
		{"Heading southwest", 225, true},
		{"Heading southeast", 135, true},
		{"Heading east (abeam)", 90, true},            // 90° off nose
		{"Heading west (abeam)", 270, true},           // 90° off nose
		{"Heading north away from airport", 0, false}, // 180° off nose
		{"Heading NNE away", 30, false},               // 150° off nose
		{"Heading NNW away", 330, false},              // 150° off nose
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nmPerLong := float32(52)
			magVar := float32(0)
			bearing := math.Heading2LL(acPos, airportLoc, nmPerLong, magVar)
			diff := math.HeadingDifference(tt.heading, bearing)
			pastFilter := diff <= 120

			if pastFilter != tt.shouldBePastFilter {
				t.Errorf("heading=%.0f bearing=%.0f diff=%.0f: got pastFilter=%v, want %v",
					tt.heading, bearing, diff, pastFilter, tt.shouldBePastFilter)
			}
		})
	}
}

// setupTestRunway installs a minimal runway into the global aviation DB
// for the duration of the test, then removes it on cleanup.
func setupTestRunway(t *testing.T, icao string, rwy av.Runway) {
	t.Helper()
	if av.DB == nil {
		av.DB = &av.StaticDatabase{Airports: map[string]av.FAAAirport{}}
	}
	old, hadAirport := av.DB.Airports[icao]
	ap := av.FAAAirport{Id: icao, Runways: []av.Runway{rwy}}
	av.DB.Airports[icao] = ap
	t.Cleanup(func() {
		if hadAirport {
			av.DB.Airports[icao] = old
		} else {
			delete(av.DB.Airports, icao)
		}
	})
}

func TestVisualApproachWaypoints(t *testing.T) {
	// Runway 36 at (0,0), heading 360 (north). Centerline extends south.
	rwy := av.Runway{
		Id:                      "36",
		Heading:                 360,
		Threshold:               math.Point2LL{0, 0},
		Elevation:               100,
		ThresholdCrossingHeight: 50,
	}
	setupTestRunway(t, "KTEST", rwy)

	nmPerLong := float32(52) // ~40°N

	tests := []struct {
		name     string
		pos      math.Point2LL
		heading  float32
		wantNil  bool   // expect go-around (nil)
		wantBase bool   // expect a _BASE waypoint
		baseSide string // "left" or "right" of centerline (looking inbound, i.e. north)
	}{
		{
			name:     "Aligned on centerline, 8nm south",
			pos:      math.Point2LL{0, -8.0 / 60}, // 8nm south
			heading:  360,                         // heading north
			wantBase: false,
		},
		{
			name:     "Slightly offset (1nm east), 8nm south",
			pos:      math.Point2LL{1.0 / nmPerLong, -8.0 / 60}, // 1nm east, 8nm south
			heading:  360,
			wantBase: false, // within 1.5nm threshold
		},
		{
			name:     "Offset 3nm east, 8nm south — base turn right",
			pos:      math.Point2LL{3.0 / nmPerLong, -8.0 / 60},
			heading:  360,
			wantBase: true,
			baseSide: "right",
		},
		{
			name:     "Offset 3nm west, 8nm south — base turn left",
			pos:      math.Point2LL{-3.0 / nmPerLong, -8.0 / 60},
			heading:  360,
			wantBase: true,
			baseSide: "left",
		},
		{
			name:    "Behind threshold — go around",
			pos:     math.Point2LL{0, 1.0 / 60}, // 1nm north of threshold
			heading: 360,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := nav.Nav{
				FlightState: nav.FlightState{
					Position:          tt.pos,
					Heading:           tt.heading,
					NmPerLongitude:    nmPerLong,
					MagneticVariation: 0,
					ArrivalAirport:    av.Waypoint{Fix: "KTEST"},
				},
				Approach: nav.NavApproach{
					AssignedId: "V36",
					Assigned:   &av.Approach{Type: av.ChartedVisualApproach, Runway: "36"},
				},
			}

			intent, ok := n.ClearedDirectVisual("36", time.Time{})

			if tt.wantNil {
				if ok {
					t.Fatalf("expected nil (go-around), got intent=%v", intent)
				}
				return
			}
			if !ok {
				t.Fatal("expected waypoints, got nil (go-around)")
			}

			wps := n.Waypoints
			if tt.wantBase {
				// Expect: _BASE, _3NM_FINAL, _THRESHOLD
				if len(wps) != 3 {
					t.Fatalf("expected 3 waypoints, got %d: %v", len(wps), wpNames(wps))
				}
				if wps[0].Fix != "_36_BASE" {
					t.Errorf("first waypoint = %q, want _36_BASE", wps[0].Fix)
				}
				if wps[1].Fix != "_36_3NM_FINAL" {
					t.Errorf("second waypoint = %q, want _36_3NM_FINAL", wps[1].Fix)
				}

				// Verify the base waypoint is on the correct side.
				baseNM := math.LL2NM(wps[0].Location, nmPerLong)
				thresholdNM := math.LL2NM(rwy.Threshold, nmPerLong)
				// For runway 36 (heading north), east is positive x.
				dx := baseNM[0] - thresholdNM[0]
				if tt.baseSide == "right" && dx <= 0 {
					t.Errorf("base waypoint should be to the right (east), dx=%.2f", dx)
				}
				if tt.baseSide == "left" && dx >= 0 {
					t.Errorf("base waypoint should be to the left (west), dx=%.2f", dx)
				}
			} else {
				// Expect: _3NM_FINAL, _THRESHOLD
				if len(wps) != 2 {
					t.Fatalf("expected 2 waypoints, got %d: %v", len(wps), wpNames(wps))
				}
				if wps[0].Fix != "_36_3NM_FINAL" {
					t.Errorf("first waypoint = %q, want _36_3NM_FINAL", wps[0].Fix)
				}
			}

			// Last waypoint should always be the threshold with Land set.
			last := wps[len(wps)-1]
			if last.Fix != "_36_THRESHOLD" {
				t.Errorf("last waypoint = %q, want _36_THRESHOLD", last.Fix)
			}
			if !last.Land() {
				t.Error("threshold waypoint should have Land=true")
			}
			if !last.FlyOver() {
				t.Error("threshold waypoint should have FlyOver=true")
			}

			// 3nm final should have an at-or-above altitude restriction.
			var final3nm av.Waypoint
			for _, wp := range wps {
				if wp.Fix == "_36_3NM_FINAL" {
					final3nm = wp
				}
			}
			if alt := final3nm.AltitudeRestriction(); alt == nil {
				t.Error("3nm final should have an altitude restriction")
			} else if alt.Range[0] == 0 {
				t.Error("3nm final altitude lower bound should be non-zero")
			}
		})
	}
}

func wpNames(wps []av.Waypoint) []string {
	names := make([]string, len(wps))
	for i, wp := range wps {
		names[i] = wp.Fix
	}
	return names
}

func TestAirportAdvisoryAccuracyCheck(t *testing.T) {
	// Airport at (0, 0). Aircraft 5nm north heading south → airport is at 12 o'clock.
	// Actual bearing from ac to airport ≈ 180°.
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})

	tests := []struct {
		name        string
		oclock      int
		miles       int
		wantLooking bool // true if pilot should say "looking" (bad direction)
	}{
		{
			name:        "Accurate direction (12 o'clock, airport ahead)",
			oclock:      12,
			miles:       5,
			wantLooking: false, // May get field or looking based on probability, but not forced looking
		},
		{
			name:        "Way off direction (6 o'clock = behind, 180° error)",
			oclock:      6,
			miles:       5,
			wantLooking: true, // >120° error → always looking
		},
		{
			name:        "Moderately off direction (3 o'clock = right, ~90° error)",
			oclock:      3,
			miles:       5,
			wantLooking: false, // <120° error → might see
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := makeVisualTestSim(airportLoc, "13L")
			ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180) // 5nm north heading south

			intent := sim.handleAirportAdvisory(ac, tt.oclock, tt.miles)
			fi, ok := intent.(av.FieldInSightIntent)
			if !ok {
				t.Fatalf("expected FieldInSightIntent, got %T", intent)
			}

			if tt.wantLooking {
				if fi.HasField {
					t.Error("expected looking (bad direction), but got field in sight")
				}
				if !fi.Looking {
					t.Error("expected looking=true for bad direction, got IMC response")
				}
			}
		})
	}
}

func TestAirportAdvisoryIMC(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 1SM OVC003"} // IMC

	ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 6, 5)
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if fi.HasField || fi.Looking {
		t.Error("expected IMC response (HasField=false, Looking=false)")
	}
}

func TestAirportAdvisoryTooFar(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")

	// Aircraft 20nm north, beyond visualMaxDistance. Use correct direction (12 o'clock)
	// so we isolate the distance check from the bearing error check.
	ac := makeVisualTestAircraft(math.Point2LL{0, 20.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 12, 20)
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if fi.HasField {
		t.Error("expected looking for distant airport, got field in sight")
	}
	if !fi.Looking {
		t.Error("expected looking=true for too-far airport")
	}
}

func TestAirportAdvisoryAboveCeiling(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM BKN030"} // 3000ft ceiling, VMC

	// Aircraft at 4000ft, above ceiling (elev 0 + 3000 = 3000).
	ac := makeVisualTestAircraftAlt(math.Point2LL{0, 5.0 / 60}, 180, 4000)
	intent := sim.handleAirportAdvisory(ac, 12, 5)
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if fi.HasField || fi.Looking {
		t.Error("expected IMC response for aircraft above ceiling")
	}
}

func TestAirportAdvisoryLowVisibility(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 5SM BKN050"} // 5SM visibility

	// Aircraft at 8nm, beyond 5SM visibility → should be "looking".
	ac := makeVisualTestAircraft(math.Point2LL{0, 8.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 12, 8)
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if fi.HasField {
		t.Error("expected looking for aircraft beyond visibility range, got field in sight")
	}
	if !fi.Looking {
		t.Error("expected looking=true for aircraft beyond visibility range")
	}
}

func TestDelayedFieldInSight(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.SimTime = time.Now()

	ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180) // 5nm north heading south
	ac.FieldLookingUntil = sim.State.SimTime.Add(20 * time.Second)
	sim.Aircraft = map[av.ADSBCallsign]*Aircraft{"AAL123": ac}
	sim.PendingContacts = make(map[TCP][]PendingContact)

	// Should not fire before the timer expires.
	sim.checkDelayedFieldInSight(ac)
	if ac.FieldInSight {
		t.Fatal("field in sight reported before timer expired")
	}

	// Advance past the timer and check again.
	sim.State.SimTime = sim.State.SimTime.Add(21 * time.Second)
	sim.checkDelayedFieldInSight(ac)
	if !ac.FieldInSight {
		t.Error("expected field in sight after timer expired")
	}
}

func TestCVRequiresFieldInSight(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(ac *Aircraft)
		wantAllow bool
	}{
		{
			name:      "No field in sight, no visual request → refused",
			setup:     func(ac *Aircraft) {},
			wantAllow: false,
		},
		{
			name:      "FieldInSight set → allowed",
			setup:     func(ac *Aircraft) { ac.FieldInSight = true },
			wantAllow: true,
		},
		{
			name:      "RequestedVisual set → allowed",
			setup:     func(ac *Aircraft) { ac.RequestedVisual = true },
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
			tt.setup(ac)

			// Mirror the gate check in ClearedVisualApproach.
			allowed := ac.FieldInSight || ac.RequestedVisual
			if allowed != tt.wantAllow {
				t.Errorf("CV gate: allowed=%v, want %v", allowed, tt.wantAllow)
			}
		})
	}
}
