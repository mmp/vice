// cmd/importfixpairs/main.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Imports STARS DMS fix-pair adaptation dumps into a facility configuration.
// Input is the text dumps of the three DMS fix-pair windows, given in any
// order and identified by the table sections they contain:
//
//   - "Fix Pair Configurations (1 of 2)": the FIXPAIRS table (fix pairs in
//     sequential match order, with flight type and RNAV route flag)
//   - "Fix Pair Configurations (2 of 2)": the CONFIGURATION table (the
//     facility's configuration plans) and the FIXPAIRS_TCP table (the owning
//     TCP per fix pair and plan)
//   - "Fix Pair Reassignment": the FIXPAIR_REASSIGNMENT table (derived fix
//     pair rules; its columns continue in a second table joined by row)
//   - "Significant Points" (optional): the FIX table, whose adapted locations
//     resolve fixes the fix pairs reference
//
// The tables are combined into a "fix_pair_configuration" block that replaces
// any existing one in the target config's "facility_adaptations", preserving
// the rest of the file. A facility with a single configuration plan gets its
// TCPs keyed by "*" (the plan is always in effect); with several plans the
// TCPs are keyed by plan id, which must then be renamed by hand to the vice
// configuration ids the scenarios use. The FIXPAIRS table's Terminal Sector,
// From Facility, and partial-resectorization Fix Pair Id columns have no vice
// counterpart and are dropped, as are fix pairs whose TCPs are not control
// positions of the target facility.
//
// Fixes the fix pairs reference but the config doesn't adapt are resolved
// into new "significant_points" / "airports" entries where possible: from the
// significant points dump when one is given, and otherwise from the nav
// database (navaids and fixes become empty entries, which vice locates at
// load; airports get their name and location). A derived location is only
// accepted within -range nm of the facility center, guarding against a
// same-named fix elsewhere in the country. STARS-internal derived fixes
// (pseudo fixes with no real-world location) can only come from a significant
// points dump; without one they are reported for hand adaptation.
//
// After writing, the config is validated the same way vice validates it at
// load and remaining problems are reported.
//
// Must be run from the top of a vice checkout so that the resources are found.
//
// Usage:
//
//	importfixpairs [-dryrun] -config resources/configurations/ZNY/N90.json dump.txt...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func main() {
	configPath := flag.String("config", "", "facility configuration `file` to add the fix pairs to")
	dryRun := flag.Bool("dryrun", false, "print the generated adaptation without modifying the config")
	rangeNM := flag.Float64("range", 200, "maximum distance in `nm` from the facility center for a derived fix location")
	flag.Parse()

	if *configPath == "" || flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: importfixpairs [-dryrun] -config <config.json> <dump.txt...>\n")
		os.Exit(1)
	}

	av.InitDB()

	var sections []section
	for _, path := range flag.Args() {
		b, err := os.ReadFile(path)
		if err != nil {
			fatalf("%v", err)
		}
		sections = append(sections, parseReport(string(b))...)
	}

	adaptation, warnings, err := buildAdaptation(sections)
	if err != nil {
		fatalf("%v", err)
	}
	dumpPoints, err := parseSigPoints(sections)
	if err != nil {
		fatalf("%v", err)
	}

	contents, err := os.ReadFile(*configPath)
	if err != nil {
		fatalf("%v", err)
	}
	var fc sim.FacilityConfig
	if err := json.Unmarshal(contents, &fc); err != nil {
		fatalf("%s: %v", *configPath, err)
	}

	warnings = append(warnings, filterTCPs(adaptation, fc.ControlPositions)...)

	center, haveCenter := locate(fc.FacilityAdaptation.CenterString)
	if !haveCenter {
		warnings = append(warnings, fmt.Sprintf("config center %q not resolvable; derived fix locations are not range-checked",
			fc.FacilityAdaptation.CenterString))
	}
	sigEntries, airportEntries, resolveWarnings := resolveFixes(referencedFixes(adaptation),
		&fc.FacilityAdaptation, dumpPoints, center, haveCenter, float32(*rangeNM))
	warnings = append(warnings, resolveWarnings...)

	// Merge the resolved fixes first and splice the fix-pair block last so it
	// always lands at the head of "facility_adaptations": that keeps a re-run
	// on an already-imported config byte-identical.
	updated := contents
	if len(sigEntries) > 0 {
		if updated, err = insertIntoAdaptationMember(updated, "significant_points", sigEntries); err != nil {
			fatalf("%s: %v", *configPath, err)
		}
	}
	if len(airportEntries) > 0 {
		if updated, err = insertIntoAdaptationMember(updated, "airports", airportEntries); err != nil {
			fatalf("%s: %v", *configPath, err)
		}
	}
	updated, block, err := spliceConfig(updated, adaptation)
	if err != nil {
		fatalf("%s: %v", *configPath, err)
	}
	if !json.Valid(updated) {
		fatalf("%s: splicing produced invalid JSON; is the existing adaptation well-formed?", *configPath)
	}

	if *dryRun {
		fmt.Println(block)
		for _, member := range []struct {
			name    string
			entries []string
		}{{"significant_points", sigEntries}, {"airports", airportEntries}} {
			if len(member.entries) > 0 {
				fmt.Printf("%s:\n", member.name)
				for _, e := range member.entries {
					fmt.Printf("  %s\n", e)
				}
			}
		}
	} else {
		if err := os.WriteFile(*configPath, updated, 0o644); err != nil {
			fatalf("%s: %v", *configPath, err)
		}
		fmt.Printf("Wrote %d fix pairs (%d arrival, %d departure, %d overflight), %d reassignment rules, "+
			"%d significant points, and %d airports to %s\n",
			adaptation.total(), len(adaptation.arrival), len(adaptation.departure), len(adaptation.overflight),
			len(adaptation.reassignments), len(sigEntries), len(airportEntries), *configPath)
		warnings = append(warnings, validateConfig(*configPath, updated)...)
	}

	for _, w := range warnings {
		fmt.Printf("warning: %s\n", w)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

///////////////////////////////////////////////////////////////////////////
// Report parsing

// A DMS dump is a series of sections, each introduced by a
// "******* NAME *******" banner and holding one or more pipe-delimited
// tables. A table starts with a header row whose first cell is "#."; when a
// section's columns don't fit one table, they continue in a second table
// whose rows correspond one-to-one with the first's.
type table struct {
	header []string
	rows   [][]string
}

type section struct {
	name   string
	tables []table
}

func parseReport(text string) []section {
	var sections []section
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if name, ok := sectionBanner(line); ok {
			sections = append(sections, section{name: name})
			continue
		}
		if !strings.HasPrefix(line, "|") || len(sections) == 0 {
			continue
		}
		cells := splitRow(line)
		s := &sections[len(sections)-1]
		if len(cells) > 0 && cells[0] == "#." {
			s.tables = append(s.tables, table{header: cells})
		} else if len(s.tables) > 0 && !allBlank(cells) {
			t := &s.tables[len(s.tables)-1]
			t.rows = append(t.rows, cells)
		}
	}
	return sections
}

// sectionBanner matches "******* NAME *******" lines.
func sectionBanner(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "*******") || !strings.HasSuffix(line, "*******") {
		return "", false
	}
	name := strings.TrimSpace(strings.Trim(line, "*"))
	return name, name != ""
}

func splitRow(line string) []string {
	cells := strings.Split(strings.Trim(line, "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func allBlank(cells []string) bool {
	return !slices.ContainsFunc(cells, func(s string) bool { return s != "" })
}

// joinedRows combines a section's tables into one set of rows: a single table
// is returned as-is, and a continuation table contributes additional columns
// to the corresponding row of the first.
func joinedRows(s section) (header []string, rows [][]string, err error) {
	if len(s.tables) == 0 {
		return nil, nil, fmt.Errorf("section %s: no tables", s.name)
	}
	header = s.tables[0].header
	rows = s.tables[0].rows
	for _, t := range s.tables[1:] {
		if len(t.rows) != len(rows) {
			return nil, nil, fmt.Errorf("section %s: continuation table has %d rows, want %d",
				s.name, len(t.rows), len(rows))
		}
		header = append(header, t.header...)
		for i := range rows {
			rows[i] = append(rows[i], t.rows[i]...)
		}
	}
	return header, rows, nil
}

// col returns the index of the named column. Header cells may be truncated to
// the column width, so a header that is a prefix of the requested name also
// matches.
func col(header []string, name string) (int, error) {
	for i, h := range header {
		if h == name || (h != "" && strings.HasPrefix(name, h)) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("column %q not found in header %v", name, header)
}

// wild maps the dumps' "~~~" any-value marker to our "*".
func wild(s string) string {
	if s == "~~~" {
		return "*"
	}
	return s
}

///////////////////////////////////////////////////////////////////////////
// Semantic extraction

type fixPairRow struct {
	id         string // fixpair_id, joining the FIXPAIRS and FIXPAIRS_TCP tables
	entry      string
	exit       string
	flightType string // A | P | E | *
	rnav       bool
}

type reassignRow struct {
	seq          int
	rtype        string // TCP | AHO
	fix1, fix2   string
	lower, upper string
	acClass      string
	acid         string
	flightType   string
	activeRunway string
	dfix1, dfix2 string
}

type assignmentEntry struct {
	fixPair [2]string
	tcp     map[string]string // plan id (or "*") -> TCP
	rnav    bool
}

type adaptation struct {
	arrival, departure, overflight []assignmentEntry
	plans                          map[string]string // plan id -> name; nil for a single-plan facility
	reassignments                  []reassignRow     // ordered TCP-type rules
}

func (a *adaptation) total() int {
	return len(a.arrival) + len(a.departure) + len(a.overflight)
}

func buildAdaptation(sections []section) (*adaptation, []string, error) {
	var fixPairs []fixPairRow
	var planIds []string
	planNames := make(map[string]string)
	tcps := make(map[string]map[string]string) // fixpair_id -> plan -> TCP
	var reassign []reassignRow
	var warnings []string
	facilities := make(map[string]bool)

	for _, s := range sections {
		header, rows, err := joinedRows(s)
		if err != nil {
			return nil, nil, err
		}
		get := func(row []string, name string) string {
			i, err2 := col(header, name)
			if err2 != nil && err == nil {
				err = fmt.Errorf("section %s: %v", s.name, err2)
			}
			if err2 != nil {
				return ""
			}
			return row[i]
		}
		switch s.name {
		case "FIXPAIRS":
			for _, row := range rows {
				facilities[get(row, "Facility Id")] = true
				fixPairs = append(fixPairs, fixPairRow{
					id:         get(row, "fixpair_id"),
					entry:      wild(get(row, "Entry Fix")),
					exit:       wild(get(row, "Exit Fix")),
					flightType: wild(get(row, "Flight Type")),
					rnav:       get(row, "RNAV Route") == "Y",
				})
			}
		case "CONFIGURATION":
			for _, row := range rows {
				facilities[get(row, "Facility ID")] = true
				id := get(row, "Config ID")
				planIds = append(planIds, id)
				planNames[id] = get(row, "Config Name")
			}
		case "FIXPAIRS_TCP":
			for _, row := range rows {
				id := get(row, "fixpair_id")
				if tcps[id] == nil {
					tcps[id] = make(map[string]string)
				}
				tcps[id][get(row, "Config ID")] = get(row, "Logical TCP")
			}
		case "FIXPAIRS_RNAV":
			if len(rows) > 0 {
				warnings = append(warnings, fmt.Sprintf("FIXPAIRS_RNAV: %d RNAV delete region entries ignored (not supported)", len(rows)))
			}
		case "FIXPAIR_REASSIGNMENT":
			for _, row := range rows {
				seq, _ := strconv.Atoi(get(row, "seq_id"))
				facilities[get(row, "Terminal Area Id")] = true
				reassign = append(reassign, reassignRow{
					seq:          seq,
					rtype:        get(row, "Reassignment Type"),
					fix1:         wild(get(row, "FP-Fix 1")),
					fix2:         wild(get(row, "FP-Fix 2")),
					lower:        wild(get(row, "Lower Band")),
					upper:        wild(get(row, "Upper Band")),
					acClass:      wild(get(row, "A/C Type Class")),
					acid:         wild(get(row, "ACID")),
					flightType:   wild(get(row, "Type of Flight")),
					activeRunway: wild(get(row, "Active Runway")),
					dfix1:        wild(get(row, "Derived-Fix 1")),
					dfix2:        wild(get(row, "Derived-Fix 2")),
				})
			}
		}
		if err != nil {
			return nil, nil, err
		}
	}

	if len(fixPairs) == 0 {
		return nil, nil, fmt.Errorf("no FIXPAIRS table found in the input files")
	}
	if len(tcps) == 0 {
		return nil, nil, fmt.Errorf("no FIXPAIRS_TCP table found in the input files")
	}
	if len(facilities) > 1 {
		return nil, nil, fmt.Errorf("input mixes multiple facilities: %s", strings.Join(slices.Sorted(mapKeys(facilities)), ", "))
	}

	a := &adaptation{}

	// A single configuration plan is always in effect, so key its TCPs by "*"
	// rather than requiring a matching vice configuration id. With several
	// plans, keep the plan ids; they must be renamed to the vice
	// configuration ids the target facility's scenarios select.
	singlePlan := len(planIds) <= 1
	if !singlePlan {
		a.plans = planNames
		warnings = append(warnings, fmt.Sprintf("%d configuration plans (%s): vice requires plan ids to be "+
			`configuration ids of the target facility; rename them in "plans" and the per-row "tcp" keys`,
			len(planIds), strings.Join(planIds, ", ")))
	}

	// The FIXPAIRS table order is the sequential match order; carry it into
	// the per-flight-type tables.
	for _, fp := range fixPairs {
		planTCPs := tcps[fp.id]
		if len(planTCPs) == 0 {
			warnings = append(warnings, fmt.Sprintf("fix pair %s (%s/%s): no TCP assignment; row dropped",
				fp.id, fp.entry, fp.exit))
			continue
		}
		entry := assignmentEntry{fixPair: [2]string{fp.entry, fp.exit}, rnav: fp.rnav, tcp: make(map[string]string)}
		for plan, tcp := range planTCPs {
			if singlePlan {
				plan = "*"
			}
			entry.tcp[plan] = tcp
		}
		var buckets []*[]assignmentEntry
		switch fp.flightType {
		case "A":
			buckets = []*[]assignmentEntry{&a.arrival}
		case "P":
			buckets = []*[]assignmentEntry{&a.departure}
		case "E":
			buckets = []*[]assignmentEntry{&a.overflight}
		case "*":
			// Flight type not checked: the pair applies to all three tables.
			buckets = []*[]assignmentEntry{&a.arrival, &a.departure, &a.overflight}
		default:
			warnings = append(warnings, fmt.Sprintf("fix pair %s (%s/%s): unknown flight type %q; row dropped",
				fp.id, fp.entry, fp.exit, fp.flightType))
			continue
		}
		for _, b := range buckets {
			*b = append(*b, entry)
		}
	}

	slices.SortStableFunc(reassign, func(x, y reassignRow) int { return x.seq - y.seq })
	acClasses := make(map[string]bool)
	skippedAHO := 0
	for _, r := range reassign {
		// Only TCP-type conditions apply in vice; AHO conditions drive
		// BGS interfacility auto-handoff, which isn't modeled.
		if r.rtype != "TCP" {
			skippedAHO++
			continue
		}
		a.reassignments = append(a.reassignments, r)
		if r.acClass != "*" && r.acClass != "" {
			acClasses[r.acClass] = true
		}
	}
	if skippedAHO > 0 {
		warnings = append(warnings, fmt.Sprintf("%d non-TCP reassignment conditions skipped (auto-handoff is not modeled)", skippedAHO))
	}
	if len(acClasses) > 0 {
		warnings = append(warnings, fmt.Sprintf(`reassignment rules reference aircraft type classes (%s): define them in "tcp_assignment_classes"`,
			strings.Join(slices.Sorted(mapKeys(acClasses)), ", ")))
	}

	return a, warnings, nil
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Fix resolution

// dumpPoint is one row of a Significant Points dump's FIX table.
type dumpPoint struct {
	name        string
	shortName   string
	description string
	simOnly     bool
	location    math.Point2LL
}

// parseSigPoints extracts the FIX table of a Significant Points dump, when
// one was among the inputs.
func parseSigPoints(sections []section) ([]dumpPoint, error) {
	var pts []dumpPoint
	for _, s := range sections {
		if s.name != "FIX" {
			continue
		}
		header, rows, err := joinedRows(s)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			get := func(name string) string {
				i, err2 := col(header, name)
				if err2 != nil && err == nil {
					err = fmt.Errorf("section %s: %v", s.name, err2)
				}
				if err2 != nil {
					return ""
				}
				return row[i]
			}
			loc, ok := parseDMS(get("Latitude"), get("N/S"), get("Longitude"), get("E/W"))
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			pts = append(pts, dumpPoint{
				name:        get("Name"),
				shortName:   get("Short Name"),
				description: get("Description"),
				simOnly:     get("SIM Only") == "Y",
				location:    loc,
			})
		}
	}
	return pts, nil
}

// parseDMS converts the dumps' ddmmss latitude / dddmmss longitude columns
// and their hemisphere indicators to a position.
func parseDMS(lat, ns, lon, ew string) (math.Point2LL, bool) {
	dms := func(s string, degDigits int) (float32, bool) {
		if len(s) != degDigits+4 {
			return 0, false
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		deg, min, sec := v/10000, (v/100)%100, v%100
		if min >= 60 || sec >= 60 {
			return 0, false
		}
		return float32(deg) + float32(min)/60 + float32(sec)/3600, true
	}
	la, ok := dms(lat, 2)
	if !ok {
		return math.Point2LL{}, false
	}
	lo, ok := dms(lon, 3)
	if !ok {
		return math.Point2LL{}, false
	}
	if ns == "S" {
		la = -la
	}
	if ew == "W" {
		lo = -lo
	}
	return math.Point2LL{lo, la}, true
}

// filterTCPs drops TCP assignments that don't name a control position of the
// target facility, and fix pairs left with no assignment at all.
func filterTCPs(a *adaptation, positions map[sim.TCP]*av.Controller) []string {
	if len(positions) == 0 {
		return []string{`config has no "control_positions"; TCP assignments not checked`}
	}
	unknown := make(map[string]bool)
	dropped := 0
	filter := func(rows []assignmentEntry) []assignmentEntry {
		kept := rows[:0]
		for _, e := range rows {
			for plan, tcp := range e.tcp {
				if _, ok := positions[sim.TCP(tcp)]; !ok {
					unknown[tcp] = true
					delete(e.tcp, plan)
				}
			}
			if len(e.tcp) == 0 {
				dropped++
				continue
			}
			kept = append(kept, e)
		}
		return kept
	}
	a.arrival = filter(a.arrival)
	a.departure = filter(a.departure)
	a.overflight = filter(a.overflight)
	if len(unknown) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(`%d TCPs are not in "control_positions" (%s); %d fix pairs dropped`,
		len(unknown), strings.Join(slices.Sorted(mapKeys(unknown)), " "), dropped)}
}

// referencedFixes returns the fix ids the adaptation references, sorted.
func referencedFixes(a *adaptation) []string {
	refs := make(map[string]bool)
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" && id != "*" {
				refs[id] = true
			}
		}
	}
	for _, rows := range [][]assignmentEntry{a.arrival, a.departure, a.overflight} {
		for _, e := range rows {
			add(e.fixPair[0], e.fixPair[1])
		}
	}
	for _, r := range a.reassignments {
		add(r.fix1, r.fix2, r.dfix1, r.dfix2)
	}
	return slices.Sorted(mapKeys(refs))
}

// locate resolves a position string or fix name via the nav database.
func locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return math.Point2LL{}, false
	}
	if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	}
	if n, ok := av.DB.Navaids[s]; ok {
		return n.Location, true
	}
	if ap, ok := av.DB.LookupAirport(s); ok {
		return ap.Location, true
	}
	if f, ok := av.DB.Fixes[s]; ok {
		return f.Location, true
	}
	return math.Point2LL{}, false
}

// resolveFixes works out "significant_points" and "airports" entries for
// referenced fixes the config doesn't adapt: from the significant points dump
// first, then the nav database. Navaids and fixes from the database become
// empty entries whose locations vice resolves at load; airports carry their
// name and location. A derived location is accepted only within maxNM of the
// facility center so a same-named fix elsewhere in the country can't slip in.
func resolveFixes(refs []string, fa *sim.FacilityAdaptation, dump []dumpPoint,
	center math.Point2LL, haveCenter bool, maxNM float32) (sigEntries, airportEntries, warnings []string) {
	inRange := func(p math.Point2LL) bool {
		return !haveCenter || math.NMDistance2LL(center, p) <= maxNM
	}
	shortNames := make(map[string]bool)
	for _, sp := range fa.SignificantPoints {
		if sp.ShortName != "" {
			shortNames[sp.ShortName] = true
		}
	}
	adapted := func(id string) bool {
		if _, ok := fa.SignificantPoints[id]; ok && len(id) <= 3 {
			return true
		}
		if _, ok := fa.Airports[id]; ok {
			return true
		}
		return shortNames[id]
	}
	var underivable, outOfRange []string
	for _, id := range refs {
		if adapted(id) {
			continue
		}
		if pt, ok := dumpLookup(dump, id); ok {
			if !inRange(pt.location) {
				outOfRange = append(outOfRange, id)
				continue
			}
			sigEntries = append(sigEntries, formatSigPoint(pt))
		} else if db := av.DB; db == nil { // uninitialized in tests
			underivable = append(underivable, id)
		} else if n, ok := db.Navaids[id]; ok && inRange(n.Location) {
			sigEntries = append(sigEntries, jsonStr(id)+": {}")
		} else if ap, ok := db.LookupAirport(id); ok && inRange(ap.Location) {
			airportEntries = append(airportEntries, formatAirport(id, ap))
		} else if f, ok := db.Fixes[id]; ok && inRange(f.Location) {
			sigEntries = append(sigEntries, jsonStr(id)+": {}")
		} else {
			underivable = append(underivable, id)
		}
	}
	if len(outOfRange) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d fixes in the significant points dump are farther than %.0f nm from the facility center and were not added: %s",
			len(outOfRange), maxNM, strings.Join(outOfRange, " ")))
	}
	if len(underivable) > 0 {
		warnings = append(warnings, fmt.Sprintf(`%d fixes have no derivable location and need "significant_points" entries by hand: %s`,
			len(underivable), strings.Join(underivable, " ")))
	}
	return sigEntries, airportEntries, warnings
}

// dumpLookup finds the dump row a fix-pair endpoint id refers to: a point
// whose (at most 3-character) name or short name is the id. Points usable
// outside the simulator are preferred.
func dumpLookup(dump []dumpPoint, id string) (dumpPoint, bool) {
	var best dumpPoint
	found := false
	for _, pt := range dump {
		if (len(pt.name) <= 3 && pt.name == id) || pt.shortName == id {
			if !found || (best.simOnly && !pt.simOnly) {
				best, found = pt, true
			}
		}
	}
	return best, found
}

func formatSigPoint(pt dumpPoint) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: {", jsonStr(pt.name))
	if pt.shortName != "" {
		fmt.Fprintf(&sb, "\"short_name\": %s, ", jsonStr(pt.shortName))
	}
	if pt.description != "" {
		fmt.Fprintf(&sb, "\"description\": %s, ", jsonStr(pt.description))
	}
	fmt.Fprintf(&sb, "\"location\": %s}", jsonStr(pt.location.DMSString()))
	return sb.String()
}

func formatAirport(id string, ap av.FAAAirport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: {", jsonStr(id))
	if ap.Name != "" {
		fmt.Fprintf(&sb, "\"name\": %s, ", jsonStr(ap.Name))
	}
	fmt.Fprintf(&sb, "\"location\": %s}", jsonStr(ap.Location.DMSString()))
	return sb.String()
}

///////////////////////////////////////////////////////////////////////////
// JSON generation

// formatAdaptation renders the "fix_pair_configuration" member with the given
// base indentation (the member indent inside "facility_adaptations"), without
// a trailing comma or newline.
func formatAdaptation(a *adaptation, indent string) string {
	var sb strings.Builder
	in2, in3, in4 := indent+"  ", indent+"    ", indent+"      "

	fmt.Fprintf(&sb, "%s\"fix_pair_configuration\": {\n", indent)
	if len(a.plans) > 0 {
		fmt.Fprintf(&sb, "%s\"plans\": {\n", in2)
		for i, id := range slices.Sorted(mapKeys(a.plans)) {
			fmt.Fprintf(&sb, "%s%s: %s%s\n", in3, jsonStr(id), jsonStr(a.plans[id]), comma(i < len(a.plans)-1))
		}
		fmt.Fprintf(&sb, "%s},\n", in2)
	}
	if len(a.reassignments) > 0 {
		fmt.Fprintf(&sb, "%s\"fix_pair_reassignment\": [\n", in2)
		for i, r := range a.reassignments {
			fmt.Fprintf(&sb, "%s%s%s\n", in3, formatReassignment(r), comma(i < len(a.reassignments)-1))
		}
		fmt.Fprintf(&sb, "%s],\n", in2)
	}
	fmt.Fprintf(&sb, "%s\"fix_pair_assignments\": {\n", in2)
	buckets := []struct {
		name string
		rows []assignmentEntry
	}{{"arrival", a.arrival}, {"departure", a.departure}, {"overflight", a.overflight}}
	buckets = slices.DeleteFunc(buckets, func(b struct {
		name string
		rows []assignmentEntry
	}) bool {
		return len(b.rows) == 0
	})
	for bi, b := range buckets {
		fmt.Fprintf(&sb, "%s%s: [\n", in3, jsonStr(b.name))
		for i, row := range b.rows {
			fmt.Fprintf(&sb, "%s%s%s\n", in4, formatAssignment(row), comma(i < len(b.rows)-1))
		}
		fmt.Fprintf(&sb, "%s]%s\n", in3, comma(bi < len(buckets)-1))
	}
	fmt.Fprintf(&sb, "%s}\n", in2)
	fmt.Fprintf(&sb, "%s}", indent)
	return sb.String()
}

func formatAssignment(e assignmentEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "{\"fix_pair\": [%s, %s], \"tcp\": {", jsonStr(e.fixPair[0]), jsonStr(e.fixPair[1]))
	for i, plan := range slices.Sorted(mapKeys(e.tcp)) {
		fmt.Fprintf(&sb, "%s%s: %s", comma(i > 0)+ifStr(i > 0, " "), jsonStr(plan), jsonStr(e.tcp[plan]))
	}
	sb.WriteString("}")
	if e.rnav {
		sb.WriteString(", \"rnav\": true")
	}
	sb.WriteString("}")
	return sb.String()
}

// formatReassignment renders one reassignment rule, omitting any-valued
// criteria so the adaptation reads like the meaningful subset.
func formatReassignment(r reassignRow) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "{\"fp_fix_pair\": [%s, %s]", jsonStr(r.fix1), jsonStr(r.fix2))
	if bands := formatLevelBand(r.lower, r.upper); bands != "" {
		fmt.Fprintf(&sb, ", \"alt_bands\": %s", bands)
	}
	if r.acClass != "*" && r.acClass != "" {
		fmt.Fprintf(&sb, ", \"ac_type\": %s", jsonStr(r.acClass))
	}
	if r.acid != "*" && r.acid != "" {
		fmt.Fprintf(&sb, ", \"acid\": %s", jsonStr(r.acid))
	}
	if r.flightType != "*" && r.flightType != "" {
		fmt.Fprintf(&sb, ", \"type_of_flight\": %s", jsonStr(r.flightType))
	}
	if r.activeRunway != "*" && r.activeRunway != "" {
		fmt.Fprintf(&sb, ", \"active_runway\": %s", jsonStr(r.activeRunway))
	}
	fmt.Fprintf(&sb, ", \"derived_fix_pair\": [%s, %s]}", jsonStr(r.dfix1), jsonStr(r.dfix2))
	return sb.String()
}

// formatLevelBand renders a Lower/Upper Band pair: "" for any (omitted),
// "VFR"/"OTP" for the altitude codes, or a [lo, hi] range in hundreds of
// feet. A blank upper bound with a numeric lower means an exact level.
func formatLevelBand(lower, upper string) string {
	switch lower {
	case "", "*":
		return ""
	case "VFR", "OTP":
		return jsonStr(lower)
	}
	lo, err := strconv.Atoi(lower)
	if err != nil {
		return jsonStr(lower) // let config validation report it
	}
	hi := lo
	if upper != "" && upper != "*" {
		if h, err := strconv.Atoi(upper); err == nil {
			hi = h
		}
	}
	return fmt.Sprintf("[%d, %d]", lo, hi)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func comma(b bool) string {
	return ifStr(b, ",")
}

func ifStr(b bool, s string) string {
	if b {
		return s
	}
	return ""
}

///////////////////////////////////////////////////////////////////////////
// Config splicing

// spliceConfig returns the config contents with the generated
// "fix_pair_configuration" as the first member of "facility_adaptations",
// replacing any existing one. It also returns the rendered block.
func spliceConfig(contents []byte, a *adaptation) ([]byte, string, error) {
	text := string(contents)

	if start, end, ok := findMember(text, `"fix_pair_configuration"`); ok {
		text = text[:start] + text[end:]
	}

	// Insert after the "facility_adaptations": { line, one level deeper.
	i := strings.Index(text, `"facility_adaptations"`)
	if i == -1 {
		return nil, "", fmt.Errorf(`no "facility_adaptations" member found`)
	}
	lineStart := strings.LastIndexByte(text[:i], '\n') + 1
	indent := text[lineStart:i]
	if strings.TrimSpace(indent) != "" {
		return nil, "", fmt.Errorf(`"facility_adaptations" is not at the start of a line`)
	}
	open := strings.IndexByte(text[i:], '{')
	if open == -1 {
		return nil, "", fmt.Errorf(`malformed "facility_adaptations"`)
	}
	insertAt := i + open + 1

	block := formatAdaptation(a, indent+"  ")
	return []byte(text[:insertAt] + "\n" + block + "," + text[insertAt:]), block, nil
}

// findMember locates a "key": value object member in JSON text, returning the
// range from the start of the key through the value's closing brace and any
// trailing comma and newline.
func findMember(text, key string) (start, end int, ok bool) {
	start = strings.Index(text, key)
	if start == -1 {
		return 0, 0, false
	}
	// Back up over the key's leading indentation so the whole line goes.
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	if strings.TrimSpace(text[lineStart:start]) == "" {
		start = lineStart
	}
	i := strings.IndexByte(text[start:], '{')
	if i == -1 {
		return 0, 0, false
	}
	end, ok = matchBrace(text, i+start)
	if !ok {
		return 0, 0, false
	}
	for end < len(text) && (text[end] == ',' || text[end] == ' ') {
		end++
	}
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return start, end, true
}

// matchBrace returns the index just past the '}' matching the '{' at open.
// Braces inside strings are skipped.
func matchBrace(text string, open int) (int, bool) {
	depth, inString, escaped := 0, false, false
	for i := open; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString:
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// findTopLevelKey locates `"key"` used as a member name at the top level of
// the object spanning text[open:close], returning the index of its opening
// quote.
func findTopLevelKey(text string, open, close int, key string) (int, bool) {
	depth, inString, escaped, strStart := 0, false, false, 0
	for i := open; i < close; i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString:
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
				if depth == 1 && text[strStart+1:i] == key {
					j := i + 1
					for j < close && (text[j] == ' ' || text[j] == '\t') {
						j++
					}
					if j < close && text[j] == ':' {
						return strStart, true
					}
				}
			}
		case c == '"':
			inString, strStart = true, i
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		}
	}
	return 0, false
}

// insertIntoAdaptationMember adds rendered `"key": value` entries at the head
// of the named object member of "facility_adaptations", creating the member
// when the config doesn't have one.
func insertIntoAdaptationMember(contents []byte, member string, entries []string) ([]byte, error) {
	text := string(contents)
	i := strings.Index(text, `"facility_adaptations"`)
	if i == -1 {
		return nil, fmt.Errorf(`no "facility_adaptations" member found`)
	}
	faIndent := text[strings.LastIndexByte(text[:i], '\n')+1 : i]
	open := strings.IndexByte(text[i:], '{')
	if open == -1 {
		return nil, fmt.Errorf(`malformed "facility_adaptations"`)
	}
	open += i
	faEnd, ok := matchBrace(text, open)
	if !ok {
		return nil, fmt.Errorf(`unbalanced braces in "facility_adaptations"`)
	}

	if keyIdx, ok := findTopLevelKey(text, open, faEnd, member); ok {
		mOpen := strings.IndexByte(text[keyIdx:], '{')
		if mOpen == -1 {
			return nil, fmt.Errorf("%q is not an object member", member)
		}
		mOpen += keyIdx
		memberIndent := text[strings.LastIndexByte(text[:keyIdx], '\n')+1 : keyIdx]
		entryIndent := memberIndent + "  "
		j := mOpen + 1
		for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r' || text[j] == '\n') {
			j++
		}
		block := "\n" + entryIndent + strings.Join(entries, ",\n"+entryIndent)
		if j < len(text) && text[j] == '}' { // empty member object
			block += "\n" + memberIndent
		} else {
			block += ","
		}
		return []byte(text[:mOpen+1] + block + text[mOpen+1:]), nil
	}

	// No such member: create it as the first entry of facility_adaptations.
	memberIndent := faIndent + "  "
	entryIndent := memberIndent + "  "
	block := "\n" + memberIndent + jsonStr(member) + ": {\n" + entryIndent +
		strings.Join(entries, ",\n"+entryIndent) + "\n" + memberIndent + "},"
	return []byte(text[:open+1] + block + text[open+1:]), nil
}

///////////////////////////////////////////////////////////////////////////
// Validation

// validateConfig runs the updated config through the same checks vice applies
// at load and returns the problems found. Endpoint errors are expected until
// the referenced significant points and airports are adapted.
func validateConfig(path string, contents []byte) []string {
	var e util.ErrorLogger
	for _, d := range util.FindDuplicateJSONKeys(contents) {
		e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
	}
	util.CheckJSON[sim.FacilityConfig](contents, &e)
	var fc sim.FacilityConfig
	if err := json.Unmarshal(contents, &fc); err != nil {
		e.Error(err)
	} else {
		fc.PostDeserialize(path, &e)
	}
	if !e.HaveErrors() {
		return nil
	}
	// The same unresolved fix or TCP recurs in every row that references it;
	// summarize those and pass everything else through.
	unresolvedFixes := make(map[string]bool)
	unresolvedTCPs := make(map[string]bool)
	var warnings []string
	for err := range e.Errors() {
		if id, ok := errorId(err, `fix "`, `" is not an adapted significant point or airport`); ok {
			unresolvedFixes[id] = true
		} else if id, ok := errorId(err, `entry "`, `" is not an adapted significant point or airport`); ok {
			unresolvedFixes[id] = true
		} else if id, ok := errorId(err, `exit "`, `" is not an adapted significant point or airport`); ok {
			unresolvedFixes[id] = true
		} else if id, ok := errorId(err, `tcp "`, `" (plan`); ok {
			unresolvedTCPs[id] = true
		} else {
			warnings = append(warnings, err)
		}
	}
	if len(unresolvedFixes) > 0 {
		warnings = append(warnings, fmt.Sprintf(`%d fixes need "significant_points" (or "airports") entries: %s`,
			len(unresolvedFixes), strings.Join(slices.Sorted(mapKeys(unresolvedFixes)), " ")))
	}
	if len(unresolvedTCPs) > 0 {
		warnings = append(warnings, fmt.Sprintf(`%d TCPs are not in "control_positions": %s`,
			len(unresolvedTCPs), strings.Join(slices.Sorted(mapKeys(unresolvedTCPs)), " ")))
	}
	return warnings
}

// errorId extracts the quoted id from a validation message containing
// prefix<id>suffix.
func errorId(err, prefix, suffix string) (string, bool) {
	i := strings.Index(err, prefix)
	if i == -1 {
		return "", false
	}
	rest := err[i+len(prefix):]
	j := strings.Index(rest, suffix)
	if j == -1 {
		return "", false
	}
	return rest[:j], true
}
