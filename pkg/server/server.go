// pkg/server/server.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"html/template"
	gomath "math"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"runtime"
	"strconv"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"

	"github.com/shirou/gopsutil/cpu"
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
const ViceSerializeVersion = 39

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8000 + ViceRPCVersion
const ViceRPCVersion = ViceSerializeVersion
const ViceHTTPServerPort = 6502

type Server struct {
	*RPCClient
	name        string
	configs     map[string]map[string]*Configuration
	runningSims map[string]*RemoteSim
}

type NewSimConfiguration struct {
	// FIXME: unify Password/RemoteSimPassword, SelectedRemoteSim / NewSimName, etc.
	NewSimType   int32
	NewSimName   string
	GroupName    string
	ScenarioName string

	SelectedRemoteSim         string
	SelectedRemoteSimPosition string

	Scenario *SimScenarioConfiguration

	TFRs []av.TFR

	TRACONName        string
	RequirePassword   bool
	Password          string // for create remote only
	RemoteSimPassword string // for join remote only

	LiveWeather bool

	InstructorAllowed bool
	Instructor        bool
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

func MakeNewSimConfiguration() NewSimConfiguration {
	return NewSimConfiguration{NewSimName: rand.Make().AdjectiveNoun()}
}

type RemoteSim struct {
	GroupName          string
	ScenarioName       string
	PrimaryController  string
	RequirePassword    bool
	InstructorAllowed  bool
	AvailablePositions map[string]av.Controller
	CoveredPositions   map[string]av.Controller
}

type serverConnection struct {
	Server *Server
	Err    error
}

func (s *Server) Close() error {
	return s.RPCClient.Close()
}

func (s *Server) GetConfigs() map[string]map[string]*Configuration {
	return s.configs
}

func (s *Server) setRunningSims(rs map[string]*RemoteSim) {
	s.runningSims = rs
}

func (s *Server) GetRunningSims() map[string]*RemoteSim {
	return s.runningSims
}

func RunServer(extraScenario string, extraVideoMap string, serverPort int, lg *log.Logger) {
	if err := speech.InitTTS(); err != nil {
		lg.Errorf("TTS: %v", err)
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", serverPort))
	if err != nil {
		lg.Errorf("tcp listen: %v", err)
		return
	}

	// If we're just running the server, we don't care about the returned
	// configs...
	var e util.ErrorLogger
	if runServer(l, false, extraScenario, extraVideoMap, &e, lg) == nil && e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}
}

func getClient(hostname string, lg *log.Logger) (*RPCClient, error) {
	conn, err := net.Dial("tcp", hostname)
	if err != nil {
		return nil, err
	}

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeGOBClientCodec(cc)
	codec = util.MakeLoggingClientCodec(hostname, codec, lg)
	return &RPCClient{rpc.NewClientWithCodec(codec)}, nil
}

func TryConnectRemoteServer(hostname string, lg *log.Logger) chan *serverConnection {
	ch := make(chan *serverConnection, 1)
	go func() {
		if client, err := getClient(hostname, lg); err != nil {
			ch <- &serverConnection{Err: err}
			return
		} else {
			var cr ConnectResult
			start := time.Now()
			if err := client.CallWithTimeout("SimManager.Connect", ViceRPCVersion, &cr); err != nil {
				ch <- &serverConnection{Err: err}
			} else {
				lg.Debugf("%s: server returned configuration in %s", hostname, time.Since(start))
				ch <- &serverConnection{
					Server: &Server{
						RPCClient:   client,
						name:        "Network (Multi-controller)",
						configs:     cr.Configurations,
						runningSims: cr.RunningSims,
					},
				}
			}
		}
	}()

	return ch
}

func LaunchLocalServer(extraScenario string, extraVideoMap string, e *util.ErrorLogger, lg *log.Logger) (chan *Server, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return nil, err
	}

	port := l.Addr().(*net.TCPAddr).Port

	configsChan := runServer(l, true, extraScenario, extraVideoMap, e, lg)
	if e.HaveErrors() {
		return nil, nil
	}

	ch := make(chan *Server, 1)
	go func() {
		configs := <-configsChan

		client, err := getClient(fmt.Sprintf("localhost:%d", port), lg)
		if err != nil {
			lg.Errorf("unable to get client: %v", err)
			os.Exit(1)
		}

		ch <- &Server{
			RPCClient: client,
			name:      "Local (Single controller)",
			configs:   configs,
		}
	}()

	return ch, nil
}

func runServer(l net.Listener, isLocal bool, extraScenario string, extraVideoMap string,
	e *util.ErrorLogger, lg *log.Logger) chan map[string]map[string]*Configuration {
	scenarioGroups, simConfigurations, mapManifests :=
		LoadScenarioGroups(isLocal, extraScenario, extraVideoMap, e, lg)
	if e.HaveErrors() {
		return nil
	}

	ch := make(chan map[string]map[string]*Configuration, 1)

	server := func() {
		server := rpc.NewServer()

		sm := NewSimManager(scenarioGroups, simConfigurations, mapManifests, lg)
		if err := server.Register(sm); err != nil {
			lg.Errorf("unable to register SimManager: %v", err)
			os.Exit(1)
		}
		if err := server.RegisterName("Sim", &Dispatcher{sm: sm}); err != nil {
			lg.Errorf("unable to register dispatcher: %v", err)
			os.Exit(1)
		}

		go launchHTTPServer(sm)

		ch <- simConfigurations

		lg.Infof("Listening on %+v", l)

		for {
			conn, err := l.Accept()
			lg.Infof("%s: new connection", conn.RemoteAddr())
			if err != nil {
				lg.Errorf("Accept error: %v", err)
			} else if cc, err := util.MakeCompressedConn(util.MakeLoggingConn(conn, lg)); err != nil {
				lg.Errorf("MakeCompressedConn: %v", err)
			} else {
				codec := util.MakeGOBServerCodec(cc, lg)
				codec = util.MakeLoggingServerCodec(conn.RemoteAddr().String(), codec, lg)
				go server.ServeCodec(codec)
			}
		}
	}

	if isLocal {
		go server()
	} else {
		server()
	}
	return ch
}

///////////////////////////////////////////////////////////////////////////
// Status / statistics via HTTP...

var launchTime time.Time

func launchHTTPServer(sm *SimManager) {
	launchTime = time.Now()
	http.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		statsHandler(w, r, sm)
		sm.lg.Infof("%s: served stats request", r.URL.String())
	})
	http.HandleFunc("/speech", sm.HandleSpeechWSConnection)

	if err := http.ListenAndServe(":"+strconv.Itoa(ViceHTTPServerPort), nil); err != nil {
		sm.lg.Warnf("Unable to start HTTP stats server")
	}
}

type serverStats struct {
	Uptime           time.Duration
	AllocMemory      uint64
	TotalAllocMemory uint64
	SysMemory        uint64
	RX, TX           int64
	NumGC            uint32
	NumGoRoutines    int
	CPUUsage         int

	SimStatus []simStatus
}

func formatBytes(v int64) string {
	if v < 1024 {
		return fmt.Sprintf("%d B", v)
	} else if v < 1024*1024 {
		return fmt.Sprintf("%d KiB", v/1024)
	} else if v < 1024*1024*1024 {
		return fmt.Sprintf("%d MiB", v/1024/1024)
	} else {
		return fmt.Sprintf("%d GiB", v/1024/1024/1024)
	}
}

var templateFuncs = template.FuncMap{"bytes": formatBytes}

var statsTemplate = template.Must(template.New("").Funcs(templateFuncs).Parse(`
<!DOCTYPE html>
<html>
<head>
<title>vice vice baby</title>
</head>
<style>
table {
  border-collapse: collapse;
  width: 100%;
}

th, td {
  border: 1px solid #dddddd;
  padding: 8px;
  text-align: left;
}

tr:nth-child(even) {
  background-color: #f2f2f2;
}

#log {
    font-family: "Courier New", monospace;  /* use a monospace font */
    width: 100%;
    height: 500px;
    font-size: 12px;
    overflow: auto;  /* add scrollbars as necessary */
    white-space: pre-wrap;  /* wrap text */
    border: 1px solid #ccc;
    padding: 10px;
}
</style>
<body>
<h1>Server Status</h1>
<ul>
  <li>Uptime: {{.Uptime}}</li>
  <li>CPU usage: {{.CPUUsage}}%</li>
  <li>Bandwidth: {{bytes .RX}} RX, {{bytes .TX}} TX</li>
  <li>Allocated memory: {{.AllocMemory}} MB</li>
  <li>Total allocated memory: {{.TotalAllocMemory}} MB</li>
  <li>System memory: {{.SysMemory}} MB</li>
  <li>Garbage collection passes: {{.NumGC}}</li>
  <li>Running goroutines: {{.NumGoRoutines}}</li>
</ul>

<h1>Sim Status</h1>
<table>
  <tr>
  <th>Name</th>
  <th>Scenario</th>
  <th>IFR</th>
  <th>VFR</th>
  <th>Idle Time</th>
  <th>Active Controllers</th>

{{range .SimStatus}}
  </tr>
  <td>{{.Name}}</td>
  <td>{{.Config}}</td>
  <td>{{.TotalIFR}}</td>
  <td>{{.TotalVFR}}</td>
  <td>{{.IdleTime}}</td>
  <td><tt>{{.Controllers}}</tt></td>
</tr>
{{end}}
</table>

</body>
</html>
`))

func statsHandler(w http.ResponseWriter, r *http.Request, sm *SimManager) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	usage, _ := cpu.Percent(time.Second, false)
	stats := serverStats{
		Uptime:           time.Since(launchTime).Round(time.Second),
		AllocMemory:      m.Alloc / (1024 * 1024),
		TotalAllocMemory: m.TotalAlloc / (1024 * 1024),
		SysMemory:        m.Sys / (1024 * 1024),
		NumGC:            m.NumGC,
		NumGoRoutines:    runtime.NumGoroutine(),
		CPUUsage:         int(gomath.Round(usage[0])),

		SimStatus: sm.getSimStatus(),
	}

	stats.RX, stats.TX = util.GetLoggedRPCBandwidth()

	statsTemplate.Execute(w, stats)
}
