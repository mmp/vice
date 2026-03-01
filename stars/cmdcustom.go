// stars/cmdcustom.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Custom/special commands that aren't real-world STARS commands

package stars

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/wx"

	"github.com/goforj/godump"
)

func registerCustomCommands() {
	registerCommand(CommandModeNone, ".BOUNDS", func(sp *STARSPane) { sp.showTRACONBoundary = !sp.showTRACONBoundary })

	// Mode-switching commands
	registerCommand(CommandModeNone, ".DRAWROUTE", func(sp *STARSPane, ctx *panes.Context) CommandStatus {
		sp.setCommandMode(ctx, CommandModeDrawRoute)
		return CommandStatus{Clear: ClearInput}
	})

	// Display toggles
	registerCommand(CommandModeNone, ".ROUTE", func(sp *STARSPane) { sp.drawRouteAircraft = "" })

	// .ROUTE[SLEW]: Draw route for clicked aircraft
	registerCommand(CommandModeNone, ".ROUTE[SLEW]", func(sp *STARSPane, trk *sim.Track) {
		sp.drawRouteAircraft = trk.ADSBCallsign
	})

	registerCommand(CommandModeNone, ".VFR", func(sp *STARSPane) { sp.showVFRAirports = !sp.showVFRAirports })

	// .WIND: Enter wind drawing mode
	registerCommand(CommandModeNone, ".WIND", func(sp *STARSPane, ctx *panes.Context) CommandStatus {
		sp.setCommandMode(ctx, CommandModeDrawWind)
		if sp.atmosGrid == nil {
			ctx.Client.GetAtmosGrid(ctx.Client.State.SimTime,
				func(ag *wx.AtmosGrid, err error) {
					if err != nil {
						ctx.Lg.Errorf("%v", err)
					} else {
						sp.atmosGrid = ag
						sp.windDrawAltitudeIndex = sp.minWindDrawAltitudeIndex(ctx)
					}
				})
		}
		sp.windDrawAltitudeIndex = sp.minWindDrawAltitudeIndex(ctx)
		return CommandStatus{Clear: ClearInput}
	})

	// ?: print aircraft state
	registerCommand(CommandModeNone, "?[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) {
		ads, err := ctx.Client.GetAircraftDisplayState(trk.ADSBCallsign)
		if err != nil {
			ctx.Lg.Error("print aircraft", slog.String("callsign", string(trk.ADSBCallsign)),
				slog.Any("err", err))
		} else {
			state := sp.TrackState[trk.ADSBCallsign]
			fmt.Println(ads.Spew + "\n\n\n" + godump.DumpStr(state))
		}
	})

	// Capture commands (require capture.enabled)
	registerCommand(CommandModeNone, "CR", func(sp *STARSPane) error {
		if !sp.capture.enabled {
			return ErrSTARSCommandFormat
		}
		sp.capture.specifyingRegion = false
		sp.capture.haveRegion = false
		return nil
	})

	// CR[POS_RAW]: Set first capture region point
	registerCommand(CommandModeNone, "CR[POS_RAW]", func(sp *STARSPane, pos [2]float32) (CommandStatus, error) {
		if !sp.capture.enabled {
			return CommandStatus{}, ErrSTARSCommandFormat
		}
		sp.capture.specifyingRegion = true
		sp.capture.region[0] = pos
		return CommandStatus{
			Clear: ClearNone,
			CommandHandlers: makeCommandHandlers(
				"CR[POS_RAW]", func(sp *STARSPane, pos [2]float32) {
					sp.capture.region[1] = pos
					sp.capture.specifyingRegion = false
					sp.capture.haveRegion = true
				}),
		}, nil
	})

	registerCommand(CommandModeNone, "CS", func(sp *STARSPane) error {
		if !sp.capture.enabled {
			return ErrSTARSCommandFormat
		}
		sp.capture.doStill = true
		return nil
	})

	registerCommand(CommandModeNone, "CV", func(sp *STARSPane) error {
		if !sp.capture.enabled {
			return ErrSTARSCommandFormat
		}
		sp.capture.doVideo = !sp.capture.doVideo
		return nil
	})

	// TGT GEN / TGT GEN LOCK
	// Empty command: do nothing
	registerCommand(CommandModeTargetGen, "", func() {})

	// P: Toggle sim pause
	registerCommand(CommandModeTargetGen, "P", func(ctx *panes.Context) {
		ctx.Client.ToggleSimPause()
	})

	registerCommand(CommandModeTargetGen, "[SLEW]", func(trk *sim.Track) {})
	registerCommand(CommandModeTargetGen, "[ALL_TEXT]", targetGenAircraftCommand)
	registerCommand(CommandModeTargetGen, "[ALL_TEXT][SLEW]", targetGenClickCommand)

	registerCommand(CommandModeTargetGenLock, "", func() {})
	registerCommand(CommandModeTargetGenLock, "P", func(ctx *panes.Context) {
		ctx.Client.ToggleSimPause()
	})
	registerCommand(CommandModeTargetGenLock, "[ALL_TEXT]", targetGenAircraftCommand)
	registerCommand(CommandModeTargetGenLock, "[ALL_TEXT][SLEW]", targetGenClickCommand)

	targetGenSendMessage := func(ctx *panes.Context, text string) {
		ctx.Client.SendGlobalMessage(text)
	}
	registerCommand(CommandModeTargetGen, "/[ALL_TEXT]", targetGenSendMessage)
	registerCommand(CommandModeTargetGenLock, "/[ALL_TEXT]", targetGenSendMessage)

	// .DRAWROUTE
	registerCommand(CommandModeDrawRoute, "[POS]", func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL) CommandStatus {
		sp.drawRoutePoints = append(sp.drawRoutePoints, pos)
		var cb []string
		for _, p := range sp.drawRoutePoints {
			cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
		}
		ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))
		return CommandStatus{Clear: ClearNone, Output: fmt.Sprintf("%d POINTS", len(sp.drawRoutePoints))}
	})
}

// targetGenAircraftCommand handles aircraft commands in target gen mode.
// Parses the input to extract callsign suffix and commands, then runs them.
func targetGenAircraftCommand(sp *STARSPane, ctx *panes.Context, input string) (CommandStatus, error) {
	if input == "" {
		return CommandStatus{}, ErrSTARSCommandFormat
	}

	// Parse callsign suffix and commands
	suffix, cmds, ok := strings.Cut(input, " ")
	if !ok {
		suffix = string(sp.tgtGenDefaultCallsign(ctx))
		cmds = input
	}

	if ctx.Client.RadioIsActive() && !ctx.TCWIsPrivileged(ctx.UserTCW) && cmds != "X" {
		// Don't allow issuing commands during pilot transmissions unless
		// it's an instructor/RPO or the command is "X" to delete an aircraft.
		return CommandStatus{Clear: ClearNone}, nil
	}

	matching := ctx.TracksFromACIDSuffix(suffix)

	var trk *sim.Track
	multiple := len(matching) > 1
	if len(matching) > 0 {
		trk = matching[0]
	} else if sp.tgtGenDefaultCallsign(ctx) != "" {
		// If a valid callsign wasn't given, try the last callsign used.
		trk, _ = ctx.GetTrackByCallsign(sp.tgtGenDefaultCallsign(ctx))
		// But now we're going to run all of the given input as commands.
		cmds = input
	}

	if trk != nil {
		sp.runAircraftCommands(ctx, trk.ADSBCallsign, cmds, multiple, false)
	} else {
		return CommandStatus{}, ErrSTARSIllegalACID
	}
	return CommandStatus{}, nil
}

// targetGenClickCommand runs commands on clicked aircraft in target gen mode.
func targetGenClickCommand(sp *STARSPane, ctx *panes.Context, cmd string, trk *sim.Track) error {
	if ctx.Client.RadioIsActive() && !ctx.TCWIsPrivileged(ctx.UserTCW) && cmd != "X" {
		// Don't allow issuing commands during pilot transmissions unless
		// it's an instructor/RPO or the command is "X" to delete an aircraft.
		return nil
	}

	if len(cmd) > 0 {
		sp.runAircraftCommands(ctx, trk.ADSBCallsign, cmd, false, true)
	}
	return nil
}
