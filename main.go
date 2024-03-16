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
	"io/fs"
	"log/slog"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"time"

	"github.com/apenwarr/fixconsole"
	"github.com/checkandmate1/AirportWeatherData"
	"github.com/mmp/imgui-go/v4"
)

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8000

var (
	// There are a handful of widely-used global variables in vice, all
	// defined here.  While in principle it would be nice to have fewer (or
	// no!) globals, it's cleaner to have these easily accessible where in
	// the system without having to pass them through deep callchains.
	// Note that in some cases they are passed down from main (e.g.,
	// platform); this is plumbing in preparation for reducing the
	// number of these in the future.
	globalConfig *GlobalConfig
	platform     Platform
	database     *StaticDatabase
	lg           *Logger
	resourcesFS  fs.StatFS

	// client only
	newWorldChan chan *World
	localServer  *SimServer
	remoteServer *SimServer
	airportWind  map[string]Wind
	windRequest  map[string]chan []getweather.MetarData
	heldAircraft []*Aircraft
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
		fmt.Printf("FixConsole: %v", err)
	}

	// Initialize the logging system first and foremost.
	lg = NewLogger(*server, *logLevel)

	// If the path is non-absolute, convert it to an absolute path
	// w.r.t. the current directory.  (This is to work around that vice
	// changes the working directory to above where the resources are,
	// which in turn was causing profiling data to be written in an
	// unexpected place...)
	absPath := func(p *string) {
		if p != nil && !path.IsAbs(*p) {
			if cwd, err := os.Getwd(); err == nil {
				*p = path.Join(cwd, *p)
			}
		}
	}
	absPath(memprofile)
	absPath(cpuprofile)

	writeMemProfile := func() {
		f, err := os.Create(*memprofile)
		if err != nil {
			lg.Errorf("%s: unable to create memory profile file: %v", *memprofile, err)
		}
		if err = pprof.WriteHeapProfile(f); err != nil {
			lg.Errorf("%s: unable to write memory profile file: %v", *memprofile, err)
		}
		f.Close()
	}

	if *cpuprofile != "" {
		if f, err := os.Create(*cpuprofile); err != nil {
			lg.Errorf("%s: unable to create CPU profile file: %v", *cpuprofile, err)
		} else {
			if err = pprof.StartCPUProfile(f); err != nil {
				lg.Errorf("unable to start CPU profile: %v", err)
			} else {
				defer pprof.StopCPUProfile()

				// Catch ctrl-c and to write out the profile before exiting
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, os.Interrupt)

				go func() {
					<-sig
					pprof.StopCPUProfile()
					f.Close()
					os.Exit(0)
				}()
			}
		}
	}
	if *memprofile != "" {
		// Catch ctrl-c
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		go func() {
			<-sig
			writeMemProfile()
			os.Exit(0)
		}()
	}

	resourcesFS = getResourcesFS()

	eventStream := NewEventStream()

	database = InitializeStaticDatabase()

	if *lintScenarios {
		var e ErrorLogger
		_, _ = LoadScenarioGroups(&e)
		if e.HaveErrors() {
			e.PrintErrors(nil)
			os.Exit(1)
		}
	} else if *broadcastMessage != "" {
		BroadcastMessage(*serverAddress, *broadcastMessage, *broadcastPassword)
	} else if *server {
		RunSimServer()
	} else if *showRoutes != "" {
		ap, ok := database.Airports[*showRoutes]
		if !ok {
			fmt.Printf("%s: airport not present in database\n", *showRoutes)
			os.Exit(1)
		}
		fmt.Printf("STARs:\n")
		for _, s := range SortedMapKeys(ap.STARs) {
			ap.STARs[s].Print(s)
		}
		fmt.Printf("\nApproaches:\n")
		for _, appr := range SortedMapKeys(ap.Approaches) {
			fmt.Printf("%-5s: ", appr)
			for i, wp := range ap.Approaches[appr] {
				if i > 0 {
					fmt.Printf("       ")
				}
				fmt.Println(wp.Encode())
			}
		}
	} else {
		localSimServerChan, err := LaunchLocalSimServer()
		if err != nil {
			lg.Errorf("error launching local SimServer: %v", err)
			os.Exit(1)
		}

		lastRemoteServerAttempt := time.Now()
		remoteSimServerChan := TryConnectRemoteServer(*serverAddress)

		var stats Stats
		var renderer Renderer

		// Catch any panics so that we can put up a dialog box and hopefully
		// get a bug report.
		var context *imgui.Context
		if os.Getenv("DELVE_GOVERSION") == "" { // hack: don't catch panics when debugging..
			defer func() {
				if err := recover(); err != nil {
					lg.Error("Caught panic!", slog.String("stack", string(debug.Stack())))
					ShowFatalErrorDialog(renderer, platform,
						"Unfortunately an unexpected error has occurred and vice is unable to recover.\n"+
							"Apologies! Please do file a bug and include the vice.log file for this session\nso that "+
							"this bug can be fixed.\n\nError: %v", err)
				}

				// Clean up in backwards order from how things were created.
				renderer.Dispose()
				platform.Dispose()
				context.Destroy()
			}()
		}

		///////////////////////////////////////////////////////////////////////////
		// Global initialization and set up. Note that there are some subtle
		// inter-dependencies in the following; the order is carefully crafted.

		context = imguiInit()

		LoadOrMakeDefaultConfig()

		multisample := runtime.GOOS != "darwin"
		platform, err = NewGLFWPlatform(imgui.CurrentIO(), globalConfig.InitialWindowSize,
			globalConfig.InitialWindowPosition, multisample)
		if err != nil {
			panic(fmt.Sprintf("Unable to create application window: %v", err))
		}
		imgui.CurrentIO().SetClipboard(platform.GetClipboard())

		renderer, err = NewOpenGL2Renderer()
		if err != nil {
			panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
		}

		fontsInit(renderer, platform)

		newWorldChan = make(chan *World, 2)
		var world *World

		localServer = <-localSimServerChan

		if globalConfig.Sim != nil && !*resetSim {
			var result NewSimResult
			if err := localServer.Call("SimManager.Add", globalConfig.Sim, &result); err != nil {
				lg.Errorf("error restoring saved Sim: %v", err)
			} else {
				world = result.World
				world.simProxy = &SimProxy{
					ControllerToken: result.ControllerToken,
					Client:          localServer.RPCClient,
				}
				world.ToggleShowScenarioInfoWindow()
			}
		}

		wmInit()

		uiInit(renderer, platform, eventStream)

		globalConfig.Activate(world, renderer, eventStream)

		if world == nil {
			uiShowConnectDialog(false)
		}

		if !globalConfig.AskedDiscordOptIn {
			uiShowDiscordOptInDialog()
		}

		simStartTime := time.Now()

		///////////////////////////////////////////////////////////////////////////
		// Main event / rendering loop
		lg.Info("Starting main loop")
		// Init the maps
		airportWind = make(map[string]Wind)
		windRequest = make(map[string]chan []getweather.MetarData)

		stopConnectingRemoteServer := false
		frameIndex := 0
		stats.startTime = time.Now()
		for {
			select {
			case nw := <-newWorldChan:
				if world != nil {
					world.Disconnect()
				}
				world = nw
				simStartTime = time.Now()

				if world == nil {
					uiShowConnectDialog(false)
				} else if world != nil {
					world.ToggleShowScenarioInfoWindow()
					globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
						p.ResetWorld(world)
					})
				}

			case remoteServerConn := <-remoteSimServerChan:
				if err := remoteServerConn.err; err != nil {
					lg.Warn("Unable to connect to remote server", slog.Any("error", err))

					if err.Error() == ErrRPCVersionMismatch.Error() {
						uiShowModalDialog(NewModalDialogBox(&ErrorModalClient{
							message: "This version of vice is incompatible with the vice multi-controller server.\n" +
								"If you're using an older version of vice, please upgrade to the latest\n" +
								"version for multi-controller support. (If you're using a beta build, then\n" +
								"thanks for your help testing vice; when the beta is released, the server\n" +
								"will be updated as well.)",
						}), true)

						stopConnectingRemoteServer = true
					}
					remoteServer = nil
				} else {
					remoteServer = remoteServerConn.server
				}

			default:
			}

			if world == nil {
				platform.SetWindowTitle("vice: [disconnected]")
				SetDiscordStatus(discordStatus{start: simStartTime})
			} else {
				platform.SetWindowTitle("vice: " + world.GetWindowTitle())
				// Update discord RPC
				SetDiscordStatus(discordStatus{
					totalDepartures: world.TotalDepartures,
					totalArrivals:   world.TotalArrivals,
					callsign:        world.Callsign,
					start:           simStartTime,
				})
			}

			if remoteServer == nil && time.Since(lastRemoteServerAttempt) > 10*time.Second && !stopConnectingRemoteServer {
				lastRemoteServerAttempt = time.Now()
				remoteSimServerChan = TryConnectRemoteServer(*serverAddress)
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
				world.GetUpdates(eventStream,
					func(err error) {
						eventStream.Post(Event{
							Type:    StatusMessageEvent,
							Message: "Error getting update from server: " + err.Error(),
						})
						if isRPCServerError(err) {
							uiShowModalDialog(NewModalDialogBox(&ErrorModalClient{
								message: "Lost connection to the vice server.",
							}), true)

							remoteServer = nil
							world = nil

							uiShowConnectDialog(false)
						}
					})
			}

			platform.NewFrame()
			imgui.NewFrame()

			// Generate and render vice draw lists
			if world != nil {
				wmDrawPanes(platform, renderer, world, &stats)
			} else {
				commandBuffer := GetCommandBuffer()
				commandBuffer.ClearRGB(RGB{})
				stats.render = renderer.RenderCommandBuffer(commandBuffer)
				ReturnCommandBuffer(commandBuffer)
			}

			timeMarker(&stats.drawPanes)

			// Draw the user interface
			drawUI(platform, renderer, world, eventStream, &stats)
			timeMarker(&stats.drawImgui)

			// Wait for vsync
			platform.PostRender()

			// Periodically log current memory use, etc.
			if frameIndex%18000 == 0 {
				lg.Debug("performance", slog.Any("stats", stats))
			}
			frameIndex++

			if platform.ShouldStop() && len(ui.activeModalDialogs) == 0 {
				// Do this while we're still running the event loop.
				saveSim := world != nil && world.simProxy.Client == localServer.RPCClient
				globalConfig.SaveIfChanged(renderer, platform, world, saveSim)

				if world != nil {
					world.Disconnect()
				}
				break
			}
		}
	}

	// Common cleanup
	if *memprofile != "" {
		writeMemProfile()
	}
}
