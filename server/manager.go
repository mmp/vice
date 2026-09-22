// server/manager.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

type SimManager struct {
	// scenarios is everything scenario.Load produced, swapped as a
	// unit by ReloadScenarios. It is held atomically rather than under
	// sm.mu so that readers, which run concurrently with RPC service,
	// never see a half-replaced set and don't have to lock to look at it.
	scenarios atomic.Pointer[scenario.Tables]

	// Active sessions
	sessionsByName  map[string]*simSession
	sessionsByToken map[string]*simSession

	// Helpers and such
	wxProvider     *wx.Provider
	providersReady chan struct{}
	lg             *log.Logger

	// Stats and internal details
	mu        util.LoggingMutex
	startTime time.Time
	httpPort  int
	local     bool
}

///////////////////////////////////////////////////////////////////////////
// Constructor and Initialization

func NewSimManager(config LaunchConfig, tables *scenario.Tables, lg *log.Logger) *SimManager {
	sm := &SimManager{
		sessionsByName:  make(map[string]*simSession),
		sessionsByToken: make(map[string]*simSession),
		startTime:       time.Now(),
		local:           config.IsLocal,
		providersReady:  make(chan struct{}),
		lg:              lg,
	}
	sm.scenarios.Store(tables)

	// Initialize WX provider asynchronously so the server can start
	// accepting connections immediately. Callers that need providers will
	// block in getProviders() until initialization completes or times out.
	go func() {
		defer close(sm.providersReady)
		sm.wxProvider = wx.MakeProvider(config.ServerAddress, lg)
	}()

	sm.launchHTTPServer()

	return sm
}

// getWXProvider blocks until the weather provider is initialized.
// Synchronization is via the providersReady channel, not sm.mu.
func (sm *SimManager) getWXProvider() *wx.Provider {
	<-sm.providersReady
	return sm.wxProvider
}

///////////////////////////////////////////////////////////////////////////
// Session Management - Creating and Connecting to Sims

type NewSimRequest struct {
	Facility     string
	NewSimName   string
	GroupName    string
	ScenarioName string

	ScenarioSpec *scenario.Spec
	StartTime    time.Time

	RequirePassword bool
	Password        string

	EnforceUniqueCallsignSuffix bool

	PilotErrorInterval float32

	Initials   string // Controller initials (e.g., "XX")
	Privileged bool
}

func MakeNewSimRequest() NewSimRequest {
	return NewSimRequest{
		NewSimName:         rand.Make().AdjectiveNoun(),
		PilotErrorInterval: 0,
	}
}

type NewSimResult struct {
	SimState        *SimState
	ControllerToken string
}

// SimState wraps sim.UserState and adds server-specific fields.
type SimState struct {
	sim.UserState

	// User-related items not managed by the Sim.
	UserTCW                             sim.TCW
	ActiveTCWs                          []sim.TCW
	ControllerVideoMaps                 []string
	ControllerDefaultVideoMaps          []string
	ControllerMonitoredBeaconCodeBlocks []av.Squawk
	ControllerVideoMapFile              string
	VideoMapLibraryHashes               map[string][]byte

	UserIsPrivileged bool // Whether this user has elevated privileges (can control any aircraft)

	FlightStripACIDs []sim.ACID
}

// TCWIsPrivileged returns whether the given TCW has elevated privileges.
// Note: only the current user's TCW should be passed; this is for API compatibility.
func (ss *SimState) TCWIsPrivileged(tcw sim.TCW) bool {
	return ss.UserIsPrivileged
}

const NewSimRPC = "SimManager.NewSim"

func (sm *SimManager) NewSim(req *NewSimRequest, result *NewSimResult) error {
	defer sm.lg.CatchAndReportCrash()

	lg := sm.lg.With(slog.String("sim_name", req.NewSimName))

	nsc, err := sm.makeSimConfiguration(req, lg)
	if err != nil {
		return err
	}
	s := sim.NewSim(*nsc, lg)
	session := makeSimSession(req.NewSimName, req.GroupName, req.ScenarioName, req.Password, s, sm.lg)
	pos := s.ScenarioRootPosition()
	return sm.Add(session, result, pos, req.Initials, req.Privileged, true)
}

// makeSimConfiguration only accesses read-only SimManager members that are set at
// construction time; no mutex necessary.
func (sm *SimManager) makeSimConfiguration(req *NewSimRequest, lg *log.Logger) (*sim.NewSimConfiguration, error) {
	tables := sm.scenarios.Load()
	facility, ok := tables.Groups[req.Facility]
	if !ok {
		lg.Errorf("%s: unknown facility", req.Facility)
		return nil, ErrInvalidSimConfiguration
	}
	sg, ok := facility[req.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", req.GroupName)
		return nil, ErrInvalidSimConfiguration
	}

	if err := sm.checkScenarioTrafficSource(req.Facility, req.GroupName, req.ScenarioName,
		req.ScenarioSpec.LaunchConfig.TrafficSource); err != nil {
		lg.Errorf("%s/%s: %s traffic: %v", req.Facility, req.ScenarioName,
			req.ScenarioSpec.LaunchConfig.TrafficSource, err)
		return nil, err
	}

	nsc, err := sg.NewSimConfiguration(req.ScenarioName, req.ScenarioSpec.LaunchConfig)
	if err != nil {
		lg.Errorf("%s: %v", req.ScenarioName, err)
		return nil, ErrInvalidSimConfiguration
	}

	briefMarkdown, err := tables.Briefs.LoadBrief(req.Facility)
	if err != nil {
		lg.Warnf("unable to load brief for %q: %v", req.Facility, err)
	}

	nsc.Description = util.Select(sm.local, " "+req.ScenarioName, "@"+req.NewSimName+": "+req.ScenarioName)
	nsc.Brief = briefMarkdown
	nsc.EnforceUniqueCallsignSuffix = req.EnforceUniqueCallsignSuffix
	nsc.PilotErrorInterval = req.PilotErrorInterval
	nsc.WXProvider = sm.getWXProvider()
	nsc.Emergencies = tables.Emergencies
	nsc.StartTime = req.StartTime

	// Look up historical TFRs for this facility and time.
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = db.DB.ARTCCForFacility(req.Facility)
	}
	if artcc != "" {
		var err error
		if db.DB.IsARTCC(req.Facility) {
			nsc.TFRs, err = wx.GetCachedTFRsForARTCC(artcc, req.StartTime)
		} else {
			nsc.TFRs, err = wx.GetCachedTFRsForTRACON(artcc, nsc.Center, nsc.Range, req.StartTime)
		}
		if err != nil {
			lg.Warnf("unable to load TFRs for %s: %v", artcc, err)
		}
	}

	return nsc, nil
}

type JoinSimRequest struct {
	SimName         string
	TCW             sim.TCW   // Which TCW to sign into
	SelectedTCPs    []sim.TCP // TCPs to consolidate (non-relief only)
	Initials        string    // Controller initials (e.g., "MP")
	Password        string
	Privileged      bool
	JoiningAsRelief bool
}

const ConnectToSimRPC = "SimManager.ConnectToSim"

func (sm *SimManager) ConnectToSim(req *JoinSimRequest, result *NewSimResult) error {
	defer sm.lg.CatchAndReportCrash()

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

	session.AddHumanController(token, tcw, req.Initials, eventSub)
	sm.sessionsByToken[token] = session

	*result = *sm.buildNewSimResult(session, tcw, token)

	return nil
}

// makeControllerToken touches no sm.* fields beyond the logger and so
// does not require sm.mu.
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

func (sm *SimManager) buildNewSimResult(session *simSession, tcw sim.TCW, token string) *NewSimResult {
	videoMaps, defaultMaps, beaconCodes := session.sim.GetControllerVideoMaps(tcw)

	vmFile := session.sim.GetControllerVideoMapFile(tcw)

	// Collect hashes for every video map file the client may need: the
	// controller's primary file plus any referenced by the scenario brief.
	hashes := make(map[string][]byte)
	tables := sm.scenarios.Load()
	maps.Copy(hashes, tables.Briefs.VideoMapHashes(session.sim.Facility()))
	if _, present := hashes[vmFile]; !present && vmFile != "" {
		if spec, ok := tables.MapSpecs[vmFile]; ok {
			if h, err := spec.Hash(); err == nil {
				hashes[vmFile] = h
			}
		}
	}

	return &NewSimResult{
		SimState: &SimState{
			UserState:                           *session.sim.GetUserState(),
			UserTCW:                             tcw,
			ActiveTCWs:                          session.GetActiveTCWs(),
			ControllerVideoMaps:                 videoMaps,
			ControllerDefaultVideoMaps:          defaultMaps,
			ControllerMonitoredBeaconCodeBlocks: beaconCodes,
			ControllerVideoMapFile:              vmFile,
			VideoMapLibraryHashes:               hashes,
			UserIsPrivileged:                    session.sim.TCWIsPrivileged(tcw),
		},
		ControllerToken: token,
	}
}

const AddLocalRPC = "SimManager.AddLocal"

type AddLocalRequest struct {
	Sim      *sim.Sim
	Initials string
}

func (sm *SimManager) AddLocal(req *AddLocalRequest, result *NewSimResult) error {
	defer sm.lg.CatchAndReportCrash()

	session := makeLocalSimSession(req.Sim, sm.lg)
	if !sm.local {
		sm.lg.Errorf("Called AddLocal with sm.local == false")
	}
	return sm.Add(session, result, req.Sim.ScenarioRootPosition(), req.Initials, false, false)
}

func (sm *SimManager) Add(session *simSession, result *NewSimResult, initialTCP sim.ControlPosition, initials string, instructor bool,
	prespawn bool) error {
	wxp := sm.getWXProvider()
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
		TCW:        tcw,
		Initials:   initials,
		Privileged: instructor,
	}
	token, eventSub, err := sm.signOn(session, joinReq)
	if err != nil {
		sm.mu.Unlock(sm.lg)
		return err
	}

	session.AddHumanController(token, tcw, initials, eventSub)
	sm.sessionsByToken[token] = session

	sm.mu.Unlock(sm.lg)

	// Run prespawn after the root controller is signed in.
	if prespawn {
		session.sim.Prespawn()
	}

	go sm.runSimUpdateLoop(session)

	// buildNewSimResult only reads init-immutable sm fields and goes
	// through Sim accessors that take their own lock — no sm.mu needed.
	*result = *sm.buildNewSimResult(session, tcw, token)

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
		session.sim.ClearSTTCommands(result.TCW)
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
	msg += strings.Join(util.MapSlice(positions, func(p sim.ControlPosition) string { return string(p) }), ", ")
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
	ScenarioCatalogs      map[string]map[string]*scenario.Catalog
	RunningSims           map[string]*RunningSim
	AvailableWXByFacility map[string][]util.TimeInterval
}

const ConnectRPC = "SimManager.Connect"

func (sm *SimManager) Connect(version int, result *ConnectResult) error {
	defer sm.lg.CatchAndReportCrash()

	if version != ViceRPCVersion {
		return ErrRPCVersionMismatch
	}

	// Before we acquire the lock...
	if err := sm.GetRunningSims(0, &result.RunningSims); err != nil {
		return err
	}

	result.AvailableWXByFacility = make(map[string][]util.TimeInterval)
	maps.Copy(result.AvailableWXByFacility, wx.GetTRACONTimeIntervals())
	maps.Copy(result.AvailableWXByFacility, wx.GetARTCCTimeIntervals())

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	result.ScenarioCatalogs = sm.scenarios.Load().Catalogs

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
	defer sm.lg.CatchAndReportCrash()

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	running := make(map[string]*RunningSim)
	for name, ss := range sm.sessionsByName {
		running[name] = &RunningSim{
			GroupName:                    ss.scenarioGroup,
			ScenarioName:                 ss.scenario,
			RequirePassword:              ss.password != "",
			ScenarioDefaultConsolidation: ss.sim.ScenarioDefaultConsolidation,
			CurrentConsolidation:         ss.GetCurrentConsolidation(),
		}
	}

	*result = running
	return nil
}

// controllerContext holds the context for a connected controller, returned by LookupController.
// A nil value indicates the controller was not found.
type controllerContext struct {
	token    string
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

func (sm *SimManager) GetStateUpdate(token string) (*SimStateUpdate, error) {
	sm.mu.Lock(sm.lg)
	session, ok := sm.sessionsByToken[token]
	if !ok {
		sm.mu.Unlock(sm.lg)
		return nil, ErrNoSimForControllerToken
	}
	sm.mu.Unlock(sm.lg)

	return session.GetStateUpdate(token)
}

// SimStateUpdate wraps sim.StateUpdate and adds server-specific fields.
type SimStateUpdate struct {
	sim.StateUpdate

	ActiveTCWs []sim.TCW
	Events     []sim.Event

	// Error from the sim, generally in response to a user command;
	// reserve the RPC error return value for legit RPC/network errors.
	SimErrorMessage string
}

// Apply applies the update to the state, including server-specific fields.
// The caller is responsible for delivering su.Events to consumers.
func (su *SimStateUpdate) Apply(state *SimState) {
	// Make sure the generation index is above the current index so that if
	// updates are returned out of order we ignore stale ones.
	if state.GenerationIndex < su.GenerationIndex {
		state.DynamicState = su.DynamicState
		state.DerivedState = su.DerivedState
	}

	state.ActiveTCWs = su.ActiveTCWs
	state.FlightStripACIDs = su.FlightStripACIDs
}

// GetStateUpdate fills in a server.SimStateUpdate with both sim state and human controllers.
func (c *controllerContext) GetStateUpdate() SimStateUpdate {
	// Re-validate the connection under the session lock before pulling events:
	// the client may have signed off (via SimManager.SignOff or CullIdleControllers)
	// between LookupController and now, in which case c.eventSub.Unsubscribe()
	// has already run.
	var events []sim.Event
	c.session.mu.Lock(c.session.lg)
	if _, ok := c.session.connectionsByToken[c.token]; ok {
		events = c.eventSub.Get()
	}
	c.session.mu.Unlock(c.session.lg)

	return SimStateUpdate{
		StateUpdate: c.sim.GetStateUpdate(c.tcw),
		ActiveTCWs:  c.session.GetActiveTCWs(),
		Events:      c.sim.PrepareRadioTransmissionsForTCW(c.tcw, events),
	}
}

const GetSerializeSimJSONRPC = "SimManager.GetSerializeSimJSON"

func (sm *SimManager) GetSerializeSimJSON(token string, s *[]byte) error {
	defer sm.lg.CatchAndReportCrash()

	c := sm.LookupController(token)
	if c == nil {
		return ErrNoSimForControllerToken
	}

	sm.mu.Lock(sm.lg)
	defer sm.mu.Unlock(sm.lg)

	var err error
	*s, err = c.sim.GetSerializeSimJSON()
	return err
}

///////////////////////////////////////////////////////////////////////////
// Weather

func (sm *SimManager) GetPrecipURL(args wx.PrecipURLArgs, result *wx.PrecipURL) error {
	defer sm.lg.CatchAndReportCrash()

	provider := sm.getWXProvider()

	var err error
	result.URL, result.NextTime, err = provider.GetPrecipURL(args.Facility, args.Time)
	return err
}

func (sm *SimManager) GetAtmosGrid(args wx.GetAtmosArgs, result *wx.GetAtmosResult) error {
	defer sm.lg.CatchAndReportCrash()

	provider := sm.getWXProvider()

	var err error
	result.AtmosByPointSOA, result.Time, result.NextTime, err =
		provider.GetAtmosGrid(args.Facility, args.Time, args.WeatherStation)
	return err
}

///////////////////////////////////////////////////////////////////////////
// Traffic preview

const GetTrafficCountsRPC = "SimManager.GetTrafficCounts"

type TrafficCountsArgs struct {
	Facility     string
	GroupName    string
	ScenarioName string
	StartTime    time.Time
	LaunchConfig sim.LaunchConfig
}

// TrafficCountsResult holds one count per minute over the window
// sim.TrafficCounts covers, which begins sim.TrafficCountsPad before the
// requested start time, plus the same traffic totaled by airport.
type TrafficCountsResult struct {
	Departures        []uint16
	Arrivals          []uint16
	AirportOperations map[av.ICAOAirportCode]int
}

// GetTrafficCounts reports how much published traffic a scenario would fly starting at a given
// time.
//
// Like makeSimConfiguration, this touches no session state: the scenario catalogs are set at
// construction and read-only after it, the flight data cache carries its own lock, and the launch
// config it works from arrived with the request. So it doesn't acquire sm.mu, and several clients
// previewing at once don't wait on each other or on the sims the server is running.
func (sm *SimManager) GetTrafficCounts(args *TrafficCountsArgs, result *TrafficCountsResult) error {
	defer sm.lg.CatchAndReportCrash()

	if err := sm.checkScenarioTrafficSource(args.Facility, args.GroupName, args.ScenarioName,
		args.LaunchConfig.TrafficSource); err != nil {
		return err
	}

	var historical []traffic.Flight
	var err error
	if args.LaunchConfig.TrafficSource == sim.TrafficSourceHistorical {
		historical, err = traffic.ReadFlightDataCellsAround(util.GetResourcesFS(),
			traffic.FlightDataCells(args.LaunchConfig.IFRAirports()), args.StartTime)
		if err != nil {
			return err
		}
	}

	result.Departures, result.Arrivals, result.AirportOperations, err =
		sim.TrafficCounts(&args.LaunchConfig, args.StartTime, historical)
	return err
}

// checkScenarioTrafficSource reports whether the named scenario exists and can be flown with the
// given traffic source. Which sources a scenario offers is the server's to decide: it is the one
// that knows which airline lists, timetables, and flight data are there. Don't take the client's
// word for any of it. The catalogs are init-immutable, so no mutex.
func (sm *SimManager) checkScenarioTrafficSource(facility, groupName, scenarioName string,
	src sim.TrafficSource) error {
	catalog, ok := sm.scenarios.Load().Catalogs[facility][groupName]
	if !ok {
		return ErrInvalidSimConfiguration
	}
	spec, ok := catalog.Scenarios[scenarioName]
	if !ok {
		return ErrInvalidSimConfiguration
	}
	if !slices.Contains(spec.TrafficSources, src) {
		return ErrInvalidTrafficSource
	}
	return nil
}

const ReloadScenarioBriefRPC = "SimManager.ReloadScenarioBrief"

type ReloadScenarioBriefArgs struct {
	Facility string
}

type ReloadScenarioBriefResult struct {
	Markdown string
}

// ReloadScenarioBrief returns the current on-disk contents of the brief
// file for the given facility. It does not parse, validate, cache, or
// propagate the result; the client is responsible for parsing the
// markdown, validating videomap references against its local cache, and
// rendering. Briefs are validated server-side only at startup
// (scenario.Load).
func (sm *SimManager) ReloadScenarioBrief(args ReloadScenarioBriefArgs, result *ReloadScenarioBriefResult) error {
	defer sm.lg.CatchAndReportCrash()

	facility := strings.ToUpper(args.Facility)

	content, err := sm.scenarios.Load().Briefs.LoadBrief(facility)
	if err != nil {
		return fmt.Errorf("failed to load brief for %q: %w", facility, err)
	}
	if content == "" {
		return fmt.Errorf("no brief loaded for facility %q", facility)
	}
	result.Markdown = content
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Admin

type BroadcastMessage struct {
	Password string
	Message  string
}

const BroadcastRPC = "SimManager.Broadcast"

func (sm *SimManager) Broadcast(m *BroadcastMessage, _ *struct{}) error {
	defer sm.lg.CatchAndReportCrash()

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

///////////////////////////////////////////////////////////////////////////
// Crash Reporting

// ReportCrash receives crash reports from clients and logs them.
// This RPC does not require a controller token.
func (sm *SimManager) ReportCrash(report *log.CrashReport, _ *struct{}) error {
	defer sm.lg.CatchAndReportCrash()

	sm.lg.Warn("Received crash report from client",
		slog.String("cpu_model", report.System.CPUModel),
		slog.String("gpu_renderer", report.System.GPURenderer),
		slog.Time("crash_time", report.Timestamp))

	// Save the crash report to disk
	fn := filepath.Join(sm.lg.LogDir, "client-crash-"+report.Timestamp.Format(time.RFC3339)+".txt")
	if err := os.WriteFile(fn, []byte(report.Report), 0o600); err != nil {
		sm.lg.Errorf("Failed to write crash report: %v", err)
	}

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Whisper Benchmark Reporting

// WhisperBenchmarkResult contains benchmark results for a single model.
type WhisperBenchmarkResult struct {
	ModelName string
	LatencyMs int64
	Status    string // "selected", "acceptable", "too_slow", "failed", "skipped"
}

// WhisperBenchmarkReport contains the full benchmark results from a client.
type WhisperBenchmarkReport struct {
	DeviceName    string // GPU/device description from whisper.ProcessorDescription()
	SelectedModel string
	Results       []WhisperBenchmarkResult
}

const ReportWhisperBenchmarkRPC = "SimManager.ReportWhisperBenchmark"

// ReportWhisperBenchmark receives whisper benchmark results from clients and logs them.
func (sm *SimManager) ReportWhisperBenchmark(report *WhisperBenchmarkReport, _ *struct{}) error {
	defer sm.lg.CatchAndReportCrash()

	sm.lg.Info("Received whisper benchmark report", slog.Any("report", *report))

	return nil
}

///////////////////////////////////////////////////////////////////////////
// STT Log Reporting

// STTLogArgs contains STT command data for logging (sent by local sim clients).
type STTLogArgs struct {
	Callsign          string
	Commands          string
	WhisperDuration   time.Duration
	AudioDuration     time.Duration
	WhisperTranscript string
	WhisperPrompt     string
	WhisperProcessor  string
	WhisperModel      string
	AircraftContext   map[string]stt.Aircraft
	STTDebugLogs      []string
}

const ReportSTTLogRPC = "SimManager.ReportSTTLog"

// ReportSTTLog receives STT command data from local sim clients and logs it.
func (sm *SimManager) ReportSTTLog(args *STTLogArgs, _ *struct{}) error {
	defer sm.lg.CatchAndReportCrash()

	sm.lg.Info("STT command",
		slog.String("transcript", args.WhisperTranscript),
		slog.String("whisper_prompt", args.WhisperPrompt),
		slog.Float64("whisper_duration_ms", float64(args.WhisperDuration.Microseconds())/1000.0),
		slog.Float64("audio_duration_ms", float64(args.AudioDuration.Microseconds())/1000.0),
		slog.String("processor", args.WhisperProcessor),
		slog.String("whisper_model", args.WhisperModel),
		slog.String("callsign", args.Callsign),
		slog.String("command", args.Commands),
		slog.Any("stt_aircraft", args.AircraftContext),
		slog.Any("logs", args.STTDebugLogs))

	return nil
}

///////////////////////////////////////////////////////////////////////////
// Reloading scenarios

// ReloadScenariosArgs names the override files to load alongside the
// built-in ones. They replace whatever the server was launched with, so
// that a tool offering a file picker doesn't have to be restarted for the
// choice to take effect.
type ReloadScenariosArgs struct {
	Overrides scenario.OverrideFiles
}

type ReloadScenariosResult struct {
	// Errors holds the validation errors from the attempted load. When it
	// is non-empty nothing was replaced and the scenarios in use are
	// unchanged.
	Errors []string
	// OverrideErrors are the errors from the override files, which are
	// non-fatal: the rest of the scenarios are reloaded without them.
	OverrideErrors string
	// Catalogs is the reloaded set, so a caller can refresh its scenario
	// list without a second round trip.
	Catalogs map[string]map[string]*scenario.Catalog
}

const ReloadScenariosRPC = "SimManager.ReloadScenarios"

// ReloadScenarios re-reads the scenario and facility configuration files
// from disk and, if they all validate, swaps them in for subsequent sims.
// Sims that are already running keep the scenario they were created with.
func (sm *SimManager) ReloadScenarios(args *ReloadScenariosArgs, result *ReloadScenariosResult) error {
	db.ReloadDB()

	var e util.ErrorLogger
	tables, overrideErrors := scenario.Load(args.Overrides, &e, sm.lg)

	if e.HaveErrors() {
		result.Errors = slices.Collect(e.Errors())
		return nil
	}

	sm.scenarios.Store(tables)

	result.OverrideErrors = overrideErrors
	result.Catalogs = tables.Catalogs
	sm.lg.Infof("Reloaded scenarios")
	return nil
}
