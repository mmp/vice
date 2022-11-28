// commands.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file defines functionality related to built-in commands in vice,
// including both function-key based and in the CLI.

package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// FKeyCommandArg interface and implementations

// FKeyCommandArg defines an interface that describes individual arguments
// to function-key commands in vice.
type FKeyCommandArg interface {
	// Prompt returns a string that gives a prompt describing the
	// argument's purpose.
	Prompt() string

	// Expand takes user input for an argument and attempts to expand it to
	// a valid string.  For example, for an argument that gives an aircraft
	// callsign, if the user enters "12" and the only known aircraft with
	// "12" in its callsign is "AAL12", Expand would return "AAL12" with
	// a nil error.  Alternatively, if multiple aircraft have "12" in their
	// callsign, it would return an empty string and a non-nil error.
	Expand(s string) (string, error)
}

// AircraftCommandArg represents an argument that is an aircraft callsign.
// It takes an additional callback function that further validates the
// callsign; this allows limiting the considered aircraft to a subset of
// all of them, for example, just the ones that the user is currently
// tracking.
type AircraftCommandArg struct {
	validateCallsign func(string) bool
}

func (*AircraftCommandArg) Prompt() string { return "aircraft" }

func (a *AircraftCommandArg) Expand(s string) (string, error) {
	// If we have a fully-specified callsign that matches an existing aircraft that also
	// passes the validation function's check, we're all set.
	if server.GetAircraft(s) != nil && (a.validateCallsign == nil || a.validateCallsign(s)) {
		return s, nil
	}

	// Otherwise get all of the active aircraft where the track is active,
	// the supplied string is a substring of the callsign, and the
	// validation function (if supplied) passes.
	now := server.CurrentTime()
	ac := server.GetFilteredAircraft(func(ac *Aircraft) bool {
		return !ac.LostTrack(now) && strings.Contains(ac.Callsign(), s) &&
			(a.validateCallsign == nil || a.validateCallsign(ac.Callsign()))
	})

	// Convert matching *Aircraft into callsign strings.
	matches := MapSlice[*Aircraft, string](ac, func(ac *Aircraft) string { return ac.Callsign() })

	if len(matches) == 1 {
		// A single match; all good.
		return matches[0], nil
	} else if len(matches) == 0 {
		return "", errors.New("No matching aircraft")
	} else {
		sort.Strings(matches)
		return "", fmt.Errorf("Multiple matching aircraft: %v", matches)
	}
}

// canEditAircraftAssignments is a utility function to be used for
// AircraftCommandArg's validateCallsign callback.  It checks whether the
// user can edit the aircraft's properties, either because it is untracked
// or because the user is tracking it.
func canEditAircraftAssignments(callsign string) bool {
	tc := server.GetTrackingController(callsign)
	return tc == "" || tc == server.Callsign()
}

// aircraftTrackedByMe is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft is currently
// tracked by the user.
func aircraftTrackedByMe(callsign string) bool {
	return server.GetTrackingController(callsign) == server.Callsign()
}

// aircraftHasFlightPlan is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft has a valid
// flight plan.
func aircraftHasFlightPlan(callsign string) bool {
	ac := server.GetAircraft(callsign)
	return ac != nil && ac.flightPlan != nil
}

// aircraftTrackedByMe is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft is not
// tracked by any controller.
func aircraftIsUntracked(callsign string) bool {
	return server.GetTrackingController(callsign) == ""
}

// aircraftTrackedByMe is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft is being handed
// off by another controller to the current controller.
func aircraftIsInboundHandoff(callsign string) bool {
	return server.InboundHandoffController(callsign) != ""
}

// ControllerCommandArg represents an argument that identifies another
// active controller.
type ControllerCommandArg struct{}

func (*ControllerCommandArg) Prompt() string { return "controller" }

func (*ControllerCommandArg) Expand(s string) (string, error) {
	if s != server.Callsign() && server.GetController(s) != nil {
		// The string exactly matches another active controller; done.
		return s, nil
	}

	var matches []string
	for _, ctrl := range server.GetAllControllers() {
		// The controller matches if s is a substring of the controller's
		// callsign or if it exactly matches the controller's sector id. We
		// don't allow substring matches of the sector id, since that seems
		// fairly arbitrary/obscure.
		ok := strings.Contains(ctrl.callsign, s)
		if pos := ctrl.GetPosition(); pos != nil {
			ok = ok || pos.sectorId == s
		}

		if ok {
			matches = append(matches, ctrl.callsign)
		}
	}

	if len(matches) == 1 {
		return matches[0], nil
	} else if len(matches) == 0 {
		return "", errors.New("No matching controllers")
	} else {
		sort.Strings(matches)
		return "", fmt.Errorf("Multiple matching controllers: %v", matches)
	}
}

// AltitudeCommandArg represents an argument that specifies an altitude.
type AltitudeCommandArg struct{}

func (*AltitudeCommandArg) Prompt() string { return "altitude" }

func (*AltitudeCommandArg) Expand(s string) (string, error) {
	// The string must be a valid integer. Note that we don't check that
	// it's positive or in a reasonable range...
	if _, err := strconv.Atoi(s); err == nil {
		return s, nil
	} else {
		return "", err
	}
}

// LimitedStringCommandArg represents an argument that is a string of some
// limited length.
type LimitedStringCommandArg struct {
	// ArgPrompt specifies the prompt to print for the argument.
	ArgPrompt string
	// MaxChars gives the maximum number of characters allowed for the
	// argument.
	MaxChars int
}

func (ss *LimitedStringCommandArg) Prompt() string { return ss.ArgPrompt }

func (ss *LimitedStringCommandArg) Expand(s string) (string, error) {
	if len(s) <= ss.MaxChars {
		return s, nil
	}
	return "", fmt.Errorf("Only %d characters allowed for "+ss.ArgPrompt, ss.MaxChars)
}

// SquawkCommandArg represents a squawk code argument (or an empty string,
// for use for automatically-assigned squawk codes).
type SquawkCommandArg struct{}

func (*SquawkCommandArg) Prompt() string { return "squawk" }

func (*SquawkCommandArg) Expand(s string) (string, error) {
	if s == "" {
		return s, nil
	} else if _, err := ParseSquawk(s); err == nil {
		return s, nil
	} else {
		return "", err
	}
}

// VoiceCapabilityCommandArg represents an argument that specifies an
// aircraft's voice capability.
type VoiceCapabilityCommandArg struct{}

func (*VoiceCapabilityCommandArg) Prompt() string { return "voice capability" }

func (v *VoiceCapabilityCommandArg) Expand(s string) (string, error) {
	if s == "V" || s == "R" || s == "T" {
		return s, nil
	}
	return "", errors.New("Voice capability must be \"V\", \"R\", or \"T\".")
}

///////////////////////////////////////////////////////////////////////////
// FKeyCommand

// FKeyCommand defines the interface for commands that are run via F-keys
// pressed by the user.
type FKeyCommand interface {
	// Name returns the name of the command, for display in the status bar.
	Name() string
	// ArgTypes returns a slice of objects that all implement the
	// FKeyCommandArg interface, one for each argument to the command.
	ArgTypes() []FKeyCommandArg
	// Do executes the command, using the provided
	// arguments. Implementations can assume that args has the same length
	// as the slice returned by ArgTypes and that each argument is valid, as
	// per the corresponding FKeyCommandArg Complete method.
	Do(args []string) error
}

// allFKeyCommands collects all of the f-key commands along with a
// human-readable description; this description is used in the UI for
// associating f-keys with commands.
var allFKeyCommands map[string]FKeyCommand = map[string]FKeyCommand{
	"Accept handoff":           &AcceptHandoffFKeyCommand{},
	"Add to MIT sequence":      &AddToMITListFKeyCommand{},
	"Assign final altitude":    &AssignFinalAltitudeFKeyCommand{},
	"Assign temp. altitude":    &AssignTemporaryAltitudeFKeyCommand{},
	"Assign squawk code":       &AssignSquawkFKeyCommand{},
	"Contact me":               &ContactMeFKeyCommand{},
	"Draw route":               &DrawRouteFKeyCommand{},
	"Drop track":               &DropTrackFKeyCommand{},
	"Handoff":                  &HandoffFKeyCommand{},
	"Initiate track":           &TrackFKeyCommand{},
	"Point out":                &PointOutFKeyCommand{},
	"Push flight strip":        &PushFlightStripFKeyCommand{},
	"Reject handoff":           &RejectHandoffFKeyCommand{},
	"Remove from MIT sequence": &RemoveFromMITListFKeyCommand{},
	"Set aircraft type":        &SetAircraftTypeFKeyCommand{},
	"Set equipment suffix":     &SetEquipmentSuffixFKeyCommand{},
	"Set IFR":                  &SetIFRFKeyCommand{},
	"Set VFR":                  &SetVFRFKeyCommand{},
	"Set voice capability":     &SetVoiceCapabilityFKeyCommand{},
	"Set scratchpad":           &ScratchpadFKeyCommand{},
}

// AcceptHandoffFKeyCommand accepts a handoff from another controller
type AcceptHandoffFKeyCommand struct{}

func (*AcceptHandoffFKeyCommand) Name() string { return "accept handoff" }

func (*AcceptHandoffFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: aircraftIsInboundHandoff}}
}

func (*AcceptHandoffFKeyCommand) Do(args []string) error { return server.AcceptHandoff(args[0]) }

// AddToMITListFKeyCommand adds an aircraft to the miles in trail sequence
// that's shown on the radar scope.
type AddToMITListFKeyCommand struct{}

func (*AddToMITListFKeyCommand) Name() string { return "add to MIT" }

func (*AddToMITListFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{}}
}

func (*AddToMITListFKeyCommand) Do(args []string) error {
	if ac := server.GetAircraft(args[0]); ac == nil {
		// This shouldn't happen, but...
		return ErrNoAircraftForCallsign
	} else {
		positionConfig.mit = append(positionConfig.mit, ac)
		return nil
	}
}

// AssignFinalAltitudeFKeyCommand assigns a final altitude to an aircraft.
type AssignFinalAltitudeFKeyCommand struct{}

func (*AssignFinalAltitudeFKeyCommand) Name() string { return "assign final altitude" }

func (*AssignFinalAltitudeFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&AltitudeCommandArg{}}
}

func (*AssignFinalAltitudeFKeyCommand) Do(args []string) error {
	altitude, err := strconv.Atoi(args[1])
	if err != nil {
		return err
	}
	if altitude < 1000 {
		altitude *= 100
	}

	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.altitude = altitude })
}

// AssignSquawkFKeyCommand assigns a squawk code to an aircraft.
type AssignSquawkFKeyCommand struct{}

func (*AssignSquawkFKeyCommand) Name() string { return "assign squawk" }

func (*AssignSquawkFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&SquawkCommandArg{}}
}

func (*AssignSquawkFKeyCommand) Do(args []string) error {
	if args[1] == "" {
		// No squawk code specified, so try to assign it automatically from
		// the allocated codes for the position.
		return server.SetSquawkAutomatic(args[0])
	}

	if squawk, err := ParseSquawk(args[1]); err != nil {
		return err
	} else {
		return server.SetSquawk(args[0], squawk)
	}
}

// AssignTemporaryAltitudeFKeyCommand assigns a temporary altitude to an
// aircraft.
type AssignTemporaryAltitudeFKeyCommand struct{}

func (*AssignTemporaryAltitudeFKeyCommand) Name() string { return "assign temp. altitude" }

func (*AssignTemporaryAltitudeFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&AltitudeCommandArg{}}
}

func (*AssignTemporaryAltitudeFKeyCommand) Do(args []string) error {
	if altitude, err := strconv.Atoi(args[1]); err != nil {
		return err
	} else {
		// Allow specifying either a flight level or a complete altitude.
		if altitude < 1000 {
			altitude *= 100
		}
		return server.SetTemporaryAltitude(args[0], altitude)
	}
}

// ContactMeFKeyCommand sends a "contact me" message to an aircraft.
type ContactMeFKeyCommand struct{}

func (*ContactMeFKeyCommand) Name() string { return "contact me" }

func (*ContactMeFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{}}
}

func (*ContactMeFKeyCommand) Do(args []string) error {
	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   args[0],
		contents: fmt.Sprintf("Please contact me on %s. Please do not respond via private "+
			"message - use the frequency instead.", positionConfig.primaryFrequency),
	}

	return server.SendTextMessage(tm)
}

// DrawRouteFKeyCommand draws the selected aircraft's route on the scope.
type DrawRouteFKeyCommand struct{}

func (*DrawRouteFKeyCommand) Name() string { return "draw route" }

func (*DrawRouteFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: aircraftHasFlightPlan}}
}

func (*DrawRouteFKeyCommand) Do(args []string) error {
	ac := server.GetAircraft(args[0])

	// Neither of these errors should be possible due to validation, but be
	// careful anyway so that we don't accidentally dereference a nil
	// pointer.
	if ac == nil {
		return ErrNoAircraftForCallsign
	}
	if ac.flightPlan == nil {
		return ErrNoFlightPlan
	}

	positionConfig.drawnRoute = ac.flightPlan.depart + " " + ac.flightPlan.route + " " +
		ac.flightPlan.arrive
	positionConfig.drawnRouteEndTime = time.Now().Add(5 * time.Second)
	return nil
}

// DropTrackFKeyCommand drops the track on a tracked aircraft.
type DropTrackFKeyCommand struct{}

func (*DropTrackFKeyCommand) Name() string { return "drop track" }

func (*DropTrackFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: aircraftTrackedByMe}}
}

func (*DropTrackFKeyCommand) Do(args []string) error { return server.DropTrack(args[0]) }

// HandoffFKeyCommand initiates the handoff of an aircraft to another
// controller.
type HandoffFKeyCommand struct{}

func (*HandoffFKeyCommand) Name() string { return "handoff" }

func (*HandoffFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: aircraftTrackedByMe},
		&ControllerCommandArg{}}
}

func (*HandoffFKeyCommand) Do(args []string) error { return server.Handoff(args[0], args[1]) }

// PointOutFKeyCommand points an aircraft out to a controller.
type PointOutFKeyCommand struct{}

func (*PointOutFKeyCommand) Name() string { return "point out" }

func (*PointOutFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: aircraftTrackedByMe},
		&ControllerCommandArg{}}
}

func (*PointOutFKeyCommand) Do(args []string) error { return server.PointOut(args[0], args[1]) }

// PushFlightStripFKeyCommand pushes an aircraft's flight strip to another
// controller.
type PushFlightStripFKeyCommand struct{}

func (*PushFlightStripFKeyCommand) Name() string { return "push flight strip" }

func (*PushFlightStripFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: aircraftTrackedByMe},
		&ControllerCommandArg{}}
}

func (*PushFlightStripFKeyCommand) Do(args []string) error {
	return server.PushFlightStrip(args[0], args[1])
}

// RejectHandoffFKeyCommand rejects an inbound handoff from another
// controller.
type RejectHandoffFKeyCommand struct{}

func (*RejectHandoffFKeyCommand) Name() string { return "reject handoff" }

func (*RejectHandoffFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: aircraftIsInboundHandoff}}
}

func (*RejectHandoffFKeyCommand) Do(args []string) error { return server.RejectHandoff(args[0]) }

// RemoveFromMITListFKeyCommand removes an aircraft from the miles in trail
// sequence.
type RemoveFromMITListFKeyCommand struct{}

func (*RemoveFromMITListFKeyCommand) Name() string { return "remove from MIT list" }

func (*RemoveFromMITListFKeyCommand) ArgTypes() []FKeyCommandArg {
	isInMITList := func(callsign string) bool {
		for _, ac := range positionConfig.mit {
			if ac.callsign == callsign {
				return true
			}
		}
		return false
	}
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: isInMITList}}
}

func (*RemoveFromMITListFKeyCommand) Do(args []string) error {
	positionConfig.mit = FilterSlice(positionConfig.mit,
		func(ac *Aircraft) bool { return ac.callsign != args[0] })
	return nil
}

// ScratchpadFKeyCommand sets the scratchpad for an aircraft.
type ScratchpadFKeyCommand struct{}

func (*ScratchpadFKeyCommand) Name() string { return "scratchpad" }

func (*ScratchpadFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&LimitedStringCommandArg{ArgPrompt: "scratchpad", MaxChars: 4}}
}

func (*ScratchpadFKeyCommand) Do(args []string) error {
	return server.SetScratchpad(args[0], args[1])
}

// SetAircraftTypeFKeyCommand sets an aircraft's type.
type SetAircraftTypeFKeyCommand struct{}

func (*SetAircraftTypeFKeyCommand) Name() string { return "set aircraft type" }

func (*SetAircraftTypeFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&LimitedStringCommandArg{ArgPrompt: "type", MaxChars: 10}}
}

func (*SetAircraftTypeFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) {
		fp.actype = args[1]
	})
}

// SetEquipmentSuffixFKeyCommand sets just the equipment suffix of an
// aircraft, replacing the old suffix, if present.
type SetEquipmentSuffixFKeyCommand struct{}

func (*SetEquipmentSuffixFKeyCommand) Name() string { return "set equipment suffix" }

func (*SetEquipmentSuffixFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&LimitedStringCommandArg{ArgPrompt: "suffix", MaxChars: 1}}
}

func (*SetEquipmentSuffixFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) {
		fp.actype = fp.TypeWithoutSuffix() + "/" + args[1]
	})
}

// SetIFRFKeyCommand sets the flight rules for an aircraft to be IFR.
type SetIFRFKeyCommand struct{}

func (*SetIFRFKeyCommand) Name() string { return "set IFR" }

func (*SetIFRFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: canEditAircraftAssignments}}
}

func (*SetIFRFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.rules = IFR })
}

// SetVFRFKeyCommand sets the flight rules for an aircraft to be VFR.
type SetVFRFKeyCommand struct{}

func (*SetVFRFKeyCommand) Name() string { return "set VFR" }

func (*SetVFRFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: canEditAircraftAssignments}}
}

func (*SetVFRFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.rules = VFR })
}

// SetVoiceCapabilityFKeyCommand sets an aircraft's voice capability.
type SetVoiceCapabilityFKeyCommand struct{}

func (*SetVoiceCapabilityFKeyCommand) Name() string { return "set voice capability" }

func (*SetVoiceCapabilityFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: canEditAircraftAssignments},
		&VoiceCapabilityCommandArg{}}
}

func (*SetVoiceCapabilityFKeyCommand) Do(args []string) error {
	var vc VoiceCapability
	switch args[1] {
	case "V":
		vc = VoiceFull
	case "R":
		vc = VoiceReceive
	case "T":
		vc = VoiceText
	}
	return server.SetVoiceType(args[0], vc)
}

// TrackFKeyCommand initiates track of an untracked aircraft.
type TrackFKeyCommand struct{}

func (*TrackFKeyCommand) Name() string { return "track" }

func (*TrackFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: aircraftIsUntracked}}
}

func (*TrackFKeyCommand) Do(args []string) error { return server.InitiateTrack(args[0]) }
