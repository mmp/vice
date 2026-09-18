// aviation/route.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
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
	WaypointFlagNoPT WaypointFlags = 1 << iota
	WaypointFlagFlyOver
	WaypointFlagIAF
	WaypointFlagIF
	WaypointFlagFAF
	WaypointFlagOnSID
	WaypointFlagOnSTAR
	WaypointFlagOnApproach
	WaypointFlagTurnLeft
	WaypointFlagTurnRight
	WaypointFlagSyntheticCrossing
	WaypointFlagHasAltRestriction
	WaypointFlagHasSpeedRestriction
	WaypointFlagSequenceVFRLanding
)

// WaypointActionTerminationType indicates when a waypoint action group is complete.
type WaypointActionTerminationType int

const (
	WaypointActionNoTermination WaypointActionTerminationType = iota
	WaypointActionAltitude
	WaypointActionDME
	WaypointActionCourse
	WaypointActionRadial
	WaypointActionDistance
)

// WaypointActionTermination describes a non-fix condition for advancing to the
// next waypoint action group.
type WaypointActionTermination struct {
	Type            WaypointActionTerminationType
	Altitude        int
	AtOrAbove       bool // altitude: at or above (+) rather than at or below (-); DME: at or beyond rather than within
	DMEFix          string
	DMEDistance     float32
	DMEFixLocation  math.Point2LL
	DMEFixElevation int
	Distance        float32 // nm from where the group took effect
	Course          int16   // magnetic course to the next fix on the route
	// CourseFix is the navaid Course is a radial of, if any. A radial names
	// the line only, since a leg may fly it either inbound or outbound, so
	// the next fix gives the direction along it.
	CourseFix string
	// CourseFixVariation is the variation Course is referenced to: the
	// navaid's station declination, or the area's variation for a course
	// without one.
	CourseFixVariation float32
	Radial             int16 // magnetic radial of RadialFix to cross
	RadialFix          string
	RadialFixLocation  math.Point2LL
	// RadialFixVariation is the variation Radial is referenced to: the
	// navaid's station declination, or the area's variation for a fix
	// without one.
	RadialFixVariation float32
}

// WaypointHeadingAction is a heading or ground track to fly. A track with a
// Fix is that fix's radial: the aircraft joins the radial and follows it
// away from the fix.
type WaypointHeadingAction struct {
	Heading        int16
	Turn           TurnDirection
	Track          bool
	PresentHeading bool
	Fix            string
	FixLocation    math.Point2LL
	// FixVariation is the variation the radial is referenced to: the
	// navaid's station declination, or the area's variation for a fix
	// without one.
	FixVariation float32
}

// IsSet reports whether the action gives a heading to fly.
func (wha WaypointHeadingAction) IsSet() bool {
	return wha.Heading != 0 || wha.PresentHeading
}

// WaypointActions describes actions that take effect together after passing a
// waypoint or after an ordered action group's termination condition is met.
type WaypointActions struct {
	Heading WaypointHeadingAction

	HumanHandoff              bool
	HandoffController         ControlPosition
	PointOut                  ControlPosition
	ClearApproach             bool
	InterceptApproach         bool
	GoAroundContactController ControlPosition
	PrimaryScratchpad         string
	ClearPrimaryScratchpad    bool
	SecondaryScratchpad       string
	ClearSecondaryScratchpad  bool
	TransferComms             bool
	Delete                    bool
	Land                      bool

	ClimbAltitude   int // feet; 0 = unset
	DescendAltitude int // feet; 0 = unset
}

// HasSimActions reports whether the actions include any the sim carries
// out, which is to say any but the heading.
func (wa WaypointActions) HasSimActions() bool {
	return wa.HumanHandoff || wa.HandoffController != "" || wa.PointOut != "" ||
		wa.ClearApproach || wa.InterceptApproach || wa.GoAroundContactController != "" ||
		wa.PrimaryScratchpad != "" || wa.ClearPrimaryScratchpad ||
		wa.SecondaryScratchpad != "" || wa.ClearSecondaryScratchpad || wa.TransferComms ||
		wa.Delete || wa.Land || wa.ClimbAltitude != 0 || wa.DescendAltitude != 0
}

func (wa WaypointActions) Encoded() string {
	var s string
	if wa.Heading.IsSet() {
		s += wa.Heading.Encoded()
	}
	if wa.HumanHandoff {
		s += "/ho"
	}
	if hc := wa.HandoffController; hc != "" {
		s += "/ho" + string(hc)
	}
	if po := wa.PointOut; po != "" {
		s += "/po" + string(po)
	}
	if wa.ClearApproach {
		s += "/clearapp"
	}
	if wa.InterceptApproach {
		s += "/intercept"
	}
	if ps := wa.PrimaryScratchpad; ps != "" {
		s += "/spsp" + ps
	}
	if wa.ClearPrimaryScratchpad {
		s += "/cpsp"
	}
	if ss := wa.SecondaryScratchpad; ss != "" {
		s += "/sssp" + ss
	}
	if wa.ClearSecondaryScratchpad {
		s += "/cssp"
	}
	if wa.TransferComms {
		s += "/tc"
	}
	if ca := wa.ClimbAltitude; ca != 0 {
		s += fmt.Sprintf("/c%d", ca)
	}
	if da := wa.DescendAltitude; da != 0 {
		s += fmt.Sprintf("/d%d", da)
	}
	if wa.Delete {
		s += "/delete"
	}
	if wa.Land {
		s += "/land"
	}
	return s
}

// encodeActionGroups returns the groups in the form a "waypoint_actions"
// value takes them: route syntax without the leading slash.
func encodeActionGroups(groups []WaypointActionGroup) string {
	var sb strings.Builder
	for _, g := range groups {
		sb.WriteString(g.Encoded())
	}
	return strings.TrimPrefix(sb.String(), "/")
}

type WaypointActionGroup struct {
	Actions WaypointActions
	Until   WaypointActionTermination
}

func (wag WaypointActionGroup) Encoded() string {
	return wag.Actions.Encoded() + wag.Until.Encoded()
}

// Encoded returns the trigger in route syntax, e.g. "/@a500+", or "" for no
// termination.
func (wat WaypointActionTermination) Encoded() string {
	switch wat.Type {
	case WaypointActionAltitude:
		return fmt.Sprintf("/@a%d%s", wat.Altitude, util.Select(wat.AtOrAbove, "+", "-"))
	case WaypointActionDME:
		return fmt.Sprintf("/@%s-D%.1f%s", wat.DMEFix, wat.DMEDistance, util.Select(wat.AtOrAbove, "+", "-"))
	case WaypointActionCourse:
		if wat.CourseFix != "" {
			return fmt.Sprintf("/@crs%s-R%03d", wat.CourseFix, wat.Course)
		}
		return fmt.Sprintf("/@crs%03d", wat.Course)
	case WaypointActionRadial:
		return fmt.Sprintf("/@%s-R%03d", wat.RadialFix, wat.Radial)
	case WaypointActionDistance:
		return fmt.Sprintf("/@d%.1f", wat.Distance)
	default:
		return ""
	}
}

// ActionGroupHeadingKind describes how a waypoint's action groups steer the
// aircraft once it has passed the fix.
type ActionGroupHeadingKind int

const (
	// ActionGroupHeadingNone: the groups give no heading, so the aircraft
	// carries on along its route.
	ActionGroupHeadingNone ActionGroupHeadingKind = iota
	// ActionGroupHeadingAssigned: a heading the aircraft holds until a
	// controller intervenes, just as it would one the controller assigned.
	ActionGroupHeadingAssigned
	// ActionGroupHeadingManeuvers: a sequence of maneuvers the aircraft comes
	// back to its route from when the last of them ends.
	ActionGroupHeadingManeuvers
)

// ActionGroupHeading reports how groups are flown, along with the heading
// action when they assign one. A lone group with no termination is the only
// shape that becomes an assigned heading, and then only if it names a heading
// to hold rather than a course to track.
func ActionGroupHeading(groups []WaypointActionGroup) (WaypointHeadingAction, ActionGroupHeadingKind) {
	if len(groups) == 0 {
		return WaypointHeadingAction{}, ActionGroupHeadingNone
	}
	if len(groups) != 1 || groups[0].Until.Type != WaypointActionNoTermination {
		return WaypointHeadingAction{}, ActionGroupHeadingManeuvers
	}

	h := groups[0].Actions.Heading
	switch {
	case !h.IsSet():
		return h, ActionGroupHeadingNone
	case h.PresentHeading, !h.Track:
		return h, ActionGroupHeadingAssigned
	}
	return h, ActionGroupHeadingManeuvers
}

func (wha WaypointHeadingAction) Encoded() string {
	if wha.PresentHeading {
		return "/ph"
	}

	prefix := util.Select(wha.Track, "t", "h")
	switch wha.Turn {
	case TurnLeft:
		prefix = util.Select(wha.Track, "lt", "l")
	case TurnRight:
		prefix = util.Select(wha.Track, "rt", "r")
	}
	if wha.Fix != "" {
		return fmt.Sprintf("/%s%s-R%03d", prefix, wha.Fix, wha.Heading)
	}
	return fmt.Sprintf("/%s%03d", prefix, wha.Heading)
}

// WaypointActionEvent reports a waypoint's actions coming due. The waypoint
// comes along with them because a group behind a trigger runs when the
// aircraft may be several fixes past it, and actions like /land still need
// the fix's restrictions.
type WaypointActionEvent struct {
	Waypoint Waypoint
	Actions  WaypointActions
}

// Waypoint is the core waypoint struct. Most waypoints only use Fix,
// Location, and a few flags; Extra fields are heap-allocated only when
// needed. AltRestriction and SpdRestriction are inline because they are
// used by roughly half of all waypoints, avoiding a heap allocation for those.
type Waypoint struct {
	Fix            string              `json:"fix"`
	Location       math.Point2LL       `json:"location,omitempty"`
	AltRestriction AltitudeRestriction // valid iff WaypointFlagHasAltRestriction set
	SpdRestriction SpeedRestriction    // valid iff WaypointFlagHasSpeedRestriction set
	Extra          *WaypointExtra
	Flags          WaypointFlags
	VFRPhase       uint8 // VFRPhaseNone for non-VFR-pattern waypoints
}

// VFRPhase constants identify which leg of the VFR traffic pattern a waypoint belongs to.
const (
	VFRPhaseNone       uint8 = 0
	VFRPhaseRollout    uint8 = 1
	VFRPhaseUpwind     uint8 = 2
	VFRPhaseCrosswind  uint8 = 3
	VFRPhaseDownwind   uint8 = 4
	VFRPhaseBase       uint8 = 5
	VFRPhaseFinal      uint8 = 6
	VFRPhaseStraightIn uint8 = 7
	VFRPhaseOrbit      uint8 = 8
)

// WaypointExtra holds the rarely-used fields, heap-allocated only when needed.
type WaypointExtra struct {
	ProcedureTurn  *ProcedureTurn
	Arc            *DMEArc
	ActionGroups   []WaypointActionGroup
	Airway         string
	Radius         float32
	Shift          float32
	LegOffset      float32
	AirworkRadius  int8
	AirworkMinutes int8
}

// InitExtra allocates Extra if nil and returns it.
func (wp *Waypoint) InitExtra() *WaypointExtra {
	if wp.Extra == nil {
		wp.Extra = &WaypointExtra{}
	}
	return wp.Extra
}

// Clone returns a copy of wp that shares nothing with the original, so that
// modifying the copy leaves the original untouched.
func (wp Waypoint) Clone() Waypoint {
	if wp.Extra == nil {
		return wp
	}
	extra := *wp.Extra
	extra.ActionGroups = util.DuplicateSlice(extra.ActionGroups)
	if extra.Arc != nil {
		arc := *extra.Arc
		extra.Arc = &arc
	}
	if extra.ProcedureTurn != nil {
		pt := *extra.ProcedureTurn
		extra.ProcedureTurn = &pt
	}
	wp.Extra = &extra
	return wp
}

// CarryOverActions copies over actions from the given Waypoint prev; this is used when splicing
// approaches into an aircraft's route where we have two instances of the same fix. All of prev's
// action groups are flattened into one, so an action behind a trigger like /@a4000- survives the
// splice but runs at the fix rather than when the trigger is met.
func (wp Waypoint) CarryOverActions(prev Waypoint) Waypoint {
	var carried WaypointActions
	for _, group := range prev.ActionGroups() {
		carried.merge(group.Actions)
	}
	carried.Heading = WaypointHeadingAction{}

	wp = wp.Clone()
	wp.SetNoPT(wp.NoPT() || prev.NoPT())

	if carried.HasSimActions() {
		groups := wp.ActionGroups()
		if n := len(groups); n > 0 && groups[n-1].Until.Type == WaypointActionNoTermination {
			groups[n-1].Actions.merge(carried)
		} else {
			wp.InitExtra().ActionGroups = append(groups, WaypointActionGroup{Actions: carried})
		}
	}
	return wp
}

// MergeWith returns the one waypoint that stands in for wp and next, which
// name the same fix, when the route that ends at wp is spliced onto the one
// that starts at next. It carries what each of them gives: wp's geometry,
// restrictions, and flags, next's where wp has none, and both of their
// actions, next's after wp's own. Restrictions the two both give become the
// range that satisfies each.
func (wp Waypoint) MergeWith(next Waypoint) Waypoint {
	wp = wp.Clone()
	wp.Location = util.Select(wp.Location.IsZero(), next.Location, wp.Location)
	// The turn is the one onto the leg that reaches the fix, which is wp's.
	wp.SetTurn(util.Select(wp.Turn() == TurnClosest, next.Turn(), wp.Turn()))
	wp.Flags |= next.Flags &^ (WaypointFlagTurnLeft | WaypointFlagTurnRight |
		WaypointFlagHasAltRestriction | WaypointFlagHasSpeedRestriction)
	wp.VFRPhase = util.Select(wp.VFRPhase == VFRPhaseNone, next.VFRPhase, wp.VFRPhase)

	if ar := next.AltitudeRestriction(); ar != nil {
		if mine := wp.AltitudeRestriction(); mine == nil {
			wp.SetAltitudeRestriction(*ar)
		} else if rng, ok := mine.ClampRange(ar.Range); ok {
			wp.SetAltitudeRestriction(AltitudeRestriction{NavigationRestriction{Range: rng}})
		}
	}
	if sr := next.SpeedRestriction(); sr != nil {
		if mine := wp.SpeedRestriction(); mine == nil {
			wp.SetSpeedRestriction(*sr)
		} else if rng, ok := mine.ClampRange(sr.Range); ok && mine.IsMach == sr.IsMach {
			wp.SetSpeedRestriction(SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: rng},
				IsMach: sr.IsMach})
		}
	}

	if next.Extra == nil {
		return wp
	}

	x, nx := wp.InitExtra(), next.Extra
	if x.ProcedureTurn == nil && nx.ProcedureTurn != nil {
		pt := *nx.ProcedureTurn
		x.ProcedureTurn = &pt
	}
	if x.Arc == nil && nx.Arc != nil {
		arc := *nx.Arc
		x.Arc = &arc
	}
	x.Airway = util.Select(x.Airway == "", nx.Airway, x.Airway)
	x.Radius = util.Select(x.Radius == 0, nx.Radius, x.Radius)
	x.Shift = util.Select(x.Shift == 0, nx.Shift, x.Shift)
	x.LegOffset = util.Select(x.LegOffset == 0, nx.LegOffset, x.LegOffset)
	x.AirworkRadius = util.Select(x.AirworkRadius == 0, nx.AirworkRadius, x.AirworkRadius)
	x.AirworkMinutes = util.Select(x.AirworkMinutes == 0, nx.AirworkMinutes, x.AirworkMinutes)
	x.ActionGroups = append(x.ActionGroups, nx.ActionGroups...)
	return wp
}

// SpliceRoutes joins next onto the end of base, merging the waypoints where
// they meet--a run of them, if next starts with the same fix more than once.
// Two waypoints at one fix would leave a zero-length leg between them: the
// heading out of the first is taken toward the second, which is in the same
// place, so it comes out north rather than on course and the aircraft turns
// off the leg that reaches the fix at the wrong point.
func SpliceRoutes(base, next WaypointArray) WaypointArray {
	wps := util.DuplicateSlice(base)
	n := len(wps)
	for n > 0 && len(next) > 0 && wps[n-1].Fix == next[0].Fix {
		wps[n-1] = wps[n-1].MergeWith(next[0])
		next = next[1:]
	}
	return append(wps, next...)
}

// Flag readers (value receiver)
func (wp Waypoint) NoPT() bool       { return wp.Flags&WaypointFlagNoPT != 0 }
func (wp Waypoint) FlyOver() bool    { return wp.Flags&WaypointFlagFlyOver != 0 }
func (wp Waypoint) IAF() bool        { return wp.Flags&WaypointFlagIAF != 0 }
func (wp Waypoint) IF() bool         { return wp.Flags&WaypointFlagIF != 0 }
func (wp Waypoint) FAF() bool        { return wp.Flags&WaypointFlagFAF != 0 }
func (wp Waypoint) OnSID() bool      { return wp.Flags&WaypointFlagOnSID != 0 }
func (wp Waypoint) OnSTAR() bool     { return wp.Flags&WaypointFlagOnSTAR != 0 }
func (wp Waypoint) OnApproach() bool { return wp.Flags&WaypointFlagOnApproach != 0 }
func (wp Waypoint) SyntheticCrossing() bool {
	return wp.Flags&WaypointFlagSyntheticCrossing != 0
}

// IsNamedFix reports whether a waypoint name is a published fix or navaid
// identifier someone could say on the radio, as opposed to one of the points
// routes are built out of: scenario helper fixes ("_EWR4_22Ra"), runway
// thresholds and midpoints ("22R", "1-mid"), lat/long waypoints, and CIFP
// procedure fixes ("CF13L", "RW22L"). Published identifiers are three to five
// letters and never contain a digit.
func IsNamedFix(fix string) bool {
	if len(fix) < 3 || len(fix) > 5 {
		return false
	}
	for _, ch := range fix {
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') {
			return false
		}
	}
	return true
}
func (wp Waypoint) HasAltitudeRestriction() bool {
	return wp.Flags&WaypointFlagHasAltRestriction != 0
}
func (wp Waypoint) HasSpeedRestriction() bool {
	return wp.Flags&WaypointFlagHasSpeedRestriction != 0
}
func (wp Waypoint) SequenceVFRLanding() bool {
	return wp.Flags&WaypointFlagSequenceVFRLanding != 0
}
func (wp Waypoint) HasTransferCommsAction() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.TransferComms })
}

// AssignsHeading reports whether any of the waypoint's actions assigns a
// heading to fly.
func (wp Waypoint) AssignsHeading() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.Heading.IsSet() })
}
func (wp Waypoint) HasHumanHandoff() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.HumanHandoff })
}
func (wp Waypoint) HasDeleteAction() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.Delete })
}
func (wp Waypoint) HasLandAction() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.Land })
}
func (wp Waypoint) HasInterceptApproachAction() bool {
	return slices.ContainsFunc(wp.ActionGroups(),
		func(group WaypointActionGroup) bool { return group.Actions.InterceptApproach })
}

// ClearLandAction removes the /land action wherever it appears in the
// waypoint's action groups.
func (wp *Waypoint) ClearLandAction() {
	if wp.Extra == nil {
		return
	}
	for i := range wp.Extra.ActionGroups {
		wp.Extra.ActionGroups[i].Actions.Land = false
	}
}

// Turn returns the direction of the turn toward the waypoint from the one
// before it.
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

func (wp *Waypoint) SetNoPT(v bool)               { wp.setFlag(WaypointFlagNoPT, v) }
func (wp *Waypoint) SetFlyOver(v bool)            { wp.setFlag(WaypointFlagFlyOver, v) }
func (wp *Waypoint) SetIAF(v bool)                { wp.setFlag(WaypointFlagIAF, v) }
func (wp *Waypoint) SetIF(v bool)                 { wp.setFlag(WaypointFlagIF, v) }
func (wp *Waypoint) SetFAF(v bool)                { wp.setFlag(WaypointFlagFAF, v) }
func (wp *Waypoint) SetOnSID(v bool)              { wp.setFlag(WaypointFlagOnSID, v) }
func (wp *Waypoint) SetOnSTAR(v bool)             { wp.setFlag(WaypointFlagOnSTAR, v) }
func (wp *Waypoint) SetOnApproach(v bool)         { wp.setFlag(WaypointFlagOnApproach, v) }
func (wp *Waypoint) SetSyntheticCrossing(v bool)  { wp.setFlag(WaypointFlagSyntheticCrossing, v) }
func (wp *Waypoint) SetSequenceVFRLanding(v bool) { wp.setFlag(WaypointFlagSequenceVFRLanding, v) }

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
	if wp.HasAltitudeRestriction() {
		return &wp.AltRestriction
	}
	return nil
}

// SetAltitudeRestriction stores the restriction inline and sets the flag.
func (wp *Waypoint) SetAltitudeRestriction(ar AltitudeRestriction) {
	wp.AltRestriction = ar
	wp.Flags |= WaypointFlagHasAltRestriction
}

// ClearAltitudeRestriction removes the inline restriction and clears the flag.
func (wp *Waypoint) ClearAltitudeRestriction() {
	wp.AltRestriction = AltitudeRestriction{}
	wp.Flags &^= WaypointFlagHasAltRestriction
}

// SpeedRestriction returns a pointer to the inline restriction if the flag is set, else nil.
func (wp *Waypoint) SpeedRestriction() *SpeedRestriction {
	if wp.HasSpeedRestriction() {
		return &wp.SpdRestriction
	}
	return nil
}

// SetSpeedRestriction stores the restriction inline and sets the flag.
func (wp *Waypoint) SetSpeedRestriction(sr SpeedRestriction) {
	wp.SpdRestriction = sr
	wp.Flags |= WaypointFlagHasSpeedRestriction
}

// ClearSpeedRestriction removes the inline restriction and clears the flag.
func (wp *Waypoint) ClearSpeedRestriction() {
	wp.SpdRestriction = SpeedRestriction{}
	wp.Flags &^= WaypointFlagHasSpeedRestriction
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
func (wp Waypoint) ActionGroups() []WaypointActionGroup {
	if wp.Extra != nil {
		return wp.Extra.ActionGroups
	}
	return nil
}
func (wp Waypoint) Airway() string {
	if wp.Extra != nil {
		return wp.Extra.Airway
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

// LegOffset returns how far along the leg after the preceding charted fix the
// waypoint was synthesized, as a fraction of the way to the fix at its end. It
// is 0 for the route's own fixes.
func (wp Waypoint) LegOffset() float32 {
	if wp.Extra != nil {
		return wp.Extra.LegOffset
	}
	return 0
}

// AlongLeg reports whether the waypoint was synthesized at a point partway
// along the leg after a charted fix rather than being a fix of the route.
func (wp Waypoint) AlongLeg() bool { return wp.LegOffset() != 0 }

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

// HasAltitudeActions reports whether any of the waypoint's action groups
// has a /c or /d altitude action.
func (wp Waypoint) HasAltitudeActions() bool {
	return slices.ContainsFunc(wp.ActionGroups(), func(group WaypointActionGroup) bool {
		return group.Actions.ClimbAltitude != 0 || group.Actions.DescendAltitude != 0
	})
}

// HeadingAction returns the heading the aircraft leaves the waypoint on, if
// its first action group gives one.
func (wp Waypoint) HeadingAction() (WaypointHeadingAction, bool) {
	if groups := wp.ActionGroups(); len(groups) > 0 && groups[0].Actions.Heading.IsSet() {
		return groups[0].Actions.Heading, true
	}
	return WaypointHeadingAction{}, false
}

// MergeActions adds actions to the waypoint's last action group if it is
// still open, or starts a new group if the last one has a termination.
func (wp *Waypoint) MergeActions(actions WaypointActions) error {
	groups := wp.ActionGroups()
	if n := len(groups); n > 0 && groups[n-1].Until.Type == WaypointActionNoTermination {
		return mergeWaypointActions(&groups[n-1].Actions, actions)
	}
	wp.InitExtra().ActionGroups = append(groups, WaypointActionGroup{Actions: actions})
	return nil
}

// courseFlags are the flags that say how a waypoint is flown, as opposed to
// where it came from or what was synthesized around it.
const courseFlags = WaypointFlagNoPT | WaypointFlagFlyOver | WaypointFlagTurnLeft |
	WaypointFlagTurnRight | WaypointFlagHasAltRestriction | WaypointFlagHasSpeedRestriction

// sameCourse reports whether the two waypoints are flown the same way: the
// same fix with the same restrictions and the same geometry. The actions at
// them are no part of it, since "waypoint_actions" is how a route taken from
// the CIFP gets those.
func (wp Waypoint) sameCourse(o Waypoint) bool {
	if wp.Fix != o.Fix || wp.Flags&courseFlags != o.Flags&courseFlags ||
		wp.Airway() != o.Airway() || wp.Radius() != o.Radius() || wp.Shift() != o.Shift() ||
		wp.AirworkRadius() != o.AirworkRadius() || wp.AirworkMinutes() != o.AirworkMinutes() {
		return false
	}
	if wp.HasAltitudeRestriction() && wp.AltRestriction != o.AltRestriction {
		return false
	}
	if wp.HasSpeedRestriction() && wp.SpdRestriction != o.SpdRestriction {
		return false
	}
	if pt, opt := wp.ProcedureTurn(), o.ProcedureTurn(); (pt == nil) != (opt == nil) ||
		(pt != nil && *pt != *opt) {
		return false
	}
	arc, oarc := wp.Arc(), o.Arc()
	return (arc == nil) == (oarc == nil) && (arc == nil || *arc == *oarc)
}

// sameCourse reports whether the routes are flown the same way; see
// Waypoint.sameCourse.
func (wa WaypointArray) sameCourse(o WaypointArray) bool {
	return slices.EqualFunc(wa, o, Waypoint.sameCourse)
}

// sameRoute reports whether the routes are flown the same way and carry the
// same actions along them.
func (wa WaypointArray) sameRoute(o WaypointArray) bool {
	return slices.EqualFunc(wa, o, func(x, y Waypoint) bool {
		return x.sameCourse(y) && slices.Equal(x.ActionGroups(), y.ActionGroups())
	})
}

// addedActions puts into actions the "waypoint_actions" entries that give
// wa's own actions at the charted route's fixes, which must fly the same
// course. A value's action groups replace the ones the CIFP charts at its
// fix, so the only difference addedActions can't express is dropping the
// charted groups without giving any in their place.
func (wa WaypointArray) addedActions(charted WaypointArray, actions map[string]string) bool {
	for i, wp := range wa {
		mine := wp.ActionGroups()
		if slices.Equal(mine, charted[i].ActionGroups()) {
			continue
		}
		if len(mine) == 0 {
			return false
		}
		actions[wp.Fix] = encodeActionGroups(mine)
	}
	return true
}

func (wp Waypoint) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("fix", wp.Fix)}
	if ar := wp.AltitudeRestriction(); ar != nil {
		attrs = append(attrs, slog.Any("altitude_restriction", ar))
	}
	if sr := wp.SpeedRestriction(); sr != nil {
		attrs = append(attrs, slog.String("speed_restriction", sr.Encoded()))
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
	if wp.FlyOver() {
		attrs = append(attrs, slog.Bool("fly_over", true))
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
	if groups := wp.ActionGroups(); len(groups) > 0 {
		attrs = append(attrs, slog.String("actions", strings.Join(util.MapSlice(groups,
			func(group WaypointActionGroup) string { return group.Encoded() }), "")))
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

// containsFix reports whether the route passes over the named fix.
func (wa WaypointArray) containsFix(fix string) bool {
	return slices.ContainsFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
}

// HasHumanHandoff returns true if any waypoint has HumanHandoff set.
func (wa WaypointArray) HasHumanHandoff() bool {
	return slices.ContainsFunc(wa, Waypoint.HasHumanHandoff)
}

// HandoffControllers returns the positions the route names as handoff targets
// in any of its waypoints' action groups. These are the /hoXX handoffs, which
// name where the track goes; the bare /ho of HasHumanHandoff leaves that to
// whoever is working the flow.
func (wa WaypointArray) HandoffControllers() []ControlPosition {
	var controllers []ControlPosition
	add := func(pos ControlPosition) {
		if pos != "" && !slices.Contains(controllers, pos) {
			controllers = append(controllers, pos)
		}
	}
	for _, wp := range wa {
		for _, group := range wp.ActionGroups() {
			add(group.Actions.HandoffController)
		}
	}
	return controllers
}

func (wa WaypointArray) Encode() string {
	var entries []string
	for i, w := range wa {
		var s strings.Builder
		s.WriteString(w.Fix)
		if ar := w.AltitudeRestriction(); ar != nil {
			s.WriteString("/a" + ar.Encoded())
		}
		if sr := w.SpeedRestriction(); sr != nil {
			s.WriteString("/s" + sr.Encoded())
		}
		if pt := w.ProcedureTurn(); pt != nil {
			if pt.Type == PTStandard45 {
				if !pt.RightTurns {
					s.WriteString("/lpt45")
				} else {
					s.WriteString("/pt45")
				}
			} else {
				if !pt.RightTurns {
					s.WriteString("/lhilpt")
				} else {
					s.WriteString("/hilpt")
				}
			}
			if pt.MinuteLimit != 0 {
				s.WriteString(fmt.Sprintf("%.1fmin", pt.MinuteLimit))
			} else if pt.NmLimit != 0 {
				s.WriteString(fmt.Sprintf("%.1fnm", pt.NmLimit))
			}
			if pt.Entry180NoPT {
				s.WriteString("/nopt180")
			}
			if pt.ExitAltitude != 0 {
				s.WriteString(fmt.Sprintf("/pta%d", pt.ExitAltitude))
			}
		}
		if w.IAF() {
			s.WriteString("/iaf")
		}
		if w.IF() {
			s.WriteString("/if")
		}
		if w.FAF() {
			s.WriteString("/faf")
		}
		if w.NoPT() {
			s.WriteString("/nopt")
		}
		if w.FlyOver() {
			s.WriteString("/flyover")
		}
		if arc := w.Arc(); arc != nil {
			switch arc.Direction {
			case DMEArcDirectionClockwise:
				s.WriteString("/rarc")
			case DMEArcDirectionCounterClockwise:
				s.WriteString("/larc")
			default:
				s.WriteString("/arc")
			}
			if arc.Fix != "" {
				s.WriteString(strconv.FormatFloat(float64(arc.Radius), 'f', -1, 32) + arc.Fix)
			} else {
				s.WriteString(strconv.FormatFloat(float64(arc.Length), 'f', -1, 32))
			}
		}
		for _, group := range w.ActionGroups() {
			s.WriteString(group.Encoded())
		}
		if aw := w.Airway(); aw != "" {
			s.WriteString("/airway" + aw)
		}
		if w.OnSID() {
			s.WriteString("/sid")
		}
		if w.OnSTAR() {
			s.WriteString("/star")
		}
		if w.OnApproach() {
			s.WriteString("/appr")
		}
		if w.AirworkRadius() != 0 {
			s.WriteString(fmt.Sprintf("/airwork%dnm%dm", w.AirworkRadius(), w.AirworkMinutes()))
		}
		if w.Radius() != 0 {
			s.WriteString(fmt.Sprintf("/radius%.1f", w.Radius()))
		}
		if w.Shift() != 0 {
			s.WriteString(fmt.Sprintf("/shift%.1f", w.Shift()))
		}

		// The turn direction to the next fix is given on the fix before it.
		if i+1 < len(wa) {
			switch wa[i+1].Turn() {
			case TurnLeft:
				s.WriteString("/ld")
			case TurnRight:
				s.WriteString("/rd")
			}
		}

		entries = append(entries, s.String())
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

// Clone returns a copy of the waypoints that shares nothing with the
// original, so that initializing the copy leaves the original untouched.
func (wa WaypointArray) Clone() WaypointArray {
	wps := util.DuplicateSlice(wa)
	for i := range wps {
		wps[i] = wps[i].Clone()
	}
	return wps
}

// RouteString renders the waypoints back to a route string. A run of them
// sharing an airway collapses to its two ends.
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

func (wa WaypointArray) CheckDeparture(e *util.ErrorLogger, elevation int, controllers map[ControlPosition]*Controller, checkScratchpads func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, controllers, checkScratchpads)

	var lastMin float32 // previous minimum altitude restriction
	var minFix string

	for _, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in departure")
		}
		for _, group := range wp.ActionGroups() {
			// @a altitudes are MSL, so one at or below the field elevation is
			// met the moment the aircraft starts rolling. Almost always a
			// missing factor of 100.
			if group.Until.Type == WaypointActionAltitude && group.Until.Altitude <= elevation {
				sign := util.Select(group.Until.AtOrAbove, "+", "-")
				e.ErrorString("/@a%d%s is at or below the %d' field elevation, so it takes effect immediately. Is it supposed to be /@a%d%s?",
					group.Until.Altitude, sign, elevation, group.Until.Altitude*100, sign)
			}
			if alt := group.Actions.ClimbAltitude; alt != 0 && alt <= elevation {
				e.ErrorString("/c%d is at or below the %d' field elevation", alt, elevation)
			}
			if alt := group.Actions.DescendAltitude; alt != 0 && alt <= elevation {
				e.ErrorString("/d%d is at or below the %d' field elevation", alt, elevation)
			}
		}
		if war := wp.AltitudeRestriction(); war != nil {
			checkAltitudeRange(e, *war)
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

	checkFeet := func(qualifier string, alt int) {
		if alt > 0 && float32(alt) < minCrossingAltitude {
			hundredsOfFeetError(e, fmt.Sprintf("/%s%d", qualifier, alt), fmt.Sprintf("/%s%d", qualifier, alt*100))
		}
	}

	for i, wp := range wa {
		e.Push(wp.Fix)
		if sr := wp.SpeedRestriction(); sr != nil {
			checkSpeedRange(e, *sr)
		}

		if pt := wp.ProcedureTurn(); pt != nil {
			checkFeet("pta", pt.ExitAltitude)
		}

		if wp.AirworkMinutes() > 0 {
			if ar := wp.AltitudeRestriction(); ar == nil {
				e.ErrorString(`Must provide altitude range via "/aXXX-YYY" with /airwork`)
			} else if ar.Range[0] == 0 || ar.Range[1] == MaxAltitude {
				e.ErrorString(`Must provide top and bottom in altitude range "/aXXX-YYY" with /airwork`)
			} else if ar.Range[1]-ar.Range[0] < 2000 {
				e.ErrorString("Must provide at least 2,000' of altitude range with /airwork")
			}
		}

		for _, group := range wp.ActionGroups() {
			if !wa.atEnd(i) {
				checkFeet("c", group.Actions.ClimbAltitude)
				checkFeet("d", group.Actions.DescendAltitude)
			}
			if po := group.Actions.PointOut; po != "" {
				if !util.MapContains(controllers,
					func(_ ControlPosition, ctrl *Controller) bool {
						return ctrl.PositionId() == po
					}) {
					e.ErrorString("No controller found with id %q for point out", po)
				}
			}
			if hc := group.Actions.HandoffController; hc != "" {
				if !util.MapContains(controllers,
					func(_ ControlPosition, ctrl *Controller) bool {
						return ctrl.PositionId() == ControlPosition(hc)
					}) {
					e.ErrorString("No controller found with id %q for handoff", hc)
				}
			}
			if !checkScratchpad(group.Actions.PrimaryScratchpad) {
				e.ErrorString("%s: invalid primary_scratchpad", group.Actions.PrimaryScratchpad)
			}
			if !checkScratchpad(group.Actions.SecondaryScratchpad) {
				e.ErrorString("%s: invalid secondary scratchpad", group.Actions.SecondaryScratchpad)
			}
		}

		if i == 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify /shift at the first fix in a route")
		}
		if wp.Radius() > 0 && wp.Shift() > 0 {
			e.ErrorString("Can't specify both /radius and /shift at the same fix")
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

// minCrossingAltitude is the lowest altitude we accept in a route qualifier
// away from the runway. Route altitudes are in feet, so a lower one is
// almost always a missing factor of 100.
const minCrossingAltitude float32 = 500

// minRouteAltitude and maxRouteAltitude bound the altitudes we accept in a
// route's crossing restrictions; outside them, the value is a typo rather
// than a restriction. The floor allows for fields below sea level: the
// lowest runway threshold in the CIFP is KCLR's, at -145'.
const (
	minRouteAltitude float32 = -1000
	maxRouteAltitude float32 = 50000
)

// atEnd reports whether the i'th waypoint is where the route effectively
// ends: the aircraft is at the runway there, so low altitudes are expected.
func (wa WaypointArray) atEnd(i int) bool {
	return i+1 == len(wa) || wa[i].HasDeleteAction() || wa[i+1].HasDeleteAction()
}

func hundredsOfFeetError(e *util.ErrorLogger, given, scaled string) {
	e.ErrorString("%s is below %s, which is almost certainly not intended. Is it supposed to be %s?",
		given, FormatAltitude(minCrossingAltitude), scaled)
}

// minRouteSpeed and maxRouteSpeed bound the airspeeds we accept in scenarios,
// both initial speeds and crossing restrictions; outside them the value is a
// typo rather than an airspeed. The floor leaves room for light GA, which
// spawns off the runway as slow as 60 knots.
const (
	minRouteSpeed = 50
	maxRouteSpeed = 350
)

// minMach and maxMach bound Mach numbers, which are otherwise exempt from the
// knots range; "M8500" parses as Mach 85 and is as much a typo as the rest.
// The ceiling covers Concorde, the only supersonic type in the performance
// database, which tops out at Mach 2.04.
const (
	minMach = 0.3
	maxMach = 2.5
)

func checkSpeed(e *util.ErrorLogger, what string, speed Airspeed) {
	if speed.IsMach {
		if speed.Value < minMach || speed.Value > maxMach {
			e.ErrorString("%s %s: Mach must be between %g and %g", what, speed, minMach, maxMach)
		}
	} else if speed.Value < minRouteSpeed || speed.Value > maxRouteSpeed {
		e.ErrorString("%s %s: speeds must be between %d and %d knots", what, speed, minRouteSpeed, maxRouteSpeed)
	}
}

func checkSpeedRange(e *util.ErrorLogger, sr SpeedRestriction) {
	if sr.IsMach {
		checkSpeed(e, "speed restriction", MakeMach(sr.Range[0]))
		return
	}
	// A zero lower bound or a MaxRestrictionSpeed upper bound means unbounded
	// rather than an airspeed to check.
	unreasonable := func(s float32) bool { return s < minRouteSpeed || s > maxRouteSpeed }
	if (sr.Range[0] != 0 && unreasonable(sr.Range[0])) ||
		(sr.Range[1] != MaxRestrictionSpeed && unreasonable(sr.Range[1])) {
		e.ErrorString("invalid speed restriction %s: speeds must be between %d and %d knots",
			sr.Encoded(), minRouteSpeed, maxRouteSpeed)
	}
}

func checkAltitudeRange(e *util.ErrorLogger, ar AltitudeRestriction) {
	unreasonable := func(alt float32) bool {
		return alt < minRouteAltitude || alt >= maxRouteAltitude
	}
	if unreasonable(ar.Range[0]) || (ar.Range[1] != MaxAltitude && unreasonable(ar.Range[1])) {
		e.ErrorString("Invalid altitude restriction %q: altitudes must be between %s and FL500",
			ar.Encoded(), FormatAltitude(minRouteAltitude))
	}
}

func (wa WaypointArray) CheckArrival(e *util.ErrorLogger, ctrl map[ControlPosition]*Controller, approachAssigned bool,
	checkScratchpad func(string) bool) {
	defer e.CheckDepth(e.CurrentDepth())

	wa.checkBasics(e, ctrl, checkScratchpad)
	wa.checkDescending(e)
	haveHO := false

	for i, wp := range wa {
		e.Push(wp.Fix)
		if wp.IAF() || wp.IF() || wp.FAF() {
			e.ErrorString("Unexpected IAF/IF/FAF specification in arrival")
		}
		if ar := wp.AltitudeRestriction(); ar != nil && !wa.atEnd(i) {
			alt := ar.Range[0]
			if ar.Range[1] != MaxAltitude {
				alt = max(alt, ar.Range[1])
			}
			if alt > 0 && alt < minCrossingAltitude {
				scaled := *ar
				scaled.Range[0] *= 100
				if scaled.Range[1] != MaxAltitude {
					scaled.Range[1] *= 100
				}
				hundredsOfFeetError(e, "/a"+ar.Encoded(), "/a"+scaled.Encoded())
			}
		}
		for _, group := range wp.ActionGroups() {
			if group.Actions.ClearApproach && !approachAssigned {
				e.ErrorString("/clearapp specified but no approach has been assigned")
			}
			if group.Actions.InterceptApproach && !approachAssigned {
				e.ErrorString("/intercept specified but no approach has been assigned")
			}
			if group.Actions.HumanHandoff {
				haveHO = true
			}
			if group.Actions.TransferComms && !haveHO {
				e.ErrorString("Must have /ho to handoff to a human controller before /tc")
			}
		}
		e.Pop()
	}
}

// checkApproachJoins reports /clearapp and /intercept actions the aircraft
// won't be able to act on. Both join appr by looking for a fix that the route
// and the approach share, so something from the action's own fix onward has
// to be on it. An open-ended heading action is the exception: the aircraft
// keeps that heading until a controller intervenes and is vectored to the
// approach course, so it needs no shared fix.
func (wa WaypointArray) checkApproachJoins(appr *Approach, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	onApproach := func(fix string) bool {
		return slices.ContainsFunc(appr.Waypoints, func(route WaypointArray) bool {
			return slices.ContainsFunc(route, func(wp Waypoint) bool { return wp.Fix == fix })
		})
	}

	onHeading := false
	for i, wp := range wa {
		if _, kind := ActionGroupHeading(wp.ActionGroups()); kind == ActionGroupHeadingAssigned {
			onHeading = true
		}
		if onHeading {
			continue
		}

		var action string
		if slices.ContainsFunc(wp.ActionGroups(), func(g WaypointActionGroup) bool { return g.Actions.ClearApproach }) {
			action = "/clearapp"
		} else if wp.HasInterceptApproachAction() {
			action = "/intercept"
		} else {
			continue
		}

		if slices.ContainsFunc(wa[i:], func(wp Waypoint) bool { return onApproach(wp.Fix) }) {
			continue
		}

		e.Push(wp.Fix)
		e.ErrorString("%s can't join the %s: neither this fix nor any after it is on it",
			action, appr.FullName)
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
			if war.Range[0] > war.Range[1] {
				e.ErrorString("Minimum altitude %s is higher than maximum %s",
					FormatAltitude(war.Range[0]), FormatAltitude(war.Range[1]))
			}

			checkAltitudeRange(e, *war)

			if war.Range[0] != 0 {
				if minFix != "" && war.Range[0] > lastMin {
					e.ErrorString("Minimum altitude %s is higher than previous fix %s's minimum %s",
						FormatAltitude(war.Range[0]), minFix, FormatAltitude(lastMin))
				}
				minFix = wp.Fix
				lastMin = war.Range[0]
			}
		}

		e.Pop()
	}

}

func RandomizeRoute(w []Waypoint, r *rand.Rand, randomizeAltitudeRange bool, perf AircraftPerformance, nmPerLongitude float32,
	magneticVariation float32, airport ICAOAirportCode, lg *log.Logger) {
	// Random values used for altitude and position randomization
	rtheta, rrad := r.Float32(), r.Float32()
	ralt := r.Float32()

	// We use this to some random variation to the random sample after each
	// use. In this way, there's some correlation between adjacent
	// waypoints: if they're relatively high at one, they'll tend to be
	// relatively high at the next one, though the random choices still
	// vary a bit.
	jitter := func(v float32) float32 {
		v += r.Float32Range(-0.1, 0.1)
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
				if high == MaxAltitude {
					high = low + 3000
				}
				// Cap at VFR max (17,500') since randomizeAltitudeRange is only true for VFR.
				// This prevents VFR aircraft from being assigned altitudes in Class A airspace.
				const maxVFRAltitude = 17500
				high = min(high, maxVFRAltitude)
				low = min(low, maxVFRAltitude)
				alt := math.Lerp(ralt, low, high)

				// Update the altitude restriction to just be the single altitude.
				wp.SetAltitudeRestriction(MakeAtAltitudeRestriction(alt))

				ralt = jitter(ralt)
			}
		}
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

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// parseCourse parses a magnetic heading or course. North is 360 rather than
// 0, since Waypoint.Heading uses 0 to mean "unset".
func parseCourse(s string) (int16, error) {
	hdg, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid heading: %v", s, err)
	}
	if hdg < 1 || hdg > 360 {
		return 0, fmt.Errorf("%s: heading must be between 1-360 (use 360 for north)", s)
	}
	return int16(hdg), nil
}

func parseWaypointHeadingAction(f string) (WaypointHeadingAction, bool, error) {
	if f == "ph" {
		return WaypointHeadingAction{PresentHeading: true}, true, nil
	}

	var headingAction WaypointHeadingAction
	var hdg string
	switch {
	case len(f) >= 3 && f[:2] == "lt":
		headingAction.Turn = TurnLeft
		headingAction.Track = true
		hdg = f[2:]
	case len(f) >= 3 && f[:2] == "rt":
		headingAction.Turn = TurnRight
		headingAction.Track = true
		hdg = f[2:]
	case len(f) >= 2 && f[0] == 't':
		headingAction.Track = true
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'h':
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'l':
		headingAction.Turn = TurnLeft
		hdg = f[1:]
	case len(f) >= 2 && f[0] == 'r':
		headingAction.Turn = TurnRight
		hdg = f[1:]
	default:
		return WaypointHeadingAction{}, false, nil
	}

	// A navaid's radial in place of the heading, /tPXR-R336, is tracked
	// away from the navaid.
	if !allDigits(hdg) {
		if !strings.Contains(hdg, "-R") {
			return WaypointHeadingAction{}, false, nil
		}
		if !headingAction.Track {
			return WaypointHeadingAction{}, true, fmt.Errorf("%s: a radial can only be tracked (/t, /lt, /rt), not flown as a heading", f)
		}
		fix, radial, err := parseRadial(hdg)
		if err != nil {
			return WaypointHeadingAction{}, true, err
		}
		headingAction.Fix, headingAction.Heading = fix, radial
		return headingAction, true, nil
	}

	heading, err := parseCourse(hdg)
	if err != nil {
		return WaypointHeadingAction{}, true, err
	}
	headingAction.Heading = heading
	return headingAction, true, nil
}

func parseWaypointActionModifier(f string) (WaypointActions, bool, error) {
	switch {
	case f == "ho":
		return WaypointActions{HumanHandoff: true}, true, nil
	case strings.HasPrefix(f, "ho"):
		return WaypointActions{HandoffController: ControlPosition(f[2:])}, true, nil
	case len(f) > 2 && f[:2] == "po":
		return WaypointActions{PointOut: ControlPosition(f[2:])}, true, nil
	case f == "clearapp":
		return WaypointActions{ClearApproach: true}, true, nil
	case f == "intercept":
		return WaypointActions{InterceptApproach: true}, true, nil
	case f == "delete":
		return WaypointActions{Delete: true}, true, nil
	case f == "land":
		return WaypointActions{Land: true}, true, nil
	case strings.HasPrefix(f, "spsp"):
		return WaypointActions{PrimaryScratchpad: f[4:]}, true, nil
	case f == "cpsp":
		return WaypointActions{ClearPrimaryScratchpad: true}, true, nil
	case strings.HasPrefix(f, "sssp"):
		return WaypointActions{SecondaryScratchpad: f[4:]}, true, nil
	case f == "cssp":
		return WaypointActions{ClearSecondaryScratchpad: true}, true, nil
	case f == "tc":
		return WaypointActions{TransferComms: true}, true, nil
	case len(f) > 1 && (f[0] == 'c' || f[0] == 'd') && allDigits(f[1:]):
		alt, err := strconv.Atoi(f[1:])
		if err != nil || alt < 100 || alt > 60000 || alt%100 != 0 {
			return WaypointActions{}, true, fmt.Errorf("%s: altitude must be a multiple of 100 between 100 and 60000 feet", f)
		}
		if f[0] == 'c' {
			return WaypointActions{ClimbAltitude: alt}, true, nil
		}
		return WaypointActions{DescendAltitude: alt}, true, nil
	}

	if heading, ok, err := parseWaypointHeadingAction(f); ok || err != nil {
		return WaypointActions{Heading: heading}, ok, err
	}

	return WaypointActions{}, false, nil
}

// merge combines src into wa
func (wa *WaypointActions) merge(src WaypointActions) {
	if src.Heading.IsSet() {
		wa.Heading = src.Heading
	}
	if src.ClimbAltitude != 0 {
		wa.ClimbAltitude, wa.DescendAltitude = src.ClimbAltitude, 0
	}
	if src.DescendAltitude != 0 {
		wa.DescendAltitude, wa.ClimbAltitude = src.DescendAltitude, 0
	}
	wa.HumanHandoff = wa.HumanHandoff || src.HumanHandoff
	if src.HandoffController != "" {
		wa.HandoffController = src.HandoffController
	}
	if src.PointOut != "" {
		wa.PointOut = src.PointOut
	}
	wa.ClearApproach = wa.ClearApproach || src.ClearApproach
	wa.InterceptApproach = wa.InterceptApproach || src.InterceptApproach
	wa.Delete = wa.Delete || src.Delete
	wa.Land = wa.Land || src.Land
	if src.GoAroundContactController != "" {
		wa.GoAroundContactController = src.GoAroundContactController
	}
	if src.PrimaryScratchpad != "" {
		wa.PrimaryScratchpad = src.PrimaryScratchpad
	}
	wa.ClearPrimaryScratchpad = wa.ClearPrimaryScratchpad || src.ClearPrimaryScratchpad
	if src.SecondaryScratchpad != "" {
		wa.SecondaryScratchpad = src.SecondaryScratchpad
	}
	wa.ClearSecondaryScratchpad = wa.ClearSecondaryScratchpad || src.ClearSecondaryScratchpad
	wa.TransferComms = wa.TransferComms || src.TransferComms
}

// mergeWaypointActions combines src into dst, rejecting a second value for
// anything an action group may only give once.
func mergeWaypointActions(dst *WaypointActions, src WaypointActions) error {
	if src.Heading.IsSet() && dst.Heading.IsSet() {
		return fmt.Errorf("multiple heading actions in the same waypoint action group")
	}
	if src.ClimbAltitude != 0 && dst.ClimbAltitude != 0 {
		return fmt.Errorf("multiple climb altitude actions in the same waypoint action group")
	}
	if src.DescendAltitude != 0 && dst.DescendAltitude != 0 {
		return fmt.Errorf("multiple descend altitude actions in the same waypoint action group")
	}
	if (src.ClimbAltitude != 0 && dst.DescendAltitude != 0) ||
		(src.DescendAltitude != 0 && dst.ClimbAltitude != 0) {
		return fmt.Errorf("cannot specify both /c and /d in the same waypoint action group")
	}
	dst.merge(src)
	return nil
}

// parseWaypointActionTermination parses the condition of a trigger, after
// its @: an altitude (a4277+ at or above, a4277- at or below), a distance
// flown (d7.9), a course to the next fix (crs220, or crsHLN-R322 when the
// leg runs along a navaid's radial), a navaid's radial (HLN-R322), or a DME
// distance from a navaid (ILSQ-D2.3+ at or beyond, ILSQ-D2.3- within).
func parseWaypointActionTermination(f string) (WaypointActionTermination, error) {
	// cutSign splits off the trailing + or - that says which side of the
	// value the trigger is met on.
	cutSign := func(s string) (value string, atOrAbove, ok bool) {
		if value, ok = strings.CutSuffix(s, "+"); ok {
			return value, true, true
		}
		value, ok = strings.CutSuffix(s, "-")
		return value, false, ok
	}

	switch {
	case len(f) > 1 && f[0] == 'a':
		alt, atOrAbove, ok := cutSign(f[1:])
		if !ok || !allDigits(alt) {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected an altitude followed by + (at or above) or - (at or below)", f)
		}
		a, err := strconv.Atoi(alt)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid altitude: %w", f, err)
		}
		if a > 60000 {
			return WaypointActionTermination{}, fmt.Errorf("%s: trigger altitude must be between 0 and 60000 feet", f)
		}
		return WaypointActionTermination{Type: WaypointActionAltitude, Altitude: a, AtOrAbove: atOrAbove}, nil

	case len(f) > 1 && f[0] == 'd':
		d, err := strconv.ParseFloat(f[1:], 32)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid distance %q", f, f[1:])
		}
		if d <= 0 {
			return WaypointActionTermination{}, fmt.Errorf("%s: distance must be positive", f)
		}
		return WaypointActionTermination{Type: WaypointActionDistance, Distance: float32(d)}, nil

	case strings.HasPrefix(f, "crs"):
		if strings.Contains(f[3:], "-R") {
			// A leg along a navaid's radial gives that radial, which is
			// referenced to the station's declination rather than the
			// area's variation, in place of the course.
			fix, radial, err := parseRadial(f[3:])
			if err != nil {
				return WaypointActionTermination{}, fmt.Errorf("%s: %w", f, err)
			}
			return WaypointActionTermination{Type: WaypointActionCourse, Course: radial, CourseFix: fix}, nil
		}
		if !allDigits(f[3:]) || len(f) == 3 {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a course after crs", f)
		}
		course, err := parseCourse(f[3:])
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: %w", f, err)
		}
		return WaypointActionTermination{Type: WaypointActionCourse, Course: course}, nil

	case strings.Contains(f, "-R"):
		fix, radial, err := parseRadial(f)
		if err != nil {
			return WaypointActionTermination{}, err
		}
		return WaypointActionTermination{Type: WaypointActionRadial, Radial: radial, RadialFix: fix}, nil

	case strings.Contains(f, "-D"):
		i := strings.LastIndex(f, "-D")
		fix, spec := f[:i], f[i+2:]
		if fix == "" {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a navaid before -D", f)
		}
		dist, atOrAbove, ok := cutSign(spec)
		if !ok {
			return WaypointActionTermination{}, fmt.Errorf("%s: expected a DME distance followed by + (at or beyond) or - (within)", f)
		}
		d, err := strconv.ParseFloat(dist, 32)
		if err != nil {
			return WaypointActionTermination{}, fmt.Errorf("%s: invalid DME distance %q", f, dist)
		}
		if d <= 0 {
			return WaypointActionTermination{}, fmt.Errorf("%s: DME distance must be positive", f)
		}
		return WaypointActionTermination{Type: WaypointActionDME, DMEDistance: float32(d), DMEFix: fix, AtOrAbove: atOrAbove}, nil

	default:
		return WaypointActionTermination{}, fmt.Errorf("%s: unknown trigger; expected an altitude (a4277+), "+
			"a distance flown (d7.9), a course (crs220 or crsHLN-R322), a radial (HLN-R322), "+
			"or a DME distance (ILSQ-D2.3+)", f)
	}
}

// parseRadial parses a navaid's radial written as a chart does, HLN-R322.
func parseRadial(s string) (fix string, radial int16, err error) {
	i := strings.LastIndex(s, "-R")
	if i <= 0 {
		return "", 0, fmt.Errorf("%s: expected a radial as NAVAID-R<radial>", s)
	}
	fix, digits := s[:i], s[i+2:]
	if digits == "" || !allDigits(digits) {
		return "", 0, fmt.Errorf("%s: expected a radial after %s-R", s, fix)
	}
	radial, err = parseCourse(digits)
	if err != nil {
		return "", 0, err
	}
	return fix, radial, nil
}

// parseWaypointActionKey parses a "waypoint_actions" key: a fix, optionally
// followed by an offset along the leg after it ("CKING@.75"). The offset is 0
// if the key gives none.
func parseWaypointActionKey(key string) (string, float32, error) {
	if strings.Contains(key, "/") {
		return "", 0, fmt.Errorf("the key gives a fix, optionally with an offset; triggers go in the value")
	}
	fix, offsetString, haveOffset := strings.Cut(key, "@")
	if fix == "" {
		return "", 0, fmt.Errorf("no fix given")
	}
	var offset float32
	if haveOffset {
		t, err := strconv.ParseFloat(offsetString, 32)
		if err != nil {
			return "", 0, fmt.Errorf("invalid offset %q: %w", offsetString, err)
		}
		// Written so that a NaN, which ParseFloat returns without an error, is
		// rejected along with the out-of-range values.
		if !(t > 0 && t < 1) {
			return "", 0, fmt.Errorf("offset %q: must be greater than 0 and less than 1", offsetString)
		}
		offset = float32(t)
	}
	return fix, offset, nil
}

// parseWaypointActionValue parses a "waypoint_actions" value--a fix's options
// in route syntax, without their leading slash ("h284/hoC35/@a513+")--into a
// waypoint carrying what it gives: action groups, altitude and speed
// restrictions, and /flyover. A /ld or /rd comes back separately: it gives
// the turn made at the fix onto the leg that follows, so its flag belongs on
// the route's next fix. The other options a waypoint can carry describe
// route structure or approach coding that a route taken from the CIFP owns,
// so they are rejected.
func parseWaypointActionValue(value string) (Waypoint, TurnDirection, error) {
	if value == "" {
		return Waypoint{}, TurnClosest, fmt.Errorf("empty value")
	}
	if strings.Contains(value, ",") {
		return Waypoint{}, TurnClosest, fmt.Errorf("%s: options are separated by slashes, not commas", value)
	}
	var wp Waypoint
	turn, err := parseWaypointModifiers(&wp, value, strings.Split(value, "/"))
	if err != nil {
		return Waypoint{}, TurnClosest, err
	}
	if wp.NoPT() || wp.IAF() || wp.IF() || wp.FAF() ||
		wp.OnSID() || wp.OnSTAR() || wp.OnApproach() || wp.ProcedureTurn() != nil ||
		wp.Arc() != nil || wp.Airway() != "" || wp.AirworkRadius() != 0 ||
		wp.Radius() != 0 || wp.Shift() != 0 {
		return Waypoint{}, TurnClosest, fmt.Errorf("%s: only actions, triggers, /a and /s restrictions, /flyover, and /ld or /rd may be given", value)
	}
	return wp, turn, nil
}

// applyActions applies a "waypoint_actions" value to the fix. The value's
// action groups replace the ones charted at the fix--a value with no actions
// or triggers leaves the charted groups as they are--its altitude or speed
// restriction replaces the charted restriction of its kind, /flyover marks
// the fix as flown over, and /ld or /rd gives the turn made at the fix onto
// the leg that follows. An offset puts the value's actions on a point
// synthesized partway along the leg after the fix instead, which the
// returned route carries; its location waits for InitializeLocations.
func (wa WaypointArray) applyActions(fix string, offset float32, value string) (WaypointArray, error) {
	i := slices.IndexFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
	if i == -1 {
		return wa, fmt.Errorf("%s: not in the route %s", fix, wa.RouteString())
	}
	o, turn, err := parseWaypointActionValue(value)
	if err != nil {
		return wa, err
	}

	if offset != 0 {
		if o.HasAltitudeRestriction() || o.HasSpeedRestriction() {
			return wa, fmt.Errorf(`%s: restrictions may not be specified at an "@" offset fix`, value)
		}
		if o.FlyOver() {
			return wa, fmt.Errorf(`/flyover may not be specified at an "@" offset fix`)
		}
		if turn != TurnClosest {
			return wa, fmt.Errorf(`%s: /ld and /rd may not be specified at an "@" offset fix`, value)
		}
		return wa.insertOffsetActions(i, offset, o.ActionGroups())
	}

	groups := o.ActionGroups()
	if n := len(groups); n > 0 && groups[n-1].Until.Type == WaypointActionCourse && i == len(wa)-1 {
		return wa, fmt.Errorf("%s: /@crs has no following fix to give a course to; %s ends the route", value, fix)
	}
	if turn != TurnClosest {
		next := wa.nextChartedFix(i)
		if next == len(wa) {
			return wa, fmt.Errorf("%s: /ld or /rd has no following fix to turn to; %s ends the route", value, fix)
		}
		// The turn direction to the next fix is carried on that fix.
		wa[next].SetTurn(turn)
	}
	wp := &wa[i]
	if len(groups) > 0 {
		wp.InitExtra().ActionGroups = groups
	}
	if ar := o.AltitudeRestriction(); ar != nil {
		wp.SetAltitudeRestriction(*ar)
	}
	if sr := o.SpeedRestriction(); sr != nil {
		wp.SetSpeedRestriction(*sr)
	}
	if o.FlyOver() {
		wp.SetFlyOver(true)
	}
	return wa, nil
}

// formatOffset renders a fraction of the way along a leg the shortest way that
// reads back as the same number.
func formatOffset(t float32) string {
	return strconv.FormatFloat(float64(t), 'g', -1, 32)
}

// nextChartedFix returns the index of the first waypoint after i that is a fix
// of the route rather than a point synthesized along the leg, or len(wa) if
// the leg after i runs off the end of the route.
func (wa WaypointArray) nextChartedFix(i int) int {
	for j := i + 1; j < len(wa); j++ {
		if !wa[j].AlongLeg() {
			return j
		}
	}
	return len(wa)
}

// hasOffsetLeg reports whether the fix is in the route with another fix after
// it, so that a point partway along the leg it starts can be measured.
func (wa WaypointArray) hasOffsetLeg(fix string) bool {
	i := slices.IndexFunc(wa, func(wp Waypoint) bool { return wp.Fix == fix })
	return i != -1 && wa.nextChartedFix(i) != len(wa)
}

// insertAlongLeg inserts a waypoint at the given fraction of the way along the
// leg after the fix at index i, keeping the points on the leg in the order
// they are flown. Its location is left for InitializeLocations, which takes it
// from the fixes on either side.
func (wa WaypointArray) insertAlongLeg(i int, offset float32, wp Waypoint) WaypointArray {
	wp.InitExtra().LegOffset = offset

	j := i + 1
	for j < len(wa) && wa[j].AlongLeg() && wa[j].LegOffset() < offset {
		j++
	}
	return slices.Insert(wa, j, wp)
}

// insertOffsetActions inserts a waypoint carrying the action groups at the
// point offset of the way from the fix at index i to the next charted fix
// after it, named for the leg it sits on ("_CKING-DRIFT@0.75"). The name has
// the leg's far end in it because one "waypoint_actions" key may reach
// several routes that diverge at the fix: two points are then named the same
// only when they are in the same place.
func (wa WaypointArray) insertOffsetActions(i int, offset float32, groups []WaypointActionGroup) (WaypointArray, error) {
	fix := wa[i].Fix
	if wa[i].Arc() != nil {
		return wa, fmt.Errorf("%s: the leg after it is a DME arc, which a point between the two fixes isn't on", fix)
	}
	if aw := wa[i].Airway(); aw != "" {
		return wa, fmt.Errorf("%s: %s follows it, so the fix the offset would be measured to isn't the next one "+
			"in the route", fix, aw)
	}
	next := wa.nextChartedFix(i)
	if next == len(wa) {
		return wa, fmt.Errorf("%s: ends the route %s, so there is no following fix to measure the offset to",
			fix, wa.RouteString())
	}
	// The turn is made toward the first waypoint on the leg, so a point
	// partway along it would leave the turn's direction behind at the far
	// fix, never to be consulted.
	if wa[next].Turn() != TurnClosest {
		return wa, fmt.Errorf("%s: the leg to %s carries a turn direction, which a point partway along it would defeat",
			fix, wa[next].Fix)
	}
	if slices.ContainsFunc(wa[i+1:next], func(w Waypoint) bool { return w.LegOffset() == offset }) {
		return wa, fmt.Errorf("%s: another key already puts a point there", fix)
	}

	// No flags to carry over: the routes this runs on are marked as being on a
	// SID or STAR later, over all of their waypoints at once.
	return wa.insertAlongLeg(i, offset, Waypoint{
		Fix:   fmt.Sprintf("_%s-%s@%s", fix, wa[next].Fix, formatOffset(offset)),
		Extra: &WaypointExtra{ActionGroups: groups},
	}), nil
}

// ResolveActionControllers returns a "waypoint_actions" value with the
// control positions of its handoffs and point outs passed through resolve.
// Parts that aren't such actions--triggers, properties, anything that doesn't
// parse--are left as they are for validation to report.
func ResolveActionControllers(actions string, resolve func(ControlPosition) ControlPosition) string {
	parts := strings.Split(actions, "/")
	for i, a := range parts {
		acts, ok, err := parseWaypointActionModifier(a)
		if !ok || err != nil {
			continue
		}
		if acts.HandoffController != "" {
			acts.HandoffController = resolve(acts.HandoffController)
		}
		if acts.PointOut != "" {
			acts.PointOut = resolve(acts.PointOut)
		}
		parts[i] = strings.TrimPrefix(acts.Encoded(), "/")
	}
	return strings.Join(parts, "/")
}

// parseWaypointModifiers applies a waypoint's /-separated options--actions,
// triggers, and properties--to wp; field names the full option string in
// error messages. It returns the turn direction that a /ld or /rd gives,
// which applies at the route's next waypoint.
func parseWaypointModifiers(wp *Waypoint, field string, mods []string) (TurnDirection, error) {
	nextWaypointTurn := TurnClosest
	for _, f := range mods {
		if len(f) == 0 {
			return TurnClosest, fmt.Errorf("no command found after / in %q", field)
		}

		// A trigger ends the group of the actions before it; the
		// actions after it start when it is met.
		if cond, isTrigger := strings.CutPrefix(f, "@"); isTrigger {
			groups := wp.ActionGroups()
			if n := len(groups); n == 0 || groups[n-1].Until.Type != WaypointActionNoTermination {
				return TurnClosest, fmt.Errorf("%s: trigger /%s must follow an action; use /ph to fly present heading",
					field, f)
			}
			until, err := parseWaypointActionTermination(cond)
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid trigger /%s: %w", field, f, err)
			}
			groups[len(groups)-1].Until = until
			continue
		}

		actions, ok, err := parseWaypointActionModifier(f)
		if err != nil {
			return TurnClosest, fmt.Errorf("%s: invalid waypoint action /%s: %w", field, f, err)
		}
		if ok {
			if err := wp.MergeActions(actions); err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid waypoint action /%s: %w", field, f, err)
			}
			continue
		}

		if f == "flyover" {
			wp.SetFlyOver(true)
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
		} else if strings.HasPrefix(f, "airwork") {
			a := f[7:]
			radius, minutes := 7, 15
			i := 0
			for len(a) > 0 {
				if a[i] >= '0' && a[i] <= '9' {
					i++
				} else if n, err := strconv.Atoi(a[:i]); err != nil {
					return TurnClosest, fmt.Errorf("%v: parsing %q", f, a[:i])
				} else if a[i] == 'm' {
					minutes = n
					a = a[i+1:]
					i = 0
				} else if a[i] == 'n' && len(a) > i+1 && a[i+1] == 'm' {
					radius = n
					a = a[i+2:]
					i = 0
				} else {
					return TurnClosest, fmt.Errorf("unexpected suffix %q after %q in %q", a[i:], a[:i], f)
				}
			}
			if i > 0 {
				return TurnClosest, fmt.Errorf("unexpected numbers %q after %q", a, f)
			}
			e := wp.InitExtra()
			e.AirworkRadius = int8(radius)
			e.AirworkMinutes = int8(minutes)
		} else if strings.HasPrefix(f, "radius") {
			rstr := f[6:]
			if rad, err := strconv.ParseFloat(rstr, 32); err != nil {
				return TurnClosest, err
			} else {
				wp.InitExtra().Radius = float32(rad)
			}
		} else if strings.HasPrefix(f, "shift") {
			sstr := f[5:]
			if shift, err := strconv.ParseFloat(sstr, 32); err != nil {
				return TurnClosest, err
			} else {
				wp.InitExtra().Shift = float32(shift)
			}
		} else if (len(f) >= 4 && f[:4] == "pt45") || (len(f) >= 5 && f[:5] == "lpt45") {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Type = PTStandard45
			pt.ProcedureTurn.RightTurns = f[0] == 'p'
			wp.SetFlyOver(true)

			extent := f[4:]
			if !pt.ProcedureTurn.RightTurns {
				extent = extent[1:]
			}
			if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
				return TurnClosest, err
			}
		} else if (len(f) >= 5 && f[:5] == "hilpt") || (len(f) >= 6 && f[:6] == "lhilpt") {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Type = PTRacetrack
			pt.ProcedureTurn.RightTurns = f[0] == 'h'
			wp.SetFlyOver(true)

			extent := f[5:]
			if !pt.ProcedureTurn.RightTurns {
				extent = extent[1:]
			}
			if err := parsePTExtent(pt.ProcedureTurn, extent); err != nil {
				return TurnClosest, err
			}
		} else if len(f) >= 4 && f[:3] == "pta" {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}

			alt, err := strconv.Atoi(f[3:])
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: error parsing procedure turn exit altitude: %v", f[3:], err)
			}
			if alt < 0 || alt > 60000 {
				return TurnClosest, fmt.Errorf("%s: procedure turn exit altitude must be between 0 and 60000 feet", f)
			}
			pt.ProcedureTurn.ExitAltitude = alt
		} else if f == "nopt" {
			wp.SetNoPT(true)
		} else if f == "nopt180" {
			pt := wp.InitExtra()
			if pt.ProcedureTurn == nil {
				pt.ProcedureTurn = &ProcedureTurn{}
			}
			pt.ProcedureTurn.Entry180NoPT = true
		} else if len(f) >= 4 && (f[:3] == "arc" || f[:4] == "larc" || f[:4] == "rarc") {
			// The direction is inferred from the surrounding fixes
			// unless given: /larc turns left (counterclockwise),
			// /rarc right.
			direction := DMEArcDirectionUnset
			spec := f[3:]
			if f[0] == 'l' {
				direction, spec = DMEArcDirectionCounterClockwise, f[4:]
			} else if f[0] == 'r' {
				direction, spec = DMEArcDirectionClockwise, f[4:]
			}
			rend := 0
			for rend < len(spec) &&
				((spec[rend] >= '0' && spec[rend] <= '9') || spec[rend] == '.') {
				rend++
			}
			if rend == 0 {
				return TurnClosest, fmt.Errorf("%s: radius not found after /arc", f)
			}

			v, err := strconv.ParseFloat(spec[:rend], 32)
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: invalid arc radius/length: %w", f, err)
			}

			if rend == len(spec) {
				// no fix given, so interpret it as an arc length
				wp.InitExtra().Arc = &DMEArc{
					Length:    float32(v),
					Direction: direction,
				}
			} else {
				wp.InitExtra().Arc = &DMEArc{
					Fix:       spec[rend:],
					Radius:    float32(v),
					Direction: direction,
				}
			}
		} else if len(f) >= 7 && f[:6] == "airway" {
			wp.InitExtra().Airway = f[6:]

			// Do these last since they only match the first character...
		} else if f[0] == 'a' {
			if wp.HasAltitudeRestriction() {
				return TurnClosest, fmt.Errorf("%s: multiple altitude restrictions given; use a range (e.g. /a8000-10000) instead",
					field)
			}
			ar, err := ParseAltitudeRestriction(f[1:])
			if err != nil {
				return TurnClosest, err
			}
			wp.SetAltitudeRestriction(*ar)
		} else if f[0] == 's' {
			if wp.HasSpeedRestriction() {
				return TurnClosest, fmt.Errorf("%s: multiple speed restrictions given; use a range (e.g. /s180-210) instead",
					field)
			}
			sr, err := ParseSpeedRestriction(f[1:])
			if err != nil {
				return TurnClosest, fmt.Errorf("%s: error parsing speed restriction: %v", f[1:], err)
			}
			wp.SetSpeedRestriction(*sr)
		} else if f == "ld" {
			nextWaypointTurn = TurnLeft
		} else if f == "rd" {
			nextWaypointTurn = TurnRight
		} else {
			return TurnClosest, fmt.Errorf("%s: unknown fix modifier: %s", field, f)
		}
	}

	if pt := wp.ProcedureTurn(); pt != nil && pt.Type == PTUndefined {
		return TurnClosest, fmt.Errorf("%s: no procedure turn specified for fix (e.g., pt45/hilpt) even though PT parameters were given", wp.Fix)
	}

	// /@crs ends with the aircraft going direct to the next fix, so a
	// following action group would immediately preempt it.
	if groups := wp.ActionGroups(); len(groups) > 1 &&
		slices.ContainsFunc(groups[:len(groups)-1], func(g WaypointActionGroup) bool {
			return g.Until.Type == WaypointActionCourse
		}) {
		return TurnClosest, fmt.Errorf("%s: /@crs must be the last trigger at a fix", wp.Fix)
	}

	return nextWaypointTurn, nil
}

func parseWaypoints(str string) (WaypointArray, error) {
	var waypoints WaypointArray
	var nextWaypointTurn TurnDirection
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

		wp := Waypoint{Fix: components[0]}
		if nextWaypointTurn != TurnClosest {
			wp.SetTurn(nextWaypointTurn)
		}
		turn, err := parseWaypointModifiers(&wp, field, components[1:])
		if err != nil {
			return nil, err
		}
		nextWaypointTurn = turn

		waypoints = append(waypoints, wp)
	}

	if nextWaypointTurn != TurnClosest {
		return nil, fmt.Errorf("/ld or /rd on the last waypoint has no next waypoint to apply to")
	}

	if n := len(waypoints); n > 0 {
		if groups := waypoints[n-1].ActionGroups(); len(groups) > 0 &&
			groups[len(groups)-1].Until.Type == WaypointActionCourse {
			return nil, fmt.Errorf("%s: /@crs on the last waypoint has no following fix to give a course to",
				waypoints[n-1].Fix)
		}
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
		ar := MakeAtOrBelowAltitudeRestriction(float32(alt))
		return &ar, nil
	} else if s[n-1] == '+' {
		// At or above
		alt, err := strconv.Atoi(s[:n-1])
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		}
		ar := MakeAtOrAboveAltitudeRestriction(float32(alt))
		return &ar, nil
	} else if alts := strings.Split(s, "-"); len(alts) == 2 {
		// Between
		if low, err := strconv.Atoi(alts[0]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if high, err := strconv.Atoi(alts[1]); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else if low > high {
			return nil, fmt.Errorf("%s: low altitude %d is above high altitude %d", s, low, high)
		} else {
			ar := MakeRangeAltitudeRestriction(float32(low), float32(high))
			return &ar, nil
		}
	} else {
		// At
		if alt, err := strconv.Atoi(s); err != nil {
			return nil, fmt.Errorf("%s: error parsing altitude restriction: %v", s, err)
		} else {
			ar := MakeAtAltitudeRestriction(float32(alt))
			return &ar, nil
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

	// Declination returns the station declination of the named VHF navaid,
	// if it has one: the variation its radials are referenced to.
	Declination(fix string) (float32, bool)

	// If Locate fails, Similar can be called to get alternatives that are
	// similarly-spelled to be offered in error messages.
	Similar(fix string) []string
}

type DMELocator interface {
	LocateDME(fix string) (math.Point2LL, int, bool)
}

// initializeActionLocations resolves the fixes the waypoint's action groups
// refer to: the navaids that DME distances and radials are measured from and
// the fixes whose radials are flown.
func (wp *Waypoint) initializeActionLocations(loc Locator, magneticVariation float32, allowSlop bool,
	e *util.ErrorLogger) {
	// radialVariation returns the variation a radial of fix is referenced
	// to. A VHF navaid's radials are fixed to its station declination,
	// which the local variation has usually drifted from since the station
	// was aligned; a fix without a station uses the area's variation.
	radialVariation := func(fix string) float32 {
		if d, ok := loc.Declination(fix); ok {
			return d
		}
		return magneticVariation
	}
	dmeLocator, canLocateDME := loc.(DMELocator)

	for j, group := range wp.ActionGroups() {
		if group.Until.Type == WaypointActionDME {
			if !canLocateDME {
				if e != nil && !allowSlop {
					e.ErrorString("%s: unable to locate DME station %q for waypoint action group %q",
						wp.Fix, group.Until.DMEFix, group.Encoded())
				}
			} else if pos, elevation, ok := dmeLocator.LocateDME(group.Until.DMEFix); ok {
				until := &wp.InitExtra().ActionGroups[j].Until
				until.DMEFixLocation, until.DMEFixElevation = pos, elevation
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate DME station %q with elevation for waypoint action group %q",
					wp.Fix, group.Until.DMEFix, group.Encoded())
			}
		}
		if group.Until.Type == WaypointActionRadial {
			if pos, ok := loc.Locate(group.Until.RadialFix); ok {
				until := &wp.InitExtra().ActionGroups[j].Until
				until.RadialFixLocation, until.RadialFixVariation = pos, radialVariation(group.Until.RadialFix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, group.Until.RadialFix, group.Encoded())
			}
		}
		// The course's line runs through the next fix, so the navaid it
		// is a radial of supplies only the variation, not a location.
		if fix := group.Until.CourseFix; fix != "" {
			if _, ok := loc.Locate(fix); ok {
				wp.InitExtra().ActionGroups[j].Until.CourseFixVariation = radialVariation(fix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, fix, group.Encoded())
			}
		}
		if fix := group.Actions.Heading.Fix; fix != "" {
			if pos, ok := loc.Locate(fix); ok {
				heading := &wp.InitExtra().ActionGroups[j].Actions.Heading
				heading.FixLocation, heading.FixVariation = pos, radialVariation(fix)
			} else if e != nil && !allowSlop {
				e.ErrorString("%s: unable to locate %q for waypoint action group %q",
					wp.Fix, fix, group.Encoded())
			}
		}
	}
}

func (wa WaypointArray) InitializeLocations(loc Locator, nmPerLongitude float32, magneticVariation float32,
	allowSlop bool, e *util.ErrorLogger) WaypointArray {
	if len(wa) == 0 {
		return wa
	}

	defer e.CheckDepth(e.CurrentDepth())

	// Get the locations of all waypoints and cull the route after 250nm if cullFar is true.
	// prev is the last waypoint located, which points along a leg are not, so
	// the distance check below spans one charted leg however many of them sit
	// on it.
	var prev math.Point2LL
	prevIdx, nLocated := -1, 0
	for i, wp := range wa {
		if e != nil {
			e.Push("Fix " + wp.Fix)
		}
		wa[i].initializeActionLocations(loc, magneticVariation, allowSlop, e)

		if wp.AlongLeg() {
			// Placed below, once the fixes on either side have been located.
		} else if pos, ok := loc.Locate(wp.Fix); !ok {
			if e != nil && !allowSlop {
				var errstr strings.Builder
				errstr.WriteString("unable to locate waypoint.")
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
						errstr.WriteString(" Did you mean: ")
					}
					for _, s := range sim {
						errstr.WriteString(fmt.Sprintf("%s (%.1fnm) ", s, dist[s]))
					}
				}
				e.ErrorString("%s", errstr.String())
			}
		} else {
			wa[i].Location = pos

			// A leg longer than this is almost always a typo in a
			// hand-written route; the longest the CIFP charts run about 220nm,
			// out to an exit fix at the edge of a wide TRACON.
			const suspiciousLegLength = 250

			d := math.NMDistance2LL(prev, wa[i].Location)
			if nLocated > 1 && d > suspiciousLegLength && e != nil && !allowSlop && wa[prevIdx].Airway() == "" {
				e.ErrorString("waypoint at %s is suspiciously far from previous one (%s at %s): %f nm",
					wa[i].Location.DDString(), wa[prevIdx].Fix, wa[prevIdx].Location.DDString(), d)
			}
			prev, prevIdx = wa[i].Location, i
			nLocated++
		}

		if e != nil {
			e.Pop()
		}
	}

	// Points partway along a leg take their location from the charted fixes on
	// either side, so they are placed once those have been located. Lerping
	// lat-longs rather than following the great circle, as "spawn" does; the
	// difference over a leg is nothing. A point beside a fix that couldn't be
	// located keeps a zero location, the same as the fix itself.
	for i, fix := 0, 0; i < len(wa); i++ {
		if !wa[i].AlongLeg() {
			fix = i
			continue
		}
		if next := wa.nextChartedFix(i); next < len(wa) &&
			!wa[fix].Location.IsZero() && !wa[next].Location.IsZero() {
			wa[i].Location = math.Lerp2f(wa[i].LegOffset(), wa[fix].Location, wa[next].Location)
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

		if wa[i].Arc().Direction == DMEArcDirectionUnset {
			// Direction wasn't explicitly provided (e.g., from CIFP turn
			// direction); infer it from the surrounding waypoints.
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
			wa[i].InitExtra().Arc.Direction = util.Select(x < 0, DMEArcDirectionClockwise, DMEArcDirectionCounterClockwise)
		}

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

// ProcedureBase strips a SID or STAR's revision number: CUUDA3 and CUUDA4 are
// revisions of the same arrival.
func ProcedureBase(name string) string {
	return strings.TrimRight(name, "0123456789")
}

// TokenNamesAirport reports whether a route token names the airport, as
// either its ICAO id or its FAA local identifier.
func TokenNamesAirport(token string, icao ICAOAirportCode) bool {
	if token == string(icao) {
		return true
	}
	faa, ok := ICAOAirportToFAA(icao)
	return ok && token == string(faa)
}

// TokenNamesProcedure reports whether a route token names a SID or a STAR:
// it ends with a revision digit and isn't an airway.
func TokenNamesProcedure(token string) bool {
	if token == "" {
		return false
	}
	if c := token[len(token)-1]; c < '0' || c > '9' {
		return false
	}
	_, airway := DB.Airways[token]
	return !airway
}

// TrimDepartureAirportTokens removes from the head of a filed route the
// tokens that name the departure airport itself--pilots file "OGG LNY ..."
// out of PHOG, naming the airport rather than a fix to fly--skipping over a
// leading procedure token, since filings render the SID ahead of them. A
// token leading onto an airway that reaches a further fix stays: it is the
// airway's entry, the airport's id doubling as its VOR's.
//
// This and TrimDestinationAirportTokens are for normalizing a published route
// that stays as text; code that goes on to match the route's fixes wants the
// WaypointArray pair below. The two pairs carry the same rules and have to
// stay in step: waypoints parsed by RouteWaypoints can't be rendered back to
// a route string (see RouteString), so neither pair can be written in terms
// of the other.
func TrimDepartureAirportTokens(fields []string, icao ICAOAirportCode) []string {
	i, skippedProcedure := 0, false
	for i < len(fields) {
		// The airport comes first: plenty of ids end in a digit, and one of
		// those names the airport rather than a procedure.
		if TokenNamesAirport(fields[i], icao) {
			if i+2 < len(fields) {
				if _, ok := DB.Airways[fields[i+1]]; ok {
					break
				}
			}
			fields = slices.Delete(fields, i, i+1)
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(fields[i]) {
			break
		}
		skippedProcedure = true
		i++
	}
	return fields
}

// TrimDestinationAirportTokens is TrimDepartureAirportTokens's mirror for the
// other end of the route: it removes trailing tokens that name the
// destination airport, skipping over a trailing procedure token. A token
// ending an airway that reaches back to an entry fix stays: it is the
// airway's exit, as the ITO in "... V16 UPP V2 ITO" into PHTO.
func TrimDestinationAirportTokens(fields []string, icao ICAOAirportCode) []string {
	last, skippedProcedure := len(fields)-1, false
	for last >= 0 {
		if TokenNamesAirport(fields[last], icao) {
			if last >= 2 {
				if _, ok := DB.Airways[fields[last-1]]; ok {
					break
				}
			}
			fields = slices.Delete(fields, last, last+1)
			last--
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(fields[last]) {
			break
		}
		skippedProcedure = true
		last--
	}
	return fields
}

// TrimDepartureAirportWaypoints removes the leading waypoints that name the
// departure airport, as TrimDepartureAirportTokens does for a route that is
// still text; this is the form for code that matches the route's fixes.
func TrimDepartureAirportWaypoints(wps WaypointArray, icao ICAOAirportCode) WaypointArray {
	i, skippedProcedure := 0, false
	for i < len(wps) {
		if TokenNamesAirport(wps[i].Fix, icao) {
			if wps[i].Airway() != "" && i+1 < len(wps) {
				break
			}
			wps = slices.Delete(wps, i, i+1)
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(wps[i].Fix) {
			break
		}
		skippedProcedure = true
		i++
	}
	return wps
}

// TrimDestinationAirportWaypoints removes the trailing waypoints that name
// the destination airport: TrimDepartureAirportWaypoints's mirror, and the
// WaypointArray form of TrimDestinationAirportTokens.
func TrimDestinationAirportWaypoints(wps WaypointArray, icao ICAOAirportCode) WaypointArray {
	last, skippedProcedure := len(wps)-1, false
	for last >= 0 {
		if TokenNamesAirport(wps[last].Fix, icao) {
			if last >= 1 && wps[last-1].Airway() != "" {
				break
			}
			wps = slices.Delete(wps, last, last+1)
			last--
			continue
		}
		if skippedProcedure || !TokenNamesProcedure(wps[last].Fix) {
			break
		}
		skippedProcedure = true
		last--
	}
	return wps
}

// routeProcedureToken returns the last token of a route into or out of the
// airport if it names a procedure, or "" otherwise.
func routeProcedureToken(route string, icao ICAOAirportCode) string {
	fields := strings.Fields(route)
	if n := len(fields); n > 0 && TokenNamesAirport(fields[n-1], icao) {
		fields = fields[:n-1]
	}
	if len(fields) == 0 || !TokenNamesProcedure(fields[len(fields)-1]) {
		return ""
	}
	return fields[len(fields)-1]
}

// RouteSTAR returns the STAR a filed route into the airport ends with, under
// the name the current CIFP charts it, along with the fix filed ahead of it
// on the route, or empty strings if it names none. A route may carry a stale
// revision--CUUDA3 where the cycle has CUUDA4--so procedures match on their
// base names.
func RouteSTAR(route string, icao ICAOAirportCode) (star, entry string) {
	token := routeProcedureToken(route, icao)
	if token == "" {
		return "", ""
	}

	names := util.SortedMapKeys(DB.Airports[icao].STARs)
	i := slices.IndexFunc(names, func(name string) bool { return ProcedureBase(name) == ProcedureBase(token) })
	if i == -1 {
		return "", ""
	}
	name := names[i]

	fields := strings.Fields(route)
	if i := slices.Index(fields, token); i > 0 {
		if _, ok := DB.Airways[fields[i-1]]; !ok {
			entry = fields[i-1]
		}
	}
	return name, entry
}

// AltitudeFloor returns the highest "at or above" crossing restriction along
// the route. Cruise is the top of a flight's profile, so it can be no lower.
func (wa WaypointArray) AltitudeFloor() int {
	var floor float32
	for _, wp := range wa {
		if ar := wp.AltitudeRestriction(); ar != nil {
			floor = max(floor, ar.Range[0])
		}
	}
	return int(floor)
}

// transitionFloor returns the AltitudeFloor of the transition a route joins a
// procedure at. The fields are the route's tokens working outward from the
// procedure's own name; the first of them naming a transition is the one
// flown, since an airway or a fix off the procedure may sit between. Naming
// none, every way of flying the procedure is at or above the lowest
// transition's floor.
func transitionFloor(transitions map[string]WaypointArray, fields []string) int {
	for _, f := range fields {
		if wps, ok := transitions[f]; ok {
			return wps.AltitudeFloor()
		}
	}
	floor := -1
	for _, wps := range transitions {
		if f := wps.AltitudeFloor(); floor == -1 || f < floor {
			floor = f
		}
	}
	return max(floor, 0)
}

// routeSID returns the SID a route out of the airport begins with, under the
// name the current CIFP charts it, along with the index of the field naming
// it. A route may carry a stale revision--DOTSS2 where the cycle has
// DOTSS3--so procedures match on their base names.
func routeSID(fields []string, icao ICAOAirportCode) (SID, int, bool) {
	i := 0
	if len(fields) > 0 && TokenNamesAirport(fields[0], icao) {
		i = 1
	}
	if i >= len(fields) || !TokenNamesProcedure(fields[i]) {
		return SID{}, 0, false
	}
	for name, sid := range util.SortedMap(DB.Airports[icao].SIDs) {
		if ProcedureBase(name) == ProcedureBase(fields[i]) {
			return sid, i, true
		}
	}
	return SID{}, 0, false
}

// RouteAltitudeFloor returns the lowest altitude a flight filing the route can
// cruise at: the highest "at or above" restriction its SID and its STAR
// publish. It is 0 when the route names neither, as one out of an airport the
// CIFP doesn't cover can't, and when the procedures it does name publish no
// such restriction, as the open-route STARs into JFK don't.
func RouteAltitudeFloor(route string, departureAirport, arrivalAirport ICAOAirportCode) int {
	fields := strings.Fields(route)
	floor := 0

	if sid, i, ok := routeSID(fields, departureAirport); ok {
		floor = max(floor, transitionFloor(sid.EnrouteTransitions, fields[i+1:]))
	}

	end := len(fields)
	if end > 0 && TokenNamesAirport(fields[end-1], arrivalAirport) {
		end--
	}
	if star, _ := RouteSTAR(route, arrivalAirport); star != "" && end > 0 {
		before := slices.Clone(fields[:end-1])
		slices.Reverse(before)
		floor = max(floor, transitionFloor(DB.Airports[arrivalAirport].STARs[star].Transitions, before))
	}

	return floor
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

// CIFPRoute is one coded route of a procedure: the name it is flown under,
// its encoded waypoints, and the waypoints themselves. The waypoints hold no
// locations: they come from the CIFP, which names its fixes rather than
// placing them, so a caller that draws them must run InitializeLocations on
// a copy first.
type CIFPRoute struct {
	Name      string
	Route     string
	Waypoints WaypointArray
}

// Routes returns the STAR's transitions and runway routes.
func (s STAR) Routes(name string) []CIFPRoute {
	var routes []CIFPRoute
	for tr, wps := range util.SortedMap(s.Transitions) {
		routes = append(routes, CIFPRoute{Name: name + "." + tr, Route: wps.Encode(), Waypoints: wps})
	}
	for rwy, wps := range util.SortedMap(s.RunwayWaypoints) {
		routes = append(routes, CIFPRoute{Name: name + ".RWY" + rwy, Route: wps.Encode(), Waypoints: wps})
	}
	return routes
}

func (s STAR) Print(name string) {
	for _, r := range s.Routes(name) {
		fmt.Printf(routePrintFormat, r.Name, r.Route)
	}
}

///////////////////////////////////////////////////////////////////////////
// SID

// SID is a standard instrument departure as coded in the FAA CIFP.
type SID struct {
	// RunwayTransitions is keyed by runway. A transition that starts with
	// a leg flown from the runway--a heading to an altitude, say--begins at
	// the runway's departure end, named as the opposite runway's threshold:
	// KJFK-13R for a departure off 31L.
	RunwayTransitions map[string]WaypointArray
	// Common is the route every runway and enroute transition shares.
	Common WaypointArray
	// EnrouteTransitions is keyed by transition name, each with the common
	// route spliced onto its front.
	EnrouteTransitions map[string]WaypointArray
}

// MakeSID returns an empty SID with its maps allocated.
func MakeSID() *SID {
	return &SID{
		RunwayTransitions:  make(map[string]WaypointArray),
		EnrouteTransitions: make(map[string]WaypointArray),
	}
}

// Waypoints returns the SID as flown off the given runway by a departure
// leaving over the given exit fix. The route follows the runway transition
// and then the named enroute transition if one is given, or the one the
// exit lies on otherwise, ending at the exit; with no transition named and
// none leading to the exit it follows the common route to its end. An empty
// runway gives the SID from its common route on, for a departure that is
// vectored to it.
func (s SID) Waypoints(runway, transition, exit string) (WaypointArray, error) {
	var wps WaypointArray
	if runway != "" {
		var ok bool
		if wps, ok = s.RunwayTransitions[runway]; !ok {
			if len(s.RunwayTransitions) == 0 {
				return nil, fmt.Errorf("no runway transitions in the CIFP")
			}
			return nil, fmt.Errorf("no runway transition for runway %s. Options: %s", runway,
				strings.Join(util.SortedMapKeys(s.RunwayTransitions), ", "))
		}
	}

	body := s.Common
	if transition != "" {
		var ok bool
		if body, ok = s.EnrouteTransitions[transition]; !ok {
			return nil, fmt.Errorf("no enroute transition %s. Options: %s", transition,
				strings.Join(util.SortedMapKeys(s.EnrouteTransitions), ", "))
		}
	} else {
		hasExit := func(wps WaypointArray) bool {
			return slices.ContainsFunc(wps, func(wp Waypoint) bool { return wp.Fix == exit })
		}
		if !hasExit(wps) && !hasExit(s.Common) {
			for _, name := range util.SortedMapKeys(s.EnrouteTransitions) {
				if hasExit(s.EnrouteTransitions[name]) {
					body = s.EnrouteTransitions[name]
					break
				}
			}
		}
	}

	route := spliceSIDTransition(wps, body)
	if i := slices.IndexFunc(route, func(wp Waypoint) bool { return wp.Fix == exit }); i != -1 {
		route = route[:i+1]
	}
	return route, nil
}

// spliceSIDTransition appends tr to a copy of base. A transition starts at
// the last fix of what precedes it (ARINC 424 attachment 5, 4.10), so if its
// first fix is found in base the route continues from there, keeping the
// transition's restrictions at the junction where base has none.
func spliceSIDTransition(base, tr WaypointArray) WaypointArray {
	route := util.DuplicateSlice(base)
	if len(tr) == 0 {
		return route
	}

	idx := slices.IndexFunc(route, func(wp Waypoint) bool { return wp.Fix == tr[0].Fix })
	if idx == -1 {
		return append(route, tr...)
	}

	junction := &route[idx]
	if junction.AltitudeRestriction() == nil {
		if ar := tr[0].AltitudeRestriction(); ar != nil {
			junction.SetAltitudeRestriction(*ar)
		}
	}
	if junction.SpeedRestriction() == nil {
		if sr := tr[0].SpeedRestriction(); sr != nil {
			junction.SetSpeedRestriction(*sr)
		}
	}
	return append(route[:idx+1], tr[1:]...)
}

// Routes returns the SID's runway transitions, common route, and enroute
// transitions.
func (s SID) Routes(name string) []CIFPRoute {
	var routes []CIFPRoute
	for rwy, wps := range util.SortedMap(s.RunwayTransitions) {
		routes = append(routes, CIFPRoute{Name: name + ".RWY" + rwy, Route: wps.Encode(), Waypoints: wps})
	}
	if len(s.Common) > 0 {
		routes = append(routes, CIFPRoute{Name: name, Route: s.Common.Encode(), Waypoints: s.Common})
	}
	for tr, wps := range util.SortedMap(s.EnrouteTransitions) {
		routes = append(routes, CIFPRoute{Name: name + "." + tr, Route: wps.Encode(), Waypoints: wps})
	}
	return routes
}

// Print writes the SID's routes in the scenario waypoint syntax.
func (s SID) Print(name string) {
	for _, r := range s.Routes(name) {
		fmt.Printf(routePrintFormat, r.Name, r.Route)
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

// LegLimit returns the extent of the procedure turn's outbound legs as a
// distance or a time, whichever the turn gives. Without one, the legs of
// an ILS, localizer, or VOR approach's turn are a minute long and an RNAV
// approach's are 4nm; both are zero for other approach types.
func (pt *ProcedureTurn) LegLimit(appr ApproachType) (nm, minutes float32) {
	switch {
	case pt.NmLimit > 0:
		return pt.NmLimit, 0
	case pt.MinuteLimit > 0:
		return 0, pt.MinuteLimit
	}
	switch appr {
	case ILSApproach, LocalizerApproach, VORApproach:
		return 0, 1
	case RNAVApproach:
		return 4, 0
	default:
		return 0, 0
	}
}

///////////////////////////////////////////////////////////////////////////
// NavigationRestriction

// NavigationRestriction is the shared base for altitude and speed restrictions.
// Range[0] is the floor, Range[1] is the ceiling.
// 0 means "no floor" (at or below); the type-specific max constant
// means "no ceiling" (at or above).
// Invariant: Range[0] <= Range[1].
type NavigationRestriction struct {
	Range [2]float32
}

// Target clamps val into the restriction's range.
func (r NavigationRestriction) Target(val float32) float32 {
	return math.Clamp(val, r.Range[0], r.Range[1])
}

// Satisfied returns true if val complies with the restriction.
func (r NavigationRestriction) Satisfied(val float32) bool {
	return val >= r.Range[0] && val <= r.Range[1]
}

// ExactValue returns the value if this is an "at" restriction (lo == hi),
// or false if it's a range.
func (r NavigationRestriction) ExactValue() (float32, bool) {
	if r.Range[0] == r.Range[1] {
		return r.Range[0], true
	}
	return 0, false
}

// ClampRange limits a range to satisfy the restriction;
// the returned Boolean indicates whether the ranges overlapped.
func (r NavigationRestriction) ClampRange(rng [2]float32) (c [2]float32, ok bool) {
	ok = rng[0] <= r.Range[1] && rng[1] >= r.Range[0]
	c[0] = math.Clamp(rng[0], r.Range[0], r.Range[1])
	c[1] = math.Clamp(rng[1], r.Range[0], r.Range[1])
	return
}

// encoded returns the restriction in compact text form, using maxVal as
// the sentinel for "no ceiling".
func (r NavigationRestriction) encoded(maxVal float32) string {
	if r.Range[0] != 0 {
		if r.Range[0] == r.Range[1] {
			return fmt.Sprintf("%.0f", r.Range[0])
		} else if r.Range[1] != maxVal {
			return fmt.Sprintf("%.0f-%.0f", r.Range[0], r.Range[1])
		} else {
			return fmt.Sprintf("%.0f+", r.Range[0])
		}
	} else if r.Range[1] != 0 && r.Range[1] != maxVal {
		return fmt.Sprintf("%.0f-", r.Range[1])
	} else {
		return ""
	}
}

///////////////////////////////////////////////////////////////////////////
// AltitudeRestriction

// MaxAltitude is used as the upper bound for "at or
// above" restrictions.  This lets the invariant Range[0] <= Range[1]
// always hold, eliminating sentinel checks in clamping/bounding code.
const MaxAltitude float32 = 100000

type AltitudeRestriction struct {
	NavigationRestriction
}

func MakeAtAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{alt, alt}}}
}

func MakeAtOrAboveAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{alt, MaxAltitude}}}
}

func MakeAtOrBelowAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{0, alt}}}
}

func MakeRangeAltitudeRestriction(low, high float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{low, high}}}
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
			// Migrate old serialized "at or above" {alt, 0} to {alt, MaxAlt}.
			if a.Range[0] != 0 && a.Range[1] == 0 {
				a.Range[1] = MaxAltitude
			}
			return nil
		} else {
			return err
		}
	}
}

// TargetAltitude clamps alt into the restriction's range.
func (a AltitudeRestriction) TargetAltitude(alt float32) float32 {
	return a.Target(alt)
}

// Encoded returns the restriction in the encoded form used in scenario
// configuration files, e.g. "5000+" for "at or above 5000".
func (a AltitudeRestriction) Encoded() string {
	return a.encoded(MaxAltitude)
}

///////////////////////////////////////////////////////////////////////////
// SpeedRestriction

// MaxRestrictionSpeed is the sentinel value used as the upper bound for
// "at or above" speed restrictions. It is not a real airspeed limit;
// compare against aircraft performance for actual maxima.
const MaxRestrictionSpeed float32 = 1000

type SpeedRestriction struct {
	NavigationRestriction
	IsMach bool
}

func MakeAtSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{speed, speed}}}
}

func MakeMachRestriction(mach float32) SpeedRestriction {
	return SpeedRestriction{
		NavigationRestriction: NavigationRestriction{Range: [2]float32{mach, mach}},
		IsMach:                true,
	}
}

func MakeAtOrAboveSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{speed, MaxRestrictionSpeed}}}
}

func MakeAtOrBelowSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{0, speed}}}
}

func MakeRangeSpeedRestriction(low, high float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{low, high}}}
}

func (s *SpeedRestriction) UnmarshalJSON(b []byte) error {
	// null: leave as zero value (no restriction).
	if string(b) == "null" {
		return nil
	}
	// Plain number: treat as "at" restriction (backwards compat with existing JSON).
	if spd, err := strconv.Atoi(string(b)); err == nil {
		s.Range = [2]float32{float32(spd), float32(spd)}
		return nil
	}
	// String form: "250-", "210+", "180-210", "210".
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		sr, err := ParseSpeedRestriction(str)
		if err != nil {
			return err
		}
		*s = *sr
		return nil
	}
	// Struct form: {"Range": [lo, hi], "IsMach": bool}.
	ar := struct {
		Range  [2]float32
		IsMach bool
	}{}
	if err := json.Unmarshal(b, &ar); err == nil {
		s.Range = ar.Range
		s.IsMach = ar.IsMach
		if s.IsMach && s.Range[0] != s.Range[1] {
			return fmt.Errorf("mach restriction must be exact")
		}
		return nil
	} else {
		return err
	}
}

// Encoded returns the restriction in compact text form, e.g. "210+", "250-", "210".
func (s SpeedRestriction) Encoded() string {
	if s.IsMach {
		if s.Range[0] != s.Range[1] {
			return ""
		}
		return fmt.Sprintf("M%02d", int(s.Range[0]*100+.5))
	}
	return s.encoded(MaxRestrictionSpeed)
}

// CheckJSON implements util.JSONChecker so the JSON type checker accepts
// plain numbers and strings in addition to the struct form.
func (s SpeedRestriction) CheckJSON(json any) bool {
	switch json.(type) {
	case float64, string, map[string]any, nil:
		return true
	}
	return false
}

// IsZero returns true if the speed restriction is unset.
func (s SpeedRestriction) IsZero() bool {
	return s.Range[0] == 0 && s.Range[1] == 0
}

// ParseSpeedRestriction parses a speed restriction from compact text form:
// "210", "210+", "210-", "180-210".
func ParseSpeedRestriction(s string) (*SpeedRestriction, error) {
	if s == "" {
		return nil, fmt.Errorf("empty speed restriction")
	}
	if strings.HasPrefix(s, "M") || strings.HasPrefix(s, "m") {
		machStr := s[1:]
		machStr = strings.TrimSuffix(machStr, "+")
		machStr = strings.TrimSuffix(machStr, "-")
		mach, err := strconv.Atoi(machStr)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		sr := MakeMachRestriction(float32(mach) / 100)
		return &sr, nil
	}

	if low, high, ok := strings.Cut(s, "-"); ok {
		// Either a range or at-or-below
		min, err := strconv.Atoi(low)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		if high != "" {
			max, err := strconv.Atoi(high)
			if err != nil {
				return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
			}
			sr := MakeRangeSpeedRestriction(float32(min), float32(max))
			return &sr, nil

		} else {
			sr := MakeAtOrBelowSpeedRestriction(float32(min))
			return &sr, nil
		}
	} else {
		// Single speed or at-or-above
		low, aoa := strings.CutSuffix(s, "+")
		min, err := strconv.Atoi(low)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		if aoa {
			sr := MakeAtOrAboveSpeedRestriction(float32(min))
			return &sr, nil
		} else {
			sr := MakeAtSpeedRestriction(float32(min))
			return &sr, nil
		}
	}
}

// ParseDistanceDirection parses strings like "5W" or "10NE" into a distance
// in miles and a cardinal/ordinal direction.
func ParseDistanceDirection(s string) (int, math.CardinalOrdinalDirection, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i == len(s) {
		return 0, 0, fmt.Errorf("invalid distance/direction")
	}
	dist, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, 0, err
	}
	dir, err := math.ParseCardinalOrdinalDirection(s[i:])
	if err != nil {
		return 0, 0, err
	}
	return dist, dir, nil
}

// ParseSyntheticCrossingFix parses a synthetic cross-distance waypoint name
// like "_DETGY/5W" and returns the named fix, distance, and direction.
func ParseSyntheticCrossingFix(fix string) (string, int, math.CardinalOrdinalDirection, bool) {
	if !strings.HasPrefix(fix, "_") {
		return "", 0, 0, false
	}
	parts := strings.Split(fix[1:], "/")
	if len(parts) < 2 || parts[0] == "" {
		return "", 0, 0, false
	}
	dist, dir, err := ParseDistanceDirection(parts[1])
	if err != nil {
		return "", 0, 0, false
	}
	return parts[0], dist, dir, true
}

// ParseSyntheticDMEFix parses a synthetic cross-DME waypoint name like
// "_04L_10DME" and returns the runway and distance (in nm) from the threshold.
func ParseSyntheticDMEFix(fix string) (string, int, bool) {
	if !strings.HasPrefix(fix, "_") || !strings.HasSuffix(fix, "DME") {
		return "", 0, false
	}
	inner := strings.TrimSuffix(fix[1:], "DME")
	i := strings.LastIndex(inner, "_")
	if i <= 0 || i == len(inner)-1 {
		return "", 0, false
	}
	dist, err := strconv.Atoi(inner[i+1:])
	if err != nil {
		return "", 0, false
	}
	return inner[:i], dist, true
}

///////////////////////////////////////////////////////////////////////////
// DMEArc

type DMEArcDirection int

const (
	DMEArcDirectionUnset            DMEArcDirection = iota
	DMEArcDirectionClockwise                        // right turn
	DMEArcDirectionCounterClockwise                 // left turn
)

func (d DMEArcDirection) IsClockwise() bool {
	return d == DMEArcDirectionClockwise
}

// Can either be specified with (Fix,Radius), or (Length,Direction); the
// remaining fields are then derived from those.
type DMEArc struct {
	Fix            string
	Center         math.Point2LL
	Radius         float32
	Length         float32
	InitialHeading math.MagneticHeading
	Direction      DMEArcDirection
}

// Initialize resolves the arc's center, radius, and initial heading from
// its specification. Direction must be set before calling. startLoc and
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
		delta := float32(util.Select(arc.Direction.IsClockwise(), -.01, .01))

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

	// Heading from the center of the arc to the start fix (true), then
	// convert to magnetic; perpendicular depending on the arc's direction.
	hfix := math.Heading2LL(arc.Center, startLoc, nmPerLongitude)
	arc.InitialHeading = math.TrueToMagnetic(
		math.OffsetHeading(hfix, float32(util.Select(arc.Direction.IsClockwise(), 90, -90))),
		magneticVariation)

	return true
}

///////////////////////////////////////////////////////////////////////////
// Hold

// TurnDirection specifies the direction of a turn.
type TurnDirection math.TurnDirection

const (
	TurnClosest TurnDirection = TurnDirection(math.TurnClosest) // default: turn the shortest direction
	TurnLeft    TurnDirection = TurnDirection(math.TurnLeft)
	TurnRight   TurnDirection = TurnDirection(math.TurnRight)
)

func (t TurnDirection) String() string {
	return []string{"closest", "left", "right"}[int(t)]
}

// Hold represents a charted holding pattern from CIFP or HPF
type Hold struct {
	Fix             string               // Fix identifier where hold is located
	InboundCourse   math.MagneticHeading // Inbound magnetic course to the fix
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

func (h Hold) Entry(headingToFix math.MagneticHeading) HoldEntry {
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
	CruiseAltitudes     util.SingleOrArray[int] `json:"cruise_altitude"`
	AssignedAltitude    float32                 `json:"assigned_altitude"`
	InitialSpeed        Airspeed                `json:"initial_speed"`
	AssignedSpeed       float32                 `json:"assigned_speed"`
	SpeedRestriction    SpeedRestriction        `json:"speed_restriction"`
	InitialController   ControlPosition         `json:"initial_controller"`
	Scratchpad          string                  `json:"scratchpad"`
	SecondaryScratchpad string                  `json:"secondary_scratchpad"`
	Description         string                  `json:"description"`
	IsRNAV              bool                    `json:"is_rnav"`
	Airlines            []OverflightAirline     `json:"airlines"`
	TypeOfFlightString  string                  `json:"flight_type"`
	TypeOfFlight        TypeOfFlight            // set via TypeOfFlightString
}

type OverflightAirline struct {
	AirlineSpecifier
	DepartureAirport ICAOAirportCode `json:"departure_airport"`
	ArrivalAirport   ICAOAirportCode `json:"arrival_airport"`
}

func (of *Overflight) PostDeserialize(loc Locator, nmPerLongitude float32, magneticVariation float32,
	airports map[ICAOAirportCode]*Airport, controlPositions map[ControlPosition]*Controller, checkScratchpad func(string) bool,
	e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())
	if len(of.Waypoints) < 2 {
		e.ErrorString(`must provide at least two "waypoints" for overflight`)
	}

	of.Waypoints = of.Waypoints.InitializeLocations(loc, nmPerLongitude, magneticVariation, false, e)

	of.Waypoints[len(of.Waypoints)-1].MergeActions(WaypointActions{Delete: true})
	of.Waypoints[len(of.Waypoints)-1].SetFlyOver(true)

	of.Waypoints.CheckOverflight(e, controlPositions, checkScratchpad)

	if len(of.Airlines) == 0 {
		e.ErrorString(`must specify at least one airline in "airlines"`)
	}
	for i := range of.Airlines {
		of.Airlines[i].Check(e)

		if of.Airlines[i].DepartureAirport == "" {
			e.ErrorString(`must specify "departure_airport"`)
		} else if _, ok := airports[of.Airlines[i].DepartureAirport]; !ok {
			if err := CheckAirport("departure", of.Airlines[i].DepartureAirport); err != nil {
				e.Error(err)
			}
		}

		if of.Airlines[i].ArrivalAirport == "" {
			e.ErrorString(`must specify "arrival_airport"`)
		} else if _, ok := airports[of.Airlines[i].ArrivalAirport]; !ok {
			if err := CheckAirport("arrival", of.Airlines[i].ArrivalAirport); err != nil {
				e.Error(err)
			}
		}
	}

	if len(of.InitialAltitudes) == 0 {
		e.ErrorString(`must specify at least one "initial_altitude"`)
	}

	if of.InitialSpeed.IsZero() {
		e.ErrorString(`must specify "initial_speed"`)
	} else {
		checkSpeed(e, `"initial_speed"`, of.InitialSpeed)
	}

	if of.AssignedSpeed != 0 {
		checkSpeed(e, `"assigned_speed"`, MakeIAS(of.AssignedSpeed))
	}

	if !of.SpeedRestriction.IsZero() {
		checkSpeedRange(e, of.SpeedRestriction)
	}

	if of.InitialController == "" {
		e.ErrorString(`Must specify "initial_controller".`)
	} else if _, ok := controlPositions[of.InitialController]; !ok {
		e.ErrorString(`controller %q not found for "initial_controller"`, of.InitialController)
	}

	if !checkScratchpad(of.Scratchpad) {
		e.ErrorString("%s: invalid scratchpad", of.Scratchpad)
	}
	if !checkScratchpad(of.SecondaryScratchpad) {
		e.ErrorString("%s: invalid secondary scratchpad", of.SecondaryScratchpad)
	}

	switch of.TypeOfFlightString {
	case "", "overflight":
		of.TypeOfFlight = FlightTypeOverflight
	case "departure":
		of.TypeOfFlight = FlightTypeDeparture
	case "arrival":
		of.TypeOfFlight = FlightTypeArrival
	default:
		e.ErrorString(`%s: unknown "flight_type" value. Options: "departure", "arrival", "overflight".`,
			of.TypeOfFlightString)
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

// RouteRayIntersection extends math.RayRouteIntersection with the index of
// the WaypointArray the hit falls on.
type RouteRayIntersection struct {
	math.RayRouteIntersection
	RouteIndex int
}

// IntersectRayWithRoutes runs math.IntersectRayWithRoute on each entry in
// routes and returns all hits. Callers apply their own scoring (closest,
// earliest, turn-angle tiers, etc.).
func IntersectRayWithRoutes(origin math.Point2LL, heading math.TrueHeading, routes []WaypointArray) []RouteRayIntersection {
	var hits []RouteRayIntersection
	for ri, route := range routes {
		pts := make([]math.Point2LL, len(route))
		for i, wp := range route {
			pts[i] = wp.Location
		}
		for _, h := range math.IntersectRayWithRoute(origin, heading, pts) {
			hits = append(hits, RouteRayIntersection{RayRouteIntersection: h, RouteIndex: ri})
		}
	}
	return hits
}

// ClosestRayRouteIntersection returns the hit with the smallest distance
// from origin across all supplied routes.
func ClosestRayRouteIntersection(origin math.Point2LL, heading math.TrueHeading, routes []WaypointArray) (RouteRayIntersection, bool) {
	var best RouteRayIntersection
	found := false
	bestDist := float32(0)
	for _, h := range IntersectRayWithRoutes(origin, heading, routes) {
		d := math.NMDistance2LL(origin, h.Location)
		if !found || d < bestDist {
			best = h
			bestDist = d
			found = true
		}
	}
	return best, found
}

///////////////////////////////////////////////////////////////////////////

// ScrapedRoute is one way a city pair has recently been flown, taken from
// recently filed flight plans by cmd/scraperoutes.
type ScrapedRoute struct {
	Route string `json:"route"`
	// Count is how many times the route was filed over the sampled period.
	Count int `json:"count"`
	// Aircraft is the classes observed flying the route; zero means no one
	// looked.
	Aircraft AircraftClass `json:"aircraft,omitempty"`
	// MinAltitude and MaxAltitude bound the filed cruise altitudes, in feet.
	MinAltitude int `json:"min_altitude,omitempty"`
	MaxAltitude int `json:"max_altitude,omitempty"`
	// Hours is the local hours of day the route has been observed filed at:
	// noise abatement runs some routes only at night.
	Hours HourRanges `json:"hours,omitempty"`
}

// ScrapedRouteSet is everything the scraper knows about one city pair. An
// entry with no routes still records that the pair was looked up, so that it
// isn't fetched again until it goes stale.
type ScrapedRouteSet struct {
	// Updated is the YYYY-MM-DD day the pair was last fetched, which is what
	// cmd/scraperoutes judges staleness by.
	Updated string         `json:"updated"`
	Routes  []ScrapedRoute `json:"routes,omitempty"`
}

// ScrapedRoutesPath is where the scraped route database lives in the
// resources, keyed by "KSFO-KPDX"-style directed city pairs.
const ScrapedRoutesPath = "scraped-routes.json"

// ReadScrapedRoutes parses the scraped route database, or returns an empty
// map if none has been written yet.
func ReadScrapedRoutes(resources fs.StatFS) (map[string]ScrapedRouteSet, error) {
	sets := make(map[string]ScrapedRouteSet)
	if _, err := resources.Stat(ScrapedRoutesPath); err != nil {
		return sets, nil
	}
	b, err := fs.ReadFile(resources, ScrapedRoutesPath)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &sets); err != nil {
		return nil, fmt.Errorf("%s: %w", ScrapedRoutesPath, err)
	}
	return sets, nil
}

// parseScrapedRoutes loads the scraped route database for route selection,
// most-filed routes first.
func parseScrapedRoutes() map[AirportPair][]ScrapedRoute {
	sets, err := ReadScrapedRoutes(util.GetResourcesFS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	routes := make(map[AirportPair][]ScrapedRoute)
	for key, set := range sets {
		if len(set.Routes) == 0 {
			continue
		}
		from, to, ok := strings.Cut(key, "-")
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: %q isn't a FROM-TO city pair\n", ScrapedRoutesPath, key)
			os.Exit(1)
		}
		routes[AirportPair{From: ICAOAirportCode(from), To: ICAOAirportCode(to)}] = set.Routes
	}
	return routes
}

// HourRanges is a set of hours of the day, held as a bit per hour and encoded
// in JSON as ranges like "6-9,22-23".
type HourRanges uint32

// Contains reports whether the hour is in the set.
func (h HourRanges) Contains(hour int) bool {
	return h&(1<<(((hour%24)+24)%24)) != 0
}

// Add puts the hour in the set.
func (h *HourRanges) Add(hour int) {
	*h |= 1 << (((hour % 24) + 24) % 24)
}

func (h HourRanges) String() string {
	var ranges []string
	for hour := 0; hour < 24; {
		if !h.Contains(hour) {
			hour++
			continue
		}
		end := hour
		for end+1 < 24 && h.Contains(end+1) {
			end++
		}
		if end == hour {
			ranges = append(ranges, strconv.Itoa(hour))
		} else {
			ranges = append(ranges, fmt.Sprintf("%d-%d", hour, end))
		}
		hour = end + 1
	}
	return strings.Join(ranges, ",")
}

func (h HourRanges) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

func (h *HourRanges) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	*h = 0
	if s == "" {
		return nil
	}
	for r := range strings.SplitSeq(s, ",") {
		first, last, isRange := strings.Cut(r, "-")
		start, err := strconv.Atoi(first)
		if err != nil {
			return fmt.Errorf("%q: %w", r, err)
		}
		end := start
		if isRange {
			if end, err = strconv.Atoi(last); err != nil {
				return fmt.Errorf("%q: %w", r, err)
			}
		}
		if start < 0 || end > 23 || start > end {
			return fmt.Errorf("%q: hours must run from 0 to 23", r)
		}
		for hour := start; hour <= end; hour++ {
			h.Add(hour)
		}
	}
	return nil
}
