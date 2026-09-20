// scenario/finalize.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Finalizing turns the freshly parsed JSON of a scenario group into
// something a sim can be started from: controller references resolved to
// their canonical form, the facility adaptation checked, fixes and airspace
// located, routes spliced onto the procedures they join, and every reference
// between them validated. This file holds the order it happens in; the work
// for each type is a Finalize method on the type itself.

package scenario

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

func (sg *Group) Finalize(e *util.ErrorLogger, catalogs map[string]map[string]*Catalog,
	mapSpec *videomaps.LibrarySpec, mapSpecs map[string]*videomaps.LibrarySpec) {
	defer e.CheckDepth(e.CurrentDepth())

	// Rewrite legacy files to be TCP-based.
	sg.rewriteControllers(e)

	// config items. This goes first because we need to initialize
	// Center (and thence NmPerLongitude) ASAP.

	e.Push("Facility config " + facilityConfigPath(sg))
	sg.FacilityConfig.FacilityAdaptation.Finalize(sg, e)
	e.Pop()

	sg.NmPerLatitude = 60
	sg.NmPerLongitude = math.NMPerLongitudeAt(sg.FacilityConfig.FacilityAdaptation.Center)

	// Create default airport filters for the airports the facility config
	// doesn't cover itself. This is a scenario-group concern because it uses
	// the scenario group's airport lists.
	fa := &sg.FacilityConfig.FacilityAdaptation
	// Sorted so that the regions come out in a consistent order; STARS assigns
	// system map ids to them by walking the slices.
	allAirports := util.SortedMapKeys(sg.Airports)
	ifrAirports := util.FilterSlice(allAirports, func(name av.ICAOAirportCode) bool {
		return sg.Airports[name].HasIFROperations()
	})
	nmPerLongitude := math.NMPerLongitudeAt(fa.Center)

	// An airport that one of the config's own regions already covers doesn't
	// get a default one.
	uncovered := func(regions sim.FilterRegions, airports []av.ICAOAirportCode) []av.ICAOAirportCode {
		return util.FilterSlice(airports, func(name av.ICAOAirportCode) bool {
			ap, ok := av.DB.Airports[name]
			return !ok || !regions.Inside(ap.Location, ap.Elevation)
		})
	}
	arrivalDrop := makePolygonAirportFilters("DROP", "ARRIVAL DROP", 0.35, 500,
		uncovered(fa.Filters.ArrivalDrop, ifrAirports), nmPerLongitude, e)
	departure := makePolygonAirportFilters("DEP", "DEPARTURE", 0.5, 500,
		uncovered(fa.Filters.Departure, ifrAirports), nmPerLongitude, e)
	inhibitCA := makeCircleAirportFilters("NOCA", "CONFLICT SUPPRESS", 5, 3000,
		uncovered(fa.Filters.InhibitCA, ifrAirports), e)
	inhibitMSAW := makeCircleAirportFilters("NOSA", "MSAW SUPPRESS", 5, 3000,
		uncovered(fa.Filters.InhibitMSAW, ifrAirports), e)
	surfaceTracking := makePolygonAirportFilters("SURF", "SURFACE TRACKING", 0.15, 200,
		uncovered(fa.Filters.SurfaceTracking, allAirports), nmPerLongitude, e)

	// Validate the newly created airport filters (the config's own are
	// validated inside fa.Finalize), mark them as vice's rather than the
	// adaptation's, and add them to the adapted ones.
	addAirportFilters := func(regions, created sim.FilterRegions) sim.FilterRegions {
		for i := range created {
			e.Push(created[i].Description)
			created[i].AirspaceVolume.Finalize(sg, e)
			e.Pop()
			created[i].Default = true
		}
		return append(regions, created...)
	}
	fa.Filters.ArrivalDrop = addAirportFilters(fa.Filters.ArrivalDrop, arrivalDrop)
	fa.Filters.Departure = addAirportFilters(fa.Filters.Departure, departure)
	fa.Filters.InhibitCA = addAirportFilters(fa.Filters.InhibitCA, inhibitCA)
	fa.Filters.InhibitMSAW = addAirportFilters(fa.Filters.InhibitMSAW, inhibitMSAW)
	fa.Filters.SurfaceTracking = addAirportFilters(fa.Filters.SurfaceTracking, surfaceTracking)

	if sg.TRACON == "" && sg.ARTCC == "" {
		e.ErrorString(`"tracon" or "artcc" must be specified`)
	} else if sg.TRACON != "" && sg.ARTCC != "" {
		e.ErrorString(`only one of "tracon" and "artcc" may be specified`)
	} else if sg.ARTCC == "" {
		// Note: this also rejects known ATCT-only identifiers; the TRACON
		// database provides the center/radius that e.g. weather handling
		// requires.
		if !av.DB.IsTRACON(sg.TRACON) {
			e.ErrorString("TRACON %q is unknown; it must have an entry in resources/tracons.json", sg.TRACON)
		}
	} else {
		if _, ok := av.DB.ARTCCs[sg.ARTCC]; !ok {
			e.ErrorString("ARTCC %q is unknown; it must be a 3-letter identifier listed at "+
				"https://www.faa.gov/about/office_org/headquarters_offices/ato/service_units/air_traffic_services/artcc", sg.ARTCC)
		}
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

		// Entries in "fixes" should not shadow navaids, fixes, airports, or
		// runway thresholds already in the aviation DB, since Locate will
		// find them anyway.
		if p, ok := sg.Fixes[fix]; ok && !sg.AllowFixRedefinitions {
			if _, ok := av.DB.LookupWaypoint(fix); ok {
				e.ErrorString("fix shadows a navaid/fix in the aviation DB; remove it from \"fixes\"")
			} else if _, ok := av.DB.LookupICAOAirport(av.ICAOAirportCode(fix)); ok {
				e.ErrorString("fix shadows an airport in the aviation DB; remove it from \"fixes\"")
			} else if _, ok := av.DB.LookupFAAAirport(av.FAAAirportCode(fix)); ok {
				e.ErrorString("fix shadows an airport in the aviation DB; remove it from \"fixes\"")
			} else if rwy, ok := duplicateRunwayThreshold(fix, p); ok {
				e.ErrorString("fix duplicates the built-in runway threshold waypoint %s; "+
					"use that in place of it and remove it from \"fixes\"", rwy)
			}
		}

		e.Pop()
	}

	e.Push("Facility config " + facilityConfigPath(sg))
	FinalizeFacilityAdaptation(&sg.FacilityConfig.FacilityAdaptation, e, sg, mapSpec, mapSpecs)
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

	// The adjustment is only meant to bring the magnetic grid to the epoch
	// the scenario's charts were drawn for; anything larger is rotating the
	// magnetic frame, so charted headings no longer fly as charted.
	if math.Abs(sg.MagneticAdjustment) > maxMagneticAdjustment {
		e.ErrorString("magnetic_adjustment %g is more than %g degrees; it may only correct "+
			"for the magnetic grid's epoch, not rotate the magnetic frame",
			sg.MagneticAdjustment, maxMagneticAdjustment)
	}

	// One facility, one magnetic variation: it is sampled at a single point
	// for the facility, so groups covering different parts of the same
	// facility agree on it. ERAM samples at the ARTCC's adaptation center,
	// the same point NmPerLongitude comes from; STARS at the TRACON's
	// published center.
	center := sg.FacilityConfig.FacilityAdaptation.Center
	if sg.ARTCC == "" {
		if fac, ok := av.DB.LookupFacility(sg.facility()); !ok {
			e.ErrorString("%s: facility unknown", sg.facility())
		} else {
			center = fac.Center()
		}
	}
	if mvar, err := av.DB.MagneticGrid.Lookup(center); err != nil {
		e.ErrorString("%s: unable to find magnetic declination: %v", sg.facility(), err)
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
		e.Push("Airport " + string(name))
		ap.Finalize(name, sg, sg.NmPerLongitude, sg.MagneticVariation,
			sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.Scratchpads, sg.Airports,
			sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
		e.Pop()
	}

	// Auto-set default_airport if only one airport has converging runways
	var crdaAirport av.ICAOAirportCode
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
	// (Core controller validation happens in FacilityConfig.Finalize.)
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
		checkFlowNameRevisions(name, sg.Airports, e)

		for i := range flow.Arrivals {
			flow.Arrivals[i].Finalize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
			checkArrivalSpawnAltitude(flow.Arrivals[i], e)
		}
		for i := range flow.Overflights {
			flow.Overflights[i].Finalize(sg, sg.NmPerLongitude, sg.MagneticVariation,
				sg.Airports, sg.FacilityConfig.ControlPositions, sg.FacilityConfig.FacilityAdaptation.CheckScratchpad, e)
		}

		e.Pop()
	}

	for i := range sg.VFRReportingPoints {
		sg.VFRReportingPoints[i].Finalize(sg, sg.FacilityConfig.ControlPositions, e)
	}

	// Do after airports!
	if len(sg.Scenarios) == 0 {
		e.ErrorString(`No "scenarios" specified`)
	}
	for name, s := range sg.Scenarios {
		e.Push("Scenario " + name)
		s.Finalize(sg, e, mapSpec)
		e.Pop()
	}

	initializeSimConfigurations(sg, catalogs, e)
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
