// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !windows

package log

// RedirectStderrToCrashFile is a no-op on non-Windows platforms: vice
// is built as a console program there, so stderr already has a
// reasonable destination (terminal or systemd journal).
func RedirectStderrToCrashFile(logDir string) string { return "" }

// RemoveCurrentCrashStderrFile is a no-op on non-Windows platforms.
func RemoveCurrentCrashStderrFile() {}
