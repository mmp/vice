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
// idt
// receivedmessages
// adaptation
// starscomputers
// eraminboxes
// trackinfo
// stars:
// receivesmessages
// idt
// eraminbox
// unsupported
// starsinboxes
// trackinfo

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
	ERAMInboxes      map[string]*[]FlightPlanMessage
	ReceivedMessages *[]FlightPlanMessage
	FlightPlans      map[av.Squawk]*STARSFlightPlan
	TrackInformation map[string]*TrackInformation
	AvailableSquawks map[av.Squawk]interface{}
	Identifier       string
	Adaptation       av.ERAMAdaptation
}

func MakeERAMComputer(fac string, adapt av.ERAMAdaptation, starsBeaconBank int) *ERAMComputer {
	ec := &ERAMComputer{
		Adaptation:       adapt,
		STARSComputers:   make(map[string]*STARSComputer),
		ERAMInboxes:      make(map[string]*[]FlightPlanMessage),
		ReceivedMessages: &[]FlightPlanMessage{},
		FlightPlans:      make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation: make(map[string]*TrackInformation),
		AvailableSquawks: getValidSquawkCodes(),
		Identifier:       fac,
	}

	starsAvailableSquawks := getBeaconBankSquawks(starsBeaconBank)

	for id, tracon := range av.DB.TRACONs {
		if tracon.ARTCC == fac {
			sc := MakeSTARSComputer(id, starsAvailableSquawks)
			// make the ERAM inbox
			sc.ERAMInbox = ec.ReceivedMessages
			ec.STARSComputers[id] = sc
		}
	}

	return ec
}

func getValidSquawkCodes() map[av.Squawk]interface{} {
	sq := make(map[av.Squawk]interface{})

	for i := 0o1001; i <= 0o7777; i++ {
		// Skip SPCs and VFR
		if spc, _ := av.SquawkIsSPC(av.Squawk(i)); !spc && i != 0o1200 {
			sq[av.Squawk(i)] = nil
		}
	}
	return sq
}

func getBeaconBankSquawks(bank int) map[av.Squawk]interface{} {
	sq := make(map[av.Squawk]interface{})

	for i := bank*0o100 + 1; i <= bank*0o100+0o77; i++ {
		sq[av.Squawk(i)] = nil
	}
	return sq
}

// For NAS codes
func (comp *ERAMComputer) CreateSquawk() (av.Squawk, error) {
	// Pick an available one at random
	for sq := range comp.AvailableSquawks {
		delete(comp.AvailableSquawks, sq)
		return sq, nil
	}
	return av.Squawk(0), ErrNoMoreAvailableSquawkCodes
}

func (comp *ERAMComputer) SendFlightPlans(tracon string, simTime time.Time, lg *log.Logger) {
	// FIXME(mtrokel): does this need to remove plans from comp.FlightPlans
	// / comp.TrackInformation after sending them?
	return

	sendPlanIfReady := func(fp *STARSFlightPlan) {
		if simTime.Add(TransmitFPMessageTime).Before(fp.CoordinationTime.Time) {
			return
		}

		if coordFix, ok := comp.Adaptation.CoordinationFixes[fp.CoordinationFix]; !ok {
			lg.Errorf("%s: no coordination fix found for STARSFlightPlan CoordinationFix",
				fp.CoordinationFix)
		} else if adaptFix, err := coordFix.Fix(fp.Altitude); err != nil {
			lg.Errorf("%s @ %s", fp.CoordinationFix, fp.Altitude)
		} else if !slices.Contains(fp.ContainedFacilities, adaptFix.ToFacility) {
			comp.SendFlightPlan(fp, tracon, simTime)
		}
	}

	for _, info := range comp.TrackInformation {
		if fp := info.FlightPlan; fp != nil {
			if fp.Callsign == "" && fp.Altitude == "" {
				// FIXME(mtrokel): figure out why these are sneaking in here!
				delete(comp.TrackInformation, info.Identifier)
			} else {
				sendPlanIfReady(fp)
			}
		}
	}
	for _, fp := range comp.FlightPlans {
		sendPlanIfReady(fp)
	}
}

// For individual plans being sent.
func (comp *ERAMComputer) SendFlightPlan(fp *STARSFlightPlan, tracon string, simTime time.Time) error {
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
		fp.ContainedFacilities = append(fp.ContainedFacilities, adaptFix.ToFacility)
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
		starsFP.ContainedFacilities = []string{fix.ToFacility}
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

func (comp *ERAMComputer) SendMessageToERAM(facility string, msg FlightPlanMessage) error {
	if msg.MessageType == Unset {
		panic("unset message type")
	}

	if inbox, ok := comp.ERAMInboxes[facility]; !ok {
		return ErrUnknownFacility
	} else {
		*inbox = append(*inbox, msg)
		return nil
	}
}

func (comp *ERAMComputer) SortMessages(simTime time.Time, lg *log.Logger) {
	for _, msg := range *comp.ReceivedMessages {
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
					lg.Warnf("Coordination fix not found for route \"%s\", altitude \"%s",
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
			facility := msg.SourceID[:3] // Facility asking for FP
			// Find the flight plan
			plan, ok := comp.FlightPlans[msg.BCN]
			if ok {
				msg := FlightPlanDepartureMessage(*plan.FlightPlan, comp.Identifier, simTime)
				comp.SendMessageToSTARSFacility(facility, msg)
			}

			// FIXME: why is this here?
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
			comp.AvailableSquawks[msg.BCN] = nil

			for name, fixes := range comp.Adaptation.CoordinationFixes {
				alt := comp.TrackInformation[msg.Identifier].FlightPlan.Altitude
				if fix, err := fixes.Fix(alt); err != nil {
					lg.Warnf("Couldn't find adaptation fix: %v. Altitude \"%s\", Fixes %+v",
						err, alt, fixes)
				} else {
					if name == msg.CoordinationFix && fix.ToFacility != comp.Identifier { // Forward
						msg.SourceID = formatSourceID(comp.Identifier, simTime)
						if to := fix.ToFacility; len(to) > 0 && to[0] == 'Z' { // To another ARTCC
							comp.SendMessageToERAM(to, msg)
						} else { // To a TRACON
							comp.SendMessageToSTARSFacility(to, msg)
						}
					} else if name == msg.CoordinationFix && fix.ToFacility == comp.Identifier { // Stay here
						comp.TrackInformation[msg.Identifier] = &TrackInformation{
							TrackOwner:        msg.TrackOwner,
							HandoffController: msg.HandoffController,
							FlightPlan:        comp.FlightPlans[msg.BCN],
						}
					}
				}
			}

		case AcceptRecallTransfer:
			adaptationFixes, ok := comp.Adaptation.CoordinationFixes[msg.CoordinationFix]
			if !ok {
				lg.Warnf("%s: adaptation fixes not found for coordination fix",
					msg.CoordinationFix)
			} else {
				if info := comp.TrackInformation[msg.Identifier]; info != nil {
					// Recall message, we can free up this code now
					if msg.TrackOwner == info.TrackOwner {
						comp.AvailableSquawks[msg.BCN] = nil
					}
					info.TrackOwner = msg.TrackOwner
				}

				altitude := comp.TrackInformation[msg.Identifier].FlightPlan.Altitude
				if adaptationFix, err := adaptationFixes.Fix(altitude); err == nil {
					if adaptationFix.FromFacility != comp.Identifier {
						// Comes from a different ERAM facility
						comp.SendMessageToERAM(adaptationFix.FromFacility, msg)
					}
				}
			}
		}
	}

	clear(*comp.ReceivedMessages)
}

func (ec *ERAMComputer) FixForRouteAndAltitude(route string, altitude string) *av.AdaptationFix {
	return ec.Adaptation.FixForRouteAndAltitude(route, altitude)
}

func (ec *ERAMComputer) AdaptationFixForAltitude(fix string, altitude string) *av.AdaptationFix {
	return ec.Adaptation.AdaptationFixForAltitude(fix, altitude)
}

func (comp *ERAMComputer) InitiateTrack(callsign string, controller string, fp *STARSFlightPlan) error {
	if fp != nil { // FIXME: why is this nil?
		delete(comp.AvailableSquawks, fp.AssignedSquawk)
	}
	return nil
}

func (comp *ERAMComputer) HandoffTrack(ac *av.Aircraft, from, to *av.Controller, simTime time.Time) error {
	plan := comp.FlightPlans[ac.Squawk]
	if plan == nil {
		return av.ErrNoFlightPlan
	}
	msg := plan.Message()
	msg.SourceID = formatSourceID(from.Facility, simTime)
	msg.TrackInformation = TrackInformation{
		TrackOwner:        from.Callsign,
		HandoffController: to.Callsign,
		Identifier:        ac.Callsign,
	}
	msg.MessageType = InitiateTransfer

	if stars, ok := comp.STARSComputers[from.FacilityIdentifier]; ok { // in host ERAM
		comp.SendMessageToSTARSFacility(stars.Identifier, msg)
	} else { // needs to go through another ERAM
		// FIXME: need to get access to the full set of ERAM computers
		/*
			receivingERAM, _, err := comp.ERAMComputers.FacilityComputers(to.FacilityIdentifier)
			if err != nil {
				return err
			}
			comp.SendMessageToERAM(receivingERAM.Identifier, msg)
		*/
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

func (comp *ERAMComputer) CompletelyDeleteAircraft(ac *av.Aircraft) {
	for sq, trk := range comp.TrackInformation {
		if fp := trk.FlightPlan; fp != nil {
			if fp.Callsign == ac.Callsign {
				delete(comp.TrackInformation, sq)
			} else if fp.AssignedSquawk == ac.Squawk {
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
	ERAMInbox         *[]FlightPlanMessage            // The address of the overlying ERAM's message inbox.
	STARSInbox        map[string]*[]FlightPlanMessage // Other STARS Facilities' inboxes
	UnsupportedTracks []UnsupportedTrack
	AvailableSquawks  map[av.Squawk]interface{}
}

func MakeSTARSComputer(id string, sq map[av.Squawk]interface{}) *STARSComputer {
	return &STARSComputer{
		Identifier:       id,
		ContainedPlans:   make(map[av.Squawk]*STARSFlightPlan),
		TrackInformation: make(map[string]*TrackInformation),
		STARSInbox:       make(map[string]*[]FlightPlanMessage),
		AvailableSquawks: sq,
	}
}

// For local codes
func (comp *STARSComputer) CreateSquawk() (av.Squawk, error) {
	for sq := range comp.AvailableSquawks {
		delete(comp.AvailableSquawks, sq)
		return sq, nil
	}
	return av.Squawk(0), ErrNoMoreAvailableSquawkCodes
}

func (comp *STARSComputer) SendTrackInfo(receivingFacility string, msg FlightPlanMessage, simTime time.Time) {
	msg.SourceID = formatSourceID(comp.Identifier, simTime)
	if inbox := comp.STARSInbox[receivingFacility]; inbox != nil {
		*inbox = append(*inbox, msg)
	} else {
		comp.SendToOverlyingERAMFacility(msg)
	}
}

func formatSourceID(id string, t time.Time) string {
	return id + t.Format("1504Z")
}

func (comp *STARSComputer) SendToOverlyingERAMFacility(msg FlightPlanMessage) {
	*comp.ERAMInbox = append(*comp.ERAMInbox, msg)
}

func (comp *STARSComputer) RequestFlightPlan(bcn av.Squawk, simTime time.Time) {
	message := FlightPlanMessage{
		MessageType: RequestFlightPlan,
		BCN:         bcn,
		SourceID:    formatSourceID(comp.Identifier, simTime),
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

func (comp *STARSComputer) AddUnsupportedTrack(ut UnsupportedTrack) {
	comp.UnsupportedTracks = append(comp.UnsupportedTracks, ut)
}

func (comp *STARSComputer) AutoAssociateFP(ac *av.Aircraft, fp *STARSFlightPlan) {
	comp.AddTrackInformation(ac.Callsign, TrackInformation{
		TrackOwner:      ac.TrackingController, // Should happen initially, so ac.TrackingController can still be used
		FlightPlan:      fp,
		AutoAssociateFP: true,
	})
}

func (comp *STARSComputer) InitiateTrack(callsign string, controller string, fp *STARSFlightPlan, haveControl bool) error {
	if _, ok := comp.TrackInformation[callsign]; ok {
		return av.ErrOtherControllerHasTrack
	}

	trk := &TrackInformation{
		TrackOwner: controller,
		FlightPlan: fp,
	}
	if haveControl {
		trk.HandoffController = controller
	}
	comp.TrackInformation[callsign] = trk

	// TODO: shouldn't this be done earlier?
	if fp != nil { // FIXME: why is this nil?
		delete(comp.ContainedPlans, fp.AssignedSquawk)
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

		comp.TrackInformation[callsign] = &TrackInformation{
			TrackOwner:        from.Callsign,
			HandoffController: to.Callsign,
			FlightPlan:        trk.FlightPlan,
		}
	} else {
		trk.HandoffController = to.Callsign
	}
	return nil
}

func (comp *STARSComputer) HandoffControl(callsign string, nextController string) error {
	trk := comp.TrackInformation[callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	if trk.HandoffController != nextController {
		return fmt.Errorf("trk.HandoffController %s != nextController %s", trk.HandoffController,
			nextController)
	}

	trk.TrackOwner = nextController
	return nil
}

func (comp *STARSComputer) AcceptHandoff(ac *av.Aircraft, ctrl *av.Controller,
	controllers map[string]*av.Controller, adaptation STARSFacilityAdaptation, simTime time.Time) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil {
		return av.ErrNoAircraftForCallsign
	}

	if octrl := controllers[trk.TrackOwner]; octrl != nil && octrl.FacilityIdentifier != "" { // inter-facility
		fp := comp.ContainedPlans[ac.Squawk]
		if fp == nil {
			fp = trk.FlightPlan
		}

		msg := fp.Message()
		msg.SourceID = formatSourceID(ctrl.Callsign, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner: ctrl.Callsign,
		}
		msg.MessageType = AcceptRecallTransfer
		msg.Identifier = ac.Callsign

		if coordFixes, ok := adaptation.CoordinationFixes[fp.CoordinationFix]; ok {
			if coordFix, err := coordFixes.Fix(fp.Altitude); err == nil {
				if coordFix.FromFacility[0] == 'Z' {
					comp.SendToOverlyingERAMFacility(msg)
				} else {
					comp.SendTrackInfo(coordFix.FromFacility, msg, simTime)
				}
			}
		}
	}

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

	if ctrl := controllers[trk.TrackOwner]; ctrl != nil && ctrl.FacilityIdentifier != "" { // inter-facility
		// TODO: in other places where a *STARSFlightPlan is passed in, can
		// we look it up this way instead?
		msg := comp.ContainedPlans[ac.Squawk].Message()
		msg.SourceID = formatSourceID(trk.TrackOwner, simTime)
		msg.TrackInformation = TrackInformation{
			TrackOwner: trk.HandoffController,
		}
		msg.MessageType = AcceptRecallTransfer
		comp.SendTrackInfo(ctrl.FacilityIdentifier, msg, simTime)
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
		}
	} else {
		trk.HandoffController = octrl.Callsign
	}
	return nil
}

func (comp *STARSComputer) RedirectHandoff(ac *av.Aircraft, ctrl, octrl *av.Controller) error {
	trk := comp.TrackInformation[ac.Callsign]
	if trk == nil || trk.HandoffController == "" {
		return av.ErrNotBeingHandedOffToMe
	}

	// FIXME(mtrokel): ac.TrackingController
	trk.RedirectedHandoff.OriginalOwner = ac.TrackingController
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
				}

				delete(comp.ContainedPlans, msg.BCN)

				e.Post(Event{
					Type:         TransferAcceptedEvent,
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
	for sq, info := range comp.TrackInformation {
		if fp := info.FlightPlan; fp != nil {
			if fp.Callsign == ac.Callsign {
				delete(comp.TrackInformation, sq)
			} else if fp.AssignedSquawk == ac.Squawk {
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
	ContainedFacilities []string
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

func (fp *STARSFlightPlan) SetCoordinationFix(fa STARSFacilityAdaptation, ac *av.Aircraft, simTime time.Time) error {
	cf, ok := fa.GetCoordinationFix(fp, ac.Position(), ac.Waypoints())
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

	TrackInformation // For track messages
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
	AutoAssociateFP   bool // If it's white or not
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

func MakeERAMComputers(starsBeaconBank int, lg *log.Logger) ERAMComputers {
	ec := ERAMComputers{
		Computers: make(map[string]*ERAMComputer),
	}

	// Make the ERAM computer for each ARTCC that we have adaptations defined for.
	for fac, adapt := range av.DB.ERAMAdaptations {
		ec.Computers[fac] = MakeERAMComputer(fac, adapt, starsBeaconBank)
	}

	// Let each ERAM computer know about the other ARTCC ERAM computers'
	// inboxes.
	//
	// TODO: remove this, just look it up from ERAMComputers when we need
	// it.
	for fac, comp := range ec.Computers {
		for fac2, comp2 := range ec.Computers {
			// Don't add our own ERAM to the inbox.
			if fac != fac2 {
				comp.ERAMInboxes[fac2] = comp2.ReceivedMessages
			}
		}
	}

	allSTARSInboxes := make(map[string]*[]FlightPlanMessage)
	for _, eram := range ec.Computers {
		for _, stars := range eram.STARSComputers {
			allSTARSInboxes[stars.Identifier] = &stars.ReceivedMessages
		}
	}

	// Initialize STARSInbox in the STARSComputers; we store a pointer to
	// all other STARSComputers' inboxes in each STARSComputer.
	//
	// TODO: this also should probably be removed, to be looked up when
	// needed.
	for _, eram := range ec.Computers {
		for _, stars := range eram.STARSComputers {
			for tracon, address := range allSTARSInboxes {
				if tracon != stars.Identifier {
					stars.STARSInbox[tracon] = address
				}
			}
		}
	}

	return ERAMComputers(ec)
}

// If given an ARTCC, returns the corresponding ERAMComputer; if given a TRACON,
// returns both the associated ERMANComputer and STARSComputer
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
// messages. Messages will send when the time is appropriate (e.g.,
// handoff).  Some messages will be sent from recieved messages (for
// example a FP message from a RF message).
func (ec *ERAMComputers) Update(tracon string, simTime time.Time, e *EventStream, lg *log.Logger) {
	// _, fac := w.FacilityComputers(FIXME)
	// Sort through messages made
	for _, comp := range ec.Computers {
		comp.SortMessages(simTime, lg)
		comp.SendFlightPlans(tracon, simTime, lg)
		for _, stars := range comp.STARSComputers {
			stars.SortReceivedMessages(e)
		}
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
	if err := starsFP.SetCoordinationFix(fa, ac, simTime); err != nil {
		return err
	}

	artcc, stars, err := ec.FacilityComputers(facility)
	if err != nil {
		return err
	}

	sq, err := artcc.CreateSquawk()
	if err != nil {
		return err
	}

	ac.FlightPlan.AssignedSquawk = sq
	ac.Squawk = sq

	artcc.AddFlightPlan(starsFP)

	trk := TrackInformation{
		TrackOwner: ac.TrackingController,
		FlightPlan: starsFP,
		Identifier: ac.Callsign,
	}

	if artcc != nil {
		artcc.AddTrackInformation(ac.Callsign, trk)
	} else {
		stars.AddTrackInformation(ac.Callsign, trk)
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

	stars.TrackInformation[callsign].SP1 = scratchpad
	return nil
}
func (ec *ERAMComputers) SetSecondaryScratchpad(callsign, facility, scratchpad string) error {
	_, stars, err := ec.FacilityComputers(facility)
	if err != nil {
		return err
	}

	stars.TrackInformation[callsign].SP2 = scratchpad
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
		if strings.HasPrefix(field, STARSTriangleCharacter) && len(field) > 3 && len(field) <= 5 || (len(field) <= 6 && facilityAdaptation.AllowLongScratchpad[0]) { // See if it's specifying the SC1

			if slices.Contains(badScratchpads, field) {
				output.Error = ErrIllegalScratchpad
				return output
			}
			if util.IsAllNumbers(field[len(field)-3:]) {
				output.Error = ErrIllegalScratchpad
			}
			output.SC1 = field
		}
		if strings.HasPrefix(field, "+") && len(field) > 2 && (len(field) <= 4 || (len(field) <= 5 && facilityAdaptation.AllowLongScratchpad[1])) { // See if it's specifying the SC1
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
