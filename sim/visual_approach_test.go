package sim

import (
	"io"
	"log/slog"
	"slices"
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

// testApproachWaypoints builds a minimal straight-in reference route for the
// given runway: a 25nm extended-final waypoint back along the runway
// reciprocal, then the threshold. The runway must already be registered via
// setupTestRunway.
func testApproachWaypoints(airport, runway string, airportLoc math.Point2LL, nmPerLong float32) []av.WaypointArray {
	rwy, ok := av.LookupRunway(airport, runway)
	if !ok {
		return []av.WaypointArray{{{Fix: "RW" + runway, Location: airportLoc}}}
	}
	rwyTrue := math.MagneticToTrue(rwy.Heading, 0)
	reciprocal := math.TrueHeading(math.NormalizeHeading(float32(rwyTrue) + 180))
	extendedFinal := math.Offset2LL(airportLoc, reciprocal, 25, nmPerLong)
	return []av.WaypointArray{{
		{Fix: "FAF" + runway, Location: extendedFinal},
		{Fix: "RW" + runway, Location: airportLoc},
	}}
}

// NewVisualScenario creates a scenario with an airport at the given location,
// VMC METAR, and a single aircraft positioned at acPos heading in the given
// direction. The aircraft has an ILS approach assigned for the given runway.
func NewVisualScenario(t *testing.T, airportLoc math.Point2LL, runway string, acPos math.Point2LL, heading math.MagneticHeading) *VisualScenario {
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
				SimTime:              NewSimTime(time.Now()),
				CurrentConsolidation: map[TCW]*TCPConsolidation{tcw: {PrimaryTCP: TCP(freq)}},
			},
			Airports: map[string]*av.Airport{
				"KJFK": {
					Location: airportLoc,
					Approaches: map[string]*av.Approach{
						"V" + runway: {Type: av.ChartedVisualApproach, Runway: runway, Waypoints: testApproachWaypoints("KJFK", runway, airportLoc, 52)},
						"I" + runway: {Type: av.ILSApproach, Runway: runway, Waypoints: testApproachWaypoints("KJFK", runway, airportLoc, 52)},
					},
				},
			},
		},
		Aircraft:        map[av.ADSBCallsign]*Aircraft{callsign: ac},
		PendingContacts: make(map[TCP][]PendingContact),
		PrivilegedTCWs:  map[TCW]bool{tcw: true},
		eventStream:     NewEventStream(lg),
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
	intent, err := vs.Sim.AirportAdvisory(vs.tcw, vs.callsign, oclock, miles)
	if err != nil {
		vs.t.Fatalf("AirportAdvisory(%d,%d) error: %v", oclock, miles, err)
	}
	return intent
}

// ClearedVisual issues a CVA command and returns the intent.
func (vs *VisualScenario) ClearedVisual(runway string) (av.CommandIntent, error) {
	vs.t.Helper()
	return vs.Sim.ClearedVisualApproach(vs.tcw, vs.callsign, runway, "")
}

// AdvanceTime moves sim time forward by d.
func (vs *VisualScenario) AdvanceTime(d time.Duration) {
	vs.Sim.State.SimTime = vs.Sim.State.SimTime.Add(d)
}

// CheckDelayedFieldInSight runs the delayed field-in-sight processor.
func (vs *VisualScenario) CheckDelayedFieldInSight() {
	vs.Sim.processFutureFieldInSight()
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

func requireSeenTraffic(t *testing.T, ac *Aircraft, callsign av.ADSBCallsign) *SeenAircraft {
	t.Helper()
	for i := range ac.SeenTraffic {
		if ac.SeenTraffic[i].Callsign == callsign {
			return &ac.SeenTraffic[i]
		}
	}
	t.Fatalf("missing sighting for %s", callsign)
	return nil
}

// ExpectFieldInSight asserts the intent is LookForFieldFound.
func (vs *VisualScenario) ExpectFieldInSight(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		vs.t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldFound {
		vs.t.Errorf("expected LookForFieldFound, got %v", fi)
	}
}

// ExpectLooking asserts the intent is LookForFieldLooking.
func (vs *VisualScenario) ExpectLooking(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		vs.t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLooking {
		vs.t.Errorf("expected LookForFieldLooking, got %v", fi)
	}
}

// ExpectIMC asserts the intent is LookForFieldLookingIMC.
func (vs *VisualScenario) ExpectIMC(intent av.CommandIntent) {
	vs.t.Helper()
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		vs.t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLookingIMC {
		vs.t.Errorf("expected LookForFieldLookingIMC, got %v", fi)
	}
}

// makeVisualTestAircraft creates an arrival aircraft with an ILS approach
// assigned, on frequency, positioned at the given location heading in the
// given direction. Suitable for canRequestVisualApproach and
// checkSpontaneousVisualRequest tests.
func makeVisualTestAircraft(pos math.Point2LL, heading math.MagneticHeading) *Aircraft {
	return makeVisualTestAircraftAlt(pos, heading, 3000) // default 3000ft MSL
}

func makeVisualTestAircraftAlt(pos math.Point2LL, heading math.MagneticHeading, altitude float32) *Aircraft {
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
						"V13L": {Type: av.ChartedVisualApproach, Runway: runway, Waypoints: testApproachWaypoints("KJFK", runway, airportLoc, 52)},
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
			name:      "Too far from airport (30nm)",
			sim:       makeVisualTestSim(airportLoc, "13L"),
			ac:        makeVisualTestAircraft(math.Point2LL{0, 30.0 / 60}, 180),
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
				ac.RequestedVisualApproach = true
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
		heading            math.MagneticHeading
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
			bearing := math.TrueToMagnetic(math.Heading2LL(acPos, airportLoc, nmPerLong), 0)
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
	setupTestRunways(t, icao, []av.Runway{rwy})
}

func setupTestRunways(t *testing.T, icao string, runways []av.Runway) {
	t.Helper()
	if av.DB == nil {
		av.DB = &av.StaticDatabase{Airports: map[string]av.FAAAirport{}}
	}
	old, hadAirport := av.DB.Airports[icao]
	ap := av.FAAAirport{Id: icao, Runways: runways}
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

	// Test positions sit near lat=0, so nmPerLong must match
	// math.NMPerLongitudeAt(pos) ≈ 60 for the ray/route helpers to project
	// coordinates consistently with the rest of the nav pipeline.
	nmPerLong := float32(60)

	reference := &av.Approach{
		Type:      av.ILSApproach,
		Runway:    "36",
		Threshold: rwy.Threshold,
		Waypoints: []av.WaypointArray{{
			{Fix: "FAF36", Location: math.Point2LL{0, -25.0 / 60}},
			{Fix: "_36_THRESHOLD", Location: rwy.Threshold},
		}},
	}

	tests := []struct {
		name         string
		pos          math.Point2LL
		heading      math.MagneticHeading
		assigned     *math.MagneticHeading
		wantNil      bool // expect go-around (nil)
		wantFirstFix string
	}{
		{
			name:         "Aligned on centerline, 8nm south",
			pos:          math.Point2LL{0, -8.0 / 60}, // 8nm south
			heading:      360,                         // heading north
			wantFirstFix: "_36_3NM_FINAL",
		},
		{
			name:         "Slightly offset (1nm east), 8nm south",
			pos:          math.Point2LL{1.0 / nmPerLong, -8.0 / 60}, // 1nm east, 8nm south
			heading:      360,
			wantFirstFix: "_36_3NM_FINAL",
		},
		{
			name:         "Offset 3nm east, pointed at field — FAF first",
			pos:          math.Point2LL{3.0 / nmPerLong, -8.0 / 60},
			heading:      360,
			wantFirstFix: "_36_3NM_FINAL",
		},
		{
			name:         "Offset 3nm west, pointed away from field — project to final",
			pos:          math.Point2LL{-3.0 / nmPerLong, -8.0 / 60},
			heading:      180,
			wantFirstFix: "_36_PROJECTION",
		},
		{
			name:         "Assigned heading intercepts outside FAF — intercept assigned heading",
			pos:          math.Point2LL{3.0 / nmPerLong, -8.0 / 60},
			heading:      360,
			assigned:     ptr(math.MagneticHeading(315)),
			wantFirstFix: "_36_INTERCEPT",
		},
		{
			name:         "Assigned heading intercepts inside FAF — FAF first",
			pos:          math.Point2LL{3.0 / nmPerLong, -5.0 / 60},
			heading:      360,
			assigned:     ptr(math.MagneticHeading(315)),
			wantFirstFix: "_36_3NM_FINAL",
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
			if tt.assigned != nil {
				n.Heading.Assigned = tt.assigned
			}

			intent := n.ClearedVisualApproach("36", nil, []*av.Approach{reference}, "", nav.Time{})
			if tt.wantNil {
				if _, unable := intent.(av.UnableIntent); !unable {
					t.Fatalf("expected UnableIntent, got %T: %v", intent, intent)
				}
				return
			}
			if _, unable := intent.(av.UnableIntent); unable {
				t.Fatalf("unexpected UnableIntent: %v", intent)
			}

			wps := n.Waypoints
			if len(wps) < 2 {
				t.Fatalf("expected at least 2 waypoints, got %d: %v", len(wps), wpNames(wps))
			}
			if wps[0].Fix != tt.wantFirstFix {
				t.Errorf("first waypoint = %q, want %q; all waypoints: %v", wps[0].Fix, tt.wantFirstFix, wpNames(wps))
			}
			if tt.wantFirstFix != "_36_3NM_FINAL" && wps[1].Fix != "_36_3NM_FINAL" {
				t.Errorf("second waypoint = %q, want _36_3NM_FINAL; all waypoints: %v", wps[1].Fix, wpNames(wps))
			}

			if tt.wantFirstFix == "_36_PROJECTION" {
				projectionNM := math.LL2NM(wps[0].Location, nmPerLong)
				if math.Abs(projectionNM[0]) > 0.05 {
					t.Errorf("projection waypoint should be on centerline, x=%.2f", projectionNM[0])
				}
			}
			if tt.wantFirstFix == "_36_INTERCEPT" {
				interceptNM := math.LL2NM(wps[0].Location, nmPerLong)
				if math.Abs(interceptNM[0]) > 0.05 {
					t.Errorf("intercept waypoint should be on centerline, x=%.2f", interceptNM[0])
				}
				bearingToIntercept := math.Heading2LL(tt.pos, wps[0].Location, nmPerLong)
				if math.HeadingDifference(bearingToIntercept, math.MagneticToTrue(*tt.assigned, 0)) > 1 {
					t.Errorf("intercept should be on assigned heading, bearing %.1f heading %.1f", bearingToIntercept, *tt.assigned)
				}
			}

			// The threshold (with Land set) should be second-to-last,
			// followed by the arrival airport.
			if got := wps[len(wps)-1].Fix; got != "KTEST" {
				t.Errorf("last waypoint = %q, want arrival airport KTEST", got)
			}
			threshold := wps[len(wps)-2]
			if threshold.Fix != "_36_THRESHOLD" {
				t.Errorf("penultimate waypoint = %q, want _36_THRESHOLD", threshold.Fix)
			}
			if !threshold.Land() {
				t.Error("threshold waypoint should have Land=true")
			}
			if !threshold.FlyOver() {
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

func TestVisualApproachWaypointsUseReferenceApproachDogleg(t *testing.T) {
	rwy := av.Runway{
		Id:                      "36",
		Heading:                 360,
		Threshold:               math.Point2LL{0, 0},
		Elevation:               100,
		ThresholdCrossingHeight: 50,
	}
	setupTestRunway(t, "KTEST", rwy)

	nmPerLong := float32(60)
	reference := &av.Approach{
		Type:      av.VORApproach,
		Runway:    "36",
		Threshold: rwy.Threshold,
		Waypoints: []av.WaypointArray{{
			{Fix: "ASALT", Location: math.NM2LL([2]float32{-6, -8}, nmPerLong)},
			{Fix: "ZADUD", Location: math.NM2LL([2]float32{-6, -3}, nmPerLong)},
			{Fix: "WIRKO", Location: math.NM2LL([2]float32{-2, -3}, nmPerLong)},
			{Fix: "_36_THRESHOLD", Location: rwy.Threshold},
		}},
	}

	n := nav.Nav{
		FlightState: nav.FlightState{
			Position:          math.NM2LL([2]float32{-8, -5}, nmPerLong),
			Heading:           180,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: 0,
			ArrivalAirport:    av.Waypoint{Fix: "KTEST"},
		},
	}

	_ = n.ClearedVisualApproach("36", nil, []*av.Approach{reference}, "", nav.Time{})

	if len(n.Waypoints) < 5 {
		t.Fatalf("expected projection, intermediate dogleg fixes, 3nm final, and threshold; got %v", wpNames(n.Waypoints))
	}
	if n.Waypoints[0].Fix != "_36_PROJECTION" {
		t.Fatalf("first waypoint = %q, want projection; route %v", n.Waypoints[0].Fix, wpNames(n.Waypoints))
	}
	projectionNM := math.LL2NM(n.Waypoints[0].Location, nmPerLong)
	if math.Abs(projectionNM[0]-(-6)) > 0.05 || math.Abs(projectionNM[1]-(-5)) > 0.05 {
		t.Fatalf("projection = %.2f, %.2f; want near -6, -5", projectionNM[0], projectionNM[1])
	}
	if n.Waypoints[1].Fix != "ZADUD" || n.Waypoints[2].Fix != "WIRKO" {
		t.Fatalf("expected dogleg fixes after projection, got %v", wpNames(n.Waypoints))
	}
	finalIdx := slices.IndexFunc(n.Waypoints, func(wp av.Waypoint) bool { return wp.Fix == "_36_3NM_FINAL" })
	if finalIdx == -1 {
		t.Fatalf("missing route-equivalent 3nm final in %v", wpNames(n.Waypoints))
	}
	finalNM := math.LL2NM(n.Waypoints[finalIdx].Location, nmPerLong)
	if math.Abs(finalNM[0]-(-1.66)) > 0.1 || math.Abs(finalNM[1]-(-2.50)) > 0.1 {
		t.Fatalf("3nm final = %.2f, %.2f; want a point along the WIRKO-threshold segment, not runway centerline",
			finalNM[0], finalNM[1])
	}
}

func TestVisualReferenceApproachSelection(t *testing.T) {
	ac := &Aircraft{
		FlightPlan: av.FlightPlan{ArrivalAirport: "KTEST"},
		Nav: nav.Nav{Approach: nav.NavApproach{
			Assigned: &av.Approach{Type: av.RNAVApproach, Runway: "36", Waypoints: []av.WaypointArray{{{}, {}}}},
		}},
	}
	s := &Sim{State: &CommonState{Airports: map[string]*av.Airport{"KTEST": {
		Approaches: map[string]*av.Approach{
			"R36": {Type: av.RNAVApproach, Runway: "36", Waypoints: []av.WaypointArray{{{}, {}}}},
			"V36": {Type: av.VORApproach, Runway: "36", Waypoints: []av.WaypointArray{{{}, {}}}},
			"I35": {Type: av.ILSApproach, Runway: "35", Waypoints: []av.WaypointArray{{{}, {}}}},
		},
	}}}}

	if got := s.visualReferenceApproaches(ac, "36", nil); len(got) != 1 || got[0] != ac.Nav.Approach.Assigned {
		t.Fatalf("assigned matching approach should win, got %+v", got)
	}

	ac.Nav.Approach.Assigned = nil
	if got := s.visualReferenceApproaches(ac, "36", nil); len(got) != 1 || got[0].Type != av.VORApproach {
		t.Fatalf("fallback approach type = %v, want VOR", got)
	}
}

func wpNames(wps []av.Waypoint) []string {
	names := make([]string, len(wps))
	for i, wp := range wps {
		names[i] = wp.Fix
	}
	return names
}

func ptr[T any](v T) *T {
	return &v
}

func TestVisualApproachFollowingTrafficTurnsBase(t *testing.T) {
	// Runway 36 at (0,0), heading 360 (north). Centerline extends south.
	setupTestRunway(t, "KTEST", av.Runway{
		Id:                      "36",
		Heading:                 360,
		Threshold:               math.Point2LL{0, 0},
		Elevation:               100,
		ThresholdCrossingHeight: 50,
	})

	nmPerLong := float32(52)
	acPos := math.Point2LL{3.0 / nmPerLong, -8.0 / 60} // east of final, south of the threshold
	trafficPos := math.Point2LL{0, -5.0 / 60}          // traffic already established on final

	n := nav.Nav{
		FlightState: nav.FlightState{
			Position:          acPos,
			Heading:           360,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: 0,
			ArrivalAirport:    av.Waypoint{Fix: "KTEST"},
		},
		Approach: nav.NavApproach{
			AssignedId: "V36",
			Assigned:   &av.Approach{Type: av.ChartedVisualApproach, Runway: "36"},
		},
	}

	reference := &av.Approach{
		Type:      av.ILSApproach,
		Runway:    "36",
		Threshold: math.Point2LL{0, 0},
		Waypoints: []av.WaypointArray{{
			{Fix: "FAF36", Location: math.Point2LL{0, -25.0 / 60}},
			{Fix: "RW36", Location: math.Point2LL{0, 0}},
		}},
	}
	_ = n.ClearedVisualApproach("36", &nav.FollowTraffic{Position: trafficPos}, []*av.Approach{reference}, "", nav.Time{})

	wps := n.Waypoints
	if len(wps) != 4 {
		t.Fatalf("expected follow traffic, 3nm final, threshold, airport; got %d: %v", len(wps), wpNames(wps))
	}
	if wps[0].Fix != "_36_FOLLOW_TRAFFIC" {
		t.Fatalf("first waypoint = %q, want _36_FOLLOW_TRAFFIC", wps[0].Fix)
	}

	joinNM := math.LL2NM(wps[0].Location, nmPerLong)
	if math.Abs(joinNM[0]) > 0.05 {
		t.Errorf("follow-traffic waypoint should be on centerline, x=%.2f", joinNM[0])
	}

	bearingToJoin := math.Heading2LL(acPos, wps[0].Location, nmPerLong)
	if math.HeadingDifference(bearingToJoin, math.TrueHeading(315)) > 1 {
		t.Errorf("follow-traffic route should turn base toward traffic; bearing %.1f", bearingToJoin)
	}
}

func TestScenarioCVAFollowTrafficUsesTrafficRoute(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "36", math.Point2LL{3.0 / 52, -8.0 / 60}, 360)
	vs.AC.Nav.Approach.Assigned = &av.Approach{
		Type:   av.ILSApproach,
		Runway: "36",
		Waypoints: []av.WaypointArray{{
			{Fix: "ILS36", Location: math.NM2LL([2]float32{0, -10}, 52)},
			{Fix: "RW36", Location: airportLoc},
		}},
	}

	trafficPos := math.NM2LL([2]float32{-4, -4.2}, 52)
	traffic := makeVisualTestAircraft(trafficPos, 360)
	traffic.ADSBCallsign = "AAL5207"
	traffic.Nav.FlightState.ArrivalAirport = av.Waypoint{Fix: "KJFK"}
	traffic.Nav.Approach.Cleared = true
	zadud := av.Waypoint{Fix: "ZADUD", Location: math.NM2LL([2]float32{-6, -5}, 52)}
	wirko := av.Waypoint{Fix: "WIRKO", Location: math.NM2LL([2]float32{-2, -2.5}, 52)}
	rw36 := av.Waypoint{Fix: "RW36", Location: airportLoc}
	traffic.Nav.Approach.Assigned = &av.Approach{
		Type:   av.RNAVApproach,
		Runway: "36",
		Waypoints: []av.WaypointArray{{
			zadud,
			wirko,
			rw36,
		}},
	}
	rw36.SetLand(true)
	traffic.Nav.Waypoints = av.WaypointArray{wirko, rw36, traffic.Nav.FlightState.ArrivalAirport}
	vs.Sim.Aircraft[traffic.ADSBCallsign] = traffic

	vs.AC.RecordSighting(traffic.ADSBCallsign, vs.Sim.State.SimTime)

	intent, err := vs.ClearedVisual("36")
	if err != nil {
		t.Fatalf("ClearedVisual error: %v", err)
	}
	if _, ok := intent.(av.ClearedApproachIntent); !ok {
		t.Fatalf("expected ClearedApproachIntent, got %T", intent)
	}
	if len(vs.AC.Nav.Waypoints) == 0 || vs.AC.Nav.Waypoints[0].Fix != "_36_FOLLOW_TRAFFIC" {
		t.Fatalf("expected follow-traffic route, got %v", wpNames(vs.AC.Nav.Waypoints))
	}
	if d := math.NMDistance2LLFast(vs.AC.Nav.Waypoints[0].Location, trafficPos, 52); d > 0.01 {
		t.Fatalf("follow-traffic waypoint %.2fnm from traffic position", d)
	}
	if got := wpNames(vs.AC.Nav.Waypoints); !slices.Equal(got, []string{"_36_FOLLOW_TRAFFIC", "WIRKO", "RW36", "KJFK"}) {
		t.Fatalf("route = %v, want traffic position followed by traffic's remaining route", got)
	}
}

func TestVisualApproachFollowingTrafficCopiesRemainingTrafficRoute(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KTEST", av.Runway{
		Id:                      "36",
		Heading:                 360,
		Threshold:               airportLoc,
		Elevation:               100,
		ThresholdCrossingHeight: 50,
	})

	nmPerLong := float32(52)
	trafficPos := math.NM2LL([2]float32{0, -4}, nmPerLong)
	final3NM := av.Waypoint{Fix: "_36_3NM_FINAL", Location: math.NM2LL([2]float32{0, -3}, nmPerLong)}
	threshold := av.Waypoint{Fix: "RW36", Location: airportLoc}

	n := nav.Nav{
		FlightState: nav.FlightState{
			Position:          math.NM2LL([2]float32{3, -8}, nmPerLong),
			Heading:           360,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: 0,
			ArrivalAirport:    av.Waypoint{Fix: "KTEST"},
		},
	}
	threshold.SetLand(true)
	trafficRoute := av.WaypointArray{final3NM, threshold, n.FlightState.ArrivalAirport}
	_ = n.ClearedVisualApproach("36", &nav.FollowTraffic{Position: trafficPos, Route: trafficRoute}, nil, "", nav.Time{})
	if got := wpNames(n.Waypoints); !slices.Equal(got, []string{"_36_FOLLOW_TRAFFIC", "_36_3NM_FINAL", "RW36", "KTEST"}) {
		t.Fatalf("route = %v, want traffic, 3nm final, threshold, airport", got)
	}
}

func TestVisualApproachFollowingTrafficRejectsNearThresholdLeader(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KTEST", av.Runway{
		Id:                      "36",
		Heading:                 360,
		Threshold:               airportLoc,
		Elevation:               100,
		ThresholdCrossingHeight: 50,
	})

	nmPerLong := float32(52)
	trafficPos := math.NM2LL([2]float32{0, -0.4}, nmPerLong)
	threshold := av.Waypoint{Fix: "RW36", Location: airportLoc}

	n := nav.Nav{
		FlightState: nav.FlightState{
			Position:          math.NM2LL([2]float32{3, -8}, nmPerLong),
			Heading:           360,
			NmPerLongitude:    nmPerLong,
			MagneticVariation: 0,
			ArrivalAirport:    av.Waypoint{Fix: "KTEST"},
		},
	}

	intent := n.ClearedVisualApproach("36",
		&nav.FollowTraffic{Position: trafficPos, Route: av.WaypointArray{threshold, n.FlightState.ArrivalAirport}},
		nil, "", nav.Time{})
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent when leader is inside 0.5nm of threshold, got %T", intent)
	}
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
			wantLooking: true, // >30° error → always looking
		},
		{
			name:        "Moderately off direction (3 o'clock = right, ~90° error)",
			oclock:      3,
			miles:       5,
			wantLooking: true, // >30° error → always looking
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := makeVisualTestSim(airportLoc, "13L")
			ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180) // 5nm north heading south

			intent := sim.handleAirportAdvisory(ac, tt.oclock, tt.miles)
			fi, ok := intent.(av.LookForFieldIntent)
			if !ok {
				t.Fatalf("expected LookForFieldIntent, got %T", intent)
			}

			if tt.wantLooking {
				if fi != av.LookForFieldLooking {
					t.Errorf("expected LookForFieldLooking for bad direction, got %v", fi)
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
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLookingIMC {
		t.Errorf("expected LookForFieldLookingIMC, got %v", fi)
	}
}

func TestAirportAdvisoryTooFar(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")

	// Aircraft 30nm north, beyond visualMaxDistance. Use correct direction (12 o'clock)
	// so we isolate the distance check from the bearing error check.
	ac := makeVisualTestAircraft(math.Point2LL{0, 30.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 12, 30)
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLooking {
		t.Errorf("expected LookForFieldLooking for too-far airport, got %v", fi)
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
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLookingIMC {
		t.Errorf("expected LookForFieldLookingIMC for aircraft above ceiling, got %v", fi)
	}
}

func TestAirportAdvisoryLowVisibility(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 5SM BKN050"} // 5SM visibility, no obscuration phenomena

	// Aircraft at 8nm, beyond 5SM visibility → "looking" with out-of-range reason
	// (reduced surface vis alone without an obscuration phenomenon is "too far", not "obscured").
	ac := makeVisualTestAircraft(math.Point2LL{0, 8.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 12, 8)
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLooking {
		t.Errorf("expected LookForFieldLooking for aircraft beyond visibility range, got %v", fi)
	}
}

func TestAirportAdvisoryObscuration(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM HZ BKN050"}

	// Haze reduces the effective range below this distance even with 10SM reported visibility.
	ac := makeVisualTestAircraft(math.Point2LL{0, 21.0 / 60}, 180)
	intent := sim.handleAirportAdvisory(ac, 12, 21)
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}
	if fi != av.LookForFieldLookingObscured {
		t.Errorf("expected LookForFieldLookingObscured for obscured field, got %v", fi)
	}
}

func TestEffectiveVisualRangeObscurationPenalty(t *testing.T) {
	clear := wx.METAR{Raw: "KJFK 10SM BKN050"}
	haze := wx.METAR{Raw: "KJFK 10SM HZ BKN050"}

	clearRange := clear.EffectiveVisualRange(0)
	hazeRange := haze.EffectiveVisualRange(0)
	if hazeRange >= clearRange {
		t.Fatalf("obscured range = %.2f, clear range = %.2f; expected obscuration penalty", hazeRange, clearRange)
	}
}

func TestFutureFieldInSightDropsWhenFieldNotVisible(t *testing.T) {
	// Field is out of range → processor fires at event.Time but drops without
	// setting FieldInSight. Event is removed from the queue.
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.SimTime = NewSimTime(time.Now())

	ac := makeVisualTestAircraft(math.Point2LL{0, 30.0 / 60}, 180) // 30nm — beyond effective range
	sim.Aircraft = map[av.ADSBCallsign]*Aircraft{"AAL123": ac}
	sim.PendingContacts = make(map[TCP][]PendingContact)

	sim.FutureFieldInSights = []FutureFieldInSight{{ac.ADSBCallsign, sim.State.SimTime.Add(-time.Second)}}

	sim.processFutureFieldInSight()
	if ac.FieldInSight {
		t.Fatal("aircraft beyond effective range should not report field in sight")
	}
	if len(sim.FutureFieldInSights) != 0 {
		t.Fatal("fired event should be removed from queue")
	}
}

func TestFutureTrafficInSightFiresAtDeadline(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.SimTime = NewSimTime(time.Now())
	sim.PendingContacts = make(map[TCP][]PendingContact)

	ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
	sim.Aircraft = map[av.ADSBCallsign]*Aircraft{ac.ADSBCallsign: ac}
	ac.UnseenTrafficCall = &UnseenTrafficCall{
		Callsign:   "DAL456",
		CalledTime: sim.State.SimTime.Add(-15 * time.Second),
	}
	sim.FutureTrafficInSights = []FutureTrafficInSight{
		{ac.ADSBCallsign, "DAL456", sim.State.SimTime.Add(-time.Second)},
	}

	sim.processFutureTrafficInSight()

	if sighting := requireSeenTraffic(t, ac, "DAL456"); sighting.OfferedToMaintainSeparation {
		t.Fatal("delayed traffic-in-sight report should not volunteer separation")
	}
	if ac.UnseenTrafficCall != nil {
		t.Fatal("future traffic-in-sight report should clear the matching unseen traffic call")
	}
	if len(sim.FutureTrafficInSights) != 0 {
		t.Fatal("fired event should be removed from queue")
	}
}

// A fresh TRAFFIC advisory must cancel any earlier queued "looking" event for
// the same aircraft — otherwise the pilot could later report the stale target.
func TestFutureTrafficInSightSupersededByNewAdvisory(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc})
	sim := makeVisualTestSim(airportLoc, "13L")
	sim.State.SimTime = NewSimTime(time.Now())
	sim.PendingContacts = make(map[TCP][]PendingContact)

	ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
	sim.Aircraft = map[av.ADSBCallsign]*Aircraft{ac.ADSBCallsign: ac}

	// Stale event from an earlier advisory.
	sim.FutureTrafficInSights = []FutureTrafficInSight{
		{ac.ADSBCallsign, "OLD456", sim.State.SimTime.Add(10 * time.Second)},
	}

	// Any new advisory path must purge the stale entry.
	sim.handleTrafficAdvisory(ac, 12, 5, ac.Altitude())

	for _, f := range sim.FutureTrafficInSights {
		if f.TrafficCallsign == "OLD456" {
			t.Fatal("stale FutureTrafficInSight for OLD456 should be purged by a new advisory")
		}
	}
}

func TestApprovedAcceptsVolunteeredVisualSeparationWithoutReadback(t *testing.T) {
	vs := NewVisualScenario(t, math.Point2LL{0, 0}, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	sighting := vs.AC.RecordSighting("DAL456", vs.Sim.State.SimTime)
	sighting.OfferedToMaintainSeparation = true

	result := vs.Sim.RunAircraftControlCommands(vs.tcw, vs.callsign, "APPROVED", 0)
	if result.Error != nil {
		t.Fatalf("APPROVED returned error: %v", result.Error)
	}
	if result.ReadbackSpokenText != "" {
		t.Fatalf("APPROVED should not produce a pilot readback, got %q", result.ReadbackSpokenText)
	}
	sighting = requireSeenTraffic(t, vs.AC, "DAL456")
	if sighting.OfferedToMaintainSeparation {
		t.Fatal("APPROVED should clear the pending volunteered visual separation")
	}
	if !sighting.MaintainingVisualSeparation {
		t.Fatal("APPROVED should promote the sighting to maintaining visual separation")
	}
}

func TestMaintainVisualSeparationMarksAircraftState(t *testing.T) {
	vs := NewVisualScenario(t, math.Point2LL{0, 0}, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	sighting := vs.AC.RecordSighting("DAL456", vs.Sim.State.SimTime)
	sighting.OfferedToMaintainSeparation = true

	_, err := vs.Sim.MaintainVisualSeparation(vs.tcw, vs.callsign)
	if err != nil {
		t.Fatalf("VISSEP returned error: %v", err)
	}
	sighting = requireSeenTraffic(t, vs.AC, "DAL456")
	if sighting.OfferedToMaintainSeparation {
		t.Fatal("VISSEP should clear the pending volunteered visual separation")
	}
	if !sighting.MaintainingVisualSeparation {
		t.Fatal("VISSEP should mark the sighting as maintaining visual separation")
	}
}

func TestCVAFollowTrafficRequiresRecentApproachClearedTraffic(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	traffic := makeVisualTestAircraft(math.Point2LL{0, 4.0 / 60}, 180)
	traffic.ADSBCallsign = "DAL456"
	traffic.Nav.Approach.Cleared = true
	vs.Sim.Aircraft[traffic.ADSBCallsign] = traffic

	sighting := vs.AC.RecordSighting(traffic.ADSBCallsign, vs.Sim.State.SimTime.Add(-31*time.Second))

	if vs.Sim.hasRecentApproachTrafficInSight(vs.AC) {
		t.Fatal("stale traffic report should not allow follow-traffic visual")
	}

	sighting.SightedTime = vs.Sim.State.SimTime
	traffic.Nav.Approach.Cleared = false
	if vs.Sim.hasRecentApproachTrafficInSight(vs.AC) {
		t.Fatal("traffic that is not approach-cleared should not allow follow-traffic visual")
	}

	traffic.Nav.Approach.Cleared = true
	if !vs.Sim.hasRecentApproachTrafficInSight(vs.AC) {
		t.Fatal("recent approach-cleared traffic should allow follow-traffic visual")
	}
}

func TestTrafficAdvisoryClearsOfferedStateButKeepsSightingHistory(t *testing.T) {
	vs := NewVisualScenario(t, math.Point2LL{0, 0}, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	sighting := vs.AC.RecordSighting("DAL456", vs.Sim.State.SimTime.Add(-20*time.Second))
	sighting.OfferedToMaintainSeparation = true
	vs.AC.UnseenTrafficCall = &UnseenTrafficCall{
		Callsign:   "DAL456",
		CalledTime: vs.Sim.State.SimTime.Add(-15 * time.Second),
	}
	vs.Sim.FutureTrafficInSights = []FutureTrafficInSight{
		{vs.callsign, "DAL456", vs.Sim.State.SimTime.Add(10 * time.Second)},
	}

	intent := vs.Sim.handleTrafficAdvisory(vs.AC, 12, 5, vs.AC.Altitude())
	ti, ok := intent.(av.TrafficAdvisoryIntent)
	if !ok {
		t.Fatalf("expected TrafficAdvisoryIntent, got %T", intent)
	}
	if ti.Response != av.TrafficResponseLooking {
		t.Fatalf("expected looking response, got %v", ti.Response)
	}
	if len(vs.AC.SeenTraffic) != 1 {
		t.Fatalf("expected prior sighting history to be preserved, got %d entries", len(vs.AC.SeenTraffic))
	}
	sighting = requireSeenTraffic(t, vs.AC, "DAL456")
	if sighting.OfferedToMaintainSeparation {
		t.Fatal("new traffic advisory should clear stale offered-to-maintain state")
	}
	if utc := vs.AC.UnseenTrafficCall; utc != nil && utc.Callsign == "DAL456" {
		t.Fatal("no-traffic advisory response should clear the prior unresolved unseen traffic call (for DAL456)")
	}
	if len(vs.Sim.FutureTrafficInSights) != 0 {
		t.Fatal("new traffic advisory should cancel stale delayed traffic-in-sight events")
	}
}

func TestRecentApproachTrafficInSightForRunwaySkipsNewerWrongRunway(t *testing.T) {
	vs := NewVisualScenario(t, math.Point2LL{0, 0}, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	matching := makeVisualTestAircraft(math.Point2LL{0, 4.0 / 60}, 180)
	matching.ADSBCallsign = "DAL456"
	matching.Nav.Approach.Cleared = true
	matching.Nav.Approach.Assigned.Runway = "13L"
	vs.Sim.Aircraft[matching.ADSBCallsign] = matching

	wrongRunway := makeVisualTestAircraft(math.Point2LL{0, 3.5 / 60}, 180)
	wrongRunway.ADSBCallsign = "UAL789"
	wrongRunway.Nav.Approach.Cleared = true
	wrongRunway.Nav.Approach.Assigned.Runway = "22"
	vs.Sim.Aircraft[wrongRunway.ADSBCallsign] = wrongRunway

	vs.AC.SeenTraffic = []SeenAircraft{
		{Callsign: matching.ADSBCallsign, SightedTime: vs.Sim.State.SimTime.Add(-20 * time.Second)},
		{Callsign: wrongRunway.ADSBCallsign, SightedTime: vs.Sim.State.SimTime.Add(-5 * time.Second)},
	}

	traffic := vs.Sim.recentApproachTrafficInSightForRunway(vs.AC, "13L")
	if traffic == nil {
		t.Fatal("expected to find older matching-runway traffic")
	}
	if traffic.ADSBCallsign != matching.ADSBCallsign {
		t.Fatalf("got %s, want %s", traffic.ADSBCallsign, matching.ADSBCallsign)
	}
}

func TestMaintainingVisualSeparationPersistsUntilTrafficNoLongerVisible(t *testing.T) {
	vs := NewVisualScenario(t, math.Point2LL{0, 0}, "13L", math.Point2LL{0, 5.0 / 60}, 180)

	traffic := makeVisualTestAircraft(math.Point2LL{0, 4.0 / 60}, 180)
	traffic.ADSBCallsign = "DAL456"
	vs.Sim.Aircraft[traffic.ADSBCallsign] = traffic

	vs.AC.SeenTraffic = []SeenAircraft{{
		Callsign:                    traffic.ADSBCallsign,
		SightedTime:                 vs.Sim.State.SimTime.Add(-2 * time.Minute),
		MaintainingVisualSeparation: true,
	}}

	vs.Sim.refreshSeenTraffic(vs.AC)
	if len(vs.AC.SeenTraffic) != 1 {
		t.Fatal("maintaining visual separation should persist while the traffic remains visible")
	}

	traffic.Nav.FlightState.Position = math.Point2LL{0, 6.0 / 60}
	vs.Sim.refreshSeenTraffic(vs.AC)
	if len(vs.AC.SeenTraffic) != 0 {
		t.Fatal("maintaining visual separation should clear once the traffic is no longer visible")
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
			name:      "RequestedVisualApproach set → allowed",
			setup:     func(ac *Aircraft) { ac.RequestedVisualApproach = true },
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := makeVisualTestAircraft(math.Point2LL{0, 5.0 / 60}, 180)
			tt.setup(ac)

			// Mirror the gate check in ClearedVisualApproach.
			allowed := ac.FieldInSight || ac.RequestedVisualApproach
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
		name      string
		command   string // e.g. "EVA22L"
		wantRwy   string // expected in ApproachName
		wantLAHSO string
	}{
		{"EVA22L", "EVA22L", "22L", ""},
		{"EVA13R", "EVA13R", "13R", ""},
		{"EVA31", "EVA31", "31", ""},
		{"EVA4R", "EVA4R", "4R", ""},
		{"EVA22L with LAHSO", "EVA22L/LAHSO26", "22L", "26"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the EVA handler logic from RunAircraftCommands.
			if !strings.HasPrefix(tt.command, "EVA") || len(tt.command) <= 3 {
				t.Fatal("bad test command")
			}
			runway, lahsoRunway := parseLAHSOSuffix(tt.command[3:])
			intent := av.ApproachIntent{
				Type:         av.ApproachExpect,
				ApproachName: "Visual Runway " + runway,
				LAHSORunway:  lahsoRunway,
			}

			if intent.Type != av.ApproachExpect {
				t.Errorf("intent type = %v, want ApproachExpect", intent.Type)
			}
			wantName := "Visual Runway " + tt.wantRwy
			if intent.ApproachName != wantName {
				t.Errorf("ApproachName = %q, want %q", intent.ApproachName, wantName)
			}
			if intent.LAHSORunway != tt.wantLAHSO {
				t.Errorf("LAHSORunway = %q, want %q", intent.LAHSORunway, tt.wantLAHSO)
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

func TestScenarioCVATooCloseIsUnable(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	oppositeLoc := math.Point2LL{0, 1.0 / 60}
	setupTestRunways(t, "KJFK", []av.Runway{
		{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13},
		{Id: "18", Heading: 180, Threshold: oppositeLoc, Elevation: 13},
	})

	// North of the runway 36 threshold and heading north: too close/behind
	// for a stable visual, so CVA is refused.
	vs := NewVisualScenario(t, airportLoc, "36", math.Point2LL{0, 1.0 / 60}, 360)
	vs.AC.FieldInSight = true
	vs.AC.Nav.Approach.Assigned = nil
	vs.AC.Nav.Approach.AssignedId = ""

	intent, err := vs.ClearedVisual("36")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for too-close CVA, got %T", intent)
	}
	if vs.AC.WentAround {
		t.Fatal("too-close CVA should not auto-trigger go-around")
	}
}

func TestScenarioAPThenClearedVisual(t *testing.T) {
	// Full flow: AP advisory → field in sight → CVA clearance.
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "36", Heading: 360, Threshold: airportLoc, Elevation: 13})

	// Aircraft 5nm south heading north — on extended centerline for runway 36.
	vs := NewVisualScenario(t, airportLoc, "36", math.Point2LL{0, -5.0 / 60}, 360)

	// Issue AP: 12 o'clock, 5 miles. Should get field in sight or looking.
	intent := vs.AirportAdvisory(12, 5)
	fi, ok := intent.(av.LookForFieldIntent)
	if !ok {
		t.Fatalf("expected LookForFieldIntent, got %T", intent)
	}

	if fi == av.LookForFieldFound {
		// Great — pilot sees the field. Should accept CVA now.
		intent, err := vs.ClearedVisual("36")
		if err != nil {
			t.Fatalf("ClearedVisual error: %v", err)
		}
		if intent == nil {
			t.Fatal("expected non-nil intent from CVA")
		}
	} else if fi == av.LookForFieldLooking {
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

func TestScenarioCVAInvalidRunwayDoesNotGoAround(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.AC.FieldInSight = true

	intent, err := vs.ClearedVisual("99")
	if err != nil {
		t.Fatalf("expected nil error (unable is an intent, not an error), got %v", err)
	}
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent on invalid runway, got %T", intent)
	}
	if vs.AC.WentAround {
		t.Fatal("invalid runway should not trigger go-around")
	}
}

func TestScenarioEVAInvalidRunwayIsUnable(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "22L", Heading: 220, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "22L", math.Point2LL{0, 5.0 / 60}, 180)
	res := vs.Sim.RunAircraftControlCommands(vs.tcw, vs.callsign, "EVA2LL", 0)

	if res.Error != nil {
		t.Fatalf("expected nil error (unable is an intent, not an error), got %v", res.Error)
	}
	if !strings.Contains(strings.ToLower(res.ReadbackSpokenText), "unable") {
		t.Fatalf("expected unable readback for invalid EVA runway, got %q", res.ReadbackSpokenText)
	}
	if strings.Contains(strings.ToLower(res.ReadbackSpokenText), "expect") {
		t.Fatalf("invalid EVA runway should not produce expect readback, got %q", res.ReadbackSpokenText)
	}
}

func TestScenarioEVAInactiveRunwayIsUnable(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunways(t, "KJFK", []av.Runway{
		{Id: "22L", Heading: 220, Threshold: airportLoc, Elevation: 13},
		{Id: "31R", Heading: 310, Threshold: airportLoc, Elevation: 13},
	})

	vs := NewVisualScenario(t, airportLoc, "22L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.Sim.State.ArrivalRunways = []ArrivalRunway{{Airport: "KJFK", Runway: "22L"}}

	res := vs.Sim.RunAircraftControlCommands(vs.tcw, vs.callsign, "EVA31R", 0)

	if res.Error != nil {
		t.Fatalf("expected nil error (unable is an intent, not an error), got %v", res.Error)
	}
	if !strings.Contains(strings.ToLower(res.ReadbackSpokenText), "unable") {
		t.Fatalf("expected unable readback for inactive EVA runway, got %q", res.ReadbackSpokenText)
	}
	if strings.Contains(strings.ToLower(res.ReadbackSpokenText), "expect") {
		t.Fatalf("inactive EVA runway should not produce expect readback, got %q", res.ReadbackSpokenText)
	}
}

func TestScenarioCVACancelsPendingInitialContact(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.AC.FieldInSight = true

	tcp := TCP(vs.AC.ControllerFrequency)
	vs.Sim.PendingContacts[tcp] = []PendingContact{
		{
			ADSBCallsign: vs.callsign,
			TCP:          tcp,
			Type:         PendingTransmissionArrival,
			ReadyTime:    vs.Sim.State.SimTime,
		},
	}

	intent, err := vs.ClearedVisual("13L")
	if err != nil {
		t.Fatalf("ClearedVisual error: %v", err)
	}
	if intent == nil {
		t.Fatal("expected non-nil intent from CVA")
	}

	for _, pc := range vs.Sim.PendingContacts[tcp] {
		if pc.ADSBCallsign == vs.callsign &&
			(pc.Type == PendingTransmissionArrival || pc.Type == PendingTransmissionDeparture) {
			t.Fatal("expected pending initial contact to be canceled after CVA")
		}
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
	vs.AC.WantsVisualApproach = true

	vs.CheckSpontaneousVisual()

	if !vs.AC.FieldInSight {
		t.Error("expected FieldInSight once eligibility holds")
	}
	if !vs.HasPendingTransmission(PendingTransmissionFieldInSight) {
		t.Error("expected PendingTransmissionFieldInSight to be enqueued")
	}
}

func TestScenarioSpontaneousVisualRequest(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	// Aircraft starts 5nm north — well inside the request distance.
	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.AC.VisualApproachRequestDistance = 10

	vs.CheckSpontaneousVisual()

	if !vs.AC.FieldInSight {
		t.Error("expected FieldInSight")
	}
	if !vs.AC.RequestedVisualApproach {
		t.Error("expected RequestedVisualApproach when within VisualApproachRequestDistance")
	}
	if vs.AC.VisualApproachRequestDistance != 0 {
		t.Error("expected VisualApproachRequestDistance cleared after check")
	}
	if !vs.HasPendingTransmission(PendingTransmissionRequestVisual) {
		t.Error("expected PendingTransmissionRequestVisual to be enqueued")
	}
}

func TestScenarioVisualRequestGivesUpIfFieldNotVisible(t *testing.T) {
	airportLoc := math.Point2LL{0, 0}
	setupTestRunway(t, "KJFK", av.Runway{Id: "13L", Heading: 130, Threshold: airportLoc, Elevation: 13})

	// Aircraft within request distance but with the airport obscured.
	vs := NewVisualScenario(t, airportLoc, "13L", math.Point2LL{0, 5.0 / 60}, 180)
	vs.SetMETAR("KJFK 1SM FG OVC003") // IMC + obscuration
	vs.AC.VisualApproachRequestDistance = 10

	vs.CheckSpontaneousVisual()

	if vs.AC.RequestedVisualApproach {
		t.Error("should not request visual when field not visible")
	}
	if vs.AC.VisualApproachRequestDistance != 0 {
		t.Error("VisualApproachRequestDistance should be cleared after the one-shot check")
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
