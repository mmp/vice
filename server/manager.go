// server/manager.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

type SimManager struct {
	scenarioGroups   map[string]map[string]*scenarioGroup
	scenarioCatalogs map[string]map[string]*ScenarioCatalog

	// Active sessions
	sessionsByName  map[string]*simSession
	sessionsByToken map[string]*simSession

	// Helpers and such
	tts            sim.TTSProvider
	wxProvider     wx.Provider
	providersReady chan struct{}
	mapManifests   map[string]*sim.VideoMapManifest
	lg             *log.Logger

	// Stats and internal details
	mu               util.LoggingMutex
	startTime        time.Time
	httpPort         int
	websocketTXBytes atomic.Int64
	ttsUsageByIP     map[string]*ttsUsageStats
	local            bool
}

// Client-side info about the available scenarios.
type ScenarioCatalog struct {
	Scenarios        map[string]*ScenarioSpec
	ControlPositions map[sim.TCP]*av.Controller
	DefaultScenario  string
	Facility         string
	ARTCC            string
	Area             string
}

type ScenarioSpec struct {
	ControllerConfiguration *sim.ControllerConfiguration
	PrimaryAirport          string
	MagneticVariation       float32
	WindSpecifier           *wx.WindSpecifier

	LaunchConfig sim.LaunchConfig

	DepartureRunways []sim.DepartureRunway
	ArrivalRunways   []sim.ArrivalRunway
}

func (s *ScenarioSpec) AllAirports() []string {
	allAirports := make(map[string]bool)
	for _, runway := range s.DepartureRunways {
		allAirports[runway.Airport] = true
	}
	for _, runway := range s.ArrivalRunways {
		allAirports[runway.Airport] = true
	}
	return util.SortedMapKeys(allAirports)
}

///////////////////////////////////////////////////////////////////////////
// Constructor and Initialization

func NewSimManager(scenarioGroups map[string]map[string]*scenarioGroup, scenarioCatalogs map[string]map[string]*ScenarioCatalog,
	mapManifests map[string]*sim.VideoMapManifest, serverAddress string, isLocal bool, lg *log.Logger) *SimManager {
	sm := &SimManager{
		scenarioGroups:   scenarioGroups,
		scenarioCatalogs: scenarioCatalogs,
		sessionsByName:   make(map[string]*simSession),
		sessionsByToken:  make(map[string]*simSession),
		mapManifests:     mapManifests,
		startTime:        time.Now(),
		ttsUsageByIP:     make(map[string]*ttsUsageStats),
		local:            isLocal,
		providersReady:   make(chan struct{}),
		lg:               lg,
	}

	// Initialize TTS and WX providers asynchronously so the server can start
	// accepting connections immediately. Callers that need providers will
	// block in getProviders() until initialization completes or times out.
	go sm.initRemoteProviders(serverAddress, lg)

	sm.launchHTTPServer()

	return sm
}

func (sm *SimManager) initRemoteProviders(serverAddress string, lg *log.Logger) {
	defer close(sm.providersReady)

	// Use a single context to control all provider initialization.
	// This must complete before the client RPC timeout (5 seconds).
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// Initialize TTS and WX providers in parallel since they're independent
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sm.tts = makeTTSProvider(ctx, serverAddress, lg)
	}()

	go func() {
		defer wg.Done()
		sm.wxProvider, _ = MakeWXProvider(ctx, serverAddress, lg)
	}()

	wg.Wait()
}

func (sm *SimManager) getProviders() (sim.TTSProvider, wx.Provider) {
	<-sm.providersReady
	return sm.tts, sm.wxProvider
}

///////////////////////////////////////////////////////////////////////////
// Session Management - Creating and Connecting to Sims

type NewSimRequest struct {
	Facility     string
	NewSimName   string
	GroupName    string
	ScenarioName string

	ScenarioSpec *ScenarioSpec
	StartTime    time.Time

	TFRs        []av.TFR
	Emergencies []sim.Emergency

	RequirePassword bool
	Password        string

	EnforceUniqueCallsignSuffix bool

	PilotErrorInterval float32

	Initials            string // Controller initials (e.g., "XX")
	Privileged          bool
	DisableTextToSpeech bool
}

func MakeNewSimRequest() NewSimRequest {
	return NewSimRequest{
		NewSimName:         rand.Make().AdjectiveNoun(),
		PilotErrorInterval: 3,
	}
}

type NewSimResult struct {
	SimState        *SimState
	ControllerToken string
	SpeechWSPort    int
}

// SimState wraps sim.State and adds server-specific fields.
// The server derives ActiveTCWs from connectionsByToken on each request;
// it is not stored in simSession.
type SimState struct {
	sim.State

	// This is server-layer information - the sim layer doesn't track this.
	ActiveTCWs []sim.TCW
}

// pruneForClient tidies the NewSimResult, removing fields that are not used by client code
// in order to reduce the amount of bandwidth used to send the NewSimResult to the client.
func (r *NewSimResult) pruneForClient() {
	r.SimState = deep.MustCopy(r.SimState)

	for _, ap := range r.SimState.Airports {
		ap.Departures = nil
	}
}

const NewSimRPC = "SimManager.NewSim"

func (sm *SimManager) NewSim(req *NewSimRequest, result *NewSimResult) error {
	lg := sm.lg.With(slog.String("sim_name", req.NewSimName))

	if nsc := sm.makeSimConfiguration(req, lg); nsc != nil {
		manifest := sm.mapManifests[nsc.FacilityAdaptation.VideoMapFile]
		s := sim.NewSim(*nsc, manifest, lg)
		session := makeSimSession(req.NewSimName, req.GroupName, req.ScenarioName, req.Password, s, sm.lg)
		pos := s.ScenarioRootPosition()
		return sm.Add(session, result, pos, req.Initials, req.Privileged, true, req.DisableTextToSpeech)
	} else {
		return ErrInvalidSimConfiguration
	}
}

func (sm *SimManager) makeSimConfiguration(req *NewSimRequest, lg *log.Logger) *sim.NewSimConfiguration {
	facility, ok := sm.scenarioGroups[req.Facility]
	if !ok {
		lg.Errorf("%s: unknown facility", req.Facility)
		return nil
	}
	sg, ok := facility[req.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", req.GroupName)
		return nil
	}
	sc, ok := sg.Scenarios[req.ScenarioName]
	if !ok {
		lg.Errorf("%s: unknown scenario", req.ScenarioName)
		return nil
	}

	description := util.Select(sm.local, " "+req.ScenarioName, "@"+req.NewSimName+": "+req.ScenarioName)

	_, wxp := sm.getProviders()

	nsc := sim.NewSimConfiguration{
		TFRs:                        req.TFRs,
		Facility:                    req.Facility,
		LaunchConfig:                req.ScenarioSpec.LaunchConfig,
		FacilityAdaptation:          deep.MustCopy(sg.FacilityAdaptation),
		EnforceUniqueCallsignSuffix: req.EnforceUniqueCallsignSuffix,
		PilotErrorInterval:          req.PilotErrorInterval,
		DepartureRunways:            sc.DepartureRunways,
		ArrivalRunways:              sc.ArrivalRunways,
		VFRReportingPoints:          sg.VFRReportingPoints,
		ReportingPoints:             sg.ReportingPoints,
		Description:                 description,
		MagneticVariation:           sg.MagneticVariation,
		NmPerLongitude:              sg.NmPerLongitude,
		WindSpecifier:               sc.WindSpecifier,
		Airports:                    sg.Airports,
		Fixes:                       sg.Fixes,
		PrimaryAirport:              sg.PrimaryAirport,
		Center:                      util.Select(sc.Center.IsZero(), sg.FacilityAdaptation.Center, sc.Center),
		Range:                       util.Select(sc.Range == 0, sg.FacilityAdaptation.Range, sc.Range),
		DefaultMaps:                 sc.DefaultMaps,
		DefaultMapGroup:             sc.DefaultMapGroup,
		InboundFlows:                sg.InboundFlows,
		Airspace:                    sg.Airspace,
		ControllerAirspace:          sc.Airspace,
		ControlPositions:            sg.ControlPositions,
		VirtualControllers:          sc.VirtualControllers,
		ControllerConfiguration:     sc.ControllerConfiguration,
		WXProvider:                  wxp,
		Emergencies:                 req.Emergencies,
		StartTime:                   req.StartTime,
	}

	return &nsc
}

type JoinSimRequest struct {
	SimName             string
	TCW                 sim.TCW   // Which TCW to sign into
	SelectedTCPs        []sim.TCP // TCPs to consolidate (non-relief only)
	Initials            string    // Controller initials (e.g., "MP")
	Password            string
	Privileged          bool
	DisableTextToSpeech bool
	JoiningAsRelief     bool
}

const ConnectToSimRPC = "SimManager.ConnectToSim"

func (sm *SimManager) ConnectToSim(req *JoinSimRequest, result *NewSimResult) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	session, ok := sm.sessionsByName[req.SimName]
	if !ok {
		return ErrNoNamedSim
	}

	if session.password != "" && req.Password != session.password {
		return ErrInvalidPassword
	}

	tcw := req.TCW

	var token string
	var eventSub *sim.EventsSubscription
	if req.JoiningAsRelief {
		// Relief mode: don't call sim.SignOn (position already signed in)
		// Just generate a token for this user
		token = sm.makeControllerToken()

		// Relief controllers get their own event subscription
		eventSub = session.sim.Subscribe()
	} else {
		// Normal sign-in: check if TCW is already occupied
		if err := sm.checkTCWAvailable(session, tcw); err != nil {
			return err
		}

		// Normal sign-in: call sim.SignOn
		var err error
		token, eventSub, err = sm.signOn(session, req)
		if err != nil {
			return err
		}
	}

	session.AddHumanController(token, tcw, req.Initials, req.DisableTextToSpeech, eventSub)
	sm.sessionsByToken[token] = session

	*result = *sm.buildNewSimResult(session, tcw, eventSub, token)

	return nil
}

func (sm *SimManager) makeControllerToken() string {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		sm.lg.Errorf("%v", err)
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf[:])
}

// checkTCWAvailable checks if a TCW is available (not already occupied by a human).
// Returns ErrTCWAlreadyOccupied if the TCW is in use.
// Assumes SimManager lock is held.
func (sm *SimManager) checkTCWAvailable(ss *simSession, tcw sim.TCW) error {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	for _, conn := range ss.connectionsByToken {
		if conn.tcw == tcw {
			return ErrTCWAlreadyOccupied
		}
	}

	return nil
}

// buildNewSimResult prepares the controller state and constructs a NewSimResult.
func (sm *SimManager) buildNewSimResult(session *simSession, tcw sim.TCW,
	eventSub *sim.EventsSubscription, token string) *NewSimResult {

	state := session.sim.GetStateForController(tcw)
	var initialUpdate sim.StateUpdate
	session.sim.GetStateUpdate(tcw, eventSub, &initialUpdate)
	initialUpdate.Apply(state, nil)

	result := &NewSimResult{
		SimState:        &SimState{State: *state, ActiveTCWs: session.GetActiveTCWs()},
		ControllerToken: token,
		SpeechWSPort:    util.Select(sm.tts != nil, sm.httpPort, 0),
	}
	result.pruneForClient()
	return result
}

const AddLocalRPC = "SimManager.AddLocal"

func (sm *SimManager) AddLocal(s *sim.Sim, result *NewSimResult) error {
	session := makeLocalSimSession(s, sm.lg)
	if !sm.local {
		sm.lg.Errorf("Called AddLocal with sm.local == false")
	}
	return sm.Add(session, result, s.ScenarioRootPosition(), "", false, false, false)
}

func (sm *SimManager) Add(session *simSession, result *NewSimResult, initialTCP sim.ControllerPosition, initials string, instructor bool,
	prespawn bool, disableTextToSpeech bool) error {
	_, wxp := sm.getProviders()
	session.sim.Activate(session.lg, wxp)

	sm.mu.Lock(sm.lg)

	// Empty sim name is just a local sim, so no problem with replacing it...
	if _, ok := sm.sessionsByName[session.name]; ok && session.name != "" {
		sm.mu.Unlock(sm.lg)
		return ErrDuplicateSimName
	}

	sm.lg.Infof("%s: adding sim", session.name)
	sm.sessionsByName[session.name] = session

	tcw := sim.TCW(initialTCP)
	joinReq := &JoinSimRequest{
		TCW:                 tcw,
		Privileged:          instructor,
		DisableTextToSpeech: disableTextToSpeech,
	}
	token, eventSub, err := sm.signOn(session, joinReq)
	if err != nil {
		sm.mu.Unlock(sm.lg)
		return err
	}

	session.AddHumanController(token, tcw, initials, disableTextToSpeech, eventSub)
	sm.sessionsByToken[token] = session

	sm.mu.Unlock(sm.lg)

	// Run prespawn after the root controller is signed in.
	if prespawn {
		session.sim.Prespawn()
	}

	go sm.runSimUpdateLoop(session)

	// Get the state after prespawn (if any) has completed.
	*result = *sm.buildNewSimResult(session, tcw, eventSub, token)

	return nil
}

// runSimUpdateLoop runs the update loop for a sim session, handling idle
// timeout and cleanup.
func (sm *SimManager) runSimUpdateLoop(session *simSession) {
	defer sm.lg.CatchAndReportCrash()

	// Terminate idle Sims after 4 hours, but not local Sims.
	const simIdleLimit = 4 * time.Hour
	for sm.local || session.sim.IdleTime() < simIdleLimit {
		if !sm.local && !util.DebuggerIsRunning() {
			session.CullIdleControllers(sm)
		}

		session.sim.Update()

		sm.websocketTXBytes.Add(session.SendSpeechMP3s())

		time.Sleep(100 * time.Millisecond)
	}

	sm.lg.Infof("%s: terminating sim after %s idle", session.name, session.sim.IdleTime())

	session.sim.Destroy()

	sm.mu.Lock(sm.lg)
	// Clean up all controllers for this sim
	for token, ss := range sm.sessionsByToken {
		if ss == session {
			delete(sm.sessionsByToken, token)
		}
	}
	delete(sm.sessionsByName, session.name)
	sm.mu.Unlock(sm.lg)
}

///////////////////////////////////////////////////////////////////////////
// Session Management - Sign On/Off

func (sm *SimManager) SignOff(token string) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.signOff(token)
}

func (sm *SimManager) signOff(token string) error {
	session, ok := sm.sessionsByToken[token]
	if !ok {
		return ErrNoSimForControllerToken
	}

	delete(sm.sessionsByToken, token)

	result, ok := session.SignOff(token)
	if !ok {
		return ErrNoSimForControllerToken
	}

	// If this was the last user at the TCW, post messages and clear privileges
	if result.UsersAtTCW == 0 {
		// Get positions for the uncovered message
		uncoveredPositions := session.sim.GetPositionsForTCW(result.TCW)

		// Clear privileged status
		session.sim.SetPrivilegedTCW(result.TCW, false)

		msg := string(result.TCW)
		if result.Initials != "" {
			msg += " (" + result.Initials + ")"
		}
		msg += " has signed off."
		session.sim.PostEvent(sim.Event{
			Type:        sim.StatusMessageEvent,
			WrittenText: msg,
		})

		// If there are uncovered positions, post an error message
		if len(uncoveredPositions) > 0 {
			tcpStrs := make([]string, len(uncoveredPositions))
			for i, tcp := range uncoveredPositions {
				tcpStrs[i] = string(tcp)
			}
			slices.Sort(tcpStrs)
			session.sim.PostEvent(sim.Event{
				Type:        sim.ErrorMessageEvent,
				WrittenText: "Uncovered positions: " + strings.Join(tcpStrs, ", "),
			})
		}
	}

	return nil
}

// assume SimManager lock is held
func (sm *SimManager) signOn(ss *simSession, req *JoinSimRequest) (string, *sim.EventsSubscription, error) {
	_, eventSub, err := ss.sim.SignOn(req.TCW, req.SelectedTCPs)
	if err != nil {
		return "", nil, err
	}

	// Set privileged status if instructor
	if req.Privileged {
		ss.sim.SetPrivilegedTCW(req.TCW, true)
	}

	// Post sign-on message
	msg := string(req.TCW) + " (" + req.Initials + ") has signed on for "
	positions := ss.sim.GetPositionsForTCW(req.TCW)
	msg += strings.Join(util.MapSlice(positions, func(p sim.ControllerPosition) string { return string(p) }), ", ")
	msg += "."
	ss.sim.PostEvent(sim.Event{
		Type:        sim.StatusMessageEvent,
		WrittenText: msg,
	})

	return sm.makeControllerToken(), eventSub, nil
}

///////////////////////////////////////////////////////////////////////////
// Controller Lookup and State Updates

type ConnectResult struct {
	ScenarioCatalogs    map[string]map[string]*ScenarioCatalog
	RunningSims         map[string]*RunningSim
	HaveTTS             bool
	AvailableWXByTRACON map[string][]util.TimeInterval
}

const ConnectRPC = "SimManager.Connect"

func (sm *SimManager) Connect(version int, result *ConnectResult) error {
	if version != ViceRPCVersion {
		return ErrRPCVersionMismatch
	}

	// Before we acquire the lock...
	if err := sm.GetRunningSims(0, &result.RunningSims); err != nil {
		return err
	}

	tts, wxp := sm.getProviders()
	result.HaveTTS = tts != nil
	result.AvailableWXByTRACON = wxp.GetAvailableTimeIntervals()

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	result.ScenarioCatalogs = sm.scenarioCatalogs

	return nil
}

type TCPConsolidation struct {
	sim.TCPConsolidation
	Initials []string // Server-layer addition: initials of signed-in controllers
}

func (s TCPConsolidation) IsOccupied() bool { return len(s.Initials) > 0 }

type RunningSim struct {
	GroupName                    string
	ScenarioName                 string
	RequirePassword              bool
	ScenarioDefaultConsolidation map[sim.TCP][]sim.TCP
	CurrentConsolidation         map[sim.TCW]TCPConsolidation
}

const GetRunningSimsRPC = "SimManager.GetRunningSims"

func (sm *SimManager) GetRunningSims(_ int, result *map[string]*RunningSim) error {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	running := make(map[string]*RunningSim)
	for name, ss := range sm.sessionsByName {
		running[name] = &RunningSim{
			GroupName:                    ss.scenarioGroup,
			ScenarioName:                 ss.scenario,
			RequirePassword:              ss.password != "",
			ScenarioDefaultConsolidation: ss.sim.State.ScenarioDefaultConsolidation,
			CurrentConsolidation:         ss.GetCurrentConsolidation(),
		}
	}

	*result = running
	return nil
}

// controllerContext holds the context for a connected controller, returned by LookupController.
// A nil value indicates the controller was not found.
type controllerContext struct {
	tcw      sim.TCW
	initials string
	sim      *sim.Sim
	eventSub *sim.EventsSubscription
	session  *simSession
}

func (sm *SimManager) LookupController(token string) *controllerContext {
	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	return sm.lookupController(token)
}

func (sm *SimManager) lookupController(token string) *controllerContext {
	if session, ok := sm.sessionsByToken[token]; ok {
		return session.MakeControllerContext(token)
	}
	return nil
}

func (sm *SimManager) GetStateUpdate(token string, update *SimStateUpdate) error {
	sm.mu.Lock(sm.lg)
	session, ok := sm.sessionsByToken[token]
	if !ok {
		sm.mu.Unlock(sm.lg)
		return ErrNoSimForControllerToken
	}
	sm.mu.Unlock(sm.lg)

	session.GetStateUpdate(token, update, sm.tts)
	return nil
}

// SimStateUpdate wraps sim.StateUpdate and adds server-specific fields.
type SimStateUpdate struct {
	sim.StateUpdate

	// ActiveTCWs tracks which workstations have humans signed in.
	ActiveTCWs []sim.TCW
}

// Apply applies the update to the state, including server-specific fields.
func (su *SimStateUpdate) Apply(state *SimState, eventStream *sim.EventStream) {
	su.StateUpdate.Apply(&state.State, eventStream)
	state.ActiveTCWs = su.ActiveTCWs
}

// GetStateUpdate fills in a server.SimStateUpdate with both sim state and human controllers.
func (c *controllerContext) GetStateUpdate(update *SimStateUpdate) {
	c.sim.GetStateUpdate(c.tcw, c.eventSub, &update.StateUpdate)
	update.ActiveTCWs = c.session.GetActiveTCWs()
}

const GetSerializeSimRPC = "SimManager.GetSerializeSim"

func (sm *SimManager) GetSerializeSim(token string, s *sim.Sim) error {
	c := sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	*s = c.sim.GetSerializeSim()
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Text-to-Speech

func (sm *SimManager) HandleSpeechWSConnection(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	sm.mu.Lock(sm.lg)

	session, ok := sm.sessionsByToken[token]
	if !ok {
		sm.mu.Unlock(sm.lg)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		sm.lg.Errorf("Invalid token for speech websocket: %s", token)
		return
	}
	sm.mu.Unlock(sm.lg)

	session.HandleSpeechWSConnection(token, w, r)
}

const GetAllVoicesRPC = "SimManager.GetAllVoices"

// GetAllVoices returns all available voices for TTS
func (sm *SimManager) GetAllVoices(_ struct{}, voices *[]sim.Voice) error {
	if sm.tts == nil {
		return fmt.Errorf("TTS not available")
	}

	fut := sm.tts.GetAllVoices()
	// Note: TTS implementations guarantee exactly one channel will send a value
	// before both channels are closed. If both close without sending, this loop
	// would hang indefinitely.
	for {
		select {
		case v, ok := <-fut.VoicesCh:
			if ok {
				*voices = v
				return nil
			}
			fut.VoicesCh = nil // stop checking
		case err, ok := <-fut.ErrCh:
			if ok {
				return err
			}
			fut.ErrCh = nil
		}
	}
}

const TextToSpeechRPC = "SimManager.TextToSpeech"

// TextToSpeech converts text to speech and returns the audio data
func (sm *SimManager) TextToSpeech(req *TTSRequest, speechMp3 *[]byte) error {
	if sm.tts == nil {
		return fmt.Errorf("TTS not available")
	}

	if len(strings.Fields(req.Text)) > 50 {
		return fmt.Errorf("TTS capacity exceeded")
	}

	// Use ClientIP from the request (populated by LoggingServerCodec)
	clientIP := req.ClientIP
	if clientIP == "" {
		clientIP = "unknown"
	}

	if err := sm.UpdateTTSUsage(clientIP, req.Text); err != nil {
		return err
	}

	fut := sm.tts.TextToSpeech(req.Voice, req.Text)
	// Note: Current TTS implementations guarantee that exactly one channel will send a value before
	// both channels are closed. If both close without sending, this loop would hang indefinitely.
	for {
		select {
		case mp3, ok := <-fut.Mp3Ch:
			if ok {
				*speechMp3 = mp3
				return nil
			}
			fut.Mp3Ch = nil // stop checking
		case err, ok := <-fut.ErrCh:
			if ok {
				return err
			}
			fut.ErrCh = nil // stop checking
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Weather

const GetMETARRPC = "SimManager.GetMETAR"

func (sm *SimManager) GetMETAR(airports []string, result *map[string]wx.METARSOA) error {
	defer sm.lg.CatchAndReportCrash()

	if sm.wxProvider == nil {
		return ErrWeatherUnavailable
	}

	var err error
	*result, err = sm.wxProvider.GetMETAR(airports)
	return err
}

const GetTimeIntervalsRPC = "SimManager.GetTimeIntervals"

func (sm *SimManager) GetTimeIntervals(_ struct{}, result *map[string][]util.TimeInterval) error {
	defer sm.lg.CatchAndReportCrash()

	if sm.wxProvider == nil {
		return ErrWeatherUnavailable
	}

	*result = sm.wxProvider.GetAvailableTimeIntervals()
	return nil
}

type PrecipURLArgs struct {
	Facility string
	Time     time.Time
}

type PrecipURL struct {
	URL      string
	NextTime time.Time
}

const GetPrecipURLRPC = "SimManager.GetPrecipURL"

func (sm *SimManager) GetPrecipURL(args PrecipURLArgs, result *PrecipURL) error {
	defer sm.lg.CatchAndReportCrash()

	if sm.wxProvider == nil {
		return ErrWeatherUnavailable
	}

	var err error
	result.URL, result.NextTime, err = sm.wxProvider.GetPrecipURL(args.Facility, args.Time)
	return err
}

type GetAtmosArgs struct {
	Facility       string
	Time           time.Time
	PrimaryAirport string
}

type GetAtmosResult struct {
	AtmosByPointSOA *wx.AtmosByPointSOA
	Time            time.Time
	NextTime        time.Time
}

const GetAtmosGridRPC = "SimManager.GetAtmosGrid"

func (sm *SimManager) GetAtmosGrid(args GetAtmosArgs, result *GetAtmosResult) error {
	defer sm.lg.CatchAndReportCrash()

	if sm.wxProvider == nil {
		return ErrWeatherUnavailable
	}

	// Only load for TRACON scenarios (for now)
	if _, ok := av.DB.TRACONs[args.Facility]; !ok {
		return nil
	}

	var err error
	result.AtmosByPointSOA, result.Time, result.NextTime, err = sm.wxProvider.GetAtmosGrid(args.Facility, args.Time, args.PrimaryAirport)
	return err
}

///////////////////////////////////////////////////////////////////////////
// Admin

type BroadcastMessage struct {
	Password string
	Message  string
}

const BroadcastRPC = "SimManager.Broadcast"

func (sm *SimManager) Broadcast(m *BroadcastMessage, _ *struct{}) error {
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

	sm.lg.Warnf("Broadcasting message: %s", m.Message)

	for _, ss := range sm.sessionsByName {
		ss.sim.PostEvent(sim.Event{
			Type:        sim.ServerBroadcastMessageEvent,
			WrittenText: m.Message,
		})
	}
	return nil
}
