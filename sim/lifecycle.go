// sim/lifecycle.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (s *Sim) Activate(lg *log.Logger, provider *wx.Provider) {
	s.lg = lg
	s.lastSTTCommands = make(map[TCW]*lastSTTCommand)

	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}

	if s.pubCh == nil {
		s.pubCh = make(chan struct{})
	}
	// Continue the publication-generation series from where the saved state left off.
	if s.pubGen == 0 && s.State.GenerationIndex > 0 {
		s.pubGen = uint64(s.State.GenerationIndex)
	}
	if s.simDoneCh == nil {
		s.simDoneCh = make(chan struct{})
	}

	now := time.Now()
	s.lastSimUpdateTime = now
	s.lastControlCommandTime = now

	if s.Rand == nil {
		s.Rand = rand.Make()
	}

	s.wxProvider = provider
	if s.wxModel == nil {
		s.wxModel = wx.MakeModel(provider, s.State.Facility, string(s.State.FacilityAdaptation.WeatherStation),
			s.State.SimTime.Time(), s.lg)
	}

	// Restore json:"-" fields that are lost during JSON config save/load.
	restoreControllerFields(s.ControlPositions)
	restoreControllerFields(s.State.Controllers)
	restoreERAMCoordinationGeometry(s.State.ERAMCoordination, lg)
}

// restoreERAMCoordinationGeometry re-derives the json:"-" geometry (zone-area
// centers, restriction lines) that ParseGeometry parses from adapted strings
// at scenario-group load: a saved sim's restore goes through Activate, not
// scenario loading, so without this the geometry would be zero-valued and
// zone_based coordination would compute bearings from the origin.
func restoreERAMCoordinationGeometry(ec *enroute.Coordination, lg *log.Logger) {
	if ec == nil || ec.Coord == nil {
		return
	}
	var errs util.ErrorLogger
	enroute.ParseGeometry(map[string]*enroute.ArtsCoordEntry{ec.ComputerID: ec.Coord}, ec.Restrictions, enroute.DBLocator{}, &errs)
	errs.PrintErrors(lg)
}

// restoreControllerFields reconstructs the json:"-" fields
// (FacilityIdentifier, ERAMFacility, Area) on controllers from the
// map key and Position. These fields are excluded from JSON
// serialization to prevent them from appearing in facility config
// files, but they need to be restored after loading a saved sim.
func restoreControllerFields(controllers map[TCP]*av.Controller) {
	for tcp, ctrl := range controllers {
		key := string(tcp)

		// FacilityIdentifier is the prefix: key = FacilityIdentifier + Position
		if len(key) > len(ctrl.Position) {
			ctrl.FacilityIdentifier = key[:len(key)-len(ctrl.Position)]
		}

		// ERAMFacility: ARTCC controllers have 2-digit numeric positions.
		if len(ctrl.Position) == 2 && ctrl.Position[0] >= '0' && ctrl.Position[0] <= '9' &&
			ctrl.Position[1] >= '0' && ctrl.Position[1] <= '9' {
			ctrl.ERAMFacility = true
		}

		// Note: Area is not restored here because it has a proper JSON tag
		// and survives serialization. It's auto-derived (for TRACON) or
		// manually specified (for ERAM) in Finalize/rewriteControllers.
	}
}

func (s *Sim) Destroy() {
	s.eventStream.Destroy()

	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	select {
	case <-s.simDoneCh:
		// already closed
	default:
		close(s.simDoneCh)
	}
}

// Publish makes the caller's state updates available to clients.
func (s *Sim) Publish() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.publish()
}

// publish bumps the publication generation and wakes any parked
// GetStateUpdate waiters. Caller must hold s.mu.
func (s *Sim) publish() {
	s.pubGen++
	if s.pubCh != nil {
		close(s.pubCh)
	}
	s.pubCh = make(chan struct{})
	s.lastPublishTime = time.Now()
}

// snapshotPub returns the current publication generation and the channel
// that will be closed on the next publication. Caller must hold s.mu.
func (s *Sim) snapshotPub() (uint64, chan struct{}) {
	return s.pubGen, s.pubCh
}

// Done returns a channel that is closed when the sim has been destroyed.
// Long-poll waiters select on it to unblock during sim teardown.
func (s *Sim) Done() <-chan struct{} {
	return s.simDoneCh
}

// Subscribe creates a new event subscription for this simulation.
// The caller is responsible for calling Unsubscribe when done.
func (s *Sim) Subscribe() *EventsSubscription {
	return s.eventStream.Subscribe()
}

// GetSerializeSimJSON returns the sim encoded as JSON, for saving in the
// user's configuration file.
func (s *Sim) GetSerializeSimJSON() ([]byte, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return json.Marshal(s)
}

func (s *Sim) LogValue() slog.Value {
	// Only the parts of State that change as the sim runs: the rest is
	// scenario configuration, identical on every line, and it dwarfs
	// everything else here by a factor of thirty.
	return slog.GroupValue(
		slog.Time("sim_time", s.State.SimTime.Time()),
		slog.Float64("sim_rate", float64(s.State.SimRate)),
		slog.Bool("paused", s.State.Paused),
		slog.Int("generation_index", s.State.GenerationIndex),
		slog.Int("aircraft", len(s.Aircraft)),
		slog.Any("departure_state", s.DepartureState),
		slog.Int("scheduled_departures", len(s.Schedule.Departures)),
		slog.Int("scheduled_arrivals", len(s.Schedule.Arrivals)),
		slog.Int("scheduled_overflights", len(s.Schedule.Overflights)),
		slog.Any("automatic_handoffs", s.Handoffs),
		slog.Any("automatic_pointouts", s.PointOuts))
}

// log prints the provided message to stdout and posts it to the clients' event streams so that it
// may be shown in the messages pane.
func (s *Sim) log(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	fmt.Println(text)
	if s.eventStream != nil {
		s.eventStream.Post(Event{Type: SimLogMessageEvent, WrittenText: text})
	}
}

func (s *Sim) TogglePause() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.Paused = !s.State.Paused
	s.lastSimUpdateTime = time.Now() // ignore time passage...
	s.lastControlCommandTime = time.Now()
	s.publish()
}

// SetPausedByServer allows the server to pause/unpause the sim when
// humans connect or disconnect.
func (s *Sim) SetPausedByServer(paused bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if s.pausedByServer == paused {
		return
	}
	s.pausedByServer = paused
	if !paused {
		// Reset time so we don't try to catch up
		s.lastSimUpdateTime = time.Now()
	}
	s.publish()
}

func (s *Sim) FastForward() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for range 15 {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = 0
	s.lastSimUpdateTime = time.Now()
	s.lastControlCommandTime = time.Now()
	s.publish()
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return time.Since(s.lastSimUpdateTime)
}

// SimTime returns the current simulation time.
func (s *Sim) SimTime() Time {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.State.SimTime
}

func (s *Sim) SetSimRate(tcw TCW, rate float32) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.SimRate = rate
	s.lastControlCommandTime = time.Now()

	s.lg.Infof("sim rate set to %f", s.State.SimRate)
	s.publish()
	return nil
}
