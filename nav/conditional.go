// nav/conditional.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	vmath "github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
)

// ConditionalKind is an alias for av.ConditionalKind so that nav and
// aviation share the same enum (aviation holds the canonical definition
// because ConditionalCommandIntent lives there and nav cannot be imported
// from aviation).
//
// ConditionalLeaving fires once the aircraft's altitude has passed the
// trigger by more than a small tolerance in the direction of current
// vertical motion. ConditionalReaching fires on first contact within
// 100 ft of the trigger altitude, regardless of vertical rate.
type ConditionalKind = av.ConditionalKind

const (
	ConditionalLeaving  = av.ConditionalLeaving
	ConditionalReaching = av.ConditionalReaching
)

// ConditionalAction is the deferred action to execute when a LV/RC trigger
// fires. Concrete types cover the closed set of supported inner commands
// (heading, direct-fix, speed, mach).
type ConditionalAction interface {
	// Execute mutates nav to carry out the deferred action. Called with the
	// PendingConditionalCommand slot already cleared, so re-entry is safe.
	// temp is the outside air temperature at the aircraft's current altitude,
	// required by mach-speed conversions; other actions ignore it.
	Execute(nav *Nav, simTime Time, temp av.Temperature)

	// Render emits the action-specific readback fragment (e.g., "fly heading
	// 010") used inside ConditionalCommandIntent.
	Render(rt *av.RadioTransmission, r *rand.Rand)
}

// PendingConditionalCommand is the single slot on Nav that stores a
// deferred LV/RC action. A new LV/RC command supersedes any prior slot;
// successful trigger firing clears it.
type PendingConditionalCommand struct {
	Kind     ConditionalKind
	Altitude float32 // feet MSL
	Action   ConditionalAction
}

// ConditionalHeading is a deferred heading assignment. Exactly one of
// Heading or ByDegrees is nonzero:
//   - Heading != 0  → fly (or turn to) the absolute heading.
//   - ByDegrees != 0 → turn N degrees from present heading in the given
//     direction (Turn must be TurnLeft or TurnRight).
type ConditionalHeading struct {
	Heading   int              // 1..360, 0 if unused
	Turn      av.TurnDirection // TurnClosest, TurnLeft, TurnRight
	ByDegrees int              // nonzero for LnnD / RnnD
}

func (c ConditionalHeading) Execute(nav *Nav, simTime Time, temp av.Temperature) {
	if c.ByDegrees != 0 {
		switch c.Turn {
		case av.TurnLeft:
			nav.assignHeading(
				vmath.OffsetHeading(nav.FlightState.Heading, float32(-c.ByDegrees)),
				av.TurnLeft, simTime, 0)
		case av.TurnRight:
			nav.assignHeading(
				vmath.OffsetHeading(nav.FlightState.Heading, float32(c.ByDegrees)),
				av.TurnRight, simTime, 0)
		}
		return
	}
	nav.assignHeading(vmath.MagneticHeading(c.Heading), c.Turn, simTime, 0)
}

func (c ConditionalHeading) Render(rt *av.RadioTransmission, r *rand.Rand) {
	if c.ByDegrees != 0 {
		switch c.Turn {
		case av.TurnLeft:
			rt.Add("[left|turn left] {num} degrees", c.ByDegrees)
		case av.TurnRight:
			rt.Add("[right|turn right] {num} degrees", c.ByDegrees)
		}
		return
	}
	switch c.Turn {
	case av.TurnLeft:
		rt.Add("[left heading|turn left heading] {hdg}", c.Heading)
	case av.TurnRight:
		rt.Add("[right heading|turn right heading] {hdg}", c.Heading)
	default:
		rt.Add("[fly heading|heading] {hdg}", c.Heading)
	}
}

// ConditionalDirectFix is a deferred direct-to-fix instruction.
type ConditionalDirectFix struct {
	Fix  string
	Turn av.TurnDirection // TurnClosest, TurnLeft, TurnRight
}

func (c ConditionalDirectFix) Execute(nav *Nav, simTime Time, temp av.Temperature) {
	// Silent fire path — discard the intent because conditional actions
	// don't produce a readback when they fire.
	_ = nav.DirectFix(c.Fix, c.Turn, simTime, 0)
}

func (c ConditionalDirectFix) Render(rt *av.RadioTransmission, r *rand.Rand) {
	switch c.Turn {
	case av.TurnLeft:
		rt.Add("[left direct|turn left direct] {fix}", c.Fix)
	case av.TurnRight:
		rt.Add("[right direct|turn right direct] {fix}", c.Fix)
	default:
		rt.Add("[direct|proceed direct] {fix}", c.Fix)
	}
}

// ConditionalSpeed is a deferred speed assignment.
type ConditionalSpeed struct {
	Restriction av.SpeedRestriction
}

func (c ConditionalSpeed) Execute(nav *Nav, simTime Time, temp av.Temperature) {
	sr := c.Restriction
	_ = nav.AssignSpeed(&sr, false)
}

func (c ConditionalSpeed) Render(rt *av.RadioTransmission, r *rand.Rand) {
	if spd, ok := c.Restriction.ExactValue(); ok {
		rt.Add("[reduce speed to|maintain|slowing to] {spd}", int(spd))
	}
}

// ConditionalMach is a deferred mach-speed assignment.
type ConditionalMach struct {
	Mach float32
}

func (c ConditionalMach) Execute(nav *Nav, simTime Time, temp av.Temperature) {
	_ = nav.AssignMach(c.Mach, false, temp)
}

func (c ConditionalMach) Render(rt *av.RadioTransmission, r *rand.Rand) {
	rt.Add("[mach|maintain mach] {mach}", c.Mach)
}

// ConditionalTriggered reports whether the pending conditional command
// should fire given the aircraft's current vertical state.
//
//	ConditionalLeaving: fires when altitude is >50 ft past trigger in the
//	                    direction of current vertical motion.
//	ConditionalReaching: fires when altitude is within 100 ft of trigger.
func ConditionalTriggered(nav *Nav, pc *PendingConditionalCommand) bool {
	alt := nav.FlightState.Altitude
	diff := alt - pc.Altitude
	switch pc.Kind {
	case ConditionalLeaving:
		const leavingTol = 50.0
		if vmath.Abs(diff) <= leavingTol {
			return false
		}
		rate := nav.FlightState.AltitudeRate
		// Same-sign check: diff>0 (above trigger) requires rate>0 (climbing),
		// diff<0 (below) requires rate<0 (descending). Zero rate with altitude
		// drift outside tolerance (unusual but possible) is not a trigger.
		return (diff > 0 && rate > 0) || (diff < 0 && rate < 0)
	case ConditionalReaching:
		const reachingTol = 100.0
		return vmath.Abs(diff) <= reachingTol
	}
	return false
}
