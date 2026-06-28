// cmd/viceserver
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Runs the vice scenario server

package main

import (
	"flag"
	"net"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/wx"
)

var (
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
	loadOnly              = flag.Bool("loadonly", false, "exit as soon as scenarios have loaded; useful for CI smoketests under -race")
)

func main() {
	flag.Parse()

	resolvedLogDir := log.DefaultLogDir(true, *logDir)
	lg := log.New(true, *logLevel, resolvedLogDir)

	if *serverAddress != "" && !strings.Contains(*serverAddress, ":") {
		*serverAddress = net.JoinHostPort(*serverAddress, strconv.Itoa(server.ViceServerPort))
	}

	av.InitDB()
	wx.Init()

	nav.InitNavLog(*navLogEnabled, *navLogCategories, *navLogCallsign)

	server.LaunchServer(server.ServerLaunchConfig{
		Port:               *serverPort,
		ExtraScenario:      *scenarioFilename,
		ExtraVideoMap:      *videoMapFilename,
		ExtraScenarioBrief: *scenarioBriefFilename,
		ServerAddress:      *serverAddress,
		IsLocal:            false,
		ExitAfterLoad:      *loadOnly,
	}, lg)
}
