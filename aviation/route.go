// aviation/route.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// WaypointArray

type WaypointArray []Waypoint

// containsFix reports whether the route passes over the named fix.
func (wa WaypointArray) containsFix(fix string) bool {
	return slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
}

// HasHumanHandoff returns true if any waypoint has HumanHandoff set.
func (wa WaypointArray) HasHumanHandoff() bool {
	return slices.ContainsFunc(wa, Waypoint.HasHumanHandoff)
}

// HandoffControllers returns the positions the route names as handoff targets
// in any of its waypoints' action groups. These are the /hoXX handoffs, which
// name where the track goes; the bare /ho of HasHumanHandoff leaves that to
// whoever is working the flow.
func (wa WaypointArray) HandoffControllers() []ControlPosition {
	var controllers []ControlPosition
	add := func(pos ControlPosition) {
		if pos != "" && !slices.Contains(controllers, pos) {
			controllers = append(controllers, pos)
		}
	}
	for _, wp := range wa {
		for _, group := range wp.ActionGroups() {
			add(group.Actions.HandoffController)
		}
	}
	return controllers
}

func (wa WaypointArray) Encode() string {
	var entries []string
	for i, w := range wa {
		var s strings.Builder
		s.WriteString(w.Fix)
		if ar := w.AltitudeRestriction(); ar != nil {
			s.WriteString("/a" + ar.Encoded())
		}
		if sr := w.SpeedRestriction(); sr != nil {
			s.WriteString("/s" + sr.Encoded())
		}
		if pt := w.ProcedureTurn(); pt != nil {
			if pt.Type == PTStandard45 {
				if !pt.RightTurns {
					s.WriteString("/lpt45")
				} else {
					s.WriteString("/pt45")
				}
			} else {
				if !pt.RightTurns {
					s.WriteString("/lhilpt")
				} else {
					s.WriteString("/hilpt")
				}
			}
			if pt.MinuteLimit != 0 {
				s.WriteString(fmt.Sprintf("%.1fmin", pt.MinuteLimit))
			} else if pt.NmLimit != 0 {
				s.WriteString(fmt.Sprintf("%.1fnm", pt.NmLimit))
			}
			if pt.Entry180NoPT {
				s.WriteString("/nopt180")
			}
			if pt.ExitAltitude != 0 {
				s.WriteString(fmt.Sprintf("/pta%d", pt.ExitAltitude))
			}
		}
		if w.IAF() {
			s.WriteString("/iaf")
		}
		if w.IF() {
			s.WriteString("/if")
		}
		if w.FAF() {
			s.WriteString("/faf")
		}
		if w.NoPT() {
			s.WriteString("/nopt")
		}
		if w.FlyOver() {
			s.WriteString("/flyover")
		}
		if arc := w.Arc(); arc != nil {
			switch arc.Direction {
			case DMEArcDirectionClockwise:
				s.WriteString("/rarc")
			case DMEArcDirectionCounterClockwise:
				s.WriteString("/larc")
			default:
				s.WriteString("/arc")
			}
			if arc.Fix != "" {
				s.WriteString(strconv.FormatFloat(float64(arc.Radius), 'f', -1, 32) + arc.Fix)
			} else {
				s.WriteString(strconv.FormatFloat(float64(arc.Length), 'f', -1, 32))
			}
		}
		for _, group := range w.ActionGroups() {
			s.WriteString(group.Encoded())
		}
		if aw := w.Airway(); aw != "" {
			s.WriteString("/airway" + aw)
		}
		if w.OnSID() {
			s.WriteString("/sid")
		}
		if w.OnSTAR() {
			s.WriteString("/star")
		}
		if w.OnApproach() {
			s.WriteString("/appr")
		}
		if w.AirworkRadius() != 0 {
			s.WriteString(fmt.Sprintf("/airwork%dnm%dm", w.AirworkRadius(), w.AirworkMinutes()))
		}
		if w.Radius() != 0 {
			s.WriteString(fmt.Sprintf("/radius%.1f", w.Radius()))
		}
		if w.Shift() != 0 {
			s.WriteString(fmt.Sprintf("/shift%.1f", w.Shift()))
		}

		// The turn direction to the next fix is given on the fix before it.
		if i+1 < len(wa) {
			switch wa[i+1].Turn() {
			case TurnLeft:
				s.WriteString("/ld")
			case TurnRight:
				s.WriteString("/rd")
			}
		}

		entries = append(entries, s.String())
	}

	return strings.Join(entries, " ")
}

func (wa *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		// Handle the string encoding used in scenario JSON files
		wp, err := parseWaypoints(string(b[1 : len(b)-1]))
		if err == nil {
			*wa = wp
		}
		return err
	} else {
		// Otherwise unmarshal it normally
		var wp []Waypoint
		err := json.Unmarshal(b, &wp)
		if err == nil {
			*wa = wp
		}
		return err
	}
}

// Clone returns a copy of the waypoints that shares nothing with the
// original, so that initializing the copy leaves the original untouched.
func (wa WaypointArray) Clone() WaypointArray {
	wps := util.DuplicateSlice(wa)
	for i := range wps {
		wps[i] = wps[i].Clone()
	}
	return wps
}

// RouteString renders the waypoints back to a route string. A run of them
// sharing an airway collapses to its two ends.
func (wa WaypointArray) RouteString() string {
	var r []string
	airway := ""
	for _, wp := range wa {
		wpAirway := wp.Airway()
		if airway != "" && wpAirway == airway {
			// This fix was automatically added for an airway so don't include it here.
			continue
		}
		r = append(r, wp.Fix)

		if wpAirway != airway {
			if wpAirway != "" {
				r = append(r, wpAirway)
			}
			airway = wpAirway
		}
	}
	return strings.Join(r, " ")
}

func (wa WaypointArray) CheckDeparture(e *util.ErrorLogger, elevation int, controllers map[ControlPosition]*Controller, checkScratchpads func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, controllers, checkScratchpads)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in departure")
		}
		for _, group := range wp.ActionGroups() {
			// @a altitudes are MSL, so one at or below the field elevation is
			// met the moment the aircraft starts rolling. Almost always a
			// missing factor of 100.
			if group.Until.Type == WaypointActionAltitude && group.Until.Altitude <= elevation {
				sign := util.Select(group.Until.AtOrAbove, "+", "-")
				e.ErrorString("/@a%d%s is at or below the %d' field elevation, so it takes effect immediately. Is it supposed to be /@a%d%s?",
					group.Until.Altitude, sign, elevation, group.Until.Altitude*100, sign)
			}
			if alt := group.Actions.ClimbAltitude; alt != 0 && alt <= elevation {
				e.ErrorString("/c%d is at or below the %d' field elevation", alt, elevation)
			}
			if alt := group.Actions.DescendAltitude; alt != 0 && alt <= elevation {
				e.ErrorString("/d%d is at or below the %d' field elevation", alt, elevation)
			}
		}
		if war := wp.AltitudeRestriction(); war != nil {
			checkAltitudeRange(e, *war)
			if war.Range[0] != 0 {
				if lastMin != 0 && war.Range[0] < lastMin {
					// our minimum must be >= the previous minimum
					e.ErrorString("Minimum altitude %s is lower than previous fix %s's minimum %s",
						FormatAltitude(war.Range[0]), minFix, FormatAltitude(lastMin))
				}
				lastMin = war.Range[0]
				minFix = wp.Fix
			}
		}

		e.Pop()
	}
}

func (wa WaypointArray) checkBasics(e *util.ErrorLogger, controllers map[ControlPosition]*Controller, checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	checkFeet := func(qualifier string, alt int) {
		if alt > 0 && float32(alt) < minCrossingAltitude {
			hundredsOfFeetError(e, fmt.Sprintf("/%s%d", qualifier, alt), fmt.Sprintf("/%s%d", qualifier, alt*100))
		}
	}

	for i, wp := range wa {
		e.Push(wp.Fix)
		if sr := wp.SpeedRestriction(); sr != nil {
			checkSpeedRange(e, *sr)
		}

		if pt := wp.ProcedureTurn(); pt != nil {
			checkFeet("pta", pt.ExitAltitude)
		}

		if wp.AirworkMinutes() > 0 {
			if ar := wp.AltitudeRestriction(); ar == nil {
				e.ErrorString(`Must provide altitude range via "/aXXX-YYY" with /airwork`)
			} else if ar.Range[0] == 0 || ar.Range[1] == MaxAltitude {
				e.ErrorString(`Must provide top and bottom in altitude range "/aXXX-YYY" with /airwork`)
			} else if ar.Range[1]-ar.Range[0] < 2000 {
				e.ErrorString("Must provide at least 2,000' of altitude range with /airwork")
			}
		}

		for _, group := range wp.ActionGroups() {
			if !wa.atEnd(i) {
				checkFeet("c", group.Actions.ClimbAltitude)
				checkFeet("d", group.Actions.DescendAltitude)
			}
			if po := group.Actions.PointOut; po != "" {
				if !util.MapContains(controllers,
					func(_ ControlPosition, ctrl *Controller) bool {
						return ctrl.PositionId() == po
					}) {
					e.ErrorString("No controller found with id %q for point out", po)
				}
			}
			if hc := group.Actions.HandoffController; hc != "" {
				if !util.MapContains(controllers,
					func(_ ControlPosition, ctrl *Controller) bool {
						return ctrl.PositionId() == ControlPosition(hc)
					}) {
					e.ErrorString("No controller found with id %q for handoff", hc)
				}
			}
			if !checkScratchpad(group.Actions.PrimaryScratchpad) {
				e.ErrorString("%s: invalid primary_scratchpad", group.Actions.PrimaryScratchpad)
			}
			if !checkScratchpad(group.Actions.SecondaryScratchpad) {
				e.ErrorString("%s: invalid secondary scratchpad", group.Actions.SecondaryScratchpad)
			}
		}

		if i == 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify /shift at the first fix in a route")
		}
		if wp.Radius() > 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify both /radius and /shift at the same fix")
		}

		e.Pop()
	}
}

func CheckApproaches(e *util.ErrorLogger, wps []WaypointArray, requireFAF bool, controllers map[ControlPosition]*Controller,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	foundFAF := false
	for _, w := range wps {
		w.checkBasics(e, controllers, checkScratchpad)
		w.checkDescending(e)

		if len(w) < 2 {
			e.ErrorString("must have at least two waypoints in an approach")
		}

		for _, wp := range w {
			if wp.FAF() {
				foundFAF = true
			}
		}
	}
	if requireFAF && !foundFAF {
		e.ErrorString("No /faf specifier found in approach")
	}
}

// minCrossingAltitude is the lowest altitude we accept in a route qualifier
// away from the runway. Route altitudes are in feet, so a lower one is
// almost always a missing factor of 100.
const minCrossingAltitude float32 = 500

// minRouteAltitude and maxRouteAltitude bound the altitudes we accept in a
// route's crossing restrictions; outside them, the value is a typo rather
// than a restriction. The floor allows for fields below sea level: the
// lowest runway threshold in the CIFP is KCLR's, at -145'.
const (
	minRouteAltitude float32 = -1000
	maxRouteAltitude float32 = 50000
)

// atEnd reports whether the i'th waypoint is where the route effectively
// ends: the aircraft is at the runway there, so low altitudes are expected.
func (wa WaypointArray) atEnd(i int) bool {
	return i+1 == len(wa) || wa[i].HasDeleteAction() || wa[i+1].HasDeleteAction()
}

func hundredsOfFeetError(e *util.ErrorLogger, given, scaled string) {
	e.ErrorString("%s is below %s, which is almost certainly not intended. Is it supposed to be %s?",
		given, FormatAltitude(minCrossingAltitude), scaled)
}

// minRouteSpeed and maxRouteSpeed bound the airspeeds we accept in scenarios,
// both initial speeds and crossing restrictions; outside them the value is a
// typo rather than an airspeed. The floor leaves room for light GA, which
// spawns off the runway as slow as 60 knots.
const (
	minRouteSpeed = 50
	maxRouteSpeed = 350
)

// minMach and maxMach bound Mach numbers, which are otherwise exempt from the
// knots range; "M8500" parses as Mach 85 and is as much a typo as the rest.
// The ceiling covers Concorde, the only supersonic type in the performance
// database, which tops out at Mach 2.04.
const (
	minMach = 0.3
	maxMach = 2.5
)

func checkSpeed(e *util.ErrorLogger, what string, speed Airspeed) {
	if speed.IsMach {
		if speed.Value < minMach || speed.Value > maxMach {
			e.ErrorString("%s %s: Mach must be between %g and %g", what, speed, minMach, maxMach)
		}
	} else if speed.Value < minRouteSpeed || speed.Value > maxRouteSpeed {
		e.ErrorString("%s %s: speeds must be between %d and %d knots", what, speed, minRouteSpeed, maxRouteSpeed)
	}
}

func checkSpeedRange(e *util.ErrorLogger, sr SpeedRestriction) {
	if sr.IsMach {
		checkSpeed(e, "speed restriction", MakeMach(sr.Range[0]))
		return
	}
	// A zero lower bound or a MaxRestrictionSpeed upper bound means unbounded
	// rather than an airspeed to check.
	unreasonable := func(s float32) bool { return s < minRouteSpeed || s > maxRouteSpeed }
	if (sr.Range[0] != 0 && unreasonable(sr.Range[0])) ||
		(sr.Range[1] != MaxRestrictionSpeed && unreasonable(sr.Range[1])) {
		e.ErrorString("invalid speed restriction %s: speeds must be between %d and %d knots",
			sr.Encoded(), minRouteSpeed, maxRouteSpeed)
	}
}

func checkAltitudeRange(e *util.ErrorLogger, ar AltitudeRestriction) {
	unreasonable := func(alt float32) bool {
		return alt < minRouteAltitude || alt >= maxRouteAltitude
	}
	if unreasonable(ar.Range[0]) || (ar.Range[1] != MaxAltitude && unreasonable(ar.Range[1])) {
		e.ErrorString("Invalid altitude restriction %q: altitudes must be between %s and FL500",
			ar.Encoded(), FormatAltitude(minRouteAltitude))
	}
}

func (wa WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[ControlPosition]*Controller, approachAssigned bool,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, ctrl, checkScratchpad)
	wa.checkDescending(e)
	haveHO := false

	for i, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		if ar := wp.AltitudeRestriction(); ar != nil && !wa.atEnd(i) {
			alt := ar.Range[0]
			if ar.Range[1] != MaxAltitude {
				alt = max(alt, ar.Range[1])
			}
			if alt > 0 && alt < minCrossingAltitude {
				scaled := *ar
				scaled.Range[0] *= 100
				if scaled.Range[1] != MaxAltitude {
					scaled.Range[1] *= 100
				}
				hundredsOfFeetError(e, "/a"+ar.Encoded(), "/a"+scaled.Encoded())
			}
		}
		for _, group := range wp.ActionGroups() {
			if group.Actions.ClearApproach && !approachAssigned {
				e.ErrorString("/clearapp specified but no approach has been assigned")
			}
			if group.Actions.InterceptApproach && !approachAssigned {
				e.ErrorString("/intercept specified but no approach has been assigned")
			}
			if group.Actions.HumanHandoff {
				haveHO = true
			}
			if group.Actions.TransferComms && !haveHO {
				e.ErrorString("Must have /ho to handoff to a human controller before /tc")
			}
		}
		e.Pop()
	}
}

// checkApproachJoins reports /clearapp and /intercept actions the aircraft
// won't be able to act on. Both join appr by looking for a fix that the route
// and the approach share, so something from the action's own fix onward has
// to be on it. An open-ended heading action is the exception: the aircraft
// keeps that heading until a controller intervenes and is vectored to the
// approach course, so it needs no shared fix.
func (wa WaypointArray) checkApproachJoins(appr *Approach, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	onApproach := func(fix string) bool {
		return slices.ContainsFunc(appr.Waypoints, func(route WaypointArray) bool {
			return slices.ContainsFunc(route, func(wp Waypoint) bool { return wp.Fix == fix })
		})
	}

	onHeading := false
	for i, wp := range wa {
		if _, kind := ActionGroupHeading(wp.ActionGroups()); kind == ActionGroupHeadingAssigned {
			onHeading = true
		}
		if onHeading {
			continue
		}

		var action string
		if slices.ContainsFunc(wp.ActionGroups(), func(g WaypointActionGroup) bool { return g.Actions.ClearApproach }) {
			action = "/clearapp"
		} else if wp.HasInterceptApproachAction() {
			action = "/intercept"
		} else {
			continue
		}

		if slices.ContainsFunc(wa[i:], func(wp Waypoint) bool { return onApproach(wp.Fix) }) {
			continue
		}

		e.Push(wp.Fix)
		e.ErrorString("%s can't join the %s: neither this fix nor any after it is on it",
			action, appr.FullName)
		e.Pop()
	}
}

func (wa WaypointArray) CheckOverflight(e *util.ErrorLogger, ctrl map[ControlPosition]*Controller, checkScratchpads func(string) bool) {
	wa.checkBasics(e, ctrl, checkScratchpads)
	wa.checkProcedureActions(e)
}

// checkProcedureActions reports /cvs and /dvs actions the aircraft won't be
// able to act on. The fix is behind the aircraft by the time its actions
// run, so the procedure to climb or descend via has to continue with the
// next fix. It must be called after the waypoints' OnSID and OnSTAR flags
// have been set.
func (wa WaypointArray) checkProcedureActions(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	for i, wp := range wa {
		nextOn := func(on func(Waypoint) bool) bool { return i+1 < len(wa) && on(wa[i+1]) }

		e.Push(wp.Fix)
		for _, group := range wp.ActionGroups() {
			if group.Actions.ClimbViaSID && !nextOn(Waypoint.OnSID) {
				e.ErrorString("/cvs: the next fix is not on a SID, so there is no SID to climb via")
			}
			if group.Actions.DescendViaSTAR && !nextOn(Waypoint.OnSTAR) {
				e.ErrorString("/dvs: the next fix is not on a STAR, so there is no STAR to descend via")
			}
		}
		e.Pop()
	}
}

func (wa WaypointArray) checkDescending(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	// or at least, check not climbing...
	var lastMin float32
	var minFix string // last fix that established a specific minimum alt

	for _, wp := range wa {
		e.Push(wp.Fix)

		if war := wp.AltitudeRestriction(); war != nil {
			if war.Range[0] > war.Range[1] {
				e.ErrorString("Minimum altitude %s is higher than maximum %s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			checkAltitudeRange(e, *war)

			if war.Range[0] != 0 {
				if minFix != "" && war.Range[0] > lastMin {
					e.ErrorString("Minimum altitude %s is higher than previous fix %s's minimum %s",
						FormatAltitude(war.Range[0]), minFix, FormatAltitude(lastMin))
				}
				minFix = wp.Fix
				lastMin = war.Range[0]
			}
		}

		e.Pop()
	}

}
