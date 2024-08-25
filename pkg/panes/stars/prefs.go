// pkg/panes/stars/prefs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type PreferenceSet struct {
	Name string

	DisplayDCB  bool
	DCBPosition int

	Center math.Point2LL
	Range  float32

	CurrentCenter math.Point2LL
	OffCenter     bool

	RangeRingsCenter math.Point2LL
	RangeRingRadius  int

	// TODO? cursor speed

	CurrentATIS string
	GIText      [9]string

	AudioVolume int // 1-10

	RadarTrackHistory int
	// 4-94: 0.5s increments via trackball but 0.1s increments allowed if
	// keyboard input.
	RadarTrackHistoryRate float32

	DisplayWeatherLevel     [6]bool
	LastDisplayWeatherLevel [6]bool

	// If empty, then then MULTI or FUSED mode, depending on
	// FusedRadarMode.  The custom JSON name is so we don't get errors
	// parsing old configs, which stored this as an array...
	RadarSiteSelected string `json:"RadarSiteSelectedName"`
	FusedRadarMode    bool

	AudioEffectEnabled []bool

	// For tracked by the user
	LeaderLineDirection math.CardinalOrdinalDirection
	LeaderLineLength    int // 0-7
	// For tracked by other controllers
	ControllerLeaderLineDirections map[string]math.CardinalOrdinalDirection
	// If not specified in ControllerLeaderLineDirections...
	OtherControllerLeaderLineDirection *math.CardinalOrdinalDirection
	// Only set if specified by the user (and not used currently...)
	UnassociatedLeaderLineDirection *math.CardinalOrdinalDirection

	AltitudeFilters struct {
		Unassociated [2]int // low, high
		Associated   [2]int
	}

	QuickLookAll       bool
	QuickLookAllIsPlus bool
	QuickLookPositions []QuickLookPosition

	CRDA struct {
		Disabled bool
		// Has the same size and indexing as corresponding STARSPane
		// STARSConvergingRunways
		RunwayPairState []CRDARunwayPairState
		ForceAllGhosts  bool
	}

	DisplayLDBBeaconCodes bool // TODO: default?
	SelectedBeaconCodes   []string

	// TODO: review--should some of the below not be in prefs but be in STARSPane?

	// DisplayUncorrelatedTargets bool // NOT USED

	DisableCAWarnings bool
	DisableMSAW       bool

	OverflightFullDatablocks bool
	AutomaticFDBOffset       bool

	DisplayTPASize               bool
	DisplayATPAInTrailDist       bool `json:"DisplayATPAIntrailDist"`
	DisplayATPAWarningAlertCones bool
	DisplayATPAMonitorCones      bool

	VideoMapVisible map[int]interface{}

	PTLLength      float32
	PTLOwn, PTLAll bool

	DisplayRequestedAltitude bool

	DwellMode DwellMode

	Bookmarks [10]struct {
		Center math.Point2LL
		Range  float32
	}

	Brightness struct {
		DCB                STARSBrightness
		BackgroundContrast STARSBrightness
		VideoGroupA        STARSBrightness
		VideoGroupB        STARSBrightness
		FullDatablocks     STARSBrightness
		Lists              STARSBrightness
		Positions          STARSBrightness
		LimitedDatablocks  STARSBrightness
		OtherTracks        STARSBrightness
		Lines              STARSBrightness
		RangeRings         STARSBrightness
		Compass            STARSBrightness
		BeaconSymbols      STARSBrightness
		PrimarySymbols     STARSBrightness
		History            STARSBrightness
		Weather            STARSBrightness
		WxContrast         STARSBrightness
	}

	CharSize struct {
		DCB             int
		Datablocks      int
		Lists           int
		Tools           int
		PositionSymbols int
	}

	PreviewAreaPosition [2]float32

	SSAList struct {
		Position [2]float32
		Visible  bool
		Filter   struct {
			All                 bool
			Wx                  bool
			Time                bool
			Altimeter           bool
			Status              bool
			Radar               bool
			Codes               bool
			SpecialPurposeCodes bool
			Range               bool
			PredictedTrackLines bool
			AltitudeFilters     bool
			AirportWeather      bool
			QuickLookPositions  bool
			DisabledTerminal    bool
			ActiveCRDAPairs     bool
			WxHistory           bool

			Text struct {
				Main bool
				GI   [9]bool
			}
		}
	}
	VFRList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	TABList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	AlertList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	CoastList struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
	SignOnList struct {
		Position [2]float32
		Visible  bool
	}
	VideoMapsList struct {
		Position  [2]float32
		Visible   bool
		Selection VideoMapsGroup
	}
	CRDAStatusList struct {
		Position [2]float32
		Visible  bool
	}
	TowerLists [3]struct {
		Position [2]float32
		Visible  bool
		Lines    int
	}
}

func (ps *PreferenceSet) ResetCRDAState(rwys []STARSConvergingRunways) {
	ps.CRDA.RunwayPairState = nil
	state := CRDARunwayPairState{}
	// The first runway is enabled by default
	state.RunwayState[0].Enabled = true
	for range rwys {
		ps.CRDA.RunwayPairState = append(ps.CRDA.RunwayPairState, state)
	}
}

func (sp *STARSPane) MakePreferenceSet(name string, ss *sim.State) PreferenceSet {
	var ps PreferenceSet

	ps.Name = name

	ps.DisplayDCB = true
	ps.DCBPosition = dcbPositionTop

	if ss != nil {
		ps.Center = ss.GetInitialCenter()
		ps.Range = ss.GetInitialRange()
	} else {
		ps.Center = math.Point2LL{73.475, 40.395} // JFK-ish
		ps.Range = 50
	}

	ps.CurrentCenter = ps.Center

	ps.RangeRingsCenter = ps.Center
	ps.RangeRingRadius = 5

	ps.RadarTrackHistory = 5
	ps.RadarTrackHistoryRate = 4.5

	ps.AudioVolume = 10
	ps.AudioEffectEnabled = make([]bool, AudioNumTypes)
	for i := range AudioNumTypes {
		ps.AudioEffectEnabled[i] = true
	}

	ps.VideoMapVisible = make(map[int]interface{})

	ps.FusedRadarMode = true
	ps.LeaderLineDirection = math.North
	ps.LeaderLineLength = 1

	ps.AltitudeFilters.Unassociated = [2]int{100, 60000}
	ps.AltitudeFilters.Associated = [2]int{100, 60000}

	//ps.DisplayUncorrelatedTargets = true

	ps.DisplayTPASize = true
	ps.DisplayATPAWarningAlertCones = true

	ps.PTLLength = 1

	ps.Brightness.DCB = 60
	ps.Brightness.BackgroundContrast = 0
	ps.Brightness.VideoGroupA = 50
	ps.Brightness.VideoGroupB = 40
	ps.Brightness.FullDatablocks = 80
	ps.Brightness.Lists = 80
	ps.Brightness.Positions = 80
	ps.Brightness.LimitedDatablocks = 80
	ps.Brightness.OtherTracks = 80
	ps.Brightness.Lines = 40
	ps.Brightness.RangeRings = 20
	ps.Brightness.Compass = 40
	ps.Brightness.BeaconSymbols = 55
	ps.Brightness.PrimarySymbols = 80
	ps.Brightness.History = 60
	ps.Brightness.Weather = 30
	ps.Brightness.WxContrast = 30

	for i := range ps.DisplayWeatherLevel {
		ps.DisplayWeatherLevel[i] = true
	}

	ps.CharSize.DCB = 1
	ps.CharSize.Datablocks = 1
	ps.CharSize.Lists = 1
	ps.CharSize.Tools = 1
	ps.CharSize.PositionSymbols = 0

	ps.PreviewAreaPosition = [2]float32{.05, .75}

	ps.SSAList.Position = [2]float32{.05, .9}
	ps.SSAList.Visible = true
	ps.SSAList.Filter.All = true

	ps.TABList.Position = [2]float32{.05, .65}
	ps.TABList.Lines = 5
	ps.TABList.Visible = true

	ps.VFRList.Position = [2]float32{.05, .2}
	ps.VFRList.Lines = 5
	ps.VFRList.Visible = true

	ps.AlertList.Position = [2]float32{.8, .25}
	ps.AlertList.Lines = 5
	ps.AlertList.Visible = true

	ps.CoastList.Position = [2]float32{.8, .65}
	ps.CoastList.Lines = 5
	ps.CoastList.Visible = false

	ps.SignOnList.Position = [2]float32{.8, .9}
	ps.SignOnList.Visible = true

	ps.VideoMapsList.Position = [2]float32{.85, .5}
	ps.VideoMapsList.Visible = false

	ps.CRDAStatusList.Position = [2]float32{.05, .7}

	ps.TowerLists[0].Position = [2]float32{.05, .5}
	ps.TowerLists[0].Lines = 5
	ps.TowerLists[0].Visible = true

	ps.TowerLists[1].Position = [2]float32{.05, .8}
	ps.TowerLists[1].Lines = 5

	ps.TowerLists[2].Position = [2]float32{.05, .9}
	ps.TowerLists[2].Lines = 5

	ps.ResetCRDAState(sp.ConvergingRunways)

	return ps
}

func (ps *PreferenceSet) Duplicate() PreferenceSet {
	dupe := *ps
	dupe.SelectedBeaconCodes = util.DuplicateSlice(ps.SelectedBeaconCodes)
	dupe.CRDA.RunwayPairState = util.DuplicateSlice(ps.CRDA.RunwayPairState)
	dupe.VideoMapVisible = util.DuplicateMap(ps.VideoMapVisible)
	return dupe
}

func (ps *PreferenceSet) Activate(p platform.Platform, sp *STARSPane) {
	// It should only take integer values but it's a float32 and we
	// previously didn't enforce this...
	ps.Range = float32(int(ps.Range))

	if ps.PTLAll { // both can't be set; we didn't enforce this previously...
		ps.PTLOwn = false
	}

	if ps.RadarTrackHistoryRate == 0 {
		ps.RadarTrackHistoryRate = 4.5 // upgrade from old
	}

	p.SetAudioVolume(ps.AudioVolume)

	// Brightness goes in steps of 5 (similarly not enforced previously...)
	remapBrightness := func(b *STARSBrightness) {
		*b = (*b + 2) / 5 * 5
		*b = math.Clamp(*b, 0, 100)
	}
	remapBrightness(&ps.Brightness.DCB)
	remapBrightness(&ps.Brightness.BackgroundContrast)
	remapBrightness(&ps.Brightness.VideoGroupA)
	remapBrightness(&ps.Brightness.VideoGroupB)
	remapBrightness(&ps.Brightness.FullDatablocks)
	remapBrightness(&ps.Brightness.Lists)
	remapBrightness(&ps.Brightness.Positions)
	remapBrightness(&ps.Brightness.LimitedDatablocks)
	remapBrightness(&ps.Brightness.OtherTracks)
	remapBrightness(&ps.Brightness.Lines)
	remapBrightness(&ps.Brightness.RangeRings)
	remapBrightness(&ps.Brightness.Compass)
	remapBrightness(&ps.Brightness.BeaconSymbols)
	remapBrightness(&ps.Brightness.PrimarySymbols)
	remapBrightness(&ps.Brightness.History)
	remapBrightness(&ps.Brightness.Weather)
	remapBrightness(&ps.Brightness.WxContrast)

	for len(ps.AudioEffectEnabled) < AudioNumTypes {
		ps.AudioEffectEnabled = append(ps.AudioEffectEnabled, true)
	}

	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[int]interface{})
	}
}

func (ps *PreferenceSet) Upgrade(from, to int) {
	if from < 8 {
		ps.Brightness.DCB = 60
		ps.CharSize.DCB = 1
	}
	if from < 9 {
		remap := func(b *STARSBrightness) {
			*b = STARSBrightness(math.Min(*b*2, 100))
		}
		remap(&ps.Brightness.VideoGroupA)
		remap(&ps.Brightness.VideoGroupB)
		remap(&ps.Brightness.RangeRings)
		remap(&ps.Brightness.Compass)
	}
	if from < 12 {
		if ps.Brightness.DCB == 0 {
			ps.Brightness.DCB = 60
		}
	}
	if from < 17 {
		// Added DisplayWeatherLevel
		for i := range ps.DisplayWeatherLevel {
			ps.DisplayWeatherLevel[i] = true
		}
	}
	if from < 18 {
		// ATPA; set defaults
		ps.DisplayATPAInTrailDist = true
		ps.DisplayATPAWarningAlertCones = true
	}
	if from < 21 {
		// System list offsets changed from updated handling of
		// transformation matrices with and without the DCB visible.
		ps.CharSize.DCB = math.Max(0, ps.CharSize.DCB-1)
		ps.CharSize.Datablocks = math.Max(0, ps.CharSize.Datablocks-1)
		ps.CharSize.Lists = math.Max(0, ps.CharSize.Lists-1)
		ps.CharSize.Tools = math.Max(0, ps.CharSize.Tools-1)
		ps.CharSize.PositionSymbols = math.Max(0, ps.CharSize.PositionSymbols-1)

		if ps.DisplayDCB && ps.DCBPosition == dcbPositionTop {
			shift := func(y *float32) {
				*y = math.Max(0, *y-.05)
			}
			shift(&ps.SSAList.Position[1])
			shift(&ps.VFRList.Position[1])
			shift(&ps.TABList.Position[1])
			shift(&ps.AlertList.Position[1])
			shift(&ps.CoastList.Position[1])
			shift(&ps.SignOnList.Position[1])
			shift(&ps.VideoMapsList.Position[1])
			shift(&ps.CRDAStatusList.Position[1])
			for i := range ps.TowerLists {
				shift(&ps.TowerLists[i].Position[1])
			}
		}
	}
	if from < 23 {
		// This should have been in the from < 21 case...
		if ps.PreviewAreaPosition[0] == .05 && ps.PreviewAreaPosition[1] == .8 {
			ps.PreviewAreaPosition = [2]float32{.05, .75}
		}
	}
	if from < 24 {
		ps.AudioVolume = 10
	}
}
