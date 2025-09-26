// wxpicker.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
)

const (
	wxOnlyVMC = 0
	wxOnlyIMC = 1
	wxMixed   = 2
	wxUnknown = -1

	// UI dimensions
	windIndicatorRadius = 60
	buttonSize          = 25
	iconSpacing         = 10
	timeSliderWidth     = 250
	calendarColumnWidth = 270
	pickerTableWidth    = 800
	okButtonWidth       = 50
	okButtonHeight      = 25

	// Wind arrow dimensions
	arrowHeadLength = 15
	arrowHeadAngle  = 25
	windDotRadius   = 8
)

// getValidFullDays returns a sorted list of dates where the entire 24-hour period
// is covered by intervals (midnight to midnight in UTC)
func getValidFullDays(intervals []util.TimeInterval) []time.Time {
	var days []time.Time

	for _, interval := range intervals {
		// Start at midnight of the first day that could be fully covered
		// (the day after the interval starts, if it doesn't start at midnight)
		curDay := interval.Start().Truncate(24 * time.Hour)
		if !interval.Start().Equal(curDay) {
			// Interval doesn't start at midnight, so this day isn't fully covered
			curDay = curDay.Add(24 * time.Hour)
		}

		// Add all full days in this interval
		for curDay.Before(interval.End()) {
			nextDay := curDay.Add(24 * time.Hour)
			if !interval.End().Before(nextDay) {
				days = append(days, curDay)
			}
			curDay = nextDay
		}
	}

	return days
}

// dayWeatherStatus returns weather status for a given day
func dayWeatherStatus(metars []wx.METAR, year int, month time.Month, day int) int {
	dayStart := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	nextDay := dayStart.AddDate(0, 0, 1)

	startIdx, _ := slices.BinarySearchFunc(metars, dayStart, func(m wx.METAR, t time.Time) int {
		return m.Time.Compare(t)
	})

	hasVMC, hasIMC := false, false

	for i := startIdx; i < len(metars) && metars[i].Time.Before(nextDay); i++ {
		if metars[i].IsVMC() {
			hasVMC = true
		} else {
			hasIMC = true
		}

		// Early exit if we found both
		if hasVMC && hasIMC {
			return wxMixed
		}
	}

	if hasVMC && !hasIMC {
		return wxOnlyVMC
	} else if hasIMC && !hasVMC {
		return wxOnlyIMC
	} else {
		return wxUnknown
	}
}

func windSpeedColor(speed int) imgui.Vec4 {
	switch {
	case speed <= 2:
		return imgui.Vec4{X: 0.5, Y: 0.5, Z: 0.5, W: 1.0} // gray for calm
	case speed <= 10:
		return imgui.Vec4{X: 0.0, Y: 0.8, Z: 0.0, W: 1.0} // green for light
	case speed <= 20:
		return imgui.Vec4{X: 1.0, Y: 1.0, Z: 0.0, W: 1.0} // yellow for moderate
	case speed <= 35:
		return imgui.Vec4{X: 1.0, Y: 0.6, Z: 0.0, W: 1.0} // orange for strong
	default:
		return imgui.Vec4{X: 1.0, Y: 0.0, Z: 0.0, W: 1.0} // red for very strong
	}
}

type weatherCondition struct {
	icon        string
	description string
	color       imgui.Vec4
	pattern     *regexp.Regexp
}

// weatherTable contains weather conditions, ordered by priority (more specific patterns first)
var weatherTable = []weatherCondition{
	// Thunderstorm
	{
		icon:        renderer.FontAwesomeIconBolt,
		description: "Thunderstorm",
		color:       imgui.Vec4{1.0, 0.8, 0.0, 1.0},      // Yellow
		pattern:     regexp.MustCompile(`^[-+]?(VC)?TS`), // Matches TS with optional intensity/vicinity
	},
	// Heavy Rain (must come before regular rain)
	{
		icon:        renderer.FontAwesomeIconCloudShowersHeavy,
		description: "Heavy Rain",
		color:       imgui.Vec4{0.2, 0.4, 0.8, 1.0},         // Dark blue
		pattern:     regexp.MustCompile(`^(\+|SH)(RA|DZ)$`), // Matches +RA, +DZ, SHRA, SHDZ
	},
	// Rain (light or moderate, without heavy indicator)
	{
		icon:        renderer.FontAwesomeIconCloudRain,
		description: "Rain",
		color:       imgui.Vec4{0.4, 0.6, 0.9, 1.0},                       // Light blue
		pattern:     regexp.MustCompile(`^[-]?(DR|FZ|MI|PR|VC)?(RA|DZ)$`), // Matches rain/drizzle without + or SH
	},
	// Snow/Ice
	{
		icon:        renderer.FontAwesomeIconSnowflake,
		description: "Snow/Ice",
		color:       imgui.Vec4{0.8, 0.9, 1.0, 1.0}, // Light cyan
		pattern:     regexp.MustCompile(`^[-+]?(DR|FZ|MI|PR|SH|VC)?(SN|SG|PL|GR|GS|IC)$`),
	},
	// Fog/Mist
	{
		icon:        renderer.FontAwesomeIconSmog,
		description: "Fog/Mist",
		color:       imgui.Vec4{0.7, 0.7, 0.7, 1.0}, // Gray
		pattern:     regexp.MustCompile(`^[-+]?(DR|FZ|MI|PR|SH|VC)?(FG|BR|HZ|FU|VA|DU|SA)$`),
	},
}

// cloudTable contains cloud coverage conditions, ordered by priority (highest coverage first)
var cloudTable = []weatherCondition{
	// Cloudy (broken/overcast)
	{
		icon:        renderer.FontAwesomeIconCloud,
		description: "Cloudy",
		color:       imgui.Vec4{0.6, 0.6, 0.6, 1.0},       // Medium gray
		pattern:     regexp.MustCompile(`^(BKN|OVC)\d*$`), // Matches BKN or OVC with optional altitude
	},
	// Partly Cloudy (few/scattered)
	{
		icon:        renderer.FontAwesomeIconCloudSun,
		description: "Partly Cloudy",
		color:       imgui.Vec4{0.8, 0.8, 0.8, 1.0},       // Light gray
		pattern:     regexp.MustCompile(`^(FEW|SCT)\d*$`), // Matches FEW or SCT with optional altitude
	},
	// Clear
	{
		icon:        renderer.FontAwesomeIconSun,
		description: "Clear",
		color:       imgui.Vec4{1.0, 0.9, 0.0, 1.0},          // Yellow
		pattern:     regexp.MustCompile(`^(SKC|CLR|CAVOK)$`), // Matches clear sky indicators
	},
}

func parseWeatherConditions(raw string) []weatherCondition {
	// Split raw METAR into parts, stopping at RMK
	parts := strings.Fields(raw)
	if len(parts) > 3 {
		// Drop ICAO, time, and wind.
		parts = parts[3:]
	}
	if idx := slices.Index(parts, "RMK"); idx != -1 {
		parts = parts[:idx]
	}

	var conditions []weatherCondition
	for _, part := range parts {
		// Try each pattern in order (priority matters)
		for _, wc := range weatherTable {
			if wc.pattern.MatchString(part) {
				if !slices.ContainsFunc(conditions, func(c weatherCondition) bool {
					return c.icon == wc.icon
				}) {
					conditions = append(conditions, wc)
				}
				break // Stop after first match for this part
			}
		}
	}

	// Cloud coverage, also checking in priority order.
loop:
	for _, cc := range cloudTable {
		for _, part := range parts {
			if cc.pattern.MatchString(part) {
				conditions = append(conditions, cc)
				break loop
			}
		}
	}

	// Remove regular rain icon if heavy rain is present
	hasHeavyRain := slices.ContainsFunc(conditions, func(c weatherCondition) bool {
		return c.icon == renderer.FontAwesomeIconCloudShowersHeavy
	})
	if hasHeavyRain {
		conditions = util.FilterSlice(conditions, func(c weatherCondition) bool {
			return c.icon != renderer.FontAwesomeIconCloudRain
		})
	}

	return conditions
}

// drawWindBackground draws the background circle for the wind indicator
func drawWindBackground(center [2]float32, drawList *imgui.DrawList) {
	centerImGui := imgui.Vec2{X: center[0], Y: center[1]}
	drawList.AddCircle(centerImGui, windIndicatorRadius-5, imgui.ColorU32Vec4(imgui.Vec4{X: 0.3, Y: 0.3, Z: 0.3, W: 1.0}))
}

// drawDirectionalWind draws a directional wind arrow with gust indication
func drawDirectionalWind(metar wx.METAR, center [2]float32, drawList *imgui.DrawList) {
	color := windSpeedColor(metar.WindSpeed)
	centerImGui := imgui.Vec2{X: center[0], Y: center[1]}

	screenAngle := float32(*metar.WindDir) - 90
	sincos := math.SinCos(math.Radians(screenAngle))
	direction := [2]float32{sincos[1], sincos[0]}

	// Calculate arrow length based on wind speed
	maxLength := float32(windIndicatorRadius - 15)
	speedFactor := float32(metar.WindSpeed) * 1.5
	if speedFactor > maxLength {
		speedFactor = maxLength
	}
	arrowLength := 15 + speedFactor

	// Calculate arrow endpoint
	arrowVector := math.Scale2f(direction, arrowLength)
	arrowEnd := math.Add2f(center, arrowVector)

	// Draw arrow shaft
	drawList.AddLineV(centerImGui, imgui.Vec2{X: arrowEnd[0], Y: arrowEnd[1]}, imgui.ColorU32Vec4(color), 2.0)

	// Draw arrowhead
	// Create arrowhead vectors by rotating the direction vector
	// Arrowheads point outward from center
	rotateLeft := math.Rotator2f(arrowHeadAngle)
	rotateRight := math.Rotator2f(-arrowHeadAngle)

	headLeft := math.Add2f(center, math.Scale2f(rotateLeft(direction), arrowHeadLength))
	headRight := math.Add2f(center, math.Scale2f(rotateRight(direction), arrowHeadLength))

	drawList.AddLineV(centerImGui, imgui.Vec2{X: headLeft[0], Y: headLeft[1]}, imgui.ColorU32Vec4(color), 2.0)
	drawList.AddLineV(centerImGui, imgui.Vec2{X: headRight[0], Y: headRight[1]}, imgui.ColorU32Vec4(color), 2.0)

	// Add gust indication if present
	if metar.WindGust != nil && *metar.WindGust > metar.WindSpeed {
		gustColor := imgui.Vec4{X: color.X, Y: color.Y, Z: color.Z, W: 0.6}
		gustLength := arrowLength + 10
		gustVector := math.Scale2f(direction, gustLength)
		gustEnd := math.Add2f(center, gustVector)
		drawList.AddLine(centerImGui, imgui.Vec2{X: gustEnd[0], Y: gustEnd[1]}, imgui.ColorU32Vec4(gustColor))
	}
}

// drawVariableOrCalmWind draws variable wind text or calm wind dot
func drawVariableOrCalmWind(metar wx.METAR, center [2]float32, drawList *imgui.DrawList) {
	centerImGui := imgui.Vec2{X: center[0], Y: center[1]}

	if metar.WindSpeed > 0 {
		// Variable wind - draw "VRB" text
		textSize := imgui.CalcTextSize("VRB")
		textPos := imgui.Vec2{X: center[0] - textSize.X/2, Y: center[1] - textSize.Y/2}
		drawList.AddTextVec2(textPos, imgui.ColorU32Vec4(windSpeedColor(metar.WindSpeed)), "VRB")
	} else {
		// Calm wind - draw small dot
		drawList.AddCircleFilled(centerImGui, windDotRadius, imgui.ColorU32Vec4(windSpeedColor(0)))
	}
}

// drawWindIndicator draws a visual wind direction and speed indicator
func drawWindIndicator(metar wx.METAR) {
	drawList := imgui.WindowDrawList()
	pos := imgui.CursorScreenPos()
	center := [2]float32{pos.X + windIndicatorRadius, pos.Y + windIndicatorRadius}

	// Draw background circle
	drawWindBackground(center, drawList)

	// Handle different wind conditions
	if metar.WindDir == nil {
		// Variable or calm wind
		drawVariableOrCalmWind(metar, center, drawList)
	} else if metar.WindSpeed > 0 {
		// Draw directional arrow
		drawDirectionalWind(metar, center, drawList)
	}

	// Reserve space for the indicator
	imgui.Dummy(imgui.Vec2{X: windIndicatorRadius * 2, Y: windIndicatorRadius * 2})
}

// formatRawMETAR formats the raw METAR text by removing airport code and remarks
func formatRawMETAR(raw string) string {
	// Remove the first element (airport code)
	if parts := strings.Fields(raw); len(parts) > 1 {
		raw = strings.Join(parts[1:], " ")
	}
	if idx := strings.Index(raw, "RMK"); idx != -1 {
		raw = strings.TrimSpace(raw[:idx])
	}
	return raw
}

// drawVMCIMCStatus displays VMC/IMC status with appropriate colors
func drawVMCIMCStatus(metar wx.METAR) {
	if metar.IsVMC() {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.0, 0.8, 0.0, 1.0}) // Green
		imgui.Text("VMC")
		imgui.PopStyleColor()
	} else {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.8, 0.0, 0.0, 1.0}) // Red
		imgui.Text("IMC")
		imgui.PopStyleColor()
	}
}

// drawVisibilityAndCeiling displays visibility and ceiling information
func drawVisibilityAndCeiling(metar wx.METAR) {
	if vis, err := metar.Visibility(); err == nil {
		imgui.Text(fmt.Sprintf("Visibility: %.1f sm", vis))
	}

	imgui.Spacing()

	if ceil, err := metar.Ceiling(); err == nil {
		if ceil >= 12000 {
			imgui.Text("Ceiling: Unlimited")
		} else {
			imgui.Text(fmt.Sprintf("Ceiling: %d ft AGL", ceil))
		}
	}
}

// drawWindAndWeatherIcons renders wind information and weather condition icons
func drawWindAndWeatherIcons(metar wx.METAR, largeFont *renderer.Font) {
	// Wind text display
	if metar.WindDir == nil {
		if metar.WindSpeed > 0 {
			imgui.Text(fmt.Sprintf("Wind: Variable at %d kt", metar.WindSpeed))
		} else {
			imgui.Text("Wind: Calm")
		}
	} else {
		if metar.WindGust != nil && *metar.WindGust > 0 {
			imgui.Text(fmt.Sprintf("Wind: %03d° at %d kt, gusting %d kt", *metar.WindDir, metar.WindSpeed, *metar.WindGust))
		} else {
			imgui.Text(fmt.Sprintf("Wind: %03d° at %d kt", *metar.WindDir, metar.WindSpeed))
		}
	}

	// Wind indicator and weather icons side by side
	imgui.BeginGroup()

	// Remember starting Y position for alignment
	startY := imgui.CursorPosY()

	// Wind indicator on the left
	drawWindIndicator(metar)

	imgui.SameLine()

	// Weather icons on the right, vertically centered with the wind
	// indicator--icons are approximately 64px tall; ad hoc offset of half
	// that-ish to center.
	iconY := startY + 28
	startX := imgui.CursorPosX()

	imgui.PushFont(&largeFont.Ifont)
	for _, cond := range parseWeatherConditions(metar.Raw) {
		imgui.SetCursorPos(imgui.Vec2{X: startX, Y: iconY})

		imgui.PushStyleColorVec4(imgui.ColText, cond.color)
		imgui.Text(cond.icon)
		imgui.PopStyleColor()

		if imgui.IsItemHovered() {
			imgui.PopFont()
			imgui.SetTooltip(cond.description)
			imgui.PushFont(&largeFont.Ifont)
		}

		iconWidth, _ := largeFont.BoundText(cond.icon, 0)
		startX += float32(iconWidth) + iconSpacing
	}
	imgui.PopFont()

	imgui.EndGroup()
}

// drawMETARDisplay renders the METAR information panel
func drawMETARDisplay(metar wx.METAR, monospaceFont *imgui.Font, largeFont *renderer.Font) {
	imgui.PushFont(monospaceFont)
	imgui.TextWrapped(formatRawMETAR(metar.Raw))
	imgui.PopFont()

	imgui.Spacing()
	drawVMCIMCStatus(metar)

	imgui.Spacing()
	drawVisibilityAndCeiling(metar)

	imgui.Spacing()
	imgui.BeginGroup()
	drawWindAndWeatherIcons(metar, largeFont)
	imgui.EndGroup()
}

// Returns true if the button was clicked
func drawCurrentMonthDayButton(year int, month time.Month, day int, isSelected bool, validDays []time.Time, metars []wx.METAR) bool {
	pushedStyles := 0
	dayDisabled := false

	if day > 0 {
		dayStart := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)

		_, ok := slices.BinarySearchFunc(validDays, dayStart, func(a, b time.Time) int {
			return a.Compare(b)
		})
		dayDisabled = !ok
	}

	if isSelected {
		imgui.PushStyleColorVec4(imgui.ColButton, imgui.Vec4{0.06, 0.53, 0.98, 0.5})
		pushedStyles++
	}

	if dayDisabled {
		imgui.BeginDisabled()
	} else {
		if s := dayWeatherStatus(metars, year, month, day); s != wxUnknown {
			var color imgui.Vec4
			switch s {
			case wxOnlyVMC:
				color = imgui.Vec4{0.0, 0.8, 0.0, 1.0} // Green
			case wxOnlyIMC:
				color = imgui.Vec4{0.8, 0.0, 0.0, 1.0} // Red
			case wxMixed:
				color = imgui.Vec4{0.8, 0.8, 0.0, 1.0} // Yellow
			}
			imgui.PushStyleColorVec4(imgui.ColText, color)
			pushedStyles++
		}
	}

	clicked := imgui.ButtonV(strconv.Itoa(day), imgui.Vec2{buttonSize, buttonSize})

	if dayDisabled {
		imgui.EndDisabled()
	}
	for range pushedStyles {
		imgui.PopStyleColor()
	}

	return clicked && !dayDisabled && day > 0
}

// isMonthBeforeRange checks if the given month is before the start of the valid range
func isMonthBeforeRange(month, start time.Time) bool {
	startOfMonth := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	return month.Before(startOfMonth)
}

// isMonthAfterRange checks if the given month is after the end of the valid range
func isMonthAfterRange(month, end time.Time) bool {
	endOfMonth := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, -1)
	return month.After(endOfMonth)
}

// drawTimeSelector renders the time slider for selecting hour and minute
// Returns true if the time was changed
// Since we only allow selecting full days, this always shows 00:00 to 23:59
func drawTimeSelector(date *time.Time) bool {
	const minMinute = 0
	const maxMinute = 1439 // 23:59

	imgui.PushItemWidth(timeSliderWidth)
	curMinute := int32(date.Hour()*60 + date.Minute())
	changed := imgui.SliderIntV("##timeSlider", &curMinute, minMinute, maxMinute,
		fmt.Sprintf("%02d:%02d", curMinute/60, curMinute%60), imgui.SliderFlagsAlwaysClamp)
	if changed {
		*date = time.Date(date.Year(), date.Month(), date.Day(), int(curMinute)/60, int(curMinute)%60, 0, 0, time.UTC)
	}
	imgui.PopItemWidth()

	return changed
}

// drawMonthNavigation renders the month navigation buttons and header
// Returns true if the month was changed
func drawMonthNavigation(date *time.Time, validDays []time.Time, columnWidth float32) bool {
	start := validDays[0]
	end := validDays[len(validDays)-1].AddDate(0, 0, 1) // End of the last valid day

	changed := false
	year, month := date.Year(), date.Month()
	prevMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	nextMo := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)

	// Month/Year navigation
	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, imgui.Vec2{2, 2})

	// Previous month button
	prevDisabled := isMonthBeforeRange(prevMonth, start)
	if prevDisabled {
		imgui.BeginDisabled()
	}
	if imgui.ArrowButton("##prev", imgui.DirLeft) {
		*date = prevMonth
		if date.Before(start) {
			*date = start
		}
		changed = true
	}
	if prevDisabled {
		imgui.EndDisabled()
	}

	imgui.SameLine()

	// Month and year display
	monthYearStr := fmt.Sprintf("%s %d", month.String(), year)
	textSize := imgui.CalcTextSize(monthYearStr)
	imgui.SetCursorPosX(buttonSize + (columnWidth-2*buttonSize-textSize.X)/2)
	imgui.Text(monthYearStr)

	imgui.SameLine()
	imgui.SetCursorPosX(columnWidth - buttonSize)

	// Next month button
	nextDisabled := isMonthAfterRange(nextMo, end)
	if nextDisabled {
		imgui.BeginDisabled()
	}
	if imgui.ArrowButton("##next", imgui.DirRight) {
		*date = nextMo
		if date.After(end) {
			*date = end
		}
		changed = true
	}
	if nextDisabled {
		imgui.EndDisabled()
	}

	imgui.PopStyleVar()
	imgui.Separator()

	return changed
}

// drawCalendarHeader renders the weekday headers (Su, Mo, Tu, etc.)
func drawCalendarHeader() {
	dayHeaders := []string{"Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"}
	for _, h := range dayHeaders {
		imgui.TableSetupColumn(h)
	}

	// Header row
	imgui.TableNextRow()
	for _, h := range dayHeaders {
		imgui.TableNextColumn()
		ts := imgui.CalcTextSize(h)
		imgui.SetCursorPosX(imgui.CursorPosX() + (buttonSize-ts.X)/2)
		imgui.Text(h)
	}
}

// drawCalendarGrid renders the calendar grid with day buttons
// Returns true if a date was selected
func drawCalendarGrid(date *time.Time, validDays []time.Time, metars []wx.METAR) bool {
	changed := false
	year, month := date.Year(), date.Month()
	prevMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)

	// Calendar grid styling
	imgui.PushStyleColorVec4(imgui.ColButton, imgui.Vec4{0, 0, 0, 0})
	imgui.PushStyleColorVec4(imgui.ColButtonHovered, imgui.Vec4{0.26, 0.59, 0.98, 0.4})
	imgui.PushStyleColorVec4(imgui.ColButtonActive, imgui.Vec4{0.06, 0.53, 0.98, 1.0})

	// Calendar days
	firstWeekday := int(time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Weekday())
	daysInCurrMonth := daysInMonth(*date)
	daysInPrevMonth := daysInMonth(prevMonth)

	day, nextDay := 1, 1
	for week := 0; week < 6 && day <= daysInCurrMonth; week++ {
		imgui.TableNextRow()
		for col := range 7 {
			imgui.TableNextColumn()

			if week == 0 && col < firstWeekday {
				// Previous month days
				imgui.BeginDisabled()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.5, 0.5, 0.5, 0.5})
				imgui.ButtonV(strconv.Itoa(daysInPrevMonth-firstWeekday+1+col)+"##prev", imgui.Vec2{buttonSize, buttonSize})
				imgui.PopStyleColor()
				imgui.EndDisabled()
			} else if day <= daysInCurrMonth {
				// Current month days
				if drawCurrentMonthDayButton(year, month, day, day == date.Day(), validDays, metars) {
					// Since we only allow full days, just set to midnight
					*date = time.Date(year, month, day, date.Hour(), date.Minute(), 0, 0, time.UTC)
					changed = true
				}
				day++
			} else {
				// Next month days
				imgui.BeginDisabled()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.5, 0.5, 0.5, 0.5})
				imgui.ButtonV(strconv.Itoa(nextDay)+"##next", imgui.Vec2{buttonSize, buttonSize})
				imgui.PopStyleColor()
				imgui.EndDisabled()
				nextDay++
			}
		}
	}

	imgui.PopStyleColor()
	imgui.PopStyleColor()
	imgui.PopStyleColor()

	return changed
}

// drawCalendar renders the calendar portion of the date picker
// Returns true if a date was selected
func drawCalendar(date *time.Time, validDays []time.Time, metars []wx.METAR, columnWidth float32) bool {
	changed := drawMonthNavigation(date, validDays, columnWidth)

	if imgui.BeginTableV("calendar_full", 7, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
		drawCalendarHeader()
		changed = drawCalendarGrid(date, validDays, metars) || changed
		imgui.EndTable()
	}

	return changed
}

// validateAndAdjustDate validates the date is within valid days and adjusts if necessary
// Returns true if the date was changed
func validateAndAdjustDate(date *time.Time, validDays []time.Time) bool {
	if len(validDays) == 0 {
		return false
	}

	// Check if the current date is a valid day
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	idx, found := slices.BinarySearchFunc(validDays, dayStart, func(a, b time.Time) int {
		return a.Compare(b)
	})

	if found {
		return false // date unchanged
	}

	// Current day is not valid, find the nearest valid day
	if idx >= len(validDays) {
		// After all valid days, use the last one
		*date = validDays[len(validDays)-1]
	} else if idx == 0 {
		// Before all valid days, use the first one
		*date = validDays[0]
	} else {
		// Between two valid days, pick the closer one
		prevDay, nextDay := validDays[idx-1], validDays[idx]
		if dayStart.Sub(prevDay) < nextDay.Sub(dayStart) {
			*date = prevDay
		} else {
			*date = nextDay
		}
	}
	return true // changed the date
}

// drawTimePickerPopup renders the popup with date picker and METAR display
// Returns true if the time was changed
func drawTimePickerPopup(date *time.Time, intervals []util.TimeInterval, metars []wx.METAR, metarIdx int, monospaceFont *imgui.Font) bool {
	changed := false

	// Compute valid days from intervals
	validDays := getValidFullDays(intervals)
	if len(validDays) == 0 {
		// No valid full days, shouldn't happen but handle gracefully
		return false
	}

	if imgui.BeginTableV("picker_layout", 2, imgui.TableFlagsBorders|imgui.TableFlagsSizingFixedFit, imgui.Vec2{pickerTableWidth, 0}, 0) {
		imgui.TableSetupColumnV("Date Selection", imgui.TableColumnFlagsWidthFixed, calendarColumnWidth, 0)
		imgui.TableSetupColumnV("Weather Information", imgui.TableColumnFlagsWidthStretch, 0, 0)
		imgui.TableHeadersRow()

		imgui.TableNextRow()

		// Left side: Date picker
		imgui.TableNextColumn()
		changed = drawCalendar(date, validDays, metars, calendarColumnWidth) || changed

		imgui.Separator()
		changed = drawTimeSelector(date) || changed

		// Right side: METAR display
		imgui.TableNextColumn()
		largeFont := renderer.GetFont(renderer.FontIdentifier{Name: renderer.LargeFontAwesomeOnly, Size: 64})
		drawMETARDisplay(metars[metarIdx], monospaceFont, largeFont)

		// "Ok" button--push the button to the bottom using available space
		availableHeight := imgui.ContentRegionAvail().Y
		if availableHeight > okButtonHeight {
			imgui.SetCursorPosY(imgui.CursorPosY() + availableHeight - okButtonHeight)
		}
		// Now draw it all the way to the right.
		availableWidth := imgui.ContentRegionAvail().X
		imgui.SetCursorPosX(imgui.CursorPosX() + availableWidth - okButtonWidth)
		if imgui.ButtonV("Ok", imgui.Vec2{okButtonWidth, 0}) {
			imgui.CloseCurrentPopup()
		}

		imgui.EndTable()
	}

	return changed
}

// TimePicker displays a calendar widget for time selection and displays
// the METAR for the selected time.  Returns true if the time was changed.
func TimePicker(label string, date *time.Time, intervals []util.TimeInterval, metars []wx.METAR, monospaceFont *imgui.Font) bool {
	// We lose the timezone when the times come through RPC from the
	// server, so reestablish that here since we'd like to work in UTC
	// throughout.
	intervals = util.MapSlice(intervals, func(ti util.TimeInterval) util.TimeInterval {
		return util.TimeInterval{ti[0].UTC(), ti[1].UTC()}
	})

	// Compute valid days from intervals
	validDays := getValidFullDays(intervals)
	if len(validDays) == 0 {
		return false
	}

	changed := validateAndAdjustDate(date, validDays)

	// Find the most recent METAR before `date`
	metarIdx, ok := slices.BinarySearchFunc(metars, *date, func(m wx.METAR, t time.Time) int {
		return m.Time.Compare(t)
	})
	if !ok && metarIdx > 0 {
		metarIdx--
	}
	// Ensure metarIdx is within bounds
	if metarIdx >= len(metars) {
		metarIdx = len(metars) - 1
	}

	imgui.Text(label)
	imgui.SameLine()
	if imgui.Button(date.Format("2006-01-02 15:04")) {
		imgui.OpenPopupStr(label + "_popup")
	}
	if imgui.BeginPopupV(label+"_popup", imgui.WindowFlagsNoMove|imgui.WindowFlagsNoResize) {
		changed = drawTimePickerPopup(date, intervals, metars, metarIdx, monospaceFont) || changed
		imgui.EndPopup()
	}

	return changed
}

func daysInMonth(d time.Time) int {
	switch d.Month() {
	case time.January, time.March, time.May, time.July, time.August, time.October, time.December:
		return 31
	case time.April, time.June, time.September, time.November:
		return 30
	case time.February:
		if year := d.Year(); (year%4 == 0 && year%100 != 0) || (year%400 == 0) {
			return 29 // leap year
		}
		return 28
	default:
		return 30
	}
}
