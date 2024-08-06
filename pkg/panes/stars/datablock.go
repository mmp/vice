// pkg/panes/stars/datablock.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

type DatablockType int

const (
	PartialDatablock = iota
	LimitedDatablock
	FullDatablock
)

type STARSDatablockFieldColors struct {
	Start, End int
	Color      renderer.RGB
}

type STARSDatablockLine struct {
	Text   string
	Colors []STARSDatablockFieldColors
}

func (s *STARSDatablockLine) RightJustify(n int) {
	if n > len(s.Text) {
		delta := n - len(s.Text)
		s.Text = fmt.Sprintf("%*c", delta, ' ') + s.Text
		// Keep the formatting aligned.
		for i := range s.Colors {
			s.Colors[i].Start += delta
			s.Colors[i].End += delta
		}
	}
}

type STARSDatablock struct {
	Lines [4]STARSDatablockLine
}

func (s *STARSDatablock) RightJustify(n int) {
	for i := range s.Lines {
		s.Lines[i].RightJustify(n)
	}
}

func (s *STARSDatablock) Duplicate() STARSDatablock {
	var sd STARSDatablock
	for i := range s.Lines {
		sd.Lines[i].Text = s.Lines[i].Text
		sd.Lines[i].Colors = util.DuplicateSlice(s.Lines[i].Colors)
	}
	return sd
}

func (s *STARSDatablock) BoundText(font *renderer.Font) (int, int) {
	text := ""
	for i, l := range s.Lines {
		text += l.Text
		if i+1 < len(s.Lines) {
			text += "\n"
		}
	}
	return font.BoundText(text, 0)
}

func (s *STARSDatablock) DrawText(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, baseColor renderer.RGB,
	brightness STARSBrightness) {
	style := renderer.TextStyle{
		Font:        font,
		Color:       brightness.ScaleRGB(baseColor),
		LineSpacing: 0}

	for _, line := range s.Lines {
		haveFormatting := len(line.Colors) > 0
		if haveFormatting {
			p0 := pt // save starting point

			// Gather spans of characters that have the same color
			spanColor := baseColor
			start, end := 0, 0

			flush := func(newColor renderer.RGB) {
				if end > start {
					style := renderer.TextStyle{
						Font:        font,
						Color:       brightness.ScaleRGB(spanColor),
						LineSpacing: 0}
					pt = td.AddText(line.Text[start:end], pt, style)
					start = end
				}
				spanColor = newColor
			}

			for ; end < len(line.Text); end++ {
				if line.Text[end] == ' ' {
					// let spaces ride regardless of style
					continue
				}
				// Does this character have a new color?
				chColor := baseColor
				for _, format := range line.Colors {
					if end >= format.Start && end < format.End {
						chColor = format.Color
						break
					}
				}
				if !spanColor.Equals(chColor) {
					flush(chColor)
				}
			}
			flush(spanColor)

			// newline from start so we maintain aligned columns.
			pt = td.AddText("\n", p0, style)
		} else {
			pt = td.AddText(line.Text+"\n", pt, style)
		}
	}
}

func (sp *STARSPane) datablockType(ctx *panes.Context, ac *av.Aircraft) DatablockType {
	trk := sp.getTrack(ctx, ac)

	if trk == nil || trk.TrackOwner == "" {
		// Must be limited, regardless of anything else.
		return LimitedDatablock
	} else {
		// The track owner is known, so it will be a P/FDB
		state := sp.Aircraft[ac.Callsign]
		dt := state.DatablockType

		beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(platform.KeyF1)
		if beaconator {
			// Partial is always full with the beaconator, so we're done
			// here in that case.
			return FullDatablock
		}

		// TODO: when do we do a partial vs limited datablock?
		if ac.Squawk != trk.FlightPlan.AssignedSquawk {
			dt = PartialDatablock
		}

		if trk.TrackOwner == ctx.ControlClient.Callsign {
			// it's under our control
			dt = FullDatablock
		}

		if ac.HandoffTrackController == ctx.ControlClient.Callsign && ac.RedirectedHandoff.RedirectedTo == "" {
			// it's being handed off to us
			dt = FullDatablock
		}

		if sp.haveActiveWarnings(ctx, ac) {
			dt = FullDatablock
		}

		// Point outs are FDB until acked.
		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok {
			dt = FullDatablock
		}
		if state.PointedOut {
			dt = FullDatablock
		}
		if state.ForceQL {
			dt = FullDatablock
		}
		if len(trk.RedirectedHandoff.Redirector) > 0 {
			if trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
				dt = FullDatablock
			}
		}

		if trk.RedirectedHandoff.OriginalOwner == ctx.ControlClient.Callsign {
			dt = FullDatablock
		}

		// Quicklook
		ps := sp.CurrentPreferenceSet
		if ps.QuickLookAll {
			dt = FullDatablock
		} else if slices.ContainsFunc(ps.QuickLookPositions,
			func(q QuickLookPosition) bool { return q.Callsign == trk.TrackOwner }) {
			dt = FullDatablock
		}

		return dt
	}
}

func (sp *STARSPane) getDatablocks(ctx *panes.Context, ac *av.Aircraft) []STARSDatablock {
	now := ctx.ControlClient.CurrentTime()
	state := sp.Aircraft[ac.Callsign]
	if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
		return nil
	}

	dbs := sp.formatDatablocks(ctx, ac)

	// For Southern or Westerly directions the datablock text should be
	// right justified, since the leader line will be connecting on that
	// side.
	dir := sp.getLeaderLineDirection(ac, ctx)
	rightJustify := dir >= math.South
	if rightJustify {
		maxLen := 0
		for _, db := range dbs {
			for _, line := range db.Lines {
				maxLen = math.Max(maxLen, len(line.Text))
			}
		}
		for i := range dbs {
			dbs[i].RightJustify(maxLen)
		}
	}

	return dbs
}

func (sp *STARSPane) getDatablockOffset(ctx *panes.Context, textBounds [2]float32, leaderDir math.CardinalOrdinalDirection) [2]float32 {
	// To place the datablock, start with the vector for the leader line.
	drawOffset := sp.getLeaderLineVector(ctx, leaderDir)

	// And now fine-tune so that the leader line connects with the midpoint
	// of the line that includes the callsign.
	lineHeight := textBounds[1] / 4
	switch leaderDir {
	case math.North, math.NorthEast, math.East, math.SouthEast:
		drawOffset = math.Add2f(drawOffset, [2]float32{2, lineHeight * 3 / 2})
	case math.South, math.SouthWest, math.West, math.NorthWest:
		drawOffset = math.Add2f(drawOffset, [2]float32{-2 - textBounds[0], lineHeight * 3 / 2})
	}

	return drawOffset
}

func (sp *STARSPane) formatDatablocks(ctx *panes.Context, ac *av.Aircraft) []STARSDatablock {
	if ac.Mode == av.Standby {
		return nil
	}

	state := sp.Aircraft[ac.Callsign]

	warnings := sp.getWarnings(ctx, ac)

	// baseDB is what stays the same for all datablock variants
	baseDB := STARSDatablock{}
	baseDB.Lines[0].Text = strings.Join(warnings, "/") // want e.g., EM/LA if multiple things going on
	if len(warnings) > 0 {
		baseDB.Lines[0].Colors = append(baseDB.Lines[0].Colors,
			STARSDatablockFieldColors{
				Start: 0,
				End:   len(baseDB.Lines[0].Text),
				Color: STARSTextAlertColor,
			})
	}

	ty := sp.datablockType(ctx, ac)
	beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(platform.KeyF1)

	switch ty {
	case LimitedDatablock:
		db := baseDB.Duplicate()
		db.Lines[1].Text = util.Select(beaconator, ac.Callsign, ac.Squawk.String()) // TODO(mtrokel): confirm
		db.Lines[2].Text = fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if state.FullLDBEndTime.After(ctx.Now) {
			db.Lines[2].Text += fmt.Sprintf(" %02d", (state.TrackGroundspeed()+5)/10)
		}

		if state.Ident(ctx.Now) {
			// flash ID after squawk code
			start := len(db.Lines[1].Text)
			db.Lines[1].Text += "ID"

			// The text is the same but the "ID" is much dimmer for the flash.
			db2 := db.Duplicate()
			color, _ := sp.datablockColor(ctx, ac)
			db2.Lines[1].Colors = append(db2.Lines[1].Colors,
				STARSDatablockFieldColors{Start: start, End: start + 2, Color: color.Scale(0.3)})
			return []STARSDatablock{db, db2}
		} else {
			return []STARSDatablock{db}
		}

	case PartialDatablock:
		dbs := []STARSDatablock{baseDB.Duplicate(), baseDB.Duplicate()}
		trk := sp.getTrack(ctx, ac)

		if ac.Squawk != trk.FlightPlan.AssignedSquawk && ac.Squawk != 0o1200 {
			sq := ac.Squawk.String()
			if len(baseDB.Lines[0].Text) > 0 {
				dbs[0].Lines[0].Text += " "
				dbs[1].Lines[0].Text += " "
			}
			dbs[0].Lines[0].Text += sq
			dbs[1].Lines[0].Text += sq + "WHO"
		}

		if state.Ident(ctx.Now) {
			alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
			dbs[0].Lines[1].Text = alt + " ID"
			dbs[1].Lines[1].Text = alt + " ID"

			color, _ := sp.datablockColor(ctx, ac)
			dbs[1].Lines[1].Colors = append(dbs[1].Lines[1].Colors,
				STARSDatablockFieldColors{Start: 4, End: 6, Color: color.Scale(0.3)})

			return dbs
		}

		if fp := trk.FlightPlan; fp != nil && fp.Rules == av.VFR {
			as := fmt.Sprintf("%03d  %02d", (state.TrackAltitude()+50)/100, (state.TrackGroundspeed()+5)/10)
			dbs[0].Lines[1].Text = as
			dbs[1].Lines[1].Text = as
			return dbs
		}

		field2 := " "
		if trk.HandoffController != "" {
			if ctrl := ctx.ControlClient.Controllers[trk.HandoffController]; ctrl != nil {
				if trk.RedirectedHandoff.RedirectedTo != "" {
					if toctrl := ctx.ControlClient.Controllers[trk.RedirectedHandoff.RedirectedTo]; toctrl != nil {
						field2 = toctrl.SectorId[len(ctrl.SectorId)-1:]
					}
				} else {
					if ctrl.ERAMFacility { // Same facility
						field2 = "C"
					} else if ctrl.FacilityIdentifier == "" { // Enroute handoff
						field2 = ctrl.SectorId[len(ctrl.SectorId)-1:]
					} else { // Different facility
						field2 = ctrl.FacilityIdentifier
					}

				}
			}
		}

		field3 := ""
		if trk.FlightPlan.Rules == av.VFR {
			field3 += "V"
		} else if sp.isOverflight(ctx, trk) {
			field3 += "E"
		}
		field3 += state.CWTCategory

		// Field 1: alternate between altitude and either primary
		// scratchpad or destination airport.
		ap := trk.FlightPlan.ArrivalAirport
		if len(ap) == 4 {
			ap = ap[1:] // drop the leading K
		}
		alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		sp := fmt.Sprintf("%3s", trk.SP1)

		field1 := [2]string{}
		field1[0] = alt
		if trk.SP1 != "" {
			field1[1] = sp
		} else if airport := ctx.ControlClient.Airports[trk.FlightPlan.ArrivalAirport]; airport != nil && !airport.OmitArrivalScratchpad {
			field1[1] = ap
		} else {
			field1[1] = alt
		}

		dbs[0].Lines[1].Text = field1[0] + field2 + field3
		dbs[1].Lines[1].Text = field1[1] + field2 + field3

		return dbs

	case FullDatablock:
		trk := sp.getTrack(ctx, ac)

		// Line 1: fields 1, 2, and 8 (surprisingly). Field 8 may be multiplexed.
		field1 := util.Select(beaconator, ac.Squawk.String(), ac.Callsign)

		field2 := ""
		if state.InhibitMSAW || state.DisableMSAW {
			if state.DisableCAWarnings {
				field2 = "+"
			} else {
				field2 = "*"
			}
		} else if state.DisableCAWarnings {
			field2 = STARSTriangleCharacter
		}

		field8 := []string{""}
		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok || state.PointedOut {
			field8 = []string{" PO"}
		} else if id, ok := sp.OutboundPointOuts[ac.Callsign]; ok {
			field8 = []string{" PO" + id}
		} else if ctx.Now.Before(state.UNFlashingEndTime) {
			field8 = []string{"", " UN"}
		} else if state.POFlashingEndTime.After(ctx.Now) {
			field8 = []string{"", " PO"}
		} else if ac.RedirectedHandoff.ShowRDIndicator(ctx.ControlClient.Callsign, state.RDIndicatorEnd) {
			field8 = []string{" RD"}
		}

		// Line 2: fields 3, 4, 5
		alt := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if state.LostTrack(ctx.ControlClient.SimTime) {
			alt = "CST"
		}
		// Build up field3 and field4 in tandem because 4 gets a "+" if 3
		// is displaying the secondary scratchpad.  Leave the empty string
		// as a placeholder in field 4 otherwise.
		field3 := []string{alt}
		field4 := []string{""}
		if !state.Ident(ctx.Now) {
			// Don't display these if they're identing: then it's just altitude and speed + "ID"
			if ac.Scratchpad != "" {
				field3 = append(field3, ac.Scratchpad)
				field4 = append(field4, "")
			}
			if ac.SecondaryScratchpad != "" {
				field3 = append(field3, ac.SecondaryScratchpad)
				field4 = append(field4, "+") // 2-67, "Field 4 Contents"
			}
			if len(field3) == 1 {
				if ap := ctx.ControlClient.Airports[trk.FlightPlan.ArrivalAirport]; ap != nil && !ap.OmitArrivalScratchpad {
					ap := ac.FlightPlan.ArrivalAirport
					if len(ap) == 4 {
						ap = ap[1:] // drop the leading K
					}
					field3 = append(field3, ap)
					field4 = append(field4, "")
				}
			}
		}

		// Fill in empty field4 entries.
		for i := range field4 {
			if field4[i] == "" && trk.HandoffController != "" {
				if ctrl := ctx.ControlClient.Controllers[trk.HandoffController]; ctrl != nil {
					if trk.RedirectedHandoff.RedirectedTo != "" {
						if toctrl := ctx.ControlClient.Controllers[trk.RedirectedHandoff.RedirectedTo]; toctrl != nil {
							field4 = append(field4, toctrl.SectorId[len(ctrl.SectorId)-1:])
						}
					} else {
						if ctrl.ERAMFacility { // Same facility
							field4 = append(field4, "C")
						} else if ctrl.FacilityIdentifier == "" { // Enroute handoff
							field4 = append(field4, ctrl.SectorId[len(ctrl.SectorId)-1:])
						} else { // Different facility
							field4 = append(field4, ctrl.FacilityIdentifier)
						}
					}
				}
			}
			for len(field4[i]) < 2 {
				field4[i] += " "
			}
		}

		speed := fmt.Sprintf("%02d", (state.TrackGroundspeed()+5)/10)
		if state.IFFlashing {
			speed = "IF"
		}

		field5 := []string{} // alternate speed and aircraft type
		var line5FieldColors *STARSDatablockFieldColors
		color, _ := sp.datablockColor(ctx, ac)
		if state.Ident(ctx.Now) {
			// Speed is followed by ID when identing (2-67, field 5)
			field5 = append(field5, speed+"ID")
			field5 = append(field5, speed+"ID")

			if speed == "IF" {
				line5FieldColors = &STARSDatablockFieldColors{
					Start: len(speed) - 3,
					End:   len(speed) + 3,
					Color: color.Scale(0.3),
				}
			} else {
				line5FieldColors = &STARSDatablockFieldColors{
					Start: len(speed) + 1,
					End:   len(speed) + 3,
					Color: color.Scale(0.3),
				}
			}
		} else {
			if speed == "IF" {
				line5FieldColors = &STARSDatablockFieldColors{
					Start: len(speed) - 1,
					End:   len(speed) + 1,
					Color: color.Scale(0.3),
				}
			}

			acCategory := ""
			actype := ac.FlightPlan.TypeWithoutSuffix()
			if strings.Index(actype, "/") == 1 {
				actype = actype[2:]
			}
			modifier := ""
			if ac.FlightPlan.Rules == av.VFR {
				modifier += "V"
			} else if sp.isOverflight(ctx, trk) {
				modifier += "E"
			} else {
				modifier = " "
			}
			acCategory = modifier + state.CWTCategory

			field5 = append(field5, speed+acCategory)

			field5 = append(field5, actype)
			if (state.DisplayRequestedAltitude != nil && *state.DisplayRequestedAltitude) ||
				(state.DisplayRequestedAltitude == nil && sp.CurrentPreferenceSet.DisplayRequestedAltitude) {
				field5 = append(field5, fmt.Sprintf("R%03d", ac.FlightPlan.Altitude/100))
			}
		}
		for i := range field5 {
			if len(field5[i]) < 5 {
				field5[i] = fmt.Sprintf("%-5s", field5[i])
			}
		}

		field6 := []string{}
		var line3FieldColors *STARSDatablockFieldColors
		if state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert {
			field6 = append(field6, "*TPA")
		} else if state.IntrailDistance != 0 && sp.CurrentPreferenceSet.DisplayATPAInTrailDist {
			field6 = append(field6, fmt.Sprintf("%.2f", state.IntrailDistance))

			if state.ATPAStatus == ATPAStatusWarning {
				line3FieldColors = &STARSDatablockFieldColors{
					Start: 0,
					End:   len(field6),
					Color: STARSATPAWarningColor,
				}
			} else if state.ATPAStatus == ATPAStatusAlert {
				line3FieldColors = &STARSDatablockFieldColors{
					Start: 0,
					End:   len(field6),
					Color: STARSATPAAlertColor,
				}
			}
		}

		field7 := []string{}
		if ac.TempAltitude != 0 {
			ta := (ac.TempAltitude + 50) / 100
			field7 = append(field7, fmt.Sprintf("A%03d", ta))
		}
		if sq := trk.FlightPlan.AssignedSquawk; sq != ac.Squawk {
			if isSPC, _ := av.SquawkIsSPC(ac.Squawk); !isSPC {
				field7 = append(field7, sq.String())
				field6 = append(field6, ac.Squawk.String()+"  ")
				color, _ := sp.datablockColor(ctx, ac)
				idx := len(field6) - 1
				line3FieldColors = &STARSDatablockFieldColors{
					Start: len(field6[idx]) + 1,
					End:   len(field6[idx]) + 5,
					Color: color.Scale(0.3),
				}
			}
		}
		if len(field7) == 0 {
			field7 = append(field7, "")
		}
		if len(field6) == 0 {
			field6 = append(field6, "")
		}
		for i := range field6 {
			for len(field6[i]) < 5 {
				field6[i] += " "
			}
		}

		// Now make some datablocks. Note that line 1 has already been set
		// in baseDB above.
		//
		// A number of the fields may be multiplexed; the total number of
		// unique datablock variations is the least common multiple of all
		// of their lengths.  and 8 may be time multiplexed, which
		// simplifies db creation here.
		dbs := []STARSDatablock{}
		n := math.LCM(math.LCM(math.LCM(len(field3), len(field4)), math.LCM(len(field5), len(field8))), math.LCM(len(field6), len(field7)))
		for i := 0; i < n; i++ {
			db := baseDB.Duplicate()
			db.Lines[1].Text = field1 + field2 + field8[i%len(field8)]
			db.Lines[2].Text = field3[i%len(field3)] + field4[i%len(field4)] + field5[i%len(field5)]
			db.Lines[3].Text = field6[i%len(field6)] + "  " + field7[i%len(field7)]
			if line3FieldColors != nil && i&1 == 1 {
				// Flash the correct squawk
				fc := *line3FieldColors
				fc.Start += len(field6[i%len(field6)]) - 7
				fc.End += len(field6[i%len(field6)]) - 7
				db.Lines[3].Colors = append(db.Lines[3].Colors, fc)
			}
			if line5FieldColors != nil && i&1 == 0 {
				// Flash "ID" for identing
				fc := *line5FieldColors
				fc.Start += len(field3[i%len(field3)]) + len(field4)
				fc.End += len(field3[i%len(field3)]) + len(field4)
				db.Lines[2].Colors = append(db.Lines[2].Colors, fc)
			}
			dbs = append(dbs, db)
		}
		return dbs
	}

	return nil
}

func (sp *STARSPane) datablockColor(ctx *panes.Context, ac *av.Aircraft) (color renderer.RGB, brightness STARSBrightness) {
	ps := sp.CurrentPreferenceSet
	dt := sp.datablockType(ctx, ac)
	state := sp.Aircraft[ac.Callsign]
	brightness = util.Select(dt == PartialDatablock || dt == LimitedDatablock,
		ps.Brightness.LimitedDatablocks, ps.Brightness.FullDatablocks)

	if ac.Callsign == sp.dwellAircraft {
		brightness = STARSBrightness(100)
	}

	trk := sp.getTrack(ctx, ac)
	if trk == nil {
		return STARSUntrackedAircraftColor, brightness
	}

	for _, controller := range trk.RedirectedHandoff.Redirector {
		if controller == ctx.ControlClient.Callsign && trk.RedirectedHandoff.RedirectedTo != ctx.ControlClient.Callsign {
			color = STARSUntrackedAircraftColor
		}
	}

	// Handle cases where it should flash
	if ctx.Now.Second()&1 == 0 { // one second cycle
		if _, pointOut := sp.InboundPointOuts[ac.Callsign]; pointOut {
			// point out
			brightness /= 3
		} else if state.OutboundHandoffAccepted && ctx.Now.Before(state.OutboundHandoffFlashEnd) {
			// we handed it off, it was accepted, but we haven't yet acknowledged
			brightness /= 3
		} else if (trk.HandoffController == ctx.ControlClient.Callsign && // handing off to us
			!slices.Contains(trk.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign)) || // not a redirector
			trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign { // redirected to
			brightness /= 3
		}
	}

	// Check if were the controller being ForceQL
	if slices.Contains(sp.ForceQLAircraft, ac.Callsign) {
		color = STARSInboundPointOutColor
	}

	if trk.TrackOwner == "" {
		color = STARSUntrackedAircraftColor
	} else {
		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok || state.PointedOut || state.ForceQL {
			// yellow for pointed out by someone else or uncleared after acknowledged.
			color = STARSInboundPointOutColor
		} else if state.IsSelected {
			// middle button selected
			color = STARSSelectedAircraftColor
		} else if trk.TrackOwner == ctx.ControlClient.Callsign { //change
			// we own the track track
			color = STARSTrackedAircraftColor
		} else if trk.RedirectedHandoff.OriginalOwner == ctx.ControlClient.Callsign || trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
			color = STARSTrackedAircraftColor
		} else if trk.HandoffController == ctx.ControlClient.Callsign &&
			!slices.Contains(trk.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign) {
			// flashing white if it's being handed off to us.
			color = STARSTrackedAircraftColor
		} else if state.OutboundHandoffAccepted {
			// we handed it off, it was accepted, but we haven't yet acknowledged
			color = STARSTrackedAircraftColor
		} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
			// quick look all plus
			color = STARSTrackedAircraftColor
		} else if slices.ContainsFunc(ps.QuickLookPositions,
			func(q QuickLookPosition) bool { return q.Callsign == trk.TrackOwner && q.Plus }) {
			// individual quicklook plus controller
			color = STARSTrackedAircraftColor
		} else if trk.AutoAssociateFP {
			color = STARSTrackedAircraftColor
		} else {
			color = STARSUntrackedAircraftColor
		}
	}

	return
}

func (sp *STARSPane) datablockVisible(ac *av.Aircraft, ctx *panes.Context) bool {
	trk := sp.getTrack(ctx, ac)

	af := sp.CurrentPreferenceSet.AltitudeFilters
	alt := sp.Aircraft[ac.Callsign].TrackAltitude()
	if trk != nil && trk.TrackOwner == ctx.ControlClient.Callsign {
		// For owned datablocks
		return true
	} else if trk != nil && trk.HandoffController == ctx.ControlClient.Callsign {
		// For receiving handoffs
		return true
	} else if ac.ControllingController == ctx.ControlClient.Callsign {
		// For non-greened handoffs
		return true
	} else if sp.Aircraft[ac.Callsign].PointedOut {
		// Pointouts: This is if its been accepted,
		// for an incoming pointout, it falls to the FDB check
		return true
	} else if ok, _ := av.SquawkIsSPC(ac.Squawk); ok {
		// Special purpose codes
		return true
	} else if sp.Aircraft[ac.Callsign].DatablockType == FullDatablock {
		// If FDB, may trump others but idc
		// This *should* be primarily doing CA and ATPA cones
		return true
	} else if sp.isOverflight(ctx, trk) && sp.CurrentPreferenceSet.OverflightFullDatablocks { //Need a f7 + e
		// Overflights
		return true
	} else if sp.CurrentPreferenceSet.QuickLookAll {
		// Quick look all
		return true
	} else if trk != nil && trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
		// Redirected to
		return true
	} else if trk != nil && slices.Contains(trk.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign) {
		// Had it but redirected it
		return true
	}

	// Quick Look Positions.
	for _, quickLookPositions := range sp.CurrentPreferenceSet.QuickLookPositions {
		if trk != nil && trk.TrackOwner == quickLookPositions.Callsign {
			return true
		}
	}

	if trk == nil || trk.TrackOwner != "" {
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else {
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) drawDatablocks(aircraft []*av.Aircraft, ctx *panes.Context,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	now := ctx.ControlClient.SimTime
	realNow := ctx.Now // for flashing rate...
	ps := sp.CurrentPreferenceSet
	font := sp.systemFont[ps.CharSize.Datablocks]

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		dbs := sp.getDatablocks(ctx, ac)
		if len(dbs) == 0 {
			continue
		}

		color, brightness := sp.datablockColor(ctx, ac)
		if brightness == 0 {
			continue
		}

		// Compute the bounds of the datablock; always use the first one so
		// things don't jump around when it switches between multiple of
		// them.
		w, h := dbs[0].BoundText(font)
		datablockOffset := sp.getDatablockOffset(ctx, [2]float32{float32(w), float32(h)},
			sp.getLeaderLineDirection(ac, ctx))

		// Draw characters starting at the upper left.
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		pt := math.Add2f(datablockOffset, pac)
		idx := (realNow.Second() / 2) % len(dbs) // 2 second cycle
		dbs[idx].DrawText(td, pt, font, color, brightness)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) haveActiveWarnings(ctx *panes.Context, ac *av.Aircraft) bool {
	ps := sp.CurrentPreferenceSet
	state := sp.Aircraft[ac.Callsign]

	if state.MSAW && !state.InhibitMSAW && !state.DisableMSAW && !ps.DisableMSAW {
		return true
	}
	if ok, _ := av.SquawkIsSPC(ac.Squawk); ok {
		return true
	}
	if ac.SPCOverride != "" {
		return true
	}
	if !ps.DisableCAWarnings && !state.DisableCAWarnings &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
			}) {
		return true
	}
	if _, outside := sp.WarnOutsideAirspace(ctx, ac); outside {
		return true
	}

	return false
}

func (sp *STARSPane) getWarnings(ctx *panes.Context, ac *av.Aircraft) []string {
	var warnings []string
	addWarning := func(w string) {
		if !slices.Contains(warnings, w) {
			warnings = append(warnings, w)
		}
	}

	ps := sp.CurrentPreferenceSet
	state := sp.Aircraft[ac.Callsign]

	if state.MSAW && !state.InhibitMSAW && !state.DisableMSAW && !ps.DisableMSAW {
		addWarning("LA")
	}
	if ok, code := av.SquawkIsSPC(ac.Squawk); ok {
		addWarning(code)
	}
	if ac.SPCOverride != "" {
		addWarning(ac.SPCOverride)
	}
	if !ps.DisableCAWarnings && !state.DisableCAWarnings &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
			}) {
		addWarning("CA")
	}
	if alts, outside := sp.WarnOutsideAirspace(ctx, ac); outside {
		altStrs := ""
		for _, a := range alts {
			altStrs += fmt.Sprintf("/%d-%d", a[0]/100, a[1]/100)
		}
		addWarning("AS" + altStrs)
	}

	if len(warnings) > 1 {
		slices.Sort(warnings)
	}

	return warnings
}
