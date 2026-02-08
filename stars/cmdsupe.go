// stars/cmdsupe.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 8 of the TCW Operator Manual
package stars

import (
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
)

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
		if len(sp.CRDAPairs) == 0 {
			return CommandStatus{}, ErrSTARSIllegalFunction
		}
		for i, pair := range sp.CRDAPairs {
			if pair.Airport == ap && pair.Index == idx {
				ps.CRDAStatusList.Visible = true

				if mode == "D" {
					ps.CRDA.RunwayPairState[i].Enabled = false
					return CommandStatus{Output: ap + " " + pair.getRegionsString() + " INHIBITED"}, nil
				} else {
					// Check that neither region is already enabled in another pair
					for j, pairState := range ps.CRDA.RunwayPairState {
						if !pairState.Enabled {
							continue
						}
						if sp.CRDAPairs[j].Regions[0] == pair.Regions[0] ||
							sp.CRDAPairs[j].Regions[0] == pair.Regions[1] ||
							sp.CRDAPairs[j].Regions[1] == pair.Regions[0] ||
							sp.CRDAPairs[j].Regions[1] == pair.Regions[1] {
							return CommandStatus{}, ErrSTARSIllegalRunway
						}
					}

					if mode == "T" {
						ps.CRDA.RunwayPairState[i].Mode = CRDAModeTie
					} else {
						ps.CRDA.RunwayPairState[i].Mode = CRDAModeStagger
					}
					ps.CRDA.RunwayPairState[i].Enabled = true
					return CommandStatus{Output: ap + " " + pair.getRegionsString() + " ENABLED"}, nil
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
			ctrl := ctx.UserController()
			if len(ctrl.DefaultAirport) == 0 {
				return CommandStatus{}, ErrSTARSIllegalFunction
			}
			ap := ctrl.DefaultAirport[1:]
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "T")
		})
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]S",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) (CommandStatus, error) {
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "S")
		})
	registerCommand(CommandModeMultiFunc, "NP[NUM]S",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) (CommandStatus, error) {
			ctrl := ctx.UserController()
			if len(ctrl.DefaultAirport) == 0 {
				return CommandStatus{}, ErrSTARSIllegalFunction
			}
			ap := ctrl.DefaultAirport[1:]
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "S")
		})
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]D",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) (CommandStatus, error) {
			return enableInhibitRunwayPair(sp, ctx, ps, ap, idx, "D")
		})
	registerCommand(CommandModeMultiFunc, "NP[NUM]D",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) (CommandStatus, error) {
			ctrl := ctx.UserController()
			if len(ctrl.DefaultAirport) == 0 {
				return CommandStatus{}, ErrSTARSIllegalFunction
			}
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
	configureFDAM := func(sp *STARSPane, ctx *panes.Context, op sim.FDAMConfigOp, regionId string) error {
		ctx.Client.ConfigureFDAM(op, regionId,
			func(output string, err error) {
				if err != nil {
					sp.displayError(err, ctx, "")
				} else {
					sp.previewAreaOutput = output
				}
			})
		return nil
	}
	registerCommand(CommandModeMultiFunc, "2X",
		func(sp *STARSPane, ctx *panes.Context) error {
			return configureFDAM(sp, ctx, sim.FDAMToggleSystem, "")
		})
	registerCommand(CommandModeMultiFunc, "2XE",
		func(sp *STARSPane, ctx *panes.Context) error {
			return configureFDAM(sp, ctx, sim.FDAMEnableSystem, "")
		})
	registerCommand(CommandModeMultiFunc, "2XI",
		func(sp *STARSPane, ctx *panes.Context) error {
			return configureFDAM(sp, ctx, sim.FDAMInhibitSystem, "")
		})

	// 8.38 Enable / disable ATPA system-wide
	hasATPAVolumes := func(ctx *panes.Context) bool {
		for _, ap := range ctx.Client.State.Airports {
			if len(ap.ATPAVolumes) > 0 {
				return true
			}
		}
		return false
	}
	configureATPA := func(sp *STARSPane, ctx *panes.Context, op sim.ATPAConfigOp, volumeId string) error {
		ctx.Client.ConfigureATPA(op, volumeId,
			func(output string, err error) {
				if err != nil {
					sp.displayError(err, ctx, "")
				} else {
					// This is sort of a hack but gives us instant updates on the scope after state changes
					sp.updateInTrailDistance(ctx)
					sp.previewAreaOutput = output
				}
			})
		return nil
	}
	registerCommand(CommandModeMultiFunc, "2ATPAE",
		func(sp *STARSPane, ctx *panes.Context) error {
			if !hasATPAVolumes(ctx) {
				return ErrSTARSIllegalFunction
			}
			return configureATPA(sp, ctx, sim.ATPAEnable, "")
		})
	registerCommand(CommandModeMultiFunc, "2ATPAI",
		func(sp *STARSPane, ctx *panes.Context) error {
			if !hasATPAVolumes(ctx) {
				return ErrSTARSIllegalFunction
			}
			return configureATPA(sp, ctx, sim.ATPADisable, "")
		})

	// 8.39 Enable / disable ATPA approach volume
	registerCommand(CommandModeMultiFunc, "2ATPA[FIELD]",
		func(sp *STARSPane, ctx *panes.Context, text string) error {
			if n := len(text); n < 2 || n > 6 {
				return ErrSTARSCommandFormat
			} else {
				vol := text[:n-1]
				switch text[n-1] {
				case 'E':
					return configureATPA(sp, ctx, sim.ATPAEnableVolume, vol)
				case 'I':
					for _, state := range sp.TrackState {
						state.DisplayATPAWarnAlert = nil
					}
					return configureATPA(sp, ctx, sim.ATPADisableVolume, vol)
				default:
					return ErrSTARSCommandFormat
				}
			}
		})

	// 8.40 Enable / disable ATPA 2.5nm reduced separation
	registerCommand(CommandModeMultiFunc, "2.5[FIELD]",
		func(sp *STARSPane, ctx *panes.Context, text string) error {
			if n := len(text); n < 2 || n > 6 {
				return ErrSTARSCommandFormat
			} else {
				vol := text[:n-1]
				switch text[n-1] {
				case 'E':
					return configureATPA(sp, ctx, sim.ATPAEnableReduced25, vol)
				case 'I':
					return configureATPA(sp, ctx, sim.ATPADisableReduced25, vol)
				default:
					return ErrSTARSCommandFormat
				}
			}
		})

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
