// stars/cmdslew.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// All of the things that happen with a bare aircraft slew.

package stars

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func registerSlewCommands() {
	registerCommand(CommandModeNone, "[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
			anno := sp.annotations(ctx, trk.ADSBCallsign)
			errCB := func(err error) { sp.displayError(err, ctx, "") }
			writeAnno := func() {
				ctx.Client.SetTrackAnnotations(trk.ADSBCallsign, anno, errCB)
			}

			// This is all (hopefully) following the command precedence list in 2.10

			if trk.IsAssociated() {
				fp := trk.FlightPlan

				if fp.HandoffController != "" {
					if ctx.UserControlsPosition(fp.HandoffController) {
						// 5.1.3 Accept handoff (implied)
						// 5.1.4 Take control of interfacility track (implied)
						// 5.1.10 Accept inbound handoff
						ctx.Client.AcceptHandoff(fp.ACID, errCB)
						return CommandStatus{}
					} else if ctx.UserOwnsFlightPlan(fp) {
						// 5.1.2 Recall handoff (implied)
						// 5.1.6 Recall redirected handoff
						// 5.1.17 Recall handoff (p. 5-33)
						// 5.1.19 Recall redirected handoff
						ctx.Client.CancelHandoff(fp.ACID, errCB)
						return CommandStatus{}
					} else if ctx.UserControlsPosition(fp.RedirectedHandoff.RedirectedTo) ||
						ctx.UserControlsPosition(fp.RedirectedHandoff.GetLastRedirector()) {
						ctx.Client.AcceptRedirectedHandoff(fp.ACID, errCB)
						return CommandStatus{}
					}
				}

				if tcps, ok := sp.PointOuts[fp.ACID]; ok {
					if ctx.UserControlsPosition(tcps.To) {
						// 6.12.2 Accept intrafacility pointout (implied)
						// 6.12.8 Accept interfacility pointout (implied)
						ctx.Client.AcknowledgePointOut(fp.ACID, errCB)
						return CommandStatus{}
					} else if ctx.UserControlsPosition(tcps.From) {
						// 6.12.4 Recall intrafacility pointout
						// 6.12.9 Recall interfacility pointout
						ctx.Client.RecallPointOut(fp.ACID, errCB)
						return CommandStatus{}
					}

				}
				// 6.12.5 Clear pointout color (implied)
				if anno.PointOutAcknowledged {
					anno.PointOutAcknowledged = false
					writeAnno()
					return CommandStatus{}
				}

				// 6.12.10 Clear reject / cancel pointout indication
				if _, ok := sp.RejectedPointOuts[fp.ACID]; ok {
					delete(sp.RejectedPointOuts, fp.ACID)
					return CommandStatus{}
				}
			}

			// 7.3 Acknowledge CA / MSAW / SPC / FMA track
			if idx := slices.IndexFunc(sp.CAAircraft, func(ca CAAircraft) bool {
				return (ca.ADSBCallsigns[0] == trk.ADSBCallsign || ca.ADSBCallsigns[1] == trk.ADSBCallsign) &&
					!ca.Acknowledged
			}); idx != -1 {
				sp.CAAircraft[idx].Acknowledged = true
				return CommandStatus{}
			}
			if idx := slices.IndexFunc(sp.MCIAircraft, func(ca CAAircraft) bool {
				return ca.ADSBCallsigns[0] == trk.ADSBCallsign && !ca.Acknowledged
			}); idx != -1 {
				sp.MCIAircraft[idx].Acknowledged = true
				return CommandStatus{}
			}
			if anno.MSAW && !anno.MSAWAcknowledged {
				anno.MSAWAcknowledged = true
				writeAnno()
				return CommandStatus{}
			}
			if anno.SPCAlert && !anno.SPCAcknowledged {
				// Acknowledged SPC alert part 1
				anno.SPCAcknowledged = true
				writeAnno()
				return CommandStatus{}
			}
			// 6.13.31 Remove full data block forced by special condition
			if anno.DatablockAlert {
				anno.DatablockAlert = false
				writeAnno()
				return CommandStatus{}
			}

			// TODO: stop blinking canceled FP  indicator

			if trk.IsAssociated() {
				fp := trk.FlightPlan

				// 5.6.1 Change ABC to RBC for track in mismatch (implied)
				if !trk.IsUnsupportedDB() && trk.Squawk != fp.AssignedSquawk {
					spec := sim.FlightPlanSpecifier{}
					spec.ACID.Set(fp.ACID)
					spec.ImplicitSquawkAssignment.Set(trk.Squawk)
					modifyFlightPlan(sp, ctx, fp.ACID, spec, false)
					return CommandStatus{}
				}

				if fp.Suspended {
					anno.SuspendedShowAltitudeEndTime = ctx.SimTime.Add(5 * time.Second)
					writeAnno()
					// 5.7.2 Display suspended track's flight plan in preview area
					return CommandStatus{Output: formatFlightPlan(sp, ctx, trk.FlightPlan, trk)}
				}

				// 5.6.4 Inhibit duplicate beacon code indicator (implied)
				if _, ok := sp.DuplicateBeacons[trk.Squawk]; ok && anno.DBAcknowledged != trk.Squawk {
					anno.DBAcknowledged = trk.Squawk
					writeAnno()
					return CommandStatus{Output: formatFlightPlan(sp, ctx, trk.FlightPlan, trk)}
				}
			}

			// TODO: Remove ADS−B duplicate target address indicator

			// TODO: Remove blinking Resume flight progress indicator

			// 5.6.5 Remove blinking indicators and/or inhibit blinking full data block (implied)
			if anno.IFFlashing {
				anno.IFFlashing = false
				writeAnno()
			}

			// Inhibit blinking data block at former local owner’s TCW/TDW

			// TODO: Remove ACID/Target ID mismatch indicator

			// TODO: Remove ADS−B data loss indicator

			// 6.13.20 Return data block to unowned color (implied)
			if anno.OutboundHandoffAccepted {
				anno.OutboundHandoffAccepted = false
				anno.OutboundHandoffFlashEnd = ctx.SimTime
				anno.RDIndicatorEnd = sim.Time{}
				writeAnno()
				return CommandStatus{}
			}

			// Take control of interfacility track

			// TODO: Remove frozen flight from display

			// TODO: Acknowledge Time Based Flow Management (TBFM) runway mismatch

			if trk.IsAssociated() {
				fp := trk.FlightPlan

				if anno.ForceQL {
					// Clear force ql to self. (Not listed in the precedence table)
					anno.ForceQL = false
					writeAnno()
					return CommandStatus{}
				} else if _, ok := sp.ForceQLACIDs[fp.ACID]; ok {
					delete(sp.ForceQLACIDs, fp.ACID)
					return CommandStatus{}
				} else if ctx.UserOwnsFlightPlan(fp) {
					// 6.13.3 Beacon readout - owned and associated track (implied)
					rbc := util.Select(trk.Mode == av.TransponderModeStandby, "    ", trk.Squawk.String())
					return CommandStatus{Output: string(fp.ACID) + " " + rbc + " " + fp.AssignedSquawk.String()}
				} else if fp.SPCOverride != "" && !anno.SPCAcknowledged {
					// Remove FDB forced by SPC
					// Acknowledged SPC alert part 2
					anno.SPCAcknowledged = true
					writeAnno()
					return CommandStatus{}
				} else {
					// 6.13.4 Toggle quick look for a single track (implied)
					anno.DisplayFDB = !anno.DisplayFDB
					writeAnno()
					return CommandStatus{}
				}
			}

			// TODO: Create FP and associate to LDB with blinking ACID or frozen SPC

			// Inhibit No flight plan alert for unassociated track
			// 6.13.25 Inhibit no flight plan alert for unassociated track
			if trk.MissingFlightPlan && !anno.MissingFlightPlanAcknowledged {
				anno.MissingFlightPlanAcknowledged = true
				writeAnno()
				return CommandStatus{}
			}

			// Beacon Readout −− unassociated track
			if trk.IsUnassociated() {
				// 6.13.2 Beacon readout - unassociated track (implied)
				s := ctx.FacilityAdaptation.Datablocks.LDB.FullSeconds
				if s == 0 {
					s = 5
				}
				anno.FullLDBEndTime = ctx.SimTime.Add(time.Duration(s) * time.Second)
				writeAnno()
			}

			return CommandStatus{}
		})
}
