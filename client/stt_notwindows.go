// client/stt_notwindows.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !windows

package client

func isWindowsARM() bool {
	return false
}
