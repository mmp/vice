package main 

import "time"

type ERAMComputer struct {
	STARSComputers map[string]STARSComputer
	FlightPlans map[string]FlightPlan
}

type STARSComputer struct {
	RecievedPlans []FlightPlanMessage
	ContainedPlans map[string]STARSFlightPlan
}

type STARSFlightPlan struct {
	FlightPlan
	FlightPlanType int 
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
	SourceID string // LLLdddd e.g. ZCN2034 (ZNY at 2034z)
	MessageType int
	FlightID string // ddaLa(a)(a)(a)(a)(a)ECID (3 chars start w/ digit), Aircraft ID (2-7 chars start with letter)
	AircraftData AircraftDataMessage
	BCN Squawk
	CoordinationFix string 
	CoordinationTime time.Time
	Altitude int // Requested/ assigned
	Route string 
}

type AircraftDataMessage struct {
	DepartureLocation string 
	NumberOfAircraft int 
	AircraftType string 
	Equipment string 
}

// Converts the message to a flight plan.
func (s FlightPlanMessage) FlightPlan() STARSFlightPlan {
	flightPlan := STARSFlightPlan{}
	if s.MessageType == IFRFlightPlan {
		flightPlan.Rules = IFR
	} else if s.MessageType == VFRFlightPlan {
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


// Message types sent from either ERAM or STARS(?)
const (
	IFRFlightPlan = iota
	VFRFlightPlan
	Amendment
	Cancellation
	RequestFlightPlan
	DepartureType 
	BeaconTerminate

	/* TODO:
	Track Data
	Test
	Response
	*/
)

// func (comp *ERAMComputer) MessageToSTARSFacility(facility string, msg FlightPlanMessage) error {
// 	STARSFacility, ok := comp.FlightPlans[facility]
// 	if !ok {
// 		return ErrNoSTARSFacility
// 	}

// 	switch msg.MessageType {
// 	case IFRFlightPlan:

// 	}

// 	return nil
// }
