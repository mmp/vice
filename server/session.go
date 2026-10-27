// server/session.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"log/slog"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
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
	// Controllers are added and removed with mu held as well, so that
	// signing on and off happens one controller at a time.
	connectionsByToken map[string]*connectionState

	// GetRunningSims lists every session for each client that connects, so
	// it can't wait on mu: a sim that is slow to finish a request would hold
	// up every connection. It reads these instead. The scenario's default
	// consolidation never changes, and consolidation is a copy of the sim's
	// current one that apply refreshes, since only requests change it.
	defaultConsolidation sim.PositionConsolidation
	consolidation        atomic.Pointer[map[sim.TCW]*sim.TCPConsolidation]

	// mu is the sim's lock. It is held while the sim ticks and while a
	// request changes or reads it, so that each request happens between two
	// ticks. It may be held while acquiring SimManager.mu, never the reverse.
	mu util.LoggingMutex

	lg *log.Logger
}

func makeSimSession(name, scenarioGroup, scenario, password string, s *sim.Sim, lg *log.Logger) *simSession {
	if name != "" {
		lg = lg.With(slog.String("sim_name", name))
	}
	ss := &simSession{
		name:                 name,
		scenarioGroup:        scenarioGroup,
		scenario:             scenario,
		sim:                  s,
		password:             password,
		lg:                   lg,
		connectionsByToken:   make(map[string]*connectionState),
		defaultConsolidation: s.DefaultConsolidation(),
	}
	ss.saveConsolidation()
	return ss
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

// apply runs f, which carries out a controller's request, between two of the
// sim's ticks and returns the sim's reason for refusing the request. Without
// the lock, a tick could land partway through a request, such as between two
// of the commands in one transmission.
func (ss *simSession) apply(f func() error) error {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	return ss.applyLocked(f)
}

// applyLocked is apply for a caller that already holds mu.
func (ss *simSession) applyLocked(f func() error) error {
	err := f()
	ss.saveConsolidation()
	return err
}

// saveConsolidation copies the sim's current consolidation for
// getCurrentConsolidation. The caller holds mu, unless the session isn't
// running yet.
func (ss *simSession) saveConsolidation() {
	c := ss.sim.GetCurrentConsolidation()
	ss.consolidation.Store(&c)
}

// withSim runs f with the sim held, so that no tick or request runs while it
// does. Unlike apply, it is for reading the sim or running it forward rather
// than for a request that changes it.
func (ss *simSession) withSim(f func()) {
	ss.mu.Lock(ss.lg)
	defer ss.mu.Unlock(ss.lg)

	f()
}

///////////////////////////////////////////////////////////////////////////
// Controller Lifecycle

// addHumanController connects a controller to the sim at tcw. The caller
// holds mu and SimManager.mu.
func (ss *simSession) addHumanController(token string, tcw sim.TCW, initials string,
	sub *sim.EventsSubscription) {
	ss.connectionsByToken[token] = &connectionState{
		token:               token,
		tcw:                 tcw,
		initials:            initials,
		lastUpdateCall:      time.Now(),
		stateUpdateEventSub: sub,
	}
}

type signOffResult struct {
	TCW        sim.TCW
	Initials   string
	UsersAtTCW int
}

// signOff disconnects the controller with the given token and reports how
// many others remain at its TCW. The caller holds mu and SimManager.mu.
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

	return result, true
}

// hasHumans reports whether any human is connected; the sim is paused when
// none is. The caller holds SimManager.mu.
func (ss *simSession) hasHumans() bool {
	return util.SeqContainsFunc(maps.Values(ss.connectionsByToken),
		func(conn *connectionState) bool { return conn.tcw != "" })
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

	// Add the initials to the consolidation the sim's last request left
	consolidation := make(map[sim.TCW]TCPConsolidation)
	for tcw, cons := range *ss.consolidation.Load() {
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
