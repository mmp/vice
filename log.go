// log.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Logger provides a simple logging system with a few different log levels;
// debugging and verbose output may both be suppressed independently.
type Logger struct {
	// Note that CircularLogBuffer is not thread-safe, so Logger must hold
	// this mutex before calling into it.
	mu            sync.Mutex
	printToStderr bool
	verbose       *CircularLogBuffer
	err           *CircularLogBuffer
	start         time.Time
	monitors      map[ErrorMonitor]interface{}
}

// ErrorMonitor is an interface for objects that wish to be made aware of
// error messages from elsewhere in the system. Logger passes messages
// along to the monitors that have registered with it.
type ErrorMonitor interface {
	ErrorReported(msg string)
}

// Each log message is stored using a LogEntry, which also records the time
// it was submitted (w.r.t. the time at which logging was initialized.)
type LogEntry struct {
	message string
	offset  time.Duration
}

func (l *LogEntry) String() string {
	return fmt.Sprintf("%16s %s", l.offset.Round(time.Millisecond), l.message)
}

// CircularLogBuffer stores a fixed maximum number of logging messages; this
// lets us hold on to logging messages for use in bug reports without worrying
// about whether we're using too much memory to do so.
type CircularLogBuffer struct {
	entries []LogEntry
	max     int
	index   int
	start   time.Time
}

func (c *CircularLogBuffer) Add(s string) {
	e := LogEntry{message: s, offset: time.Since(c.start)}
	if len(c.entries) < c.max {
		// Append to the entries slice if it hasn't yet hit the limit.
		c.entries = append(c.entries, e)
	} else {
		// Otherwise treat c.entries as a ring buffer where
		// (c.index+1)%c.max is the oldest entry and successive newer
		// entries follow.
		c.entries[c.index%c.max] = e
	}
	c.index++
}

func (c *CircularLogBuffer) String() string {
	var b strings.Builder
	for i := 0; i < len(c.entries); i++ {
		b.WriteString(c.entries[(c.index+i)%len(c.entries)].String())
	}
	return b.String()
}

func NewCircularLogBuffer(maxLines int) *CircularLogBuffer {
	return &CircularLogBuffer{max: maxLines, start: time.Now()}
}

func NewLogger(verbose bool, printToStderr bool, maxLines int) *Logger {
	l := &Logger{printToStderr: printToStderr, start: time.Now()}
	if verbose {
		l.verbose = NewCircularLogBuffer(maxLines)
	}
	l.err = NewCircularLogBuffer(maxLines)
	l.monitors = make(map[ErrorMonitor]interface{})

	// Start out the logs with some basic information about the system
	// we're running on and the build of vice that's being used.
	l.Printf("Hello logging at %s", time.Now())
	l.Printf("Arch: %s OS: %s CPUs: %d", runtime.GOARCH, runtime.GOOS, runtime.NumCPU())
	if bi, ok := debug.ReadBuildInfo(); ok {
		l.Printf("Build: go %s path %s", bi.GoVersion, bi.Path)
		for _, dep := range bi.Deps {
			if dep.Replace == nil {
				l.Printf("Module %s @ %s", dep.Path, dep.Version)
			} else {
				l.Printf("Module %s @ %s replaced by %s @ %s", dep.Path, dep.Version,
					dep.Replace.Path, dep.Replace.Version)
			}
		}
		for _, setting := range bi.Settings {
			l.Printf("Build setting %s = %s", setting.Key, setting.Value)
		}
	}

	return l
}

// Printf adds the given message, specified using Printf-style format
// string, to the "verbose" log.  If verbose logging is not enabled, the
// message is discarded.
func (l *Logger) Printf(f string, args ...interface{}) {
	if l.verbose == nil {
		return
	}

	msg := l.format(2, f, args...)
	if l.printToStderr {
		fmt.Fprint(os.Stderr, msg)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.verbose.Add(msg)
}

// Errorf adds the given message, specified using Printf-style format
// string, to the error log.
func (l *Logger) Errorf(f string, args ...interface{}) {
	l.errorf(3, f, args...)
}

// ErrorfUp1 adds the given message to the error log, though the source
// file and line number logged are one level up in the call stack from the
// function that calls ErrorfUp1. (This can be useful for functions that
// are called from many places and where the context of the calling
// function is more likely to be useful for debugging the error.)
func (l *Logger) ErrorfUp1(f string, args ...interface{}) {
	l.errorf(4, f, args...)
}

func (l *Logger) errorf(levels int, f string, args ...interface{}) {
	msg := l.format(levels, f, args...)

	// Always print it
	fmt.Fprint(os.Stderr, "ERROR: "+msg)

	for m := range l.monitors {
		m.ErrorReported(msg)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.err.Add(msg)
}

func (l *Logger) RegisterErrorMonitor(m ErrorMonitor) {
	l.monitors[m] = nil

	// Poke into the error buffer and forward along anything that's already
	// in there...
	for i := 0; i < len(l.err.entries); i++ {
		idx := (i + l.err.index) % len(l.err.entries)
		m.ErrorReported(l.err.entries[idx].message)
	}
}

func (l *Logger) DeregisterErrorMonitor(m ErrorMonitor) {
	delete(l.monitors, m)
}

func (l *Logger) GetVerboseLog() string {
	if l.verbose == nil {
		return ""
	}
	return l.verbose.String()
}

func (l *Logger) GetErrorLog() string {
	return l.err.String()
}

// format is a utility function for formatting logging messages. It
// prepends the source file and line number of the logging call to the
// returned message string.
func (l *Logger) format(levels int, f string, args ...interface{}) string {
	// Go up the call stack the specified nubmer of levels
	_, fn, line, _ := runtime.Caller(levels)

	// Elapsed time
	s := fmt.Sprintf("%8.2fs ", time.Since(l.start).Seconds())

	// Source file and line
	fnline := path.Base(fn) + fmt.Sprintf(":%d", line)
	s += fmt.Sprintf("%-20s ", fnline)

	// Add the provided logging message.
	s += fmt.Sprintf(f, args...)

	// The message shouldn't have a newline at the end but if it does, we
	// won't gratuitously add another one.
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// Stats collects a few statistics related to rendering and time spent in
// various phases of the system.
type Stats struct {
	render          RendererStats
	processMessages time.Duration
	drawImgui       time.Duration
	drawPanes       time.Duration
	startTime       time.Time
	redraws         int
}

var startupMallocs uint64

// LogStats adds the proivded Stats to the log and also includes information about
// the current system performance, memory use, etc.
func (l *Logger) LogStats(stats Stats) {
	lg.Printf("Redraws per second: %.1f", float64(stats.redraws)/time.Since(stats.startTime).Seconds())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if startupMallocs == 0 {
		startupMallocs = mem.Mallocs
	}

	elapsed := time.Since(l.start).Seconds()
	mallocsPerSecond := int(float64(mem.Mallocs-startupMallocs) / elapsed)
	active1000s := (mem.Mallocs - mem.Frees) / 1000
	lg.Printf("Stats: mallocs/second %d (%dk active) %d MB in use", mallocsPerSecond, active1000s,
		mem.HeapAlloc/(1024*1024))

	lg.Printf("Stats: process messages %s draw panes %s draw imgui %s",
		stats.processMessages.String(), stats.drawPanes.String(), stats.drawImgui.String())

	lg.Printf("Stats: rendering: %s", stats.render.String())
}

func (l *Logger) SaveLogs() {
	dir, err := os.UserConfigDir()
	if err != nil {
		lg.Errorf("Unable to find user config dir: %v", err)
		dir = "."
	}

	fn := path.Join(dir, "Vice", "vice.log")
	s := l.verbose.String() + "\n-----\nErrors:\n" + l.err.String()
	os.WriteFile(fn, []byte(s), 0600)
}
