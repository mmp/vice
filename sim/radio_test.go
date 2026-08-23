package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

// TestPopReadyContactPrioritizesResponses verifies that a pilot's response or
// request during an established exchange (here, the full request after "go
// ahead") is spoken before an unrelated aircraft's initial check-in, even when
// the check-in was queued first.
func TestPopReadyContactPrioritizesResponses(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: tcp, Type: PendingTransmissionArrival, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: past},
	}

	// The go-ahead response comes out first despite being enqueued later.
	if pc := s.popReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "N509EZ" {
		t.Fatalf("expected N509EZ (go-ahead response) first, got %s (type %v)", pc.ADSBCallsign, pc.Type)
	}

	// The unrelated initial check-in follows.
	if pc := s.popReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the initial check-in next")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 next, got %s", pc.ADSBCallsign)
	}

	if pc := s.popReadyContact([]TCP{tcp}); pc != nil {
		t.Fatalf("expected empty queue, got %s", pc.ADSBCallsign)
	}
}

// TestPopReadyContactRespectsReadyTime verifies that response prioritization
// does not override ReadyTime: a response that isn't ready yet must not
// preempt an initial check-in that is.
func TestPopReadyContactRespectsReadyTime(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)
	future := s.State.SimTime.Add(10 * time.Second)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: tcp, Type: PendingTransmissionArrival, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: future},
	}

	if pc := s.popReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the ready initial check-in")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 (only ready contact), got %s", pc.ADSBCallsign)
	}

	// The response is still not ready.
	if pc := s.popReadyContact([]TCP{tcp}); pc != nil {
		t.Fatalf("expected no ready contact, got %s", pc.ADSBCallsign)
	}
}

// TestPopReadyContactAbbreviatedVFRIsInitial verifies that the abbreviated
// "VFR request" is classified as an initial check-in, so it yields to a
// response-type transmission.
func TestPopReadyContactAbbreviatedVFRIsInitial(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "N12AB", TCP: tcp, Type: PendingTransmissionFlightFollowingReq, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: past},
	}

	if pc := s.popReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "N509EZ" {
		t.Fatalf("expected N509EZ (response) before the abbreviated VFR request, got %s", pc.ADSBCallsign)
	}
}

// TestPopReadyContactWaitsForAssociation verifies that a departure's check-in
// stays queued until its track tags up. GenerateContactTransmission has nothing
// to say for an unassociated track, and a popped contact that comes back empty
// is discarded, so popping one early would lose the check-in for good.
func TestPopReadyContactWaitsForAssociation(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	ac := MakeTestAircraft("AAL123", "13L")
	ac.TypeOfFlight = av.FlightTypeDeparture
	s.Aircraft[ac.ADSBCallsign] = ac

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Type: PendingTransmissionDeparture,
			ReadyTime: s.State.SimTime.Add(-time.Second)},
	}

	if pc := s.popReadyContact([]TCP{tcp}); pc != nil {
		t.Fatalf("popped %s before its track associated", pc.ADSBCallsign)
	}

	ac.AssociateFlightPlan(&NASFlightPlan{ACID: ACID(ac.ADSBCallsign)})

	if pc := s.popReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the check-in once the track associated")
	} else if pc.ADSBCallsign != ac.ADSBCallsign {
		t.Fatalf("expected %s, got %s", ac.ADSBCallsign, pc.ADSBCallsign)
	}
}

// TestPopReadyContactTakesOldestAcrossPositions verifies that a check-in on a
// later-scanned position isn't starved by a fresher one on an earlier position:
// with departures split across positions by SID, first-position-first ordering
// would leave the last position's SID silent whenever the queue is backed up.
func TestPopReadyContactTakesOldestAcrossPositions(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	first, last := TCP("125.0"), TCP("126.6")

	s.PendingContacts[first] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: first, Type: PendingTransmissionDeparture,
			ReadyTime: s.State.SimTime.Add(-10 * time.Second)},
	}
	s.PendingContacts[last] = []PendingContact{
		{ADSBCallsign: "SWA22", TCP: last, Type: PendingTransmissionDeparture,
			ReadyTime: s.State.SimTime.Add(-time.Minute)},
	}

	if pc := s.popReadyContact([]TCP{first, last}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "SWA22" {
		t.Fatalf("expected the longer-waiting SWA22, got %s", pc.ADSBCallsign)
	}

	if pc := s.popReadyContact([]TCP{first, last}); pc == nil {
		t.Fatal("expected the second contact")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90, got %s", pc.ADSBCallsign)
	}
}

// makeTowerSwitchAircraft returns an aircraft cleared for the approach and past
// the FAF, positioned the given distance north of the runway threshold.
func makeTowerSwitchAircraft(engineType string, distance float32) *Aircraft {
	ac := MakeTestAircraft("AAL123", "13L")
	ac.Nav.Perf.Engine.AircraftType = engineType
	ac.Nav.FlightState.Position = math.Point2LL{0, distance / 60}
	ac.Nav.Approach.Cleared = true
	ac.Nav.Approach.PassedFAF = true
	return ac
}

// TestTowerSwitchDistanceGate verifies that the pilot only asks about switching
// to tower once close to the runway. Aircraft cleared for a visual approach
// cross a synthetic FAF marker that can sit far from the field, so passing it
// alone must not trigger the question.
func TestTowerSwitchDistanceGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		engine   string
		distance float32
		want     bool
	}{
		{"jet just inside", "J", 4.5, true},
		{"jet just outside", "J", 5.5, false},
		{"jet cleared visual far out", "J", 20, false},
		{"turboprop inside jet range", "T", 4, false},
		{"turboprop just inside", "T", 2.5, true},
		{"piston just inside", "P", 2.5, true},
		{"piston just outside", "P", 3.5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAskAboutTowerSwitch(makeTowerSwitchAircraft(tc.engine, tc.distance)); got != tc.want {
				t.Errorf("shouldAskAboutTowerSwitch() = %v, want %v", got, tc.want)
			}
		})
	}

	// Close in, but the approach isn't underway: no question either way.
	notCleared := makeTowerSwitchAircraft("J", 2)
	notCleared.Nav.Approach.Cleared = false
	if shouldAskAboutTowerSwitch(notCleared) {
		t.Error("expected no question when not cleared for the approach")
	}

	beforeFAF := makeTowerSwitchAircraft("J", 2)
	beforeFAF.Nav.Approach.PassedFAF = false
	if shouldAskAboutTowerSwitch(beforeFAF) {
		t.Error("expected no question before the FAF")
	}

	noApproach := makeTowerSwitchAircraft("J", 2)
	noApproach.Nav.Approach.Assigned = nil
	if shouldAskAboutTowerSwitch(noApproach) {
		t.Error("expected no question without an assigned approach")
	}
}

// TestTowerSwitchTransmissionGoesStale verifies that a queued tower-switch
// question is dropped at dispatch if it went moot while waiting to be spoken,
// either because the controller sent the aircraft to tower or because it is no
// longer flying the approach (go-around, cancelled clearance).
func TestTowerSwitchTransmissionGoesStale(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	tcp := TCP("125.0")

	for _, tc := range []struct {
		name  string
		spoil func(*Aircraft)
	}{
		{"sent to tower", func(ac *Aircraft) { ac.GotContactTower = true }},
		{"no longer cleared", func(ac *Aircraft) { ac.Nav.Approach.Cleared = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(lg)
			ac := makeTowerSwitchAircraft("J", 2)
			s.Aircraft[ac.ADSBCallsign] = ac

			pc := &PendingContact{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Type: PendingTransmissionRequestTowerSwitch}
			if spoken, _ := s.GenerateContactTransmission(pc); spoken == "" {
				t.Fatal("expected a transmission before the question goes stale")
			}

			tc.spoil(ac)
			if spoken, _ := s.GenerateContactTransmission(pc); spoken != "" {
				t.Errorf("expected the stale question to be dropped, got %q", spoken)
			}
		})
	}
}
