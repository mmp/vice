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

///////////////////////////////////////////////////////////////////////////
// CLICommands

type CommandArgsFormat int

const (
	// Only one of aircraft, controller, or string should be set.
	CommandArgsAircraft = 1 << iota
	CommandArgsController
	CommandArgsString
	CommandArgsOptional // Can only be at the end. Allows 0 or 1 args.
	CommandArgsMultiple // Can only be at the end. Allows 0, 1, 2, ... args
)

type Command interface {
	Names() []string
	Help() string
	Usage() string
	Syntax(isAircraftSelected bool) []CommandArgsFormat
	Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry
}

var (
	cliCommands []Command = []Command{
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
		&PrivateMessageSelectedCommand{},
		&TransmitCommand{},
		&WallopCommand{},
		&RequestATISCommand{},
		&ContactMeCommand{},
		&MessageReplyCommand{},

		&EchoCommand{},
	}
)

func checkCommands(cmds []Command) {
	seen := make(map[string]interface{})
	for _, c := range cmds {
		for _, name := range c.Names() {
			if _, ok := seen[name]; ok {
				lg.Errorf("%s: command has multiple definitions", name)
			} else {
				seen[name] = nil
			}
		}

		syntax := c.Syntax(false)
		for i := 0; i < len(syntax)-1; i++ {
			if syntax[i]&CommandArgsOptional != 0 {
				lg.Errorf("%v: optional arguments can only be at the end", c.Names())
			}
			if syntax[i]&CommandArgsMultiple != 0 {
				lg.Errorf("%v: multiple arguments can only be at the end", c.Names())
			}
			if syntax[i]&CommandArgsOptional != 0 && syntax[i]&CommandArgsMultiple != 0 {
				lg.Errorf("%v: cannot specify both optional and multiple arguments", c.Names())
			}
		}
	}
}

func getCallsign(args []string) (string, []string) {
	if positionConfig.selectedAircraft != nil {
		return positionConfig.selectedAircraft.Callsign(), args
	} else if len(args) == 0 {
		lg.Errorf("Insufficient args passed to getCallsign!")
		return "", nil
	} else {
		return args[0], args[1:]
	}
}

type SetACTypeCommand struct{}

func (*SetACTypeCommand) Names() []string { return []string{"actype", "ac"} }
func (*SetACTypeCommand) Help() string {
	return "Sets the aircraft's type."
}
func (*SetACTypeCommand) Usage() string {
	return "<callsign> <type>"
}
func (*SetACTypeCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetACTypeCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	err := amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.actype = strings.ToUpper(args[0])
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
func (sa *SetAltitudeCommand) Usage() string {
	return "<callsign> <altitude>"
}
func (sa *SetAltitudeCommand) Help() string {
	if sa.isTemporary {
		return "Sets the aircraft's temporary clearance altitude."
	} else {
		return "Sets the aircraft's clearance altitude."
	}
}
func (*SetAltitudeCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (sa *SetAltitudeCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)

	altitude, err := strconv.Atoi(args[0])
	if err != nil {
		return ErrorConsoleEntry(err)
	}
	if altitude < 1000 {
		altitude *= 100
	}

	if sa.isTemporary {
		err = server.SetTemporaryAltitude(callsign, altitude)
	} else {
		err = amendFlightPlan(callsign, func(fp *FlightPlan) {
			fp.altitude = altitude
		})
	}
	return ErrorConsoleEntry(err)
}

type SetArrivalCommand struct{}

func (*SetArrivalCommand) Names() []string { return []string{"arrive", "ar"} }
func (*SetArrivalCommand) Usage() string {
	return "<callsign> <airport>"
}
func (*SetArrivalCommand) Help() string {
	return "Sets the aircraft's arrival airport."
}
func (*SetArrivalCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetArrivalCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	if len(args[0]) > 5 {
		return ErrorConsoleEntry(ErrAirportTooLong)
	}
	err := amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.arrive = strings.ToUpper(args[0])
	})
	return ErrorConsoleEntry(err)
}

type SetDepartureCommand struct{}

func (*SetDepartureCommand) Names() []string { return []string{"depart", "dp"} }
func (*SetDepartureCommand) Usage() string {
	return "<callsign> <airport>"
}
func (*SetDepartureCommand) Help() string {
	return "Sets the aircraft's departure airport"
}
func (*SetDepartureCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetDepartureCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	if len(args[0]) > 5 {
		return ErrorConsoleEntry(ErrAirportTooLong)
	}
	err := amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.depart = strings.ToUpper(args[0])
	})
	return ErrorConsoleEntry(err)
}

type SetEquipmentSuffixCommand struct{}

func (*SetEquipmentSuffixCommand) Names() []string { return []string{"equip", "eq"} }
func (*SetEquipmentSuffixCommand) Usage() string {
	return "<callsign> <suffix>"
}
func (*SetEquipmentSuffixCommand) Help() string {
	return "Sets the aircraft's equipment suffix."
}
func (*SetEquipmentSuffixCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetEquipmentSuffixCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	if ac := server.GetAircraft(callsign); ac == nil {
		return ErrorConsoleEntry(ErrNoAircraftForCallsign)
	} else if ac.flightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlanFiled)
	} else {
		atype := ac.flightPlan.TypeWithoutSuffix()
		suffix := strings.ToUpper(args[0])
		if suffix[0] != '/' {
			suffix = "/" + suffix
		}
		ac.flightPlan.actype = atype + suffix
		err := server.AmendFlightPlan(callsign, *ac.flightPlan)
		return ErrorConsoleEntry(err)
	}
}

type SetIFRCommand struct{}

func (*SetIFRCommand) Names() []string { return []string{"ifr"} }
func (*SetIFRCommand) Usage() string {
	return "<callsign>"
}
func (*SetIFRCommand) Help() string {
	return "Marks the aircraft as an IFR flight."
}
func (*SetIFRCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*SetIFRCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	err := amendFlightPlan(callsign, func(fp *FlightPlan) { fp.rules = IFR })
	return ErrorConsoleEntry(err)
}

type SetScratchpadCommand struct{}

func (*SetScratchpadCommand) Names() []string { return []string{"scratchpad", "sp"} }
func (*SetScratchpadCommand) Usage() string {
	return "<callsign> <contents--optional>"
}
func (*SetScratchpadCommand) Help() string {
	return "Sets the aircraft's scratchpad. If no contents are specified, the scratchpad is cleared."
}
func (*SetScratchpadCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsOptional}
	}
}
func (*SetScratchpadCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	if len(args) == 0 {
		// clear scratchpad
		return ErrorConsoleEntry(server.SetScratchpad(callsign, ""))
	} else {
		return ErrorConsoleEntry(server.SetScratchpad(callsign, strings.ToUpper(args[0])))
	}
}

type SetSquawkCommand struct{}

func (*SetSquawkCommand) Names() []string { return []string{"squawk", "sq"} }
func (*SetSquawkCommand) Usage() string {
	return "<aircraft> <squawk--optional>"
}
func (*SetSquawkCommand) Help() string {
	return "Sets the aircraft's squawk code. If no code is provided and the aircraft is IFR, a code is assigned automatically."
}
func (*SetSquawkCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsOptional}
	}
}
func (*SetSquawkCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	if len(args) == 0 {
		return ErrorConsoleEntry(server.SetSquawkAutomatic(callsign))
	} else {
		squawk, err := ParseSquawk(args[0])
		if err != nil {
			return ErrorConsoleEntry(err)
		}
		return ErrorConsoleEntry(server.SetSquawk(callsign, squawk))
	}
}

type SetVoiceCommand struct{}

func (*SetVoiceCommand) Names() []string { return []string{"voice", "v"} }
func (*SetVoiceCommand) Usage() string {
	return "<aircraft> <voice type:v, r, or t>"
}
func (*SetVoiceCommand) Help() string {
	return "Sets the aircraft's voice communications type."
}
func (*SetVoiceCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetVoiceCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)

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

	return ErrorConsoleEntry(server.SetVoiceType(callsign, cap))
}

type SetVFRCommand struct{}

func (*SetVFRCommand) Names() []string { return []string{"vfr"} }
func (*SetVFRCommand) Usage() string {
	return "<callsign>"
}
func (*SetVFRCommand) Help() string {
	return "Marks the aircraft as a VFR flight."
}
func (*SetVFRCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*SetVFRCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	err := amendFlightPlan(callsign, func(fp *FlightPlan) { fp.rules = VFR })
	return ErrorConsoleEntry(err)
}

type EditRouteCommand struct{}

func (*EditRouteCommand) Names() []string { return []string{"editroute", "er"} }
func (*EditRouteCommand) Usage() string {
	return "<callsign>"
}
func (*EditRouteCommand) Help() string {
	return "Loads the aircraft's route into the command buffer for editing using the \"route\" command."
}
func (*EditRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*EditRouteCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return ErrorConsoleEntry(ErrNoAircraftForCallsign)
	}
	if ac.flightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	cli.input.cmd = "route " + callsign + " "
	cli.input.cursor = len(cli.input.cmd)
	cli.input.cmd += ac.flightPlan.route

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

func (*NYPRDCommand) Names() []string { return []string{"nyprd"} }
func (*NYPRDCommand) Usage() string {
	return "<callsign>"
}
func (*NYPRDCommand) Help() string {
	return "Looks up the aircraft's route in the ZNY preferred route database."
}
func (*NYPRDCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*NYPRDCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return ErrorConsoleEntry(ErrNoAircraftForCallsign)
	}
	if ac.flightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	depart, arrive := ac.flightPlan.depart, ac.flightPlan.arrive
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

func (*PRDCommand) Names() []string { return []string{"faaprd"} }
func (*PRDCommand) Usage() string {
	return "<callsign>"
}
func (*PRDCommand) Help() string {
	return "Looks up the aircraft's route in the FAA preferred route database."
}
func (*PRDCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*PRDCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return ErrorConsoleEntry(ErrNoAircraftForCallsign)
	}
	if ac.flightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	depart, arrive := ac.flightPlan.depart, ac.flightPlan.arrive
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

func (*SetRouteCommand) Names() []string { return []string{"route", "rt"} }
func (*SetRouteCommand) Usage() string {
	return "<callsign> <route...>"
}
func (*SetRouteCommand) Help() string {
	return "Sets the specified aircraft's route to the one provided."
}
func (*SetRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsMultiple}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsMultiple}
	}
}
func (*SetRouteCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	err := amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.route = strings.ToUpper(strings.Join(args, " "))
	})
	return ErrorConsoleEntry(err)
}

type DropTrackCommand struct{}

func (*DropTrackCommand) Names() []string { return []string{"drop", "dt", "refuse"} }
func (*DropTrackCommand) Usage() string {
	return "<callsign>"
}
func (*DropTrackCommand) Help() string {
	return "Drops the track or refuses an offered handoff of the selected aircraft."
}
func (*DropTrackCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*DropTrackCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	if server.InboundHandoffController(callsign) != "" {
		return ErrorConsoleEntry(server.RejectHandoff(callsign))
	} else {
		return ErrorConsoleEntry(server.DropTrack(callsign))
	}
}

type HandoffCommand struct{}

func (*HandoffCommand) Names() []string { return []string{"handoff", "ho"} }
func (*HandoffCommand) Usage() string {
	return "<callsign> <controller>"
}
func (*HandoffCommand) Help() string {
	return "Hands off the specified aircraft to the specified controller."
}
func (*HandoffCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsController}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsController}
	}
}
func (*HandoffCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	return ErrorConsoleEntry(server.Handoff(callsign, args[0]))
}

type PointOutCommand struct{}

func (*PointOutCommand) Names() []string { return []string{"pointout", "po"} }
func (*PointOutCommand) Usage() string {
	return "<callsign> <controller>"
}
func (*PointOutCommand) Help() string {
	return "Points the specified aircraft out to the specified controller."
}
func (*PointOutCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsController}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsController}
	}
}
func (*PointOutCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	return ErrorConsoleEntry(server.PointOut(callsign, args[0]))
}

type TrackAircraftCommand struct{}

func (*TrackAircraftCommand) Names() []string { return []string{"track", "tr"} }
func (*TrackAircraftCommand) Usage() string {
	return "<callsign>"
}
func (*TrackAircraftCommand) Help() string {
	return "Initiates a track or accepts an offered handoff for the specified aircraft."
}
func (*TrackAircraftCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*TrackAircraftCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	if server.InboundHandoffController(callsign) != "" {
		// it's being offered as a handoff
		return ErrorConsoleEntry(server.AcceptHandoff(callsign))
	} else {
		return ErrorConsoleEntry(server.InitiateTrack(callsign))
	}
}

type PushFlightStripCommand struct{}

func (*PushFlightStripCommand) Names() []string { return []string{"push", "ps"} }
func (*PushFlightStripCommand) Usage() string {
	return "<callsign> <controller>"
}
func (*PushFlightStripCommand) Help() string {
	return "Pushes the aircraft's flight strip to the specified controller."
}
func (*PushFlightStripCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsController}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsController}
	}
}
func (*PushFlightStripCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	return ErrorConsoleEntry(server.PushFlightStrip(callsign, args[0]))
}

type FindCommand struct{}

func (*FindCommand) Names() []string { return []string{"find"} }
func (*FindCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*FindCommand) Help() string {
	return "Finds the specified object and highlights it in any radar scopes in which it is visible."
}
func (*FindCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft | CommandArgsString}
	}
}
func (*FindCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	var pos Point2LL
	if len(args) == 0 && positionConfig.selectedAircraft != nil {
		pos = positionConfig.selectedAircraft.Position()
	} else {
		name := strings.ToUpper(args[0])

		aircraft := matchingAircraft(name)
		if len(aircraft) == 1 {
			pos = aircraft[0].Position()
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return ErrorStringConsoleEntry("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			var ok bool
			if pos, ok = database.Locate(name); !ok {
				return ErrorStringConsoleEntry(args[0] + ": no matches found")
			}
		}
	}
	positionConfig.highlightedLocation = pos
	positionConfig.highlightedLocationEndTime = time.Now().Add(3 * time.Second)
	return nil
}

type MITCommand struct{}

func (*MITCommand) Names() []string { return []string{"mit"} }
func (*MITCommand) Usage() string {
	return "<zero, one, or more callsigns...>"
}
func (*MITCommand) Help() string {
	return "With no callsigns, this clears the current miles in trail list. " +
		"Otherwise, the specified aircraft are added to it."
}
func (*MITCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsAircraft | CommandArgsMultiple}
}
func (*MITCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	if len(args) == 0 {
		// clear it
		positionConfig.mit = nil
	} else {
		for _, callsign := range args {
			ac := server.GetAircraft(callsign)
			if ac == nil {
				return ErrorStringConsoleEntry(callsign + ": aircraft does not exist")
			}

			positionConfig.mit = append(positionConfig.mit, ac)
		}
	}

	result := "Current MIT list: "
	for _, ac := range positionConfig.mit {
		result += ac.Callsign() + " "
	}
	return StringConsoleEntry(result)
}

type DrawRouteCommand struct{}

func (*DrawRouteCommand) Names() []string { return []string{"drawroute", "dr"} }
func (*DrawRouteCommand) Usage() string {
	return "<callsign>"
}
func (*DrawRouteCommand) Help() string {
	return "Draws the route of the specified aircraft in any radar scopes in which it is visible."
}
func (*DrawRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*DrawRouteCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	var ac *Aircraft
	if len(args) == 0 {
		ac = positionConfig.selectedAircraft
	} else {
		aircraft := matchingAircraft(strings.ToUpper(args[0]))
		if len(aircraft) == 1 {
			ac = aircraft[0]
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return ErrorStringConsoleEntry("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			return ErrorStringConsoleEntry(args[0] + ": no matches found")
		}
	}
	if ac.flightPlan == nil {
		return ErrorConsoleEntry(ErrNoFlightPlan)
	}

	positionConfig.drawnRoute = ac.flightPlan.depart + " " + ac.flightPlan.route + " " +
		ac.flightPlan.arrive
	positionConfig.drawnRouteEndTime = time.Now().Add(5 * time.Second)
	return nil
}

type InfoCommand struct{}

func (*InfoCommand) Names() []string { return []string{"i", "info"} }
func (*InfoCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*InfoCommand) Help() string {
	return "Prints available information about the specified object."
}
func (*InfoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft | CommandArgsString}
	}
}
func (*InfoCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	acInfo := func(ac *Aircraft) string {
		var result string
		var indent int
		if ac.flightPlan == nil {
			result = ac.Callsign() + ": no flight plan filed"
			indent = len(ac.Callsign()) + 1
		} else {
			result, indent = ac.GetFormattedFlightPlan(true)
			result = strings.TrimRight(result, "\n")
		}

		indstr := fmt.Sprintf("%*c", indent, ' ')
		if u := server.GetUser(ac.Callsign()); u != nil {
			result += fmt.Sprintf("\n%spilot: %s %s (%s)", indstr, u.name, u.rating, u.note)
		}
		if ac.flightPlan != nil {
			if tel := ac.Telephony(); tel != "" {
				result += fmt.Sprintf("\n%stele:  %s", indstr, tel)
			}
		}
		if c := server.GetTrackingController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sTracked by: %s", indstr, c)
		}
		if c := server.InboundHandoffController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sInbound handoff from %s", indstr, c)
		}
		if c := server.OutboundHandoffController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sOutbound handoff from %s", indstr, c)
		}
		if ac.squawk != ac.assignedSquawk {
			result += fmt.Sprintf("\n%s*** Actual squawk: %s", indstr, ac.squawk)
		}
		if ac.LostTrack(server.CurrentTime()) {
			result += fmt.Sprintf("\n%s*** Lost Track!", indstr)
		}
		return result
	}

	if len(args) == 0 && positionConfig.selectedAircraft != nil {
		return StringConsoleEntry(acInfo(positionConfig.selectedAircraft))
	} else {
		name := strings.ToUpper(args[0])

		// e.g. "fft" matches both a VOR and a callsign, so report both...
		var info []string
		if navaid, ok := database.FAA.navaids[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s %s %s", name, stopShouting(navaid.name),
				navaid.navtype, navaid.location))
		}
		if fix, ok := database.FAA.fixes[name]; ok {
			info = append(info, fmt.Sprintf("%s: Fix %s", name, fix.location))
		}
		if ap, ok := database.FAA.airports[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s: %s, alt %d", name, stopShouting(ap.name),
				ap.location, ap.elevation))
		}
		if cs, ok := database.callsigns[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s (%s)", name, cs.telephony, cs.company))
		}
		if ct := server.GetController(name); ct != nil {
			info = append(info, fmt.Sprintf("%s (%s) @ %s, range %d", ct.callsign,
				ct.rating, ct.frequency.String(), ct.scopeRange))
			if u := server.GetUser(name); u != nil {
				info = append(info, fmt.Sprintf("%s %s (%s)", u.name, u.rating, u.note))
			}
		}

		if len(info) > 0 {
			return StringConsoleEntry(strings.Join(info, "\n"))
		}

		aircraft := matchingAircraft(name)
		if len(aircraft) == 1 {
			return StringConsoleEntry(acInfo(aircraft[0]))
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return ErrorStringConsoleEntry("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			return ErrorStringConsoleEntry(name + ": unknown")
		}
	}
}

type TrafficCommand struct{}

func (*TrafficCommand) Names() []string { return []string{"traffic", "tf"} }
func (*TrafficCommand) Usage() string {
	return "<callsign>"
}
func (*TrafficCommand) Help() string {
	return "Summarizes information related to nearby traffic for the specified aircraft."
}
func (*TrafficCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsAircraft}
}
func (*TrafficCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return ErrorStringConsoleEntry(callsign + ": aircraft does not exist")
	}

	type Traffic struct {
		ac       *Aircraft
		distance float32
	}
	now := server.CurrentTime()
	filter := func(a *Aircraft) bool {
		return a.Callsign() == ac.Callsign() || a.LostTrack(now) || a.OnGround()
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
			return traffic[i].ac.Callsign() < traffic[j].ac.Callsign()
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
		if t.ac.flightPlan != nil {
			actype = t.ac.flightPlan.actype
		}
		str += fmt.Sprintf("  %-10s %2d o'c %2d mi %2s bound %-10s %5d' [%s]\n",
			ac.Callsign(), clock, int(t.distance+0.5),
			shortCompass(t.ac.Heading()), actype, int(alt), t.ac.Callsign())
	}

	return StringConsoleEntry(str)
}

type TimerCommand struct{}

func (*TimerCommand) Names() []string { return []string{"timer"} }
func (*TimerCommand) Usage() string {
	return "<minutes> <message...>"
}
func (*TimerCommand) Help() string {
	return "Starts a timer for the specified number of minutes with the associated message."
}
func (*TimerCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*TimerCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
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

func (*ToDoCommand) Names() []string { return []string{"todo"} }
func (*ToDoCommand) Usage() string {
	return "<message...>"
}
func (*ToDoCommand) Help() string {
	return "Adds a todo with the associated message to the todo list."
}
func (*ToDoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*ToDoCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	note := strings.Join(args[0:], " ")
	positionConfig.todos = append(positionConfig.todos, ToDoReminderItem{note: note})
	return nil
}

type EchoCommand struct{}

func (*EchoCommand) Names() []string { return []string{"echo"} }
func (*EchoCommand) Usage() string {
	return "<message...>"
}
func (*EchoCommand) Help() string {
	return "Prints the parameters given to it."
}
func (*EchoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*EchoCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	return StringConsoleEntry(strings.Join(args, " "))
}

type ATCChatCommand struct{}

func (*ATCChatCommand) Names() []string { return []string{"/atc"} }
func (*ATCChatCommand) Usage() string {
	return "[message]"
}
func (*ATCChatCommand) Help() string {
	return "Send the specified message to all in-range controllers."
}
func (*ATCChatCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*ATCChatCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	tm := TextMessage{messageType: TextATC, contents: strings.Join(args, " ")}
	return cli.sendTextMessage(tm)
}

type PrivateMessageCommand struct{}

func (*PrivateMessageCommand) Names() []string { return []string{"/dm"} }
func (*PrivateMessageCommand) Usage() string {
	return "<recipient> [message]"
}
func (*PrivateMessageCommand) Help() string {
	return "Send the specified message to the recipient (aircraft or controller)."
}
func (*PrivateMessageCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
	} else {
		return []CommandArgsFormat{CommandArgsString, CommandArgsString, CommandArgsString | CommandArgsMultiple}
	}
}
func (*PrivateMessageCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	callsign, args := getCallsign(args)
	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   strings.ToUpper(callsign),
		contents:    strings.Join(args[0:], " ")}
	return cli.sendTextMessage(tm)
}

type PrivateMessageSelectedCommand struct{}

func (*PrivateMessageSelectedCommand) Names() []string { return []string{"/dmsel"} }
func (*PrivateMessageSelectedCommand) Usage() string {
	return "[message]"
}
func (*PrivateMessageSelectedCommand) Help() string {
	return "Send the specified message to the currently selected aircraft."
}
func (*PrivateMessageSelectedCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*PrivateMessageSelectedCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	if positionConfig.selectedAircraft == nil {
		return ErrorStringConsoleEntry("No aircraft is currently selected")
	}

	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   positionConfig.selectedAircraft.callsign,
		contents:    strings.Join(args[0:], " ")}
	return cli.sendTextMessage(tm)
}

type TransmitCommand struct{}

func (*TransmitCommand) Names() []string { return []string{"/tx"} }
func (*TransmitCommand) Usage() string {
	return "[message]"
}
func (*TransmitCommand) Help() string {
	return "Transmits the text message on the primed frequency."
}
func (*TransmitCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*TransmitCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
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
		return cli.sendTextMessage(tm)
	}
}

type WallopCommand struct{}

func (*WallopCommand) Names() []string { return []string{"/wallop"} }
func (*WallopCommand) Usage() string {
	return "[message]"
}
func (*WallopCommand) Help() string {
	return "Send the specified message to all online supervisors."
}
func (*WallopCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*WallopCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	tm := TextMessage{messageType: TextWallop, contents: strings.Join(args, " ")}
	return cli.sendTextMessage(tm)
}

type RequestATISCommand struct{}

func (*RequestATISCommand) Names() []string { return []string{"/atis"} }
func (*RequestATISCommand) Usage() string {
	return "<controller>"
}
func (*RequestATISCommand) Help() string {
	return "Request the ATIS of the specified controller."
}
func (*RequestATISCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString}
}
func (*RequestATISCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	return ErrorConsoleEntry(server.RequestControllerATIS(args[0]))
}

type ContactMeCommand struct{}

func (*ContactMeCommand) Names() []string { return []string{"/contactme", "/cme"} }
func (*ContactMeCommand) Usage() string {
	return "<callsign>"
}
func (*ContactMeCommand) Help() string {
	return "Send a \"contact me\" request to the specified aircraft."
}
func (*ContactMeCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*ContactMeCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
	if positionConfig.primaryFrequency == Frequency(0) {
		return ErrorStringConsoleEntry("Unable to send contactme since no prime frequency is set.")
	}

	callsign, _ := getCallsign(args)
	tm := TextMessage{
		messageType: TextPrivate,
		recipient:   callsign,
		contents: fmt.Sprintf("Please contact me on %s. Please do not respond via private "+
			"message - use the frequency instead.", positionConfig.primaryFrequency),
	}

	return cli.sendTextMessage(tm)
}

type MessageReplyCommand struct{}

func (*MessageReplyCommand) Names() []string {
	return []string{"/0", "/1", "/2", "/3", "/4", "/5", "/6", "/7", "/8", "/9"}
}
func (*MessageReplyCommand) Usage() string {
	return "[message]"
}
func (*MessageReplyCommand) Help() string {
	return "Send the specified message to the recipient."
}
func (*MessageReplyCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*MessageReplyCommand) Run(cli *CLIPane, cmd string, args []string) []*ConsoleEntry {
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
