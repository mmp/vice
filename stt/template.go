package stt

import (
	"fmt"
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
// Runs of adjacent literal words (required and optional) are grouped into a
// single phraseMatcher so they can be matched — and scored — as a phrase.
func elementsToCommandMatchers(elements []templateElement) ([]matcher, error) {
	var matchers []matcher
	var phrase []phraseWord
	flushPhrase := func() {
		if len(phrase) > 0 {
			matchers = append(matchers, &phraseMatcher{words: phrase})
			phrase = nil
		}
	}

	for _, elem := range elements {
		switch elem.Kind {
		case elementLiteral:
			phrase = append(phrase, phraseWord{keywords: elem.Keywords})
		case elementOptionalLiteral:
			phrase = append(phrase, phraseWord{keywords: elem.Keywords, optional: true})
		case elementTyped:
			flushPhrase()
			parser := getTypeParser(elem.TypeSpec)
			if parser == nil {
				return nil, fmt.Errorf("unknown type: %s", elem.TypeSpec)
			}
			matchers = append(matchers, &slotMatcher{parser: parser})
		case elementOptionalGroup:
			flushPhrase()
			inner, err := elementsToCommandMatchers(elem.Inner)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, &optionalGroupMatcher{matchers: inner})
		}
	}
	flushPhrase()
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
