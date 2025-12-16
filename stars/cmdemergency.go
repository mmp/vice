// stars/cmdemergency.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 7 of the TCW Operator Manual

package stars

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
)

func init() {
	// 7.1 Display bearing / range to known emergency airport / heliport
	// registerCommand(CommandModeNone, "*[POS][POS]", unimplementedCommand) // needs a callback...

	// 7.2 Display bearing / range to known emergency airport / heliport (implied)
	// registerCommand(CommandModeNone, "*[#]L[POS]", unimplementedCommand)
	// registerCommand(CommandModeNone, "*[#][POS]", unimplementedCommand)

	// 7.4 Force a track into or out of Special Condition (implied)
	registerCommand(CommandModeNone, "[SPC][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, spc string, trk *sim.Track) error {
			if !trk.IsAssociated() || !ctx.Client.StringIsSPC(spc) {
				return ErrSTARSCommandFormat
			}
			if sqspc, _ := trk.Squawk.IsSPC(); sqspc && trk.Mode != av.TransponderModeStandby {
				// Can't override if they're already squawking a different one.
				return ErrSTARSIllegalFunctionAlertActive
			}

			state := sp.TrackState[trk.ADSBCallsign]
			state.SPCAcknowledged = false
			var spec sim.FlightPlanSpecifier
			if spc == trk.FlightPlan.SPCOverride { // matches, so turn it off
				spec.SPCOverride.Set("")
			} else { // set it to something new
				spec.SPCOverride.Set(spc)
			}
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* no display */)
			return nil
		})

	// 7.5 Show hospital location on-screen (implied)
	// registerCommand(CommandModeNone, "*[#]", unimplementedCommand)

	// 7.6 Display / remove Hospital list (p. 7-10)
	//(CommandModeMultiFunc, "TH", unimplementedCommand),

	// 7.7 Move hospital list
	//(CommandModeMultiFunc, "TH[SLEW]", unimplementedCommand),

	// 7.9 Enable / inhibit CA for an owned track system-wide
	registerCommand(CommandModeCollisionAlert, "K [TRK_ACID]|K [TRK_BCN]|K[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if !trk.IsAssociated() {
			return ErrSTARSIllegalTrack
		}

		var spec sim.FlightPlanSpecifier
		spec.DisableCA.Set(!trk.FlightPlan.DisableCA)
		spec.MCISuppressedCode.Set(av.Squawk(0)) // 7-18: this clears the MCI inhibit code
		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
		return nil
	})

	// 7.10 Enable / inhibit CA for track pair system-wide
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "[SLEW]"})

	// 7.11 Inhibit CA for track pair in current conflict system-wide
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "P [TRK_ACID]|P[SLEW]"})
	// 7.11 Inhibit CA for track pair in current conflict system-wide *and*
	// 7.12 Enable CA for CA-inhibited track pair system-wide
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "P [TRK_ACID][SLEW]"})

	// 7.13 Enable / inhibit CA for track pairs owned by entering TCP system wide
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "C"}) // toggle
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "CE"}) // enable
	// registerCommand(UserCommand{M: CommandModeCollisionAlert, C: "CI"}) // inhibit

	// 7.14 Inhibit an MSAW alert for a single track in MSAW system-wide (p. 7-26)
	registerCommand(CommandModeMultiFunc, "Q[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() || (!ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && !ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP)) {
			return ErrSTARSIllegalTrack
		}

		state := sp.TrackState[trk.ADSBCallsign]
		state.InhibitMSAW = true
		return nil
	})

	// 7.15 Enable / inhibit MSAW for a single track system-wide (p. 7-27)
	registerCommand(CommandModeMultiFunc, "V[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}

		var spec sim.FlightPlanSpecifier
		spec.DisableMSAW.Set(!trk.FlightPlan.DisableMSAW)
		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	})

	// 7.16 Acknowledge SPC alert for unassociated track system-wide
	// {(CommandModeMultiFunc, G[SLEW]" )

	// 7.17 Toggle MCI suppression for individual track and specified beacon (p. 7-29)
	registerCommand(CommandModeCollisionAlert, "M [TRK_ACID] [BCN]|M [TRK_BCN] [BCN]|M[BCN][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track, beacon av.Squawk) (CommandStatus, error) {
			if ps.DisableMCIWarnings {
				return CommandStatus{}, ErrSTARSIllegalFunction
			}
			if trk.IsUnassociated() || (!ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && !ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP)) {
				return CommandStatus{}, ErrSTARSIllegalTrack
			}

			sfp := trk.FlightPlan
			var spec sim.FlightPlanSpecifier
			if sfp.MCISuppressedCode == beacon { // entered same code; clear suppression
				spec.MCISuppressedCode.Set(av.Squawk(0))
			} else {
				spec.MCISuppressedCode.Set(beacon)
				spec.DisableCA.Set(false) // 7-30; can't have both
			}

			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
			return CommandStatus{}, nil
		})
	registerCommand(CommandModeCollisionAlert, "M [TRK_ACID]|M [TRK_BCN]|M[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track) (CommandStatus, error) {
			if ps.DisableMCIWarnings {
				return CommandStatus{}, ErrSTARSIllegalFunction
			}
			if trk.IsUnassociated() || (!ctx.UserControlsPosition(trk.FlightPlan.TrackingController) && !ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP)) {
				return CommandStatus{}, ErrSTARSIllegalTrack
			}

			sfp := trk.FlightPlan

			var spec sim.FlightPlanSpecifier
			if sfp.MCISuppressedCode != av.Squawk(0) {
				// clear suppression
				spec.MCISuppressedCode.Set(av.Squawk(0))
			} else {
				// TODO: 0477 is the default but it's adaptable
				spec.MCISuppressedCode.Set(av.Squawk(0o0477))
				spec.DisableCA.Set(false) // 7-30; can't have both
			}

			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
			return CommandStatus{}, nil
		})

	// 7.18 Hide / show MCI suppression list
	registerCommand(CommandModeMultiFunc, "TQ", func(ps *Preferences) {
		ps.MCISuppressionList.Visible = !ps.MCISuppressionList.Visible
	})

	// 7.19 Move MCI suppression list
	registerCommand(CommandModeMultiFunc, "TQ[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.MCISuppressionList.Position = pos
		ps.MCISuppressionList.Visible = true
	})

	// 7.20.1 Suppress / clear Military alert suppression zone
	// registerCommand(CommandModeRestrictionArea, "Z[TEXT] S", unimplemented)
	// registerCommand(CommandModeRestrictionArea, "Z[TEXT] C", unimplemented)

	// 7.20.2 Hide / show Military alert suppression zone list (p. 7-35)
	//(CommandModeMultiFunc, "TSZ", unimplementedCommand),

	// 7.20.3 Move Military alert suppression zone list (p. 7-36)
	//(CommandModeMultiFunc, "TSZ[SLEW]", unimplementedCommand),

	// 7.23.1 Enable / inhibit / display ASV volume(s) for an airport
	//(CommandModeMultiFunc, "W[FIELD:3] E", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] I", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] S", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] A E", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] A I", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] A S", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] D E", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] D I", unimplementedCommand),
	//(CommandModeMultiFunc, "W[FIELD:3] D S", unimplementedCommand),

	// 7.23.2 Enable / inhibit / display ASV processing at this TCW/TDW
	//{(CommandModeMultiFunc,  "WE")
	//{(CommandModeMultiFunc,  "WI")
	//{(CommandModeMultiFunc,  "WS")

	// 7.23.3 Enable /inhibit / display ASV processing for a flight system-wide
	//{(CommandModeMultiFunc,  "W[TRK_ACID] E|W[BCN] E|WE[SLEW]")
	//{(CommandModeMultiFunc,  "W[TRK_ACID] I|W[BCN] I|WI[SLEW]")
	//{(CommandModeMultiFunc,  "W[TRK_ACID] S|W[BCN] S|WS[SLEW]")

	// 7.30 Toggle MCI suppression for individual track (p. 7-81)
	// This is already handled by the command at line 112:
	// "M [TRK_ACID] [BCN]|M[BCN][SLEW]"
}
