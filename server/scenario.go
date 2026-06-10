// pkg/server/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/brief"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

type scenarioGroup struct {
	ARTCC              string                     `json:"artcc"`
	Area               string                     `json:"area"`
	TRACON             string                     `json:"tracon"`
	Name               string                     `json:"name"`
	Airports           map[string]*av.Airport     `json:"airports"`
	Fixes              map[string]math.Point2LL   `json:"-"`
	FixesStrings       util.OrderedMap            `json:"fixes"`
	Scenarios          map[string]*scenario       `json:"scenarios"`
	DefaultScenario    string                     `json:"default_scenario"`
	Airspace           av.Airspace                `json:"airspace"`
	InboundFlows       map[string]*av.InboundFlow `json:"inbound_flows"`
	VFRReportingPoints []av.VFRReportingPoint     `json:"vfr_reporting_points"`

	AllowFixRedefinitions bool   `json:"allow_fix_redefinitions"`
	PrimaryAirport        string `json:"primary_airport"`

	ReportingPointStrings []string            `json:"reporting_points"`
	ReportingPoints       []av.ReportingPoint // not in JSON

	NmPerLatitude      float32 // Always 60
	NmPerLongitude     float32 // Derived from Center
	MagneticVariation  float32
	MagneticAdjustment float32 `json:"magnetic_adjustment"`

	// FacilityConfig is populated at runtime from the facility config file,
	// not from the scenario group JSON.
	FacilityConfig sim.FacilityConfig `json:"-"`

	SourceFile string // path of the JSON file this was loaded from
}

type scenario struct {
	// ConfigurationString holds the plain configuration ID string from JSON
	// (e.g. "STD"). It references a key in facility_adaptations.configurations.
	ConfigurationString string `json:"configuration"`

	// ControllerConfiguration is the runtime-resolved configuration data,
	// populated during PostDeserialize from ConfigurationString.
	ControllerConfiguration sim.ControllerConfiguration `json:"-"`

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
}

func (s *scenario) PostDeserialize(sg *scenarioGroup, e *util.ErrorLogger, mapSpec *sim.VideoMapSpec) {
	defer e.CheckDepth(e.CurrentDepth())

	// Validate wind specifier if present
	if s.WindSpecifier != nil {
		e.Push(`"wind"`)
		if err := s.WindSpecifier.Validate(); err != nil {
			e.Error(err)
		}
		e.Pop()
	}

	// Validate configuration
	if s.ConfigurationString == "" {
		e.ErrorString(`"configuration" is required`)
		return
	}

	// Resolve configuration string to get assignments and consolidation from facility_adaptations.configurations
	if config, ok := sg.FacilityConfig.FacilityAdaptation.Configurations[s.ConfigurationString]; !ok {
		e.ErrorString(`"configuration" %q not found in "facility_adaptations" "configurations"`, s.ConfigurationString)
	} else {
		// Copy assignments from the referenced configuration
		s.ControllerConfiguration.InboundAssignments = maps.Clone(config.InboundAssignments)
		s.ControllerConfiguration.DepartureAssignments = maps.Clone(config.DepartureAssignments)
		s.ControllerConfiguration.GoAroundAssignments = maps.Clone(config.GoAroundAssignments)
		s.ControllerConfiguration.DefaultConsolidation = deep.MustCopy(config.DefaultConsolidation)

		// Auto-add airspace controllers to consolidation if they're valid
		// control positions but missing from the consolidation tree.
		if len(s.ControllerConfiguration.DefaultConsolidation) > 0 {
			allPos := s.ControllerConfiguration.AllPositions()
			root, rootErr := s.ControllerConfiguration.DefaultConsolidation.RootPosition()
			if rootErr == nil {
				for ctrl := range s.Airspace {
					if !slices.Contains(allPos, ctrl) {
						if _, inFacility := sg.FacilityConfig.ControlPositions[sg.resolveController(ctrl)]; inFacility {
							s.ControllerConfiguration.DefaultConsolidation[root] = append(
								s.ControllerConfiguration.DefaultConsolidation[root], ctrl)
						}
					}
				}
			}
		}

		// Filter assignments to only include entries targeting positions that
		// exist as known controllers. The facility config's full assignments
		// cover all positions in the TRACON, but some may reference
		// positions that don't exist in the loaded controller set.
		for flow, tcp := range s.ControllerConfiguration.InboundAssignments {
			if resolved := sg.resolveController(tcp); resolved != tcp {
				s.ControllerConfiguration.InboundAssignments[flow] = resolved
			} else if _, ok := sg.FacilityConfig.ControlPositions[tcp]; !ok {
				delete(s.ControllerConfiguration.InboundAssignments, flow)
			}
		}
		for spec, tcp := range s.ControllerConfiguration.DepartureAssignments {
			if resolved := sg.resolveController(tcp); resolved != tcp {
				s.ControllerConfiguration.DepartureAssignments[spec] = resolved
			} else if _, ok := sg.FacilityConfig.ControlPositions[tcp]; !ok {
				delete(s.ControllerConfiguration.DepartureAssignments, spec)
			}
		}

		s.ControllerConfiguration.Validate(sg.FacilityConfig.ControlPositions, e)

		// Validate inbound flow assignments.
		// A flow only needs an inbound_assignment if it has a generic /ho
		// handoff (which doesn't specify a sector). Flows with no /ho at
		// all, or only specific handoffs like /ho1F, are exempt.
		flowNeedsHumanAssignment := func(flow *av.InboundFlow) bool {
			return flow.HasHumanHandoff()
		}

		// Check that every flow with generic /ho handoffs has an assignment to
		// a human position in default_consolidation.
		// Note: It is NOT an error if the configuration has excess assignments that the scenario doesn't use.
		humanPositions := s.ControllerConfiguration.AllPositions()
		for flowName := range s.InboundFlowDefaultRates {
			flow, ok := sg.InboundFlows[flowName]
			if !ok || !flowNeedsHumanAssignment(flow) {
				continue
			}
			tcp, ok := s.ControllerConfiguration.InboundAssignments[flowName]
			if !ok {
				e.ErrorString(`inbound flow %q needs human controller but has no assignment in "inbound_assignments"`, flowName)
			} else if !slices.Contains(humanPositions, tcp) {
				e.ErrorString(`inbound_assignments in %q: flow %q assigns to %q which is not a human position in "default_consolidation"`, s.ConfigurationString, flowName, tcp)
			}
		}
		// departure_assignments validation is done below, after activeAirportSIDs/Runways maps are built

		// Validate go_around_assignments
		for spec, tcp := range s.ControllerConfiguration.GoAroundAssignments {
			if !slices.Contains(s.ControllerConfiguration.AllPositions(), tcp) {
				e.ErrorString(`go_around_assignments: %q assigns to %q which is not a human position in "default_consolidation"`, spec, tcp)
			}
			// Validate airport/runway
			airport, runway, hasRunway := strings.Cut(spec, "/")
			if _, ok := sg.Airports[airport]; !ok {
				e.ErrorString("go_around_assignments: airport %q not in scenario", airport)
			} else if hasRunway && !av.AirportHasRunway(airport, av.RunwayID(runway)) {
				e.ErrorString("go_around_assignments: runway %q not a valid runway at %q", runway, airport)
			}
		}
	}

	for ctrl, vnames := range s.Airspace {
		e.Push("airspace")

		// Verify controller is in configuration
		found := slices.Contains(s.ControllerConfiguration.AllPositions(), ctrl)
		if !found {
			e.ErrorString("Controller %q not used in scenario", ctrl)
		}
		for _, vname := range vnames {
			if _, ok := sg.Airspace.Volumes[vname]; !ok {
				e.ErrorString(`Airspace volume %q for controller %q not defined in scenario group "airspace"`,
					vname, ctrl)
			}
		}
		e.Pop()
	}

	sort.Slice(s.DepartureRunways, func(i, j int) bool {
		if s.DepartureRunways[i].Airport != s.DepartureRunways[j].Airport {
			return s.DepartureRunways[i].Airport < s.DepartureRunways[j].Airport
		} else if s.DepartureRunways[i].Runway != s.DepartureRunways[j].Runway {
			return s.DepartureRunways[i].Runway < s.DepartureRunways[j].Runway
		} else {
			return s.DepartureRunways[i].Category < s.DepartureRunways[j].Category
		}
	})

	// Auto-derive virtual controllers from routes, inbound flows, etc.
	// Every controller referenced must exist in sg.FacilityConfig.ControlPositions.
	humanPositionsSet := make(map[sim.TCP]bool)
	for _, pos := range s.ControllerConfiguration.AllPositions() {
		humanPositionsSet[pos] = true
	}
	addController := func(tcp sim.TCP) {
		if tcp == "" {
			return
		}
		tcp = sg.resolveController(tcp)
		if _, ok := sg.FacilityConfig.ControlPositions[tcp]; !ok {
			e.ErrorString("controller %q referenced in route/flow but not defined in facility configuration", tcp)
			return
		}
		// Only add to VirtualControllers if not a human position
		if !humanPositionsSet[tcp] && !slices.Contains(s.VirtualControllers, tcp) {
			s.VirtualControllers = append(s.VirtualControllers, tcp)
		}
	}
	addControllersFromWaypoints := func(route []av.Waypoint) {
		for _, wp := range route {
			addController(sim.TCP(wp.HandoffController()))
			for _, group := range wp.ActionGroups() {
				addController(sim.TCP(group.Actions.HandoffController))
			}
		}
	}
	// Make sure all of the controllers used in airspace awareness will be there.
	for _, aa := range sg.FacilityConfig.FacilityAdaptation.AirspaceAwareness {
		addController(sim.TCP(aa.ReceivingController))
	}
	for _, area := range sg.FacilityConfig.FacilityAdaptation.Areas {
		for _, aa := range area.AirspaceAwareness {
			addController(sim.TCP(aa.ReceivingController))
		}
	}

	airportExits := make(map[string]map[string]any) // airport -> exit -> is it covered
	for _, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + rwy.Airport + " " + string(rwy.Runway))

		if airportExits[rwy.Airport] == nil {
			airportExits[rwy.Airport] = make(map[string]any)
		}

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString(`airport not found in scenario group "airports"`)
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				e.ErrorString("runway departure routes not found")
			} else {
				for exit := range routes {
					// It's fine if multiple active runways cover the exit.
					airportExits[rwy.Airport][string(exit)] = nil
				}

				for _, r := range routes {
					addControllersFromWaypoints(r.Waypoints)
				}
			}

			if len(ap.Departures) == 0 {
				e.ErrorString(`no "departures" specified for airport`)
			}

			if rwy.Category != "" {
				found := slices.ContainsFunc(ap.Departures, func(dep av.Departure) bool {
					return ap.ExitCategories[dep.Exit] == rwy.Category
				})
				if !found {
					e.ErrorString("no departures have exit category %q", rwy.Category)
				}
			}
		}
		e.Pop()
	}

	sort.Slice(s.ArrivalRunways, func(i, j int) bool {
		if s.ArrivalRunways[i].Airport == s.ArrivalRunways[j].Airport {
			return s.ArrivalRunways[i].Runway < s.ArrivalRunways[j].Runway
		}
		return s.ArrivalRunways[i].Airport < s.ArrivalRunways[j].Airport
	})

	activeAirports := make(map[*av.Airport]any) // all airports with departures or arrivals
	for _, rwy := range s.ArrivalRunways {
		e.Push("Arrival runway " + rwy.Airport + " " + string(rwy.Runway))

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString(`airport not found in scenario group "airports"`)
		} else {
			activeAirports[ap] = nil

			if !util.SeqContainsFunc(maps.Values(ap.Approaches),
				func(appr *av.Approach) bool { return appr.Runway == rwy.Runway.Base() }) {
				e.ErrorString("no approach found that reaches this runway")
			}

			// Validate go_around procedure if specified
			if rwy.GoAround != nil {
				e.Push("go_around")

				// Resolve heading: 0 means runway heading, otherwise must be 1-360
				if rwy.GoAround.Heading == 0 {
					rwy.GoAround.IsRunwayHeading = true
					for _, appr := range ap.Approaches {
						if appr.Runway == rwy.Runway.Base() {
							rwy.GoAround.Heading = int(math.TrueToMagnetic(appr.RunwayHeading(sg.NmPerLongitude), sg.MagneticVariation) + 0.5)
							break
						}
					}
				} else if rwy.GoAround.Heading < 1 || rwy.GoAround.Heading > 360 {
					e.ErrorString("heading must be between 1 and 360, got %d", rwy.GoAround.Heading)
				}

				// Validate altitude: must be reasonable (1000-15000 feet)
				if rwy.GoAround.Altitude < 1000 || rwy.GoAround.Altitude > 15000 {
					e.ErrorString("altitude must be between 1000 and 15000 feet, got %d", rwy.GoAround.Altitude)
				}

				// Validate handoff_controller: must be a valid TCP in control_positions
				if rwy.GoAround.HandoffController != "" {
					if _, ok := sg.FacilityConfig.ControlPositions[rwy.GoAround.HandoffController]; !ok {
						e.ErrorString(`"handoff_controller" %q not found in "control_positions"`, rwy.GoAround.HandoffController)
					}
				}

				// Validate hold_departures: each must be a valid runway at the airport
				for _, holdRwy := range rwy.GoAround.HoldDepartures {
					if !av.AirportHasRunway(rwy.Airport, av.RunwayID(holdRwy)) {
						e.ErrorString("hold_departures: runway %q not a valid runway at %q", holdRwy, rwy.Airport)
					}
				}

				e.Pop()
			}
		}

		e.Pop()
	}

	// Figure out which airports/runways and airports/SIDs are used in the scenario.
	activeAirportSIDs := make(map[string]map[string]any)
	activeAirportRunways := make(map[string]map[string]any)
	activeDepartureAirports := make(map[string]any)
	for _, rwy := range s.DepartureRunways {
		e.Push("departure runway " + string(rwy.Runway))

		ap, ok := sg.Airports[rwy.Airport]
		if !ok {
			e.ErrorString(`%s: airport not found in "airports"`, rwy.Airport)
		} else {
			activeAirports[ap] = nil
			activeDepartureAirports[rwy.Airport] = nil

			if ap.DepartureController != "" {
				addController(sim.TCP(ap.DepartureController))
			} else {
				// Only check for a human controller to be covering the track if there isn't
				// a virtual controller assigned to it.
				exitRoutes := ap.DepartureRoutes[rwy.Runway]
				for fix, route := range exitRoutes {
					fixCategory := ap.ExitCategories[fix]
					if rwy.Category != "" && fixCategory == "" {
						e.ErrorString(`exit fix %q (SID %s) has no entry in "exit_categories" but runway uses category %q`,
							fix, route.SID, rwy.Category)
					}
					if rwy.Category == "" || fixCategory == rwy.Category {
						if activeAirportSIDs[rwy.Airport] == nil {
							activeAirportSIDs[rwy.Airport] = make(map[string]any)
						}
						if activeAirportRunways[rwy.Airport] == nil {
							activeAirportRunways[rwy.Airport] = make(map[string]any)
						}
						if route.DepartureController != "" {
							addController(sim.TCP(route.DepartureController))
						}
						activeAirportSIDs[rwy.Airport][route.SID] = nil
						activeAirportRunways[rwy.Airport][string(rwy.Runway)] = nil
					}
				}
			}
		}

		e.Pop()
	}

	// Validate departure_assignments - now that we have the activeAirportSIDs and activeAirportRunways maps
	// Note: Unlike arrivals/overflights, departures are handed to humans by default.
	// They only stay with virtual controllers if departure_controller is explicitly set.
	// activeAirportSIDs already filters out airports with departure_controller set.
	// Note: It is NOT an error if the configuration has excess assignments that the scenario doesn't use.
	// Track per-airport: assigned SIDs, assigned runways, and whether there's a fallback
	// Only track assignments that are relevant to THIS scenario's active airports/SIDs/runways
	assignedSIDs := make(map[string]map[string]any)    // airport -> set of SIDs
	assignedRunways := make(map[string]map[string]any) // airport -> set of runways
	hasAirportFallback := make(map[string]bool)        // airport -> has plain airport assignment

	for spec, tcp := range s.ControllerConfiguration.DepartureAssignments {
		ap, sidRunway, haveSIDRunway := strings.Cut(spec, "/")

		// Only process assignments for airports that are active in this scenario
		// and need human controller assignments (i.e., are in activeAirportSIDs)
		sids, isActiveHumanAirport := activeAirportSIDs[ap]
		if !isActiveHumanAirport {
			// Skip - either not an active departure airport, or has virtual controller
			continue
		}

		relevant := false
		if haveSIDRunway {
			// Track assigned SIDs and runways per airport (only if active in this scenario)
			_, okSID := sids[sidRunway]
			_, okRunway := activeAirportRunways[ap][sidRunway]

			if okSID {
				if assignedSIDs[ap] == nil {
					assignedSIDs[ap] = make(map[string]any)
				}
				assignedSIDs[ap][sidRunway] = nil
				relevant = true
			}
			if okRunway {
				if assignedRunways[ap] == nil {
					assignedRunways[ap] = make(map[string]any)
				}
				assignedRunways[ap][sidRunway] = nil
				relevant = true
			}
			// Note: If neither okSID nor okRunway, this assignment is for a SID/runway
			// not active in this scenario, which is fine (excess assignments are OK)

			// Check for mixing SIDs and runways for this airport
			if len(assignedSIDs[ap]) > 0 && len(assignedRunways[ap]) > 0 {
				e.ErrorString("departure_assignments: cannot mix runways and SIDs as specifiers for airport %q in %q",
					ap, s.ConfigurationString)
			}
		} else {
			// Plain airport assignment (fallback)
			hasAirportFallback[ap] = true
			relevant = true
		}

		if relevant && !humanPositionsSet[tcp] {
			e.ErrorString(`departure_assignments in %q: %q assigns to %q which is not a human position in "default_consolidation"`, s.ConfigurationString, spec, tcp)
		}
	}

	// Check that every active departure airport has complete coverage
	for ap, activeSIDs := range activeAirportSIDs {
		if hasAirportFallback[ap] {
			// Airport has a fallback, so incomplete SID/runway coverage is OK
			continue
		}

		if assigned, ok := assignedSIDs[ap]; ok {
			// Using SID-based assignments - check all active SIDs are covered
			for sid := range activeSIDs {
				if _, ok := assigned[sid]; !ok {
					e.ErrorString("departure_assignments: airport %q uses SID-based assignments but SID %q has no assignment in %q",
						ap, sid, s.ConfigurationString)
				}
			}
		} else if assigned, ok := assignedRunways[ap]; ok {
			// Using runway-based assignments - check all active runways are covered
			for rwy := range activeAirportRunways[ap] {
				if _, ok := assigned[rwy]; !ok {
					e.ErrorString("departure_assignments: airport %q uses runway-based assignments but runway %q has no assignment in %q",
						ap, rwy, s.ConfigurationString)
				}
			}
		} else {
			// No assignments at all for this airport
			e.ErrorString(`departure airport %q has no assignment in "departure_assignments" in %q`, ap,
				s.ConfigurationString)
		}
	}

	// Do any active airports have CRDA?
	haveCRDA := util.SeqContainsFunc(maps.Keys(activeAirports),
		func(ap *av.Airport) bool { return len(ap.CRDAPairs) > 0 })
	if haveCRDA {
		// Make sure all of the controllers involved have a valid default airport via areas
		for _, pos := range s.ControllerConfiguration.AllPositions() {
			if ctrl, ok := sg.FacilityConfig.ControlPositions[pos]; ok {
				da := sg.FacilityConfig.FacilityAdaptation.DefaultAirportForArea(ctrl.Area)
				if da == "" {
					e.ErrorString("%s: controller must have a default airport specified via areas (required for CRDA).", ctrl.Position)
				} else {
					if _, ok := sg.Airports[da]; !ok {
						e.ErrorString("%s: default airport %q is not included in scenario", ctrl.Position, da)
					}
				}
			}
		}
	}

	for name := range util.SortedMap(s.InboundFlowDefaultRates) {
		e.Push("Inbound flow " + name)
		// Make sure the inbound flow has been defined
		if flow, ok := sg.InboundFlows[name]; !ok {
			e.ErrorString("inbound flow not found")
		} else {
			for _, ar := range flow.Arrivals {
				addController(sim.TCP(ar.InitialController))
				addControllersFromWaypoints(ar.Waypoints)
				for _, rwys := range ar.RunwayWaypoints {
					for _, rwyWps := range rwys {
						addControllersFromWaypoints(rwyWps)
					}
				}
			}
			for _, of := range flow.Overflights {
				addController(sim.TCP(of.InitialController))
				addControllersFromWaypoints(of.Waypoints)
			}

			// Check the airports in it
			for category := range s.InboundFlowDefaultRates[name] {
				if category == "overflights" {
					if len(flow.Overflights) == 0 {
						e.ErrorString(`Rate specified for "overflights" but no overflights specified in %q`, name)
					}
				} else {
					airport := category
					e.Push("Airport " + airport)
					if _, ok := sg.Airports[airport]; !ok {
						e.ErrorString("unknown arrival airport")
					} else {
						// Make sure the airport exists in at least one of the
						// arrivals in the group.
						found := false
						for _, ar := range flow.Arrivals {
							if _, ok := ar.Airlines[airport]; ok {
								found = true

								// Make sure the airport has at least one
								// active arrival runway.
								if !slices.ContainsFunc(s.ArrivalRunways,
									func(r sim.ArrivalRunway) bool {
										return r.Airport == airport
									}) {
									e.ErrorString(`no runways listed in "arrival_runways" for %s even though there are %s arrivals in "arrivals"`,
										airport, airport)
								}
							}
						}
						if !found {
							e.ErrorString("airport not used for any arrivals")
						}
					}
					e.Pop()
				}
			}

		}
		e.Pop()
	}

	// Remove any human-allocatable positions from VirtualControllers.
	// They may have been added from JSON or from InitialController/HandoffController in routes.
	humanPositions := s.ControllerConfiguration.AllPositions()
	s.VirtualControllers = slices.DeleteFunc(s.VirtualControllers, func(tcp sim.TCP) bool {
		return slices.Contains(humanPositions, tcp)
	})

	for _, ctrl := range s.VirtualControllers {
		if _, ok := sg.FacilityConfig.ControlPositions[ctrl]; !ok {
			e.ErrorString("controller %q unknown", ctrl)
		}
	}

	if s.CenterString != "" {
		if pos, ok := sg.Locate(s.CenterString); !ok {
			e.ErrorString(`unknown location %q specified for "center"`, s.CenterString)
		} else {
			s.Center = pos
		}
	}

	for _, dm := range s.DefaultMaps {
		if !mapSpec.HasMap(dm) {
			e.ErrorString(`video map %q in "default_maps" not found. Use -listmaps `+
				"<path to *.mappack> to show available video maps for a facilty.", dm)
		}
	}
	if sg.ARTCC != "" {
		if !mapSpec.HasMapGroup(s.DefaultMapGroup) {
			e.ErrorString(`video map group %q in "default_map_group" not found. Use -listmaps `+
				"<path to *.mappack> to show available video map groups for a facility.", s.DefaultMapGroup)
		}
	}

	if s.VFRRateScale == nil { // unspecified -> default to 1
		one := float32(1)
		s.VFRRateScale = &one
	}
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

func (sg *scenarioGroup) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if p, ok := sg.Fixes[s]; ok {
		return p, true
	} else if n, ok := av.DB.Navaids[s]; ok {
		return n.Location, ok
	} else if ap, ok := av.DB.LookupAirport(s); ok {
		return ap.Location, ok
	} else if f, ok := av.DB.Fixes[s]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else if len(s) > 5 && s[4] == '-' {
		if rwy, ok := av.LookupRunway(s[:4], s[5:]); ok {
			return rwy.Threshold, true
		}
	}

	return math.Point2LL{}, false
}

func (sg *scenarioGroup) LocateDME(s string) (math.Point2LL, int, bool) {
	return av.DB.LookupDME(s)
}

// resolveController normalizes a TCP that may use a short prefix (e.g.
// "N2K") to the canonical long-prefix form (e.g. "NNN2K") stored in
// ControlPositions.  If tcp is already present or no expansion matches,
// it is returned unchanged.
func (sg *scenarioGroup) resolveController(tcp sim.TCP) sim.TCP {
	if _, ok := sg.FacilityConfig.ControlPositions[tcp]; ok {
		return tcp
	}
	s := string(tcp)
	for _, hid := range sg.FacilityConfig.HandoffIDs {
		// Find the canonical (longest) prefix and collect shorter ones.
		canonical, shorter := "", []string(nil)
		for _, id := range []string{hid.StarsID, hid.TwoCharStarsID, hid.SingleCharStarsID, hid.Prefix} {
			if id == "" {
				continue
			}
			if canonical == "" {
				canonical = id
			} else {
				shorter = append(shorter, id)
			}
		}
		for _, short := range shorter {
			if strings.HasPrefix(s, short) {
				if resolved := sim.TCP(canonical + s[len(short):]); sg.FacilityConfig.ControlPositions[resolved] != nil {
					return resolved
				}
			}
		}
	}
	return tcp
}

// resolveControllerRefs walks all airports and inbound flows, resolving
// short-prefix controller references to their canonical (longest-prefix)
// form in place. This must be called before airport/flow PostDeserialize
// so that validation in the aviation package finds the controllers.
func (sg *scenarioGroup) resolveControllerRefs() {
	resolve := func(cp av.ControlPosition) av.ControlPosition {
		return av.ControlPosition(sg.resolveController(sim.TCP(cp)))
	}
	resolveWaypoints := func(wps av.WaypointArray) {
		for i := range wps {
			if wps[i].HandoffController() != "" {
				wps[i].InitExtra().HandoffController = resolve(wps[i].HandoffController())
			}
			if wps[i].PointOut() != "" {
				wps[i].InitExtra().PointOut = resolve(wps[i].PointOut())
			}
			for j := range wps[i].ActionGroups() {
				if wps[i].Extra.ActionGroups[j].Actions.HandoffController != "" {
					wps[i].Extra.ActionGroups[j].Actions.HandoffController =
						resolve(wps[i].Extra.ActionGroups[j].Actions.HandoffController)
				}
				if wps[i].Extra.ActionGroups[j].Actions.PointOut != "" {
					wps[i].Extra.ActionGroups[j].Actions.PointOut =
						resolve(wps[i].Extra.ActionGroups[j].Actions.PointOut)
				}
			}
		}
	}

	for _, ap := range sg.Airports {
		if ap.DepartureController != "" {
			ap.DepartureController = resolve(ap.DepartureController)
		}
		for _, exitRoutes := range ap.DepartureRoutes {
			for _, route := range exitRoutes {
				if route.HandoffController != "" {
					route.HandoffController = resolve(route.HandoffController)
				}
				if route.DepartureController != "" {
					route.DepartureController = resolve(route.DepartureController)
				}
				resolveWaypoints(route.Waypoints)
			}
		}
		for _, appr := range ap.Approaches {
			for _, wps := range appr.Waypoints {
				resolveWaypoints(wps)
			}
		}
	}

	for _, flow := range sg.InboundFlows {
		for i := range flow.Arrivals {
			if flow.Arrivals[i].InitialController != "" {
				flow.Arrivals[i].InitialController = resolve(flow.Arrivals[i].InitialController)
			}
			resolveWaypoints(flow.Arrivals[i].Waypoints)
			for _, rwyWps := range flow.Arrivals[i].RunwayWaypoints {
				for _, wps := range rwyWps {
					resolveWaypoints(wps)
				}
			}
		}
		for i := range flow.Overflights {
			if flow.Overflights[i].InitialController != "" {
				flow.Overflights[i].InitialController = resolve(flow.Overflights[i].InitialController)
			}
			resolveWaypoints(flow.Overflights[i].Waypoints)
		}
	}
}

func (sg *scenarioGroup) Similar(fix string) []string {
	d1, d2 := util.SelectInTwoEdits(fix, maps.Keys(sg.Fixes), nil, nil)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Navaids), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Fixes), d1, d2)
	return util.Select(len(d1) > 0, d1, d2)
}

var (
	// "FIX@HDG/DIST"
	reFixHeadingDistance = regexp.MustCompile(`^([\w-]{3,})@([\d]{3})/(\d+(\.\d+)?)$`)
)

func makeCircleAirportFilters(id string, description string, radius float32,
	ceiling int, airports []string, e *util.ErrorLogger) sim.FilterRegions {
	var regions sim.FilterRegions
	for _, apname := range airports {
		ap, ok := av.DB.Airports[apname]
		if !ok {
			e.ErrorString("Airport %q not found", apname)
		}
		if len(apname) == 4 {
			apname = apname[1:]
		}
		regions = append(regions, sim.FilterRegion{
			AirspaceVolume: av.AirspaceVolume{
				Id:          id + apname,
				Description: description + " " + apname,
				Type:        av.AirspaceVolumeCircle,
				Floor:       0,
				Ceiling:     ap.Elevation + ceiling,
				Center:      ap.Location,
				Radius:      radius,
			},
		})
	}
	return regions
}

func makePolygonAirportFilters(id string, description string, delta float32,
	ceiling int, airports []string, nmPerLongitude float32, e *util.ErrorLogger) sim.FilterRegions {
	var regions sim.FilterRegions
	for _, apname := range airports {
		ap, ok := av.DB.Airports[apname]
		if !ok {
			e.ErrorString("Airport %q not found", apname)
		}
		if len(apname) == 4 {
			apname = apname[1:]
		}

		p := util.MapSlice(ap.Runways, func(r av.Runway) [2]float32 { return math.LL2NM(r.Threshold, nmPerLongitude) })
		var hull [][2]float32

		if len(p) == 2 {
			// Single runway so compute an OBB directly.
			v := math.Normalize2f(math.Sub2f(p[1], p[0]))
			v = math.Scale2f(v, delta)
			nv := math.Scale2f(v, -1)
			vp := [2]float32{v[1], -v[0]} // perp
			nvp := math.Scale2f(vp, -1)

			hull = [][2]float32{
				math.Add2f(p[0], math.Add2f(nv, vp)),
				math.Add2f(p[1], math.Add2f(v, vp)),
				math.Add2f(p[1], math.Add2f(v, nvp)),
				math.Add2f(p[0], math.Add2f(nv, nvp))}
		} else {
			// Convex hull of the runway threshold points
			hull = math.ConvexHull(p)

			// Expand the hull by delta: hacky polygon dilation--
			// compute the average point as a center and then offset
			// each away from it.
			var c [2]float32
			for _, p := range hull {
				c = math.Add2f(c, p)
			}
			c = math.Scale2f(c, 1/float32(len(hull)))
			for i := range hull {
				v := math.Sub2f(hull[i], c)
				hull[i] = math.Add2f(hull[i], math.Scale2f(v, delta))
			}
		}

		// Back to lat-long for the AirspaceVolume
		pll := util.MapSlice(hull, func(p [2]float32) math.Point2LL { return math.NM2LL(p, nmPerLongitude) })

		regions = append(regions, sim.FilterRegion{
			AirspaceVolume: av.AirspaceVolume{
				Id:          id + apname,
				Description: description + " " + apname,
				Type:        av.AirspaceVolumePolygon,
				Floor:       0,
				Ceiling:     ap.Elevation + ceiling,
				Vertices:    pll,
			},
		})
	}
	return regions
}

func (sg *scenarioGroup) PostDeserialize(e *util.ErrorLogger, catalogs map[string]map[string]*ScenarioCatalog,
	mapSpec *sim.VideoMapSpec, mapSpecs map[string]*sim.VideoMapSpec) {
	defer e.CheckDepth(e.CurrentDepth())

	// Rewrite legacy files to be TCP-based.
	sg.rewriteControllers(e)

	// config items. This goes first because we need to initialize
	// Center (and thence NmPerLongitude) ASAP.

	e.Push("Facility config " + facilityConfigPath(sg))
	sg.FacilityConfig.FacilityAdaptation.PostDeserialize(sg, e)
	e.Pop()

	sg.NmPerLatitude = 60
	sg.NmPerLongitude = math.NMPerLongitudeAt(sg.FacilityConfig.FacilityAdaptation.Center)

	// Auto-create default airport filters if none were specified in the
	// facility config. This is a scenario-group concern because it uses
	// the scenario group's airport lists.
	fa := &sg.FacilityConfig.FacilityAdaptation
	controlledAirports := slices.Collect(
		util.Seq2Keys(
			util.FilterSeq2(maps.All(sg.Airports), func(name string, ap *av.Airport) bool {
				return len(ap.Departures) > 0 || len(ap.Approaches) > 0
			})))
	allAirports := slices.Collect(maps.Keys(sg.Airports))
	nmPerLongitude := math.NMPerLongitudeAt(fa.Center)

	if len(fa.Filters.ArrivalDrop) == 0 {
		fa.Filters.ArrivalDrop = makePolygonAirportFilters("DROP", "ARRIVAL DROP", 0.35, 500, controlledAirports, nmPerLongitude, e)
	}
	if len(fa.Filters.Departure) == 0 {
		fa.Filters.Departure = makePolygonAirportFilters("DEP", "DEPARTURE", 0.5, 500, controlledAirports, nmPerLongitude, e)
	}
	if len(fa.Filters.InhibitCA) == 0 {
		fa.Filters.InhibitCA = makeCircleAirportFilters("NOCA", "CONFLICT SUPPRESS", 5, 3000, controlledAirports, e)
	}
	if len(fa.Filters.InhibitMSAW) == 0 {
		fa.Filters.InhibitMSAW = makeCircleAirportFilters("NOSA", "MSAW SUPPRESS", 5, 3000, controlledAirports, e)
	}
	if len(fa.Filters.SurfaceTracking) == 0 {
		fa.Filters.SurfaceTracking = makePolygonAirportFilters("SURF", "SURFACE TRACKING", 0.15, 200, allAirports, nmPerLongitude, e)
	}

	// Validate the newly created airport filters (user-defined ones are
	// validated inside fa.PostDeserialize).
	checkAirportFilter := func(f sim.FilterRegions) {
		for i, filt := range f {
			e.Push(filt.Description)
			f[i].AirspaceVolume.PostDeserialize(sg, e)
			e.Pop()
		}
	}
	checkAirportFilter(fa.Filters.ArrivalDrop)
	checkAirportFilter(fa.Filters.Departure)
	checkAirportFilter(fa.Filters.InhibitCA)
	checkAirportFilter(fa.Filters.InhibitMSAW)
	checkAirportFilter(fa.Filters.SurfaceTracking)

	if sg.ARTCC == "" {
		if sg.TRACON == "" {
			e.ErrorString(`"tracon" or "artcc" must be specified`)
		} else if !av.DB.IsFacility(sg.TRACON) {
			e.ErrorString("TRACON %q is unknown; it must be a 3-letter identifier listed at "+
				"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/tracon.",
				sg.TRACON)
		}
	} else if sg.TRACON == "" {
		if sg.ARTCC == "" {
			e.ErrorString(`"artcc" must be specified`)
		}
		if _, ok := av.DB.ARTCCs[sg.ARTCC]; !ok {
			e.ErrorString("ARTCC %q is unknown; it must be a 3-letter identifier listed at "+
				"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/artcc", sg.ARTCC)
		}
		sg.TRACON = sg.ARTCC // TODO: find a better way to do this
	}

	sg.Fixes = make(map[string]math.Point2LL)
	for _, fix := range sg.FixesStrings.Keys() {
		loc, _ := sg.FixesStrings.Get(fix)
		location, ok := loc.(string)
		if !ok {
			e.ErrorString("location for fix %q is not a string: %+v", fix, loc)
			continue
		}

		fix := strings.ToUpper(fix)
		e.Push("Fix  " + fix)

		if _, ok := sg.Fixes[fix]; ok {
			e.ErrorString("fix has multiple definitions")
		} else if strs := reFixHeadingDistance.FindStringSubmatch(location); len(strs) >= 4 {
			// "FIX@HDG/DIST"
			//fmt.Printf("A loc %s -> strs %+v\n", location, strs)
			if pll, ok := sg.Locate(strs[1]); !ok {
				e.ErrorString("base fix %q unknown", strs[1])
			} else if hdg, err := strconv.Atoi(strs[2]); err != nil {
				e.ErrorString("heading %q: %v", strs[2], err)
			} else if dist, err := strconv.ParseFloat(strs[3], 32); err != nil {
				e.ErrorString("distance %q: %v", strs[3], err)
			} else {
				// Offset along the given heading and distance from the fix.
				sg.Fixes[fix] = math.Offset2LL(pll, math.MagneticToTrue(math.MagneticHeading(hdg), sg.MagneticVariation),
					float32(dist), sg.NmPerLongitude)
			}
		} else if pos, ok := sg.Locate(location); ok {
			// It's something simple. Check this after FIX@HDG/DIST,
			// though, since the runway matching KJFK-31L discards stuff
			// after the runway and we don't want that to match in that
			// case.
			sg.Fixes[fix] = pos
		} else {
			e.ErrorString("invalid location syntax %q for fix %q", location, fix)
		}

		// Entries in "fixes" should not shadow navaids, fixes, or airports
		// already in the aviation DB, since Locate will find them anyway.
		if _, ok := sg.Fixes[fix]; ok && !sg.AllowFixRedefinitions {
			if _, ok := av.DB.LookupWaypoint(fix); ok {
				e.ErrorString("fix shadows a navaid/fix in the aviation DB; remove it from \"fixes\"")
			} else if _, ok := av.DB.LookupAirport(fix); ok {
				e.ErrorString("fix shadows an airport in the aviation DB; remove it from \"fixes\"")
			}
		}

		e.Pop()
	}

	e.Push("Facility config " + facilityConfigPath(sg))
	PostDeserializeFacilityAdaptation(&sg.FacilityConfig.FacilityAdaptation, e, sg, mapSpec, mapSpecs)
	e.Pop()

	for name, volumes := range sg.Airspace.Volumes {
		for i, vol := range volumes {
			e.Push("Airspace volume " + name)

			for _, b := range vol.BoundaryNames {
				if pts, ok := sg.Airspace.Boundaries[b]; !ok {
					e.ErrorString("airspace boundary %q not found", b)
				} else {
					sg.Airspace.Volumes[name][i].Boundaries = append(sg.Airspace.Volumes[name][i].Boundaries, pts)
				}
			}

			if vol.Label == "" {
				// Default label if none specified
				if vol.LowerLimit == vol.UpperLimit {
					sg.Airspace.Volumes[name][i].Label = fmt.Sprintf("%d", vol.LowerLimit/100)
				} else {
					sg.Airspace.Volumes[name][i].Label = fmt.Sprintf("%d-%d", vol.LowerLimit/100, vol.UpperLimit/100)
				}
			}
			if vol.LabelPosition.IsZero() {
				// Label at the center if no center specified
				e := math.EmptyExtent2D()
				for _, pts := range sg.Airspace.Volumes[name][i].Boundaries {
					for _, p := range pts {
						e = math.Union(e, p)
					}
				}
				sg.Airspace.Volumes[name][i].LabelPosition = e.Center()
			}

			e.Pop()
		}
	}
	if sg.ARTCC == "" {
		if sg.PrimaryAirport == "" {
			e.ErrorString(`"primary_airport" not specified`)
		} else if ap, ok := av.DB.Airports[sg.PrimaryAirport]; !ok {
			e.ErrorString(`"primary_airport" %q unknown`, sg.PrimaryAirport)
		} else if mvar, err := av.DB.MagneticGrid.Lookup(ap.Location); err != nil {
			e.ErrorString("%s: unable to find magnetic declination: %v", sg.PrimaryAirport, err)
		} else {
			sg.MagneticVariation = mvar + sg.MagneticAdjustment
		}
	} else if mvar, err := av.DB.MagneticGrid.Lookup(sg.FacilityConfig.FacilityAdaptation.Center); err != nil {
		e.ErrorString("%s: unable to find magnetic declination: %v", sg.ARTCC, err)
	} else {
		sg.MagneticVariation = mvar + sg.MagneticAdjustment
	}

	// Resolve short-prefix controller references (e.g. "N5W" -> "NNN5W")
	// before airport/flow validation so they find the canonical entries.
	sg.resolveControllerRefs()

	if len(sg.Airports) == 0 {
		e.ErrorString(`No "airports" specified in scenario group`)
	}
	for name, ap := range sg.Airports {
		e.Push("Airport " + name)
		ap.PostDeserialize(name, sg, sg.NmPerLongitude, sg.MagneticVariation,
			sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.Scratchpads, sg.Airports,
			sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
		e.Pop()
	}

	// Auto-set default_airport if only one airport has converging runways
	var crdaAirport string
	crdaCount := 0
	for name, ap := range sg.Airports {
		if len(ap.CRDAPairs) > 0 {
			crdaAirport = name
			crdaCount++
		}
	}
	if crdaCount == 1 {
		for _, areaConfig := range sg.FacilityConfig.FacilityAdaptation.Areas {
			if areaConfig != nil && areaConfig.DefaultAirport == "" {
				areaConfig.DefaultAirport = crdaAirport
			}
		}
	}

	if _, ok := sg.Scenarios[sg.DefaultScenario]; !ok {
		e.ErrorString(`default scenario %q not found in "scenarios"`, sg.DefaultScenario)
	}

	// Check that neighbor controllers loaded at runtime have facility_id set.
	// (Core controller validation happens in FacilityConfig.PostDeserialize.)
	for position, ctrl := range sg.FacilityConfig.ControlPositions {
		if ctrl.ERAMFacility && sg.ARTCC == "" {
			if ctrl.FacilityIdentifier == "" {
				e.Push("Controller " + string(position))
				e.ErrorString(`must specify "facility_id" for center controller in TRACON scenario group`)
				e.Pop()
			}
		}
	}

	for name, flow := range sg.InboundFlows {
		e.Push("Inbound flow " + name)
		if len(flow.Arrivals) == 0 && len(flow.Overflights) == 0 {
			e.ErrorString("no arrivals or overflights in inbound flow group")
		}

		for i := range flow.Arrivals {
			flow.Arrivals[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
			checkArrivalSpawnAltitude(flow.Arrivals[i], e)
		}
		for i := range flow.Overflights {
			flow.Overflights[i].PostDeserialize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
		}

		e.Pop()
	}

	for _, rp := range sg.ReportingPointStrings {
		if loc, ok := sg.Locate(rp); !ok {
			e.ErrorString(`unknown "reporting_point" %q`, rp)
		} else {
			sg.ReportingPoints = append(sg.ReportingPoints, av.ReportingPoint{Fix: rp, Location: loc})
		}
	}

	for i := range sg.VFRReportingPoints {
		sg.VFRReportingPoints[i].PostDeserialize(sg, sg.FacilityConfig.ControlPositions, e)
	}

	// Do after airports!
	if len(sg.Scenarios) == 0 {
		e.ErrorString(`No "scenarios" specified`)
	}
	for name, s := range sg.Scenarios {
		e.Push("Scenario " + name)
		s.PostDeserialize(sg, e, mapSpec)
		e.Pop()
	}

	initializeSimConfigurations(sg, catalogs, e)
}

func (sg *scenarioGroup) rewriteControllers(e *util.ErrorLogger) {
	// Set Position from map key and derive area for controllers that
	// don't already have them set (neighbor controllers have Position
	// set by loadNeighborControllers).
	for position, ctrl := range sg.FacilityConfig.ControlPositions {
		if ctrl.Position == "" {
			ctrl.Position = string(position)
		}

		// Auto-derive area from the first digit of the Position for
		// TRACON controllers only. Center (ERAM) controllers must have
		// area specified manually in the facility config.
		if !ctrl.ERAMFacility && ctrl.Area == "" && len(ctrl.Position) > 0 && ctrl.Position[0] >= '0' && ctrl.Position[0] <= '9' {
			ctrl.Area = string(ctrl.Position[0])
		}
	}

	// Rebuild the map with PositionId keys (identity for local, prefixed for external).
	pos := make(map[sim.TCP]*av.Controller)
	for _, ctrl := range sg.FacilityConfig.ControlPositions {
		id := sim.TCP(ctrl.PositionId())
		if _, ok := pos[id]; ok {
			e.ErrorString(`%s: TCP / position used for multiple "control_positions"`, ctrl.Position)
		}
		pos[id] = ctrl
	}

	rewriteString := func(s *string) {
		if *s == "" {
			return
		}
		if ctrl, ok := sg.FacilityConfig.ControlPositions[sim.TCP(*s)]; ok {
			*s = string(ctrl.PositionId())
		}
	}
	rewriteControlPosition := func(s *sim.ControlPosition) {
		if *s == "" {
			return
		}
		if ctrl, ok := sg.FacilityConfig.ControlPositions[*s]; ok {
			*s = sim.TCP(ctrl.PositionId())
		}
	}
	rewriteWaypoints := func(wp av.WaypointArray) {
		for _, w := range wp {
			if w.HandoffController() != "" {
				hc := w.HandoffController()
				rewriteControlPosition(&hc)
				w.InitExtra().HandoffController = hc
			}
			for j := range w.ActionGroups() {
				if w.Extra.ActionGroups[j].Actions.HandoffController != "" {
					hc := w.Extra.ActionGroups[j].Actions.HandoffController
					rewriteControlPosition(&hc)
					w.Extra.ActionGroups[j].Actions.HandoffController = hc
				}
			}
		}
	}

	for _, s := range sg.Scenarios {
		if len(s.Airspace) > 0 {
			a := make(map[sim.TCP][]string)
			for ctrl, vols := range s.Airspace {
				rewriteControlPosition(&ctrl)
				a[ctrl] = vols
			}
			s.Airspace = a
		}

		for _, rwy := range s.DepartureRunways {
			if ap, ok := sg.Airports[rwy.Airport]; ok {
				rewriteControlPosition(&ap.DepartureController)
			}
		}

		for _, rwy := range s.ArrivalRunways {
			if rwy.GoAround != nil {
				rewriteControlPosition(&rwy.GoAround.HandoffController)
			}
		}

		// Rewrite Configuration default_consolidation
		newPositions := make(map[sim.TCP][]sim.TCP)
		for parent, children := range s.ControllerConfiguration.DefaultConsolidation {
			rewriteControlPosition(&parent)
			newChildren := make([]sim.TCP, len(children))
			for i, child := range children {
				c := child
				rewriteControlPosition(&c)
				newChildren[i] = c
			}
			newPositions[parent] = newChildren
		}
		s.ControllerConfiguration.DefaultConsolidation = newPositions

		for flow, tcp := range s.ControllerConfiguration.InboundAssignments {
			rewriteControlPosition(&tcp)
			s.ControllerConfiguration.InboundAssignments[flow] = tcp
		}
		for airport, tcp := range s.ControllerConfiguration.DepartureAssignments {
			rewriteControlPosition(&tcp)
			s.ControllerConfiguration.DepartureAssignments[airport] = tcp
		}
		for spec, tcp := range s.ControllerConfiguration.GoAroundAssignments {
			rewriteControlPosition(&tcp)
			s.ControllerConfiguration.GoAroundAssignments[spec] = tcp
		}

		for i := range s.VirtualControllers {
			rewriteControlPosition(&s.VirtualControllers[i])
		}
	}

	for _, ap := range sg.Airports {
		rewriteControlPosition(&ap.DepartureController)

		for _, exitroutes := range ap.DepartureRoutes {
			for _, route := range exitroutes {
				rewriteControlPosition(&route.HandoffController)
				rewriteWaypoints(route.Waypoints)
			}
		}

		for _, app := range ap.Approaches {
			for _, wps := range app.Waypoints {
				rewriteWaypoints(wps)
			}
		}
		for _, dep := range ap.Departures {
			rewriteWaypoints(dep.RouteWaypoints)
		}
	}

	fa := &sg.FacilityConfig.FacilityAdaptation
	for i := range fa.AirspaceAwareness {
		rewriteString(&fa.AirspaceAwareness[i].ReceivingController)
	}
	for _, area := range fa.Areas {
		for i := range area.AirspaceAwareness {
			rewriteString(&area.AirspaceAwareness[i].ReceivingController)
		}
	}
	for position, config := range fa.Controllers {
		// Rewrite controller
		delete(fa.Controllers, position)
		p := string(position)
		rewriteString(&p)
		fa.Controllers[sim.ControlPosition(p)] = config
	}
	// Rewrite TCP references in configurations (controller assignments)
	for _, config := range fa.Configurations {
		for flow, tcp := range config.InboundAssignments {
			rewriteControlPosition(&tcp)
			config.InboundAssignments[flow] = tcp
		}
		for spec, tcp := range config.DepartureAssignments {
			rewriteControlPosition(&tcp)
			config.DepartureAssignments[spec] = tcp
		}
		for spec, tcp := range config.GoAroundAssignments {
			rewriteControlPosition(&tcp)
			config.GoAroundAssignments[spec] = tcp
		}
	}

	for _, flow := range sg.InboundFlows {
		for i := range flow.Arrivals {
			rewriteControlPosition(&flow.Arrivals[i].InitialController)
			rewriteWaypoints(flow.Arrivals[i].Waypoints)
			for _, rwyWps := range flow.Arrivals[i].RunwayWaypoints {
				for _, wps := range rwyWps {
					rewriteWaypoints(wps)
				}
			}
		}
		for i := range flow.Overflights {
			rewriteControlPosition(&flow.Overflights[i].InitialController)
			rewriteWaypoints(flow.Overflights[i].Waypoints)
		}
	}

	// Rewrite TCP references in filter regions.
	for i := range fa.Filters.Quicklook {
		for j := range fa.Filters.Quicklook[i].TCPs {
			rewriteControlPosition(&fa.Filters.Quicklook[i].TCPs[j])
		}
		for j := range fa.Filters.Quicklook[i].OwningTCPs {
			rewriteControlPosition(&fa.Filters.Quicklook[i].OwningTCPs[j])
		}
	}
	for i := range fa.Filters.FDAM {
		for j := range fa.Filters.FDAM[i].TCPs {
			rewriteControlPosition(&fa.Filters.FDAM[i].TCPs[j])
		}
		for j := range fa.Filters.FDAM[i].OwningTCPs {
			rewriteControlPosition(&fa.Filters.FDAM[i].OwningTCPs[j])
		}
		rewriteControlPosition(&fa.Filters.FDAM[i].NewOwnerTCP)
		for j := range fa.Filters.FDAM[i].PointoutTCPs {
			rewriteControlPosition(&fa.Filters.FDAM[i].PointoutTCPs[j])
		}
	}

	sg.FacilityConfig.ControlPositions = pos
}

// PostDeserializeFacilityAdaptation validates FacilityAdaptation fields that
// require the scenario group's Locator, mapSpec, or airport data. Self-contained
// validation is done earlier in FacilityAdaptation.ValidateConfig.
func PostDeserializeFacilityAdaptation(s *sim.FacilityAdaptation, e *util.ErrorLogger, sg *scenarioGroup,
	mapSpec *sim.VideoMapSpec, mapSpecs map[string]*sim.VideoMapSpec) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("facility_adaptations")

	// specForArea returns the effective spec for an area: the area's
	// own mapSpec if it has a video_map_file, otherwise the facility-level one.
	specForArea := func(ac *sim.STARSArea) *sim.VideoMapSpec {
		if ac.VideoMapFile != "" {
			if m, ok := mapSpecs[ac.VideoMapFile]; ok {
				return m
			}
		}
		return mapSpec
	}

	// Validate area-level video_map_file entries exist in mapSpecs.
	for areaNum, ac := range s.Areas {
		if ac.VideoMapFile != "" {
			if _, ok := mapSpecs[ac.VideoMapFile]; !ok {
				e.ErrorString(`video_map_file %q in area %s not found. Options: %s`,
					ac.VideoMapFile, areaNum, strings.Join(util.SortedMapKeys(mapSpecs), ", "))
			}
		}
	}

	// Video maps: validate area-level video_maps against the effective spec.
	for areaNum, ac := range s.Areas {
		m := specForArea(ac)
		for _, name := range ac.VideoMapNames {
			if name != "" && !m.HasMap(name) {
				e.ErrorString(`video map %q in area %s "video_maps" is not a valid video map`, name, areaNum)
			}
		}
		for _, name := range ac.DefaultMaps {
			if name != "" && !m.HasMap(name) {
				e.ErrorString(`video map %q in area %s "default_maps" is not a valid video map`, name, areaNum)
			}
		}
	}

	// Video map labels are validated in fc.validateSTARSAdaptation.

	// A TRACON scenario's facility config must define either controllers or
	// video maps in areas to drive a STARS display. ARTCC scenarios use
	// ERAM and don't need these.
	var allAreaVideoMaps []string
	for _, ac := range s.Areas {
		allAreaVideoMaps = append(allAreaVideoMaps, ac.VideoMapNames...)
	}
	if sg.ARTCC == "" && len(s.Controllers) == 0 && len(allAreaVideoMaps) == 0 {
		e.ErrorString(`must specify either "controllers" or "video_maps" in "areas"`)
	}

	// Controller config centers and video maps (require Locator + mapSpec).
	if len(s.Controllers) > 0 {
		for ctrl, config := range s.Controllers {
			if config.CenterString != "" {
				if pos, ok := sg.Locate(config.CenterString); !ok {
					e.ErrorString(`unknown location %q specified for "center"`, s.CenterString)
				} else {
					config.Center = pos
					s.Controllers[ctrl] = config
				}
			}
		}

		for tcp, config := range s.Controllers {
			// Resolve mapSpec: controller video_map_file > area > facility.
			ctrlSpec := mapSpec
			if config.VideoMapFile != "" {
				if m, ok := mapSpecs[config.VideoMapFile]; ok {
					ctrlSpec = m
				} else {
					e.ErrorString(`video_map_file %q for controller %q not found. Options: %s`,
						config.VideoMapFile, tcp, strings.Join(util.SortedMapKeys(mapSpecs), ", "))
				}
			} else if ctrl, ok := sg.FacilityConfig.ControlPositions[tcp]; ok && ctrl.Area != "" {
				if ac, ok := s.Areas[ctrl.Area]; ok {
					ctrlSpec = specForArea(ac)
				}
			}
			for _, name := range config.DefaultMaps {
				if !ctrlSpec.HasMap(name) {
					e.ErrorString(`video map %q in "default_maps" for controller %q is not a valid video map`,
						name, tcp)
				}
			}
			for _, name := range config.VideoMapNames {
				if name != "" && !ctrlSpec.HasMap(name) {
					e.ErrorString(`video map %q in "video_maps" for controller %q is not a valid video map`,
						name, tcp)
				}
			}
		}
	}

	// Radar sites (require Locator).
	for name, rs := range s.RadarSites {
		e.Push("Radar site " + name)
		if p, ok := sg.Locate(rs.PositionString); rs.PositionString == "" || !ok {
			e.ErrorString("radar site position %q not found", rs.PositionString)
		} else {
			rs.Position = p
		}
		if rs.Char == "" {
			e.ErrorString(`radar site is missing "char"`)
		}
		if rs.Elevation == 0 {
			e.ErrorString(`radar site is missing "elevation"`)
		}
		e.Pop()
	}

	// Coordination fixes (require Locator + DB).
	for fix, fixes := range s.CoordinationFixes {
		e.Push("Coordination fix " + fix)
		// FIXME(mtrokel)
		/*
			if _, ok := sg.Locate(fix); !ok {
				e.ErrorString(`coordination fix "%v" cannot be located`, fix)
			}
		*/
		acceptableTypes := []string{"route", "zone"}
		for i, fix := range fixes {
			e.Push(fmt.Sprintf("Number %v", i))
			if !slices.Contains(acceptableTypes, fix.Type) {
				e.ErrorString(`type "%v" is invalid. Valid types are "route" and "zone"`, fix.Type)
			}
			if fix.Altitude[0] < 0 {
				e.ErrorString(`bottom altitude "%v" is below zero`, fix.Altitude[0])
			}
			if fix.Altitude[0] > fix.Altitude[1] {
				e.ErrorString(`bottom altitude "%v" is higher than the top altitude "%v"`, fix.Altitude[0], fix.Altitude[1])
			}
			if !av.DB.IsFacility(fix.ToFacility) {
				e.ErrorString(`to facility "%v" is invalid`, fix.ToFacility)
			}
			if !av.DB.IsFacility(fix.FromFacility) {
				e.ErrorString(`from facility "%v" is invalid`, fix.FromFacility)
			}
			e.Pop()
		}
		e.Pop()
	}

	// Single char AIDs (require sg.Airports).
	for char, airport := range s.SingleCharAIDs {
		e.Push("Airport ID " + char)
		if _, ok := sg.Airports[airport]; !ok {
			e.ErrorString(`airport %q isn't specified`, airport)
		}
		e.Pop()
	}

	// Significant points (require Locator).
	e.Push(`"significant_points"`)
	if s.SignificantPoints == nil {
		s.SignificantPoints = make(map[string]sim.SignificantPoint)
	}
	for name, sp := range s.SignificantPoints {
		e.Push(name)

		if len(name) < 3 {
			e.ErrorString("name must be at least 3 characters")
		} else {
			sp.Name = name

			if sp.ShortName != "" && len(name) == 3 {
				e.ErrorString(`"short_name" can only be given if name is more than 3 characters.`)
			}
			if len(sp.ShortName) > 3 {
				e.ErrorString(`"short_name" cannot be more than 3 characters.`)
			}
			if sp.Location.IsZero() {
				if p, ok := sg.Locate(name); !ok {
					e.ErrorString("unable to find location of %q", name)
				} else {
					sp.Location = p
				}
			}
		}

		// Update for any changes we made
		s.SignificantPoints[name] = sp

		e.Pop()
	}
	e.Pop()

	// Altimeters (require sg.Airports).
	if len(s.Lists.SSA.Altimeters) > 6 {
		e.ErrorString(`Only 6 airports may be specified for "altimeters"; %d were given`, len(s.Lists.SSA.Altimeters))
	}
	for _, ap := range s.Lists.SSA.Altimeters {
		if _, ok := sg.Airports[ap]; !ok {
			e.ErrorString(`Airport %q in "altimeters" not found in scenario group "airports"`, ap)
		}
	}

	// Hold for release validation (require sg.Airports).
	for airport, ap := range sg.Airports {
		var matches []string
		for _, list := range s.Lists.Coordination {
			if slices.Contains(list.Airports, airport) {
				matches = append(matches, list.Name)
			}
		}

		hfr := ap.HoldForRelease
		for _, rwy := range ap.DepartureRoutes {
			for _, exitRoute := range rwy {
				if exitRoute.HoldForRelease {
					hfr = true
				}
			}
		}

		if hfr {
			// Make sure it's in either zero or one of the coordination lists.
			if len(matches) > 1 {
				e.ErrorString(`Airport %q is in multiple entries in "coordination_lists": %s.`, airport, strings.Join(matches, ", "))
			}
		} else if len(matches) != 0 {
			// And it shouldn't be any if it's not hold for release
			e.ErrorString(`Airport %q isn't "hold_for_release" but is in "coordination_lists": %s.`, airport,
				strings.Join(matches, ", "))
		}
	}

	// Coordination list airports (require sg.Airports).
	for _, list := range s.Lists.Coordination {
		e.Push(`"coordination_lists" ` + list.Name)
		for _, ap := range list.Airports {
			if _, ok := sg.Airports[ap]; !ok {
				e.ErrorString("Airport %q not defined in scenario group.", ap)
			}
		}
		e.Pop()
	}

	// Airspace awareness (require Locator + ControlPositions).
	for _, aa := range s.AirspaceAwareness {
		for _, fix := range aa.Fix {
			if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
				e.ErrorString("%s : fix unknown", fix)
			}
		}

		if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
			e.ErrorString(`lower end of "altitude_range" %d above upper end %d`,
				aa.AltitudeRange[0], aa.AltitudeRange[1])
		}

		if _, ok := sg.FacilityConfig.ControlPositions[sg.resolveController(sim.TCP(aa.ReceivingController))]; !ok {
			e.ErrorString("%s: controller unknown", aa.ReceivingController)
		}

		for _, t := range aa.AircraftType {
			if t != "J" && t != "T" && t != "P" {
				e.ErrorString(`%q: invalid "aircraft_type". Expected "J", "T", or "P".`, t)
			}
		}
	}
	for areaID, area := range s.Areas {
		e.Push("areas[" + areaID + "].airspace_awareness")
		for _, aa := range area.AirspaceAwareness {
			for _, fix := range aa.Fix {
				if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
					e.ErrorString("%s : fix unknown", fix)
				}
			}

			if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
				e.ErrorString(`lower end of "altitude_range" %d above upper end %d`,
					aa.AltitudeRange[0], aa.AltitudeRange[1])
			}

			if _, ok := sg.FacilityConfig.ControlPositions[sg.resolveController(sim.TCP(aa.ReceivingController))]; !ok {
				e.ErrorString("%s: controller unknown", aa.ReceivingController)
			}

			for _, t := range aa.AircraftType {
				if t != "J" && t != "T" && t != "P" {
					e.ErrorString(`%q: invalid "aircraft_type". Expected "J", "T", or "P".`, t)
				}
			}
		}
		e.Pop()
	}

	// Restriction areas: vertex resolution and spatial checks (require Locator).
	e.Push(`"restriction_areas"`)
	for idx, ra := range s.RestrictionAreas {
		if len(ra.VerticesUser) > 0 {
			// Polygons
			if ra.CircleRadius > 0 {
				e.ErrorString(`Cannot specify both "circle_radius" and "vertices".`)
			}

			ra.VerticesUser = ra.VerticesUser.InitializeLocations(sg, sg.NmPerLongitude, sg.MagneticVariation, false, e)
			var verts []math.Point2LL
			for _, v := range ra.VerticesUser {
				verts = append(verts, v.Location)
			}

			nv := len(verts)
			if ra.Closed && nv < 3 {
				e.ErrorString(`At least 3 "vertices" must be given for a closed restriction area.`)
			}
			if !ra.Closed && nv < 2 {
				e.ErrorString(`At least 2 "vertices" must be given for an open restriction area.`)
			}

			ra.Vertices = make([][]math.Point2LL, 1)
			ra.Vertices[0] = verts
			ra.UpdateTriangles()

			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.AverageVertexPosition()
			}
		} else if ra.CircleRadius > 0 {
			// Circle-related checks
			if ra.CircleRadius > 125 {
				e.ErrorString(`"radius" cannot be larger than 125.`)
			}
			if ra.CircleCenter.IsZero() {
				e.ErrorString(`Must specify "circle_center" if "circle_radius" is given.`)
			}
			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.CircleCenter
			}
		} else {
			// Must be text-only
			if (ra.Text[0] != "" || ra.Text[1] != "") && ra.TextPosition.IsZero() {
				e.ErrorString(`Must specify "text_position" with restriction area`)
			}
		}
		s.RestrictionAreas[idx] = ra
	}
	e.Pop()

	e.Pop() // config
}

func initializeSimConfigurations(sg *scenarioGroup, catalogs map[string]map[string]*ScenarioCatalog, e *util.ErrorLogger) {
	facility := sg.TRACON
	if facility == "" {
		facility = sg.ARTCC
	}
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = av.DB.ARTCCForFacility(facility)
	}

	catalog := &ScenarioCatalog{
		Scenarios:        make(map[string]*ScenarioSpec),
		ControlPositions: sg.FacilityConfig.ControlPositions,
		DefaultScenario:  sg.DefaultScenario,
		Facility:         facility,
		ARTCC:            artcc,
		Area:             sg.Area,
		Airports:         util.SortedMapKeys(sg.Airports),
	}

	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range sg.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}
	for name, scenario := range sg.Scenarios {
		if scenario.ConfigurationString == "" {
			continue
		}
		haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(sg.FacilityConfig.FacilityAdaptation.Controllers),
			func(cc *sim.STARSController) bool { return len(cc.FlightFollowingAirspace) > 0 })
		lc := sim.MakeLaunchConfig(scenario.DepartureRunways, *scenario.VFRRateScale, vfrAirports,
			scenario.InboundFlowDefaultRates, haveVFRReportingRegions)

		spec := &ScenarioSpec{
			ControllerConfiguration: &scenario.ControllerConfiguration,
			LaunchConfig:            lc,
			Description:             scenario.Description,
			DepartureRunways:        scenario.DepartureRunways,
			ArrivalRunways:          scenario.ArrivalRunways,
			PrimaryAirport:          sg.PrimaryAirport,
			MagneticVariation:       sg.MagneticVariation,
			WindSpecifier:           scenario.WindSpecifier,
		}

		catalog.Scenarios[name] = spec
	}

	if len(catalog.Scenarios) > 0 {
		if catalogs[facility] == nil {
			catalogs[facility] = make(map[string]*ScenarioCatalog)
		}
		catalogs[facility][sg.Name] = catalog
	}
}

///////////////////////////////////////////////////////////////////////////
// LoadScenarioGroups

func loadScenarioGroup(filesystem fs.FS, path string, e *util.ErrorLogger) *scenarioGroup {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	// Check for duplicate keys in the JSON
	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	// Reject forbidden top-level keys that should now be in the facility config.
	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(contents, &rawKeys); err == nil {
		for _, forbidden := range []string{"config", "facility_adaptations", "control_positions"} {
			if _, ok := rawKeys[forbidden]; ok {
				e.ErrorString("%q must not appear in scenario group files; it belongs in the facility configuration file", forbidden)
			}
		}
	}

	util.CheckJSON[scenarioGroup](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s scenarioGroup
	if err := util.UnmarshalJSONBytes(contents, &s); err != nil {
		e.Error(err)
		return nil
	}
	if s.Name == "" {
		e.ErrorString(`scenario group is missing "name"`)
		return nil
	}
	if s.TRACON == "" && s.ARTCC == "" {
		e.ErrorString(`scenario group is missing "tracon" or "artcc"`)
		return nil
	}
	s.SourceFile = path
	return &s
}

// facilityConfigPath derives the path to the facility configuration file
// from the scenario group's TRACON/ARTCC fields. The convention is:
// configurations/<ARTCC>/<facility>.json where facility is the TRACON
// (for STARS scenarios) or the ARTCC itself (for ERAM scenarios).
func facilityConfigPath(sg *scenarioGroup) string {
	facility := sg.TRACON
	if facility == "" {
		facility = sg.ARTCC
	}
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = av.DB.ARTCCForFacility(sg.TRACON)
	}
	return "configurations/" + artcc + "/" + facility + ".json"
}

// scenarioBriefPath returns the expected path of the scenario brief for
// the given facility (TRACON or ARTCC name). The convention parallels
// facility configurations: briefs/<ARTCC>/<facility>.md.
func scenarioBriefPath(facility string) string {
	artcc := av.DB.ARTCCForFacility(facility)
	if artcc == "" {
		artcc = facility
	}
	return "briefs/" + artcc + "/" + facility + ".md"
}

// facilityConfigCache caches loaded facility configs so that multiple
// scenario groups referencing the same facility (e.g., N90) share one load.
var facilityConfigCache = make(map[string]*sim.FacilityConfig)

// loadFacilityConfig loads and unmarshals a facility configuration file.
// Results are cached so that shared facilities (like N90, referenced by
// jfk.json, lga.json, etc.) are only loaded once. JSON validation
// (duplicate keys, unknown fields) is performed here; call PostDeserialize
// separately for semantic validation.
func loadFacilityConfig(filesystem fs.FS, path string, e *util.ErrorLogger) *sim.FacilityConfig {
	if fc, ok := facilityConfigCache[path]; ok {
		return fc
	}

	e.Push("Facility config " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	hadErrors := e.HaveErrors()
	util.CheckJSON[sim.FacilityConfig](contents, e)
	if !hadErrors && e.HaveErrors() {
		return nil
	}

	var fc sim.FacilityConfig
	if err := util.UnmarshalJSONBytes(contents, &fc); err != nil {
		e.Error(err)
		return nil
	}

	facilityConfigCache[path] = &fc
	return &fc
}

// isARTCC returns true if the facility code looks like an ARTCC
// (starts with "Z" and is 3 characters long, e.g., "ZDC", "ZNY").
func isARTCC(facility string) bool {
	return len(facility) == 3 && strings.HasPrefix(facility, "Z")
}

// loadNeighborControllers loads controllers from a neighboring facility's
// config file and adds them to the scenario group's ControlPositions.
// The neighbor is identified by facility code (e.g., "ABE", "PHL", "ZDC").
// If the neighbor's config file doesn't exist, it's silently skipped since
// not all facilities in the real NAS have configs in this system.
//
// Each neighbor controller gets the canonical (longest) prefix applied to
// its position and FacilityIdentifier so that controllers from different
// facilities don't collide.
// Controllers are stored under only this canonical prefix; shorter
// references are resolved at lookup time via resolveController.
func neighborPrefix(facility string, handoffIDs []sim.HandoffID) string {
	for _, hid := range handoffIDs {
		if hid.ID == facility {
			switch {
			case hid.StarsID != "":
				return hid.StarsID
			case hid.Prefix != "":
				return hid.Prefix
			}
		}
	}
	return ""
}

func loadNeighborControllers(filesystem fs.FS, sg *scenarioGroup, neighbor string,
	handoffIDs []sim.HandoffID, e *util.ErrorLogger) {
	prefix := neighborPrefix(neighbor, handoffIDs)
	if prefix == "" {
		e.ErrorString("TRACON neighbor %s not found in handoff_ids", neighbor)
		return
	}

	// Determine the ARTCC for this neighbor.
	artcc := av.DB.ARTCCForFacility(neighbor)
	if artcc == "" {
		e.Push("Scenario group: " + sg.Name)
		e.ErrorString("unknown facility %s", neighbor)
		e.Pop()
		return
	}

	path := fmt.Sprintf("configurations/%s/%s.json", artcc, neighbor)

	// Check if file exists before trying to load.
	if _, err := fs.Stat(filesystem, path); err != nil {
		return // Neighbor config doesn't exist — not an error.
	}

	fc := loadFacilityConfig(filesystem, path, e)
	if fc == nil {
		return
	}

	// Add neighbor controllers under the full prefix only.
	// Shorter references are resolved at lookup time via resolveController.
	// Don't overwrite existing positions (the primary facility takes precedence).
	neighborIsARTCC := isARTCC(neighbor)
	for position, ctrl := range fc.ControlPositions {
		ctrlCopy := deep.MustCopy(ctrl)
		ctrlCopy.FacilityIdentifier = prefix
		ctrlCopy.Position = string(position)
		if neighborIsARTCC {
			ctrlCopy.ERAMFacility = true
		}
		pid := sim.TCP(ctrlCopy.PositionId())

		if _, exists := sg.FacilityConfig.ControlPositions[pid]; !exists {
			sg.FacilityConfig.ControlPositions[pid] = ctrlCopy
		}
	}
}

// LoadScenarioGroups loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, a scenario file provided on the command line, and runs the
// full startup validation pass: video map references, arrival spawn
// altitudes, and emergencies.json. It doesn't try to do any sort of
// meaningful error handling but it does try to continue on in the
// presence of errors; all errors will be printed and the program will
// exit if there are any.  We'd rather force any errors due to invalid
// scenario definitions to be fixed...
//
// Returns: scenarioGroups, simConfigurations, mapSpecs, scenarioBriefs, extraScenarioErrors
// If the extra scenario file has errors, they are returned in extraScenarioErrors
// and that scenario is not loaded, but execution continues.
func LoadScenarioGroups(extraScenarioFilename string, extraVideoMapFilename string, extraScenarioBriefFilename string,
	e *util.ErrorLogger, lg *log.Logger) (map[string]map[string]*scenarioGroup, map[string]map[string]*ScenarioCatalog, map[string]*sim.VideoMapSpec, *briefRegistry, string) {
	start := time.Now()

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*scenarioGroup)
	briefs := newBriefRegistry()
	catalogs := make(map[string]map[string]*ScenarioCatalog)

	err := util.WalkResources("scenarios", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking scenarios/: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) != ".json" {
			return nil
		}

		s := loadScenarioGroup(fs, path, e)
		if s != nil {
			facility := util.Select(s.TRACON == "", s.ARTCC, s.TRACON)
			if _, ok := scenarioGroups[facility][s.Name]; ok {
				e.ErrorString("%s / %s: scenario redefined", s.TRACON, s.Name)
			} else {
				if scenarioGroups[facility] == nil {
					scenarioGroups[facility] = make(map[string]*scenarioGroup)
				}
				scenarioGroups[facility][s.Name] = s
			}
		}
		return nil
	})
	if err != nil {
		e.Error(err)
	}
	if e.HaveErrors() {
		// Don't keep going since we'll likely crash in the following
		return nil, nil, nil, nil, ""
	}

	// Load the scenario specified on command line, if any.
	// Store it separately so we can validate it with a separate error logger
	var extraScenario *scenarioGroup
	var extraScenarioFacility string
	var extraScenarioErrors string
	if extraScenarioFilename != "" {
		var extraE util.ErrorLogger
		fs := func() fs.FS {
			if filepath.IsAbs(extraScenarioFilename) {
				return util.RootFS{}
			} else {
				return os.DirFS(".")
			}
		}()
		s := loadScenarioGroup(fs, extraScenarioFilename, &extraE)
		if s != nil {
			facility := util.Select(s.TRACON == "", s.ARTCC, s.TRACON)

			// Load and validate facility config for the extra scenario.
			extraResourcesFS := util.GetResourcesFS()
			fc := loadFacilityConfig(extraResourcesFS, facilityConfigPath(s), &extraE)
			if fc != nil {
				fc.PostDeserialize(facilityConfigPath(s), &extraE)
			}
			if fc != nil && !extraE.HaveErrors() {
				s.FacilityConfig = *deep.MustCopy(fc)

				for _, neighbor := range fc.HandoffIDs {
					neighbor := string(neighbor.ID)
					loadNeighborControllers(extraResourcesFS, s, neighbor, fc.HandoffIDs, &extraE)
				}
			}

			// These may have an empty "video_map_file" member, which
			// is automatically patched up here...
			if fc != nil && s.FacilityConfig.FacilityAdaptation.VideoMapFile == "" {
				if extraVideoMapFilename != "" {
					s.FacilityConfig.FacilityAdaptation.VideoMapFile = extraVideoMapFilename
				} else {
					extraE.ErrorString(`%s: no "video_map_file" in scenario and -videomap not specified`,
						extraScenarioFilename)
				}
			}

			// Store the scenario for later validation (don't add to scenarioGroups yet)
			if !extraE.HaveErrors() {
				extraScenario = s
				extraScenarioFacility = facility
			}
		}

		// Capture any errors from the extra scenario
		if extraE.HaveErrors() {
			extraScenarioErrors = extraE.String()
			lg.Warnf("Extra scenario file has errors and will not be loaded: %s", extraScenarioFilename)
		}
	}

	// Load video map specs (header-only) for validation.
	mapSpecs := make(map[string]*sim.VideoMapSpec)
	err = util.WalkResources("videomaps", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking videomaps: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, ".mappack") {
			mapSpecs[path], err = sim.LoadVideoMapSpec(path)
		}

		return err
	})
	if err != nil {
		lg.Errorf("error loading videomaps: %v", err)
		os.Exit(1)
	}

	// Load the video map specified on the command line, if any.
	if extraVideoMapFilename != "" {
		mapSpecs[extraVideoMapFilename], err = sim.LoadVideoMapSpec(extraVideoMapFilename)
		if err != nil {
			lg.Errorf("%s: %v", extraVideoMapFilename, err)
			os.Exit(1)
		}
	}

	lg.Infof("scenario/video map mapSpec load time: %s\n", time.Since(start))

	// validateBriefForFacility runs the facility-has-scenarios check and the brief validation
	// pipeline, returning the per-videomap hashes and whether the facility is known.
	validateBriefForFacility := func(facility string, content []byte, e *util.ErrorLogger) (map[string][]byte, bool) {
		if _, ok := scenarioGroups[facility]; !ok {
			e.ErrorString("brief exists for facility %q which has no scenarios", facility)
			return nil, false
		}

		e.Push(facility + ".md")
		defer e.Pop()

		parsed := brief.ParseMarkdown(content, brief.ParseOptions{})
		for _, msg := range parsed.ParseErrors() {
			e.ErrorString("%s", msg)
		}

		videoMapHashes := make(map[string][]byte)
		for _, filename := range parsed.VideoMapFiles() {
			if mapSpec, ok := mapSpecs[filename]; !ok {
				e.ErrorString("Unknown video map file %q referenced in brief", filename)
			} else if hash, err := mapSpec.Hash(); err != nil {
				e.ErrorString("Unable to compute hash for video map %q: %v", filename, err)
			} else {
				videoMapHashes[filename] = hash
			}
		}
		return videoMapHashes, true
	}

	// Load facility briefs from resources/briefs/<ARTCC>/<facility>.md.
	err = util.WalkResources("briefs", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking briefs/: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		base := filepath.Base(path)
		if filepath.Ext(base) != ".md" || strings.HasPrefix(base, "#") || strings.HasPrefix(base, ".#") {
			return nil
		}

		e.Push("Brief file " + path)
		defer e.Pop()

		facility := strings.ToUpper(strings.TrimSuffix(base, ".md"))
		if expected := scenarioBriefPath(facility); path != expected {
			e.ErrorString("brief for %q expected at %q, found at %q", facility, expected, path)
			return nil
		}

		if content, err := fs.ReadFile(filesystem, path); err != nil {
			e.Error(err)
		} else if hashes, ok := validateBriefForFacility(facility, content, e); ok {
			briefs.register(facility, hashes, "")
		}

		return nil
	})
	if err != nil {
		e.Error(err)
	}

	// Phase 1: Load and validate all facility configs by walking the
	// configurations/ directory. Every .json file is loaded and validated
	// via PostDeserialize, regardless of whether a scenario references it.
	resourcesFS := util.GetResourcesFS()
	err = util.WalkResources("configurations", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking configurations/: %v", err)
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}

		fc := loadFacilityConfig(filesystem, path, e)
		if fc != nil {
			fc.PostDeserialize(path, e)
		}
		return nil
	})
	if err != nil {
		e.Error(err)
	}

	// Phase 2: Attach validated configs to scenario groups and load
	// neighbor controllers. No further config validation is done here.
	for _, tracon := range scenarioGroups {
		for name, sg := range tracon {
			fc := loadFacilityConfig(resourcesFS, facilityConfigPath(sg), e)
			if fc == nil {
				delete(tracon, name)
				continue
			}

			sg.FacilityConfig = *deep.MustCopy(fc)

			// Add missing airports referenced by altimeters and coordination
			// lists from sibling scenario groups. The facility config is
			// shared across all scenario groups for a TRACON, but sub-area
			// scenarios only define a subset of airports.
			addFromSibling := func(airport string) {
				if _, ok := sg.Airports[airport]; ok {
					return
				}
				for _, sibling := range tracon {
					if sibling == sg {
						continue
					}
					if _, ok := sibling.Airports[airport]; ok {
						if sg.Airports == nil {
							sg.Airports = make(map[string]*av.Airport)
						}
						sg.Airports[airport] = &av.Airport{} // This is an uninitialized, empty airport that is soley used for altimiter and coordination lists so that they're consistent across areas of a TRACON.
						// For example, for the N90 ISP files, the EWR and LGA airports aren't defined, so when their altimeter and coorindation lists were called from the N90 configuration file, there was no defined airport.
						return
					}
				}
			}
			for _, ap := range sg.FacilityConfig.FacilityAdaptation.Lists.SSA.Altimeters {
				addFromSibling(ap)
			}
			for _, cl := range sg.FacilityConfig.FacilityAdaptation.Lists.Coordination {
				for _, ap := range cl.Airports {
					addFromSibling(ap)
					// Airports in coordination lists must be hold for release.
					if a, ok := sg.Airports[ap]; ok && !a.HoldForRelease {
						a.HoldForRelease = true
					}
				}
			}

			// Load controllers from neighboring facilities.
			for _, neighborFac := range fc.HandoffIDs {
				neighbor := string(neighborFac.ID)
				loadNeighborControllers(resourcesFS, sg, neighbor, fc.HandoffIDs, e)
			}
		}
	}

	// Final tidying before we return the loaded scenarios.
	for tname, tracon := range scenarioGroups {
		e.Push("TRACON " + tname)

		scenarioNames := make(map[string]string)

		for groupName, sgroup := range tracon {
			e.Push(sgroup.SourceFile)
			e.Push("Scenario group " + groupName)

			// Make sure the same scenario name isn't used in multiple
			// group definitions.
			for scenarioName := range sgroup.Scenarios {
				if other, ok := scenarioNames[scenarioName]; ok {
					e.ErrorString("scenario %q is also defined in the %q scenario group",
						scenarioName, other)
				}
				scenarioNames[scenarioName] = groupName
			}

			// Make sure we have what we need in terms of video maps
			fa := &sgroup.FacilityConfig.FacilityAdaptation
			if vf := fa.VideoMapFile; vf == "" {
				e.ErrorString(`no "video_map_file" specified`)
			} else if mapSpec, ok := mapSpecs[vf]; !ok {
				e.ErrorString("no mapSpec for video map %q found. Options: %s", vf,
					strings.Join(util.SortedMapKeys(mapSpecs), ", "))
			} else {
				sgroup.PostDeserialize(e, catalogs, mapSpec, mapSpecs)
			}

			e.Pop() // Scenario group
			e.Pop() // SourceFile
		}
		e.Pop() // TRACON
	}

	// Validate the extra scenario separately with its own error logger
	if extraScenario != nil {
		var extraE util.ErrorLogger
		extraE.Push(extraScenario.SourceFile)
		extraE.Push("TRACON " + extraScenarioFacility)
		extraE.Push("Scenario group " + extraScenario.Name)

		// Make sure we have what we need in terms of video maps
		fa := &extraScenario.FacilityConfig.FacilityAdaptation
		if vf := fa.VideoMapFile; vf == "" {
			extraE.ErrorString(`no "video_map_file" specified`)
		} else if mapSpec, ok := mapSpecs[vf]; !ok {
			extraE.ErrorString("no mapSpec for video map %q found. Options: %s", vf,
				strings.Join(util.SortedMapKeys(mapSpecs), ", "))
		} else {
			extraScenario.PostDeserialize(&extraE, catalogs, mapSpec, mapSpecs)
		}

		extraE.Pop() // Scenario group
		extraE.Pop() // TRACON
		extraE.Pop() // SourceFile

		if extraE.HaveErrors() {
			extraScenarioErrors = extraE.String()
			lg.Warnf("Extra scenario file has validation errors and will not be loaded: %s", extraScenarioFilename)
		} else {
			// Only add to scenarioGroups if validation succeeded
			if scenarioGroups[extraScenarioFacility] == nil {
				scenarioGroups[extraScenarioFacility] = make(map[string]*scenarioGroup)
			}
			scenarioGroups[extraScenarioFacility][extraScenario.Name] = extraScenario
		}
	}

	// Load extra scenario brief file from command line, if any. Must run after the extra scenario
	// (if any) has been inserted into scenarioGroups.
	if extraScenarioBriefFilename != "" {
		var extraE util.ErrorLogger
		extraE.Push("Extra brief file " + extraScenarioBriefFilename)

		facility := strings.ToUpper(strings.TrimSuffix(filepath.Base(extraScenarioBriefFilename), ".md"))

		if content, err := os.ReadFile(extraScenarioBriefFilename); err != nil {
			extraE.Error(err)
		} else if hashes, ok := validateBriefForFacility(facility, content, &extraE); ok && !extraE.HaveErrors() {
			briefs.register(facility, hashes, extraScenarioBriefFilename)
		}

		extraE.Pop()

		if extraE.HaveErrors() {
			if extraScenarioErrors != "" {
				extraScenarioErrors += "\n"
			}
			extraScenarioErrors += extraE.String()
			lg.Warnf("Extra scenario brief file has errors and will not be loaded: %s", extraScenarioBriefFilename)
		}
	}

	// Walk all of the scenario groups to get all of the possible departing aircraft
	// types to see where V2 is needed in the performance database..
	acTypes := make(map[string]struct{})

	for _, tracon := range scenarioGroups {
		for _, sg := range tracon {
			for _, ap := range sg.Airports {
				for _, dep := range ap.Departures {
					for _, al := range dep.Airlines {
						for _, ac := range al.Aircraft() {
							acTypes[ac.ICAO] = struct{}{}
						}
					}
				}
			}
		}
	}
	var missing []string
	for t := range util.SortedMap(acTypes) {
		if av.DB.AircraftPerformance[t].Speed.V2 == 0 {
			missing = append(missing, t)
		}
	}
	lg.Infof("Missing V2 in performance database: %s", strings.Join(missing, ", "))

	loadEmergencies(e)

	return scenarioGroups, catalogs, mapSpecs, briefs, extraScenarioErrors
}

// ListAllScenarios returns a sorted list of all available scenarios in TRACON/scenario format
func ListAllScenarios(scenarioFilename, videoMapFilename string, lg *log.Logger) ([]string, error) {
	var e util.ErrorLogger
	scenarioGroups, _, _, _, _ := LoadScenarioGroups(scenarioFilename, videoMapFilename, "", &e, lg)
	if e.HaveErrors() {
		return nil, fmt.Errorf("failed to load scenarios")
	}

	var scenarios []string
	for tracon, groups := range scenarioGroups {
		for _, group := range groups {
			for scenarioName := range group.Scenarios {
				scenarios = append(scenarios, tracon+"/"+scenarioName)
			}
		}
	}

	slices.Sort(scenarios)
	return scenarios, nil
}

// LookupScenario finds a scenario configuration by TRACON/scenario name
func LookupScenario(tracon, scenarioName string, scenarioGroups map[string]map[string]*scenarioGroup, catalogs map[string]map[string]*ScenarioCatalog) (*ScenarioCatalog, *scenarioGroup, error) {
	if groups, ok := scenarioGroups[tracon]; ok {
		for _, group := range groups {
			if _, ok := group.Scenarios[scenarioName]; ok {
				if facilityCatalogs, ok := catalogs[tracon]; ok {
					for _, catalog := range facilityCatalogs {
						if catalog.Scenarios[scenarioName] != nil {
							return catalog, group, nil
						}
					}
				}
			}
		}
	}
	return nil, nil, fmt.Errorf("scenario not found: %s/%s", tracon, scenarioName)
}

// CreateLaunchConfig creates a properly initialized LaunchConfig from scenario data
func CreateLaunchConfig(scenario *scenario, scenarioGroup *scenarioGroup) sim.LaunchConfig {
	// Create VFR airports map
	vfrAirports := make(map[string]*av.Airport)
	for name, ap := range scenarioGroup.Airports {
		if ap.VFRRateSum() > 0 {
			vfrAirports[name] = ap
		}
	}

	// Check for VFR reporting regions
	haveVFRReportingRegions := util.SeqContainsFunc(maps.Values(scenarioGroup.FacilityConfig.FacilityAdaptation.Controllers),
		func(cfg *sim.STARSController) bool { return cfg.FlightFollowingAirspace != nil })

	// Create proper LaunchConfig
	return sim.MakeLaunchConfig(
		scenario.DepartureRunways,
		util.Select(scenario.VFRRateScale == nil, 1.0, *scenario.VFRRateScale),
		vfrAirports,
		scenario.InboundFlowDefaultRates,
		haveVFRReportingRegions,
	)
}

// CreateNewSimConfiguration creates a NewSimConfiguration from scenario components
func CreateNewSimConfiguration(catalog *ScenarioCatalog, scenarioGroup *scenarioGroup, scenarioName string) (*sim.NewSimConfiguration, error) {
	scenario, ok := scenarioGroup.Scenarios[scenarioName]
	if !ok {
		return nil, fmt.Errorf("scenario %s not found in group", scenarioName)
	}

	simConfig := catalog.Scenarios[scenarioName]
	if simConfig == nil {
		return nil, fmt.Errorf("scenario configuration %s not found", scenarioName)
	}

	newSimConfig := &sim.NewSimConfiguration{
		Facility:                scenarioGroup.TRACON,
		Description:             scenarioName,
		LaunchConfig:            CreateLaunchConfig(scenario, scenarioGroup),
		DepartureRunways:        simConfig.DepartureRunways,
		ArrivalRunways:          simConfig.ArrivalRunways,
		PrimaryAirport:          simConfig.PrimaryAirport,
		Airports:                scenarioGroup.Airports,
		Fixes:                   scenarioGroup.Fixes,
		VFRReportingPoints:      scenarioGroup.VFRReportingPoints,
		ControlPositions:        scenarioGroup.FacilityConfig.ControlPositions,
		ControllerConfiguration: &scenario.ControllerConfiguration,
		ConfigurationId:         scenario.ConfigurationString,
		InboundFlows:            scenarioGroup.InboundFlows,
		FacilityAdaptation:      deep.MustCopy(scenarioGroup.FacilityConfig.FacilityAdaptation),
		ReportingPoints:         scenarioGroup.ReportingPoints,
		MagneticVariation:       scenarioGroup.MagneticVariation,
		NmPerLongitude:          scenarioGroup.NmPerLongitude,
		WindSpecifier:           scenario.WindSpecifier,
		Center:                  util.Select(scenario.Center.IsZero(), scenarioGroup.FacilityConfig.FacilityAdaptation.Center, scenario.Center),
		Range:                   util.Select(scenario.Range == 0, scenarioGroup.FacilityConfig.FacilityAdaptation.Range, scenario.Range),
		ScenarioCenter:          scenario.Center,
		ScenarioRange:           scenario.Range,
		DefaultMaps:             scenario.DefaultMaps,
		DefaultMapGroup:         scenario.DefaultMapGroup,
		Airspace:                scenarioGroup.Airspace,
		ControllerAirspace:      scenario.Airspace,
		VirtualControllers:      scenario.VirtualControllers,
		HandoffIDs:              scenarioGroup.FacilityConfig.HandoffIDs,
		FixPairs:                scenarioGroup.FacilityConfig.FixPairs,
	}

	// Resolve fix pair assignments from the selected configuration
	if scenario.ConfigurationString != "" {
		if config, ok := scenarioGroup.FacilityConfig.FacilityAdaptation.Configurations[scenario.ConfigurationString]; ok {
			newSimConfig.FixPairAssignments = config.FixPairAssignments
		}
	}

	// LoadScenarioGroups already validated emergencies.json; re-parse to hand the list to the sim.
	newSimConfig.Emergencies = loadEmergencies(nil)

	return newSimConfig, nil
}

// checkArrivalSpawnAltitude flags an arrival whose initial altitude is
// too high to meet its first "at or below" restriction given the distance
// to that waypoint. Assumes 2500 fpm descent at 250 kts ground speed.
func checkArrivalSpawnAltitude(arr av.Arrival, e *util.ErrorLogger) {
	if arr.AssignedAltitude > 0 {
		return
	}
	if len(arr.Waypoints) == 0 {
		return
	}

	spawnAlt := arr.InitialAltitude
	if spawnAlt == 0 {
		return
	}

	dist := float32(0)

	for i, wp := range arr.Waypoints {
		if i > 0 {
			dist += math.NMDistance2LL(arr.Waypoints[i-1].Location, wp.Location)
		}
		if wp.Flags&av.WaypointFlagHasAltRestriction == 0 {
			continue
		}
		restr := wp.AltRestriction

		// Only care about "at or below" constraints that require descent.
		upperBound := restr.Range[1]
		if upperBound == av.MaxAltitude || spawnAlt <= upperBound {
			continue
		}

		// Conservative estimate: 2500 fpm descent at 250 kts ground speed.
		const descentRate = 2500
		const gs = 250
		eta := float32(dist) / gs * 3600 // seconds
		maxDescent := float32(descentRate) * eta / 60

		needed := spawnAlt - upperBound
		if needed > maxDescent {
			e.ErrorString("arrival %s spawns at %.0f ft but restriction [%.0f,%.0f] at %s is %.1f nm away "+
				"(need %.0f ft descent, max achievable ~%.0f ft)",
				arr.STAR, spawnAlt,
				restr.Range[0], restr.Range[1], wp.Fix, dist,
				needed, maxDescent)
		}

		// Only check the first altitude restriction that requires descent.
		return
	}
}

// loadEmergencies loads and validates the emergencies.json resource file.
// Errors are reported via the ErrorLogger.
func loadEmergencies(e *util.ErrorLogger) []sim.Emergency {
	e.Push("File emergencies.json")
	defer e.Pop()

	r := util.LoadResource("emergencies.json")
	defer r.Close()

	var emap map[string][]sim.Emergency // "emergencies": [ ... ]
	if err := util.UnmarshalJSON(r, &emap); err != nil {
		e.Error(err)
		return nil
	}
	emergencies := emap["emergencies"]

	if len(emergencies) == 0 {
		e.ErrorString(`No "emergencies" found`)
		return nil
	}

	namesSeen := make(map[string]struct{})
	for i := range emergencies {
		em := &emergencies[i] // so we can modify it...

		if _, ok := namesSeen[em.Name]; ok {
			e.ErrorString("Duplicate emergency name %q", em.Name)
			continue
		}
		namesSeen[em.Name] = struct{}{}

		e.Push(em.Name)

		// Default weight to 1.0 if not specified
		if em.Weight == 0 {
			em.Weight = 1
		}

		if em.ApplicableToString == "" {
			e.ErrorString("missing required field 'applicable_to'")
		} else {
			for typeStr := range strings.SplitSeq(em.ApplicableToString, ",") {
				typeStr = strings.TrimSpace(typeStr)
				switch typeStr {
				case "departure":
					em.ApplicableTo |= sim.EmergencyApplicabilityDeparture
				case "arrival":
					em.ApplicableTo |= sim.EmergencyApplicabilityArrival
				case "external":
					em.ApplicableTo |= sim.EmergencyApplicabilityExternal
				case "approach":
					em.ApplicableTo |= sim.EmergencyApplicabilityApproach
				default:
					e.ErrorString(`invalid "applicable_to" value %q: must be one or more of "departure", "arrival", "external", "approach" (comma-separated)`,
						typeStr)
				}
			}
		}

		if len(em.Stages) == 0 {
			e.ErrorString(`no emergency "stages" defined`)
		}
		for i, stage := range em.Stages {
			// transmission is required unless request_return is true
			if stage.Transmission == "" && !stage.RequestReturn {
				e.ErrorString(`stage %d missing required field "transmission"`, i)
			}
			// duration_minutes is required for all stages except the last one
			isLastStage := i == len(em.Stages)-1
			if !isLastStage {
				if stage.DurationMinutes[1] == 0 {
					e.ErrorString(`stage %d missing required field "duration_minutes"`, i)
				}
				if stage.DurationMinutes[0] > stage.DurationMinutes[1] {
					e.ErrorString(`First value in "duration_minutes" cannot be greater than second`)
				}
			}
		}
		e.Pop()
	}

	return emergencies
}
