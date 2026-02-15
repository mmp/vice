// stars/cmdtools.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 6 of the TCW Operator Manual

package stars

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

func init() {
	// 6.1.1 Change range ring spacing,
	// 6.1.2 Define user-specified range ring center
	// 6.1.3 Toggle between default and user-specified range ring center
	// All done in dcb.go

	// 6.2 Display weather history
	registerCommand(CommandModeMultiFunc, "W"+STARSTriangleCharacter, func(sp *STARSPane, ctx *panes.Context) CommandStatus {
		sp.wxHistoryDraw = 2
		sp.wxNextHistoryStepTime = ctx.Now.Add(5 * time.Second)
		return CommandStatus{Output: "IN PROGRESS"}
	})

	// 6.3.1 Toggle Predicted track line on/off for a single track (p. 6-13)
	registerCommand(CommandModeMultiFunc, "R[SLEW]", func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track) error {
		if ps.PTLAll || (ps.PTLOwn && trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan)) {
			return ErrSTARSIllegalTrack
		}
		if trk.IsAssociated() && !trk.FlightPlan.Location.IsZero() {
			// Not allowed for unsupported dbs
			return ErrSTARSIllegalTrack
		}

		state := sp.TrackState[trk.ADSBCallsign]
		state.DisplayPTL = !state.DisplayPTL
		return nil
	})

	// 6.3.2 Toggle predicted track lines on/off for associated tracks: DCB button only
	// 6.3.3 Toggle predicted track lines on/off for owned tracks: DCB button only
	// 6.3.4 Toggle predicted track line value: handled in dcb.go

	// 6.4 Show or remove minimum separation
	registerCommand(CommandModeMin, "", func(sp *STARSPane) {
		sp.MinSepAircraft[0] = ""
		sp.MinSepAircraft[1] = ""
	})
	registerCommand(CommandModeMin, "[SLEW]", func(sp *STARSPane, trk *sim.Track) CommandStatus {
		sp.MinSepAircraft[0] = trk.ADSBCallsign
		return CommandStatus{
			Clear: ClearNone,
			CommandHandlers: makeCommandHandlers(
				"[SLEW]", func(sp *STARSPane, trk *sim.Track) {
					sp.MinSepAircraft[1] = trk.ADSBCallsign
				},
			),
		}
	})

	// 6.5.1 Enable / inhibit CRDA for this TCW/TDW
	registerCommand(CommandModeMultiFunc, "N", func(sp *STARSPane, ps *Preferences) error {
		if len(sp.CRDAPairs) == 0 {
			return ErrSTARSIllegalFunction
		}
		ps.CRDA.Disabled = !ps.CRDA.Disabled
		return nil
	})

	// 6.5.2 Toggle display of ghost data blocks for specified runway pair
	toggleCRDAGhostsForRunwayPair := func(sp *STARSPane, ctx *panes.Context, ps *Preferences, ap string, idx int) error {
		if len(sp.CRDAPairs) == 0 {
			return ErrSTARSIllegalFunction
		}
		for i, pair := range sp.CRDAPairs {
			if pair.Airport == ap && pair.Index == idx {
				ps.CRDA.RunwayPairState[i].Enabled = !ps.CRDA.RunwayPairState[i].Enabled
				return nil
			}
		}
		return ErrSTARSCommandFormat
	}
	registerCommand(CommandModeMultiFunc, "NP[AIRPORT_ID] [NUM]", toggleCRDAGhostsForRunwayPair)
	registerCommand(CommandModeMultiFunc, "NP[NUM]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) error {
			// Use default airport from area config
			defaultAirport := ctx.FacilityAdaptation.DefaultAirportForController(ctx.UserController())
			if len(defaultAirport) == 0 {
				return ErrSTARSIllegalFunction
			}
			ap := defaultAirport[1:]
			if _, ok := av.DB.LookupAirport(ap); !ok {
				panic(defaultAirport)
			}
			return toggleCRDAGhostsForRunwayPair(sp, ctx, ps, ap, idx)
		})

	// 6.5.3 Enable / inhibit display of Ghost data blocks for specified runway
	registerCommand(CommandModeMultiFunc, "N[CRDA_REGION_ID]",
		func(sp *STARSPane, runwayState *CRDARunwayState) CommandStatus {
			runwayState.Enabled = !runwayState.Enabled
			s := util.Select(runwayState.Enabled, "ENABLED", "INHIBITED")
			if !runwayState.Enabled {
				runwayState.DrawQualificationRegion = false
				runwayState.DrawCourseLines = false
			}
			// TODO: if this results in disabling ghosting on both runways in a pair, remove any CRDA maps from the display
			return CommandStatus{Output: runwayState.Airport + " " + runwayState.Region + " GHOSTING " + s}
		})
	registerCommand(CommandModeMultiFunc, "N[CRDA_REGION_ID]E",
		func(sp *STARSPane, runwayState *CRDARunwayState) CommandStatus {
			runwayState.Enabled = true
			return CommandStatus{Output: runwayState.Airport + " " + runwayState.Region + " GHOSTING ENABLED"}
		})
	registerCommand(CommandModeMultiFunc, "N[CRDA_REGION_ID]I",
		func(sp *STARSPane, runwayState *CRDARunwayState) CommandStatus {
			runwayState.Enabled = false
			runwayState.DrawQualificationRegion = false
			runwayState.DrawCourseLines = false
			return CommandStatus{Output: runwayState.Airport + " " + runwayState.Region + " GHOSTING INHIBITED"}
		})

	// 6.5.4 Toggle display of a single ghost data block at this TCW/TDW
	registerCommand(CommandModeMultiFunc, "N[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) CommandStatus {
		state := sp.TrackState[trk.ADSBCallsign]
		if trk.IsUnassociated() || !trackInCRDARegion(sp, ctx, trk) || state.Ghost.State != GhostStateSuppressed {
			return CommandStatus{Output: "ILL TRK"} // informational
		}
		state.Ghost.State = GhostStateRegular
		return CommandStatus{}
	})
	registerCommand(CommandModeMultiFunc, "N[GHOST_SLEW]", func(sp *STARSPane, ctx *panes.Context, ghost *av.GhostTrack) error {
		if state, ok := sp.TrackState[ghost.ADSBCallsign]; !ok {
			return ErrSTARSIllegalTrack
		} else {
			state.Ghost.State = GhostStateSuppressed
			return nil
		}
	})

	// 6.5.5 Change leader line direction for ghost data blocks on specified runway
	registerCommand(CommandModeMultiFunc, "NL[CRDA_REGION_ID][#]", func(sp *STARSPane, runwayState *CRDARunwayState, num int) error {
		dir, ok := sp.numpadToDirection(num)
		if !ok {
			return ErrSTARSCommandFormat
		}
		runwayState.LeaderLineDirection = dir
		return nil
	})

	// 6.5.6 Display ghost data block parent track information
	registerCommand(CommandModeMultiFunc, "N*[GHOST_SLEW]", func(sp *STARSPane, ctx *panes.Context, ghost *av.GhostTrack) (CommandStatus, error) {
		if trk, ok := ctx.GetTrackByCallsign(ghost.ADSBCallsign); ok && trk.IsAssociated() {
			return CommandStatus{Output: formatFlightPlan(sp, ctx, trk.FlightPlan, trk)}, nil
		}
		return CommandStatus{}, ErrSTARSIllegalTrack
	})

	// 6.5.7 Toggle ghost data block between full and partial data block formats (implied)
	registerCommand(CommandModeNone, "[GHOST_SLEW]", func(sp *STARSPane, ghost *av.GhostTrack) {
		state := sp.TrackState[ghost.ADSBCallsign]
		state.Ghost.PartialDatablock = !state.Ghost.PartialDatablock
	})

	// 6.5.8 Force / unforce ghost qualification for all tracks
	registerCommand(CommandModeMultiFunc, "N*ALL", func(sp *STARSPane, ps *Preferences) error {
		if len(sp.CRDAPairs) == 0 {
			return ErrSTARSIllegalFunction
		}
		ps.CRDA.ForceAllGhosts = !ps.CRDA.ForceAllGhosts
		return nil
	})

	// 6.5.9 Toggle display of a runway's CRDA qualification region
	registerCommand(CommandModeMultiFunc, "N[CRDA_REGION_ID] B", func(sp *STARSPane, runwayState *CRDARunwayState) {
		runwayState.DrawQualificationRegion = !runwayState.DrawQualificationRegion
	})

	// 6.5.10 Toggle display of a runway's CRDA course line segments
	registerCommand(CommandModeMultiFunc, "N[CRDA_REGION_ID] L", func(sp *STARSPane, runwayState *CRDARunwayState) {
		runwayState.DrawCourseLines = !runwayState.DrawCourseLines
	})

	// 6.5.11 Toggle display of CRDA status list (p. 6-32)
	registerCommand(CommandModeMultiFunc, "TN", func(ps *Preferences) {
		ps.CRDAStatusList.Visible = !ps.CRDAStatusList.Visible
	})

	// 6.5.12 Move CRDA status list (p. 6-33)
	registerCommand(CommandModeMultiFunc, "TN[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.CRDAStatusList.Position = pos
		ps.CRDAStatusList.Visible = true
	})

	// 6.6.1 Create and display restriction area text
	createRestrictionArea := func(sp *STARSPane, ctx *panes.Context, ra av.RestrictionArea) {
		ctx.Client.CreateRestrictionArea(ra, func(idx int, err error) {
			if err == nil {
				ps := sp.currentPrefs()
				ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
			} else {
				sp.displayError(err, ctx, "")
			}
		})
	}
	registerCommand(CommandModeRestrictionArea, "G[RA_TEXT_AND_LOCATION]",
		func(sp *STARSPane, ctx *panes.Context, parsed RAText) {
			ra := av.RestrictionArea{
				Text:         parsed.text,
				TextPosition: parsed.pos,
				BlinkingText: parsed.blink,
			}
			createRestrictionArea(sp, ctx, ra)
		})
	registerCommand(CommandModeRestrictionArea, "G[RA_TEXT][POS]",
		func(sp *STARSPane, ctx *panes.Context, parsed RAText, pos math.Point2LL) {
			ra := av.RestrictionArea{
				Text:         parsed.text,
				TextPosition: pos,
				BlinkingText: parsed.blink,
			}
			createRestrictionArea(sp, ctx, ra)
		})

	// 6.6.2 Create and display restriction area circle and text (p. 6-38)
	startRACircle := func(sp *STARSPane, ctx *panes.Context, radius float32, parsed RAText, pos math.Point2LL) (CommandStatus, error) {
		if radius < 1 || radius > 125 {
			return CommandStatus{}, ErrSTARSIllegalRange
		}

		sp.wipRestrictionArea = &av.RestrictionArea{
			Text:         parsed.text,
			CircleCenter: pos,
			CircleRadius: radius,
			BlinkingText: parsed.blink,
			Shaded:       parsed.shaded,
			Color:        parsed.color,
		}
		return CommandStatus{
			Clear: ClearInput,
			CommandHandlers: makeCommandHandlers(
				"", func(sp *STARSPane, ctx *panes.Context) {
					ra := sp.wipRestrictionArea
					ra.TextPosition = ra.CircleCenter
					createRestrictionArea(sp, ctx, *ra)
					sp.wipRestrictionArea = nil
				},
				"[POS]", func(sp *STARSPane, ctx *panes.Context, p math.Point2LL) {
					ra := sp.wipRestrictionArea
					ra.TextPosition = p
					createRestrictionArea(sp, ctx, *ra)
					sp.wipRestrictionArea = nil
				},
			),
		}, nil
	}
	registerCommand(CommandModeRestrictionArea, "C[FLOAT][RA_CLOSED_TEXT][POS]", startRACircle)
	registerCommand(CommandModeRestrictionArea, "C[FLOAT][RA_CLOSED_TEXT_AND_LOCATION]",
		func(sp *STARSPane, ctx *panes.Context, radius float32, parsed RAText) (CommandStatus, error) {
			return startRACircle(sp, ctx, radius, parsed, parsed.pos)
		})

	// 6.6.3 Create and display restriction area closed polygon and text
	startRAPolygon := func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL, closed bool) CommandStatus {
		sp.wipRestrictionArea = &av.RestrictionArea{
			Closed:   false,
			Vertices: [][]math.Point2LL{{pos}},
		}
		if ctx.Mouse != nil {
			sp.wipRestrictionAreaMousePos = ctx.Mouse.Pos
		} else {
			sp.wipRestrictionAreaMousePos = [2]float32{}
		}
		sp.wipRestrictionAreaMouseMoved = false

		return CommandStatus{
			Clear: ClearInput,
			CommandHandlers: makeCommandHandlers(
				"[POS]|[RA_LOCATION]", func(sp *STARSPane, ctx *panes.Context, p math.Point2LL) CommandStatus {
					ra := sp.wipRestrictionArea
					ra.Vertices[0] = append(ra.Vertices[0], p)
					sp.wipRestrictionAreaMousePos = ctx.Mouse.Pos
					sp.wipRestrictionAreaMouseMoved = false
					return CommandStatus{Clear: ClearInput}
				},
				"[RA_CLOSED_TEXT][POS]", func(sp *STARSPane, ctx *panes.Context, parsed RAText, p math.Point2LL) CommandStatus {
					ra := sp.wipRestrictionArea
					ra.Text = parsed.text
					ra.TextPosition = p
					ra.BlinkingText = parsed.blink
					ra.Shaded = parsed.shaded
					ra.Color = parsed.color

					if closed {
						if len(ra.Vertices[0]) < 3 {
							return CommandStatus{Output: "ILL FNCT - MIN 3 VERTICES"}
						}
						ra.Vertices[0] = append(ra.Vertices[0], ra.Vertices[0][0])
					} else {
						if len(ra.Vertices[0]) < 2 {
							return CommandStatus{Output: "ILL FNCT - MIN 2 VERTICES"}
						}
					}
					createRestrictionArea(sp, ctx, *ra)
					sp.wipRestrictionArea = nil
					return CommandStatus{}
				},
			),
		}
	}
	registerCommand(CommandModeRestrictionArea, "P[POS]|P[RA_LOCATION]",
		func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL) CommandStatus {
			return startRAPolygon(sp, ctx, pos, true)
		})

	// 6.6.4 Create and display restriction area open polygon and text
	registerCommand(CommandModeRestrictionArea, "A[POS]|A[RA_LOCATION]",
		func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL) CommandStatus {
			return startRAPolygon(sp, ctx, pos, false)
		})

	// 6.6.5 Move restriction area
	updateRestrictionArea := func(sp *STARSPane, ctx *panes.Context, idx int, ra av.RestrictionArea) {
		ctx.Client.UpdateRestrictionArea(idx, ra, func(err error) {
			if err == nil {
				ps := sp.currentPrefs()
				if settings, ok := ps.RestrictionAreaSettings[idx]; ok {
					settings.Visible = true
				} else {
					ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
				}
			} else {
				sp.displayError(err, ctx, "")
			}
		})
	}
	registerCommand(CommandModeRestrictionArea, "[USER_RA_INDEX]*"+STARSTriangleCharacter+"[RA_LOCATION]|"+
		"[USER_RA_INDEX]*"+STARSTriangleCharacter+"[POS]",
		func(sp *STARSPane, ctx *panes.Context, idx int, pos math.Point2LL) {
			ra := sp.getRestrictionArea(ctx, idx, true)
			ra.MoveTo(pos)
			ra.BlinkingText = true
			updateRestrictionArea(sp, ctx, idx, *ra)
		})
	registerCommand(CommandModeRestrictionArea, "[USER_RA_INDEX]*[RA_LOCATION]|[USER_RA_INDEX]*[POS]",
		func(sp *STARSPane, ctx *panes.Context, idx int, pos math.Point2LL) {
			ra := sp.getRestrictionArea(ctx, idx, true)
			ra.MoveTo(pos)
			ra.BlinkingText = true
			updateRestrictionArea(sp, ctx, idx, *ra)
		})

	// 6.6.6 Delete restriction area
	registerCommand(CommandModeRestrictionArea, "[USER_RA_INDEX]DEL",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) {
			delete(ps.RestrictionAreaSettings, idx)
			ctx.Client.DeleteRestrictionArea(idx, func(err error) { sp.displayError(err, ctx, "") })
		})

	// 6.6.7 Change restriction area text
	registerCommand(CommandModeRestrictionArea, "[USER_RA_INDEX]T[RA_TEXT]",
		func(sp *STARSPane, ctx *panes.Context, idx int, parsed RAText) {
			ra := sp.getRestrictionArea(ctx, idx, true)
			ra.Text = parsed.text
			ra.BlinkingText = parsed.blink
			updateRestrictionArea(sp, ctx, idx, *ra)
		})

	// 6.6.8 Hide / show restriction area text
	registerCommand(CommandModeRestrictionArea, "[RA_INDEX]T",
		func(ps *Preferences, idx int) {
			if ps.RestrictionAreaSettings[idx] == nil {
				ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{}
			}
			ps.RestrictionAreaSettings[idx].HideText = !ps.RestrictionAreaSettings[idx].HideText
			ps.RestrictionAreaSettings[idx].ForceBlinkingText = false
		})
	registerCommand(CommandModeRestrictionArea, "[RA_INDEX]T "+STARSTriangleCharacter,
		func(ps *Preferences, idx int) error {
			if ps.RestrictionAreaSettings[idx] == nil {
				ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{}
			}
			ps.RestrictionAreaSettings[idx].HideText = !ps.RestrictionAreaSettings[idx].HideText

			// If text is now visible, force blinking; if now hidden, error
			if !ps.RestrictionAreaSettings[idx].HideText {
				ps.RestrictionAreaSettings[idx].ForceBlinkingText = true
				return nil
			} else {
				return ErrSTARSCommandFormat
			}
		})

	// 6.6.9 Hide / show restriction area and text, or stop blinking text
	registerCommand(CommandModeRestrictionArea, "[RA_INDEX]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, idx int) {
			settings, ok := ps.RestrictionAreaSettings[idx]
			if !ok {
				settings = &RestrictionAreaSettings{}
				ps.RestrictionAreaSettings[idx] = settings
			}

			// If text is blinking, stop it; otherwise toggle visibility
			ra := sp.getRestrictionArea(ctx, idx, false)
			blinking := settings.ForceBlinkingText || (ra.BlinkingText && !settings.StopBlinkingText)
			if blinking {
				settings.ForceBlinkingText = false
				settings.StopBlinkingText = true
			} else {
				settings.Visible = !settings.Visible
			}
		})
	registerCommand(CommandModeRestrictionArea, "[RA_INDEX]E",
		func(ps *Preferences, idx int) {
			settings, ok := ps.RestrictionAreaSettings[idx]
			if !ok {
				settings = &RestrictionAreaSettings{}
				ps.RestrictionAreaSettings[idx] = settings
			}
			settings.Visible = true
		})
	registerCommand(CommandModeRestrictionArea, "[RA_INDEX]I",
		func(ps *Preferences, idx int) {
			settings, ok := ps.RestrictionAreaSettings[idx]
			if !ok {
				settings = &RestrictionAreaSettings{}
				ps.RestrictionAreaSettings[idx] = settings
			}
			settings.Visible = false
		})

	// 6.6.10 Hide / show Restriction area list
	registerCommand(CommandModeMultiFunc, "TRA", func(ps *Preferences) {
		ps.RestrictionAreaList.Visible = !ps.RestrictionAreaList.Visible
	})

	// 6.6.11 Move Restriction area list
	registerCommand(CommandModeMultiFunc, "TRA[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.RestrictionAreaList.Position = pos
		ps.RestrictionAreaList.Visible = true
	})

	// 6.6.12 Change restriction area color (DoD only)

	// 6.7 Create range bearing line
	makeRBLCommandHandlers := func(rbl *STARSRangeBearingLine) []userCommand {
		return makeCommandHandlers(
			"*T[SLEW]", func(sp *STARSPane, trk *sim.Track) {
				rbl.P[1].ADSBCallsign = trk.ADSBCallsign
				sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
				sp.wipRBL = nil
			},
			"*T[POS]", func(sp *STARSPane, pos math.Point2LL) {
				rbl.P[1].Loc = pos
				sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
				sp.wipRBL = nil
			},
			"*T[FIELD]", func(sp *STARSPane, ctx *panes.Context, fixBeaconOrACID string) error {
				if func() bool {
					// Fix takes priority over ACID in the unlikely chance that there are both of the same name.
					if p, ok := av.DB.LookupWaypoint(fixBeaconOrACID); ok {
						rbl.P[1].Loc = p
						return true
					}

					if sq, err := av.ParseSquawk(fixBeaconOrACID); err == nil {
						for _, trk := range sp.visibleTracks {
							if trk.IsAssociated() && trk.FlightPlan.AssignedSquawk == sq {
								rbl.P[1].ADSBCallsign = trk.ADSBCallsign
								return true
							}
						}
					}

					for _, trk := range sp.visibleTracks {
						if trk.IsAssociated() && trk.FlightPlan.ACID == sim.ACID(fixBeaconOrACID) {
							rbl.P[1].ADSBCallsign = trk.ADSBCallsign
							return true
						}
					}

					return false
				}() {
					sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
					sp.wipRBL = nil
					return nil
				}

				return ErrSTARSCommandFormat
			},
		)
	}
	registerCommand(CommandModeNone, "*T[FIX]|*T[POS]",
		func(sp *STARSPane, p math.Point2LL) CommandStatus {
			rbl := STARSRangeBearingLine{}
			rbl.P[0].Loc = p
			sp.wipRBL = &rbl
			sp.previewAreaInput = "*T" // set up for the second point

			return CommandStatus{
				Clear:           ClearNone,
				CommandHandlers: makeRBLCommandHandlers(&rbl),
			}
		})
	registerCommand(CommandModeNone, "*T[SLEW]|*T [TRK_ACID]|*T [TRK_BCN]",
		func(sp *STARSPane, trk *sim.Track) CommandStatus {
			rbl := STARSRangeBearingLine{}
			rbl.P[0].ADSBCallsign = trk.ADSBCallsign
			sp.wipRBL = &rbl
			sp.previewAreaInput = "*T" // set up for the second point

			return CommandStatus{
				Clear:           ClearNone,
				CommandHandlers: makeRBLCommandHandlers(&rbl),
			}
		})

	// 6.8 Remove range bearing line
	registerCommand(CommandModeNone, "*T[NUM]", func(sp *STARSPane, idx int) error {
		idx-- // Convert to 0-based
		if idx < 0 || idx >= len(sp.RangeBearingLines) {
			return ErrSTARSIllegalParam // FIXME: Should be "RBL ID" evidently
		}
		sp.RangeBearingLines = util.DeleteSliceElement(sp.RangeBearingLines, idx)
		return nil
	})
	registerCommand(CommandModeNone, "*T", func(sp *STARSPane) {
		sp.wipRBL = nil
		sp.RangeBearingLines = nil
	})

	// 6.9 Display latitude / longitude of cursor location
	registerCommand(CommandModeMultiFunc, "D*[POS]", func(pos math.Point2LL) CommandStatus {
		format := func(v float32) string {
			v = math.Abs(v)
			d := int(v)
			v -= float32(d)
			m := int(60 * v)
			v = 60*v - float32(m)
			s := int(60 * v)
			return fmt.Sprintf("%02d %02d %02d", d, m, s)
		}

		s := format(pos.Latitude())
		if pos.Latitude() < 0 {
			s += "S"
		}
		s += "/"

		s += format(pos.Longitude())
		if pos.Longitude() > 0 {
			s += "E"
		}

		return CommandStatus{Output: s}
	})

	// 6.10 Print track data

	// 6.11 Display facilities with enabled TZ message transmission

	// 6.12.1 Initiate intrafacility pointout (implied)
	// 6.12.7 Initiate interfacility pointout (implied)
	registerCommand(CommandModeNone, "[TCP1]*[SLEW]|[TCP2]*[SLEW]|[TCP_TRI]*[SLEW]|[ARTCC]*[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, tcp string, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}

			ctrl := lookupControllerWithAirspace(ctx, tcp, trk)
			if ctrl == nil {
				return ErrSTARSIllegalPosition
			}

			ctx.Client.PointOut(trk.FlightPlan.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
			return nil
		})

	// 6.12.3 Reject intrafacility pointout (implied)
	registerCommand(CommandModeNone, "UN[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}
		ctx.Client.RejectPointOut(trk.FlightPlan.ACID, func(err error) { sp.displayError(err, ctx, "") })
		return nil
	})

	// 6.12.6 Force quicklook for single track at one or more TCPs
	forceQL := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, tcps string) error {
		if trk.IsUnassociated() || trk.FlightPlan.Suspended {
			return ErrSTARSIllegalTrack
		}

		for len(tcps) > 0 {
			if strings.HasPrefix(tcps, "ALL") {
				// Per 6.12.6: ALL silently skips the owning TCP's positions.
				fac := ctx.UserController().FacilityIdentifier
				for _, ctrl := range ctx.Client.State.Controllers {
					if !ctrl.ERAMFacility && ctrl.FacilityIdentifier == fac &&
						!ctx.Client.State.TCWControlsPosition(trk.FlightPlan.OwningTCW, sim.ControlPosition(ctrl.PositionId())) {
						ctx.Client.ForceQL(trk.FlightPlan.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
					}
				}
				tcps = strings.TrimPrefix(tcps, "ALL")
			} else if tcps[0] >= '1' && tcps[0] <= '9' {
				// two-character TCP?
				if len(tcps) == 1 || !(tcps[1] >= 'A' && tcps[1] <= 'Z') {
					return ErrSTARSCommandFormat
				}

				ctrl := lookupControllerByTCP(ctx.Client.State.Controllers, tcps[:2], ctx.UserController().SectorID)
				if ctrl == nil {
					return ErrSTARSIllegalPosition
				}
				ctx.Client.ForceQL(trk.FlightPlan.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
				tcps = tcps[2:]
			} else {
				// TCP without controller subset
				ctrl := lookupControllerByTCP(ctx.Client.State.Controllers, tcps[:1], ctx.UserController().SectorID)
				if ctrl == nil {
					return ErrSTARSIllegalPosition
				}
				ctx.Client.ForceQL(trk.FlightPlan.ACID, sim.TCP(ctrl.PositionId()), func(err error) { sp.displayError(err, ctx, "") })
				tcps = tcps[1:]

				// Must be followed by a space if not at the end
				if len(tcps) > 0 {
					if tcps[0] != ' ' {
						return ErrSTARSCommandFormat
					}
					tcps = tcps[1:]
				}
			}
		}
		return nil
	}
	registerCommand(CommandModeNone, "**[TRK_ACID] [ALL_TEXT]|**[TRK_BCN] [ALL_TEXT]|**[TRK_INDEX] [ALL_TEXT]", forceQL)
	registerCommand(CommandModeNone, "**[ALL_TEXT][SLEW]", forceQL)
	forceQLToSelf := func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		// Force QL to self
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}
		if !ctx.FacilityAdaptation.ForceQLToSelf || !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			return ErrSTARSIllegalPosition
		}
		state := sp.TrackState[trk.ADSBCallsign]
		state.ForceQL = true
		return nil
	}
	registerCommand(CommandModeNone, "**[TRK_ACID]|**[TRK_BCN]|**[TRK_INDEX]", forceQLToSelf)
	registerCommand(CommandModeNone, "**[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() {
			return ErrSTARSIllegalTrack
		}

		acid := trk.FlightPlan.ACID
		if po, ok := sp.PointOuts[acid]; ok && ctx.UserControlsPosition(po.To) {
			// 5.1.11 Accept handoff of track in intrafacility pointout
			ctx.Client.AcceptHandoff(acid, func(err error) { sp.displayError(err, ctx, "") })
			return nil
		} else {
			return forceQLToSelf(sp, ctx, trk)
		}
	})

	// 6.12.11 Display pointout accept history for a single track
	registerCommand(CommandModeMultiFunc, "O[TRK_ACID]|O[TRK_BCN]|O[TRK_INDEX]|O[SLEW]",
		func(ctx *panes.Context, trk *sim.Track) (CommandStatus, error) {
			if trk.IsUnassociated() {
				return CommandStatus{}, ErrSTARSIllegalTrack
			}

			if len(trk.FlightPlan.PointOutHistory) == 0 {
				return CommandStatus{Output: "PO NONE"}, nil
			}
			// Convert []TCP to []string for display
			history := util.MapSlice(trk.FlightPlan.PointOutHistory, func(tcp sim.TCP) string { return string(tcp) })
			return CommandStatus{Output: strings.Join(history, " ")}, nil
		})

	// 6.12.12 Clear pointout accept history and count for single track
	registerCommand(CommandModeMultiFunc, "O* [TRK_ACID]|O* [TRK_BCN]|O* [TRK_INDEX]|O* [SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() || (!ctx.UserOwnsFlightPlan(trk.FlightPlan) && !ctx.TCWIsPrivileged(ctx.UserTCW)) {
				return ErrSTARSIllegalTrack
			}

			var spec sim.FlightPlanSpecifier
			spec.PointOutHistory.Set(nil)
			modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false)
			return nil
		})

	// 6.12.13 Enable / inhibit display of pointout accept count

	// 6.13.1 Specify datablock position for a single track (implied)
	registerCommand(CommandModeNone, "[#][SLEW]",
		func(sp *STARSPane, direction int, state *TrackState) error {
			if dir, ok := sp.numpadToDirection(direction); ok {
				state.LeaderLineDirection = dir
				if dir != nil {
					state.UseGlobalLeaderLine = false
				}
				return nil
			} else {
				return ErrSTARSCommandFormat
			}
		})

	// 6.13.5 Toggle quick look for another owner's tracks (implied)
	// 6.13.6 Display quick looked TCPs and quicklook regions
	toggleQL := func(sp *STARSPane, ctx *panes.Context, ps *Preferences, tcp string) error {
		plus := strings.HasSuffix(tcp, "+")
		tcp = strings.TrimSuffix(tcp, "+")

		ctrl := lookupControllerByTCP(ctx.Client.State.Controllers, tcp, ctx.UserController().SectorID)
		if ctrl == nil {
			return ErrSTARSIllegalPosition
		}

		if ps.QuickLookTCPs == nil {
			ps.QuickLookTCPs = make(map[string]bool)
		}

		ps.QuickLookAll = false
		targetTCP := string(ctrl.PositionId())
		if curPlus, enabled := ps.QuickLookTCPs[targetTCP]; enabled && curPlus == plus {
			delete(ps.QuickLookTCPs, targetTCP)
		} else {
			ps.QuickLookTCPs[targetTCP] = plus
		}
		return nil
	}
	registerCommand(CommandModeNone, "[TCP1]|[TCP2]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, tcp string) (CommandStatus, error) {
			if ctx.UserControlsPosition(sim.ControlPosition(tcp)) { // Display quick looked TCPs and quicklook regions
				return displayQLStatus(sp, ctx), nil
			} else { // Toggle quick look for another owner's tracks
				if err := toggleQL(sp, ctx, ps, tcp); err != nil {
					return CommandStatus{}, err
				} else {
					return CommandStatus{Output: sp.qlPositionsString()}, nil
				}
			}
		})
	registerCommand(CommandModeNone, "[TCP1]+|[TCP2]+",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, tcp string) (CommandStatus, error) {
			if err := toggleQL(sp, ctx, ps, tcp+"+"); err != nil {
				return CommandStatus{}, err
			} else {
				return CommandStatus{Output: sp.qlPositionsString()}, nil
			}
		})

	// 6.13.7 Toggle beacon code display for an unassociated track
	// 6.13.8 Display associated track's ACID, RBC, and ABC in Preview area (p. 6-93)
	// TODO: 5.6.15 Release assigned beacon code from inactive or suspended flight plan
	registerCommand(CommandModeMultiFunc, "B[SLEW]", func(sp *STARSPane, trk *sim.Track) CommandStatus {
		if trk.IsAssociated() {
			// Display ACID, RBC (received beacon code), ABC (assigned beacon code)
			rbc := util.Select(trk.Mode == av.TransponderModeStandby, "    ", trk.Squawk.String())
			return CommandStatus{Output: string(trk.FlightPlan.ACID) + " " + rbc + " " + trk.FlightPlan.AssignedSquawk.String()}
		} else {
			// 6.13.7 For unassociated tracks, toggle beacon code display in LDB
			state := sp.TrackState[trk.ADSBCallsign]
			state.DisplayLDBBeaconCode = !state.DisplayLDBBeaconCode
			return CommandStatus{}
		}
	})

	// 6.13.9 Toggle beacon code display for limited data blocks
	registerCommand(CommandModeMultiFunc, "B", func(ps *Preferences) {
		ps.DisplayLDBBeaconCodes = !ps.DisplayLDBBeaconCodes
	})
	registerCommand(CommandModeMultiFunc, "BE", func(ps *Preferences) {
		ps.DisplayLDBBeaconCodes = true
	})
	registerCommand(CommandModeMultiFunc, "BI", func(ps *Preferences) {
		ps.DisplayLDBBeaconCodes = false
	})

	// 6.13.10 Toggle beacon code between mode 2 and mode 3a

	// 6.13.11 Toggle presentation for beacon code-selected unassociated tracks
	registerCommand(CommandModeMultiFunc, "B[BCN]|B[BCN_BLOCK]", func(ps *Preferences, beacon av.Squawk) error {
		if idx := slices.Index(ps.SelectedBeacons, beacon); idx != -1 {
			// Already selected, remove it
			ps.SelectedBeacons = slices.Delete(ps.SelectedBeacons, idx, idx+1)
		} else if len(ps.SelectedBeacons) == 10 {
			// At capacity
			return ErrSTARSCapacity
		} else {
			// Add it
			ps.SelectedBeacons = append(ps.SelectedBeacons, beacon)
			slices.Sort(ps.SelectedBeacons)
		}
		return nil
	})
	registerCommand(CommandModeMultiFunc, "B*", func(ps *Preferences) {
		clear(ps.SelectedBeacons)
	})

	// 6.13.12 Beacon code readout for all tracks -- beaconator, handled in datablock.go

	// 6.13.13 Toggle quicklook for another owner's tracks
	// 6.13.16 Display quick looked TCPs and quicklook regions
	registerCommand(CommandModeMultiFunc, "Q[QL_POSITIONS]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, tcps []string) (CommandStatus, error) {
			if len(tcps) == 1 && ctx.UserControlsPosition(sim.ControlPosition(tcps[0])) { // Display quick looked TCPs and quicklook regions
				return displayQLStatus(sp, ctx), nil
			} else {
				for _, tcp := range tcps {
					if err := toggleQL(sp, ctx, ps, tcp); err != nil {
						return CommandStatus{}, err
					}
				}
				return CommandStatus{Output: sp.qlPositionsString()}, nil
			}
		})
	registerCommand(CommandModeMultiFunc, "Q", func(ps *Preferences) {
		ps.QuickLookAll = false
		ps.QuickLookAllIsPlus = false
		ps.QuickLookTCPs = nil
	})

	// 6.13.14 Quick look all other owners tracks
	registerCommand(CommandModeMultiFunc, "QALL", func(ps *Preferences) CommandStatus {
		ps.QuickLookAll = true
		ps.QuickLookAllIsPlus = false
		return CommandStatus{Output: "QL ALL"}
	})
	registerCommand(CommandModeMultiFunc, "QALL+", func(ps *Preferences) CommandStatus {
		ps.QuickLookAll = true
		ps.QuickLookAllIsPlus = true
		return CommandStatus{Output: "QL ALL+"}
	})

	// 6.13.15 Disable quick looks for all tracks (partially done in 6.13.13)
	registerCommand(CommandModeMultiFunc, "Q+", func(ps *Preferences) {
		ps.QuickLookAll = false
		ps.QuickLookAllIsPlus = false
	})

	// 6.13.17 Specify data block position for a single track
	registerCommand(CommandModeMultiFunc, "L[#] [TRK_ACID]|L[#] [TRK_BCN]|L[#] [TRK_INDEX]|L[#][SLEW]",
		func(sp *STARSPane, direction int, trk *sim.Track) error {
			if trk.IsUnassociated() {
				return ErrSTARSIllegalTrack
			}
			if dir, ok := sp.numpadToDirection(direction); ok {
				state := sp.TrackState[trk.ADSBCallsign]
				state.LeaderLineDirection = dir
				if dir != nil {
					state.UseGlobalLeaderLine = false
				}
			}
			return nil
		})

	// 6.13.18 Globally data block position for a single track
	registerCommand(CommandModeMultiFunc, "L[NUM:2] [TRK_ACID]|L[NUM:2][SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track, direction int) error {
			if direction/10 != direction%10 { // digits don't match
				return ErrSTARSCommandFormat
			} else if trk.IsUnassociated() || !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
				return ErrSTARSIllegalTrack
			} else if dir, ok := sp.numpadToDirection(direction / 10); ok {
				var spec sim.FlightPlanSpecifier
				spec.GlobalLeaderLineDirection.Set(dir)
				modifyFlightPlan(sp, ctx, trk.FlightPlan.ACID, spec, false /* no display */)
			}
			return nil
		})

	// 6.13.19 Toggle display of aircraft type in a full data block
	registerCommand(CommandModeFlightData, "[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() || trk.FlightPlan.Suspended {
				return ErrSTARSIllegalTrack
			}
			if trk.FlightPlan.AircraftType == "" {
				return ErrSTARSIllegalFunction
			}

			fp := trk.FlightPlan
			extendTime := ctx.Now.Add(5 * time.Second)

			if ctx.UserOwnsFlightPlan(fp) {
				// Owned track - modify the flight plan globally
				spec := sim.FlightPlanSpecifier{}
				if !fp.InhibitACTypeDisplay && ctx.Now.Before(fp.ForceACTypeDisplayEndTime) {
					spec.ForceACTypeDisplayEndTime.Set(extendTime)
				} else {
					spec.InhibitACTypeDisplay.Set(!fp.InhibitACTypeDisplay)
					if !fp.InhibitACTypeDisplay {
						spec.ForceACTypeDisplayEndTime.Set(extendTime)
					}
				}
				modifyFlightPlan(sp, ctx, fp.ACID, spec, false)
			} else {
				// Non-owned track - local state only
				state := sp.TrackState[trk.ADSBCallsign]
				inhibit := state.InhibitACTypeDisplay != nil && *state.InhibitACTypeDisplay
				if !inhibit && ctx.Now.Before(state.ForceACTypeDisplayEndTime) {
					state.ForceACTypeDisplayEndTime = extendTime
				} else {
					newInhibit := !inhibit
					state.InhibitACTypeDisplay = &newInhibit
					if !newInhibit {
						state.ForceACTypeDisplayEndTime = extendTime
					}
				}
			}
			return nil
		})

	// 6.13.21 Enable / disable quicklook region
	registerCommand(CommandModeMultiFunc, "Q[QL_REGION]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, regionID string) error {
			if !ctx.FacilityAdaptation.Filters.Quicklook.HaveId(regionID) {
				return ErrSTARSIllegalFunction
			}

			// Toggle
			if _, ok := ps.DisabledQLRegions[regionID]; ok {
				delete(ps.DisabledQLRegions, regionID)
			} else {
				if ps.DisabledQLRegions == nil {
					ps.DisabledQLRegions = make(map[string]any)
				}
				ps.DisabledQLRegions[regionID] = nil
			}
			sp.updateQuicklookRegionTracks(ctx)
			return nil
		})
	registerCommand(CommandModeMultiFunc, "Q[QL_REGION] I",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, regionID string) error {
			if !ctx.FacilityAdaptation.Filters.Quicklook.HaveId(regionID) {
				return ErrSTARSIllegalFunction
			}
			if ps.DisabledQLRegions == nil {
				ps.DisabledQLRegions = make(map[string]any)
			}
			ps.DisabledQLRegions[regionID] = nil
			sp.updateQuicklookRegionTracks(ctx)
			return nil
		})
	registerCommand(CommandModeMultiFunc, "Q[QL_REGION] E",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, regionID string) error {
			if !ctx.FacilityAdaptation.Filters.Quicklook.HaveId(regionID) {
				return ErrSTARSIllegalFunction
			}
			delete(ps.DisabledQLRegions, regionID)
			sp.updateQuicklookRegionTracks(ctx)
			return nil
		})

	// 6.13.22 Disable all quicklook regions
	registerCommand(CommandModeMultiFunc, "Q*", func(sp *STARSPane, ctx *panes.Context) {
		ps := sp.currentPrefs()
		if ps.DisabledQLRegions == nil {
			ps.DisabledQLRegions = make(map[string]any)
		}
		for _, f := range ctx.FacilityAdaptation.Filters.Quicklook {
			ps.DisabledQLRegions[f.Id] = nil
		}
		sp.updateQuicklookRegionTracks(ctx)
	})

	// 6.13.23 Toggle display of requested altitude for all FDBs
	registerCommand(CommandModeMultiFunc, "RA", func(sp *STARSPane) {
		sp.DisplayRequestedAltitude = !sp.DisplayRequestedAltitude
	})
	registerCommand(CommandModeMultiFunc, "RAE", func(sp *STARSPane) {
		sp.DisplayRequestedAltitude = true
	})
	registerCommand(CommandModeMultiFunc, "RAI", func(sp *STARSPane) {
		sp.DisplayRequestedAltitude = false
	})

	// 6.13.24 Toggle display of requested altitude for a single Full data block
	registerCommand(CommandModeMultiFunc, "RAE[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() || trk.FlightPlan.Suspended || sp.datablockType(ctx, *trk) != FullDatablock {
				return ErrSTARSIllegalTrack
			}
			if trk.FlightPlan.RequestedAltitude == 0 {
				return ErrSTARSIllegalFunction
			}
			state := sp.TrackState[trk.ADSBCallsign]
			b := true
			state.DisplayRequestedAltitude = &b
			return nil
		})
	registerCommand(CommandModeMultiFunc, "RAI[SLEW]", func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
		if trk.IsUnassociated() || trk.FlightPlan.Suspended || sp.datablockType(ctx, *trk) != FullDatablock {
			return ErrSTARSIllegalTrack
		}
		if trk.FlightPlan.RequestedAltitude == 0 {
			return ErrSTARSIllegalFunction
		}
		state := sp.TrackState[trk.ADSBCallsign]
		b := false
		state.DisplayRequestedAltitude = &b
		return nil
	})
	registerCommand(CommandModeMultiFunc, "RA[SLEW]",
		func(sp *STARSPane, ctx *panes.Context, trk *sim.Track) error {
			if trk.IsUnassociated() || trk.FlightPlan.Suspended || sp.datablockType(ctx, *trk) != FullDatablock {
				return ErrSTARSIllegalTrack
			}
			if trk.FlightPlan.RequestedAltitude == 0 {
				return ErrSTARSIllegalFunction
			}

			state := sp.TrackState[trk.ADSBCallsign]
			if state.DisplayRequestedAltitude == nil {
				b := sp.DisplayRequestedAltitude
				state.DisplayRequestedAltitude = &b
			}
			*state.DisplayRequestedAltitude = !*state.DisplayRequestedAltitude
			return nil
		})

	// 6.13.26 Enable / inhibit no flight plan alerts for a departure filter system-wide

	// 6.13.27 Enable / inhibit no flight plan alerts for a no flight plan alert filter system-wide

	// 6.13.28 Selected beacon code display
	registerCommand(CommandModeNone, "**[BCN]",
		func(sp *STARSPane, ctx *panes.Context, beacon av.Squawk) error {
			if !slices.ContainsFunc(sp.visibleTracks, func(trk sim.Track) bool { return trk.Squawk == beacon }) {
				return ErrSTARSNoTrack
			}

			sp.DisplayBeaconCode = beacon
			sp.DisplayBeaconCodeEndTime = ctx.Now.Add(15 * time.Second)
			return nil
		})

	// 6.13.29 Toggle display of a track's secondary data block

	// 6.13.30 Remove a track's secondary data block

	// 6.14 Text message list commands...

	// 6.15 Workstation note commands...

	// 6.16 Final monitoring aid (FMA) commands...

	// 6.17 Military operations area (MOA) commands

	// 6.18 Display bearing / range and readout data for a significant point (implied)
	registerCommand(CommandModeNone, "*F[POS]", func(sp *STARSPane, ctx *panes.Context, pos math.Point2LL) CommandStatus {
		sp.previewAreaInput += " " // if the fix is entered via keyboard, it appears on the next line
		return CommandStatus{
			Clear: ClearNone,
			CommandHandlers: makeCommandHandlers(
				"*F [FIELD]", func(sp *STARSPane, sigpt string) (CommandStatus, error) {
					if sig, ok := sp.significantPoints[sigpt]; !ok {
						return CommandStatus{}, ErrSTARSCommandFormat
					} else {
						return sp.displaySignificantPointInfo(pos, sig.Location, ctx.NmPerLongitude, ctx.MagneticVariation), nil
					}
				},
				"*F [POS]", func(sp *STARSPane, pos2 math.Point2LL) CommandStatus {
					return sp.displaySignificantPointInfo(pos, pos2, ctx.NmPerLongitude, ctx.MagneticVariation)
				},
			),
		}
	})

	// 6.19 Time out warning (TOW) list commands

	// 6.20 Toggle track highlight: handled in commands.go

	// 6.21.1 Enable / inhibit display of TPA / ATPA graphics and data (DCB command)

	// 6.21.2 Display TPA J-Ring for a single track (implied)
	// 6.21.3 Modify TPA J-Ring radius for single track (Implied command
	registerCommand(CommandModeNone, "*J[TPA_FLOAT][SLEW]", func(radius float32, state *TrackState) error {
		// FIXME: *J is not allowed if the track has a ghost

		if radius < 1 || radius > 30 {
			return ErrSTARSIllegalValue
		}
		state.JRingRadius = radius
		state.ConeLength = 0 // can't have both
		return nil
	})

	// 6.21.4 Delete TPA J-Ring for single track (Implied command)
	registerCommand(CommandModeNone, "*J[SLEW]", func(state *TrackState) {
		state.JRingRadius = 0
	})

	// 6.21.5 Delete TPA J-Rings for all tracks (Implied command)
	registerCommand(CommandModeNone, "**J", func(sp *STARSPane) {
		for _, state := range sp.TrackState {
			state.JRingRadius = 0
		}
	})

	// 6.21.6 Display TPA cone for single track (implied)
	// 6.21.7 Modify TPA Cone length for single track (Implied command)
	registerCommand(CommandModeNone, "*P[TPA_FLOAT][SLEW]", func(length float32, state *TrackState) error {
		if length < 1 || length > 30 {
			return ErrSTARSIllegalValue
		}
		// TODO: ILL FNCT if cone length is less than allowed in-trail separation (?)
		state.ConeLength = length
		state.JRingRadius = 0 // can't have both
		return nil
	})

	// 6.21.8 Delete TPA Cone for single track (Implied command) (p. 6-178)
	registerCommand(CommandModeNone, "*P[SLEW]", func(state *TrackState) {
		state.ConeLength = 0
	})

	// 6.21.9 Delete TPA Cones for all tracks (Implied command) (p. 6-179)
	registerCommand(CommandModeNone, "**P", func(sp *STARSPane) {
		for _, state := range sp.TrackState {
			state.ConeLength = 0
		}
	})

	// 6.21.10 Toggle display of TPA / ATPA size data for single track (Implied command)
	registerCommand(CommandModeNone, "*D+[SLEW]", func(ps *Preferences, state *TrackState) {
		if state.DisplayTPASize == nil {
			b := ps.DisplayTPASize // new variable; don't alias ps.DisplayTPASize!
			state.DisplayTPASize = &b
		}
		*state.DisplayTPASize = !*state.DisplayTPASize
	})
	registerCommand(CommandModeNone, "*D+E[SLEW]", func(state *TrackState) {
		b := true
		state.DisplayTPASize = &b
	})
	registerCommand(CommandModeNone, "*D+I[SLEW]", func(state *TrackState) {
		b := false
		state.DisplayTPASize = &b
	})

	// 6.21.11 Toggle display of TPA / ATPA size data for all tracks (Implied command)
	registerCommand(CommandModeNone, "*D+", func(sp *STARSPane, ps *Preferences) CommandStatus {
		ps.DisplayTPASize = !ps.DisplayTPASize
		for _, state := range sp.TrackState {
			state.DisplayTPASize = nil
		}
		return CommandStatus{Output: util.Select(ps.DisplayTPASize, "TPA SIZE ON", "TPA SIZE OFF")}
	})
	registerCommand(CommandModeNone, "*D+E", func(sp *STARSPane, ps *Preferences) CommandStatus {
		ps.DisplayTPASize = true
		for _, state := range sp.TrackState {
			state.DisplayTPASize = nil
		}
		return CommandStatus{Output: "TPA SIZE ON"}
	})
	registerCommand(CommandModeNone, "*D+I", func(sp *STARSPane, ps *Preferences) CommandStatus {
		ps.DisplayTPASize = false
		for _, state := range sp.TrackState {
			state.DisplayTPASize = nil
		}
		return CommandStatus{Output: "TPA SIZE OFF"}
	})

	// 6.21.12 Enable / inhibit ATPA Warning and Alert Cones for single track (Implied command)
	setATPAWarnAlertConeState := func(sp *STARSPane, ps *Preferences, trk *sim.Track, value bool) error {
		if !ps.DisplayATPAWarningAlertCones {
			return ErrSTARSIllegalFunction
		}
		if !trk.IsAssociated() || trk.ATPAVolume == nil {
			return ErrSTARSIllegalTrack
		}
		// TODO: ILL TRK if VFR or not in an ATPA approach volume

		state := sp.TrackState[trk.ADSBCallsign]
		state.DisplayATPAWarnAlert = &value
		return nil
	}
	registerCommand(CommandModeNone, "*AE[SLEW]", func(sp *STARSPane, ps *Preferences, trk *sim.Track) error {
		return setATPAWarnAlertConeState(sp, ps, trk, true)
	})
	registerCommand(CommandModeNone, "*AI[SLEW]", func(sp *STARSPane, ps *Preferences, trk *sim.Track) error {
		return setATPAWarnAlertConeState(sp, ps, trk, false)
	})

	// 6.21.13 Enable / inhibit ATPA Warning and Alert Cones for this TCW/TDW (implied)
	registerCommand(CommandModeNone, "*AE", func(ps *Preferences) {
		ps.DisplayATPAWarningAlertCones = true
	})
	registerCommand(CommandModeNone, "*AI", func(ps *Preferences) {
		ps.DisplayATPAWarningAlertCones = false
	})

	// 6.21.14 Enable / inhibit ATPA Monitor Cone for single track (Implied command)
	setATPAMonitorState := func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track, value bool) error {
		if !ps.DisplayATPAWarningAlertCones {
			return ErrSTARSIllegalFunction
		}
		if !trk.IsAssociated() || trk.ATPAVolume == nil || !ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			return ErrSTARSIllegalTrack
		}

		state := sp.TrackState[trk.ADSBCallsign]
		state.DisplayATPAMonitor = &value
		return nil
	}
	registerCommand(CommandModeNone, "*BE[SLEW]", func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track) error {
		return setATPAMonitorState(sp, ctx, ps, trk, true)
	})
	registerCommand(CommandModeNone, "*BI[SLEW]", func(sp *STARSPane, ctx *panes.Context, ps *Preferences, trk *sim.Track) error {
		return setATPAMonitorState(sp, ctx, ps, trk, false)
	})

	// 6.21.15 Enable / inhibit ATPA Monitor Cones for this TCW/TDW (implied)
	registerCommand(CommandModeNone, "*BE", func(ps *Preferences) {
		ps.DisplayATPAMonitorCones = true
	})
	registerCommand(CommandModeNone, "*BI", func(ps *Preferences) {
		ps.DisplayATPAMonitorCones = false
	})

	// 6.21.16 Enable / inhibit in-trail distance for single track (implied)
	setTrackDisplayInTrail := func(sp *STARSPane, ps *Preferences, trk *sim.Track, value bool) error {
		if !trk.IsAssociated() || trk.ATPAVolume == nil {
			return ErrSTARSIllegalTrack
		}
		state := sp.TrackState[trk.ADSBCallsign]
		state.InhibitDisplayInTrailDist = value
		return nil
	}
	registerCommand(CommandModeNone, "*DE[SLEW]", func(sp *STARSPane, ps *Preferences, trk *sim.Track) error {
		return setTrackDisplayInTrail(sp, ps, trk, true)
	})
	registerCommand(CommandModeNone, "*DI[SLEW]", func(sp *STARSPane, ps *Preferences, trk *sim.Track) error {
		return setTrackDisplayInTrail(sp, ps, trk, false)
	})

	// 6.21.17 Enable / inhibit in-trail distances for this TCW/TDW (implied)
	registerCommand(CommandModeNone, "*DE", func(ps *Preferences) {
		ps.DisplayATPAInTrailDist = true
	})
	registerCommand(CommandModeNone, "*DI", func(ps *Preferences) {
		ps.DisplayATPAInTrailDist = false
	})

	// 6.21.18 Display disabled ATPA approach volumes in preview area
	// registerCommand(CommandModeMultiFunc, "+", func(sp *STARSPane) CommandStatus {	})

	// 6.21.19 Enable / display override of ATPA exclusion criteria for a single track
	// registerCommand(CommandModeNone, "*VE[SLEW]", ...)
	// registerCommand(CommandModeNone, "*VI[SLEW]", ...)

	// 6.22 Display sensor tile information for selected location
	// registerCommand(CommandModeMultiFunc, "ZR[POS]", ...)

	// 6.23 Display general terrain information for selected location
	// registerCommand(CommandModeMultiFunc, "ZT[POS]", ...)

	// 6.24 Display altimeter tile information for selected location
	// registerCommand(CommandModeMultiFunc, "A[POS]", ...)

	// 6.25 ADS-B Ground Station List Commands...

	// 6.26 Display flight plan flush time
	// registerCommand(CommandModeMultiFunc, "TF", ...)

	// 6.27 Display enable / disable states of total filters
	// registerCommand(CommandModeMultiFunc, "2TS", ...)

	// 6.28 Display track information in preview area
	// registerCommand(CommandModeMultiFunc, "Z[TRK_ACID]|Z[TRK_BCN]|Z[TRK_INDEX]|Z[SLEW]", ...)

	// 6.29 Terminal sequencing and spacing (TSAS) commands...
}

// trackInCRDARegion checks if a track is inside any enabled CRDA region.
func trackInCRDARegion(sp *STARSPane, ctx *panes.Context, trk *sim.Track) bool {
	ps := sp.currentPrefs()
	state := sp.TrackState[trk.ADSBCallsign]

	for i, pairState := range ps.CRDA.RunwayPairState {
		if !pairState.Enabled {
			continue
		}
		for j, rwyState := range pairState.RunwayState {
			if !rwyState.Enabled {
				continue
			}
			region := sp.CRDAPairs[i].CRDARegions[j]
			if lat, _ := region.Inside(state.track.Location, trk.TrueAltitude,
				ctx.NmPerLongitude); lat {
				return true
			}
		}
	}
	return false
}

func displayQLStatus(sp *STARSPane, ctx *panes.Context) CommandStatus {
	ps := sp.currentPrefs()
	var output string
	if ps.QuickLookAll {
		output = "ALL"
		if ps.QuickLookAllIsPlus {
			output += "+"
		}
	} else {
		output = sp.qlPositionsString()
	}

	var active, inactive []string
	for _, f := range ctx.FacilityAdaptation.Filters.Quicklook {
		if _, ok := ps.DisabledQLRegions[f.Id]; ok {
			inactive = append(inactive, f.Id)
		} else {
			active = append(active, f.Id)
		}
	}
	appendRegions := func(regions []string, ty string) {
		if len(regions) == 0 {
			return
		}
		if output != "" {
			output += "\n"
		}
		output += ty + " QUICKLOOK REGIONS\n"
		slices.Sort(regions)
		out, _ := util.TextWrapConfig{
			ColumnLimit: 32,
		}.Wrap(strings.Join(regions, " "))
		output += out
	}

	appendRegions(active, "ACTIVE")
	appendRegions(inactive, "INACTIVE")

	return CommandStatus{Output: output}
}
