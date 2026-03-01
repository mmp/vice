// pkg/log/log.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/rpc"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/cpu"
	"gopkg.in/natefinch/lumberjack.v2"
)

// SystemInfo contains system information for crash reports.
type SystemInfo struct {
	CPUCores    int
	CPUModel    string
	CPUVendor   string
	CPUFlags    []string
	GPUVendor   string
	GPURenderer string
	GoVersion   string
	OS          string
	Arch        string
}

// CrashReport is sent via RPC when a crash occurs.
type CrashReport struct {
	Report    string
	System    SystemInfo
	Timestamp time.Time
}

type Logger struct {
	*slog.Logger
	LogFile string
	LogDir  string
	Start   time.Time

	mu               sync.Mutex
	crashRPCClient   *rpc.Client
	crashClientReady chan struct{}
	crashClientOnce  sync.Once
	systemInfo       SystemInfo
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

	h := newHandler(w, &slog.HandlerOptions{Level: lvl})
	l := &Logger{
		Logger:           slog.New(h),
		LogFile:          w.Filename,
		LogDir:           dir,
		Start:            time.Now(),
		crashClientReady: make(chan struct{}),
	}

	// Collect CPU info for crash reports
	l.systemInfo = SystemInfo{
		CPUCores:  runtime.NumCPU(),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		l.systemInfo.CPUModel = cpuInfo[0].ModelName
		l.systemInfo.CPUVendor = cpuInfo[0].VendorID
		l.systemInfo.CPUFlags = cpuInfo[0].Flags
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
		args = append([]any{slog.Any("callstack", Callstack(nil).Strings())}, args...)
		l.Logger.Debug(msg, args...)
	}
}

// Debugf is a convenience wrapper that logs just a message and allows
// printf-style formatting of the provided args.
func (l *Logger) Debugf(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelDebug) {
		l.Logger.Debug(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	}
}

func (l *Logger) Info(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelInfo) {
		args = append([]any{slog.Any("callstack", Callstack(nil).Strings())}, args...)
		l.Logger.Info(msg, args...)
	}
}

func (l *Logger) Infof(msg string, args ...any) {
	if l != nil && l.Logger.Enabled(nil, slog.LevelInfo) {
		l.Logger.Info(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	}
}

func (l *Logger) Warn(msg string, args ...any) {
	args = append([]any{slog.Any("callstack", Callstack(nil).Strings())}, args...)
	if l == nil {
		slog.Warn(msg, args...)
	} else {
		l.Logger.Warn(msg, args...)
	}
}

func (l *Logger) Warnf(msg string, args ...any) {
	if l == nil {
		slog.Warn(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	} else {
		l.Logger.Warn(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	}
}

func (l *Logger) Error(msg string, args ...any) {
	args = append([]any{slog.Any("callstack", Callstack(nil).Strings())}, args...)
	if l == nil {
		slog.Error(msg, args...)
	} else {
		l.Logger.Error(msg, args...)
	}
}

func (l *Logger) Errorf(msg string, args ...any) {
	if l == nil {
		slog.Error(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	} else {
		l.Logger.Error(fmt.Sprintf(msg, args...), slog.Any("callstack", Callstack(nil).Strings()))
	}
}

func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger:         l.Logger.With(args...),
		LogFile:        l.LogFile,
		LogDir:         l.LogDir,
		Start:          l.Start,
		crashRPCClient: l.crashRPCClient,
		systemInfo:     l.systemInfo,
	}
}

// SetCrashReportClient sets the RPC client to use for sending crash reports
// to the server. This should be called after connecting to the server.
func (l *Logger) SetCrashReportClient(client *rpc.Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.crashRPCClient = client
	if client != nil {
		l.crashClientOnce.Do(func() { close(l.crashClientReady) })
	}
}

// SetGPUInfo sets the GPU information for crash reports. This should be
// called after OpenGL is initialized.
func (l *Logger) SetGPUInfo(vendor, renderer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.systemInfo.GPUVendor = vendor
	l.systemInfo.GPURenderer = renderer
}

func (l *Logger) CatchAndReportCrash() any {
	// Skip recovery when race detector is active - let panics propagate
	// so race conditions are clearly visible with full stack traces.
	if RaceEnabled {
		return nil
	}

	// Janky way to check if we're running under the debugger.
	if dlv, ok := os.LookupEnv("_"); ok && strings.HasSuffix(dlv, "/dlv") {
		return nil
	}

	err := recover()
	if err != nil {
		l.ReportCrash(err)
	}

	return err
}

// ReportCrashRPC is the RPC method name for crash reports.
const ReportCrashRPC = "SimManager.ReportCrash"

func (l *Logger) ReportCrash(err any) {
	l.Errorf("Crashed: %v", err)

	now := time.Now()

	// Get the system info and RPC client with a lock
	l.mu.Lock()
	sysInfo := l.systemInfo
	rpcClient := l.crashRPCClient
	l.mu.Unlock()

	// If we don't have an RPC client yet, wait a few seconds for the
	// remote server connection to be established.
	if rpcClient == nil {
		select {
		case <-l.crashClientReady:
			l.mu.Lock()
			rpcClient = l.crashRPCClient
			l.mu.Unlock()
		case <-time.After(4 * time.Second):
			l.Warn("No crash report RPC client available")
		}
	}

	// Format the report information
	report := fmt.Sprintf("Crashed: %v\n", err)
	report += fmt.Sprintf("Time: %s\n\n", now.Format(time.RFC3339))

	report += "== System Info ==\n"
	report += fmt.Sprintf("CPU: %s (%d cores)\n", sysInfo.CPUModel, sysInfo.CPUCores)
	report += fmt.Sprintf("CPU Vendor: %s\n", sysInfo.CPUVendor)
	// Include just a few key CPU flags that are relevant for debugging
	relevantFlags := filterRelevantCPUFlags(sysInfo.CPUFlags)
	if len(relevantFlags) > 0 {
		report += fmt.Sprintf("CPU Flags: %s\n", strings.Join(relevantFlags, ", "))
	}
	if sysInfo.GPURenderer != "" {
		report += fmt.Sprintf("GPU: %s (%s)\n", sysInfo.GPURenderer, sysInfo.GPUVendor)
	}
	report += fmt.Sprintf("Go: %s\n", sysInfo.GoVersion)
	report += fmt.Sprintf("OS/Arch: %s/%s\n\n", sysInfo.OS, sysInfo.Arch)

	report += "== Build Info ==\n"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range bi.Settings {
			report += setting.Key + ": " + setting.Value + "\n"
		}
	}
	report += "\n== Stack Trace ==\n"
	report += string(debug.Stack())

	// Print it to stdout
	fmt.Println(report)

	// Try to save it to disk locally as a backup
	fn := filepath.Join(l.LogDir, "crash-"+now.Format(time.RFC3339)+".txt")
	_ = os.WriteFile(fn, []byte(report), 0o600)

	// Send via RPC if we have a client
	if rpcClient != nil {
		crashReport := &CrashReport{
			Report:    report,
			System:    sysInfo,
			Timestamp: now,
		}
		// Attempt to send, but don't block for long since we're crashing
		done := make(chan *rpc.Call, 1)
		rpcClient.Go(ReportCrashRPC, crashReport, &struct{}{}, done)
		select {
		case call := <-done:
			if call.Error != nil {
				l.Errorf("Error sending crash report via RPC: %v", call.Error)
			} else {
				l.Info("Crash report sent via RPC")
			}
		case <-time.After(4 * time.Second):
			l.Warn("Timeout sending crash report via RPC")
		}
	}
}

// filterRelevantCPUFlags returns a subset of CPU flags that are relevant for debugging.
func filterRelevantCPUFlags(flags []string) []string {
	relevant := []string{"avx", "avx2", "avx512f", "sse4_1", "sse4_2", "sse4a", "ssse3", "fma"}
	var result []string
	for _, flag := range flags {
		for _, r := range relevant {
			if flag == r {
				result = append(result, flag)
				break
			}
		}
	}
	return result
}

///////////////////////////////////////////////////////////////////////////

// handler is an implementation of slog.Handler that sends log entries both
// to a JSON handler (that will log to disk) and a text handler that prints
// warnings and errors to stderr.
type handler struct {
	json slog.Handler
	txt  slog.Handler
}

func newHandler(w io.Writer, opts *slog.HandlerOptions) *handler {
	return &handler{
		json: slog.NewJSONHandler(w, opts),
		txt:  slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}),
	}
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.json.Enabled(ctx, level) || h.txt.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	if h.txt.Enabled(ctx, rec.Level) {
		_ = h.txt.Handle(ctx, rec)
	}
	if h.json.Enabled(ctx, rec.Level) {
		return h.json.Handle(ctx, rec)
	}
	return nil
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Handlers own the attrs passed to them, so we make sure each gets
	// its own copy.
	return &handler{
		json: h.json.WithAttrs(slices.Clone(attrs)),
		txt:  h.txt.WithAttrs(slices.Clone(attrs)),
	}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{
		json: h.json.WithGroup(name),
		txt:  h.txt.WithGroup(name),
	}
}

///////////////////////////////////////////////////////////////////////////

// AnyPointerSlice is similar to slog.Any but takes a slice of pointers;
// unlike passing a slice of pointers to slog.Any, it logs the values
// pointed-to by the pointers rather than the pointer values themselves.
func AnyPointerSlice[T any](name string, ptrs []*T) slog.Attr {
	values := make([]any, len(ptrs))
	for i, ptr := range ptrs {
		if ptr == nil {
			values[i] = nil
			continue
		}

		// Check if this implements LogValuer
		if lv, ok := any(ptr).(slog.LogValuer); ok {
			v := lv.LogValue()
			// If it's a group, convert to a map for proper JSON serialization
			if v.Kind() == slog.KindGroup {
				m := make(map[string]any)
				for _, attr := range v.Group() {
					m[attr.Key] = attr.Value.Any()
				}
				values[i] = m
			} else {
				values[i] = v.Any()
			}
		} else {
			values[i] = *ptr
		}
	}
	return slog.Any(name, values)
}
