// sim/radio_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/speech"
)

// sayNextContact returns the pending contact to be said next on the given
// positions and takes it out of the queue, as reporting it would.
func sayNextContact(s *Sim, positions []TCP) *PendingContact {
	pc := s.nextContact(positions)
	if pc != nil {
		s.PendingContacts[pc.TCP] = slices.DeleteFunc(s.PendingContacts[pc.TCP], func(p PendingContact) bool {
			return p.ADSBCallsign == pc.ADSBCallsign && p.Type == pc.Type
		})
	}
	return pc
}

// addTunedAircraft adds an associated aircraft tuned to tcp, so that the
// contacts it queues there apply.
func addTunedAircraft(s *Sim, callsign av.ADSBCallsign, tcp TCP) {
	ac := MakeTestAircraft(callsign, "13L")
	ac.ControllerFrequency = ControlPosition(tcp)
	ac.AssociateFlightPlan(&FlightPlan{ACID: ACID(callsign)})
	s.Aircraft[callsign] = ac
}

// TestNextContactPrioritizesResponses verifies that a pilot's response or
// request during an established exchange (here, the full request after "go
// ahead") is spoken before an unrelated aircraft's initial check-in, even when
// the check-in was queued first.
func TestNextContactPrioritizesResponses(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)
	addTunedAircraft(s, "AAL90", tcp)
	addTunedAircraft(s, "N509EZ", tcp)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: tcp, Type: PendingTransmissionArrival, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: past},
	}

	// The go-ahead response comes out first despite being enqueued later.
	if pc := sayNextContact(s, []TCP{tcp}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "N509EZ" {
		t.Fatalf("expected N509EZ (go-ahead response) first, got %s (type %v)", pc.ADSBCallsign, pc.Type)
	}

	// The unrelated initial check-in follows.
	if pc := sayNextContact(s, []TCP{tcp}); pc == nil {
		t.Fatal("expected the initial check-in next")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 next, got %s", pc.ADSBCallsign)
	}

	if pc := sayNextContact(s, []TCP{tcp}); pc != nil {
		t.Fatalf("expected empty queue, got %s", pc.ADSBCallsign)
	}
}

// TestNextContactRespectsReadyTime verifies that response prioritization
// does not override ReadyTime: a response that isn't ready yet must not
// preempt an initial check-in that is.
func TestNextContactRespectsReadyTime(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)
	future := s.State.SimTime.Add(10 * time.Second)
	addTunedAircraft(s, "AAL90", tcp)
	addTunedAircraft(s, "N509EZ", tcp)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: tcp, Type: PendingTransmissionArrival, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: future},
	}

	if pc := sayNextContact(s, []TCP{tcp}); pc == nil {
		t.Fatal("expected the ready initial check-in")
	} else if pc.ADSBCallsign != "AAL90" {
		t.Fatalf("expected AAL90 (only ready contact), got %s", pc.ADSBCallsign)
	}

	// The response is still not ready.
	if pc := sayNextContact(s, []TCP{tcp}); pc != nil {
		t.Fatalf("expected no ready contact, got %s", pc.ADSBCallsign)
	}
}

// TestNextContactAbbreviatedVFRIsInitial verifies that the abbreviated
// "VFR request" is classified as an initial check-in, so it yields to a
// response-type transmission.
func TestNextContactAbbreviatedVFRIsInitial(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	tcp := TCP("125.0")
	past := s.State.SimTime.Add(-time.Second)
	addTunedAircraft(s, "N12AB", tcp)
	addTunedAircraft(s, "N509EZ", tcp)

	s.PendingContacts[tcp] = []PendingContact{
		{ADSBCallsign: "N12AB", TCP: tcp, Type: PendingTransmissionFlightFollowingReq, ReadyTime: past},
		{ADSBCallsign: "N509EZ", TCP: tcp, Type: PendingTransmissionFlightFollowingFull, ReadyTime: past},
	}

	if pc := sayNextContact(s, []TCP{tcp}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "N509EZ" {
		t.Fatalf("expected N509EZ (response) before the abbreviated VFR request, got %s", pc.ADSBCallsign)
	}
}

// TestNextContactWaitsForAssociation verifies that a departure's check-in
// isn't offered until its track tags up; the pilot has nothing to say for an
// unassociated track.
func TestNextContactWaitsForAssociation(t *testing.T) {
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

	if pc := sayNextContact(s, []TCP{tcp}); pc != nil {
		t.Fatalf("offered %s before its track associated", pc.ADSBCallsign)
	}

	ac.AssociateFlightPlan(&FlightPlan{ACID: ACID(ac.ADSBCallsign)})

	if pc := sayNextContact(s, []TCP{tcp}); pc == nil {
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

	if _, err := s.STARSComputer.CreateFlightPlan(FlightPlan{
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
	if pc := sayNextContact(s, []TCP{dep}); pc != nil {
		t.Fatalf("offered %s's check-in before its track associated", pc.ADSBCallsign)
	}

	ac.AssociateFlightPlan(s.STARSComputer.takeFlightPlanByACID(ACID(ac.ADSBCallsign)))
	if pc := sayNextContact(s, []TCP{dep}); pc == nil {
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

	if _, err := s.STARSComputer.CreateFlightPlan(FlightPlan{
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

	if _, err := s.STARSComputer.CreateFlightPlan(FlightPlan{ACID: ACID(ac.ADSBCallsign)}); err != nil {
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

// TestNextContactTakesOldestAcrossPositions verifies that a check-in on a
// later-scanned position isn't starved by a fresher one on an earlier position:
// with departures split across positions by SID, first-position-first ordering
// would leave the last position's SID silent whenever the queue is backed up.
func TestNextContactTakesOldestAcrossPositions(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)

	first, last := TCP("125.0"), TCP("126.6")
	addTunedAircraft(s, "AAL90", first)
	addTunedAircraft(s, "SWA22", last)

	s.PendingContacts[first] = []PendingContact{
		{ADSBCallsign: "AAL90", TCP: first, Type: PendingTransmissionDeparture,
			ReadyTime: s.State.SimTime.Add(-10 * time.Second)},
	}
	s.PendingContacts[last] = []PendingContact{
		{ADSBCallsign: "SWA22", TCP: last, Type: PendingTransmissionDeparture,
			ReadyTime: s.State.SimTime.Add(-time.Minute)},
	}

	if pc := sayNextContact(s, []TCP{first, last}); pc == nil {
		t.Fatal("expected a ready contact")
	} else if pc.ADSBCallsign != "SWA22" {
		t.Fatalf("expected the longer-waiting SWA22, got %s", pc.ADSBCallsign)
	}

	if pc := sayNextContact(s, []TCP{first, last}); pc == nil {
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

// instruct has the controller at the test TCW give the aircraft an
// instruction.
func instruct(s *Sim, ac *Aircraft) {
	if _, err := s.dispatchControlledAircraftCommand(E2ETCW(), ac.ADSBCallsign,
		func(TCW, *Aircraft) speech.CommandIntent { return nil }); err != nil {
		panic(err)
	}
}

// TestContactGoesStale verifies each pending transmission type's rule for
// when it has gone moot while waiting to be spoken.
func TestContactGoesStale(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	tcp := TCP("125.0")

	clear := func(s *Sim, ac *Aircraft) { ac.Nav.Approach.Cleared = true }
	intercepted := func(ac *Aircraft) { ac.Nav.Approach.InterceptState = nav.OnApproachCourse }
	cleared := func(ac *Aircraft) { ac.Nav.Approach.Cleared = true }
	sighted := func(ac *Aircraft) { ac.SightedReportingPoint = &av.ReportingPoint{Names: []string{"Bridge"}} }

	for _, tc := range []struct {
		name  string
		ty    PendingTransmissionType
		setup func(*Aircraft)
		spoil func(*Sim, *Aircraft)
	}{
		{"aircraft gone", PendingTransmissionGoAround, nil,
			func(s *Sim, ac *Aircraft) { delete(s.Aircraft, ac.ADSBCallsign) }},
		{"off frequency", PendingTransmissionTrafficInSight, nil,
			func(s *Sim, ac *Aircraft) { ac.ControllerFrequency = "_TOWER" }},
		{"check-in after an instruction", PendingTransmissionArrival, nil, instruct},
		{"vectors after an instruction", PendingTransmissionRequestVectors, nil, instruct},
		{"approach clearance request cleared", PendingTransmissionRequestApproachClearance, intercepted, clear},
		{"approach clearance request off course", PendingTransmissionRequestApproachClearance, intercepted,
			func(s *Sim, ac *Aircraft) { ac.Nav.Approach.InterceptState = nav.NotIntercepting }},
		{"field in sight cleared", PendingTransmissionSpontaneousFieldInSight, nil, clear},
		{"field in sight cleared at fix", PendingTransmissionSpontaneousFieldInSight, nil,
			func(s *Sim, ac *Aircraft) { ac.Nav.Approach.AtFixClearedRoute = []av.Waypoint{{Fix: "ONE"}} }},
		{"visual request cleared", PendingTransmissionRequestVisual, nil, clear},
		{"altitude assigned", PendingTransmissionRequestAltitude, nil,
			func(s *Sim, ac *Aircraft) { ac.Nav.Altitude.Assigned = new(float32(3000)) }},
		{"altitude after speed", PendingTransmissionRequestAltitude, nil,
			func(s *Sim, ac *Aircraft) { ac.Nav.Altitude.AfterSpeed = new(float32(3000)) }},
		{"altitude request cleared", PendingTransmissionRequestAltitude, nil, clear},
		{"tower switch no longer cleared", PendingTransmissionRequestTowerSwitch, cleared,
			func(s *Sim, ac *Aircraft) { ac.Nav.Approach.Cleared = false }},
		{"reporting point cleared", PendingTransmissionSpontaneousReportingPointInSight, sighted, clear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewTestSim(lg)
			ac := MakeTestAircraft("UPS1330", "12")
			if tc.setup != nil {
				tc.setup(ac)
			}
			s.Aircraft[ac.ADSBCallsign] = ac

			pc := PendingContact{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Type: tc.ty,
				QueuedTime: s.State.SimTime.Add(-2 * time.Second), ReadyTime: s.State.SimTime.Add(-time.Second)}
			if !s.contactApplies(pc) {
				t.Fatal("expected the transmission to apply before it goes stale")
			}

			tc.spoil(s, ac)
			if s.contactApplies(pc) {
				t.Error("expected the stale transmission not to apply")
			}
		})
	}
}

// TestCheckInSurvivesOtherInstructions verifies that a check-in is only moot
// once the controller it is for gives the pilot an instruction: the previous
// controller's instructions and any given before the pilot queued the
// check-in don't count.
func TestCheckInSurvivesOtherInstructions(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	ac := MakeTestAircraft("AAL123", "13L")
	s.Aircraft[ac.ADSBCallsign] = ac

	instruct(s, ac)
	s.State.SimTime = s.State.SimTime.Add(time.Second)
	pc := PendingContact{ADSBCallsign: ac.ADSBCallsign, TCP: "126.6", Type: PendingTransmissionArrival,
		QueuedTime: s.State.SimTime, ReadyTime: s.State.SimTime.Add(8 * time.Second)}
	if !s.contactApplies(pc) {
		t.Fatal("expected a check-in queued after an instruction to apply")
	}

	instruct(s, ac) // still on the previous controller's frequency
	if !s.contactApplies(pc) {
		t.Error("expected the previous controller's instruction to leave the check-in in place")
	}

	ac.ControllerFrequency = "126.6"
	s.State.SimTime = s.State.SimTime.Add(10 * time.Second)
	if !s.contactApplies(pc) {
		t.Fatal("expected the check-in to apply once the pilot is on the frequency")
	}
	instruct(s, ac)
	if s.contactApplies(pc) {
		t.Error("expected the check-in to be moot once the new controller gave an instruction")
	}
}

// TestContactWaitsForFrequencySwitch verifies that a check-in isn't dropped
// for being off frequency while the pilot is still switching, only once the
// pilot is ready to talk.
func TestContactWaitsForFrequencySwitch(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	ac := MakeTestAircraft("AAL123", "13L")
	ac.ControllerFrequency = ""
	s.Aircraft[ac.ADSBCallsign] = ac

	pc := PendingContact{ADSBCallsign: ac.ADSBCallsign, TCP: "126.6", Type: PendingTransmissionArrival,
		QueuedTime: s.State.SimTime, ReadyTime: s.State.SimTime.Add(8 * time.Second)}
	if !s.contactApplies(pc) {
		t.Fatal("expected the check-in to apply while the pilot switches frequencies")
	}

	s.State.SimTime = s.State.SimTime.Add(10 * time.Second)
	if s.contactApplies(pc) {
		t.Error("expected the check-in not to apply once ready with the pilot elsewhere")
	}
}

// TestStaleContactIsCulled verifies that a contact that has gone moot is
// neither offered to the controller nor kept in the queue.
func TestStaleContactIsCulled(t *testing.T) {
	lg := log.New(true, "error", t.TempDir())
	s := NewTestSim(lg)
	tcp := TCP("125.0")
	ac := MakeTestAircraft("UPS1330", "12")
	s.Aircraft[ac.ADSBCallsign] = ac

	s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionRequestAltitude)
	s.State.SimTime = s.State.SimTime.Add(time.Second)
	if s.nextContact([]TCP{tcp}) == nil {
		t.Fatal("expected the altitude request to be ready")
	}

	ac.Nav.Altitude.Assigned = new(float32(3000))
	if s.nextContact([]TCP{tcp}) != nil {
		t.Error("expected the moot altitude request not to be offered")
	}
	s.cullStaleContacts()
	if n := len(s.PendingContacts[tcp]); n != 0 {
		t.Errorf("expected the moot altitude request to be culled, %d contacts left", n)
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
	ac.DepartureAirport = "KFRG"
	s.Aircraft[ac.ADSBCallsign] = ac

	tcp := TCP("125.0")
	rt := speech.MakeContactTransmission(
		"declaring an emergency, [we have|] {num} souls, request return to {airport}, level at {alt}",
		112, ac.DepartureAirport, 4000)
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

	pt := s.NextPilotTransmission(E2ETCW())
	if pt == nil {
		t.Fatal("no pilot transmission after saving and reloading")
	}
	if pt.Spoken == "" || pt.Written == "" {
		t.Fatalf("emergency transmission rendered nothing after saving: %q / %q", pt.Spoken, pt.Written)
	}
	if !strings.Contains(pt.Written, "KFRG") || !strings.Contains(pt.Written, "112") ||
		!strings.Contains(pt.Written, "4,000") {
		t.Errorf("emergency transmission lost arguments after saving: %q", pt.Written)
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
	s.postReadbackTransmission(ac.ADSBCallsign, rt, TCW("TEST"))

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

// TestTransmissionTextMatchesSpeech verifies that what the messages pane
// shows for a pilot transmission is what the pilot says: the same choice of
// phrasing, with the callsign, for both a readback and a call the pilot
// starts.
func TestTransmissionTextMatchesSpeech(t *testing.T) {
	s := NewTestSim(testLogger())
	s.State.Controllers = map[ControlPosition]*av.Controller{"125.0": {RadioName: "Test Approach"}}
	ac := MakeTestAircraft("AAL123", "22L")
	s.Aircraft[ac.ADSBCallsign] = ac

	phrasings := []string{"what altitude should we maintain", "what altitude do you want us at"}
	check := func(what, written, spoken string) {
		t.Helper()
		if !strings.Contains(written, "American 123") {
			t.Errorf("%s text %q is missing the callsign", what, written)
		}
		for _, p := range phrasings {
			if strings.Contains(written, p) != strings.Contains(spoken, p) {
				t.Errorf("%s text %q and speech %q differ on %q", what, written, spoken, p)
			}
		}
	}
	writtenEvent := func(sub *EventsSubscription) string {
		t.Helper()
		events := sub.Get()
		i := slices.IndexFunc(events, func(e Event) bool { return e.Type == RadioTransmissionEvent })
		if i == -1 {
			t.Fatal("no transmission posted")
		}
		return events[i].WrittenText
	}

	for range 20 {
		sub := s.eventStream.Subscribe()
		spoken := s.postReadbackTransmission(ac.ADSBCallsign,
			speech.MakeReadbackTransmission("["+strings.Join(phrasings, "|")+"]"), E2ETCW())
		check("readback", writtenEvent(sub), spoken)
		sub.Unsubscribe()

		s.enqueuePilotTransmission(ac.ADSBCallsign, "125.0", PendingTransmissionRequestAltitude)
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		pt := s.NextPilotTransmission(E2ETCW())
		if pt == nil {
			t.Fatal("no pilot transmission published")
		}
		if !strings.HasPrefix(pt.Written, "Test Approach") {
			t.Errorf("call text %q doesn't start with the controller's name", pt.Written)
		}
		check("call", pt.Written, pt.Spoken)

		sub = s.eventStream.Subscribe()
		s.ReportPilotTransmission(E2ETCW(), *pt)
		if written := writtenEvent(sub); written != pt.Written {
			t.Errorf("posted %q for the call, but the client said %q", written, pt.Written)
		}
		sub.Unsubscribe()
	}
}

// TestPilotTransmissionLifecycle verifies that the next pilot transmission is
// published with the same phrasing until it is reported, that reporting it
// takes it out of the queue and moves on to the next one, and that a report
// of a transmission no longer queued changes nothing.
func TestPilotTransmissionLifecycle(t *testing.T) {
	s := NewTestSim(testLogger())
	tcp := TCP("125.0")
	addTunedAircraft(s, "AAL123", tcp)
	addTunedAircraft(s, "UAL456", tcp)

	s.enqueuePilotTransmission("AAL123", tcp, PendingTransmissionTrafficInSight)
	s.enqueuePilotTransmission("UAL456", tcp, PendingTransmissionTrafficInSight)
	s.State.SimTime = s.State.SimTime.Add(time.Second)

	first := s.NextPilotTransmission(E2ETCW())
	if first == nil || first.ADSBCallsign != "AAL123" {
		t.Fatalf("expected AAL123's call first, got %+v", first)
	}
	for range 10 {
		if again := s.NextPilotTransmission(E2ETCW()); again == nil || *again != *first {
			t.Fatalf("republished call changed from %+v to %+v", first, again)
		}
	}
	if !s.PilotTransmissionIsNext(E2ETCW(), first.ContactID) {
		t.Error("expected the published call to be next")
	}

	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()
	s.ReportPilotTransmission(E2ETCW(), *first)
	if s.PilotTransmissionIsNext(E2ETCW(), first.ContactID) {
		t.Error("expected the reported call not to be next any more")
	}
	if next := s.NextPilotTransmission(E2ETCW()); next == nil || next.ADSBCallsign != "UAL456" {
		t.Errorf("expected UAL456's call next, got %+v", next)
	}

	// A second report, as from another connection at the TCW, is ignored.
	s.ReportPilotTransmission(E2ETCW(), *first)
	n := len(slices.DeleteFunc(sub.Get(), func(e Event) bool { return e.Type != RadioTransmissionEvent }))
	if n != 1 {
		t.Errorf("reporting the call twice posted %d transmissions, want 1", n)
	}
	if len(s.PendingContacts[tcp]) != 1 {
		t.Errorf("expected UAL456's call to stay queued, have %d contacts", len(s.PendingContacts[tcp]))
	}
}

// TestPublishingPilotTransmissionsLeavesSimRandAlone verifies, for every type
// of pilot transmission, that publishing it, which happens for every state
// update and isn't recorded in the session log, draws nothing from the sim's
// random numbers and words it the same way each time.
func TestPublishingPilotTransmissionsLeavesSimRandAlone(t *testing.T) {
	sighted := func(ac *Aircraft) {
		ac.SightedReportingPoint = &av.ReportingPoint{Names: []string{"Dumbarton bridge", "bridge"}}
	}
	setup := map[PendingTransmissionType]func(*Aircraft){
		PendingTransmissionRequestApproachClearance: func(ac *Aircraft) {
			ac.Nav.Approach.InterceptState = nav.OnApproachCourse
		},
		PendingTransmissionRequestTowerSwitch:               func(ac *Aircraft) { ac.Nav.Approach.Cleared = true },
		PendingTransmissionReportingPointInSight:            sighted,
		PendingTransmissionSpontaneousReportingPointInSight: sighted,
	}

	for ty := PendingTransmissionDeparture; ty <= PendingTransmissionSpontaneousReportingPointInSight; ty++ {
		s := NewTestSim(testLogger())
		ac := MakeTestAircraft("AAL123", "22L")
		ac.DepartureAirport = "KJFK"
		ac.AssociateFlightPlan(&FlightPlan{ACID: ACID(ac.ADSBCallsign)})
		if f := setup[ty]; f != nil {
			f(ac)
		}
		s.Aircraft[ac.ADSBCallsign] = ac
		if ty == PendingTransmissionEmergency {
			s.enqueueEmergencyTransmission(ac.ADSBCallsign, "125.0",
				speech.MakeContactTransmission("[declaring an emergency|mayday mayday mayday]"))
		} else {
			s.enqueuePilotTransmission(ac.ADSBCallsign, "125.0", ty)
		}
		s.State.SimTime = s.State.SimTime.Add(time.Second)

		before, err := json.Marshal(s.Rand)
		if err != nil {
			t.Fatal(err)
		}
		first := s.NextPilotTransmission(E2ETCW())
		if first == nil {
			t.Errorf("type %d: nothing published", ty)
			continue
		}
		for range 20 {
			if pt := s.NextPilotTransmission(E2ETCW()); pt == nil || *pt != *first {
				t.Errorf("type %d: published %+v, then %+v", ty, first, pt)
				break
			}
		}
		if after, err := json.Marshal(s.Rand); err != nil || !bytes.Equal(before, after) {
			t.Errorf("type %d: publishing drew from the sim's random numbers", ty)
		}
	}
}
