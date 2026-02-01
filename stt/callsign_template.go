package stt

import (
	"fmt"
	"strconv"
	"strings"
)

// parseCallsignTemplate parses a callsign template string into matchers.
//
// Template syntax:
//   - {skip:N}        - Skip up to N garbage tokens at start
//   - {airline}       - Match airline telephony name (fuzzy, multi-word)
//   - {flight}        - Match flight number digits (for airline candidates)
//   - {flight_only}   - Match flight number against all aircraft (standalone)
//   - {exact_phrase}  - Match entire phrase exactly against aircraft map
//   - {suffix_phrase} - Match phrase as suffix of GA callsign
func parseCallsignTemplate(template string) ([]callsignMatcher, error) {
	var matchers []callsignMatcher
	pos := 0
	template = strings.TrimSpace(template)

	for pos < len(template) {
		// Skip whitespace
		for pos < len(template) && template[pos] == ' ' {
			pos++
		}
		if pos >= len(template) {
			break
		}

		if template[pos] != '{' {
			return nil, fmt.Errorf("expected '{' at position %d, got %q", pos, template[pos])
		}

		// Find matching }
		end := strings.IndexByte(template[pos:], '}')
		if end == -1 {
			return nil, fmt.Errorf("unmatched '{' at position %d", pos)
		}
		typeSpec := template[pos+1 : pos+end]

		matcher, err := createCallsignMatcher(typeSpec)
		if err != nil {
			return nil, fmt.Errorf("error creating matcher for %q: %w", typeSpec, err)
		}
		matchers = append(matchers, matcher)
		pos = pos + end + 1
	}

	return matchers, nil
}

// createCallsignMatcher creates a matcher from a type specification.
func createCallsignMatcher(typeSpec string) (callsignMatcher, error) {
	// Handle skip:N format
	if strings.HasPrefix(typeSpec, "skip:") {
		nStr := typeSpec[5:]
		n, err := strconv.Atoi(nStr)
		if err != nil {
			return nil, fmt.Errorf("invalid skip count %q: %w", nStr, err)
		}
		return &skipMatcher{maxSkip: n}, nil
	}

	switch typeSpec {
	case "airline":
		return &airlineMatcher{}, nil
	case "flight":
		return &flightMatcher{}, nil
	case "flight_only":
		return &flightOnlyMatcher{}, nil
	case "exact_phrase":
		return &exactPhraseMatcher{}, nil
	case "suffix_phrase":
		return &suffixPhraseMatcher{}, nil
	case "ga_november":
		return &gaNovemberMatcher{}, nil
	default:
		return nil, fmt.Errorf("unknown callsign matcher type: %s", typeSpec)
	}
}
