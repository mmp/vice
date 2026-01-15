package sttlocal

import (
	"strings"
	"time"

	"github.com/mmp/vice/log"
)

// LocalSTTProvider implements STTTranscriptProvider using local algorithmic parsing.
// It replaces the LLM-based approach with fast fuzzy matching.
type LocalSTTProvider struct {
	lg *log.Logger
}

// NewLocalSTTProvider creates a new local STT provider.
func NewLocalSTTProvider(lg *log.Logger) *LocalSTTProvider {
	return &LocalSTTProvider{lg: lg}
}

// DecodeTranscript converts a speech transcript to aircraft control commands.
// It returns one of:
//   - "{CALLSIGN} {CMD1} {CMD2} ..." for successful parsing
//   - "{CALLSIGN} AGAIN" if callsign identified but commands unclear
//   - "BLOCKED" if no callsign could be identified
//   - "" if transcript is empty
func (p *LocalSTTProvider) DecodeTranscript(
	aircraft map[string]STTAircraft,
	transcript string,
	whisperDuration time.Duration,
	numCores int,
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
	var ac STTAircraft
	if callsignMatch.SpokenKey != "" {
		ac = aircraft[callsignMatch.SpokenKey]
	}
	logLocalStt("aircraft context: State=%q Altitude=%d Fixes=%d Approaches=%d",
		ac.State, ac.Altitude, len(ac.Fixes), len(ac.CandidateApproaches))
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
	var output string
	if len(validation.ValidCommands) == 0 {
		if confidence >= 0.4 {
			// We're confident about the callsign but couldn't parse commands
			output = callsignMatch.Callsign + " AGAIN"
		} else {
			// Low confidence overall
			output = "BLOCKED"
		}
	} else {
		output = callsignMatch.Callsign + " " + strings.Join(validation.ValidCommands, " ")
	}

	elapsed := time.Since(start)
	logLocalStt("=== DecodeTranscript END: %q (conf=%.2f, time=%s) ===", output, confidence, elapsed)
	p.logInfo("local STT: %q -> %q (conf=%.2f, time=%s)",
		transcript, output, confidence, elapsed)

	return strings.TrimSpace(output), nil
}

// GetUsageStats returns usage statistics for this provider.
func (p *LocalSTTProvider) GetUsageStats() string {
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
func (p *LocalSTTProvider) ParseTranscriptDetailed(
	aircraft map[string]STTAircraft,
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
	var ac STTAircraft
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

// logging helpers

func (p *LocalSTTProvider) logDebug(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Debugf(format, args...)
	}
}

func (p *LocalSTTProvider) logInfo(format string, args ...interface{}) {
	if p.lg != nil {
		p.lg.Infof(format, args...)
	}
}
