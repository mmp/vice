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
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
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
	InhibitCAVolumes    []av.AirspaceVolume               `json:"inhibit_ca_volumes"`
	RadarSites          map[string]*av.RadarSite          `json:"radar_sites"`
	Center              math.Point2LL                     `json:"-"`
	CenterString        string                            `json:"center"`
	Range               float32                           `json:"range"`
	Scratchpads         map[string]string                 `json:"scratchpads"`
	SignificantPoints   map[string]SignificantPoint       `json:"significant_points"`
	Altimeters          []string                          `json:"altimeters"`

	MonitoredBeaconCodeBlocksString *string `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []av.Squawk

	VideoMapFile      string                        `json:"video_map_file"`
	CoordinationFixes map[string]av.AdaptationFixes `json:"coordination_fixes"`
	SingleCharAIDs    map[string]string             `json:"single_char_aids"` // Char to airport
	BeaconBank        int                           `json:"beacon_bank"`
	KeepLDB           bool                          `json:"keep_ldb"`
	FullLDBSeconds    int                           `json:"full_ldb_seconds"`

	HandoffAcceptFlashDuration int  `json:"handoff_acceptance_flash_duration"`
	DisplayHOFacilityOnly      bool `json:"display_handoff_facility_only"`
	HOSectorDisplayDuration    int  `json:"handoff_sector_display_duration"`

	FlightPlan struct {
		QuickACID      string            `json:"quick_acid"`
		ACIDExpansions map[string]string `json:"acid_expansions"`
	} `json:"flight_plan"`

	PDB struct {
		ShowScratchpad2   bool `json:"show_scratchpad2"`
		HideGroundspeed   bool `json:"hide_gs"`
		ShowAircraftType  bool `json:"show_aircraft_type"`
		SplitGSAndCWT     bool `json:"split_gs_and_cwt"`
		DisplayCustomSPCs bool `json:"display_custom_spcs"`
	} `json:"pdb"`

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

type STARSControllerConfig struct {
	VideoMapNames                   []string      `json:"video_maps"`
	DefaultMaps                     []string      `json:"default_maps"`
	Center                          math.Point2LL `json:"-"`
	CenterString                    string        `json:"center"`
	Range                           float32       `json:"range"`
	MonitoredBeaconCodeBlocksString *string       `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []av.Squawk
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
	EntryFix              string
	ExitFix               string
	ExitFixIsIntermediate bool
	Rules                 av.FlightRules
	ETAOrPTD              time.Time // predicted time of arrival / proposed time of departure

	AssignedSquawk av.Squawk

	TrackingController     string // Who has the radar track
	ControllingController  string // Who has control; not necessarily the same as TrackingController
	HandoffTrackController string // Handoff offered but not yet accepted

	AircraftCount   int
	AircraftType    string
	EquipmentSuffix string
	CWTCategory     string

	TypeOfFlight      av.TypeOfFlight
	InitialController string // For abbreviated FPs

	AssignedAltitude      int
	RequestedAltitude     int
	PilotReportedAltitude int

	Scratchpad          string
	SecondaryScratchpad string

	RNAV bool

	PointOutHistory             []string
	InhibitModeCAltitudeDisplay bool
	SPCOverride                 string
	DisableMSAW                 bool
	DisableCA                   bool
	MCISuppressedCode           av.Squawk
	GlobalLeaderLineDirection   *math.CardinalOrdinalDirection

	// FIXME: these are used internally by NAS code. It's convenient to
	// have them here but this stuff should just be managed internally
	// there.
	ListIndex           int
	CoordinationTime    CoordinationTime
	CoordinationFix     string
	ContainedFacilities []string
	AutoAssociate       bool
	RedirectedHandoff   RedirectedHandoff
}

type ACID string

type STARSFlightPlanSpecifier struct {
	ACID                  util.Optional[ACID]
	EntryFix              util.Optional[string]
	ExitFix               util.Optional[string]
	ExitFixIsIntermediate util.Optional[bool]
	Rules                 util.Optional[av.FlightRules]
	ETAOrPTD              util.Optional[time.Time]

	AssignedSquawk util.Optional[av.Squawk]

	AircraftCount   util.Optional[int]
	AircraftType    util.Optional[string]
	EquipmentSuffix util.Optional[string]
	CWTCategory     util.Optional[string]

	TypeOfFlight      util.Optional[av.TypeOfFlight]
	InitialController util.Optional[string]

	AssignedAltitude      util.Optional[int]
	RequestedAltitude     util.Optional[int]
	PilotReportedAltitude util.Optional[int]

	Scratchpad          util.Optional[string]
	SecondaryScratchpad util.Optional[string]

	RNAV util.Optional[bool]

	Location util.Optional[math.Point2LL]

	PointOutHistory             util.Optional[[]string]
	InhibitModeCAltitudeDisplay util.Optional[bool]
	SPCOverride                 util.Optional[string]
	DisableMSAW                 util.Optional[bool]
	DisableCA                   util.Optional[bool]
	MCISuppressedCode           util.Optional[av.Squawk]
	GlobalLeaderLineDirection   util.Optional[*math.CardinalOrdinalDirection]

	// Specifiers used when creating flight plans but not held on beyond
	// that.
	AssignIFRSquawk bool
	AssignVFRSquawk bool
	AutoAssociate   bool
	CreateQuick     bool
}

func (s STARSFlightPlanSpecifier) GetFlightPlan() STARSFlightPlan {
	return STARSFlightPlan{
		ACID:                  s.ACID.GetOr(""),
		EntryFix:              s.EntryFix.GetOr(""),
		ExitFix:               s.ExitFix.GetOr(""),
		ExitFixIsIntermediate: s.ExitFixIsIntermediate.GetOr(false),
		Rules:                 s.Rules.GetOr(av.FlightRulesUnknown),
		ETAOrPTD:              s.ETAOrPTD.GetOr(time.Time{}),

		AssignedSquawk: s.AssignedSquawk.GetOr(av.Squawk(0)),

		AircraftCount:   s.AircraftCount.GetOr(1),
		AircraftType:    s.AircraftType.GetOr(""),
		EquipmentSuffix: s.EquipmentSuffix.GetOr(""),
		CWTCategory:     s.CWTCategory.GetOr(""),

		TypeOfFlight:      s.TypeOfFlight.GetOr(av.FlightTypeUnknown),
		InitialController: s.InitialController.GetOr(""),

		AssignedAltitude:      s.AssignedAltitude.GetOr(0),
		RequestedAltitude:     s.RequestedAltitude.GetOr(0),
		PilotReportedAltitude: s.PilotReportedAltitude.GetOr(0),

		Scratchpad:          s.Scratchpad.GetOr(""),
		SecondaryScratchpad: s.SecondaryScratchpad.GetOr(""),

		RNAV: s.RNAV.GetOr(false),

		PointOutHistory:             s.PointOutHistory.GetOr(nil),
		InhibitModeCAltitudeDisplay: s.InhibitModeCAltitudeDisplay.GetOr(false),
		SPCOverride:                 s.SPCOverride.GetOr(""),
		DisableMSAW:                 s.DisableMSAW.GetOr(false),
		DisableCA:                   s.DisableCA.GetOr(false),
		MCISuppressedCode:           s.MCISuppressedCode.GetOr(av.Squawk(0)),
		GlobalLeaderLineDirection:   s.GlobalLeaderLineDirection.GetOr(nil),

		AutoAssociate: s.AutoAssociate,
	}
}

func (fp *STARSFlightPlan) Update(spec STARSFlightPlanSpecifier) {
	if spec.ACID.IsSet {
		fp.ACID = spec.ACID.Get()
	}
	if spec.EntryFix.IsSet {
		fp.EntryFix = spec.EntryFix.Get()
	}
	if spec.ExitFix.IsSet {
		fp.ExitFix = spec.ExitFix.Get()
		fp.ExitFixIsIntermediate = spec.ExitFixIsIntermediate.GetOr(false)
	}
	if spec.Rules.IsSet {
		fp.Rules = spec.Rules.Get()
	}
	if spec.ETAOrPTD.IsSet {
		fp.ETAOrPTD = spec.ETAOrPTD.Get()
	}

	if spec.AssignedSquawk.IsSet {
		fp.AssignedSquawk = spec.AssignedSquawk.Get()
	}

	if spec.AircraftType.IsSet {
		fp.AircraftType = spec.AircraftType.Get()
		fp.AircraftCount = spec.AircraftCount.GetOr(1)
		fp.EquipmentSuffix = spec.EquipmentSuffix.GetOr("")
		fp.CWTCategory = spec.CWTCategory.GetOr("")
	}
	if spec.TypeOfFlight.IsSet {
		fp.TypeOfFlight = spec.TypeOfFlight.Get()
	}
	if spec.InitialController.IsSet {
		fp.InitialController = spec.InitialController.Get()
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
		fp.Scratchpad = spec.Scratchpad.Get()
	}
	if spec.SecondaryScratchpad.IsSet {
		fp.SecondaryScratchpad = spec.SecondaryScratchpad.Get()
	}
	if spec.RNAV.IsSet {
		fp.RNAV = spec.RNAV.Get()
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
}

type CoordinationTime struct {
	Time time.Time
	Type string // A for arrivals, P for Departures, E for overflights
}

type STARSFlightPlanType int

// Flight plan types (STARS)
const (
	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute STARSFlightPlanType = iota

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
