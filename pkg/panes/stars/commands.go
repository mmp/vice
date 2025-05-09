// pkg/panes/stars/commands.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/davecgh/go-spew/spew"
)

// Cache the routes we show when paused but periodically fetch them
var pausedAircraftInfo *util.TransientMap[av.ADSBCallsign, string] = util.NewTransientMap[av.ADSBCallsign, string]()

type CommandMode int

const (
	// Keyboard command entry modes; can be main or DCB menu for these; sp.dcbShowAux decides.
	CommandModeNone CommandMode = iota
	CommandModeInitiateControl
	CommandModeTerminateControl
	CommandModeHandOff
	CommandModeVFRPlan
	CommandModeMultiFunc
	CommandModeFlightData
	CommandModeCollisionAlert
	CommandModeMin
	CommandModeTargetGen
	CommandModeReleaseDeparture
	CommandModeRestrictionArea
	CommandModeDrawRoute

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

func (c CommandMode) PreviewString() string {
	switch c {
	case CommandModeNone:
		return ""
	case CommandModeInitiateControl:
		return "IC"
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
		if !TargetGenLock {
			return "TG"
		} else {
			return "TG LOCK"
		}
	case CommandModeReleaseDeparture:
		return "RD"
	case CommandModeRestrictionArea:
		return "AR"
	case CommandModeDrawRoute:
		return "DRAWROUTE"
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

type CommandStatus struct {
	clear  bool
	output string
	err    error
}

var TargetGenLock bool

func (sp *STARSPane) processKeyboardInput(ctx *panes.Context, tracks []sim.Track) {
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
			if status := sp.executeSTARSCommand(ctx, sp.previewAreaInput, tracks); status.err != nil {
				sp.displayError(status.err, ctx, "")
			} else {
				if status.clear && !TargetGenLock {
					sp.setCommandMode(ctx, CommandModeNone)
					sp.maybeAutoHomeCursor(ctx)
				} else {
					sp.setCommandMode(ctx, CommandModeTargetGen)
				}
				sp.previewAreaOutput = status.output
			}

		case imgui.KeyEscape:
			if sp.activeSpinner != nil {
				sp.setCommandMode(ctx, sp.activeSpinner.EscapeMode())
			} else {
				sp.setCommandMode(ctx, CommandModeNone)
				TargetGenLock = false // unlock target generation
			}

		case imgui.KeyF1:
			if ctx.Keyboard.KeyControl() {
				// Recenter
				ps.UseUserCenter = false
			}
			if ctx.Keyboard.KeyShift() {
				// Treat this as F13
				sp.setCommandMode(ctx, CommandModeReleaseDeparture)
			}

		case imgui.KeyF2:
			if ctx.Keyboard.KeyControl() {
				sp.setCommandMode(ctx, CommandModeMaps)
			}

		case imgui.KeyF3:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeBrite)
			} else {
				sp.setCommandMode(ctx, CommandModeInitiateControl)
			}

		case imgui.KeyF4:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeLDR)
			} else {
				sp.setCommandMode(ctx, CommandModeTerminateControl)
			}

		case imgui.KeyF5:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeCharSize)
			} else {
				sp.setCommandMode(ctx, CommandModeHandOff)
			}

		case imgui.KeyF6:
			sp.setCommandMode(ctx, CommandModeFlightData)

		case imgui.KeyF7:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeNone)
				sp.dcbShowAux = !sp.dcbShowAux
			} else {
				sp.setCommandMode(ctx, CommandModeMultiFunc)
			}

		case imgui.KeyF8:
			if ctx.Keyboard.KeyControl() {
				sp.resetInputState(ctx)
				ps.DisplayDCB = !ps.DisplayDCB
			} else {
				sp.setCommandMode(ctx, CommandModeWX)
			}

		case imgui.KeyF9:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRangeRings)
			} else {
				sp.setCommandMode(ctx, CommandModeVFRPlan)
			}

		case imgui.KeyF10:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRange)
			}

		case imgui.KeyF11:
			if ctx.Keyboard.KeyControl() && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeSite)
			} else {
				sp.setCommandMode(ctx, CommandModeCollisionAlert)
			}

		case imgui.KeyF12:
			sp.setCommandMode(ctx, CommandModeRestrictionArea)

		case imgui.KeyF13:
			sp.setCommandMode(ctx, CommandModeReleaseDeparture)

		case imgui.KeyInsert:
			sp.setCommandMode(ctx, CommandModePref)

		case imgui.KeyTab:
			if imgui.IsKeyDown(imgui.KeyLeftShift) { // Check if LeftShift is pressed
				TargetGenLock = true
				sp.setCommandMode(ctx, CommandModeTargetGen)
			} else {
				sp.setCommandMode(ctx, CommandModeTargetGen)
			}
		}
	}
}

func (sp *STARSPane) executeSTARSCommand(ctx *panes.Context, cmd string, tracks []sim.Track) (status CommandStatus) {
	// If there's an active spinner, it gets keyboard input; we thus won't
	// worry about the corresponding CommandModes in the following.
	if sp.activeSpinner != nil {
		if mode, err := sp.activeSpinner.KeyboardInput(cmd); err != nil {
			status.err = err
		} else {
			// Clear the input area, and disable the spinner's mouse
			// capture, and switch to the indicated command mode.
			sp.setCommandMode(ctx, mode)
		}
		return
	}

	lookupTrack := func(s string) *sim.Track {
		if trk, ok := ctx.GetTrackByACID(sim.ACID(s)); ok {
			return trk
		}

		// try to match squawk code
		if sq, err := av.ParseSquawk(s); err == nil {
			for _, trk := range tracks {
				if trk.Squawk == sq {
					return &trk
				}
			}
		}

		if idx, err := strconv.Atoi(s); err == nil {
			if trk, ok := util.SeqLookupFunc(maps.Values(ctx.Client.State.Tracks),
				func(trk *sim.Track) bool {
					return trk.IsAssociated() && trk.FlightPlan.ListIndex == idx
				}); ok {
				return trk
			}
		}

		return nil
	}

	lookupFlightPlan := func(s string) (*sim.STARSFlightPlan, *sim.Track) {
		sq, err := av.ParseSquawk(s)
		if err != nil {
			sq = av.Squawk(0)
		}
		idx, err := strconv.Atoi(s)
		if err != nil {
			idx = -1
		}

		state := &ctx.Client.State
		for _, trk := range state.Tracks {
			if trk.IsAssociated() && (trk.FlightPlan.ACID == sim.ACID(s) ||
				trk.FlightPlan.AssignedSquawk == sq || trk.FlightPlan.ListIndex == idx) {
				return trk.FlightPlan, trk
			}
		}
		for i, fp := range state.UnassociatedFlightPlans {
			if fp.ACID == sim.ACID(s) || fp.AssignedSquawk == sq || fp.ListIndex == idx {
				return state.UnassociatedFlightPlans[i], nil
			}
		}

		return nil, nil
	}

	ps := sp.currentPrefs()
	switch sp.commandMode {
	case CommandModeNone:
		switch cmd {
		case "*AE":
			// Enable ATPA warning/alert cones
			ps.DisplayATPAWarningAlertCones = true
			status.clear = true
			return

		case "*AI":
			// Inhibit ATPA warning/alert cones
			ps.DisplayATPAWarningAlertCones = false
			status.clear = true
			return

		case "*BE":
			// Enable ATPA monitor cones
			ps.DisplayATPAMonitorCones = true
			status.clear = true
			return

		case "*BI":
			// Inhibit ATPA monitor cones
			ps.DisplayATPAMonitorCones = false
			status.clear = true
			return

		case "*DE":
			// Enable ATPA in-trail distances
			ps.DisplayATPAInTrailDist = true
			status.clear = true
			return

		case "*DI":
			// Inhibit ATPA in-trail distances
			ps.DisplayATPAInTrailDist = false
			status.clear = true
			return

		case "*D+":
			// Toggle
			ps.DisplayTPASize = !ps.DisplayTPASize
			for _, state := range sp.TrackState {
				state.DisplayTPASize = nil
			}
			status.output = util.Select(ps.DisplayTPASize, "TPA SIZE ON", "TPA SIZE OFF")
			status.clear = true
			return

		case "*D+E":
			// Enable
			ps.DisplayTPASize = true
			for _, state := range sp.TrackState {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE ON"
			return

		case "*D+I":
			// Inhibit
			ps.DisplayTPASize = false
			for _, state := range sp.TrackState {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE OFF"
			return

		case "**J":
			// remove all j-rings
			for _, state := range sp.TrackState {
				state.JRingRadius = 0
			}
			status.clear = true
			return

		case "**P":
			// remove all cones
			for _, state := range sp.TrackState {
				state.ConeLength = 0
			}
			status.clear = true
			return

		case ".ROUTE":
			sp.drawRouteAircraft = ""
			status.clear = true
			return

		case ".DRAWROUTE":
			sp.setCommandMode(ctx, CommandModeDrawRoute)
			return

		case ".VFR":
			sp.showVFRAirports = !sp.showVFRAirports
			status.clear = true
			return

			/*
				case "?":
					ctx.Client.State.ERAMComputers.DumpMap()
					status.clear = true
					return
			*/

		case "CR":
			if sp.capture.enabled && (sp.capture.specifyingRegion || sp.capture.haveRegion) {
				sp.capture.specifyingRegion = false
				sp.capture.haveRegion = false
				status.clear = true
				return
			}

		case "CS":
			if sp.capture.enabled {
				sp.capture.doStill = true
				status.clear = true
				return
			}

		case "CV":
			if sp.capture.enabled {
				sp.capture.doVideo = !sp.capture.doVideo
				status.clear = true
				return
			}
		}

		if len(cmd) == 6 && strings.HasPrefix(cmd, "**") {
			// 6-117 Selected beacon code display
			code, err := av.ParseSquawk(cmd[2:])
			if err != nil {
				status.err = ErrSTARSIllegalCode
			} else if !util.SeqContainsFunc(maps.Values(ctx.Client.State.Tracks),
				func(trk *sim.Track) bool { return trk.Squawk == code }) {
				status.err = ErrSTARSNoTrack
			} else {
				sp.DisplayBeaconCode = code
				sp.DisplayBeaconCodeEndTime = ctx.Now.Add(15 * time.Second)
				status.clear = true
			}
			return
		}

		if len(cmd) > 5 && cmd[:2] == "**" { // Force QL
			// Manual 6-69
			cmd = cmd[2:]

			callsign, tcps, _ := strings.Cut(cmd, " ")
			trk := lookupTrack(callsign)
			if trk == nil {
				status.err = ErrSTARSNoFlight
			} else if trk.IsUnassociated() {
				status.err = ErrSTARSIllegalTrack
			} else {
				for _, tcp := range strings.Split(tcps, " ") {
					if tcp == "ALL" {
						fac := ctx.Client.State.Controllers[ctx.UserTCP].FacilityIdentifier
						for _, control := range ctx.Client.State.Controllers {
							if !control.ERAMFacility && control.FacilityIdentifier == fac {
								sp.forceQL(ctx, trk.FlightPlan.ACID, control.Id())
							}
						}
					} else {
						control := sp.lookupControllerForId(ctx, tcp, trk.FlightPlan.ACID)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
							return
						}
						sp.forceQL(ctx, trk.FlightPlan.ACID, control.Id())
					}
				}
				status.clear = true
				return
			}
		}

		if len(cmd) >= 2 && cmd[:2] == "*T" {
			suffix := cmd[2:]
			if suffix == "" {
				// Remove all RBLs
				sp.wipRBL = nil
				sp.RangeBearingLines = nil
				status.clear = true
			} else if idx, err := strconv.Atoi(cmd[2:]); err == nil {
				// Delete specified rbl
				idx--
				if idx >= 0 && idx < len(sp.RangeBearingLines) {
					sp.RangeBearingLines = util.DeleteSliceElement(sp.RangeBearingLines, idx)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
			} else if p, ok := ctx.Client.State.Locate(suffix); ok {
				// Fix name for first or second point of RBL
				if rbl := sp.wipRBL; rbl != nil {
					rbl.P[1].Loc = p
					sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
					sp.wipRBL = nil
					status.clear = true
				} else {
					sp.wipRBL = &STARSRangeBearingLine{}
					sp.wipRBL.P[0].Loc = p
					sp.scopeClickHandler = rblSecondClickHandler(ctx, sp, tracks)
					sp.previewAreaInput = "*T" // set up for the second point
				}
			} else {
				status.err = ErrSTARSIllegalFix
			}
			return
		}

		if len(cmd) > 3 && cmd[:3] == "*F " && sp.wipSignificantPoint != nil {
			if sig, ok := sp.significantPoints[cmd[3:]]; ok {
				status = sp.displaySignificantPointInfo(*sp.wipSignificantPoint, sig.Location,
					ctx.NmPerLongitude, ctx.MagneticVariation)
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return
		}

		if (len(cmd) == 5 || len(cmd) == 6) && cmd[0] == '*' {
			if sq, err := av.ParseSquawk(cmd[1:5]); err == nil {
				// 5-141 create quick flight plan
				base := ctx.FacilityAdaptation.FlightPlan.QuickACID
				acid := sim.ACID(base + sq.String())

				if _, ok := ctx.Client.State.GetTrackByACID(acid); ok {
					status.err = ErrSTARSDuplicateACID
					return
				}
				if ctx.Client.State.BeaconCodeInUse(sq) {
					status.err = ErrSTARSIllegalCode
					return
				}

				var spec sim.STARSFlightPlanSpecifier
				spec.ACID.Set(acid)
				spec.AssignedSquawk.Set(sq)
				spec.CoordinationTime.Set(ctx.Now)
				switch cmd[5:] {
				case "":
					// no flight rules, no problem
				case "V", "P": // VFR or VFR on top
					spec.Rules.Set(av.FlightRulesVFR)
				case "E": // enroute
					spec.Rules.Set(av.FlightRulesIFR)
				default:
					status.err = ErrSTARSIllegalValue
					return
				}
				sp.createFlightPlan(ctx, spec)
				status.clear = true
				return
			}
		}
		if len(cmd) > 0 {
			if cmd == "ALL" {
				if ps.QuickLookAll && ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = false
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else if cmd == "ALL+" {
				if ps.QuickLookAll && !ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = true
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else if cmd == ctx.UserTCP { // TODO: any TCP assigned to this scope
				// 6-91 show QL information in the preview area
				if ps.QuickLookAll {
					status.output = "ALL"
					if ps.QuickLookAllIsPlus {
						status.output += "+"
					}
				} else {
					pstrs := util.MapSlice(ps.QuickLookPositions, func(p QuickLookPosition) string { return p.String() })
					status.output = strings.Join(pstrs, " ")
				}
				status.clear = true
				return
			} else if _, err := sp.updateQL(ctx, cmd); err == nil {
				// It was valid quicklook positions
				status.clear = true
				return
			} else if isVFRFlightPlan(cmd) {
				if spec, err := parseFlightPlan("+ACID+VFR_ARR_FIXES+AC_TYPE/EQ?ALT_R?TCP", cmd, nil); err == nil {
					// Implied entry of VFR FP: 5-94
					spec.Rules.Set(av.FlightRulesVFR)
					spec.TypeOfFlight.Set(av.FlightTypeArrival)
					sp.createFlightPlan(ctx, spec)
					status.clear = true
				} else {
					status.err = err
				}
				return
			} else if spec, err := parseFlightPlan("+ACID?BEACON,TCP,FLT_TYPE,TRI_SP1,PLUS_SP2,#/AC_TYPE/EQ,ALT_R,RULES", cmd,
				func(s string, primary bool) bool {
					return checkScratchpad(ctx, s, !primary, true /* implied */) == nil
				}); err == nil {
				// Implied entry of abbreviated flight plan 5-89
				spec.Rules.Set(av.FlightRulesIFR)
				spec.TypeOfFlight.Set(av.FlightTypeArrival)
				sp.createFlightPlan(ctx, spec)
				status.clear = true
				return
			} else {
				status.err = err
				return
			}
		}

	case CommandModeInitiateControl:
		checkfp := func(s string, primary bool) bool { return checkScratchpad(ctx, s, !primary, false) == nil }
		if spec, err := parseFlightPlan("+ACID+BEACON?TRI_SP1,PLUS_SP2,#/AC_TYPE/EQ", cmd, checkfp); err == nil {
			// Pending FP with discrete beacon code 5-120
			spec.Rules.Set(av.FlightRulesIFR)
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			spec.AutoAssociate = true
			sp.createFlightPlan(ctx, spec)
			status.clear = true
		} else {
			status.err = err
		}
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for _, trk := range tracks {
				if (trk.IsAssociated() || trk.IsUnsupportedDB()) && trk.FlightPlan.TrackingController == ctx.UserTCP {
					sp.deleteFlightPlan(ctx, trk.FlightPlan.ACID)
				}
			}
			status.clear = true
		} else if fp := ctx.Client.State.FindMatchingFlightPlan(cmd); fp != nil {
			sp.deleteFlightPlan(ctx, fp.ACID)
			status.clear = true
		} else {
			status.err = ErrSTARSNoFlight
		}
		return

	case CommandModeHandOff:
		f := strings.Fields(cmd)
		switch len(f) {
		case 0:
			// Accept hand off of target closest to range rings center
			var closest *sim.Track
			var closestDistance float32
			for _, trk := range tracks {
				if trk.IsUnassociated() || trk.FlightPlan.HandoffTrackController != ctx.UserTCP {
					continue
				}

				ctr := util.Select(ps.UseUserRangeRingsCenter, ps.RangeRingsUserCenter, ps.DefaultCenter)
				d := math.NMDistance2LL(ctr, trk.Location)
				if closest == nil || d < closestDistance {
					closest = &trk
					closestDistance = d
				}
			}

			if closest != nil {
				sp.acceptHandoff(ctx, closest.FlightPlan.ACID)
			}
			status.clear = true
			return
		case 1:
			// Is it an ACID?
			if trk := lookupTrack(f[0]); trk != nil {
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					sp.cancelHandoff(ctx, trk.FlightPlan.ACID)
					status.clear = true
				}
			} else {
				// Enabling / disabling automatic handoff processing, 4-30
				switch f[0] {
				case "CXE":
					ps.AutomaticHandoffs.Interfacility = true
					status.clear = true
				case "CXI":
					ps.AutomaticHandoffs.Interfacility = false
					status.clear = true
				case "CTE":
					ps.AutomaticHandoffs.Intrafacility = true
					status.clear = true
				case "CTI":
					ps.AutomaticHandoffs.Intrafacility = false
					status.clear = true
				case "CE":
					ps.AutomaticHandoffs.Intrafacility = true
					ps.AutomaticHandoffs.Interfacility = true
					status.clear = true
				case "CI":
					ps.AutomaticHandoffs.Intrafacility = false
					ps.AutomaticHandoffs.Interfacility = false
					status.clear = true
				default:
					status.err = ErrSTARSCommandFormat
				}
			}
			return

		case 2:
			if trk := lookupTrack(f[1]); trk == nil {
				status.err = ErrSTARSNoFlight
			} else if trk.IsUnassociated() {
				status.err = ErrSTARSIllegalTrack
			} else if err := sp.handoffTrack(ctx, trk.FlightPlan.ACID, f[0]); err != nil {
				status.err = err
			} else {
				status.clear = true
			}
			return
		}

	case CommandModeVFRPlan:
		if len(cmd) > 0 && !strings.Contains(cmd, " ") {
			// 5-85: delete VFR FP from VFR list
			fps := ctx.Client.State.UnassociatedFlightPlans
			if n, err := strconv.Atoi(cmd); err == nil { // line number
				if idx := slices.IndexFunc(fps, func(fp *sim.STARSFlightPlan) bool {
					return fp.ListIndex == n && fp.Rules == av.FlightRulesVFR
				}); idx != -1 {
					sp.deleteFlightPlan(ctx, fps[idx].ACID)
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalTrack
				}
			} else if idx := slices.IndexFunc(fps, func(fp *sim.STARSFlightPlan) bool {
				return fp.ACID == sim.ACID(cmd) && fp.Rules == av.FlightRulesVFR
			}); idx != -1 {
				sp.deleteFlightPlan(ctx, fps[idx].ACID)
				status.clear = true
			} else {
				status.err = ErrSTARSIllegalTrack
			}
			return
		} else if spec, err := parseFlightPlan("+ACID+VFR_ARR_FIXES+AC_TYPE/EQ?ALT_R?TCP", cmd, nil); err == nil {
			// Create/modify VFR FP: 5-133
			spec.Rules.Set(av.FlightRulesVFR)
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			sp.createFlightPlan(ctx, spec)
			status.clear = true
		} else {
			status.err = err
		}
		return

	case CommandModeMultiFunc:
		switch sp.multiFuncPrefix {
		case "2":
			// 4-29
			if cmd == "PE" {
				ps.InhibitPositionSymOnUnassociatedPrimary = false
				status.clear = true
				return
			} else if cmd == "PI" {
				ps.InhibitPositionSymOnUnassociatedPrimary = true
				status.clear = true
				return
			}

		case "B":
			if cmd == "" {
				// B -> for unassociated track, toggle display of beacon code in LDB
				ps.DisplayLDBBeaconCodes = !ps.DisplayLDBBeaconCodes
				status.clear = true
				return
			} else if cmd == "E" {
				// BE -> enable display of beacon code in ldbs
				ps.DisplayLDBBeaconCodes = true
				status.clear = true
				return
			} else if cmd == "I" {
				// BI -> inhibit display of beacon code in ldbs
				ps.DisplayLDBBeaconCodes = false
				status.clear = true
				return
			} else if sq, err := av.ParseSquawkOrBlock(cmd); err == nil {
				// B[0-7][0-7] -> toggle select beacon code block
				// B[0-7][0-7][0-7][0-7] -> toggle select discrete beacon code
				if idx := slices.Index(ps.SelectedBeacons, sq); idx != -1 {
					ps.SelectedBeacons = slices.Delete(ps.SelectedBeacons, idx, idx+1)
					status.clear = true
				} else if len(ps.SelectedBeacons) == 10 {
					status.err = ErrSTARSCapacity
				} else {
					ps.SelectedBeacons = append(ps.SelectedBeacons, sq)
					slices.Sort(ps.SelectedBeacons)
					status.clear = true
				}
				return
			} else if cmd == "*" {
				clear(ps.SelectedBeacons)
				status.clear = true
				return
			}

		case "D":
			if cmd == "E" {
				ps.DwellMode = DwellModeOn
				status.clear = true
			} else if cmd == "L" {
				ps.DwellMode = DwellModeLock
				status.clear = true
			} else if cmd == "I" { // inhibit
				ps.DwellMode = DwellModeOff
				status.clear = true
			} else if len(cmd) == 1 {
				// illegal value for dwell
				status.err = ErrSTARSIllegalValue
			} else if fp, trk := lookupFlightPlan(cmd); fp != nil {
				// D(callsign) / D(beacon) / D(line #)
				// Display flight plan: 5-186
				status.output = sp.formatFlightPlan(ctx, fp, trk)
				if ctx.FacilityAdaptation.FlightPlan.ModifyAfterDisplay {
					// This goes into [multifunc]M mode while the flight plan
					// is displayed, so status.clear is *not* set; we add a
					// space so that the modification entry can be entered
					// directly.
					sp.multiFuncPrefix = "M"
					sp.previewAreaInput += " "
				} else {
					status.clear = true
				}
			} else {
				status.err = ErrSTARSNoFlight
			}
			return

		case "E":
			switch cmd {
			case "":
				ps.OverflightFullDatablocks = !ps.OverflightFullDatablocks
				status.clear = true
			case "E":
				ps.OverflightFullDatablocks = true
				status.clear = true
			case "I":
				ps.OverflightFullDatablocks = false
				status.clear = true
			default:
				status.err = ErrSTARSCommandFormat
			}
			return

		case "F":
			// altitude filters
			af := &ps.AltitudeFilters
			if cmd == "" {
				// F -> display current in preview area
				status.output = fmt.Sprintf("%03d %03d\n%03d %03d",
					af.Unassociated[0]/100, af.Unassociated[1]/100,
					af.Associated[0]/100, af.Associated[1]/100)
				status.clear = true
				return
			} else if cmd[0] == 'C' {
				// FC(low associated)(high associated)
				if len(cmd[1:]) != 6 {
					status.err = ErrSTARSCommandFormat
				} else if digits, err := strconv.Atoi(cmd[1:]); err == nil {
					// TODO: validation?
					// The first three digits give the low altitude in 100s of feet
					af.Associated[0] = (digits / 1000) * 100
					// And the last three give the high altitude in 100s of feet
					af.Associated[1] = (digits % 1000) * 100
				} else {
					status.err = ErrSTARSIllegalParam
				}
				status.clear = true
				return
			} else {
				// F(low unassociated)(high unassociated) (low associated)(high associated)
				if len(cmd) != 13 {
					status.err = ErrSTARSCommandFormat
				} else {
					unassoc, assoc := cmd[0:6], cmd[7:13]
					if digits, err := strconv.Atoi(unassoc); err == nil {
						// TODO: more validation?
						af.Unassociated[0] = (digits / 1000) * 100
						// And the last three give the high altitude in 100s of feet
						af.Unassociated[1] = (digits % 1000) * 100

						if digits, err := strconv.Atoi(assoc); err == nil {
							// TODO: more validation?
							af.Associated[0] = (digits / 1000) * 100
							// And the last three give the high altitude in 100s of feet
							af.Associated[1] = (digits % 1000) * 100
						} else {
							status.err = ErrSTARSIllegalParam
						}
					} else {
						status.err = ErrSTARSIllegalParam
					}
				}
				status.clear = true
				return
			}

		case "H":
			// 5-178: modify rnav symbol, a/c type, eq suffix, or flight rules
			if id, mod, ok := strings.Cut(cmd, " "); !ok {
				status.err = ErrSTARSCommandFormat
			} else if trk := lookupTrack(id); trk == nil || trk.IsUnassociated() {
				status.err = ErrSTARSIllegalTrack
			} else if spec, err := parseOneFlightPlan("RNAV,#/AC_TYPE/EQ,RULES", mod, nil); err != nil {
				status.err = err
			} else {
				sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
				status.clear = true
			}
			return

		case "I":
			if cmd == "*" {
				// I* clears the status area(?!)
				status.clear = true
				return
			} else if cmd == "HS" { // enable auto cursor home
				ps.AutoCursorHome = true
				status.clear = true
				status.output = "HOME"
				return
			} else if cmd == "NH" { // disable auto cursor home
				ps.AutoCursorHome = false
				status.clear = true
				status.output = "NO HOME"
				return
			}

		case "K":
			if cmd == "" { // 4-21: reset to default prefs
				sp.prefSet.ResetDefault(ctx.Client.State, ctx.Platform, sp)
			}

		case "L":
			// leader lines
			if l := len(cmd); l == 1 {
				if dir, ok := sp.numpadToDirection(cmd[0]); ok && dir != nil {
					// 4-97: tracked by me, '5' not allowed
					ps.LeaderLineDirection = *dir
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
			} else if l == 2 {
				if dir, ok := sp.numpadToDirection(cmd[0]); ok && dir != nil && cmd[1] == 'U' {
					// 4-101: unassociated tracks; '5' is not allowed here.
					ps.UnassociatedLeaderLineDirection = dir
					status.clear = true
				} else if ok && cmd[1] == '*' {
					// 4-98: tracked by other controllers
					ps.OtherControllerLeaderLineDirection = dir
					// This also clears out any controller-specific assignments (4-98)
					clear(ps.ControllerLeaderLineDirections)
					status.clear = true
				} else if cmd == "5*" {
					// Remove setting for other controllers
					ps.OtherControllerLeaderLineDirection = nil
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
			} else if len(cmd) >= 3 {
				// 4-99: track owned by a specific TCP: L(tcp)(dir),(where
				// tcp has a space if it's given as a single character).
				tcp := strings.TrimSuffix(cmd[:2], " ")
				if controller := sp.lookupControllerForId(ctx, tcp, ""); controller != nil {
					if dir, ok := sp.numpadToDirection(cmd[2]); ok {
						// Per-controller leaderline
						if ps.ControllerLeaderLineDirections == nil {
							ps.ControllerLeaderLineDirections = make(map[string]math.CardinalOrdinalDirection)
						}
						if dir != nil {
							ps.ControllerLeaderLineDirections[controller.Id()] = *dir
						} else {
							delete(ps.ControllerLeaderLineDirections, controller.Id())
						}
						status.clear = true
						return
					}
				} else if num, acid, ok := strings.Cut(cmd, " "); ok {
					// L(#) (ACID) or L(##) (ACID)
					if trk := lookupTrack(acid); trk != nil {
						if err := sp.setLeaderLine(ctx, *trk, num); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
					} else {
						status.err = ErrSTARSNoFlight
					}
				} else {
					status.err = ErrSTARSIllegalPosition
				}
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return

		case "M":
			// 5-171 change one field of a flight plan
			if id, entry, ok := strings.Cut(cmd, " "); !ok {
				status.err = ErrSTARSCommandFormat
			} else {
				if fp, _ := lookupFlightPlan(id); fp == nil {
					status.err = ErrSTARSNoFlight
				} else {
					const modFpFormat = "ACID,BEACON,TCP,COORD_TIME,FIX_PAIR,TRI_SP1,PLUS_SP2,TRI_ALT_A,ALT_R"
					checkfp := func(s string, primary bool) bool {
						return checkScratchpad(ctx, s, !primary, false /* not implied */) == nil
					}
					if spec, err := parseOneFlightPlan(modFpFormat, entry, checkfp); err != nil {
						status.err = err
					} else {
						sp.modifyFlightPlan(ctx, fp.ACID, spec, false /* don't display fp */)
						status.clear = true
					}
				}
			}
			return

		case "N":
			// CRDA...
			if len(sp.ConvergingRunways) == 0 {
				// These are all illegal if there are no CRDA runway pairs
				status.err = ErrSTARSIllegalFunction
				return
			}
			if cmd == "" {
				// Toggle CRDA processing (on by default). Note that when
				// it is disabled we still hold on to CRDARunwayPairState array so
				// that we're back where we started if CRDA is reenabled.
				ps.CRDA.Disabled = !ps.CRDA.Disabled
				status.clear = true
				return
			} else if cmd == "*ALL" {
				ps.CRDA.ForceAllGhosts = !ps.CRDA.ForceAllGhosts
				status.clear = true
				return
			} else {
				// Given a string that starts with a runway identifier and then possibly has some extra text,
				// return the runway and the text as separate strings.
				getRunway := func(s string) (string, string) {
					i := 0
					for i < len(s) {
						ch := s[i]
						if ch >= '0' && ch <= '9' {
							i++
						} else if ch == 'L' || ch == 'R' || ch == 'C' {
							i++
							break
						} else {
							break
						}
					}
					return s[:i], s[i:]
				}

				// This function takes a string of the form "JFK 22LMORE"
				// or "22LMORE" and looks for the associated
				// CRDARunwayPairState and CRDARunwayState for an enabled
				// CRDA runway.  "MORE" represents arbitrary text *that may
				// contain spaces*.  If the airport is not specified, then
				// it must be possible to unambiguously determine the
				// airport given the runway. It returns:
				//
				// airport: the name of the associated airport
				// runway: the runway identifier
				// ps: CRDARunwayPairState for the runway
				// rs: CRDARunwayState for the runway
				// extra: any excess text after the runway identifier
				// err: ErrSTARSIllegalParam if there is no such enabled
				//   runway pair or if more than one matches when only a
				//   runway is specified.
				getRunwayState := func(s string) (airport string, runway string, ps *CRDARunwayPairState,
					rs *CRDARunwayState, extra string, err error) {
					if s[0] >= '0' && s[0] <= '9' {
						// It starts with a runway identifier. (We'll
						// assume CRDA isn't happening for airports
						// with names like '87N'..)
						runway, extra = getRunway(s)

						for i, pair := range sp.ConvergingRunways {
							pairState := &sp.currentPrefs().CRDA.RunwayPairState[i]
							if !pairState.Enabled {
								continue
							}
							for j, pairRunway := range pair.Runways {
								if runway != pairRunway {
									continue
								}

								if ps != nil {
									// We found more than one match...
									err = ErrSTARSIllegalParam
									return
								}
								airport = pair.Airport
								ps, rs = pairState, &pairState.RunwayState[j]
							}
						}
						if ps == nil {
							err = ErrSTARSIllegalParam
						}
					} else {
						// Expect airport and then a space.
						var ok bool
						airport, extra, ok = strings.Cut(s, " ")
						if !ok {
							err = ErrSTARSIllegalParam
							return
						}

						runway, extra = getRunway(extra)
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport != airport {
								continue
							}

							pairState := &sp.currentPrefs().CRDA.RunwayPairState[i]
							if !pairState.Enabled {
								continue
							}

							for j, pairRunway := range pair.Runways {
								if runway == pairRunway {
									ps, rs = pairState, &pairState.RunwayState[j]
									return
								}
							}
						}
						err = ErrSTARSIllegalParam
					}
					return
				}

				// Check these commands first; if we key off cmd[0]=='L' for example we end up issuing
				// an error if the user actually specified an airport starting with "L"...
				if ap, rwy, _, runwayState, extra, err := getRunwayState(cmd); err == nil {
					if extra == "E" || (extra == "" && !runwayState.Enabled) {
						// 6-23: enable ghosts for runway
						runwayState.Enabled = true
						status.output = ap + " " + rwy + " GHOSTING ENABLED"
						status.clear = true
						return
					} else if extra == "I" || (extra == "" && runwayState.Enabled) {
						// 6-23: disable ghosts for runway
						runwayState.Enabled = false
						status.output = ap + " " + rwy + " GHOSTING INHIBITED"
						// this also disables the runway's visualizations
						runwayState.DrawQualificationRegion = false
						runwayState.DrawCourseLines = false
						status.clear = true
						return
					} else if extra == " B" { // 6-31
						runwayState.DrawQualificationRegion = !runwayState.DrawQualificationRegion
						status.clear = true
						return
					} else if extra == " L" { // 6-32
						runwayState.DrawCourseLines = !runwayState.DrawCourseLines
						status.clear = true
						return
					}
				}
				if cmd[0] == 'L' {
					// 6-26: Set leader line direction: NL(airport) (runway)(1-9)
					// or: NL(runway)(1-9); runway must unambiguously define airport
					if _, _, _, runwayState, num, err := getRunwayState(cmd[1:]); err == nil {
						if len(num) == 1 {
							if dir, ok := sp.numpadToDirection(num[0]); ok {
								runwayState.LeaderLineDirection = dir
								status.clear = true
								return
							}
						}
						status.err = ErrSTARSCommandFormat
						return
					}
				} else if cmd[0] == 'P' {
					// These commands either start with an airport and a
					// space or use the controller's default airport if
					// none is specified. None of the commands otherwise
					// allow spaces, so we can use the presence of a space
					// to determine if an airport was specified.
					airport, extra, ok := strings.Cut(cmd[1:], " ")
					if !ok {
						if ctrl, ok := ctx.Client.State.Controllers[ctx.UserTCP]; ok {
							airport = ctrl.DefaultAirport[1:] // drop leading "K"
							extra = cmd[1:]
						}
					}

					if index, err := strconv.Atoi(extra); err == nil {
						// 6-22: toggle ghosts for a runway pair
						// NP(airport )(idx) / NP(idx)
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport == airport && pair.Index == index {
								// TODO: we toggle each independently; is that correct?
								rps := &ps.CRDA.RunwayPairState[i]
								rps.RunwayState[0].Enabled = !rps.RunwayState[0].Enabled
								rps.RunwayState[1].Enabled = !rps.RunwayState[1].Enabled
								status.clear = true
								return
							}
						}
						status.err = ErrSTARSCommandFormat
						return
					} else {
						// 8-11: disable/set stagger or tie mode for a runway pair
						// NP(airport )(idx)(cmd) / NP(idx)(cmd)
						n := len(extra)
						if n < 2 || (extra[n-1] != 'S' && extra[n-1] != 'T' && extra[n-1] != 'D') {
							status.err = ErrSTARSCommandFormat
							return
						}
						index, err := strconv.Atoi(extra[:n-1])
						if err != nil {
							status.err = ErrSTARSIllegalRPC
							return
						}
						for i, pair := range sp.ConvergingRunways {
							if pair.Airport != airport || pair.Index != index {
								continue
							}

							if extra[n-1] == 'D' {
								ps.CRDA.RunwayPairState[i].Enabled = false
								status.clear = true
								status.output = airport + " " + pair.getRunwaysString() + " INHIBITED"
								return
							} else {
								// Make sure neither of the runways involved is already enabled in
								// another pair.
								for j, pairState := range ps.CRDA.RunwayPairState {
									if !pairState.Enabled {
										continue
									}
									if sp.ConvergingRunways[j].Runways[0] == pair.Runways[0] ||
										sp.ConvergingRunways[j].Runways[0] == pair.Runways[1] ||
										sp.ConvergingRunways[j].Runways[1] == pair.Runways[0] ||
										sp.ConvergingRunways[j].Runways[1] == pair.Runways[1] {
										status.err = ErrSTARSIllegalRunway
										return
									}
								}

								if extra[n-1] == 'S' {
									ps.CRDA.RunwayPairState[i].Mode = CRDAModeStagger
								} else {
									ps.CRDA.RunwayPairState[i].Mode = CRDAModeTie
								}
								ps.CRDA.RunwayPairState[i].Enabled = true
								ps.CRDAStatusList.Visible = true
								status.output = airport + " " + pair.getRunwaysString() + " ENABLED"
								status.clear = true
								return
							}
						}
					}
				}
			}
			status.err = ErrSTARSIllegalParam
			return

		case "O":
			if cmd == "" {
				ps.AutomaticFDBOffset = !ps.AutomaticFDBOffset
				status.clear = true
				return
			} else if cmd == "E" {
				ps.AutomaticFDBOffset = true
				status.clear = true
				return
			} else if cmd == "I" {
				ps.AutomaticFDBOffset = true
				status.clear = true
				return
			} else if len(cmd) > 2 {
				clearHistory := false
				if cmd[0] == '*' {
					clearHistory = true
					cmd = cmd[1:]
				}

				trk := lookupTrack(cmd)
				if trk == nil {
					status.err = ErrSTARSCommandFormat
					return
				} else if trk.IsUnassociated() || trk.FlightPlan.TrackingController != ctx.UserTCP {
					status.err = ErrSTARSIllegalTrack
					return
				}

				if clearHistory {
					// 6-81 clear point out history
					var spec sim.STARSFlightPlanSpecifier
					spec.PointOutHistory.Set(nil)
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* no display */)
					status.clear = true
					return
				} else {
					// 6-79 display point out history
					sfp := trk.FlightPlan
					if len(sfp.PointOutHistory) == 0 {
						status.output = "PO NONE"
					} else {
						status.output = strings.Join(sfp.PointOutHistory, " ")
					}
					status.clear = true
					return
				}
			}

		case "P":
			// Tower/coordination lists: 4-55, 4-59, 4-64, 4-65
			f := strings.Fields(cmd)
			if len(f) != 1 && len(f) != 2 {
				status.err = ErrSTARSCommandFormat
				return
			}

			id := f[0]
			if id == "1" || id == "2" || id == "3" {
				// Tower list
				tl := &ps.TowerLists[id[0]-'1']
				if len(f) == 1 {
					// Toggle list visibility
					tl.Visible = !tl.Visible
					status.clear = true
				} else if n, err := strconv.Atoi(f[1]); err != nil || n < 1 || n > 100 {
					status.err = ErrSTARSIllegalParam
				} else {
					// Set number of visible lines
					tl.Lines = n
					// Setting lines also makes the tower list visible.
					tl.Visible = true
					status.clear = true
				}
			} else if cl, ok := ps.CoordinationLists[id]; ok {
				// Coordination list
				if len(f) == 1 {
					status.err = ErrSTARSCommandFormat
				} else {
					n, err := strconv.Atoi(f[1])
					if err != nil || n < 1 || n > 100 {
						status.err = ErrSTARSIllegalParam
					} else {
						// Set number of visible lines
						cl.Lines = n
						status.clear = true
					}
				}
			}
			return

		case "Q": // quicklook
			if len(cmd) == 0 {
				// inhibit for all
				ps.QuickLookAll = false
				ps.QuickLookAllIsPlus = false
				ps.QuickLookPositions = nil
				status.clear = true
				return
			} else if cmd == "ALL" {
				if ps.QuickLookAll && ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = false
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else if cmd == "ALL+" {
				if ps.QuickLookAll && !ps.QuickLookAllIsPlus {
					ps.QuickLookAllIsPlus = true
				} else {
					ps.QuickLookAll = !ps.QuickLookAll
					ps.QuickLookAllIsPlus = false
					ps.QuickLookPositions = nil
				}
				status.clear = true
				return
			} else {
				input, err := sp.updateQL(ctx, cmd)
				if err == nil {
					status.clear = true
				} else {
					status.err = err
					sp.previewAreaInput = input
				}
				return
			}

		case "R": // requested altitude: 6-107
			switch cmd {
			case "": // clear all individually-enabled PTLs
				for _, state := range sp.TrackState {
					state.DisplayPTL = false
				}
				status.clear = true
				return
			case "A": // toggle
				sp.DisplayRequestedAltitude = !sp.DisplayRequestedAltitude
				status.clear = true
				return
			case "AE": // enable
				sp.DisplayRequestedAltitude = true
				status.clear = true
				return
			case "AI": // inhibit
				sp.DisplayRequestedAltitude = false
				status.clear = true
				return
			}

		case "S":
			switch len(cmd) {
			case 0:
				// S -> clear atis, first line of text
				ps.ATIS = ""
				ps.GIText[0] = ""
				status.clear = true
				return

			case 1:
				if cmd[0] == '*' {
					// S* -> clear atis
					ps.ATIS = ""
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' {
					// S[1-9] -> clear corresponding line of text
					idx := cmd[0] - '1'
					ps.GIText[idx] = ""
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis) -> set atis code
					ps.ATIS = string(cmd[0])
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}

			default:
				if len(cmd) == 2 && cmd[0] >= 'A' && cmd[0] <= 'Z' && cmd[1] == '*' {
					// S(atis)* -> set atis, delete first line of text
					ps.ATIS = string(cmd[0])
					ps.GIText[0] = ""
					status.clear = true
					return
				} else if cmd[0] == '*' {
					// S*(text) -> clear atis, set first line of gi text
					ps.ATIS = ""
					ps.GIText[0] = cmd[1:]
					status.clear = true
					return
				} else if cmd[0] >= '1' && cmd[0] <= '9' && cmd[1] == ' ' {
					// S[1-9](spc)(text) -> set corresponding line of GI text
					idx := cmd[0] - '1'
					ps.GIText[idx] = cmd[2:]
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// S(atis)(text) -> set atis and first line of GI text
					ps.ATIS = string(cmd[0])
					ps.GIText[0] = cmd[1:]
					status.clear = true
					return
				} else {
					status.err = ErrSTARSIllegalParam
					return
				}

			}

		case "T":
			updateList := func(cmd string, visible *bool, lines *int) {
				if cmd == "" {
					*visible = !*visible
				} else if lines != nil {
					if n, err := strconv.Atoi(cmd); err == nil && n >= 1 && n <= 100 {
						*lines = n
						*visible = true
					} else {
						// 4-64 et al.
						status.err = ErrSTARSIllegalParam
					}
				}
				status.clear = true
			}

			if len(cmd) == 0 {
				updateList("", &ps.TABList.Visible, &ps.TABList.Lines)
				return
			} else if cmd == "Q" {
				// 7-31: can only toggle display
				ps.MCISuppressionList.Visible = !ps.MCISuppressionList.Visible
				status.clear = true
				return
			} else if cmd == "RA" {
				// Can't set number of lines, can just toggle display.
				ps.RestrictionAreaList.Visible = !ps.RestrictionAreaList.Visible
				status.clear = true
				return
			} else {
				switch cmd[0] {
				case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
					updateList(cmd, &ps.TABList.Visible, &ps.TABList.Lines)
					return
				case 'V':
					updateList(cmd[1:], &ps.VFRList.Visible, &ps.VFRList.Lines)
					return
				case 'C':
					// Note: the coast/suspend list is always visible; we
					// should probably issue an error if the user attempts
					// to toggle visibility here.
					updateList(cmd[1:], &ps.CoastList.Visible, &ps.CoastList.Lines)
					return
				case 'S':
					updateList(cmd[1:], &ps.SignOnList.Visible, nil)
					return
				case 'X':
					updateList(cmd[1:], &ps.VideoMapsList.Visible, nil)
					return
				case 'N':
					updateList(cmd[1:], &ps.CRDAStatusList.Visible, nil)
					return
				default:
					status.err = ErrSTARSIllegalFunction
					return
				}
			}

		case "V":
			switch cmd {
			case "MI":
				ps.DisableMSAW = true
				status.clear = true
				return
			case "ME":
				ps.DisableMSAW = false
				status.clear = true
				return
			}

		case "W":
			// WX history 6-11
			if cmd == STARSTriangleCharacter {
				sp.wxHistoryDraw = 2
				sp.wxNextHistoryStepTime = ctx.Now.Add(5 * time.Second)
				status.output = "IN PROGRESS"
				status.clear = true
				return
			}

		case "Y":
			isSecondary := false
			if len(cmd) > 0 && cmd[0] == '+' {
				isSecondary = true
				cmd = cmd[1:]
			}

			f := strings.Fields(cmd)
			if len(f) == 1 {
				// Y callsign -> clear scratchpad and reported altitude
				// Y+ callsign -> secondary scratchpad..
				if trk := lookupTrack(f[0]); trk == nil {
					status.err = ErrSTARSNoFlight
				} else if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					var spec sim.STARSFlightPlanSpecifier
					spec.PilotReportedAltitude.Set(0)
					if isSecondary {
						spec.SecondaryScratchpad.Set("")
					} else {
						spec.Scratchpad.Set("")
					}
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
				}
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scratchpad
				// Y callsign <space> ### -> set pilot alt
				// as above, Y+ -> secondary scratchpad

				// Either pilot alt or scratchpad entry
				if trk := lookupTrack(f[0]); trk == nil {
					status.err = ErrSTARSNoFlight
				} else if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else if spec, err := parseOneFlightPlan("SP1,ALT_P", f[1],
					func(s string, primary bool) bool {
						return checkScratchpad(ctx, s, !primary, false /* !implied */) == nil
					}); err != nil {
					status.err = err
				} else {
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display */)
					status.clear = true
				}
				return
			}

		case "Z":
			switch cmd {
			case "A": // 4-88 play test sound
				sp.testAudioEndTime = ctx.Now.Add(5 * time.Second)
				ctx.Platform.StartPlayAudioContinuous(sp.audioEffects[AudioTest])
				status.clear = true
				return

			case "EE":
				ps.AudioEffectEnabled[AudioCommandError] = true
				status.clear = true
				return

			case "EI":
				ps.AudioEffectEnabled[AudioCommandError] = false
				status.clear = true
				return
			}
			status.err = ErrSTARSCommandFormat
			return
		}

	case CommandModeFlightData:
		// Create flight plan 5-109
		const createFpFormat = "+ACID?BEACON,TCP/FIX_PAIR,COORD_TIME,TRI_SP1,PLUS_SP2,#/AC_TYPE/EQ,ALT_R,RULES"
		checkfp := func(s string, primary bool) bool { return checkScratchpad(ctx, s, !primary, false) == nil }
		if spec, err := parseFlightPlan(createFpFormat, cmd, checkfp); err == nil {
			spec.Rules.Set(av.FlightRulesIFR)
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			sp.createFlightPlan(ctx, spec)
			status.clear = true
		} else {
			status.err = err
		}
		return

	case CommandModeCollisionAlert:
		if len(cmd) > 3 && cmd[:2] == "K " {
			if trk := lookupTrack(cmd[2:]); trk != nil && trk.IsAssociated() {
				var spec sim.STARSFlightPlanSpecifier
				spec.DisableCA.Set(!trk.FlightPlan.DisableCA)
				spec.MCISuppressedCode.Set(av.Squawk(0)) // 7-18: this clears the MCI inhibit code
				sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)

			} else {
				status.err = ErrSTARSNoFlight
			}
			status.clear = true
			return
		} else if strings.HasPrefix(cmd, "M ") {
			// Suppress a beacon code for MCI
			f := strings.Fields(cmd[2:])
			if len(f) != 1 && len(f) != 2 {
				status.err = ErrSTARSCommandFormat
			} else if trk := lookupTrack(f[0]); trk == nil {
				status.err = ErrSTARSNoFlight
			} else if len(f) == 1 {
				status = sp.updateMCISuppression(ctx, *trk, "")
			} else {
				status = sp.updateMCISuppression(ctx, *trk, f[1])
			}
			return

		} else if cmd == "AI" {
			if ps.DisableCAWarnings {
				status.output = "NO CHANGE"
			}
			ps.DisableCAWarnings = true
			status.clear = true
			return
		} else if cmd == "AE" {
			if !ps.DisableCAWarnings {
				status.output = "NO CHANGE"
			}
			ps.DisableCAWarnings = false
			status.clear = true
			return
		} else if cmd == "MI" {
			if ps.DisableMCIWarnings {
				status.output = "NO CHANGE"
			}
			ps.DisableMCIWarnings = true
			status.clear = true
			return
		} else if cmd == "ME" {
			if !ps.DisableMCIWarnings {
				status.output = "NO CHANGE"
			}
			ps.DisableMCIWarnings = false
			status.clear = true
			return
		}

	case CommandModeMin:
		if cmd == "" {
			// Clear min sep
			sp.MinSepAircraft[0] = ""
			sp.MinSepAircraft[1] = ""
			status.clear = true
		} else {
			status.err = ErrSTARSCommandFormat
		}
		return

	case CommandModeSavePrefAs:
		if cmd != "" {
			if len(cmd) > 7 {
				status.err = ErrSTARSCommandFormat
			} else if slices.ContainsFunc(sp.prefSet.Saved[:],
				func(p *Preferences) bool { return p != nil && p.Name == cmd }) {
				// Can't repeat pref set names
				status.err = ErrSTARSIllegalPrefset
			} else if v, err := strconv.Atoi(cmd); err == nil && v >= 1 && v <= numSavedPreferenceSets {
				// Can't give it a numeric name that conflicts with pref set #s
				status.err = ErrSTARSIllegalPrefset
			} else {
				// Find the first empty slot
				idx := slices.Index(sp.prefSet.Saved[:], nil)
				if idx == -1 {
					// This shouldn't happen since SAVE AS should be disabled if there are
					// no free slots...
					idx = len(sp.prefSet.Saved) - 1
				}
				p := sp.prefSet.Current.Duplicate()
				p.Name = cmd
				sp.prefSet.Selected = &idx
				sp.prefSet.Saved[idx] = p
				status.clear = true
			}
		}
		return

	case CommandModeMaps:
		if cmd == "A" {
			// remove all maps
			clear(ps.VideoMapVisible)
			status.clear = true
			return
		} else if n := len(cmd); n > 0 {
			op := "T"            // toggle by default
			if cmd[n-1] == 'E' { // enable
				op = "E"
				cmd = cmd[:n-1]
			} else if cmd[n-1] == 'I' { // inhibit
				op = "I"
				cmd = cmd[:n-1]
			}

			if idx, err := strconv.Atoi(cmd); err != nil {
				status.err = ErrSTARSCommandFormat
			} else if idx <= 0 {
				status.err = ErrSTARSIllegalMap
			} else if slices.ContainsFunc(sp.allVideoMaps, func(v sim.VideoMap) bool { return v.Id == idx }) {
				// Valid map index.
				_, vis := ps.VideoMapVisible[idx]
				if (vis && op == "T") || op == "I" {
					delete(ps.VideoMapVisible, idx)
				} else if (!vis && op == "T") || op == "E" {
					ps.VideoMapVisible[idx] = nil
				}
				status.clear = true
			} else {
				status.err = ErrSTARSIllegalMap
			}
			return
		}

	case CommandModeSite:
		radarSites := ctx.FacilityAdaptation.RadarSites

		if cmd == STARSTriangleCharacter {
			sp.setRadarModeMulti()
			status.clear = true
		} else if cmd == "+" {
			sp.setRadarModeFused()
			status.clear = true
		} else if idx, err := strconv.Atoi(cmd); err == nil {
			idx-- // 1-based
			if idx < 0 || idx > len(radarSites) {
				status.err = ErrSTARSRangeLimit
			} else {
				ps.RadarSiteSelected = util.SortedMapKeys(radarSites)[idx]
				status.clear = true
			}
		} else if id, _, ok := util.MapLookupFunc(radarSites,
			func(id string, site *av.RadarSite) bool { return site.Char == cmd }); ok {
			ps.RadarSiteSelected = id
			status.clear = true
		} else {
			status.err = ErrSTARSIllegalParam
		}
		return

	case CommandModeWX:
		// 4-42
		if cmd == "C" {
			// Clear all
			ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
			clear(ps.DisplayWeatherLevel[:])
			status.clear = true
			return
		} else if cmd == "A" {
			// Toggle between previously displayed levels
			ps.DisplayWeatherLevel, ps.LastDisplayWeatherLevel = ps.LastDisplayWeatherLevel, ps.DisplayWeatherLevel
			status.clear = true
			return
		} else if len(cmd) >= 1 && cmd[0] >= '1' && cmd[0] <= '6' {
			lvl := cmd[0] - '1'
			if len(cmd) == 1 {
				// Toggle level
				ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
				ps.DisplayWeatherLevel[lvl] = !ps.DisplayWeatherLevel[lvl]
				status.clear = true
				return
			} else if len(cmd) == 2 && cmd[1] == 'E' {
				// Enable level
				ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
				ps.DisplayWeatherLevel[lvl] = true
				status.clear = true
				return
			} else if len(cmd) == 2 && cmd[0] >= '1' && cmd[0] <= '6' && cmd[1] == 'I' {
				// Inhibit level
				ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
				ps.DisplayWeatherLevel[lvl] = false
				status.clear = true
				return
			}
		} else if len(cmd) >= 0 && cmd[0] == '0' || (cmd[0] >= '7' && cmd[0] <= '9') {
			status.err = ErrSTARSRangeLimit
			return
		}
		// Otherwise fall through to ErrSTARSCommandFormat

	case CommandModePref:
		// 4-10: apply preference set
		if cmd != "" {
			idx, err := strconv.Atoi(cmd)
			if err == nil && (idx <= 0 || idx > numSavedPreferenceSets) {
				// Got a number but it's out of range
				status.err = ErrSTARSCommandFormat
				return
			} else if err == nil {
				idx-- // 0-based indexing
				if sp.prefSet.Saved[idx] == nil {
					// No saved prefs at the given index
					status.err = ErrSTARSCommandFormat
					return
				}
			} else {
				idx = slices.IndexFunc(sp.prefSet.Saved[:], func(p *Preferences) bool { return p != nil && p.Name == cmd })
				if idx == -1 {
					// Named pref set doesn't exist
					status.err = ErrSTARSIllegalPrefset
					return
				}
			}
			// Success
			sp.prefSet.Selected = &idx
			sp.prefSet.SetCurrent(*sp.prefSet.Saved[idx], ctx.Platform, sp)

			status.clear = true
			return
		}

	case CommandModeRestrictionArea:
		cmd, n, ok := tryConsumeInt(cmd)
		if ok {
			// All digits or at least starts with numbers
			if cmd == "E" || cmd == "I" || cmd == "" {
				ra := getRestrictionAreaByIndex(ctx, n)
				if ra == nil {
					status.err = ErrSTARSIllegalGeoId
					return
				}

				settings, ok := ps.RestrictionAreaSettings[n]
				if !ok {
					settings = &RestrictionAreaSettings{}
					ps.RestrictionAreaSettings[n] = settings
				}

				if cmd == "E" {
					// 6-50: display hidden
					settings.Visible = true
				} else if cmd == "I" {
					// 6-50: hide displayed
					settings.Visible = false
				} else if cmd == "" {
					// 6-50 stop blinking text
					blinking := settings.ForceBlinkingText || (ra.BlinkingText && !settings.StopBlinkingText)
					if blinking {
						settings.ForceBlinkingText = false
						settings.StopBlinkingText = true
					} else {
						// If no blinking, toggle display
						settings.Visible = !settings.Visible
					}
				}
				status.clear = true
			} else if cmd == "T" || cmd == "T"+STARSTriangleCharacter || cmd == "T "+STARSTriangleCharacter {
				// 6-49: hide/show restriction area text
				ra := getRestrictionAreaByIndex(ctx, n)
				if ra == nil {
					status.err = ErrSTARSIllegalGeoId
					return
				}

				ps.RestrictionAreaSettings[n].HideText = !ps.RestrictionAreaSettings[n].HideText
				ps.RestrictionAreaSettings[n].ForceBlinkingText = false
				if strings.HasSuffix(cmd, STARSTriangleCharacter) {
					if !ps.RestrictionAreaSettings[n].HideText {
						ps.RestrictionAreaSettings[n].ForceBlinkingText = true
					} else {
						status.err = ErrSTARSCommandFormat
						return
					}
				}
				sp.updateRestrictionArea(ctx, n, *ra)
				status.clear = true
			} else if cmd[0] == 'T' {
				// 6-48: change text
				ra := getUserRestrictionAreaByIndex(ctx, n) // only user-defined
				if ra == nil {
					status.err = ErrSTARSIllegalGeoId
					return
				}

				if parsed, err := parseRAText(strings.Fields(cmd[1:]), false, false); err != nil {
					status.err = err
				} else if len(parsed.extra) > 0 {
					status.err = ErrSTARSCommandFormat
				} else {
					ra.Text = parsed.text
					ra.BlinkingText = parsed.blink
					sp.updateRestrictionArea(ctx, n, *ra)
					status.clear = true
				}
				return
			} else if cmd[0] == '*' {
				// 6-45: move restriction area
				ra := getUserRestrictionAreaByIndex(ctx, n) // only user-defined
				if ra == nil {
					status.err = ErrSTARSIllegalGeoId
					return
				}

				cmd = cmd[1:]
				blink := false
				if strings.HasPrefix(cmd, STARSTriangleCharacter) {
					cmd = strings.TrimPrefix(cmd, STARSTriangleCharacter)
					blink = true
				}
				if pos, ok := sp.parseRALocation(ctx, cmd); !ok {
					status.err = ErrSTARSIllegalGeoLoc
				} else {
					ra.MoveTo(pos)
					if blink {
						ra.BlinkingText = true
					}
					sp.updateRestrictionArea(ctx, n, *ra)
					status.clear = true
				}
			} else if cmd == "DEL" {
				// 6-47: delete
				ra := getUserRestrictionAreaByIndex(ctx, n) // only user-defined
				if ra == nil {
					status.err = ErrSTARSIllegalGeoId
					return
				}

				delete(ps.RestrictionAreaSettings, n)
				ctx.Client.DeleteRestrictionArea(n, nil, func(err error) { sp.displayError(err, ctx, "") })
				status.clear = true
			} else {
				status.err = ErrSTARSCommandFormat
			}
		} else if ra := sp.wipRestrictionArea; ra != nil {
			// Add to or complete a WIP RA definition
			if ra.CircleRadius > 0 {
				// Circle
				if cmd == "" {
					ra.TextPosition = ra.CircleCenter
					sp.createRestrictionArea(ctx, *ra)
					sp.wipRestrictionArea = nil
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return
			} else if len(ra.Vertices) > 0 {
				// Polygon
				if p, ok := sp.parseRALocation(ctx, cmd); ok {
					// Another vertex location
					ra.Vertices[0] = append(ra.Vertices[0], p)
					sp.previewAreaInput = ""
					if ctx.Mouse != nil {
						sp.wipRestrictionAreaMousePos = ctx.Mouse.Pos
						sp.wipRestrictionAreaMouseMoved = false
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return
			}
		} else {
			// Doesn't start with digits
			switch cmd[0] {
			case 'G':
				// 6-37: create text
				if parsed, err := parseRAText(strings.Fields(cmd[1:]), false, true); err != nil {
					status.err = err
				} else if pos, ok := sp.parseRALocation(ctx, strings.Join(parsed.extra, " ")); !ok {
					status.err = ErrSTARSIllegalGeoLoc
				} else {
					ra := av.RestrictionArea{
						Text:         parsed.text,
						TextPosition: pos,
						BlinkingText: parsed.blink,
					}
					sp.createRestrictionArea(ctx, ra)
					status.clear = true
				}
				return

			case 'C':
				// 6-39: circle + text
				cmd, rad, ok := tryConsumeFloat(cmd[1:])
				if !ok {
					status.err = ErrSTARSCommandFormat
				} else if rad < 1 || rad > 125 {
					status.err = ErrSTARSIllegalRange
				} else if parsed, err := parseRAText(strings.Fields(cmd), true, true); err != nil {
					status.err = err
				} else if pos, ok := sp.parseRALocation(ctx, strings.Join(parsed.extra, " ")); !ok {
					status.err = ErrSTARSIllegalGeoLoc
				} else {
					// Mostly done but need to allow the text position to be specified.
					sp.setWIPRestrictionArea(ctx, &av.RestrictionArea{
						Text:         parsed.text,
						CircleCenter: pos,
						CircleRadius: rad,
						BlinkingText: parsed.blink,
						Shaded:       parsed.shaded,
						Color:        parsed.color,
					})
					sp.previewAreaInput = ""
				}
				return

			case 'A', 'P':
				// 6-41: closed polygon + text / 6-43: open polygon + text
				if p, ok := sp.parseRALocation(ctx, cmd[1:]); !ok {
					status.err = ErrSTARSIllegalGeoLoc
				} else {
					sp.setWIPRestrictionArea(ctx, &av.RestrictionArea{
						Closed:   cmd[0] == 'P',
						Vertices: [][]math.Point2LL{{p}},
					})
					sp.previewAreaInput = ""
				}
				return

			default:
				status.err = ErrSTARSCommandFormat
			}
		}
		return

	case CommandModeReleaseDeparture:
		// 5-45
		rel := ctx.Client.State.GetSTARSReleaseDepartures()

		// Filter out the ones that have been released and then deleted
		// from the coordination list by the controller.
		rel = slices.DeleteFunc(rel,
			func(dep sim.ReleaseDeparture) bool { return sp.TrackState[dep.ADSBCallsign].ReleaseDeleted })

		if cmd == "" {
			// If there is only one unacknowledged, then ack/release it.
			unack := slices.DeleteFunc(rel, func(dep sim.ReleaseDeparture) bool { return dep.Released })
			switch len(unack) {
			case 0:
				status.err = ErrSTARSIllegalFlight
			case 1:
				ctx.Client.ReleaseDeparture(unack[0].ADSBCallsign, nil,
					func(err error) { sp.displayError(err, ctx, "") })
				status.clear = true
			default:
				status.err = ErrSTARSMultipleFlights
			}
			return
		} else if cmd == "T" {
			// 5-49: toggle display of empty
			ps.DisplayEmptyCoordinationLists = !ps.DisplayEmptyCoordinationLists
			status.clear = true
			return
		} else if cmd == "TI" {
			// 5-49: disable (inhibit) display of empty
			ps.DisplayEmptyCoordinationLists = false
			status.clear = true
			return
		} else if cmd == "TE" {
			// 5-49: enable display of empty
			ps.DisplayEmptyCoordinationLists = true
			status.clear = true
			return
		} else if f := strings.Fields(cmd); len(f) == 2 {
			// 5-48 enable/disable auto ack (release)
			if len(f[0]) >= 2 && f[0][0] == 'P' {
				if cl := ps.CoordinationLists[f[0][1:]]; cl != nil {
					if f[1] == "A*" { // enable
						cl.AutoRelease = true
						status.clear = true
					} else if f[1] == "M*" { // inhibit
						cl.AutoRelease = false
						status.clear = true
					} else {
						status.err = ErrSTARSCommandFormat
					}
				} else {
					status.err = ErrSTARSIllegalFunction
				}
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return
		} else {
			// Release or delete an aircraft in the list
			rel := func() *sim.ReleaseDeparture {
				n, nerr := strconv.Atoi(cmd)
				sq, sqerr := av.ParseSquawk(cmd)
				for i, dep := range rel {
					if dep.ADSBCallsign == av.ADSBCallsign(cmd) {
						return &rel[i]
					}
					if sqerr == nil && sq == dep.Squawk {
						return &rel[i]
					}
					if nerr == nil && n >= 0 && n == dep.ListIndex {
						return &rel[i]
					}
				}
				return nil
			}()
			if rel == nil {
				if _, err := strconv.Atoi(cmd); err == nil && len(cmd) < 4 /* else assume it's a beacon code */ {
					// Given a line number that doesn't exist.
					status.err = ErrSTARSIllegalLine
				} else if trk := lookupTrack(cmd); trk != nil {
					// There is such a flight but it's not in our release list.
					if trk.HoldForRelease {
						// It's in another controller's list
						status.err = ErrSTARSIllegalFunction
					} else {
						status.err = ErrSTARSIllegalFlight
					}
				} else {
					// No such flight anywhere.
					status.err = ErrSTARSNoFlight
				}
			} else if !rel.Released {
				rel.Released = true // hack for instant update pending the next server update
				ctx.Client.ReleaseDeparture(rel.ADSBCallsign, nil,
					func(err error) { sp.displayError(err, ctx, "") })
				status.clear = true
			} else {
				sp.TrackState[rel.ADSBCallsign].ReleaseDeleted = true
				status.clear = true
			}
			return
		}

	case CommandModeTargetGen:
		// Special cases for non-control commands.
		if cmd == "" {
			return
		}
		if cmd == "P" {
			ctx.Client.ToggleSimPause()
			status.clear = true
			return
		}

		// Otherwise looks like an actual control instruction .
		suffix, cmds, ok := strings.Cut(cmd, " ")
		if !ok {
			suffix = string(sp.targetGenLastCallsign)
			cmds = cmd
		}

		instructor := ctx.Client.State.AmInstructor()
		matching := sp.tracksFromACIDSuffix(ctx, suffix, instructor)
		if len(matching) > 1 {
			status.err = ErrSTARSAmbiguousACID
			return
		}

		var trk *sim.Track
		if len(matching) == 1 {
			trk = matching[0]
		} else if len(matching) == 0 && sp.targetGenLastCallsign != "" {
			// If a valid callsign wasn't given, try the last callsign used.
			trk, _ = ctx.GetTrackByCallsign(sp.targetGenLastCallsign)
			// But now we're going to run all of the given input as commands.
			cmds = cmd
		}

		if trk != nil {
			sp.runAircraftCommands(ctx, trk.ADSBCallsign, cmds)
			status.clear = true

		} else {
			status.err = ErrSTARSIllegalACID
		}

		return
	}

	status.err = ErrSTARSCommandFormat
	return
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

func (sp *STARSPane) runAircraftCommands(ctx *panes.Context, callsign av.ADSBCallsign, cmds string) {
	sp.targetGenLastCallsign = callsign

	ctx.Client.RunAircraftCommands(callsign, cmds,
		func(errStr string, remaining string) {
			if errStr != "" {
				sp.commandMode = CommandModeTargetGen
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

func (sp *STARSPane) setWIPRestrictionArea(ctx *panes.Context, ra *av.RestrictionArea) {
	sp.wipRestrictionArea = ra
	if ctx.Mouse != nil {
		sp.wipRestrictionAreaMousePos = ctx.Mouse.Pos
	} else {
		sp.wipRestrictionAreaMousePos = [2]float32{-1, -1}
	}
	sp.wipRestrictionAreaMouseMoved = false
}

func tryConsumeInt(cmd string) (string, int, bool) {
	idx := strings.IndexFunc(cmd, func(r rune) bool { return r < '0' || r > '9' })
	if idx == 0 {
		return cmd, 0, false
	}
	var rest string
	if idx != -1 {
		rest = cmd[idx:]
		cmd = cmd[:idx]
	}
	n, err := strconv.Atoi(cmd)
	if err != nil {
		return cmd, 0, false
	}
	return rest, n, true
}

// Note: only positive floats.
func tryConsumeFloat(cmd string) (string, float32, bool) {
	sawpt := false
	num, rest := cmd, ""

	// Scan until the first non-numeric character, allowing a single
	// decimal point along the way.
	for i, ch := range cmd {
		if ch == '.' {
			if sawpt {
				num = cmd[:i]
				rest = cmd[i:]
				break
			}
			sawpt = true
		} else if ch < '0' || ch > '9' {
			num = cmd[:i]
			rest = cmd[i:]
			break
		}
	}

	if f, err := strconv.ParseFloat(num, 32); err != nil {
		return cmd, 0, false
	} else {
		return rest, float32(f), true
	}
}

func getUserRestrictionAreaByIndex(ctx *panes.Context, idx int) *av.RestrictionArea {
	if idx < 1 || idx-1 >= len(ctx.Client.State.UserRestrictionAreas) {
		return nil
	} else if ra := &ctx.Client.State.UserRestrictionAreas[idx-1]; ra.Deleted {
		return nil
	} else {
		return ra
	}
}

func getRestrictionAreaByIndex(ctx *panes.Context, idx int) *av.RestrictionArea {
	if ra := getUserRestrictionAreaByIndex(ctx, idx); ra != nil {
		return ra
	} else if idx < 101 || idx-101 >= len(ctx.FacilityAdaptation.RestrictionAreas) {
		return nil
	} else if ra := &ctx.FacilityAdaptation.RestrictionAreas[idx-101]; ra.Deleted {
		return nil
	} else {
		return ra
	}
}

func (sp *STARSPane) parseRABasicLocation(s string) (math.Point2LL, bool) {
	if latstr, longstr, ok := strings.Cut(s, "/"); ok {
		// Latitude
		if len(latstr) != 7 || (latstr[6] != 'N' && latstr[6] != 'S') {
			return math.Point2LL{}, false
		}

		convert := func(str string, neg bool) (float32, bool) {
			lat, err := strconv.Atoi(str)
			if err != nil {
				return 0, false
			}
			deg := (lat / 10000)
			min := (lat / 100) % 100
			sec := lat % 100
			v := float32(deg) + float32(min)/60 + float32(sec)/3600
			if neg {
				v = -v
			}
			return v, true
		}

		var p math.Point2LL
		p[1], ok = convert(latstr[:6], latstr[6] == 'S')
		if !ok {
			return p, false
		}

		if len(longstr) != 8 || (longstr[7] != 'E' && longstr[7] != 'W') {
			return p, false
		}
		p[0], ok = convert(longstr[:7], longstr[7] == 'W')
		return p, ok
	} else if pt, ok := sp.significantPoints[s]; ok {
		return pt.Location, true
	} else {
		return math.Point2LL{}, false
	}
}

func (sp *STARSPane) parseRALocation(ctx *panes.Context, s string) (math.Point2LL, bool) {
	f := strings.Fields(s)
	if len(f) != 1 && len(f) != 3 {
		return math.Point2LL{}, false
	}

	p, ok := sp.parseRABasicLocation(f[0])
	if !ok {
		return math.Point2LL{}, false
	}

	if len(f) == 3 {
		bearing, err := strconv.Atoi(f[1])
		if err != nil {
			return p, true
		}
		if bearing < 1 || bearing > 360 {
			return p, false
		}

		dist, err := strconv.ParseFloat(f[2], 32)
		if err != nil || dist > 125 {
			return p, false
		}

		p = math.Offset2LL(p, float32(bearing), float32(dist), ctx.NmPerLongitude,
			ctx.MagneticVariation)
	}
	return p, true
}

func tidyRAText(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, ".", " "))
	if len(s) > 32 {
		// In theory we're suppose to give a CAPACITY error in this case..
		s = s[:32]
	}
	return s
}

type parsedRAText struct {
	text   [2]string
	blink  bool
	shaded bool
	extra  []string
	color  int
}

func parseRAText(f []string, closedShape bool, expectPosition bool) (parsed parsedRAText, err error) {
	doTriPlus := func(s string) error {
		var getColor bool
		for _, ch := range s {
			if getColor {
				if ch < '1' || ch > '8' {
					return ErrSTARSIllegalColor
				}
				parsed.color = int(ch - '0') // 1-based indexing
				getColor = false
			} else if string(ch) == STARSTriangleCharacter {
				parsed.blink = true
			} else if ch == '+' && closedShape {
				parsed.shaded = true
			} else if ch == '*' && closedShape {
				getColor = true
			} else {
				return ErrSTARSCommandFormat
			}
		}
		if getColor {
			return ErrSTARSCommandFormat
		}
		return nil
	}

	// We always start with the first text field
	if len(f) == 0 {
		return parsed, ErrSTARSCommandFormat
	}

	// It's illegal to give the additional options as the text for the
	// first line so return an error if it parses cleanly.
	if doTriPlus(f[0]) == nil {
		return parsed, ErrSTARSCommandFormat
	}

	parsed.text[0] = tidyRAText(f[0])
	f = f[1:]

	// We may be done, may get a ∆ or +, or may have a second line of text.
	// If the caller needs a position entered, though, don't slurp that up
	// as text and return nothing.
	if len(f) == 0 || (expectPosition && len(f) == 1) {
		parsed.extra = f
		return
	}
	if doTriPlus(f[0]) == nil {
		parsed.extra = f[1:]
		return
	} else {
		parsed.text[1] = tidyRAText(f[0])
		f = f[1:]
	}

	// We've been given two lines of text. Last chance for ∆ or +,
	// but if we don't have that just return what's left.
	if len(f) > 0 && doTriPlus(f[0]) == nil {
		f = f[1:]
	}
	parsed.extra = f
	return
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
					ctx.Client.ReleaseDeparture(dep.ADSBCallsign, nil,
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

func (sp *STARSPane) getTowerOrCoordinationList(id string) (*BasicSTARSList, bool) {
	ps := sp.currentPrefs()
	if cl, ok := ps.CoordinationLists[id]; ok {
		return &cl.BasicSTARSList, false
	}
	if id == "1" || id == "2" || id == "3" {
		return &ps.TowerLists[id[0]-'1'], true
	}
	return nil, false
}

func (sp *STARSPane) updateQL(ctx *panes.Context, input string) (previewInput string, err error) {
	positions, input, err := sp.parseQuickLookPositions(ctx, input)
	if err != nil {
		previewInput = input
		return
	}

	if len(positions) > 0 {
		ps := sp.currentPrefs()
		ps.QuickLookAll = false

		for _, pos := range positions {
			// Toggle
			match := func(q QuickLookPosition) bool { return q.Id == pos.Id && q.Plus == pos.Plus }
			matchId := func(q QuickLookPosition) bool { return q.Id == pos.Id }
			if slices.ContainsFunc(ps.QuickLookPositions, match) {
				nomatch := func(q QuickLookPosition) bool { return !match(q) }
				ps.QuickLookPositions = util.FilterSliceInPlace(ps.QuickLookPositions, nomatch)
			} else if idx := slices.IndexFunc(ps.QuickLookPositions, matchId); idx != -1 {
				// Toggle plus
				ps.QuickLookPositions[idx].Plus = !ps.QuickLookPositions[idx].Plus
			} else {
				ps.QuickLookPositions = append(ps.QuickLookPositions, pos)
			}
		}
		// Quick look plus is listed first; otherwise sort alphabetically
		slices.SortFunc(ps.QuickLookPositions, func(a, b QuickLookPosition) int {
			if a.Plus && !b.Plus {
				return -1
			} else if b.Plus && !a.Plus {
				return 1
			} else {
				return strings.Compare(a.Id, b.Id)
			}
		})
	}

	if err != nil {
		previewInput = input
	}
	return
}

func (sp *STARSPane) setGlobalLeaderLine(ctx *panes.Context, callsign av.ADSBCallsign, dir *math.CardinalOrdinalDirection) {
	state := sp.TrackState[callsign]
	state.UseGlobalLeaderLine = dir != nil

	var spec sim.STARSFlightPlanSpecifier
	spec.GlobalLeaderLineDirection.Set(dir)
	trk, _ := ctx.GetTrackByCallsign(callsign)
	sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* no display */)
}

func (sp *STARSPane) associateFlightPlan(ctx *panes.Context, callsign av.ADSBCallsign, spec sim.STARSFlightPlanSpecifier) error {
	if !spec.TrackingController.IsSet {
		spec.TrackingController.Set(ctx.UserTCP)
	}

	ctx.Client.AssociateFlightPlan(callsign, spec,
		func(any) {
			if trk, ok := ctx.GetTrackByCallsign(callsign); ok && trk.IsAssociated() {
				sp.previewAreaOutput = sp.formatFlightPlan(ctx, trk.FlightPlan, trk)
			}
		},
		func(err error) { sp.displayError(err, ctx, "") })
	return nil
}

func (sp *STARSPane) activateFlightPlan(ctx *panes.Context, trackCallsign av.ADSBCallsign, fpACID sim.ACID,
	spec *sim.STARSFlightPlanSpecifier) {
	ctx.Client.ActivateFlightPlan(trackCallsign, fpACID, spec, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) deleteFlightPlan(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.DeleteFlightPlan(acid, nil, func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) acceptHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcceptHandoff(acid, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) handoffTrack(ctx *panes.Context, acid sim.ACID, controller string) error {
	control := sp.lookupControllerForId(ctx, controller, acid)
	if control == nil {
		return ErrSTARSIllegalPosition
	}

	ctx.Client.HandoffTrack(acid, control.Id(), nil,
		func(err error) { sp.displayError(err, ctx, "") })

	return nil
}
func (sp *STARSPane) setLeaderLine(ctx *panes.Context, trk sim.Track, cmd string) error {
	state := sp.TrackState[trk.ADSBCallsign]
	if len(cmd) == 1 { // Local 6-81
		if dir, ok := sp.numpadToDirection(cmd[0]); ok {
			state.LeaderLineDirection = dir
			if dir != nil {
				state.UseGlobalLeaderLine = false
			}
			return nil
		}
	} else if len(cmd) == 2 && cmd[0] == cmd[1] { // Global leader lines 6-101
		if trk.IsUnassociated() || trk.FlightPlan.TrackingController != ctx.UserTCP {
			return ErrSTARSIllegalTrack
		} else if dir, ok := sp.numpadToDirection(cmd[0]); ok {
			sp.setGlobalLeaderLine(ctx, trk.ADSBCallsign, dir)
			return nil
		}
	}
	return ErrSTARSCommandFormat
}

func (sp *STARSPane) forceQL(ctx *panes.Context, acid sim.ACID, tcp string) {
	ctx.Client.ForceQL(acid, tcp, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) redirectHandoff(ctx *panes.Context, acid sim.ACID, tcp string) {
	ctx.Client.RedirectHandoff(acid, tcp, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) acceptRedirectedHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcceptRedirectedHandoff(acid, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) removeForceQL(ctx *panes.Context, acid sim.ACID) bool {
	if _, ok := sp.ForceQLACIDs[acid]; ok {
		delete(sp.ForceQLACIDs, acid)
		return true
	}
	return false
}

func (sp *STARSPane) pointOut(ctx *panes.Context, acid sim.ACID, tcp string) {
	ctx.Client.PointOut(acid, tcp, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) acknowledgePointOut(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.AcknowledgePointOut(acid, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) recallPointOut(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.RecallPointOut(acid, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) cancelHandoff(ctx *panes.Context, acid sim.ACID) {
	ctx.Client.CancelHandoff(acid, nil,
		func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) updateMCISuppression(ctx *panes.Context, trk sim.Track, code string) (status CommandStatus) {
	ps := sp.currentPrefs()
	if ps.DisableMCIWarnings {
		status.err = ErrSTARSIllegalFunction
	} else if trk.IsUnassociated() || trk.FlightPlan.TrackingController != ctx.UserTCP {
		status.err = ErrSTARSIllegalTrack
	} else {
		sfp := trk.FlightPlan
		if code == "" {
			var spec sim.STARSFlightPlanSpecifier

			if sfp.MCISuppressedCode != av.Squawk(0) {
				// clear suppression
				spec.MCISuppressedCode.Set(av.Squawk(0))
			} else {
				// TODO: 0477 is the default but it's adaptable
				spec.MCISuppressedCode.Set(av.Squawk(0o0477))
				spec.DisableCA.Set(false) // 7-30; can't have both
			}

			sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
			status.clear = true
		} else if sq, err := av.ParseSquawk(code); err != nil {
			status.err = ErrSTARSIllegalValue // TODO: what should this be?
		} else {
			var spec sim.STARSFlightPlanSpecifier

			if sfp.MCISuppressedCode == sq { // entered same code; clear suppression
				spec.MCISuppressedCode.Set(av.Squawk(0))
			} else {
				spec.MCISuppressedCode.Set(sq)
				spec.DisableCA.Set(false) // 7-30; can't have both
			}

			sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
			status.clear = true
		}
	}
	return
}

func (sp *STARSPane) executeSTARSClickedCommand(ctx *panes.Context, cmd string, mousePosition [2]float32,
	ghosts []*av.GhostTrack, transforms ScopeTransformations, tracks []sim.Track) (status CommandStatus) {
	// See if an aircraft was clicked
	trk, trkDistance := sp.tryGetClosestTrack(ctx, mousePosition, transforms, tracks)
	ghost, ghostDistance := sp.tryGetClosestGhost(ghosts, mousePosition, transforms)

	ps := sp.currentPrefs()

	// The only thing that can happen with a ghost is to switch between a full/partial
	// datablock. Note that if we found both an aircraft and a ghost and a command was entered,
	// we don't issue an error for a bad ghost command but
	if ghost != nil && ghostDistance < trkDistance {
		if sp.commandMode == CommandModeNone && cmd == "" {
			state := sp.TrackState[ghost.ADSBCallsign]
			state.Ghost.PartialDatablock = !state.Ghost.PartialDatablock
			status.clear = true
			return
		} else if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "N" {
			if cmd == "" {
				// Suppress ghost
				state := sp.TrackState[ghost.ADSBCallsign]
				state.Ghost.State = GhostStateSuppressed
				status.clear = true
				return
			} else if cmd == "*" {
				// Display parent aircraft flight plan
				if trk, ok := ctx.GetTrackByCallsign(ghost.ADSBCallsign); ok && trk.IsAssociated() {
					status.output = sp.formatFlightPlan(ctx, trk.FlightPlan, trk)
				}
				status.clear = true
				return
			}
		}
	}

	trySetLeaderLine := func(spec string) bool {
		err := sp.setLeaderLine(ctx, *trk, cmd)
		return err == nil
	}

	if trk != nil {
		state := sp.TrackState[trk.ADSBCallsign]

		switch sp.commandMode {
		case CommandModeNone:
			if cmd == "" {
				var acid sim.ACID
				if trk.IsAssociated() {
					acid = trk.FlightPlan.ACID
				}

				if time.Until(state.RDIndicatorEnd) > 0 {
					if state.OutboundHandoffAccepted {
						state.OutboundHandoffAccepted = false
						state.OutboundHandoffFlashEnd = ctx.Now
					}
					state.RDIndicatorEnd = time.Time{}
					status.clear = true
					return
				} else if trk.IsAssociated() && (trk.FlightPlan.RedirectedHandoff.RedirectedTo == ctx.UserTCP ||
					trk.FlightPlan.RedirectedHandoff.GetLastRedirector() == ctx.UserTCP) {
					sp.acceptRedirectedHandoff(ctx, acid)
					status.clear = true
					return
				} else if trk.IsAssociated() && trk.FlightPlan.HandoffTrackController == ctx.UserTCP {
					status.clear = true
					sp.acceptHandoff(ctx, acid)
					return
				} else if sp.removeForceQL(ctx, acid) {
					status.clear = true
					return
				} else if idx := slices.IndexFunc(sp.CAAircraft, func(ca CAAircraft) bool {
					return (ca.ADSBCallsigns[0] == trk.ADSBCallsign || ca.ADSBCallsigns[1] == trk.ADSBCallsign) &&
						!ca.Acknowledged
				}); idx != -1 {
					// Acknowledged a CA
					status.clear = true
					sp.CAAircraft[idx].Acknowledged = true
					return
				} else if idx := slices.IndexFunc(sp.MCIAircraft, func(ca CAAircraft) bool {
					return ca.ADSBCallsigns[0] == trk.ADSBCallsign && !ca.Acknowledged
				}); idx != -1 {
					// Acknowledged a MCI
					status.clear = true
					sp.MCIAircraft[idx].Acknowledged = true
					return
				} else if state.MSAW && !state.MSAWAcknowledged {
					// Acknowledged a MSAW
					state.MSAWAcknowledged = true
					status.clear = true
					return
				} else if state.SPCAlert && !state.SPCAcknowledged {
					// Acknowledged SPC alert part 1
					state.SPCAcknowledged = true
					status.clear = true
					return
				} else if trk.IsAssociated() && trk.FlightPlan.SPCOverride != "" && !state.SPCAcknowledged {
					// Acknowledged SPC alert part 2
					state.SPCAcknowledged = true
					status.clear = true
					return
				} else if trk.IsAssociated() && trk.FlightPlan.HandoffTrackController != "" &&
					trk.FlightPlan.HandoffTrackController != ctx.UserTCP &&
					trk.FlightPlan.TrackingController == ctx.UserTCP {
					// cancel offered handoff offered
					status.clear = true
					sp.cancelHandoff(ctx, acid)
					return
				} else if tcps, ok := sp.PointOuts[acid]; ok && tcps.To == ctx.UserTCP {
					// ack point out
					sp.acknowledgePointOut(ctx, acid)
					status.clear = true
					return
				} else if ok && tcps.From == ctx.UserTCP {
					// recall point out
					sp.recallPointOut(ctx, acid)
					status.clear = true
					return
				} else if state.PointOutAcknowledged {
					state.PointOutAcknowledged = false
					status.clear = true
					return
				} else if state.ForceQL {
					state.ForceQL = false
					status.clear = true
				} else if state.DatablockAlert {
					state.DatablockAlert = false
					status.clear = true
				} else if _, ok := sp.RejectedPointOuts[acid]; ok {
					// ack rejected point out
					delete(sp.RejectedPointOuts, acid)
					status.clear = true
					return
				} else if state.IFFlashing {
					state.IFFlashing = false
					return
				} else if state.OutboundHandoffAccepted {
					// ack an accepted handoff
					status.clear = true
					state.OutboundHandoffAccepted = false
					state.OutboundHandoffFlashEnd = ctx.Now
					return
				} else if trk.IsAssociated() && trk.Squawk != trk.FlightPlan.AssignedSquawk {
					// 5-147: change ABC to RBC for track in mismatch
					spec := sim.STARSFlightPlanSpecifier{}
					spec.ACID.Set(trk.FlightPlan.ACID)
					spec.AssignedSquawk.Set(trk.Squawk)
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
					return
				} else if _, ok := sp.DuplicateBeacons[trk.Squawk]; ok && state.DBAcknowledged != trk.Squawk { // after ABC -> RBC check
					state.DBAcknowledged = trk.Squawk
					status.clear = true
					return
				}

				if db := sp.datablockType(ctx, *trk); db == LimitedDatablock {
					s := ctx.FacilityAdaptation.FullLDBSeconds
					if s == 0 {
						s = 5
					}
					state.FullLDBEndTime = ctx.Now.Add(time.Duration(s) * time.Second)
				} else if trk.IsAssociated() && trk.FlightPlan.TrackingController != ctx.UserTCP {
					// Toggle FDB display for tracks owned by other controllers
					state.DisplayFDB = !state.DisplayFDB
				}

				if trk.IsAssociated() && trk.FlightPlan.TrackingController == ctx.UserTCP {
					// 5-202
					status.output = sp.formatFlightPlan(ctx, trk.FlightPlan, trk)
				}
			} else if cmd == "." {
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					var spec sim.STARSFlightPlanSpecifier
					spec.Scratchpad.Set("")
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
				}
				return
			} else if cmd == "+" {
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					var spec sim.STARSFlightPlanSpecifier
					spec.SecondaryScratchpad.Set("")
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
				}
				return
			} else if cmd == "*" {
				from := trk.Location
				nmPerLongitude := ctx.NmPerLongitude
				magneticVariation := ctx.MagneticVariation
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
					p := transforms.LatLongFromWindowP(pw)
					hdg := math.Heading2LL(from, p, nmPerLongitude, magneticVariation)
					dist := math.NMDistance2LL(from, p)

					status.output = fmt.Sprintf("%03d/%.2f", int(hdg+.5), dist)
					status.clear = true
					return
				}
				return
			} else if trySetLeaderLine(cmd) {
				status.clear = true
				return
			} else if cmd == "?" {
				ads, err := ctx.Client.GetAircraftDisplayState(trk.ADSBCallsign)
				if err != nil {
					ctx.Lg.Error("print aircraft", slog.String("callsign", string(trk.ADSBCallsign)),
						slog.Any("err", err))
				} else {
					fmt.Println(ads.Spew + "\n\n\n" + spew.Sdump(state))
				}
				status.clear = true
				return
			} else if cmd == "*F" {
				// 6-148 range/bearing to significant point
				p := trk.Location
				sp.wipSignificantPoint = &p
				sp.scopeClickHandler = toSignificantPointClickHandler(ctx, sp)
				sp.previewAreaInput += " " // sort of a hack: if the fix is entered via keyboard, it appears on the next line
				return
			} else if cmd == "*J" {
				// remove j-ring for aircraft
				state.JRingRadius = 0
				status.clear = true
				return
			} else if cmd == "*P" {
				// remove cone for aircraft
				state.ConeLength = 0
				status.clear = true
				return
			} else if cmd == "*T" {
				// range bearing line
				sp.wipRBL = &STARSRangeBearingLine{}
				sp.wipRBL.P[0].ADSBCallsign = trk.ADSBCallsign
				sp.scopeClickHandler = rblSecondClickHandler(ctx, sp, tracks)
				// Do not clear the input area to allow entering a fix for the second location
				return
			} else if trk.IsAssociated() && ctx.Client.StringIsSPC(cmd) {
				state.SPCAcknowledged = false
				var spec sim.STARSFlightPlanSpecifier
				if cmd == trk.FlightPlan.SPCOverride { // matches, so turn it off
					spec.SPCOverride.Set("")
				} else { // set it to something new
					spec.SPCOverride.Set(cmd)
				}
				sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* no display */)
				status.clear = true
				return
			} else if cmd == "UN" && trk.IsAssociated() {
				ctx.Client.RejectPointOut(trk.FlightPlan.ACID, nil,
					func(err error) { sp.displayError(err, ctx, "") })
				status.clear = true
				return
			} else if spec, err := parseOneFlightPlan("#/AC_TYPE/EQ", cmd, nil); err == nil {
				// 5-158 Add or modify aircraft type. Note that this has to
				// be done before 5-150 Add or modify scratchpad or
				// altitude, and aircraft type so that a 4-char entry is
				// interpreted as setting the aircraft type.
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display */)
					status.clear = true
				}
				return
			} else if lc := len(cmd); lc >= 2 && cmd[0:2] == "**" { // Force QL. You need to specify a TCP unless otherwise specified in STARS config
				// STARS Manual 6-70 (On slew). Cannot go interfacility
				// TODO: Or can be used to accept a pointout as a handoff.

				if cmd == "**" { // Non specified TCP
					if ctx.FacilityAdaptation.ForceQLToSelf && trk.IsAssociated() &&
						trk.FlightPlan.TrackingController == ctx.UserTCP {
						state.ForceQL = true
						status.clear = true
					} else {
						status.err = ErrSTARSIllegalPosition
					}
				} else if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					tcps := strings.Split(cmd[2:], " ")
					if len(tcps) > 0 && tcps[0] == "ALL" {
						// Force QL for all TCP
						// Find user fac
						if ctrl, ok := ctx.Client.State.Controllers[ctx.UserTCP]; ok && !ctrl.ERAMFacility {
							sp.forceQL(ctx, trk.FlightPlan.ACID, ctx.UserTCP)
						}
						status.clear = true
					}
					for _, tcp := range tcps {
						control := sp.lookupControllerForId(ctx, tcp, trk.FlightPlan.ACID)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
						} else {
							sp.forceQL(ctx, trk.FlightPlan.ACID, control.Id())
							status.clear = true
						}
					}
				}
				return
			} else if cmd == "*D+" {
				// TODO: this and the following two should give ILL FNCT if
				// there's no j-ring/[A]TPA cone being displayed for the
				// track (6-173).

				// toggle TPA size display
				if state.DisplayTPASize == nil {
					b := ps.DisplayTPASize // new variable; don't alias ps.DisplayTPASize!
					state.DisplayTPASize = &b
				}
				*state.DisplayTPASize = !*state.DisplayTPASize
				status.clear = true
				return
			} else if cmd == "*D+E" {
				// enable TPA size display
				b := true
				state.DisplayTPASize = &b
				status.clear = true
				return
			} else if cmd == "*D+I" {
				// inhibit TPA size display
				b := false
				state.DisplayTPASize = &b
				status.clear = true
				return
			} else if cmd == "*AE" {
				// Enable ATPA warning/alert cones for the track
				// TODO: for this and *AI and the two *B commands below, we
				// should issue an error if not IFR, not displaying FDB, or
				// not in ATPA approach volume (6-176).
				b := true
				state.DisplayATPAWarnAlert = &b
				status.clear = true
				return
			} else if cmd == "*AI" {
				// Inhibit ATPA warning/alert cones for the track
				b := false
				state.DisplayATPAWarnAlert = &b
				status.clear = true
				return
			} else if cmd == "*BE" {
				// Enable ATPA monitor cones for the track
				b := true
				state.DisplayATPAMonitor = &b
				status.clear = true
				return
			} else if cmd == "*BI" {
				// Inhibit ATPA monitor cones for the track
				b := false
				state.DisplayATPAMonitor = &b
				status.clear = true
				return
			} else if cmd == "Y" && trk.IsUnassociated() {
				// 5-145: create quick ACID flight plan
				spec := sim.STARSFlightPlanSpecifier{CreateQuick: true}
				spec.Rules.Set(av.FlightRulesIFR)
				spec.TypeOfFlight.Set(av.FlightTypeOverflight)
				if err := sp.associateFlightPlan(ctx, trk.ADSBCallsign, spec); err != nil {
					status.err = err
				} else {
					state.DatablockAlert = true // Display DB in yellow until it's slewed.
					status.clear = true
				}
				return
			} else if cmd == ".ROUTE" {
				sp.drawRouteAircraft = trk.ADSBCallsign
				status.clear = true
				return
			} else if len(cmd) > 2 && cmd[:2] == "*J" {
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
						state.ConeLength = 0 // can't have both
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.JRingRadius = float32(r)
						state.ConeLength = 0 // can't have both
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if len(cmd) > 2 && cmd[:2] == "*P" {
				// Not allowed for unsupported datablocks
				if trk.IsUnsupportedDB() {
					status.err = ErrSTARSIllegalTrack
				} else if r, err := strconv.Atoi(cmd[2:]); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.ConeLength = float32(r)
						state.JRingRadius = 0 // can't have both
					}
					status.clear = true
				} else if r, err := strconv.ParseFloat(cmd[2:], 32); err == nil {
					if r < 1 || r > 30 {
						status.err = ErrSTARSIllegalValue
					} else {
						state.ConeLength = float32(r)
						state.JRingRadius = 0 // can't have both
					}
					status.clear = true
				} else {
					status.err = ErrSTARSIllegalParam
				}
				return
			} else if lc := len(cmd); lc >= 2 && cmd[lc-1] == '*' { // Some sort of pointout
				// First check for errors. (Manual 6-64, 6-73)

				// TODO: if it's to a different facility and it's an arrival, ILL TRK

				// Check if being handed off, pointed out or suspended (TODO suspended)
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else if _, ok := sp.PointOuts[trk.FlightPlan.ACID]; ok {
					status.err = ErrSTARSIllegalTrack
				} else if trk.FlightPlan.HandoffTrackController != ctx.UserTCP {
					status.err = ErrSTARSIllegalTrack
				} else {
					control := sp.lookupControllerForId(ctx, strings.TrimSuffix(cmd, "*"), trk.FlightPlan.ACID)
					if control == nil {
						status.err = ErrSTARSIllegalPosition
					} else {
						status.clear = true
						sp.pointOut(ctx, trk.FlightPlan.ACID, control.Id())
					}
				}
				return
			} else if trk.IsAssociated() {
				// See if cmd works as a sector id; if so, make it a handoff.
				sfp := trk.FlightPlan
				if control := sp.lookupControllerForId(ctx, cmd, trk.FlightPlan.ACID); control != nil {
					if sfp.HandoffTrackController == ctx.UserTCP ||
						sfp.RedirectedHandoff.RedirectedTo == ctx.UserTCP { // Redirect
						sp.redirectHandoff(ctx, sfp.ACID, control.Id())
						status.clear = true
					} else if err := sp.handoffTrack(ctx, sfp.ACID, cmd); err == nil {
						status.clear = true
					} else {
						status.err = err
					}
					return
				}

				first, second, _ := strings.Cut(cmd, " ")

				// 5-150: Add or modify scratchpad or altitude, and aircraft type
				checkfp := func(s string, primary bool) bool {
					return checkScratchpad(ctx, s, !primary, true /* implied */) == nil
				}
				spec, err := parseOneFlightPlan("SP1,ALT_P,PLUS_SP2,PLUS_ALT_A,PLUS_PLUS_ALT_R", first, checkfp)
				if err != nil {
					status.err = err
					return
				}

				if second != "" {
					if spec2, err := parseOneFlightPlan("AC_TYPE/EQ", second, nil); err != nil {
						status.err = err
						return
					} else {
						spec.AircraftType = spec2.AircraftType
						spec.EquipmentSuffix = spec2.EquipmentSuffix
					}
				}

				sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display */)

				status.clear = true
				return
			} else if trk.IsUnassociated() && len(cmd) > 0 {
				// 5-70 activate fp and associate
				if fp := ctx.Client.State.FindMatchingFlightPlan(cmd); fp != nil {
					sp.activateFlightPlan(ctx, trk.ADSBCallsign, fp.ACID, nil)
					status.clear = true
					return
				} else if cmd[0] >= 'A' && cmd[0] <= 'Z' {
					// // 5-99: create FP and associate
					checkfp := func(s string, primary bool) bool { return checkScratchpad(ctx, s, !primary, false) == nil }
					if spec, err := parseFlightPlan("+ACID?BEACON,TRI_SP1,PLUS_SP2,ALT_A,#/AC_TYPE/EQ", cmd, checkfp); err != nil {
						status.err = err
					} else {
						spec.Rules.Set(av.FlightRulesIFR)
						spec.TypeOfFlight.Set(av.FlightTypeArrival)
						if err := sp.associateFlightPlan(ctx, trk.ADSBCallsign, spec); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
					}
				}
				return
			}

		case CommandModeInitiateControl:
			first, rest, _ := strings.Cut(cmd, " ")
			checkfp := func(s string, primary bool) bool {
				return checkScratchpad(ctx, s, !primary, false /* !implied */) == nil
			}
			if trk.IsAssociated() {
				status.err = ErrSTARSIllegalTrack
			} else if fp := ctx.Client.State.FindMatchingFlightPlan(first); fp != nil {
				// 5-72 activate existing FP
				if spec, err := parseFlightPlan("?SP1,TRI_SP1,PLUS_SP2,ALT_A", rest, checkfp); err != nil {
					status.err = err
				} else {
					sp.activateFlightPlan(ctx, trk.ADSBCallsign, fp.ACID, &spec)
					status.clear = true
				}
				return
			} else {
				// 5-124: create active FP with discrete beacon code
				if spec, err := parseFlightPlan("+ACID?BEACON,TRI_SP1,PLUS_SP2,ALT_A,#/AC_TYPE/EQ", cmd, checkfp); err != nil {
					status.err = err
				} else {
					spec.Rules.Set(av.FlightRulesIFR)
					spec.TypeOfFlight.Set(av.FlightTypeArrival)
					if !spec.AssignedSquawk.IsSet { // take the current code from the aircraft
						spec.AssignedSquawk.Set(trk.Squawk)
					}
					if err := sp.associateFlightPlan(ctx, trk.ADSBCallsign, spec); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
			}
			return

		case CommandModeTerminateControl:
			// 5-83
			if trk.IsAssociated() {
				sp.deleteFlightPlan(ctx, trk.FlightPlan.ACID)
				status.clear = true
			} else {
				status.err = ErrSTARSIllegalTrack
			}
			return

		case CommandModeHandOff:
			if !trk.IsAssociated() {
				status.err = ErrSTARSIllegalTrack
			} else {
				acid := trk.FlightPlan.ACID
				if cmd == "" {
					if po, ok := sp.PointOuts[acid]; ok && po.To == ctx.UserTCP {
						sp.acceptHandoff(ctx, acid)
					} else {
						// Try to cancel it; if it's not ours, we'll get an error from this
						sp.cancelHandoff(ctx, acid)
					}
					status.clear = true
				} else {
					if err := sp.handoffTrack(ctx, acid, cmd); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
			}
			return

		case CommandModeMultiFunc:
			switch sp.multiFuncPrefix {
			case "B":
				if cmd == "" {
					if trk.IsAssociated() {
						// Associated track; display ACID, RBC (received beacon code), ABC (assigned beacon code) in preview area.
						status.output = string(trk.ADSBCallsign) + " " + trk.Squawk.String() + " " +
							trk.FlightPlan.AssignedSquawk.String()
					} else {
						// Unassociated track.
						state.DisplayLDBBeaconCode = !state.DisplayLDBBeaconCode
					}
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "D":
				if cmd == "" {
					if trk.IsUnassociated() {
						status.err = ErrSTARSNoFlight
					} else {
						status.output = sp.formatFlightPlan(ctx, trk.FlightPlan, trk)
						if ctx.FacilityAdaptation.FlightPlan.ModifyAfterDisplay {
							// This goes into [multifunc]M mode while the flight plan
							// is displayed, so status.clear is *not* set; we add a
							// space so that the modification entry can be entered
							// directly.
							sp.multiFuncPrefix = "M"
							sp.previewAreaInput = string(trk.FlightPlan.ACID) + " "
						} else {
							status.clear = true
						}
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "H":
				// 5-178: modify rnav symbol, a/c type, eq suffix, or flight rules
				if spec, err := parseOneFlightPlan("RNAV,#/AC_TYPE/EQ,RULES", cmd, nil); err != nil {
					status.err = err
				} else if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
				}
				return

			case "L": // Leader line
				if err := sp.setLeaderLine(ctx, *trk, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return

			case "M":
				if cmd == "" && trk.IsAssociated() {
					var spec sim.STARSFlightPlanSpecifier
					spec.InhibitModeCAltitudeDisplay.Set(!trk.FlightPlan.InhibitModeCAltitudeDisplay)
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* no display */)
					status.clear = true
				} else {
					// 5-171 Modify flight plan
					const modFpFormat = "ACID,BEACON,TCP,FIX_PAIR,COORD_TIME,TRI_SP1,PLUS_SP1,TRI_ALT_A,ALT_R"
					checkfp := func(s string, primary bool) bool {
						return checkScratchpad(ctx, s, !primary, false /* not implied */) == nil
					}
					if spec, err := parseOneFlightPlan(modFpFormat, cmd, checkfp); err != nil {
						status.err = err
					} else if trk.IsUnassociated() {
						status.err = ErrSTARSIllegalTrack
					} else {
						sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
						status.clear = true
					}
				}
				return

			case "N":
				// CRDA
				if cmd == "" {
					clickedGhost := ghost != nil && ghostDistance < trkDistance
					if clickedGhost {
						state.Ghost.State = GhostStateSuppressed
					} else if slices.ContainsFunc(ghosts, func(g *av.GhostTrack) bool { return g.ADSBCallsign == trk.ADSBCallsign }) {
						state.Ghost.State = GhostStateRegular
					} else {
						status.err = ErrSTARSIllegalTrack
					}
				} else if cmd == "*" {
					clickedGhost := ghost != nil && ghostDistance < trkDistance
					if clickedGhost {
						// 6-27: display track information in preview area (as an arrival)
						if trk.IsAssociated() {
							status.output = sp.formatFlightPlan(ctx, trk.FlightPlan, trk)
						}
						status.clear = true
					} else {
						// 6-29: force/unforce ghost qualification
						if !slices.ContainsFunc(ghosts, func(g *av.GhostTrack) bool { return g.ADSBCallsign == trk.ADSBCallsign }) {
							status.err = ErrSTARSIllegalTrack
						} else {
							// Is it inside an enabled approach region?
							for i, pairState := range ps.CRDA.RunwayPairState {
								if !pairState.Enabled {
									continue
								}
								for j, rwyState := range pairState.RunwayState {
									if !rwyState.Enabled {
										continue
									}
									region := sp.ConvergingRunways[i].ApproachRegions[j]
									if lat, _ := region.Inside(trk.Location, trk.Altitude,
										ctx.NmPerLongitude, ctx.MagneticVariation); lat {
										// All good. Whew
										if state.Ghost.State == GhostStateForced {
											state.Ghost.State = GhostStateRegular
										} else {
											state.Ghost.State = GhostStateForced
										}
										status.clear = true
										return
									}
								}
							}
							status.err = ErrSTARSIllegalTrack
						}
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "O": // 6-79 Pointout history
				if cmd == "" {
					if trk.IsUnassociated() || trk.FlightPlan.TrackingController != ctx.UserTCP {
						status.err = ErrSTARSIllegalTrack
						return
					}

					sfp := trk.FlightPlan
					if len(sfp.PointOutHistory) == 0 {
						status.output = "PO NONE"
					} else {
						status.output = strings.Join(sfp.PointOutHistory, " ")
					}
					status.clear = true
				} else if cmd == "*" {
					// 6-81 clear point out history
					if trk.IsUnassociated() || trk.FlightPlan.TrackingController != ctx.UserTCP {
						status.err = ErrSTARSIllegalTrack
					} else {
						var spec sim.STARSFlightPlanSpecifier
						spec.PointOutHistory.Set(nil)
						sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* no display */)
						status.clear = true
					}
					return
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Q":
				if cmd == "" {
					if trk.IsUnassociated() || (trk.FlightPlan.TrackingController != ctx.UserTCP &&
						trk.FlightPlan.ControllingController != ctx.UserTCP) {
						status.err = ErrSTARSIllegalTrack
					} else {
						status.clear = true
						state.InhibitMSAW = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "R":
				switch cmd {
				case "":
					if ps.PTLAll || (ps.PTLOwn && trk.IsAssociated() && trk.FlightPlan.TrackingController == ctx.UserTCP) {
						status.err = ErrSTARSIllegalTrack // 6-13
					} else {
						state.DisplayPTL = !state.DisplayPTL
						status.clear = true
					}
					return
				case "A": // toggle requested altitude: 6-108
					if sp.datablockType(ctx, *trk) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						if state.DisplayRequestedAltitude == nil {
							b := sp.DisplayRequestedAltitude // inherit from system-wide
							state.DisplayRequestedAltitude = &b
						}
						*state.DisplayRequestedAltitude = !*state.DisplayRequestedAltitude
						status.clear = true
					}
					return
				case "AE": // enable requested altitude: 6-108
					if sp.datablockType(ctx, *trk) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						b := true
						state.DisplayRequestedAltitude = &b
						status.clear = true
					}
					return
				case "AI": // inhibit requested altitude: 6-108
					if sp.datablockType(ctx, *trk) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						b := false
						state.DisplayRequestedAltitude = &b
						status.clear = true
					}
					return
				}

			case "V":
				if cmd == "" {
					if trk.IsUnassociated() {
						status.err = ErrSTARSIllegalTrack
					} else if trk.FlightPlan.TrackingController != ctx.UserTCP &&
						trk.FlightPlan.ControllingController != ctx.UserTCP {
						status.err = ErrSTARSIllegalTrack
					} else {
						var spec sim.STARSFlightPlanSpecifier
						spec.DisableMSAW.Set(!trk.FlightPlan.DisableMSAW) // toggle
						sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Y":
				if cmd == "" {
					// Clear pilot reported altitude and scratchpad
					if trk.IsUnassociated() {
						status.err = ErrSTARSIllegalTrack
					} else {
						var spec sim.STARSFlightPlanSpecifier
						spec.PilotReportedAltitude.Set(0)
						spec.Scratchpad.Set("")
						sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
						status.clear = true
					}
					return
				} else if spec, err := parseOneFlightPlan("SP1,PLUS_SP2,ALT_P", cmd,
					func(s string, primary bool) bool {
						return checkScratchpad(ctx, s, !primary, false /* !implied */) == nil
					}); err != nil {
					status.err = err
				} else if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
				}
				return
			}

		case CommandModeFlightData:
			// 6-107 toggle display of aircraft type in FDB
			if dt := sp.datablockType(ctx, *trk); dt != FullDatablock {
				status.err = ErrSTARSIllegalTrack
			} else if trk.FlightPlan.AircraftType == "" {
				status.err = ErrSTARSIllegalFunction
			} else {
				fp := trk.FlightPlan
				if fp.TrackingController == ctx.UserTCP {
					// Owned track, so we'll modify the flight plan so this applies for all controllers
					spec := sim.STARSFlightPlanSpecifier{}
					if !fp.InhibitACTypeDisplay && ctx.Now.Before(fp.ForceACTypeDisplayEndTime) {
						// It's currently visible; extend the time
						// TODO: make the times adaptable
						spec.ForceACTypeDisplayEndTime.Set(ctx.Now.Add(5 * time.Second))
					} else {
						// Toggle display
						spec.InhibitACTypeDisplay.Set(!fp.InhibitACTypeDisplay)
						if !spec.InhibitACTypeDisplay.Get() {
							// It was made visible; keep it visible for a bit
							spec.ForceACTypeDisplayEndTime.Set(ctx.Now.Add(5 * time.Second))
						}
					}
					sp.modifyFlightPlan(ctx, fp.ACID, spec, false /* don't display fp */)
				} else {
					// Someone else owns it so it will just apply locally.
					if state.InhibitACTypeDisplay == nil {
						t := true
						state.InhibitACTypeDisplay = &t
					} else if !*state.InhibitACTypeDisplay && ctx.Now.Before(state.ForceACTypeDisplayEndTime) {
						// Currently visible; extend the time
						state.ForceACTypeDisplayEndTime = ctx.Now.Add(5 * time.Second)
					} else {
						*state.InhibitACTypeDisplay = !*state.InhibitACTypeDisplay
						if !*state.InhibitACTypeDisplay {
							state.ForceACTypeDisplayEndTime = ctx.Now.Add(5 * time.Second) // TODO: this is adaptable
						}
					}
				}
				status.clear = true
			}
			return

		case CommandModeCollisionAlert:
			if cmd == "K" {
				if trk.IsUnassociated() {
					status.err = ErrSTARSIllegalTrack
				} else {
					sfp := trk.FlightPlan

					var spec sim.STARSFlightPlanSpecifier
					spec.DisableCA.Set(!sfp.DisableCA)
					spec.MCISuppressedCode.Set(av.Squawk(0)) // 7-18: this clears the MCI inhibit code

					sp.modifyFlightPlan(ctx, trk.FlightPlan.ACID, spec, false /* don't display fp */)
					status.clear = true
					// TODO: check should we set sp.commandMode = CommandMode
					// (applies here and also to others similar...)
				}
				return
			} else if len(cmd) > 0 && cmd[0] == 'M' { // 7-29
				status = sp.updateMCISuppression(ctx, *trk, cmd[1:])
				return
			}

		case CommandModeMin:
			if cmd == "" {
				sp.MinSepAircraft[0] = trk.ADSBCallsign
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
					if trk, _ := sp.tryGetClosestTrack(ctx, pw, transforms, tracks); trk != nil {
						sp.MinSepAircraft[1] = trk.ADSBCallsign
						status.clear = true
					} else {
						status.err = ErrSTARSNoFlight
					}
					return
				}
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return

		case CommandModeTargetGen:
			if len(cmd) > 0 {
				sp.runAircraftCommands(ctx, trk.ADSBCallsign, cmd)
				status.clear = true
				return
			}
		}
	}

	// No aircraft selected
	switch sp.commandMode {
	case CommandModeNone:
		if cmd == "*F" {
			// 6-148 range/bearing to significant point
			p := transforms.LatLongFromWindowP(mousePosition)
			sp.wipSignificantPoint = &p
			sp.scopeClickHandler = toSignificantPointClickHandler(ctx, sp)
			sp.previewAreaInput += " " // sort of a hack: if the fix is entered via keyboard, it appears on the next line
			return
		} else if cmd == "*T" {
			sp.wipRBL = &STARSRangeBearingLine{}
			sp.wipRBL.P[0].Loc = transforms.LatLongFromWindowP(mousePosition)
			sp.scopeClickHandler = rblSecondClickHandler(ctx, sp, tracks)
			return
		} else if sp.capture.enabled {
			if cmd == "CR" {
				sp.capture.specifyingRegion = true
				sp.capture.region[0] = mousePosition
				status.clear = true
				return
			} else if sp.capture.specifyingRegion {
				sp.capture.region[1] = mousePosition
				sp.capture.specifyingRegion = false
				sp.capture.haveRegion = true
				status.clear = true
				return
			}
		} else if spec, err := parseFlightPlan("+ACID?BEACON,TRI_SP1,PLUS_SP2,ALT_A,#/AC_TYPE/EQ", cmd,
			func(s string, primary bool) bool {
				return checkScratchpad(ctx, s, !primary, false /* !implied */) == nil
			}); err == nil {
			// 5-99 create Unsupported datablock
			spec.Rules.Set(av.FlightRulesIFR)
			spec.TypeOfFlight.Set(av.FlightTypeArrival)
			spec.Location.Set(transforms.LatLongFromWindowP(mousePosition))
			sp.createFlightPlan(ctx, spec)
			status.clear = true
			return
		} else if cmd != "" {
			status.err = ErrSTARSCommandFormat
		}

	case CommandModeInitiateControl:
		// 5-72 create unsupported datablock from existing flight plan
		first, rest, _ := strings.Cut(cmd, " ")
		checkfp := func(s string, primary bool) bool {
			return checkScratchpad(ctx, s, !primary, false /* !implied */) == nil
		}
		if fp := ctx.Client.State.FindMatchingFlightPlan(first); fp == nil {
			status.err = ErrSTARSNoFlight
		} else if spec, err := parseFlightPlan("?SP1,TRI_SP1,PLUS_SP2,ALT_A", rest, checkfp); err != nil {
			status.err = err
		} else {
			spec.Location.Set(transforms.LatLongFromWindowP(mousePosition))
			sp.modifyFlightPlan(ctx, fp.ACID, spec, false /* no display */)
			status.clear = true
		}
		return

	case CommandModeMultiFunc:
		cmd = sp.multiFuncPrefix + cmd
		if cmd == "D*" {
			pll := transforms.LatLongFromWindowP(mousePosition)
			format := func(v float32) string {
				v = math.Abs(v)
				d := int(v)
				v = 60 * (v - float32(d))
				return fmt.Sprintf("%d %.2f", d, v)
			}
			status.output = fmt.Sprintf("%s / %s", format(pll.Latitude()), format(pll.Longitude()))
			status.clear = true
			return
		} else if cmd == "INC" { // enable and define auto-home position
			ps.AutoCursorHome = true
			ps.CursorHome = mousePosition
			status.clear = true
			status.output = "HOME"
			return
		} else if cmd == "P" {
			ps.PreviewAreaPosition = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "S" {
			ps.SSAList.Position = transforms.NormalizedFromWindowP(mousePosition)
			status.clear = true
			return
		} else if cmd == "T" {
			ps.TABList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.TABList.Visible = true
			status.clear = true
			return
		} else if cmd == "TV" {
			ps.VFRList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.VFRList.Visible = true
			status.clear = true
			return
		} else if cmd == "TM" {
			ps.AlertList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.AlertList.Visible = true
			status.clear = true
			return
		} else if cmd == "TC" {
			ps.CoastList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.CoastList.Visible = true
			status.clear = true
			return
		} else if cmd == "TQ" {
			ps.MCISuppressionList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.MCISuppressionList.Visible = true
			status.clear = true
			return
		} else if cmd == "TRA" {
			ps.RestrictionAreaList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.RestrictionAreaList.Visible = true
			status.clear = true
			return
		} else if cmd == "TS" {
			ps.SignOnList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.SignOnList.Visible = true
			status.clear = true
			return
		} else if cmd == "TX" {
			ps.VideoMapsList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.VideoMapsList.Visible = true
			status.clear = true
			return
		} else if cmd == "TN" {
			ps.CRDAStatusList.Position = transforms.NormalizedFromWindowP(mousePosition)
			ps.CRDAStatusList.Visible = true
			status.clear = true
			return
		} else if len(cmd) >= 2 && cmd[0] == 'P' {
			list, _ := sp.getTowerOrCoordinationList(cmd[1:])
			if list == nil {
				status.err = ErrSTARSIllegalFunction
				return
			}

			if list, ok := ps.CoordinationLists[cmd[1:]]; ok {
				list.Position = transforms.NormalizedFromWindowP(mousePosition)
				status.clear = true
				return
			}
			if idx, err := strconv.Atoi(cmd[1:]); err == nil && idx > 0 && idx <= 3 {
				ps.TowerLists[idx-1].Position = transforms.NormalizedFromWindowP(mousePosition)
				ps.TowerLists[idx-1].Visible = true
				status.clear = true
				return
			}
		}

	case CommandModeRestrictionArea:
		cmd, n, ok := tryConsumeInt(cmd)
		if ok {
			// 6-45: move restriction area
			// It starts with digits
			ra := getUserRestrictionAreaByIndex(ctx, n)
			if ra == nil {
				// Either it doesn't exist or it's not user-defined.
				status.err = ErrSTARSIllegalGeoId
				return
			}

			if len(cmd) == 0 || cmd[0] != '*' {
				status.err = ErrSTARSCommandFormat
				return
			}
			if cmd[1:] == STARSTriangleCharacter {
				ra.BlinkingText = true
			}

			ra.MoveTo(transforms.LatLongFromWindowP(mousePosition))

			sp.updateRestrictionArea(ctx, n, *ra)
			status.clear = true
			return
		} else if ra := sp.wipRestrictionArea; ra != nil {
			p := transforms.LatLongFromWindowP(mousePosition)
			if ra.CircleRadius > 0 {
				// Circle
				ra.TextPosition = p
				sp.createRestrictionArea(ctx, *ra)
				sp.wipRestrictionArea = nil
				status.clear = true
				return
			} else if len(ra.Vertices) > 0 {
				// Polygon
				if cmd != "" {
					// Input is the text to display, click position is
					// where to put the text.
					if parsed, err := parseRAText(strings.Fields(cmd), true, false); err != nil {
						status.err = err
					} else if len(parsed.extra) > 0 {
						status.err = ErrSTARSCommandFormat
					} else {
						ra.Text = parsed.text
						ra.BlinkingText = parsed.blink
						ra.Shaded = parsed.shaded
						ra.Color = parsed.color
						ra.TextPosition = p

						sp.createRestrictionArea(ctx, *ra)
						sp.wipRestrictionArea = nil
						status.clear = true
					}
				} else {
					ra.Vertices[0] = append(ra.Vertices[0], p)
					sp.wipRestrictionAreaMousePos = mousePosition
					sp.wipRestrictionAreaMouseMoved = false
				}
				return
			}
		} else if cmd == "A" || cmd == "P" {
			// Start a polygon
			p := transforms.LatLongFromWindowP(mousePosition)
			sp.setWIPRestrictionArea(ctx, &av.RestrictionArea{
				Closed:   cmd[0] == 'P',
				Vertices: [][]math.Point2LL{{p}},
			})
			sp.previewAreaInput = ""
			return
		} else if len(cmd) > 2 && cmd[0] == 'C' {
			// 6-39: create circle + text
			cmd, rad, ok := tryConsumeFloat(cmd[1:])
			if !ok {
				status.err = ErrSTARSCommandFormat
			} else if rad < 1 || rad > 125 {
				status.err = ErrSTARSIllegalRange
			} else if parsed, err := parseRAText(strings.Fields(cmd), true, false); err != nil {
				status.err = err
			} else if len(parsed.extra) != 0 {
				status.err = ErrSTARSCommandFormat
			} else {
				// Still need the text position, one way or another.
				sp.setWIPRestrictionArea(ctx, &av.RestrictionArea{
					Text:         parsed.text,
					CircleRadius: rad,
					CircleCenter: transforms.LatLongFromWindowP(mousePosition),
					BlinkingText: parsed.blink,
					Shaded:       parsed.shaded,
					Color:        parsed.color,
				})
				sp.previewAreaInput = ""
			}
			return
		} else if len(cmd) > 2 && cmd[0] == 'G' {
			// 6-37: create text
			if parsed, err := parseRAText(strings.Fields(cmd[1:]), false, false); err != nil {
				status.err = err
			} else if len(parsed.extra) != 0 {
				status.err = ErrSTARSCommandFormat
			} else {
				ra := av.RestrictionArea{
					Text:         parsed.text,
					TextPosition: transforms.LatLongFromWindowP(mousePosition),
					BlinkingText: parsed.blink,
				}
				sp.createRestrictionArea(ctx, ra)
				status.clear = true
			}
			return
		}

	case CommandModeDrawRoute:
		mouseLatLong := transforms.LatLongFromWindowP(mousePosition)
		sp.drawRoutePoints = append(sp.drawRoutePoints, mouseLatLong)
		var cb []string
		for _, p := range sp.drawRoutePoints {
			cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
		}
		ctx.Platform.GetClipboard().SetClipboard(strings.Join(cb, " "))
		status.output = fmt.Sprintf("%d POINTS", len(sp.drawRoutePoints))
		return
	}

	if cmd != "" {
		status.err = ErrSTARSCommandFormat
	}
	return
}

func (sp *STARSPane) createRestrictionArea(ctx *panes.Context, ra av.RestrictionArea) {
	// Go ahead and make it visible, assuming which index will be assigned
	// to reduce update latency.
	ps := sp.currentPrefs()
	idx := len(ctx.Client.State.UserRestrictionAreas)
	ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}

	ctx.Client.CreateRestrictionArea(ra, func(idx int) {
		// Just in case (e.g. a race with another controller also adding
		// one), make sure we have the one we made visible.
		ps := sp.currentPrefs()
		ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
	}, func(err error) { sp.displayError(err, ctx, "") })
}

func (sp *STARSPane) updateRestrictionArea(ctx *panes.Context, idx int, ra av.RestrictionArea) {
	ctx.Client.UpdateRestrictionArea(idx, ra, func(any) {
		ps := sp.currentPrefs()
		if settings, ok := ps.RestrictionAreaSettings[idx]; ok {
			settings.Visible = true
		} else {
			ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
		}
	}, func(err error) { sp.displayError(err, ctx, "") })
}

// Returns the cardinal-ordinal direction associated with the numbpad keys,
// interpreting 5 as the center; (nil, true) is returned for '5' and
// (nil, false) is returned for an invalid key.
func (sp *STARSPane) numpadToDirection(key byte) (*math.CardinalOrdinalDirection, bool) {
	if key < '1' || key > '9' {
		return nil, false
	}
	if key == '5' {
		return nil, true
	}
	if sp.FlipNumericKeypad {
		dirs := [9]math.CardinalOrdinalDirection{
			math.NorthWest, math.North, math.NorthEast,
			math.West, math.CardinalOrdinalDirection(-1), math.East,
			math.SouthWest, math.South, math.SouthEast,
		}
		return &dirs[key-'1'], true
	} else {
		dirs := [9]math.CardinalOrdinalDirection{
			math.SouthWest, math.South, math.SouthEast,
			math.West, math.CardinalOrdinalDirection(-1), math.East,
			math.NorthWest, math.North, math.NorthEast,
		}
		return &dirs[key-'1'], true
	}
}

func (sp *STARSPane) consumeMouseEvents(ctx *panes.Context, ghosts []*av.GhostTrack,
	transforms ScopeTransformations, tracks []sim.Track, cb *renderer.CommandBuffer) {
	if ctx.Mouse == nil {
		return
	}

	mouse := ctx.Mouse
	ps := sp.currentPrefs()

	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] && !ctx.HaveFocus {
		if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil {
			sp.events.PostEvent(sim.Event{
				Type:         sim.TrackClickedEvent,
				ADSBCallsign: trk.ADSBCallsign,
			})
		}
		ctx.KeyboardFocus.Take(sp)
		return
	}
	if (ctx.Mouse.Clicked[platform.MouseButtonSecondary] || ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
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

	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
			mouseLatLong := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		if ctx.Keyboard != nil && ctx.Keyboard.KeyControl() && !ctx.Keyboard.KeyShift() { // There is a conflict between this and initating a track CRC-style,
			// so making sure that shift isn't being pressed would be a good idea.
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil {
				if state := sp.TrackState[trk.ADSBCallsign]; state != nil {
					state.IsSelected = !state.IsSelected
					return
				}
			}
		}

		// If a scope click handler has been registered, give it the click
		// and then clear it out.
		var status CommandStatus
		if sp.scopeClickHandler != nil {
			status = sp.scopeClickHandler(ctx.Mouse.Pos, transforms)
		}
		if sp.scopeClickHandler == nil {
			status = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.Mouse.Pos, ghosts, transforms, tracks)
		}

		if status.err != nil {
			sp.displayError(status.err, ctx, "")
		} else {
			if status.clear {
				sp.resetInputState(ctx)
			}
			sp.maybeAutoHomeCursor(ctx)
			sp.previewAreaOutput = status.output
		}
	} else if ctx.Mouse.Clicked[platform.MouseButtonTertiary] {
		if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil {
			if state := sp.TrackState[trk.ADSBCallsign]; state != nil {
				state.IsSelected = !state.IsSelected
			}
		}
	} else if !ctx.Client.State.Paused {
		switch sp.currentPrefs().DwellMode {
		case DwellModeOff:
			sp.dwellAircraft = ""

		case DwellModeOn:
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil {
				sp.dwellAircraft = trk.ADSBCallsign
			} else {
				sp.dwellAircraft = ""
			}

		case DwellModeLock:
			if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil {
				sp.dwellAircraft = trk.ADSBCallsign
			}
			// Otherwise leave sp.dwellAircraft as is
		}
	} else {
		if trk, _ := sp.tryGetClosestTrack(ctx, ctx.Mouse.Pos, transforms, tracks); trk != nil && !trk.IsUnsupportedDB() {
			td := renderer.GetTextDrawBuilder()
			defer renderer.ReturnTextDrawBuilder(td)

			ps := sp.currentPrefs()
			font := sp.systemFont(ctx, ps.CharSize.Datablocks)
			style := renderer.TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: 0}

			// Track position in window coordinates
			pac := transforms.WindowFromLatLongP(trk.Location)

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
}

func (sp *STARSPane) resetInputState(ctx *panes.Context) {
	sp.previewAreaInput = ""
	sp.previewAreaOutput = ""
	sp.commandMode = CommandModeNone
	sp.multiFuncPrefix = ""

	sp.wipRBL = nil
	sp.wipSignificantPoint = nil
	sp.wipRestrictionArea = nil

	sp.scopeClickHandler = nil
	sp.activeSpinner = nil

	sp.drawRoutePoints = nil

	ctx.Platform.EndCaptureMouse()
	ctx.Platform.StopMouseDeltaMode()
}

func (sp *STARSPane) displayError(err error, ctx *panes.Context, acid sim.ACID) {
	if err != nil { // it should be, but...
		sp.playOnce(ctx.Platform, AudioCommandError)
		sp.previewAreaOutput = GetSTARSError(err, ctx.Lg).Error()

		if err == ErrSTARSDuplicateACID {
			sp.previewAreaOutput += " " + string(acid)
			if trk, ok := ctx.Client.State.GetTrackByACID(acid); ok && trk.IsAssociated() {
				sp.previewAreaOutput += "\nFLIGHT ACTIVE AT " + trk.FlightPlan.TrackingController
			} else if idx := slices.IndexFunc(ctx.Client.State.UnassociatedFlightPlans,
				func(fp *sim.STARSFlightPlan) bool {
					return fp.ACID == acid
				}); idx != -1 {
				fp := ctx.Client.State.UnassociatedFlightPlans[idx]
				if fp.TrackingController != "" {
					sp.previewAreaOutput += "\nFLIGHT INACTIVE AT " + fp.TrackingController
				}
			}
		}
	}
}

type QuickLookPosition struct {
	Id   string
	Plus bool
}

func (q QuickLookPosition) String() string {
	return q.Id + util.Select(q.Plus, "+", "")
}

func (sp *STARSPane) parseQuickLookPositions(ctx *panes.Context, s string) ([]QuickLookPosition, string, error) {
	var positions []QuickLookPosition

	// per 6-94, this is "fun"
	// - in general the string is a list of TCPs / sector ids.
	// - each may have a plus at the end
	// - if a single character id is entered, then we prepend the number for
	//   the current controller's sector id. in that case a space is required
	//   before the next one, if any
	ids := strings.Fields(s)
	for i, id := range ids {
		plus := len(id) > 1 && id[len(id)-1] == '+'
		id = strings.TrimRight(id, "+")

		control := sp.lookupControllerForId(ctx, id, "")
		if control == nil || control.FacilityIdentifier != "" || control.Id() == ctx.UserTCP {
			return positions, strings.Join(ids[i:], " "), ErrSTARSCommandFormat
		} else {
			positions = append(positions, QuickLookPosition{
				Id:   control.Id(),
				Plus: plus,
			})
		}
	}

	return positions, "", nil
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
				if control.TCP == id[1:] && control.FacilityIdentifier == string(id[0]) {
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
		} else if ctrl, ok := ctx.Client.State.Controllers[tcp]; ok {
			return ctrl
		}
	} else {
		// Non ARTCC airspace-awareness handoffs
		if lc == 1 { // Must be a same sector.
			userController := *ctx.Client.State.Controllers[ctx.UserTCP]

			for _, control := range ctx.Client.State.Controllers { // If the controller fac/ sector == userControllers fac/ sector its all good!
				if control.FacilityIdentifier == "" && // Same facility? (Facility ID will be "" if they are the same fac)
					control.TCP[0] == userController.TCP[0] && // Same Sector?
					string(control.TCP[1]) == id { // The actual controller
					return control
				}
			}
		} else if lc == 2 {
			// Must be a same sector || same facility.
			for _, control := range ctx.Client.State.Controllers {
				if control.TCP == id && control.FacilityIdentifier == "" {
					return control
				}
			}
		}

		for _, control := range ctx.Client.State.Controllers {
			if control.ERAMFacility && control.TCP == id {
				return control
			}
		}
	}
	return nil
}

func (sp *STARSPane) tryGetClosestTrack(ctx *panes.Context, mousePosition [2]float32, transforms ScopeTransformations,
	tracks []sim.Track) (*sim.Track, float32) {
	var trk *sim.Track
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, t := range tracks {
		pw := transforms.WindowFromLatLongP(t.Location)
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			trk = &t
			distance = dist
		}
	}

	return trk, distance
}

func (sp *STARSPane) tryGetClosestGhost(ghosts []*av.GhostTrack, mousePosition [2]float32, transforms ScopeTransformations) (*av.GhostTrack, float32) {
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

func (sp *STARSPane) createFlightPlan(ctx *panes.Context, spec sim.STARSFlightPlanSpecifier) {
	if !spec.TrackingController.IsSet {
		spec.TrackingController.Set(ctx.UserTCP)
	}
	ctx.Client.CreateFlightPlan(spec, sim.LocalNonEnroute,
		func(fp sim.STARSFlightPlan) {
			sp.previewAreaOutput = sp.formatFlightPlan(ctx, &fp, nil)
		},
		func(err error) { sp.displayError(err, ctx, "") })

}

func (sp *STARSPane) modifyFlightPlan(ctx *panes.Context, acid sim.ACID, spec sim.STARSFlightPlanSpecifier, display bool) {
	if !spec.ACID.IsSet {
		spec.ACID.Set(acid)
	}

	ctx.Client.ModifyFlightPlan(acid, spec,
		func(fp sim.STARSFlightPlan) {
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
				sp.previewAreaOutput = sp.formatFlightPlan(ctx, &fp, trk)
			}
		},
		func(err error) {
			sp.displayError(err, ctx, acid)
		})
}

// Returns all aircraft that match the given suffix. If instructor is true,
// returns all matching aircraft; otherwise only ones under the current
// controller's control are considered for matching.
func (sp *STARSPane) tracksFromACIDSuffix(ctx *panes.Context, suffix string, instructor bool) []*sim.Track {
	match := func(trk *sim.Track) bool {
		if trk.IsUnassociated() {
			return false
		}

		fp := trk.FlightPlan
		if !strings.HasSuffix(string(fp.ACID), suffix) {
			return false
		}

		if instructor || fp.ControllingController == ctx.UserTCP {
			return true
		}

		// Hold for release aircraft still in the list
		if ctx.Client.State.ResolveController(trk.FlightPlan.TrackingController) == ctx.UserTCP &&
			trk.FlightPlan.ControllingController == "" {
			return true
		}
		return false
	}
	return slices.Collect(util.FilterSeq(maps.Values(ctx.Client.State.Tracks), match))
}
