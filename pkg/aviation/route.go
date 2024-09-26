// pkg/aviation/route.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// Waypoint

type Waypoint struct {
	Fix                 string               `json:"fix"`
	Location            math.Point2LL        // not provided in scenario JSON; derived from fix
	AltitudeRestriction *AltitudeRestriction `json:"altitude_restriction,omitempty"`
	Speed               int                  `json:"speed,omitempty"`
	Heading             int                  `json:"heading,omitempty"` // outbound heading after waypoint
	ProcedureTurn       *ProcedureTurn       `json:"pt,omitempty"`
	NoPT                bool                 `json:"nopt,omitempty"`
	Handoff             bool                 `json:"handoff,omitempty"`
	PointOut            string               `json:"pointout,omitempty"`
	FlyOver             bool                 `json:"flyover,omitempty"`
	Delete              bool                 `json:"delete,omitempty"`
	Arc                 *DMEArc              `json:"arc,omitempty"`
	IAF, IF, FAF        bool                 // not provided in scenario JSON; derived from fix
	Airway              string               // when parsing waypoints, this is set if we're on an airway after the fix
	OnSID, OnSTAR       bool                 // set during deserialization
}

func (wp Waypoint) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("fix", wp.Fix)}
	if wp.AltitudeRestriction != nil {
		attrs = append(attrs, slog.Any("altitude_restriction", wp.AltitudeRestriction))
	}
	if wp.Speed != 0 {
		attrs = append(attrs, slog.Int("speed", wp.Speed))
	}
	if wp.Heading != 0 {
		attrs = append(attrs, slog.Int("heading", wp.Heading))
	}
	if wp.ProcedureTurn != nil {
		attrs = append(attrs, slog.Any("procedure_turn", wp.ProcedureTurn))
	}
	if wp.IAF {
		attrs = append(attrs, slog.Bool("IAF", wp.IAF))
	}
	if wp.IF {
		attrs = append(attrs, slog.Bool("IF", wp.IF))
	}
	if wp.FAF {
		attrs = append(attrs, slog.Bool("FAF", wp.FAF))
	}
	if wp.NoPT {
		attrs = append(attrs, slog.Bool("no_pt", wp.NoPT))
	}
	if wp.Handoff {
		attrs = append(attrs, slog.Bool("handoff", wp.Handoff))
	}
	if wp.PointOut != "" {
		attrs = append(attrs, slog.String("pointout", wp.PointOut))
	}
	if wp.FlyOver {
		attrs = append(attrs, slog.Bool("fly_over", wp.FlyOver))
	}
	if wp.Delete {
		attrs = append(attrs, slog.Bool("delete", wp.Delete))
	}
	if wp.Arc != nil {
		attrs = append(attrs, slog.Any("arc", wp.Arc))
	}
	if wp.Airway != "" {
		attrs = append(attrs, slog.String("airway", wp.Airway))
	}
	if wp.OnSID {
		attrs = append(attrs, slog.Bool("on_sid", wp.OnSID))
	}
	if wp.OnSTAR {
		attrs = append(attrs, slog.Bool("on_star", wp.OnSTAR))
	}

	return slog.GroupValue(attrs...)
}

func (wp *Waypoint) ETA(p math.Point2LL, gs float32) time.Duration {
	dist := math.NMDistance2LL(p, wp.Location)
	eta := dist / gs
	return time.Duration(eta * float32(time.Hour))
}

///////////////////////////////////////////////////////////////////////////
// WaypointArray

type WaypointArray []Waypoint

func (wslice WaypointArray) Encode() string {
	var entries []string
	for _, w := range wslice {
		s := w.Fix
		if w.AltitudeRestriction != nil {
			s += "/a" + w.AltitudeRestriction.Encoded()
		}
		if w.Speed != 0 {
			s += fmt.Sprintf("/s%d", w.Speed)
		}
		if pt := w.ProcedureTurn; pt != nil {
			if pt.Type == PTStandard45 {
				if !pt.RightTurns {
					s += "/lpt45"
				} else {
					s += "/pt45"
				}
			} else {
				if !pt.RightTurns {
					s += "/lhilpt"
				} else {
					s += "/hilpt"
				}
			}
			if pt.MinuteLimit != 0 {
				s += fmt.Sprintf("%.1fmin", pt.MinuteLimit)
			} else {
				s += fmt.Sprintf("%.1fnm", pt.NmLimit)
			}
			if pt.Entry180NoPT {
				s += "/nopt180"
			}
			if pt.ExitAltitude != 0 {
				s += fmt.Sprintf("/pta%d", pt.ExitAltitude)
			}
		}
		if w.IAF {
			s += "/iaf"
		}
		if w.IF {
			s += "/if"
		}
		if w.FAF {
			s += "/faf"
		}
		if w.NoPT {
			s += "/nopt"
		}
		if w.Handoff {
			s += "/ho"
		}
		if w.PointOut != "" {
			s += "/po" + w.PointOut
		}
		if w.FlyOver {
			s += "/flyover"
		}
		if w.Delete {
			s += "/delete"
		}
		if w.Heading != 0 {
			s += fmt.Sprintf("/h%d", w.Heading)
		}
		if w.Arc != nil {
			if w.Arc.Fix != "" {
				s += fmt.Sprintf("/arc%.1f%s", w.Arc.Radius, w.Arc.Fix)
			} else {
				s += fmt.Sprintf("/arc%.1f", w.Arc.Length)
			}
		}
		if w.Airway != "" {
			s += "/airway" + w.Airway
		}
		if w.OnSID {
			s += "/sid"
		}
		if w.OnSTAR {
			s += "/star"
		}

		entries = append(entries, s)

	}

	return strings.Join(entries, " ")
}

func (w *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		// Handle the string encoding used in scenario JSON files
		wp, err := parseWaypoints(string(b[1 : len(b)-1]))
		if err == nil {
			*w = wp
		}
		return err
	} else {
		// Otherwise unmarshal it normally
		var wp []Waypoint
		err := json.Unmarshal(b, &wp)
		if err == nil {
			*w = wp
		}
		return err
	}
}

func (w WaypointArray) RouteString() string {
	var r []string
	airway := ""
	for _, wp := range w {
		if airway != "" && wp.Airway == airway {
			// This fix was automatically added for an airway so don't include it here.
			continue
		}
		r = append(r, wp.Fix)

		if wp.Airway != airway {
			if wp.Airway != "" {
				r = append(r, wp.Airway)
			}
			airway = wp.Airway
		}
	}
	return strings.Join(r, " ")
}

func (w WaypointArray) CheckDeparture(e *util.ErrorLogger, controllers map[string]*Controller) {
	w.checkBasics(e, controllers)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in departure")
		}
		if war := wp.AltitudeRestriction; war != nil {
			// Make sure it's generally reasonable
			if war.Range[0] < 0 || war.Range[0] >= 50000 || war.Range[1] < 0 || war.Range[1] >= 50000 {
				e.ErrorString("Invalid altitude range: should be between 0 and FL500: %s-%s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}
			if war.Range[0] != 0 {
				if lastMin != 0 && war.Range[0] < lastMin {
					// our minimum must be >= the previous minimum
					e.ErrorString("Minimum altitude %s is lower than previous fix %s's minimum %s",
						FormatAltitude(war.Range[0]), minFix, FormatAltitude(lastMin))
				}
				lastMin = war.Range[0]
				minFix = wp.Fix
			}
		}

		e.Pop()
	}
}

func (w WaypointArray) checkBasics(e *util.ErrorLogger, controllers map[string]*Controller) {
	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.Speed < 0 || wp.Speed > 300 {
			e.ErrorString("invalid speed restriction %d", wp.Speed)
		}

		if wp.PointOut != "" {
			if !util.MapContains(controllers,
				func(callsign string, ctrl *Controller) bool { return ctrl.SectorId == wp.PointOut }) {
				e.ErrorString("No controller found with TCP id %q for point out", wp.PointOut)
			}
		}
		e.Pop()
	}
}

func (w WaypointArray) CheckApproach(e *util.ErrorLogger, controllers map[string]*Controller) {
	w.checkBasics(e, controllers)
	w.checkDescending(e)

	if len(w) < 2 {
		e.ErrorString("must have at least two waypoints in an approach")
	}

	/*
		// Disable for now...
		foundFAF := false
		for _, wp := range w {
			if wp.FAF {
				foundFAF = true
				break
			}
		}
		if !foundFAF {
			e.ErrorString("No /faf specifier found in approach")
		}
	*/
}

func (w WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[string]*Controller) {
	w.checkBasics(e, ctrl)
	w.checkDescending(e)

	for _, wp := range w {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		e.Pop()
	}
}

func (w WaypointArray) CheckOverflight(e *util.ErrorLogger, ctrl map[string]*Controller) {
	w.checkBasics(e, ctrl)
}

func (w WaypointArray) checkDescending(e *util.ErrorLogger) {
	// or at least, check not climbing...
	var lastMin float32
	var minFix string // last fix that established a specific minimum alt

	for _, wp := range w {
		e.Push(wp.Fix)

		if war := wp.AltitudeRestriction; war != nil {
			if war.Range[0] != 0 && war.Range[1] != 0 && war.Range[0] > war.Range[1] {
				e.ErrorString("Minimum altitude %s is higher than maximum %s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			// Make sure it's generally reasonable
			if war.Range[0] < 0 || war.Range[0] >= 50000 || war.Range[1] < 0 || war.Range[1] >= 50000 {
				e.ErrorString("Invalid altitude range: should be between 0 and FL500: %s-%s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			if war.Range[0] != 0 {
				if minFix != "" && war.Range[0] > lastMin {
					e.ErrorString("Minimum altitude %s is higher than previous fix %s's minimum %s",
						FormatAltitude(war.Range[1]), minFix, FormatAltitude(lastMin))
				}
				minFix = wp.Fix
				lastMin = war.Range[0]
			}
		}

		e.Pop()
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

func parseWaypoints(str string) (WaypointArray, error) {
	var waypoints WaypointArray
	entries := strings.Fields(str)
	for ei, field := range entries {
		if len(field) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: %q", str)
		}

		components := strings.Split(field, "/")

		// Is it an airway?
		if _, ok := DB.Airways[components[0]]; ok {
			if ei == 0 {
				return nil, fmt.Errorf("%s: can't begin a route with an airway", components[0])
			} else if ei == len(entries)-1 {
				return nil, fmt.Errorf("%s: can't end a route with an airway", components[0])
			} else if len(components) > 1 {
				return nil, fmt.Errorf("%s: can't have fix modifiers with an airway", field)
			} else {
				// Just set the Airway field for now; we'll patch up the
				// waypoints to include the airway waypoints at the end of
				// this function.
				nwp := len(waypoints)
				waypoints[nwp-1].Airway = components[0]
				continue
			}
		}

		wp := Waypoint{}
		for i, f := range components {
			if i == 0 {
				wp.Fix = f
			} else if len(f) == 0 {
				return nil, fmt.Errorf("no command found after / in %q", field)
			} else {
				if f == "ho" {
					wp.Handoff = true
				} else if f == "flyover" {
					wp.FlyOver = true
				} else if f == "delete" {
					wp.Delete = true
				} else if f == "iaf" {
					wp.IAF = true
				} else if f == "if" {
					wp.IF = true
				} else if f == "faf" {
					wp.FAF = true
				} else if f == "sid" {
					wp.OnSID = true
				} else if f == "star" {
					wp.OnSTAR = true
				} else if len(f) > 2 && f[:2] == "po" {
					wp.PointOut = f[2:]
				} else if (len(f) >= 4 && f[:4] == "pt45") || len(f) >= 5 && f[:5] == "lpt45" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Type = PTStandard45
					wp.ProcedureTurn.RightTurns = f[0] == 'p'

					extent := f[4:]
					if !wp.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(wp.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if (len(f) >= 5 && f[:5] == "hilpt") || (len(f) >= 6 && f[:6] == "lhilpt") {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Type = PTRacetrack
					wp.ProcedureTurn.RightTurns = f[0] == 'h'

					extent := f[5:]
					if !wp.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(wp.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if len(f) >= 4 && f[:3] == "pta" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}

					if alt, err := strconv.Atoi(f[3:]); err == nil {
						wp.ProcedureTurn.ExitAltitude = alt
					} else {
						return nil, fmt.Errorf("%s: error parsing procedure turn exit altitude: %v", f[3:], err)
					}
				} else if f == "nopt" {
					wp.NoPT = true
				} else if f == "nopt180" {
					if wp.ProcedureTurn == nil {
						wp.ProcedureTurn = &ProcedureTurn{}
					}
					wp.ProcedureTurn.Entry180NoPT = true
				} else if len(f) >= 4 && f[:3] == "arc" {
					spec := f[3:]
					rend := 0
					for rend < len(spec) &&
						((spec[rend] >= '0' && spec[rend] <= '9') || spec[rend] == '.') {
						rend++
					}
					if rend == 0 {
						return nil, fmt.Errorf("%s: radius not found after /arc", f)
					}

					v, err := strconv.ParseFloat(spec[:rend], 32)
					if err != nil {
						return nil, fmt.Errorf("%s: invalid arc radius/length: %w", f, err)
					}

					if rend == len(spec) {
						// no fix given, so interpret it as an arc length
						wp.Arc = &DMEArc{
							Length: float32(v),
						}
					} else {
						wp.Arc = &DMEArc{
							Fix:    spec[rend:],
							Radius: float32(v),
						}
					}
				} else if len(f) >= 7 && f[:6] == "airway" {
					wp.Airway = f[6:]

					// Do these last since they only match the first character...
				} else if f[0] == 'a' {
					var err error
					wp.AltitudeRestriction, err = ParseAltitudeRestriction(f[1:])
					if err != nil {
						return nil, err
					}
				} else if f[0] == 's' {
					kts, err := strconv.Atoi(f[1:])
					if err != nil {
						return nil, fmt.Errorf("%s: error parsing number after speed restriction: %v", f[1:], err)
					}
					wp.Speed = kts
				} else if f[0] == 'h' { // after "ho" and "hilpt" check...
					if hdg, err := strconv.Atoi(f[1:]); err != nil {
						return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", f[1:], err)
					} else {
						wp.Heading = hdg
					}

				} else {
					return nil, fmt.Errorf("%s: unknown fix modifier: %s", field, f)
				}
			}
		}

		if wp.ProcedureTurn != nil && wp.ProcedureTurn.Type == PTUndefined {
			return nil, fmt.Errorf("%s: no procedure turn specified for fix (e.g., pt45/hilpt) even though PT parameters were given", wp.Fix)
		}

		waypoints = append(waypoints, wp)
	}

	// Now go through and expand out any airways into their constituent waypoints
	var wpExpanded []Waypoint
	for i, wp := range waypoints {
		wpExpanded = append(wpExpanded, wp)

		if wp.Airway != "" {
			found := false
			wp0, wp1 := wp.Fix, waypoints[i+1].Fix
			for _, airway := range DB.Airways[wp.Airway] {
				if awp, ok := airway.WaypointsBetween(wp0, wp1); ok {
					wpExpanded = append(wpExpanded, awp...)
					found = true
					break
				}
			}

			if !found {
				return nil, fmt.Errorf("%s: unable to find fix pair %s - %s in airway", wp.Airway, wp0, wp1)
			}
		}
	}

	return wpExpanded, nil
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
		return &AltitudeRestriction{Range: [2]float32{0, float32(alt)}}, nil
	} else if s[n-1] == '+' {
		// At or above
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		return &AltitudeRestriction{Range: [2]float32{float32(alt), 0}}, nil
	} else if alts := strings.Split(s, "-"); len(alts) == 2 {
		// Between
		if low, err := strconv.Atoi(alts[0]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if high, err := strconv.Atoi(alts[1]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if low > high {
			return nil, fmt.Errorf("%s: low altitude %d is above high altitude %d", s, low, high)
		} else {
			return &AltitudeRestriction{Range: [2]float32{float32(low), float32(high)}}, nil
		}
	} else {
		// At
		if alt, err := strconv.Atoi(s); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else {
			return &AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}, nil
		}
	}
}

type Locator interface {
	Locate(fix string) (math.Point2LL, bool)
}

func (waypoints WaypointArray) InitializeLocations(loc Locator, nmPerLongitude float32, magneticVariation float32, e *util.ErrorLogger) {
	var prev math.Point2LL

	for i, wp := range waypoints {
		if e != nil {
			e.Push("Fix " + wp.Fix)
		}
		if pos, ok := loc.Locate(wp.Fix); !ok {
			if e != nil {
				e.ErrorString("unable to locate waypoint")
			}
		} else {
			waypoints[i].Location = pos

			d := math.NMDistance2LL(prev, waypoints[i].Location)
			if i > 1 && d > 120 && e != nil {
				e.ErrorString("waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
					waypoints[i].Location.DDString(), waypoints[i-1].Fix, waypoints[i-1].Location.DDString(), d)
			}
			prev = waypoints[i].Location
		}

		if e != nil {
			e.Pop()
		}
	}

	// Do (DME) arcs after wp.Locations have been initialized
	for i, wp := range waypoints {
		if wp.Arc == nil {
			continue
		}

		if e != nil {
			e.Push("Fix " + wp.Fix)
		}

		if i+1 == len(waypoints) {
			if e != nil {
				e.ErrorString("can't have DME arc starting at the final waypoint")
				e.Pop()
			}
			break
		}

		// Which way are we turning as we depart p0? Use either the
		// previous waypoint or the next one after the end of the arc
		// to figure it out.
		var v0, v1 [2]float32
		p0, p1 := math.LL2NM(wp.Location, nmPerLongitude), math.LL2NM(waypoints[i+1].Location, nmPerLongitude)
		if i > 0 {
			v0 = math.Sub2f(p0, math.LL2NM(waypoints[i-1].Location, nmPerLongitude))
			v1 = math.Sub2f(p1, p0)
		} else {
			if i+2 == len(waypoints) {
				if e != nil {
					e.ErrorString("must have at least one waypoint before or after arc to determine its orientation")
					e.Pop()
				}
				continue
			}
			v0 = math.Sub2f(p1, p0)
			v1 = math.Sub2f(math.LL2NM(waypoints[i+2].Location, nmPerLongitude), p1)
		}
		// cross product
		x := v0[0]*v1[1] - v0[1]*v1[0]
		wp.Arc.Clockwise = x < 0

		if wp.Arc.Fix != "" {
			// Center point was specified
			var ok bool
			if wp.Arc.Center, ok = loc.Locate(wp.Arc.Fix); !ok {
				if e != nil {
					e.ErrorString("unable to locate arc center \"" + wp.Arc.Fix + "\"")
					e.Pop()
				}
				continue
			}
		} else {
			// Just the arc length was specified; need to figure out the
			// center and radius of the circle that gives that.
			d := math.Distance2f(p0, p1)
			if d >= wp.Arc.Length {
				if e != nil {
					e.ErrorString("distance between waypoints %.2fnm is greater than specified arc length %.2fnm",
						d, wp.Arc.Length)
					e.Pop()
				}
				continue
			}
			if wp.Arc.Length > d*3.14159 {
				// No circle is possible to give an arc that long
				if e != nil {
					e.ErrorString("no valid circle will give a distance between waypoints %.2fnm", wp.Arc.Length)
					e.Pop()
				}
				continue
			}

			// Now search for a center point of a circle that goes through
			// p0 and p1 and has the desired arc length.  We will search
			// along the line perpendicular to the vector p1-p0 that goes
			// through its center point.

			// There are two possible center points for the circle, one on
			// each side of the line p0-p1.  We will take positive or
			// negative steps in parametric t along the perpendicular line
			// so that we're searching in the right direction to get the
			// clockwise/counter clockwise route we want.
			delta := float32(util.Select(wp.Arc.Clockwise, -.01, .01))

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

				if arcLength < wp.Arc.Length {
					wp.Arc.Center = math.NM2LL(center, nmPerLongitude)
					wp.Arc.Radius = radius
					break
				}

				t += delta
			}

			if t >= limit {
				if e != nil {
					e.ErrorString("unable to find valid circle radius for arc")
					e.Pop()
				}
				continue
			}
		}

		// Heading from the center of the arc to the current fix
		hfix := math.Heading2LL(wp.Arc.Center, wp.Location, nmPerLongitude, magneticVariation)

		// Then perpendicular to that, depending on the arc's direction
		wp.Arc.InitialHeading = math.NormalizeHeading(hfix + float32(util.Select(wp.Arc.Clockwise, 90, -90)))

		if e != nil {
			e.Pop()
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// STAR

type STAR struct {
	Transitions     map[string]WaypointArray
	RunwayWaypoints map[string]WaypointArray
}

func (s STAR) Check(e *util.ErrorLogger) {
	check := func(wps WaypointArray) {
		for _, wp := range wps {
			_, okn := DB.Navaids[wp.Fix]
			_, okf := DB.Fixes[wp.Fix]
			if !okn && !okf {
				e.ErrorString("fix %s not found in navaid database", wp.Fix)
			}
		}
	}
	for _, wps := range s.Transitions {
		check(wps)
	}
	for _, wps := range s.RunwayWaypoints {
		check(wps)
	}
}

func MakeSTAR() *STAR {
	return &STAR{
		Transitions:     make(map[string]WaypointArray),
		RunwayWaypoints: make(map[string]WaypointArray),
	}
}

func (s STAR) Print(name string) {
	for tr, wps := range s.Transitions {
		fmt.Printf("%-12s: %s\n", name+"."+tr, wps.Encode())
	}
	for rwy, wps := range s.RunwayWaypoints {
		fmt.Printf("%-12s: %s\n", name+".RWY"+rwy, wps.Encode())
	}
}

///////////////////////////////////////////////////////////////////////////
// HILPT

type PTType int

const (
	PTUndefined = iota
	PTRacetrack
	PTStandard45
)

func (pt PTType) String() string {
	return []string{"undefined", "racetrack", "standard 45"}[pt]
}

type ProcedureTurn struct {
	Type         PTType
	RightTurns   bool
	ExitAltitude int     `json:",omitempty"`
	MinuteLimit  float32 `json:",omitempty"`
	NmLimit      float32 `json:",omitempty"`
	Entry180NoPT bool    `json:",omitempty"`
}

type RacetrackPTEntry int

const (
	DirectEntryShortTurn = iota
	DirectEntryLongTurn
	ParallelEntry
	TeardropEntry
)

func (e RacetrackPTEntry) String() string {
	return []string{"direct short", "direct long", "parallel", "teardrop"}[int(e)]
}

func (e RacetrackPTEntry) MarshalJSON() ([]byte, error) {
	s := "\"" + e.String() + "\""
	return []byte(s), nil
}

func (e *RacetrackPTEntry) UnmarshalJSON(b []byte) error {
	if len(b) < 2 {
		return fmt.Errorf("invalid HILPT")
	}

	switch string(b[1 : len(b)-1]) {
	case "direct short":
		*e = DirectEntryShortTurn
	case "direct long":
		*e = DirectEntryLongTurn
	case "parallel":
		*e = ParallelEntry
	case "teardrop":
		*e = TeardropEntry
	default:
		return fmt.Errorf("%s: malformed HILPT JSON", string(b))
	}
	return nil
}

func (pt *ProcedureTurn) SelectRacetrackEntry(inboundHeading float32, aircraftFixHeading float32) RacetrackPTEntry {
	// Rotate so we can treat inboundHeading as 0.
	hdg := aircraftFixHeading - inboundHeading
	if hdg < 0 {
		hdg += 360
	}

	if pt.RightTurns {
		if hdg > 290 {
			return DirectEntryLongTurn
		} else if hdg < 110 {
			return DirectEntryShortTurn
		} else if hdg > 180 {
			return ParallelEntry
		} else {
			return TeardropEntry
		}
	} else {
		if hdg > 250 {
			return DirectEntryShortTurn
		} else if hdg < 70 {
			return DirectEntryLongTurn
		} else if hdg < 180 {
			return ParallelEntry
		} else {
			return TeardropEntry
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// AltitudeRestriction

type AltitudeRestriction struct {
	// We treat 0 as "unset", which works naturally for the bottom but
	// requires occasional care at the top.
	Range [2]float32
}

func (a *AltitudeRestriction) UnmarshalJSON(b []byte) error {
	// For backwards compatibility with saved scenarios, we allow
	// unmarshaling from the single-valued altitude restrictions we had
	// before.
	if alt, err := strconv.Atoi(string(b)); err == nil {
		a.Range = [2]float32{float32(alt), float32(alt)}
		return nil
	} else {
		// Otherwise declare a temporary variable with matching structure
		// but a different type to avoid an infinite loop when
		// json.Unmarshal is called.
		ar := struct{ Range [2]float32 }{}
		if err := json.Unmarshal(b, &ar); err == nil {
			a.Range = ar.Range
			return nil
		} else {
			return err
		}
	}
}

func (a AltitudeRestriction) TargetAltitude(alt float32) float32 {
	if a.Range[1] != 0 {
		return math.Clamp(alt, a.Range[0], a.Range[1])
	} else {
		return math.Max(alt, a.Range[0])
	}
}

// ClampRange limits a range of altitudes to satisfy the altitude
// restriction; the returned Boolean indicates whether the ranges
// overlapped.
func (a AltitudeRestriction) ClampRange(r [2]float32) ([2]float32, bool) {
	a0, a1 := a.Range[0], a.Range[1]
	if a1 == 0 {
		a1 = 1000000
	}

	ok := r[0] <= a1 || r[1] >= a0
	return [2]float32{math.Clamp(r[0], a0, a1), math.Clamp(r[1], a0, a1)}, ok
}

// Summary returns a human-readable summary of the altitude
// restriction.
func (a AltitudeRestriction) Summary() string {
	if a.Range[0] != 0 {
		if a.Range[1] == a.Range[0] {
			return fmt.Sprintf("at %s", FormatAltitude(a.Range[0]))
		} else if a.Range[1] != 0 {
			return fmt.Sprintf("between %s-%s", FormatAltitude(a.Range[0]), FormatAltitude(a.Range[1]))
		} else {
			return fmt.Sprintf("at or above %s", FormatAltitude(a.Range[0]))
		}
	} else if a.Range[1] != 0 {
		return fmt.Sprintf("at or below %s", FormatAltitude(a.Range[1]))
	} else {
		return ""
	}
}

// Encoded returns the restriction in the encoded form in which it is
// specified in scenario configuration files, e.g. "5000+" for "at or above
// 5000".
func (a AltitudeRestriction) Encoded() string {
	if a.Range[0] != 0 {
		if a.Range[0] == a.Range[1] {
			return fmt.Sprintf("%.0f", a.Range[0])
		} else if a.Range[1] != 0 {
			return fmt.Sprintf("%.0f-%.0f", a.Range[0], a.Range[1])
		} else {
			return fmt.Sprintf("%.0f+", a.Range[0])
		}
	} else if a.Range[1] != 0 {
		return fmt.Sprintf("%.0f-", a.Range[1])
	} else {
		return ""
	}
}

///////////////////////////////////////////////////////////////////////////
// DMEArc

// Can either be specified with (Fix,Radius), or (Length,Clockwise); the
// remaining fields are then derived from those.
type DMEArc struct {
	Fix            string
	Center         math.Point2LL
	Radius         float32
	Length         float32
	InitialHeading float32
	Clockwise      bool
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
		wps = append(wps, Waypoint{
			Fix:    a.Fixes[i].Fix,
			Airway: a.Name, // maintain the identity that we're on an airway
		})
	}
	return wps, true
}

///////////////////////////////////////////////////////////////////////////
// Overflight

type Overflight struct {
	Waypoints           WaypointArray           `json:"waypoints"`
	InitialAltitudes    util.SingleOrArray[int] `json:"initial_altitude"`
	CruiseAltitude      float32                 `json:"cruise_altitude"`
	AssignedAltitude    float32                 `json:"assigned_altitude"`
	InitialSpeed        float32                 `json:"initial_speed"`
	AssignedSpeed       float32                 `json:"assigned_speed"`
	SpeedRestriction    float32                 `json:"speed_restriction"`
	InitialController   string                  `json:"initial_controller"`
	Scratchpad          string                  `json:"scratchpad"`
	SecondaryScratchpad string                  `json:"secondary_scratchpad"`
	Description         string                  `json:"description"`
	Airlines            []OverflightAirline     `json:"airlines"`
}

type OverflightAirline struct {
	AirlineSpecifier
	DepartureAirport string `json:"departure_airport"`
	ArrivalAirport   string `json:"arrival_airport"`
}

func (of *Overflight) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[string]*Airport, controlPositions map[string]*Controller, e *util.ErrorLogger) {
	if len(of.Waypoints) < 2 {
		e.ErrorString("must provide at least two \"waypoints\" for overflight")
	}

	of.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, e)

	of.Waypoints[len(of.Waypoints)-1].Delete = true
	of.Waypoints[len(of.Waypoints)-1].FlyOver = true

	of.Waypoints.CheckOverflight(e, controlPositions)

	if len(of.Airlines) == 0 {
		e.ErrorString("must specify at least one airline in \"airlines\"")
	}
	for _, al := range of.Airlines {
		al.Check(e)
	}

	if len(of.InitialAltitudes) == 0 {
		e.ErrorString("must specify at least one \"initial_altitude\"")
	}

	if of.InitialSpeed == 0 {
		e.ErrorString("must specify \"initial_speed\"")
	}

	if of.InitialController == "" {
		e.ErrorString("\"initial_controller\" missing")
	} else if _, ok := controlPositions[of.InitialController]; !ok {
		e.ErrorString("controller %q not found for \"initial_controller\"", of.InitialController)
	}
}
