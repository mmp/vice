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

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// Waypoint

type WaypointFlags uint32

const (
	WaypointFlagPresentHeading WaypointFlags = 1 << iota
	WaypointFlagNoPT
	WaypointFlagHumanHandoff
	WaypointFlagClearApproach
	WaypointFlagFlyOver
	WaypointFlagDelete
	WaypointFlagLand
	WaypointFlagIAF
	WaypointFlagIF
	WaypointFlagFAF
	WaypointFlagOnSID
	WaypointFlagOnSTAR
	WaypointFlagOnApproach
	WaypointFlagClearPrimaryScratchpad
	WaypointFlagClearSecondaryScratchpad
	WaypointFlagTransferComms
	WaypointFlagTurnLeft
	WaypointFlagTurnRight
	WaypointFlagHasAltRestriction
)

// Waypoint is the core waypoint struct. Most waypoints only use Fix,
// Location, and a few flags; Extra fields are heap-allocated only when
// needed. AltRestriction, Heading, and Speed are inline because they
// are used by ~48% of waypoints, avoiding a heap allocation for those.
type Waypoint struct {
	Fix            string              `json:"fix"`
	Location       math.Point2LL       `json:"location,omitempty"`
	AltRestriction AltitudeRestriction // valid iff WaypointFlagHasAltRestriction set
	Extra          *WaypointExtra
	Flags          WaypointFlags
	Heading        int16 // 0 = unset
	Speed          int16 // 0 = unset
}

// WaypointExtra holds the rarely-used fields, heap-allocated only when needed.
type WaypointExtra struct {
	ProcedureTurn             *ProcedureTurn
	Arc                       *DMEArc
	HandoffController         ControlPosition
	PointOut                  ControlPosition
	GoAroundContactController ControlPosition
	Airway                    string
	PrimaryScratchpad         string
	SecondaryScratchpad       string
	Radius                    float32
	Shift                     float32
	ClimbAltitude             int16 // hundreds of feet; 0 = unset
	DescendAltitude           int16 // hundreds of feet; 0 = unset
	AirworkRadius             int8
	AirworkMinutes            int8
}

// InitExtra allocates Extra if nil and returns it.
func (wp *Waypoint) InitExtra() *WaypointExtra {
	if wp.Extra == nil {
		wp.Extra = &WaypointExtra{}
	}
	return wp.Extra
}

// Flag readers (value receiver)
func (wp Waypoint) PresentHeading() bool { return wp.Flags&WaypointFlagPresentHeading != 0 }
func (wp Waypoint) NoPT() bool           { return wp.Flags&WaypointFlagNoPT != 0 }
func (wp Waypoint) HumanHandoff() bool   { return wp.Flags&WaypointFlagHumanHandoff != 0 }
func (wp Waypoint) ClearApproach() bool  { return wp.Flags&WaypointFlagClearApproach != 0 }
func (wp Waypoint) FlyOver() bool        { return wp.Flags&WaypointFlagFlyOver != 0 }
func (wp Waypoint) Delete() bool         { return wp.Flags&WaypointFlagDelete != 0 }
func (wp Waypoint) Land() bool           { return wp.Flags&WaypointFlagLand != 0 }
func (wp Waypoint) IAF() bool            { return wp.Flags&WaypointFlagIAF != 0 }
func (wp Waypoint) IF() bool             { return wp.Flags&WaypointFlagIF != 0 }
func (wp Waypoint) FAF() bool            { return wp.Flags&WaypointFlagFAF != 0 }
func (wp Waypoint) OnSID() bool          { return wp.Flags&WaypointFlagOnSID != 0 }
func (wp Waypoint) OnSTAR() bool         { return wp.Flags&WaypointFlagOnSTAR != 0 }
func (wp Waypoint) OnApproach() bool     { return wp.Flags&WaypointFlagOnApproach != 0 }
func (wp Waypoint) ClearPrimaryScratchpad() bool {
	return wp.Flags&WaypointFlagClearPrimaryScratchpad != 0
}
func (wp Waypoint) ClearSecondaryScratchpad() bool {
	return wp.Flags&WaypointFlagClearSecondaryScratchpad != 0
}
func (wp Waypoint) TransferComms() bool { return wp.Flags&WaypointFlagTransferComms != 0 }

func (wp Waypoint) Turn() TurnDirection {
	if wp.Flags&WaypointFlagTurnLeft != 0 {
		return TurnLeft
	}
	if wp.Flags&WaypointFlagTurnRight != 0 {
		return TurnRight
	}
	return TurnClosest
}

// Flag setters (pointer receiver)
func (wp *Waypoint) setFlag(f WaypointFlags, v bool) {
	if v {
		wp.Flags |= f
	} else {
		wp.Flags &^= f
	}
}

func (wp *Waypoint) SetPresentHeading(v bool) { wp.setFlag(WaypointFlagPresentHeading, v) }
func (wp *Waypoint) SetNoPT(v bool)           { wp.setFlag(WaypointFlagNoPT, v) }
func (wp *Waypoint) SetHumanHandoff(v bool)   { wp.setFlag(WaypointFlagHumanHandoff, v) }
func (wp *Waypoint) SetClearApproach(v bool)  { wp.setFlag(WaypointFlagClearApproach, v) }
func (wp *Waypoint) SetFlyOver(v bool)        { wp.setFlag(WaypointFlagFlyOver, v) }
func (wp *Waypoint) SetDelete(v bool)         { wp.setFlag(WaypointFlagDelete, v) }
func (wp *Waypoint) SetLand(v bool)           { wp.setFlag(WaypointFlagLand, v) }
func (wp *Waypoint) SetIAF(v bool)            { wp.setFlag(WaypointFlagIAF, v) }
func (wp *Waypoint) SetIF(v bool)             { wp.setFlag(WaypointFlagIF, v) }
func (wp *Waypoint) SetFAF(v bool)            { wp.setFlag(WaypointFlagFAF, v) }
func (wp *Waypoint) SetOnSID(v bool)          { wp.setFlag(WaypointFlagOnSID, v) }
func (wp *Waypoint) SetOnSTAR(v bool)         { wp.setFlag(WaypointFlagOnSTAR, v) }
func (wp *Waypoint) SetOnApproach(v bool)     { wp.setFlag(WaypointFlagOnApproach, v) }
func (wp *Waypoint) SetClearPrimaryScratchpad(v bool) {
	wp.setFlag(WaypointFlagClearPrimaryScratchpad, v)
}
func (wp *Waypoint) SetClearSecondaryScratchpad(v bool) {
	wp.setFlag(WaypointFlagClearSecondaryScratchpad, v)
}
func (wp *Waypoint) SetTransferComms(v bool) { wp.setFlag(WaypointFlagTransferComms, v) }

func (wp *Waypoint) SetTurn(t TurnDirection) {
	wp.Flags &^= WaypointFlagTurnLeft | WaypointFlagTurnRight
	switch t {
	case TurnLeft:
		wp.Flags |= WaypointFlagTurnLeft
	case TurnRight:
		wp.Flags |= WaypointFlagTurnRight
	}
}

// AltitudeRestriction returns a pointer to the inline restriction if the flag is set, else nil.
func (wp *Waypoint) AltitudeRestriction() *AltitudeRestriction {
	if wp.Flags&WaypointFlagHasAltRestriction != 0 {
		return &wp.AltRestriction
	}
	return nil
}

// SetAltitudeRestriction stores the restriction inline and sets the flag.
func (wp *Waypoint) SetAltitudeRestriction(ar AltitudeRestriction) {
	wp.AltRestriction = ar
	wp.Flags |= WaypointFlagHasAltRestriction
}

// Extra field readers (value receiver, nil-safe)
func (wp Waypoint) ProcedureTurn() *ProcedureTurn {
	if wp.Extra != nil {
		return wp.Extra.ProcedureTurn
	}
	return nil
}
func (wp Waypoint) Arc() *DMEArc {
	if wp.Extra != nil {
		return wp.Extra.Arc
	}
	return nil
}
func (wp Waypoint) HandoffController() ControlPosition {
	if wp.Extra != nil {
		return wp.Extra.HandoffController
	}
	return ""
}
func (wp Waypoint) PointOut() ControlPosition {
	if wp.Extra != nil {
		return wp.Extra.PointOut
	}
	return ""
}
func (wp Waypoint) GoAroundContactController() ControlPosition {
	if wp.Extra != nil {
		return wp.Extra.GoAroundContactController
	}
	return ""
}
func (wp Waypoint) Airway() string {
	if wp.Extra != nil {
		return wp.Extra.Airway
	}
	return ""
}
func (wp Waypoint) PrimaryScratchpad() string {
	if wp.Extra != nil {
		return wp.Extra.PrimaryScratchpad
	}
	return ""
}
func (wp Waypoint) SecondaryScratchpad() string {
	if wp.Extra != nil {
		return wp.Extra.SecondaryScratchpad
	}
	return ""
}
func (wp Waypoint) Radius() float32 {
	if wp.Extra != nil {
		return wp.Extra.Radius
	}
	return 0
}
func (wp Waypoint) Shift() float32 {
	if wp.Extra != nil {
		return wp.Extra.Shift
	}
	return 0
}
func (wp Waypoint) AirworkRadius() int {
	if wp.Extra != nil {
		return int(wp.Extra.AirworkRadius)
	}
	return 0
}
func (wp Waypoint) AirworkMinutes() int {
	if wp.Extra != nil {
		return int(wp.Extra.AirworkMinutes)
	}
	return 0
}
func (wp Waypoint) ClimbAltitude() int {
	if wp.Extra != nil {
		return int(wp.Extra.ClimbAltitude) * 100
	}
	return 0
}
func (wp Waypoint) DescendAltitude() int {
	if wp.Extra != nil {
		return int(wp.Extra.DescendAltitude) * 100
	}
	return 0
}

func (wp Waypoint) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("fix", wp.Fix)}
	if ar := wp.AltitudeRestriction(); ar != nil {
		attrs = append(attrs, slog.Any("altitude_restriction", ar))
	}
	if wp.Speed != 0 {
		attrs = append(attrs, slog.Int("speed", int(wp.Speed)))
	}
	if wp.Heading != 0 {
		attrs = append(attrs, slog.Int("heading", int(wp.Heading)))
	}
	if wp.PresentHeading() {
		attrs = append(attrs, slog.Bool("present_heading", true))
	}
	if pt := wp.ProcedureTurn(); pt != nil {
		attrs = append(attrs, slog.Any("procedure_turn", pt))
	}
	if wp.IAF() {
		attrs = append(attrs, slog.Bool("IAF", true))
	}
	if wp.IF() {
		attrs = append(attrs, slog.Bool("IF", true))
	}
	if wp.FAF() {
		attrs = append(attrs, slog.Bool("FAF", true))
	}
	if wp.NoPT() {
		attrs = append(attrs, slog.Bool("no_pt", true))
	}
	if wp.HumanHandoff() {
		attrs = append(attrs, slog.Bool("human_handoff", true))
	}
	if hc := wp.HandoffController(); hc != "" {
		attrs = append(attrs, slog.String("tcp_handoff", string(hc)))
	}
	if po := wp.PointOut(); po != "" {
		attrs = append(attrs, slog.String("pointout", string(po)))
	}
	if wp.ClearApproach() {
		attrs = append(attrs, slog.Bool("clear_approach", true))
	}
	if wp.FlyOver() {
		attrs = append(attrs, slog.Bool("fly_over", true))
	}
	if wp.Delete() {
		attrs = append(attrs, slog.Bool("delete", true))
	}
	if wp.Land() {
		attrs = append(attrs, slog.Bool("land", true))
	}
	if arc := wp.Arc(); arc != nil {
		attrs = append(attrs, slog.Any("arc", arc))
	}
	if aw := wp.Airway(); aw != "" {
		attrs = append(attrs, slog.String("airway", aw))
	}
	if wp.OnSID() {
		attrs = append(attrs, slog.Bool("on_sid", true))
	}
	if wp.OnSTAR() {
		attrs = append(attrs, slog.Bool("on_star", true))
	}
	if wp.OnApproach() {
		attrs = append(attrs, slog.Bool("on_approach", true))
	}
	if ps := wp.PrimaryScratchpad(); ps != "" {
		attrs = append(attrs, slog.String("primary_scratchpad", ps))
	}
	if wp.ClearPrimaryScratchpad() {
		attrs = append(attrs, slog.Bool("clear_primary_scratchpad", true))
	}
	if ss := wp.SecondaryScratchpad(); ss != "" {
		attrs = append(attrs, slog.String("secondary_scratchpad", ss))
	}
	if wp.ClearSecondaryScratchpad() {
		attrs = append(attrs, slog.Bool("clear_secondary_scratchpad", true))
	}
	if wp.TransferComms() {
		attrs = append(attrs, slog.Bool("transfer_comms", true))
	}
	if ca := wp.ClimbAltitude(); ca != 0 {
		attrs = append(attrs, slog.Int("climb_altitude", ca))
	}
	if da := wp.DescendAltitude(); da != 0 {
		attrs = append(attrs, slog.Int("descend_altitude", da))
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

// HasHumanHandoff returns true if any waypoint has HumanHandoff set.
func (wa WaypointArray) HasHumanHandoff() bool {
	return slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.HumanHandoff() })
}

func (wa WaypointArray) Encode() string {
	var entries []string
	for _, w := range wa {
		s := w.Fix
		if ar := w.AltitudeRestriction(); ar != nil {
			s += "/a" + ar.Encoded()
		}
		if w.Speed != 0 {
			s += fmt.Sprintf("/s%d", w.Speed)
		}
		if pt := w.ProcedureTurn(); pt != nil {
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
		if w.IAF() {
			s += "/iaf"
		}
		if w.IF() {
			s += "/if"
		}
		if w.FAF() {
			s += "/faf"
		}
		if w.NoPT() {
			s += "/nopt"
		}
		if w.HumanHandoff() {
			s += "/ho"
		}
		if hc := w.HandoffController(); hc != "" {
			s += "/ho" + string(hc)
		}
		if po := w.PointOut(); po != "" {
			s += "/po" + string(po)
		}
		if w.ClearApproach() {
			s += "/clearapp"
		}
		if w.FlyOver() {
			s += "/flyover"
		}
		if w.Delete() {
			s += "/delete"
		}
		if w.Land() {
			s += "/land"
		}
		if w.Heading != 0 {
			s += fmt.Sprintf("/h%d", w.Heading)
		}
		if w.PresentHeading() {
			s += "/ph"
		}
		if arc := w.Arc(); arc != nil {
			if arc.Fix != "" {
				s += fmt.Sprintf("/arc%.1f%s", arc.Radius, arc.Fix)
			} else {
				s += fmt.Sprintf("/arc%.1f", arc.Length)
			}
		}
		if aw := w.Airway(); aw != "" {
			s += "/airway" + aw
		}
		if w.OnSID() {
			s += "/sid"
		}
		if w.OnSTAR() {
			s += "/star"
		}
		if w.OnApproach() {
			s += "/appr"
		}
		if w.AirworkRadius() != 0 {
			s += fmt.Sprintf("/airwork%dnm%dm", w.AirworkRadius(), w.AirworkMinutes())
		}
		if w.Radius() != 0 {
			s += fmt.Sprintf("/radius%.1f", w.Radius())
		}
		if w.Shift() != 0 {
			s += fmt.Sprintf("/shift%.1f", w.Shift())
		}
		if ps := w.PrimaryScratchpad(); ps != "" {
			s += "/spsp" + ps
		}
		if w.ClearPrimaryScratchpad() {
			s += "/cpsp"
		}
		if ss := w.SecondaryScratchpad(); ss != "" {
			s += "/sssp" + ss
		}
		if w.ClearSecondaryScratchpad() {
			s += "/cssp"
		}
		if w.TransferComms() {
			s += "/tc"
		}
		if ca := w.ClimbAltitude(); ca != 0 {
			s += fmt.Sprintf("/c%d", ca/100)
		}
		if da := w.DescendAltitude(); da != 0 {
			s += fmt.Sprintf("/d%d", da/100)
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
		wpAirway := wp.Airway()
		if airway != "" && wpAirway == airway {
			// This fix was automatically added for an airway so don't include it here.
			continue
		}
		r = append(r, wp.Fix)

		if wpAirway != airway {
			if wpAirway != "" {
				r = append(r, wpAirway)
			}
			airway = wpAirway
		}
	}
	return strings.Join(r, " ")
}

func (wa WaypointArray) CheckDeparture(e *util.ErrorLogger, controllers map[ControlPosition]*Controller, checkScratchpads func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, controllers, checkScratchpads)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in departure")
		}
		if war := wp.AltitudeRestriction(); war != nil {
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

func (wa WaypointArray) checkBasics(e *util.ErrorLogger, controllers map[ControlPosition]*Controller, checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	for i, wp := range wa {
		e.Push(wp.Fix)
		if wp.Speed < 0 || wp.Speed > 300 {
			e.ErrorString("invalid speed restriction %d", wp.Speed)
		}

		if wp.AirworkMinutes() > 0 {
			if ar := wp.AltitudeRestriction(); ar == nil {
				e.ErrorString("Must provide altitude range via \"/aXXX-YYY\" with /airwork")
			} else if ar.Range[0] == 0 || ar.Range[1] == 0 {
				e.ErrorString("Must provide top and bottom in altitude range \"/aXXX-YYY\" with /airwork")
			} else if ar.Range[1]-ar.Range[0] < 2000 {
				e.ErrorString("Must provide at least 2,000' of altitude range with /airwork")
			}
		}

		if po := wp.PointOut(); po != "" {
			if !util.MapContains(controllers,
				func(_ ControlPosition, ctrl *Controller) bool {
					return ctrl.PositionId() == po
				}) {
				e.ErrorString("No controller found with id %q for point out", po)
			}
		}

		if hc := wp.HandoffController(); hc != "" {
			if !util.MapContains(controllers,
				func(_ ControlPosition, ctrl *Controller) bool {
					return ctrl.PositionId() == ControlPosition(hc)
				}) {
				e.ErrorString("No controller found with id %q for handoff", hc)
			}
		}

		if wp.HumanHandoff() {
			// Check if any subsequent waypoints have a HandoffController
			for _, wfut := range wa[i:] {
				if wfut.HandoffController() != "" {
					e.ErrorString("Cannot have handoff to virtual controller after human handoff")
					break
				}
			}
		}

		if i == 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify /shift at the first fix in a route")
		}
		if wp.Radius() > 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify both /radius and /shift at the same fix")
		}

		if !checkScratchpad(wp.PrimaryScratchpad()) {
			e.ErrorString("%s: invalid primary_scratchpad", wp.PrimaryScratchpad())
		}
		if !checkScratchpad(wp.SecondaryScratchpad()) {
			e.ErrorString("%s: invalid secondary scratchpad", wp.SecondaryScratchpad())
		}

		e.Pop()
	}
}

func CheckApproaches(e *util.ErrorLogger, wps []WaypointArray, requireFAF bool, controllers map[ControlPosition]*Controller,
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
			if wp.FAF() {
				foundFAF = true
			}
		}
	}
	if requireFAF && !foundFAF {
		e.ErrorString("No /faf specifier found in approach")
	}
}

func (wa WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[ControlPosition]*Controller, approachAssigned bool,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, ctrl, checkScratchpad)
	wa.checkDescending(e)
	haveHO := false

	for _, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		if wp.ClearApproach() && !approachAssigned {
			e.ErrorString("/clearapp specified but no approach has been assigned")
		}
		if wp.HumanHandoff() {
			haveHO = true
		}
		if wp.TransferComms() && !haveHO {
			e.ErrorString("Must have /ho to handoff to a human controller at a waypoint prior to /tc")
		}
		e.Pop()
	}
}

func (wa WaypointArray) CheckOverflight(e *util.ErrorLogger, ctrl map[ControlPosition]*Controller, checkScratchpads func(string) bool) {
	wa.checkBasics(e, ctrl, checkScratchpads)
}

func (wa WaypointArray) checkDescending(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	// or at least, check not climbing...
	var lastMin float32
	var minFix string // last fix that established a specific minimum alt

	for _, wp := range wa {
		e.Push(wp.Fix)

		if war := wp.AltitudeRestriction(); war != nil {
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
	magneticVariation float32, airport string, lg *log.Logger) {
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

	for i := range w {
		wp := &w[i]
		if rad := wp.Radius(); rad > 0 {
			// Work in nm coordinates
			p := math.LL2NM(wp.Location, nmPerLongitude)

			// radius and theta
			r := math.Sqrt(rrad) * rad // equi-area mapping
			const Pi = 3.1415926535
			t := 2 * Pi * rtheta

			pp := math.Add2f(p, math.Scale2f(math.SinCos(t), r))
			wp.Location = math.NM2LL(pp, nmPerLongitude)
			wp.InitExtra().Radius = 0 // clean up

			rtheta = jitter(rtheta)
			rrad = jitter(rrad)
		} else if sh := wp.Shift(); sh > 0 {
			p0, p1 := math.LL2NM(w[i-1].Location, nmPerLongitude), math.LL2NM(w[i].Location, nmPerLongitude)
			v := math.Normalize2f(math.Sub2f(p1, p0))
			t := math.Lerp(rrad, -sh, sh)
			p := math.Add2f(p1, math.Scale2f(v, t))
			wp.Location = math.NM2LL(p, nmPerLongitude)

			wp.InitExtra().Shift = 0 // clean up

			rrad = jitter(rrad)
		}

		if randomizeAltitudeRange {
			if ar := wp.AltitudeRestriction(); ar != nil {
				low, high := ar.Range[0], ar.Range[1]
				// We should clamp low to be a few hundred feet AGL, but
				// hopefully we'll generally be given a full range.
				if high == 0 {
					high = low + 3000
				}
				// Cap at VFR max (17,500') since randomizeAltitudeRange is only true for VFR.
				// This prevents VFR aircraft from being assigned altitudes in Class A airspace.
				const maxVFRAltitude = 17500
				high = min(high, maxVFRAltitude)
				low = min(low, maxVFRAltitude)
				alt := math.Lerp(ralt, low, high)

				// Update the altitude restriction to just be the single altitude.
				wp.SetAltitudeRestriction(AltitudeRestriction{Range: [2]float32{alt, alt}})

				ralt = jitter(ralt)
			}
		}
	}
}

// Takes waypoints up to the one with the Land specifier. Rewrite that one and then append the landing route.
func AppendVFRLanding(wps []Waypoint, perf AircraftPerformance, airport string, windDir float32, nmPerLongitude float32,
	magneticVariation float32, lg *log.Logger) []Waypoint {
	wp := &wps[len(wps)-1]
	wp.SetLand(false)
	wp.SetDelete(false)

	ap, ok := DB.Airports[airport]
	if !ok {
		lg.Errorf("%s: couldn't find arrival airport", airport)
		wp.SetDelete(true)
		return wps // best we can do
	}

	rwy, opp := ap.SelectBestRunway(windDir, magneticVariation)
	if rwy == nil || opp == nil {
		lg.Error("couldn't find a runway to land on", slog.String("airport", airport), slog.Any("runways", ap.Runways))
		wp.SetDelete(true)
		return wps // best we can do
	}

	rg := MakeRouteGenerator(rwy.Threshold, opp.Threshold, nmPerLongitude)

	// Calculate aircraft heading from current position to runway threshold
	aircraftHeading := math.Heading2LL(wp.Location, rwy.Threshold, nmPerLongitude, magneticVariation)

	// Check if aircraft is aligned with runway (within +/- 90 degrees)
	headingDiff := math.HeadingDifference(aircraftHeading, rwy.Heading)

	addpt := func(n string, dx, dy, dalt float32, fo bool, slow bool) {
		wp := rg.Waypoint("_"+n, dx, dy)
		alt := float32(ap.Elevation) + dalt
		wp.SetAltitudeRestriction(AltitudeRestriction{Range: [2]float32{alt, alt}})
		wp.SetFlyOver(fo)
		if slow {
			wp.Speed = 70
		}

		wps = append(wps, wp)
	}

	if headingDiff <= 60 {
		// Aircraft is aligned with runway - create straight-in approach

		// Waypoint 1 mile out at 300' AGL on extended centerline
		addpt("lineup", -2, 0, 300, false, true)
		addpt("threshold", -1, 0, 0, true, true)
		addpt("end", 1, 0, 0, true, true)
	} else {
		// Aircraft not aligned - use standard traffic pattern
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
	}

	wps[len(wps)-1].SetDelete(true)

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
				waypoints[nwp-1].InitExtra().Airway = components[0]
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
					wp.SetHumanHandoff(true)
				} else if strings.HasPrefix(f, "ho") {
					wp.InitExtra().HandoffController = ControlPosition(f[2:])
				} else if f == "clearapp" {
					wp.SetClearApproach(true)
				} else if f == "flyover" {
					wp.SetFlyOver(true)
				} else if f == "delete" {
					wp.SetDelete(true)
				} else if f == "land" {
					wp.SetLand(true)
				} else if f == "iaf" {
					wp.SetIAF(true)
				} else if f == "if" {
					wp.SetIF(true)
				} else if f == "faf" {
					wp.SetFAF(true)
				} else if f == "sid" {
					wp.SetOnSID(true)
				} else if f == "star" {
					wp.SetOnSTAR(true)
				} else if f == "appr" {
					wp.SetOnApproach(true)
				} else if f == "ph" {
					wp.SetPresentHeading(true)
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
					e := wp.InitExtra()
					e.AirworkRadius = int8(radius)
					e.AirworkMinutes = int8(minutes)
				} else if strings.HasPrefix(f, "radius") {
					rstr := f[6:]
					if rad, err := strconv.ParseFloat(rstr, 32); err != nil {
						return nil, err
					} else {
						wp.InitExtra().Radius = float32(rad)
					}
				} else if strings.HasPrefix(f, "shift") {
					sstr := f[5:]
					if shift, err := strconv.ParseFloat(sstr, 32); err != nil {
						return nil, err
					} else {
						wp.InitExtra().Shift = float32(shift)
					}
				} else if len(f) > 2 && f[:2] == "po" {
					wp.InitExtra().PointOut = ControlPosition(f[2:])
				} else if strings.HasPrefix(f, "spsp") {
					wp.InitExtra().PrimaryScratchpad = f[4:]
				} else if f == "cpsp" {
					wp.SetClearPrimaryScratchpad(true)
				} else if strings.HasPrefix(f, "sssp") {
					wp.InitExtra().SecondaryScratchpad = f[4:]
				} else if f == "cssp" {
					wp.SetClearSecondaryScratchpad(true)
				} else if f == "tc" {
					wp.SetTransferComms(true)
				} else if (len(f) >= 4 && f[:4] == "pt45") || len(f) >= 5 && f[:5] == "lpt45" {
					pt := wp.InitExtra()
					if pt.ProcedureTurn == nil {
						pt.ProcedureTurn = &ProcedureTurn{}
					}
					pt.ProcedureTurn.Type = PTStandard45
					pt.ProcedureTurn.RightTurns = f[0] == 'p'

					extent := f[4:]
					if !pt.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if (len(f) >= 5 && f[:5] == "hilpt") || (len(f) >= 6 && f[:6] == "lhilpt") {
					pt := wp.InitExtra()
					if pt.ProcedureTurn == nil {
						pt.ProcedureTurn = &ProcedureTurn{}
					}
					pt.ProcedureTurn.Type = PTRacetrack
					pt.ProcedureTurn.RightTurns = f[0] == 'h'

					extent := f[5:]
					if !pt.ProcedureTurn.RightTurns {
						extent = extent[1:]
					}
					if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
						return nil, err
					}
				} else if len(f) >= 4 && f[:3] == "pta" {
					pt := wp.InitExtra()
					if pt.ProcedureTurn == nil {
						pt.ProcedureTurn = &ProcedureTurn{}
					}

					if alt, err := strconv.Atoi(f[3:]); err == nil {
						pt.ProcedureTurn.ExitAltitude = alt
					} else {
						return nil, fmt.Errorf("%s: error parsing procedure turn exit altitude: %v", f[3:], err)
					}
				} else if f == "nopt" {
					wp.SetNoPT(true)
				} else if f == "nopt180" {
					pt := wp.InitExtra()
					if pt.ProcedureTurn == nil {
						pt.ProcedureTurn = &ProcedureTurn{}
					}
					pt.ProcedureTurn.Entry180NoPT = true
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
						wp.InitExtra().Arc = &DMEArc{
							Length: float32(v),
						}
					} else {
						wp.InitExtra().Arc = &DMEArc{
							Fix:    spec[rend:],
							Radius: float32(v),
						}
					}
				} else if len(f) >= 7 && f[:6] == "airway" {
					wp.InitExtra().Airway = f[6:]

					// Do these last since they only match the first character...
				} else if f[0] == 'a' {
					ar, err := ParseAltitudeRestriction(f[1:])
					if err != nil {
						return nil, err
					}
					wp.SetAltitudeRestriction(*ar)
				} else if f[0] == 's' {
					kts, err := strconv.Atoi(f[1:])
					if err != nil {
						return nil, fmt.Errorf("%s: error parsing number after speed restriction: %v", f[1:], err)
					}
					wp.Speed = int16(kts)
				} else if f[0] == 'h' { // after "ho" and "hilpt" check...
					if hdg, err := strconv.Atoi(f[1:]); err != nil {
						return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", f[1:], err)
					} else if hdg < 0 || hdg > 360 {
						return nil, fmt.Errorf("%s: waypoint outbound heading must be between 0-360: %v", f[1:], err)
					} else {
						wp.Heading = int16(hdg)
					}
				} else if f[0] == 'l' {
					if hdg, err := strconv.Atoi(f[1:]); err != nil {
						return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", f[1:], err)
					} else if hdg < 0 || hdg > 360 {
						return nil, fmt.Errorf("%s: waypoint outbound heading must be between 0-360: %v", f[1:], err)
					} else {
						wp.Heading = int16(hdg)
						wp.SetTurn(TurnLeft)
					}
				} else if f[0] == 'r' {
					if hdg, err := strconv.Atoi(f[1:]); err != nil {
						return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", f[1:], err)
					} else if hdg < 0 || hdg > 360 {
						return nil, fmt.Errorf("%s: waypoint outbound heading must be between 0-360: %v", f[1:], err)
					} else {
						wp.Heading = int16(hdg)
						wp.SetTurn(TurnRight)
					}
				} else if f[0] == 'c' {
					alt, err := strconv.Atoi(f[1:])
					if err != nil {
						return nil, fmt.Errorf("%s: error parsing altitude after /c: %v", f[1:], err)
					}
					if alt < 0 || alt > 600 {
						return nil, fmt.Errorf("%s: climb altitude must be between 0 and 600 (in 100s of feet)", f)
					}
					wp.InitExtra().ClimbAltitude = int16(alt)
				} else if f[0] == 'd' {
					alt, err := strconv.Atoi(f[1:])
					if err != nil {
						return nil, fmt.Errorf("%s: error parsing altitude after /d: %v", f[1:], err)
					}
					if alt < 0 || alt > 600 {
						return nil, fmt.Errorf("%s: descend altitude must be between 0 and 600 (in 100s of feet)", f)
					}
					wp.InitExtra().DescendAltitude = int16(alt)

				} else {
					return nil, fmt.Errorf("%s: unknown fix modifier: %s", field, f)
				}
			}
		}

		if pt := wp.ProcedureTurn(); pt != nil && pt.Type == PTUndefined {
			return nil, fmt.Errorf("%s: no procedure turn specified for fix (e.g., pt45/hilpt) even though PT parameters were given", wp.Fix)
		}

		if wp.ClimbAltitude() != 0 && wp.DescendAltitude() != 0 {
			return nil, fmt.Errorf("%s: cannot specify both /c and /d at the same waypoint", wp.Fix)
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
			if i > 1 && d > 200 && e != nil && !allowSlop && wa[i-1].Airway() == "" {
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
	if slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.Airway() != "" }) { // any airways?
		var wpExpanded []Waypoint
		for i, wp := range wa {
			wpExpanded = append(wpExpanded, wp)

			if wp.Airway() != "" && i+1 < len(wa) {
				found := false
				wp0, wp1 := wp.Fix, wa[i+1].Fix
				for _, airway := range DB.Airways[wp.Airway()] {
					if awps, ok := airway.WaypointsBetween(wp0, wp1); ok {
						for _, awp := range awps {
							if awp.Location, ok = loc.Locate(awp.Fix); ok {
								wpExpanded = append(wpExpanded, awp)
							} else if !allowSlop {
								e.ErrorString("%s: unable to locate fix in airway %s", awp.Fix, wp.Airway())
							}
						}
						found = true
						break
					}
				}

				if !found && e != nil && !allowSlop {
					e.ErrorString("%s: unable to find fix pair %s - %s in airway", wp.Airway(), wp0, wp1)
				}
			}
		}
		wa = wpExpanded
	}

	if allowSlop {
		wa = util.FilterSliceInPlace(wa, func(wp Waypoint) bool { return !wp.Location.IsZero() })
	}

	// Do (DME) arcs after wp.Locations have been initialized
	for i := range wa {
		if wa[i].Arc() == nil {
			continue
		}

		if e != nil {
			e.Push("Fix " + wa[i].Fix)
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
		p0 := math.LL2NM(wa[i].Location, nmPerLongitude)
		p1 := math.LL2NM(wa[i+1].Location, nmPerLongitude)
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
		wa[i].InitExtra().Arc.Clockwise = x < 0

		if !wa[i].Extra.Arc.Initialize(loc, wa[i].Location, wa[i+1].Location, nmPerLongitude, magneticVariation, e) {
			wa[i].Extra.Arc = nil
		}

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
		return max(alt, a.Range[0])
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
		c[0] = max(a.Range[0], r[0])
		if r[1] != 0 {
			c[1] = max(a.Range[0], r[1])
		}
	}

	if a.Range[1] != 0 { // at or below
		ok = ok && c[0] <= a.Range[1]
		c[0] = min(c[0], a.Range[1])
		c[1] = min(c[1], a.Range[1])
	}

	return
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

// Initialize resolves the arc's center, radius, and initial heading from
// its specification. Clockwise must be set before calling. startLoc and
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
		delta := float32(util.Select(arc.Clockwise, -.01, .01))

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

	// Heading from the center of the arc to the start fix
	hfix := math.Heading2LL(arc.Center, startLoc, nmPerLongitude, magneticVariation)

	// Then perpendicular to that, depending on the arc's direction
	arc.InitialHeading = math.NormalizeHeading(hfix + float32(util.Select(arc.Clockwise, 90, -90)))

	return true
}

///////////////////////////////////////////////////////////////////////////
// Hold

// TurnDirection specifies the direction of a turn.
type TurnDirection int

const (
	TurnClosest TurnDirection = iota // default: turn the shortest direction
	TurnLeft
	TurnRight
)

func (t TurnDirection) String() string {
	return []string{"closest", "left", "right"}[int(t)]
}

// Hold represents a charted holding pattern from CIFP or HPF
type Hold struct {
	Fix             string  // Fix identifier where hold is located
	InboundCourse   float32 // Inbound magnetic course to the fix
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

func (h Hold) Entry(headingToFix float32) HoldEntry {
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
	InitialController   ControlPosition         `json:"initial_controller"`
	Scratchpad          string                  `json:"scratchpad"`
	SecondaryScratchpad string                  `json:"secondary_scratchpad"`
	Description         string                  `json:"description"`
	IsRNAV              bool                    `json:"is_rnav"`
	Airlines            []OverflightAirline     `json:"airlines"`
}

type OverflightAirline struct {
	AirlineSpecifier
	DepartureAirport string `json:"departure_airport"`
	ArrivalAirport   string `json:"arrival_airport"`
}

func (of *Overflight) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[string]*Airport, controlPositions map[ControlPosition]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())
	if len(of.Waypoints) < 2 {
		e.ErrorString("must provide at least two \"waypoints\" for overflight")
	}

	of.Waypoints = of.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

	of.Waypoints[len(of.Waypoints)-1].SetDelete(true)
	of.Waypoints[len(of.Waypoints)-1].SetFlyOver(true)

	of.Waypoints.CheckOverflight(e, controlPositions, checkScratchpad)

	if len(of.Airlines) == 0 {
		e.ErrorString("must specify at least one airline in \"airlines\"")
	}
	for i := range of.Airlines {
		of.Airlines[i].Check(e)

		if of.Airlines[i].DepartureAirport == "" {
			e.ErrorString("must specify \"departure_airport\"")
		} else if _, ok := airports[of.Airlines[i].DepartureAirport]; !ok {
			if _, ok := DB.Airports[of.Airlines[i].DepartureAirport]; !ok {
				e.ErrorString("departure airport %q is unknown", of.Airlines[i].DepartureAirport)
			}
		}

		if of.Airlines[i].ArrivalAirport == "" {
			e.ErrorString("must specify \"arrival_airport\"")
		} else if _, ok := airports[of.Airlines[i].ArrivalAirport]; !ok {
			if _, ok := DB.Airports[of.Airlines[i].ArrivalAirport]; !ok {
				e.ErrorString("arrival airport %q is unknown", of.Airlines[i].ArrivalAirport)
			}
		}
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
