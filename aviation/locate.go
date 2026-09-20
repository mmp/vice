// aviation/locate.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// Locator is a simple interface to abstract looking up the location of a
// named thing (e.g. a fix).  This is mostly present so that the route code
// can call back into the ScenarioGroup to resolve locations accounting for
// fixes defined in a scenario, without exposing Scenario-related types to
// the aviation package.
type Locator interface {
	// Locate returns the lat-long coordinates of the named point if they
	// are available; the bool indicates whether the point was known.
	Locate(fix string) (math.Point2LL, bool)

	// Declination returns the station declination of the named VHF navaid,
	// if it has one: the variation its radials are referenced to.
	Declination(fix string) (float32, bool)

	// If Locate fails, Similar can be called to get alternatives that are
	// similarly-spelled to be offered in error messages.
	Similar(fix string) []string
}

type DMELocator interface {
	LocateDME(fix string) (math.Point2LL, int, bool)
}

// initializeActionLocations resolves the fixes the waypoint's action groups
// refer to: the navaids that DME distances and radials are measured from and
// the fixes whose radials are flown.
func (wp *Waypoint) initializeActionLocations(loc Locator, magneticVariation float32, allowSlop bool,
	e *util.ErrorLogger) {
	// radialVariation returns the variation a radial of fix is referenced
	// to. A VHF navaid's radials are fixed to its station declination,
	// which the local variation has usually drifted from since the station
	// was aligned; a fix without a station uses the area's variation.
	radialVariation := func(fix string) float32 {
		if d, ok := loc.Declination(fix); ok {
			return d
		}
		return magneticVariation
	}
	dmeLocator, canLocateDME := loc.(DMELocator)

	for j, group := range wp.ActionGroups() {
		if group.Until.Type == WaypointActionDME {
			if !canLocateDME {
				if e != nil && !allowSlop {
					e.ErrorString("%s: unable to locate DME station %q for waypoint action group %q",
						wp.Fix, group.Until.DMEFix, group.Encoded())
				}
			} else if pos, elevation, ok := dmeLocator.LocateDME(group.Until.DMEFix); ok {
				until := &wp.InitExtra().ActionGroups[j].Until
				until.DMEFixLocation, until.DMEFixElevation = pos, elevation
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate DME station %q with elevation for waypoint action group %q",
					wp.Fix, group.Until.DMEFix, group.Encoded())
			}
		}
		if group.Until.Type == WaypointActionRadial {
			if pos, ok := loc.Locate(group.Until.RadialFix); ok {
				until := &wp.InitExtra().ActionGroups[j].Until
				until.RadialFixLocation, until.RadialFixVariation = pos, radialVariation(group.Until.RadialFix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, group.Until.RadialFix, group.Encoded())
			}
		}
		// The course's line runs through the next fix, so the navaid it
		// is a radial of supplies only the variation, not a location.
		if fix := group.Until.CourseFix; fix != "" {
			if _, ok := loc.Locate(fix); ok {
				wp.InitExtra().ActionGroups[j].Until.CourseFixVariation = radialVariation(fix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, fix, group.Encoded())
			}
		}
		if fix := group.Actions.Heading.Fix; fix != "" {
			if pos, ok := loc.Locate(fix); ok {
				heading := &wp.InitExtra().ActionGroups[j].Actions.Heading
				heading.FixLocation, heading.FixVariation = pos, radialVariation(fix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, fix, group.Encoded())
			}
		}
	}
}

func (wa WaypointArray) InitializeLocations(loc Locator, nmPerLongitude float32, magneticVariation float32,
	allowSlop bool, e *util.ErrorLogger) WaypointArray {
	if len(wa) == 0 {
		return wa
	}

	defer e.CheckDepth(e.CurrentDepth())

	// Get the locations of all waypoints and cull the route after 250nm if cullFar is true.
	// prev is the last waypoint located, which points along a leg are not, so
	// the distance check below spans one charted leg however many of them sit
	// on it.
	var prev math.Point2LL
	prevIdx, nLocated := -1, 0
	for i, wp := range wa {
		if e != nil {
			e.Push("Fix " + wp.Fix)
		}
		wa[i].initializeActionLocations(loc, magneticVariation, allowSlop, e)

		if wp.AlongLeg() {
			// Placed below, once the fixes on either side have been located.
		} else if pos, ok := loc.Locate(wp.Fix); !ok {
			if e != nil && !allowSlop {
				var errstr strings.Builder
				errstr.WriteString("unable to locate waypoint.")
				if sim := loc.Similar(wp.Fix); len(sim) > 0 {
					dist := make(map[string]float32)
					for _, s := range sim {
						if p, ok := loc.Locate(s); ok {
							dist[s] = math.NMDistance2LL(prev, p)
						} else {
							dist[s] = 999999
						}
					}

					sim = util.FilterSliceInPlace(sim, func(s string) bool { return dist[s] < 150 })

					slices.SortFunc(sim, func(a, b string) int {
						return util.Select(dist[a] < dist[b], -1, 1)
					})

					if len(sim) > 0 {
						errstr.WriteString(" Did you mean: ")
					}
					for _, s := range sim {
						errstr.WriteString(fmt.Sprintf("%s (%.1fnm) ", s, dist[s]))
					}
				}
				e.ErrorString("%s", errstr.String())
			}
		} else {
			wa[i].Location = pos

			// A leg longer than this is almost always a typo in a
			// hand-written route; the longest the CIFP charts run about 220nm,
			// out to an exit fix at the edge of a wide TRACON.
			const suspiciousLegLength = 250

			d := math.NMDistance2LL(prev, wa[i].Location)
			if nLocated > 1 && d > suspiciousLegLength && e != nil && !allowSlop && wa[prevIdx].Airway() == "" {
				e.ErrorString("waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
					wa[i].Location.DDString(), wa[prevIdx].Fix, wa[prevIdx].Location.DDString(), d)
			}
			prev, prevIdx = wa[i].Location, i
			nLocated++
		}

		if e != nil {
			e.Pop()
		}
	}

	// Points partway along a leg take their location from the charted fixes on
	// either side, so they are placed once those have been located. Lerping
	// lat-longs rather than following the great circle, as "spawn" does; the
	// difference over a leg is nothing. A point beside a fix that couldn't be
	// located keeps a zero location, the same as the fix itself.
	for i, fix := 0, 0; i < len(wa); i++ {
		if !wa[i].AlongLeg() {
			fix = i
			continue
		}
		if next := wa.nextChartedFix(i); next < len(wa) &&
			!wa[fix].Location.IsZero() && !wa[next].Location.IsZero() {
			wa[i].Location = math.Lerp2f(wa[i].LegOffset(), wa[fix].Location, wa[next].Location)
		}
	}

	// Now go through and expand out any airways into their constituent waypoints
	if slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.Airway() != "" }) { // any airways?
		var wpExpanded []Waypoint
		for i, wp := range wa {
			wpExpanded = append(wpExpanded, wp)

			if wp.Airway() != "" && i+1 < len(wa) {
				found := false
				wp0, wp1 := wp.Fix, wa[i+1].Fix
				for _, airway := range DB.Airways[wp.Airway()] {
					if awps, ok := airway.WaypointsBetween(wp0, wp1); ok {
						for _, awp := range awps {
							if awp.Location, ok = loc.Locate(awp.Fix); ok {
								wpExpanded = append(wpExpanded, awp)
							} else if !allowSlop {
								e.ErrorString("%s: unable to locate fix in airway %s", awp.Fix, wp.Airway())
							}
						}
						found = true
						break
					}
				}

				if !found && e != nil && !allowSlop {
					e.ErrorString("%s: unable to find fix pair %s - %s in airway", wp.Airway(), wp0, wp1)
				}
			}
		}
		wa = wpExpanded
	}

	if allowSlop {
		wa = util.FilterSliceInPlace(wa, func(wp Waypoint) bool { return !wp.Location.IsZero() })
	}

	// Do (DME) arcs after wp.Locations have been initialized
	for i := range wa {
		if wa[i].Arc() == nil {
			continue
		}

		if e != nil {
			e.Push("Fix " + wa[i].Fix)
		}

		if i+1 == len(wa) {
			if e != nil {
				e.ErrorString("can't have DME arc starting at the final waypoint")
				e.Pop()
			}
			break
		}

		if wa[i].Arc().Direction == DMEArcDirectionUnset {
			// Direction wasn't explicitly provided (e.g., from CIFP turn
			// direction); infer it from the surrounding waypoints.
			var v0, v1 [2]float32
			p0 := math.LL2NM(wa[i].Location, nmPerLongitude)
			p1 := math.LL2NM(wa[i+1].Location, nmPerLongitude)
			if i > 0 {
				v0 = math.Sub2f(p0, math.LL2NM(wa[i-1].Location, nmPerLongitude))
				v1 = math.Sub2f(p1, p0)
			} else {
				if i+2 == len(wa) {
					if e != nil {
						e.ErrorString("must have at least one waypoint before or after arc to determine its orientation")
						e.Pop()
					}
					continue
				}
				v0 = math.Sub2f(p1, p0)
				v1 = math.Sub2f(math.LL2NM(wa[i+2].Location, nmPerLongitude), p1)
			}
			// cross product
			x := v0[0]*v1[1] - v0[1]*v1[0]
			wa[i].InitExtra().Arc.Direction = util.Select(x < 0, DMEArcDirectionClockwise, DMEArcDirectionCounterClockwise)
		}

		if !wa[i].Extra.Arc.Initialize(loc, wa[i].Location, wa[i+1].Location, nmPerLongitude, magneticVariation, e) {
			wa[i].Extra.Arc = nil
		}

		if e != nil {
			e.Pop()
		}
	}

	return wa
}
