// server/http.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/shirou/gopsutil/cpu"
)

type ttsClientStats struct {
	IP       string
	Calls    int
	Words    int
	LastUsed time.Time
}

type serverStats struct {
	Uptime           time.Duration
	AllocMemory      uint64
	TotalAllocMemory uint64
	SysMemory        uint64
	RX, TX           int64
	TXWebsocket      int64
	NumGC            uint32
	NumGoRoutines    int
	CPUUsage         int

	SimStatus []simStatus
	TTSStats  []ttsClientStats
}

///////////////////////////////////////////////////////////////////////////
// TTS usage tracking

func (sm *SimManager) UpdateTTSUsage(ip, text string) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	if _, ok := sm.ttsUsageByIP[ip]; !ok {
		sm.ttsUsageByIP[ip] = &ttsUsageStats{}
	}

	stats := sm.ttsUsageByIP[ip]
	stats.Calls++
	stats.Words += len(strings.Fields(text))
	stats.LastUsed = time.Now()

	if stats.Words > 30000 {
		return fmt.Errorf("TTS capacity exceeded")
	}

	return nil
}

func (sm *SimManager) GetTTSStats() []ttsClientStats {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var stats []ttsClientStats
	for ip, usage := range sm.ttsUsageByIP {
		stats = append(stats, ttsClientStats{
			IP:       ip,
			Calls:    usage.Calls,
			Words:    usage.Words,
			LastUsed: usage.LastUsed,
		})
	}

	// Sort high to low on word count
	slices.SortFunc(stats, func(a, b ttsClientStats) int { return b.Words - a.Words })

	return stats
}

///////////////////////////////////////////////////////////////////////////
// STT logging

// STTLogEntry represents logged data from an STT transcription.
type STTLogEntry struct {
	Transcript      string        `json:"transcript"`
	WhisperDuration time.Duration `json:"whisper_duration"`
	Callsign        string        `json:"callsign"`
	Command         string        `json:"command"`
	STTDuration     time.Duration `json:"stt_duration"`
}

func (sm *SimManager) handleSTTLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var entry STTLogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	sm.lg.Infof("STT log: transcript=%q whisper=%s callsign=%s command=%q stt=%s",
		entry.Transcript, entry.WhisperDuration, entry.Callsign, entry.Command, entry.STTDuration)

	w.WriteHeader(http.StatusOK)
}

///////////////////////////////////////////////////////////////////////////
// Status / statistics via HTTP...

func (sm *SimManager) launchHTTPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		sm.statsHandler(w, r)
		sm.lg.Infof("%s: served stats request", r.URL.String())
	})

	mux.HandleFunc("/speech", sm.HandleSpeechWSConnection)

	mux.HandleFunc("/stt-log", sm.handleSTTLog)

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	var listener net.Listener
	var err error
	var port int
	for i := range 10 {
		port = ViceHTTPServerPort + i
		if listener, err = net.Listen("tcp", ":"+strconv.Itoa(port)); err == nil {
			sm.httpPort = port
			fmt.Printf("Launching HTTP server on port %d\n", port)
			break
		}
	}

	if err != nil {
		sm.lg.Warnf("Unable to start HTTP server")
	} else {
		go func() {
			if err := http.Serve(listener, mux); err != nil {
				sm.lg.Errorf("HTTP server error: %v", err)
			}
		}()
	}
}

type simStatus struct {
	Name               string
	Config             string
	IdleTime           time.Duration
	ActiveTCWs         string
	TotalIFR, TotalVFR int
}

func (ss simStatus) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", ss.Name),
		slog.String("config", ss.Config),
		slog.Duration("idle", ss.IdleTime),
		slog.String("active_tcws", ss.ActiveTCWs),
		slog.Int("total_ifr", ss.TotalIFR),
		slog.Int("total_vfr", ss.TotalVFR))
}

func (sm *SimManager) GetSimStatus() []simStatus {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var status []simStatus
	for name, ss := range util.SortedMap(sm.sessionsByName) {
		ifr, vfr := ss.sim.GetTrafficCounts()
		activeTCWs := util.MapSlice(ss.GetActiveTCWs(), func(tcw sim.TCW) string { return string(tcw) })
		status = append(status, simStatus{
			Name:       name,
			Config:     ss.scenario,
			IdleTime:   ss.sim.IdleTime().Round(time.Second),
			TotalIFR:   ifr,
			TotalVFR:   vfr,
			ActiveTCWs: strings.Join(activeTCWs, ", "),
		})
	}

	return status
}

var templateFuncs = template.FuncMap{"bytes": func(v int64) string { return util.ByteCount(v).String() }}

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
  <li>Bandwidth: {{bytes .RX}} RX, {{bytes .TX}} TX, {{bytes .TXWebsocket}} TX Websocket</li>
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
  <th>Active TCWs</th>

{{range .SimStatus}}
  </tr>
  <td>{{.Name}}</td>
  <td>{{.Config}}</td>
  <td>{{.TotalIFR}}</td>
  <td>{{.TotalVFR}}</td>
  <td>{{.IdleTime}}</td>
  <td><tt>{{.ActiveTCWs}}</tt></td>
</tr>
{{end}}
</table>

<h1>Text-to-Speech Usage</h1>
{{if .TTSStats}}
<table>
  <tr>
  <th>Client IP</th>
  <th>Call Count</th>
  <th>Word Count</th>
  <th>Last Used</th>
  </tr>
{{range .TTSStats}}
  <tr>
  <td>{{.IP}}</td>
  <td>{{.Calls}}</td>
  <td>{{.Words}}</td>
  <td>{{.LastUsed.Format "2006-01-02 15:04:05"}}</td>
  </tr>
{{end}}
</table>
{{else}}
<p>No TTS usage recorded.</p>
{{end}}

</body>
</html>
`))

func (sm *SimManager) statsHandler(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	usage, _ := cpu.Percent(time.Second, false)
	stats := serverStats{
		Uptime:           time.Since(sm.startTime).Round(time.Second),
		AllocMemory:      m.Alloc / (1024 * 1024),
		TotalAllocMemory: m.TotalAlloc / (1024 * 1024),
		SysMemory:        m.Sys / (1024 * 1024),
		NumGC:            m.NumGC,
		NumGoRoutines:    runtime.NumGoroutine(),
		CPUUsage:         int(math.Round(float32(usage[0]))),
		TXWebsocket:      sm.websocketTXBytes.Load(),

		SimStatus: sm.GetSimStatus(),
		TTSStats:  sm.GetTTSStats(),
	}

	stats.RX, stats.TX = util.GetLoggedRPCBandwidth()

	statsTemplate.Execute(w, stats)
}
