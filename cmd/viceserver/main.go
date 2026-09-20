// cmd/viceserver
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Runs the vice scenario server

package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

var (
	cpuprofile            = flag.String("cpuprofile", "", "write CPU profile to `file`")
	memprofile            = flag.String("memprofile", "", "write memory profile to `file`")
	logLevel              = flag.String("loglevel", "info", "logging `level`: debug, info, warn, error")
	logDir                = flag.String("logdir", "", "log file `directory`")
	serverPort            = flag.Int("port", server.ViceServerPort, "`port` to listen on")
	serverAddress         = flag.String("server", net.JoinHostPort(server.ViceServerAddress, strconv.Itoa(server.ViceServerPort)), "IP `address` of vice multi-controller server")
	scenarioFilename      = flag.String("scenario", "", "`filename` of JSON file with a scenario definition")
	videoMapFilename      = flag.String("videomap", "", "`filename` of JSON file with video map definitions")
	scenarioBriefFilename = flag.String("scenariobrief", "", "`filename` of markdown file with a scenario brief")
	navLogEnabled         = flag.Bool("navlog", false, "enable navigation logging")
	navLogCategories      = flag.String("navlog-categories", "all", "navigation log `categories`")
	navLogCallsign        = flag.String("navlog-callsign", "", "filter navigation logs to only show this `callsign`")
	smoketest             = flag.Duration("smoketest", 0, "load the scenarios, run a sim with an RPC client attached for this long, and exit; for CI under -race")
	wxFacilities          = flag.String("wxfacilities", "", "write the weather pipeline's airport and facility list as JSON to `file` and exit")

	facilityConfigFilenames []string
)

func init() {
	flag.Func("facilityconfig", "`filename` of JSON file with a facility configuration; may be given multiple times",
		func(s string) error {
			facilityConfigFilenames = append(facilityConfigFilenames, s)
			return nil
		})
}

// writeWXFacilities writes the list that wxingest and wxpackage use to decide
// what to ingest and package. wxingest runs from a container image that can't
// load the scenarios itself, since doing so validates their video maps against
// the .mappack files.
func writeWXFacilities(path string, lg *log.Logger) error {
	fac, err := scenario.WXFacilities(lg)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := fac.Write(f); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.Close()
}

func main() {
	flag.Parse()

	resolvedLogDir := log.DefaultLogDir(true, *logDir)
	lg := log.New(true, *logLevel, resolvedLogDir)

	if err := run(lg); err != nil {
		lg.Errorf("%v", err)
		os.Exit(1)
	}
}

func run(lg *log.Logger) error {
	profiler, err := util.CreateProfiler(*cpuprofile, *memprofile)
	if err != nil {
		return err
	}
	if profiler != nil {
		profiler.RegisterSignalCleanup()
		defer profiler.Cleanup()
	}

	if *serverAddress != "" && !strings.Contains(*serverAddress, ":") {
		*serverAddress = net.JoinHostPort(*serverAddress, strconv.Itoa(server.ViceServerPort))
	}

	// Bring the resources directory up to date before anything reads from it.
	// A canceled sync means the server didn't start, so it exits non-zero like
	// any other startup failure.
	if err := util.SyncResources(&util.TextSyncUI{}); err != nil {
		return fmt.Errorf("Unable to sync resources: %v", err)
	}

	av.InitDB()
	wx.Init()

	if *wxFacilities != "" {
		return writeWXFacilities(*wxFacilities, lg)
	}

	nav.InitNavLog(*navLogEnabled, *navLogCategories, *navLogCallsign)

	config := server.LaunchConfig{
		Port: *serverPort,
		Overrides: scenario.OverrideFiles{
			Scenario:        *scenarioFilename,
			VideoMap:        *videoMapFilename,
			ScenarioBrief:   *scenarioBriefFilename,
			FacilityConfigs: facilityConfigFilenames,
		},
		ServerAddress: *serverAddress,
		IsLocal:       false,
	}

	if *smoketest > 0 {
		return runSmoketest(config, *smoketest, lg)
	}

	server.LaunchServer(config, lg)
	return nil
}

// runSmoketest loads the scenarios, starts a sim, and polls it for state
// updates for the given duration. Under the race detector that covers
// scenario loading, the sim update loop, and the RPC replies net/rpc encodes
// after the sim lock has been released -- the last of which a load-only run
// never reaches.
func runSmoketest(config server.LaunchConfig, d time.Duration, lg *log.Logger) error {
	rpcPort, e, overrideErrors := server.LaunchServerAsync(config, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		return errors.New("errors loading scenarios")
	}
	if overrideErrors != "" {
		lg.Warnf("Override files had errors:\n%s", overrideErrors)
	}

	conn, err := net.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(rpcPort)))
	if err != nil {
		return err
	}
	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return err
	}
	client := rpc.NewClientWithCodec(util.MakeMessagepackClientCodec(cc))
	defer client.Close()

	var connect server.ConnectResult
	if err := client.Call(server.ConnectRPC, server.ViceRPCVersion, &connect); err != nil {
		return fmt.Errorf("%s: %w", server.ConnectRPC, err)
	}
	if len(connect.ScenarioCatalogs) == 0 {
		return errors.New("server offered no scenarios")
	}

	// Prefer a facility with packaged weather so the sim runs with real
	// atmospherics rather than the fallback, and take it in sorted order so
	// that a failure is reproducible rather than depending on map iteration.
	req := server.MakeNewSimRequest()
	req.Initials = "XX"
	req.StartTime = time.Now().UTC()
	for _, facility := range util.SortedMapKeys(connect.ScenarioCatalogs) {
		if wxAvail := connect.AvailableWXByFacility[facility]; len(wxAvail) > 0 {
			req.Facility, req.StartTime = facility, wxAvail[0].Start()
			break
		}
	}
	if req.Facility == "" {
		req.Facility = util.SortedMapKeys(connect.ScenarioCatalogs)[0]
	}

	var catalog *scenario.Catalog
	req.GroupName, catalog = util.FirstSortedMapEntry(connect.ScenarioCatalogs[req.Facility])
	req.ScenarioName = catalog.DefaultScenario
	req.ScenarioSpec = catalog.Scenarios[req.ScenarioName]
	if req.ScenarioSpec == nil {
		return fmt.Errorf("%s: no spec for default scenario", req.ScenarioName)
	}

	// The server rejects a traffic source it doesn't offer for this scenario.
	if src := &req.ScenarioSpec.LaunchConfig.TrafficSource; !slices.Contains(req.ScenarioSpec.TrafficSources, *src) {
		if len(req.ScenarioSpec.TrafficSources) == 0 {
			return fmt.Errorf("%s: scenario offers no traffic sources", req.ScenarioName)
		}
		*src = req.ScenarioSpec.TrafficSources[0]
	}

	var result server.NewSimResult
	if err := client.Call(server.NewSimRPC, &req, &result); err != nil {
		return fmt.Errorf("%s: %w", server.NewSimRPC, err)
	}
	lg.Infof("smoketest: running %s/%s/%s for %v", req.Facility, req.GroupName, req.ScenarioName, d)

	// GetStateUpdate blocks until the sim publishes, so this keeps a reply
	// being encoded for most of the time the update loop is running.
	for stop := time.Now().Add(d); time.Now().Before(stop); {
		var update server.SimStateUpdate
		if err := client.Call(server.GetStateUpdateRPC, result.ControllerToken, &update); err != nil {
			return fmt.Errorf("%s: %w", server.GetStateUpdateRPC, err)
		}
	}
	return nil
}
