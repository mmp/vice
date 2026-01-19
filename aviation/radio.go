// pkg/aviation/radio.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// These all store a mapping from written text to phonetic pronunciations that work better with
// voice synthesis. For the ones with arrays as map values, one of the items is randomly
// selected when one is needed.
var (
	sayAirportMap map[string][]string
	sayACTypeMap  map[string][]string
	sayFixMap     map[string]string
	sayAirlineMap map[string]string
	saySIDMap     map[string]string
	saySTARMap    map[string]string

	pronunciationsOnce sync.Once
)

func loadPronunciationsIfNeeded() {
	pronunciationsOnce.Do(func() {
		n := 0
		report := func(file string, err error) {
			fmt.Fprintf(os.Stderr, "%s: %v\n", file, err)
			n++
		}

		if err := json.Unmarshal(util.LoadResourceBytes("sayactype.json"), &sayACTypeMap); err != nil {
			report("sayactype.json", err)
		}
		if err := json.Unmarshal(util.LoadResourceBytes("sayairport.json"), &sayAirportMap); err != nil {
			report("sayairport.json", err)
		}
		if err := json.Unmarshal(util.LoadResourceBytes("sayairline.json"), &sayAirlineMap); err != nil {
			report("sayairline.json", err)
		}
		if err := json.Unmarshal(util.LoadResourceBytes("sayfix.json"), &sayFixMap); err != nil {
			report("sayfix.json", err)
		}
		if err := json.Unmarshal(util.LoadResourceBytes("saysid.json"), &saySIDMap); err != nil {
			report("saysid.json", err)
		}
		if err := json.Unmarshal(util.LoadResourceBytes("saystar.json"), &saySTARMap); err != nil {
			report("saystar.json", err)
		}

		if n > 0 {
			os.Exit(1)
		}
	})
}

///////////////////////////////////////////////////////////////////////////
// RadioTransmission

// RadioTransmission holds components that together represent a single
// radio transmission by a pilot; they may be built up from multiple
// instructions provided in a single controller command.
type RadioTransmission struct {
	Strings []PhraseFormatString
	Args    [][]any // each slice contains values passed to the corresponding PhraseFormatString
	Type    RadioTransmissionType
}

// MakeContactRadioTransmission is a helper function to make a pilot
// transmission for initial contact from a single formatting string and set
// of arguments.
func MakeContactTransmission(s string, args ...any) *RadioTransmission {
	rt := &RadioTransmission{Type: RadioTransmissionContact}
	rt.Add(s, args...)
	return rt
}

// MakeReadbackTransmission is a helper function to make a pilot
// transmission of a readback from a single formatting string and set of
// arguments.
func MakeReadbackTransmission(s string, args ...any) *RadioTransmission {
	rt := &RadioTransmission{Type: RadioTransmissionReadback}
	rt.Add(s, args...)
	return rt
}

// MakeUnexpectedTransmission similarly makes a single pilot
// transmission from the provided format string and arguments, but also
// marks the transmission as unexpected.
func MakeUnexpectedTransmission(s string, args ...any) *RadioTransmission {
	rt := &RadioTransmission{Type: RadioTransmissionUnexpected}
	rt.Add(s, args...)
	return rt
}

// MakeMixedUpTransmission creates a pilot transmission when the pilot is
// confused about who is being addressed.
func MakeMixedUpTransmission(s string, args ...any) *RadioTransmission {
	rt := &RadioTransmission{Type: RadioTransmissionMixUp}
	rt.Add(s, args...)
	return rt
}

// MakeNoIdTransmission creates a pilot transmission where the pilot doesn't
// identify themselves with their callsign (e.g., for saying "blocked").
func MakeNoIdTransmission(s string, args ...any) *RadioTransmission {
	rt := &RadioTransmission{Type: RadioTransmissionNoId}
	rt.Add(s, args...)
	return rt
}

// Merge takes a separately-constructed RadioTransmission and merges its
// contents with the current one.
func (rt *RadioTransmission) Merge(r *RadioTransmission) {
	rt.Strings = append(rt.Strings, r.Strings...)
	rt.Args = append(rt.Args, r.Args...)
	if r.Type == RadioTransmissionUnexpected {
		rt.Type = RadioTransmissionUnexpected
	}
}

// Validate ensures that the types of arguments match with the formatting
// directives in the PhraseFormatStrings; errors are logged to the provided
// logger.
func (rt *RadioTransmission) Validate(lg *log.Logger) {
	if len(rt.Strings) != len(rt.Args) {
		lg.Errorf("Mismatching len(Strings) %d and len(Args) %d", len(rt.Strings), len(rt.Args))
		return
	}
	for i := range rt.Strings {
		rt.Strings[i].Validate(rt.Args[i], lg)
	}
}

// Add is a convenience function to add a transmission snippet to the RadioTransmission.
// It's more or less equivalent to calling Merge(MakeRadioTransmission(...)).
func (rt *RadioTransmission) Add(s string, args ...any) {
	rt.Strings = append(rt.Strings, PhraseFormatString(s))
	rt.Args = append(rt.Args, args)
}

// Spoken returns a string corresponding to how the transmission should be
// spoken, which appropriate phonetic substitutions made (e.g. "9" ->
// "niner").
func (rt RadioTransmission) Spoken(r *rand.Rand) string {
	var result []string

	for i := range rt.Strings {
		s := rt.Strings[i].Spoken(r, rt.Args[i])
		result = append(result, s)
	}

	return strings.Join(result, " ") + "."
}

// Written returns a string corresponding to how the transmission should be
// displayed as text on the screen.
func (rt RadioTransmission) Written(r *rand.Rand) string {
	var result []string

	for i := range rt.Strings {
		s := rt.Strings[i].Written(r, rt.Args[i])
		result = append(result, strings.TrimSuffix(strings.TrimSpace(s), ","))
	}

	return strings.Join(result, ", ")
}

/////////////////////////////////////////////////////////////////////////////////////
// SnippetFormatter

// SnippetFormatter defines an interface for formatting short
// text snippets corresponding to various aviation-related concepts into
// both speech and text. Each is takes a single value specifying the value
// of the corresponding thing (altitude, speed, etc.)
type SnippetFormatter interface {
	// Written
	Written(arg any) string
	Spoken(r *rand.Rand, arg any) string
	Validate(arg any) error
}

var (
	// phraseFormats stores associates all of the formatting strings with
	// SnippetFormatter implementations. The map keys specify
	// the associated formatting strings.
	phraseFormats map[string]SnippetFormatter = map[string]SnippetFormatter{
		"actrl":    &AppControllerSnippetFormatter{},
		"actype":   &AircraftTypeSnippetFormatter{},
		"airport":  &AirportSnippetFormatter{},
		"alt":      &AltSnippetFormatter{},
		"altrest":  &AltRestrictionSnippetFormatter{},
		"appr":     &ApproachSnippetFormatter{},
		"beacon":   &BeaconCodeSnippetFormatter{},
		"callsign": &CallsignSnippetFormatter{},
		"ch":       &LetterSnippetFormatter{},
		"dctrl":    &DepControllerSnippetFormatter{},
		"fix":      &FixSnippetFormatter{},
		"freq":     &FrequencySnippetFormatter{},
		"gf":       &GroupFormSnippetFormatter{},
		"hdg":      &HeadingSnippetFormatter{},
		"num":      &BasicNumberSnippetFormatter{},
		"sid":      &SIDSnippetFormatter{},
		"spd":      &SpeedSnippetFormatter{},
		"star":     &STARSnippetFormatter{},
	}
)

///////////////////////////////////////////////////////////////////////////
// PhraseFormatString

// PhraseFormatString is a string that potentially includes
type PhraseFormatString string

// NOTE: allow extra args for variants. But need 1:1 for ordering...

func (s PhraseFormatString) Written(r *rand.Rand, args []any) string {
	sr := s.resolveOptions(r, nil)

	var result strings.Builder
	sr.applyFormatting(args, func(fmt SnippetFormatter, arg any) {
		result.WriteString(fmt.Written(arg))
	}, func(ch rune) {
		result.WriteRune(ch)
	})
	return result.String()
}

func (s PhraseFormatString) Spoken(r *rand.Rand, args []any) string {
	sr := s.resolveOptions(r, nil)

	var result strings.Builder
	sr.applyFormatting(args, func(f SnippetFormatter, arg any) {
		result.WriteString(f.Spoken(r, arg))
	}, func(ch rune) {
		result.WriteRune(ch)
	})

	return result.String()
}

func (s PhraseFormatString) Validate(args []any, lg *log.Logger) bool {
	anyErrors := false
	logFunc := func(err string) {
		anyErrors = true
		lg.Errorf("%s: %s", s, err)
	}

	for _, sr := range s.allResolved(logFunc) {
		sr.applyFormatting(args, func(f SnippetFormatter, arg any) {
			if err := f.Validate(arg); err != nil {
				logFunc(err.Error())
			}
		},
			func(ch rune) {})
	}
	return !anyErrors
}

func (s PhraseFormatString) applyFormatting(args []any, fmt func(SnippetFormatter, any), c func(rune)) {
	braceIndex := 0
	argIndex := 0
	foundBrace := false

	// No error checking here: assume that Validate() has been called to catch any issues.
	for i, ch := range s {
		if ch == '{' {
			foundBrace = true
			braceIndex = i
		} else if ch == '}' {
			foundBrace = false
			match := string(s[braceIndex+1 : i])
			if f, ok := phraseFormats[match]; ok {
				if argIndex < len(args) {
					fmt(f, args[argIndex])
					argIndex++
				}
			}
		} else if !foundBrace {
			c(ch)
		}
	}
}

func (s PhraseFormatString) allResolved(err func(string)) []PhraseFormatString {
	return allResolvedHelper("", string(s), err)
}

func allResolvedHelper(spre string, spost string, err func(string)) []PhraseFormatString {
	inBrackets := false
	var pre, options strings.Builder

	pre.WriteString(spre)

	for i, ch := range spost {
		if ch == '[' {
			if inBrackets {
				err("unclosed [")
			}
			inBrackets = true
		} else if ch == ']' {
			inBrackets = false
			var resolved []PhraseFormatString
			for opt := range strings.SplitSeq(options.String(), "|") {
				resolved = append(resolved, allResolvedHelper(pre.String()+opt, spost[i+1:], err)...)
			}
			return resolved
		} else if inBrackets {
			options.WriteRune(ch)
		} else {
			pre.WriteRune(ch)
		}
	}
	if inBrackets {
		err("unclosed [")
	}

	return []PhraseFormatString{PhraseFormatString(pre.String())}
}

// given a string of the form "hello [you|there] I'm [me|myself]", returns
// a randomly sampled variant of the string, e.g. "hello there, I'm me".
func (s PhraseFormatString) resolveOptions(r *rand.Rand, err func(string)) PhraseFormatString {
	inBrackets := false
	var result, options strings.Builder

	for _, ch := range s {
		if ch == '[' {
			if inBrackets && err != nil {
				err("unclosed [")
			}
			inBrackets = true
		} else if ch == ']' {
			inBrackets = false
			opts := strings.Split(options.String(), "|")
			result.WriteString(opts[r.Intn(len(opts))])
			options.Reset()
		} else if inBrackets {
			options.WriteRune(ch)
		} else {
			result.WriteRune(ch)
		}
	}
	if inBrackets && err != nil {
		err("unclosed [")
	}

	return PhraseFormatString(result.String())
}

///////////////////////////////////////////////////////////////////////////
// General "saying things" utilities...

func sayDigit(n int) string {
	return []string{"zero", "one", "two", "three", "four", "five", "six",
		"seven", "eight", "niner"}[n]
}

// Returns a string that says the digits of v individually, with leading
// "zero"s as needed to ensure that n digits are spoken.
func sayDigits(v, n int) string {
	var d []string
	for v != 0 {
		d = append([]string{sayDigit(v % 10)}, d...)
		v /= 10
	}
	for len(d) < n {
		d = append([]string{"zero"}, d...)
	}
	return strings.Join(d, " ")
}

// Returns a string that corresponds to saying the given number in group form.
func groupForm(v int) string {
	if v < 10 {
		return sayDigit(v)
	} else if v < 100 {
		return strconv.Itoa(v)
	} else if (v%100) == 0 && v < 1000 {
		return sayDigit(v/100) + " hundred"
	} else {
		gf := groupForm(v / 100)
		v = v % 100
		if v < 10 {
			return gf + " zero " + sayDigit(v)
		} else {
			return gf + " " + strconv.Itoa(v)
		}
	}
}

func sayAltitude(alt int, r *rand.Rand) string {
	alt = 100 * (alt / 100) // round to 100s
	if alt >= 18000 {
		// flight levels
		fl := alt / 100
		return "flight level " + sayDigits(fl, 0)
	} else if alt < 1000 {
		return sayDigit(alt/100) + " hundred"
	} else {
		th := alt / 1000
		hu := (alt % 1000) / 100
		if hu != 0 {
			// have hundreds
			if r.Bool() {
				return sayDigits(th, 0) + " thousand " + sayDigit(hu) + " hundred"
			} else {
				return fmt.Sprintf("%d thousand %d hundred", th, hu)
			}
		} else {
			// at a multiple of 1000
			if r.Bool() {
				return sayDigits(th, 0) + " thousand"
			} else {
				return fmt.Sprintf("%d thousand", th)
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// AltSnippetFormatter

// AltSnippetFormatter formats altitudes, which may be given as ints or float32s.
type AltSnippetFormatter struct{}

func (a *AltSnippetFormatter) Written(arg any) string {
	if alt, ok := arg.(float32); ok {
		return FormatAltitude(alt)
	} else if alt, ok := arg.(int); ok {
		return FormatAltitude(float32(alt))
	} else {
		return "???"
	}
}

func (a *AltSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	alt, ok := arg.(int)
	if !ok {
		alt = int(arg.(float32))
	}

	return sayAltitude(alt, r)
}

func (a *AltSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(float32); !ok {
		if _, ok := arg.(int); !ok {
			return fmt.Errorf("expected int or float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// ApproachSnippetFormatter

type ApproachSnippetFormatter struct{}

func (ApproachSnippetFormatter) Written(arg any) string {
	return arg.(string)
}

func (ApproachSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	appr := arg.(string)

	// Split on commas first, process each part, then rejoin with commas.
	// This handles approach names like "ILS Runway 15R, then visual approach..."
	var spokenParts []string
	for part := range strings.SplitSeq(appr, ",") {
		spokenParts = append(spokenParts, spokenApproachPart(r, strings.TrimSpace(part)))
	}
	return strings.Join(spokenParts, ", ")
}

func spokenApproachPart(r *rand.Rand, appr string) string {
	var result []string
	lastRunway := false
	for word := range strings.FieldsSeq(appr) {
		if lastRunway {
			for _, ch := range strings.ToLower(word) {
				switch ch {
				case 'l':
					result = append(result, "left")
				case 'r':
					result = append(result, "right")
				case 'c':
					result = append(result, "center")
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					result = append(result, sayDigit(int(ch-'0')))
				default:
					panic(string(ch) + ": unexpected in runway " + word)
				}
			}
			lastRunway = false
		} else {
			if strings.ToLower(word) == "runway" {
				lastRunway = true
				if r.Bool() {
					result = append(result, "runway")
				}
			} else if strings.ToUpper(word) == "ILS" {
				result = append(result, "I-L-S")
			} else if strings.ToUpper(word) == "RNAV" {
				result = append(result, "r-nav")
			} else if strings.ToUpper(word) == "VOR" {
				result = append(result, "v-o-r")
			} else if sp, ok := spokenLetters[strings.ToUpper(word)]; ok {
				result = append(result, sp)
			} else {
				result = append(result, word)
			}
		}
	}

	return strings.Join(result, " ")
}

func (ApproachSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AirportSnippetFormatter

type AirportSnippetFormatter struct{}

func (AirportSnippetFormatter) Written(arg any) string {
	return arg.(string)
}

var trailingParenRe = regexp.MustCompile(`^(.*) \([^)]+\)$`)

func (AirportSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	loadPronunciationsIfNeeded()
	icao := arg.(string)
	if opts, ok := sayAirportMap[icao]; ok && len(opts) > 0 {
		ap, _ := rand.SampleSeq(r, slices.Values(opts))
		return ap
	} else if ap, ok := DB.Airports[icao]; ok && ap.Name != "" {
		name := ap.Name

		// If it's multiple things separated by a slash, pick one at random.
		f := strings.Split(name, "/")
		name = strings.TrimSpace(f[r.Intn(len(f))])

		// Strip any trailing parenthetical.
		if sm := trailingParenRe.FindStringSubmatch(name); sm != nil {
			name = sm[1]
		}

		// Strip suffixes that likely wouldn't be said verbally.
		for _, extra := range []string{"Airport", "Air Field", "Field", "Strip", "Airstrip", "International", "Regional"} {
			name = strings.TrimSuffix(name, " "+extra)
		}

		return name
	} else {
		return icao
	}
}

func (AirportSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// DepControllerSnippetFormatter

type DepControllerSnippetFormatter struct{}

func shortenController(n string) string {
	n = strings.ToLower(n)
	for _, pos := range []string{"tower", "departure", "approach", "center"} {
		if strings.Contains(n, pos) {
			return pos
		}
	}
	return n
}

func (DepControllerSnippetFormatter) Written(arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Approach", "Departure")
	n = strings.ReplaceAll(n, "approach", "departure")
	return n
}

func (DepControllerSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Approach", "Departure")
	n = strings.ReplaceAll(n, "approach", "departure")
	if r.Bool() {
		return shortenController(n)
	} else {
		return n
	}
}

func (DepControllerSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(*Controller); !ok {
		return fmt.Errorf("expected *Controller arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AppControllerSnippetFormatter

type AppControllerSnippetFormatter struct{}

func (AppControllerSnippetFormatter) Written(arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Departure", "Approach")
	n = strings.ReplaceAll(n, "departure", "approach")
	return n
}

func (AppControllerSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Departure", "Approach")
	n = strings.ReplaceAll(n, "departure", "approach")
	if r.Bool() {
		return shortenController(n)
	} else {
		return n
	}
}

func (AppControllerSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(*Controller); !ok {
		return fmt.Errorf("expected *Controller arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SpeedSnippetFormatter

type SpeedSnippetFormatter struct{}

func (SpeedSnippetFormatter) Written(arg any) string {
	spd, ok := arg.(int)
	if !ok {
		spd = int(arg.(float32))
	}
	return fmt.Sprintf("%d knots", spd)
}

func (SpeedSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	spd, ok := arg.(int)
	if !ok {
		spd = int(arg.(float32))
	}

	knots := util.Select(r.Bool(), " knots", "")
	if r.Bool() {
		return sayDigits(spd, 0) + knots
	} else {
		return groupForm(spd) + knots
	}
}

func (SpeedSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int or float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// FixSnippetFormatter

type FixSnippetFormatter struct{}

func (FixSnippetFormatter) Written(arg any) string {
	fix := arg.(string)
	// Cut off any trailing bits like COLIN.JT
	fix, _, _ = strings.Cut(fix, ".")

	if aid, ok := DB.Navaids[fix]; ok {
		return util.StopShouting(aid.Name)
	} else if ap, ok := DB.Airports[fix]; ok {
		return ap.Name
	}
	return fix
}

func (f FixSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	return GetFixTelephony(arg.(string))
}

func (FixSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// HeadingSnippetFormatter

type HeadingSnippetFormatter struct{}

func (HeadingSnippetFormatter) Written(arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return fmt.Sprintf("%03d", hdg)
}

func (HeadingSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}

	if r.Bool() || hdg < 100 {
		return sayDigits(hdg, 3)
	} else {
		return groupForm(hdg)
	}
}

func (HeadingSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int/float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// BasicNumberSnippetFormatter

type BasicNumberSnippetFormatter struct{}

func (BasicNumberSnippetFormatter) Written(arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return strconv.Itoa(hdg)
}

func (BasicNumberSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return strconv.Itoa(hdg)
}

func (BasicNumberSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int/float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Callsign utilities

// GetACTypePronunciations returns all pronunciation variants for an aircraft type.
// For example, "C172" might return ["skyhawk", "cessna one seventy-two"].
// Returns nil if the type is not found in sayactype.json.
func GetACTypePronunciations(acType string) []string {
	loadPronunciationsIfNeeded()
	if variants, ok := sayACTypeMap[acType]; ok {
		return variants
	}
	return nil
}

// GetTrailing3Spoken returns the trailing 3 characters of a callsign as spoken phonetics.
// For "N123AB", returns "3 alpha bravo".
// For callsigns shorter than 3 chars (excluding N prefix), returns the whole number.
func GetTrailing3Spoken(callsign string) string {
	if len(callsign) < 2 {
		return ""
	}

	// Get the part after N (or the whole callsign if no N prefix)
	suffix := callsign
	if strings.HasPrefix(callsign, "N") {
		suffix = callsign[1:]
	}

	// Get trailing 3 characters
	if len(suffix) > 3 {
		suffix = suffix[len(suffix)-3:]
	}

	// Convert to spoken form
	var parts []string
	for _, ch := range strings.ToUpper(suffix) {
		if ch >= '0' && ch <= '9' {
			parts = append(parts, sayDigit(int(ch-'0')))
		} else if sp, ok := spokenLetters[string(ch)]; ok {
			parts = append(parts, strings.ToLower(sp))
		}
	}
	return strings.Join(parts, " ")
}

// SplitCallsign splits a callsign into ICAO prefix and flight number.
// For "UAL123" returns ("UAL", "123"). For "N12345" returns ("N", "12345").
func SplitCallsign(callsign string) (prefix, number string) {
	if idx := strings.IndexAny(callsign, "0123456789"); idx != -1 {
		return callsign[:idx], callsign[idx:]
	}
	return callsign, ""
}

// GetTelephony returns the spoken telephony string for a callsign.
// cwtCategory determines the heavy/super suffix: "A" = super, "B"/"C"/"D" = heavy.
func GetTelephony(callsign string, cwtCategory string) string {
	prefix, number := SplitCallsign(callsign)

	var tele string
	if prefix == "N" {
		tele = "november"
	} else if t, ok := DB.Callsigns[prefix]; ok {
		tele = t
	}

	if number != "" {
		tele += " " + number
	}

	if cwtCategory == "A" {
		tele += " super"
	} else if len(cwtCategory) > 0 && cwtCategory[0] <= 'D' {
		tele += " heavy"
	}

	return tele
}

// GetFixTelephony returns the spoken name for a fix (navaid, airport, or waypoint).
// It uses pronunciations from sayfix.json when available, falls back to database
// lookups for navaids/airports, and uses StopShouting for other fixes.
func GetFixTelephony(fix string) string {
	loadPronunciationsIfNeeded()

	// Cut off any trailing bits like COLIN.JT
	fix, _, _ = strings.Cut(fix, ".")

	// For 3-char fixes or 4-char starting with K (VORs, airports), use the full name
	if len(fix) == 3 || (len(fix) == 4 && fix[0] == 'K') {
		if aid, ok := DB.Navaids[fix]; ok {
			return util.StopShouting(aid.Name)
		} else if ap, ok := DB.Airports[fix]; ok {
			return ap.Name
		}
	}

	// Check sayfix.json for pronunciation
	if say, ok := sayFixMap[fix]; ok {
		return say
	}

	// Fall back to StopShouting for readability
	return util.StopShouting(fix)
}

// GetApproachTelephony returns the spoken form of an approach name.
// For example, "RNAV X Runway 22L" becomes "r-nav x-ray runway 2 2 left".
func GetApproachTelephony(approach string) string {
	var result []string
	lastRunway := false

	for word := range strings.FieldsSeq(approach) {
		if lastRunway {
			// Handle runway number and suffix (e.g., "22L")
			for _, ch := range strings.ToLower(word) {
				switch ch {
				case 'l':
					result = append(result, "left")
				case 'r':
					result = append(result, "right")
				case 'c':
					result = append(result, "center")
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					result = append(result, sayDigit(int(ch-'0')))
				}
			}
			lastRunway = false
		} else {
			upper := strings.ToUpper(word)
			lower := strings.ToLower(word)
			if lower == "runway" {
				lastRunway = true
				result = append(result, "runway")
			} else if upper == "ILS" {
				result = append(result, "I L S")
			} else if upper == "RNAV" {
				result = append(result, "r-nav")
			} else if upper == "VOR" {
				result = append(result, "V O R")
			} else if upper == "GPS" {
				result = append(result, "G P S")
			} else if upper == "LOC" {
				result = append(result, "localizer")
			} else if upper == "LDA" {
				result = append(result, "L D A")
			} else if upper == "NDB" {
				result = append(result, "N D B")
			} else if sp, ok := spokenLetters[upper]; ok {
				result = append(result, sp)
			} else {
				result = append(result, word)
			}
		}
	}

	return strings.Join(result, " ")
}

///////////////////////////////////////////////////////////////////////////
// CallsignSnippetFormatter

type CallsignSnippetFormatter struct{}

// CallsignArg provides additional context for formatting callsigns.
type CallsignArg struct {
	Callsign           ADSBCallsign
	IsEmergency        bool
	AlwaysFullCallsign bool
}

// GACallsignArg provides context for formatting GA callsigns with type-based addressing.
// When UseTypeForm is true, the callsign is spoken as "aircraft type + trailing 3"
// (e.g., "skyhawk 3 alpha bravo" instead of "november 1 2 3 alpha bravo").
type GACallsignArg struct {
	Callsign     ADSBCallsign
	AircraftType string // e.g., "C172"
	UseTypeForm  bool   // If true, use type+trailing3 form
	IsEmergency  bool
}

func (CallsignSnippetFormatter) Written(arg any) string {
	var callsign string
	var isEmergency bool
	var useTypeForm bool
	var acType string

	switch ca := arg.(type) {
	case CallsignArg:
		callsign = string(ca.Callsign)
		isEmergency = ca.IsEmergency
	case GACallsignArg:
		callsign = string(ca.Callsign)
		isEmergency = ca.IsEmergency
		useTypeForm = ca.UseTypeForm
		acType = ca.AircraftType
	default:
		return "???"
	}

	icao, fnum := SplitCallsign(callsign)
	if icao == "N" {
		// For GA callsigns with type form, show abbreviated version
		if useTypeForm && acType != "" {
			acAlias := DB.AircraftTypeAliases[acType]
			if acAlias == "" {
				acAlias = acType
			}
			// Get trailing 3
			suffix := fnum
			if len(suffix) > 3 {
				suffix = suffix[len(suffix)-3:]
			}
			cs := acAlias + " " + suffix
			if isEmergency {
				cs += " (emergency)"
			}
			return cs
		}
		return callsign
	}

	cs := DB.Callsigns[icao] + " " + fnum

	if isEmergency {
		cs += " (emergency)"
	}

	return cs
}

func (CallsignSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	loadPronunciationsIfNeeded()

	var callsign string
	var isEmergency bool
	var alwaysFullCallsign bool
	var useTypeForm bool
	var acType string

	switch ca := arg.(type) {
	case CallsignArg:
		callsign = string(ca.Callsign)
		isEmergency = ca.IsEmergency
		alwaysFullCallsign = ca.AlwaysFullCallsign
	case GACallsignArg:
		callsign = string(ca.Callsign)
		isEmergency = ca.IsEmergency
		useTypeForm = ca.UseTypeForm
		acType = ca.AircraftType
		alwaysFullCallsign = true // GA always says full identifier in type form
	default:
		return "???"
	}

	icao, fnum := SplitCallsign(callsign)

	if icao == "N" {
		// For GA callsigns with type form, use aircraft type + trailing 3
		if useTypeForm && acType != "" {
			typeVariants := sayACTypeMap[acType]
			if len(typeVariants) > 0 {
				// Filter out variants with numbers to avoid callsign confusion
				var filtered []string
				for _, v := range typeVariants {
					if !strings.ContainsAny(v, "0123456789") {
						filtered = append(filtered, v)
					}
				}
				if len(filtered) > 0 {
					// Pick a random pronunciation variant
					typeSpoken, _ := rand.SampleSeq(r, slices.Values(filtered))
					trailing3 := GetTrailing3Spoken(callsign)
					return typeSpoken + " " + trailing3
				}
			}
		}

		// Default: spell out the full N-number
		var s []string
		for _, ch := range callsign {
			if ch >= '0' && ch <= '9' {
				s = append(s, sayDigit(int(ch-'0')))
			} else {
				s = append(s, spokenLetters[string(ch)])
			}
		}
		return strings.Join(s, " ")
	}

	// peel off any trailing letters
	var suffix string
	if suffixIdx := strings.IndexAny(fnum, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); suffixIdx != -1 {
		for _, ch := range fnum[suffixIdx:] {
			suffix += " " + spokenLetters[string(ch)]
		}
		fnum = fnum[:suffixIdx]
	}

	// figure out the telephony
	tel := DB.Callsigns[icao]
	if tel2, ok := sayAirlineMap[tel]; ok { // overrides
		tel = tel2
	}

	// For non-emergency aircraft reading back instructions, 5% of the time
	// skip the ICAO identifier and just say the flight number.
	if !isEmergency && !alwaysFullCallsign && r.Float32() < 0.05 {
		tel = ""
	}

	result := strings.TrimSpace(tel + " " + sayFlightNumber(fnum) + suffix)

	return result
}

func sayFlightNumber(id string) string {
	if len(id) == 0 {
		return ""
	}
	if id[0] != '0' {
		// No leading zeros, just do regular group form.
		n, _ := strconv.Atoi(id)
		return groupForm(n)
	} else {
		// Digits individually
		s := ""
		for _, d := range id {
			s += sayDigit(int(d-'0')) + " "
		}
		return s
	}
}

func (CallsignSnippetFormatter) Validate(arg any) error {
	switch arg.(type) {
	case CallsignArg, GACallsignArg:
		return nil
	default:
		return fmt.Errorf("expected CallsignArg or GACallsignArg, got %T", arg)
	}
}

///////////////////////////////////////////////////////////////////////////
// LetterSnippetFormatter

type LetterSnippetFormatter struct{}

func (LetterSnippetFormatter) Written(arg any) string {
	return arg.(string)
}

var spokenLetters = map[string]string{
	"A": "alpha", "B": "brahvo", "C": "charlie", "D": "delta",
	"E": "echo", "F": "foxtrot", "G": "golf", "H": "hotel", "I": "India",
	"J": "Juliet", "K": "Kilo", "L": "Lima", "M": "mike", "N": "November",
	"O": "Oscar", "P": "Pahpah", "Q": "Kebeck", "R": "Romeo", "S": "Sierra",
	"T": "tango", "U": "uniform", "V": "victor", "W": "whiskey", "X": "x-ray",
	"Y": "yankee", "Z": "zulu",
}

func (LetterSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	return spokenLetters[arg.(string)]
}

func (LetterSnippetFormatter) Validate(arg any) error {
	if s, ok := arg.(string); !ok || len(s) != 1 || (s[0] < 'A' || s[0] > 'Z') {
		return fmt.Errorf("expected single-character string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SIDSnippetFormatter

type SIDSnippetFormatter struct{}

func (s SIDSnippetFormatter) Written(arg any) string {
	return arg.(string)
}

func (SIDSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	loadPronunciationsIfNeeded()
	sid, num := trimNumber(arg.(string))
	if say, ok := saySIDMap[sid]; ok {
		return say + " " + sayDigit(num)
	}
	return sid + " " + sayDigit(num)
}

func (SIDSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

func trimNumber(s string) (string, int) {
	if n := len(s); n > 1 && (s[n-1] >= '0' && s[n-1] <= '9') {
		return s[:n-1], int(s[n-1] - '0')
	}
	return s, 0
}

///////////////////////////////////////////////////////////////////////////
// STARSnippetFormatter

type STARSnippetFormatter struct{}

func (s STARSnippetFormatter) Written(arg any) string {
	return arg.(string)
}

func (STARSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	loadPronunciationsIfNeeded()
	star, num := trimNumber(arg.(string))
	if say, ok := saySTARMap[star]; ok {
		return say + " " + sayDigit(num)
	}
	return star + " " + sayDigit(num)
}

func (STARSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// FrequencySnippetFormatter

type FrequencySnippetFormatter struct{}

func (FrequencySnippetFormatter) Written(arg any) string {
	f := arg.(Frequency)
	return fmt.Sprintf("%03d.%02d", f/1000, (f%1000)/10)
}

func (FrequencySnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	f := arg.(Frequency)
	whole := (f / 1000) % 100
	frac := (f % 1000) / 10 // Two digits after decimal

	switch r.Intn(3) {
	case 0:
		// Two digit pairs: "twenty-three forty-five" or "twenty-eight twenty"
		return fmt.Sprintf("%d %d", whole, frac)
	case 1:
		// With "one" prefix: "one twenty-three point forty-five" or "one twenty-eight point two"
		if frac%10 == 0 {
			return fmt.Sprintf("one %d point %d", whole, frac/10)
		}
		return fmt.Sprintf("one %d point %d", whole, frac)
	default:
		// Without "one": "twenty-three point forty-five" or "twenty-eight point two"
		if frac%10 == 0 {
			return fmt.Sprintf("%d point %d", whole, frac/10)
		}
		return fmt.Sprintf("%d point %d", whole, frac)
	}
}

func (FrequencySnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(Frequency); !ok {
		return fmt.Errorf("expected Frequency arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// GroupFormSnippetFormatter

type GroupFormSnippetFormatter struct{}

func (GroupFormSnippetFormatter) Written(arg any) string {
	v := arg.(int)
	return fmt.Sprintf("%d", v)
}

func (GroupFormSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	v := arg.(int)
	return groupForm(v)
}

func (GroupFormSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		return fmt.Errorf("expected int arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// BeaconCodeSnippetFormatter

type BeaconCodeSnippetFormatter struct{}

func (BeaconCodeSnippetFormatter) Written(arg any) string {
	v := arg.(Squawk)
	return v.String()
}

func (BeaconCodeSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	v := arg.(Squawk)
	s := v.String()
	if r.Bool() {
		return s[:2] + " " + s[2:]
	} else {
		return sayDigit(int(s[0]-'0')) + " " + sayDigit(int(s[1]-'0')) + " " + sayDigit(int(s[2]-'0')) + " " + sayDigit(int(s[3]-'0'))
	}
}

func (BeaconCodeSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(Squawk); !ok {
		return fmt.Errorf("expected Squawk arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AircraftTypeSnippetFormatter

type AircraftTypeSnippetFormatter struct{}

func (AircraftTypeSnippetFormatter) Written(arg any) string {
	return DB.AircraftTypeAliases[arg.(string)] + "(" + arg.(string) + ")"
}

func (AircraftTypeSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	loadPronunciationsIfNeeded()
	ac := arg.(string)
	if say, ok := sayACTypeMap[ac]; ok && len(say) > 0 {
		s, _ := rand.SampleSeq(r, slices.Values(say))
		return s
	}
	return DB.AircraftTypeAliases[ac]
}

func (AircraftTypeSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AltRestrictionSnippetFormatter

type AltRestrictionSnippetFormatter struct{}

func (AltRestrictionSnippetFormatter) Written(arg any) string {
	ar, ok := arg.(AltitudeRestriction)
	if !ok {
		ar = *arg.(*AltitudeRestriction)
	}

	if ar.Range[0] != 0 {
		if ar.Range[1] == ar.Range[0] {
			return "at " + FormatAltitude(ar.Range[0])
		} else if ar.Range[1] != 0 {
			return "between " + FormatAltitude(ar.Range[0]) + " and " + FormatAltitude(ar.Range[1])
		} else {
			return "at or above " + FormatAltitude(ar.Range[0])
		}
	} else if ar.Range[1] != 0 {
		return "at or below " + FormatAltitude(ar.Range[1])
	} else {
		return ""
	}
}

func (AltRestrictionSnippetFormatter) Spoken(r *rand.Rand, arg any) string {
	ar, ok := arg.(AltitudeRestriction)
	if !ok {
		ar = *arg.(*AltitudeRestriction)
	}

	if ar.Range[0] != 0 {
		if ar.Range[1] == ar.Range[0] {
			return "at " + sayAltitude(int(ar.Range[0]), r)
		} else if ar.Range[1] != 0 {
			return "between " + sayAltitude(int(ar.Range[0]), r) + " and " + sayAltitude(int(ar.Range[1]), r)
		} else {
			return "at or above " + sayAltitude(int(ar.Range[0]), r)
		}
	} else if ar.Range[1] != 0 {
		return "at or below " + sayAltitude(int(ar.Range[1]), r)
	} else {
		return ""
	}
}

func (AltRestrictionSnippetFormatter) Validate(arg any) error {
	if _, ok := arg.(*AltitudeRestriction); !ok {
		if _, ok := arg.(AltitudeRestriction); !ok {
			return fmt.Errorf("expected [*]AltitudeRestriction arg, got %T", arg)
		}
	}
	return nil
}
