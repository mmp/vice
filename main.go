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
	"log/slog"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
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
	// There are a handful of widely-used global variables in vice, all
	// defined here.  While in principle it would be nice to have fewer (or
	// no!) globals, it's cleaner to have these easily accessible where in
	// the system without having to pass them through deep callchains.
	// Note that in some cases they are passed down from main; this is
	// plumbing in preparation for reducing the number of these in the
	// future.
	globalConfig *GlobalConfig
	lg           *log.Logger

	// client only
	newSimConnectionChan chan *sim.SimConnection
	localServer          *sim.SimServer
	remoteServer         *sim.SimServer

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
	listScenarios     = flag.Bool("listscenarios", false, "list all of the available scenarios")
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
	lg = log.New(*server, *logLevel)

	// If the path is non-absolute, convert it to an absolute path
	// w.r.t. the current directory.  (This is to work around that vice
	// changes the working directory to above where the resources are,
	// which in turn was causing profiling data to be written in an
	// unexpected place...)
	absPath := func(p *string) {
		if p != nil && *p != "" && !path.IsAbs(*p) {
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

	eventStream := sim.NewEventStream(lg)

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
		sim.RunSimServer(*scenarioFilename, *videoMapFilename, *serverPort, lg)
	} else if *showRoutes != "" {
		ap, ok := av.DB.Airports[*showRoutes]
		if !ok {
			fmt.Printf("%s: airport not present in database\n", *showRoutes)
			os.Exit(1)
		}
		fmt.Printf("STARs:\n")
		for _, s := range util.SortedMapKeys(ap.STARs) {
			ap.STARs[s].Print(s)
		}
		fmt.Printf("\nApproaches:\n")
		for _, appr := range util.SortedMapKeys(ap.Approaches) {
			fmt.Printf("%-5s: ", appr)
			for i, wp := range ap.Approaches[appr] {
				if i > 0 {
					fmt.Printf("       ")
				}
				fmt.Println(wp.Encode())
			}
		}
	} else if *listMaps != "" {
		var e util.ErrorLogger
		lib := av.MakeVideoMapLibrary()
		path := *listMaps
		lib.AddFile(os.DirFS("."), path, true, make(map[string]interface{}), &e)

		if e.HaveErrors() {
			e.PrintErrors(lg)
			os.Exit(1)
		}

		var videoMaps []av.VideoMap
		for _, name := range lib.AvailableMaps(path) {
			if m, err := lib.GetMap(path, name); err != nil {
				panic(err)
			} else {
				videoMaps = append(videoMaps, *m)
			}
		}

		sort.Slice(videoMaps, func(i, j int) bool {
			vi, vj := videoMaps[i], videoMaps[j]
			if vi.Id != vj.Id {
				return vi.Id < vj.Id
			}
			return vi.Name < vj.Name
		})

		fmt.Printf("%5s\t%20s\t%s\n", "Id", "Label", "Name")
		for _, m := range videoMaps {
			fmt.Printf("%5d\t%20s\t%s\n", m.Id, m.Label, m.Name)
		}
	} else {
		localSimServerChan, mapLibrary, err :=
			sim.LaunchLocalSimServer(*scenarioFilename, *videoMapFilename, lg)
		if err != nil {
			lg.Errorf("error launching local SimServer: %v", err)
			os.Exit(1)
		}

		lastRemoteServerAttempt := time.Now()
		remoteSimServerChan := sim.TryConnectRemoteServer(*serverAddress, lg)

		var stats Stats
		var render renderer.Renderer
		var plat platform.Platform

		// Catch any panics so that we can put up a dialog box and hopefully
		// get a bug report.
		var context *imgui.Context
		if os.Getenv("DELVE_GOVERSION") == "" { // hack: don't catch panics when debugging..
			defer func() {
				if err := recover(); err != nil {
					lg.Error("Caught panic!", slog.String("stack", string(debug.Stack())))
					ShowFatalErrorDialog(render, plat,
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

		LoadOrMakeDefaultConfig(plat)

		plat, err = platform.New(&globalConfig.Config, lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to create application window: %v", err))
		}
		imgui.CurrentIO().SetClipboard(plat.GetClipboard())

		render, err = renderer.NewOpenGL2Renderer(lg)
		if err != nil {
			panic(fmt.Sprintf("Unable to initialize OpenGL: %v", err))
		}

		renderer.FontsInit(render, plat)

		newSimConnectionChan = make(chan *sim.SimConnection, 2)
		var world *World

		localServer = <-localSimServerChan

		if globalConfig.Sim != nil && !*resetSim {
			if err := globalConfig.Sim.PostLoad(mapLibrary); err != nil {
				lg.Errorf("Error in Sim PostLoad: %v", err)
			} else {
				var result sim.NewSimResult
				if err := localServer.Call("SimManager.Add", globalConfig.Sim, &result); err != nil {
					lg.Errorf("error restoring saved Sim: %v", err)
				} else {
					world = NewWorldFromSimState(*result.SimState, result.ControllerToken,
						localServer.RPCClient)
					world.ToggleShowScenarioInfoWindow()
				}
			}
		}

		wmInit()

		uiInit(render, plat, eventStream)

		globalConfig.Activate(world, render, plat, eventStream)

		if world == nil {
			uiShowConnectDialog(false, plat)
		}

		if !globalConfig.AskedDiscordOptIn {
			uiShowDiscordOptInDialog(plat)
		}
		if !globalConfig.NotifiedNewCommandSyntax {
			uiShowNewCommandSyntaxDialog(plat)
		}

		simStartTime := time.Now()

		///////////////////////////////////////////////////////////////////////////
		// Main event / rendering loop
		lg.Info("Starting main loop")

		stopConnectingRemoteServer := false
		frameIndex := 0
		stats.startTime = time.Now()
		for {
			select {
			case ns := <-newSimConnectionChan:
				if world != nil {
					world.Disconnect()
				}
				world = NewWorldFromSimState(ns.SimState, ns.SimProxy.ControllerToken,
					ns.SimProxy.Client)
				simStartTime = time.Now()

				if world == nil {
					uiShowConnectDialog(false, plat)
				} else if world != nil {
					world.ToggleShowScenarioInfoWindow()
					globalConfig.DisplayRoot.VisitPanes(func(p panes.Pane) {
						p.Reset(world.State, lg)
					})
				}

			case remoteServerConn := <-remoteSimServerChan:
				if err := remoteServerConn.Err; err != nil {
					lg.Warn("Unable to connect to remote server", slog.Any("error", err))

					if err.Error() == sim.ErrRPCVersionMismatch.Error() {
						uiShowModalDialog(NewModalDialogBox(&ErrorModalClient{
							message: "This version of vice is incompatible with the vice multi-controller server.\n" +
								"If you're using an older version of vice, please upgrade to the latest\n" +
								"version for multi-controller support. (If you're using a beta build, then\n" +
								"thanks for your help testing vice; when the beta is released, the server\n" +
								"will be updated as well.)",
						}, plat), true)

						stopConnectingRemoteServer = true
					}
					remoteServer = nil
				} else {
					remoteServer = remoteServerConn.Server
				}

			default:
			}

			if world == nil {
				plat.SetWindowTitle("vice: [disconnected]")
				SetDiscordStatus(DiscordStatus{Start: simStartTime}, lg)
			} else {
				plat.SetWindowTitle("vice: " + world.GetWindowTitle())
				// Update discord RPC
				SetDiscordStatus(DiscordStatus{
					TotalDepartures: world.TotalDepartures,
					TotalArrivals:   world.TotalArrivals,
					Callsign:        world.Callsign,
					Start:           simStartTime,
				}, lg)
			}

			if remoteServer == nil && time.Since(lastRemoteServerAttempt) > 10*time.Second && !stopConnectingRemoteServer {
				lastRemoteServerAttempt = time.Now()
				remoteSimServerChan = sim.TryConnectRemoteServer(*serverAddress, lg)
			}

			// Inform imgui about input events from the user.
			plat.ProcessEvents()

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
						eventStream.Post(sim.Event{
							Type:    sim.StatusMessageEvent,
							Message: "Error getting update from server: " + err.Error(),
						})
						if util.IsRPCServerError(err) {
							uiShowModalDialog(NewModalDialogBox(&ErrorModalClient{
								message: "Lost connection to the vice server.",
							}, plat), true)

							remoteServer = nil
							world = nil

							uiShowConnectDialog(false, plat)
						}
					})
			}

			plat.NewFrame()
			imgui.NewFrame()

			// Generate and render vice draw lists
			if world != nil {
				wmDrawPanes(plat, render, world, &stats)
			} else {
				commandBuffer := renderer.GetCommandBuffer()
				commandBuffer.ClearRGB(renderer.RGB{})
				stats.render = render.RenderCommandBuffer(commandBuffer)
				renderer.ReturnCommandBuffer(commandBuffer)
			}

			timeMarker(&stats.drawPanes)

			// Draw the user interface
			drawUI(plat, render, world, eventStream, &stats)
			timeMarker(&stats.drawImgui)

			// Wait for vsync
			plat.PostRender()

			// Periodically log current memory use, etc.
			if frameIndex%18000 == 0 {
				lg.Debug("performance", slog.Any("stats", stats))
			}
			frameIndex++

			if plat.ShouldStop() && len(ui.activeModalDialogs) == 0 {
				// Do this while we're still running the event loop.
				saveSim := world != nil && world.simProxy.Client == localServer.RPCClient
				globalConfig.SaveIfChanged(render, plat, world, saveSim)

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
