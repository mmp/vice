// pkg/server/server.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"net"
	"net/rpc"
	"os"
	"strconv"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
)

// Version history 0-7 not explicitly recorded
// 8: STARSPane DCB improvements, added DCB font size control
// 9: correct STARSColors, so update brightness settings to compensate
// 10: stop being clever about JSON encoding Waypoint arrays to strings
// 11: expedite, intercept localizer, fix airspace serialization
// 12: set 0 DCB brightness to 50 (WAR not setting a default for it)
// 13: update departure handling for multi-controllers (and rename some members)
// 14: Aircraft ArrivalHandoffController -> WaypointHandoffController
// 15: audio engine rewrite
// 16: cleared/assigned alt for departures, minor nav changes
// 17: weather intensity default bool
// 18: STARS ATPA
// 19: runway waypoints now per-airport
// 20: "stars_config" and various scenario fields moved there, plus STARSFacilityAdaptation
// 21: STARS DCB drawing changes, so system list positions changed
// 22: draw points using triangles, remove some CommandBuffer commands
// 23: video map format update
// 24: packages, audio to platform, flight plan processing
// 25: remove ArrivalGroup/Index from Aircraft
// 26: make allow_long_scratchpad a single bool
// 27: rework prefs, videomaps
// 28: new departure flow
// 29: TFR cache
// 30: video map improvements
// 31: audio squelch for pilot readback
// 32: VFRs, custom spcs, pilot reported altitude, ...
// 33: VFRs v2
// 34: sim/server refactor, signon flow
// 35: VFRRunways in sim.State, METAR Wind struct changes
// 36: STARS center representation changes
// 37: rework STARS flight plan (et al)
// 38: rework STARS flight plan (et al) ongoing
// 39: speech v0.1
// 40: clean up what's transmitted server->client at initial connect/spawn, gob->msgpack
// 41: sim.State.SimStartTime
const ViceSerializeVersion = 41

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8000 + ViceRPCVersion
const ViceRPCVersion = ViceSerializeVersion
const ViceHTTPServerPort = 6502

type ServerLaunchConfig struct {
	Port                int // if 0, finds an open one
	MultiControllerOnly bool
	ExtraScenario       string
	ExtraVideoMap       string
}

func LaunchServer(config ServerLaunchConfig, lg *log.Logger) {
	util.MonitorCPUUsage(95, true /* panic if wedged */, lg)
	util.MonitorMemoryUsage(128 /* trigger MB */, 64 /* delta MB */, lg)

	_, server, e := makeServer(config, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}
	server()
}

func LaunchServerAsync(config ServerLaunchConfig, lg *log.Logger) (int, util.ErrorLogger) {
	rpcPort, server, e := makeServer(config, lg)
	if e.HaveErrors() {
		return 0, e
	}

	go server()

	return rpcPort, e
}

func makeServer(config ServerLaunchConfig, lg *log.Logger) (int, func(), util.ErrorLogger) {
	var listener net.Listener
	var err error
	var errorLogger util.ErrorLogger
	var rpcPort int
	if config.Port == 0 {
		if listener, err = net.Listen("tcp", ":0"); err != nil {
			errorLogger.Error(err)
			return 0, nil, errorLogger
		}
		rpcPort = listener.Addr().(*net.TCPAddr).Port
	} else if listener, err = net.Listen("tcp", ":"+strconv.Itoa(config.Port)); err == nil {
		rpcPort = config.Port
	} else {
		errorLogger.Error(err)
		return 0, nil, errorLogger
	}

	scenarioGroups, simConfigurations, mapManifests :=
		LoadScenarioGroups(config.MultiControllerOnly, config.ExtraScenario, config.ExtraVideoMap, &errorLogger, lg)
	if errorLogger.HaveErrors() {
		return 0, nil, errorLogger
	}

	serverFunc := func() {
		server := rpc.NewServer()

		sm := NewSimManager(scenarioGroups, simConfigurations, mapManifests, lg)
		if err := server.Register(sm); err != nil {
			lg.Errorf("unable to register SimManager: %v", err)
			os.Exit(1)
		}
		if err := server.RegisterName("Sim", &dispatcher{sm: sm}); err != nil {
			lg.Errorf("unable to register dispatcher: %v", err)
			os.Exit(1)
		}

		lg.Infof("Listening on %+v", listener)

		for {
			conn, err := listener.Accept()
			lg.Infof("%s: new connection", conn.RemoteAddr())
			if err != nil {
				lg.Errorf("Accept error: %v", err)
			} else if cc, err := util.MakeCompressedConn(util.MakeLoggingConn(conn, lg)); err != nil {
				lg.Errorf("MakeCompressedConn: %v", err)
			} else {
				codec := util.MakeMessagepackServerCodec(cc, lg)
				codec = util.MakeLoggingServerCodec(conn.RemoteAddr().String(), codec, lg)
				go server.ServeCodec(codec)
			}
		}
	}

	return rpcPort, serverFunc, errorLogger
}
