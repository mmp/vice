package eram

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

const wxWindowWidth = float32(320)
const wxRefreshInterval = 10 * time.Minute

// wxMetarResult holds the raw METAR data for one airport.
type wxMetarResult struct {
	icao    string
	rawText string // Full METAR text
	err     error
}

// wxDisplayID returns the short display identifier for an ICAO code.
// US airports (K + 3-letter IATA) drop the leading K for display.
func wxDisplayID(icao string) string {
	if len(icao) == 4 && icao[0] == 'K' {
		return icao[1:]
	}
	return icao
}

// wxFetchMETAR asynchronously fetches a full METAR from VATSIM,
// sending the result to ch when done.
func wxFetchMETAR(icao string, ch chan<- wxMetarResult) {
	go func() {
		url := "https://metar.vatsim.net/metar.php?id=" + icao
		resp, err := http.Get(url)
		if err != nil {
			ch <- wxMetarResult{icao: icao, err: err}
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			ch <- wxMetarResult{icao: icao, err: err}
			return
		}

		raw := strings.TrimSpace(string(body))
		ch <- wxMetarResult{
			icao:    icao,
			rawText: raw,
			err:     nil,
		}
	}()
}

// drawWXView renders the WX floating window.
func (ep *ERAMPane) drawWXView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.WX.Visible {
		return
	}

	// Drain completed fetch results
draining:
	for {
		select {
		case result := <-ep.wxFetchCh:
			ep.wxMetars[result.icao] = result
			ep.wxLastFetch[result.icao] = time.Now()
			ep.wxFetching[result.icao] = false
		default:
			break draining
		}
	}

	// Kick off fetches for airports that need refreshing
	for _, icao := range ep.WXReportStations {
		if ep.wxFetching[icao] {
			continue
		}
		lastFetch, fetched := ep.wxLastFetch[icao]
		if !fetched || time.Since(lastFetch) > wxRefreshInterval {
			ep.wxFetching[icao] = true
			wxFetchMETAR(icao, ep.wxFetchCh)
		}
	}

	// --- Font setup ---
	fontNum := ps.WX.Font
	if fontNum < 1 {
		fontNum = 1
	}
	if fontNum > 3 {
		fontNum = 3
	}

	titleFont := ep.ERAMFont(2)
	titleTextStyle := renderer.TextStyle{
		Font:        titleFont,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	listFont := ep.ERAMFont(fontNum)
	textStyle := renderer.TextStyle{
		Font:        listFont,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	_, by := listFont.BoundText("0", textStyle.LineSpacing)
	lineH := float32(by + 2)

	fontScale := []float32{0.85, 1.0, 1.2}[fontNum-1]
	boxTopPad := float32(8) * fontScale
	boxW := float32(13) * fontScale
	boxH := lineH
	boxGap := float32(4) * fontScale

	p0 := ps.WX.Position
	width := wxWindowWidth

	titleH := float32(16)
	if _, tby := titleFont.BoundText("WX", titleTextStyle.LineSpacing); tby > 0 {
		titleH = float32(tby) + 4
	}

	numRows := len(ep.WXReportStations)
	visibleRows := ps.WX.Lines
	if visibleRows < 3 {
		visibleRows = 3
	}
	if visibleRows > 24 {
		visibleRows = 24
	}

	// --- Pre-pass: compute word-wrapped content for each station ---
	// X coordinates (p0[0], width) are known before height is computed,
	// so we can do the wrapping now and use the results to derive bodyHeight.
	type stationRender struct {
		lines []string // wrapped METAR lines; empty means show msg instead
		msg   string   // text for loading / error state
	}
	stationRenders := make([]stationRender, len(ep.WXReportStations))

	spaceW, _ := listFont.BoundText(" ", textStyle.LineSpacing)
	threeSpaceW := float32(3 * spaceW)
	stationIDX := p0[0] + 4 + boxW + boxGap // left edge of station ID text

	for i, icao := range ep.WXReportStations {
		displayID := wxDisplayID(icao)
		displayIDW, _ := listFont.BoundText(displayID, textStyle.LineSpacing)
		metarStartX := stationIDX + float32(displayIDW) + threeSpaceW
		firstLineAvailW := width - (metarStartX - p0[0]) - 4
		contLineAvailW := width - (stationIDX - p0[0]) - 4

		result, hasResult := ep.wxMetars[icao]
		sr := &stationRenders[i]

		if !hasResult || ep.wxFetching[icao] {
			sr.msg = "....  (Loading)"
			continue
		}
		if result.err != nil || result.rawText == "" {
			sr.msg = "-M-"
			continue
		}

		// Find the time-group token: all digits followed by 'Z' (e.g. "220100Z").
		// Using strings.Index would wrongly match the 'Z' inside station IDs like "CYYZ".
		rawFields := strings.Fields(result.rawText)
		timeField := -1
		for j, f := range rawFields {
			if len(f) >= 5 && f[len(f)-1] == 'Z' {
				allDigits := true
				for _, c := range f[:len(f)-1] {
					if c < '0' || c > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					timeField = j
					break
				}
			}
		}
		if timeField == -1 || len(rawFields[timeField]) < 5 {
			sr.msg = "-M-"
			continue
		}

		tok := rawFields[timeField]
		hhmmStr := tok[len(tok)-5 : len(tok)-1] // last 4 digits before 'Z'
		metarData := hhmmStr
		if timeField+1 < len(rawFields) {
			metarData += " " + strings.Join(rawFields[timeField+1:], " ")
		}

		var cur string
		isFirstLine := true
		for word := range strings.FieldsSeq(metarData) {
			curAvailW := firstLineAvailW
			if !isFirstLine {
				curAvailW = contLineAvailW
			}
			test := cur
			if test != "" {
				test += " "
			}
			test += word
			w, _ := listFont.BoundText(test, textStyle.LineSpacing)
			if float32(w) > curAvailW && cur != "" {
				sr.lines = append(sr.lines, cur)
				cur = word
				isFirstLine = false
			} else {
				cur = test
			}
		}
		if cur != "" {
			sr.lines = append(sr.lines, cur)
		}
	}

	// Count total text lines across all stations to determine if scrolling is needed
	totalLines := 0
	stationLineCount := make([]int, numRows) // lines per station
	for i, sr := range stationRenders {
		n := 1
		if len(sr.lines) > 0 {
			n = len(sr.lines)
		}
		stationLineCount[i] = n
		totalLines += n
	}

	// Determine which stations to show based on LINES limit
	startIdx := 0
	endIdx := numRows
	if totalLines > visibleRows {
		// LINES limits the total text lines displayed, not the station count
		// Paging: scroll by stations until accumulated lines >= visibleRows
		currentLines := 0
		startIdx = ep.wxScrollOffset
		if startIdx >= numRows {
			startIdx = numRows - 1
		}
		if startIdx < 0 {
			startIdx = 0
		}

		endIdx = startIdx
		for endIdx < numRows && currentLines < visibleRows {
			currentLines += stationLineCount[endIdx]
			endIdx++
			if currentLines >= visibleRows {
				break
			}
		}
		if endIdx > numRows {
			endIdx = numRows
		}

		// Clamp scroll offset: don't scroll past the last station
		maxOffset := numRows - 1
		if ep.wxScrollOffset > maxOffset {
			ep.wxScrollOffset = maxOffset
		}
	}

	// Compute bodyHeight from the stations that will be displayed
	bodyHeight := float32(0)
	if numRows > 0 {
		for i := startIdx; i < endIdx; i++ {
			n := stationLineCount[i]
			bodyHeight += boxTopPad + float32(n)*lineH + boxGap
		}
		bodyHeight = max(bodyHeight, lineH+4)
	}
	height := titleH + bodyHeight

	// --- Draw builders ---
	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	// Window corners
	p1 := math.Add2f(p0, [2]float32{width, 0})
	p2 := math.Add2f(p1, [2]float32{0, -height})
	p3 := math.Add2f(p2, [2]float32{-width, 0})

	// Body background (below title bar)
	bodyP0 := math.Add2f(p0, [2]float32{0, -titleH})
	bodyP1 := math.Add2f(bodyP0, [2]float32{width, 0})
	bodyP2 := math.Add2f(bodyP1, [2]float32{0, -bodyHeight})
	bodyP3 := math.Add2f(bodyP0, [2]float32{0, -bodyHeight})
	trid.AddQuad(bodyP0, bodyP1, bodyP2, bodyP3, renderer.RGB{R: 0, G: 0, B: 0})

	// Borders
	if ps.WX.ShowBorder {
		// 1px grey border around the body list area
		listBorderColor := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}
		ld.AddLine(bodyP0, bodyP1, listBorderColor)
		ld.AddLine(bodyP1, bodyP2, listBorderColor)
		ld.AddLine(bodyP2, bodyP3, listBorderColor)
		ld.AddLine(bodyP3, bodyP0, listBorderColor)

		// 1px border around entire window
		borderColor := ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .914, G: .914, B: .914})
		ld.AddLine(p0, p1, borderColor)
		ld.AddLine(p1, p2, borderColor)
		ld.AddLine(p2, p3, borderColor)
		ld.AddLine(p3, p0, borderColor)
	}

	// Title bar background
	titleBgColor := renderer.RGB{R: 0, G: 0, B: 0}
	if ps.WX.Opaque {
		titleBgColor = renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0}
	}
	titleP0 := p0
	titleP1 := math.Add2f(p0, [2]float32{width, 0})
	titleP2 := math.Add2f(titleP1, [2]float32{0, -titleH})
	titleP3 := math.Add2f(p0, [2]float32{0, -titleH})
	trid.AddQuad(titleP0, titleP1, titleP2, titleP3, titleBgColor)

	mouse := ctx.Mouse

	// Title text (centered)
	titleText := "WX REPORT"
	ttw, tth := titleFont.BoundText(titleText, titleTextStyle.LineSpacing)
	titlePos := math.Add2f(titleP0, [2]float32{width/2 - float32(ttw)/2, -titleH/2 + float32(tth)/2})
	titleColor := ps.Brightness.Text.ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85})

	// M button (left side of title bar)
	mLabel := "M"
	mw, mh := titleFont.BoundText(mLabel, titleTextStyle.LineSpacing)
	mPad := float32(2)
	mRect := math.Extent2D{
		P0: [2]float32{titleP0[0], titleP0[1] - titleH},
		P1: [2]float32{titleP0[0] + mPad + float32(mw) + mPad, titleP0[1]},
	}

	// Minimize button (right side of title bar)
	minLabel := "-"
	minw, minh := titleFont.BoundText(minLabel, titleTextStyle.LineSpacing)
	minRect := math.Extent2D{
		P0: [2]float32{titleP1[0] - mPad - float32(minw) - mPad, titleP1[1] - titleH},
		P1: titleP1,
	}

	titleRect := math.Extent2D{
		P0: [2]float32{mRect.P1[0], titleP2[1]},
		P1: [2]float32{minRect.P0[0], titleP1[1]},
	}

	mouseInsideM := mouse != nil && mRect.Inside(mouse.Pos)
	mouseInsideMin := mouse != nil && minRect.Inside(mouse.Pos)
	mouseInsideTitle := mouse != nil && titleRect.Inside(mouse.Pos)

	if mouseInsideTitle {
		titleColor = toolbarHoveredOutlineColor
	}
	td.AddText(titleText, titlePos, renderer.TextStyle{Font: titleFont, Color: titleColor})

	mTextColor := titleTextStyle.Color
	if mouseInsideM {
		mTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(mLabel, math.Add2f(titleP0, [2]float32{mPad, -titleH/2 + float32(mh)/2}),
		renderer.TextStyle{Font: titleFont, Color: mTextColor, LineSpacing: titleTextStyle.LineSpacing})

	minTextColor := titleTextStyle.Color
	if mouseInsideMin {
		minTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(minLabel, [2]float32{minRect.P0[0] + mPad, titleP1[1] - titleH/2 + float32(minh)/2},
		renderer.TextStyle{Font: titleFont, Color: minTextColor, LineSpacing: titleTextStyle.LineSpacing})

	// Outlines for M, -, and title area
	drawRectOutline := func(ex math.Extent2D, color renderer.RGB) {
		rp0 := [2]float32{ex.P0[0], ex.P0[1]}
		rp1 := [2]float32{ex.P1[0], ex.P0[1]}
		rp2 := [2]float32{ex.P1[0], ex.P1[1]}
		rp3 := [2]float32{ex.P0[0], ex.P1[1]}
		ld.AddLine(rp0, rp1, color)
		ld.AddLine(rp1, rp2, color)
		ld.AddLine(rp2, rp3, color)
		ld.AddLine(rp3, rp0, color)
	}
	if !mouseInsideM {
		drawRectOutline(mRect, toolbarOutlineColor)
	}
	if !mouseInsideMin {
		drawRectOutline(minRect, toolbarOutlineColor)
	}
	if !mouseInsideTitle {
		drawRectOutline(titleRect, toolbarOutlineColor)
	}
	if mouseInsideM {
		drawRectOutline(mRect, toolbarHoveredOutlineColor)
	}
	if mouseInsideMin {
		drawRectOutline(minRect, toolbarHoveredOutlineColor)
	}
	if mouseInsideTitle {
		drawRectOutline(titleRect, toolbarHoveredOutlineColor)
	}

	// Handle title bar clicks
	if mouse := ctx.Mouse; ep.mousePrimaryClicked(mouse) || ep.mouseTertiaryClicked(mouse) {
		// Check scroll bar clicks first (if scroll bar is visible)
		if totalLines > visibleRows {
			const scrollBarContentW = float32(15)
			const scrollBarBorderW = float32(1)
			const scrollBarTotalW = scrollBarContentW + 2*scrollBarBorderW
			const scrollBarGap = float32(2)

			scrollBarSectionH := (bodyHeight - 2 - scrollBarGap) / 2

			scrollX1 := bodyP1[0] - 4
			scrollX0 := scrollX1 - scrollBarTotalW

			upY1 := bodyP0[1] - 1
			upY0 := upY1 - scrollBarSectionH

			downY1 := upY0 - scrollBarGap
			downY0 := downY1 - scrollBarSectionH

			upRect := math.Extent2D{
				P0: [2]float32{scrollX0, upY0},
				P1: [2]float32{scrollX1, upY1},
			}
			downRect := math.Extent2D{
				P0: [2]float32{scrollX0, downY0},
				P1: [2]float32{scrollX1, downY1},
			}

			if upRect.Inside(mouse.Pos) {
				if ep.wxScrollOffset > 0 {
					ep.wxScrollOffset--
				}
				mouse.Clicked = [platform.MouseButtonCount]bool{}
			} else if downRect.Inside(mouse.Pos) {
				if ep.wxScrollOffset < numRows-1 {
					ep.wxScrollOffset++
				}
				mouse.Clicked = [platform.MouseButtonCount]bool{}
			}
		}

		switch {
		case mRect.Inside(mouse.Pos):
			ctx.SetMousePosition(math.Add2f(mouse.Pos, [2]float32{width * 1.5, 0}))
			ep.wxMenuOpen = !ep.wxMenuOpen
			if ep.wxMenuOpen {
				ep.altimSetMenuOpen = false
			}
			mouse.Clicked = [platform.MouseButtonCount]bool{}
		case minRect.Inside(mouse.Pos):
			ps.WX.Visible = false
		case titleRect.Inside(mouse.Pos) && !ep.wxReposition:
			ep.wxReposition = true
			ep.wxRepoStart = time.Now()
			ep.wxDragOffset = math.Sub2f(mouse.Pos, p0)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
		case ep.wxReposition && time.Since(ep.wxRepoStart) > 100*time.Millisecond:
			ps.WX.Position = math.Sub2f(mouse.Pos, ep.wxDragOffset)
			ep.wxReposition = false
			ctx.Platform.EndCaptureMouse()
		}
	}

	// Reposition preview outline while dragging
	if ep.wxReposition && mouse != nil {
		previewP0 := math.Sub2f(mouse.Pos, ep.wxDragOffset)
		previewP1 := math.Add2f(previewP0, [2]float32{width, 0})
		previewP2 := math.Add2f(previewP1, [2]float32{0, -height})
		previewP3 := math.Add2f(previewP0, [2]float32{0, -height})
		c := toolbarHoveredOutlineColor
		ld.AddLine(previewP0, previewP1, c)
		ld.AddLine(previewP1, previewP2, c)
		ld.AddLine(previewP2, previewP3, c)
		ld.AddLine(previewP3, previewP0, c)
	}

	// Menu, if open
	if ep.wxMenuOpen {
		menuOrigin := math.Add2f(titleP3, [2]float32{width, titleH})
		ep.drawWXMenu(ctx, transforms, menuOrigin, 150, cb)
	}

	// --- Draw METAR list ---
	bright := ps.WX.Bright
	if bright < 0 {
		bright = 0
	}
	if bright > 100 {
		bright = 100
	}

	var yellowColor renderer.RGB
	if bright <= 80 {
		scale := float32(bright) / 80.0
		yellowColor = renderer.RGB{
			R: (159.0 / 255.0) * scale,
			G: (163.0 / 255.0) * scale,
			B: (9.0 / 255.0) * scale,
		}
	} else {
		scale := float32(bright-80) / 20.0
		r80 := float32(159.0 / 255.0)
		r100 := float32(198.0 / 255.0)
		g80 := float32(163.0 / 255.0)
		g100 := float32(203.0 / 255.0)
		b80 := float32(9.0 / 255.0)
		b100 := float32(11.0 / 255.0)
		yellowColor = renderer.RGB{
			R: r80 + (r100-r80)*scale,
			G: g80 + (g100-g80)*scale,
			B: b80 + (b100-b80)*scale,
		}
	}

	var textColor renderer.RGB
	if bright <= 80 {
		scale := float32(bright) / 80.0
		textColor = renderer.RGB{
			R: float32(187.0/255.0) * scale,
			G: float32(187.0/255.0) * scale,
			B: float32(187.0/255.0) * scale,
		}
	} else {
		scale := float32(bright-80) / 20.0
		t80 := float32(187.0 / 255.0)
		t100 := float32(233.0 / 255.0)
		textColor = renderer.RGB{
			R: t80 + (t100-t80)*scale,
			G: t80 + (t100-t80)*scale,
			B: t80 + (t100-t80)*scale,
		}
	}
	textColor = ps.Brightness.Text.ScaleRGB(textColor)

	greyBorderColor := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}

	yOffset := bodyP0[1]
	linesDrawn := 0
	for i := startIdx; i < endIdx; i++ {
		if linesDrawn >= visibleRows {
			break
		}
		icao := ep.WXReportStations[i]
		sr := stationRenders[i]
		displayID := wxDisplayID(icao)

		n := 1
		if len(sr.lines) > 0 {
			n = len(sr.lines)
		}

		// Truncate station if we've already drawn some lines and are at the limit
		linesToDraw := n
		if linesDrawn+n > visibleRows {
			linesToDraw = visibleRows - linesDrawn
		}

		stationTop := yOffset
		stationBottom := stationTop - boxTopPad - float32(linesToDraw)*lineH - boxGap

		// Yellow indicator box: sits boxTopPad below the station top
		boxX0 := bodyP0[0] + 4
		boxX1 := boxX0 + boxW
		boxY1 := stationTop - boxTopPad // top of box
		boxY0 := boxY1 - boxH           // bottom of box

		if ps.WX.ShowIndicators {
			bp0 := [2]float32{boxX0, boxY0}
			bp1 := [2]float32{boxX1, boxY0}
			bp2 := [2]float32{boxX1, boxY1}
			bp3 := [2]float32{boxX0, boxY1}
			trid.AddQuad(bp0, bp1, bp2, bp3, yellowColor)
			ld.AddLine(bp0, bp1, greyBorderColor)
			ld.AddLine(bp1, bp2, greyBorderColor)
			ld.AddLine(bp2, bp3, greyBorderColor)
			ld.AddLine(bp3, bp0, greyBorderColor)
		}

		// Station ID text: top-left aligned with the top of the yellow box
		textX := boxX1 + boxGap
		textY := boxY0 + float32(by)
		td.AddText(displayID, [2]float32{textX, textY},
			renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})

		// METAR or status text: line 1 starts 3 spaces after the station ID;
		// continuation lines start at the station ID's left edge.
		displayIDW, _ := listFont.BoundText(displayID, textStyle.LineSpacing)
		metarStartX := textX + float32(displayIDW) + threeSpaceW

		currentY := textY
		if len(sr.lines) > 0 {
			// Draw only up to linesToDraw lines
			for j := 0; j < linesToDraw && j < len(sr.lines); j++ {
				line := sr.lines[j]
				lineX := metarStartX
				if j > 0 {
					lineX = textX
				}
				td.AddText(line, [2]float32{lineX, currentY},
					renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
				currentY -= lineH
			}
		} else if linesDrawn == 0 {
			// Only draw status message for first station
			td.AddText(sr.msg, [2]float32{metarStartX, currentY},
				renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
		}

		linesDrawn += linesToDraw
		yOffset = stationBottom
	}

	// Render scroll bar when there are more text lines than can fit in the current view
	if totalLines > visibleRows {
		const scrollBarContentW = float32(14)
		const scrollBarBorderW = float32(1)
		const scrollBarTotalW = scrollBarContentW + 2*scrollBarBorderW // 16 pixels total
		const scrollBarGap = float32(2)

		scrollBarSectionH := (bodyHeight - 2 - scrollBarGap) / 2

		scrollX1 := bodyP1[0] - 1
		scrollX0 := scrollX1 - scrollBarTotalW

		upY1 := bodyP0[1] - 1
		upY0 := upY1 - scrollBarSectionH

		downY0 := upY0 - scrollBarGap
		downY1 := downY0 - scrollBarSectionH

		scrollBg := renderer.RGB{R: 0, G: 0, B: 0}
		scrollBorder := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}
		arrowColor := renderer.RGB{R: 145.0 / 255.0, G: 145.0 / 255.0, B: 145.0 / 255.0}

		upP0 := [2]float32{scrollX0, upY0}
		upP1 := [2]float32{scrollX1, upY0}
		upP2 := [2]float32{scrollX1, upY1}
		upP3 := [2]float32{scrollX0, upY1}
		trid.AddQuad(upP0, upP1, upP2, upP3, scrollBg)
		ld.AddLine(upP0, upP1, scrollBorder)
		ld.AddLine(upP1, upP2, scrollBorder)
		ld.AddLine(upP2, upP3, scrollBorder)
		ld.AddLine(upP3, upP0, scrollBorder)

		arrowCenterX := scrollX0 + scrollBarBorderW + scrollBarContentW/2
		downArrowTopY := downY1 + 1
		downArrowWidths := []float32{1, 3, 5, 7, 9}
		for i, w := range downArrowWidths {
			y := downArrowTopY + float32(i)
			x0 := arrowCenterX - w/2
			x1 := arrowCenterX + w/2
			ld.AddLine([2]float32{x0, y}, [2]float32{x1, y}, arrowColor)
		}

		downP0 := [2]float32{scrollX0, downY0}
		downP1 := [2]float32{scrollX1, downY0}
		downP2 := [2]float32{scrollX1, downY1}
		downP3 := [2]float32{scrollX0, downY1}
		trid.AddQuad(downP0, downP1, downP2, downP3, scrollBg)
		ld.AddLine(downP0, downP1, scrollBorder)
		ld.AddLine(downP1, downP2, scrollBorder)
		ld.AddLine(downP2, downP3, scrollBorder)
		ld.AddLine(downP3, downP0, scrollBorder)

		upArrowTopY := upY1 - 5
		updatedUpArrowWidths := []float32{9, 7, 5, 3, 1}
		for i, w := range updatedUpArrowWidths {
			y := upArrowTopY + float32(i)
			x0 := arrowCenterX - w/2
			x1 := arrowCenterX + w/2
			ld.AddLine([2]float32{x0, y}, [2]float32{x1, y}, arrowColor)
		}
	}

	// Commit all draw commands
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawWXMenu renders the configuration menu for the WX REPORT window.
func (ep *ERAMPane) drawWXMenu(ctx *panes.Context, transforms radar.ScopeTransformations, origin [2]float32, menuWidth float32, cb *renderer.CommandBuffer) {
	if !ep.wxMenuOpen {
		return
	}

	ps := ep.currentPrefs()

	blackBg := renderer.RGB{R: 0, G: 0, B: 0}
	greyBg := renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0}
	greenBg := renderer.RGB{R: 0, G: 157.0 / 255.0, B: 0}
	textColor := renderer.RGB{R: .85, G: .85, B: .85}

	// T/O button — shows "T" (transparent) or "O" (opaque)
	tLabel := "T"
	tBg := blackBg
	if ps.WX.Opaque {
		tLabel = "O"
		tBg = greyBg
	}

	// BORDER button - grey when ON, black when OFF
	borderBg := blackBg
	if ps.WX.ShowBorder {
		borderBg = greyBg
	}

	// TEAROFF button - grey when ON, black when OFF
	tearoffBg := blackBg
	if ps.WX.ShowIndicators {
		tearoffBg = greyBg
	}

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: textColor, Centered: true, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.WX.Opaque = !ps.WX.Opaque
			}
			return false
		}},
		{Label: "BORDER", BgColor: borderBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.WX.ShowBorder = !ps.WX.ShowBorder
			}
			return false
		}},
		{Label: "TEAROFF", BgColor: tearoffBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.WX.ShowIndicators = !ps.WX.ShowIndicators
			}
			return false
		}},
		{Label: fmt.Sprintf("LINES %d", ps.WX.Lines), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.WX.Lines, 3, 24, 1)
			if ep.wxScrollOffset > 0 {
				maxOffset := len(ep.WXReportStations) - ps.WX.Lines
				if maxOffset < 0 {
					maxOffset = 0
				}
				if ep.wxScrollOffset > maxOffset {
					ep.wxScrollOffset = maxOffset
				}
			}
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.WX.Font), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.WX.Font, 1, 3, 1)
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.WX.Bright), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			handleClick(ep, &ps.WX.Bright, 0, 100, 1)
			return false
		}},
	}

	cfg := ERAMMenuConfig{
		Title:                 "WX",
		OnClose:               func() { ep.wxMenuOpen = false },
		Width:                 menuWidth,
		Font:                  ep.ERAMFont(2),
		ShowBorder:            true,
		BorderColor:           renderer.RGB{R: 213.0 / 255.0, G: 213.0 / 255.0, B: 213.0 / 255.0},
		DismissOnClickOutside: true,
		Rows:                  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, origin, cfg)
}
