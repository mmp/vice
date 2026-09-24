// wx/model.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"sync"
	"time"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

// Model gives the atmospheric conditions a sim flies in. It interpolates
// between two atmospheric grids that bracket the sim's time. It has its
// first grid when it is made and fetches the next one in the background as
// the sim's clock nears the end of the pair. Those take effect only when the
// sim calls Advance, so a run's weather depends on its ticks and not on when
// fetches happen to finish.
type Model struct {
	provider *Provider
	facility string
	station  string

	grids     [2]*AtmosGrid
	updates   [2]AtmosUpdate // what each grid was built from
	nextFetch time.Time
	ch        <-chan atmosFetch

	mu sync.Mutex
	lg *log.Logger
}

// AtmosUpdate is an atmospheric grid for a Model to install, in the form it
// was fetched in: Time is the time it is valid for, and NextTime is when the
// next grid in the series is.
type AtmosUpdate struct {
	Atmos    *AtmosByPointSOA
	Time     time.Time
	NextTime time.Time
}

type atmosFetch struct {
	update AtmosUpdate
	grid   *AtmosGrid
	err    error
}

// MakeCalmModel returns a Model with no weather data, so all lookups return
// the standard atmosphere with no wind until updates are installed in it.
func MakeCalmModel() *Model {
	return &Model{}
}

func MakeModel(provider *Provider, facility string, station string, startTime time.Time, lg *log.Logger) *Model {
	m := &Model{
		facility: facility,
		station:  station,
		lg:       lg,
	}
	if !db.DB.IsFacility(facility) {
		return m
	}

	m.provider = provider
	if provider != nil {
		// The first grid can't wait for a tick to pick it up: what the sim
		// sets up before its first tick, like which runway VFRs use,
		// depends on the wind. The provider bounds the wait by falling back
		// to the bundled resources when its backend is slow to answer.
		m.installFetched(m.fetch(startTime))
	}

	return m
}

// fetch gets the grid for time t from the provider and builds it.
func (m *Model) fetch(t time.Time) atmosFetch {
	atmos, atmosTime, nextTime, err := m.provider.GetAtmosGrid(m.facility, t, m.station)
	f := atmosFetch{
		update: AtmosUpdate{Atmos: atmos, Time: atmosTime, NextTime: nextTime},
		err:    err,
	}
	if err == nil && atmos != nil {
		f.grid = atmos.ToAOS().GetGrid()
	}
	return f
}

// fetchAtmos fetches the grid for time t in the background, so that the
// sim's update doesn't stall on the fetch or on building the grid.
func (m *Model) fetchAtmos(t time.Time) <-chan atmosFetch {
	if m.provider == nil {
		return nil
	}

	ch := make(chan atmosFetch, 1)
	go func() {
		defer close(ch)
		ch <- m.fetch(t)
	}()

	return ch
}

func (m *Model) Lookup(p math.Point2LL, alt float32, t time.Time) Sample {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.grids[0] == nil {
		return MakeStandardSampleForAltitude(alt)
	}

	s0, ok0 := m.grids[0].Lookup(p, alt)
	s1, ok1 := m.grids[1].Lookup(p, alt)
	if !ok0 || m.updates[0].Time.Equal(m.updates[1].Time) {
		return s1
	} else if !ok1 {
		return s0
	} else {
		delta := t.Sub(m.updates[0].Time).Seconds() / m.updates[1].Time.Sub(m.updates[0].Time).Seconds()
		delta = math.Clamp(delta, 0, 1)
		return LerpSample(float32(delta), s0, s1)
	}
}

// Advance is called by the sim once each tick, t being the sim's time. It
// starts fetching the next grid once t passes the later of the two current
// ones, and it installs a grid whose fetch has finished, returning it; it
// returns nil if it installed none.
func (m *Model) Advance(t time.Time) *AtmosUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.updates[1].Time.IsZero() && t.After(m.updates[1].Time) && !m.nextFetch.IsZero() && m.ch == nil {
		m.ch = m.fetchAtmos(m.nextFetch)
	}

	select {
	case f := <-m.ch:
		m.ch = nil
		return m.installFetched(f)
	default:
		return nil
	}
}

// installFetched installs the grid a fetch got, if it got one, returning
// the update it was built from.
func (m *Model) installFetched(f atmosFetch) *AtmosUpdate {
	if f.err != nil || f.grid == nil {
		if f.err != nil {
			m.lg.Errorf("%v", f.err)
		}
		// Carry on with the grids we have rather than asking again
		// every tick.
		m.nextFetch = time.Time{}
		return nil
	}
	m.install(f.update, f.grid)
	return &f.update
}

// Install installs a grid a replayed session's model installed.
func (m *Model) Install(u AtmosUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.install(u, u.Atmos.ToAOS().GetGrid())
}

// Installed returns the updates that, installed in order in a new model,
// give it the grids this one has.
func (m *Model) Installed() []AtmosUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.grids[0] == nil {
		return nil
	} else if m.grids[0] == m.grids[1] {
		return []AtmosUpdate{m.updates[1]}
	}
	return []AtmosUpdate{m.updates[0], m.updates[1]}
}

func (m *Model) install(u AtmosUpdate, grid *AtmosGrid) {
	// Shift down to make room for the new one in [1].
	m.grids[0], m.updates[0] = m.grids[1], m.updates[1]
	m.grids[1], m.updates[1] = grid, u
	m.nextFetch = u.NextTime

	if m.grids[0] == nil {
		// We just got the very first one; copy it into [0] for now so
		// code elsewhere can assume that either none or both are
		// present.
		m.grids[0], m.updates[0] = m.grids[1], m.updates[1]

		// And get started on fetching the next one, when the series has
		// one: a time past the end of it comes back with no next time,
		// and fetching that would ask for the zero time and fail.
		if !m.nextFetch.IsZero() {
			m.ch = m.fetchAtmos(m.nextFetch)
		}
	}
}
