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
	font := ep.ERAMFont(2)
	textStyle := renderer.TextStyle{
		Font:        font,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	_, by := font.BoundText("0", textStyle.LineSpacing)
	lineH := float32(by + 2)

	const boxW = float32(13)
	const boxH = float32(17)
	const boxGap = float32(4)
	const boxTopPad = float32(8)
	const boxBottomPad = float32(8)

	p0 := ps.AltimSet.Position
	width := altimWindowWidth

	titleH := float32(16)
	if _, tby := font.BoundText("ALTIM SET", textStyle.LineSpacing); tby > 0 {
		titleH = float32(tby) + 4
	}

	numRows := len(ep.AltimSetAirports)
	var bodyHeight float32
	if numRows == 0 {
		bodyHeight = lineH + 4
	} else {
		bodyHeight = boxTopPad + boxBottomPad + float32(numRows)*boxH + float32(numRows-1)*boxGap
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
	borderColor := ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .914, G: .914, B: .914})
	ld.AddLine(p0, p1, borderColor)
	ld.AddLine(p1, p2, borderColor)
	ld.AddLine(p2, p3, borderColor)
	ld.AddLine(p3, p0, borderColor)

	// Title bar background (black)
	titleP0 := p0
	titleP1 := math.Add2f(p0, [2]float32{width, 0})
	titleP2 := math.Add2f(titleP1, [2]float32{0, -titleH})
	titleP3 := math.Add2f(p0, [2]float32{0, -titleH})
	trid.AddQuad(titleP0, titleP1, titleP2, titleP3, renderer.RGB{R: 0, G: 0, B: 0})

	mouse := ctx.Mouse

	// Title text (centered)
	titleText := "ALTIM SET"
	ttw, tth := font.BoundText(titleText, textStyle.LineSpacing)
	titlePos := math.Add2f(titleP0, [2]float32{width/2 - float32(ttw)/2, -titleH/2 + float32(tth)/2})
	titleColor := ps.Brightness.Text.ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85})

	// M button (left side of title bar)
	mLabel := "M"
	mw, mh := font.BoundText(mLabel, textStyle.LineSpacing)
	mPad := float32(2)
	mRect := math.Extent2D{
		P0: [2]float32{titleP0[0], titleP0[1] - titleH},
		P1: [2]float32{titleP0[0] + mPad + float32(mw) + mPad, titleP0[1]},
	}

	// Minimize button (right side of title bar)
	minLabel := "-"
	minw, minh := font.BoundText(minLabel, textStyle.LineSpacing)
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
	td.AddText(titleText, titlePos, renderer.TextStyle{Font: font, Color: titleColor})

	mTextColor := textStyle.Color
	if mouseInsideM {
		mTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(mLabel, math.Add2f(titleP0, [2]float32{mPad, -titleH/2 + float32(mh)/2}),
		renderer.TextStyle{Font: font, Color: mTextColor, LineSpacing: textStyle.LineSpacing})

	minTextColor := textStyle.Color
	if mouseInsideMin {
		minTextColor = toolbarHoveredOutlineColor
	}
	td.AddText(minLabel, [2]float32{minRect.P0[0] + mPad, titleP1[1] - titleH/2 + float32(minh)/2},
		renderer.TextStyle{Font: font, Color: minTextColor, LineSpacing: textStyle.LineSpacing})

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
		switch {
		case mRect.Inside(mouse.Pos):
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

	// Draw airport rows
	textColor := ps.Brightness.Text.ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85})
	rowX0 := bodyP0[0] + 4
	yellowColor := renderer.RGB{R: 159.0 / 255.0, G: 163.0 / 255.0, B: 9.0 / 255.0}
	boxBorderColor := renderer.RGB{R: 0.5, G: 0.5, B: 0.5}

	textWidth := func(s string) float32 {
		w, _ := font.BoundText(s, textStyle.LineSpacing)
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
	for i, icao := range ep.AltimSetAirports {
		displayID := altimDisplayID(icao)
		result, hasResult := ep.altimSetMetars[icao]

		// Yellow indicator box
		boxX0 := rowX0
		boxX1 := rowX0 + boxW
		boxY1 := bodyP0[1] - boxTopPad - float32(i)*(boxH+boxGap) // top of box
		boxY0 := boxY1 - boxH                                     // bottom of box
		bp0 := [2]float32{boxX0, boxY0}
		bp1 := [2]float32{boxX1, boxY0}
		bp2 := [2]float32{boxX1, boxY1}
		bp3 := [2]float32{boxX0, boxY1}
		trid.AddQuad(bp0, bp1, bp2, bp3, yellowColor)
		ld.AddLine(bp0, bp1, boxBorderColor)
		ld.AddLine(bp1, bp2, boxBorderColor)
		ld.AddLine(bp2, bp3, boxBorderColor)
		ld.AddLine(bp3, bp0, boxBorderColor)

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
			td.AddText(line, textCursor, renderer.TextStyle{Font: font, Color: textColor, LineSpacing: 0})
			continue
		}

		prefix := fmt.Sprintf("%-4s  ", displayID)
		timeField := fmt.Sprintf("%4s", timeStr)
		mid := " "
		altField := fmt.Sprintf("%3s", altStr)
		suffix := "  "
		line := prefix + timeField + mid + altField + suffix

		td.AddText(line, textCursor, renderer.TextStyle{Font: font, Color: textColor, LineSpacing: 0})

		if staleTime && timeStr != "...." {
			underlineText(textCursor[0]+textWidth(prefix), textCursor[1], timeField, textColor)
		}
		if altBelowStandard && altStr != "..." {
			underlineText(textCursor[0]+textWidth(prefix)+textWidth(timeField)+textWidth(mid), textCursor[1], altField, textColor)
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}
