package main 

type ERAMComputer struct {
	STARSComputers map[string]STARSComputer
	FlightPlans map[string]FlightPlan
}

type STARSComputer struct {
	RecievedPlans []SentFlightPlan
	ContainedPlans map[string]FlightPlan
}

// Different flight plans
const (
	RemoteEnroute = iota // Flight plan received from a NAS ARTCC
	/* This is essentially a flight plan that has been sent over by an overlying ERAM facility. */
	RemoteNonEnroute // Flight plan received from an adjacent terminal facility
	/* This is a flight plan that has been sent over by another STARS facility. */
	LocalEnroute // VFR interfacility flight plan entered locally for which the NAS ARTCC has not returned a flight plan
	/* This is a flight plan that is made by a STARS facility that gets a NAS code.*/
	LocalNonEnroute // Flight plan entered by TCW or flight plan from an adjacent terminal that has been handed off to this STARS facility
	/*This is a flight plan that is made at a STARS facility and gets a local code.*/
)

type STARSFlightPlan struct {
	FlightPlan
	FlightplanType int
}

type FlightPlanMessage struct {
	MessageType int
	FlightPlan FlightPlan // For IFR, VFR, Amendment, and Cancellation flight plans

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

func (comp *ERAMComputer) MessageToSTARSFacility(facility string, msg FlightPlanMessage) error {
	// fullFacility, ok := comp.FlightPlans[facility]
	// if !ok {
	// 	return ErrNoSTARSFacility
	// }


	// switch msg.MessageType {
	// case IFRFlightPlan:

	// case VFRFlightPlan:
	// }

	return nil
}
