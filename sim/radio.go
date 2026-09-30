// sim/radio.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
)

// reportTransmissionFailure surfaces a transmission that couldn't be
// formatted. The pilot says nothing at all in that case, which to the
// controller is indistinguishable from an aircraft ignoring them, so the
// phrase that failed goes to the messages pane along with the callsign.
func (s *Sim) reportTransmissionFailure(callsign av.ADSBCallsign, tcp ControlPosition, err error) {
	s.lg.Errorf("%s: %v", callsign, err)
	s.eventStream.Post(Event{
		Type:         ErrorMessageEvent,
		ADSBCallsign: callsign,
		ToController: tcp,
		WrittenText:  string(callsign) + ": unable to format radio transmission " + err.Error(),
	})
}

// postReadbackTransmission posts a pilot's response to a command from the
// controller at tcw, which goes to that controller regardless of any
// consolidation changes, and returns it as spoken. The pilot ends it with
// the callsign unless it is a mix-up, which already names one, or goes
// without one.
func (s *Sim) postReadbackTransmission(from av.ADSBCallsign, tr *speech.RadioTransmission, tcw TCW) string {
	if tr.Type != speech.RadioTransmissionMixUp && tr.Type != speech.RadioTransmissionNoId {
		if suffix := s.readbackCallsignSuffix(from, tcw); suffix != nil {
			tr.Merge(suffix)
		}
	}

	tcp := s.State.PrimaryPositionForTCW(tcw)
	rd, err := tr.Render(s.textRand)
	if err != nil {
		s.reportTransmissionFailure(from, tcp, err)
		return ""
	}
	if rd.Written == "" && rd.Spoken == "" {
		return ""
	}

	if ac, ok := s.Aircraft[from]; ok {
		ac.LastRadioTransmission = s.State.SimTime
	}

	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          from,
		ToController:          tcp,
		WrittenText:           rd.Written,
		RadioTransmissionType: tr.Type,
	})
	return rd.Spoken
}

// readbackCallsignSuffix returns the callsign a pilot ends a readback to the
// controller at tcw with.
func (s *Sim) readbackCallsignSuffix(callsign av.ADSBCallsign, tcw TCW) *speech.RadioTransmission {
	ac, ok := s.Aircraft[callsign]
	if !ok {
		return nil
	}

	primaryTCP := s.State.PrimaryPositionForTCW(tcw)
	ctrl := s.State.Controllers[primaryTCP]

	var heavySuper string
	if ctrl != nil && !ctrl.ERAMFacility {
		if perf, ok := db.DB.AircraftPerformance[ac.AircraftType]; ok {
			if perf.WeightClass == "H" {
				heavySuper = " heavy"
			} else if perf.WeightClass == "J" {
				heavySuper = " super"
			}
		}
	}

	// Use GACallsignArg for GA aircraft when addressed with type+trailing3 form
	var csArg any
	if strings.HasPrefix(string(callsign), "N") && ac.LastAddressingForm == AddressingFormTypeTrailing3 {
		csArg = speech.GACallsignArg{
			Callsign:     ac.ADSBCallsign,
			AircraftType: ac.AircraftType,
			UseTypeForm:  true,
			IsEmergency:  ac.EmergencyState != nil,
		}
	} else {
		csArg = speech.CallsignArg{
			Callsign:    ac.ADSBCallsign,
			IsEmergency: ac.EmergencyState != nil,
		}
	}
	return speech.MakeReadbackTransmission("{callsign}"+heavySuper, csArg)
}

///////////////////////////////////////////////////////////////////////////
// Deferred operations

// PendingTransmissionType identifies the type of pilot-initiated transmission.
type PendingTransmissionType int

const (
	PendingTransmissionDeparture                        PendingTransmissionType = iota // Departure checking in
	PendingTransmissionArrival                                                         // Arrival/handoff checking in
	PendingTransmissionTrafficInSight                                                  // "Traffic in sight" call
	PendingTransmissionFlightFollowingReq                                              // Abbreviated "VFR request"
	PendingTransmissionFlightFollowingFull                                             // Full flight following request
	PendingTransmissionGoAround                                                        // Go-around announcement
	PendingTransmissionEmergency                                                       // Emergency stage transmission
	PendingTransmissionRequestApproachClearance                                        // Pilot requesting approach clearance
	PendingTransmissionFieldInSight                                                    // Delayed "field in sight" after "looking"
	PendingTransmissionSpontaneousFieldInSight                                         // Unprompted "field in sight"
	PendingTransmissionRequestVisual                                                   // Spontaneous "field in sight, requesting visual"
	PendingTransmissionRequestVectors                                                  // Pilot requesting vectors (overshot localizer)
	PendingTransmissionRequestAltitude                                                 // Pilot requesting altitude after being vectored off STAR
	PendingTransmissionRequestTowerSwitch                                              // Pilot is close in on the approach without being sent to tower
	PendingTransmissionReportingPointInSight                                           // Delayed reporting point "in sight" after "looking"
	PendingTransmissionSpontaneousReportingPointInSight                                // Unprompted reporting point "in sight"
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
	ID                     uint64 // Identifies the contact to the clients that say it
	ADSBCallsign           av.ADSBCallsign
	TCP                    TCP
	QueuedTime             Time                      // When the pilot decided to transmit
	ReadyTime              Time                      // When pilot is ready to transmit
	Type                   PendingTransmissionType   // What kind of transmission
	ReportDepartureHeading bool                      // For departures: include assigned heading
	HasQueuedEmergency     bool                      // For departures: trigger emergency after contact
	PrebuiltTransmission   *speech.RadioTransmission // For emergency transmissions: pre-built message
	ATIS                   string                    // For arrivals: the ATIS letter the pilot reports, if any
}

// PilotTransmission is what a pilot who has called a controller says,
// rendered from the aircraft's state when it was published.
type PilotTransmission struct {
	ContactID    uint64
	ADSBCallsign av.ADSBCallsign
	Written      string
	Spoken       string
	Voice        string
	Type         speech.RadioTransmissionType
}

// hasPendingCheckIn reports whether the aircraft has a pending arrival or
// departure check-in that hasn't been transmitted yet and still applies.
func (s *Sim) hasPendingCheckIn(callsign av.ADSBCallsign) bool {
	for _, pcs := range s.PendingContacts {
		for _, pc := range pcs {
			if pc.ADSBCallsign == callsign &&
				(pc.Type == PendingTransmissionArrival || pc.Type == PendingTransmissionDeparture) &&
				s.contactApplies(pc) {
				return true
			}
		}
	}
	return false
}

// contactReady reports whether the pilot is ready to say a pending contact:
// its ReadyTime has passed and, for a check-in, the track has tagged up.
func (s *Sim) contactReady(pc PendingContact) bool {
	if !s.State.SimTime.After(pc.ReadyTime) {
		return false
	}
	if pc.Type != PendingTransmissionDeparture && pc.Type != PendingTransmissionArrival {
		return true
	}
	ac, ok := s.Aircraft[pc.ADSBCallsign]
	return ok && ac.IsAssociated()
}

// contactApplies reports whether a pending contact is still worth saying:
// the aircraft is still around, a pilot who is ready to talk is on the
// frequency they meant to call, and nothing that has happened since the
// pilot queued it has made it moot. Every transmission type states its
// rule here; a type without one is never said.
func (s *Sim) contactApplies(pc PendingContact) bool {
	ac, ok := s.Aircraft[pc.ADSBCallsign]
	if !ok {
		return false
	}
	// A pilot who isn't ready to talk yet may still be switching to the
	// frequency.
	if s.State.SimTime.After(pc.ReadyTime) && ac.ControllerFrequency != ControlPosition(pc.TCP) {
		return false
	}

	switch pc.Type {
	case PendingTransmissionDeparture, PendingTransmissionArrival:
		// Checking in is moot once the controller has given the pilot an
		// instruction.
		return !ac.instructedSince(pc)

	case PendingTransmissionTrafficInSight, PendingTransmissionFlightFollowingReq,
		PendingTransmissionFlightFollowingFull, PendingTransmissionGoAround,
		PendingTransmissionEmergency, PendingTransmissionFieldInSight:
		// Reports of what happened, requests for service, and reports the
		// controller asked for stay worth making.
		return true

	case PendingTransmissionRequestApproachClearance:
		// Moot once the clearance came in or the aircraft is no longer
		// tracking the approach course waiting for one.
		return !ac.Nav.Approach.EffectivelyCleared() && ac.Nav.InterceptedButNotCleared()

	case PendingTransmissionSpontaneousFieldInSight, PendingTransmissionRequestVisual:
		// An unprompted report or request is moot once the aircraft is
		// cleared for an approach, immediately or "at fix".
		return !ac.Nav.Approach.EffectivelyCleared()

	case PendingTransmissionRequestVectors:
		// Moot once the controller has given the pilot an instruction,
		// having seen the overshoot and vectored or re-cleared the aircraft.
		return !ac.instructedSince(pc)

	case PendingTransmissionRequestAltitude:
		// Moot once the controller has assigned an altitude, possibly
		// deferred behind a speed change, or cleared the approach, whose
		// altitudes now govern.
		return ac.Nav.Altitude.Assigned == nil && ac.Nav.Altitude.AfterSpeed == nil &&
			!ac.Nav.Approach.EffectivelyCleared()

	case PendingTransmissionRequestTowerSwitch:
		// Moot once the aircraft is no longer flying the approach (went
		// around, clearance cancelled, vectored off). Being sent to tower
		// takes it off the frequency.
		return ac.Nav.Approach.Cleared

	case PendingTransmissionReportingPointInSight:
		return ac.SightedReportingPoint != nil

	case PendingTransmissionSpontaneousReportingPointInSight:
		return ac.SightedReportingPoint != nil && !ac.Nav.Approach.EffectivelyCleared()

	default:
		return false
	}
}

// instructedSince reports whether the controller the contact is for has
// given the pilot an instruction since the pilot queued it. Controller
// requests run between ticks, so one made at the sim time a contact was
// queued came after it.
func (ac *Aircraft) instructedSince(pc PendingContact) bool {
	return ac.LastInstructionFrequency == ControlPosition(pc.TCP) && !ac.LastInstructionTime.Before(pc.QueuedTime)
}

// cullStaleContacts drops the pending contacts that no longer apply. It also
// drops, and reports, ready ones whose transmission can't be rendered, which
// would otherwise stay at the front of their controller's queue.
func (s *Sim) cullStaleContacts() {
	for tcp, pcs := range util.SortedMap(s.PendingContacts) {
		s.PendingContacts[tcp] = slices.DeleteFunc(pcs, func(pc PendingContact) bool {
			if !s.contactApplies(pc) {
				return true
			}
			if s.contactReady(pc) {
				if _, err := s.renderContact(pc); err != nil {
					s.reportTransmissionFailure(pc.ADSBCallsign, pc.TCP, err)
					return true
				}
			}
			return false
		})
	}
}

// addPendingContact adds an aircraft to the pending contacts queue for a controller.
func (s *Sim) addPendingContact(pc PendingContact) {
	if s.PendingContacts == nil {
		s.PendingContacts = make(map[TCP][]PendingContact)
	}
	s.LastContactID++
	pc.ID = s.LastContactID
	pc.QueuedTime = s.State.SimTime
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
					s.setControllerFrequency(ac, ffc.TCP)
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

func (s *Sim) setControllerFrequency(ac *Aircraft, pos ControlPosition) {
	if ac.ControllerFrequency != pos {
		s.clearAircraftSTTCommands(ac.ADSBCallsign)
		ac.ControllerFrequency = pos
	}
}

// isInitialCheckIn reports whether the transmission type is an aircraft's
// first call to the controller, as opposed to a response or request made
// during an already-established exchange.
func (t PendingTransmissionType) isInitialCheckIn() bool {
	switch t {
	case PendingTransmissionDeparture, PendingTransmissionArrival, PendingTransmissionFlightFollowingReq:
		return true
	default:
		return false
	}
}

// nextContact returns the pending contact the controller working the given
// positions is to hear next, or nil if no pilot is ready to call them.
func (s *Sim) nextContact(positions []TCP) *PendingContact {
	// A pilot's response or request during an already-established exchange
	// (the full request after "go ahead", "traffic in sight", a go-around,
	// etc.) takes priority over an unrelated aircraft's initial check-in: it
	// would be unrealistic for a third party to key up in the middle of an
	// exchange the controller just initiated. Prefer a ready response, then
	// fall back to initial check-ins.
	if pc := s.readyMatching(positions, func(t PendingTransmissionType) bool { return !t.isInitialCheckIn() }); pc != nil {
		return pc
	}
	return s.readyMatching(positions, PendingTransmissionType.isInitialCheckIn)
}

// readyMatching returns the longest-waiting pending contact across the given
// positions whose type satisfies match, that the pilot is ready to say, and
// that still applies, or nil if none qualify. Taking the oldest rather than
// the first position's first entry keeps a busy position from starving the
// others.
func (s *Sim) readyMatching(positions []TCP, match func(PendingTransmissionType) bool) *PendingContact {
	var best *PendingContact
	for _, tcp := range positions {
		for i, pc := range s.PendingContacts[tcp] {
			if !match(pc.Type) || !s.contactReady(pc) || !s.contactApplies(pc) {
				continue
			}
			if best == nil || pc.ReadyTime.Before(best.ReadyTime) {
				best = &s.PendingContacts[tcp][i]
			}
		}
	}
	if best == nil {
		return nil
	}
	pc := *best
	return &pc
}

// NextPilotTransmission returns what the controller at tcw is to hear next
// from a pilot calling them, or nil if no pilot is ready to.
func (s *Sim) NextPilotTransmission(tcw TCW) *PilotTransmission {
	pc := s.nextContact(s.GetPositionsForTCW(tcw))
	if pc == nil {
		return nil
	}
	pt, err := s.renderContact(*pc)
	if err != nil {
		// The next tick's culling reports it.
		s.lg.Errorf("%s: %v", pc.ADSBCallsign, err)
		return nil
	}
	pt.Voice = s.GetReadbackVoice(pc.ADSBCallsign)
	return pt
}

// TakePilotTransmission takes pt out of the queue for the client to play it,
// if it is still what the controller at tcw is to hear next, and reports
// whether it did; the client plays pt only then. Its text, which is what the
// client synthesized, goes to the messages pane, and whatever follows the
// pilot saying it is set in motion. Either way the state is published so
// that the client's reply carries what is now next.
func (s *Sim) TakePilotTransmission(tcw TCW, pt PilotTransmission) bool {
	defer s.publish()

	pc := s.nextContact(s.GetPositionsForTCW(tcw))
	if pc == nil || pc.ID != pt.ContactID {
		return false
	}
	s.PendingContacts[pc.TCP] = slices.DeleteFunc(s.PendingContacts[pc.TCP],
		func(p PendingContact) bool { return p.ID == pc.ID })
	ac := s.Aircraft[pc.ADSBCallsign] // present, since the contact applies

	switch pc.Type {
	case PendingTransmissionDeparture:
		if pc.HasQueuedEmergency && ac.EmergencyState != nil {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}

	case PendingTransmissionArrival:
		if pc.ATIS != "" {
			ac.ReportedATIS = pc.ATIS
		}
		if ac.EmergencyState != nil && ac.EmergencyState.CurrentStage == -1 {
			ac.EmergencyState.CurrentStage = 0
			s.runEmergencyStage(ac)
		}

	case PendingTransmissionGoAround:
		ac.SentAroundForSpacing = false
	}

	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          pc.ADSBCallsign,
		ToController:          pc.TCP,
		WrittenText:           pt.Written,
		RadioTransmissionType: pt.Type,
	})
	return true
}

// processVirtualControllerContacts handles pending contacts for virtual
// controllers. Human controllers hear theirs when their clients play and
// report them, but virtual controllers have no client, so we process their
// contacts here in the update loop.
func (s *Sim) processVirtualControllerContacts() {
	for tcp, contacts := range util.SortedMap(s.PendingContacts) {
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
	if tcp == "" {
		s.lg.Errorf("%s: no controller to send the pilot to", ac.ADSBCallsign)
		return
	}

	// Aircraft will switch frequency (2-4 sec), then listen before transmitting (3-6 sec).
	switchDelay := s.Rand.DurationRange(2*time.Second, 5*time.Second)
	listenDelay := s.Rand.DurationRange(3*time.Second, 7*time.Second)
	s.FutureFrequencyChanges = append(s.FutureFrequencyChanges,
		FutureFrequencyChange{ADSBCallsign: ac.ADSBCallsign, TCP: tcp, Time: s.State.SimTime.Add(switchDelay)})

	s.addPendingContact(PendingContact{
		ADSBCallsign: ac.ADSBCallsign,
		TCP:          tcp,
		ReadyTime:    s.State.SimTime.Add(switchDelay + listenDelay),
		Type:         util.Select(ac.IsDeparture(), PendingTransmissionDeparture, PendingTransmissionArrival),
		ATIS:         s.atisToReport(ac, tcp, fromPos),
	})
}

// atisToReport returns the ATIS letter an arrival checking in with tcp will
// report, if any. Pilots only give the ATIS when they first contact a TRACON
// controller in a facility, and not all of them do.
func (s *Sim) atisToReport(ac *Aircraft, tcp TCP, fromPos ControlPosition) string {
	if ac.IsDeparture() || ac.IsOverflight() || !s.isTRACONController(ControlPosition(tcp)) ||
		!s.isFirstFacilityContact(fromPos) {
		return ""
	}
	letter := s.State.ATISLetter[ac.ArrivalAirport]
	if letter == "" || s.Rand.Float32() >= 0.85 { // 85% of aircraft give the ATIS
		return ""
	}

	// Possible report having the previous ATIS if it has changed recently: always
	// report the last one in the first 20 seconds after a change, then linearly
	// ramp down the probability to zero 3 minutes after a change.
	age := s.State.SimTime.Sub(s.ATISChangedTime[ac.ArrivalAirport])
	p := 1 - max(0, (age.Seconds()-20)/(300-20))
	if s.Rand.Float32() < float32(p) {
		return string(rune((letter[0]-'A'+25)%26 + 'A'))
	}
	return letter
}

// isFirstFacilityContact reports whether the aircraft is making its first
// call to a controller in this facility: the source position belongs to
// another facility, or is unknown.
func (s *Sim) isFirstFacilityContact(fromPos ControlPosition) bool {
	if _, ok := s.State.Controllers[s.State.ResolveController(fromPos)]; !ok {
		return true // Unknown source, assume new facility
	}
	return s.State.IsExternalController(fromPos)
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
			s.setControllerFrequency(ac, ControlPosition(targetTCP))
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
		s.setControllerFrequency(ac, ControlPosition(targetTCP))
		s.processDeferredContact(ac)
	} else {
		// Virtual-to-human: realistic delay.
		s.enqueueControllerContact(ac, targetTCP, ac.ControllerFrequency)
	}
}

// enqueueDepartureContact adds a departure to the pending contacts queue.
// Departures are ready immediately (they're already on frequency).
func (s *Sim) enqueueDepartureContact(ac *Aircraft, tcp TCP) {
	if tcp == "" {
		s.lg.Errorf("%s: no departure controller to check in with", ac.ADSBCallsign)
		return
	}

	s.setControllerFrequency(ac, ControlPosition(tcp))
	s.addPendingContact(PendingContact{
		ADSBCallsign:           ac.ADSBCallsign,
		TCP:                    tcp,
		ReadyTime:              s.State.SimTime,
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
		ReadyTime:    s.State.SimTime,
		Type:         txType,
	})
}

// enqueueEmergencyTransmission adds an emergency transmission to the pending queue.
// Emergency transmissions have a pre-built message since they're generated at trigger time.
func (s *Sim) enqueueEmergencyTransmission(callsign av.ADSBCallsign, tcp TCP, rt *speech.RadioTransmission) {
	s.addPendingContact(PendingContact{
		ADSBCallsign:         callsign,
		TCP:                  tcp,
		ReadyTime:            s.State.SimTime,
		Type:                 PendingTransmissionEmergency,
		PrebuiltTransmission: rt,
	})
}

// renderContact renders what the pilot says for a pending contact, from the
// aircraft's current state. Its choice of phrasing depends only on the
// contact, so it stays the same from one state update to the next while the
// facts it reports change. It changes nothing in the sim.
func (s *Sim) renderContact(pc PendingContact) (*PilotTransmission, error) {
	ac, ok := s.Aircraft[pc.ADSBCallsign]
	if !ok {
		return nil, av.ErrNoAircraftForCallsign
	}

	r := rand.New(pc.ID)
	rt := s.contactTransmission(pc, ac, r)
	// The pilot starts with the name of the controller they are calling and
	// their callsign.
	if ctrl := s.State.Controllers[pc.TCP]; ctrl != nil {
		prefix := contactPrefix(ac, ctrl, r)
		prefix.Merge(rt)
		rt = prefix
	}

	rd, err := rt.Render(r)
	if err != nil {
		return nil, err
	}
	return &PilotTransmission{
		ContactID:    pc.ID,
		ADSBCallsign: pc.ADSBCallsign,
		Written:      rd.Written,
		Spoken:       rd.Spoken,
		Type:         rt.Type,
	}, nil
}

// contactTransmission returns what the pilot says for a pending contact after
// calling the controller.
func (s *Sim) contactTransmission(pc PendingContact, ac *Aircraft, r *rand.Rand) *speech.RadioTransmission {
	var rt *speech.RadioTransmission

	switch pc.Type {
	case PendingTransmissionDeparture:
		sid := ""
		if ac.ReportDepartureSID {
			sid = ac.SID
		}
		rt = ac.Nav.DepartureMessage(sid, pc.ReportDepartureHeading)

	case PendingTransmissionArrival:
		rt = ac.ContactMessage()
		rt.Type = speech.RadioTransmissionContact
		if pc.ATIS != "" {
			rt.Add("[we have information {ch}|information {ch}|we have {ch}]", pc.ATIS)
		}

	case PendingTransmissionTrafficInSight:
		rt = speech.MakeContactTransmission("[we've got the traffic|we have the traffic in sight|traffic in sight now]")

	case PendingTransmissionFieldInSight, PendingTransmissionSpontaneousFieldInSight:
		rt = speech.MakeContactTransmission("[we have the field in sight now|field in sight|we have the airport in sight now]")

	case PendingTransmissionReportingPointInSight, PendingTransmissionSpontaneousReportingPointInSight:
		rp := ac.SightedReportingPoint
		if pc.Type == PendingTransmissionReportingPointInSight {
			// The controller named it, so any of its names will do.
			rt = speech.MakeContactTransmission("[{rp} in sight now|{rp} in sight]", rand.SampleSlice(r, rp.Names))
		} else {
			// Unprompted, the pilot gives its full name.
			rt = speech.MakeContactTransmission("{rp} in sight", rp.Name())
		}

	case PendingTransmissionFlightFollowingReq:
		rt = speech.MakeContactTransmission("[VFR request|with a VFR request]")

	case PendingTransmissionFlightFollowingFull:
		rt = s.generateFlightFollowingMessage(ac, r)

	case PendingTransmissionGoAround:
		rt = speech.MakeContactTransmission("[going around|on the go]")
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
		}
		rt.Type = speech.RadioTransmissionUnexpected

	case PendingTransmissionRequestApproachClearance:
		rt = speech.MakeContactTransmission("[are we cleared for the approach|looking for the approach|we're going to need the approach here shortly]")
		rt.Type = speech.RadioTransmissionUnexpected

	case PendingTransmissionRequestVectors:
		if ac.Nav.Approach.HasLocalizer() {
			rt = speech.MakeContactTransmission("[we're going to overshoot the localizer, request vectors|we're gonna be unable to intercept, request new heading|we're going to miss the localizer, request vectors]")
		} else {
			rt = speech.MakeContactTransmission("[we're going to overshoot the final approach course, request vectors|we're gonna be unable to intercept, request new heading|we're going to miss final, request vectors]")
		}
		rt.Type = speech.RadioTransmissionUnexpected

	case PendingTransmissionRequestAltitude:
		rt = speech.MakeContactTransmission("[what altitude should we maintain|what altitude do you want us at]")
		rt.Type = speech.RadioTransmissionUnexpected

	case PendingTransmissionRequestTowerSwitch:
		rt = speech.MakeContactTransmission("[should we switch to tower|do you want us with tower|should we contact tower]")
		rt.Type = speech.RadioTransmissionUnexpected

	case PendingTransmissionEmergency:
		t := *pc.PrebuiltTransmission
		t.Type = speech.RadioTransmissionUnexpected // Mark as urgent for display
		rt = &t

	case PendingTransmissionRequestVisual:
		runway := ""
		if ac.Nav.Approach.Assigned != nil {
			runway = ac.Nav.Approach.Assigned.Runway
		}

		// Pilot just reports field in sight and requests "the visual" —
		// it's the controller's decision whether to clear a plain visual
		// (CVA) or a charted visual procedure (C).
		rt = speech.MakeContactTransmission(
			"[field in sight|we have the airport in sight], [request visual|requesting the visual|can we get the visual] [approach |]runway {rwy}",
			runway)

	}

	return rt
}

// contactPrefix returns how a pilot starts a call to ctrl: the controller's
// name and the full callsign.
func contactPrefix(ac *Aircraft, ctrl *av.Controller, r *rand.Rand) *speech.RadioTransmission {
	var heavySuper string
	if perf, ok := db.DB.AircraftPerformance[ac.AircraftType]; ok && !ctrl.ERAMFacility {
		if perf.WeightClass == "H" {
			heavySuper = " heavy"
		} else if perf.WeightClass == "J" {
			heavySuper = " super"
		}
	}

	// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super
	if ac.EmergencyState != nil && r.Bool() {
		heavySuper += " emergency aircraft"
	}

	csArg := speech.CallsignArg{
		Callsign:           ac.ADSBCallsign,
		IsEmergency:        ac.EmergencyState != nil,
		AlwaysFullCallsign: true,
	}

	if ac.TypeOfFlight == av.FlightTypeDeparture {
		return speech.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper, ctrl, csArg)
	}
	return speech.MakeContactTransmission("{actrl}, {callsign}"+heavySuper, ctrl, csArg)
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

func (s *Sim) processFutureSquawkChanges() {
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
	Type     speech.RadioTransmissionType
	Text     string
	SimTime  Time // Virtual simulation time when transmission was made
}
