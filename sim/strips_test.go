// sim/strips_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

// newStripTestSim returns a sim with human positions 2A and 2B, signed in at
// TCWs A and B, and virtual positions 14 and 15.
func newStripTestSim() *Sim {
	s := NewTestSim(testLogger())
	s.STARSComputer = makeSTARSComputer("TEST")
	s.ScenarioDefaultConsolidation = PositionConsolidation{"2A": {"2B"}}
	s.State.CurrentConsolidation = map[TCW]*TCPConsolidation{"A": {PrimaryTCP: "2A"}, "B": {PrimaryTCP: "2B"}}
	s.ControlPositions = map[TCP]*av.Controller{}
	s.State.Controllers = map[ControlPosition]*av.Controller{}
	for _, tcp := range []TCP{"2A", "2B", "14", "15"} {
		s.ControlPositions[tcp] = &av.Controller{Position: string(tcp)}
		s.State.Controllers[tcp] = &av.Controller{Position: string(tcp)}
	}
	return s
}

// addStripTestDeparture adds an IFR departure from KSMF whose track owner is
// on frequency with it.
func addStripTestDeparture(t *testing.T, s *Sim, owner TCP) (*Aircraft, *FlightPlan) {
	t.Helper()

	ac := MakeTestAircraft("AAL123", "13L")
	ac.TypeOfFlight = av.FlightTypeDeparture
	ac.DepartureAirport = "KSMF"
	ac.ControllerFrequency = owner
	s.Aircraft[ac.ADSBCallsign] = ac

	acid := ACID(ac.ADSBCallsign)
	if _, err := s.STARSComputer.CreateFlightPlan(FlightPlan{
		ACID:               acid,
		Rules:              av.FlightRulesIFR,
		TypeOfFlight:       av.FlightTypeDeparture,
		DepartureAirport:   "KSMF",
		TrackingController: owner,
		OwningTCW:          s.tcwForPosition(owner),
	}); err != nil {
		t.Fatalf("CreateFlightPlan: %v", err)
	}
	fp := s.STARSComputer.takeFlightPlanByACID(acid)
	ac.AssociateFlightPlan(fp)
	return ac, fp
}

// acceptVirtualHandoffs runs the sim far enough for virtual controllers to
// accept the handoffs offered to them.
func acceptVirtualHandoffs(s *Sim) {
	s.State.SimTime = s.State.SimTime.Add(time.Minute)
	s.lastSimUpdate = s.State.SimTime // skip the once-a-second aircraft update
	s.updateState()
}

// A departure that goes through a second virtual controller before reaching
// the human gets its strip when the second one hands it off.
func TestStripPushedOnVirtualHandoff(t *testing.T) {
	s := newStripTestSim()
	ac, fp := addStripTestDeparture(t, s, "14")

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{HandoffController: "15"}})
	if fp.StripOwner != "" {
		t.Fatalf("a handoff between virtual controllers gave the strip to %q", fp.StripOwner)
	}
	acceptVirtualHandoffs(s)
	if fp.TrackingController != "15" {
		t.Fatalf("15 didn't accept the handoff; %q owns the track", fp.TrackingController)
	}

	s.applyWaypointActionEvent(ac, av.WaypointActionEvent{Actions: av.WaypointActions{HandoffController: "2A"}})
	if fp.StripOwner != "2A" {
		t.Errorf("the handoff to 2A gave the strip to %q", fp.StripOwner)
	}
	if !slices.Contains(s.flightStripACIDsForTCW("A"), fp.ACID) {
		t.Errorf("A's strips %v don't include %s", s.flightStripACIDsForTCW("A"), fp.ACID)
	}
}

// Only human positions get strips: a strip anywhere else is never seen, and
// its CID is never freed.
func TestStripOnlyForHumanPositions(t *testing.T) {
	s := newStripTestSim()
	_, fp := addStripTestDeparture(t, s, "14")

	for _, tcp := range []TCP{"", "14"} {
		s.giveFlightStrip(fp, tcp)
		if fp.StripOwner != "" {
			t.Errorf("giving the strip to %q printed one at %q", tcp, fp.StripOwner)
		}
	}
}

func TestNoStripWhenAirportDoesNotPrintThem(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name         string
		airport      av.Airport
		typeOfFlight av.TypeOfFlight
		arrival      av.ICAOAirportCode
	}{
		{name: "departure", airport: av.Airport{PrintDepartureStrips: &off},
			typeOfFlight: av.FlightTypeDeparture, arrival: "KSFO"},
		// A departure to a nearby airport may be handled as an arrival.
		{name: "local arrival", airport: av.Airport{PrintDepartureStrips: &off},
			typeOfFlight: av.FlightTypeArrival, arrival: "KSFO"},
		{name: "arrival", airport: av.Airport{PrintArrivalStrips: &off},
			typeOfFlight: av.FlightTypeArrival, arrival: "KSMF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStripTestSim()
			s.State.Airports["KSMF"] = &tc.airport
			_, fp := addStripTestDeparture(t, s, "14")
			fp.TypeOfFlight, fp.ArrivalAirport = tc.typeOfFlight, tc.arrival

			s.giveFlightStrip(fp, "2A")
			if fp.StripOwner != "" {
				t.Errorf("KSMF doesn't print these strips, but %q got one", fp.StripOwner)
			}
		})
	}
}

// A human's handoff leaves the strip where it is until the controller pushes
// it or switches the aircraft to the new controller.
func TestStripGoesAlongWithFrequencyChange(t *testing.T) {
	s := newStripTestSim()
	_, fp := addStripTestDeparture(t, s, "2A")
	s.giveFlightStrip(fp, "2A")

	if err := s.HandoffTrack("A", fp.ACID, "2B"); err != nil {
		t.Fatalf("HandoffTrack: %v", err)
	}
	if err := s.AcceptHandoff("B", fp.ACID); err != nil {
		t.Fatalf("AcceptHandoff: %v", err)
	}
	if fp.StripOwner != "2A" {
		t.Fatalf("the strip moved to %q before 2A pushed it", fp.StripOwner)
	}

	if _, err := s.ContactTrackingController("A", fp.ACID, 0); err != nil {
		t.Fatalf("ContactTrackingController: %v", err)
	}
	if fp.StripOwner != "2B" {
		t.Errorf("the frequency change left the strip at %q", fp.StripOwner)
	}
}

func TestFrequencyChangeDoesNotPrintStrip(t *testing.T) {
	s := newStripTestSim()
	_, fp := addStripTestDeparture(t, s, "2A")

	if _, err := s.ContactController("A", fp.ACID, "2B", 0); err != nil {
		t.Fatalf("ContactController: %v", err)
	}
	if fp.StripOwner != "" {
		t.Errorf("a flight without a strip got one at %q", fp.StripOwner)
	}
}

func TestDepartureStripPrintedAtTakeoffRoll(t *testing.T) {
	installIntersectingRunwayFixture(t)

	now := NewSimTime(time.Now())
	s, rwy9, _ := departureQueueSim(now)
	s.ScenarioDefaultConsolidation = PositionConsolidation{"125.0": nil}

	rwy9.ReleasedIFR = []DepartureAircraft{stageDeparture(s, "BAR1", "BAR", now)}
	if _, err := s.STARSComputer.CreateFlightPlan(FlightPlan{
		ACID:               "BAR1",
		Rules:              av.FlightRulesIFR,
		TypeOfFlight:       av.FlightTypeDeparture,
		DepartureAirport:   "XTST",
		TrackingController: "125.0",
	}); err != nil {
		t.Fatalf("CreateFlightPlan: %v", err)
	}
	fp := s.STARSComputer.lookupFlightPlanByACID("BAR1")
	if fp.StripOwner != "" {
		t.Fatalf("the strip went to %q before the takeoff roll", fp.StripOwner)
	}

	s.launchNextDeparture(rwy9, "XTST", "9", now)
	if fp.StripOwner != "125.0" {
		t.Errorf("at the takeoff roll, the strip went to %q", fp.StripOwner)
	}
}
