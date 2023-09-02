// log.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger provides a simple logging system with a few different log levels;
// debugging and verbose output may both be suppressed independently.
type Logger struct {
	w, errors io.Writer
	start     time.Time
}

func NewLogger(server bool, printToStderr bool) *Logger {
	l := &Logger{
		start: time.Now(),
	}

	if server {
		l.w = &lumberjack.Logger{
			Filename: "vice-logs/full",
			MaxSize:  100, // MB
			MaxAge:   14,
			Compress: false,
		}
		l.errors = &lumberjack.Logger{
			Filename: "vice-logs/errors",
			MaxSize:  10, // MB
			MaxAge:   14,
			Compress: false,
		}
	} else {
		dir, err := os.UserConfigDir()
		if err != nil {
			lg.Errorf("Unable to find user config dir: %v", err)
			dir = "."
		}
		fn := path.Join(dir, "Vice", "vice.log")

		l.w, err = os.Create(fn)
		if err != nil {
			lg.Errorf("%s: %v", fn, err)
			l.w = os.Stderr
		} else if printToStderr {
			l.w = io.MultiWriter(l.w, os.Stderr)
		}
	}

	// Start out the logs with some basic information about the system
	// we're running on and the build of vice that's being used.
	l.Infof("Hello logging at %s", time.Now())
	l.Infof("Arch: %s OS: %s CPUs: %d", runtime.GOARCH, runtime.GOOS, runtime.NumCPU())
	if bi, ok := debug.ReadBuildInfo(); ok {
		l.Infof("Build: go %s path %s", bi.GoVersion, bi.Path)
		for _, dep := range bi.Deps {
			if dep.Replace == nil {
				l.Infof("Module %s @ %s", dep.Path, dep.Version)
			} else {
				l.Infof("Module %s @ %s replaced by %s @ %s", dep.Path, dep.Version,
					dep.Replace.Path, dep.Replace.Version)
			}
		}
		for _, setting := range bi.Settings {
			l.Infof("Build setting %s = %s", setting.Key, setting.Value)
		}
	}

	return l
}

// Infof adds the given message, specified using Infof-style format
// string, to the "verbose" log.  If verbose logging is not enabled, the
// message is discarded.
func (l *Logger) Infof(f string, args ...interface{}) {
	l.printf(3, f, args...)
}

// InfofUp1 adds the given message to the error log, but with reported the
// source file and line number are one level up in the call stack from the
// function that called it.
func (l *Logger) InfofUp1(f string, args ...interface{}) {
	l.printf(4, f, args...)
}

func (l *Logger) printf(levels int, f string, args ...interface{}) {
	if l == nil {
		// ignore
		return
	}

	msg := l.format(levels, f, args...)
	l.w.Write([]byte(msg))
}

// Errorf adds the given message, specified using Printf-style format
// string, to the error log.
func (l *Logger) Errorf(f string, args ...interface{}) {
	if l == nil {
		fmt.Fprintf(os.Stderr, "ERROR: "+f, args...)
		return
	}
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
	msg := l.format(levels, "ERROR: "+f, args...)

	// Always print it
	fmt.Fprint(os.Stderr, msg)

	if l.errors != nil {
		l.errors.Write([]byte(msg))
	}
	l.w.Write([]byte(msg))
}

// format is a utility function for formatting logging messages. It
// prepends the source file and line number of the logging call to the
// returned message string.
func (l *Logger) format(levels int, f string, args ...interface{}) string {
	// Go up the call stack the specified nubmer of levels
	_, fn, line, _ := runtime.Caller(levels)

	// Current time
	s := time.Now().Format(time.RFC1123) + " "

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
	render    RendererStats
	renderUI  RendererStats
	drawImgui time.Duration
	drawPanes time.Duration
	startTime time.Time
	redraws   int
}

var startupMallocs uint64

// LogStats adds the proivded Stats to the log and also includes information about
// the current system performance, memory use, etc.
func (l *Logger) LogStats(stats Stats) {
	lg.Infof("Redraws per second: %.1f", float64(stats.redraws)/time.Since(stats.startTime).Seconds())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if startupMallocs == 0 {
		startupMallocs = mem.Mallocs
	}

	elapsed := time.Since(l.start).Seconds()
	mallocsPerSecond := int(float64(mem.Mallocs-startupMallocs) / elapsed)
	active1000s := (mem.Mallocs - mem.Frees) / 1000
	lg.Infof("Stats: mallocs/second %d (%dk active) %d MB in use", mallocsPerSecond, active1000s,
		mem.HeapAlloc/(1024*1024))

	lg.Infof("Stats: draw panes %s draw imgui %s", stats.drawPanes.String(), stats.drawImgui.String())

	lg.Infof("Stats: rendering: %s", stats.render.String())
	lg.Infof("Stats: UI rendering: %s", stats.renderUI.String())
}
