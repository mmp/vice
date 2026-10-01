// util/text.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"iter"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type ByteCount int64

func (b ByteCount) String() string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	} else if b < 1024*1024 {
		return fmt.Sprintf("%d kB", b/1024)
	} else if b < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	} else {
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	}
}

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
	isContinuation := false // true if current physical line is a wrapped continuation
	preformatted := false   // true if current input line should bypass wrapping

	// Helper to compute capacity for the current physical line
	capacityForLine := func() int {
		if isContinuation {
			cap := cfg.ColumnLimit - cfg.Indent
			return max(1, cap)
		}
		return cfg.ColumnLimit
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

		// If an input newline is present in the buffer, flush the whole buffer
		if ch == '\n' {
			result.WriteString(string(currentLine))
			currentLine = currentLine[:0]
			lines++
			isContinuation = false
			continue
		}

		// Wrap while currentLine exceeds capacity
		for cap := capacityForLine(); len(currentLine) > cap; cap = capacityForLine() {
			lastBreakIndex := -1
			scanFrom := min(cap, len(currentLine)) - 1
			for i := scanFrom; i >= 0; i-- {
				if currentLine[i] == ' ' || currentLine[i] == '.' {
					lastBreakIndex = i
					break
				}
			}

			// If we are not allowed to break mid-word and there is no break in the first cap chars,
			// scan the rest of the buffer for the first break point
			if !cfg.WrapNoSpace && lastBreakIndex == -1 {
				for i := cap; i < len(currentLine); i++ {
					if currentLine[i] == ' ' || currentLine[i] == '.' {
						lastBreakIndex = i
						break
					}
				}
				if lastBreakIndex == -1 {
					break // still no break found, allow overflow until break/newline
				}
			}

			breakPos := cap
			if !cfg.WrapNoSpace && lastBreakIndex >= 0 {
				// Prefer wrapping at last break when allowed
				breakPos = min(lastBreakIndex+1, len(currentLine))
			}

			// Emit up to breakPos, then newline + indent
			result.WriteString(string(currentLine[:breakPos]))
			result.WriteRune('\n')
			lines++
			for range cfg.Indent {
				result.WriteRune(' ')
			}

			// Remainder stays in currentLine
			currentLine = currentLine[breakPos:]
			isContinuation = true
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
func CommaKeyExpand[S ~string, T any](in map[S]T) (map[S]T, error) {
	m := make(map[S]T)
	for k, v := range in {
		for s := range strings.SplitSeq(string(k), ",") {
			s = strings.TrimSpace(s)
			if _, ok := m[S(s)]; ok {
				return nil, errors.New("key repeated in map " + s)
			}
			m[S(s)] = v
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
// base string. Swapping two adjacent characters counts as a single edit,
// since that is a common typo.
// https://en.wikipedia.org/wiki/Damerau%E2%80%93Levenshtein_distance#Optimal_string_alignment_distance
func SelectInTwoEdits[S ~string](str string, seq iter.Seq[S], dist1, dist2 []string) ([]string, []string) {
	var prev2, prev, cur []int
	n := len(str)
candidates:
	for s2 := range seq {
		str2 := string(s2)
		if str == str2 {
			continue
		}

		n2 := len(str2)
		if n2+1 > len(cur) {
			prev2, prev, cur = make([]int, n2+1), make([]int, n2+1), make([]int, n2+1)
		}

		for x := range n2 + 1 {
			prev[x] = x
		}

		for y := 1; y <= n; y++ {
			cur[0] = y
			rowBest := y

			for x := 1; x <= n2; x++ {
				cost := 0
				if str[y-1] != str2[x-1] {
					cost = 1
				}
				cur[x] = min(prev[x-1]+cost, cur[x-1]+1, prev[x]+1)
				if y > 1 && x > 1 && str[y-1] == str2[x-2] && str[y-2] == str2[x-1] {
					cur[x] = min(cur[x], prev2[x-2]+1)
				}
				rowBest = min(rowBest, cur[x])
			}

			// The distance never drops below the best in a row.
			if rowBest > 2 {
				continue candidates
			}
			prev2, prev, cur = prev, cur, prev2
		}

		switch prev[n2] {
		case 1:
			dist1 = append(dist1, str2)
		case 2:
			dist2 = append(dist2, str2)
		}
	}
	return dist1, dist2
}

func TransposeStrings(strs []string) ([]string, error) {
	if len(strs) == 0 {
		return nil, nil
	}

	n := len(strs[0])
	b := make([]strings.Builder, n)
	for _, s := range strs {
		if len(s) != n {
			return nil, errors.New("not all string lengths are equal")
		}
		for i := range len(s) {
			b[i].WriteByte(s[i])
		}
	}

	r := make([]string, n)
	for i := range n {
		r[i] = b[i].String()
	}
	return r, nil
}

// Similar to strings.Cut, but cuts at the first rune where `f` return true;
// note that the second returned string includes the cutpoint rune.
func CutFunc(s string, f func(rune) bool) (string, string, bool) {
	for i, ch := range s {
		if f(ch) {
			return s[:i], s[i:], true
		}
	}
	return s, "", false
}

// CutAtSpace is like strings.Cut(s, " ") but preserves the space in the
// second return value.
func CutAtSpace(s string) (string, string) {
	if idx := strings.IndexByte(s, ' '); idx >= 0 {
		return s[:idx], s[idx:]
	}
	return s, ""
}

var ansiEscapeRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// StripANSI removes ANSI color escape sequences from s.
func StripANSI(s string) string {
	return ansiEscapeRE.ReplaceAllString(s, "")
}
