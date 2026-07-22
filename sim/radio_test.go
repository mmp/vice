package sim

import (
	"testing"
	"time"

	"github.com/mmp/vice/log"
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
