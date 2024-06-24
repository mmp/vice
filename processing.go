package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

/*
idt
receivedmessages
adaptation
starscomputers
eraminboxes
trackinfo
stars:
receivesmessages
idt
eraminbox
unsupported
starsinboxes
trackinfo
*/

func (w *World) initComputers() {
	w.ERAMComputers = make(map[string]*ERAMComputer)
	for fac := range database.ARTCCs {
		w.ERAMComputers[fac] = &ERAMComputer{
			Identifier:       fac,
			ReceivedMessages: &[]FlightPlanMessage{},
		}
		availibleSquawks := make(map[int]interface{})
		for i := 0o1001; i <= 0o7777; i++ {
			availibleSquawks[i] = nil
			
		}
		starsAvailibleSquawks := make(map[int]interface{})
		
			
		bank := int64(w.STARSFacilityAdaptation.BeaconBank)
		min, _ := strconv.ParseInt(fmt.Sprint(1+bank*100), 8, 64)
		max, _ := strconv.ParseInt(fmt.Sprint(77+bank*100), 8, 64)
		for i := min; i <= max; i++ {
			starsAvailibleSquawks[int(i)] = nil
		}

		w.ERAMComputers[fac].Adaptation = database.ERAMAdaptations[fac]
		w.ERAMComputers[fac].STARSComputers = make(map[string]*STARSComputer)
		w.ERAMComputers[fac].TrackInformation = make(map[string]*TrackInformation)
		w.ERAMComputers[fac].FlightPlans = make(map[Squawk]*STARSFlightPlan)
		w.ERAMComputers[fac].AvailibleSquawks = availibleSquawks
		for name, stars := range database.TRACONs {
			if stars.ARTCC == fac { // if the artcc of the tracon is the same
				w.ERAMComputers[fac].STARSComputers[name] = &STARSComputer{ // make a news stars comp for this new fac (with var name)
					Identifier:        name,
					AvailibleSquawks:  starsAvailibleSquawks,
					UnsupportedTracks: make(map[int]*UnsupportedTrack), // Using one value for the bank is good enough (for now)
					TrackInformation:  make(map[string]*TrackInformation),
					ContainedPlans:    make(map[Squawk]*STARSFlightPlan),
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
		for _, stars := range eram.STARSComputers {
			inboxes[stars.Identifier] = &stars.RecievedMessages
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
	if controller != nil && controller.Facility != "" {
		return controller.Facility
	} else if controller != nil {
		return w.TRACON
	}
	if len(callsign) == 7 && (callsign[3:] == "_APP" || callsign[3:] == "_DEP") {
		return w.TRACON // figure out why sometimes EWR_APP (primary controller) doesn't show up
	}
	return ""
}

// Give the computers a chance to sort through their received messages. Messages will send when the time is appropriate (eg. handoff).
// Some messages will be sent from recieved messages (for example a FP message from a RF message).
func (w *World) UpdateComputers(simTime time.Time, e *EventStream) {
	// _, fac := w.SafeFacility("")
	// Sort through messages made
	for _, comp := range w.ERAMComputers {
		comp.SortMessages(simTime, w)
		comp.SendFlightPlans(w)
		for _, stars := range comp.STARSComputers {
			stars.SortReceivedMessages(e)
		}
	}
}

type UnsupportedTrack struct {
	TrackLocation     Point2LL
	Owner             string
	HandoffController string
	FlightPlan        *STARSFlightPlan
}

type AdaptationFixes []AdaptationFix

func (fp *STARSFlightPlan) GetCoordinationFix(w *World, ac *Aircraft) string {
	fixes := w.STARSFacilityAdaptation.CoordinationFixes
	for fix, multiple := range fixes {

		info := multiple.Fix(fp.Altitude)

		if info.Type == ZoneBasedFix { // Exclude zone based fixes for now. They come in after the route-based fixe
			continue
		}
		if strings.Contains(fp.Route, fix) {
			return fix
		}
		for _, waypoint := range ac.Nav.Waypoints {
			if waypoint.Fix == fix {
				return fix
			}
		}

	}
	var closestFix string
	smallestValue := float32(math.MaxFloat32)
	for fix, multiple := range fixes {
		for _, info := range multiple {
			if info.Type == ZoneBasedFix {
				dist := nmdistance2ll(ac.Position(), database.Fixes[fix].Location)
				if dist < smallestValue {
					smallestValue = dist
					closestFix = fix
				}
			}
		}
	}

	if closestFix == "" {
		lg.Errorf("No fix for %v/%v. Route: %v.", ac.Callsign, ac.Squawk, ac.Nav.Waypoints)
	}
	return closestFix
}

func (fixes AdaptationFixes) Fix(altitude string) AdaptationFix {
	if len(fixes) == 1 {
		return fixes[0]
	}
	if len(fixes) == 0 {
		// lg.Errorf("0 len was returned. Alt: %v.")
		return AdaptationFix{}
	}
	alt, err := strconv.Atoi(altitude) // eventually make a function to parse a string that has a block altitude (for example)
	// and return an int (figure out how STARS handles that). For now strconv.Atoi can be used
	if err == nil {
		for _, fix := range fixes {
			if fix.Altitude[0] <= alt && fix.Altitude[0] >= alt {
				return fix
			}
		}
	}
	return AdaptationFix{}
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

	InitiateTransfer     // When handoff gets sent. Sends the flightplan, contains track location
	AcceptRecallTransfer // Accept/ recall handoff
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
	TrackInformation map[string]*TrackInformation
	AvailibleSquawks map[int]interface{}
	Identifier       string
	Adaptation       ERAMAdaptation
}

type ERAMTrackInfo struct {
	Location          Point2LL
	Owner             string
	HandoffController string
}

const TransmitFPMessageTime = 30 * time.Minute

func (comp *ERAMComputer) SendMessageToERAM(facility string, msg FlightPlanMessage) error {
	if _, ok := comp.ERAMInboxes[facility]; ok {
		*comp.ERAMInboxes[facility] = append(*comp.ERAMInboxes[facility], msg)
		return nil
	} else {
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

func (comp *ERAMComputer) SortMessages(simTime time.Time, w *World) {
	if comp.ReceivedMessages == nil {
		comp.ReceivedMessages = &[]FlightPlanMessage{}
	}
	for _, msg := range *comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			blank := Squawk(0)
			if comp == nil {
				lg.Errorf("comp = nil")
			} else if msg.FlightPlan() == nil {
				lg.Errorf("msg.plan = nil")
			} else if msg.FlightPlan().AssignedSquawk != blank { // TODO: Figure out why it's sending a blank fp
				if comp.FlightPlans == nil {
					comp.FlightPlans = make(map[Squawk]*STARSFlightPlan) // Use appropriate types here
				}
				// Ensure comp.FlightPlans[msg.BCN] is initialized
				comp.FlightPlans[msg.BCN] = msg.FlightPlan()
				fp := comp.FlightPlans[msg.BCN]
				if fp.CoordinationFix == "" {
					for fix, fixes := range comp.Adaptation.CoordinationFixes {
						properties := fixes.Fix(fp.Altitude)
						if properties.Type == ZoneBasedFix {
							continue
						}
						if strings.Contains(fp.Route, fix) {
							fp.CoordinationFix = fix
							break
						}
					}
					if fp.CoordinationFix == "" {
					}
				}
				// check if another facility needs this plan.
				if to := comp.Adaptation.CoordinationFixes[fp.CoordinationFix].Fix(fp.Altitude).ToFacility; to != comp.Identifier {
					// Send the plan to the STARS facility that needs it.
					comp.ToSTARSFacility(to, msg)
				}
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
		case InitiateTransfer:
			// Forward these to w.TRACON for now. ERAM adaptations will have to fix this eventually...

			if comp.TrackInformation[msg.Identifier] == nil {
				comp.TrackInformation[msg.Identifier] = &TrackInformation{
					FlightPlan: comp.FlightPlans[msg.BCN],
				}
			}
			comp.TrackInformation[msg.Identifier].TrackOwner = msg.TrackOwner
			comp.TrackInformation[msg.Identifier].HandoffController = msg.HandoffController
			comp.AvailibleSquawks[int(msg.BCN)] = nil

			for name, fixes := range comp.Adaptation.CoordinationFixes {
				fix := fixes.Fix(comp.TrackInformation[msg.Identifier].FlightPlan.Altitude)

				if name == msg.CoordinationFix && fix.ToFacility != comp.Identifier { // Forward
					msg.SourceID = comp.Identifier + simTime.Format("1504Z")
					if to := fix.ToFacility; to[0] == 'Z' { // To another ARTCC
						comp.SendMessageToERAM(to, msg)
					} else { // To a TRACON
						comp.ToSTARSFacility(to, msg)
					}
				} else if name == msg.CoordinationFix && fix.ToFacility == comp.Identifier { // Stay here
					comp.TrackInformation[msg.Identifier] = &TrackInformation{
						TrackOwner:        msg.TrackOwner,
						HandoffController: msg.HandoffController,
						FlightPlan:        comp.FlightPlans[msg.BCN],
					}
				}
			}

		case AcceptRecallTransfer:
			fixInfo := comp.Adaptation.CoordinationFixes[msg.CoordinationFix].Fix(comp.TrackInformation[msg.Identifier].FlightPlan.Altitude)
			if info := comp.TrackInformation[msg.Identifier]; info != nil {
				if msg.TrackOwner == info.TrackOwner { // Recall message, we can free up this code now
					comp.AvailibleSquawks[int(msg.BCN)] = nil
				}
				info.TrackOwner = msg.TrackOwner
			}
			if fixInfo.FromFacility != comp.Identifier { // Comes from a different ERAM facility
				comp.SendMessageToERAM(fixInfo.FromFacility, msg)
			}

		}
	}
	clear(*comp.ReceivedMessages)
}

// Prepare the message to sent to a STARS facility after a RF message
func (fp FlightPlan) DepartureMessage(sendingFacility string, simTime time.Time) FlightPlanMessage {
	message := FlightPlanMessage{}
	message.SourceID = sendingFacility + simTime.Format("1504Z")
	message.MessageType = Plan
	message.FlightID = fp.ECID + fp.Callsign
	message.AircraftData = AircraftDataMessage{
		DepartureLocation: fp.DepartureAirport,
		ArrivalLocation:   fp.ArrivalAirport,
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

func (comp *ERAMComputer) SendFlightPlans(w *World) {

	sendPlanIfReady := func(fp *STARSFlightPlan) {
		to := comp.Adaptation.CoordinationFixes[fp.CoordinationFix].Fix(fp.Altitude).ToFacility
		if !w.SimTime.Add(TransmitFPMessageTime).Before(fp.CoordinationTime.Time) && !slices.Contains(fp.ContainedFacilities, to) {
			comp.SendFlightPlan(fp, w)
		}
	}

	for _, info := range comp.TrackInformation {
		var fp *STARSFlightPlan
		if info.FlightPlan != nil {
			fp = info.FlightPlan
		} else {
			continue
		}
		if fp.Callsign == "" && fp.Altitude == "" {
			delete(comp.TrackInformation, info.Identifier) // figure out why these are sneaking in here!
			continue
		}
		sendPlanIfReady(fp)

	}
	for _, info := range comp.FlightPlans {
		var fp *STARSFlightPlan
		if info != nil {
			fp = info
		} else {
			continue
		}
		sendPlanIfReady(fp)
	}

}

func (comp *ERAMComputer) SendFlightPlan(fp *STARSFlightPlan, w *World) { // For individual plans being sent.
	msg := fp.Message()
	msg.MessageType = Plan
	msg.SourceID = comp.Identifier + w.SimTime.Format("1504Z")
	to := comp.Adaptation.CoordinationFixes[fp.CoordinationFix].Fix(fp.Altitude).ToFacility
	err := comp.ToSTARSFacility(w.TRACON, msg) // change w.TRACON to the fix pair assignment (this will be in the adaptation)
	if err != nil {                            // must go to another ERAM facility
		comp.SendMessageToERAM(database.TRACONs[w.TRACON].ARTCC, msg)
	}
	fp.ContainedFacilities = append(fp.ContainedFacilities, to)
}

type STARSComputer struct {
	ContainedPlans    map[Squawk]*STARSFlightPlan
	RecievedMessages  []FlightPlanMessage
	TrackInformation  map[string]*TrackInformation
	ERAMInbox         *[]FlightPlanMessage // The address of the overlying ERAM's message inbox.
	Identifier        string
	STARSInbox        map[string]*[]FlightPlanMessage // Other STARS Facilities inbox.
	UnsupportedTracks map[int]*UnsupportedTrack
	AvailibleSquawks  map[int]interface{}
}

type STARSFlightPlan struct {
	FlightPlan
	FlightPlanType      int
	CoordinationTime    CoordinationTime
	CoordinationFix     string
	ContainedFacilities []string
	Altitude            string
	SP1                 string
	SP2                 string
	InitialController   string // For abbreviated FPs
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
		Altitude: fp.Altitude, // Eventually we'll change this to a string
		Route:    fp.Route,
		AircraftData: AircraftDataMessage{
			DepartureLocation: fp.DepartureAirport,
			ArrivalLocation:   fp.ArrivalAirport,
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
	Identifier        string
	TrackOwner        string
	HandoffController string
	FlightPlan        *STARSFlightPlan
	PointOut          string
	PointOutHistory   []string
	RedirectedHandoff RedirectedHandoff
	SP1               string
	SP2               string
	AutoAssociateFP   bool // If it's white or not
}

func (comp *STARSComputer) SendTrackInfo(receivingFacility string, msg FlightPlanMessage, simTime time.Time, Type int) {

	msg.MessageType = Type
	msg.SourceID = comp.Identifier + simTime.Format("1504Z")
	inbox := comp.STARSInbox[receivingFacility]
	if inbox != nil {
		*inbox = append(*inbox, msg)
	} else {
		comp.ToOverlyingERAMFacility(msg)
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
	ArrivalLocation   string // Only for arrivals. I think this is made up, but I don't know where to get the arrival info from.
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
	if comp == nil {
		lg.Error("STARS computer is nil.\n")
		return
	}
	if *comp.ERAMInbox != nil {
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
	flightPlan.ArrivalAirport = s.AircraftData.ArrivalLocation
	flightPlan.Route = s.Route
	flightPlan.CoordinationFix = s.CoordinationFix
	flightPlan.CoordinationTime = s.CoordinationTime
	flightPlan.Altitude = s.Altitude

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
	if ac != nil {
		ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
		ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
		if ap != nil {
			if nmdistance2ll(ap.Location, ac.Position()) <= 2 && !ac.inDropArea(w) {
				return true
			}
		}
		if ap2 != nil {
			if nmdistance2ll(ap2.Location, ac.Position()) <= 2 && !ac.inDropArea(w) {
				return true
			}
		}
	}
	return false
}

func (ac *Aircraft) inDropArea(w *World) bool {
	ap := w.GetAirport(ac.FlightPlan.DepartureAirport)
	ap2 := w.GetAirport(ac.FlightPlan.ArrivalAirport)
	if (ap != nil && nmdistance2ll(ap.Location, ac.Position()) <= 1) || (ap2 != nil && nmdistance2ll(ap2.Location, ac.Position()) <= 1) {
		if (ap != nil && ac.Altitude() <= float32(database.Airports[ac.FlightPlan.DepartureAirport].Elevation+50)) ||
			ac.Altitude() <= float32(database.Airports[ac.FlightPlan.ArrivalAirport].Elevation+50) {
			return true
		}
	}

	return false
}

// Sorting the STARS messages. This will store flight plans with FP messages, change flight plans with AM messages,
// cancel flight plans with CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages(e *EventStream) {
	if comp.ContainedPlans == nil {
		comp.ContainedPlans = make(map[Squawk]*STARSFlightPlan)
	}
	if comp.TrackInformation == nil {
		comp.TrackInformation = make(map[string]*TrackInformation)
	}
	for _, msg := range comp.RecievedMessages {
		switch msg.MessageType {
		case Plan:
			sq := Squawk(0)
			if msg.BCN == sq {
				break
			}
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)
		case InitiateTransfer:
			// 1. Store the data comp.trackinfo. we now know whos tracking the plane. Use the squawk to get the plan
			if fp := comp.ContainedPlans[msg.BCN]; fp != nil { // We have the plan
				comp.TrackInformation[msg.Identifier] = &TrackInformation{
					TrackOwner:        msg.TrackOwner,
					HandoffController: msg.HandoffController,
					FlightPlan:        fp,
				}
				delete(comp.ContainedPlans, msg.BCN)
				e.Post(Event{
					Type:         DataAcceptance,
					Callsign:     msg.Identifier,
					ToController: msg.TrackOwner,
				})
			} else {
				if trk := comp.TrackInformation[msg.Identifier]; trk != nil {
					comp.TrackInformation[msg.Identifier] = &TrackInformation{
						TrackOwner:        msg.TrackOwner,
						HandoffController: msg.HandoffController,
						FlightPlan:        trk.FlightPlan,
					}
					delete(comp.ContainedPlans, msg.BCN)
					e.Post(Event{
						Type:         DataAcceptance,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				} else { // send an IF msg
					e.Post(Event{
						Type:         DataRejection,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				}

			}

		case AcceptRecallTransfer:
			// When we send an accept message, we set the track ownership to us.
			// when we receive an accept message, we change the track ownership to the receiving controller.
			// When we send a recall message, we tell our system to stop the flashing.
			// When we receive a recall message, we keep the plan and if we click the track, it is no longer able to be accepted
			// We can infer whether its a recall/ accept by the track ownership that gets sent back.

			info := comp.TrackInformation[msg.Identifier]
			if info == nil {
				break
			}

			if msg.TrackOwner != comp.TrackInformation[msg.Identifier].TrackOwner { // has to be an accept message. (We initiated the handoff here)
				if entry, ok := comp.TrackInformation[msg.Identifier]; ok {
					entry.TrackOwner = msg.TrackOwner
					entry.HandoffController = ""
					comp.TrackInformation[msg.Identifier] = entry
				}
			} else { // has to be a recall message. (we received the handoff)
				delete(comp.TrackInformation, msg.Identifier)
			}
		}
	}
	clear(comp.RecievedMessages)
}

// For NAS codes
func (comp *ERAMComputer) CreateSquawk() Squawk {
	for sq := range comp.AvailibleSquawks {
		delete(comp.AvailibleSquawks, sq)
		return Squawk(sq)
	}
	return -1 // 0000 could theoretically be a squawk code(?)
}

// For local codes
func (comp *STARSComputer) CreateSquawk(x int) Squawk {
	for sq := range comp.AvailibleSquawks {
		delete(comp.AvailibleSquawks, sq)
		return Squawk(sq)
	}
	return -1
}

const (
	ACID = iota
	BCN
	ControllingPosition
	TypeOfFlight // Figure out this
	SC1
	SC2
	AircraftType
	RequestedALT
	Rules
	DepartureAirport // Specified with type of flight (maybe)
	Errors
)

type AbbreviatedFPFields struct {
	ACID string 
	BCN Squawk
	ControllingPosition string
	TypeOfFlight string // Figure out this
	SC1 string
	SC2 	string 
	AircraftType string 
	RequestedALT string 
	Rules FlightRules
	DepartureAirport string  // Specified with type of flight (maybe)
	Error error

}

func (w *World) parseAbbreviatedFPFields(fields []string) AbbreviatedFPFields {
	output := AbbreviatedFPFields{}
	if len(fields[0]) >= 2 && len(fields[0]) <= 7 && unicode.IsLetter(rune(fields[0][0])) {
		output.ACID = fields[0]

	} else {
		output.Error = ErrSTARSIllegalACID
		return output
	}

	for _, field := range fields[1:] { // fields[0] is always the ACID
		sq, err := ParseSquawk(field) // See if it's a BCN
		if err == nil {
			output.BCN = sq
			continue
		}
		if len(field) == 2 { // See if its specifying the controlling position
			output.ControllingPosition = field
			continue
		}
		if len(field) <= 2 { // See if it's specifying the type of flight. No errors for this because this could turn into a scratchpad
			if len(field) == 1 {
				switch field {
				case "A":
					output.TypeOfFlight = "arrival"
				case "P":
					output.TypeOfFlight = "departure"
				case "E":
					output.TypeOfFlight = "overflight"
				}
			} else if len(field) == 2 { // Type first, then airport id
				types := []string{"A", "P", "E"}
				if slices.Contains(types, field[:1]) {
					output.TypeOfFlight = field[:1]
					output.DepartureAirport = field[1:]
					continue
				}
			}
		}

		badScratchpads := []string{"NAT", "CST", "AMB", "RDR", "ADB", "XXX"}
		if strings.HasPrefix(field, STARSTriangleCharacter) && len(field) > 3 && len(field) <= 5 || (len(field) <= 6 && w.STARSFacilityAdaptation.AllowLongScratchpad[0]) { // See if it's specifying the SC1

			if slices.Contains(badScratchpads, field) {
				output.Error = ErrSTARSIllegalScratchpad
				return output
			}
			if isAllNumbers(field[len(field)-3:]) {
				output.Error = ErrSTARSIllegalScratchpad
			}
			output.SC1 = field
		}
		if strings.HasPrefix(field, "+") && len(field) > 2 && (len(field) <= 4 || (len(field) <= 5 && w.STARSFacilityAdaptation.AllowLongScratchpad[1])) { // See if it's specifying the SC1
			if slices.Contains(badScratchpads, field) {
				output.Error = ErrSTARSIllegalScratchpad
				return output
			}
			if isAllNumbers(field[len(field)-3:]) {
				output.Error = ErrSTARSIllegalScratchpad
			}
			output.SC2 = field
		}
		if acFields := strings.Split(field, "/"); len(field) >= 4 { // See if it's specifying the type of flight
			switch len(acFields) {
			case 1: // Just the AC Type
				if _, ok := database.AircraftPerformance[field]; !ok { // AC doesn't exist
				output.Error = ErrSTARSIllegalACType
					continue
				} else {
					output.AircraftType = field
					continue
				}
			case 2: // Either a formation number with the ac type or a ac type with a equipment suffix
				if all := isAllNumbers(acFields[0]); all { // Formation number
					if !unicode.IsLetter(rune(acFields[1][0])) {
						output.Error = ErrSTARSCommandFormat
						return output
					}
					if _, ok := database.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
					output.Error = ErrSTARSIllegalACType // This error is informational. Shouldn't end the entire function. Just this switch statement
						continue
					}
					output.AircraftType = field
				} else { // AC Type with equipment suffix
					if len(acFields[1]) > 1 || !isAllLetters(acFields[1]) {
						output.Error = ErrSTARSCommandFormat
						return output
					}
					if _, ok := database.AircraftPerformance[acFields[0]]; !ok { // AC doesn't exist
					output.Error = ErrSTARSIllegalACType
						continue
					}
					output.AircraftType = field
				}
			case 3:
				if len(acFields[2]) > 1 || !isAllLetters(acFields[2]) {
					output.Error = ErrSTARSCommandFormat
					return output
				}
				if !unicode.IsLetter(rune(acFields[1][0])) {
					output.Error = ErrSTARSCommandFormat
					return output
				}
				if _, ok := database.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
				output.Error = ErrSTARSIllegalACType
					break
				}
				output.AircraftType = field
			}
			continue
		}
		if len(field) == 3 && isAllNumbers(field) {
			output.RequestedALT = field
			continue
		}
		if len(field) == 2 {
			if field[0] != '.' {
				output.Error = ErrSTARSCommandFormat
				return output
			}
			switch field[1] {
			case 'V':
				output.Rules = VFR
				break // This is the last entry, so we can break here
			case 'P':
				output.Rules = VFR // vfr on top
				break
			case 'E':
				output.Rules = IFR // enroute
				break
			default:
				output.Error = ErrSTARSIllegalValue
				return output
			}
		}

	}
	return output
}

// For debugging purposes
func printERAMComputerMap(computers map[string]*ERAMComputer) {
	for key, eramComputer := range computers {
		allowedFacilities := []string{"ZNY", "ZDC", "ZBW"} // Just so the console doesn't get flodded with empty ARTCCs (I debug with EWR)
		if !slices.Contains(allowedFacilities, key) {
			continue
		}
		fmt.Printf("Key: %s\n", key)
		fmt.Printf("Identifier: %s\n", eramComputer.Identifier)

		fmt.Println("STARSComputers:")
		for scKey, starsComputer := range eramComputer.STARSComputers {
			fmt.Printf("\tKey: %s, Identifier: %s\n", scKey, starsComputer.Identifier)
			fmt.Printf("\tReceivedMessages: %v\n\n", starsComputer.RecievedMessages)

			fmt.Println("\tContainedPlans:")
			for sq, plan := range starsComputer.ContainedPlans {
				fmt.Printf("\t\tSquawk: %s, Callsign %v, Plan: %+v\n\n", sq, plan.Callsign, *plan)
			}

			fmt.Println("\tTrackInformation:")
			for sq, trackInfo := range starsComputer.TrackInformation {
				fmt.Printf("\tIdentifier: %s, TrackInfo:\n", sq)
				fmt.Printf("\t\tIdentifier: %+v\n", trackInfo.Identifier)
				fmt.Printf("\t\tOwner: %s\n", trackInfo.TrackOwner)
				fmt.Printf("\t\tHandoffController: %s\n", trackInfo.HandoffController)
				if trackInfo.FlightPlan != nil {
					fmt.Printf("\t\tFlightPlan: %+v\n\n", *trackInfo.FlightPlan)
				} else {
					fmt.Printf("\t\tFlightPlan: nil\n\n")
				}
			}

			if starsComputer.ERAMInbox != nil {
				fmt.Printf("\tERAMInbox: %v\n", *starsComputer.ERAMInbox)
			}

		}

		fmt.Println("ERAMInboxes:")
		for eiKey, inbox := range eramComputer.ERAMInboxes {
			fmt.Printf("\tKey: %s, Messages: %v\n\n", eiKey, *inbox)
		}

		if eramComputer.ReceivedMessages != nil {
			fmt.Printf("ReceivedMessages: %v\n\n", *eramComputer.ReceivedMessages)
		}

		fmt.Println("FlightPlans:")
		for sq, plan := range eramComputer.FlightPlans {
			fmt.Printf("\tSquawk: %s, Plan: %+v\n\n", sq, *plan)
		}

		fmt.Println("TrackInformation:")
		for sq, trackInfo := range eramComputer.TrackInformation {
			fmt.Printf("\tIdentifier: %s, TrackInfo:\n", sq)
			fmt.Printf("\t\tIdentifier: %+v\n", trackInfo.Identifier)
			fmt.Printf("\t\tOwner: %s\n", trackInfo.TrackOwner)
			fmt.Printf("\t\tHandoffController: %s\n", trackInfo.HandoffController)
			if trackInfo.FlightPlan != nil {
				fmt.Printf("\t\tFlightPlan: %+v\n\n", *trackInfo.FlightPlan)
			} else {
				fmt.Printf("\t\tFlightPlan: nil\n\n")
			}

		}
	}
}
