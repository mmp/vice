package main

import (
	"strings"
	"time"
)

// Message types sent from either ERAM or STARS
const (
	Plan              = iota // Both STARS & ERAM send this. ERAM only receives VFR plans/
	Amendment                // ERAM
	Cancellation             // ERAM
	RequestFlightPlan        // STARS
	DepartureDM              // STARS
	BeaconTerminate          // STARS

	/* TODO:
	Track Data
	Test
	Response
	*/
)

type ERAMComputer struct {
	STARSComputers map[string]*STARSComputer
	FlightPlans    map[Squawk]*FlightPlan
}

// Sends a message, whether that be a flight plan or any other message type to a STARS computer.
// The STARS computer will sort messages by itself
func (comp *ERAMComputer) ToSTARSFacility(facility string, msg FlightPlanMessage) error {
	STARSFacility, ok := comp.STARSComputers[facility]
	if !ok {
		return ErrNoSTARSFacility
	}
	STARSFacility.RecievedMessages = append(STARSFacility.RecievedMessages, msg)
	return nil
}

type STARSComputer struct {
	RecievedMessages []FlightPlanMessage
	ContainedPlans   map[Squawk]STARSFlightPlan
}

type STARSFlightPlan struct {
	FlightPlan
	FlightPlanType   int
	CoordinationTime time.Time
	CoordinationFix  string
}

// Different flight plans (STARS)
const (
	RemoteEnroute = iota // Flight plan received from a NAS ARTCC
	// This is a flight plan that has been sent over by an overlying ERAM facility.

	RemoteNonEnroute // Flight plan received from an adjacent terminal facility
	// This is a flight plan that has been sent over by another STARS facility.

	LocalEnroute // VFR interfacility flight plan entered locally for which the NAS ARTCC has not returned a flight plan
	// This is a flight plan that is made by a STARS facility that gets a NAS code.

	LocalNonEnroute // Flight plan entered by TCW or flight plan from an adjacent terminal that has been handed off to this STARS facility
	// This is a flight plan that is made at a STARS facility and gets a local code.
)

type FlightPlanMessage struct {
	SourceID         string // LLLdddd e.g. ZCN2034 (ZNY at 2034z)
	MessageType      int
	FlightID         string // ddaLa(a)(a)(a)(a)(a)ECID (3 chars start w/ digit), Aircraft ID (2-7 chars start with letter)
	AircraftData     AircraftDataMessage
	BCN              Squawk
	CoordinationFix  string
	CoordinationTime time.Time

	// Altitude will either be requested (cruise altitude) for departures, or the assigned altitude for arrivals.
	// ERAM has the ability to assign interm alts (and is used much more than STARS interm alts) with `QQ`.
	// This interim altiude gets sent down to the STARS computer instead of the cruising altitude. If no interim
	// altitude is set, use the cruise altitude (check this)
	// Examples of altitudes could be 310, VFR/170, VFR, 170B210 (block altitude), etc.
	Altitude string
	Route    string
}

type AircraftDataMessage struct {
	DepartureLocation string // Only for departures.
	NumberOfAircraft  int    // Default this at one for now.
	AircraftType      string // A20N, B737, etc.

	// V = VFR (not heavy jet),
	// H = Heavy,
	// W = Heavy + VFR,
	// U = Heavy + OTP.
	AircraftCategory string
	Equipment        string // /L, /G, /A, etc
}

// Converts the message to a STARS flight plan.
func (s FlightPlanMessage) FlightPlan() STARSFlightPlan {
	flightPlan := STARSFlightPlan{}
	if !strings.Contains(s.Altitude, "VFR") {
		flightPlan.Rules = IFR
	} else {
		flightPlan.Rules = VFR
	}
	flightPlan.AircraftType = s.AircraftData.AircraftType
	flightPlan.AssignedSquawk = s.BCN
	flightPlan.DepartureAirport = s.AircraftData.DepartureLocation
	flightPlan.Route = s.Route
	flightPlan.CoordinationFix = s.CoordinationFix
	flightPlan.CoordinationTime = s.CoordinationTime
	/* TODO:
	- Cruising altitude
	- Arrival Airport
	*/

	return flightPlan
}

// Sorting the STARS messages. This will store flight plans with FP messages, change flight plans with AM messages,
// cancel flight plans with CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages() {
	for len(comp.RecievedMessages) != 0 {
		msg := comp.RecievedMessages[0]
		switch msg.MessageType {
		case Plan:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			comp.RecievedMessages = comp.RecievedMessages[1:]
		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			comp.RecievedMessages = comp.RecievedMessages[1:]
		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)
		}
	}
}
