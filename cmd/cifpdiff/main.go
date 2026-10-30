// cmd/cifpdiff/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// cifpdiff compares two cycles of the FAA CIFP and reports what changed that
// the scenarios depend on: the SIDs, STARs, and approaches at the airports
// they use, and every line of the scenarios and facility configurations that
// names a procedure the new cycle renumbered or dropped or a fix it removed
// or moved. Many of those lines pass "vice -lint": a hand-written route
// keeps a stale procedure name, a configuration key names a renamed flow,
// and a departure's route to another facility drops a fix it can't locate.
// It is run from the top of the repository with the previous cycle taken
// from git:
//
//	git show HEAD:resources/FAACIFP18.zst > /tmp/old.zst
//	go run ./cmd/cifpdiff /tmp/old.zst resources/FAACIFP18.zst
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

var (
	allAirports  = flag.Bool("all", false, "report procedure changes at every airport, not just the ones the scenarios use")
	resourcesDir = flag.String("resources", "resources", "`directory` holding the scenarios and facility configurations")
	movedNM      = flag.Float64("moved", 0.5, "report references to fixes that moved at least this many `nm`")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: cifpdiff [flags] old-cifp new-cifp\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(flag.Arg(0), flag.Arg(1)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(oldPath, newPath string) error {
	var old, cur db.ARINC424Result
	var oldErr, curErr error
	var wg sync.WaitGroup
	wg.Go(func() { old, oldErr = parseCIFP(oldPath) })
	wg.Go(func() { cur, curErr = parseCIFP(newPath) })
	wg.Wait()
	if err := errors.Join(oldErr, curErr); err != nil {
		return err
	}

	used, err := scenarioAirports(filepath.Join(*resourcesDir, "scenarios"))
	if err != nil {
		return err
	}

	changes := diffProcedures(old, cur)
	printProcedureChanges(changes, used)

	notes := procedureNotes(changes, cur)
	removed, moved := diffFixes(old, cur)
	fmt.Printf("\n%d fixes and navaids were removed and %d moved %.1f nm or more.\n", len(removed), len(moved), *movedNM)
	for _, id := range removed {
		notes[id] = "fix removed"
	}
	for id, nm := range moved {
		notes[id] = fmt.Sprintf("fix moved %.1f nm", nm)
	}
	return printReferences(*resourcesDir, notes)
}

// parseCIFP reads a CIFP file, decompressing it if it is zstd compressed.
func parseCIFP(path string) (db.ARINC424Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return db.ARINC424Result{}, err
	}
	defer f.Close()

	var r io.Reader = bufio.NewReader(f)
	if filepath.Ext(path) == ".zst" {
		zr, err := zstd.NewReader(r)
		if err != nil {
			return db.ARINC424Result{}, fmt.Errorf("%s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	}
	return db.ParseARINC424(r), nil
}

// scenarioAirports returns the airports that the scenarios in dir define.
func scenarioAirports(dir string) (map[av.ICAOAirportCode]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	used := make(map[av.ICAOAirportCode]bool)
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var sg struct {
			Airports map[av.ICAOAirportCode]json.RawMessage `json:"airports"`
		}
		if err := json.Unmarshal(b, &sg); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for icao := range sg.Airports {
			used[icao] = true
		}
	}
	return used, nil
}

// procedureKinds are the kinds of procedures compared, in report order.
var procedureKinds = []string{"SID", "STAR", "approach"}

// procedureChange is a procedure that an airport lost, gained, or had
// recoded between the cycles. A renumbered procedure is a single change
// that has both names; Old is empty for one that was added and New for one
// that was removed.
type procedureChange struct {
	Airport              av.ICAOAirportCode
	Kind                 string
	Old, New             string
	OldRoutes, NewRoutes []av.CIFPRoute
}

// airportProcedures returns the airport's procedures by kind and then by
// name, each given as its coded routes.
func airportProcedures(ap db.Airport) map[string]map[string][]av.CIFPRoute {
	procs := map[string]map[string][]av.CIFPRoute{"SID": {}, "STAR": {}, "approach": {}}
	for name, sid := range ap.SIDs {
		procs["SID"][name] = sid.Routes(name)
	}
	for name, star := range ap.STARs {
		procs["STAR"][name] = star.Routes(name)
	}
	for id, appr := range ap.Approaches {
		procs["approach"][id] = util.MapSlice(appr.Waypoints, func(wps av.WaypointArray) av.CIFPRoute {
			return av.CIFPRoute{Name: id, Route: wps.Encode()}
		})
	}
	return procs
}

// procedureBase returns a SID or STAR name without its revision number.
func procedureBase(name string) string {
	return strings.TrimRight(name, "0123456789")
}

// diffProcedures returns the procedures that differ between the cycles,
// sorted by airport. A SID or STAR that disappeared is paired with one that
// appeared with the same name but for its number, as the FAA renames a
// procedure when it revises it; approaches are never paired, since their
// ids are made of the approach type and runway.
func diffProcedures(old, cur db.ARINC424Result) []procedureChange {
	var changes []procedureChange
	icaos := slices.Sorted(util.SeqConcat(maps.Keys(old.Airports), maps.Keys(cur.Airports)))
	for _, icao := range slices.Compact(icaos) {
		oldProcs, curProcs := airportProcedures(old.Airports[icao]), airportProcedures(cur.Airports[icao])
		for _, kind := range procedureKinds {
			o, c := oldProcs[kind], curProcs[kind]
			paired := make(map[string]bool)
			for name, routes := range util.SortedMap(o) {
				if curRoutes, ok := c[name]; ok {
					if !sameRoutes(name, routes, name, curRoutes) {
						changes = append(changes, procedureChange{Airport: icao, Kind: kind, Old: name, New: name,
							OldRoutes: routes, NewRoutes: curRoutes})
					}
					continue
				}

				change := procedureChange{Airport: icao, Kind: kind, Old: name, OldRoutes: routes}
				if kind != "approach" {
					successors := slices.DeleteFunc(util.SortedMapKeys(c), func(n string) bool {
						_, inOld := o[n]
						return inOld || paired[n] || procedureBase(n) != procedureBase(name)
					})
					if len(successors) == 1 {
						change.New, change.NewRoutes = successors[0], c[successors[0]]
						paired[change.New] = true
					}
				}
				changes = append(changes, change)
			}
			for name, routes := range util.SortedMap(c) {
				if _, ok := o[name]; !ok && !paired[name] {
					changes = append(changes, procedureChange{Airport: icao, Kind: kind, New: name, NewRoutes: routes})
				}
			}
		}
	}
	return changes
}

// routeKeys returns the routes as strings that compare equal across a
// renumbering: each route's name with the procedure's name trimmed off,
// along with the route itself.
func routeKeys(name string, routes []av.CIFPRoute) []string {
	return util.MapSlice(routes, func(r av.CIFPRoute) string {
		return strings.TrimPrefix(r.Name, name) + " " + r.Route
	})
}

func sameRoutes(oldName string, old []av.CIFPRoute, newName string, cur []av.CIFPRoute) bool {
	o, c := routeKeys(oldName, old), routeKeys(newName, cur)
	slices.Sort(o)
	slices.Sort(c)
	return slices.Equal(o, c)
}

// printProcedureChanges reports the changes at the airports the scenarios
// use, or at all of them with -all, showing the routes a renumbered or
// recoded procedure no longer flies and the ones it now does.
func printProcedureChanges(changes []procedureChange, used map[av.ICAOAirportCode]bool) {
	fmt.Println("Procedure changes at", util.Select(*allAirports, "all airports:", "the scenarios' airports:"))

	printRoutes := func(prefix, name string, routes []av.CIFPRoute, omit []string) {
		for _, r := range routes {
			if !slices.Contains(omit, strings.TrimPrefix(r.Name, name)+" "+r.Route) {
				fmt.Printf("    %s %-14s: %s\n", prefix, r.Name, r.Route)
			}
		}
	}

	var airport av.ICAOAirportCode
	for _, c := range changes {
		if !*allAirports && !used[c.Airport] {
			continue
		}
		if c.Airport != airport {
			airport = c.Airport
			fmt.Printf("%s\n", airport)
		}

		switch {
		case c.Old == "":
			fmt.Printf("  %s %s added\n", c.Kind, c.New)
			printRoutes("+", c.New, c.NewRoutes, nil)
		case c.New == "":
			fmt.Printf("  %s %s removed\n", c.Kind, c.Old)
			printRoutes("-", c.Old, c.OldRoutes, nil)
		case sameRoutes(c.Old, c.OldRoutes, c.New, c.NewRoutes):
			fmt.Printf("  %s %s -> %s: routes unchanged\n", c.Kind, c.Old, c.New)
		default:
			if c.Old == c.New {
				fmt.Printf("  %s %s recoded\n", c.Kind, c.Old)
			} else {
				fmt.Printf("  %s %s -> %s\n", c.Kind, c.Old, c.New)
			}
			printRoutes("-", c.Old, c.OldRoutes, routeKeys(c.New, c.NewRoutes))
			printRoutes("+", c.New, c.NewRoutes, routeKeys(c.Old, c.OldRoutes))
		}
	}
}

// procedureNotes describes each SID and STAR name that the new cycle
// renumbered or dropped at some airport: what replaced it there and where it
// is still charted, if anywhere. Approaches are left out since their ids
// recur at every airport with a runway of the same number.
func procedureNotes(changes []procedureChange, cur db.ARINC424Result) map[string]string {
	outcomes := make(map[string]map[string][]string) // old name -> outcome -> airports
	for _, c := range changes {
		if c.Kind == "approach" || c.Old == "" || c.Old == c.New {
			continue
		}
		outcome := util.Select(c.New == "", "removed", "now "+c.New)
		if outcomes[c.Old] == nil {
			outcomes[c.Old] = make(map[string][]string)
		}
		outcomes[c.Old][outcome] = append(outcomes[c.Old][outcome], string(c.Airport))
	}

	notes := make(map[string]string)
	for name, byOutcome := range outcomes {
		var parts []string
		for outcome, airports := range util.SortedMap(byOutcome) {
			parts = append(parts, outcome+" at "+strings.Join(airports, ","))
		}
		var still []string
		for icao, ap := range util.SortedMap(cur.Airports) {
			_, isSID := ap.SIDs[name]
			_, isSTAR := ap.STARs[name]
			if isSID || isSTAR {
				still = append(still, string(icao))
			}
		}
		if len(still) > 0 {
			parts = append(parts, "still charted at "+strings.Join(still, ","))
		}
		notes[name] = strings.Join(parts, "; ")
	}
	return notes
}

// diffFixes returns the fixes and navaids that the new cycle doesn't have
// and the ones it moved at least -moved nm, with the distance moved.
func diffFixes(old, cur db.ARINC424Result) ([]string, map[string]float32) {
	locations := func(r db.ARINC424Result) map[string]math.Point2LL {
		m := make(map[string]math.Point2LL)
		for id, fix := range r.Fixes {
			m[id] = fix.Location
		}
		for id, navaid := range r.Navaids {
			m[id] = navaid.Location
		}
		return m
	}
	o, c := locations(old), locations(cur)

	var removed []string
	moved := make(map[string]float32)
	for id, p := range util.SortedMap(o) {
		q, ok := c[id]
		if !ok {
			removed = append(removed, id)
		} else if !p.IsZero() && !q.IsZero() {
			if nm := math.NMDistance2LL(p, q); nm >= float32(*movedNM) {
				moved[id] = nm
			}
		}
	}
	return removed, moved
}

// printReferences lists the lines of the scenarios and facility
// configurations that name something the notes describe.
func printReferences(dir string, notes map[string]string) error {
	scenarios, err := filepath.Glob(filepath.Join(dir, "scenarios", "*.json"))
	if err != nil {
		return err
	}
	configs, err := filepath.Glob(filepath.Join(dir, "configurations", "*", "*.json"))
	if err != nil {
		return err
	}

	// A name runs from a capital letter through the letters and digits that
	// follow it, so "50_BAGGY3" and "KOAK/QUAKE2" yield the procedure names.
	nameRE := regexp.MustCompile(`[A-Z][A-Z0-9]*`)
	refs := make(map[string]map[string][]int) // name -> file -> lines
	for _, file := range append(scenarios, configs...) {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, name := range nameRE.FindAllString(line, -1) {
				if _, ok := notes[name]; !ok {
					continue
				}
				if refs[name] == nil {
					refs[name] = make(map[string][]int)
				}
				if lines := refs[name][file]; len(lines) == 0 || lines[len(lines)-1] != i+1 {
					refs[name][file] = append(lines, i+1)
				}
			}
		}
	}

	fmt.Printf("\nReferences in %s:\n", dir)
	if len(refs) == 0 {
		fmt.Println("  none")
	}
	for name, files := range util.SortedMap(refs) {
		fmt.Printf("%s: %s\n", name, notes[name])
		for file, lines := range util.SortedMap(files) {
			fmt.Printf("    %s:%s\n", file, strings.Join(util.MapSlice(lines, func(l int) string { return fmt.Sprint(l) }), ","))
		}
	}
	return nil
}
