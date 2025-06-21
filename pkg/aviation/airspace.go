// pkg/aviation/airspace.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

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
		return []byte("\"polygon\""), nil
	case AirspaceVolumeCircle:
		return []byte("\"circle\""), nil
	default:
		return nil, fmt.Errorf("%d: unknown airspace volume type", *t)
	}
}

func (t *AirspaceVolumeType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "\"polygon\"":
		*t = AirspaceVolumePolygon
		return nil
	case "\"circle\"":
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
		for _, hole := range a.Holes {
			if math.PointInPolygon2LL(p, hole) {
				return false
			}
		}
		return true
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
		for _, hole := range a.Holes {
			if math.PointInPolygon2LL(p, hole) {
				return false
			}
		}
		return true
	case AirspaceVolumeCircle:
		return math.NMDistance2LL(p, a.Center) < a.Radius
	default:
		panic("unhandled AirspaceVolume type")
	}
}

func (a *AirspaceVolume) GenerateDrawCommands(cb *renderer.CommandBuffer, nmPerLongitude float32) {
	ld := renderer.GetLinesDrawBuilder()

	switch a.Type {
	case AirspaceVolumePolygon:
		var v [][2]float32
		for _, vtx := range a.Vertices {
			v = append(v, [2]float32(vtx))
		}
		ld.AddLineLoop(v)

		for _, h := range a.Holes {
			var v [][2]float32
			for _, vtx := range h {
				v = append(v, [2]float32(vtx))
			}
			ld.AddLineLoop(v)
		}
	case AirspaceVolumeCircle:
		ld.AddLatLongCircle(a.Center, nmPerLongitude, a.Radius, 360)
	default:
		panic("unhandled AirspaceVolume type")
	}

	ld.GenerateCommands(cb)
	renderer.ReturnLinesDrawBuilder(ld)
}

func (a *AirspaceVolume) PostDeserialize(loc Locator, e *util.ErrorLogger) {
	if a.Id == "" {
		e.ErrorString("must provide \"id\" with airspace volume")
	}
	if len(a.Id) > 7 {
		e.ErrorString("airspace volume id %q cannot be more than 7 characters", a.Id)
	}
	if a.Description == "" {
		e.ErrorString("must provide \"description\" with airspace volume")
	}
	if a.Floor > a.Ceiling {
		e.ErrorString("\"floor\" %d is above \"ceiling\" %d", a.Floor, a.Ceiling)
	}
	switch a.Type {
	case AirspaceVolumeUnknown:
		e.ErrorString("must provide \"type\" with airspace volume")

	case AirspaceVolumePolygon:
		if len(a.Vertices) == 0 {
			var vstrs []string
			if a.VerticesStr.A != nil { // single string provided
				vstrs = strings.Fields(*a.VerticesStr.A)
			} else {
				vstrs = *a.VerticesStr.B
			}
			if len(vstrs) == 0 {
				e.ErrorString("must provide \"vertices\" with \"polygon\" airspace volume")
			} else if len(vstrs) < 3 {
				e.ErrorString("must provide at least 3 \"vertices\" with \"polygon\" airspace volume")
			}

			for _, s := range vstrs {
				if p, ok := loc.Locate(s); !ok {
					e.ErrorString("unknown point %q in \"vertices\"", s)
				} else {
					a.Vertices = append(a.Vertices, p)
				}
			}
		}

		b := math.Extent2DFromPoints(util.MapSlice(a.Vertices, func(p math.Point2LL) [2]float32 { return p }))
		a.PolygonBounds = &b

	case AirspaceVolumeCircle:
		if a.Radius == 0 {
			e.ErrorString("must provide \"radius\" with \"circle\" airspace volume")
		}
		if a.Center.IsZero() {
			e.ErrorString("must provide \"center\" with \"circle\" airspace volume")
		}
	}
}

type ApproachRegion struct {
	Runway           string  // set during deserialization
	HeadingTolerance float32 `json:"heading_tolerance"`

	ReferenceLineHeading   float32       `json:"reference_heading"`
	ReferenceLineLength    float32       `json:"reference_length"`
	ReferencePointAltitude float32       `json:"reference_altitude"`
	ReferencePoint         math.Point2LL `json:"reference_point"`

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

// returns a point along the reference line with given distance from the
// reference point, in nm coordinates.
func (ar *ApproachRegion) referenceLinePoint(dist, nmPerLongitude, magneticVariation float32) [2]float32 {
	hdg := math.Radians(ar.ReferenceLineHeading + 180 - magneticVariation)
	v := [2]float32{math.Sin(hdg), math.Cos(hdg)}
	pref := math.LL2NM(ar.ReferencePoint, nmPerLongitude)
	return math.Add2f(pref, math.Scale2f(v, dist))
}

func (ar *ApproachRegion) NearPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) FarPoint(nmPerLongitude, magneticVariation float32) [2]float32 {
	return ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
}

func (ar *ApproachRegion) GetLateralGeometry(nmPerLongitude, magneticVariation float32) (line [2]math.Point2LL, quad [4]math.Point2LL) {
	// Start with the reference line
	p0 := ar.referenceLinePoint(0, nmPerLongitude, magneticVariation)
	p1 := ar.referenceLinePoint(ar.ReferenceLineLength, nmPerLongitude, magneticVariation)
	line = [2]math.Point2LL{math.NM2LL(p0, nmPerLongitude), math.NM2LL(p1, nmPerLongitude)}

	// Get the unit vector perpendicular to the reference line
	v := math.Normalize2f(math.Sub2f(p1, p0))
	vperp := [2]float32{-v[1], v[0]}

	pNear := ar.referenceLinePoint(ar.NearDistance, nmPerLongitude, magneticVariation)
	pFar := ar.referenceLinePoint(ar.NearDistance+ar.RegionLength, nmPerLongitude, magneticVariation)
	q0 := math.Add2f(pNear, math.Scale2f(vperp, ar.NearHalfWidth))
	q1 := math.Add2f(pFar, math.Scale2f(vperp, ar.FarHalfWidth))
	q2 := math.Add2f(pFar, math.Scale2f(vperp, -ar.FarHalfWidth))
	q3 := math.Add2f(pNear, math.Scale2f(vperp, -ar.NearHalfWidth))
	quad = [4]math.Point2LL{math.NM2LL(q0, nmPerLongitude), math.NM2LL(q1, nmPerLongitude),
		math.NM2LL(q2, nmPerLongitude), math.NM2LL(q3, nmPerLongitude)}

	return
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

func (rp *VFRReportingPoint) PostDeserialize(loc Locator, controllers map[string]*Controller, e *util.ErrorLogger) {
	if rp.Description == "" {
		e.ErrorString("must specify \"description\" with reporting point")
	}
	if rp.Location.IsZero() {
		e.ErrorString("must specify \"location\" with reporting point")
	}
}
