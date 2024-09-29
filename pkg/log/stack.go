// pkg/log/stack.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"path/filepath"
	"runtime"
	"strings"
)

type StackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

func Callstack(fr []StackFrame) []StackFrame {
	var callers [16]uintptr
	n := runtime.Callers(3, callers[:]) // skip up to function that is doing logging
	frames := runtime.CallersFrames(callers[:n])

	fr = fr[:0]
	if cap(fr) < n {
		fr = make([]StackFrame, n)
	}

	for i := 0; i < n; i++ {
		frame, more := frames.Next()
		fr[i] = StackFrame{
			File:     filepath.Base(frame.File),
			Line:     frame.Line,
			Function: strings.TrimPrefix(frame.Function, "main."),
		}

		// Don't keep going up into go runtime stack frames.
		if !more || frame.Function == "main.main" {
			fr = fr[:i+1]
			break
		}
	}
	return fr
}
