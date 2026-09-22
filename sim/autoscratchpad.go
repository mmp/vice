// sim/autoscratchpad.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Automatic scratchpad assignment

// asaDeltaChar is octal 200 (STARS font renders it as a delta); a caret in an
// adapted scratchpad value is converted to it.
const asaDeltaChar = string(rune(0x80))

// AutoScratchpadRow is one automatic_scratchpad_assignment adaptation entry
// (DMS Table 4-31): when a flight plan is created without a scratchpad and its
// (config plan, entry/exit fix, flight type, altitude) match an adapted row,
// the row's scratchpad defaults are applied. Rows are evaluated most-specific-
// first (see sortAutoScratchpad); the first match wins.
type AutoScratchpadRow struct {
	ConfigPlan   string `json:"config_plan"`   // config plan id, or "*" for any
	EntryFix     string `json:"entry_fix"`     // fix/airport/derived; "*" = any (incl unassigned); "" = only unassigned
	ExitFix      string `json:"exit_fix"`      // same convention as entry_fix
	FlightType   string `json:"flight_type"`   // exactly one of "A", "P", or "E"
	Altitude     string `json:"altitude"`      // hundreds of feet ("000".."999"); "VFR"; "*" or "" = any
	AltitudeType string `json:"altitude_type"` // "R" (equals) | "RA" (at or below); required for a numeric altitude
	GroupID      string `json:"group_id"`      // A-Z or ""; groups fix pairs for the supervisor scratchpad-group command
	Scratchpad1  string `json:"scratchpad1"`   // 1-3 chars; "^" -> delta symbol
	Scratchpad2  string `json:"scratchpad2"`
}

// asaFixMatch matches an ASA entry/exit fix criterion against the flight
// plan's fix: the derived fix (when reassignment substituted one) is checked
// first, then the actual fix, so a criterion may name either. "*" matches any
// fix including unassigned; "" matches only an unassigned fix.
func asaFixMatch(field, actual, derived string) bool {
	switch field {
	case "*":
		return true
	case "":
		return actual == "" && derived == ""
	default:
		return field == derived || field == actual
	}
}

// altitudeMatches evaluates the altitude / altitude-type criterion. R requires
// the requested altitude to equal the adapted value; RA requires the requested
// (departures) or assigned (arrivals/overflights) altitude to be at or below it.
func (r AutoScratchpadRow) altitudeMatches(fp *NASFlightPlan) bool {
	switch strings.ToUpper(strings.TrimSpace(r.Altitude)) {
	case "", "*":
		return true
	case "VFR":
		return fp.Rules == av.FlightRulesVFR
	default: // numeric altitude; validation requires an R or RA altitude_type
		adapted, err := strconv.Atoi(strings.TrimSpace(r.Altitude))
		if err != nil {
			return false
		}
		switch strings.ToUpper(strings.TrimSpace(r.AltitudeType)) {
		case "R": // requested altitude must equal the adapted value
			return fp.RequestedAltitude/100 == adapted
		case "RA": // requested (dep) / assigned (arr,ovf) at or below the value
			return fixPairLevel(fp) <= adapted
		default:
			return false
		}
	}
}

// matches reports whether all of the row's criteria fire for the flight.
func (r AutoScratchpadRow) matches(plan string, fp *NASFlightPlan) bool {
	return wildcardMatch(r.ConfigPlan, plan) &&
		asaFixMatch(r.EntryFix, fp.EntryFix, fp.DerivedEntryFix) &&
		asaFixMatch(r.ExitFix, fp.ExitFix, fp.DerivedExitFix) &&
		strings.ToUpper(strings.TrimSpace(r.FlightType)) == flightTypeCode(fp.TypeOfFlight) &&
		r.altitudeMatches(fp)
}

// sortAutoScratchpad orders rows most-specific-first so a first-match scan picks
// the tightest applicable rule (Table 4-31: Config Plan, Entry Fix, Exit
// Fix, VFR, Altitude Type [R before RA], Altitude [low to high], Flight Type
// [A, E, P]; "*" is last at every level).
func sortAutoScratchpad(rows []AutoScratchpadRow) {
	wild := func(s string) int {
		if s == "*" {
			return 1
		}
		return 0
	}
	// wildcardMatch treats "" the same as "*" for ConfigPlan (unlike EntryFix/
	// ExitFix, where "" is the narrower "only unassigned" criterion), so the
	// sort must rank it as a wildcard too or a "" row can win ahead of
	// plan-specific rows it should lose to.
	configPlanWild := func(s string) int {
		if s == "*" || s == "" {
			return 1
		}
		return 0
	}
	isVFR := func(r AutoScratchpadRow) int {
		if strings.EqualFold(strings.TrimSpace(r.Altitude), "VFR") {
			return 0
		}
		return 1
	}
	altType := func(s string) int {
		switch strings.ToUpper(strings.TrimSpace(s)) {
		case "R":
			return 0
		case "RA":
			return 1
		default:
			return 2
		}
	}
	altValue := func(r AutoScratchpadRow) int {
		s := strings.ToUpper(strings.TrimSpace(r.Altitude))
		if s == "" || s == "*" {
			return 1 << 20 // any-altitude sorts last
		}
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
		return 0
	}
	ftRank := func(s string) int {
		switch strings.ToUpper(strings.TrimSpace(s)) {
		case "A":
			return 0
		case "E":
			return 1
		default: // "P"
			return 2
		}
	}
	slices.SortStableFunc(rows, func(a, b AutoScratchpadRow) int {
		for _, d := range []int{
			configPlanWild(a.ConfigPlan) - configPlanWild(b.ConfigPlan),
			wild(a.EntryFix) - wild(b.EntryFix),
			wild(a.ExitFix) - wild(b.ExitFix),
			isVFR(a) - isVFR(b),
			altType(a.AltitudeType) - altType(b.AltitudeType),
			altValue(a) - altValue(b),
			ftRank(a.FlightType) - ftRank(b.FlightType),
		} {
			if d != 0 {
				return d
			}
		}
		return 0
	})
}

// asaScratchpadText renders an adapted scratchpad value, converting a caret to
// the delta character.
func asaScratchpadText(s string) string {
	return strings.ReplaceAll(s, "^", asaDeltaChar)
}

// validateASAScratchpadText checks an adapted scratchpad value: 1-3 characters
// from the allowed set (alphanumerics and + / . * ^), not a special track
// condition string, and not three numerals (which would read as an altitude).
// Returns a problem description or "".
func validateASAScratchpadText(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 3 {
		return "must be at most 3 characters"
	}
	digits := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits++
		} else if !((c >= 'A' && c <= 'Z') || strings.ContainsRune("+/.*^", c)) {
			return fmt.Sprintf("invalid character %q", c)
		}
	}
	if digits == 3 {
		return "must not be three numerals"
	}
	switch s {
	case "NAT", "CST", "AMB", "RDR", "ADB", "ADS", "XXX":
		return "is reserved for special track conditions"
	}
	return ""
}

// validateAutoScratchpad checks the adapted rows and sorts them for evaluation.
func validateAutoScratchpad(rows []AutoScratchpadRow, e *util.ErrorLogger) {
	for i := range rows {
		r := &rows[i]
		e.Push(fmt.Sprintf("automatic_scratchpad_assignment[%d]", i))
		if ft := strings.ToUpper(strings.TrimSpace(r.FlightType)); ft != "A" && ft != "P" && ft != "E" {
			e.ErrorString(`"flight_type" %q must be exactly one of "A", "P", or "E"`, r.FlightType)
		}
		alt := strings.ToUpper(strings.TrimSpace(r.Altitude))
		at := strings.ToUpper(strings.TrimSpace(r.AltitudeType))
		numeric := alt != "" && alt != "*" && alt != "VFR"
		if numeric {
			if _, err := strconv.Atoi(strings.TrimSpace(r.Altitude)); err != nil {
				e.ErrorString(`altitude %q: want hundreds of feet ("000".."999"), "VFR", or "*"`, r.Altitude)
			}
			if at != "R" && at != "RA" {
				e.ErrorString(`altitude_type must be "R" or "RA" for a numeric altitude`)
			}
		} else if at != "" {
			e.ErrorString(`altitude_type is not allowed for a non-numeric altitude (VFR or "*")`)
		}
		if g := r.GroupID; g != "" && (len(g) != 1 || g[0] < 'A' || g[0] > 'Z') {
			e.ErrorString(`group_id %q: want a single letter A-Z or ""`, g)
		}
		// Table 4-31: entries with an Altitude Type of "R" cannot be in a group.
		if r.GroupID != "" && at == "R" {
			e.ErrorString(`group_id is not allowed with altitude_type "R"`)
		}
		for _, sp := range []string{r.Scratchpad1, r.Scratchpad2} {
			if problem := validateASAScratchpadText(sp); problem != "" {
				e.ErrorString("scratchpad %q %s", sp, problem)
			}
		}
		if r.Scratchpad1 == "" && r.Scratchpad2 == "" {
			e.ErrorString(`at least one of scratchpad1/scratchpad2 must be set`)
		}
		e.Pop()
	}
	sortAutoScratchpad(rows)
}
