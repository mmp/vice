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

	"github.com/davecgh/go-spew/spew"
)

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
		return "TG"
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
			sp.displayError(ErrSTARSCapacity, ctx)
			return
		}
	}

	sp.previewAreaInput += strings.Replace(input, "`", STARSTriangleCharacter, -1)

	ps := sp.currentPrefs()

	for key := range ctx.Keyboard.Pressed {
		switch key {
		case platform.KeyBackspace:
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

		case platform.KeyEnd:
			sp.setCommandMode(ctx, CommandModeMin)

		case platform.KeyEnter:
			if status := sp.executeSTARSCommand(sp.previewAreaInput, ctx); status.err != nil {
				sp.displayError(status.err, ctx)
			} else {
				if status.clear {
					sp.setCommandMode(ctx, CommandModeNone)
					sp.maybeAutoHomeCursor(ctx)
				}
				sp.previewAreaOutput = status.output
			}

		case platform.KeyEscape:
			if sp.activeSpinner != nil {
				sp.setCommandMode(ctx, sp.activeSpinner.EscapeMode())
			} else {
				sp.setCommandMode(ctx, CommandModeNone)
			}

		case platform.KeyF1:
			if ctx.Keyboard.WasPressed(platform.KeyControl) {
				// Recenter
				ps.UseUserCenter = false
			}
			if ctx.Keyboard.WasPressed(platform.KeyShift) {
				// Treat this as F13
				sp.setCommandMode(ctx, CommandModeReleaseDeparture)
			}

		case platform.KeyF2:
			if ctx.Keyboard.WasPressed(platform.KeyControl) {
				sp.setCommandMode(ctx, CommandModeMaps)
			}

		case platform.KeyF3:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeBrite)
			} else {
				sp.setCommandMode(ctx, CommandModeInitiateControl)
			}

		case platform.KeyF4:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeLDR)
			} else {
				sp.setCommandMode(ctx, CommandModeTerminateControl)
			}

		case platform.KeyF5:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeCharSize)
			} else {
				sp.setCommandMode(ctx, CommandModeHandOff)
			}

		case platform.KeyF6:
			sp.setCommandMode(ctx, CommandModeFlightData)

		case platform.KeyF7:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeNone)
				sp.dcbShowAux = !sp.dcbShowAux
			} else {
				sp.setCommandMode(ctx, CommandModeMultiFunc)
			}

		case platform.KeyF8:
			if ctx.Keyboard.WasPressed(platform.KeyControl) {
				sp.resetInputState(ctx)
				ps.DisplayDCB = !ps.DisplayDCB
			} else {
				sp.setCommandMode(ctx, CommandModeWX)
			}

		case platform.KeyF9:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRangeRings)
			} else {
				sp.setCommandMode(ctx, CommandModeVFRPlan)
			}

		case platform.KeyF10:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeRange)
			}

		case platform.KeyF11:
			if ctx.Keyboard.WasPressed(platform.KeyControl) && ps.DisplayDCB {
				sp.setCommandMode(ctx, CommandModeSite)
			} else {
				sp.setCommandMode(ctx, CommandModeCollisionAlert)
			}

		case platform.KeyF12:
			sp.setCommandMode(ctx, CommandModeRestrictionArea)

		case platform.KeyF13:
			sp.setCommandMode(ctx, CommandModeReleaseDeparture)

		case platform.KeyInsert:
			sp.setCommandMode(ctx, CommandModePref)

		case platform.KeyTab:
			sp.setCommandMode(ctx, CommandModeTargetGen)
		}
	}
}

func (sp *STARSPane) executeSTARSCommand(cmd string, ctx *panes.Context) (status CommandStatus) {
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

	lookupAircraft := func(callsign string) *av.Aircraft {
		if ac := ctx.ControlClient.Aircraft[callsign]; ac != nil {
			return ac
		}

		// try to match squawk code
		if sq, err := av.ParseSquawk(callsign); err == nil {
			for _, ac := range sp.visibleAircraft(ctx) {
				if ac.Squawk == sq {
					return ac
				}
			}
		}

		if idx, err := strconv.Atoi(callsign); err == nil {
			if idx >= 0 && idx < TabListEntries && sp.TabListAircraft[idx] != "" {
				return ctx.ControlClient.Aircraft[sp.TabListAircraft[idx]]
			}

			if trk := ctx.ControlClient.STARSComputer().LookupTrackIndex(idx); trk != nil {
				// May be nil, but this is our last option
				return ctx.ControlClient.Aircraft[trk.Identifier]
			}
		}

		return nil
	}

	lookupCallsign := func(callsign string) string {
		if ac := lookupAircraft(callsign); ac != nil {
			return ac.Callsign
		}
		return callsign
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
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.output = util.Select(ps.DisplayTPASize, "TPA SIZE ON", "TPA SIZE OFF")
			status.clear = true
			return

		case "*D+E":
			// Enable
			ps.DisplayTPASize = true
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE ON"
			return

		case "*D+I":
			// Inhibit
			ps.DisplayTPASize = false
			for _, state := range sp.Aircraft {
				state.DisplayTPASize = nil
			}
			status.clear = true
			status.output = "TPA SIZE OFF"
			return

		case "**J":
			// remove all j-rings
			for _, state := range sp.Aircraft {
				state.JRingRadius = 0
			}
			status.clear = true
			return

		case "**P":
			// remove all cones
			for _, state := range sp.Aircraft {
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

		case "?":
			ctx.ControlClient.State.ERAMComputers.DumpMap()
			status.clear = true
			return

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
			} else if !util.SeqContainsFunc(maps.Values(ctx.ControlClient.Aircraft),
				func(ac *av.Aircraft) bool { return ac.Squawk == code }) {
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
			aircraft := lookupAircraft(callsign)
			if aircraft == nil {
				status.err = ErrSTARSNoFlight
			} else {
				for _, tcp := range strings.Split(tcps, " ") {
					if tcp == "ALL" {
						fac := ctx.ControlClient.Controllers[ctx.ControlClient.UserTCP].FacilityIdentifier
						for _, control := range ctx.ControlClient.Controllers {
							if !control.ERAMFacility && control.FacilityIdentifier == fac {
								sp.forceQL(ctx, aircraft.Callsign, control.Id())
							}
						}
					} else {
						control := sp.lookupControllerForId(ctx, tcp, aircraft.Callsign)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
							return
						}
						sp.forceQL(ctx, aircraft.Callsign, control.Id())
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
			} else if p, ok := ctx.ControlClient.Locate(suffix); ok {
				// Fix name for first or second point of RBL
				if rbl := sp.wipRBL; rbl != nil {
					rbl.P[1].Loc = p
					sp.RangeBearingLines = append(sp.RangeBearingLines, *rbl)
					sp.wipRBL = nil
					status.clear = true
				} else {
					sp.wipRBL = &STARSRangeBearingLine{}
					sp.wipRBL.P[0].Loc = p
					sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
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
					ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
			} else {
				status.err = ErrSTARSCommandFormat
			}
			return
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
			} else if cmd == ctx.ControlClient.UserTCP { // TODO: any TCP assigned to this scope
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
			} else if sp.previewAreaInput, status.err = sp.updateQL(ctx, cmd); status.err == nil {
				// It was valid quicklook positions
				status.clear = true
				return
			} else {
				// Is it an abbreviated flight plan?
				fp, err := sim.MakeSTARSFlightPlanFromAbbreviated(cmd, ctx.ControlClient.STARSComputer(),
					ctx.ControlClient.STARSFacilityAdaptation)
				if fp != nil {
					ctx.ControlClient.UploadFlightPlan(fp, av.LocalNonEnroute, nil,
						func(err error) { sp.displayError(err, ctx) })
					status.output = fmt.Sprintf("%v%v%v %04o\nNO ROUTE %v", fp.Callsign,
						util.Select(fp.AircraftType != "", " ", ""), fp.AircraftType, fp.AssignedSquawk,
						util.Select(fp.Altitude != "VFR", fp.Altitude, ""))
				}
				status.clear = err == nil
				status.err = err
				return
			}
		}

	case CommandModeInitiateControl:
		if ac := lookupAircraft(cmd); ac == nil {
			status.err = ErrSTARSCommandFormat
		} else if err := sp.initiateTrack(ctx, ac.Callsign); err != nil {
			status.err = err
		} else {
			status.clear = true
		}
		return

	case CommandModeTerminateControl:
		if cmd == "ALL" {
			for callsign, ac := range ctx.ControlClient.Aircraft {
				if trk := sp.getTrack(ctx, ac); trk.TrackOwner == ctx.ControlClient.UserTCP {
					sp.dropTrack(ctx, callsign)
				}
			}
			status.clear = true
			return
		} else {
			sp.dropTrack(ctx, lookupCallsign(cmd))
			return
		}

	case CommandModeHandOff:
		f := strings.Fields(cmd)
		switch len(f) {
		case 0:
			// Accept hand off of target closest to range rings center
			var closest *av.Aircraft
			var closestDistance float32
			for _, ac := range sp.visibleAircraft(ctx) {
				trk := sp.getTrack(ctx, ac)
				if trk.HandoffController != ctx.ControlClient.UserTCP {
					continue
				}

				state := sp.Aircraft[ac.Callsign]
				ctr := util.Select(ps.UseUserRangeRingsCenter, ps.RangeRingsUserCenter, ps.DefaultCenter)
				d := math.NMDistance2LL(ctr, state.TrackPosition())
				if closest == nil || d < closestDistance {
					closest = ac
					closestDistance = d
				}
			}

			if closest != nil {
				sp.acceptHandoff(ctx, closest.Callsign)
			}
			status.clear = true
			return
		case 1:
			// Is it an ACID?
			if ac := lookupAircraft(f[0]); ac != nil {
				sp.cancelHandoff(ctx, ac.Callsign)
				status.clear = true
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
			if err := sp.handoffTrack(ctx, lookupCallsign(f[1]), f[0]); err != nil {
				status.err = err
			} else {
				status.clear = true
			}
			return
		}

	case CommandModeVFRPlan:
		// TODO
		status.err = ErrSTARSCommandFormat
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
			} else if ac := lookupAircraft(cmd); ac != nil {
				// D(callsign)
				// Display flight plan
				status.output, status.err = sp.flightPlanSTARS(ctx, ac)
				if status.err == nil {
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
				sp.prefSet.ResetDefault(ctx.ControlClient.State, ctx.Platform, sp)
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
					if ac := lookupAircraft(acid); ac != nil {
						if err := sp.setLeaderLine(ctx, ac, num); err != nil {
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
						if ctrl, ok := ctx.ControlClient.Controllers[ctx.ControlClient.UserTCP]; ok {
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
			if len(cmd) > 2 {
				aircraft := lookupAircraft(cmd)
				if aircraft == nil {
					status.err = ErrSTARSCommandFormat
					return
				} else if trk := sp.getTrack(ctx, aircraft); trk.TrackOwner != ctx.ControlClient.UserTCP {
					status.err = ErrSTARSIllegalTrack
					return
				} else {
					if len(trk.PointOutHistory) == 0 {
						status.output = "PO NONE"
					} else {
						status.output = strings.Join(aircraft.PointOutHistory, " ")
					}
					status.clear = true
					return
				}
			}
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
				sp.previewAreaInput, status.err = sp.updateQL(ctx, cmd)
				status.clear = status.err == nil
				return
			}

		case "R": // requested altitude: 6-107
			switch cmd {
			case "": // clear all individually-enabled PTLs
				for _, state := range sp.Aircraft {
					state.DisplayPTL = false
				}
				status.clear = true
				return
			case "A": // toggle
				ps.DisplayRequestedAltitude = !ps.DisplayRequestedAltitude
				status.clear = true
				return
			case "AE": // enable
				ps.DisplayRequestedAltitude = true
				status.clear = true
				return
			case "AI": // inhibit
				ps.DisplayRequestedAltitude = false
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
				callsign := lookupCallsign(f[0])
				sp.setPilotReportedAltitude(ctx, callsign, 0)
				if err := sp.setScratchpad(ctx, callsign, "", isSecondary, false); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if len(f) == 2 {
				// Y callsign <space> scratch -> set scatchpad
				// Y callsign <space> ### -> set pilot alt
				// as above, Y+ -> secondary scratchpad

				// Either pilot alt or scratchpad entry
				if ac := lookupAircraft(f[0]); ac == nil {
					status.err = ErrSTARSNoFlight
				} else if alt, err := strconv.Atoi(f[1]); err == nil {
					sp.setPilotReportedAltitude(ctx, ac.Callsign, alt)
					status.clear = true
				} else {
					if err := sp.setScratchpad(ctx, ac.Callsign, f[1], isSecondary, false); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
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
		f := strings.Fields(cmd)
		if len(f) == 1 {
			callsign := lookupCallsign(f[0])
			status.err = ctx.ControlClient.SetSquawkAutomatic(callsign)
		} else if len(f) == 2 {
			if squawk, err := av.ParseSquawk(f[1]); err == nil {
				callsign := lookupCallsign(f[0])
				status.err = ctx.ControlClient.SetSquawk(callsign, squawk)
			} else {
				status.err = ErrSTARSIllegalCode
			}
		} else {
			status.err = ErrSTARSCommandFormat
		}
		if status.err == nil {
			status.clear = true
		}
		return

	case CommandModeCollisionAlert:
		if len(cmd) > 3 && cmd[:2] == "K " {
			if ac := lookupAircraft(cmd[2:]); ac != nil {
				state := sp.Aircraft[ac.Callsign]
				state.DisableCAWarnings = !state.DisableCAWarnings
				state.MCISuppressedCode = av.Squawk(0) // 7-18: this clears the MCI inhibit code
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
			} else if ac := lookupAircraft(f[0]); ac == nil {
				status.err = ErrSTARSNoFlight
			} else if len(f) == 1 {
				status = sp.updateMCISuppression(ctx, ac, "")
			} else {
				status = sp.updateMCISuppression(ctx, ac, f[1])
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
			} else if slices.ContainsFunc(sp.allVideoMaps, func(v av.VideoMap) bool { return v.Id == idx }) {
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
		radarSites := ctx.ControlClient.State.STARSFacilityAdaptation.RadarSites

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
				ctx.ControlClient.DeleteRestrictionArea(n, nil, func(err error) { sp.displayError(err, ctx) })
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
		rel := ctx.ControlClient.State.GetSTARSReleaseDepartures()

		// Filter out the ones that have been released and then deleted
		// from the coordination list by the controller.
		rel = util.FilterSliceInPlace(rel,
			func(ac *av.Aircraft) bool { return !sp.Aircraft[ac.Callsign].ReleaseDeleted })

		if cmd == "" {
			// If there is only one unacknowledged, then ack/release it.
			unack := util.FilterSliceInPlace(rel, func(ac *av.Aircraft) bool { return !ac.Released })
			switch len(unack) {
			case 0:
				status.err = ErrSTARSIllegalFlight
			case 1:
				ctx.ControlClient.ReleaseDeparture(unack[0].Callsign, nil,
					func(err error) { sp.displayError(err, ctx) })
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
			ac := func() *av.Aircraft {
				n, nerr := strconv.Atoi(cmd)
				sq, sqerr := av.ParseSquawk(cmd)
				for _, ac := range rel {
					if ac.Callsign == cmd {
						return ac
					}
					if sqerr == nil && sq == ac.Squawk {
						return ac
					}
					if nerr == nil && n >= 0 && n == sp.Aircraft[ac.Callsign].TabListIndex {
						return ac
					}
				}
				return nil
			}()
			if ac == nil {
				if _, err := strconv.Atoi(cmd); err == nil && len(cmd) < 4 /* else assume it's a beacon code */ {
					// Given a line number that doesn't exist.
					status.err = ErrSTARSIllegalLine
				} else if ac := lookupAircraft(cmd); ac != nil {
					// There is such a flight but it's not in our release list.
					if ac.HoldForRelease {
						// It's in another controller's list
						status.err = ErrSTARSIllegalFunction
					} else {
						status.err = ErrSTARSIllegalFlight
					}
				} else {
					// No such flight anywhere.
					status.err = ErrSTARSNoFlight
				}
			} else if !ac.Released {
				ac.Released = true // hack for instant update pending the next server update
				ctx.ControlClient.ReleaseDeparture(ac.Callsign, nil,
					func(err error) { sp.displayError(err, ctx) })
				status.clear = true
			} else {
				sp.Aircraft[ac.Callsign].ReleaseDeleted = true
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
			ctx.ControlClient.ToggleSimPause()
			status.clear = true
			return
		}

		// Otherwise looks like an actual control instruction .
		suffix, cmds, ok := strings.Cut(cmd, " ")
		if !ok {
			suffix = sp.targetGenLastCallsign
			cmds = cmd
		}

		instructor := ctx.ControlClient.AmInstructor()
		matching := ctx.ControlClient.AircraftFromCallsignSuffix(suffix, instructor)
		if len(matching) > 1 {
			status.err = ErrSTARSAmbiguousACID
			return
		}

		var ac *av.Aircraft
		if len(matching) == 1 {
			ac = matching[0]
		} else if len(matching) == 0 && sp.targetGenLastCallsign != "" {
			// If a valid callsign wasn't given, try the last callsign used.
			ac = ctx.ControlClient.Aircraft[sp.targetGenLastCallsign]
			// But now we're going to run all of the given input as commands.
			cmds = cmd
		}

		if ac != nil {
			sp.runAircraftCommands(ctx, ac, cmds)
			sp.targetGenLastCallsign = ac.Callsign
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

func (sp *STARSPane) runAircraftCommands(ctx *panes.Context, ac *av.Aircraft, cmds string) {
	ctx.ControlClient.RunAircraftCommands(ac.Callsign, cmds,
		func(errStr string, remaining string) {
			if errStr != "" {
				sp.commandMode = CommandModeTargetGen
				sp.previewAreaInput = remaining
				if err := server.TryDecodeErrorString(errStr); err != nil {
					err = GetSTARSError(err, ctx.Lg)
					sp.displayError(err, ctx)
				} else {
					sp.displayError(ErrSTARSCommandFormat, ctx)
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
	if idx < 1 || idx-1 >= len(ctx.ControlClient.State.UserRestrictionAreas) {
		return nil
	} else if ra := &ctx.ControlClient.State.UserRestrictionAreas[idx-1]; ra.Deleted {
		return nil
	} else {
		return ra
	}
}

func getRestrictionAreaByIndex(ctx *panes.Context, idx int) *av.RestrictionArea {
	if ra := getUserRestrictionAreaByIndex(ctx, idx); ra != nil {
		return ra
	} else if idx < 101 || idx-101 >= len(ctx.ControlClient.STARSFacilityAdaptation.RestrictionAreas) {
		return nil
	} else if ra := &ctx.ControlClient.STARSFacilityAdaptation.RestrictionAreas[idx-101]; ra.Deleted {
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

		p = math.Offset2LL(p, float32(bearing), float32(dist), ctx.ControlClient.NmPerLongitude,
			ctx.ControlClient.MagneticVariation)
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
		sp.ReleaseRequests = make(map[string]interface{})
	}

	ps := sp.currentPrefs()
	releaseAircraft := ctx.ControlClient.State.GetSTARSReleaseDepartures()

	fa := ctx.ControlClient.STARSFacilityAdaptation
	for _, list := range fa.CoordinationLists {
		// Get the aircraft that should be included in this list.
		aircraft := util.FilterSlice(releaseAircraft,
			func(ac *av.Aircraft) bool { return slices.Contains(list.Airports, ac.FlightPlan.DepartureAirport) })

		cl, ok := ps.CoordinationLists[list.Id]
		if !ok {
			// This shouldn't happen, but...
			continue
		}

		for _, ac := range aircraft {
			if _, ok := sp.ReleaseRequests[ac.Callsign]; !ok {
				// Haven't seen this one before
				if cl.AutoRelease {
					ctx.ControlClient.ReleaseDeparture(ac.Callsign, nil,
						func(err error) { ctx.Lg.Errorf("%s: %v", ac.Callsign, err) })
				}
				// Note that we've seen it, whether or not it was auto-released.
				sp.ReleaseRequests[ac.Callsign] = nil
			}
		}
	}

	// Clean up release requests for aircraft that have departed and aren't
	// on the hold for release list.
	for callsign := range sp.ReleaseRequests {
		if !slices.ContainsFunc(releaseAircraft,
			func(ac *av.Aircraft) bool { return ac.Callsign == callsign }) {
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

func (sp *STARSPane) setScratchpad(ctx *panes.Context, callsign string, contents string, isSecondary bool, isImplied bool) error {
	lc := len([]rune(contents))

	ac := ctx.ControlClient.Aircraft[callsign]
	if ac == nil {
		return ErrSTARSNoFlight
	}

	trk := sp.getTrack(ctx, ac)
	if trk.TrackOwner == "" {
		// This is because /OK can be used for associated tracks that are
		// not owned by this TCP. But /OK cannot be used for unassociated
		// tracks. So might as well weed them out now.
		return ErrSTARSIllegalTrack
	}

	// 5-148
	fac := ctx.ControlClient.STARSFacilityAdaptation
	if fac.AllowLongScratchpad && lc > 4 {
		return ErrSTARSCommandFormat
	} else if !fac.AllowLongScratchpad && lc > 3 {
		return ErrSTARSCommandFormat
	}
	if !isSecondary && isImplied && lc == 1 {
		// One-character for primary is only allowed via [MF]Y
		return ErrSTARSCommandFormat
	}

	// Make sure it's only allowed characters
	allowedCharacters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./*" + STARSTriangleCharacter
	for _, letter := range contents {
		if !strings.ContainsRune(allowedCharacters, letter) {
			return ErrSTARSCommandFormat
		}
	}

	// It can't be three numerals
	if lc == 3 && contents[0] >= '0' && contents[0] <= '9' &&
		contents[1] >= '0' && contents[1] <= '9' &&
		contents[2] >= '0' && contents[2] <= '9' {
		return ErrSTARSCommandFormat
	}

	if !isSecondary && isImplied {
		// For the implied version (i.e., not [multifunc]Y), it also can't
		// match one of the TCPs
		if lc == 2 {
			for _, ctrl := range ctx.ControlClient.Controllers {
				if ctrl.FacilityIdentifier == "" && ctrl.TCP == contents {
					return ErrSTARSCommandFormat
				}
			}
		}
	}

	// Certain specific strings aren't allowed in the first 3 characters
	illegalScratchpads := []string{"NAT", "CST", "AMB", "RDR", "ADB", "XXX"}
	if lc >= 3 && slices.Contains(illegalScratchpads, contents[:3]) {
		return ErrSTARSIllegalScratchpad
	}

	if isSecondary {
		ctx.ControlClient.SetSecondaryScratchpad(callsign, contents, nil,
			func(err error) { sp.displayError(err, ctx) })
	} else {
		if trk.SP1 == "" {
			sp.Aircraft[callsign].ClearedScratchpadAlternate = true
		}
		ctx.ControlClient.SetScratchpad(callsign, contents, nil,
			func(err error) { sp.displayError(err, ctx) })
	}
	return nil
}

func (sp *STARSPane) setTemporaryAltitude(ctx *panes.Context, callsign string, alt int) {
	ctx.ControlClient.SetTemporaryAltitude(callsign, alt, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) setPilotReportedAltitude(ctx *panes.Context, callsign string, alt int) {
	ctx.ControlClient.SetPilotReportedAltitude(callsign, alt*100, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) setGlobalLeaderLine(ctx *panes.Context, callsign string, dir *math.CardinalOrdinalDirection) {
	state := sp.Aircraft[callsign]
	state.GlobalLeaderLineDirection = dir // hack for instant update
	state.UseGlobalLeaderLine = dir != nil

	ctx.ControlClient.SetGlobalLeaderLine(callsign, dir, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) initiateTrack(ctx *panes.Context, callsign string) error {
	// TODO: should we actually be looking up the flight plan on the server
	// side anyway?
	fp, err := ctx.ControlClient.STARSComputer().GetFlightPlan(callsign)
	if err != nil {
		// TODO: do what here?
	}

	if ctx.ControlClient.Aircraft[callsign].Squawk == 0o1200 {
		return ErrSTARSIllegalFlight
	}

	ctx.ControlClient.InitiateTrack(callsign, fp,
		func(any) {
			if ac, ok := ctx.ControlClient.Aircraft[callsign]; ok {
				sp.previewAreaOutput, _ = sp.flightPlanSTARS(ctx, ac)
			}
		},
		func(err error) { sp.displayError(err, ctx) })
	return nil
}

func (sp *STARSPane) dropTrack(ctx *panes.Context, callsign string) {
	ctx.ControlClient.DropTrack(callsign, nil, func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acceptHandoff(ctx *panes.Context, callsign string) {
	ctx.ControlClient.AcceptHandoff(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) handoffTrack(ctx *panes.Context, callsign string, controller string) error {
	control := sp.lookupControllerForId(ctx, controller, callsign)
	if control == nil {
		return ErrSTARSIllegalPosition
	}

	ctx.ControlClient.HandoffTrack(callsign, control.Id(), nil,
		func(err error) { sp.displayError(err, ctx) })

	return nil
}
func (sp *STARSPane) setLeaderLine(ctx *panes.Context, ac *av.Aircraft, cmd string) error {
	state := sp.Aircraft[ac.Callsign]
	if len(cmd) == 1 { // Local 6-81
		if dir, ok := sp.numpadToDirection(cmd[0]); ok {
			state.LeaderLineDirection = dir
			if dir != nil {
				state.UseGlobalLeaderLine = false
			}
			return nil
		}
	} else if len(cmd) == 2 && cmd[0] == cmd[1] { // Global leader lines 6-101
		trk := sp.getTrack(ctx, ac)
		if trk.TrackOwner != ctx.ControlClient.UserTCP {
			return ErrSTARSIllegalTrack
		} else if dir, ok := sp.numpadToDirection(cmd[0]); ok {
			sp.setGlobalLeaderLine(ctx, ac.Callsign, dir)
			return nil
		}
	}
	return ErrSTARSCommandFormat
}

func (sp *STARSPane) forceQL(ctx *panes.Context, callsign, controller string) {
	ctx.ControlClient.ForceQL(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) redirectHandoff(ctx *panes.Context, callsign, controller string) {
	ctx.ControlClient.RedirectHandoff(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acceptRedirectedHandoff(ctx *panes.Context, callsign string) {
	ctx.ControlClient.AcceptRedirectedHandoff(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) removeForceQL(ctx *panes.Context, callsign string) bool {
	if _, ok := sp.ForceQLCallsigns[callsign]; ok {
		delete(sp.ForceQLCallsigns, callsign)
		return true
	}
	return false
}

func (sp *STARSPane) pointOut(ctx *panes.Context, callsign string, controller string) {
	ctx.ControlClient.PointOut(callsign, controller, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) acknowledgePointOut(ctx *panes.Context, callsign string) {
	ctx.ControlClient.AcknowledgePointOut(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) recallPointOut(ctx *panes.Context, callsign string) {
	ctx.ControlClient.RecallPointOut(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) cancelHandoff(ctx *panes.Context, callsign string) {
	ctx.ControlClient.CancelHandoff(callsign, nil,
		func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) updateMCISuppression(ctx *panes.Context, ac *av.Aircraft, code string) (status CommandStatus) {
	ps := sp.currentPrefs()
	if ps.DisableMCIWarnings {
		status.err = ErrSTARSIllegalFunction
	} else if ac.TrackingController != ctx.ControlClient.UserTCP {
		status.err = ErrSTARSIllegalTrack
	} else {
		state := sp.Aircraft[ac.Callsign]
		if code == "" {
			if state.MCISuppressedCode != av.Squawk(0) {
				// clear suppression
				state.MCISuppressedCode = av.Squawk(0)
			} else {
				// TODO: 0477 is the default but it's adaptable
				state.MCISuppressedCode = av.Squawk(0o0477)
				state.DisableCAWarnings = false // 7-30; can't have both
			}
			status.clear = true
		} else if sq, err := av.ParseSquawk(code); err != nil {
			status.err = ErrSTARSIllegalValue // TODO: what should this be?
		} else {
			if state.MCISuppressedCode == sq { // entered same code; clear suppression
				state.MCISuppressedCode = av.Squawk(0)
			} else {
				state.MCISuppressedCode = sq
				state.DisableCAWarnings = false // 7-30; can't have both
			}
			status.clear = true
		}
	}
	return
}

func (sp *STARSPane) executeSTARSClickedCommand(ctx *panes.Context, cmd string, mousePosition [2]float32,
	ghosts []*av.GhostAircraft, transforms ScopeTransformations) (status CommandStatus) {
	// See if an aircraft was clicked
	ac, acDistance := sp.tryGetClosestAircraft(ctx, mousePosition, transforms)
	ghost, ghostDistance := sp.tryGetClosestGhost(ghosts, mousePosition, transforms)

	ps := sp.currentPrefs()

	// The only thing that can happen with a ghost is to switch between a full/partial
	// datablock. Note that if we found both an aircraft and a ghost and a command was entered,
	// we don't issue an error for a bad ghost command but
	if ghost != nil && ghostDistance < acDistance {
		if sp.commandMode == CommandModeNone && cmd == "" {
			state := sp.Aircraft[ghost.Callsign]
			state.Ghost.PartialDatablock = !state.Ghost.PartialDatablock
			status.clear = true
			return
		} else if sp.commandMode == CommandModeMultiFunc && sp.multiFuncPrefix == "N" {
			if cmd == "" {
				// Suppress ghost
				state := sp.Aircraft[ghost.Callsign]
				state.Ghost.State = GhostStateSuppressed
				status.clear = true
				return
			} else if cmd == "*" {
				// Display parent aircraft flight plan
				ac := ctx.ControlClient.Aircraft[ghost.Callsign]
				status.output, status.err = sp.flightPlanSTARS(ctx, ac)
				if status.err == nil {
					status.clear = true
				}
				return
			}
		}
	}

	trySetLeaderLine := func(spec string) bool {
		err := sp.setLeaderLine(ctx, ac, cmd)
		return err == nil
	}

	if ac != nil {
		state := sp.Aircraft[ac.Callsign]
		trk := sp.getTrack(ctx, ac)

		switch sp.commandMode {
		case CommandModeNone:
			if cmd == "" {
				if time.Until(state.RDIndicatorEnd) > 0 {
					if state.OutboundHandoffAccepted {
						state.OutboundHandoffAccepted = false
						state.OutboundHandoffFlashEnd = ctx.Now
					}
					state.RDIndicatorEnd = time.Time{}
					status.clear = true
					return
				} else if trk.RedirectedHandoff.RedirectedTo == ctx.ControlClient.UserTCP || trk.RedirectedHandoff.GetLastRedirector() == ctx.ControlClient.UserTCP {
					sp.acceptRedirectedHandoff(ctx, ac.Callsign)
					status.clear = true
					return
				} else if trk.HandoffController == ctx.ControlClient.UserTCP {
					status.clear = true
					sp.acceptHandoff(ctx, ac.Callsign)
					return
				} else if sp.removeForceQL(ctx, ac.Callsign) {
					status.clear = true
					return
				} else if idx := slices.IndexFunc(sp.CAAircraft, func(ca CAAircraft) bool {
					return (ca.Callsigns[0] == ac.Callsign || ca.Callsigns[1] == ac.Callsign) &&
						!ca.Acknowledged
				}); idx != -1 {
					// Acknowledged a CA
					status.clear = true
					sp.CAAircraft[idx].Acknowledged = true
					return
				} else if idx := slices.IndexFunc(sp.MCIAircraft, func(ca CAAircraft) bool {
					return ca.Callsigns[0] == ac.Callsign && !ca.Acknowledged
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
				} else if (state.SPCAlert || ac.SPCOverride != "") && !state.SPCAcknowledged {
					// Acknowledged SPC alert
					state.SPCAcknowledged = true
					status.clear = true
					return
				} else if _, ok := sp.DuplicateBeacons[ac.Squawk]; ok && state.DBAcknowledged != ac.Squawk {
					state.DBAcknowledged = ac.Squawk
					status.clear = true
					return
				} else if trk.HandoffController != "" && trk.HandoffController != ctx.ControlClient.UserTCP &&
					trk.TrackOwner == ctx.ControlClient.UserTCP {
					// cancel offered handoff offered
					status.clear = true
					sp.cancelHandoff(ctx, ac.Callsign)
					return
				} else if tcps, ok := sp.PointOuts[ac.Callsign]; ok && tcps.To == ctx.ControlClient.UserTCP {
					// ack point out
					sp.acknowledgePointOut(ctx, ac.Callsign)
					status.clear = true
					return
				} else if ok && tcps.From == ctx.ControlClient.UserTCP {
					// recall point out
					sp.recallPointOut(ctx, ac.Callsign)
					status.clear = true
					return
				} else if state.PointOutAcknowledged {
					state.PointOutAcknowledged = false
					status.clear = true
					return
				} else if state.ForceQL {
					state.ForceQL = false
					status.clear = true
				} else if _, ok := sp.RejectedPointOuts[ac.Callsign]; ok {
					// ack rejected point out
					delete(sp.RejectedPointOuts, ac.Callsign)
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
				} else if ctx.Keyboard != nil {
					_, ctrl := ctx.Keyboard.Pressed[platform.KeyControl]
					_, shift := ctx.Keyboard.Pressed[platform.KeyShift]
					if ctrl && shift {
						// initiate track, CRC style
						if err := sp.initiateTrack(ctx, ac.Callsign); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
						return
					}
				}
				if db := sp.datablockType(ctx, ac); db == LimitedDatablock {
					s := ctx.ControlClient.STARSFacilityAdaptation.FullLDBSeconds
					if s == 0 {
						s = 5
					}
					state.FullLDBEndTime = ctx.Now.Add(time.Duration(s) * time.Second)
				} else if trk.TrackOwner != ctx.ControlClient.UserTCP {
					state.DisplayFDB = !state.DisplayFDB
				}

				if trk.TrackOwner == ctx.ControlClient.UserTCP {
					status.output = slewAircaft(ac)
				}

			} else if cmd == "." {
				if err := sp.setScratchpad(ctx, ac.Callsign, "", false, true); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if cmd == "+" {
				if err := sp.setScratchpad(ctx, ac.Callsign, "", true, true); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return
			} else if cmd == "*" {
				from := sp.Aircraft[ac.Callsign].TrackPosition()
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
					p := transforms.LatLongFromWindowP(pw)
					hdg := math.Heading2LL(from, p, ac.NmPerLongitude(), ac.MagneticVariation())
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
				ctx.Lg.Info("print aircraft", slog.String("callsign", ac.Callsign),
					slog.Any("aircraft", ac))
				fmt.Println(spew.Sdump(ac) + "\n" + ac.Nav.FlightState.Summary())
				status.clear = true
				return
			} else if cmd == "*F" {
				// 6-148 range/bearing to significant point
				p := state.TrackPosition()
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
				sp.wipRBL.P[0].Callsign = ac.Callsign
				sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
				// Do not clear the input area to allow entering a fix for the second location
				return
			} else if ctx.ControlClient.StringIsSPC(cmd) {
				state.SPCAcknowledged = false
				ctx.ControlClient.ToggleSPCOverride(ac.Callsign, cmd, nil,
					func(err error) { sp.displayError(err, ctx) })
				status.clear = true
				return
			} else if cmd == "UN" {
				ctx.ControlClient.RejectPointOut(ac.Callsign, nil,
					func(err error) { sp.displayError(err, ctx) })
				status.clear = true
				return
			} else if lc := len(cmd); lc >= 2 && cmd[0:2] == "**" { // Force QL. You need to specify a TCP unless otherwise specified in STARS config
				// STARS Manual 6-70 (On slew). Cannot go interfacility
				// TODO: Or can be used to accept a pointout as a handoff.

				if cmd == "**" { // Non specified TCP
					if ctx.ControlClient.STARSFacilityAdaptation.ForceQLToSelf && trk.TrackOwner == ctx.ControlClient.UserTCP {
						state.ForceQL = true
						status.clear = true
						return
					} else {
						status.err = ErrSTARSIllegalPosition
						return
					}
				} else {
					tcps := strings.Split(cmd[2:], " ")
					if len(tcps) > 0 && tcps[0] == "ALL" {
						// Force QL for all TCP
						// Find user fac
						if ctrl, ok := ctx.ControlClient.Controllers[ctx.ControlClient.UserTCP]; ok && !ctrl.ERAMFacility {
							sp.forceQL(ctx, ac.Callsign, ctx.ControlClient.UserTCP)
						}
					}
					for _, tcp := range tcps {
						control := sp.lookupControllerForId(ctx, tcp, ac.Callsign)
						if control == nil {
							status.err = ErrSTARSIllegalPosition
							return
						}
						sp.forceQL(ctx, ac.Callsign, control.Id())
					}
					status.clear = true
					return
				}

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
			} else if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
				sp.setPilotReportedAltitude(ctx, ac.Callsign, alt)
				status.clear = true
				return
			} else if len(cmd) == 5 && cmd[:2] == "++" {
				if alt, err := strconv.Atoi(cmd[2:]); err == nil {
					status.err = amendFlightPlan(ctx, ac.Callsign, func(fp *av.FlightPlan) {
						fp.Altitude = alt * 100
					})
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return
			} else if len(cmd) >= 2 && cmd[0] == '+' {
				if alt, err := strconv.Atoi(cmd[1:]); err == nil {
					sp.setTemporaryAltitude(ctx, ac.Callsign, alt*100)
					status.clear = true
				} else {
					if err := sp.setScratchpad(ctx, ac.Callsign, cmd[1:], true, true); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
				return
			} else if cmd == ".ROUTE" {
				sp.drawRouteAircraft = ac.Callsign
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
				if r, err := strconv.Atoi(cmd[2:]); err == nil {
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
				if _, ok := sp.PointOuts[ac.Callsign]; ok {
					status.err = ErrSTARSIllegalTrack
					return
				}
				if ac.HandoffTrackController != "" && ac.HandoffTrackController != ctx.ControlClient.UserTCP {
					status.err = ErrSTARSIllegalTrack
					return
				}

				control := sp.lookupControllerForId(ctx, strings.TrimSuffix(cmd, "*"), ac.Callsign)
				if control == nil {
					status.err = ErrSTARSIllegalPosition
				} else {
					status.clear = true
					sp.pointOut(ctx, ac.Callsign, control.Id())
				}
				return

			} else if len(cmd) > 0 {
				// If it matches the callsign, attempt to initiate track.
				if cmd == ac.Callsign {
					if err := sp.initiateTrack(ctx, ac.Callsign); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
					return
				}

				// See if cmd works as a sector id; if so, make it a handoff.
				control := sp.lookupControllerForId(ctx, cmd, ac.Callsign)
				if control != nil {
					if ac.HandoffTrackController == ctx.ControlClient.UserTCP ||
						ac.RedirectedHandoff.RedirectedTo == ctx.ControlClient.UserTCP { // Redirect
						sp.redirectHandoff(ctx, ac.Callsign, control.Id())
						status.clear = true
					} else if err := sp.handoffTrack(ctx, ac.Callsign, cmd); err == nil {
						status.clear = true
					} else {
						status.err = err
					}
				} else {
					// Try setting the scratchpad
					if err := sp.setScratchpad(ctx, ac.Callsign, cmd, false, true); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
				}
				return
			}

		case CommandModeInitiateControl:
			if cmd != ac.Callsign {
				status.err = ErrSTARSCommandFormat
			} else if err := sp.initiateTrack(ctx, ac.Callsign); err != nil {
				status.err = err
			} else {
				status.clear = true
			}
			return

		case CommandModeTerminateControl:
			// TODO: error if cmd != ""?
			status.clear = true
			sp.dropTrack(ctx, ac.Callsign)
			return

		case CommandModeHandOff:
			if cmd == "" {
				if po, ok := sp.PointOuts[ac.Callsign]; ok && po.To == ctx.ControlClient.UserTCP {
					sp.acceptHandoff(ctx, ac.Callsign)
				} else {
					// Try to cancel it; if it's not ours, we'll get an error from this
					sp.cancelHandoff(ctx, ac.Callsign)
				}
				status.clear = true
			} else {
				if err := sp.handoffTrack(ctx, ac.Callsign, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
			}
			return

		case CommandModeVFRPlan:
			// TODO: implement
			status.err = ErrSTARSCommandFormat
			return

		case CommandModeMultiFunc:
			switch sp.multiFuncPrefix {
			case "B":
				if cmd == "" {
					if trk := sp.getTrack(ctx, ac); trk.TrackOwner != "" {
						// Associated track; display ACID, RBC (received beacon code), ABC (assigned beacon code) in preview area.
						status.output = ac.Callsign + " " + ac.Squawk.String() + " " + ac.FlightPlan.AssignedSquawk.String()
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
					status.output, status.err = sp.flightPlanSTARS(ctx, ac)
					if status.err == nil {
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "L": // Leader line
				if err := sp.setLeaderLine(ctx, ac, cmd); err != nil {
					status.err = err
				} else {
					status.clear = true
				}
				return

			case "M":
				if cmd == "" {
					ctx.ControlClient.ToggleDisplayModeCAltitude(ac.Callsign, nil, func(err error) { sp.displayError(err, ctx) })
					status.clear = true
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "N":
				// CRDA
				if cmd == "" {
					clickedGhost := ghost != nil && ghostDistance < acDistance
					if clickedGhost {
						state.Ghost.State = GhostStateSuppressed
					} else if slices.ContainsFunc(ghosts, func(g *av.GhostAircraft) bool { return g.Callsign == ac.Callsign }) {
						state.Ghost.State = GhostStateRegular
					} else {
						status.err = ErrSTARSIllegalTrack
					}
				} else if cmd == "*" {
					clickedGhost := ghost != nil && ghostDistance < acDistance
					if clickedGhost {
						// 6-27: display track information in preview area (as an arrival)
						if fp, err := sp.flightPlanSTARS(ctx, ac); err != nil {
							status.err = err
						} else {
							status.output = fp
							status.clear = true
						}
					} else {
						// 6-29: force/unforce ghost qualification
						if !slices.ContainsFunc(ghosts, func(g *av.GhostAircraft) bool { return g.Callsign == ac.Callsign }) {
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
									if lat, _ := region.Inside(state.TrackPosition(), float32(state.TrackAltitude()),
										ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation); lat {
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

			case "O": // Pointout history
				if len(trk.PointOutHistory) == 0 {
					status.output = "PO NONE"
				} else {
					status.output = strings.Join(trk.PointOutHistory, " ")
				}
				status.clear = true
				return

			case "Q":
				if cmd == "" {
					if trk.TrackOwner != ctx.ControlClient.UserTCP && ac.ControllingController != ctx.ControlClient.UserTCP {
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
					if ps.PTLAll || (ps.PTLOwn && trk.TrackOwner == ctx.ControlClient.UserTCP) {
						status.err = ErrSTARSIllegalTrack // 6-13
					} else {
						state.DisplayPTL = !state.DisplayPTL
						status.clear = true
					}
					return
				case "A": // toggle requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						if state.DisplayRequestedAltitude == nil {
							b := ps.DisplayRequestedAltitude // inherit from system-wide
							state.DisplayRequestedAltitude = &b
						}
						*state.DisplayRequestedAltitude = !*state.DisplayRequestedAltitude
						status.clear = true
					}
					return
				case "AE": // enable requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
						status.err = ErrSTARSIllegalFunction
					} else {
						b := true
						state.DisplayRequestedAltitude = &b
						status.clear = true
					}
					return
				case "AI": // inhibit requested altitude: 6-108
					if sp.datablockType(ctx, ac) != FullDatablock {
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
					if trk.TrackOwner != ctx.ControlClient.UserTCP && ac.ControllingController != ctx.ControlClient.UserTCP {
						status.err = ErrSTARSIllegalTrack
					} else {
						state.DisableMSAW = !state.DisableMSAW
						status.clear = true
					}
				} else {
					status.err = ErrSTARSCommandFormat
				}
				return

			case "Y":
				if cmd == "" {
					// Clear pilot reported altitude and scratchpad
					isSecondary := false
					if len(cmd) > 0 && cmd[0] == '+' {
						isSecondary = true
						cmd = cmd[1:]
					}

					sp.setPilotReportedAltitude(ctx, ac.Callsign, 0)
					if err := sp.setScratchpad(ctx, ac.Callsign, "", isSecondary, false); err != nil {
						status.err = err
					} else {
						status.clear = true
					}
					return
				} else {
					// Is it an altitude or a scratchpad update?
					if alt, err := strconv.Atoi(cmd); err == nil && len(cmd) == 3 {
						sp.setPilotReportedAltitude(ctx, ac.Callsign, alt)
						status.clear = true
					} else {
						isSecondary := false
						if len(cmd) > 0 && cmd[0] == '+' {
							isSecondary = true
							cmd = cmd[1:]
						}

						if err := sp.setScratchpad(ctx, ac.Callsign, cmd, isSecondary, false); err != nil {
							status.err = err
						} else {
							status.clear = true
						}
					}
					return
				}
			}

		case CommandModeFlightData:
			if cmd == "" {
				status.clear = true
				status.err = ctx.ControlClient.SetSquawkAutomatic(ac.Callsign)
				return
			} else {
				if squawk, err := av.ParseSquawk(cmd); err == nil {
					status.err = ctx.ControlClient.SetSquawk(ac.Callsign, squawk)
				} else {
					status.err = ErrSTARSIllegalParam
				}
				status.clear = true
				return
			}

		case CommandModeCollisionAlert:
			if cmd == "K" {
				state := sp.Aircraft[ac.Callsign]
				state.DisableCAWarnings = !state.DisableCAWarnings
				state.MCISuppressedCode = av.Squawk(0) // 7-18: this clears the MCI inhibit code
				status.clear = true
				// TODO: check should we set sp.commandMode = CommandMode
				// (applies here and also to others similar...)
				return
			} else if len(cmd) > 0 && cmd[0] == 'M' { // 7-29
				status = sp.updateMCISuppression(ctx, ac, cmd[1:])
				return
			}

		case CommandModeMin:
			if cmd == "" {
				sp.MinSepAircraft[0] = ac.Callsign
				sp.scopeClickHandler = func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
					if ac, _ := sp.tryGetClosestAircraft(ctx, pw, transforms); ac != nil {
						sp.MinSepAircraft[1] = ac.Callsign
						status.clear = true
					} else {
						status.err = ErrSTARSNoFlight
					}
					return
				}
			} else {
				status.err = ErrSTARSCommandFormat
				return
			}

		case CommandModeTargetGen:
			if len(cmd) > 0 {
				sp.runAircraftCommands(ctx, ac, cmd)
				sp.targetGenLastCallsign = ac.Callsign
				status.clear = true
				return
			}
		}
	}

	// No aircraft selected
	if sp.commandMode == CommandModeNone {
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
			sp.scopeClickHandler = rblSecondClickHandler(ctx, sp)
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
		}
	}

	if sp.commandMode == CommandModeMultiFunc {
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
	}

	if sp.commandMode == CommandModeRestrictionArea {
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
	}

	if sp.commandMode == CommandModeDrawRoute {
		mouseLatLong := transforms.LatLongFromWindowP(mousePosition)
		sp.drawRoutePoints = append(sp.drawRoutePoints, mouseLatLong)
		var cb []string
		for _, p := range sp.drawRoutePoints {
			cb = append(cb, strings.ReplaceAll(p.DMSString(), " ", ""))
		}
		ctx.Platform.GetClipboard().SetText(strings.Join(cb, " "))
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
	idx := len(ctx.ControlClient.State.UserRestrictionAreas)
	ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}

	ctx.ControlClient.CreateRestrictionArea(ra, func(idx int) {
		// Just in case (e.g. a race with another controller also adding
		// one), make sure we have the one we made visible.
		ps := sp.currentPrefs()
		ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
	}, func(err error) { sp.displayError(err, ctx) })
}

func (sp *STARSPane) updateRestrictionArea(ctx *panes.Context, idx int, ra av.RestrictionArea) {
	ctx.ControlClient.UpdateRestrictionArea(idx, ra, func(any) {
		ps := sp.currentPrefs()
		if settings, ok := ps.RestrictionAreaSettings[idx]; ok {
			settings.Visible = true
		} else {
			ps.RestrictionAreaSettings[idx] = &RestrictionAreaSettings{Visible: true}
		}
	}, func(err error) { sp.displayError(err, ctx) })
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

func (sp *STARSPane) consumeMouseEvents(ctx *panes.Context, ghosts []*av.GhostAircraft,
	transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	if ctx.Mouse == nil {
		return
	}

	mouse := ctx.Mouse
	ps := sp.currentPrefs()

	if ctx.Mouse.Clicked[platform.MouseButtonPrimary] && !ctx.HaveFocus {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			sp.events.PostEvent(sim.Event{Type: sim.TrackClickedEvent, Callsign: ac.Callsign})
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
					if _, ok := ctx.Keyboard.Pressed[platform.KeyControl]; ok {
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
		if ctx.Keyboard != nil && ctx.Keyboard.WasPressed(platform.KeyShift) && ctx.Keyboard.WasPressed(platform.KeyControl) {
			// Shift-Control-click anywhere -> copy current mouse lat-long to the clipboard.
			mouseLatLong := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			ctx.Platform.GetClipboard().SetText(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))
		}

		if ctx.Keyboard != nil && ctx.Keyboard.WasPressed(platform.KeyControl) && !ctx.Keyboard.WasPressed(platform.KeyShift) { // There is a conflict between this and initating a track CRC-style,
			// so making sure that shift isn't being pressed would be a good idea.
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				if state := sp.Aircraft[ac.Callsign]; state != nil {
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
			status = sp.executeSTARSClickedCommand(ctx, sp.previewAreaInput, ctx.Mouse.Pos, ghosts, transforms)
		}

		if status.err != nil {
			sp.displayError(status.err, ctx)
		} else {
			if status.clear {
				sp.resetInputState(ctx)
			}
			sp.maybeAutoHomeCursor(ctx)
			sp.previewAreaOutput = status.output
		}
	} else if ctx.Mouse.Clicked[platform.MouseButtonTertiary] {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			if state := sp.Aircraft[ac.Callsign]; state != nil {
				state.IsSelected = !state.IsSelected
			}
		}
	} else if !ctx.ControlClient.State.Paused {
		switch sp.currentPrefs().DwellMode {
		case DwellModeOff:
			sp.dwellAircraft = ""

		case DwellModeOn:
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			} else {
				sp.dwellAircraft = ""
			}

		case DwellModeLock:
			if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
				sp.dwellAircraft = ac.Callsign
			}
			// Otherwise leave sp.dwellAircraft as is
		}
	} else {
		if ac, _ := sp.tryGetClosestAircraft(ctx, ctx.Mouse.Pos, transforms); ac != nil {
			td := renderer.GetTextDrawBuilder()
			defer renderer.ReturnTextDrawBuilder(td)

			ps := sp.currentPrefs()
			font := sp.systemFont(ctx, ps.CharSize.Datablocks)
			style := renderer.TextStyle{
				Font:        font,
				Color:       ps.Brightness.FullDatablocks.ScaleRGB(STARSListColor),
				LineSpacing: 0}

			// Aircraft track position in window coordinates
			state := sp.Aircraft[ac.Callsign]
			pac := transforms.WindowFromLatLongP(state.TrackPosition())

			// Upper-left corner of where we start drawing the text
			pad := float32(5)
			ptext := math.Add2f([2]float32{2 * pad, 0}, pac)
			info := ac.NavSummary(ctx.Lg)
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

// amendFlightPlan is a useful utility function for changing an entry in
// the flightplan; the provided callback function should make the update
// and the rest of the details are handled here.
func amendFlightPlan(ctx *panes.Context, callsign string, amend func(fp *av.FlightPlan)) error {
	if ac := ctx.ControlClient.Aircraft[callsign]; ac == nil {
		return av.ErrNoAircraftForCallsign
	} else {
		fp := util.Select(ac.FlightPlan != nil, ac.FlightPlan, &av.FlightPlan{})
		amend(fp)
		return ctx.ControlClient.AmendFlightPlan(callsign, *fp)
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

func (sp *STARSPane) displayError(err error, ctx *panes.Context) {
	if err != nil { // it should be, but...
		sp.playOnce(ctx.Platform, AudioCommandError)
		sp.previewAreaOutput = GetSTARSError(err, ctx.Lg).Error()
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
		if control == nil || control.FacilityIdentifier != "" || control.Id() == ctx.ControlClient.UserTCP {
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

// See STARS Operators Manual 5-184...
func (sp *STARSPane) flightPlanSTARS(ctx *panes.Context, ac *av.Aircraft) (string, error) {
	fp := ac.FlightPlan
	if fp == nil {
		return "", ErrSTARSIllegalFlight
	}

	fmtTime := func(t time.Time) string {
		return t.UTC().Format("1504")
	}

	trk := sp.getTrack(ctx, ac)

	// Common stuff
	owner := trk.TrackOwner
	state := sp.Aircraft[ac.Callsign]

	result := ac.Callsign + " "             // all start with aricraft id
	if ctx.ControlClient.IsOverflight(ac) { // check this first
		result += fp.AircraftType + " "
		result += ac.FlightPlan.AssignedSquawk.String() + " " + owner + "\n"

		// TODO: entry fix
		result += "E" + fmtTime(state.FirstSeen) + " "
		// TODO: exit fix
		result += "R" + fmt.Sprintf("%03d", fp.Altitude/100) + "\n"

		// TODO: [mode S equipage] [target identification] [target address]
	} else if ctx.ControlClient.IsDeparture(ac) {
		if state.FirstRadarTrack.IsZero() {
			// Proposed departure
			result += fp.AircraftType + " "
			result += ac.FlightPlan.AssignedSquawk.String() + " " + owner + "\n"

			if len(fp.DepartureAirport) > 0 {
				result += fp.DepartureAirport[1:] + " "
			}
			result += ac.Scratchpad + " " // should be exit fix--close enough?
			result += "P" + fmtTime(state.FirstSeen) + " "
			result += "R" + fmt.Sprintf("%03d", fp.Altitude/100)
		} else {
			// Active departure
			result += ac.FlightPlan.AssignedSquawk.String() + " "
			if len(fp.DepartureAirport) > 0 {
				result += fp.DepartureAirport[1:] + " "
			}
			result += "D" + fmtTime(state.FirstRadarTrack) + " "
			result += fmt.Sprintf("%03d", int(ac.Altitude())/100) + "\n"

			result += ac.Scratchpad + " "
			result += "R" + fmt.Sprintf("%03d", fp.Altitude/100) + " "

			result += fp.AircraftType

			// TODO: [mode S equipage] [target identification] [target address]
		}
	} else {
		// Format it as an arrival
		result += fp.AircraftType + " "
		result += ac.FlightPlan.AssignedSquawk.String() + " "
		result += owner + " "
		result += fmt.Sprintf("%03d", int(ac.Altitude())/100) + "\n"

		// Use the last item in the route for the entry fix
		routeFields := strings.Fields(fp.Route)
		if n := len(routeFields); n > 0 {
			result += routeFields[n-1] + " "
		}
		result += "A" + fmtTime(state.FirstRadarTrack) + " "
		if len(fp.ArrivalAirport) > 0 {
			result += fp.ArrivalAirport[1:] + " "
		}
		// TODO: [mode S equipage] [target identification] [target address]
	}

	return result, nil
}

// In CRC, whenever a tracked aircraft is slewed, it displays the callsign, squawk, and assigned squawk
func slewAircaft(ac *av.Aircraft) string {
	return fmt.Sprintf("%v %v %v", ac.Callsign, ac.Squawk, ac.FlightPlan.AssignedSquawk)
}

// returns the controller responsible for the aircraft given its altitude
// and route.
func calculateAirspace(ctx *panes.Context, callsign string) (string, error) {
	ac := ctx.ControlClient.Aircraft[callsign]
	if ac == nil {
		return "", ErrSTARSIllegalFlight
	}

	for _, rules := range ctx.ControlClient.STARSFacilityAdaptation.AirspaceAwareness {
		for _, fix := range rules.Fix {
			// Does the fix in the rules match the route?
			if fix != "ALL" && !ac.RouteIncludesFix(fix) {
				continue
			}

			// Does the final altitude satisfy the altitude range, if specified?
			alt := rules.AltitudeRange
			if !(alt[0] == 0 && alt[1] == 0) /* none specified */ &&
				(ac.FlightPlan.Altitude < alt[0] || ac.FlightPlan.Altitude > alt[1]) {
				continue
			}

			// Finally make sure any aircraft type specified in the rules
			// in the matches.
			aircraftType := ac.AircraftPerformance().Engine.AircraftType
			if len(rules.AircraftType) == 0 || slices.Contains(rules.AircraftType, aircraftType) {
				return rules.ReceivingController, nil
			}
		}
	}

	return "", ErrSTARSIllegalPosition
}

func singleScope(ctx *panes.Context, facilityIdentifier string) *av.Controller {
	var controllersInFacility []*av.Controller
	for _, controller := range ctx.ControlClient.Controllers {
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
func (sp *STARSPane) lookupControllerForId(ctx *panes.Context, id, callsign string) *av.Controller {
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
			for _, control := range ctx.ControlClient.Controllers {
				if control.TCP == id[1:] && control.FacilityIdentifier == string(id[0]) {
					return control
				}
			}
		}
	} else if id == "C" {
		// ARTCC airspace-awareness; must have an aircraft callsign
		if callsign == "" {
			return nil
		}

		if controlCallsign, err := calculateAirspace(ctx, callsign); err != nil {
			return nil
		} else if control, ok := ctx.ControlClient.Controllers[controlCallsign]; ok {
			return control
		}
	} else {
		// Non ARTCC airspace-awareness handoffs
		if lc == 1 { // Must be a same sector.
			userController := *ctx.ControlClient.Controllers[ctx.ControlClient.UserTCP]

			for _, control := range ctx.ControlClient.Controllers { // If the controller fac/ sector == userControllers fac/ sector its all good!
				if control.FacilityIdentifier == "" && // Same facility? (Facility ID will be "" if they are the same fac)
					control.TCP[0] == userController.TCP[0] && // Same Sector?
					string(control.TCP[1]) == id { // The actual controller
					return control
				}
			}
		} else if lc == 2 {
			// Must be a same sector || same facility.
			for _, control := range ctx.ControlClient.Controllers {
				if control.TCP == id && control.FacilityIdentifier == "" {
					return control
				}
			}
		}

		for _, control := range ctx.ControlClient.Controllers {
			if control.ERAMFacility && control.TCP == id {
				return control
			}
		}
	}
	return nil
}

func (sp *STARSPane) tryGetClosestAircraft(ctx *panes.Context, mousePosition [2]float32, transforms ScopeTransformations) (*av.Aircraft, float32) {
	var ac *av.Aircraft
	distance := float32(20) // in pixels; don't consider anything farther away

	for _, a := range sp.visibleAircraft(ctx) {
		pw := transforms.WindowFromLatLongP(sp.Aircraft[a.Callsign].TrackPosition())
		dist := math.Distance2f(pw, mousePosition)
		if dist < distance {
			ac = a
			distance = dist
		}
	}

	return ac, distance
}

func (sp *STARSPane) tryGetClosestGhost(ghosts []*av.GhostAircraft, mousePosition [2]float32, transforms ScopeTransformations) (*av.GhostAircraft, float32) {
	var ghost *av.GhostAircraft
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
