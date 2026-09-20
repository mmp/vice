// scenario/group.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

type Group struct {
	ARTCC              string                             `json:"artcc"`
	Area               string                             `json:"area"`
	TRACON             string                             `json:"tracon"`
	Name               string                             `json:"name"`
	Airports           map[av.ICAOAirportCode]*av.Airport `json:"airports"`
	Fixes              map[string]math.Point2LL           `json:"-"`
	FixesStrings       util.OrderedMap                    `json:"fixes"`
	Scenarios          map[string]*Scenario               `json:"scenarios"`
	DefaultScenario    string                             `json:"default_scenario"`
	Airspace           av.Airspace                        `json:"airspace"`
	InboundFlows       map[string]*av.InboundFlow         `json:"inbound_flows"`
	VFRReportingPoints []av.VFRReportingPoint             `json:"vfr_reporting_points"`

	AllowFixRedefinitions bool `json:"allow_fix_redefinitions"`

	NmPerLatitude      float32 // Always 60
	NmPerLongitude     float32 // Derived from Center
	MagneticVariation  float32
	MagneticAdjustment float32 `json:"magnetic_adjustment"`

	// FacilityConfig is populated at runtime from the facility config file,
	// not from the scenario group JSON.
	FacilityConfig sim.FacilityConfig `json:"-"`

	// ERAMCoordination is the resolved pseudo-ERAM adaptation for this
	// facility's TRACON computer id, loaded from the ERAM host (ARTCC)
	// config. Nil if none is adapted.
	ERAMCoordination *enroute.Coordination `json:"-"`

	SourceFile string // path of the JSON file this was loaded from
}

///////////////////////////////////////////////////////////////////////////
// ScenarioGroup

// facility is the radar facility the group is flown at: its TRACON for STARS
// scenarios, its ARTCC for ERAM ones.
func (sg *Group) facility() string {
	return util.Select(sg.TRACON == "", sg.ARTCC, sg.TRACON)
}

func (sg *Group) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if p, ok := sg.Fixes[s]; ok {
		return p, true
	}
	//... and then the static database.
	return enroute.DBLocator{}.Locate(s)
}

// Airways returns the airways published under the given name.
func (sg *Group) Airways(name string) ([]av.Airway, bool) {
	aw, ok := av.DB.Airways[name]
	return aw, ok
}

func (sg *Group) LocateDME(s string) (math.Point2LL, int, bool) {
	return av.DB.LookupDME(s)
}

func (sg *Group) Declination(s string) (float32, bool) {
	return av.DB.Declination(s)
}

// resolveController normalizes a TCP that may use a short prefix (e.g.
// "N2K") to the canonical long-prefix form (e.g. "NNN2K") stored in
// ControlPositions.  If tcp is already present or no expansion matches,
// it is returned unchanged.
func (sg *Group) resolveController(tcp sim.TCP) sim.TCP {
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
// form in place. This must be called before airport/flow Finalize
// so that validation in the aviation package finds the controllers.
func (sg *Group) resolveControllerRefs() {
	resolve := func(cp av.ControlPosition) av.ControlPosition {
		return av.ControlPosition(sg.resolveController(sim.TCP(cp)))
	}
	resolveWaypoints := func(wps av.WaypointArray) {
		for i := range wps {
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
			for _, routes := range exitRoutes {
				for _, route := range routes {
					if route.HandoffController != "" {
						route.HandoffController = resolve(route.HandoffController)
					}
					if route.DepartureController != "" {
						route.DepartureController = resolve(route.DepartureController)
					}
					resolveWaypoints(route.Waypoints)
					for key, actions := range route.WaypointActions {
						route.WaypointActions[key] = av.ResolveActionControllers(actions, resolve)
					}
				}
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
			for key, actions := range flow.Arrivals[i].WaypointActions {
				flow.Arrivals[i].WaypointActions[key] = av.ResolveActionControllers(actions, resolve)
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

func (sg *Group) Similar(fix string) []string {
	d1, d2 := util.SelectInTwoEdits(fix, maps.Keys(sg.Fixes), nil, nil)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Navaids), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Fixes), d1, d2)
	return util.Select(len(d1) > 0, d1, d2)
}

var (
	// "FIX@HDG/DIST"
	reFixHeadingDistance = regexp.MustCompile(`^([\w-]{3,})@([\d]{3})/(\d+(\.\d+)?)$`)

	// A "fixes" entry named for a runway threshold: an airport identifier
	// followed by a runway, e.g. "_JFK_31L", "_COS35R", "IAH8L", "KFIN-11".
	reAirportRunwayFix = regexp.MustCompile(`^_?([A-Z0-9]{3,4})[_-](\d{1,2}[LRCW]?)$|^_([A-Z]{3,4})(\d{1,2}[LRC]?)$|^([A-Z]{3})(\d{1,2}[LRC]?)$`)
)

// duplicateRunwayThreshold reports the built-in runway threshold waypoint that
// makes a "fixes" entry redundant: either the name is itself that waypoint, or
// it names an airport and runway and sits close enough to one of that
// airport's thresholds that the author clearly meant it.
func duplicateRunwayThreshold(fix string, p math.Point2LL) (string, bool) {
	if ident, rwy, found := strings.Cut(fix, "-"); found && len(ident) >= 3 {
		if _, ok := av.LookupRunway(av.ICAOAirportCode(ident), rwy); ok {
			return fix, true
		}
	}

	m := reAirportRunwayFix.FindStringSubmatch(fix)
	if m == nil {
		return "", false
	}
	groups := util.FilterSlice(m[1:], func(s string) bool { return s != "" })
	if len(groups) != 2 {
		return "", false
	}
	// The airport part of the fix may be written with either of its ids.
	ap, ok := av.DB.LookupICAOAirport(av.ICAOAirportCode(groups[0]))
	if !ok {
		ap, ok = av.DB.LookupFAAAirport(av.FAAAirportCode(groups[0]))
	}
	if !ok {
		return "", false
	}
	named := strings.TrimPrefix(groups[1], "0")

	const tolerance = 1000 * math.FeetToNauticalMiles
	best, bestDist := "", float32(tolerance)
	for _, rwy := range ap.Runways {
		if d := math.NMDistance2LL(p, rwy.Threshold); d < bestDist {
			best, bestDist = rwy.Id, d
		}
	}
	if best == "" {
		return "", false
	}

	// Some scenarios name a departure fix for the runway being departed
	// rather than the one it sits on, so the nearest threshold is the usual
	// answer; prefer the runway the fix is named for when it is also in
	// range, since a few airports have thresholds only a few hundred feet
	// apart.
	if i := slices.IndexFunc(ap.Runways, func(rwy av.Runway) bool { return rwy.Id == named }); i != -1 {
		if math.NMDistance2LL(p, ap.Runways[i].Threshold) < tolerance {
			best = named
		}
	}

	return string(ap.Id) + "-" + best, true
}

// airportVolumeId names a default airport filter region within the
// 7-character limit on airspace volume ids; a 4-character airport identifier

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

func (sg *Group) rewriteControllers(e *util.ErrorLogger) {
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
			for _, routes := range exitroutes {
				for _, route := range routes {
					rewriteControlPosition(&route.HandoffController)
					rewriteWaypoints(route.Waypoints)
				}
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
	// Rewrite the owning TCPs in fix-pair assignment rows, first normalizing
	// any short-prefix neighbor references to their canonical form.
	// (Reassignment rules and arts_coordination reference only fixes, not
	// positions.)
	if fpc := fa.FixPairConfiguration; fpc != nil {
		for _, rows := range [][]sim.FixPairAssignmentRow{
			fpc.Assignments.Arrival, fpc.Assignments.Departure, fpc.Assignments.Overflight,
		} {
			for i := range rows {
				for plan, tcp := range rows[i].TCP {
					tcp = sg.resolveController(tcp)
					rewriteControlPosition(&tcp)
					rows[i].TCP[plan] = tcp
				}
			}
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

// checkArrivalSpawnAltitude flags an arrival whose initial altitude is
// too high to meet its first "at or below" restriction given the distance
// to that waypoint. Assumes 2500 fpm descent at 250 kts ground speed. If
// multiple spawn altitudes are configured, each is checked.
func checkArrivalSpawnAltitude(arr av.Arrival, e *util.ErrorLogger) {
	if arr.AssignedAltitude > 0 {
		return
	}
	if len(arr.Waypoints) == 0 {
		return
	}

	for _, alt := range arr.InitialAltitudes {
		spawnAlt := float32(alt)
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
			break
		}
	}
}

// checkFlowNameRevisions reports an inbound flow whose name carries a STAR
// revision the FAA CIFP doesn't chart. The name is only a label--the arrivals'
// "star" is what they fly and is validated exactly--so nothing else notices it
// going stale, and it is what the arrivals list shows the controller.
func checkFlowNameRevisions(name string, airports map[av.ICAOAirportCode]*av.Airport, e *util.ErrorLogger) {
	for _, token := range strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		proc, ok := leadingProcedureName(token)
		if !ok {
			continue
		}

		charted := make(map[string]struct{})
		for icao := range airports {
			for star := range av.DB.Airports[icao].STARs {
				if av.ProcedureBase(star) == av.ProcedureBase(proc) {
					charted[star] = struct{}{}
				}
			}
		}
		if len(charted) == 0 {
			// Nothing of that name is charted at any of the scenario's
			// airports, so the token isn't a procedure name at all.
			continue
		}
		if _, ok := charted[proc]; ok {
			continue
		}

		e.ErrorString("%s in the name isn't a STAR the FAA CIFP charts; the current revision is %s",
			proc, strings.Join(util.SortedMapKeys(charted), ", "))
	}
}

// leadingProcedureName returns the SID or STAR name at the start of a token:
// two to five letters and the revision number after them, as in the LAIKS3 of
// an inbound flow named LAIKS3S.
func leadingProcedureName(token string) (string, bool) {
	n := 0
	for n < len(token) && token[n] >= 'A' && token[n] <= 'Z' {
		n++
	}
	if n < 2 || n > 5 || n == len(token) || token[n] < '0' || token[n] > '9' {
		return "", false
	}
	return token[:n+1], true
}

// loadEmergencies loads and validates the emergencies.json resource file.
