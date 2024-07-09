// fullscreen_linux.go

package platform

import (
	"github.com/go-gl/glfw/v3.3/glfw"
)

func (g *glfwPlatform) IsFullScreen() bool {
	return g.window.GetMonitor() != nil
}

func (g *glfwPlatform) EnableFullScreen(fullscreen bool) {
	monitors := glfw.GetMonitors()
	if g.config.FullScreenMonitor >= len(monitors) {
		// Shouldn't happen, but just to be sure
		g.config.FullScreenMonitor = 0
	}

	monitor := monitors[g.config.FullScreenMonitor]
	vm := monitor.GetVideoMode()
	if fullscreen {
		g.window.SetMonitor(monitor, 0, 0, vm.Width, vm.Height, vm.RefreshRate)
	} else {
		windowSize := [2]int{g.config.InitialWindowSize[0], g.config.InitialWindowSize[1]}

		if windowSize[0] == 0 || windowSize[1] == 0 {
			windowSize[0] = vm.Width - 150
			windowSize[1] = vm.Height - 150

		}

		g.window.SetMonitor(nil, g.config.InitialWindowPosition[0], g.config.InitialWindowPosition[1],
			windowSize[0], windowSize[1], glfw.DontCare)
	}
}
