// sim/control_test.go
// Copyright (c) 2025 Matthew Murphy. All rights reserved.

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
)

func TestParseHold(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		wantFix       string
		wantHold      *av.Hold
		wantErr       bool
		errContains   string
		checkTurn     bool
		wantTurnDir   av.TurnDirection
		checkLeg      bool
		wantLegLength float32
		wantLegTime   float32
		checkRadial   bool
		wantRadial    math.MagneticHeading
	}{
		{
			name:     "Published hold - no options",
			command:  "JIMEE",
			wantFix:  "JIMEE",
			wantHold: nil,
			wantErr:  false,
		},
		{
			name:        "Controller hold - left turns with radial",
			command:     "JIMEE/L/R090",
			wantFix:     "JIMEE",
			wantErr:     false,
			checkTurn:   true,
			wantTurnDir: av.TurnLeft,
			checkRadial: true,
			wantRadial:  90,
			checkLeg:    true,
			wantLegTime: 1.0,
		},
		{
			name:        "Controller hold - right turns with radial",
			command:     "JIMEE/R/R270",
			wantFix:     "JIMEE",
			wantErr:     false,
			checkTurn:   true,
			wantTurnDir: av.TurnRight,
			checkRadial: true,
			wantRadial:  270,
			checkLeg:    true,
			wantLegTime: 1.0,
		},
		{
			name:          "Controller hold - distance legs",
			command:       "JIMEE/5NM/R180",
			wantFix:       "JIMEE",
			wantErr:       false,
			checkLeg:      true,
			wantLegLength: 5.0,
			wantLegTime:   0,
			checkRadial:   true,
			wantRadial:    180,
		},
		{
			name:          "Controller hold - time legs",
			command:       "JIMEE/2M/R045",
			wantFix:       "JIMEE",
			wantErr:       false,
			checkLeg:      true,
			wantLegTime:   2.0,
			wantLegLength: 0,
			checkRadial:   true,
			wantRadial:    45,
		},
		{
			name:          "Controller hold - all options",
			command:       "JIMEE/L/5NM/R090",
			wantFix:       "JIMEE",
			wantErr:       false,
			checkTurn:     true,
			wantTurnDir:   av.TurnLeft,
			checkLeg:      true,
			wantLegLength: 5.0,
			wantLegTime:   0,
			checkRadial:   true,
			wantRadial:    90,
		},
		{
			name:        "Controller hold - variable digit radial (2 digits)",
			command:     "JIMEE/R90",
			wantFix:     "JIMEE",
			wantErr:     false,
			checkRadial: true,
			wantRadial:  90,
		},
		{
			name:        "Controller hold - variable digit radial (1 digit)",
			command:     "JIMEE/R5",
			wantFix:     "JIMEE",
			wantErr:     false,
			checkRadial: true,
			wantRadial:  5,
		},
		{
			name:        "Controller hold - lowercase options normalized",
			command:     "jimee/l/5nm/r090",
			wantFix:     "JIMEE",
			wantErr:     false,
			checkTurn:   true,
			wantTurnDir: av.TurnLeft,
		},
		{
			name:        "Error - conflicting turn directions",
			command:     "JIMEE/L/R/R090",
			wantErr:     true,
			errContains: "conflicting hold options: both left and right turns",
		},
		{
			name:        "Error - conflicting leg types",
			command:     "JIMEE/2M/5NM/R090",
			wantErr:     true,
			errContains: "conflicting hold options: both distance and time legs",
		},
		{
			name:        "Error - duplicate left turns",
			command:     "JIMEE/L/L/R090",
			wantErr:     true,
			errContains: "duplicate hold option: left turns",
		},
		{
			name:        "Error - duplicate right turns",
			command:     "JIMEE/R/R/R090",
			wantErr:     true,
			errContains: "duplicate hold option: right turns",
		},
		{
			name:        "Error - duplicate distance legs",
			command:     "JIMEE/5NM/3NM/R090",
			wantErr:     true,
			errContains: "duplicate hold option: distance legs",
		},
		{
			name:        "Error - duplicate time legs",
			command:     "JIMEE/2M/3M/R090",
			wantErr:     true,
			errContains: "duplicate hold option: time legs",
		},
		{
			name:        "Error - duplicate radials",
			command:     "JIMEE/R090/R180",
			wantErr:     true,
			errContains: "duplicate hold option: radial",
		},
		{
			name:        "Error - missing radial for controller hold",
			command:     "JIMEE/L",
			wantErr:     true,
			errContains: "radial (Rxxx) is required",
		},
		{
			name:        "Error - invalid distance",
			command:     "JIMEE/XNM/R090",
			wantErr:     true,
			errContains: "invalid distance",
		},
		{
			name:        "Error - negative distance",
			command:     "JIMEE/-5NM/R090",
			wantErr:     true,
			errContains: "invalid distance",
		},
		{
			name:        "Error - zero distance",
			command:     "JIMEE/0NM/R090",
			wantErr:     true,
			errContains: "invalid distance",
		},
		{
			name:        "Error - invalid time",
			command:     "JIMEE/XM/R090",
			wantErr:     true,
			errContains: "invalid time",
		},
		{
			name:        "Error - negative time",
			command:     "JIMEE/-2M/R090",
			wantErr:     true,
			errContains: "invalid time",
		},
		{
			name:        "Error - zero time",
			command:     "JIMEE/0M/R090",
			wantErr:     true,
			errContains: "invalid time",
		},
		{
			name:        "Error - invalid radial format",
			command:     "JIMEE/RX",
			wantErr:     true,
			errContains: "invalid radial",
		},
		{
			name:        "Error - radial too large",
			command:     "JIMEE/R361",
			wantErr:     true,
			errContains: "invalid radial",
		},
		{
			name:        "Error - negative radial",
			command:     "JIMEE/R-90",
			wantErr:     true,
			errContains: "invalid radial",
		},
		{
			name:        "Error - invalid option",
			command:     "JIMEE/INVALID/R090",
			wantErr:     true,
			errContains: "invalid hold option",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFix, gotHold, ok := parseHold(tt.command)

			if tt.wantErr {
				if ok {
					t.Errorf("parseHold() expected error, got success")
					return
				}
				return
			}

			if !ok {
				t.Errorf("parseHold() unexpected failure")
				return
			}

			if gotFix != tt.wantFix {
				t.Errorf("parseHold() fix = %v, want %v", gotFix, tt.wantFix)
			}

			// If no checks are specified, we expect a published hold (nil)
			expectPublishedHold := !tt.checkTurn && !tt.checkLeg && !tt.checkRadial

			if expectPublishedHold {
				if gotHold != nil {
					t.Errorf("parseHold() hold = %v, want nil", gotHold)
				}
				return
			}

			if gotHold == nil {
				t.Errorf("parseHold() hold = nil, want non-nil")
				return
			}

			if gotHold.Fix != tt.wantFix {
				t.Errorf("parseHold() hold.Fix = %v, want %v", gotHold.Fix, tt.wantFix)
			}

			if tt.checkTurn && gotHold.TurnDirection != tt.wantTurnDir {
				t.Errorf("parseHold() hold.TurnDirection = %v, want %v", gotHold.TurnDirection, tt.wantTurnDir)
			}

			if tt.checkLeg {
				if gotHold.LegLengthNM != tt.wantLegLength {
					t.Errorf("parseHold() hold.LegLengthNM = %v, want %v", gotHold.LegLengthNM, tt.wantLegLength)
				}
				if gotHold.LegMinutes != tt.wantLegTime {
					t.Errorf("parseHold() hold.LegMinutes = %v, want %v", gotHold.LegMinutes, tt.wantLegTime)
				}
			}

			if tt.checkRadial && gotHold.InboundCourse != tt.wantRadial {
				t.Errorf("parseHold() hold.InboundCourse = %v, want %v", gotHold.InboundCourse, tt.wantRadial)
			}
		})
	}
}

func TestParseInterceptRadial(t *testing.T) {
	tests := []struct {
		command      string
		wantFix      string
		wantRadial   int
		wantOutbound bool
		wantOk       bool
	}{
		{command: "WAVEY/050", wantFix: "WAVEY", wantRadial: 50, wantOk: true},
		{command: "WAVEY/50", wantFix: "WAVEY", wantRadial: 50, wantOk: true},
		{command: "WAVEY/050I", wantFix: "WAVEY", wantRadial: 50, wantOk: true},
		{command: "WAVEY/050O", wantFix: "WAVEY", wantRadial: 50, wantOutbound: true, wantOk: true},
		{command: "wavey/360o", wantFix: "WAVEY", wantRadial: 360, wantOutbound: true, wantOk: true},
		{command: "WAVEY/000"},
		{command: "WAVEY/361"},
		{command: "WAVEY/"},
		{command: "WAVEY"},
		{command: "/050"},
		{command: "WAVEY/05X"},
		{command: "WAVEY/050IO"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			fix, radial, outbound, ok := parseInterceptRadial(tt.command)
			if ok != tt.wantOk {
				t.Fatalf("parseInterceptRadial() ok = %v, want %v", ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if fix != tt.wantFix || radial != tt.wantRadial || outbound != tt.wantOutbound {
				t.Errorf("parseInterceptRadial() = (%q, %d, %v), want (%q, %d, %v)",
					fix, radial, outbound, tt.wantFix, tt.wantRadial, tt.wantOutbound)
			}
		})
	}
}

func TestRunOneControlCommandInterceptRadial(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())

	wavey, ok := av.DB.LookupWaypoint("WAVEY")
	if !ok {
		t.Fatal("WAVEY not found")
	}

	newSim := func() (*Sim, av.ADSBCallsign) {
		callsign := av.ADSBCallsign("TEST123")
		return &Sim{
			State: &CommonState{
				DynamicState: DynamicState{
					CurrentConsolidation: map[TCW]*TCPConsolidation{
						"TCW1": {PrimaryTCP: "1A"},
					},
				},
			},
			Aircraft: map[av.ADSBCallsign]*Aircraft{
				callsign: {
					ADSBCallsign:        callsign,
					ControllerFrequency: "1A",
					Nav: nav.Nav{
						// Ten miles north of WAVEY heading east, so the
						// 050 radial lies ahead of the aircraft and
						// northeast of the fix.
						FlightState: nav.FlightState{
							Position:          math.Point2LL{wavey[0], wavey[1] + 10.0/60},
							Heading:           90,
							NmPerLongitude:    math.NMPerLongitudeAt(wavey),
							MagneticVariation: 13,
						},
						Waypoints: []av.Waypoint{{Fix: "WAVEY", Location: wavey}},
						Rand:      rand.Make(),
					},
				},
			},
			PendingContacts: map[TCP][]PendingContact{},
			lg:              lg,
		}, callsign
	}

	for _, tc := range []struct {
		command      string
		wantRadial   math.MagneticHeading
		wantOutbound bool
	}{
		{command: "IWAVEY/050", wantRadial: 50},
		{command: "IWAVEY/050O", wantRadial: 50, wantOutbound: true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			s, callsign := newSim()
			intent, err := s.runOneControlCommand("TCW1", callsign, tc.command, 0)
			if err != nil {
				t.Fatalf("runOneControlCommand() returned error: %v", err)
			}
			navIntent, ok := intent.(av.NavigationIntent)
			if !ok {
				t.Fatalf("runOneControlCommand() returned %T, want av.NavigationIntent", intent)
			}
			if navIntent.Type != av.NavInterceptRadial || navIntent.Fix != "WAVEY" ||
				navIntent.Radial != tc.wantRadial || navIntent.Outbound != tc.wantOutbound {
				t.Errorf("got %+v, want intercept of the WAVEY %v radial, outbound %v",
					navIntent, tc.wantRadial, tc.wantOutbound)
			}
			if dh := s.Aircraft[callsign].Nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 {
				t.Error("no maneuvers were queued for the intercept")
			}
		})
	}
}

func TestRunOneControlCommandAtFixClearedStraightInApproach(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())

	appr := &av.Approach{
		FullName: "RNAV Runway 24",
		Waypoints: []av.WaypointArray{
			{
				{Fix: "MATTY"},
			},
		},
	}

	callsign := av.ADSBCallsign("TEST123")
	s := &Sim{
		State: &CommonState{
			DynamicState: DynamicState{
				CurrentConsolidation: map[TCW]*TCPConsolidation{
					"TCW1": {PrimaryTCP: "1A"},
				},
			},
		},
		Aircraft: map[av.ADSBCallsign]*Aircraft{
			callsign: {
				ADSBCallsign:        callsign,
				ControllerFrequency: "1A",
				Nav: nav.Nav{
					Waypoints: []av.Waypoint{
						{Fix: "MATTY"},
					},
					Approach: nav.NavApproach{
						Assigned:   appr,
						AssignedId: "RG24",
					},
				},
			},
		},
		PendingContacts: map[TCP][]PendingContact{},
		lg:              lg,
	}

	intent, err := s.runOneControlCommand("TCW1", callsign, "AMATTY/CSIRG24", 0)
	if err != nil {
		t.Fatalf("runOneControlCommand() returned error: %v", err)
	}

	approachIntent, ok := intent.(av.ApproachIntent)
	if !ok {
		t.Fatalf("runOneControlCommand() returned %T, want av.ApproachIntent", intent)
	}
	if approachIntent.Type != av.ApproachAtFixCleared {
		t.Fatalf("runOneControlCommand() intent type = %v, want %v", approachIntent.Type, av.ApproachAtFixCleared)
	}
	if !approachIntent.StraightIn {
		t.Fatal("runOneControlCommand() did not preserve straight-in clearance")
	}
	if approachIntent.Fix != "MATTY" {
		t.Fatalf("runOneControlCommand() fix = %q, want %q", approachIntent.Fix, "MATTY")
	}
	if s.Aircraft[callsign].Nav.Approach.AtFixClearedRoute == nil {
		t.Fatal("AtFixClearedRoute was not populated")
	}
}
