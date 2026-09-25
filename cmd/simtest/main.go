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
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"

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

// run checks the logs the arguments name and returns the exit status.
func run(args []string) int {
	logs, err := findLogs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
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
	if arch := sess.Header.GOARCH; arch != runtime.GOARCH {
		fmt.Fprintf(t.out, "Recorded on %s; floating-point arithmetic there may differ enough to exceed tight tolerances.\n",
			arch)
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
	h.GOARCH, h.Revision = runtime.GOARCH, simlog.Revision()
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
