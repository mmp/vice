// platform/sdl2/microphone_darwin.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build darwin

package sdl2

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

// microphoneAuthorizationStatus returns the current microphone authorization status.
func microphoneAuthorizationStatus() micAuthStatus {
	return micAuthStatus(C.getMicrophoneAuthorizationStatus())
}

// requestMicrophoneAccess triggers the microphone permission dialog.
// This is asynchronous - it returns immediately and the dialog is shown.
// Check microphoneAuthorizationStatus() again later to see the result.
func requestMicrophoneAccess() {
	C.requestMicrophoneAccessAsync()
}
