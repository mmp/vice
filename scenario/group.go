// scenario/group.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type Group struct {
	// The published aeronautical data a scenario is checked against; Locate
	// below takes precedence for fixes the scenario defines itself.
	enroute.DBLocator

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
	aw, ok := db.DB.Airways[name]
	return aw, ok
}

func (sg *Group) LocateDME(s string) (math.Point2LL, int, bool) {
	return db.DB.LookupDME(s)
}

func (sg *Group) Declination(s string) (float32, bool) {
	return db.DB.Declination(s)
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
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(db.DB.Navaids), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(db.DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(db.DB.Fixes), d1, d2)
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
		if _, ok := av.LookupRunway(db.Lookups{}, av.ICAOAirportCode(ident), rwy); ok {
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
	ap, ok := db.DB.LookupICAOAirport(av.ICAOAirportCode(groups[0]))
	if !ok {
		ap, ok = db.DB.LookupFAAAirport(av.FAAAirportCode(groups[0]))
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
			for star := range db.DB.Airports[icao].STARs {
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
