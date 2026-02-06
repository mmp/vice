package stt

import (
	"fmt"
	"sync"
)

// LogBuffer captures STT processing logs for bug reports.
type LogBuffer struct {
	lines []string
	mu    sync.Mutex
}

var (
	currentLogBuffer *LogBuffer
	logBufferMu      sync.Mutex
)

// StartCapture begins capturing log lines to a buffer.
// Returns the buffer that will collect the logs.
func StartCapture() *LogBuffer {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	buf := &LogBuffer{lines: make([]string, 0, 50)}
	currentLogBuffer = buf
	return buf
}

// StopCapture stops capturing and returns the captured lines.
func StopCapture() []string {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	if currentLogBuffer == nil {
		return nil
	}
	buf := currentLogBuffer
	currentLogBuffer = nil
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.lines
}

// logLocalStt logs local STT processing details.
// Also captures to buffer if capture is active.
func logLocalStt(format string, args ...any) {
	return

	line := fmt.Sprintf("[local-stt] "+format, args...)
	fmt.Println(line)

	logBufferMu.Lock()
	buf := currentLogBuffer
	logBufferMu.Unlock()

	if buf != nil {
		buf.mu.Lock()
		buf.lines = append(buf.lines, line)
		buf.mu.Unlock()
	}
}
