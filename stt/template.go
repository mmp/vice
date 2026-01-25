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
	var matchers []matcher
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

		switch template[pos] {
		case '[':
			// Optional section
			end := findMatchingBracket(template, pos, '[', ']')
			if end == -1 {
				return nil, fmt.Errorf("unmatched '[' at position %d", pos)
			}
			inner := template[pos+1 : end]
			innerMatchers, err := parseOptionalSection(inner)
			if err != nil {
				return nil, fmt.Errorf("error parsing optional section: %w", err)
			}
			matchers = append(matchers, innerMatchers...)
			pos = end + 1

		case '{':
			// Typed parameter
			end := strings.IndexByte(template[pos:], '}')
			if end == -1 {
				return nil, fmt.Errorf("unmatched '{' at position %d", pos)
			}
			typeID := template[pos+1 : pos+end]
			parser := getTypeParser(typeID)
			if parser == nil {
				return nil, fmt.Errorf("unknown type: %s", typeID)
			}
			matchers = append(matchers, &typedMatcher{parser: parser})
			pos = pos + end + 1

		default:
			// Literal word or alternatives
			end := pos
			for end < len(template) && template[end] != ' ' && template[end] != '[' && template[end] != '{' {
				end++
			}
			word := template[pos:end]

			// Check for alternatives
			if strings.Contains(word, "|") {
				alternatives := strings.Split(word, "|")
				matchers = append(matchers, &literalMatcher{keywords: alternatives})
			} else {
				matchers = append(matchers, &literalMatcher{keywords: []string{word}})
			}
			pos = end
		}
	}

	return matchers, nil
}

// parseOptionalSection parses the content inside [...].
func parseOptionalSection(content string) ([]matcher, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}

	// Check if this contains a typed parameter
	if strings.Contains(content, "{") {
		// Parse inner content
		innerMatchers, err := parseTemplate(content)
		if err != nil {
			return nil, err
		}
		// Wrap in optional group
		return []matcher{&optionalGroupMatcher{matchers: innerMatchers}}, nil
	}

	// Just optional literal words
	words := strings.Fields(content)
	var matchers []matcher
	for _, word := range words {
		if strings.Contains(word, "|") {
			alternatives := strings.Split(word, "|")
			matchers = append(matchers, &optionalLiteralMatcher{keywords: alternatives})
		} else {
			matchers = append(matchers, &optionalLiteralMatcher{keywords: []string{word}})
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
