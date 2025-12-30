// sim/control_test.go
// Copyright (c) 2025 Matthew Murphy. All rights reserved.

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
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
		wantRadial    float32
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
