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
	"github.com/mmp/vice/client"
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
	CommandModeFMA_TSAS
	CommandModeIFDT
	CommandModeFlightData
	CommandModeCollisionAlert
	CommandModeMin
	CommandModeTargetGen
	CommandModeTargetGenLock
	CommandModeReleaseDeparture
	CommandModeRestrictionArea
	CommandModeDrawRoute
	CommandModeDrawWind
	CommandModeMacro

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
	case CommandModeFMA_TSAS:
		return "FMA/TSAS"
	case CommandModeIFDT:
		return "IP"
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
	case CommandModeMacro:
		return "AM"
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
		input = input[1:]
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

			// If there's an active spinner, it gets keyboard input first.
			if sp.activeSpinner != nil {
				mode, inputErr := sp.activeSpinner.KeyboardInput(sp.previewAreaInput)
				if inputErr != nil {
					err = inputErr
				} else {
					sp.setCommandMode(ctx, mode)
				}
			} else if len(sp.transientCommandHandlers) > 0 {
				// Check if transient command handlers should intercept this input
				status, err = sp.executeTransientCommandHandlers(ctx, sp.previewAreaInput,
					nil, false, [2]float32{}, nil, radar.ScopeTransformations{})
			} else if s, e, matched := sp.tryExecuteMacro(ctx, sp.previewAreaInput, false,
				[2]float32{}, nil, radar.ScopeTransformations{}); matched {
				status, err = s, e
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
			} else if ctx.Keyboard.KeyShift() { // F16
				sp.setCommandMode(ctx, CommandModeMacro)
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
				sp.setCommandMode(ctx, CommandModeFMA_TSAS)
			}

		case imgui.KeyF9:
			if ctx.Keyboard.KeyControl() {
				sp.resetInputState(ctx.Platform)
				ps.DisplayDCB = !ps.DisplayDCB
			} else {
				sp.setCommandMode(ctx, CommandModeVFRPlan)
			}

		case imgui.KeyF10:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRangeRings)
			} else {
				sp.setCommandMode(ctx, CommandModeIFDT)
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

		case imgui.KeyF15:
			sp.setCommandMode(ctx, CommandModeTargetGen)

		case imgui.KeyF16:
			sp.setCommandMode(ctx, CommandModeMacro)

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

	ctx.Client.RunAircraftCommands(client.AircraftCommandRequest{
		Callsign:     callsign,
		Commands:     cmds,
		Multiple:     multiple,
		ClickedTrack: clickedTrack,
	}, func(errStr string, remaining string) {
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
		sp.ReleaseRequests = make(map[av.ADSBCallsign]any)
	}

	ps := sp.currentPrefs()
	releaseAircraft := ctx.Client.State.GetSTARSReleaseDepartures()

	fa := ctx.FacilityAdaptation
	for _, list := range fa.Lists.Coordination {
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
			delta := func() float32 {
				if ctx.Keyboard != nil {
					if ctx.Keyboard.KeyControl() {
						return 3 * mouse.Wheel[1]
					}
				}
				return mouse.Wheel[1]
			}()
			nr := math.Clamp(r+delta, 6, 256) // 4-33
			ps.Range = nr

			// We want to zoom in centered at the mouse position; this affects
			// the scope center after the zoom, so we'll find the
			// transformation that gives the new center position.
			mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
			scale := nr / r
			centerTransform := math.Identity3x3().
				Translate(mouseLL[0], mouseLL[1]).
				Scale(scale, scale).
				Translate(-mouseLL[0], -mouseLL[1])

			ps.UserCenter = centerTransform.TransformPoint(ps.UserCenter)
			ps.UseUserCenter = true
		}
	}

	// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
	// On macOS, physical Ctrl-click is delivered as a right-click by Cocoa,
	// and physical Ctrl maps to KeySuper() due to the Ctrl/Super swap.
	// So we check both: primary release + KeyControl (Cmd-Shift-Click on macOS,
	// Ctrl-Shift-Click elsewhere) and secondary release + KeySuper (physical
	// Ctrl-Shift-Click on macOS).
	if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() &&
		((ctx.Mouse.Released[platform.MouseButtonPrimary] && ctx.Keyboard.KeyControl()) ||
			(ctx.Mouse.Released[platform.MouseButtonSecondary] && ctx.Keyboard.KeySuper())) {
		mouseLatLong := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
		ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
	}

	if ctx.Mouse.Released[platform.MouseButtonPrimary] {

		if ctx.Keyboard != nil && ctx.Keyboard.KeyControl() {
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms); trk != nil {
				if _, ok := sp.TrackState[trk.ADSBCallsign]; ok {
					anno := sp.annotations(ctx, trk.ADSBCallsign)
					anno.IsSelected = !anno.IsSelected
					ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
						func(err error) { sp.displayError(err, ctx, "") })
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
		} else if s, e, matched := sp.tryExecuteMacro(ctx, sp.previewAreaInput, true,
			ctx.Mouse.Pos, ghosts, transforms); matched {
			status, err = s, e
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
					sp.resetInputState(ctx.Platform)
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
			if _, ok := sp.TrackState[trk.ADSBCallsign]; ok {
				anno := sp.annotations(ctx, trk.ADSBCallsign)
				anno.IsSelected = !anno.IsSelected
				ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno,
					func(err error) { sp.displayError(err, ctx, "") })
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
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(sp.Colors.List),
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
	sp.resetInputState(ctx.Platform)
	sp.commandMode = mode
}

func (sp *STARSPane) resetInputState(pl platform.Platform) {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.wipRBL = nil
	sp.wipRestrictionArea = nil

	sp.transientCommandHandlers = nil
	sp.activeSpinner = nil

	sp.drawRoutePoints = nil

	pl.EndCaptureMouse()
	pl.StopMouseDeltaMode()
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
func calculateAirspace(ctx *panes.Context, trk *sim.Track) (string, error) {
	area := ctx.UserController().Area
	for _, rules := range ctx.FacilityAdaptation.AirspaceAwarenessForArea(area) {
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

// lookupControllerByTCP resolves a TCP identifier to a controller.
// Handles: triangle prefix (external facility), single char (same sector expansion),
// and two char (same facility or ERAM).
// userSectorId is required for single-char expansion (e.g., "K" -> "2K" if user is in sector 2).
func lookupControllerByTCP(controllers map[sim.ControlPosition]*av.Controller, id string, userSectorId string) *av.Controller {
	findController := func(pred func(*av.Controller) bool) *av.Controller {
		for _, ctrl := range controllers {
			if pred(ctrl) {
				return ctrl
			}
		}
		return nil
	}

	haveTrianglePrefix := strings.HasPrefix(id, STARSTriangleCharacter)
	id = strings.TrimPrefix(id, STARSTriangleCharacter)

	if len(id) == 0 {
		return nil
	}

	if haveTrianglePrefix {
		// Triangle prefix: ∆N4P format - facility identifier + position
		if len(id) == 3 {
			return findController(func(ctrl *av.Controller) bool {
				return ctrl.Position == id[1:] && ctrl.FacilityIdentifier == string(id[0])
			})
		}
		return nil
	}

	// Single char: expand using user's sector (e.g., "K" -> "2K" if user is "2J")
	if len(id) == 1 && len(userSectorId) >= 2 {
		if ctrl := findController(func(ctrl *av.Controller) bool {
			return ctrl.FacilityIdentifier == "" &&
				ctrl.Position[0] == userSectorId[0] &&
				ctrl.Position[1] == id[0]
		}); ctrl != nil {
			return ctrl
		}
	}

	// Two chars: same facility lookup
	if len(id) == 2 {
		if ctrl := findController(func(ctrl *av.Controller) bool {
			return ctrl.Position == id && ctrl.FacilityIdentifier == ""
		}); ctrl != nil {
			return ctrl
		}
	}

	// Fallback: ERAM facility
	return findController(func(ctrl *av.Controller) bool {
		return ctrl.ERAMFacility && string(ctrl.PositionId()) == id
	})
}

// lookupControllerWithAirspace resolves a TCP identifier to a controller,
// with support for ARTCC airspace awareness ("C" identifier).
// Use this when the TCP might be "C" and you have an aircraft context.
func lookupControllerWithAirspace(ctx *panes.Context, id string, trk *sim.Track) *av.Controller {
	if id == "C" {
		tcp, err := calculateAirspace(ctx, trk)
		if err != nil {
			return nil
		}
		if ctrl, ok := ctx.Client.State.Controllers[sim.ControlPosition(tcp)]; ok {
			return ctrl
		}
		return nil
	}

	return lookupControllerByTCP(ctx.Client.State.Controllers, id, ctx.UserController().Position)
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

// macroCommandModes maps the mode strings used in macro [MODE] prefixes
// to their corresponding CommandMode values.
var macroCommandModes = map[string]CommandMode{
	"INIT CNTL":  CommandModeInitiateControl,
	"F1":         CommandModeInitiateControl,
	"TRK RPOS":   CommandModeTrackReposition,
	"F2":         CommandModeTrackReposition,
	"TRK SUSP":   CommandModeTrackSuspend,
	"F3":         CommandModeTrackSuspend,
	"TERM CNTL":  CommandModeTerminateControl,
	"F4":         CommandModeTerminateControl,
	"HND OFF":    CommandModeHandOff,
	"F5":         CommandModeHandOff,
	"FLT DATA":   CommandModeFlightData,
	"F6":         CommandModeFlightData,
	"MULTI FUNC": CommandModeMultiFunc,
	"F7":         CommandModeMultiFunc,
	"F8":         CommandModeFMA_TSAS,
	"VFR FP":     CommandModeVFRPlan,
	"F9":         CommandModeVFRPlan,
	"F10":        CommandModeIFDT,
	"CA":         CommandModeCollisionAlert,
	"F11":        CommandModeCollisionAlert,
	"RESTR AREA": CommandModeRestrictionArea,
	"F12":        CommandModeRestrictionArea,
	"REL DEP":    CommandModeReleaseDeparture,
	"F13":        CommandModeReleaseDeparture,
	"F14":        CommandModeSite,
	"TGT GEN":    CommandModeTargetGen,
	"F15":        CommandModeTargetGen,
	"F16":        CommandModeMacro,
	"MAP":        CommandModeMaps,
}

func init() {
	sim.ValidateMacroCommandMode = func(mode string) bool {
		_, ok := macroCommandModes[mode]
		return ok
	}
}

func (sp *STARSPane) executeMacro(ctx *panes.Context, macro *sim.STARSMacro, inputIsSlew bool, args []string,
	mousePosition [2]float32, ghosts []*av.GhostTrack, transforms radar.ScopeTransformations) (CommandStatus, error) {

	for _, cmd := range macro.Commands {
		// Parse optional [MODE] prefix; if absent, use CommandModeNone.
		mode := CommandModeNone
		if strings.HasPrefix(cmd, "[") {
			endBracket := strings.Index(cmd, "]")
			if endBracket == -1 {
				sp.setCommandMode(ctx, CommandModeNone)
				return CommandStatus{}, ErrSTARSCommandFormat
			}
			var ok bool
			mode, ok = macroCommandModes[cmd[1:endBracket]]
			if !ok {
				sp.setCommandMode(ctx, CommandModeNone)
				return CommandStatus{}, ErrSTARSCommandFormat
			}
			cmd = cmd[endBracket+1:]
		}

		// Substitute $1..$9 with args
		for i := 1; i <= 9; i++ {
			placeholder := "$" + strconv.Itoa(i)
			var replacement string
			if i-1 < len(args) {
				replacement = args[i-1]
			}
			cmd = strings.ReplaceAll(cmd, placeholder, replacement)
		}

		// Use setCommandMode to properly reset state between sub-commands.
		sp.setCommandMode(ctx, mode)

		// For MultiFunc mode, extract the first character as the
		// multi-func prefix, matching the normal keyboard input behavior.
		if mode == CommandModeMultiFunc && len(cmd) > 0 {
			sp.multiFuncPrefix = string(cmd[0])
			cmd = cmd[1:]
		}

		var err error
		if inputIsSlew {
			_, err = sp.executeSTARSClickedCommand(ctx, cmd, mousePosition, ghosts, transforms)
		} else {
			_, err = sp.executeSTARSCommand(ctx, cmd)
		}
		if err != nil {
			sp.setCommandMode(ctx, CommandModeNone)
			return CommandStatus{}, err
		}
	}

	sp.setCommandMode(ctx, CommandModeNone)
	return CommandStatus{Output: strings.ToUpper(macro.Output)}, nil
}

// tryExecuteMacro checks all facility macros against the current command
// mode and user input. Returns matched=true if a macro was found and
// executed; the caller should fall through to built-in commands otherwise.
//
// Two-pass matching: exact-name macros take priority (pass 1), then
// parameterized catch-all macros (empty name + uses $1) act as a
// fallback (pass 2).
func (sp *STARSPane) tryExecuteMacro(ctx *panes.Context, input string, isSlew bool, mousePosition [2]float32,
	ghosts []*av.GhostTrack, transforms radar.ScopeTransformations) (CommandStatus, error, bool) {
	fields := strings.Fields(input)
	var inputName string
	var args []string
	if len(fields) > 0 {
		inputName, args = fields[0], fields[1:]
	}

	macros := ctx.FacilityAdaptation.STARSMacros

	// Helper: check mode and slew match.
	modeMatches := func(macro *sim.STARSMacro) bool {
		if macro.IsSlew() != isSlew {
			return false
		}
		if mode := macro.Mode(); mode == "" {
			return sp.commandMode == CommandModeNone
		} else {
			return macroCommandModes[mode] == sp.commandMode
		}
	}

	// Pass 1: exact name match. Skip parameterized catch-alls (empty
	// name + uses $1) — those belong in pass 2.
	for i := range macros {
		macro := &macros[i]
		if macro.Name() == "" && macro.HasParameters() {
			continue
		}
		if macro.Name() != inputName || !modeMatches(macro) {
			continue
		}
		status, err := sp.executeMacro(ctx, macro, isSlew, args, mousePosition, ghosts, transforms)
		return status, err, true
	}

	// Pass 2: catch-all macros with empty name that use $1.
	// All entered words become parameters ($1, $2, ...).
	if len(fields) > 0 {
		for i := range macros {
			macro := &macros[i]
			if macro.Name() != "" || !macro.HasParameters() || !modeMatches(macro) {
				continue
			}
			status, err := sp.executeMacro(ctx, macro, isSlew, fields, mousePosition, ghosts, transforms)
			return status, err, true
		}
	}

	return CommandStatus{}, nil, false
}
