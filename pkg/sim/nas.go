// pkg/sim/nas.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

// TODO:
/*
General:
Wait for the receiving system to send an accept/ ok message for things like
handoffs and accept handoffs. For example, N90 sends a handoff to PHL, PHL doesn't
have the flight plan, so it automatically rejects the handoff message and sends a
reject message to N90. N90 will not update the trk.TrackController unless PHL sends a
okay message.

ERAM:
idt
STARS:
idt
unsupported tracks
*/

// Message types sent from either ERAM or STARS
const (
	Unset             = iota // Have this so we can catch unset types.
	Plan                     // Both STARS & ERAM send this.
	Amendment                // ERAM (STARS?)
	Cancellation             // ERAM (STARS?)
	RequestFlightPlan        // STARS
	DepartureDM              // STARS
	BeaconTerminate          // STARS

	// Track Data

	InitiateTransfer     // When handoff gets sent. Sends the flightplan, contains track location
	AcceptRecallTransfer // Accept/ recall handoff
	// updated track coordinates. If off by some amount that is unacceptable, you'd see "AMB" in STARS datatag.
	// If no target is even close with same beacon code on the receiving STARS system, you'd see "NAT".

	// TODO:
	// Track Data
	// Test
	// Response
)

type ERAMComputer struct {
	STARSComputers   map[string]*STARSComputer
	ReceivedMessages []FlightPlanMessage
	FlightPlans      map[av.Squawk]*STARSFlightPlan
	TrackInformation map[string]*TrackInformation
	SquawkCodePool   *av.SquawkCodePool
	// This is shared among all STARS computers for our facility; we keep a
	// copy in ERAMComputer so that when we deserialize after loading a
	// saved sim, we are still sharing the same one.
	STARSCodePool *av.SquawkCodePool
	Identifier    string
	Adaptation    av.ERAMAdaptation

	eramComputers *ERAMComputers // do not include when we serialize
}

func MakeERAMComputer(fac string, adapt av.ERAMAdaptation, starsAdapt STARSFacilityAdaptation, eramComputers *ERAMComputers) *ERAMComputer {
	starsBeaconBank := starsAdapt.BeaconBank
	ec := &ERAMComputer{
		Adaptation:       adapt,
		STARSComputers:   make(map[string]*STARSComputer),
		FlightPlans:      make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation: make(map[string]*TrackInformation),
		SquawkCodePool:   av.MakeCompleteSquawkCodePool(),
		STARSCodePool:    av.MakeSquawkBankCodePool(starsBeaconBank),
		Identifier:       fac,
		eramComputers:    eramComputers,
	}

	for id, tracon := range av.DB.TRACONs {
		if tracon.ARTCC == fac {
			sc := MakeSTARSComputer(id, ec.STARSCodePool)
			sc.ERAMID = ec.Adaptation.FacilityIDs[sc.Identifier]
			sc.ERAMInbox = &ec.ReceivedMessages
			sc.Adaptation = starsAdapt
			ec.STARSComputers[id] = sc
		}
	}
	return ec
}

func (comp *ERAMComputer) Activate(ec *ERAMComputers) {
	comp.eramComputers = ec

	// When a sim is saved, we lose the fact that the STARSComputers all
	// share the same SquawkCodePool; so we will reestablish that now from
	// the copy saved in ERAMComputer.
	for _, sc := range comp.STARSComputers {
		sc.Activate(comp.STARSCodePool)
	}
}

// For NAS codes
func (comp *ERAMComputer) CreateSquawk() (av.Squawk, error) {
	return comp.SquawkCodePool.Get()
}

func (comp *ERAMComputer) SendFlightPlans(tracon string, simTime time.Time, lg *log.Logger, aircaft map[string]*av.Aircraft) {
	// FIXME(mtrokel): does this need to remove plans from comp.FlightPlans
	// / comp.TrackInformation after sending them?

	sendPlanIfReady := func(ac *av.Aircraft, fp *STARSFlightPlan) {
		if simTime.Add(TransmitFPMessageTime).Before(fp.CoordinationTime.Time) {
			return
		}

		if coordFix, ok := comp.Adaptation.CoordinationFixes[fp.CoordinationFix]; !ok {
			// lg.Errorf("%s: no coordination fix found for %v/%v CoordinationFix",
			// fp.CoordinationFix, fp.Callsign, fp.AssignedSquawk)
		} else if _, err := coordFix.Fix(fp.Altitude); err != nil {
			lg.Errorf("%s @ %s", fp.CoordinationFix, fp.Altitude)
		} else if !fp.Sent {
			comp.SendFlightPlan(fp, tracon, simTime, ac)
		}
	}

	for _, info := range comp.TrackInformation {
		if fp := info.FlightPlan; fp != nil {
			if fp.Callsign == "" && fp.Altitude == "" {
				// FIXME(mtrokel): figure out why these are sneaking in here!
				delete(comp.TrackInformation, info.Identifier)
			} else {
				ac := aircaft[info.Identifier]
				sendPlanIfReady(ac, fp)
			}
		}
	}
	for _, fp := range comp.FlightPlans {
		sendPlanIfReady(nil, fp) 
	}
}

// For individual plans being sent.
func (comp *ERAMComputer) SendFlightPlan(fp *STARSFlightPlan, tracon string, simTime time.Time, ac *av.Aircraft) error {
	msg := fp.Message()
	msg.MessageType = Plan
	msg.SourceID = formatSourceID(comp.Identifier, simTime)

	if coordFix, ok := comp.Adaptation.CoordinationFixes[fp.CoordinationFix]; !ok {
		return av.ErrNoMatchingFix
	} else if adaptFix, err := coordFix.Fix(fp.Altitude); err != nil {
		return err
	} else {
		// TODO: change tracon to the fix pair assignment (this will be in the adaptation)
		err := comp.SendMessageToSTARSFacility(tracon, msg)
		if err != nil {
			comp.SendMessageToERAM(av.DB.TRACONs[tracon].ARTCC, msg)
		}
		fp.Sent =  true 

		// If the previous facility was not a ERAM facility, find a new coordination fix

		if _, ok := av.DB.ARTCCs[adaptFix.ToFacility]; !ok {
			copy := *fp
			
			// Convert the route to waypoints.
			copy.Route = strings.TrimPrefix(copy.Route, "/. ")
			rte, err := av.ParseWaypoints(copy.Route)
			if err != nil {
				return nil
			}
			slicedRoute := rte.RouteString()
			idx := strings.Index(slicedRoute, adaptFix.Name)
			if idx + len(adaptFix.Name) + 1 > len(fp.Route) { // Last fix in the route
				return nil 
			}
			slicedRoute = slicedRoute[idx + len(adaptFix.Name) + 1:]
			slicedRoute = strings.TrimSpace(slicedRoute)

			copy.Route = slicedRoute
			fix := comp.FixForRouteAndAltitude(copy.Route, copy.Altitude)
			if fix == nil {
				return nil
			}
			fp.CoordinationFix = fix.Name
			// Assign a coordination time for the new fix
			var distanceToFix, timeToFix float32
			if ac == nil {
				// Find the distance between the previous fix and the new fix, then use the filled cruise speed to calculate the time to the new fix
				route := strings.Fields(slicedRoute)
				prevFix := adaptFix.Name
				for _, fix := range route {
					pos1, ok := av.DB.LookupWaypoint(prevFix)
					if !ok {
						return nil
					}
					pos2, ok := av.DB.LookupWaypoint(fix)
					if !ok {
						return nil
					}
					distanceToFix += math.NMDistance2LL(pos1, pos2)
					timeToFix = distanceToFix / float32(fp.CruiseSpeed) * 60 // No TransmitFPMessage time here because we're taking the distance of the current coordination fix (which in theory should be TransmitFPMessage minutes away from the fix),
					// so that time is already accounted for.
				}
			} else {
				distanceToFix, err = ac.Nav.DistanceAlongRoute(fp.CoordinationFix)
				if err != nil {
				}
				timeToFix = distanceToFix / float32(ac.FlightPlan.CruiseSpeed) * 60
				timeToFix -= float32(TransmitFPMessageTime)
			}
			
			
			
			fp.CoordinationTime.Time = simTime.Add(time.Duration(timeToFix * float32(time.Minute)))
		}

		return nil
	}
}

func (comp *ERAMComputer) AddFlightPlan(plan *STARSFlightPlan) {
	comp.FlightPlans[plan.FlightPlan.AssignedSquawk] = plan
}

func (comp *ERAMComputer) AddTrackInformation(callsign string, trk TrackInformation) {
	comp.TrackInformation[callsign] = &trk
}

func (comp *ERAMComputer) AddDeparture(fp *av.FlightPlan, tracon string, simTime time.Time) {
	starsFP := MakeSTARSFlightPlan(fp)

	if fix := comp.Adaptation.FixForRouteAndAltitude(starsFP.Route, starsFP.Altitude); fix != nil {
		msg := starsFP.Message()
		msg.SourceID = formatSourceID(comp.Identifier, simTime)
		msg.MessageType = Plan
		comp.SendMessageToERAM(fix.ToFacility, msg)

		starsFP.CoordinationFix = fix.Name
		starsFP.Sent = true 
	}

	comp.AddFlightPlan(starsFP)
	comp.SendMessageToSTARSFacility(tracon, FlightPlanDepartureMessage(*fp, comp.Identifier, simTime))
}

// Sends a message, whether that be a flight plan or any other message type to a STARS computer.
// The STARS computer will sort messages by itself
func (comp *ERAMComputer) SendMessageToSTARSFacility(facility string, msg FlightPlanMessage) error {
	if msg.MessageType == Unset {
		panic("unset message type")
	}

	if stars, ok := comp.STARSComputers[facility]; !ok {
		return ErrUnknownFacility
	} else {
		stars.ReceivedMessages = append(stars.ReceivedMessages, msg)
		return nil
	}
}

func (comp *ERAMComputer) Update(s *Sim) {
	comp.SortMessages(s.SimTime, s.lg)
	comp.SendFlightPlans(s.State.TRACON, s.SimTime, s.lg, s.State.Aircraft)

	for _, stars := range comp.STARSComputers {
		stars.Update(s)
	}
}

func (comp *ERAMComputer) SendMessageToERAM(facility string, msg FlightPlanMessage) error {
	if msg.MessageType == Unset {
		panic("unset message type")
	}

	if facERAM, ok := comp.eramComputers.Computers[facility]; !ok {
		return ErrUnknownFacility
	} else {
		facERAM.ReceivedMessages = append(facERAM.ReceivedMessages, msg)
		return nil
	}
}

func (comp *ERAMComputer) SortMessages(simTime time.Time, lg *log.Logger) {
	for _, msg := range comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			fp := msg.FlightPlan()

			if fp.AssignedSquawk == av.Squawk(0) {
				// TODO: Figure out why it's sending a blank fp
				//panic("zero squawk")
				break
			}

			// Ensure comp.FlightPlans[msg.BCN] is initialized
			comp.FlightPlans[msg.BCN] = fp

			if fp.CoordinationFix == "" {
				if fix := comp.FixForRouteAndAltitude(fp.Route, fp.Altitude); fix != nil {
					fp.CoordinationFix = fix.Name
				} else {
					lg.Warnf("Coordination fix not found for route %q, altitude \"%s",
						fp.Route, fp.Altitude)
					continue
				}
			}

			// Check if another facility needs this plan.
			if af := comp.AdaptationFixForAltitude(fp.CoordinationFix, fp.Altitude); af != nil {
				if af.ToFacility != comp.Identifier {
					// Send the plan to the STARS facility that needs it.
					comp.SendMessageToSTARSFacility(af.ToFacility, msg)
				}
			}

		case RequestFlightPlan:
			facility := msg.RequestedFacility // Facility asking for FP
			// Find the flight plan
			err := comp.RequestFP(msg.Identifier, facility)
			if err != nil {
				comp.SendMessageToSTARSFacility(facility, FlightPlanMessage{
					MessageType: Plan,
					Error:       err,
				})
			}

		case DepartureDM: // Stars ERAM coordination time tracking

		case BeaconTerminate: // TODO: Find out what this does

		case InitiateTransfer:
			// Save the track information and forward it to another facility in necessary
			comp.TrackInformation[msg.Identifier] = &TrackInformation{
				TrackOwner:        msg.TrackOwner,
				HandoffController: msg.HandoffController,
				FlightPlan:        comp.FlightPlans[msg.BCN],
				Identifier:        msg.Identifier,
			}
			if des := msg.FacilityDestination; des != comp.Identifier { // Going to another facility

				// ERAM cannot send to a STARS facility that isn't in the same ARTCC, so first check if the facility is in this ARTCC
				if _, ok := comp.STARSComputers[des]; ok {
					comp.SendMessageToSTARSFacility(des, msg)
				} else { // Forward to another ERAM facility
					// find the overlying ARTCC
					if _, ok := av.DB.ARTCCs[des]; !ok {
						des = av.DB.TRACONs[des].ARTCC
					}

					comp.SendMessageToERAM(des, msg)
				}
			}

		case AcceptRecallTransfer:
			// Find if it's an accept or recall message
			if trk := comp.TrackInformation[msg.Identifier]; trk.TrackOwner != msg.TrackOwner { // Accept message
				trk.TrackOwner = msg.TrackOwner
				trk.HandoffController = ""
			} else { // Recall message, just delete the track info
				delete(comp.TrackInformation, msg.Identifier)
			}
			// Figure out if this needs to be forwarded to another facility
			if des := msg.FacilityDestination; des != comp.Identifier { // Going to another facility
				if _, ok := comp.STARSComputers[des]; ok {
					comp.SendMessageToSTARSFacility(des, msg)
				} else { // Forward to another ERAM facility
					// find the overlying ARTCC
					if _, ok := av.DB.ARTCCs[des]; !ok {
						des = av.DB.TRACONs[des].ARTCC
					}
					comp.SendMessageToERAM(des, msg)
				}
			}
		}
	}
	clear(comp.ReceivedMessages)
}

func (ec *ERAMComputer) FixForRouteAndAltitude(route string, altitude string) *av.AdaptationFix {
	return ec.Adaptation.FixForRouteAndAltitude(route, altitude)
}

func (ec *ERAMComputer) AdaptationFixForAltitude(fix string, altitude string) *av.AdaptationFix {
	return ec.Adaptation.AdaptationFixForAltitude(fix, altitude)
}

func (comp *ERAMComputer) InitiateTrack(callsign string, controller string, fp *STARSFlightPlan) error {
	if fp != nil { // FIXME: why is this nil?
		comp.SquawkCodePool.Claim(fp.AssignedSquawk)
	}
	return nil
}

func (comp *ERAMComputer) HandoffTrack(ac *av.Aircraft, from, to *av.Controller, simTime time.Time) error {
	plan := comp.FlightPlans[ac.Squawk]
	if plan == nil {
		return av.ErrNoFlightPlan
	}
	if from.Facility == to.Facility { // intra-facility
		trk := comp.TrackInformation[ac.Callsign]
		trk.HandoffController = to.Callsign
	} else { // inter-facility
		msg := plan.Message()
		msg.SourceID = formatSourceID(from.Facility, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner:        from.Callsign,
			HandoffController: to.Callsign,
			Identifier:        ac.Callsign,
		}
		msg.MessageType = InitiateTransfer

		comp.TrackInformation[ac.Callsign].HandoffController = to.Callsign
		msg.FacilityDestination = to.Facility

		if stars, ok := comp.STARSComputers[msg.FacilityDestination]; ok { // in host ERAM
			comp.SendMessageToSTARSFacility(stars.Identifier, msg)
		} else { // needs to go through another ERAM
			var nextFacility string
			if receivingARTCC, ok := av.DB.ARTCCs[msg.FacilityDestination]; !ok {
				nextFacility = av.DB.TRACONs[msg.FacilityDestination].ARTCC
			} else {
				nextFacility = receivingARTCC.Name
			}
			receivingERAM, ok := comp.eramComputers.Computers[nextFacility]
			if !ok {
				return av.ErrInvalidController
			}
			comp.SendMessageToERAM(receivingERAM.Identifier, msg)

		}
	}

	return nil
}

func (comp *ERAMComputer) DropTrack(ac *av.Aircraft) error {
	if trk := comp.TrackInformation[ac.Callsign]; trk != nil {
		delete(comp.FlightPlans, trk.FlightPlan.AssignedSquawk)
		delete(comp.TrackInformation, ac.Callsign)
	}
	return nil
}

func (comp *ERAMComputer) RequestFP(identifier, receivingFaciility string) error {
	receivingFaciility, ok := comp.Adaptation.FacilityIDs[receivingFaciility]
	if !ok {
		return av.ErrNoSTARSFacility
	}
	if _, ok := comp.STARSComputers[receivingFaciility]; !ok {
		return av.ErrNoSTARSFacility
	}
	if sq, err := av.ParseSquawk(identifier); err == nil {
		if fp, ok := comp.FlightPlans[sq]; ok {
			msg := fp.Message()
			msg.SourceID = formatSourceID(comp.Identifier, time.Now())
			msg.FacilityDestination = receivingFaciility
			msg.MessageType = Plan
			comp.SendMessageToSTARSFacility(receivingFaciility, msg)
		} else {
			return av.ErrNoFlightPlan
		}
	} else {
		for _, plan := range comp.FlightPlans {
			if plan.ECID == identifier {
				msg := plan.Message()
				msg.SourceID = formatSourceID(comp.Identifier, time.Now())
				msg.FacilityDestination = receivingFaciility
				msg.MessageType = Plan
				comp.SendMessageToSTARSFacility(receivingFaciility, msg)
			} else {
				return av.ErrNoFlightPlan // Change this to the accurate error code. (and find out what it is)
			}
		}
	}
	return nil
}

func (comp *ERAMComputer) CompletelyDeleteAircraft(ac *av.Aircraft) {
	// Delete their code from the code bank

	for sq, trk := range comp.TrackInformation {
		if fp := trk.FlightPlan; fp != nil {
			if fp.Callsign == ac.Callsign {
				if comp.SquawkCodePool.IsAssigned(fp.AssignedSquawk) {
					comp.SquawkCodePool.Unassign(fp.AssignedSquawk)
				}
				delete(comp.TrackInformation, sq)
			} else if fp.AssignedSquawk == ac.Squawk {
				if comp.SquawkCodePool.IsAssigned(fp.AssignedSquawk) {
					comp.SquawkCodePool.Unassign(fp.AssignedSquawk)
				}
				delete(comp.TrackInformation, sq)
			}
		}
	}

	for _, stars := range comp.STARSComputers {
		stars.CompletelyDeleteAircraft(ac)
	}
}

type ERAMComputers struct {
	Computers map[string]*ERAMComputer
}

type ERAMTrackInfo struct {
	Location          math.Point2LL
	Owner             string
	HandoffController string
}

const TransmitFPMessageTime = 30 * time.Minute

type STARSComputer struct {
	Identifier        string
	ContainedPlans    map[av.Squawk]*STARSFlightPlan
	ReceivedMessages  []FlightPlanMessage
	TrackInformation  map[string]*TrackInformation
	ERAMInbox         *[]FlightPlanMessage // The address of the overlying ERAM's message inbox.
	UnsupportedTracks map[string]*UnsupportedTrack
	SquawkCodePool    *av.SquawkCodePool
	HoldForRelease    []*av.Aircraft
	Adaptation STARSFacilityAdaptation
	ERAMID	string // How ERAM identifies this facility
}

func MakeSTARSComputer(id string, sq *av.SquawkCodePool) *STARSComputer {
	return &STARSComputer{
		Identifier:       id,
		ContainedPlans:   make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation: make(map[string]*TrackInformation),
		UnsupportedTracks: make(map[string]*UnsupportedTrack),
		SquawkCodePool:   sq,
	}
}

func (comp *STARSComputer) Activate(pool *av.SquawkCodePool) {
	comp.SquawkCodePool = pool
}

// For local codes
func (comp *STARSComputer) CreateSquawk() (av.Squawk, error) {
	return comp.SquawkCodePool.Get()
}

// Send inter-faciliy track info
func (comp *STARSComputer) SendTrackInfo(receivingFacility string, msg FlightPlanMessage, simTime time.Time) {
	msg.SourceID = formatSourceID(comp.Identifier, simTime)
	msg.FacilityDestination = receivingFacility
	comp.SendToOverlyingERAMFacility(msg)
}

func formatSourceID(id string, t time.Time) string {
	return id + t.Format("1504Z")
}

func (comp *STARSComputer) SendToOverlyingERAMFacility(msg FlightPlanMessage) {
	// FIXME(mtrokel): this crashes on a handoff to an adjacent facility
	*comp.ERAMInbox = append(*comp.ERAMInbox, msg)
}

func (comp *STARSComputer) RequestFlightPlan(bcn av.Squawk, simTime time.Time, requestedFacility string) {
	if requestedFacility == "" {
		requestedFacility = comp.Identifier
	}
	message := FlightPlanMessage{
		MessageType:       RequestFlightPlan,
		BCN:               bcn,
		SourceID:          formatSourceID(comp.Identifier, simTime),
		RequestedFacility: requestedFacility,
	}
	comp.SendToOverlyingERAMFacility(message)
}

func (comp *STARSComputer) GetFlightPlan(identifier string) (*STARSFlightPlan, error) {
	if squawk, err := av.ParseSquawk(identifier); err == nil {
		// Squawk code was entered
		if fp, ok := comp.ContainedPlans[squawk]; ok {
			// The flight plan is stored in the system
			return fp, nil
		}
	} else {
		// See if it matches a callsign we know about
		for _, plan := range comp.ContainedPlans {
			if plan.Callsign == identifier { // We have this plan in our system
				return plan, nil
			}
		}
	}
	return nil, ErrNoMatchingFlight
}

func (comp *STARSComputer) AddFlightPlan(plan *STARSFlightPlan) {
	comp.ContainedPlans[plan.FlightPlan.AssignedSquawk] = plan
}

func (comp *STARSComputer) AddTrackInformation(callsign string, info TrackInformation) {
	comp.TrackInformation[callsign] = &info
}

func (comp *STARSComputer) AddUnsupportedTrack(ut *UnsupportedTrack) {
	comp.UnsupportedTracks[ut.FlightPlan.Callsign] = ut
}

func (comp *STARSComputer) ChangeUnsupportedTrack(ut *UnsupportedTrack) {
	comp.UnsupportedTracks[ut.FlightPlan.Callsign] = ut
}

func (comp *STARSComputer) DropUnsupportedTrack(callsign string) {
	delete(comp.UnsupportedTracks, callsign)
}

func (comp *STARSComputer) HandoffUnsupportedTrack(callsign, handoffController string) error {
	if ut, ok := comp.UnsupportedTracks[callsign]; ok {
		if ut.HandoffController != "" {
			return av.ErrInvalidController // What error here?
		}
	}
	comp.UnsupportedTracks[callsign].HandoffController = handoffController
	return nil 
}

func (comp *STARSComputer) AcceptUnsupportedHandoff(callsign, handoffController string) {
	comp.UnsupportedTracks[callsign].Owner = handoffController
	comp.UnsupportedTracks[callsign].HandoffController = ""
}

func (comp *STARSComputer) CancelUnsupportedHandoff(callsign  string) {
	comp.UnsupportedTracks[callsign].HandoffController = ""
}

func (comp *STARSComputer) UnsupportedScratchpad(callsign, sp  string, secondary bool ) {
	comp.UnsupportedTracks[callsign].HandoffController = ""
}

func (comp *STARSComputer) LookupTrackIndex(idx int) *TrackInformation {
	if idx >= len(comp.TrackInformation) {
		return nil
	}

	// FIXME: this is assigning indices alphabetically; I think they are
	// supposed to be done first-come first-served.
	k := util.SortedMapKeys(comp.TrackInformation)
	return comp.TrackInformation[k[idx]]
}

// This should be facility-defined in the json file, but for now it's 2nm
// near their departure airport.
func InAcquisitionArea(ac *av.Aircraft) bool {
	if InDropArea(ac) {
		return false
	}

	for _, icao := range []string{ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport} {
		ap := av.DB.Airports[icao]
		if math.NMDistance2LL(ap.Location, ac.Position()) <= 4 &&
			ac.Altitude() <= float32(ap.Elevation+500) {
			return true
		}
	}
	return false
}

func InDropArea(ac *av.Aircraft) bool {
	for _, icao := range []string{ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport} {
		ap := av.DB.Airports[icao]
		if math.NMDistance2LL(ap.Location, ac.Position()) <= 2 &&
			ac.Altitude() <= float32(ap.Elevation+100) {
			return true
		}
	}

	return false
}

func (comp *STARSComputer) InitiateTrack(callsign string, controller string, fp *STARSFlightPlan, haveControl bool) error {
	if _, ok := comp.TrackInformation[callsign]; ok {
		return av.ErrOtherControllerHasTrack
	}

	trk := &TrackInformation{
		TrackOwner: controller,
		FlightPlan: fp,
		Identifier: callsign,
	}

	comp.TrackInformation[callsign] = trk

	// TODO: shouldn't this be done earlier?
	if fp != nil { // FIXME: why is this nil?
		delete(comp.ContainedPlans, fp.AssignedSquawk)
	}

	// Remove it from the released departures list
	idx := slices.IndexFunc(comp.HoldForRelease, func(ac *av.Aircraft) bool { return ac.Callsign == callsign })
	if idx != -1 {
		comp.HoldForRelease = append(comp.HoldForRelease[:idx], comp.HoldForRelease[idx+1:]...)
	}

	return nil
}

func (comp *STARSComputer) DropTrack(ac *av.Aircraft) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	delete(comp.ContainedPlans, ac.Squawk)
	delete(comp.TrackInformation, ac.Callsign)

	return nil
}

func (comp *STARSComputer) HandoffTrack(callsign string, from *av.Controller, to *av.Controller, simTime time.Time) error {
	if comp == nil || from == nil || to == nil {
		return nil
	}
	trk := comp.TrackInformation[callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	if to.Facility != from.Facility { // inter-facility
		msg := trk.FlightPlan.Message()
		msg.SourceID = formatSourceID(from.Callsign, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner:        from.Callsign,
			HandoffController: to.Callsign,
			Identifier:        callsign,
		}
		msg.Identifier = callsign
		msg.MessageType = InitiateTransfer
		comp.SendTrackInfo(to.Facility, msg, simTime)
	}

	trk.HandoffController = to.Callsign

	return nil
}

func (comp *STARSComputer) AcceptHandoff(ac *av.Aircraft, ctrl *av.Controller,
	controllers map[string]*av.Controller, simTime time.Time) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	if octrl := controllers[trk.TrackOwner]; octrl != nil && octrl.Facility != ctrl.Facility { // inter-facility
		fp := trk.FlightPlan
		if fp == nil {
			fp = comp.ContainedPlans[ac.Squawk]
		}

		msg := fp.Message()
		msg.SourceID = formatSourceID(ctrl.Callsign, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner: ctrl.Callsign,
			FlightPlan: trk.FlightPlan,
		}
		msg.MessageType = AcceptRecallTransfer
		msg.Identifier = ac.Callsign

		comp.SendTrackInfo(octrl.Facility, msg, simTime)
	}

	// Change it locally reguardless
	trk.HandoffController = ""
	trk.TrackOwner = ctrl.Callsign
	return nil
}

func (comp *STARSComputer) AutomatedAcceptHandoff(ac *av.Aircraft, controller string,
	receivingSTARS *STARSComputer, controllers map[string]*av.Controller, simTime time.Time) error {
	// TODO: can this be unified with AcceptHandoff() above?
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	if ctrl := controllers[trk.TrackOwner]; ctrl != nil && comp.Adaptation.FacilityIDs[ctrl.Facility] != "" { // inter-facility
		// TODO: in other places where a *STARSFlightPlan is passed in, can
		// we look it up this way instead?
		msg := comp.ContainedPlans[ac.Squawk].Message()
		msg.SourceID = formatSourceID(trk.TrackOwner, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner: trk.HandoffController,
		}
		msg.MessageType = AcceptRecallTransfer
		comp.SendTrackInfo(comp.Adaptation.FacilityIDs[ctrl.Facility], msg, simTime)
	} else {
		// TODO(mtrokel): AcceptHandoff() always does this, but the code
		// for automated handoffs has it under an else clause. Intentional?
		trk.TrackOwner = trk.HandoffController
		trk.HandoffController = ""
	}
	return nil
}

func (comp *STARSComputer) CancelHandoff(ac *av.Aircraft, ctrl *av.Controller,
	controllers map[string]*av.Controller, simTime time.Time) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNotBeingHandedOffToMe
	}

	octrl := controllers[trk.HandoffController]
	if octrl == nil {
		return av.ErrInvalidController
	}

	if octrl.Facility != ctrl.Facility { // inter-facility
		msg := trk.FlightPlan.Message()
		msg.SourceID = formatSourceID(ctrl.Callsign, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner: ctrl.Callsign,
			Identifier: ac.Callsign,
		}
		msg.Identifier = ac.Callsign
		msg.MessageType = InitiateTransfer
		comp.SendTrackInfo(octrl.Facility, msg, simTime)

		comp.TrackInformation[ac.Callsign] = &TrackInformation{
			TrackOwner: ctrl.Callsign,
			FlightPlan: trk.FlightPlan,
			Identifier: ac.Callsign,
		}
	} else {
		trk.HandoffController = ""
	}
	return nil
}

func (comp *STARSComputer) RedirectHandoff(ac *av.Aircraft, ctrl, octrl *av.Controller) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNotBeingHandedOffToMe
	}

	trk.RedirectedHandoff.OriginalOwner = trk.TrackOwner
	if trk.RedirectedHandoff.ShouldFallbackToHandoff(ctrl.Callsign, octrl.Callsign) {
		trk.HandoffController = trk.RedirectedHandoff.Redirector[0]
		trk.RedirectedHandoff = av.RedirectedHandoff{}
	} else {
		trk.RedirectedHandoff.AddRedirector(ctrl)
		trk.RedirectedHandoff.RedirectedTo = octrl.Callsign
	}
	return nil
}

func (comp *STARSComputer) AcceptRedirectedHandoff(ac *av.Aircraft, ctrl *av.Controller) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNotBeingHandedOffToMe
	}

	if trk.RedirectedHandoff.RedirectedTo == ctrl.Callsign { // Accept
		trk.HandoffController = ""
		trk.TrackOwner = trk.RedirectedHandoff.RedirectedTo
		trk.RedirectedHandoff = av.RedirectedHandoff{}
	} else if trk.RedirectedHandoff.GetLastRedirector() == ctrl.Callsign { // Recall (only the last redirector is able to recall)
		if n := len(trk.RedirectedHandoff.Redirector); n > 1 { // Multiple redirected handoff, recall & still show "RD"
			trk.RedirectedHandoff.RedirectedTo = trk.RedirectedHandoff.Redirector[n-1]
		} else { // One redirect took place, clear the RD and show it as a normal handoff
			trk.HandoffController = trk.RedirectedHandoff.Redirector[n-1]
			trk.RedirectedHandoff = av.RedirectedHandoff{}
		}
	}
	return nil
}

func (comp *STARSComputer) PointOut(callsign, toController string) error {
	trk := comp.TrackInformation[callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNoAircraftForCallsign
	}

	trk.PointOut = toController
	return nil
}

func (comp *STARSComputer) AcknowledgePointOut(callsign, controller string) error {
	trk := comp.TrackInformation[callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNoAircraftForCallsign
	}

	trk.PointOut = ""
	// FIXME: we should be storing TCP IDs not callsigns
	if len(trk.PointOutHistory) < 20 {
		trk.PointOutHistory = append([]string{controller}, trk.PointOutHistory...)
	} else {
		trk.PointOutHistory = trk.PointOutHistory[:19]
		trk.PointOutHistory = append([]string{controller}, trk.PointOutHistory...)
	}
	return nil
}

func (comp *STARSComputer) RejectPointOut(callsign, controller string) error {
	trk := comp.TrackInformation[callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNoAircraftForCallsign
	}

	// TODO(mtrokel): what needs to be done here, if anything?
	return nil
}

func (comp *STARSComputer) ReleaseDeparture(callsign string) error {
	idx := slices.IndexFunc(comp.HoldForRelease, func(ac *av.Aircraft) bool { return ac.Callsign == callsign })
	if idx == -1 {
		return av.ErrNoAircraftForCallsign
	}
	if comp.HoldForRelease[idx].Released {
		return ErrAircraftAlreadyReleased
	} else {
		comp.HoldForRelease[idx].Released = true
		return nil
	}
}

func (comp *STARSComputer) GetReleaseDepartures() []*av.Aircraft {
	return comp.HoldForRelease
}

func (comp *STARSComputer) AddHeldDeparture(ac *av.Aircraft) {
	comp.HoldForRelease = append(comp.HoldForRelease, ac)
}

func (comp *STARSComputer) Update(s *Sim) {
	comp.SortReceivedMessages(s.eventStream)
}

// Sorting the STARS messages. This will store flight plans with FP
// messages, change flight plans with AM messages, cancel flight plans with
// CX messages, etc.
func (comp *STARSComputer) SortReceivedMessages(e *EventStream) {
	for _, msg := range comp.ReceivedMessages {
		switch msg.MessageType {
		case Plan:
			if msg.BCN != av.Squawk(0) {
				comp.ContainedPlans[msg.BCN] = msg.FlightPlan()
			}

		case Amendment:
			comp.ContainedPlans[msg.BCN] = msg.FlightPlan()

		case Cancellation: // Deletes the flight plan from the computer
			delete(comp.ContainedPlans, msg.BCN)

		case InitiateTransfer:
			// 1. Store the data comp.trackinfo. We now know who's tracking
			// the plane. Use the squawk to get the plan.
			if fp := comp.ContainedPlans[msg.BCN]; fp != nil { // We have the plan
				comp.TrackInformation[msg.Identifier] = &TrackInformation{
					TrackOwner:        msg.TrackOwner,
					HandoffController: msg.HandoffController,
					FlightPlan:        fp,
					Identifier:        msg.Identifier,
				}
				delete(comp.ContainedPlans, msg.BCN)

				e.Post(Event{
					Type:         TransferAcceptedEvent, // Should this be an InitTransfer event?
					Callsign:     msg.Identifier,
					ToController: msg.TrackOwner,
				})
			} else {
				if trk := comp.TrackInformation[msg.Identifier]; trk != nil {
					comp.TrackInformation[msg.Identifier] = &TrackInformation{
						TrackOwner:        msg.TrackOwner,
						HandoffController: msg.HandoffController,
						FlightPlan:        trk.FlightPlan,
						Identifier:        msg.Identifier,
					}

					delete(comp.ContainedPlans, msg.BCN)

					e.Post(Event{
						Type:         TransferAcceptedEvent,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				} else { // send an IF msg
					e.Post(Event{
						Type:         TransferRejectedEvent,
						Callsign:     msg.Identifier,
						ToController: msg.TrackOwner,
					})
				}

			}

		case AcceptRecallTransfer:
			// - When we send an accept message, we set the track ownership to us.
			// - When we receive an accept message, we change the track
			//   ownership to the receiving controller.
			// - When we send a recall message, we tell our system to stop the flashing.
			// - When we receive a recall message, we keep the plan and if
			//   we click the track, it is no longer able to be accepted
			//
			// We can infer whether its a recall/ accept by the track ownership that gets sent back.
			info := comp.TrackInformation[msg.Identifier]
			if info == nil {
				break
			}

			if msg.TrackOwner != info.TrackOwner {
				// It has to be an accept message. (We initiated the handoff here)
				info.TrackOwner = msg.TrackOwner
				info.HandoffController = ""
			} else {
				// It has to be a recall message. (we received the handoff)
				delete(comp.TrackInformation, msg.Identifier)
			}
		}
	}

	clear(comp.ReceivedMessages)
}

func (comp *STARSComputer) CompletelyDeleteAircraft(ac *av.Aircraft) {
	comp.HoldForRelease = util.FilterSlice(comp.HoldForRelease,
		func(a *av.Aircraft) bool { return ac.Callsign != a.Callsign })

	for sq, info := range comp.TrackInformation {
		if fp := info.FlightPlan; fp != nil {
			if fp.Callsign == ac.Callsign {
				if comp.SquawkCodePool.IsAssigned(fp.AssignedSquawk) {
					comp.SquawkCodePool.Unassign(fp.AssignedSquawk)
				}
				delete(comp.TrackInformation, sq)
			} else if fp.AssignedSquawk == ac.Squawk {
				if comp.SquawkCodePool.IsAssigned(fp.AssignedSquawk) {
					comp.SquawkCodePool.Unassign(fp.AssignedSquawk)
				}
				delete(comp.TrackInformation, sq)
			}
		}
	}
}

type STARSFlightPlan struct {
	*av.FlightPlan
	FlightPlanType      int
	CoordinationTime    CoordinationTime
	CoordinationFix     string
	Sent bool 
	Altitude            string
	SP1                 string
	SP2                 string
	InitialController   string // For abbreviated FPs
}

// Flight plan types (STARS)
const (
	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute = iota

	// Flight plan received from an adjacent terminal facility This is a
	// flight plan that has been sent over by another STARS facility.
	RemoteNonEnroute

	// VFR interfacility flight plan entered locally for which the NAS
	// ARTCC has not returned a flight plan This is a flight plan that is
	// made by a STARS facility that gets a NAS code.
	LocalEnroute

	// Flight plan entered by TCW or flight plan from an adjacent terminal
	// that has been handed off to this STARS facility This is a flight
	// plan that is made at a STARS facility and gets a local code.
	LocalNonEnroute
)

func MakeSTARSFlightPlan(fp *av.FlightPlan) *STARSFlightPlan {
	return &STARSFlightPlan{
		FlightPlan: fp,
		Altitude:   fmt.Sprint(fp.Altitude),
	}
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
		FlightID:         fp.ECID + fp.Callsign,
		CoordinationFix:  fp.CoordinationFix,
		CoordinationTime: fp.CoordinationTime,
	}
}

func (fp *STARSFlightPlan) SetCoordinationFix(fa av.ERAMAdaptation, ac *av.Aircraft, simTime time.Time) error {
	cf, ok := GetCoordinationFix(fa, fp, ac.Position(), ac.Waypoints())
	if !ok {
		return ErrNoCoordinationFix
	}
	fp.CoordinationFix = cf

	if dist, err := ac.DistanceAlongRoute(cf); err == nil {
		m := dist / float32(fp.CruiseSpeed) * 60
		fp.CoordinationTime = CoordinationTime{
			Time: simTime.Add(time.Duration(m * float32(time.Minute))),
		}
	} else { // zone based fixes.
		loc, ok := av.DB.LookupWaypoint(fp.CoordinationFix)
		if !ok {
			return ErrNoCoordinationFix
		}

		dist := math.NMDistance2LL(ac.Position(), loc)
		m := dist / float32(fp.CruiseSpeed) * 60
		fp.CoordinationTime = CoordinationTime{
			Time: simTime.Add(time.Duration(m * float32(time.Minute))),
		}
	}
	return nil
}

type FlightPlanMessage struct {
	SourceID         string // LLLdddd e.g. ZCN2034 (ZNY at 2034z)
	MessageType      int
	FlightID         string // ddaLa(a)(a)(a)(a)(a)ECID (3 chars start w/ digit), Aircraft ID (2-7 chars start with letter)
	AircraftData     AircraftDataMessage
	BCN              av.Squawk
	CoordinationFix  string
	CoordinationTime CoordinationTime

	// Altitude will either be requested (cruise altitude) for departures,
	// or the assigned altitude for arrivals.  ERAM has the ability to
	// assign interm alts (and is used much more than STARS interm alts)
	// with `QQ`.  This interim altiude gets sent down to the STARS
	// computer instead of the cruising altitude. If no interim altitude is
	// set, use the cruise altitude (check this) Examples of altitudes
	// could be 310, VFR/170, VFR, 170B210 (block altitude), etc.
	Altitude string
	Route    string

	FacilityDestination string // For InitiateTransfer or AcceptRecallTransfer messages
	// that are inter-facility handoffs so that the ERAM computer knows where to send the message to.

	TrackInformation // For track messages

	RequestedFacility string // For RequestFacility messages that don't go to the same facility.
	Error             error  // For error messages
}

type TrackInformation struct {
	Identifier        string
	TrackOwner        string
	HandoffController string
	FlightPlan        *STARSFlightPlan
	PointOut          string
	PointOutHistory   []string
	RedirectedHandoff av.RedirectedHandoff
	SP1               string
	SP2               string
	TempAltitude      int
	AutoAssociateFP   bool
}

func (trk TrackInformation) String(sq string) string {

	str := fmt.Sprintf("\tIdentifier: %s, TrackInfo:\n", sq)
	str = str + fmt.Sprintf("\t\tIdentifier: %+v\n", trk.Identifier)
	str = str + fmt.Sprintf("\t\tOwner: %s\n", trk.TrackOwner)
	str = str + fmt.Sprintf("\t\tHandoffController: %s\n", trk.HandoffController)
	if trk.FlightPlan != nil {
		str = str + fmt.Sprintf("\t\tFlightPlan: %+v\n\n", *trk.FlightPlan)
	} else {
		str = str + "\t\tFlightPlan: nil\n\n"
	}
	return str
}

func (trk TrackInformation) HandingOffTo(ctrl string) bool {
	return (trk.HandoffController == ctrl && // handing off to them
		!slices.Contains(trk.RedirectedHandoff.Redirector, ctrl)) || // not a redirector
		trk.RedirectedHandoff.RedirectedTo == ctrl // redirected to
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
	ACID                string
	BCN                 av.Squawk
	ControllingPosition string
	TypeOfFlight        string // Figure out this
	SC1                 string
	SC2                 string
	AircraftType        string
	RequestedALT        string
	Rules               av.FlightRules
	DepartureAirport    string // Specified with type of flight (maybe)

	// TODO: why is there an error stored here (vs just returned from the
	// parsing function)?
	Error error
}

type UnsupportedTrack struct {
	TrackLocation     math.Point2LL
	Owner             string
	HandoffController string
	FlightPlan        *STARSFlightPlan
}

func MakeERAMComputers(starsAdapt STARSFacilityAdaptation, lg *log.Logger) *ERAMComputers {
	ec := &ERAMComputers{
		Computers: make(map[string]*ERAMComputer),
	}
	// Make the ERAM computer for each ARTCC that we have adaptations defined for.
	for fac, adapt := range av.DB.ERAMAdaptations {
		ec.Computers[fac] = MakeERAMComputer(fac, adapt, starsAdapt, ec)
	}

	return ec
}

func (ec *ERAMComputers) Activate() {
	for artcc := range ec.Computers {
		ec.Computers[artcc].Activate(ec)
	}
}

// If given an ARTCC, returns the corresponding ERAMComputer; if given a TRACON,
// returns both the associated ERAMComputer and STARSComputer
func (ec *ERAMComputers) FacilityComputers(fac string) (*ERAMComputer, *STARSComputer, error) {
	if ec, ok := ec.Computers[fac]; ok {
		// fac is an ARTCC
		return ec, nil, nil
	}

	tracon, ok := av.DB.TRACONs[fac]
	if !ok {
		return nil, nil, ErrUnknownFacility
	}

	eram, ok := ec.Computers[tracon.ARTCC]
	if !ok {
		// This shouldn't happen...
		panic("no ERAM computer found for " + tracon.ARTCC + " from TRACON " + fac)
	}

	stars, ok := eram.STARSComputers[fac]
	if !ok {
		// This also shouldn't happen...
		panic("no STARS computer found for " + fac)
	}

	return eram, stars, nil
}

// Give the computers a chance to sort through their received
// messages and do assorted housekeeping.
func (ec ERAMComputers) Update(s *Sim) {
	for _, comp := range ec.Computers {
		comp.Update(s)
	}
}

// identifier can be bcn or callsign
func (ec ERAMComputers) GetSTARSFlightPlan(tracon string, identifier string) (*STARSFlightPlan, error) {
	_, starsComputer, err := ec.FacilityComputers(tracon)
	if err != nil {
		return nil, err
	}

	return starsComputer.GetFlightPlan(identifier)
}

func (ec *ERAMComputers) AddArrival(ac *av.Aircraft, facility string, fa STARSFacilityAdaptation, simTime time.Time) error {
	starsFP := MakeSTARSFlightPlan(ac.FlightPlan)

	artcc, stars, err := ec.FacilityComputers(facility)
	if err != nil {
		return err
	}

	if err := starsFP.SetCoordinationFix(artcc.Adaptation, ac, simTime); err != nil {
		return err
	}

	
	sq, err := artcc.CreateSquawk()
	if err != nil {
		return err
	}

	ac.FlightPlan.AssignedSquawk = sq
	ac.Squawk = sq
	starsFP.AssignedSquawk = sq

	artcc.AddFlightPlan(starsFP)

	trk := TrackInformation{
		TrackOwner: ac.TrackingController,
		FlightPlan: starsFP,
		Identifier: ac.Callsign,
	}

	if stars != nil {
		stars.AddTrackInformation(ac.Callsign, trk)
	} else {
		artcc.AddTrackInformation(ac.Callsign, trk)
	}
	return nil
}

func (ec *ERAMComputers) CompletelyDeleteAircraft(ac *av.Aircraft) {
	// TODO: update these FPs
	for _, eram := range ec.Computers {
		eram.CompletelyDeleteAircraft(ac)
	}
}

func (ec *ERAMComputers) HandoffTrack(ac *av.Aircraft, from, to string, controllers map[string]*av.Controller, simTime time.Time) error {
	fromCtrl, toCtrl := controllers[from], controllers[to]
	if fromCtrl == nil || toCtrl == nil {
		return av.ErrInvalidController
	}

	eram, stars, err := ec.FacilityComputers(fromCtrl.Facility)
	if err != nil {
		return err
	}

	if stars != nil {
		return stars.HandoffTrack(ac.Callsign, fromCtrl, toCtrl, simTime)
	} else {
		return eram.HandoffTrack(ac, fromCtrl, toCtrl, simTime)
	}
}

func (ec *ERAMComputers) SetScratchpad(callsign, facility, scratchpad string) error {
	_, stars, err := ec.FacilityComputers(facility)
	if err != nil {
		return err
	}

	if trk := stars.TrackInformation[callsign]; trk != nil {
		trk.SP1 = scratchpad
	}
	return nil
}
func (ec *ERAMComputers) SetSecondaryScratchpad(callsign, facility, scratchpad string) error {
	_, stars, err := ec.FacilityComputers(facility)
	if err != nil {
		return err
	}

	if trk := stars.TrackInformation[callsign]; trk != nil {
		trk.SP2 = scratchpad
	}
	return nil
}

// For debugging purposes
func (e ERAMComputers) DumpMap() {
	for key, eramComputer := range e.Computers {
		allowedFacilities := []string{"ZNY", "ZDC", "ZBW"} // Just so the console doesn't get flodded with empty ARTCCs (I debug with EWR)
		if !slices.Contains(allowedFacilities, key) {
			continue
		}
		fmt.Printf("Key: %s\n", key)
		fmt.Printf("Identifier: %s\n", eramComputer.Identifier)

		fmt.Println("STARSComputers:")
		for scKey, starsComputer := range eramComputer.STARSComputers {
			fmt.Printf("\tKey: %s, Identifier: %s\n", scKey, starsComputer.Identifier)
			fmt.Printf("\tReceivedMessages: %v\n\n", starsComputer.ReceivedMessages)

			fmt.Println("\tContainedPlans:")
			for sq, plan := range starsComputer.ContainedPlans {
				fmt.Printf("\t\tSquawk: %s, Callsign %v, Plan: %+v\n\n", sq, plan.Callsign, *plan)
			}

			fmt.Println("\tTrackInformation:")
			for sq, trackInfo := range starsComputer.TrackInformation {
				fmt.Print(trackInfo.String(sq))
			}

			if starsComputer.ERAMInbox != nil {
				fmt.Printf("\tERAMInbox: %v\n", *starsComputer.ERAMInbox)
			}

		}

		if len(eramComputer.ReceivedMessages) > 0 {
			fmt.Printf("ReceivedMessages: %v\n\n", eramComputer.ReceivedMessages)
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

// Converts the message to a STARS flight plan.
func (s FlightPlanMessage) FlightPlan() *STARSFlightPlan {
	rules := av.FlightRules(util.Select(strings.Contains(s.Altitude, "VFR"), av.VFR, av.IFR))
	flightPlan := &STARSFlightPlan{
		FlightPlan: &av.FlightPlan{
			Rules:            rules,
			AircraftType:     s.AircraftData.AircraftType,
			AssignedSquawk:   s.BCN,
			DepartureAirport: s.AircraftData.DepartureLocation,
			ArrivalAirport:   s.AircraftData.ArrivalLocation,
			Route:            s.Route,
		},
		CoordinationFix:  s.CoordinationFix,
		CoordinationTime: s.CoordinationTime,
		Altitude:         s.Altitude,
	}

	if len(s.FlightID) > 3 {
		flightPlan.ECID = s.FlightID[:3]
		flightPlan.Callsign = s.FlightID[3:]
	}

	return flightPlan
}

// Prepare the message to sent to a STARS facility after a RF message
func FlightPlanDepartureMessage(fp av.FlightPlan, sendingFacility string, simTime time.Time) FlightPlanMessage {
	return FlightPlanMessage{
		SourceID:    formatSourceID(sendingFacility, simTime),
		MessageType: Plan,
		FlightID:    fp.ECID + fp.Callsign,
		AircraftData: AircraftDataMessage{
			DepartureLocation: fp.DepartureAirport,
			ArrivalLocation:   fp.ArrivalAirport,
			NumberOfAircraft:  1, // One for now.
			AircraftType:      fp.TypeWithoutSuffix(),
			AircraftCategory:  fp.AircraftType, // TODO: Use a method to turn this into an aircraft category
			Equipment:         strings.TrimPrefix(fp.AircraftType, fp.TypeWithoutSuffix()),
		},
		BCN:             fp.AssignedSquawk,
		CoordinationFix: fp.Exit,
		Altitude:        util.Select(fp.Rules == av.VFR, "VFR/", "") + strconv.Itoa(fp.Altitude),
		Route:           fp.Route,
	}
}

func MakeSTARSFlightPlanFromAbbreviated(abbr string, stars *STARSComputer, facilityAdaptation STARSFacilityAdaptation) (*STARSFlightPlan, error) {
	if strings.Contains(abbr, "*") {
		// VFR FP; it's a required field
		// TODO(mtrokel)
		return nil, nil
	} else {
		// Abbreviated FP
		fields := strings.Fields(abbr)
		if len(fields) == 0 {
			return nil, ErrInvalidAbbreviatedFP
		}

		info := ParseAbbreviatedFPFields(facilityAdaptation, fields)
		if err := info.Error; err != nil && err != ErrIllegalACType {
			return nil, err
		} else {
			if info.BCN == av.Squawk(0) {
				var err error
				if info.BCN, err = stars.CreateSquawk(); err != nil {
					return nil, err
				}
			}

			fp := &STARSFlightPlan{ // TODO: Do single char ap parsing
				FlightPlan: &av.FlightPlan{
					Callsign:         info.ACID,
					Rules:            util.Select(info.Rules == av.UNKNOWN, av.VFR, info.Rules),
					AircraftType:     info.AircraftType,
					DepartureAirport: info.DepartureAirport,
					AssignedSquawk:   info.BCN,
				},
				Altitude:          util.Select(info.RequestedALT == "", "VFR", info.RequestedALT),
				SP1:               info.SC1,
				SP2:               info.SC1,
				InitialController: info.ControllingPosition,
			}
			return fp, err
		}
	}
}

// FIXME: yuck, duplicated here
const STARSTriangleCharacter = string(rune(0x80))

func ParseAbbreviatedFPFields(facilityAdaptation STARSFacilityAdaptation, fields []string) AbbreviatedFPFields {
	output := AbbreviatedFPFields{}
	if len(fields[0]) >= 2 && len(fields[0]) <= 7 && unicode.IsLetter(rune(fields[0][0])) {
		output.ACID = fields[0]
	} else {
		output.Error = ErrIllegalACID
		return output
	}

	for _, field := range fields[1:] { // fields[0] is always the ACID
		// See if it's a BCN
		if sq, err := av.ParseSquawk(field); err == nil {
			output.BCN = sq
			continue
		}

		// See if its specifying the controlling position
		if len(field) == 2 {
			output.ControllingPosition = field
			continue
		}

		// See if it's specifying the type of flight. No errors for this
		// because this could turn into a scratchpad.
		if len(field) <= 2 {
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
		if strings.HasPrefix(field, STARSTriangleCharacter) && len(field) > 3 && len(field) <= 5 || (len(field) <= 6 && facilityAdaptation.AllowLongScratchpad) { // See if it's specifying the SC1

			if slices.Contains(badScratchpads, field) {
				output.Error = ErrIllegalScratchpad
				return output
			}
			if util.IsAllNumbers(field[len(field)-3:]) {
				output.Error = ErrIllegalScratchpad
			}
			output.SC1 = field
		}
		if strings.HasPrefix(field, "+") && len(field) > 2 && (len(field) <= 4 || (len(field) <= 5 && facilityAdaptation.AllowLongScratchpad)) { // See if it's specifying the SC1
			if slices.Contains(badScratchpads, field) {
				output.Error = ErrIllegalScratchpad
				return output
			}
			if util.IsAllNumbers(field[len(field)-3:]) {
				output.Error = ErrIllegalScratchpad
			}
			output.SC2 = field
		}
		if acFields := strings.Split(field, "/"); len(field) >= 4 { // See if it's specifying the type of flight
			switch len(acFields) {
			case 1: // Just the AC Type
				if _, ok := av.DB.AircraftPerformance[field]; !ok { // AC doesn't exist
					output.Error = ErrIllegalACType
					continue
				} else {
					output.AircraftType = field
					continue
				}
			case 2: // Either a formation number with the ac type or a ac type with a equipment suffix
				if all := util.IsAllNumbers(acFields[0]); all { // Formation number
					if !unicode.IsLetter(rune(acFields[1][0])) {
						output.Error = ErrInvalidAbbreviatedFP
						return output
					}
					if _, ok := av.DB.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
						output.Error = ErrIllegalACType // This error is informational. Shouldn't end the entire function. Just this switch statement
						continue
					}
					output.AircraftType = field
				} else { // AC Type with equipment suffix
					if len(acFields[1]) > 1 || !util.IsAllLetters(acFields[1]) {
						output.Error = ErrInvalidAbbreviatedFP
						return output
					}
					if _, ok := av.DB.AircraftPerformance[acFields[0]]; !ok { // AC doesn't exist
						output.Error = ErrIllegalACType
						continue
					}
					output.AircraftType = field
				}
			case 3:
				if len(acFields[2]) > 1 || !util.IsAllLetters(acFields[2]) {
					output.Error = ErrInvalidAbbreviatedFP
					return output
				}
				if !unicode.IsLetter(rune(acFields[1][0])) {
					output.Error = ErrInvalidAbbreviatedFP
					return output
				}
				if _, ok := av.DB.AircraftPerformance[acFields[1]]; !ok { // AC doesn't exist
					output.Error = ErrIllegalACType
					break
				}
				output.AircraftType = field
			}
			continue
		}
		if len(field) == 3 && util.IsAllNumbers(field) {
			output.RequestedALT = field
			continue
		}
		if len(field) == 2 {
			if field[0] != '.' {
				output.Error = ErrInvalidAbbreviatedFP
				return output
			}
			switch field[1] {
			case 'V':
				output.Rules = av.VFR
			case 'P':
				output.Rules = av.VFR // vfr on top
			case 'E':
				output.Rules = av.IFR // enroute
			default:
				output.Error = ErrInvalidAbbreviatedFP
				return output
			}
		}

	}
	return output
}
