package eram

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

const altimRefreshInterval = 10 * time.Minute

const altimWindowWidth = float32(207)

// altimMetarResult holds the parsed METAR data for one airport.
type altimMetarResult struct {
	icao         string
	obsTime      time.Time
	timeStr      string // "HHMM" from METAR observation time
	altimeter    string // 3-digit string, e.g., "991" for A2991
	altimeterRaw int    // 4-digit A-setting, e.g., 2991
	err          error
}

// parseMETARForAltim extracts the observation time (HHMM) and altimeter
// (last 3 digits of the A-setting) from a raw METAR string.
func parseMETARForAltim(raw string) (timeStr string, obsTime time.Time, altStr string, altRaw int, err error) {
	tokens := strings.Fields(raw)
	if len(tokens) < 2 {
		return "", time.Time{}, "", 0, fmt.Errorf("invalid METAR: too few tokens")
	}

	// Token[1] is the date/time group, e.g., "201151Z" → extract chars [2:6] = "1151"
	t := tokens[1]
	if len(t) >= 7 && strings.HasSuffix(t, "Z") {
		day, dayErr := strconv.Atoi(t[0:2])
		hour, hourErr := strconv.Atoi(t[2:4])
		min, minErr := strconv.Atoi(t[4:6])
		if dayErr == nil && hourErr == nil && minErr == nil {
			timeStr = t[2:6]
			now := time.Now().UTC()
			obsTime = time.Date(now.Year(), now.Month(), day, hour, min, 0, 0, time.UTC)
			for i := 0; i < 40 && obsTime.After(now.Add(12*time.Hour)); i++ {
				obsTime = obsTime.AddDate(0, 0, -1)
			}
			for i := 0; i < 40 && obsTime.Before(now.Add(-36*time.Hour)); i++ {
				obsTime = obsTime.AddDate(0, 0, 1)
			}
		}
	} else if len(t) >= 6 {
		timeStr = t[2:6]
	}

	// Find the altimeter setting: token "A" followed by exactly 4 digits.
	for _, tok := range tokens[2:] {
		if len(tok) == 5 && tok[0] == 'A' {
			allDigits := true
			for _, c := range tok[1:] {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				altRaw, _ = strconv.Atoi(tok[1:5])
				// Display the last 3 digits (e.g., A2991 → "991", A3000 → "000")
				altStr = tok[2:5]
				return timeStr, obsTime, altStr, altRaw, nil
			}
		}
	}

	if timeStr != "" {
		return timeStr, obsTime, "", 0, fmt.Errorf("altimeter not found in METAR")
	}
	return "", time.Time{}, "", 0, fmt.Errorf("METAR parse failed")
}

// altimDisplayID returns the short display identifier for an ICAO code.
// US airports (K + 3-letter IATA) drop the leading K for display.
func altimDisplayID(icao string) string {
	if len(icao) == 4 && icao[0] == 'K' {
		return icao[1:]
	}
	return icao
}

// altimFetchMETAR asynchronously fetches and parses a METAR from VATSIM,
// sending the result to ch when done.
func altimFetchMETAR(icao string, ch chan<- altimMetarResult) {
	go func() {
		url := "https://metar.vatsim.net/metar.php?id=" + icao
		resp, err := http.Get(url)
		if err != nil {
			ch <- altimMetarResult{icao: icao, err: err}
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			ch <- altimMetarResult{icao: icao, err: err}
			return
		}

		raw := strings.TrimSpace(string(body))
		timeStr, obsTime, altStr, altRaw, parseErr := parseMETARForAltim(raw)
		ch <- altimMetarResult{
			icao:         icao,
			obsTime:      obsTime,
			timeStr:      timeStr,
			altimeter:    altStr,
			altimeterRaw: altRaw,
			err:          parseErr,
		}
	}()
}

// drawAltimSetView renders the ALTIM SET floating window.
func (ep *ERAMPane) drawAltimSetView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.AltimSet.Visible {
		return
	}

	// Drain completed fetch results.
draining:
	for {
		select {
		case result := <-ep.altimSetFetchCh:
			ep.altimSetMetars[result.icao] = result
			ep.altimSetLastFetch[result.icao] = time.Now()
			ep.altimSetFetching[result.icao] = false
		default:
			break draining
		}
	}

	// Kick off fetches for airports that need refreshing.
	for _, icao := range ep.AltimSetAirports {
		if ep.altimSetFetching[icao] {
			continue
		}
		lastFetch, fetched := ep.altimSetLastFetch[icao]
		if !fetched || time.Since(lastFetch) > altimRefreshInterval {
			ep.altimSetFetching[icao] = true
			altimFetchMETAR(icao, ep.altimSetFetchCh)
		}
	}

	// --- Layout ---
	fontNum := ps.AltimSet.Font
	if fontNum < 1 {
		fontNum = 1
	}
	if fontNum > 3 {
		fontNum = 3
	}

	// Title uses fixed FONT 2 for consistent size
	titleFont := ep.ERAMFont(2)
	titleTextStyle := renderer.TextStyle{
		Font:        titleFont,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	// List uses selected font, only affects list items
	listFont := ep.ERAMFont(fontNum)
	textStyle := renderer.TextStyle{
		Font:        listFont,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	_, by := listFont.BoundText("0", textStyle.LineSpacing)
	lineH := float32(by + 2)

	// Scale boxes based on font size: 1=0.85x, 2=1.0x, 3=1.2x (only affects list)
	fontScale := []float32{0.85, 1.0, 1.2}[fontNum-1]
	boxW := float32(13) * fontScale
	boxH := float32(17) * fontScale
	boxGap := float32(4) * fontScale
	boxTopPad := float32(8) * fontScale
	boxBottomPad := float32(8) * fontScale

	p0 := ps.AltimSet.Position

	// Calculate window width based on number of columns and font scale
	numCols := ps.AltimSet.Col
	if numCols < 1 {
		numCols = 1
	}
	if numCols > 4 {
		numCols = 4
	}
	width := altimWindowWidth * float32(numCols) * fontScale

	titleH := float32(16)
	if _, tby := titleFont.BoundText("ALTIM SET", titleTextStyle.LineSpacing); tby > 0 {
		titleH = float32(tby) + 4
	}

	numRows := len(ep.AltimSetAirports)
	visibleRows := ps.AltimSet.Lines
	if visibleRows < 3 {
		visibleRows = 3
	}
	if visibleRows > 24 {
		visibleRows = 24
	}
	var bodyHeight float32
	if visibleRows == 0 {
		bodyHeight = lineH + 4
	} else {
		bodyHeight = boxTopPad + boxBottomPad + float32(visibleRows)*boxH + float32(visibleRows-1)*boxGap
	}
	height := titleH + bodyHeight

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

	// 1px border
	if ps.AltimSet.ShowBorder {
		borderColor := ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .914, G: .914, B: .914})
		ld.AddLine(p0, p1, borderColor)
		ld.AddLine(p1, p2, borderColor)
		ld.AddLine(p2, p3, borderColor)
		ld.AddLine(p3, p0, borderColor)
	}

	// Title bar background (black or grey based on Opaque mode)
	titleBgColor := renderer.RGB{R: 0, G: 0, B: 0}
	if ps.AltimSet.Opaque {
		titleBgColor = renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0}
	}
	titleP0 := p0
	titleP1 := math.Add2f(p0, [2]float32{width, 0})
	titleP2 := math.Add2f(titleP1, [2]float32{0, -titleH})
	titleP3 := math.Add2f(p0, [2]float32{0, -titleH})
	trid.AddQuad(titleP0, titleP1, titleP2, titleP3, titleBgColor)

	mouse := ctx.Mouse

	// Title text (centered)
	titleText := "ALTIM SET"
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
		// Calculate max airports per page for scroll bar visibility
		maxAirportsPerPage := visibleRows * numCols

		// Check scroll bar clicks first (if scroll bar is visible)
		if numRows > maxAirportsPerPage {
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
				// Scroll up (decrease offset)
				if ep.altimSetScrollOffset > 0 {
					ep.altimSetScrollOffset--
				}
				mouse.Clicked = [platform.MouseButtonCount]bool{}
			} else if downRect.Inside(mouse.Pos) {
				// Scroll down (increase offset)
				maxOffset := numRows - maxAirportsPerPage
				if ep.altimSetScrollOffset < maxOffset {
					ep.altimSetScrollOffset++
				}
				mouse.Clicked = [platform.MouseButtonCount]bool{}
			}
		}

		switch {
		case mRect.Inside(mouse.Pos):
			ctx.SetMousePosition(math.Add2f(mouse.Pos, [2]float32{width * 1.5, 0}))
			ep.altimSetMenuOpen = !ep.altimSetMenuOpen
			mouse.Clicked = [platform.MouseButtonCount]bool{}
		case minRect.Inside(mouse.Pos):
			ps.AltimSet.Visible = false
		case titleRect.Inside(mouse.Pos) && !ep.altimSetReposition:
			ep.altimSetReposition = true
			ep.altimSetRepoStart = time.Now()
			ep.altimSetDragOffset = math.Sub2f(mouse.Pos, p0)
			ctx.Platform.StartCaptureMouse(ctx.PaneExtent)
		case ep.altimSetReposition && time.Since(ep.altimSetRepoStart) > 100*time.Millisecond:
			ps.AltimSet.Position = math.Sub2f(mouse.Pos, ep.altimSetDragOffset)
			ep.altimSetReposition = false
			ctx.Platform.EndCaptureMouse()
		}
	}

	// Reposition preview outline while dragging
	if ep.altimSetReposition && mouse != nil {
		previewP0 := math.Sub2f(mouse.Pos, ep.altimSetDragOffset)
		previewP1 := math.Add2f(previewP0, [2]float32{width, 0})
		previewP2 := math.Add2f(previewP1, [2]float32{0, -height})
		previewP3 := math.Add2f(previewP0, [2]float32{0, -height})
		c := toolbarHoveredOutlineColor
		ld.AddLine(previewP0, previewP1, c)
		ld.AddLine(previewP1, previewP2, c)
		ld.AddLine(previewP2, previewP3, c)
		ld.AddLine(previewP3, previewP0, c)
	}

	// Menu, if open (drawn to the right side of the title by default)
	if ep.altimSetMenuOpen {
		menuOrigin := math.Add2f(titleP3, [2]float32{width, titleH})
		ep.drawAltimSetMenu(ctx, transforms, menuOrigin, 150, cb)
	}

	// Draw airport rows
	// Calculate colors based on brightness setting
	bright := ps.AltimSet.Bright
	if bright < 0 {
		bright = 0
	}
	if bright > 100 {
		bright = 100
	}

	// Scale yellow color based on bright (0=black, 80=current, 100=max bright)
	var yellowColor renderer.RGB
	if bright <= 80 {
		scale := float32(bright) / 80.0
		yellowColor = renderer.RGB{
			R: (159.0 / 255.0) * scale,
			G: (163.0 / 255.0) * scale,
			B: (9.0 / 255.0) * scale,
		}
	} else {
		// Interpolate between 80 and 100
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

	// Scale text color based on bright (0=black, 80=default 187/255, 100=233/255)
	var textColor renderer.RGB
	if bright <= 80 {
		scale := float32(bright) / 80.0
		textColor = renderer.RGB{
			R: float32(187.0/255.0) * scale,
			G: float32(187.0/255.0) * scale,
			B: float32(187.0/255.0) * scale,
		}
	} else {
		// Interpolate between 80 and 100
		scale := float32(bright-80) / 20.0
		t80 := float32(187.0 / 255.0)
		t100 := float32(233.0 / 255.0)
		textColor = renderer.RGB{
			R: t80 + (t100-t80)*scale,
			G: t80 + (t100-t80)*scale,
			B: t80 + (t100-t80)*scale,
		}
	}
	// Apply brightness scaling
	textColor = ps.Brightness.Text.ScaleRGB(textColor)

	boxBorderColor := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}

	textWidth := func(s string) float32 {
		w, _ := listFont.BoundText(s, textStyle.LineSpacing)
		return float32(w)
	}
	underlineText := func(x float32, y float32, s string, color renderer.RGB) {
		if s == "" {
			return
		}
		underlineY := y - float32(by) - 1
		ld.AddLine([2]float32{x, underlineY}, [2]float32{x + textWidth(s), underlineY}, color)
	}

	now := time.Now().UTC()

	// Multi-column rendering: fill each column with visibleRows items
	numCols = ps.AltimSet.Col
	if numCols < 1 {
		numCols = 1
	}
	if numCols > 4 {
		numCols = 4
	}

	// Calculate column width (scaled by font size)
	colWidth := altimWindowWidth * fontScale

	// For single/multi-column mode, calculate start/end indices based on scroll offset
	maxAirportsPerPage := visibleRows * numCols
	var startIdx, endIdx int
	if numRows > maxAirportsPerPage {
		startIdx = ep.altimSetScrollOffset
		endIdx = startIdx + maxAirportsPerPage
		if endIdx > numRows {
			endIdx = numRows
		}
	} else {
		startIdx = 0
		endIdx = numRows
	}

	// Render all airports across columns
	for dataIdx := 0; dataIdx < len(ep.AltimSetAirports); dataIdx++ {
		icao := ep.AltimSetAirports[dataIdx]

		// Skip rows before startIdx or at/after endIdx (for scroll mode)
		if dataIdx < startIdx || dataIdx >= endIdx {
			continue
		}

		// Determine which column and row within that column
		var colIdx, rowIdx int
		if numCols == 1 {
			// Single column: use scroll offset
			colIdx = 0
			rowIdx = dataIdx - startIdx
		} else {
			// Multi-column: fill columns sequentially
			colIdx = dataIdx / visibleRows
			rowIdx = dataIdx % visibleRows
		}

		// Skip if beyond current column setting
		if colIdx >= numCols {
			break
		}

		displayID := altimDisplayID(icao)
		result, hasResult := ep.altimSetMetars[icao]

		// Calculate position: base column X + column offset
		colX0 := bodyP0[0] + float32(colIdx)*colWidth
		rowX0 := colX0 + 4

		// Calculate Y position within column
		boxY1 := bodyP0[1] - boxTopPad - float32(rowIdx)*(boxH+boxGap) // top of box
		boxY0 := boxY1 - boxH                                          // bottom of box

		// Yellow indicator box
		if ps.AltimSet.ShowIndicators {
			boxX0 := rowX0
			boxX1 := rowX0 + boxW
			bp0 := [2]float32{boxX0, boxY0}
			bp1 := [2]float32{boxX1, boxY0}
			bp2 := [2]float32{boxX1, boxY1}
			bp3 := [2]float32{boxX0, boxY1}
			trid.AddQuad(bp0, bp1, bp2, bp3, yellowColor)
			ld.AddLine(bp0, bp1, boxBorderColor)
			ld.AddLine(bp1, bp2, boxBorderColor)
			ld.AddLine(bp2, bp3, boxBorderColor)
			ld.AddLine(bp3, bp0, boxBorderColor)
		}

		// Text position
		boxX1 := rowX0 + boxW

		timeStr := "...."
		altStr := "..."
		staleTime := false
		tooOld := false
		altBelowStandard := false
		missingReport := false
		if hasResult && !ep.altimSetFetching[icao] {
			if result.timeStr != "" {
				timeStr = result.timeStr
			}
			if !result.obsTime.IsZero() {
				age := now.Sub(result.obsTime)
				staleTime = age > 65*time.Minute
				tooOld = age > 120*time.Minute
			}
			altBelowStandard = result.altimeterRaw > 0 && result.altimeterRaw < 2992
			if result.err == nil && !tooOld && result.altimeter != "" {
				altStr = result.altimeter
			} else if result.err != nil || tooOld {
				missingReport = true
			}
		} else if !hasResult {
			missingReport = true
		}

		textCursor := [2]float32{boxX1 + boxGap, boxY0 + float32(by)}
		if missingReport {
			line := fmt.Sprintf("%-4s   -M-  ", displayID)
			td.AddText(line, textCursor, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
			continue
		}

		prefix := fmt.Sprintf("%-4s  ", displayID)
		timeField := fmt.Sprintf("%4s", timeStr)
		mid := " "
		altField := fmt.Sprintf("%3s", altStr)
		suffix := "  "
		line := prefix + timeField + mid + altField + suffix

		td.AddText(line, textCursor, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})

		if staleTime && timeStr != "...." {
			underlineText(textCursor[0]+textWidth(prefix), textCursor[1], timeField, textColor)
		}
		if altBelowStandard && altStr != "..." {
			underlineText(textCursor[0]+textWidth(prefix)+textWidth(timeField)+textWidth(mid), textCursor[1], altField, textColor)
		}
	}

	// Render scroll bar when there are more airports than can fit in the current view
	if numRows > maxAirportsPerPage {
		const scrollBarContentW = float32(14)
		const scrollBarBorderW = float32(1)
		const scrollBarTotalW = scrollBarContentW + 2*scrollBarBorderW // 16 pixels total
		const scrollBarGap = float32(2)

		// Calculate section height: (bodyHeight - 2px spacing - gap) / 2
		scrollBarSectionH := (bodyHeight - 2 - scrollBarGap) / 2

		// Position: right edge of the body, hugging right side
		scrollX1 := bodyP1[0] - 1
		scrollX0 := scrollX1 - scrollBarTotalW

		// Up arrow section (top)
		upY1 := bodyP0[1] - 1
		upY0 := upY1 - scrollBarSectionH

		// Down arrow section (below up section)
		downY0 := upY0 - scrollBarGap
		downY1 := downY0 - scrollBarSectionH

		scrollBg := renderer.RGB{R: 0, G: 0, B: 0}
		scrollBorder := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}
		arrowColor := renderer.RGB{R: 145.0 / 255.0, G: 145.0 / 255.0, B: 145.0 / 255.0}

		// Draw up arrow section
		upP0 := [2]float32{scrollX0, upY0}
		upP1 := [2]float32{scrollX1, upY0}
		upP2 := [2]float32{scrollX1, upY1}
		upP3 := [2]float32{scrollX0, upY1}
		trid.AddQuad(upP0, upP1, upP2, upP3, scrollBg)
		ld.AddLine(upP0, upP1, scrollBorder)
		ld.AddLine(upP1, upP2, scrollBorder)
		ld.AddLine(upP2, upP3, scrollBorder)
		ld.AddLine(upP3, upP0, scrollBorder)

		// Draw down arrow pointing upward: 1,3,5,7,9 pixels wide from top to bottom
		// (positioned in top section for scrolling up)
		arrowCenterX := scrollX0 + scrollBarBorderW + scrollBarContentW/2
		downArrowTopY := downY1 + 1
		downArrowWidths := []float32{1, 3, 5, 7, 9}
		for i, w := range downArrowWidths {
			y := downArrowTopY + float32(i)
			x0 := arrowCenterX - w/2
			x1 := arrowCenterX + w/2
			ld.AddLine([2]float32{x0, y}, [2]float32{x1, y}, arrowColor)
		}

		// Draw down button background (top section)
		downP0 := [2]float32{scrollX0, downY0}
		downP1 := [2]float32{scrollX1, downY0}
		downP2 := [2]float32{scrollX1, downY1}
		downP3 := [2]float32{scrollX0, downY1}
		trid.AddQuad(downP0, downP1, downP2, downP3, scrollBg)
		ld.AddLine(downP0, downP1, scrollBorder)
		ld.AddLine(downP1, downP2, scrollBorder)
		ld.AddLine(downP2, downP3, scrollBorder)
		ld.AddLine(downP3, downP0, scrollBorder)

		// Draw up arrow pointing downward: 9,7,5,3,1 pixels wide from top to bottom
		// (positioned in bottom section for scrolling down)
		upArrowTopY := upY1 - 5
		updatedUpArrowWidths := []float32{9, 7, 5, 3, 1}
		for i, w := range updatedUpArrowWidths {
			y := upArrowTopY + float32(i)
			x0 := arrowCenterX - w/2
			x1 := arrowCenterX + w/2
			ld.AddLine([2]float32{x0, y}, [2]float32{x1, y}, arrowColor)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawAltimSetMenu draws the ALTIM SET configuration menu when ep.altimSetMenuOpen is true.
func (ep *ERAMPane) drawAltimSetMenu(ctx *panes.Context, transforms radar.ScopeTransformations, origin [2]float32, width float32, cb *renderer.CommandBuffer) {
	if !ep.altimSetMenuOpen {
		return
	}

	ps := ep.currentPrefs()

	// Calculate fontNum for use in menu config
	fontNum := ps.AltimSet.Font
	if fontNum < 1 {
		fontNum = 1
	}
	if fontNum > 3 {
		fontNum = 3
	}

	blackBg := renderer.RGB{R: 0, G: 0, B: 0}
	greyBg := renderer.RGB{R: 153.0 / 255.0, G: 153.0 / 255.0, B: 153.0 / 255.0}
	greenBg := renderer.RGB{R: 0, G: 157.0 / 255.0, B: 0}
	textColor := renderer.RGB{R: .85, G: .85, B: .85}

	// T/O button - toggles opaque mode
	tLabel := "T"
	tBg := blackBg
	if ps.AltimSet.Opaque {
		tLabel = "O"
		tBg = greyBg
	}

	// BORDER button - grey when ON, black when OFF
	borderBg := blackBg
	if ps.AltimSet.ShowBorder {
		borderBg = greyBg
	}

	// TEAROFF button - grey when ON, black when OFF
	tearoffBg := blackBg
	if ps.AltimSet.ShowIndicators {
		tearoffBg = greyBg
	}

	rows := []ERAMMenuItem{
		{Label: tLabel, BgColor: tBg, Color: textColor, Centered: true, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.AltimSet.Opaque = !ps.AltimSet.Opaque
			}
			return false
		}},
		{Label: "BORDER", BgColor: borderBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.AltimSet.ShowBorder = !ps.AltimSet.ShowBorder
			}
			return false
		}},
		{Label: "TEAROFF", BgColor: tearoffBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickTertiary {
				ps.AltimSet.ShowIndicators = !ps.AltimSet.ShowIndicators
			}
			return false
		}},
		{Label: fmt.Sprintf("LINES %d", ps.AltimSet.Lines), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickPrimary {
				// Left-click: decrement
				if ps.AltimSet.Lines > 3 {
					ps.AltimSet.Lines--
					if ep.altimSetScrollOffset > 0 {
						maxOffset := len(ep.AltimSetAirports) - ps.AltimSet.Lines
						if maxOffset < 0 {
							maxOffset = 0
						}
						if ep.altimSetScrollOffset > maxOffset {
							ep.altimSetScrollOffset = maxOffset
						}
					}
				}
			} else if ct == MenuClickTertiary {
				// Middle-click: increment
				if ps.AltimSet.Lines < 24 {
					ps.AltimSet.Lines++
				}
			}
			return false
		}},
		{Label: fmt.Sprintf("COL %d", ps.AltimSet.Col), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickPrimary {
				// Left-click: decrement
				if ps.AltimSet.Col > 1 {
					ps.AltimSet.Col--
				}
			} else if ct == MenuClickTertiary {
				// Middle-click: increment
				if ps.AltimSet.Col < 4 {
					ps.AltimSet.Col++
				}
			}
			return false
		}},
		{Label: fmt.Sprintf("FONT %d", ps.AltimSet.Font), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickPrimary {
				// Left-click: decrement
				if ps.AltimSet.Font > 1 {
					ps.AltimSet.Font--
				}
			} else if ct == MenuClickTertiary {
				// Middle-click: increment
				if ps.AltimSet.Font < 3 {
					ps.AltimSet.Font++
				}
			}
			return false
		}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.AltimSet.Bright), BgColor: greenBg, Color: textColor, Centered: false, OnClick: func(ct ERAMMenuClickType) bool {
			if ct == MenuClickPrimary {
				// Left-click: decrement
				if ps.AltimSet.Bright > 0 {
					ps.AltimSet.Bright--
				}
			} else if ct == MenuClickTertiary {
				// Middle-click: increment
				if ps.AltimSet.Bright < 100 {
					ps.AltimSet.Bright++
				}
			}
			return false
		}},
		{Label: "TEMPLATE", BgColor: blackBg, Color: textColor, Centered: false},
	}

	cfg := ERAMMenuConfig{
		Title:                 "AS",
		OnClose:               func() { ep.altimSetMenuOpen = false },
		Width:                 width,
		Font:                  ep.ERAMFont(2), // Menu always uses FONT 2, not affected by FONT setting
		ShowBorder:            true,
		BorderColor:           renderer.RGB{R: 213.0 / 255.0, G: 213.0 / 255.0, B: 213.0 / 255.0},
		DismissOnClickOutside: true,
		Rows:                  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, origin, cfg)
}
