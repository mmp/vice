// eram/datablock.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// DatablockType enumerates the supported ERAM datablock formats. Only the
// general types are provided here; the specific contents are defined
// elsewhere.
type DatablockType int

const (
	// LimitedDatablock represents the two-line limited data block used for
	// untracked or unpaired targets.
	LimitedDatablock DatablockType = iota
	// FullDatablock represents the five line full data block.
	FullDatablock
	// EnhancedLimitedDatablock represents the optional enhanced limited data
	// block.  It behaves like LimitedDatablock with additional information.
	EnhancedLimitedDatablock
)

// datablock abstracts the different concrete datablock implementations.  A
// datablock knows how to render itself at a particular point relative to the
// leader line.
type datablock interface {
	draw(td *renderer.TextDrawBuilder, pt [2]float32, font *renderer.Font,
		sb *strings.Builder, brightness scope.Brightness,
		dir math.CardinalOrdinalDirection, halfSeconds int64)
	// xOffset returns the horizontal offset from the end of the leader line
	// to the datablock's text anchor when it is positioned in direction dir.
	xOffset(dir math.CardinalOrdinalDirection, font *renderer.Font) float32
	// dim scales every populated character's color; used for the conflict
	// alert brightness-cycle flash.
	dim(factor float32)
	// portalFenceVisible reports whether the datablock has data in line 0 or
	// column 0, the fields that sit outside of the portal fence.
	portalFenceVisible() bool
}

// dbChar represents a single character in a datablock along with its colour and
// flashing state.
type dbChar struct {
	ch       rune
	color    renderer.RGB
	flashing bool
}

// --- Drawing helpers -----------------------------------------------------

// dbLine stores characters making up a single line of a datablock.  The slice
// length is capped to the maximum possible number of characters drawn on a
// line.
type dbLine struct {
	length int
	ch     [16]dbChar
}

// dbMakeLine flattens a number of datablock fields into a contiguous line.
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

// Len returns the number of active characters in the line.
func (l dbLine) Len() int {
	for i := l.length - 1; i >= 0; i-- {
		if l.ch[i].ch != 0 {
			return i + 1
		}
	}
	return 0
}

// dbChopTrailing removes trailing unset characters from the provided field.
func dbChopTrailing(f []dbChar) []dbChar {
	for i, v := range slices.Backward(f) {
		if v.ch != 0 {
			return f[:i+1]
		}
	}
	return nil
}

// dbDrawLines renders the given datablock lines.  The leader line direction is
// used only to determine justification.
func dbDrawLines(lines []dbLine, td *renderer.TextDrawBuilder, pt [2]float32,
	font *renderer.Font, sb *strings.Builder, brightness scope.Brightness,
	dir math.CardinalOrdinalDirection, halfSeconds int64) {
	for _, line := range lines {
		sb.Reset()
		dbDrawLine(line, td, pt, font, sb, brightness, halfSeconds)
		pt[1] -= dbLinePitch(font)
	}
}

func dbSplitLine(l dbLine, n int) (dbLine, dbLine) {
	var lead, rest dbLine
	for i := range l.length {
		if i < n {
			lead.ch[lead.length] = l.ch[i]
			lead.length++
		} else {
			rest.ch[rest.length] = l.ch[i]
			rest.length++
		}
	}
	return lead, rest
}

// dbDrawLine renders a single datablock line.
func dbDrawLine(line dbLine, td *renderer.TextDrawBuilder, pt [2]float32,
	font *renderer.Font, sb *strings.Builder, brightness scope.Brightness,
	halfSeconds int64) {

	style := renderer.TextStyle{Font: font}

	flush := func() {
		if sb.Len() > 0 {
			pt = td.AddText(rewriteDelta(sb.String()), pt, style)
			sb.Reset()
		}
	}

	for i := range line.length {
		ch := line.ch[i]
		if ch.ch == 0 {
			sb.WriteByte(' ')
			continue
		}

		if ch.flashing && halfSeconds&1 == 1 { // TODO: adjust this value
			continue
		}

		c := ch.color
		if !c.Equals(style.Color) {
			flush()
			style.Color = c
		}
		sb.WriteRune(ch.ch)
	}
	flush()
}

// fieldEmpty reports whether the datablock field contains any characters.
func fieldEmpty(f []dbChar) bool {
	for _, ch := range f {
		if ch.ch != 0 {
			return false
		}
	}
	return true
}

// dbWriteText writes the provided text into the datablock field using the given
// colour. Any unused characters remain unset.
func dbWriteText(dst []dbChar, s string, c renderer.RGB, flashing bool) {
	for i, ch := range s {
		if i >= len(dst) {
			break
		}
		dst[i] = dbChar{ch: ch, color: c, flashing: flashing}
	}
}

func rewriteDelta(s string) string { return s }

type limitedDatablock struct {
	line0 [8]dbChar
	line1 [8]dbChar
	line2 [8]dbChar
}

func (db limitedDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32,
	font *renderer.Font, sb *strings.Builder, brightness scope.Brightness,
	dir math.CardinalOrdinalDirection, halfSeconds int64) {
	lines := []dbLine{
		dbMakeLine(dbChopTrailing(db.line0[:])),
		dbMakeLine(dbChopTrailing(db.line1[:])),
		dbMakeLine(dbChopTrailing(db.line2[:])),
	}
	pt[1] += float32(font.Size)
	dbDrawLines(lines, td, pt, font, sb, brightness, dir, halfSeconds)
}

func (db *limitedDatablock) xOffset(dir math.CardinalOrdinalDirection, font *renderer.Font) float32 {
	if dir != math.West {
		return 0
	}
	cols := max(len(dbChopTrailing(db.line0[:])), len(dbChopTrailing(db.line1[:])), len(dbChopTrailing(db.line2[:])))
	return -dbTextWidth(cols, font)
}

type fullDatablock struct {
	line0 [16]dbChar
	line1 [16]dbChar
	// line 2
	vci   [2]dbChar
	line2 [16]dbChar
	// line3
	col1   [2]dbChar
	fieldD [8]dbChar
	fieldE [8]dbChar
	hsf    [1]dbChar // drawn right after Field E
	line4  [16]dbChar
	// FONT's PORTAL and LINE 4 may make the fonts of the fields outside the
	// portal fence, line 0 and column 0, and of line 4 smaller than the
	// others' (3.7).
	portalFont, line4Font *renderer.Font
}

func (db fullDatablock) draw(td *renderer.TextDrawBuilder, pt [2]float32,
	font *renderer.Font, sb *strings.Builder, brightness scope.Brightness,
	dir math.CardinalOrdinalDirection, halfSeconds int64) {

	var lines [5]dbLine
	fullDatablockLines(&db, &lines)
	pt[1] += float32(font.Size)
	at := func(line int, dx, dy float32) [2]float32 {
		return [2]float32{pt[0] + dx, pt[1] - float32(line)*dbLinePitch(font) - dy}
	}
	drawLine := func(line dbLine, p [2]float32, f *renderer.Font) {
		sb.Reset()
		dbDrawLine(line, td, p, f, sb, brightness, halfSeconds)
	}

	// The fields outside the portal fence sit on their lines' bottoms, and
	// those of column 0 end at the fence.
	portalY := float32(font.Size - db.portalFont.Size)
	drawLine(lines[0], at(0, 0, portalY), db.portalFont)
	drawLine(lines[dbCallsignLine], at(dbCallsignLine, 0, 0), font)
	leadX := -dbLeadFieldGap*dbCharWidth(font) - dbLeadFieldChars*dbCharWidth(db.portalFont)
	for _, i := range []int{dbVCILine, dbCIDLine} {
		lead, rest := dbSplitLine(lines[i], dbLeadFieldChars)
		drawLine(lead, at(i, leadX, portalY), db.portalFont)
		drawLine(rest, at(i, 0, 0), font)
	}
	drawLine(lines[dbLine4], at(dbLine4, 0, 0), db.line4Font)
}

func (db *fullDatablock) ownershipSymbolCol() (int, bool) {
	for i, ch := range db.col1 {
		if ch.ch == 'R' {
			return i, true
		}
	}
	return 0, false
}

// leadOverhang returns how far left of the datablock's anchor the
// ownership symbol starts, if it is drawn; it is in column 0, drawn in the
// portal font.
func (db *fullDatablock) leadOverhang(font *renderer.Font) float32 {
	if col, drawn := db.ownershipSymbolCol(); drawn {
		return dbCharWidth(db.portalFont)*float32(dbLeadFieldChars-col) + dbCharWidth(font)*dbLeadFieldGap
	}
	return 0
}

func (db *fullDatablock) mainWidth(font *renderer.Font) float32 {
	var lines [5]dbLine
	fullDatablockLines(db, &lines)
	cols := 0
	for i := range lines[:dbLine4] {
		n := lines[i].Len()
		if i == dbVCILine || i == dbCIDLine {
			n -= dbLeadFieldChars
		}
		cols = max(cols, n)
	}
	return max(dbTextWidth(cols, font), dbTextWidth(lines[dbLine4].Len(), db.line4Font))
}

// dbTextWidth returns the width of the ink of a line of n characters.
func dbTextWidth(n int, font *renderer.Font) float32 {
	if n == 0 {
		return 0
	}
	return float32(n-1)*font.LookupGlyph(' ').AdvanceX + font.LookupGlyph('0').Width()
}

func (db *fullDatablock) xOffset(dir math.CardinalOrdinalDirection, font *renderer.Font) float32 {
	clearance := dbLeaderClearance * font.LookupGlyph(' ').AdvanceX
	switch dir {
	case math.West, math.NorthWest, math.SouthWest:
		return -(db.mainWidth(font) + clearance)
	case math.North, math.South:
		if over := db.leadOverhang(font); over > 0 {
			return over - db.portalFont.LookupGlyph('0').Width()/2
		}
		return clearance
	default:
		return clearance
	}
}

// dimChars scales the color of every populated character in the field.
// Datablock char colors are final once built (dbDrawLine renders them
// as-is, ignoring its brightness parameter), so scaling here is the only
// point where the conflict-alert dim can be applied.
func dimChars(chars []dbChar, factor float32) {
	for i := range chars {
		if chars[i].ch != 0 {
			chars[i].color = chars[i].color.Scale(factor)
		}
	}
}

func (db *fullDatablock) dim(factor float32) {
	dimChars(db.line0[:], factor)
	dimChars(db.line1[:], factor)
	dimChars(db.vci[:], factor)
	dimChars(db.line2[:], factor)
	dimChars(db.hsf[:], factor)
	dimChars(db.col1[:], factor)
	dimChars(db.fieldD[:], factor)
	dimChars(db.fieldE[:], factor)
	dimChars(db.line4[:], factor)
}

func (db *limitedDatablock) dim(factor float32) {
	dimChars(db.line0[:], factor)
	dimChars(db.line1[:], factor)
	dimChars(db.line2[:], factor)
}

func (db *fullDatablock) portalFenceVisible() bool {
	return !fieldEmpty(db.line0[:]) || !fieldEmpty(db.vci[:]) || !fieldEmpty(db.col1[:])
}

func (db *limitedDatablock) portalFenceVisible() bool { return false }

// drawPortalFence draws the datablock's portal fence: a horizontal segment
// in the gap between lines 0 and 1 and a vertical segment down the left side
// of lines 1 through 3, separating line 0 and column 0 from the rest of the
// datablock.
func drawPortalFence(ld *renderer.ColoredLinesDrawBuilder, l DatablockLayout, color renderer.RGB) {
	x := l.Anchor[0] - l.CharWidth*dbFenceInset
	top, bottom := l.gapCenter(1), l.gapCenter(4)
	ld.AddLine([2]float32{x, top}, [2]float32{x + l.CharWidth*dbFenceCols, top}, color)
	ld.AddLine([2]float32{x, top}, [2]float32{x, bottom}, color)
}

func (ep *Scope) getAllDatablocks(ctx *scope.Context, tracks []sim.Track) map[av.ADSBCallsign]datablock {
	ep.fdbArena.Reset()
	ep.ldbArena.Reset()

	dbs := make(map[av.ADSBCallsign]datablock)
	for _, trk := range tracks {
		state := ep.TrackState[trk.ADSBCallsign]
		if state == nil {
			continue
		}

		dbType := ep.datablockType(ctx, trk)
		ps := ep.currentPrefs()
		brite := util.Select(dbType == FullDatablock, ps.Brightness.FDB, ps.Brightness.LDB)
		color := brite.ScaleRGB(colors.yellow)
		db := ep.getDatablock(ctx, trk, dbType, color)
		if db != nil && ep.caDimmed(trk.ADSBCallsign) {
			db.dim(caDimFactor)
		}
		dbs[trk.ADSBCallsign] = db
	}
	return dbs
}

func (ep *Scope) getDatablock(ctx *scope.Context, trk sim.Track, dbType DatablockType, color renderer.RGB) datablock {
	state := ep.TrackState[trk.ADSBCallsign]
	ps := ep.currentPrefs()
	switch dbType {
	case FullDatablock:
		db := ep.fdbArena.AllocClear()
		db.portalFont, db.line4Font = ep.fdbPortalFont(), ep.fdbLine4Font()
		// Line 0: point-out indicator.
		if ch, glyphColor, ok := ep.pointOutIndicatorGlyph(ctx, &trk); ok {
			dbWriteText(db.line0[2:], string(ch), glyphColor, false)
		}

		// Line 1
		dbWriteText(db.line1[:], string(trk.FlightPlan.ACID), color, false) // also * if satcom
		vciColor := (ps.Brightness.ONFREQ + ps.Brightness.Portal).ScaleRGB(colors.vciGreen)
		dbWriteText(db.vci[:], util.Select(state.DisplayVCI || state.HoverVCI, vci, ""), vciColor, false)

		// Line 2. The VRI is drawn at line 4's brightness, and not in quick
		// look FDBs (5.1.5.13).
		line4Color := (ps.Brightness.FDB - ps.Brightness.Line4).ScaleRGB(colors.yellow)
		alt := ep.getAltitudeFormat(trk)
		dbWriteText(db.line2[:], alt, color, false)
		quickLook := !ctx.UserOwnsFlightPlan(trk.FlightPlan) && ep.quickLooked(ctx, trk.FlightPlan)
		if ep.dbOverrideHeld(overrideVRI) && !quickLook {
			dbWriteText(db.line2[len(alt)+1:], verticalRateText(state), line4Color, false)
		}

		// format line 3.
		// TODO: HIJK, RDOF, EMERG (what colors are these?) incoming handoff
		colColor := (ps.Brightness.FDB + ps.Brightness.Portal).ScaleRGB(colors.yellow)
		dbWriteText(db.col1[:], util.Select(ctx.UserOwnsFlightPlan(trk.FlightPlan), "", " R"), colColor, false)
		dbWriteText(db.fieldD[:], trk.FlightPlan.CID, color, false)

		gsText := fmt.Sprintf(" %v", int(state.Track.Groundspeed))
		shortERAMID := func(ctrl *av.Controller) string {
			return shortFieldERAMID(ctrl.FacilityIdentifier, ctrl.Position, ctx.Client.State.HandoffIDs)
		}
		specialText, special := specialCodeText[state.Track.Squawk]
		// Of the special codes, 7600 and 7700 blink, as would the two
		// adapted ones that vice has no adaptation for (5.1.5.7).
		blink := special && (state.Track.Squawk == 0o7600 || state.Track.Squawk == 0o7700)
		if ep.dbOverrideHeld(overrideCode) {
			// The code takes the place of everything else, without time
			// sharing; with no code, Field E is blank (5.1.5.5).
			dbWriteText(db.fieldE[:], util.Select(state.Track.Mode == av.TransponderModeStandby, "    ",
				reportedCodeText(state.Track)), color, blink)
		} else if ep.dbOverrideHeld(overrideSpeed) {
			dbWriteText(db.fieldE[:], gsText, color, false)
		} else if special {
			dbWriteText(db.fieldE[:], specialText, color, blink)
		} else if trk.FlightPlan.HandoffController != "" {
			var controller string
			if ctrl := ctx.GetResolvedController(trk.FlightPlan.HandoffController); ctrl != nil {
				controller = shortERAMID(ctrl)
			} else {
				controller = string(trk.FlightPlan.HandoffController)
			}
			if len(controller) == 2 {
				controller = "-" + controller
			}
			dbWriteText(db.fieldE[:], util.Select(ep.dbAlternate, fmt.Sprintf("H%v", controller), gsText), color, false)
		} else if ctx.InterpolatedSimTime.Before(state.OSectorEndTime) {
			var controller string
			if ctrl := ctx.GetResolvedController(trk.FlightPlan.TrackingController); ctrl != nil {
				controller = shortERAMID(ctrl)
				if len(controller) == 2 {
					controller = "-" + controller
				}
			} else {
				controller = string(trk.FlightPlan.TrackingController)
			}
			dbWriteText(db.fieldE[:], util.Select(ep.dbAlternate, fmt.Sprintf("O%v", controller), gsText), color, false)
		} else {
			middle := " "
			airportCode, ok := ctx.Client.State.FacilityAdaptation.AirportCodes[trk.FlightPlan.ExitFix]
			if ok {
				middle = airportCode
			}
			gsText = fmt.Sprintf("%v%v", middle, int(state.Track.Groundspeed))
			dbWriteText(db.fieldE[:], gsText, color, false)
		}

		// The HSF indicator follows Field E (5.1.5.20.1).
		if hsfDataExists(trk.FlightPlan) {
			dbWriteText(db.hsf[:], hsfIndicator, color, false)
		}

		// Get line 4 (if applicable)
		if state != nil && !state.HSFHide && hsfDataExists(trk.FlightPlan) {
			clear(db.line4[:])

			const headingStart = 0
			speedStart := speedStartFromGroundspeedLine4(gsText)
			if speedStart < 0 {
				speedStart = 0
			} else if speedStart >= len(db.line4) {
				speedStart = len(db.line4) - 1
			}

			sp := trk.FlightPlan.Scratchpad
			if isQSFreeText(sp) {
				// Free text overrides. The clear-weather circle is only an indicator; do not render it.
				dbWriteText(db.line4[headingStart:], stripQSFreeTextIndicator(sp), line4Color, false)
			} else {
				// Heading (optional)
				if sp != "" {
					txt := sp
					if len(sp) != 4 {
						txt = "H" + sp
					}
					dbWriteText(db.line4[headingStart:], txt, line4Color, false)
				}
				// Speed/Mach (optional)
				if sm := trk.FlightPlan.SecondaryScratchpad; sm != "" {
					dbWriteText(db.line4[speedStart:], sm, line4Color, false)
				}
			}
		} else if ps.Line4Type == Line4Destination {
			dbWriteText(db.line4[:], string(trk.FlightPlan.ArrivalAirport), line4Color, false)
		} else if ps.Line4Type == Line4Type {
			dbWriteText(db.line4[:], formatTypeAndSuffix(trk.FlightPlan), line4Color, false)
		}
		return db
	case EnhancedLimitedDatablock:
		return ep.ldbArena.AllocClear()
	case LimitedDatablock:
		db := ep.ldbArena.AllocClear()
		state := ep.TrackState[trk.ADSBCallsign]
		id := ep.limitedDatablockID(trk, state)
		dbWriteText(db.line0[:], id, color, false)
		dbWriteText(db.line1[:], fmt.Sprintf("%03d", int(state.Track.TransponderAltitude+50)/100), color, false)
		// The code goes on line 3 unless line 1 already shows it (5.1.5.5).
		if ep.dbOverrideHeld(overrideCode) && state.Track.Mode != av.TransponderModeStandby &&
			id != state.Track.Squawk.String() {
			dbWriteText(db.line2[:], reportedCodeText(state.Track), color, false)
		}
		return db
	default:
		return nil // should not happen
	}
}

// specialCodeText holds the text that data blocks show in place of the
// special beacon codes (Table 5-4).
var specialCodeText = map[av.Squawk]string{
	0o1276: "ADIZ", 0o7400: "LLNK", 0o7500: "HIJK",
	0o7600: "RDOF", 0o7700: "EMRG", 0o7777: "AFIO",
}

// reportedCodeText returns the beacon code of a radar sample as the CODE
// override shows it: a special code as its text (5.1.5.5).
func reportedCodeText(rt av.RadarTrack) string {
	if text, ok := specialCodeText[rt.Squawk]; ok {
		return text
	}
	return rt.Squawk.String()
}

// verticalRateText returns a track's vertical rate between its last two
// radar samples as the VRI shows it, in hundreds of feet per minute with
// its sign; a rate under 100 feet per minute isn't shown (5.1.5.13).
func verticalRateText(state *TrackState) string {
	minutes := state.TrackTime.Sub(state.PreviousTrackTime).Minutes()
	if state.PreviousTrackTime.IsZero() || minutes <= 0 || state.Track.Mode != av.TransponderModeAltitude ||
		state.PreviousTrack.Mode != av.TransponderModeAltitude {
		return ""
	}
	rate := int(float64(state.Track.TransponderAltitude-state.PreviousTrack.TransponderAltitude) / minutes / 100)
	switch {
	case rate > 0:
		return fmt.Sprintf("+%d", rate)
	case rate < 0:
		return fmt.Sprint(rate)
	default:
		return ""
	}
}

// limitedDatablockID returns the first line of a track's LDB: the ACID if
// it is paired and otherwise the beacon code. With the BCAST FLID filter
// off, an unpaired target that isn't squawking 1200 shows its ADS-B
// callsign instead (5.1.2, 5.1.3).
func (ep *Scope) limitedDatablockID(trk sim.Track, state *TrackState) string {
	if trk.IsAssociated() {
		return string(trk.FlightPlan.ACID)
	}
	if ep.currentPrefs().UnpairedLDBCallsigns && state.Track.Squawk != 0o1200 {
		return trk.ADSBCallsign.String()
	}
	return state.Track.Squawk.String()
}

func hsfDataExists(fp *sim.FlightPlan) bool {
	if fp == nil {
		return false
	}
	return fp.Scratchpad != "" || fp.SecondaryScratchpad != ""
}

// isQSFreeText reports whether a flight plan's primary scratchpad holds
// fourth-line free text rather than a heading.
func isQSFreeText(s string) bool {
	return strings.HasPrefix(s, sim.QSFreeTextIndicator)
}

// stripQSFreeTextIndicator returns the free text that a scratchpad holds, or
// "" if it holds none.
func stripQSFreeTextIndicator(s string) string {
	text, ok := strings.CutPrefix(s, sim.QSFreeTextIndicator)
	return util.Select(ok, text, "")
}

// speedStartFromGroundspeedLine4 returns the column in line4 where the 'S'/'M' should start.
func speedStartFromGroundspeedLine4(gsText string) int {
	// Find digit positions in the groundspeed-bearing text.
	var digitPos []int
	for i := 0; i < len(gsText); i++ {
		if gsText[i] >= '0' && gsText[i] <= '9' {
			digitPos = append(digitPos, i)
		}
	}
	if len(digitPos) < 2 {
		return -1
	}

	const line3FieldEStart = 10
	const line3ToLine4Shift = 7
	line3Index := line3FieldEStart + digitPos[1]
	return line3Index - line3ToLine4Shift
}

func (ep *Scope) getAltitudeFormat(track sim.Track) string {
	state := ep.TrackState[track.ADSBCallsign]
	currentAltitude := state.Track.TransponderAltitude
	fp := track.FlightPlan
	displayAlt := fp.DataBlockAltitude()
	formatScopeAltitude := func(alt int) string {
		alt = int(alt+50) / 100
		return string([]byte{
			byte('0' + alt/100),
			byte('0' + (alt/10)%10),
			byte('0' + alt%10),
		})
	}
	formatCurrent := formatScopeAltitude(int(currentAltitude))
	formatDisplay := formatScopeAltitude(displayAlt)
	if alt, t := fp.DisplayedInterimAltitude(); alt > 0 {
		return formatDisplay + t.String() + formatCurrent
	}
	if fp.HasAltitudeBlock() {
		// Inside the block, the floor and ceiling; outside it, the floor
		// and the current altitude.
		floor, ceiling := fp.AltitudeBlock[0], fp.AltitudeBlock[1]
		upper := formatCurrent
		if cur := (int(currentAltitude) + 50) / 100 * 100; cur >= floor && cur <= ceiling {
			upper = formatScopeAltitude(ceiling)
		}
		return formatScopeAltitude(floor) + "B" + upper
	}
	if q := fp.AssignedAltitudeQualifier; q == sim.VFRAltitude || q == sim.OTPAltitude {
		return q.String() + "/" + formatCurrent
	}
	switch {
	case formatCurrent == formatDisplay:
		return formatCurrent + "C"
	case currentAltitude > float32(displayAlt) && displayAlt > -1: // TODO: Find actual font so that the up arrows draw
		return formatDisplay + downArrow + formatCurrent
	case currentAltitude < float32(displayAlt):
		return formatDisplay + upArrow + formatCurrent
	}
	return "" // This shouldn't happen?
}

func (ep *Scope) drawDatablocks(tracks []sim.Track, dbs map[av.ADSBCallsign]datablock,
	ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ep.ldbIdx = ep.ldbIdx[:0]
	ep.eldbIdx = ep.eldbIdx[:0]
	ep.fdbIdx = ep.fdbIdx[:0]
	for i, trk := range tracks {
		if !ep.datablockVisible(ctx, trk) {
			continue
		}
		switch ep.datablockType(ctx, trk) {
		case FullDatablock:
			ep.fdbIdx = append(ep.fdbIdx, i)
		case EnhancedLimitedDatablock:
			ep.eldbIdx = append(ep.eldbIdx, i)
		default:
			ep.ldbIdx = append(ep.ldbIdx, i)
		}
	}

	var sb strings.Builder
	halfSeconds := time.Now().UnixMilli() / 500
	ps := ep.currentPrefs()
	fenceColor := ps.Brightness.Fence.ScaleRGB(renderer.RGB{R: 1, G: 1, B: 1})

	draw := func(indices []int) {
		for _, i := range indices {
			trk := &tracks[i]
			db := dbs[trk.ADSBCallsign]
			if db == nil {
				continue
			}
			state := ep.TrackState[trk.ADSBCallsign]
			if state == nil {
				continue
			}
			dbType := ep.datablockType(ctx, *trk)
			var sz int
			if dbType == FullDatablock {
				sz = ps.FDBSize
			} else {
				sz = ps.LDBSize
			}
			font := ep.ERAMFont(sz)
			end, dir := ep.datablockAnchor(ctx, *trk, db, dbType, transforms)
			brightness := ep.datablockBrightness(state)
			db.draw(td, end, font, &sb, brightness, dir, halfSeconds)
			if ps.PortalFence && db.portalFenceVisible() {
				drawPortalFence(ld, makeDatablockLayout(end, font, ep.fdbPortalFont(), ep.fdbLine4Font()), fenceColor)
			}
		}
	}

	draw(ep.ldbIdx)
	draw(ep.eldbIdx)
	draw(ep.fdbIdx)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// datablockAnchor returns the window-space anchor point used by both the
// datablock renderer and the hover-outline computation. It also returns the
// (possibly adjusted) direction the datablock was placed in.
func (ep *Scope) datablockAnchor(ctx *scope.Context, trk sim.Track, db datablock, dbType DatablockType,
	transforms scope.Transformations) ([2]float32, math.CardinalOrdinalDirection) {
	state := ep.TrackState[trk.ADSBCallsign]
	start := transforms.WindowFromLatLongP(state.Track.Location)
	dir := ep.datablockDirection(state, dbType)

	ps := ep.currentPrefs()
	font := ep.ERAMFont(util.Select(dbType == FullDatablock, ps.FDBSize, ps.LDBSize))

	if dbType != FullDatablock {
		end := math.Add2f(start, math.Scale2f(ep.leaderLineVectorNoLength(dir), ctx.DrawPixelScale))
		end[0] += db.xOffset(dir, font)
		end[1] -= 10 * ctx.DrawPixelScale
		return end, dir
	}

	lengthMode := ep.leaderLineLength(state)
	end := math.Add2f(start, math.Scale2f(ep.leaderLineVectorWithLength(dir, lengthMode), ctx.DrawPixelScale))
	end[0] += db.xOffset(dir, font)
	if lengthMode == 0 && dir == math.East {
		end[0] += 2 * font.LookupGlyph(' ').AdvanceX
	}
	end[1] += datablockLeaderConnectOffset(dir, font)
	return end, dir
}

func datablockLeaderConnectOffset(dir math.CardinalOrdinalDirection, font *renderer.Font) float32 {
	inkTop := func(line float32) float32 {
		return line*dbLinePitch(font) + (dbLeaderInkInset-1)*float32(font.Size)
	}
	switch dir {
	case math.South, math.SouthEast, math.SouthWest:
		return inkTop(dbCallsignLine) + dbInkHeight*float32(font.Size)
	default:
		return inkTop(dbCIDLine)
	}
}

// sectorDisplayID returns the ID the scope shows for the sector of position
// p.
func sectorDisplayID(ctx *scope.Context, p sim.ControlPosition) string {
	if ctrl := ctx.GetResolvedController(p); ctrl != nil {
		return shortFieldERAMID(ctrl.FacilityIdentifier, ctrl.Position, ctx.Client.State.HandoffIDs)
	}
	return string(p)
}

// shortFieldERAMID returns the ERAM Field E sector identifier for a
// given facility prefix and position. The format depends on the
// facility's FieldEFormat, which determines which identifier and how
// much of the position string to include.
func shortFieldERAMID(prefix, position string, handoffIDs []sim.HandoffID) string {
	if prefix == "" {
		return position
	}
	for _, hid := range handoffIDs {
		ids := []string{hid.StarsID, hid.TwoCharStarsID, hid.SingleCharStarsID, hid.Prefix}
		found := slices.Contains(ids, prefix)
		if !found {
			continue
		}

		switch hid.FieldEFormat {
		case "OneLetterAndSubset":
			// B1D: B = Field E Letter (or single char ID), 1D = full position
			if hid.FieldELetter != "" {
				return hid.FieldELetter + position
			}
			return hid.SingleCharStarsID + position

		case "TwoLetters":
			// B0D: B0 = two char ID, D = sector letter (last char of position)
			if len(position) > 0 {
				return hid.TwoCharStarsID + position[len(position)-1:]
			}
			return hid.TwoCharStarsID

		case "TwoLettersAndSubset":
			// B01D: B0 = two char ID, 1D = full position
			return hid.TwoCharStarsID + position

		case "OneLetterAndStarsId":
			// AB0A: A = Field E Letter, B0A = STARS ID (no position)
			return hid.FieldELetter + hid.StarsID

		case "FullStarsIdOnly":
			// B0A: full STARS ID (no position)
			return hid.StarsID

		default:
			// add error logging here
		}
	}
	return prefix + position
}
