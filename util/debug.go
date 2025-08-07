// pkg/util/debug.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"os"
	"strings"
)

// DebuggerIsRunning returns true if we are running under a debugger; this
// allows inhibiting various timeouts that may otherwise get in the way of
// debugging. Currently only detects dlv (TODO others as applicable).
func DebuggerIsRunning() bool {
	dlv, ok := os.LookupEnv("_")
	return ok && strings.HasSuffix(dlv, "/dlv")
}
