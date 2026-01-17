package stt

import (
	"testing"
)

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
			name:       "cleared approach with speed until distance (5 mile final is not altitude)",
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
			expected: "THY10Z CI2L S180",
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
			name:       "tree for too in callsign",
			transcript: "Delta for fower too turn left heading too seven zero",
			aircraft: map[string]Aircraft{
				"Delta 442": {Callsign: "DAL442", State: "arrival"},
			},
			expected: "DAL442 L270",
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
			expected: "BLOCKED",
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
			name:       "no position ID without radio name",
			transcript: "Encore 208 New York departure",
			aircraft: map[string]Aircraft{
				"Encore 208": {Callsign: "WEN208", State: "departure"},
			},
			radioName:        "", // No radio name - should not detect position ID
			expected:         "WEN208 AGAIN",
			expectPositionID: false,
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
