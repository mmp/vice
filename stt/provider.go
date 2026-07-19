package stt

import (
	"maps"
	"math"
	"slices"
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

	// A transmission that OPENS with "correction" revises the previous
	// transmission (nothing precedes it to correct within this one): undo
	// the previous command and issue what follows.
	rollback := false
	if idx := slices.IndexFunc(commandTokens, func(t Token) bool {
		return !IsFillerWord(strings.ToLower(t.Text))
	}); idx >= 0 && strings.ToLower(commandTokens[idx].Text) == "correction" {
		rollback = true
		commandTokens = commandTokens[idx+1:]
		logLocalStt("leading 'correction': will ROLLBACK previous command")
	}

	// Handle "disregard" or "correction" in remaining tokens
	// This discards previous command attempts but preserves callsign
	hadTokens := len(commandTokens) > 0
	commandTokens = applyDisregard(commandTokens)
	logLocalStt("command tokens after disregard: %d", len(commandTokens))
	for i, t := range commandTokens {
		logLocalStt("  token[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}

	// A transmission that retracts itself ("disregard", possibly with
	// trailing filler) requires no response.
	if hadTokens && countNonFiller(commandTokens) == 0 {
		logLocalStt("disregard-only transmission, returning empty")
		elapsed := time.Since(start)
		logLocalStt(`=== DecodeTranscript END: "" (disregard, time=%s) ===`, elapsed)
		p.logInfo(`local STT: %q -> "" (disregard, time=%s)`, transcript, elapsed)
		return "", nil
	}

	// Altimeter readings are stripped positionally: the 4-digit reading
	// after "altimeter" absorbs arbitrary garble ("right" for "niner"),
	// which template matching cannot express.
	commandTokens = stripAltimeterSuffix(commandTokens)

	// Layer 4: Command parsing. Informational phrases — position IDs,
	// "radar contact", acknowledgments, sign-offs — match kind-tagged
	// templates that emit no commands; what kinds were seen drives the
	// output assembly below.
	parse := parseTransmission(commandTokens, ac)
	logLocalStt("parsed commands: %v (conf=%.2f, kinds=%b)", parse.commands, parse.conf, parse.kinds)

	// Layer 5: Validation
	validation := ValidateCommands(parse.commands, ac)
	logLocalStt("validated commands: %v (conf=%.2f)", validation.ValidCommands, validation.Confidence)
	if len(validation.Errors) > 0 {
		logLocalStt("validation errors: %v", validation.Errors)
	}

	// Compute overall confidence
	confidence := callsignConfidence
	if len(parse.commands) > 0 {
		confidence *= parse.conf * validation.Confidence
	}

	// Generate output.
	var output string
	// conf > 0 with no commands means a pattern matched but intentionally
	// produced no output. Informational-only transmissions (no
	// command-kind segment at all) require no response beyond, at most,
	// an implicit go-ahead or frequency change.
	noCommands := len(validation.ValidCommands) == 0 && parse.conf == 0
	informationalOnly := len(validation.ValidCommands) == 0 && parse.conf > 0 &&
		parse.kinds != 0 && !parse.sawKind(kindCommand)
	rollbackPrefix := ""
	if rollback && len(validation.ValidCommands) > 0 {
		rollbackPrefix = "ROLLBACK "
	}
	if isFallback {
		if noCommands {
			output = "AGAIN"
		} else {
			output = rollbackPrefix + strings.Join(validation.ValidCommands, " ")
		}
	} else if len(validation.ValidCommands) > 0 {
		output = callsign + " " + rollbackPrefix + strings.Join(validation.ValidCommands, " ")
	} else if informationalOnly {
		switch {
		case parse.sawKind(kindPositionID) && parse.sawKind(kindSignOff):
			// The controller named a facility and signed off: a handoff.
			logLocalStt("position ID with sign-off, treating as handoff")
			output = callsign + " FC"
		case parse.sawKind(kindAcknowledgment):
			// Acknowledgments and greetings require no response.
			logLocalStt("acknowledgment-only transmission, returning empty")
		case parse.sawKind(kindPositionID) && ac.State == "vfr flight following":
			// A VFR pilot addressed with just the facility name is being
			// invited to check in.
			logLocalStt("VFR aircraft with position ID only, treating as implicit go ahead")
			output = callsign + " GA"
		default:
			// Radar contact and the like require no response.
			logLocalStt("informational-only transmission, returning empty")
		}
	} else if noCommands {
		// Only emit AGAIN when the callsign match is confident enough.
		// A weak airline match combined with a coincidental flight
		// number can produce a false callsign; requesting a repeat
		// from the wrong aircraft is worse than silence.
		if callsignConfidence >= 0.93 {
			output = callsign + " AGAIN"
		}
	} else {
		// A command-kind pattern matched but produced no command (e.g.,
		// "standby for the approach"). Emit just the callsign when
		// confident; stay silent for low-confidence matches for the same
		// reason as AGAIN.
		if callsignConfidence >= 0.93 {
			output = callsign
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

	// Layer 3: Callsign matching. Among near-tied candidates, prefer one
	// whose following tokens parse as commands.
	var callsignMatch CallsignMatch
	remainingTokens := tokens
	if cands := MatchCallsignCandidates(tokens, aircraft); len(cands) > 0 {
		callsignMatch = selectCallsignByCommands(tokens, cands, aircraft)
		remainingTokens = tokens[callsignMatch.Consumed:]
	}
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

// selectCallsignByCommands picks among near-tied callsign candidates by
// whether the tokens that follow parse as commands: if the best-scoring
// callsign leaves an unparseable remainder but a near-tied alternative's
// remainder parses cleanly, the alternative likely absorbed the right
// tokens and is the real addressee.
func selectCallsignByCommands(tokens []Token, cands []CallsignMatch, aircraft map[string]Aircraft) CallsignMatch {
	best := cands[0]
	if len(cands) == 1 {
		return best
	}

	// Probe with a cloned fix map so expect-approach fix injection during
	// probing doesn't leak into the shared aircraft context. The probe's
	// quality combines how well the remaining tokens parse (the beam's
	// coverage-adjusted score) with the callsign evidence, so a candidate
	// whose remainder parses as a complete command beats one that absorbed
	// half the command into the callsign.
	probe := func(c CallsignMatch) (float64, bool) {
		ac := aircraft[c.SpokenKey]
		ac.Fixes = maps.Clone(ac.Fixes)
		parse := parseTransmission(tokens[c.Consumed:], ac)
		if len(parse.commands) == 0 && parse.conf == 0 {
			return math.Inf(-1), false
		}
		return parse.score + math.Log(max(c.Confidence, 1e-3)), len(parse.commands) > 0
	}

	bestQuality, _ := probe(best)
	for _, c := range cands[1:] {
		if best.Confidence-c.Confidence > callsignJointMargin {
			break
		}
		// Displacing the leader requires actual commands — a remainder
		// that merely scores well as informational chatter is not a
		// better reading of the transmission.
		if q, hasCommands := probe(c); hasCommands && q > bestQuality+scoreCallsignSwitchGain {
			logLocalStt("  callsign %q (conf=%.3f consumed=%d) parses better than %q: selecting it",
				c.Callsign, c.Confidence, c.Consumed, best.Callsign)
			best = c
			bestQuality = q
		}
	}
	return best
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
			Route:               trk.RouteFixes,
			ExpectedDirectFix:   trk.ExpectedDirectFix,
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
			// Also check whether a command keyword precedes "correction".
			// If so, this is a value/command self-correction (e.g.,
			// "speed one eighty correction one sixty"), not a callsign
			// re-addressing. applyDisregard handles this case.
			precededByCommand := false
			for j := i - 1; j >= 0; j-- {
				w := strings.ToLower(tokens[j].Text)
				if IsCommandKeyword(w) {
					precededByCommand = true
					break
				}
			}
			if !followedByCommand && !precededByCommand {
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

// correctionTypeKeywords maps command-type indicator words, in check
// order, to the keyword prepended when a bare corrected value inherits
// its command type ("fly heading 270 correction 290").
var correctionTypeKeywords = []struct{ word, keyword string }{
	{"heading", "heading"}, {"fly", "heading"}, {"flight", "heading"}, {"turn", "heading"},
	{"speed", "speed"}, {"reduce", "speed"}, {"slow", "speed"}, {"increase", "speed"}, {"knots", "speed"},
	{"descend", "descend"}, {"climb", "climb"}, {"altitude", "maintain"},
}

// findCommandTypeKeyword scans tokens backwards for the most recent
// command-type indicator, used when "correction <number>" needs to inherit
// the command type of the corrected portion.
func findCommandTypeKeyword(tokens []Token) string {
	maintain := ""
	for i := len(tokens) - 1; i >= 0; i-- {
		w := strings.ToLower(tokens[i].Text)
		for _, k := range correctionTypeKeywords {
			if WordScore(w, k.word) >= 0.85 {
				return k.keyword
			}
		}
		// "maintain" alone is ambiguous between speed and altitude:
		// following speed words decide; otherwise assume altitude but keep
		// scanning for an explicit descend/climb further back.
		if maintain == "" && WordScore(w, "maintain") >= 0.85 {
			maintain = "maintain"
			for j := i + 1; j < len(tokens); j++ {
				next := strings.ToLower(tokens[j].Text)
				if next == "speed" || next == "knots" {
					return "speed"
				}
			}
		}
	}
	return maintain
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

// stripAltimeterSuffix removes altimeter settings from the token stream
// wherever they appear. Controllers often include "(airport) altimeter
// (4 digits)" as informational; it is not an actionable command.
//
// An altimeter reading is 4 digits. We walk forward from "altimeter" until
// 4 digits have been consumed: a TokenNumber contributes its digit count
// (so "30" counts as 2), any other token contributes 1 (STT garbles like
// "right" for "niner" are absorbed). A "point" token after exactly 2 digits
// have been consumed is skipped without contributing, so readings spoken as
// "three zero point one four" are eaten cleanly. The span is stripped only
// when at least 2 number tokens were seen — otherwise we assume "altimeter"
// was a false positive and leave the stream alone.
func stripAltimeterSuffix(tokens []Token) []Token {
	result := make([]Token, 0, len(tokens))
	i := 0
	for i < len(tokens) {
		if strings.ToLower(tokens[i].Text) != "altimeter" {
			result = append(result, tokens[i])
			i++
			continue
		}
		end := i + 1
		digits := 0
		numCount := 0
		for end < len(tokens) && digits < 4 {
			if digits == 2 && strings.ToLower(tokens[end].Text) == "point" {
				end++
				continue
			}
			if tokens[end].Type == TokenNumber {
				numCount++
				digits += len(tokens[end].Text)
			} else {
				digits++
			}
			end++
		}
		if numCount < 2 {
			result = append(result, tokens[i])
			i++
			continue
		}
		if len(result) > 0 && result[len(result)-1].Type == TokenWord &&
			!IsCommandKeyword(strings.ToLower(result[len(result)-1].Text)) {
			result = result[:len(result)-1]
		}
		logLocalStt("stripped altimeter reading: %d tokens", end-i)
		i = end
	}
	return result
}

// logging helpers

func (p *Transcriber) logInfo(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Infof(format, args...)
	}
}
