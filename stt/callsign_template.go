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
//   - word            - Match literal keyword (fuzzy matched)
//   - word1|word2     - Match keyword alternatives
//   - [word]          - Match optional literal keyword
//   - [word {type}]   - Match optional group with typed param
func parseCallsignTemplate(template string) ([]callsignMatcher, error) {
	elements, err := parseTemplateElements(template)
	if err != nil {
		return nil, err
	}
	return elementsToCallsignMatchers(elements)
}

// elementsToCallsignMatchers converts template elements to callsign matchers.
func elementsToCallsignMatchers(elements []templateElement) ([]callsignMatcher, error) {
	var matchers []callsignMatcher
	for _, elem := range elements {
		switch elem.Kind {
		case elementTyped:
			m, err := createCallsignMatcher(elem.TypeSpec)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, m)
		case elementLiteral:
			matchers = append(matchers, &callsignLiteralMatcher{keywords: elem.Keywords})
		case elementOptionalLiteral:
			matchers = append(matchers, &callsignOptionalLiteralMatcher{keywords: elem.Keywords})
		case elementOptionalGroup:
			inner, err := elementsToCallsignMatchers(elem.Inner)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, &callsignOptionalGroupMatcher{matchers: inner})
		}
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
