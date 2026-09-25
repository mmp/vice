// server/session.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/simlog"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/vmihailenco/msgpack/v5"
)

///////////////////////////////////////////////////////////////////////////
// Types and Constructors

type simSession struct {
	name          string
	scenarioGroup string
	scenario      string
	// sim is not safe for concurrent use. Once the session is running, it is
	// only used with mu held, through apply and withSim, and nothing taken
	// out from under mu may alias its state: an RPC reply is encoded after
	// the lock is released.
	sim      *sim.Sim
	password string
	// connectionsByToken holds the controllers connected to the sim. Like
	// the SimManager's session tables, it is guarded by SimManager.mu.
	connectionsByToken map[string]*connectionState

	// mu is the sim's lock. It is held while the sim ticks and while a
	// request changes or reads it, so that each request happens between two
	// ticks and the session log records it at the sim time it took effect.
	// SimManager.mu may be held while acquiring it, never the reverse.
	mu util.LoggingMutex
	// log is the session log, if the server is writing one for the
	// session. It is only accessed with mu held.
	log *simlog.Writer
	// request is the record of the request being applied, as far as the
	// request itself fills it in: what it read from outside the sim and
	// the aircraft it turned out to be about. If the session is a replay,
	// replayInputs holds what the request read when the session made it.
	// Both are only accessed with mu held.
	request      simlog.Request
	replaying    bool
	replayInputs []msgpack.RawMessage

	lg *log.Logger
}

func makeSimSession(name, scenarioGroup, scenario, password string, s *sim.Sim, lg *log.Logger) *simSession {
	if name != "" {
		lg = lg.With(slog.String("sim_name", name))
	}
	return &simSession{
		name:               name,
		scenarioGroup:      scenarioGroup,
		scenario:           scenario,
		sim:                s,
		password:           password,
		lg:                 lg,
		connectionsByToken: make(map[string]*connectionState),
	}
}

func makeLocalSimSession(s *sim.Sim, lg *log.Logger) *simSession {
	return makeSimSession("", "", "", "", s, lg)
}

// connectionState holds state for a single human's connection to a sim at a TCW.
type connectionState struct {
	token               string
	tcw                 sim.TCW
	initials            string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
	stateUpdateEventSub *sim.EventsSubscription

	// lastSentGen is the most recent publication generation that has been
	// delivered to this client via GetStateUpdate. The long-poll waits for
	// the sim's pubGen to advance past this value.
	lastSentGen uint64
}

///////////////////////////////////////////////////////////////////////////
// Changing the sim

// apply runs f, which carries out the request an RPC made on behalf of the
// controller at tcw, between two of the sim's ticks, and returns the sim's
// reason for refusing the request. Without the step mutex, a tick could land
// partway through a request, such as between two of the commands in one
// transmission. The session log records the request under the RPC's method
// with its arguments, which is how a replay makes it again (see Replay).
func (ss *simSession) apply(tcw sim.TCW, method string, args any, f func() error) error {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	t := ss.sim.SimTime()
	ss.request = simlog.Request{}
	err := f()
	ss.recordRequest(t, tcw, method, args, err)
	return err
}

func (ss *simSession) recordRequest(t sim.Time, tcw sim.TCW, method string, args any, err error) {
	if ss.log == nil {
		return
	}

	r := ss.request
	r.Time, r.TCW, r.Method = t.Time(), string(tcw), method
	if args != nil {
		args = withoutToken(args)
		b, encErr := msgpack.Marshal(args)
		if encErr != nil {
			ss.lg.Errorf("%s: unable to record request: %v", method, encErr)
			return
		}
		r.Args = b
	}
	aircraft, summary := describeRequest(method, args)
	r.Aircraft = cmp.Or(r.Aircraft, aircraft)
	r.Summary = summary
	if err != nil {
		r.Error = err.Error()
	}
	ss.log.Request(r)
}

// recordInput returns what read returns: something the request being
// applied reads from outside the sim, like the resources. The session log
// records it with the request, and a replay of the request gets the recorded
// value back instead of reading it, so that it doesn't depend on what the
// resources hold by then.
func recordInput[T any](ss *simSession, read func() T) T {
	var v T
	if ss.replaying && len(ss.replayInputs) > 0 {
		if err := msgpack.Unmarshal(ss.replayInputs[0], &v); err != nil {
			ss.lg.Errorf("recorded input: %v", err)
		}
		ss.replayInputs = ss.replayInputs[1:]
	} else {
		if ss.replaying {
			ss.lg.Errorf("the replayed request reads something the session's didn't")
		}
		v = read()
	}

	if ss.log != nil {
		if b, err := msgpack.Marshal(v); err != nil {
			ss.lg.Errorf("unable to record input: %v", err)
		} else {
			ss.request.Inputs = append(ss.request.Inputs, b)
		}
	}
	return v
}

// recordAircraft names the aircraft the request being applied turned out to
// be about, for a request whose arguments don't name one.
func (ss *simSession) recordAircraft(callsign av.ADSBCallsign) {
	ss.request.Aircraft = string(callsign)
}

// withoutToken returns a copy of an RPC's arguments with the controller's
// token cleared: the log records what a request asked for, not who asked.
func withoutToken(args any) any {
	v := reflect.ValueOf(args)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return args
	}
	c := reflect.New(v.Elem().Type())
	c.Elem().Set(v.Elem())
	if f := c.Elem().FieldByName("ControllerToken"); f.IsValid() {
		f.SetString("")
	}
	return c.Interface()
}

// describeRequest names the aircraft an RPC's arguments say a request is
// about and summarizes the request for someone reading a session log: the
// method and the arguments' other simple fields, or for aircraft commands,
// the commands.
func describeRequest(method string, args any) (aircraft, summary string) {
	summary = strings.TrimPrefix(method, "Sim.")
	v := reflect.Indirect(reflect.ValueOf(args))
	if v.Kind() != reflect.Struct {
		return "", summary
	}

	var fields []string
	for sf, f := range v.Fields() {
		name := sf.Name
		switch {
		case !sf.IsExported() || f.IsZero():
		case name == "Callsign" || name == "ACID" || name == "TrackCallsign":
			aircraft = cmp.Or(aircraft, fmt.Sprint(f.Interface()))
		case f.Kind() == reflect.Struct:
			// A launch's flight, for one, names its aircraft.
			if cs := f.FieldByName("Callsign"); cs.IsValid() && !cs.IsZero() {
				aircraft = cmp.Or(aircraft, fmt.Sprint(cs.Interface()))
			}
		case f.Kind() == reflect.String || f.Kind() == reflect.Bool || f.CanInt() || f.CanUint() || f.CanFloat():
			fields = append(fields, fmt.Sprintf("%s=%v", name, f.Interface()))
		}
	}

	if method == RunAircraftCommandsRPC {
		return aircraft, v.FieldByName("Commands").String()
	}
	return aircraft, strings.Join(append([]string{summary}, fields...), " ")
}

// withSim runs f with the sim held, so that no tick or request runs while it
// does. Unlike apply, it doesn't record f in the session log: it is for what
// a replay doesn't make again, whether reading the sim or running it forward,
// where the ticks f runs record themselves.
func (ss *simSession) withSim(f func()) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	f()
}

// startLog starts recording the session in w, which already holds the
// snapshot the sim started from.
func (ss *simSession) startLog(w *simlog.Writer) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	ss.log = w
	ss.sim.SetSessionLog(w)
}

// closeLog stops recording the session and finishes its log.
func (ss *simSession) closeLog() {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	if ss.log == nil {
		return
	}
	ss.sim.SetSessionLog(nil)
	if err := ss.log.Close(); err != nil {
		ss.lg.Errorf("session log: %v", err)
	}
	ss.log = nil
}

///////////////////////////////////////////////////////////////////////////
// Controller Lifecycle

// addHumanController connects a controller to the sim at tcw. The caller
// holds SimManager.mu.
func (ss *simSession) addHumanController(token string, tcw sim.TCW, initials string,
	sub *sim.EventsSubscription) {
	ss.connectionsByToken[token] = &connectionState{
		token:               token,
		tcw:                 tcw,
		initials:            initials,
		lastUpdateCall:      time.Now(),
		stateUpdateEventSub: sub,
	}

	// Update pause state - may unpause sim now that a human is connected
	ss.updateSimPauseState()
}

type signOffResult struct {
	TCW        sim.TCW
	Initials   string
	UsersAtTCW int
}

// signOff disconnects the controller with the given token and reports how
// many others remain at its TCW. The caller holds SimManager.mu.
func (ss *simSession) signOff(token string) (signOffResult, bool) {
	conn, ok := ss.connectionsByToken[token]
	if !ok {
		return signOffResult{}, false
	}

	result := signOffResult{
		TCW:      conn.tcw,
		Initials: conn.initials,
	}

	// Unsubscribe from events before deleting
	if conn.stateUpdateEventSub != nil {
		conn.stateUpdateEventSub.Unsubscribe()
	}

	delete(ss.connectionsByToken, token)

	// Count remaining users at this TCW
	for _, c := range ss.connectionsByToken {
		if c.tcw == result.TCW {
			result.UsersAtTCW++
		}
	}

	// Update pause state - may pause sim if no humans remain
	ss.updateSimPauseState()

	return result, true
}

// updateSimPauseState pauses the sim if no humans are connected, unpauses if at least one.
// The caller holds SimManager.mu.
func (ss *simSession) updateSimPauseState() {
	hasHumans := util.SeqContainsFunc(maps.Values(ss.connectionsByToken),
		func(conn *connectionState) bool { return conn.tcw != "" })
	ss.withSim(func() { ss.sim.SetPausedByServer(!hasHumans) })
}

///////////////////////////////////////////////////////////////////////////
// State Updates and Controller Context

// State-update timing constants.
const (
	// StateUpdateMaxWait is the bound past which we assume the publish loop has stalled, in which
	// case waitForStateUpdate returns ErrSimPublishStalled and the client surfaces a
	// connection-lost status event.
	StateUpdateMaxWait = 2 * time.Second

	// StateUpdateWarn and StateUpdateKick are the lastUpdateCall ages at which the server
	// respectively warns the other controllers and signs an unresponsive client off; they must
	// exceed StateUpdateMaxWait so a healthy long-poll cycle doesn't trip them.
	StateUpdateWarn = 5 * time.Second
	StateUpdateKick = 15 * time.Second
)

// waitForStateUpdate returns the sim's state for the controller at tcw once
// the sim has published a generation past sinceGen, the last one the
// controller was sent. The sim publishes at least every 1.1 seconds, so if
// StateUpdateMaxWait passes without one its update loop has stalled, and
// rather than deliver a stale snapshot this reports ErrSimPublishStalled.
func (ss *simSession) waitForStateUpdate(tcw sim.TCW, sinceGen uint64) (sim.StateUpdate, error) {
	var update sim.StateUpdate
	var published, done <-chan struct{}
	current := false
	ss.withSim(func() {
		var gen uint64
		gen, published = ss.sim.Publication()
		done = ss.sim.Done()
		if current = gen > sinceGen; current {
			update = ss.sim.GetStateUpdate(tcw)
		}
	})
	if current {
		return update, nil
	}

	select {
	case <-published:
	case <-done:
	case <-time.After(StateUpdateMaxWait):
		return sim.StateUpdate{}, ErrSimPublishStalled
	}

	ss.withSim(func() { update = ss.sim.GetStateUpdate(tcw) })
	return update, nil
}

// makeControllerContext returns a ControllerContext for the given token, or nil if not found.
// The caller holds SimManager.mu.
func (ss *simSession) makeControllerContext(token string) *controllerContext {
	conn, ok := ss.connectionsByToken[token]
	if !ok {
		return nil
	}
	return &controllerContext{
		token:    token,
		tcw:      conn.tcw,
		initials: conn.initials,
		sim:      ss.sim,
		eventSub: conn.stateUpdateEventSub,
		session:  ss,
	}
}

///////////////////////////////////////////////////////////////////////////
// Position/TCW State Queries (for GetRunningSims)

// getCurrentConsolidation returns the sim's consolidation with the initials of
// the controllers signed in at each TCW. The caller holds SimManager.mu.
func (ss *simSession) getCurrentConsolidation() map[sim.TCW]TCPConsolidation {
	tcwInitials := make(map[sim.TCW][]string)
	for _, conn := range ss.connectionsByToken {
		tcwInitials[conn.tcw] = append(tcwInitials[conn.tcw], conn.initials)
	}

	var current map[sim.TCW]*sim.TCPConsolidation
	ss.withSim(func() { current = ss.sim.GetCurrentConsolidation() })

	// Get consolidation from sim and add initials
	consolidation := make(map[sim.TCW]TCPConsolidation)
	for tcw, cons := range current {
		consolidation[tcw] = TCPConsolidation{
			TCPConsolidation: *cons,
			Initials:         tcwInitials[tcw],
		}
	}

	return consolidation
}

// getActiveTCWs returns the sorted set of TCWs that have at least one human
// signed in. The caller holds SimManager.mu.
func (ss *simSession) getActiveTCWs() []sim.TCW {
	var tcws []string
	for _, conn := range ss.connectionsByToken {
		if conn.tcw != "" {
			tcws = append(tcws, string(conn.tcw))
		}
	}
	slices.Sort(tcws)
	tcws = slices.Compact(tcws) // may have multiple connections to a TCW...
	return util.MapSlice(tcws, func(tcw string) sim.TCW { return sim.TCW(tcw) })
}

// RequestContact pops the next pending contact for the TCW, generates the transmission
// with current aircraft state, and returns text + voice name for client-side synthesis.
// Returns empty values if no contact is pending.
func (ss *simSession) RequestContact(tcw sim.TCW) (text string, voiceName string, callsign av.ADSBCallsign, ty speech.RadioTransmissionType) {
	// Get all positions controlled by this TCW (primary + consolidated secondaries)
	positions := ss.sim.GetPositionsForTCW(tcw)
	if len(positions) == 0 {
		return "", "", "", 0
	}

	// Try pending contacts from any of the controlled positions
	for {
		pc := ss.sim.PopReadyContact(positions)
		if pc == nil {
			return "", "", "", 0
		}

		// Generate the contact transmission with current aircraft state
		spokenText, _ := ss.sim.GenerateContactTransmission(pc)
		if spokenText == "" {
			// Aircraft may be gone or invalid - try the next one
			continue
		}

		voiceName := ss.sim.GetReadbackVoice(pc.ADSBCallsign)

		return spokenText, voiceName, pc.ADSBCallsign, speech.RadioTransmissionContact
	}
}

// Replay runs a recorded session again with the code as it is now, writing a
// log of the replay to w as it goes so that it can be compared with the
// session's. The replay decodes the snapshot the session started from, signs
// controllers on and off when the log records that they did, and makes each
// request the log records at the tick it records it, by calling the same RPC
// method the session's controller did with the same arguments. It installs
// the weather the session's model did when the session did. The static
// database the session ran on must already be installed as db.DB.
func Replay(sess *simlog.Session, w *simlog.Writer, lg *log.Logger) error {
	s, err := sim.DecodeSnapshot(sess.Snapshot)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	model := wx.MakeCalmModel()
	s.SetWeatherModel(model)
	s.Activate(lg, nil)
	defer s.Destroy()

	ss := makeSimSession("", sess.Header.ScenarioGroup, sess.Header.Scenario, "", s, lg)
	ss.replaying = true
	ss.startLog(w)

	rp := replayer{
		sd: &dispatcher{sm: &SimManager{
			sessionsByName:  map[string]*simSession{"": ss},
			sessionsByToken: make(map[string]*simSession),
			lg:              lg,
		}},
		ss:     ss,
		tokens: make(map[sim.TCW]string),
	}

	for _, e := range sess.Events {
		switch e.Kind {
		case simlog.KindWeather:
			model.Install(e.Weather.Update)
			w.Weather(*e.Weather)

		case simlog.KindRequest:
			if err := rp.request(e.Request); err != nil {
				return err
			}

		case simlog.KindTick:
			ss.withSim(func() { s.Step(time.Second) })
			if t := s.SimTime().Time(); !t.Equal(e.Tick.Time) {
				return fmt.Errorf("replay reached %s at a tick the session reached %s", t.UTC(), e.Tick.Time.UTC())
			}
		}
	}
	return nil
}

// replayer makes the requests a session log records again.
type replayer struct {
	sd     *dispatcher
	ss     *simSession
	tokens map[sim.TCW]string // for the controller signed on at each TCW
}

// request makes the request r records again on behalf of the controller
// signed on at the TCW that made it. Whether the sim refuses it this time
// goes into the replay's log.
func (rp *replayer) request(r *simlog.Request) error {
	if t := rp.ss.sim.SimTime().Time(); !t.Equal(r.Time) {
		return fmt.Errorf("%s: recorded at %s but replayed at %s", r.Method, r.Time.UTC(), t.UTC())
	}
	rp.ss.mu.Lock(rp.ss.lg)
	rp.ss.replayInputs = r.Inputs
	rp.ss.mu.Unlock(rp.ss.lg)

	tcw := sim.TCW(r.TCW)
	switch r.Method {
	case signOnMethod:
		var args signOnArgs
		if err := msgpack.Unmarshal(r.Args, &args); err != nil {
			return fmt.Errorf("%s: %w", r.Method, err)
		}
		sm := rp.sd.sm
		sm.mu.Lock(sm.lg)
		defer sm.mu.Unlock(sm.lg)
		req := &JoinSimRequest{TCW: tcw, SelectedTCPs: args.TCPs, Privileged: args.Privileged}
		if token, sub, err := sm.signOn(rp.ss, req); err == nil {
			rp.ss.addHumanController(token, tcw, "", sub)
			sm.sessionsByToken[token] = rp.ss
			rp.tokens[tcw] = token
		}
		return nil

	case signOffMethod:
		token := rp.tokens[tcw]
		delete(rp.tokens, tcw)
		return rp.sd.sm.SignOff(token)
	}

	token, ok := rp.tokens[tcw]
	if !ok {
		return fmt.Errorf("%s: made at %s, where no controller is signed on", r.Method, r.TCW)
	}
	m, ok := reflect.TypeFor[*dispatcher]().MethodByName(strings.TrimPrefix(r.Method, "Sim."))
	if !ok {
		return fmt.Errorf("%s: session log records an unknown request", r.Method)
	}
	arg := reflect.ValueOf(token) // for the RPCs whose argument is the token
	if t := m.Type.In(1); t.Kind() != reflect.String {
		arg = reflect.New(t.Elem())
		if err := msgpack.Unmarshal(r.Args, arg.Interface()); err != nil {
			return fmt.Errorf("%s: %w", r.Method, err)
		}
		arg.Elem().FieldByName("ControllerToken").SetString(token)
	}
	m.Func.Call([]reflect.Value{reflect.ValueOf(rp.sd), arg, reflect.New(m.Type.In(2).Elem())})
	return nil
}

///////////////////////////////////////////////////////////////////////////
// Session logging

// The server records each session it runs in a session log (see package
// simlog), in the simlogs directory under its log directory. A local server
// keeps only the most recent few of them; a public one keeps them all.

// localSessionLogs is how many session logs a local server keeps.
const localSessionLogs = 5

// sessionLogDir returns the directory session logs go in, or "" if the
// server doesn't write them, as when a test runs it without a log directory.
func (sm *SimManager) sessionLogDir() string {
	if sm.lg.LogDir == "" {
		return ""
	}
	return filepath.Join(sm.lg.LogDir, "simlogs")
}

// saveDatabase writes the static database sims are running on to the session
// log directory and returns its hash. Hashing the database takes a fraction
// of a second, so the result is kept until the database is reloaded.
func (sm *SimManager) saveDatabase() (string, error) {
	dir := sm.sessionLogDir()
	if dir == "" {
		return "", nil
	}

	sm.dbSnapshotMu.Lock()
	defer sm.dbSnapshotMu.Unlock()

	if sm.dbSnapshotDB == db.DB {
		return sm.dbSnapshotHash, nil
	}
	hash, err := simlog.SaveDatabase(dir, db.DB)
	if err != nil {
		return "", err
	}
	sm.dbSnapshotDB, sm.dbSnapshotHash = db.DB, hash
	return hash, nil
}

// createSessionLog creates the log for a session whose sim starts from
// snapshot and runs on the database with the given hash. It returns nil if
// the server doesn't write logs or if the log couldn't be created, in which
// case the session runs without one.
func (sm *SimManager) createSessionLog(ss *simSession, dbHash string, snapshot []byte) *simlog.Writer {
	dir := sm.sessionLogDir()
	if dir == "" {
		return nil
	}

	if sm.local {
		// Make room for the new one.
		pruneSessionLogs(dir, localSessionLogs-1, dbHash, sm.lg)
	}

	start := time.Now().UTC()
	h := simlog.Header{
		Facility:      ss.sim.Facility(),
		ScenarioGroup: ss.scenarioGroup,
		Scenario:      ss.sim.State.ScenarioName,
		Start:         start,
		SimStart:      ss.sim.SimTime().Time(),
		Database:      dbHash,
		GOARCH:        runtime.GOARCH,
		Revision:      simlog.Revision(),
	}

	name := start.Format("2006-01-02T15-04-05Z") + "_" + fileNameComponent(h.Facility) + "_" +
		fileNameComponent(h.Scenario)
	for i := 1; ; i++ {
		path := filepath.Join(dir, name+".simlog")
		if i > 1 {
			path = filepath.Join(dir, fmt.Sprintf("%s-%d.simlog", name, i))
		}
		w, err := simlog.Create(path, h, snapshot)
		if errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			sm.lg.Errorf("unable to create session log: %v", err)
			return nil
		}
		sm.lg.Infof("%s: recording session to %s", ss.name, path)
		return w
	}
}

var reFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fileNameComponent makes s safe to use in a file name.
func fileNameComponent(s string) string {
	return strings.Trim(reFileNameUnsafe.ReplaceAllString(s, "-"), "-")
}

// pruneSessionLogs deletes all but the most recent keep logs in dir, and then
// the databases that neither the logs left nor the session about to start,
// which runs on the database with hash dbHash, refer to.
func pruneSessionLogs(dir string, keep int, dbHash string, lg *log.Logger) {
	logs, err := filepath.Glob(filepath.Join(dir, "*.simlog"))
	if err != nil {
		lg.Errorf("%s: %v", dir, err)
		return
	}
	// The names start with the time the session started.
	slices.Sort(logs)
	if len(logs) > keep {
		for _, path := range logs[:len(logs)-keep] {
			if err := os.Remove(path); err != nil {
				lg.Errorf("%v", err)
			}
		}
		logs = logs[len(logs)-keep:]
	}

	inUse := map[string]bool{dbHash: true}
	for _, path := range logs {
		if h, err := simlog.ReadHeader(path); err == nil {
			inUse[h.Database] = true
		}
	}
	databases, _ := filepath.Glob(simlog.DatabasePath(dir, "*"))
	for _, path := range databases {
		if hash := strings.TrimSuffix(filepath.Base(path), ".db"); !inUse[hash] {
			if err := os.Remove(path); err != nil {
				lg.Errorf("%v", err)
			}
		}
	}
}
