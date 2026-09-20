package sim

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
)

// makeCorrectionSim builds a sim with two ordinary (unprivileged) TCWs, so
// that command authority follows the aircraft's frequency.
func makeCorrectionSim() *Sim {
	s := NewTestSim(&log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	clear(s.PrivilegedTCWs)
	s.State.CurrentConsolidation["OTHER"] = &TCPConsolidation{PrimaryTCP: "126.0"}
	for _, cs := range []av.ADSBCallsign{"UAL123", "DAL456", "N123AB"} {
		ac := MakeTestAircraft(cs, "22L")
		ac.Nav.Perf = db.DB.AircraftPerformance["A320"]
		s.Aircraft[cs] = ac
	}
	s.Aircraft["DAL456"].ControllerFrequency = "126.0"
	return s
}

func runCorrectionCommand(t *testing.T, s *Sim, tcw TCW, callsign av.ADSBCallsign, commands string) {
	t.Helper()
	if res := s.RunAircraftControlCommands(tcw, callsign, commands, 0); res.Error != nil {
		t.Fatalf("%s: %s %s: %v", tcw, callsign, commands, res.Error)
	}
}

func assertHeading(t *testing.T, s *Sim, callsign av.ADSBCallsign, want int) {
	t.Helper()
	if h, ok := s.Aircraft[callsign].Nav.AssignedHeading(); !ok || int(h) != want {
		t.Errorf("%s: assigned heading = %v (ok=%v), want %d", callsign, h, ok, want)
	}
}

func TestCorrectionHistoryPerTCW(t *testing.T) {
	s := makeCorrectionSim()
	runCorrectionCommand(t, s, E2ETCW(), "UAL123", "L010 D20 ATIS/V")
	runCorrectionCommand(t, s, "OTHER", "DAL456", "R090")

	// A correction that re-addresses a different aircraft undoes everything the
	// previous transmission did, on this TCW only.
	runCorrectionCommand(t, s, E2ETCW(), "N123AB/T", "CORRECTION L030")
	if _, ok := s.Aircraft["UAL123"].Nav.AssignedHeading(); ok {
		t.Error("wrong recipient retained its heading")
	}
	if ac := s.Aircraft["UAL123"]; ac.Nav.Altitude.Assigned != nil || ac.ReportedATIS != "" {
		t.Error("wrong recipient retained altitude or ATIS")
	}
	assertHeading(t, s, "DAL456", 90)
	assertHeading(t, s, "N123AB", 30)
	if ac := s.Aircraft["N123AB"]; ac.LastAddressingForm != AddressingFormTypeTrailing3 {
		t.Error("correction lost type/trailing-three addressing")
	}

	// One that names the same aircraft amends what it was told; nothing is undone.
	runCorrectionCommand(t, s, E2ETCW(), "N123AB/T", "CORRECTION L050")
	assertHeading(t, s, "N123AB", 50)

	runCorrectionCommand(t, s, E2ETCW(), "N123AB", "ROLLBACK")
	assertHeading(t, s, "N123AB", 30)
	runCorrectionCommand(t, s, E2ETCW(), "N123AB", "ROLLBACK")
	assertHeading(t, s, "N123AB", 30)
}

// TestCorrectionAfterAcknowledgement checks that a transmission carrying no commands
// still becomes what a following correction revises.
func TestCorrectionAfterAcknowledgement(t *testing.T) {
	s := makeCorrectionSim()
	runCorrectionCommand(t, s, E2ETCW(), "UAL123", "L010")
	runCorrectionCommand(t, s, E2ETCW(), "N123AB", "")
	runCorrectionCommand(t, s, E2ETCW(), "N123AB", "CORRECTION L020")
	assertHeading(t, s, "UAL123", 10)
	assertHeading(t, s, "N123AB", 20)
}

// TestLastAddressedCallsign covers what makeDerivedState publishes for clients to
// attribute a correction that names no aircraft.
func TestLastAddressedCallsign(t *testing.T) {
	s := makeCorrectionSim()
	if cs := s.lastAddressedCallsign(E2ETCW()); cs != "" {
		t.Errorf("nothing transmitted yet: %q", cs)
	}
	runCorrectionCommand(t, s, E2ETCW(), "UAL123", "L010")
	if cs := s.lastAddressedCallsign(E2ETCW()); cs != "UAL123" {
		t.Errorf("after transmitting: %q", cs)
	}
	if cs := s.lastAddressedCallsign("OTHER"); cs != "" {
		t.Errorf("another TCW: %q", cs)
	}
	// An aircraft the controller can no longer command is no longer theirs to correct.
	s.Aircraft["UAL123"].ControllerFrequency = "126.0"
	if cs := s.lastAddressedCallsign(E2ETCW()); cs != "" {
		t.Errorf("after leaving the frequency: %q", cs)
	}

	// The form the controller has been using rides along, so that a correction goes
	// back to them the same way.
	runCorrectionCommand(t, s, E2ETCW(), "N123AB/T", "L020")
	if cs := s.lastAddressedCallsign(E2ETCW()); cs != "N123AB/T" {
		t.Errorf("type/trailing-three addressing: %q", cs)
	}
}

func TestCorrectionFrequencyTransfer(t *testing.T) {
	for _, transfer := range []string{"controller", "tower", "radar services terminated"} {
		t.Run(transfer, func(t *testing.T) {
			s := makeCorrectionSim()
			ac := s.Aircraft["UAL123"]
			runCorrectionCommand(t, s, E2ETCW(), ac.ADSBCallsign, "L010 D20")
			switch transfer {
			case "controller":
				s.mu.Lock(s.lg)
				s.contactController("125.0", &NASFlightPlan{}, ac, "126.0")
				s.mu.Unlock(s.lg)
			case "tower":
				ac.Nav.Approach.Cleared = true
				runCorrectionCommand(t, s, E2ETCW(), ac.ADSBCallsign, "TO")
			case "radar services terminated":
				if _, err := s.RadarServicesTerminated(E2ETCW(), ac.ADSBCallsign); err != nil {
					t.Fatal(err)
				}
			}

			res := s.RunAircraftControlCommands(E2ETCW(), "UAL123", "CORRECTION L020", 0)
			if !errors.Is(res.Error, av.ErrOtherControllerHasTrack) {
				t.Fatalf("correction after leaving frequency: %v", res.Error)
			}
			if res := s.RunAircraftControlCommands(E2ETCW(), "UAL123", "ROLLBACK", 0); res.Error != nil {
				t.Fatal(res.Error)
			}
			assertHeading(t, s, "UAL123", 10)

			if transfer == "controller" {
				s.State.SimTime = s.State.SimTime.Add(time.Minute)
				s.processFutureFrequencyChanges()
				runCorrectionCommand(t, s, "OTHER", ac.ADSBCallsign, "L080")
				runCorrectionCommand(t, s, "OTHER", ac.ADSBCallsign, "ROLLBACK")
				assertHeading(t, s, "UAL123", 10)
			}

			// Coming back does not revive what the controller said before the transfer.
			s.setControllerFrequency(ac, "125.0")
			runCorrectionCommand(t, s, E2ETCW(), "UAL123", "ROLLBACK")
			assertHeading(t, s, "UAL123", 10)
		})
	}
}

func TestCorrectionPrivilegedTCW(t *testing.T) {
	s := makeCorrectionSim()
	s.PrivilegedTCWs[E2ETCW()] = true
	// DAL456 is tuned to another position, but an instructor may command it
	// and so must be able to correct what they said.
	runCorrectionCommand(t, s, E2ETCW(), "DAL456", "L010")
	runCorrectionCommand(t, s, E2ETCW(), "DAL456", "CORRECTION L030")
	assertHeading(t, s, "DAL456", 30)
	runCorrectionCommand(t, s, E2ETCW(), "DAL456", "ROLLBACK")
	assertHeading(t, s, "DAL456", 10)
}

func TestCorrectionMissingContext(t *testing.T) {
	s := makeCorrectionSim()
	// Nothing to undo: the instruction still has to reach the aircraft.
	runCorrectionCommand(t, s, E2ETCW(), "UAL123", "CORRECTION L020")
	assertHeading(t, s, "UAL123", 20)

	s.ClearSTTCommands(E2ETCW())
	runCorrectionCommand(t, s, E2ETCW(), "UAL123", "ROLLBACK")
	assertHeading(t, s, "UAL123", 20)
}

func TestCorrectionConcurrentTCWs(t *testing.T) {
	s := makeCorrectionSim()
	start := make(chan struct{})
	done := make(chan error, 2)
	for tcw, cs := range map[TCW]av.ADSBCallsign{E2ETCW(): "UAL123", "OTHER": "DAL456"} {
		go func() {
			<-start
			for range 10 {
				if res := s.RunAircraftControlCommands(tcw, cs, "H010", 0); res.Error != nil {
					done <- res.Error
					return
				}
				if res := s.RunAircraftControlCommands(tcw, cs, "CORRECTION H020", 0); res.Error != nil {
					done <- res.Error
					return
				}
			}
			done <- nil
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for _, cs := range []av.ADSBCallsign{"UAL123", "DAL456"} {
		assertHeading(t, s, cs, 20)
	}
}

// A panic while running control commands must surface as a panic rather than deadlocking
// on s.mu, and must not leave the mutex held. Nilling the correction history reproduces
// the nil-map write that originally hung the sim instead of reporting a crash.
func TestControlCommandPanicSurfaces(t *testing.T) {
	s := makeCorrectionSim()
	s.lastSTTCommands = nil

	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		s.RunAircraftControlCommands(E2ETCW(), "UAL123", "H010", 0)
	}()

	select {
	case r := <-panicked:
		if r == nil {
			t.Fatal("expected a panic from the nil correction history map")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAircraftControlCommands deadlocked instead of panicking")
	}

	if !s.mu.TryLock() {
		t.Error("s.mu still held after the panic unwound")
	}
}
