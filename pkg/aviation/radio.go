// pkg/speech/format.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

var (
	sayAirportMap map[string][]string
	sayACTypeMap  map[string][]string
	sayFixMap     map[string]string
	sayAirlineMap map[string]string
	saySIDMap     map[string]string
	saySTARMap    map[string]string
)

func init() {
	n := 0
	report := func(file string, err error) {
		fmt.Printf("%s: %v\n", file, err)
		n++
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
}

///////////////////////////////////////////////////////////////////////////

type RadioTransmissionType int

const (
	RadioTransmissionContact    = iota // Messages initiated by the pilot
	RadioTransmissionReadback          // Reading back an instruction
	RadioTransmissionUnexpected        // Something urgent or unusual
)

func (r RadioTransmissionType) String() string {
	switch r {
	case RadioTransmissionContact:
		return "contact"
	case RadioTransmissionReadback:
		return "readback"
	case RadioTransmissionUnexpected:
		return "urgent"
	default:
		return "(unhandled type)"
	}
}

type RadioTransmission struct {
	Controller  string
	WrittenText string
	SpokenText  string
	Type        RadioTransmissionType
}

// Frequencies are scaled by 1000 and then stored in integers.
type Frequency int

func NewFrequency(f float32) Frequency {
	// 0.5 is key for handling rounding!
	return Frequency(f*1000 + 0.5)
}

func (f Frequency) String() string {
	s := fmt.Sprintf("%03d.%03d", f/1000, f%1000)
	for len(s) < 7 {
		s += "0"
	}
	return s
}

///////////////////////////////////////////////////////////////////////////
// PilotTransmission

type PilotTransmission struct {
	Strings    []PhraseFormatString
	Args       [][]any
	Unexpected bool // should it be highlighted in the UI
}

func MakePilotTransmission(s string, args ...any) PilotTransmission {
	pr := PilotTransmission{}
	pr.Add(s, args...)
	return pr
}

func MakeUnexpectedPilotTransmission(s string, args ...any) PilotTransmission {
	pr := PilotTransmission{Unexpected: true}
	pr.Add(s, args...)
	return pr
}

func (pr *PilotTransmission) Merge(p PilotTransmission) {
	pr.Strings = append(pr.Strings, p.Strings...)
	pr.Args = append(pr.Args, p.Args...)
	pr.Unexpected = pr.Unexpected || p.Unexpected
}

// given a string of the form "hello [you|there] I'm [me|myself]", resolveStringOptions
// returns a randomly sampled variant of the string, e.g. "hello there, I'm me".
func resolveStringOptions(s string, r *rand.Rand) string {
	inBrackets := false
	var result, options strings.Builder
	for _, ch := range s {
		if ch == '[' {
			if inBrackets {
				panic("unclosed [")
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
		panic("unclosed [")
	}

	return result.String()
}

func (pr *PilotTransmission) Validate(lg *log.Logger) bool {
	return true
}

func (pr *PilotTransmission) Add(s string, args ...any) {
	pr.Strings = append(pr.Strings, PhraseFormatString(s))
	pr.Args = append(pr.Args, args)
}

func (pr PilotTransmission) Spoken(r *rand.Rand) string {
	var result []string

	for i := range pr.Strings {
		s := pr.Strings[i].Spoken(r, pr.Args[i]...)
		result = append(result, s)
	}

	return strings.Join(result, " ")
}

func (pr PilotTransmission) Written(r *rand.Rand) string {
	var result []string

	for i := range pr.Strings {
		s := pr.Strings[i].Written(r, pr.Args[i]...)
		result = append(result, s)
	}

	return strings.Join(result, ", ")
}

/////////////////////////////////////////////////////////////////////////////////////
// PhraseFormatter

type PhraseFormatter interface {
	Written(arg any) string
	Spoken(r *rand.Rand, arg any) string
	Validate(arg any) error
}

var (
	fmtRE = regexp.MustCompile(`\{(.*?)\}`)

	phraseFormats map[string]PhraseFormatter = map[string]PhraseFormatter{
		"actrl":    &AppControllerPhraseFormatter{},
		"actype":   &AircraftTypePhraseFormatter{},
		"airport":  &AirportPhraseFormatter{},
		"alt":      &AltPhraseFormatter{},
		"appr":     &ApproachPhraseFormatter{},
		"beacon":   &BeaconCodePhraseFormatter{},
		"callsign": &CallsignPhraseFormatter{},
		"ch":       &LetterPhraseFormatter{},
		"dctrl":    &DepControllerPhraseFormatter{},
		"fix":      &FixPhraseFormatter{},
		"freq":     &FrequencyPhraseFormatter{},
		"gf":       &GroupFormPhraseFormatter{},
		"hdg":      &HeadingPhraseFormatter{},
		"num":      &BasicNumberPhraseFormatter{},
		"sid":      &SIDPhraseFormatter{},
		"spd":      &SpeedPhraseFormatter{},
		"star":     &STARPhraseFormatter{},
	}
)

///////////////////////////////////////////////////////////////////////////
// PhraseFormatString

type PhraseFormatString string

// NOTE: allow extra args for variants. But need 1:1 for ordering...

func (s PhraseFormatString) Written(r *rand.Rand, args ...any) string {
	s = s.resolveOptions(r)

	i := 0
	var result bytes.Buffer
	s.resolveFormatting(func(fmt PhraseFormatter) {
		result.WriteString(fmt.Written(args[i]))
		i++
	}, func(ch rune) {
		result.WriteRune(ch)
	})
	return result.String()
}

func (s PhraseFormatString) Spoken(r *rand.Rand, args ...any) string {
	s = s.resolveOptions(r)

	var result bytes.Buffer
	arg := 0
	s.resolveFormatting(func(f PhraseFormatter) {
		if args[arg] == nil {
			result.WriteRune('\x00') // will split on this in calling code (should only happen in GetAllSpeechSnippets()...)
		} else {
			result.WriteString(f.Spoken(r, args[arg]))
		}
		arg++
	}, func(ch rune) {
		result.WriteRune(ch)
	})

	return result.String()
}

func (s PhraseFormatString) resolveFormatting(fmt func(PhraseFormatter), c func(rune)) {
	braceIndex := 0
	foundBrace := false

	for i, ch := range s {
		if ch == '{' {
			if foundBrace {
				panic("unclosed {")
			}
			foundBrace = true
			braceIndex = i
		} else if ch == '}' {
			if !foundBrace {
				panic("mismatched }")
			}
			foundBrace = false
			match := string(s[braceIndex+1 : i])
			if f, ok := phraseFormats[match]; ok {
				fmt(f)
			} else {
				panic("unhandled format: " + match)
			}
		} else if !foundBrace {
			c(ch)
		}
	}
}

func (s PhraseFormatString) resolveOptions(r *rand.Rand) PhraseFormatString {
	return PhraseFormatString(resolveStringOptions(string(s), r))
}

///////////////////////////////////////////////////////////////////////////
// General "saying things" utilities...

func sayDigit(n int) string {
	return []string{"zero", "one", "two", "three", "four", "five", "six",
		"seven", "eight", "niner"}[n]
}

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

func groupForm(v int) string {
	var gf []string
	if v == 100 {
		return "one hundred"
	}
	if v > 100 {
		gf = append(gf, fmt.Sprintf("%d", v/100))
		v %= 100
		if v >= 10 {
			gf = append(gf, fmt.Sprintf("%d", v))
		} else {
			gf = append(gf, "zero", fmt.Sprintf("%d", v))
		}
	} else {
		gf = append(gf, fmt.Sprintf("%d", v))
	}
	return strings.Join(gf, " ")
}

///////////////////////////////////////////////////////////////////////////
// AltPhraseFormatter

type AltPhraseFormatter struct{}

func (a *AltPhraseFormatter) Written(arg any) string {
	if alt, ok := arg.(float32); ok {
		return FormatAltitude(alt)
	} else if alt, ok := arg.(int); ok {
		return FormatAltitude(float32(alt))
	} else {
		return "???"
	}
}

func (a *AltPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	alt, ok := arg.(int)
	if !ok {
		alt = int(arg.(float32))
	}

	alt = 100 * (alt / 100) // round to 100s
	if alt >= 18000 {
		// flight levels
		fl := alt / 100
		if r.Bool() {
			return "flight level " + sayDigits(fl, 0)
		} else {
			// group form
			hu, tens := fl/100, fl%100
			return fmt.Sprintf("flight level %d %d", hu, tens)
		}
	} else if alt < 1000 {
		return fmt.Sprintf("%d hundred", alt/100)
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

func (a *AltPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(float32); !ok {
		if _, ok := arg.(int); !ok {
			return fmt.Errorf("expected int or float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// ApproachPhraseFormatter

type ApproachPhraseFormatter struct{}

func (ApproachPhraseFormatter) Written(arg any) string {
	return arg.(string)
}

func (ApproachPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	appr := arg.(string)

	var result []string
	lastRunway := false
	for _, word := range strings.Fields(appr) {
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

func (ApproachPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AirportPhraseFormatter

type AirportPhraseFormatter struct{}

func (AirportPhraseFormatter) Written(arg any) string {
	return arg.(string)
}

var trailingParenRe = regexp.MustCompile(`^(.*) \([^)]+\)$`)

func (AirportPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
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

func (AirportPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// DepControllerPhraseFormatter

type DepControllerPhraseFormatter struct{}

func shortenController(n string) string {
	n = strings.ToLower(n)
	for _, pos := range []string{"tower", "departure", "approach", "center"} {
		if strings.Contains(n, pos) {
			return pos
		}
	}
	return n
}

func (DepControllerPhraseFormatter) Written(arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Approach", "Departure")
	n = strings.ReplaceAll(n, "approach", "departure")
	return n
}

func (DepControllerPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Approach", "Departure")
	n = strings.ReplaceAll(n, "approach", "departure")
	if r.Bool() {
		return shortenController(n)
	} else {
		return n
	}
}

func (DepControllerPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(*Controller); !ok {
		return fmt.Errorf("expected *Controller arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AppControllerPhraseFormatter

type AppControllerPhraseFormatter struct{}

func (AppControllerPhraseFormatter) Written(arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Departure", "Approach")
	n = strings.ReplaceAll(n, "departure", "approach")
	return n
}

func (AppControllerPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	n := arg.(*Controller).RadioName
	n = strings.ReplaceAll(n, "Departure", "Approach")
	n = strings.ReplaceAll(n, "departure", "approach")
	if r.Bool() {
		return shortenController(n)
	} else {
		return n
	}
}

func (AppControllerPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(*Controller); !ok {
		return fmt.Errorf("expected *Controller arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SpeedPhraseFormatter

type SpeedPhraseFormatter struct{}

func (SpeedPhraseFormatter) Written(arg any) string {
	spd, ok := arg.(int)
	if !ok {
		spd = int(arg.(float32))
	}
	return fmt.Sprintf("%d knots", spd)
}

func (SpeedPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
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

func (SpeedPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int or float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// FixPhraseFormatter

type FixPhraseFormatter struct{}

func (FixPhraseFormatter) Written(arg any) string {
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

func (f FixPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	fix := arg.(string)
	// Cut off any trailing bits like COLIN.JT
	fix, _, _ = strings.Cut(fix, ".")

	if len(fix) == 3 || (len(fix) == 4 && fix[0] == 'K') { // VOR, airport, etc.
		return f.Written(fix)
	} else if say, ok := sayFixMap[fix]; ok {
		return say
	} else {
		return fix // #yolo
	}
}

func (FixPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// HeadingPhraseFormatter

type HeadingPhraseFormatter struct{}

func (HeadingPhraseFormatter) Written(arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return fmt.Sprintf("%03d", hdg)
}

func (HeadingPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
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

func (HeadingPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int/float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// BasicNumberPhraseFormatter

type BasicNumberPhraseFormatter struct{}

func (BasicNumberPhraseFormatter) Written(arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return strconv.Itoa(hdg)
}

func (BasicNumberPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	hdg, ok := arg.(int)
	if !ok {
		hdg = int(arg.(float32))
	}
	return strconv.Itoa(hdg)
}

func (BasicNumberPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		if _, ok := arg.(float32); !ok {
			return fmt.Errorf("expected int/float32 arg, got %T", arg)
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// CallsignPhraseFormatter

type CallsignPhraseFormatter struct{}

func (CallsignPhraseFormatter) Written(arg any) string {
	ac := arg.(Aircraft)
	idx := strings.IndexAny(string(ac.ADSBCallsign), "0123456789")
	icao, fnum := string(ac.ADSBCallsign[:idx]), string(ac.ADSBCallsign[idx:])
	if icao == "N" {
		return string(ac.ADSBCallsign)
	}

	cs := DB.Callsigns[icao] + " " + fnum + heavySuper(ac)

	return cs
}

func (CallsignPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	ac := arg.(Aircraft)
	idx := strings.IndexAny(string(ac.ADSBCallsign), "0123456789")
	icao, fnum := string(ac.ADSBCallsign[:idx]), string(ac.ADSBCallsign[idx:])

	if icao == "N" {
		var s []string
		for _, ch := range ac.ADSBCallsign {
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
	if idx = strings.IndexAny(fnum, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"); idx != -1 {
		for _, ch := range fnum[idx:] {
			suffix += " " + spokenLetters[string(ch)]
		}
		fnum = fnum[:idx]
	}

	// figure out the telephony
	tel := DB.Callsigns[icao]
	if tel2, ok := sayAirlineMap[tel]; ok { // overrides
		tel = tel2
	}

	cs := tel + " " + sayFlightNumber(fnum) + suffix
	if r.Bool() {
		// Pilots don't always read back heavy/super
		cs += heavySuper(ac)
	}

	return cs
}

func heavySuper(ac Aircraft) string {
	if perf, ok := DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok {
		if perf.WeightClass == "H" {
			return " heavy"
		} else if perf.WeightClass == "J" {
			return " super"
		}
	}
	return ""
}

func sayFlightNumber(id string) string {
	fnum, suffix := id, ""
	idx := strings.IndexAny(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if idx != -1 {
		fnum = id[:idx]
		suffix = id[idx:]
	}

	do0 := func(s string) string {
		if len(s) == 2 && s[0] == '0' {
			return sayDigit(int(s[0]-'0')) + " " + sayDigit(int(s[1]-'0'))
		}
		return s
	}

	if len(fnum) == 2 {
		fnum = do0(fnum)
	} else if len(fnum) == 3 {
		if fnum == "100" {
			fnum = "1 hundred"
		}
		fnum = string(fnum[0]) + " " + do0(fnum[1:])
	} else if len(fnum) == 4 {
		fnum = do0(fnum[:2]) + " " + do0(fnum[2:])
	}

	if suffix != "" {
		// add spaces if there are multiple characters
		suffix = strings.Join(strings.Split(suffix, ""), " ")
		fnum += " " + suffix
	}

	return fnum
}

func (CallsignPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(Aircraft); !ok {
		return fmt.Errorf("expected *Aircraft arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// LetterPhraseFormatter

type LetterPhraseFormatter struct{}

func (LetterPhraseFormatter) Written(arg any) string {
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

func (LetterPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	return spokenLetters[arg.(string)]
}

func (LetterPhraseFormatter) Validate(arg any) error {
	if s, ok := arg.(string); !ok || len(s) != 1 || (s[0] < 'A' || s[0] > 'Z') {
		return fmt.Errorf("expected single-character string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SIDPhraseFormatter

type SIDPhraseFormatter struct{}

func (s SIDPhraseFormatter) Written(arg any) string {
	return arg.(string)
}

func (SIDPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	sid, num := trimNumber(arg.(string))
	if say, ok := saySIDMap[sid]; ok {
		return say + " " + sayDigit(num)
	}
	return sid + " " + sayDigit(num)
}

func (SIDPhraseFormatter) Validate(arg any) error {
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
// STARPhraseFormatter

type STARPhraseFormatter struct{}

func (s STARPhraseFormatter) Written(arg any) string {
	return arg.(string)
}

func (STARPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	star, num := trimNumber(arg.(string))
	if say, ok := saySTARMap[star]; ok {
		return say + " " + sayDigit(num)
	}
	return star + " " + sayDigit(num)
}

func (STARPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// FrequencyPhraseFormatter

type FrequencyPhraseFormatter struct{}

func (FrequencyPhraseFormatter) Written(arg any) string {
	f := arg.(Frequency)
	return fmt.Sprintf("%03d.%02d", f/1000, (f%1000)/10)
}

func (FrequencyPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	f := arg.(Frequency)
	whole := (f / 1000) % 100
	frac := (f % 1000) / 10
	point := ""
	if frac%10 == 0 { // e.g., 121.9 -> read as 21 point 9 not 21 90
		frac /= 10
		point = "point "
	}
	if r.Bool() {
		return fmt.Sprintf("%d ", whole) + point + fmt.Sprintf("%d", frac)
	} else {
		return fmt.Sprintf("one %d point %d", whole, frac)
	}
}

func (FrequencyPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(Frequency); !ok {
		return fmt.Errorf("expected Frequency arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// GroupFormPhraseFormatter

type GroupFormPhraseFormatter struct{}

func (GroupFormPhraseFormatter) Written(arg any) string {
	v := arg.(int)
	return fmt.Sprintf("%d", v)
}

func (GroupFormPhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	v := arg.(int)
	return groupForm(v)
}

func (GroupFormPhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(int); !ok {
		return fmt.Errorf("expected int arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// BeaconCodePhraseFormatter

type BeaconCodePhraseFormatter struct{}

func (BeaconCodePhraseFormatter) Written(arg any) string {
	v := arg.(Squawk)
	return v.String()
}

func (BeaconCodePhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	v := arg.(Squawk)
	s := v.String()
	if r.Bool() {
		return s[:2] + " " + s[2:]
	} else {
		return sayDigit(int(s[0]-'0')) + " " + sayDigit(int(s[1]-'0')) + " " + sayDigit(int(s[2]-'0')) + " " + sayDigit(int(s[3]-'0'))
	}
}

func (BeaconCodePhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(Squawk); !ok {
		return fmt.Errorf("expected Squawk arg, got %T", arg)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AircraftTypePhraseFormatter

type AircraftTypePhraseFormatter struct{}

func (AircraftTypePhraseFormatter) Written(arg any) string {
	return DB.AircraftTypeAliases[arg.(string)] + "(" + arg.(string) + ")"
}

func (AircraftTypePhraseFormatter) Spoken(r *rand.Rand, arg any) string {
	ac := arg.(string)
	if say, ok := sayACTypeMap[ac]; ok && len(say) > 0 {
		s, _ := rand.SampleSeq(r, slices.Values(say))
		return s
	}
	return DB.AircraftTypeAliases[ac]
}

func (AircraftTypePhraseFormatter) Validate(arg any) error {
	if _, ok := arg.(string); !ok {
		return fmt.Errorf("expected string arg, got %T", arg)
	}
	return nil
}
