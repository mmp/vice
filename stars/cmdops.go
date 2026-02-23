// stars/cmdops.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 5 of the TCW Operator Manual

package stars

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func registerOpsCommands() {
	// 5.1.1 Initiate handoff (implied)
	// 5.1.5 Redirect handoff (implied)
	handoffOrRedirectTrack := func(sp *STARSPane, ctx *panes.Context, tcp string, trk *sim.Track) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}

		fp := trk.FlightPlan
		ctrl := lookupControllerWithAirspace(ctx, tcp, trk)
		if ctrl == nil {
			return ErrSTARSIllegalPosition
		}

		// If handoff is in progress TO user, redirect it instead
		// Pass the original targetTCP so HandoffTrackController records the actual target position
		if ctx.UserControlsPosition(fp.HandoffController) || ctx.UserControlsPosition(fp.RedirectedHandoff.RedirectedTo) {
			ctx.Client.RedirectHandoff(fp.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
		} else {
			ctx.Client.HandoffTrack(fp.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
		}

		return nil
	}
	registerCommand(CommandModeNone, "[TCP1][SLEW]|[TCP2][SLEW]|[TCP_TRI][SLEW]|[ARTCC][SLEW]", handoffOrRedirectTrack)

	// 5.1.7 Enable / inhibit automatic handoff for a flight
	// registerCommand(CommandModeNone, STARSTriangleCharater+"[SLEW]", ...)

	// 5.1.9 Initiate intrafacility handoff (p. 5-18)
	// 5.1.16 Transfer track ownership to another keyboard
	registerCommand(CommandModeHandOff, "[TCP1][SLEW]|[TCP2][SLEW]", handoffOrRedirectTrack)
	registerCommand(CommandModeHandOff, "[TCP1] [TRK_ACID]|[TCP2] [TRK_ACID]", handoffOrRedirectTrack)
	registerCommand(CommandModeHandOff, "[TCP1] [TRK_BCN]|[TCP2] [TRK_BCN]", handoffOrRedirectTrack)

	// 5.1.10 Accept handoff (p. 5-20) ([SLEW] variants are handled in cmdslew.go)
	// 5.1.17 Recall handoff (p. 5-33)
	// 5.1.19 Recall redirected handoff
	registerCommand(CommandModeHandOff, "[TRK_ACID]|[TRK_BCN]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			acid := trk.FlightPlan.ACID
			if ctx.UserControlsPosition(trk.FlightPlan.HandoffController) {
				// 5.1.10 Accept inbound handoff
				ctx.Client.AcceptHandoff(acid, func(err error) { sp.displayError(err, ctx, "") })
			} else {
				ctx.Client.CancelHandoff(acid, func(err error) { sp.displayError(err, ctx, "") })
			}
			return nil
		})

	// 5.1.11 Accept handoff of track in intrafacility point out
	// ** Handled with **[SLEW] for QL to self under 6.12.6 in cmdtools.go **

	// 5.1.12 Accept handoff of track closest to range ring default center (p. 5-23)
	registerCommand(CommandModeHandOff, "", func(sp *STARSPane, ctx *panes.Context, ps *Preferences) CommandStatus {
		var closest *sim.Track
		var closestDistance float32
		for _, trk := range sp.visibleTracks {
			if trk.IsUnassociated() || !ctx.UserControlsPosition(trk.FlightPlan.HandoffController) {
				continue
			}
			ctr := util.Select(ps.UseUserRangeRingsCenter, ps.RangeRingsUserCenter, ps.DefaultCenter)
			state := sp.TrackState[trk.ADSBCallsign]
			d := math.NMDistance2LL(ctr, state.track.Location)
			if closest == nil || d < closestDistance {
				closest = &trk
				closestDistance = d
			}
		}
		if closest != nil {
			// TODO: ILL TRK if AMB / TRK / OLD, or CST...
			ctx.Client.AcceptHandoff(closest.FlightPlan.ACID, func(err error) { sp.displayError(err, ctx, "") })
			return CommandStatus{}
		}
		return CommandStatus{Output: "NO FLIGHT"}
	})

	// 5.1.13 Initiate handoff to ARTCC
	registerCommand(CommandModeHandOff, "[ARTCC] [TRK_ACID]|[ARTCC] [TRK_BCN]|[ARTCC][SLEW]", handoffOrRedirectTrack)

	// 5.1.14 Initiate NAS FP handoff to adjacent tracon
	registerCommand(CommandModeHandOff, "[TCP_TRI] [TRK_ACID]|[TCP_TRI] [TRK_BCN]|[TCP_TRI][SLEW]", handoffOrRedirectTrack)

	// 5.1.20 Enable / inhibit automatic handoff of a flight
	// registerCommand(UserCommand{M: CommandModeHandOff, C: STARSTriangleCharacter + "[SLEW]|" + STARSTriangleCharacter + " [TRK_ACID]"})

	// 5.2.1 Create coordination message
	// 5.2.2 Modify coordination message text
	// registerCommand(UserCommand{M: CommandModeReleaseDeparture, C: "[FIELD] [TEXT]"})

	// 5.2.1 Create coordination message
	// registerCommand(UserCommand{M: CommandModeReleaseDeparture, C: "[FIELD]|[FIELD] [NUM] [TEXT]"})

	// 5.2.3 Send coordination message
	// 5.2.7 Recall coordination message
	// registerCommand(UserCommand{M: CommandModeReleaseDeparture, C: "[FIELD] /"})

	// 5.2.4 Re-order coordination message
	// registerCommand(UserCommand{M: CommandModeReleaseDeparture, C: "[FIELD] [NUM]"})

	// 5.2.5 Acknowledge coordination message (p. 5-46)
	registerCommand(CommandModeReleaseDeparture, "", // release single departure
		func(sp *STARSPane, ctx *panes.Context) error {
			rel := ctx.Client.State.GetSTARSReleaseDepartures()

			// Filter out the ones that have been released and then deleted
			// from the coordination list by the controller.
			rel = slices.DeleteFunc(rel,
				func(dep sim.ReleaseDeparture) bool {
					state, ok := sp.TrackState[dep.ADSBCallsign]
					return ok && state.ReleaseDeleted
				})

			// If there is only one unacknowledged, then ack/release it.
			unack := slices.DeleteFunc(rel, func(dep sim.ReleaseDeparture) bool { return dep.Released })
			switch len(unack) {
			case 0:
				return ErrSTARSIllegalFlight
			case 1:
				ctx.Client.ReleaseDeparture(unack[0].ADSBCallsign,
					func(err error) { sp.displayError(err, ctx, "") })
				return nil
			default:
				return ErrSTARSMultipleFlights
			}
		})
	// release specific departure
	lookupDeparture := func(sp *STARSPane, ctx *panes.Context, pred func(dep sim.ReleaseDeparture) bool) *sim.ReleaseDeparture {
		rel := ctx.Client.State.GetSTARSReleaseDepartures()

		// Filter out the ones that have been released and then deleted
		// from the coordination list by the controller.
		rel = slices.DeleteFunc(rel,
			func(dep sim.ReleaseDeparture) bool {
				state, ok := sp.TrackState[dep.ADSBCallsign]
				return ok && state.ReleaseDeleted
			})

		for i, dep := range rel {
			if pred(dep) {
				return &rel[i]
			}
		}
		return nil
	}
	doRelease := func(sp *STARSPane, ctx *panes.Context, dep *sim.ReleaseDeparture) error {
		if !dep.Released {
			dep.Released = true // hack for instant update pending the next server update
			ctx.Client.ReleaseDeparture(dep.ADSBCallsign,
				func(err error) { sp.displayError(err, ctx, "") })
		} else if ts := sp.TrackState[dep.ADSBCallsign]; ts != nil {
			ts.ReleaseDeleted = true
		}
		return nil
	}
	registerCommand(CommandModeReleaseDeparture, "[ACID]",
		func(sp *STARSPane, ctx *panes.Context, acid sim.ACID) error {
			dep := lookupDeparture(sp, ctx,
				func(dep sim.ReleaseDeparture) bool { return dep.ADSBCallsign == av.ADSBCallsign(acid) })

			if dep == nil {
				if trk, ok := ctx.GetTrackByACID(acid); ok {
					// There is such a flight but it's not in our release list.
					if trk.HoldForRelease {
						// It's in another controller's list
						return ErrSTARSIllegalFunction
					} else {
						return ErrSTARSIllegalFlight
					}
				} else {
					// No such flight anywhere.
					return ErrSTARSNoFlight
				}
			}
			return doRelease(sp, ctx, dep)
		})
	registerCommand(CommandModeReleaseDeparture, "[BCN]",
		func(sp *STARSPane, ctx *panes.Context, sq av.Squawk) error {
			dep := lookupDeparture(sp, ctx, func(dep sim.ReleaseDeparture) bool { return dep.Squawk == sq })

			if dep == nil {
				for _, trk := range sp.visibleTracks {
					if trk.Squawk == sq {
						// There is such a flight but it's not in our release list.
						if trk.HoldForRelease {
							// It's in another controller's list
							return ErrSTARSIllegalFunction
						} else {
							return ErrSTARSIllegalFlight
						}
					}
				}
				return ErrSTARSNoFlight
			}
			return doRelease(sp, ctx, dep)
		})
	registerCommand(CommandModeReleaseDeparture, "[NUM]",
		func(sp *STARSPane, ctx *panes.Context, idx int) error {
			dep := lookupDeparture(sp, ctx,
				func(dep sim.ReleaseDeparture) bool {
					return dep.ListIndex != sim.UnsetSTARSListIndex && dep.ListIndex == idx
				})

			if dep == nil {
				return ErrSTARSIllegalLine
			}
			return doRelease(sp, ctx, dep)
		})

	// 5.2.6 Delete coordination message (p. 5-48)
	// registerCommand(UserCommand{M: CommandModeReleaseDeparture, C: "[FIELD]"}) // Note interaction with 5.2.5. This is only valid for sender also..

	// 5.2.8 Enable / inhibit coordination message auto-acknowledge (p. 5-51)
	registerCommand(CommandModeReleaseDeparture, "P[FIELD] A*", func(ps *Preferences, listID string) error {
		cl := ps.CoordinationLists[listID]
		if cl == nil {
			return ErrSTARSIllegalFunction
		}
		cl.AutoRelease = true
		return nil
	})
	registerCommand(CommandModeReleaseDeparture, "P[FIELD] M*", func(ps *Preferences, listID string) error {
		cl := ps.CoordinationLists[listID]
		if cl == nil {
			return ErrSTARSIllegalFunction
		}
		cl.AutoRelease = false
		return nil
	})

	// 5.2.9 Enable / inhibit display of Coordination list title (p. 5-52)
	registerCommand(CommandModeReleaseDeparture, "T", func(ps *Preferences) {
		ps.DisplayEmptyCoordinationLists = !ps.DisplayEmptyCoordinationLists
	})
	registerCommand(CommandModeReleaseDeparture, "TE", func(ps *Preferences) {
		ps.DisplayEmptyCoordinationLists = true
	})
	registerCommand(CommandModeReleaseDeparture, "TI", func(ps *Preferences) {
		ps.DisplayEmptyCoordinationLists = false
	})

	// 5.3.1 Modify system altimeter setting
	// {(CommandModeMultiFunc,  "S[NUM]")

	// 5.3.2 Modify regional or airport altimeter setting
	// {(CommandModeMultiFunc,  "A[FIELD] [NUM]|A[FIELD] A|A[FIELD] [NUM]E")

	// 5.3.3 Create or modify ATIS code and main Gen. info text (p. 5-58)
	registerCommand(CommandModeMultiFunc, "S[FIELD:1]", func(ps *Preferences, alpha string) error {
		if !isAlpha(alpha[0]) {
			return ErrSTARSIllegalATIS
		}
		ps.ATIS[0] = alpha
		return nil
	})
	registerCommand(CommandModeMultiFunc, "S[FIELD:1][ALL_TEXT]", func(ps *Preferences, alpha string, text string) error {
		if !isAlpha(alpha[0]) {
			return ErrSTARSIllegalATIS
		}
		ps.ATIS[0] = alpha
		ps.GIText[0] = text
		return nil
	})

	// 5.3.4 Delete system ATIS code and enter new main gen. info text
	registerCommand(CommandModeMultiFunc, "S*", func(ps *Preferences) {
		ps.ATIS[0] = ""
	})
	registerCommand(CommandModeMultiFunc, "S* [ALL_TEXT]", func(ps *Preferences, text string) {
		ps.ATIS[0] = ""
		ps.GIText[0] = text
	})

	// 5.3.5 Delete main gen. info text and enter new ATIS code
	registerCommand(CommandModeMultiFunc, "S[FIELD:1]*", func(ps *Preferences, alpha string) error {
		if !isAlpha(alpha[0]) {
			return ErrSTARSIllegalATIS
		}
		ps.ATIS[0] = alpha
		ps.GIText[0] = ""
		return nil
	})

	// 5.3.6 Delete main Gen. info text and ATIS code (p. 5-62)
	registerCommand(CommandModeMultiFunc, "S", func(ps *Preferences) {
		ps.ATIS[0] = ""
		ps.GIText[0] = ""
	})

	// 5.3.7 Create or modify auxiliary gen. info text and ATIS code
	registerCommand(CommandModeMultiFunc, "S[#] [FIELD:1] [ALL_TEXT]",
		func(ps *Preferences, line int, alpha, text string) error {
			if line == 0 {
				return ErrSTARSIllegalLine
			}
			if !isAlpha(alpha[0]) {
				return ErrSTARSIllegalATIS
			}
			ps.ATIS[line] = alpha
			ps.GIText[line] = text
			return nil
		})
	registerCommand(CommandModeMultiFunc, "S[#] [ALL_TEXT]", func(ps *Preferences, line int, text string) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		ps.GIText[line] = text
		return nil
	})

	// 5.3.8 Create or modify auxiliary ATIS code
	registerCommand(CommandModeMultiFunc, "S[#] [FIELD:1]", func(ps *Preferences, line int, alpha string) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		if !isAlpha(alpha[0]) {
			return ErrSTARSIllegalATIS
		}
		ps.ATIS[line] = alpha
		return nil
	})

	// 5.3.9 Delete auxiliary ATIS code and enter new auxiliary gen. info text
	registerCommand(CommandModeMultiFunc, "S[#] *", func(ps *Preferences, line int) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		ps.ATIS[line] = ""
		return nil
	})
	registerCommand(CommandModeMultiFunc, "S[#] * [ALL_TEXT]", func(ps *Preferences, line int, text string) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		ps.ATIS[line] = ""
		ps.GIText[line] = text
		return nil
	})

	// 5.3.10 Delete auxiliary gen. info text and enter new auxiliary ATIS code
	registerCommand(CommandModeMultiFunc, "S[#] [FIELD:1]*", func(ps *Preferences, line int, alpha string) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		if !isAlpha(alpha[0]) {
			return ErrSTARSIllegalATIS
		}
		ps.ATIS[line] = alpha
		ps.GIText[line] = ""
		return nil
	})

	// 5.3.11 Delete auxiliary Gen. info text and ATIS code
	registerCommand(CommandModeMultiFunc, "S[#]", func(ps *Preferences, line int) error {
		if line == 0 {
			return ErrSTARSIllegalLine
		}
		ps.ATIS[line] = ""
		ps.GIText[line] = ""
		return nil
	})

	// 5.3.13 Stop blinking ATIS and gen. info text
	// registerCommand(CommandModeNone, STARSTriangleCharacter, ...)

	// 5.4.1 Activate FP and associate or create Unsupported data block (Implied command)
	registerCommand(CommandModeNone, "[UNASSOC_FP][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, trk *sim.Track) error {
			if trk.IsAssociated() {
				return ErrSTARSIllegalTrack
			}
			var spec sim.FlightPlanSpecifier
			spec.TrackingController.Set(ctx.UserPrimaryPosition())
			ctx.Client.ActivateFlightPlan(trk.ADSBCallsign, fp.ACID, spec,
				func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})
	registerCommand(CommandModeNone, "[UNASSOC_FP][POS]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, pos math.Point2LL) {
			var spec sim.FlightPlanSpecifier
			spec.Location.Set(pos)
			modifyFlightPlan(sp, ctx, fp.ACID, spec, false)
		})

	// 5.4.2 Activate existing flight plan or create Unsupported data block
	registerCommand(CommandModeInitiateControl, "[UNASSOC_FP] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A][POS]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, spec sim.FlightPlanSpecifier, pos math.Point2LL) {
			spec.Location.Set(pos)
			spec.TrackingController.Set(ctx.UserPrimaryPosition())
			modifyFlightPlan(sp, ctx, fp.ACID, spec, false)
		})
	registerCommand(CommandModeInitiateControl, "[UNASSOC_FP] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, spec sim.FlightPlanSpecifier, trk *sim.Track) error {
			if trk.IsAssociated() {
				return ErrSTARSIllegalTrack
			}
			spec.TrackingController.Set(ctx.UserPrimaryPosition())
			ctx.Client.ActivateFlightPlan(trk.ADSBCallsign, fp.ACID, spec,
				func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})
	registerCommand(CommandModeInitiateControl, "[UNASSOC_FP][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, trk *sim.Track) error {
			if trk.IsAssociated() {
				return ErrSTARSIllegalTrack
			}
			var spec sim.FlightPlanSpecifier
			spec.TrackingController.Set(ctx.UserPrimaryPosition())
			ctx.Client.ActivateFlightPlan(trk.ADSBCallsign, fp.ACID, spec,
				func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})

	// 5.4.3 Suspend flight plan (p. 5-75)
	registerCommand(CommandModeTrackSuspend, "[TRK_ACID]|[TRK_BCN]|[TRK_INDEX]|[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			} else if state := sp.TrackState[trk.ADSBCallsign]; state.MSAW || state.SPCAlert {
				return ErrSTARSIllegalTrack
			} else if slices.ContainsFunc(sp.CAAircraft,
				func(ca CAAircraft) bool {
					return ca.ADSBCallsigns[0] == trk.ADSBCallsign || ca.ADSBCallsigns[1] == trk.ADSBCallsign
				}) {
				return ErrSTARSIllegalTrack
			} else if slices.ContainsFunc(sp.MCIAircraft,
				func(mci CAAircraft) bool { return mci.ADSBCallsigns[0] == trk.ADSBCallsign }) {
				return ErrSTARSIllegalTrack
			} else {
				var spec sim.FlightPlanSpecifier
				spec.Suspended.Set(true)
				spec.CoastSuspendIndex.Set(sp.CoastSuspendIndex % 100)
				if trk.FlightPlan.Rules == av.FlightRulesIFR {
					// Suspending the flight plan clears these
					spec.DisableMSAW.Set(false)
					spec.DisableCA.Set(false)
				}
				sp.CoastSuspendIndex++
				modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* don't display */)
				return nil
			}
		})

	// 5.4.4 Unsuspend flight plan (p. 5-77)
	unsuspendFP := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() || !trk.FlightPlan.Suspended {
			return ErrSTARSIllegalTrack
		}
		var spec sim.FlightPlanSpecifier
		spec.Suspended.Set(false)
		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	}
	registerCommand(CommandModeInitiateControl, "[TRK_ACID]|[TRK_BCN]|[TRK_INDEX_SUSPENDED]", unsuspendFP)

	unsuspendAndUpdateFP := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) error {
		if trk.IsUnassociated() || !trk.FlightPlan.Suspended {
			return ErrSTARSIllegalTrack
		}
		spec.Suspended.Set(false)
		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	}
	registerCommand(CommandModeInitiateControl, "[TRK_ACID] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2]", unsuspendAndUpdateFP)
	registerCommand(CommandModeInitiateControl, "[TRK_BCN] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2]", unsuspendAndUpdateFP)
	registerCommand(CommandModeInitiateControl, "[TRK_INDEX_SUSPENDED] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2]", unsuspendAndUpdateFP)
	registerCommand(CommandModeInitiateControl, "[*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2][SLEW]", unsuspendAndUpdateFP)

	registerCommand(CommandModeInitiateControl, "[TRK_ACID][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.ACID != trk2.FlightPlan.ACID {
				return ErrSTARSIllegalTrack
			}
			return unsuspendFP(sp, ctx, trk1)
		})
	registerCommand(CommandModeInitiateControl, "[TRK_BCN][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.AssignedSquawk != trk2.FlightPlan.AssignedSquawk {
				return ErrSTARSIllegalTrack
			}
			return unsuspendFP(sp, ctx, trk1)
		})
	registerCommand(CommandModeInitiateControl, "[TRK_INDEX_SUSPENDED][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.ACID != trk2.FlightPlan.ACID {
				return ErrSTARSIllegalTrack
			}
			return unsuspendFP(sp, ctx, trk1)
		})
	registerCommand(CommandModeInitiateControl, "[TRK_ACID] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track, spec sim.FlightPlanSpecifier) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.CoastSuspendIndex != trk2.FlightPlan.CoastSuspendIndex {
				return ErrSTARSIllegalTrack
			}
			return unsuspendAndUpdateFP(sp, ctx, trk1, spec)
		})
	registerCommand(CommandModeInitiateControl, "[TRK_BCN] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track, spec sim.FlightPlanSpecifier) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.ACID != trk2.FlightPlan.ACID {
				return ErrSTARSIllegalTrack
			}
			return unsuspendAndUpdateFP(sp, ctx, trk1, spec)
		})
	registerCommand(CommandModeInitiateControl, "[TRK_INDEX_SUSPENDED] [*FP_SP1|FP_TRI_SP1|FP_PLUS_SP2][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk1 *sim.Track, trk2 *sim.Track, spec sim.FlightPlanSpecifier) error {
			if trk1.IsUnassociated() || trk2.IsUnassociated() || trk1.FlightPlan.CoastSuspendIndex != trk2.FlightPlan.CoastSuspendIndex {
				return ErrSTARSIllegalTrack
			}
			return unsuspendAndUpdateFP(sp, ctx, trk1, spec)
		})

	// 5.4.5 Toggle hold state for flight plan
	registerCommand(CommandModeMultiFunc, "ZZ [TRK_ACID]|ZZ [TRK_BCN]|ZZ [TRK_INDEX]|ZZ[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSNoFlight
			}
			if trk.FlightPlan.Suspended || trk.FlightPlan.HandoffController != "" {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.HoldState.Set(!trk.FlightPlan.HoldState)
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})

	// 5.4.6 Delete single flight plan (p. 5-83)
	registerCommand(CommandModeTerminateControl, "[TRK_ACID]|[TRK_BCN]|[TRK_INDEX]|[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if !trk.IsAssociated() && !trk.IsUnsupportedDB() {
				return ErrSTARSIllegalTrack
			}
			ctx.Client.DeleteFlightPlan(trk.FlightPlan.ACID, func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})
	registerCommand(CommandModeTerminateControl, "[TRK_ACID] [TIME]|[TRK_BCN] [TIME]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, _ string) error {
			// TODO? Use specified coordination time
			if !trk.IsAssociated() && !trk.IsUnsupportedDB() {
				return ErrSTARSIllegalTrack
			}
			ctx.Client.DeleteFlightPlan(trk.FlightPlan.ACID, func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})
	registerCommand(CommandModeTerminateControl, "[UNASSOC_FP]", func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan) {
		ctx.Client.DeleteFlightPlan(fp.ACID, func(err error) { sp.displayError(err, ctx, "") })
	})
	// This runs if the above one can't find a flight plan...
	registerCommand(CommandModeTerminateControl, "[FIELD]", func(string) error {
		return ErrSTARSNoFlight
	})

	// 5.4.7 Delete VFR flight plan from VFR FP list (p. 5-85)
	registerCommand(CommandModeVFRPlan, "[FIELD]",
		func(sp *STARSPane, ctx *panes.Context, acid string) error {
			fps := ctx.Client.State.UnassociatedFlightPlans
			if idx := slices.IndexFunc(fps, func(fp *sim.NASFlightPlan) bool {
				return fp.ACID == sim.ACID(acid) && fp.Rules == av.FlightRulesVFR
			}); idx != -1 {
				ctx.Client.DeleteFlightPlan(fps[idx].ACID, func(err error) { sp.displayError(err, ctx, "") })
				return nil
			}
			return ErrSTARSIllegalTrack
		})
	registerCommand(CommandModeVFRPlan, "[NUM]",
		func(sp *STARSPane, ctx *panes.Context, index int) error {
			fps := ctx.Client.State.UnassociatedFlightPlans
			if idx := slices.IndexFunc(fps, func(fp *sim.NASFlightPlan) bool {
				return fp.ListIndex == index && fp.ListIndex != sim.UnsetSTARSListIndex && fp.Rules == av.FlightRulesVFR
			}); idx != -1 {
				ctx.Client.DeleteFlightPlan(fps[idx].ACID, func(err error) { sp.displayError(err, ctx, "") })
				return nil
			}
			return ErrSTARSIllegalTrack
		})

	// 5.4.8 Delete a TCP's flight plans (p. 5-86)
	registerCommand(CommandModeTerminateControl, "ALL", func(sp *STARSPane, ctx *panes.Context) {
		for _, trk := range sp.visibleTracks {
			if (trk.IsAssociated() || trk.IsUnsupportedDB()) && ctx.UserOwnsFlightPlan(trk.FlightPlan) {
				ctx.Client.DeleteFlightPlan(trk.FlightPlan.ACID, func(err error) { sp.displayError(err, ctx, "") })
			}
		}
	})

	// 5.4.9 Delete a TCP's passive and pending flight plans
	// registerCommand(CommandModeMultiFunc, "T[TIME]", ...)

	// 5.5.1 Create abbreviated flight plan (Implied command)
	createAbbrevFP := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		spec.PlanType.Set(sim.LocalNonEnroute)
		createFlightPlan(sp, ctx, spec)
	}
	registerCommand(CommandModeNone, "[FP_ACID]", createAbbrevFP)
	registerCommand(CommandModeNone, "[FP_ACID] [*FP_BEACON|FP_TCP|FP_FLT_TYPE|FP_TRI_SP1|FP_PLUS_SP2|FP_NUM_ACTYPE|FP_ALT_R|FP_RULES]", createAbbrevFP)

	// 5.5.2 Create / modify interfacility VFR FP and send FP message to ARTCC (Implied command)
	registerCommand(CommandModeNone, "[FP_ACID] [FP_VFR_FIXES][FP_ACTYPE][?FP_ALT_R][?FP_TCP]",
		func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
			spec.Rules.Set(av.FlightRulesVFR)
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			spec.PlanType.Set(sim.LocalEnroute)
			createFlightPlan(sp, ctx, spec)
		})

	// 5.5.3 Create FP and associate or create Unsupported data block (Implied command) (p. 5-99)
	createFPAndAssociate := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, trk *sim.Track) error {
		if trk.IsAssociated() {
			return ErrSTARSIllegalTrack
		}
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		if !spec.SquawkAssignment.IsSet {
			spec.ImplicitSquawkAssignment.Set(trk.Squawk)
		}
		associateFlightPlan(sp, ctx, trk.ADSBCallsign, spec)
		return nil
	}
	registerCommand(CommandModeNone, "[FP_ACID][SLEW]", createFPAndAssociate)
	registerCommand(CommandModeNone, "[FP_ACID] [*FP_BEACON|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A|FP_NUM_ACTYPE][SLEW]", createFPAndAssociate)

	createUnsupportedDB := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, p math.Point2LL) {
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		spec.Location.Set(p)
		createFlightPlan(sp, ctx, spec)
	}
	registerCommand(CommandModeNone, "[FP_ACID][POS]", createUnsupportedDB)
	registerCommand(CommandModeNone, "[FP_ACID] [*FP_BEACON|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A|FP_NUM_ACTYPE][POS]", createUnsupportedDB)

	// 5.5.4 Create FP and associate to LDB with blinking ACID or frozen SPC (implied)
	// {C: "[*FP_ACID|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A|FP_NUM_ACTYPE][SLEW]" }

	// 5.5.5 Create flight plan (p. 5-109)
	registerCommand(CommandModeFlightData, "[FP_ACID]",
		func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			spec.PlanType.Set(sim.LocalNonEnroute)
			createFlightPlan(sp, ctx, spec)
		})
	registerCommand(CommandModeFlightData, "[FP_ACID] [*FP_BEACON|FP_TCP|FP_FIX_PAIR|FP_COORD_TIME|FP_TRI_SP1|FP_PLUS_SP2|FP_NUM_ACTYPE|FP_ALT_R|FP_RULES]",
		func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			spec.PlanType.Set(sim.LocalNonEnroute)
			createFlightPlan(sp, ctx, spec)
		})

	// 5.5.6 Create Departure FP with DM indicator
	// registerCommand(CommandModeFlightData, "* [FP_ACID]", ...)
	// registerCommand(CommandModeFlightData, "* [FP_ACID] [*FP_BEACON|FP_EXIT_FIX|FP_COORD_TIME|FP_TRI_SP1|FP_PLUS_SP2|FP_NUM_ACTYPE|FP_ALT_R]"", ...)

	// 5.5.7 Create pending FP with discrete beacon code (p. 5-120)
	createPendingFPWithBeacon := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		spec.PlanType.Set(sim.LocalNonEnroute)
		createFlightPlan(sp, ctx, spec)
	}
	registerCommand(CommandModeInitiateControl, "[FP_ACID] [FP_BEACON]", createPendingFPWithBeacon)
	registerCommand(CommandModeInitiateControl, "[FP_ACID] [FP_BEACON] [*FP_TRI_SP1|FP_PLUS_SP2|FP_NUM_ACTYPE]", createPendingFPWithBeacon)

	// 5.5.8 Create active FP with discrete beacon code (p. 5-124)
	createActiveFPWithBeacon := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, trk *sim.Track) error {
		if trk.IsAssociated() {
			return ErrSTARSIllegalTrack
		}
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		if !spec.SquawkAssignment.IsSet {
			spec.ImplicitSquawkAssignment.Set(trk.Squawk)
		}
		associateFlightPlan(sp, ctx, trk.ADSBCallsign, spec)
		return nil
	}
	registerCommand(CommandModeInitiateControl, "[FP_ACID][SLEW]", createActiveFPWithBeacon)
	registerCommand(CommandModeInitiateControl, "[FP_ACID] [*FP_BEACON|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A|FP_NUM_ACTYPE][SLEW]",
		createActiveFPWithBeacon)

	// 5.5.9 Create active FP and Unsupported data block (p. 5-129)
	createActiveFPAndUnsupportedDB := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, pos math.Point2LL) {
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		spec.Location.Set(pos)
		spec.PlanType.Set(sim.LocalNonEnroute)
		createFlightPlan(sp, ctx, spec)
	}
	registerCommand(CommandModeInitiateControl, "[FP_ACID][POS]", createActiveFPAndUnsupportedDB)
	registerCommand(CommandModeInitiateControl, "[FP_ACID] [*FP_BEACON|FP_TRI_SP1|FP_PLUS_SP2|FP_ALT_A|FP_NUM_ACTYPE][POS]",
		createActiveFPAndUnsupportedDB)

	// 5.5.10 Create / modify VFR FP and send FP message to ARTCC (p. 5-133)
	createModifyVFRFP := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
		spec.Rules.Set(av.FlightRulesVFR)
		spec.TypeOfFlight.Set(av.FlightTypeArrival)
		spec.PlanType.Set(sim.LocalEnroute)
		createFlightPlan(sp, ctx, spec)
	}
	registerCommand(CommandModeVFRPlan, "[FP_ACID] [FP_VFR_FIXES] [FP_ACTYPE]", createModifyVFRFP)
	registerCommand(CommandModeVFRPlan, "[FP_ACID] [FP_VFR_FIXES] [FP_ACTYPE] [*FP_ALT_R|FP_TCP]", createModifyVFRFP)

	// 5.5.11 Create flight plans on formation breakup
	// registerCommand(CommandModeInitiateControl, "STARSTriangleCharacter+" [TRK_ACID] ..." also TRK_INDEX, TRK_BCN)

	// 5.5.12 Create quick flight plan (Implied command) (p. 5-141)
	quickFPFromSquawk := func(sp *STARSPane, ctx *panes.Context, sq av.Squawk, rules av.FlightRules) error {
		base := ctx.FacilityAdaptation.FlightPlan.QuickACID
		acid := sim.ACID(base + sq.String())

		if _, ok := ctx.Client.State.GetTrackByACID(acid); ok {
			return ErrSTARSDuplicateACID
		}

		var spec sim.FlightPlanSpecifier
		spec.QuickFlightPlan.Set(true)
		spec.ACID.Set(acid)
		spec.SquawkAssignment.Set(sq.String())
		spec.TypeOfFlight.Set(av.FlightTypeOverflight)
		spec.CoordinationTime.Set(ctx.Now)
		spec.Rules.Set(rules)
		spec.PlanType.Set(sim.LocalNonEnroute)
		createFlightPlan(sp, ctx, spec)
		return nil
	}
	registerCommand(CommandModeNone, "*[BCN]|*[BCN]E", func(sp *STARSPane, ctx *panes.Context, sq av.Squawk) error {
		return quickFPFromSquawk(sp, ctx, sq, av.FlightRulesIFR)
	})
	registerCommand(CommandModeNone, "*[BCN]P|*[BCN]V", func(sp *STARSPane, ctx *panes.Context, sq av.Squawk) error {
		return quickFPFromSquawk(sp, ctx, sq, av.FlightRulesVFR)
	})

	// 5.5.13 Create interfacility VFR flight plan from active local track
	// registerCommand(CommandModeVFRPlan, "[SLEW]", ...)
	// registerCommand(CommandModeVFRPlan, "*[SLEW]", ...)
	// registerCommand(CommandModeVFRPlan, "*[FP_ALT_R][SLEW]", ...)
	// registerCommand(CommandModeVFRPlan, "[FP_ALT_R][SLEW]", ...)

	// 5.5.14 Create quick ACID flight plan (Implied command) (p. 5-145)
	registerCommand(CommandModeNone, "Y[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsAssociated() {
			return ErrSTARSIllegalTrack
		}
		var spec sim.FlightPlanSpecifier
		spec.QuickFlightPlan.Set(true)
		spec.Rules.Set(av.FlightRulesIFR)
		spec.TypeOfFlight.Set(av.FlightTypeOverflight)

		associateFlightPlan(sp, ctx, trk.ADSBCallsign, spec)

		state := sp.TrackState[trk.ADSBCallsign]
		state.DatablockAlert = true // yellow until slewed
		return nil
	})

	// 5.6.2 Add or modify aircraft type (Implied command) (p. 5-148)
	registerCommand(CommandModeNone, "[FP_NUM_ACTYPE4][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, trk *sim.Track) error {
			if !trk.IsAssociated() {
				return ErrSTARSCommandFormat
			}
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* don't display */)
			return nil
		})

	// 5.6.3 Add or modify scratchpad or altitude, and aircraft type (Implied command) (p. 5-150)
	modifyFPSlew := func(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier, trk *sim.Track) error {
		if !trk.IsAssociated() {
			return ErrSTARSIllegalTrack
		}

		// 5-166: pilot-reported altitude can't be entered if mode-c is available and it hasn't
		// been toggled off. (Though allow zero to clear pilot-reported altitude.)
		if trk.Mode == av.TransponderModeAltitude && !trk.FlightPlan.InhibitModeCAltitudeDisplay &&
			spec.PilotReportedAltitude.GetOr(0) != 0 {
			return ErrSTARSIllegalFunction
		}

		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	}
	registerCommand(CommandModeNone, "[FP_SP1|FP_ALT_P|FP_PLUS_SP2|FP_PLUS_ALT_A|FP_PLUS2_ALT_R][SLEW]", modifyFPSlew)
	registerCommand(CommandModeNone, "[FP_SP1|FP_ALT_P|FP_PLUS_SP2|FP_PLUS_ALT_A|FP_PLUS2_ALT_R] [FP_ACTYPE][SLEW]", modifyFPSlew)

	// 5.6.6 Inhibit blinking data block at former local owner's TCW/TDW (implied)

	// 5.6.7 Remove blinking resume flight progress indicator (implied)

	// 5.6.8 Delete scratchpad #1 (Implied command)
	registerCommand(CommandModeNone, ".[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.Scratchpad.Set("")
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})

	// 5.6.9 Delete runway text (scratchpad #2) (Implied command)
	registerCommand(CommandModeNone, "+[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.SecondaryScratchpad.Set("")
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})

	// 5.6.10 Add or modify scratchpad #1 (p. 5-159), 5.6.11 scratchpad 2 (p. 5-161), 5.6.13 pilot reported altitude (p. 5-165)
	addModSP12PilotAlt := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}

		// Mode-C validation (STARS Manual 5-166): Can't set non-zero pilot altitude
		// if mode-c is available and it hasn't been toggled off.
		// Exception: Allow zero to clear pilot-reported altitude.
		if trk.Mode == av.TransponderModeAltitude &&
			!trk.FlightPlan.InhibitModeCAltitudeDisplay &&
			spec.PilotReportedAltitude.GetOr(0) != 0 {
			return ErrSTARSIllegalFunction
		}

		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	}
	registerCommand(CommandModeMultiFunc, "Y[TRK_ACID] [FP_SP1|FP_PLUS_SP2|FP_ALT_P]", addModSP12PilotAlt)
	registerCommand(CommandModeMultiFunc, "Y[TRK_BCN] [FP_SP1|FP_PLUS_SP2|FP_ALT_P]", addModSP12PilotAlt)
	registerCommand(CommandModeMultiFunc, "Y[FP_SP1|FP_PLUS_SP2|FP_ALT_P][SLEW]", addModSP12PilotAlt)

	// 5.6.12 Delete scratchpad #1 or #2 (p. 5-163)
	registerCommand(CommandModeMultiFunc, "Y[TRK_ACID]|Y[TRK_BCN]|Y[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.PilotReportedAltitude.Set(0)
			spec.Scratchpad.Set("")
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})
	registerCommand(CommandModeMultiFunc, "Y+[TRK_ACID]|Y+[TRK_BCN]|Y+[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.SecondaryScratchpad.Set("")
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})

	// 5.6.14 Toggle display of Mode-C altitude
	registerCommand(CommandModeMultiFunc, "M[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			inhibit := !trk.FlightPlan.InhibitModeCAltitudeDisplay
			spec.InhibitModeCAltitudeDisplay.Set(inhibit)
			if trk.Mode == av.TransponderModeAltitude && !inhibit {
				// Clear pilot reported if inhibit toggled on and we have mode-C altitude
				spec.PilotReportedAltitude.Set(0)
			}
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* no display */)
			return nil
		})

	// 5.6.15 Release assigned beacon code from inactive or suspended flight plan
	// registerCommand(CommandModeMultiFunc, "B[FP_ACID]", ... )

	// 5.6.16 Mark non-active departure flight with blinking DM indicator
	// registerCommand(CommandModeMultiFunc, "M* [UNASSOC_FP???]", ... )

	// 5.6.17 Modify flight plan (p. 5-171)
	const modFPEntries = "[FP_ACID|FP_BEACON|FP_TCP|FP_COORD_TIME|FP_FIX_PAIR|FP_TRI_SP1|FP_PLUS_SP2|FP_TRI_ALT_A|FP_ALT_R]"
	modifyFP := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, spec sim.FlightPlanSpecifier) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}
		modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
		return nil
	}
	registerCommand(CommandModeMultiFunc, "M[TRK_ACID] "+modFPEntries, modifyFP)
	registerCommand(CommandModeMultiFunc, "M[TRK_BCN] "+modFPEntries, modifyFP)
	registerCommand(CommandModeMultiFunc, "M[TRK_INDEX] "+modFPEntries, modifyFP)
	registerCommand(CommandModeMultiFunc, "M"+modFPEntries+"[SLEW]", modifyFP)

	// 5.6.18 Modify RNAV symbol, a/c type, equipment suffix, or flight rules (p. 5-178)
	registerCommand(CommandModeMultiFunc, "H[TRK_ACID] [FP_RNAV|FP_NUM_ACTYPE|FP_RULES]", modifyFP)
	registerCommand(CommandModeMultiFunc, "H[TRK_BCN] [FP_RNAV|FP_NUM_ACTYPE|FP_RULES]", modifyFP)
	registerCommand(CommandModeMultiFunc, "H[TRK_INDEX] [FP_RNAV|FP_NUM_ACTYPE|FP_RULES]", modifyFP)
	registerCommand(CommandModeMultiFunc, "H[FP_RNAV|FP_NUM_ACTYPE|FP_RULES][SLEW]", modifyFP)

	// 5.6.19 Retransmit VFR FP message with amended fix data
	// note it's the VFR tab line number: is this different than TRK_INDEX? (actually, TRK_INDEX is n/a since fp is not associated...)

	// 5.6.20 Modify ACID (Implied command) (p. 5-183)
	registerCommand(CommandModeNone, "*[FP_ACID][SLEW]", modifyFP)

	// 5.7.1 Display flight plan in Preview area (p. 5-186)
	displayFlightPlan := func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, trk *sim.Track) (CommandStatus, error) {
		output := formatFlightPlan(sp, ctx, fp, trk)

		// Handle ModifyAfterDisplay mode transition (STARS Manual 5-186)
		// When enabled, after displaying a flight plan, the system automatically
		// enters M (modify) mode with the ACID pre-filled, allowing immediate modification.
		if ctx.FacilityAdaptation.FlightPlan.ModifyAfterDisplay {
			sp.multiFuncPrefix = "M"
			sp.previewAreaInput = string(fp.ACID) + " "
			return CommandStatus{Output: output, Clear: ClearNone}, nil
		}

		return CommandStatus{Output: output}, nil
	}
	registerCommand(CommandModeMultiFunc, "D[TRK_ACID]|D[TRK_BCN]|D[TRK_INDEX]|D[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
			if trk.IsUnassociated() {
				return CommandStatus{}, ErrSTARSNoFlight
			}
			return displayFlightPlan(sp, ctx, trk.FlightPlan, trk)
		})
	registerCommand(CommandModeMultiFunc, "D[UNASSOC_FP]",
		func(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan) (CommandStatus, error) {
			return displayFlightPlan(sp, ctx, fp, nil)
		})

	// 5.7.3 Reposition active track's (or unsupported) Full data block (p. 5-191)
	registerCommand(CommandModeTrackReposition, "[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
			if trk.IsUnassociated() || trk.FlightPlan.HandoffController != "" {
				return CommandStatus{}, ErrSTARSIllegalTrack
			}

			acid := trk.FlightPlan.ACID
			return CommandStatus{
				Clear: ClearNone,
				CommandHandlers: makeCommandHandlers(
					"[SLEW]", func(sp *STARSPane, ctx *panes.Context, dstTrk *sim.Track) error {
						// Associate fp with the second track
						if dstTrk.IsAssociated() {
							return ErrSTARSIllegalTrack
						}
						ctx.Client.RepositionTrack(acid, dstTrk.ADSBCallsign, math.Point2LL{},
							func(err error) { sp.displayError(err, ctx, "") })
						return nil
					},
					"[POS]", func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL) {
						// Make an unsupported datablock
						ctx.Client.RepositionTrack(acid, "", pos,
							func(err error) { sp.displayError(err, ctx, "") })
					},
				),
			}, nil
		})
	registerCommand(CommandModeTrackReposition, "[TRK_ACID][POS]|[TRK_BCN][POS]|[TRK_INDEX][POS]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, pos math.Point2LL) error {
			if trk.IsUnassociated() || trk.FlightPlan.HandoffController != "" {
				return ErrSTARSIllegalTrack
			}
			ctx.Client.RepositionTrack(trk.FlightPlan.ACID, "", pos, func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})
	registerCommand(CommandModeTrackReposition, "[TRK_ACID][SLEW]|[TRK_BCN][SLEW]|[TRK_INDEX][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, src, dst *sim.Track) error {
			if src.IsUnassociated() || src.FlightPlan.HandoffController != "" || dst.IsAssociated() {
				return ErrSTARSIllegalTrack
			}
			ctx.Client.RepositionTrack(src.FlightPlan.ACID, dst.ADSBCallsign, math.Point2LL{},
				func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})

	// 5.7.5 Remove frozen flight from display (implied)
	// slew to track with "ZZ" in data block...

	// 5.7.6 Assign destination airport data to track
	// registerCommand(CommandModeMultiFunc, "J[FIELD:1][SLEW]"
	// registerCommand(CommandModeMultiFunc, "J[FIELD:1][FIELD:1][SLEW]"

	// 5.7.7 Remove ADS-B data loss indicator (implied)
	// 5.7.8 Remove ADS-B duplicate target address indicator (implied)

	// 5.7.9 Stop blinking canceled flight plan indicator (implied)

	// 5.7.10 Implied compound command description (implied)

	// 5.7.11 Remove ACID / target ID mismatch indicator (implied)

	// 5.7.12 Enable / inhibit display of altitude for all active suspended tracks (p. 5-206)
	registerCommand(CommandModeTrackSuspend, "E", func(ps *Preferences) {
		ps.DisplaySuspendedTrackAltitude = true
	})
	registerCommand(CommandModeTrackSuspend, "I", func(ps *Preferences) {
		ps.DisplaySuspendedTrackAltitude = false
	})
}

func associateFlightPlan(sp *STARSPane, ctx *panes.Context, callsign av.ADSBCallsign, spec sim.FlightPlanSpecifier) {
	if !spec.TrackingController.IsSet {
		spec.TrackingController.Set(ctx.UserPrimaryPosition())
	}
	if !spec.CoordinationTime.IsSet {
		spec.CoordinationTime.Set(ctx.Now)
	}

	ctx.Client.AssociateFlightPlan(callsign, spec,
		func(err error) {
			if err == nil {
				if trk, ok := ctx.GetTrackByCallsign(callsign); ok && trk.IsAssociated() {
					sp.previewAreaOutput = formatFlightPlan(sp, ctx, trk.FlightPlan, trk)
				}
			} else {
				sp.displayError(err, ctx, "")
			}
		})
}

func createFlightPlan(sp *STARSPane, ctx *panes.Context, spec sim.FlightPlanSpecifier) {
	if !spec.TrackingController.IsSet {
		spec.TrackingController.Set(ctx.UserPrimaryPosition())
	}
	if !spec.CoordinationTime.IsSet {
		spec.CoordinationTime.Set(ctx.Now)
	}

	ctx.Client.CreateFlightPlan(spec,
		func(err error) {
			if err == nil {
				if fp := ctx.Client.State.GetFlightPlanForACID(spec.ACID.Get()); fp != nil {
					sp.previewAreaOutput = formatFlightPlan(sp, ctx, fp, nil)
				}
			} else {
				sp.displayError(err, ctx, "")
			}
		})
}

func modifyFlightPlan(sp *STARSPane, ctx *panes.Context, acid sim.ACID, spec sim.FlightPlanSpecifier, display bool) {
	if !spec.ACID.IsSet {
		spec.ACID.Set(acid)
	}

	ctx.Client.ModifyFlightPlan(acid, spec,
		func(err error) {
			if err == nil {
				if spec.RequestedAltitude.IsSet {
					if state, ok := sp.trackStateForACID(ctx, acid); ok {
						t := true
						state.DisplayRequestedAltitude = &t
					}
				}
				if spec.Scratchpad.IsSet && spec.Scratchpad.Get() == "" {
					if state, ok := sp.trackStateForACID(ctx, acid); ok {
						state.ClearedScratchpadAlternate = true
					}
				}
				if display {
					trk, _ := ctx.Client.State.GetTrackByACID(acid)
					sp.previewAreaOutput = formatFlightPlan(sp, ctx, trk.FlightPlan, trk)
				}
			} else {
				sp.displayError(err, ctx, acid)
			}
		})
}

// See STARS Operators Manual 5-184...
// trk may be nil
func formatFlightPlan(sp *STARSPane, ctx *panes.Context, fp *sim.NASFlightPlan, trk *sim.Track) string {
	if fp == nil { // shouldn't happen...
		return "NO PLAN"
	}

	fmtTime := func(t time.Time) string {
		return t.UTC().Format("1504")
	}

	// Common stuff
	var state *TrackState
	if trk != nil {
		state = sp.TrackState[trk.ADSBCallsign]
	}

	var aircraftType string
	if fp.AircraftCount > 1 {
		aircraftType = strconv.Itoa(fp.AircraftCount) + "/"
	}
	aircraftType += fp.AircraftType
	if fp.CWTCategory != "" {
		aircraftType += "/" + fp.CWTCategory
	}
	if fp.RNAV {
		aircraftType += "^"
	}
	if fp.EquipmentSuffix != "" {
		aircraftType += "/" + fp.EquipmentSuffix
	}

	fmtfix := func(f string) string {
		if f == "" {
			return ""
		}
		if len(f) > 3 {
			f = f[:3]
		}
		return f + "  "
	}

	trkalt := func() string {
		if trk == nil {
			return ""
		} else if trk.Mode == av.TransponderModeAltitude {
			return fmt.Sprintf("%03d ", int(trk.TransponderAltitude+50)/100)
		} else if fp.PilotReportedAltitude != 0 {
			return fmt.Sprintf("%03d ", fp.PilotReportedAltitude/100)
		} else {
			return "RDR "
		}
	}
	result := string(fp.ACID) + " " // all start with aricraft id
	switch fp.TypeOfFlight {
	case av.FlightTypeOverflight:
		result += aircraftType + " "
		result += fp.AssignedSquawk.String() + " " + string(fp.TrackingController) + " "
		result += trkalt()
		result += "\n"

		result += fmtfix(fp.EntryFix)
		if state != nil {
			result += "E" + fmtTime(state.FirstRadarTrackTime) + " "
		} else {
			result += "E" + fmtTime(fp.CoordinationTime) + " "
		}
		result += fmtfix(fp.ExitFix)
		if fp.RequestedAltitude != 0 {
			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100) + "\n"
		}

		// TODO: [mode S equipage] [target identification] [target address]

	case av.FlightTypeDeparture:
		if state == nil || state.FirstRadarTrackTime.IsZero() {
			// Proposed departure
			result += aircraftType + " "
			result += fp.AssignedSquawk.String() + " " + string(fp.TrackingController) + "\n"

			result += fmtfix(fp.EntryFix)
			result += fmtfix(fp.ExitFix)
			if !fp.CoordinationTime.IsZero() {
				result += "P" + fmtTime(fp.CoordinationTime) + " "
			}
			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100)
		} else {
			// Active departure
			result += fp.AssignedSquawk.String() + " "
			result += fmtfix(fp.EntryFix)
			result += "D" + fmtTime(state.FirstRadarTrackTime) + " "
			result += trkalt() + "\n"

			result += fmtfix(fp.ExitFix)
			result += "R" + fmt.Sprintf("%03d", fp.RequestedAltitude/100) + " "
			result += aircraftType

			// TODO: [mode S equipage] [target identification] [target address]
		}
	case av.FlightTypeArrival:
		result += aircraftType + " "
		result += fp.AssignedSquawk.String() + " "
		result += string(fp.TrackingController) + " "
		result += trkalt() + "\n"

		result += fmtfix(fp.EntryFix)
		if state != nil {
			result += "A" + fmtTime(state.FirstRadarTrackTime) + " "
		} else {
			result += "A" + fmtTime(fp.CoordinationTime) + " "
		}
		result += fmtfix(fp.ExitFix)
		// TODO: [mode S equipage] [target identification] [target address]

	default:
		return "FLIGHT TYPE UNKNOWN"
	}

	return result
}
