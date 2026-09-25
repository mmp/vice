// sim/radio_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/speech"
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
	if pc := s.PopReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "N509EZ" {
		t.Fatalf("expected N509EZ (go-ahead response) first, got %s (type %v)", pc.ADSBCallsign, pc.Type)
	}

	// The unrelated initial check-in follows.
	if pc := s.PopReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the initial check-in next")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 next, got %s", pc.ADSBCallsign)
	}

	if pc := s.PopReadyContact([]TCP{tcp}); pc != nil {
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

	if pc := s.PopReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the ready initial check-in")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 (only ready contact), got %s", pc.ADSBCallsign)
	}

	// The response is still not ready.
	if pc := s.PopReadyContact([]TCP{tcp}); pc != nil {
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

	if pc := s.PopReadyContact([]TCP{tcp}); pc == nil {
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

	if pc := s.PopReadyContact([]TCP{tcp}); pc != nil {
		t.Fatalf("popped %s before its track associated", pc.ADSBCallsign)
	}

	ac.AssociateFlightPlan(&NASFlightPlan{ACID: ACID(ac.ADSBCallsign)})

	if pc := s.PopReadyContact([]TCP{tcp}); pc == nil {
		t.Fatal("expected the check-in once the track associated")
	} else if pc.ADSBCallsign != ac.ADSBCallsign {
		t.Fatalf("expected %s, got %s", ac.ADSBCallsign, pc.ADSBCallsign)
	}
}

// TestTransferCommsBeforeAssociation verifies that a departure reaching a /tc
// waypoint before its track tags up is still sent to the departure controller.
// Transfer of comms points can sit a half mile past the departure end, which a
// departure reaches well within the flight plan acquisition delay; dropping the
// action there left the aircraft on nobody's frequency, unable to check in and
// unable to be commanded, with nothing to retry it.
func TestTransferCommsBeforeAssociation(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	s.STARSComputer = makeSTARSComputer("TEST")

	dep := TCP("125.0")
	ac := MakeTestAircraft("AAL123", "13L")
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.ControllerFrequency = "" // hasn't been sent to anyone yet
	s.Aircraft[ac.ADSBCallsign] = ac

	if _, err := s.STARSComputer.CreateFlightPlan(NASFlightPlan{
		ACID:                     ACID(ac.ADSBCallsign),
		InboundHandoffController: dep,
	}); err != nil {
		t.Fatalf("CreateFlightPlan: %v", err)
	}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{TransferComms: true}})

	if ac.ControllerFrequency != dep {
		t.Errorf("expected the pilot on %s, got %q", dep, ac.ControllerFrequency)
	}
	if ac.DepartureContactAltitude != -1 {
		t.Errorf("expected DepartureContactAltitude -1 (contacted), got %v", ac.DepartureContactAltitude)
	}

	// The check-in itself waits until the track tags up.
	s.State.SimTime = s.State.SimTime.Add(time.Second)
	if pc := s.PopReadyContact([]TCP{dep}); pc != nil {
		t.Fatalf("popped %s's check-in before its track associated", pc.ADSBCallsign)
	}

	ac.AssociateFlightPlan(s.STARSComputer.takeFlightPlanByACID(ACID(ac.ADSBCallsign)))
	if pc := s.PopReadyContact([]TCP{dep}); pc == nil {
		t.Error("expected the check-in once the track associated")
	}
}

// TestVirtualHandoffAcceptBeforeAssociation verifies that a departure handed
// off between virtual controllers before its track tags up is still moved to
// the accepting controller's frequency. Routes put a named handoff at the
// departure end, which is flown at 400' AGL, well inside the flight plan
// acquisition delay; leaving the pilot on the old frequency strands every later
// transfer of comms, since those are keyed on the frequency the pilot is
// supposed to be on.
func TestVirtualHandoffAcceptBeforeAssociation(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	s.STARSComputer = makeSTARSComputer("TEST")
	s.ScenarioDefaultConsolidation = PositionConsolidation{TCP("2A"): nil}

	dep, next := TCP("D1D"), TCP("14")
	s.ControlPositions = map[TCP]*av.Controller{"2A": {}, dep: {}, next: {}}
	s.State.Controllers = map[ControlPosition]*av.Controller{"2A": {}, dep: {}, next: {}}

	ac := MakeTestAircraft("AAL123", "13L")
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.ControllerFrequency = dep // released by a virtual departure controller
	ac.DepartureContactAltitude = -1
	s.Aircraft[ac.ADSBCallsign] = ac

	if _, err := s.STARSComputer.CreateFlightPlan(NASFlightPlan{
		ACID:               ACID(ac.ADSBCallsign),
		TypeOfFlight:       av.FlightTypeDeparture,
		TrackingController: dep,
	}); err != nil {
		t.Fatalf("CreateFlightPlan: %v", err)
	}

	// The route's /ho14 at the departure end, before the track associates.
	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{
		Actions: av.WaypointActions{HandoffController: next}})

	fp := s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
	if fp.HandoffController != next {
		t.Fatalf("expected a handoff to %s, got %q", next, fp.HandoffController)
	}

	// The other virtual controller accepts while the track is still untagged.
	s.State.SimTime = s.State.SimTime.Add(time.Minute)
	s.lastSimUpdate = s.State.SimTime // skip the once-a-second aircraft update
	s.updateState()

	if fp.TrackingController != next {
		t.Fatalf("expected %s to own the track, got %q", next, fp.TrackingController)
	}
	if ac.ControllerFrequency != ControlPosition(next) {
		t.Errorf("expected the pilot on %s, got %q", next, ac.ControllerFrequency)
	}
}

// TestWaypointScratchpadBeforeAssociation verifies that a waypoint's scratchpad
// command reaches the flight plan before the track tags up--the scratchpad
// lives on the flight plan, which is reachable by ACID either way--and that it
// still leaves alone the scratchpad of an aircraft a human is working.
func TestWaypointScratchpadBeforeAssociation(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	s.STARSComputer = makeSTARSComputer("TEST")
	s.ScenarioDefaultConsolidation = PositionConsolidation{TCP("2A"): nil}

	ac := MakeTestAircraft("AAL123", "13L")
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.ControllerFrequency = ""
	s.Aircraft[ac.ADSBCallsign] = ac

	if _, err := s.STARSComputer.CreateFlightPlan(NASFlightPlan{ACID: ACID(ac.ADSBCallsign)}); err != nil {
		t.Fatalf("CreateFlightPlan: %v", err)
	}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{PrimaryScratchpad: "GRB"}})

	sfp := s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
	if sfp.Scratchpad != "GRB" {
		t.Errorf("expected scratchpad GRB on the unassociated flight plan, got %q", sfp.Scratchpad)
	}

	// Once a human is working the aircraft, the route leaves the scratchpad be.
	ac.ControllerFrequency = "2A"
	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{PrimaryScratchpad: "MSP"}})
	if sfp.Scratchpad != "GRB" {
		t.Errorf("route overwrote the scratchpad of an aircraft a human is working: %q", sfp.Scratchpad)
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

	if pc := s.PopReadyContact([]TCP{first, last}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "SWA22" {
		t.Fatalf("expected the longer-waiting SWA22, got %s", pc.ADSBCallsign)
	}

	if pc := s.PopReadyContact([]TCP{first, last}); pc == nil {
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

// TestWaypointClimbActionUnderHumanControl verifies that a route's /c is
// issued while a virtual controller is working the aircraft and left to the
// human once the aircraft is on their frequency.
func TestWaypointClimbActionUnderHumanControl(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	s.STARSComputer = makeSTARSComputer("TEST")
	s.ScenarioDefaultConsolidation = PositionConsolidation{TCP("2A"): nil}

	ac := MakeTestAircraft("AAL123", "13L")
	ac.Nav.Perf.Ceiling = 41000
	ac.ControllerFrequency = ""
	s.Aircraft[ac.ADSBCallsign] = ac

	assigned := func() float32 {
		if alt := ac.Nav.Altitude.Assigned; alt != nil {
			return *alt
		}
		return 0
	}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{ClimbAltitude: 5000}})
	if assigned() != 5000 {
		t.Fatalf("expected the route's climb to 5000 to be assigned, got %.0f", assigned())
	}

	ac.ControllerFrequency = "2A"
	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{ClimbAltitude: 7000}})
	if assigned() != 5000 {
		t.Errorf("route changed the altitude of an aircraft a human is working: %.0f", assigned())
	}
}

// An emergency transmission is built when the stage fires but spoken later, so
// it is the one transmission that can be saved to the user's config and read
// back. Its arguments are typed, and JSON erases those types unless
// RadioTransmission carries them along.
func TestEmergencyTransmissionSurvivesSaving(t *testing.T) {
	lg := testLogger()
	s := NewTestSim(lg)
	ac := MakeTestAircraft("AAL123", "22L")
	ac.FlightPlan.DepartureAirport = "KFRG"
	s.Aircraft[ac.ADSBCallsign] = ac

	tcp := TCP("125.0")
	rt := speech.MakeContactTransmission(
		"declaring an emergency, [we have|] {num} souls, request return to {airport}, level at {alt}",
		112, ac.FlightPlan.DepartureAirport, 4000)
	s.enqueueEmergencyTransmission(ac.ADSBCallsign, tcp, rt)
	for i := range s.PendingContacts[tcp] {
		s.PendingContacts[tcp][i].ReadyTime = s.State.SimTime.Add(-time.Second)
	}

	b, err := json.Marshal(s.PendingContacts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	clear(s.PendingContacts)
	if err := json.Unmarshal(b, &s.PendingContacts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	pc := s.PopReadyContact([]TCP{tcp})
	if pc == nil {
		t.Fatal("no pending contact after saving and reloading")
	}
	spoken, written := s.GenerateContactTransmission(pc)
	if spoken == "" || written == "" {
		t.Fatalf("emergency transmission rendered nothing after saving: %q / %q", spoken, written)
	}
	if !strings.Contains(written, "KFRG") || !strings.Contains(written, "112") ||
		!strings.Contains(written, "4,000") {
		t.Errorf("emergency transmission lost arguments after saving: %q", written)
	}
}

// A transmission with an argument the formatter can't handle leaves the pilot
// silent, which to the controller looks like an aircraft ignoring them. The
// failure has to reach the messages pane rather than only the log.
func TestUnformattableTransmissionIsReported(t *testing.T) {
	s := NewTestSim(testLogger())
	ac := MakeTestAircraft("AAL123", "22L")
	s.Aircraft[ac.ADSBCallsign] = ac

	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()

	rt := speech.MakeReadbackTransmission("departing {airport}", "KFRG") // want an ICAOAirportCode
	s.postReadbackTransmission(ac.ADSBCallsign, *rt, TCW("TEST"))

	events := sub.Get()
	if slices.ContainsFunc(events, func(e Event) bool { return e.Type == RadioTransmissionEvent }) {
		t.Error("posted a transmission that couldn't be formatted")
	}

	i := slices.IndexFunc(events, func(e Event) bool { return e.Type == ErrorMessageEvent })
	if i == -1 {
		t.Fatalf("nothing reported the failed transmission: %+v", events)
	}
	if msg := events[i].WrittenText; !strings.Contains(msg, "AAL123") ||
		!strings.Contains(msg, "departing {airport}") {
		t.Errorf("report %q names neither the aircraft nor the phrase that failed", msg)
	}
	if to := events[i].ToController; to != TCP("125.0") {
		t.Errorf("report went to %q, want the controller the readback was for", to)
	}
}
