// fullscreen_darwin.go

package platform

/*
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework Cocoa

#include <Cocoa/Cocoa.h>

// Function to set macOS specific properties
void makeFullscreenNative(void *window) {
    NSWindow *nswindow = ((NSWindow*)window);
    [nswindow setCollectionBehavior: NSWindowCollectionBehaviorFullScreenPrimary];
    [nswindow toggleFullScreen:nil];
}

// Function to check if the window is in native fullscreen mode
int isNativeFullscreen(void *window) {
    NSWindow *nswindow = ((NSWindow*)window);
    return (nswindow.styleMask & NSWindowStyleMaskFullScreen) == NSWindowStyleMaskFullScreen ? 1 : 0;
}

*/
import "C"

func (g *glfwPlatform) EnableFullScreen(fullscreen bool) {
	window := g.window.GetCocoaWindow()
	C.makeFullscreenNative(window)
}

func (g *glfwPlatform) IsFullScreen() bool {
	if g.window.GetMonitor() != nil {
		return true
	} else if C.isNativeFullscreen(g.window.GetCocoaWindow()) == 1 {
		return true
	} else {
		return false
	}
}
