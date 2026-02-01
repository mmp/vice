package stt

import (
	"fmt"
	"reflect"
	"strings"
)

// parseTemplate parses a template string into a sequence of matchers.
//
// Template syntax:
//   - `word` - Literal keyword (fuzzy matched)
//   - `word1|word2` - Keyword alternatives
//   - `[words]` - Optional literal words
//   - `{type}` - Typed parameter
//   - `[word {type}]` - Optional section with typed param
func parseTemplate(template string) ([]matcher, error) {
	elements, err := parseTemplateElements(template)
	if err != nil {
		return nil, err
	}
	return elementsToCommandMatchers(elements)
}

// elementsToCommandMatchers converts template elements to command matchers.
func elementsToCommandMatchers(elements []templateElement) ([]matcher, error) {
	var matchers []matcher
	for _, elem := range elements {
		switch elem.Kind {
		case elementLiteral:
			matchers = append(matchers, &literalMatcher{keywords: elem.Keywords})
		case elementTyped:
			parser := getTypeParser(elem.TypeSpec)
			if parser == nil {
				return nil, fmt.Errorf("unknown type: %s", elem.TypeSpec)
			}
			matchers = append(matchers, &typedMatcher{parser: parser})
		case elementOptionalLiteral:
			matchers = append(matchers, &optionalLiteralMatcher{keywords: elem.Keywords})
		case elementOptionalGroup:
			inner, err := elementsToCommandMatchers(elem.Inner)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, &optionalGroupMatcher{matchers: inner})
		}
	}
	return matchers, nil
}

// findMatchingBracket finds the matching closing bracket.
func findMatchingBracket(s string, start int, open, close byte) int {
	if start >= len(s) || s[start] != open {
		return -1
	}

	depth := 1
	for i := start + 1; i < len(s); i++ {
		if s[i] == open {
			depth++
		} else if s[i] == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// optionalLiteralMatcher matches optional literal words.
type optionalLiteralMatcher struct {
	keywords []string
}

func (m *optionalLiteralMatcher) match(tokens []Token, pos int, ac Aircraft, skipWords []string, allowSlack bool) matchResult {
	if pos >= len(tokens) {
		return matchResult{consumed: pos} // Optional, so success with 0 additional tokens
	}

	text := strings.ToLower(tokens[pos].Text)

	// Try to match against any keyword
	for _, kw := range m.keywords {
		// For short keywords (2 chars or less like "to", "at"), require exact match
		// to prevent false positives like "torch" matching "to".
		if len(kw) <= 2 {
			if text == kw {
				return matchResult{consumed: pos + 1}
			}
			continue
		}

		// For longer keywords, use fuzzy matching but check length ratio.
		// Don't match if lengths differ by more than 2x (e.g., "torch" vs "direct").
		textLen := len(text)
		kwLen := len(kw)
		if textLen > 2*kwLen || kwLen > 2*textLen {
			continue
		}

		if FuzzyMatch(text, kw, 0.8) {
			return matchResult{consumed: pos + 1}
		}
	}

	// Didn't match, but that's ok - it's optional
	return matchResult{consumed: pos}
}

func (m *optionalLiteralMatcher) goType() reflect.Type {
	return nil
}

func (m *optionalLiteralMatcher) isOptional() bool {
	return true
}

// extractSkipWords extracts skip words from the template.
// These are words in [] that should be skipped during matching.
func extractSkipWords(template string) []string {
	var skipWords []string
	pos := 0
	for pos < len(template) {
		if template[pos] == '[' {
			end := findMatchingBracket(template, pos, '[', ']')
			if end != -1 {
				inner := template[pos+1 : end]
				// If no typed params, these are skip words
				if !strings.Contains(inner, "{") {
					words := strings.Fields(inner)
					skipWords = append(skipWords, words...)
				}
			}
		}
		pos++
	}
	return skipWords
}
