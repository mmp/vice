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

	"golang.org/x/exp/slog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Logger provides a simple logging system with a few different log levels;
// debugging and verbose output may both be suppressed independently.
type Logger struct {
	*slog.Logger
	start time.Time
}

func NewLogger(server bool, printToStderr bool) *Logger {
	var w io.Writer

	if server {
		w = &lumberjack.Logger{
			Filename: "vice-logs/slog",
			MaxSize:  100, // MB
			MaxAge:   14,
			Compress: false,
		}
	} else {
		dir, err := os.UserConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Unable to find user config dir: %v", err)
			dir = "."
		}
		fn := path.Join(dir, "Vice", "vice.log")

		w, err = os.Create(fn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v", fn, err)
			w = os.Stderr
		} else if printToStderr {
			w = io.MultiWriter(w, os.Stderr)
		}
	}

	h := slog.NewJSONHandler(w, nil)
	l := &Logger{
		Logger: slog.New(h),
		start:  time.Now(),
	}

	// Start out the logs with some basic information about the system
	// we're running on and the build of vice that's being used.
	l.Info("Hello logging", slog.Time("start", time.Now()))
	l.Info("System information",
		slog.String("GOARCH", runtime.GOARCH),
		slog.String("GOOS", runtime.GOOS),
		slog.Int("NumCPUs", runtime.NumCPU()))

	var deps, settings []any
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			deps = append(deps, slog.String(dep.Path, dep.Version))
			if dep.Replace != nil {
				deps = append(deps, slog.String("Replacement "+dep.Replace.Path, dep.Replace.Version))
			}
		}
		for _, setting := range bi.Settings {
			settings = append(settings, slog.String(setting.Key, setting.Value))
		}

		l.Info("Build",
			slog.String("Go version", bi.GoVersion),
			slog.String("Path", bi.Path),
			slog.Group("Dependencies", deps...),
			slog.Group("Settings", settings...))
	}

	return l
}

// callstack returns slog.Attr representing the callstack, starting at the function
// that called into one of the logging functions
func callstack() slog.Attr {
	var callers [16]uintptr
	n := runtime.Callers(3, callers[:]) // skip up to function that is doing logging
	frames := runtime.CallersFrames(callers[:n])

	var s []any
	for i := 0; ; i++ {
		frame, more := frames.Next()
		s = append(s, slog.Group("frame"+fmt.Sprintf("%d", i),
			slog.String("file", path.Base(frame.File)),
			slog.Int("line", frame.Line),
			slog.String("function", strings.TrimPrefix(frame.Function, "main."))))

		// Don't keep going up into go runtime stack frames.
		if !more || frame.Function == "main.main" {
			break
		}
	}
	return slog.Group("callstack", s...)
}

// Debug wraps slog.Debug to add call stack information (and similarly for
// Info, Error, and Warn below). Note that we do not wrap the entire slog
// logging interface, so, for example, InfoContext or Log do not have
// callstacks included.
func (l *Logger) Debug(msg string, args ...any) {
	if l == nil {
		return
	}
	args = append([]any{callstack()}, args...)
	l.Logger.Debug(msg, args...)
}

// Debugf is a convenience wrapper that logs just a message and allows
// printf-style formatting of the provided args.
func (l *Logger) Debugf(msg string, args ...any) {
	if l == nil {
		return
	}
	l.Logger.Debug(fmt.Sprintf(msg, args...), callstack())
}

func (l *Logger) Info(msg string, args ...any) {
	if l == nil {
		return
	}
	args = append([]any{callstack()}, args...)
	l.Logger.Info(msg, args...)
}

func (l *Logger) Infof(msg string, args ...any) {
	if l == nil {
		return
	}
	l.Logger.Info(fmt.Sprintf(msg, args...), callstack())
}

func (l *Logger) Error(msg string, args ...any) {
	if l == nil {
		return
	}
	args = append([]any{callstack()}, args...)
	l.Logger.Error(msg, args...)
}

func (l *Logger) Errorf(msg string, args ...any) {
	if l == nil {
		return
	}
	l.Logger.Error(fmt.Sprintf(msg, args...), callstack())
}

func (l *Logger) Warn(msg string, args ...any) {
	if l == nil {
		return
	}
	args = append([]any{callstack()}, args...)
	l.Logger.Warn(msg, args...)
}

func (l *Logger) Warnf(msg string, args ...any) {
	if l == nil {
		return
	}
	l.Logger.Warn(fmt.Sprintf(msg, args...), callstack())
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

// Log adds the Stats to the provided Logger and also includes information
// about the current system performance, memory use, etc.
func (stats *Stats) Log(lg *Logger) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if startupMallocs == 0 { // first call
		startupMallocs = mem.Mallocs
	}

	elapsed := time.Since(lg.start).Seconds()
	mallocsPerSecond := float64(mem.Mallocs-startupMallocs) / elapsed

	lg.Info("Performance",
		slog.Float64("Redraws/second", float64(stats.redraws)/time.Since(stats.startTime).Seconds()),
		slog.Float64("Mallocs/second", mallocsPerSecond),
		slog.Int64("Active mallocs", int64(mem.Mallocs-mem.Frees)),
		slog.Int64("Memory in use", int64(mem.HeapAlloc)),
		slog.Duration("Draw panes", stats.drawPanes),
		slog.Duration("Draw imgui", stats.drawImgui),
		slog.Group("Render", stats.render.LogAttrs()...),
		slog.Group("UI", stats.renderUI.LogAttrs()...))
}
