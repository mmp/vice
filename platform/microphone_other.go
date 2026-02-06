// platform/microphone_other.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !darwin

package platform

// MicrophoneAuthStatus represents the authorization status for microphone access
type MicrophoneAuthStatus int

const (
	// MicAuthNotDetermined - User has not yet made a choice
	MicAuthNotDetermined MicrophoneAuthStatus = 0
	// MicAuthRestricted - Access restricted by system policy
	MicAuthRestricted MicrophoneAuthStatus = 1
	// MicAuthDenied - User explicitly denied access
	MicAuthDenied MicrophoneAuthStatus = 2
	// MicAuthAuthorized - User granted access
	MicAuthAuthorized MicrophoneAuthStatus = 3
)

func (s MicrophoneAuthStatus) String() string {
	switch s {
	case MicAuthNotDetermined:
		return "NotDetermined"
	case MicAuthRestricted:
		return "Restricted"
	case MicAuthDenied:
		return "Denied"
	case MicAuthAuthorized:
		return "Authorized"
	default:
		return "Unknown"
	}
}

// GetMicrophoneAuthorizationStatus returns the current microphone authorization status.
// On non-macOS platforms, this always returns MicAuthAuthorized since there's no
// system-level permission required.
func GetMicrophoneAuthorizationStatus() MicrophoneAuthStatus {
	return MicAuthAuthorized
}

// RequestMicrophoneAccess triggers the microphone permission dialog.
// On non-macOS platforms, this is a no-op since no permission is required.
func RequestMicrophoneAccess() {
}
