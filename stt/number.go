package stt

import (
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
)

// This file holds the package's single numeric decoder: DecodeNumber reads
// one number of a given kind (heading, speed, altitude, mach) from the
// token stream and returns every plausible interpretation as a scored
// candidate, best first. Whisper garbles numbers in stereotyped ways —
// merged digits, dropped or duplicated digits, "to" heard as "two",
// teen/-ty confusion — and each recovery is expressed here as one scored
// transform applied uniformly, rather than as per-parser special cases.
//
// Transform penalties: a literal in-range reading scores 1.0; each repair
// scores lower, ordered so that less-mangled interpretations win. These
// are relative ranks among interpretations of the same tokens, not
// similarity thresholds (those live in score.go).
const (
	numScoreLiteral = 1.0 // value used as spoken
	// Ordered repair ranks. When several repairs of the same tokens are
	// possible, the corpus-validated preference order is: remove a
	// duplicated (stuttered) digit, then reinterpret against the kind's
	// preferred multiples, then drop a leading garbled digit, then drop a
	// trailing garbled digit.
	numScoreDupDigit     = 0.95
	numScorePreferredMul = 0.90
	numScoreDropLeading  = 0.85
	numScoreDropTrailing = 0.80
)

// NumberKind identifies what kind of value is being decoded, which
// determines the valid range and the plausibility priors.
type NumberKind int

const (
	NumAltitude        NumberKind = iota // encoded hundreds of feet (50 = 5,000 ft)
	NumHeading                           // 001-360 degrees, normally a multiple of 5
	NumSpeed                             // 100-400 knots, normally a multiple of 10
	NumMach                              // hundredths (75 = Mach 0.75), 60-99
	NumOClock                            // clock position 1-12 (traffic advisories)
	NumTrafficAltitude                   // encoded hundreds, as reported in traffic advisories
)

// NumberContext carries the decoding kind and the aircraft context used
// for plausibility priors (current altitude, performance envelope).
type NumberContext struct {
	Kind NumberKind
	AC   Aircraft
	// AllowFlightLevel permits altitude values in 100-400 (which would
	// otherwise be reserved for speeds) when decoding in explicit
	// climb/descend context.
	AllowFlightLevel bool
}

// NumberCandidate is one scored interpretation of the tokens at a position.
type NumberCandidate struct {
	Value    int
	Consumed int // tokens consumed starting at the decode position
	Score    float64
}

// DecodeNumber decodes one number of the given kind at tokens[pos],
// returning all plausible interpretations ordered best-first. An empty
// result means no reading of the tokens at pos yields a valid value.
func DecodeNumber(tokens []Token, pos int, ctx NumberContext) []NumberCandidate {
	if pos >= len(tokens) {
		return nil
	}

	cands := kindCandidates(tokens, pos, ctx)

	// Garbled trailing digits: a word right after a number that reads as
	// a digit word extends the digit string ("three three fine" -> 335).
	// The extended readings are scored by the digit words' similarity and
	// compete with the unextended interpretations.
	if tokens[pos].Type == TokenNumber {
		text := tokens[pos].Text
		extra := 0
		digitConf := 1.0
		for p := pos + 1; p < len(tokens) && tokens[p].Type == TokenWord; p++ {
			d, ds := fuzzyDigit(tokens[p].Text)
			if d == "" {
				break
			}
			text += d
			extra++
			digitConf *= ds
		}
		if extra > 0 {
			if v, err := strconv.Atoi(text); err == nil {
				synth := append([]Token{{Text: text, Type: TokenNumber, Value: v}}, tokens[pos+1+extra:]...)
				for _, c := range kindCandidates(synth, 0, ctx) {
					c.Consumed += extra
					c.Score *= digitConf
					cands = append(cands, c)
				}
			}
		}
	}

	// Best first; ties broken deterministically by more tokens explained,
	// then lower value.
	slices.SortStableFunc(cands, func(a, b NumberCandidate) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		if a.Consumed != b.Consumed {
			return b.Consumed - a.Consumed
		}
		return a.Value - b.Value
	})
	return cands
}

// kindCandidates generates the per-kind interpretations at tokens[pos].
func kindCandidates(tokens []Token, pos int, ctx NumberContext) []NumberCandidate {
	switch ctx.Kind {
	case NumHeading:
		return headingCandidates(tokens, pos)
	case NumSpeed:
		return speedCandidates(tokens, pos, ctx.AC)
	case NumAltitude:
		return altitudeCandidates(tokens, pos, ctx)
	case NumMach:
		return machCandidates(tokens, pos)
	case NumOClock:
		return oclockCandidates(tokens, pos)
	case NumTrafficAltitude:
		return trafficAltitudeCandidates(tokens, pos)
	}
	return nil
}

// fuzzyDigit reads a garbled word as a spoken digit ("fine" for "five"),
// returning the digit and the match score, or "" if the word doesn't read
// as a digit. Command vocabulary and filler words never read as digits.
func fuzzyDigit(word string) (string, float64) {
	w := strings.ToLower(word)
	if len(w) < 3 || IsFillerWord(w) || commandVocabulary[w] {
		return "", 0
	}
	best, bestDigit := 0.0, ""
	for i, dw := range [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"} {
		if ws := WordScore(w, dw); ws >= 0.8 && ws > best {
			best = ws
			bestDigit = strconv.Itoa(i)
		}
	}
	return bestDigit, best
}

// headingCandidates generates interpretations of the tokens at pos as a
// heading (001-360).
func headingCandidates(tokens []Token, pos int) []NumberCandidate {
	t := tokens[pos]

	// "to"/"too"/"tu" heard for the digit "two": combine with the
	// following number, e.g. "heading to nine zero" -> 290.
	if t.Type == TokenWord && pos+1 < len(tokens) && tokens[pos+1].Type == TokenNumber {
		text := strings.ToLower(t.Text)
		if text == "to" || text == "too" || text == "tu" || text == "t" {
			nextVal := tokens[pos+1].Value
			if nextVal >= 0 && nextVal <= 160 {
				if combined := 200 + nextVal; combined <= 360 {
					return []NumberCandidate{{Value: combined, Consumed: 2, Score: numScoreDropLeading}}
				}
			}
		}
	}

	if t.Type != TokenNumber {
		return nil
	}
	v := t.Value

	// 4-digit values with a garbled extra digit (e.g., 1507): rank the
	// possible repairs against each other.
	if v > 360 && v < 10000 {
		var cands []NumberCandidate
		if hdg := v / 10; hdg >= 1 && hdg <= 360 {
			// Dropping the trailing digit. A multiple of 5 is a clean
			// heading; otherwise this reading is likely still mangled and
			// the repairs below outrank it.
			score := numScoreLiteral
			if hdg%5 != 0 {
				score = numScoreDropTrailing
			}
			cands = append(cands, NumberCandidate{Value: hdg, Consumed: 1, Score: score})

			if hdg%5 != 0 {
				// STT stutter duplicated a digit ("zero nine nine zero"
				// -> 0990; the speaker said 090).
				if better, ok := removeDuplicateDigit(t.Text, func(n int) bool {
					return n >= 1 && n <= 360 && n%5 == 0
				}); ok {
					cands = append(cands, NumberCandidate{Value: better, Consumed: 1, Score: numScoreDupDigit})
				}
				// STT inserted an extra leading digit ("one two one zero"
				// -> 1210; the speaker said 210).
				if mod := v % 1000; mod >= 1 && mod <= 360 && mod%5 == 0 {
					cands = append(cands, NumberCandidate{Value: mod, Consumed: 1, Score: numScorePreferredMul})
				}
			}
		} else if mod := v % 1000; mod >= 1 && mod <= 360 {
			cands = append(cands, NumberCandidate{Value: mod, Consumed: 1, Score: numScoreDropLeading})
		}
		return cands
	}

	// Leading-zero 4-digit values whose trailing digit breaks the
	// multiple-of-5 convention: the trailing digit is noise ("zero one
	// zero" plus a stray digit).
	if len(t.Text) == 4 && t.Text[0] == '0' && v%5 != 0 {
		if hdg := ParseNumber(t.Text[:3]); hdg >= 1 {
			return []NumberCandidate{{Value: hdg, Consumed: 1, Score: numScoreDropTrailing}}
		}
	}

	if v >= 1 && v <= 360 {
		// Garbled word between digit groups: "3 [garble] 40" -> 340.
		if v >= 1 && v <= 3 && pos+2 < len(tokens) &&
			tokens[pos+1].Type == TokenWord && !IsCommandKeyword(tokens[pos+1].Text) &&
			tokens[pos+2].Type == TokenNumber && tokens[pos+2].Value >= 10 {
			if combined := v*100 + tokens[pos+2].Value; combined >= 100 && combined <= 360 {
				logLocalStt("  heading: skipping garbled %q between digits: %d + %d -> %d",
					tokens[pos+1].Text, v, tokens[pos+2].Value, combined)
				return []NumberCandidate{{Value: combined, Consumed: 3, Score: numScorePreferredMul}}
			}
		}

		// Leading-zero and dropped-trailing-zero conventions. ATC headings
		// below 100 are spoken with a leading zero ("zero niner zero"), and
		// a bare one- or two-digit value usually lost its trailing zero.
		hdg := v
		score := numScoreLiteral
		if hasLeadingZero := len(t.Text) > 0 && t.Text[0] == '0'; hasLeadingZero {
			// "01" -> 010; but a 3-digit multiple of 5 like "005" is a
			// legitimate heading 5.
			if hdg < 10 && (len(t.Text) < 3 || hdg%5 != 0) {
				hdg *= 10
				score = numScorePreferredMul
			}
		} else if len(t.Text) == 2 && hdg >= 10 && hdg <= 36 && hdg%10 != 0 {
			hdg *= 10
			score = numScorePreferredMul
		} else if hdg < 10 {
			hdg *= 10
			score = numScorePreferredMul
		}
		return []NumberCandidate{{Value: hdg, Consumed: 1, Score: score}}
	}

	return nil
}

// speedCandidates generates interpretations of the tokens at pos as an
// assigned speed (100-400 knots). All values pass through the aircraft
// performance prior, which repairs speeds below the aircraft's envelope.
func speedCandidates(tokens []Token, pos int, ac Aircraft) []NumberCandidate {
	t := tokens[pos]
	if t.Type != TokenNumber {
		return nil
	}
	// A leading zero means the number was spoken digit-by-digit starting
	// with "zero" ("zero two eight zero"): a flight level, never a speed.
	if len(t.Text) > 0 && t.Text[0] == '0' {
		return nil
	}
	v := t.Value

	perf := func(speed, consumed int, score float64) NumberCandidate {
		return NumberCandidate{Value: adjustSpeedForPerformance(speed, ac), Consumed: consumed, Score: score}
	}

	// Normal range: round down to a multiple of 10 (ATC speeds always
	// are), except that trailing teens are the -ty form misheard ("one
	// eighteen" for "one eighty").
	if v >= 100 && v <= 400 {
		rounded := (v / 10) * 10
		score := numScoreLiteral
		if lastTwo := v % 100; lastTwo >= 11 && lastTwo <= 19 {
			rounded = (v/100)*100 + (v%10)*10
			score = numScorePreferredMul
		}
		return []NumberCandidate{perf(rounded, 1, score)}
	}

	// 4-digit value starting with 2 and ending in 0: the leading "2" is
	// "to" misheard as "two" ("speed to one seven zero" -> 2170 -> 170).
	if v >= 2000 && v < 3000 && v%10 == 0 {
		if last3 := v % 1000; last3 >= 100 && last3 <= 400 {
			return []NumberCandidate{perf(last3, 1, numScoreDropLeading)}
		}
	}

	// Extra garbled digit: rank dropping the leading digit against
	// dropping the trailing digit, preferring whichever yields a clean
	// multiple of 10.
	if v > 400 {
		div10, mod1000 := v/10, v%1000
		div10Valid := div10 >= 100 && div10 <= 400
		mod1000Valid := mod1000 >= 100 && mod1000 <= 400
		var cands []NumberCandidate
		if div10Valid && mod1000Valid {
			if mod1000%10 == 0 && div10%10 != 0 {
				cands = append(cands, perf(mod1000, 1, numScorePreferredMul), perf(div10, 1, numScoreDropTrailing))
			} else {
				cands = append(cands, perf(div10, 1, numScorePreferredMul), perf(mod1000, 1, numScoreDropLeading))
			}
		} else if mod1000Valid {
			cands = append(cands, perf(mod1000, 1, numScoreDropLeading))
		} else if div10Valid {
			cands = append(cands, perf(div10, 1, numScoreDropTrailing))
		}
		return cands
	}

	// Two-digit value that lost its trailing zero ("two one zero" split
	// or truncated), or whose leading "two" was heard as "to".
	if v >= 10 && v <= 99 {
		if pos+1 < len(tokens) {
			next := tokens[pos+1]
			// Explicit trailing zero as its own token: "21" "0" -> 210.
			if next.Type == TokenNumber && next.Value == 0 {
				if combined := v * 10; combined >= 100 && combined <= 400 {
					return []NumberCandidate{perf(combined, 2, numScorePreferredMul)}
				}
			}
			// Filler word standing in for a garbled "zero": "one seven
			// day" -> 170.
			if IsFillerWord(next.Text) {
				if combined := v * 10; combined >= 100 && combined <= 400 {
					return []NumberCandidate{perf(combined, 2, numScoreDropTrailing)}
				}
			}
			// "knots" confirms speed context and implies the dropped zero:
			// "two zero knots" -> 200. Require >= 150 to avoid implausible
			// low speeds; "knots" itself is not consumed.
			if strings.ToLower(next.Text) == "knots" {
				if combined := v * 10; combined >= 150 && combined <= 400 {
					return []NumberCandidate{perf(combined, 1, numScorePreferredMul)}
				}
			}
		}
		// A preceding "to" was really the leading digit "two" ("speed to
		// five zero" -> 250), but only when the combined value is already a
		// clean multiple of ten — otherwise the "to" is the preposition and
		// the value lost its trailing zero ("speed to one seven" -> 170).
		if pos > 0 && strings.ToLower(tokens[pos-1].Text) == "to" && v%10 == 0 {
			if combined := 200 + v; combined >= 100 && combined <= 400 {
				return []NumberCandidate{perf(combined, 1, numScoreDropLeading)}
			}
		}
		// Fallback: a bare two-digit value in speed context lost its
		// trailing zero ("speed two one" -> 210). Ranked below every
		// evidence-backed reading.
		if combined := v * 10; combined >= 150 && combined <= 400 {
			return []NumberCandidate{perf(combined, 1, numScoreDropTrailing)}
		}
	}

	return nil
}

// altitudeCandidates generates interpretations of the tokens at pos as an
// encoded altitude (hundreds of feet; 50 = 5,000 ft).
func altitudeCandidates(tokens []Token, pos int, ctx NumberContext) []NumberCandidate {
	t := tokens[pos]

	// An explicitly anchored altitude ("N thousand [M hundred]", "flight
	// level N") is as good as literal.
	if t.Type == TokenAltitude {
		return []NumberCandidate{{Value: t.Value, Consumed: 1, Score: numScoreLiteral}}
	}
	if t.Type != TokenNumber {
		return nil
	}
	v := t.Value

	// Standard encoded range (10-99 = 1,000-9,900 ft; higher with
	// AllowFlightLevel). 100-400 collides with the speed range and is
	// only readable as an altitude in explicit climb/descend context —
	// or when spoken digit-by-digit with a leading "zero" ("zero two
	// eight zero"), which is flight-level phrasing, never a speed.
	leadingZero := len(t.Text) > 0 && t.Text[0] == '0'
	if v >= 10 && v <= 600 && (v < 100 || v > 400 || ctx.AllowFlightLevel || leadingZero) {
		alt := v
		score := numScoreLiteral

		// In climb/descend context, a 3-digit value that isn't a multiple
		// of 10 has a garbled "thousand" merged into the digits: "one one
		// five" -> 115 where "five" was "thousand"; the altitude is 110.
		if ctx.AllowFlightLevel && alt >= 100 && alt%10 != 0 {
			corrected := (alt / 10) * 10
			logLocalStt("  altitude correction: %d -> %d (garbled thousand in digit sequence)", alt, corrected)
			alt = corrected
			score = numScoreDropTrailing
		}

		// Plausibility prior: an altitude below the aircraft's current
		// altitude whose x10 reading is valid was likely spoken with the
		// magnitude dropped ("16" for 16,000 ft with the aircraft at
		// 7,000 ft would otherwise read as 1,600 ft).
		if currentAlt := ctx.AC.Altitude / 100; alt < currentAlt && alt*10 <= 600 {
			logLocalStt("  altitude correction: %d -> %d (aircraft at %d ft)", alt, alt*10, ctx.AC.Altitude)
			alt *= 10
			score = min(score, numScorePreferredMul)
		}

		return []NumberCandidate{{Value: alt, Consumed: 1, Score: score}}
	}

	// Raw feet: "8000" -> 80.
	if v >= 1000 && v <= 60000 && v%100 == 0 {
		return []NumberCandidate{{Value: v / 100, Consumed: 1, Score: numScoreLiteral}}
	}

	// Repeated-altitude pattern ABAB ("one three thirteen" -> 1313): the
	// altitude was stated twice.
	if v >= 1010 && v <= 6060 {
		firstHalf, secondHalf := v/100, v%100
		if firstHalf == secondHalf && firstHalf >= 10 && firstHalf <= 60 {
			logLocalStt("  repeated altitude pattern: %d -> %d", v, firstHalf*10)
			return []NumberCandidate{{Value: firstHalf * 10, Consumed: 1, Score: numScoreDupDigit}}
		}
	}

	// A bare single digit means thousands ("descend five" -> 5,000 ft).
	if v >= 1 && v <= 9 {
		return []NumberCandidate{{Value: v * 10, Consumed: 1, Score: numScorePreferredMul}}
	}

	return nil
}

// machCandidates generates interpretations of the tokens at pos as a mach
// number in hundredths (75 = Mach 0.75).
func machCandidates(tokens []Token, pos int) []NumberCandidate {
	t := tokens[pos]
	if t.Type != TokenNumber {
		return nil
	}
	if t.Value >= 60 && t.Value <= 99 {
		return []NumberCandidate{{Value: t.Value, Consumed: 1, Score: numScoreLiteral}}
	}
	if t.Value >= 6 && t.Value <= 9 {
		return []NumberCandidate{{Value: t.Value * 10, Consumed: 1, Score: numScorePreferredMul}}
	}
	return nil
}

// trafficAltitudeCandidates generates interpretations of the tokens at pos
// as a traffic-advisory altitude (encoded hundreds of feet). Unlike
// assigned altitudes, traffic altitudes are commonly garbled together with
// the aircraft type ("Boeing 737 at 3 thousand" -> 73730), so implausible
// values recover from their trailing digits.
func trafficAltitudeCandidates(tokens []Token, pos int) []NumberCandidate {
	t := tokens[pos]

	if t.Type == TokenAltitude {
		// Altitudes above 60,000 ft are implausible: the token merged the
		// aircraft type with the altitude; recover the trailing 2 digits.
		if t.Value > 600 {
			if extracted := t.Value % 100; extracted >= 10 && extracted <= 60 {
				return []NumberCandidate{{Value: extracted, Consumed: 1, Score: numScoreDropLeading}}
			}
			return nil
		}
		return []NumberCandidate{{Value: t.Value, Consumed: 1, Score: numScoreLiteral}}
	}
	if t.Type != TokenNumber {
		return nil
	}
	v := t.Value

	if pos+1 < len(tokens) {
		next := strings.ToLower(tokens[pos+1].Text)
		if FuzzyMatch(next, "thousand", 0.8) {
			// "N thousand": the max with "thousand" is 17,000 ft — above
			// that it's "flight level" — so a larger N has tokenizer-merged
			// leading noise digits; recover the trailing digits.
			n := v
			score := numScoreLiteral
			if n > 17 {
				if last2 := n % 100; last2 >= 1 && last2 <= 17 {
					n = last2
				} else if last1 := n % 10; last1 >= 1 && last1 <= 17 {
					n = last1
				} else {
					return nil
				}
				score = numScoreDropLeading
			}
			alt, consumed := n*10, 2
			// "N thousand M hundred"
			if pos+3 < len(tokens) && tokens[pos+2].Type == TokenNumber &&
				FuzzyMatch(tokens[pos+3].Text, "hundred", 0.8) {
				alt += tokens[pos+2].Value
				consumed += 2
			}
			return []NumberCandidate{{Value: alt, Consumed: consumed, Score: score}}
		}
		// "N hundred" (low altitude like "five hundred")
		if FuzzyMatch(next, "hundred", 0.8) {
			return []NumberCandidate{{Value: v, Consumed: 2, Score: numScoreLiteral}}
		}
	}

	// Raw number in feet — must be a multiple of 100 to be an altitude
	// (which also skips aircraft types like 787, 737).
	if v >= 500 && v <= 60000 && v%100 == 0 {
		return []NumberCandidate{{Value: v / 100, Consumed: 1, Score: numScoreLiteral}}
	}
	return nil
}

// oclockCandidates generates interpretations of the tokens at pos as a
// clock position (1-12).
func oclockCandidates(tokens []Token, pos int) []NumberCandidate {
	t := tokens[pos]
	if t.Type != TokenNumber {
		return nil
	}
	v := t.Value
	if v >= 1 && v <= 12 {
		return []NumberCandidate{{Value: v, Consumed: 1, Score: numScoreLiteral}}
	}
	// The tokenizer merged a spurious leading digit into the clock
	// position ("eight eleven o'clock" -> 811): recover it from the
	// trailing digits.
	if last2 := v % 100; last2 >= 1 && last2 <= 12 {
		return []NumberCandidate{{Value: last2, Consumed: 1, Score: numScoreDropLeading}}
	}
	if last1 := v % 10; last1 >= 1 && last1 <= 12 {
		return []NumberCandidate{{Value: last1, Consumed: 1, Score: numScoreDropLeading}}
	}
	return nil
}

// removeDuplicateDigit repairs STT stutter where a digit was transcribed
// twice ("zero nine nine zero" -> "0990" instead of "090"). It removes each
// adjacent duplicated digit in turn and returns the first result accepted
// by valid.
func removeDuplicateDigit(text string, valid func(int) bool) (int, bool) {
	for i := 0; i+1 < len(text); i++ {
		if text[i] == text[i+1] {
			candidate := text[:i] + text[i+1:]
			val, err := strconv.Atoi(candidate)
			if err != nil {
				continue
			}
			if valid(val) {
				return val, true
			}
		}
	}
	return 0, false
}

// adjustSpeedForPerformance checks if a parsed speed is below the aircraft's
// minimum speed and, if so, tries bumping by +100/+200/+300 to find a
// plausible speed. This handles cases where the leading digit was garbled
// or lost in transcription (e.g., "one one zero" when the controller said
// "two one zero").
func adjustSpeedForPerformance(speed int, ac Aircraft) int {
	if ac.AircraftType == "" || av.DB == nil {
		return speed
	}
	perf, ok := av.DB.AircraftPerformance[ac.AircraftType]
	if !ok {
		return speed
	}
	minSpeed := perf.Speed.Min
	if minSpeed <= 0 || float32(speed) >= minSpeed {
		return speed
	}

	// Speed is below aircraft min; the leading digit was likely garbled.
	// Try bumping by 100 at a time to find the lowest plausible speed.
	for bump := 100; bump <= 300; bump += 100 {
		candidate := speed + bump
		if candidate > 400 {
			break
		}
		if float32(candidate) >= minSpeed {
			// Below 10,000' the speed limit is 250 kts; prefer
			// candidates that respect this when possible.
			if ac.Altitude > 0 && ac.Altitude < 10000 && candidate > 250 {
				continue
			}
			return candidate
		}
	}
	return speed
}
