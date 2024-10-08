// pkg/sim/server.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	gomath "math"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/util"
	"github.com/shirou/gopsutil/cpu"
)

const ViceServerAddress = "vice.pharr.org"
const ViceServerPort = 8000 + ViceRPCVersion
const ViceRPCVersion = 19

type Server struct {
	*util.RPCClient
	name        string
	configs     map[string]map[string]*Configuration
	runningSims map[string]*RemoteSim
}

type serverConnection struct {
	Server *Server
	Err    error
}

func (s *Server) Close() error {
	return s.RPCClient.Close()
}

func RunServer(extraScenario string, extraVideoMap string, serverPort int, lg *log.Logger) {
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

func getClient(hostname string, lg *log.Logger) (*util.RPCClient, error) {
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
	return &util.RPCClient{rpc.NewClientWithCodec(codec)}, nil
}

func TryConnectRemoteServer(hostname string, lg *log.Logger) chan *serverConnection {
	ch := make(chan *serverConnection, 1)
	go func() {
		if client, err := getClient(hostname, lg); err != nil {
			ch <- &serverConnection{Err: err}
			return
		} else {
			var so SignOnResult
			start := time.Now()
			if err := client.CallWithTimeout("SimManager.SignOn", ViceRPCVersion, &so); err != nil {
				ch <- &serverConnection{Err: err}
			} else {
				lg.Debugf("%s: server returned configuration in %s", hostname, time.Since(start))
				ch <- &serverConnection{
					Server: &Server{
						RPCClient:   client,
						name:        "Network (Multi-controller)",
						configs:     so.Configurations,
						runningSims: so.RunningSims,
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

		go launchHTTPStats(sm)

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

func launchHTTPStats(sm *SimManager) {
	launchTime = time.Now()
	http.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		statsHandler(w, r, sm)
		sm.lg.Infof("%s: served stats request", r.URL.String())
	})
	http.HandleFunc("/vice-logs/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if f, err := os.Open("." + r.URL.String()); err == nil {
			if n, err := io.Copy(w, f); err != nil {
				sm.lg.Errorf("%s: %v", r.URL.String(), err)
			} else {
				sm.lg.Infof("%s: served %d bytes", r.URL.String(), n)
			}
		}
	})

	if err := http.ListenAndServe(":6502", nil); err != nil {
		sm.lg.Errorf("Failed to start HTTP server for stats: %v\n", err)
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
	Errors    string
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
  <th>Dep</th>
  <th>Arr</th>
  <th>Idle Time</th>
  <th>Active Controllers</th>

{{range .SimStatus}}
  </tr>
  <td>{{.Name}}</td>
  <td>{{.Config}}</td>
  <td>{{.TotalDepartures}}</td>
  <td>{{.TotalArrivals}}</td>
  <td>{{.IdleTime}}</td>
  <td><tt>{{.Controllers}}</tt></td>
</tr>
{{end}}
</table>

<h1>Errors</h1>
<div id="log" class="bot">
{{.Errors}}
</div>

<script>
window.onload = function() {
    var divs = document.getElementsByClassName("bot");
    for (var i = 0; i < divs.length; i++) {
        divs[i].scrollTop = divs[i].scrollHeight - divs[i].clientHeight;
    }
}
</script>

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

	// process logs
	cmd := exec.Command("jq", `select(.level == "WARN" or .level == "ERROR")|.callstack = .callstack[0]`,
		sm.lg.LogFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stats.Errors = "jq: " + err.Error() + "\n" + stderr.String()
	} else {
		stats.Errors = stdout.String()
	}

	stats.RX, stats.TX = util.GetLoggedRPCBandwidth()

	statsTemplate.Execute(w, stats)
}
