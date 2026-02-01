package stt

import (
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
)

// Transcriber converts speech transcripts to aircraft control commands using
// local algorithmic parsing with fast fuzzy matching.
type Transcriber struct {
	lg *log.Logger
}

// NewTranscriber creates a new STT transcriber.
func NewTranscriber(lg *log.Logger) *Transcriber {
	return &Transcriber{lg: lg}
}

// DecodeTranscript converts a speech transcript to aircraft control commands.
// It returns one of:
//   - "{CALLSIGN} {CMD1} {CMD2} ..." for successful parsing
//   - "{CALLSIGN} AGAIN" if callsign identified but commands unclear
//   - "" if transcript is empty, only contains position identification, or no callsign could be matched
//
// Commands may include SAYAGAIN/TYPE for partial parses where keywords were recognized
// but the associated value couldn't be extracted (e.g., "fly heading blark" would return
// "SAYAGAIN/HEADING"). Valid types are: HEADING, ALTITUDE, SPEED, APPROACH, TURN, SQUAWK, FIX.
// When combined with other commands, e.g., "{CALLSIGN} C50 SAYAGAIN/HEADING", the aircraft
// will execute the valid commands and ask for clarification on the missed part.
//
// controllerRadioName is the user's controller radio name (e.g., "New York Departure")
// used to detect position identification phrases. Pass empty string if not available.
func (p *Transcriber) DecodeTranscript(
	aircraft map[string]Aircraft,
	transcript string,
	controllerRadioName string,
) (string, error) {
	return p.decodeInternal(aircraft, transcript, controllerRadioName, "")
}

// DecodeCommandsForCallsign parses commands from a transcript for a known callsign.
// This is used when the controller repeats a command without saying the callsign
// after an aircraft replied "AGAIN". It skips callsign matching and directly parses
// the entire transcript as commands for the specified aircraft.
// Returns one of:
//   - "{commands}" for successfully parsed commands
//   - "AGAIN" if no commands could be parsed
//   - "" if transcript is empty
func (p *Transcriber) DecodeCommandsForCallsign(
	aircraft map[string]Aircraft,
	transcript string,
	callsign string,
) (string, error) {
	return p.decodeInternal(aircraft, transcript, "", callsign)
}

// decodeInternal is the shared implementation for DecodeTranscript and DecodeCommandsForCallsign.
// If fallbackCallsign is non-empty, callsign matching is skipped and that callsign is used directly.
func (p *Transcriber) decodeInternal(
	aircraft map[string]Aircraft,
	transcript string,
	controllerRadioName string,
	fallbackCallsign string,
) (string, error) {
	start := time.Now()
	isFallback := fallbackCallsign != ""

	if isFallback {
		logLocalStt("=== DecodeTranscript START (fallback callsign=%q) ===", fallbackCallsign)
	} else {
		logLocalStt("=== DecodeTranscript START ===")
	}
	logLocalStt("input transcript: %q", transcript)

	// Handle empty transcript
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		logLocalStt("empty transcript, returning \"\"")
		return "", nil
	}

	// Layer 1: Phonetic normalization
	words := NormalizeTranscript(transcript)
	logLocalStt("normalized words: %v", words)
	if len(words) == 0 {
		logLocalStt("no words after normalization, returning \"\"")
		return "", nil
	}

	// Layer 2: Tokenization
	tokens := Tokenize(words)
	logLocalStt("tokens: %d", len(tokens))
	for i, t := range tokens {
		logLocalStt("  token[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}
	if len(tokens) == 0 {
		logLocalStt("no tokens, returning \"\"")
		return "", nil
	}

	var callsign string
	var addressingForm sim.CallsignAddressingForm
	var ac Aircraft
	var commandTokens []Token
	var callsignConfidence = 1.0

	if isFallback {
		// Skip callsign matching - use the provided callsign
		callsign = fallbackCallsign
		commandTokens = tokens

		// Look up the aircraft context directly by ICAO callsign
		var found bool
		if ac, found = aircraft[callsign]; !found {
			logLocalStt("no aircraft context found for callsign %q, returning AGAIN", callsign)
			elapsed := time.Since(start)
			p.logInfo("local STT (fallback): %q -> AGAIN (no aircraft context, time=%s)", transcript, elapsed)
			return "AGAIN", nil
		}
		logLocalStt("found aircraft context for callsign %q", callsign)
	} else {
		// Check for "negative, that was for {callsign}" correction pattern BEFORE callsign matching
		// e.g., "Negative that was for Delta 456. Delta 456, turn left heading 270"
		// This triggers a ROLLBACK of the last command, then processes the rest for the correct aircraft
		// The wrong aircraft's callsign should NOT be in the transcript (to avoid confusing them)
		if tokensAfterNegative, found := detectNegativeThatWasFor(tokens); found {
			logLocalStt("detected 'negative that was for' correction pattern at start")
			// Match the correct callsign from the tokens after the correction phrase
			correctMatch, correctRemaining := MatchCallsign(tokensAfterNegative, aircraft)
			if correctMatch.Callsign != "" {
				logLocalStt("correct callsign match: Callsign=%q SpokenKey=%q Conf=%.2f",
					correctMatch.Callsign, correctMatch.SpokenKey, correctMatch.Confidence)

				// Get aircraft context for the correct callsign
				var correctAc Aircraft
				if correctMatch.SpokenKey != "" {
					correctAc = aircraft[correctMatch.SpokenKey]
				}

				// Parse and validate commands for the correct callsign
				commands, cmdConf := ParseCommands(correctRemaining, correctAc)
				logLocalStt("parsed commands for correct callsign: %v (conf=%.2f)", commands, cmdConf)
				validation := ValidateCommands(commands, correctAc)
				logLocalStt("validated commands: %v", validation.ValidCommands)

				// Build output: ROLLBACK + commands for correct callsign
				var output string
				if len(validation.ValidCommands) > 0 {
					// Encode addressing form in callsign if needed
					correctCallsignWithForm := correctMatch.Callsign
					if correctMatch.AddressingForm == sim.AddressingFormTypeTrailing3 {
						correctCallsignWithForm += "/T"
					}
					output = "ROLLBACK " + correctCallsignWithForm + " " + strings.Join(validation.ValidCommands, " ")
				} else {
					// Just ROLLBACK if no valid commands were parsed
					output = "ROLLBACK"
				}

				elapsed := time.Since(start)
				logLocalStt("=== DecodeTranscript END: %q (negative correction, time=%s) ===", output, elapsed)
				p.logInfo("local STT: %q -> %q (negative correction, time=%s)", transcript, output, elapsed)
				return strings.TrimSpace(output), nil
			}
			// Couldn't match correct callsign, just return ROLLBACK
			logLocalStt("couldn't match correct callsign after 'negative that was for', returning just ROLLBACK")
			elapsed := time.Since(start)
			p.logInfo("local STT: %q -> ROLLBACK (negative correction, no new callsign, time=%s)", transcript, elapsed)
			return "ROLLBACK", nil
		}

		// Layer 3: Callsign matching
		callsignMatch, remainingTokens := MatchCallsign(tokens, aircraft)
		logLocalStt("callsign match: Callsign=%q SpokenKey=%q Conf=%.2f Consumed=%d",
			callsignMatch.Callsign, callsignMatch.SpokenKey, callsignMatch.Confidence, callsignMatch.Consumed)

		if callsignMatch.Callsign == "" {
			// No callsign identified - just ignore the transmission
			logLocalStt("no callsign match for %q, ignoring", transcript)
			return "", nil
		}

		// Check for "not for you" correction pattern
		// e.g., "479, that was not for you, Virgin 47 Foxtrot, expect..."
		// If found, re-match callsign from the tokens after the correction phrase
		if tokensAfterCorrection, found := detectNotForYouCorrection(remainingTokens); found {
			logLocalStt("detected 'not for you' correction, re-matching callsign")
			newMatch, newRemaining := MatchCallsign(tokensAfterCorrection, aircraft)
			if newMatch.Callsign != "" {
				logLocalStt("new callsign match: Callsign=%q SpokenKey=%q Conf=%.2f Consumed=%d",
					newMatch.Callsign, newMatch.SpokenKey, newMatch.Confidence, newMatch.Consumed)
				callsignMatch = newMatch
				remainingTokens = newRemaining
			}
		}

		callsign = callsignMatch.Callsign
		addressingForm = callsignMatch.AddressingForm
		callsignConfidence = callsignMatch.Confidence
		commandTokens = remainingTokens

		// Get aircraft context for the matched callsign
		if callsignMatch.SpokenKey != "" {
			ac = aircraft[callsignMatch.SpokenKey]
		}
	}

	logLocalStt("aircraft context: State=%q Altitude=%d Fixes=%d Approaches=%d AssignedApproach=%q LAHSORunways=%v",
		ac.State, ac.Altitude, len(ac.Fixes), len(ac.CandidateApproaches), ac.AssignedApproach, ac.LAHSORunways)
	for spokenName, fixID := range ac.Fixes {
		logLocalStt("  fix: %q -> %q", spokenName, fixID)
	}
	for spokenName, apprID := range ac.CandidateApproaches {
		logLocalStt("  approach: %q -> %q", spokenName, apprID)
	}

	// Check if "disregard" is the only command (meaning "ignore this transmission")
	// e.g., "Blue Streak 4193, disregard." should return empty, not AGAIN
	if isDisregardOnly(commandTokens) {
		logLocalStt("detected disregard-only command, returning empty")
		elapsed := time.Since(start)
		logLocalStt("=== DecodeTranscript END: \"\" (disregard, time=%s) ===", elapsed)
		p.logInfo("local STT: %q -> \"\" (disregard, time=%s)", transcript, elapsed)
		return "", nil
	}

	// Handle "disregard" or "correction" in remaining tokens
	// This discards previous command attempts but preserves callsign
	commandTokens = applyDisregard(commandTokens)
	logLocalStt("command tokens after disregard: %d", len(commandTokens))
	for i, t := range commandTokens {
		logLocalStt("  token[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}

	// Check for position identification phrases (only for normal decode, not fallback)
	if !isFallback && isPositionIdentification(commandTokens, controllerRadioName) {
		logLocalStt("detected position identification, returning empty")
		elapsed := time.Since(start)
		logLocalStt("=== DecodeTranscript END: \"\" (position ID, time=%s) ===", elapsed)
		p.logInfo("local STT: %q -> \"\" (position identification, time=%s)", transcript, elapsed)
		return "", nil
	}

	// Check if remaining tokens are just acknowledgment filler words (roger, wilco, copy)
	// These need no response - return empty
	if isAcknowledgmentOnly(commandTokens) {
		logLocalStt("detected acknowledgment only (roger/wilco/copy), returning empty")
		elapsed := time.Since(start)
		logLocalStt("=== DecodeTranscript END: \"\" (acknowledgment, time=%s) ===", elapsed)
		p.logInfo("local STT: %q -> \"\" (acknowledgment, time=%s)", transcript, elapsed)
		return "", nil
	}

	// Check if remaining tokens contain "radar contact" phrase (informational only)
	// This can appear with facility names like "Miami Departure, radar contact"
	if isRadarContactPhrase(commandTokens) {
		logLocalStt("detected radar contact phrase, returning empty")
		elapsed := time.Since(start)
		logLocalStt("=== DecodeTranscript END: \"\" (radar contact, time=%s) ===", elapsed)
		p.logInfo("local STT: %q -> \"\" (radar contact, time=%s)", transcript, elapsed)
		return "", nil
	}

	// Layer 4: Command parsing
	commands, cmdConf := ParseCommands(commandTokens, ac)
	logLocalStt("parsed commands: %v (conf=%.2f)", commands, cmdConf)

	// Layer 5: Validation
	validation := ValidateCommands(commands, ac)
	logLocalStt("validated commands: %v (conf=%.2f)", validation.ValidCommands, validation.Confidence)
	if len(validation.Errors) > 0 {
		logLocalStt("validation errors: %v", validation.Errors)
	}

	// Compute overall confidence
	confidence := callsignConfidence
	if len(commands) > 0 {
		confidence *= cmdConf * validation.Confidence
	}

	// Generate output
	var output string
	if isFallback {
		// For fallback mode, return just the commands (caller will prepend callsign)
		if len(validation.ValidCommands) == 0 {
			output = "AGAIN"
		} else {
			output = strings.Join(validation.ValidCommands, " ")
		}
	} else {
		// Encode addressing form in callsign: append /T for type-based addressing (GA aircraft)
		callsignWithForm := callsign
		if addressingForm == sim.AddressingFormTypeTrailing3 {
			callsignWithForm += "/T"
		}

		if len(validation.ValidCommands) == 0 {
			// Callsign matched but couldn't parse commands - ask for say again
			output = callsignWithForm + " AGAIN"
		} else {
			output = callsignWithForm + " " + strings.Join(validation.ValidCommands, " ")
		}
	}

	elapsed := time.Since(start)
	logLocalStt("=== DecodeTranscript END: %q (conf=%.2f, time=%s) ===", output, confidence, elapsed)
	if isFallback {
		p.logInfo("local STT (fallback): %q -> %q (time=%s)", transcript, output, elapsed)
	} else {
		p.logInfo("local STT: %q -> %q (conf=%.2f, time=%s)", transcript, output, confidence, elapsed)
	}

	return strings.TrimSpace(output), nil
}

// DecodeFromState decodes a transcript using the simulation state directly.
// It builds the aircraft context internally from the provided state.
// Only tracks on the user's frequency (ControllerFrequency) are included in the context.
func (p *Transcriber) DecodeFromState(
	state *sim.UserState,
	userTCW sim.TCW,
	transcript string,
) (string, error) {
	// Build aircraft context from state
	aircraft := p.BuildAircraftContext(state, userTCW)

	// Get the controller's radio name for position identification detection
	controllerRadioName := ""
	primaryPos := state.PrimaryPositionForTCW(userTCW)
	if ctrl, ok := state.Controllers[primaryPos]; ok && ctrl != nil {
		controllerRadioName = ctrl.RadioName
	}

	// Delegate to existing decoder
	return p.DecodeTranscript(aircraft, transcript, controllerRadioName)
}

// BuildAircraftContext creates the STT aircraft context from simulation state.
func (p *Transcriber) BuildAircraftContext(
	state *sim.UserState,
	userTCW sim.TCW,
) map[string]Aircraft {
	acCtx := make(map[string]Aircraft)

	for _, trk := range state.Tracks {
		// Check if the aircraft is on the user's frequency
		if !state.TCWControlsPosition(userTCW, trk.ControllerFrequency) {
			continue
		}

		sttAc := Aircraft{
			Callsign:            string(trk.ADSBCallsign),
			Altitude:            int(trk.TrueAltitude),
			ControllerFrequency: string(trk.ControllerFrequency),
		}

		// Add tracking controller and aircraft type from flight plan
		if trk.FlightPlan != nil {
			sttAc.TrackingController = string(trk.FlightPlan.TrackingController)
			sttAc.AircraftType = trk.FlightPlan.AircraftType
		}

		// Build fixes map
		sttAc.Fixes = make(map[string]string)
		for _, fix := range trk.Fixes {
			// For airports, add all telephony variants so STT can match any of them
			if variants := av.GetAirportTelephonyVariants(fix); len(variants) > 0 {
				for _, variant := range variants {
					sttAc.Fixes[variant] = fix
				}
			} else {
				sttAc.Fixes[av.GetFixTelephony(fix)] = fix
			}
		}

		// Determine state and set SID/STAR
		if trk.IsDeparture() {
			sttAc.State = "departure"
			sttAc.SID = trk.SID
		} else if trk.IsArrival() {
			if trk.ClearedForApproach {
				sttAc.State = "cleared approach"
			} else {
				sttAc.State = "arrival"
			}
			sttAc.STAR = trk.STAR
		} else if trk.IsOverflight() {
			sttAc.State = "overflight"
		}

		// Build candidate approaches for arrivals
		if trk.IsArrival() && trk.ArrivalAirport != "" {
			sttAc.CandidateApproaches = make(map[string]string)
			sttAc.ApproachFixes = make(map[string]map[string]string)
			if trk.Approach != "" {
				sttAc.AssignedApproach = trk.Approach
			}
			// Track runways we've seen to avoid duplicate LAHSO runway entries
			seenRunways := make(map[string]bool)
			// Add active approaches for the arrival airport
			for _, ar := range state.ArrivalRunways {
				if ar.Airport != trk.ArrivalAirport {
					continue
				}
				if ap, ok := state.Airports[ar.Airport]; ok {
					for code, appr := range ap.Approaches {
						if appr.Runway == ar.Runway {
							sttAc.CandidateApproaches[av.GetApproachTelephony(appr.FullName)] = code

							// Build fixes map for this approach
							approachFixes := make(map[string]string)
							for _, wps := range appr.Waypoints {
								for _, wp := range wps {
									fix := wp.Fix
									// Skip internal fixes (start with underscore) and invalid lengths
									if len(fix) >= 3 && len(fix) <= 5 && fix[0] != '_' {
										approachFixes[av.GetFixTelephony(fix)] = fix
									}
								}
							}
							if len(approachFixes) > 0 {
								sttAc.ApproachFixes[code] = approachFixes
							}
						}
					}
					// Build LAHSORunways for this arrival runway (avoid duplicates)
					if !seenRunways[ar.Runway] {
						seenRunways[ar.Runway] = true
						intersecting := av.IntersectingRunways(ar.Airport, ar.Runway, state.NmPerLongitude, 0.5)
						sttAc.LAHSORunways = append(sttAc.LAHSORunways, intersecting...)
					}
				}
			}

			// If there's an assigned approach, merge its fixes into the main Fixes map
			// so they're available for matching "proceed direct" commands
			if sttAc.AssignedApproach != "" {
				if approachFixes, ok := sttAc.ApproachFixes[sttAc.AssignedApproach]; ok {
					if sttAc.Fixes == nil {
						sttAc.Fixes = make(map[string]string)
					}
					for spoken, fix := range approachFixes {
						if _, exists := sttAc.Fixes[spoken]; !exists {
							sttAc.Fixes[spoken] = fix
						}
					}
				}
			}
		}

		// Key by telephony (spoken callsign)
		var cwt string
		if trk.FlightPlan != nil {
			cwt = trk.FlightPlan.CWTCategory
		}
		telephony := av.GetCallsignSpoken(string(trk.ADSBCallsign), cwt)

		// Default addressing form is full callsign
		sttAc.AddressingForm = sim.AddressingFormFull
		acCtx[telephony] = sttAc

		// For GA callsigns (N-prefix), also add type-based addressing variants
		callsign := string(trk.ADSBCallsign)
		if strings.HasPrefix(callsign, "N") && sttAc.AircraftType != "" {
			typePronunciations := av.GetACTypePronunciations(sttAc.AircraftType)
			if len(typePronunciations) > 0 {
				trailing3 := av.GetTrailing3Spoken(callsign)
				if trailing3 != "" {
					// Create a copy with TypeTrailing3 addressing form
					typeAc := sttAc
					typeAc.AddressingForm = sim.AddressingFormTypeTrailing3

					// Add entry for each pronunciation variant that doesn't contain numbers
					// (to avoid confusion with other callsigns)
					for _, typeSpoken := range typePronunciations {
						if strings.ContainsAny(typeSpoken, "0123456789") {
							continue
						}
						key := typeSpoken + " " + trailing3
						acCtx[key] = typeAc
					}
				}
			}
		}
	}

	return acCtx
}

// GetUsageStats returns usage statistics for this provider.
func (p *Transcriber) GetUsageStats() string {
	return "local algorithmic parser (no API calls)"
}

// ParseResult holds detailed parsing results for debugging/testing.
type ParseResult struct {
	Callsign       string
	Commands       []string
	Confidence     float64
	CallsignConf   float64
	CommandConf    float64
	ValidationConf float64
	Errors         []string
}

// ParseTranscriptDetailed provides detailed parsing results for testing.
func (p *Transcriber) ParseTranscriptDetailed(
	aircraft map[string]Aircraft,
	transcript string,
) ParseResult {
	result := ParseResult{}

	// Normalize and tokenize
	words := NormalizeTranscript(transcript)
	if len(words) == 0 {
		return result
	}

	tokens := Tokenize(words)
	if len(tokens) == 0 {
		return result
	}

	// Match callsign
	callsignMatch, remainingTokens := MatchCallsign(tokens, aircraft)
	result.Callsign = callsignMatch.Callsign
	result.CallsignConf = callsignMatch.Confidence

	if callsignMatch.Callsign == "" {
		return result
	}

	// Get aircraft context
	var ac Aircraft
	if callsignMatch.SpokenKey != "" {
		ac = aircraft[callsignMatch.SpokenKey]
	}

	// Parse commands
	commands, cmdConf := ParseCommands(remainingTokens, ac)
	result.CommandConf = cmdConf

	// Validate
	validation := ValidateCommands(commands, ac)
	result.Commands = validation.ValidCommands
	result.ValidationConf = validation.Confidence
	result.Errors = validation.Errors

	// Overall confidence
	result.Confidence = result.CallsignConf * result.CommandConf * result.ValidationConf

	return result
}

// detectNotForYouCorrection detects correction phrases like "that was not for you"
// which indicate the previous callsign was addressed by mistake.
// Returns the tokens after the correction phrase and true if found.
func detectNotForYouCorrection(tokens []Token) ([]Token, bool) {
	// Look for patterns like:
	// "that was not for you" or "not for you" at the start of tokens
	// These phrases mean the controller is correcting a mistaken callsign
	for i := range min(len(tokens), 4) {
		// Check for "not for you" pattern starting at position i
		if i+2 < len(tokens) {
			t0 := strings.ToLower(tokens[i].Text)
			t1 := strings.ToLower(tokens[i+1].Text)
			t2 := strings.ToLower(tokens[i+2].Text)
			if t0 == "not" && t1 == "for" && t2 == "you" {
				// Return tokens after "not for you"
				return tokens[i+3:], true
			}
		}
		if tokens[i].Text == "correction" || JaroWinkler(tokens[i].Text, "correction") > 0.9 || PhoneticMatch(tokens[i].Text, "correction") {
			return tokens[i+1:], true
		}
	}
	return tokens, false
}

// detectNegativeThatWasFor detects correction phrases like "negative, that was for {callsign}"
// which indicate the previous command went to the wrong aircraft due to STT callsign misinterpretation.
// This is used to trigger a ROLLBACK of the previous command.
//
// Patterns detected:
// - "negative that was for {callsign}..."
// - "no that was for {callsign}..."
// - "negative was for {callsign}..."
//
// Returns the tokens starting from the correct callsign and true if the pattern was found.
// The caller should then match the callsign and process the rest as a normal command,
// prepending ROLLBACK to undo the previous command.
func detectNegativeThatWasFor(tokens []Token) ([]Token, bool) {
	// Look for patterns starting with "negative" or "no" followed by "that was for" or "was for"
	for i := range min(len(tokens), 3) {
		w := strings.ToLower(tokens[i].Text)
		if w != "negative" && w != "no" {
			continue
		}

		// Found "negative" or "no" - check for "that was for" or "was for" following
		remaining := tokens[i+1:]
		if len(remaining) < 2 {
			continue
		}

		// Pattern 1: "negative/no that was for"
		if len(remaining) >= 3 {
			t0 := strings.ToLower(remaining[0].Text)
			t1 := strings.ToLower(remaining[1].Text)
			t2 := strings.ToLower(remaining[2].Text)
			if t0 == "that" && t1 == "was" && t2 == "for" {
				// Return tokens after "that was for" (starting at the callsign)
				return remaining[3:], true
			}
		}

		// Pattern 2: "negative/no was for"
		t0 := strings.ToLower(remaining[0].Text)
		t1 := strings.ToLower(remaining[1].Text)
		if t0 == "was" && t1 == "for" {
			// Return tokens after "was for" (starting at the callsign)
			return remaining[2:], true
		}
	}
	return tokens, false
}

// applyDisregard handles "disregard" or "correction" in tokens.
// For "disregard": discards everything before it.
// For "correction": if what follows is a complete command (contains command keywords),
// discard everything before. If it's just numbers (frequency correction), only discard
// the preceding numbers to preserve command keywords like "contact".
func applyDisregard(tokens []Token) []Token {
	for i := len(tokens) - 1; i >= 0; i-- {
		text := strings.ToLower(tokens[i].Text)
		if text == "disregard" {
			return tokens[i+1:]
		}
		if text == "correction" {
			// Check if tokens after "correction" contain command keywords
			// If so, it's a full command replacement - discard everything before
			afterCorrection := tokens[i+1:]
			hasCommandKeyword := false
			for j, t := range afterCorrection {
				w := strings.ToLower(t.Text)
				// Skip "left"/"right" if they follow a number (runway designation, not command)
				if (w == "left" || w == "right") && j > 0 && afterCorrection[j-1].Type == TokenNumber {
					continue
				}
				if isCommandKeyword(w) {
					hasCommandKeyword = true
					break
				}
			}
			if hasCommandKeyword {
				// Full command correction - discard everything before
				return afterCorrection
			}

			// Just numbers after correction (e.g., frequency) - only discard preceding numbers
			// e.g., "contact departure 12, correction 126.8"
			numStart := i
			for j := i - 1; j >= 0; j-- {
				if tokens[j].Type == TokenNumber || strings.ToLower(tokens[j].Text) == "point" {
					numStart = j
				} else {
					break
				}
			}
			// Keep tokens before the corrected numbers, add tokens after "correction"
			return append(tokens[:numStart], afterCorrection...)
		}
	}
	return tokens
}

// isCommandKeyword returns true if the word is a command keyword that indicates
// a full command is being spoken (not just a number/frequency correction).
func isCommandKeyword(w string) bool {
	commandKeywords := map[string]bool{
		"turn": true, "heading": true, "left": true, "right": true,
		"climb": true, "descend": true, "maintain": true,
		"speed": true, "reduce": true, "increase": true,
		"expect": true, "cleared": true, "direct": true,
		"contact": true, "squawk": true, "ident": true,
		"hold": true, "intercept": true, "localizer": true,
		"approach": true, "ils": true, "rnav": true, "visual": true,
	}
	return commandKeywords[w]
}

// isDisregardOnly returns true if the tokens consist only of "disregard"
// (possibly with filler words). This indicates the controller is telling
// the pilot to ignore the previous transmission, and no command should be sent.
func isDisregardOnly(tokens []Token) bool {
	hasDisregard := false
	for _, t := range tokens {
		text := strings.ToLower(t.Text)
		if text == "disregard" {
			hasDisregard = true
		} else if !IsFillerWord(text) {
			// Found a non-filler, non-disregard token
			return false
		}
	}
	return hasDisregard
}

// isAcknowledgmentOnly returns true if the tokens contain only acknowledgment
// words (roger, wilco, copy) and filler words. These are pilot readbacks that
// need no further action from the controller.
func isAcknowledgmentOnly(tokens []Token) bool {
	if len(tokens) == 0 {
		return false
	}

	acknowledgmentWords := map[string]bool{
		"roger": true, "wilco": true, "copy": true, "affirm": true, "affirmative": true,
	}

	hasAcknowledgment := false
	for _, t := range tokens {
		text := strings.ToLower(t.Text)
		if acknowledgmentWords[text] {
			hasAcknowledgment = true
		} else if !IsFillerWord(text) {
			// Non-acknowledgment, non-filler word found
			return false
		}
	}

	return hasAcknowledgment
}

// isRadarContactPhrase returns true if the tokens contain "radar contact" phrase,
// possibly with facility words/names and position identification. This is an
// informational phrase from controller to pilot that needs no action.
// Examples: "radar contact", "Miami Departure radar contact", "departure radar contact"
func isRadarContactPhrase(tokens []Token) bool {
	if len(tokens) < 2 {
		return false
	}

	// Facility type words that commonly appear with "radar contact"
	// (only positions that use radar - not ground/clearance/delivery)
	facilityTypes := map[string]bool{
		"departure": true, "approach": true, "tower": true, "center": true,
	}

	// Command keywords that would indicate this is NOT just a radar contact phrase
	commandKeywords := map[string]bool{
		"climb": true, "descend": true, "maintain": true, "turn": true,
		"heading": true, "direct": true, "cleared": true, "expect": true,
		"speed": true, "reduce": true, "increase": true, "squawk": true,
		"fly": true, "cross": true, "hold": true, "intercept": true,
	}

	hasRadar := false
	hasContact := false
	hasFacilityType := false

	for _, t := range tokens {
		text := strings.ToLower(t.Text)

		// Reject if we find a command keyword
		if commandKeywords[text] {
			return false
		}

		switch text {
		case "radar":
			hasRadar = true
		case "contact":
			hasContact = true
		default:
			if facilityTypes[text] {
				hasFacilityType = true
			}
			// Allow other words (facility names like "miami", "kennedy", etc.)
		}
	}

	// Must have both "radar" and "contact", and typically a facility type
	// (to distinguish from other uses of these words)
	return hasRadar && hasContact && hasFacilityType
}

// isPositionIdentification detects controller position identification phrases
// like "New York departure", "Kennedy approach", "Boston center", etc.
// These are informational (controller identifying themselves) and need no response.
//
// controllerRadioName is the user's radio name (e.g., "New York Departure").
// The function does fuzzy matching allowing "departure" and "approach" to be
// interchangeable since controllers may say either depending on context.
func isPositionIdentification(tokens []Token, controllerRadioName string) bool {
	if len(tokens) == 0 || controllerRadioName == "" {
		return false
	}

	// Find "radar contact" anywhere in the tokens
	radarContactIdx := -1
	for i := range len(tokens) - 1 {
		if strings.ToLower(tokens[i].Text) == "radar" &&
			strings.ToLower(tokens[i+1].Text) == "contact" {
			radarContactIdx = i
			break
		}
	}

	// If "radar contact" found and there are tokens after it, this isn't just position ID
	if radarContactIdx >= 0 && radarContactIdx+2 < len(tokens) {
		// There are tokens after "radar contact" - these are commands, not position ID
		logLocalStt("  position ID: 'radar contact' at position %d with %d tokens after - not position ID",
			radarContactIdx, len(tokens)-radarContactIdx-2)
		return false
	}

	// Determine lastIdx for phrase building
	lastIdx := len(tokens) - 1
	if radarContactIdx >= 0 {
		// "radar contact" is at the end, strip it
		lastIdx = radarContactIdx - 1
		if lastIdx < 0 {
			// Just "radar contact" with no position - still informational
			logLocalStt("  position ID: just 'radar contact' - informational")
			return true
		}
	}

	// Command keywords that indicate actual commands (not position identification).
	// If any of these appear in the tokens, this isn't JUST a position ID - there are
	// commands to process. Position-related words like "departure", "approach" are NOT in this set.
	commandStopWords := map[string]bool{
		"proceed": true, "direct": true, "climb": true, "descend": true,
		"maintain": true, "turn": true, "heading": true, "speed": true,
		"cleared": true, "expect": true, "vectors": true, "squawk": true,
		"contact": true, "cross": true, "expedite": true, "reduce": true,
		"increase": true, "fly": true, "intercept": true, "cancel": true,
		"resume": true, "ident": true, "go": true,
	}

	// Check if any command keywords appear in the tokens.
	// If so, this isn't just a position ID - there are commands to process.
	for i := 0; i <= lastIdx; i++ {
		word := strings.ToLower(tokens[i].Text)
		if commandStopWords[word] {
			logLocalStt("  position ID: found command keyword %q at position %d - not just position ID", word, i)
			return false
		}
	}

	// Build the phrase from remaining tokens (no command keywords present)
	var parts []string
	for i := 0; i <= lastIdx; i++ {
		parts = append(parts, strings.ToLower(tokens[i].Text))
	}
	phrase := strings.Join(parts, " ")

	// Normalize the controller radio name for comparison
	radioName := strings.ToLower(controllerRadioName)

	// Position suffixes that are interchangeable
	positionSuffixes := []string{"departure", "approach", "center", "tower", "ground"}

	// Check for exact or fuzzy match
	if FuzzyMatch(phrase, radioName, 0.75) {
		logLocalStt("  position ID: fuzzy match %q ~ %q", phrase, radioName)
		return true
	}

	// Try swapping position suffixes (e.g., "new york approach" matches "New York Departure")
	for _, suffix := range positionSuffixes {
		if strings.HasSuffix(phrase, suffix) {
			// Extract the facility part (everything before the suffix)
			facilityPart := strings.TrimSuffix(phrase, suffix)
			facilityPart = strings.TrimSpace(facilityPart)

			// Try matching with each alternative suffix
			for _, altSuffix := range positionSuffixes {
				altPhrase := facilityPart + " " + altSuffix
				if FuzzyMatch(altPhrase, radioName, 0.75) {
					logLocalStt("  position ID: fuzzy match with suffix swap %q ~ %q (orig: %q)",
						altPhrase, radioName, phrase)
					return true
				}
			}
			break // Only one suffix match is possible
		}
	}

	// Check if the phrase ends with a position suffix and the facility part fuzzy matches
	// the controller's facility (e.g., "mumble departure" where mumble ~ "new york")
	for _, suffix := range positionSuffixes {
		if strings.HasSuffix(phrase, suffix) {
			spokenFacility := strings.TrimSuffix(phrase, suffix)
			spokenFacility = strings.TrimSpace(spokenFacility)

			// Extract facility from radio name
			for _, radioSuffix := range positionSuffixes {
				if strings.HasSuffix(radioName, radioSuffix) {
					radioFacility := strings.TrimSuffix(radioName, radioSuffix)
					radioFacility = strings.TrimSpace(radioFacility)

					if spokenFacility != "" && radioFacility != "" &&
						FuzzyMatch(spokenFacility, radioFacility, 0.70) {
						logLocalStt("  position ID: facility fuzzy match %q ~ %q (suffix: %s)",
							spokenFacility, radioFacility, suffix)
						return true
					}
					break
				}
			}
			break
		}
	}

	return false
}

// logging helpers

func (p *Transcriber) logInfo(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Infof(format, args...)
	}
}
