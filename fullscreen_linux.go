// fullscreen_linux.go

package main

import (
	"runtime"

	"github.com/go-gl/glfw/v3.3/glfw"
)

func (g *GLFWPlatform) IsFullScreen() bool {
	return g.window.GetMonitor() != nil || g.IsMacOSNativeFullScreen()
}

func (g *GLFWPlatform) EnableFullScreen(fullscreen bool) {
	if g.IsMacOSNativeFullScreen() {
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
