// platform/sdl2/microphone_other.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !darwin

package sdl2

// microphoneAuthorizationStatus returns the current microphone authorization
// status. On non-macOS platforms, this always returns micAuthAuthorized since
// there's no system-level permission required.
func microphoneAuthorizationStatus() micAuthStatus {
	return micAuthAuthorized
}

// requestMicrophoneAccess triggers the microphone permission dialog.
// On non-macOS platforms, this is a no-op since no permission is required.
func requestMicrophoneAccess() {
}
