// sim/lifecycle.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// SetWeatherModel sets the model the sim flies in; it must be called before
// the sim is activated. A replay gives the sim a model that installs only
// the grids the session's log records.
func (s *Sim) SetWeatherModel(m *wx.Model) {
	s.wxModel = m
}

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
	if s.textRand == nil {
		s.textRand = rand.Make()
	}

	s.wxProvider = provider
	if s.wxModel == nil {
		s.wxModel = wx.MakeModel(provider, s.State.Facility, string(s.State.FacilityAdaptation.WeatherStation),
			s.State.SimTime.Time(), s.lg)
	}

	s.initializeAirspaceGrids()
}

func (s *Sim) Destroy() {
	s.eventStream.Destroy()

	select {
	case <-s.simDoneCh:
		// already closed
	default:
		close(s.simDoneCh)
	}
}

// publish bumps the publication generation and wakes any parked
// GetStateUpdate waiters.
func (s *Sim) publish() {
	s.pubGen++
	if s.pubCh != nil {
		close(s.pubCh)
	}
	s.pubCh = make(chan struct{})
	s.lastPublishTime = time.Now()
}

// Publication returns the current publication generation and the channel
// that will be closed on the next publication. A long-poll for the next state
// update compares the generation with the last one it delivered and, if that
// was the current one, waits on the channel; the sim's Update publishes at
// least every 1.1 seconds, even when paused, so the wait is bounded.
func (s *Sim) Publication() (uint64, <-chan struct{}) {
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
		slog.Any("automatic_handoffs", s.Handoffs))
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

// TogglePause pauses or unpauses the sim and returns whether it is now paused.
func (s *Sim) TogglePause() bool {
	s.State.Paused = !s.State.Paused
	s.lastSimUpdateTime = time.Now() // ignore time passage...
	s.lastControlCommandTime = time.Now()
	s.publish()
	return s.State.Paused
}

// SetPausedByServer allows the server to pause/unpause the sim when
// humans connect or disconnect.
func (s *Sim) SetPausedByServer(paused bool) {
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
	return time.Since(s.lastSimUpdateTime)
}

// SimTime returns the current simulation time.
func (s *Sim) SimTime() Time {
	return s.State.SimTime
}

func (s *Sim) SetSimRate(tcw TCW, rate float32) error {
	s.State.SimRate = rate
	s.lastControlCommandTime = time.Now()

	s.lg.Infof("sim rate set to %f", s.State.SimRate)
	s.publish()
	return nil
}
