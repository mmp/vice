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
// 42: server.NewSimRequest.StartTime
// 43: WX rework (scrape, etc.)
// 44: store pane instances and split positions separately, rather than the entire DisplayNode hierarchy
// 45: Change STARSFacilityAdaptation to FacilityAdaptation
// 46: Remove token from video map fetch RPC, misc wx updates
// 47: pass PrimaryAirport to GetAtmosGrid RPC
// 48: release bump
// 49: STARS consolidation
// 50: rework State/StateUpdate management
// 51: preemptive before v0.13.3 release
// 52: STT
// 53: local STT
// 54: Move ControllerFrequency from NASFlightPlan to Aircraft
// 55: STT logging
// 56: STT iteration
// 57: rework contact radio transmission management
// 58: STT fin rev?
// 59: server-side flightstrip management
const ViceSerializeVersion = 59

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8000 - 50 + ViceRPCVersion
const ViceRPCVersion = ViceSerializeVersion
const ViceHTTPServerPort = 6502

type ServerLaunchConfig struct {
	Port          int // if 0, finds an open one
	ExtraScenario string
	ExtraVideoMap string
	ServerAddress string // address to use for remote TTS provider
	IsLocal       bool
}

func LaunchServer(config ServerLaunchConfig, lg *log.Logger) {
	util.InitFlightRecorder(lg)
	util.MonitorCPUUsage(95, false /* don't panic if wedged */, lg)
	util.MonitorMemoryUsage(512 /* trigger MB */, 64 /* delta MB */, lg)

	_, server, e, extraScenarioErrors := makeServer(config, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}
	if extraScenarioErrors != "" {
		lg.Warnf("Extra scenario file had errors:\n%s", extraScenarioErrors)
	}
	server()
}

func LaunchServerAsync(config ServerLaunchConfig, lg *log.Logger) (int, util.ErrorLogger, string) {
	rpcPort, server, e, extraScenarioErrors := makeServer(config, lg)
	if e.HaveErrors() {
		return 0, e, ""
	}

	go server()

	return rpcPort, e, extraScenarioErrors
}

func makeServer(config ServerLaunchConfig, lg *log.Logger) (int, func(), util.ErrorLogger, string) {
	var listener net.Listener
	var err error
	var errorLogger util.ErrorLogger
	var rpcPort int
	if config.Port == 0 {
		if listener, err = net.Listen("tcp", ":0"); err != nil {
			errorLogger.Error(err)
			return 0, nil, errorLogger, ""
		}
		rpcPort = listener.Addr().(*net.TCPAddr).Port
	} else if listener, err = net.Listen("tcp", ":"+strconv.Itoa(config.Port)); err == nil {
		rpcPort = config.Port
	} else {
		errorLogger.Error(err)
		return 0, nil, errorLogger, ""
	}

	scenarioGroups, scenarioCatalogs, mapManifests, extraScenarioErrors :=
		LoadScenarioGroups(config.ExtraScenario, config.ExtraVideoMap, false /* skipVideoMaps */, &errorLogger, lg)
	if errorLogger.HaveErrors() {
		return 0, nil, errorLogger, ""
	}

	serverFunc := func() {
		server := rpc.NewServer()

		sm := NewSimManager(scenarioGroups, scenarioCatalogs, mapManifests, config.ServerAddress, config.IsLocal, lg)
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

	return rpcPort, serverFunc, errorLogger, extraScenarioErrors
}
