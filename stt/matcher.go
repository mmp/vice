package stt

import (
	"reflect"
	"strings"
)

// matchResult represents the result of a matcher's attempt to match tokens.
type matchResult struct {
	value    any    // Extracted value (for typed matchers)
	consumed int    // Number of tokens consumed
	sayAgain string // SAYAGAIN type if partial match (e.g., "ALTITUDE")
}

// matcher is the interface for template matchers.
type matcher interface {
	// match attempts to match tokens starting at pos.
	// Returns the result with consumed > 0 on success, or consumed = 0 on failure.
	// allowSlack indicates whether the matcher can skip unrecognized tokens.
	match(tokens []Token, pos int, ac Aircraft, skipWords []string, allowSlack bool) matchResult

	// goType returns the Go type this matcher extracts (nil for non-typed matchers).
	goType() reflect.Type

	// isOptional returns true if this matcher is optional (can consume 0 tokens).
	isOptional() bool
}

// literalMatcher matches one or more keyword alternatives with fuzzy matching.
type literalMatcher struct {
	keywords []string // Alternative keywords (e.g., ["descend", "descended"])
}

func (m *literalMatcher) match(tokens []Token, pos int, ac Aircraft, skipWords []string, allowSlack bool) matchResult {
	if pos >= len(tokens) {
		return matchResult{}
	}

	// Skip filler words
	for pos < len(tokens) {
		text := strings.ToLower(tokens[pos].Text)
		if IsFillerWord(text) {
			pos++
			continue
		}
		break
	}

	if pos >= len(tokens) {
		return matchResult{}
	}

	// Try to match against any keyword at the current position
	text := strings.ToLower(tokens[pos].Text)

	// If the next token is a TokenAltitude (high-confidence altitude from "thousand" pattern),
	// require a higher fuzzy match threshold. This prevents weak matches like "claimed"→"climbed"
	// from consuming altitude tokens that should be handled by standalone_altitude.
	threshold := 0.80
	if pos+1 < len(tokens) && tokens[pos+1].Type == TokenAltitude {
		threshold = 0.95
	}

	for _, kw := range m.keywords {
		if FuzzyMatch(text, kw, threshold) {
			return matchResult{consumed: pos + 1}
		}
	}

	// Slack mechanism: try skipping up to 3 unrecognized tokens (STT garbage)
	// This handles cases where garbage words appear between clear keywords.
	// Only enabled for internal matchers (not the first keyword in a template).
	if allowSlack {
		maxSlack := 3
		for slack := 1; slack <= maxSlack && pos+slack < len(tokens); slack++ {
			checkPos := pos + slack
			checkText := strings.ToLower(tokens[checkPos].Text)

			// Skip filler words in slack region
			if IsFillerWord(checkText) {
				continue
			}

			// Stop at command boundary keywords - these indicate a new command context.
			// For example, in "left approach speed 180", when searching for a heading
			// after "left", we should stop at "speed" because it starts a new command.
			// Exception: if this keyword is what we're looking for, try to match it.
			isKeyword := IsCommandKeyword(checkText)
			if isKeyword {
				// Check if this keyword is one of our alternatives
				isOurKeyword := false
				for _, kw := range m.keywords {
					if FuzzyMatch(checkText, kw, 0.8) {
						isOurKeyword = true
						break
					}
				}
				if !isOurKeyword {
					// Hit a different command boundary - stop slack search
					break
				}
			}

			// Try to match at this position
			for _, kw := range m.keywords {
				if FuzzyMatch(checkText, kw, 0.8) {
					return matchResult{consumed: checkPos + 1}
				}
			}
		}
	}

	return matchResult{}
}

func (m *literalMatcher) goType() reflect.Type {
	return nil
}

func (m *literalMatcher) isOptional() bool {
	return false
}

// typedMatcher wraps a typeParser for value extraction.
type typedMatcher struct {
	parser typeParser
	inner  matcher // For optional wrappers, stores the original
}

func (m *typedMatcher) match(tokens []Token, pos int, ac Aircraft, skipWords []string, allowSlack bool) matchResult {
	if pos >= len(tokens) {
		return matchResult{}
	}

	// Skip only filler words (not skipWords - those are for optional sections)
	for pos < len(tokens) {
		text := strings.ToLower(tokens[pos].Text)
		if IsFillerWord(text) {
			pos++
			continue
		}
		break
	}

	if pos >= len(tokens) {
		return matchResult{}
	}

	value, consumed, sayAgain := m.parser.parse(tokens, pos, ac)
	if consumed == 0 {
		return matchResult{sayAgain: sayAgain}
	}

	return matchResult{
		value:    value,
		consumed: pos + consumed,
	}
}

func (m *typedMatcher) goType() reflect.Type {
	return m.parser.goType()
}

func (m *typedMatcher) isOptional() bool {
	return false
}

// optionalGroupMatcher wraps a sequence of matchers that are all optional.
// If the group matches, all values are extracted; if not, all become nil.
type optionalGroupMatcher struct {
	matchers []matcher
}

func (m *optionalGroupMatcher) match(tokens []Token, pos int, ac Aircraft, skipWords []string, allowSlack bool) matchResult {
	var values []any

	// Try to match all matchers in sequence
	for i, inner := range m.matchers {
		// Allow slack for non-first matchers within the optional group
		innerSlack := allowSlack && i > 0
		res := inner.match(tokens, pos, ac, skipWords, innerSlack)
		if res.consumed == 0 {
			// Group failed - return no match but don't fail the overall parse
			return matchResult{consumed: 0}
		}
		values = append(values, res.value)
		pos = res.consumed
	}

	// All matchers succeeded
	return matchResult{
		value:    values,
		consumed: pos,
	}
}

func (m *optionalGroupMatcher) goType() reflect.Type {
	// Return a slice type that holds all inner types
	return nil
}

func (m *optionalGroupMatcher) isOptional() bool {
	return true
}
