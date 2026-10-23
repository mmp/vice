// eram/input.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// The command line: the MCA (Message Composition Area), where the user
// types commands and clicks positions and tracks into them; the keyboard
// and mouse handling that feeds it; and the RA (Response Area), where the
// commands' responses appear (4.3).

///////////////////////////////////////////////////////////////////////////
// Input text and feedback

// Custom text characters. Some of these are not for all fonts. Size 11 has everything.
const checkMark string = "r"
const xMark string = "s"
const upArrow string = "t"
const downArrow string = "u"
const hsfIndicator string = "v"
const locationSymbol string = "w"
const vci string = " x"
const circleClear string = "y"
const circleFilled string = "z"

type inputChar struct {
	char          rune
	location      math.Point2LL
	trackCallsign av.ADSBCallsign // set on 'locationSymbol' chars added by AddLocation when the click landed on a track
}

type inputText []inputChar

func (inp *inputText) Set(str string) {
	inp.Clear()
	for _, char := range formatInput(str) {
		*inp = append(*inp, inputChar{char: char})
	}
}

func (inp *inputText) AddLocation(location math.Point2LL, callsign av.ADSBCallsign) {
	// Trim trailing whitespace inputChars in place so prior chars (and the
	// click locations they carry) are preserved.
	for len(*inp) > 0 && unicode.IsSpace((*inp)[len(*inp)-1].char) {
		*inp = (*inp)[:len(*inp)-1]
	}
	for _, char := range formatInput(" " + locationSymbol + " ") {
		*inp = append(*inp, inputChar{char: char, location: location, trackCallsign: callsign})
	}
}

// No formatting needed
func (inp *inputText) AddBasic(str string) {
	for _, char := range formatInput(str) {
		*inp = append(*inp, inputChar{char: char})
	}
}

func formatInput(str string) string {
	output := strings.ReplaceAll(str, "`", circleClear)
	output = strings.ReplaceAll(output, "~", circleFilled)
	output = strings.ReplaceAll(output, "[", upArrow)
	output = strings.ReplaceAll(output, "]", downArrow)
	return output
}

func (inp *inputText) DeleteOne() {
	if len(*inp) > 0 {
		*inp = (*inp)[:len(*inp)-1]
	}
}

func (inp *inputText) Clear() {
	*inp = (*inp)[:0]
}

func (inp inputText) String() string {
	var sb strings.Builder
	for _, ic := range inp {
		sb.WriteString(string(ic.char))
	}
	return sb.String()
}

// feedbackMessage is the MCA feedback area's content: either nothing, a
// success message (green check + text), or an error message (red x + text).
// The text is stored with formatInput already applied so render-time work is
// just wrapping + drawing.
type feedbackMessage struct {
	kind feedbackKind
	msg  string
}

type feedbackKind int

const (
	feedbackNone feedbackKind = iota
	feedbackSuccess
	feedbackError
)

func (m *feedbackMessage) Success(str string) {
	m.kind = feedbackSuccess
	m.msg = formatInput(str)
}

func (m *feedbackMessage) Error(err error) {
	if err == nil {
		return
	}
	m.kind = feedbackError
	m.msg = formatInput(toUpper(err.Error()))
}

func (m *feedbackMessage) Clear() {
	*m = feedbackMessage{}
}

// toUpper uppercases the input but preserves the lowercase
// location symbol (w) so clicked locations display correctly in errors.
func toUpper(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if string(r) == locationSymbol {
			b.WriteRune(r)
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

type CommandStatus struct {
	responseArea []string
	feedbackArea []string
}

func (ep *Scope) executeERAMCommand(ctx *scope.Context, cmdLine inputText) (CommandStatus, error) {
	original := strings.TrimSpace(cmdLine.String())

	// Extract all embedded locations from clicking while typing, along with
	// the callsigns (if any) of tracks under each click resolved at click time.
	var mousePositions []math.Point2LL
	var trackCallsigns []av.ADSBCallsign
	for _, ic := range cmdLine {
		if string(ic.char) == locationSymbol {
			mousePositions = append(mousePositions, ic.location)
			trackCallsigns = append(trackCallsigns, ic.trackCallsign)
		}
	}

	if status, err, handled := ep.tryExecuteUserCommand(ctx, original, mousePositions, trackCallsigns); handled {
		return status, err
	}

	// No registered command matched the input; surface a generic syntax error so the user gets
	// feedback instead of silent no-op (e.g. "QP <FLID> <SECTOR>" with the args swapped).
	if original != "" {
		return CommandStatus{}, ErrCommandFormat
	}
	return CommandStatus{}, nil
}

func (ep *Scope) displayError(err error, ctx *scope.Context) {
	if err != nil {
		ep.feedbackArea.Error(GetError(err, ctx.Lg))
		ep.soundAlarm()
	}
}

// applyCommandStatus routes a CommandStatus to the feedback/response areas:
// an error overrides everything; otherwise non-empty feedback and response
// lines are joined with newlines and shown.
func (ep *Scope) applyCommandStatus(ctx *scope.Context, status CommandStatus, err error) {
	if err != nil {
		ep.displayError(err, ctx)
		return
	}

	if len(status.feedbackArea) > 0 {
		ep.feedbackArea.Success(strings.Join(status.feedbackArea, "\n"))
	}
	if len(status.responseArea) > 0 {
		ep.responseArea = formatInput(strings.Join(status.responseArea, "\n"))
	}
}

///////////////////////////////////////////////////////////////////////////
// Keyboard

// AFAIK, you can only type white, regular characters in the input (apart from the location symbols)
func (ep *Scope) processKeyboardInput(ctx *scope.Context) {
	if ctx.Keyboard == nil {
		return
	}
	// An open keyboard-taking popup (e.g. the free-form text menu) gets the
	// keystrokes instead of the command input.
	if kp, ok := ep.popup.(keyboardPopup); ok {
		if kp.handleKeyboard(ep, ctx) {
			return
		}
	}
	// Likewise a clicked altitude limits text box takes them; the sub-entry
	// box merely being displayed does not. So do a Range View field clicked
	// for entry, the PREFSET menu taking a name to save under, and the DRAW
	// menu taking text or a circle's location.
	if ep.handleAltitudeLimitsKeyboard(ctx) || ep.handleRangeViewKeyboard(ctx) || ep.handlePrefSetKeyboard(ctx) ||
		ep.handleAnnotationKeyboard(ctx) {
		return
	}
	ps := ep.currentPrefs()
	keyboardInput := strings.ToUpper(ctx.Keyboard.Input)
	ep.input.AddBasic(keyboardInput)
	input := ep.input.String()
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyG: // debugging
			if ctx.Keyboard.KeyControl() && ctx.Keyboard.KeyShift() && ctx.Mouse != nil {
				big := ctx.Mouse.Pos
				big[1] -= 38
				ps.MCA.Position = big
				ps.RA.Position = [2]float32{big[0] + 390, big[1]}
			}
		case imgui.KeyBackspace:
			if ep.drawingRoute {
				if n := len(ep.drawRoutePoints); n > 0 {
					ep.setDrawRoutePoints(ctx, ep.drawRoutePoints[:n-1])
				}
			} else if len(ep.input) > 0 {
				ep.input = ep.input[:len(ep.input)-1]
			}
		case imgui.KeyEnter:
			// Cancel any in-progress view drag so an incidental click in the
			// MCA body doesn't leave the drag preview following the cursor
			// after the command completes.
			ep.viewRepo.Cancel()
			// Process the command
			status, err := ep.executeERAMCommand(ctx, ep.input)
			ep.input.Clear()
			ep.applyCommandStatus(ctx, status, err)
		case imgui.KeyEscape:
			if _, ok := ep.popup.(*pickAreaPopup); ok {
				ep.popup = nil
				break
			}
			if ep.cancelToolbarGesture(ctx) {
				break
			}
			if ep.saaTearingOff != "" {
				ep.saaTearingOff = ""
				break
			}
			// Cancel any in-progress drag.
			anyActive := ep.viewRepo.activeID != ""
			if anyActive {
				ep.viewRepo.Cancel()
			}
			if !anyActive {
				if ep.drawingRoute {
					ep.drawingRoute, ep.drawRoutePoints = false, nil
					ep.responseArea = ""
				}
				ep.input.Clear()
				ep.feedbackArea.Clear()
			}
		case imgui.KeyTab:
			if input == "" {
				ep.input.Set("TG ")
			}
		case imgui.KeyPageUp: // velocity vector *2
			if ep.VelocityTime == 0 {
				ep.VelocityTime = 1
			} else if ep.VelocityTime < 8 {
				ep.VelocityTime *= 2
			}
		case imgui.KeyPageDown: // velocity vector /2
			if ep.VelocityTime > 0 {
				ep.VelocityTime /= 2
			} else {
				ep.VelocityTime = 0
			}
		case imgui.KeyF1:
			if ctx.Keyboard.KeyShift() {
				ep.input.Set("DM ")
			} else {
				ep.input.Set("QF ")
			}
		case imgui.KeyF2:
			if ctx.Keyboard.KeyShift() {
				ep.input.Set("QD ")
			} else {
				ep.input.Set("QP ")
			}
		case imgui.KeyF3:
			ep.input.Set("QT ")
		case imgui.KeyF4:
			ep.input.Set("QX ")
		case imgui.KeyF5:
			ep.input.Set("QZ ")
		case imgui.KeyF6:
			ep.input.Set("QU ")
		case imgui.KeyF7:
			if ctx.Keyboard.KeyShift() {
				ep.input.Set("WR ")
			} else {
				ep.input.Set("QL ")
			}
		case imgui.KeyF8:
			if ctx.Keyboard.KeyShift() {
				ep.input.Set("QR ")
			} else {
				ep.input.Set("QQ ")
			}
		case imgui.KeyF9:
			ep.input.Set("QB ")
		}
	}

	if (keyboardInput != "" || len(ctx.Keyboard.Pressed) > 0) && !ctx.TCWIsPrivileged(ctx.UserTCW) &&
		strings.HasPrefix(ep.input.String(), "TG") {
		// An aircraft control instruction is being entered; as with STARS'
		// TGT GEN mode, hold radio transmissions for the coming few seconds.
		ctx.Client.HoldRadioTransmissions()
	}
}

///////////////////////////////////////////////////////////////////////////
// Mouse

// Mouse button helpers:
// When UseRightClick is set, logical primary = physical right button click, logical tertiary = physical left button click.
func (ep *Scope) mousePrimaryClicked(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Clicked[platform.MouseButtonSecondary]
	}
	return m.Clicked[platform.MouseButtonPrimary]
}

func (ep *Scope) mousePrimaryDown(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Down[platform.MouseButtonSecondary]
	}
	return m.Down[platform.MouseButtonPrimary]
}

func (ep *Scope) mousePrimaryReleased(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Released[platform.MouseButtonSecondary]
	}
	return m.Released[platform.MouseButtonPrimary]
}

func (ep *Scope) mouseTertiaryClicked(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Clicked[platform.MouseButtonPrimary]
	}
	return m.Clicked[platform.MouseButtonTertiary]
}

func (ep *Scope) mouseTertiaryDown(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Down[platform.MouseButtonPrimary]
	}
	return m.Down[platform.MouseButtonTertiary]
}

func (ep *Scope) mouseTertiaryReleased(m *platform.MouseState) bool {
	if m == nil {
		return false
	}
	if ep.currentPrefs().UseRightClick {
		return m.Released[platform.MouseButtonPrimary]
	}
	return m.Released[platform.MouseButtonTertiary]
}

// clearMousePrimaryConsumed clears the physical button used for logical primary so the click is not processed again.
func (ep *Scope) clearMousePrimaryConsumed(m *platform.MouseState) {
	if m == nil {
		return
	}
	if ep.currentPrefs().UseRightClick {
		m.Clicked[platform.MouseButtonSecondary] = false
	} else {
		m.Clicked[platform.MouseButtonPrimary] = false
	}
}

// clearMouseTertiaryConsumed clears the physical button used for logical tertiary.
func (ep *Scope) clearMouseTertiaryConsumed(m *platform.MouseState) {
	if m == nil {
		return
	}
	if ep.currentPrefs().UseRightClick {
		m.Clicked[platform.MouseButtonPrimary] = false
	} else {
		m.Clicked[platform.MouseButtonTertiary] = false
	}
}

// consumeMouseClick clears BOTH primary and tertiary consumed flags. Most
// View click handlers want this: the click should not fall through to any
// later handler whether it was logically primary or tertiary.
func (ep *Scope) consumeMouseClick(m *platform.MouseState) {
	ep.clearMousePrimaryConsumed(m)
	ep.clearMouseTertiaryConsumed(m)
}

func (ep *Scope) consumeMouseEvents(ctx *scope.Context, transforms scope.Transformations) {
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	ps := ep.currentPrefs()
	if ep.mousePrimaryReleased(mouse) {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			mouseLatLong := transforms.LatLongFromWindowP(mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))

		}
	}
	if ep.mouseTertiaryReleased(mouse) {
		// A middle-click is treated as "left-click + [Enter] at this position"; append the click to
		// the input and execute.
		pos := transforms.LatLongFromWindowP(mouse.Pos)
		var callsign av.ADSBCallsign
		if trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms); trk != nil {
			callsign = trk.ADSBCallsign
		}
		// Skip an empty middle-click on empty space: nothing to dispatch.
		if callsign != "" || ep.input.String() != "" {
			ep.input.AddLocation(pos, callsign)
			status, err := ep.executeERAMCommand(ctx, ep.input)
			ep.input.Clear()
			ep.applyCommandStatus(ctx, status, err)
		}
	}

	// Pan with a drag of the secondary button.
	if mouse.Dragging[platform.MouseButtonSecondary] {
		delta := mouse.DragDelta
		if delta[0] != 0 || delta[1] != 0 {
			deltaLL := transforms.LatLongFromWindowV(delta)
			ps.CurrentCenter = math.Sub2f(ps.CurrentCenter, deltaLL)
		}
	}

	if ep.mousePrimaryClicked(mouse) {
		if ep.drawingRoute {
			ep.setDrawRoutePoints(ctx, append(ep.drawRoutePoints, transforms.LatLongFromWindowP(mouse.Pos)))
		} else if ep.input.String() != "" {
			pos := transforms.LatLongFromWindowP(mouse.Pos)
			var callsign av.ADSBCallsign
			if trk, _ := ep.tryGetClosestTrack(ctx, mouse.Pos, transforms); trk != nil {
				callsign = trk.ADSBCallsign
			}
			ep.input.AddLocation(pos, callsign)
		}
	}

	// zoom
	if z := mouse.Wheel[1]; z != 0 {
		r := ps.Range
		ps.Range += func() float32 {
			var amt float32 = 20

			if r < 4 {
				amt = .5
			} else if r == 4 {
				if z > 0 {
					amt = 2
				} else {
					amt = .5
				}
			} else if r < 20 {
				amt = 2
			} else if r == 20 {
				if z > 0 {
					amt = 20
				} else {
					amt = 2
				}
			}
			return amt * z / 10
		}()
		ps.Range = math.Clamp(ps.Range, .5, 2600)

		// We want to zoom in centered at the mouse position; this affects
		// the scope center after the zoom, so we'll find the
		// transformation that gives the new center position.
		mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
		scale := ps.Range / r
		centerTransform := math.Identity3x3().
			Translate(mouseLL[0], mouseLL[1]).
			Scale(scale, scale).
			Translate(-mouseLL[0], -mouseLL[1])

		ps.CurrentCenter = centerTransform.TransformPoint(ps.CurrentCenter)
	}
}

///////////////////////////////////////////////////////////////////////////
// MCA - Message Composition Area

// drawMessageCompositionArea renders the MCA: the preview area, where
// commands are typed, above the feedback area. The preview area starts at
// two lines and grows with the input up to ps.MCA.PALines lines; the
// feedback area starts at four lines and grows to fit its message (4.3.1.1,
// 4.3.1.2). Both areas share black bg and a white border; the seam between
// them is drawn by View as part of the outer border (and a separator line in
// the body). Width fits ps.MCA.Width characters of the selected font plus
// 2px side padding.
func (ep *Scope) drawMessageCompositionArea(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	font := ep.ERAMFont(ps.MCA.Font)
	cols := ps.MCA.Width
	width := float32(cols)*charWidth(font) + 4
	lineSpacing := int(float32(font.Size) * 0.4)
	lineH := font.LayoutBounds("0", lineSpacing).Height()

	// Input longer than the preview area shows its last lines, where the
	// cursor is.
	inText, _ := util.WrapText(ep.input.String()+"_", cols, 0, true, true)
	inLines := strings.Split(inText, "\n")
	inLines = inLines[max(0, len(inLines)-ps.MCA.PALines):]
	inText = strings.Join(inLines, "\n")
	inputH := float32(max(2, len(inLines)))*lineH + 4

	// Feedback rendering: icon (success/error) in its own color, then the
	// message in default text color. The wrap input includes the icon so the
	// available width on the first line accounts for it.
	var feedbackIcon, feedbackRest string
	var feedbackIconColor renderer.RGB
	feedbackLines := 0
	if ep.feedbackArea.kind != feedbackNone {
		switch ep.feedbackArea.kind {
		case feedbackSuccess:
			feedbackIcon, feedbackIconColor = checkMark, colors.successGreen
		case feedbackError:
			feedbackIcon, feedbackIconColor = xMark, colors.errorRed
		}
		wrapped, _ := util.WrapText(feedbackIcon+" "+ep.feedbackArea.msg, cols, 0, true, true)
		// The feedback area holds up to 30 lines 50 characters wide or 34
		// lines 30 characters wide.
		lines := strings.Split(wrapped, "\n")
		lines = lines[:min(len(lines), util.Select(cols >= 50, 30, 34))]
		feedbackLines = len(lines)
		wrapped = strings.Join(lines, "\n")
		// wrapped starts with the icon char; the rest starts with the space
		// that pushes the message past the icon's column on the first line.
		feedbackRest = wrapped[len(feedbackIcon):]
	}
	feedbackH := float32(max(4, feedbackLines))*lineH + 4

	// ps.MCA.Position is the top-left of the feedback box (prefs
	// semantics). View sees the top-left of the whole envelope (input top).
	viewPos := [2]float32{ps.MCA.Position[0], ps.MCA.Position[1] + inputH}

	v := View{
		Position:   &viewPos,
		ID:         "mca",
		Width:      width,
		BodyHeight: inputH + feedbackH,
		Opaque:     true,
		ShowBorder: true,
		Brightness: ps.Brightness.Border,
		Menu:       mcaMenu{},
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			// body.P1 = top-right (top of input); body.P0 = bottom-left (bottom of feedback).
			seamY := body.P1[1] - inputH

			// Seam line between input and feedback.
			borderColor := ps.Brightness.Border.ScaleRGB(colors.view.border)
			b.Ld.AddLine([2]float32{body.P0[0], seamY}, [2]float32{body.P1[0], seamY}, borderColor)

			inputTopLeft := [2]float32{body.P0[0], body.P1[1]}
			feedbackTopLeft := [2]float32{body.P0[0], seamY}
			textStyle := renderer.TextStyle{Font: font, Color: ps.MCA.Bright.ScaleRGB(colors.toolbar.text), LineSpacing: lineSpacing}

			// Input text (top box).
			b.Td.AddText(inText, [2]float32{inputTopLeft[0] + 2, inputTopLeft[1] - 2}, textStyle)

			// Feedback text (bottom box). Two AddText calls share the same
			// origin so newline resets in `feedbackRest` land at column 0.
			loc := [2]float32{feedbackTopLeft[0] + 2, feedbackTopLeft[1] - 2}
			iconStyle := renderer.TextStyle{Font: font, Color: ps.MCA.Bright.ScaleRGB(feedbackIconColor), LineSpacing: lineSpacing}
			b.Td.AddText(feedbackIcon, loc, iconStyle)
			b.Td.AddText(feedbackRest, loc, textStyle)
		},
	}
	ep.DrawView(ctx, transforms, cb, v)

	// View may have updated viewPos via drag; translate back to prefs semantics.
	ps.MCA.Position = [2]float32{viewPos[0], viewPos[1] - inputH}
}

// mcaMenu is the Message Composition Area's menu.
type mcaMenu struct{}

func (mcaMenu) title() string { return "MCA" }

func (mcaMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	return []MenuItem{
		makeIntMenuItem(ep, &ps.MCA.PALines, "PA LINES", 2, 50, 1),
		makeWidthMenuItem(ep, &ps.MCA.Width, 30, 50),
		makeIntMenuItem(ep, &ps.MCA.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.MCA.Bright, "BRIGHT", 0, 100, 2),
	}
}

///////////////////////////////////////////////////////////////////////////
// RA: Response Area

// nonADSBMark stands for the non-ADS-B indicator in the RA's text; the RA
// draws it as a coral A (4.3.2).
const nonADSBMark = "\x01"

// drawResponseArea renders the RA, which shows one response at a time. It
// is four lines tall and grows with the response to ten lines, past which
// it scrolls; responses are cut off after 300 lines (4.3.2). Width fits
// ps.RA.Width characters of the selected font plus 2px side padding.
func (ep *Scope) drawResponseArea(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	font := ep.ERAMFont(ps.RA.Font)
	cols := ps.RA.Width
	width := float32(cols)*charWidth(font) + 4
	lineSpacing := int(float32(font.Size) * 0.4)
	lineH := font.LayoutBounds("0", lineSpacing).Height()

	wrapped, _ := util.WrapText(ep.responseArea, cols, 0, true, false)
	lines := strings.Split(wrapped, "\n")
	lines = lines[:min(len(lines), 300)]
	visible := math.Clamp(len(lines), 4, 10)

	if ep.raScrolledText != ep.responseArea {
		ep.raScroll, ep.raScrolledText = ViewScrollState{}, ep.responseArea
	}
	maxOffset := max(0, len(lines)-visible)
	ep.raScroll.Offset = math.Clamp(ep.raScroll.Offset, 0, maxOffset)
	shownLines := lines[ep.raScroll.Offset:min(len(lines), ep.raScroll.Offset+visible)]
	shown := strings.ReplaceAll(strings.Join(shownLines, "\n"), nonADSBMark, " ")
	textColor := ps.RA.Bright.ScaleRGB(colors.toolbar.text)
	markStyle := renderer.TextStyle{Font: font, Color: ps.Brightness.NonADSB.ScaleRGB(colors.coral)}

	v := View{
		Position:   &ps.RA.Position,
		ID:         "ra",
		Width:      width,
		BodyHeight: float32(visible)*lineH + 4,
		BodyFont:   font,
		Opaque:     true,
		ShowBorder: true,
		Brightness: ps.RA.Bright,
		Menu:       raMenu{},
		DrawBody: func(body math.Extent2D, b *ViewBuilders) {
			topLeft := [2]float32{body.P0[0] + 2, body.P1[1] - 2}
			b.Td.AddText(shown, topLeft,
				renderer.TextStyle{Font: font, Color: textColor, LineSpacing: lineSpacing})
			for i, line := range shownLines {
				if j := strings.Index(line, nonADSBMark); j >= 0 {
					b.Td.AddText("A", [2]float32{topLeft[0] + float32(j)*charWidth(font), topLeft[1] - float32(i)*lineH},
						markStyle)
				}
			}
		},
	}
	if maxOffset > 0 {
		v.Scroll = &ViewScrollConfig{State: &ep.raScroll, MaxOffset: maxOffset}
	}
	ep.DrawView(ctx, transforms, cb, v)
}

// raMenu is the Response Area's menu.
type raMenu struct{}

func (raMenu) title() string { return "RA" }

func (raMenu) rows(ep *Scope, _ *scope.Context) []MenuItem {
	ps := ep.currentPrefs()
	return []MenuItem{
		makeWidthMenuItem(ep, &ps.RA.Width, 25, 50),
		makeIntMenuItem(ep, &ps.RA.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.RA.Bright, "BRIGHT", 0, 100, 2),
		makeActionMenuItem("CLEAR", func(MenuClickType) { ep.responseArea = "" }),
	}
}
