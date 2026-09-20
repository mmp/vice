package sim_test

import (
	"io"
	"log/slog"
	"reflect"
	"slices"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech/stt"
)

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
		ac.Nav.Perf = av.DB.AircraftPerformance["A320"]
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
