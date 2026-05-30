// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// vice.exe is linked with -H=windowsgui (see build.bat), so when it is
// launched normally there is no attached console and stderr writes go
// nowhere. The Go runtime's fatal stack dump — including for
// unrecoverable conditions like "signal arrived during external code
// execution" — is written to fd 2 via WriteFile(GetStdHandle(
// STD_ERROR_HANDLE), ...). If we install a file as STD_ERROR_HANDLE
// before any crash occurs, that dump lands in the file and we can
// upload it on the next run.

var currentCrashStderr *os.File

// RedirectStderrToCrashFile creates a crash-stderr-<timestamp>.txt file
// in logDir and installs it as both STD_ERROR_HANDLE and os.Stderr. The
// returned path is the file it created (empty on failure or when
// skipped because stderr is already a real console).
//
// The file is left in place on process exit; the next vice startup
// scans for these files and uploads any that aren't deleted via
// RemoveCurrentCrashStderrFile. That way a Go runtime fatal — which
// does not run deferred functions — leaves its dump behind for the
// next run to ship to the crash server.
func RedirectStderrToCrashFile(logDir string) string {
	if logDir == "" {
		return ""
	}
	// If stderr is already attached to a real console (e.g. vice was
	// launched from cmd.exe and fixconsole attached us to the parent),
	// leave it alone so the user sees output as they expect.
	if stderr, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil && stderr != 0 {
		var mode uint32
		if windows.GetConsoleMode(stderr, &mode) == nil {
			return ""
		}
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return ""
	}

	fn := filepath.Join(logDir, "crash-stderr-"+time.Now().Format("20060102T150405")+".txt")
	f, err := os.OpenFile(fn, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return ""
	}

	// Write a small header so an empty file (no crash) is still
	// recognizable, and so the report has timestamp context.
	hdr := fmt.Sprintf("=== vice crash-stderr capture, start %s ===\n", time.Now().Format(time.RFC3339))
	_, _ = f.WriteString(hdr)

	// Redirect the Win32 STD_ERROR_HANDLE so the Go runtime's fatal
	// writes go to the file. The runtime calls GetStdHandle on every
	// write to fd 2, so this takes effect immediately.
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		_ = f.Close()
		_ = os.Remove(fn)
		return ""
	}

	// Also point os.Stderr at the file so Go-side writes (e.g. the
	// text slog handler) land in the same place.
	os.Stderr = f

	currentCrashStderrMu.Lock()
	currentCrashStderr = f
	currentCrashStderrFn = fn
	currentCrashStderrMu.Unlock()

	return fn
}

// RemoveCurrentCrashStderrFile closes and deletes the crash-stderr
// file created by this run. Callers should defer this in main() so
// that a clean exit leaves no file behind; only runtime fatals (which
// skip defers) leave the file around to be uploaded on the next start.
func RemoveCurrentCrashStderrFile() {
	currentCrashStderrMu.Lock()
	f := currentCrashStderr
	fn := currentCrashStderrFn
	currentCrashStderr = nil
	currentCrashStderrFn = ""
	currentCrashStderrMu.Unlock()

	if f == nil {
		return
	}
	_ = f.Close()
	if fn != "" {
		_ = os.Remove(fn)
	}
}
