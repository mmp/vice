// stars/commands.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// Cache the routes we show when paused but periodically fetch them
var pausedAircraftInfo *util.TransientMap[av.ADSBCallsign, string] = util.NewTransientMap[av.ADSBCallsign, string]()

type CommandMode int

const (
	// Keyboard command entry modes; can be main or DCB menu for these; sp.dcbShowAux decides.
	CommandModeNone CommandMode = iota
	CommandModeInitiateControl
	CommandModeTrackReposition
	CommandModeTrackSuspend
	CommandModeTerminateControl
	CommandModeHandOff
	CommandModeVFRPlan
	CommandModeMultiFunc
	CommandModeFlightData
	CommandModeCollisionAlert
	CommandModeMin
	CommandModeTargetGen
	CommandModeTargetGenLock
	CommandModeReleaseDeparture
	CommandModeRestrictionArea
	CommandModeDrawRoute
	CommandModeDrawWind

	// These correspond to buttons on the main DCB menu.
	CommandModeRange
	CommandModePlaceCenter
	CommandModeRangeRings
	CommandModePlaceRangeRings
	CommandModeMaps
	CommandModeWX
	CommandModeBrite
	CommandModeBriteSpinner
	CommandModeLDR
	CommandModeLDRDir
	CommandModeCharSize
	CommandModeCharSizeSpinner
	CommandModeSite
	CommandModePref
	CommandModeSavePrefAs
	CommandModeSSAFilter
	CommandModeGITextFilter

	// These correspond to buttons on the secondary DCB menu.
	CommandModeVolume
	CommandModeHistory
	CommandModeHistoryRate
	CommandModePTLLength
	CommandModeDwell
	CommandModeTPA
)

func (c CommandMode) PreviewString(sp *STARSPane) string {
	switch c {
	case CommandModeNone:
		return ""
	case CommandModeInitiateControl:
		return "IC"
	case CommandModeTrackReposition:
		return "RP"
	case CommandModeTrackSuspend:
		return "SU"
	case CommandModeTerminateControl:
		return "TC"
	case CommandModeHandOff:
		return "HD"
	case CommandModeVFRPlan:
		return "VP"
	case CommandModeMultiFunc:
		return "F"
	case CommandModeFlightData:
		return "DA"
	case CommandModeCollisionAlert:
		return "CA"
	case CommandModeMin:
		return "MIN"
	case CommandModeTargetGen:
		return "TG"
	case CommandModeTargetGenLock:
		return "TG LOCK"
	case CommandModeReleaseDeparture:
		return "RD"
	case CommandModeRestrictionArea:
		return "AR"
	case CommandModeDrawRoute:
		return "DRAWROUTE"
	case CommandModeDrawWind:
		if sp.atmosGrid != nil {
			return "WIND " + strconv.Itoa(int(sp.atmosGrid.AltitudeForIndex(sp.windDrawAltitudeIndex)))
		} else {
			return "WIND"
		}
	case CommandModeRange:
		return "RANGE"
	case CommandModePlaceCenter:
		return "CNTR"
	case CommandModeRangeRings:
		return "RR"
	case CommandModePlaceRangeRings:
		return "PLC RR"
	case CommandModeMaps:
		return "MAP"
	case CommandModeWX:
		return "WX"
	case CommandModeBrite:
		return ""
	case CommandModeBriteSpinner:
		return "BRT"
	case CommandModeLDR:
		return "LLL"
	case CommandModeLDRDir:
		return "LDR"
	case CommandModeCharSize:
		return ""
	case CommandModeCharSizeSpinner:
		return "CHAR"
	case CommandModeSite:
		return "SITE"
	case CommandModePref:
		return "PREF"
	case CommandModeSavePrefAs:
		return "PREF SET NAME"
	case CommandModeSSAFilter:
		return ""
	case CommandModeGITextFilter:
		return ""
	case CommandModeVolume:
		return "VOL"
	case CommandModeHistory:
		return "HIST"
	case CommandModeHistoryRate:
		return "HRATE"
	case CommandModePTLLength:
		return "PTL"
	case CommandModeDwell:
		return "DWELL"
	case CommandModeTPA:
		return ""
	default:
		panic("unhandled command mode")
	}
}

// CommandClear specifies how command state should be cleared after execution.
type CommandClear int

const (
	ClearAll   CommandClear = iota // Clear all command state (default zero value)
	ClearInput                     // Clear user text input but stay in same command mode
	ClearNone                      // Don't clear any command state
)

// CommandStatus is returned by command handlers to indicate behavior.
// The zero value means: clear command state, no output.
type CommandStatus struct {
	Clear  CommandClear // how to clear command state after execution
	Output string       // text to display in preview area

	// CommandHandlers registers transient handlers that intercept subsequent
	// input (keyboard Enter or scope click). Uses the same userCommand type
	// as registerCommand, with cmd specifying the command spec (e.g., "",
	// "[NUM]", "[POS]") and handlerFunc the handler function. Handlers are tried in
	// order; first matching handler wins.
	CommandHandlers []userCommand
}

func (sp *STARSPane) processKeyboardInput(ctx *panes.Context) {
	if !ctx.HaveFocus || ctx.Keyboard == nil {
		return
	}

	input := strings.ToUpper(ctx.Keyboard.Input)
	if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "" && len(input) > 0 {
		sp.multiFuncPrefix = string(input[0])
		input = input[1:]
	}

	if len(input) > 0 && input[0] == sp.TgtGenKey { // [TGT GEN]
		sp.setCommandMode(ctx, CommandModeTargetGen)
		if !ctx.TCWIsPrivileged(ctx.UserTCW) {
			ctx.Client.HoldRadioTransmissions()
		}
		input = input[1:]
	}

	if sp.commandMode == CommandModeTargetGen || sp.commandMode == CommandModeTargetGenLock {
		if !ctx.TCWIsPrivileged(ctx.UserTCW) && input != "" {
			// As long as text is being entered, hold radio transmissions
			// for the coming few seconds.
			ctx.Client.HoldRadioTransmissions()
		}
	}

	// Enforce the 32-character-per-line limit
	if lines := strings.Fields(sp.previewAreaInput); len(lines) > 0 {
		if len(lines[len(lines)-1]) > 32 {
			lines[len(lines)-1] = lines[len(lines)-1][:32] // chop to 32 characters
			sp.previewAreaInput = strings.Join(lines, " ")
			sp.displayError(ErrSTARSCapacity, ctx, "")
			return
		}
	}

	sp.previewAreaInput += strings.ReplaceAll(input, "`", STARSTriangleCharacter)

	ps := sp.currentPrefs()

	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if len(sp.previewAreaInput) > 0 {
				// We need to be careful to deal with UTF8 for the triangle...
				r := []rune(sp.previewAreaInput)
				sp.previewAreaInput = string(r[:len(r)-1])
			} else {
				sp.multiFuncPrefix = ""
			}
			if n := len(sp.drawRoutePoints); n > 0 {
				sp.drawRoutePoints = sp.drawRoutePoints[:n-1]
			}

		case imgui.KeyEnd:
			sp.setCommandMode(ctx, CommandModeMin)

		case imgui.KeyEnter:
			var status CommandStatus
			var err error

			// Check if transient command handlers should intercept this input
			if len(sp.transientCommandHandlers) > 0 {
				status, err = sp.executeTransientCommandHandlers(ctx, sp.previewAreaInput,
					nil, false, [2]float32{}, nil, radar.ScopeTransformations{})
			} else {
				status, err = sp.executeSTARSCommand(ctx, sp.previewAreaInput)
			}

			if err != nil {
				sp.displayError(err, ctx, "")
			} else {
				// Install any new command handlers from the command result
				if len(status.CommandHandlers) > 0 {
					sp.transientCommandHandlers = status.CommandHandlers
				}

				switch status.Clear {
				case ClearAll:
					if sp.commandMode != CommandModeTargetGenLock {
						sp.setCommandMode(ctx, CommandModeNone)
						sp.maybeAutoHomeCursor(ctx)
					} else {
						sp.previewAreaInput = ""
					}
				case ClearInput:
					sp.previewAreaInput = ""
				case ClearNone:
					// Don't clear anything
				}
				sp.previewAreaOutput = status.Output
			}

		case imgui.KeyEscape:
			sp.movingList = ""
			if sp.activeSpinner != nil {
				sp.setCommandMode(ctx, sp.activeSpinner.ModeAfter())
			} else {
				sp.setCommandMode(ctx, CommandModeNone)
			}

		case imgui.KeyF1:
			if ctx.Keyboard.KeyControl() {
				// Beaconator; handled elsewhere
			} else if ctx.Keyboard.KeyShift() { // F13
				sp.setCommandMode(ctx, CommandModeReleaseDeparture)
			} else { // INIT CNTL
				sp.setCommandMode(ctx, CommandModeInitiateControl)
			}

		case imgui.KeyF2:
			if ctx.Keyboard.KeyControl() { // CNTR
				ps.UseUserCenter = false
			} else { // TRK RPOS
				sp.setCommandMode(ctx, CommandModeTrackReposition)
			}

		case imgui.KeyF3:
			if ctx.Keyboard.KeyControl() { // MAPS
				sp.setCommandMode(ctx, CommandModeMaps)
			} else { // TRK SUSP
				sp.setCommandMode(ctx, CommandModeTrackSuspend)
			}

		case imgui.KeyF4:
			if ctx.Keyboard.KeyControl() {
				sp.setCommandMode(ctx, CommandModeWX)
			} else {
				sp.setCommandMode(ctx, CommandModeTerminateControl)
			}

		case imgui.KeyF5:
			if ctx.Keyboard.KeyControl() {
				sp.setCommandMode(ctx, CommandModeBrite)
			} else {
				sp.setCommandMode(ctx, CommandModeHandOff)
			}

		case imgui.KeyF6:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeLDR)
			} else {
				sp.setCommandMode(ctx, CommandModeFlightData)
			}

		case imgui.KeyF7:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeCharSize)
			} else {
				sp.setCommandMode(ctx, CommandModeMultiFunc)
			}

		case imgui.KeyF8:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.dcbShowAux = !sp.dcbShowAux
			} else {
				// TODO: FMA alerts / TSAS
				sp.setCommandMode(ctx, CommandModeNone)
			}

		case imgui.KeyF9:
			if ctx.Keyboard.KeyControl() {
				sp.resetInputState(ctx)
				ps.DisplayDCB = !ps.DisplayDCB
			} else {
				sp.setCommandMode(ctx, CommandModeVFRPlan)
			}

		case imgui.KeyF10:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRangeRings)
			} else {
				// TODO: Printout/save interfacility data transfer messages (IFDT)
				sp.setCommandMode(ctx, CommandModeNone)
			}

		case imgui.KeyF11:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRange)
			} else {
				sp.setCommandMode(ctx, CommandModeCollisionAlert)
			}

		case imgui.KeyF12:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModePref)
			} else {
				sp.setCommandMode(ctx, CommandModeRestrictionArea)
			}

		case imgui.KeyF13:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeSite)
			} else {
				sp.setCommandMode(ctx, CommandModeReleaseDeparture)
			}

		case imgui.KeyF14:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeSite)
			}

		case imgui.KeyTab:
			if imgui.IsKeyDown(imgui.KeyLeftShift) { // Check if LeftShift is pressed
				sp.setCommandMode(ctx, CommandModeTargetGenLock)
			} else {
				sp.setCommandMode(ctx, CommandModeTargetGen)
			}

		case imgui.KeyUpArrow:
			if sp.commandMode == CommandModeDrawWind && sp.atmosGrid != nil {
				sp.windDrawAltitudeIndex++
				sp.windDrawAltitudeIndex = min(sp.windDrawAltitudeIndex, sp.atmosGrid.Res[2]-1)
			}

		case imgui.KeyDownArrow:
			if sp.commandMode == CommandModeDrawWind && sp.atmosGrid != nil {
				sp.windDrawAltitudeIndex--
				sp.windDrawAltitudeIndex = max(sp.windDrawAltitudeIndex, sp.minWindDrawAltitudeIndex(ctx))
			}
		}
	}
}

func (sp *STARSPane) minWindDrawAltitudeIndex(ctx *panes.Context) int {
	if sp.atmosGrid != nil {
		elev := av.DB.Airports[ctx.Client.State.PrimaryAirport].Elevation
		for i := range sp.atmosGrid.Res[2] - 1 {
			if sp.atmosGrid.AltitudeForIndex(i) >= float32(elev) {
				return i
			}
		}
	}
	return 0
}

func (sp *STARSPane) executeSTARSCommand(ctx *panes.Context, cmd string) (CommandStatus, error) {
	// If there's an active spinner, it gets keyboard input; we thus won't
	// worry about the corresponding CommandModes in the following.
	if sp.activeSpinner != nil {
		mode, err := sp.activeSpinner.KeyboardInput(cmd)
		if err != nil {
			return CommandStatus{}, err
		}
		// Clear the input area, and disable the spinner's mouse
		// capture, and switch to the indicated command mode.
		sp.setCommandMode(ctx, mode)
		return CommandStatus{}, nil
	}

	if status, err, ok := sp.tryExecuteUserCommand(ctx, cmd, nil, false, [2]float32{}, radar.ScopeTransformations{}, nil, nil); ok {
		return status, err
	}
	return CommandStatus{}, ErrSTARSCommandFormat
}

func (sp *STARSPane) tgtGenDefaultCallsign(ctx *panes.Context) av.ADSBCallsign {
	if cs := ctx.Client.LastTTSCallsign(); cs != "" {
		// If TTS is active, return the last TTS transmitter.
		return cs
	}
	// Otherwise the most recent one the user selected.
	return sp.targetGenLastCallsign
}

func (sp *STARSPane) runAircraftCommands(ctx *panes.Context, callsign av.ADSBCallsign, cmds string, multiple, clickedTrack bool) {
	sp.targetGenLastCallsign = callsign
	prevMode := sp.commandMode

	ctx.Client.RunAircraftCommands(callsign, cmds, multiple, clickedTrack,
		func(errStr string, remaining string) {
			if errStr != "" {
				sp.commandMode = prevMode // CommandModeTargetGen or TargetGenLock
				sp.previewAreaInput = remaining
				if err := server.TryDecodeErrorString(errStr); err != nil {
					err = GetSTARSError(err, ctx.Lg)
					sp.displayError(err, ctx, "")
				} else {
					sp.displayError(ErrSTARSCommandFormat, ctx, "")
				}
			}
		})
}

func (sp *STARSPane) autoReleaseDepartures(ctx *panes.Context) {
	if sp.ReleaseRequests == nil {
		sp.ReleaseRequests = make(map[av.ADSBCallsign]interface{})
	}

	ps := sp.currentPrefs()
	releaseAircraft := ctx.Client.State.GetSTARSReleaseDepartures()

	fa := ctx.FacilityAdaptation
	for _, list := range fa.CoordinationLists {
		// Get the aircraft that should be included in this list.
		deps := util.FilterSlice(releaseAircraft,
			func(dep sim.ReleaseDeparture) bool {
				return slices.Contains(list.Airports, dep.DepartureAirport)
			})

		cl, ok := ps.CoordinationLists[list.Id]
		if !ok {
			// This shouldn't happen, but...
			continue
		}

		for _, dep := range deps {
			if _, ok := sp.ReleaseRequests[dep.ADSBCallsign]; !ok {
				// Haven't seen this one before
				if cl.AutoRelease {
					ctx.Client.ReleaseDeparture(dep.ADSBCallsign,
						func(err error) { ctx.Lg.Errorf("%s: %v", dep.ADSBCallsign, err) })
				}
				// Note that we've seen it, whether or not it was auto-released.
				sp.ReleaseRequests[dep.ADSBCallsign] = nil
			}
		}
	}

	// Clean up release requests for aircraft that have departed and aren't
	// on the hold for release list.
	for callsign := range sp.ReleaseRequests {
		if !slices.ContainsFunc(releaseAircraft,
			func(dep sim.ReleaseDeparture) bool { return dep.ADSBCallsign == callsign }) {
			delete(sp.ReleaseRequests, callsign)
		}
	}
}

func (sp *STARSPane) executeSTARSClickedCommand(ctx *panes.Context, cmd string, mousePosition [2]float32,
	ghosts []*av.GhostTrack, transforms radar.ScopeTransformations) (CommandStatus, error) {
	trk, clickedGhost := sp.findClickedTrackAndGhost(ctx, mousePosition, transforms, ghosts)

	if status, err, ok := sp.tryExecuteUserCommand(ctx, cmd, trk, true, mousePosition, transforms, clickedGhost, ghosts); ok {
		return status, err
	} else if cmd != "" {
		return CommandStatus{}, ErrSTARSCommandFormat
	}
	return CommandStatus{}, nil
}

// executeTransientCommandHandlers processes transient command handlers for either
// keyboard input (Enter press) or scope click.
func (sp *STARSPane) executeTransientCommandHandlers(ctx *panes.Context, text string,
	clickedTrack *sim.Track, hasClick bool, mousePosition [2]float32,
	ghosts []*av.GhostTrack, transforms radar.ScopeTransformations) (CommandStatus, error) {

	// For click input, find what was clicked
	var trk *sim.Track
	var clickedGhost *av.GhostTrack
	if hasClick {
		trk, clickedGhost = sp.findClickedTrackAndGhost(ctx, mousePosition, transforms, ghosts)
	}
	if clickedTrack != nil {
		trk = clickedTrack
	}

	input := &CommandInput{
		text:          text,
		clickedTrack:  trk,
		hasClick:      hasClick,
		mousePosition: mousePosition,
		transforms:    transforms,
		clickedGhost:  clickedGhost,
		ghosts:        ghosts,
	}

	fmt.Printf("\n[TRANSIENT] Executing transient command handlers, %d handlers, text=%q, trk=%v, hasClick=%v\n",
		len(sp.transientCommandHandlers), text, trk != nil, hasClick)

	status, err, handled := sp.dispatchCommand(ctx, sp.transientCommandHandlers, input)
	if !handled {
		return CommandStatus{}, ErrSTARSCommandFormat
	}
	return status, err
}

func (sp *STARSPane) consumeMouseEvents(ctx *panes.Context, ghosts []*av.GhostTrack, transforms radar.ScopeTransformations,
	cb *renderer.CommandBuffer) {
	if ctx.Mouse == nil {
		return
	}

	mouse := ctx.Mouse
	ps := sp.currentPrefs()

	if (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonSecondary] ||
		ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
		ctx.KeyboardFocus.Take(sp)
	}

	if sp.activeSpinner == nil && !sp.LockDisplay {
		// Handle dragging the scope center
		if mouse.Dragging[platform.MouseButtonSecondary] || sp.commandMode == CommandModePlaceCenter {
			delta := mouse.DragDelta
			if delta[0] != 0 || delta[1] != 0 {
				deltaLL := transforms.LatLongFromWindowV(delta)
				ps.UserCenter = math.Sub2f(ps.UserCenter, deltaLL)
				ps.UseUserCenter = true
			}
		}

		// Consume mouse wheel
		if mouse.Wheel[1] != 0 {
			r := ps.Range
			ps.Range += func() float32 {
				if ctx.Keyboard != nil {
					if ctx.Keyboard.KeyControl() {
						return 3 * mouse.Wheel[1]
					}
				}
				return mouse.Wheel[1]
			}()
			ps.Range = math.Clamp(ps.Range, 6, 256) // 4-33

			// We want to zoom in centered at the mouse position; this affects
			// the scope center after the zoom, so we'll find the
			// transformation that gives the new center position.
			mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
			scale := ps.Range / r
			centerTransform := math.Identity3x3().
				Translate(mouseLL[0], mouseLL[1]).
				Scale(scale, scale).
				Translate(-mouseLL[0], -mouseLL[1])

			ps.UserCenter = centerTransform.TransformPoint(ps.UserCenter)
			ps.UseUserCenter = true
		}
	}

	if ctx.Mouse.Released[platform.MouseButtonPrimary] {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
			mouseLatLong := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		if ctx.Keyboard != nil && ctx.Keyboard.KeyControl() {
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil {
				if state := sp.TrackState[trk.ADSBCallsign]; state != nil {
					state.IsSelected = !state.IsSelected
					return
				}
			}
		}

		// If transient command handlers have been registered, give them the click.
		var status CommandStatus
		var err error
		fmt.Printf("\n[MOUSE] Click, transientCommandHandlers=%d\n", len(sp.transientCommandHandlers))
		if len(sp.transientCommandHandlers) > 0 {
			status, err = sp.executeTransientCommandHandlers(ctx, sp.previewAreaInput, nil, true, ctx.Mouse.Pos, ghosts, transforms)
		} else {
			status, err = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.Mouse.Pos, ghosts, transforms)
		}
		fmt.Printf("[MOUSE] status.Clear=%v, status.CommandHandlers=%d, err=%v\n",
			status.Clear, len(status.CommandHandlers), err)

		if err != nil {
			sp.displayError(err, ctx, "")
		} else {
			// Install any new command handlers from the command result
			if len(status.CommandHandlers) > 0 {
				sp.transientCommandHandlers = status.CommandHandlers
			}

			switch status.Clear {
			case ClearAll:
				if sp.commandMode != CommandModeTargetGenLock {
					sp.resetInputState(ctx)
				} else {
					sp.previewAreaInput = ""
				}
			case ClearInput:
				sp.previewAreaInput = ""
			case ClearNone:
				// Don't clear anything
			}
			sp.maybeAutoHomeCursor(ctx)
			sp.previewAreaOutput = status.Output
		}
	}

	if ctx.Mouse.Released[platform.MouseButtonTertiary] {
		// 6.20 Toggle track highlight (implied)
		// TODO? ILL FNCT if in p/o or h/o to us, ILL TRK if it's suspended
		if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil {
			if state := sp.TrackState[trk.ADSBCallsign]; state != nil {
				state.IsSelected = !state.IsSelected
			}
		}
	}

	if ctx.Mouse.Down[platform.MouseButtonTertiary] {
		// 4.9.28: Show list frames when middle button held and not over a track
		trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms)
		sp.showListFrames = trk == nil
	} else {
		sp.showListFrames = false
	}

	if !ctx.Client.State.Paused {
		switch sp.currentPrefs().DwellMode {
		case DwellModeOff:
			sp.dwellAircraft = ""

		case DwellModeOn:
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil {
				sp.dwellAircraft = trk.ADSBCallsign
			} else {
				sp.dwellAircraft = ""
			}

		case DwellModeLock:
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil {
				sp.dwellAircraft = trk.ADSBCallsign
			}
			// Otherwise leave sp.dwellAircraft as is
		}
	} else {
		if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil && !trk.IsUnsupportedDB() {
			td := renderer.GetTextDrawBuilder()
			defer renderer.ReturnTextDrawBuilder(td)

			ps := sp.currentPrefs()
			font := sp.systemFont(ctx, ps.CharSize.Datablocks)
			style := renderer.TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: 0}

			// Track position in window coordinates
			state := sp.TrackState[trk.ADSBCallsign]
			pac := transforms.WindowFromLatLongP(state.track.Location)

			// Upper-left corner of where we start drawing the text
			pad := float32(5)
			ptext := math.Add2f([2]float32{2 * pad, 0}, pac)
			info := ""
			var ok bool
			if info, ok = pausedAircraftInfo.Get(trk.ADSBCallsign); !ok {
				if ads, err := ctx.Client.GetAircraftDisplayState(trk.ADSBCallsign); err != nil {
					ctx.Lg.Errorf("%s: error fetching display state: %s", trk.ADSBCallsign, err)
				} else {
					info = ads.FlightState
					pausedAircraftInfo.Add(trk.ADSBCallsign, info, 2*time.Second)
				}
			}
			td.AddText(info, ptext, style)

			// Draw an alpha-blended quad behind the text to make it more legible.
			trid := renderer.GetTrianglesDrawBuilder()
			defer renderer.ReturnTrianglesDrawBuilder(trid)
			bx, by := font.BoundText(info, style.LineSpacing)
			trid.AddQuad(math.Add2f(ptext, [2]float32{-pad, 0}),
				math.Add2f(ptext, [2]float32{float32(bx) + pad, 0}),
				math.Add2f(ptext, [2]float32{float32(bx) + pad, -float32(by) - pad}),
				math.Add2f(ptext, [2]float32{-pad, -float32(by) - pad}))

			// Get it all into the command buffer
			transforms.LoadWindowViewingMatrices(cb)
			cb.SetRGBA(renderer.RGBA{R: 0.25, G: 0.25, B: 0.25, A: 0.75})
			cb.Blend()
			trid.GenerateCommands(cb)
			cb.DisableBlend()
			td.GenerateCommands(cb)
		}
	}
}

func (sp *STARSPane) setCommandMode(ctx *panes.Context, mode CommandMode) {
	sp.resetInputState(ctx)
	sp.commandMode = mode

	if mode == CommandModeTargetGen || sp.commandMode == CommandModeTargetGenLock {
		ctx.Client.HoldRadioTransmissions()
	} else {
		ctx.Client.AllowRadioTransmissions()
	}
}

func (sp *STARSPane) resetInputState(ctx *panes.Context) {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.wipRBL = nil
	sp.wipRestrictionArea = nil

	sp.transientCommandHandlers = nil
	sp.activeSpinner = nil

	sp.drawRoutePoints = nil

	ctx.Client.AllowRadioTransmissions()

	ctx.Platform.EndCaptureMouse()
	ctx.Platform.StopMouseDeltaMode()
}

// installCommandHandlers sets transient command handlers for the next keyboard Enter or scope click.
// This is used by DCB code that needs to set handlers directly (not via CommandStatus).
func (sp *STARSPane) installCommandHandlers(handlers []userCommand) {
	sp.transientCommandHandlers = handlers
}

func (sp *STARSPane) displayError(err error, ctx *panes.Context, acid sim.ACID) {
	if err != nil { // it should be, but...
		sp.playOnce(ctx.Platform, AudioCommandError)
		sp.previewAreaOutput = GetSTARSError(err, ctx.Lg).Error()

		if err == ErrSTARSDuplicateACID {
			sp.previewAreaOutput += " " + string(acid)
			if trk, ok := ctx.Client.State.GetTrackByACID(acid); ok && trk.IsAssociated() {
				sp.previewAreaOutput += "\nFLIGHT ACTIVE AT " + string(trk.FlightPlan.TrackingController)
			} else if idx := slices.IndexFunc(ctx.Client.State.UnassociatedFlightPlans,
				func(fp *sim.NASFlightPlan) bool {
					return fp.ACID == acid
				}); idx != -1 {
				fp := ctx.Client.State.UnassociatedFlightPlans[idx]
				if fp.TrackingController != "" {
					sp.previewAreaOutput += "\nFLIGHT INACTIVE AT " + string(fp.TrackingController)
				}
			}
		}
	}
}

func (sp *STARSPane) maybeAutoHomeCursor(ctx *panes.Context) {
	ps := sp.currentPrefs()
	if ps.AutoCursorHome {
		sp.hideMouseCursor = true

		if ps.CursorHome[0] == 0 && ps.CursorHome[1] == 0 {
			c := ctx.PaneExtent.Center()
			// Make sure we have integer coordinates so we don't spuriously
			// mismatch the mouse position and instantly unhide.
			ps.CursorHome = [2]float32{math.Floor(c[0]), math.Floor(c[1])}
		}

		ctx.SetMousePosition(ps.CursorHome)
	}
}

// returns the controller responsible for the aircraft given its altitude
// and route.
func calculateAirspace(ctx *panes.Context, acid sim.ACID) (string, error) {
	trk, ok := ctx.GetTrackByCallsign(av.ADSBCallsign(acid)) // HAX conflates callsign/ACID
	if !ok || !trk.IsAssociated() {
		return "", ErrSTARSIllegalFlight
	}

	for _, rules := range ctx.FacilityAdaptation.AirspaceAwareness {
		fp := trk.FlightPlan
		for _, fix := range rules.Fix {
			// Does the fix in the rules match the route?
			if fix != "ALL" && fp.ExitFix != fix {
				continue
			}

			// Does the final altitude satisfy the altitude range, if specified?
			alt := rules.AltitudeRange
			if !(alt[0] == 0 && alt[1] == 0) /* none specified */ &&
				(fp.RequestedAltitude < alt[0] || fp.RequestedAltitude > alt[1]) {
				continue
			}

			// Finally make sure any aircraft type specified in the rules
			// in the matches.
			if perf, ok := av.DB.AircraftPerformance[fp.AircraftType]; ok {
				engineType := perf.Engine.AircraftType
				if len(rules.AircraftType) == 0 || slices.Contains(rules.AircraftType, engineType) {
					return rules.ReceivingController, nil
				}
			}
		}
	}

	return "", ErrSTARSIllegalPosition
}

func singleScope(ctx *panes.Context, facilityIdentifier string) *av.Controller {
	var controllersInFacility []*av.Controller
	for _, controller := range ctx.Client.State.Controllers {
		if controller.FacilityIdentifier == facilityIdentifier {
			controllersInFacility = append(controllersInFacility, controller)
		}
	}
	if len(controllersInFacility) == 1 {
		return controllersInFacility[0]
	} else {
		return nil
	}
}

// Given a controller TCP id and optionally an aircraft callsign, returns
// the associated Controller.
func (sp *STARSPane) lookupControllerForId(ctx *panes.Context, id string, acid sim.ACID) *av.Controller {
	haveTrianglePrefix := strings.HasPrefix(id, STARSTriangleCharacter)
	id = strings.TrimPrefix(id, STARSTriangleCharacter)

	lc := len(id)
	if lc == 0 {
		return nil
	}

	if haveTrianglePrefix {
		if lc == 1 {
			// Facility id where there's only one controller at that facility.
			return singleScope(ctx, id)
		} else if lc == 3 {
			// ∆N4P for example. Must be a different facility.
			for _, control := range ctx.Client.State.Controllers {
				if control.SectorID == id[1:] && control.FacilityIdentifier == string(id[0]) {
					return control
				}
			}
		}
	} else if id == "C" {
		// ARTCC airspace-awareness; must have an aircraft callsign
		if acid == "" {
			return nil
		}

		if tcp, err := calculateAirspace(ctx, acid); err != nil {
			return nil
		} else if ctrl, ok := ctx.Client.State.Controllers[sim.ControllerPosition(tcp)]; ok {
			return ctrl
		}
	} else {
		// Non ARTCC airspace-awareness handoffs
		if lc == 1 { // Must be the same sector.
			userController := *ctx.UserController()

			for _, control := range ctx.Client.State.Controllers { // If the controller fac/ sector == userControllers fac/ sector its all good!
				if control.FacilityIdentifier == "" && // Same facility? (Facility ID will be "" if they are the same fac)
					control.SectorID[0] == userController.SectorID[0] && // Same Sector?
					control.SectorID[1] == id[0] { // The actual controller
					return control
				}
			}
		} else if lc == 2 {
			// Must be a same sector || same facility.
			for _, control := range ctx.Client.State.Controllers {
				if control.SectorID == id && control.FacilityIdentifier == "" {
					return control
				}
			}
		}

		for _, control := range ctx.Client.State.Controllers {
			if control.ERAMFacility && control.SectorID == id {
				return control
			}
		}
	}
	return nil
}

func (sp *STARSPane) tryGetClosestTrack(ctx *panes.Context, mousePosition [2]float32, transforms radar.ScopeTransformations) (*sim.Track, float32) {
	var trk *sim.Track
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, t := range sp.visibleTracks {
		pw := transforms.WindowFromLatLongP(t.Location)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			trk = &t
			distance = dist
		}
	}

	return trk, distance
}

func (sp *STARSPane) tryGetClosestGhost(ghosts []*av.GhostTrack, mousePosition [2]float32, transforms radar.ScopeTransformations) (*av.GhostTrack, float32) {
	var ghost *av.GhostTrack
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, g := range ghosts {
		pw := transforms.WindowFromLatLongP(g.Position)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			ghost = g
			distance = dist
		}
	}

	return ghost, distance
}

// findClickedTrackAndGhost finds the closest track and ghost to the mouse position,
// returning the track and the ghost if it's closer than the track.
func (sp *STARSPane) findClickedTrackAndGhost(ctx *panes.Context, mousePosition [2]float32,
	transforms radar.ScopeTransformations, ghosts []*av.GhostTrack) (*sim.Track, *av.GhostTrack) {
	trk, trkDistance := sp.tryGetClosestTrack(ctx, mousePosition, transforms)
	ghost, ghostDistance := sp.tryGetClosestGhost(ghosts, mousePosition, transforms)

	var clickedGhost *av.GhostTrack
	if ghost != nil && ghostDistance < trkDistance {
		clickedGhost = ghost
	}

	return trk, clickedGhost
}
