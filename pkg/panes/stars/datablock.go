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

// datablock is a simple interface that abstracts the various types of
// datablock. The only operation that exposes is drawing the datablock.
type datablock interface {
	// pt is end of leader line--attachment point
	draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, brightness STARSBrightness,
		leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64)
}

// dbChar represents a single character in a datablock.
type dbChar struct {
	ch       rune
	color    renderer.RGB
	flashing bool
}

///////////////////////////////////////////////////////////////////////////
// fullDatablock

type fullDatablock struct {
	// line 0
	field0 [16]dbChar
	// line 1
	field1 [7]dbChar
	field2 [1]dbChar
	field8 [4]dbChar
	// line 2
	field34 [3][5]dbChar // field 3 and 4 together, since they're connected
	field5  [3][7]dbChar
	// line 3
	field6 [2][5]dbChar
	field7 [2][4]dbChar
}

func (db fullDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	// Figure out the maximum number of values any field is cycling through.
	numVariants := func(fields [][]dbChar) int {
		n := 0
		for _, field := range fields {
			if fieldEmpty(field) {
				break
			}
			n++
		}
		return n
	}

	// Find the maximum number of field values that we are cycling through.
	nc := math.Max(numVariants([][]dbChar{db.field34[0][:], db.field34[1][:], db.field34[2][:]}),
		numVariants([][]dbChar{db.field5[0][:], db.field5[1][:], db.field5[2][:]}))
	nc = math.Max(nc, numVariants([][]dbChar{db.field6[0][:], db.field6[1][:]}))
	nc = math.Max(nc, numVariants([][]dbChar{db.field7[0][:], db.field7[1][:]}))

	// Cycle 1 is 2s, others are 1.5s. Then get that in half seconds.
	fullCycleHalfSeconds := 4 + 3*(nc-1)
	// Figure out which cycle we are in
	cycle := 0
	for idx := halfSeconds % int64(fullCycleHalfSeconds); idx > 4; idx -= 3 {
		cycle++
	}

	selectMultiplexed := func(fields [][]dbChar) []dbChar {
		n := numVariants(fields)
		if cycle < n {
			return fields[cycle]
		}
		return fields[0]
	}

	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(dbChopTrailing(db.field1[:]), db.field2[:], db.field8[:]),
		dbMakeLine(dbChopTrailing(selectMultiplexed([][]dbChar{db.field34[0][:], db.field34[1][:], db.field34[2][:]})),
			selectMultiplexed([][]dbChar{db.field5[0][:], db.field5[1][:], db.field5[2][:]})),
		dbMakeLine(selectMultiplexed([][]dbChar{db.field6[0][:], db.field6[1][:]}),
			selectMultiplexed([][]dbChar{db.field7[0][:], db.field7[1][:]})),
	}
	pt[1] += float32(font.Size) // align leader with line 1
	dbDrawLines(lines, td, pt, font, brightness, leaderLineDirection, halfSeconds)
}

///////////////////////////////////////////////////////////////////////////
// partialDatablock

type partialDatablock struct {
	// line 0
	field0 [16]dbChar
	// line 1
	field12 [3][5]dbChar
	field3  [2][4]dbChar
	field4  [2]dbChar
}

func (db partialDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	// How many cycles?
	nc := util.Select(fieldEmpty(db.field3[1][:]), 1, 2)
	// If all three of field12 are set, it's 4 cycles: 0, 1, 0, 2 for field12
	e1, e2 := fieldEmpty(db.field12[1][:]), fieldEmpty(db.field12[2][:])
	switch {
	case e1 && e2:
		// all set
	case (e1 && !e2) || (e2 && !e1):
		nc = 2
	case !e1 && !e2:
		nc = 4
	}

	// Cycle 1 is 2s, others are 1.5s. Then get that in half seconds.
	fullCycleHalfSeconds := 4 + 3*(nc-1)
	// Figure out which cycle we are in
	cycle := 0
	for idx := halfSeconds % int64(fullCycleHalfSeconds); idx > 4; idx -= 3 {
		cycle++
	}

	f12 := db.field12[0][:]
	if !fieldEmpty(db.field12[1][:]) && cycle == 1 {
		f12 = db.field12[1][:]
	}
	if !fieldEmpty(db.field12[2][:]) && cycle == 3 {
		f12 = db.field12[2][:]
	}

	f3 := db.field3[0][:]
	if cycle == 1 && !fieldEmpty(db.field3[1][:]) {
		f3 = db.field3[1][:]
	}

	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(dbChopTrailing(f12), f3, db.field4[:]),
	}
	pt[1] += float32(font.Size) // align leader with line 1
	dbDrawLines(lines, td, pt, font, brightness, leaderLineDirection, halfSeconds)
}

///////////////////////////////////////////////////////////////////////////
// limitedDatablock

type limitedDatablock struct {
	// Line 0
	field0 [8]dbChar
	// Line 1
	field1 [7]dbChar
	field2 [1]dbChar // unused
	// Line 2
	field3 [3]dbChar
	field4 [2]dbChar // unused
	field5 [4]dbChar
	// Line 3 (not in manual, but for beaconator callsign)
	field6 [8]dbChar
}

func (db limitedDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(db.field1[:], db.field2[:]),
		dbMakeLine(db.field3[:], db.field4[:], db.field5[:]),
		dbMakeLine(db.field6[:]),
	}
	pt[1] += 2 * float32(font.Size) // align leader with line 2
	dbDrawLines(lines, td, pt, font, brightness, leaderLineDirection, halfSeconds)
}

///////////////////////////////////////////////////////////////////////////
// ghostDatablock

// both partial and full in the same one
type ghostDatablock struct {
	// line 0
	field0 [8]dbChar
	// line 1
	field1 [3]dbChar
}

func (db ghostDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(db.field1[:]),
	}
	// Leader aligns with line 0, so no offset is needed
	dbDrawLines(lines, td, pt, font, brightness, leaderLineDirection, halfSeconds)
}

///////////////////////////////////////////////////////////////////////////
// dbLine

// dbLine stores the characters in a line of a datablock; it allows drawing
// code to not worry about the details of the individual fields on a line.
type dbLine struct {
	length int
	ch     [16]dbChar // maximum length of a datablock field
}

// dbMakeLine flattens the given datablock fields into a single contiguous
// line of characters.
func dbMakeLine(fields ...[]dbChar) dbLine {
	var l dbLine
	for _, f := range fields {
		for _, ch := range f {
			l.ch[l.length] = ch
			l.length++
		}
	}
	return l
}

// Len returns the number of valid characters in the line (i.e., how many
// will be drawn). Note that it does include spaces but not unset ones.
func (l dbLine) Len() int {
	for i := l.length - 1; i >= 0; i-- {
		if l.ch[i].ch != 0 {
			return i + 1
		}
	}
	return 0
}

///////////////////////////////////////////////////////////////////////////

// dbChopTrailing takes a datablock field and returns a shortened slice
// with trailing unset characters removed.
func dbChopTrailing(f []dbChar) []dbChar {
	for i := len(f) - 1; i >= 0; i-- {
		if f[i].ch != 0 {
			return f[:i+1]
		}
	}
	return nil
}

func dbDrawLines(lines []dbLine, td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	rightJustify := leaderLineDirection >= math.South
	glyph := font.LookupGlyph(' ')
	fontWidth := glyph.AdvanceX

	for _, line := range lines {
		xOffset := float32(4)
		if rightJustify {
			xOffset = -4 - float32(line.Len())*fontWidth
		}
		dbDrawLine(line, td, math.Add2f(pt, [2]float32{xOffset, 0}), font, brightness, halfSeconds)
		// Step down to the next line
		pt[1] -= float32(font.Size)
	}
}

func dbDrawLine(line dbLine, td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
	brightness STARSBrightness, halfSeconds int64) {
	// We will batch characters to be drawn up into str and flush them out
	// in a call to TextDrawBuider AddText() only when the color
	// changes. (This is some effort to minimize the number of AddText()
	// calls.)
	str := ""
	style := renderer.TextStyle{Font: font}

	flush := func() {
		if len(str) > 0 {
			pt = td.AddText(str, pt, style)
			str = ""
		}
	}

	for i := range line.length {
		ch := line.ch[i]
		if ch.ch == 0 {
			// Treat unset as a space
			str += " "
		} else {
			// Flashing text goes on a 0.5 second cycle.
			br := brightness
			if ch.flashing && halfSeconds&1 == 1 {
				br /= 3
			}

			c := br.ScaleRGB(ch.color)
			if !c.Equals(style.Color) {
				flush()
				style.Color = c
			}
			str += string(ch.ch)
		}
	}
	flush()
}

func fieldEmpty(f []dbChar) bool {
	for _, ch := range f {
		if ch.ch != 0 {
			return false
		}
	}
	return true
}

///////////////////////////////////////////////////////////////////////////

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

		if sp.currentPrefs().OverflightFullDatablocks && sp.isOverflight(ctx, trk) {
			dt = FullDatablock
		}

		if sp.isQuicklooked(ctx, ac) {
			dt = FullDatablock
		}

		return dt
	}
}

// Utility function for assembling datablocks: puts the given string into
// the field with associated properties; returns the number of characters
// added.
func formatDBText(field []dbChar, s string, c renderer.RGB, flashing bool) int {
	i := 0
	for _, ch := range s {
		if i == len(field) {
			return i
		}
		field[i] = dbChar{ch: ch, color: c, flashing: flashing}
		i++
	}
	return len(s)
}

func (sp *STARSPane) getDatablock(ctx *panes.Context, ac *av.Aircraft) datablock {
	now := ctx.ControlClient.CurrentTime()
	state := sp.Aircraft[ac.Callsign]
	if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
		return nil
	}

	if ac.Mode == av.Standby {
		return nil
	}

	color, _, _ := sp.trackDatablockColorBrightness(ctx, ac)

	// Alerts are common to all datablock types
	var alerts [16]dbChar
	formatDBText(alerts[:], strings.Join(sp.getWarnings(ctx, ac), "/"), STARSTextAlertColor,
		false /* do these ever flash? */)

	trk := sp.getTrack(ctx, ac)

	// Check if the track is being handed off.
	//
	// handoffTCP is only set if it's coming from an enroute position or
	// from a different facility; otherwise we just show the
	// single-character id.
	handoffId, handoffTCP := " ", ""
	if trk.HandoffController != "" {
		toCallsign := util.Select(trk.RedirectedHandoff.RedirectedTo != "",
			trk.RedirectedHandoff.RedirectedTo, trk.HandoffController)
		inbound := toCallsign == ctx.ControlClient.Callsign

		if inbound {
			// Always show our id
			if toCtrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]; toCtrl != nil {
				handoffId = toCtrl.SectorId[len(toCtrl.SectorId)-1:]
			}
			if fromCtrl := ctx.ControlClient.Controllers[trk.TrackOwner]; fromCtrl != nil {
				if fromCtrl.ERAMFacility { // Enroute controller
					// From any center
					handoffTCP = fromCtrl.SectorId
				} else if fromCtrl.FacilityIdentifier != "" {
					// Different facility; show full id of originator
					handoffTCP = fromCtrl.FacilityIdentifier + fromCtrl.SectorId
				}
			}
		} else { // outbound
			if toCtrl := ctx.ControlClient.Controllers[toCallsign]; toCtrl != nil {
				if toCtrl.ERAMFacility { // Enroute
					// Always the one-character id and the sector
					handoffId = toCtrl.FacilityIdentifier
					handoffTCP = toCtrl.SectorId
				} else if toCtrl.FacilityIdentifier != "" { // Different facility
					// Different facility: show their TCP, id is the facility #
					handoffId = toCtrl.FacilityIdentifier
					handoffTCP = toCtrl.FacilityIdentifier + toCtrl.SectorId
				} else {
					handoffId = toCtrl.SectorId[len(toCtrl.SectorId)-1:]
				}
			}
		}
	}

	// Various other values that will be repeatedly useful below...
	beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(platform.KeyF1)
	actype := ac.FlightPlan.TypeWithoutSuffix()
	if strings.Index(actype, "/") == 1 {
		actype = actype[2:]
	}
	ident := state.Ident(ctx.Now)
	squawkingSPC, _ := av.SquawkIsSPC(ac.Squawk)
	altitude := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
	groundspeed := fmt.Sprintf("%02d", (state.TrackGroundspeed()+5)/10)
	// Note arrivalAirport is only set if it should be shown when there is no scratchpad set
	arrivalAirport := ""
	if ap := ctx.ControlClient.Airports[trk.FlightPlan.ArrivalAirport]; ap != nil && !ap.OmitArrivalScratchpad {
		arrivalAirport = trk.FlightPlan.ArrivalAirport
		if len(arrivalAirport) == 4 && arrivalAirport[0] == 'K' {
			arrivalAirport = arrivalAirport[1:]
		}
	}
	beaconMismatch := ac.Squawk != trk.FlightPlan.AssignedSquawk && !squawkingSPC

	// Figure out what to display for scratchpad 1 (used in both FDB and PDBs)
	sp1 := trk.SP1
	// If it hasn't been set to something and the adapted scratchpad hasn't
	// been cleared, show an adapted one, if appropriate.
	if sp1 == "" && !state.ClearedScratchpadAlternate {
		adapt := ctx.ControlClient.STARSFacilityAdaptation
		falt := func() string {
			alt := ac.FlightPlan.Altitude
			if adapt.AllowLongScratchpad {
				return fmt.Sprintf("%03d", alt/100)
			} else {
				return fmt.Sprintf("%02d", alt/1000)
			}
		}
		shortExit := func() string {
			if e := ac.FlightPlan.Exit; e != "" {
				e, _, _ = strings.Cut(e, ".")
				if sp, ok := sp.significantPoints[e]; ok {
					if sp.ShortName != "" {
						return sp.ShortName
					} else if len(e) > 3 {
						return e[:3]
					} else {
						return e
					}
				}
			}
			return ""
		}
		abbrevExit := func() string {
			if e := ac.FlightPlan.Exit; e != "" {
				e, _, _ = strings.Cut(e, ".")
				if sp, ok := sp.significantPoints[e]; ok {
					if sp.Abbreviation != "" {
						return sp.Abbreviation
					}
					return e[:1]
				}
			}
			return ""
		}
		if arrivalAirport != "" {
			sp1 = arrivalAirport
		} else if adapt.Scratchpad1.DisplayExitFix {
			sp1 = shortExit()
		} else if adapt.Scratchpad1.DisplayExitFix1 {
			sp1 = abbrevExit()
		} else if adapt.Scratchpad1.DisplayExitGate {
			if ex := abbrevExit(); ex != "" {
				sp1 = ex + falt()
			}
		} else if adapt.Scratchpad1.DisplayAltExitGate {
			if ex := abbrevExit(); ex != "" {
				sp1 = falt() + ex
			}
		}
	}

	switch sp.datablockType(ctx, ac) {
	case LimitedDatablock:
		db := &limitedDatablock{}

		// Field 0: CA, MCI, and SPCs
		copy(db.field0[:], alerts[:])

		extended := state.FullLDBEndTime.After(ctx.Now)

		ps := sp.currentPrefs()
		if beaconator || extended || ident || ps.DisplayLDBBeaconCodes || state.DisplayLDBBeaconCode {
			// Field 1: reported beacon code
			// TODO: Field 1: WHO if unassociated and no flight plan
			f1 := formatDBText(db.field1[:], ac.Squawk.String(), color, false)
			// Field 1: flashing ID after beacon code if ident.
			if ident {
				formatDBText(db.field1[f1:], "ID", color, true)
			}
		}

		// Field 3: mode C altitude
		formatDBText(db.field3[:], altitude, color, false)

		if extended {
			// Field 5: groundspeed
			formatDBText(db.field5[:], groundspeed, color, false)
		}

		if beaconator {
			// Field 6: callsign
			formatDBText(db.field6[:], ac.Callsign, color, false)
		}

		return db

	case PartialDatablock:
		fa := ctx.ControlClient.STARSFacilityAdaptation
		db := &partialDatablock{}

		// Field0: TODO cautions in yellow
		// TODO: 2-69 doesn't list CA/MCI, so should this be blank even in
		// those cases? (Note that SPC upgrades partial to full datablocks.)
		//
		// TODO: previously we had the following check:
		// if ac.Squawk != trk.FlightPlan.AssignedSquawk && ac.Squawk != 0o1200 {
		// and would display ac.Squawk + flashing WHO in field0
		copy(db.field0[:], alerts[:])

		// Field 1: a) mode-c or pilot reported altitude, b) scratchpad 1
		// or possibly arrival airport (adapted), c) scratchpad 2 (adapted)
		// Combined with:
		// Field 2: receiving TCP if being handed off or + if sp2 is shown.
		// TODO: * if field 1 is showing pilot-reported altitude
		field1Length := util.Select(fa.AllowLongScratchpad, 4, 3)
		fmt1 := func(s string) string {
			for len(s) < field1Length {
				s += " "
			}
			return s
		}
		formatDBText(db.field12[0][:], fmt1(altitude)+handoffId, color, false)
		f12Idx := 1
		if sp1 != "" {
			formatDBText(db.field12[1][:], fmt1(sp1)+handoffId, color, false)
			f12Idx++
		}
		if fa.PDB.ShowScratchpad2 && trk.SP2 != "" {
			formatDBText(db.field12[f12Idx][:], fmt1(trk.SP2)+"+", color, false)
		}

		// Field 3: by default, groundspeed and/or "V" for VFR, "E" for overflight, followed by CWT,
		// but may be adapted.
		rulesCategory := " "
		if trk.FlightPlan.Rules == av.VFR {
			rulesCategory = "V"
		} else if sp.isOverflight(ctx, trk) {
			rulesCategory = "E"
		}
		if fa.PDB.SplitGSAndCWT {
			// [GS, CWT] timesliced
			formatDBText(db.field3[0][:], groundspeed, color, false)
			formatDBText(db.field3[1][:], rulesCategory+state.CWTCategory, color, false)
		} else {
			if fa.PDB.HideGroundspeed {
				// [CWT]
				formatDBText(db.field3[0][:], rulesCategory+state.CWTCategory, color, false)
			} else {
				// [GS CWT]
				formatDBText(db.field3[0][:], groundspeed+rulesCategory+state.CWTCategory, color, false)
			}
			if fa.PDB.ShowAircraftType {
				// [ACTYPE]
				formatDBText(db.field3[1][:], actype, color, false)
			}
		}

		// Field 4: ident
		if ident {
			formatDBText(db.field4[:], "ID", color, true)
		}

		return db

	case FullDatablock:
		db := &fullDatablock{}

		// Line 0
		// Field 0: special conditions, safety alerts (red), cautions (yellow)
		copy(db.field0[:], alerts[:])

		// Line 1
		// Field 1: callsign (ACID) (or squawk if beaconator)
		if beaconator {
			formatDBText(db.field1[:], ac.Squawk.String(), color, false)
		} else {
			formatDBText(db.field1[:], ac.Callsign, color, false)
		}

		// Field 2: various symbols for inhibited stuff
		if state.InhibitMSAW || state.DisableMSAW {
			if state.DisableCAWarnings {
				formatDBText(db.field2[:], "+", color, false)
			} else {
				formatDBText(db.field2[:], "*", color, false)
			}
		} else if state.DisableCAWarnings {
			formatDBText(db.field2[:], STARSTriangleCharacter, color, false)
		}

		// Field 8: point out, rejected pointout, redirected
		// handoffs... Some flash, some don't.
		if _, ok := sp.InboundPointOuts[ac.Callsign]; ok || state.PointedOut {
			formatDBText(db.field8[:], "PO", color, false)
		} else if id, ok := sp.OutboundPointOuts[ac.Callsign]; ok {
			if len(id) > 1 && id[0] >= '0' && id[0] <= '9' {
				id = id[1:]
			}
			formatDBText(db.field8[:], "PO"+id, color, false)
		} else if ctx.Now.Before(state.UNFlashingEndTime) {
			formatDBText(db.field8[:], "UN", color, true)
		} else if state.POFlashingEndTime.After(ctx.Now) {
			formatDBText(db.field8[:], "PO", color, true)
		} else if ac.RedirectedHandoff.ShowRDIndicator(ctx.ControlClient.Callsign, state.RDIndicatorEnd) {
			formatDBText(db.field8[:], "RD", color, false)
		}

		// Line 2
		// Fields 3 and 4: 3 is altitude plus possibly other stuff; 4 is
		// special indicators, possible associated with 3, so they're a
		// single field
		field3Length := util.Select(ctx.ControlClient.STARSFacilityAdaptation.AllowLongScratchpad, 4, 3)
		fmt3 := func(s string) string {
			for len(s) < field3Length {
				s += " "
			}
			return s
		}

		formatDBText(db.field34[0][:], fmt3(altitude)+handoffId, color, false)
		idx34 := 1
		if sp1 != "" {
			formatDBText(db.field34[idx34][:], fmt3(sp1)+handoffId, color, false)
			idx34++
		}
		if handoffTCP != "" {
			formatDBText(db.field34[idx34][:], fmt3(handoffTCP)+handoffId, color, false)
		} else if ac.SecondaryScratchpad != "" { // don't show secondary if we're showing a center
			// TODO: confirm no handoffId here
			formatDBText(db.field34[idx34][:], fmt3(trk.SP2)+"+", color, false)
		}

		// Field 5: groundspeed
		rulesCategory := " "
		if ac.FlightPlan.Rules == av.VFR {
			rulesCategory = "V"
		} else if sp.isOverflight(ctx, trk) {
			rulesCategory = "E"
		}
		rulesCategory += state.CWTCategory + " "

		if state.IFFlashing {
			if ident {
				formatDBText(db.field5[0][:], "IF"+"ID", color, true)
			} else {
				formatDBText(db.field5[0][:], "IF"+rulesCategory, color, true)
			}
		} else {
			idx := formatDBText(db.field5[0][:], groundspeed, color, false)
			if ident {
				formatDBText(db.field5[0][idx:], "ID", color, true)
			} else {
				formatDBText(db.field5[0][idx:], rulesCategory, color, false)
			}
		}
		// Field 5: +aircraft type and possibly requested altitude, if not
		// identing.
		if !ident {
			formatDBText(db.field5[1][:], actype+" ", color, false)

			if (state.DisplayRequestedAltitude != nil && *state.DisplayRequestedAltitude) ||
				(state.DisplayRequestedAltitude == nil && sp.currentPrefs().DisplayRequestedAltitude) {
				formatDBText(db.field5[2][:], fmt.Sprintf("R%03d ", ac.FlightPlan.Altitude/100), color, false)
			}
		}

		// Field 6: ATPA info and possibly beacon code
		// TODO: DB for duplicate beacon code as well
		if state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert {
			formatDBText(db.field6[0][:], "*TPA", color, false)
		} else if state.IntrailDistance != 0 && sp.currentPrefs().DisplayATPAInTrailDist {
			distColor := color
			if state.ATPAStatus == ATPAStatusWarning {
				distColor = STARSATPAWarningColor
			} else if state.ATPAStatus == ATPAStatusAlert {
				distColor = STARSATPAAlertColor
			}
			formatDBText(db.field6[0][:], fmt.Sprintf("%.2f", state.IntrailDistance), distColor, false)
		}
		if beaconMismatch {
			idx := util.Select(fieldEmpty(db.field6[0][:]), 0, 1)
			formatDBText(db.field6[idx][:], ac.Squawk.String(), color, false)
		}

		// Field 7: assigned altitude, assigned beacon if mismatch
		if ac.TempAltitude != 0 {
			ta := (ac.TempAltitude + 50) / 100
			formatDBText(db.field7[0][:], fmt.Sprintf("A%03d", ta), color, false)
		}
		if beaconMismatch {
			idx := util.Select(fieldEmpty(db.field7[0][:]), 0, 1)
			formatDBText(db.field7[idx][:], trk.FlightPlan.AssignedSquawk.String(), color, true)
		}

		return db
	}

	return nil
}

func (sp *STARSPane) getGhostDatablock(ghost *av.GhostAircraft, color renderer.RGB) ghostDatablock {
	var db ghostDatablock

	state := sp.Aircraft[ghost.Callsign]
	groundspeed := fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)
	if state.Ghost.PartialDatablock {
		// Partial datablock is just airspeed and then aircraft CWT type
		formatDBText(db.field0[:], groundspeed+state.CWTCategory, color, false)
	} else {
		// The full datablock ain't much more...
		formatDBText(db.field0[:], ghost.Callsign, color, false)
		formatDBText(db.field1[:], groundspeed, color, false) // TODO: no CWT?
	}

	return db
}

func (sp *STARSPane) trackDatablockColorBrightness(ctx *panes.Context, ac *av.Aircraft) (color renderer.RGB, dbBrightness, posBrightness STARSBrightness) {
	ps := sp.currentPrefs()
	dt := sp.datablockType(ctx, ac)
	state := sp.Aircraft[ac.Callsign]
	trk := sp.getTrack(ctx, ac)

	// Cases where it's always a full datablock
	_, forceFDB := sp.InboundPointOuts[ac.Callsign]
	forceFDB = forceFDB || (state.OutboundHandoffAccepted && ctx.Now.Before(state.OutboundHandoffFlashEnd))
	forceFDB = forceFDB || trk.HandingOffTo(ctx.ControlClient.Callsign)

	// Figure out the datablock and position symbol brightness first
	if ac.Callsign == sp.dwellAircraft { // dwell overrides everything as far as brightness
		dbBrightness = STARSBrightness(100)
		posBrightness = STARSBrightness(100)
	} else if forceFDB || state.OutboundHandoffAccepted {
		dbBrightness = ps.Brightness.FullDatablocks
		posBrightness = ps.Brightness.Positions
	} else if dt == PartialDatablock || dt == LimitedDatablock {
		dbBrightness = ps.Brightness.LimitedDatablocks
		posBrightness = ps.Brightness.LimitedDatablocks
	} else /* dt == FullDatablock */ {
		if trk != nil && trk.TrackOwner != ctx.ControlClient.Callsign {
			dbBrightness = ps.Brightness.OtherTracks
			posBrightness = ps.Brightness.OtherTracks
		} else {
			// Regular FDB that we own
			dbBrightness = ps.Brightness.FullDatablocks
			posBrightness = ps.Brightness.Positions
		}
	}

	// Possibly adjust brightness if it should be flashing.
	halfSeconds := ctx.Now.UnixMilli() / 500
	if forceFDB && halfSeconds&1 == 0 { // half-second cycle
		dbBrightness /= 2
		posBrightness /= 2
	}

	if trk == nil {
		color = STARSUntrackedAircraftColor
		return
	}

	for _, controller := range trk.RedirectedHandoff.Redirector {
		if controller == ctx.ControlClient.Callsign && trk.RedirectedHandoff.RedirectedTo != ctx.ControlClient.Callsign {
			color = STARSUntrackedAircraftColor
		}
	}

	// Check if we're the controller being ForceQL
	if _, ok := sp.ForceQLCallsigns[ac.Callsign]; ok {
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
			/* FIXME(mtrokel): temporarily disabled. This flashes in and out e.g. in JFK scenarios for the LGA water gate departures.
			} else if trk.AutoAssociateFP {
				color = STARSTrackedAircraftColor
			*/
		} else {
			color = STARSUntrackedAircraftColor
		}
	}

	return
}

func (sp *STARSPane) datablockVisible(ac *av.Aircraft, ctx *panes.Context) bool {
	trk := sp.getTrack(ctx, ac)

	af := sp.currentPrefs().AltitudeFilters
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
	} else if sp.isOverflight(ctx, trk) && sp.currentPrefs().OverflightFullDatablocks { //Need a f7 + e
		// Overflights
		return true
	} else if sp.isQuicklooked(ctx, ac) {
		return true
	} else if trk != nil && trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.Callsign {
		// Redirected to
		return true
	} else if trk != nil && slices.Contains(trk.RedirectedHandoff.Redirector, ctx.ControlClient.Callsign) {
		// Had it but redirected it
		return true
	}

	// Check altitude filters
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
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Datablocks)

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		db := sp.getDatablock(ctx, ac)
		if db == nil {
			continue
		}

		_, brightness, _ := sp.trackDatablockColorBrightness(ctx, ac)
		if brightness == 0 {
			continue
		}

		// Calculate the endpoint of the leader line
		pac := transforms.WindowFromLatLongP(state.TrackPosition())
		leaderLineDirection := sp.getLeaderLineDirection(ac, ctx)
		vll := sp.getLeaderLineVector(ctx, leaderLineDirection)
		pll := math.Add2f(pac, vll)
		if math.Length2f(vll) == 0 {
			// no leader line is being drawn; make sure that the datablock
			// doesn't overlap the target track.
			sz := sp.getTrackSize(ctx, transforms) / 2
			rightJustify := leaderLineDirection >= math.South
			pll[0] += util.Select(rightJustify, -sz, sz)
			pll[1] += float32(font.Size)
		} else {
			// Start drawing down a half line-height to align the leader
			// line in the middle of the db line.
			pll[1] += float32(font.Size / 2)
		}

		halfSeconds := realNow.UnixMilli() / 500
		db.draw(td, pll, font, brightness, sp.getLeaderLineDirection(ac, ctx), halfSeconds)
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) haveActiveWarnings(ctx *panes.Context, ac *av.Aircraft) bool {
	ps := sp.currentPrefs()
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

	ps := sp.currentPrefs()
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
