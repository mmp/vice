// sim/radio.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// postReadbackTransmission posts a radio event for a pilot responding to a command.
// DestinationTCW is the specific TCW that issued the command.
// Use this for readbacks, where the response must go to the issuing controller
// regardless of any consolidation changes.
func (s *Sim) postReadbackTransmission(from av.ADSBCallsign, tr av.RadioTransmission, tcw TCW) {
	tr.Validate(s.lg)

	if ac, ok := s.Aircraft[from]; ok {
		ac.LastRadioTransmission = s.State.SimTime
	}

	tcp := s.State.PrimaryPositionForTCW(tcw)
	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          from,
		ToController:          tcp,
		DestinationTCW:        tcw,
		WrittenText:           tr.Written(s.Rand),
		SpokenText:            tr.Spoken(s.Rand),
		RadioTransmissionType: tr.Type,
	})
}

///////////////////////////////////////////////////////////////////////////
// Deferred operations

// PendingTransmissionType identifies the type of pilot-initiated transmission.
type PendingTransmissionType int

const (
	PendingTransmissionDeparture                PendingTransmissionType = iota // Departure checking in
	PendingTransmissionArrival                                                 // Arrival/handoff checking in
	PendingTransmissionTrafficInSight                                          // "Traffic in sight" call
	PendingTransmissionFlightFollowingReq                                      // Abbreviated "VFR request"
	PendingTransmissionFlightFollowingFull                                     // Full flight following request
	PendingTransmissionGoAround                                                // Go-around announcement
	PendingTransmissionEmergency                                               // Emergency stage transmission
	PendingTransmissionRequestApproachClearance                                // Pilot requesting approach clearance
	PendingTransmissionFieldInSight                                            // Delayed "field in sight" after "looking"
	PendingTransmissionFieldNegativeContact                                    // "Negative contact" after looking timer expires
	PendingTransmissionRequestVisual                                           // Spontaneous "field in sight, requesting visual"
	PendingTransmissionRequestVectors                                          // Pilot requesting vectors (overshot localizer)
	PendingTransmissionRequestAltitude                                         // Pilot requesting altitude after being vectored off STAR
	PendingTransmissionTrafficWhere                                            // Pilot proactively asks "where's that traffic" after 30-60s of looking
)

// FutureFrequencyChange represents a pilot switching to a new frequency.
// Once the Time passes, the aircraft's ControllerFrequency is set and
// the entry is removed.
type FutureFrequencyChange struct {
	ADSBCallsign av.ADSBCallsign
	TCP          TCP
	Time         Time
}

// PendingContact represents a pilot-initiated transmission waiting to be played.
type PendingContact struct {
	ADSBCallsign           av.ADSBCallsign
	TCP                    TCP
	ReadyTime              Time                    // When pilot is ready to transmit
	Type                   PendingTransmissionType // What kind of transmission
	ReportDepartureHeading bool                    // For departures: include assigned heading
	HasQueuedEmergency     bool                    // For departures: trigger emergency after contact
	PrebuiltTransmission   *av.RadioTransmission   // For emergency transmissions: pre-built message
	FirstInFacility        bool                    // For arrivals: first contact in this TRACON facility
}

// hasPendingCheckIn reports whether the aircraft has a pending arrival or
// departure check-in that hasn't been transmitted yet.
func (s *Sim) hasPendingCheckIn(callsign av.ADSBCallsign) bool {
	for _, pcs := range s.PendingContacts {
		for _, pc := range pcs {
			if pc.ADSBCallsign == callsign &&
				(pc.Type == PendingTransmissionArrival || pc.Type == PendingTransmissionDeparture) {
				return true
			}
		}
	}
	return false
}

// addPendingContact adds an aircraft to the pending contacts queue for a controller.
func (s *Sim) addPendingContact(pc PendingContact) {
	if s.PendingContacts == nil {
		s.PendingContacts = make(map[TCP][]PendingContact)
	}
	s.PendingContacts[pc.TCP] = append(s.PendingContacts[pc.TCP], pc)
}

// cancelFutureFrequencyChange removes any pending frequency change for
// the given aircraft. Called when the aircraft's frequency is being managed
// directly (e.g., controller contact, radar services terminated) to prevent
// a stale queued switch from overwriting the new state.
func (s *Sim) cancelFutureFrequencyChange(callsign av.ADSBCallsign) {
	s.FutureFrequencyChanges = slices.DeleteFunc(s.FutureFrequencyChanges,
		func(ffc FutureFrequencyChange) bool {
			return ffc.ADSBCallsign == callsign
		})
}

// processFutureFrequencyChanges sets ControllerFrequency for aircraft whose
// frequency-switch time has passed, then removes those entries.
func (s *Sim) processFutureFrequencyChanges() {
	now := s.State.SimTime
	var switched []*Aircraft
	s.FutureFrequencyChanges = util.FilterSliceInPlace(s.FutureFrequencyChanges,
		func(ffc FutureFrequencyChange) bool {
			if now.After(ffc.Time) {
				if ac, ok := s.Aircraft[ffc.ADSBCallsign]; ok {
					ac.ControllerFrequency = ffc.TCP
					switched = append(switched, ac)
				}
				return false
			}
			return true
		})
	for _, ac := range switched {
		s.processDeferredContact(ac)
	}
}

// PopReadyContact removes and returns the first pending contact whose ReadyTime has passed
// for any of the given positions, or nil if none are ready yet.
// This is called when the client is ready to play a contact.
func (s *Sim) PopReadyContact(positions []TCP) *PendingContact {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.popReadyContact(positions)
}

// popReadyContact is the internal version that requires the lock to already be held.
func (s *Sim) popReadyContact(positions []TCP) *PendingContact {
	if s.PendingContacts == nil {
		return nil
	}

	// Find the first contact that's ready (ReadyTime has passed) for any position
	for _, tcp := range positions {
		for i, pc := range s.PendingContacts[tcp] {
			if s.State.SimTime.After(pc.ReadyTime) {
				s.PendingContacts[tcp] = slices.Delete(s.PendingContacts[tcp], i, i+1)
				return &pc
			}
		}
	}

	return nil
}

// processVirtualControllerContacts handles pending contacts for virtual
// controllers. Human controllers' contacts are processed by their clients
// via PopReadyContact/GenerateContactTransmission, but virtual controllers
// have no client, so we process their contacts here in the update loop.
func (s *Sim) processVirtualControllerContacts() {
	for tcp, contacts := range s.PendingContacts {
		if !s.isVirtualController(tcp) {
			continue
		}

		s.PendingContacts[tcp] = util.FilterSliceInPlace(contacts,
			func(pc PendingContact) bool {
				if !s.State.SimTime.After(pc.ReadyTime) {
					return true // not ready yet; leave it in the slice
				}

				if ac, ok := s.Aircraft[pc.ADSBCallsign]; ok && ac.IsDeparture() {
					// For departures contacting virtual controllers, enqueue climbing to
					// cruise altitude.
					s.enqueueDepartOnCourse(ac.ADSBCallsign)
				}
				// In any case, we can drop it from the pending contacts slice.
				return false
			})
	}
}

// enqueueControllerContact adds an aircraft to the pending contacts queue.
// Called when an aircraft should contact a controller (after handoff accepted, etc.)
// fromPos is the controller position the aircraft is coming from, used to
// determine whether this is the first contact in a TRACON facility (for ATIS reporting).
func (s *Sim) enqueueControllerContact(ac *Aircraft, tcp TCP, fromPos ControlPosition) {
	// Aircraft will switch frequency (2-4 sec), then listen before transmitting (3-6 sec).
	switchDelay := s.Rand.DurationRange(2*time.Second, 5*time.Second)
	listenDelay := s.Rand.DurationRange(3*time.Second, 7*time.Second)
	s.FutureFrequencyChanges = append(s.FutureFrequencyChanges,
		FutureFrequencyChange{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Time: s.State.SimTime.Add(switchDelay)})

	s.addPendingContact(PendingContact{
		ADSBCallsign:    ac.ADSBCallsign,
		TCP:             tcp,
		ReadyTime:       s.State.SimTime.Add(switchDelay + listenDelay),
		Type:            util.Select(ac.IsDeparture(), PendingTransmissionDeparture, PendingTransmissionArrival),
		FirstInFacility: s.isFirstFacilityContact(fromPos, tcp),
	})
}

// isFirstFacilityContact returns true if transitioning from fromPos to
// toTCP represents the aircraft's first contact in a TRACON facility.
// This is true when the target is a local TRACON controller and the
// source is external (ERAM center or a different TRACON).
func (s *Sim) isFirstFacilityContact(fromPos ControlPosition, toTCP TCP) bool {
	toCtrl, ok := s.State.Controllers[ControlPosition(toTCP)]
	if !ok || toCtrl.ERAMFacility {
		return false // Target is not a TRACON controller
	}

	fromCtrl, ok := s.State.Controllers[fromPos]
	if !ok {
		return true // Unknown source, assume new facility
	}

	// If the source is external (ERAM or different TRACON), this is the
	// first contact in the local TRACON facility.
	return fromCtrl.IsExternal()
}

// virtualControllerTransferComms handles the comms transfer when a handoff
// from a virtual controller is accepted. If the pilot is currently on the
// virtual's frequency, the transfer happens immediately; otherwise it is
// deferred until the pilot arrives on the virtual's frequency.
func (s *Sim) virtualControllerTransferComms(ac *Aircraft, virtualTCP TCP, targetTCP TCP) {
	if ac.ControllerFrequency == ControlPosition(virtualTCP) {
		// Pilot is on the virtual's frequency right now.
		if s.isVirtualController(targetTCP) {
			// Virtual-to-virtual: instant frequency change, then check
			// for further deferred contacts on the new position.
			ac.ControllerFrequency = ControlPosition(targetTCP)
			s.processDeferredContact(ac)
		} else {
			// Virtual-to-human: realistic switch/listen delay.
			s.enqueueControllerContact(ac, targetTCP, ControlPosition(virtualTCP))
		}
	} else {
		// Pilot hasn't reached the virtual's frequency yet. Store a
		// deferred contact so that when the pilot does arrive, we send
		// them onward to targetTCP.
		if s.DeferredContacts == nil {
			s.DeferredContacts = make(map[av.ADSBCallsign]map[ControlPosition]TCP)
		}
		if s.DeferredContacts[ac.ADSBCallsign] == nil {
			s.DeferredContacts[ac.ADSBCallsign] = make(map[ControlPosition]TCP)
		}
		s.DeferredContacts[ac.ADSBCallsign][ControlPosition(virtualTCP)] = targetTCP
	}
}

// processDeferredContact checks whether the aircraft's current frequency
// matches a deferred contact entry. If so, it triggers the transfer (instant
// for virtual targets, enqueued for human targets) and removes the entry.
func (s *Sim) processDeferredContact(ac *Aircraft) {
	if s.DeferredContacts == nil {
		return
	}
	m, ok := s.DeferredContacts[ac.ADSBCallsign]
	if !ok {
		return
	}
	targetTCP, ok := m[ac.ControllerFrequency]
	if !ok {
		return
	}
	delete(m, ac.ControllerFrequency)
	if len(m) == 0 {
		delete(s.DeferredContacts, ac.ADSBCallsign)
	}

	// Cancel the orphaned PendingContact for the virtual position that
	// directed the pilot here (it was created by contactController).
	virtualTCP := TCP(ac.ControllerFrequency)
	if pcs, ok := s.PendingContacts[virtualTCP]; ok {
		s.PendingContacts[virtualTCP] = slices.DeleteFunc(pcs,
			func(pc PendingContact) bool { return pc.ADSBCallsign == ac.ADSBCallsign })
	}

	if s.isVirtualController(targetTCP) {
		// Virtual-to-virtual: instant, then recurse.
		ac.ControllerFrequency = ControlPosition(targetTCP)
		s.processDeferredContact(ac)
	} else {
		// Virtual-to-human: realistic delay.
		s.enqueueControllerContact(ac, targetTCP, ac.ControllerFrequency)
	}
}

// enqueueDepartureContact adds a departure to the pending contacts queue.
// Departures are ready immediately (they're already on frequency).
func (s *Sim) enqueueDepartureContact(ac *Aircraft, tcp TCP) {
	ac.ControllerFrequency = ControlPosition(tcp)
	s.addPendingContact(PendingContact{
		ADSBCallsign:           ac.ADSBCallsign,
		TCP:                    tcp,
		Type:                   PendingTransmissionDeparture,
		ReportDepartureHeading: ac.ReportDepartureHeading,
		HasQueuedEmergency:     ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1,
	})
}

// enqueuePilotTransmission adds a pilot-initiated transmission to the pending queue.
func (s *Sim) enqueuePilotTransmission(callsign av.ADSBCallsign, tcp TCP, txType PendingTransmissionType) {
	s.addPendingContact(PendingContact{
		ADSBCallsign: callsign,
		TCP:          tcp,
		Type:         txType,
	})
}

// enqueueEmergencyTransmission adds an emergency transmission to the pending queue.
// Emergency transmissions have a pre-built message since they're generated at trigger time.
func (s *Sim) enqueueEmergencyTransmission(callsign av.ADSBCallsign, tcp TCP, rt *av.RadioTransmission) {
	s.addPendingContact(PendingContact{
		ADSBCallsign:         callsign,
		TCP:                  tcp,
		Type:                 PendingTransmissionEmergency,
		PrebuiltTransmission: rt,
	})
}

// cancelPendingInitialContact removes any pending Departure or Arrival contact
// for the given aircraft. Called when a controller issues a command to an
// aircraft that hasn't checked in yet, preventing stale check-ins.
// Caller must hold s.mu.
func (s *Sim) cancelPendingInitialContact(callsign av.ADSBCallsign) {
	if s.PendingContacts == nil {
		return
	}

	ac := s.Aircraft[callsign]
	tcp := TCP(ac.ControllerFrequency)

	s.PendingContacts[tcp] = slices.DeleteFunc(s.PendingContacts[tcp], func(pc PendingContact) bool {
		return pc.ADSBCallsign == callsign &&
			(pc.Type == PendingTransmissionDeparture || pc.Type == PendingTransmissionArrival)
	})
}

// GenerateContactTransmission generates a transmission for a pending contact.
// Returns the spoken and written text, or empty strings if the contact is invalid.
// This is called when the client requests a contact, using current aircraft state.
func (s *Sim) GenerateContactTransmission(pc *PendingContact) (spokenText, writtenText string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ac, ok := s.Aircraft[pc.ADSBCallsign]
	if !ok {
		return "", ""
	}

	var rt *av.RadioTransmission

	switch pc.Type {
	case PendingTransmissionDeparture:
		if !ac.IsAssociated() {
			return "", ""
		}
		sid := ""
		if ac.ReportDepartureSID {
			sid = ac.SID
		}
		rt = ac.Nav.DepartureMessage(sid, pc.ReportDepartureHeading)

		// Handle emergency activation for departures
		humanAllocated := !s.isVirtualController(ac.ControllerFrequency)
		if humanAllocated && pc.HasQueuedEmergency {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}
		// For departures to virtual controllers, enqueue climbing to cruise
		if ac.IsDeparture() && !humanAllocated {
			s.enqueueDepartOnCourse(ac.ADSBCallsign)
		}

	case PendingTransmissionArrival:
		if !ac.IsAssociated() {
			return "", ""
		}
		rt = ac.ContactMessage(s.ReportingPoints)
		rt.Type = av.RadioTransmissionContact

		// Append ATIS information only when this is the first contact
		// in a TRACON facility (not when transferring between
		// controllers in the same facility, and not for ERAM controllers).
		if pc.FirstInFacility {
			arrivalAirport := ac.FlightPlan.ArrivalAirport
			if letter, ok := s.State.ATISLetter[arrivalAirport]; ok && letter != "" {
				if s.Rand.Float32() < 0.85 { // 85% of aircraft give the ATIS
					reportLetter := letter
					age := s.State.SimTime.Sub(s.ATISChangedTime[arrivalAirport])

					// Possible report having the previous ATIS if it has changed recently: always
					// report the last one in the first 20 seconds after a change, then linearly
					// ramp down the probability to zero 3 minutes after a change.
					p := 1 - max(0, (age.Seconds()-20)/(300-20))
					if s.Rand.Float32() < float32(p) {
						reportLetter = string(rune((letter[0]-'A'+25)%26 + 'A'))
					}
					ac.ReportedATIS = reportLetter
					rt.Add("[we have information {ch}|information {ch}|we have {ch}]", reportLetter)
				}
			}
		}

		// Handle emergency activation for arrivals
		humanAllocated := !s.isVirtualController(ac.ControllerFrequency)
		if humanAllocated && ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1 {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}

	case PendingTransmissionTrafficInSight:
		rt = av.MakeContactTransmission("[we've got the traffic|we have the traffic in sight|traffic in sight now]")

	case PendingTransmissionTrafficWhere:
		rt = av.MakeContactTransmission("[where's that traffic|request update on that traffic|we still don't have the traffic]")
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionFieldInSight:
		rt = av.MakeContactTransmission("[we have the field in sight now|field in sight|we have the airport in sight now]")

	case PendingTransmissionFieldNegativeContact:
		rt = av.MakeContactTransmission("[negative field|field not in sight|no joy on the field]")

	case PendingTransmissionFlightFollowingReq:
		rt = av.MakeContactTransmission("[VFR request|with a VFR request]")

	case PendingTransmissionFlightFollowingFull:
		rt = s.generateFlightFollowingMessage(ac)

	case PendingTransmissionGoAround:
		rt = av.MakeContactTransmission("[going around|on the go]")
		targetAlt, _, _ := ac.Nav.TargetAltitude()
		currentAlt := ac.Altitude()
		if currentAlt < targetAlt {
			rt.Add("[at|] {alt} [climbing|for] {alt}", currentAlt, targetAlt)
		} else {
			rt.Add("[at|] {alt}", currentAlt)
		}
		if ac.GoAroundOnRunwayHeading {
			rt.Add("[runway heading|on a runway heading]")
		} else if ac.Nav.Heading.Assigned != nil {
			rt.Add("heading {hdg}", int(*ac.Nav.Heading.Assigned+0.5))
		}
		if ac.SentAroundForSpacing {
			rt.Add("[tower sent us around for spacing|we were sent around for spacing]")
			ac.SentAroundForSpacing = false
		}
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionRequestApproachClearance:
		rt = av.MakeContactTransmission("[are we cleared for the approach|looking for the approach|we're going to need the approach here shortly]")
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionRequestVectors:
		rt = av.MakeContactTransmission("[we're going to overshoot the localizer, request vectors|we're gonna be unable to intercept, request new heading|we're going to miss the localizer, request vectors]")
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionRequestAltitude:
		rt = av.MakeContactTransmission("[what altitude should we maintain|what altitude do you want us at]")
		rt.Type = av.RadioTransmissionUnexpected

	case PendingTransmissionEmergency:
		if pc.PrebuiltTransmission == nil {
			return "", ""
		}
		rt = pc.PrebuiltTransmission
		rt.Type = av.RadioTransmissionUnexpected // Mark as urgent for display

	case PendingTransmissionRequestVisual:
		runway := ""
		if ac.Nav.Approach.Assigned != nil {
			runway = ac.Nav.Approach.Assigned.Runway
		}

		// Pilot just reports field in sight and requests "the visual" —
		// it's the controller's decision whether to clear a plain visual
		// (CVA) or a charted visual procedure (C).
		rt = av.MakeContactTransmission(
			"[field in sight|we have the airport in sight], [requesting the visual|can we get the visual] [approach |]runway {rwy}",
			runway)

	default:
		return "", ""
	}

	if rt == nil {
		return "", ""
	}

	// Get the base (unprefixed) text for the event stream.
	// prepareRadioTransmissions will add the prefix when delivering to clients.
	baseSpoken := rt.Spoken(s.Rand)
	baseWritten := rt.Written(s.Rand)

	// Post the radio event with unprefixed text
	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          pc.ADSBCallsign,
		ToController:          pc.TCP,
		DestinationTCW:        s.State.TCWForPosition(pc.TCP),
		WrittenText:           baseWritten,
		SpokenText:            baseSpoken,
		RadioTransmissionType: rt.Type,
	})

	// Generate prefixed text for the TTS return value (not going through event stream)
	ctrl := s.State.Controllers[pc.TCP]
	if ctrl == nil {
		return baseSpoken, baseWritten
	}

	var heavySuper string
	if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok && !ctrl.ERAMFacility {
		if perf.WeightClass == "H" {
			heavySuper = " heavy"
		} else if perf.WeightClass == "J" {
			heavySuper = " super"
		}
	}

	// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super
	if ac.EmergencyState != nil && s.Rand.Bool() {
		heavySuper += " emergency aircraft"
	}

	csArg := av.CallsignArg{
		Callsign:           ac.ADSBCallsign,
		IsEmergency:        ac.EmergencyState != nil,
		AlwaysFullCallsign: true,
	}

	var prefix *av.RadioTransmission
	if ac.TypeOfFlight == av.FlightTypeDeparture {
		prefix = av.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper, ctrl, csArg)
	} else {
		prefix = av.MakeContactTransmission("{actrl}, {callsign}"+heavySuper, ctrl, csArg)
	}

	prefix.Merge(rt)
	spokenText = prefix.Spoken(s.Rand)
	writtenText = prefix.Written(s.Rand)
	return spokenText, writtenText
}

type FutureChangeSquawk struct {
	ADSBCallsign av.ADSBCallsign
	Code         av.Squawk
	Mode         av.TransponderMode
	Time         Time
}

func (s *Sim) enqueueTransponderChange(callsign av.ADSBCallsign, code av.Squawk, mode av.TransponderMode) {
	wait := s.Rand.DurationRange(5*time.Second, 10*time.Second)
	s.FutureSquawkChanges = append(s.FutureSquawkChanges,
		FutureChangeSquawk{ADSBCallsign: callsign, Code: code, Mode: mode, Time: s.State.SimTime.Add(wait)})
}

func (s *Sim) processFutureChangeSquawk() {
	s.FutureSquawkChanges = util.FilterSliceInPlace(s.FutureSquawkChanges,
		func(fcs FutureChangeSquawk) bool {
			if s.State.SimTime.After(fcs.Time) {
				if ac, ok := s.Aircraft[fcs.ADSBCallsign]; ok {
					ac.Squawk = fcs.Code
					ac.Mode = fcs.Mode
				}
				return false
			}
			return true
		})
}

type PilotSpeech struct {
	Callsign av.ADSBCallsign
	Type     av.RadioTransmissionType
	Text     string
	SimTime  Time // Virtual simulation time when transmission was made
}
