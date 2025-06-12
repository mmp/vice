// pkg/server/manager.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	crand "crypto/rand"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"log/slog"
	gomath "math"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"text/template"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"
	"github.com/shirou/gopsutil/cpu"

	"github.com/brunoga/deep"
	"github.com/gorilla/websocket"
)

///////////////////////////////////////////////////////////////////////////
// SimManager

type SimManager struct {
	scenarioGroups     map[string]map[string]*scenarioGroup
	configs            map[string]map[string]*Configuration
	activeSims         map[string]*activeSim
	controllersByToken map[string]*humanController
	mu                 util.LoggingMutex
	mapManifests       map[string]*sim.VideoMapManifest
	startTime          time.Time
	httpPort           int
	websocketTXBytes   int64
	haveTTS            bool
	lg                 *log.Logger
}

type Configuration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*av.Controller
	DefaultScenario  string
}

type humanController struct {
	asim                *activeSim
	tcp                 string
	token               string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
	speechWs            *websocket.Conn
}

type SimScenarioConfiguration struct {
	SelectedController  string
	SelectedSplit       string
	SplitConfigurations av.SplitConfigurationSet
	PrimaryAirport      string

	Wind         av.Wind
	LaunchConfig sim.LaunchConfig

	DepartureRunways []sim.DepartureRunway
	ArrivalRunways   []sim.ArrivalRunway
}

type activeSim struct {
	name          string
	scenarioGroup string
	scenario      string
	sim           *sim.Sim
	password      string
	local         bool

	controllersByTCP map[string]*humanController
}

type NewSimConfiguration struct {
	// FIXME: unify Password/RemoteSimPassword, SelectedRemoteSim / NewSimName, etc.
	NewSimType   int32
	NewSimName   string
	GroupName    string
	ScenarioName string

	SelectedRemoteSim string
	SignOnPosition    string

	Scenario *SimScenarioConfiguration

	TFRs []av.TFR

	TRACONName        string
	RequirePassword   bool
	Password          string // for create remote only
	RemoteSimPassword string // for join remote only

	LiveWeather bool

	AllowInstructorRPO bool
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
	AvailablePositions map[string]av.Controller
	CoveredPositions   map[string]av.Controller
}

func (as *activeSim) AddHumanController(tcp, token string) *humanController {
	hc := &humanController{
		asim:           as,
		tcp:            tcp,
		lastUpdateCall: time.Now(),
		token:          token,
	}

	as.controllersByTCP[tcp] = hc

	return hc
}

func NewSimManager(scenarioGroups map[string]map[string]*scenarioGroup,
	simConfigurations map[string]map[string]*Configuration, manifests map[string]*sim.VideoMapManifest,
	lg *log.Logger) *SimManager {
	sm := &SimManager{
		scenarioGroups:     scenarioGroups,
		configs:            simConfigurations,
		activeSims:         make(map[string]*activeSim),
		controllersByToken: make(map[string]*humanController),
		mapManifests:       manifests,
		startTime:          time.Now(),
		lg:                 lg,
	}

	if err := speech.InitTTS(); err != nil {
		lg.Warnf("TTS: %v", err)
	} else {
		sm.haveTTS = true
	}

	sm.launchHTTPServer()

	return sm
}

type NewSimResult struct {
	SimState        *sim.State
	ControllerToken string
	SpeechWSPort    int
}

func (sm *SimManager) New(config *NewSimConfiguration, result *NewSimResult) error {
	if config.NewSimType == NewSimCreateLocal || config.NewSimType == NewSimCreateRemote {
		lg := sm.lg.With(slog.String("sim_name", config.NewSimName))
		if nsc := sm.makeSimConfiguration(config, lg); nsc != nil {
			manifest := sm.mapManifests[nsc.STARSFacilityAdaptation.VideoMapFile]
			sim := sim.NewSim(*nsc, manifest, lg)
			as := &activeSim{
				name:             config.NewSimName,
				scenarioGroup:    config.GroupName,
				scenario:         config.ScenarioName,
				sim:              sim,
				password:         config.Password,
				local:            config.NewSimType == NewSimCreateLocal,
				controllersByTCP: make(map[string]*humanController),
			}
			pos := config.SignOnPosition
			if pos == "" {
				pos = sim.State.PrimaryController
			}
			return sm.Add(as, result, pos, true)
		} else {
			return ErrInvalidSSimConfiguration
		}
	} else {
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)

		as, ok := sm.activeSims[config.SelectedRemoteSim]
		if !ok {
			return ErrNoNamedSim
		}

		if as.password != "" && config.RemoteSimPassword != as.password {
			return ErrInvalidPassword
		}

		ss, token, err := sm.signOn(as, config.SignOnPosition)
		if err != nil {
			return err
		}

		hc := as.AddHumanController(config.SignOnPosition, token)
		sm.controllersByToken[token] = hc

		*result = NewSimResult{
			SimState:        ss,
			ControllerToken: token,
			SpeechWSPort:    util.Select(sm.haveTTS, sm.httpPort, 0),
		}
		return nil
	}
}

func (sm *SimManager) makeSimConfiguration(config *NewSimConfiguration, lg *log.Logger) *sim.NewSimConfiguration {
	tracon, ok := sm.scenarioGroups[config.TRACONName]
	if !ok {
		lg.Errorf("%s: unknown TRACON", config.TRACONName)
		return nil
	}
	sg, ok := tracon[config.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", config.GroupName)
		return nil
	}
	sc, ok := sg.Scenarios[config.ScenarioName]
	if !ok {
		lg.Errorf("%s: unknown scenario", config.ScenarioName)
		return nil
	}

	description := util.Select(config.NewSimType == NewSimCreateLocal, " "+config.ScenarioName,
		"@"+config.NewSimName+": "+config.ScenarioName)

	nsc := sim.NewSimConfiguration{
		TFRs:                    config.TFRs,
		LiveWeather:             config.LiveWeather,
		TRACON:                  config.TRACONName,
		LaunchConfig:            config.Scenario.LaunchConfig,
		STARSFacilityAdaptation: deep.MustCopy(sg.STARSFacilityAdaptation),
		IsLocal:                 config.NewSimType == NewSimCreateLocal,
		DepartureRunways:        sc.DepartureRunways,
		ArrivalRunways:          sc.ArrivalRunways,
		VFRReportingPoints:      sg.VFRReportingPoints,
		ReportingPoints:         sg.ReportingPoints,
		Description:             description,
		MagneticVariation:       sg.MagneticVariation,
		NmPerLongitude:          sg.NmPerLongitude,
		Wind:                    sc.Wind,
		Airports:                sg.Airports,
		Fixes:                   sg.Fixes,
		PrimaryAirport:          sg.PrimaryAirport,
		Center:                  util.Select(sc.Center.IsZero(), sg.STARSFacilityAdaptation.Center, sc.Center),
		Range:                   util.Select(sc.Range == 0, sg.STARSFacilityAdaptation.Range, sc.Range),
		DefaultMaps:             sc.DefaultMaps,
		InboundFlows:            sg.InboundFlows,
		Airspace:                sg.Airspace,
		ControllerAirspace:      sc.Airspace,
		ControlPositions:        sg.ControlPositions,
		VirtualControllers:      sc.VirtualControllers,
		SignOnPositions:         make(map[string]*av.Controller),
	}

	if !nsc.IsLocal {
		selectedSplit := config.Scenario.SelectedSplit
		var err error
		nsc.PrimaryController, err = sc.SplitConfigurations.GetPrimaryController(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get primary controller: %v", err)
		}
		nsc.MultiControllers, err = sc.SplitConfigurations.GetConfiguration(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get multi controllers: %v", err)
		}
	} else {
		nsc.PrimaryController = sc.SoloController
	}

	add := func(callsign string) {
		if ctrl, ok := sg.ControlPositions[callsign]; !ok {
			lg.Errorf("%s: control position unknown??!", callsign)
		} else {
			nsc.SignOnPositions[callsign] = ctrl
		}
	}
	if !nsc.IsLocal {
		configs, err := sc.SplitConfigurations.GetConfiguration(config.Scenario.SelectedSplit)
		if err != nil {
			lg.Errorf("unable to get configurations for split: %v", err)
		}
		for callsign := range configs {
			add(callsign)
		}
	} else {
		add(sc.SoloController)
	}
	if config.AllowInstructorRPO {
		nsc.SignOnPositions["INS"] = &av.Controller{
			Position:   "Instructor",
			Instructor: true,
		}
		nsc.SignOnPositions["RPO"] = &av.Controller{
			Position: "Remote Pilot Operator",
			RPO:      true,
		}
	}

	return &nsc
}

func (sm *SimManager) AddLocal(sim *sim.Sim, result *NewSimResult) error {
	as := &activeSim{ // no password, etc.
		sim:              sim,
		controllersByTCP: make(map[string]*humanController),
		local:            true,
	}
	return sm.Add(as, result, sim.State.PrimaryController, false)
}

func (sm *SimManager) Add(as *activeSim, result *NewSimResult, initialTCP string, prespawn bool) error {
	lg := sm.lg
	if as.name != "" {
		lg = lg.With(slog.String("sim_name", as.name))
	}
	as.sim.Activate(lg)

	sm.mu.Lock(sm.lg)

	// Empty sim name is just a local sim, so no problem with replacing it...
	if _, ok := sm.activeSims[as.name]; ok && as.name != "" {
		sm.mu.Unlock(sm.lg)
		return ErrDuplicateSimName
	}

	sm.lg.Infof("%s: adding sim", as.name)
	sm.activeSims[as.name] = as

	ss, token, err := sm.signOn(as, initialTCP)
	if err != nil {
		sm.mu.Unlock(sm.lg)
		return err
	}

	hc := as.AddHumanController(initialTCP, token)
	sm.controllersByToken[token] = hc

	sm.mu.Unlock(sm.lg)

	// Run prespawn after the primary controller is signed in.
	if prespawn {
		as.sim.Prespawn()
	}

	go func() {
		defer sm.lg.CatchAndReportCrash()

		for !sm.SimShouldExit(as.sim) {
			// Terminate idle Sims after 4 hours, but not local Sims.
			if !as.local {
				// Sign off controllers we haven't heard from in 15 seconds so that
				// someone else can take their place. We only make this check for
				// multi-controller sims; we don't want to do this for local sims
				// so that we don't kick people off e.g. when their computer
				// sleeps.
				sm.mu.Lock(sm.lg) // FIXME: have a per-ActiveSim lock?
				for tcp, ctrl := range as.controllersByTCP {
					if time.Since(ctrl.lastUpdateCall) > 5*time.Second {
						if !ctrl.warnedNoUpdateCalls {
							ctrl.warnedNoUpdateCalls = true
							sm.lg.Warnf("%s: no messages for 5 seconds", tcp)
							as.sim.PostEvent(sim.Event{
								Type:        sim.StatusMessageEvent,
								WrittenText: tcp + " has not been heard from for 5 seconds. Connection lost?",
							})
						}

						if time.Since(ctrl.lastUpdateCall) > 15*time.Second {
							sm.lg.Warnf("%s: signing off idle controller", tcp)
							if err := sm.signOff(ctrl.token); err != nil {
								sm.lg.Errorf("%s: error signing off idle controller: %v", tcp, err)
							}
						}
					}
				}
				sm.mu.Unlock(sm.lg)
			}

			as.sim.Update()

			for tcp, ctrl := range as.controllersByTCP {
				if ctrl.speechWs == nil {
					continue
				}

				for _, ps := range as.sim.GetControllerSpeech(tcp) {
					sm.websocketTXBytes += int64(len(ps.MP3))

					w, err := ctrl.speechWs.NextWriter(websocket.BinaryMessage)
					if err != nil {
						sm.lg.Errorf("speechWs: %v", err)
						continue
					}

					enc := gob.NewEncoder(w)
					if err := enc.Encode(ps); err != nil {
						sm.lg.Errorf("speechWs encode: %v", err)
						continue
					}

					if err := w.Close(); err != nil {
						sm.lg.Errorf("speechWs close: %v", err)
						continue
					}
				}
			}

			time.Sleep(100 * time.Millisecond)
		}

		sm.lg.Infof("%s: terminating sim after %s idle", as.name, as.sim.IdleTime())

		sm.mu.Lock(sm.lg)
		delete(sm.activeSims, as.name)
		sm.mu.Unlock(sm.lg)
	}()

	*result = NewSimResult{
		SimState:        ss,
		ControllerToken: token,
		SpeechWSPort:    util.Select(sm.haveTTS, sm.httpPort, 0),
	}

	return nil
}

type ConnectResult struct {
	Configurations map[string]map[string]*Configuration
	RunningSims    map[string]*RemoteSim
	HaveTTS        bool
}

func (sm *SimManager) Connect(version int, result *ConnectResult) error {
	if version != ViceRPCVersion {
		return ErrRPCVersionMismatch
	}

	// Before we acquire the lock...
	if err := sm.GetRunningSims(0, &result.RunningSims); err != nil {
		return err
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	result.Configurations = sm.configs
	result.HaveTTS = sm.haveTTS

	return nil
}

// assume SimManager lock is held
func (sm *SimManager) signOn(as *activeSim, tcp string) (*sim.State, string, error) {
	ss, err := as.sim.SignOn(tcp)
	if err != nil {
		return nil, "", err
	}

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}
	token := base64.StdEncoding.EncodeToString(buf[:])

	return ss, token, nil
}

func (sm *SimManager) SignOff(token string) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.signOff(token)
}

func (sm *SimManager) signOff(token string) error {
	if ctrl, s, ok := sm.lookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		err := s.SignOff(ctrl.tcp)

		// Do this cleanup regardless of an error return from SignOff
		delete(sm.controllersByToken[token].asim.controllersByTCP, ctrl.tcp)
		delete(sm.controllersByToken, token)

		return err
	}
}

func (sm *SimManager) handleSpeechWSConnection(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	ctrl, ok := sm.controllersByToken[token]
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		sm.lg.Errorf("Invalid token for speech websocket: %s", token)
		return
	}
	if ctrl.speechWs != nil {
		ctrl.speechWs.Close()
	}

	var err error
	upgrader := websocket.Upgrader{EnableCompression: false}
	ctrl.speechWs, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		sm.lg.Errorf("Unable to upgrade speech websocket: %v", err)
		return
	}
}

func (sm *SimManager) GetRunningSims(_ int, result *map[string]*RemoteSim) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	running := make(map[string]*RemoteSim)
	for name, as := range sm.activeSims {
		rs := &RemoteSim{
			GroupName:         as.scenarioGroup,
			ScenarioName:      as.scenario,
			PrimaryController: as.sim.State.PrimaryController,
			RequirePassword:   as.password != "",
		}

		rs.AvailablePositions, rs.CoveredPositions = as.sim.GetAvailableCoveredPositions()

		running[name] = rs
	}

	*result = running
	return nil
}

func (sm *SimManager) LookupController(token string) (*humanController, *sim.Sim, bool) {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.lookupController(token)
}

func (sm *SimManager) lookupController(token string) (*humanController, *sim.Sim, bool) {
	if ctrl, ok := sm.controllersByToken[token]; ok {
		return ctrl, ctrl.asim.sim, true
	}
	return nil, nil, false
}

const simIdleLimit = 4 * time.Hour

func (sm *SimManager) SimShouldExit(sim *sim.Sim) bool {
	if sim.IdleTime() < simIdleLimit {
		return false
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	nIdle := 0
	for _, as := range sm.activeSims {
		if as.sim.IdleTime() >= simIdleLimit {
			nIdle++
		}
	}
	return nIdle > 10
}

func (sm *SimManager) GetSerializeSim(token string, s *sim.Sim) error {
	if _, sim, ok := sm.LookupController(token); !ok {
		return ErrNoSimForControllerToken
	} else {
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)

		*s = sim.GetSerializeSim()
	}
	return nil
}

func (sm *SimManager) GetStateUpdate(token string, update *sim.StateUpdate) error {
	sm.mu.Lock(sm.lg)

	if ctrl, ok := sm.controllersByToken[token]; !ok {
		sm.mu.Unlock(sm.lg)
		return ErrNoSimForControllerToken
	} else {
		s := ctrl.asim.sim
		ctrl.lastUpdateCall = time.Now()
		if ctrl.warnedNoUpdateCalls {
			ctrl.warnedNoUpdateCalls = false
			sm.lg.Warnf("%s: connection re-established", ctrl.tcp)
			s.PostEvent(sim.Event{
				Type:        sim.StatusMessageEvent,
				WrittenText: ctrl.tcp + " is back online.",
			})
		}

		sm.mu.Unlock(sm.lg)

		s.GetStateUpdate(ctrl.tcp, update)

		return nil
	}
}

type SimBroadcastMessage struct {
	Password string
	Message  string
}

func (sm *SimManager) Broadcast(m *SimBroadcastMessage, _ *struct{}) error {
	pw, err := os.ReadFile("password")
	if err != nil {
		return err
	}

	password := strings.TrimRight(string(pw), "\n\r")
	if password != m.Password {
		return ErrInvalidPassword
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	sm.lg.Infof("Broadcasting message: %s", m.Message)

	for _, as := range sm.activeSims {
		as.sim.PostEvent(sim.Event{
			Type:        sim.ServerBroadcastMessageEvent,
			WrittenText: m.Message,
		})
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Status / statistics via HTTP...

func (sm *SimManager) launchHTTPServer() int {
	handler := http.NewServeMux()
	handler.HandleFunc("/sup", func(w http.ResponseWriter, r *http.Request) {
		sm.statsHandler(w, r)
		sm.lg.Infof("%s: served stats request", r.URL.String())
	})
	handler.HandleFunc("/speech", sm.handleSpeechWSConnection)

	var listener net.Listener
	var err error
	var port int
	for i := range 10 {
		port = ViceHTTPServerPort + i
		if listener, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err == nil {
			sm.httpPort = port
			fmt.Printf("Launching HTTP server on port %d\n", port)
			break
		}
	}

	if err != nil {
		sm.lg.Warnf("Unable to start HTTP server")
		return 0
	} else {
		go http.Serve(listener, handler)

		sm.httpPort = port
		return port
	}
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

type simStatus struct {
	Name               string
	Config             string
	IdleTime           time.Duration
	Controllers        string
	TotalIFR, TotalVFR int
}

func (ss simStatus) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", ss.Name),
		slog.String("config", ss.Config),
		slog.Duration("idle", ss.IdleTime),
		slog.String("controllers", ss.Controllers),
		slog.Int("total_ifr", ss.TotalIFR),
		slog.Int("total_vfr", ss.TotalVFR))
}

func (sm *SimManager) getSimStatus() []simStatus {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var ss []simStatus
	for _, name := range util.SortedMapKeys(sm.activeSims) {
		as := sm.activeSims[name]
		status := simStatus{
			Name:        name,
			Config:      as.scenario,
			IdleTime:    as.sim.IdleTime().Round(time.Second),
			TotalIFR:    as.sim.State.TotalIFR,
			TotalVFR:    as.sim.State.TotalVFR,
			Controllers: strings.Join(as.sim.ActiveControllers(), ", "),
		}

		ss = append(ss, status)
	}

	return ss
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
		CPUUsage:         int(gomath.Round(usage[0])),
		TXWebsocket:      sm.websocketTXBytes,

		SimStatus: sm.getSimStatus(),
	}

	stats.RX, stats.TX = util.GetLoggedRPCBandwidth()

	statsTemplate.Execute(w, stats)
}
