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

type TextWrapConfig struct {
	ColumnLimit int
	Indent      int
	WrapAll     bool
	WrapNoSpace bool
}

func (cfg TextWrapConfig) Wrap(s string) (string, int) {
	if cfg.ColumnLimit <= 0 {
		return s, strings.Count(s, "\n") + 1
	}

	var result strings.Builder
	lines := 1

	// Buffer for the current (not-yet-emitted) line segment
	var currentLine []rune
	lastSpaceIndex := -1 // index of last space in currentLine; -1 means none

	isContinuation := false // true if current physical line is a wrapped continuation
	preformatted := false   // true if current input line should bypass wrapping

	// Helper to compute capacity for the current physical line
	capacityForLine := func() int {
		if isContinuation {
			cap := cfg.ColumnLimit - cfg.Indent
			if cap <= 0 {
				return 1 // ensure forward progress
			}
			return cap
		}
		return cfg.ColumnLimit
	}

	// Helper to write indent for continuation lines
	writeIndent := func() {
		for i := 0; i < cfg.Indent; i++ {
			result.WriteRune(' ')
		}
	}

	// Helper to recompute lastSpaceIndex after slicing currentLine
	recomputeLastSpace := func() {
		lastSpaceIndex = -1
		for i := len(currentLine) - 1; i >= 0; i-- {
			if currentLine[i] == ' ' {
				lastSpaceIndex = i
				break
			}
		}
	}

	for _, ch := range s {
		// Detect preformatted input lines (those that begin with a space) unless WrapAll
		if len(currentLine) == 0 && !isContinuation {
			preformatted = !cfg.WrapAll && ch == ' '
		}

		if preformatted {
			// Pass through until input newline
			result.WriteRune(ch)
			if ch == '\n' {
				lines++
				isContinuation = false
				preformatted = false
			}
			continue
		}

		currentLine = append(currentLine, ch)
		if ch == ' ' {
			lastSpaceIndex = len(currentLine) - 1
		}

		// If an input newline is present in the buffer, flush the whole buffer
		if ch == '\n' {
			result.WriteString(string(currentLine))
			currentLine = currentLine[:0]
			lastSpaceIndex = -1
			lines++
			isContinuation = false
			continue
		}

		// Wrap while currentLine exceeds capacity
		for cap := capacityForLine(); len(currentLine) > cap; cap = capacityForLine() {
			// If we are not allowed to break mid-word and there is no space, allow overflow until space/newline
			if !cfg.WrapNoSpace && lastSpaceIndex == -1 {
				break
			}

			breakPos := cap
			if !cfg.WrapNoSpace && lastSpaceIndex >= 0 {
				// Prefer wrapping at last space when allowed
				breakPos = lastSpaceIndex + 1
			}

			// Emit up to breakPos, then newline + indent
			result.WriteString(string(currentLine[:breakPos]))
			result.WriteRune('\n')
			lines++
			writeIndent()

			// Remainder stays in currentLine; recompute space index
			currentLine = currentLine[breakPos:]
			isContinuation = true
			recomputeLastSpace()
		}
	}

	if len(currentLine) > 0 {
		result.WriteString(string(currentLine))
	}

	return result.String(), lines
}

func WrapText(s string, columnLimit int, indent int, wrapAll bool, noSpace bool) (string, int) {
	cfg := TextWrapConfig{
		ColumnLimit: columnLimit,
		Indent:      indent,
		WrapAll:     wrapAll,
		WrapNoSpace: noSpace,
	}
	return cfg.Wrap(s)
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
