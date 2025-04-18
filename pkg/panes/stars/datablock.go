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
	PartialDatablock DatablockType = iota
	LimitedDatablock
	FullDatablock
)

// datablock is a simple interface that abstracts the various types of
// datablock. The only operation that exposes is drawing the datablock.
type datablock interface {
	// pt is end of leader line--attachment point
	draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
		brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64)
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

func (db fullDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
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
	dbDrawLines(lines, td, pt, font, strBuilder, brightness, leaderLineDirection, halfSeconds)
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

func (db partialDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
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
	dbDrawLines(lines, td, pt, font, strBuilder, brightness, leaderLineDirection, halfSeconds)
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
	field4 [1]dbChar // unused: 2-70 says 2 chars, but evidently 1 is the usual.
	field5 [4]dbChar
	// Line 3 (not in manual, but for beaconator callsign)
	field6 [8]dbChar
}

func (db limitedDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(db.field1[:], db.field2[:]),
		dbMakeLine(db.field3[:], db.field4[:], db.field5[:]),
		dbMakeLine(db.field6[:]),
	}
	pt[1] += 2 * float32(font.Size) // align leader with line 2
	dbDrawLines(lines, td, pt, font, strBuilder, brightness, leaderLineDirection, halfSeconds)
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

func (db ghostDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(db.field1[:]),
	}
	// Leader aligns with line 0, so no offset is needed
	dbDrawLines(lines, td, pt, font, strBuilder, brightness, leaderLineDirection, halfSeconds)
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

func dbDrawLines(lines []dbLine, td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	rightJustify := leaderLineDirection >= math.South
	glyph := font.LookupGlyph(' ')
	fontWidth := glyph.AdvanceX

	for _, line := range lines {
		xOffset := float32(4)
		if rightJustify {
			xOffset = -4 - float32(line.Len())*fontWidth
		}
		strBuilder.Reset()
		dbDrawLine(line, td, math.Add2f(pt, [2]float32{xOffset, 0}), font, strBuilder, brightness, halfSeconds)
		// Step down to the next line
		pt[1] -= float32(font.Size)
	}
}

func dbDrawLine(line dbLine, td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, str *strings.Builder,
	brightness STARSBrightness, halfSeconds int64) {
	// We will batch characters to be drawn up into str and flush them out
	// in a call to TextDrawBuider AddText() only when the color
	// changes. (This is some effort to minimize the number of AddText()
	// calls.)
	style := renderer.TextStyle{Font: font}

	flush := func() {
		if str.Len() > 0 {
			pt = td.AddText(rewriteDelta(str.String()), pt, style)
			str.Reset()
		}
	}

	for i := range line.length {
		ch := line.ch[i]
		if ch.ch == 0 {
			// Treat unset as a space
			str.WriteByte(' ')
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
			str.WriteRune(ch.ch)
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
	if ac.TrackingController == "" {
		// Must be limited, regardless of anything else.
		return LimitedDatablock
	} else {
		// The track owner is known, so it will be a P/FDB
		state := sp.Aircraft[ac.Callsign]

		beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(platform.KeyF1)
		if beaconator {
			// Partial is always full with the beaconator, so we're done
			// here in that case.
			return FullDatablock
		}

		if ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && ac.Squawk == sp.DisplayBeaconCode {
			// 6-117
			return FullDatablock
		}

		if ac.TrackingController == ctx.ControlClient.UserTCP {
			// it's under our control
			return FullDatablock
		}

		if ac.HandingOffTo(ctx.ControlClient.UserTCP) {
			// it's being handed off to us
			return FullDatablock
		}

		if state.DisplayFDB {
			// Outbound handoff or we slewed a PDB to make it a FDB
			return FullDatablock
		}

		if sp.haveActiveWarnings(ctx, ac) {
			return FullDatablock
		}

		// Point outs are FDB until acked.
		if tcps, ok := sp.PointOuts[ac.Callsign]; ok && tcps.To == ctx.ControlClient.UserTCP {
			return FullDatablock
		}
		if state.PointOutAcknowledged {
			return FullDatablock
		}
		if state.ForceQL {
			return FullDatablock
		}

		if len(ac.RedirectedHandoff.Redirector) > 0 {
			if ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.UserTCP {
				return FullDatablock
			}
		}

		if ac.RedirectedHandoff.OriginalOwner == ctx.ControlClient.UserTCP {
			return FullDatablock
		}

		if sp.currentPrefs().OverflightFullDatablocks && sp.isOverflight(ctx, ac) {
			return FullDatablock
		}

		if sp.isQuicklooked(ctx, ac) {
			return FullDatablock
		}

		return PartialDatablock
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

func (sp *STARSPane) getAllDatablocks(aircraft []*av.Aircraft, ctx *panes.Context) map[string]datablock {
	sp.fdbArena.Reset()
	sp.pdbArena.Reset()
	sp.ldbArena.Reset()

	m := make(map[string]datablock)
	for _, ac := range aircraft {
		m[ac.Callsign] = sp.getDatablock(ctx, ac)
	}
	return m
}

func (sp *STARSPane) getDatablock(ctx *panes.Context, ac *av.Aircraft) datablock {
	now := ctx.ControlClient.CurrentTime()
	state := sp.Aircraft[ac.Callsign]
	if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
		return nil
	}

	color, brightness, _ := sp.trackDatablockColorBrightness(ctx, ac)

	trk := sp.getTrack(ctx, ac)

	// Check if the track is being handed off.
	//
	// handoffTCP is only set if it's coming from an enroute position or
	// from a different facility; otherwise we just show the
	// single-character id.
	handoffId, handoffTCP := " ", ""
	if trk.HandoffController != "" {
		toTCP := util.Select(trk.RedirectedHandoff.RedirectedTo != "",
			trk.RedirectedHandoff.RedirectedTo, trk.HandoffController)
		inbound := toTCP == ctx.ControlClient.UserTCP

		if inbound {
			// Always show our id
			handoffId = toTCP[len(toTCP)-1:]
			if fromCtrl := ctx.ControlClient.Controllers[trk.TrackOwner]; fromCtrl != nil {
				if fromCtrl.ERAMFacility { // Enroute controller
					// From any center
					handoffTCP = trk.TrackOwner
				} else if fromCtrl.FacilityIdentifier != "" {
					// Different facility; show full id of originator
					handoffTCP = fromCtrl.FacilityIdentifier + fromCtrl.TCP
				}
			}
		} else { // outbound
			if toCtrl := ctx.ControlClient.Controllers[toTCP]; toCtrl != nil {
				if toCtrl.ERAMFacility { // Enroute
					// Always the one-character id and the sector
					handoffId = toCtrl.FacilityIdentifier
					handoffTCP = toTCP
				} else if toCtrl.FacilityIdentifier != "" { // Different facility
					// Different facility: show their TCP, id is the facility #
					handoffId = toCtrl.FacilityIdentifier
					handoffTCP = toCtrl.FacilityIdentifier + toTCP
				} else {
					handoffId = toTCP[len(toTCP)-1:]
				}
			}
		}
	}
	if handoffTCP == "" && ctx.Now.Before(state.AcceptedHandoffDisplayEnd) {
		handoffTCP = state.AcceptedHandoffSector
	}

	// Various other values that will be repeatedly useful below...
	beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(platform.KeyF1)
	actype := ac.FlightPlan.TypeWithoutSuffix()
	if strings.Index(actype, "/") == 1 {
		actype = actype[2:]
	}
	ident := state.Ident(ctx.Now)
	squawkingSPC, _ := ac.Squawk.IsSPC()

	// Note: this is only for PDBs and FDBs. LDBs don't have pilot reported
	// altitude or inhibit mode C.
	altitude := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
	if ac.PilotReportedAltitude != 0 {
		altitude = fmt.Sprintf("%03d", (ac.PilotReportedAltitude+50)/100)
	} else if ac.InhibitModeCAltitudeDisplay {
		altitude = "***"
	} else if ac.Mode == av.Standby {
		altitude = "RDR"
	} else if ac.Mode == av.On {
		altitude = ""
	}

	displayBeaconCode := ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && ac.Squawk == sp.DisplayBeaconCode

	groundspeed := fmt.Sprintf("%02d", (state.TrackGroundspeed()+5)/10)
	// Note arrivalAirport is only set if it should be shown when there is no scratchpad set
	arrivalAirport := ""
	if ap := ctx.ControlClient.Airports[ac.FlightPlan.ArrivalAirport]; ap != nil && !ap.OmitArrivalScratchpad {
		arrivalAirport = ac.FlightPlan.ArrivalAirport
		if len(arrivalAirport) == 4 && arrivalAirport[0] == 'K' {
			arrivalAirport = arrivalAirport[1:]
		}
	}
	beaconMismatch := ac.Squawk != ac.FlightPlan.AssignedSquawk && !squawkingSPC

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
		db := sp.ldbArena.AllocClear()

		// Field 0: CA, MCI, and squawking special codes
		alerts := sp.getDatablockAlerts(ctx, ac, LimitedDatablock)
		copy(db.field0[:], alerts[:])

		extended := state.FullLDBEndTime.After(ctx.Now)

		if len(alerts) == 0 && ac.Mode == av.On && !extended {
			return nil
		}

		ps := sp.currentPrefs()
		if ac.Mode != av.Standby {
			mci := !ps.DisableMCIWarnings && slices.ContainsFunc(sp.MCIAircraft, func(mci CAAircraft) bool {
				return mci.Callsigns[1] == ac.Callsign && sp.Aircraft[mci.Callsigns[0]].MCISuppressedCode != ac.Squawk
			})

			if mci || beaconator || extended || ident || ps.DisplayLDBBeaconCodes || state.DisplayLDBBeaconCode || displayBeaconCode {
				// Field 1: reported beacon code
				// TODO: Field 1: WHO if unassociated and no flight plan
				var f1 int
				if displayBeaconCode { // flashing yellow
					f1 = formatDBText(db.field1[:], ac.Squawk.String(), brightness.ScaleRGB(STARSTextWarningColor), true)
				} else {
					f1 = formatDBText(db.field1[:], ac.Squawk.String(), color, false)
				}
				// Field 1: flashing ID after beacon code if ident.
				if ident {
					formatDBText(db.field1[f1:], "ID", color, true)
				}
			}
		}

		// Field 3: mode C altitude
		altitude := fmt.Sprintf("%03d", (state.TrackAltitude()+50)/100)
		if ac.Mode == av.Standby {
			if extended {
				altitude = "RDR"
			} else {
				altitude = ""
			}
		} else if ac.Mode == av.On { // mode-a; altitude is blank
			altitude = ""
		}

		formatDBText(db.field3[:], altitude, color, false)

		if extended {
			// Field 5: groundspeed
			formatDBText(db.field5[:], groundspeed, color, false)
		}

		if (extended || beaconator) && ac.Mode != av.Standby {
			// Field 6: callsign
			formatDBText(db.field6[:], ac.Callsign, color, false)
		}

		return db

	case PartialDatablock:
		fa := ctx.ControlClient.STARSFacilityAdaptation
		db := sp.pdbArena.AllocClear()

		// Field0: TODO cautions in yellow
		// TODO: 2-69 doesn't list CA/MCI, so should this be blank even in
		// those cases? (Note that SPC upgrades partial to full datablocks.)
		//
		// TODO: previously we had the following check:
		// if ac.Squawk != ac.FlightPlan.AssignedSquawk && ac.Squawk != 0o1200 {
		// and would display ac.Squawk + flashing WHO in field0
		alerts := sp.getDatablockAlerts(ctx, ac, PartialDatablock)
		copy(db.field0[:], alerts[:])

		// Field 1: a) mode-c or pilot reported altitude, b) scratchpad 1
		// or possibly arrival airport (adapted), c) scratchpad 2 (adapted)
		// Combined with:
		// Field 2: receiving TCP if being handed off or + if sp2 is shown.
		// TODO: * if field 1 is showing pilot-reported altitude
		field1Length := util.Select(fa.AllowLongScratchpad, 4, 3)
		fmt1 := func(s string) string {
			for len([]rune(s)) < field1Length {
				s += " "
			}
			return s
		}
		if ac.PilotReportedAltitude != 0 {
			formatDBText(db.field12[0][:], fmt1(altitude+"*"), color, false)
		} else {
			formatDBText(db.field12[0][:], fmt1(altitude)+handoffId, color, false)
		}
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
		if ac.FlightPlan.Rules == av.VFR {
			rulesCategory = "V"
		} else if sp.isOverflight(ctx, ac) {
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
		db := sp.fdbArena.AllocClear()

		// Line 0
		// Field 0: special conditions, safety alerts (red), cautions (yellow)
		alerts := sp.getDatablockAlerts(ctx, ac, FullDatablock)
		copy(db.field0[:], alerts[:])

		// Line 1
		// Field 1: callsign (ACID) (or squawk if beaconator)
		if beaconator && ac.Mode != av.Standby {
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
		} else if state.DisableCAWarnings || state.MCISuppressedCode != 0 {
			formatDBText(db.field2[:], STARSTriangleCharacter, color, false)
		}

		// Field 8: point out, rejected pointout, redirected
		// handoffs... Some flash, some don't.
		if tcps, ok := sp.PointOuts[ac.Callsign]; ok && tcps.To == ctx.ControlClient.UserTCP {
			formatDBText(db.field8[:], "PO", color, false)
		} else if ok && tcps.From == ctx.ControlClient.UserTCP {
			id := tcps.To
			if len(id) > 1 && id[0] >= '0' && id[0] <= '9' {
				id = id[1:]
			}
			formatDBText(db.field8[:], "PO"+id, color, false)
		} else if ctx.Now.Before(state.UNFlashingEndTime) {
			formatDBText(db.field8[:], "UN", color, true)
		} else if state.POFlashingEndTime.After(ctx.Now) {
			formatDBText(db.field8[:], "PO", color, true)
		} else if ac.RedirectedHandoff.ShowRDIndicator(ctx.ControlClient.UserTCP, state.RDIndicatorEnd) {
			formatDBText(db.field8[:], "RD", color, false)
		}

		// Line 2
		// Fields 3 and 4: 3 is altitude plus possibly other stuff; 4 is
		// special indicators, possible associated with 3, so they're a
		// single field
		field3Length := util.Select(ctx.ControlClient.STARSFacilityAdaptation.AllowLongScratchpad, 4, 3)
		fmt3 := func(s string) string {
			for len([]rune(s)) < field3Length {
				s += " "
			}
			return s
		}

		if ac.PilotReportedAltitude != 0 {
			formatDBText(db.field34[0][:], fmt3(altitude+"*"), color, false)
		} else {
			formatDBText(db.field34[0][:], fmt3(altitude)+handoffId, color, false)
		}
		idx34 := 1
		if sp1 != "" {
			formatDBText(db.field34[idx34][:], fmt3(sp1)+handoffId, color, false)
			idx34++
		}
		if handoffTCP != "" && !ctx.ControlClient.STARSFacilityAdaptation.DisplayHOFacilityOnly {
			formatDBText(db.field34[idx34][:], fmt3(handoffTCP)+handoffId, color, false)
		} else if ac.SecondaryScratchpad != "" { // don't show secondary if we're showing a center
			// TODO: confirm no handoffId here
			formatDBText(db.field34[idx34][:], fmt3(trk.SP2)+"+", color, false)
		}

		// Field 5: groundspeed
		rulesCategory := " "
		if ac.FlightPlan.Rules == av.VFR {
			rulesCategory = "V"
		} else if sp.isOverflight(ctx, ac) {
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
		idx6 := 0
		if state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert {
			formatDBText(db.field6[idx6][:], "*TPA", color, false)
			idx6++
		} else if state.IntrailDistance != 0 && sp.currentPrefs().DisplayATPAInTrailDist {
			distColor := color
			if state.ATPAStatus == ATPAStatusWarning {
				distColor = STARSATPAWarningColor
			} else if state.ATPAStatus == ATPAStatusAlert {
				distColor = STARSATPAAlertColor
			}
			formatDBText(db.field6[idx6][:], fmt.Sprintf("%.2f", state.IntrailDistance), distColor, false)
			idx6++
		}
		if displayBeaconCode {
			formatDBText(db.field6[idx6][:], ac.Squawk.String(), brightness.ScaleRGB(STARSTextWarningColor), true)
			idx6++
		} else if beaconMismatch {
			formatDBText(db.field6[idx6][:], ac.Squawk.String(), color, false)
			idx6++
		}
		if _, ok := sp.DuplicateBeacons[ac.Squawk]; ok {
			acked := state.DBAcknowledged == ac.Squawk
			formatDBText(db.field6[idx6][:], "DB", color, !acked)
		}

		// Field 7: assigned altitude, assigned beacon if mismatch
		if ac.TempAltitude != 0 {
			ta := (ac.TempAltitude + 50) / 100
			formatDBText(db.field7[0][:], fmt.Sprintf("A%03d", ta), color, false)
		}
		if beaconMismatch {
			idx := util.Select(fieldEmpty(db.field7[0][:]), 0, 1)
			formatDBText(db.field7[idx][:], ac.FlightPlan.AssignedSquawk.String(), color, true)
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

	inboundPointOut := false
	if tcps, ok := sp.PointOuts[ac.Callsign]; ok && tcps.To == ctx.ControlClient.UserTCP {
		inboundPointOut = true
	}

	// Cases where it's always a full datablock
	forceFDB := inboundPointOut
	forceFDB = forceFDB || (state.OutboundHandoffAccepted && ctx.Now.Before(state.OutboundHandoffFlashEnd))
	forceFDB = forceFDB || ac.HandingOffTo(ctx.ControlClient.UserTCP)
	if tcps, ok := sp.PointOuts[ac.Callsign]; ok && tcps.To == ctx.ControlClient.UserTCP {
		forceFDB = true
	}

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
		if ac.TrackingController != ctx.ControlClient.UserTCP {
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

	if _, ok := sp.ForceQLCallsigns[ac.Callsign]; ok {
		// Check if we're the controller being ForceQL
		color = STARSInboundPointOutColor
	} else if state.PointOutAcknowledged || state.ForceQL {
		// Ack'ed point out to us (but not cleared) or force quick look.
		color = STARSInboundPointOutColor
	} else if inboundPointOut {
		// Pointed out to us.
		color = STARSInboundPointOutColor
	} else if state.IsSelected {
		// middle button selected
		color = STARSSelectedAircraftColor
	} else if ac.TrackingController == "" {
		color = STARSUntrackedAircraftColor
	} else if ac.TrackingController == ctx.ControlClient.UserTCP { //change
		// we own the track track
		color = STARSTrackedAircraftColor
	} else if ac.RedirectedHandoff.OriginalOwner == ctx.ControlClient.UserTCP || ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.UserTCP {
		color = STARSTrackedAircraftColor
	} else if ac.HandoffTrackController == ctx.ControlClient.UserTCP &&
		!slices.Contains(ac.RedirectedHandoff.Redirector, ctx.ControlClient.UserTCP) {
		// flashing white if it's being handed off to us.
		color = STARSTrackedAircraftColor
	} else if state.OutboundHandoffAccepted {
		// we handed it off, it was accepted, but we haven't yet acknowledged
		color = STARSTrackedAircraftColor
	} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
		// quick look all plus
		color = STARSTrackedAircraftColor
	} else if slices.ContainsFunc(ps.QuickLookPositions,
		func(q QuickLookPosition) bool { return q.Id == ac.TrackingController && q.Plus }) {
		// individual quicklook plus controller
		color = STARSTrackedAircraftColor
		/* FIXME(mtrokel): temporarily disabled. This flashes in and out e.g. in JFK scenarios for the LGA water gate departures.
		} else if trk.AutoAssociateFP {
			color = STARSTrackedAircraftColor
		*/
	} else {
		color = STARSUntrackedAircraftColor
	}

	return
}

func (sp *STARSPane) datablockVisible(ac *av.Aircraft, ctx *panes.Context) bool {
	state := sp.Aircraft[ac.Callsign]

	af := sp.currentPrefs().AltitudeFilters
	alt := state.TrackAltitude()

	if ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && ac.Squawk == sp.DisplayBeaconCode {
		// beacon code display 6-117
		return true
	}

	if ac.TrackingController == "" { // unassociated
		if ac.Mode == av.Standby {
			// unassociated also primary only, only show a datablock if it's been slewed
			return ctx.Now.Before(state.FullLDBEndTime)
		}
		// Check altitude filters
		return alt >= af.Unassociated[0] && alt <= af.Unassociated[1]
	} else { // associated
		state := sp.Aircraft[ac.Callsign]

		if ac.TrackingController == ctx.ControlClient.UserTCP {
			// For owned datablocks
			return true
		} else if ac.HandoffTrackController == ctx.ControlClient.UserTCP {
			// For receiving handoffs
			return true
		} else if ac.ControllingController == ctx.ControlClient.UserTCP {
			// For non-greened handoffs
			return true
		} else if state.PointOutAcknowledged {
			// Pointouts: This is if its been accepted,
			// for an incoming pointout, it falls to the FDB check
			return true
		} else if ok, _ := ac.Squawk.IsSPC(); ok {
			// Special purpose codes
			return true
		} else if state.DisplayFDB {
			return true
		} else if sp.isOverflight(ctx, ac) && sp.currentPrefs().OverflightFullDatablocks { //Need a f7 + e
			// Overflights
			return true
		} else if sp.isQuicklooked(ctx, ac) {
			return true
		} else if ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.UserTCP {
			// Redirected to
			return true
		} else if slices.Contains(ac.RedirectedHandoff.Redirector, ctx.ControlClient.UserTCP) {
			// Had it but redirected it
			return true
		}

		// Check altitude filters
		return alt >= af.Associated[0] && alt <= af.Associated[1]
	}
}

func (sp *STARSPane) drawDatablocks(aircraft []*av.Aircraft, dbs map[string]datablock, ctx *panes.Context,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	now := ctx.ControlClient.SimTime
	realNow := ctx.Now // for flashing rate...
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Datablocks)

	// Partition them by DB type so we can draw FDBs last
	var ldbs, pdbs, fdbs []*av.Aircraft

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !sp.datablockVisible(ac, ctx) {
			continue
		}

		switch sp.datablockType(ctx, ac) {
		case LimitedDatablock:
			ldbs = append(ldbs, ac)
		case PartialDatablock:
			pdbs = append(pdbs, ac)
		case FullDatablock:
			fdbs = append(fdbs, ac)
		}
	}

	if sp.dwellAircraft != "" {
		// If the dwelled aircraft is in the given slice, move it to the
		// end so that it is drawn last (among its datablock category) and
		// appears on top.
		moveDwelledToEnd := func(aircraft []*av.Aircraft) []*av.Aircraft {
			isDwelled := func(ac *av.Aircraft) bool { return ac.Callsign == sp.dwellAircraft }
			if idx := slices.IndexFunc(aircraft, isDwelled); idx != -1 {
				aircraft = append(aircraft, aircraft[idx])
				aircraft = append(aircraft[:idx], aircraft[idx+1:]...)
			}
			return aircraft
		}

		ldbs = moveDwelledToEnd(ldbs)
		pdbs = moveDwelledToEnd(pdbs)
		fdbs = moveDwelledToEnd(fdbs)
	}

	var strBuilder strings.Builder
	for _, dbAircraft := range [][]*av.Aircraft{ldbs, pdbs, fdbs} {
		for _, ac := range dbAircraft {
			db := dbs[ac.Callsign]
			if db == nil {
				continue
			}

			_, brightness, _ := sp.trackDatablockColorBrightness(ctx, ac)
			if brightness == 0 {
				continue
			}

			state := sp.Aircraft[ac.Callsign]

			// Calculate the endpoint of the leader line
			pac := transforms.WindowFromLatLongP(state.TrackPosition())
			leaderLineDirection := sp.getLeaderLineDirection(ac, ctx)
			vll := sp.getLeaderLineVector(ctx, leaderLineDirection)
			pll := math.Add2f(pac, math.Scale2f(vll, ctx.DrawPixelScale))
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
			db.draw(td, pll, font, &strBuilder, brightness, sp.getLeaderLineDirection(ac, ctx), halfSeconds)
		}
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
	if ok, _ := ac.Squawk.IsSPC(); ok {
		return true
	}
	if ac.SPCOverride != "" && av.StringIsSPC(ac.SPCOverride) /* only alerts, not custom warning SPCs */ {
		return true
	}
	if !ps.DisableCAWarnings && !state.DisableCAWarnings &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
			}) ||
		slices.ContainsFunc(sp.MCIAircraft,
			func(ca CAAircraft) bool {
				return ca.Callsigns[0] == ac.Callsign &&
					ctx.ControlClient.Aircraft[ca.Callsigns[0]].Squawk != state.MCISuppressedCode
			}) {
		return true
	}
	if _, warn := sp.WarnOutsideAirspace(ctx, ac); warn {
		return true
	}

	return false
}

func (sp *STARSPane) getDatablockAlerts(ctx *panes.Context, ac *av.Aircraft, dbtype DatablockType) []dbChar {
	ps := sp.currentPrefs()
	state := sp.Aircraft[ac.Callsign]

	var alerts []dbChar
	added := make(map[string]interface{})
	addAlert := func(s string, flash bool, red bool) {
		if _, ok := added[s]; ok {
			return // don't duplicate it
		}
		added[s] = nil

		color := util.Select(red, STARSTextAlertColor, STARSTextWarningColor)
		if len(alerts) > 0 {
			alerts = append(alerts, dbChar{ch: '/', color: color})
		}
		for _, ch := range s {
			alerts = append(alerts, dbChar{ch: ch, color: color, flashing: flash})
		}
	}

	if dbtype == LimitedDatablock || dbtype == FullDatablock {
		if !ps.DisableMCIWarnings {
			if idx := slices.IndexFunc(sp.MCIAircraft, func(mci CAAircraft) bool {
				if mci.Callsigns[0] != ac.Callsign && mci.Callsigns[1] != ac.Callsign {
					return false
				}
				if sp.Aircraft[mci.Callsigns[0]].MCISuppressedCode == ctx.ControlClient.Aircraft[mci.Callsigns[1]].Squawk {
					return false
				}
				return true
			}); idx != -1 {
				addAlert("CA", !sp.MCIAircraft[idx].Acknowledged, true)
			}
		}
		if ok, code := ac.Squawk.IsSPC(); ok {
			addAlert(code, !state.SPCAcknowledged, true)
		}
	}
	if dbtype == FullDatablock {
		if state.MSAW && !state.InhibitMSAW && !state.DisableMSAW && !ps.DisableMSAW {
			addAlert("LA", !state.MSAWAcknowledged, true)
		}
		if ac.SPCOverride != "" {
			red := av.StringIsSPC(ac.SPCOverride) // std ones are red, adapted ones are yellow.
			addAlert(ac.SPCOverride, !state.SPCAcknowledged, red)
		}
		if !ps.DisableCAWarnings && !state.DisableCAWarnings {
			if idx := slices.IndexFunc(sp.CAAircraft,
				func(ca CAAircraft) bool {
					return ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign
				}); idx != -1 {
				addAlert("CA", !sp.CAAircraft[idx].Acknowledged, true)
			}
		}
		if alts, warn := sp.WarnOutsideAirspace(ctx, ac); warn {
			altStrs := ""
			for _, a := range alts {
				altStrs += fmt.Sprintf("/%d-%d", a[0]/100, a[1]/100)
			}
			addAlert("AS"+altStrs, false, true)
		}
	} else if dbtype == PartialDatablock {
		fa := ctx.ControlClient.State.STARSFacilityAdaptation
		if ac.SPCOverride != "" && fa.PDB.DisplayCustomSPCs {
			// We only care about adapted alerts
			if slices.Contains(fa.CustomSPCs, ac.SPCOverride) {
				addAlert(ac.SPCOverride, !state.SPCAcknowledged, false)
			}
		}
	}

	// Both FDB and PDB
	if sp.radarMode(ctx.ControlClient.State.STARSFacilityAdaptation.RadarSites) == RadarModeFused &&
		ac.TrackingController != "" && ac.PilotReportedAltitude == 0 &&
		(ac.Mode != av.Altitude || ac.InhibitModeCAltitudeDisplay) {
		// No altitude being reported, one way or another (off or mode
		// A). Only when FUSED and for tracked aircraft.
		addAlert("ISR", false, false)
	}

	return alerts
}
