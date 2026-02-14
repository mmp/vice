// sim/nas_messages.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
)

// NASMessageType identifies the type of inter-facility NAS message.
type NASMessageType string

const (
	// Flight Data messages
	MsgFP NASMessageType = "FP" // Flight Plan
	MsgAM NASMessageType = "AM" // Amendment
	MsgCX NASMessageType = "CX" // Cancellation (FP won't enter facility)
	MsgRF NASMessageType = "RF" // Request Flight Plan
	MsgDM NASMessageType = "DM" // Departure Message
	MsgTB NASMessageType = "TB" // Beacon Terminate (FP no longer exists)

	// Track Data messages
	MsgTI NASMessageType = "TI" // Initiate Transfer (handoff offer)
	MsgTM NASMessageType = "TM" // Initiate Transfer (alternate)
	MsgTN NASMessageType = "TN" // Accept Transfer
	MsgTL NASMessageType = "TL" // Recall Transfer
	MsgTU NASMessageType = "TU" // Track Update
	MsgTZ NASMessageType = "TZ" // Track/Full Data Block Information
	MsgTS NASMessageType = "TS" // Transfer Secondary Radar Targets
	MsgTP NASMessageType = "TP" // Transfer Primary Radar Targets

	// Response messages
	MsgDA NASMessageType = "DA" // Acceptance
	MsgDR NASMessageType = "DR" // Rejection
)

// RejectReason identifies why a message was rejected.
type RejectReason int

const (
	RejectNoFlightPlan    RejectReason = iota // No flight plan exists at receiver
	RejectNoTrack                             // No track exists at receiver
	RejectInvalidFacility                     // Invalid or unreachable facility
	RejectDuplicate                           // Duplicate message/request
)

// NASMessage is the unit of communication between facility computers.
// Messages are appended to the receiving facility's Inbox and processed
// during the next simulation tick.
type NASMessage struct {
	Type         NASMessageType
	FromFacility string // Sending facility code
	ToFacility   string // Receiving facility code
	ACID         ACID
	Timestamp    time.Time // SimTime when sent

	// Payload fields (not all populated for every message type):
	FlightPlan *NASFlightPlan  // FP/AM: the flight plan data
	Controller ControlPosition // TI: offered controller; DA: resolved controller; TN: accepting controller
	EntryFix   string          // TI: entry fix at receiver
	ExitFix    string          // TI: exit fix
	Altitude   int             // TI: assigned altitude for fix-pair matching
	Reject     RejectReason    // DR: reason for rejection
}

// ProcessInbox handles all pending messages for this STARS computer.
// Called once per sim-second from updateState().
func (sc *STARSComputer) ProcessInbox(net *NASNetwork, simTime time.Time, lg *slog.Logger) {
	for _, msg := range sc.Inbox {
		switch msg.Type {
		case MsgFP:
			sc.handleFP(net, msg, simTime, lg)
		case MsgAM:
			sc.handleAM(net, msg, lg)
		case MsgCX:
			sc.handleCX(net, msg, lg)
		case MsgRF:
			sc.handleRF(net, msg, simTime, lg)
		case MsgDM:
			sc.handleDM(net, msg, lg)
		case MsgTB:
			sc.handleTB(net, msg, lg)
		case MsgTI:
			sc.handleTI(net, msg, simTime, lg)
		case MsgTN:
			sc.handleTN(net, msg, lg)
		case MsgTL:
			sc.handleTL(net, msg, lg)
		case MsgDA:
			sc.handleDA(net, msg, lg)
		case MsgDR:
			sc.handleDR(net, msg, lg)
		case MsgTU, MsgTZ, MsgTS, MsgTP, MsgTM:
			// TODO: placeholder message types
			lg.Debug("received unhandled NAS message", slog.String("type", string(msg.Type)),
				slog.String("acid", string(msg.ACID)), slog.String("from", msg.FromFacility))
		}
	}

	// Save processed messages for debug GUI history before clearing
	for _, msg := range sc.Inbox {
		sc.RecentInbox = append(sc.RecentInbox, TimestampedMessage{Msg: msg, ReceivedAt: simTime})
	}
	sc.Inbox = sc.Inbox[:0]

	// Prune messages older than 5 seconds
	cutoff := simTime.Add(-5 * time.Second)
	sc.RecentInbox = slices.DeleteFunc(sc.RecentInbox, func(tm TimestampedMessage) bool {
		return tm.ReceivedAt.Before(cutoff)
	})
}

// ProcessInbox handles all pending messages for this ERAM computer.
func (ec *ERAMComputer) ProcessInbox(net *NASNetwork, simTime time.Time, lg *slog.Logger) {
	for _, msg := range ec.Inbox {
		switch msg.Type {
		case MsgFP:
			ec.handleFP(net, msg, simTime, lg)
		case MsgAM:
			ec.handleAM(net, msg, lg)
		case MsgCX:
			ec.handleCX(net, msg, lg)
		case MsgRF:
			ec.handleRF(net, msg, simTime, lg)
		case MsgDM:
			ec.handleDM(net, msg, lg)
		case MsgTB:
			ec.handleTB(net, msg, lg)
		case MsgTI:
			ec.handleTI(net, msg, simTime, lg)
		case MsgTN:
			ec.handleTN(net, msg, lg)
		case MsgTL:
			ec.handleTL(net, msg, lg)
		case MsgDA:
			ec.handleDA(net, msg, lg)
		case MsgDR:
			ec.handleDR(net, msg, lg)
		case MsgTU, MsgTZ, MsgTS, MsgTP, MsgTM:
			lg.Debug("received unhandled NAS message", slog.String("type", string(msg.Type)),
				slog.String("acid", string(msg.ACID)), slog.String("from", msg.FromFacility))
		}
	}

	// Save processed messages for debug GUI history before clearing
	for _, msg := range ec.Inbox {
		ec.RecentInbox = append(ec.RecentInbox, TimestampedMessage{Msg: msg, ReceivedAt: simTime})
	}
	ec.Inbox = ec.Inbox[:0]

	// Prune messages older than 5 seconds
	cutoff := simTime.Add(-5 * time.Second)
	ec.RecentInbox = slices.DeleteFunc(ec.RecentInbox, func(tm TimestampedMessage) bool {
		return tm.ReceivedAt.Before(cutoff)
	})
}

// STARS message handlers

func (sc *STARSComputer) handleFP(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	if msg.FlightPlan == nil {
		return
	}

	if _, exists := sc.FlightPlans[msg.ACID]; exists {
		// Already have this FP - treat as amendment
		*sc.FlightPlans[msg.ACID] = *msg.FlightPlan
		lg.Debug("FP amended at STARS", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier))
		return
	}

	// Store the flight plan
	fp := *msg.FlightPlan // copy
	fp.ReceivedFrom = msg.FromFacility
	fp.ListIndex = sc.getListIndex()
	fp.PlanType = RemoteEnroute
	sc.FlightPlans[msg.ACID] = &fp

	lg.Debug("FP received at STARS", slog.String("acid", string(msg.ACID)),
		slog.String("facility", sc.Identifier), slog.String("from", msg.FromFacility))

	// Send DA back to sender
	sc.SendMessage(net, NASMessage{
		Type:      MsgDA,
		ToFacility: msg.FromFacility,
		ACID:      msg.ACID,
		Timestamp: simTime,
	})
}

func (sc *STARSComputer) handleTI(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	// Look up FP: first in unassociated pool, then in existing track
	// (already auto-acquired by squawk match).
	fp, hasFP := sc.FlightPlans[msg.ACID]
	if !hasFP {
		if track, hasTrack := sc.Tracks[msg.ACID]; hasTrack && track.CoupledFP != nil {
			fp = track.CoupledFP
			hasFP = true
		}
	}
	if !hasFP {
		lg.Debug("TI rejected: no flight plan", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier), slog.String("from", msg.FromFacility))
		sc.SendMessage(net, NASMessage{
			Type:      MsgDR,
			ToFacility: msg.FromFacility,
			ACID:      msg.ACID,
			Timestamp: simTime,
			Reject:    RejectNoFlightPlan,
		})
		return
	}

	// Resolve receiving controller via fix-pair routing
	resolvedController := sc.resolveControllerForHandoff(msg, fp)

	// Create/update track as "offered"
	sc.Tracks[msg.ACID] = &FacilityTrack{
		ACID:          msg.ACID,
		CoupledFP:     fp,
		Owner:         resolvedController,
		OwnerFacility: msg.FromFacility, // Still owned by sender until TN
		HandoffState:  TrackHandoffOffered,
	}

	lg.Debug("TI accepted, DA sent", slog.String("acid", string(msg.ACID)),
		slog.String("facility", sc.Identifier), slog.String("resolved_controller", string(resolvedController)))

	// Send DA with resolved controller
	sc.SendMessage(net, NASMessage{
		Type:       MsgDA,
		ToFacility: msg.FromFacility,
		ACID:       msg.ACID,
		Timestamp:  simTime,
		Controller: resolvedController,
	})
}

func (sc *STARSComputer) handleTN(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	// TN means the receiving facility accepted the handoff.
	// Update sender's local track: owner becomes remote controller.
	if track, ok := sc.Tracks[msg.ACID]; ok {
		track.Owner = msg.Controller
		track.OwnerFacility = msg.FromFacility
		track.HandoffState = TrackHandoffAccepted
		lg.Debug("TN received, track transferred", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier), slog.String("new_owner", string(msg.Controller)))
	}

	// Also clear HandoffController on the flight plan
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		fp.HandoffController = ""
	}
}

func (sc *STARSComputer) handleTL(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	// Recall transfer - cancel pending handoff
	if track, ok := sc.Tracks[msg.ACID]; ok && track.HandoffState == TrackHandoffOffered {
		delete(sc.Tracks, msg.ACID)
		lg.Debug("TL received, handoff recalled", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier))
	}
}

func (sc *STARSComputer) handleDA(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	// Acknowledgement from receiver - update local handoff state
	if track, ok := sc.Tracks[msg.ACID]; ok {
		if msg.Controller != "" {
			track.Owner = msg.Controller
		}
	}
	lg.Debug("DA received", slog.String("acid", string(msg.ACID)),
		slog.String("facility", sc.Identifier), slog.String("from", msg.FromFacility))
}

func (sc *STARSComputer) handleDR(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	// Rejection - cancel handoff attempt
	if track, ok := sc.Tracks[msg.ACID]; ok {
		track.HandoffState = TrackHandoffNone
	}
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		fp.HandoffController = ""
	}
	lg.Debug("DR received", slog.String("acid", string(msg.ACID)),
		slog.String("facility", sc.Identifier), slog.Int("reason", int(msg.Reject)))
}

func (sc *STARSComputer) handleAM(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if msg.FlightPlan == nil {
		return
	}
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		// Preserve local-only fields
		listIndex := fp.ListIndex
		planType := fp.PlanType
		*fp = *msg.FlightPlan
		fp.ListIndex = listIndex
		fp.PlanType = planType
		lg.Debug("AM received", slog.String("acid", string(msg.ACID)), slog.String("facility", sc.Identifier))
	}
}

func (sc *STARSComputer) handleCX(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		sc.returnListIndex(fp.ListIndex)
		delete(sc.FlightPlans, msg.ACID)
		delete(sc.Tracks, msg.ACID)
		lg.Debug("CX received, FP removed", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier))
	}
}

func (sc *STARSComputer) handleRF(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		sc.SendMessage(net, NASMessage{
			Type:       MsgFP,
			ToFacility: msg.FromFacility,
			ACID:       msg.ACID,
			Timestamp:  simTime,
			FlightPlan: fp,
		})
		lg.Debug("RF fulfilled", slog.String("acid", string(msg.ACID)), slog.String("facility", sc.Identifier))
	}
}

func (sc *STARSComputer) handleDM(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	lg.Debug("DM received", slog.String("acid", string(msg.ACID)), slog.String("facility", sc.Identifier))
}

func (sc *STARSComputer) handleTB(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if fp, ok := sc.FlightPlans[msg.ACID]; ok {
		sc.returnListIndex(fp.ListIndex)
		delete(sc.FlightPlans, msg.ACID)
		delete(sc.Tracks, msg.ACID)
		lg.Debug("TB received, FP cleaned up", slog.String("acid", string(msg.ACID)),
			slog.String("facility", sc.Identifier))
	}
}

// ERAM message handlers

func (ec *ERAMComputer) handleFP(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	if msg.FlightPlan == nil {
		return
	}

	if _, exists := ec.FlightPlans[msg.ACID]; exists {
		*ec.FlightPlans[msg.ACID] = *msg.FlightPlan
		lg.Debug("FP amended at ERAM", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
		return
	}

	fp := *msg.FlightPlan
	fp.ReceivedFrom = msg.FromFacility
	ec.FlightPlans[msg.ACID] = &fp

	lg.Debug("FP received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier), slog.String("from", msg.FromFacility))

	// Send DA back
	ec.SendMessage(net, NASMessage{
		Type:      MsgDA,
		ToFacility: msg.FromFacility,
		ACID:      msg.ACID,
		Timestamp: simTime,
	})
}

func (ec *ERAMComputer) handleTI(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	fp, hasFP := ec.FlightPlans[msg.ACID]
	if !hasFP {
		lg.Debug("TI rejected at ERAM: no flight plan", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
		ec.SendMessage(net, NASMessage{
			Type:      MsgDR,
			ToFacility: msg.FromFacility,
			ACID:      msg.ACID,
			Timestamp: simTime,
			Reject:    RejectNoFlightPlan,
		})
		return
	}

	ec.Tracks[msg.ACID] = &FacilityTrack{
		ACID:          msg.ACID,
		CoupledFP:     fp,
		Owner:         msg.Controller,
		OwnerFacility: msg.FromFacility,
		HandoffState:  TrackHandoffOffered,
	}

	lg.Debug("TI accepted at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))

	ec.SendMessage(net, NASMessage{
		Type:       MsgDA,
		ToFacility: msg.FromFacility,
		ACID:       msg.ACID,
		Timestamp:  simTime,
		Controller: msg.Controller,
	})
}

func (ec *ERAMComputer) handleTN(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if track, ok := ec.Tracks[msg.ACID]; ok {
		track.Owner = msg.Controller
		track.OwnerFacility = msg.FromFacility
		track.HandoffState = TrackHandoffAccepted
		lg.Debug("TN received at ERAM", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
	}
	if fp, ok := ec.FlightPlans[msg.ACID]; ok {
		fp.HandoffController = ""
	}
}

func (ec *ERAMComputer) handleTL(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if track, ok := ec.Tracks[msg.ACID]; ok && track.HandoffState == TrackHandoffOffered {
		delete(ec.Tracks, msg.ACID)
		lg.Debug("TL received at ERAM", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
	}
}

func (ec *ERAMComputer) handleDA(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if track, ok := ec.Tracks[msg.ACID]; ok {
		if msg.Controller != "" {
			track.Owner = msg.Controller
		}
	}
	lg.Debug("DA received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))
}

func (ec *ERAMComputer) handleDR(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if track, ok := ec.Tracks[msg.ACID]; ok {
		track.HandoffState = TrackHandoffNone
	}
	if fp, ok := ec.FlightPlans[msg.ACID]; ok {
		fp.HandoffController = ""
	}
	lg.Debug("DR received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))
}

func (ec *ERAMComputer) handleAM(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	if msg.FlightPlan == nil {
		return
	}
	if fp, ok := ec.FlightPlans[msg.ACID]; ok {
		*fp = *msg.FlightPlan
		lg.Debug("AM received at ERAM", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
	}
}

func (ec *ERAMComputer) handleCX(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	delete(ec.FlightPlans, msg.ACID)
	delete(ec.Tracks, msg.ACID)
	lg.Debug("CX received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))
}

func (ec *ERAMComputer) handleRF(net *NASNetwork, msg NASMessage, simTime time.Time, lg *slog.Logger) {
	if fp, ok := ec.FlightPlans[msg.ACID]; ok {
		ec.SendMessage(net, NASMessage{
			Type:       MsgFP,
			ToFacility: msg.FromFacility,
			ACID:       msg.ACID,
			Timestamp:  simTime,
			FlightPlan: fpForMessage(fp),
		})
		lg.Debug("RF fulfilled at ERAM", slog.String("acid", string(msg.ACID)),
			slog.String("facility", ec.Identifier))
	}
}

func (ec *ERAMComputer) handleDM(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	lg.Debug("DM received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))
}

func (ec *ERAMComputer) handleTB(net *NASNetwork, msg NASMessage, lg *slog.Logger) {
	delete(ec.FlightPlans, msg.ACID)
	delete(ec.Tracks, msg.ACID)
	lg.Debug("TB received at ERAM", slog.String("acid", string(msg.ACID)),
		slog.String("facility", ec.Identifier))
}

// resolveControllerForHandoff uses fix pairs and fix pair assignments to
// determine which controller at this STARS facility should receive the aircraft.
func (sc *STARSComputer) resolveControllerForHandoff(msg NASMessage, fp *NASFlightPlan) ControlPosition {
	// If the TI already specifies a controller, use it
	if msg.Controller != "" {
		return msg.Controller
	}

	// Determine flight type: geographic departure check.
	// For departures, NASFlightPlan.EntryFix is set to the departure airport code.
	// Check if the entry fix (airport) is within this TRACON's airport list.
	flightType := fp.TypeOfFlight
	for _, apt := range sc.Airports {
		if apt == fp.EntryFix {
			flightType = av.FlightTypeDeparture
			break
		}
	}

	// Use assigned altitude for fix-pair matching
	altitude := fp.AssignedAltitude

	// Match against fix pair definitions
	fpIdx, matched := MatchFixPair(sc.FixPairs, msg.EntryFix, msg.ExitFix, flightType, altitude)
	if matched {
		// Find the assignment for this fix pair index
		for _, fpa := range sc.FixPairAssignments {
			if fpa.FixPairIndex == fpIdx {
				return fpa.TCP
			}
		}
	}

	// Fallback: use InboundHandoffController from the FP if set
	if fp.InboundHandoffController != "" {
		return fp.InboundHandoffController
	}

	// Ultimate fallback: empty (caller should handle)
	return ""
}
