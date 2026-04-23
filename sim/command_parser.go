// sim/command_parser.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Aircraft command dispatch

var ErrInvalidCommandSyntax = fmt.Errorf("invalid command syntax")

type ControlCommandsResult struct {
	RemainingInput     string
	Error              error
	ReadbackSpokenText string          // Spoken text for TTS synthesis (if readback was generated)
	ReadbackCallsign   av.ADSBCallsign // Aircraft callsign for the readback
}

// RunAircraftControlCommands executes a space-separated string of control commands for an aircraft.
// Returns the remaining unparsed input and any error that occurred.
// This is the core command execution logic shared by the dispatcher and automated test code.
// All intents from commands are collected and rendered together as a single transmission.
// audioDuration is the length of the voice transmission (zero for typed or non-voice commands);
// the pilot-reaction delay applied by deferred-action Nav commands is reduced by
// (audioDuration - callsignAudioOffset), floored at zero.
func (s *Sim) RunAircraftControlCommands(tcw TCW, callsign av.ADSBCallsign, commandStr string, audioDuration time.Duration) ControlCommandsResult {
	commands := strings.Fields(commandStr)

	delayReduction := audioDuration - callsignAudioOffset
	if delayReduction < 0 {
		delayReduction = 0
	}

	// Parse addressing form suffix from callsign: /T indicates type+trailing3 addressing
	// (e.g., "skyhawk 3 alpha bravo" instead of "november 1 2 3 alpha bravo")
	addressingForm := AddressingFormFull
	if strings.HasSuffix(string(callsign), "/T") {
		addressingForm = AddressingFormTypeTrailing3
		callsign = av.ADSBCallsign(strings.TrimSuffix(string(callsign), "/T"))
	}

	// Update aircraft's last addressing form for readback rendering
	if ac, ok := s.Aircraft[callsign]; ok {
		ac.LastAddressingForm = addressingForm
	}

	// Handle ROLLBACK as callsign: STT outputs "ROLLBACK {callsign} {commands}" or "ROLLBACK {commands}".
	// The client splits on first space, so callsign="ROLLBACK" and commands contain the rest.
	if callsign == "ROLLBACK" {
		// Save last command's callsign before rollback clears it
		var lastCallsign av.ADSBCallsign
		if s.LastSTTCommand != nil {
			lastCallsign = s.LastSTTCommand.Callsign
		}

		if err := s.rollbackLastCommand(); err != nil {
			s.lg.Warnf("ROLLBACK failed: %v", err)
		}
		if len(commands) == 0 {
			return ControlCommandsResult{}
		}

		// Check if first element is a callsign (for "negative that was for {cs}")
		// or a command (for "negative, {commands}" without callsign)
		potentialCallsign := av.ADSBCallsign(commands[0])
		lookupCS := av.ADSBCallsign(strings.TrimSuffix(string(potentialCallsign), "/T"))
		if _, ok := s.Aircraft[lookupCS]; ok {
			callsign = potentialCallsign
			commands = commands[1:]
		} else if lastCallsign != "" {
			callsign = lastCallsign
		} else {
			s.lg.Warn("ROLLBACK: no target callsign available")
			return ControlCommandsResult{}
		}
		if len(commands) == 0 {
			return ControlCommandsResult{}
		}
	}

	// Handle special STT commands that need direct TTS synthesis
	// These short-circuit normal command processing
	if len(commands) == 1 {
		switch commands[0] {
		case "AGAIN":
			cs, spokenText, err := s.SayAgain(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		case "NOTCLEARED":
			cs, spokenText, err := s.SayNotCleared(tcw, callsign)
			return ControlCommandsResult{
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   cs,
			}
		}
	}

	// Take a snapshot before executing commands (for potential future rollback)
	if ac, ok := s.Aircraft[callsign]; ok {
		s.LastSTTCommand = &LastSTTCommand{
			Callsign:    callsign,
			NavSnapshot: ac.Nav.TakeSnapshot(),
		}
	}

	var intents []av.CommandIntent

	for i, command := range commands {
		intent, err := s.runOneControlCommand(tcw, callsign, command, delayReduction)
		if err != nil {
			// Post any collected intents before returning error
			spokenText := s.renderAndPostReadback(callsign, tcw, intents)
			return ControlCommandsResult{
				RemainingInput:     strings.Join(commands[i:], " "),
				Error:              err,
				ReadbackSpokenText: spokenText,
				ReadbackCallsign:   callsign,
			}
		}
		if intent != nil {
			intents = append(intents, intent)
		}
	}

	// Render all intents together as a single transmission
	spokenText := s.renderAndPostReadback(callsign, tcw, intents)
	return ControlCommandsResult{
		ReadbackSpokenText: spokenText,
		ReadbackCallsign:   callsign,
	}
}

// rollbackLastCommand restores the nav state of the last aircraft that received a command.
// This is used when the controller says "negative, that was for {other callsign}" to undo
// commands given to the wrong aircraft due to STT callsign misinterpretation.
func (s *Sim) rollbackLastCommand() error {
	if s.LastSTTCommand == nil {
		return ErrNoRecentCommand
	}

	ac, ok := s.Aircraft[s.LastSTTCommand.Callsign]
	if !ok {
		s.LastSTTCommand = nil
		return ErrNoRecentCommand
	}

	// Restore the nav state from the snapshot
	ac.Nav.RestoreSnapshot(s.LastSTTCommand.NavSnapshot)

	// Clear the snapshot - consecutive rollbacks should fail
	s.LastSTTCommand = nil

	return nil
}

// renderAndPostReadback renders a batch of command intents as a pilot readback transmission.
// The tcw ensures the readback goes to the controller who issued the command,
// regardless of any consolidation changes.
// Returns the spoken text for TTS synthesis, including the callsign suffix.
func (s *Sim) renderAndPostReadback(callsign av.ADSBCallsign, tcw TCW, intents []av.CommandIntent) string {
	if rt := av.RenderIntents(intents, s.Rand); rt != nil {
		s.postReadbackTransmission(callsign, *rt, tcw)
		// MixUp transmissions already include the callsign in the message
		if rt.Type != av.RadioTransmissionMixUp {
			if suffix := s.readbackCallsignSuffix(callsign, tcw); suffix != nil {
				rt.Merge(suffix)
			}
		}
		return rt.Spoken(s.Rand)
	}
	return ""
}

// readbackCallsignSuffix generates a RadioTransmission for the callsign suffix in readbacks.
// This is used both for synchronous TTS and matches what prepareRadioTransmissions does for events.
func (s *Sim) readbackCallsignSuffix(callsign av.ADSBCallsign, tcw TCW) *av.RadioTransmission {
	ac, ok := s.Aircraft[callsign]
	if !ok {
		return nil
	}

	primaryTCP := s.State.PrimaryPositionForTCW(tcw)
	ctrl := s.State.Controllers[primaryTCP]

	var heavySuper string
	if ctrl != nil && !ctrl.ERAMFacility {
		if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok {
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
		csArg = av.GACallsignArg{
			Callsign:     ac.ADSBCallsign,
			AircraftType: ac.FlightPlan.AircraftType,
			UseTypeForm:  true,
			IsEmergency:  ac.EmergencyState != nil,
		}
	} else {
		csArg = av.CallsignArg{
			Callsign:    ac.ADSBCallsign,
			IsEmergency: ac.EmergencyState != nil,
		}
	}
	return av.MakeReadbackTransmission("{callsign}"+heavySuper, csArg)
}

// parseSpeedUntil parses the "until" specification from a speed command.
// Formats:
//   - "ROSLY" -> fix name
//   - "5DME"  -> 5 DME
//   - "6"     -> 6 mile final
func parseSpeedUntil(untilStr string) *av.SpeedUntil {
	untilStr = strings.ToUpper(untilStr)

	// Check for DME pattern: digits followed by DME
	if strings.HasSuffix(untilStr, "DME") {
		numStr := strings.TrimSuffix(untilStr, "DME")
		if n, err := strconv.Atoi(numStr); err == nil && n > 0 {
			return &av.SpeedUntil{DME: n}
		}
	}

	// Check for pure number (mile final)
	if n, err := strconv.Atoi(untilStr); err == nil && n > 0 {
		return &av.SpeedUntil{MileFinal: n}
	}

	// Otherwise it's a fix name
	return &av.SpeedUntil{Fix: untilStr}
}

// parseConditionalAltitude parses the altitude-encoding convention used
// by LV/RC commands: number × 100, with a carve-out for values that look
// like feet already (>600 and evenly divisible by 100).
func parseConditionalAltitude(s string) (float32, error) {
	if s == "" {
		return 0, ErrInvalidCommandSyntax
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n > 600 && n%100 == 0 {
		return float32(n), nil
	}
	return float32(n * 100), nil
}

// parseConditionalAction parses an inner command string (the right-hand
// side of LV/RC) into a typed ConditionalAction. Accepts only lateral and
// speed/mach actions; altitude-changing and unknown inners return
// ErrInvalidCommandSyntax.
//
// Grammar:
//
//	H{hdg}             → ConditionalHeading (closest turn)
//	L{hdg} | R{hdg}    → ConditionalHeading (left/right turn to heading)
//	L{deg}D | R{deg}D  → ConditionalHeading (turn N degrees)
//	D{fix}             → ConditionalDirectFix (closest)
//	LD{fix} | RD{fix}  → ConditionalDirectFix (left/right)
//	S{spd}             → ConditionalSpeed
//	M{mach}            → ConditionalMach (2-digit mach, e.g. M78 → 0.78)
func parseConditionalAction(s string) (nav.ConditionalAction, error) {
	if len(s) < 2 {
		return nil, ErrInvalidCommandSyntax
	}
	switch s[0] {
	case 'H':
		hdg, err := strconv.Atoi(s[1:])
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		return nav.ConditionalHeading{Heading: hdg, Turn: av.TurnClosest}, nil

	case 'L', 'R':
		turn := av.TurnLeft
		if s[0] == 'R' {
			turn = av.TurnRight
		}
		// LD{fix} / RD{fix}
		if len(s) >= 5 && s[1] == 'D' {
			return nav.ConditionalDirectFix{Fix: strings.ToUpper(s[2:]), Turn: turn}, nil
		}
		// LnnD / RnnD
		if l := len(s); l > 2 && s[l-1] == 'D' {
			deg, err := strconv.Atoi(s[1 : l-1])
			if err != nil {
				return nil, ErrInvalidCommandSyntax
			}
			return nav.ConditionalHeading{ByDegrees: deg, Turn: turn}, nil
		}
		// L{hdg} / R{hdg}
		hdg, err := strconv.Atoi(s[1:])
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		return nav.ConditionalHeading{Heading: hdg, Turn: turn}, nil

	case 'D':
		if len(s) < 4 {
			return nil, ErrInvalidCommandSyntax
		}
		return nav.ConditionalDirectFix{Fix: strings.ToUpper(s[1:]), Turn: av.TurnClosest}, nil

	case 'S':
		sr, err := av.ParseSpeedRestriction(s[1:])
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		return nav.ConditionalSpeed{Restriction: *sr}, nil

	case 'M':
		if len(s) != 3 {
			return nil, ErrInvalidCommandSyntax
		}
		mach, err := strconv.ParseFloat(s[1:], 32)
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		return nav.ConditionalMach{Mach: float32(mach) / 100.0}, nil
	}
	return nil, ErrInvalidCommandSyntax
}

// parseCompoundSpeed parses a compound speed command string like
// "250+/UFIX1/210-/UFIX2/180+" into CompoundSpeedSegments.
// The input is the part after 'S' (e.g., "250+/UFIX1/210-/UFIX2/180+").
func parseCompoundSpeed(s string) ([]av.CompoundSpeedSegment, error) {
	// Split on "/U" to get alternating speed/fix pairs.
	// First element is the first speed, then alternating fix+speed pairs.
	parts := strings.Split(s, "/U")
	if len(parts) < 2 {
		return nil, ErrInvalidCommandSyntax
	}

	var segments []av.CompoundSpeedSegment

	// First part is just the first speed.
	sr, err := av.ParseSpeedRestriction(parts[0])
	if err != nil {
		return nil, err
	}

	// For parts[1..n-1], each contains "FIX/SPEED" (the fix for the previous
	// segment's UntilFix, followed by the next speed after a "/").
	// The last part may be just "FIX/SPEED" or just "FIX" (if the last
	// segment has no further speed — but per the plan, the last segment is
	// always an open-ended speed).
	for i := 1; i < len(parts); i++ {
		fix, speedStr, hasSpeed := strings.Cut(parts[i], "/")
		if fix == "" {
			return nil, ErrInvalidCommandSyntax
		}

		// Close out the previous segment with this fix.
		segments = append(segments, av.CompoundSpeedSegment{
			Speed:    sr,
			UntilFix: fix,
		})

		if hasSpeed {
			// Parse the next speed.
			sr, err = av.ParseSpeedRestriction(speedStr)
			if err != nil {
				return nil, err
			}
		} else if i < len(parts)-1 {
			// Not the last part but no speed — invalid.
			return nil, ErrInvalidCommandSyntax
		} else {
			// Last part with no trailing speed — no final open-ended segment.
			return segments, nil
		}
	}

	// Add the final open-ended segment (no UntilFix).
	segments = append(segments, av.CompoundSpeedSegment{
		Speed: sr,
	})

	return segments, nil
}

// parseHold parses a hold command string in the format "FIX/[option]/[option]"
// and returns the fix name and a controller-specified hold if options are present.
// Returns (fixName, nil, true) if no options are specified (use published hold).
// Returns (fixName, *Hold, true) if options are successfully parsed.
// Returns ("", nil, false) if parsing fails.
//
// Options may be:
// - L: left turns
// - R: right turns
// - xxNM: xx nautical mile legs
// - xxM: xx minute legs
// - Rxxx: inbound course on the xxx radial to the fix
//
// If options are specified, the Rxxx radial option is required.
// Multiple options of the same type result in an error.
func parseHold(command string) (string, *av.Hold, bool) {
	fix, opts, ok := strings.Cut(command, "/")
	fix = strings.ToUpper(fix)
	if !ok {
		// No options, use published hold
		return fix, nil, true
	}

	// Controller-specified hold with defaults
	hold := av.Hold{Fix: fix}
	directionSet := false

	for opt := range strings.SplitSeq(opts, "/") {
		opt = strings.ToUpper(opt)

		switch {
		case opt == "L":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnLeft
			directionSet = true

		case opt == "R":
			if directionSet {
				// Redundantly specified
				return "", nil, false
			}
			hold.TurnDirection = av.TurnRight
			directionSet = true

		case strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			dist, err := strconv.ParseFloat(strings.TrimSuffix(opt, "NM"), 32)
			if err != nil || dist <= 0 {
				return "", nil, false
			}
			hold.LegLengthNM = float32(dist)

		case strings.HasSuffix(opt, "M") && !strings.HasSuffix(opt, "NM"):
			if hold.LegLengthNM != 0 || hold.LegMinutes != 0 {
				return "", nil, false
			}
			time, err := strconv.ParseFloat(strings.TrimSuffix(opt, "M"), 32)
			if err != nil || time <= 0 {
				return "", nil, false
			}
			hold.LegMinutes = float32(time)

		case strings.HasPrefix(opt, "R") && len(opt) > 1:
			if hold.InboundCourse != 0 {
				return "", nil, false
			}
			radial, err := strconv.Atoi(opt[1:])
			if err != nil || radial <= 0 || radial > 360 {
				return "", nil, false
			}
			hold.InboundCourse = math.MagneticHeading(radial)

		default:
			return "", nil, false
		}
	}

	// Radial is required for controller-specified holds
	if hold.InboundCourse == 0 {
		return "", nil, false
	}
	if !directionSet {
		hold.TurnDirection = av.TurnRight
	}
	if hold.LegMinutes == 0 && hold.LegLengthNM == 0 {
		hold.LegMinutes = 1
	}

	return fix, &hold, true
}

func parseLAHSOSuffix(spec string) (base string, lahsoRunway string) {
	base, suffix, ok := strings.Cut(spec, "/")
	if ok && strings.HasPrefix(suffix, "LAHSO") {
		lahsoRunway = strings.TrimPrefix(suffix, "LAHSO")
	}
	return base, lahsoRunway
}

// runOneControlCommand executes a single control command for an aircraft.
// Returns the intent generated by the command (if any) for batching.
// delayReduction is subtracted from the pilot-reaction delay on deferred
// Nav commands (heading, direct-fix, altitude), floored at zero.
func (s *Sim) runOneControlCommand(tcw TCW, callsign av.ADSBCallsign, command string, delayReduction time.Duration) (av.CommandIntent, error) {
	if len(command) == 0 {
		return nil, ErrInvalidCommandSyntax
	}

	// A###, C###, and D### all equivalently assign an altitude
	if (command[0] == 'A' || command[0] == 'C' || command[0] == 'D') && len(command) > 1 && util.IsAllNumbers(command[1:]) {
		alt, err := strconv.Atoi(command[1:])
		if err != nil {
			return nil, err
		}
		if alt > 600 && (alt%100 == 0) {
			// Sometimes STT transcript interpretation forgets altitudes are in 100s...
			alt /= 100
		}
		return s.AssignAltitude(tcw, callsign, 100*alt, false, delayReduction)
	}

	switch command[0] {
	case 'A':
		if command == "A" {
			return s.AltitudeOurDiscretion(tcw, callsign)
		} else if command == "APPROVED" {
			return s.ApproveVisualSeparation(tcw, callsign)
		} else if command == "AGAIN" {
			// AGAIN is handled specially in RunAircraftControlCommands for TTS synthesis
			return nil, nil
		} else if command == "AP" {
			return s.AirportInSightInquiry(tcw, callsign)
		} else if remainder, ok := strings.CutPrefix(command, "AP/"); ok {
			clockstr, milesstr, ok := strings.Cut(remainder, "/")
			if !ok {
				return nil, ErrInvalidCommandSyntax
			}

			oclock, err := strconv.Atoi(clockstr)
			if err != nil || oclock < 1 || oclock > 12 {
				return nil, ErrInvalidCommandSyntax
			}
			miles, err := strconv.Atoi(milesstr)
			if err != nil || miles < 1 || miles > 50 {
				return nil, ErrInvalidCommandSyntax
			}

			return s.AirportAdvisory(tcw, callsign, oclock, miles)
		} else if strings.HasPrefix(command, "ATIS/") {
			return s.ATISCommand(tcw, callsign, command[5:])
		} else {
			components := strings.Split(command, "/")
			if len(components) != 2 || len(components[1]) == 0 {
				return nil, ErrInvalidCommandSyntax
			}

			fix := strings.ToUpper(components[0][1:])
			switch components[1][0] {
			case 'C':
				rest := components[1][1:]
				straightIn := strings.HasPrefix(rest, "SI")
				rest = strings.TrimPrefix(rest, "SI")
				if util.IsAllNumbers(rest) && len(rest) > 0 {
					alt, err := strconv.Atoi(rest)
					if err != nil {
						return nil, err
					}
					return s.AfterFixAltitude(tcw, callsign, fix, alt*100)
				}
				return s.AtFixCleared(tcw, callsign, fix, rest, straightIn)
			case 'D':
				rest := components[1][1:]
				if !util.IsAllNumbers(rest) || len(rest) == 0 {
					return nil, ErrInvalidCommandSyntax
				}
				alt, err := strconv.Atoi(rest)
				if err != nil {
					return nil, err
				}
				return s.AfterFixAltitude(tcw, callsign, fix, alt*100)
			case 'I':
				return s.AtFixIntercept(tcw, callsign, fix)
			case 'S':
				sr, err := av.ParseSpeedRestriction(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.AfterFixSpeed(tcw, callsign, fix, sr)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		}

	case 'C':
		if command == "CAC" {
			return s.CancelApproachClearance(tcw, callsign)
		} else if command == "CWT" {
			return s.CautionWakeTurbulence(tcw, callsign)
		} else if command == "CVS" {
			return s.ClimbViaSID(tcw, callsign)
		} else if strings.HasPrefix(command, "CVA") {
			runway, lahsoRunway := parseLAHSOSuffix(command[3:])
			return s.ClearedVisualApproach(tcw, callsign, runway, lahsoRunway)
		} else if strings.HasPrefix(command, "CDME") && len(command) > 4 {
			distStr, rest, _ := strings.Cut(command[4:], "/")
			d, err := strconv.Atoi(distStr)
			if err != nil {
				return nil, ErrInvalidCommandSyntax
			}
			var ar *av.AltitudeRestriction
			var sr *av.SpeedRestriction
			if rest != "" {
				for _, cmd := range strings.Split(rest, "/") {
					if len(cmd) == 0 {
						return nil, ErrInvalidCommandSyntax
					}
					if cmd[0] == 'A' && len(cmd) > 1 {
						if ar, err = av.ParseAltitudeRestriction(cmd[1:]); err != nil {
							return nil, err
						}
						ar.Range[0] *= 100
						if ar.Range[1] != av.MaxAltitude {
							ar.Range[1] *= 100
						}
					} else if cmd[0] == 'S' {
						if sr, err = av.ParseSpeedRestriction(cmd[1:]); err != nil {
							return nil, err
						}
					} else if cmd[0] == 'M' {
						if sr, err = av.ParseSpeedRestriction(cmd); err != nil {
							return nil, err
						}
					} else {
						return nil, ErrInvalidCommandSyntax
					}
				}
			}
			if ar == nil && sr == nil {
				return nil, ErrInvalidCommandSyntax
			}
			return s.CrossDMEAt(tcw, callsign, float32(d), ar, sr)
		} else if command == "CSI" || (strings.HasPrefix(command, "CSI") && !util.IsAllNumbers(command[3:])) {
			return s.ClearedApproach(tcw, callsign, command[3:], true)
		} else if components := strings.Split(command, "/"); len(components) > 1 {
			fix := components[0][1:]
			var ar *av.AltitudeRestriction
			var sr *av.SpeedRestriction
			dist := float32(-1)
			var dir math.CardinalOrdinalDirection
			for _, cmd := range components[1:] {
				if len(cmd) == 0 {
					return nil, ErrInvalidCommandSyntax
				}

				if cmd[0] == 'A' && len(cmd) > 1 {
					var err error
					if ar, err = av.ParseAltitudeRestriction(cmd[1:]); err != nil {
						return nil, err
					}
					ar.Range[0] *= 100
					if ar.Range[1] != av.MaxAltitude {
						ar.Range[1] *= 100
					}
				} else if cmd[0] == 'S' {
					var err error
					if sr, err = av.ParseSpeedRestriction(cmd[1:]); err != nil {
						return nil, err
					}
				} else if cmd[0] == 'M' {
					var err error
					if sr, err = av.ParseSpeedRestriction(cmd); err != nil {
						return nil, err
					}
				} else if d, dd, err := av.ParseDistanceDirection(cmd); err == nil {
					dist = float32(d)
					dir = dd
				} else {
					return nil, ErrInvalidCommandSyntax
				}
			}

			if dist >= 0 {
				return s.CrossDistanceFromFixAt(tcw, callsign, fix, dist, dir, ar, sr)
			}
			return s.CrossFixAt(tcw, callsign, fix, ar, sr)
		} else if strings.HasPrefix(command, "CT") && len(command) > 2 {
			// Only treat as contact command if the TCP exists as a valid controller;
			// otherwise treat as cleared approach (e.g., "CTTL" -> cleared for TTL approach)
			tcp := TCP(command[2:])
			if _, ok := s.State.Controllers[tcp]; ok {
				return s.ContactController(tcw, ACID(callsign), tcp)
			}
			return s.ClearedApproach(tcw, callsign, command[1:], false)
		} else {
			return s.ClearedApproach(tcw, callsign, command[1:], false)
		}

	case 'D':
		if command == "DVS" {
			return s.DescendViaSTAR(tcw, callsign)
		} else if components := strings.Split(command, "/"); len(components) > 1 && len(components[1]) > 1 {
			fix := components[0][1:]

			switch components[1][0] {
			case 'D':
				return s.DepartFixDirect(tcw, callsign, fix, components[1][1:])
			case 'H':
				hdg, err := strconv.Atoi(components[1][1:])
				if err != nil {
					return nil, err
				}
				return s.DepartFixHeading(tcw, callsign, fix, hdg)
			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else if len(command) >= 4 && len(command) <= 6 {
			return s.DirectFix(tcw, callsign, command[1:], av.TurnClosest, delayReduction)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'E':
		if command == "ED" {
			return s.ExpediteDescent(tcw, callsign)
		} else if strings.HasPrefix(command, "ED") && len(command) > 2 {
			alt, err := strconv.Atoi(command[2:])
			if err == nil {
				return s.ExpediteDescentThrough(tcw, callsign, float32(alt*100))
			}
			// Fall through to expect-approach parsing below
		}
		if command == "EC" {
			return s.ExpediteClimb(tcw, callsign)
		} else if strings.HasPrefix(command, "EC") && len(command) > 2 {
			alt, err := strconv.Atoi(command[2:])
			if err == nil {
				return s.ExpediteClimbThrough(tcw, callsign, float32(alt*100))
			}
			// Fall through
		}
		if strings.HasPrefix(command, "EVA") && len(command) > 3 {
			// "Expect visual approach runway XX" — just a heads-up, doesn't
			// change the approach assignment. The actual clearance comes via CVA.
			runway, lahsoRunway := parseLAHSOSuffix(command[3:])
			return s.ExpectVisualApproach(tcw, callsign, runway, lahsoRunway)
		} else if strings.HasPrefix(command, "EXPDIR") && len(command) > 6 {
			return s.ExpectDirect(tcw, callsign, command[6:])
		} else if len(command) > 1 {
			approach, lahsoRunway := parseLAHSOSuffix(command[1:])
			return s.ExpectApproach(tcw, callsign, approach, lahsoRunway)
		} else if command == "E" {
			// Bare "E" re-issues expect for the already-assigned approach
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.Assigned != nil {
				return s.ExpectApproach(tcw, callsign, ac.Nav.Approach.Assigned.Id, "")
			}
			return av.MakeUnableIntent("unable. We haven't been told to expect an approach"), nil
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'F':
		if command == "FC" {
			if ac, ok := s.Aircraft[callsign]; ok && ac.Nav.Approach.Cleared {
				// STT sometimes gets confused and gives FC for "contact tower" instructions, so
				// we'll just roll with that.
				return s.ContactTower(tcw, callsign, av.Frequency(0))
			} else {
				return s.ContactTrackingController(tcw, ACID(callsign))
			}
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'G':
		if command == "GA" {
			if err := s.GoAhead(tcw, callsign); err != nil {
				return nil, err
			}
			return nil, nil // GoAhead returns no intent
		} else if command == "GRD" {
			return s.GoodRateDescent(tcw, callsign)
		} else if command == "GRC" {
			return s.GoodRateClimb(tcw, callsign)
		} else if strings.HasPrefix(command, "GR") && len(command) > 2 {
			alt, err := strconv.Atoi(command[2:])
			if err != nil {
				return nil, ErrInvalidCommandSyntax
			}
			return s.GoodRateThrough(tcw, callsign, float32(alt*100))
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'H':
		if len(command) == 1 {
			// Present heading
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Present:        true,
				DelayReduction: delayReduction,
			})
		} else if hdg, err := strconv.Atoi(command[1:]); err == nil {
			// Fly heading xxx
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnClosest,
				DelayReduction: delayReduction,
			})
		} else {
			// Hold at fix (published or controller-specified)
			if fix, hold, ok := parseHold(command[1:]); !ok {
				return nil, ErrInvalidCommandSyntax
			} else {
				return s.HoldAtFix(tcw, callsign, fix, hold)
			}
		}

	case 'I':
		if len(command) == 1 {
			return s.InterceptApproach(tcw, callsign)
		} else if command == "ID" {
			return s.Ident(tcw, callsign)
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'L':
		if strings.HasPrefix(command, "LV") {
			if len(command) <= 2 {
				return nil, ErrInvalidCommandSyntax
			}
			altStr, inner, ok := strings.Cut(command[2:], "/")
			if !ok || altStr == "" || inner == "" {
				return nil, ErrInvalidCommandSyntax
			}
			alt, err := parseConditionalAltitude(altStr)
			if err != nil {
				return nil, err
			}
			action, err := parseConditionalAction(inner)
			if err != nil {
				return nil, err
			}
			return s.AssignConditional(tcw, callsign, nav.ConditionalLeaving, alt, action)
		}
		if len(command) >= 5 && command[1] == 'D' {
			return s.DirectFix(tcw, callsign, command[2:], av.TurnLeft, delayReduction)
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				LeftDegrees:    deg,
				DelayReduction: delayReduction,
			})
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnLeft,
				DelayReduction: delayReduction,
			})
		}
	case 'M': // mach speed
		// M78 for mach 0.78
		// + and - operators work here as well
		if len(command) != 3 {
			return nil, ErrInvalidCommandSyntax
		}

		machStr := command[1:]
		mach, err := strconv.ParseFloat(machStr, 32)
		if err != nil {
			return nil, ErrInvalidCommandSyntax
		}
		mach /= 100.0

		return s.AssignMach(tcw, callsign, float32(mach), false)

	case 'R':
		if command == "RON" {
			return s.ResumeOwnNavigation(tcw, callsign)
		} else if command == "RST" {
			return s.RadarServicesTerminated(tcw, callsign)
		} else if strings.HasPrefix(command, "RC") {
			if len(command) <= 2 {
				return nil, ErrInvalidCommandSyntax
			}
			altStr, inner, ok := strings.Cut(command[2:], "/")
			if !ok || altStr == "" || inner == "" {
				return nil, ErrInvalidCommandSyntax
			}
			alt, err := parseConditionalAltitude(altStr)
			if err != nil {
				return nil, err
			}
			action, err := parseConditionalAction(inner)
			if err != nil {
				return nil, err
			}
			return s.AssignConditional(tcw, callsign, nav.ConditionalReaching, alt, action)
		} else if len(command) >= 5 && command[1] == 'D' {
			return s.DirectFix(tcw, callsign, command[2:], av.TurnRight, delayReduction)
		} else if l := len(command); l > 2 && command[l-1] == 'D' {
			deg, err := strconv.Atoi(command[1 : l-1])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				RightDegrees:   deg,
				DelayReduction: delayReduction,
			})
		} else {
			hdg, err := strconv.Atoi(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignHeading(&HeadingArgs{
				TCW:            tcw,
				ADSBCallsign:   callsign,
				Heading:        hdg,
				Turn:           av.TurnRight,
				DelayReduction: delayReduction,
			})
		}

	case 'S':
		if len(command) == 1 {
			return s.AssignSpeed(tcw, callsign, nil, false)
		} else if command == "SPRES" {
			return s.MaintainPresentSpeed(tcw, callsign)
		} else if command == "SMIN" {
			return s.MaintainSlowestPractical(tcw, callsign)
		} else if command == "SMAX" {
			return s.MaintainMaximumForward(tcw, callsign)
		} else if command == "SS" {
			return s.SaySpeed(tcw, callsign)
		} else if command == "SI" {
			return s.SayIndicatedSpeed(tcw, callsign)
		} else if command == "SM" {
			return s.SayMach(tcw, callsign)
		} else if command == "SQS" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeStandby)
		} else if command == "SQA" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeAltitude)
		} else if command == "SQON" {
			return s.ChangeTransponderMode(tcw, callsign, av.TransponderModeOn)
		} else if len(command) == 6 && command[:2] == "SQ" {
			sq, err := av.ParseSquawk(command[2:])
			if err != nil {
				return nil, err
			}
			return s.ChangeSquawk(tcw, callsign, sq)
		} else if command == "SH" {
			return s.SayHeading(tcw, callsign)
		} else if command == "SA" {
			return s.SayAltitude(tcw, callsign)
		} else if strings.HasPrefix(command, "SAYAGAIN/") {
			return s.SayAgainCommand(tcw, callsign, command[9:])
		} else if speedStr, untilStr, ok := strings.Cut(command[1:], "/U"); ok {
			// Check for compound format: S250/UFIX1/210/UFIX2/180
			// After the first Cut on "/U", if untilStr contains another "/",
			// it's compound. This works because current single speed-until
			// specs (fix names, numbers, NdME) never contain "/".
			if strings.Contains(untilStr, "/") {
				segments, err := parseCompoundSpeed(command[1:])
				if err != nil {
					return nil, err
				}
				return s.AssignCompoundSpeed(tcw, callsign, segments)
			}
			sr, err := av.ParseSpeedRestriction(speedStr)
			if err != nil {
				return nil, err
			}
			until := parseSpeedUntil(untilStr)
			return s.AssignSpeedUntil(tcw, callsign, sr, until)
		} else {
			sr, err := av.ParseSpeedRestriction(command[1:])
			if err != nil {
				return nil, err
			}
			return s.AssignSpeed(tcw, callsign, sr, false)
		}

	case 'T':
		if strings.HasPrefix(command, "TRAFFIC/") {
			return s.TrafficAdvisory(tcw, callsign, command)
		} else if command == "TO" || strings.HasPrefix(command, "TO/") {
			var freq av.Frequency
			if strings.HasPrefix(command, "TO/") {
				if n, err := strconv.Atoi(command[3:]); err == nil {
					freq = av.Frequency(n)
				}
			}
			return s.ContactTower(tcw, callsign, freq)
		} else if n := len(command); n > 2 {
			if deg, err := strconv.Atoi(command[1 : n-1]); err == nil {
				if command[n-1] == 'L' {
					return s.AssignHeading(&HeadingArgs{
						TCW:            tcw,
						ADSBCallsign:   callsign,
						LeftDegrees:    deg,
						DelayReduction: delayReduction,
					})
				} else if command[n-1] == 'R' {
					return s.AssignHeading(&HeadingArgs{
						TCW:            tcw,
						ADSBCallsign:   callsign,
						RightDegrees:   deg,
						DelayReduction: delayReduction,
					})
				}
			}

			switch command[:2] {
			case "TS":
				sr, err := av.ParseSpeedRestriction(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignSpeed(tcw, callsign, sr, true)
			case "TM":
				mach, err := strconv.ParseFloat(command[2:], 32)
				if err != nil {
					return nil, err
				}
				mach /= 100.0
				return s.AssignMach(tcw, callsign, float32(mach), true)
			case "TA", "TC", "TD":
				alt, err := strconv.Atoi(command[2:])
				if err != nil {
					return nil, err
				}
				return s.AssignAltitude(tcw, callsign, 100*alt, true, delayReduction)

			default:
				return nil, ErrInvalidCommandSyntax
			}
		} else {
			return nil, ErrInvalidCommandSyntax
		}

	case 'V':
		if command == "VISSEP" {
			return s.MaintainVisualSeparation(tcw, callsign)
		}
		return nil, ErrInvalidCommandSyntax

	case 'X':
		s.DeleteAircraft(tcw, callsign)
		return nil, nil // DeleteAircraft returns no intent

	default:
		return nil, ErrInvalidCommandSyntax
	}
}
