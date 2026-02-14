// sim/nas_messages_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

func makeTestLogger() *slog.Logger {
	return slog.Default()
}

func makeTestNetwork() (*NASNetwork, *STARSComputer, *ERAMComputer) {
	net := NewNASNetwork()

	eram := makeERAMComputer("ZNY", nil)
	net.ERAMComputers["ZNY"] = eram

	stars := makeSTARSComputer("N90")
	stars.ParentERAM = eram
	eram.Children["N90"] = stars
	net.STARSComputers["N90"] = stars
	net.parentERAM["N90"] = "ZNY"

	return net, stars, eram
}

func makeTestNetworkWithNeighbor() (*NASNetwork, *STARSComputer, *STARSComputer, *ERAMComputer) {
	net, stars, eram := makeTestNetwork()

	// Add neighbor TRACON
	neighbor := makeSTARSComputer("PHL")
	neighbor.ParentERAM = eram
	eram.Children["PHL"] = neighbor
	net.STARSComputers["PHL"] = neighbor
	net.parentERAM["PHL"] = "ZNY"

	return net, stars, neighbor, eram
}

// TestTIBeforeFP_RejectsDR verifies that a TI arriving at a STARS computer
// that has no flight plan for the ACID results in a DR (rejection).
func TestTIBeforeFP_RejectsDR(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	// Send TI to STARS without any FP present
	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTI,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "AAL123",
		Timestamp:  now,
		Controller: "EWR_P_APP",
		EntryFix:   "COATE",
		ExitFix:    "KEWR",
		Altitude:   10000,
	})

	stars.ProcessInbox(net, now, lg)

	// Check that ERAM received a DR
	if len(stars.ParentERAM.Inbox) == 0 {
		t.Fatal("expected DR message in ERAM inbox, got nothing")
	}

	// The STARS sends to parent ERAM which routes to destination.
	// Since the sender is ZNY (an ERAM), the DR should be routed to ZNY.
	foundDR := false
	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgDR && msg.ACID == "AAL123" {
			if msg.Reject != RejectNoFlightPlan {
				t.Errorf("expected RejectNoFlightPlan, got %d", msg.Reject)
			}
			foundDR = true
		}
	}
	if !foundDR {
		t.Error("expected DR message with RejectNoFlightPlan")
	}
}

// TestTIAfterFP_SendsDA verifies that when a FP exists at the STARS computer,
// a TI results in a DA (acceptance) with the resolved controller.
func TestTIAfterFP_SendsDA(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	// Set up fix pairs for controller resolution
	stars.FixPairs = []FixPairDefinition{
		{EntryFix: "COATE", ExitFix: "KEWR", FlightType: "A", Priority: 1},
	}
	stars.FixPairAssignments = []FixPairAssignment{
		{FixPairIndex: 0, TCP: "EWR_S_APP", Priority: 1},
	}

	// First, add a FP to the STARS computer
	stars.FlightPlans["AAL123"] = &NASFlightPlan{
		ACID:            "AAL123",
		AircraftType:    "B738",
		AssignedSquawk:  av.Squawk(0o4521),
		TypeOfFlight:    av.FlightTypeArrival,
		ArrivalAirport:  "KEWR",
		AssignedAltitude: 10000,
	}

	// Now send TI
	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTI,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "AAL123",
		Timestamp:  now,
		Controller: "", // Let fix pairs resolve
		EntryFix:   "COATE",
		ExitFix:    "KEWR",
		Altitude:   10000,
	})

	stars.ProcessInbox(net, now, lg)

	// Check that a DA was sent (to parent ERAM since STARS routes through parent)
	foundDA := false
	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgDA && msg.ACID == "AAL123" {
			if msg.Controller != "EWR_S_APP" {
				t.Errorf("expected resolved controller EWR_S_APP, got %q", msg.Controller)
			}
			foundDA = true
		}
	}
	if !foundDA {
		t.Error("expected DA message with resolved controller")
	}

	// Check that a FacilityTrack was created
	track, ok := stars.Tracks["AAL123"]
	if !ok {
		t.Fatal("expected FacilityTrack to be created for AAL123")
	}
	if track.HandoffState != TrackHandoffOffered {
		t.Errorf("expected HandoffState Offered, got %d", track.HandoffState)
	}
	if track.Owner != "EWR_S_APP" {
		t.Errorf("expected Owner EWR_S_APP, got %q", track.Owner)
	}
}

// TestNoFixPairMatch_FallsBackToInboundHandoff verifies that when no fix pair
// matches, the controller falls back to InboundHandoffController from the FP.
func TestNoFixPairMatch_FallsBackToInboundHandoff(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	// No fix pairs configured
	stars.FixPairs = nil
	stars.FixPairAssignments = nil

	// FP with InboundHandoffController set
	stars.FlightPlans["DAL456"] = &NASFlightPlan{
		ACID:                    "DAL456",
		AircraftType:            "A320",
		AssignedSquawk:          av.Squawk(0o4522),
		TypeOfFlight:            av.FlightTypeArrival,
		InboundHandoffController: "JFK_A_APP",
	}

	// Send TI
	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTI,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "DAL456",
		Timestamp:  now,
		EntryFix:   "CAMRN",
		ExitFix:    "KJFK",
	})

	stars.ProcessInbox(net, now, lg)

	// Should get DA with fallback controller
	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgDA && msg.ACID == "DAL456" {
			if msg.Controller != "JFK_A_APP" {
				t.Errorf("expected fallback controller JFK_A_APP, got %q", msg.Controller)
			}
			return
		}
	}
	t.Error("expected DA message")
}

// TestTBCleansUpFP verifies that a TB message removes the flight plan
// and track from the receiver.
func TestTBCleansUpFP(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	// Add FP and track
	stars.FlightPlans["UAL789"] = &NASFlightPlan{
		ACID:       "UAL789",
		ListIndex:  5,
	}
	stars.Tracks["UAL789"] = &FacilityTrack{
		ACID: "UAL789",
	}

	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTB,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "UAL789",
		Timestamp:  now,
	})

	stars.ProcessInbox(net, now, lg)

	if _, exists := stars.FlightPlans["UAL789"]; exists {
		t.Error("expected FP to be removed after TB")
	}
	if _, exists := stars.Tracks["UAL789"]; exists {
		t.Error("expected track to be removed after TB")
	}
}

// TestCXCleansUpFP verifies that a CX (cancellation) message removes
// the flight plan from the receiver.
func TestCXCleansUpFP(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	stars.FlightPlans["SWA101"] = &NASFlightPlan{
		ACID:      "SWA101",
		ListIndex: 3,
	}

	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgCX,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "SWA101",
		Timestamp:  now,
	})

	stars.ProcessInbox(net, now, lg)

	if _, exists := stars.FlightPlans["SWA101"]; exists {
		t.Error("expected FP to be removed after CX")
	}
}

// TestRFReturnsFP verifies that an RF (request flight plan) message causes
// the receiver to send the FP back.
func TestRFReturnsFP(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	fp := &NASFlightPlan{
		ACID:         "JBU202",
		AircraftType: "A321",
		AssignedSquawk: av.Squawk(0o4523),
	}
	stars.FlightPlans["JBU202"] = fp

	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgRF,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "JBU202",
		Timestamp:  now,
	})

	stars.ProcessInbox(net, now, lg)

	// STARS routes through parent ERAM, so the FP reply goes there
	foundFP := false
	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgFP && msg.ACID == "JBU202" && msg.FlightPlan != nil {
			foundFP = true
		}
	}
	if !foundFP {
		t.Error("expected FP message in response to RF")
	}
}

// TestSTARSRoutesToDestination verifies that a STARS computer routes
// messages directly to the destination facility via the network layer.
func TestSTARSRoutesToDestination(t *testing.T) {
	net, stars, neighbor, _ := makeTestNetworkWithNeighbor()
	_ = makeTestLogger()
	now := time.Now()

	// Send from N90 to PHL
	stars.SendMessage(net, NASMessage{
		Type:       MsgFP,
		ToFacility: "PHL",
		ACID:       "AAL999",
		Timestamp:  now,
	})

	// Message should go directly to PHL
	if len(neighbor.Inbox) != 1 {
		t.Fatalf("expected 1 message in PHL inbox, got %d", len(neighbor.Inbox))
	}
	if neighbor.Inbox[0].FromFacility != "N90" {
		t.Errorf("expected FromFacility N90, got %q", neighbor.Inbox[0].FromFacility)
	}
}

// TestERAMRelaysCorrectly verifies that ERAM correctly routes
// messages to its children.
func TestERAMRelaysCorrectly(t *testing.T) {
	net, _, neighbor, eram := makeTestNetworkWithNeighbor()
	lg := makeTestLogger()
	now := time.Now()

	fp := &NASFlightPlan{
		ACID:         "EGF303",
		AircraftType: "C172",
	}

	// Send FP from ERAM to PHL
	eram.SendMessage(net, NASMessage{
		Type:       MsgFP,
		ToFacility: "PHL",
		ACID:       "EGF303",
		Timestamp:  now,
		FlightPlan: fp,
	})

	if len(neighbor.Inbox) != 1 {
		t.Fatalf("expected 1 message in PHL inbox, got %d", len(neighbor.Inbox))
	}

	neighbor.ProcessInbox(net, now, lg)

	if _, exists := neighbor.FlightPlans["EGF303"]; !exists {
		t.Error("expected FP to be stored at PHL after processing")
	}
}

// TestFixPairAltitudeUsesAssigned verifies that fix pair matching
// uses the assigned altitude from the flight plan.
func TestFixPairAltitudeUsesAssigned(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	stars.FixPairs = []FixPairDefinition{
		{EntryFix: "COATE", ExitFix: "KEWR", FlightType: "A", AltitudeRange: [2]int{8000, 12000}, Priority: 1},
		{EntryFix: "COATE", ExitFix: "KEWR", FlightType: "A", Priority: 99}, // Fallback (no altitude constraint)
	}
	stars.FixPairAssignments = []FixPairAssignment{
		{FixPairIndex: 0, TCP: "EWR_S_APP", Priority: 1},
		{FixPairIndex: 1, TCP: "EWR_P_APP", Priority: 99},
	}

	// FP with altitude in range
	stars.FlightPlans["AAL111"] = &NASFlightPlan{
		ACID:            "AAL111",
		TypeOfFlight:    av.FlightTypeArrival,
		AssignedAltitude: 10000,
	}

	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTI,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "AAL111",
		Timestamp:  now,
		EntryFix:   "COATE",
		ExitFix:    "KEWR",
		Altitude:   10000,
	})

	stars.ProcessInbox(net, now, lg)

	// Should match the altitude-constrained pair
	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgDA && msg.ACID == "AAL111" {
			if msg.Controller != "EWR_S_APP" {
				t.Errorf("expected EWR_S_APP (altitude in range), got %q", msg.Controller)
			}
			return
		}
	}
	t.Error("expected DA message")
}

// TestGeographicDepartureType verifies that when the entry fix matches
// a TRACON airport, the flight type is treated as Departure.
func TestGeographicDepartureType(t *testing.T) {
	net, stars, _ := makeTestNetwork()
	lg := makeTestLogger()
	now := time.Now()

	// Set TRACON airports
	stars.Airports = []string{"KEWR", "KJFK", "KLGA"}

	stars.FixPairs = []FixPairDefinition{
		{EntryFix: "KEWR", FlightType: "P", Priority: 1}, // P = departure
		{EntryFix: "KEWR", FlightType: "A", Priority: 2}, // A = arrival
	}
	stars.FixPairAssignments = []FixPairAssignment{
		{FixPairIndex: 0, TCP: "EWR_DEP", Priority: 1},
		{FixPairIndex: 1, TCP: "EWR_P_APP", Priority: 2},
	}

	// FP where EntryFix is an airport in this TRACON (geographic departure)
	stars.FlightPlans["UAL555"] = &NASFlightPlan{
		ACID:         "UAL555",
		EntryFix:     "KEWR", // Departure airport stored as entry fix
		TypeOfFlight: av.FlightTypeArrival, // Original type - should be overridden
	}

	stars.Inbox = append(stars.Inbox, NASMessage{
		Type:       MsgTI,
		FromFacility: "ZNY",
		ToFacility: "N90",
		ACID:       "UAL555",
		Timestamp:  now,
		EntryFix:   "KEWR",
	})

	stars.ProcessInbox(net, now, lg)

	for _, msg := range stars.ParentERAM.Inbox {
		if msg.Type == MsgDA && msg.ACID == "UAL555" {
			if msg.Controller != "EWR_DEP" {
				t.Errorf("expected EWR_DEP (geographic departure), got %q", msg.Controller)
			}
			return
		}
	}
	t.Error("expected DA message")
}
