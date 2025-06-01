// main.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// This file contains the implementation of the main() function, which
// Initializes the system and then runs the event loop until the system
// exits.

import (
	_ "embed"
	"flag"
	"fmt"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/apenwarr/fixconsole"
)

var (
	//go:embed resources/version.txt
	buildVersion string

	// Command-line options are only used for developer features.
	cpuprofile        = flag.String("cpuprofile", "", "write CPU profile to file")
	memprofile        = flag.String("memprofile", "", "write memory profile to this file")
	logLevel          = flag.String("loglevel", "info", "logging level: debug, info, warn, error")
	logDir            = flag.String("logdir", "", "log file directory")
	lintScenarios     = flag.Bool("lint", false, "check the validity of the built-in scenarios")
	runServer         = flag.Bool("runserver", false, "run vice scenario server")
	serverPort        = flag.Int("port", server.ViceServerPort, "port to listen on when running server")
	serverAddress     = flag.String("server", server.ViceServerAddress+fmt.Sprintf(":%d", server.ViceServerPort), "IP address of vice multi-controller server")
	scenarioFilename  = flag.String("scenario", "", "filename of JSON file with a scenario definition")
	videoMapFilename  = flag.String("videomap", "", "filename of JSON file with video map definitions")
	broadcastMessage  = flag.String("broadcast", "", "message to broadcast to all active clients on the server")
	broadcastPassword = flag.String("password", "", "password to authenticate with server for broadcast message")
	resetSim          = flag.Bool("resetsim", false, "discard the saved simulation and do not try to resume it")
	showRoutes        = flag.String("routes", "", "display the STARS, SIDs, and approaches known for the given airport")
	listMaps          = flag.String("listmaps", "", "path to a video map file to list maps of (e.g., resources/videomaps/ZNY-videomaps.gob.zst)")
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

	// Common initialization for both client and server
	if err := fixconsole.FixConsoleIfNeeded(); err != nil {
		// Not sure this will actually appear, but what else are we going
		// to do...
		fmt.Printf("FixConsole: %v\n", err)
	}

	// Initialize the logging system first and foremost.
	lg := log.New(*runServer, *logLevel, *logDir)

	profiler, err := util.CreateProfiler(*cpuprofile, *memprofile)
	if err != nil {
		lg.Errorf("%v", err)
	}
	defer profiler.Cleanup()

	if *serverAddress != "" && !strings.Contains(*serverAddress, ":") {
		*serverAddress += fmt.Sprintf(":%d", server.ViceServerPort)
	}

	if *lintScenarios {
		var e util.ErrorLogger
		scenarioGroups, _, _ :=
			server.LoadScenarioGroups(true, *scenarioFilename, *videoMapFilename, &e, lg)

		videoMaps := make(map[string]interface{})
		for _, sgs := range scenarioGroups {
			for _, sg := range sgs {
				if sg.STARSFacilityAdaptation.VideoMapFile != "" {
					videoMaps[sg.STARSFacilityAdaptation.VideoMapFile] = nil
				}
			}
		}
		for m := range videoMaps {
			sim.CheckVideoMapManifest(m, &e)
		}

		if e.HaveErrors() {
			e.PrintErrors(nil)
			os.Exit(1)
		}

		scenarioAirports := make(map[string]map[string]interface{})
		for tracon, scenarios := range scenarioGroups {
			if scenarioAirports[tracon] == nil {
				scenarioAirports[tracon] = make(map[string]interface{})
			}
			for _, sg := range scenarios {
				for name := range sg.Airports {
					scenarioAirports[tracon][name] = nil
				}
			}
		}

		for _, tracon := range util.SortedMapKeys(scenarioAirports) {
			airports := util.SortedMapKeys(scenarioAirports[tracon])
			fmt.Printf("%s (%s),\n", tracon, strings.Join(airports, ", "))
		}
		os.Exit(0)
	} else if *broadcastMessage != "" {
		server.BroadcastMessage(*serverAddress, *broadcastMessage, *broadcastPassword, lg)
	} else if *runServer {
		server.RunServer(*scenarioFilename, *videoMapFilename, *serverPort, lg)
	} else if *showRoutes != "" {
		if err := av.PrintCIFPRoutes(*showRoutes); err != nil {
			lg.Errorf("%s", err)
		}
	} else if *listMaps != "" {
		var e util.ErrorLogger
		sim.PrintVideoMaps(*listMaps, &e)
		if e.HaveErrors() {
			e.PrintErrors(lg)
		}
	} else {
		var stats Stats
		var render renderer.Renderer
		var plat platform.Platform

		defer lg.CatchAndReportCrash()

		go func() {
			t := time.Tick(15 * time.Second)
			for {
				<-t
				// Try to more aggressively return freed memory to the OS.
				debug.FreeOSMemory()
			}
		}()

		///////////////////////////////////////////////////////////////////////////
		// Global initialization and set up. Note that there are some subtle
		// inter-dependencies in the following; the order is carefully crafted.

		_ = imguiInit()

		config, configErr := LoadOrMakeDefaultConfig(lg)

		var controlClient *server.ControlClient
		var mgr *server.ConnectionManager
		var err error
		var simErrorLogger util.ErrorLogger

		plat, err = platform.New(&config.Config, lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to create application window: %v", err))
		}

		if err := speech.InitTTS(); err != nil {
			lg.Errorf("TTS: %v", err)
		}

		mgr, err = server.MakeServerConnection(*serverAddress, *scenarioFilename, *videoMapFilename,
			&simErrorLogger, lg,
			func(c *server.ControlClient) { // updated client
				if c != nil {
					panes.ResetSim(config.DisplayRoot, c, c.State, plat, lg)
				}
				uiResetControlClient(c, plat, lg)
				controlClient = c
			},
			func(err error) {
				switch err {
				case server.ErrRPCVersionMismatch:
					ShowErrorDialog(plat, lg,
						"This version of vice is incompatible with the vice multi-controller server.\n"+
							"If you're using an older version of vice, please upgrade to the latest\n"+
							"version for multi-controller support. (If you're using a beta build, then\n"+
							"thanks for your help testing vice; when the beta is released, the server\n"+
							"will be updated as well.)")

				case server.ErrServerDisconnected:
					ShowErrorDialog(plat, lg, "Lost connection to the vice server.")
					uiShowConnectDialog(mgr, false, config, plat, lg)

				default:
					lg.Errorf("Server connection error: %v", err)
				}
			},
		)
		if err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}

		if configErr != nil {
			ShowErrorDialog(plat, lg, "Configuration file is corrupt: %v", configErr)
		}
		imgui.CurrentPlatformIO().SetClipboardHandler(plat.GetClipboard())

		render, err = renderer.NewOpenGL2Renderer(lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
		}
		renderer.FontsInit(render, plat)

		eventStream := sim.NewEventStream(lg)

		uiInit(render, plat, config, eventStream, lg)

		config.Activate(render, plat, eventStream, lg)

		if simErrorLogger.HaveErrors() { // After we have plat and render
			ShowFatalErrorDialog(render, plat, lg, "%s", simErrorLogger.String())
		}

		// After config.Activate(), if we have a loaded sim, get configured for it.
		if config.Sim != nil && !*resetSim {
			if client, err := mgr.LoadLocalSim(config.Sim, lg); err != nil {
				lg.Errorf("Error loading local sim: %v", err)
			} else {
				panes.LoadedSim(config.DisplayRoot, client, client.State, plat, lg)
				uiResetControlClient(client, plat, lg)
				controlClient = client
			}
		}

		if !mgr.Connected() {
			uiShowConnectDialog(mgr, false, config, plat, lg)
		}

		///////////////////////////////////////////////////////////////////////////
		// Main event / rendering loop
		lg.Info("Starting main loop")

		stats.startTime = time.Now()
		for {
			plat.SetWindowTitle("vice: " + controlClient.Status())

			if controlClient == nil {
				SetDiscordStatus(DiscordStatus{Start: mgr.ConnectionStartTime()}, config, lg)
			} else {
				id := controlClient.State.UserTCP
				if ctrl, ok := controlClient.State.Controllers[id]; ok {
					id += " (" + ctrl.Position + ")"
				}
				stats := controlClient.SessionStats
				SetDiscordStatus(DiscordStatus{
					TotalDepartures: stats.Departures + stats.IntraFacility,
					TotalArrivals:   stats.Arrivals + stats.IntraFacility,
					Position:        id,
					Start:           mgr.ConnectionStartTime(),
				}, config, lg)
			}

			mgr.Update(eventStream, plat, lg)

			// Inform imgui about input events from the user.
			plat.ProcessEvents()

			stats.redraws++

			plat.NewFrame()
			imgui.NewFrame()

			// Generate and render vice draw lists
			stats.drawPanes = panes.DrawPanes(config.DisplayRoot, plat, render, controlClient,
				ui.menuBarHeight, lg)

			// Draw the user interface
			stats.drawUI = uiDraw(mgr, config, plat, render, controlClient, eventStream, lg)

			// Wait for vsync
			plat.PostRender()

			// Periodically log current memory use, etc.
			if stats.redraws%18000 == 9000 { // Every 5min at 60fps, starting 2.5min after launch
				lg.Info("performance", "stats", stats)
			}

			if plat.ShouldStop() && len(ui.activeModalDialogs) == 0 {
				// Do this while we're still running the event loop.
				saveSim := mgr.ClientIsLocal()
				config.SaveIfChanged(render, plat, controlClient, saveSim, lg)
				mgr.Disconnect()
				break
			}
		}
	}
}
