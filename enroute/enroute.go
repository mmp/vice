// enroute/enroute.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package enroute emulates the slice of the enroute host computer (ERAM) that
// vice needs: given a flight's route and a simple trajectory model, derive
// the coordination fix at which it crosses the boundary between the center
// and one of its TRACONs, the way the host would when sending the flight plan
// to STARS; all real ERAM background processing is out of scope. The
// adaptation lives on the ERAM host (ARTCC) facility config and is keyed, for
// arts_coordination, by the TRACON's STARS computer id (A90 = "BOA").
// route_based coordination is tried first; zone_based is the fallback when no
// route rule matched.
package enroute

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Adaptation

// ArtsCoordEntry is the per-TRACON coordination adaptation.
type ArtsCoordEntry struct {
	RouteBased []RouteRule `json:"route_based"`
	ZoneBased  []ZoneArea  `json:"zone_based"`
}

// RouteRule matches a filed route and supplies a coordination fix. The
// "waypoints" type matches the fixes the flight actually flies over rather
// than its filed route string.
type RouteRule struct {
	Type         string     `json:"type"`          // sid | star | airway | string | waypoints
	ID           string     `json:"id"`            // procedure/route/airway id; for "string"/"waypoints", the fix sequence
	Direction    string     `json:"direction"`     // up | down | both (airway only; sid/star = down)
	AltitudeKind string     `json:"altitude_kind"` // "Assigned" (default) | "Trajectory": which altitude the fix criteria compare
	Fixes        []CoordFix `json:"fixes"`         // criteria-ordered; first match wins
	DefaultFix   string     `json:"default_fix"`   // used when no criteria fix matches
}

// CoordFix is a criteria-gated coordination fix. A criterion is skipped when
// empty; `!`-prefixed tokens exclude (convention, shared with STARS).
type CoordFix struct {
	Fix         string   `json:"fix"`
	Altitude    [2]int   `json:"altitude"`     // [lo,hi] hundreds of ft; [0,0] = any
	Engine      []string `json:"engine"`       // jet | turboprop | prop (mapped to J/T/P)
	Nav         []string `json:"nav"`          // rnav | conventional
	Type        []string `json:"type"`         // aircraft ICAO type
	DestAirport []string `json:"dest_airport"` // destination airport filter
}

// ZoneArea is a zone-based coordination area: with several areas adapted, the
// one whose center is nearest the flight's boundary point applies, and then
// the fix pick is a bearing sector from Center.
type ZoneArea struct {
	AreaID       string        `json:"area_id"`
	CenterStr    string        `json:"center"`
	Center       math.Point2LL `json:"-"`
	AltitudeKind string        `json:"altitude_kind"`
	Arrival      []ZoneEntry   `json:"arrival"`
	Departure    []ZoneEntry   `json:"departure"`
	Overflight   []ZoneEntry   `json:"overflight"`
}

// ZoneEntry selects a coordination fix by bearing sector (+ optional criteria).
type ZoneEntry struct {
	Bearings   string     `json:"bearings"` // "start,end" degrees; "" = any
	Fixes      []CoordFix `json:"fixes"`
	DefaultFix string     `json:"default_fix"`
}

// Restriction caps or floors a matching flight's modeled vertical profile;
// only altitude restrictions are supported.
type Restriction struct {
	Name                string          `json:"name"`
	FlightType          string          `json:"flight_type"` // arrival | departure
	Procedure           string          `json:"procedure"`
	Mode                string          `json:"mode"` // line
	LineStr             string          `json:"line"`
	Line                []math.Point2LL `json:"-"`
	AltitudeRestriction string          `json:"altitude_restriction"`
	ArrivalAirports     []string        `json:"arrival_airports"`
	Aircraft            struct {
		Engine []string `json:"engine"`
		Type   []string `json:"type"`
	} `json:"aircraft"`
}

// Coordination is the resolved adaptation a sim uses: the coordination entry
// and restrictions that apply to its TRACON computer id. Populated at
// scenario-group load from the ERAM host config.
type Coordination struct {
	ComputerID   string
	Coord        *ArtsCoordEntry
	Restrictions []Restriction
}

// DBLocator resolves locations against the static nav database alone; it is
// used for facility-config data that must resolve the same way regardless of
// which scenario group (with its own fixes) is loading it, and to re-derive
// ArtsCoordEntry/Restriction geometry (see ParseGeometry) after a saved sim is
// restored, since that geometry is excluded from JSON.
type DBLocator struct{}

func (DBLocator) Similar(fix string) []string {
	d1, d2 := util.SelectInTwoEdits(fix, maps.Keys(av.DB.Navaids), nil, nil)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Airports), d1, d2)
	d1, d2 = util.SelectInTwoEdits(fix, maps.Keys(av.DB.Fixes), d1, d2)
	return util.Select(len(d1) > 0, d1, d2)
}

func (DBLocator) Declination(s string) (float32, bool) {
	return av.DB.Declination(s)
}

func (DBLocator) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	if n, ok := av.DB.Navaids[s]; ok {
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

// parseBoundary parses a space-separated list of "lat,long" vertices.
func parseBoundary(s string) ([]math.Point2LL, error) {
	var pts []math.Point2LL
	for tok := range strings.FieldsSeq(s) {
		p, err := math.ParseLatLong([]byte(tok))
		if err != nil {
			return nil, fmt.Errorf("%q: %w", tok, err)
		}
		pts = append(pts, p)
	}
	return pts, nil
}

// ParseGeometry parses the adaptation's geometry strings (zone centers,
// restriction lines) into their native forms. The server calls it once per
// ERAM host config after all facility configs are loaded; TRACON scenario
// groups then share the parsed adaptation read-only.
func ParseGeometry(coord map[string]*ArtsCoordEntry, restrictions []Restriction, loc av.Locator, e *util.ErrorLogger) {
	for id, entry := range coord {
		for i := range entry.ZoneBased {
			za := &entry.ZoneBased[i]
			if za.CenterStr == "" {
				e.ErrorString("arts_coordination[%s] zone_based %s: no center", id, za.AreaID)
				continue
			}
			if pos, ok := loc.Locate(za.CenterStr); ok {
				za.Center = pos
			} else {
				e.ErrorString("arts_coordination[%s] zone_based %s: unknown center %q", id, za.AreaID, za.CenterStr)
			}
		}
	}
	for i := range restrictions {
		r := &restrictions[i]
		if r.LineStr == "" {
			continue
		}
		pts, err := parseBoundary(r.LineStr)
		if err != nil {
			e.ErrorString("restrictions %q: bad line: %v", r.Name, err)
		} else {
			r.Line = pts
		}
	}
}

// Validate performs non-geometry validation of the coordination adaptation.
// Errors accumulate in e.
func Validate(coord map[string]*ArtsCoordEntry, restrictions []Restriction, e *util.ErrorLogger) {
	for id, entry := range coord {
		e.Push(fmt.Sprintf("arts_coordination[%s]", id))
		for i, r := range entry.RouteBased {
			e.Push(fmt.Sprintf("route_based[%d]", i))
			switch strings.ToLower(r.Type) {
			case "sid", "star", "airway", "string", "waypoints":
			case "":
				e.ErrorString(`"type" is required (sid, star, airway, string, or waypoints)`)
			default:
				e.ErrorString(`invalid "type" %q: want sid, star, airway, string, or waypoints`, r.Type)
			}
			if r.ID == "" {
				e.ErrorString(`"id" is required`)
			}
			if r.Direction != "" && r.Direction != "up" && r.Direction != "down" && r.Direction != "both" {
				e.ErrorString(`invalid "direction" %q: want up, down, or both`, r.Direction)
			}
			if len(r.Fixes) == 0 && r.DefaultFix == "" {
				e.ErrorString(`route rule has no "fixes" and no "default_fix"`)
			}
			e.Pop()
		}
		for i, za := range entry.ZoneBased {
			e.Push(fmt.Sprintf("zone_based[%d]", i))
			if za.AreaID == "" {
				e.ErrorString(`"area_id" is required`)
			}
			for _, bucket := range [][]ZoneEntry{za.Arrival, za.Departure, za.Overflight} {
				for _, ze := range bucket {
					if ze.Bearings != "" {
						if _, _, ok := parseBearings(ze.Bearings); !ok {
							e.ErrorString(`invalid "bearings" %q: want "start,end" degrees`, ze.Bearings)
						}
					}
					if len(ze.Fixes) == 0 && ze.DefaultFix == "" {
						e.ErrorString(`zone entry (bearings %q) has no "fixes" and no "default_fix"`, ze.Bearings)
					}
				}
			}
			e.Pop()
		}
		e.Pop()
	}
	for i, r := range restrictions {
		e.Push(fmt.Sprintf("restrictions[%d]", i))
		if r.FlightType != "arrival" && r.FlightType != "departure" {
			e.ErrorString(`"flight_type" %q must be "arrival" or "departure"`, r.FlightType)
		}
		if r.Mode != "" && r.Mode != "line" {
			e.ErrorString(`invalid "mode" %q: only "line" is supported`, r.Mode)
		}
		if _, err := av.ParseAltitudeRestriction(r.AltitudeRestriction); err != nil {
			e.ErrorString(`invalid "altitude_restriction": %v`, err)
		}
		e.Pop()
	}
}

///////////////////////////////////////////////////////////////////////////
// Criteria matching

// Attrs are the aircraft attributes coordination criteria match against.
type Attrs struct {
	Engine        string // spec vocabulary: "jet" | "turboprop" | "prop"
	Nav           string // "rnav" | "conventional"
	ACType        string // aircraft ICAO type
	DestAirport   string
	AssignedLevel int // assigned altitude in hundreds of feet (for altitude_kind Assigned)
}

// matchTokens applies an include/exclude token list (`!` convention)
// to a value. Empty list = criterion skipped. Any matching `!token` excludes;
// if any positive tokens are present, at least one must match.
func matchTokens(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	hasInclude, included := false, false
	for _, p := range patterns {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if strings.EqualFold(neg, value) {
				return false
			}
		} else {
			hasInclude = true
			if strings.EqualFold(p, value) {
				included = true
			}
		}
	}
	return !hasInclude || included
}

// matchCoordFix reports whether a coordination fix's criteria are satisfied.
// altHundreds is the flight's altitude at the fix in hundreds of feet.
func matchCoordFix(f CoordFix, a Attrs, altHundreds int) bool {
	if f.Altitude[0] != 0 || f.Altitude[1] != 0 {
		if altHundreds < f.Altitude[0] || altHundreds > f.Altitude[1] {
			return false
		}
	}
	return matchTokens(f.Engine, a.Engine) &&
		matchTokens(f.Nav, a.Nav) &&
		matchTokens(f.Type, a.ACType) &&
		matchTokens(f.DestAirport, a.DestAirport)
}

// procedureMatches reports whether a route contains the given procedure token.
// A trailing '#' is a wildcard for the version suffix (e.g. "ROBUC#" matches
// "ROBUC3").
func procedureMatches(routeStr, proc string) bool {
	if proc == "" {
		return true
	}
	if prefix, wild := strings.CutSuffix(proc, "#"); wild {
		for tok := range strings.FieldsSeq(routeStr) {
			if strings.HasPrefix(tok, prefix) {
				return true
			}
		}
		return false
	}
	for tok := range strings.FieldsSeq(routeStr) {
		if tok == proc {
			return true
		}
	}
	return false
}

// restrictionApplies reports whether an altitude restriction applies to a flight
// matching flight type, procedure, arrival airport, and aircraft
// include/exclude criteria.
func restrictionApplies(r Restriction, ft av.TypeOfFlight, routeStr, arrivalAirport string, attrs Attrs) bool {
	switch r.FlightType {
	case "arrival":
		if ft != av.FlightTypeArrival {
			return false
		}
	case "departure":
		if ft != av.FlightTypeDeparture {
			return false
		}
	}
	if !procedureMatches(routeStr, r.Procedure) {
		return false
	}
	if len(r.ArrivalAirports) > 0 && !slices.Contains(r.ArrivalAirports, arrivalAirport) {
		return false
	}
	return matchTokens(r.Aircraft.Engine, attrs.Engine) && matchTokens(r.Aircraft.Type, attrs.ACType)
}

// parseBearings parses a "start,end" bearing sector into degrees.
func parseBearings(s string) (start, end int, ok bool) {
	lo, hi, found := strings.Cut(s, ",")
	if !found {
		return 0, 0, false
	}
	var err error
	if start, err = strconv.Atoi(strings.TrimSpace(lo)); err != nil {
		return 0, 0, false
	}
	if end, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
		return 0, 0, false
	}
	return start, end, true
}

///////////////////////////////////////////////////////////////////////////
// Coordination engine

// Result is the outcome of coordination for one flight.
type Result struct {
	Fix  string // the coordination fix
	Rule string // human description of the matched rule (for logging/tests)
	OK   bool
}

// DeriveCoordinationFix applies route_based then zone_based coordination.
func DeriveCoordinationFix(entry *ArtsCoordEntry, traj *Trajectory,
	attrs Attrs, ft av.TypeOfFlight, routeStr string) Result {
	// route_based first.
	flownSeq := strings.Join(FlownFixes(traj.Waypoints, attrs.DestAirport), " ")
	for _, rule := range entry.RouteBased {
		if !routeRuleMatches(rule, traj.Waypoints, routeStr, flownSeq) {
			continue
		}
		fix := selectCoordFix(rule.Fixes, rule.DefaultFix, traj, attrs, rule.AltitudeKind)
		if fix == "" {
			continue
		}
		return Result{Fix: fix, Rule: "route_based:" + rule.Type + "/" + rule.ID, OK: true}
	}
	// zone_based fallback.
	area := selectZoneArea(entry.ZoneBased, traj, ft)
	if area == nil {
		return Result{}
	}
	entries := zoneBucket(area, ft)
	bearing := zoneBearing(area.Center, traj, ft)
	for _, ze := range entries {
		if !bearingEntryMatches(ze, bearing) {
			continue
		}
		fix := selectCoordFix(ze.Fixes, ze.DefaultFix, traj, attrs, area.AltitudeKind)
		if fix == "" {
			continue
		}
		return Result{Fix: fix, Rule: "zone_based:" + area.AreaID, OK: true}
	}
	return Result{}
}

// routeRuleMatches reports whether a route_based rule matches the flight's
// route: sid/star by the presence of SID/STAR waypoints plus the procedure id
// appearing as a token in the filed route string; airway by a matching Airway
// on some waypoint; "string" by the id appearing in the route.
func routeRuleMatches(rule RouteRule, wps []av.Waypoint, routeStr, flownSeq string) bool {
	switch strings.ToLower(rule.Type) {
	case "star":
		return hasFlag(wps, av.Waypoint.OnSTAR) && routeHasToken(routeStr, rule.ID)
	case "sid":
		return hasFlag(wps, av.Waypoint.OnSID) && routeHasToken(routeStr, rule.ID)
	case "airway":
		if !airwayMatches(wps, rule.ID) {
			return false
		}
		// Direction matters only for airways: "down" = flying the route in
		// the order its fixes are defined, "up" = the opposite. A directional
		// rule only applies when the flight traverses the airway in that
		// direction; "both"/"" matches either way.
		if dir := strings.ToLower(rule.Direction); dir == "up" || dir == "down" {
			// Fail closed when the direction can't be determined, rather than
			// letting both an "up" and a "down" rule match the same flight.
			if flightAirwayDirection(wps, rule.ID) != dir {
				return false
			}
		}
		return true
	case "string":
		// The id may be a single fix or a multi-fix route string (an expanded
		// adapted route, e.g. "TUBBA CURSD"); match if the filed route is that
		// string or contains its fixes as a contiguous run.
		return routeStr == rule.ID || routeContainsSequence(routeStr, rule.ID)
	case "waypoints":
		// Unlike the filed-route types, this matches the fixes the flight
		// actually flies over, anchored at the start of its route: approach,
		// runway, and airway fixes appended past the adapted sequence can't
		// disturb the match.
		return flownSeq == rule.ID || strings.HasPrefix(flownSeq, rule.ID+" ")
	}
	return false
}

// NamedFixes returns the proper fix names along a route's waypoints, skipping
// synthesized waypoints, raw lat-long positions, and adjacent repeats.
func NamedFixes(wps []av.Waypoint) []string {
	var fixes []string
	for _, wp := range wps {
		if wp.Fix == "" || strings.HasPrefix(wp.Fix, "_") {
			continue
		}
		if _, err := math.ParseLatLong([]byte(wp.Fix)); err == nil {
			continue
		}
		if len(fixes) > 0 && fixes[len(fixes)-1] == wp.Fix {
			continue
		}
		fixes = append(fixes, wp.Fix)
	}
	return fixes
}

// FlownFixes returns the fix names a "waypoints" rule is matched against: the
// named fixes along the route with the trailing destination airport dropped
// (nav appends it to every route, and adapted rule sequences end where the
// adapted route does, not at the field). (Exported so adaptation tooling can
// build rule ids the matcher will accept.)
func FlownFixes(wps []av.Waypoint, destAirport string) []string {
	fixes := NamedFixes(wps)
	if n := len(fixes); n > 0 && destAirport != "" && fixes[n-1] == destAirport {
		fixes = fixes[:n-1]
	}
	return fixes
}

// routeContainsSequence reports whether the tokens of id appear as a contiguous
// run within routeStr's tokens. A single-token id degenerates to a token match.
func routeContainsSequence(routeStr, id string) bool {
	idToks := strings.Fields(id)
	if len(idToks) == 0 {
		return false
	}
	rToks := strings.Fields(routeStr)
	for i := 0; i+len(idToks) <= len(rToks); i++ {
		match := true
		for j, t := range idToks {
			if rToks[i+j] != t {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func hasFlag(wps []av.Waypoint, pred func(av.Waypoint) bool) bool {
	return slices.ContainsFunc(wps, pred)
}

func airwayMatches(wps []av.Waypoint, id string) bool {
	for _, wp := range wps {
		if wp.Airway() == id {
			return true
		}
	}
	return false
}

// flightAirwayDirection reports whether the flight traverses the given airway
// "down" (in the airway's defined fix order) or "up" (opposite), by comparing
// the order of the flight's on-airway fixes against the canonical airway
// definition. Returns "" when it can't be determined (no on-airway fixes, or
// the fixes aren't found in the airway definition).
func flightAirwayDirection(wps []av.Waypoint, airwayID string) string {
	var flownIdx []int
	for i, wp := range wps {
		if wp.Airway() == airwayID {
			flownIdx = append(flownIdx, i)
		}
	}
	if len(flownIdx) == 0 {
		return ""
	}
	first, last := wps[flownIdx[0]].Fix, wps[flownIdx[len(flownIdx)-1]].Fix
	if len(flownIdx) == 1 {
		// Adjacent airway fixes have no intermediate fix to tag, so only the
		// clause's entry fix is marked; fall back to the very next waypoint
		// in the route, which the route grammar ("entry AIRWAY exit")
		// guarantees is that clause's exit fix, even though it isn't itself
		// tagged with this airway.
		i := flownIdx[0]
		if i+1 >= len(wps) {
			return ""
		}
		last = wps[i+1].Fix
	}
	for _, awy := range av.DB.Airways[airwayID] {
		i0, i1 := -1, -1
		for idx, af := range awy.Fixes {
			if af.Fix == first {
				i0 = idx
			}
			if af.Fix == last {
				i1 = idx
			}
		}
		if i0 >= 0 && i1 >= 0 && i0 != i1 {
			if i1 > i0 {
				return "down"
			}
			return "up"
		}
	}
	return ""
}

func routeHasToken(routeStr, id string) bool {
	for tok := range strings.FieldsSeq(routeStr) {
		if tok == id {
			return true
		}
	}
	return false
}

// selectCoordFix returns the first criteria-matching fix, else the default fix.
func selectCoordFix(fixes []CoordFix, defaultFix string, traj *Trajectory, attrs Attrs, kind string) string {
	for _, f := range fixes {
		alt := coordFixAltitude(traj, f.Fix, kind, attrs.AssignedLevel)
		if matchCoordFix(f, attrs, alt) {
			return f.Fix
		}
	}
	return defaultFix
}

// coordFixAltitude returns the altitude (hundreds of feet) the ARTS
// coordination compares against a fix's altitude range, chosen by the rule's
// altitude_kind: "Trajectory" uses the flight-plan trajectory altitude at the
// fix (cruise if off-route); anything else — including the default when unset
// — uses the flight's assigned altitude.
func coordFixAltitude(traj *Trajectory, fix, kind string, assignedLevel int) int {
	if strings.EqualFold(strings.TrimSpace(kind), "Trajectory") {
		if alt, ok := traj.AltitudeAtFix(fix); ok {
			return alt / 100
		}
		return int(traj.cruiseAlt) / 100
	}
	return assignedLevel
}

// selectZoneArea picks the coordination area: with several areas adapted, the
// one whose center is nearest the flight's TRACON boundary point.
func selectZoneArea(areas []ZoneArea, traj *Trajectory, ft av.TypeOfFlight) *ZoneArea {
	if len(areas) == 0 {
		return nil
	}
	if pt, ok := boundaryPoint(traj, ft); ok && len(areas) > 1 {
		best, bestDist := 0, float32(1e30)
		for i := range areas {
			if d := math.NMDistance2LL(areas[i].Center, pt); d < bestDist {
				best, bestDist = i, d
			}
		}
		return &areas[best]
	}
	return &areas[0]
}

// boundaryPoint returns the flight's TRACON boundary point: the entry gate
// (first waypoint) for arrivals/overflights, the exit gate (last waypoint)
// for departures.
func boundaryPoint(traj *Trajectory, ft av.TypeOfFlight) (math.Point2LL, bool) {
	if len(traj.Waypoints) == 0 {
		return math.Point2LL{}, false
	}
	if ft == av.FlightTypeDeparture {
		return traj.Waypoints[len(traj.Waypoints)-1].Location, true
	}
	return traj.Waypoints[0].Location, true
}

// zoneBearing returns the bearing (degrees) from the area center to the flight's
// boundary point (bearing from center of the trajectory crossing).
func zoneBearing(center math.Point2LL, traj *Trajectory, ft av.TypeOfFlight) int {
	pt, ok := boundaryPoint(traj, ft)
	if !ok {
		return 0
	}
	nmPer := traj.nmPerLong
	if nmPer < 1 {
		nmPer = 47 // mid-latitude default when unset
	}
	return int(math.Heading2LL(center, pt, nmPer))
}

func zoneBucket(area *ZoneArea, ft av.TypeOfFlight) []ZoneEntry {
	switch ft {
	case av.FlightTypeArrival:
		return area.Arrival
	case av.FlightTypeDeparture:
		return area.Departure
	case av.FlightTypeOverflight:
		return area.Overflight
	}
	return nil
}

// bearingEntryMatches reports whether the zone entry's bearing sector contains
// the flight's bearing. An empty sector matches any bearing.
func bearingEntryMatches(ze ZoneEntry, bearing int) bool {
	if ze.Bearings == "" {
		return true
	}
	start, end, ok := parseBearings(ze.Bearings)
	if !ok {
		return false
	}
	return bearingInSector(bearing, start, end)
}

// bearingInSector reports whether b lies in the clockwise sector [start,end],
// handling wraparound through 0.
func bearingInSector(b, start, end int) bool {
	norm := func(x int) int { return ((x % 360) + 360) % 360 }
	b, start, end = norm(b), norm(start), norm(end)
	if start <= end {
		return b >= start && b <= end
	}
	return b >= start || b <= end
}

///////////////////////////////////////////////////////////////////////////
// Trajectory

// Trajectory is the pseudo-ERAM trajectory model. It is deliberately minimal
// and kept behind this type so fidelity can be improved later:
// - Horizontal path = the filed route (the aircraft's waypoints).
// - Vertical profile = linear by flight type, using the aircraft-performance
// climb/descent rates: departures climb from field elevation toward the
// filed altitude; arrivals descend toward the arrival field; overflights are
// level at the filed altitude.
//
// It is consumed for the altitude at a route point — to evaluate
// `altitude:[lo,hi]` coordination criteria — after capping the vertical
// envelope per adapted restrictions.
//
// Acceleration/deceleration, speed, wind, and realistic top-of-climb /
// top-of-descent are not modeled.
type Trajectory struct {
	Waypoints  []av.Waypoint
	FlightType av.TypeOfFlight
	cumDist    []float32 // cumulative nm from the route start, per waypoint
	totalDist  float32
	fieldElev  float32 // departure or arrival field elevation (feet)
	cruiseAlt  float32 // filed altitude (feet)
	gradient   float32 // vertical gradient in ft/nm (climb for P, descent for A)
	nmPerLong  float32 // nm per degree longitude, for bearing computations
}

// MakeTrajectory builds the trajectory model for a flight: its route
// waypoints, filed cruise altitude, and — for a departure or arrival — the
// field elevation it climbs from or descends to. The vertical gradient comes
// from acType's performance data.
func MakeTrajectory(wps []av.Waypoint, ft av.TypeOfFlight, acType string, cruiseAlt, fieldElev, nmPerLong float32) *Trajectory {
	t := &Trajectory{
		Waypoints:  wps,
		FlightType: ft,
		cruiseAlt:  cruiseAlt,
		fieldElev:  fieldElev,
		nmPerLong:  nmPerLong,
	}
	// Cumulative along-route distance per waypoint.
	t.cumDist = make([]float32, len(wps))
	for i := 1; i < len(wps); i++ {
		t.cumDist[i] = t.cumDist[i-1] + math.NMDistance2LL(wps[i-1].Location, wps[i].Location)
	}
	if len(wps) > 0 {
		t.totalDist = t.cumDist[len(wps)-1]
	}
	perf := av.DB.AircraftPerformance[acType]
	speed := perf.Speed.CruiseTAS
	if speed < 1 {
		speed = 250 // avoid divide-by-zero for missing perf data
	}
	var rate float32
	switch ft {
	case av.FlightTypeDeparture:
		rate = perf.Rate.Climb
	case av.FlightTypeArrival:
		rate = perf.Rate.Descent
	default: // overflight: level
	}
	// Convert the vertical rate (ft/min) to a gradient (ft/nm) at cruise
	// speed: ft/nm = rate / (speed/60).
	t.gradient = rate * 60 / speed
	return t
}

// ApplyRestrictions caps/floors the trajectory's vertical envelope for every
// matching altitude restriction. A gate-line crossing (mode "line") is
// approximated as applying to the whole trajectory; only altitude is honored.
func (t *Trajectory) ApplyRestrictions(restrictions []Restriction, routeStr, arrivalAirport string, attrs Attrs) {
	for _, r := range restrictions {
		t.applyRestriction(r, t.FlightType, routeStr, arrivalAirport, attrs)
	}
}

// applyRestriction caps/floors the vertical envelope for a single restriction
// if it applies to the flight. The restriction string uses the standard FAA
// notation av.ParseAltitudeRestriction accepts: "14000+" for at-or-above,
// "14000-" for at-or-below, "14000" for a hard "at", and "10000-14000" for a
// range.
func (t *Trajectory) applyRestriction(r Restriction, ft av.TypeOfFlight, routeStr, arrivalAirport string, attrs Attrs) {
	if !restrictionApplies(r, ft, routeStr, arrivalAirport, attrs) {
		return
	}
	ar, err := av.ParseAltitudeRestriction(r.AltitudeRestriction)
	if err != nil {
		return
	}
	t.fieldElev = max(t.fieldElev, ar.Range[0])
	t.cruiseAlt = min(t.cruiseAlt, ar.Range[1])
	if t.fieldElev > t.cruiseAlt {
		t.fieldElev = t.cruiseAlt
	}
}

// AltitudeAtDistance returns the linear-profile altitude (feet) at the given
// along-route distance from the route start.
func (t *Trajectory) AltitudeAtDistance(dist float32) int {
	var alt float32
	switch t.FlightType {
	case av.FlightTypeDeparture:
		alt = t.fieldElev + t.gradient*dist
	case av.FlightTypeArrival:
		alt = t.fieldElev + t.gradient*(t.totalDist-dist)
	default:
		alt = t.cruiseAlt
	}
	return int(math.Clamp(alt, t.fieldElev, t.cruiseAlt))
}

// AltitudeAtFix returns the profile altitude (feet) at the named route fix and
// whether the fix is on the route.
func (t *Trajectory) AltitudeAtFix(fix string) (int, bool) {
	for i, wp := range t.Waypoints {
		if wp.Fix == fix {
			return t.AltitudeAtDistance(t.cumDist[i]), true
		}
	}
	return 0, false
}
