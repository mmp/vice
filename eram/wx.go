package eram

import (
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

	// --- Layout ---
	fontNum := ps.WX.Font
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

	// List uses selected font
	listFont := ep.ERAMFont(fontNum)
	textStyle := renderer.TextStyle{
		Font:        listFont,
		Color:       radar.Brightness(90).ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	_, by := listFont.BoundText("0", textStyle.LineSpacing)
	lineH := float32(by + 2)

	// Scale boxes based on font size
	fontScale := []float32{0.85, 1.0, 1.2}[fontNum-1]
	boxTopPad := float32(8) * fontScale
	boxBottomPad := float32(8) * fontScale

	p0 := ps.WX.Position

	// Fixed window width
	width := wxWindowWidth

	titleH := float32(16)
	if _, tby := titleFont.BoundText("WX", titleTextStyle.LineSpacing); tby > 0 {
		titleH = float32(tby) + 4
	}

	visibleRows := ps.WX.Lines
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
		bodyHeight = boxTopPad + boxBottomPad + float32(visibleRows)*lineH
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
	if ps.WX.ShowBorder {
		borderColor := ps.Brightness.Border.ScaleRGB(renderer.RGB{R: .914, G: .914, B: .914})
		ld.AddLine(p0, p1, borderColor)
		ld.AddLine(p1, p2, borderColor)
		ld.AddLine(p2, p3, borderColor)
		ld.AddLine(p3, p0, borderColor)
	}

	// Title bar background (black or grey based on Opaque mode)
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
		switch {
		case mRect.Inside(mouse.Pos):
			ctx.SetMousePosition(math.Add2f(mouse.Pos, [2]float32{width * 1.5, 0}))
			ep.wxMenuOpen = !ep.wxMenuOpen
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

	// Menu, if open (drawn to the right side of the title by default)
	if ep.wxMenuOpen {
		menuOrigin := math.Add2f(titleP3, [2]float32{width, titleH})
		ep.drawWXMenu(ctx, transforms, menuOrigin, 150, cb)
	}

	// Draw METAR list
	bright := ps.WX.Bright
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

	// Scale text color based on bright
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
	boxW := float32(13) * fontScale
	boxH := lineH
	boxGap := float32(4) * fontScale

	// Render each station's METAR
	yOffset := bodyP0[1]
	for _, icao := range ep.WXReportStations {
		displayID := wxDisplayID(icao)
		result, hasResult := ep.wxMetars[icao]

		// Record the top of this station block for border drawing
		stationBlockTop := yOffset

		// Yellow indicator box for station ID - 8 pixels from top of border area
		boxX0 := bodyP0[0] + 4
		boxX1 := boxX0 + boxW
		boxY1 := yOffset - 8
		boxY0 := boxY1 - boxH

		bp0 := [2]float32{boxX0, boxY0}
		bp1 := [2]float32{boxX1, boxY0}
		bp2 := [2]float32{boxX1, boxY1}
		bp3 := [2]float32{boxX0, boxY1}
		trid.AddQuad(bp0, bp1, bp2, bp3, yellowColor)
		ld.AddLine(bp0, bp1, greyBorderColor)
		ld.AddLine(bp1, bp2, greyBorderColor)
		ld.AddLine(bp2, bp3, greyBorderColor)
		ld.AddLine(bp3, bp0, greyBorderColor)

		// Draw station ID next to box - position text baseline at bottom of yellow box
		textX := boxX1 + boxGap
		textY := boxY0 + float32(by)
		td.AddText(displayID, [2]float32{textX, textY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})

		// Calculate where the station ID text ends so METAR starts after it
		displayIDWidth, _ := listFont.BoundText(displayID, textStyle.LineSpacing)
		metarStartX := textX + float32(displayIDWidth) + boxGap

		// Draw METAR text if available
		if hasResult && !ep.wxFetching[icao] {
			if result.err == nil && result.rawText != "" {
				// Find the Zulu time indicator (format: DDHHMMZ)
				zuluIdx := strings.Index(result.rawText, "Z")
				var metarData string
				if zuluIdx != -1 && zuluIdx >= 4 {
					// Extract HHMM (4 chars before Z), skip DD prefix and Z itself
					// Format is DDHHMMZ, so 4 chars before Z gets us HHMM
					hhmmStart := zuluIdx - 4
					hhmmStr := result.rawText[hhmmStart:zuluIdx]
					restStr := result.rawText[zuluIdx+1:] // Everything after Z
					if restStr != "" {
						metarData = hhmmStr + " " + restStr
					} else {
						metarData = hhmmStr
					}

					// Word wrap the METAR text
					// Calculate available width from where METAR starts to 53 pixels before right edge (adds 10px text room)
					maxTextWidth := width - (metarStartX - bodyP0[0]) - 53
					words := strings.Fields(metarData)
					var currentLine string
					currentY := textY
					isFirstLine := true

					for _, word := range words {
						testLine := currentLine
						if testLine != "" {
							testLine += " "
						}
						testLine += word

						w, _ := listFont.BoundText(testLine, textStyle.LineSpacing)
						if float32(w) > maxTextWidth && currentLine != "" {
							// Draw current line and move to next line
							if isFirstLine {
								td.AddText(currentLine, [2]float32{metarStartX, currentY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
								isFirstLine = false
							} else {
								// Wrapped lines align under the text (metarStartX)
								td.AddText(currentLine, [2]float32{metarStartX, currentY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
							}
							currentY -= lineH
							currentLine = word
						} else {
							currentLine = testLine
						}
					}

					// Draw last line
					if currentLine != "" {
						td.AddText(currentLine, [2]float32{metarStartX, currentY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
						currentY -= lineH
					}

					yOffset = currentY - boxGap
				} else {
					// Draw error message
					td.AddText("  -M-  (METAR unavailable)", [2]float32{metarStartX, textY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
					yOffset = textY - lineH - boxGap
				}
			} else {
				// Draw loading or missing message
				td.AddText("  ....  (Loading)", [2]float32{metarStartX, textY}, renderer.TextStyle{Font: listFont, Color: textColor, LineSpacing: 0})
				yOffset = textY - lineH - boxGap
			}
		}

		// Add spacing between stations
		yOffset -= boxGap

		// Draw 1px grey border around each station block
		stationBlockBottom := yOffset
		stationBlockLeft := bodyP0[0] + 2
		stationBlockRight := bodyP1[0] - 2

		borderP0 := [2]float32{stationBlockLeft, stationBlockBottom}
		borderP1 := [2]float32{stationBlockRight, stationBlockBottom}
		borderP2 := [2]float32{stationBlockRight, stationBlockTop}
		borderP3 := [2]float32{stationBlockLeft, stationBlockTop}
		ld.AddLine(borderP0, borderP1, greyBorderColor)
		ld.AddLine(borderP1, borderP2, greyBorderColor)
		ld.AddLine(borderP2, borderP3, greyBorderColor)
		ld.AddLine(borderP3, borderP0, greyBorderColor)
	}

	// Commit all draw commands
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// drawWXMenu renders the menu for the WX window (similar to ALTIM SET menu).
func (ep *ERAMPane) drawWXMenu(ctx *panes.Context, transforms radar.ScopeTransformations, menuOrigin [2]float32, menuWidth float32, cb *renderer.CommandBuffer) {
	// TODO: Implement WX menu similar to ALTIM SET menu
	// For now, this is a placeholder
	ps := ep.currentPrefs()

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	font := ep.ERAMFont(2)
	textStyle := renderer.TextStyle{
		Font:        font,
		Color:       ps.Brightness.Text.ScaleRGB(renderer.RGB{R: .85, G: .85, B: .85}),
		LineSpacing: 0,
	}

	// Simple menu placeholder
	menuHeight := float32(100)
	p0 := menuOrigin
	p1 := math.Add2f(p0, [2]float32{menuWidth, 0})
	p2 := math.Add2f(p1, [2]float32{0, -menuHeight})
	p3 := math.Add2f(p0, [2]float32{0, -menuHeight})

	// Menu background
	trid.AddQuad(p0, p1, p2, p3, renderer.RGB{R: 0, G: 0, B: 0})

	// Menu border
	borderColor := ps.Brightness.Border.ScaleRGB(menuOutlineColor)
	ld.AddLine(p0, p1, borderColor)
	ld.AddLine(p1, p2, borderColor)
	ld.AddLine(p2, p3, borderColor)
	ld.AddLine(p3, p0, borderColor)

	// Placeholder text
	textPos := math.Add2f(p0, [2]float32{10, -20})
	td.AddText("WX Menu", textPos, textStyle)

	// Commit all draw commands
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}
