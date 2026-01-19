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
//   - "BLOCKED" if no callsign could be identified
//   - "" if transcript is empty or only contains position identification
//
// controllerRadioName is the user's controller radio name (e.g., "New York Departure")
// used to detect position identification phrases. Pass empty string if not available.
func (p *Transcriber) DecodeTranscript(
	aircraft map[string]Aircraft,
	transcript string,
	controllerRadioName string,
) (string, error) {
	start := time.Now()

	logLocalStt("=== DecodeTranscript START ===")
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

	// Layer 3: Callsign matching
	callsignMatch, remainingTokens := MatchCallsign(tokens, aircraft)
	logLocalStt("callsign match: Callsign=%q SpokenKey=%q Conf=%.2f Consumed=%d",
		callsignMatch.Callsign, callsignMatch.SpokenKey, callsignMatch.Confidence, callsignMatch.Consumed)
	if callsignMatch.Callsign == "" {
		// No callsign identified
		if len(tokens) > 2 {
			// Had some content but couldn't match callsign
			logLocalStt("BLOCKED: no callsign match for %q", transcript)
			p.logDebug("BLOCKED: no callsign match for %q", transcript)
			return "BLOCKED", nil
		}
		// Very short/empty - treat as no speech
		logLocalStt("too few tokens without callsign, returning \"\"")
		return "", nil
	}

	// Get aircraft context for the matched callsign
	var ac Aircraft
	if callsignMatch.SpokenKey != "" {
		ac = aircraft[callsignMatch.SpokenKey]
	}
	logLocalStt("aircraft context: State=%q Altitude=%d Fixes=%d Approaches=%d AssignedApproach=%q",
		ac.State, ac.Altitude, len(ac.Fixes), len(ac.CandidateApproaches), ac.AssignedApproach)
	for spokenName, fixID := range ac.Fixes {
		logLocalStt("  fix: %q -> %q", spokenName, fixID)
	}
	for spokenName, apprID := range ac.CandidateApproaches {
		logLocalStt("  approach: %q -> %q", spokenName, apprID)
	}

	// Handle "disregard" or "correction" in remaining tokens
	// This discards previous command attempts but preserves callsign
	remainingTokens = applyDisregard(remainingTokens)
	logLocalStt("remaining tokens after disregard: %d", len(remainingTokens))
	for i, t := range remainingTokens {
		logLocalStt("  remaining[%d]: Text=%q Type=%d Value=%d", i, t.Text, t.Type, t.Value)
	}

	// Check for position identification phrases (e.g., "New York departure", "Kennedy approach")
	// These are informational only and should return empty (no command needed)
	if isPositionIdentification(remainingTokens, controllerRadioName) {
		logLocalStt("detected position identification, returning empty")
		elapsed := time.Since(start)
		logLocalStt("=== DecodeTranscript END: \"\" (position ID, time=%s) ===", elapsed)
		p.logInfo("local STT: %q -> \"\" (position identification, time=%s)", transcript, elapsed)
		return "", nil
	}

	// Layer 4: Command parsing
	commands, cmdConf := ParseCommands(remainingTokens, ac)
	logLocalStt("parsed commands: %v (conf=%.2f)", commands, cmdConf)

	// Layer 5: Validation
	validation := ValidateCommands(commands, ac)
	logLocalStt("validated commands: %v (conf=%.2f)", validation.ValidCommands, validation.Confidence)
	if len(validation.Errors) > 0 {
		logLocalStt("validation errors: %v", validation.Errors)
	}

	// Compute overall confidence
	confidence := callsignMatch.Confidence
	if len(commands) > 0 {
		confidence *= cmdConf * validation.Confidence
	}

	// Generate output
	// Encode addressing form in callsign: append /T for type-based addressing (GA aircraft)
	callsignWithForm := callsignMatch.Callsign
	if callsignMatch.AddressingForm == sim.AddressingFormTypeTrailing3 {
		callsignWithForm += "/T"
	}

	var output string
	if len(validation.ValidCommands) == 0 {
		if confidence >= 0.4 {
			// We're confident about the callsign but couldn't parse commands
			output = callsignWithForm + " AGAIN"
		} else {
			// Low confidence overall
			output = "BLOCKED"
		}
	} else {
		output = callsignWithForm + " " + strings.Join(validation.ValidCommands, " ")
	}

	elapsed := time.Since(start)
	logLocalStt("=== DecodeTranscript END: %q (conf=%.2f, time=%s) ===", output, confidence, elapsed)
	p.logInfo("local STT: %q -> %q (conf=%.2f, time=%s)",
		transcript, output, confidence, elapsed)

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
			sttAc.Fixes[av.GetFixTelephony(fix)] = fix
		}

		// Determine state and set SID/STAR
		if trk.IsDeparture() {
			sttAc.State = "departure"
			sttAc.SID = trk.SID
		} else if trk.IsArrival() {
			if trk.OnApproach {
				sttAc.State = "on approach"
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
			if trk.Approach != "" {
				sttAc.AssignedApproach = trk.Approach
			}
			// Add active approaches for the arrival airport
			for _, ar := range state.ArrivalRunways {
				if ar.Airport != trk.ArrivalAirport {
					continue
				}
				if ap, ok := state.Airports[ar.Airport]; ok {
					for code, appr := range ap.Approaches {
						if appr.Runway == ar.Runway {
							sttAc.CandidateApproaches[av.GetApproachTelephony(appr.FullName)] = code
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
		telephony := av.GetTelephony(string(trk.ADSBCallsign), cwt)

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

// applyDisregard handles "disregard" or "correction" in tokens.
// Discards everything before the last disregard keyword.
func applyDisregard(tokens []Token) []Token {
	for i := len(tokens) - 1; i >= 0; i-- {
		text := strings.ToLower(tokens[i].Text)
		if text == "disregard" || text == "correction" {
			return tokens[i+1:]
		}
	}
	return tokens
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

func (p *Transcriber) logDebug(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Debugf(format, args...)
	}
}

func (p *Transcriber) logInfo(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Infof(format, args...)
	}
}
