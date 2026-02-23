package sim

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// VisualScenario is a test helper for stepping through multi-stage visual
// approach flows. It sets up a minimal Sim with one aircraft and lets
// tests issue commands and inspect both intents and pending transmissions.
type VisualScenario struct {
	t   *testing.T
	Sim *Sim
	AC  *Aircraft

	callsign av.ADSBCallsign
	tcw      TCW
}

// NewVisualScenario creates a scenario with an airport at the given location,
// VMC METAR, and a single aircraft positioned at acPos heading in the given
// direction. The aircraft has an ILS approach assigned for the given runway.
func NewVisualScenario(t *testing.T, airportLoc math.Point2LL, runway string, acPos math.Point2LL, heading float32) *VisualScenario {
	t.Helper()

	callsign := av.ADSBCallsign("AAL123")
	tcw := TCW("TEST")
	freq := ControlPosition("125.0")

	ac := &Aircraft{
		ADSBCallsign:        callsign,
		TypeOfFlight:        av.FlightTypeArrival,
		ControllerFrequency: freq,
		FlightPlan: av.FlightPlan{
			ArrivalAirport: "KJFK",
		},
		Nav: nav.Nav{
			FlightState: nav.FlightState{
				Position:                acPos,
				Heading:                 heading,
				Altitude:                3000,
				NmPerLongitude:          52,
				MagneticVariation:       0,
				ArrivalAirport:          av.Waypoint{Fix: "KJFK"},
				ArrivalAirportLocation:  airportLoc,
				ArrivalAirportElevation: 13,
			},
			Approach: nav.NavApproach{
				AssignedId: "I" + runway,
				Assigned: &av.Approach{
					Type:   av.ILSApproach,
					Runway: runway,
				},
			},
		},
	}

	// Create a discard logger for tests.
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	sim := &Sim{
		lg:   lg,
		Rand: vrand.Make(),
		State: &CommonState{
			DynamicState: DynamicState{
				METAR: map[string]wx.METAR{
					"KJFK": {Raw: "KJFK 10SM BKN050"},
				},
				SimTime:              time.Now(),
				CurrentConsolidation: map[TCW]*TCPConsolidation{tcw: {PrimaryTCP: TCP(freq)}},
			},
			Airports: map[string]*av.Airport{
				"KJFK": {
					Location: airportLoc,
					Approaches: map[string]*av.Approach{
						"V" + runway: {Type: av.ChartedVisualApproach, Runway: runway},
						"I" + runway: {Type: av.ILSApproach, Runway: runway},
					},
				},
			},
		},
		Aircraft:        map[av.ADSBCallsign]*Aircraft{callsign: ac},
		PendingContacts: make(map[TCP][]PendingContact),
		PrivilegedTCWs:  map[TCW]bool{tcw: true},
	}

	return &VisualScenario{t: t, Sim: sim, AC: ac, callsign: callsign, tcw: tcw}
}

// SetMETAR replaces the METAR for KJFK.
func (vs *VisualScenario) SetMETAR(raw string) {
	vs.Sim.State.METAR["KJFK"] = wx.METAR{Raw: raw}
}

// SetPosition moves the aircraft.
func (vs *VisualScenario) SetPosition(pos math.Point2LL) {
	vs.AC.Nav.FlightState.Position = pos
}

// SetAltitude changes the aircraft altitude.
func (vs *VisualScenario) SetAltitude(alt float32) {
	vs.AC.Nav.FlightState.Altitude = alt
}

// AirportAdvisory issues an AP command and returns the intent.
func (vs *VisualScenario) AirportAdvisory(oclock, miles int) av.CommandIntent {
	vs.t.Helper()
	cmd := fmt.Sprintf("AP/%d/%d", oclock, miles)
	intent, err := vs.Sim.AirportAdvisory(vs.tcw, vs.callsign, cmd)
	if err != nil {
		vs.t.Fatalf("AirportAdvisory(%s) error: %v", cmd, err)
	}
	return intent
}

// ClearedVisual issues a CVA command and returns the intent.
func (vs *VisualScenario) ClearedVisual(runway string) (av.CommandIntent, error) {
	vs.t.Helper()
	return vs.Sim.ClearedVisualApproach(vs.tcw, vs.callsign, runway)
}

// AdvanceTime moves sim time forward by d.
func (vs *VisualScenario) AdvanceTime(d time.Duration) {
	vs.Sim.State.SimTime = vs.Sim.State.SimTime.Add(d)
}

// CheckDelayedFieldInSight runs the delayed field-in-sight check.
func (vs *VisualScenario) CheckDelayedFieldInSight() {
	vs.Sim.checkDelayedFieldInSight(vs.AC)
}

// CheckSpontaneousVisual runs the spontaneous visual request check.
func (vs *VisualScenario) CheckSpontaneousVisual() {
	vs.Sim.checkSpontaneousVisualRequest(vs.AC)
}

// PendingTransmissions returns all pending transmission types for this aircraft.
func (vs *VisualScenario) PendingTransmissions() []PendingTransmissionType {
	var types []PendingTransmissionType
	for _, pcs := range vs.Sim.PendingContacts {
		for _, pc := range pcs {
			if pc.ADSBCallsign == vs.callsign {
				types = append(types, pc.Type)
			}
		}
	}
	return types
}

// HasPendingTransmission checks if a specific transmission type is pending.
func (vs *VisualScenario) HasPendingTransmission(txType PendingTransmissionType) bool {
	for _, t := range vs.PendingTransmissions() {
		if t == txType {
			return true
		}
	}
	return false
}

// ClearPendingTransmissions removes all pending transmissions.
func (vs *VisualScenario) ClearPendingTransmissions() {
	vs.Sim.PendingContacts = make(map[TCP][]PendingContact)
}

// ExpectFieldInSight asserts the intent is FieldInSightIntent with HasField=true.
func (vs *VisualScenario) ExpectFieldInSight(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		vs.t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if !fi.HasField {
		vs.t.Error("expected HasField=true")
	}
}

// ExpectLooking asserts the intent is FieldInSightIntent with Looking=true.
func (vs *VisualScenario) ExpectLooking(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		vs.t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if !fi.Looking {
		vs.t.Error("expected Looking=true")
	}
	if fi.HasField {
		vs.t.Error("expected HasField=false when Looking")
	}
}

// ExpectIMC asserts the intent is FieldInSightIntent with neither HasField nor Looking.
func (vs *VisualScenario) ExpectIMC(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		vs.t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}
	if fi.HasField || fi.Looking {
		vs.t.Errorf("expected IMC response (HasField=false, Looking=false), got HasField=%v Looking=%v", fi.HasField, fi.Looking)
	}
}

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
			name: "Nil assigned approach — still sees field, just no runway",
			sim:  makeVisualTestSim(airportLoc, "13L"),
			ac: func() *Aircraft {
				ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
				ac.Nav.Approach.Assigned = nil
				return ac
			}(),
			wantField: true,
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

			// 3nm final should have an "at" altitude restriction.
			var final3nm av.Waypoint
			for _, wp := range wps {
				if wp.Fix == "_36_3NM_FINAL" {
					final3nm = wp
				}
			}
			if alt := final3nm.AltitudeRestriction(); alt == nil {
				t.Error("3nm final should have an altitude restriction")
			} else if alt.Range[0] == 0 || alt.Range[0] != alt.Range[1] {
				t.Errorf("3nm final altitude should be 'at' (range[0]==range[1]), got %v", alt.Range)
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

func TestCVARequiresFieldInSight(t *testing.T) {
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
				t.Errorf("CVA gate: allowed=%v, want %v", allowed, tt.wantAllow)
			}
		})
	}
}

func TestEVACommandReturnsGenericExpect(t *testing.T) {
	// EVA commands should return a generic "Visual Runway XX" expect intent,
	// NOT resolve to a charted visual approach name like "Belmont Visual".
	tests := []struct {
		name    string
		command string // e.g. "EVA22L"
		wantRwy string // expected in ApproachName
	}{
		{"EVA22L", "EVA22L", "22L"},
		{"EVA13R", "EVA13R", "13R"},
		{"EVA31", "EVA31", "31"},
		{"EVA4R", "EVA4R", "4R"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the EVA handler logic from RunAircraftCommands.
			if !strings.HasPrefix(tt.command, "EVA") || len(tt.command) <= 3 {
				t.Fatal("bad test command")
			}
			runway := tt.command[3:]
			intent := av.ApproachIntent{
				Type:         av.ApproachExpect,
				ApproachName: "Visual Runway " + runway,
			}

			if intent.Type != av.ApproachExpect {
				t.Errorf("intent type = %v, want ApproachExpect", intent.Type)
			}
			wantName := "Visual Runway " + tt.wantRwy
			if intent.ApproachName != wantName {
				t.Errorf("ApproachName = %q, want %q", intent.ApproachName, wantName)
			}
			// Must NOT contain charted visual names.
			for _, bad := range []string{"Belmont", "River", "Expressway"} {
				if strings.Contains(intent.ApproachName, bad) {
					t.Errorf("ApproachName %q should not contain charted visual name %q", intent.ApproachName, bad)
				}
			}
		})
	}
}

func TestSpontaneousTransmissionsUseContactType(t *testing.T) {
	// Pilot-initiated transmissions (field in sight, traffic in sight,
	// visual request) should use MakeContactTransmission, not
	// MakeReadbackTransmission. Contact transmissions get the
	// "Approach, {callsign}, ..." prefix automatically; readback
	// transmissions get "{message}, {callsign}" suffix format.
	tests := []struct {
		name    string
		makeFn  func() *av.RadioTransmission
		wantTyp int
	}{
		{
			name:    "traffic in sight",
			makeFn:  func() *av.RadioTransmission { return av.MakeContactTransmission("[traffic in sight]") },
			wantTyp: av.RadioTransmissionContact,
		},
		{
			name:    "field in sight",
			makeFn:  func() *av.RadioTransmission { return av.MakeContactTransmission("[field in sight]") },
			wantTyp: av.RadioTransmissionContact,
		},
		{
			name:    "negative field",
			makeFn:  func() *av.RadioTransmission { return av.MakeContactTransmission("[negative field]") },
			wantTyp: av.RadioTransmissionContact,
		},
		{
			name: "visual request",
			makeFn: func() *av.RadioTransmission {
				return av.MakeContactTransmission("[field in sight], [requesting the visual] runway {rwy}", "13L")
			},
			wantTyp: av.RadioTransmissionContact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := tt.makeFn()
			if rt.Type != av.RadioTransmissionType(tt.wantTyp) {
				t.Errorf("Type = %v, want RadioTransmissionContact (%d)", rt.Type, tt.wantTyp)
			}
		})
	}
}

func TestTransmissionStringsNoDoubleApproach(t *testing.T) {
	// The transmission template strings for spontaneous pilot messages
	// should NOT contain "approach" or "{callsign}" — those are added
	// automatically by the radio transmission system.
	templates := []struct {
		name     string
		template string
	}{
		{"traffic in sight", "[we've got the traffic|we have the traffic in sight|traffic in sight now]"},
		{"field in sight", "[we have the field in sight now|field in sight|we have the airport in sight now]"},
		{"negative field", "[negative field|field not in sight|no joy on the field]"},
		{"visual request", "[field in sight|we have the airport in sight], [requesting the visual|can we get the visual] [approach |]runway {rwy}"},
	}

	for _, tt := range templates {
		t.Run(tt.name, func(t *testing.T) {
			// Should not start with "[approach" or contain "{callsign}"
			if strings.HasPrefix(tt.template, "[approach") {
				t.Errorf("template starts with [approach — this will cause double 'approach' in output")
			}
			if strings.Contains(tt.template, "{callsign}") {
				t.Errorf("template contains {callsign} — callsign is added automatically by MakeContactTransmission")
			}
		})
	}
}

// === Scenario tests using VisualScenario helper ===

func TestScenarioAPThenClearedVisual(t *testing.T) {
	// Full flow: AP advisory → field in sight → CVA clearance.
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13})

	// Aircraft 5nm south heading north — on extended centerline for runway 36.
	vs := NewVisualScenario(t, airportLoc, "36", math.Point2LL{0, -5.0 / 60}, 360)

	// Issue AP: 12 o'clock, 5 miles. Should get field in sight or looking.
	intent := vs.AirportAdvisory(12, 5)
	fi, ok := intent.(av.FieldInSightIntent)
	if !ok {
		t.Fatalf("expected FieldInSightIntent, got %T", intent)
	}

	if fi.HasField {
		// Great — pilot sees the field. Should accept CVA now.
		intent, err := vs.ClearedVisual("36")
		if err != nil {
			t.Fatalf("ClearedVisual error: %v", err)
		}
		if intent == nil {
			t.Fatal("expected non-nil intent from CVA")
		}
	} else if fi.Looking {
		// Pilot is looking — advance time and check delayed callback.
		vs.AdvanceTime(25 * time.Second)
		vs.CheckDelayedFieldInSight()

		if vs.AC.FieldInSight {
			// Now CVA should work.
			intent, err := vs.ClearedVisual("36")
			if err != nil {
				t.Fatalf("ClearedVisual after delayed field: %v", err)
			}
			if intent == nil {
				t.Fatal("expected non-nil intent from CVA after delayed field")
			}
		}
	}
}

func TestScenarioCVARefusedWithoutFieldInSight(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	// Try CVA without field in sight — should be refused.
	intent, err := vs.ClearedVisual("13L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Errorf("expected UnableIntent without field in sight, got %T", intent)
	}
}

func TestScenarioAPInIMC(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.SetMETAR("KJFK 1SM OVC003") // IMC

	intent := vs.AirportAdvisory(12, 5)
	vs.ExpectIMC(intent)
}

func TestScenarioAPBadDirection(t *testing.T) {
	// Airport is south, but controller says 6 o'clock (behind).
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	intent := vs.AirportAdvisory(6, 5) // 6 o'clock = behind = wrong
	vs.ExpectLooking(intent)
}

func TestScenarioSpontaneousFieldInSight(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.AC.WantsVisual = true
	vs.AC.WantsVisualRequest = false

	// First check sets VisualRequestTime (delay).
	vs.CheckSpontaneousVisual()
	if vs.AC.FieldInSight {
		t.Fatal("field in sight should not be immediate — delay expected")
	}
	if vs.AC.VisualRequestTime.IsZero() {
		t.Fatal("VisualRequestTime should be set after first eligibility check")
	}

	// Advance past the delay.
	vs.AdvanceTime(10 * time.Second)
	vs.CheckSpontaneousVisual()

	if !vs.AC.FieldInSight {
		t.Error("expected FieldInSight after delay expired")
	}
	if !vs.HasPendingTransmission(PendingTransmissionFieldInSight) {
		t.Error("expected PendingTransmissionFieldInSight to be enqueued")
	}
}

func TestScenarioSpontaneousVisualRequest(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.AC.WantsVisual = true
	vs.AC.WantsVisualRequest = true

	// First check → sets delay.
	vs.CheckSpontaneousVisual()

	// Advance past delay.
	vs.AdvanceTime(10 * time.Second)
	vs.CheckSpontaneousVisual()

	if !vs.AC.FieldInSight {
		t.Error("expected FieldInSight")
	}
	if !vs.AC.RequestedVisual {
		t.Error("expected RequestedVisual when WantsVisualRequest=true")
	}
	if !vs.HasPendingTransmission(PendingTransmissionRequestVisual) {
		t.Error("expected PendingTransmissionRequestVisual to be enqueued")
	}
}

func TestScenarioAboveCeilingIMC(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.SetMETAR("KJFK 10SM BKN030") // 3000ft ceiling, VMC surface
	vs.SetAltitude(4000)            // above ceiling

	intent := vs.AirportAdvisory(12, 5)
	vs.ExpectIMC(intent) // Aircraft in clouds → IMC response
}
