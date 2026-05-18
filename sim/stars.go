// pkg/sim/stars.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// FacilityConfiguration defines which controller handles each inbound/departure flow,
// the default consolidation hierarchy for the configuration, and optional fix pair assignments.
type FacilityConfiguration struct {
	InboundAssignments   map[string]TCP `json:"inbound_assignments"`
	DepartureAssignments map[string]TCP `json:"departure_assignments"`
	// GoAroundAssignments maps airport or airport/runway to the controller
	// who should handle go-arounds. If not specified, departure controller is used.
	GoAroundAssignments  map[string]TCP        `json:"go_around_assignments"`
	DefaultConsolidation PositionConsolidation `json:"default_consolidation"`
	FixPairAssignments   []FixPairAssignment   `json:"fix_pair_assignments,omitempty"`

	// ScratchpadLeaderLineDirectionStrings is the JSON-facing map from
	// primary scratchpad values to cardinal/ordinal direction strings
	// (e.g. "N", "NE", "SW"). Resolved into ScratchpadLeaderLineDirections
	// during PostDeserialize.
	ScratchpadLeaderLineDirectionStrings map[string]string `json:"scratchpad_leader_line_directions"`
	// ScratchpadLeaderLineDirections is the resolved map from primary
	// scratchpad values to leader line directions.
	ScratchpadLeaderLineDirections map[string]math.CardinalOrdinalDirection `json:"-"`
}

type FacilityAdaptation struct {
	// Configurations maps config IDs (max 3 chars) to facility configurations.
	// These define which TCP handles each inbound flow and departure airport/runway/SID.
	Configurations map[string]*FacilityConfiguration `json:"configurations"`

	AirspaceAwareness []AirspaceAwareness                  `json:"airspace_awareness"`
	VideoMapLabels    map[string]string                    `json:"map_labels"`
	Controllers       map[ControlPosition]*STARSController `json:"controllers"`
	Areas             map[string]*STARSArea                `json:"areas,omitempty"`
	RadarSites        map[string]*av.RadarSite             `json:"radar_sites"`
	Center            math.Point2LL                        `json:"-"`
	CenterString      string                               `json:"center"`
	MaxDistance       float32                              `json:"max_distance"` // Distance from center where aircraft get culled from (default 125nm STARS, 400nm ERAM)
	Range             float32                              `json:"range"`
	Scratchpads       map[string]string                    `json:"scratchpads"`
	SignificantPoints map[string]SignificantPoint          `json:"significant_points"`

	// Airpsace filters
	Filters struct {
		AutoAcquisition FilterRegions    `json:"auto_acquisition"`
		ArrivalDrop     FilterRegions    `json:"arrival_drop"`
		Departure       FilterRegions    `json:"departure"`
		InhibitCA       FilterRegions    `json:"inhibit_ca"`
		InhibitMSAW     FilterRegions    `json:"inhibit_msaw"`
		Quicklook       QuicklookRegions `json:"quicklook"`
		FDAM            FDAMRegions      `json:"fdam"`
		SecondaryDrop   FilterRegions    `json:"secondary_drop"`
		SurfaceTracking FilterRegions    `json:"surface_tracking"`
		VFRInhibit      FilterRegions    `json:"vfr_inhibit"`
	} `json:"filters"`

	MonitoredBeaconCodeBlocksString  *string
	MonitoredBeaconCodeBlocks        []av.Squawk
	UntrackedPositionSymbolOverrides struct {
		CodeRangesString string         `json:"beacon_codes"`
		CodeRanges       [][2]av.Squawk // inclusive
		Symbol           string         `json:"symbol"`
	} `json:"untracked_position_symbol_overrides"`

	VideoMapFile       string                        `json:"video_map_file"`
	CoordinationFixes  map[string]av.AdaptationFixes `json:"coordination_fixes"`
	SingleCharAIDs     map[string]string             `json:"single_char_aids"`
	Monitor            string                        `json:"monitor"`
	Thick20NmRangeRing bool                          `json:"thick_20nm_range_ring"`

	SSRCodes av.LocalSquawkCodePoolSpecifier `json:"ssr_codes"`

	AirportCodes map[string]string `json:"airport_codes"`

	FlightPlan struct {
		QuickACID          string            `json:"quick_acid"`
		ACIDExpansions     map[string]string `json:"acid_expansions"`
		ModifyAfterDisplay bool              `json:"modify_after_display"`
	} `json:"flight_plan"`

	Datablocks struct {
		ClockPhase struct {
			Sequence  []int     `json:"sequence"`  // e.g., [1,2,1,3]
			Intervals []float32 `json:"intervals"` // per-position durations in seconds
		} `json:"clock_phase"`
		FDB struct {
			DisplayRequestedAltitude bool `json:"display_requested_altitude"`
			Scratchpad2OnLine3       bool `json:"scratchpad2_on_line3"`
			DisplayFacilityOnly      bool `json:"display_facility_only"`
			AcceptFlashDuration      int  `json:"accept_flash_duration"`
			SectorDisplayDuration    int  `json:"sector_display_duration"`
		} `json:"fdb"`
		PDB struct {
			ShowScratchpad2   bool `json:"show_scratchpad2"`
			HideGroundspeed   bool `json:"hide_gs"`
			ShowAircraftType  bool `json:"show_aircraft_type"`
			SplitGSAndCWT     bool `json:"split_gs_and_cwt"`
			DisplayCustomSPCs bool `json:"display_custom_spcs"`
		} `json:"pdb"`
		LDB struct {
			KeepAfterSlew bool `json:"keep_after_slew"`
			FullSeconds   int  `json:"full_seconds"`
		} `json:"ldb"`
		Scratchpad1 struct {
			DisplayExitFix     bool `json:"display_exit_fix"`
			DisplayExitFix1    bool `json:"display_exit_fix_1"`
			DisplayExitGate    bool `json:"display_exit_gate"`
			DisplayAltExitGate bool `json:"display_alternate_exit_gate"`
		} `json:"scratchpad1"`
		AllowLongScratchpad bool     `json:"allow_long_scratchpad"`
		ForceQLToSelf       bool     `json:"force_ql_self"`
		CustomSPCs          []string `json:"custom_spcs"`
		DisplayRNAVSymbol   bool     `json:"display_rnav_symbol"`
	} `json:"datablocks"`

	Lists struct {
		Coordination []CoordinationList `json:"coordination"`
		SSA          struct {
			Altimeters        []string `json:"altimeters"`
			FlashOnATISUpdate bool     `json:"flash_on_atis_update"`
		} `json:"ssa"`
		VFR struct {
			Format string `json:"format"`
		} `json:"vfr"`
		TAB struct {
			Format string `json:"format"`
		} `json:"tab"`
		CoastSuspend struct {
			Format string `json:"format"`
		} `json:"coast_suspend"`
		MCISuppression struct {
			Format string `json:"format"`
		} `json:"mci_suppression"`
		Tower struct {
			Format string `json:"format"`
		} `json:"tower"`
	} `json:"lists"`
	RestrictionAreas map[int]av.RestrictionArea `json:"restriction_areas"`
	UseLegacyFont    bool                       `json:"use_legacy_font"`
	STARSMacros      STARSMacroSet              `json:"stars_macros"`
}

// STARSMacroSet is a list of facility-defined keyboard macros.
type STARSMacroSet []STARSMacro

// STARSMacro defines a single keyboard macro that chains multiple STARS
// commands together when activated via F16.
type STARSMacro struct {
	Input       string   `json:"input"`
	Commands    []string `json:"commands"`
	Output      string   `json:"output"`
	Description string   `json:"description"`
}

func (m STARSMacro) IsSlew() bool {
	return strings.HasSuffix(m.Input, "[SLEW]")
}

func (m STARSMacro) Name() string {
	n := strings.TrimSuffix(m.Input, "[SLEW]")
	if idx := strings.IndexByte(n, ']'); idx != -1 {
		return n[idx+1:]
	}
	return n
}

func (m STARSMacro) Mode() string {
	n := strings.TrimSuffix(m.Input, "[SLEW]")
	if idx := strings.IndexByte(n, ']'); idx > 2 {
		return n[1:idx]
	}
	return ""
}

func (m STARSMacro) HasParameters() bool {
	for _, cmd := range m.Commands {
		if strings.Contains(cmd, "$1") {
			return true
		}
	}
	return false
}

// ValidateMacroCommandMode is registered by the stars package at init time.
// It returns true if the given string is a valid command mode for macro commands.
var ValidateMacroCommandMode func(string) bool

type STARSController struct {
	VideoMapFile                    string        `json:"video_map_file,omitempty"`
	VideoMapNames                   []string      `json:"video_maps"`
	DefaultMaps                     []string      `json:"default_maps"`
	Center                          math.Point2LL `json:"-"`
	CenterString                    string        `json:"center"`
	Range                           float32       `json:"range"`
	MonitoredBeaconCodeBlocksString *string       `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []av.Squawk
	FlightFollowingAirspace         []av.AirspaceVolume `json:"flight_following_airspace"`
	Altimeters                      []string            `json:"altimeters"`
}

// STARSArea provides default configuration for all controllers
// within a TRACON area. Controller-specific settings in Controllers
// override or append these defaults.
type STARSArea struct {
	DefaultAirport                  string                         `json:"default_airport,omitempty"` // CRDA default airport for this area
	VideoMapFile                    string                         `json:"video_map_file,omitempty"`
	VideoMapNames                   []string                       `json:"video_maps,omitempty"`
	DefaultMaps                     []string                       `json:"default_maps,omitempty"`
	Center                          math.Point2LL                  `json:"-"`
	CenterString                    string                         `json:"center,omitempty"`
	Range                           float32                        `json:"range,omitempty"`
	MonitoredBeaconCodeBlocksString *string                        `json:"beacon_code_blocks,omitempty"`
	MonitoredBeaconCodeBlocks       []av.Squawk                    `json:"-"`
	FlightFollowingAirspace         []av.AirspaceVolume            `json:"flight_following_airspace,omitempty"`
	Altimeters                      []string                       `json:"altimeters,omitempty"`
	Scratchpads                     map[string]string              `json:"scratchpads,omitempty"`
	CoordinationLists               []CoordinationList             `json:"coordination_lists,omitempty"`
	AirspaceAwareness               []AirspaceAwareness            `json:"airspace_awareness,omitempty"`
	Airspace                        map[string][]av.AirspaceVolume `json:"airspace,omitempty"`
}

// CurrentDatablockClockPhase returns the current clock phase (1-4)
// based on the configured phase sequence and interval durations.
func (fa *FacilityAdaptation) CurrentDatablockClockPhase(now time.Time) int {
	seq := fa.Datablocks.ClockPhase.Sequence
	intervals := fa.Datablocks.ClockPhase.Intervals

	// Total cycle duration in milliseconds.
	totalMs := util.ReduceSlice(intervals, func(d float32, acc int64) int64 { return acc + int64(d*1000) }, int64(0))
	posMs := now.UnixMilli() % totalMs

	var elapsed int64
	for i, d := range intervals {
		elapsed += int64(d * 1000)
		if posMs < elapsed {
			return seq[i]
		}
	}
	return seq[0]
}

// DefaultAirportForArea returns the CRDA default airport for a given
// area identifier. Returns empty string if no area config or default
// airport is defined.
func (fa *FacilityAdaptation) DefaultAirportForArea(area string) string {
	if area == "" {
		return ""
	}
	if ac, ok := fa.Areas[area]; ok {
		return ac.DefaultAirport
	}
	return ""
}

// ScratchpadForFix returns the scratchpad code for a given fix,
// checking area-specific scratchpads first, then falling back to
// facility-level scratchpads.
func (fa *FacilityAdaptation) ScratchpadForFix(fix string, area string) (string, bool) {
	if area != "" {
		if ac, ok := fa.Areas[area]; ok && ac.Scratchpads != nil {
			if sp, ok := ac.Scratchpads[fix]; ok {
				return sp, true
			}
		}
	}
	if sp, ok := fa.Scratchpads[fix]; ok {
		return sp, true
	}
	return "", false
}

// AirspaceAwarenessForArea returns the airspace awareness rules for a
// given area. Area-level entries come first so they take priority (since
// calculateAirspace returns on the first match), with facility-level
// entries as fallback.
func (fa *FacilityAdaptation) AirspaceAwarenessForArea(area string) []AirspaceAwareness {
	if area != "" {
		if ac, ok := fa.Areas[area]; ok && len(ac.AirspaceAwareness) > 0 {
			return slices.Concat(ac.AirspaceAwareness, fa.AirspaceAwareness)
		}
	}
	return fa.AirspaceAwareness
}

// VideoMapFileForArea returns the effective video map file for a given
// area. If the area has its own VideoMapFile, it is used; otherwise
// the facility-level VideoMapFile is returned.
func (fa *FacilityAdaptation) VideoMapFileForArea(area string) string {
	if area != "" {
		if ac, ok := fa.Areas[area]; ok && ac.VideoMapFile != "" {
			return ac.VideoMapFile
		}
	}
	return fa.VideoMapFile
}

type CoordinationList struct {
	Name          string   `json:"name"`
	Id            string   `json:"id"`
	Airports      []string `json:"airports"`
	YellowEntries bool     `json:"yellow_entries"`
	Format        string   `json:"format"`
}

// Validates a format string for a STARS system list. Extra specifiers that are specific to
// particular list types may be provided via extra.
func validateListFormat(format string, extra ...string) error {
	fpSpecifiers := []string{"ACID", "ACID_MSAWCA", "ACTYPE", "BEACON", "CWT", "DEP_EXIT_FIX", "ENTRY_FIX",
		"EXIT_FIX", "EXIT_GATE", "INDEX", "NUMAC", "OWNER", "REQ_ALT"}

	i := 0
	for i < len(format) {
		if format[i] == '[' {
			// Find the end of the specifier
			endIdx := strings.IndexByte(format[i:], ']')
			if endIdx == -1 {
				return fmt.Errorf(`unclosed "[" at offset %d`, i)
			}

			specifier := format[i+1 : i+endIdx]
			if specifier == "" {
				return fmt.Errorf("empty specifier at offset %d", i)
			} else if !slices.Contains(fpSpecifiers, specifier) && !slices.Contains(extra, specifier) {
				return fmt.Errorf("unknown specifier %q at offset %d", specifier, i)
			}

			i += endIdx + 1
		} else {
			i++
		}
	}

	return nil
}

type SignificantPoint struct {
	Name         string        // JSON comes in as a map from name to SignificantPoint; we set this.
	ShortName    string        `json:"short_name"`
	Abbreviation string        `json:"abbreviation"`
	Description  string        `json:"description"`
	Location     math.Point2LL `json:"location"`
}

type AirspaceAwareness struct {
	Fix                 []string `json:"fixes"`
	AltitudeRange       [2]int   `json:"altitude_range"`
	ReceivingController string   `json:"receiving_controller"`
	AircraftType        []string `json:"aircraft_type"`
}

func (fa *FacilityAdaptation) PostDeserialize(loc av.Locator, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if ctr := fa.CenterString; ctr == "" {
		e.ErrorString(`No "center" specified`)
	} else if pos, ok := loc.Locate(ctr); !ok {
		e.ErrorString(`unknown location %q specified for "center"`, ctr)
	} else {
		fa.Center = pos
	}

	// Locator-dependent controller config validation.
	for tcp, config := range fa.Controllers {
		e.Push(fmt.Sprintf("controllers[%s]", tcp))

		if config.CenterString != "" {
			if pos, ok := loc.Locate(config.CenterString); ok {
				config.Center = pos
			} else {
				e.ErrorString("unknown location %q specified for controller center", config.CenterString)
			}
		}

		for i := range config.FlightFollowingAirspace {
			config.FlightFollowingAirspace[i].PostDeserialize(loc, e)
		}

		e.Pop()
	}

	// Locator-dependent area config validation.
	for areaNum, ac := range fa.Areas {
		e.Push(fmt.Sprintf("areas[%s]", areaNum))

		for i := range ac.FlightFollowingAirspace {
			ac.FlightFollowingAirspace[i].PostDeserialize(loc, e)
		}

		if ac.CenterString != "" {
			if pos, ok := loc.Locate(ac.CenterString); ok {
				ac.Center = pos
			} else {
				e.ErrorString("unknown location %q specified for area center", ac.CenterString)
			}
		}

		for name, volumes := range ac.Airspace {
			for i := range volumes {
				volumes[i].PostDeserialize(loc, e)
				if volumes[i].Id == "" {
					volumes[i].Id = fmt.Sprintf("A%s-%s-%d", areaNum, name, i+1)
				}
			}
			ac.Airspace[name] = volumes
		}

		e.Pop()
	}

	checkFilter := func(f FilterRegions, name string) {
		ids := make(map[string]any)
		for i, filt := range f {
			e.Push(filt.Description)
			f[i].AirspaceVolume.PostDeserialize(loc, e)

			if _, ok := ids[filt.Id]; ok {
				e.ErrorString(`filter "id"s must be unique: %q was repeated`, filt.Id)
			}
			ids[filt.Id] = nil

			e.Pop()
		}
	}
	checkFilter(fa.Filters.AutoAcquisition, "auto_acquisition")
	checkFilter(fa.Filters.ArrivalDrop, "arrival_drop")
	checkFilter(fa.Filters.Departure, "departure")
	checkFilter(fa.Filters.InhibitCA, "inhibit_ca")
	checkFilter(fa.Filters.InhibitMSAW, "inhibit_msaw")
	checkFilter(fa.Filters.SecondaryDrop, "secondary_drop")
	checkFilter(fa.Filters.SurfaceTracking, "surface_tracking")

	{
		ids := make(map[string]any)
		for i, filt := range fa.Filters.Quicklook {
			e.Push(filt.Description)
			fa.Filters.Quicklook[i].PostDeserialize(loc, e)

			if _, ok := ids[filt.Id]; ok {
				e.ErrorString(`quicklook filter "id"s must be unique: %q was repeated`, filt.Id)
			}
			ids[filt.Id] = nil

			e.Pop()
		}
	}

	{
		ids := make(map[string]any)
		for i, filt := range fa.Filters.FDAM {
			e.Push(filt.Description)
			fa.Filters.FDAM[i].PostDeserialize(loc, e)

			if _, ok := ids[filt.Id]; ok {
				e.ErrorString(`FDAM filter "id"s must be unique: %q was repeated`, filt.Id)
			}
			ids[filt.Id] = nil

			e.Pop()
		}
	}

	// This one kicks in when they exit the "inside" region defined by the volume.
	for i := range fa.Filters.SecondaryDrop {
		fa.Filters.SecondaryDrop[i].InvertTest = true
	}

	// Quick FP ACID
	e.Push(`"flight_plan"`)
	fa.FlightPlan.QuickACID = strings.ToUpper(fa.FlightPlan.QuickACID)
	if qa := fa.FlightPlan.QuickACID; qa == "" {
		fa.FlightPlan.QuickACID = "VCE"
	} else {
		if qa[0] < 'A' || qa[0] > 'Z' {
			e.ErrorString(`"quick_acid" must start with a letter`)
		}
		if len(qa) > 3 {
			e.ErrorString(`"quick_acid" can't be more than three characters`)
		}
	}

	// ACID expansions
	for abbrev, exp := range fa.FlightPlan.ACIDExpansions {
		if len(abbrev) != 1 {
			e.ErrorString("Abbreviation %q is not allowed: must be a single character", abbrev)
		}
		if !strings.Contains("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+.*^/", abbrev) {
			e.ErrorString("Abbreviation %q must be A-Z, 0-9, +, ., *, ^, or /", abbrev)
		}
		if len(exp) == 0 {
			e.ErrorString("Must specify an expansion for %q", abbrev)
		} else if exp[0] < 'A' || exp[0] > 'Z' {
			e.ErrorString("Expansion %q for %q must start with a letter", exp, abbrev)
		}
	}
	e.Pop()

	e.Push(`"tab"`)
	if fa.Lists.TAB.Format == "" {
		fa.Lists.TAB.Format = "[INDEX] [ACID_MSAWCA][DUPE_BEACON] [BEACON] [DEP_EXIT_FIX]"
	}
	if err := validateListFormat(fa.Lists.TAB.Format, "DUPE_BEACON"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.Lists.TAB.Format, err)
	}
	e.Pop()

	e.Push(`"vfr"`)
	if fa.Lists.VFR.Format == "" {
		fa.Lists.VFR.Format = "[INDEX] [ACID_MSAWCA][VFR_STATUS] [BEACON]"
	}
	if err := validateListFormat(fa.Lists.VFR.Format, "VFR_STATUS"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.Lists.VFR.Format, err)
	}
	e.Pop()

	e.Push(`"coast_suspend"`)
	if fa.Lists.CoastSuspend.Format == "" {
		fa.Lists.CoastSuspend.Format = "[INDEX] [ACID] S [BEACON] [ALT]"
	}
	if err := validateListFormat(fa.Lists.CoastSuspend.Format, "ALT"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.Lists.CoastSuspend.Format, err)
	}
	e.Pop()

	e.Push(`"mci_suppression"`)
	if fa.Lists.MCISuppression.Format == "" {
		fa.Lists.MCISuppression.Format = "[ACID] [BEACON]  [SUPP_BEACON]"
	}
	if err := validateListFormat(fa.Lists.MCISuppression.Format, "SUPP_BEACON"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.Lists.MCISuppression.Format, err)
	}
	e.Pop()

	e.Push(`"tower"`)
	if fa.Lists.Tower.Format == "" {
		fa.Lists.Tower.Format = "[ACID] [ACTYPE]"
	}
	if err := validateListFormat(fa.Lists.Tower.Format); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.Lists.Tower.Format, err)
	}
	e.Pop()

	e.Push(`"coordination"`)
	for i, cl := range fa.Lists.Coordination {
		if cl.Format == "" {
			// Default format
			fa.Lists.Coordination[i].Format = "[INDEX][ACKED]    [ACID] [ACTYPE] [BEACON]   [EXIT_FIX] [REQ_ALT]"
		}

		if err := validateListFormat(cl.Format, "ACKED"); err != nil {
			e.ErrorString("Invalid format string for coordination list %d (%s): %v", i, cl.Name, err)
		}
	}
	e.Pop()
}

func (fa FacilityAdaptation) CheckScratchpad(sp string) bool {
	lc := len([]rune(sp))

	// 5-148
	if fa.Datablocks.AllowLongScratchpad && lc > 4 {
		return false
	} else if !fa.Datablocks.AllowLongScratchpad && lc > 3 {
		return false
	}

	// Make sure it's only allowed characters; handling of Δ is a little wonky
	// since STARSPane rewrites it to 0x80 but there are a few options for delta
	// that can show up in scenario files.
	const STARSTriangleCharacter = string(rune(0x80))
	allowedCharacters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./*Δ∆" + STARSTriangleCharacter
	for _, letter := range sp {
		if !strings.ContainsRune(allowedCharacters, letter) {
			return false
		}
	}

	// It can't be three numerals
	if lc == 3 && sp[0] >= '0' && sp[0] <= '9' &&
		sp[1] >= '0' && sp[1] <= '9' &&
		sp[2] >= '0' && sp[2] <= '9' {
		return false
	}

	// Certain specific strings aren't allowed in the first 3 characters
	for _, ill := range []string{"NAT", "CST", "AMB", "RDR", "ADB", "XXX"} {
		if strings.HasPrefix(sp, ill) {
			return false
		}
	}

	return true
}
