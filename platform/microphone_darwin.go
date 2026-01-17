// platform/microphone_darwin.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build darwin

package platform

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework AVFoundation

#import <AVFoundation/AVFoundation.h>

// Returns: 0=NotDetermined, 1=Restricted, 2=Denied, 3=Authorized
// (matches AVAuthorizationStatus enum values)
int getMicrophoneAuthorizationStatus() {
    AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    return (int)status;
}

// Request microphone access asynchronously. Shows the permission dialog
// but does not wait for the result. Check status again later.
void requestMicrophoneAccessAsync() {
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
        // Result is ignored - caller should check status again later
    }];
}
*/
import "C"

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

// GetMicrophoneAuthorizationStatus returns the current microphone authorization status
func GetMicrophoneAuthorizationStatus() MicrophoneAuthStatus {
	return MicrophoneAuthStatus(C.getMicrophoneAuthorizationStatus())
}

// RequestMicrophoneAccess triggers the microphone permission dialog.
// This is asynchronous - it returns immediately and the dialog is shown.
// Check GetMicrophoneAuthorizationStatus() again later to see the result.
func RequestMicrophoneAccess() {
	C.requestMicrophoneAccessAsync()
}
