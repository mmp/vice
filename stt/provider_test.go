package stt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

// TestMain initializes the aviation database and STT registries for all tests.
func TestMain(m *testing.M) {
	av.InitDB()
	Init()
	os.Exit(m.Run())
}

// Test cases from sttSystemPrompt.md

func TestBasicAltitudeCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "descend and maintain",
			transcript: "American 5936 descend and maintain 8000",
			aircraft: map[string]Aircraft{
				"American 5936": {Callsign: "AAL5936", Altitude: 12000, State: "arrival"},
			},
			expected: "AAL5936 D80",
		},
		{
			name:       "climb and maintain flight level",
			transcript: "United 452 climb and maintain flight level three five zero",
			aircraft: map[string]Aircraft{
				"United 452": {Callsign: "UAL452", Altitude: 28000, State: "overflight"},
			},
			expected: "UAL452 C350",
		},
		{
			name:       "climb and maintain FL abbreviation",
			transcript: "United 452 climb and maintain FL three five zero",
			aircraft: map[string]Aircraft{
				"United 452": {Callsign: "UAL452", Altitude: 28000, State: "overflight"},
			},
			expected: "UAL452 C350",
		},
		{
			name:       "fly level as flight level",
			transcript: "United 452 climb and maintain fly level two three zero",
			aircraft: map[string]Aircraft{
				"United 452": {Callsign: "UAL452", Altitude: 18000, State: "overflight"},
			},
			expected: "UAL452 C230",
		},
		{
			name:       "radar contact climb",
			transcript: "Delta 88 radar contact climb and maintain niner thousand",
			aircraft: map[string]Aircraft{
				"Delta 88": {Callsign: "DAL88", Altitude: 3000, State: "departure"},
			},
			expected: "DAL88 C90",
		},
		{
			name:       "maintain altitude at same level",
			transcript: "Southwest 221 maintain one zero ten thousand",
			aircraft: map[string]Aircraft{
				"Southwest 221": {Callsign: "SWA221", Altitude: 10000, State: "overflight"},
			},
			expected: "SWA221 A100",
		},
		{
			name:       "expedite climb",
			transcript: "JetBlue 615 expedite climb",
			aircraft: map[string]Aircraft{
				"JetBlue 615": {Callsign: "JBU615", Altitude: 5000, State: "departure"},
			},
			expected: "JBU615 EC",
		},
		{
			name:       "descend via star",
			transcript: "Frontier 900 descend via the star",
			aircraft: map[string]Aircraft{
				"Frontier 900": {Callsign: "FFT900", Altitude: 25000, State: "arrival"},
			},
			expected: "FFT900 DVS",
		},
		{
			name:       "alphanumeric callsign with heavy suffix and single digit altitude",
			transcript: "Lufthansa 4WJ heavy descend and maintain niner",
			aircraft: map[string]Aircraft{
				"Lufthansa 4WJ heavy": {Callsign: "DLH4WJ", Altitude: 12000, State: "arrival"},
			},
			expected: "DLH4WJ D90",
		},
		{
			name:       "hyphenated altitude from STT",
			transcript: "Republic 4583 climb and maintain 1-1-thousand",
			aircraft: map[string]Aircraft{
				"Republic 4583": {Callsign: "RPA4583", Altitude: 3000, State: "departure"},
			},
			expected: "RPA4583 C110",
		},
		{
			name:       "niner thousand as 9 or 1000",
			transcript: "American 17 descend and maintain, 9 or 1000",
			aircraft: map[string]Aircraft{
				"American 17": {Callsign: "AAL17", Altitude: 12000, State: "arrival"},
			},
			expected: "AAL17 D90",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBasicHeadingCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "turn left heading",
			transcript: "American 123 turn left heading two seven zero",
			aircraft: map[string]Aircraft{
				"American 123": {Callsign: "AAL123", State: "arrival"},
			},
			expected: "AAL123 L270",
		},
		{
			name:       "turn right heading with leading zero",
			transcript: "Delta 456 turn right heading zero niner zero",
			aircraft: map[string]Aircraft{
				"Delta 456": {Callsign: "DAL456", State: "arrival"},
			},
			expected: "DAL456 R090",
		},
		{
			name:       "fly present heading",
			transcript: "United 789 fly present heading",
			aircraft: map[string]Aircraft{
				"United 789": {Callsign: "UAL789", State: "arrival"},
			},
			expected: "UAL789 H",
		},
		{
			name:       "turn degrees left",
			transcript: "Southwest 333 turn twenty degrees left",
			aircraft: map[string]Aircraft{
				"Southwest 333": {Callsign: "SWA333", State: "arrival"},
			},
			expected: "SWA333 T20L",
		},
		{
			name:       "heading only",
			transcript: "JetBlue 100 heading one eight zero",
			aircraft: map[string]Aircraft{
				"JetBlue 100": {Callsign: "JBU100", State: "arrival"},
			},
			expected: "JBU100 H180",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBasicSpeedCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "reduce speed",
			transcript: "Alaska 500 reduce speed to two five zero",
			aircraft: map[string]Aircraft{
				"Alaska 500": {Callsign: "ASA500", State: "arrival"},
			},
			expected: "ASA500 S250",
		},
		{
			name:       "slowest practical",
			transcript: "Spirit 101 maintain slowest practical speed",
			aircraft: map[string]Aircraft{
				"Spirit 101": {Callsign: "NKS101", State: "on approach"},
			},
			expected: "NKS101 SMIN",
		},
		{
			name:       "increase speed",
			transcript: "Delta 200 increase speed to two eight zero",
			aircraft: map[string]Aircraft{
				"Delta 200": {Callsign: "DAL200", State: "departure"},
			},
			expected: "DAL200 S280",
		},
		{
			name:       "say airspeed",
			transcript: "American 300 say airspeed",
			aircraft: map[string]Aircraft{
				"American 300": {Callsign: "AAL300", State: "arrival"},
			},
			expected: "AAL300 SS",
		},
		{
			name:       "resume normal speed",
			transcript: "Delta 200 resume normal speed",
			aircraft: map[string]Aircraft{
				"Delta 200": {Callsign: "DAL200", State: "arrival"},
			},
			expected: "DAL200 S",
		},
		{
			name:       "say indicated speed",
			transcript: "United 452 say indicated speed",
			aircraft: map[string]Aircraft{
				"United 452": {Callsign: "UAL452", State: "overflight"},
			},
			expected: "UAL452 SI",
		},
		{
			name:       "say indicated airspeed",
			transcript: "Delta 200 say indicated airspeed",
			aircraft: map[string]Aircraft{
				"Delta 200": {Callsign: "DAL200", State: "arrival"},
			},
			expected: "DAL200 SI",
		},
		{
			name:       "say mach",
			transcript: "American 300 say mach",
			aircraft: map[string]Aircraft{
				"American 300": {Callsign: "AAL300", State: "overflight"},
			},
			expected: "AAL300 SM",
		},
		{
			name:       "say mach number",
			transcript: "Alaska 500 say mach number",
			aircraft: map[string]Aircraft{
				"Alaska 500": {Callsign: "ASA500", State: "overflight"},
			},
			expected: "ASA500 SM",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestMachSpeedCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "maintain mach point seven five",
			transcript: "Delta 200 maintain mach point seven five",
			aircraft: map[string]Aircraft{
				"Delta 200": {Callsign: "DAL200", State: "overflight"},
			},
			expected: "DAL200 M75",
		},
		{
			name:       "mach without point",
			transcript: "United 452 mach seven five",
			aircraft: map[string]Aircraft{
				"United 452": {Callsign: "UAL452", State: "overflight"},
			},
			expected: "UAL452 M75",
		},
		{
			name:       "reduce to mach",
			transcript: "American 300 reduce to mach point seven two",
			aircraft: map[string]Aircraft{
				"American 300": {Callsign: "AAL300", State: "arrival"},
			},
			expected: "AAL300 M72",
		},
		{
			name:       "increase to mach",
			transcript: "Alaska 500 increase to mach point eight zero",
			aircraft: map[string]Aircraft{
				"Alaska 500": {Callsign: "ASA500", State: "departure"},
			},
			expected: "ASA500 M80",
		},
		{
			name:       "single digit mach",
			transcript: "Spirit 101 maintain mach point seven",
			aircraft: map[string]Aircraft{
				"Spirit 101": {Callsign: "NKS101", State: "overflight"},
			},
			expected: "NKS101 M70",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestCompoundCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "speed then descend",
			transcript: "JetBlue 789 reduce speed to two five zero then descend and maintain one zero thousand",
			aircraft: map[string]Aircraft{
				"JetBlue 789": {Callsign: "JBU789", Altitude: 15000, State: "arrival"},
			},
			expected: "JBU789 S250 TD100",
		},
		{
			name:       "descend then speed",
			transcript: "American 100 descend and maintain eight thousand then reduce speed to two one zero",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", Altitude: 12000, State: "arrival"},
			},
			expected: "AAL100 D80 TS210",
		},
		{
			name:       "turn and descend",
			transcript: "United 333 turn left heading one eight zero descend and maintain six thousand",
			aircraft: map[string]Aircraft{
				"United 333": {Callsign: "UAL333", Altitude: 10000, State: "arrival"},
			},
			expected: "UAL333 L180 D60",
		},
		{
			name:       "cleared approach with speed until 5 mile final",
			transcript: "Turkish 10Z heavy cleared I L S runway two two left approach maintain speed 180 until 5 mile final",
			aircraft: map[string]Aircraft{
				"Turkish 10Z heavy": {
					Callsign:         "THY10Z",
					Altitude:         3000,
					State:            "arrival",
					AssignedApproach: "I2L", // Required for cleared approach validation
					CandidateApproaches: map[string]string{
						"I L S runway two two left": "I2L",
					},
				},
			},
			expected: "THY10Z CI2L S180/U5",
		},
		{
			name:       "cleared approach with joined ILS and missing runway",
			transcript: "American 717 cleared ILS 28 Center approach",
			aircraft: map[string]Aircraft{
				"American 717": {
					Callsign:         "AAL717",
					Altitude:         7000,
					State:            "arrival",
					AssignedApproach: "ILS Runway 28C",
					CandidateApproaches: map[string]string{
						"I L S runway two eight center": "I8C",
					},
				},
			},
			expected: "AAL717 CI8C",
		},
		{
			name:       "speed until DME",
			transcript: "Delta 456 speed one eight zero until five DME",
			aircraft: map[string]Aircraft{
				"Delta 456": {Callsign: "DAL456", Altitude: 3000, State: "arrival"},
			},
			expected: "DAL456 S180/U5DME",
		},
		{
			name:       "speed until 6 mile final",
			transcript: "Southwest 221 maintain speed one niner zero until six mile final",
			aircraft: map[string]Aircraft{
				"Southwest 221": {Callsign: "SWA221", Altitude: 4000, State: "arrival"},
			},
			expected: "SWA221 S190/U6",
		},
		{
			name:       "reduce speed until fix",
			transcript: "JetBlue 615 reduce speed to one seven zero until Rosley",
			aircraft: map[string]Aircraft{
				"JetBlue 615": {
					Callsign: "JBU615",
					Altitude: 5000,
					State:    "arrival",
					Fixes:    map[string]string{"Rosley": "ROSLY"},
				},
			},
			expected: "JBU615 S170/UROSLY",
		},
		{
			name:       "speed until with D M E spelled out",
			transcript: "American 100 speed one eight zero until five D M E",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", Altitude: 3500, State: "arrival"},
			},
			expected: "AAL100 S180/U5DME",
		},
		{
			name:       "speed until advised",
			transcript: "Delta 789 maintain one niner zero until advised",
			aircraft: map[string]Aircraft{
				"Delta 789": {Callsign: "DAL789", Altitude: 3000, State: "arrival"},
			},
			expected: "DAL789 S190",
		},
		{
			name:       "speed for now",
			transcript: "Southwest 123 maintain speed one eight zero for now",
			aircraft: map[string]Aircraft{
				"Southwest 123": {Callsign: "SWA123", Altitude: 4000, State: "arrival"},
			},
			expected: "SWA123 S180",
		},
		{
			name:       "cleared approach with speed until advised",
			transcript: "United 456 cleared ILS runway two eight left approach maintain one niner zero until advised",
			aircraft: map[string]Aircraft{
				"United 456": {
					Callsign:         "UAL456",
					Altitude:         3500,
					State:            "arrival",
					AssignedApproach: "ILS Runway 28L",
					CandidateApproaches: map[string]string{
						"I L S runway two eight left": "I28L",
					},
				},
			},
			expected: "UAL456 CI28L S190",
		},
		{
			name:       "cleared approach with speed for now",
			transcript: "American 789 cleared ILS runway three six approach speed one eight zero for now",
			aircraft: map[string]Aircraft{
				"American 789": {
					Callsign:         "AAL789",
					Altitude:         4000,
					State:            "arrival",
					AssignedApproach: "ILS Runway 36",
					CandidateApproaches: map[string]string{
						"I L S runway three six": "I36",
					},
				},
			},
			expected: "AAL789 CI36 S180",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSayAgainCommands(t *testing.T) {
	// Tests for SAYAGAIN commands - generated when STT recognizes command keywords
	// but fails to extract the associated value (e.g., garbled heading/altitude)
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "garbled heading after valid command",
			transcript: "American 123 descend and maintain five thousand fly heading blark bling",
			aircraft: map[string]Aircraft{
				"American 123": {Callsign: "AAL123", Altitude: 10000, State: "arrival"},
			},
			expected: "AAL123 D50 SAYAGAIN/HEADING",
		},
		{
			name:       "garbled altitude after callsign",
			transcript: "Delta 456 climb and maintain mumble jumble",
			aircraft: map[string]Aircraft{
				"Delta 456": {Callsign: "DAL456", Altitude: 5000, State: "departure"},
			},
			expected: "DAL456 SAYAGAIN/ALTITUDE",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestTransponderCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "squawk code",
			transcript: "Southwest 900 squawk one two zero zero",
			aircraft: map[string]Aircraft{
				"Southwest 900": {Callsign: "SWA900", State: "departure"},
			},
			expected: "SWA900 SQ1200",
		},
		{
			name:       "ident",
			transcript: "Spirit 111 ident",
			aircraft: map[string]Aircraft{
				"Spirit 111": {Callsign: "NKS111", State: "vfr flight following"},
			},
			expected: "NKS111 ID",
		},
		{
			name:       "squawk altitude",
			transcript: "Delta 222 squawk altitude",
			aircraft: map[string]Aircraft{
				"Delta 222": {Callsign: "DAL222", State: "departure"},
			},
			expected: "DAL222 SQA",
		},
		{
			name:       "transponder on",
			transcript: "Delta 222 transponder on",
			aircraft: map[string]Aircraft{
				"Delta 222": {Callsign: "DAL222", State: "departure"},
			},
			expected: "DAL222 SQON",
		},
		{
			name:       "squawk normal",
			transcript: "Delta 222 squawk normal",
			aircraft: map[string]Aircraft{
				"Delta 222": {Callsign: "DAL222", State: "departure"},
			},
			expected: "DAL222 SQON",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestHandoffCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "contact tower",
			transcript: "American 222 contact tower",
			aircraft: map[string]Aircraft{
				"American 222": {Callsign: "AAL222", State: "on approach"},
			},
			expected: "AAL222 TO",
		},
		{
			name:       "frequency change with frequency",
			transcript: "Delta 500 contact Los Angeles Center one three two point four",
			aircraft: map[string]Aircraft{
				"Delta 500": {Callsign: "DAL500", State: "overflight"},
			},
			expected: "DAL500 FC",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestVFRCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "go ahead",
			transcript: "Cessna 345 go ahead",
			aircraft: map[string]Aircraft{
				"Cessna 345": {Callsign: "N345", State: "vfr flight following"},
			},
			expected: "N345 GA",
		},
		{
			name:       "say request",
			transcript: "Cessna 345 uh say request",
			aircraft: map[string]Aircraft{
				"Cessna 345": {Callsign: "N345", State: "vfr flight following"},
			},
			expected: "N345 GA",
		},
		{
			name:       "radar services terminated",
			transcript: "November 123AB radar services terminated squawk VFR frequency change approved",
			aircraft: map[string]Aircraft{
				"November 123AB": {Callsign: "N123AB", State: "vfr flight following"},
			},
			expected: "N123AB RST",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestNavigationCommands(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "depart fix heading",
			transcript: "American 870 depart Pucky heading 180",
			aircraft: map[string]Aircraft{
				"American 870": {
					Callsign: "AAL870",
					Altitude: 19000,
					State:    "arrival",
					Fixes:    map[string]string{"Pucky": "PUCKY", "Deer Park": "DPK"},
				},
			},
			expected: "AAL870 DPUCKY/H180",
		},
		{
			name:       "depart fix heading with approach",
			transcript: "American 870 depart Pucky heading 180 vectors I L S runway two two left",
			aircraft: map[string]Aircraft{
				"American 870": {
					Callsign:            "AAL870",
					Altitude:            19000,
					State:               "arrival",
					Fixes:               map[string]string{"Pucky": "PUCKY"},
					CandidateApproaches: map[string]string{"I L S runway two two left": "I22L"},
				},
			},
			expected: "AAL870 DPUCKY/H180 EI22L",
		},
		{
			name:       "direct fix",
			transcript: "United 300 proceed direct JENNY",
			aircraft: map[string]Aircraft{
				"United 300": {
					Callsign: "UAL300",
					Altitude: 15000,
					State:    "arrival",
					Fixes:    map[string]string{"jenny": "JENNY"},
				},
			},
			expected: "UAL300 DJENNY",
		},
		{
			name:       "procedure at misrecognized as proceed direct",
			transcript: "Delta 450 procedure at MERIT",
			aircraft: map[string]Aircraft{
				"Delta 450": {
					Callsign: "DAL450",
					Altitude: 10000,
					State:    "arrival",
					Fixes:    map[string]string{"merit": "MERIT"},
				},
			},
			expected: "DAL450 DMERIT",
		},
		{
			name:       "depart fix heading with fuzzy keyword",
			transcript: "American 870 tepart pucky heading 180",
			aircraft: map[string]Aircraft{
				"American 870": {
					Callsign: "AAL870",
					Altitude: 19000,
					State:    "arrival",
					Fixes:    map[string]string{"Pucky": "PUCKY"},
				},
			},
			expected: "AAL870 DPUCKY/H180",
		},
		{
			name:       "at fix cleared approach",
			transcript: "Delta 8499 at Fergi clear for the River Visual runway one niner approach",
			aircraft: map[string]Aircraft{
				"Delta 8499": {
					Callsign:            "DAL8499",
					Altitude:            4000,
					State:               "arrival",
					AssignedApproach:    "RIV",
					Fixes:               map[string]string{"Fergi": "FERGI"},
					CandidateApproaches: map[string]string{"River Visual runway one niner": "RIV"},
				},
			},
			expected: "DAL8499 AFERGI/CRIV",
		},
		{
			name:       "at fix intercept localizer",
			transcript: "Delta 8499 at Fergi intercept the localizer",
			aircraft: map[string]Aircraft{
				"Delta 8499": {
					Callsign:         "DAL8499",
					Altitude:         4000,
					State:            "arrival",
					AssignedApproach: "I22L",
					Fixes:            map[string]string{"Fergi": "FERGI"},
				},
			},
			expected: "DAL8499 AFERGI/I",
		},
		{
			name:       "at fix intercept with runway identifier",
			transcript: "United 123 at Rosly intercept the 2 2 left localizer",
			aircraft: map[string]Aircraft{
				"United 123": {
					Callsign:         "UAL123",
					Altitude:         3000,
					State:            "arrival",
					AssignedApproach: "I22L",
					Fixes:            map[string]string{"Rosly": "ROSLY"},
				},
			},
			expected: "UAL123 AROSLY/I",
		},
		{
			name:       "at fix intercept with runway keyword",
			transcript: "American 456 at Merit intercept the runway 3 1 right localizer",
			aircraft: map[string]Aircraft{
				"American 456": {
					Callsign:         "AAL456",
					Altitude:         5000,
					State:            "arrival",
					AssignedApproach: "I31R",
					Fixes:            map[string]string{"Merit": "MERIT"},
				},
			},
			expected: "AAL456 AMERIT/I",
		},
		// Hold commands
		{
			name:       "hold at fix as published",
			transcript: "American 500 hold at MERIT as published",
			aircraft: map[string]Aircraft{
				"American 500": {
					Callsign: "AAL500",
					Altitude: 8000,
					State:    "arrival",
					Fixes:    map[string]string{"MERIT": "MERIT"},
				},
			},
			expected: "AAL500 HMERIT",
		},
		{
			name:       "hold direction of fix as published",
			transcript: "Delta 200 hold north of JIMEE as published",
			aircraft: map[string]Aircraft{
				"Delta 200": {
					Callsign: "DAL200",
					Altitude: 10000,
					State:    "arrival",
					Fixes:    map[string]string{"JIMEE": "JIMEE"},
				},
			},
			expected: "DAL200 HJIMEE",
		},
		{
			name:       "hold direction of fix as published with maintain",
			transcript: "United 300 hold south of BETTE as published maintain 6000",
			aircraft: map[string]Aircraft{
				"United 300": {
					Callsign: "UAL300",
					Altitude: 10000,
					State:    "arrival",
					Fixes:    map[string]string{"BETTE": "BETTE"},
				},
			},
			expected: "UAL300 HBETTE A60",
		},
		{
			name:       "hold with expect further clearance ignored",
			transcript: "Southwest 400 hold at MERIT as published expect further clearance 1 2 3 0",
			aircraft: map[string]Aircraft{
				"Southwest 400": {
					Callsign: "SWA400",
					Altitude: 8000,
					State:    "arrival",
					Fixes:    map[string]string{"MERIT": "MERIT"},
				},
			},
			expected: "SWA400 HMERIT",
		},
		{
			name:       "hold controller specified with radial and turns",
			transcript: "JetBlue 600 hold west of MERIT on the 280 radial inbound 2 minute legs left turns",
			aircraft: map[string]Aircraft{
				"JetBlue 600": {
					Callsign: "JBU600",
					Altitude: 8000,
					State:    "arrival",
					Fixes:    map[string]string{"MERIT": "MERIT"},
				},
			},
			expected: "JBU600 HMERIT/R280/2M/L",
		},
		{
			name:       "hold controller specified with radial right turns default",
			transcript: "Alaska 700 hold east of JIMEE on the 90 radial 3 minute legs right turns",
			aircraft: map[string]Aircraft{
				"Alaska 700": {
					Callsign: "ASA700",
					Altitude: 10000,
					State:    "arrival",
					Fixes:    map[string]string{"JIMEE": "JIMEE"},
				},
			},
			expected: "ASA700 HJIMEE/R90/3M/R",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestCallsignMatchingPriority(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "exact flight number beats fuzzy airline with wrong number",
			transcript: "Detva 5778 turn 10 degrees left",
			aircraft: map[string]Aircraft{
				"Delta 2991":    {Callsign: "DAL2991", State: "arrival"},
				"Endeavor 5778": {Callsign: "EDV5778", State: "arrival"},
			},
			expected: "EDV5778 T10L",
		},
		{
			name:       "exact airline and number beats number-only match",
			transcript: "Delta 2991 turn left heading 270",
			aircraft: map[string]Aircraft{
				"Delta 2991":    {Callsign: "DAL2991", State: "arrival"},
				"Endeavor 2991": {Callsign: "EDV2991", State: "arrival"},
			},
			expected: "DAL2991 L270",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestHeadingVsDegreesDisambiguation(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "turn left 100 is heading not degrees",
			transcript: "Delta 123 turn left 100",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 L100",
		},
		{
			name:       "turn 30 left is degrees turn",
			transcript: "Delta 123 turn 30 left",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 T30L",
		},
		{
			name:       "turn left 30 degrees is degrees turn",
			transcript: "Delta 123 turn left 30 degrees",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 T30L",
		},
		{
			name:       "turn 20 degrees right is degrees turn",
			transcript: "Delta 123 turn 20 degrees right",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 T20R",
		},
		{
			name:       "turn right 45 degrees is degrees turn",
			transcript: "Delta 123 turn right 45 degrees",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 T45R",
		},
		{
			name:       "turn left heading 100 is heading",
			transcript: "Delta 123 turn left heading 100",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 L100",
		},
		{
			name:       "turn right 200 is heading not degrees",
			transcript: "Delta 123 turn right 200",
			aircraft: map[string]Aircraft{
				"Delta 123": {Callsign: "DAL123", State: "arrival"},
			},
			expected: "DAL123 R200",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSTTErrorRecovery(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "phonetic numbers in callsign",
			transcript: "American fife niner tree six descended and maintained 8000",
			aircraft: map[string]Aircraft{
				"American 5936": {Callsign: "AAL5936", Altitude: 12000, State: "arrival"},
			},
			expected: "AAL5936 D80",
		},
		{
			name:       "garbage word at start of transcript",
			transcript: "Lass China Southern 940 heavy fly heading 180",
			aircraft: map[string]Aircraft{
				"China Southern 940 heavy": {Callsign: "CSN940", State: "arrival"},
			},
			expected: "CSN940 H180",
		},
		{
			name:       "fight instead of flight (STT error)",
			transcript: "American 49 maintain fight level two one zero",
			aircraft: map[string]Aircraft{
				"American 49": {Callsign: "AAL49", Altitude: 25000, State: "overflight"},
			},
			expected: "AAL49 A210",
		},
		{
			name:       "climin instead of climb and (STT error)",
			transcript: "November 355UC proceed direct Forpe climin maintain flight level two one zero",
			aircraft: map[string]Aircraft{
				"November 355UC": {Callsign: "N355UC", Altitude: 15000, State: "overflight", Fixes: map[string]string{"Forpe": "FORPE"}},
			},
			expected: "N355UC DFORPE C210",
		},
		{
			name:       "clementine instead of climb and maintain (STT error)",
			transcript: "November 355UC Clementine 10000",
			aircraft: map[string]Aircraft{
				"November 355UC": {Callsign: "N355UC", Altitude: 5000, State: "departure"},
			},
			expected: "N355UC C100",
		},
		{
			name:       "con instead of climb (STT error)",
			transcript: "Frontier 5165 con and maintain flight level two one zero",
			aircraft: map[string]Aircraft{
				"Frontier 5165": {Callsign: "FFT5165", Altitude: 15000, State: "overflight"},
			},
			expected: "FFT5165 C210",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDisregardHandling(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "disregard cancels previous",
			transcript: "American 100 turn left no disregard turn right heading two seven zero",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival"},
			},
			expected: "AAL100 R270",
		},
		// Correction with command type preservation tests
		{
			name:       "correction preserves heading context",
			transcript: "American 100 fly heading two seven zero correction two niner zero join localizer",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival"},
			},
			expected: "AAL100 H290 I",
		},
		{
			name:       "correction preserves speed context",
			transcript: "American 100 reduce speed to two one zero correction one niner zero then descend and maintain four thousand",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival", Altitude: 5000},
			},
			expected: "AAL100 S190 TD40",
		},
		{
			name:       "correction preserves altitude context",
			transcript: "American 100 descend and maintain five thousand correction four thousand speed two one zero",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival", Altitude: 6000},
			},
			expected: "AAL100 D40 S210",
		},
		{
			name:       "correction with turn left preserves heading",
			transcript: "American 100 turn left heading one eight zero correction two zero zero speed one eight zero",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival"},
			},
			expected: "AAL100 H200 S180",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "empty transcript",
			transcript: "",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival"},
			},
			expected: "",
		},
		{
			name:       "no callsign match",
			transcript: "unintelligible static noise",
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100", State: "arrival"},
			},
			expected: "",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// Test position identification detection
func TestPositionIdentification(t *testing.T) {
	tests := []struct {
		name             string
		transcript       string
		aircraft         map[string]Aircraft
		radioName        string
		expected         string
		expectPositionID bool
	}{
		{
			name:       "new york departure exact match",
			transcript: "Encore 208 New York departure",
			aircraft: map[string]Aircraft{
				"Encore 208": {Callsign: "WEN208", State: "departure"},
			},
			radioName:        "New York Departure",
			expected:         "",
			expectPositionID: true,
		},
		{
			name:       "new york approach when radio is departure (interchangeable)",
			transcript: "Encore 208 New York approach",
			aircraft: map[string]Aircraft{
				"Encore 208": {Callsign: "WEN208", State: "departure"},
			},
			radioName:        "New York Departure",
			expected:         "",
			expectPositionID: true,
		},
		{
			name:       "position ID with radar contact suffix",
			transcript: "Delta 100 New York departure radar contact",
			aircraft: map[string]Aircraft{
				"Delta 100": {Callsign: "DAL100", State: "departure"},
			},
			radioName:        "New York Departure",
			expected:         "",
			expectPositionID: true,
		},
		{
			name:       "fuzzy facility match",
			transcript: "American 5936 neww york departure",
			aircraft: map[string]Aircraft{
				"American 5936": {Callsign: "AAL5936", State: "departure"},
			},
			radioName:        "New York Departure",
			expected:         "",
			expectPositionID: true,
		},
		{
			name:       "no position ID - actual command",
			transcript: "Encore 208 climb and maintain 8000",
			aircraft: map[string]Aircraft{
				"Encore 208": {Callsign: "WEN208", State: "departure"},
			},
			radioName:        "New York Departure",
			expected:         "WEN208 C80",
			expectPositionID: false,
		},
		{
			name:       "position ID without radio name",
			transcript: "Encore 208 New York departure",
			aircraft: map[string]Aircraft{
				"Encore 208": {Callsign: "WEN208", State: "departure"},
			},
			radioName:        "", // No radio name - still detect position ID
			expected:         "",
			expectPositionID: true,
		},
		{
			name:       "radar contact in middle with commands after",
			transcript: "JetBlue 2655 New York departure radar contact direct BETTE",
			aircraft: map[string]Aircraft{
				"JetBlue 2655": {Callsign: "JBU2655", State: "departure", Fixes: map[string]string{"Betty": "BETTE"}},
			},
			radioName:        "New York Departure",
			expected:         "JBU2655 DBETTE",
			expectPositionID: false,
		},
		{
			name:       "position ID prefix followed by direct and climb commands",
			transcript: "JetBlue 25 New York departure proceed direct Wavy climb and maintain 12000",
			aircraft: map[string]Aircraft{
				"JetBlue 25": {Callsign: "JBU25", State: "departure", Fixes: map[string]string{"Wavey": "WAVEY"}},
			},
			radioName:        "New York Approach",
			expected:         "JBU25 DWAVEY C120",
			expectPositionID: false,
		},
		{
			name:       "roger only - no command",
			transcript: "Delta 88 roger",
			aircraft: map[string]Aircraft{
				"Delta 88": {Callsign: "DAL88", Altitude: 3000, State: "departure"},
			},
			radioName: "New York Departure",
			expected:  "",
		},
		{
			name:       "roger followed by command",
			transcript: "Delta 88 roger climb and maintain niner thousand",
			aircraft: map[string]Aircraft{
				"Delta 88": {Callsign: "DAL88", Altitude: 3000, State: "departure"},
			},
			radioName: "New York Departure",
			expected:  "DAL88 C90",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, tt.radioName)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// Unit tests for individual components

func TestJaroWinkler(t *testing.T) {
	tests := []struct {
		s1, s2   string
		minScore float64
	}{
		{"american", "american", 1.0},
		{"american", "amurican", 0.9},
		{"delta", "delta", 1.0},
		{"delta", "deltta", 0.9},
		{"", "", 1.0},
		{"abc", "", 0.0},
		// STT transcription errors for command keywords
		{"tepart", "depart", 0.8}, // Common mistranscription
		{"descend", "descend", 1.0},
		{"decend", "descend", 0.8},
	}

	for _, tt := range tests {
		score := JaroWinkler(tt.s1, tt.s2)
		if score < tt.minScore {
			t.Errorf("JaroWinkler(%q, %q) = %.2f, want >= %.2f", tt.s1, tt.s2, score, tt.minScore)
		}
	}
}

func TestPhoneticMatch(t *testing.T) {
	tests := []struct {
		w1, w2 string
		match  bool
	}{
		{"american", "amurican", true},
		{"delta", "deltta", true},
		{"tree", "three", true},
		{"cat", "dog", false},
		// STT transcription errors - should match phonetically
		{"tepart", "depart", true}, // T and D both encode to T in phonetic
	}

	for _, tt := range tests {
		result := PhoneticMatch(tt.w1, tt.w2)
		if result != tt.match {
			t.Errorf("PhoneticMatch(%q, %q) = %v, want %v", tt.w1, tt.w2, result, tt.match)
		}
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		word, target string
		threshold    float64
		match        bool
	}{
		// Exact match
		{"depart", "depart", 0.8, true},
		// JaroWinkler match
		{"tepart", "depart", 0.8, true}, // Common STT error - first letter wrong
		// No match
		{"xyz", "depart", 0.8, false},
		// Phonetic match (even if JaroWinkler might be lower)
		{"tree", "three", 0.8, true},
	}

	for _, tt := range tests {
		result := FuzzyMatch(tt.word, tt.target, tt.threshold)
		if result != tt.match {
			// Also print the JaroWinkler score for debugging
			score := JaroWinkler(tt.word, tt.target)
			phonetic := PhoneticMatch(tt.word, tt.target)
			t.Errorf("FuzzyMatch(%q, %q, %.1f) = %v, want %v (JW=%.3f, phonetic=%v)",
				tt.word, tt.target, tt.threshold, result, tt.match, score, phonetic)
		}
	}
}

func TestNormalizeTranscript(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"tree fower too", []string{"3", "4", "2"}},
		{"American 123", []string{"american", "123"}}, // Airline names stay as-is; matching happens via aircraft context
		// Note: disregard handling moved to provider level after callsign matching
		{"turn left disregard turn right", []string{"turn", "left", "disregard", "turn", "right"}},
		{"1-1-thousand", []string{"1", "1", "thousand"}}, // Hyphens split into separate words
		// "niner" sometimes transcribed as "nine or" - should skip "or" between digits
		{"two nine or zero", []string{"2", "9", "0"}},
		{"heading two niner zero", []string{"heading", "2", "9", "0"}},
		{"", nil},
		// "niner thousand" transcribed as "9 or 1000" - should convert 1000 to thousand
		{"descend and maintain, 9 or 1000", []string{"descend", "and", "maintain", "9", "thousand"}},
		// "fly heading" sometimes transcribed as "flighting"
		{"flighting 030", []string{"fly", "heading", "030"}},
	}

	for _, tt := range tests {
		result := NormalizeTranscript(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("NormalizeTranscript(%q) = %v, want %v", tt.input, result, tt.expected)
			continue
		}
		for i := range result {
			if result[i] != tt.expected[i] {
				t.Errorf("NormalizeTranscript(%q)[%d] = %q, want %q", tt.input, i, result[i], tt.expected[i])
			}
		}
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input    []string
		numToks  int
		firstVal int
	}{
		{[]string{"8", "thousand"}, 1, 80},                   // altitude
		{[]string{"flight", "level", "3", "5", "0"}, 1, 350}, // FL
		{[]string{"2", "7", "0"}, 1, 270},                    // heading
	}

	for _, tt := range tests {
		result := Tokenize(tt.input)
		if len(result) != tt.numToks {
			t.Errorf("Tokenize(%v) got %d tokens, want %d", tt.input, len(result), tt.numToks)
			continue
		}
		if tt.numToks > 0 && result[0].Value != tt.firstVal {
			t.Errorf("Tokenize(%v)[0].Value = %d, want %d", tt.input, result[0].Value, tt.firstVal)
		}
	}
}

// Benchmark for performance verification

func BenchmarkDecodeTranscript(b *testing.B) {
	provider := NewTranscriber(nil)
	aircraft := map[string]Aircraft{
		"American 5936": {
			Callsign: "AAL5936",
			Altitude: 12000,
			State:    "arrival",
			Fixes: map[string]string{
				"deer park": "DPK",
				"jenny":     "JENNY",
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.DecodeTranscript(aircraft, "American 5936 descend and maintain 8000", "")
	}
}

func BenchmarkDecodeTranscriptComplex(b *testing.B) {
	provider := NewTranscriber(nil)
	aircraft := map[string]Aircraft{
		"JetBlue 789": {
			Callsign: "JBU789",
			Altitude: 15000,
			State:    "arrival",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.DecodeTranscript(aircraft, "JetBlue 789 reduce speed to two five zero then descend and maintain one zero thousand", "")
	}
}

// =============================================================================
// Unit Tests for commands.go
// =============================================================================

func TestParseCommandsBasic(t *testing.T) {
	tests := []struct {
		name     string
		tokens   []Token
		ac       Aircraft
		expected []string
	}{
		{
			name: "descend command",
			tokens: []Token{
				{Text: "descend", Type: TokenWord},
				{Text: "maintain", Type: TokenWord},
				{Text: "80", Type: TokenAltitude, Value: 80},
			},
			ac:       Aircraft{Altitude: 12000, State: "arrival"},
			expected: []string{"D80"},
		},
		{
			name: "climb command",
			tokens: []Token{
				{Text: "climb", Type: TokenWord},
				{Text: "90", Type: TokenAltitude, Value: 90},
			},
			ac:       Aircraft{Altitude: 5000, State: "departure"},
			expected: []string{"C90"},
		},
		{
			name: "turn left heading",
			tokens: []Token{
				{Text: "turn", Type: TokenWord},
				{Text: "left", Type: TokenWord},
				{Text: "heading", Type: TokenWord},
				{Text: "270", Type: TokenNumber, Value: 270},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"L270"},
		},
		{
			name: "turn right heading",
			tokens: []Token{
				{Text: "turn", Type: TokenWord},
				{Text: "right", Type: TokenWord},
				{Text: "heading", Type: TokenWord},
				{Text: "090", Type: TokenNumber, Value: 90},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"R090"},
		},
		{
			name: "reduce speed",
			tokens: []Token{
				{Text: "reduce", Type: TokenWord},
				{Text: "speed", Type: TokenWord},
				{Text: "250", Type: TokenNumber, Value: 250},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"S250"},
		},
		{
			name: "expedite climb",
			tokens: []Token{
				{Text: "expedite", Type: TokenWord},
				{Text: "climb", Type: TokenWord},
			},
			ac:       Aircraft{State: "departure"},
			expected: []string{"EC"},
		},
		{
			name: "expedite descent",
			tokens: []Token{
				{Text: "expedite", Type: TokenWord},
				{Text: "descent", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"ED"},
		},
		{
			name:     "empty tokens",
			tokens:   []Token{},
			ac:       Aircraft{State: "arrival"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, _ := ParseCommands(tt.tokens, tt.ac)
			if len(commands) != len(tt.expected) {
				t.Errorf("ParseCommands() got %v, want %v", commands, tt.expected)
				return
			}
			for i := range commands {
				if commands[i] != tt.expected[i] {
					t.Errorf("ParseCommands()[%d] = %q, want %q", i, commands[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParseCommandsThenSequencing(t *testing.T) {
	tests := []struct {
		name     string
		tokens   []Token
		ac       Aircraft
		expected []string
	}{
		{
			name: "speed then descend",
			tokens: []Token{
				{Text: "reduce", Type: TokenWord},
				{Text: "speed", Type: TokenWord},
				{Text: "250", Type: TokenNumber, Value: 250},
				{Text: "then", Type: TokenWord},
				{Text: "descend", Type: TokenWord},
				{Text: "80", Type: TokenAltitude, Value: 80},
			},
			ac:       Aircraft{Altitude: 12000, State: "arrival"},
			expected: []string{"S250", "TD80"},
		},
		{
			name: "descend then speed",
			tokens: []Token{
				{Text: "descend", Type: TokenWord},
				{Text: "80", Type: TokenAltitude, Value: 80},
				{Text: "then", Type: TokenWord},
				{Text: "reduce", Type: TokenWord},
				{Text: "210", Type: TokenNumber, Value: 210},
			},
			ac:       Aircraft{Altitude: 12000, State: "arrival"},
			expected: []string{"D80", "TS210"},
		},
		{
			name: "at altitude triggers then",
			tokens: []Token{
				{Text: "at", Type: TokenWord},
				{Text: "30", Type: TokenAltitude, Value: 30},
				{Text: "reduce", Type: TokenWord},
				{Text: "speed", Type: TokenWord},
				{Text: "180", Type: TokenNumber, Value: 180},
			},
			ac:       Aircraft{Altitude: 5000, State: "arrival"},
			expected: []string{"TS180"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, _ := ParseCommands(tt.tokens, tt.ac)
			if len(commands) != len(tt.expected) {
				t.Errorf("ParseCommands() got %v, want %v", commands, tt.expected)
				return
			}
			for i := range commands {
				if commands[i] != tt.expected[i] {
					t.Errorf("ParseCommands()[%d] = %q, want %q", i, commands[i], tt.expected[i])
				}
			}
		})
	}
}

func TestParseCommandsPriorityResolution(t *testing.T) {
	tests := []struct {
		name     string
		tokens   []Token
		ac       Aircraft
		expected []string
		desc     string
	}{
		{
			name: "descend_maintain beats descend (higher priority)",
			tokens: []Token{
				{Text: "descend", Type: TokenWord},
				{Text: "maintain", Type: TokenWord},
				{Text: "80", Type: TokenAltitude, Value: 80},
			},
			ac:       Aircraft{Altitude: 12000, State: "arrival"},
			expected: []string{"D80"},
			desc:     "Both match but descend_maintain (priority 10) wins over descend (priority 5)",
		},
		{
			name: "climb_maintain beats climb (higher priority)",
			tokens: []Token{
				{Text: "climb", Type: TokenWord},
				{Text: "maintain", Type: TokenWord},
				{Text: "100", Type: TokenAltitude, Value: 100},
			},
			ac:       Aircraft{Altitude: 5000, State: "departure"},
			expected: []string{"C100"},
			desc:     "climb_maintain (priority 10) beats climb (priority 5)",
		},
		{
			name: "turn_left_heading beats heading_only",
			tokens: []Token{
				{Text: "turn", Type: TokenWord},
				{Text: "left", Type: TokenWord},
				{Text: "heading", Type: TokenWord},
				{Text: "270", Type: TokenNumber, Value: 270},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"L270"},
			desc:     "turn_left_heading (priority 10) beats heading_only (priority 5)",
		},
		{
			name: "climb_via_sid beats climb",
			tokens: []Token{
				{Text: "climb", Type: TokenWord},
				{Text: "via", Type: TokenWord},
				{Text: "the", Type: TokenWord},
				{Text: "sid", Type: TokenWord},
			},
			ac:       Aircraft{State: "departure"},
			expected: []string{"CVS"},
			desc:     "climb_via_sid (priority 15) beats climb (priority 5)",
		},
		{
			name: "present_heading beats heading_only",
			tokens: []Token{
				{Text: "present", Type: TokenWord},
				{Text: "heading", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"H"},
			desc:     "present_heading (priority 12) beats heading_only (priority 5)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, _ := ParseCommands(tt.tokens, tt.ac)
			if len(commands) != len(tt.expected) {
				t.Errorf("%s: got %v, want %v", tt.desc, commands, tt.expected)
				return
			}
			for i := range commands {
				if commands[i] != tt.expected[i] {
					t.Errorf("%s: [%d] = %q, want %q", tt.desc, i, commands[i], tt.expected[i])
				}
			}
		})
	}
}

func TestVisualApproachSTTPatterns(t *testing.T) {
	tests := []struct {
		name     string
		tokens   []Token
		ac       Aircraft
		expected []string
	}{
		{
			name: "cleared visual approach produces CVA",
			tokens: []Token{
				{Text: "cleared", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "approach", Type: TokenWord},
				{Text: "runway", Type: TokenWord},
				{Text: "13", Type: TokenNumber, Value: 13},
				{Text: "left", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"CVA13L"},
		},
		{
			name: "cleared visual without runway keyword",
			tokens: []Token{
				{Text: "cleared", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "26", Type: TokenNumber, Value: 26},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"CVA26"},
		},
		{
			name: "expect visual approach produces EVA not charted visual",
			tokens: []Token{
				{Text: "expect", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "approach", Type: TokenWord},
				{Text: "runway", Type: TokenWord},
				{Text: "22", Type: TokenNumber, Value: 22},
				{Text: "left", Type: TokenWord},
			},
			ac: Aircraft{
				State: "arrival",
				CandidateApproaches: map[string]string{
					"Visual belmont runway two two left": "VB2L", // charted visual — should NOT match
					"I L S runway two two left":         "I22L",
				},
			},
			expected: []string{"EVA22L"},
		},
		{
			name: "expect visual approach without left/right",
			tokens: []Token{
				{Text: "expect", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "approach", Type: TokenWord},
				{Text: "runway", Type: TokenWord},
				{Text: "31", Type: TokenNumber, Value: 31},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"EVA31"},
		},
		{
			name: "vectors visual approach produces EVA",
			tokens: []Token{
				{Text: "vectors", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "approach", Type: TokenWord},
				{Text: "runway", Type: TokenWord},
				{Text: "31", Type: TokenNumber, Value: 31},
				{Text: "right", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"EVA31R"},
		},
		{
			name: "vectors for the visual approach",
			tokens: []Token{
				{Text: "vectors", Type: TokenWord},
				{Text: "for", Type: TokenWord},
				{Text: "the", Type: TokenWord},
				{Text: "visual", Type: TokenWord},
				{Text: "approach", Type: TokenWord},
				{Text: "runway", Type: TokenWord},
				{Text: "4", Type: TokenNumber, Value: 4},
				{Text: "right", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"EVA4R"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, _ := ParseCommands(tt.tokens, tt.ac)
			if len(commands) != len(tt.expected) {
				t.Errorf("ParseCommands() got %v, want %v", commands, tt.expected)
				return
			}
			for i := range commands {
				if commands[i] != tt.expected[i] {
					t.Errorf("ParseCommands()[%d] = %q, want %q", i, commands[i], tt.expected[i])
				}
			}
		})
	}
}

func TestAirportAdvisorySTTPatterns(t *testing.T) {
	provider := NewTranscriber(nil)

	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "basic airport advisory",
			transcript: "American 123 airport twelve o'clock eight miles",
			aircraft: map[string]Aircraft{
				"American 123": {Callsign: "AAL123", State: "arrival", Altitude: 5000},
			},
			expected: "AAL123 AP/12/8",
		},
		{
			name:       "field advisory",
			transcript: "American 123 field eleven o'clock six miles",
			aircraft: map[string]Aircraft{
				"American 123": {Callsign: "AAL123", State: "arrival", Altitude: 5000},
			},
			expected: "AAL123 AP/11/6",
		},
		{
			name:       "airport name before o'clock position",
			transcript: "American 123 kennedy at your eleven o'clock eight miles",
			aircraft: map[string]Aircraft{
				"American 123": {
					Callsign: "AAL123",
					State:    "arrival",
					Altitude: 5000,
					Fixes:    map[string]string{"kennedy": "KJFK"},
				},
			},
			expected: "AAL123 AP/11/8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractAltitude(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		expectedAlt  int
		expectedCons int
	}{
		{
			name: "altitude token",
			tokens: []Token{
				{Text: "80", Type: TokenAltitude, Value: 80},
			},
			expectedAlt:  80,
			expectedCons: 1,
		},
		{
			name: "number in valid altitude range",
			tokens: []Token{
				{Text: "50", Type: TokenNumber, Value: 50},
			},
			expectedAlt:  50,
			expectedCons: 1,
		},
		{
			name: "large number (raw feet)",
			tokens: []Token{
				{Text: "8000", Type: TokenNumber, Value: 8000},
			},
			expectedAlt:  80,
			expectedCons: 1,
		},
		{
			name: "single digit converted to thousands",
			tokens: []Token{
				{Text: "9", Type: TokenNumber, Value: 9},
			},
			expectedAlt:  90,
			expectedCons: 1,
		},
		{
			name: "skip miles - not altitude",
			tokens: []Token{
				{Text: "5", Type: TokenNumber, Value: 5},
				{Text: "miles", Type: TokenWord},
			},
			expectedAlt:  0,
			expectedCons: 0,
		},
		{
			name:         "empty tokens",
			tokens:       []Token{},
			expectedAlt:  0,
			expectedCons: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alt, consumed := extractAltitude(tt.tokens)
			if alt != tt.expectedAlt {
				t.Errorf("extractAltitude() alt = %d, want %d", alt, tt.expectedAlt)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractAltitude() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestExtractHeading(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		expectedHdg  int
		expectedCons int
	}{
		{
			name: "number in valid heading range",
			tokens: []Token{
				{Text: "180", Type: TokenNumber, Value: 180},
			},
			expectedHdg:  180,
			expectedCons: 1,
		},
		{
			name: "heading 360",
			tokens: []Token{
				{Text: "360", Type: TokenNumber, Value: 360},
			},
			expectedHdg:  360,
			expectedCons: 1,
		},
		{
			name:         "empty tokens",
			tokens:       []Token{},
			expectedHdg:  0,
			expectedCons: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hdg, consumed := extractHeading(tt.tokens)
			if hdg != tt.expectedHdg {
				t.Errorf("extractHeading() hdg = %d, want %d", hdg, tt.expectedHdg)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractHeading() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestExtractSpeed(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		expectedSpd  int
		expectedCons int
	}{
		{
			name: "number in valid speed range",
			tokens: []Token{
				{Text: "210", Type: TokenNumber, Value: 210},
			},
			expectedSpd:  210,
			expectedCons: 1,
		},
		{
			name:         "empty tokens",
			tokens:       []Token{},
			expectedSpd:  0,
			expectedCons: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spd, consumed := extractSpeed(tt.tokens)
			if spd != tt.expectedSpd {
				t.Errorf("extractSpeed() spd = %d, want %d", spd, tt.expectedSpd)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractSpeed() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestExtractFix(t *testing.T) {
	fixes := map[string]string{
		"jenny":     "JENNY",
		"deer park": "DPK",
		"pucky":     "PUCKY",
		"gayel":     "GAYEL",
	}

	tests := []struct {
		name         string
		tokens       []Token
		expectedFix  string
		expectedCons int
	}{
		{
			name: "exact match",
			tokens: []Token{
				{Text: "jenny", Type: TokenWord},
			},
			expectedFix:  "JENNY",
			expectedCons: 1,
		},
		{
			name: "multi-word fix",
			tokens: []Token{
				{Text: "deer", Type: TokenWord},
				{Text: "park", Type: TokenWord},
			},
			expectedFix:  "DPK",
			expectedCons: 2,
		},
		{
			name: "fuzzy match",
			tokens: []Token{
				{Text: "jenney", Type: TokenWord},
			},
			expectedFix:  "JENNY",
			expectedCons: 1,
		},
		{
			name: "vowel normalization (gail -> gayel)",
			tokens: []Token{
				{Text: "gail", Type: TokenWord},
			},
			expectedFix:  "GAYEL",
			expectedCons: 1,
		},
		{
			name:         "no match",
			tokens:       []Token{{Text: "xyz", Type: TokenWord}},
			expectedFix:  "",
			expectedCons: 0,
		},
		{
			name:         "empty tokens",
			tokens:       []Token{},
			expectedFix:  "",
			expectedCons: 0,
		},
		// Spelling correction tests
		{
			name: "spelling with thats trigger - deer park thats delta papa kilo",
			tokens: []Token{
				{Text: "deer", Type: TokenWord},
				{Text: "park", Type: TokenWord},
				{Text: "thats", Type: TokenWord},
				{Text: "delta", Type: TokenWord},
				{Text: "papa", Type: TokenWord},
				{Text: "kilo", Type: TokenWord},
			},
			expectedFix:  "DPK",
			expectedCons: 6, // all tokens consumed
		},
		{
			name: "spelling confirms match - jenny thats juliet echo november november yankee",
			tokens: []Token{
				{Text: "jenny", Type: TokenWord},
				{Text: "thats", Type: TokenWord},
				{Text: "juliet", Type: TokenWord},
				{Text: "echo", Type: TokenWord},
				{Text: "november", Type: TokenWord},
				{Text: "november", Type: TokenWord},
				{Text: "yankee", Type: TokenWord},
			},
			expectedFix:  "JENNY",
			expectedCons: 7, // all tokens consumed
		},
		{
			name: "spelling without trigger - pucky papa uniform charlie kilo yankee",
			tokens: []Token{
				{Text: "pucky", Type: TokenWord},
				{Text: "papa", Type: TokenWord},
				{Text: "uniform", Type: TokenWord},
				{Text: "charlie", Type: TokenWord},
				{Text: "kilo", Type: TokenWord},
				{Text: "yankee", Type: TokenWord},
			},
			expectedFix:  "PUCKY",
			expectedCons: 6, // all tokens consumed
		},
		{
			name: "spelling overrides garbled spoken name",
			tokens: []Token{
				{Text: "deerpak", Type: TokenWord}, // garbled "deer park"
				{Text: "thats", Type: TokenWord},
				{Text: "delta", Type: TokenWord},
				{Text: "papa", Type: TokenWord},
				{Text: "kilo", Type: TokenWord},
			},
			expectedFix:  "DPK",
			expectedCons: 5, // all tokens consumed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fix, _, consumed := extractFix(tt.tokens, fixes)
			if fix != tt.expectedFix {
				t.Errorf("extractFix() fix = %q, want %q", fix, tt.expectedFix)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractFix() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestExtractSquawk(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		expectedCode string
		expectedCons int
	}{
		{
			name: "four digit tokens",
			tokens: []Token{
				{Text: "1", Type: TokenWord},
				{Text: "2", Type: TokenWord},
				{Text: "0", Type: TokenWord},
				{Text: "0", Type: TokenWord},
			},
			expectedCode: "1200",
			expectedCons: 4,
		},
		{
			name: "number token",
			tokens: []Token{
				{Text: "7700", Type: TokenNumber, Value: 7700},
			},
			expectedCode: "7700",
			expectedCons: 1,
		},
		{
			name:         "incomplete code",
			tokens:       []Token{{Text: "1", Type: TokenWord}, {Text: "2", Type: TokenWord}},
			expectedCode: "",
			expectedCons: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, consumed := extractSquawk(tt.tokens)
			if code != tt.expectedCode {
				t.Errorf("extractSquawk() code = %q, want %q", code, tt.expectedCode)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractSquawk() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestExtractDegrees(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		expectedDeg  int
		expectedDir  string
		expectedCons int
	}{
		{
			name: "degrees left",
			tokens: []Token{
				{Text: "20", Type: TokenNumber, Value: 20},
				{Text: "degrees", Type: TokenWord},
				{Text: "left", Type: TokenWord},
			},
			expectedDeg:  20,
			expectedDir:  "left",
			expectedCons: 3,
		},
		{
			name: "degrees right",
			tokens: []Token{
				{Text: "30", Type: TokenNumber, Value: 30},
				{Text: "right", Type: TokenWord},
			},
			expectedDeg:  30,
			expectedDir:  "right",
			expectedCons: 2,
		},
		{
			name: "direction before number without degrees keyword - no match",
			tokens: []Token{
				{Text: "left", Type: TokenWord},
				{Text: "15", Type: TokenNumber, Value: 15},
			},
			expectedDeg:  0,
			expectedDir:  "",
			expectedCons: 0,
		},
		{
			name: "direction before number with degrees keyword - matches",
			tokens: []Token{
				{Text: "left", Type: TokenWord},
				{Text: "15", Type: TokenNumber, Value: 15},
				{Text: "degrees", Type: TokenWord},
			},
			expectedDeg:  15,
			expectedDir:  "left",
			expectedCons: 3,
		},
		{
			name:         "missing direction",
			tokens:       []Token{{Text: "20", Type: TokenNumber, Value: 20}},
			expectedDeg:  0,
			expectedDir:  "",
			expectedCons: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deg, dir, consumed := extractDegrees(tt.tokens)
			if deg != tt.expectedDeg {
				t.Errorf("extractDegrees() deg = %d, want %d", deg, tt.expectedDeg)
			}
			if dir != tt.expectedDir {
				t.Errorf("extractDegrees() dir = %q, want %q", dir, tt.expectedDir)
			}
			if consumed != tt.expectedCons {
				t.Errorf("extractDegrees() consumed = %d, want %d", consumed, tt.expectedCons)
			}
		})
	}
}

func TestUntilEstablishedPattern(t *testing.T) {
	tests := []struct {
		name     string
		tokens   []Token
		ac       Aircraft
		expected []string
	}{
		{
			name: "altitude until established",
			tokens: []Token{
				{Text: "40", Type: TokenAltitude, Value: 40},
				{Text: "until", Type: TokenWord},
				{Text: "established", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"A40"},
		},
		{
			name: "altitude until established on localizer",
			tokens: []Token{
				{Text: "30", Type: TokenAltitude, Value: 30},
				{Text: "until", Type: TokenWord},
				{Text: "established", Type: TokenWord},
				{Text: "on", Type: TokenWord},
				{Text: "the", Type: TokenWord},
				{Text: "localizer", Type: TokenWord},
			},
			ac:       Aircraft{State: "arrival"},
			expected: []string{"A30"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands, _ := ParseCommands(tt.tokens, tt.ac)
			if len(commands) != len(tt.expected) {
				t.Errorf("ParseCommands() got %v, want %v", commands, tt.expected)
				return
			}
			for i := range commands {
				if commands[i] != tt.expected[i] {
					t.Errorf("ParseCommands()[%d] = %q, want %q", i, commands[i], tt.expected[i])
				}
			}
		})
	}
}

// =============================================================================
// Unit Tests for validate.go
// =============================================================================

func TestValidateCommands(t *testing.T) {
	tests := []struct {
		name         string
		commands     []string
		ac           Aircraft
		expectedLen  int
		minConf      float64
		expectErrors bool
	}{
		{
			name:         "valid descend for arrival",
			commands:     []string{"D80"},
			ac:           Aircraft{Altitude: 12000, State: "arrival"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "invalid descend - target above current",
			commands:     []string{"D150"},
			ac:           Aircraft{Altitude: 10000, State: "arrival"},
			expectedLen:  1, // becomes SAYAGAIN/ALTITUDE
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "valid climb for departure",
			commands:     []string{"C100"},
			ac:           Aircraft{Altitude: 5000, State: "departure"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "invalid climb - target below current",
			commands:     []string{"C50"},
			ac:           Aircraft{Altitude: 10000, State: "departure"},
			expectedLen:  1, // becomes SAYAGAIN/ALTITUDE
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "expedite climb invalid for arrival",
			commands:     []string{"EC"},
			ac:           Aircraft{Altitude: 10000, State: "arrival"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "expedite descent invalid for departure",
			commands:     []string{"ED"},
			ac:           Aircraft{Altitude: 10000, State: "departure"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "contact tower valid for arrival",
			commands:     []string{"TO"},
			ac:           Aircraft{State: "arrival"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "contact tower valid on approach",
			commands:     []string{"TO"},
			ac:           Aircraft{State: "on approach"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "CVS only valid for departures",
			commands:     []string{"CVS"},
			ac:           Aircraft{State: "arrival"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "CVS valid for departures",
			commands:     []string{"CVS"},
			ac:           Aircraft{State: "departure"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "expect approach invalid for departure",
			commands:     []string{"EILS22L"},
			ac:           Aircraft{State: "departure"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "altitude discretion only valid for VFR",
			commands:     []string{"A"},
			ac:           Aircraft{State: "arrival"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "altitude discretion valid for VFR",
			commands:     []string{"A"},
			ac:           Aircraft{State: "vfr flight following"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "mixed valid and invalid commands",
			commands:     []string{"L270", "D150"}, // heading valid, descend invalid (target > current)
			ac:           Aircraft{Altitude: 10000, State: "arrival"},
			expectedLen:  2, // heading survives, descend becomes SAYAGAIN/ALTITUDE
			minConf:      0.0,
			expectErrors: true,
		},
		{
			name:         "empty commands",
			commands:     []string{},
			ac:           Aircraft{State: "arrival"},
			expectedLen:  0,
			minConf:      0.0,
			expectErrors: false,
		},
		{
			name:         "speed with do not exceed suffix",
			commands:     []string{"S250-"},
			ac:           Aircraft{State: "arrival", AircraftType: "B738"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "speed with at or above suffix",
			commands:     []string{"S250+"},
			ac:           Aircraft{State: "arrival", AircraftType: "B738"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
		{
			name:         "speed until with suffix",
			commands:     []string{"S180-/U5DME"},
			ac:           Aircraft{State: "arrival", AircraftType: "B738"},
			expectedLen:  1,
			minConf:      0.9,
			expectErrors: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateCommands(tt.commands, tt.ac)

			if len(result.ValidCommands) != tt.expectedLen {
				t.Errorf("ValidateCommands() got %d commands, want %d (got %v)",
					len(result.ValidCommands), tt.expectedLen, result.ValidCommands)
			}
			if result.Confidence < tt.minConf {
				t.Errorf("ValidateCommands() confidence = %.2f, want >= %.2f",
					result.Confidence, tt.minConf)
			}
			if tt.expectErrors && len(result.Errors) == 0 {
				t.Error("ValidateCommands() expected errors but got none")
			}
			if !tt.expectErrors && len(result.Errors) > 0 {
				t.Errorf("ValidateCommands() unexpected errors: %v", result.Errors)
			}
		})
	}
}

func TestValidateCommandsForState(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
		state    string
		expected []string
	}{
		{
			name:     "departure rejects descend altitude",
			commands: []string{"D80", "L270", "C100"},
			state:    "departure",
			expected: []string{"L270", "C100"},
		},
		{
			name:     "departure rejects contact tower",
			commands: []string{"L270", "TO"},
			state:    "departure",
			expected: []string{"L270"},
		},
		{
			name:     "arrival rejects climb altitude",
			commands: []string{"C100", "L270", "DVS"},
			state:    "arrival",
			expected: []string{"L270", "DVS"},
		},
		{
			name:     "arrival rejects contact tower",
			commands: []string{"L270", "TO"},
			state:    "arrival",
			expected: []string{"L270"},
		},
		{
			name:     "overflight rejects contact tower",
			commands: []string{"D80", "C100", "TO"},
			state:    "overflight",
			expected: []string{"D80", "C100"},
		},
		{
			name:     "on approach allows all",
			commands: []string{"TO", "S250"},
			state:    "on approach",
			expected: []string{"TO", "S250"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateCommandsForState(tt.commands, tt.state)
			if len(result) != len(tt.expected) {
				t.Errorf("ValidateCommandsForState() = %v, want %v", result, tt.expected)
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("ValidateCommandsForState()[%d] = %q, want %q",
						i, result[i], tt.expected[i])
				}
			}
		})
	}
}

// =============================================================================
// Unit Tests for callsign.go
// =============================================================================

func TestMatchCallsignDirect(t *testing.T) {
	tests := []struct {
		name          string
		tokens        []Token
		aircraft      map[string]Aircraft
		expectedCS    string
		expectedConf  float64
		expectedCons  int
		expectNoMatch bool
	}{
		{
			name: "exact airline and number match",
			tokens: []Token{
				{Text: "american", Type: TokenWord},
				{Text: "5936", Type: TokenNumber, Value: 5936},
			},
			aircraft: map[string]Aircraft{
				"American 5936": {Callsign: "AAL5936"},
			},
			expectedCS:   "AAL5936",
			expectedConf: 0.9,
			expectedCons: 2,
		},
		{
			name: "digit tokens for flight number",
			tokens: []Token{
				{Text: "delta", Type: TokenWord},
				{Text: "4", Type: TokenWord},
				{Text: "4", Type: TokenWord},
				{Text: "2", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"Delta 442": {Callsign: "DAL442"},
			},
			expectedCS:   "DAL442",
			expectedConf: 0.9,
			expectedCons: 4,
		},
		{
			name: "multi-word airline",
			tokens: []Token{
				{Text: "china", Type: TokenWord},
				{Text: "southern", Type: TokenWord},
				{Text: "940", Type: TokenNumber, Value: 940},
			},
			aircraft: map[string]Aircraft{
				"China Southern 940": {Callsign: "CSN940"},
			},
			expectedCS:   "CSN940",
			expectedConf: 0.9,
			expectedCons: 3,
		},
		{
			name: "fuzzy airline match",
			tokens: []Token{
				{Text: "amurican", Type: TokenWord}, // fuzzy
				{Text: "100", Type: TokenNumber, Value: 100},
			},
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100"},
			},
			expectedCS:   "AAL100",
			expectedConf: 0.5,
			expectedCons: 2,
		},
		{
			name: "skip garbage at start",
			tokens: []Token{
				{Text: "lass", Type: TokenWord}, // garbage
				{Text: "delta", Type: TokenWord},
				{Text: "100", Type: TokenNumber, Value: 100},
			},
			aircraft: map[string]Aircraft{
				"Delta 100": {Callsign: "DAL100"},
			},
			expectedCS:   "DAL100",
			expectedConf: 0.5,
			expectedCons: 3,
		},
		{
			name: "no match returns empty",
			tokens: []Token{
				{Text: "xyz", Type: TokenWord},
				{Text: "999", Type: TokenNumber, Value: 999},
			},
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100"},
			},
			expectNoMatch: true,
		},
		{
			name:          "empty tokens",
			tokens:        []Token{},
			aircraft:      map[string]Aircraft{"American 100": {Callsign: "AAL100"}},
			expectNoMatch: true,
		},
		{
			name: "empty aircraft",
			tokens: []Token{
				{Text: "american", Type: TokenWord},
				{Text: "100", Type: TokenNumber, Value: 100},
			},
			aircraft:      map[string]Aircraft{},
			expectNoMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, remaining := MatchCallsign(tt.tokens, tt.aircraft)

			if tt.expectNoMatch {
				if match.Callsign != "" {
					t.Errorf("MatchCallsign() expected no match, got %q", match.Callsign)
				}
				return
			}

			if match.Callsign != tt.expectedCS {
				t.Errorf("MatchCallsign() callsign = %q, want %q", match.Callsign, tt.expectedCS)
			}
			if match.Confidence < tt.expectedConf {
				t.Errorf("MatchCallsign() confidence = %.2f, want >= %.2f",
					match.Confidence, tt.expectedConf)
			}
			if match.Consumed != tt.expectedCons {
				t.Errorf("MatchCallsign() consumed = %d, want %d", match.Consumed, tt.expectedCons)
			}
			expectedRemaining := len(tt.tokens) - tt.expectedCons
			if len(remaining) != expectedRemaining {
				t.Errorf("MatchCallsign() remaining = %d tokens, want %d",
					len(remaining), expectedRemaining)
			}
		})
	}
}

func TestMatchCallsignGACallsigns(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		aircraft     map[string]Aircraft
		expectedCS   string
		expectedCons int
	}{
		{
			name: "november N-number",
			tokens: []Token{
				{Text: "november", Type: TokenWord},
				{Text: "1", Type: TokenWord},
				{Text: "2", Type: TokenWord},
				{Text: "3", Type: TokenWord},
				{Text: "a", Type: TokenWord},
				{Text: "b", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"November 123AB": {Callsign: "N123AB"},
			},
			expectedCS:   "N123AB",
			expectedCons: 6,
		},
		{
			name: "N prefix",
			tokens: []Token{
				{Text: "n", Type: TokenWord},
				{Text: "3", Type: TokenWord},
				{Text: "4", Type: TokenWord},
				{Text: "5", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"November 345": {Callsign: "N345"},
			},
			expectedCS:   "N345",
			expectedCons: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, _ := MatchCallsign(tt.tokens, tt.aircraft)
			if match.Callsign != tt.expectedCS {
				t.Errorf("MatchCallsign() = %q, want %q", match.Callsign, tt.expectedCS)
			}
			if match.Consumed != tt.expectedCons {
				t.Errorf("MatchCallsign() consumed = %d, want %d", match.Consumed, tt.expectedCons)
			}
		})
	}
}

func TestMatchCallsignWithSuffix(t *testing.T) {
	tests := []struct {
		name         string
		tokens       []Token
		aircraft     map[string]Aircraft
		expectedCS   string
		expectedCons int
	}{
		{
			name: "heavy suffix consumed",
			tokens: []Token{
				{Text: "lufthansa", Type: TokenWord},
				{Text: "4wj", Type: TokenWord},
				{Text: "heavy", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"Lufthansa 4WJ heavy": {Callsign: "DLH4WJ"},
			},
			expectedCS:   "DLH4WJ",
			expectedCons: 3,
		},
		{
			name: "super suffix consumed",
			tokens: []Token{
				{Text: "emirates", Type: TokenWord},
				{Text: "100", Type: TokenNumber, Value: 100},
				{Text: "super", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"Emirates 100 super": {Callsign: "UAE100"},
			},
			expectedCS:   "UAE100",
			expectedCons: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, _ := MatchCallsign(tt.tokens, tt.aircraft)
			if match.Callsign != tt.expectedCS {
				t.Errorf("MatchCallsign() = %q, want %q", match.Callsign, tt.expectedCS)
			}
			if match.Consumed != tt.expectedCons {
				t.Errorf("MatchCallsign() consumed = %d, want %d", match.Consumed, tt.expectedCons)
			}
		})
	}
}

func TestFlightNumberOnlyFallback(t *testing.T) {
	tests := []struct {
		name        string
		tokens      []Token
		aircraft    map[string]Aircraft
		expectedCS  string
		expectMatch bool
	}{
		{
			name: "unique flight number matches",
			tokens: []Token{
				{Text: "garbled", Type: TokenWord},
				{Text: "5936", Type: TokenNumber, Value: 5936},
			},
			aircraft: map[string]Aircraft{
				"American 5936": {Callsign: "AAL5936"},
			},
			expectedCS:  "AAL5936",
			expectMatch: true,
		},
		{
			name: "completely garbled input with no flight number",
			tokens: []Token{
				{Text: "xyz", Type: TokenWord},
				{Text: "abc", Type: TokenWord},
			},
			aircraft: map[string]Aircraft{
				"American 100": {Callsign: "AAL100"},
			},
			expectMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, _ := MatchCallsign(tt.tokens, tt.aircraft)
			if tt.expectMatch {
				if match.Callsign != tt.expectedCS {
					t.Errorf("MatchCallsign() = %q, want %q", match.Callsign, tt.expectedCS)
				}
			} else {
				if match.Callsign != "" {
					t.Errorf("MatchCallsign() expected no match, got %q", match.Callsign)
				}
			}
		})
	}
}

// TestDecodeCommandsForCallsign tests decoding commands when callsign is already known.
// This is used when controller repeats a command without callsign after an AGAIN response.
func TestDecodeCommandsForCallsign(t *testing.T) {
	// Helper to create aircraft map with both telephony and ICAO callsign keys
	// (matching what BuildAircraftContext does)
	makeAircraftMap := func(telephony string, ac Aircraft) map[string]Aircraft {
		return map[string]Aircraft{
			telephony:   ac,
			ac.Callsign: ac,
		}
	}

	tests := []struct {
		name       string
		transcript string
		callsign   string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "simple altitude command",
			transcript: "descend and maintain 8000",
			callsign:   "AAL5936",
			aircraft:   makeAircraftMap("American 5936", Aircraft{Callsign: "AAL5936", Altitude: 12000, State: "arrival"}),
			expected:   "D80",
		},
		{
			name:       "heading command",
			transcript: "turn left heading two seven zero",
			callsign:   "UAL452",
			aircraft:   makeAircraftMap("United 452", Aircraft{Callsign: "UAL452", Altitude: 10000, State: "arrival"}),
			expected:   "L270",
		},
		{
			name:       "speed command",
			transcript: "reduce speed to two five zero",
			callsign:   "DAL88",
			aircraft:   makeAircraftMap("Delta 88", Aircraft{Callsign: "DAL88", Altitude: 10000, State: "arrival"}),
			expected:   "S250",
		},
		{
			name:       "multiple commands",
			transcript: "turn right heading one eight zero descend and maintain six thousand",
			callsign:   "SWA221",
			aircraft:   makeAircraftMap("Southwest 221", Aircraft{Callsign: "SWA221", Altitude: 10000, State: "arrival"}),
			expected:   "R180 D60",
		},
		{
			name:       "no valid commands returns AGAIN",
			transcript: "mumble garble nonsense",
			callsign:   "AAL5936",
			aircraft:   makeAircraftMap("American 5936", Aircraft{Callsign: "AAL5936", Altitude: 12000, State: "arrival"}),
			expected:   "AGAIN",
		},
		{
			name:       "empty transcript",
			transcript: "",
			callsign:   "AAL5936",
			aircraft:   makeAircraftMap("American 5936", Aircraft{Callsign: "AAL5936", Altitude: 12000, State: "arrival"}),
			expected:   "",
		},
		{
			name:       "callsign not in aircraft context",
			transcript: "descend and maintain 8000",
			callsign:   "UNKNOWN",
			aircraft:   makeAircraftMap("American 5936", Aircraft{Callsign: "AAL5936", Altitude: 12000, State: "arrival"}),
			expected:   "AGAIN",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeCommandsForCallsign(tt.aircraft, tt.transcript, tt.callsign)
			if err != nil {
				t.Errorf("DecodeCommandsForCallsign(%q, %q) error = %v",
					tt.transcript, tt.callsign, err)
				return
			}
			if result != tt.expected {
				t.Errorf("DecodeCommandsForCallsign(%q, %q) = %q, want %q",
					tt.transcript, tt.callsign, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// JSON File Tests from tests/ directory
// =============================================================================

// STTTestFile represents the JSON structure of test files in tests/ directory.
// These files are logged by SimManager.ReportSTTLog and contain the full context
// present when an STT command was processed.
type STTTestFile struct {
	Transcript  string `json:"transcript"`
	Callsign    string `json:"callsign"`
	Command     string `json:"command"` // Expected command output
	STTAircraft map[string]struct {
		Callsign            string            `json:"Callsign"`
		AircraftType        string            `json:"AircraftType"`
		Fixes               map[string]string `json:"Fixes"`
		CandidateApproaches map[string]string `json:"CandidateApproaches"`
		AssignedApproach    string            `json:"AssignedApproach"`
		SID                 string            `json:"SID"`
		STAR                string            `json:"STAR"`
		Altitude            int               `json:"Altitude"`
		State               string            `json:"State"`
		ControllerFrequency string            `json:"ControllerFrequency"`
		TrackingController  string            `json:"TrackingController"`
		AddressingForm      int               `json:"AddressingForm"`
		LAHSORunways        []string          `json:"LAHSORunways"`
	} `json:"stt_aircraft"`
}

// TestSTTFromJSONFiles runs all JSON test files from the tests/ directory.
// Each file contains a transcript, the full aircraft context, and the expected
// command output. This allows regression testing with real-world scenarios.
func TestSTTFromJSONFiles(t *testing.T) {
	testsDir := "tests"

	// Check if tests directory exists
	if _, err := os.Stat(testsDir); os.IsNotExist(err) {
		t.Skip("tests/ directory not found")
		return
	}

	// Find all JSON files
	files, err := filepath.Glob(filepath.Join(testsDir, "*.json"))
	if err != nil {
		t.Fatalf("failed to glob test files: %v", err)
	}

	if len(files) == 0 {
		t.Skip("no JSON test files found in tests/")
		return
	}

	provider := NewTranscriber(nil)

	for _, file := range files {
		testName := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(testName, func(t *testing.T) {
			// Read and parse the JSON file
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("failed to read %s: %v", file, err)
			}

			var testFile STTTestFile
			if err := json.Unmarshal(data, &testFile); err != nil {
				t.Fatalf("failed to parse %s: %v", file, err)
			}

			// Convert JSON aircraft to STT Aircraft map.
			// Bake /T into the callsign for type-based addressing entries,
			// mirroring the production context initialization in provider.go.
			aircraft := make(map[string]Aircraft)
			for key, ac := range testFile.STTAircraft {
				callsign := ac.Callsign
				form := sim.CallsignAddressingForm(ac.AddressingForm)
				if form == sim.AddressingFormTypeTrailing3 && !strings.HasSuffix(callsign, "/T") {
					callsign += "/T"
				}
				aircraft[key] = Aircraft{
					Callsign:            callsign,
					AircraftType:        ac.AircraftType,
					Fixes:               ac.Fixes,
					CandidateApproaches: ac.CandidateApproaches,
					AssignedApproach:    ac.AssignedApproach,
					SID:                 ac.SID,
					STAR:                ac.STAR,
					Altitude:            ac.Altitude,
					State:               ac.State,
					ControllerFrequency: ac.ControllerFrequency,
					TrackingController:  ac.TrackingController,
					AddressingForm:      form,
					LAHSORunways:        ac.LAHSORunways,
				}
			}

			// Run the transcript through STT
			result, err := provider.DecodeTranscript(aircraft, testFile.Transcript, "")
			if err != nil {
				t.Errorf("DecodeTranscript error: %v", err)
				return
			}

			// Build expected output: "CALLSIGN COMMANDS" or "" if both empty
			var expected string
			if testFile.Callsign == "" && testFile.Command == "" {
				expected = ""
			} else {
				expected = strings.TrimSpace(testFile.Callsign + " " + testFile.Command)
			}

			if !CommandsEquivalent(expected, result, aircraft) {
				t.Errorf("got %q, want %q", result, expected)
			}
		})
	}
}

func TestSpelledFixNames(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "direct fix with thats spelling",
			transcript: "American 123 proceed direct Deer Park thats delta papa kilo",
			aircraft: map[string]Aircraft{
				"American 123": {
					Callsign: "AAL123",
					State:    "arrival",
					Fixes: map[string]string{
						"deer park": "DPK",
						"merit":     "MERIT",
					},
				},
			},
			expected: "AAL123 DDPK",
		},
		{
			name:       "direct fix with spelling no trigger word",
			transcript: "Delta 456 direct Cameron charlie alpha mike romeo november",
			aircraft: map[string]Aircraft{
				"Delta 456": {
					Callsign: "DAL456",
					State:    "arrival",
					Fixes: map[string]string{
						"cameron": "CAMRN",
						"merit":   "MERIT",
					},
				},
			},
			expected: "DAL456 DCAMRN",
		},
		{
			name:       "spelling corrects garbled fix name",
			transcript: "United 789 direct Cameroon thats charlie alpha mike romeo november",
			aircraft: map[string]Aircraft{
				"United 789": {
					Callsign: "UAL789",
					State:    "arrival",
					Fixes: map[string]string{
						"cameron": "CAMRN",
					},
				},
			},
			expected: "UAL789 DCAMRN",
		},
		{
			name:       "spelling with spelled trigger word",
			transcript: "Southwest 100 proceed direct carmel spelled charlie alpha romeo mike lima",
			aircraft: map[string]Aircraft{
				"Southwest 100": {
					Callsign: "SWA100",
					State:    "arrival",
					Fixes: map[string]string{
						"carmel": "CARML",
					},
				},
			},
			expected: "SWA100 DCARML",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestApproachFixInjection tests that when an aircraft is told to expect an approach,
// the fixes from that approach become available for subsequent commands.
func TestApproachFixInjection(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "expect approach then direct to approach fix",
			transcript: "Delta 123 expect I L S runway two two left direct rosly",
			aircraft: map[string]Aircraft{
				"Delta 123": {
					Callsign: "DAL123",
					State:    "arrival",
					Fixes:    map[string]string{}, // ROSLY not in aircraft's route
					CandidateApproaches: map[string]string{
						"i l s runway two two left": "I22L",
					},
					ApproachFixes: map[string]map[string]string{
						"I22L": {
							"rosly": "ROSLY",
							"torby": "TORBY",
						},
					},
				},
			},
			expected: "DAL123 EI22L DROSLY",
		},
		{
			name:       "expect approach with LAHSO then direct to approach fix",
			transcript: "United 456 vectors ILS runway two eight center land hold short two two direct MERIT",
			aircraft: map[string]Aircraft{
				"United 456": {
					Callsign: "UAL456",
					State:    "arrival",
					Fixes:    map[string]string{}, // MERIT not in route
					CandidateApproaches: map[string]string{
						"i l s runway two eight center": "I28C",
					},
					ApproachFixes: map[string]map[string]string{
						"I28C": {
							"merit": "MERIT",
							"camrn": "CAMRN",
						},
					},
					LAHSORunways: []string{"22"},
				},
			},
			expected: "UAL456 EI28C/LAHSO22 DMERIT",
		},
		{
			name:       "approach fix does not override existing aircraft fix",
			transcript: "Southwest 789 expect RNAV runway three six direct merit",
			aircraft: map[string]Aircraft{
				"Southwest 789": {
					Callsign: "SWA789",
					State:    "arrival",
					Fixes: map[string]string{
						"merit": "MRIT1", // Aircraft already has MERIT in route (different code)
					},
					CandidateApproaches: map[string]string{
						"rnav runway three six": "R36",
					},
					ApproachFixes: map[string]map[string]string{
						"R36": {
							"merit": "MERIT", // Approach also has a fix spelled "merit"
						},
					},
				},
			},
			expected: "SWA789 ER36 DMRIT1", // Should use the original aircraft's fix, not the approach's
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestAssignedApproachPreference tests that when an aircraft has an assigned approach,
// approach clearances prefer matching that approach when the direction is ambiguous.
func TestAssignedApproachPreference(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "garbled direction prefers assigned approach",
			transcript: "JetBlue 1695 at Bunker cleared I L S runway one zero at approach",
			aircraft: map[string]Aircraft{
				"JetBlue 6095": {
					Callsign: "JBU6095",
					State:    "arrival",
					Fixes: map[string]string{
						"bunker": "BUNKR",
					},
					CandidateApproaches: map[string]string{
						"i l s runway one zero left":  "I0L",
						"i l s runway one zero right": "I0R",
					},
					AssignedApproach: "ILS Runway 10R", // Expecting 10 Right
				},
			},
			expected: "JBU6095 ABUNKR/CI0R", // Should match 10R, not 10L
		},
		{
			name:       "explicit direction overrides assigned approach",
			transcript: "Delta 456 cleared ILS runway two eight left approach",
			aircraft: map[string]Aircraft{
				"Delta 456": {
					Callsign: "DAL456",
					State:    "arrival",
					CandidateApproaches: map[string]string{
						"i l s runway two eight left":   "I28L",
						"i l s runway two eight center": "I28C",
						"i l s runway two eight right":  "I28R",
					},
					AssignedApproach: "ILS Runway 28C", // Expected center, but pilot said left
				},
			},
			expected: "DAL456 CI28L", // Should match what was actually said (left)
		},
		{
			name:       "no assigned approach uses alphabetical tiebreaker",
			transcript: "United 789 cleared ILS runway three six approach",
			aircraft: map[string]Aircraft{
				"United 789": {
					Callsign: "UAL789",
					State:    "arrival",
					CandidateApproaches: map[string]string{
						"i l s runway three six left":  "I36L",
						"i l s runway three six right": "I36R",
					},
					AssignedApproach: "", // No assigned approach
				},
			},
			expected: "UAL789 CI36L", // Alphabetically earlier (L < R)
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDetectNegativeThatWasFor(t *testing.T) {
	tests := []struct {
		name           string
		tokens         []Token
		expectedFound  bool
		expectedOffset int // number of tokens consumed (to get remaining)
	}{
		{
			name: "negative that was for",
			tokens: []Token{
				{Text: "negative", Type: TokenWord},
				{Text: "that", Type: TokenWord},
				{Text: "was", Type: TokenWord},
				{Text: "for", Type: TokenWord},
				{Text: "delta", Type: TokenWord},
				{Text: "456", Type: TokenNumber, Value: 456},
			},
			expectedFound:  true,
			expectedOffset: 4, // after "negative that was for"
		},
		{
			name: "no that was for",
			tokens: []Token{
				{Text: "no", Type: TokenWord},
				{Text: "that", Type: TokenWord},
				{Text: "was", Type: TokenWord},
				{Text: "for", Type: TokenWord},
				{Text: "united", Type: TokenWord},
				{Text: "123", Type: TokenNumber, Value: 123},
			},
			expectedFound:  true,
			expectedOffset: 4, // after "no that was for"
		},
		{
			name: "negative was for (without that)",
			tokens: []Token{
				{Text: "negative", Type: TokenWord},
				{Text: "was", Type: TokenWord},
				{Text: "for", Type: TokenWord},
				{Text: "american", Type: TokenWord},
				{Text: "789", Type: TokenNumber, Value: 789},
			},
			expectedFound:  true,
			expectedOffset: 3, // after "negative was for"
		},
		{
			name: "just negative without correction pattern",
			tokens: []Token{
				{Text: "negative", Type: TokenWord},
				{Text: "delta", Type: TokenWord},
				{Text: "456", Type: TokenNumber, Value: 456},
			},
			expectedFound: false,
		},
		{
			name: "regular command (no correction)",
			tokens: []Token{
				{Text: "turn", Type: TokenWord},
				{Text: "left", Type: TokenWord},
				{Text: "heading", Type: TokenWord},
				{Text: "270", Type: TokenNumber, Value: 270},
			},
			expectedFound: false,
		},
		{
			name:          "empty tokens",
			tokens:        []Token{},
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining, found := detectNegativeThatWasFor(tt.tokens)
			if found != tt.expectedFound {
				t.Errorf("found = %v, want %v", found, tt.expectedFound)
			}
			if tt.expectedFound {
				expectedRemaining := len(tt.tokens) - tt.expectedOffset
				if len(remaining) != expectedRemaining {
					t.Errorf("remaining tokens = %d, want %d", len(remaining), expectedRemaining)
				}
			}
		})
	}
}

func TestNegativeThatWasForFullParse(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		aircraft   map[string]Aircraft
		expected   string
	}{
		{
			name:       "negative that was for with new command",
			transcript: "Negative that was for United 123. United 123, turn left heading 270",
			aircraft: map[string]Aircraft{
				"Delta 456":  {Callsign: "DAL456", State: "arrival"},
				"United 123": {Callsign: "UAL123", State: "arrival"},
			},
			expected: "ROLLBACK UAL123 L270",
		},
		{
			name:       "no that was for with altitude",
			transcript: "No that was for Southwest 221. Southwest 221, descend and maintain 8000",
			aircraft: map[string]Aircraft{
				"American 789":  {Callsign: "AAL789", Altitude: 12000, State: "arrival"},
				"Southwest 221": {Callsign: "SWA221", Altitude: 12000, State: "arrival"},
			},
			expected: "ROLLBACK SWA221 D80",
		},
		{
			name:       "negative was for (shorter form)",
			transcript: "Negative was for Delta 88. Delta 88, climb and maintain niner thousand",
			aircraft: map[string]Aircraft{
				"JetBlue 100": {Callsign: "JBU100", Altitude: 5000, State: "departure"},
				"Delta 88":    {Callsign: "DAL88", Altitude: 3000, State: "departure"},
			},
			expected: "ROLLBACK DAL88 C90",
		},
		{
			name:       "negative that was for with multiple commands",
			transcript: "Negative that was for Frontier 900. Frontier 900, turn right heading 180, descend and maintain 6000",
			aircraft: map[string]Aircraft{
				"United 452":   {Callsign: "UAL452", Altitude: 10000, State: "arrival"},
				"Frontier 900": {Callsign: "FFT900", Altitude: 10000, State: "arrival"},
			},
			expected: "ROLLBACK FFT900 R180 D60",
		},
	}

	provider := NewTranscriber(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.DecodeTranscript(tt.aircraft, tt.transcript, "")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}
