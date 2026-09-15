// cmd/backshop/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// backshop is a tool for facility engineering: developing vice scenarios,
// facility configurations, and video maps. It draws a scenario's geometry on
// a plain map, exposes the aviation database and everything vice derives
// from it, and reloads edited JSON without restarting.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
	"github.com/apenwarr/fixconsole"
)

// selfTestEnv makes backshop run its self test instead of opening a window:
// it loads the scenarios, starts a sim, builds the maps and reloads, then
// exits. Its value names the facility to open, or is empty for the one last
// used. It is an environment variable and not a command-line flag because
// backshop takes no arguments at all: it is for people working on scenarios,
// not people working in a shell, and everything it can be told is settable
// in the window. It exists for CI and for checking a machine with no
// display.
const selfTestEnv = "BACKSHOP_SELFTEST"

func init() {
	// OpenGL and friends require that all calls be made from the primary
	// application thread, so lock it down before anything else runs.
	runtime.LockOSThread()
}

func main() {
	if err := fixconsole.FixConsoleIfNeeded(); err != nil {
		fmt.Printf("FixConsole: %v\n", err)
	}

	lg := log.New(false /* not server mode */, "info", log.DefaultLogDir(false, ""))
	config := loadConfig(lg)

	run := run
	if facility, ok := os.LookupEnv(selfTestEnv); ok {
		if facility != "" {
			config.LastFacility, config.LastGroup, config.LastScenario = facility, "", ""
		}
		run = runSelfTest
	}
	if err := run(config, lg); err != nil {
		lg.Errorf("%v", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// imguiInit sets up the imgui context. It must run before platform.New,
// which expects the context to exist.
func imguiInit(config *Config) {
	imgui.CreateContext()
	io := imgui.CurrentIO()
	io.SetIniFilename("")
	io.SetConfigFlags(io.ConfigFlags() | imgui.ConfigFlagsViewportsEnable | imgui.ConfigFlagsDockingEnable)
	io.SetConfigMacOSXBehaviors(false)
	io.SetConfigWindowsMoveFromTitleBarOnly(true)

	style := imgui.CurrentStyle()
	style.SetFrameRounding(2)
	style.SetWindowRounding(4)
	style.SetPopupRounding(4)
	style.SetScrollbarSize(6)
	style.ScaleAllSizes(1.25)

	if config.ImGuiSettings != "" {
		imgui.LoadIniSettingsFromMemory(config.ImGuiSettings)
	}
}

func run(config *Config, lg *log.Logger) error {
	imguiInit(config)

	plat, err := platform.New(&config.Config, lg)
	if err != nil {
		return fmt.Errorf("unable to create application window: %w", err)
	}
	defer plat.Dispose()
	plat.SetWindowTitle("backshop")

	imgui.CurrentPlatformIO().SetClipboardHandler(plat.GetClipboard())

	render, err := renderer.NewOpenGL2Renderer(lg)
	if err != nil {
		return fmt.Errorf("unable to initialize OpenGL: %w", err)
	}
	renderer.FontsInit(render, plat)
	plat.InitViewportBackends()

	if runtime.GOOS == "windows" {
		imgui.CurrentStyle().ScaleAllSizes(plat.DPIScale())
	}

	uiFont := renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: config.UIFontSize})

	if err := initResources(gui.NewSyncUI(plat, uiFont)); err != nil {
		if errors.Is(err, util.ErrSyncCanceled) {
			// The user chose to quit rather than have the sync overwrite
			// resource files they had modified.
			return nil
		}
		return err
	}

	app, err := newApp(config, plat, render, lg)
	if err != nil {
		return err
	}

	for {
		app.update()

		plat.ProcessEvents()
		plat.NewFrame()
		imgui.NewFrame()

		uiFont.ImguiPush()
		app.draw()
		imgui.PopFont()

		imgui.Render()
		implogl3.RenderDrawData(imgui.CurrentDrawData())
		renderer.SyncFontAtlasTexID()

		io := imgui.CurrentIO()
		if io.ConfigFlags()&imgui.ConfigFlagsViewportsEnable != 0 {
			imgui.UpdatePlatformWindows()
			imgui.RenderPlatformWindowsDefault()
			plat.MakeContextCurrent()
		}

		plat.PostRender()

		if plat.ShouldStop() {
			break
		}
	}

	app.shutdown()
	config.save(plat, lg)
	return nil
}

// initResources brings the resource files up to date and loads the aviation
// database and weather. ui presents the sync; it is a text UI under
// -selftest, which runs without a window to put dialogs in.
func initResources(ui util.SyncUI) error {
	if err := util.SyncResources(ui); err != nil {
		return err
	}
	av.InitDB()
	wx.Init()
	return nil
}
