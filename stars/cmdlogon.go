// stars/cmdlogon.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 3 of the TCW Operator Manual
package stars

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func init() {
	// 3.11.1 Basic consolidation of inactive and future flights (p. 3-22)
	// C[receiver][sender] - Basic consolidation
	consolidate := func(sp *STARSPane, ctx *panes.Context, receiver sim.TCW, sender string, ty sim.ConsolidationType) {
		if len(receiver) == 1 {
			receiver = ctx.UserTCW[:1] + receiver
		}
		if len(sender) == 1 {
			sender = string(ctx.UserTCW[:1]) + sender
		}
		ctx.Client.ConsolidateTCP(receiver, sim.TCP(sender), ty,
			func(err error) { sp.displayError(err, ctx, "") })
	}
	registerCommand(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP1]|C"+STARSTriangleCharacter+"[TCP2]",
		func(sp *STARSPane, ctx *panes.Context, sender string) {
			consolidate(sp, ctx, ctx.UserTCW, sender, sim.ConsolidationBasic)
		})
	registerCommand(CommandModeMultiFunc, "C[TCW][TCP1]|C[TCW][TCP2]",
		func(sp *STARSPane, ctx *panes.Context, receiver sim.TCW, sender string) {
			consolidate(sp, ctx, receiver, sender, sim.ConsolidationBasic)
		})

	// 3.11.2 Limited consolidation of inactive and future flights assigned by fix pairs
	//(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP]/", unimplementedCommand),
	//(CommandModeMultiFunc, "C[TCP][TCP]/", unimplementedCommand),

	// 3.11.3 Full consolidation of active, inactive, and future flights (p. 3-28)
	// C[receiver][sender]+ - Full consolidation
	registerCommand(CommandModeMultiFunc, "C"+STARSTriangleCharacter+"[TCP1]+|C"+STARSTriangleCharacter+"[TCP2]+",
		func(sp *STARSPane, ctx *panes.Context, sender string) {
			consolidate(sp, ctx, ctx.UserTCW, sender, sim.ConsolidationFull)
		})
	registerCommand(CommandModeMultiFunc, "C[TCW][TCP1]+|C[TCW][TCP2]+",
		func(sp *STARSPane, ctx *panes.Context, receiver sim.TCW, sender string) {
			consolidate(sp, ctx, receiver, sender, sim.ConsolidationFull)
		})

	// 3.11.4 Deconsolidate inactive and future flights (p. 3-32)
	// C - Deconsolidate user's own TCP back to their keyboard
	registerCommand(CommandModeMultiFunc, "C",
		func(sp *STARSPane, ctx *panes.Context) {
			ctx.Client.DeconsolidateTCP(sim.TCP(ctx.UserTCW), func(err error) { sp.displayError(err, ctx, "") })
		})
	// C[tcp] - Deconsolidate specified TCP
	registerCommand(CommandModeMultiFunc, "C[TCP2]",
		func(sp *STARSPane, ctx *panes.Context, tcp string) {
			ctx.Client.DeconsolidateTCP(sim.TCP(tcp), func(err error) { sp.displayError(err, ctx, "") })
		})

	// 3.11.5 Limited deconsolidation (p. 3-34)
	//(CommandModeMultiFunc, "C/", unimplementedCommand),

	// 3.11.6 Perform partial resectorization by fix pair ID (p. 3-36)
	//(CommandModeMultiFunc, "C.[TCP][FIELD:1]", unimplementedCommand),

	// 3.11.7 Perform partial resectorization by fix names
	//(CommandModeMultiFunc, "C.[TCP][FIELD:3]", unimplementedCommand),
	//(CommandModeMultiFunc, "C.[TCP][FIELD:3]*[FIELD:3]", unimplementedCommand),

	// 3.11.8 Print out current consolidation or sectorization data (p. 3-38)
	//(CommandModeMultiFunc, "CP1", unimplementedCommand),
	//(CommandModeMultiFunc, "CP2", unimplementedCommand),

	// 3.11.9 Display consolidated positions in Preview area (p. 3-43)
	registerCommand(CommandModeMultiFunc, "D+",
		func(sp *STARSPane, ctx *panes.Context) CommandStatus {
			configId := ctx.Client.State.ConfigurationId
			var parts []string

			for _, cons := range ctx.Client.State.CurrentConsolidation {
				if len(cons.SecondaryTCPs) > 0 {
					// Extract and then sort the secondary TCPs
					secondaries := util.MapSlice(cons.SecondaryTCPs,
						func(s sim.SecondaryTCP) string { return string(s.TCP) })
					slices.Sort(secondaries)

					// Now that they're sorted, prepend a "*" to basic consolidations
					for i, tcp := range secondaries {
						if slices.ContainsFunc(cons.SecondaryTCPs, func(s sim.SecondaryTCP) bool {
							return string(s.TCP) == tcp && s.Type == sim.ConsolidationBasic
						}) {
							secondaries[i] = "*" + secondaries[i]
						}
					}

					// Format: primary:sec1,sec2,... (per Figure 3-8, p. 3-43)
					parts = append(parts, fmt.Sprintf("%s:%s", cons.PrimaryTCP, strings.Join(secondaries, ",")))
				}
			}

			if len(parts) == 0 {
				return CommandStatus{Output: configId + " NO CONSOLIDATIONS"}
			}

			slices.Sort(parts)
			return CommandStatus{Output: configId + " " + strings.Join(parts, " ")}
		})
}
