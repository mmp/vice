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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/klauspost/compress/zstd"
)

type RedirectedHandoff struct {
	OriginalOwner string   // Controller callsign
	Redirector    []string // Controller callsign
	RedirectedTo  string   // Controller callsign
}

func (rd *RedirectedHandoff) GetLastRedirector() string {
	if length := len(rd.Redirector); length > 0 {
		return rd.Redirector[length-1]
	} else {
		return ""
	}
}

func (rd *RedirectedHandoff) ShowRDIndicator(callsign string, RDIndicatorEnd time.Time) bool {
	// Show "RD" to the redirect target, last redirector until the RD is accepted.
	// Show "RD" to the original owner up to 30 seconds after the RD is accepted.
	return rd.RedirectedTo == callsign || rd.GetLastRedirector() == callsign ||
		rd.OriginalOwner == callsign || time.Until(RDIndicatorEnd) > 0
}

func (rd *RedirectedHandoff) ShouldFallbackToHandoff(ctrl, octrl string) bool {
	// True if the 2nd redirector redirects back to the 1st redirector
	return (len(rd.Redirector) == 1 || (len(rd.Redirector) > 1) && rd.Redirector[1] == ctrl) && octrl == rd.Redirector[0]
}

func (rd *RedirectedHandoff) AddRedirector(ctrl *av.Controller) {
	if len(rd.Redirector) == 0 || rd.Redirector[len(rd.Redirector)-1] != ctrl.Id() {
		// Don't append the same controller multiple times
		// (the case in which the last redirector recalls and then redirects again)
		rd.Redirector = append(rd.Redirector, ctrl.Id())
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

	CommandBuffer renderer.CommandBuffer
}

// This should match VideoMapLibrary in dat2vice
type VideoMapLibrary struct {
	Maps []VideoMap
}

// VideoMapManifest stores which maps are available in a video map file and
// is also able to provide the video map file's hash.
type VideoMapManifest struct {
	names      map[string]interface{}
	filesystem fs.FS
	filename   string
}

func CheckVideoMapManifest(filename string, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

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

	var names map[string]interface{}
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
	_, ok := v.names[s]
	return ok
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
		_, _ = br.Seek(0, io.SeekStart)
		if zr != nil {
			_ = zr.Reset(br)
		}
		if err := gob.NewDecoder(r).Decode(&vmf.Maps); err != nil {
			return nil, err
		}
	}

	// Convert the line specifications into command buffers for drawing.
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	for i, m := range vmf.Maps {
		ld.Reset()

		for _, lines := range m.Lines {
			// Slightly annoying: the line vertices are stored with
			// Point2LLs but AddLineStrip() expects [2]float32s.
			fl := util.MapSlice(lines, func(p math.Point2LL) [2]float32 { return p })
			ld.AddLineStrip(fl)
		}
		ld.GenerateCommands(&m.CommandBuffer)

		// Clear out Lines so that the memory can be reclaimed since they
		// aren't needed any more.
		m.Lines = nil
		vmf.Maps[i] = m
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

type STARSFacilityAdaptation struct {
	AirspaceAwareness   []AirspaceAwareness               `json:"airspace_awareness"`
	ForceQLToSelf       bool                              `json:"force_ql_self"`
	AllowLongScratchpad bool                              `json:"allow_long_scratchpad"`
	VideoMapNames       []string                          `json:"stars_maps"`
	VideoMapLabels      map[string]string                 `json:"map_labels"`
	ControllerConfigs   map[string]*STARSControllerConfig `json:"controller_configs"`
	RadarSites          map[string]*av.RadarSite          `json:"radar_sites"`
	Center              math.Point2LL                     `json:"-"`
	CenterString        string                            `json:"center"`
	Range               float32                           `json:"range"`
	Scratchpads         map[string]string                 `json:"scratchpads"`
	SignificantPoints   map[string]SignificantPoint       `json:"significant_points"`
	Altimeters          []string                          `json:"altimeters"`

	// Airpsace filters
	Filters struct {
		ArrivalAcquisition   FilterRegions `json:"arrival_acquisition"`
		ArrivalDrop          FilterRegions `json:"arrival_drop"`
		DepartureAcquisition FilterRegions `json:"departure_acquisition"`
		InhibitCA            FilterRegions `json:"inhibit_ca"`
		InhibitMSAW          FilterRegions `json:"inhibit_msaw"`
		Quicklook            FilterRegions `json:"quicklook"`
		SecondaryDrop        FilterRegions `json:"secondary_drop"`
		SurfaceTracking      FilterRegions `json:"surface_tracking"`
		VFRInhibit           FilterRegions `json:"vfr_inhibit"`
	} `json:"filters"`

	MonitoredBeaconCodeBlocksString *string `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []av.Squawk

	VideoMapFile      string                        `json:"video_map_file"`
	CoordinationFixes map[string]av.AdaptationFixes `json:"coordination_fixes"`
	SingleCharAIDs    map[string]string             `json:"single_char_aids"` // Char to airport
	KeepLDB           bool                          `json:"keep_ldb"`
	FullLDBSeconds    int                           `json:"full_ldb_seconds"`

	SSRCodes av.LocalSquawkCodePoolSpecifier `json:"ssr_codes"`

	HandoffAcceptFlashDuration int  `json:"handoff_acceptance_flash_duration"`
	DisplayHOFacilityOnly      bool `json:"display_handoff_facility_only"`
	HOSectorDisplayDuration    int  `json:"handoff_sector_display_duration"`

	FlightPlan struct {
		QuickACID          string            `json:"quick_acid"`
		ACIDExpansions     map[string]string `json:"acid_expansions"`
		ModifyAfterDisplay bool              `json:"modify_after_display"`
	} `json:"flight_plan"`

	PDB struct {
		ShowScratchpad2   bool `json:"show_scratchpad2"`
		HideGroundspeed   bool `json:"hide_gs"`
		ShowAircraftType  bool `json:"show_aircraft_type"`
		SplitGSAndCWT     bool `json:"split_gs_and_cwt"`
		DisplayCustomSPCs bool `json:"display_custom_spcs"`
	} `json:"pdb"`

	FDB struct {
		DisplayRequestedAltitude bool `json:"display_requested_altitude"`
		Scratchpad2OnLine3       bool `json:"scratchpad2_on_line3"`
	} `json:"fdb"`

	Scratchpad1 struct {
		DisplayExitFix     bool `json:"display_exit_fix"`
		DisplayExitFix1    bool `json:"display_exit_fix_1"`
		DisplayExitGate    bool `json:"display_exit_gate"`
		DisplayAltExitGate bool `json:"display_alternate_exit_gate"`
	} `json:"scratchpad1"`

	CustomSPCs []string `json:"custom_spcs"`

	CoordinationLists []CoordinationList   `json:"coordination_lists"`
	RestrictionAreas  []av.RestrictionArea `json:"restriction_areas"`
	UseLegacyFont     bool                 `json:"use_legacy_font"`
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

type CoordinationList struct {
	Name          string   `json:"name"`
	Id            string   `json:"id"`
	Airports      []string `json:"airports"`
	YellowEntries bool     `json:"yellow_entries"`
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

type STARSFlightPlan struct {
	ACID                  ACID
	CID                   string
	EntryFix              string
	ExitFix               string
	ExitFixIsIntermediate bool
	Rules                 av.FlightRules
	CoordinationTime      time.Time
	PlanType              STARSFlightPlanType

	AssignedSquawk av.Squawk

	TrackingController     string // Who has the radar track
	ControllingController  string // Who has control; not necessarily the same as TrackingController
	HandoffTrackController string // Handoff offered but not yet accepted
	LastLocalController    string // (May be the current controller.)

	AircraftCount   int
	AircraftType    string
	EquipmentSuffix string

	TypeOfFlight av.TypeOfFlight

	AssignedAltitude      int
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

	PointOutHistory             []string
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
	InboundHandoffController string

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

type STARSFlightPlanSpecifier struct {
	ACID                  util.Optional[ACID]
	EntryFix              util.Optional[string]
	ExitFix               util.Optional[string]
	ExitFixIsIntermediate util.Optional[bool]
	Rules                 util.Optional[av.FlightRules]
	CoordinationTime      util.Optional[time.Time]
	PlanType              util.Optional[STARSFlightPlanType]

	SquawkAssignment         util.Optional[string]
	ImplicitSquawkAssignment util.Optional[av.Squawk] // only used when taking the track's current code

	TrackingController util.Optional[string]

	AircraftCount   util.Optional[int]
	AircraftType    util.Optional[string]
	EquipmentSuffix util.Optional[string]

	TypeOfFlight util.Optional[av.TypeOfFlight]

	AssignedAltitude      util.Optional[int]
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

func (s STARSFlightPlanSpecifier) GetFlightPlan(localPool *av.LocalSquawkCodePool,
	nasPool *av.EnrouteSquawkCodePool) (STARSFlightPlan, error) {
	sfp := STARSFlightPlan{
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

		PointOutHistory:             s.PointOutHistory.GetOr(nil),
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

func assignCode(assignment util.Optional[string], planType STARSFlightPlanType, rules av.FlightRules,
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

func (fp *STARSFlightPlan) Update(spec STARSFlightPlanSpecifier, localPool *av.LocalSquawkCodePool,
	nasPool *av.EnrouteSquawkCodePool) (err error) {
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
		fp.AssignedSquawk, rules, err = assignCode(spec.SquawkAssignment, fp.PlanType, fp.Rules, localPool, nasPool)
		if !spec.Rules.IsSet {
			// Only take the rules from the pool if no rules were given in spec.
			fp.Rules = rules
			// Disable MSAW for VFRs
			if rules != av.FlightRulesIFR && !spec.DisableMSAW.IsSet {
				fp.DisableMSAW = true
			}
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
		if fp.Scratchpad == spec.Scratchpad.Get() {
			fp.Scratchpad = fp.PriorScratchpad
		} else {
			fp.PriorScratchpad = fp.Scratchpad
			fp.Scratchpad = spec.Scratchpad.Get()
		}
	}
	if spec.SecondaryScratchpad.IsSet {
		if fp.SecondaryScratchpad == spec.SecondaryScratchpad.Get() {
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
		fp.PointOutHistory = spec.PointOutHistory.Get()
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

type STARSFlightPlanType int

// Flight plan types (STARS)
const (
	UnknownFlightPlanType STARSFlightPlanType = iota

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

func (fa *STARSFacilityAdaptation) PostDeserialize(loc av.Locator, controlledAirports []string, allAirports []string, e *util.ErrorLogger) {
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
					config.FlightFollowingAirspace[i].Id = "FF" + tcp + strconv.Itoa(i+1)
				}
				if config.FlightFollowingAirspace[i].Description == "" {
					config.FlightFollowingAirspace[i].Description = "FLIGHT FOLLOWING " + tcp + " " + strconv.Itoa(i+1)
				}

				config.FlightFollowingAirspace[i].PostDeserialize(loc, e)
			}

			config.MonitoredBeaconCodeBlocks = nil
			if config.MonitoredBeaconCodeBlocksString == nil {
				// None specified: 12xx block by default
				config.MonitoredBeaconCodeBlocks = append(config.MonitoredBeaconCodeBlocks, 0o12)
			} else {
				for _, s := range strings.Split(*config.MonitoredBeaconCodeBlocksString, ",") {
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
	nmPerLongitude := 60 * math.Cos(math.Radians(fa.Center[1]))

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
	if len(fa.Filters.DepartureAcquisition) == 0 {
		fa.Filters.DepartureAcquisition = makePolygonAirportFilters("ACQ", "DEPARTURE ACQUISITION", 0.35, 0, 500, controlledAirports)
	}
	if len(fa.Filters.InhibitCA) == 0 {
		fa.Filters.InhibitCA = makeCircleAirportFilters("NOCA", "CONFLICT SUPPRESS", 5, 0, 3000, controlledAirports)
	}
	if len(fa.Filters.InhibitMSAW) == 0 {
		fa.Filters.InhibitMSAW = makeCircleAirportFilters("NOSA", "MSAW SUPPRESS", 5, 0, 3000, controlledAirports)
	}
	if len(fa.Filters.SurfaceTracking) == 0 {
		fa.Filters.SurfaceTracking = makePolygonAirportFilters("SURF", "SURFACE TRACKING", 0.15, 0, 250, allAirports)
	}

	checkFilter := func(f FilterRegions, name string) {
		ids := make(map[string]interface{})
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
	checkFilter(fa.Filters.ArrivalAcquisition, "arrival_acquisition")
	checkFilter(fa.Filters.ArrivalDrop, "arrival_drop")
	checkFilter(fa.Filters.DepartureAcquisition, "departure_acquisition")
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
}

func (fa STARSFacilityAdaptation) CheckScratchpad(sp string) bool {
	lc := len([]rune(sp))

	// 5-148
	if fa.AllowLongScratchpad && lc > 4 {
		return false
	} else if !fa.AllowLongScratchpad && lc > 3 {
		return false
	}

	// Make sure it's only allowed characters
	const STARSTriangleCharacter = string(rune(0x80))
	allowedCharacters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./*" + STARSTriangleCharacter
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
