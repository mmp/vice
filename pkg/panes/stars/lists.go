// pkg/panes/stars/lists.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

func (sp *STARSPane) drawSystemLists(aircraft []*av.Aircraft, ctx *panes.Context, paneExtent math.Extent2D,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	transforms.LoadWindowViewingMatrices(cb)

	font := sp.systemFont[ps.CharSize.Lists]
	listStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}

	sp.drawPreviewArea(normalizedToWindow(ps.PreviewAreaPosition), font, td)

	sp.drawSSAList(ctx, normalizedToWindow(ps.SSAList.Position), aircraft, td, transforms, cb)
	sp.drawVFRList(ctx, normalizedToWindow(ps.VFRList.Position), aircraft, listStyle, td)
	sp.drawTABList(ctx, normalizedToWindow(ps.TABList.Position), aircraft, listStyle, td)
	sp.drawAlertList(ctx, normalizedToWindow(ps.AlertList.Position), aircraft, listStyle, td)
	sp.drawCoastList(ctx, normalizedToWindow(ps.CoastList.Position), listStyle, td)
	sp.drawMapsList(ctx, normalizedToWindow(ps.VideoMapsList.Position), listStyle, td)
	sp.drawRestrictionAreasList(ctx, normalizedToWindow(ps.RestrictionAreaList.Position), listStyle, td)
	sp.drawCRDAStatusList(ctx, normalizedToWindow(ps.CRDAStatusList.Position), aircraft, listStyle, td)

	towerListAirports := ctx.ControlClient.TowerListAirports()
	for i, tl := range ps.TowerLists {
		if tl.Visible && i < len(towerListAirports) {
			sp.drawTowerList(ctx, normalizedToWindow(tl.Position), towerListAirports[i], tl.Lines,
				aircraft, listStyle, td)
		}
	}

	sp.drawSignOnList(ctx, normalizedToWindow(ps.SignOnList.Position), listStyle, td)
	sp.drawCoordinationLists(ctx, paneExtent, transforms, cb)

	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawPreviewArea(pw [2]float32, font *renderer.Font, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()

	var text strings.Builder
	text.WriteString(sp.previewAreaOutput)
	text.WriteByte('\n')

	switch sp.commandMode {
	case CommandModeInitiateControl:
		text.WriteString("IC\n")
	case CommandModeTerminateControl:
		text.WriteString("TC\n")
	case CommandModeHandOff:
		text.WriteString("HD\n")
	case CommandModeVFRPlan:
		text.WriteString("VP\n")
	case CommandModeMultiFunc:
		text.WriteString("F")
		text.WriteString(sp.multiFuncPrefix)
		text.WriteString("\n")
	case CommandModeFlightData:
		text.WriteString("DA\n")
	case CommandModeCollisionAlert:
		text.WriteString("CA\n")
	case CommandModeMin:
		text.WriteString("MIN\n")
	case CommandModeMaps:
		text.WriteString("MAP\n")
	case CommandModeSavePrefAs:
		text.WriteString("PREF SET NAME\n")
	case CommandModeLDR:
		text.WriteString("LLL\n")
	case CommandModeRangeRings:
		text.WriteString("RR\n")
	case CommandModeRange:
		text.WriteString("RANGE\n")
	case CommandModeSiteMenu:
		text.WriteString("SITE\n")
	case CommandModeWX:
		text.WriteString("WX\n")
	case CommandModePref:
		text.WriteString("PREF SET\n")
	case CommandModeReleaseDeparture:
		text.WriteString("RD\n")
	case CommandModeRestrictionArea:
		text.WriteString("AR\n")
	case CommandModeTargetGen:
		text.WriteString("TG ")
		text.WriteString(sp.targetGenLastCallsign)
		text.WriteString("\n")
	}
	text.WriteString(strings.Join(strings.Fields(sp.previewAreaInput), "\n")) // spaces are rendered as newlines
	if text.Len() > 0 {
		style := renderer.TextStyle{
			Font:  font,
			Color: ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
		}
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) getTabListIndex(ac *av.Aircraft) string {
	state := sp.Aircraft[ac.Callsign]
	if state.TabListIndex == TabListUnassignedIndex {
		// Try to assign a Tab list index
		for i := range TabListEntries {
			idx := (sp.TabListSearchStart + i) % TabListEntries
			if sp.TabListAircraft[idx] == "" {
				state.TabListIndex = idx
				sp.TabListAircraft[idx] = ac.Callsign
				sp.TabListSearchStart = idx + 1
				break
			}
		}
	}

	if state.TabListIndex != TabListUnassignedIndex {
		return fmt.Sprintf("%2d", state.TabListIndex)
	} else {
		return "  " // no tab list number assigned
	}
}

func (sp *STARSPane) drawSSAList(ctx *panes.Context, pw [2]float32, aircraft []*av.Aircraft, td *renderer.TextDrawBuilder,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	font := sp.systemFont[ps.CharSize.Lists]
	listStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}
	alertStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor),
	}

	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	formatMETAR := func(ap string, metar *av.METAR) string {
		alt := strings.TrimPrefix(metar.Altimeter, "A")
		if len(alt) == 4 {
			alt = alt[:2] + "." + alt[2:]
		}
		return stripK(ap) + " " + alt
	}

	x := pw[0]
	newline := func() {
		pw[0] = x
		pw[1] -= float32(font.Size)
	}

	// Inverted red triangle and green box...
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	pIndicator := math.Add2f(pw, [2]float32{5, 0})
	tv := math.EquilateralTriangleVertices(7)
	for i := range tv {
		tv[i] = math.Add2f(pIndicator, math.Scale2f(tv[i], -1))
	}
	trid.AddTriangle(tv[0], tv[1], tv[2], ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor))
	trid.GenerateCommands(cb)

	square := [][2]float32{[2]float32{-5, -5}, [2]float32{5, -5}, [2]float32{5, 5}, [2]float32{-5, 5}}
	square = util.MapSlice(square, func(p [2]float32) [2]float32 { return math.Add2f(p, pIndicator) })
	ld.AddLineLoop(ps.Brightness.Lists.ScaleRGB(STARSListColor), square)
	ld.GenerateCommands(cb)

	pw[1] -= 10

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
			text += ctx.ControlClient.CurrentTime().UTC().Format("1504/05 ")
		}
		if filter.All || filter.Altimeter {
			if metar := ctx.ControlClient.METAR[ctx.ControlClient.PrimaryAirport]; metar != nil {
				text += formatMETAR(ctx.ControlClient.PrimaryAirport, metar)
			}
		}
		td.AddText(text, pw, listStyle)
		newline()
	}

	// ATIS/GI text. (Note that per 4-44 filter.All does not apply to GI text.)
	if filter.Text.Main && (ps.ATIS != "" || ps.GIText[0] != "") {
		pw = td.AddText(strings.Join([]string{ps.ATIS, ps.GIText[0]}, " "), pw, listStyle)
		newline()
	}
	for i := 1; i < len(ps.GIText); i++ {
		if filter.Text.GI[i] && ps.GIText[i] != "" {
			pw = td.AddText(ps.GIText[i], pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.Status || filter.Radar {
		if filter.All || filter.Status {
			if ctx.ControlClient.Connected() {
				pw = td.AddText("OK/OK/NA ", pw, listStyle)
			} else {
				pw = td.AddText("NA/NA/NA ", pw, alertStyle)
			}
		}
		if filter.All || filter.Radar {
			pw = td.AddText(sp.radarSiteId(ctx.ControlClient.RadarSites), pw, listStyle)
		}
		newline()
	}

	if filter.All || filter.Codes {
		if len(ps.SelectedBeaconCodes) > 0 {
			pw = td.AddText(strings.Join(ps.SelectedBeaconCodes, " "), pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.SpecialPurposeCodes {
		// Special purpose codes listed in red, if anyone is squawking
		// those.
		codes := make(map[string]interface{})
		for _, ac := range aircraft {
			if ac.SPCOverride != "" {
				codes[ac.SPCOverride] = nil
			}
			if ok, code := av.SquawkIsSPC(ac.Squawk); ok {
				codes[code] = nil
			}
		}

		if len(codes) > 0 {
			td.AddText(strings.Join(util.SortedMapKeys(codes), " "), pw, alertStyle)
			newline()
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
		airports := util.SortedMapKeys(ctx.ControlClient.Airports)
		// Sort via 1. primary? 2. tower list index, 3. alphabetic
		sort.Slice(airports, func(i, j int) bool {
			if airports[i] == ctx.ControlClient.PrimaryAirport {
				return true
			} else if airports[j] == ctx.ControlClient.PrimaryAirport {
				return false
			} else {
				a, b := ctx.ControlClient.Airports[airports[i]], ctx.ControlClient.Airports[airports[j]]
				ai := util.Select(a.TowerListIndex != 0, a.TowerListIndex, 1000)
				bi := util.Select(b.TowerListIndex != 0, b.TowerListIndex, 1000)
				if ai != bi {
					return ai < bi
				}
			}
			return airports[i] < airports[j]
		})

		// 2-78: apparently it's limited to 6 airports; there are also
		// some nuances about automatically-entered versus manually
		// entered, stale entries, and a possible "*" for airports
		// where "instrument approach statistics are maintained".
		var altimeters []string
		for _, icao := range airports {
			if metar := ctx.ControlClient.METAR[icao]; metar != nil {
				altimeters = append(altimeters, formatMETAR(icao, metar))
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
			pw = td.AddText("WX HIST:"+strconv.Itoa(sp.wxHistoryDraw), pw, listStyle)
			newline()
		}
	}

	if (filter.All || filter.QuickLookPositions) && (ps.QuickLookAll || len(ps.QuickLookPositions) > 0) {
		if ps.QuickLookAll {
			if ps.QuickLookAllIsPlus {
				pw = td.AddText("QL: ALL+", pw, listStyle)
			} else {
				pw = td.AddText("QL: ALL", pw, listStyle)
			}
		} else {
			pos := util.MapSlice(ps.QuickLookPositions,
				func(q QuickLookPosition) string {
					return q.Id + util.Select(q.Plus, "+", "")
				})

			pw = td.AddText("QL: "+strings.Join(pos, " "), pw, listStyle)
		}
		newline()
	}

	if filter.All || filter.DisabledTerminal {
		var disabled []string
		if ps.DisableCAWarnings {
			disabled = append(disabled, "CA")
		}
		if ps.CRDA.Disabled {
			disabled = append(disabled, "CRDA")
		}
		if ps.DisableMSAW {
			disabled = append(disabled, "MSAW")
		}
		// TODO: others?
		if len(disabled) > 0 {
			text := "TW OFF: " + strings.Join(disabled, " ")
			pw = td.AddText(text, pw, listStyle)
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
}

func (sp *STARSPane) drawVFRList(ctx *panes.Context, pw [2]float32, aircraft []*av.Aircraft, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.VFRList.Visible {
		return
	}

	var text strings.Builder
	var vfr []*av.Aircraft
	// Find all untracked av.VFR aircraft
	// FIXME: this should actually be based on VFR flight plans
	for _, ac := range aircraft {
		if ac.Squawk == av.Squawk(0o1200) && ac.TrackingController == "" {
			vfr = append(vfr, ac)
		}
	}

	// FIXME: this should actually be sorted by when we first saw the aircraft
	slices.SortFunc(vfr, func(a, b *av.Aircraft) int { return strings.Compare(a.Callsign, b.Callsign) })

	text.WriteString("VFR LIST\n")
	if len(vfr) > ps.VFRList.Lines {
		text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.VFRList.Lines, len(vfr)))
	}
	for i := range math.Min(len(vfr), ps.VFRList.Lines) {
		ac := vfr[i]
		text.WriteString(fmt.Sprintf("%s %-7s VFR\n", sp.getTabListIndex(ac), ac.Callsign))
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawTABList(ctx *panes.Context, pw [2]float32, aircraft []*av.Aircraft, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.TABList.Visible {
		return
	}

	var text strings.Builder
	var dep []*av.Aircraft
	// Untracked departures departing from one of the airports we're
	// responsible for.
	for _, ac := range aircraft {
		if fp := ac.FlightPlan; fp != nil && ac.TrackingController == "" {
			if ap := ctx.ControlClient.DepartureAirports[fp.DepartureAirport]; ap != nil {
				if ctx.ControlClient.DepartureController(ac, ctx.Lg) == ctx.ControlClient.Callsign {
					dep = append(dep, ac)
				}
			}
		}
	}

	slices.SortFunc(dep, func(a, b *av.Aircraft) int { return strings.Compare(a.Callsign, b.Callsign) })

	text.WriteString("FLIGHT PLAN\n")
	if len(dep) > ps.TABList.Lines {
		text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.TABList.Lines, len(dep)))
	}
	for i := range math.Min(len(dep), ps.TABList.Lines) {
		ac := dep[i]
		text.WriteString(fmt.Sprintf("%s %-7s %s\n", sp.getTabListIndex(ac), ac.Callsign, ac.Squawk.String()))
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawAlertList(ctx *panes.Context, pw [2]float32, aircraft []*av.Aircraft, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	// The alert list can't be hidden.
	var text strings.Builder
	var lists []string
	n := 0 // total number of aircraft in the mix
	ps := sp.currentPrefs()

	if !ps.DisableMSAW {
		lists = append(lists, "LA")
		for _, ac := range aircraft {
			if sp.Aircraft[ac.Callsign].MSAW {
				n++
			}
		}
	}
	if !ps.DisableCAWarnings {
		lists = append(lists, "CA")
		n += len(sp.CAAircraft)
	}

	if len(lists) > 0 {
		text.WriteString(strings.Join(lists, "/") + "\n")
		const alertListMaxLines = 50 // this is hard-coded
		if n > alertListMaxLines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", alertListMaxLines, n))
		}

		// LA
		if !ps.DisableMSAW {
			for _, ac := range aircraft {
				if n == 0 {
					break
				}
				if sp.Aircraft[ac.Callsign].MSAW {
					text.WriteString(fmt.Sprintf("%-14s%03d LA\n", ac.Callsign, int((ac.Altitude()+50)/100)))
					n--
				}
			}
		}

		// CA
		if !ps.DisableCAWarnings {
			for _, pair := range sp.CAAircraft {
				if n == 0 {
					break
				}

				text.WriteString(fmt.Sprintf("%-17s CA\n", pair.Callsigns[0]+"*"+pair.Callsigns[1]))
				n--
			}
		}

		if text.Len() > 0 {
			td.AddText(text.String(), pw, style)
		}
	}
}

func (sp *STARSPane) drawCoastList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	// TODO
	td.AddText("COAST/SUSPEND", pw, style)
}

func (sp *STARSPane) drawMapsList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.VideoMapsList.Visible {
		return
	}

	var text strings.Builder
	format := func(m av.VideoMap) {
		if m.Label == "" {
			return
		}
		_, vis := ps.VideoMapVisible[m.Id]
		text.WriteString(util.Select(vis, ">", " "))
		text.WriteString(fmt.Sprintf("%3d ", m.Id))
		text.WriteString(fmt.Sprintf("%-8s ", strings.ToUpper(m.Label)))
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
	var m []av.VideoMap
	if ps.VideoMapsList.Selection == VideoMapCurrent {
		for _, vm := range sp.allVideoMaps {
			if _, ok := ps.VideoMapVisible[vm.Id]; ok {
				m = append(m, vm)
			}
		}
	} else {
		for _, vm := range sp.allVideoMaps {
			if vm.Category == int(ps.VideoMapsList.Selection) {
				m = append(m, vm)
			}
		}
	}

	// Sort by number
	slices.SortFunc(m, func(a, b av.VideoMap) int { return a.Id - b.Id })

	// If more than 50, only display the first 50.
	if len(m) > 50 {
		m = m[:50]
	}

	for _, vm := range m {
		format(vm)
	}

	td.AddText(text.String(), pw, style)
}

func (sp *STARSPane) drawRestrictionAreasList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.RestrictionAreaList.Visible {
		return
	}

	var text strings.Builder
	text.WriteString("GEO RESTRICTIONS\n")

	add := func(ra sim.RestrictionArea, idx int) {
		if ra.Deleted {
			return
		}
		if settings, ok := ps.RestrictionAreaSettings[idx]; ok && settings.Visible {
			text.WriteByte('>')
		} else {
			text.WriteByte(' ')
		}
		text.WriteString(fmt.Sprintf("%-3d ", idx))
		if ra.Title != "" {
			text.WriteString(strings.ToUpper(ra.Title))
		} else {
			text.WriteString(strings.ToUpper(ra.Text[0]))
		}
		text.WriteByte('\n')
	}

	for i, ra := range ctx.ControlClient.State.UserRestrictionAreas {
		add(ra, i+1)
	}
	for i, ra := range ctx.ControlClient.State.STARSFacilityAdaptation.RestrictionAreas {
		add(ra, i+101)
	}

	td.AddText(text.String(), pw, style)
}

func (sp *STARSPane) drawCRDAStatusList(ctx *panes.Context, pw [2]float32, aircraft []*av.Aircraft, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.CRDAStatusList.Visible {
		return
	}

	var text strings.Builder
	text.WriteString("CRDA STATUS\n")
	pairIndex := 0 // reset for each new airport
	currentAirport := ""
	for i, crda := range ps.CRDA.RunwayPairState {
		if !crda.Enabled {
			text.WriteString(" ")
		} else {
			text.WriteString(util.Select(crda.Mode == CRDAModeStagger, "S", "T"))
		}

		pair := sp.ConvergingRunways[i]
		ap := pair.Airport
		if ap != currentAirport {
			currentAirport = ap
			pairIndex = 1
		}

		text.WriteString(strconv.Itoa(pairIndex))
		text.WriteByte(' ')
		pairIndex++
		text.WriteString(ap + " ")
		text.WriteString(pair.getRunwaysString())
		if crda.Enabled {
			for text.Len() < 16 {
				text.WriteByte(' ')
			}
			ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
			text.WriteString(ctrl.SectorId)
		}
		text.WriteByte('\n')
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawTowerList(ctx *panes.Context, pw [2]float32, airport string, lines int, aircraft []*av.Aircraft,
	style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	var text strings.Builder
	loc := ctx.ControlClient.ArrivalAirports[airport].Location
	text.WriteString(stripK(airport) + " TOWER\n")
	m := make(map[float32]string)
	for _, ac := range aircraft {
		if ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == airport {
			dist := math.NMDistance2LL(loc, sp.Aircraft[ac.Callsign].TrackPosition())
			actype := ac.FlightPlan.TypeWithoutSuffix()
			actype = strings.TrimPrefix(actype, "H/")
			actype = strings.TrimPrefix(actype, "S/")
			// We'll punt on the chance that two aircraft have the
			// exact same distance to the airport...
			m[dist] = fmt.Sprintf("%-7s %s", ac.Callsign, actype)
		}
	}

	k := util.SortedMapKeys(m)
	if len(k) > lines {
		k = k[:lines]
	}

	for _, key := range k {
		text.WriteString(m[key] + "\n")
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawSignOnList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.SignOnList.Visible {
		return
	}

	var text strings.Builder
	if ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]; ctrl != nil {
		text.WriteString(ctrl.SectorId + " " + ctrl.SignOnTime.UTC().Format("1504")) // TODO: initials
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawCoordinationLists(ctx *panes.Context, paneExtent math.Extent2D, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	font := sp.systemFont[ps.CharSize.Lists]
	titleStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}

	releaseAircraft := ctx.ControlClient.State.GetReleaseDepartures()

	fa := ctx.ControlClient.STARSFacilityAdaptation
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
		aircraft := util.FilterSlice(releaseAircraft,
			func(ac *av.Aircraft) bool {
				return slices.Contains(cl.Airports, ac.FlightPlan.DepartureAirport) &&
					!sp.Aircraft[ac.Callsign].ReleaseDeleted
			})
		if len(aircraft) == 0 && !ps.DisplayEmptyCoordinationLists {
			continue
		}

		pw := normalizedToWindow(list.Position)

		halfSeconds := ctx.Now.UnixMilli() / 500
		blinkDim := halfSeconds&1 == 0

		if list.AutoRelease {
			pw = td.AddText(strings.ToUpper(cl.Name)+"    AUTO\n", pw, titleStyle)
		} else {
			pw = td.AddText(strings.ToUpper(cl.Name)+"\n", pw, titleStyle)
		}
		if len(aircraft) > list.Lines {
			pw = td.AddText(fmt.Sprintf("MORE: %d/%d\n", list.Lines, len(aircraft)), pw, listStyle)
		}
		var text strings.Builder
		for i := range math.Min(len(aircraft), list.Lines) {
			ac := aircraft[i]
			text.Reset()
			trk := sp.getTrack(ctx, ac)
			// TODO: NO FP if no flight plan
			text.WriteString("     " + sp.getTabListIndex(ac))
			text.WriteString(util.Select(ac.Released, "+", " "))
			text.WriteString(fmt.Sprintf(" %-10s %5s %s %5s %03d\n", ac.Callsign, ac.FlightPlan.BaseType(),
				ac.Squawk, trk.SP1, ac.FlightPlan.Altitude/100))
			if !ac.Released && blinkDim {
				pw = td.AddText(text.String(), pw, dimStyle)
			} else {
				pw = td.AddText(text.String(), pw, listStyle)
			}
		}
	}

	td.GenerateCommands(cb)
}
