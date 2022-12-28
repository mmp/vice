// commands.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file defines functionality related to built-in commands in vice,
// including both function-key based and in the CLI.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
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
		return !ac.LostTrack(now) && strings.Contains(ac.Callsign, s) &&
			(a.validateCallsign == nil || a.validateCallsign(ac.Callsign))
	})

	// Convert matching *Aircraft into callsign strings.
	matches := MapSlice[*Aircraft, string](ac, func(ac *Aircraft) string { return ac.Callsign })

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
	if ac := server.GetAircraft(callsign); ac == nil {
		return false
	} else {
		return ac.TrackingController == "" || ac.TrackingController == server.Callsign()
	}
}

// aircraftTrackedByMe is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft is currently
// tracked by the user.
func aircraftTrackedByMe(callsign string) bool {
	ac := server.GetAircraft(callsign)
	return ac != nil && ac.TrackingController == server.Callsign()
}

// aircraftHasFlightPlan is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft has a valid
// flight plan.
func aircraftHasFlightPlan(callsign string) bool {
	ac := server.GetAircraft(callsign)
	return ac != nil && ac.FlightPlan != nil
}

// aircraftTrackedByMe is a utility function for the AircraftCommandArg's
// validateCallsign callback.  It checks whether the aircraft is not
// tracked by any controller.
func aircraftIsUntracked(callsign string) bool {
	ac := server.GetAircraft(callsign)
	return ac != nil && ac.TrackingController == ""
}

// aircraftIsInboundHandoff is a utility function for the
// AircraftCommandArg's validateCallsign callback.  It checks whether the
// aircraft is being handed off by another controller to the current
// controller.
func aircraftIsInboundHandoff(callsign string) bool {
	ac := server.GetAircraft(callsign)
	return ac != nil && ac.InboundHandoffController != ""
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
		ok := strings.Contains(ctrl.Callsign, s) && ctrl.Callsign != server.Callsign()
		if pos := ctrl.GetPosition(); pos != nil {
			ok = ok || pos.SectorId == s
		}

		if ok {
			matches = append(matches, ctrl.Callsign)
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
	"Multi-drop track":         &MultiDropTrackFKeyCommand{},
	"Multi-track":              &MultiTrackFKeyCommand{},
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

	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.Altitude = altitude })
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
	if ac.FlightPlan == nil {
		return ErrNoFlightPlan
	}

	positionConfig.drawnRoute = ac.FlightPlan.DepartureAirport + " " + ac.FlightPlan.Route + " " +
		ac.FlightPlan.ArrivalAirport
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

// MultiDropTrackFKeyCommand drops track on an aircraft if we're tracking
// it or rejects a handoff from another controller.
type MultiDropTrackFKeyCommand struct{}

func (*MultiDropTrackFKeyCommand) Name() string { return "multidrop" }

func (*MultiDropTrackFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: func(callsign string) bool {
			ac := server.GetAircraft(callsign)
			// Either we must be tracking it or someone must be trying to
			// hand the aircraft off to us.
			return ac != nil && (ac.TrackingController == server.Callsign() || ac.InboundHandoffController != "")
		}}}
}

func (*MultiDropTrackFKeyCommand) Do(args []string) error {
	ac := server.GetAircraft(args[0])
	if ac != nil && ac.TrackingController == server.Callsign() {
		return server.DropTrack(args[0])
	} else {
		return server.RejectHandoff(args[0])
	}
}

// MultiTrackFKeyCommand tracks an untracked aircraft, cancels an outbound
// handoff, or accepts an inbound handoff, as appropriate.
type MultiTrackFKeyCommand struct{}

func (*MultiTrackFKeyCommand) Name() string { return "multitrack" }

func (*MultiTrackFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{
		&AircraftCommandArg{validateCallsign: func(callsign string) bool {
			ac := server.GetAircraft(callsign)
			return ac != nil &&
				(ac.TrackingController == "" || // No one's tracking it
					ac.OutboundHandoffController != "" || // We're trying to hand it off
					ac.InboundHandoffController != "") // Someone's handing it off to us
		}}}
}

func (*MultiTrackFKeyCommand) Do(args []string) error {
	if ac := server.GetAircraft(args[0]); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.TrackingController == "" {
		return server.InitiateTrack(args[0])
	} else if ac.OutboundHandoffController != "" {
		return server.CancelHandoff(args[0])
	} else {
		return server.AcceptHandoff(args[0])
	}
}

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
			if ac.Callsign == callsign {
				return true
			}
		}
		return false
	}
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: isInMITList}}
}

func (*RemoveFromMITListFKeyCommand) Do(args []string) error {
	positionConfig.mit = FilterSlice(positionConfig.mit,
		func(ac *Aircraft) bool { return ac.Callsign != args[0] })
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
		fp.AircraftType = args[1]
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
		fp.AircraftType = fp.TypeWithoutSuffix() + "/" + args[1]
	})
}

// SetIFRFKeyCommand sets the flight rules for an aircraft to be IFR.
type SetIFRFKeyCommand struct{}

func (*SetIFRFKeyCommand) Name() string { return "set IFR" }

func (*SetIFRFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: canEditAircraftAssignments}}
}

func (*SetIFRFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.Rules = IFR })
}

// SetVFRFKeyCommand sets the flight rules for an aircraft to be VFR.
type SetVFRFKeyCommand struct{}

func (*SetVFRFKeyCommand) Name() string { return "set VFR" }

func (*SetVFRFKeyCommand) ArgTypes() []FKeyCommandArg {
	return []FKeyCommandArg{&AircraftCommandArg{validateCallsign: canEditAircraftAssignments}}
}

func (*SetVFRFKeyCommand) Do(args []string) error {
	return amendFlightPlan(args[0], func(fp *FlightPlan) { fp.Rules = VFR })
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

///////////////////////////////////////////////////////////////////////////
// CLICommands

// There is admittedly some redundancy between CLICommands and
// FKeyCommands--"I take these parameters", "here are the parameters, now
// run the command", etc. It would be nice to unify them at some point...

type CLICommand interface {
	Names() []string
	Help() string
	Usage() string
	TakesAircraft() bool
	TakesController() bool
	AdditionalArgs() (min int, max int)
	Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry
}

var (
	cliCommands []CLICommand = []CLICommand{
		&SetACTypeCommand{},
		&SetAltitudeCommand{isTemporary: false},
		&SetAltitudeCommand{isTemporary: true},
		&SetArrivalCommand{},
		&SetDepartureCommand{},
		&SetEquipmentSuffixCommand{},
		&SetIFRCommand{},
		&SetScratchpadCommand{},
		&SetSquawkCommand{},
		&SetVoiceCommand{},
		&SetVFRCommand{},

		&EditRouteCommand{},
		&NYPRDCommand{},
		&PRDCommand{},
		&SetRouteCommand{},

		&DropTrackCommand{},
		&HandoffCommand{},
		&PointOutCommand{},
		&TrackAircraftCommand{},
		&PushFlightStripCommand{},

		&FindCommand{},
		&MITCommand{},
		&DrawRouteCommand{},

		&InfoCommand{},
		&TimerCommand{},
		&ToDoCommand{},
		&TrafficCommand{},

		&ATCChatCommand{},
		&PrivateMessageCommand{},
		&TransmitCommand{},
		&WallopCommand{},
		&RequestATISCommand{},
		&ContactMeCommand{},
		&MessageReplyCommand{},

		&EchoCommand{},
	}
)

func checkCommands(cmds []CLICommand) {
	seen := make(map[string]interface{})
	for _, c := range cmds {
		for _, name := range c.Names() {
			if _, ok := seen[name]; ok {
				lg.Errorf("%s: command has multiple definitions", name)
			} else {
				seen[name] = nil
			}
		}
	}
}

type SetACTypeCommand struct{}

func (*SetACTypeCommand) Names() []string { return []string{"actype", "ac"} }
func (*SetACTypeCommand) Help() string {
	return "Sets the aircraft's type."
}
func (*SetACTypeCommand) Usage() string                      { return "<type>" }
func (*SetACTypeCommand) TakesAircraft() bool                { return true }
func (*SetACTypeCommand) TakesController() bool              { return false }
func (*SetACTypeCommand) AdditionalArgs() (min int, max int) { return 1, 1 }

func (*SetACTypeCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	err := amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
		fp.AircraftType = strings.ToUpper(args[0])
	})
	return ErrorConsoleEntry(err)
}

type SetAltitudeCommand struct {
	isTemporary bool
}

func (sa *SetAltitudeCommand) Names() []string {
	if sa.isTemporary {
		return []string{"tempalt", "ta"}
	} else {
		return []string{"alt"}
	}
}
func (sa *SetAltitudeCommand) Usage() string                      { return "<altitude>" }
func (sa *SetAltitudeCommand) TakesAircraft() bool                { return true }
func (sa *SetAltitudeCommand) TakesController() bool              { return false }
func (sa *SetAltitudeCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (sa *SetAltitudeCommand) Help() string {
	if sa.isTemporary {
		return "Sets the aircraft's temporary clearance altitude."
	} else {
		return "Sets the aircraft's clearance altitude."
	}
}
func (sa *SetAltitudeCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if altitude, err := strconv.Atoi(args[0]); err == nil {
		if altitude < 1000 {
			altitude *= 100
		}

		if sa.isTemporary {
			return ErrorConsoleEntry(server.SetTemporaryAltitude(ac.Callsign, altitude))
		} else {
			return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) { fp.Altitude = altitude }))
		}
	} else {
		return ErrorConsoleEntry(err)
	}
}

type SetArrivalCommand struct{}

func (*SetArrivalCommand) Names() []string                    { return []string{"arrive", "ar"} }
func (*SetArrivalCommand) Usage() string                      { return "<airport>" }
func (*SetArrivalCommand) TakesAircraft() bool                { return true }
func (*SetArrivalCommand) TakesController() bool              { return false }
func (*SetArrivalCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (*SetArrivalCommand) Help() string {
	return "Sets the aircraft's arrival airport."
}
func (*SetArrivalCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if len(args[0]) > 5 {
		return ErrorConsoleEntry(ErrAirportTooLong)
	}
	return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
		fp.ArrivalAirport = strings.ToUpper(args[0])
	}))
}

type SetDepartureCommand struct{}

func (*SetDepartureCommand) Names() []string                    { return []string{"depart", "dp"} }
func (*SetDepartureCommand) Usage() string                      { return "<airport>" }
func (*SetDepartureCommand) TakesAircraft() bool                { return true }
func (*SetDepartureCommand) TakesController() bool              { return false }
func (*SetDepartureCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (*SetDepartureCommand) Help() string {
	return "Sets the aircraft's departure airport"
}
func (*SetDepartureCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if len(args[0]) > 5 {
		return ErrorConsoleEntry(ErrAirportTooLong)
	}
	return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
		fp.DepartureAirport = strings.ToUpper(args[0])
	}))
}

type SetEquipmentSuffixCommand struct{}

func (*SetEquipmentSuffixCommand) Names() []string                    { return []string{"equip", "eq"} }
func (*SetEquipmentSuffixCommand) Usage() string                      { return "<suffix>" }
func (*SetEquipmentSuffixCommand) TakesAircraft() bool                { return true }
func (*SetEquipmentSuffixCommand) TakesController() bool              { return false }
func (*SetEquipmentSuffixCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (*SetEquipmentSuffixCommand) Help() string {
	return "Sets the aircraft's equipment suffix."
}
func (*SetEquipmentSuffixCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.FlightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlanFiled)
	} else {
		return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
			atype := fp.TypeWithoutSuffix()
			suffix := strings.ToUpper(args[0])
			if suffix[0] != '/' {
				suffix = "/" + suffix
			}
			fp.AircraftType = atype + suffix
		}))
	}
}

type SetIFRCommand struct{}

func (*SetIFRCommand) Names() []string                    { return []string{"ifr"} }
func (*SetIFRCommand) Usage() string                      { return "" }
func (*SetIFRCommand) TakesAircraft() bool                { return true }
func (*SetIFRCommand) TakesController() bool              { return false }
func (*SetIFRCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*SetIFRCommand) Help() string {
	return "Marks the aircraft as an IFR flight."
}
func (*SetIFRCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) { fp.Rules = IFR }))
}

type SetScratchpadCommand struct{}

func (*SetScratchpadCommand) Names() []string                    { return []string{"scratchpad", "sp"} }
func (*SetScratchpadCommand) Usage() string                      { return "<contents--optional>" }
func (*SetScratchpadCommand) TakesAircraft() bool                { return true }
func (*SetScratchpadCommand) TakesController() bool              { return false }
func (*SetScratchpadCommand) AdditionalArgs() (min int, max int) { return 0, 1 }
func (*SetScratchpadCommand) Help() string {
	return "Sets the aircraft's scratchpad. If no contents are specified, the scratchpad is cleared."
}
func (*SetScratchpadCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if len(args) == 0 {
		// clear scratchpad
		return ErrorConsoleEntry(server.SetScratchpad(ac.Callsign, ""))
	} else {
		return ErrorConsoleEntry(server.SetScratchpad(ac.Callsign, strings.ToUpper(args[0])))
	}
}

type SetSquawkCommand struct{}

func (*SetSquawkCommand) Names() []string                    { return []string{"squawk", "sq"} }
func (*SetSquawkCommand) Usage() string                      { return "<squawk--optional>" }
func (*SetSquawkCommand) TakesAircraft() bool                { return true }
func (*SetSquawkCommand) TakesController() bool              { return false }
func (*SetSquawkCommand) AdditionalArgs() (min int, max int) { return 0, 1 }
func (*SetSquawkCommand) Help() string {
	return "Sets the aircraft's squawk code. If no code is provided and the aircraft is IFR, a code is assigned automatically."
}
func (*SetSquawkCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if len(args) == 0 {
		return ErrorConsoleEntry(server.SetSquawkAutomatic(ac.Callsign))
	} else {
		squawk, err := ParseSquawk(args[0])
		if err != nil {
			return ErrorConsoleEntry(err)
		}
		return ErrorConsoleEntry(server.SetSquawk(ac.Callsign, squawk))
	}
}

type SetVoiceCommand struct{}

func (*SetVoiceCommand) Names() []string                    { return []string{"voice", "v"} }
func (*SetVoiceCommand) Usage() string                      { return "<voice type:v, r, or t>" }
func (*SetVoiceCommand) TakesAircraft() bool                { return true }
func (*SetVoiceCommand) TakesController() bool              { return false }
func (*SetVoiceCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (*SetVoiceCommand) Help() string {
	return "Sets the aircraft's voice communications type."
}
func (*SetVoiceCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	var cap VoiceCapability
	switch strings.ToLower(args[0]) {
	case "v":
		cap = VoiceFull
	case "r":
		cap = VoiceReceive
	case "t":
		cap = VoiceText
	default:
		return ErrorStringConsoleEntry("Invalid voice communications type specified")
	}

	return ErrorConsoleEntry(server.SetVoiceType(ac.Callsign, cap))
}

type SetVFRCommand struct{}

func (*SetVFRCommand) Names() []string                    { return []string{"vfr"} }
func (*SetVFRCommand) Usage() string                      { return "" }
func (*SetVFRCommand) TakesAircraft() bool                { return true }
func (*SetVFRCommand) TakesController() bool              { return false }
func (*SetVFRCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*SetVFRCommand) Help() string {
	return "Marks the aircraft as a VFR flight."
}
func (*SetVFRCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) { fp.Rules = VFR }))
}

type EditRouteCommand struct{}

func (*EditRouteCommand) Names() []string                    { return []string{"editroute", "er"} }
func (*EditRouteCommand) Usage() string                      { return "" }
func (*EditRouteCommand) TakesAircraft() bool                { return true }
func (*EditRouteCommand) TakesController() bool              { return false }
func (*EditRouteCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*EditRouteCommand) Help() string {
	return "Loads the aircraft's route into the command buffer for editing using the \"route\" command."
}
func (*EditRouteCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.FlightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	cli.input.cmd = "route "
	cli.input.cursor = len(cli.input.cmd)
	cli.input.cmd += ac.FlightPlan.Route

	return nil
}

type NYPRDEntry struct {
	Id            int       `json:"id"`
	AirportOrigin string    `json:"airport_origin"`
	AirportDest   string    `json:"airport_dest"`
	Route         string    `json:"route"`
	Hours1        string    `json:"hours1"`
	Hours2        string    `json:"hours2"`
	Hours3        string    `json:"hours3"`
	RouteType     string    `json:"route_type"`
	Area          string    `json:"area"`
	Altitude      string    `json:"altitude"`
	Aircraft      string    `json:"aircraft"`
	Direction     string    `json:"direction"`
	Seq           string    `json:"seq"`
	CenterOrigin  string    `json:"center_origin"`
	CenterDest    string    `json:"center_dest"`
	IsLocal       int       `json:"is_local"`
	Created       time.Time `json:"created_at"`
	Updated       time.Time `json:"updated_at"`
}

type NYPRDCommand struct{}

func (*NYPRDCommand) Names() []string                    { return []string{"nyprd"} }
func (*NYPRDCommand) Usage() string                      { return "" }
func (*NYPRDCommand) TakesAircraft() bool                { return true }
func (*NYPRDCommand) TakesController() bool              { return false }
func (*NYPRDCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*NYPRDCommand) Help() string {
	return "Looks up the aircraft's route in the ZNY preferred route database."
}
func (*NYPRDCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.FlightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	depart, arrive := ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport
	url := fmt.Sprintf("https://nyartcc.org/prd/search?depart=%s&arrive=%s", depart, arrive)

	resp, err := http.Get(url)
	if err != nil {
		lg.Printf("PRD get err: %+v", err)
		return ErrorStringConsoleEntry("nyprd: network error")
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var prdEntries []NYPRDEntry
	if err := decoder.Decode(&prdEntries); err != nil {
		lg.Errorf("PRD decode err: %+v", err)
		return ErrorStringConsoleEntry("error decoding PRD entry")
	}

	if len(prdEntries) == 0 {
		return ErrorStringConsoleEntry(fmt.Sprintf("no PRD found for route from %s to %s", depart, arrive))
	}

	anyType := false
	anyArea := false
	anyAlt := false
	anyAC := false
	for _, entry := range prdEntries {
		anyType = anyType || (entry.RouteType != "")
		anyArea = anyArea || (entry.Area != "")
		anyAlt = anyAlt || (entry.Altitude != "")
		anyAC = anyAC || (entry.Aircraft != "")
	}

	var result strings.Builder
	w := tabwriter.NewWriter(&result, 0 /* min width */, 1 /* tab width */, 1 /* padding */, ' ', 0)
	w.Write([]byte("\tORG\tDST\t"))
	writeIf := func(b bool, s string) {
		if b {
			w.Write([]byte(s))
		}
	}

	writeIf(anyType, "TYPE\t")
	writeIf(anyArea, "AREA\t")
	writeIf(anyAlt, "ALT\t")
	writeIf(anyAC, "A/C\t")
	w.Write([]byte("ROUTE\n"))

	print := func(entry NYPRDEntry) {
		w.Write([]byte(entry.AirportOrigin + "\t" + entry.AirportDest + "\t"))
		writeIf(anyType, entry.RouteType+"\t")
		writeIf(anyArea, entry.Area+"\t")
		writeIf(anyAlt, entry.Altitude+"\t")
		writeIf(anyAC, entry.Aircraft+"\t")
		w.Write([]byte(entry.Route + "\n"))
	}

	// Print the required ones first, with an asterisk
	for _, entry := range prdEntries {
		if entry.IsLocal == 0 {
			continue
		}
		w.Write([]byte("*\t"))
		print(entry)
	}
	for _, entry := range prdEntries {
		if entry.IsLocal != 0 {
			continue
		}
		w.Write([]byte("\t"))
		print(entry)
	}
	w.Flush()

	return StringConsoleEntry(result.String())
}

type PRDCommand struct{}

func (*PRDCommand) Names() []string                    { return []string{"faaprd"} }
func (*PRDCommand) Usage() string                      { return "" }
func (*PRDCommand) TakesAircraft() bool                { return true }
func (*PRDCommand) TakesController() bool              { return false }
func (*PRDCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*PRDCommand) Help() string {
	return "Looks up the aircraft's route in the FAA preferred route database."
}
func (*PRDCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.FlightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	depart, arrive := ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport
	if len(depart) == 4 && depart[0] == 'K' {
		depart = depart[1:]
	}
	if len(arrive) == 4 && arrive[0] == 'K' {
		arrive = arrive[1:]
	}

	if prdEntries, ok := database.FAA.prd[AirportPair{depart, arrive}]; !ok {
		return ErrorStringConsoleEntry(fmt.Sprintf(depart + "-" + arrive + ": no entry in FAA PRD"))
	} else {
		anyType := false
		anyHour1, anyHour2, anyHour3 := false, false, false
		anyAC := false
		anyAlt, anyDir := false, false
		for _, entry := range prdEntries {
			anyType = anyType || (entry.Type != "")
			anyHour1 = anyHour1 || (entry.Hours[0] != "")
			anyHour2 = anyHour2 || (entry.Hours[1] != "")
			anyHour3 = anyHour3 || (entry.Hours[2] != "")
			anyAC = anyAC || (entry.Aircraft != "")
			anyAlt = anyAlt || (entry.Altitude != "")
			anyDir = anyDir || (entry.Direction != "")
		}

		var result strings.Builder
		w := tabwriter.NewWriter(&result, 0 /* min width */, 1 /* tab width */, 1 /* padding */, ' ', 0)
		w.Write([]byte("NUM\tORG\tDST\t"))

		writeIf := func(b bool, s string) {
			if b {
				w.Write([]byte(s))
			}
		}
		writeIf(anyType, "TYPE\t")
		writeIf(anyHour1, "HOUR1\t")
		writeIf(anyHour2, "HOUR2\t")
		writeIf(anyHour3, "HOUR3\t")
		writeIf(anyAC, "A/C\t")
		writeIf(anyAlt, "ALT\t")
		writeIf(anyDir, "DIR\t")
		w.Write([]byte("ROUTE\n"))

		for _, entry := range prdEntries {
			w.Write([]byte(entry.Seq + "\t" + entry.Depart + "\t" + entry.Arrive + "\t"))
			writeIf(anyType, entry.Type+"\t")
			writeIf(anyHour1, entry.Hours[0]+"\t")
			writeIf(anyHour2, entry.Hours[1]+"\t")
			writeIf(anyHour3, entry.Hours[2]+"\t")
			writeIf(anyAC, entry.Aircraft+"\t")
			writeIf(anyAlt, entry.Altitude+"\t")
			writeIf(anyDir, entry.Direction+"\t")
			w.Write([]byte(entry.Route + "\n"))
		}
		w.Flush()

		return StringConsoleEntry(result.String())
	}
}

type SetRouteCommand struct{}

func (*SetRouteCommand) Names() []string                    { return []string{"route", "rt"} }
func (*SetRouteCommand) Usage() string                      { return "<route...>" }
func (*SetRouteCommand) TakesAircraft() bool                { return true }
func (*SetRouteCommand) TakesController() bool              { return false }
func (*SetRouteCommand) AdditionalArgs() (min int, max int) { return 0, 1000 }
func (*SetRouteCommand) Help() string {
	return "Sets the specified aircraft's route to the one provided."
}
func (*SetRouteCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(amendFlightPlan(ac.Callsign, func(fp *FlightPlan) {
		fp.Route = strings.ToUpper(strings.Join(args, " "))
	}))
}

type DropTrackCommand struct{}

func (*DropTrackCommand) Names() []string                    { return []string{"drop", "dt", "refuse"} }
func (*DropTrackCommand) Usage() string                      { return "" }
func (*DropTrackCommand) TakesAircraft() bool                { return true }
func (*DropTrackCommand) TakesController() bool              { return false }
func (*DropTrackCommand) AdditionalArgs() (min int, max int) { return 0, 0 }

func (*DropTrackCommand) Help() string {
	return "Drops the track or refuses an offered handoff of the selected aircraft."
}
func (*DropTrackCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.InboundHandoffController != "" {
		return ErrorConsoleEntry(server.RejectHandoff(ac.Callsign))
	} else {
		return ErrorConsoleEntry(server.DropTrack(ac.Callsign))
	}
}

type HandoffCommand struct{}

func (*HandoffCommand) Names() []string                    { return []string{"handoff", "ho"} }
func (*HandoffCommand) Usage() string                      { return "" }
func (*HandoffCommand) TakesAircraft() bool                { return true }
func (*HandoffCommand) TakesController() bool              { return true }
func (*HandoffCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*HandoffCommand) Help() string {
	return "Hands off the specified aircraft to the specified controller."
}
func (*HandoffCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(server.Handoff(ac.Callsign, ctrl.Callsign))
}

type PointOutCommand struct{}

func (*PointOutCommand) Names() []string                    { return []string{"pointout", "po"} }
func (*PointOutCommand) Usage() string                      { return "" }
func (*PointOutCommand) TakesAircraft() bool                { return true }
func (*PointOutCommand) TakesController() bool              { return true }
func (*PointOutCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*PointOutCommand) Help() string {
	return "Points the specified aircraft out to the specified controller."
}
func (*PointOutCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(server.PointOut(ac.Callsign, ctrl.Callsign))
}

type TrackAircraftCommand struct{}

func (*TrackAircraftCommand) Names() []string                    { return []string{"track", "tr"} }
func (*TrackAircraftCommand) Usage() string                      { return "" }
func (*TrackAircraftCommand) TakesAircraft() bool                { return true }
func (*TrackAircraftCommand) TakesController() bool              { return false }
func (*TrackAircraftCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*TrackAircraftCommand) Help() string {
	return "Initiates a track or accepts an offered handoff for the specified aircraft."
}
func (*TrackAircraftCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.InboundHandoffController != "" {
		// it's being offered as a handoff
		return ErrorConsoleEntry(server.AcceptHandoff(ac.Callsign))
	} else if ac.OutboundHandoffController != "" {
		// We're trying to hand it off
		return ErrorConsoleEntry(server.CancelHandoff(args[0]))
	} else {
		return ErrorConsoleEntry(server.InitiateTrack(ac.Callsign))
	}
}

type PushFlightStripCommand struct{}

func (*PushFlightStripCommand) Names() []string                    { return []string{"push", "ps"} }
func (*PushFlightStripCommand) Usage() string                      { return "" }
func (*PushFlightStripCommand) TakesAircraft() bool                { return true }
func (*PushFlightStripCommand) TakesController() bool              { return true }
func (*PushFlightStripCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*PushFlightStripCommand) Help() string {
	return "Pushes the aircraft's flight strip to the specified controller."
}
func (*PushFlightStripCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(server.PushFlightStrip(ac.Callsign, ctrl.Callsign))
}

type FindCommand struct{}

func (*FindCommand) Names() []string { return []string{"find"} }
func (*FindCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*FindCommand) TakesAircraft() bool                { return false }
func (*FindCommand) TakesController() bool              { return false }
func (*FindCommand) AdditionalArgs() (min int, max int) { return 1, 1 }
func (*FindCommand) Help() string {
	return "Finds the specified object and highlights it in any radar scopes in which it is visible."
}
func (*FindCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	var pos Point2LL
	name := strings.ToUpper(args[0])
	aircraft := matchingAircraft(name)
	if len(aircraft) == 1 {
		pos = aircraft[0].Position()
	} else if len(aircraft) > 1 {
		callsigns := MapSlice(aircraft, func(a *Aircraft) string { return a.Callsign })
		return ErrorStringConsoleEntry("Multiple aircraft match: " + strings.Join(callsigns, ", "))
	} else {
		var ok bool
		if pos, ok = database.Locate(name); !ok {
			return ErrorStringConsoleEntry(args[0] + ": no matches found")
		}
	}

	positionConfig.highlightedLocation = pos
	positionConfig.highlightedLocationEndTime = time.Now().Add(3 * time.Second)
	return nil
}

type MITCommand struct{}

func (*MITCommand) Names() []string                    { return []string{"mit"} }
func (*MITCommand) Usage() string                      { return "" }
func (*MITCommand) TakesAircraft() bool                { return false }
func (*MITCommand) TakesController() bool              { return false }
func (*MITCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*MITCommand) Help() string {
	return "With no aircraft selected, this clears the current miles in trail list. " +
		"Otherwise, the selected aircraft is added to it."
}
func (*MITCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if positionConfig.selectedAircraft == nil {
		// clear it
		positionConfig.mit = nil
	} else {
		positionConfig.mit = append(positionConfig.mit, ac)
	}

	result := "Current MIT list: "
	for _, ac := range positionConfig.mit {
		result += ac.Callsign + " "
	}
	return StringConsoleEntry(result)
}

type DrawRouteCommand struct{}

func (*DrawRouteCommand) Names() []string                    { return []string{"drawroute", "dr"} }
func (*DrawRouteCommand) Usage() string                      { return "" }
func (*DrawRouteCommand) TakesAircraft() bool                { return true }
func (*DrawRouteCommand) TakesController() bool              { return false }
func (*DrawRouteCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*DrawRouteCommand) Help() string {
	return "Draws the route of the specified aircraft in any radar scopes in which it is visible."
}
func (*DrawRouteCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if ac.FlightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	} else {
		positionConfig.drawnRoute = ac.FlightPlan.DepartureAirport + " " + ac.FlightPlan.Route + " " +
			ac.FlightPlan.ArrivalAirport
		positionConfig.drawnRouteEndTime = time.Now().Add(5 * time.Second)
		return nil
	}
}

type InfoCommand struct{}

func (*InfoCommand) Names() []string { return []string{"i", "info"} }
func (*InfoCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*InfoCommand) TakesAircraft() bool                { return false }
func (*InfoCommand) TakesController() bool              { return false }
func (*InfoCommand) AdditionalArgs() (min int, max int) { return 0, 1 }

func (*InfoCommand) Help() string {
	return "Prints available information about the specified object."
}
func (*InfoCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	acInfo := func(ac *Aircraft) string {
		var result string
		var indent int
		if ac.FlightPlan == nil {
			result = ac.Callsign + ": no flight plan filed"
			indent = len(ac.Callsign) + 1
		} else {
			result, indent = ac.GetFormattedFlightPlan(true)
			result = strings.TrimRight(result, "\n")
		}

		indstr := fmt.Sprintf("%*c", indent, ' ')
		if u := server.GetUser(ac.Callsign); u != nil {
			result += fmt.Sprintf("\n%spilot: %s %s (%s)", indstr, u.Name, u.Rating, u.Note)
		}
		if ac.FlightPlan != nil {
			if tel := ac.Telephony(); tel != "" {
				result += fmt.Sprintf("\n%stele:  %s", indstr, tel)
			}

			if a, ok := database.LookupAircraftType(ac.FlightPlan.BaseType()); ok {
				result += fmt.Sprintf("\n%stype:  %s", indstr, a.Name)
			}
		}
		if ac.TrackingController != "" {
			result += fmt.Sprintf("\n%sctrl:  %s", indstr, ac.TrackingController)
		}
		if ac.InboundHandoffController != "" {
			result += fmt.Sprintf("\n%sin h/o:  %s", indstr, ac.InboundHandoffController)
		}
		if ac.OutboundHandoffController != "" {
			result += fmt.Sprintf("\n%sout h/o: %s", indstr, ac.OutboundHandoffController)
		}
		if ac.FlightPlan != nil {
			if acType, ok := database.LookupAircraftType(ac.FlightPlan.BaseType()); ok {
				result += fmt.Sprintf("\n%stype:  %d engine %s (%s)", indstr, acType.NumEngines(),
					acType.EngineType(), acType.Manufacturer)
				result += fmt.Sprintf("\n%sappr:  %s", indstr, acType.ApproachCategory())
				result += fmt.Sprintf("\n%srecat: %s", indstr, acType.RECATCategory())
			}
		}
		if ac.HaveTrack() {
			result += fmt.Sprintf("\n%scralt: %d", indstr, ac.Altitude())
		}
		if ac.Squawk != ac.AssignedSquawk {
			result += fmt.Sprintf("\n%s*** Actual squawk: %s", indstr, ac.Squawk)
		}
		if ac.LostTrack(server.CurrentTime()) {
			result += fmt.Sprintf("\n%s*** Lost Track!", indstr)
		}
		return result
	}

	if len(args) == 1 {
		name := strings.ToUpper(args[0])

		// e.g. "fft" matches both a VOR and a callsign, so report both...
		var info []string
		if navaid, ok := database.FAA.navaids[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s %s %s", name, stopShouting(navaid.Name),
				navaid.Type, navaid.Location.DMSString()))
		}
		if fix, ok := database.FAA.fixes[name]; ok {
			info = append(info, fmt.Sprintf("%s: Fix %s", name, fix.Location.DMSString()))
		}
		if ap, ok := database.FAA.airports[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s: %s, alt %d", name, stopShouting(ap.Name),
				ap.Location.DMSString(), ap.Elevation))
		}
		if cs, ok := database.callsigns[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s (%s)", name, cs.Telephony, cs.Company))
		}
		if ct := server.GetController(name); ct != nil {
			info = append(info, fmt.Sprintf("%s (%s) @ %s, range %d", ct.Callsign,
				ct.Rating, ct.Frequency.String(), ct.ScopeRange))
			_ = server.RequestControllerATIS(name)
			if u := server.GetUser(name); u != nil {
				info = append(info, fmt.Sprintf("%s %s (%s)", u.Name, u.Rating, u.Note))
			}
		}
		if ac, ok := database.LookupAircraftType(name); ok {
			indent := fmt.Sprintf("%*c", len(ac.Name)+2, ' ')
			info = append(info, fmt.Sprintf("%s: %d engine %s (%s)",
				ac.Name, ac.NumEngines(), ac.EngineType(), ac.Manufacturer))
			info = append(info, indent+"Approach: "+ac.ApproachCategory())
			info = append(info, indent+"RECAT: "+ac.RECATCategory())
		}

		if len(info) > 0 {
			return StringConsoleEntry(strings.Join(info, "\n"))
		}

		aircraft := matchingAircraft(name)
		if len(aircraft) == 1 {
			return StringConsoleEntry(acInfo(aircraft[0]))
		} else if len(aircraft) > 1 {
			callsigns := MapSlice(aircraft, func(a *Aircraft) string { return a.Callsign })
			return ErrorStringConsoleEntry("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			return ErrorStringConsoleEntry(name + ": unknown")
		}
	} else if positionConfig.selectedAircraft != nil {
		return StringConsoleEntry(acInfo(positionConfig.selectedAircraft))
	} else {
		return ErrorStringConsoleEntry(cmd + ": must either specify a fix/VOR/etc. or select an aircraft")
	}
}

type TrafficCommand struct{}

func (*TrafficCommand) Names() []string                    { return []string{"traffic", "tf"} }
func (*TrafficCommand) Usage() string                      { return "" }
func (*TrafficCommand) TakesAircraft() bool                { return true }
func (*TrafficCommand) TakesController() bool              { return false }
func (*TrafficCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*TrafficCommand) Help() string {
	return "Summarizes information related to nearby traffic for the specified aircraft."
}
func (*TrafficCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	type Traffic struct {
		ac       *Aircraft
		distance float32
	}
	now := server.CurrentTime()
	filter := func(a *Aircraft) bool {
		return a.Callsign != ac.Callsign && !a.LostTrack(now) && !a.OnGround()
	}

	lateralLimit := float32(6.)
	verticalLimit := 1500

	var traffic []Traffic
	for _, other := range server.GetFilteredAircraft(filter) {
		ldist := nmdistance2ll(ac.Position(), other.Position())
		vdist := abs(ac.Altitude() - other.Altitude())
		if ldist < lateralLimit && vdist < verticalLimit {
			traffic = append(traffic, Traffic{other, ldist})
		}
	}

	sort.Slice(traffic, func(i, j int) bool {
		if traffic[i].distance == traffic[j].distance {
			return traffic[i].ac.Callsign < traffic[j].ac.Callsign
		}
		return traffic[i].distance < traffic[j].distance
	})

	str := ""
	for _, t := range traffic {
		alt := (t.ac.Altitude() + 250) / 500 * 500
		hto := headingp2ll(ac.Position(), t.ac.Position(), database.MagneticVariation)
		hdiff := hto - ac.Heading()
		clock := headingAsHour(hdiff)
		actype := "???"
		if t.ac.FlightPlan != nil {
			actype = t.ac.FlightPlan.AircraftType
		}
		str += fmt.Sprintf("  %-10s %2d o'c %2d mi %2s bound %-10s %5d' [%s]\n",
			ac.Callsign, clock, int(t.distance+0.5),
			shortCompass(t.ac.Heading()), actype, int(alt), t.ac.Callsign)
	}

	return StringConsoleEntry(str)
}

type TimerCommand struct{}

func (*TimerCommand) Names() []string { return []string{"timer"} }
func (*TimerCommand) Usage() string {
	return "<minutes> <message...>"
}
func (*TimerCommand) TakesAircraft() bool                { return false }
func (*TimerCommand) TakesController() bool              { return false }
func (*TimerCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*TimerCommand) Help() string {
	return "Starts a timer for the specified number of minutes with the associated message."
}
func (*TimerCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if minutes, err := strconv.ParseFloat(args[0], 64); err != nil {
		return ErrorStringConsoleEntry(args[0] + ": expected time in minutes")
	} else {
		end := time.Now().Add(time.Duration(minutes * float64(time.Minute)))
		timer := TimerReminderItem{end: end, note: strings.Join(args[1:], " ")}

		positionConfig.timers = append(positionConfig.timers, timer)
		sort.Slice(positionConfig.timers, func(i, j int) bool {
			return positionConfig.timers[i].end.Before(positionConfig.timers[j].end)
		})

		return nil
	}
}

type ToDoCommand struct{}

func (*ToDoCommand) Names() []string                    { return []string{"todo"} }
func (*ToDoCommand) Usage() string                      { return "<message...>" }
func (*ToDoCommand) TakesAircraft() bool                { return false }
func (*ToDoCommand) TakesController() bool              { return false }
func (*ToDoCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*ToDoCommand) Help() string {
	return "Adds a todo with the associated message to the todo list."
}
func (*ToDoCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	note := strings.Join(args[0:], " ")
	positionConfig.todos = append(positionConfig.todos, ToDoReminderItem{note: note})
	return nil
}

type EchoCommand struct{}

func (*EchoCommand) Names() []string                    { return []string{"echo"} }
func (*EchoCommand) Usage() string                      { return "<message...>" }
func (*EchoCommand) TakesAircraft() bool                { return false }
func (*EchoCommand) TakesController() bool              { return false }
func (*EchoCommand) AdditionalArgs() (min int, max int) { return 0, 1000 }
func (*EchoCommand) Help() string {
	return "Prints the parameters given to it."
}
func (*EchoCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return StringConsoleEntry(strings.Join(args, " "))
}

type ATCChatCommand struct{}

func (*ATCChatCommand) Names() []string                    { return []string{"/atc"} }
func (*ATCChatCommand) Usage() string                      { return "<message...>" }
func (*ATCChatCommand) TakesAircraft() bool                { return false }
func (*ATCChatCommand) TakesController() bool              { return false }
func (*ATCChatCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*ATCChatCommand) Help() string {
	return "Send the specified message to all in-range controllers."
}
func (*ATCChatCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	tm := TextMessage{messageType: TextATC, contents: strings.Join(args, " ")}
	return cli.sendTextMessage(tm)
}

type PrivateMessageCommand struct{}

func (*PrivateMessageCommand) Names() []string { return []string{"/dm"} }
func (*PrivateMessageCommand) Usage() string {
	return "<recipient> [message]"
}
func (*PrivateMessageCommand) TakesAircraft() bool                { return false }
func (*PrivateMessageCommand) TakesController() bool              { return false }
func (*PrivateMessageCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*PrivateMessageCommand) Help() string {
	return "Send the specified message to the recipient (aircraft or controller)."
}
func (*PrivateMessageCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	var callsign string
	if positionConfig.selectedAircraft != nil {
		callsign = positionConfig.selectedAircraft.Callsign
	} else if ctrl := server.GetController(args[0]); ctrl != nil {
		callsign = ctrl.Callsign
		args = args[1:]
	} else {
		return ErrorStringConsoleEntry(args[0] + ": message recipient unknown")
	}

	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   callsign,
		contents:    strings.Join(args[0:], " ")}
	return cli.sendTextMessage(tm)
}

type TransmitCommand struct{}

func (*TransmitCommand) Names() []string                    { return []string{"/tx"} }
func (*TransmitCommand) Usage() string                      { return "[message]" }
func (*TransmitCommand) TakesAircraft() bool                { return false }
func (*TransmitCommand) TakesController() bool              { return false }
func (*TransmitCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*TransmitCommand) Help() string {
	return "Transmits the text message on the primed frequency."
}
func (*TransmitCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if positionConfig.primaryFrequency == Frequency(0) {
		return ErrorStringConsoleEntry("Not primed on a frequency")
	} else {
		tx, _ := FlattenMap(FilterMap(positionConfig.txFrequencies,
			func(f Frequency, on *bool) bool {
				return on != nil && *on
			}))

		tm := TextMessage{
			messageType: TextFrequency,
			frequencies: tx,
			contents:    strings.Join(args, " ")}

		if positionConfig.selectedAircraft != nil {
			tm.contents = positionConfig.selectedAircraft.Callsign + ": " + tm.contents
		}

		return cli.sendTextMessage(tm)
	}
}

type WallopCommand struct{}

func (*WallopCommand) Names() []string                    { return []string{"/wallop"} }
func (*WallopCommand) Usage() string                      { return "[message]" }
func (*WallopCommand) TakesAircraft() bool                { return false }
func (*WallopCommand) TakesController() bool              { return false }
func (*WallopCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*WallopCommand) Help() string {
	return "Send the specified message to all online supervisors."
}
func (*WallopCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	tm := TextMessage{messageType: TextWallop, contents: strings.Join(args, " ")}
	if positionConfig.selectedAircraft != nil {
		tm.contents = positionConfig.selectedAircraft.Callsign + ": " + tm.contents
	}
	return cli.sendTextMessage(tm)
}

type RequestATISCommand struct{}

func (*RequestATISCommand) Names() []string                    { return []string{"/atis"} }
func (*RequestATISCommand) Usage() string                      { return "<controller>" }
func (*RequestATISCommand) TakesAircraft() bool                { return false }
func (*RequestATISCommand) TakesController() bool              { return true }
func (*RequestATISCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*RequestATISCommand) Help() string {
	return "Request the ATIS of the specified controller."
}
func (*RequestATISCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	return ErrorConsoleEntry(server.RequestControllerATIS(ctrl.Callsign))
}

type ContactMeCommand struct{}

func (*ContactMeCommand) Names() []string                    { return []string{"/contactme", "/cme"} }
func (*ContactMeCommand) Usage() string                      { return "" }
func (*ContactMeCommand) TakesAircraft() bool                { return true }
func (*ContactMeCommand) TakesController() bool              { return false }
func (*ContactMeCommand) AdditionalArgs() (min int, max int) { return 0, 0 }
func (*ContactMeCommand) Help() string {
	return "Send a \"contact me\" request to the specified aircraft."
}
func (*ContactMeCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	if positionConfig.primaryFrequency == Frequency(0) {
		return ErrorStringConsoleEntry("Unable to send contactme since no prime frequency is set.")
	}

	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   ac.Callsign,
		contents: fmt.Sprintf("Please contact me on %s. Please do not respond via private "+
			"message - use the frequency instead.", positionConfig.primaryFrequency),
	}

	return cli.sendTextMessage(tm)
}

type MessageReplyCommand struct{}

func (*MessageReplyCommand) Names() []string {
	return []string{"/0", "/1", "/2", "/3", "/4", "/5", "/6", "/7", "/8", "/9"}
}
func (*MessageReplyCommand) Usage() string                      { return "<message...>" }
func (*MessageReplyCommand) TakesAircraft() bool                { return false }
func (*MessageReplyCommand) TakesController() bool              { return false }
func (*MessageReplyCommand) AdditionalArgs() (min int, max int) { return 1, 1000 }
func (*MessageReplyCommand) Help() string {
	return "Send the specified message to the recipient."
}
func (*MessageReplyCommand) Run(cmd string, ac *Aircraft, ctrl *Controller, args []string, cli *CLIPane) []*ConsoleEntry {
	id := int([]byte(cmd)[1] - '0')
	if id < 0 || id > len(cli.messageReplyRecipients) {
		return ErrorStringConsoleEntry(cmd + ": unexpected reply id")
	}
	if cli.messageReplyRecipients[id] == nil {
		return ErrorStringConsoleEntry(cmd + ": no conversation with that id")
	}

	// Initialize the new message using the reply template.
	tm := *cli.messageReplyRecipients[id]
	tm.contents = strings.Join(args, " ")

	return cli.sendTextMessage(tm)
}
