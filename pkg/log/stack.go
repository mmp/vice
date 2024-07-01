package log

import (
	"path"
	"runtime"
	"strings"
)

type StackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
}

func Callstack() []StackFrame {
	var callers [16]uintptr
	n := runtime.Callers(3, callers[:]) // skip up to function that is doing logging
	frames := runtime.CallersFrames(callers[:n])

	var fr []StackFrame
	for i := 0; ; i++ {
		frame, more := frames.Next()
		fr = append(fr, StackFrame{
			File:     path.Base(frame.File),
			Line:     frame.Line,
			Function: strings.TrimPrefix(frame.Function, "main."),
		})

		// Don't keep going up into go runtime stack frames.
		if !more || frame.Function == "main.main" {
			break
		}
	}
	return fr
}
