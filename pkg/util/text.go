// pkg/util/text.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"crypto/sha256"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
)

///////////////////////////////////////////////////////////////////////////
// decompression

// WrapText wraps the provided text string to the given column limit, returning the
// wrapped string and the number of lines it became.  indent gives the amount to
// indent wrapped lines.  By default, lines that start with a space are assumed to be
// preformatted and are not wrapped; providing a true value for wrapAll overrides
// that behavior and causes them to be wrapped as well.
func WrapText(s string, columnLimit int, indent int, wrapAll bool) (string, int) {
	var accum, result strings.Builder

	var wrapLine bool
	column := 0
	lines := 1

	flush := func() {
		if wrapLine && column > columnLimit {
			result.WriteRune('\n')
			lines++
			for i := 0; i < indent; i++ {
				result.WriteRune(' ')
			}
			column = indent + accum.Len()
		}
		result.WriteString(accum.String())
		accum.Reset()
	}

	for _, ch := range s {
		// If wrapAll isn't enabled, then if the line starts with a space,
		// assume it is preformatted and pass it through unchanged.
		if column == 0 {
			wrapLine = wrapAll || ch != ' '
		}

		accum.WriteRune(ch)
		column++

		if ch == '\n' {
			flush()
			column = 0
			lines++
		} else if ch == ' ' {
			flush()
		}
	}

	flush()
	return result.String(), lines
}

// StopShouting turns text of the form "UNITED AIRLINES" to "United Airlines"
func StopShouting(orig string) string {
	var s strings.Builder
	wsLast := true
	for _, ch := range orig {
		if unicode.IsSpace(ch) {
			wsLast = true
		} else if unicode.IsLetter(ch) {
			if wsLast {
				// leave it alone
				wsLast = false
			} else {
				ch = unicode.ToLower(ch)
			}
		}

		// otherwise leave it alone

		s.WriteRune(ch)
	}
	return s.String()
}

// atof is a utility for parsing floating point values that sends errors to
// the logging system.
func Atof(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func IsAllNumbers(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func IsAllLetters(s string) bool {
	for _, runeValue := range s {
		if !unicode.IsLetter(runeValue) {
			return false
		}
	}
	return true
}

// Given a map from strings to some type T where the keys are assumed to be
// of the form "foo,bar,bat", return a new map where each comma-delineated
// string in the keys has its own entry in the returned map.  Returns an
// error if a key is repeated.
func CommaKeyExpand[T any](in map[string]T) (map[string]T, error) {
	m := make(map[string]T)
	for k, v := range in {
		for _, s := range strings.Split(k, ",") {
			s = strings.TrimSpace(s)
			if _, ok := m[s]; ok {
				return nil, errors.New("key repeated in map " + s)
			}
			m[s] = v
		}
	}
	return m, nil
}

func Hash(r io.Reader) ([]byte, error) {
	hash := sha256.New()
	_, err := io.Copy(hash, r)
	if err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}
