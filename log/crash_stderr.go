// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import "sync"

// Shared state for the platform-specific stderr redirect (currently
// only implemented on Windows; see crash_stderr_windows.go) and for
// the cross-platform upload-on-startup path (crash_stderr_upload.go).
// Declared here so non-Windows builds still compile the upload code.
var (
	currentCrashStderrMu sync.Mutex
	currentCrashStderrFn string
)
