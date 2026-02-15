// pkg/aviation/path.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"github.com/mmp/vice/math"
)

// Path represents a piecewise curve (line segments and circular arcs) in
// NM coordinates. Path direction follows aircraft travel direction: the
// first point is the far end, the last point is the convergence end
// (threshold/fix).
type Path struct {
	Segments []PathSegment
	Length   float32
}

type PathSegment struct {
	P0, P1    [2]float32 // start/end in NM coordinates
	Arc       *PathArc   // nil for straight segments
	StartDist float32    // cumulative distance at segment start
	Length    float32    // segment length
}

type PathArc struct {
	Center     [2]float32
	Radius     float32
	StartAngle float32 // angle from center to P0 (radians)
	Sweep      float32 // signed: positive=CCW, negative=CW
}

// Project projects a point onto the path, returning the distance along the
// path, the signed perpendicular offset (positive = left of path
// direction), and the local path heading (degrees, true north).
func (path *Path) Project(p [2]float32) (dist, perpOffset, heading float32) {
	bestDist := float32(-1)
	bestTotalDist := float32(0)
	bestPerp := float32(0)
	bestHeading := float32(0)

	for _, seg := range path.Segments {
		var segDist, segPerp, segHeading float32
		if seg.Arc != nil {
			segDist, segPerp, segHeading = projectOntoArc(p, seg)
		} else {
			segDist, segPerp, segHeading = projectOntoLine(p, seg)
		}

		// Distance from point to nearest point on segment
		nearest := pointOnSegmentAtDist(seg, segDist)
		d := math.Distance2f(p, nearest)

		if bestDist < 0 || d < bestDist {
			bestDist = d
			bestTotalDist = seg.StartDist + segDist
			bestPerp = segPerp
			bestHeading = segHeading
		}
	}

	return bestTotalDist, bestPerp, bestHeading
}

// PointAtDistance returns the point and heading at a given distance along
// the path. If dist exceeds the path length, the path is extended
// linearly along the last segment's final heading. If dist is negative,
// the path is extended linearly backwards from the first segment.
func (path *Path) PointAtDistance(dist float32) (point [2]float32, heading float32) {
	if len(path.Segments) == 0 {
		return [2]float32{}, 0
	}

	// Before the path start: extend backwards from first segment
	if dist < 0 {
		seg := path.Segments[0]
		h := segmentHeadingAt(seg, 0)
		hrad := math.Radians(h)
		v := math.SinCos(hrad)
		p := seg.P0
		return math.Add2f(p, math.Scale2f(v, dist)), h
	}

	// Beyond path end: extend linearly from last segment
	if dist > path.Length {
		seg := path.Segments[len(path.Segments)-1]
		h := segmentHeadingAt(seg, seg.Length)
		hrad := math.Radians(h)
		v := math.SinCos(hrad)
		p := seg.P1
		extra := dist - path.Length
		return math.Add2f(p, math.Scale2f(v, extra)), h
	}

	// Find the segment containing this distance
	for _, seg := range path.Segments {
		if dist <= seg.StartDist+seg.Length {
			localDist := dist - seg.StartDist
			return pointOnSegmentAtDist(seg, localDist), segmentHeadingAt(seg, localDist)
		}
	}

	// Shouldn't get here, but return last point
	seg := path.Segments[len(path.Segments)-1]
	return seg.P1, segmentHeadingAt(seg, seg.Length)
}

// Polyline returns a polyline approximation of the path for drawing. Arc
// segments are sampled at approximately 1-degree intervals.
func (path *Path) Polyline() [][2]float32 {
	if len(path.Segments) == 0 {
		return nil
	}

	var pts [][2]float32
	for i, seg := range path.Segments {
		if i == 0 {
			pts = append(pts, seg.P0)
		}
		if seg.Arc != nil {
			// Sample the arc
			steps := max(1, int(math.Abs(math.Degrees(seg.Arc.Sweep))))
			for s := 1; s <= steps; s++ {
				t := float32(s) / float32(steps)
				angle := seg.Arc.StartAngle + seg.Arc.Sweep*t
				pt := [2]float32{
					seg.Arc.Center[0] + seg.Arc.Radius*math.Cos(angle),
					seg.Arc.Center[1] + seg.Arc.Radius*math.Sin(angle),
				}
				pts = append(pts, pt)
			}
		} else {
			pts = append(pts, seg.P1)
		}
	}
	return pts
}

// PathFromReferenceLine creates a Path from a straight reference line
// defined by a reference point, heading, and length. The path goes from
// far end toward the reference point (matching the aircraft travel
// direction convention).
func PathFromReferenceLine(refPoint math.Point2LL, heading, length, nmPerLongitude, magneticVariation float32) Path {
	// The reference point is at the convergence end (path end).
	// The heading is the magnetic heading aircraft fly (toward ref point).
	// We build the path from far end to ref point.
	pref := math.LL2NM(refPoint, nmPerLongitude)
	// Direction aircraft are flying: heading corrected for magnetic variation
	hdg := math.Radians(heading + 180 - magneticVariation)
	v := math.SinCos(hdg)

	// Far end is at distance=length from reference point, along the reverse heading
	farEnd := math.Add2f(pref, math.Scale2f(v, length))

	seg := PathSegment{
		P0:        farEnd,
		P1:        pref,
		StartDist: 0,
		Length:    length,
	}

	return Path{
		Segments: []PathSegment{seg},
		Length:   length,
	}
}

// PathFromRoutePoints creates a Path from resolved CRDA route points.
// Consecutive point pairs become segments, using pt.Arc for arc segments.
func PathFromRoutePoints(pts []CRDARoutePoint, nmPerLongitude float32) Path {
	if len(pts) < 2 {
		return Path{}
	}

	var segments []PathSegment
	var cumDist float32

	for i := 0; i < len(pts)-1; i++ {
		p0 := math.LL2NM(pts[i].Location, nmPerLongitude)
		p1 := math.LL2NM(pts[i+1].Location, nmPerLongitude)

		seg := PathSegment{
			P0:        p0,
			P1:        p1,
			StartDist: cumDist,
		}

		if pts[i].Arc != nil {
			arc := pts[i].Arc
			center := math.LL2NM(arc.Center, nmPerLongitude)
			radius := arc.Radius

			// Compute angles from center to P0 and P1
			startAngle := math.Atan2(p0[1]-center[1], p0[0]-center[0])
			endAngle := math.Atan2(p1[1]-center[1], p1[0]-center[0])

			// Determine sweep based on clockwise/counterclockwise
			sweep := endAngle - startAngle
			if arc.Clockwise {
				// CW = negative sweep
				if sweep > 0 {
					sweep -= 2 * math.Pi
				}
			} else {
				// CCW = positive sweep
				if sweep < 0 {
					sweep += 2 * math.Pi
				}
			}

			seg.Arc = &PathArc{
				Center:     center,
				Radius:     radius,
				StartAngle: startAngle,
				Sweep:      sweep,
			}
			seg.Length = radius * math.Abs(sweep)
		} else {
			seg.Length = math.Distance2f(p0, p1)
		}

		cumDist += seg.Length
		segments = append(segments, seg)
	}

	return Path{
		Segments: segments,
		Length:   cumDist,
	}
}

// projectOntoLine projects point p onto a straight line segment, returning
// the clamped distance along the segment, signed perpendicular offset, and heading.
func projectOntoLine(p [2]float32, seg PathSegment) (dist, perp, heading float32) {
	d := math.Sub2f(seg.P1, seg.P0)
	segLen := seg.Length
	if segLen < 1e-6 {
		return 0, math.Distance2f(p, seg.P0), 0
	}

	// Parameter along the line
	t := math.Dot(math.Sub2f(p, seg.P0), d) / (segLen * segLen)
	t = math.Clamp(t, 0, 1)
	dist = t * segLen

	// Perpendicular offset: positive = left of travel direction
	// Travel direction is d; left is (-d[1], d[0])
	leftNorm := math.Normalize2f([2]float32{-d[1], d[0]})
	onLine := math.Add2f(seg.P0, math.Scale2f(d, t))
	perp = math.Dot(math.Sub2f(p, onLine), leftNorm)

	// Heading: direction of travel in degrees from true north
	heading = math.Degrees(math.Atan2(d[0], d[1]))
	if heading < 0 {
		heading += 360
	}
	return
}

// projectOntoArc projects point p onto an arc segment.
func projectOntoArc(p [2]float32, seg PathSegment) (dist, perp, heading float32) {
	arc := seg.Arc
	// Angle from center to p
	dp := math.Sub2f(p, arc.Center)
	angle := math.Atan2(dp[1], dp[0])

	// Normalize angle relative to start
	relAngle := angle - arc.StartAngle
	// Normalize to [-pi, pi]
	for relAngle > math.Pi {
		relAngle -= 2 * math.Pi
	}
	for relAngle < -math.Pi {
		relAngle += 2 * math.Pi
	}

	// Clamp to sweep range
	var t float32
	if arc.Sweep > 0 {
		// CCW
		if relAngle < 0 {
			relAngle += 2 * math.Pi
		}
		t = relAngle / arc.Sweep
	} else {
		// CW
		if relAngle > 0 {
			relAngle -= 2 * math.Pi
		}
		t = relAngle / arc.Sweep
	}
	t = math.Clamp(t, 0, 1)

	dist = t * seg.Length

	// Perpendicular offset: distance from arc centerline (positive = left of travel)
	pDist := math.Length2f(dp)
	radialOffset := pDist - arc.Radius

	// For CW arcs, the center is to the right, so positive radial = left of travel
	// For CCW arcs, the center is to the left, so positive radial = right of travel
	if arc.Sweep > 0 {
		perp = -radialOffset // CCW: center is to the left
	} else {
		perp = radialOffset // CW: center is to the right
	}

	// Heading: tangent to arc at projected point
	projAngle := arc.StartAngle + arc.Sweep*t
	var tangentAngle float32
	if arc.Sweep > 0 {
		tangentAngle = projAngle + math.Pi/2 // CCW: tangent is 90 degrees ahead of radial
	} else {
		tangentAngle = projAngle - math.Pi/2 // CW: tangent is 90 degrees behind radial
	}
	// Convert to compass heading: atan2(east, north) in NM coords
	tx := math.Cos(tangentAngle)
	ty := math.Sin(tangentAngle)
	heading = math.Degrees(math.Atan2(tx, ty))
	if heading < 0 {
		heading += 360
	}

	return
}

// pointOnSegmentAtDist returns the point on a segment at the given local distance.
func pointOnSegmentAtDist(seg PathSegment, dist float32) [2]float32 {
	if seg.Arc != nil {
		t := dist / seg.Length
		angle := seg.Arc.StartAngle + seg.Arc.Sweep*t
		return [2]float32{
			seg.Arc.Center[0] + seg.Arc.Radius*math.Cos(angle),
			seg.Arc.Center[1] + seg.Arc.Radius*math.Sin(angle),
		}
	}
	if seg.Length < 1e-6 {
		return seg.P0
	}
	t := dist / seg.Length
	return math.Lerp2f(t, seg.P0, seg.P1)
}

// segmentHeadingAt returns the heading at a given local distance along a segment.
func segmentHeadingAt(seg PathSegment, dist float32) float32 {
	if seg.Arc != nil {
		t := dist / seg.Length
		projAngle := seg.Arc.StartAngle + seg.Arc.Sweep*t
		var tangentAngle float32
		if seg.Arc.Sweep > 0 {
			tangentAngle = projAngle + math.Pi/2
		} else {
			tangentAngle = projAngle - math.Pi/2
		}
		tx := math.Cos(tangentAngle)
		ty := math.Sin(tangentAngle)
		h := math.Degrees(math.Atan2(tx, ty))
		if h < 0 {
			h += 360
		}
		return h
	}

	d := math.Sub2f(seg.P1, seg.P0)
	h := math.Degrees(math.Atan2(d[0], d[1]))
	if h < 0 {
		h += 360
	}
	return h
}
