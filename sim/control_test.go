// sim/control_test.go
// Copyright (c) 2025 Matthew Murphy. All rights reserved.

package sim

import (
	"errors"
	"reflect"
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

func TestRunOneControlCommandAtFixClearedStraightInApproach(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())

	appr := &av.Approach{
		Id:       "RG24",
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
						Assigned: appr,
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

func TestTriggerReachable(t *testing.T) {
	cases := []struct {
		name     string
		kind     nav.ConditionalKind
		trigger  float32
		current  float32
		assigned *float32
		want     bool
	}{
		// LV: within 500ft slack even if direction is wrong
		{"LV aircraft at 3050 climbing past", nav.ConditionalLeaving, 3000, 3050, ptr[float32](5000), true},
		{"LV aircraft far past", nav.ConditionalLeaving, 3000, 5000, ptr[float32](7000), false},
		{"LV trigger in path", nav.ConditionalLeaving, 3000, 1000, ptr[float32](5000), true},
		{"LV no target, far from trigger", nav.ConditionalLeaving, 3000, 8000, nil, false},
		// RC: trigger must be between current and assigned target
		{"RC target is trigger", nav.ConditionalReaching, 10000, 5000, ptr[float32](10000), true},
		{"RC trigger above target", nav.ConditionalReaching, 12000, 5000, ptr[float32](10000), false},
		{"RC no target but close", nav.ConditionalReaching, 10000, 9900, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ac := &Aircraft{}
			ac.Nav.FlightState.Altitude = tc.current
			ac.Nav.Altitude.Assigned = tc.assigned
			got := triggerReachable(ac, tc.kind, tc.trigger)
			if got != tc.want {
				t.Errorf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestParseConditionalAltitude(t *testing.T) {
	cases := []struct {
		in      string
		want    float32
		wantErr bool
	}{
		{"30", 3000, false},       // hundreds-of-feet
		{"130", 13000, false},
		{"100", 10000, false},
		{"1000", 1000, false},     // >600 && %100==0 → already feet
		{"13000", 13000, false},   // ditto
		{"", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseConditionalAltitude(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseConditionalAltitude(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseConditionalAltitude(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func setupTestSimWithAircraftAt(t *testing.T, altitude, assigned float32) (*Sim, av.ADSBCallsign, TCW) {
	t.Helper()
	lg := log.New(true, "error", t.TempDir())
	callsign := av.ADSBCallsign("TEST123")
	tcw := TCW("TCW1")
	s := &Sim{
		State: &CommonState{
			DynamicState: DynamicState{
				CurrentConsolidation: map[TCW]*TCPConsolidation{
					tcw: {PrimaryTCP: "1A"},
				},
			},
		},
		Aircraft: map[av.ADSBCallsign]*Aircraft{
			callsign: {
				ADSBCallsign:        callsign,
				ControllerFrequency: "1A",
				Nav: nav.Nav{
					FlightState: nav.FlightState{
						Altitude: altitude,
					},
					Altitude: nav.NavAltitude{
						Assigned: ptr[float32](assigned),
					},
				},
			},
		},
		PendingContacts: map[TCP][]PendingContact{},
		PrivilegedTCWs:  map[TCW]bool{tcw: true},
		lg:              lg,
	}
	return s, callsign, tcw
}

func TestAssignConditionalInstallsSlot(t *testing.T) {
	s, callsign, tcw := setupTestSimWithAircraftAt(t, 2000, 7000)
	action := nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest}
	intent, err := s.AssignConditional(tcw, callsign, nav.ConditionalLeaving, 3000, action)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent == nil {
		t.Fatalf("expected non-nil intent")
	}
	if _, ok := intent.(av.ConditionalCommandIntent); !ok {
		t.Fatalf("expected ConditionalCommandIntent, got %T", intent)
	}
	pc := s.Aircraft[callsign].Nav.PendingConditionalCommand
	if pc == nil {
		t.Fatalf("expected PendingConditionalCommand installed")
	}
	if pc.Altitude != 3000 {
		t.Fatalf("wrong altitude: %v", pc.Altitude)
	}
	if pc.Kind != nav.ConditionalLeaving {
		t.Fatalf("wrong kind: %v", pc.Kind)
	}
}

func TestAssignConditionalRejectsUnreachable(t *testing.T) {
	// Aircraft at 5000 level (assigned also 5000); trigger 3000 -> unreachable.
	s, callsign, tcw := setupTestSimWithAircraftAt(t, 5000, 5000)
	action := nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest}
	intent, err := s.AssignConditional(tcw, callsign, nav.ConditionalLeaving, 3000, action)
	if err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Fatalf("expected UnableIntent for unreachable trigger, got %T", intent)
	}
	if s.Aircraft[callsign].Nav.PendingConditionalCommand != nil {
		t.Fatalf("expected no slot installed after unable")
	}
}

func TestAssignConditionalSupersedes(t *testing.T) {
	s, callsign, tcw := setupTestSimWithAircraftAt(t, 2000, 7000)
	first := nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest}
	second := nav.ConditionalDirectFix{Fix: "AAC", Turn: av.TurnClosest}
	if _, err := s.AssignConditional(tcw, callsign, nav.ConditionalLeaving, 3000, first); err != nil {
		t.Fatalf("first assign: %v", err)
	}
	if _, err := s.AssignConditional(tcw, callsign, nav.ConditionalReaching, 6000, second); err != nil {
		t.Fatalf("second assign: %v", err)
	}
	pc := s.Aircraft[callsign].Nav.PendingConditionalCommand
	if pc == nil {
		t.Fatalf("expected superseded slot, got nil")
	}
	if pc.Kind == nav.ConditionalLeaving {
		t.Fatalf("old Leaving kind not replaced")
	}
	if pc.Kind != nav.ConditionalReaching || pc.Altitude != 6000 {
		t.Fatalf("expected superseded slot: reaching 6000, got %+v", pc)
	}
}

func TestParseConditionalAction(t *testing.T) {
	cases := []struct {
		in        string
		wantType  string // type name of returned ConditionalAction
		wantProps map[string]any
		wantErr   bool
	}{
		{"H010", "ConditionalHeading", map[string]any{"Heading": 10, "Turn": av.TurnClosest}, false},
		{"L100", "ConditionalHeading", map[string]any{"Heading": 100, "Turn": av.TurnLeft}, false},
		{"R100", "ConditionalHeading", map[string]any{"Heading": 100, "Turn": av.TurnRight}, false},
		{"L20D", "ConditionalHeading", map[string]any{"ByDegrees": 20, "Turn": av.TurnLeft}, false},
		{"R30D", "ConditionalHeading", map[string]any{"ByDegrees": 30, "Turn": av.TurnRight}, false},
		{"DAAC", "ConditionalDirectFix", map[string]any{"Fix": "AAC", "Turn": av.TurnClosest}, false},
		{"LDAAC", "ConditionalDirectFix", map[string]any{"Fix": "AAC", "Turn": av.TurnLeft}, false},
		{"RDAAC", "ConditionalDirectFix", map[string]any{"Fix": "AAC", "Turn": av.TurnRight}, false},
		{"S210", "ConditionalSpeed", nil, false},
		{"M78", "ConditionalMach", map[string]any{"Mach": float32(0.78)}, false},

		// Rejections: altitude-changing inners, unknowns, malformed
		{"C50", "", nil, true},
		{"CVS", "", nil, true},
		{"DVS", "", nil, true},
		{"X010", "", nil, true},
		{"", "", nil, true},
		{"H", "", nil, true},
		{"HXYZ", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseConditionalAction(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseConditionalAction(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			typeName := reflect.TypeOf(got).Name()
			if typeName != tc.wantType {
				t.Fatalf("parseConditionalAction(%q) type = %s, want %s", tc.in, typeName, tc.wantType)
			}
			v := reflect.ValueOf(got)
			for k, want := range tc.wantProps {
				field := v.FieldByName(k)
				if !field.IsValid() {
					t.Errorf("no field %s on %s", k, typeName)
					continue
				}
				if !reflect.DeepEqual(field.Interface(), want) {
					t.Errorf("%s.%s = %v, want %v", typeName, k, field.Interface(), want)
				}
			}
		})
	}
}

func TestRunOneControlCommandLV(t *testing.T) {
	s, callsign, tcw := setupTestSimWithAircraftAt(t, 2000, 7000)
	intent, err := s.runOneControlCommand(tcw, callsign, "LV30/H010", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := intent.(av.ConditionalCommandIntent); !ok {
		t.Fatalf("expected ConditionalCommandIntent, got %T", intent)
	}
	ac := s.Aircraft[callsign]
	if ac.Nav.PendingConditionalCommand == nil {
		t.Fatalf("slot not installed")
	}
	if ac.Nav.PendingConditionalCommand.Altitude != 3000 {
		t.Fatalf("wrong altitude %v", ac.Nav.PendingConditionalCommand.Altitude)
	}
}

func TestRunOneControlCommandLVRejectsMalformed(t *testing.T) {
	cases := []struct {
		cmd        string
		wantSyntax bool
	}{
		{"LV", true},         // bare command, too short
		{"LV30H010", true},   // missing slash
		{"LV/H010", true},    // empty altitude
		{"LV30/", true},      // empty inner
		{"LVABC/H010", false}, // non-numeric altitude (strconv error)
		{"LV30/X010", true},  // unknown inner command
		{"LV30/C50", true},   // altitude-changing inner rejected by parseConditionalAction
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			s, callsign, tcw := setupTestSimWithAircraftAt(t, 2000, 7000)
			_, err := s.runOneControlCommand(tcw, callsign, tc.cmd, 0)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.cmd)
			}
			if tc.wantSyntax && !errors.Is(err, ErrInvalidCommandSyntax) {
				t.Fatalf("expected ErrInvalidCommandSyntax for %q, got %v", tc.cmd, err)
			}
		})
	}
}

func TestRunOneControlCommandRC(t *testing.T) {
	s, callsign, tcw := setupTestSimWithAircraftAt(t, 5000, 10000)
	intent, err := s.runOneControlCommand(tcw, callsign, "RC100/DAAC", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := intent.(av.ConditionalCommandIntent); !ok {
		t.Fatalf("expected ConditionalCommandIntent, got %T", intent)
	}
	ac := s.Aircraft[callsign]
	if ac.Nav.PendingConditionalCommand == nil {
		t.Fatalf("slot not installed")
	}
	if ac.Nav.PendingConditionalCommand.Altitude != 10000 {
		t.Fatalf("wrong altitude %v", ac.Nav.PendingConditionalCommand.Altitude)
	}
	if ac.Nav.PendingConditionalCommand.Kind != nav.ConditionalReaching {
		t.Fatalf("wrong kind %v", ac.Nav.PendingConditionalCommand.Kind)
	}
}

func TestRunOneControlCommandRCRejectsMalformed(t *testing.T) {
	cases := []struct {
		cmd        string
		wantSyntax bool
	}{
		{"RC", true},          // bare command, too short
		{"RC100H010", true},   // missing slash
		{"RC/H010", true},     // empty altitude
		{"RC100/", true},      // empty inner
		{"RCABC/H010", false}, // non-numeric altitude (strconv error)
		{"RC100/X010", true},  // unknown inner command
		{"RC100/C50", true},   // altitude-changing inner rejected by parseConditionalAction
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			s, callsign, tcw := setupTestSimWithAircraftAt(t, 5000, 10000)
			_, err := s.runOneControlCommand(tcw, callsign, tc.cmd, 0)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.cmd)
			}
			if tc.wantSyntax && !errors.Is(err, ErrInvalidCommandSyntax) {
				t.Fatalf("expected ErrInvalidCommandSyntax for %q, got %v", tc.cmd, err)
			}
		})
	}
}

func TestFireConditionalIfTriggeredFiresAndClearsSlot(t *testing.T) {
	// Aircraft climbing through 3000 with a pending LV 3000/H010 command.
	s, callsign, _ := setupTestSimWithAircraftAt(t, 3100, 7000)
	ac := s.Aircraft[callsign]
	ac.NASFlightPlan = &NASFlightPlan{} // make IsAssociated() return true
	ac.Nav.Rand = rand.Make()           // needed by EnqueueHeading for pilot-delay jitter
	ac.Nav.FlightState.AltitudeRate = 500 // climbing
	ac.Nav.PendingConditionalCommand = &nav.PendingConditionalCommand{
		Kind:     nav.ConditionalLeaving,
		Altitude: 3000,
		Action:   nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest},
	}

	s.fireConditionalIfTriggered(ac, av.Temperature{})

	if ac.Nav.PendingConditionalCommand != nil {
		t.Fatalf("expected slot cleared after firing, still got %+v", ac.Nav.PendingConditionalCommand)
	}
	if hdg, ok := ac.Nav.AssignedHeading(); !ok || hdg != 10 {
		t.Fatalf("expected assigned heading 10, got ok=%v hdg=%v", ok, hdg)
	}
}

func TestFireConditionalIfTriggeredHoldsSlotWhenNotTriggered(t *testing.T) {
	// Aircraft at 2000 climbing — has not yet reached 3000 trigger.
	s, callsign, _ := setupTestSimWithAircraftAt(t, 2000, 7000)
	ac := s.Aircraft[callsign]
	ac.NASFlightPlan = &NASFlightPlan{} // make IsAssociated() return true
	ac.Nav.FlightState.AltitudeRate = 500
	pc := &nav.PendingConditionalCommand{
		Kind:     nav.ConditionalLeaving,
		Altitude: 3000,
		Action:   nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest},
	}
	ac.Nav.PendingConditionalCommand = pc

	s.fireConditionalIfTriggered(ac, av.Temperature{})

	if ac.Nav.PendingConditionalCommand != pc {
		t.Fatalf("expected slot still installed (not triggered yet)")
	}
	if _, ok := ac.Nav.AssignedHeading(); ok {
		t.Fatalf("expected no heading assigned before trigger fires")
	}
}

func TestFireConditionalIfTriggeredSkipsWhenUnassociated(t *testing.T) {
	// Setup sim and aircraft state that WOULD trigger, but aircraft has no
	// NASFlightPlan so IsAssociated() returns false.
	s, callsign, _ := setupTestSimWithAircraftAt(t, 3100, 7000)
	ac := s.Aircraft[callsign]
	// NASFlightPlan is nil by default from setupTestSimWithAircraftAt — unassociated.
	ac.Nav.FlightState.AltitudeRate = 500
	pc := &nav.PendingConditionalCommand{
		Kind:     nav.ConditionalLeaving,
		Altitude: 3000,
		Action:   nav.ConditionalHeading{Heading: 10, Turn: av.TurnClosest},
	}
	ac.Nav.PendingConditionalCommand = pc

	s.fireConditionalIfTriggered(ac, av.Temperature{})

	if ac.Nav.PendingConditionalCommand != pc {
		t.Fatalf("expected slot preserved when unassociated")
	}
}
