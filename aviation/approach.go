// aviation/approach.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type ApproachType int

const (
	UnknownApproach ApproachType = iota
	ILSApproach
	RNAVApproach
	ChartedVisualApproach
	VisualApproach
	LocalizerApproach
	VORApproach
)

func (at ApproachType) String() string {
	return []string{"Unknown", "ILS", "RNAV", "Charted Visual", "Visual", "Localizer", "VOR"}[at]
}

func (at ApproachType) MarshalJSON() ([]byte, error) {
	switch at {
	case UnknownApproach:
		return []byte(`"Unknown"`), nil
	case ILSApproach:
		return []byte(`"ILS"`), nil
	case RNAVApproach:
		return []byte(`"RNAV"`), nil
	case ChartedVisualApproach:
		return []byte(`"ChartedVisual"`), nil
	case VisualApproach:
		return []byte(`"Visual"`), nil
	case LocalizerApproach:
		return []byte(`"Localizer"`), nil
	case VORApproach:
		return []byte(`"VOR"`), nil
	default:
		return nil, fmt.Errorf("unhandled approach type in MarshalJSON()")
	}
}

func (at *ApproachType) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"Unknown"`:
		*at = UnknownApproach
		return nil

	case `"ILS"`:
		*at = ILSApproach
		return nil

	case `"RNAV"`:
		*at = RNAVApproach
		return nil

	case `"ChartedVisual"`:
		*at = ChartedVisualApproach
		return nil

	case `"Visual"`:
		*at = VisualApproach
		return nil

	case `"Localizer"`:
		*at = LocalizerApproach
		return nil

	case `"VOR"`:
		*at = VORApproach
		return nil

	default:
		return fmt.Errorf("%s: unknown approach_type", string(b))
	}
}

type Approach struct {
	Id        string          `json:"cifp_id"`
	FullName  string          `json:"full_name"`
	Type      ApproachType    `json:"type"`
	Runway    string          `json:"runway"`
	Waypoints []WaypointArray `json:"waypoints"`

	// ReportingPoints are the charted visual landmarks that a pilot
	// expecting a charted visual approach may report in sight, keyed by the
	// identifier the RP command names them by.
	ReportingPoints map[string]*ReportingPoint `json:"reporting_points,omitempty"`

	// Set in Airport Finalize()
	Threshold         math.Point2LL
	OppositeThreshold math.Point2LL
}

// ReportingPoint is a landmark on a charted visual approach, e.g. the
// Dumbarton bridge.
type ReportingPoint struct {
	// Names are the names pilots and controllers use for the landmark
	// ("Dumbarton bridge", "bridge"). The first is its full name, which
	// pilots use when they bring it up themselves.
	Names    []string         `json:"names"`
	Location ScenarioPoint2LL `json:"location"`

	// Set in Airport Finalize() from its key in the approach's reporting
	// points.
	Id string
}

// Name returns the reporting point's full name.
func (rp ReportingPoint) Name() string {
	return rp.Names[0]
}

// maxReportingPointDistance bounds how far from its airport (in nm) a
// reporting point may be; it catches mistyped locations.
const maxReportingPointDistance = 50

// finalizeReportingPoints resolves the locations of the approach's reporting
// points and checks them, given the location of its airport.
func (ap *Approach) finalizeReportingPoints(loc Locator, airport math.Point2LL, e *util.ErrorLogger) {
	if len(ap.ReportingPoints) > 0 && ap.Type != ChartedVisualApproach {
		e.ErrorString(`"reporting_points" can only be given for "ChartedVisual" approaches`)
	}

	// Identifiers and names each have to pick out a single reporting point:
	// identifiers for RP commands, which are typed in upper case, and names
	// for speech recognition.
	ids := make(map[string]string)   // lowercased identifier -> identifier
	named := make(map[string]string) // lowercased name -> identifier
	for id, rp := range util.SortedMap(ap.ReportingPoints) {
		e.Push("Reporting point " + id)
		rp.Id = id

		if id == "" || strings.ContainsFunc(id, unicode.IsSpace) || strings.Contains(id, "/") {
			e.ErrorString(`identifier must be a single word without "/"`)
		}
		if other, ok := ids[strings.ToLower(id)]; ok {
			e.ErrorString("identifier differs from %q only in case", other)
		}
		ids[strings.ToLower(id)] = id

		if len(rp.Names) == 0 {
			e.ErrorString(`Must specify "names"`)
		} else if slices.Contains(rp.Names, "") {
			e.ErrorString(`"names" cannot include an empty name`)
		}
		for _, name := range rp.Names {
			if other, ok := named[strings.ToLower(name)]; ok && other != id {
				e.ErrorString("%q is also a name of reporting point %s", name, other)
			}
			named[strings.ToLower(name)] = id
		}

		if rp.Location.String == "" && rp.Location.IsZero() {
			e.ErrorString(`Must specify "location"`)
		} else {
			rp.Location.Resolve(loc, "location", e)
			if d := math.NMDistance2LL(rp.Location.Point2LL, airport); !rp.Location.IsZero() && d > maxReportingPointDistance {
				e.ErrorString(`"location" is %.0f nm from the airport; it must be within %d nm`,
					d, maxReportingPointDistance)
			}
		}
		e.Pop()
	}
}

// DefaultFullName is the approach's name as it is charted, worked out from
// its CIFP identifier: RZ22L is "RNAV Z Runway 22L".
func (ap Approach) DefaultFullName() string {
	name := ap.Type.String() + " "
	if len(ap.Id) >= 3 && ap.Id[1] >= 'W' && ap.Id[1] <= 'Z' {
		name += string(ap.Id[1]) + " "
	}
	if len(ap.Id) >= 3 && ap.Id[0] == 'G' {
		name += "GPS "
	}
	return name + "Runway " + ap.Runway
}

// InitializeWaypoints resolves waypoint locations and adds the runway
// threshold waypoint to each route. It also sets the OnApproach flag,
// Threshold, and OppositeThreshold fields.
func (ap *Approach) InitializeWaypoints(icao ICAOAirportCode, db Database, nmPerLongitude float32,
	magneticVariation float32, e *util.ErrorLogger) {
	rwy, ok := LookupRunway(db, icao, ap.Runway)
	if !ok {
		e.ErrorString(`"runway" %q is unknown. Options: %s`, ap.Runway,
			db.ValidRunways(icao))
	}
	ap.Threshold = rwy.Threshold

	if opp, ok := LookupOppositeRunway(db, icao, ap.Runway); ok {
		ap.OppositeThreshold = opp.Threshold
	} else {
		e.ErrorString("no opposite runway found for %q\n", ap.Runway)
	}

	for i := range ap.Waypoints {
		ap.Waypoints[i] =
			ap.Waypoints[i].InitializeLocations(db, nmPerLongitude, magneticVariation, false, e)

		// Add the final fix at the runway threshold.
		alt := rwy.Elevation + rwy.ThresholdCrossingHeight
		threshold := math.Offset2LL(rwy.Threshold, math.MagneticToTrue(rwy.Heading, magneticVariation),
			rwy.DisplacedThresholdDistance, nmPerLongitude)

		thresholdWP := Waypoint{
			Fix:      "_" + ap.Runway + "_THRESHOLD",
			Location: threshold,
			Flags:    WaypointFlagFlyOver,
		}
		thresholdWP.MergeActions(WaypointActions{Land: true})
		thresholdWP.SetAltitudeRestriction(MakeAtAltitudeRestriction(float32(alt)))
		ap.Waypoints[i] = append(ap.Waypoints[i], thresholdWP)

		for j := range ap.Waypoints[i] {
			ap.Waypoints[i][j].SetOnApproach(true)
		}
	}
}

// Find the FAF: return the corresponding waypoint array and the index of the FAF within it.
func (ap *Approach) FAFSegment(nmPerLongitude, magneticVariation float32) (WaypointArray, int) {
	// For approaches with multiple segments, want the segment that is most
	// closely aligned with the runway.
	rwyHdg := ap.RunwayHeading(nmPerLongitude)

	bestWpsIdx, bestWpsFAFIdx := -1, -1
	minDiff := float32(360)

	for i, wps := range ap.Waypoints {
		fafIdx := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF() })
		if fafIdx == -1 {
			// no FAF on this segment(?)
			continue
		}

		if wps[fafIdx].IF() || wps[fafIdx].IAF() {
			// Likely a HILPT; don't go outbound for the approach course as
			// it may be some random feeder fix.
			fafIdx++
		}

		// Go from the previous fix to the FAF if possible.
		if fafIdx == 0 {
			fafIdx++
		}

		hdg := math.Heading2LL(wps[fafIdx-1].Location, wps[fafIdx].Location, nmPerLongitude)

		diff := math.HeadingDifference(hdg, rwyHdg)
		if diff < minDiff {
			minDiff = diff
			bestWpsIdx = i
			bestWpsFAFIdx = fafIdx
		}
	}

	if bestWpsIdx != -1 {
		return ap.Waypoints[bestWpsIdx], bestWpsFAFIdx
	} else {
		// Shouldn't ever happen since we ensure there is a FAF for each approach.
		return nil, 0
	}
}

func (ap *Approach) ExtendedCenterline(nmPerLongitude, magneticVariation float32) [2]math.Point2LL {
	return [2]math.Point2LL{ap.Threshold, ap.OppositeThreshold}
}

func (ap *Approach) RunwayHeading(nmPerLongitude float32) math.TrueHeading {
	return math.Heading2LL(ap.Threshold, ap.OppositeThreshold, nmPerLongitude)
}
