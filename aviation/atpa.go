// aviation/atpa.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"slices"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// ATPARunway is a runway's ATPA adaptation: how in-trail separation is
// monitored for the aircraft landing on it and the shape of the approach
// volumes generated along its final approach courses.
type ATPARunway struct {
	// ReducedSeparation authorizes 2.5nm in-trail separation within
	// ReducedSeparationDistance of the runway (7110.65 5-5-4.j).
	ReducedSeparation   bool     `json:"reduced_separation"`
	FilteredScratchpads []string `json:"filtered_scratchpads"`
	ExcludedScratchpads []string `json:"excluded_scratchpads"`

	ATPAVolumeGeometry
}

// ATPAVolumeGeometry is the extent of an ATPA approach volume around its
// final approach course.
type ATPAVolumeGeometry struct {
	MaxHeadingDeviation float32 `json:"max_heading_deviation"`
	Floor               float32 `json:"floor"`
	Ceiling             float32 `json:"ceiling"`
	Length              float32 `json:"length"`
	LeftWidth           float32 `json:"left_width"`
	RightWidth          float32 `json:"right_width"`
}

// ReducedSeparationDistance is how close to the landing runway, in nm,
// aircraft established on the final approach course may be separated by
// 2.5nm (7110.65 5-5-4.j).
const ReducedSeparationDistance = 10

// ATPAVolume is a region along a final approach course in which STARS's
// automated terminal proximity alert monitors in-trail spacing. STARS adapts
// each one as a rectangle extending from the runway threshold (DMS 4.26.2).
// vice's follow the course aircraft actually fly, so that an offset
// localizer or a curved RNAV final is covered. An airport has one for each
// distinct final approach course to each of its runways.
type ATPAVolume struct {
	Id     string
	Runway string

	// Path is the final approach course in the direction of flight, ending
	// at the runway threshold. The volume extends LeftWidth and RightWidth
	// feet either side of it.
	Path      Path
	Threshold math.Point2LL

	// Outline is the volume's boundary as a closed polygon, for drawing it.
	Outline []math.Point2LL

	ATPAVolumeGeometry
}

const (
	defaultATPALength           = 15   // nm
	defaultATPAWidth            = 2000 // feet, each side of the course
	defaultATPAHeadingDeviation = 90

	// maxATPACourseTurn is how far, in degrees, the route before an
	// approach's FAF may turn and still count as the final approach course:
	// a dogleg at the intermediate fix is flown as part of the final, but
	// the base leg of a T-shaped RNAV approach is not.
	maxATPACourseTurn = 30

	// atpaCenterlineTolerance is how far off the line from a volume's far
	// end to the threshold its course may wander and still be straight.
	atpaCenterlineTolerance = 0.2 // nm
)

// finalize fills in defaults for dimensions the scenario left out and reports
// ones that can't be meant.
func (s *ATPAVolumeGeometry) finalize(elevation int, e *util.ErrorLogger) {
	if s.MaxHeadingDeviation == 0 {
		s.MaxHeadingDeviation = defaultATPAHeadingDeviation
	}
	if s.Floor == 0 {
		s.Floor = float32(elevation + 100)
	}
	if s.Ceiling == 0 {
		s.Ceiling = float32(elevation + 5000)
	}
	if s.Length == 0 {
		s.Length = defaultATPALength
	}
	if s.LeftWidth == 0 {
		s.LeftWidth = defaultATPAWidth
	}
	if s.RightWidth == 0 {
		s.RightWidth = defaultATPAWidth
	}

	if s.MaxHeadingDeviation < 0 || s.MaxHeadingDeviation > 180 {
		e.ErrorString(`"max_heading_deviation" %.0f must be between 0 and 180`, s.MaxHeadingDeviation)
	}
	if s.Ceiling < float32(elevation+1000) {
		e.ErrorString(`"ceiling" %.0f is less than 1000 feet above the airport's elevation of %d`,
			s.Ceiling, elevation)
	}
	if s.Floor >= s.Ceiling {
		e.ErrorString(`"floor" %.0f must be below "ceiling" %.0f`, s.Floor, s.Ceiling)
	}
}

// finalizeATPAVolumes generates the airport's ATPA volumes from its
// approaches: one for each distinct final approach course to each runway,
// shared by the approaches that fly it. A volume on the runway's own
// centerline is named after the runway; any other is named after the
// approach that defines it. Each approach records the volume it flies in.
// A runway's volumes take their geometry from the scenario's "atpa" entry
// for it; a runway that has volumes but no entry gets a default one.
func (ap *Airport) finalizeATPAVolumes(icao ICAOAirportCode, db Database, nmPerLongitude, magneticVariation float32,
	e *util.ErrorLogger) {
	elevation := db.AirportElevation(icao)
	for rwy, r := range util.SortedMap(ap.ATPARunways) {
		e.Push("ATPA " + rwy)
		if dbr, ok := LookupRunway(db, icao, rwy); !ok || dbr.Id != rwy {
			e.ErrorString("%q is not a runway; the settings are keyed by runway, one of %s", rwy, db.ValidRunways(icao))
		} else if len(ap.ApproachesToRunway(rwy)) == 0 {
			e.ErrorString("no approaches land on runway %q, so it has no ATPA volume", rwy)
		}
		r.finalize(elevation, e)
		e.Pop()
	}

	if ap.ATPARunways == nil {
		ap.ATPARunways = make(map[string]*ATPARunway)
	}
	ap.ATPAVolumes = make(map[string]*ATPAVolume)
	for _, rwy := range db.AirportRunways(icao) {
		r, adapted := ap.ATPARunways[rwy.Id]
		if !adapted {
			r = &ATPARunway{}
			r.finalize(elevation, e)
		}

		var volumes []*ATPAVolume
		// Localizer approaches go first so that the localizer's course
		// defines the volume the other approaches along it share.
		for _, localizer := range []bool{true, false} {
			for name, appr := range util.SortedMap(ap.Approaches) {
				if appr.Runway != rwy.Id || appr.Type.HasLocalizer() != localizer {
					continue
				}
				path, ok := appr.atpaCourse(r.Length, nmPerLongitude, magneticVariation)
				if !ok {
					continue
				}

				// An approach joins the volume that holds its course. Straight
				// courses within a few degrees of the runway all count as its
				// centerline and share the runway's volume, which widens to
				// hold them: an ILS a couple of degrees off its runway and
				// the RNAV on the centerline are half a mile apart 15nm out,
				// but they are flown as one final.
				centerline := courseOnCenterline(path, appr.RunwayHeading(nmPerLongitude))
				i := slices.IndexFunc(volumes, func(v *ATPAVolume) bool {
					return v.containsCourse(path) || (centerline && v.Id == rwy.Id)
				})
				if i != -1 {
					volumes[i].widenToHold(path, nmPerLongitude)
					appr.ATPAVolumeId = volumes[i].Id
					continue
				}

				vol := &ATPAVolume{
					Id:                 util.Select(centerline, rwy.Id, name),
					Runway:             rwy.Id,
					Path:               path,
					Threshold:          rwy.Threshold,
					ATPAVolumeGeometry: r.ATPAVolumeGeometry,
				}
				vol.setOutline(nmPerLongitude)
				if _, ok := ap.ATPAVolumes[vol.Id]; ok {
					e.ErrorString("ATPA volume %q for approach %s to runway %s has the same name as another volume",
						vol.Id, name, rwy.Id)
				}
				ap.ATPAVolumes[vol.Id] = vol
				volumes = append(volumes, vol)
				appr.ATPAVolumeId = vol.Id
			}
		}
		if len(volumes) > 0 && !adapted {
			ap.ATPARunways[rwy.Id] = r
		}
	}
}

// atpaCourse returns the approach's final approach course as a path of the
// given length in the direction of flight, ending at the runway threshold.
// An approach flown on a localizer follows the localizer, turning onto the
// runway where it is well offset (see localizerTurnWaypoint). Any other
// follows its route back from the threshold to the FAF and then on through
// the fixes before it for as long as the route holds the course into the
// FAF; one without a FAF, as a charted visual may be, follows its whole
// route.
func (ap *Approach) atpaCourse(length, nmPerLongitude, magneticVariation float32) (Path, bool) {
	wps, _ := ap.FAFSegment(nmPerLongitude, magneticVariation)
	if wps == nil && len(ap.Waypoints) > 0 {
		wps = ap.Waypoints[0]
	}
	if len(wps) < 2 || ap.Threshold.IsZero() {
		return Path{}, false
	}

	var pts []CRDARoutePoint
	if ap.Type.HasLocalizer() {
		line := ap.FinalApproachCourseLine(nmPerLongitude, magneticVariation)
		back := math.OppositeHeading(math.Heading2LL(line[0], line[1], nmPerLongitude))
		pts = []CRDARoutePoint{{Location: ap.Threshold}}
		if turn := wps[len(wps)-2]; strings.HasSuffix(turn.Fix, "_LOC_TURN") {
			pts = append(pts, CRDARoutePoint{Location: turn.Location})
		}
		anchor := pts[len(pts)-1].Location
		up := max(length-math.NMDistance2LL(anchor, ap.Threshold), 1)
		pts = append(pts, CRDARoutePoint{Location: math.Offset2LL(anchor, back, up, nmPerLongitude)})
		slices.Reverse(pts)
	} else {
		start := max(slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF() }), 0)
		if start > 0 {
			course := math.Heading2LL(wps[start-1].Location, wps[start].Location, nmPerLongitude)
			onCourse := func(i int) bool {
				return math.HeadingDifference(math.Heading2LL(wps[i-1].Location, wps[i].Location, nmPerLongitude),
					course) <= maxATPACourseTurn
			}
			for start > 0 && onCourse(start) {
				start--
			}
		}
		for _, wp := range wps[start:] {
			pts = append(pts, CRDARoutePoint{Fix: wp.Fix, Location: wp.Location, Arc: wp.Arc()})
		}
	}
	return fitCourseLength(pts, length, nmPerLongitude), true
}

// fitCourseLength returns the path through pts, in order, cut or extended at
// its start so that it is length nm long. An extension continues the first
// leg straight back.
func fitCourseLength(pts []CRDARoutePoint, length, nmPerLongitude float32) Path {
	const tolerance = 0.01 // nm; don't add a negligible segment
	path := PathFromRoutePoints(pts, nmPerLongitude)
	if math.Abs(path.Length-length) < tolerance {
		return path
	}
	if path.Length < length {
		p, _ := path.PointAtDistance(path.Length - length)
		pts = slices.Insert(pts, 0, CRDARoutePoint{Location: math.NM2LL(p, nmPerLongitude)})
		return PathFromRoutePoints(pts, nmPerLongitude)
	}

	cut := path.Length - length
	for i, seg := range path.Segments {
		if cut < seg.StartDist+seg.Length {
			// The new first point starts the cut segment's arc, if it has
			// one, since PathFromRoutePoints takes an arc's start from its
			// point.
			p := pointOnSegmentAtDist(seg, cut-seg.StartDist)
			pts = append([]CRDARoutePoint{{Location: math.NM2LL(p, nmPerLongitude), Arc: pts[i].Arc}}, pts[i+1:]...)
			return PathFromRoutePoints(pts, nmPerLongitude)
		}
	}
	return path
}

// courseSamples returns points every half mile along the final approach
// course path, clear of its ends.
func courseSamples(path Path) [][2]float32 {
	var pts [][2]float32
	for d := float32(0.25); d < path.Length; d += 0.5 {
		p, _ := path.PointAtDistance(d)
		pts = append(pts, p)
	}
	return pts
}

// containsCourse reports whether the final approach course path lies within
// the volume, so that the approach flying it can share the volume.
func (v *ATPAVolume) containsCourse(path Path) bool {
	return !slices.ContainsFunc(courseSamples(path), func(p [2]float32) bool {
		offset, toLeft, _ := v.courseAt(p)
		return offset > v.width(toLeft)
	})
}

// atpaWidenMargin is how much beyond a course the volume widened to hold
// it extends, so that aircraft flying the course are not on its edge.
const atpaWidenMargin = 200 // feet

// widenToHold widens the volume as far as it has to for the final approach
// course path to lie within it.
func (v *ATPAVolume) widenToHold(path Path, nmPerLongitude float32) {
	left, right := v.LeftWidth, v.RightWidth
	for _, p := range courseSamples(path) {
		offset, toLeft, _ := v.courseAt(p)
		if toLeft {
			left = max(left, offset+atpaWidenMargin)
		} else {
			right = max(right, offset+atpaWidenMargin)
		}
	}
	if left != v.LeftWidth || right != v.RightWidth {
		v.LeftWidth, v.RightWidth = left, right
		v.setOutline(nmPerLongitude)
	}
}

// courseAt returns how far p is from the nearest point of the volume's
// final approach course, in feet, which side of the course it is on, and
// the course's heading there. Measuring from the nearest point rounds the
// volume's ends: a point past the threshold is as far from the course as it
// is from the threshold.
func (v *ATPAVolume) courseAt(p [2]float32) (offset float32, toLeft bool, course math.TrueHeading) {
	dist, perp, heading := v.Path.Project(p)
	nearest, _ := v.Path.PointAtDistance(dist)
	return math.Distance2f(p, nearest) * math.NauticalMilesToFeet, perp >= 0, math.TrueHeading(heading)
}

// width returns the volume's width in feet on the given side of its course.
func (v *ATPAVolume) width(left bool) float32 {
	return util.Select(left, v.LeftWidth, v.RightWidth)
}

// setOutline sets the volume's outline from its path and widths.
func (v *ATPAVolume) setOutline(nmPerLongitude float32) {
	outline := corridorOutline(v.Path, v.LeftWidth/math.NauticalMilesToFeet, v.RightWidth/math.NauticalMilesToFeet)
	v.Outline = util.MapSlice(outline, func(p [2]float32) math.Point2LL { return math.NM2LL(p, nmPerLongitude) })
}

// courseOnCenterline reports whether the final approach course path is a
// straight line aligned with its runway, as a straight-in approach's is.
func courseOnCenterline(path Path, runwayHeading math.TrueHeading) bool {
	pts := path.Polyline()
	first, last := pts[0], pts[len(pts)-1]
	if math.HeadingDifference(math.VectorHeading(math.Sub2f(last, first)), runwayHeading) > maxLocalizerOffset {
		return false
	}
	return !slices.ContainsFunc(pts, func(p [2]float32) bool {
		return math.Abs(math.SignedPointLineDistance(p, first, last)) > atpaCenterlineTolerance
	})
}

// corridorOutline returns the boundary of the region left and right nm
// either side of path, as a closed polygon in NM coordinates: up its left
// side and back down its right. It is for drawing the volume; at a sharp
// bend its inside corner can fold over, so containment is tested against
// the path instead.
func corridorOutline(path Path, left, right float32) [][2]float32 {
	pts := path.Polyline()
	n := len(pts)
	direction := func(i, j int) [2]float32 { return math.Normalize2f(math.Sub2f(pts[j], pts[i])) }
	leftOf := func(d [2]float32) [2]float32 { return [2]float32{-d[1], d[0]} }

	outline := make([][2]float32, 2*n)
	for i, p := range pts {
		// The offset at a bend follows the bisector of the two legs, scaled
		// so that the corridor keeps its width along both.
		var normal [2]float32
		scale := float32(1)
		switch i {
		case 0:
			normal = leftOf(direction(0, 1))
		case n - 1:
			normal = leftOf(direction(n-2, n-1))
		default:
			in, out := leftOf(direction(i-1, i)), leftOf(direction(i, i+1))
			normal = math.Normalize2f(math.Add2f(in, out))
			scale = 1 / max(math.Dot(normal, out), 0.5)
		}
		outline[i] = math.Add2f(p, math.Scale2f(normal, left*scale))
		outline[2*n-1-i] = math.Add2f(p, math.Scale2f(normal, -right*scale))
	}
	return outline
}

// Inside reports whether an aircraft at p, at altitude alt and flying true
// heading hdg, is in the volume: within its altitudes, within its width of
// the course, and heading within MaxHeadingDeviation of the course where it
// is.
func (v *ATPAVolume) Inside(p math.Point2LL, alt float32, hdg math.TrueHeading, nmPerLongitude float32) bool {
	if alt < v.Floor || alt > v.Ceiling {
		return false
	}
	offset, toLeft, course := v.courseAt(math.LL2NM(p, nmPerLongitude))
	return offset <= v.width(toLeft) && math.HeadingDifference(hdg, course) <= v.MaxHeadingDeviation
}
