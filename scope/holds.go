// scope/holds.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
)

// skipProcedureTurnHolds keeps charted holds from being drawn at fixes
// whose procedure turn the route has just drawn: a hold in lieu of a
// procedure turn is in the database as both.
func SkipProcedureTurnHolds(wps av.WaypointArray, drawnHolds map[string]any) {
	for _, wp := range wps {
		if wp.ProcedureTurn() != nil {
			drawnHolds[wp.Fix] = nil
		}
	}
}

func DrawEnrouteHolds(nmPerLongitude, magneticVariation float32, transforms Transformations, wps av.WaypointArray, procedure string,
	color renderer.RGB, ld *renderer.ColoredLinesDrawBuilder, td *renderer.TextDrawBuilder, style renderer.TextStyle,
	drawn *DrawnRoutes, drawnHolds map[string]any) {
	for _, wp := range wps {
		if holds, ok := db.DB.EnrouteHolds[wp.Fix]; ok {
			for _, h := range holds {
				// Draw if: procedure matches OR procedure is empty (HPF hold at this waypoint)
				if h.Procedure == procedure || h.Procedure == "" {
					DrawHoldPattern(nmPerLongitude, magneticVariation, transforms, h, color, td, ld, style, drawn, drawnHolds)
				}
			}
		}
	}
}

// drawHoldPattern draws a charted hold, unless one at its fix has been
// drawn already: the database often has several records for one fix that
// differ only in their altitudes.
func DrawHoldPattern(nmPerLongitude, magneticVariation float32, transforms Transformations,
	hold av.Hold, color renderer.RGB, td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	style renderer.TextStyle, drawn *DrawnRoutes, drawnHolds map[string]any) {
	if _, ok := drawnHolds[hold.Fix]; ok {
		return
	}
	drawnHolds[hold.Fix] = nil

	fixLoc, _ := db.DB.LookupWaypoint(hold.Fix)

	// Default leg length/time if not specified
	legLength := hold.LegLengthNM
	if legLength == 0 && hold.LegMinutes > 0 {
		// Approximate: assume 120 knots = 2 nm/minute
		legLength = hold.LegMinutes * 2
	}
	if legLength == 0 {
		legLength = 4 // Default 4nm legs
	}

	// Convert fix location to nm coordinates
	fixNM := math.LL2NM(fixLoc, nmPerLongitude)

	// Inbound course (magnetic to true)
	inboundMag := hold.InboundCourse
	inboundTrue := math.MagneticToTrue(inboundMag, magneticVariation)
	inboundRad := math.Radians(inboundTrue)

	// Outbound is 180° from inbound
	outboundRad := inboundRad + math.Pi

	// Inbound vector (pointing toward fix)
	inboundVec := math.SinCos(inboundRad)

	// Outbound vector (pointing away from fix)
	outboundVec := [2]float32{-inboundVec[0], -inboundVec[1]}

	// Use 1nm turn radius to match the racetrack drawing in DrawWaypoints.
	turnRadius := float32(1)

	// Perpendicular vector for turn offset (depends on turn direction)
	var perpVec [2]float32
	if hold.TurnDirection == av.TurnRight {
		perpVec = [2]float32{inboundVec[1], -inboundVec[0]} // 90° right of inbound
	} else {
		perpVec = [2]float32{-inboundVec[1], inboundVec[0]} // 90° left of inbound
	}

	// The hold pattern geometry:
	// After a 180° turn, aircraft is displaced by 2*turnRadius perpendicular to course

	// Turn 1 at fix: from inbound to outbound
	turnCenter1 := math.Add2f(fixNM, math.Scale2f(perpVec, turnRadius))

	// After the first turn, aircraft is displaced by 2*turnRadius from the inbound centerline
	outboundStart := math.Add2f(fixNM, math.Scale2f(perpVec, 2*turnRadius))
	outboundEnd := math.Add2f(outboundStart, math.Scale2f(outboundVec, legLength))

	// Turn 2 at end of outbound: from outbound to inbound
	// For outbound leg, perpendicular is in opposite direction
	var perpVec2 [2]float32
	if hold.TurnDirection == av.TurnRight {
		perpVec2 = [2]float32{outboundVec[1], -outboundVec[0]} // 90° right of outbound
	} else {
		perpVec2 = [2]float32{-outboundVec[1], outboundVec[0]} // 90° left of outbound
	}
	turnCenter2 := math.Add2f(outboundEnd, math.Scale2f(perpVec2, turnRadius))

	// After the second turn, we're back on the inbound centerline
	inboundStart := math.Add2f(outboundEnd, math.Scale2f(perpVec2, 2*turnRadius))

	// Draw inbound leg
	p1ll := math.NM2LL(inboundStart, nmPerLongitude)
	ld.AddLine(p1ll, fixLoc, color)

	// Draw first turn (at fix)
	// The arc starts from the fix and sweeps to the outbound leg
	// Angle from turn center to fix (where arc starts)
	turn1StartAngle := inboundRad - math.Pi/2
	if hold.TurnDirection == av.TurnLeft {
		turn1StartAngle = inboundRad + math.Pi/2
	}
	drawHoldTurn(nmPerLongitude, transforms, turnCenter1, turnRadius, turn1StartAngle, hold.TurnDirection, color, ld)

	// Draw outbound leg
	p3ll := math.NM2LL(outboundStart, nmPerLongitude)
	p4ll := math.NM2LL(outboundEnd, nmPerLongitude)
	ld.AddLine(p3ll, p4ll, color)

	// Draw arrow on outbound leg showing direction of flight
	outboundMid := math.Mid2f(outboundStart, outboundEnd)
	aa := outboundRad + math.Radians(float32(180+30))
	pa := math.Add2f(outboundMid, math.Scale2f(math.SinCos(aa), 0.5))
	ld.AddLine(math.NM2LL(outboundMid, nmPerLongitude), math.NM2LL(pa, nmPerLongitude), color)
	ba := outboundRad - math.Radians(float32(180+30))
	pb := math.Add2f(outboundMid, math.Scale2f(math.SinCos(ba), 0.5))
	ld.AddLine(math.NM2LL(outboundMid, nmPerLongitude), math.NM2LL(pb, nmPerLongitude), color)

	// Draw second turn (connecting outbound back to inbound)
	// Angle from turn center to end of outbound leg (where arc starts)
	turn2StartAngle := outboundRad - math.Pi/2
	if hold.TurnDirection == av.TurnLeft {
		turn2StartAngle = outboundRad + math.Pi/2
	}
	drawHoldTurn(nmPerLongitude, transforms, turnCenter2, turnRadius, turn2StartAngle, hold.TurnDirection, color, ld)

	// Label the fix unless a route already has.
	if drawn.ClaimFix(hold.Fix) {
		td.AddText(hold.Fix, transforms.WindowFromLatLongP(fixLoc), style)
	}
}

func drawHoldTurn(nmPerLongitude float32, transforms Transformations, centerNM [2]float32, radius float32,
	startAngle float32, turnDirection av.TurnDirection, color renderer.RGB, ld *renderer.ColoredLinesDrawBuilder) {
	// Draw 180° turn arc with segments
	clockwise := turnDirection == av.TurnRight
	numSegments := 16
	for i := range numSegments {
		t1 := float32(i) / float32(numSegments)
		t2 := float32(i+1) / float32(numSegments)

		// For hold patterns, we always turn 180° (π radians)
		var a1, a2 float32
		if clockwise {
			// Turn right (clockwise): add positive angles
			a1 = startAngle + t1*math.Pi
			a2 = startAngle + t2*math.Pi
		} else {
			// Turn left (counterclockwise): subtract angles
			a1 = startAngle - t1*math.Pi
			a2 = startAngle - t2*math.Pi
		}

		p1nm := [2]float32{
			centerNM[0] + radius*math.Sin(a1),
			centerNM[1] + radius*math.Cos(a1),
		}
		p2nm := [2]float32{
			centerNM[0] + radius*math.Sin(a2),
			centerNM[1] + radius*math.Cos(a2),
		}

		p1ll := math.NM2LL(p1nm, nmPerLongitude)
		p2ll := math.NM2LL(p2nm, nmPerLongitude)
		ld.AddLine(p1ll, p2ll, color)
	}
}
