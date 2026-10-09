// log/crash_stderr_other.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !windows

package log

// redirectStderrToCrashFile is a no-op on non-Windows platforms: vice
// and backshop are built as console programs there, so stderr already
// has a reasonable destination (terminal or systemd journal).
func redirectStderrToCrashFile(logDir string) string { return "" }

// RemoveCurrentCrashStderrFile is a no-op on non-Windows platforms.
func RemoveCurrentCrashStderrFile() {}
