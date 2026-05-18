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
	Init()
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
	_ string, // controllerRadioName - currently unused
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
		logLocalStt(`empty transcript, returning ""`)
		return "", nil
	}

	// Layer 1: Phonetic normalization
	words := NormalizeTranscript(transcript)
	logLocalStt("normalized words: %v", words)
	if len(words) == 0 {
		logLocalStt(`no words after normalization, returning ""`)
		return "", nil
	}

	// Layer 2: Tokenization
	tokens := Tokenize(words)
	logLocalStt("tokens: %d", len(tokens))
	for i, t := range tokens {
		logLocalStt("  token[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}
	if len(tokens) == 0 {
		logLocalStt(`no tokens, returning ""`)
		return "", nil
	}

	var callsign string
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
		var earlyResult string
		callsign, ac, commandTokens, callsignConfidence, earlyResult =
			p.resolveCallsign(tokens, aircraft, transcript, start)
		if earlyResult != "" {
			return earlyResult, nil
		}
		if callsign == "" {
			return "", nil
		}
	}

	logLocalStt("aircraft context: State=%q Altitude=%d Fixes=%d Approaches=%d VisualApproaches=%d AssignedApproach=%q LAHSORunways=%v",
		ac.State, ac.Altitude, len(ac.Fixes), len(ac.CandidateApproaches), len(ac.CandidateVisualApproaches), ac.AssignedApproach, ac.LAHSORunways)
	for spokenName, fixID := range ac.Fixes {
		logLocalStt("  fix: %q -> %q", spokenName, fixID)
	}
	for spokenName, apprID := range ac.CandidateApproaches {
		logLocalStt("  approach: %q -> %q", spokenName, apprID)
	}
	for spokenName, runway := range ac.CandidateVisualApproaches {
		logLocalStt("  visual approach: %q -> %q", spokenName, runway)
	}

	// Handle "disregard" or "correction" in remaining tokens
	// This discards previous command attempts but preserves callsign
	commandTokens = applyDisregard(commandTokens)
	logLocalStt("command tokens after disregard: %d", len(commandTokens))
	for i, t := range commandTokens {
		logLocalStt("  token[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}

	// Classify and early-return for non-command transmissions
	if kind := classifyTransmission(commandTokens); kind != transmissionCommand {
		logLocalStt("classified as %s, returning empty", kind)
		elapsed := time.Since(start)
		logLocalStt(`=== DecodeTranscript END: "" (%s, time=%s) ===`, kind, elapsed)
		p.logInfo(`local STT: %q -> "" (%s, time=%s)`, transcript, kind, elapsed)
		return "", nil
	}

	// Strip informational phrases (position ID prefix, radar contact, altimeter setting)
	commandTokens = stripInformational(commandTokens)

	// If no tokens remain after stripping, controller just identified themselves.
	// For VFR aircraft, treat this as an implicit "go ahead" — the pilot is
	// checking in on frequency with just their callsign + facility name.
	if len(commandTokens) == 0 {
		if ac.State == "vfr flight following" {
			output := callsign + " GA"
			elapsed := time.Since(start)
			logLocalStt("VFR aircraft with position ID only, treating as implicit go ahead")
			logLocalStt(`=== DecodeTranscript END: %q (implicit GA, time=%s) ===`, output, elapsed)
			p.logInfo(`local STT: %q -> %q (implicit GA, time=%s)`, transcript, output, elapsed)
			return output, nil
		}
		logLocalStt("no tokens after stripping prefixes, returning empty")
		elapsed := time.Since(start)
		logLocalStt(`=== DecodeTranscript END: "" (position ID only, time=%s) ===`, elapsed)
		p.logInfo(`local STT: %q -> "" (position ID only, time=%s)`, transcript, elapsed)
		return "", nil
	}

	// Re-classify after stripping — position ID removal may reveal an
	// acknowledgment-only or radar-contact-only transmission.
	if kind := classifyTransmission(commandTokens); kind != transmissionCommand {
		logLocalStt("classified as %s after stripping, returning empty", kind)
		elapsed := time.Since(start)
		logLocalStt(`=== DecodeTranscript END: "" (%s, time=%s) ===`, kind, elapsed)
		p.logInfo(`local STT: %q -> "" (%s, time=%s)`, transcript, kind, elapsed)
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

	// If no valid commands were found but the tokens contain a greeting word
	// (hello, hi, hey), this is likely a check-in, not a garbled command.
	// Return empty instead of AGAIN.
	if len(validation.ValidCommands) == 0 && containsGreeting(commandTokens) {
		logLocalStt("no commands but greeting detected, returning empty")
		elapsed := time.Since(start)
		logLocalStt(`=== DecodeTranscript END: "" (greeting, time=%s) ===`, elapsed)
		p.logInfo(`local STT: %q -> "" (greeting, time=%s)`, transcript, elapsed)
		return "", nil
	}

	// Generate output
	var output string
	// cmdConf > 0 with no commands means a pattern matched but intentionally
	// produced no output (e.g., "standby for the approach"). Treat as
	// understood — return just the callsign, not AGAIN.
	noCommands := len(validation.ValidCommands) == 0 && cmdConf == 0
	if isFallback {
		if noCommands {
			output = "AGAIN"
		} else {
			output = strings.Join(validation.ValidCommands, " ")
		}
	} else {
		if noCommands {
			// Only emit AGAIN when the callsign match is confident enough.
			// A weak airline match combined with a coincidental flight
			// number can produce a false callsign; requesting a repeat
			// from the wrong aircraft is worse than silence.
			if callsignConfidence >= 0.93 {
				output = callsign + " AGAIN"
			}
		} else if len(validation.ValidCommands) == 0 {
			// A pattern matched but produced no command (informational
			// transmission). Emit just the callsign when confident; stay
			// silent for low-confidence matches for the same reason as AGAIN.
			if callsignConfidence >= 0.93 {
				output = callsign
			}
		} else {
			output = callsign + " " + strings.Join(validation.ValidCommands, " ")
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

// resolveCallsign handles callsign identification for the non-fallback path.
// It detects "negative that was for" corrections, performs callsign matching,
// and handles "not for you" corrections.
// Returns earlyResult non-empty when the function should return immediately (e.g., ROLLBACK).
// Returns callsign="" when no callsign could be matched.
func (p *Transcriber) resolveCallsign(
	tokens []Token, aircraft map[string]Aircraft, transcript string, start time.Time,
) (callsign string, ac Aircraft, cmdTokens []Token, confidence float64, earlyResult string) {
	confidence = 1.0

	// Check for "negative, that was for {callsign}" correction pattern BEFORE callsign matching.
	// e.g., "Negative that was for Delta 456. Delta 456, turn left heading 270"
	// This triggers a ROLLBACK of the last command, then processes the rest for the correct aircraft.
	if tokensAfterNegative, found := detectNegativeThatWasFor(tokens); found {
		logLocalStt("detected 'negative that was for' correction pattern at start")
		correctMatch, correctRemaining := MatchCallsign(tokensAfterNegative, aircraft)
		if correctMatch.Callsign != "" {
			logLocalStt("correct callsign match: Callsign=%q SpokenKey=%q Conf=%.2f",
				correctMatch.Callsign, correctMatch.SpokenKey, correctMatch.Confidence)

			var correctAc Aircraft
			if correctMatch.SpokenKey != "" {
				correctAc = aircraft[correctMatch.SpokenKey]
			}

			commands, cmdConf := ParseCommands(correctRemaining, correctAc)
			logLocalStt("parsed commands for correct callsign: %v (conf=%.2f)", commands, cmdConf)
			validation := ValidateCommands(commands, correctAc)
			logLocalStt("validated commands: %v", validation.ValidCommands)

			var output string
			if len(validation.ValidCommands) > 0 {
				output = "ROLLBACK " + correctMatch.Callsign + " " + strings.Join(validation.ValidCommands, " ")
			} else {
				output = "ROLLBACK"
			}

			elapsed := time.Since(start)
			logLocalStt("=== DecodeTranscript END: %q (negative correction, time=%s) ===", output, elapsed)
			p.logInfo("local STT: %q -> %q (negative correction, time=%s)", transcript, output, elapsed)
			earlyResult = strings.TrimSpace(output)
			return
		}
		logLocalStt("couldn't match correct callsign after 'negative that was for', returning just ROLLBACK")
		elapsed := time.Since(start)
		p.logInfo("local STT: %q -> ROLLBACK (negative correction, no new callsign, time=%s)", transcript, elapsed)
		earlyResult = "ROLLBACK"
		return
	}

	// Layer 3: Callsign matching
	callsignMatch, remainingTokens := MatchCallsign(tokens, aircraft)
	logLocalStt("callsign match: Callsign=%q SpokenKey=%q Conf=%.2f Consumed=%d",
		callsignMatch.Callsign, callsignMatch.SpokenKey, callsignMatch.Confidence, callsignMatch.Consumed)

	if callsignMatch.Callsign == "" {
		// Check for "negative, {commands}" without callsign.
		// Skip validation since we don't know the target aircraft context.
		if remaining, found := detectNegativePrefix(tokens); found {
			commands, _ := ParseCommands(remaining, Aircraft{})
			if len(commands) > 0 {
				output := "ROLLBACK " + strings.Join(commands, " ")
				elapsed := time.Since(start)
				logLocalStt("=== DecodeTranscript END: %q (negative without callsign, time=%s) ===", output, elapsed)
				p.logInfo("local STT: %q -> %q (negative without callsign, time=%s)", transcript, output, elapsed)
				earlyResult = output
				return
			}
		}
		logLocalStt("no callsign match for %q, ignoring", transcript)
		return // callsign is "", earlyResult is ""
	}

	// Check for "not for you" correction pattern
	// e.g., "479, that was not for you, Virgin 47 Foxtrot, expect..."
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
	confidence = callsignMatch.Confidence
	cmdTokens = remainingTokens

	if callsignMatch.SpokenKey != "" {
		ac = aircraft[callsignMatch.SpokenKey]
	}
	return
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
			// For 4-letter ICAO airports, add all telephony variants so STT can match any of them.
			// For 3-letter identifiers, prefer navaid lookup (via GetFixTelephony) since these
			// are typically VORs, not airports. This avoids collisions where a 3-letter VOR
			// identifier matches a foreign airport ICAO code.
			if len(fix) == 4 {
				if variants := av.GetAirportTelephonyVariants(fix); len(variants) > 0 {
					for _, variant := range variants {
						sttAc.Fixes[variant] = fix
					}
					continue
				}
			}
			sttAc.Fixes[av.GetFixTelephony(fix)] = fix
		}

		// Determine state and set SID/STAR
		if trk.IsDeparture() {
			sttAc.State = "departure"
			sttAc.SID = trk.SID
			sttAc.STAR = trk.STAR // Local departures may have a STAR
		} else if trk.IsArrival() {
			if trk.ClearedForApproach {
				sttAc.State = "cleared approach"
			} else {
				sttAc.State = "arrival"
			}
			sttAc.STAR = trk.STAR
		} else if trk.IsOverflight() {
			sttAc.State = "overflight"
		} else if trk.RequestedFlightFollowing {
			sttAc.State = "vfr flight following"
		}

		// Build candidate approaches for arrivals and local departures
		// (departures arriving at a TRACON airport need approach commands too)
		_, arrivingLocally := state.ArrivalAirports[trk.ArrivalAirport]
		if trk.ArrivalAirport != "" && (trk.IsArrival() || arrivingLocally) {
			sttAc.CandidateApproaches = make(map[string]string)
			sttAc.CandidateVisualApproaches = make(map[string]string)
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
					visualRunway := ar.Runway.Base()
					for _, phrase := range visualApproachTelephonyVariants(visualRunway) {
						sttAc.CandidateVisualApproaches[phrase] = visualRunway
					}

					for code, appr := range ap.Approaches {
						if appr.Runway == ar.Runway.Base() {
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
					if !seenRunways[ar.Runway.Base()] {
						seenRunways[ar.Runway.Base()] = true
						intersecting := av.IntersectingRunways(ar.Airport, ar.Runway, state.NmPerLongitude, 0.5)
						sttAc.LAHSORunways = append(sttAc.LAHSORunways, intersecting...)
					}
				}
			}

			// If there's an assigned approach, merge its fixes into the main Fixes map
			// so they're available for matching "proceed direct" commands.
			// AssignedApproach is a full name (e.g., "ILS Runway 22L") but
			// ApproachFixes is keyed by short code (e.g., "I2L"). Convert
			// via GetApproachTelephony + CandidateApproaches.
			if sttAc.AssignedApproach != "" {
				telephony := av.GetApproachTelephony(sttAc.AssignedApproach)
				if code, ok := sttAc.CandidateApproaches[telephony]; ok {
					if approachFixes, ok := sttAc.ApproachFixes[code]; ok {
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
		}

		// Key by telephony (spoken callsign). Use the true CWT category
		// from the aircraft performance DB rather than the NAS flight plan,
		// since the user may have changed the flight plan's aircraft type.
		telephony := av.GetCallsignSpoken(string(trk.ADSBCallsign), trk.CWTCategory)

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
					// Create a copy with TypeTrailing3 addressing form.
					// Bake /T into the callsign so downstream code gets it automatically.
					typeAc := sttAc
					typeAc.Callsign += "/T"
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

		// Check for bare "correction" keyword, but only when the next
		// meaningful token is NOT a command keyword. When a command keyword
		// follows (e.g., "correction descend and maintain..."), this is a
		// command self-correction handled by applyDisregard, not a callsign
		// re-addressing.
		if strings.ToLower(tokens[i].Text) == "correction" {
			followedByCommand := false
			for j := i + 1; j < len(tokens); j++ {
				w := strings.ToLower(tokens[j].Text)
				if IsFillerWord(w) {
					continue
				}
				if IsCommandKeyword(w) {
					followedByCommand = true
				}
				break
			}
			if !followedByCommand {
				return tokens[i+1:], true
			}
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

// detectNegativePrefix checks if tokens start with "negative" or "no" (without
// "that was for" / "was for" following). Returns the remaining tokens after the
// negative word. This is used for "negative, {commands}" without callsign.
func detectNegativePrefix(tokens []Token) ([]Token, bool) {
	if len(tokens) < 2 {
		return nil, false
	}
	w := strings.ToLower(tokens[0].Text)
	if w != "negative" && w != "no" {
		return nil, false
	}
	return tokens[1:], true
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
				if IsCommandKeyword(w) {
					hasCommandKeyword = true
					break
				}
			}
			if hasCommandKeyword {
				// Check if afterCorrection starts with a number or altitude - if so, we need to
				// preserve the command type context from before "correction".
				// e.g., "fly heading 270 correction 290 join localizer" - the "290"
				// needs "heading" to be parsed correctly as H290.
				// Note: altitude values like "four thousand" become TokenAltitude, not TokenNumber.
				if len(afterCorrection) > 0 && (afterCorrection[0].Type == TokenNumber || afterCorrection[0].Type == TokenAltitude) {
					// Look back to find what command type was being corrected
					if keyword := findCommandTypeKeyword(tokens[:i]); keyword != "" {
						logLocalStt("  correction: preserving command keyword %q for corrected value", keyword)
						// Prepend the keyword to afterCorrection
						keywordToken := Token{Text: keyword, Type: TokenWord}
						return append([]Token{keywordToken}, afterCorrection...)
					}
				}
				// Check for partial correction: afterCorrection starts with a command
				// keyword (e.g., "maintain 3000") and there are commands of a different
				// category before "correction" (e.g., "turn left heading 150 maintain
				// 5000 correction maintain 3000"). In this case only the same-category
				// command is corrected; earlier different-category commands are kept.
				if len(afterCorrection) > 0 {
					firstWord := strings.ToLower(afterCorrection[0].Text)
					if cat := correctionKeywordCategory(firstWord); cat != "" {
						// Scan backward from "correction" to find the nearest
						// same-category keyword and check for different-category ones.
						sameCatIdx := -1
						hasDifferentCat := false
						for j := i - 1; j >= 0; j-- {
							w := strings.ToLower(tokens[j].Text)
							if c := correctionKeywordCategory(w); c != "" {
								if c == cat && sameCatIdx < 0 {
									sameCatIdx = j
								} else if c != cat {
									hasDifferentCat = true
								}
							}
						}
						if sameCatIdx >= 0 && hasDifferentCat {
							logLocalStt("  correction: partial correction — keeping tokens before index %d, replacing %s command", sameCatIdx, cat)
							return append(tokens[:sameCatIdx], afterCorrection...)
						}
					}
				}
				// Full command correction - discard everything before
				return afterCorrection
			}

			// No command keywords after correction - this is a value correction.
			// Scan backward to find and replace the corrected value.
			// Handle both numeric values (e.g., "contact departure 12, correction 126.8")
			// and plain words like fix names (e.g., "direct lever correction haupt").
			valStart := i
			for j := i - 1; j >= 0; j-- {
				w := strings.ToLower(tokens[j].Text)
				if tokens[j].Type == TokenNumber || w == "point" ||
					w == "miles" || w == "knots" || w == "degrees" {
					valStart = j
				} else {
					break
				}
			}
			// If no numbers were stripped and the token directly before "correction"
			// is a plain word (not a command keyword), strip that single word.
			// This handles fix name corrections like "direct lever correction haupt"
			// where "lever" (the mis-spoken fix) should be replaced by "haupt".
			if valStart == i && i > 0 {
				prev := strings.ToLower(tokens[i-1].Text)
				if tokens[i-1].Type == TokenWord && !IsCommandKeyword(prev) {
					valStart = i - 1
				}
			}
			// Keep tokens before the corrected value, add tokens after "correction"
			return append(tokens[:valStart], afterCorrection...)
		}
	}
	return tokens
}

// findCommandTypeKeyword scans tokens backwards to find the most recent command
// type keyword that indicates what kind of value (heading, speed, altitude) was
// being specified. This is used when "correction <number>" needs to inherit the
// command type from the corrected portion.
// Returns the keyword to prepend (e.g., "heading", "speed", "descend") or empty string if none found.
func findCommandTypeKeyword(tokens []Token) string {
	// Scan backwards through tokens looking for command type indicators
	// For altitude, we may find "maintain" first but should keep looking for "descend"/"climb"
	// since those are more specific (e.g., "descend and maintain 5000" -> prefer "descend")
	var altitudeKeyword string

	for i := len(tokens) - 1; i >= 0; i-- {
		w := strings.ToLower(tokens[i].Text)

		// Heading indicators - return immediately
		if w == "heading" || FuzzyMatch(w, "heading", 0.85) {
			return "heading"
		}
		// "fly" before heading context implies heading
		if w == "fly" || w == "flight" || FuzzyMatch(w, "fly", 0.80) || FuzzyMatch(w, "flight", 0.80) {
			return "heading"
		}
		// "turn" with "left"/"right" implies heading
		if w == "turn" {
			return "heading"
		}

		// Speed indicators - return immediately
		if w == "speed" || FuzzyMatch(w, "speed", 0.85) {
			return "speed"
		}
		if w == "reduce" || w == "slow" || w == "increase" {
			return "speed"
		}
		// "knots" indicates speed context
		if w == "knots" || FuzzyMatch(w, "knots", 0.85) {
			return "speed"
		}

		// Altitude indicators - "descend"/"climb" are preferred over "maintain"
		if w == "descend" || FuzzyMatch(w, "descend", 0.85) {
			return "descend"
		}
		if w == "climb" || FuzzyMatch(w, "climb", 0.85) {
			return "climb"
		}
		if w == "altitude" {
			return "maintain"
		}
		// "maintain" could be speed or altitude - check context, but keep looking for descend/climb
		if w == "maintain" && altitudeKeyword == "" {
			// Look ahead to see if there's altitude context (thousand, hundred, flight level)
			for j := i + 1; j < len(tokens); j++ {
				next := strings.ToLower(tokens[j].Text)
				if next == "thousand" || next == "hundred" || next == "feet" || next == "level" {
					altitudeKeyword = "maintain"
					break
				}
				if next == "speed" || next == "knots" {
					return "speed"
				}
			}
			if altitudeKeyword == "" {
				// Default to maintain (altitude) as it's more common
				altitudeKeyword = "maintain"
			}
		}
	}

	// Return altitude keyword if found (maintain, but descend/climb would have returned early)
	return altitudeKeyword
}

// correctionKeywordCategory returns the command category for a keyword used in
// partial-correction detection: "altitude", "heading", or "speed". Returns ""
// if the word is not a category-relevant keyword.
func correctionKeywordCategory(word string) string {
	switch word {
	case "maintain", "descend", "climb", "altitude":
		return "altitude"
	case "heading", "turn", "fly":
		return "heading"
	case "speed", "reduce", "slow", "increase":
		return "speed"
	default:
		return ""
	}
}

// transmissionKind categorizes a transmission for early-return decisions.
type transmissionKind int

const (
	transmissionCommand        transmissionKind = iota // Contains actionable commands
	transmissionDisregard                              // "disregard" only
	transmissionAcknowledgment                         // roger, wilco, copy, etc.
	transmissionRadarContact                           // "radar contact" with no real commands
)

func (k transmissionKind) String() string {
	switch k {
	case transmissionDisregard:
		return "disregard"
	case transmissionAcknowledgment:
		return "acknowledgment"
	case transmissionRadarContact:
		return "radar contact"
	default:
		return "command"
	}
}

// classifyTransmission returns the kind of transmission represented by tokens.
// Non-command transmissions (disregard, acknowledgment, radar contact) should
// be returned as empty strings without parsing commands.
func classifyTransmission(tokens []Token) transmissionKind {
	if len(tokens) == 0 {
		return transmissionCommand
	}

	acknowledgmentWords := map[string]bool{
		"roger": true, "wilco": true, "copy": true, "affirm": true, "affirmative": true,
		"hello": true, "hey": true, "hi": true, "howdy": true,
	}

	hasDisregard := false
	hasAcknowledgment := false
	hasRadarContact := false
	allFillerOrSpecial := true

	for i, t := range tokens {
		text := strings.ToLower(t.Text)
		switch {
		case text == "disregard":
			hasDisregard = true
		case acknowledgmentWords[text]:
			hasAcknowledgment = true
		case text == "radar" && i+1 < len(tokens) && strings.ToLower(tokens[i+1].Text) == "contact":
			hasRadarContact = true
		case text == "contact" && i > 0 && strings.ToLower(tokens[i-1].Text) == "radar":
			// Already counted as part of "radar contact" pair
		case IsFillerWord(text):
			// Filler words don't affect classification
		default:
			allFillerOrSpecial = false
		}
	}

	if !allFillerOrSpecial {
		// If there's a radar contact phrase but also other non-filler,
		// non-special tokens, check if any are command keywords.
		if hasRadarContact {
			hasCommandKeyword := false
			for _, t := range tokens {
				text := strings.ToLower(t.Text)
				if text == "radar" || text == "contact" {
					continue
				}
				if IsCommandKeyword(text) {
					hasCommandKeyword = true
					break
				}
			}
			if !hasCommandKeyword {
				return transmissionRadarContact
			}
		}
		return transmissionCommand
	}

	// All tokens are filler/special — classify by what special words we found
	if hasDisregard {
		return transmissionDisregard
	}
	if hasAcknowledgment {
		return transmissionAcknowledgment
	}
	if hasRadarContact {
		return transmissionRadarContact
	}
	return transmissionCommand
}

// containsGreeting returns true if any token is a greeting word.
// Used to detect check-in transmissions that have no actionable commands.
func containsGreeting(tokens []Token) bool {
	for _, t := range tokens {
		text := strings.ToLower(t.Text)
		if text == "hello" || text == "hi" || text == "hey" || text == "howdy" {
			return true
		}
	}
	return false
}

// stripInformational applies all informational prefix/suffix strippers in sequence:
// position ID prefix, radar contact prefix, and altimeter setting suffix.
func stripInformational(tokens []Token) []Token {
	tokens = stripPositionIDPrefix(tokens)
	tokens = stripRadarContactPrefix(tokens)
	tokens = stripAltimeterSuffix(tokens)
	return tokens
}

// stripPositionIDPrefix removes a controller position identification prefix
// from the tokens (e.g., "New York departure", "Boston approach").
// This appears right after the callsign when the controller identifies themselves.
// Only strips if no command keyword appears before the position suffix.
func stripPositionIDPrefix(tokens []Token) []Token {
	if len(tokens) == 0 {
		return tokens
	}

	positionSuffixes := map[string]bool{
		"departure": true, "approach": true, "center": true,
	}

	commandKeywords := map[string]bool{
		"climb": true, "climbing": true, "descend": true, "descending": true,
		"maintain": true, "turn": true, "heading": true, "speed": true,
		"direct": true, "proceed": true, "cleared": true, "expect": true,
		"contact": true, "squawk": true, "ident": true, "cross": true,
		"hold": true, "intercept": true, "fly": true, "reduce": true,
		"increase": true, "expedite": true, "cancel": true, "canceled": true, "cancelled": true,
		"resume": true, "vectors": true, "go": true, "standby": true,
	}

	for i, t := range tokens {
		text := strings.ToLower(t.Text)
		if commandKeywords[text] {
			return tokens
		}
		if positionSuffixes[text] {
			remaining := tokens[i+1:]
			if len(remaining) < len(tokens) {
				logLocalStt("stripped position ID prefix: %d tokens", i+1)
			}
			return remaining
		}
		if i >= 4 {
			break
		}
	}

	return tokens
}

// stripRadarContactPrefix removes "radar contact" from the start of tokens.
func stripRadarContactPrefix(tokens []Token) []Token {
	if len(tokens) >= 2 &&
		strings.ToLower(tokens[0].Text) == "radar" &&
		strings.ToLower(tokens[1].Text) == "contact" {
		logLocalStt("stripped 'radar contact' prefix")
		return tokens[2:]
	}
	return tokens
}

// stripAltimeterSuffix removes an altimeter setting from the end of the
// token stream. Controllers often append "(airport) altimeter (setting)"
// as informational; it is not an actionable command.
func stripAltimeterSuffix(tokens []Token) []Token {
	for i, t := range tokens {
		if strings.ToLower(t.Text) != "altimeter" {
			continue
		}
		if i+1 >= len(tokens) || tokens[i+1].Type != TokenNumber {
			continue
		}
		if i+2 < len(tokens) {
			continue
		}
		start := i
		if start > 0 && tokens[start-1].Type == TokenWord &&
			!IsCommandKeyword(strings.ToLower(tokens[start-1].Text)) {
			start--
		}
		logLocalStt("stripped altimeter suffix: %d tokens", len(tokens)-start)
		return tokens[:start]
	}
	return tokens
}

// logging helpers

func (p *Transcriber) logInfo(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Infof(format, args...)
	}
}
