// client/stt_windows.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"golang.org/x/sys/windows"
)

// isWindowsARM reports whether the process is running on Windows ARM hardware
// (via x86 emulation). We detect this using IsWow64Process2 which reports the
// native machine architecture regardless of emulation.
func isWindowsARM() bool {
	var processMachine, nativeMachine uint16
	err := windows.IsWow64Process2(windows.CurrentProcess(), &processMachine, &nativeMachine)
	if err != nil {
		return false
	}
	// IMAGE_FILE_MACHINE_ARM64 = 0xAA64
	return nativeMachine == 0xAA64
}
