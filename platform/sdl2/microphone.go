// platform/sdl2/microphone.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sdl2

// micAuthStatus represents the authorization status for microphone access.
type micAuthStatus int

const (
	// micAuthNotDetermined - User has not yet made a choice
	micAuthNotDetermined micAuthStatus = 0
	// micAuthRestricted - Access restricted by system policy
	micAuthRestricted micAuthStatus = 1
	// micAuthDenied - User explicitly denied access
	micAuthDenied micAuthStatus = 2
	// micAuthAuthorized - User granted access
	micAuthAuthorized micAuthStatus = 3
)

func (s micAuthStatus) String() string {
	switch s {
	case micAuthNotDetermined:
		return "NotDetermined"
	case micAuthRestricted:
		return "Restricted"
	case micAuthDenied:
		return "Denied"
	case micAuthAuthorized:
		return "Authorized"
	default:
		return "Unknown"
	}
}
