package sim_test

import (
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/wx"

	"log/slog"
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

	// Subtests below clobber db.DB.Airports["KJFK"] with synthetic runway
	// fixtures (threshold at the origin) so the test aircraft's (0,0)
	// position lines up with the CVA/EVA geometry checks. Save and restore
	// the real entry so other tests in the binary aren't poisoned.
	origKJFK := db.DB.Airports["KJFK"]
	t.Cleanup(func() { db.DB.Airports["KJFK"] = origKJFK })

	tests := []e2eCase{
		{
			name:       "expect visual approach → generic visual, not charted",
			transcript: "Delta forty three expect visual approach runway two two left",
			sttAircraft: map[string]stt.Aircraft{
				"Delta 43": {
					Callsign:     "DAL43",
					AircraftType: "A321",
					CandidateApproaches: map[string]string{
						"ILS Runway 22L":    "I22L",
						"ILS Runway 22R":    "I22R",
						"Visual Runway 22L": "V22L",
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
						"ILS Runway 31R":    "I31R",
						"Visual Runway 31R": "V31R",
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
						"ILS Runway 26":    "I26",
						"Visual Runway 26": "V26",
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
						"ILS Runway 12R":    "I12R",
						"Visual Runway 12R": "VR1",
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
						"ILS Runway 22L":    "I22L",
						"Visual Runway 22L": "V22L",
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
						"ILS Runway 22L":                 "I22L",
						"Mount Vernon Visual Runway 22L": "MTV",
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

			// CVA/EVA runway validation and visual-path setup use db.DB runway data.
			runways := []av.Runway{
				{Id: runway, Heading: 180, Threshold: [2]float32{0, 0}, Elevation: 13},
			}
			if lahsoRunway != "" {
				runways = append(runways, av.Runway{Id: lahsoRunway, Heading: 260, Threshold: [2]float32{0, 0}, Elevation: 13})
			}
			db.DB.Airports["KJFK"] = db.Airport{
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
	before, after, ok := strings.Cut(sttResult, " ")
	if !ok {
		return sttResult, ""
	}
	return before, after
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

// correctionRunner decodes transcripts and dispatches them the way the client does,
// rebuilding the aircraft context between transmissions so that the decoder sees who
// the controller last addressed.
func correctionRunner(t *testing.T, s *sim.Sim, aircraft map[string]stt.Aircraft) func(transcript, want string) {
	t.Helper()
	transcriber := stt.NewTranscriber(&log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return func(transcript, want string) {
		t.Helper()
		last := sim.LastAddressedCallsign(s, sim.E2ETCW())
		for key, ac := range aircraft {
			ac.LastAddressed = av.ADSBCallsign(ac.Callsign) == last
			aircraft[key] = ac
		}

		decoded, err := transcriber.DecodeTranscript(aircraft, transcript, "")
		if err != nil {
			t.Fatalf("DecodeTranscript(%q): %v", transcript, err)
		}
		if decoded != want {
			t.Fatalf("%q decoded to %q, want %q", transcript, decoded, want)
		}
		cs, commands := splitCallsignAndCommands(decoded)
		if res := s.RunAircraftControlCommands(sim.E2ETCW(), av.ADSBCallsign(cs), commands, 0); res.Error != nil {
			t.Fatalf("dispatch %q: %v (remaining %q)", decoded, res.Error, res.RemainingInput)
		}
	}
}

func assignedHeading(t *testing.T, ac *sim.Aircraft, want int) {
	t.Helper()
	if h, ok := ac.Nav.AssignedHeading(); !ok || int(h) != want {
		t.Errorf("%s: assigned heading = %v (ok=%v), want %d", ac.ADSBCallsign, h, ok, want)
	}
}

func TestCorrectionEndToEnd(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := sim.NewTestSim(lg)
	for _, cs := range []av.ADSBCallsign{"UAL1482", "DAL456", "N123AB"} {
		ac := sim.MakeTestAircraft(cs, "22L")
		ac.Nav.Perf = db.DB.AircraftPerformance["A320"]
		s.Aircraft[cs] = ac
	}
	run := correctionRunner(t, s, map[string]stt.Aircraft{
		"United 1482": {Callsign: "UAL1482", AircraftType: "A320", State: "arrival", Altitude: 6000},
		"Delta 456":   {Callsign: "DAL456", AircraftType: "A320", State: "arrival", Altitude: 6000},
		"november 1 2 3 alpha brahvo": {Callsign: "N123AB", AircraftType: "C172",
			State: "vfr flight following", Altitude: 4500},
	})

	run("United fourteen eighty two turn left heading zero one zero", "UAL1482 L010")
	assignedHeading(t, s.Aircraft["UAL1482"], 10)

	// The same aircraft: the correction amends what it was told.
	run("United fourteen eighty two correction turn left heading zero three zero", "UAL1482 CORRECTION L030")
	assignedHeading(t, s.Aircraft["UAL1482"], 30)

	// A different one: the previous transmission was for nobody, so United is put
	// back the way the transmission before that left it.
	run("correction Delta four five six turn left heading zero five zero", "DAL456 CORRECTION L050")
	assignedHeading(t, s.Aircraft["UAL1482"], 10)
	assignedHeading(t, s.Aircraft["DAL456"], 50)

	// No callsign spoken: the aircraft the controller last addressed.
	run("correction heading zero six zero", "DAL456 CORRECTION H060")
	assignedHeading(t, s.Aircraft["DAL456"], 60)

	// An unintelligible correction retracts nothing.
	run("Delta four five six correction blorf", "DAL456 AGAIN")
	assignedHeading(t, s.Aircraft["DAL456"], 60)

	// Nor does one that leaves the pilot asking for the garbled part again, even
	// though it re-addresses a different aircraft.
	run("correction United fourteen eighty two descend and maintain blorf", "UAL1482 SAYAGAIN/ALTITUDE")
	assignedHeading(t, s.Aircraft["DAL456"], 60)

	// An implicit go-ahead is an instruction like any other, so correcting to one
	// still puts the aircraft that wrongly took the previous transmission back.
	run("United fourteen eighty two turn right heading two seven zero", "UAL1482 R270")
	assignedHeading(t, s.Aircraft["UAL1482"], 270)
	run("correction November one two three alpha bravo New York approach", "N123AB CORRECTION GA")
	assignedHeading(t, s.Aircraft["UAL1482"], 10)

	// Saying which aircraft it was meant for retracts the previous transmission
	// outright, so it comes back even though the replacement was garbled.
	run("United fourteen eighty two turn right heading zero two zero", "UAL1482 R020")
	assignedHeading(t, s.Aircraft["UAL1482"], 20)
	run("negative that was for Delta four five six descend and maintain blorf",
		"DAL456 CORRECTION SAYAGAIN/ALTITUDE")
	assignedHeading(t, s.Aircraft["UAL1482"], 10)
}

// TestATISCorrectionPreservesApproach covers a correction to one of two instructions
// given in a single transmission: the instruction the pilot got right has to survive.
func TestATISCorrectionPreservesApproach(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := sim.NewTestSim(lg)
	ac := sim.MakeTestAircraft("SWA2949", "30")
	s.Aircraft[ac.ADSBCallsign] = ac
	ac.FlightPlan.ArrivalAirport = "KOAK"
	ac.Nav.FlightState.ArrivalAirport = av.Waypoint{Fix: "KOAK"}
	ac.Nav.Approach = nav.Approach{}
	ac.Nav.Waypoints = av.WaypointArray{{Fix: "BOYYS"}, {Fix: "HOPTA"}, {Fix: "KOAK"}}
	ac.STARRunwayWaypoints = map[string]av.WaypointArray{
		"30": {{Fix: "HOPTA"}, {Fix: "ALLXX"}, {Fix: "CRSEN"}},
	}
	s.State.Airports["KOAK"] = &av.Airport{Approaches: map[string]*av.Approach{
		"I30": {Type: av.ILSApproach, Runway: "30", FullName: "ILS Runway 30"},
	}}
	run := correctionRunner(t, s, map[string]stt.Aircraft{
		"Southwest 2949": {Callsign: "SWA2949", State: "arrival", Altitude: 7400,
			CandidateApproaches: map[string]string{"ILS Runway 30": "I30"}},
	})

	run("Southwest twenty nine forty nine information victor is current expect ILS runway three zero approach",
		"SWA2949 ATIS/V EI30")
	wps := slices.Clone(ac.Nav.Waypoints)
	if !slices.ContainsFunc(wps, func(w av.Waypoint) bool { return w.Fix == "ALLXX" }) {
		t.Fatal("initial approach assignment did not add runway waypoints")
	}

	run("Southwest twenty nine forty nine correction ATIS information whiskey is current",
		"SWA2949 CORRECTION ATIS/W")
	if ac.ReportedATIS != "W" || ac.Nav.Approach.AssignedId != "I30" || ac.Nav.Approach.Assigned == nil {
		t.Fatalf("ATIS = %q, approach = %+v", ac.ReportedATIS, ac.Nav.Approach)
	}
	if !reflect.DeepEqual(ac.Nav.Waypoints, wps) {
		t.Fatalf("ATIS correction changed route: got %v, want %v", ac.Nav.Waypoints, wps)
	}
}
