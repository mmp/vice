// aviation/holds.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// DMEArc

type DMEArcDirection int

const (
	DMEArcDirectionUnset            DMEArcDirection = iota
	DMEArcDirectionClockwise                        // right turn
	DMEArcDirectionCounterClockwise                 // left turn
)

func (d DMEArcDirection) IsClockwise() bool {
	return d == DMEArcDirectionClockwise
}

// Can either be specified with (Fix,Radius), or (Length,Direction); the
// remaining fields are then derived from those.
type DMEArc struct {
	Fix            string
	Center         math.Point2LL
	Radius         float32
	Length         float32
	InitialHeading math.MagneticHeading
	Direction      DMEArcDirection
}

// Initialize resolves the arc's center, radius, and initial heading from
// its specification. Direction must be set before calling. startLoc and
// endLoc are the waypoint positions at each end of the arc. Returns true
// on success; returns false if the arc should be dropped (either due to
// error or because it's approximately linear).
func (arc *DMEArc) Initialize(loc Locator, startLoc, endLoc math.Point2LL, nmPerLongitude, magneticVariation float32, e *util.ErrorLogger) bool {
	p0 := math.LL2NM(startLoc, nmPerLongitude)
	p1 := math.LL2NM(endLoc, nmPerLongitude)

	if arc.Fix != "" {
		// Center point was specified
		var ok bool
		if arc.Center, ok = loc.Locate(arc.Fix); !ok {
			e.ErrorString("unable to locate arc center %q", arc.Fix)
			return false
		}
	} else {
		// Just the arc length was specified; need to figure out the
		// center and radius of the circle that gives that.
		d := math.Distance2f(p0, p1)
		if arc.Length < d {
			if math.Abs(arc.Length-d) < float32(0.1) {
				// Close enough to linear
				return false
			}
			e.ErrorString("distance between waypoints %.2fnm is greater than specified arc length %.2fnm",
				d, arc.Length)
			return false
		}
		if arc.Length > d*3.14159 {
			e.ErrorString("no valid circle will give a distance between waypoints %.2fnm", arc.Length)
			return false
		}

		// Search for a center point of a circle that goes through p0
		// and p1 and has the desired arc length, searching along the
		// line perpendicular to p1-p0 that goes through its center
		// point.
		//
		// There are two possible center points for the circle, one on
		// each side of the line p0-p1. We will take positive or
		// negative steps in parametric t along the perpendicular line
		// so that we're searching in the right direction to get the
		// clockwise/counter clockwise route we want.
		delta := float32(util.Select(arc.Direction.IsClockwise(), -.01, .01))

		// We will search with uniform small steps along the line. Some
		// sort of bisection search would probably be better, but...
		t := delta
		limit := 100 * math.Distance2f(p0, p1) // ad-hoc
		v := math.Normalize2f(math.Sub2f(p1, p0))
		v[0], v[1] = -v[1], v[0] // perp!
		for t < limit {
			center := math.Add2f(math.Mid2f(p0, p1), math.Scale2f(v, t))
			radius := math.Distance2f(center, p0)

			// Angle subtended by p0 and p1 w.r.t. center
			cosTheta := math.Dot(math.Sub2f(p0, center), math.Sub2f(p1, center)) / math.Sqr(radius)
			theta := math.SafeACos(cosTheta)

			arcLength := theta * radius

			if arcLength < arc.Length {
				arc.Center = math.NM2LL(center, nmPerLongitude)
				arc.Radius = radius
				break
			}

			t += delta
		}

		if t >= limit {
			e.ErrorString("unable to find valid circle radius for arc")
			return false
		}
	}

	// Heading from the center of the arc to the start fix (true), then
	// convert to magnetic; perpendicular depending on the arc's direction.
	hfix := math.Heading2LL(arc.Center, startLoc, nmPerLongitude)
	arc.InitialHeading = math.TrueToMagnetic(
		math.OffsetHeading(hfix, float32(util.Select(arc.Direction.IsClockwise(), 90, -90))),
		magneticVariation)

	return true
}

///////////////////////////////////////////////////////////////////////////
// Hold

// TurnDirection specifies the direction of a turn.
type TurnDirection math.TurnDirection

const (
	TurnClosest TurnDirection = TurnDirection(math.TurnClosest) // default: turn the shortest direction
	TurnLeft    TurnDirection = TurnDirection(math.TurnLeft)
	TurnRight   TurnDirection = TurnDirection(math.TurnRight)
)

func (t TurnDirection) String() string {
	return []string{"closest", "left", "right"}[int(t)]
}

// Hold represents a charted holding pattern from CIFP or HPF
type Hold struct {
	Fix             string               // Fix identifier where hold is located
	InboundCourse   math.MagneticHeading // Inbound magnetic course to the fix
	TurnDirection   TurnDirection
	LegLengthNM     float32 // Distance-based leg length (nautical miles), 0 if time-based
	LegMinutes      float32 // Time-based leg duration (minutes), 0 if distance-based
	MinimumAltitude int     // Minimum altitude for hold (feet)
	MaximumAltitude int     // Maximum altitude for hold (feet), 0 if no maximum
	HoldingSpeed    int     // Speed limit in hold (knots), 0 if not specified
	Procedure       string  // Associated procedure (e.g., "ILS06 (IAP)", "CAMRN5", "ENROUTE HIGH")
}

func (h Hold) DisplayName() string {
	n := fmt.Sprintf("%s (%s", h.Fix, h.TurnDirection)
	if h.LegLengthNM != 0 {
		n += fmt.Sprintf(", %.1f nm", h.LegLengthNM)
	} else if h.LegMinutes != 0 {
		n += fmt.Sprintf(", %.1f min", h.LegMinutes)
	}
	return n + ")"
}

// Speed returns the holding speed in knots for the given altitude.
// If the hold has a published holding speed, that is returned.
// Otherwise, standard holding speeds are applied based on altitude:
// ≤6000 ft: 200 knots, ≤14000 ft: 230 knots, >14000 ft: 265 knots.
func (h Hold) Speed(alt float32) float32 {
	if h.HoldingSpeed > 0 {
		return float32(h.HoldingSpeed)
	} else if alt <= 6000 {
		return 200
	} else if alt <= 14000 {
		return 230
	} else {
		return 265
	}
}

type HoldEntry int

const (
	HoldEntryDirect HoldEntry = iota
	HoldEntryParallel
	HoldEntryTeardrop
)

func (e HoldEntry) String() string {
	return []string{"Direct", "Parallel", "Teardrop"}[int(e)]
}

func (h Hold) Entry(headingToFix math.MagneticHeading) HoldEntry {
	outboundCourse := math.OppositeHeading(h.InboundCourse)

	// Dividing line is 70° from outbound on holding side This creates
	// three sectors measured from the outbound course:
	// - Parallel: 110° on holding side from outbound
	// - Teardrop: 70° on non-holding side from outbound
	// - Direct: remaining 180°
	if h.TurnDirection == TurnRight {
		// Right turns: holding side is clockwise from outbound
		// Parallel sector: outbound to outbound+110°
		// Teardrop sector: outbound-70° to outbound
		if math.IsHeadingBetween(headingToFix, outboundCourse, outboundCourse+110) {
			return HoldEntryParallel
		} else if math.IsHeadingBetween(headingToFix, outboundCourse-70, outboundCourse) {
			return HoldEntryTeardrop
		} else {
			return HoldEntryDirect
		}
	} else {
		// Left turns: holding side is counter-clockwise from outbound
		// Parallel sector: outbound-110° to outbound
		// Teardrop sector: outbound to outbound+70°
		if math.IsHeadingBetween(headingToFix, outboundCourse-110, outboundCourse) {
			return HoldEntryParallel
		} else if math.IsHeadingBetween(headingToFix, outboundCourse, outboundCourse+70) {
			return HoldEntryTeardrop
		} else {
			return HoldEntryDirect
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Airways

type AirwayLevel int

const (
	AirwayLevelAll = iota
	AirwayLevelLow
	AirwayLevelHigh
)

type AirwayDirection int

const (
	AirwayDirectionAny = iota
	AirwayDirectionForward
	AirwayDirectionBackward
)

type AirwayFix struct {
	Fix       string
	Level     AirwayLevel
	Direction AirwayDirection
}

type Airway struct {
	Name  string
	Fixes []AirwayFix
}

func (a Airway) WaypointsBetween(wp0, wp1 string) ([]Waypoint, bool) {
	start := slices.IndexFunc(a.Fixes, func(f AirwayFix) bool { return f.Fix == wp0 })
	end := slices.IndexFunc(a.Fixes, func(f AirwayFix) bool { return f.Fix == wp1 })
	if start == -1 || end == -1 {
		return nil, false
	}

	var wps []Waypoint
	delta := util.Select(start < end, 1, -1)
	// Index so that we return waypoints exclusive of wp0 and wp1
	for i := start + delta; i != end; i += delta {
		wp := Waypoint{Fix: a.Fixes[i].Fix}
		wp.InitExtra().Airway = a.Name // maintain the identity that we're on an airway
		wps = append(wps, wp)
	}
	return wps, true
}
