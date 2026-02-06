// sim/cmdsetup.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Commands defined in chapter 4 of the TCW Operator Manual
package stars

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/util"
)

func init() {
	// 4.1.3 Apply preference set
	registerCommand(CommandModePref, "[NUM]", func(sp *STARSPane, ctx *panes.Context, idx int) error {
		if idx <= 0 || idx > numSavedPreferenceSets {
			return ErrSTARSCommandFormat
		}

		idx-- // Convert to 0-based
		if sp.prefSet.Saved[idx] == nil {
			return ErrSTARSCommandFormat
		}

		sp.prefSet.Selected = &idx
		sp.prefSet.SetCurrent(*sp.prefSet.Saved[idx], ctx.Platform, sp)
		sp.setCommandMode(ctx, CommandModeNone)
		return nil
	})
	registerCommand(CommandModePref, "[ALL_TEXT]", func(sp *STARSPane, ctx *panes.Context, name string) (CommandStatus, error) {
		idx := slices.IndexFunc(sp.prefSet.Saved[:], func(p *Preferences) bool { return p != nil && p.Name == name })
		if idx == -1 {
			return CommandStatus{}, ErrSTARSIllegalPrefset
		}

		sp.prefSet.Selected = &idx
		sp.prefSet.SetCurrent(*sp.prefSet.Saved[idx], ctx.Platform, sp)
		sp.setCommandMode(ctx, CommandModeNone)
		return CommandStatus{}, nil
	})

	// 4.1.4 Create new preference set [sic]
	registerCommand(CommandModeSavePrefAs, "", func(sp *STARSPane, ctx *panes.Context) CommandStatus {
		return CommandStatus{Clear: ClearNone}
	})
	registerCommand(CommandModeSavePrefAs, "[ALL_TEXT]", savePreferences)

	// 4.1.10 Reconfigure TCW/TDW to default display characteristics (p. 4-21)
	registerCommand(CommandModeMultiFunc, "K", func(sp *STARSPane, ctx *panes.Context) {
		sp.prefSet.ResetDefault(ctx.Client.State, ctx.Platform, sp)
	})

	// 4.2.1 Enable Single sensor or Multi-sensor or Fused mode
	registerCommand(CommandModeSite, "+", func(sp *STARSPane) {
		sp.setRadarModeFused()
	})
	registerCommand(CommandModeSite, "[NUM]", func(ctx *panes.Context, ps *Preferences, idx int) error {
		radarSites := ctx.FacilityAdaptation.RadarSites
		idx-- // Convert to 0-based
		if idx < 0 || idx >= len(radarSites) {
			return ErrSTARSRangeLimit
		}

		ps.RadarSiteSelected = util.SortedMapKeys(radarSites)[idx]
		return nil
	})
	registerCommand(CommandModeSite, "[FIELD:1]", func(ctx *panes.Context, ps *Preferences, char string) error {
		if id, _, ok := util.MapLookupFunc(ctx.FacilityAdaptation.RadarSites,
			func(id string, site *av.RadarSite) bool { return site.Char == char }); ok {
			ps.RadarSiteSelected = id
			return nil
		}

		return ErrSTARSIllegalParam
	})
	registerCommand(CommandModeSite, STARSTriangleCharacter, func(sp *STARSPane) {
		sp.setRadarModeMulti()
	})

	// 4.2.2 Enable / inhibit parallel approach monitor (PAM) display view
	// registerCommand(CommandModeMultiFunc, "6PA E" / "6PA I")

	// 4.2.6 Enable / inhibit track position symbols for unassociated primary tracks (p. 4-29)
	registerCommand(CommandModeMultiFunc, "2PE", func(ps *Preferences) {
		ps.InhibitPositionSymOnUnassociatedPrimary = false
	})
	registerCommand(CommandModeMultiFunc, "2PI", func(ps *Preferences) {
		ps.InhibitPositionSymOnUnassociatedPrimary = true
	})

	// 4.3 Enable / inhibit automatic handoff processing for entering TCP (p. 4-30)
	registerCommand(CommandModeHandOff, "CE", func(ps *Preferences) {
		ps.AutomaticHandoffs.Intrafacility = true
		ps.AutomaticHandoffs.Interfacility = true
	})
	registerCommand(CommandModeHandOff, "CI", func(ps *Preferences) {
		ps.AutomaticHandoffs.Intrafacility = false
		ps.AutomaticHandoffs.Interfacility = false
	})
	registerCommand(CommandModeHandOff, "CTE", func(ps *Preferences) {
		ps.AutomaticHandoffs.Intrafacility = true
	})
	registerCommand(CommandModeHandOff, "CTI", func(ps *Preferences) {
		ps.AutomaticHandoffs.Intrafacility = false
	})
	registerCommand(CommandModeHandOff, "CXE", func(ps *Preferences) {
		ps.AutomaticHandoffs.Interfacility = true
	})
	registerCommand(CommandModeHandOff, "CXI", func(ps *Preferences) {
		ps.AutomaticHandoffs.Interfacility = false
	})

	// 4.5.1 Display / remove maps
	registerCommand(CommandModeMaps, "A", func(ps *Preferences) { clear(ps.VideoMapVisible) })
	registerCommand(CommandModeMaps, "[NUM]", func(sp *STARSPane, ps *Preferences, idx int) error {
		if idx <= 0 {
			return ErrSTARSIllegalMap
		}
		if !slices.ContainsFunc(sp.allVideoMaps, func(v radar.ClientVideoMap) bool { return v.Id == idx }) {
			return ErrSTARSIllegalMap
		}

		_, visible := ps.VideoMapVisible[idx]
		if visible {
			delete(ps.VideoMapVisible, idx)
		} else {
			ps.VideoMapVisible[idx] = nil
		}
		return nil
	})
	registerCommand(CommandModeMaps, "[NUM]E", func(sp *STARSPane, ps *Preferences, idx int) error {
		if idx <= 0 {
			return ErrSTARSIllegalMap
		}
		if !slices.ContainsFunc(sp.allVideoMaps, func(v radar.ClientVideoMap) bool { return v.Id == idx }) {
			return ErrSTARSIllegalMap
		}
		ps.VideoMapVisible[idx] = nil
		return nil
	})
	registerCommand(CommandModeMaps, "[NUM]I", func(sp *STARSPane, ps *Preferences, idx int) error {
		if idx <= 0 {
			return ErrSTARSIllegalMap
		}
		if !slices.ContainsFunc(sp.allVideoMaps, func(v radar.ClientVideoMap) bool { return v.Id == idx }) {
			return ErrSTARSIllegalMap
		}
		delete(ps.VideoMapVisible, idx)
		return nil
	})

	// 4.5.3 Move map category list
	registerCommand(CommandModeMultiFunc, "TX[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.VideoMapsList.Position = pos
		ps.VideoMapsList.Visible = true
	})

	// 4.5.4 Hide / show map category list
	registerCommand(CommandModeMultiFunc, "TX", func(ps *Preferences) {
		ps.VideoMapsList.Visible = !ps.VideoMapsList.Visible
	})

	// 4.6 Display / remove weather
	registerCommand(CommandModeWX, "[#]", func(ps *Preferences, level int) error {
		if level < 1 || level > 6 {
			return ErrSTARSRangeLimit
		}
		ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
		ps.DisplayWeatherLevel[level-1] = !ps.DisplayWeatherLevel[level-1]
		return nil
	})
	registerCommand(CommandModeWX, "[#]E", func(ps *Preferences, level int) error {
		if level < 1 || level > 6 {
			return ErrSTARSRangeLimit
		}
		ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
		ps.DisplayWeatherLevel[level-1] = true
		return nil
	})
	registerCommand(CommandModeWX, "[#]I", func(ps *Preferences, level int) error {
		if level < 1 || level > 6 {
			return ErrSTARSRangeLimit
		}
		ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
		ps.DisplayWeatherLevel[level-1] = false
		return nil
	})
	registerCommand(CommandModeWX, "A", func(ps *Preferences) {
		ps.DisplayWeatherLevel, ps.LastDisplayWeatherLevel = ps.LastDisplayWeatherLevel, ps.DisplayWeatherLevel
	})
	registerCommand(CommandModeWX, "C", func(ps *Preferences) {
		ps.LastDisplayWeatherLevel = ps.DisplayWeatherLevel
		clear(ps.DisplayWeatherLevel[:])
	})

	// 4.9.2 Clear Preview areas and return cursors to home position (p. 4-50)
	registerCommand(CommandModeMultiFunc, "I*", func() {})

	// 4.9.3 Enable / inhibit automatic readout area clear
	//{(CommandModeMultiFunc, "PTOE")
	//{(CommandModeMultiFunc, "PTOI")

	// 4.9.4 Move System status area (p. 4-52)
	registerCommand(CommandModeMultiFunc, "S[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.SSAList.Position = pos
	})

	// 4.9.5 Move Preview area (p. 4-53)
	registerCommand(CommandModeMultiFunc, "P[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.PreviewAreaPosition = pos
	})

	// 4.9.6 Move and show aircraft list (p. 4-54)
	registerCommand(CommandModeMultiFunc, "T[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.TABList.Position = pos
		ps.TABList.Visible = true
	})
	registerCommand(CommandModeMultiFunc, "TC[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.CoastList.Position = pos
		ps.CoastList.Visible = true
	})
	registerCommand(CommandModeMultiFunc, "TM[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.AlertList.Position = pos
		ps.AlertList.Visible = true
	})
	registerCommand(CommandModeMultiFunc, "TV[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.VFRList.Position = pos
		ps.VFRList.Visible = true
	})

	// 4.9.7 Move Tower list (p. 4-55)
	registerCommand(CommandModeMultiFunc, "P[#][POS_NORM]", func(ps *Preferences, idx int, pos [2]float32) error {
		if idx < 1 || idx > 3 {
			return ErrSTARSIllegalValue
		}
		ps.TowerLists[idx-1].Position = pos
		ps.TowerLists[idx-1].Visible = true
		return nil
	})

	// 4.9.8 Move Coordination list (p. 4-56)
	registerCommand(CommandModeMultiFunc, "P[FIELD][POS_NORM]", func(ps *Preferences, listID string, pos [2]float32) error {
		list, ok := ps.CoordinationLists[listID]
		if !ok {
			return ErrSTARSIllegalFunction
		}
		list.Position = pos
		return nil
	})

	// 4.9.9 Move Sign-on list (p. 4-57)
	registerCommand(CommandModeMultiFunc, "TS[POS_NORM]", func(ps *Preferences, pos [2]float32) {
		ps.SignOnList.Position = pos
		ps.SignOnList.Visible = true
	})

	// 4.9.10 Show or move CA suppression zone list
	// registerCommand(CommandModeCollisionAlert, "QQ[POS_NORM]", ...)

	// 4.9.11 Hide / show VFR or flight plan list
	registerCommand(CommandModeMultiFunc, "T", func(ps *Preferences) {
		ps.TABList.Visible = !ps.TABList.Visible
	})
	registerCommand(CommandModeMultiFunc, "TV", func(ps *Preferences) {
		ps.VFRList.Visible = !ps.VFRList.Visible
	})
	registerCommand(CommandModeMultiFunc, "TC", func(ps *Preferences) {
		ps.CoastList.Visible = !ps.CoastList.Visible
	})

	// 4.9.12 Hide / show Tower list (p. 4-60)
	registerCommand(CommandModeMultiFunc, "P[#]", func(ps *Preferences, idx int) error {
		if idx < 1 || idx > 3 {
			return ErrSTARSIllegalValue
		}
		ps.TowerLists[idx-1].Visible = !ps.TowerLists[idx-1].Visible
		return nil
	})

	// 4.9.13 Hide / show Sign-on list (p. 4-61)
	registerCommand(CommandModeMultiFunc, "TS", func(ps *Preferences) {
		ps.SignOnList.Visible = !ps.SignOnList.Visible
	})

	// 4.9.14 Hide / show CA suppression zone list
	// registerCommand(CommandModeCollisionAlert, C: "QQ"})
	// registerCommand(CommandModeCollisionAlert, C: "QQ[TEXT]"})

	// 4.9.15 Change size of Flight plan, Coast/Suspend, or VFR list (p. 4-64)
	// Note that 5.4.9 Delete a TCP's passive and pending flight plans has "T[TIME]", so limit to 1-2 digits in parse
	registerCommand(CommandModeMultiFunc, "T[#]|T[##]", func(ps *Preferences, n int) error {
		if n < 1 || n > 100 {
			return ErrSTARSIllegalParam
		}
		ps.TABList.Lines = n
		ps.TABList.Visible = true
		return nil
	})
	registerCommand(CommandModeMultiFunc, "TC[NUM]", func(ps *Preferences, n int) error {
		if n < 1 || n > 100 {
			return ErrSTARSIllegalParam
		}
		ps.CoastList.Lines = n
		ps.CoastList.Visible = true
		return nil
	})
	registerCommand(CommandModeMultiFunc, "TV[NUM]", func(ps *Preferences, n int) error {
		if n < 1 || n > 100 {
			return ErrSTARSIllegalParam
		}
		ps.VFRList.Lines = n
		ps.VFRList.Visible = true
		return nil
	})

	// 4.9.16 Change size of Tower list (p. 4-65)
	registerCommand(CommandModeMultiFunc, "P[#] [NUM]", func(ps *Preferences, idx int, n int) error {
		if idx < 1 || idx > 3 {
			return ErrSTARSIllegalValue
		}
		if n < 1 || n > 100 {
			return ErrSTARSIllegalParam
		}
		ps.TowerLists[idx-1].Lines = n
		ps.TowerLists[idx-1].Visible = true
		return nil
	})

	// 4.9.17 Change size of Coordination list (p. 4-66)
	registerCommand(CommandModeMultiFunc, "P[FIELD] [NUM]", func(ps *Preferences, listID string, n int) error {
		list, ok := ps.CoordinationLists[listID]
		if !ok {
			return ErrSTARSIllegalFunction
		}
		list.Lines = n
		return nil
	})

	// 4.9.18 Move altimeter station list
	// {(CommandModeMultiFunc, "TL[POS_NORM]")

	// 4.9.19 Hide/Show altimeter station list
	// {(CommandModeMultiFunc, "TL")

	// 4.9.20 Move satellite arrival list
	// {(CommandModeMultiFunc, "TA[POS_NORM]")

	// 4.9.21 Hide/Show altimeter satellite arrival list
	// {(CommandModeMultiFunc, "TA")

	// 4.9.22/23 TSAS status list...

	// 4.9.24 Move code/callsign list
	// {(CommandModeMultiFunc, "TB[POS_NORM]")

	// 4.9.??? Hide/Show code/callsign list
	// {(CommandModeMultiFunc, "TB")

	// 4.9.??? Change size of code/callsign list
	// {(CommandModeMultiFunc, "TB [NUM]")

	// 4.9.27 Move any on-screen data-area or data list
	// basically click list bbox and drag

	// 4.9.28 Show all data areas and lists
	// (middle button)

	// 4.11.1 Display altitude filter limits (p. 4-85)
	registerCommand(CommandModeMultiFunc, "F", func(ps *Preferences) CommandStatus {
		af := &ps.AltitudeFilters
		return CommandStatus{Output: fmt.Sprintf("%03d %03d\n%03d %03d",
			af.Unassociated[0]/100, af.Unassociated[1]/100,
			af.Associated[0]/100, af.Associated[1]/100)}
	})

	// 4.11.2 Modify altitude filter limits for unassociated / associated tracks (p. 4-86)
	registerCommand(CommandModeMultiFunc, "F[ALT_FILTER_6] [ALT_FILTER_6]", func(ps *Preferences, unassocFilter [2]int, assocFilter [2]int) {
		ps.AltitudeFilters.Unassociated[0] = min(unassocFilter[0], unassocFilter[1])
		ps.AltitudeFilters.Unassociated[1] = max(unassocFilter[0], unassocFilter[1])
		ps.AltitudeFilters.Associated[0] = min(assocFilter[0], assocFilter[1])
		ps.AltitudeFilters.Associated[1] = max(assocFilter[0], assocFilter[1])
	})

	// 4.11.3 Modify altitude filter limits for associated tracks (p. 4-87)
	registerCommand(CommandModeMultiFunc, "FC[ALT_FILTER_6]", func(ps *Preferences, filter [2]int) {
		ps.AltitudeFilters.Associated[0] = min(filter[0], filter[1])
		ps.AltitudeFilters.Associated[1] = max(filter[0], filter[1])
	})

	// 4.11.4 Enable / inhibit altitude filter override volume for associated tracks
	// {(CommandModeMultiFunc,  "FC[FIELD] E")
	// {(CommandModeMultiFunc,  "FC[FIELD] I")

	// 4.11.5 Enable / inhibit altitude filter override volume for unassociated tracks
	// {(CommandModeMultiFunc,  "F [FIELD] E")
	// {(CommandModeMultiFunc,  "F [FIELD] I")

	// 4.12.1 Display active arrival hold areas
	// {(CommandModeMultiFunc,  "AH")

	// 4.12.2 Enable/inhibit arrival hold area
	// {(CommandModeMultiFunc,  "A[FIELD]")
	// {(CommandModeMultiFunc,  "A[FIELD] E|A[FIELD] H")
	// {(CommandModeMultiFunc,  "A[FIELD] I")

	// 4.12.13 Modify arrival hold area altitudes
	// {(CommandModeMultiFunc,  "A[TEXT] [NUM:3]")
	// {(CommandModeMultiFunc,  "A[TEXT] [ALT_FILTER_6]")

	// 4.13.4 Enable / inhibit audible notification for departure release (p. 4-99)
	//(CommandModeMultiFunc, "ZDE", unimplementedCommand),
	//(CommandModeMultiFunc, "ZDI", unimplementedCommand),

	// 4.13.1 Test audio alarm (p. 4-96)
	registerCommand(CommandModeMultiFunc, "ZA", func(sp *STARSPane, ctx *panes.Context) {
		sp.testAudioEndTime = ctx.Now.Add(5 * time.Second)
		ctx.Platform.StartPlayAudioContinuous(sp.audioEffects[AudioTest])
	})

	// 4.13.3 Enable / inhibit audible notification for TCP command errors (p. 4-98)
	registerCommand(CommandModeMultiFunc, "ZEE", func(ps *Preferences) {
		ps.AudioEffectEnabled[AudioCommandError] = true
	})
	registerCommand(CommandModeMultiFunc, "ZEI", func(ps *Preferences) {
		ps.AudioEffectEnabled[AudioCommandError] = false
	})

	// 4.13.4 Enable / inhibit audible notification for departure release
	// registerCommand(CommandModeMultiFunc, "ZDE", ...)
	// registerCommand(CommandModeMultiFunc, "ZDI", ...)

	// 4.14.3 Change leader line length: handled in DCB code.

	// 4.14.4 Enable / disable Auto offset (p. 4-104)
	registerCommand(CommandModeMultiFunc, "OE", func(ps *Preferences) {
		ps.AutomaticFDBOffset = true
	})

	// 4.14.4 Enable / disable Auto offset (p. 4-104)
	registerCommand(CommandModeMultiFunc, "OI", func(ps *Preferences) {
		ps.AutomaticFDBOffset = false
	})
	registerCommand(CommandModeMultiFunc, "O", func(ps *Preferences) {
		ps.AutomaticFDBOffset = !ps.AutomaticFDBOffset
	})

	// 4.14.5 Specify data block position for tracks owned at this TCW/TDW (p. 4-105)
	registerCommand(CommandModeMultiFunc, "L[#]", func(sp *STARSPane, ps *Preferences, direction int) error {
		dir, ok := sp.numpadToDirection(direction)
		if !ok || dir == nil {
			return ErrSTARSIllegalParam
		}
		ps.LeaderLineDirection = *dir
		return nil
	})
	// CommandModeLDR version handled in DCB code.

	// 4.14.6 Specify data block position for tracks owned by others (p. 4-106)
	registerCommand(CommandModeMultiFunc, "L[#]*", func(sp *STARSPane, ps *Preferences, direction int) error {
		dir, ok := sp.numpadToDirection(direction)
		if !ok {
			return ErrSTARSIllegalParam
		}
		ps.OtherControllerLeaderLineDirection = dir
		clear(ps.ControllerLeaderLineDirections)
		return nil
	})

	// 4.14.7 Specify data block position for a specified owner (p. 4-107)
	registerCommand(CommandModeMultiFunc, "L[TCP2][#]|L[TCP1] [#]",
		func(sp *STARSPane, ctx *panes.Context, ps *Preferences, tcp string, direction int) error {
			ctrl := lookupControllerByTCP(ctx.Client.State.Controllers, tcp, ctx.UserController().SectorID)
			if ctrl == nil {
				return ErrSTARSIllegalPosition
			}

			dir, ok := sp.numpadToDirection(direction)
			if !ok {
				return ErrSTARSCommandFormat
			}

			if ps.ControllerLeaderLineDirections == nil {
				ps.ControllerLeaderLineDirections = make(map[av.ControlPosition]math.CardinalOrdinalDirection)
			}
			if dir != nil {
				ps.ControllerLeaderLineDirections[ctrl.PositionId()] = *dir
			} else {
				// Direction 5 (center) clears the setting
				delete(ps.ControllerLeaderLineDirections, ctrl.PositionId())
			}
			return nil
		})

	// 4.14.8 Specify data block position for all unassociated tracks (p. 4-109)
	registerCommand(CommandModeMultiFunc, "L[#]U", func(sp *STARSPane, ps *Preferences, direction int) error {
		dir, ok := sp.numpadToDirection(direction)
		if !ok || dir == nil /* 5 is invalid for this */ {
			return ErrSTARSCommandFormat
		}
		ps.UnassociatedLeaderLineDirection = dir
		return nil
	})

	// 4.14.9 Enable / inhibit display of Full data blocks for all overflights (p. 4-110)
	registerCommand(CommandModeMultiFunc, "E", func(ps *Preferences) {
		ps.OverflightFullDatablocks = !ps.OverflightFullDatablocks
	})
	registerCommand(CommandModeMultiFunc, "EE", func(ps *Preferences) {
		ps.OverflightFullDatablocks = true
	})
	registerCommand(CommandModeMultiFunc, "EI", func(ps *Preferences) {
		ps.OverflightFullDatablocks = false
	})

	// 4.14.10 Enable / inhibit ADS-B indicators
	// {(CommandModeMultiFunc,  "ADS*")
	// {(CommandModeMultiFunc,  "ADS*E")
	// {(CommandModeMultiFunc,  "ADS*I")

	// 4.15.1 Re-define cursor home position and enable auto-home (p. 4-113)
	registerCommand(CommandModeMultiFunc, "INC[POS_RAW]", func(ps *Preferences, pos [2]float32) CommandStatus {
		ps.AutoCursorHome = true
		ps.CursorHome = pos
		return CommandStatus{Output: "HOME"}
	})

	// 4.15.2 Enable auto-home cursor positioning (p. 4-114)
	registerCommand(CommandModeMultiFunc, "IHS", func(ps *Preferences) CommandStatus {
		ps.AutoCursorHome = true
		return CommandStatus{Output: "HOME"}
	})

	// 4.15.3 Disable auto-home cursor positioning (p. 4-115)
	registerCommand(CommandModeMultiFunc, "INH", func(ps *Preferences) CommandStatus {
		ps.AutoCursorHome = false
		return CommandStatus{Output: "NO HOME"}
	})

	// 4.15.5 Dwell mode selection (p. 4-117)
	registerCommand(CommandModeMultiFunc, "DE", func(ps *Preferences) {
		ps.DwellMode = DwellModeOn
	})
	registerCommand(CommandModeMultiFunc, "DI", func(ps *Preferences) {
		ps.DwellMode = DwellModeOff
	})
	registerCommand(CommandModeMultiFunc, "DL", func(ps *Preferences) {
		ps.DwellMode = DwellModeLock
	})

	// 4.18 Enable/inhibit traffic flow regions for automatic runway assignment
	// registerCommand(CommandModeRestrictionArea, "[FIELD:1]E", unimplemented)
	// registerCommand(CommandModeRestrictionArea, "[FIELD:1]I", unimplemented)

	// 4.19 Enable / inhibit Flight data auto-modify (FDAM) region (p. 4-123)
	//(CommandModeMultiFunc, "2X[TEXT]", unimplementedCommand),
	//(CommandModeMultiFunc, "2X[TEXT] E", unimplementedCommand),
	//(CommandModeMultiFunc, "2X[TEXT] I", unimplementedCommand),

	// 4.20 Display status of all Flight data auto-modify (FDAM) regions (p. 4-125)
	//(CommandModeMultiFunc, "2XS", unimplementedCommand),
}

// savePreferences saves current preferences with the given name.
func savePreferences(sp *STARSPane, ctx *panes.Context, name string) error {
	if len(name) > 7 {
		return ErrSTARSCommandFormat
	}

	if slices.ContainsFunc(sp.prefSet.Saved[:],
		func(p *Preferences) bool { return p != nil && p.Name == name }) {
		// Can't repeat pref set names
		return ErrSTARSIllegalPrefset
	}

	if v, err := strconv.Atoi(name); err == nil && v >= 1 && v <= numSavedPreferenceSets {
		// Can't give it a numeric name that conflicts with pref set #s
		return ErrSTARSIllegalPrefset
	}

	// Find the first empty slot
	idx := slices.Index(sp.prefSet.Saved[:], nil)
	if idx == -1 {
		// This shouldn't happen since SAVE AS should be disabled if there are no free slots
		idx = len(sp.prefSet.Saved) - 1
	}

	p := sp.prefSet.Current.Duplicate()
	p.Name = name
	sp.prefSet.Selected = &idx
	sp.prefSet.Saved[idx] = p
	return nil
}
