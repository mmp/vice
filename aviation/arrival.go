// aviation/arrival.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type InboundFlow struct {
	Arrivals    []Arrival    `json:"arrivals"`
	Overflights []Overflight `json:"overflights"`
}

// HasHumanHandoff returns true if any arrival or overflight in the flow
// has a waypoint with HumanHandoff set.
func (f InboundFlow) HasHumanHandoff() bool {
	for _, ar := range f.Arrivals {
		if ar.Waypoints.HasHumanHandoff() {
			return true
		}
		for _, rwys := range ar.RunwayWaypoints {
			for _, wps := range rwys {
				if wps.HasHumanHandoff() {
					return true
				}
			}
		}
	}
	for _, of := range f.Overflights {
		if of.Waypoints.HasHumanHandoff() {
			return true
		}
	}
	return false
}

// InitialControllers returns a list of all initial controllers specified
// for arrivals and overflights in this flow.
func (f InboundFlow) InitialControllers() []ControlPosition {
	c := make(map[ControlPosition]struct{})
	for _, ar := range f.Arrivals {
		if ar.InitialController != "" {
			c[ar.InitialController] = struct{}{}
		}
	}
	for _, of := range f.Overflights {
		if of.InitialController != "" {
			c[of.InitialController] = struct{}{}
		}
	}
	return slices.Collect(maps.Keys(c))
}

type Arrival struct {
	Waypoints       WaypointArray                                `json:"waypoints"`
	RunwayWaypoints map[ICAOAirportCode]map[string]WaypointArray `json:"runway_waypoints"` // Airport -> runway -> waypoints
	SpawnWaypoint   string                                       `json:"spawn"`            // if "waypoints" aren't specified
	CruiseAltitudes util.SingleOrArray[int]                      `json:"cruise_altitude"`
	STAR            string                                       `json:"star"`

	// WaypointActions amends the fixes of a route taken from the CIFP.
	WaypointActions map[string]string `json:"waypoint_actions"`

	// STARFeeds are the STARs whose traffic the arrival takes; this is useful e.g. for getting
	// real-world traffic wired up for finals scenarios since we generally get traffic after they've
	// flown STARs in that case.
	STARFeeds []string `json:"star_feeds"`

	// Note: this is *only* used for flight strips displayed to the user; it should not in any way
	// be referenced for an aircraft's route: Waypoints should always be used for that.
	FlightStripDisplayRoute string `json:"route"`

	InitialController   ControlPosition         `json:"initial_controller"`
	InitialAltitudes    util.SingleOrArray[int] `json:"initial_altitude"`
	AssignedAltitude    float32                 `json:"assigned_altitude"`
	ClearedAltitude     float32                 `json:"cleared_altitude"`
	InitialSpeed        Airspeed                `json:"initial_speed"`
	SpeedRestriction    SpeedRestriction        `json:"speed_restriction"`
	Scratchpad          string                  `json:"scratchpad"`
	SecondaryScratchpad string                  `json:"secondary_scratchpad"`
	Description         string                  `json:"description"`
	CoordinationFix     string                  `json:"coordination_fix"`
	IsRNAV              bool                    `json:"is_rnav"`

	// Aircraft restricts which types of flights the arrival carries; it's
	// used when selecting arrivals with real-world traffic.
	Aircraft AircraftClass `json:"aircraft,omitempty"`

	ExpectApproach util.OneOf[string, map[ICAOAirportCode]string] `json:"expect_approach"`

	// Airports the arrival brings traffic to, in sorted order. Required,
	// except for an arrival that names a STAR the FAA CIFP charts for some of
	// the scenario's airports: it takes those. Every airport in Airlines must
	// be named here.
	Airports []ICAOAirportCode `json:"airports"`

	// Airport -> arrival airlines. Optional: without it the scenario can't
	// generate its own arrivals here, but published traffic still can.
	Airlines map[ICAOAirportCode][]ArrivalAirline `json:"airlines"`
}

// ServedSTARs returns the STARs whose traffic the arrival takes, in the order
// they were given.
func (ar Arrival) ServedSTARs() []string {
	if len(ar.STARFeeds) > 0 {
		return ar.STARFeeds
	}
	if ar.STAR != "" {
		return []string{ar.STAR}
	}
	return nil
}

// starAirports returns the airports among those given that the FAA CIFP charts
// the named STAR for, in sorted order. A STAR serving several airports is
// recorded once under each of them, so the ones whose entry has it are the ones
// it is charted for.
func starAirports(db Database, star string, airports map[ICAOAirportCode]*Airport) []ICAOAirportCode {
	var icaos []ICAOAirportCode
	for icao := range airports {
		if _, ok := db.AirportSTARs(icao)[star]; ok {
			icaos = append(icaos, icao)
		}
	}
	slices.Sort(icaos)
	return icaos
}

///////////////////////////////////////////////////////////////////////////
// Arrival

// starRunMargin is how many more of its legs in a row a STAR must have the
// arrival flying before the one it names is called wrong. The STARs into an
// airport converge on the same last fixes, so one leg more than the named one
// is no evidence; two is.
const starRunMargin = 2

// followedSTAR returns the STAR whose legs the arrival's waypoints fly the
// most of in a row, how many that is, and how many of the STAR the arrival
// names they fly. Two legs of the same fixes in a row matching the STAR's
// order are enough to count as following it.
//
// The name comes back empty if the waypoints follow no STAR, or follow two of
// them equally: this is a heuristic and the STARs into an airport share their
// last fixes.
func (ar *Arrival) followedSTAR(db Database) (best string, bestRun, namedRun int) {
	var fixes []string
	for _, wp := range ar.Waypoints {
		// Waypoints synthesized during deserialization are no part of a STAR.
		if !strings.HasPrefix(wp.Fix, "_") {
			fixes = append(fixes, wp.Fix)
		}
	}
	if len(fixes) == 0 {
		return "", 0, 0
	}

	tied := false
	for _, icao := range ar.Airports {
		for _, name := range util.SortedMapKeys(db.AirportSTARs(icao)) {
			run, shared := 0, 0
			score := func(wps WaypointArray) {
				run = max(run, sharedRun(fixes, wps))
				shared = max(shared, sharedFixes(fixes, wps))
			}
			// The runway transitions count as much as the transitions in: an
			// arrival that starts inside the facility flies only those.
			star := db.AirportSTARs(icao)[name]
			for _, wps := range star.Transitions {
				score(wps)
			}
			for _, wps := range star.RunwayWaypoints {
				score(wps)
			}
			if ProcedureBase(name) == ProcedureBase(ar.STAR) {
				namedRun = max(namedRun, run)
			}
			if follows := run >= 3 || (run >= 2 && 2*shared >= len(fixes)); !follows {
				continue
			}
			if run > bestRun {
				best, bestRun, tied = name, run, false
			} else if run == bestRun && name != best {
				tied = true
			}
		}
	}
	if tied {
		return "", 0, namedRun
	}
	return best, bestRun, namedRun
}

// sharedRun is the longest stretch of the fixes that the waypoints also pass
// over in the same order with nothing in between: the arrival flying the
// STAR's own legs rather than happening to cross it.
func sharedRun(fixes []string, wps WaypointArray) int {
	best := 0
	for i := range fixes {
		for j := range wps {
			n := 0
			for i+n < len(fixes) && j+n < len(wps) && fixes[i+n] == wps[j+n].Fix {
				n++
			}
			best = max(best, n)
		}
	}
	return best
}

// sharedFixes is how many of the fixes the waypoints also pass over.
func sharedFixes(fixes []string, wps WaypointArray) int {
	n := 0
	for _, fix := range fixes {
		if slices.ContainsFunc(wps, func(wp Waypoint) bool { return wp.Fix == fix }) {
			n++
		}
	}
	return n
}

// joinableApproaches returns the approaches the arrival's aircraft into icao
// may be cleared for: the one "expect_approach" gives them, and any that land
// on a runway the arrival has "runway_waypoints" for, since a controller can
// send them to one of those instead.
func (ar *Arrival) joinableApproaches(ap *Airport, icao ICAOAirportCode) []*Approach {
	var id string
	if ar.ExpectApproach.A != nil {
		id = *ar.ExpectApproach.A
	} else if ar.ExpectApproach.B != nil {
		id = (*ar.ExpectApproach.B)[icao]
	}

	var approaches []*Approach
	if appr, ok := ap.Approaches[id]; ok {
		approaches = append(approaches, appr)
	}
	for _, rwy := range util.SortedMapKeys(ar.RunwayWaypoints[icao]) {
		for _, appr := range ap.ApproachesToRunway(rwy) {
			if !slices.Contains(approaches, appr) {
				approaches = append(approaches, appr)
			}
		}
	}
	return approaches
}

// approachRoute returns the route the arrival's aircraft into icao fly when
// they're given appr: ExpectApproach splices the runway waypoints for the
// approach's runway into the arrival's own, where the two meet.
func (ar *Arrival) approachRoute(icao ICAOAirportCode, appr *Approach) WaypointArray {
	rwywp, ok := ar.RunwayWaypoints[icao][appr.Runway]
	if !ok || len(rwywp) == 0 || len(ar.Waypoints) == 0 {
		return ar.Waypoints
	}
	wps := slices.Concat(ar.Waypoints[:len(ar.Waypoints)-1], rwywp)
	wps[len(ar.Waypoints)-1] = rwywp[0].CarryOverActions(ar.Waypoints[len(ar.Waypoints)-1])
	return wps
}

// starWaypointsFrom returns a copy of the first of the STAR's routes that
// passes over fix, starting there. The transitions in come first; an arrival
// may also start on one of the runway transitions off the end, as a scenario's
// finals do.
func starWaypointsFrom(star STAR, fix string, e *util.ErrorLogger) WaypointArray {
	for _, routes := range []map[string]WaypointArray{star.Transitions, star.RunwayWaypoints} {
		for wps := range util.SortedMapValues(routes) {
			idx := slices.IndexFunc(wps, func(w Waypoint) bool { return w.Fix == fix })
			if idx == -1 {
				continue
			}
			if idx == len(wps)-1 {
				e.ErrorString("Only have one waypoint on STAR: %q. 2 or more are necessary for navigation", fix)
			}
			return wps[idx:].Clone()
		}
	}
	return nil
}

// starRunwayWaypoints returns a copy of the STAR's transition to the runway.
// One charted for both parallels, "16B", serves each of them.
func starRunwayWaypoints(star STAR, rwy string) (WaypointArray, bool) {
	if wps, ok := star.RunwayWaypoints[rwy]; ok {
		return wps.Clone(), true
	}
	if len(rwy) > 1 {
		if wps, ok := star.RunwayWaypoints[rwy[:len(rwy)-1]+"B"]; ok {
			return wps.Clone(), true
		}
	}
	return nil, false
}

// takeSTARWaypoints fills in the arrival's route and the runway transitions
// off the end of it from the FAA CIFP's record of its STAR at each of the
// airports it serves. The waypoints come back as the CIFP has them: their
// locations are resolved once "waypoint_actions" have been added, since an
// action may name a fix of its own to locate.
func (ar *Arrival) takeSTARWaypoints(db Database, spawnPoint string, e *util.ErrorLogger) {
	for _, icao := range ar.Airports {
		if !db.IsPublishedAirport(icao) {
			e.ErrorString("airport %q not found in database", icao)
			continue
		}

		star, ok := db.AirportSTARs(icao)[ar.STAR]
		if !ok {
			e.ErrorString("STAR %q not available for %s. Options: %s", ar.STAR, icao,
				strings.Join(util.SortedMapKeys(db.AirportSTARs(icao)), ", "))
			continue
		}

		star.Check(db, e)

		if len(ar.Waypoints) == 0 {
			ar.Waypoints = starWaypointsFrom(star, spawnPoint, e)
		}

		for _, rwy := range db.AirportRunways(icao) {
			wps, ok := starRunwayWaypoints(star, rwy.Id)
			if !ok {
				continue
			}
			if ar.RunwayWaypoints == nil {
				ar.RunwayWaypoints = make(map[ICAOAirportCode]map[string]WaypointArray)
			}
			if ar.RunwayWaypoints[icao] == nil {
				ar.RunwayWaypoints[icao] = make(map[string]WaypointArray)
			}
			ar.RunwayWaypoints[icao][rwy.Id] = wps
		}
	}
}

// routes returns the arrival's own route along with the runway transitions
// off the end of it, in a deterministic order.
func (ar *Arrival) routes() []WaypointArray {
	routes := []WaypointArray{ar.Waypoints}
	for _, icao := range util.SortedMapKeys(ar.RunwayWaypoints) {
		for _, rwy := range util.SortedMapKeys(ar.RunwayWaypoints[icao]) {
			routes = append(routes, ar.RunwayWaypoints[icao][rwy])
		}
	}
	return routes
}

// addWaypointActions applies "waypoint_actions" to the routes taken from the
// CIFP. A fix may be on the arrival's own route, on the runway transitions off
// the end of it, or--where the two meet--on both, so the actions go onto every
// route that passes over it. An offset at the fix where the arrival's own
// route ends therefore goes onto the transitions alone, a different point on
// each, as they run on to different fixes.
//
// The offsets are measured along the STAR's legs as charted, so spawnT is only
// used to reject a point the arrival spawns past; applySpawnOffset moves the
// first waypoint afterwards.
func (ar *Arrival) addWaypointActions(spawnPoint string, spawnT float32, e *util.ErrorLogger) {
	for _, key := range util.SortedMapKeys(ar.WaypointActions) {
		fix, offset, err := parseWaypointActionKey(key)
		if err != nil {
			e.ErrorString(`"waypoint_actions" %q: %v`, key, err)
			continue
		}

		// A route carries the actions if it passes over the fix, and, for an
		// offset, if it has a fix after it to measure the offset to.
		carries := func(wps WaypointArray) bool {
			return wps.containsFix(fix) && (offset == 0 || wps.hasOffsetLeg(fix))
		}
		routes := ar.routes()
		if !slices.ContainsFunc(routes, func(wps WaypointArray) bool { return wps.containsFix(fix) }) {
			e.ErrorString(`"waypoint_actions" %q: %s is not in the route %s or in any of the STAR's `+
				`runway transitions`, key, fix, ar.Waypoints.RouteString())
			continue
		}
		if !slices.ContainsFunc(routes, carries) {
			e.ErrorString(`"waypoint_actions" %q: %s ends the route and every one of the STAR's runway `+
				`transitions it is on, so there is no following fix to measure the offset to`, key, fix)
			continue
		}
		if offset != 0 && fix == spawnPoint && offset <= spawnT {
			if offset == spawnT {
				e.ErrorString(`"waypoint_actions" %q: the arrival spawns at this point; %q takes the `+
					`actions where it spawns`, key, fix)
			} else {
				e.ErrorString(`"waypoint_actions" %q: the arrival spawns %s of the way along %s's leg `+
					`and never reaches this point; %q takes the actions where it spawns`,
					key, formatOffset(spawnT), fix, fix)
			}
			continue
		}

		reported := false
		ar.eachRoute(e, func(wps WaypointArray) WaypointArray {
			if !carries(wps) {
				return wps
			}
			amended, err := wps.applyActions(fix, offset, ar.WaypointActions[key])
			if err != nil {
				// The routes differ only in what follows the fix, so an error
				// at it is generally the same for each; report the first.
				if !reported {
					e.ErrorString(`"waypoint_actions" %q: %v`, key, err)
					reported = true
				}
				return wps
			}
			return amended
		})
	}
}

// handedOff reports whether the arrival's route or one of its runway
// transitions says where the track goes, so that vice need not add a handoff
// to a human of its own.
func (ar *Arrival) handedOff() bool {
	return slices.ContainsFunc(ar.routes(), func(wps WaypointArray) bool {
		return wps.HasHumanHandoff() || len(wps.HandoffControllers()) > 0
	})
}

// addAutomaticHandoff hands the aircraft to a human halfway from where it
// spawns to the next fix of the route, unless "waypoint_actions" already says
// where the track goes, as a fully-virtual arrival does when it only hands off
// between virtual controllers. The halfway point is measured along the charted
// leg like any other offset, so it accounts for the "spawn" offset that
// applySpawnOffset applies later.
func (ar *Arrival) addAutomaticHandoff(spawnT float32) {
	if ar.handedOff() {
		return
	}

	handoff := WaypointActions{HumanHandoff: true}
	next := ar.Waypoints.nextChartedFix(0)
	// An arrival that joins the STAR at the end of a route, or on a DME arc--no
	// point between whose fixes is on it--is handed off where it spawns.
	if next == len(ar.Waypoints) || ar.Waypoints[0].Arc() != nil {
		// empty string -> to human
		ar.Waypoints[0].MergeActions(handoff)
		return
	}

	t := (1 + spawnT) / 2
	// A fraction this close to the handoff's is the same place on the leg--a
	// foot or so along the longest of them--and the two are not bit-identical
	// in any case: "spawn" 0.32 gives a midpoint of 0.65999997 where the key
	// "@0.66" parses to 0.66.
	const samePoint = 1e-5
	if i := slices.IndexFunc(ar.Waypoints[1:next],
		func(wp Waypoint) bool { return math.Abs(wp.LegOffset()-t) < samePoint }); i != -1 {
		// A "waypoint_actions" offset already puts a point there, which takes
		// the handoff rather than the route carrying two waypoints in one
		// place.
		ar.Waypoints[1+i].MergeActions(handoff)
		return
	}

	ar.Waypoints = ar.Waypoints.insertAlongLeg(0, t, Waypoint{
		Fix:   "_handoff",
		Extra: &WaypointExtra{ActionGroups: []WaypointActionGroup{{Actions: handoff}}},
	})
}

// eachRoute replaces the arrival's own route and each of the STAR's runway
// transitions with what f makes of it.
func (ar *Arrival) eachRoute(e *util.ErrorLogger, f func(wps WaypointArray) WaypointArray) {
	ar.Waypoints = f(ar.Waypoints)
	ar.eachRunwayTransition(e, f)
}

// eachRunwayTransition replaces each of the STAR's runway transitions with
// what f makes of it, with the error logger scoped to the airport and runway
// the transition serves.
func (ar *Arrival) eachRunwayTransition(e *util.ErrorLogger, f func(wps WaypointArray) WaypointArray) {
	for _, icao := range util.SortedMapKeys(ar.RunwayWaypoints) {
		e.Push("Airport " + string(icao))
		for _, rwy := range util.SortedMapKeys(ar.RunwayWaypoints[icao]) {
			e.Push("Runway " + rwy)
			ar.RunwayWaypoints[icao][rwy] = f(ar.RunwayWaypoints[icao][rwy])
			e.Pop()
		}
		e.Pop()
	}
}

// applySpawnOffset moves the arrival's first waypoint the given fraction of
// the way along the leg after it, where "spawn" says the aircraft appears. The
// waypoint is moved rather than replaced so that it keeps the fix's altitude
// and speed restrictions, and its name takes the prefix synthesized waypoints
// have, since the aircraft no longer crosses the fix the CIFP charts.
func (ar *Arrival) applySpawnOffset(t float32) {
	next := ar.Waypoints.nextChartedFix(0)
	if t == 0 || next == len(ar.Waypoints) {
		return
	}
	ar.Waypoints[0].Location = math.Lerp2f(t, ar.Waypoints[0].Location, ar.Waypoints[next].Location)
	ar.Waypoints[0].Fix = "_" + ar.Waypoints[0].Fix
}

// sameRunwayTransitions reports whether the two sets of runway transitions
// cover the same airports and runways with routes that match.
func sameRunwayTransitions(a, b map[ICAOAirportCode]map[string]WaypointArray,
	match func(WaypointArray, WaypointArray) bool) bool {
	return maps.EqualFunc(a, b, func(x, y map[string]WaypointArray) bool {
		return maps.EqualFunc(x, y, match)
	})
}

// chartedSTARRoute reports whether the arrival's hand-written route and
// runway transitions are the CIFP's STAR as charted, so that naming the STAR
// and where it joins would fly it the same way, and returns the
// "waypoint_actions" that give its own actions at the STAR's fixes.
func (ar *Arrival) chartedSTARRoute(db Database, nmPerLongitude float32,
	magneticVariation float32) (map[string]string, bool) {
	if ar.STAR == "" || len(ar.Waypoints) == 0 {
		return nil, false
	}
	// Without a handoff of its own, the arrival would pick up the one vice
	// adds partway along the first leg of a route taken from the CIFP.
	if !ar.handedOff() {
		return nil, false
	}

	var scratch util.ErrorLogger
	charted := Arrival{STAR: ar.STAR, Airports: ar.Airports}
	charted.takeSTARWaypoints(db, ar.Waypoints[0].Fix, &scratch)
	if scratch.HaveErrors() || len(charted.Waypoints) == 0 {
		return nil, false
	}
	locate := func(wps WaypointArray) WaypointArray {
		return wps.InitializeLocations(db, nmPerLongitude, magneticVariation, false, &scratch)
	}
	charted.eachRoute(&scratch, locate)
	if scratch.HaveErrors() {
		return nil, false
	}

	if !ar.Waypoints.sameCourse(charted.Waypoints) ||
		!sameRunwayTransitions(ar.RunwayWaypoints, charted.RunwayWaypoints, WaypointArray.sameCourse) {
		return nil, false
	}

	actions := make(map[string]string)
	ok := ar.Waypoints.addedActions(charted.Waypoints, actions)
	for _, icao := range util.SortedMapKeys(ar.RunwayWaypoints) {
		for _, rwy := range util.SortedMapKeys(ar.RunwayWaypoints[icao]) {
			ok = ok && ar.RunwayWaypoints[icao][rwy].addedActions(charted.RunwayWaypoints[icao][rwy], actions)
		}
	}
	if !ok {
		return nil, false
	}

	// Only redundant if the STAR with those actions is the arrival's route
	// exactly, not just closely: one entry adds its actions to every route
	// the fix is on, which is not always what the waypoints say.
	charted.WaypointActions = actions
	// No spawn point to give: addedActions derives its keys from waypoints the
	// route flies over, so none of them carries an offset.
	charted.addWaypointActions("", 0, &scratch)
	if scratch.HaveErrors() || !ar.Waypoints.sameRoute(charted.Waypoints) ||
		!sameRunwayTransitions(ar.RunwayWaypoints, charted.RunwayWaypoints, WaypointArray.sameRoute) {
		return nil, false
	}
	return actions, true
}

// checkChartedSTARRoute reports an arrival that spells out the STAR it names
// as the CIFP charts it; it should say where it joins the STAR instead.
func (ar *Arrival) checkChartedSTARRoute(db Database, nmPerLongitude float32, magneticVariation float32,
	e *util.ErrorLogger) {
	actions, ok := ar.chartedSTARRoute(db, nmPerLongitude, magneticVariation)
	if !ok {
		return
	}

	give := []string{fmt.Sprintf(`"spawn": %q`, ar.Waypoints[0].Fix)}
	if len(actions) > 0 {
		encoded, _ := json.Marshal(actions)
		give = append(give, fmt.Sprintf(`"waypoint_actions": %s`, encoded))
	}
	spelled := `"waypoints"`
	if len(ar.RunwayWaypoints) > 0 {
		spelled = `"waypoints" and "runway_waypoints"`
	}
	e.ErrorString(`%s fly the %s STAR as the CIFP charts it; drop them and give %s`,
		spelled, ar.STAR, strings.Join(give, " and "))
}

func (ar *Arrival) Finalize(db Database, nmPerLongitude float32, magneticVariation float32,
	airports map[ICAOAirportCode]*Airport, controlPositions map[ControlPosition]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if ar.FlightStripDisplayRoute != "" {
		r := strings.TrimPrefix(strings.TrimPrefix(ar.FlightStripDisplayRoute, "/."), "./")
		for word := range strings.FieldsSeq(r) {
			if strings.Contains(word, "/") {
				e.ErrorString(`"route" word %q contains a slash; did you mean "waypoints"? (confusingly, "waypoints" specifies the route flown, "route" is only used for flight strips)`, word)
			}
		}
	}

	switch {
	case ar.FlightStripDisplayRoute != "":
		e.Push("FlightStripDisplayRoute " + ar.FlightStripDisplayRoute)
	case ar.STAR != "":
		e.Push("Route " + ar.STAR)
	default:
		e.Push("Spawn " + ar.SpawnWaypoint)
	}
	defer e.Pop()

	if len(ar.Airports) == 0 {
		// An arrival that names no airports of its own serves the ones the FAA
		// charts its STAR for.
		if ar.STAR == "" {
			e.ErrorString(`must name the airports the arrival serves in "airports"`)
			return
		}
		ar.Airports = starAirports(db, ar.STAR, airports)
		if len(ar.Airports) == 0 {
			e.ErrorString("STAR %q isn't charted for any of the scenario's airports; "+
				`name the airports it serves in "airports"`, ar.STAR)
			return
		}
	} else {
		// Only the airports the scenario gives itself need checking;
		// starAirports picks from the scenario's own to begin with.
		slices.Sort(ar.Airports)
		for _, icao := range ar.Airports {
			if _, ok := airports[icao]; !ok {
				e.ErrorString("arrival airport %q unknown", icao)
			}
		}
	}

	for _, icao := range util.SortedMapKeys(ar.Airlines) {
		if !slices.Contains(ar.Airports, icao) {
			e.ErrorString(`"airlines" gives airlines into %q, which isn't one of the arrival's "airports"`, icao)
		}
	}

	if ar.STAR != "" && len(ar.Waypoints) > 0 {
		if !slices.ContainsFunc(ar.Airports, func(icao ICAOAirportCode) bool {
			_, ok := db.AirportSTARs(icao)[ar.STAR]
			return ok
		}) {
			e.ErrorString(`"star" %q isn't charted for any of the airports the arrival serves: %s`,
				ar.STAR, strings.Join(util.MapSlice(ar.Airports, func(icao ICAOAirportCode) string { return string(icao) }), ", "))
		}
	}

	if len(ar.Waypoints) == 0 {
		// STAR details are coming from the FAA CIFP; make sure
		// everything is ok so we don't get into trouble when we
		// spawn arrivals...
		if ar.STAR == "" {
			e.ErrorString(`must provide "star" if "waypoints" aren't given`)
			return
		}
		if ar.SpawnWaypoint == "" {
			e.ErrorString(`must specify "spawn" if "waypoints" aren't given with arrival`)
			return
		}

		spawnPoint, spawnTString, ok := strings.Cut(ar.SpawnWaypoint, "@")
		spawnT := float32(0)
		if ok {
			st, err := strconv.ParseFloat(spawnTString, 32)
			switch {
			case err != nil:
				e.ErrorString(`"spawn" offset %q: %v`, spawnTString, err)
			// Written so that a NaN, which ParseFloat returns without an
			// error, is rejected along with the out-of-range values.
			case !(st >= 0 && st < 1):
				e.ErrorString(`"spawn" offset %q: must be at least 0 and less than 1`, spawnTString)
			default:
				spawnT = float32(st)
			}
		}

		ar.takeSTARWaypoints(db, spawnPoint, e)
		if len(ar.Waypoints) == 0 {
			e.ErrorString("Couldn't find waypoint %s in any of the STAR routes", spawnPoint)
			return
		}

		// Before the handoff, so that one given at an offset counts as the
		// arrival's own.
		ar.addWaypointActions(spawnPoint, spawnT, e)
		ar.addAutomaticHandoff(spawnT)

		ar.Waypoints = ar.Waypoints.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)
		ar.eachRunwayTransition(e, func(wps WaypointArray) WaypointArray {
			wps = wps.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)
			for i := range wps {
				wps[i].SetOnSTAR(true)
			}
			wps.checkBasics(e, controlPositions, checkScratchpad)
			return wps
		})

		ar.applySpawnOffset(spawnT)
	} else {
		if len(ar.WaypointActions) > 0 {
			e.ErrorString(`"waypoint_actions" applies only to a route taken from the CIFP; ` +
				`put the actions in "waypoints"`)
		}
		if len(ar.Waypoints) < 2 {
			e.ErrorString(
				`must provide at least two "waypoints" for arrival ` +
					`(even if "runway_waypoints" are provided)`,
			)
			return
		}
		if ar.SpawnWaypoint != "" {
			e.ErrorString(`"spawn" cannot be specified if "waypoints" are provided`)
			return
		}

		ar.Waypoints = ar.Waypoints.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)
		if len(ar.Waypoints) == 0 {
			// Every waypoint named an airway; takeAirways has said why.
			return
		}

		for ap, rwywp := range ar.RunwayWaypoints {
			e.Push("Airport " + string(ap))

			if err := db.CheckAirport("runway waypoints", ap); err != nil {
				e.Error(err)
				e.Pop()
				continue
			}

			for rwy, wp := range rwywp {
				e.Push("Runway " + rwy)

				if _, ok := LookupRunway(db, ap, rwy); !ok {
					e.ErrorString("runway %q is unknown. Options: %s", rwy, db.ValidRunways(ap))
				}

				wp = wp.InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)
				if len(wp) == 0 {
					// Every waypoint named an airway; takeAirways has said why.
					e.Pop()
					continue
				}

				for i := range wp {
					wp[i].SetOnSTAR(true)
				}

				if wp[0].Fix != ar.Waypoints[len(ar.Waypoints)-1].Fix {
					e.ErrorString(
						`initial "runway_waypoints" fix must match ` +
							`last "waypoints" fix`,
					)
				}

				// For the check, splice together the last common
				// waypoint and the runway waypoints.  This will give
				// us a repeated first fix, but this way we can check
				// compliance with restrictions at that fix...
				ewp := append([]Waypoint{ar.Waypoints[len(ar.Waypoints)-1]}, wp...)
				approachAssigned := ar.ExpectApproach.A != nil || ar.ExpectApproach.B != nil
				WaypointArray(ewp).CheckArrival(e, controlPositions, approachAssigned, checkScratchpad)

				e.Pop()
			}
			e.Pop()
		}

		ar.checkChartedSTARRoute(db, nmPerLongitude, magneticVariation, e)
	}

	for i := range ar.Waypoints {
		ar.Waypoints[i].SetOnSTAR(true)
	}

	// Which STAR the waypoints fly: one the arrival doesn't name it should,
	// and one it does name should be the one they fly.
	star, run, namedRun := ar.followedSTAR(db)
	switch {
	case ar.STAR == "" && star != "":
		// An arrival that gives "star_feeds" takes traffic from several STARs
		// and flies the stretch they share, so there is no one STAR to ask it
		// to name.
		if len(ar.STARFeeds) == 0 {
			e.ErrorString(`the arrival's waypoints follow the %s STAR; give it in "star"`, star)
		}
	case ar.STAR != "" && star != "" && ProcedureBase(star) != ProcedureBase(ar.STAR) &&
		run-namedRun >= starRunMargin:
		e.ErrorString(`"star" is %s but the waypoints fly %d of the %s STAR's legs in a row and %d of %s's`,
			ar.STAR, run, star, namedRun, ar.STAR)
	}

	for _, star := range ar.STARFeeds {
		if !slices.ContainsFunc(ar.Airports, func(icao ICAOAirportCode) bool {
			_, ok := db.AirportSTARs(icao)[star]
			return ok
		}) {
			e.ErrorString(`"star_feeds" %q isn't charted for any of the airports the arrival serves: %s`,
				star, strings.Join(util.MapSlice(ar.Airports, func(icao ICAOAirportCode) string { return string(icao) }), ", "))
		}
	}

	approachAssigned := ar.ExpectApproach.A != nil || ar.ExpectApproach.B != nil
	ar.Waypoints.CheckArrival(e, controlPositions, approachAssigned, checkScratchpad)

	for _, arrivalAirport := range ar.Airports {
		e.Push("Arrival airport " + string(arrivalAirport))
		for i := range ar.Airlines[arrivalAirport] {
			ar.Airlines[arrivalAirport][i].Check(db, e)
			if err := db.CheckAirport("departure", ar.Airlines[arrivalAirport][i].Airport); err != nil {
				e.Error(err)
			}
		}
		if ap, ok := airports[arrivalAirport]; ok {
			for _, appr := range ar.joinableApproaches(ap, arrivalAirport) {
				ar.approachRoute(arrivalAirport, appr).checkApproachJoins(appr, e)
			}
		}
		e.Pop()
	}

	if ar.ExpectApproach.A != nil { // Given a single string
		if len(ar.Airports) > 1 {
			e.ErrorString(`There are multiple arrival airports but only one approach in "expect_approach"; ` +
				`give an approach for each of them instead`)
		}
		airport := ar.Airports[0]
		// We checked the arrival airports were valid above, no need to issue an error if not found.
		if ap, ok := airports[airport]; ok {
			if _, ok := ap.Approaches[*ar.ExpectApproach.A]; !ok {
				e.ErrorString(
					`arrival airport %q doesn't have a %q approach for "expect_approach"`,
					airport, *ar.ExpectApproach.A,
				)
			}
		}
	} else if ar.ExpectApproach.B != nil {
		for airport, appr := range *ar.ExpectApproach.B {
			if !slices.Contains(ar.Airports, airport) {
				e.ErrorString(
					`airport %q is listed in "expect_approach" but is not in arrival airports`,
					airport,
				)
				continue
			}
			if ap, ok := airports[airport]; ok {
				if _, ok := ap.Approaches[appr]; !ok {
					e.ErrorString(
						`arrival airport %q doesn't have a %q approach for "expect_approach"`,
						airport, appr,
					)
				}
			}
		}
	}

	if len(ar.InitialAltitudes) == 0 {
		e.ErrorString(`must specify at least one "initial_altitude"`)
	} else {
		// Make sure no possible initial altitude is below any of the
		// altitude restrictions.
		for _, alt := range ar.InitialAltitudes {
			a := float32(alt)
			for _, wp := range ar.Waypoints {
				if wp.AltitudeRestriction() != nil &&
					wp.AltitudeRestriction().TargetAltitude(a) > a {
					e.ErrorString(`"initial_altitude" %d is below altitude restriction at %q`, alt, wp.Fix)
				}
			}
		}
	}

	if ar.InitialSpeed.IsZero() {
		e.ErrorString(`must specify "initial_speed"`)
	} else {
		checkSpeed(e, `"initial_speed"`, ar.InitialSpeed)
	}

	if !ar.SpeedRestriction.IsZero() {
		checkSpeedRange(e, ar.SpeedRestriction)
	}

	if ar.InitialController == "" {
		e.ErrorString(`"initial_controller" missing`)
	} else if _, ok := controlPositions[ar.InitialController]; !ok {
		e.ErrorString(`controller %q not found for "initial_controller"`, ar.InitialController)
	}

	if !checkScratchpad(ar.Scratchpad) {
		e.ErrorString("%s: invalid scratchpad", ar.Scratchpad)
	}
	if !checkScratchpad(ar.SecondaryScratchpad) {
		e.ErrorString("%s: invalid secondary scratchpad", ar.SecondaryScratchpad)
	}
}

func (ar Arrival) GetRunwayWaypoints(airport ICAOAirportCode, rwy string) WaypointArray {
	if ap, ok := ar.RunwayWaypoints[airport]; !ok {
		return nil
	} else if wp, ok := ap[rwy]; !ok {
		return nil
	} else {
		return wp
	}
}
