package stt

import (
	"fmt"
	"strings"
)

// templateElementKind identifies the type of template element.
type templateElementKind int

const (
	elementLiteral         templateElementKind = iota // word or word|alt
	elementTyped                                      // {type} or {type:param}
	elementOptionalGroup                              // [content with {type}]
	elementOptionalLiteral                            // [word] or [word|alt]
)

// templateElement represents a parsed element from a template string.
type templateElement struct {
	Kind     templateElementKind
	Keywords []string          // for literal/optionalLiteral (includes alternatives)
	TypeSpec string            // for typed: "altitude", "skip:3", etc.
	Inner    []templateElement // for optionalGroup
}

// parseTemplateElements parses a template string into abstract elements.
// Supports: {type}, {type:param}, [optional], word, word|alt, [word {type}]
func parseTemplateElements(template string) ([]templateElement, error) {
	var elements []templateElement
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

			// Check if this contains a typed parameter
			if strings.Contains(inner, "{") {
				// Parse inner content recursively
				innerElements, err := parseTemplateElements(inner)
				if err != nil {
					return nil, fmt.Errorf("error parsing optional section: %w", err)
				}
				elements = append(elements, templateElement{
					Kind:  elementOptionalGroup,
					Inner: innerElements,
				})
			} else {
				// Just optional literal words
				words := strings.Fields(inner)
				for _, word := range words {
					var keywords []string
					if strings.Contains(word, "|") {
						keywords = strings.Split(word, "|")
					} else {
						keywords = []string{word}
					}
					elements = append(elements, templateElement{
						Kind:     elementOptionalLiteral,
						Keywords: keywords,
					})
				}
			}
			pos = end + 1

		case '{':
			// Typed parameter
			end := strings.IndexByte(template[pos:], '}')
			if end == -1 {
				return nil, fmt.Errorf("unmatched '{' at position %d", pos)
			}
			typeSpec := template[pos+1 : pos+end]
			elements = append(elements, templateElement{
				Kind:     elementTyped,
				TypeSpec: typeSpec,
			})
			pos = pos + end + 1

		default:
			// Literal word or alternatives
			end := pos
			for end < len(template) && template[end] != ' ' && template[end] != '[' && template[end] != '{' {
				end++
			}
			word := template[pos:end]

			var keywords []string
			if strings.Contains(word, "|") {
				keywords = strings.Split(word, "|")
			} else {
				keywords = []string{word}
			}
			elements = append(elements, templateElement{
				Kind:     elementLiteral,
				Keywords: keywords,
			})
			pos = end
		}
	}

	return elements, nil
}

// parseTypeSpec extracts type name and parameter from "type:param" or "type".
func parseTypeSpec(spec string) (typeName, param string) {
	if idx := strings.Index(spec, ":"); idx > 0 {
		return spec[:idx], spec[idx+1:]
	}
	return spec, ""
}

// generatePatternName creates a debug name from a template string.
// Extracts the first few significant words/types for a human-readable name.
func generatePatternName(template string) string {
	elements, err := parseTemplateElements(template)
	if err != nil {
		return "unnamed"
	}

	var parts []string
	for _, elem := range elements {
		switch elem.Kind {
		case elementLiteral:
			// Take first alternative
			if len(elem.Keywords) > 0 {
				parts = append(parts, elem.Keywords[0])
			}
		case elementTyped:
			// Extract type name without parameter
			typeName, _ := parseTypeSpec(elem.TypeSpec)
			parts = append(parts, typeName)
		case elementOptionalGroup, elementOptionalLiteral:
			// Skip optional elements in name generation
			continue
		}
		if len(parts) >= 3 {
			break
		}
	}

	if len(parts) == 0 {
		return "unnamed"
	}
	return strings.Join(parts, "_")
}
