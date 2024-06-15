package main

import (
	"fmt"
	"math"
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

	for fac, comp := range w.ERAMComputers {
		for fac2, comp2 := range w.ERAMComputers {
			if fac == fac2 {
				continue // dont add our own ERAM to the inbox
			}
			if comp.ERAMInboxes == nil {
				comp.ERAMInboxes = make(map[string]*[]FlightPlanMessage)
			}
			if comp2.ERAMInboxes == nil {
				comp2.ERAMInboxes = make(map[string]*[]FlightPlanMessage)
			}
			comp.ERAMInboxes[fac2] = comp2.ReceivedMessages
		}
	}

	inboxes := make(map[string]*[]FlightPlanMessage)

	for _, eram := range w.ERAMComputers {
		if eram.PendingMessages == nil {
			eram.PendingMessages = make(map[*FlightPlanMessage]string)
		}
		for _, stars := range eram.STARSComputers {
			inboxes[stars.Identifier] = &stars.RecievedMessages
			if stars.MessageMap == nil {
				stars.MessageMap = make(map[FlightPlanMessage]string)
			}
		}
	}

	for _, eram := range w.ERAMComputers {
		for _, stars := range eram.STARSComputers {
			for fac, address := range inboxes {
				if fac == stars.Identifier {
					continue
				}
				if stars.STARSInbox == nil {
					stars.STARSInbox = make(map[string]*[]FlightPlanMessage)
				}
				stars.STARSInbox[fac] = address
			}
		}
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
		_, ok := database.ARTCCs[inputTracon]
		if !ok {
			lg.Errorf("TRACON/ ARTCC %s not found: %v", inputTracon, database.TRACONs)
		} else {
			return w.ERAMComputers[inputTracon], nil
		}

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
	controller := w.GetControllerByCallsign(callsign)
	if controller != nil {
		return controller.Facility
	}
	lg.Errorf("Couldn't find facility for %v: %v. \n", callsign, w.GetAllControllers())
	if len(callsign) == 7 && (callsign[3:] =="_APP" || callsign[3:] == "_DEP") {
		return w.TRACON // figure out why sometimes EWR_APP (primary controller) doesn't show up
	}
	return ""
}

// Give the computers a chance to sort through their received messages. Messages will send when the time is appropriate (eg. handoff).
// Some messages will be sent from recieved messages (for example a FP message from a RF message).
func (w *World) UpdateComputers(simTime time.Time) {
	// _, fac := w.SafeFacility("")
	// Sort through messages made
	for _, comp := range w.ERAMComputers {
		comp.SortMessages(simTime, w.TRACON)
		comp.SendFlightPlans(w)
		for _, stars := range comp.STARSComputers {
			stars.SortReceivedMessages()
		}
	}
}

func (fp *STARSFlightPlan) CordinationFix(w *World, ac *Aircraft) string { // TODO: Replace AC with track info
	fixes := w.STARSFacilityAdaptation.CoordinationFixes
	for fix, typ := range fixes {
		if typ == ZoneBasedFix { // Exclude zone based fixes for now. They come in after the route-based fixes.
			continue
		}
		if strings.Contains(fp.Route, fix) {
			return fix
		}
	}
	distanceMap := make(map[string]float32) //  -->
	for fix, typ := range fixes {
		if typ == RouteBasedFix {
			distanceMap[fix] = nmdistance2ll(ac.Position(), database.Fixes[fix].Location)
		}
	}
	var closestFix string
	smallestValue := float32(math.MaxFloat32)
	for key, value := range distanceMap {
		if value < smallestValue {
			smallestValue = value
			closestFix = key
		}
	}
	if closestFix == "" {
		lg.Errorf("No fix for %v/%v. Route: %v.", ac.Callsign, ac.Squawk, ac.FlightPlan.Route)
	}
	return closestFix
}

func (fp *FlightPlan) STARS() *STARSFlightPlan {
	return &STARSFlightPlan{
		FlightPlan: *fp,
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
	ERAMInboxes      map[string]*[]FlightPlanMessage
	ReceivedMessages *[]FlightPlanMessage
	FlightPlans      map[Squawk]*STARSFlightPlan
	TrackInformation map[Squawk]*TrackInformation
	PendingMessages  map[*FlightPlanMessage]string // if a ZBW to N90 handoff goes through ZNY, ZNY needs to keep track of who sent that handoff initially, so that
	// when N90 sends the accept handoff message, it can go back to ZBW.
	Identifier       string
}

type ERAMTrackInfo struct {
	Location          Point2LL
	Owner             string
	HandoffController string
}

func (comp *ERAMComputer) UpdateTrackInfo(w *World) {
	for sq, info := range comp.TrackInformation {
		info.TrackLocation = w.GetAircraft(comp.FlightPlans[sq].Callsign, false).Position()
	}
}

func (comp *ERAMComputer) SendMessageToERAM(facility string, msg FlightPlanMessage) error {
	if _, ok := comp.ERAMInboxes[facility]; ok {
		*comp.ERAMInboxes[facility] = append(*comp.ERAMInboxes[facility], msg)
		fmt.Printf("Sent msg for %v to %v.\n", msg.BCN, facility)
		return nil
	} else {
		fmt.Printf("Eram facility %v could not be found in %v inbox: %v\n", facility, comp.Identifier, comp.ERAMInboxes)
		return ErrNoERAMFacility
	}
}

// func (w *World) GatherFilledPlans() {
// 	for _, plan := range w.NonSortedPlans {
// 		eram := w.ERAMComputers[database.Airports[plan.DepartureAirport].ARTCC]
// 		eram.FlightPlans[plan.AssignedSquawk] = plan
// 		w.NonSortedPlans = w.NonSortedPlans[1:]
// 	}
// }

func (comp *ERAMComputer) SortMessages(simTime time.Time, tracon string) {
	if comp.ReceivedMessages == nil {
		comp.ReceivedMessages = &[]FlightPlanMessage{}
	}
	for _, msg := range *comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			if comp == nil {
				lg.Errorf("comp = nil")
			} else if msg.FlightPlan() == nil {
				lg.Errorf("msg.plan = nil")
			} else {
				if comp.FlightPlans == nil {
					comp.FlightPlans = make(map[Squawk]*STARSFlightPlan) // Use appropriate types here
				}
				// Ensure comp.FlightPlans[msg.BCN] is initialized
				comp.FlightPlans[msg.BCN] = msg.FlightPlan()
			}
		case RequestFlightPlan:
			facility := msg.SourceID[:3] // Facility asking for FP
			// Find the flight plan
			plan, ok := comp.FlightPlans[msg.BCN]
			if ok {
				comp.ToSTARSFacility(facility, plan.DepartureMessage(comp.Identifier, simTime))
			}
			*comp.ReceivedMessages = (*comp.ReceivedMessages)[1:]
		case DepartureDM: // Stars ERAM coordination time tracking
		case BeaconTerminate: // TODO: Find out what this does
		case InitateTransfer:
			// Forward these to w.TRACON for now. ERAM adaptations will have to fix this eventually...
			msg.SourceID = comp.Identifier + msg.SourceID[3:]
			comp.ToSTARSFacility(tracon, msg)
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
	fmt.Printf("Identifier %v. Messages: %v.\n", facility, STARSFacility.RecievedMessages)
	return nil
}

func (comp *ERAMComputer) SendFlightPlans(w *World) {
	if len(comp.TrackInformation) == 0 {
		return
	}
	// fmt.Printf("Sending fplans for %v. Len: %v.\n", comp.Identifier, len(comp.TrackInformation))
	for sq := range comp.TrackInformation {
		if comp.FlightPlans != nil {
			if fp := comp.FlightPlans[sq]; fp != nil {
				if !w.SimTime.Add(30*time.Minute).Before(fp.CoordinationTime.Time) && !fp.Sent {
					comp.SendFlightPlan(fp, w)
				} else if !fp.Sent {
					fmt.Printf("%v is more than 30 minutes away from his coordination fix %v. Coordination Time: %v, Time Added: %v.\n\n", fp.Callsign, fp.CoordinationFix, fp.CoordinationTime, w.SimTime.Add(30*time.Minute))
				}
			} else {
				lg.Errorf("%v: Plan for %v is nil: %v.", comp.Identifier, sq, comp.FlightPlans)
			}
		} else {
			lg.Errorf("Plans for %v is nil.", comp.Identifier)
		}
	}
}

func (comp *ERAMComputer) SendFlightPlan(fp *STARSFlightPlan, w *World) { // For individual plans being sent.
	err := comp.ToSTARSFacility(w.TRACON, fp.Message())
	fmt.Printf("Sent %v plan to %v\n", fp.AssignedSquawk, w.TRACON)
	fp.Sent = true
	if err != nil {
		artcc := database.TRACONs[w.TRACON].ARTCC
		err = comp.SendMessageToERAM(artcc, fp.Message())
		if err != nil {
			lg.Error(err.Error())
		}
	}
}

type STARSComputer struct {
	RecievedMessages []FlightPlanMessage
	ContainedPlans   map[Squawk]*STARSFlightPlan
	TrackInformation map[Squawk]*TrackInformation
	ERAMInbox        *[]FlightPlanMessage // The address of the overlying ERAM's message inbox.
	Identifier       string
	STARSInbox       map[string]*[]FlightPlanMessage // Other STARS Facilities inbox.
	MessageMap map[FlightPlanMessage]string
}

type STARSFlightPlan struct {
	FlightPlan
	FlightPlanType   int
	CoordinationTime CoordinationTime
	CoordinationFix  string
	Sent             bool
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
		FlightID:         fmt.Sprintf("%v%v", fp.ECID, fp.Callsign),
		CoordinationFix:  fp.CoordinationFix,
		CoordinationTime: fp.CoordinationTime,
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
	if inbox != nil {
		*inbox = append(*inbox, msg)
		fmt.Printf("%v: Appended for %v. Msg: %v.\n", receivingFacility, msg.SourceID, msg)
	} else {
		lg.Errorf("%v: inbox is nil: %v.\n", receivingFacility, comp.STARSInbox) // TODO: give to overlying ERAM
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
func (w *World) getSTARSFlightPlan(identifier string) (*STARSFlightPlan, error) {
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

// This should be facility-defined in the json file, but for now it's 30nm near their departure airport
func (ac *Aircraft) inAcquisitionArea(w *World) bool {
	ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
	ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
	if ap != nil {
		if (nmdistance2ll(ap.Location, ac.Position()) <= 2 || nmdistance2ll(ap2.Location, ac.Position()) <= 2) && !ac.inDropArea(w) {
			return true
		}
	}
	return false
}

func (ac *Aircraft) inDropArea(w *World) bool {
	ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
	ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
	if (ap != nil && nmdistance2ll(ap.Location, ac.Position()) <= 1) || (ap2 != nil && nmdistance2ll(ap2.Location, ac.Position()) <= 1) {
		if (ap != nil && ac.Altitude() <= float32(database.Airports[ac.FlightPlan.DepartureAirport].Elevation+200)) ||
			ac.Altitude() <= float32(database.Airports[ac.FlightPlan.ArrivalAirport].Elevation+200) {
			return true
		}
	}

	return false
}

// Sorting the STARS messages. This will store flight plans with FP messages, change flight plans with AM messages,
// cancel flight plans with CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages() {
	if comp.ContainedPlans == nil {
		comp.ContainedPlans = make(map[Squawk]*STARSFlightPlan)
	}
	if comp.TrackInformation == nil {
		comp.TrackInformation = make(map[Squawk]*TrackInformation)
	}
	for _, msg := range comp.RecievedMessages {
		switch msg.MessageType {
		case Plan:
			sq, _ := ParseSquawk("0000")
			if msg.BCN != sq {
				comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
				comp.RecievedMessages = comp.RecievedMessages[1:]
			}
			// todo: change with adaptations
			
		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			comp.RecievedMessages = comp.RecievedMessages[1:]
		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)
		case InitateTransfer:
			// 1. Store the data comp.trackinfo. we now know whos tracking the plane. Use the squawk to get the plan
			if fp := comp.ContainedPlans[msg.BCN]; fp != nil { // We have the plan
				comp.TrackInformation[msg.BCN] = &TrackInformation{
					TrackOwner:        msg.TrackOwner,
					HandoffController: msg.HandoffController,
					FlightPlan:        fp,
				}
				delete(comp.ContainedPlans, msg.BCN)
				comp.MessageMap[msg] = msg.SourceID[:3]
				fmt.Printf("Message for %v has been received and sorted: %v.\n", msg.BCN, comp.ContainedPlans[msg.BCN])
			} else {
				// lg.Errorf("No flight plan for %v.\n", msg.BCN)
				// reject the thing
			}

		case AcceptRecallTransfer:
			// When we send an accept message, we set the track ownership to us.
			// when we receive an accept message, we change the track ownership to the receiving controller.
			// When we send a recall message, we tell our system to stop the flashing.
			// When we receive a recall message, we keep the plan and if we click the track, it is no longer able to be accepted
			// We can infer whether its a recall/ accept by the track ownership that gets sent back.

			info := comp.TrackInformation[msg.BCN]
			if info == nil {
				fmt.Printf("no track info for: %v. Fac: %v.\n", msg.BCN, comp.Identifier)
				break
			}

			if msg.TrackOwner != comp.TrackInformation[msg.BCN].TrackOwner { // has to be an accept message. (We initiated the handoff here)
				if entry, ok := comp.TrackInformation[msg.BCN]; ok {
					entry.TrackOwner = msg.TrackOwner
					entry.HandoffController = ""
					comp.TrackInformation[msg.BCN] = entry
				}
			} else { // has to be a recall message. (we received the handoff)
				delete(comp.TrackInformation, msg.BCN)
			}

		}
	}
	clear(comp.RecievedMessages)
}

func printERAMComputerMap(computers map[string]*ERAMComputer) {
	for key, eramComputer := range computers {
		if key != "ZNY" {
			continue
		}
		fmt.Printf("Key: %s\n", key)
		fmt.Printf("Identifier: %s\n", eramComputer.Identifier)

		fmt.Println("STARSComputers:")
		for scKey, starsComputer := range eramComputer.STARSComputers {
			fmt.Printf("\tKey: %s, Identifier: %s\n", scKey, starsComputer.Identifier)
			fmt.Printf("\tReceivedMessages: %v\n", starsComputer.RecievedMessages)

			fmt.Println("\tContainedPlans:")
			for sq, plan := range starsComputer.ContainedPlans {
				fmt.Printf("\t\tSquawk: %s, Callsign %v, Plan: %+v\n", sq, plan.Callsign, *plan)
			}

			fmt.Println("\tTrackInformation:")
			for sq, trackInfo := range starsComputer.TrackInformation {
				fmt.Printf("\t\tSquawk: %s, TrackInfo: %+v\n", sq, *trackInfo)
			}

			if starsComputer.ERAMInbox != nil {
				fmt.Printf("\tERAMInbox: %v\n", *starsComputer.ERAMInbox)
			}

		}

		fmt.Println("ERAMInboxes:")
		for eiKey, inbox := range eramComputer.ERAMInboxes {
			fmt.Printf("\tKey: %s, Messages: %v\n", eiKey, *inbox)
		}

		if eramComputer.ReceivedMessages != nil {
			fmt.Printf("ReceivedMessages: %v\n", *eramComputer.ReceivedMessages)
		}

		fmt.Println("FlightPlans:")
		for sq, plan := range eramComputer.FlightPlans {
			fmt.Printf("\tSquawk: %s, Plan: %+v\n", sq, *plan)
		}

		fmt.Println("TrackInformation:")
		for sq, trackInfo := range eramComputer.TrackInformation {
			fmt.Printf("\tSquawk: %s, TrackInfo: %+v\n", sq, *trackInfo)
		}
	}
}
