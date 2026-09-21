// aviation/routeparse.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

func RandomizeRoute(w []Waypoint, r *rand.Rand, randomizeAltitudeRange bool, perf AircraftPerformance, nmPerLongitude float32,
	magneticVariation float32, airport ICAOAirportCode, lg *log.Logger) {
	// Random values used for altitude and position randomization
	rtheta, rrad := r.Float32(), r.Float32()
	ralt := r.Float32()

	// We use this to some random variation to the random sample after each
	// use. In this way, there's some correlation between adjacent
	// waypoints: if they're relatively high at one, they'll tend to be
	// relatively high at the next one, though the random choices still
	// vary a bit.
	jitter := func(v float32) float32 {
		v += r.Float32Range(-0.1, 0.1)
		if v < 0 {
			v = -v
		} else if v > 1 {
			v = 1 - (v - 1)
		}
		return v
	}

	for i := range w {
		wp := &w[i]
		if rad := wp.Radius(); rad > 0 {
			// Work in nm coordinates
			p := math.LL2NM(wp.Location, nmPerLongitude)

			// radius and theta
			r := math.Sqrt(rrad) * rad // equi-area mapping
			const Pi = 3.1415926535
			t := 2 * Pi * rtheta

			pp := math.Add2f(p, math.Scale2f(math.SinCos(t), r))
			wp.Location = math.NM2LL(pp, nmPerLongitude)
			wp.InitExtra().Radius = 0 // clean up

			rtheta = jitter(rtheta)
			rrad = jitter(rrad)
		} else if sh := wp.Shift(); sh > 0 {
			p0, p1 := math.LL2NM(w[i-1].Location, nmPerLongitude), math.LL2NM(w[i].Location, nmPerLongitude)
			v := math.Normalize2f(math.Sub2f(p1, p0))
			t := math.Lerp(rrad, -sh, sh)
			p := math.Add2f(p1, math.Scale2f(v, t))
			wp.Location = math.NM2LL(p, nmPerLongitude)

			wp.InitExtra().Shift = 0 // clean up

			rrad = jitter(rrad)
		}

		if randomizeAltitudeRange {
			if ar := wp.AltitudeRestriction(); ar != nil {
				low, high := ar.Range[0], ar.Range[1]
				// We should clamp low to be a few hundred feet AGL, but
				// hopefully we'll generally be given a full range.
				if high == MaxAltitude {
					high = low + 3000
				}
				// Cap at VFR max (17,500') since randomizeAltitudeRange is only true for VFR.
				// This prevents VFR aircraft from being assigned altitudes in Class A airspace.
				const maxVFRAltitude = 17500
				high = min(high, maxVFRAltitude)
				low = min(low, maxVFRAltitude)
				alt := math.Lerp(ralt, low, high)

				// Update the altitude restriction to just be the single altitude.
				wp.SetAltitudeRestriction(MakeAtAltitudeRestriction(alt))

				ralt = jitter(ralt)
			}
		}
	}
}

func parsePTExtent(pt *ProcedureTurn, extent string) error {
	if len(extent) == 0 {
		// Unspecified; we will use the default of 1min for ILS, 4nm for RNAV
		return nil
	}
	if len(extent) < 3 {
		return fmt.Errorf("%s: invalid extent specification for procedure turn", extent)
	}

	var err error
	var limit float64
	if extent[len(extent)-2:] == "nm" {
		if limit, err = strconv.ParseFloat(extent[:len(extent)-2], 32); err != nil {
			return fmt.Errorf("%s: unable to parse length in nm for procedure turn: %v", extent, err)
		}
		pt.NmLimit = float32(limit)
	} else if extent[len(extent)-3:] == "min" {
		if limit, err = strconv.ParseFloat(extent[:len(extent)-3], 32); err != nil {
			return fmt.Errorf("%s: unable to parse minutes in procedure turn: %v", extent, err)
		}
		pt.MinuteLimit = float32(limit)
	} else {
		return fmt.Errorf("%s: invalid extent units for procedure turn", extent)
	}

	return nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// parseCourse parses a magnetic heading or course. North is 360 rather than
// 0, since Waypoint.Heading uses 0 to mean "unset".
func parseCourse(s string) (int16, error) {
	hdg, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid heading: %v", s, err)
	}
	if hdg < 1 || hdg > 360 {
		return 0, fmt.Errorf("%s: heading must be between 1-360 (use 360 for north)", s)
	}
	return int16(hdg), nil
}

func parseWaypointHeadingAction(f string) (WaypointHeadingAction, bool, error) {
	if f == "ph" {
		return WaypointHeadingAction{PresentHeading: true}, true, nil
	}

	var headingAction WaypointHeadingAction
	var hdg string
	switch {
	case len(f) >= 3 && f[:2] == "lt":
		headingAction.Turn = TurnLeft
		headingAction.Track = true
		hdg = f[2:]
	case len(f) >= 3 && f[:2] == "rt":
		headingAction.Turn = TurnRight
		headingAction.Track = true
		hdg = f[2:]
	case len(f) >= 2 && f[0] == 't':
		headingAction.Track = true
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'h':
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'l':
		headingAction.Turn = TurnLeft
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'r':
		headingAction.Turn = TurnRight
		hdg = f[1:]
	default:
		return WaypointHeadingAction{}, false, nil
	}

	// A navaid's radial in place of the heading, /tPXR-R336, is tracked
	// away from the navaid.
	if !allDigits(hdg) {
		if !strings.Contains(hdg, "-R") {
			return WaypointHeadingAction{}, false, nil
		}
		if !headingAction.Track {
			return WaypointHeadingAction{}, true, fmt.Errorf("%s: a radial can only be tracked (/t, /lt, /rt), not flown as a heading", f)
		}
		fix, radial, err := parseRadial(hdg)
		if err != nil {
			return WaypointHeadingAction{}, true, err
		}
		headingAction.Fix, headingAction.Heading = fix, radial
		return headingAction, true, nil
	}

	heading, err := parseCourse(hdg)
	if err != nil {
		return WaypointHeadingAction{}, true, err
	}
	headingAction.Heading = heading
	return headingAction, true, nil
}

func parseWaypointActionModifier(f string) (WaypointActions, bool, error) {
	switch {
	case f == "ho":
		return WaypointActions{HumanHandoff: true}, true, nil
	case strings.HasPrefix(f, "ho"):
		return WaypointActions{HandoffController: ControlPosition(f[2:])}, true, nil
	case len(f) > 2 && f[:2] == "po":
		return WaypointActions{PointOut: ControlPosition(f[2:])}, true, nil
	case f == "clearapp":
		return WaypointActions{ClearApproach: true}, true, nil
	case f == "intercept":
		return WaypointActions{InterceptApproach: true}, true, nil
	case f == "delete":
		return WaypointActions{Delete: true}, true, nil
	case f == "land":
		return WaypointActions{Land: true}, true, nil
	case strings.HasPrefix(f, "spsp"):
		return WaypointActions{PrimaryScratchpad: f[4:]}, true, nil
	case f == "cpsp":
		return WaypointActions{ClearPrimaryScratchpad: true}, true, nil
	case strings.HasPrefix(f, "sssp"):
		return WaypointActions{SecondaryScratchpad: f[4:]}, true, nil
	case f == "cssp":
		return WaypointActions{ClearSecondaryScratchpad: true}, true, nil
	case f == "tc":
		return WaypointActions{TransferComms: true}, true, nil
	case len(f) > 1 && (f[0] == 'c' || f[0] == 'd') && allDigits(f[1:]):
		alt, err := strconv.Atoi(f[1:])
		if err != nil || alt < 100 || alt > 60000 || alt%100 != 0 {
			return WaypointActions{}, true, fmt.Errorf("%s: altitude must be a multiple of 100 between 100 and 60000 feet", f)
		}
		if f[0] == 'c' {
			return WaypointActions{ClimbAltitude: alt}, true, nil
		}
		return WaypointActions{DescendAltitude: alt}, true, nil
	}

	if heading, ok, err := parseWaypointHeadingAction(f); ok || err != nil {
		return WaypointActions{Heading: heading}, ok, err
	}

	return WaypointActions{}, false, nil
}

// merge combines src into wa
func (wa *WaypointActions) merge(src WaypointActions) {
	if src.Heading.IsSet() {
		wa.Heading = src.Heading
	}
	if src.ClimbAltitude != 0 {
		wa.ClimbAltitude, wa.DescendAltitude = src.ClimbAltitude, 0
	}
	if src.DescendAltitude != 0 {
		wa.DescendAltitude, wa.ClimbAltitude = src.DescendAltitude, 0
	}
	wa.HumanHandoff = wa.HumanHandoff || src.HumanHandoff
	if src.HandoffController != "" {
		wa.HandoffController = src.HandoffController
	}
	if src.PointOut != "" {
		wa.PointOut = src.PointOut
	}
	wa.ClearApproach = wa.ClearApproach || src.ClearApproach
	wa.InterceptApproach = wa.InterceptApproach || src.InterceptApproach
	wa.Delete = wa.Delete || src.Delete
	wa.Land = wa.Land || src.Land
	if src.GoAroundContactController != "" {
		wa.GoAroundContactController = src.GoAroundContactController
	}
	if src.PrimaryScratchpad != "" {
		wa.PrimaryScratchpad = src.PrimaryScratchpad
	}
	wa.ClearPrimaryScratchpad = wa.ClearPrimaryScratchpad || src.ClearPrimaryScratchpad
	if src.SecondaryScratchpad != "" {
		wa.SecondaryScratchpad = src.SecondaryScratchpad
	}
	wa.ClearSecondaryScratchpad = wa.ClearSecondaryScratchpad || src.ClearSecondaryScratchpad
	wa.TransferComms = wa.TransferComms || src.TransferComms
}

// mergeWaypointActions combines src into dst, rejecting a second value for
// anything an action group may only give once.
func mergeWaypointActions(dst *WaypointActions, src WaypointActions) error {
	if src.Heading.IsSet() && dst.Heading.IsSet() {
		return fmt.Errorf("multiple heading actions in the same waypoint action group")
	}
	if src.ClimbAltitude != 0 && dst.ClimbAltitude != 0 {
		return fmt.Errorf("multiple climb altitude actions in the same waypoint action group")
	}
	if src.DescendAltitude != 0 && dst.DescendAltitude != 0 {
		return fmt.Errorf("multiple descend altitude actions in the same waypoint action group")
	}
	if (src.ClimbAltitude != 0 && dst.DescendAltitude != 0) ||
		(src.DescendAltitude != 0 && dst.ClimbAltitude != 0) {
		return fmt.Errorf("cannot specify both /c and /d in the same waypoint action group")
	}
	dst.merge(src)
	return nil
}

// parseWaypointActionTermination parses the condition of a trigger, after
// its @: an altitude (a4277+ at or above, a4277- at or below), a distance
// flown (d7.9), a course to the next fix (crs220, or crsHLN-R322 when the
// leg runs along a navaid's radial), a navaid's radial (HLN-R322), or a DME
// distance from a navaid (ILSQ-D2.3+ at or beyond, ILSQ-D2.3- within).
func parseWaypointActionTermination(f string) (WaypointActionTermination, error) {
	// cutSign splits off the trailing + or - that says which side of the
	// value the trigger is met on.
	cutSign := func(s string) (value string, atOrAbove, ok bool) {
		if value, ok = strings.CutSuffix(s, "+"); ok {
			return value, true, true
		}
		value, ok = strings.CutSuffix(s, "-")
		return value, false, ok
	}

	switch {
	case len(f) > 1 && f[0] == 'a':
		alt, atOrAbove, ok := cutSign(f[1:])
		if !ok || !allDigits(alt) {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected an altitude followed by + (at or above) or - (at or below)", f)
		}
		a, err := strconv.Atoi(alt)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid altitude: %w", f, err)
		}
		if a > 60000 {
			return WaypointActionTermination{}, fmt.Errorf("%s: trigger altitude must be between 0 and 60000 feet", f)
		}
		return WaypointActionTermination{Type: WaypointActionAltitude, Altitude: a, AtOrAbove: atOrAbove}, nil

	case len(f) > 1 && f[0] == 'd':
		d, err := strconv.ParseFloat(f[1:], 32)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid distance %q", f, f[1:])
		}
		if d <= 0 {
			return WaypointActionTermination{}, fmt.Errorf("%s: distance must be positive", f)
		}
		return WaypointActionTermination{Type: WaypointActionDistance, Distance: float32(d)}, nil

	case strings.HasPrefix(f, "crs"):
		if strings.Contains(f[3:], "-R") {
			// A leg along a navaid's radial gives that radial, which is
			// referenced to the station's declination rather than the
			// area's variation, in place of the course.
			fix, radial, err := parseRadial(f[3:])
			if err != nil {
				return WaypointActionTermination{}, fmt.Errorf("%s: %w", f, err)
			}
			return WaypointActionTermination{Type: WaypointActionCourse, Course: radial, CourseFix: fix}, nil
		}
		if !allDigits(f[3:]) || len(f) == 3 {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a course after crs", f)
		}
		course, err := parseCourse(f[3:])
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: %w", f, err)
		}
		return WaypointActionTermination{Type: WaypointActionCourse, Course: course}, nil

	case strings.Contains(f, "-R"):
		fix, radial, err := parseRadial(f)
		if err != nil {
			return WaypointActionTermination{}, err
		}
		return WaypointActionTermination{Type: WaypointActionRadial, Radial: radial, RadialFix: fix}, nil

	case strings.Contains(f, "-D"):
		i := strings.LastIndex(f, "-D")
		fix, spec := f[:i], f[i+2:]
		if fix == "" {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a navaid before -D", f)
		}
		dist, atOrAbove, ok := cutSign(spec)
		if !ok {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a DME distance followed by + (at or beyond) or - (within)", f)
		}
		d, err := strconv.ParseFloat(dist, 32)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid DME distance %q", f, dist)
		}
		if d <= 0 {
			return WaypointActionTermination{}, fmt.Errorf("%s: DME distance must be positive", f)
		}
		return WaypointActionTermination{Type: WaypointActionDME, DMEDistance: float32(d), DMEFix: fix, AtOrAbove: atOrAbove}, nil

	default:
		return WaypointActionTermination{}, fmt.Errorf("%s: unknown trigger; expected an altitude (a4277+), "+
			"a distance flown (d7.9), a course (crs220 or crsHLN-R322), a radial (HLN-R322), "+
			"or a DME distance (ILSQ-D2.3+)", f)
	}
}

// parseRadial parses a navaid's radial written as a chart does, HLN-R322.
func parseRadial(s string) (fix string, radial int16, err error) {
	i := strings.LastIndex(s, "-R")
	if i <= 0 {
		return "", 0, fmt.Errorf("%s: expected a radial as NAVAID-R<radial>", s)
	}
	fix, digits := s[:i], s[i+2:]
	if digits == "" || !allDigits(digits) {
		return "", 0, fmt.Errorf("%s: expected a radial after %s-R", s, fix)
	}
	radial, err = parseCourse(digits)
	if err != nil {
		return "", 0, err
	}
	return fix, radial, nil
}

// parseWaypointActionKey parses a "waypoint_actions" key: a fix, optionally
// followed by an offset along the leg after it ("CKING@.75"). The offset is 0
// if the key gives none.
func parseWaypointActionKey(key string) (string, float32, error) {
	if strings.Contains(key, "/") {
		return "", 0, fmt.Errorf("the key gives a fix, optionally with an offset; triggers go in the value")
	}
	fix, offsetString, haveOffset := strings.Cut(key, "@")
	if fix == "" {
		return "", 0, fmt.Errorf("no fix given")
	}
	var offset float32
	if haveOffset {
		t, err := strconv.ParseFloat(offsetString, 32)
		if err != nil {
			return "", 0, fmt.Errorf("invalid offset %q: %w", offsetString, err)
		}
		// Written so that a NaN, which ParseFloat returns without an error, is
		// rejected along with the out-of-range values.
		if !(t > 0 && t < 1) {
			return "", 0, fmt.Errorf("offset %q: must be greater than 0 and less than 1", offsetString)
		}
		offset = float32(t)
	}
	return fix, offset, nil
}

// parseWaypointActionValue parses a "waypoint_actions" value--a fix's options
// in route syntax, without their leading slash ("h284/hoC35/@a513+")--into a
// waypoint carrying what it gives: action groups, altitude and speed
// restrictions, and /flyover. A /ld or /rd comes back separately: it gives
// the turn made at the fix onto the leg that follows, so its flag belongs on
// the route's next fix. The other options a waypoint can carry describe
// route structure or approach coding that a route taken from the CIFP owns,
// so they are rejected.
func parseWaypointActionValue(value string) (Waypoint, TurnDirection, error) {
	if value == "" {
		return Waypoint{}, TurnClosest, fmt.Errorf("empty value")
	}
	if strings.Contains(value, ",") {
		return Waypoint{}, TurnClosest, fmt.Errorf("%s: options are separated by slashes, not commas", value)
	}
	var wp Waypoint
	turn, err := parseWaypointModifiers(&wp, value, strings.Split(value, "/"))
	if err != nil {
		return Waypoint{}, TurnClosest, err
	}
	if wp.NoPT() || wp.IAF() || wp.IF() || wp.FAF() ||
		wp.OnSID() || wp.OnSTAR() || wp.OnApproach() || wp.ProcedureTurn() != nil ||
		wp.Arc() != nil || wp.Airway() != "" || wp.AirworkRadius() != 0 ||
		wp.Radius() != 0 || wp.Shift() != 0 {
		return Waypoint{}, TurnClosest, fmt.Errorf("%s: only actions, triggers, /a and /s restrictions, /flyover, and /ld or /rd may be given", value)
	}
	return wp, turn, nil
}

// applyActions applies a "waypoint_actions" value to the fix. The value's
// action groups replace the ones charted at the fix--a value with no actions
// or triggers leaves the charted groups as they are--its altitude or speed
// restriction replaces the charted restriction of its kind, /flyover marks
// the fix as flown over, and /ld or /rd gives the turn made at the fix onto
// the leg that follows. An offset puts the value's actions on a point
// synthesized partway along the leg after the fix instead, which the
// returned route carries; its location waits for InitializeLocations.
func (wa WaypointArray) applyActions(fix string, offset float32, value string) (WaypointArray, error) {
	i := slices.IndexFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
	if i == -1 {
		return wa, fmt.Errorf("%s: not in the route %s", fix, wa.RouteString())
	}
	o, turn, err := parseWaypointActionValue(value)
	if err != nil {
		return wa, err
	}

	if offset != 0 {
		if o.HasAltitudeRestriction() || o.HasSpeedRestriction() {
			return wa, fmt.Errorf(`%s: restrictions may not be specified at an "@" offset fix`, value)
		}
		if o.FlyOver() {
			return wa, fmt.Errorf(`/flyover may not be specified at an "@" offset fix`)
		}
		if turn != TurnClosest {
			return wa, fmt.Errorf(`%s: /ld and /rd may not be specified at an "@" offset fix`, value)
		}
		return wa.insertOffsetActions(i, offset, o.ActionGroups())
	}

	groups := o.ActionGroups()
	if n := len(groups); n > 0 && groups[n-1].Until.Type == WaypointActionCourse && i == len(wa)-1 {
		return wa, fmt.Errorf("%s: /@crs has no following fix to give a course to; %s ends the route", value, fix)
	}
	if turn != TurnClosest {
		next := wa.nextChartedFix(i)
		if next == len(wa) {
			return wa, fmt.Errorf("%s: /ld or /rd has no following fix to turn to; %s ends the route", value, fix)
		}
		// The turn direction to the next fix is carried on that fix.
		wa[next].SetTurn(turn)
	}
	wp := &wa[i]
	if len(groups) > 0 {
		wp.InitExtra().ActionGroups = groups
	}
	if ar := o.AltitudeRestriction(); ar != nil {
		wp.SetAltitudeRestriction(*ar)
	}
	if sr := o.SpeedRestriction(); sr != nil {
		wp.SetSpeedRestriction(*sr)
	}
	if o.FlyOver() {
		wp.SetFlyOver(true)
	}
	return wa, nil
}

// formatOffset renders a fraction of the way along a leg the shortest way that
// reads back as the same number.
func formatOffset(t float32) string {
	return strconv.FormatFloat(float64(t), 'g', -1, 32)
}

// nextChartedFix returns the index of the first waypoint after i that is a fix
// of the route rather than a point synthesized along the leg, or len(wa) if
// the leg after i runs off the end of the route.
func (wa WaypointArray) nextChartedFix(i int) int {
	for j := i + 1; j < len(wa); j++ {
		if !wa[j].AlongLeg() {
			return j
		}
	}
	return len(wa)
}

// hasOffsetLeg reports whether the fix is in the route with another fix after
// it, so that a point partway along the leg it starts can be measured.
func (wa WaypointArray) hasOffsetLeg(fix string) bool {
	i := slices.IndexFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
	return i != -1 && wa.nextChartedFix(i) != len(wa)
}

// insertAlongLeg inserts a waypoint at the given fraction of the way along the
// leg after the fix at index i, keeping the points on the leg in the order
// they are flown. Its location is left for InitializeLocations, which takes it
// from the fixes on either side.
func (wa WaypointArray) insertAlongLeg(i int, offset float32, wp Waypoint) WaypointArray {
	wp.InitExtra().LegOffset = offset

	j := i + 1
	for j < len(wa) && wa[j].AlongLeg() && wa[j].LegOffset() < offset {
		j++
	}
	return slices.Insert(wa, j, wp)
}

// insertOffsetActions inserts a waypoint carrying the action groups at the
// point offset of the way from the fix at index i to the next charted fix
// after it, named for the leg it sits on ("_CKING-DRIFT@0.75"). The name has
// the leg's far end in it because one "waypoint_actions" key may reach
// several routes that diverge at the fix: two points are then named the same
// only when they are in the same place.
func (wa WaypointArray) insertOffsetActions(i int, offset float32, groups []WaypointActionGroup) (WaypointArray, error) {
	fix := wa[i].Fix
	if wa[i].Arc() != nil {
		return wa, fmt.Errorf("%s: the leg after it is a DME arc, which a point between the two fixes isn't on", fix)
	}
	if aw := wa[i].Airway(); aw != "" {
		return wa, fmt.Errorf("%s: %s follows it, so the fix the offset would be measured to isn't the next one "+
			"in the route", fix, aw)
	}
	next := wa.nextChartedFix(i)
	if next == len(wa) {
		return wa, fmt.Errorf("%s: ends the route %s, so there is no following fix to measure the offset to",
			fix, wa.RouteString())
	}
	// The turn is made toward the first waypoint on the leg, so a point
	// partway along it would leave the turn's direction behind at the far
	// fix, never to be consulted.
	if wa[next].Turn() != TurnClosest {
		return wa, fmt.Errorf("%s: the leg to %s carries a turn direction, which a point partway along it would defeat",
			fix, wa[next].Fix)
	}
	if slices.ContainsFunc(wa[i+1:next], func(w Waypoint) bool { return w.LegOffset() == offset }) {
		return wa, fmt.Errorf("%s: another key already puts a point there", fix)
	}

	// No flags to carry over: the routes this runs on are marked as being on a
	// SID or STAR later, over all of their waypoints at once.
	return wa.insertAlongLeg(i, offset, Waypoint{
		Fix:   fmt.Sprintf("_%s-%s@%s", fix, wa[next].Fix, formatOffset(offset)),
		Extra: &WaypointExtra{ActionGroups: groups},
	}), nil
}

// ResolveActionControllers returns a "waypoint_actions" value with the
// control positions of its handoffs and point outs passed through resolve.
// Parts that aren't such actions--triggers, properties, anything that doesn't
// parse--are left as they are for validation to report.
func ResolveActionControllers(actions string, resolve func(ControlPosition) ControlPosition) string {
	parts := strings.Split(actions, "/")
	for i, a := range parts {
		acts, ok, err := parseWaypointActionModifier(a)
		if !ok || err != nil {
			continue
		}
		if acts.HandoffController != "" {
			acts.HandoffController = resolve(acts.HandoffController)
		}
		if acts.PointOut != "" {
			acts.PointOut = resolve(acts.PointOut)
		}
		parts[i] = strings.TrimPrefix(acts.Encoded(), "/")
	}
	return strings.Join(parts, "/")
}

// parseWaypointModifiers applies a waypoint's /-separated options--actions,
// triggers, and properties--to wp; field names the full option string in
// error messages. It returns the turn direction that a /ld or /rd gives,
// which applies at the route's next waypoint.
func parseWaypointModifiers(wp *Waypoint, field string, mods []string) (TurnDirection, error) {
	nextWaypointTurn := TurnClosest
	for _, f := range mods {
		if len(f) == 0 {
			return TurnClosest, fmt.Errorf("no command found after / in %q", field)
		}

		// A trigger ends the group of the actions before it; the
		// actions after it start when it is met.
		if cond, isTrigger := strings.CutPrefix(f, "@"); isTrigger {
			groups := wp.ActionGroups()
			if n := len(groups); n == 0 || groups[n-1].Until.Type != WaypointActionNoTermination {
				return TurnClosest, fmt.Errorf("%s: trigger /%s must follow an action; use /ph to fly present heading",
					field, f)
			}
			until, err := parseWaypointActionTermination(cond)
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid trigger /%s: %w", field, f, err)
			}
			groups[len(groups)-1].Until = until
			continue
		}

		actions, ok, err := parseWaypointActionModifier(f)
		if err != nil {
			return TurnClosest, fmt.Errorf("%s: invalid waypoint action /%s: %w", field, f, err)
		}
		if ok {
			if err := wp.MergeActions(actions); err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid waypoint action /%s: %w", field, f, err)
			}
			continue
		}

		if f == "flyover" {
			wp.SetFlyOver(true)
		} else if f == "iaf" {
			wp.SetIAF(true)
		} else if f == "if" {
			wp.SetIF(true)
		} else if f == "faf" {
			wp.SetFAF(true)
		} else if f == "sid" {
			wp.SetOnSID(true)
		} else if f == "star" {
			wp.SetOnSTAR(true)
		} else if f == "appr" {
			wp.SetOnApproach(true)
		} else if strings.HasPrefix(f, "airwork") {
			a := f[7:]
			radius, minutes := 7, 15
			i := 0
			for len(a) > 0 {
				if a[i] >= '0' && a[i] <= '9' {
					i++
				} else if n, err := strconv.Atoi(a[:i]); err != nil {
					return TurnClosest, fmt.Errorf("%v: parsing %q", f, a[:i])
				} else if a[i] == 'm' {
					minutes = n
					a = a[i+1:]
					i = 0
				} else if a[i] == 'n' && len(a) > i+1 && a[i+1] == 'm' {
					radius = n
					a = a[i+2:]
					i = 0
				} else {
					return TurnClosest, fmt.Errorf("unexpected suffix %q after %q in %q", a[i:], a[:i], f)
				}
			}
			if i > 0 {
				return TurnClosest, fmt.Errorf("unexpected numbers %q after %q", a, f)
			}
			e := wp.InitExtra()
			e.AirworkRadius = int8(radius)
			e.AirworkMinutes = int8(minutes)
		} else if strings.HasPrefix(f, "radius") {
			rstr := f[6:]
			if rad, err := strconv.ParseFloat(rstr, 32); err != nil {
				return TurnClosest, err
			} else {
				wp.InitExtra().Radius = float32(rad)
			}
		} else if strings.HasPrefix(f, "shift") {
			sstr := f[5:]
			if shift, err := strconv.ParseFloat(sstr, 32); err != nil {
				return TurnClosest, err
			} else {
				wp.InitExtra().Shift = float32(shift)
			}
		} else if (len(f) >= 4 && f[:4] == "pt45") || (len(f) >= 5 && f[:5] == "lpt45") {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Type = PTStandard45
			pt.ProcedureTurn.RightTurns = f[0] == 'p'
			wp.SetFlyOver(true)

			extent := f[4:]
			if !pt.ProcedureTurn.RightTurns {
				extent = extent[1:]
			}
			if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
				return TurnClosest, err
			}
		} else if (len(f) >= 5 && f[:5] == "hilpt") || (len(f) >= 6 && f[:6] == "lhilpt") {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Type = PTRacetrack
			pt.ProcedureTurn.RightTurns = f[0] == 'h'
			wp.SetFlyOver(true)

			extent := f[5:]
			if !pt.ProcedureTurn.RightTurns {
				extent = extent[1:]
			}
			if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
				return TurnClosest, err
			}
		} else if len(f) >= 4 && f[:3] == "pta" {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}

			alt, err := strconv.Atoi(f[3:])
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: error parsing procedure turn exit altitude: %v", f[3:], err)
			}
			if alt < 0 || alt > 60000 {
				return TurnClosest, fmt.Errorf("%s: procedure turn exit altitude must be between 0 and 60000 feet", f)
			}
			pt.ProcedureTurn.ExitAltitude = alt
		} else if f == "nopt" {
			wp.SetNoPT(true)
		} else if f == "nopt180" {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Entry180NoPT = true
		} else if len(f) >= 4 && (f[:3] == "arc" || f[:4] == "larc" || f[:4] == "rarc") {
			// The direction is inferred from the surrounding fixes
			// unless given: /larc turns left (counterclockwise),
			// /rarc right.
			direction := DMEArcDirectionUnset
			spec := f[3:]
			if f[0] == 'l' {
				direction, spec = DMEArcDirectionCounterClockwise, f[4:]
			} else if f[0] == 'r' {
				direction, spec = DMEArcDirectionClockwise, f[4:]
			}
			rend := 0
			for rend < len(spec) &&
				((spec[rend] >= '0' && spec[rend] <= '9') || spec[rend] == '.') {
				rend++
			}
			if rend == 0 {
				return TurnClosest, fmt.Errorf("%s: radius not found after /arc", f)
			}

			v, err := strconv.ParseFloat(spec[:rend], 32)
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid arc radius/length: %w", f, err)
			}

			if rend == len(spec) {
				// no fix given, so interpret it as an arc length
				wp.InitExtra().Arc = &DMEArc{
					Length:    float32(v),
					Direction: direction,
				}
			} else {
				wp.InitExtra().Arc = &DMEArc{
					Fix:       spec[rend:],
					Radius:    float32(v),
					Direction: direction,
				}
			}
		} else if len(f) >= 7 && f[:6] == "airway" {
			wp.InitExtra().Airway = f[6:]

			// Do these last since they only match the first character...
		} else if f[0] == 'a' {
			if wp.HasAltitudeRestriction() {
				return TurnClosest, fmt.Errorf("%s: multiple altitude restrictions given; use a range (e.g. /a8000-10000) instead",
					field)
			}
			ar, err := ParseAltitudeRestriction(f[1:])
			if err != nil {
				return TurnClosest, err
			}
			wp.SetAltitudeRestriction(*ar)
		} else if f[0] == 's' {
			if wp.HasSpeedRestriction() {
				return TurnClosest, fmt.Errorf("%s: multiple speed restrictions given; use a range (e.g. /s180-210) instead",
					field)
			}
			sr, err := ParseSpeedRestriction(f[1:])
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: error parsing speed restriction: %v", f[1:], err)
			}
			wp.SetSpeedRestriction(*sr)
		} else if f == "ld" {
			nextWaypointTurn = TurnLeft
		} else if f == "rd" {
			nextWaypointTurn = TurnRight
		} else {
			return TurnClosest, fmt.Errorf("%s: unknown fix modifier: %s", field, f)
		}
	}

	if pt := wp.ProcedureTurn(); pt != nil && pt.Type == PTUndefined {
		return TurnClosest, fmt.Errorf("%s: no procedure turn specified for fix (e.g., pt45/hilpt) even though PT parameters were given", wp.Fix)
	}

	// /@crs ends with the aircraft going direct to the next fix, so a
	// following action group would immediately preempt it.
	if groups := wp.ActionGroups(); len(groups) > 1 &&
		slices.ContainsFunc(groups[:len(groups)-1], func(g WaypointActionGroup) bool {
			return g.Until.Type == WaypointActionCourse
		}) {
		return TurnClosest, fmt.Errorf("%s: /@crs must be the last trigger at a fix", wp.Fix)
	}

	return nextWaypointTurn, nil
}

func parseWaypoints(str string) (WaypointArray, error) {
	var waypoints WaypointArray
	var nextWaypointTurn TurnDirection
	entries := strings.Fields(str)
	for _, field := range entries {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: %q", str)
		}

		components := strings.Split(field, "/")

		// Is it a lat-long specifier like 4900N/05000W? We need to patch
		// things up if so since we use '/' to delimit our own specifiers
		// after fixes.
		if len(components) >= 2 {
			c0, c1 := components[0], components[1]
			allNumbers := func(s string) bool {
				for _, ch := range s {
					if ch < '0' || ch > '9' {
						return false
					}
				}
				return true
			}
			if len(c0) == 5 && (c0[4] == 'N' || c0[4] == 'S') &&
				len(c1) == 6 && (c1[5] == 'E' || c1[5] == 'W') &&
				allNumbers(c0[:4]) && allNumbers(c1[:5]) {
				// Reconstitute the fix in the first element of components and
				// shift the rest (if any) down.
				components[0] += "/" + c1
				components = append(components[:1], components[2:]...)
			}
		}

		wp := Waypoint{Fix: components[0]}
		if nextWaypointTurn != TurnClosest {
			wp.SetTurn(nextWaypointTurn)
		}
		turn, err := parseWaypointModifiers(&wp, field, components[1:])
		if err != nil {
			return nil, err
		}
		nextWaypointTurn = turn

		waypoints = append(waypoints, wp)
	}

	if nextWaypointTurn != TurnClosest {
		return nil, fmt.Errorf("/ld or /rd on the last waypoint has no next waypoint to apply to")
	}

	if n := len(waypoints); n > 0 {
		if groups := waypoints[n-1].ActionGroups(); len(groups) > 0 &&
			groups[len(groups)-1].Until.Type == WaypointActionCourse {
			return nil, fmt.Errorf("%s: /@crs on the last waypoint has no following fix to give a course to",
				waypoints[n-1].Fix)
		}
	}

	return waypoints, nil
}

// ParseAltitudeRestriction parses an altitude restriction in the compact
// text format used in scenario definition files.
func ParseAltitudeRestriction(s string) (*AltitudeRestriction, error) {
	n := len(s)
	if n == 0 {
		return nil, fmt.Errorf("%s: no altitude provided for crossing restriction", s)
	}

	if s[n-1] == '-' {
		// At or below
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		ar := MakeAtOrBelowAltitudeRestriction(float32(alt))
		return &ar, nil
	} else if s[n-1] == '+' {
		// At or above
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		ar := MakeAtOrAboveAltitudeRestriction(float32(alt))
		return &ar, nil
	} else if alts := strings.Split(s, "-"); len(alts) == 2 {
		// Between
		if low, err := strconv.Atoi(alts[0]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if high, err := strconv.Atoi(alts[1]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if low > high {
			return nil, fmt.Errorf("%s: low altitude %d is above high altitude %d", s, low, high)
		} else {
			ar := MakeRangeAltitudeRestriction(float32(low), float32(high))
			return &ar, nil
		}
	} else {
		// At
		if alt, err := strconv.Atoi(s); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else {
			ar := MakeAtAltitudeRestriction(float32(alt))
			return &ar, nil
		}
	}
}

// RouteWaypoints converts a real-world route from the city-pair database into
// waypoints. An airway name attaches to the fix before it, so
// InitializeLocations fills in the fixes it passes through. The returned
// waypoints have no Location: the caller must run InitializeLocations on them,
// which is also what discards the tokens that aren't fixes at all--SID and STAR
// names, radial/DME fixes like SLI341/019.
//
// This deliberately doesn't go through the scenario route parser, which
// understands vice's "/" waypoint modifiers and so can't read the routes that
// name such fixes.
func RouteWaypoints(db Database, route string, e *util.ErrorLogger) WaypointArray {
	var waypoints WaypointArray
	for field := range strings.FieldsSeq(route) {
		if _, ok := db.Airways(field); ok && len(waypoints) > 0 {
			// An airway is carried on the fix it leaves, so a route naming
			// two in a row has lost the fix where they meet and only the
			// first can be flown. cmd/importroutes mends the published
			// routes; one written by hand is a mistake to report.
			if wp := &waypoints[len(waypoints)-1]; wp.Airway() == "" {
				wp.InitExtra().Airway = field
			} else {
				e.ErrorString("%s: can't follow the airway %s with another airway",
					field, wp.Airway())
			}
		} else {
			waypoints = append(waypoints, Waypoint{Fix: field})
		}
	}
	return waypoints
}
