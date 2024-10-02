// pkg/panes/stars/prefs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/sim"

	"github.com/brunoga/deep"
)

const numSavedPreferenceSets = 32

// PreferenceSet stores the currently active preferences and up to
// numSavedPreferenceSets saved preferences; STARSPane keeps a separate
// PreferenceSet for each TRACON that the user signs in to.
type PreferenceSet struct {
	Current  Preferences
	Selected *int // if non-nil, an index into Saved
	Saved    [numSavedPreferenceSets]*Preferences
}

func (p *PreferenceSet) Upgrade(from, to int) {
	if p.Selected != nil && (*p.Selected < 0 || *p.Selected >= len(p.Saved)) {
		p.Selected = nil
	}
	p.Current.Upgrade(from, to)
	for _, p := range p.Saved {
		if p != nil {
			p.Upgrade(from, to)
		}
	}
}

func (p *PreferenceSet) SetCurrent(cur Preferences, pl platform.Platform, sp *STARSPane) {
	// Make sure we don't alias slices, maps, etc.
	p.Current = deep.MustCopy(cur)

	// Slightly annoying, but we need to let the Platform know the audio
	// volume from the prefs.
	p.Current.Activate(pl, sp)
}

// Reset ends up being called when a new Sim is started. It is responsible
// for resetting all of the preference values in the PreferenceSet that we
// don't expect to persist on a restart (e.g. quick look positions.)
func (p *PreferenceSet) Reset(ss sim.State, sp *STARSPane) {
	// Only reset Current; leave everything as is in the saved prefs.
	p.Current.Reset(ss, sp)
}

// ResetDefault resets the current preferences to the system defaults.
func (p *PreferenceSet) ResetDefault(ss sim.State, pl platform.Platform, sp *STARSPane) {
	// Start with the full-on STARS defaults and then update for the current Sim.
	p.Current = *makeDefaultPreferences()
	p.Reset(ss, sp)

	p.Selected = nil
	p.Current.Activate(pl, sp)
}

// Preferences encapsulates the user-settable STARS preferences that
type Preferences struct {
	CommonPreferences

	Name string // Name given if it's been saved

	Center math.Point2LL // The default center
	Range  float32

	CurrentCenter math.Point2LL

	RangeRingsCenter math.Point2LL
	RangeRingRadius  int
	// Whether we center them at RangeRingsCenter or Center
	RangeRingsUserCenter bool

	// User-supplied text for the SSA list
	ATIS   string
	GIText [9]string

	// If empty, then then MULTI or FUSED mode, depending on
	// FusedRadarMode.  The custom JSON name is so we don't get errors
	// parsing old configs, which stored this as an array...
	RadarSiteSelected string `json:"RadarSiteSelectedName"`
	FusedRadarMode    bool

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

	AutomaticHandoffs struct { // 4-30
		Interfacility bool
		Intrafacility bool
	}

	QuickLookAll       bool
	QuickLookAllIsPlus bool
	QuickLookPositions []QuickLookPosition

	DisplayEmptyCoordinationLists bool

	CRDA struct {
		Disabled bool
		// RunwayPairState has the same size and indexing as corresponding
		// the STARSPane STARSConvergingRunways member.
		RunwayPairState []CRDARunwayPairState
		ForceAllGhosts  bool
	}

	DisplayLDBBeaconCodes bool // TODO: default?
	SelectedBeaconCodes   []string

	// DisplayUncorrelatedTargets bool // NOT USED

	DisableCAWarnings bool
	DisableMSAW       bool

	VideoMapVisible map[int]interface{}

	DisplayRequestedAltitude bool
}

// CommonPreferences stores the STARS preference settings that are
// generally TRACON-independent--font size, brightness, etc.  This is
// admittedly somewhat subjective.  Splitting them out in this way lets us
// maintain those settings when the user starts a scenario at a new TRACON
// so that they don't need to start from scratch for each one.
type CommonPreferences struct {
	DisplayDCB  bool
	DCBPosition int

	AudioVolume int // 1-10

	RadarTrackHistory int // Number of history markers
	// 4-94: 0.5s increments via trackball but 0.1s increments allowed if
	// keyboard input.
	RadarTrackHistoryRate float32

	AudioEffectEnabled []bool

	DisplayWeatherLevel     [numWxLevels]bool
	LastDisplayWeatherLevel [numWxLevels]bool

	// For aircraft tracked by the user.
	LeaderLineDirection math.CardinalOrdinalDirection
	LeaderLineLength    int // 0-7

	OverflightFullDatablocks bool
	AutomaticFDBOffset       bool

	DisplayTPASize               bool
	DisplayATPAInTrailDist       bool `json:"DisplayATPAIntrailDist"`
	DisplayATPAWarningAlertCones bool
	DisplayATPAMonitorCones      bool

	PTLLength      float32
	PTLOwn, PTLAll bool

	DwellMode DwellMode

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
	VFRList       BasicSTARSList
	TABList       BasicSTARSList
	AlertList     BasicSTARSList
	CoastList     BasicSTARSList
	SignOnList    BasicSTARSList
	VideoMapsList struct {
		Position  [2]float32
		Visible   bool
		Selection VideoMapsGroup
	}
	CRDAStatusList      BasicSTARSList
	TowerLists          [3]BasicSTARSList
	CoordinationLists   map[string]*CoordinationList
	RestrictionAreaList BasicSTARSList

	RestrictionAreaSettings map[int]*RestrictionAreaSettings
}

type BasicSTARSList struct {
	Position [2]float32
	Visible  bool
	Lines    int
}

type CoordinationList struct {
	BasicSTARSList // Note that Visible is ignored for coordination lists.
	Group          string
	AutoRelease    bool
}

// RestrictionAreaSettings holds local settings related to restriction
// areas that aren't sent back to the server to be changed for all users.
type RestrictionAreaSettings struct {
	Visible           bool
	HideText          bool
	StopBlinkingText  bool
	ForceBlinkingText bool
}

func (p *Preferences) Reset(ss sim.State, sp *STARSPane) {
	// Get the scope centered and set the range according to the Sim's initial values.
	p.Center = ss.GetInitialCenter()
	p.CurrentCenter = p.Center
	p.RangeRingsCenter = p.Center
	p.RangeRingsUserCenter = false
	p.Range = ss.GetInitialRange()

	p.ATIS = ""
	for i := range p.GIText {
		p.GIText[i] = ""
	}

	p.RadarSiteSelected = ""

	// Reset CRDA state
	p.CRDA.RunwayPairState = nil
	state := CRDARunwayPairState{}
	// The first runway is enabled by default
	state.RunwayState[0].Enabled = true
	for range sp.ConvergingRunways {
		p.CRDA.RunwayPairState = append(p.CRDA.RunwayPairState, state)
	}

	clear(p.RestrictionAreaSettings)

	// Make the scenario's default video maps visible
	p.VideoMapVisible = make(map[int]interface{})
	_, defaultVideoMaps := ss.GetControllerVideoMaps()
	for _, dm := range defaultVideoMaps {
		if idx := slices.IndexFunc(sp.allVideoMaps, func(v av.VideoMap) bool { return v.Name == dm }); idx != -1 {
			p.VideoMapVisible[sp.allVideoMaps[idx].Id] = nil
		} else {
			// This should have been validated at load time.
			// lg.Errorf("%s: \"default_map\" not found in \"stars_maps\"", dm)
		}
	}
}

func makeDefaultPreferences() *Preferences {
	var prefs Preferences

	prefs.DisplayDCB = true
	prefs.DCBPosition = dcbPositionTop

	prefs.RangeRingRadius = 5

	prefs.RadarTrackHistory = 5
	prefs.RadarTrackHistoryRate = 4.5

	prefs.AudioVolume = 10
	prefs.AudioEffectEnabled = make([]bool, AudioNumTypes)
	for i := range AudioNumTypes {
		prefs.AudioEffectEnabled[i] = true
	}

	prefs.VideoMapVisible = make(map[int]interface{})

	prefs.FusedRadarMode = true
	prefs.LeaderLineDirection = math.North
	prefs.LeaderLineLength = 1

	prefs.AltitudeFilters.Unassociated = [2]int{100, 60000}
	prefs.AltitudeFilters.Associated = [2]int{100, 60000}

	//prefs.DisplayUncorrelatedTargets = true

	prefs.DisplayTPASize = true
	prefs.DisplayATPAWarningAlertCones = true

	prefs.PTLLength = 1

	prefs.Brightness.DCB = 60
	prefs.Brightness.BackgroundContrast = 0
	prefs.Brightness.VideoGroupA = 50
	prefs.Brightness.VideoGroupB = 40
	prefs.Brightness.FullDatablocks = 80
	prefs.Brightness.Lists = 80
	prefs.Brightness.Positions = 80
	prefs.Brightness.LimitedDatablocks = 80
	prefs.Brightness.OtherTracks = 80
	prefs.Brightness.Lines = 40
	prefs.Brightness.RangeRings = 20
	prefs.Brightness.Compass = 40
	prefs.Brightness.BeaconSymbols = 55
	prefs.Brightness.PrimarySymbols = 80
	prefs.Brightness.History = 60
	prefs.Brightness.Weather = 30
	prefs.Brightness.WxContrast = 30

	for i := range prefs.DisplayWeatherLevel {
		prefs.DisplayWeatherLevel[i] = true
	}

	prefs.CharSize.DCB = 1
	prefs.CharSize.Datablocks = 1
	prefs.CharSize.Lists = 1
	prefs.CharSize.Tools = 1
	prefs.CharSize.PositionSymbols = 0

	prefs.PreviewAreaPosition = [2]float32{.05, .75}

	prefs.SSAList.Position = [2]float32{.05, .9}
	prefs.SSAList.Filter.All = true

	prefs.SSAList.Filter.Text.Main = true
	for i := range prefs.SSAList.Filter.Text.GI {
		prefs.SSAList.Filter.Text.GI[i] = true
	}

	prefs.TABList.Position = [2]float32{.05, .65}
	prefs.TABList.Lines = 5
	prefs.TABList.Visible = true

	prefs.VFRList.Position = [2]float32{.05, .2}
	prefs.VFRList.Lines = 5
	prefs.VFRList.Visible = true

	prefs.AlertList.Position = [2]float32{.8, .25}
	prefs.AlertList.Lines = 5
	prefs.AlertList.Visible = true

	prefs.CoastList.Position = [2]float32{.8, .65}
	prefs.CoastList.Lines = 5
	prefs.CoastList.Visible = false

	prefs.SignOnList.Position = [2]float32{.9, .9}
	prefs.SignOnList.Visible = true

	prefs.VideoMapsList.Position = [2]float32{.85, .5}
	prefs.VideoMapsList.Visible = false

	prefs.CRDAStatusList.Position = [2]float32{.05, .7}

	prefs.TowerLists[0].Position = [2]float32{.05, .5}
	prefs.TowerLists[0].Lines = 5

	prefs.TowerLists[1].Position = [2]float32{.05, .8}
	prefs.TowerLists[1].Lines = 5

	prefs.TowerLists[2].Position = [2]float32{.05, .9}
	prefs.TowerLists[2].Lines = 5

	prefs.RestrictionAreaList.Position = [2]float32{.85, .575}

	prefs.CoordinationLists = make(map[string]*CoordinationList)
	prefs.RestrictionAreaSettings = make(map[int]*RestrictionAreaSettings)

	return &prefs
}

func (p *Preferences) Duplicate() *Preferences {
	c := deep.MustCopy(*p)
	return &c
}

func (p *Preferences) Activate(pl platform.Platform, sp *STARSPane) {
	pl.SetAudioVolume(p.AudioVolume)

	if p.VideoMapVisible == nil {
		p.VideoMapVisible = make(map[int]interface{})
	}
	if p.RestrictionAreaSettings == nil {
		p.RestrictionAreaSettings = make(map[int]*RestrictionAreaSettings)
	}
}

func (ps *Preferences) Upgrade(from, to int) {
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
	if from < 26 {
		// These are all from earlier releases but were previously done in
		// PreferenceSet Activate (unfortunately), so some of these may
		// still be lingering...

		// It should only take integer values but it's a float32 and we
		// previously didn't enforce this...
		ps.Range = float32(int(ps.Range))

		if ps.PTLAll { // both can't be set; we didn't enforce this previously...
			ps.PTLOwn = false
		}

		if ps.RadarTrackHistoryRate == 0 {
			ps.RadarTrackHistoryRate = 4.5 // upgrade from old
		}

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
	}
	if from < 27 {
		ps.SSAList.Filter.Text.Main = true
		for i := range ps.SSAList.Filter.Text.GI {
			ps.SSAList.Filter.Text.GI[i] = true
		}
		ps.CoordinationLists = make(map[string]*CoordinationList)

		ps.RangeRingsUserCenter = ps.RangeRingsCenter != ps.Center
	}
	if from < 29 {
		ps.RestrictionAreaList.Position = [2]float32{.8, .575}
		ps.RestrictionAreaSettings = make(map[int]*RestrictionAreaSettings)
	}
}

func (sp *STARSPane) initPrefsForLoadedSim(ss sim.State, pl platform.Platform) {
	prefSet, ok := sp.TRACONPreferenceSets[ss.TRACON]
	if !ok {
		// First time we've seen this TRACON. Start out with system defaults.
		prefSet = &PreferenceSet{
			Current: *makeDefaultPreferences(),
		}

		if sp.OldPrefsCurrentPreferenceSet != nil {
			// We loaded a saved config from a previous version; bootstrap
			// with the prefs from there.  (We're implicitly assuming that
			// they all apply to the selected TRACON, which should always
			// be the case...)
			prefSet.Current = *sp.OldPrefsCurrentPreferenceSet
			if sp.OldPrefsSelectedPreferenceSet != nil && *sp.OldPrefsSelectedPreferenceSet < len(sp.OldPrefsPreferenceSets) {
				prefSet.Selected = sp.OldPrefsSelectedPreferenceSet
			}
			for i, p := range sp.OldPrefsPreferenceSets {
				if i < len(prefSet.Saved) {
					prefSet.Saved[i] = &p
				}
			}

			// No more need for the old prefs representation
			sp.OldPrefsCurrentPreferenceSet = nil
			sp.OldPrefsSelectedPreferenceSet = nil
			sp.OldPrefsPreferenceSets = nil
		} else if sp.prefSet != nil {
			// Inherit the common prefs from the previously-active TRACON's
			// preferences.
			prefSet.Current.CommonPreferences = sp.prefSet.Current.CommonPreferences
		}

		sp.TRACONPreferenceSets[ss.TRACON] = prefSet
	}

	// Cache the PreferenceSet for use throughout the rest of the STARSPane
	// methods.
	sp.prefSet = prefSet
	sp.prefSet.Current.Activate(pl, sp)
}

// This is called when a new Sim is started from scratch.
func (sp *STARSPane) resetPrefsForNewSim(ss sim.State, pl platform.Platform) {
	sp.initPrefsForLoadedSim(ss, pl)

	// Clear out the preference-related state (e.g. quicklooks) that we
	// don't expect to persist across Sim restarts.
	sp.prefSet.Reset(ss, sp)
}

func (sp *STARSPane) currentPrefs() *Preferences {
	// sp.prefSet is initialized when either LoadSim() or ResetSim() ends
	// up calling initPrefsForLoadedSim().
	return &sp.prefSet.Current
}
