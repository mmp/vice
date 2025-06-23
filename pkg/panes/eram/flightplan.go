// pkg/panes/stars/flightplan.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

// All parsing functions have this signature; they take a string to try to
// parse, an optional callback to validate scratchpads (required if
// scratchpads may be parsed), and the STARSFlightPlanSpecifier to update
// with the result. On a successful parse, true, nil is returned.  There
// are two possibilities for an unsuccessful parse:
//
// 1. true, (non-nil error) means that this matches the general syntax for
// the thing but it is invalid (e.g., an aircraft specification of the form
// a/b/c where we have found the slashes (which aren't used in any other
// specifiers) but where instead of giving a number for "a", text is given.
// In that case, we can stop parsing and return an error.
//
// 2. false, non-nil error: it's invalid and it's not that thing; if there
// are other things it might be, we keep trying them.
type fpEntryParseFunc func(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error)

// Flight plans can be comprised of a variety of different entries,
// sometimes with different syntax in different cases.  The functions below
// take specifiers based on the keys of this map to indicate which flight
// plan entries are valid; the associated values are functions that try to
// parse the corresponding thing.
var fpParseFuncs = map[string]fpEntryParseFunc{
	"ACID":            parseFpACID,
	"#/AC_TYPE/EQ":    parseFpNumAcTypeEqSuffix,
	"#/AC_TYPE4/EQ":   parseFpNumAcType4EqSuffix,
	"AC_TYPE/EQ":      parseFpAcTypeEqSuffix,
	"ALT_A":           parseFpAssignedAltitude,
	"ALT_I":           parseFPInterimAltitude,
	"ALT_P":           parseFpPilotAltitude,
	"ALT_R":           parseFpRequestedAltitude,
	"BEACON":          parseFpBeacon,
	"COORD_TIME":      parseFPCoordinationTime,
	"FIX_PAIR":        parseFpFixPair,
	"FLT_TYPE":        parseFpTypeOfFlight,
	"PLUS_ALT_A":      parseFpPlusAssignedAltitude,
	"PLUS_PLUS_ALT_R": parseFpPlus2RequestedAltitude,
	// "PLUS_SP2":        parseFpPlusSp2,
	"RNAV":  parseFpRNAVToggle,
	"RULES": parseFpFlightRules,
	// "SP1":             parseFpSp1,
	"TCP":          parseFpTCP,
	"TCP/FIX_PAIR": parseFpTCPOrFixPair,
	// "TRI_ALT_A":       parseFpTriAssignedAltitude,
	// "TRI_SP1":         parseFpTriSp1,
	"VFR_ARR_FIXES": parseFpVFRArrivalFixes,
}

// parseOneFlightPlan takes a format string that of one or more of the
// items in fpParseFuncs (comma-separated), user-entered text, and a
// scratchpad validator (if we may be parsing a scratchpad). It tries to
// parse the text as the given thing.  Note that it panics if the format is
// invalid; this is a bit unfriendly (and requires testing all of the
// codepaths that call this, but the format strings should all be
// compile-time constants so it's not like there's an opportunity for
// runtime recovery here.
func parseOneFlightPlan(format string, text string, checkSp func(s string, primary bool) bool) (sim.STARSFlightPlanSpecifier, error) {
	spec := sim.STARSFlightPlanSpecifier{}

	if text == "" {
		return spec, ErrCommandFormat
	}

	text, _, ok := strings.Cut(text, " ")
	if ok {
		// Extra junk at the end
		return spec, ErrCommandFormat
	}

	for _, s := range strings.Split(format, ",") {
		if parse, ok := fpParseFuncs[s]; !ok {
			panic("unknown fp entry specifier: " + s)
		} else if ok, err := parse(text, checkSp, &spec); ok {
			return spec, err
		}
	}

	// Nothing matched
	return spec, ErrCommandFormat
}

// parseFlightPlan is similar to parseOneFlightPlan but it takes a richer
// syntax for the format string. The user text is split into fields
// (delineated by spaces) that are parsed successively.
// The format string options are:
//
// 1. "+" followed by a parse specifier: the corresponding thing must be
// found and parsed correctly.
//
// 2. "?" followed by a single parse specifier: if the current field parses
// successfully, it is consumed. If not, the parse specifier is ignored and parsing continues.
//
// 3. "?" followed by one or more comma-delineated parse specifiers: if the
// current field parses successfully according to any of the specifiers,
// considered in order, it is consumed and then we try to parse following
// field (if any), again according to the given specifiers. Once a field is
// not successfully consumed, the parse specifier is ignored and parsing
// continues.
func parseFlightPlan(format string, text string, checkSp func(s string, primary bool) bool) (sim.STARSFlightPlanSpecifier, error) {
	spec := sim.STARSFlightPlanSpecifier{}

	fields := strings.Fields(text)
	for len(format) > 0 {
		mode := format[0]
		format = format[1:]

		// Extract the format specifier(s)
		var fmtspec string
		fmtspec, format = func() (string, string) {
			for i, ch := range format {
				if ch == '+' || ch == '?' { // next mode
					return format[:i], format[i:]
				}
			}
			// got to end
			return format, ""
		}()

		tryParse := func(fmtspec, field string) (bool, error) {
			if parse, ok := fpParseFuncs[fmtspec]; !ok {
				panic("unknown fp entry specifier: " + fmtspec)
			} else {
				return parse(field, checkSp, &spec)
			}
		}

		if mode == '+' {
			if strings.Contains(fmtspec, ",") {
				panic("only one specifier allowed after +. Got: " + fmtspec)
			}

			if len(fields) == 0 {
				return spec, ErrCommandFormat
			}

			ok, err := tryParse(fmtspec, fields[0])
			fields = fields[1:]

			if err != nil {
				return spec, err
			}
			if !ok { // required but not successfully parsed
				return spec, ErrCommandFormat
			}
		} else if mode == '?' {
			fmtspecs := strings.Split(fmtspec, ",")
		outer:
			for len(fields) > 0 {
				for _, fmtspec := range fmtspecs {
					ok, err := tryParse(fmtspec, fields[0])

					if ok && err == nil { // success
						fields = fields[1:]
						if len(fmtspecs) == 1 { // only one, so don't keep trying more fields
							break outer
						} else { // on to try the next field with our set of specs
							continue outer
						}
					} else if ok && err != nil { // it is this thing, but it's invalid
						return spec, err
					}
					// !ok is fine; on to the next spec.
				}
				// none of the specs matched but they're optional, so move on to the next spec
				break
			}
		} else {
			panic("unexpected mode specifier: " + string(mode) + ": " + format)
		}
	}

	if len(fields) > 0 {
		// There's extra junk at the end of the specified flight plan
		return spec, ErrCommandFormat
	}

	return spec, nil
}

func parseFpACID(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if s[0] < 'A' || s[0] > 'Z' {
		// ACID must start with a letter
		return false, ErrERAMIllegalACID
	}
	if len(s) > 7 {
		// No more than 7 characters
		return false, ErrERAMIllegalACID
	}

	spec.ACID.Set(sim.ACID(s))
	return true, nil
}

// A320, A320/G, 2/F16/G
func parseFpNumAcTypeEqSuffix(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if tf := strings.Split(s, "/"); len(tf) == 3 { // formation # / actype / eq suffix
		if count, err := strconv.Atoi(tf[0]); err != nil || len(tf[0]) > 2 { // 2 digits max
			return true, ErrCommandFormat
		} else {
			spec.AircraftCount.Set(count)
			s = strings.Join(tf[1:], "/")
		}
	}

	// Handle the rest of it
	return parseFpAcTypeEqSuffix(s, checkSp, spec)
}

// F16* 2/B2**/G : require 4 chars for actype
func parseFpNumAcType4EqSuffix(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if s == "" {
		return false, ErrCommandFormat
	}

	tf := strings.Split(s, "/")
	if len(tf) == 3 { // formation # / actype / eq suffix
		if count, err := strconv.Atoi(tf[0]); err != nil || len(tf[0]) > 2 { // 2 digits max
			return true, ErrCommandFormat
		} else {
			spec.AircraftCount.Set(count)
			tf = tf[1:]
		}
	}

	if len(tf[0]) != 4 {
		return false, ErrCommandFormat
	}

	tf[0] = strings.TrimRight(tf[0], "*")

	// Handle the rest of it
	return parseFpAcTypeEqSuffix(strings.Join(tf, "/"), checkSp, spec)
}

// A320/G, C172
func parseFpAcTypeEqSuffix(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	actype, suffix, _ := strings.Cut(s, "/")

	if len(actype) < 2 || actype[0] < 'A' || actype[0] > 'Z' {
		// a/c types are at least 2 chars and start with a letter
		return false, ErrCommandFormat
	}

	actype = strings.TrimRight(actype, "*") // hack: for ones that require 4 chars

	spec.AircraftType.Set(actype)

	// equipment suffix?
	if len(suffix) > 0 {
		if len(suffix) != 1 || suffix[0] < 'A' || suffix[0] > 'Z' {
			return true, ErrCommandFormat
		}
		spec.EquipmentSuffix.Set(suffix)
	}
	return true, nil
}

func parseFpAltitude(s string, ptr *util.Optional[int]) (bool, error) {
	if len(s) != 3 {
		return false, ErrCommandFormat
	}

	if alt, err := strconv.Atoi(s); err != nil {
		return false, ErrCommandFormat
	} else {
		ptr.Set(alt * 100)
		return true, nil
	}
}

func parseFpAssignedAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	return parseFpAltitude(s, &spec.AssignedAltitude)
}

func parseFPInterimAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if unicode.IsLetter(rune(s[0])) {
		interimType := string(s[0])
		if !slices.Contains([]string{"P", "L"}, interimType) {
			return false, ErrCommandFormat
		}
		spec.InterimType.Set(interimType)
		s = strings.TrimPrefix(s, interimType)
	}
	return parseFpAltitude(s, &spec.InterimAlt)
}

func parseFpPlusAssignedAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if !strings.HasPrefix(s, "+") {
		return false, ErrCommandFormat
	}
	s = strings.TrimPrefix(s, "+")
	return parseFpAltitude(s, &spec.AssignedAltitude)
}

func parseFpPlus2RequestedAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if !strings.HasPrefix(s, "++") {
		return false, ErrCommandFormat
	}
	s = strings.TrimPrefix(s, "++")
	return parseFpAltitude(s, &spec.RequestedAltitude)
}

func parseFpBeacon(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if s == "+" || s == "/" || s == "/1" || s == "/2" || s == "/3" || s == "/4" ||
		(len(s) == 4 && util.IsAllNumbers(s)) {
		spec.SquawkAssignment.Set(s)
		return true, nil
	} else {
		return false, nil
	}

	return false, nil
}

func parseFPCoordinationTime(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if len(s) != 5 || s[4] != 'E' {
		return false, ErrCommandFormat
	}

	t, err := time.Parse("1504", s[:4])
	if err != nil {
		return false, ErrCommandFormat
	}

	spec.CoordinationTime.Set(t)

	return true, nil
}

func parseFpFixPair(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	// entry*exit / entry*exit*type of flight
	entry, exit, ok := strings.Cut(s, "*")
	if !ok {
		return false, ErrCommandFormat
	}

	exit, flttp, _ := strings.Cut(exit, "*")

	// TODO: validate these
	spec.EntryFix.Set(entry)
	spec.ExitFix.Set(exit)
	if len(flttp) > 0 {
		return parseFpTypeOfFlight(flttp, checkSp, spec)
	}
	return true, nil
}

func parseFpRNAVToggle(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if s == "R" {
		spec.RNAVToggle.Set(true)
		return true, nil
	}
	return false, nil
}

func parseFpFlightRules(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if strings.HasPrefix(s, ".") {
		switch strings.TrimPrefix(s, ".") {
		case "V", "P" /* VFR on top */ :
			spec.Rules.Set(av.FlightRulesVFR)
			return true, nil

		case "E" /* enroute */, "":
			spec.Rules.Set(av.FlightRulesIFR)
			return true, nil

		default:
			return true, ErrERAMIllegalValue
		}
	}
	return false, ErrCommandFormat
}

func parseFpPilotAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	return parseFpAltitude(s, &spec.PilotReportedAltitude)
}

func parseFpRequestedAltitude(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	return parseFpAltitude(s, &spec.RequestedAltitude)
}

// func parseFpSp1(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
// 	if !checkSp(s, true) {
// 		return false, ErrSTARSIllegalScratchpad
// 	}
// 	spec.Scratchpad.Set(s)
// 	return true, nil
// }

// func parseFpTriSp1(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
// 	if !strings.HasPrefix(s, STARSTriangleCharacter) {
// 		return false, ErrCommandFormat
// 	}
// 	sp := strings.TrimPrefix(s, STARSTriangleCharacter)
// 	return parseFpSp1(sp, checkSp, spec)
// }

// func parseFpPlusSp2(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
// 	// Scratchpad 2
// 	if !strings.HasPrefix(s, "+") {
// 		return false, ErrCommandFormat
// 	}
// 	sp := strings.TrimPrefix(s, "+")
// 	if !checkSp(sp, false) {
// 		return false, ErrSTARSIllegalScratchpad
// 	}
// 	spec.SecondaryScratchpad.Set(sp)
// 	return true, nil
// }

func parseFpTCP(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	if len(s) != 2 || s[0] < '1' || s[0] > '9' || s[1] < 'A' || s[1] > 'Z' { // must be two char TCP
		return false, ErrERAMIllegalPosition
	}

	spec.TrackingController.Set(s)

	return true, nil
}

func parseFpTCPOrFixPair(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	// TODO: use per-controller defaults for entry/exit if not specified.
	// TODO: assign TCP based on fix pair if it's not specified
	// TODO: adapted single-char ids
	if strings.Contains(s, "*") { // Fix pair(ish)
		if s[0] != '*' {
			var entry string
			entry, s, _ = strings.Cut(s, "*")
			// TODO: validate?
			spec.EntryFix.Set(entry)
		}
		if len(s) > 0 && s[0] != '*' {
			var exit string
			exit, s, _ = strings.Cut(s, "*")
			// TODO: validate?
			spec.ExitFix.Set(exit)
		}
		if len(s) > 0 {
			return parseFpTypeOfFlight(s, checkSp, spec)
		}
	} else if len(s) == 2 && s[0] >= '1' && s[0] <= '9' && s[1] >= 'A' && s[1] <= 'Z' { // TCP
		spec.TrackingController.Set(s)
		return true, nil
	}
	return false, ErrERAMIllegalPosition
}

func parseFpTypeOfFlight(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	// Type of flight
	// TODO: allow optional single character airport ID after type
	if s == "A" {
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		return true, nil
	}
	if s == "P" {
		spec.TypeOfFlight.Set(av.FlightTypeDeparture)
		return true, nil
	}
	if s == "E" {
		spec.TypeOfFlight.Set(av.FlightTypeOverflight)
		return true, nil
	}
	return false, ErrCommandFormat
}

// JFK, LGA*JFK, FRG*DPK*
func parseFpVFRArrivalFixes(s string, checkSp func(s string, primary bool) bool, spec *sim.STARSFlightPlanSpecifier) (bool, error) {
	// For now just see if it's a valid airport, pending a better model of
	// coordination fixes.
	if dep, arr, ok := strings.Cut(s, "*"); ok {
		if len(dep) != 3 || len(arr) != 3 {
			return false, ErrERAMIllegalAirport
		}
		// TODO: ILL FIX if entry fix is invalid
		spec.EntryFix.Set(dep)
		spec.ExitFixIsIntermediate.Set(strings.HasSuffix(arr, "*"))
		spec.ExitFix.Set(arr)
	} else {
		// TODO: entry should be our default airport
		if len(s) != 3 {
			return false, ErrERAMIllegalAirport
		}
		spec.ExitFix.Set(s)
	}

	if spec.ExitFixIsIntermediate.IsSet && spec.ExitFixIsIntermediate.Get() {
		// TODO: validate?
		return true, nil
	} else if _, ok := av.DB.Airports[spec.ExitFix.Get()]; ok {
		return true, nil
	} else if _, ok := av.DB.Airports["K"+spec.ExitFix.Get()]; ok {
		return true, nil
	} else {
		return false, ErrERAMIllegalAirport
	}
}

func isVFRFlightPlan(text string) bool {
	f := strings.Fields(text)
	if len(f) < 2 {
		return false
	}
	f = f[:2]
	_, err := parseFlightPlan("+ACID+VFR_ARR_FIXES", strings.Join(f, " "), func(string, bool) bool { return false })
	return err == nil
}

func checkScratchpad(ctx *panes.Context, contents string, isSecondary, isImplied bool) error {
	lc := len([]rune(contents))
	fac := ctx.FacilityAdaptation

	if !fac.CheckScratchpad(contents) {
		return ErrCommandFormat
	}

	if !isSecondary && isImplied && lc == 1 {
		// One-character for primary is only allowed via [MF]Y
		return ErrCommandFormat
	}

	if !isSecondary && isImplied {
		// For the implied version (i.e., not [multifunc]Y), it also can't
		// match one of the TCPs
		if lc == 2 {
			for _, ctrl := range ctx.Client.State.Controllers {
				if ctrl.FacilityIdentifier == "" && ctrl.TCP == contents {
					return ErrCommandFormat
				}
			}
		}
	}

	return nil
}

// trk may be nil

// TODO Make for ERAM

// func (sp *STARSPane) formatFlightPlan(ctx *panes.Context, fp *sim.STARSFlightPlan, trk *sim.Track) string {
// 	if fp == nil { // shouldn't happen...
// 		return "NO PLAN"
// 	}

// 	fmtTime := func(t time.Time) string {
// 		return t.UTC().Format("1504")
// 	}

// 	// Common stuff
// 	var state *TrackState
// 	if trk != nil {
// 		state = sp.TrackState[trk.ADSBCallsign]
// 	}

// 	var aircraftType string
// 	if fp.AircraftCount > 1 {
// 		aircraftType = strconv.Itoa(fp.AircraftCount) + "/"
// 	}
// 	aircraftType += fp.AircraftType
// 	if fp.CWTCategory != "" {
// 		aircraftType += "/" + fp.CWTCategory
// 	}
// 	if fp.RNAV {
// 		aircraftType += "^"
// 	}
// 	if fp.EquipmentSuffix != "" {
// 		aircraftType += "/" + fp.EquipmentSuffix
// 	}

// 	fmtfix := func(f string) string {
// 		if f == "" {
// 			return ""
// 		}
// 		if len(f) > 3 {
// 			f = f[:3]
// 		}
// 		return f + "  "
// 	}

// 	trkalt := func() string {
// 		if trk == nil {
// 			return ""
// 		} else if trk.Mode == av.TransponderModeAltitude {
// 			return fmt.Sprintf("%03d ", int(trk.TransponderAltitude+50)/100)
// 		} else if fp.PilotReportedAltitude != 0 {
// 			return fmt.Sprintf("%03d ", fp.PilotReportedAltitude/100)
// 		} else {
// 			return "RDR "
// 		}
// 	}
// 	result := string(fp.ACID) + " " // all start with aricraft id
// 	switch fp.TypeOfFlight {
// 	case av.FlightTypeOverflight:
// 		result += aircraftType + " "
// 		result += fp.AssignedSquawk.String() + " " + fp.TrackingController + " "
// 		result += trkalt()
// 		result += "\n"

// 		result += fmtfix(fp.EntryFix)
// 		if state != nil {
// 			result += "E" + fmtTime(state.FirstRadarTrackTime) + " "
// 		} else {
// 			result += "E" + fmtTime(fp.CoordinationTime) + " "
// 		}
// 		result += fmtfix(fp.ExitFix)
// 		if fp.RequestedAltitude != 0 {
// 			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100) + "\n"
// 		}

// 		// TODO: [mode S equipage] [target identification] [target address]

// 	case av.FlightTypeDeparture:
// 		if state == nil || state.FirstRadarTrackTime.IsZero() {
// 			// Proposed departure
// 			result += aircraftType + " "
// 			result += fp.AssignedSquawk.String() + " " + fp.TrackingController + "\n"

// 			result += fmtfix(fp.EntryFix)
// 			result += fmtfix(fp.ExitFix)
// 			if !fp.CoordinationTime.IsZero() {
// 				result += "P" + fmtTime(fp.CoordinationTime) + " "
// 			}
// 			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100)
// 		} else {
// 			// Active departure
// 			result += fp.AssignedSquawk.String() + " "
// 			result += fmtfix(fp.EntryFix)
// 			result += "D" + fmtTime(state.FirstRadarTrackTime) + " "
// 			result += trkalt() + "\n"

// 			result += fmtfix(fp.ExitFix)
// 			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100) + " "
// 			result += aircraftType

// 			// TODO: [mode S equipage] [target identification] [target address]
// 		}
// 	case av.FlightTypeArrival:
// 		result += aircraftType + " "
// 		result += fp.AssignedSquawk.String() + " "
// 		result += fp.TrackingController + " "
// 		result += trkalt() + "\n"

// 		result += fmtfix(fp.EntryFix)
// 		if state != nil {
// 			result += "A" + fmtTime(state.FirstRadarTrackTime) + " "
// 		} else {
// 			result += "A" + fmtTime(fp.CoordinationTime) + " "
// 		}
// 		result += fmtfix(fp.ExitFix)
// 		// TODO: [mode S equipage] [target identification] [target address]

// 	default:
// 		return "FLIGHT TYPE UNKNOWN"
// 	}

// 	return result
// }
