// main.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// This file contains the implementation of the main() function, which
// initializes the system and then runs the event loop until the system
// exits.

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"time"

	"github.com/apenwarr/fixconsole"
	"github.com/mmp/imgui-go/v4"
)

var (
	// There are a handful of widely-used global variables in vice, all
	// defined here.  While in principle it would be nice to have fewer (or
	// no!) globals, it's cleaner to have these easily accessible where in
	// the system without having to pass them through deep callchains.
	// Note that in some cases they are passed down from main (e.g.,
	// platform); this is plumbing in preparation for reducing the
	// number of these in the future.
	globalConfig   *GlobalConfig
	platform       Platform
	database       *StaticDatabase
	world          *World
	lg             *Logger
	scenarioGroups map[string]*ScenarioGroup

	//go:embed resources/version.txt
	buildVersion string

	// Command-line options are only used for developer features.
	cpuprofile       = flag.String("cpuprofile", "", "write CPU profile to file")
	memprofile       = flag.String("memprofile", "", "write memory profile to this file")
	devmode          = flag.Bool("devmode", false, "developer mode")
	scenarioFilename = flag.String("scenario", "", "filename of JSON file with a scenario definition")
	videoMapFilename = flag.String("videomap", "", "filename of JSON file with video map definitions")
)

func init() {
	// OpenGL and friends require that all calls be made from the primary
	// application thread, while by default, go allows the main thread to
	// run on different hardware threads over the course of
	// execution. Therefore, we must lock the main thread at startup time.
	runtime.LockOSThread()
}

func main() {
	flag.Parse()

	var stats Stats
	var renderer Renderer

	// Catch any panics so that we can put up a dialog box and hopefully
	// get a bug report.
	var context *imgui.Context
	if !*devmode {
		defer func() {
			if err := recover(); err != nil {
				lg.Errorf("Panic stack: %s", string(debug.Stack()))
				ShowFatalErrorDialog(renderer, platform,
					"Unfortunately an unexpected error has occurred and vice is unable to recover.\n"+
						"Apologies! Please do file a bug and include the vice.log file for this session\nso that "+
						"this bug can be fixed.\n\nError: %v", err)
			}
			lg.SaveLogs()

			// Clean up in backwards order from how things were created.
			renderer.Dispose()
			platform.Dispose()
			context.Destroy()
		}()
	}

	if err := fixconsole.FixConsoleIfNeeded(); err != nil {
		// Not sure this will actually appear, but what else are we going
		// to do...
		fmt.Printf("FixConsole: %v", err)
	}

	///////////////////////////////////////////////////////////////////////////
	// Global initialization and set up. Note that there are some subtle
	// inter-dependencies in the following; the order is carefully crafted.

	// Initialize the logging system first and foremost.
	lg = NewLogger(true, *devmode, 50000)

	if *cpuprofile != "" {
		if f, err := os.Create(*cpuprofile); err != nil {
			lg.Errorf("%s: unable to create CPU profile file: %v", *cpuprofile, err)
		} else {
			if err = pprof.StartCPUProfile(f); err != nil {
				lg.Errorf("unable to start CPU profile: %v", err)
			} else {
				defer pprof.StopCPUProfile()
			}
		}
	}

	context = imguiInit()

	var err error
	if err = audioInit(); err != nil {
		lg.Errorf("Unable to initialize audio: %v", err)
	}

	LoadOrMakeDefaultConfig()

	database = InitializeStaticDatabase()

	// After the database is loaded
	var e ErrorLogger
	scenarioGroups = LoadScenarioGroups(&e)
	if e.HaveErrors() {
		e.PrintErrors()
		os.Exit(1)
	}

	multisample := runtime.GOOS != "darwin"
	platform, err = NewGLFWPlatform(imgui.CurrentIO(), globalConfig.InitialWindowSize,
		globalConfig.InitialWindowPosition, multisample)
	if err != nil {
		panic(fmt.Sprintf("Unable to create application window: %v", err))
	}
	imgui.CurrentIO().SetClipboard(platform.GetClipboard())

	renderer, err = NewOpenGL2Renderer(imgui.CurrentIO())
	if err != nil {
		panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
	}

	fontsInit(renderer, platform)

	sim := globalConfig.Sim
	if sim != nil {
		sim.Activate()
	} else {
		sim = NewSim(MakeSimConfiguration())
	}

	world, err = sim.SignOn(globalConfig.Callsign)
	if err != nil {
		lg.Errorf("%s: %v", globalConfig.Callsign, err)
		world, err = sim.SignOn("") // default
		if err != nil {
			lg.Errorf("default callsign: %v", err)
		}
	}

	wmInit(world)

	uiInit(renderer, platform)

	globalConfig.Activate(world)

	///////////////////////////////////////////////////////////////////////////
	// Main event / rendering loop
	lg.Printf("Starting main loop")
	frameIndex := 0
	stats.startTime = time.Now()
	for {
		if world == nil {
			platform.SetWindowTitle("vice: [disconnected]")
		} else {
			platform.SetWindowTitle("vice: " + world.GetWindowTitle())
		}

		// Inform imgui about input events from the user.
		platform.ProcessEvents()

		stats.redraws++

		lastTime := time.Now()
		timeMarker := func(d *time.Duration) {
			now := time.Now()
			*d = now.Sub(lastTime)
			lastTime = now
		}

		// Let the world update its state based on messages from the
		// network; a synopsis of changes to aircraft is then passed along
		// to the window panes.
		if world != nil {
			world.GetUpdates()
		}

		platform.NewFrame()
		imgui.NewFrame()

		// Generate and render vice draw lists
		if world != nil {
			wmDrawPanes(platform, renderer, world, &stats)
		}
		timeMarker(&stats.drawPanes)

		// Draw the user interface
		drawUI(platform, renderer, world, &stats)
		timeMarker(&stats.drawImgui)

		// Wait for vsync
		platform.PostRender()

		// Periodically log current memory use, etc.
		if *devmode && frameIndex%18000 == 0 {
			lg.LogStats(stats)
		}
		frameIndex++

		if platform.ShouldStop() && len(ui.activeModalDialogs) == 0 {
			// Do this while we're still running the event loop.
			globalConfig.SaveIfChanged(renderer, platform, world)
			break
		}
	}

	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			lg.Errorf("%s: unable to create memory profile file: %v", *memprofile, err)
		}
		if err = pprof.WriteHeapProfile(f); err != nil {
			lg.Errorf("%s: unable to write memory profile file: %v", *memprofile, err)
		}
		f.Close()
	}

	if *devmode {
		fmt.Print(lg.GetErrorLog())
	}
}
