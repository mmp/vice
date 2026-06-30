// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempCapture(t *testing.T, content string) string {
	t.Helper()
	fn := filepath.Join(t.TempDir(), "crash-stderr-20260101T000000.txt")
	if err := os.WriteFile(fn, []byte(content), 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	return fn
}

func TestHasRuntimeFatalMarker_RealCrash(t *testing.T) {
	// Mirrors a real Go runtime fatal: panic line followed by goroutine
	// stack. Comes from runtime/panic.go.
	const body = `=== vice crash-stderr capture, start 2026-06-15T21:40:27-05:00 ===
some slog warn output
panic: runtime error: invalid memory address or nil pointer dereference
[signal 0xc0000005 code=0x0 addr=0x0 pc=0x7ff6026578e8]

goroutine 5495 [running]:
...
`
	fn := writeTempCapture(t, body)
	if !hasRuntimeFatalMarker(fn) {
		t.Errorf("expected panic to be detected")
	}
}

func TestHasRuntimeFatalMarker_WindowsCgoException(t *testing.T) {
	const body = `=== capture start ===
Exception 0xc0000094 0x0 0x0 0x7ff81fecd9d7
signal arrived during external code execution
`
	fn := writeTempCapture(t, body)
	if !hasRuntimeFatalMarker(fn) {
		t.Errorf("expected Windows SEH dump to be detected")
	}
}

func TestHasRuntimeFatalMarker_ScenarioErrorsOnly(t *testing.T) {
	// The 97-report bucket: scenario-developer JSON errors dumped via
	// slog plus the (now-fixed) wx-path warning. No real crash.
	const body = `=== vice crash-stderr capture, start 2026-06-14T09:42:38-04:00 ===
time=2026-06-14T09:42:40.028-04:00 level=ERROR msg="TRACON RSW / Scenario: video map not found"
time=2026-06-14T09:42:40.028-04:00 level=ERROR msg="TRACON TPA / Scenario: inbound flow not found"
`
	fn := writeTempCapture(t, body)
	if hasRuntimeFatalMarker(fn) {
		t.Errorf("scenario-error-only capture should not look like a crash")
	}
}

func TestHasRuntimeFatalMarker_StderrNoise(t *testing.T) {
	// User closed via Task Manager after a session with warnings — no
	// actual crash; should not be uploaded.
	const body = `=== capture start ===
time=2026-06-15T10:43:31 level=WARN msg="Subscriber has not called Get() recently"
time=2026-06-15T10:57:29 level=WARN msg="ERAM cursor not found"
`
	fn := writeTempCapture(t, body)
	if hasRuntimeFatalMarker(fn) {
		t.Errorf("warn-only capture should not look like a crash")
	}
}

func TestHasRuntimeFatalMarker_MissingFile(t *testing.T) {
	if hasRuntimeFatalMarker(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Errorf("missing file should not look like a crash")
	}
}
