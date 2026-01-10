// aviation/intent.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"reflect"
	"slices"

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
var commandIntentType = reflect.TypeOf((*CommandIntent)(nil)).Elem()

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
	sliceType := reflect.TypeOf([]CommandIntent{})
	if fnType.Out(0) != sliceType {
		panic("RegisterIntentMerger: first return value must be []CommandIntent")
	}
	if fnType.Out(1) != reflect.TypeOf(true) {
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
	SpeedUntilFinal // speed restriction until 5 mile final
)

// SpeedIntent represents speed assignment commands
type SpeedIntent struct {
	Speed         float32
	Type          SpeedType
	AfterAltitude *float32 // speed change conditional on reaching this altitude
}

func (s SpeedIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch s.Type {
	case SpeedCancel:
		rt.Add("cancel speed restrictions")
	case SpeedSlowestPractical:
		rt.Add("[slowest practical speed|slowing as much as we can]")
	case SpeedMaximumForward:
		rt.Add("[maximum forward speed|maintaining maximum forward speed]")
	case SpeedUntilFinal:
		rt.Add("{spd} until 5 mile final", s.Speed)
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

// ReportSpeedIntent represents "say speed" responses
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
		rt.Add("[heading|at|] {hdg} [turning to|turning|]", r.Current, *r.Assigned)
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
	ApproachAlreadyExpecting
	ApproachIntercept
	ApproachJoin // for non-ILS approaches
	ApproachAtFixCleared
	ApproachCancel
)

// ApproachIntent represents approach-related commands
type ApproachIntent struct {
	Type         ApproachIntentType
	ApproachName string // full name of the approach (e.g., "ILS Runway 22L")
	Fix          string // for AtFixCleared
}

func (a ApproachIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch a.Type {
	case ApproachExpect:
		rt.Add("[we'll expect the|expecting the|we'll plan for the] {appr} approach", a.ApproachName)
	case ApproachAlreadyExpecting:
		rt.Add("you already told us to expect the {appr} approach.", a.ApproachName)
	case ApproachIntercept:
		rt.Add("[intercepting the {appr} approach|intercepting {appr}]", a.ApproachName)
	case ApproachJoin:
		rt.Add("[joining the {appr} approach course|joining {appr}]", a.ApproachName)
	case ApproachAtFixCleared:
		rt.Add("at {fix} cleared {appr}", a.Fix, a.ApproachName)
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

// GoAroundIntent represents a go-around instruction
type GoAroundIntent struct{}

func (g GoAroundIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add("[going around|on the go]")
}

// ContactTowerIntent represents contact tower command
type ContactTowerIntent struct{}

func (c ContactTowerIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add("[contact|over to|] tower")
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
// Initial Contact Intents

type InitialContactType int

const (
	InitialContactDeparture InitialContactType = iota
	InitialContactArrival
	InitialContactVFR
)

// InitialContactIntent represents pilot's initial contact on frequency
type InitialContactIntent struct {
	Type            InitialContactType
	CurrentAltitude float32
	TargetAltitude  *float32         // if climbing/descending
	Heading         *float32         // if on assigned heading
	STAR            string           // if on a STAR
	AircraftType    string           // for VFR contacts
	ReportingPoints []ReportingPoint // for position reports
}

func (i InitialContactIntent) Render(rt *RadioTransmission, r *rand.Rand) {
	switch i.Type {
	case InitialContactDeparture:
		if i.TargetAltitude != nil && *i.TargetAltitude-i.CurrentAltitude > 100 {
			rt.Add("[at|] {alt} climbing {alt}", i.CurrentAltitude, *i.TargetAltitude)
		} else {
			rt.Add("[at|] {alt}", i.CurrentAltitude)
		}
	case InitialContactArrival:
		if i.Heading != nil {
			rt.Add("[heading {hdg}|on a {hdg} heading]", *i.Heading)
		} else if i.STAR != "" {
			if i.TargetAltitude == nil {
				rt.Add("descending on the {star}", i.STAR)
			} else {
				rt.Add("on the {star}", i.STAR)
			}
		}
		if i.TargetAltitude != nil && *i.TargetAltitude != i.CurrentAltitude {
			rt.Add("[at|] {alt} for {alt} [assigned|]", i.CurrentAltitude, *i.TargetAltitude)
		}
	case InitialContactVFR:
		rt.Add("[VFR request|with a VFR request]")
	}
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
