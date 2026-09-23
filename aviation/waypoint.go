// aviation/waypoint.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/math"
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
	ClimbViaSID     bool
	DescendViaSTAR  bool
}

// HasSimActions reports whether the actions include any the sim carries
// out, which is to say any but the heading.
func (wa WaypointActions) HasSimActions() bool {
	return wa.HumanHandoff || wa.HandoffController != "" || wa.PointOut != "" ||
		wa.ClearApproach || wa.InterceptApproach || wa.GoAroundContactController != "" ||
		wa.PrimaryScratchpad != "" || wa.ClearPrimaryScratchpad ||
		wa.SecondaryScratchpad != "" || wa.ClearSecondaryScratchpad || wa.TransferComms ||
		wa.Delete || wa.Land || wa.ClimbAltitude != 0 || wa.DescendAltitude != 0 ||
		wa.ClimbViaSID || wa.DescendViaSTAR
}

// hasAltitudeAction reports whether the actions include a /c, /d, /cvs, or
// /dvs; the four supersede one another.
func (wa WaypointActions) hasAltitudeAction() bool {
	return wa.ClimbAltitude != 0 || wa.DescendAltitude != 0 || wa.ClimbViaSID || wa.DescendViaSTAR
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
	if wa.ClimbViaSID {
		s += "/cvs"
	}
	if wa.DescendViaSTAR {
		s += "/dvs"
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

// Clone returns a copy of we that shares nothing with the original, so that
// modifying the copy leaves the original untouched. A nil receiver gives a
// nil result.
func (we *WaypointExtra) Clone() *WaypointExtra {
	if we == nil {
		return nil
	}
	x := *we
	x.ActionGroups = util.DuplicateSlice(we.ActionGroups)
	if we.Arc != nil {
		arc := *we.Arc
		x.Arc = &arc
	}
	if we.ProcedureTurn != nil {
		pt := *we.ProcedureTurn
		x.ProcedureTurn = &pt
	}
	return &x
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
	wp.Extra = wp.Extra.Clone()
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
	wps := base.Clone()
	n := len(wps)
	for n > 0 && len(next) > 0 && wps[n-1].Fix == next[0].Fix {
		wps[n-1] = wps[n-1].MergeWith(next[0])
		next = next[1:]
	}
	return append(wps, next.Clone()...)
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
// has a /c, /d, /cvs, or /dvs altitude action.
func (wp Waypoint) HasAltitudeActions() bool {
	return slices.ContainsFunc(wp.ActionGroups(), func(group WaypointActionGroup) bool {
		return group.Actions.hasAltitudeAction()
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
