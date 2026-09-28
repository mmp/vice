// cmd/simtest/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// simtest replays session logs that a vice server recorded (see package
// simlog) with the current code and reports the aircraft that fly
// differently than they did in the session: the controllers' instructions
// to each, and where and how it diverged. It is an end-to-end test of nav
// and sim against sessions that people flew.
//
// Usage:
//
//	simtest [flags] <log.simlog | directory>...
//
// A directory stands for the logs in it. With -bless, each log is replaced
// by its replay, making the current code's flights the reference that later
// runs are compared with. The exit status is 1 if any replay differed.
//
// With -summary, simtest doesn't replay the logs but describes what
// happened in each session: the aircraft of each kind of flight, how many
// of them the controllers worked, what became of them, and the requests the
// controllers made.
package main

import (
	"cmp"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/simlog"
	"github.com/mmp/vice/util"
)

var (
	bless     = flag.Bool("bless", false, "replace each log with its replay, making the current code's flights the reference")
	selfCheck = flag.Bool("selfcheck", false, "also replay each log a second time and compare the two replays, to find nondeterminism")
	callsign  = flag.String("callsign", "", "report only the aircraft with this `callsign`")
	exact     = flag.Bool("exact", false, "count any difference at all as flying differently")
	posTol    = flag.Float64("pos", 0.05, "position `tolerance`, in nautical miles")
	altTol    = flag.Float64("alt", 25, "altitude `tolerance`, in feet")
	speedTol  = flag.Float64("speed", 2, "speed `tolerance`, in knots")
	hdgTol    = flag.Float64("hdg", 2, "heading `tolerance`, in degrees")
	logLevel  = flag.String("loglevel", "error", "`level` of the sim's own log messages to write to stderr: debug, info, warn, error")
	summary   = flag.Bool("summary", false, "describe what happened in each log's session instead of replaying it")

	navLogEnabled    = flag.Bool("navlog", false, "enable navigation logging (requires the navlog build tag)")
	navLogCategories = flag.String("navlog-categories", "all", "navigation log `categories`")
	navLogCallsign   = flag.String("navlog-callsign", "", "filter navigation logs to only show this `callsign`")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: simtest [flags] <log.simlog | directory>...\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	os.Exit(run(flag.Args()))
}

// run checks or summarizes the logs the arguments name and returns the exit
// status.
func run(args []string) int {
	logs, err := findLogs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if *summary {
		status := 0
		for _, path := range logs {
			sess, err := simlog.Load(path)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				status = 1
				continue
			}
			fmt.Println(filepath.Base(path))
			summarize(os.Stdout, sess)
			fmt.Println()
		}
		return status
	}

	// Replays run in an empty directory to be sure that there are no inadvertent
	// reads of resources/.
	empty, err := os.MkdirTemp("", "simtest")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(empty)
	if err := os.Chdir(empty); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	nav.InitNavLog(*navLogEnabled, *navLogCategories, *navLogCallsign)

	// The sim prints some of what it does as it runs, which is only noise
	// around the reports unless it's nav logging that was asked for.
	out := os.Stdout
	if !*navLogEnabled {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer devNull.Close()
		os.Stdout = devNull
		defer func() { os.Stdout = out }()
	}

	tol := simlog.Tolerances{
		Position: float32(*posTol),
		Altitude: float32(*altTol),
		Speed:    float32(*speedTol),
		Heading:  float32(*hdgTol),
	}
	if *exact {
		tol = simlog.Tolerances{}
	}

	t := tester{out: out, lg: makeLogger(*logLevel), tol: tol, databases: make(map[string]*db.StaticDatabase)}
	status := 0
	for _, path := range logs {
		if !t.check(path) {
			status = 1
		}
	}
	return status
}

// findLogs returns the absolute paths of the logs the arguments name.
func findLogs(args []string) ([]string, error) {
	var logs []string
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		paths := []string{arg}
		if fi.IsDir() {
			if paths, err = filepath.Glob(filepath.Join(arg, "*.simlog")); err != nil {
				return nil, err
			}
			slices.Sort(paths)
		}
		for _, p := range paths {
			abs, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			logs = append(logs, abs)
		}
	}
	return logs, nil
}

func makeLogger(level string) *log.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid log level\n", level)
		os.Exit(2)
	}
	return &log.Logger{Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))}
}

type tester struct {
	out       io.Writer // where reports go
	lg        *log.Logger
	tol       simlog.Tolerances
	databases map[string]*db.StaticDatabase // keyed by hash
}

// check replays the log at path, reports how the replay differed, and
// returns whether it didn't.
func (t *tester) check(path string) bool {
	fmt.Fprintln(t.out, filepath.Base(path))
	fail := func(err error) bool {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return false
	}

	sess, err := simlog.Load(path)
	if err != nil {
		return fail(err)
	}
	if rec, cur := sess.Header.Build, simlog.CurrentBuild(); !rec.SameArithmetic(cur) {
		fmt.Fprintf(t.out, "Recorded on %s; replaying on %s. Floating point may round differently, and a "+
			"difference too small to see can go on to change which aircraft spawn.\n", rec.Platform(), cur.Platform())
		if rec.GOARCH != cur.GOARCH || rec.GOAMD64 != cur.GOAMD64 {
			env := "GOARCH=" + rec.GOARCH
			if rec.GOAMD64 != "" {
				env += " GOAMD64=" + rec.GOAMD64
			}
			fmt.Fprintf(t.out, "To replay it on the architecture it was recorded on: %s go run ./cmd/simtest %s\n", env, path)
		}
	}

	dir := filepath.Dir(path)
	d, ok := t.databases[sess.Header.Database]
	if !ok {
		if d, err = simlog.LoadDatabase(dir, sess.Header.Database); err != nil {
			return fail(err)
		}
		t.databases[sess.Header.Database] = d
	}
	db.DB = d

	replayPath, replay, err := t.replay(sess, dir)
	if replay == nil {
		return fail(err)
	}
	if err != nil {
		// Report how far it got.
		fmt.Fprintf(t.out, "Replay stopped: %v\n", err)
	}

	repeatable := true
	if *selfCheck {
		againPath, again, err := t.replay(sess, dir)
		if again == nil {
			return fail(err)
		}
		os.Remove(againPath)
		if report := simlog.Compare(replay, again, simlog.Tolerances{}); report.Diverged() {
			fmt.Fprintln(t.out, "Two replays of the log differ, so something in the sim isn't deterministic:")
			report.Write(t.out)
			repeatable = false
		}
	}

	report := simlog.Compare(sess, replay, t.tol)
	if *callsign != "" {
		report.Aircraft = util.FilterSlice(report.Aircraft,
			func(ar simlog.AircraftReport) bool { return ar.Callsign == *callsign })
	}
	report.Write(t.out)
	passed := err == nil && repeatable && !report.Diverged()

	// A replay that didn't finish, or that isn't repeatable, is no
	// reference.
	if *bless && err == nil && repeatable {
		if err := os.Rename(replayPath, path); err != nil {
			return fail(err)
		}
		fmt.Fprintln(t.out, "Blessed the replay as the new reference.")
		passed = true
	} else {
		os.Remove(replayPath)
	}
	fmt.Fprintln(t.out)

	return passed
}

// replay replays sess into a new log in dir, returning the log's path and
// contents. If the replay stops partway, it returns the log of as much as
// it did along with the error.
func (t *tester) replay(sess *simlog.Session, dir string) (string, *simlog.Session, error) {
	f, err := os.CreateTemp(dir, "replay-*.simlog.tmp")
	if err != nil {
		return "", nil, err
	}

	h := sess.Header
	h.Build = simlog.CurrentBuild()
	w, err := simlog.NewWriter(f, h, sess.Snapshot)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}

	replayErr := server.Replay(sess, w, t.lg)
	if err := w.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}

	replay, err := simlog.Load(f.Name())
	if err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), replay, replayErr
}

///////////////////////////////////////////////////////////////////////////
// Summaries

// flightKind is a kind of flight, like IFR arrivals.
type flightKind struct{ rules, flight string }

// loggedAircraft is what a session log says of one aircraft's time in the
// sim.
type loggedAircraft struct {
	kind   flightKind
	flew   bool   // it was ever flying, rather than only waiting to depart
	worked bool   // a controller made a request about it that the sim carried out
	end    string // why it left the sim, or "" if it was still there when the log ended
}

// summarize describes what happened in a session: how long it ran, the
// aircraft of each kind of flight and what became of them, and the requests
// the controllers made.
func summarize(w io.Writer, sess *simlog.Session) {
	var aircraft []*loggedAircraft
	inSim := make(map[string]*loggedAircraft)                       // by callsign
	requests, refused := make(map[string]int), make(map[string]int) // by method
	tcws := make(map[string]bool)
	var end time.Time
	for _, e := range sess.Events {
		switch e.Kind {
		case simlog.KindSpawn:
			ac := &loggedAircraft{kind: flightKind{rules: e.Spawn.Rules, flight: e.Spawn.Flight}}
			aircraft = append(aircraft, ac)
			inSim[e.Spawn.Callsign] = ac
		case simlog.KindDelete:
			if ac, ok := inSim[e.Delete.Callsign]; ok {
				ac.end = e.Delete.Reason
				delete(inSim, e.Delete.Callsign)
			}
		case simlog.KindTick:
			end = e.Tick.Time
			for _, s := range e.Tick.Aircraft {
				if ac, ok := inSim[s.Callsign]; ok {
					ac.flew = true
				}
			}
		case simlog.KindRequest:
			r := e.Request
			method := r.Method[strings.LastIndex(r.Method, ".")+1:]
			requests[method]++
			tcws[r.TCW] = true
			if r.Error != "" {
				refused[method]++
			} else if ac, ok := inSim[r.Aircraft]; ok && r.Method != server.LaunchAircraftRPC {
				// Launching an aircraft brings it into the sim but isn't
				// working it.
				ac.worked = true
			}
		}
	}

	h := sess.Header
	fmt.Fprintln(w, h.Describe())
	if end.IsZero() {
		fmt.Fprintln(w, "The sim never ran.")
	} else {
		fmt.Fprintf(w, "Ran %s of sim time, %s to %s.\n", end.Sub(h.SimStart).Round(time.Second),
			h.SimStart.UTC().Format(time.TimeOnly), end.UTC().Format(time.TimeOnly))
	}
	writeAircraftTable(w, aircraft)
	writeRequestCounts(w, requests, refused, util.SortedMapKeys(tcws))
}

// writeAircraftTable writes a table with a column for each kind of flight
// and one for all of them. Its rows count the aircraft, the ones the
// controllers worked, and what became of them.
func writeAircraftTable(w io.Writer, aircraft []*loggedAircraft) {
	kinds := util.MapSlice(aircraft, func(ac *loggedAircraft) flightKind { return ac.kind })
	slices.SortFunc(kinds, func(a, b flightKind) int {
		return cmp.Or(cmp.Compare(a.flight, b.flight), cmp.Compare(a.rules, b.rules))
	})
	kinds = slices.Compact(kinds)

	type row struct {
		label  string
		counts []int // for each kind, then for all of them
	}
	count := func(label string, pred func(*loggedAircraft) bool) row {
		r := row{label: label, counts: make([]int, len(kinds)+1)}
		for _, ac := range aircraft {
			if pred(ac) {
				r.counts[slices.Index(kinds, ac.kind)]++
				r.counts[len(kinds)]++
			}
		}
		return r
	}
	total := func(r row) int { return r.counts[len(kinds)] }

	reasons := util.MapSlice(aircraft, func(ac *loggedAircraft) string { return ac.end })
	slices.Sort(reasons)
	reasons = slices.DeleteFunc(slices.Compact(reasons), func(r string) bool { return r == "" })
	var ends []row
	for _, reason := range reasons {
		ends = append(ends, count(reason, func(ac *loggedAircraft) bool { return ac.end == reason }))
	}
	slices.SortStableFunc(ends, func(a, b row) int { return total(b) - total(a) })
	ends = append(ends,
		count("still flying", func(ac *loggedAircraft) bool { return ac.end == "" && ac.flew }),
		count("never flew", func(ac *loggedAircraft) bool { return ac.end == "" && !ac.flew }))
	ends = slices.DeleteFunc(ends, func(r row) bool { return total(r) == 0 })

	rows := append([]row{
		count("aircraft", func(*loggedAircraft) bool { return true }),
		count("worked", func(ac *loggedAircraft) bool { return ac.worked }),
	}, ends...)

	rules := append(util.MapSlice(kinds, func(k flightKind) string { return k.rules }), "")
	flights := append(util.MapSlice(kinds, func(k flightKind) string { return k.flight + "s" }), "total")
	widths := make([]int, len(kinds)+1)
	for i := range widths {
		widths[i] = max(len(rules[i]), len(flights[i]), len(strconv.Itoa(rows[0].counts[i])))
	}
	labelWidth := 0
	for _, r := range rows {
		labelWidth = max(labelWidth, len(r.label))
	}

	line := func(label string, cells []string) {
		var s strings.Builder
		fmt.Fprintf(&s, "  %-*s", labelWidth, label)
		for i, c := range cells {
			fmt.Fprintf(&s, "  %*s", widths[i], c)
		}
		fmt.Fprintln(w, strings.TrimRight(s.String(), " "))
	}
	line("", rules)
	line("", flights)
	for _, r := range rows {
		line(r.label, util.MapSlice(r.counts, strconv.Itoa))
	}
}

// writeRequestCounts writes how many requests the controllers at the given
// positions made with each method and how many of them the sim refused.
func writeRequestCounts(w io.Writer, requests, refused map[string]int, tcws []string) {
	if len(requests) == 0 {
		fmt.Fprintln(w, "The controllers made no requests.")
		return
	}

	refusals := func(n int) string {
		return util.Select(n == 0, "", fmt.Sprintf(" (%d refused)", n))
	}
	total, totalRefused := 0, 0
	for method, n := range requests {
		total += n
		totalRefused += refused[method]
	}
	fmt.Fprintf(w, "Controllers at %s made %d %s%s:\n", strings.Join(tcws, ", "), total,
		util.Select(total == 1, "request", "requests"), refusals(totalRefused))

	methods := util.SortedMapKeys(requests)
	slices.SortStableFunc(methods, func(a, b string) int { return requests[b] - requests[a] })
	width := len(strconv.Itoa(requests[methods[0]]))
	for _, m := range methods {
		fmt.Fprintf(w, "  %*d %s%s\n", width, requests[m], m, refusals(refused[m]))
	}
}
