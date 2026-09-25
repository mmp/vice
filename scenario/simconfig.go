// scenario/simconfig.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"

	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/brunoga/deep"
)

// Lookup finds the catalog and group that hold the named scenario at the
// given facility.
func (t *Tables) Lookup(facility, scenarioName string) (*Catalog, *Group, error) {
	if groups, ok := t.Groups[facility]; ok {
		for _, group := range groups {
			if _, ok := group.Scenarios[scenarioName]; ok {
				if facilityCatalogs, ok := t.Catalogs[facility]; ok {
					for _, catalog := range facilityCatalogs {
						if catalog.Scenarios[scenarioName] != nil {
							return catalog, group, nil
						}
					}
				}
			}
		}
	}
	return nil, nil, fmt.Errorf("scenario not found: %s/%s", facility, scenarioName)
}

// NewSimConfiguration builds the sim configuration for one of the group's
// scenarios. lc is a parameter because which traffic a sim is flown with is
// the caller's to decide. The caller fills in afterwards whatever only it
// knows: the description users see, the brief, the weather provider, the
// start time, and the emergencies to draw from.
func (sg *Group) NewSimConfiguration(scenarioName string, lc sim.LaunchConfig) (*sim.NewSimConfiguration, error) {
	sc, ok := sg.Scenarios[scenarioName]
	if !ok {
		return nil, fmt.Errorf("scenario %s not found in group", scenarioName)
	}
	fa := sg.FacilityConfig.FacilityAdaptation

	nsc := &sim.NewSimConfiguration{
		Facility:                   sg.facility(),
		ScenarioName:               scenarioName,
		LaunchConfig:               lc,
		FacilityAdaptation:         deep.MustCopy(fa),
		DisableTFRRestrictionAreas: sg.FacilityConfig.DisableTFRRestrictionAreas,
		DepartureRunways:           sc.DepartureRunways,
		ArrivalRunways:             sc.ArrivalRunways,
		VFRReportingPoints:         sg.VFRReportingPoints,
		MagneticVariation:          sg.MagneticVariation,
		NmPerLongitude:             sg.NmPerLongitude,
		WindSpecifier:              sc.WindSpecifier,
		Airports:                   sg.Airports,
		Fixes:                      sg.Fixes,
		Center:                     sc.center(sg),
		Range:                      util.Select(sc.Range == 0, fa.Range, sc.Range),
		ScenarioCenter:             sc.Center.Point2LL,
		ScenarioRange:              sc.Range,
		ScenarioAltitudeLimits:     sc.AltitudeLimits,
		DefaultMaps:                sc.DefaultMaps,
		DefaultMapGroup:            sc.DefaultMapGroup,
		InboundFlows:               sg.InboundFlows,
		Airspace:                   sg.Airspace,
		ControllerAirspace:         sc.Airspace,
		ControlPositions:           sg.FacilityConfig.ControlPositions,
		VirtualControllers:         sc.VirtualControllers,
		ControllerConfiguration:    &sc.ControllerConfiguration,
		ConfigurationId:            sc.ConfigurationString,
		HandoffIDs:                 sg.FacilityConfig.HandoffIDs,
		ERAMCoordination:           sg.ERAMCoordination,
	}

	pruneAirportFilters(&nsc.FacilityAdaptation, util.SortedMapKeys(sg.Airports),
		sc.DepartureRunways, sc.ArrivalRunways, lc.VFRAirportRates)

	return nsc, nil
}

// NewSimConfigurationForScenario builds the configuration for a scenario run
// without a server: it is flown with the scenario's own default traffic.
func (t *Tables) NewSimConfigurationForScenario(facility, scenarioName string) (*sim.NewSimConfiguration, error) {
	catalog, group, err := t.Lookup(facility, scenarioName)
	if err != nil {
		return nil, err
	}
	spec := catalog.Scenarios[scenarioName]
	if spec == nil {
		return nil, fmt.Errorf("scenario configuration %s not found", scenarioName)
	}

	nsc, err := group.NewSimConfiguration(scenarioName, spec.LaunchConfig)
	if err != nil {
		return nil, err
	}
	nsc.Description = scenarioName
	nsc.Emergencies = t.Emergencies

	return nsc, nil
}
