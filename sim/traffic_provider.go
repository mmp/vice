// sim/traffic_provider.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"sort"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// trafficProvider supplies automatically generated IFR aircraft to the
// simulation. It also controls when the next departure request should occur;
// random traffic uses a rate-based delay while schedule traffic uses the next
// published runway time.
type trafficProvider interface {
	createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error)
	createInbound(s *Sim, group string, rates map[string]float32) (*Aircraft, float32, error)
}

// randomTrafficProvider preserves Vice's existing rate-based random traffic
// generation behavior.
type randomTrafficProvider struct{}

func (randomTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error) {
	ac, err := s.makeNewIFRDeparture(airport, runway)
	depState := s.DepartureState[airport][runway]
	return ac, randomWait(depState.IFRSpawnRate, false, s.Rand), err
}

func (randomTrafficProvider) createInbound(s *Sim, group string, rates map[string]float32) (*Aircraft, float32, error) {
	flow, rateSum := sampleRateMap(rates, s.State.LaunchConfig.InboundFlowRateScale, s.Rand)

	if flow == "overflights" {
		ac, err := s.createOverflightNoLock(group)
		return ac, rateSum, err
	}

	ac, err := s.createArrivalNoLock(group, flow)
	return ac, rateSum, err
}

type scheduledDeparture struct {
	flight ScheduledFlight
	offset time.Duration
}

// scheduleTrafficProvider emits departures in published-time order. The
// schedule clock starts at ScheduleStartMinute when the provider is
// first initialized.
type scheduleTrafficProvider struct {
	airport    string
	start      Time
	departures []scheduledDeparture
	next       int
}

func newScheduleTrafficProvider(schedule BuiltInSchedule, startMinute int, start Time) *scheduleTrafficProvider {
	p := &scheduleTrafficProvider{airport: schedule.Airport, start: start}
	for _, flight := range schedule.Flights {
		if flight.OperationAt(schedule.Airport) != ScheduleOperationDeparture {
			continue
		}
		minutes := (flight.ScheduledMinute - startMinute + 24*60) % (24 * 60)
		p.departures = append(p.departures, scheduledDeparture{
			flight: flight,
			offset: time.Duration(minutes) * time.Minute,
		})
	}
	sort.SliceStable(p.departures, func(i, j int) bool {
		return p.departures[i].offset < p.departures[j].offset
	})
	return p
}

func (p *scheduleTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error) {
	const idleDelay = 365 * 24 * time.Hour
	if airport != p.airport || p.next >= len(p.departures) {
		return nil, idleDelay, nil
	}

	scheduled := p.departures[p.next]
	due := p.start.Add(scheduled.offset)
	if s.State.SimTime.Before(due) {
		return nil, due.Sub(s.State.SimTime), nil
	}

	rates := s.State.LaunchConfig.DepartureRates[airport][runway]
	category, rateSum := sampleRateMap(rates, s.State.LaunchConfig.DepartureRateScale, s.Rand)
	if rateSum == 0 {
		return nil, time.Second, nil
	}

	ac, err := s.createScheduledIFRDepartureNoLock(scheduled.flight, airport, runway, category)
	p.next++ // unmatched flights are skipped and reported by the caller

	delay := idleDelay
	if p.next < len(p.departures) {
		nextDue := p.start.Add(p.departures[p.next].offset)
		delay = max(time.Millisecond, nextDue.Sub(s.State.SimTime))
	}
	return ac, delay, err
}

func (p *scheduleTrafficProvider) createInbound(s *Sim, group string, rates map[string]float32) (*Aircraft, float32, error) {
	// Scheduled arrivals are added in a later commit. Keep the existing arrival
	// behavior until that path is implemented.
	return randomTrafficProvider{}.createInbound(s, group, rates)
}

type errorTrafficProvider struct{ err error }

func (p errorTrafficProvider) createIFRDeparture(_ *Sim, _ string, _ av.RunwayID) (*Aircraft, time.Duration, error) {
	return nil, time.Minute, p.err
}
func (p errorTrafficProvider) createInbound(_ *Sim, _ string, _ map[string]float32) (*Aircraft, float32, error) {
	return nil, 0, p.err
}

func (s *Sim) activeTrafficProvider() trafficProvider {
	if s.trafficProvider != nil {
		return s.trafficProvider
	}

	if s.State.LaunchConfig.TrafficSource != TrafficSourceRealWorldSchedule {
		s.trafficProvider = randomTrafficProvider{}
		return s.trafficProvider
	}

	catalog, err := LoadBuiltInScheduleCatalog(util.GetResourcesFS(), "schedules")
	if err != nil {
		s.trafficProvider = errorTrafficProvider{err: err}
		return s.trafficProvider
	}
	schedule, ok := catalog.Find(s.State.PrimaryAirport, s.State.LaunchConfig.ScheduleID)
	if !ok {
		s.trafficProvider = errorTrafficProvider{err: fmt.Errorf("real-world schedule %q not found for %s",
			s.State.LaunchConfig.ScheduleID, s.State.PrimaryAirport)}
		return s.trafficProvider
	}

	s.trafficProvider = newScheduleTrafficProvider(schedule,
		int(s.State.LaunchConfig.ScheduleStartMinute), s.State.SimTime)
	return s.trafficProvider
}
