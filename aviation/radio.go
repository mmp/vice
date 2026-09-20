// pkg/aviation/radio.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// pronunciations maps written text to the phonetic spellings that work
// better with voice synthesis. Where a slice is stored, one of its items is
// chosen at random when one is needed.
type pronunciations struct {
	airports map[string][]string
	acTypes  map[string][]string
	fixes    map[string]string
	airlines map[string]string
	sids     map[string]string
	stars    map[string]string
}

// parsePronunciations reads the say*.json files; it is called as part of
// loading the aviation database, so editing one of them takes effect on a
// reload along with the rest of it.
func parsePronunciations() pronunciations {
	var say pronunciations
	load := func(file string, m any) {
		if err := json.Unmarshal(util.LoadResourceBytes(file), m); err != nil {
			panic(fmt.Sprintf("%s: %v", file, err))
		}
	}

	load("sayactype.json", &say.acTypes)
	load("sayairport.json", &say.airports)
	load("sayairline.json", &say.airlines)
	load("sayfix.json", &say.fixes)
	load("saysid.json", &say.sids)
	load("saystar.json", &say.stars)

	return say
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

// A RadioTransmission is built when the pilot decides what to say but rendered
// when they say it, so a queued one has to survive being saved to the user's
// config and read back. Args is []any, and a plain JSON round trip loses the
// types the SnippetFormatters require: every number returns as a float64 and
// every named string type as a string. taggedArg carries the type along.
type taggedArg struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// serializedTransmission is the on-disk form of a RadioTransmission.
type serializedTransmission struct {
	Strings []PhraseFormatString
	Args    [][]taggedArg
	Type    RadioTransmissionType
}

func decodeArg[T any](b json.RawMessage) (any, error) {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// phraseArgTypes gives a decoder for each type a phrase argument may have,
// keyed as %T prints it. A type the SnippetFormatters accept but that isn't
// here can't be saved, so MarshalJSON reports it rather than writing something
// that won't read back.
var phraseArgTypes = map[string]func(json.RawMessage) (any, error){
	"int":                           decodeArg[int],
	"float32":                       decodeArg[float32],
	"string":                        decodeArg[string],
	"aviation.ICAOAirportCode":      decodeArg[ICAOAirportCode],
	"aviation.FAAAirportCode":       decodeArg[FAAAirportCode],
	"aviation.Squawk":               decodeArg[Squawk],
	"aviation.Frequency":            decodeArg[Frequency],
	"aviation.CallsignArg":          decodeArg[CallsignArg],
	"aviation.GACallsignArg":        decodeArg[GACallsignArg],
	"aviation.AltitudeRestriction":  decodeArg[AltitudeRestriction],
	"*aviation.AltitudeRestriction": decodeArg[*AltitudeRestriction],
	"*aviation.Controller":          decodeArg[*Controller],
	"math.MagneticHeading":          decodeArg[math.MagneticHeading],
}

func (rt RadioTransmission) MarshalJSON() ([]byte, error) {
	args := make([][]taggedArg, len(rt.Args))
	for i, snippet := range rt.Args {
		args[i] = make([]taggedArg, len(snippet))
		for j, arg := range snippet {
			ty := fmt.Sprintf("%T", arg)
			if _, ok := phraseArgTypes[ty]; !ok {
				return nil, fmt.Errorf("%s: phrase argument type can't be saved", ty)
			}
			v, err := json.Marshal(arg)
			if err != nil {
				return nil, err
			}
			args[i][j] = taggedArg{Type: ty, Value: v}
		}
	}

	return json.Marshal(serializedTransmission{Strings: rt.Strings, Args: args, Type: rt.Type})
}

func (rt *RadioTransmission) UnmarshalJSON(b []byte) error {
	var st serializedTransmission
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}

	rt.Strings, rt.Type = st.Strings, st.Type
	rt.Args = make([][]any, len(st.Args))
	for i, snippet := range st.Args {
		rt.Args[i] = make([]any, len(snippet))
		for j, ta := range snippet {
			decode, ok := phraseArgTypes[ta.Type]
			if !ok {
				return fmt.Errorf("%s: unknown phrase argument type", ta.Type)
			}
			arg, err := decode(ta.Value)
			if err != nil {
				return fmt.Errorf("%s: %w", ta.Type, err)
			}
			rt.Args[i][j] = arg
		}
	}
	return nil
}

// MakeContactTransmission is a helper function to make a pilot
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

// Merge takes a separately-constructed RadioTransmission and merges its
// contents with the current one.
func (rt *RadioTransmission) Merge(r *RadioTransmission) {
	rt.Strings = append(rt.Strings, r.Strings...)
	rt.Args = append(rt.Args, r.Args...)
	if r.Type == RadioTransmissionUnexpected {
		rt.Type = RadioTransmissionUnexpected
	}
}

// render formats each of the transmission's snippets with f, which is either
// PhraseFormatString's Spoken or its Written. An argument that isn't one of the
// types its directive handles is an error; since that leaves the transmission
// saying something other than what was intended, the pilot says nothing at all
// rather than transmitting a mangled instruction. The error names the phrase
// that couldn't be formatted so that the caller can report which transmission
// was lost.
func (rt RadioTransmission) render(f func(PhraseFormatString, []any) (string, error)) ([]string, error) {
	if len(rt.Strings) != len(rt.Args) {
		return nil, fmt.Errorf("mismatching len(Strings) %d and len(Args) %d", len(rt.Strings), len(rt.Args))
	}

	var result []string
	for i := range rt.Strings {
		s, err := f(rt.Strings[i], rt.Args[i])
		if err != nil {
			return nil, fmt.Errorf("%q: %w", rt.Strings[i], err)
		}
		s = strings.TrimRight(strings.TrimSpace(s), ",.")
		if s != "" {
			result = append(result, s)
		}
	}
	return result, nil
}

// Add is a convenience function to add a transmission snippet to the RadioTransmission.
// It's more or less equivalent to calling Merge(MakeRadioTransmission(...)).
func (rt *RadioTransmission) Add(s string, args ...any) {
	rt.Strings = append(rt.Strings, PhraseFormatString(s))
	rt.Args = append(rt.Args, args)
}

// Spoken returns a string corresponding to how the transmission should be
// spoken, which appropriate phonetic substitutions made (e.g. "9" ->
// "niner"). It returns an error if any of its arguments can't be formatted.
func (rt RadioTransmission) Spoken(r *rand.Rand) (string, error) {
	result, err := rt.render(func(s PhraseFormatString, args []any) (string, error) {
		return s.Spoken(r, args)
	})
	if err != nil || len(result) == 0 {
		return "", err
	}
	return strings.Join(result, ", ") + ".", nil
}

// Written returns a string corresponding to how the transmission should be
// displayed as text on the screen. It returns an error if any of its arguments
// can't be formatted.
func (rt RadioTransmission) Written(r *rand.Rand) (string, error) {
	result, err := rt.render(func(s PhraseFormatString, args []any) (string, error) {
		return s.Written(r, args)
	})
	if err != nil {
		return "", err
	}
	return strings.Join(result, ", "), nil
}

/////////////////////////////////////////////////////////////////////////////////////
// SnippetFormatter

// SnippetFormatter defines an interface for formatting short
// text snippets corresponding to various aviation-related concepts into
// both speech and text. Each is takes a single value specifying the value
// of the corresponding thing (altitude, speed, etc.)
type SnippetFormatter interface {
	// Written and Spoken return an error if the argument isn't one of the
	// types the formatter handles; RadioTransmission's Written and Spoken
	// log it and render nothing.
	Written(arg any) (string, error)
	Spoken(r *rand.Rand, arg any) (string, error)
}

var (
	// phraseFormats stores associates all of the formatting strings with
	// SnippetFormatter implementations. The map keys specify
	// the associated formatting strings.
	phraseFormats map[string]SnippetFormatter = map[string]SnippetFormatter{
		"actrl":    &ControllerSnippetFormatter{From: "Departure", To: "Approach"},
		"actype":   &AircraftTypeSnippetFormatter{},
		"airport":  &AirportSnippetFormatter{},
		"alt":      &AltSnippetFormatter{},
		"altrest":  &AltRestrictionSnippetFormatter{},
		"appr":     &ApproachSnippetFormatter{},
		"beacon":   &BeaconCodeSnippetFormatter{},
		"callsign": &CallsignSnippetFormatter{},
		"ch":       &LetterSnippetFormatter{},
		"dctrl":    &ControllerSnippetFormatter{From: "Approach", To: "Departure"},
		"fix":      &FixSnippetFormatter{},
		"freq":     &FrequencySnippetFormatter{},
		"gf":       &GroupFormSnippetFormatter{},
		"hdg":      &HeadingSnippetFormatter{},
		"num":      &BasicNumberSnippetFormatter{},
		"rwy":      &RunwaySnippetFormatter{},
		"sid":      &SIDSnippetFormatter{},
		"mach":     &MachSnippetFormatter{},
		"spd":      &SpeedSnippetFormatter{},
		"star":     &STARSnippetFormatter{},
	}
)

///////////////////////////////////////////////////////////////////////////
// PhraseFormatString

// PhraseFormatString is a string that potentially includes
type PhraseFormatString string

// NOTE: allow extra args for variants. But need 1:1 for ordering...

// format resolves the phrase's alternations and fills in its directives using
// snippet, which is either a SnippetFormatter's Written or its Spoken. It
// returns the first error any of them reported.
func (s PhraseFormatString) format(r *rand.Rand, args []any,
	snippet func(SnippetFormatter, any) (string, error)) (string, error) {
	var err error
	record := func(e error) {
		if err == nil {
			err = e
		}
	}

	var result strings.Builder
	s.resolveOptions(r, record).applyFormatting(args, func(f SnippetFormatter, arg any) {
		if err != nil {
			return
		}
		var str string
		if str, err = snippet(f, arg); err == nil {
			result.WriteString(str)
		}
	}, func(ch rune) {
		result.WriteRune(ch)
	}, record)

	return result.String(), err
}

func (s PhraseFormatString) Written(r *rand.Rand, args []any) (string, error) {
	return s.format(r, args, func(f SnippetFormatter, arg any) (string, error) {
		return f.Written(arg)
	})
}

func (s PhraseFormatString) Spoken(r *rand.Rand, args []any) (string, error) {
	return s.format(r, args, func(f SnippetFormatter, arg any) (string, error) {
		return f.Spoken(r, arg)
	})
}

// applyFormatting walks the phrase, calling format for each directive that has
// an argument, c for each literal rune, and err if the phrase runs out of
// arguments before it runs out of directives.
func (s PhraseFormatString) applyFormatting(args []any, format func(SnippetFormatter, any), c func(rune),
	err func(error)) {
	braceIndex := 0
	argIndex := 0
	foundBrace := false

	for i, ch := range s {
		if ch == '{' {
			foundBrace = true
			braceIndex = i
		} else if ch == '}' {
			foundBrace = false
			match := string(s[braceIndex+1 : i])
			if f, ok := phraseFormats[match]; ok {
				if argIndex < len(args) {
					format(f, args[argIndex])
					argIndex++
				} else {
					err(fmt.Errorf("{%s}: no argument left for it; have %d", match, len(args)))
				}
			}
		} else if !foundBrace {
			c(ch)
		}
	}
}

// given a string of the form "hello [you|there] I'm [me|myself]", returns
// a randomly sampled variant of the string, e.g. "hello there, I'm me".
func (s PhraseFormatString) resolveOptions(r *rand.Rand, err func(error)) PhraseFormatString {
	inBrackets := false
	var result, options strings.Builder

	for _, ch := range s {
		if ch == '[' {
			if inBrackets {
				err(errors.New("unclosed ["))
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
	if inBrackets {
		err(errors.New("unclosed ["))
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

// intArg converts a numeric any value to int.
func intArg(arg any) (int, error) {
	switch v := arg.(type) {
	case int:
		return v, nil
	case float32:
		return int(v), nil
	default:
		return 0, fmt.Errorf("expected int/float32 arg, got %T", arg)
	}
}

// stringArg returns a string argument.
func stringArg(arg any) (string, error) {
	if s, ok := arg.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("expected string arg, got %T", arg)
}

///////////////////////////////////////////////////////////////////////////
// AltSnippetFormatter

// AltSnippetFormatter formats altitudes, which may be given as ints or float32s.
type AltSnippetFormatter struct{}

func (a *AltSnippetFormatter) Written(arg any) (string, error) {
	alt, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return FormatAltitude(float32(alt)), nil
}

func (a *AltSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	alt, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return sayAltitude(alt, r), nil
}

///////////////////////////////////////////////////////////////////////////
// ApproachSnippetFormatter

type ApproachSnippetFormatter struct{}

func (ApproachSnippetFormatter) Written(arg any) (string, error) {
	return stringArg(arg)
}

func (ApproachSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	appr, err := stringArg(arg)
	if err != nil {
		return "", err
	}

	// Split on commas first, process each part, then rejoin with commas.
	// This handles approach names like "ILS Runway 15R, then visual approach..."
	var spokenParts []string
	for part := range strings.SplitSeq(appr, ",") {
		sp, err := spokenApproachPart(r, strings.TrimSpace(part))
		if err != nil {
			return "", err
		}
		spokenParts = append(spokenParts, sp)
	}
	return strings.Join(spokenParts, ", "), nil
}

func spokenApproachPart(r *rand.Rand, appr string) (string, error) {
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
					return "", fmt.Errorf("%s: unexpected in runway %q", string(ch), word)
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

	return strings.Join(result, " "), nil
}

///////////////////////////////////////////////////////////////////////////
// AirportSnippetFormatter

type AirportSnippetFormatter struct{}

// airportArg converts an airport argument to the id the aviation database
// keys airports by; FAA local identifiers are looked up.
func airportArg(arg any) (ICAOAirportCode, error) {
	switch v := arg.(type) {
	case ICAOAirportCode:
		return v, nil
	case FAAAirportCode:
		if icao, ok := FAAAirportToICAO(v); ok {
			return icao, nil
		}
		return ICAOAirportCode(v), nil
	default:
		return "", fmt.Errorf("expected ICAOAirportCode/FAAAirportCode arg, got %T", arg)
	}
}

func (AirportSnippetFormatter) Written(arg any) (string, error) {
	icao, err := airportArg(arg)
	if err != nil {
		return "", err
	}
	return string(icao), nil
}

var trailingParenRe = regexp.MustCompile(`^(.*) \([^)]+\)$`)

func (AirportSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	icao, err := airportArg(arg)
	if err != nil {
		return "", err
	}
	if opts, ok := DB.say.airports[string(icao)]; ok && len(opts) > 0 {
		ap, _ := rand.SampleSeq(r, slices.Values(opts))
		return ap, nil
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

		return name, nil
	} else {
		return string(icao), nil
	}
}

///////////////////////////////////////////////////////////////////////////
// RunwaySnippetFormatter

type RunwaySnippetFormatter struct{}

func (RunwaySnippetFormatter) Written(arg any) (string, error) {
	return stringArg(arg)
}

func (RunwaySnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	rwy, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	var result []string
	for _, ch := range rwy {
		switch ch {
		case 'L', 'l':
			result = append(result, "left")
		case 'R', 'r':
			result = append(result, "right")
		case 'C', 'c':
			result = append(result, "center")
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			result = append(result, sayDigit(int(ch-'0')))
		}
	}
	return strings.Join(result, " "), nil
}

///////////////////////////////////////////////////////////////////////////
// ControllerSnippetFormatter

// ControllerSnippetFormatter formats a controller's radio name, rewriting the
// position in it from From to To. A controller named "approach" is called
// "departure" when a departure is sent to it and vice versa.
type ControllerSnippetFormatter struct {
	From, To string
}

func controllerArg(arg any) (*Controller, error) {
	ctrl, ok := arg.(*Controller)
	if !ok {
		return nil, fmt.Errorf("expected *Controller arg, got %T", arg)
	}
	if ctrl == nil {
		return nil, errors.New("nil *Controller arg")
	}
	return ctrl, nil
}

func shortenController(n string) string {
	n = strings.ToLower(n)
	for _, pos := range []string{"tower", "departure", "approach", "center"} {
		if strings.Contains(n, pos) {
			return pos
		}
	}
	return n
}

func (c ControllerSnippetFormatter) Written(arg any) (string, error) {
	ctrl, err := controllerArg(arg)
	if err != nil {
		return "", err
	}
	n := strings.ReplaceAll(ctrl.RadioName, c.From, c.To)
	return strings.ReplaceAll(n, strings.ToLower(c.From), strings.ToLower(c.To)), nil
}

func (c ControllerSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	n, err := c.Written(arg)
	if err != nil {
		return "", err
	}
	if r.Bool() {
		return shortenController(n), nil
	}
	return n, nil
}

///////////////////////////////////////////////////////////////////////////
// MachSnippetFormatter formats mach numbers (e.g., 0.75 → "mach .75" written, "mach point seven five" spoken)

type MachSnippetFormatter struct{}

func (MachSnippetFormatter) Written(arg any) (string, error) {
	mach, err := machIntArg(arg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mach .%d", mach), nil
}

func (MachSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	mach, err := machIntArg(arg)
	if err != nil {
		return "", err
	}
	return "mach point " + sayDigits(mach, 2), nil
}

// machIntArg converts a mach argument to an integer (e.g., 0.75 → 75).
// int values are used directly; float values are multiplied by 100.
func machIntArg(arg any) (int, error) {
	switch v := arg.(type) {
	case int:
		return v, nil
	case float32:
		return int(v * 100), nil
	default:
		return 0, fmt.Errorf("expected int/float32 arg, got %T", arg)
	}
}

///////////////////////////////////////////////////////////////////////////
// SpeedSnippetFormatter

type SpeedSnippetFormatter struct{}

func (SpeedSnippetFormatter) Written(arg any) (string, error) {
	spd, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d knots", spd), nil
}

func (SpeedSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	spd, err := intArg(arg)
	if err != nil {
		return "", err
	}

	knots := util.Select(r.Bool(), " knots", "")
	if r.Bool() {
		return sayDigits(spd, 0) + knots, nil
	}
	return groupForm(spd) + knots, nil
}

///////////////////////////////////////////////////////////////////////////
// FixSnippetFormatter

type FixSnippetFormatter struct{}

func (FixSnippetFormatter) Written(arg any) (string, error) {
	fix, err := stringArg(arg)
	if err != nil {
		return "", err
	}

	if strings.HasPrefix(fix, "_") {
		if namedFix, dist, dir, ok := ParseSyntheticCrossingFix(fix); ok {
			return fmt.Sprintf("%d miles %s of %s", dist, math.Compass(dir.Heading()), namedFix), nil
		}
		if _, dist, ok := ParseSyntheticDMEFix(fix); ok {
			return fmt.Sprintf("%d D M E", dist), nil
		}
	}

	// Cut off any trailing bits like COLIN.JT
	fix, _, _ = strings.Cut(fix, ".")

	if aid, ok := DB.Navaids[fix]; ok {
		return util.StopShouting(aid.Name), nil
	} else if ap, ok := DB.Airports[ICAOAirportCode(fix)]; ok {
		return ap.Name, nil
	}
	return fix, nil
}

func (f FixSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	fix, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	return GetFixTelephony(fix), nil
}

///////////////////////////////////////////////////////////////////////////
// HeadingSnippetFormatter

type HeadingSnippetFormatter struct{}

func headingArg(arg any) (int, error) {
	switch v := arg.(type) {
	case int:
		return v, nil
	case float32:
		return int(v), nil
	case math.MagneticHeading:
		return int(v), nil
	default:
		return 0, fmt.Errorf("expected int/float32/MagneticHeading arg, got %T", arg)
	}
}

func (HeadingSnippetFormatter) Written(arg any) (string, error) {
	hdg, err := headingArg(arg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%03d", hdg), nil
}

func (HeadingSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	hdg, err := headingArg(arg)
	if err != nil {
		return "", err
	}

	if r.Bool() || hdg < 100 {
		return sayDigits(hdg, 3), nil
	}
	return groupForm(hdg), nil
}

///////////////////////////////////////////////////////////////////////////
// BasicNumberSnippetFormatter

type BasicNumberSnippetFormatter struct{}

func (BasicNumberSnippetFormatter) Written(arg any) (string, error) {
	n, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(n), nil
}

func (BasicNumberSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	n, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(n), nil
}

///////////////////////////////////////////////////////////////////////////
// Callsign utilities

// GetACTypePronunciations returns all pronunciation variants for an aircraft type.
// For example, "C172" might return ["skyhawk", "cessna one seventy-two"].
// Returns nil if the type is not found in sayactype.json.
func GetACTypePronunciations(acType string) []string {
	if variants, ok := DB.say.acTypes[acType]; ok {
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

// GetCallsignSpoken returns the spoken telephony string for a callsign, formatted as it would be
// pronounced. It is the speech-to-text spelling: it feeds the whisper initial prompt and keys the
// command parser's aircraft context, so it uses the plain telephony name rather than
// the pronunciation files' voice-synthesis respellings.
// Example: "JBU520" → "jetblue five 20", "BAW22J" → "speedbird 22 juliet"
func GetCallsignSpoken(callsign string, cwtCategory string) string {
	prefix, fnum := SplitCallsign(callsign)

	// GA N-numbers: spell out character by character
	if prefix == "N" {
		var s []string
		for _, ch := range callsign {
			if ch >= '0' && ch <= '9' {
				s = append(s, sayDigit(int(ch-'0')))
			} else {
				s = append(s, strings.ToLower(spokenLetters[string(ch)]))
			}
		}
		return strings.Join(s, " ")
	}

	// Extract trailing letters from flight number (e.g., "22J" → suffix=" juliet")
	var suffix strings.Builder
	if suffixIdx := strings.IndexAny(fnum, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); suffixIdx != -1 {
		for _, ch := range fnum[suffixIdx:] {
			suffix.WriteString(" " + strings.ToLower(spokenLetters[string(ch)]))
		}
		fnum = fnum[:suffixIdx]
	}

	tel := DB.Callsigns[prefix]

	// Build result with spoken flight number
	result := strings.TrimSpace(tel + " " + sayFlightNumber(fnum) + suffix.String())

	// Add heavy/super suffix
	if cwtCategory == "A" {
		result += " super"
	} else if len(cwtCategory) > 0 && cwtCategory[0] <= 'D' {
		result += " heavy"
	}

	return result
}

// GetFixTelephony returns the spoken name for a fix (navaid, airport, or waypoint).
// It uses pronunciations from sayfix.json when available, falls back to database
// lookups for navaids/airports, and uses StopShouting for other fixes.
func GetFixTelephony(fix string) string {
	if strings.HasPrefix(fix, "_") {
		if namedFix, dist, dir, ok := ParseSyntheticCrossingFix(fix); ok {
			return fmt.Sprintf("%d miles %s of %s", dist, math.Compass(dir.Heading()), GetFixTelephony(namedFix))
		}
		if _, dist, ok := ParseSyntheticDMEFix(fix); ok {
			return fmt.Sprintf("%d D M E", dist)
		}
	}

	// Cut off any trailing bits like COLIN.JT
	fix, _, _ = strings.Cut(fix, ".")

	if say, ok := DB.say.fixes[fix]; ok {
		return say
	}

	// For 3-char fixes or 4-char ICAO codes (VORs, airports), use the full name
	if len(fix) == 3 || len(fix) == 4 {
		if aid, ok := DB.Navaids[fix]; ok {
			return util.StopShouting(aid.Name)
		} else if ap, ok := DB.Airports[ICAOAirportCode(fix)]; ok {
			return ap.Name
		}
	}

	// Fall back to StopShouting for readability
	return util.StopShouting(fix)
}

// GetAirportTelephonyVariants returns all spoken name variants for an airport.
// This is used by STT for matching spoken airport names to ICAO codes.
// Returns all variants from sayairport.json if available, otherwise returns
// a slice with just the database name (if available), or nil if not found.
func GetAirportTelephonyVariants(icao string) []string {
	// First check sayairport.json for custom variants
	if variants, ok := DB.say.airports[icao]; ok && len(variants) > 0 {
		return variants
	}

	// Fall back to database name
	if ap, ok := DB.Airports[ICAOAirportCode(icao)]; ok && ap.Name != "" {
		// Strip common suffixes that wouldn't typically be said
		name := ap.Name
		for _, extra := range []string{"Airport", "Air Field", "Field", "Strip", "Airstrip", "International", "Regional"} {
			name = strings.TrimSuffix(name, " "+extra)
		}
		if name != "" {
			return []string{name}
		}
	}

	return nil
}

// GetSIDTelephony returns the spoken form of a SID name.
// For example, "MERIT5" becomes "merit five".
func GetSIDTelephony(sid string) string {
	name, num := trimNumber(sid)
	if say, ok := DB.say.sids[name]; ok {
		name = say
	}
	if num > 0 {
		return name + " " + sayDigit(num)
	}
	return name
}

// GetSTARTelephony returns the spoken form of a STAR name.
// For example, "CAMRN4" becomes "cameron four".
func GetSTARTelephony(star string) string {
	name, num := trimNumber(star)
	if say, ok := DB.say.stars[name]; ok {
		name = say
	}
	if num > 0 {
		return name + " " + sayDigit(num)
	}
	return name
}

// GetApproachTelephony returns the spoken form of an approach name.
// For example, "RNAV X Runway 22L" becomes "r-nav x-ray runway two two left".
func GetApproachTelephony(approach string) string {
	types, runway := ApproachTelephonyComponents(approach)
	if runway != "" {
		types = append(types, "runway", runway)
	}
	return strings.Join(types, " ")
}

// ApproachTelephonyComponents returns the spoken units of an approach name separately: the
// type-related words (e.g., "I L S", "r-nav", "x-ray") and the spoken runway (e.g., "two two
// left"; empty if the name doesn't include one).
func ApproachTelephonyComponents(approach string) (types []string, runway string) {
	var rwy []string
	lastRunway := false

	for word := range strings.FieldsSeq(approach) {
		lower := strings.ToLower(word)
		// The runway's side may be spelled out as its own word ("Runway 4
		// Right"); it belongs with the runway, not ahead of it.
		if len(rwy) > 0 && (lower == "left" || lower == "right" || lower == "center") {
			rwy = append(rwy, lower)
			continue
		}
		if lastRunway {
			// Handle runway number and suffix (e.g., "22L")
			for _, ch := range lower {
				switch ch {
				case 'l':
					rwy = append(rwy, "left")
				case 'r':
					rwy = append(rwy, "right")
				case 'c':
					rwy = append(rwy, "center")
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					rwy = append(rwy, sayDigit(int(ch-'0')))
				}
			}
			lastRunway = false
		} else {
			upper := strings.ToUpper(word)
			if lower == "runway" {
				lastRunway = true
			} else if upper == "ILS" {
				types = append(types, "I L S")
			} else if upper == "RNAV" {
				types = append(types, "r-nav")
			} else if upper == "VOR" {
				types = append(types, "V O R")
			} else if upper == "GPS" {
				types = append(types, "G P S")
			} else if upper == "LOC" {
				types = append(types, "localizer")
			} else if upper == "LDA" {
				types = append(types, "L D A")
			} else if upper == "NDB" {
				types = append(types, "N D B")
			} else if sp, ok := spokenLetters[upper]; ok {
				types = append(types, sp)
			} else {
				types = append(types, word)
			}
		}
	}

	return types, strings.Join(rwy, " ")
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

func (CallsignSnippetFormatter) Written(arg any) (string, error) {
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
		return "", fmt.Errorf("expected CallsignArg/GACallsignArg arg, got %T", arg)
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
			return cs, nil
		}
		return callsign, nil
	}

	cs := DB.Callsigns[icao] + " " + fnum

	if isEmergency {
		cs += " (emergency)"
	}

	return cs, nil
}

func (CallsignSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
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
		return "", fmt.Errorf("expected CallsignArg/GACallsignArg arg, got %T", arg)
	}

	icao, fnum := SplitCallsign(callsign)

	if icao == "N" {
		// For GA callsigns with type form, use aircraft type + trailing 3
		if useTypeForm && acType != "" {
			typeVariants := DB.say.acTypes[acType]
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
					return typeSpoken + " " + trailing3, nil
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
		return strings.Join(s, " "), nil
	}

	// peel off any trailing letters
	var suffix strings.Builder
	if suffixIdx := strings.IndexAny(fnum, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); suffixIdx != -1 {
		for _, ch := range fnum[suffixIdx:] {
			suffix.WriteString(" " + spokenLetters[string(ch)])
		}
		fnum = fnum[:suffixIdx]
	}

	// figure out the telephony
	tel := DB.Callsigns[icao]
	if tel2, ok := DB.say.airlines[tel]; ok { // overrides
		tel = tel2
	}

	// For non-emergency aircraft reading back instructions, sometimes
	// skip the ICAO identifier and just say the flight number.
	// (Disabled: set to 0% to always include the full callsign.)
	if !isEmergency && !alwaysFullCallsign && r.Float32() < 0 {
		tel = ""
	}

	return strings.TrimSpace(tel + " " + sayFlightNumber(fnum) + suffix.String()), nil
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
		var s strings.Builder
		for _, d := range id {
			s.WriteString(sayDigit(int(d-'0')) + " ")
		}
		return s.String()
	}
}

///////////////////////////////////////////////////////////////////////////
// LetterSnippetFormatter

type LetterSnippetFormatter struct{}

// letterArg returns a single-character A-Z argument.
func letterArg(arg any) (string, error) {
	s, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	if len(s) != 1 || s[0] < 'A' || s[0] > 'Z' {
		return "", fmt.Errorf("expected a single A-Z character, got %q", s)
	}
	return s, nil
}

func (LetterSnippetFormatter) Written(arg any) (string, error) {
	return letterArg(arg)
}

var spokenLetters = map[string]string{
	"A": "alpha", "B": "brahvo", "C": "charlie", "D": "delta",
	"E": "echo", "F": "foxtrot", "G": "golf", "H": "hotel", "I": "India",
	"J": "Juliet", "K": "Kilo", "L": "Lima", "M": "mike", "N": "November",
	"O": "Oscar", "P": "Pahpah", "Q": "Kebeck", "R": "Romeo", "S": "Sierra",
	"T": "tango", "U": "uniform", "V": "victor", "W": "whiskey", "X": "x-ray",
	"Y": "yankee", "Z": "zulu",
}

// NATOPhonetic maps uppercase letters to their standard NATO phonetic alphabet words.
var NATOPhonetic = map[string]string{
	"A": "alpha", "B": "bravo", "C": "charlie", "D": "delta",
	"E": "echo", "F": "foxtrot", "G": "golf", "H": "hotel",
	"I": "india", "J": "juliet", "K": "kilo", "L": "lima",
	"M": "mike", "N": "november", "O": "oscar", "P": "papa",
	"Q": "quebec", "R": "romeo", "S": "sierra", "T": "tango",
	"U": "uniform", "V": "victor", "W": "whiskey", "X": "x-ray",
	"Y": "yankee", "Z": "zulu",
}

func (LetterSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	ch, err := letterArg(arg)
	if err != nil {
		return "", err
	}
	return spokenLetters[ch], nil
}

///////////////////////////////////////////////////////////////////////////
// SIDSnippetFormatter

type SIDSnippetFormatter struct{}

func (s SIDSnippetFormatter) Written(arg any) (string, error) {
	return stringArg(arg)
}

func (SIDSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	name, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	sid, num := trimNumber(name)
	if say, ok := DB.say.sids[sid]; ok {
		return say + " " + sayDigit(num), nil
	}
	return sid + " " + sayDigit(num), nil
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

func (s STARSnippetFormatter) Written(arg any) (string, error) {
	return stringArg(arg)
}

func (STARSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	name, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	star, num := trimNumber(name)
	if say, ok := DB.say.stars[star]; ok {
		return say + " " + sayDigit(num), nil
	}
	return star + " " + sayDigit(num), nil
}

///////////////////////////////////////////////////////////////////////////
// FrequencySnippetFormatter

type FrequencySnippetFormatter struct{}

func frequencyArg(arg any) (Frequency, error) {
	if f, ok := arg.(Frequency); ok {
		return f, nil
	}
	return 0, fmt.Errorf("expected Frequency arg, got %T", arg)
}

func (FrequencySnippetFormatter) Written(arg any) (string, error) {
	f, err := frequencyArg(arg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%03d.%02d", f/1000, (f%1000)/10), nil
}

func (FrequencySnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	freq, err := frequencyArg(arg)
	if err != nil {
		return "", err
	}
	f := int(freq)
	whole := (f / 1000) % 100
	frac := (f % 1000) / 10 // Two digits after decimal

	switch r.Intn(4) {
	case 0:
		// Two digit pairs: "twenty-three forty-five" or "twenty-eight twenty"
		return fmt.Sprintf("%d %s", whole, sayFrequencyPair(frac)), nil
	case 1:
		// With "one" prefix: "one twenty-three point forty-five" or "one twenty-eight point two"
		return fmt.Sprintf("one %d point %s", whole, sayFrequencyPoint(frac)), nil
	case 2:
		// Without "one": "twenty-three point forty-five" or "twenty-eight point two"
		return fmt.Sprintf("%d point %s", whole, sayFrequencyPoint(frac)), nil
	default:
		// Digit by digit: "one two three point four five"
		return sayDigits(f/1000, 3) + " point " + sayFrequencyDigits(frac), nil
	}
}

// The following return the spoken form of the two digits after a frequency's
// decimal point, given frac in [0,99]; each of the forms used above says them
// differently. Note that all of them must handle frac < 10, where the leading
// zero is significant: 127.05 is "point zero five", not "point five".

// sayFrequencyPair returns the digits as the second half of a digit pair:
// "twenty-three forty-five", "twenty-three zero five".
func sayFrequencyPair(frac int) string {
	if frac == 0 {
		return "zero"
	} else if frac < 10 {
		return "zero " + sayDigit(frac)
	}
	return strconv.Itoa(frac)
}

// sayFrequencyPoint returns the digits as a number following "point", where a
// lone trailing zero is dropped: "point forty-five", "point niner".
func sayFrequencyPoint(frac int) string {
	if frac >= 10 && frac%10 == 0 {
		return strconv.Itoa(frac / 10)
	}
	return sayFrequencyPair(frac)
}

// sayFrequencyDigits returns the digits individually, as in the digit by digit
// form: "point four five", "point zero five", "point niner".
func sayFrequencyDigits(frac int) string {
	if frac%10 == 0 {
		return sayDigit(frac / 10)
	}
	return sayDigits(frac, 2)
}

///////////////////////////////////////////////////////////////////////////
// GroupFormSnippetFormatter

type GroupFormSnippetFormatter struct{}

func (GroupFormSnippetFormatter) Written(arg any) (string, error) {
	n, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(n), nil
}

func (GroupFormSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	n, err := intArg(arg)
	if err != nil {
		return "", err
	}
	return groupForm(n), nil
}

///////////////////////////////////////////////////////////////////////////
// BeaconCodeSnippetFormatter

type BeaconCodeSnippetFormatter struct{}

func squawkArg(arg any) (Squawk, error) {
	if sq, ok := arg.(Squawk); ok {
		return sq, nil
	}
	return 0, fmt.Errorf("expected Squawk arg, got %T", arg)
}

func (BeaconCodeSnippetFormatter) Written(arg any) (string, error) {
	sq, err := squawkArg(arg)
	if err != nil {
		return "", err
	}
	return sq.String(), nil
}

func (BeaconCodeSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	sq, err := squawkArg(arg)
	if err != nil {
		return "", err
	}
	s := sq.String()
	if r.Bool() {
		return s[:2] + " " + s[2:], nil
	}
	return sayDigit(int(s[0]-'0')) + " " + sayDigit(int(s[1]-'0')) + " " +
		sayDigit(int(s[2]-'0')) + " " + sayDigit(int(s[3]-'0')), nil
}

///////////////////////////////////////////////////////////////////////////
// AircraftTypeSnippetFormatter

type AircraftTypeSnippetFormatter struct{}

func (AircraftTypeSnippetFormatter) Written(arg any) (string, error) {
	ac, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	return DB.AircraftTypeAliases[ac] + "(" + ac + ")", nil
}

func (AircraftTypeSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	ac, err := stringArg(arg)
	if err != nil {
		return "", err
	}
	if say, ok := DB.say.acTypes[ac]; ok && len(say) > 0 {
		s, _ := rand.SampleSeq(r, slices.Values(say))
		return s, nil
	}
	return DB.AircraftTypeAliases[ac], nil
}

///////////////////////////////////////////////////////////////////////////
// AltRestrictionSnippetFormatter

type AltRestrictionSnippetFormatter struct{}

func altRestrictionArg(arg any) (AltitudeRestriction, error) {
	switch v := arg.(type) {
	case AltitudeRestriction:
		return v, nil
	case *AltitudeRestriction:
		return *v, nil
	default:
		return AltitudeRestriction{}, fmt.Errorf("expected [*]AltitudeRestriction arg, got %T", arg)
	}
}

func (AltRestrictionSnippetFormatter) Written(arg any) (string, error) {
	ar, err := altRestrictionArg(arg)
	if err != nil {
		return "", err
	}

	if ar.Range[0] != 0 {
		if ar.Range[1] == ar.Range[0] {
			return "at " + FormatAltitude(ar.Range[0]), nil
		} else if ar.Range[1] != MaxAltitude {
			return "between " + FormatAltitude(ar.Range[0]) + " and " + FormatAltitude(ar.Range[1]), nil
		}
		return "at or above " + FormatAltitude(ar.Range[0]), nil
	} else if ar.Range[1] != 0 && ar.Range[1] != MaxAltitude {
		return "at or below " + FormatAltitude(ar.Range[1]), nil
	}
	return "", nil
}

func (AltRestrictionSnippetFormatter) Spoken(r *rand.Rand, arg any) (string, error) {
	ar, err := altRestrictionArg(arg)
	if err != nil {
		return "", err
	}

	if ar.Range[0] != 0 {
		if ar.Range[1] == ar.Range[0] {
			return "at " + sayAltitude(int(ar.Range[0]), r), nil
		} else if ar.Range[1] != MaxAltitude {
			return "between " + sayAltitude(int(ar.Range[0]), r) + " and " + sayAltitude(int(ar.Range[1]), r), nil
		}
		return "at or above " + sayAltitude(int(ar.Range[0]), r), nil
	} else if ar.Range[1] != 0 && ar.Range[1] != MaxAltitude {
		return "at or below " + sayAltitude(int(ar.Range[1]), r), nil
	}
	return "", nil
}
