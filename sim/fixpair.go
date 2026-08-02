// sim/fixpair.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// This file implements the STARS side of the fix-pair feature. Given an
// aircraft's entry/exit fix pair (derived by the pseudo-ERAM side; see
// eram.go), STARS optionally substitutes a derived pair (DMS Fix Pair
// Reassignment Window, Sec. 4.7.4) and then first-match looks the pair up to
// assign the initial owning TCP for the active configuration plan (DMS Fix
// Pair Configurations Windows, Sec. 4.7.5). The active plan is the
// scenario-selected facility configuration id (Sim/State.ConfigurationId), so
// the `plans` map keys must be facility configuration ids.

// FixPairConfiguration is the STARS-facility fix-pair adaptation, nested
// under "facility_adaptations".
type FixPairConfiguration struct {
	// Plans maps a Configuration Plan id (== facility configuration id, e.g.
	// "CNE") to a human description.
	Plans map[string]string `json:"plans"`
	// Reassignments derive an alternate fix pair before assignment; rules are
	// evaluated in order and the first match wins. These correspond to the
	// real system's TCP-type reassignment conditions (the AHO type drives
	// BGS interfacility auto-handoff, which vice doesn't model).
	Reassignments []FixPairReassignment `json:"fix_pair_reassignment"`
	// Assignments is the sequential first-match table, split by the
	// arrival/departure/overflight bucket.
	Assignments FixPairAssignments `json:"fix_pair_assignments"`
}

// FixPairAssignments splits the assignment table by flight-type bucket (A/P/E).
type FixPairAssignments struct {
	Arrival    []FixPairAssignmentRow `json:"arrival"`
	Departure  []FixPairAssignmentRow `json:"departure"`
	Overflight []FixPairAssignmentRow `json:"overflight"`
}

// FixPairAssignmentRow maps a fix pair [entry, exit] ("*" = any) to the owning
// TCP per Configuration Plan.
type FixPairAssignmentRow struct {
	FixPair [2]string      `json:"fix_pair"` // [entry, exit]
	TCP     map[string]TCP `json:"tcp"`      // config plan id -> owning logical TCP
	// RNAV marks the fix pair as an RNAV route ("RNAV Route?" in the Fix Pair
	// Configurations Window): matching flights are identified as RNAV and
	// display the RNAV symbol in their data blocks.
	RNAV bool `json:"rnav,omitempty"`
}

// FixPairReassignment derives an alternate fix pair when its criteria fire
// (DMS Table 4-26). Every unset / "*" field matches any.
type FixPairReassignment struct {
	FpFixPair    [2]string `json:"fp_fix_pair"`    // flight-plan [fix1, fix2]; "*" = any
	AltBands     LevelBand `json:"alt_bands"`      // [lo,hi] hundreds ft | "VFR" | "OTP" | "*"
	ACType       string    `json:"ac_type"`        // an adapted "tcp_assignment" aircraft type class, or "*"
	ACID         string    `json:"acid"`           // callsign pattern (see matchACIDPattern)
	TypeOfFlight string    `json:"type_of_flight"` // "A" | "P" | "E" | "*"
	// ActiveRunway matches when it is one of the active runways at the
	// facility's primary airport ("*" or unset = any).
	ActiveRunway string `json:"active_runway"`
	// DepartureRunway matches the flight's own departure runway exactly
	// ("*" or unset = any). This is a vice extension, not a DMS criterion:
	// it distinguishes simultaneous departures off different runways (e.g.
	// east- vs west-side splits), which active_runway cannot when both
	// runways are active.
	DepartureRunway string    `json:"departure_runway"`
	DerivedFixPair  [2]string `json:"derived_fix_pair"` // [derived1, derived2]; "" or "*" keeps original
}

// matches reports whether all reassignment criteria fire for in.
func (r FixPairReassignment) matches(in FixPairInput, classes map[string][]string) bool {
	return wildcardMatch(r.FpFixPair[0], in.Entry) &&
		wildcardMatch(r.FpFixPair[1], in.Exit) &&
		matchFlightType(r.TypeOfFlight, in.FlightType) &&
		r.AltBands.matches(in) &&
		matchACTypeClass(classes, r.ACType, in.ACType) &&
		matchACIDPattern(r.ACID, in.ACID) &&
		matchActiveRunway(r.ActiveRunway, in.ActiveRunways) &&
		wildcardMatch(r.DepartureRunway, in.DepartureRunway)
}

// LevelBand is a reassignment level criterion: an inclusive [lower,upper] band
// in hundreds of feet, or the special "VFR"/"OTP" states, or "*"/absent = any.
type LevelBand struct {
	Any      bool
	VFR      bool
	OTP      bool
	HasRange bool
	Range    [2]int // hundreds of feet, valid when HasRange
}

// CheckJSON lets the strict config checker accept a LevelBand's raw JSON forms
// ([lo,hi] number array, or a "*"/"VFR"/"OTP" string), mirroring UnmarshalJSON —
// otherwise the checker would expect the struct's object shape and reject them.
func (LevelBand) CheckJSON(j any) bool {
	switch v := j.(type) {
	case string, nil:
		return true
	case []any:
		return len(v) == 2
	default:
		return false
	}
}

// MarshalJSON emits a LevelBand in the same forms UnmarshalJSON accepts
// ([lo,hi] | "VFR" | "OTP" | "*"), so a saved config round-trips. Without it the
// struct would serialize to {"Any":…,"Range":[…]} and fail to reload.
func (lb LevelBand) MarshalJSON() ([]byte, error) {
	switch {
	case lb.VFR:
		return []byte(`"VFR"`), nil
	case lb.OTP:
		return []byte(`"OTP"`), nil
	case lb.HasRange:
		return json.Marshal(lb.Range)
	default:
		return []byte(`"*"`), nil
	}
}

func (lb *LevelBand) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		lb.Any = true
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		switch strings.ToUpper(strings.TrimSpace(s)) {
		case "", "*":
			lb.Any = true
		case "VFR":
			lb.VFR = true
		case "OTP":
			lb.OTP = true
		default:
			return fmt.Errorf(`invalid level_band %q: want [lo,hi], "VFR", "OTP", or "*"`, s)
		}
		return nil
	}
	var r [2]int
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf(`invalid level_band %s: want [lo,hi], "VFR", "OTP", or "*"`, string(b))
	}
	lb.Range = r
	lb.HasRange = true
	return nil
}

// FixPairAirport is a facility "airports" entry: fix-pair endpoints
// may be significant points or airports. The name, location, and single-char
// abbreviation feed the STARS client's significant-point display.
type FixPairAirport struct {
	Name         string        `json:"name"`
	LocationStr  string        `json:"location"`
	Abbreviation string        `json:"abbreviation"`
	Location     math.Point2LL `json:"-"`
}

// FixPair is an (entry, exit) pair.
type FixPair struct {
	Entry string
	Exit  string
}

// FixPairInput carries the aircraft attributes the fix-pair engine matches on.
type FixPairInput struct {
	Entry, Exit     string
	FlightType      av.TypeOfFlight
	Level           int // hundreds of feet (see fixPairLevel)
	VFR, OTP        bool
	ACType          string // aircraft type, e.g. "A320"
	ACID            string
	ActiveRunways   []string // active runways at the primary airport
	DepartureRunway string   // the flight's own departure runway; empty for arrivals/overflights
}

// flightTypeCode maps a TypeOfFlight to its adapted A/P/E code.
func flightTypeCode(ft av.TypeOfFlight) string {
	switch ft {
	case av.FlightTypeArrival:
		return "A"
	case av.FlightTypeDeparture:
		return "P"
	case av.FlightTypeOverflight:
		return "E"
	default:
		return ""
	}
}

// wildcardMatch reports whether an exact-match adapted field (with "" / "*"
// meaning any) matches val.
func wildcardMatch(field, val string) bool {
	return field == "" || field == "*" || field == val
}

// matchFlightType reports whether an adapted A/P/E/"*" field matches ft.
func matchFlightType(field string, ft av.TypeOfFlight) bool {
	return field == "" || field == "*" || field == flightTypeCode(ft)
}

// matchActiveRunway reports whether an adapted active-runway criterion is
// satisfied: "" or "*" matches any, otherwise the runway must be one of the
// active runways at the primary airport.
func matchActiveRunway(field string, active []string) bool {
	return field == "" || field == "*" || slices.Contains(active, field)
}

// matchACIDPattern reports whether acid matches an adapted callsign pattern.
// Lowercase 'd' matches any digit, lowercase 'a' matches any letter, '*'
// matches any single character, and any other character matches literally
// (callsigns are uppercase, so literal 'A'/'D' are distinct from the 'a'/'d'
// metacharacters). A trailing run of '*'s also matches zero characters, so
// "Nd*****" matches N1A as well as N123AB. The whole-field pattern "" or "*"
// matches any ACID.
func matchACIDPattern(pattern, acid string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// pattern[fixed:] is all '*'s; those match the (possibly empty) remainder
	// of the callsign.
	fixed := len(pattern)
	for fixed > 0 && pattern[fixed-1] == '*' {
		fixed--
	}
	if len(acid) < fixed || len(acid) > len(pattern) {
		return false
	}
	for i := 0; i < fixed; i++ {
		p, c := pattern[i], acid[i]
		switch p {
		case '*':
		// any single character
		case 'd':
			if c < '0' || c > '9' {
				return false
			}
		case 'a':
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
				return false
			}
		default:
			if p != c {
				return false
			}
		}
	}
	return true
}

// matches reports whether the level criterion is satisfied by in.
func (lb LevelBand) matches(in FixPairInput) bool {
	switch {
	case lb.Any:
		return true
	case lb.VFR:
		return in.VFR
	case lb.OTP:
		return in.OTP
	case lb.HasRange:
		return in.Level >= lb.Range[0] && in.Level <= lb.Range[1]
	default:
		return true // unspecified = any
	}
}

// Reassign evaluates the reassignment rows in order and returns the (possibly
// derived) fix pair. The first matching row wins; a derived field set to "" or
// "*" keeps the original value. With no match (or a nil config) the original
// pair is returned unchanged.
func (c *FixPairConfiguration) Reassign(in FixPairInput, classes map[string][]string) FixPair {
	pair := FixPair{Entry: in.Entry, Exit: in.Exit}
	if c == nil {
		return pair
	}
	for _, r := range c.Reassignments {
		if r.matches(in, classes) {
			if d := r.DerivedFixPair[0]; d != "" && d != "*" {
				pair.Entry = d
			}
			if d := r.DerivedFixPair[1]; d != "" && d != "*" {
				pair.Exit = d
			}
			return pair
		}
	}
	return pair
}

// assignmentRow walks the flight-type bucket top-to-bottom and returns the
// first row whose fix pair matches ("*" matches any), or nil. Row properties
// like RNAV apply on this match alone, independent of whether the row has a
// TCP for the active plan.
func (c *FixPairConfiguration) assignmentRow(pair FixPair, ft av.TypeOfFlight) *FixPairAssignmentRow {
	if c == nil {
		return nil
	}
	var rows []FixPairAssignmentRow
	switch ft {
	case av.FlightTypeArrival:
		rows = c.Assignments.Arrival
	case av.FlightTypeDeparture:
		rows = c.Assignments.Departure
	case av.FlightTypeOverflight:
		rows = c.Assignments.Overflight
	default:
		return nil
	}
	for i := range rows {
		if wildcardMatch(rows[i].FixPair[0], pair.Entry) && wildcardMatch(rows[i].FixPair[1], pair.Exit) {
			return &rows[i]
		}
	}
	return nil
}

// AssignTCP returns the owning TCP for the active plan from the first
// assignment row whose fix pair matches. Returns ("", false) if no row matches
// or the matched row has no TCP for activePlan.
func (c *FixPairConfiguration) AssignTCP(pair FixPair, ft av.TypeOfFlight, activePlan string) (TCP, bool) {
	row := c.assignmentRow(pair, ft)
	if row == nil {
		return "", false
	}
	tcp, ok := row.TCP[activePlan]
	if !ok {
		// Fall back to a "*" default plan so fix pairs apply on any
		// configuration without keying every one (lets a facility use one
		// owner regardless of the active config).
		tcp, ok = row.TCP["*"]
	}
	return tcp, ok
}

// fixPairLevel returns the flight-plan level compared against reassignment
// level bands, in hundreds of feet: the requested altitude for departures and
// the assigned altitude for arrivals and overflights (DMS Table 4-26). TRACON
// flight plans may carry only one of the two, so the missing one falls back
// to the other.
func fixPairLevel(fp *NASFlightPlan) int {
	if fp.TypeOfFlight == av.FlightTypeDeparture {
		if fp.RequestedAltitude != 0 {
			return fp.RequestedAltitude / 100
		}
		return fp.AssignedAltitude / 100
	}
	if fp.AssignedAltitude != 0 {
		return fp.AssignedAltitude / 100
	}
	return fp.RequestedAltitude / 100
}

// activePrimaryRunways returns the active runways at the facility's primary
// airport; an adapted "active_runway" reassignment criterion is matched
// against these.
func (s *Sim) activePrimaryRunways() []string {
	var rwys []string
	add := func(airport string, rwy av.RunwayID) {
		if airport == s.State.PrimaryAirport && !slices.Contains(rwys, string(rwy)) {
			rwys = append(rwys, string(rwy))
		}
	}
	for _, r := range s.State.DepartureRunways {
		add(r.Airport, r.Runway)
	}
	for _, r := range s.State.ArrivalRunways {
		add(r.Airport, r.Runway)
	}
	return rwys
}

// applyFixPairAssignment runs the STARS fix-pair reassignment + assignment
// pipeline for the given flight plan. When the facility adapts a
// fix_pair_configuration, it records any derived pair on the flight plan
// (leaving the actual entry/exit fixes in place) and, on an assignment-table
// match, sets the inbound handoff controller to the owning TCP for the active
// plan; it returns true in that case. Facilities without a
// fix_pair_configuration are left untouched so the caller keeps its existing
// inbound-flow assignment.
func (s *Sim) applyFixPairAssignment(nasFp *NASFlightPlan, ac *Aircraft) bool {
	cfg := s.State.FacilityAdaptation.FixPairConfiguration
	if cfg == nil {
		return false
	}
	in := FixPairInput{
		Entry:           nasFp.EntryFix,
		Exit:            nasFp.ExitFix,
		FlightType:      nasFp.TypeOfFlight,
		Level:           fixPairLevel(nasFp),
		VFR:             nasFp.Rules == av.FlightRulesVFR,
		OTP:             nasFp.VFROTP,
		ACType:          nasFp.AircraftType,
		ACID:            string(nasFp.ACID),
		ActiveRunways:   s.activePrimaryRunways(),
		DepartureRunway: ac.FlightPlan.DepartureRunway,
	}
	// Reassignment may substitute a derived pair; the flight plan keeps its
	// actual fixes and carries the substitutions separately.
	pair := cfg.Reassign(in, s.State.FacilityAdaptation.TCPAssignmentClasses)
	if pair.Entry != nasFp.EntryFix {
		nasFp.DerivedEntryFix = pair.Entry
	}
	if pair.Exit != nasFp.ExitFix {
		nasFp.DerivedExitFix = pair.Exit
	}
	row := cfg.assignmentRow(pair, nasFp.TypeOfFlight)
	if row == nil {
		return false
	}
	if row.RNAV && s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol {
		nasFp.RNAV = true
	}
	tcp, ok := row.TCP[s.State.ConfigurationId]
	if !ok {
		tcp, ok = row.TCP["*"]
	}
	if !ok {
		return false
	}
	// The adaptation may name positions beyond what this scenario loads
	// (excess adaptation is tolerated); leave the caller's assignment in
	// place rather than handing off to a position that doesn't exist.
	if _, ok := s.ControlPositions[tcp]; !ok {
		if s.lg != nil {
			s.lg.Infof("fixpair: %s tcp %s not among loaded control positions; skipping", nasFp.ACID, tcp)
		}
		return false
	}
	s.setOwningPosition(nasFp, tcp)
	if s.lg != nil {
		s.lg.Infof("fixpair: %s type=%v pair=%s/%s plan=%q -> tcp=%s (tracking=%s handoff=%s)",
			nasFp.ACID, nasFp.TypeOfFlight, pair.Entry, pair.Exit, s.State.ConfigurationId,
			tcp, nasFp.TrackingController, nasFp.InboundHandoffController)
	}
	return true
}

// setOwningPosition stamps the initial owning position on a flight plan; shared
// by the fix-pair assignment and the scenario ownership override.
//
// Arrivals/overflights enter from center: the position is who the inbound track
// is handed to (InboundHandoffController), and the track stays owned by the
// entering controller until accepted. A departure originates locally and is
// owned by a local position from the start, so when it is owned by a human
// position that owner is set too — not just a downstream handoff target. Virtual
// (auto-release) departures keep their virtual owner; only the handoff is set.
func (s *Sim) setOwningPosition(nasFp *NASFlightPlan, tcp TCP) {
	nasFp.InboundHandoffController = tcp
	if nasFp.TypeOfFlight == av.FlightTypeDeparture && !s.isVirtualController(nasFp.TrackingController) {
		nasFp.TrackingController = tcp
		nasFp.OwningTCW = s.tcwForPosition(tcp)
	}
}

// resolveEndpoint reports whether a fix-pair endpoint id can match a flight
// plan's entry/exit fix at this facility. Flight plans carry 3-character fix
// ids, so an endpoint must be a significant point's name of at most 3
// characters, a significant point's short name, or an airport. "*" and "" are
// wildcards and always resolve.
func (fa *FacilityAdaptation) resolveEndpoint(id string) bool {
	if id == "" || id == "*" {
		return true
	}
	// Significant point by name (the short-named case)...
	if _, ok := fa.SignificantPoints[id]; ok && len(id) <= 3 {
		return true
	}
	//... or by short name (the longer-named case).
	for _, sp := range fa.SignificantPoints {
		if sp.ShortName == id {
			return true
		}
	}
	//... or an airport.
	_, ok := fa.Airports[id]
	return ok
}

// validate checks the fix-pair configuration against the facility's control
// positions, configurations, aircraft type classes, and adapted endpoints.
// Errors accumulate in e.
func (c *FixPairConfiguration) validate(fa *FacilityAdaptation, controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	if c == nil {
		return
	}
	e.Push("fix_pair_configuration")
	defer e.Pop()
	for plan := range c.Plans {
		if len(plan) > 3 {
			e.ErrorString("plan id %q must be at most 3 characters", plan)
		}
		// The active plan is the scenario-selected configuration id, so a plan
		// that isn't a configuration id could never be selected.
		if _, ok := fa.Configurations[plan]; !ok {
			e.ErrorString(`plan %q is not a configuration id in "configurations"`, plan)
		}
	}
	for i, r := range c.Reassignments {
		e.Push(fmt.Sprintf("fix_pair_reassignment[%d]", i))
		if ft := r.TypeOfFlight; ft != "" && ft != "*" && ft != "A" && ft != "P" && ft != "E" {
			e.ErrorString(`"type_of_flight" %q must be "A", "P", "E", or "*"`, ft)
		}
		if ac := r.ACType; ac != "" && ac != "*" {
			if _, ok := fa.TCPAssignmentClasses[ac]; !ok {
				e.ErrorString(`"ac_type" %q is not a class in "tcp_assignment_classes"`, ac)
			}
		}
		if rwy := r.ActiveRunway; rwy != "" && rwy != "*" && !validRunwayId(rwy) {
			e.ErrorString(`"active_runway" %q is not a valid runway id`, rwy)
		}
		// departure_runway matches the scenario's departure runway id
		// exactly, and those may carry suffixes ("15R.Props", "12R_17"), so
		// its format isn't checked.
		for _, ep := range []string{r.FpFixPair[0], r.FpFixPair[1], r.DerivedFixPair[0], r.DerivedFixPair[1]} {
			if !fa.resolveEndpoint(ep) {
				e.ErrorString("fix %q is not an adapted significant point or airport", ep)
			}
		}
		e.Pop()
	}
	// Assignment rows, by bucket.
	buckets := []struct {
		name string
		rows []FixPairAssignmentRow
	}{
		{"arrival", c.Assignments.Arrival},
		{"departure", c.Assignments.Departure},
		{"overflight", c.Assignments.Overflight},
	}
	for _, b := range buckets {
		for i, row := range b.rows {
			e.Push(fmt.Sprintf("fix_pair_assignments.%s[%d]", b.name, i))
			if !fa.resolveEndpoint(row.FixPair[0]) {
				e.ErrorString("entry %q is not an adapted significant point or airport", row.FixPair[0])
			}
			if !fa.resolveEndpoint(row.FixPair[1]) {
				e.ErrorString("exit %q is not an adapted significant point or airport", row.FixPair[1])
			}
			if len(row.TCP) == 0 {
				e.ErrorString(`no "tcp" assignments given`)
			}
			for plan, tcp := range row.TCP {
				if plan != "*" && len(c.Plans) > 0 {
					if _, ok := c.Plans[plan]; !ok {
						e.ErrorString(`tcp plan %q is not defined in "plans"`, plan)
					}
				}
				if _, ok := controlPositions[tcp]; !ok {
					e.ErrorString(`tcp %q (plan %q) is not in "control_positions"`, tcp, plan)
				}
			}
			e.Pop()
		}
	}
}

// validRunwayId reports whether s looks like a runway id: one or two digits
// optionally followed by L, R, or C.
func validRunwayId(s string) bool {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n < 1 || n > 2 {
		return false
	}
	return n == len(s) || (n == len(s)-1 && (s[n] == 'L' || s[n] == 'R' || s[n] == 'C'))
}

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

// applyAutoScratchpadAssignment fills a spawning flight's scratchpad(s) from the
// first matching adapted row, skipping any scratchpad already set (an explicitly
// entered scratchpad is never overridden). A no-op without adapted rows. Fix
// criteria consider the derived fix pair first and then the actual one, so it
// must run after fix-pair reassignment.
func (s *Sim) applyAutoScratchpadAssignment(nasFp *NASFlightPlan) {
	rows := s.State.FacilityAdaptation.AutoScratchpadAssignment
	if len(rows) == 0 || (nasFp.Scratchpad != "" && nasFp.SecondaryScratchpad != "") {
		return
	}
	plan := s.State.ConfigurationId
	for i := range rows {
		r := &rows[i]
		if !r.matches(plan, nasFp) {
			continue
		}
		if nasFp.Scratchpad == "" && r.Scratchpad1 != "" {
			nasFp.Scratchpad = asaScratchpadText(r.Scratchpad1)
		}
		if nasFp.SecondaryScratchpad == "" && r.Scratchpad2 != "" {
			nasFp.SecondaryScratchpad = asaScratchpadText(r.Scratchpad2)
		}
		return
	}
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
