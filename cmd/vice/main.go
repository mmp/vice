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
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goforj/godump"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stars"
	"github.com/mmp/vice/stt"
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
	navLog            = flag.Bool("navlog", false, "enable navigation logging")
	navLogCategories  = flag.String("navlog-categories", "all", "navigation log categories (comma-separated: state,waypoint,altitude,speed,heading,approach,command,route)")
	navLogCallsign    = flag.String("navlog-callsign", "", "filter navigation logs to only show this callsign (empty = show all)")
	replayMode        = flag.Bool("replay", false, "replay scenario from saved config")
	replayDuration    = flag.String("replay-duration", "3600", "replay duration in seconds or 'until:CALLSIGN'")
	waypointCommands  = flag.String("waypoint-commands", "", "waypoint commands in format 'FIX:CMD CMD CMD, FIX:CMD ...,'")
	starsRandoms      = flag.Bool("starsrandoms", false, "run STARS command fuzz testing with full UI (randomly picks a scenario)")
)

func setupSignalHandler(profiler *util.Profiler) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "Caught signal, cleaning up...")
		profiler.Cleanup()
		fmt.Fprintln(os.Stderr, "Cleanup complete, exiting")
		os.Exit(0)
	}()
}

func init() {
	// OpenGL and friends require that all calls be made from the primary
	// application thread, while by default, go allows the main thread to
	// run on different hardware threads over the course of
	// execution. Therefore, we must lock the main thread at startup time.
	runtime.LockOSThread()
}

func replayScenario(lg *log.Logger) {
	// Initialize navigation logging
	nav.InitNavLog(*navLog, *navLogCategories, *navLogCallsign)

	config, err := LoadOrMakeDefaultConfig(lg)
	if err != nil {
		lg.Errorf("Error loading config: %v", err)
		os.Exit(1)
	}

	if config.Sim == nil {
		lg.Errorf("No saved simulation found in config. Please configure a scenario in the UI first.")
		os.Exit(1)
	}

	if err := config.Sim.ReplayScenario(*waypointCommands, *replayDuration, lg); err != nil {
		lg.Errorf("Scenario replay failed: %v", err)
		os.Exit(1)
	}
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

	if *cpuprofile != "" || *memprofile != "" {
		setupSignalHandler(&profiler)
	}

	if *serverAddress != "" && !strings.Contains(*serverAddress, ":") {
		*serverAddress = net.JoinHostPort(*serverAddress, strconv.Itoa(server.ViceServerPort))
	}

	// Common initialization functionality when running in the shell and not launching the GUI.
	cliInit := func() {
		if err := SyncResources(nil, nil, nil); err != nil {
			lg.Errorf("SyncResources: %v", err)
			os.Exit(1)
		}

		av.InitDB()
	}
	_ = imguiInit()
	config, configErr := LoadOrMakeDefaultConfig(lg)
	if *scenarioFilename == "" && config.ScenarioFile != "" {
		*scenarioFilename = config.ScenarioFile
	}
	if *videoMapFilename == "" && config.VideoMapFile != "" {
		*videoMapFilename = config.VideoMapFile
	}

	if *lintScenarios {
		cliInit()

		var e util.ErrorLogger
		scenarioGroups, _, _, _ := server.LoadScenarioGroups(*scenarioFilename, *videoMapFilename, &e, lg)

		// Check emergencies.json
		loadEmergencies(&e)

		videoMaps := make(map[string]interface{})
		for _, sgs := range scenarioGroups {
			for _, sg := range sgs {
				if sg.FacilityAdaptation.VideoMapFile != "" {
					videoMaps[sg.FacilityAdaptation.VideoMapFile] = nil
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
		cliInit()

		scenarios, err := server.ListAllScenarios(*scenarioFilename, *videoMapFilename, lg)
		if err != nil {
			lg.Errorf("Failed to list scenarios: %v", err)
			os.Exit(1)
		}

		for _, s := range scenarios {
			fmt.Println(s)
		}
	} else if *runSim != "" {
		cliInit()

		parts := strings.SplitN(*runSim, "/", 2)
		if len(parts) != 2 {
			lg.Errorf("Invalid scenario format. Expected: TRACON/scenario")
			os.Exit(1)
		}
		tracon, scenarioName := parts[0], parts[1]

		var e util.ErrorLogger
		scenarioGroups, configs, _, _ := server.LoadScenarioGroups(*scenarioFilename, *videoMapFilename, &e, lg)
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

		// Initialize navigation logging if requested
		nav.InitNavLog(*navLog, *navLogCategories, *navLogCallsign)

		newSimConfig, err := server.CreateNewSimConfiguration(config, scenarioGroup, scenarioName)
		if err != nil {
			lg.Errorf("Failed to create simulation configuration: %v", err)
			os.Exit(1)
		}

		// Pick a random time in November 2025
		r := rand.Make()
		day, hour, min, sec := 1+r.Intn(30), r.Intn(24), r.Intn(60), r.Intn(60)
		newSimConfig.StartTime = time.Date(2025, time.November, day, hour, min, sec, 0, time.UTC)
		fmt.Printf("Simulation start time: %s\n", newSimConfig.StartTime.Format(time.RFC3339))

		var emergencyLogger util.ErrorLogger
		emergencies := loadEmergencies(&emergencyLogger)
		if emergencyLogger.HaveErrors() {
			emergencyLogger.PrintErrors(lg)
			os.Exit(1)
		}

		newSimConfig.Emergencies = emergencies
		s := sim.NewSim(*newSimConfig, nil /*manifest*/, lg)

		// Sign on as instructor if waypoint commands are specified
		instructor := *waypointCommands != ""
		rootController, _ := newSimConfig.ControllerConfiguration.RootPosition()
		state, _, err := s.SignOn(sim.TCW(rootController), s.AllScenarioPositions())
		if err != nil {
			lg.Errorf("Failed to sign in root controller %s: %v", rootController, err)
			os.Exit(1)
		}
		if instructor {
			s.SetPrivilegedTCW(sim.TCW(rootController), true)
		}

		// Apply waypoint commands after signing on
		s.SetWaypointCommands(sim.TCW(rootController), *waypointCommands)

		// Check launch configuration
		fmt.Println("Departure rates:")
		godump.Dump(state.LaunchConfig.DepartureRates)
		fmt.Println("Inbound flow rates:")
		godump.Dump(state.LaunchConfig.InboundFlowRates)

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
	} else if *replayMode {
		cliInit()
		replayScenario(lg)
	} else if *broadcastMessage != "" {
		client.BroadcastMessage(*serverAddress, *broadcastMessage, *broadcastPassword, lg)
	} else if *runServer {
		cliInit()

		// Initialize navigation logging if requested
		nav.InitNavLog(*navLog, *navLogCategories, *navLogCallsign)

		server.LaunchServer(server.ServerLaunchConfig{
			Port:          *serverPort,
			ExtraScenario: *scenarioFilename,
			ExtraVideoMap: *videoMapFilename,
			ServerAddress: *serverAddress,
			IsLocal:       false,
		}, lg)
	} else if *showRoutes != "" {
		cliInit()

		if err := av.PrintCIFPRoutes(*showRoutes); err != nil {
			lg.Errorf("%s", err)
		}
	} else if *listMaps != "" {
		cliInit()

		var e util.ErrorLogger
		sim.PrintVideoMaps(*listMaps, &e)
		if e.HaveErrors() {
			e.PrintErrors(lg)
		}
	} else {
		var stats Stats
		var render renderer.Renderer
		var plat platform.Platform
		var fuzzController *stars.FuzzController // For -starsrandoms mode

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

		// Initialize navigation logging if requested
		nav.InitNavLog(*navLog, *navLogCategories, *navLogCallsign)

		// After we have plat and render
		if configErr != nil {
			ShowErrorDialog(plat, lg, "Saved configuration file is corrupt. Discarding. (%v)", configErr)
		}

		config.Activate(render, plat, eventStream, lg)

		var mgr *client.ConnectionManager
		var errorLogger util.ErrorLogger
		var extraScenarioErrors string
		mgr, errorLogger, extraScenarioErrors = client.MakeServerManager(*serverAddress, *scenarioFilename, *videoMapFilename, lg,
			func(c *client.ControlClient) { // updated client
				if c != nil {
					// Determine if this is a STARS or ERAM scenario
					_, isSTARSSim := av.DB.TRACONs[c.State.Facility]

					// Rebuild the display hierarchy with the appropriate pane
					config.RebuildDisplayRootForSim(isSTARSSim)

					// Reactivate the display hierarchy
					panes.Activate(config.DisplayRoot, render, plat, eventStream, lg)

					panes.ResetSim(config.DisplayRoot, c, plat, lg)

					// Apply waypoint commands if specified via command line (only for new clients)
					if *waypointCommands != "" {
						c.SetWaypointCommands(*waypointCommands)
					}
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

		// Show non-fatal dialog for extra scenario errors
		if extraScenarioErrors != "" {
			ShowErrorDialog(plat, lg, "Errors in additional scenario file (scenario will not be loaded):\n\n%s", extraScenarioErrors)
		}

		// Show warning if server is unreachable and TTS is unavailable
		if mgr.LocalServer != nil && !mgr.LocalServer.HaveTTS {
			ShowErrorDialog(plat, lg,
				"Unable to connect to vice server.\n\n"+
					"Running in local-only mode without text-to-speech support.")
		}

		// After config.Activate(), if we have a loaded sim, get configured for it.
		if config.Sim != nil && !*resetSim && !*starsRandoms {
			if client, err := mgr.LoadLocalSim(config.Sim, config.ControllerInitials, lg); err != nil {
				lg.Errorf("Error loading local sim: %v", err)
			} else {
				panes.LoadedSim(config.DisplayRoot, client, plat, lg)
				uiResetControlClient(client, plat, lg)
				controlClient = client
				// Apply waypoint commands if specified via command line
				if *waypointCommands != "" {
					client.SetWaypointCommands(*waypointCommands)
				}
			}
		}

		// Handle -starsrandoms: randomly pick a scenario and start fuzz testing
		if *starsRandoms {
			// Determine which server to use (local or remote)
			var srv *client.Server
			defaultAddr := net.JoinHostPort(server.ViceServerAddress, strconv.Itoa(server.ViceServerPort))
			useRemote := *serverAddress != defaultAddr && *serverAddress != ""

			if useRemote {
				fmt.Printf("Waiting for remote server at %s...\n", *serverAddress)
				timeout := time.After(30 * time.Second)
				for mgr.RemoteServer == nil {
					mgr.Update(eventStream, plat, lg)
					select {
					case <-timeout:
						lg.Errorf("Timeout waiting for remote server connection")
						os.Exit(1)
					case <-time.After(100 * time.Millisecond):
					}
				}
				srv = mgr.RemoteServer
				fmt.Printf("Connected to remote server\n")
			} else {
				srv = mgr.LocalServer
			}

			// Select a random scenario and create the sim
			req, err := stars.SelectRandomScenario(srv)
			if err != nil {
				lg.Errorf("%v", err)
				os.Exit(1)
			}
			req.Privileged = true // Fuzz testing needs privileged access to control all aircraft
			if err := mgr.CreateNewSim(req, "FUZZ", srv, lg); err != nil {
				lg.Errorf("Failed to create sim: %v", err)
				os.Exit(1)
			}
			// controlClient is now set via the onNewClient callback

			// Create fuzz controller with the STARSPane
			fuzzController = stars.NewFuzzController(config.STARSPane, stars.FuzzConfig{}, lg)
		}

		if !mgr.Connected() && !*starsRandoms {
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
				pos := controlClient.State.GetPositionsForTCW(controlClient.State.UserTCW)
				posStr := strings.Join(util.MapSlice(pos, func(p sim.ControlPosition) string { return string(p) }), ", ")
				stats := controlClient.SessionStats
				SetDiscordStatus(DiscordStatus{
					TotalDepartures: stats.Departures + stats.IntraFacility,
					TotalArrivals:   stats.Arrivals + stats.IntraFacility,
					Position:        posStr,
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

			stt.ProcessSTTKeyboardInput(plat, controlClient, lg, config.UserPTTKey, &config.SelectedMicrophone)

			// Execute fuzz commands if in fuzz testing mode
			if fuzzController != nil && controlClient != nil {
				ctx := panes.NewFuzzContext(plat, render, controlClient, lg)
				fuzzController.ExecuteFrame(ctx, controlClient)
			}

			// Draw the user interface
			stats.drawUI = uiDraw(mgr, config, plat, render, controlClient, eventStream, lg)

			// Wait for vsync
			plat.PostRender()

			// Periodically log current memory use, etc.
			if stats.redraws%18000 == 9000 { // Every 5min at 60fps, starting 2.5min after launch
				lg.Info("performance", "stats", stats)
			}

			// Check for fuzz test completion
			if fuzzController != nil && !fuzzController.ShouldContinue() {
				elapsed := time.Since(stats.startTime)
				fuzzController.PrintStatistics()
				frameCount := fuzzController.FrameCount()
				fmt.Printf("Fuzz testing completed: %d frames in %.2fs (%.0f fps)\n",
					frameCount, elapsed.Seconds(), float64(frameCount)/elapsed.Seconds())
				mgr.Disconnect()
				break
			}

			if plat.ShouldStop() && !hasActiveModalDialogs() {
				// Do this while we're still running the event loop.
				if fuzzController != nil {
					fuzzController.PrintStatistics()
				}
				saveSim := mgr.ClientIsLocal() && fuzzController == nil // Don't save fuzz sims
				config.SaveIfChanged(render, plat, controlClient, saveSim, lg)
				mgr.Disconnect()
				break
			}
		}
	}
}
