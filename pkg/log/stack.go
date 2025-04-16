// pkg/log/stack.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"path/filepath"
	"runtime"
	"strconv"
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
		fn := strings.TrimPrefix(frame.Function, "github.com/mmp/vice/pkg")
		fn = strings.TrimPrefix(fn, "main.")

		fr[i] = StackFrame{
			File:     filepath.Base(frame.File),
			Line:     frame.Line,
			Function: fn,
		}

		// Don't keep going up into go runtime stack frames.
		if !more || frame.Function == "main.main" {
			fr = fr[:i+1]
			break
		}
	}
	return fr
}

func (f StackFrame) String() string {
	return f.File + ":" + strconv.Itoa(f.Line) + ":" + f.Function
}
