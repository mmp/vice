// scenario/scenario.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
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

func (s *Scenario) Finalize(sg *Group, e *util.ErrorLogger, mapSpec *videomaps.LibrarySpec) {
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

		// A scenario may override the facility configuration's consolidation
		// tree; otherwise fall back to the configuration's. A scenario-provided
		// override is validated the same way facility configurations are (the
		// fallback was already validated at config load).
		if len(s.DefaultConsolidation) > 0 {
			s.DefaultConsolidation.Validate(sg.FacilityConfig.ControlPositions, e)
			s.ControllerConfiguration.DefaultConsolidation = deep.MustCopy(s.DefaultConsolidation)
		} else {
			s.ControllerConfiguration.DefaultConsolidation = deep.MustCopy(config.DefaultConsolidation)
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

		// Check that every flow with generic /ho handoffs has an assignment.
		// Note: It is NOT an error if the configuration has excess assignments that the scenario doesn't use.
		for flowName := range s.InboundFlowDefaultRates {
			if flow, ok := sg.InboundFlows[flowName]; ok && flowNeedsHumanAssignment(flow) {
				if _, ok := s.ControllerConfiguration.InboundAssignments[flowName]; !ok {
					e.ErrorString(`inbound flow %q needs human controller but has no assignment in "inbound_assignments"`, flowName)
				}
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
			if _, ok := sg.Airports[av.ICAOAirportCode(airport)]; !ok {
				e.ErrorString("go_around_assignments: airport %q not in scenario", airport)
			} else if hasRunway && !av.AirportHasRunway(av.ICAOAirportCode(airport), av.RunwayID(runway)) {
				e.ErrorString("go_around_assignments: runway %q not a valid runway at %q", runway, airport)
			}
		}
	}

	for ctrl, vnames := range s.Airspace {
		e.Push("airspace")

		// Only a position a human may staff can be given airspace; one that
		// isn't in the consolidation tree is virtual.
		if !slices.Contains(s.ControllerConfiguration.AllPositions(), ctrl) {
			e.ErrorString(`Controller %q is not in "default_consolidation" for this scenario, `+
				`so it is a virtual position and cannot be given airspace`, ctrl)
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
	addControllersFromWaypoints := func(route av.WaypointArray) {
		for _, tcp := range route.HandoffControllers() {
			addController(sim.TCP(tcp))
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

	for _, rwy := range s.DepartureRunways {
		e.Push("Departure runway " + string(rwy.Airport) + " " + string(rwy.Runway))

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString(`airport not found in scenario group "airports"`)
		} else {
			if routes, ok := ap.DepartureRoutes[rwy.Runway]; !ok {
				e.ErrorString("runway departure routes not found")
			} else {
				for _, exitRoutes := range routes {
					for _, r := range exitRoutes {
						addControllersFromWaypoints(r.Waypoints)
					}
				}
			}

			// An airport with no "departures" is authored for published traffic
			// only; its exits then come from "departure_routes" alone.
			if rwy.Category != "" {
				var found bool
				if len(ap.Departures) > 0 {
					found = slices.ContainsFunc(ap.Departures, func(dep av.Departure) bool {
						return ap.ExitCategory(dep.Exit) == rwy.Category
					})
				} else {
					found = util.SeqContainsFunc(maps.Keys(ap.DepartureRoutes[rwy.Runway]),
						func(exit av.ExitID) bool { return ap.ExitCategory(exit) == rwy.Category })
				}
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
		e.Push("Arrival runway " + string(rwy.Airport) + " " + string(rwy.Runway))

		if ap, ok := sg.Airports[rwy.Airport]; !ok {
			e.ErrorString(`airport not found in scenario group "airports"`)
		} else {
			activeAirports[ap] = nil

			approaches := ap.ApproachesToRunway(rwy.Runway.Base())
			if len(approaches) == 0 {
				e.ErrorString("no approach found that reaches this runway")
			}

			// Validate go_around procedure if specified
			if rwy.GoAround != nil {
				e.Push("go_around")

				// Resolve heading: 0 means runway heading, otherwise must be 1-360
				if rwy.GoAround.Heading == 0 {
					rwy.GoAround.IsRunwayHeading = true
					if len(approaches) > 0 {
						rwy.GoAround.Heading = int(math.TrueToMagnetic(approaches[0].RunwayHeading(sg.NmPerLongitude),
							sg.MagneticVariation) + 0.5)
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
	activeAirportSIDs := make(map[av.ICAOAirportCode]map[string]any)
	activeAirportRunways := make(map[av.ICAOAirportCode]map[string]any)
	activeDepartureAirports := make(map[av.ICAOAirportCode]any)
	for _, rwy := range s.DepartureRunways {
		e.Push("departure runway " + string(rwy.Runway))

		ap, ok := sg.Airports[rwy.Airport]
		if !ok {
			e.ErrorString(`%s: airport not found in "airports"`, rwy.Airport)
		} else {
			activeAirports[ap] = nil
			activeDepartureAirports[rwy.Airport] = nil

			// A categorized runway launches nothing through an exit with no
			// category, so the two must be authored together.
			exitRoutes := ap.DepartureRoutes[rwy.Runway]
			if rwy.Category != "" {
				for _, fix := range util.SortedMapKeys(exitRoutes) {
					if ap.ExitCategory(fix) == "" {
						sids := util.MapSlice(exitRoutes[fix], func(r *av.ExitRoute) string { return r.SID })
						e.ErrorString(`exit fix %q (SID %s) has no entry in "exit_categories" but runway uses category %q`,
							fix, strings.Join(sids, ", "), rwy.Category)
					}
				}
			}

			if ap.DepartureController != "" {
				addController(sim.TCP(ap.DepartureController))
			} else {
				// Only check for a human controller to be covering the track if there isn't
				// a virtual controller assigned to it.
				for fix, routes := range exitRoutes {
					if rwy.Category != "" && ap.ExitCategory(fix) != rwy.Category {
						continue
					}
					for _, route := range routes {
						if route.DepartureController != "" {
							// The route names the controller its departures start
							// with, so as with the airport-wide setting there is no
							// human assignment to look for.
							addController(sim.TCP(route.DepartureController))
							continue
						}
						if activeAirportSIDs[rwy.Airport] == nil {
							activeAirportSIDs[rwy.Airport] = make(map[string]any)
						}
						if activeAirportRunways[rwy.Airport] == nil {
							activeAirportRunways[rwy.Airport] = make(map[string]any)
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
	assignedSIDs := make(map[av.ICAOAirportCode]map[string]any)    // airport -> set of SIDs
	assignedRunways := make(map[av.ICAOAirportCode]map[string]any) // airport -> set of runways
	hasAirportFallback := make(map[av.ICAOAirportCode]bool)        // airport -> has plain airport assignment

	for spec := range s.ControllerConfiguration.DepartureAssignments {
		apname, sidRunway, haveSIDRunway := strings.Cut(spec, "/")
		ap := av.ICAOAirportCode(apname)

		// Only process assignments for airports that are active in this scenario
		// and need human controller assignments (i.e., are in activeAirportSIDs)
		sids, isActiveHumanAirport := activeAirportSIDs[ap]
		if !isActiveHumanAirport {
			// Skip - either not an active departure airport, or has virtual controller
			continue
		}

		if haveSIDRunway {
			// Track assigned SIDs and runways per airport (only if active in this scenario)
			_, okSID := sids[sidRunway]
			_, okRunway := activeAirportRunways[ap][sidRunway]

			if okSID {
				if assignedSIDs[ap] == nil {
					assignedSIDs[ap] = make(map[string]any)
				}
				assignedSIDs[ap][sidRunway] = nil
			}
			if okRunway {
				if assignedRunways[ap] == nil {
					assignedRunways[ap] = make(map[string]any)
				}
				assignedRunways[ap][sidRunway] = nil
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
					airport := av.ICAOAirportCode(category)
					e.Push("Airport " + category)
					if _, ok := sg.Airports[airport]; !ok {
						e.ErrorString("unknown arrival airport")
					} else {
						// Make sure the airport exists in at least one of the
						// arrivals in the group.
						found := false
						for _, ar := range flow.Arrivals {
							if slices.Contains(ar.Airports, airport) {
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

	// The SSA ALTSTG field shows the system altimeter for the position being
	// worked: the area's if it adapts one, otherwise the facility's. A
	// facility config may be shared by groups working different parts of it,
	// so only check the ones this scenario's own positions resolve to.
	if sg.ARTCC == "" {
		fa := &sg.FacilityConfig.FacilityAdaptation
		reported := make(map[av.ICAOAirportCode]bool)
		for _, tcp := range humanPositions {
			airport, what := fa.Lists.SSA.SystemAltimeter, `"system_altimeter"`
			if ctrl, ok := sg.FacilityConfig.ControlPositions[tcp]; ok {
				if area, ok := fa.Areas[ctrl.Area]; ok && area.SystemAltimeter != "" {
					airport = area.SystemAltimeter
					what = fmt.Sprintf(`area %s "system_altimeter"`, ctrl.Area)
				}
			}
			if reported[airport] {
				continue
			}
			reported[airport] = true
			if airport == "" {
				e.ErrorString("controller %q: no %s adapted for its area or the facility", tcp, what)
			} else if _, ok := sg.Airports[airport]; !ok {
				e.ErrorString(`Airport %q in %s not found in scenario group "airports"`, airport, what)
			}
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
	if s.VFFRequestRate == nil { // unspecified -> default to 10 per hour
		ten := int32(10)
		s.VFFRequestRate = &ten
	}
}
