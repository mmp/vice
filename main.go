// main.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// This file contains the implementation of the main() function, which
// initializes the system and then runs the event loop until the system
// exits.

import (
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
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
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/apenwarr/fixconsole"
	"github.com/mmp/imgui-go/v4"
)

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8001

var (
	//go:embed resources/version.txt
	buildVersion string

	// Command-line options are only used for developer features.
	cpuprofile        = flag.String("cpuprofile", "", "write CPU profile to file")
	memprofile        = flag.String("memprofile", "", "write memory profile to this file")
	logLevel          = flag.String("loglevel", "info", "logging level: debug, info, warn, error")
	lintScenarios     = flag.Bool("lint", false, "check the validity of the built-in scenarios")
	server            = flag.Bool("runserver", false, "run vice scenario server")
	serverPort        = flag.Int("port", ViceServerPort, "port to listen on when running server")
	serverAddress     = flag.String("server", ViceServerAddress+fmt.Sprintf(":%d", ViceServerPort), "IP address of vice multi-controller server")
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

	rand.Seed(time.Now().UnixNano())

	// Common initialization for both client and server
	if err := fixconsole.FixConsoleIfNeeded(); err != nil {
		// Not sure this will actually appear, but what else are we going
		// to do...
		fmt.Printf("FixConsole: %v\n", err)
	}

	// Initialize the logging system first and foremost.
	lg := log.New(*server, *logLevel)

	profiler, err := util.CreateProfiler(*cpuprofile, *memprofile)
	if err != nil {
		lg.Errorf("%v", err)
	}
	defer profiler.Cleanup()

	if *lintScenarios {
		var e util.ErrorLogger
		scenarioGroups, _, _ :=
			sim.LoadScenarioGroups(true, *scenarioFilename, *videoMapFilename, &e, lg)
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
		sim.BroadcastMessage(*serverAddress, *broadcastMessage, *broadcastPassword, lg)
	} else if *server {
		sim.RunServer(*scenarioFilename, *videoMapFilename, *serverPort, lg)
	} else if *showRoutes != "" {
		if err := av.PrintCIFPRoutes(*showRoutes); err != nil {
			lg.Errorf("%s", err)
		}
	} else if *listMaps != "" {
		var e util.ErrorLogger
		av.PrintVideoMaps(*listMaps, &e)
		if e.HaveErrors() {
			e.PrintErrors(lg)
		}
	} else {
		localSimServerChan, mapLibrary, err :=
			sim.LaunchLocalServer(*scenarioFilename, *videoMapFilename, lg)
		if err != nil {
			lg.Errorf("error launching local SimServer: %v", err)
			os.Exit(1)
		}

		lastRemoteServerAttempt := time.Now()
		remoteSimServerChan := sim.TryConnectRemoteServer(*serverAddress, lg)

		var stats Stats
		var render renderer.Renderer
		var plat platform.Platform
		var localServer *sim.Server
		var remoteServer *sim.Server

		// Catch any panics so that we can put up a dialog box and hopefully
		// get a bug report.
		var context *imgui.Context
		if os.Getenv("DELVE_GOVERSION") == "" { // hack: don't catch panics when debugging..
			defer func() {
				if err := recover(); err != nil {
					lg.Error("Caught panic!", slog.String("stack", string(debug.Stack())))
					ShowFatalErrorDialog(render, plat, lg,
						"Unfortunately an unexpected error has occurred and vice is unable to recover.\n"+
							"Apologies! Please do file a bug and include the vice.log file for this session\nso that "+
							"this bug can be fixed.\n\nError: %v", err)
				}

				// Clean up in backwards order from how things were created.
				render.Dispose()
				plat.Dispose()
				context.Destroy()
			}()
		}

		///////////////////////////////////////////////////////////////////////////
		// Global initialization and set up. Note that there are some subtle
		// inter-dependencies in the following; the order is carefully crafted.

		context = imguiInit()

		config, configErr := LoadOrMakeDefaultConfig(lg)

		plat, err = platform.New(&config.Config, lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to create application window: %v", err))
		}
		if configErr != nil {
			ShowErrorDialog(plat, lg, "Configuration file is corrupt: %v", configErr)
		}
		imgui.CurrentIO().SetClipboard(plat.GetClipboard())

		render, err = renderer.NewOpenGL2Renderer(lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
		}

		renderer.FontsInit(render, plat)

		newSimConnectionChan := make(chan *sim.Connection, 2)
		var controlClient *sim.ControlClient

		localServer = <-localSimServerChan

		if config.Sim != nil && !*resetSim {
			if err := config.Sim.PostLoad(mapLibrary); err != nil {
				lg.Errorf("Error in Sim PostLoad: %v", err)
			} else {
				var result sim.NewSimResult
				if err := localServer.Call("SimManager.Add", config.Sim, &result); err != nil {
					lg.Errorf("error restoring saved Sim: %v", err)
				} else {
					controlClient = sim.NewControlClient(*result.SimState, result.ControllerToken,
						localServer.RPCClient, lg)
					ui.showScenarioInfo = !ui.showScenarioInfo
				}
			}
		}

		eventStream := sim.NewEventStream(lg)

		uiInit(render, plat, config, eventStream, lg)

		config.Activate(controlClient, render, plat, eventStream, lg)

		if controlClient == nil {
			uiShowConnectDialog(newSimConnectionChan, &localServer, &remoteServer, false,
				config, plat, lg)
		}

		simStartTime := time.Now()

		///////////////////////////////////////////////////////////////////////////
		// Main event / rendering loop
		lg.Info("Starting main loop")

		stopConnectingRemoteServer := false
		stats.startTime = time.Now()
		for {
			select {
			case ns := <-newSimConnectionChan:
				if controlClient != nil {
					controlClient.Disconnect()
				}
				controlClient = sim.NewControlClient(ns.SimState, ns.SimProxy.ControllerToken,
					ns.SimProxy.Client, lg)
				simStartTime = time.Now()

				if controlClient == nil {
					uiShowConnectDialog(newSimConnectionChan, &localServer, &remoteServer,
						false, config, plat, lg)
				} else {
					ui.showScenarioInfo = !ui.showScenarioInfo
					panes.Reset(config.DisplayRoot, controlClient.State, lg)
				}

			case remoteServerConn := <-remoteSimServerChan:
				if err := remoteServerConn.Err; err != nil {
					lg.Warn("Unable to connect to remote server", slog.Any("error", err))

					if err.Error() == sim.ErrRPCVersionMismatch.Error() {
						ShowErrorDialog(plat, lg,
							"This version of vice is incompatible with the vice multi-controller server.\n"+
								"If you're using an older version of vice, please upgrade to the latest\n"+
								"version for multi-controller support. (If you're using a beta build, then\n"+
								"thanks for your help testing vice; when the beta is released, the server\n"+
								"will be updated as well.)")
						stopConnectingRemoteServer = true
					}
					remoteServer = nil
				} else {
					remoteServer = remoteServerConn.Server
				}

			default:
			}

			if controlClient == nil {
				plat.SetWindowTitle("vice: [disconnected]")
				SetDiscordStatus(DiscordStatus{Start: simStartTime}, config, lg)
			} else {
				title := "(disconnected)"
				if controlClient.SimDescription != "" {
					deparr := fmt.Sprintf(" [ %d departures %d arrivals ]", controlClient.TotalDepartures, controlClient.TotalArrivals)
					if controlClient.SimName == "" {
						title = controlClient.State.Callsign + ": " + controlClient.SimDescription + deparr
					} else {
						title = controlClient.State.Callsign + "@" + controlClient.SimName + ": " + controlClient.SimDescription + deparr
					}
				}

				plat.SetWindowTitle("vice: " + title)
				// Update discord RPC
				SetDiscordStatus(DiscordStatus{
					TotalDepartures: controlClient.State.TotalDepartures,
					TotalArrivals:   controlClient.State.TotalArrivals,
					Callsign:        controlClient.State.Callsign,
					Start:           simStartTime,
				}, config, lg)
			}

			if remoteServer == nil && time.Since(lastRemoteServerAttempt) > 10*time.Second && !stopConnectingRemoteServer {
				lastRemoteServerAttempt = time.Now()
				remoteSimServerChan = sim.TryConnectRemoteServer(*serverAddress, lg)
			}

			// Inform imgui about input events from the user.
			plat.ProcessEvents()

			stats.redraws++

			// Let the world update its state based on messages from the
			// network; a synopsis of changes to aircraft is then passed along
			// to the window panes.
			if controlClient != nil {
				controlClient.GetUpdates(eventStream,
					func(err error) {
						eventStream.Post(sim.Event{
							Type:    sim.StatusMessageEvent,
							Message: "Error getting update from server: " + err.Error(),
						})
						if util.IsRPCServerError(err) {
							uiShowModalDialog(NewModalDialogBox(&ErrorModalClient{
								message: "Lost connection to the vice server.",
							}, plat), true)

							remoteServer = nil
							controlClient = nil

							uiShowConnectDialog(newSimConnectionChan, &localServer, &remoteServer,
								false, config, plat, lg)
						}
					})
			}

			plat.NewFrame()
			imgui.NewFrame()

			// Generate and render vice draw lists
			stats.drawPanes = panes.DrawPanes(config.DisplayRoot, plat, render, controlClient,
				ui.menuBarHeight, &config.AudioEnabled, lg)

			// Draw the user interface
			stats.drawUI = drawUI(newSimConnectionChan, &localServer, &remoteServer,
				config, plat, render, controlClient, eventStream, lg)

			// Wait for vsync
			plat.PostRender()

			// Periodically log current memory use, etc.
			if stats.redraws%18000 == 0 {
				lg.Debug("performance", slog.Any("stats", stats))
			}

			if plat.ShouldStop() && len(ui.activeModalDialogs) == 0 {
				// Do this while we're still running the event loop.
				saveSim := controlClient != nil && controlClient.RPCClient() == localServer.RPCClient
				config.SaveIfChanged(render, plat, controlClient, saveSim, lg)

				if controlClient != nil {
					controlClient.Disconnect()
				}
				break
			}
		}
	}
}
