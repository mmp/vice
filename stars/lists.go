// stars/lists.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Most system lists are drawn via drawSystemList / ListFormatter, which
// allows a mostly-declarative style for specifying list contents where
// drawSystemList handles the formatting details in a single place.
type ListFormatter struct {
	Title      string
	FrameTitle string
	Lines      int
	Entries    int
	FormatLine func(idx int, sb *strings.Builder)
}

// drawSystemList draws a list with title, optional "MORE" indicator, and formatted lines.
// Returns the bounds of the drawn list.
func (sp *STARSPane) drawSystemList(ctx *panes.Context, paneExtent math.Extent2D, pos *[2]float32,
	style renderer.TextStyle, td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder, formatter ListFormatter) math.Extent2D {
	var allText strings.Builder
	if formatter.Title != "" {
		allText.WriteString(formatter.Title + "\n")
	}
	if formatter.Entries > formatter.Lines && formatter.Lines > 0 {
		allText.WriteString(fmt.Sprintf("MORE: %d/%d\n", formatter.Lines, formatter.Entries))
	}
	for i := range min(formatter.Entries, formatter.Lines) {
		l := allText.Len()
		formatter.FormatLine(i, &allText)
		if allText.Len() > l {
			allText.WriteByte('\n')
		}
	}

	return sp.drawListText(ctx, paneExtent, pos, rewriteDelta(allText.String()), style, td, formatter.FrameTitle, ld)
}

// formatListEntry formats a single entry of a STARS system list based on
// the provided format string, which uses []-delimited specifiers to
// specify entries in a line; characters outside of brackets are passed
// through unchanged. A number of built-in specifiers are available to show
// values from the NASFlightPlan; additional custom specifiers can be
// provided in custom for items that are not in the flight plan and are
// limited to specific list types.
func (sp *STARSPane) formatListEntry(ctx *panes.Context, format string, fp *sim.NASFlightPlan,
	custom map[string]func() string) string {
	rewriteFixForList := func(fix string) string {
		if spt, ok := sp.significantPoints[fix]; ok && spt.ShortName != "" {
			fix = spt.ShortName
		}
		if len(fix) > 3 {
			fix = fix[:3]
		}
		return fmt.Sprintf("%3s", fix)
	}

	formatters := map[string]func(*sim.NASFlightPlan) string{
		"ACID": func(fp *sim.NASFlightPlan) string {
			return fmt.Sprintf("%-7s", string(fp.ACID))
		},
		"ACID_MSAWCA": func(fp *sim.NASFlightPlan) string {
			s := string(fp.ACID)
			if fp.DisableMSAW {
				if fp.DisableCA {
					s += "+"
				} else {
					s += "*"
				}
			} else if fp.DisableCA {
				s += STARSTriangleCharacter
			}
			return fmt.Sprintf("%-8s", s)
		},
		"ACTYPE": func(fp *sim.NASFlightPlan) string {
			return fmt.Sprintf("%4s", fp.AircraftType)
		},
		"BEACON": func(fp *sim.NASFlightPlan) string {
			haveCode := fp.Rules == av.FlightRulesIFR || ctx.Now.Sub(sp.VFRFPFirstSeen[fp.ACID]) > 2*time.Second
			if haveCode {
				return fp.AssignedSquawk.String()
			} else {
				return "VFR "
			}
		},
		"CWT": func(fp *sim.NASFlightPlan) string {
			return util.Select(fp.CWTCategory != "", string(fp.CWTCategory[:1]), " ")
		},
		"DEP_EXIT_FIX": func(fp *sim.NASFlightPlan) string {
			if fp.TypeOfFlight == av.FlightTypeDeparture {
				return rewriteFixForList(fp.ExitFix)
			}
			return "   "
		},
		"ENTRY_FIX": func(fp *sim.NASFlightPlan) string {
			return rewriteFixForList(fp.EntryFix)
		},
		"EXIT_FIX": func(fp *sim.NASFlightPlan) string {
			return rewriteFixForList(fp.ExitFix)
		},
		"EXIT_GATE": func(fp *sim.NASFlightPlan) string {
			exit := rewriteFixForList(fp.ExitFix)
			if ctx.FacilityAdaptation.AllowLongScratchpad {
				return exit + fmt.Sprintf("%03d", fp.RequestedAltitude/100)
			} else {
				return exit + fmt.Sprintf("%02d", fp.RequestedAltitude/1000)
			}
		},
		"INDEX": func(fp *sim.NASFlightPlan) string {
			if fp.ListIndex == sim.UnsetSTARSListIndex {
				return "  "
			}
			return fmt.Sprintf("%2d", fp.ListIndex)
		},
		"NUMAC": func(fp *sim.NASFlightPlan) string {
			return strconv.Itoa(fp.AircraftCount)
		},
		"OWNER": func(fp *sim.NASFlightPlan) string {
			return fmt.Sprintf("%3s", ctx.ResolveController(fp.TrackingController))
		},
		"REQ_ALT": func(fp *sim.NASFlightPlan) string {
			return fmt.Sprintf("%03d", fp.RequestedAltitude/100)
		},
	}

	var result strings.Builder
	i := 0
	for i < len(format) {
		if format[i] == '[' {
			// Find the end of the specifier
			endIdx := strings.IndexByte(format[i:], ']')
			if endIdx == -1 {
				// Invalid format, just append the rest
				result.WriteString(format[i:])
				break
			}

			specifier := format[i+1 : i+endIdx]
			if formatter, ok := custom[specifier]; ok {
				result.WriteString(formatter())
			} else if formatter, ok := formatters[specifier]; ok {
				result.WriteString(formatter(fp))
			} else {
				// Unknown specifier, keep it as is. (This should be caught at start up time...)
				result.WriteString("[" + specifier + "]")
			}
			i += endIdx + 1
		} else {
			// Regular character, just append
			result.WriteByte(format[i])
			i++
		}
	}

	return result.String()
}

func (sp *STARSPane) drawSystemLists(ctx *panes.Context, paneExtent math.Extent2D, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	transforms.LoadWindowViewingMatrices(cb)

	listStyle := renderer.TextStyle{
		Font:  sp.systemFont(ctx, ps.CharSize.Lists),
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}

	previewAreaColor := ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor)
	if ctx.Client.RadioIsActive() && (sp.commandMode == CommandModeTargetGen || sp.commandMode == CommandModeTargetGenLock) &&
		!ctx.TCWIsPrivileged(ctx.UserTCW) {
		previewAreaColor = ps.Brightness.FullDatablocks.ScaleRGB(STARSTextAlertColor)
	}

	// Collect bounds from all lists for overlap detection
	allBounds := []math.Extent2D{
		sp.drawPreviewArea(ctx, paneExtent, previewAreaColor, td, ld),
		sp.drawSSAList(ctx, normalizedToWindow(ps.SSAList.Position), listStyle, td, transforms, ld, paneExtent, cb),
		sp.drawVFRList(ctx, paneExtent, listStyle, td, ld),
		sp.drawTABList(ctx, paneExtent, listStyle, td, ld),
		sp.drawAlertList(ctx, paneExtent, listStyle, td, ld),
		sp.drawCoastList(ctx, paneExtent, listStyle, td, ld),
		sp.drawMapsList(ctx, paneExtent, listStyle, td, ld),
		sp.drawRestrictionAreasList(ctx, paneExtent, listStyle, td, ld),
		sp.drawCRDAStatusList(ctx, paneExtent, listStyle, td, ld),
		sp.drawMCISuppressionList(ctx, paneExtent, listStyle, td, ld),
	}

	towerListAirports := ctx.Client.TowerListAirports()
	for i := range ps.TowerLists {
		if ps.TowerLists[i].Visible && i < len(towerListAirports) {
			allBounds = append(allBounds, sp.drawTowerList(ctx, paneExtent, towerListAirports[i], i, listStyle, td, ld))
		}
	}

	allBounds = append(allBounds, sp.drawSignOnList(ctx, paneExtent, listStyle, td, ld))
	allBounds = append(allBounds, sp.drawCoordinationLists(ctx, paneExtent, transforms, td, ld)...)

	td.GenerateCommands(cb)

	// Draw green frames around overlapping lists (only when not actively moving a list)
	if sp.movingList == "" && ctx.Mouse != nil && !ctx.Mouse.Down[platform.MouseButtonTertiary] {
		for i, b1 := range allBounds {
			if b1.Width() <= 0 {
				continue
			}
			for _, b2 := range allBounds[i+1:] {
				if b2.Width() <= 0 {
					continue
				}
				if math.Overlaps(b1, b2) {
					sp.drawListFrameColor(ctx, b1, STARSListColor, ld)
					sp.drawListFrameColor(ctx, b2, STARSListColor, ld)
				}
			}
		}
	}

	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawListFrame(ctx *panes.Context, bounds math.Extent2D, title string,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) {
	if bounds.Width() <= 0 {
		return
	}

	sp.drawListFrameColor(ctx, bounds, STARSListColor, ld)

	// Draw title above the frame in a smaller font
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, max(0, ps.CharSize.Lists-1))
	style := renderer.TextStyle{Font: font, Color: ps.Brightness.Lists.ScaleRGB(STARSListColor)}
	td.AddText(title, [2]float32{bounds.P0[0], bounds.P1[1] + float32(font.Size)}, style)
}

func (sp *STARSPane) drawListFrameColor(ctx *panes.Context, bounds math.Extent2D, color renderer.RGB,
	ld *renderer.ColoredLinesDrawBuilder) {
	ps := sp.currentPrefs()
	c := ps.Brightness.Lists.ScaleRGB(color)
	ld.AddLineLoop(c, [][2]float32{bounds.P0, {bounds.P1[0], bounds.P0[1]}, bounds.P1, {bounds.P0[0], bounds.P1[1]}})
}

func (sp *STARSPane) handleListDrag(ctx *panes.Context, bounds math.Extent2D, pos *[2]float32, listId string, paneExtent math.Extent2D,
	ld *renderer.ColoredLinesDrawBuilder) {
	if ctx.Mouse == nil {
		return
	}

	// First middle click inside this list's bounds starts moving
	if sp.movingList == "" && ctx.Mouse.Clicked[platform.MouseButtonTertiary] && bounds.Inside(ctx.Mouse.Pos) {
		sp.movingList = listId
		sp.movingListBounds = bounds
		sp.movingListOffset = math.Sub2f(ctx.Mouse.Pos, bounds.P0)
	}

	if sp.movingList == listId {
		// Draw green frame at original location
		sp.drawListFrameColor(ctx, sp.movingListBounds, STARSListColor, ld)

		// Draw white frame at current cursor position
		cursorPos := ctx.Mouse.Pos
		offset := sp.movingListOffset
		size := [2]float32{sp.movingListBounds.Width(), sp.movingListBounds.Height()}
		movedBounds := math.Extent2D{
			P0: math.Sub2f(cursorPos, offset),
			P1: math.Add2f(math.Sub2f(cursorPos, offset), size),
		}
		sp.drawListFrameColor(ctx, movedBounds, renderer.RGB{1, 1, 1}, ld)

		// Second middle click outside original bounds finishes the move
		if ctx.Mouse.Clicked[platform.MouseButtonTertiary] && !sp.movingListBounds.Inside(ctx.Mouse.Pos) {
			delta := math.Sub2f(movedBounds.P0, sp.movingListBounds.P0)
			pos[0] += delta[0] / paneExtent.Width()
			pos[1] += delta[1] / paneExtent.Height()
			sp.movingList = ""
		}
	}
}

func (sp *STARSPane) drawListText(ctx *panes.Context, paneExtent math.Extent2D, pos *[2]float32,
	text string, style renderer.TextStyle, td *renderer.TextDrawBuilder,
	frameTitle string, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	if text == "" {
		return math.Extent2D{}
	}

	pw := [2]float32{pos[0] * paneExtent.Width(), pos[1] * paneExtent.Height()}
	w, h := style.Font.BoundText(text, 0)
	td.AddText(text, pw, style)

	bounds := math.Extent2D{
		P0: [2]float32{pw[0], pw[1] - float32(h)},
		P1: [2]float32{pw[0] + float32(w), pw[1]},
	}
	sp.handleListDrag(ctx, bounds, pos, frameTitle, paneExtent, ld)
	if sp.showListFrames {
		sp.drawListFrame(ctx, bounds, frameTitle, td, ld)
	}
	return bounds
}

func (sp *STARSPane) drawPreviewArea(ctx *panes.Context, paneExtent math.Extent2D, color renderer.RGB,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	var text strings.Builder
	text.WriteString(sp.previewAreaOutput)
	text.WriteByte('\n')

	// Command mode indicator (possibly)
	modestr := sp.commandMode.PreviewString(sp)
	text.WriteString(modestr)
	if sp.commandMode == CommandModeMultiFunc {
		text.WriteString(sp.multiFuncPrefix)
	}
	if sp.commandMode == CommandModeTargetGen || sp.commandMode == CommandModeTargetGenLock {
		text.WriteByte(' ')
		text.WriteString(string(sp.tgtGenDefaultCallsign(ctx)))
	}
	if modestr != "" {
		text.WriteString("\n")
	}

	text.WriteString(strings.Join(strings.Fields(sp.previewAreaInput), "\n")) // spaces are rendered as newlines

	ps := sp.currentPrefs()
	style := renderer.TextStyle{
		Font:  sp.systemFont(ctx, ps.CharSize.Lists),
		Color: color,
	}
	return sp.drawListText(ctx, paneExtent, &ps.PreviewAreaPosition, rewriteDelta(text.String()),
		style, td, "PREVIEW AREA (P)", ld)
}

func (sp *STARSPane) drawSSAList(ctx *panes.Context, pw [2]float32, listStyle renderer.TextStyle, td *renderer.TextDrawBuilder,
	transforms radar.ScopeTransformations, ld *renderer.ColoredLinesDrawBuilder, paneExtent math.Extent2D, cb *renderer.CommandBuffer) math.Extent2D {
	ps := sp.currentPrefs()
	startY := pw[1]
	startX := pw[0]
	maxX := pw[0]

	font := sp.systemFont(ctx, ps.CharSize.Lists)
	alertStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor),
	}
	warnStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSTextWarningColor),
	}

	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	x := pw[0]
	newline := func() {
		maxX = max(maxX, pw[0])
		pw[0] = x
		pw[1] -= float32(font.Size)
	}

	// Inverted red triangle and green box...
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	scale := ctx.DrawPixelScale

	pIndicator := math.Add2f(pw, [2]float32{5 * scale, 0})
	tv := math.EquilateralTriangleVertices(7)
	for i := range tv {
		tv[i] = math.Add2f(pIndicator, math.Scale2f(tv[i], -scale))
	}
	trid.AddTriangle(tv[0], tv[1], tv[2], ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor))
	trid.GenerateCommands(cb)

	square := [][2]float32{[2]float32{-5, -5}, [2]float32{5, -5}, [2]float32{5, 5}, [2]float32{-5, 5}}
	square = util.MapSlice(square, func(p [2]float32) [2]float32 { return math.Add2f(math.Scale2f(p, scale), pIndicator) })
	ld.AddLineLoop(ps.Brightness.Lists.ScaleRGB(STARSListColor), square)
	ld.GenerateCommands(cb)

	pw[1] -= 10 * scale

	filter := ps.SSAList.Filter

	if filter.All || filter.Wx {
		var b strings.Builder

		for i, have := range sp.weatherRadar.HaveWeather() {
			if have && ps.DisplayWeatherLevel[i] {
				b.WriteByte('(')
				b.WriteByte(byte('1' + i))
				b.WriteByte(')')
			} else if have {
				b.WriteByte(' ')
				b.WriteByte(byte('1' + i))
				b.WriteByte(' ')
			} else {
				b.WriteString("   ")
			}
		}
		td.AddText(b.String(), pw, listStyle)
		newline()
	}

	if filter.All || filter.Time || filter.Altimeter {
		text := ""
		if filter.All || filter.Time {
			text += ctx.Client.CurrentTime().UTC().Format("1504/05 ")
		}
		if filter.All || filter.Altimeter {
			if metar, ok := ctx.Client.State.METAR[ctx.Client.State.PrimaryAirport]; ok {
				text += fmt.Sprintf("%4.2f", metar.Altimeter_inHg())
			}
		}
		td.AddText(text, pw, listStyle)
		newline()
	}

	// ATIS/GI text. (Note that per 4-44 filter.All does not apply to GI text.)
	for i := range ps.GIText {
		if filter.GIText[i] && (ps.ATIS[i] != "" || ps.GIText[i] != "") {
			pw = td.AddText(ps.ATIS[i]+" "+rewriteDelta(ps.GIText[i]), pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.Status || filter.Radar {
		if filter.All || filter.Status {
			if ctx.Client.Connected() {
				pw = td.AddText("OK/OK/NA ", pw, listStyle)
			} else {
				pw = td.AddText("NA/NA/NA ", pw, alertStyle)
			}
			// Consolidation config id
			pw = td.AddText(ctx.Client.State.ConfigurationId+" ", pw, listStyle)
		}
		if filter.All || filter.Radar {
			pw = td.AddText(sp.radarSiteId(ctx.FacilityAdaptation.RadarSites), pw, listStyle)
		}
		newline()
	}

	if filter.All || filter.Codes {
		if len(ps.SelectedBeacons) > 0 {
			codes := util.MapSlice(ps.SelectedBeacons,
				func(v av.Squawk) string {
					if v < 0o100 { // bank
						return strconv.FormatInt(int64(v), 8)
					} else {
						return v.String() // leading 0s as needed
					}
				})

			if len(codes) > 5 {
				pw = td.AddText(strings.Join(codes[:5], " "), pw, listStyle)
				codes = codes[5:]
				newline()
			}
			pw = td.AddText(strings.Join(codes, " "), pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.SpecialPurposeCodes {
		// Active special purpose codes.
		// those.
		codes := make(map[string]interface{})
		for _, trk := range sp.visibleTracks {
			if ok, code := trk.Squawk.IsSPC(); ok {
				codes[code] = nil
			} else if trk.IsAssociated() && trk.FlightPlan.SPCOverride != "" {
				codes[trk.FlightPlan.SPCOverride] = nil
			}
		}

		if len(codes) > 0 {
			// Two passes: first the red ones then the yellow ones
			for _, spc := range util.SortedMapKeys(codes) {
				if av.StringIsSPC(spc) {
					pw = td.AddText(spc+" ", pw, alertStyle)
				}
			}
			for _, spc := range util.SortedMapKeys(codes) {
				if !av.StringIsSPC(spc) {
					pw = td.AddText(spc+" ", pw, warnStyle)
				}
			}
			newline()
		}
	}

	if filter.All || filter.SysOff {
		var disabled []string
		if ps.DisableCAWarnings {
			disabled = append(disabled, "CA")
		}
		if ps.DisableMCIWarnings {
			disabled = append(disabled, "MCI")
		}
		if ps.DisableMSAW {
			disabled = append(disabled, "MSAW")
		}
		if ps.CRDA.Disabled {
			disabled = append(disabled, "CRDA")
		}
		if !ctx.Client.State.ATPAEnabled {
			disabled = append(disabled, "INTRAIL")
		}
		// TODO: others? 2-84
		if len(disabled) > 0 {
			pw = td.AddText(strings.Join(disabled, " "), pw, listStyle)
			newline()
		}
	}

	// For lines >32 characters, chop to 32 characters and then find the
	// last space; replace it with a '+' and then chop the rest.
	ssaChopLong := func(s string) string {
		if len(s) <= 32 {
			return s
		}
		b := []byte(s[:32])
		if idx := bytes.LastIndexByte(b, ' '); idx != -1 {
			b[idx] = '+'
			b = b[:idx+1]
		}
		return string(b)
	}

	if (filter.All || filter.Intrail || filter.Intrail25) && ctx.Client.State.ATPAEnabled { // 8-55 / 2-78
		var off, on, twoFive []string
		for _, r := range ctx.Client.State.ArrivalRunways {
			if ap, ok := ctx.Client.State.Airports[r.Airport]; ok {
				if vol, ok := ap.ATPAVolumes[r.Runway]; ok {
					if ctx.Client.State.IsATPAVolumeDisabled(vol.Id) {
						off = append(off, vol.Id)
					} else {
						on = append(on, vol.Id)
						if vol.Enable25nmApproach && ctx.Client.State.IsATPAVolume25nmEnabled(vol.Id) {
							twoFive = append(twoFive, vol.Id)
						}
					}
				}
			}
		}

		if filter.All || filter.Intrail {
			if len(off) > 0 {
				text := ssaChopLong("INTRAIL OFF: " + strings.Join(off, " "))
				pw = td.AddText(text, pw, listStyle)
				newline()
			} else if len(on) > 0 {
				pw = td.AddText("INTRAIL ON", pw, listStyle)
				newline()
			}
		}
		if filter.All || filter.Intrail25 {
			if len(twoFive) > 0 {
				text := ssaChopLong("INTRAIL 2.5 ON: " + strings.Join(twoFive, " "))
				pw = td.AddText(text, pw, listStyle)
				newline()
			}
		}
	}

	if filter.All || filter.Range || filter.PredictedTrackLines {
		text := ""
		if filter.All || filter.Range {
			text += fmt.Sprintf("%dNM ", int(ps.Range))
		}
		if (filter.All || filter.PredictedTrackLines) && ps.PTLLength > 0 {
			text += fmt.Sprintf("PTL: %.1f", ps.PTLLength)
		}
		pw = td.AddText(text, pw, listStyle)
		newline()
	}

	if filter.All || filter.AltitudeFilters {
		af := ps.AltitudeFilters
		text := fmt.Sprintf("%03d %03d U %03d %03d A",
			af.Unassociated[0]/100, af.Unassociated[1]/100,
			af.Associated[0]/100, af.Associated[1]/100)
		pw = td.AddText(text, pw, listStyle)
		newline()
	}

	if filter.All || filter.AirportWeather {
		airports := ctx.FacilityAdaptation.Altimeters
		if len(airports) == 0 {
			airports = util.SortedMapKeys(ctx.Client.State.Airports)

			// Filter out VFR-only
			airports = util.FilterSlice(airports, func(icao string) bool {
				ap := ctx.Client.State.Airports[icao]
				return len(ap.Departures) > 0 || len(ap.Approaches) > 0
			})

			// Sort via 1. primary? 2. tower list index, 3. alphabetic
			sort.Slice(airports, func(i, j int) bool {
				if airports[i] == ctx.Client.State.PrimaryAirport {
					return true
				} else if airports[j] == ctx.Client.State.PrimaryAirport {
					return false
				} else {
					a, b := ctx.Client.State.Airports[airports[i]], ctx.Client.State.Airports[airports[j]]
					ai := util.Select(a.TowerListIndex != 0, a.TowerListIndex, 1000)
					bi := util.Select(b.TowerListIndex != 0, b.TowerListIndex, 1000)
					if ai != bi {
						return ai < bi
					}
				}
				return airports[i] < airports[j]
			})

			// 2-79: no more than 6 are displayed.
			if len(airports) > 6 {
				airports = airports[:6]
			}
		}

		var altimeters []string
		for _, ap := range airports {
			if metar, ok := ctx.Client.State.METAR[ap]; ok {
				altimeters = append(altimeters, stripK(ap)+" "+fmt.Sprintf("%4.2fA", metar.Altimeter_inHg())) // 2-79: A -> automatic
			}
		}
		for len(altimeters) >= 3 {
			pw = td.AddText(strings.Join(altimeters[:3], " "), pw, listStyle)
			altimeters = altimeters[3:]
			newline()
		}
		if len(altimeters) > 0 {
			pw = td.AddText(strings.Join(altimeters, " "), pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.WxHistory {
		if sp.wxHistoryDraw != 0 {
			pw = td.AddText("WX HIST: "+strconv.Itoa(sp.wxHistoryDraw), pw, listStyle)
			newline()
		}
	}

	if (filter.All || filter.QuickLookPositions) && (ps.QuickLookAll || len(ps.QuickLookTCPs) > 0) {
		if ps.QuickLookAll {
			if ps.QuickLookAllIsPlus {
				pw = td.AddText("QL: ALL+", pw, listStyle)
			} else {
				pw = td.AddText("QL: ALL", pw, listStyle)
			}
		} else {
			pw = td.AddText("QL: "+sp.qlPositionsString(), pw, listStyle)
		}
		newline()
	}

	if filter.All || filter.Consolidation {
		// Display consolidation status per STARS manual 2-81
		// Format: TCW CON: primary secondary secondary...
		// Only show if the TCW actually owns positions (has a primary)
		var tcps []string
		if cons := ctx.Client.State.GetUserConsolidation(); cons != nil {
			if cons.PrimaryTCP != "" {
				tcps = append(tcps, string(cons.PrimaryTCP))
				for _, sec := range cons.SecondaryTCPs {
					// TODO: * if ConsolidationBasic ?
					tcps = append(tcps, string(sec.TCP))
				}
			}
		}

		// Do we still own any tracks for a TCP that went to another TCW via basic consolidation?
		othersBasicTCPs := make(map[sim.TCP]struct{})
		for tcw, cons := range ctx.Client.State.CurrentConsolidation {
			if tcw == ctx.UserTCW {
				continue
			}
			for _, sec := range cons.SecondaryTCPs {
				if sec.Type == sim.ConsolidationBasic {
					othersBasicTCPs[sec.TCP] = struct{}{}
				}
			}
		}

		ourOwnedBasicTCPs := make(map[sim.TCP]struct{})
		for _, trk := range sp.visibleTracks {
			if trk.IsUnassociated() {
				continue
			}
			fp := trk.FlightPlan
			if fp.OwningTCW != ctx.UserTCW {
				continue
			}
			if _, ok := othersBasicTCPs[fp.TrackingController]; ok {
				ourOwnedBasicTCPs[fp.TrackingController] = struct{}{}
			}
		}

		ourTCPs := util.SortedMapKeys(ourOwnedBasicTCPs)
		tcps = append(tcps, util.MapSlice(ourTCPs, func(tcp sim.TCP) string { return "*" + string(tcp) })...)

		if len(tcps) > 10 {
			// 2-81 says a plus sign is added to the end; presumably it's also truncated?
			tcps = tcps[:10]
			tcps[9] = tcps[9] + "+"
		}

		text := string(ctx.UserTCW) + " CON: " + strings.Join(tcps, " ")
		pw = td.AddText(text, pw, listStyle)
		newline()
	}

	if filter.All || filter.DisabledTerminal {
		// TODO: others?
		if ps.CRDA.Disabled {
			pw = td.AddText("TW OFF: CRDA", pw, listStyle)
			newline()
		}
	}

	if (filter.All || filter.ActiveCRDAPairs) && !ps.CRDA.Disabled {
		for i, crda := range ps.CRDA.RunwayPairState {
			if !crda.Enabled {
				continue
			}

			text := "*"
			text += util.Select(crda.Mode == CRDAModeStagger, "S ", "T ")
			text += sp.ConvergingRunways[i].Airport + " "
			text += sp.ConvergingRunways[i].getRunwaysString()

			pw = td.AddText(text, pw, listStyle)
			newline()
		}
	}

	maxX = max(maxX, pw[0])
	bounds := math.Extent2D{
		P0: [2]float32{startX, pw[1]},
		P1: [2]float32{maxX, startY},
	}
	sp.handleListDrag(ctx, bounds, &ps.SSAList.Position, "ssa", paneExtent, ld)
	if sp.showListFrames {
		sp.drawListFrame(ctx, bounds, "SSA (S)", td, ld)
	}
	return bounds
}

func getDuplicateBeaconCodes(ctx *panes.Context) map[av.Squawk]interface{} {
	n := len(ctx.Client.State.UnassociatedFlightPlans) + len(ctx.Client.State.Tracks)
	count := make(map[av.Squawk]int, n)

	for _, fp := range ctx.Client.State.UnassociatedFlightPlans {
		count[fp.AssignedSquawk]++
	}
	for _, trk := range ctx.Client.State.Tracks {
		// TODO: are unsupported being counted twice?
		if trk.IsAssociated() {
			count[trk.FlightPlan.AssignedSquawk]++
		}
	}

	dupes := make(map[av.Squawk]interface{})
	for sq, n := range count {
		if n > 1 {
			dupes[sq] = nil
		}
	}
	return dupes
}

func (sp *STARSPane) drawVFRList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.VFRList.Visible {
		return math.Extent2D{}
	}

	vfr := util.FilterSlice(ctx.Client.State.UnassociatedFlightPlans,
		func(fp *sim.NASFlightPlan) bool {
			// Only include NAS VFR flight plans.
			return fp.Rules != av.FlightRulesIFR && fp.Location.IsZero() && fp.PlanType == sim.LocalEnroute
		})

	for _, fp := range vfr {
		if _, ok := sp.VFRFPFirstSeen[fp.ACID]; !ok {
			sp.VFRFPFirstSeen[fp.ACID] = ctx.Now
		}
	}
	slices.SortFunc(vfr, func(a, b *sim.NASFlightPlan) int {
		return sp.VFRFPFirstSeen[a.ACID].Compare(sp.VFRFPFirstSeen[b.ACID])
	})

	return sp.drawSystemList(ctx, paneExtent, &ps.VFRList.Position, style, td, ld, ListFormatter{
		Title:      "VFR LIST",
		FrameTitle: "VFR LIST (TV)",
		Lines:      ps.VFRList.Lines,
		Entries:    len(vfr),
		FormatLine: func(idx int, sb *strings.Builder) {
			fp := vfr[idx]
			format := ctx.FacilityAdaptation.VFRList.Format
			// TODO: default after INDEX: + in-out-in flight, / dupe acid, * DM message on departure
			sb.WriteString(sp.formatListEntry(ctx, format, fp, nil))
		},
	})
}

func (sp *STARSPane) drawTABList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.TABList.Visible {
		return math.Extent2D{}
	}

	plans := util.FilterSlice(ctx.Client.State.UnassociatedFlightPlans,
		func(fp *sim.NASFlightPlan) bool {
			if seen, ok := sp.VFRFPFirstSeen[fp.ACID]; ok {
				// If it's a VFR still waiting for a NAS code, don't show it yet.
				if ctx.Now.Sub(seen) < 2*time.Second {
					return false
				}
			}

			if !fp.Location.IsZero() {
				// Unsupported DBs aren't included in the list.
				return false
			}

			if ctx.UserOwnsFlightPlan(fp) {
				return true
			}

			// TODO: should also include flight plans that we entered but
			// assigned a different initial controller to.

			if fp.InboundHandoffController == "" {
				// Only controlled by virtual
				return false
			}

			return ctx.UserControlsPosition(fp.InboundHandoffController)
		})

	// 2-92: default sort is by ACID
	slices.SortFunc(plans, func(a, b *sim.NASFlightPlan) int {
		return strings.Compare(string(a.ACID), string(b.ACID))
	})

	dupes := getDuplicateBeaconCodes(ctx)

	return sp.drawSystemList(ctx, paneExtent, &ps.TABList.Position, style, td, ld, ListFormatter{
		Title:      "FLIGHT PLAN",
		FrameTitle: "FLIGHT PLAN (T)",
		Lines:      ps.TABList.Lines,
		Entries:    len(plans),
		FormatLine: func(idx int, sb *strings.Builder) {
			fp := plans[idx]
			// TODO: after INDEX, + in-out-in flight, / dupe acid, * DM message on departure
			haveCode := fp.Rules == av.FlightRulesIFR || ctx.Now.Sub(sp.VFRFPFirstSeen[fp.ACID]) > 2*time.Second
			sb.WriteString(sp.formatListEntry(ctx, ctx.FacilityAdaptation.TABList.Format, fp, map[string]func() string{
				"DUPE_BEACON": func() string {
					if _, ok := dupes[fp.AssignedSquawk]; ok && haveCode {
						return "/"
					}
					return " "
				},
			}))
		},
	})
}

func (sp *STARSPane) drawAlertList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	// The alert list can't be hidden.
	var text strings.Builder
	var lists []string
	ps := sp.currentPrefs()

	if ps.DisableMSAW && ps.DisableCAWarnings && ps.DisableMCIWarnings {
		return math.Extent2D{}
	}

	var msaw []sim.Track
	if !ps.DisableMSAW {
		lists = append(lists, "LA")
		for _, trk := range sp.visibleTracks {
			if sp.TrackState[trk.ADSBCallsign].MSAW && trk.IsAssociated() && !trk.FlightPlan.DisableMSAW {
				msaw = append(msaw, trk)
			}
		}

		// Sort by start time
		slices.SortFunc(msaw, func(a, b sim.Track) int {
			sa, sb := sp.TrackState[a.ADSBCallsign], sp.TrackState[b.ADSBCallsign]
			return sa.MSAWStart.Compare(sb.MSAWStart)
		})
	}
	var ca, mci []CAAircraft
	if !ps.DisableCAWarnings {
		lists = append(lists, "CA")
		ca = sp.CAAircraft
		// TODO: filter out suppressed CA pairs
	}
	if !ps.DisableMCIWarnings {
		lists = append(lists, "MCI")
		mci = util.FilterSlice(sp.MCIAircraft, func(mci CAAircraft) bool {
			// remove suppressed ones
			trk0, ok0 := ctx.GetTrackByCallsign(mci.ADSBCallsigns[0])
			trk1, ok1 := ctx.GetTrackByCallsign(mci.ADSBCallsigns[1])
			return ok0 && ok1 && trk0.IsAssociated() && trk0.FlightPlan.MCISuppressedCode != trk1.Squawk
		})
	}

	n := len(msaw) + len(ca) + len(mci)
	if len(lists) > 0 {
		text.WriteString(strings.Join(lists, "/") + "\n")
		const alertListMaxLines = 50 // this is hard-coded
		if n > alertListMaxLines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", alertListMaxLines, n))
		}

		next := func() (*sim.Track, *CAAircraft, *CAAircraft) {
			if len(msaw) > 0 && (len(ca) == 0 || sp.TrackState[msaw[0].ADSBCallsign].MSAWStart.Before(ca[0].Start)) &&
				(len(mci) == 0 || sp.TrackState[msaw[0].ADSBCallsign].MSAWStart.Before(mci[0].Start)) {
				trk := msaw[0]
				msaw = msaw[1:]
				return &trk, nil, nil
			} else if len(ca) > 0 && (len(mci) == 0 || ca[0].Start.Before(mci[0].Start)) {
				r := &ca[0]
				ca = ca[1:]
				return nil, r, nil
			} else if len(mci) > 0 {
				r := &mci[0]
				mci = mci[1:]
				return nil, nil, r
			} else {
				return nil, nil, nil
			}
		}

		for range min(n, alertListMaxLines) {
			msawtrk, capair, mcipair := next()

			alt := func(trk *sim.Track) string {
				if trk.IsAssociated() && trk.FlightPlan.PilotReportedAltitude != 0 {
					return strconv.Itoa(trk.FlightPlan.PilotReportedAltitude/100) + "*"
				}
				return strconv.Itoa(int((trk.TransponderAltitude + 50) / 100))
			}

			// FIXME: should be using ACIDs for the second two cases.
			if msawtrk != nil {
				text.WriteString(fmt.Sprintf("%-13s%4s LA\n", msawtrk.FlightPlan.ACID, alt(msawtrk)))
			} else if capair != nil {
				text.WriteString(fmt.Sprintf("%-17s CA\n", capair.ADSBCallsigns[0]+"*"+capair.ADSBCallsigns[1]))
			} else if mcipair != nil {
				// For MCIs, the unassociated track is always the second callsign.
				// Beacon code is reported for MCI or blank if we don't have it.
				trk1, ok := ctx.GetTrackByCallsign(mcipair.ADSBCallsigns[1])
				if ok && trk1.Mode != av.TransponderModeStandby {
					text.WriteString(fmt.Sprintf("%-17s MCI\n", string(mcipair.ADSBCallsigns[0])+"*"+trk1.Squawk.String()))
				} else {
					text.WriteString(fmt.Sprintf("%-17s MCI\n", mcipair.ADSBCallsigns[0]+"*"))
				}
			} else {
				break
			}
		}

		if text.Len() > 0 {
			return sp.drawListText(ctx, paneExtent, &ps.AlertList.Position, text.String(),
				style, td, "ALERT LIST (TM)", ld)
		}
	}
	return math.Extent2D{}
}

func (sp *STARSPane) drawCoastList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	// Get suspended tracks (coast not yet supported)
	tracks := slices.Collect(util.FilterSeq(maps.Values(ctx.Client.State.Tracks),
		func(t *sim.Track) bool { return t.IsAssociated() && t.FlightPlan.Suspended }))
	// Sort by list index
	slices.SortFunc(tracks,
		func(a, b *sim.Track) int { return a.FlightPlan.CoastSuspendIndex - b.FlightPlan.CoastSuspendIndex })

	ps := sp.currentPrefs()
	return sp.drawSystemList(ctx, paneExtent, &ps.CoastList.Position, style, td, ld, ListFormatter{
		Title:      "COAST/SUSPEND",
		FrameTitle: "COAST/SUSPEND (TC)",
		Lines:      len(tracks), // Show all suspended tracks
		Entries:    len(tracks),
		FormatLine: func(idx int, sb *strings.Builder) {
			trk := tracks[idx]
			fp := trk.FlightPlan
			sb.WriteString(sp.formatListEntry(ctx, ctx.FacilityAdaptation.CoastSuspendList.Format, fp,
				map[string]func() string{
					"ALT": func() string {
						// For suspended, we always just show altitude (of one sort or another)
						if trk.Mode == av.TransponderModeAltitude {
							return fmt.Sprintf("%03d", int(trk.TransponderAltitude+50)/100)
						} else if fp.PilotReportedAltitude != 0 {
							return fmt.Sprintf("%03d", fp.PilotReportedAltitude)
						} else {
							return "RDR"
						}
					},
					"INDEX": func() string {
						return fmt.Sprintf("%2d", fp.CoastSuspendIndex)
					},
				}))
		},
	})
}

func (sp *STARSPane) drawMapsList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.VideoMapsList.Visible {
		return math.Extent2D{}
	}

	var text strings.Builder
	format := func(m sim.VideoMap) {
		if m.Label == "" {
			return
		}
		_, vis := ps.VideoMapVisible[m.Id]
		text.WriteString(util.Select(vis, ">", " "))
		text.WriteString(fmt.Sprintf("%3d ", m.Id))
		fmtlabel := func(s string) string {
			if len(s) > 8 {
				s = s[:8]
			}
			s = strings.ToUpper(s)
			s = strings.ReplaceAll(s, " ", "_")
			return s
		}
		text.WriteString(fmt.Sprintf("%-8s ", fmtlabel(m.Label)))
		text.WriteString(strings.ToUpper(m.Name) + "\n")
	}

	mapTitles := [VideoMapNumCategories]string{
		VideoMapGeographicMaps:     "GEOGRAPHIC MAPS",
		VideoMapControlledAirspace: "CONTROLLED AIRSPACE",
		VideoMapRunwayExtensions:   "RUNWAY EXTENSIONS",
		VideoMapDangerAreas:        "DANGER AREAS",
		VideoMapAerodromes:         "AERODROMES",
		VideoMapGeneralAviation:    "GENERAL AVIATION",
		VideoMapSIDsSTARs:          "SIDS/STARS",
		VideoMapMilitary:           "MILITARY",
		VideoMapGeographicPoints:   "GEOGRAPHIC POINTS",
		VideoMapProcessingAreas:    "PROCESSING AREAS",
		VideoMapCurrent:            "MAPS",
	}

	text.WriteString(mapTitles[ps.VideoMapsList.Selection])
	text.WriteByte('\n')
	var m []sim.VideoMap
	if ps.VideoMapsList.Selection == VideoMapCurrent {
		for _, vm := range sp.allVideoMaps {
			if _, ok := ps.VideoMapVisible[vm.Id]; ok {
				m = append(m, vm.VideoMap)
			}
		}
	} else {
		for _, vm := range sp.allVideoMaps {
			if vm.Category == int(ps.VideoMapsList.Selection) {
				m = append(m, vm.VideoMap)
			}
		}
	}

	// Sort by number
	slices.SortFunc(m, func(a, b sim.VideoMap) int { return a.Id - b.Id })

	// If more than 50, only display the first 50.
	if len(m) > 50 {
		m = m[:50]
	}

	for _, vm := range m {
		format(vm)
	}

	return sp.drawListText(ctx, paneExtent, &ps.VideoMapsList.Position, text.String(),
		style, td, "MAPS", ld)
}

func (sp *STARSPane) drawRestrictionAreasList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.RestrictionAreaList.Visible {
		return math.Extent2D{}
	}

	// Collect all restriction areas with their indices
	type indexedRA struct {
		ra  av.RestrictionArea
		idx int
	}
	var areas []indexedRA
	for i, ra := range ctx.Client.State.UserRestrictionAreas {
		if !ra.Deleted {
			areas = append(areas, indexedRA{ra, i + 1})
		}
	}
	for i, ra := range ctx.FacilityAdaptation.RestrictionAreas {
		if !ra.Deleted {
			areas = append(areas, indexedRA{ra, i + 101})
		}
	}

	return sp.drawSystemList(ctx, paneExtent, &ps.RestrictionAreaList.Position, style, td, ld, ListFormatter{
		Title:      "GEO RESTRICTIONS",
		FrameTitle: "GEO RESTRICTIONS (TRA)",
		Lines:      len(areas), // Show all restriction areas
		Entries:    len(areas),
		FormatLine: func(idx int, sb *strings.Builder) {
			area := areas[idx]
			if settings, ok := ps.RestrictionAreaSettings[area.idx]; ok && settings.Visible {
				sb.WriteByte('>')
			} else {
				sb.WriteByte(' ')
			}
			sb.WriteString(fmt.Sprintf("%-3d ", area.idx))
			if area.ra.Title != "" {
				sb.WriteString(strings.ToUpper(area.ra.Title))
			} else {
				sb.WriteString(strings.ToUpper(area.ra.Text[0]))
			}
		},
	})
}

func (sp *STARSPane) drawCRDAStatusList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.CRDAStatusList.Visible {
		return math.Extent2D{}
	}

	// Pre-compute the line data since it needs stateful processing
	var lines []string
	for i, crda := range ps.CRDA.RunwayPairState {
		var line strings.Builder
		if !crda.Enabled {
			line.WriteString(" ")
		} else {
			line.WriteString(util.Select(crda.Mode == CRDAModeStagger, "S", "T"))
		}

		pair := sp.ConvergingRunways[i]
		line.WriteString(strconv.Itoa(pair.Index))
		line.WriteByte(' ')
		line.WriteString(pair.Airport + " ")
		line.WriteString(pair.getRunwaysString())
		if crda.Enabled {
			for line.Len() < 16 {
				line.WriteByte(' ')
			}
			line.WriteString(string(ctx.UserPrimaryPosition()))
		}
		lines = append(lines, line.String())
	}

	return sp.drawSystemList(ctx, paneExtent, &ps.CRDAStatusList.Position, style, td, ld, ListFormatter{
		Title:      "CRDA STATUS",
		FrameTitle: "CRDA STATUS (TN)",
		Lines:      len(lines), // Show all CRDA pairs
		Entries:    len(lines),
		FormatLine: func(idx int, sb *strings.Builder) {
			sb.WriteString(lines[idx])
		},
	})
}

func (sp *STARSPane) drawMCISuppressionList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.MCISuppressionList.Visible {
		return math.Extent2D{}
	}

	// Filter tracks with MCI suppression
	mciTracks := util.FilterSlice(sp.visibleTracks, func(trk sim.Track) bool {
		return trk.IsAssociated() && trk.FlightPlan.MCISuppressedCode != av.Squawk(0)
	})

	return sp.drawSystemList(ctx, paneExtent, &ps.MCISuppressionList.Position, style, td, ld, ListFormatter{
		Title:      "MCI SUPPRESSION",
		FrameTitle: "MCI SUPPRESSION (TQ)",
		Lines:      len(mciTracks), // Show all MCI tracks
		Entries:    len(mciTracks),
		FormatLine: func(idx int, sb *strings.Builder) {
			trk := mciTracks[idx]
			fp := trk.FlightPlan
			sb.WriteString(sp.formatListEntry(ctx, ctx.FacilityAdaptation.MCISuppressionList.Format, fp,
				map[string]func() string{
					"SUPP_BEACON": func() string { return trk.FlightPlan.MCISuppressedCode.String() },
				}))
		},
	})
}

func (sp *STARSPane) drawTowerList(ctx *panes.Context, paneExtent math.Extent2D, airport string, towerIndex int,
	style renderer.TextStyle, td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	ps := sp.currentPrefs()
	loc := ctx.Client.State.Airports[airport].Location
	m := make(map[float32]string)
	for _, trk := range sp.visibleTracks {
		if trk.IsAssociated() && trk.ArrivalAirport == airport {
			dist := math.NMDistance2LL(loc, trk.Location)
			// We'll punt on the chance that two aircraft have the
			// exact same distance to the airport...
			m[dist] = sp.formatListEntry(ctx, ctx.FacilityAdaptation.TowerList.Format, trk.FlightPlan, nil)
		}
	}

	k := util.SortedMapKeys(m)

	return sp.drawSystemList(ctx, paneExtent, &ps.TowerLists[towerIndex].Position, style, td, ld, ListFormatter{
		Title:      stripK(airport) + " TOWER",
		FrameTitle: stripK(airport) + " TOWER (P" + strconv.Itoa(towerIndex+1) + ")",
		Lines:      ps.TowerLists[towerIndex].Lines,
		Entries:    len(k),
		FormatLine: func(idx int, sb *strings.Builder) {
			sb.WriteString(m[k[idx]])
		},
	})
}

func (sp *STARSPane) drawSignOnList(ctx *panes.Context, paneExtent math.Extent2D, style renderer.TextStyle, td *renderer.TextDrawBuilder,
	ld *renderer.ColoredLinesDrawBuilder) math.Extent2D {
	ps := sp.currentPrefs()
	if !ps.SignOnList.Visible {
		return math.Extent2D{}
	}

	if ctrl := ctx.UserController(); ctrl != nil {
		signOnTime := ctx.Client.SessionStats.SignOnTime
		initials := ctx.Client.SessionStats.Initials
		if initials != "" {
			initials = " " + initials
		}
		s := string(ctx.UserTCW) + initials + " " + signOnTime.UTC().Format("1504")
		return sp.drawListText(ctx, paneExtent, &ps.SignOnList.Position, s,
			style, td, "SIGN ON (TS)", ld)
	}
	return math.Extent2D{}
}

func (sp *STARSPane) drawCoordinationLists(ctx *panes.Context, paneExtent math.Extent2D, transforms radar.ScopeTransformations,
	td *renderer.TextDrawBuilder, ld *renderer.ColoredLinesDrawBuilder) []math.Extent2D {
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Lists)
	titleStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}

	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}

	releaseDepartures := ctx.Client.State.GetSTARSReleaseDepartures()

	var allBounds []math.Extent2D
	fa := ctx.FacilityAdaptation
	for i, cl := range fa.CoordinationLists {
		listStyle := renderer.TextStyle{
			Font:  font,
			Color: ps.Brightness.Lists.ScaleRGB(util.Select(cl.YellowEntries, renderer.RGB{1, 1, 0}, STARSListColor)),
		}
		dimStyle := renderer.TextStyle{
			Font:  font,
			Color: listStyle.Color.Scale(0.5),
		}

		// Auto-place the list if we haven't drawn it before
		list := ps.CoordinationLists[cl.Id]
		if list == nil {
			list = &CoordinationList{
				Group: cl.Name,
				BasicSTARSList: BasicSTARSList{
					Position: [2]float32{.25, .9 - .08*float32(i)},
					Lines:    10,
				},
			}
			if ps.CoordinationLists == nil {
				ps.CoordinationLists = make(map[string]*CoordinationList)
			}
			ps.CoordinationLists[cl.Id] = list
		}

		// Get the aircraft that should be included in this list: ones that
		// are from one of this list's departure airports and haven't been
		// deleted from the list by the controller.
		rel := util.FilterSlice(releaseDepartures,
			func(dep sim.ReleaseDeparture) bool {
				if !ctx.UserControlsPosition(dep.DepartureController) {
					return false
				}

				if !slices.Contains(cl.Airports, dep.DepartureAirport) {
					return false
				}
				for callsign, state := range sp.TrackState {
					if callsign == dep.ADSBCallsign {
						return !state.ReleaseDeleted
					}
				}
				return true // shouldn't get here
			})
		if len(rel) == 0 && !ps.DisplayEmptyCoordinationLists {
			continue
		}

		startPos := normalizedToWindow(list.Position)
		pw := startPos
		maxX := pw[0]

		halfSeconds := ctx.Now.UnixMilli() / 500
		blinkDim := halfSeconds&1 == 0

		if list.AutoRelease {
			pw = td.AddText(strings.ToUpper(cl.Name)+"    AUTO\n", pw, titleStyle)
		} else {
			pw = td.AddText(strings.ToUpper(cl.Name)+"\n", pw, titleStyle)
		}
		maxX = max(maxX, pw[0])
		if len(rel) > list.Lines {
			pw = td.AddText(fmt.Sprintf("MORE: %d/%d\n", list.Lines, len(rel)), pw, listStyle)
			maxX = max(maxX, pw[0])
		}
		var text strings.Builder
		for i := range min(len(rel), list.Lines) {
			dep := rel[i]
			text.Reset()
			text.WriteString("     ")
			if idx := slices.IndexFunc(ctx.Client.State.UnassociatedFlightPlans,
				func(fp *sim.NASFlightPlan) bool { return string(fp.ACID) == string(dep.ADSBCallsign) }); idx == -1 {
				text.WriteString(fmt.Sprintf(" %-10s NO FP", string(dep.ADSBCallsign)))
			} else {
				fp := ctx.Client.State.UnassociatedFlightPlans[idx]
				formattedEntry := sp.formatListEntry(ctx, cl.Format, fp, map[string]func() string{
					"ACKED": func() string { return util.Select(dep.Released, "+", " ") },
				})
				text.WriteString(formattedEntry)
				text.WriteString("\n")
				if !dep.Released && blinkDim {
					pw = td.AddText(rewriteDelta(text.String()), pw, dimStyle)
				} else {
					pw = td.AddText(rewriteDelta(text.String()), pw, listStyle)
				}
				maxX = max(maxX, pw[0])
			}
		}

		bounds := math.Extent2D{
			P0: [2]float32{startPos[0], pw[1]},
			P1: [2]float32{maxX, startPos[1]},
		}
		sp.handleListDrag(ctx, bounds, &list.Position, "coord:"+cl.Name, paneExtent, ld)
		if sp.showListFrames {
			sp.drawListFrame(ctx, bounds, strings.ToUpper(cl.Name), td, ld)
		}
		allBounds = append(allBounds, bounds)
	}

	return allBounds
}
