// pkg/log/log.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const CrashReportServer = "localhost"
const CrashReportPort = "6504"
const CrashReportPath = "/crash"
const CrashReportURL = "http://" + CrashReportServer + ":" + CrashReportPort + CrashReportPath

type Logger struct {
	*slog.Logger
	LogFile string
	LogDir  string
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
		LogDir:  dir,
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

	if server {
		go l.launchCrashServer()
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

func (l *Logger) CatchAndReportCrash() any {
	// Janky way to check if we're running under the debugger.
	if dlv, ok := os.LookupEnv("_"); ok && strings.HasSuffix(dlv, "/dlv") {
		return nil
	}

	err := recover()
	if err != nil {
		l.Errorf("Crashed: %v", err)

		// Format the report information
		report := fmt.Sprintf("Crashed: %v\n", err)
		report += "Sys: " + runtime.GOARCH + "/" + runtime.GOOS + "\n"

		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range bi.Settings {
				report += setting.Key + ": " + setting.Value + "\n"
			}
		}
		report += string(debug.Stack())

		// Print it to stdout
		fmt.Println(report)

		// Try to save it to disk locally
		fn := filepath.Join(l.LogDir, "crash-"+time.Now().Format(time.RFC3339)+".txt")
		_ = os.WriteFile(fn, []byte(report), 0o600)

		// And pass it along to the crash report server.
		l.postCrashReport(report)
	}

	return err
}

func (l *Logger) postCrashReport(report string) {
	req, err := http.NewRequest("POST", CrashReportURL, strings.NewReader(report))
	if err != nil {
		l.Errorf("Error creating request: %v", err)
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		l.Errorf("Error sending request: %v", err)
		return
	}
	defer resp.Body.Close()

	// Handle the response
	if responseBody, err := io.ReadAll(resp.Body); err != nil {
		l.Errorf("Error reading response: %v", err)
	} else {
		l.Infof("Response: %s", responseBody)
	}
}

func (l *Logger) launchCrashServer() {
	mux := http.NewServeMux()
	mux.HandleFunc(CrashReportPath, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}

		lr := &io.LimitedReader{R: r.Body, N: 4 * 1024}
		body, err := io.ReadAll(lr)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusInternalServerError)
			return
		}

		l.Info("Received crash report", slog.String("crash", string(body)))

		fn := filepath.Join(l.LogDir, "crash-"+time.Now().Format(time.RFC3339)+".txt")
		_ = os.WriteFile(fn, []byte(body), 0o600)
	})

	srv := &http.Server{
		Addr:    ":" + CrashReportPort,
		Handler: mux,
	}

	if err := srv.ListenAndServe(); err != nil {
		l.Errorf("Failed to start HTTP server for crash reports: %v\n", err)
	}
}
