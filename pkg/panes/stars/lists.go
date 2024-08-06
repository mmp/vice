// pkg/panes/stars/lists.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

func (sp *STARSPane) drawSystemLists(aircraft []*av.Aircraft, ctx *panes.Context, paneExtent math.Extent2D,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.CurrentPreferenceSet

	transforms.LoadWindowViewingMatrices(cb)

	font := sp.systemFont[ps.CharSize.Lists]
	style := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}
	alertStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSTextAlertColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}
	drawList := func(text string, p [2]float32) {
		if text != "" {
			pw := normalizedToWindow(p)
			td.AddText(text, pw, style)
		}
	}

	// Do the preview area while we're at it
	pt := sp.previewAreaOutput + "\n"
	switch sp.commandMode {
	case CommandModeInitiateControl:
		pt += "IC\n"
	case CommandModeTerminateControl:
		pt += "TC\n"
	case CommandModeHandOff:
		pt += "HD\n"
	case CommandModeVFRPlan:
		pt += "VP\n"
	case CommandModeMultiFunc:
		pt += "F" + sp.multiFuncPrefix + "\n"
	case CommandModeFlightData:
		pt += "DA\n"
	case CommandModeCollisionAlert:
		pt += "CA\n"
	case CommandModeMin:
		pt += "MIN\n"
	case CommandModeMaps:
		pt += "MAP\n"
	case CommandModeSavePrefAs:
		pt += "SAVE AS\n"
	case CommandModeLDR:
		pt += "LLL\n"
	case CommandModeRangeRings:
		pt += "RR\n"
	case CommandModeRange:
		pt += "RANGE\n"
	case CommandModeSiteMenu:
		pt += "SITE\n"
	}
	pt += strings.Join(strings.Fields(sp.previewAreaInput), "\n") // spaces are rendered as newlines
	drawList(pt, ps.PreviewAreaPosition)

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

	if ps.SSAList.Visible {
		pw := normalizedToWindow(ps.SSAList.Position)
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
			td.AddText(text, pw, style)
			newline()
		}

		// ATIS and GI text always, apparently
		if ps.CurrentATIS != "" {
			pw = td.AddText(ps.CurrentATIS+" "+ps.GIText[0], pw, style)
			newline()
		} else if ps.GIText[0] != "" {
			pw = td.AddText(ps.GIText[0], pw, style)
			newline()
		}
		for i := 1; i < len(ps.GIText); i++ {
			if txt := ps.GIText[i]; txt != "" {
				pw = td.AddText(txt, pw, style)
				newline()
			}
		}

		if filter.All || filter.Status || filter.Radar {
			if filter.All || filter.Status {
				if ctx.ControlClient.Connected() {
					pw = td.AddText("OK/OK/NA ", pw, style)
				} else {
					pw = td.AddText("NA/NA/NA ", pw, alertStyle)
				}
			}
			if filter.All || filter.Radar {
				pw = td.AddText(sp.radarSiteId(ctx.ControlClient.RadarSites), pw, style)
			}
			newline()
		}

		if filter.All || filter.Codes {
			if len(ps.SelectedBeaconCodes) > 0 {
				pw = td.AddText(strings.Join(ps.SelectedBeaconCodes, " "), pw, style)
				newline()
			}
		}

		if filter.All || filter.SpecialPurposeCodes {
			// Special purpose codes listed in red, if anyone is squawking
			// those.
			codes := make(map[string]interface{})
			for _, ac := range aircraft {
				for code := range ac.SPCOverrides {
					codes[code] = nil
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
			pw = td.AddText(text, pw, style)
			newline()
		}

		if filter.All || filter.AltitudeFilters {
			af := ps.AltitudeFilters
			text := fmt.Sprintf("%03d %03d U %03d %03d A",
				af.Unassociated[0]/100, af.Unassociated[1]/100,
				af.Associated[0]/100, af.Associated[1]/100)
			pw = td.AddText(text, pw, style)
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
				pw = td.AddText(strings.Join(altimeters[:3], " "), pw, style)
				altimeters = altimeters[3:]
				newline()
			}
			if len(altimeters) > 0 {
				pw = td.AddText(strings.Join(altimeters, " "), pw, style)
				newline()
			}
		}

		if (filter.All || filter.QuickLookPositions) && (ps.QuickLookAll || len(ps.QuickLookPositions) > 0) {
			if ps.QuickLookAll {
				if ps.QuickLookAllIsPlus {
					pw = td.AddText("QL: ALL+", pw, style)
				} else {
					pw = td.AddText("QL: ALL", pw, style)
				}
			} else {
				pos := util.MapSlice(ps.QuickLookPositions,
					func(q QuickLookPosition) string {
						return q.Id + util.Select(q.Plus, "+", "")
					})

				pw = td.AddText("QL: "+strings.Join(pos, " "), pw, style)
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
				pw = td.AddText(text, pw, style)
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

				pw = td.AddText(text, pw, style)
				newline()
			}
		}
	}

	var text strings.Builder
	if ps.VFRList.Visible {
		text.Reset()
		vfr := make(map[int]*av.Aircraft)
		// Find all untracked av.VFR aircraft
		for _, ac := range aircraft {
			if ac.Squawk == av.Squawk(0o1200) && ac.TrackingController == "" {
				vfr[sp.getAircraftIndex(ac)] = ac
			}
		}

		text.WriteString("VFR LIST\n")
		if len(vfr) > ps.VFRList.Lines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.VFRList.Lines, len(vfr)))
		}
		for i, acIdx := range util.SortedMapKeys(vfr) {
			ac := vfr[acIdx]
			text.WriteString(fmt.Sprintf("%2d %-7s av.VFR\n", acIdx, ac.Callsign))

			// Limit to the user limit
			if i == ps.VFRList.Lines {
				break
			}
		}

		drawList(text.String(), ps.VFRList.Position)
	}

	if ps.TABList.Visible {
		text.Reset()
		dep := make(map[int]*av.Aircraft)
		// Untracked departures departing from one of our airports
		for _, ac := range aircraft {
			if fp := ac.FlightPlan; fp != nil && ac.TrackingController == "" {
				if ap := ctx.ControlClient.DepartureAirports[fp.DepartureAirport]; ap != nil {
					dep[sp.getAircraftIndex(ac)] = ac
					break
				}
			}
		}

		text.WriteString("FLIGHT PLAN\n")
		if len(dep) > ps.TABList.Lines {
			text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.TABList.Lines, len(dep)))
		}
		for i, acIdx := range util.SortedMapKeys(dep) {
			ac := dep[acIdx]
			text.WriteString(fmt.Sprintf("%2d %-7s %s\n", acIdx, ac.Callsign, ac.Squawk.String()))

			// Limit to the user limit
			if i == ps.TABList.Lines {
				break
			}
		}

		drawList(text.String(), ps.TABList.Position)
	}

	if ps.AlertList.Visible {
		text.Reset()
		var lists []string
		n := 0 // total number of aircraft in the mix
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
			if n > ps.AlertList.Lines {
				text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.AlertList.Lines, n))
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

			drawList(text.String(), ps.AlertList.Position)
		}
	}

	if ps.CoastList.Visible {
		text := "COAST/SUSPEND"
		// TODO
		drawList(text, ps.CoastList.Position)
	}

	if ps.VideoMapsList.Visible {
		text.Reset()
		format := func(m av.VideoMap, i int, vis bool) {
			text.WriteString(util.Select(vis, ">", " ") + " ")
			text.WriteString(fmt.Sprintf("%3d ", i))
			text.WriteString(fmt.Sprintf("%-8s ", strings.ToUpper(m.Label)))
			text.WriteString(strings.ToUpper(m.Name) + "\n")
		}
		if ps.VideoMapsList.Selection == VideoMapsGroupGeo {
			text.WriteString("GEOGRAPHIC MAPS\n")
			videoMaps, _ := ctx.ControlClient.GetVideoMaps()
			for i, m := range videoMaps {
				if m.Id != 0 {
					format(m, m.Id, ps.DisplayVideoMap[i])
				}
			}
		} else if ps.VideoMapsList.Selection == VideoMapsGroupSysProc {
			text.WriteString("PROCESSING AREAS\n")
			for _, index := range util.SortedMapKeys(sp.systemMaps) {
				_, vis := ps.SystemMapVisible[index]
				format(*sp.systemMaps[index], index, vis)
			}
		} else if ps.VideoMapsList.Selection == VideoMapsGroupCurrent {
			text.WriteString("MAPS\n")
			videoMaps, _ := ctx.ControlClient.GetVideoMaps()
			for i, vis := range ps.DisplayVideoMap {
				if vis {
					format(videoMaps[i], videoMaps[i].Id, vis)
				}
			}
		} else {
			ctx.Lg.Errorf("%d: unhandled VideoMapsList.Selection", ps.VideoMapsList.Selection)
		}

		drawList(text.String(), ps.VideoMapsList.Position)
	}

	if ps.CRDAStatusList.Visible {
		text.Reset()
		text.WriteString("CRDA STATUS\n")
		pairIndex := 0 // reset for each new airport
		currentAirport := ""
		var line strings.Builder
		for i, crda := range ps.CRDA.RunwayPairState {
			line.Reset()
			if !crda.Enabled {
				line.WriteString(" ")
			} else {
				line.WriteString(util.Select(crda.Mode == CRDAModeStagger, "S", "T"))
			}

			pair := sp.ConvergingRunways[i]
			ap := pair.Airport
			if ap != currentAirport {
				currentAirport = ap
				pairIndex = 1
			}

			line.WriteString(strconv.Itoa(pairIndex))
			line.WriteByte(' ')
			pairIndex++
			line.WriteString(ap + " ")
			line.WriteString(pair.getRunwaysString())
			if crda.Enabled {
				for line.Len() < 16 {
					line.WriteByte(' ')
				}
				ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
				line.WriteString(ctrl.SectorId)
			}
			line.WriteByte('\n')
			text.WriteString(line.String())
		}
		drawList(text.String(), ps.CRDAStatusList.Position)
	}

	// Figure out airport<-->tower list assignments. Sort the airports
	// according to their TowerListIndex, putting zero (i.e., unassigned)
	// indices at the end. Break ties alphabetically by airport name. The
	// first three then are assigned to the corresponding tower list.
	towerListAirports := util.SortedMapKeys(ctx.ControlClient.ArrivalAirports)
	sort.Slice(towerListAirports, func(a, b int) bool {
		ai := ctx.ControlClient.ArrivalAirports[towerListAirports[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := ctx.ControlClient.ArrivalAirports[towerListAirports[b]].TowerListIndex
		if bi == 0 {
			bi = 1000
		}
		if ai == bi {
			return a < b
		}
		return ai < bi
	})

	for i, tl := range ps.TowerLists {
		if !tl.Visible || i >= len(towerListAirports) {
			continue
		}

		text.Reset()
		ap := towerListAirports[i]
		loc := ctx.ControlClient.ArrivalAirports[ap].Location
		text.WriteString(stripK(ap) + " TOWER\n")
		m := make(map[float32]string)
		for _, ac := range aircraft {
			if ac.FlightPlan != nil && ac.FlightPlan.ArrivalAirport == ap {
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
		if len(k) > tl.Lines {
			k = k[:tl.Lines]
		}

		for _, key := range k {
			text.WriteString(m[key] + "\n")
		}
		drawList(text.String(), tl.Position)
	}

	if ps.SignOnList.Visible {
		text.Reset()
		format := func(ctrl *av.Controller) {
			id := ctrl.SectorId
			if ctrl.FacilityIdentifier != "" && !ctrl.ERAMFacility {
				id = STARSTriangleCharacter + ctrl.FacilityIdentifier + id
			}
			text.WriteString(fmt.Sprintf("%4s", id) + " " + ctrl.Frequency.String() + " " +
				ctrl.Callsign + util.Select(ctrl.IsHuman, "*", "") + "\n")
		}

		// User first
		userCtrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
		if userCtrl != nil {
			format(userCtrl)
		}

		for _, callsign := range util.SortedMapKeys(ctx.ControlClient.Controllers) {
			if ctrl := ctx.ControlClient.Controllers[callsign]; ctrl != userCtrl {
				format(ctrl)
			}
		}

		drawList(text.String(), ps.SignOnList.Position)
	}

	td.GenerateCommands(cb)
}
