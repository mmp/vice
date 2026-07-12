// sim/traffic_provider.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import av "github.com/mmp/vice/aviation"

// trafficProvider supplies the next automatically generated IFR aircraft to
// the simulation. The provider chooses the flight to create; the existing
// spawn code remains responsible for when the aircraft is requested and how
// it is added to the simulation.
//
// Keeping this interface internal allows additional traffic sources to be
// introduced without changing the public simulation API.
type trafficProvider interface {
	createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, error)
	createInbound(s *Sim, group string, rates map[string]float32) (*Aircraft, float32, error)
}

// randomTrafficProvider preserves Vice's existing rate-based random traffic
// generation behavior.
type randomTrafficProvider struct{}

func (randomTrafficProvider) createIFRDeparture(s *Sim, airport string, runway av.RunwayID) (*Aircraft, error) {
	return s.makeNewIFRDeparture(airport, runway)
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

func (s *Sim) activeTrafficProvider() trafficProvider {
	if s.trafficProvider == nil {
		// Saved simulations created before traffic providers existed will not
		// have this runtime-only field initialized.
		s.trafficProvider = randomTrafficProvider{}
	}
	return s.trafficProvider
}
