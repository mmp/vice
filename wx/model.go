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
	times     [2]time.Time
	nextFetch time.Time
	ch        <-chan AtmosResult

	mu sync.Mutex
	lg *log.Logger
}

type AtmosResult struct {
	Grid     *AtmosGrid
	Time     time.Time
	NextTime time.Time
	Err      error
}

// MakeCalmModel returns a Model with no weather data, so all lookups return
// the standard atmosphere with no wind.
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
func (m *Model) fetch(t time.Time) AtmosResult {
	atmos, atmosTime, nextTime, err := m.provider.GetAtmosGrid(m.facility, t, m.station)
	ar := AtmosResult{
		Time:     atmosTime,
		NextTime: nextTime,
		Err:      err,
	}
	if err == nil && atmos != nil {
		ar.Grid = atmos.ToAOS().GetGrid()
	}
	return ar
}

// fetchAtmos fetches the grid for time t in the background, so that the
// sim's update doesn't stall on the fetch or on building the grid.
func (m *Model) fetchAtmos(t time.Time) <-chan AtmosResult {
	if m.provider == nil {
		return nil
	}

	ch := make(chan AtmosResult, 1)
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
	if !ok0 || m.times[0].Equal(m.times[1]) {
		return s1
	} else if !ok1 {
		return s0
	} else {
		delta := t.Sub(m.times[0]).Seconds() / m.times[1].Sub(m.times[0]).Seconds()
		delta = math.Clamp(delta, 0, 1)
		return LerpSample(float32(delta), s0, s1)
	}
}

// Advance is called by the sim once each tick, t being the sim's time. It
// starts fetching the next grid once t passes the later of the two current
// ones, and it installs a grid whose fetch has finished.
func (m *Model) Advance(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.times[1].IsZero() && t.After(m.times[1]) && !m.nextFetch.IsZero() && m.ch == nil {
		m.ch = m.fetchAtmos(m.nextFetch)
	}

	select {
	case ar := <-m.ch:
		m.ch = nil
		m.installFetched(ar)
	default:
	}
}

// installFetched installs the grid a fetch got, if it got one.
func (m *Model) installFetched(ar AtmosResult) {
	if ar.Err != nil || ar.Grid == nil {
		if ar.Err != nil {
			m.lg.Errorf("%v", ar.Err)
		}
		// Carry on with the grids we have rather than asking again
		// every tick.
		m.nextFetch = time.Time{}
		return
	}
	m.install(ar)
}

func (m *Model) install(ar AtmosResult) {
	// Shift down to make room for the new one in [1].
	m.grids[0], m.times[0] = m.grids[1], m.times[1]
	m.grids[1], m.times[1] = ar.Grid, ar.Time
	m.nextFetch = ar.NextTime

	if m.grids[0] == nil {
		// We just got the very first one; copy it into [0] for now so
		// code elsewhere can assume that either none or both are
		// present.
		m.grids[0], m.times[0] = m.grids[1], m.times[1]

		// And get started on fetching the next one, when the series has
		// one: a time past the end of it comes back with no next time,
		// and fetching that would ask for the zero time and fail.
		if !m.nextFetch.IsZero() {
			m.ch = m.fetchAtmos(m.nextFetch)
		}
	}
}
