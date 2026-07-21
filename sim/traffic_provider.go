// sim/traffic_provider.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"sort"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// trafficProvider supplies automatically generated IFR aircraft to the
// simulation. It also controls when the next departure request should occur;
// random traffic uses a rate-based delay while schedule traffic uses the next
// published pushback time plus simulated taxi-out time.
type trafficProvider interface {
	createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error)
	createInbound(s *Sim, group string, rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error)
}

// randomTrafficProvider preserves Vice's existing rate-based random traffic
// generation behavior.
type randomTrafficProvider struct{}

func (randomTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error) {
	ac, err := s.makeNewIFRDeparture(airport, runway)
	depState := s.DepartureState[airport][runway]
	return ac, randomWait(depState.IFRSpawnRate, false, s.Rand), err
}

func (randomTrafficProvider) createInbound(s *Sim, group string,
	rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error) {
	flow, rateSum := sampleRateMap(
		rates,
		s.State.LaunchConfig.InboundFlowRateScale,
		s.Rand,
	)

	delay := randomWait(rateSum, pushActive, s.Rand)

	if flow == "overflights" {
		ac, err := s.createOverflightNoLock(group)
		return ac, delay, err
	}

	ac, err := s.createArrivalNoLock(group, flow)
	return ac, delay, err
}

type scheduledDeparture struct {
	flight ScheduledFlight
	offset time.Duration
}

type scheduledArrival struct {
	flight ScheduledFlight
	offset time.Duration
}

func includeScheduledFlight(flight ScheduledFlight, percentage int) bool {
	percentage = min(max(percentage, 0), 100)

	// Zero explicitly disables this direction, including cargo.
	if percentage == 0 {
		return false
	}

	if percentage == 100 || flight.Cargo {
		return true
	}

	// Use a stable hash so the same percentage consistently selects the same
	// flights each time the scenario is loaded.
	hash := uint32(2166136261)
	key := flight.Callsign + flight.Origin + flight.Destination

	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= 16777619
	}

	return int(hash%100) < percentage
}

// scheduleTrafficProvider emits departures in runway-ready order. Published
// departure times are treated as pushback times; a random 10-20 minute taxi-out
// duration is generated once per departure when the provider is first used.
// The schedule clock starts at ScheduleStartMinute when the provider is created.
type scheduleTrafficProvider struct {
	airport string
	start   Time

	departures            []scheduledDeparture
	nextDeparture         int
	departuresInitialized bool

	arrivals    []scheduledArrival
	nextArrival int
}

func newScheduleTrafficProvider(
	schedule BuiltInSchedule,
	startMinute int,
	start Time,
	arrivalPercentage int,
	departurePercentage int,
) *scheduleTrafficProvider {
	p := &scheduleTrafficProvider{airport: schedule.Airport, start: start}
	for _, flight := range schedule.Flights {
		minutes := (flight.ScheduledMinute - startMinute + 24*60) % (24 * 60)
		offset := time.Duration(minutes) * time.Minute

		switch flight.OperationAt(schedule.Airport) {
		case ScheduleOperationDeparture:
			if includeScheduledFlight(flight, departurePercentage) {
				p.departures = append(p.departures, scheduledDeparture{
					flight: flight,
					offset: offset,
				})
			}

		case ScheduleOperationArrival:
			if includeScheduledFlight(flight, arrivalPercentage) {
				p.arrivals = append(p.arrivals, scheduledArrival{
					flight: flight,
					offset: offset,
				})
			}
		}
	}
	sort.SliceStable(p.departures, func(i, j int) bool {
		return p.departures[i].offset < p.departures[j].offset
	})

	sort.SliceStable(p.arrivals, func(i, j int) bool {
		return p.arrivals[i].offset < p.arrivals[j].offset
	})

	return p

}

// initializeDepartures generates one taxi-out duration for each scheduled
// departure and orders the departures by the time they reach the runway queue.
// It runs only once, so taxi times remain fixed for the life of the scenario.

func (p *scheduleTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, time.Duration, error) {
	const idleDelay = 365 * 24 * time.Hour
	if airport != p.airport || p.nextDeparture >= len(p.departures) {
		return nil, idleDelay, nil
	}

	scheduled := p.departures[p.nextDeparture]
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
	p.nextDeparture++ // unmatched flights are skipped and reported by the caller

	delay := idleDelay
	if p.nextDeparture < len(p.departures) {
		nextDue := p.start.Add(p.departures[p.nextDeparture].offset)
		delay = max(time.Millisecond, nextDue.Sub(s.State.SimTime))
	}
	return ac, delay, err
}

func (p *scheduleTrafficProvider) createInbound(s *Sim, group string,
	rates map[string]float32, pushActive bool) (*Aircraft, time.Duration, error) {
	const idleDelay = 365 * 24 * time.Hour

	if p.nextArrival >= len(p.arrivals) {
		// Real-world schedules currently provide arrivals and departures only.
		// Continue generating random overflights when they are enabled.
		if _, ok := rates["overflights"]; ok {
			return randomTrafficProvider{}.createInbound(
				s,
				group,
				map[string]float32{"overflights": rates["overflights"]},
				pushActive,
			)
		}
		return nil, idleDelay, nil
	}

	scheduled := p.arrivals[p.nextArrival]
	due := p.start.Add(scheduled.offset)
	if s.State.SimTime.Before(due) {
		return nil, due.Sub(s.State.SimTime), nil
	}

	// Determine which scenario inbound flow contains a route from the
	// published origin to the schedule airport.
	matchedGroup := ""
	for candidateGroup, inboundFlow := range s.State.InboundFlows {
		if _, err := resolveScheduledArrival(
			inboundFlow.Arrivals,
			p.airport,
			scheduled.flight.Origin,
		); err == nil {
			matchedGroup = candidateGroup
			break
		}
	}

	if matchedGroup == "" {
		p.nextArrival++
		delay := idleDelay
		if p.nextArrival < len(p.arrivals) {
			nextDue := p.start.Add(p.arrivals[p.nextArrival].offset)
			delay = max(time.Millisecond, nextDue.Sub(s.State.SimTime))
		}
		return nil, delay, fmt.Errorf(
			"%s: no inbound flow from %s to %s",
			scheduled.flight.Callsign,
			scheduled.flight.Origin,
			p.airport,
		)
	}

	// Each inbound-flow timer calls this provider independently. Only the
	// matching flow should create this scheduled arrival.
	if group != matchedGroup {
		s.lg.Infof(
			"scheduled arrival %s waiting for flow %s (currently %s)",
			scheduled.flight.Callsign,
			matchedGroup,
			group,
		)
		return nil, time.Second, nil
	}

	if rate, ok := rates[p.airport]; !ok ||
		scaleRate(rate, s.State.LaunchConfig.InboundFlowRateScale) == 0 {
		return nil, time.Second, nil
	}

	ac, err := s.createScheduledArrivalNoLock(
		scheduled.flight,
		group,
		p.airport,
	)
	if errors.Is(err, errScheduledArrivalSpawnConflict) {
		// Keep this arrival at the head of the queue and retry shortly. This
		// preserves schedule order while allowing the preceding arrival to
		// move at least 10 NM away from the common spawn point.
		return nil, 5 * time.Second, nil
	}
	p.nextArrival++ // unmatched aircraft are skipped and reported by the caller

	delay := idleDelay
	if p.nextArrival < len(p.arrivals) {
		nextDue := p.start.Add(p.arrivals[p.nextArrival].offset)
		delay = max(time.Millisecond, nextDue.Sub(s.State.SimTime))
	}

	return ac, delay, err
}

type errorTrafficProvider struct{ err error }

func (p errorTrafficProvider) createIFRDeparture(_ *Sim, _ string, _ av.RunwayID) (*Aircraft, time.Duration, error) {
	return nil, time.Minute, p.err
}
func (p errorTrafficProvider) createInbound(_ *Sim, _ string,
	_ map[string]float32, _ bool) (*Aircraft, time.Duration, error) {
	return nil, time.Minute, p.err
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

	s.trafficProvider = newScheduleTrafficProvider(
		schedule,
		int(s.State.LaunchConfig.ScheduleStartMinute),
		s.scheduleStart,
		s.State.LaunchConfig.ScheduleArrivalPercentage,
		s.State.LaunchConfig.ScheduleDeparturePercentage,
	)
	return s.trafficProvider
}
