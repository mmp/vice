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

// buildInfoReport returns the "== Build Info ==" block naming the build that
// is running. Crash reports for a fatal caught via stderr redirection are
// uploaded by a *later* run, so the stderr file records this itself rather
// than leaving the reader to infer the build from paths in the traceback.
func buildInfoReport() string {
	var b strings.Builder
	b.WriteString("== Build Info ==\n")
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range bi.Settings {
			b.WriteString(setting.Key + ": " + setting.Value + "\n")
		}
	}
	return b.String()
}
