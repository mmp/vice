// pkg/util/text.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"crypto/sha256"
	"errors"
	"hash/fnv"
	"io"
	"iter"
	"strconv"
	"strings"
	"unicode"
)

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

func WrapTextNoSpace(s string, columnLimit int, indent int, wrapAll bool) (string, int) {
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
			// reset column to reflect indent + what’s in accum
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

		// NEW: mid-word wrap when wrapAll==true
		if wrapAll && column > columnLimit {
			wrapLine = true
			flush()
		}

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

func HashString64(s string) uint64 {
	hash := fnv.New64a()
	io.Copy(hash, strings.NewReader(s))
	return hash.Sum64()
}

// Given a string iterator and a base string, return two arrays of strings
// from the iterator that are respectively within one or two edits of the
// base string. // https://en.wikipedia.org/wiki/Levenshtein_distance
func SelectInTwoEdits(str string, seq iter.Seq[string], dist1, dist2 []string) ([]string, []string) {
	min := func(a, b int) int {
		if a < b {
			return a
		}
		return b
	}
	max := func(a, b int) int {
		if a > b {
			return a
		}
		return b
	}

	var cur, prev []int
	n := len(str)
	for str2 := range seq {
		if str == str2 {
			continue
		}

		n2 := len(str2)
		nmax := max(n, n2)

		if nmax >= len(cur) {
			cur = make([]int, nmax+1)
			prev = make([]int, nmax+1)
		}

		for i := range n2 + 1 {
			prev[i] = i
		}

		for y := 1; y <= n; y++ {
			cur[0] = y
			rowBest := y

			for x := 1; x <= n2; x++ {
				cost := 0
				if str[y-1] != str2[x-1] {
					cost = 1
				}

				cur[x] = min(prev[x-1]+cost, min(cur[x-1], prev[x])+1)

				if cur[x] < rowBest {
					rowBest = cur[x]
				}
			}

			if rowBest > 2 {
				continue
			}
			// Swap cur and prev
			cur, prev = prev, cur
		}

		if prev[n2] == 1 {
			dist1 = append(dist1, str2)
		} else if prev[n2] == 2 {
			dist2 = append(dist2, str2)
		}
	}
	return dist1, dist2
}
