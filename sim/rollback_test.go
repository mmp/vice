package sim_test

import (
	"io"
	"log/slog"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
)

// TestRollbackUndoesAndReissues covers the "correction" flow: STT sends
// "ROLLBACK {callsign} {commands}", which must undo the previous
// transmission and then run the new commands.
func TestRollbackUndoesAndReissues(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := sim.NewTestSim(lg)

	callsign := av.ADSBCallsign("TEST123")
	s.Aircraft[callsign] = sim.MakeTestAircraft(callsign, "22L")
	nav := &s.Aircraft[callsign].Nav
	nav.Perf = av.DB.AircraftPerformance["A320"] // so the altitude assignment isn't refused

	if res := s.RunAircraftControlCommands(sim.E2ETCW(), callsign, "L010 D20", 0); res.Error != nil {
		t.Fatalf("initial commands: %v (remaining %q)", res.Error, res.RemainingInput)
	}
	if hdg, ok := nav.AssignedHeading(); !ok || hdg != 10 {
		t.Fatalf("after L010: assigned heading = %v (ok=%v), want 10", hdg, ok)
	}
	if nav.Altitude.Assigned == nil {
		t.Fatal("after D20: no assigned altitude")
	}

	res := s.RunAircraftControlCommands(sim.E2ETCW(), "ROLLBACK", string(callsign)+" L030", 0)
	if res.Error != nil {
		t.Fatalf("rollback commands: %v (remaining %q)", res.Error, res.RemainingInput)
	}
	if res.ReadbackSpokenText == "" {
		t.Error("rollback transmission produced no readback")
	}
	if hdg, ok := nav.AssignedHeading(); !ok || hdg != 30 {
		t.Errorf("after ROLLBACK L030: assigned heading = %v (ok=%v), want 30", hdg, ok)
	}
	if nav.Altitude.Assigned != nil {
		t.Errorf("after ROLLBACK: assigned altitude = %v, want it undone", *nav.Altitude.Assigned)
	}
}

// TestRollbackEndToEnd checks that what the decoder emits for a leading
// "correction" is what the sim's dispatch expects: ROLLBACK leads the
// response, ahead of the callsign, because the client splits at the first
// space.
func TestRollbackEndToEnd(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	transcriber := stt.NewTranscriber(lg)

	sttAircraft := map[string]stt.Aircraft{
		"United 1482": {
			Callsign:     "UAL1482",
			AircraftType: "A320",
			State:        "arrival",
			Altitude:     6000,
		},
	}

	callsign := av.ADSBCallsign("UAL1482")
	s := sim.NewTestSim(lg)
	s.Aircraft[callsign] = sim.MakeTestAircraft(callsign, "22L")
	nav := &s.Aircraft[callsign].Nav

	run := func(transcript, want string) {
		t.Helper()
		result, err := transcriber.DecodeTranscript(sttAircraft, transcript, "")
		if err != nil {
			t.Fatalf("DecodeTranscript(%q): %v", transcript, err)
		}
		if result != want {
			t.Fatalf("STT command = %q, want %q", result, want)
		}
		cs, commands := splitCallsignAndCommands(result)
		if res := s.RunAircraftControlCommands(sim.E2ETCW(), av.ADSBCallsign(cs), commands, 0); res.Error != nil {
			t.Fatalf("dispatch %q: %v (remaining %q)", result, res.Error, res.RemainingInput)
		}
	}

	run("United fourteen eighty two turn left heading zero one zero", "UAL1482 L010")
	if hdg, ok := nav.AssignedHeading(); !ok || hdg != 10 {
		t.Fatalf("assigned heading = %v (ok=%v), want 10", hdg, ok)
	}

	run("United fourteen eighty two correction turn left heading zero three zero", "ROLLBACK UAL1482 L030")
	if hdg, ok := nav.AssignedHeading(); !ok || hdg != 30 {
		t.Errorf("after correction: assigned heading = %v (ok=%v), want 30", hdg, ok)
	}
}

// TestRollbackHistoryIsPerTCW checks that each TCW rolls back only what it
// commanded, and that history for an aircraft is discarded once it leaves the
// controller's frequency.
func TestRollbackHistoryIsPerTCW(t *testing.T) {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := sim.NewTestSim(lg)
	s.State.CurrentConsolidation["OTHER"] = &sim.TCPConsolidation{PrimaryTCP: "126.0"}

	for _, cs := range []av.ADSBCallsign{"AAL111", "AAL222"} {
		s.Aircraft[cs] = sim.MakeTestAircraft(cs, "22L")
	}
	s.Aircraft["AAL222"].ControllerFrequency = "126.0"

	run := func(tcw sim.TCW, callsign av.ADSBCallsign, commands string) {
		t.Helper()
		if res := s.RunAircraftControlCommands(tcw, callsign, commands, 0); res.Error != nil {
			t.Fatalf("%s %s %s: %v", tcw, callsign, commands, res.Error)
		}
	}
	heading := func(cs av.ADSBCallsign) float32 {
		t.Helper()
		hdg, ok := s.Aircraft[cs].Nav.AssignedHeading()
		if !ok {
			t.Fatalf("%s: no assigned heading", cs)
		}
		return float32(hdg)
	}

	run(sim.E2ETCW(), "AAL111", "L010")
	run("OTHER", "AAL222", "L020")
	run(sim.E2ETCW(), "AAL111", "L030")

	// Rolling back at OTHER must undo its own transmission, not the newer one
	// at the other TCW.
	run("OTHER", "ROLLBACK", "")
	if hdg := heading("AAL111"); hdg != 30 {
		t.Errorf("other TCW's rollback changed AAL111: heading = %v, want 30", hdg)
	}
	if _, ok := s.Aircraft["AAL222"].Nav.AssignedHeading(); ok {
		t.Error("rollback did not undo the commanding TCW's own transmission")
	}

	// Once an aircraft is off the frequency there is nothing left to roll back.
	if _, err := s.RadarServicesTerminated(sim.E2ETCW(), "AAL111"); err != nil {
		t.Fatal(err)
	}
	run(sim.E2ETCW(), "ROLLBACK", "")
	if hdg := heading("AAL111"); hdg != 30 {
		t.Errorf("rollback after leaving the frequency: heading = %v, want 30", hdg)
	}

	// Signing off the TCW discards its history too.
	run("OTHER", "AAL222", "L040")
	s.ClearSTTCommands("OTHER")
	run("OTHER", "ROLLBACK", "")
	if hdg := heading("AAL222"); hdg != 40 {
		t.Errorf("rollback after sign-off: heading = %v, want 40", hdg)
	}
}
