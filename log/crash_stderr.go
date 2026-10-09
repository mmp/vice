// log/crash_stderr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Shared state for the platform-specific stderr redirect (currently
// only implemented on Windows; see crash_stderr_windows.go) and for
// the cross-platform upload-on-startup path (crash_stderr_upload.go).
// Declared here so non-Windows builds still compile the upload code.
var (
	currentCrashStderrMu sync.Mutex
	currentCrashStderrFn string
)

// NewWithStderrCapture returns a client logger for a GUI program. On
// Windows, where those are linked without a console, it first redirects
// stderr into a crash-stderr file in the log directory unless stderr
// already goes to a console. The Go runtime's dump for a fatal error, which
// skips deferred crash handlers, then lands in that file for the next run
// to upload. The redirect has to come first because the logger captures
// os.Stderr when it is created.
func NewWithStderrCapture(level, dir string) *Logger {
	dir = DefaultLogDir(false /* not server mode */, dir)
	fn := redirectStderrToCrashFile(dir)
	l := New(false /* not server mode */, level, dir)
	if fn != "" {
		l.Infof("Redirected stderr to %s", fn)
	}
	return l
}

// buildInfoReport returns the "== Build Info ==" block naming the program
// and build that is running. Crash reports for a fatal caught via stderr
// redirection are uploaded by a *later* run, possibly of a different program
// sharing the log directory, so the stderr file records this itself rather
// than leaving the reader to infer it from paths in the traceback.
func buildInfoReport() string {
	var b strings.Builder
	b.WriteString("== Build Info ==\n")
	if bi, ok := debug.ReadBuildInfo(); ok {
		b.WriteString("path: " + bi.Path + "\n")
		for _, setting := range bi.Settings {
			b.WriteString(setting.Key + ": " + setting.Value + "\n")
		}
	}
	return b.String()
}
