// stars/datablock.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

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
	// pt is end of leader line--attachment point.
	// clockPhase is the current clock phase (1-4) for timeshared field selection.
	// halfSeconds is used for the 500ms blink cycle.
	draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
		brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int,
		halfSeconds int64)
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
	// line 0: static - not affected by clock phase
	field0 [16]dbChar
	// line 1: static - not affected by clock phase
	field1 [7]dbChar
	field2 [1]dbChar
	field8 [4]dbChar
	// line 2: indexed by clock phase 1-4 (0-based)
	field34 [4][5]dbChar // altitude/scratchpad/handoff
	field5  [4][7]dbChar // speed/actype/reqalt
	// line 3: indexed by clock phase 1-4 (0-based)
	field6 [4][5]dbChar // ATPA/beacon
	field7 [4][8]dbChar // assigned alt/sp2
}

// hasAlertInField0 checks if the given two-character alert string (e.g. "LA"
// or "CA") appears in the already-populated field0 (line 0 alerts).
func (db *fullDatablock) hasAlertInField0(a, b rune) bool {
	for i := 0; i+1 < len(db.field0); i++ {
		if db.field0[i].ch == a && db.field0[i+1].ch == b {
			return true
		}
	}
	return false
}

func (db fullDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int, halfSeconds int64) {
	idx := clockPhase - 1
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(dbChopTrailing(db.field1[:]), db.field2[:], db.field8[:]),
		dbMakeLine(dbChopTrailing(db.field34[idx][:]), db.field5[idx][:]),
		dbMakeLine(db.field6[idx][:], db.field7[idx][:]),
	}
	pt[1] += float32(font.Size) // align leader with line 1
	dbDrawLines(lines, td, pt, font, strBuilder, brightness, leaderLineDirection, halfSeconds)
}

///////////////////////////////////////////////////////////////////////////
// partialDatablock

type partialDatablock struct {
	// line 0
	field0 [16]dbChar
	// line 1: field12 indexed by clock phase 1-4 (0-based)
	field12         [4][5]dbChar
	field3Primary   [4]dbChar // GS+CWT or rules+CWT depending on adaptation
	field3Alternate [4]dbChar // actype or split CWT; empty if not adapted
	field4          [2]dbChar
}

func (db partialDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font, strBuilder *strings.Builder,
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int, halfSeconds int64) {
	f3 := db.field3Primary[:]
	if clockPhase == 2 && !fieldEmpty(db.field3Alternate[:]) {
		f3 = db.field3Alternate[:]
	}

	idx := clockPhase - 1
	lines := []dbLine{
		dbMakeLine(db.field0[:]),
		dbMakeLine(dbChopTrailing(db.field12[idx][:]), f3, db.field4[:]),
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
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int, halfSeconds int64) {
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
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int, halfSeconds int64) {
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
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, clockPhase int, halfSeconds int64) {
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
	brightness radar.Brightness, leaderLineDirection math.CardinalOrdinalDirection, halfSeconds int64) {
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
	brightness radar.Brightness, halfSeconds int64) {
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
		anno := sp.annotationsForTrack(ctx, trk)

		if trk.FlightPlan.Suspended {
			return SuspendedDatablock
		}

		beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(imgui.KeyF1) && ctx.Keyboard.KeyControl()
		if beaconator {
			// Partial is always full with the beaconator, so we're done
			// here in that case.
			return FullDatablock
		}

		if ctx.SimTime.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode {
			// 6-117
			return FullDatablock
		}

		sfp := trk.FlightPlan
		if ctx.UserOwnsFlightPlan(sfp) {
			// it's under our control
			return FullDatablock
		}

		if ctx.IsHandoffToUser(&trk) {
			// it's being handed off to us
			return FullDatablock
		}

		if sp.annotations(ctx, trk.ADSBCallsign).DisplayFDB {
			// Outbound handoff or we slewed a PDB to make it a FDB
			return FullDatablock
		}

		if sp.haveActiveWarnings(ctx, trk) {
			return FullDatablock
		}

		// Point outs are FDB until acked.
		if tcps, ok := sp.PointOuts[trk.FlightPlan.ACID]; ok && ctx.UserControlsPosition(tcps.To) {
			return FullDatablock
		}
		if anno.PointOutAcknowledged {
			return FullDatablock
		}
		if anno.ForceQL {
			return FullDatablock
		}

		if len(sfp.RedirectedHandoff.Redirector) > 0 {
			if ctx.UserControlsPosition(sfp.RedirectedHandoff.RedirectedTo) {
				return FullDatablock
			}
		}
		if ctx.UserControlsPosition(sfp.RedirectedHandoff.OriginalOwner) {
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

func (sp *STARSPane) getAllDatablocks(ctx *panes.Context) map[av.ADSBCallsign]datablock {
	sp.fdbArena.Reset()
	sp.pdbArena.Reset()
	sp.ldbArena.Reset()
	sp.sdbArena.Reset()

	m := make(map[av.ADSBCallsign]datablock)
	for _, trk := range sp.visibleTracks {
		color, brightness, _ := sp.trackDatablockColorBrightness(ctx, trk)
		m[trk.ADSBCallsign] = sp.getDatablock(ctx, trk, trk.FlightPlan, color, brightness)
	}
	return m
}

func (sp *STARSPane) getDatablock(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan,
	color renderer.RGB, brightness radar.Brightness) datablock {
	state := sp.TrackState[trk.ADSBCallsign]
	if state != nil && !sp.datablockVisible(ctx, trk) {
		return nil
	}

	handoffId, handoffTCP := sp.resolveHandoff(ctx, sfp, sp.annotationsForTrack(ctx, trk))

	// Various other values that will be repeatedly useful below...
	beaconator := ctx.Keyboard != nil && ctx.Keyboard.IsFKeyHeld(imgui.KeyF1) && ctx.Keyboard.KeyControl() && trk.ADSBCallsign != ""
	var actype string
	if sfp != nil {
		actype = sfp.AircraftType
	}
	squawkingSPC, _ := trk.Squawk.IsSPC()

	altitude, pilotReportedAltitude := formatAltitude(trk, sfp,
		trk.UnreasonableModeC)

	displayBeaconCode := ctx.SimTime.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode

	groundspeed := fmt.Sprintf("%02d", int(trk.Groundspeed+5)/10)
	if state != nil {
		groundspeed = fmt.Sprintf("%02d", int(state.track.Groundspeed+5)/10)
	}
	beaconMismatch := trk.IsAssociated() && trk.Squawk != sfp.AssignedSquawk && !squawkingSPC && !trk.IsUnsupportedDB() &&
		trk.Mode != av.TransponderModeStandby

	sp1 := sp.resolveScratchpad1(ctx, trk, sfp, state)

	switch sp.datablockType(ctx, trk) {
	case LimitedDatablock:
		if ldb := sp.buildLimitedDatablock(ctx, trk, color, brightness,
			beaconator, displayBeaconCode, groundspeed); ldb != nil {
			return ldb
		}
		return nil

	case PartialDatablock:
		return sp.buildPartialDatablock(ctx, trk, sfp, color,
			altitude, sp1, groundspeed, handoffId, actype,
			pilotReportedAltitude)

	case FullDatablock:
		return sp.buildFullDatablock(ctx, trk, sfp, color, brightness,
			altitude, sp1, groundspeed, handoffId, handoffTCP, actype,
			pilotReportedAltitude, beaconator, beaconMismatch,
			displayBeaconCode)

	case SuspendedDatablock:
		db := sp.sdbArena.AllocClear()

		s := strconv.Itoa(sfp.CoastSuspendIndex)
		anno := sp.annotationsForTrack(ctx, trk)
		if sp.currentPrefs().DisplaySuspendedTrackAltitude ||
			anno.SuspendedShowAltitudeEndTime.After(ctx.SimTime) && trk.Mode == av.TransponderModeAltitude {
			s += " " + altitude
		}
		formatDBText(db.field0[:], s, color, false)
		return db
	}

	return nil
}

func (sp *STARSPane) resolveHandoff(ctx *panes.Context, sfp *sim.NASFlightPlan,
	anno sim.TrackAnnotations) (handoffId, handoffTCP string) {
	handoffId = " "
	if sfp != nil {
		toTCP := util.Select(sfp.RedirectedHandoff.RedirectedTo != "",
			sfp.RedirectedHandoff.RedirectedTo, sfp.HandoffController)

		shortFID := func(ctrl *av.Controller) string {
			return ctrl.FacilityIdentifier
		}

		if ctx.UserControlsPosition(toTCP) { // inbound
			// Show the resolved controller's id (handles consolidated positions)
			if toCtrl := ctx.GetResolvedController(toTCP); toCtrl != nil {
				handoffId = toCtrl.Position[len(toCtrl.Position)-1:]
			} else {
				handoffId = string(toTCP[len(toTCP)-1:])
			}

			tcp := ctx.PrimaryTCPForTCW(sfp.OwningTCW)
			if fromCtrl := ctx.Client.State.Controllers[tcp]; fromCtrl != nil {
				if fromCtrl.ERAMFacility { // Enroute controller
					// From any center
					handoffTCP = shortFID(fromCtrl) + fromCtrl.Position
				} else if fromCtrl.FacilityIdentifier != "" {
					// Different facility; show full id of originator
					handoffTCP = shortFID(fromCtrl) + fromCtrl.Position
				}
			}
		} else { // outbound
			if toCtrl := ctx.GetResolvedController(toTCP); toCtrl != nil {
				if toCtrl.ERAMFacility { // Enroute
					// Always the one-character id and the sector
					handoffId = shortFID(toCtrl)
					handoffTCP = shortFID(toCtrl) + toCtrl.Position
				} else if toCtrl.FacilityIdentifier != "" { // Different facility
					// Different facility: show their TCP, id is the facility #
					handoffId = shortFID(toCtrl)
					handoffTCP = shortFID(toCtrl) + toCtrl.Position
				} else {
					// Intrafacility handoff - show the resolved controller's id
					// (e.g., handoff to "2M" consolidated to "2K" shows "K")
					handoffId = toCtrl.Position[len(toCtrl.Position)-1:]
				}
			} else if toTCP != "" {
				// Fallback: show the handoff indicator even if we can't resolve the controller
				handoffId = string(toTCP[len(toTCP)-1:])
			}
		}
	}
	if handoffTCP == "" && ctx.SimTime.Before(anno.AcceptedHandoffDisplayEnd) {
		handoffTCP = anno.AcceptedHandoffSector
	}
	return
}

func formatAltitude(trk sim.Track, sfp *sim.NASFlightPlan, unreasonableModeC bool) (altitude string, pilotReported bool) {
	if trk.IsUnsupportedDB() {
		if sfp != nil && sfp.PilotReportedAltitude != 0 {
			return fmt.Sprintf("%03d", sfp.PilotReportedAltitude/100), true
		}
		return "", false
	}

	haveTransponderAltitude := trk.Mode == av.TransponderModeAltitude && (sfp == nil || !sfp.InhibitModeCAltitudeDisplay)
	if haveTransponderAltitude && unreasonableModeC {
		return "XXX", false
	}
	if haveTransponderAltitude {
		if trk.TransponderAltitude < 0 {
			return fmt.Sprintf("N%02d", int(-trk.TransponderAltitude+50)/100), false
		}
		return fmt.Sprintf("%03d", int(trk.TransponderAltitude+50)/100), false
	}
	if sfp != nil && sfp.PilotReportedAltitude != 0 {
		return fmt.Sprintf("%03d", sfp.PilotReportedAltitude/100), true
	}
	if sfp != nil && sfp.InhibitModeCAltitudeDisplay {
		return "***", false
	}
	if trk.Mode == av.TransponderModeStandby {
		return "RDR", false
	}
	return "   ", false
}

func (sp *STARSPane) resolveScratchpad1(ctx *panes.Context, trk sim.Track,
	sfp *sim.NASFlightPlan, state *TrackState) string {
	if sfp == nil {
		return ""
	}
	if sfp.Scratchpad != "" {
		return sfp.Scratchpad
	}
	if state != nil && sp.annotationsForTrack(ctx, trk).ClearedScratchpadAlternate {
		return ""
	}

	adapt := ctx.FacilityAdaptation
	falt := func() string {
		alt := sfp.RequestedAltitude
		if adapt.Datablocks.AllowLongScratchpad {
			return fmt.Sprintf("%03d", alt/100)
		}
		return fmt.Sprintf("%02d", alt/1000)
	}
	shortExit := func() string {
		e := sfp.ExitFix
		if e == "" {
			return ""
		}
		e, _, _ = strings.Cut(e, ".")
		if sigPt, ok := sp.significantPoints[e]; ok {
			if sigPt.ShortName != "" {
				return sigPt.ShortName
			} else if len(e) > 3 {
				return e[:3]
			}
			return e
		}
		return ""
	}
	abbrevExit := func() string {
		e := sfp.ExitFix
		if e == "" {
			return ""
		}
		e, _, _ = strings.Cut(e, ".")
		if sigPt, ok := sp.significantPoints[e]; ok {
			if sigPt.Abbreviation != "" {
				return sigPt.Abbreviation
			}
			return e[:1]
		}
		return ""
	}

	if trk.IsArrival() {
		// Note arrivalAirport is only set if it should be shown when there is no scratchpad set
		ap, ok := ctx.Client.State.Airports[trk.ArrivalAirport]
		if ok && !ap.OmitArrivalScratchpad {
			return sfp.ExitFix
		}
		return ""
	}

	if adapt.Datablocks.Scratchpad1.DisplayExitFix {
		return shortExit()
	} else if adapt.Datablocks.Scratchpad1.DisplayExitFix1 {
		return abbrevExit()
	} else if adapt.Datablocks.Scratchpad1.DisplayExitGate {
		if ex := abbrevExit(); ex != "" {
			return ex + falt()
		}
	} else if adapt.Datablocks.Scratchpad1.DisplayAltExitGate {
		if ex := abbrevExit(); ex != "" {
			return falt() + ex
		}
	}
	return ""
}

func flightRulesIndicator(sfp *sim.NASFlightPlan) string {
	if sfp.Rules == av.FlightRulesVFR {
		return "V"
	}
	if sfp.TypeOfFlight == av.FlightTypeOverflight {
		return "E"
	}
	return " "
}

func (sp *STARSPane) buildLimitedDatablock(ctx *panes.Context, trk sim.Track,
	color renderer.RGB, brightness radar.Brightness,
	beaconator, displayBeaconCode bool, groundspeed string) *limitedDatablock {
	anno := sp.annotationsForTrack(ctx, trk)
	db := sp.ldbArena.AllocClear()

	// Field 0: CA, MCI, and squawking special codes
	alerts := sp.getDatablockAlerts(ctx, trk, LimitedDatablock)
	copy(db.field0[:], alerts[:])

	extended := anno.FullLDBEndTime.After(ctx.SimTime)
	sqspc, _ := trk.Squawk.IsSPC()
	extended = extended || (trk.Mode != av.TransponderModeStandby && sqspc)

	who := trk.MissingFlightPlan && !anno.MissingFlightPlanAcknowledged

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
			sp.annotationsForTrack(ctx, trk).DisplayLDBBeaconCode || displayBeaconCode {
			// Field 1: reported beacon code
			// TODO: Field 1: WHO if unassociated and no flight plan
			var f1 int
			if displayBeaconCode { // flashing yellow
				f1 = formatDBText(db.field1[:], trk.Squawk.String(), brightness.ScaleRGB(sp.Colors.TextWarning), true)
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

	// Field 3: mode C altitude (intentionally different from formatAltitude)
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
}

func (sp *STARSPane) buildPartialDatablock(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan,
	color renderer.RGB, altitude, sp1, groundspeed, handoffId, actype string, pilotReportedAltitude bool) *partialDatablock {
	fa := ctx.FacilityAdaptation
	db := sp.pdbArena.AllocClear()

	// TODO: 2-69 doesn't list CA/MCI, so should this be blank even in
	// those cases? (Note that SPC upgrades partial to full datablocks.)
	alerts := sp.getDatablockAlerts(ctx, trk, PartialDatablock)
	copy(db.field0[:], alerts[:])

	// Fields 1+2: altitude/scratchpad, populated per clock phase.
	field1Length := util.Select(fa.Datablocks.AllowLongScratchpad, 4, 3)
	fmtPad := func(s string) string {
		for len([]rune(s)) < field1Length {
			s += " "
		}
		return s
	}
	writeAltitude := func(phase int) {
		if pilotReportedAltitude {
			formatDBText(db.field12[phase][:], fmtPad(altitude+"*"), color, false)
		} else {
			formatDBText(db.field12[phase][:], fmtPad(altitude)+handoffId, color, false)
		}
	}

	// Phase 1: altitude
	writeAltitude(0)

	// Phase 2: scratchpad 1 if set, else altitude
	if sp1 != "" {
		formatDBText(db.field12[1][:], fmtPad(sp1)+handoffId, color, false)
	} else {
		writeAltitude(1)
	}

	// Phase 3: scratchpad 2 (if adapted), else scratchpad 1, else altitude
	if fa.Datablocks.PDB.ShowScratchpad2 && sfp.SecondaryScratchpad != "" {
		formatDBText(db.field12[2][:], fmtPad(sfp.SecondaryScratchpad)+"+", color, false)
	} else if sp1 != "" {
		formatDBText(db.field12[2][:], fmtPad(sp1)+handoffId, color, false)
	} else {
		writeAltitude(2)
	}

	// Phase 4: altitude (unguarded fallthrough)
	writeAltitude(3)

	// Field 3: adaptation-based variant (not clock-phase based).
	rulesCategory := flightRulesIndicator(sfp)
	cwt := util.Select(sfp.CWTCategory != "", sfp.CWTCategory, " ")
	if fa.Datablocks.PDB.SplitGSAndCWT {
		formatDBText(db.field3Primary[:], groundspeed, color, false)
		formatDBText(db.field3Alternate[:], rulesCategory+cwt, color, false)
	} else {
		if fa.Datablocks.PDB.HideGroundspeed {
			formatDBText(db.field3Primary[:], rulesCategory+cwt, color, false)
		} else {
			formatDBText(db.field3Primary[:], groundspeed+rulesCategory+cwt, color, false)
		}
		if fa.Datablocks.PDB.ShowAircraftType {
			formatDBText(db.field3Alternate[:], actype, color, false)
		}
	}

	// Field 4: ident
	if trk.Ident {
		formatDBText(db.field4[:], "ID", color, true)
	}

	return db
}

func (sp *STARSPane) buildFullDatablock(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan, color renderer.RGB,
	brightness radar.Brightness, altitude, sp1, groundspeed, handoffId, handoffTCP, actype string,
	pilotReportedAltitude, beaconator, beaconMismatch, displayBeaconCode bool) *fullDatablock {
	fa := ctx.FacilityAdaptation
	state := sp.TrackState[trk.ADSBCallsign]
	anno := sp.annotationsForTrack(ctx, trk)
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
	if state != nil { // FIXME: these should live in NASFlightPlan
		if anno.InhibitMSAW || sfp.DisableMSAW {
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
		if tcps, ok := sp.PointOuts[sfp.ACID]; ok && ctx.UserControlsPosition(tcps.To) {
			formatDBText(db.field8[:], "PO", color, false)
		} else if ok && ctx.UserControlsPosition(tcps.From) {
			id := tcps.To
			if len(id) > 1 && id[0] >= '0' && id[0] <= '9' {
				id = id[1:]
			}
			formatDBText(db.field8[:], "PO"+string(id), color, false)
		} else if ctx.SimTime.Before(anno.UNFlashingEndTime) {
			formatDBText(db.field8[:], "UN", color, true)
		} else if anno.POFlashingEndTime.After(ctx.SimTime) {
			formatDBText(db.field8[:], "PO", color, true)
		} else if sfp.RedirectedHandoff.ShowRDIndicator(ctx.UserPrimaryPosition(), anno.RDIndicatorEnd, ctx.SimTime) {
			formatDBText(db.field8[:], "RD", color, false)
		}
	}

	// Line 2
	// Fields 3 and 4: altitude/scratchpad/handoff, populated per clock phase.
	field3Length := util.Select(fa.Datablocks.AllowLongScratchpad, 4, 3)
	fmtPad := func(s string) string {
		for len([]rune(s)) < field3Length {
			s += " "
		}
		return s
	}
	writeAlt34 := func(phase int) {
		if pilotReportedAltitude {
			formatDBText(db.field34[phase][:], fmtPad(altitude+"*"), color, false)
		} else {
			formatDBText(db.field34[phase][:], fmtPad(altitude)+handoffId, color, false)
		}
	}
	sp2 := sfp.SecondaryScratchpad
	sp2OnLine2 := sp2 != "" && !fa.Datablocks.FDB.Scratchpad2OnLine3

	// When MSAW or CA is active, altitude is forced into all clock phases,
	// overriding the normal timesharing of scratchpad/handoff TCP.
	alertForceAlt := db.hasAlertInField0('L', 'A') || db.hasAlertInField0('C', 'A')

	// Phase 1: altitude
	if altitude != "" || handoffId != " " {
		writeAlt34(0)
	}

	// Phase 2: scratchpad 1 → scratchpad 2 → altitude
	if alertForceAlt {
		writeAlt34(1)
	} else if sp1 != "" {
		formatDBText(db.field34[1][:], fmtPad(sp1)+handoffId, color, false)
	} else if sp2OnLine2 {
		// TODO: confirm no handoffId here
		formatDBText(db.field34[1][:], fmtPad(sp2)+"+", color, false)
	} else if altitude != "" || handoffId != " " {
		writeAlt34(1)
	}

	// Phase 3: handoffTCP → scratchpad 2 → scratchpad 1 → altitude
	if alertForceAlt {
		writeAlt34(2)
	} else if handoffTCP != "" && !fa.Datablocks.FDB.DisplayFacilityOnly {
		formatDBText(db.field34[2][:], fmtPad(handoffTCP)+handoffId, color, false)
	} else if sp2OnLine2 {
		formatDBText(db.field34[2][:], fmtPad(sp2)+"+", color, false)
	} else if sp1 != "" {
		formatDBText(db.field34[2][:], fmtPad(sp1)+handoffId, color, false)
	} else if altitude != "" || handoffId != " " {
		writeAlt34(2)
	}

	// Phase 4: when an alert is active, force altitude here too (normally blank).
	if alertForceAlt {
		writeAlt34(3)
	}
	// Otherwise intentionally blank. In real STARS, all FDB_L2_C1 rules use explicit clock_phase
	// 1/2/3 guards with no unguarded fallthrough, so phase 4 only shows all-phases overrides
	// (frozen, coast, AMB, NAT). Unlike PDB which has an unguarded mode-c fallthrough rule.

	// Field 5: speed/actype/reqalt, populated per clock phase.
	sp.fillFDBField5(ctx, trk, sfp, db, color, groundspeed, actype)

	// Field 6: ATPA/beacon, populated per clock phase.
	sp.fillFDBField6(ctx, trk, sfp, db, color, brightness, beaconMismatch, displayBeaconCode)

	// Field 7: assigned alt/sp2/beacon mismatch, populated per clock phase.
	sp.fillFDBField7(ctx, trk, sfp, db, color, beaconMismatch)

	return db
}

func (sp *STARSPane) fillFDBField5(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan, db *fullDatablock,
	color renderer.RGB, groundspeed, actype string) {
	state := sp.TrackState[trk.ADSBCallsign]
	anno := sp.annotationsForTrack(ctx, trk)
	rulesCategory := flightRulesIndicator(sfp)
	rulesCategory += util.Select(sfp.CWTCategory != "", sfp.CWTCategory, " ")
	rulesCategory += " "

	inhibitACType := sfp.InhibitACTypeDisplay ||
		(state != nil && anno.InhibitACTypeDisplay != nil && *anno.InhibitACTypeDisplay)
	forceACType := ctx.SimTime.Before(sfp.ForceACTypeDisplayEndTime) ||
		(state != nil && ctx.SimTime.Before(anno.ForceACTypeDisplayEndTime))
	hasForceACType := forceACType && !inhibitACType && actype != "" && !trk.Ident
	showACType := !trk.Ident && actype != "" && !inhibitACType

	// Helper: write groundspeed + indicators (IF/HL/ID + rules/CWT).
	writeGSRules := func(field []dbChar) {
		if state == nil {
			return
		}
		if trk.IsUnsupportedDB() {
			// No radar return, so show 00 for speed with CWT/flight rules.
			idx := formatDBText(field, "00", color, false)
			formatDBText(field[idx:], rulesCategory, color, false)
			return
		}
		if anno.IFFlashing {
			if trk.Ident {
				formatDBText(field, "IF"+"ID", color, true)
			} else {
				formatDBText(field, "IF"+rulesCategory, color, true)
			}
		} else {
			gs := util.Select(sfp.HoldState, "HL", groundspeed)
			idx := formatDBText(field, gs, color, false)
			if trk.Ident {
				formatDBText(field[idx:], "ID", color, true)
			} else {
				formatDBText(field[idx:], rulesCategory, color, false)
			}
		}
	}

	// Helper: write aircraft type + RNAV indicator.
	writeACType := func(field []dbChar) {
		rnav := util.Select(sfp.RNAV, "^", " ")
		formatDBText(field, actype+rnav, color, false)
	}

	// Helper: write requested altitude if adapted and available.
	writeReqAlt := func(field []dbChar) bool {
		if trk.Ident || forceACType {
			return false
		}
		draTrack := sp.annotationsForTrack(ctx, trk).DisplayRequestedAltitude
		if draTrack != nil && !*draTrack {
			return false
		}
		if draTrack == nil && !sp.DisplayRequestedAltitude {
			return false
		}
		if alt := sfp.RequestedAltitude; alt != 0 {
			// FIXME: 2-67: with 2-char TCPs, the "R" goes in the
			// second place in field 4 when requested altitude is
			// being displayed--i.e., it is always in the 5th
			// column of this row of the datablock.
			formatDBText(field, fmt.Sprintf("R%03d ", alt/100), color, false)
			return true
		}
		return false
	}

	// Phase 1: forceACType → actype. Otherwise: GS + indicators.
	if hasForceACType {
		writeACType(db.field5[0][:])
	} else {
		writeGSRules(db.field5[0][:])
	}

	// Phase 2: forceACType → actype. Otherwise: actype → GS.
	if hasForceACType || showACType {
		writeACType(db.field5[1][:])
	} else {
		writeGSRules(db.field5[1][:])
	}

	// Phase 3: forceACType → actype. Otherwise: reqalt → actype → GS.
	if hasForceACType {
		writeACType(db.field5[2][:])
	} else if !writeReqAlt(db.field5[2][:]) {
		if showACType {
			writeACType(db.field5[2][:])
		} else {
			writeGSRules(db.field5[2][:])
		}
	}

	// Phase 4: same as phase 2.
	copy(db.field5[3][:], db.field5[1][:])
}

func (sp *STARSPane) fillFDBField6(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan, db *fullDatablock,
	color renderer.RGB, brightness radar.Brightness, beaconMismatch, displayBeaconCode bool) {
	if trk.IsUnsupportedDB() {
		return
	}
	anno := sp.annotationsForTrack(ctx, trk)

	// Helper: try to write ATPA content (*TPA, NOWGT, or intrail distance).
	warnAlertOverride := anno.DisplayATPAWarnAlert
	writeATPA := func(field []dbChar) bool {
		if warnAlertOverride != nil && !*warnAlertOverride {
			formatDBText(field, "*TPA", color, false)
			return true
		}
		if trk.IntrailDistance != 0 && sp.currentPrefs().DisplayATPAInTrailDist && !anno.InhibitDisplayInTrailDist {
			distColor := color
			if trk.ATPAStatus == ATPAStatusWarning {
				distColor = sp.Colors.ATPAWarning
			} else if trk.ATPAStatus == ATPAStatusAlert {
				distColor = sp.Colors.ATPAAlert
			}
			if sfp.CWTCategory == "" {
				formatDBText(field, "NOWGT", distColor, false)
			} else {
				formatDBText(field, fmt.Sprintf("%.2f", trk.IntrailDistance), distColor, false)
			}
			return true
		}
		return false
	}

	// Helper: try to write beacon code content (display beacon, mismatch, or duplicate).
	writeBeacon := func(field []dbChar) bool {
		if displayBeaconCode {
			formatDBText(field, trk.Squawk.String(), brightness.ScaleRGB(sp.Colors.TextWarning), true)
			return true
		}
		if beaconMismatch {
			formatDBText(field, trk.Squawk.String(), color, false)
			return true
		}
		if _, ok := sp.DuplicateBeacons[trk.Squawk]; ok && anno.DBAcknowledged != trk.Squawk {
			formatDBText(field, "DB", color, false)
			return true
		}
		return false
	}

	// Phase 1: ATPA first → beacon
	if !writeATPA(db.field6[0][:]) {
		writeBeacon(db.field6[0][:])
	}

	// Phase 2: ATPA first → beacon (same priority as phase 1)
	if !writeATPA(db.field6[1][:]) {
		writeBeacon(db.field6[1][:])
	}

	// Phase 3: beacon first → ATPA (priority inversion)
	if !writeBeacon(db.field6[2][:]) {
		writeATPA(db.field6[2][:])
	}

	// Phase 4: blank
}

func (sp *STARSPane) fillFDBField7(ctx *panes.Context, trk sim.Track, sfp *sim.NASFlightPlan, db *fullDatablock,
	color renderer.RGB, beaconMismatch bool) {
	leaderLineDirection := sp.getLeaderLineDirection(ctx, trk)
	altSet := sfp.AssignedAltitude != 0
	rightJustify := leaderLineDirection >= math.South

	// Helper: write assigned altitude into a field7 phase slot.
	writeAssignedAlt := func(field []dbChar) {
		if !altSet {
			return
		}
		altText := fmt.Sprintf("A%03d", sfp.AssignedAltitude/100)
		startIdx := util.Select(!rightJustify, 1, 0) // N–SE leader → indent one char
		for i, ch := range altText {
			if startIdx+i >= len(field) {
				break
			}
			field[startIdx+i] = dbChar{ch: ch, color: color}
		}
	}

	// Helper: write beacon mismatch squawk into a field7 phase slot.
	writeBeaconMismatch := func(field []dbChar) {
		if !beaconMismatch {
			return
		}
		startIdx := util.Select(rightJustify, 1, 0)
		formatDBText(field[startIdx:], sfp.AssignedSquawk.String(), color, true)
	}

	if ctx.FacilityAdaptation.Datablocks.FDB.Scratchpad2OnLine3 {
		sp2 := sfp.SecondaryScratchpad
		sp2Set := sp2 != ""

		// Helper: write scratchpad 2 into a field7 phase slot.
		writeSP2 := func(field []dbChar) {
			if !sp2Set {
				return
			}
			runes := []rune(sp2)
			if len(runes) > 3 {
				runes = runes[:4]
			}
			for len(runes) < 4 {
				runes = append(runes, ' ')
			}
			text := string(runes) + "+"
			startIdx := 2
			if rightJustify {
				startIdx = 5 - len([]rune(text))
			}
			formatDBText(field[startIdx:], text, color, false)
		}

		// Per-phase priority with SP2 timesharing.
		if beaconMismatch {
			for phase := range 4 {
				writeBeaconMismatch(db.field7[phase][:])
			}
		} else {
			// Phases 1 and 4: assigned alt → SP2
			if sp2Set && !altSet {
				for p := range 4 {
					writeSP2(db.field7[p][:])
				}
			} else {
				// Just alt or both are set; alt is in phases 1 and 4 in both cases.
				writeAssignedAlt(db.field7[0][:])
				writeAssignedAlt(db.field7[3][:])
				// SP2 in phases 2 and 3 if set; otherwise it's all alt
				if sp2Set {
					writeSP2(db.field7[1][:])
					writeSP2(db.field7[2][:])
				} else {
					writeAssignedAlt(db.field7[1][:])
					writeAssignedAlt(db.field7[2][:])
				}
			}
		}
	} else {
		// Unguarded rules apply to all phases.
		// When both assigned alt and beacon mismatch exist, timeshare
		// them: odd phases show assigned alt, even phases show mismatch.
		if beaconMismatch && altSet {
			writeAssignedAlt(db.field7[0][:])
			writeBeaconMismatch(db.field7[1][:])
			writeAssignedAlt(db.field7[2][:])
			writeBeaconMismatch(db.field7[3][:])
		} else if beaconMismatch {
			for phase := range 4 {
				writeBeaconMismatch(db.field7[phase][:])
			}
		} else {
			for phase := range 4 {
				writeAssignedAlt(db.field7[phase][:])
			}
		}
	}
}

func (sp *STARSPane) getGhostDatablock(ctx *panes.Context, ghost *av.GhostTrack, color renderer.RGB) ghostDatablock {
	var db ghostDatablock

	anno := sp.annotations(ctx, ghost.ADSBCallsign)
	trk, ok := ctx.GetTrackByCallsign(ghost.ADSBCallsign)
	cwt := ""
	if ok && trk.IsAssociated() {
		cwt = trk.FlightPlan.CWTCategory
	}
	groundspeed := fmt.Sprintf("%02d", (ghost.Groundspeed+5)/10)
	if anno.Ghost.PartialDatablock {
		// Partial datablock is just airspeed and then aircraft CWT type
		formatDBText(db.field0[:], groundspeed+cwt, color, false)
	} else {
		// The full datablock ain't much more...
		formatDBText(db.field0[:], string(ghost.ADSBCallsign), color, false)
		formatDBText(db.field1[:], groundspeed, color, false) // TODO: no CWT?
	}

	return db
}

func (sp *STARSPane) trackDatablockColorBrightness(ctx *panes.Context, trk sim.Track) (color renderer.RGB, dbBrightness, posBrightness radar.Brightness) {
	ps := sp.currentPrefs()
	dt := sp.datablockType(ctx, trk)
	anno := sp.annotationsForTrack(ctx, trk)

	// Cases where it's always a full datablock
	inboundPointOut := false
	forceFDB := false
	if trk.IsAssociated() {
		if tcps, ok := sp.PointOuts[trk.FlightPlan.ACID]; ok && ctx.UserControlsPosition(tcps.To) {
			forceFDB = true
			inboundPointOut = true
		} else {
			forceFDB = forceFDB || (anno.OutboundHandoffAccepted && ctx.SimTime.Before(anno.OutboundHandoffFlashEnd))
			forceFDB = forceFDB || ctx.IsHandoffToUser(&trk)
		}
	}

	// Figure out the datablock and position symbol brightness first
	if trk.ADSBCallsign == sp.dwellAircraft { // dwell overrides everything as far as brightness
		dbBrightness = radar.Brightness(100)
		posBrightness = radar.Brightness(100)
	} else if forceFDB || anno.OutboundHandoffAccepted {
		dbBrightness = ps.Brightness.FullDatablocks
		posBrightness = ps.Brightness.Positions
	} else if dt == PartialDatablock || dt == LimitedDatablock {
		dbBrightness = ps.Brightness.LimitedDatablocks
		posBrightness = ps.Brightness.LimitedDatablocks
	} else /* dt == FullDatablock */ {
		if !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			dbBrightness = ps.Brightness.OtherTracks
			posBrightness = ps.Brightness.OtherTracks
		} else {
			// Regular FDB that we own
			dbBrightness = ps.Brightness.FullDatablocks
			posBrightness = ps.Brightness.Positions
		}
	}

	// Possibly adjust brightness if it should be flashing.
	halfSeconds := time.Now().UnixMilli() / 500
	if forceFDB && halfSeconds&1 == 0 { // half-second cycle
		dbBrightness /= 2
		posBrightness /= 2
	}

	if anno.IsSelected {
		// middle button selected
		color = sp.Colors.SelectedDatablock
	} else if trk.IsUnassociated() {
		color = sp.Colors.UnownedDatablock
	} else {
		sfp := trk.FlightPlan
		if _, ok := sp.ForceQLACIDs[sfp.ACID]; ok {
			// Check if we're the controller being ForceQL
			color = sp.Colors.CautionDatablock
		} else if anno.PointOutAcknowledged || anno.ForceQL {
			// Ack'ed point out to us (but not cleared) or force quick look.
			color = sp.Colors.CautionDatablock
		} else if inboundPointOut {
			// Pointed out to us.
			color = sp.Colors.CautionDatablock
		} else if anno.DatablockAlert {
			// This is indeed supposed to be CautionDatablock, not AlertDatablock(!).
			color = sp.Colors.CautionDatablock
		} else if ctx.UserOwnsFlightPlan(sfp) {
			// we own the track
			color = sp.Colors.OwnedDatablock
		} else if ctx.UserControlsPosition(sfp.RedirectedHandoff.OriginalOwner) ||
			ctx.UserControlsPosition(sfp.RedirectedHandoff.RedirectedTo) {
			color = sp.Colors.OwnedDatablock
		} else if ctx.UserControlsPosition(sfp.HandoffController) &&
			!ctx.UserControlsPosition(sfp.RedirectedHandoff.GetLastRedirector()) {
			// flashing white if it's being handed off to us.
			color = sp.Colors.OwnedDatablock
		} else if anno.OutboundHandoffAccepted {
			// we handed it off, it was accepted, but we haven't yet acknowledged
			color = sp.Colors.OwnedDatablock
		} else if ps.QuickLookAll && ps.QuickLookAllIsPlus {
			// quick look all plus
			color = sp.Colors.OwnedDatablock
		} else if ps.QuickLookTCPs[string(ctx.PrimaryTCPForTCW(sfp.OwningTCW))] {
			// individual quicklook plus controller
			color = sp.Colors.OwnedDatablock
		} else {
			// other controller owns, no special case applies
			color = sp.Colors.UnownedDatablock
		}
	}

	return
}

func (sp *STARSPane) datablockVisible(ctx *panes.Context, trk sim.Track) bool {
	anno := sp.annotationsForTrack(ctx, trk)

	af := sp.currentPrefs().AltitudeFilters

	if ctx.SimTime.Before(sp.DisplayBeaconCodeEndTime) && trk.Squawk == sp.DisplayBeaconCode {
		// beacon code display 6-117
		return true
	}

	if trk.IsUnassociated() {
		if ctx.SimTime.Before(anno.FullLDBEndTime) {
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
		sfp := trk.FlightPlan

		if ctx.UserOwnsFlightPlan(sfp) {
			// For owned datablocks
			return true
		} else if ctx.UserControlsPosition(sfp.HandoffController) {
			// For receiving handoffs
			return true
		} else if anno.PointOutAcknowledged {
			// Pointouts: This is if its been accepted,
			// for an incoming pointout, it falls to the FDB check
			return true
		} else if ok, _ := trk.Squawk.IsSPC(); ok {
			// Special purpose codes
			return true
		} else if sp.annotationsForTrack(ctx, trk).DisplayFDB {
			// For non-greened handoffs
			return true
		} else if trk.IsOverflight() && sp.currentPrefs().OverflightFullDatablocks {
			// Overflights
			return true
		} else if sp.isQuicklooked(ctx, trk) {
			return true
		} else if ctx.UserControlsPosition(sfp.RedirectedHandoff.RedirectedTo) {
			// Redirected to
			return true
		} else if slices.ContainsFunc(sfp.RedirectedHandoff.Redirector, func(tcp sim.ControlPosition) bool {
			return ctx.UserControlsPosition(tcp)
		}) {
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

func (sp *STARSPane) drawDatablocks(dbs map[av.ADSBCallsign]datablock, ctx *panes.Context, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Datablocks)

	// Partition them by DB type so we can draw FDBs last
	var ldbs, pdbs, sdbs, fdbs []sim.Track

	for _, trk := range sp.visibleTracks {
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

			halfSeconds := time.Now().UnixMilli() / 500
			clockPhase := ctx.FacilityAdaptation.CurrentDatablockClockPhase(time.Now())
			db.draw(td, pll, font, &strBuilder, brightness, leaderLineDirection, clockPhase, halfSeconds)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) haveActiveWarnings(ctx *panes.Context, trk sim.Track) bool {
	ps := sp.currentPrefs()
	anno := sp.annotationsForTrack(ctx, trk)

	// Only this applies to unassociated tracks(?)
	if ok, _ := trk.Squawk.IsSPC(); ok {
		return true
	}
	if trk.IsUnassociated() {
		return false
	}
	sfp := trk.FlightPlan

	if anno.MSAW && !anno.InhibitMSAW && !sfp.DisableMSAW && !ps.DisableMSAW {
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
	anno := sp.annotationsForTrack(ctx, trk)

	var alerts []dbChar
	added := make(map[string]any)
	addAlert := func(s string, flash bool, red bool) {
		if _, ok := added[s]; ok {
			return // don't duplicate it
		}
		added[s] = nil

		color := util.Select(red, sp.Colors.AlertDatablock, sp.Colors.CautionDatablock)
		if len(alerts) > 0 {
			alerts = append(alerts, dbChar{ch: '/', color: color, flashing: flash})
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
			addAlert(code, !anno.SPCAcknowledged, true)
		}
	}
	if dbtype == LimitedDatablock {
		// That's all folks
		return alerts
	}

	sfp := trk.FlightPlan
	if dbtype == FullDatablock {
		if anno.MSAW && !anno.InhibitMSAW && !sfp.DisableMSAW && !ps.DisableMSAW {
			addAlert("LA", !anno.MSAWAcknowledged, true)
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

		if sfp.SPCOverride != "" && fa.Datablocks.PDB.DisplayCustomSPCs {
			// We only care about adapted alerts
			if slices.Contains(fa.Datablocks.CustomSPCs, sfp.SPCOverride) {
				addAlert(sfp.SPCOverride, !anno.SPCAcknowledged, false)
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
