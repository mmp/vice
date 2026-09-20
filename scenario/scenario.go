// scenario/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// maxMagneticAdjustment is the largest magnetic_adjustment a scenario group
// may specify, in degrees.
const maxMagneticAdjustment float32 = 4

type Scenario struct {
	// ConfigurationString holds the plain configuration ID string from JSON
	// (e.g. "STD"). It references a key in facility_adaptations.configurations.
	ConfigurationString string `json:"configuration"`

	// ControllerConfiguration is the runtime-resolved configuration data,
	// populated during Finalize from ConfigurationString.
	ControllerConfiguration sim.ControllerConfiguration `json:"-"`

	// DefaultConsolidation optionally overrides the referenced facility
	// configuration's consolidation tree. When empty, the facility
	// configuration's is used.
	DefaultConsolidation sim.PositionConsolidation `json:"default_consolidation,omitempty"`

	// VirtualControllers is auto-derived at runtime from the facility config
	// and scenario routes; it is NOT read from JSON.
	VirtualControllers []sim.TCP `json:"-"`

	WindSpecifier *wx.WindSpecifier `json:"wind,omitempty"`

	// Map from inbound flow names to a map from airport name to default rate,
	// with "overflights" a special case to denote overflights
	InboundFlowDefaultRates map[string]map[string]float32 `json:"inbound_rates"`

	Airspace map[sim.TCP][]string `json:"airspace"`

	Description      string                `json:"description,omitempty"`
	DepartureRunways []sim.DepartureRunway `json:"departure_runways,omitempty"`
	ArrivalRunways   []sim.ArrivalRunway   `json:"arrival_runways,omitempty"`

	Center          math.Point2LL `json:"-"`
	CenterString    string        `json:"center"`
	Range           float32       `json:"range"`
	DefaultMaps     []string      `json:"default_maps"`
	DefaultMapGroup string        `json:"default_map_group"`
	VFRRateScale    *float32      `json:"vfr_rate_scale"`
	VFFRequestRate  *int32        `json:"flight_following_request_rate,omitempty"`
}

// center is where the scenario's radar display is centered: the scenario's own
// center if it gives one, otherwise the facility's.
func (s *Scenario) center(sg *Group) math.Point2LL {
	return util.Select(s.Center.IsZero(), sg.FacilityConfig.FacilityAdaptation.Center, s.Center)
}
