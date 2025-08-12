// main.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// This file contains the implementation of the main() function, which
// Initializes the system and then runs the event loop until the system
// exits.

import (
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/apenwarr/fixconsole"
)

var (
	// Command-line options are only used for developer features.
	cpuprofile        = flag.String("cpuprofile", "", "write CPU profile to file")
	memprofile        = flag.String("memprofile", "", "write memory profile to this file")
	logLevel          = flag.String("loglevel", "info", "logging level: debug, info, warn, error")
	logDir            = flag.String("logdir", "", "log file directory")
	lintScenarios     = flag.Bool("lint", false, "check the validity of the built-in scenarios")
	runServer         = flag.Bool("runserver", false, "run vice scenario server")
	serverPort        = flag.Int("port", server.ViceServerPort, "port to listen on when running server")
	serverAddress     = flag.String("server", net.JoinHostPort(server.ViceServerAddress, strconv.Itoa(server.ViceServerPort)), "IP address of vice multi-controller server")
	scenarioFilename  = flag.String("scenario", "", "filename of JSON file with a scenario definition")
	videoMapFilename  = flag.String("videomap", "", "filename of JSON file with video map definitions")
	broadcastMessage  = flag.String("broadcast", "", "message to broadcast to all active clients on the server")
	broadcastPassword = flag.String("password", "", "password to authenticate with server for broadcast message")
	resetSim          = flag.Bool("resetsim", false, "discard the saved simulation and do not try to resume it")
	showRoutes        = flag.String("routes", "", "display the STARS, SIDs, and approaches known for the given airport")
	listMaps          = flag.String("listmaps", "", "path to a video map file to list maps of (e.g., resources/videomaps/ZNY-videomaps.gob.zst)")
	listScenarios     = flag.Bool("listscenarios", false, "list all available scenarios in ARTCC/TRACON/scenario format")
	runSim            = flag.String("runsim", "", "run specified scenario for 3600 update steps (format: ARTCC/TRACON/scenario)")
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
		*serverAddress = net.JoinHostPort(*serverAddress, strconv.Itoa(server.ViceServerPort))
	}

	if *lintScenarios {
		if err := SyncResources(nil, nil, nil); err != nil {
			lg.Errorf("SyncResources: %v", err)
			os.Exit(1)
		}

		av.InitDB()

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
	} else if *listScenarios {
		scenarios, err := server.ListAllScenarios(*scenarioFilename, *videoMapFilename, lg)
		if err != nil {
			lg.Errorf("Failed to list scenarios: %v", err)
			os.Exit(1)
		}

		for _, s := range scenarios {
			fmt.Println(s)
		}
	} else if *runSim != "" {
		parts := strings.SplitN(*runSim, "/", 2)
		if len(parts) != 2 {
			lg.Errorf("Invalid scenario format. Expected: TRACON/scenario")
			os.Exit(1)
		}
		tracon, scenarioName := parts[0], parts[1]

		var e util.ErrorLogger
		scenarioGroups, configs, _ := server.LoadScenarioGroups(false, *scenarioFilename, *videoMapFilename, &e, lg)
		if e.HaveErrors() {
			e.PrintErrors(lg)
			os.Exit(1)
		}

		// Find the matching scenario
		config, scenarioGroup, err := server.LookupScenario(tracon, scenarioName, scenarioGroups, configs)
		if err != nil {
			lg.Errorf("%v", err)
			os.Exit(1)
		}

		fmt.Printf("Running scenario: %s\n", *runSim)

		newSimConfig, err := server.CreateNewSimConfiguration(config, scenarioGroup, scenarioName)
		if err != nil {
			lg.Errorf("Failed to create simulation configuration: %v", err)
			os.Exit(1)
		}

		s := sim.NewSim(*newSimConfig, nil /*manifest*/, lg)

		state, err := s.SignOn(newSimConfig.PrimaryController, false, false)
		if err != nil {
			lg.Errorf("Failed to sign in primary controller %s: %v", newSimConfig.PrimaryController, err)
			os.Exit(1)
		}

		// Check launch configuration
		fmt.Printf("Departure rates: %v\n", state.LaunchConfig.DepartureRates)
		fmt.Printf("Inbound flow rates: %v\n", state.LaunchConfig.InboundFlowRates)

		startTime := time.Now()
		s.Prespawn()

		// Check initial aircraft count
		fmt.Printf("Starting simulation with %d aircraft\n", len(s.Aircraft))

		// Run the sim for an hour of virtual time.
		const totalUpdates = 3600
		for range totalUpdates {
			s.Step(time.Second)
		}

		// Check final aircraft count
		fmt.Printf("Simulation ended with %d aircraft\n", len(s.Aircraft))

		elapsed := time.Since(startTime)
		fmt.Printf("Simulation complete: %d updates in %.2f seconds (%.1fx real-time)\n",
			totalUpdates, elapsed.Seconds(), totalUpdates/elapsed.Seconds())
	} else if *broadcastMessage != "" {
		client.BroadcastMessage(*serverAddress, *broadcastMessage, *broadcastPassword, lg)
	} else if *runServer {
		if err := SyncResources(nil, nil, nil); err != nil {
			lg.Errorf("SyncResources: %v", err)
			os.Exit(1)
		}

		av.InitDB()

		server.LaunchServer(server.ServerLaunchConfig{
			Port:                *serverPort,
			MultiControllerOnly: true,
			ExtraScenario:       *scenarioFilename,
			ExtraVideoMap:       *videoMapFilename,
		}, lg)
	} else if *showRoutes != "" {
		if err := SyncResources(nil, nil, nil); err != nil {
			lg.Errorf("SyncResources: %v", err)
			os.Exit(1)
		}

		av.InitDB()

		if err := av.PrintCIFPRoutes(*showRoutes); err != nil {
			lg.Errorf("%s", err)
		}
	} else if *listMaps != "" {
		if err := SyncResources(nil, nil, nil); err != nil {
			lg.Errorf("SyncResources: %v", err)
			os.Exit(1)
		}

		av.InitDB()

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

		var controlClient *client.ControlClient
		var err error

		plat, err = platform.New(&config.Config, lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to create application window: %v", err))
		}

		imgui.CurrentPlatformIO().SetClipboardHandler(plat.GetClipboard())

		render, err = renderer.NewOpenGL2Renderer(lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
		}
		renderer.FontsInit(render, plat)

		eventStream := sim.NewEventStream(lg)

		uiInit(render, plat, config, eventStream, lg)

		if err := SyncResources(plat, render, lg); err != nil {
			ShowFatalErrorDialog(render, plat, lg, "Error syncing resources: %v", err)
		}

		av.InitDB()

		// After we have plat and render
		if configErr != nil {
			ShowErrorDialog(plat, lg, "Configuration file is corrupt: %v", configErr)
		}

		config.Activate(render, plat, eventStream, lg)

		var mgr *client.ConnectionManager
		var errorLogger util.ErrorLogger
		mgr, errorLogger = client.MakeServerManager(*serverAddress, *scenarioFilename, *videoMapFilename, lg,
			func(c *client.ControlClient) { // updated client
				if c != nil {
					// Determine if this is a STARS or ERAM scenario
					isSTARSSim := c.State.TRACON != ""
					
					// Rebuild the display hierarchy with the appropriate pane
					config.RebuildDisplayRootForSim(isSTARSSim)
					
					// Reactivate the display hierarchy
					panes.Activate(config.DisplayRoot, render, plat, eventStream, lg)
					
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

		if errorLogger.HaveErrors() {
			ShowFatalErrorDialog(render, plat, lg, "%s", errorLogger.String())
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
