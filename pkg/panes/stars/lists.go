// pkg/panes/stars/lists.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

func (sp *STARSPane) drawSystemLists(ctx *panes.Context, tracks []sim.Track, paneExtent math.Extent2D,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	transforms.LoadWindowViewingMatrices(cb)

	font := sp.systemFont(ctx, ps.CharSize.Lists)
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

	sp.drawSSAList(ctx, normalizedToWindow(ps.SSAList.Position), tracks, td, transforms, cb)
	sp.drawVFRList(ctx, normalizedToWindow(ps.VFRList.Position), tracks, listStyle, td)
	sp.drawTABList(ctx, normalizedToWindow(ps.TABList.Position), tracks, listStyle, td)
	sp.drawAlertList(ctx, normalizedToWindow(ps.AlertList.Position), tracks, listStyle, td)
	sp.drawCoastList(ctx, normalizedToWindow(ps.CoastList.Position), listStyle, td)
	sp.drawMapsList(ctx, normalizedToWindow(ps.VideoMapsList.Position), listStyle, td)
	sp.drawRestrictionAreasList(ctx, normalizedToWindow(ps.RestrictionAreaList.Position), listStyle, td)
	sp.drawCRDAStatusList(ctx, normalizedToWindow(ps.CRDAStatusList.Position), tracks, listStyle, td)
	sp.drawMCISuppressionList(ctx, normalizedToWindow(ps.MCISuppressionList.Position), tracks, listStyle, td)

	towerListAirports := ctx.Client.TowerListAirports()
	for i, tl := range ps.TowerLists {
		if tl.Visible && i < len(towerListAirports) {
			sp.drawTowerList(ctx, normalizedToWindow(tl.Position), towerListAirports[i], tl.Lines,
				tracks, listStyle, td)
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

	// Command mode indicator (possibly)
	modestr := sp.commandMode.PreviewString()
	text.WriteString(modestr)
	if sp.commandMode == CommandModeMultiFunc {
		text.WriteString(sp.multiFuncPrefix)
	}
	if sp.commandMode == CommandModeTargetGen {
		text.WriteByte(' ')
		text.WriteString(string(sp.targetGenLastCallsign))
	}
	if modestr != "" {
		text.WriteString("\n")
	}

	text.WriteString(strings.Join(strings.Fields(sp.previewAreaInput), "\n")) // spaces are rendered as newlines
	if text.Len() > 0 {
		style := renderer.TextStyle{
			Font:  font,
			Color: ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
		}
		td.AddText(rewriteDelta(text.String()), pw, style)
	}
}

func (sp *STARSPane) drawSSAList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, td *renderer.TextDrawBuilder,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	font := sp.systemFont(ctx, ps.CharSize.Lists)
	listStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}
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

	formatAltimeter := func(metar *av.METAR) string {
		alt := strings.TrimPrefix(metar.Altimeter, "A")
		if len(alt) == 4 {
			alt = alt[:2] + "." + alt[2:]
		}
		return alt
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
			if metar := ctx.Client.State.METAR[ctx.Client.State.PrimaryAirport]; metar != nil {
				text += formatAltimeter(metar)
			}
		}
		td.AddText(text, pw, listStyle)
		newline()
	}

	// ATIS/GI text. (Note that per 4-44 filter.All does not apply to GI text.)
	if filter.Text.Main && (ps.ATIS != "" || ps.GIText[0] != "") {
		pw = td.AddText(rewriteDelta(strings.Join([]string{ps.ATIS, ps.GIText[0]}, " ")), pw, listStyle)
		newline()
	}
	for i := 1; i < len(ps.GIText); i++ {
		if filter.Text.GI[i] && ps.GIText[i] != "" {
			pw = td.AddText(rewriteDelta(ps.GIText[i]), pw, listStyle)
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
		for _, trk := range tracks {
			if trk.IsAssociated() && trk.FlightPlan.SPCOverride != "" {
				codes[trk.FlightPlan.SPCOverride] = nil
			}
			if ok, code := trk.Squawk.IsSPC(); ok {
				codes[code] = nil
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
		// TODO: others? 2-84
		if len(disabled) > 0 {
			pw = td.AddText(strings.Join(disabled, " "), pw, listStyle)
			newline()
		}
	}

	if filter.All || filter.Intrail {
		// We don't have any way to disable them, so this is easy..
		pw = td.AddText("INTRAIL ON", pw, listStyle)
		newline()
	}
	if filter.All || filter.Intrail25 {
		var vols []string
		for _, r := range ctx.Client.State.ArrivalRunways {
			if ap, ok := ctx.Client.State.ArrivalAirports[r.Airport]; ok {
				if vol, ok := ap.ATPAVolumes[r.Runway]; ok && vol.Enable25nmApproach {
					vols = append(vols, vol.Id) // TODO:include airport?
				}
			}
		}
		if len(vols) > 0 {
			v := strings.Join(vols, " ")
			if len(v) > 16 { // 32 - "INTRAIL 2.5 ON: "
				[]byte(v)[15] = '+'
				v = v[:16]
			}
			pw = td.AddText("INTRAIL 2.5 ON: "+v, pw, listStyle)
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
			if metar := ctx.Client.State.METAR[ap]; metar != nil {
				altimeters = append(altimeters, stripK(ap)+" "+formatAltimeter(metar)+"A") // 2-79: A -> automatic
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

	if (filter.All || filter.QuickLookPositions) && (ps.QuickLookAll || len(ps.QuickLookPositions) > 0) {
		if ps.QuickLookAll {
			if ps.QuickLookAllIsPlus {
				pw = td.AddText("QL: ALL+", pw, listStyle)
			} else {
				pw = td.AddText("QL: ALL", pw, listStyle)
			}
		} else {
			pos := util.MapSlice(ps.QuickLookPositions,
				func(q QuickLookPosition) string { return q.String() })
			pw = td.AddText("QL: "+strings.Join(pos, " "), pw, listStyle)
		}
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

func (sp *STARSPane) drawVFRList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.VFRList.Visible {
		return
	}

	vfr := util.FilterSlice(ctx.Client.State.UnassociatedFlightPlans,
		func(fp *sim.STARSFlightPlan) bool {
			// Only include NAS VFR flight plans.
			return fp.Rules != av.FlightRulesIFR && fp.Location.IsZero() && fp.PlanType == sim.LocalEnroute
		})

	for _, fp := range vfr {
		if _, ok := sp.VFRFPFirstSeen[fp.ACID]; !ok {
			sp.VFRFPFirstSeen[fp.ACID] = ctx.Now
		}
	}
	slices.SortFunc(vfr, func(a, b *sim.STARSFlightPlan) int {
		return sp.VFRFPFirstSeen[a.ACID].Compare(sp.VFRFPFirstSeen[b.ACID])
	})

	dupes := getDuplicateBeaconCodes(ctx)

	var text strings.Builder
	text.WriteString("VFR LIST\n")
	if len(vfr) > ps.VFRList.Lines {
		text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.VFRList.Lines, len(vfr)))
	}
	for i := range math.Min(len(vfr), ps.VFRList.Lines) {
		fp := vfr[i]
		text.WriteString(fmt.Sprintf("%2d ", vfr[i].ListIndex))
		text.WriteByte(' ') // TODO: + in-out-in flight, / dupe acid, * DM message on departure
		acid := string(vfr[i].ACID)
		if fp.DisableMSAW {
			if fp.DisableCA {
				acid += "+"
			} else {
				acid += "*"
			}
		} else if fp.DisableCA {
			acid += STARSTriangleCharacter
		}
		text.WriteString(fmt.Sprintf("%-8s ", acid))
		if _, ok := dupes[fp.AssignedSquawk]; ok {
			text.WriteByte('/')
		} else {
			text.WriteByte(' ')
		}
		haveCode := ctx.Now.Sub(sp.VFRFPFirstSeen[fp.ACID]) > 2*time.Second
		if haveCode {
			text.WriteString(fp.AssignedSquawk.String())
		} else {
			text.WriteString("    ")
		}
		text.WriteByte(' ')
		if !haveCode {
			text.WriteString("VFR")
		} else {
			if fp.EntryFix != "" && fp.TypeOfFlight != av.FlightTypeDeparture {
				text.WriteByte(fp.EntryFix[0])
			} else {
				text.WriteByte(' ')
			}
			if fp.ExitFix != "" && fp.TypeOfFlight != av.FlightTypeArrival {
				text.WriteByte(fp.ExitFix[0])
			} else {
				text.WriteByte(' ')
			}
		}
		text.WriteByte('\n')
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawTABList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.TABList.Visible {
		return
	}

	plans := util.FilterSlice(ctx.Client.State.UnassociatedFlightPlans,
		func(fp *sim.STARSFlightPlan) bool {
			if !fp.Location.IsZero() {
				// Unsupported DBs aren't included in the list.
				return false
			}

			// TODO: handle consolidation, etc.
			if fp.TrackingController == ctx.UserTCP {
				return true
			}

			// TODO: should also include flight plans that we entered but
			// assigned a different initial controller to.

			if fp.InboundHandoffController == "" {
				// Only controlled by virtual
				return false
			}

			ctrl := ctx.Client.State.ResolveController(fp.InboundHandoffController)
			return ctrl == ctx.UserTCP
		})

	// 2-92: default sort is by ACID
	slices.SortFunc(plans, func(a, b *sim.STARSFlightPlan) int {
		return strings.Compare(string(a.ACID), string(b.ACID))
	})

	dupes := getDuplicateBeaconCodes(ctx)

	var text strings.Builder

	text.WriteString("FLIGHT PLAN\n")
	if len(plans) > ps.TABList.Lines {
		text.WriteString(fmt.Sprintf("MORE: %d/%d\n", ps.TABList.Lines, len(plans)))
	}
	for i := range math.Min(len(plans), ps.TABList.Lines) {
		fp := plans[i]
		text.WriteString(fmt.Sprintf("%2d ", fp.ListIndex))
		text.WriteByte(' ') // TODO: + in-out-in flight, / dupe acid, * DM message on departure
		acid := string(fp.ACID)
		if fp.DisableMSAW {
			if fp.DisableCA {
				acid += "+"
			} else {
				acid += "*"
			}
		} else if fp.DisableCA {
			acid += STARSTriangleCharacter
		}
		text.WriteString(fmt.Sprintf("%-8s ", acid))
		if _, ok := dupes[fp.AssignedSquawk]; ok {
			text.WriteByte('/')
		} else {
			text.WriteByte(' ')
		}
		text.WriteString(fp.AssignedSquawk.String())
		text.WriteByte(' ')
		if fp.RequestedAltitude != 0 {
			text.WriteString(fmt.Sprintf("%03d ", fp.RequestedAltitude/100))
		} else {
			text.WriteString("    ")
		}
		if fp.Rules == av.FlightRulesVFR {
			text.WriteString("VFR")
		} else {
			if fp.EntryFix != "" && fp.TypeOfFlight != av.FlightTypeDeparture {
				text.WriteByte(fp.EntryFix[0])
			} else {
				text.WriteByte(' ')
			}
			if fp.ExitFix != "" && fp.TypeOfFlight != av.FlightTypeArrival {
				text.WriteByte(fp.ExitFix[0])
			} else {
				text.WriteByte(' ')
			}
		}
		text.WriteByte('\n')
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawAlertList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	// The alert list can't be hidden.
	var text strings.Builder
	var lists []string
	ps := sp.currentPrefs()

	if ps.DisableMSAW && ps.DisableCAWarnings && ps.DisableMCIWarnings {
		return
	}

	var msaw []sim.Track
	if !ps.DisableMSAW {
		lists = append(lists, "LA")
		for _, trk := range tracks {
			if sp.TrackState[trk.ADSBCallsign].MSAW {
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
			state0 := sp.TrackState[msaw[0].ADSBCallsign]
			if len(msaw) > 0 && (len(ca) == 0 || state0.MSAWStart.Before(ca[0].Start)) &&
				(len(mci) == 0 || state0.MSAWStart.Before(mci[0].Start)) {
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

		for range math.Min(n, alertListMaxLines) {
			msawtrk, capair, mcipair := next()

			alt := func(trk *sim.Track) string {
				if trk.IsAssociated() && trk.FlightPlan.PilotReportedAltitude != 0 {
					return strconv.Itoa(trk.FlightPlan.PilotReportedAltitude/100) + "*"
				}
				return strconv.Itoa(int((trk.Altitude + 50) / 100))
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
			td.AddText(text.String(), pw, style)
		}
	}
}

func (sp *STARSPane) drawCoastList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	var text strings.Builder

	text.WriteString("COAST/SUSPEND\n")

	// Get suspended tracks (coast not yet supported)
	tracks := slices.Collect(util.FilterSeq(maps.Values(ctx.Client.State.Tracks),
		func(t *sim.Track) bool { return t.IsAssociated() && t.FlightPlan.Suspended }))
	// Sort by list index
	slices.SortFunc(tracks,
		func(a, b *sim.Track) int { return a.FlightPlan.CoastSuspendIndex - b.FlightPlan.CoastSuspendIndex })

	for _, trk := range tracks {
		text.WriteString(fmt.Sprintf("%2d ", trk.FlightPlan.CoastSuspendIndex))
		text.WriteString(fmt.Sprintf("%-8s ", trk.ADSBCallsign))
		text.WriteString("S ") // suspend only for now
		text.WriteString(trk.Squawk.String() + " ")

		// For suspended, we always just show altitude (of one sort or another)
		if trk.Mode == av.TransponderModeAltitude {
			text.WriteString(fmt.Sprintf("%03d", int(trk.Altitude+50)/100))
		} else if trk.FlightPlan.PilotReportedAltitude != 0 {
			text.WriteString(fmt.Sprintf("%03d", trk.FlightPlan.PilotReportedAltitude))
		} else {
			text.WriteString("RDR")
		}
		text.WriteByte('\n')
	}

	td.AddText(text.String(), pw, style)
}

func (sp *STARSPane) drawMapsList(ctx *panes.Context, pw [2]float32, style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.VideoMapsList.Visible {
		return
	}

	var text strings.Builder
	format := func(m sim.VideoMap) {
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
	var m []sim.VideoMap
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
	slices.SortFunc(m, func(a, b sim.VideoMap) int { return a.Id - b.Id })

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

	add := func(ra av.RestrictionArea, idx int) {
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

	for i, ra := range ctx.Client.State.UserRestrictionAreas {
		add(ra, i+1)
	}
	for i, ra := range ctx.FacilityAdaptation.RestrictionAreas {
		add(ra, i+101)
	}

	td.AddText(rewriteDelta(text.String()), pw, style)
}

func (sp *STARSPane) drawCRDAStatusList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, style renderer.TextStyle,
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
			text.WriteString(ctx.UserTCP)
		}
		text.WriteByte('\n')
	}

	if text.Len() > 0 {
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawMCISuppressionList(ctx *panes.Context, pw [2]float32, tracks []sim.Track, style renderer.TextStyle,
	td *renderer.TextDrawBuilder) {
	ps := sp.currentPrefs()
	if !ps.MCISuppressionList.Visible {
		return
	}

	var text strings.Builder
	text.WriteString("MCI SUPPRESSION\n")
	for _, trk := range tracks {
		if trk.IsAssociated() && trk.FlightPlan.MCISuppressedCode != av.Squawk(0) {
			text.WriteString(fmt.Sprintf("%7s %s  %s\n", trk.FlightPlan.ACID, trk.Squawk.String(),
				trk.FlightPlan.MCISuppressedCode.String()))
		}
	}

	td.AddText(text.String(), pw, style)
}

func (sp *STARSPane) drawTowerList(ctx *panes.Context, pw [2]float32, airport string, lines int, tracks []sim.Track,
	style renderer.TextStyle, td *renderer.TextDrawBuilder) {
	stripK := func(airport string) string {
		if len(airport) == 4 && airport[0] == 'K' {
			return airport[1:]
		} else {
			return airport
		}
	}

	var text strings.Builder
	loc := ctx.Client.State.ArrivalAirports[airport].Location
	text.WriteString(stripK(airport) + " TOWER\n")
	m := make(map[float32]string)
	for _, trk := range tracks {
		if trk.IsAssociated() && trk.ArrivalAirport == airport {
			dist := math.NMDistance2LL(loc, trk.Location)
			// We'll punt on the chance that two aircraft have the
			// exact same distance to the airport...
			m[dist] = fmt.Sprintf("%-7s %s", trk.FlightPlan.ACID, trk.FlightPlan.AircraftType)
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
	if ctrl := ctx.Client.State.Controllers[ctx.UserTCP]; ctrl != nil {
		signOnTime := ctx.Client.SessionStats.SignOnTime
		text.WriteString(ctx.UserTCP + " " + signOnTime.UTC().Format("1504")) // TODO: initials
		td.AddText(text.String(), pw, style)
	}
}

func (sp *STARSPane) drawCoordinationLists(ctx *panes.Context, paneExtent math.Extent2D, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	font := sp.systemFont(ctx, ps.CharSize.Lists)
	titleStyle := renderer.TextStyle{
		Font:  font,
		Color: ps.Brightness.Lists.ScaleRGB(STARSListColor),
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	normalizedToWindow := func(p [2]float32) [2]float32 {
		return [2]float32{p[0] * paneExtent.Width(), p[1] * paneExtent.Height()}
	}

	releaseDepartures := ctx.Client.State.GetSTARSReleaseDepartures()

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
				if ctx.Client.State.ResolveController(dep.DepartureController) != ctx.UserTCP {
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

		pw := normalizedToWindow(list.Position)

		halfSeconds := ctx.Now.UnixMilli() / 500
		blinkDim := halfSeconds&1 == 0

		if list.AutoRelease {
			pw = td.AddText(strings.ToUpper(cl.Name)+"    AUTO\n", pw, titleStyle)
		} else {
			pw = td.AddText(strings.ToUpper(cl.Name)+"\n", pw, titleStyle)
		}
		if len(rel) > list.Lines {
			pw = td.AddText(fmt.Sprintf("MORE: %d/%d\n", list.Lines, len(rel)), pw, listStyle)
		}
		var text strings.Builder
		for i := range math.Min(len(rel), list.Lines) {
			dep := rel[i]
			text.Reset()
			text.WriteString("     ")
			if idx := slices.IndexFunc(ctx.Client.State.UnassociatedFlightPlans,
				func(fp *sim.STARSFlightPlan) bool { return string(fp.ACID) == string(dep.ADSBCallsign) }); idx == -1 {
				text.WriteString(fmt.Sprintf(" %-10s NO FP", string(dep.ADSBCallsign)))
			} else {
				fp := ctx.Client.State.UnassociatedFlightPlans[idx]
				text.WriteString(fmt.Sprintf("%2d", fp.ListIndex))
				text.WriteString(util.Select(dep.Released, "+", " "))
				text.WriteString(fmt.Sprintf(" %-10s %5s %s %5s %03d\n", string(fp.ACID), fp.AircraftType,
					fp.AssignedSquawk, fp.ExitFix, fp.RequestedAltitude/100))
				if !dep.Released && blinkDim {
					pw = td.AddText(rewriteDelta(text.String()), pw, dimStyle)
				} else {
					pw = td.AddText(rewriteDelta(text.String()), pw, listStyle)
				}
			}
		}
	}

	td.GenerateCommands(cb)
}
