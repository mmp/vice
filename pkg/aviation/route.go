// pkg/aviation/route.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// Waypoint

type Waypoint struct {
	Fix                      string               `json:"fix"`
	Location                 math.Point2LL        // not provided in scenario JSON; derived from fix
	AltitudeRestriction      *AltitudeRestriction `json:"altitude_restriction,omitempty"`
	Speed                    int                  `json:"speed,omitempty"`
	Heading                  int                  `json:"heading,omitempty"` // outbound heading after waypoint
	PresentHeading           bool
	ProcedureTurn            *ProcedureTurn `json:"pt,omitempty"`
	NoPT                     bool           `json:"nopt,omitempty"`
	HumanHandoff             bool           `json:"human_handoff"` // To named TCP.
	TCPHandoff               string         `json:"tcp_handoff"`   // To named TCP.
	PointOut                 string         `json:"pointout,omitempty"`
	ClearApproach            bool           `json:"clear_approach,omitempty"` // used for distractor a/c, clears them for the approach passing the wp.
	FlyOver                  bool           `json:"flyover,omitempty"`
	Delete                   bool           `json:"delete,omitempty"`
	Land                     bool           `json:"land,omitempty"`
	Arc                      *DMEArc        `json:"arc,omitempty"`
	IAF, IF, FAF             bool           // not provided in scenario JSON; derived from fix
	Airway                   string         // when parsing waypoints, this is set if we're on an airway after the fix
	OnSID, OnSTAR            bool           // set during deserialization
	OnApproach               bool           // set during deserialization
	AirworkRadius            int            // set during deserialization
	AirworkMinutes           int            // set during deserialization
	Radius                   float32
	Shift                    float32
	PrimaryScratchpad        string
	ClearPrimaryScratchpad   bool
	SecondaryScratchpad      string
	ClearSecondaryScratchpad bool
	TransferComms            bool
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
	if wp.PresentHeading {
		attrs = append(attrs, slog.Bool("present_heading", wp.PresentHeading))
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
	if wp.HumanHandoff {
		attrs = append(attrs, slog.Bool("human_handoff", wp.HumanHandoff))
	}
	if wp.TCPHandoff != "" {
		attrs = append(attrs, slog.String("tcp_handoff", wp.TCPHandoff))
	}
	if wp.PointOut != "" {
		attrs = append(attrs, slog.String("pointout", wp.PointOut))
	}
	if wp.ClearApproach {
		attrs = append(attrs, slog.Bool("clear_approach", wp.ClearApproach))
	}
	if wp.FlyOver {
		attrs = append(attrs, slog.Bool("fly_over", wp.FlyOver))
	}
	if wp.Delete {
		attrs = append(attrs, slog.Bool("delete", wp.Delete))
	}
	if wp.Land {
		attrs = append(attrs, slog.Bool("land", wp.Land))
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
	if wp.OnApproach {
		attrs = append(attrs, slog.Bool("on_approach", wp.OnApproach))
	}
	if wp.PrimaryScratchpad != "" {
		attrs = append(attrs, slog.String("primary_scratchpad", wp.PrimaryScratchpad))
	}
	if wp.ClearPrimaryScratchpad {
		attrs = append(attrs, slog.Bool("clear_primary_scratchpad", wp.ClearPrimaryScratchpad))
	}
	if wp.SecondaryScratchpad != "" {
		attrs = append(attrs, slog.String("secondary_scratchpad", wp.SecondaryScratchpad))
	}
	if wp.ClearSecondaryScratchpad {
		attrs = append(attrs, slog.Bool("clear_secondary_scratchpad", wp.ClearSecondaryScratchpad))
	}
	if wp.TransferComms {
		attrs = append(attrs, slog.Bool("transfer_comms", wp.TransferComms))
	}

	return slog.GroupValue(attrs...)
}

func (wp *Waypoint) ETA(p math.Point2LL, gs float32, nmPerLongitude float32) time.Duration {
	dist := math.NMDistance2LLFast(p, wp.Location, nmPerLongitude)
	eta := dist / gs
	return time.Duration(eta * float32(time.Hour))
}

///////////////////////////////////////////////////////////////////////////
// WaypointArray

type WaypointArray []Waypoint

func (wa WaypointArray) Encode() string {
	var entries []string
	for _, w := range wa {
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
		if w.HumanHandoff {
			s += "/ho"
		}
		if w.TCPHandoff != "" {
			s += "/ho" + w.TCPHandoff
		}
		if w.PointOut != "" {
			s += "/po" + w.PointOut
		}
		if w.ClearApproach {
			s += "/clearapp"
		}
		if w.FlyOver {
			s += "/flyover"
		}
		if w.Delete {
			s += "/delete"
		}
		if w.Land {
			s += "/land"
		}
		if w.Heading != 0 {
			s += fmt.Sprintf("/h%d", w.Heading)
		}
		if w.PresentHeading {
			s += "/ph"
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
		if w.OnApproach {
			s += "/appr"
		}
		if w.AirworkRadius != 0 {
			s += fmt.Sprintf("/airwork%dnm%dm", w.AirworkRadius, w.AirworkMinutes)
		}
		if w.Radius != 0 {
			s += fmt.Sprintf("/radius%.1f", w.Radius)
		}
		if w.Shift != 0 {
			s += fmt.Sprintf("/shift%.1f", w.Shift)
		}
		if w.PrimaryScratchpad != "" {
			s += "/spsp" + w.PrimaryScratchpad
		}
		if w.ClearPrimaryScratchpad {
			s += "/cpsp"
		}
		if w.SecondaryScratchpad != "" {
			s += "/sssp" + w.SecondaryScratchpad
		}
		if w.ClearSecondaryScratchpad {
			s += "/cssp"
		}
		if w.TransferComms {
			s += "/tc"
		}

		entries = append(entries, s)
	}

	return strings.Join(entries, " ")
}

func (wa *WaypointArray) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		// Handle the string encoding used in scenario JSON files
		wp, err := parseWaypoints(string(b[1 : len(b)-1]))
		if err == nil {
			*wa = wp
		}
		return err
	} else {
		// Otherwise unmarshal it normally
		var wp []Waypoint
		err := json.Unmarshal(b, &wp)
		if err == nil {
			*wa = wp
		}
		return err
	}
}

func (wa WaypointArray) RouteString() string {
	var r []string
	airway := ""
	for _, wp := range wa {
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

func (wa WaypointArray) CheckDeparture(e *util.ErrorLogger, controllers map[string]*Controller, checkScratchpads func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, controllers, checkScratchpads)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range wa {
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

func (wa WaypointArray) checkBasics(e *util.ErrorLogger, controllers map[string]*Controller, checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	haveHO := false
	for i, wp := range wa {
		e.Push(wp.Fix)
		if wp.Speed < 0 || wp.Speed > 300 {
			e.ErrorString("invalid speed restriction %d", wp.Speed)
		}

		if wp.AirworkMinutes > 0 {
			if ar := wp.AltitudeRestriction; ar == nil {
				e.ErrorString("Must provide altitude range via \"/aXXX-YYY\" with /airwork")
			} else if ar.Range[0] == 0 || ar.Range[1] == 0 {
				e.ErrorString("Must provide top and bottom in altitude range \"/aXXX-YYY\" with /airwork")
			} else if ar.Range[1]-ar.Range[0] < 2000 {
				e.ErrorString("Must provide at least 2,000' of altitude range with /airwork")
			}
		}

		if wp.PointOut != "" {
			if !util.MapContains(controllers,
				func(callsign string, ctrl *Controller) bool { return ctrl.Id() == wp.PointOut }) {
				e.ErrorString("No controller found with id %q for point out", wp.PointOut)
			}
		}

		if wp.TCPHandoff != "" {
			if !util.MapContains(controllers,
				func(callsign string, ctrl *Controller) bool { return ctrl.Id() == wp.TCPHandoff }) {
				e.ErrorString("No controller found with id %q for handoff", wp.TCPHandoff)
			}
		}

		if wp.HumanHandoff {
			haveHO = true
		}

		if wp.TransferComms && !haveHO {
			e.ErrorString("Must have /ho to handoff to a human controller at a waypoint prior to /tc")
		}

		if i == 0 && wp.Shift > 0 {
			e.ErrorString("Can't specify /shift at the first fix in a route")
		}
		if wp.Radius > 0 && wp.Shift > 0 {
			e.ErrorString("Can't specify both /radius and /shift at the same fix")
		}

		if !checkScratchpad(wp.PrimaryScratchpad) {
			e.ErrorString("%s: invalid primary_scratchpad", wp.PrimaryScratchpad)
		}
		if !checkScratchpad(wp.SecondaryScratchpad) {
			e.ErrorString("%s: invalid secondary scratchpad", wp.SecondaryScratchpad)
		}

		e.Pop()
	}
}

func CheckApproaches(e *util.ErrorLogger, wps []WaypointArray, requireFAF bool, controllers map[string]*Controller,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	foundFAF := false
	for _, w := range wps {
		w.checkBasics(e, controllers, checkScratchpad)
		w.checkDescending(e)

		if len(w) < 2 {
			e.ErrorString("must have at least two waypoints in an approach")
		}

		for _, wp := range w {
			if wp.FAF {
				foundFAF = true
			}
		}
	}
	if requireFAF && !foundFAF {
		e.ErrorString("No /faf specifier found in approach")
	}
}

func (wa WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[string]*Controller, approachAssigned bool,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, ctrl, checkScratchpad)
	wa.checkDescending(e)

	for _, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF || wp.IF || wp.FAF {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		if wp.ClearApproach && !approachAssigned {
			e.ErrorString("/clearapp specified but no approach has been assigned")
		}
		e.Pop()
	}
}

func (wa WaypointArray) CheckOverflight(e *util.ErrorLogger, ctrl map[string]*Controller, checkScratchpads func(string) bool) {
	wa.checkBasics(e, ctrl, checkScratchpads)
}

func (wa WaypointArray) checkDescending(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	// or at least, check not climbing...
	var lastMin float32
	var minFix string // last fix that established a specific minimum alt

	for _, wp := range wa {
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

func RandomizeRoute(w []Waypoint, r *rand.Rand, randomizeAltitudeRange bool, perf AircraftPerformance, nmPerLongitude float32,
	magneticVariation float32, airport string, wind WindModel, lg *log.Logger) WaypointArray {
	// Random values used for altitude and position randomization
	rtheta, rrad := r.Float32(), r.Float32()
	ralt := r.Float32()

	// We use this to some random variation to the random sample after each
	// use. In this way, there's some correlation between adjacent
	// waypoints: if they're relatively high at one, they'll tend to be
	// relatively high at the next one, though the random choices still
	// vary a bit.
	jitter := func(v float32) float32 {
		v += -0.1 + 0.2*r.Float32()
		if v < 0 {
			v = -v
		} else if v > 1 {
			v = 1 - (v - 1)
		}
		return v
	}

	for i := 0; i < len(w); i++ { // NOTE: written this way since we append to w in the following
		wp := &w[i]
		if wp.Radius > 0 {
			// Work in nm coordinates
			p := math.LL2NM(wp.Location, nmPerLongitude)

			// radius and theta
			r := math.Sqrt(rrad) * wp.Radius // equi-area mapping
			const Pi = 3.1415926535
			t := 2 * Pi * rtheta

			pp := math.Add2f(p, math.Scale2f([2]float32{math.Sin(t), math.Cos(t)}, r))
			wp.Location = math.NM2LL(pp, nmPerLongitude)
			wp.Radius = 0 // clean up

			rtheta = jitter(rtheta)
			rrad = jitter(rrad)
		} else if wp.Shift > 0 {
			p0, p1 := math.LL2NM(w[i-1].Location, nmPerLongitude), math.LL2NM(w[i].Location, nmPerLongitude)
			v := math.Normalize2f(math.Sub2f(p1, p0))
			t := math.Lerp(rrad, -wp.Shift, wp.Shift)
			p := math.Add2f(p1, math.Scale2f(v, t))
			wp.Location = math.NM2LL(p, nmPerLongitude)

			wp.Shift = 0 // clean up

			rrad = jitter(rrad)
		}

		if randomizeAltitudeRange {
			if ar := wp.AltitudeRestriction; ar != nil {
				low, high := ar.Range[0], ar.Range[1]
				// We should clamp low to be a few hundred feet AGL, but
				// hopefully we'll generally be given a full range.
				if high == 0 {
					high = low + 3000
				}
				alt := math.Lerp(ralt, low, high)

				// Update the altitude restriction to just be the single altitude.
				// Note that we don't want to modify wp.AltitudeRestriction in
				// place since the pointer is shared with other instances of
				// the route.
				wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{alt, alt}}

				ralt = jitter(ralt)
			}
		}
		if wp.Land {
			land := constructVFRLanding(*wp, perf, airport, wind, nmPerLongitude, magneticVariation, lg)
			wp.Land = false
			wp.Delete = false // overflights have this added to their last waypoint automatically
			w = w[:i+1]
			w = append(w, land...)
		}
	}

	return w
}

func constructVFRLanding(wp Waypoint, perf AircraftPerformance, airport string, wind WindModel, nmPerLongitude float32,
	magneticVariation float32, lg *log.Logger) []Waypoint {
	ap, ok := DB.Airports[airport]
	if !ok {
		lg.Errorf("%s: couldn't find arrival airport", airport)
		wp.Delete = true
		return []Waypoint{wp} // best we can do
	}

	rwy, opp := ap.SelectBestRunway(wind, magneticVariation)
	if rwy == nil || opp == nil {
		lg.Error("couldn't find a runway to land on", slog.String("airport", airport), slog.Any("runways", ap.Runways))
		wp.Delete = true
		return []Waypoint{wp} // best we can do
	}

	rg := MakeRouteGenerator(rwy.Threshold, opp.Threshold, nmPerLongitude)

	// TODO: does CIFP or something else have the closed traffic side for runways encoded?
	var wps []Waypoint
	addpt := func(n string, dx, dy, dalt float32, fo bool, slow bool) {
		wp := rg.Waypoint("_"+n, dx, dy)
		alt := float32(ap.Elevation) + dalt
		wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}
		wp.FlyOver = fo
		wp.Speed = util.Select(slow, 70, 0)

		wps = append(wps, wp)
	}

	// Scale the points according to min speed so that if they have to be
	// fast, they're given more space to work with.
	sc := perf.Speed.Min / 80
	pdist := sc // pattern offset from runway

	// Slightly sketchy to do in lat-long but works in this case.
	sd := math.SignedPointLineDistance(wp.Location, rwy.Threshold, opp.Threshold)
	if sd < 0 {
		// coming from the left side of the extended runway centerline; just
		// add a point so that they enter the pattern at 45 degrees.
		addpt("enter45", 1, 1+pdist, 1000, false, true)
	} else {
		// coming from the right side; cross perpendicularly midfield, make
		// a descending right 270 and join the pattern.
		addpt("crossmidfield1", 0, -pdist, 1500, false, false)
		addpt("crossmidfield2", 0, pdist, 1500, true, false)
		addpt("crossmidfield3", 0, 1.5*pdist, 1500, true, true) // make some space to turn
		addpt("right270-1", sc, 2.5*pdist, 1250, false, true)
		addpt("right270-2", sc*2, 2*pdist, 1150, false, true)
		addpt("right270-2", sc*1.5, 1.5*pdist, 1000, false, true)
	}
	// both sides are the same from here.
	addpt("joindownwind", 0, pdist, 1000, false, true)
	addpt("base1", -1.5, pdist, 500, false, true)
	addpt("base2", -3, pdist/2, 250, false, true)
	addpt("base2", -1.5, 0, 150, false, true)
	addpt("threshold", -1, 0, 0, false, true)
	// Last point is at the far end of the runway just to give plenty of
	// slop to make sure we hit it so the aircraft is deleted.
	addpt("fin", 1, 0, 0, false, true)

	wps[len(wps)-1].Delete = true

	return wps
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

		// Is it a lat-long specifier like 4900N/05000W? We need to patch
		// things up if so since we use '/' to delimit our own specifiers
		// after fixes.
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
				// Reconstitute the fix in the first element of components and
				// shift the rest (if any) down.
				components[0] += "/" + c1
				components = append(components[:1], components[2:]...)
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
					wp.HumanHandoff = true
				} else if strings.HasPrefix(f, "ho") {
					wp.TCPHandoff = f[2:]
				} else if f == "clearapp" {
					wp.ClearApproach = true
				} else if f == "flyover" {
					wp.FlyOver = true
				} else if f == "delete" {
					wp.Delete = true
				} else if f == "land" {
					wp.Land = true
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
				} else if f == "appr" {
					wp.OnApproach = true
				} else if f == "ph" {
					wp.PresentHeading = true
				} else if strings.HasPrefix(f, "airwork") {
					a := f[7:]
					radius, minutes := 7, 15
					i := 0
					for len(a) > 0 {
						if a[i] >= '0' && a[i] <= '9' {
							i++
						} else if n, err := strconv.Atoi(a[:i]); err != nil {
							return nil, fmt.Errorf("%v: parsing %q", f, a[:i])
						} else if a[i] == 'm' {
							minutes = n
							a = a[i+1:]
							i = 0
						} else if a[i] == 'n' && len(a) > i+1 && a[i+1] == 'm' {
							radius = n
							a = a[i+2:]
							i = 0
						} else {
							return nil, fmt.Errorf("unexpected suffix %q after %q in %q", a[i:], a[:i], f)
						}
					}
					if i > 0 {
						return nil, fmt.Errorf("unexpected numbers %q after %q", a, f)
					}
					wp.AirworkRadius = radius
					wp.AirworkMinutes = minutes
				} else if strings.HasPrefix(f, "radius") {
					rstr := f[6:]
					if rad, err := strconv.ParseFloat(rstr, 32); err != nil {
						return nil, err
					} else {
						wp.Radius = float32(rad)
					}
				} else if strings.HasPrefix(f, "shift") {
					sstr := f[5:]
					if shift, err := strconv.ParseFloat(sstr, 32); err != nil {
						return nil, err
					} else {
						wp.Shift = float32(shift)
					}
				} else if len(f) > 2 && f[:2] == "po" {
					wp.PointOut = f[2:]
				} else if strings.HasPrefix(f, "spsp") {
					wp.PrimaryScratchpad = f[4:]
				} else if f == "cpsp" {
					wp.ClearPrimaryScratchpad = true
				} else if strings.HasPrefix(f, "sssp") {
					wp.SecondaryScratchpad = f[4:]
				} else if f == "cssp" {
					wp.ClearSecondaryScratchpad = true
				} else if f == "tc" {
					wp.TransferComms = true
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

	return waypoints, nil
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

// Locator is a simple interface to abstract looking up the location of a
// named thing (e.g. a fix).  This is mostly present so that the route code
// can call back into the ScenarioGroup to resolve locations accounting for
// fixes defined in a scenario, without exposing Scenario-related types to
// the aviation package.
type Locator interface {
	// Locate returns the lat-long coordinates of the named point if they
	// are available; the bool indicates whether the point was known.
	Locate(fix string) (math.Point2LL, bool)

	// If Locate fails, Similar can be called to get alternatives that are
	// similarly-spelled to be offered in error messages.
	Similar(fix string) []string
}

func (wa WaypointArray) InitializeLocations(loc Locator, nmPerLongitude float32, magneticVariation float32,
	allowSlop bool, e *util.ErrorLogger) WaypointArray {
	if len(wa) == 0 {
		return wa
	}

	defer e.CheckDepth(e.CurrentDepth())

	// Get the locations of all waypoints and cull the route after 250nm if cullFar is true.
	var prev math.Point2LL
	for i, wp := range wa {
		if e != nil {
			e.Push("Fix " + wp.Fix)
		}
		if pos, ok := loc.Locate(wp.Fix); !ok {
			if e != nil && !allowSlop {
				errstr := "unable to locate waypoint."
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
						errstr += " Did you mean: "
					}
					for _, s := range sim {
						errstr += fmt.Sprintf("%s (%.1fnm) ", s, dist[s])
					}
				}
				e.ErrorString("%s", errstr)
			}
		} else {
			wa[i].Location = pos

			d := math.NMDistance2LL(prev, wa[i].Location)
			if i > 1 && d > 200 && e != nil && !allowSlop && wa[i-1].Airway == "" {
				e.ErrorString("waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
					wa[i].Location.DDString(), wa[i-1].Fix, wa[i-1].Location.DDString(), d)
			}
			prev = wa[i].Location
		}

		if e != nil {
			e.Pop()
		}
	}

	// Now go through and expand out any airways into their constituent waypoints
	if slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.Airway != "" }) { // any airways?
		var wpExpanded []Waypoint
		for i, wp := range wa {
			wpExpanded = append(wpExpanded, wp)

			if wp.Airway != "" && i+1 < len(wa) {
				found := false
				wp0, wp1 := wp.Fix, wa[i+1].Fix
				for _, airway := range DB.Airways[wp.Airway] {
					if awps, ok := airway.WaypointsBetween(wp0, wp1); ok {
						for _, awp := range awps {
							if awp.Location, ok = loc.Locate(awp.Fix); ok {
								wpExpanded = append(wpExpanded, awp)
							} else if !allowSlop {
								e.ErrorString("%s: unable to locate fix in airway %s", awp.Fix, wp.Airway)
							}
						}
						found = true
						break
					}
				}

				if !found && e != nil && !allowSlop {
					e.ErrorString("%s: unable to find fix pair %s - %s in airway", wp.Airway, wp0, wp1)
				}
			}
		}
		wa = wpExpanded
	}

	if allowSlop {
		wa = util.FilterSliceInPlace(wa, func(wp Waypoint) bool { return !wp.Location.IsZero() })
	}

	// Do (DME) arcs after wp.Locations have been initialized
	for i, wp := range wa {
		if wp.Arc == nil {
			continue
		}

		if e != nil {
			e.Push("Fix " + wp.Fix)
		}

		if i+1 == len(wa) {
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
		p0, p1 := math.LL2NM(wp.Location, nmPerLongitude), math.LL2NM(wa[i+1].Location, nmPerLongitude)
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
		wp.Arc.Clockwise = x < 0

		if wp.Arc.Fix != "" {
			// Center point was specified
			var ok bool
			if wp.Arc.Center, ok = loc.Locate(wp.Arc.Fix); !ok {
				if e != nil {
					e.ErrorString("unable to locate arc center %q", wp.Arc.Fix)
					e.Pop()
				}
				continue
			}
		} else {
			// Just the arc length was specified; need to figure out the
			// center and radius of the circle that gives that.
			d := math.Distance2f(p0, p1)
			if wp.Arc.Length < d { // no bueno
				if math.Abs(wp.Arc.Length-d) < float32(0.1) { // allow some slop and just make it linear if it's close
					wp.Arc = nil
				} else if e != nil {
					e.ErrorString("distance between waypoints %.2fnm is greater than specified arc length %.2fnm",
						d, wp.Arc.Length)
				}
				if e != nil {
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

	return wa
}

///////////////////////////////////////////////////////////////////////////
// STAR

type STAR struct {
	Transitions     map[string]WaypointArray
	RunwayWaypoints map[string]WaypointArray
}

func (s STAR) Check(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

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

const routePrintFormat = "%-13s: %s\n"

func (s STAR) Print(name string) {
	for _, tr := range slices.Sorted(maps.Keys(s.Transitions)) {
		fmt.Printf(routePrintFormat, name+"."+tr, s.Transitions[tr].Encode())
	}

	for _, rwy := range slices.Sorted(maps.Keys(s.RunwayWaypoints)) {
		fmt.Printf(routePrintFormat, name+".RWY"+rwy, s.RunwayWaypoints[rwy].Encode())
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
func (a AltitudeRestriction) ClampRange(r [2]float32) (c [2]float32, ok bool) {
	// r: I could be at any of these altitudes and be fine for a future restriction
	// a: working backwards, we have this additional restriction, how does it limit r?
	// c: result
	ok = true
	c = r

	if a.Range[0] != 0 { // at or above
		ok = r[1] == 0 || r[1] >= a.Range[0]
		c[0] = math.Max(a.Range[0], r[0])
		if r[1] != 0 {
			c[1] = math.Max(a.Range[0], r[1])
		}
	}

	if a.Range[1] != 0 { // at or below
		ok = ok && c[0] <= a.Range[1]
		c[0] = math.Min(c[0], a.Range[1])
		c[1] = math.Min(c[1], a.Range[1])
	}

	return
}

func (a AltitudeRestriction) RadioTransmission() *RadioTransmission {
	if a.Range[0] != 0 {
		if a.Range[1] == a.Range[0] {
			return MakeRadioTransmission("at {alt}", a.Range[0])
		} else if a.Range[1] != 0 {
			return MakeRadioTransmission("between {alt} and {alt}", a.Range[0], a.Range[1])
		} else {
			return MakeRadioTransmission("at or above {alt}", a.Range[0])
		}
	} else if a.Range[1] != 0 {
		return MakeRadioTransmission("at or below {alt}", a.Range[1])
	} else {
		return nil
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
	airports map[string]*Airport, controlPositions map[string]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())
	if len(of.Waypoints) < 2 {
		e.ErrorString("must provide at least two \"waypoints\" for overflight")
	}

	of.Waypoints = of.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

	of.Waypoints[len(of.Waypoints)-1].Delete = true
	of.Waypoints[len(of.Waypoints)-1].FlyOver = true

	of.Waypoints.CheckOverflight(e, controlPositions, checkScratchpad)

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
		e.ErrorString("Must specify \"initial_controller\".")
	} else if _, ok := controlPositions[of.InitialController]; !ok {
		e.ErrorString("controller %q not found for \"initial_controller\"", of.InitialController)
	}

	if !checkScratchpad(of.Scratchpad) {
		e.ErrorString("%s: invalid scratchpad", of.Scratchpad)
	}
	if !checkScratchpad(of.SecondaryScratchpad) {
		e.ErrorString("%s: invalid secondary scratchpad", of.SecondaryScratchpad)
	}
}

///////////////////////////////////////////////////////////////////////////
// RouteGenerator

// RouteGenerator is a utility class for describing lateral routes with
// respect to a local coordinate system. The user provides two points
// (generally the endpoints of a runway) which are then at (-1,0) and
// (1,0) in the coordinate system. The y axis is perpendicular to the vector
// between the two points and points to the left of it. (Thus, note that
// lengths in the two dimensions are different.)
type RouteGenerator struct {
	p0, p1         [2]float32
	origin         [2]float32
	xvec, yvec     [2]float32 // basis vectors
	nmPerLongitude float32
}

func MakeRouteGenerator(p0ll, p1ll math.Point2LL, nmPerLongitude float32) RouteGenerator {
	rg := RouteGenerator{
		p0:             math.LL2NM(p0ll, nmPerLongitude),
		p1:             math.LL2NM(p1ll, nmPerLongitude),
		nmPerLongitude: nmPerLongitude,
	}
	rg.origin = math.Mid2f(rg.p0, rg.p1)
	rg.xvec = math.Scale2f(math.Sub2f(rg.p1, rg.p0), 0.5)
	rg.yvec = math.Normalize2f([2]float32{-rg.xvec[1], rg.xvec[0]})
	return rg
}

func (rg RouteGenerator) Waypoint(name string, dx, dy float32) Waypoint {
	p := math.Add2f(rg.origin, math.Add2f(math.Scale2f(rg.xvec, dx), math.Scale2f(rg.yvec, dy)))
	return Waypoint{
		Fix:      name,
		Location: math.NM2LL(p, rg.nmPerLongitude),
	}
}
