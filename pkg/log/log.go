// pkg/log/log.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

type Logger struct {
	*slog.Logger
	LogFile string
	Start   time.Time
}

func New(server bool, level string, dir string) *Logger {
	if dir == "" {
		if server {
			dir = "vice-logs"
		} else {
			var err error
			dir, err = os.UserConfigDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Unable to find user config dir: %v", err)
				dir = "."
			}
			dir = filepath.Join(dir, "Vice")
		}
	}

	var w *lumberjack.Logger
	if server {
		w = &lumberjack.Logger{
			Filename: filepath.Join(dir, "slog"),
			MaxSize:  64, // MB
			MaxAge:   14,
			Compress: true,
		}
	} else {
		w = &lumberjack.Logger{
			Filename:   filepath.Join(dir, "vice.slog"),
			MaxSize:    32, // MB
			MaxBackups: 1,
		}
		if level == "debug" {
			w.MaxSize = 512
		}
	}

	lvl := slog.LevelInfo
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "%s: invalid log level", level)
	}

	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	l := &Logger{
		Logger:  slog.New(h),
		LogFile: w.Filename,
		Start:   time.Now(),
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

// Debug wraps slog.Debug to add call stack information (and similarly for
// the following Logger methods...)  Note that we do not wrap the entire
// slog logging interface, so, for example, WarnContext and Log do not have
// callstacks included.
//
// We also wrap the logging methods to allow a nil *Logger, in which case
// debug and info messages are discarded (though warnings and errors still
// go through to slog.)
func (l *Logger) Debug(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelDebug) {
		args = append([]any{slog.Any("callstack", Callstack(nil))}, args...)
		l.Logger.Debug(msg, args...)
	}
}

// Debugf is a convenience wrapper that logs just a message and allows
// printf-style formatting of the provided args.
func (l *Logger) Debugf(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	}
}

func (l *Logger) Info(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelInfo) {
		args = append([]any{slog.Any("callstack", Callstack(nil))}, args...)
		l.Logger.Info(msg, args...)
	}
}

func (l *Logger) Infof(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelInfo) {
		l.Logger.Info(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	}
}

func (l *Logger) Warn(msg string, args ...any) {
	args = append([]any{slog.Any("callstack", Callstack(nil))}, args...)
	if l == nil {
		slog.Warn(msg, args...)
	} else {
		l.Logger.Warn(msg, args...)
	}
}

func (l *Logger) Warnf(msg string, args ...any) {
	if l == nil {
		slog.Warn(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	} else {
		l.Logger.Warn(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	}
}

func (l *Logger) Error(msg string, args ...any) {
	args = append([]any{slog.Any("callstack", Callstack(nil))}, args...)
	slog.Error(msg, args...)
	if l != nil {
		l.Logger.Error(msg, args...)
	}
}

func (l *Logger) Errorf(msg string, args ...any) {
	slog.Error(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	if l != nil {
		l.Logger.Error(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil)))
	}
}

func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger:  l.Logger.With(args...),
		LogFile: l.LogFile,
		Start:   l.Start,
	}
}
