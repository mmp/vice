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
	LDAApproach
	NDBApproach
)

func (at ApproachType) String() string {
	return []string{"Unknown", "ILS", "RNAV", "Charted Visual", "Visual", "Localizer", "VOR", "LDA", "NDB"}[at]
}

// HasLocalizer reports whether the approach is flown on a localizer: a
// straight course to intercept, which may be offset from the runway.
func (at ApproachType) HasLocalizer() bool {
	return at == ILSApproach || at == LocalizerApproach || at == LDAApproach
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
	case LDAApproach:
		return []byte(`"LDA"`), nil
	case NDBApproach:
		return []byte(`"NDB"`), nil
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

	case `"LDA"`:
		*at = LDAApproach
		return nil

	case `"NDB"`:
		*at = NDBApproach
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

	// CirclingOnly is set for CIFP approaches that have only circle-to-land
	// minimums.
	CirclingOnly bool

	// ReportingPoints are the charted visual landmarks that a pilot
	// expecting a charted visual approach may report in sight, keyed by the
	// identifier the RP command names them by.
	ReportingPoints map[string]*ReportingPoint `json:"reporting_points,omitempty"`

	LocalizerLocation math.Point2LL

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

// DefaultFullName is the name of an approach that neither the scenario nor
// the CIFP names: its type and runway.
func (ap Approach) DefaultFullName() string {
	return ap.Type.String() + " Runway " + ap.Runway
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
		thresholdWP := Waypoint{
			Fix:      "_" + ap.Runway + "_THRESHOLD",
			Location: rwy.Threshold,
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

// FinalApproachCourseLine returns two points along the approach's final
// approach course, in the direction of landing. An approach flown on a
// localizer follows the line from its FAF to the localizer antenna, since a
// localizer may be offset from its runway: KSLC 35's LDA is 5 degrees off,
// and PHNL 26L's crosses 0.9nm from the threshold on its way to a visual
// turn to the runway. A fix before the FAF may sit off the course, and a
// step-down fix after it gives too short a baseline. Hand-written approaches
// have no localizer data, so theirs follow the line from the FAF to the
// threshold. Other approaches follow the runway's extended centerline, as
// their ATPA volumes do.
func (ap *Approach) FinalApproachCourseLine(nmPerLongitude, magneticVariation float32) [2]math.Point2LL {
	if ap.Type.HasLocalizer() {
		if wps, _ := ap.FAFSegment(nmPerLongitude, magneticVariation); wps != nil {
			if i := slices.IndexFunc(wps, func(wp Waypoint) bool { return wp.FAF() }); i != -1 {
				if !ap.LocalizerLocation.IsZero() {
					return [2]math.Point2LL{wps[i].Location, ap.LocalizerLocation}
				}
				return [2]math.Point2LL{wps[i].Location, ap.Threshold}
			}
		}
	}
	return [2]math.Point2LL{ap.Threshold, ap.OppositeThreshold}
}

// FinalApproachCourse returns the true course of the approach's final
// approach course.
func (ap *Approach) FinalApproachCourse(nmPerLongitude, magneticVariation float32) math.TrueHeading {
	line := ap.FinalApproachCourseLine(nmPerLongitude, magneticVariation)
	return math.Heading2LL(line[0], line[1], nmPerLongitude)
}

// maxLocalizerOffset is how far, in degrees, an ILS or localizer approach's
// final approach course may be from its runway: a localizer offset further
// than that is an LDA. The largest offset among the CIFP's ILS and localizer
// approaches is 3.1 degrees (KFLG's ILS 21 and KHWD's localizer 28L, among
// others).
const maxLocalizerOffset = 3.5

// checkCourseAlignment reports an ILS or localizer approach whose final
// approach course isn't aligned with its runway. Those are usually hand-written
// approaches of some other kind that were given the type so that aircraft
// would intercept them like a localizer; an aircraft cleared for one would
// otherwise join the runway's extended centerline and then turn to the fixes.
func (ap *Approach) checkCourseAlignment(nmPerLongitude, magneticVariation float32, e *util.ErrorLogger) {
	if (ap.Type != ILSApproach && ap.Type != LocalizerApproach) || ap.Threshold.IsZero() || ap.OppositeThreshold.IsZero() {
		return
	}
	offset := math.HeadingDifference(ap.FinalApproachCourse(nmPerLongitude, magneticVariation), ap.RunwayHeading(nmPerLongitude))
	if offset > maxLocalizerOffset {
		e.ErrorString(`final approach course is %.0f degrees off runway %s; %s approaches must be within %.1f degrees `+
			`of the runway. Use "LDA" for an offset localizer, or the approach's actual type`, offset, ap.Runway, ap.Type, maxLocalizerOffset)
	}
}

// RunwayHeading returns the true heading of the approach's runway, from its
// threshold to the opposite runway's.
func (ap *Approach) RunwayHeading(nmPerLongitude float32) math.TrueHeading {
	return math.Heading2LL(ap.Threshold, ap.OppositeThreshold, nmPerLongitude)
}
