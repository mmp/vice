// pkg/util/error.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"fmt"
	"os"
	"strings"

	"github.com/mmp/vice/pkg/log"
)

// ErrorLogger is a small utility class used to log errors when validating
// the parsed JSON scenarios. It tracks context about what is currently
// being validated and accumulates multiple errors, making it possible to
// log errors while still continuing validation.
type ErrorLogger struct {
	// Tracked via Push()/Pop() calls to remember what we're looking at if
	// an error is found.
	hierarchy []string
	// Actual error messages to report.
	errors []string
}

func (e *ErrorLogger) Push(s string) {
	e.hierarchy = append(e.hierarchy, s)
}

func (e *ErrorLogger) Pop() {
	e.hierarchy = e.hierarchy[:len(e.hierarchy)-1]
}

func (e *ErrorLogger) ErrorString(s string, args ...interface{}) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+fmt.Sprintf(s, args...))
}

func (e *ErrorLogger) Error(err error) {
	e.errors = append(e.errors, strings.Join(e.hierarchy, " / ")+": "+err.Error())
}

func (e *ErrorLogger) HaveErrors() bool {
	return len(e.errors) > 0
}

func (e *ErrorLogger) PrintErrors(lg *log.Logger) {
	// Two loops so they aren't interleaved with logging to stdout
	if lg != nil {
		for _, err := range e.errors {
			lg.Errorf("%+v", err)
		}
	}
	for _, err := range e.errors {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (e *ErrorLogger) String() string {
	return strings.Join(e.errors, "\n")
}

func (e *ErrorLogger) CheckDepth(d int) {
	if e == nil || e.CurrentDepth() == d {
		return
	}

	if r := recover(); r == nil {
		// Don't give spurious warnings when there's a panic.
		fmt.Printf("Initial ErrorLogger depth %d, final %d\n", d, e.CurrentDepth())
		for _, f := range log.Callstack(nil) {
			fmt.Printf("%15s:%d %s\n", f.File, f.Line, f.Function)
		}
		os.Exit(1)
	} else {
		panic(r)
	}
}

func (e *ErrorLogger) CurrentDepth() int {
	if e == nil {
		return 0
	}
	return len(e.hierarchy)
}
