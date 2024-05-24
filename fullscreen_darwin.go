// fullscreen_darwin.go

package main


/*
#ifdef __APPLE__
#cgo darwin CFLAGS: -x objective-c
#cgo darwin LDFLAGS: -framework Cocoa

#include <Cocoa/Cocoa.h>
#include <GLFW/glfw3.h>

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
#else

#include <GLFW/glfw3.h>

// No-op functions for non-macOS platforms
void makeFullscreenNative(void *window) {
    // No operation on non-macOS platforms
}

int isNativeFullscreen(void *window) {
    // Always return 0 on non-macOS platforms
    return 0;
}
#endif
*/
import "C"

import (
	"runtime"

	"github.com/go-gl/glfw/v3.3/glfw"
)

func (g *GLFWPlatform) EnableFullScreen(fullscreen bool) {
	if runtime.GOOS == "darwin" {
		window := g.window.GetCocoaWindow()
		C.makeFullscreenNative(window)
		return
	}

	monitors := glfw.GetMonitors()
	if globalConfig.FullScreenMonitor >= len(monitors) {
		// Shouldn't happen, but just to be sure
		globalConfig.FullScreenMonitor = 0
	}

	monitor := monitors[globalConfig.FullScreenMonitor]
	vm := monitor.GetVideoMode()
	if fullscreen {
		g.window.SetMonitor(monitor, 0, 0, vm.Width, vm.Height, vm.RefreshRate)
	} else {
		windowSize := [2]int{globalConfig.InitialWindowSize[0], globalConfig.InitialWindowSize[1]}

		if windowSize[0] == 0 || windowSize[1] == 0 || runtime.GOOS == "darwin" {
			if runtime.GOOS == "windows" {
				windowSize[0] = vm.Width - 200
				windowSize[1] = vm.Height - 300
			} else {
				windowSize[0] = vm.Width - 150
				windowSize[1] = vm.Height - 150
			}
		}

		g.window.SetMonitor(nil, globalConfig.InitialWindowPosition[0], globalConfig.InitialWindowPosition[1], windowSize[0], windowSize[1], glfw.DontCare)
	}
}

func (g *GLFWPlatform) IsFullScreen() bool {
	if g.window.GetMonitor() != nil {
		return true
	} else if runtime.GOOS == "darwin" && C.isNativeFullscreen(g.window.GetCocoaWindow()) == 1 {
		return true
	} else {
		return false
	}
}