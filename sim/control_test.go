// sim/control_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// sim/control_test.go
// Copyright (c) 2025 Matthew Murphy. All rights reserved.

package sim

import (
	"fmt"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
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

	wavey, ok := db.DB.LookupWaypoint("WAVEY")
	if !ok {
		t.Fatal("WAVEY not found")
	}

	newSim := func() (*Sim, av.ADSBCallsign) {
		callsign := av.ADSBCallsign("TEST123")
		s := NewTestSim(lg)
		s.State.CurrentConsolidation["TCW1"] = &TCPConsolidation{PrimaryTCP: "1A"}
		s.Aircraft[callsign] = &Aircraft{
			ADSBCallsign:        callsign,
			ControllerFrequency: "1A",
			Nav: nav.Nav{
				// Ten miles north of WAVEY heading east, so the 050 radial
				// lies ahead of the aircraft and northeast of the fix.
				FlightState: nav.FlightState{
					Position:          math.Point2LL{wavey[0], wavey[1] + 10.0/60},
					Heading:           90,
					NmPerLongitude:    math.NMPerLongitudeAt(wavey),
					MagneticVariation: 13,
				},
				Waypoints: []av.Waypoint{{Fix: "WAVEY", Location: wavey}},
				Rand:      rand.Make(),
			},
		}
		return s, callsign
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
			navIntent, ok := intent.(speech.NavigationIntent)
			if !ok {
				t.Fatalf("runOneControlCommand() returned %T, want speech.NavigationIntent", intent)
			}
			if navIntent.Type != speech.NavInterceptRadial || navIntent.Fix != "WAVEY" ||
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
	s := NewTestSim(lg)
	s.State.CurrentConsolidation["TCW1"] = &TCPConsolidation{PrimaryTCP: "1A"}
	s.Aircraft[callsign] = &Aircraft{
		ADSBCallsign:        callsign,
		ControllerFrequency: "1A",
		Nav: nav.Nav{
			Waypoints: []av.Waypoint{
				{Fix: "MATTY"},
			},
			Approach: nav.Approach{
				Assigned:   appr,
				AssignedId: "RG24",
			},
		},
	}

	intent, err := s.runOneControlCommand("TCW1", callsign, "AMATTY/CSIRG24", 0)
	if err != nil {
		t.Fatalf("runOneControlCommand() returned error: %v", err)
	}

	approachIntent, ok := intent.(speech.ApproachIntent)
	if !ok {
		t.Fatalf("runOneControlCommand() returned %T, want speech.ApproachIntent", intent)
	}
	if approachIntent.Type != speech.ApproachAtFixCleared {
		t.Fatalf("runOneControlCommand() intent type = %v, want %v", approachIntent.Type, speech.ApproachAtFixCleared)
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

func TestParseHeading(t *testing.T) {
	for _, tc := range []struct {
		spec    string
		hdg     int
		toJoin  bool
		wantErr bool
	}{
		{spec: "120", hdg: 120},
		{spec: "120/J", hdg: 120, toJoin: true},
		{spec: "/J", wantErr: true},
		{spec: "WAVEY", wantErr: true},
	} {
		hdg, toJoin, err := parseHeading(tc.spec)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseHeading(%q) error = %v, want error %v", tc.spec, err, tc.wantErr)
		} else if err == nil && (hdg != tc.hdg || toJoin != tc.toJoin) {
			t.Errorf("parseHeading(%q) = (%d, %v), want (%d, %v)", tc.spec, hdg, toJoin, tc.hdg, tc.toJoin)
		}
	}
}

func TestRunOneControlCommandJoin(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())

	fixes := make(map[string]math.Point2LL)
	for _, id := range []string{"CYN", "WHITE", "DIXIE", "MOVFA"} {
		p, ok := db.DB.LookupWaypoint(id)
		if !ok {
			t.Fatalf("%s not found", id)
		}
		fixes[id] = p
	}

	// Ten miles to the left of the V1 leg from WHITE to DIXIE, heading to
	// cross it partway along.
	const magneticVariation = 13
	nmPerLong := math.NMPerLongitudeAt(fixes["WHITE"])
	white, dixie := math.LL2NM(fixes["WHITE"], nmPerLong), math.LL2NM(fixes["DIXIE"], nmPerLong)
	leg := math.Normalize2f(math.Sub2f(dixie, white))
	pos := math.NM2LL(math.Add2f(math.Lerp2f(0.5, white, dixie), math.Scale2f([2]float32{-leg[1], leg[0]}, 10)), nmPerLong)
	headingTo := func(t float32) int {
		target := math.NM2LL(math.Lerp2f(t, white, dixie), nmPerLong)
		return int(math.TrueToMagnetic(math.Heading2LL(pos, target, nmPerLong), magneticVariation))
	}

	newSim := func(sid, star string, onSTAR bool) (*Sim, av.ADSBCallsign) {
		callsign := av.ADSBCallsign("TEST123")
		s := NewTestSim(lg)
		s.State.CurrentConsolidation["TCW1"] = &TCPConsolidation{PrimaryTCP: "1A"}

		var wps []av.Waypoint
		for _, id := range []string{"CYN", "WHITE", "DIXIE", "MOVFA"} {
			wp := av.Waypoint{Fix: id, Location: fixes[id]}
			wp.SetOnSTAR(onSTAR)
			wps = append(wps, wp)
		}
		hdg := math.MagneticHeading(headingTo(0.75))
		s.Aircraft[callsign] = &Aircraft{
			ADSBCallsign:        callsign,
			ControllerFrequency: "1A",
			SID:                 sid,
			STAR:                star,
			Nav: nav.Nav{
				FlightState: nav.FlightState{
					Position:          pos,
					Heading:           hdg,
					NmPerLongitude:    nmPerLong,
					MagneticVariation: magneticVariation,
				},
				Heading:   nav.Heading{Assigned: &hdg},
				Waypoints: wps,
				Rand:      rand.Make(),
			},
		}
		return s, callsign
	}

	run := func(t *testing.T, s *Sim, callsign av.ADSBCallsign, command string) speech.CommandIntent {
		t.Helper()
		intent, err := s.runOneControlCommand("TCW1", callsign, command, 0)
		if err != nil {
			t.Fatalf("%s: runOneControlCommand() returned error: %v", command, err)
		}
		return intent
	}
	wantNavigation := func(t *testing.T, intent speech.CommandIntent, want speech.NavigationIntent) {
		t.Helper()
		if got, ok := intent.(speech.NavigationIntent); !ok || got != want {
			t.Errorf("got %+v, want %+v", intent, want)
		}
	}
	wantUnable := func(t *testing.T, intent speech.CommandIntent) {
		t.Helper()
		if _, ok := intent.(speech.UnableIntent); !ok {
			t.Errorf("got %+v, want an unable response", intent)
		}
	}

	t.Run("JV1", func(t *testing.T) {
		s, callsign := newSim("", "", false)
		wantNavigation(t, run(t, s, callsign, "JV1"), speech.NavigationIntent{Type: speech.NavJoinAirway, Airway: "V1"})
		if dh := s.Aircraft[callsign].Nav.DeferredNavHeading; dh == nil || len(dh.Maneuvers) == 0 || dh.Join == nil {
			t.Error("no maneuvers were queued for the join")
		}

		// A new heading to make the same join.
		command := fmt.Sprintf("H%03d/J", headingTo(0.9))
		intent := run(t, s, callsign, command)
		hi, ok := intent.(speech.HeadingIntent)
		if !ok || hi.Join == nil || hi.Join.Type != speech.NavJoinAirway || hi.Join.Airway != "V1" {
			t.Errorf("%s: got %+v, want a heading to join V1", command, intent)
		}
	})
	t.Run("J", func(t *testing.T) {
		s, callsign := newSim("", "", false)
		if _, err := s.runOneControlCommand("TCW1", callsign, "J", 0); err != ErrInvalidCommandSyntax {
			t.Errorf("J: got error %v, want %v", err, ErrInvalidCommandSyntax)
		}
	})
	t.Run("RSTAR", func(t *testing.T) {
		s, callsign := newSim("", "CAMRN4", true)
		wantNavigation(t, run(t, s, callsign, "RSTAR"), speech.NavigationIntent{Type: speech.NavResumeSTAR, Procedure: "CAMRN4"})
	})
	t.Run("RSID without a SID", func(t *testing.T) {
		s, callsign := newSim("", "CAMRN4", true)
		wantUnable(t, run(t, s, callsign, "RSID"))
	})
	t.Run("heading to join without a join", func(t *testing.T) {
		s, callsign := newSim("", "", false)
		wantUnable(t, run(t, s, callsign, "R120/J"))
	})
}

// TestRunControlCommandsStaysOnFrequency checks that a pilot who asks for part of a
// transmission again or refuses part of it stays on the controller's frequency.
func TestRunControlCommandsStaysOnFrequency(t *testing.T) {
	tests := []struct {
		name         string
		commands     string
		wantTower    bool
		wantAltitude bool
		wantReadback string
	}{
		{name: "accepted", commands: "D20 TO", wantTower: true, wantAltitude: true},
		{name: "say again", commands: "SAYAGAIN/FIX TO"},
		{name: "commands after say again", commands: "SAYAGAIN/HEADING D20"},
		{name: "commands before say again", commands: "D20 SAYAGAIN/HEADING", wantAltitude: true},
		{name: "refused direct", commands: "DBOGUS D20 TO", wantAltitude: true, wantReadback: "isn't a valid fix"},
		{name: "refused speed until", commands: "S170/UBOGUS TO/118300", wantReadback: "isn't a valid fix"},
		{name: "refused before frequency change approved", commands: "DBOGUS FC"},
		{name: "refused before radar service terminated", commands: "DBOGUS RST"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(log.New(true, "error", t.TempDir()))
			ac := MakeTestAircraft("AAL123", "22L")
			ac.Nav.Perf = db.DB.AircraftPerformance["A320"]
			ac.Nav.Approach.Cleared = true
			s.Aircraft[ac.ADSBCallsign] = ac
			freq := ac.ControllerFrequency

			res := s.RunAircraftControlCommands(E2ETCW(), ac.ADSBCallsign, tc.commands, 0, 0)
			if res.Error != nil {
				t.Fatalf("%s: %v", tc.commands, res.Error)
			}
			if !strings.Contains(res.ReadbackSpokenText, tc.wantReadback) {
				t.Errorf("%s: readback %q does not contain %q", tc.commands, res.ReadbackSpokenText, tc.wantReadback)
			}
			if ac.GotContactTower != tc.wantTower {
				t.Errorf("%s: contacted tower = %v, want %v", tc.commands, ac.GotContactTower, tc.wantTower)
			}
			if !tc.wantTower && ac.ControllerFrequency != freq {
				t.Errorf("%s: frequency = %q, want %q", tc.commands, ac.ControllerFrequency, freq)
			}
			if got := ac.Nav.Altitude.Assigned != nil; got != tc.wantAltitude {
				t.Errorf("%s: altitude assigned = %v, want %v", tc.commands, got, tc.wantAltitude)
			}
		})
	}

	s := NewTestSim(log.New(true, "error", t.TempDir()))
	s.State.Controllers = map[ControlPosition]*av.Controller{"2B": {}}
	if !s.changesFrequency("CT2B") {
		t.Error("CT2B: contacting a controller should change frequency")
	}
	if s.changesFrequency("CTTL") {
		t.Error("CTTL: clearing an approach should not change frequency")
	}
}
