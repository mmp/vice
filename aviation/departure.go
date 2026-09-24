// aviation/departure.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type ExitRoute struct {
	SID              string            `json:"sid"`
	AssignedAltitude int               `json:"assigned_altitude"`
	ClearedAltitude  int               `json:"cleared_altitude"`
	Waypoints        WaypointArray     `json:"waypoints"`
	Description      string            `json:"description"`
	IsRNAV           bool              `json:"is_rnav"`
	HoldForRelease   bool              `json:"hold_for_release"`
	InitialHeading   int               `json:"initial_heading"`  // tower-assigned
	ClimboutActions  string            `json:"climbout_actions"` // generalizes InitialHeading
	WaypointActions  map[string]string `json:"waypoint_actions"`
	// optional, control position to handoff to at a /ho
	HandoffController ControlPosition `json:"handoff_controller"`
	// optional, the initial tracking controller for the departure.
	DepartureController ControlPosition `json:"departure_controller"`

	// Aircraft restricts which types of flights take the route; it's used
	// when an exit routes them differently.
	Aircraft AircraftClass `json:"aircraft,omitempty"`

	// ERAM gives the data block entries departures on the route take off
	// with; ERAM facilities only.
	ERAM *ERAMEntries `json:"eram"`

	WaitToContactDeparture bool // whether the aircraft waits until a /TC point to contact departure
}

// ExitRoutes gives the ways departures leave via an exit; a bare JSON object
// is shorthand for a single route open to all aircraft. A departure flies the
// first route its aircraft type is admitted to, so the routes need not cover
// every type: one with no route is not launched.
type ExitRoutes []*ExitRoute

func (er *ExitRoutes) UnmarshalJSON(b []byte) error {
	var routes util.SingleOrArray[*ExitRoute]
	if err := routes.UnmarshalJSON(b); err != nil {
		return err
	}
	*er = ExitRoutes(routes)
	return nil
}

// CheckJSONErrors checks the routes in either form themselves so that a
// misspelled member is reported as such rather than as an unexpected shape.
func (er *ExitRoutes) CheckJSONErrors(json any, e *util.ErrorLogger) {
	array, ok := json.([]any)
	if !ok {
		util.TypeCheckJSONErrors[ExitRoute](json, e)
		return
	}
	for i, route := range array {
		e.Push(fmt.Sprintf("Route %d", i+1))
		util.TypeCheckJSONErrors[ExitRoute](route, e)
		e.Pop()
	}
}

// ForAircraft returns the route the given aircraft type flies, or nil if none
// of the routes takes it.
func (er ExitRoutes) ForAircraft(db Database, acType string) *ExitRoute {
	if i := slices.IndexFunc(er, func(r *ExitRoute) bool { return r.Aircraft.Matches(db, acType) }); i != -1 {
		return er[i]
	}
	return nil
}

// ExitRoutesForAircraft returns the route to each exit that the given aircraft
// type flies, leaving out the exits it has no route to.
func ExitRoutesForAircraft(db Database, routes map[ExitID]ExitRoutes, acType string) map[ExitID]*ExitRoute {
	m := make(map[ExitID]*ExitRoute, len(routes))
	for exit, er := range routes {
		if r := er.ForAircraft(db, acType); r != nil {
			m[exit] = r
		}
	}
	return m
}

// sidWaypoints returns the waypoints of the CIFP's SID off the runway to the
// exit for an exit route that gives none of its own; transition, if
// non-empty, names the SID's enroute transition to fly. A runway transition
// the CIFP charts is always taken, fixes and all; if it opens with heading
// legs from the departure end, initialize supersedes those when the route
// assigns its own heading. Only when the CIFP has no transition for the
// runway does an assigned heading--"initial_heading" or a
// "climbout_actions" heading--stand in for one, with the route starting
// from the SID's common portion.
func sidWaypoints(db Database, icao ICAOAirportCode, sid, transition string, rwy RunwayID, exit ExitID,
	assignedHeading bool) (WaypointArray, error) {
	s, ok := db.AirportSIDs(icao)[sid]
	if !ok {
		return nil, fmt.Errorf("SID %q isn't in the FAA CIFP for %s. Options: %s",
			sid, icao, strings.Join(util.SortedMapKeys(db.AirportSIDs(icao)), ", "))
	}
	runway := rwy.Base()
	if _, ok := s.RunwayTransitions[runway]; !ok && assignedHeading {
		runway = ""
	}
	wps, err := s.Waypoints(runway, transition, exit.Base())
	if err != nil {
		return nil, fmt.Errorf("SID %s: %w", sid, err)
	}
	return wps.Clone(), nil
}

// ChartedSIDPaths returns the named fixes, in order, of each path through
// the SID: the common route and each enroute transition, on their own and
// spliced onto each runway transition.
func ChartedSIDPaths(s SID) [][]string {
	bodies := []WaypointArray{s.Common}
	for tr := range util.SortedMapValues(s.EnrouteTransitions) {
		bodies = append(bodies, tr)
	}

	var paths [][]string
	add := func(wps WaypointArray) {
		names := util.FilterSlice(util.MapSlice(wps, func(wp Waypoint) string { return wp.Fix }), IsNamedFix)
		if len(names) > 0 {
			paths = append(paths, names)
		}
	}
	for _, body := range bodies {
		add(body)
		for rt := range util.SortedMapValues(s.RunwayTransitions) {
			add(SpliceSIDTransition(rt, body))
		}
	}
	return paths
}

// exitSIDs returns the base names of the SIDs that the airport's departure
// routes fly for the given exit, in sorted order.
func (ap *Airport) exitSIDs(db Database, exit string) []string {
	sids := make(map[string]bool)
	for _, exitRoutes := range ap.DepartureRoutes {
		for exitID, routes := range exitRoutes {
			if exitID.Base() != exit {
				continue
			}
			for _, er := range routes {
				if base, _, _ := strings.Cut(er.SID, "."); base != "" {
					sids[base] = true
				}
			}
		}
	}
	return util.SortedMapKeys(sids)
}

// commonPrefixLen returns how many leading elements the two slices share.
func commonPrefixLen(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// checkDepartureRouteAlongSID flags a departure whose route includes charted
// fixes of its exit's SID between the exit and the fix where the route
// leaves the SID: these hinder matching up routes from real-world flights.
func (ap *Airport) checkDepartureRouteAlongSID(db Database, icao ICAOAirportCode, dep *Departure, e *util.ErrorLogger) {
	exit := dep.Exit.Base()
	fixes := util.MapSlice(dep.RouteWaypoints, func(wp Waypoint) string { return wp.Fix })
	exitIdx := slices.Index(fixes, exit)
	if exitIdx == -1 || exitIdx+1 == len(fixes) {
		return
	}
	after := fixes[exitIdx+1:]

	// fixesFollowed is how many of the route's fixes past the exit the path
	// charts in order, starting where it reaches the first of them. A path
	// that reaches that fix behind the exit follows none of them: the route
	// joins the SID there rather than carrying on along it.
	fixesFollowed := func(path []string) int {
		start := slices.Index(path, after[0])
		if start == -1 {
			return 0
		}
		if exitAt := slices.Index(path, exit); exitAt != -1 && start <= exitAt {
			return 0
		}
		return commonPrefixLen(after, path[start:])
	}

	best, bestSID := 0, ""
	for _, sid := range ap.exitSIDs(db, exit) {
		s, ok := LookupSID(db, icao, sid)
		if !ok {
			continue
		}
		for _, path := range ChartedSIDPaths(s) {
			if n := fixesFollowed(path); n > best {
				best, bestSID = n, sid
			}
		}
	}

	if best > 1 {
		newExit := after[best-1]
		e.ErrorString(`route follows the %s SID past the exit to %s; make %s the exit and start the route there, `+
			`with "sid" flying the SID from the CIFP and "climbout_actions" giving any tower-assigned heading `+
			`or actions in place of hand-written "waypoints"`, bestSID, newExit, newExit)
	}
}

// appendUnique adds fix to fixes unless it is empty or already there.
func appendUnique(fixes []string, fix string) []string {
	if fix == "" || slices.Contains(fixes, fix) {
		return fixes
	}
	return append(fixes, fix)
}

// SIDPathExits returns the exits a route implies by where its first fix
// joins a charted path of the named SIDs: the exits behind the fix on those
// paths and, for paths with none behind, the exits ahead of it. wps is the
// route's parsed waypoints, already trimmed of airport ids; exits holds the
// exit base names to look for. SID names tolerate a stale revision.
func SIDPathExits(db Database, icao ICAOAirportCode, wps WaypointArray, exits map[string]bool,
	sids []string) (behind, ahead []string) {
	i := slices.IndexFunc(wps, func(wp Waypoint) bool { return IsNamedFix(wp.Fix) })
	if i == -1 {
		return
	}
	first := wps[i].Fix

	exitBefore := func(path []string, at int) string {
		for _, fix := range slices.Backward(path[:at]) {
			if exits[fix] {
				return fix
			}
		}
		return ""
	}
	exitAfter := func(path []string, at int) string {
		for _, fix := range path[at+1:] {
			if exits[fix] {
				return fix
			}
		}
		return ""
	}

	for _, name := range sids {
		s, ok := LookupSID(db, icao, name)
		if !ok {
			continue
		}
		for _, path := range ChartedSIDPaths(s) {
			j := slices.Index(path, first)
			if j == -1 {
				continue
			}
			if exit := exitBefore(path, j); exit != "" {
				behind = appendUnique(behind, exit)
			} else {
				ahead = appendUnique(ahead, exitAfter(path, j))
			}
		}
	}
	return
}

// LookupSID returns the airport's CIFP SID with the given name, tolerating a
// stale revision number: DEEZZ5 finds DEEZZ6 when that is what the CIFP has.
func LookupSID(db Database, icao ICAOAirportCode, name string) (SID, bool) {
	sids := db.AirportSIDs(icao)
	if len(sids) == 0 {
		return SID{}, false
	}
	if s, ok := sids[name]; ok {
		return s, true
	}
	base := ProcedureBase(name)
	for _, n := range util.SortedMapKeys(sids) {
		if ProcedureBase(n) == base {
			return sids[n], true
		}
	}
	return SID{}, false
}

// parseClimboutActions parses the route's "climbout_actions" waypoint
// options--actions, triggers, altitude/speed restrictions, and a /ld or /rd
// turn direction--into the waypoint that carries them.
func (er *ExitRoute) parseClimboutActions() (Waypoint, error) {
	wp, turn, err := parseWaypointActionValue(er.ClimboutActions)
	if err != nil {
		return Waypoint{}, err
	}
	if wp.FlyOver() {
		return Waypoint{}, fmt.Errorf("/flyover: the climbout point is not a fix the route turns at")
	}
	// A /ld or /rd gives the turn made turning on course at the end of the
	// climbout; carry it on the waypoint for initialize to put on the first
	// fix of the route.
	wp.SetTurn(turn)
	// The name labels errors from the checks the waypoint goes through.
	wp.Fix = "climbout_actions"
	return wp, nil
}

// amendSIDWaypoints applies the route's "initial_heading" and
// "waypoint_actions" to the SID's waypoints from the CIFP.
func (er *ExitRoute) amendSIDWaypoints(wps WaypointArray, e *util.ErrorLogger) WaypointArray {
	if h := er.InitialHeading; h < 0 || h > 360 {
		e.ErrorString(`"initial_heading" %d: must be between 1 and 360`, h)
	}
	for _, key := range util.SortedMapKeys(er.WaypointActions) {
		fix, offset, err := parseWaypointActionKey(key)
		if err != nil {
			e.ErrorString(`"waypoint_actions" %q: %v`, key, err)
			continue
		}
		amended, err := wps.applyActions(fix, offset, er.WaypointActions[key])
		if err != nil {
			e.ErrorString(`"waypoint_actions" %q: %v`, key, err)
			continue
		}
		wps = amended
	}
	return wps
}

// chartedSIDRoute reports whether the route's hand-written waypoints fly the
// CIFP's SID off the runway to the exit, and if so returns what stands in for
// them: the tower-assigned heading the route leaves the runway on in place of
// the SID's runway transition, if any, and the "waypoint_actions" that give
// its own actions at the SID's fixes.
func (er *ExitRoute) chartedSIDRoute(db Database, icao ICAOAirportCode, rwy RunwayID, exit ExitID, r, rend Runway,
	loc Locator, nmPerLongitude float32, magneticVariation float32) (int, map[string]string, bool) {
	sid, transition, _ := strings.Cut(er.SID, ".")

	flies := func(wps WaypointArray, initialHeading bool) (map[string]string, bool) {
		charted, err := sidWaypoints(db, icao, sid, transition, rwy, exit, initialHeading)
		if err != nil {
			return nil, false
		}
		var scratch util.ErrorLogger
		charted = charted.InitializeLocations(loc, nmPerLongitude, magneticVariation, true, &scratch)
		if scratch.HaveErrors() || !wps.sameCourse(charted) {
			return nil, false
		}

		actions := make(map[string]string)
		if !wps.addedActions(charted, actions) {
			return nil, false
		}
		// Only redundant if the CIFP's SID with those actions is the route
		// exactly, not just closely.
		amend := ExitRoute{WaypointActions: actions}
		if amended := amend.amendSIDWaypoints(charted, &scratch); scratch.HaveErrors() || !wps.sameRoute(amended) {
			return nil, false
		}
		return actions, true
	}

	if actions, ok := flies(er.Waypoints, false); ok {
		return 0, actions, true
	}

	// Failing that, the route may leave the runway on a tower-assigned
	// heading, which is what "initial_heading" gives. It stands in for the
	// SID's runway transition, so only take it for one where the CIFP charts
	// none; a route that overrides a charted transition is its own.
	// Restrictions or sim actions at the departure end have nowhere to go in
	// this form either.
	if _, charted := db.AirportSIDs(icao)[sid].RunwayTransitions[rwy.Base()]; charted {
		return 0, nil, false
	}
	heading, wps := 0, er.Waypoints
	for len(wps) > 0 && atDepartureEnd(wps[0], r, rend, nmPerLongitude) {
		if wps[0].AltitudeRestriction() != nil || wps[0].SpeedRestriction() != nil {
			return 0, nil, false
		}
		for _, group := range wps[0].ActionGroups() {
			// "initial_heading" gives a plain heading and nothing else, so a
			// turn direction, a ground track, or a radial has to stay in
			// "waypoints".
			h := group.Actions.Heading
			if group.Until.Type != WaypointActionNoTermination || group.Actions.HasSimActions() ||
				h != (WaypointHeadingAction{Heading: h.Heading}) || h.Heading < 1 || h.Heading > 360 {
				return 0, nil, false
			}
			heading = int(h.Heading)
		}
		wps = wps[1:]
	}
	if heading == 0 {
		return 0, nil, false
	}
	if actions, ok := flies(wps, true); ok {
		return heading, actions, true
	}
	return 0, nil, false
}

// checkChartedSIDRoute reports a hand-written route that spells out the SID it
// names as the CIFP charts it. One set of waypoints serves every exit in the
// route's key, so it is only redundant if they are the SID's to each of them.
func (er *ExitRoute) checkChartedSIDRoute(db Database, icao ICAOAirportCode, rwy RunwayID, exits []ExitID, r, rend Runway,
	loc Locator, nmPerLongitude float32, magneticVariation float32, e *util.ErrorLogger) {
	if er.SID == "" || len(exits) == 0 {
		return
	}

	var heading int
	var actions map[string]string
	for _, exit := range exits {
		h, a, ok := er.chartedSIDRoute(db, icao, rwy, exit, r, rend, loc, nmPerLongitude, magneticVariation)
		if !ok {
			return
		}
		heading, actions = h, a
	}

	var give []string
	if heading != 0 {
		give = append(give, fmt.Sprintf(`"initial_heading": %d`, heading))
	}
	if len(actions) > 0 {
		encoded, _ := json.Marshal(actions)
		give = append(give, fmt.Sprintf(`"waypoint_actions": %s`, encoded))
	}
	advice := `drop them and let "sid" give the route`
	if len(give) > 0 {
		advice = "drop them and give " + strings.Join(give, " and ")
	}
	e.ErrorString(`"waypoints" fly the %s SID off runway %s as the CIFP charts it; %s`,
		er.SID, rwy.Base(), advice)
}

// How close to the departure end of the runway a route's first waypoint has
// to be to be taken for one, in nm along the runway and off its centerline.
const (
	departureEndAlong   = 0.25
	departureEndLateral = 0.1
)

// atDepartureEnd reports whether the waypoint sits at the departure end of
// the runway--named as the opposite runway's threshold, or placed there as a
// lat-long--rather than being a fix the aircraft flies to after takeoff.
func atDepartureEnd(wp Waypoint, r, rend Runway, nmPerLongitude float32) bool {
	end := math.LL2NM(rend.Threshold, nmPerLongitude)
	along := math.Normalize2f(math.Sub2f(end, math.LL2NM(r.Threshold, nmPerLongitude)))
	v := math.Sub2f(math.LL2NM(wp.Location, nmPerLongitude), end)
	return math.Abs(math.Dot(v, along)) <= departureEndAlong &&
		math.Abs(math.Dot(v, [2]float32{along[1], -along[0]})) <= departureEndLateral
}

// initialize puts the runway in front of the route's located waypoints--its
// threshold and then its midpoint, from which the aircraft tracks the runway
// centerline until it is 400' above the field and only then flies the
// route--and checks the route's other members against them. override carries
// the route's parsed "climbout_actions": actions and restrictions that
// apply at the midpoint, once the aircraft is 400' up.
func (er *ExitRoute) initialize(db Database, icao ICAOAirportCode, rwy RunwayID, r, rend Runway, nmPerLongitude float32,
	magneticVariation float32, controlPositions map[ControlPosition]*Controller, override Waypoint,
	e *util.ErrorLogger) {
	course := math.TrueToMagnetic(math.Heading2LL(r.Threshold, rend.Threshold, nmPerLongitude), magneticVariation)

	// Waypoints at the departure end of the runway are the old way of saying
	// "fly the runway to the end and then turn on course"; the centerline
	// track below does that, so they go away and what they did is done once
	// the aircraft is 400' up.
	var departureEndGroups []WaypointActionGroup
	var departureEndAltitude *AltitudeRestriction
	var departureEndSpeed *SpeedRestriction
	for len(er.Waypoints) > 0 && atDepartureEnd(er.Waypoints[0], r, rend, nmPerLongitude) {
		wp := er.Waypoints[0]
		if wp.AlongLeg() {
			e.ErrorString(`"waypoint_actions" %s: the point is at the departure end of the runway, `+
				`before the route begins; "climbout_actions" gives actions that run there`, wp.Fix)
		}
		departureEndGroups = append(departureEndGroups, wp.ActionGroups()...)
		if ar := wp.AltitudeRestriction(); ar != nil {
			departureEndAltitude = ar
		}
		if sr := wp.SpeedRestriction(); sr != nil {
			departureEndSpeed = sr
		}
		er.Waypoints = er.Waypoints[1:]
	}

	// The override's restrictions belong to the same point and supersede the
	// departure end waypoints'.
	if ar := override.AltitudeRestriction(); ar != nil {
		departureEndAltitude = ar
	}
	if sr := override.SpeedRestriction(); sr != nil {
		departureEndSpeed = sr
	}

	// A first fix close behind where the aircraft turns on course is almost always the runway's
	// own threshold named in place of its departure end....
	if len(er.Waypoints) > 0 && er.InitialHeading == 0 && !override.AssignsHeading() && departureEndGroups == nil &&
		math.HeadingDifference(course, r.Heading) <= 45 {
		first := er.Waypoints[0]
		along := math.Normalize2f(math.Sub2f(math.LL2NM(rend.Threshold, nmPerLongitude), math.LL2NM(r.Threshold, nmPerLongitude)))
		toFix := math.Sub2f(math.LL2NM(first.Location, nmPerLongitude), math.LL2NM(rend.Threshold, nmPerLongitude))
		if math.Length2f(toFix) <= 2 && math.Dot(toFix, along) < -0.1 {
			e.ErrorString("%s: first fix is behind the aircraft as it leaves runway %s; the departure end of the runway is %s-%s",
				first.Fix, rwy.Base(), icao, rend.Id)
		}
	}

	midWp := Waypoint{Fix: rwy.Base() + "-mid", Location: math.Lerp2f(0.5, r.Threshold, rend.Threshold)}
	// Every departure holds the runway centerline as a ground track until it
	// is 400' above the field; only then does it turn on course.
	track := int16(math.Round(float32(math.NormalizeHeading(course))))
	if track == 0 { // headings are given as 1-360; 0 means unset
		track = 360
	}
	groups := []WaypointActionGroup{
		{
			Actions: WaypointActions{Heading: WaypointHeadingAction{Heading: track, Track: true}},
			Until: WaypointActionTermination{
				Type:      WaypointActionAltitude,
				Altitude:  db.AirportElevation(icao) + 400,
				AtOrAbove: true,
			},
		},
	}
	// The tower-applied actions from the takeoff clearance:
	// "climbout_actions" gives them in full, while "initial_heading" is
	// the common case of a bare assigned heading.
	overrideGroups := override.ActionGroups()
	assignsHeading := override.AssignsHeading()
	if h := er.InitialHeading; h >= 1 && h <= 360 {
		overrideGroups = []WaypointActionGroup{{Actions: WaypointActions{Heading: WaypointHeadingAction{Heading: int16(h)}}}}
		assignsHeading = true
	}
	if assignsHeading {
		// The tower's assigned heading: turn to it 400' above the field and
		// fly it until the departure controller sends the aircraft direct to
		// a fix on the SID. It supersedes the charted legs, but sim actions
		// given at the departure end still run at 400', with the turn.
		overrideGroups = slices.Clone(overrideGroups)
		first := &overrideGroups[0]
		for _, g := range departureEndGroups {
			g.Actions.Heading = WaypointHeadingAction{}
			first.Actions.merge(g.Actions)
		}
		groups = append(groups, overrideGroups...)
	} else {
		groups = append(groups, departureEndGroups...)
		groups = append(groups, overrideGroups...)
	}
	midWp.InitExtra().ActionGroups = groups
	// The departure end waypoints' restrictions apply from here on out.
	if departureEndAltitude != nil {
		midWp.SetAltitudeRestriction(*departureEndAltitude)
	}
	if departureEndSpeed != nil {
		midWp.SetSpeedRestriction(*departureEndSpeed)
	}

	er.Waypoints = append([]Waypoint{
		{
			Fix:      rwy.Base(),
			Location: r.Threshold,
		},
		midWp}, er.Waypoints...)

	// A "climbout_actions" /ld or /rd gives the turn made turning on course
	// at the end of the climbout, so its flag goes on the first fix of the
	// route, past the runway threshold and midpoint.
	if t := override.Turn(); t != TurnClosest {
		if len(er.Waypoints) <= 2 {
			e.ErrorString(`"climbout_actions": /ld or /rd has no fix of the route to turn to`)
		} else {
			er.Waypoints[2].SetTurn(t)
		}
	}

	for i := range er.Waypoints {
		er.Waypoints[i].SetOnSID(true)

		if er.Waypoints[i].HasTransferCommsAction() {
			er.WaitToContactDeparture = true
		}
	}
	er.Waypoints.checkProcedureActions(e)

	if er.Waypoints.HasHumanHandoff() {
		if er.HandoffController == "" {
			e.ErrorString(`no "handoff_controller" specified even though route has "/ho"`)
		} else if _, ok := controlPositions[er.HandoffController]; !ok {
			e.ErrorString("control position %q unknown in scenario", er.HandoffController)
		}
	} else if er.HandoffController != "" {
		e.ErrorString(`"handoff_controller" specified but won't be used since route has no "/ho"`)
	}

	if er.AssignedAltitude == 0 && er.ClearedAltitude == 0 {
		e.ErrorString(`must specify either "assigned_altitude" or "cleared_altitude"`)
	} else if er.AssignedAltitude != 0 && er.ClearedAltitude != 0 {
		e.ErrorString(`cannot specify both "assigned_altitude" and "cleared_altitude"`)
	}

	if er.ERAM != nil {
		er.ERAM.Check(e)
	}
}

// FinalHeading returns the heading the route leaves the aircraft on, or 0
// if it ends at a fix rather than on vectors.
func (er ExitRoute) FinalHeading() int {
	for _, v := range slices.Backward(er.Waypoints) {
		if groups := v.ActionGroups(); len(groups) > 0 {
			last := groups[len(groups)-1]
			if h := last.Actions.Heading; h.Heading != 0 && last.Until.Type == WaypointActionNoTermination {
				return int(h.Heading)
			}
		}
	}
	return 0
}

type Departure struct {
	Exit ExitID `json:"exit"`

	Destination    ICAOAirportCode         `json:"destination"`
	Altitudes      util.SingleOrArray[int] `json:"altitude,omitempty"`
	Route          string                  `json:"route"`
	RouteWaypoints WaypointArray           // not specified in user JSON
	// Airlines is optional: without it the scenario can't generate its own
	// departures here, but published traffic still flies the exit and route.
	Airlines            []DepartureAirline `json:"airlines"`
	Scratchpad          string             `json:"scratchpad"`           // optional
	SecondaryScratchpad string             `json:"secondary_scratchpad"` // optional
	Description         string             `json:"description"`
}

type DepartureAirline struct {
	AirlineSpecifier
}

// TrafficRoutes says how published traffic between an airport and specific
// other airports is routed, keyed by the other airport's ICAO code.
type TrafficRoutes struct {
	Departures map[ICAOAirportCode]TrafficRouteSet `json:"departures"`
	Arrivals   map[ICAOAirportCode]TrafficRouteSet `json:"arrivals"`
}

// TrafficRoute is one route and the aircraft classes it applies to.
type TrafficRoute struct {
	Route    string        `json:"route"`
	Aircraft AircraftClass `json:"aircraft,omitempty"`
}

// TrafficRouteSet is the routes for one city pair; a bare JSON string is
// shorthand for a single route open to all aircraft.
type TrafficRouteSet []TrafficRoute

func (ts *TrafficRouteSet) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var route string
		if err := json.Unmarshal(b, &route); err != nil {
			return err
		}
		*ts = TrafficRouteSet{TrafficRoute{Route: route}}
		return nil
	}
	var routes []TrafficRoute
	if err := json.Unmarshal(b, &routes); err != nil {
		return err
	}
	*ts = routes
	return nil
}

// Routes returns the routes the given aircraft type may fly, in listed order.
func (ts TrafficRouteSet) Routes(db Database, acType string) []string {
	var routes []string
	for _, r := range ts {
		if r.Aircraft.Matches(db, acType) {
			routes = append(routes, r.Route)
		}
	}
	return routes
}

// ExitCategory returns the category the airport gives the exit, or "" if it
// gives it none. A suffix on an exit id selects one of a gate's variants (the
// runway flow it is flown in, the class of aircraft flying it, an enroute
// transition), so an exit with no category of its own takes the one given to
// its base fix: the category describes the gate, which the variants share.
func (ap *Airport) ExitCategory(exit ExitID) string {
	if cat, ok := ap.ExitCategories[exit]; ok {
		return cat
	}
	return ap.ExitCategories[ExitID(exit.Base())]
}

// checkExits validates the three places exits are named against each other:
// "departure_routes", "departures", and "exit_categories".
func (ap *Airport) checkExits(db Database, e *util.ErrorLogger) {
	// Names an "exit_categories" entry may go by: an exit id, or the base fix
	// of one, which ExitCategory takes as covering all of the gate's variants.
	named := make(map[ExitID]any)
	addNames := func(exit ExitID) {
		named[exit] = nil
		named[ExitID(exit.Base())] = nil
	}

	routeExits := make(map[ExitID]any)
	for _, routes := range ap.DepartureRoutes {
		for exit := range routes {
			routeExits[exit] = nil
			addNames(exit)
		}
	}
	depExits := make(map[ExitID]any)
	for _, dep := range ap.Departures {
		depExits[dep.Exit] = nil
		addNames(dep.Exit)
	}

	// The sim looks up the routes a departure flies by its full exit id, so a
	// departure no runway names exactly can never be spawned.
	for _, exit := range util.SortedMapKeys(depExits) {
		if _, ok := routeExits[exit]; !ok {
			e.ErrorString(`departure exit %q: no runway in "departure_routes" has a route to it`, exit)
		}
	}
	for _, exit := range util.SortedMapKeys(ap.ExitCategories) {
		if _, ok := named[exit]; !ok {
			e.ErrorString(`"exit_categories" exit %q is used by no departure route or departure`, exit)
		}
	}

	// An exit id that names no fix is a misspelling unless the airport means it
	// as a label--a pseudo-gate for VFR practice traffic, say--and a label it
	// means is one a departure flies or the airport gives a category.
	for _, exit := range util.SortedMapKeys(routeExits) {
		_, flown := depExits[exit]
		if flown || ap.ExitCategory(exit) != "" {
			continue
		}
		if _, ok := db.Locate(exit.Base()); !ok {
			e.ErrorString(`"departure_routes" exit %q names no fix and nothing else uses it`, exit)
		}
	}
}

// routeReachesExit reports whether a departure route out of the airport
// flies over one of its exits or joins a charted path of the exits' SIDs
// that reaches one, mirroring how departureExit places published flights: a
// route that merely names a SID without touching one of its charted fixes
// reaches nothing.
func (ap *Airport) routeReachesExit(db Database, route string, icao ICAOAirportCode) bool {
	wps := TrimDepartureAirportWaypoints(db, RouteWaypoints(db, route, nil), icao)

	exits := make(map[string]bool)
	var sids []string
	for _, exitRoutes := range ap.DepartureRoutes {
		for exit, routes := range exitRoutes {
			exits[exit.Base()] = true
			for _, er := range routes {
				name, _, _ := strings.Cut(er.SID, ".")
				sids = appendUnique(sids, name)
			}
		}
	}

	if slices.ContainsFunc(wps, func(wp Waypoint) bool { return exits[wp.Fix] }) {
		return true
	}
	behind, ahead := SIDPathExits(db, icao, wps, exits, sids)
	return len(behind) > 0 || len(ahead) > 0
}
