// sim/control.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
)

// callsignAudioOffset is the approximate time taken by the callsign at the
// start of a voice transmission. When a controller issues a long instruction,
// we subtract (audioDuration - callsignAudioOffset) from the pilot-reaction
// delay in the Nav layer to offset the latency already spent receiving the
// voice transmission.
const callsignAudioOffset = time.Second

// TCWCanCommandAircraft returns true if the TCW can issue ATC commands to an aircraft
// (altitude, heading, speed, etc.). This is true if the TCW is privileged or controls
// the position whose frequency the aircraft is tuned to.
func (s *Sim) TCWCanCommandAircraft(tcw TCW, ac *Aircraft) bool {
	return s.PrivilegedTCWs[tcw] ||
		(ac != nil && s.State.TCWControlsPosition(tcw, ac.ControllerFrequency))
}

// TCWCanModifyTrack returns true if the TCW can modify the track itself (delete, reposition).
// This is true if the TCW is privileged, owns the track, or controls the TrackingController position.
func (s *Sim) TCWCanModifyTrack(tcw TCW, fp *NASFlightPlan) bool {
	return s.PrivilegedTCWs[tcw] ||
		fp.OwningTCW == tcw ||
		s.State.TCWControlsPosition(tcw, fp.TrackingController) ||
		s.State.TCWControlsPosition(tcw, fp.LastLocalController)
}

// TCWCanModifyFlightPlan returns true if the TCW can access/modify flight plan fields.
// Checks if TCW controls the owner's position (consolidation-aware). This is true if
// the TCW is privileged, owns the track, or controls the position that owns the track.
func (s *Sim) TCWCanModifyFlightPlan(tcw TCW, fp *NASFlightPlan) bool {
	return s.PrivilegedTCWs[tcw] ||
		fp.OwningTCW == tcw ||
		s.State.TCWControlsPosition(tcw, fp.TrackingController) ||
		s.State.TCWControlsPosition(tcw, fp.LastLocalController)
}

func (s *Sim) dispatchAircraftCommand(tcw TCW, callsign av.ADSBCallsign, check func(tcw TCW, ac *Aircraft) error,
	cmd func(tcw TCW, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	s.lastControlCommandTime = time.Now()

	if ac, ok := s.Aircraft[callsign]; !ok {
		return nil, av.ErrNoAircraftForCallsign
	} else if _, ok := s.State.CurrentConsolidation[tcw]; !ok {
		return nil, ErrUnknownController
	} else {
		if check != nil {
			if err := check(tcw, ac); err != nil {
				return nil, err
			}
		}

		preAc := *ac
		intent := cmd(tcw, ac)

		s.lg.Info("dispatch_command", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("prepost_aircraft", []Aircraft{preAc, *ac}),
			slog.Any("intent", intent))

		return intent, nil
	}
}

// dispatchControlledAircraftCommand dispatches a command to an aircraft if the
// TCW controls the position whose frequency the aircraft is tuned to.
func (s *Sim) dispatchControlledAircraftCommand(tcw TCW, callsign av.ADSBCallsign,
	cmd func(tcw TCW, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	intent, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			if !s.TCWCanCommandAircraft(tcw, ac) {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)

	// If command succeeded, cancel any pending initial contact for this aircraft.
	// This handles the case where a controller issues commands to an aircraft
	// that hasn't checked in yet.
	if err == nil {
		s.cancelPendingInitialContact(callsign)
	}

	return intent, err
}

// Note that ac may be nil, but flight plan will not be!
func (s *Sim) dispatchFlightPlanCommand(tcw TCW, acid ACID, check func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent) (av.CommandIntent, error) {
	s.lastControlCommandTime = time.Now()

	fp, ac, _ := s.getFlightPlanForACID(acid)
	if fp == nil {
		return nil, ErrNoMatchingFlightPlan
	}
	// ac may or may not be nil; we'll pass it along if we have it

	if _, ok := s.State.CurrentConsolidation[tcw]; !ok {
		return nil, ErrUnknownController
	}

	if check != nil {
		if err := check(tcw, fp, ac); err != nil {
			return nil, err
		}
	}

	preFp := *fp
	intent := cmd(tcw, fp, ac)

	s.lg.Info("dispatch_fp_command", slog.String("acid", string(fp.ACID)),
		slog.Any("prepost_fp", []NASFlightPlan{preFp, *fp}),
		slog.Any("intent", intent))

	return intent, nil
}

func (s *Sim) dispatchTrackedFlightPlanCommand(tcw TCW, acid ACID, check func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error,
	cmd func(tcw TCW, fp *NASFlightPlan, ac *Aircraft)) error {
	_, err := s.dispatchFlightPlanCommand(tcw, acid,
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) error {
			if !s.TCWCanModifyFlightPlan(tcw, fp) {
				return av.ErrOtherControllerHasTrack
			}
			if check != nil {
				return check(tcw, fp, ac)
			}
			return nil
		},
		func(tcw TCW, fp *NASFlightPlan, ac *Aircraft) av.CommandIntent {
			cmd(tcw, fp, ac)
			// No radio transmissions for these
			return nil
		})
	return err
}

func (s *Sim) DeleteAircraft(tcw TCW, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	_, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcw {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			s.eventStream.Post(Event{
				Type:        StatusMessageEvent,
				WrittenText: fmt.Sprintf("%s deleted %s", tcw, ac.ADSBCallsign),
			})

			s.deleteAircraft(ac)

			return nil
		})
	return err
}

func (s *Sim) deleteAircraft(ac *Aircraft) {
	if s.CIDAllocator != nil {
		if fp := ac.NASFlightPlan; fp != nil && fp.CID != "" {
			s.CIDAllocator.Release(fp.CID)
			fp.CID = ""
		} else if fp := s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign)); fp != nil && fp.CID != "" {
			s.CIDAllocator.Release(fp.CID)
			fp.CID = ""
		}
	}
	delete(s.Aircraft, ac.ADSBCallsign)

	// Remove any pending transmissions from this aircraft
	for tcp := range s.PendingContacts {
		s.PendingContacts[tcp] = slices.DeleteFunc(s.PendingContacts[tcp],
			func(pc PendingContact) bool { return pc.ADSBCallsign == ac.ADSBCallsign })
	}

	delete(s.DeferredContacts, ac.ADSBCallsign)

	// Remove any scheduled future events for this aircraft
	s.FutureOnCourse = slices.DeleteFunc(s.FutureOnCourse,
		func(foc FutureOnCourse) bool { return foc.ADSBCallsign == ac.ADSBCallsign })
	s.FutureSquawkChanges = slices.DeleteFunc(s.FutureSquawkChanges,
		func(fcs FutureChangeSquawk) bool { return fcs.ADSBCallsign == ac.ADSBCallsign })
	s.FutureEmergencyUpdates = slices.DeleteFunc(s.FutureEmergencyUpdates,
		func(feu FutureEmergencyUpdate) bool { return feu.ADSBCallsign == ac.ADSBCallsign })
	s.cancelFutureFieldCheck(ac.ADSBCallsign)
	s.cancelFutureTrafficCheck(ac.ADSBCallsign)

	s.STARSComputer.HoldForRelease = slices.DeleteFunc(s.STARSComputer.HoldForRelease,
		func(a *Aircraft) bool { return ac.ADSBCallsign == a.ADSBCallsign })

	// Clean up pattern state
	for _, ps := range s.PatternState {
		ps.Aircraft = slices.DeleteFunc(ps.Aircraft, func(pa PatternAircraft) bool {
			return pa.ADSBCallsign == ac.ADSBCallsign
		})
	}

	fp := ac.NASFlightPlan
	if fp == nil {
		fp = s.STARSComputer.takeFlightPlanByACID(ACID(ac.ADSBCallsign))
	}
	if fp != nil {
		delete(s.Handoffs, fp.ACID)
		delete(s.PointOuts, fp.ACID)
		s.deleteFlightPlan(fp)
	}

	s.lg.Info("deleted aircraft", slog.String("adsb_callsign", string(ac.ADSBCallsign)))
}

func (s *Sim) DeleteAircraftSlice(tcw TCW, aircraft []Aircraft) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for _, ac := range aircraft {
		s.deleteAircraft(&ac)
	}

	return nil
}

func (s *Sim) DeleteAllAircraft(tcw TCW) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcw {
		return av.ErrOtherControllerHasTrack
	}

	for _, ac := range s.Aircraft {
		// Only delete airborne aircraft; leave all of the ones at the
		// gate, etc., so we don't have a bubble of no departures for a
		// long time while the departure queues refill.
		if ac.IsAirborne() {
			s.deleteAircraft(ac)
		}
	}

	// Also clean up aircraft in HFR departure queues
	s.clearDepartureQueues()

	return nil
}

func (s *Sim) clearDepartureQueues() {
	// Clear HFR (Hold For Release) departure queues to remove aircraft that are held for release
	for _, runways := range s.DepartureState {
		for _, depState := range runways {
			// Delete aircraft from the Held queue (HFR)
			for _, dep := range depState.Held {
				if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok {
					s.deleteAircraft(ac)
				}
			}

			// Clear the HFR queue
			depState.Held = nil
		}
	}
}

func (s *Sim) ReleaseDeparture(tcw TCW, callsign av.ADSBCallsign) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	ac, ok := s.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	fp := s.STARSComputer.lookupFlightPlanByACID(ACID(callsign))
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if !s.State.TCWControlsPosition(tcw, fp.InboundHandoffController) {
		return ErrInvalidDepartureController
	}

	if err := s.STARSComputer.ReleaseDeparture(callsign); err == nil {
		ac.Released = true
		ac.ReleaseTime = s.State.SimTime
		return nil
	} else {
		return err
	}
}

func (s *Sim) ShouldTriggerPilotMixUp(callsign av.ADSBCallsign) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// If pilot errors are disabled (interval == 0), never trigger mix-ups
	if s.PilotErrorInterval == 0 {
		return false
	}

	// Check if enough time has passed since the last pilot error globally
	if s.State.SimTime.Sub(s.LastPilotError) <= s.PilotErrorInterval {
		return false
	}

	// Check if we've recently communicated with this specific aircraft
	if ac, ok := s.Aircraft[callsign]; ok {
		// Don't trigger mix-up if we just communicated with this pilot
		if !ac.LastRadioTransmission.IsZero() && s.State.SimTime.Sub(ac.LastRadioTransmission) < 20*time.Second {
			return false
		}
	}

	// Update the last error time and trigger the mix-up
	s.LastPilotError = s.State.SimTime
	return true
}

// PilotMixUp is called standalone (not as part of a command batch) so it posts its own event.
// Returns the spoken text for TTS synthesis and the PlayAt sim-time stamped
// on the posted event. requesterToken stamps the event for observer-side
// dedup against the requester's RPC-result-driven synthesis.
func (s *Sim) PilotMixUp(tcw TCW, callsign av.ADSBCallsign, requesterToken string) (string, Time, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	intent, err := s.dispatchControlledAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			return ac.PilotMixUp()
		})
	if err == nil && intent != nil {
		spokenText, playAt := s.renderAndPostReadbackLocked(callsign, tcw, []av.CommandIntent{intent}, requesterToken)
		return spokenText, playAt, nil
	}
	return "", Time{}, err
}
