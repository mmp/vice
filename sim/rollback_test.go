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
