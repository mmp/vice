// stars/cmdsupe.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 8 of the TCW Operator Manual
package stars

import "github.com/mmp/vice/panes"

func init() {
	// 8.1 Enable / inhibit CA system-wide
	registerCommand(CommandModeCollisionAlert, "AE", func(ps *Preferences) CommandStatus {
		if !ps.DisableCAWarnings {
			return CommandStatus{Output: "NO CHANGE"}
		}
		ps.DisableCAWarnings = false
		return CommandStatus{}
	})
	registerCommand(CommandModeCollisionAlert, "AI", func(ps *Preferences) CommandStatus {
		if ps.DisableCAWarnings {
			return CommandStatus{Output: "NO CHANGE"}
		}
		ps.DisableCAWarnings = true
		return CommandStatus{}
	})

	// 8.2 Enable / inhibit MCI system-wide
	registerCommand(CommandModeCollisionAlert, "ME", func(ps *Preferences) CommandStatus {
		if !ps.DisableMCIWarnings {
			return CommandStatus{Output: "NO CHANGE"}
		}
		ps.DisableMCIWarnings = false
		return CommandStatus{}
	})
	registerCommand(CommandModeCollisionAlert, "MI", func(ps *Preferences) CommandStatus {
		if ps.DisableMCIWarnings {
			return CommandStatus{Output: "NO CHANGE"}
		}
		ps.DisableMCIWarnings = true
		return CommandStatus{}
	})

	// 8.3 Enable / inhibit MSAW or approach monitor MSAW system-side
	// registerCommand(CommandModeMultiFunc, "VGI", func(ps *Preferences) string { ... })
	// registerCommand(CommandModeMultiFunc, "VGE", func(ps *Preferences) string { ... })
	registerCommand(CommandModeMultiFunc, "VME", func(ps *Preferences) {
		ps.DisableMSAW = false
	})
	registerCommand(CommandModeMultiFunc, "VMI", func(ps *Preferences) {
		ps.DisableMSAW = true
	})

	// 8.4 Enable / inhibit CA suppression zones
	// registerCommand(CommandModeCollisionAlert, "Q[FIELD:3]S|...)

	// 8.5 Inhibit all CA suppression zones for all airports
	// registerCommand(CommandModeCollisionAlert, "QZ", ...)

	// 8.6 Enable / inhibit CRDA system-wide
	// registerCommand(CommandModeMultiFunc, "NE", ...)
	// registerCommand(CommandModeMultiFunc, "NI", ...)

	// 8.7 Enable / inhibit runway pair configuration system-wide
	enableInhibitRunwayPair := func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int, mode string) (CommandStatus, error) {
		if len(sp.ConvergingRunways) == 0 {
			return CommandStatus{}, ErrSTARSIllegalFunction
		}
		for i, pair := range sp.ConvergingRunways {
			if pair.Airport == ap && pair.Index == idx {
				ps.CRDAStatusList.Visible = true

				if mode == "D" {
					ps.CRDA.RunwayPairState[i].Enabled = false
					return CommandStatus{Output: ap + " " + pair.getRunwaysString() + " INHIBITED"}, nil
				} else {
					// Check that neither runway is already enabled in another pair
					for j, pairState := range ps.CRDA.RunwayPairState {
						if !pairState.Enabled {
							continue
						}
						if sp.ConvergingRunways[j].Runways[0] == pair.Runways[0] ||
							sp.ConvergingRunways[j].Runways[0] == pair.Runways[1] ||
							sp.ConvergingRunways[j].Runways[1] == pair.Runways[0] ||
							sp.ConvergingRunways[j].Runways[1] == pair.Runways[1] {
							return CommandStatus{}, ErrSTARSIllegalRunway
						}
					}

					if mode == "T" {
						ps.CRDA.RunwayPairState[i].Mode = CRDAModeTie
					} else {
						ps.CRDA.RunwayPairState[i].Mode = CRDAModeStagger
					}
					ps.CRDA.RunwayPairState[i].Enabled = true
					return CommandStatus{Output: ap + " " + pair.getRunwaysString() + " ENABLED"}, nil
				}
			}
		}
		return CommandStatus{}, ErrSTARSCommandFormat
	}
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]T",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) (CommandStatus, error) {
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "T")
		})
	registerCommand(CommandModeMultiFunc, "NP[NUM]T",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) (CommandStatus, error) {
			ctrl := ctx.Client.State.Controllers[ctx.UserTCP]
			ap := ctrl.DefaultAirport[1:]
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "T")
		})
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]S",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) (CommandStatus, error) {
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "S")
		})
	registerCommand(CommandModeMultiFunc, "NP[NUM]S",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) (CommandStatus, error) {
			ctrl := ctx.Client.State.Controllers[ctx.UserTCP]
			ap := ctrl.DefaultAirport[1:]
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "S")
		})
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]D",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) (CommandStatus, error) {
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "D")
		})
	registerCommand(CommandModeMultiFunc, "NP[NUM]D",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) (CommandStatus, error) {
			ctrl := ctx.Client.State.Controllers[ctx.UserTCP]
			ap := ctrl.DefaultAirport[1:]
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "D")
		})

	// 8.8 Enable / inhibit automatic handoffs (p. 8-13)
	//registerCommand(UserCommand{M: CommandModeHandOff, C: "E", F: unimplementedCommand})
	//registerCommand(UserCommand{M: CommandModeHandOff, C: "I", F: unimplementedCommand})

	// 8.9 Hide / show restriction area at specified TCW/TDWs

	// 8.10 Consolidate all TCPs
	// registerCommand(CommandModeMultiFunc, "C[TCP] ALL|C"+STARSTriangleCharacter+" ALL", ...)

	// 8.11 Change configuration plan
	// registerCommand(CommandModeMultiFunc, "C[CONFIG_ID]|C[CONFIG_ID] ALL|C[CONFIG_ID] [TCP]|C[CONFIG_ID] T|C[CONFIG_ID] ALL T|C[CONFIG_ID] [TCP] T", ...)

	// 8.19 Delete all passive and pending flight plans
	// registerCommand(CommandModeMultiFunc, "CT[TIME]A", ...)

	// 8.22 Sign-off a user from all keyboards
	// registerCommand(CommandModeMultiFunc, "4*USER [TEXT]", ...)
	// registerCommand(CommandModeSignOn, "*USER [TEXT]", ...)

	// 8.23 Print current sign-on status report
	// registerCommand(CommandModeMultiFunc, "4P", ...)
	// registerCommand(CommandModeSignOn, "P", ...)

	// 8.26 Create system message
	// registerCommand(CommandModeRestrictionArea, "+[TEXT]", ...)

	// 8.27 Send system message to workstation message list
	// registerCommand(CommandModeRestrictionArea, "M [TEXT])

	// 8.28 Modify system / workstation message text
	// registerCommand(CommandModeRestrictionArea, "G ...", ...)

	// 8.29 Remove system message from workstation message lists
	// registerCommand(CommandModeRestrictionArea, "M[NUM] NONE", ...)

	// 8.30 Delete system / workstation message
	// registerCommand(CommandModeRestrictionArea, "/[NUM]", ...)

	// 8.31 View receiving TCPs for a system message
	// registerCommand(CommandModeRestrictionArea, "G[NUM]", ...)

	// 8.25 Modify flight plan flush time
	// registerCommand(CommandModeMultiFunc, "T[NUM]F", ...)

	// 8.36 Enable / disable total filter
	// registerCommand(CommandModeMultiFunc, "2T[TEXT] E", ...)
	// registerCommand(CommandModeMultiFunc, "2T[TEXT] D", ...)

	// 8.37 Enable / inhibit flight data auto-modify (FDAM) system-wide
	// registerCommand(CommandModeMultiFunc, "2X", ...)
	// registerCommand(CommandModeMultiFunc, "2XE", ...)
	// registerCommand(CommandModeMultiFunc, "2XI", ...)

	// 8.38 Enable / disable ATPA system-side
	// registerCommand(CommandModeMultiFunc, "2ATPAE", ...)
	// registerCommand(CommandModeMultiFunc, "2ATPAI", ...)

	// 8.39 Enable / disable ATPA approach volume
	// registerCommand(CommandModeMultiFunc, "2ATPA[TEXT]I", ...)
	// registerCommand(CommandModeMultiFunc, "2ATPA[TEXT]E", ...)

	// 8.40 Enable / disable ATPA 2.5nm reduced separation
	// registerCommand(CommandModeMultiFunc, "2.5[TEXT]I", ...)
	// registerCommand(CommandModeMultiFunc, "2.5[TEXT]E", ...)

	// 8.41 Enable / inhibit / display ASV volume(s) for an airport system-wide
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]E", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]I", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]S", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]A E", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]A I", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]A S", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]D E", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]D I", ...)
	// registerCommand(CommandModeMultiFunc, "W*[FIELD:3]D S", ...)

	// 8.42 Modify default scratchpad(s) for scratchpad group
	// registerCommand(CommandModeMultiFunc, "G*[FIELD:1] [*TRI_SP1,PLUS_SP2] // or whatever", ...)

	// 8.43 Modify default scratchpad(s) for requested altitude and fix pair
	// registerCommand(CommandModeMultiFunc, "G*[FIELD:1][FP_FIX_PAIR] [NUM:3] ...", ...)

	// 8.44 Modify default scratchpad(s) for fix pair
	// registerCommand(CommandModeMultiFunc, "G*[FIELD:1][FP_FIX_PAIR] [*TRI_SP1, ...] ...", ...)

	// 8.45 Reset TSAS sequence number values
	// TODO handle F8, "SQRA|SQR[TEXT]", ...

	// 8.46 Resequence multiple TSAS flights
	// F8 "SQ [TRK_ACID] [TRK_ACID]|SQ [TRK_ACID] [TRK_ACID] [TRK_ACID]|... then also slew multi then hit enter
}
