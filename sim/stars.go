// pkg/sim/stars.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
)

type RedirectedHandoff struct {
	OriginalOwner ControlPosition   // Controller position
	Redirector    []ControlPosition // Redirecting controllers
	RedirectedTo  ControlPosition
}

func (rd *RedirectedHandoff) GetLastRedirector() ControlPosition {
	if length := len(rd.Redirector); length > 0 {
		return rd.Redirector[length-1]
	} else {
		return ""
	}
}

func (rd *RedirectedHandoff) ShowRDIndicator(pos ControlPosition, RDIndicatorEnd time.Time) bool {
	// Show "RD" to the redirect target, last redirector until the RD is accepted.
	// Show "RD" to the original owner up to 30 seconds after the RD is accepted.
	return pos != "" && (rd.RedirectedTo == pos || rd.GetLastRedirector() == pos ||
		rd.OriginalOwner == pos || time.Until(RDIndicatorEnd) > 0)
}

func (rd *RedirectedHandoff) ShouldFallbackToHandoff(ctrl, octrl ControlPosition) bool {
	// True if the 2nd redirector redirects back to the 1st redirector
	return (len(rd.Redirector) == 1 || (len(rd.Redirector) > 1) && rd.Redirector[1] == ctrl) && octrl == rd.Redirector[0]
}

func (rd *RedirectedHandoff) AddRedirector(ctrl *av.Controller) {
	if len(rd.Redirector) == 0 || rd.Redirector[len(rd.Redirector)-1] != ctrl.PositionId() {
		// Don't append the same controller multiple times
		// (the case in which the last redirector recalls and then redirects again)
		rd.Redirector = append(rd.Redirector, ctrl.PositionId())
	}
}

///////////////////////////////////////////////////////////////////////////

// Note: this should match ViceMapSpec/VideoMap in crc2vice/dat2vice. (crc2vice
// doesn't support all of these, though.)
type VideoMap struct {
	Label       string // for DCB
	Group       int    // 0 -> A, 1 -> B
	Name        string // For maps system list
	Id          int
	Category    int
	Restriction struct {
		Id        int
		Text      [2]string
		TextBlink bool
		HideText  bool
	}
	Color int
	Lines [][]math.Point2LL
}

// This should match VideoMapLibrary in dat2vice
type VideoMapLibrary struct {
	Maps          []VideoMap
	ERAMMapGroups ERAMMapGroups
}

type ERAMMap struct {
	BcgName    string
	LabelLine1 string
	LabelLine2 string
	Name       string
	Lines      [][]math.Point2LL
}

type ERAMMapGroup struct {
	Maps       []ERAMMap
	LabelLine1 string
	LabelLine2 string
}

type ERAMMapGroups map[string]ERAMMapGroup

// VideoMapManifest stores which maps are available in a video map file and
// is also able to provide the video map file's hash.
type VideoMapManifest struct {
	names      map[string]any
	filesystem fs.FS
	filename   string
}

func CheckVideoMapManifest(filename string, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if strings.Contains(filename, "eram") {
		return // ERAM manifest not here
	}

	manifest, err := LoadVideoMapManifest(filename)
	if err != nil {
		e.Error(err)
		return
	}

	vms, err := LoadVideoMapLibrary(filename)
	if err != nil {
		e.Error(err)
		return
	}

	for n := range manifest.names {
		if !slices.ContainsFunc(vms.Maps, func(v VideoMap) bool { return v.Name == n }) {
			e.ErrorString("%s: map is in manifest file but not video map file", n)
		}
	}
	for _, m := range vms.Maps {
		if _, ok := manifest.names[m.Name]; !ok {
			e.ErrorString("%s: map is in video map file but not manifest", m.Name)
		}
	}
}

func LoadVideoMapManifest(filename string) (*VideoMapManifest, error) {
	filesystem := videoMapFS(filename)

	// Load the manifest and do initial error checking
	mf, _ := strings.CutSuffix(filename, ".zst")
	mf, _ = strings.CutSuffix(mf, "-videomaps.gob")
	mf += "-manifest.gob"

	fm, err := filesystem.Open(mf)
	if err != nil {
		return nil, err
	}
	defer fm.Close()

	var names map[string]any
	dec := gob.NewDecoder(fm)
	if err := dec.Decode(&names); err != nil {
		return nil, err
	}

	// Make sure the file exists but don't load it until it's needed.
	f, err := filesystem.Open(filename)
	if err != nil {
		return nil, err
	} else {
		f.Close()
	}

	return &VideoMapManifest{
		names:      names,
		filesystem: filesystem,
		filename:   filename,
	}, nil
}

func (v VideoMapManifest) HasMap(s string) bool {
	for i, m := range v.names {
		if i == s {
			return true
		}
		if names, ok := m.([]string); ok {
			if slices.Contains(names, s) {
				return true
			}
		}
	}
	return false
}

func (v VideoMapManifest) HasMapGroup(s string) bool {
	for i := range v.names {
		if i == s {
			return true
		}
	}
	return false
}

// Hash returns a hash of the underlying video map file (i.e., not the manifest!)
func (v VideoMapManifest) Hash() ([]byte, error) {
	if f, err := v.filesystem.Open(v.filename); err == nil {
		defer f.Close()
		return util.Hash(f)
	} else {
		return nil, err
	}
}

func LoadVideoMapLibrary(path string) (*VideoMapLibrary, error) {
	filesystem := videoMapFS(path)
	f, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	contents, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	var r io.Reader
	br := bytes.NewReader(contents)
	var zr *zstd.Decoder
	if len(contents) > 4 && contents[0] == 0x28 && contents[1] == 0xb5 && contents[2] == 0x2f && contents[3] == 0xfd {
		// zstd compressed
		zr, _ = zstd.NewReader(br, zstd.WithDecoderConcurrency(0))
		defer zr.Close()
		r = zr
	} else {
		r = br
	}

	// Decode the gobfile.
	var vmf VideoMapLibrary
	if err := gob.NewDecoder(r).Decode(&vmf); err != nil {
		// Try the old format, just an array of maps
		// Create a new reader to avoid racing with the zstd decoder goroutine
		br = bytes.NewReader(contents)
		if zr != nil {
			_ = zr.Reset(br)
		} else {
			r = br
		}
		if strings.Contains(path, "eram") {
			if vmf.ERAMMapGroups == nil {
				vmf.ERAMMapGroups = make(ERAMMapGroups)
			}
			if err := gob.NewDecoder(r).Decode(&vmf.ERAMMapGroups); err != nil {
				return nil, err
			}
		} else {
			if err := gob.NewDecoder(r).Decode(&vmf.Maps); err != nil {
				return nil, err
			}
		}
	}

	return &vmf, nil
}

// Loads the specified video map file, though only if its hash matches the
// provided hash. Returns an error otherwise.
func HashCheckLoadVideoMap(path string, wantHash []byte) (*VideoMapLibrary, error) {
	filesystem := videoMapFS(path)
	f, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if gotHash, err := util.Hash(f); err != nil {
		return nil, err
	} else if !slices.Equal(gotHash, wantHash) {
		return nil, errors.New("hash mismatch")
	}

	return LoadVideoMapLibrary(path)
}

// Returns an fs.FS that allows us to load the video map with the given path.
func videoMapFS(path string) fs.FS {
	if filepath.IsAbs(path) {
		return util.RootFS{}
	} else {
		return util.GetResourcesFS()
	}
}

func PrintVideoMaps(path string, e *util.ErrorLogger) {
	if vmf, err := LoadVideoMapLibrary(path); err != nil {
		e.Error(err)
		return
	} else {
		sort.Slice(
			vmf.Maps, func(i, j int) bool {
				vi, vj := vmf.Maps[i], vmf.Maps[j]
				if vi.Id != vj.Id {
					return vi.Id < vj.Id
				}
				return vi.Name < vj.Name
			},
		)

		fmt.Printf("%5s\t%20s\t%s\n", "Id", "Label", "Name")
		for _, m := range vmf.Maps {
			fmt.Printf("%5d\t%20s\t%s\n", m.Id, m.Label, m.Name)
		}
	}
}

// ControllerAssignments defines which controller handles each inbound/departure flow,
// the default consolidation hierarchy for the configuration, and optional fix pair assignments.
type ControllerAssignments struct {
	InboundAssignments   map[string]TCP        `json:"inbound_assignments"`
	DepartureAssignments map[string]TCP        `json:"departure_assignments"`
	DefaultConsolidation PositionConsolidation `json:"default_consolidation"`
	FixPairAssignments   []FixPairAssignment   `json:"fix_pair_assignments,omitempty"`
}

type FacilityAdaptation struct {
	// Configurations maps config IDs (max 3 chars) to controller assignments.
	// These define which TCP handles each inbound flow and departure airport/runway/SID.
	Configurations map[string]*ControllerAssignments `json:"configurations"`

	ForceQLToSelf       bool                                       `json:"force_ql_self" scope:"stars"`
	AllowLongScratchpad bool                                       `json:"allow_long_scratchpad" scope:"stars"`
	VideoMapNames       []string                                   `json:"stars_maps" scope:"stars"`
	ERAMMapNames        map[string][]string                        `json:"eram_maps" scope:"eram"`
	VideoMapLabels      map[string]string                          `json:"map_labels"`
	ControllerConfigs   map[ControlPosition]*STARSControllerConfig `json:"controller_configs"`
	AreaConfigs         map[int]*STARSAreaConfig                  `json:"area_configs,omitempty"`
	RadarSites          map[string]*av.RadarSite                   `json:"radar_sites" scope:"stars"`
	Center              math.Point2LL                              `json:"-"`
	CenterString        string                                     `json:"center"`
	MaxDistance         float32                                    `json:"max_distance"` // Distance from center where aircraft get culled from (default 125nm)
	Range               float32                                    `json:"range"`
	Scratchpads         map[string]string                          `json:"scratchpads" scope:"stars"`
	SignificantPoints   map[string]SignificantPoint                `json:"significant_points" scope:"stars"`
	Altimeters          []string                                   `json:"altimeters"`

	// Airpsace filters
	Filters struct {
		AutoAcquisition FilterRegions `json:"auto_acquisition"`
		ArrivalDrop     FilterRegions `json:"arrival_drop"`
		Departure       FilterRegions `json:"departure"`
		InhibitCA       FilterRegions `json:"inhibit_ca"`
		InhibitMSAW     FilterRegions `json:"inhibit_msaw"`
		Quicklook       FilterRegions `json:"quicklook"`
		SecondaryDrop   FilterRegions `json:"secondary_drop"`
		SurfaceTracking FilterRegions `json:"surface_tracking"`
		VFRInhibit      FilterRegions `json:"vfr_inhibit"`
	} `json:"filters" scope:"stars"` //Should this be STARS or justy parts of it?

	MonitoredBeaconCodeBlocksString  *string
	MonitoredBeaconCodeBlocks        []av.Squawk
	UntrackedPositionSymbolOverrides struct {
		CodeRangesString string         `json:"beacon_codes"`
		CodeRanges       [][2]av.Squawk // inclusive
		Symbol           string         `json:"symbol"`
	} `json:"untracked_position_symbol_overrides" scope:"stars"`

	VideoMapFile      string                        `json:"video_map_file"`
	SingleCharAIDs    map[string]string             `json:"single_char_aids" scope:"stars"` // Char to airport. TODO: Check if this is for ERAM as well.
	KeepLDB           bool                          `json:"keep_ldb" scope:"stars"`
	FullLDBSeconds    int                           `json:"full_ldb_seconds" scope:"stars"`

	SSRCodes av.LocalSquawkCodePoolSpecifier `json:"ssr_codes" scope:"stars"`

	HandoffAcceptFlashDuration int  `json:"handoff_acceptance_flash_duration" scope:"stars"`
	DisplayHOFacilityOnly      bool `json:"display_handoff_facility_only" scope:"stars"`
	HOSectorDisplayDuration    int  `json:"handoff_sector_display_duration" scope:"stars"`

	AirportCodes map[string]string `json:"airport_codes" scope:"eram"`

	FlightPlan struct {
		QuickACID          string            `json:"quick_acid"`
		ACIDExpansions     map[string]string `json:"acid_expansions"`
		ModifyAfterDisplay bool              `json:"modify_after_display"`
	} `json:"flight_plan" scope:"stars"`

	PDB struct {
		ShowScratchpad2   bool `json:"show_scratchpad2"`
		HideGroundspeed   bool `json:"hide_gs"`
		ShowAircraftType  bool `json:"show_aircraft_type"`
		SplitGSAndCWT     bool `json:"split_gs_and_cwt"`
		DisplayCustomSPCs bool `json:"display_custom_spcs"`
	} `json:"pdb" scope:"stars"`

	FDB struct {
		DisplayRequestedAltitude bool `json:"display_requested_altitude"`
		Scratchpad2OnLine3       bool `json:"scratchpad2_on_line3"`
	} `json:"fdb" scope:"stars"`

	Scratchpad1 struct {
		DisplayExitFix     bool `json:"display_exit_fix"`
		DisplayExitFix1    bool `json:"display_exit_fix_1"`
		DisplayExitGate    bool `json:"display_exit_gate"`
		DisplayAltExitGate bool `json:"display_alternate_exit_gate"`
	} `json:"scratchpad1" scope:"stars"`

	CustomSPCs []string `json:"custom_spcs"`

	CoordinationLists []CoordinationList `json:"coordination_lists" scope:"stars"`
	VFRList           struct {
		Format string `json:"format"`
	} `json:"vfr_list" scope:"stars"`
	TABList struct {
		Format string `json:"format"`
	} `json:"tab_list" scope:"stars"`
	CoastSuspendList struct {
		Format string `json:"format"`
	} `json:"coast_suspend_list" scope:"stars"`
	MCISuppressionList struct {
		Format string `json:"format"`
	} `json:"mci_suppression_list" scope:"stars"`
	TowerList struct {
		Format string `json:"format"`
	} `json:"tower_list" scope:"stars"`
	RestrictionAreas  []av.RestrictionArea `json:"restriction_areas" scope:"stars"`
	UseLegacyFont     bool                 `json:"use_legacy_font" scope:"stars"`
	DisplayRNAVSymbol bool                 `json:"display_rnav_symbol"`
}

type FilterRegion struct {
	av.AirspaceVolume
	InvertTest bool
}

type FilterRegions []FilterRegion

type STARSControllerConfig struct {
	VideoMapNames                   []string      `json:"video_maps"`
	DefaultMaps                     []string      `json:"default_maps"`
	Center                          math.Point2LL `json:"-"`
	CenterString                    string        `json:"center"`
	Range                           float32       `json:"range"`
	MonitoredBeaconCodeBlocksString *string       `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []av.Squawk
	FlightFollowingAirspace         []av.AirspaceVolume `json:"flight_following_airspace"`
}

// STARSAreaConfig provides default configuration for all controllers
// within a TRACON area. Controller-specific configs in ControllerConfigs
// override or append these defaults.
type STARSAreaConfig struct {
	DefaultAirport                  string              `json:"default_airport,omitempty"` // CRDA default airport for this area
	VideoMapNames                   []string            `json:"video_maps,omitempty"`
	DefaultMaps                     []string            `json:"default_maps,omitempty"`
	Center                          math.Point2LL       `json:"-"`
	CenterString                    string              `json:"center,omitempty"`
	Range                           float32             `json:"range,omitempty"`
	MonitoredBeaconCodeBlocksString *string             `json:"beacon_code_blocks,omitempty"`
	MonitoredBeaconCodeBlocks       []av.Squawk         `json:"-"`
	FlightFollowingAirspace         []av.AirspaceVolume `json:"flight_following_airspace,omitempty"`
	CoordinationLists               []CoordinationList  `json:"coordination_lists,omitempty"`
	Airspace                        map[string][]av.AirspaceVolume `json:"airspace,omitempty"`
}

// DefaultAirportForController returns the CRDA default airport for a
// controller based on its area assignment. Returns empty string if no
// area config or default airport is defined.
func (fa *FacilityAdaptation) DefaultAirportForController(ctrl *av.Controller) string {
	if ctrl == nil || ctrl.Area == 0 {
		return ""
	}
	if ac, ok := fa.AreaConfigs[ctrl.Area]; ok {
		return ac.DefaultAirport
	}
	return ""
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
				return fmt.Errorf("unclosed \"[\" at offset %d", i)
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

type NASFlightPlan struct {
	ACID                  ACID
	CID                   string
	EntryFix              string
	ExitFix               string
	DepartureAirport      string
	ArrivalAirport        string // Technically not a string, but until the NAS system is fully integrated, we'll need this.
	ReceivedFrom          string // Facility that sent this FP (empty if locally created)
	ExitFixIsIntermediate bool
	Rules                 av.FlightRules
	CoordinationTime      time.Time
	PlanType              NASFlightPlanType

	AssignedSquawk av.Squawk

	TrackingController  ControlPosition // Who has the radar track
	HandoffController   ControlPosition // Handoff offered but not yet accepted
	LastLocalController ControlPosition // (May be the current controller.)
	OwningTCW           TCW             // TCW that owns this track

	AircraftCount   int
	AircraftType    string
	EquipmentSuffix string

	TypeOfFlight av.TypeOfFlight

	AssignedAltitude      int
	PerceivedAssigned     int // what the previous controller would put into the hard alt, even though the aircraft is descending via a STAR.
	InterimAlt            int
	InterimType           int
	AltitudeBlock         [2]int
	ControllerReportedAlt int
	VFROTP                bool

	RequestedAltitude     int
	PilotReportedAltitude int

	Scratchpad          string
	SecondaryScratchpad string

	PriorScratchpad          string
	PriorSecondaryScratchpad string

	RNAV bool

	Location math.Point2LL
	Route    string

	PointOutHistory             []ControlPosition
	InhibitModeCAltitudeDisplay bool
	SPCOverride                 string
	DisableMSAW                 bool
	DisableCA                   bool
	MCISuppressedCode           av.Squawk
	GlobalLeaderLineDirection   *math.CardinalOrdinalDirection
	QuickFlightPlan             bool
	HoldState                   bool
	Suspended                   bool
	CoastSuspendIndex           int

	// FIXME: the following are all used internally by NAS code. It's
	// convenient to have them here but this stuff should just be managed
	// internally there.
	ListIndex int

	// First controller in the local facility to get the track: used both
	// for /ho and for departures
	InboundHandoffController ControlPosition

	CoordinationFix     string
	ContainedFacilities []string
	RedirectedHandoff   RedirectedHandoff

	InhibitACTypeDisplay      bool
	ForceACTypeDisplayEndTime time.Time
	CWTCategory               string

	// After fps are dropped, we hold on to them for a bit before they're
	// actually deleted.
	DeleteTime time.Time

	// Used so that such FPs can associate regardless of acquisition filters.
	ManuallyCreated bool
}

type ACID string

type FlightPlanSpecifier struct {
	ACID                  util.Optional[ACID]
	EntryFix              util.Optional[string]
	ExitFix               util.Optional[string]
	ExitFixIsIntermediate util.Optional[bool]
	Rules                 util.Optional[av.FlightRules]
	CoordinationTime      util.Optional[time.Time]
	PlanType              util.Optional[NASFlightPlanType]

	SquawkAssignment         util.Optional[string]
	ImplicitSquawkAssignment util.Optional[av.Squawk] // only used when taking the track's current code

	TrackingController util.Optional[ControlPosition]

	AircraftCount   util.Optional[int]
	AircraftType    util.Optional[string]
	EquipmentSuffix util.Optional[string]

	TypeOfFlight util.Optional[av.TypeOfFlight]

	AssignedAltitude      util.Optional[int]
	InterimAlt            util.Optional[int]
	InterimType           util.Optional[string]
	AltitudeBlock         util.Optional[[2]int]
	ControllerReportedAlt util.Optional[int]
	VFROTP                util.Optional[bool]
	RequestedAltitude     util.Optional[int]
	PilotReportedAltitude util.Optional[int]

	Scratchpad          util.Optional[string]
	SecondaryScratchpad util.Optional[string]

	RNAV       util.Optional[bool]
	RNAVToggle util.Optional[bool]

	Location util.Optional[math.Point2LL]

	PointOutHistory             util.Optional[[]string]
	InhibitModeCAltitudeDisplay util.Optional[bool]
	SPCOverride                 util.Optional[string]
	DisableMSAW                 util.Optional[bool]
	DisableCA                   util.Optional[bool]
	MCISuppressedCode           util.Optional[av.Squawk]
	GlobalLeaderLineDirection   util.Optional[*math.CardinalOrdinalDirection]
	QuickFlightPlan             util.Optional[bool]
	HoldState                   util.Optional[bool]
	Suspended                   util.Optional[bool]
	CoastSuspendIndex           util.Optional[int]

	InhibitACTypeDisplay      util.Optional[bool]
	ForceACTypeDisplayEndTime util.Optional[time.Time]
}

func (s FlightPlanSpecifier) GetFlightPlan(localPool *av.LocalSquawkCodePool,
	nasPool *av.EnrouteSquawkCodePool) (NASFlightPlan, error) {
	sfp := NASFlightPlan{
		ACID:                  s.ACID.GetOr(""),
		EntryFix:              s.EntryFix.GetOr(""),
		ExitFix:               s.ExitFix.GetOr(""),
		ExitFixIsIntermediate: s.ExitFixIsIntermediate.GetOr(false),
		Rules:                 s.Rules.GetOr(av.FlightRulesVFR),
		CoordinationTime:      s.CoordinationTime.GetOr(time.Time{}),
		PlanType:              s.PlanType.GetOr(UnknownFlightPlanType),

		AircraftCount:   s.AircraftCount.GetOr(1),
		AircraftType:    s.AircraftType.GetOr(""),
		EquipmentSuffix: s.EquipmentSuffix.GetOr(""),

		TypeOfFlight:       s.TypeOfFlight.GetOr(av.FlightTypeUnknown),
		TrackingController: s.TrackingController.GetOr(""),

		AssignedAltitude:      s.AssignedAltitude.GetOr(0),
		RequestedAltitude:     s.RequestedAltitude.GetOr(0),
		PilotReportedAltitude: s.PilotReportedAltitude.GetOr(0),

		Scratchpad:          s.Scratchpad.GetOr(""),
		SecondaryScratchpad: s.SecondaryScratchpad.GetOr(""),

		RNAV: s.RNAV.GetOr(false),

		Location: s.Location.GetOr(math.Point2LL{}),

		PointOutHistory:             util.MapSlice(s.PointOutHistory.GetOr(nil), func(s string) ControlPosition { return ControlPosition(s) }),
		InhibitModeCAltitudeDisplay: s.InhibitModeCAltitudeDisplay.GetOr(false),
		SPCOverride:                 s.SPCOverride.GetOr(""),
		DisableMSAW:                 s.DisableMSAW.GetOr(false),
		DisableCA:                   s.DisableCA.GetOr(false),
		MCISuppressedCode:           s.MCISuppressedCode.GetOr(av.Squawk(0)),
		GlobalLeaderLineDirection:   s.GlobalLeaderLineDirection.GetOr(nil),
		QuickFlightPlan:             s.QuickFlightPlan.GetOr(false),
		HoldState:                   s.HoldState.GetOr(false),
		Suspended:                   s.Suspended.GetOr(false),
		CoastSuspendIndex:           s.CoastSuspendIndex.GetOr(0),

		InhibitACTypeDisplay:      s.InhibitACTypeDisplay.GetOr(false),
		ForceACTypeDisplayEndTime: s.ForceACTypeDisplayEndTime.GetOr(time.Time{}),

		ManuallyCreated: true, // Always for ones created via a fp specifier
	}

	if perf, ok := av.DB.AircraftPerformance[sfp.AircraftType]; ok {
		sfp.CWTCategory = perf.Category.CWT
	}

	// Handle beacon code assignment
	var err error
	if s.ImplicitSquawkAssignment.IsSet {
		sfp.AssignedSquawk = s.ImplicitSquawkAssignment.Get()
	} else {
		var rules av.FlightRules
		sfp.AssignedSquawk, rules, err = assignCode(s.SquawkAssignment, sfp.PlanType, sfp.Rules, localPool, nasPool)
		sfp.Rules = s.Rules.GetOr(rules) // explicit rules from caller override squawk code pool rules
	}

	if sfp.Rules != av.FlightRulesIFR && !s.DisableMSAW.IsSet {
		sfp.DisableMSAW = true
	}

	// Exit fix is shown in scratchpad for NAS flight plans.
	if sfp.PlanType == LocalEnroute && s.ExitFix.IsSet {
		sfp.Scratchpad = s.ExitFix.Get()
	}

	return sfp, err
}

// Merge incorporates set fields from other into s. Fields set in other take precedence.
func (s *FlightPlanSpecifier) Merge(other FlightPlanSpecifier) {
	// Rather than have to handle each FlightPlanSpecifier individually, we get (sort of) fancy and
	// use the reflect package to iterate over the members, check which are set through
	// util.Optional, and copy those that are. This isn't necessarily super performant, but we don't
	// need to do this too much.
	sVal := reflect.ValueOf(s).Elem()
	otherVal := reflect.ValueOf(other)
	typ := otherVal.Type()

	for i := range otherVal.NumField() {
		otherField := otherVal.Field(i)
		isSet := otherField.FieldByName("IsSet")
		if !isSet.IsValid() || isSet.Kind() != reflect.Bool {
			panic(fmt.Sprintf("FlightPlanSpecifier field %q is not a util.Optional", typ.Field(i).Name))
		}
		if isSet.Bool() {
			sVal.Field(i).Set(otherField)
		}
	}
}

func assignCode(assignment util.Optional[string], planType NASFlightPlanType, rules av.FlightRules,
	localPool *av.LocalSquawkCodePool, nasPool *av.EnrouteSquawkCodePool) (av.Squawk, av.FlightRules, error) {
	if planType == LocalEnroute {
		// Squawk assignment is either empty or a straight up code (for a quick flight plan, 5-141)
		if !assignment.IsSet || assignment.Get() == "" {
			sq, err := nasPool.Get(rand.Make())
			return sq, rules, err
		} else {
			sq, err := av.ParseSquawk(assignment.Get())
			if err == nil {
				nasPool.Take(sq)
			}
			return sq, rules, err
		}
	} else {
		return localPool.Get(assignment.GetOr(""), rules, rand.Make())
	}
}

func (fp *NASFlightPlan) Update(spec FlightPlanSpecifier, sim *Sim) (err error) {
	if spec.ACID.IsSet {
		fp.ACID = spec.ACID.Get()
	}
	if spec.PlanType.IsSet { // do before exit fix
		fp.PlanType = spec.PlanType.Get()
	}
	if spec.EntryFix.IsSet {
		fp.EntryFix = spec.EntryFix.Get()
	}
	if spec.ExitFix.IsSet {
		fp.ExitFix = spec.ExitFix.Get()
		fp.ExitFixIsIntermediate = spec.ExitFixIsIntermediate.GetOr(false)

		// Exit fix is shown in scratchpad for NAS flight plans.
		if fp.PlanType == LocalEnroute && spec.ExitFix.IsSet {
			fp.Scratchpad = spec.ExitFix.Get()
		}
	}
	if spec.Rules.IsSet {
		if spec.Rules.Get() == fp.Rules {
			// same as current, so clears flight rules, which in turn implies IFR
			fp.Rules = av.FlightRulesIFR
		} else {
			fp.Rules = spec.Rules.Get()
		}
		if !spec.DisableMSAW.IsSet {
			// If MSAW disable isn't set explicitly, it's set based on the updated flight rules.
			fp.DisableMSAW = fp.Rules != av.FlightRulesIFR
		}
	}
	if spec.CoordinationTime.IsSet {
		fp.CoordinationTime = spec.CoordinationTime.Get()
	}
	if spec.ImplicitSquawkAssignment.IsSet {
		fp.AssignedSquawk = spec.ImplicitSquawkAssignment.Get()
	} else if spec.SquawkAssignment.IsSet {
		var rules av.FlightRules
		fp.AssignedSquawk, rules, err = assignCode(spec.SquawkAssignment, fp.PlanType, fp.Rules, sim.LocalCodePool,
			sim.eramComputer().SquawkCodePool)
		if !spec.Rules.IsSet {
			// Only take the rules from the pool if no rules were given in spec.
			fp.Rules = rules
			// Disable MSAW for VFRs
			if rules != av.FlightRulesIFR && !spec.DisableMSAW.IsSet {
				fp.DisableMSAW = true
			}
		}
	}
	if spec.InterimAlt.IsSet {
		fp.InterimAlt = spec.InterimAlt.Get()
	}
	if spec.InterimType.IsSet {
		interimType := spec.InterimType.Get()
		fmt.Println("Interim type:", interimType)
		switch interimType {
		case "L":
			fp.InterimType = 2
		case "P":
			fp.InterimType = 1
		}
	}

	if spec.AircraftType.IsSet {
		fp.AircraftType = spec.AircraftType.Get()
		fp.AircraftCount = spec.AircraftCount.GetOr(1)
		fp.EquipmentSuffix = spec.EquipmentSuffix.GetOr("")

		if perf, ok := av.DB.AircraftPerformance[fp.AircraftType]; ok {
			fp.CWTCategory = perf.Category.CWT
		} else {
			fp.CWTCategory = ""
		}
	}
	if spec.TypeOfFlight.IsSet {
		fp.TypeOfFlight = spec.TypeOfFlight.Get()
	}
	if spec.TrackingController.IsSet {
		fp.TrackingController = spec.TrackingController.Get()
		fp.OwningTCW = sim.tcwForPosition(fp.TrackingController)
	}
	if spec.AssignedAltitude.IsSet {
		fp.AssignedAltitude = spec.AssignedAltitude.Get()
	}
	if spec.RequestedAltitude.IsSet {
		fp.RequestedAltitude = spec.RequestedAltitude.Get()
	}
	if spec.PilotReportedAltitude.IsSet {
		fp.PilotReportedAltitude = spec.PilotReportedAltitude.Get()
	}
	if spec.Scratchpad.IsSet {
		if spec.Scratchpad.Get() == "" {
			fp.Scratchpad = ""
			fp.PriorScratchpad = ""
		} else if fp.Scratchpad == spec.Scratchpad.Get() {
			fp.Scratchpad = fp.PriorScratchpad
		} else {
			fp.PriorScratchpad = fp.Scratchpad
			fp.Scratchpad = spec.Scratchpad.Get()
		}
	}
	if spec.SecondaryScratchpad.IsSet {
		if spec.SecondaryScratchpad.Get() == "" {
			fp.SecondaryScratchpad = ""
			fp.PriorSecondaryScratchpad = ""
		} else if fp.SecondaryScratchpad == spec.SecondaryScratchpad.Get() {
			fp.SecondaryScratchpad = fp.PriorSecondaryScratchpad
		} else {
			fp.PriorSecondaryScratchpad = fp.SecondaryScratchpad
			fp.SecondaryScratchpad = spec.SecondaryScratchpad.Get()
		}
	}
	if spec.RNAV.IsSet {
		fp.RNAV = spec.RNAV.Get()
	}
	if spec.RNAVToggle.IsSet && spec.RNAVToggle.Get() {
		fp.RNAV = !fp.RNAV
	}
	if spec.Location.IsSet {
		fp.Location = spec.Location.Get()
	}
	if spec.PointOutHistory.IsSet {
		fp.PointOutHistory = util.MapSlice(spec.PointOutHistory.Get(), func(s string) ControlPosition { return ControlPosition(s) })
	}
	if spec.InhibitModeCAltitudeDisplay.IsSet {
		fp.InhibitModeCAltitudeDisplay = spec.InhibitModeCAltitudeDisplay.Get()
	}
	if spec.SPCOverride.IsSet {
		fp.SPCOverride = spec.SPCOverride.Get()
	}
	if spec.DisableMSAW.IsSet {
		fp.DisableMSAW = spec.DisableMSAW.Get()
	}
	if spec.DisableCA.IsSet {
		fp.DisableCA = spec.DisableCA.Get()
	}
	if spec.MCISuppressedCode.IsSet {
		fp.MCISuppressedCode = spec.MCISuppressedCode.Get()
	}
	if spec.GlobalLeaderLineDirection.IsSet {
		fp.GlobalLeaderLineDirection = spec.GlobalLeaderLineDirection.Get()
	}
	if spec.QuickFlightPlan.IsSet {
		fp.QuickFlightPlan = spec.QuickFlightPlan.Get()
	}
	if spec.HoldState.IsSet {
		fp.HoldState = spec.HoldState.Get()
	}
	if spec.Suspended.IsSet {
		fp.Suspended = spec.Suspended.Get()
	}
	if spec.CoastSuspendIndex.IsSet {
		fp.CoastSuspendIndex = spec.CoastSuspendIndex.Get()
	}
	if spec.InhibitACTypeDisplay.IsSet {
		fp.InhibitACTypeDisplay = spec.InhibitACTypeDisplay.Get()
	}
	if spec.ForceACTypeDisplayEndTime.IsSet {
		fp.ForceACTypeDisplayEndTime = spec.ForceACTypeDisplayEndTime.Get()
	}
	return
}

type NASFlightPlanType int

// Flight plan types (STARS)
const (
	UnknownFlightPlanType NASFlightPlanType = iota

	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute

	// Flight plan received from an adjacent terminal facility This is a
	// flight plan that has been sent over by another STARS facility.
	RemoteNonEnroute

	// VFR interfacility flight plan entered locally for which the NAS
	// ARTCC has not returned a flight plan This is a flight plan that is
	// made by a STARS facility that gets a NAS code.
	LocalEnroute

	// Flight plan entered by TCW or flight plan from an adjacent terminal
	// that has been handed off to this STARS facility This is a flight
	// plan that is made at a STARS facility and gets a local code.
	LocalNonEnroute
)

func (fa *FacilityAdaptation) PostDeserialize(loc av.Locator, controlledAirports []string, allAirports []string, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	if ctr := fa.CenterString; ctr == "" {
		e.ErrorString("No \"center\" specified")
	} else if pos, ok := loc.Locate(ctr); !ok {
		e.ErrorString("unknown location %q specified for \"center\"", ctr)
	} else {
		fa.Center = pos
	}

	if len(fa.ControllerConfigs) > 0 {
		// Handle beacon code blocks
		for tcp, config := range fa.ControllerConfigs {
			for i := range config.FlightFollowingAirspace {
				if config.FlightFollowingAirspace[i].Id == "" {
					config.FlightFollowingAirspace[i].Id = "FF" + string(tcp) + strconv.Itoa(i+1)
				}
				if config.FlightFollowingAirspace[i].Description == "" {
					config.FlightFollowingAirspace[i].Description = "FLIGHT FOLLOWING " + string(tcp) + " " + strconv.Itoa(i+1)
				}

				config.FlightFollowingAirspace[i].PostDeserialize(loc, e)
			}

			config.MonitoredBeaconCodeBlocks = nil
			if config.MonitoredBeaconCodeBlocksString == nil {
				// None specified: 12xx block by default
				config.MonitoredBeaconCodeBlocks = append(config.MonitoredBeaconCodeBlocks, 0o12)
			} else {
				for s := range strings.SplitSeq(*config.MonitoredBeaconCodeBlocksString, ",") {
					s = strings.TrimSpace(s)
					if code, err := av.ParseSquawkOrBlock(s); err != nil {
						e.ErrorString("invalid beacon code %q in \"beacon_code_blocks\": %v", s, err)
					} else {
						config.MonitoredBeaconCodeBlocks = append(config.MonitoredBeaconCodeBlocks, code)
					}
				}
			}
		}
	}

	// Process area configs similarly to controller configs.
	for areaNum, ac := range fa.AreaConfigs {
		e.Push(fmt.Sprintf("area_configs[%d]", areaNum))

		for i := range ac.FlightFollowingAirspace {
			if ac.FlightFollowingAirspace[i].Id == "" {
				ac.FlightFollowingAirspace[i].Id = fmt.Sprintf("FFA%d-%d", areaNum, i+1)
			}
			if ac.FlightFollowingAirspace[i].Description == "" {
				ac.FlightFollowingAirspace[i].Description = fmt.Sprintf("FLIGHT FOLLOWING AREA %d %d", areaNum, i+1)
			}
			ac.FlightFollowingAirspace[i].PostDeserialize(loc, e)
		}

		if ac.MonitoredBeaconCodeBlocksString != nil {
			for s := range strings.SplitSeq(*ac.MonitoredBeaconCodeBlocksString, ",") {
				s = strings.TrimSpace(s)
				if code, err := av.ParseSquawkOrBlock(s); err != nil {
					e.ErrorString("invalid beacon code %q in \"beacon_code_blocks\": %v", s, err)
				} else {
					ac.MonitoredBeaconCodeBlocks = append(ac.MonitoredBeaconCodeBlocks, code)
				}
			}
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
					volumes[i].Id = fmt.Sprintf("A%d-%s-%d", areaNum, name, i+1)
				}
			}
			ac.Airspace[name] = volumes
		}

		e.Pop()
	}

	for _, sp := range fa.Scratchpads {
		if !fa.CheckScratchpad(sp) {
			e.ErrorString("%s: invalid scratchpad in \"scratchpads\"", sp)
		}
	}

	makeCircleAirportFilters := func(id string, description string, radius float32,
		floor int, ceiling int, airports []string) FilterRegions {
		var regions FilterRegions
		for _, apname := range airports {
			ap, ok := av.DB.Airports[apname]
			if !ok {
				e.ErrorString("Airport %q not found", apname)
			}
			if len(apname) == 4 {
				apname = apname[1:]
			}
			regions = append(regions, FilterRegion{
				AirspaceVolume: av.AirspaceVolume{
					Id:          id + apname,
					Description: description + " " + apname,
					Type:        av.AirspaceVolumeCircle,
					Floor:       0,
					Ceiling:     ap.Elevation + ceiling,
					Center:      ap.Location,
					Radius:      radius,
				},
			})
		}
		return regions
	}

	// (Re)compute this ourselves rather than taking it as an argument
	// since the one in ScenarioGroup depends on our initializing Center
	// which just happened above.
	nmPerLongitude := math.NMPerLongitudeAt(fa.Center)

	makePolygonAirportFilters := func(id string, description string, delta float32,
		floor int, ceiling int, airports []string) FilterRegions {
		var regions FilterRegions
		for _, apname := range airports {
			ap, ok := av.DB.Airports[apname]
			if !ok {
				e.ErrorString("Airport %q not found", apname)
			}
			if len(apname) == 4 {
				apname = apname[1:]
			}

			p := util.MapSlice(ap.Runways, func(r av.Runway) [2]float32 { return math.LL2NM(r.Threshold, nmPerLongitude) })
			var hull [][2]float32

			if len(p) == 2 {
				// Single runway so compute an OBB directly.
				v := math.Normalize2f(math.Sub2f(p[1], p[0]))
				v = math.Scale2f(v, delta)
				nv := math.Scale2f(v, -1)
				vp := [2]float32{v[1], -v[0]} // perp
				nvp := math.Scale2f(vp, -1)

				hull = [][2]float32{
					math.Add2f(p[0], math.Add2f(nv, vp)),
					math.Add2f(p[1], math.Add2f(v, vp)),
					math.Add2f(p[1], math.Add2f(v, nvp)),
					math.Add2f(p[0], math.Add2f(nv, nvp))}
			} else {
				// Convex hull of the runway threshold points
				hull = math.ConvexHull(p)

				// Expand the hull by delta: hacky polygon dilation--
				// compute the average point as a center and then offset
				// each away from it.
				var c [2]float32
				for _, p := range hull {
					c = math.Add2f(c, p)
				}
				c = math.Scale2f(c, 1/float32(len(hull)))
				for i := range hull {
					v := math.Sub2f(hull[i], c)
					hull[i] = math.Add2f(hull[i], math.Scale2f(v, delta))
				}
			}

			// Back to lat-long for the AirspaceVolume
			pll := util.MapSlice(hull, func(p [2]float32) math.Point2LL { return math.NM2LL(p, nmPerLongitude) })

			regions = append(regions, FilterRegion{
				AirspaceVolume: av.AirspaceVolume{
					Id:          id + apname,
					Description: description + " " + apname,
					Type:        av.AirspaceVolumePolygon,
					Floor:       0,
					Ceiling:     ap.Elevation + ceiling,
					Vertices:    pll,
				},
			})
		}
		return regions
	}

	if len(fa.Filters.ArrivalDrop) == 0 {
		fa.Filters.ArrivalDrop = makePolygonAirportFilters("DROP", "ARRIVAL DROP", 0.35, 0, 500, controlledAirports)
	}
	if len(fa.Filters.Departure) == 0 {
		fa.Filters.Departure = makePolygonAirportFilters("DEP", "DEPARTURE", 0.5, 0, 500, controlledAirports)
	}
	if len(fa.Filters.InhibitCA) == 0 {
		fa.Filters.InhibitCA = makeCircleAirportFilters("NOCA", "CONFLICT SUPPRESS", 5, 0, 3000, controlledAirports)
	}
	if len(fa.Filters.InhibitMSAW) == 0 {
		fa.Filters.InhibitMSAW = makeCircleAirportFilters("NOSA", "MSAW SUPPRESS", 5, 0, 3000, controlledAirports)
	}
	if len(fa.Filters.SurfaceTracking) == 0 {
		fa.Filters.SurfaceTracking = makePolygonAirportFilters("SURF", "SURFACE TRACKING", 0.15, 0, 200, allAirports)
	}

	checkFilter := func(f FilterRegions, name string) {
		ids := make(map[string]any)
		for i, filt := range f {
			e.Push(filt.Description)
			f[i].PostDeserialize(loc, e)

			if _, ok := ids[filt.Id]; ok {
				e.ErrorString("Quicklook filter \"id\"s must be unique: %q was repeated", filt.Id)
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
	checkFilter(fa.Filters.Quicklook, "quicklook")
	checkFilter(fa.Filters.SecondaryDrop, "secondary_drop")
	checkFilter(fa.Filters.SurfaceTracking, "surface_tracking")

	// This one kicks in when they exit the "inside" region defined by the volume.
	for i := range fa.Filters.SecondaryDrop {
		fa.Filters.SecondaryDrop[i].InvertTest = true
	}

	// Quick FP ACID
	e.Push("\"flight_plan\"")
	fa.FlightPlan.QuickACID = strings.ToUpper(fa.FlightPlan.QuickACID)
	if qa := fa.FlightPlan.QuickACID; qa == "" {
		fa.FlightPlan.QuickACID = "VCE"
	} else {
		if qa[0] < 'A' || qa[0] > 'Z' {
			e.ErrorString("\"quick_acid\" must start with a letter")
		}
		if len(qa) > 3 {
			e.ErrorString("\"quick_acid\" can't be more than three characters")
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

	e.Push("\"tab_list\"")
	if fa.TABList.Format == "" {
		fa.TABList.Format = "[INDEX] [ACID_MSAWCA][DUPE_BEACON] [BEACON] [DEP_EXIT_FIX]"
	}
	if err := validateListFormat(fa.TABList.Format, "DUPE_BEACON"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.TABList.Format, err)
	}
	e.Pop()

	e.Push("\"vfr_list\"")
	if fa.VFRList.Format == "" {
		fa.VFRList.Format = "[INDEX] [ACID_MSAWCA][BEACON]"
	}
	if err := validateListFormat(fa.VFRList.Format); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.VFRList.Format, err)
	}
	e.Pop()

	e.Push("\"coast_suspend_list\"")
	if fa.CoastSuspendList.Format == "" {
		fa.CoastSuspendList.Format = "[INDEX] [ACID] S [BEACON] [ALT]"
	}
	if err := validateListFormat(fa.CoastSuspendList.Format, "ALT"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.CoastSuspendList.Format, err)
	}
	e.Pop()

	e.Push("\"mci_suppression_list\"")
	if fa.MCISuppressionList.Format == "" {
		fa.MCISuppressionList.Format = "[ACID] [BEACON]  [SUPP_BEACON]"
	}
	if err := validateListFormat(fa.MCISuppressionList.Format, "SUPP_BEACON"); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.MCISuppressionList.Format, err)
	}
	e.Pop()

	e.Push("\"tower_list\"")
	if fa.TowerList.Format == "" {
		fa.TowerList.Format = "[ACID] [ACTYPE]"
	}
	if err := validateListFormat(fa.TowerList.Format); err != nil {
		e.ErrorString("Invalid format string %q: %v", fa.TowerList.Format, err)
	}
	e.Pop()

	e.Push("\"coordination_lists\"")
	for i, cl := range fa.CoordinationLists {
		if cl.Format == "" {
			// Default format
			fa.CoordinationLists[i].Format = "[INDEX][ACKED]    [ACID] [ACTYPE] [BEACON]   [EXIT_FIX] [REQ_ALT]"
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
	if fa.AllowLongScratchpad && lc > 4 {
		return false
	} else if !fa.AllowLongScratchpad && lc > 3 {
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

func (r FilterRegion) Inside(p math.Point2LL, alt int) bool {
	return r.AirspaceVolume.Inside(p, alt) != r.InvertTest
}

func (r FilterRegions) Inside(p math.Point2LL, alt int) bool {
	return slices.ContainsFunc(r, func(r FilterRegion) bool { return r.Inside(p, alt) })
}

func (r FilterRegions) HaveId(s string) bool {
	return slices.ContainsFunc(r, func(r FilterRegion) bool { return s == r.Id })
}
