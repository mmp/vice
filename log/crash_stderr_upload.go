// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"fmt"
	"net/rpc"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadAndDeleteCrashStderrFiles spawns a background goroutine that
// scans LogDir for any crash-stderr-*.txt files left over from prior
// runs, uploads each via the existing crash report RPC, and deletes
// the file on success. The returned channel is closed once the work
// is finished (whether the upload succeeded, failed, or timed out),
// so callers can gate further risky initialization on the upload
// completing — important for the case where a recurring init-time
// crash would otherwise kill the next run before the prior report
// reaches the server.
//
// The current run's file (if any) is skipped so we don't ship a still-
// being-written log. Empty files (created but never written to because
// the run exited cleanly before any crash) are deleted without
// uploading.
func (l *Logger) UploadAndDeleteCrashStderrFiles() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.uploadAndDeleteCrashStderrFiles()
	}()
	return done
}

func (l *Logger) uploadAndDeleteCrashStderrFiles() {
	if l == nil || l.LogDir == "" {
		return
	}

	matches, err := filepath.Glob(filepath.Join(l.LogDir, "crash-stderr-*.txt"))
	if err != nil || len(matches) == 0 {
		return
	}

	currentCrashStderrMu.Lock()
	currentFn := currentCrashStderrFn
	currentCrashStderrMu.Unlock()

	// Filter to files we want to process now: skip the current run's file
	// and delete empty leftovers (clean previous exit before file was
	// fully closed/removed, or vice was never crashed-into).
	var toUpload []string
	for _, fn := range matches {
		if fn == currentFn {
			continue
		}
		st, err := os.Stat(fn)
		if err != nil {
			continue
		}
		// Empty or header-only files (<= the small header we write at
		// open) are not interesting; just delete them.
		if st.Size() < 256 {
			_ = os.Remove(fn)
			continue
		}
		// Capture files accumulate the text slog handler's warn+
		// output and any fmt.Fprintf(os.Stderr, ...) writes for the
		// life of the process — useful inline context when a crash
		// dump is also present, but on its own just session noise.
		// Only upload files that contain a marker the Go runtime (or
		// the OS, for Windows SEH) writes when killing the process.
		if !hasRuntimeFatalMarker(fn) {
			_ = os.Remove(fn)
			continue
		}
		toUpload = append(toUpload, fn)
	}
	if len(toUpload) == 0 {
		return
	}

	// Wait for the crash RPC client to become available. Bounded tight
	// because callers gate risky init on this returning — if the
	// server isn't reachable we'd rather get on with startup than
	// stall it indefinitely.
	select {
	case <-l.crashClientReady:
	case <-time.After(10 * time.Second):
		l.Warnf("No crash RPC client available; %d crash-stderr file(s) will be retried next run", len(toUpload))
		return
	}

	l.mu.Lock()
	rpcClient := l.crashRPCClient
	sysInfo := l.systemInfo
	l.mu.Unlock()

	if rpcClient == nil {
		return
	}

	for _, fn := range toUpload {
		l.uploadCrashStderrFile(rpcClient, sysInfo, fn)
	}
}

// crashTimeFromFilename parses the timestamp from a
// crash-stderr-YYYYMMDDTHHMMSS.txt filename, falling back to the
// file's mtime and finally to time.Now().
func crashTimeFromFilename(fn string) time.Time {
	base := filepath.Base(fn)
	const prefix = "crash-stderr-"
	const suffix = ".txt"
	if strings.HasPrefix(base, prefix) && strings.HasSuffix(base, suffix) {
		stamp := strings.TrimSuffix(strings.TrimPrefix(base, prefix), suffix)
		if t, err := time.ParseInLocation("20060102T150405", stamp, time.Local); err == nil {
			return t
		}
	}
	if st, err := os.Stat(fn); err == nil {
		return st.ModTime()
	}
	return time.Now()
}

func (l *Logger) uploadCrashStderrFile(rpcClient *rpc.Client, sysInfo SystemInfo, fn string) {
	contents, err := os.ReadFile(fn)
	if err != nil {
		l.Warnf("Failed to read crash-stderr file %s: %v", fn, err)
		return
	}

	crashTime := crashTimeFromFilename(fn)

	var b strings.Builder
	fmt.Fprintf(&b, "Crashed (stderr capture from prior run): %s\n", filepath.Base(fn))
	fmt.Fprintf(&b, "Captured: %s\n", crashTime.Format(time.RFC3339))
	fmt.Fprintf(&b, "Reported: %s\n\n", time.Now().Format(time.RFC3339))

	b.WriteString("== System Info (current) ==\n")
	fmt.Fprintf(&b, "CPU: %s (%d cores)\n", sysInfo.CPUModel, sysInfo.CPUCores)
	fmt.Fprintf(&b, "CPU Vendor: %s\n", sysInfo.CPUVendor)
	if flags := filterRelevantCPUFlags(sysInfo.CPUFlags); len(flags) > 0 {
		fmt.Fprintf(&b, "CPU Flags: %s\n", strings.Join(flags, ", "))
	}
	if sysInfo.GPURenderer != "" {
		fmt.Fprintf(&b, "GPU: %s (%s)\n", sysInfo.GPURenderer, sysInfo.GPUVendor)
	}
	fmt.Fprintf(&b, "Go: %s\n", sysInfo.GoVersion)
	fmt.Fprintf(&b, "OS/Arch: %s/%s\n\n", sysInfo.OS, sysInfo.Arch)

	b.WriteString("== Captured stderr ==\n")
	b.Write(contents)

	report := &CrashReport{
		Report:    b.String(),
		System:    sysInfo,
		Timestamp: crashTime,
	}

	done := make(chan *rpc.Call, 1)
	rpcClient.Go(ReportCrashRPC, report, &struct{}{}, done)
	select {
	case call := <-done:
		if call.Error != nil {
			l.Warnf("Failed to upload crash-stderr %s: %v", filepath.Base(fn), call.Error)
			return
		}
	case <-time.After(10 * time.Second):
		l.Warnf("Timeout uploading crash-stderr %s; will retry next run", filepath.Base(fn))
		return
	}

	if err := os.Remove(fn); err != nil {
		l.Warnf("Failed to delete crash-stderr %s after upload: %v", fn, err)
	} else {
		l.Infof("Uploaded and removed prior-run crash-stderr: %s", filepath.Base(fn))
	}
}

// runtimeFatalMarkers are substrings the Go runtime (or Windows SEH for
// cgo crashes) writes to fd 2 / STD_ERROR_HANDLE when killing the
// process. Anything else in the capture — slog text output, scenario
// validation dumps, our own fmt.Fprintf(os.Stderr, ...) — is session
// noise and shouldn't be reported as a crash.
var runtimeFatalMarkers = []string{
	"panic:",                // runtime/panic.go gopanic, etc.
	"fatal error:",          // runtime fatalthrow
	"runtime error:",        // runtime error wrappers
	"signal arrived during", // cgo signal during external code execution
	"Exception 0x",          // Windows SEH dumps
}

// hasRuntimeFatalMarker reports whether the given capture file contains
// any sign of a real runtime fatal. Reads at most 256 KiB — capture
// files are small and the markers always appear at the start of the
// runtime's dump.
func hasRuntimeFatalMarker(fn string) bool {
	f, err := os.Open(fn)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 256*1024)
	n, _ := f.Read(buf)
	body := string(buf[:n])
	for _, m := range runtimeFatalMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}
