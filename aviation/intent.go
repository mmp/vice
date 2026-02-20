// aviation/intent.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"reflect"
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// CommandIntent represents the semantic meaning of a pilot readback.
// Instead of generating RadioTransmission text directly, commands return
// intents that are later rendered together for coherent multi-command responses.
type CommandIntent interface {
	// Render adds the appropriate text for this intent to the transmission.
	Render(rt *RadioTransmission, r *rand.Rand)
}

// UnableIntent represents a pilot's "unable" response to a command.
// It is rendered as RadioTransmissionUnexpected and does not merge with other intents.
type UnableIntent struct {
	Message string
	Args    []any
}

func MakeUnableIntent(message string, args ...any) UnableIntent {
	return UnableIntent{Message: message, Args: args}
}

func (u UnableIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Type = RadioTransmissionUnexpected
	rt.Add(u.Message, u.Args...)
}

///////////////////////////////////////////////////////////////////////////
// Intent Merging Registration System

// intentMerger holds a registered merge function with its type signature
type intentMerger struct {
	paramTypes []reflect.Type
	fn         reflect.Value
}

// Registry of merge functions, sorted by: length descending, then registration order
var intentMergers []intentMerger

// commandIntentType is the reflect.Type for the CommandIntent interface
var commandIntentType = reflect.TypeFor[CommandIntent]()

// RegisterIntentMerger registers a merge function.
// fn must have signature: func(IntentType1, IntentType2, ...) ([]CommandIntent, bool)
// where all parameter types implement CommandIntent.
// Panics if signature is invalid or duplicate type sequence exists.
func RegisterIntentMerger(fn any) {
	fnVal := reflect.ValueOf(fn)
	fnType := fnVal.Type()

	if fnType.Kind() != reflect.Func {
		panic("RegisterIntentMerger: argument must be a function")
	}

	// Validate return type is ([]CommandIntent, bool)
	if fnType.NumOut() != 2 {
		panic("RegisterIntentMerger: function must return ([]CommandIntent, bool)")
	}
	sliceType := reflect.TypeFor[[]CommandIntent]()
	if fnType.Out(0) != sliceType {
		panic("RegisterIntentMerger: first return value must be []CommandIntent")
	}
	if fnType.Out(1) != reflect.TypeFor[bool]() {
		panic("RegisterIntentMerger: second return value must be bool")
	}

	// Extract parameter types and validate they implement CommandIntent
	numParams := fnType.NumIn()
	if numParams < 2 {
		panic("RegisterIntentMerger: function must have at least 2 parameters")
	}

	paramTypes := make([]reflect.Type, numParams)
	for i := range numParams {
		paramType := fnType.In(i)
		if !paramType.Implements(commandIntentType) {
			panic("RegisterIntentMerger: parameter " + paramType.String() + " does not implement CommandIntent")
		}
		paramTypes[i] = paramType
	}

	// Check for duplicate type sequence
	for _, existing := range intentMergers {
		if slices.Equal(existing.paramTypes, paramTypes) {
			panic("RegisterIntentMerger: duplicate type sequence already registered")
		}
	}

	merger := intentMerger{
		paramTypes: paramTypes,
		fn:         fnVal,
	}

	// Insert maintaining length-descending order (longer matches first)
	idx := slices.IndexFunc(intentMergers, func(im intentMerger) bool { return len(paramTypes) > len(im.paramTypes) })
	if idx == -1 {
		intentMergers = append(intentMergers, merger)
	} else {
		intentMergers = slices.Insert(intentMergers, idx, merger)
	}
}

///////////////////////////////////////////////////////////////////////////
// Altitude Intent

type AltitudeDirection int

const (
	AltitudeClimb AltitudeDirection = iota
	AltitudeDescend
	AltitudeMaintain
)

// AltitudeIntent represents climb/descend/maintain altitude commands
type AltitudeIntent struct {
	Altitude          float32
	Direction         AltitudeDirection
	AfterSpeed        *float32 // changing altitude only after reaching a speed
	Expedite          bool
	AlreadyExpediting bool
	ThenSpeed         *float32 // speed once we reach the altitude
	ThenSpeedType     SpeedType
}

func (a AltitudeIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	if a.AlreadyExpediting {
		rt.Add("[we're already expediting|that's our best rate]")
		return
	}

	earlySpeed := r.Bool()
	if earlySpeed && a.AfterSpeed != nil {
		rt.Add("at {spd}", *a.AfterSpeed)
	}

	if a.Expedite {
		if a.Direction == AltitudeClimb {
			rt.Add("[expediting up to|expedite] {alt}", a.Altitude)
		} else {
			rt.Add("[expediting down to|expedite] {alt}", a.Altitude)
		}
	} else {
		switch a.Direction {
		case AltitudeClimb:
			rt.Add("[climb-and-maintain|up to|] {alt}", a.Altitude)
		case AltitudeDescend:
			rt.Add("[descend-and-maintain|down to|] {alt}", a.Altitude)
		case AltitudeMaintain:
			rt.Add("[maintain|we'll keep it at|] {alt}", a.Altitude)
		}
	}

	if a.ThenSpeed != nil {
		switch a.ThenSpeedType {
		case SpeedReduce:
			rt.Add("[then we'll reduce to|then slow to|then speed] {spd}", *a.ThenSpeed)
		case SpeedIncrease:
			rt.Add("[then we'll increase to|then increase to|then speed] {spd}", *a.ThenSpeed)
		default:
			rt.Add("[then speed|then maintain] {spd}", *a.ThenSpeed)
		}
	}

	if !earlySpeed && a.AfterSpeed != nil {
		rt.Add("[once we're|] at {spd}", *a.AfterSpeed)
	}
}

///////////////////////////////////////////////////////////////////////////
// Speed Intents

type SpeedType int

const (
	SpeedAssign SpeedType = iota
	SpeedReduce
	SpeedIncrease
	SpeedCancel
	SpeedSlowestPractical
	SpeedMaximumForward
	SpeedPresentSpeed
	SpeedUntilFinal // speed restriction until 5 mile final
)

// SpeedUntil specifies when a speed restriction ends.
// Only one field should be set at a time.
type SpeedUntil struct {
	Fix       string // fix name (e.g., "ROSLY")
	DME       int    // DME distance (e.g., 5 for "5 DME")
	MileFinal int    // mile final (e.g., 6 for "6 mile final")
}

// SpeedIntent represents speed assignment commands
type SpeedIntent struct {
	Speed         float32
	Type          SpeedType
	AfterAltitude *float32    // speed change conditional on reaching this altitude
	Until         *SpeedUntil // what the speed restriction is "until"
	Mach          bool
}

func (s SpeedIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	if s.Mach {
		s.renderMach(rt, r)
		return
	}

	switch s.Type {
	case SpeedCancel:
		rt.Add("cancel speed restrictions")
	case SpeedSlowestPractical:
		rt.Add("[slowest practical speed|slowing as much as we can]")
	case SpeedMaximumForward:
		rt.Add("[maximum forward speed|maintaining maximum forward speed]")
	case SpeedPresentSpeed:
		rt.Add("[maintain present speed|present speed we'll keep it at {spd}|maintaining {spd}]", s.Speed)
	case SpeedUntilFinal:
		if s.Until != nil {
			if s.Until.Fix != "" {
				rt.Add("{spd} until {fix}", s.Speed, s.Until.Fix)
			} else if s.Until.DME > 0 {
				rt.Add("{spd} until {num} D M E", s.Speed, s.Until.DME)
			} else if s.Until.MileFinal > 0 {
				rt.Add("{spd} until {num} mile final", s.Speed, s.Until.MileFinal)
			}
		} else {
			rt.Add("[speed {spd} for now|we'll keep it at {spd} for now]", s.Speed)
		}
	case SpeedReduce:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} maintain {spd}|at {alt} {spd}|{alt} then {spd}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[reduce to {spd}|speed {spd}|slow to {spd}|{spd}]", s.Speed)
		}
	case SpeedIncrease:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} maintain {spd}|at {alt} {spd}|{alt} then {spd}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[increase to {spd}|speed {spd}|maintain {spd}|{spd}]", s.Speed)
		}
	case SpeedAssign:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} maintain {spd}|at {alt} {spd}|{alt} then {spd}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[speed {spd}|maintain {spd}|{spd}]", s.Speed)
		}
	}
}

func (s SpeedIntent) renderMach(rt *RadioTransmission, r *rand.Rand) {
	switch s.Type {
	case SpeedCancel:
		rt.Add("cancel speed restrictions")
	case SpeedReduce:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} {mach}|{alt} then {mach}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[reduce to {mach}|{mach}|slow to {mach}]", s.Speed)
		}
	case SpeedIncrease:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} {mach}|{alt} then {mach}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[increase to {mach}|{mach}|maintain {mach}]", s.Speed)
		}
	case SpeedAssign:
		if s.AfterAltitude != nil {
			rt.Add("[at {alt} {mach}|{alt} then {mach}]", *s.AfterAltitude, s.Speed)
		} else {
			rt.Add("[{mach}|maintain {mach}]", s.Speed)
		}
	}
}

// ReportSpeedIntent represents "say speed" responses (IAS)
type ReportSpeedIntent struct {
	Current  float32
	Assigned *float32
}

func (r ReportSpeedIntent) Render(rt *RadioTransmission, rnd *rand.Rand) {
	if r.Assigned != nil {
		if *r.Assigned < r.Current {
			rt.Add("[at {spd} slowing to {spd}|at {spd} down to {spd}]", r.Current, *r.Assigned)
		} else if *r.Assigned > r.Current {
			rt.Add("at {spd} speeding up to {spd}", r.Current, *r.Assigned)
		} else {
			rt.Add("[maintaining {spd}|at {spd}]", r.Current)
		}
	} else {
		rt.Add("[maintaining {spd}|at {spd}]", r.Current)
	}
}

// ReportMachIntent represents "say mach" or mach-regime "say speed" responses
type ReportMachIntent struct {
	Current  float32  // current mach (e.g., 0.78)
	Assigned *float32 // assigned mach, if any
}

func (r ReportMachIntent) Render(rt *RadioTransmission, rnd *rand.Rand) {
	if r.Assigned != nil {
		if *r.Assigned < r.Current {
			rt.Add("[at {mach} slowing to {mach}|{mach} down to {mach}]", r.Current, *r.Assigned)
		} else if *r.Assigned > r.Current {
			rt.Add("[at {mach} speeding up to {mach}|{mach} increasing to {mach}]", r.Current, *r.Assigned)
		} else {
			rt.Add("[maintaining {mach}|at {mach}]", r.Current)
		}
	} else {
		rt.Add("[maintaining {mach}|at {mach}]", r.Current)
	}
}

///////////////////////////////////////////////////////////////////////////
// Heading Intents

type HeadingType int

const (
	HeadingAssign HeadingType = iota
	HeadingTurnLeft
	HeadingTurnRight
	HeadingPresent
)

type HeadingTurn int

const (
	HeadingTurnClosest HeadingTurn = iota
	HeadingTurnToLeft
	HeadingTurnToRight
)

// HeadingIntent represents heading assignment commands
type HeadingIntent struct {
	Heading    float32
	Type       HeadingType
	Turn       HeadingTurn // for HeadingAssign: which way to turn
	Degrees    int         // for HeadingTurnLeft/Right: how many degrees
	CancelHold bool        // heading cancels an active hold
}

func (h HeadingIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	prefix := ""
	if h.CancelHold {
		prefix = "cancel [the|] hold, "
	}

	switch h.Type {
	case HeadingAssign:
		switch h.Turn {
		case HeadingTurnClosest:
			rt.Add(prefix+"[heading|fly heading] {hdg}", h.Heading)
		case HeadingTurnToRight:
			rt.Add(prefix+"[right heading|right|turn right] {hdg}", h.Heading)
		case HeadingTurnToLeft:
			rt.Add(prefix+"[left heading|left|turn left] {hdg}", h.Heading)
		}
	case HeadingTurnLeft:
		rt.Add("[turn {num} degrees left|{num} to the left|{num} left]", h.Degrees)
	case HeadingTurnRight:
		rt.Add("[turn {num} degrees right|{num} to the right|{num} right]", h.Degrees)
	case HeadingPresent:
		rt.Add("[fly present heading|present heading]")
	}
}

// ReportHeadingIntent represents "say heading" responses
type ReportHeadingIntent struct {
	Current  float32
	Assigned *float32
}

func (r ReportHeadingIntent) Render(rt *RadioTransmission, rnd *rand.Rand) {
	if r.Assigned != nil && *r.Assigned != r.Current {
		// Round current heading to multiple of 5 toward assigned heading
		current := r.Current
		assigned := *r.Assigned

		lower := math.Floor(current/5) * 5
		upper := math.Ceil(current/5) * 5

		var rounded float32
		if lower == upper {
			// Current is already a multiple of 5
			rounded = current
		} else if math.HeadingSignedTurn(current, assigned) >= 0 {
			// Turning right/clockwise, round up
			rounded = upper
		} else {
			// Turning left/counter-clockwise, round down
			rounded = lower
		}

		// Normalize 0 to 360 for aviation convention
		if rounded == 0 {
			rounded = 360
		}

		if rounded == assigned {
			// Rounded heading equals assigned, just report current
			rt.Add("[heading|] {hdg}", r.Current)
		} else {
			rt.Add("[heading|at|] {hdg} [turning to|turning|] {hdg}", rounded, assigned)
		}
	} else {
		rt.Add("[heading|] {hdg}", r.Current)
	}
}

// ReportAltitudeIntent represents "say altitude" responses
type ReportAltitudeIntent struct {
	Current   float32
	Assigned  *float32
	Direction AltitudeDirection // if climbing/descending
}

func (r ReportAltitudeIntent) Render(rt *RadioTransmission, rnd *rand.Rand) {
	if r.Assigned != nil {
		if *r.Assigned < r.Current {
			rt.Add("[at|] {alt} descending [to|] {alt}", r.Current, *r.Assigned)
		} else if *r.Assigned > r.Current {
			rt.Add("[at|] {alt} climbing [to|] {alt}", r.Current, *r.Assigned)
		} else {
			rt.Add("[maintaining {alt}|at {alt}]", r.Current)
		}
	} else {
		rt.Add("maintaining {alt}", r.Current)
	}
}

///////////////////////////////////////////////////////////////////////////
// Navigation Intents

type NavigationType int

const (
	NavDirectFix         NavigationType = iota
	NavDirectFixFromHold                // direct fix that cancels a hold
	NavHold
	NavDepartFixDirect
	NavDepartFixHeading
	NavCrossFixAt
	NavResumeOwnNav
	NavAltitudeDiscretion
)

// NavigationIntent represents navigation commands (direct, hold, depart fix, etc.)
type NavigationIntent struct {
	Type           NavigationType
	Fix            string
	SecondFix      string               // for DepartFixDirect
	Heading        float32              // for DepartFixHeading
	HoldDirection  string               // "left" or "right" for holds
	HoldLegLength  string               // e.g., "2 mile" or "1 minute"
	AltRestriction *AltitudeRestriction // for CrossFixAt
	Speed          *float32             // for CrossFixAt
}

func (n NavigationIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch n.Type {
	case NavDirectFix:
		rt.Add("direct {fix}", n.Fix)
	case NavDirectFixFromHold:
		rt.Add("cancel [the|] hold[ and|,] depart {fix} direct {fix}", n.Fix, n.SecondFix)
	case NavHold:
		if n.HoldLegLength != "" {
			rt.Add("hold "+n.HoldDirection+" of {fix}, "+n.HoldLegLength+" legs", n.Fix)
		} else {
			rt.Add("hold "+n.HoldDirection+" of {fix}", n.Fix)
		}
	case NavDepartFixDirect:
		rt.Add("depart {fix} direct {fix}", n.Fix, n.SecondFix)
	case NavDepartFixHeading:
		rt.Add("depart {fix} heading {hdg}", n.Fix, n.Heading)
	case NavCrossFixAt:
		rt.Add("cross {fix}", n.Fix)
		if n.AltRestriction != nil {
			rt.Add("{altrest}", n.AltRestriction)
		}
		if n.Speed != nil {
			rt.Add("at {spd}", *n.Speed)
		}
	case NavResumeOwnNav:
		rt.Add("[own navigation|resuming own navigation]")
	case NavAltitudeDiscretion:
		rt.Add("[altitude our discretion|altitude our discretion, maintain VFR]")
	}
}

///////////////////////////////////////////////////////////////////////////
// Approach Intents

type ApproachIntentType int

const (
	ApproachExpect ApproachIntentType = iota
	ApproachIntercept
	ApproachJoin // for non-ILS approaches
	ApproachAtFixCleared
	ApproachAtFixIntercept
	ApproachCancel
)

// ApproachIntent represents approach-related commands
type ApproachIntent struct {
	Type         ApproachIntentType
	ApproachName string // full name of the approach (e.g., "ILS Runway 22L")
	Fix          string // for AtFixCleared
	LAHSORunway  string // runway to hold short of (for LAHSO operations)
}

func (a ApproachIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch a.Type {
	case ApproachExpect:
		rt.Add("[we'll expect the|expecting the|we'll plan for the] {appr} approach", a.ApproachName)
		if a.LAHSORunway != "" {
			rt.Add("[and we'll hold short of|hold short of] runway {rwy}", a.LAHSORunway)
		}
	case ApproachIntercept:
		rt.Add("[intercepting the {appr} approach|intercepting {appr}]", a.ApproachName)
	case ApproachJoin:
		rt.Add("[joining the {appr} approach course|joining {appr}]", a.ApproachName)
	case ApproachAtFixCleared:
		rt.Add("at {fix} cleared {appr}", a.Fix, a.ApproachName)
	case ApproachAtFixIntercept:
		rt.Add("[intercept at {fix}|at {fix} intercept the localizer|at {fix} join the localizer]", a.Fix)
	case ApproachCancel:
		rt.Add("cancel approach clearance.")
	}
}

// ClearedApproachIntent represents approach clearance
type ClearedApproachIntent struct {
	Approach   string
	StraightIn bool
	CancelHold bool
}

func (c ClearedApproachIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	prefix := ""
	if c.CancelHold {
		prefix = "cancel [the|] hold, "
	}

	if c.StraightIn {
		rt.Add(prefix+"cleared straight in {appr} [approach|]", c.Approach)
	} else {
		rt.Add(prefix+"cleared {appr} [approach|]", c.Approach)
	}
}

///////////////////////////////////////////////////////////////////////////
// Procedure Intents

type ProcedureType int

const (
	ProcedureClimbViaSID ProcedureType = iota
	ProcedureDescendViaSTAR
)

// ProcedureIntent represents climb via SID / descend via STAR
type ProcedureIntent struct {
	Type ProcedureType
}

func (p ProcedureIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch p.Type {
	case ProcedureClimbViaSID:
		rt.Add("climb via the SID")
	case ProcedureDescendViaSTAR:
		rt.Add("descend via the STAR")
	}
}

///////////////////////////////////////////////////////////////////////////
// Contact Intents

type ContactType int

const (
	ContactController ContactType = iota
	ContactGoodbye
	ContactRadarTerminated
)

// ContactIntent represents contact/handoff commands
type ContactIntent struct {
	Type         ContactType
	ToController *Controller // the controller being contacted
	Frequency    Frequency
	IsDeparture  bool // affects rendering (departure vs approach controller)
}

func (c ContactIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch c.Type {
	case ContactController:
		if c.IsDeparture {
			rt.Add("[contact|over to|] {dctrl} on {freq}, [good day|seeya|]", c.ToController, c.Frequency)
		} else {
			rt.Add("[contact|over to|] {actrl} on {freq}, [good day|seeya|]", c.ToController, c.Frequency)
		}
	case ContactGoodbye:
		rt.Add("[goodbye|seeya]")
	case ContactRadarTerminated:
		rt.Add("[radar services terminated, seeya|radar services terminated, squawk VFR]")
	}
}

///////////////////////////////////////////////////////////////////////////
// Transponder Intents

type TransponderType int

// TransponderIntent represents squawk/ident/mode commands
type TransponderIntent struct {
	Code  *Squawk
	Mode  *TransponderMode
	Ident bool
}

func (t TransponderIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	if t.Ident && t.Code == nil && t.Mode == nil {
		rt.Add("ident")
		return
	}

	if t.Code != nil {
		if t.Ident {
			rt.Add("squawk {beacon}[ and|,] ident", *t.Code)
		} else {
			rt.Add("squawk {beacon}", *t.Code)
		}
	}
	if t.Mode != nil {
		if t.Ident && t.Code == nil {
			rt.Add("squawk " + t.Mode.String() + "[ and|,] ident")
		} else {
			rt.Add("squawk " + t.Mode.String())
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Special Intents

// ContactTowerIntent represents contact tower command
type ContactTowerIntent struct{}

func (c ContactTowerIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add("[contact|over to|] tower")
}

// ATISIntent represents the pilot's acknowledgment of the ATIS letter.
type ATISIntent struct {
	Letter string
}

func (a ATISIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add("[we'll pick up {ch}|we'll get {ch}]", a.Letter)
}

///////////////////////////////////////////////////////////////////////////
// Traffic Advisory Intent

// TrafficAdvisoryResponse represents the pilot's response to a traffic advisory
type TrafficAdvisoryResponse int

const (
	TrafficResponseIMC         TrafficAdvisoryResponse = iota // In IMC, can't see traffic
	TrafficResponseLooking                                    // No traffic visible, will look
	TrafficResponseTrafficSeen                                // Traffic is in sight
)

// TrafficAdvisoryIntent represents a pilot's response to a traffic advisory
type TrafficAdvisoryIntent struct {
	Response               TrafficAdvisoryResponse
	WillMaintainSeparation bool // If true, add "will maintain visual separation"
}

func (t TrafficAdvisoryIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch t.Response {
	case TrafficResponseIMC:
		rt.Add("[we're in IMC|we're IMC|in the clouds|IMC]")
	case TrafficResponseLooking:
		rt.Add("[looking|we're looking|looking for traffic|we'll keep an eye out]")
	case TrafficResponseTrafficSeen:
		if t.WillMaintainSeparation {
			rt.Add("[we have the traffic, will maintain visual separation|traffic in sight, we'll maintain visual|we see the traffic, will maintain visual separation]")
		} else {
			rt.Add("[we have the traffic|traffic in sight|we see the traffic|got the traffic]")
		}
	}
}

// VisualSeparationIntent represents a pilot's acknowledgment of visual separation responsibility
type VisualSeparationIntent struct{}

func (v VisualSeparationIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add("[will maintain visual separation|we'll maintain visual separation|maintaining visual separation|visual separation]")
}

///////////////////////////////////////////////////////////////////////////
// SayAgain Intent

// SayAgainCommandType identifies which type of command the pilot is asking to be repeated.
type SayAgainCommandType int

const (
	SayAgainHeading SayAgainCommandType = iota
	SayAgainAltitude
	SayAgainSpeed
	SayAgainApproach
	SayAgainTurn
	SayAgainSquawk
	SayAgainFix
)

// SayAgainIntent represents a pilot requesting the controller repeat part of a clearance.
// This is used when STT successfully identifies a command keyword but fails to parse
// the associated value (e.g., "fly heading blark bling five").
type SayAgainIntent struct {
	CommandType SayAgainCommandType
}

func (s SayAgainIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch s.CommandType {
	case SayAgainHeading:
		rt.Add("[say again on that heading|what was that heading again|sorry, you got blocked on the heading|missed the heading, say again]")
	case SayAgainAltitude:
		rt.Add("[say again on that altitude|what was that altitude again|sorry, you got blocked on the altitude|missed the altitude, say again]")
	case SayAgainSpeed:
		rt.Add("[say again on that speed|what was that speed again|sorry, you got blocked on the speed|missed the speed, say again]")
	case SayAgainApproach:
		rt.Add("[say again on that approach|what approach was that again|sorry, you got blocked on the approach|missed the approach, say again]")
	case SayAgainTurn:
		rt.Add("[say again on that turn|what was that turn again|sorry, you got blocked on the turn|missed the turn, say again]")
	case SayAgainSquawk:
		rt.Add("[say again on that squawk|what was that squawk again|sorry, you got blocked on the squawk|missed the squawk code, say again]")
	case SayAgainFix:
		rt.Add("[say again on that fix|what fix was that again|sorry, you got blocked on the fix|missed the fix, say again]")
	}
}

///////////////////////////////////////////////////////////////////////////
// MixUp Intent

// MixUpIntent represents pilot confusion about who was addressed
type MixUpIntent struct {
	Callsign    ADSBCallsign
	IsEmergency bool
}

func (m MixUpIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Type = RadioTransmissionMixUp
	csArg := CallsignArg{
		Callsign:           m.Callsign,
		IsEmergency:        m.IsEmergency,
		AlwaysFullCallsign: true,
	}
	rt.Add("sorry, was that for {callsign}?", csArg)
}

///////////////////////////////////////////////////////////////////////////
// RenderIntents

// RenderIntents converts a slice of CommandIntents into a single coherent RadioTransmission.
// It handles merging related intents (e.g., altitude + expedite), PTACs, etc., for more
// realistic readbacks.
func RenderIntents(intents []CommandIntent, r *rand.Rand) *RadioTransmission {
	if len(intents) == 0 {
		return nil
	}

	rt := &RadioTransmission{Type: RadioTransmissionReadback}
	for _, intent := range mergeIntents(intents) {
		intent.Render(rt, r)
	}

	return rt
}

// callMerger invokes the merge function with the given intents and returns the result
func callMerger(m intentMerger, intents []CommandIntent) ([]CommandIntent, bool) {
	args := make([]reflect.Value, len(m.paramTypes))
	for i := range m.paramTypes {
		args[i] = reflect.ValueOf(intents[i])
	}
	results := m.fn.Call(args)
	replacement := results[0].Interface().([]CommandIntent)
	ok := results[1].Bool()
	return replacement, ok
}

// findOrderedMatches finds indices of intents matching paramTypes in order (but not necessarily contiguously).
// Returns nil if no ordered match is found.
func findOrderedMatches(intentTypes []reflect.Type, paramTypes []reflect.Type) []int {
	indices := make([]int, 0, len(paramTypes))
	start := 0

	for _, paramType := range paramTypes {
		idx := slices.IndexFunc(intentTypes[start:], func(t reflect.Type) bool {
			return t == paramType
		})
		if idx == -1 {
			return nil
		}
		absIdx := start + idx
		indices = append(indices, absIdx)
		start = absIdx + 1
	}
	return indices
}

// mergeIntents combines related intents using registered merge functions.
// Merge functions are tried in priority order (longer matches first, then registration order).
// Matches are ordered but not necessarily contiguous (e.g., [A, X, B] matches a merger for [A, B]).
// The algorithm restarts from the top after each successful merge and continues until
// no further progress is made.
func mergeIntents(intents []CommandIntent) []CommandIntent {
restart:
	for range 20 { // protect against infinite loop
		intentTypes := util.MapSlice(intents, func(ci CommandIntent) reflect.Type {
			return reflect.TypeOf(ci)
		})

		for _, merger := range intentMergers {
			matchIndices := findOrderedMatches(intentTypes, merger.paramTypes)
			if matchIndices == nil {
				continue
			}

			// Gather matched intents
			matchedIntents := make([]CommandIntent, len(matchIndices))
			for i, idx := range matchIndices {
				matchedIntents[i] = intents[idx]
			}

			if replacement, ok := callMerger(merger, matchedIntents); ok {
				// Remove matched intents in reverse order (to preserve indices)
				for i := len(matchIndices) - 1; i >= 0; i-- {
					intents = slices.Delete(intents, matchIndices[i], matchIndices[i]+1)
				}
				// Insert replacement at position of first match
				intents = slices.Insert(intents, matchIndices[0], replacement...)
				continue restart
			}
		}
		// No matches this time, we done.
		break
	}
	return intents
}

// mergeAltitudeExpedite merges an AltitudeIntent representing an altitude assignment followed by
// AltitudeIntent with an expedite command into a single AltitudeIntent with Expedite=true.
func mergeAltitudeExpedite(alt AltitudeIntent, exp AltitudeIntent) ([]CommandIntent, bool) {
	// Leave the second one if we're going to say we're already expediting so that we still have a
	// read-back of the altitude for the first one.
	if exp.Expedite && !exp.AlreadyExpediting {
		alt.Expedite = exp.Expedite
		return []CommandIntent{alt}, true
	}
	return nil, false
}

// Altitude, then speed
func mergeAltitudeSpeed(alt AltitudeIntent, spd SpeedIntent) ([]CommandIntent, bool) {
	if spd.AfterAltitude != nil && (spd.Type == SpeedReduce || spd.Type == SpeedIncrease || spd.Type == SpeedAssign) {
		alt.ThenSpeed = &spd.Speed
		alt.ThenSpeedType = spd.Type
		return []CommandIntent{alt}, true
	}
	return nil, false

}

func mergeTransponder(ta, tb TransponderIntent) ([]CommandIntent, bool) {
	if tb.Code != nil {
		ta.Code = tb.Code
	}
	if tb.Ident {
		ta.Ident = true
	}
	if tb.Mode != nil {
		ta.Mode = tb.Mode
	}
	return []CommandIntent{ta}, true
}

func init() {
	RegisterIntentMerger(mergeAltitudeExpedite)
	RegisterIntentMerger(mergeAltitudeSpeed)
	RegisterIntentMerger(mergeTransponder)
}
