// aviation/crda.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"slices"
	"strings"

	"github.com/mmp/vice/math"
)

// CRDAPair describes a one-directional ghosting relationship between two
// CRDA regions. Aircraft flying through SourceRegion's qualification volume
// have ghost data blocks plotted on GhostRegion's centerline; to ghost in
// both directions, define two pairs with the roles swapped.
type CRDAPair struct {
	SourceRegion             string  `json:"source_region"`
	GhostRegion              string  `json:"ghost_region"`
	SourceLeaderDirectionStr string  `json:"source_leader_direction"`
	GhostLeaderDirectionStr  string  `json:"ghost_leader_direction"`
	TieSymbol                string  `json:"tie_symbol"`
	StaggerSymbol            string  `json:"stagger_symbol"`
	TieOffset                float32 `json:"tie_offset"`

	// Set during deserialize.
	SourceLeaderDirection math.CardinalOrdinalDirection
	GhostLeaderDirection  math.CardinalOrdinalDirection
	ConvergencePoint      math.Point2LL
}

type GhostTrack struct {
	ADSBCallsign        ADSBCallsign
	Position            math.Point2LL
	Groundspeed         int
	LeaderLineDirection math.CardinalOrdinalDirection
	TrackId             string
}

func (ar *CRDARegion) Inside(p math.Point2LL, alt float32, nmPerLongitude float32) (lateral, vertical bool) {
	pNM := math.LL2NM(p, nmPerLongitude)
	dist, perpOffset, _ := ar.Path.Project(pNM)

	// Lateral check: must be within [NearDistance, NearDistance+RegionLength]
	// and within interpolated half-width at that distance
	if dist < ar.NearDistance || dist > ar.NearDistance+ar.RegionLength {
		return
	}
	t := (dist - ar.NearDistance) / ar.RegionLength
	halfWidth := math.Lerp(t, ar.NearHalfWidth, ar.FarHalfWidth)
	if math.Abs(perpOffset) > halfWidth {
		return
	}
	lateral = true

	// Vertical check
	if dist > ar.DescentPointDistance {
		vertical = alt <= ar.DescentPointAltitude+ar.AboveAltitudeTolerance &&
			alt >= ar.DescentPointAltitude-ar.BelowAltitudeTolerance
	} else if ar.DescentPointDistance > ar.NearDistance {
		vt := (dist - ar.NearDistance) / (ar.DescentPointDistance - ar.NearDistance)
		approachAlt := math.Lerp(vt, ar.ReferencePointAltitude, ar.DescentPointAltitude)
		vertical = alt <= approachAlt+ar.AboveAltitudeTolerance &&
			alt >= approachAlt-ar.BelowAltitudeTolerance
	}
	return
}

func (ar *CRDARegion) TryMakeGhost(trk RadarTrack, heading float32,
	scratchpad string, forceGhost bool, offset float32, leaderDirection math.CardinalOrdinalDirection,
	nmPerLongitude float32, other *CRDARegion) *GhostTrack {
	// Start with lateral extent since even if it's forced, the aircraft still must be inside it.
	lat, vert := ar.Inside(trk.Location, float32(trk.TrueAltitude), nmPerLongitude)
	if !lat {
		return nil
	}

	if !forceGhost {
		// Heading must be in range
		pNM := math.LL2NM(trk.Location, nmPerLongitude)
		_, _, pathHeading := ar.Path.Project(pNM)
		if math.HeadingDifference(heading, pathHeading) > ar.HeadingTolerance {
			return nil
		}

		// Check vertical extent
		if !vert {
			return nil
		}

		if len(ar.ScratchpadPatterns) > 0 {
			if !slices.ContainsFunc(ar.ScratchpadPatterns,
				func(pat string) bool { return strings.Contains(scratchpad, pat) }) {
				return nil
			}
		}
	}

	// Project aircraft onto source path
	pNM := math.LL2NM(trk.Location, nmPerLongitude)
	pathDist, perpOffset, _ := ar.Path.Project(pNM)

	// Compute distance from convergence point
	convDist := ar.DistToConvergence + (ar.Path.Length - pathDist)

	// Map to target path: find the point at the same convergence distance
	targetDist := other.Path.Length - convDist + other.DistToConvergence
	ghostPt, ghostHeading := other.Path.PointAtDistance(targetDist)

	// Apply perpendicular offset (preserve aircraft's offset from centerline)
	perpRad := math.Radians(ghostHeading - 90)
	perpVec := math.SinCos(perpRad)
	ghostPt = math.Add2f(ghostPt, math.Scale2f(perpVec, perpOffset))

	// Apply tie offset along forward direction
	if offset != 0 {
		fwdRad := math.Radians(ghostHeading)
		fwdVec := math.SinCos(fwdRad)
		ghostPt = math.Add2f(ghostPt, math.Scale2f(fwdVec, offset))
	}

	return &GhostTrack{
		ADSBCallsign:        trk.ADSBCallsign,
		Position:            math.NM2LL(ghostPt, nmPerLongitude),
		Groundspeed:         int(trk.Groundspeed),
		LeaderLineDirection: leaderDirection,
	}
}

func (a *ATPAVolume) Inside(p math.Point2LL, alt float32, hdg math.MagneticHeading, nmPerLongitude, magneticVariation float32) bool {
	if alt < a.Floor || alt > a.Ceiling {
		return false
	}
	if math.HeadingDifference(hdg, a.Heading) > a.MaxHeadingDeviation {
		return false
	}

	rect := a.GetRect(nmPerLongitude, magneticVariation)
	return math.PointInPolygon2LL(p, rect[:])
}

func (a *ATPAVolume) GetRect(nmPerLongitude, magneticVariation float32) [4]math.Point2LL {
	// Segment along the approach course
	p0 := math.LL2NM(a.Threshold.Point2LL, nmPerLongitude)
	hdg := float32(math.MagneticToTrue(math.OppositeHeading(a.Heading), magneticVariation))
	v := math.SinCos(math.Radians(hdg))
	p1 := math.Add2f(p0, math.Scale2f(v, a.Length))

	vp := [2]float32{-v[1], v[0]} // perp
	left, right := a.LeftWidth/math.NauticalMilesToFeet, a.RightWidth/math.NauticalMilesToFeet

	quad := [4][2]float32{
		math.Add2f(p0, math.Scale2f(vp, -left)), math.Add2f(p1, math.Scale2f(vp, -left)),
		math.Add2f(p1, math.Scale2f(vp, right)), math.Add2f(p0, math.Scale2f(vp, right))}
	return [4]math.Point2LL{
		math.NM2LL(quad[0], nmPerLongitude), math.NM2LL(quad[1], nmPerLongitude),
		math.NM2LL(quad[2], nmPerLongitude), math.NM2LL(quad[3], nmPerLongitude)}
}
