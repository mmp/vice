package main

import (
	"fmt"
	"strings"
	"time"
)

func (w *World) initComputers() {
	w.ERAMComputers = make(map[string]*ERAMComputer)
	for fac := range database.ARTCCs {
		w.ERAMComputers[fac] = &ERAMComputer{
			Identifier:       fac,
			ReceivedMessages: &[]FlightPlanMessage{},
		}
		w.ERAMComputers[fac].STARSComputers = make(map[string]*STARSComputer)

		for name, stars := range database.TRACONs {
			if stars.ARTCC == fac { // if the artcc of the tracon is the same
				w.ERAMComputers[fac].STARSComputers[name] = &STARSComputer{ // make a news stars comp for this new fac (with var name)
					Identifier: name,
				}
				w.ERAMComputers[fac].STARSComputers[name].ERAMInbox = w.ERAMComputers[fac].ReceivedMessages // make the eram inbox
			}
		}
	}
	_, stars := w.SafeFacility("")
	if stars.STARSInbox == nil {
		stars.STARSInbox = make(map[string]*[]FlightPlanMessage)
	}
	for _, fac := range w.STARSFacilityAdaptation.ExternalFacilities {
		_, outerStars := w.SafeFacility(fac)

		stars.STARSInbox[outerStars.Identifier] = &outerStars.RecievedMessages
	}

}

// Access their ERAM and STARS computers. Leave blank for own TRACON
// TODO: Change the error stuff to return an err value
func (w *World) SafeFacility(inputTracon string) (*ERAMComputer, *STARSComputer) {
	if inputTracon == "" {
		inputTracon = w.TRACON
	}
	tracon, ok := database.TRACONs[inputTracon]
	if !ok {
		lg.Errorf("TRACON %s not found: %v", inputTracon, database.TRACONs)
	}

	artcc, ok := w.ERAMComputers[tracon.ARTCC]
	if !ok {
		w.initComputers()
		artcc, ok = w.ERAMComputers[tracon.ARTCC]
		if !ok {
			lg.Errorf("ARTCC %s still not found after initialization. TRACON: %v", tracon.ARTCC, tracon)
		}
	}
	fac, ok := artcc.STARSComputers[inputTracon]
	if !ok {
		w.initComputers()
		fac, ok = artcc.STARSComputers[inputTracon]
		if !ok {
			lg.Errorf("STARSComputer for TRACON %s still not found after initialization\n", w.TRACON)
		}
	}
	return artcc, fac
}

func (w *World) FacilityFromController(callsign string) string {
	controller, ok := w.Controllers[callsign]
	if ok {
		return controller.Facility
	}
	lg.Errorf("Couldn't find facility for %v.\n", callsign)
	return ""
}

// Give the computers a chance to sort through their received messages. Messages will send when the time is appropriate (eg. handoff).
// Some messages will be sent from recieved messages (for example a FP message from a RF message).
func (w *World) UpdateComputers(simTime time.Time) {
	// _, fac := w.SafeFacility("")
	// Sort through messages made
	for _, comp := range w.ERAMComputers {
		comp.SortMessages(simTime)
		for _, stars := range comp.STARSComputers {
			stars.SortReceivedMessages()
		}
	}
}

// Message types sent from either ERAM or STARS
const (
	Plan              = iota // Both STARS & ERAM send this.
	Amendment                // ERAM (STARS?)
	Cancellation             // ERAM (STARS?)
	RequestFlightPlan        // STARS
	DepartureDM              // STARS
	BeaconTerminate          // STARS

	// Track Data

	InitateTransfer      // When handoff gets sent. Sends the flightplan, contains track location
	AcceptRecallTransfer // Accept/ recall handoff
	TrackUpdate
	// updated track coordinates. If off by some amount that is unaccepable, you'd see "AMB" in STARS datatag.
	// If no target is even close with same beacon code on the receiving STARS system, you'd see "NAT".

	/* TODO:
	Track Data
	Test
	Response
	*/
)

type ERAMComputer struct {
	STARSComputers   map[string]*STARSComputer
	ReceivedMessages *[]FlightPlanMessage
	FlightPlans      map[Squawk]*FlightPlan
	Identifier       string
}

// func (w *World) GatherFilledPlans() {
// 	for _, plan := range w.NonSortedPlans {
// 		eram := w.ERAMComputers[database.Airports[plan.DepartureAirport].ARTCC]
// 		eram.FlightPlans[plan.AssignedSquawk] = plan
// 		w.NonSortedPlans = w.NonSortedPlans[1:]
// 	}
// }

func (comp *ERAMComputer) SortMessages(simTime time.Time) {
	if comp.ReceivedMessages == nil {
		comp.ReceivedMessages = &[]FlightPlanMessage{}
	}
	for _, msg := range *comp.ReceivedMessages {
		switch msg.MessageType {
		case RequestFlightPlan:
			facility := msg.SourceID[:3] // Facility asking for FP
			// Find the flight plan
			plan, ok := comp.FlightPlans[msg.BCN]
			if ok {
				comp.ToSTARSFacility(facility, plan.DepartureMessage(comp.Identifier, simTime))
			}
			*comp.ReceivedMessages = (*comp.ReceivedMessages)[1:]
		case DepartureDM: // TODO: Find out what this does
		case BeaconTerminate: // TODO: Find out what this does
		}
	}
}

// Prepare the message to sent to a STARS facility after a RF message
func (fp FlightPlan) DepartureMessage(sendingFacility string, simTime time.Time) FlightPlanMessage {
	message := FlightPlanMessage{}
	zulu := simTime.Format("1504Z")
	message.SourceID = fmt.Sprintf(sendingFacility, zulu)
	message.MessageType = Plan
	message.FlightID = fp.ECID + fp.Callsign
	message.AircraftData = AircraftDataMessage{
		DepartureLocation: fp.DepartureAirport,
		NumberOfAircraft:  1, // One for now.
		AircraftType:      fp.TypeWithoutSuffix(),
		AircraftCategory:  fp.AircraftType, // TODO: Use a method to turn this into an aircraft category
		Equipment:         strings.TrimPrefix(fp.AircraftType, fp.TypeWithoutSuffix()),
	}
	message.BCN = fp.AssignedSquawk
	message.CoordinationFix = fp.Exit
	message.Altitude = fmt.Sprintf("%v%v", Select(fp.Rules == VFR, "VFR/", ""), fp.Altitude)
	message.Route = fp.Route

	return message
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
	ContainedPlans   map[Squawk]*STARSFlightPlan
	TrackInformation map[Squawk]*TrackInformation
	ERAMInbox        *[]FlightPlanMessage // The address of the overlying ERAM's message inbox.
	Identifier       string
	STARSInbox       map[string]*[]FlightPlanMessage // Other STARS Facilities inbox.
}

type STARSFlightPlan struct {
	FlightPlan
	FlightPlanType   int
	CoordinationTime CoordinationTime
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
	CoordinationTime CoordinationTime

	// Altitude will either be requested (cruise altitude) for departures, or the assigned altitude for arrivals.
	// ERAM has the ability to assign interm alts (and is used much more than STARS interm alts) with `QQ`.
	// This interim altiude gets sent down to the STARS computer instead of the cruising altitude. If no interim
	// altitude is set, use the cruise altitude (check this)
	// Examples of altitudes could be 310, VFR/170, VFR, 170B210 (block altitude), etc.
	Altitude string
	Route    string

	TrackInformation // For track messages
}

func (fp STARSFlightPlan) Message() FlightPlanMessage {
	return FlightPlanMessage{
		BCN:      fp.AssignedSquawk,
		Altitude: fmt.Sprint(fp.Altitude), // Eventually we'll change this to a string
		Route:    fp.Route,
		AircraftData: AircraftDataMessage{
			DepartureLocation: fp.DepartureAirport,
			NumberOfAircraft:  1,
			AircraftType:      fp.TypeWithoutSuffix(),
			AircraftCategory:  fp.AircraftType, // TODO: Use a method to turn this into an aircraft category
			Equipment:         strings.TrimPrefix(fp.AircraftType, fp.TypeWithoutSuffix()),
		},
		FlightID: fmt.Sprintf("%v%v", fp.ECID, fp.Callsign),
	}
}

type TrackInformation struct {
	TrackLocation     Point2LL // TODO
	TrackOwner        string
	HandoffController string
	FlightPlan        *STARSFlightPlan
}

func (comp *STARSComputer) SendTrackInfo(receivingFacility string, msg FlightPlanMessage, simTime time.Time, Type int) {

	msg.MessageType = Type
	msg.SourceID = fmt.Sprintf("%v%v", comp.Identifier, simTime.Format("1504Z"))
	inbox := comp.STARSInbox[receivingFacility]
	if inbox == nil {
	} else {
		*inbox = append(*inbox, msg)
	}
}

const (
	DepartureTime  = "P"
	ArrivalTime    = "A"
	OverflightTime = "E"
)

type CoordinationTime struct {
	Time time.Time
	Type string // A for arrivals, P for Departures, E for overflights
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

// Sends a message to the overlying ERAM facility.
func (comp *STARSComputer) ToOverlyingERAMFacility(msg FlightPlanMessage) {
	if *comp.ERAMInbox == nil {
	} else {
		*comp.ERAMInbox = append(*comp.ERAMInbox, msg)
	}
}

// Converts the message to a STARS flight plan.
func (s FlightPlanMessage) FlightPlan() *STARSFlightPlan {
	flightPlan := STARSFlightPlan{}
	if !strings.Contains(s.Altitude, "VFR") {
		flightPlan.Rules = IFR
	} else {
		flightPlan.Rules = VFR
	}
	if len(s.FlightID) > 3 {
		flightPlan.ECID = s.FlightID[:3]
		flightPlan.Callsign = s.FlightID[3:]
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

	return &flightPlan
}

func (comp *STARSComputer) RequestFlightPlan(BCN Squawk, simTime time.Time) {
	zulu := simTime.Format("1504Z")
	message := FlightPlanMessage{
		MessageType: RequestFlightPlan,
		BCN:         BCN,
		SourceID:    fmt.Sprintf("%v%v", comp.Identifier, zulu),
	}
	comp.ToOverlyingERAMFacility(message)
}

// identifier can be bcn or callsign
func (w *World) getSTARSFlightPlan(identifier string) (*STARSFlightPlan, error ){
	_, stars := w.SafeFacility("")
	squawk, err := ParseSquawk(identifier)
	if err == nil { // Squawk code was entered
		fp, ok := stars.ContainedPlans[squawk]
		if ok { // The flight plan is stored in the system
			return fp, nil
		} 	
	} else { // Callsign was entered
		for _, plan := range stars.ContainedPlans {
			if plan.Callsign == identifier { // We have this plan in our system
				return plan, nil
			}
		}
	}
	return nil, ErrSTARSNoFlight
}

// Sorting the STARS messages. This will store flight plans with FP messages, change flight plans with AM messages,
// cancel flight plans with CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages() {
	if comp.ContainedPlans == nil {
		comp.ContainedPlans = make(map[Squawk]*STARSFlightPlan)
	}
	for _, msg := range comp.RecievedMessages {
		switch msg.MessageType {
		case Plan:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			comp.RecievedMessages = comp.RecievedMessages[1:]
		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			comp.RecievedMessages = comp.RecievedMessages[1:]
		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)
		case InitateTransfer:
			// 1. Store the data comp.trackinfo. we now know whos tracking the plane, and its flightplan
			comp.TrackInformation[msg.BCN] = &TrackInformation{
				TrackOwner:        msg.TrackOwner,
				HandoffController: msg.HandoffController,
			}
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			fmt.Printf("Message for %v has been received and sorted: %v.\n", msg.BCN, comp.ContainedPlans[msg.BCN])
		case AcceptRecallTransfer:
			// When we send an accept message, we set the track ownership to us.
			// when we receive an accept message, we change the track ownership to the receiving controller.
			// When we send a recall message, we tell our system to stop the flashing.
			// When we receive a recall message, we keep the plan and if we click the track, it is no longer able to be accepted
			// We can infer whether its a recall/ accept by the track ownership that gets sent back.

			if msg.TrackOwner != comp.TrackInformation[msg.BCN].TrackOwner { // has to be an accept message. (We initiated the handoff here)
				if entry, ok := comp.TrackInformation[msg.BCN]; ok {
					entry.TrackOwner = msg.TrackOwner
					entry.HandoffController = ""
					comp.TrackInformation[msg.BCN] = entry
				}
			} else { // has to be a recall message. (we received the handoff)
				if entry, ok := comp.TrackInformation[msg.BCN]; ok {
					entry.HandoffController = ""
					comp.TrackInformation[msg.BCN] = entry
				}
			}

		}
	}
	clear(comp.RecievedMessages)
}
