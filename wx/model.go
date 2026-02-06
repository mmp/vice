// wx/model.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"sync"
	"time"

	"github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

type Model struct {
	provider       Provider
	facility       string
	primaryAirport string

	grids     [2]*AtmosGrid
	times     [2]time.Time
	nextFetch time.Time
	ch        <-chan AtmosResult

	mu sync.Mutex
	lg *log.Logger
}

type AtmosResult struct {
	AtmosByPointSOA *AtmosByPointSOA
	Time            time.Time
	NextTime        time.Time
	Err             error
}

func MakeModel(provider Provider, facility string, primaryAirport string, startTime time.Time, lg *log.Logger) *Model {
	m := &Model{
		provider:       provider,
		facility:       facility,
		primaryAirport: primaryAirport,
		lg:             lg,
	}
	if !aviation.DB.IsFacility(facility) {
		return m
	}

	m.ch = m.fetchAtmos(startTime)

	return m
}

func (m *Model) fetchAtmos(t time.Time) <-chan AtmosResult {
	if m.provider == nil {
		return nil
	}

	ch := make(chan AtmosResult, 1)

	go func() {
		defer close(ch)
		atmos, atmosTime, nextTime, err := m.provider.GetAtmosGrid(m.facility, t, m.primaryAirport)
		ch <- AtmosResult{
			AtmosByPointSOA: atmos,
			Time:            atmosTime,
			NextTime:        nextTime,
			Err:             err,
		}
	}()

	return ch
}

func (m *Model) Lookup(p math.Point2LL, alt float32, t time.Time) Sample {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.checkFetches(t)

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

func (m *Model) checkFetches(t time.Time) {
	if !aviation.DB.IsFacility(m.facility) {
		return
	}

	// Kick off the next fetch if we've passed the second grid's time.
	if !m.times[1].IsZero() && t.After(m.times[1]) && !m.nextFetch.IsZero() && m.ch == nil {
		m.ch = m.fetchAtmos(m.nextFetch)
	}

	select {
	case ar := <-m.ch:
		m.updateAtmos(ar)
	default:
	}
}

func (m *Model) updateAtmos(ar AtmosResult) {
	if ar.Err != nil {
		m.lg.Errorf("%v", ar.Err)
		return
	} else if ar.AtmosByPointSOA != nil {
		if !aviation.DB.IsFacility(m.facility) {
			return
		}

		// Shift down to make room for the new one in [1].
		m.grids[0], m.times[0] = m.grids[1], m.times[1]

		atmos := ar.AtmosByPointSOA.ToAOS()
		m.grids[1] = atmos.GetGrid()
		m.times[1] = ar.Time
		m.nextFetch = ar.NextTime

		if m.grids[0] == nil {
			// We just got the very first one; copy it into [0] for now so
			// code elsewhere can assume that either none or both are
			// present.
			m.grids[0], m.times[0] = m.grids[1], m.times[1]

			// And get started on fetching the next one.
			m.ch = m.fetchAtmos(m.nextFetch)
		} else {
			m.ch = nil
		}
	}
}

func (m *Model) GetAtmosGrid() *AtmosGrid {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.grids[0] == nil && m.ch != nil {
		// Stall until the fetch finishes so we can return a valid grid.
		m.updateAtmos(<-m.ch)
	}

	return m.grids[0]
}
