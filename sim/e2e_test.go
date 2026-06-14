package sim_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
	"github.com/mmp/vice/wx"
)

type e2eCase struct {
	name          string
	transcript    string                  // what the controller said
	sttAircraft   map[string]stt.Aircraft // STT context (callsign matching, approaches)
	simSetup      func(s *sim.Sim)        // optional Sim tweaks (e.g., set FieldInSight)
	wantCommand   string                  // expected "CALLSIGN CMD" from STT
	wantError     bool                    // should command dispatch fail?
	wantReadback  string                  // substring expected in readback (or "")
	notInReadback string                  // substring that must NOT appear
}

func TestE2E_STTToSim(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	transcriber := stt.NewTranscriber(lg)

	// Subtests below clobber av.DB.Airports["KJFK"] with synthetic runway
	// fixtures (threshold at the origin) so the test aircraft's (0,0)
	// position lines up with the CVA/EVA geometry checks. Save and restore
	// the real entry so other tests in the binary aren't poisoned.
	origKJFK := av.DB.Airports["KJFK"]
	t.Cleanup(func() { av.DB.Airports["KJFK"] = origKJFK })

	tests := []e2eCase{
		{
			name:       "expect visual approach → generic visual, not charted",
			transcript: "Delta forty three expect visual approach runway two two left",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					CandidateApproaches: map[string]string{
						"I L S runway two two left":  "I22L",
						"I L S runway two two right": "I22R",
						"Visual runway two two left": "V22L",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway two two left":          "22L",
						"visual approach runway two two left": "22L",
						"visual two two left":                 "22L",
					},
					AssignedApproach: "ILS Runway 22L",
					State:            "arrival",
					Altitude:         6000,
				},
			},
			wantCommand:   "DAL43 EVA22L",
			wantReadback:  "visual",
			notInReadback: "Belmont",
		},
		{
			name:       "vectors visual approach → EVA command",
			transcript: "American twelve thirty two vectors visual approach runway three one right",
			sttAircraft: map[string]stt.Aircraft{
				"American 1232": {
					Callsign:     "AAL1232",
					AircraftType: "A321",
					CandidateApproaches: map[string]string{
						"I L S runway three one right":  "I31R",
						"Visual runway three one right": "V31R",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway three one right":          "31R",
						"visual approach runway three one right": "31R",
						"visual three one right":                 "31R",
					},
					AssignedApproach: "ILS Runway 31R",
					State:            "arrival",
					Altitude:         5000,
				},
			},
			wantCommand: "AAL1232 EVA31R",
		},
		{
			name:       "kennedy at your 11 o'clock 8 miles → AP command",
			transcript: "Delta forty three kennedy at your eleven o'clock eight miles",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					Fixes:        map[string]string{"Kennedy": "KJFK"},
					State:        "arrival",
					Altitude:     5000,
				},
			},
			wantCommand: "DAL43 AP/11/8",
		},
		{
			name:       "cleared visual approach → CVA command",
			transcript: "Southwest two forty seven cleared visual runway two six",
			sttAircraft: map[string]stt.Aircraft{
				"Southwest two 47": {
					Callsign:     "SWA247",
					AircraftType: "B738",
					CandidateApproaches: map[string]string{
						"I L S runway two six":  "I26",
						"Visual runway two six": "V26",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway two six":          "26",
						"visual approach runway two six": "26",
						"visual two six":                 "26",
					},
					AssignedApproach: "ILS Runway 26",
					State:            "arrival",
					Altitude:         4000,
				},
			},
			wantCommand: "SWA247 CVA26",
			simSetup: func(s *sim.Sim) {
				// CVA requires field in sight or visual request
				for _, ac := range s.Aircraft {
					ac.FieldInSight = true
				}
			},
		},
		{
			name:       "expect a visual approach — filler word 'a'",
			transcript: "Sun Country five zero five expect a visual approach runway one two right",
			sttAircraft: map[string]stt.Aircraft{
				"Sun Country five zero five": {
					Callsign:     "SCX505",
					AircraftType: "B738",
					CandidateApproaches: map[string]string{
						"I L S runway one two right":  "I12R",
						"Visual runway one two right": "VR1",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway one two right":          "12R",
						"visual approach runway one two right": "12R",
						"visual one two right":                 "12R",
					},
					AssignedApproach: "ILS Runway 12R",
					State:            "arrival",
					Altitude:         4000,
				},
			},
			wantCommand: "SCX505 EVA12R",
		},
		{
			name:       "expect visual approach with LAHSO",
			transcript: "Delta forty three expect visual approach runway two two left land hold short two six",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					CandidateVisualApproaches: map[string]string{
						"visual runway two two left":          "22L",
						"visual approach runway two two left": "22L",
						"visual two two left":                 "22L",
					},
					LAHSORunways: []string{"26"},
					State:        "arrival",
					Altitude:     6000,
				},
			},
			wantCommand:  "DAL43 EVA22L/LAHSO26",
			wantReadback: "hold short",
		},
		{
			name:       "EVA readback doesn't say 'approach approach'",
			transcript: "Delta forty three expect visual approach runway two two left",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					CandidateApproaches: map[string]string{
						"I L S runway two two left":  "I22L",
						"Visual runway two two left": "V22L",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway two two left":          "22L",
						"visual approach runway two two left": "22L",
						"visual two two left":                 "22L",
					},
					AssignedApproach: "ILS Runway 22L",
					State:            "arrival",
					Altitude:         6000,
				},
			},
			wantCommand:   "DAL43 EVA22L",
			notInReadback: "approach approach",
		},
		{
			// Naming a charted visual ("Mount Vernon visual …") must emit
			// the charted-visual code (E{code}) — not the generic EVA{rwy}
			// command that the priority-17 visual pattern would produce
			// if its parser's slack let it skip past "Mount Vernon".
			name:       "expect named charted visual → charted approach code",
			transcript: "Delta forty three expect Mount Vernon visual runway two two left",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					CandidateApproaches: map[string]string{
						"I L S runway two two left":               "I22L",
						"Mount Vernon Visual runway two two left": "MTV",
					},
					CandidateVisualApproaches: map[string]string{
						"visual runway two two left":          "22L",
						"visual approach runway two two left": "22L",
						"visual two two left":                 "22L",
					},
					State:    "arrival",
					Altitude: 6000,
				},
			},
			wantCommand: "DAL43 EMTV",
			simSetup: func(s *sim.Sim) {
				ap := s.State.Airports["KJFK"]
				ap.Approaches["MTV"] = &av.Approach{
					Type:     av.ChartedVisualApproach,
					Runway:   "22L",
					FullName: "Mount Vernon Visual Runway 22L",
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: STT decode
			result, err := transcriber.DecodeTranscript(tt.sttAircraft, tt.transcript, "")
			if err != nil {
				t.Fatalf("DecodeTranscript error: %v", err)
			}
			if result != tt.wantCommand {
				t.Fatalf("STT command = %q, want %q", result, tt.wantCommand)
			}

			// Step 2: Parse callsign + commands from STT result
			callsign, commands := splitCallsignAndCommands(result)
			if callsign == "" || commands == "" {
				t.Fatalf("failed to parse callsign/commands from %q", result)
			}

			// Step 3: Set up sim and aircraft
			s := sim.NewTestSim(lg)
			runway := guessRunway(commands)
			lahsoRunway := guessLAHSORunway(commands)

			// CVA/EVA runway validation and visual-path setup use av.DB runway data.
			runways := []av.Runway{
				{Id: runway, Heading: 180, Threshold: [2]float32{0, 0}, Elevation: 13},
			}
			if lahsoRunway != "" {
				runways = append(runways, av.Runway{Id: lahsoRunway, Heading: 260, Threshold: [2]float32{0, 0}, Elevation: 13})
			}
			av.DB.Airports["KJFK"] = av.FAAAirport{
				Id:        "KJFK",
				Elevation: 13,
				Runways:   runways,
			}

			ac := sim.MakeTestAircraft(av.ADSBCallsign(callsign), runway)
			s.Aircraft[av.ADSBCallsign(callsign)] = ac

			// Add airport with matching approaches
			s.State.METAR["KJFK"] = wx.METAR{Raw: "KJFK 10SM BKN050"}
			s.State.Airports["KJFK"] = &av.Airport{
				Location: [2]float32{0, 0},
				Approaches: map[string]*av.Approach{
					"I" + runway: {Type: av.ILSApproach, Runway: runway},
					"V" + runway: {Type: av.ChartedVisualApproach, Runway: runway},
				},
			}

			if tt.simSetup != nil {
				tt.simSetup(s)
			}

			// Step 4: Execute command
			res := s.RunAircraftControlCommands(sim.E2ETCW(), av.ADSBCallsign(callsign), commands, 0)

			if tt.wantError && res.Error == nil {
				t.Error("expected error from command dispatch, got nil")
			}
			if !tt.wantError && res.Error != nil {
				t.Errorf("unexpected dispatch error: %v (remaining: %s)", res.Error, res.RemainingInput)
			}

			// Step 5: Check readback
			readback := strings.ToLower(res.ReadbackSpokenText)
			if tt.wantReadback != "" {
				want := strings.ToLower(tt.wantReadback)
				if !strings.Contains(readback, want) {
					t.Errorf("readback %q does not contain %q", res.ReadbackSpokenText, tt.wantReadback)
				}
			}
			if tt.notInReadback != "" {
				bad := strings.ToLower(tt.notInReadback)
				if strings.Contains(readback, bad) {
					t.Errorf("readback %q should not contain %q", res.ReadbackSpokenText, tt.notInReadback)
				}
			}
		})
	}
}

// splitCallsignAndCommands splits "DAL43 EVA22L" into ("DAL43", "EVA22L").
func splitCallsignAndCommands(sttResult string) (string, string) {
	idx := strings.IndexByte(sttResult, ' ')
	if idx < 0 {
		return sttResult, ""
	}
	return sttResult[:idx], sttResult[idx+1:]
}

// guessRunway extracts a runway identifier from a command string for test setup.
// E.g., "EVA22L" → "22L", "CVA26" → "26", "AP/11/8" → "22L" (fallback).
func guessRunway(commands string) string {
	cmd := strings.Fields(commands)[0]
	for _, prefix := range []string{"EVA", "CVA"} {
		if strings.HasPrefix(cmd, prefix) && len(cmd) > len(prefix) {
			runway, _, _ := strings.Cut(cmd[len(prefix):], "/")
			return runway
		}
	}
	return "22L" // fallback for non-approach commands
}

func guessLAHSORunway(commands string) string {
	cmd := strings.Fields(commands)[0]
	_, suffix, ok := strings.Cut(cmd, "/LAHSO")
	if !ok {
		return ""
	}
	return suffix
}
