// pkg/aviation/airspace.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/brunoga/deep"
	"github.com/mmp/earcut-go"
)

type AirspaceVolume struct {
	Id          string             `json:"id"`
	Description string             `json:"description"`
	Type        AirspaceVolumeType `json:"type"`
	Floor       int                `json:"floor"`
	Ceiling     int                `json:"ceiling"`
	// Polygon
	PolygonBounds *math.Extent2D               // not always set
	VerticesStr   util.OneOf[string, []string] `json:"vertices"`
	Vertices      []math.Point2LL
	Holes         [][]math.Point2LL `json:"holes"`
	// Circle
	Center math.Point2LL `json:"center"`
	Radius float32       `json:"radius"`
}

type AirspaceVolumeType int

const (
	AirspaceVolumeUnknown AirspaceVolumeType = iota
	AirspaceVolumePolygon
	AirspaceVolumeCircle
)

func (t *AirspaceVolumeType) MarshalJSON() ([]byte, error) {
	switch *t {
	case AirspaceVolumePolygon:
		return []byte(`"polygon"`), nil
	case AirspaceVolumeCircle:
		return []byte(`"circle"`), nil
	default:
		return nil, fmt.Errorf("%d: unknown airspace volume type", *t)
	}
}

func (t *AirspaceVolumeType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"polygon"`:
		*t = AirspaceVolumePolygon
		return nil
	case `"circle"`:
		*t = AirspaceVolumeCircle
		return nil
	default:
		return fmt.Errorf("%s: unknown airspace volume type", string(b))
	}
}

func (a *AirspaceVolume) Inside(p math.Point2LL, alt int) bool {
	if alt <= a.Floor || alt > a.Ceiling {
		return false
	}

	switch a.Type {
	case AirspaceVolumePolygon:
		if a.PolygonBounds != nil && !a.PolygonBounds.Inside(p) {
			return false
		}
		if !math.PointInPolygon2LL(p, a.Vertices) {
			return false
		}
		return !util.SeqContainsFunc(slices.Values(a.Holes), func(hole []math.Point2LL) bool {
			return math.PointInPolygon2LL(p, hole)
		})
	case AirspaceVolumeCircle:
		return math.NMDistance2LL(p, a.Center) < a.Radius
	default:
		panic("unhandled AirspaceVolume type")
	}
}

func (a *AirspaceVolume) Below(p math.Point2LL, alt int) bool {
	if alt >= a.Floor {
		return false
	}

	switch a.Type {
	case AirspaceVolumePolygon:
		if a.PolygonBounds != nil && !a.PolygonBounds.Inside(p) {
			return false
		}
		if !math.PointInPolygon2LL(p, a.Vertices) {
			return false
		}
		return !util.SeqContainsFunc(slices.Values(a.Holes), func(hole []math.Point2LL) bool {
			return math.PointInPolygon2LL(p, hole)
		})
	case AirspaceVolumeCircle:
		return math.NMDistance2LL(p, a.Center) < a.Radius
	default:
		panic("unhandled AirspaceVolume type")
	}
}

func (a *AirspaceVolume) PostDeserialize(loc Locator, e *util.ErrorLogger) {
	if a.Id == "" {
		e.ErrorString(`must provide "id" with airspace volume`)
	}
	if len(a.Id) > 7 {
		e.ErrorString("airspace volume id %q cannot be more than 7 characters", a.Id)
	}
	if a.Description == "" {
		e.ErrorString(`must provide "description" with airspace volume`)
	}
	if a.Floor > a.Ceiling {
		e.ErrorString(`"floor" %d is above "ceiling" %d`, a.Floor, a.Ceiling)
	}
	switch a.Type {
	case AirspaceVolumeUnknown:
		e.ErrorString(`must provide "type" with airspace volume`)

	case AirspaceVolumePolygon:
		if len(a.Vertices) == 0 {
			var vstrs []string
			if a.VerticesStr.A != nil { // single string provided
				vstrs = strings.Fields(*a.VerticesStr.A)
			} else if a.VerticesStr.B != nil {
				vstrs = *a.VerticesStr.B
			}
			if len(vstrs) == 0 {
				e.ErrorString(`must provide "vertices" with "polygon" airspace volume`)
			} else if len(vstrs) < 3 {
				e.ErrorString(`must provide at least 3 "vertices" with "polygon" airspace volume`)
			}

			for _, s := range vstrs {
				if p, ok := loc.Locate(s); !ok {
					e.ErrorString(`unknown point %q in "vertices"`, s)
				} else {
					a.Vertices = append(a.Vertices, p)
				}
			}
		}

		b := math.Extent2DFromPoints(util.MapSlice(a.Vertices, func(p math.Point2LL) [2]float32 { return p }))
		a.PolygonBounds = &b

	case AirspaceVolumeCircle:
		if a.Radius == 0 {
			e.ErrorString(`must provide "radius" with "circle" airspace volume`)
		}
		if a.Center.IsZero() {
			e.ErrorString(`must provide "center" with "circle" airspace volume`)
		}
	}
}

type CRDARegion struct {
	Name             string  // set during deserialization from map key
	HeadingTolerance float32 `json:"heading_tolerance"`

	// Straight-line reference (mutually exclusive with ReferenceRoute)
	ReferenceLineHeading   float32       `json:"reference_heading"`
	ReferenceLineLength    float32       `json:"reference_length"`
	ReferencePointAltitude float32       `json:"reference_altitude"`
	ReferencePoint         math.Point2LL `json:"reference_point"`

	// Route-based reference (mutually exclusive with straight-line fields)
	ReferenceRoute string `json:"reference_route"`

	// lateral qualification region
	NearDistance  float32 `json:"near_distance"`
	NearHalfWidth float32 `json:"near_half_width"`
	FarHalfWidth  float32 `json:"far_half_width"`
	RegionLength  float32 `json:"region_length"`

	// vertical qualification region
	DescentPointDistance   float32 `json:"descent_distance"`
	DescentPointAltitude   float32 `json:"descent_altitude"`
	AboveAltitudeTolerance float32 `json:"above_altitude_tolerance"`
	BelowAltitudeTolerance float32 `json:"below_altitude_tolerance"`

	ScratchpadPatterns []string `json:"scratchpad_patterns"`

	// Computed during PostDeserialize
	Path              Path
	DistToConvergence float32 // distance from path end to convergence point
}

// CRDARoutePoint represents a single point in a CRDA reference route.
// Only fix names and /arc qualifiers are supported.
type CRDARoutePoint struct {
	Fix      string
	Location math.Point2LL
	Arc      *DMEArc
}

// parseCRDARoute parses a CRDA reference route string into route points.
// Only fix names (resolved via loc) and /arc qualifiers are supported;
// all other waypoint qualifiers are rejected.
func parseCRDARoute(s string, loc Locator, nmPerLongitude, magneticVariation float32, e *util.ErrorLogger) []CRDARoutePoint {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		e.ErrorString("reference route must have at least 2 waypoints")
		return nil
	}

	var points []CRDARoutePoint
	for _, field := range fields {
		components := strings.Split(field, "/")

		// Handle lat-long format like 4900N/05000W where "/" is part of the fix name
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
				components[0] += "/" + c1
				components = append(components[:1], components[2:]...)
			}
		}

		pt := CRDARoutePoint{Fix: components[0]}

		for _, mod := range components[1:] {
			if len(mod) == 0 {
				e.ErrorString("no modifier found after / in %q", field)
				continue
			}
			if len(mod) >= 4 && mod[:3] == "arc" {
				spec := mod[3:]
				rend := 0
				for rend < len(spec) &&
					((spec[rend] >= '0' && spec[rend] <= '9') || spec[rend] == '.') {
					rend++
				}
				if rend == 0 {
					e.ErrorString("%s: radius not found after /arc", mod)
					continue
				}
				v, err := strconv.ParseFloat(spec[:rend], 32)
				if err != nil {
					e.ErrorString("%s: invalid arc radius/length: %v", mod, err)
					continue
				}
				if rend == len(spec) {
					pt.Arc = &DMEArc{Length: float32(v)}
				} else {
					pt.Arc = &DMEArc{Fix: spec[rend:], Radius: float32(v)}
				}
			} else {
				e.ErrorString("%q: unsupported modifier in reference route (only /arc is allowed)", mod)
			}
		}

		e.Push("Fix " + pt.Fix)
		if pos, ok := loc.Locate(pt.Fix); !ok {
			e.ErrorString("unable to locate waypoint")
		} else {
			pt.Location = pos
		}
		e.Pop()

		points = append(points, pt)
	}

	// Initialize arcs: determine clockwise direction and resolve geometry
	for i := range points {
		if points[i].Arc == nil {
			continue
		}

		e.Push("Fix " + points[i].Fix)

		if i+1 == len(points) {
			e.ErrorString("can't have arc starting at the final waypoint")
			e.Pop()
			break
		}

		// Which way are we turning? Use previous or next-after-arc
		// waypoint to determine clockwise/counterclockwise.
		var v0, v1 [2]float32
		p0 := math.LL2NM(points[i].Location, nmPerLongitude)
		p1 := math.LL2NM(points[i+1].Location, nmPerLongitude)
		if i > 0 {
			v0 = math.Sub2f(p0, math.LL2NM(points[i-1].Location, nmPerLongitude))
			v1 = math.Sub2f(p1, p0)
		} else {
			if i+2 == len(points) {
				e.ErrorString("must have at least one waypoint before or after arc to determine its orientation")
				e.Pop()
				continue
			}
			v0 = math.Sub2f(p1, p0)
			v1 = math.Sub2f(math.LL2NM(points[i+2].Location, nmPerLongitude), p1)
		}
		x := v0[0]*v1[1] - v0[1]*v1[0]
		points[i].Arc.Clockwise = x < 0

		if !points[i].Arc.Initialize(loc, points[i].Location, points[i+1].Location, nmPerLongitude, magneticVariation, e) {
			points[i].Arc = nil
		}

		e.Pop()
	}

	return points
}

type ATPAVolume struct {
	Id                  string `json:"id"`
	ThresholdString     string `json:"runway_threshold"`
	Threshold           math.Point2LL
	Heading             float32  `json:"heading"`
	MaxHeadingDeviation float32  `json:"max_heading_deviation"`
	Floor               float32  `json:"floor"`
	Ceiling             float32  `json:"ceiling"`
	Length              float32  `json:"length"`
	LeftWidth           float32  `json:"left_width"`
	RightWidth          float32  `json:"right_width"`
	FilteredScratchpads []string `json:"filtered_scratchpads"`
	ExcludedScratchpads []string `json:"excluded_scratchpads"`
	Enable25nmApproach  bool     `json:"enable_2.5nm"`
	Dist25nmApproach    float32  `json:"2.5nm_distance"`
}

// CourseLine returns a polyline for drawing the course line of this CRDA region.
func (ar *CRDARegion) CourseLine(nmPerLongitude float32) []math.Point2LL {
	polyNM := ar.Path.Polyline()
	pts := make([]math.Point2LL, len(polyNM))
	for i, p := range polyNM {
		pts[i] = math.NM2LL(p, nmPerLongitude)
	}
	return pts
}

// QualificationPolygon returns the polygon outline of the qualification
// region for drawing.
func (ar *CRDARegion) QualificationPolygon(nmPerLongitude float32) []math.Point2LL {
	// Sample points along the path within the qualification region
	// [NearDistance, NearDistance+RegionLength], building left and right edges
	steps := max(2, int(ar.RegionLength*2)) // ~0.5nm resolution
	left := make([][2]float32, steps+1)
	right := make([][2]float32, steps+1)

	for i := range steps + 1 {
		t := float32(i) / float32(steps)
		d := ar.NearDistance + t*ar.RegionLength
		halfWidth := math.Lerp(t, ar.NearHalfWidth, ar.FarHalfWidth)

		pt, hdg := ar.Path.PointAtDistance(d)
		// Perpendicular: left of heading
		perpRad := math.Radians(hdg - 90)
		perpVec := math.SinCos(perpRad)

		left[i] = math.Add2f(pt, math.Scale2f(perpVec, halfWidth))
		right[i] = math.Add2f(pt, math.Scale2f(perpVec, -halfWidth))
	}

	// Build polygon: left edge forward, right edge backward
	var poly []math.Point2LL
	for _, p := range left {
		poly = append(poly, math.NM2LL(p, nmPerLongitude))
	}
	for i := len(right) - 1; i >= 0; i-- {
		poly = append(poly, math.NM2LL(right[i], nmPerLongitude))
	}
	return poly
}

type ControllerAirspaceVolume struct {
	LowerLimit    int               `json:"lower"`
	UpperLimit    int               `json:"upper"`
	Boundaries    [][]math.Point2LL `json:"boundary_polylines"` // not in JSON
	BoundaryNames []string          `json:"boundaries"`
	Label         string            `json:"label"`
	LabelPosition math.Point2LL     `json:"label_position"`
}

func InAirspace(p math.Point2LL, alt float32, volumes []ControllerAirspaceVolume) (bool, [][2]int) {
	var altRanges [][2]int
	for _, v := range volumes {
		inside := false
		for _, pts := range v.Boundaries {
			if math.PointInPolygon2LL(p, pts) {
				inside = !inside
			}
		}
		if inside {
			altRanges = append(altRanges, [2]int{v.LowerLimit, v.UpperLimit})
		}
	}

	// Sort altitude ranges and then merge ones that have 1000 foot separation
	sort.Slice(altRanges, func(i, j int) bool { return altRanges[i][0] < altRanges[j][0] })
	var mergedAlts [][2]int
	i := 0
	inside := false
	for i < len(altRanges) {
		low := altRanges[i][0]
		high := altRanges[i][1]

		for i+1 < len(altRanges) {
			if altRanges[i+1][0]-high <= 1000 {
				// merge
				high = altRanges[i+1][1]
				i++
			} else {
				break
			}
		}

		// 10 feet of slop for rounding error
		inside = inside || (int(alt)+10 >= low && int(alt)-10 <= high)

		mergedAlts = append(mergedAlts, [2]int{low, high})
		i++
	}

	return inside, mergedAlts
}

///////////////////////////////////////////////////////////////////////////
// RestrictionArea

// This many adapted and then this many user-defined
const MaxRestrictionAreas = 100

type RestrictionArea struct {
	Title        string        `json:"title"`
	Text         [2]string     `json:"text"`
	BlinkingText bool          `json:"blinking_text"`
	HideId       bool          `json:"hide_id"`
	TextPosition math.Point2LL `json:"text_position"`
	CircleCenter math.Point2LL `json:"circle_center"`
	CircleRadius float32       `json:"circle_radius"`
	VerticesUser WaypointArray `json:"vertices"`
	Vertices     [][]math.Point2LL
	Closed       bool `json:"closed"`
	Shaded       bool `json:"shade_region"`
	Color        int  `json:"color"`

	Tris    [][3]math.Point2LL
	Deleted bool
}

type Airspace struct {
	Boundaries map[string][]math.Point2LL            `json:"boundaries"`
	Volumes    map[string][]ControllerAirspaceVolume `json:"volumes"`
}

func RestrictionAreaFromTFR(tfr TFR) RestrictionArea {
	ra := RestrictionArea{
		Title:    tfr.LocalName,
		Vertices: deep.MustCopy(tfr.Points),
	}

	if len(ra.Title) > 32 {
		ra.Title = ra.Title[:32]
	}

	ra.HideId = true
	ra.Closed = true
	ra.Shaded = true // ??
	ra.TextPosition = ra.AverageVertexPosition()

	ra.UpdateTriangles()

	return ra
}

func (ra *RestrictionArea) AverageVertexPosition() math.Point2LL {
	var c math.Point2LL
	var n float32
	for _, loop := range ra.Vertices {
		n += float32(len(loop))
		for _, v := range loop {
			c = math.Add2f(c, v)
		}
	}
	return math.Scale2f(c, max(1, 1/n)) // avoid 1/0 and return (0,0) if there are no verts.
}

func (ra *RestrictionArea) UpdateTriangles() {
	if !ra.Closed || !ra.Shaded {
		ra.Tris = nil
		return
	}

	clear(ra.Tris)
	for _, loop := range ra.Vertices {
		if len(loop) < 3 {
			continue
		}

		vertices := make([]earcut.Vertex, len(loop))
		for i, v := range loop {
			vertices[i].P = [2]float64{float64(v[0]), float64(v[1])}
		}

		for _, tri := range earcut.Triangulate(earcut.Polygon{Rings: [][]earcut.Vertex{vertices}}) {
			var v32 [3]math.Point2LL
			for i, v64 := range tri.Vertices {
				v32[i] = [2]float32{float32(v64.P[0]), float32(v64.P[1])}
			}
			ra.Tris = append(ra.Tris, v32)
		}
	}
}

func (ra *RestrictionArea) MoveTo(p math.Point2LL) {
	if ra.CircleRadius > 0 {
		// Circle
		delta := math.Sub2f(p, ra.CircleCenter)
		ra.CircleCenter = p
		ra.TextPosition = math.Add2f(ra.TextPosition, delta)
	} else {
		pc := ra.TextPosition
		if pc.IsZero() {
			pc = ra.AverageVertexPosition()
		}
		delta := math.Sub2f(p, pc)
		ra.TextPosition = p

		for _, loop := range ra.Vertices {
			for i := range loop {
				loop[i] = math.Add2f(loop[i], delta)
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// VFRReportingPoint

type VFRReportingPoint struct {
	Description string        `json:"description"`
	Location    math.Point2LL `json:"location"`
}

func (rp *VFRReportingPoint) PostDeserialize(loc Locator, controllers map[ControlPosition]*Controller, e *util.ErrorLogger) {
	if rp.Description == "" {
		e.ErrorString(`must specify "description" with reporting point`)
	}
	if rp.Location.IsZero() {
		e.ErrorString(`must specify "location" with reporting point`)
	}
}
