// pkg/panes/stars/datablock.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/radar"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

type DatablockType int

const (
	PartialDatablock DatablockType = iota
	LimitedDatablock
	FullDatablock
	SuspendedDatablock
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
	field7 [2][8]dbChar
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
	nc := max(numVariants([][]dbChar{db.field34[0][:], db.field34[1][:], db.field34[2][:]}),
		numVariants([][]dbChar{db.field5[0][:], db.field5[1][:], db.field5[2][:]}))
	nc = max(nc, numVariants([][]dbChar{db.field6[0][:], db.field6[1][:]}))
	nc = max(nc, numVariants([][]dbChar{db.field7[0][:], db.field7[1][:]}))

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
// suspendedDatablock

type suspendedDatablock struct {
	// Line 0
	field0 [8]dbChar
}

func (db suspendedDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness STARSBrightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{dbMakeLine(db.field0[:])}
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
	pt[1] += float32(font.Size) // align leader with line 1
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

func (sp *STARSPane) datablockType(ctx *panes.Context, trk sim.Track) DatablockType {
	if trk.IsUnassociated() {
		// Must be limited, regardless of anything else.
		return LimitedDatablock
	} else {
		// The track owner is known, so it will be a P/FDB (or suspended)
		state := sp.TrackState[trk.ADSBCallsign]

		if trk.FlightPlan.Suspended {
			return SuspendedDatablock
		}

		beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(imgui.KeyF1) && ctx.Keyboard.KeyControl()
		if beaconator {
			// Partial is always full with the beaconator, so we're done
			// here in that case.
			return FullDatablock
		}

		if ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode {
			// 6-117
			return FullDatablock
		}

		sfp := trk.FlightPlan
		if sfp.TrackingController == ctx.UserTCP {
			// it's under our control
			return FullDatablock
		}

		if trk.HandingOffTo(ctx.UserTCP) {
			// it's being handed off to us
			return FullDatablock
		}

		if state.DisplayFDB {
			// Outbound handoff or we slewed a PDB to make it a FDB
			return FullDatablock
		}

		if sp.haveActiveWarnings(ctx, trk) {
			return FullDatablock
		}

		// Point outs are FDB until acked.
		if tcps, ok := sp.PointOuts[trk.FlightPlan.ACID]; ok && tcps.To == ctx.UserTCP {
			return FullDatablock
		}
		if state.PointOutAcknowledged {
			return FullDatablock
		}
		if state.ForceQL {
			return FullDatablock
		}

		if len(sfp.RedirectedHandoff.Redirector) > 0 {
			if sfp.RedirectedHandoff.RedirectedTo == ctx.UserTCP {
				return FullDatablock
			}
		}
		if sfp.RedirectedHandoff.OriginalOwner == ctx.UserTCP {
			return FullDatablock
		}

		if sp.currentPrefs().OverflightFullDatablocks && trk.IsOverflight() {
			return FullDatablock
		}

		if sp.isQuicklooked(ctx, trk) {
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

func (sp *STARSPane) getAllDatablocks(ctx *panes.Context, tracks []sim.Track) map[av.ADSBCallsign]datablock {
	sp.fdbArena.Reset()
	sp.pdbArena.Reset()
	sp.ldbArena.Reset()
	sp.sdbArena.Reset()

	m := make(map[av.ADSBCallsign]datablock)
	for _, trk := range tracks {
		color, brightness, _ := sp.trackDatablockColorBrightness(ctx, trk)
		m[trk.ADSBCallsign] = sp.getDatablock(ctx, trk, trk.FlightPlan, color, brightness)
	}
	return m
}

func (sp *STARSPane) getDatablock(ctx *panes.Context, trk sim.Track, sfp *sim.STARSFlightPlan,
	color renderer.RGB, brightness STARSBrightness) datablock {
	state := sp.TrackState[trk.ADSBCallsign]
	if state != nil && !sp.datablockVisible(ctx, trk) {
		return nil
	}

	// Check if the track is being handed off.
	//
	// handoffTCP is only set if it's coming from an enroute position or
	// from a different facility; otherwise we just show the
	// single-character id.
	handoffId, handoffTCP := " ", ""
	if sfp != nil {
		toTCP := util.Select(sfp.RedirectedHandoff.RedirectedTo != "",
			sfp.RedirectedHandoff.RedirectedTo, sfp.HandoffTrackController)
		inbound := toTCP == ctx.UserTCP

		if inbound {
			// Always show our id
			handoffId = toTCP[len(toTCP)-1:]
			if fromCtrl := ctx.Client.State.Controllers[sfp.TrackingController]; fromCtrl != nil {
				if fromCtrl.ERAMFacility { // Enroute controller
					// From any center
					handoffTCP = sfp.TrackingController
				} else if fromCtrl.FacilityIdentifier != "" {
					// Different facility; show full id of originator
					handoffTCP = fromCtrl.FacilityIdentifier + fromCtrl.TCP
				}
			}
		} else { // outbound
			if toCtrl := ctx.Client.State.Controllers[toTCP]; toCtrl != nil {
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
	if state != nil && handoffTCP == "" && ctx.Now.Before(state.AcceptedHandoffDisplayEnd) {
		handoffTCP = state.AcceptedHandoffSector
	}

	// Various other values that will be repeatedly useful below...
	beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(imgui.KeyF1) && ctx.Keyboard.KeyControl() && trk.ADSBCallsign != ""
	var actype string
	if sfp != nil {
		actype = sfp.AircraftType
	}
	squawkingSPC, _ := trk.Squawk.IsSPC()

	// Note: this is only for PDBs and FDBs. LDBs don't have pilot reported
	// altitude or inhibit mode C.
	var altitude string
	var pilotReportedAltitude bool
	if !trk.IsUnsupportedDB() {
		haveTransponderAltitude := trk.Mode == av.TransponderModeAltitude && (sfp == nil || !sfp.InhibitModeCAltitudeDisplay)
		if haveTransponderAltitude && state.UnreasonableModeC {
			altitude = "XXX"
		} else if haveTransponderAltitude {
			if trk.TransponderAltitude < 0 {
				altitude = fmt.Sprintf("N%02d", int(-trk.TransponderAltitude+50)/100)
			} else {
				altitude = fmt.Sprintf("%03d", int(trk.TransponderAltitude+50)/100)
			}
		} else if sfp != nil && sfp.PilotReportedAltitude != 0 {
			altitude = fmt.Sprintf("%03d", sfp.PilotReportedAltitude/100)
			pilotReportedAltitude = true
		} else if sfp != nil && sfp.InhibitModeCAltitudeDisplay {
			altitude = "***"
		} else if trk.Mode == av.TransponderModeStandby {
			altitude = "RDR"
		} else {
			// Display an empty field
			altitude = "   "
		}
	}

	displayBeaconCode := ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode

	groundspeed := fmt.Sprintf("%02d", int(trk.Groundspeed+5)/10)
	if state != nil {
		groundspeed = fmt.Sprintf("%02d", int(state.track.Groundspeed+5)/10)
	}
	beaconMismatch := trk.IsAssociated() && trk.Squawk != sfp.AssignedSquawk && !squawkingSPC && !trk.IsUnsupportedDB() &&
		trk.Mode != av.TransponderModeStandby

	// Figure out what to display for scratchpad 1 (used in both FDB and PDBs)
	sp1 := ""
	if sfp != nil {
		sp1 = sfp.Scratchpad

		// If it hasn't been set to something and the adapted scratchpad hasn't
		// been cleared, show an adapted one, if appropriate.
		if sp1 == "" && (state == nil || !state.ClearedScratchpadAlternate) {
			adapt := ctx.FacilityAdaptation
			falt := func() string {
				alt := sfp.RequestedAltitude
				if adapt.AllowLongScratchpad {
					return fmt.Sprintf("%03d", alt/100)
				} else {
					return fmt.Sprintf("%02d", alt/1000)
				}
			}
			shortExit := func() string {
				if e := sfp.ExitFix; e != "" {
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
				if e := sfp.ExitFix; e != "" {
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

			if trk.IsArrival() {
				// Note arrivalAirport is only set if it should be shown when there is no scratchpad set
				ap, ok := ctx.Client.State.Airports[trk.ArrivalAirport]
				if ok && !ap.OmitArrivalScratchpad {
					sp1 = sfp.ExitFix
				}
			} else {
				if adapt.Scratchpad1.DisplayExitFix {
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
		}
	}

	switch sp.datablockType(ctx, trk) {
	case LimitedDatablock:
		db := sp.ldbArena.AllocClear()

		// Field 0: CA, MCI, and squawking special codes
		alerts := sp.getDatablockAlerts(ctx, trk, LimitedDatablock)
		copy(db.field0[:], alerts[:])

		extended := state.FullLDBEndTime.After(ctx.Now)
		sqspc, _ := trk.Squawk.IsSPC()
		extended = extended || (trk.Mode != av.TransponderModeStandby && sqspc)

		who := trk.MissingFlightPlan && !state.MissingFlightPlanAcknowledged

		if len(alerts) == 0 && trk.Mode == av.TransponderModeOn && !extended {
			return nil
		}

		ps := sp.currentPrefs()
		if trk.Mode != av.TransponderModeStandby {
			mci := !ps.DisableMCIWarnings && slices.ContainsFunc(sp.MCIAircraft, func(mci CAAircraft) bool {
				trk0, ok := ctx.GetTrackByCallsign(mci.ADSBCallsigns[0])
				return ok && trk0.IsAssociated() && trk0.FlightPlan.MCISuppressedCode != trk.Squawk &&
					mci.ADSBCallsigns[1] == trk.ADSBCallsign
			})

			if mci || beaconator || who || extended || trk.Ident || ps.DisplayLDBBeaconCodes ||
				state.DisplayLDBBeaconCode || displayBeaconCode {
				// Field 1: reported beacon code
				// TODO: Field 1: WHO if unassociated and no flight plan
				var f1 int
				if displayBeaconCode { // flashing yellow
					f1 = formatDBText(db.field1[:], trk.Squawk.String(), brightness.ScaleRGB(STARSTextWarningColor), true)
				} else {
					f1 = formatDBText(db.field1[:], trk.Squawk.String(), color, false)
				}
				if who {
					formatDBText(db.field1[f1:], "WHO", color, true)
				} else if trk.Ident {
					// Field 1: flashing ID after beacon code if ident.
					formatDBText(db.field1[f1:], "ID", color, true)
				}
			}
		}

		// Field 3: mode C altitude
		altitude := fmt.Sprintf("%03d", int(trk.TransponderAltitude+50)/100)
		if trk.TransponderAltitude < 0 {
			altitude = fmt.Sprintf("N%02d", int(-trk.TransponderAltitude+50)/100)
		}
		if trk.Mode == av.TransponderModeStandby {
			if extended {
				altitude = "RDR"
			} else {
				altitude = ""
			}
		} else if trk.Mode == av.TransponderModeOn { // mode-a; altitude is blank
			altitude = ""
		}

		formatDBText(db.field3[:], altitude, color, false)

		if extended {
			// Field 5: groundspeed
			formatDBText(db.field5[:], groundspeed, color, false)
		}

		if (extended || beaconator) && trk.Mode != av.TransponderModeStandby {
			// Field 6: ACID
			formatDBText(db.field6[:], string(trk.ADSBCallsign), color, false)
		}

		return db

	case PartialDatablock:
		fa := ctx.FacilityAdaptation
		db := sp.pdbArena.AllocClear()

		// Field0: TODO cautions in yellow
		// TODO: 2-69 doesn't list CA/MCI, so should this be blank even in
		// those cases? (Note that SPC upgrades partial to full datablocks.)
		alerts := sp.getDatablockAlerts(ctx, trk, PartialDatablock)
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
		if pilotReportedAltitude {
			formatDBText(db.field12[0][:], fmt1(altitude+"*"), color, false)
		} else {
			formatDBText(db.field12[0][:], fmt1(altitude)+handoffId, color, false)
		}
		f12Idx := 1
		if sp1 != "" {
			formatDBText(db.field12[1][:], fmt1(sp1)+handoffId, color, false)
			f12Idx++
		}
		if fa.PDB.ShowScratchpad2 && sfp.SecondaryScratchpad != "" {
			formatDBText(db.field12[f12Idx][:], fmt1(sfp.SecondaryScratchpad)+"+", color, false)
		}

		// Field 3: by default, groundspeed and/or "V" for VFR, "E" for overflight, followed by CWT,
		// but may be adapted.
		rulesCategory := " "
		if sfp.Rules == av.FlightRulesVFR {
			rulesCategory = "V"
		} else if sfp.TypeOfFlight == av.FlightTypeOverflight {
			rulesCategory = "E"
		}
		cwt := util.Select(sfp.CWTCategory != "", sfp.CWTCategory, " ")
		if fa.PDB.SplitGSAndCWT {
			// [GS, CWT] timesliced
			formatDBText(db.field3[0][:], groundspeed, color, false)
			formatDBText(db.field3[1][:], rulesCategory+cwt, color, false)
		} else {
			if fa.PDB.HideGroundspeed {
				// [CWT]
				formatDBText(db.field3[0][:], rulesCategory+cwt, color, false)
			} else {
				// [GS CWT]
				formatDBText(db.field3[0][:], groundspeed+rulesCategory+cwt, color, false)
			}
			if fa.PDB.ShowAircraftType {
				// [ACTYPE]
				formatDBText(db.field3[1][:], actype, color, false)
			}
		}

		// Field 4: ident
		if trk.Ident {
			formatDBText(db.field4[:], "ID", color, true)
		}

		return db

	case FullDatablock:
		fa := ctx.FacilityAdaptation
		db := sp.fdbArena.AllocClear()

		// Line 0
		// Field 0: special conditions, safety alerts (red), cautions (yellow)
		alerts := sp.getDatablockAlerts(ctx, trk, FullDatablock)
		copy(db.field0[:], alerts[:])

		// Line 1
		// Field 1: ACID (or squawk if beaconator)
		if beaconator && trk.Mode != av.TransponderModeStandby {
			formatDBText(db.field1[:], trk.Squawk.String(), color, false)
		} else {
			formatDBText(db.field1[:], string(sfp.ACID), color, false)
		}

		// Field 2: various symbols for inhibited stuff
		if state != nil { // FIXME: these should live in STARSFlightPlan
			if state.InhibitMSAW || sfp.DisableMSAW {
				if sfp.DisableCA {
					formatDBText(db.field2[:], "+", color, false)
				} else {
					formatDBText(db.field2[:], "*", color, false)
				}
			} else if sfp.DisableCA || sfp.MCISuppressedCode != 0 {
				formatDBText(db.field2[:], STARSTriangleCharacter, color, false)
			}
		}

		// Field 8: point out, rejected pointout, redirected
		// handoffs... Some flash, some don't.
		if state != nil {
			if tcps, ok := sp.PointOuts[sfp.ACID]; ok && tcps.To == ctx.UserTCP {
				formatDBText(db.field8[:], "PO", color, false)
			} else if ok && tcps.From == ctx.UserTCP {
				id := tcps.To
				if len(id) > 1 && id[0] >= '0' && id[0] <= '9' {
					id = id[1:]
				}
				formatDBText(db.field8[:], "PO"+id, color, false)
			} else if ctx.Now.Before(state.UNFlashingEndTime) {
				formatDBText(db.field8[:], "UN", color, true)
			} else if state.POFlashingEndTime.After(ctx.Now) {
				formatDBText(db.field8[:], "PO", color, true)
			} else if sfp.RedirectedHandoff.ShowRDIndicator(ctx.UserTCP, state.RDIndicatorEnd) {
				formatDBText(db.field8[:], "RD", color, false)
			}
		}

		// Line 2
		// Fields 3 and 4: 3 is altitude plus possibly other stuff; 4 is
		// special indicators, possible associated with 3, so they're a
		// single field
		field3Length := util.Select(fa.AllowLongScratchpad, 4, 3)
		fmt3 := func(s string) string {
			for len([]rune(s)) < field3Length {
				s += " "
			}
			return s
		}

		idx34 := 0
		if altitude != "" || handoffId != " " {
			if pilotReportedAltitude {
				formatDBText(db.field34[idx34][:], fmt3(altitude+"*"), color, false)
				idx34++
			} else {
				formatDBText(db.field34[idx34][:], fmt3(altitude)+handoffId, color, false)
				idx34++
			}
		}
		if sp1 != "" {
			formatDBText(db.field34[idx34][:], fmt3(sp1)+handoffId, color, false)
			idx34++
		}
		if handoffTCP != "" && !fa.DisplayHOFacilityOnly {
			formatDBText(db.field34[idx34][:], fmt3(handoffTCP)+handoffId, color, false)
		} else if sfp.SecondaryScratchpad != "" && !ctx.FacilityAdaptation.FDB.Scratchpad2OnLine3 { // don't show secondary if we're showing a center
			// TODO: confirm no handoffId here
			formatDBText(db.field34[idx34][:], fmt3(sfp.SecondaryScratchpad)+"+", color, false)
		}

		// Field 5: groundspeed
		rulesCategory := " "
		if sfp.Rules == av.FlightRulesVFR {
			rulesCategory = "V"
		} else if sfp.TypeOfFlight == av.FlightTypeOverflight {
			rulesCategory = "E"
		}
		rulesCategory += util.Select(sfp.CWTCategory != "", sfp.CWTCategory, " ")
		rulesCategory += " "

		field5Idx := 0
		// 6-107 force display / inhibit ac type display
		inhibitACType := sfp.InhibitACTypeDisplay ||
			(state != nil && state.InhibitACTypeDisplay != nil && *state.InhibitACTypeDisplay)
		forceACType := ctx.Now.Before(sfp.ForceACTypeDisplayEndTime) ||
			(state != nil && ctx.Now.Before(state.ForceACTypeDisplayEndTime))
		if state != nil && !forceACType && !trk.IsUnsupportedDB() {
			if state.IFFlashing {
				if trk.Ident {
					formatDBText(db.field5[0][:], "IF"+"ID", color, true)
				} else {
					formatDBText(db.field5[0][:], "IF"+rulesCategory, color, true)
				}
			} else {
				gs := util.Select(sfp.HoldState, "HL", groundspeed)
				idx := formatDBText(db.field5[0][:], gs, color, false)
				if trk.Ident {
					formatDBText(db.field5[0][idx:], "ID", color, true)
				} else {
					formatDBText(db.field5[0][idx:], rulesCategory, color, false)
				}
			}
			field5Idx++
		}
		// Field 5: +aircraft type and possibly requested altitude, if not
		// identing.
		if !trk.Ident {
			if actype != "" && !inhibitACType {
				rnav := util.Select(sfp.RNAV, "^", " ")
				formatDBText(db.field5[field5Idx][:], actype+rnav, color, false)
				field5Idx++
			}

			if !forceACType && (state == nil || (state.DisplayRequestedAltitude != nil && *state.DisplayRequestedAltitude) ||
				(state.DisplayRequestedAltitude == nil && sp.DisplayRequestedAltitude)) {
				if alt := sfp.RequestedAltitude; alt != 0 {
					// FIXME: 2-67: with 2-char TCPs, the "R" goes in the
					// second place in field 4 when requested altitude is
					// being displayed--i.e., it is always in the 5th
					// column of this row of the datablock.
					formatDBText(db.field5[field5Idx][:], fmt.Sprintf("R%03d ", alt/100), color, false)
					field5Idx++
				}
			}
		}

		// Field 6: ATPA info and possibly beacon code; doesn't apply to unsupported DB
		idx6 := 0
		if !trk.IsUnsupportedDB() {
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
				if sfp.CWTCategory == "" {
					formatDBText(db.field6[idx6][:], "NOWGT", distColor, false)
				} else {
					formatDBText(db.field6[idx6][:], fmt.Sprintf("%.2f", state.IntrailDistance), distColor, false)
				}
				idx6++
			}
			if displayBeaconCode {
				formatDBText(db.field6[idx6][:], trk.Squawk.String(), brightness.ScaleRGB(STARSTextWarningColor), true)
				idx6++
			} else if beaconMismatch {
				formatDBText(db.field6[idx6][:], trk.Squawk.String(), color, false)
				idx6++
			} else if _, ok := sp.DuplicateBeacons[trk.Squawk]; ok && state.DBAcknowledged != trk.Squawk {
				formatDBText(db.field6[idx6][:], "DB", color, false)
				idx6++
			}
		}

		// Field 7: assigned altitude, assigned beacon if mismatch, secondary scratchpad on line 3 if enabled
		if ctx.FacilityAdaptation.FDB.Scratchpad2OnLine3 {
			altSet := sfp.AssignedAltitude != 0
			sp2 := sfp.SecondaryScratchpad
			sp2Set := sp2 != ""

			if altSet {
				leaderLineDirection := sp.getLeaderLineDirection(ctx, trk)
				altText := fmt.Sprintf("A%03d", sfp.AssignedAltitude/100)
				startAltIdx := 0
				if leaderLineDirection < math.South {
					startAltIdx = 1 // N–SE leader → indent one char
				}
				for i, ch := range altText {
					if startAltIdx+i >= len(db.field7[0]) {
						break
					}
					db.field7[0][startAltIdx+i] = dbChar{ch: ch, color: color, flashing: false}
				}
			}

			if sp2Set {
				leaderLineDirection := sp.getLeaderLineDirection(ctx, trk)
				runes := []rune(sp2)
				if len(runes) > 3 {
					runes = runes[:4]
				}
				for len(runes) < 4 {
					runes = append(runes, ' ')
				}
				text := string(runes) + "+"

				startIdx := 0
				if leaderLineDirection >= math.South {
					startIdx = 5 - len([]rune(text)) // char 6
				} else {
					startIdx = 2
				}

				targetField := util.Select(altSet, 1, 0) // if no alt, show spad2 in slot 0
				formatDBText(db.field7[targetField][startIdx:], text, color, false)
			}
		} else {
			// === Default behavior: Assigned altitude + squawk mismatch ===
			if alt := sfp.AssignedAltitude; alt != 0 {
				formatDBText(db.field7[0][:], fmt.Sprintf("A%03d", alt/100), color, false)
			}
			if beaconMismatch {
				idx := util.Select(fieldEmpty(db.field7[0][:]), 0, 1)
				formatDBText(db.field7[idx][:], sfp.AssignedSquawk.String(), color, true)
			}
		}

		return db

	case SuspendedDatablock:
		db := sp.sdbArena.AllocClear()

		s := strconv.Itoa(sfp.CoastSuspendIndex)
		if sp.currentPrefs().DisplaySuspendedTrackAltitude ||
			state.SuspendedShowAltitudeEndTime.After(ctx.Now) && trk.Mode == av.TransponderModeAltitude {
			s += " " + altitude
		}
		formatDBText(db.field0[:], s, color, false)
		return db
	}

	return nil
}

func (sp *STARSPane) getGhostDatablock(ctx *panes.Context, ghost *av.GhostTrack, color renderer.RGB) ghostDatablock {
	var db ghostDatablock

	state := sp.TrackState[ghost.ADSBCallsign]
	trk, ok := ctx.GetTrackByCallsign(ghost.ADSBCallsign)
	cwt := ""
	if ok && trk.IsAssociated() {
		cwt = trk.FlightPlan.CWTCategory
	}
	groundspeed := fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)
	if state.Ghost.PartialDatablock {
		// Partial datablock is just airspeed and then aircraft CWT type
		formatDBText(db.field0[:], groundspeed+cwt, color, false)
	} else {
		// The full datablock ain't much more...
		formatDBText(db.field0[:], string(ghost.ADSBCallsign), color, false)
		formatDBText(db.field1[:], groundspeed, color, false) // TODO: no CWT?
	}

	return db
}

func (sp *STARSPane) trackDatablockColorBrightness(ctx *panes.Context, trk sim.Track) (color renderer.RGB, dbBrightness, posBrightness STARSBrightness) {
	ps := sp.currentPrefs()
	dt := sp.datablockType(ctx, trk)
	state := sp.TrackState[trk.ADSBCallsign]

	// Cases where it's always a full datablock
	inboundPointOut := false
	forceFDB := false
	if trk.IsAssociated() {
		if tcps, ok := sp.PointOuts[trk.FlightPlan.ACID]; ok && tcps.To == ctx.UserTCP {
			forceFDB = true
			inboundPointOut = true
		} else {
			forceFDB = forceFDB || (state.OutboundHandoffAccepted && ctx.Now.Before(state.OutboundHandoffFlashEnd))
			forceFDB = forceFDB || trk.HandingOffTo(ctx.UserTCP)
		}
	}

	// Figure out the datablock and position symbol brightness first
	if trk.ADSBCallsign == sp.dwellAircraft { // dwell overrides everything as far as brightness
		dbBrightness = STARSBrightness(100)
		posBrightness = STARSBrightness(100)
	} else if forceFDB || state.OutboundHandoffAccepted {
		dbBrightness = ps.Brightness.FullDatablocks
		posBrightness = ps.Brightness.Positions
	} else if dt == PartialDatablock || dt == LimitedDatablock {
		dbBrightness = ps.Brightness.LimitedDatablocks
		posBrightness = ps.Brightness.LimitedDatablocks
	} else /* dt == FullDatablock */ {
		if trk.FlightPlan.TrackingController != ctx.UserTCP {
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

	if state.IsSelected {
		// middle button selected
		color = STARSSelectedAircraftColor
	} else if trk.IsUnassociated() {
		color = STARSUntrackedAircraftColor
	} else {
		sfp := trk.FlightPlan
		if _, ok := sp.ForceQLACIDs[sfp.ACID]; ok {
			// Check if we're the controller being ForceQL
			color = STARSTrackAlertColor
		} else if state.PointOutAcknowledged || state.ForceQL {
			// Ack'ed point out to us (but not cleared) or force quick look.
			color = STARSTrackAlertColor
		} else if inboundPointOut {
			// Pointed out to us.
			color = STARSTrackAlertColor
		} else if state.DatablockAlert {
			color = STARSTrackAlertColor
		} else if sfp.TrackingController == ctx.UserTCP { //change
			// we own the track
			color = STARSTrackedAircraftColor
		} else if sfp.RedirectedHandoff.OriginalOwner == ctx.UserTCP ||
			sfp.RedirectedHandoff.RedirectedTo == ctx.UserTCP {
			color = STARSTrackedAircraftColor
		} else if sfp.HandoffTrackController == ctx.UserTCP &&
			!slices.Contains(sfp.RedirectedHandoff.Redirector, ctx.UserTCP) {
			// flashing white if it's being handed off to us.
			color = STARSTrackedAircraftColor
		} else if state.OutboundHandoffAccepted {
			// we handed it off, it was accepted, but we haven't yet acknowledged
			color = STARSTrackedAircraftColor
		} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
			// quick look all plus
			color = STARSTrackedAircraftColor
		} else if slices.ContainsFunc(ps.QuickLookPositions,
			func(q QuickLookPosition) bool { return q.Id == sfp.TrackingController && q.Plus }) {
			// individual quicklook plus controller
			color = STARSTrackedAircraftColor
		} else {
			// other controller owns, no special case applies
			color = STARSUntrackedAircraftColor
		}
	}

	return
}

func (sp *STARSPane) datablockVisible(ctx *panes.Context, trk sim.Track) bool {
	state := sp.TrackState[trk.ADSBCallsign]

	af := sp.currentPrefs().AltitudeFilters

	if ctx.Now.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode {
		// beacon code display 6-117
		return true
	}

	if trk.IsUnassociated() {
		if ctx.Now.Before(state.FullLDBEndTime) {
			return true
		}
		if trk.IsTentative {
			return false
		}
		if trk.MissingFlightPlan {
			return true // WHO
		}
		if trk.Mode == av.TransponderModeStandby {
			return false
		}
		if trk.Mode == av.TransponderModeAltitude {
			// Check altitude filters
			return int(trk.TransponderAltitude) >= af.Unassociated[0] && int(trk.TransponderAltitude) <= af.Unassociated[1]
		}
		return true
	} else { // associated
		state := sp.TrackState[trk.ADSBCallsign]
		sfp := trk.FlightPlan

		if sfp.TrackingController == ctx.UserTCP {
			// For owned datablocks
			return true
		} else if sfp.HandoffTrackController == ctx.UserTCP {
			// For receiving handoffs
			return true
		} else if state.PointOutAcknowledged {
			// Pointouts: This is if its been accepted,
			// for an incoming pointout, it falls to the FDB check
			return true
		} else if ok, _ := trk.Squawk.IsSPC(); ok {
			// Special purpose codes
			return true
		} else if state.DisplayFDB {
			// For non-greened handoffs
			return true
		} else if trk.IsOverflight() && sp.currentPrefs().OverflightFullDatablocks { //Need a f7 + e
			// Overflights
			return true
		} else if sp.isQuicklooked(ctx, trk) {
			return true
		} else if sfp.RedirectedHandoff.RedirectedTo == ctx.UserTCP {
			// Redirected to
			return true
		} else if slices.Contains(sfp.RedirectedHandoff.Redirector, ctx.UserTCP) {
			// Had it but redirected it
			return true
		} else if trk.IsUnsupportedDB() {
			return true
		} else if trk.Mode == av.TransponderModeAltitude {
			// Check altitude filters
			return int(trk.TransponderAltitude) >= af.Associated[0] && int(trk.TransponderAltitude) <= af.Associated[1]
		}
		return true
	}
}

func (sp *STARSPane) drawDatablocks(tracks []sim.Track, dbs map[av.ADSBCallsign]datablock, ctx *panes.Context,
	transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	realNow := ctx.Now // for flashing rate...
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Datablocks)

	// Partition them by DB type so we can draw FDBs last
	var ldbs, pdbs, sdbs, fdbs []sim.Track

	for _, trk := range tracks {
		if !sp.datablockVisible(ctx, trk) {
			continue
		}

		switch sp.datablockType(ctx, trk) {
		case LimitedDatablock:
			ldbs = append(ldbs, trk)
		case PartialDatablock:
			pdbs = append(pdbs, trk)
		case FullDatablock:
			fdbs = append(fdbs, trk)
		case SuspendedDatablock:
			sdbs = append(sdbs, trk)
		}
	}

	if sp.dwellAircraft != "" {
		// If the dwelled aircraft is in the given slice, move it to the
		// end so that it is drawn last (among its datablock category) and
		// appears on top.
		moveDwelledToEnd := func(tracks []sim.Track) []sim.Track {
			isDwelled := func(trk sim.Track) bool { return trk.ADSBCallsign == sp.dwellAircraft }
			if idx := slices.IndexFunc(tracks, isDwelled); idx != -1 {
				tracks = append(tracks, tracks[idx])
				tracks = append(tracks[:idx], tracks[idx+1:]...)
			}
			return tracks
		}

		ldbs = moveDwelledToEnd(ldbs)
		pdbs = moveDwelledToEnd(pdbs)
		fdbs = moveDwelledToEnd(fdbs)
		sdbs = moveDwelledToEnd(sdbs)
	}

	leaderLineEndpoint := func(pw [2]float32, trk sim.Track, leaderLineDirection math.CardinalOrdinalDirection) [2]float32 {
		var vll [2]float32
		suspended := trk.IsAssociated() && trk.FlightPlan.Suspended
		if !suspended {
			vll = sp.getLeaderLineVector(ctx, leaderLineDirection)
		}
		pll := math.Add2f(pw, math.Scale2f(vll, ctx.DrawPixelScale))

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
		return pll
	}

	var strBuilder strings.Builder
	for _, dbTrack := range [][]sim.Track{ldbs, pdbs, sdbs, fdbs} {
		for _, trk := range dbTrack {
			db := dbs[trk.ADSBCallsign]
			if db == nil {
				continue
			}

			_, brightness, _ := sp.trackDatablockColorBrightness(ctx, trk)
			if brightness == 0 {
				continue
			}

			// Calculate the endpoint of the leader line and hence where to
			// start drawing the datablock.
			state := sp.TrackState[trk.ADSBCallsign]
			pac := transforms.WindowFromLatLongP(state.track.Location)
			leaderLineDirection := sp.getLeaderLineDirection(ctx, trk)
			pll := leaderLineEndpoint(pac, trk, leaderLineDirection)

			halfSeconds := realNow.UnixMilli() / 500
			db.draw(td, pll, font, &strBuilder, brightness, leaderLineDirection, halfSeconds)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) haveActiveWarnings(ctx *panes.Context, trk sim.Track) bool {
	ps := sp.currentPrefs()
	state := sp.TrackState[trk.ADSBCallsign]

	// Only this applies to unassociated tracks(?)
	if ok, _ := trk.Squawk.IsSPC(); ok {
		return true
	}
	if trk.IsUnassociated() {
		return false
	}
	sfp := trk.FlightPlan

	if state.MSAW && !state.InhibitMSAW && !sfp.DisableMSAW && !ps.DisableMSAW {
		return true
	}
	if trk.IsAssociated() {
		if spc := sfp.SPCOverride; spc != "" && av.StringIsSPC(spc) /* only alerts, not custom warning SPCs */ {
			return true
		}
	}
	if !ps.DisableCAWarnings && !sfp.DisableCA &&
		slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				return ca.ADSBCallsigns[0] == trk.ADSBCallsign || ca.ADSBCallsigns[1] == trk.ADSBCallsign
			}) ||
		slices.ContainsFunc(sp.MCIAircraft,
			func(ca CAAircraft) bool {
				trk0, ok := ctx.GetTrackByCallsign(ca.ADSBCallsigns[0])
				return ok && ca.ADSBCallsigns[0] == trk.ADSBCallsign &&
					trk0.Squawk != sfp.MCISuppressedCode
			}) {
		return true
	}
	if _, warn := sp.WarnOutsideAirspace(ctx, trk); warn {
		return true
	}

	return false
}

func (sp *STARSPane) getDatablockAlerts(ctx *panes.Context, trk sim.Track, dbtype DatablockType) []dbChar {
	if trk.IsUnsupportedDB() {
		return nil
	}

	ps := sp.currentPrefs()
	state := sp.TrackState[trk.ADSBCallsign]

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
				if mci.ADSBCallsigns[0] != trk.ADSBCallsign && mci.ADSBCallsigns[1] != trk.ADSBCallsign {
					return false
				}
				trk0, ok0 := ctx.GetTrackByCallsign(mci.ADSBCallsigns[0])
				trk1, ok1 := ctx.GetTrackByCallsign(mci.ADSBCallsigns[1])

				if ok0 && ok1 && trk0.IsAssociated() && trk0.FlightPlan.MCISuppressedCode == trk1.Squawk {
					return false
				}
				return true
			}); idx != -1 {
				addAlert("CA", !sp.MCIAircraft[idx].Acknowledged, true)
			}
		}
		if ok, code := trk.Squawk.IsSPC(); ok && trk.Mode != av.TransponderModeStandby {
			addAlert(code, !state.SPCAcknowledged, true)
		}
	}
	if dbtype == LimitedDatablock {
		// That's all folks
		return alerts
	}

	sfp := trk.FlightPlan
	if dbtype == FullDatablock {
		if state.MSAW && !state.InhibitMSAW && !sfp.DisableMSAW && !ps.DisableMSAW {
			addAlert("LA", !state.MSAWAcknowledged, true)
		}
		if spc := sfp.SPCOverride; spc != "" {
			// squawked SPC takes priority
			if sqspc, _ := trk.Squawk.IsSPC(); !sqspc || trk.Mode == av.TransponderModeStandby {
				red := av.StringIsSPC(spc)            // std ones are red, adapted ones are yellow.
				addAlert(sfp.SPCOverride, false, red) // controller-added SPC doesn't flash
			}
		}
		if !ps.DisableCAWarnings && !sfp.DisableCA {
			if idx := slices.IndexFunc(sp.CAAircraft,
				func(ca CAAircraft) bool {
					return ca.ADSBCallsigns[0] == trk.ADSBCallsign || ca.ADSBCallsigns[1] == trk.ADSBCallsign
				}); idx != -1 {
				addAlert("CA", !sp.CAAircraft[idx].Acknowledged, true)
			}
		}
		if alts, warn := sp.WarnOutsideAirspace(ctx, trk); warn {
			altStrs := ""
			for _, a := range alts {
				altStrs += fmt.Sprintf("/%d-%d", a[0]/100, a[1]/100)
			}
			addAlert("AS"+altStrs, false, true)
		}
	} else if dbtype == PartialDatablock {
		fa := ctx.FacilityAdaptation

		if sfp.SPCOverride != "" && fa.PDB.DisplayCustomSPCs {
			// We only care about adapted alerts
			if slices.Contains(fa.CustomSPCs, sfp.SPCOverride) {
				addAlert(sfp.SPCOverride, !state.SPCAcknowledged, false)
			}
		}
	}

	// Both FDB and PDB
	if sp.radarMode(ctx.FacilityAdaptation.RadarSites) == RadarModeFused &&
		sfp.PilotReportedAltitude == 0 &&
		(trk.Mode != av.TransponderModeAltitude || sfp.InhibitModeCAltitudeDisplay) {
		// No altitude being reported, one way or another (off or mode
		// A). Only when FUSED and for tracked aircraft.
		addAlert("ISR", false, false)
	}

	return alerts
}
