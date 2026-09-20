// aviation/procedures.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// STAR

type STAR struct {
	Transitions     map[string]WaypointArray
	RunwayWaypoints map[string]WaypointArray
}

// ProcedureBase strips a SID or STAR's revision number: CUUDA3 and CUUDA4 are
// revisions of the same arrival.
func ProcedureBase(name string) string {
	return strings.TrimRight(name, "0123456789")
}

// TokenNamesAirport reports whether a route token names the airport, as
// either its ICAO id or its FAA local identifier.
func TokenNamesAirport(token string, icao ICAOAirportCode) bool {
	if token == string(icao) {
		return true
	}
	faa, ok := ICAOAirportToFAA(icao)
	return ok && token == string(faa)
}

// TokenNamesProcedure reports whether a route token names a SID or a STAR:
// it ends with a revision digit and isn't an airway.
func TokenNamesProcedure(token string) bool {
	if token == "" {
		return false
	}
	if c := token[len(token)-1]; c < '0' || c > '9' {
		return false
	}
	_, airway := DB.Airways[token]
	return !airway
}

// TrimDepartureAirportTokens removes from the head of a filed route the
// tokens that name the departure airport itself--pilots file "OGG LNY ..."
// out of PHOG, naming the airport rather than a fix to fly--skipping over a
// leading procedure token, since filings render the SID ahead of them. A
// token leading onto an airway that reaches a further fix stays: it is the
// airway's entry, the airport's id doubling as its VOR's.
//
// This and TrimDestinationAirportTokens are for normalizing a published route
// that stays as text; code that goes on to match the route's fixes wants the
// WaypointArray pair below. The two pairs carry the same rules and have to
// stay in step: waypoints parsed by RouteWaypoints can't be rendered back to
// a route string (see RouteString), so neither pair can be written in terms
// of the other.
func TrimDepartureAirportTokens(fields []string, icao ICAOAirportCode) []string {
	i, skippedProcedure := 0, false
	for i < len(fields) {
		// The airport comes first: plenty of ids end in a digit, and one of
		// those names the airport rather than a procedure.
		if TokenNamesAirport(fields[i], icao) {
			if i+2 < len(fields) {
				if _, ok := DB.Airways[fields[i+1]]; ok {
					break
				}
			}
			fields = slices.Delete(fields, i, i+1)
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(fields[i]) {
			break
		}
		skippedProcedure = true
		i++
	}
	return fields
}

// TrimDestinationAirportTokens is TrimDepartureAirportTokens's mirror for the
// other end of the route: it removes trailing tokens that name the
// destination airport, skipping over a trailing procedure token. A token
// ending an airway that reaches back to an entry fix stays: it is the
// airway's exit, as the ITO in "... V16 UPP V2 ITO" into PHTO.
func TrimDestinationAirportTokens(fields []string, icao ICAOAirportCode) []string {
	last, skippedProcedure := len(fields)-1, false
	for last >= 0 {
		if TokenNamesAirport(fields[last], icao) {
			if last >= 2 {
				if _, ok := DB.Airways[fields[last-1]]; ok {
					break
				}
			}
			fields = slices.Delete(fields, last, last+1)
			last--
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(fields[last]) {
			break
		}
		skippedProcedure = true
		last--
	}
	return fields
}

// TrimDepartureAirportWaypoints removes the leading waypoints that name the
// departure airport, as TrimDepartureAirportTokens does for a route that is
// still text; this is the form for code that matches the route's fixes.
func TrimDepartureAirportWaypoints(wps WaypointArray, icao ICAOAirportCode) WaypointArray {
	i, skippedProcedure := 0, false
	for i < len(wps) {
		if TokenNamesAirport(wps[i].Fix, icao) {
			if wps[i].Airway() != "" && i+1 < len(wps) {
				break
			}
			wps = slices.Delete(wps, i, i+1)
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(wps[i].Fix) {
			break
		}
		skippedProcedure = true
		i++
	}
	return wps
}

// TrimDestinationAirportWaypoints removes the trailing waypoints that name
// the destination airport: TrimDepartureAirportWaypoints's mirror, and the
// WaypointArray form of TrimDestinationAirportTokens.
func TrimDestinationAirportWaypoints(wps WaypointArray, icao ICAOAirportCode) WaypointArray {
	last, skippedProcedure := len(wps)-1, false
	for last >= 0 {
		if TokenNamesAirport(wps[last].Fix, icao) {
			if last >= 1 && wps[last-1].Airway() != "" {
				break
			}
			wps = slices.Delete(wps, last, last+1)
			last--
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(wps[last].Fix) {
			break
		}
		skippedProcedure = true
		last--
	}
	return wps
}

// routeProcedureToken returns the last token of a route into or out of the
// airport if it names a procedure, or "" otherwise.
func routeProcedureToken(route string, icao ICAOAirportCode) string {
	fields := strings.Fields(route)
	if n := len(fields); n > 0 && TokenNamesAirport(fields[n-1], icao) {
		fields = fields[:n-1]
	}
	if len(fields) == 0 || !TokenNamesProcedure(fields[len(fields)-1]) {
		return ""
	}
	return fields[len(fields)-1]
}

// RouteSTAR returns the STAR a filed route into the airport ends with, under
// the name the current CIFP charts it, along with the fix filed ahead of it
// on the route, or empty strings if it names none. A route may carry a stale
// revision--CUUDA3 where the cycle has CUUDA4--so procedures match on their
// base names.
func RouteSTAR(route string, icao ICAOAirportCode) (star, entry string) {
	token := routeProcedureToken(route, icao)
	if token == "" {
		return "", ""
	}

	names := util.SortedMapKeys(DB.Airports[icao].STARs)
	i := slices.IndexFunc(names, func(name string) bool { return ProcedureBase(name) == ProcedureBase(token) })
	if i == -1 {
		return "", ""
	}
	name := names[i]

	fields := strings.Fields(route)
	if i := slices.Index(fields, token); i > 0 {
		if _, ok := DB.Airways[fields[i-1]]; !ok {
			entry = fields[i-1]
		}
	}
	return name, entry
}

// AltitudeFloor returns the highest "at or above" crossing restriction along
// the route. Cruise is the top of a flight's profile, so it can be no lower.
func (wa WaypointArray) AltitudeFloor() int {
	var floor float32
	for _, wp := range wa {
		if ar := wp.AltitudeRestriction(); ar != nil {
			floor = max(floor, ar.Range[0])
		}
	}
	return int(floor)
}

// transitionFloor returns the AltitudeFloor of the transition a route joins a
// procedure at. The fields are the route's tokens working outward from the
// procedure's own name; the first of them naming a transition is the one
// flown, since an airway or a fix off the procedure may sit between. Naming
// none, every way of flying the procedure is at or above the lowest
// transition's floor.
func transitionFloor(transitions map[string]WaypointArray, fields []string) int {
	for _, f := range fields {
		if wps, ok := transitions[f]; ok {
			return wps.AltitudeFloor()
		}
	}
	floor := -1
	for _, wps := range transitions {
		if f := wps.AltitudeFloor(); floor == -1 || f < floor {
			floor = f
		}
	}
	return max(floor, 0)
}

// routeSID returns the SID a route out of the airport begins with, under the
// name the current CIFP charts it, along with the index of the field naming
// it. A route may carry a stale revision--DOTSS2 where the cycle has
// DOTSS3--so procedures match on their base names.
func routeSID(fields []string, icao ICAOAirportCode) (SID, int, bool) {
	i := 0
	if len(fields) > 0 && TokenNamesAirport(fields[0], icao) {
		i = 1
	}
	if i >= len(fields) || !TokenNamesProcedure(fields[i]) {
		return SID{}, 0, false
	}
	for name, sid := range util.SortedMap(DB.Airports[icao].SIDs) {
		if ProcedureBase(name) == ProcedureBase(fields[i]) {
			return sid, i, true
		}
	}
	return SID{}, 0, false
}

// RouteAltitudeFloor returns the lowest altitude a flight filing the route can
// cruise at: the highest "at or above" restriction its SID and its STAR
// publish. It is 0 when the route names neither, as one out of an airport the
// CIFP doesn't cover can't, and when the procedures it does name publish no
// such restriction, as the open-route STARs into JFK don't.
func RouteAltitudeFloor(route string, departureAirport, arrivalAirport ICAOAirportCode) int {
	fields := strings.Fields(route)
	floor := 0

	if sid, i, ok := routeSID(fields, departureAirport); ok {
		floor = max(floor, transitionFloor(sid.EnrouteTransitions, fields[i+1:]))
	}

	end := len(fields)
	if end > 0 && TokenNamesAirport(fields[end-1], arrivalAirport) {
		end--
	}
	if star, _ := RouteSTAR(route, arrivalAirport); star != "" && end > 0 {
		before := slices.Clone(fields[:end-1])
		slices.Reverse(before)
		floor = max(floor, transitionFloor(DB.Airports[arrivalAirport].STARs[star].Transitions, before))
	}

	return floor
}

func (s STAR) Check(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	check := func(wps WaypointArray) {
		for _, wp := range wps {
			_, okn := DB.Navaids[wp.Fix]
			_, okf := DB.Fixes[wp.Fix]
			if !okn && !okf {
				e.ErrorString("fix %s not found in navaid database", wp.Fix)
			}
		}
	}
	for _, wps := range s.Transitions {
		check(wps)
	}
	for _, wps := range s.RunwayWaypoints {
		check(wps)
	}
}

func MakeSTAR() *STAR {
	return &STAR{
		Transitions:     make(map[string]WaypointArray),
		RunwayWaypoints: make(map[string]WaypointArray),
	}
}

const routePrintFormat = "%-13s: %s\n"

// CIFPRoute is one coded route of a procedure: the name it is flown under,
// its encoded waypoints, and the waypoints themselves. The waypoints hold no
// locations: they come from the CIFP, which names its fixes rather than
// placing them, so a caller that draws them must run InitializeLocations on
// a copy first.
type CIFPRoute struct {
	Name      string
	Route     string
	Waypoints WaypointArray
}

// Routes returns the STAR's transitions and runway routes.
func (s STAR) Routes(name string) []CIFPRoute {
	var routes []CIFPRoute
	for tr, wps := range util.SortedMap(s.Transitions) {
		routes = append(routes, CIFPRoute{Name: name + "." + tr, Route: wps.Encode(), Waypoints: wps})
	}
	for rwy, wps := range util.SortedMap(s.RunwayWaypoints) {
		routes = append(routes, CIFPRoute{Name: name + ".RWY" + rwy, Route: wps.Encode(), Waypoints: wps})
	}
	return routes
}

func (s STAR) Print(name string) {
	for _, r := range s.Routes(name) {
		fmt.Printf(routePrintFormat, r.Name, r.Route)
	}
}

///////////////////////////////////////////////////////////////////////////
// SID

// SID is a standard instrument departure as coded in the FAA CIFP.
type SID struct {
	// RunwayTransitions is keyed by runway. A transition that starts with
	// a leg flown from the runway--a heading to an altitude, say--begins at
	// the runway's departure end, named as the opposite runway's threshold:
	// KJFK-13R for a departure off 31L.
	RunwayTransitions map[string]WaypointArray
	// Common is the route every runway and enroute transition shares.
	Common WaypointArray
	// EnrouteTransitions is keyed by transition name, each with the common
	// route spliced onto its front.
	EnrouteTransitions map[string]WaypointArray
}

// MakeSID returns an empty SID with its maps allocated.
func MakeSID() *SID {
	return &SID{
		RunwayTransitions:  make(map[string]WaypointArray),
		EnrouteTransitions: make(map[string]WaypointArray),
	}
}

// Waypoints returns the SID as flown off the given runway by a departure
// leaving over the given exit fix. The route follows the runway transition
// and then the named enroute transition if one is given, or the one the
// exit lies on otherwise, ending at the exit; with no transition named and
// none leading to the exit it follows the common route to its end. An empty
// runway gives the SID from its common route on, for a departure that is
// vectored to it.
func (s SID) Waypoints(runway, transition, exit string) (WaypointArray, error) {
	var wps WaypointArray
	if runway != "" {
		var ok bool
		if wps, ok = s.RunwayTransitions[runway]; !ok {
			if len(s.RunwayTransitions) == 0 {
				return nil, fmt.Errorf("no runway transitions in the CIFP")
			}
			return nil, fmt.Errorf("no runway transition for runway %s. Options: %s", runway,
				strings.Join(util.SortedMapKeys(s.RunwayTransitions), ", "))
		}
	}

	body := s.Common
	if transition != "" {
		var ok bool
		if body, ok = s.EnrouteTransitions[transition]; !ok {
			return nil, fmt.Errorf("no enroute transition %s. Options: %s", transition,
				strings.Join(util.SortedMapKeys(s.EnrouteTransitions), ", "))
		}
	} else {
		hasExit := func(wps WaypointArray) bool {
			return slices.ContainsFunc(wps, func(wp Waypoint) bool { return wp.Fix == exit })
		}
		if !hasExit(wps) && !hasExit(s.Common) {
			for _, name := range util.SortedMapKeys(s.EnrouteTransitions) {
				if hasExit(s.EnrouteTransitions[name]) {
					body = s.EnrouteTransitions[name]
					break
				}
			}
		}
	}

	route := spliceSIDTransition(wps, body)
	if i := slices.IndexFunc(route, func(wp Waypoint) bool { return wp.Fix == exit }); i != -1 {
		route = route[:i+1]
	}
	return route, nil
}

// spliceSIDTransition appends tr to a copy of base. A transition starts at
// the last fix of what precedes it (ARINC 424 attachment 5, 4.10), so if its
// first fix is found in base the route continues from there, keeping the
// transition's restrictions at the junction where base has none.
func spliceSIDTransition(base, tr WaypointArray) WaypointArray {
	route := base.Clone()
	if len(tr) == 0 {
		return route
	}

	idx := slices.IndexFunc(route, func(wp Waypoint) bool { return wp.Fix == tr[0].Fix })
	if idx == -1 {
		return append(route, tr.Clone()...)
	}

	junction := &route[idx]
	if junction.AltitudeRestriction() == nil {
		if ar := tr[0].AltitudeRestriction(); ar != nil {
			junction.SetAltitudeRestriction(*ar)
		}
	}
	if junction.SpeedRestriction() == nil {
		if sr := tr[0].SpeedRestriction(); sr != nil {
			junction.SetSpeedRestriction(*sr)
		}
	}
	return append(route[:idx+1], tr[1:].Clone()...)
}

// Routes returns the SID's runway transitions, common route, and enroute
// transitions.
func (s SID) Routes(name string) []CIFPRoute {
	var routes []CIFPRoute
	for rwy, wps := range util.SortedMap(s.RunwayTransitions) {
		routes = append(routes, CIFPRoute{Name: name + ".RWY" + rwy, Route: wps.Encode(), Waypoints: wps})
	}
	if len(s.Common) > 0 {
		routes = append(routes, CIFPRoute{Name: name, Route: s.Common.Encode(), Waypoints: s.Common})
	}
	for tr, wps := range util.SortedMap(s.EnrouteTransitions) {
		routes = append(routes, CIFPRoute{Name: name + "." + tr, Route: wps.Encode(), Waypoints: wps})
	}
	return routes
}

// Print writes the SID's routes in the scenario waypoint syntax.
func (s SID) Print(name string) {
	for _, r := range s.Routes(name) {
		fmt.Printf(routePrintFormat, r.Name, r.Route)
	}
}
