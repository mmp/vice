// pkg/aviation/stars.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

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

	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
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

func (rd *RedirectedHandoff) AddRedirector(ctrl *Controller) {
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
	InhibitCAVolumes    []AirspaceVolume                  `json:"inhibit_ca_volumes"`
	RadarSites          map[string]*RadarSite             `json:"radar_sites"`
	Center              math.Point2LL                     `json:"-"`
	CenterString        string                            `json:"center"`
	Range               float32                           `json:"range"`
	Scratchpads         map[string]string                 `json:"scratchpads"`
	SignificantPoints   map[string]SignificantPoint       `json:"significant_points"`
	Altimeters          []string                          `json:"altimeters"`

	MonitoredBeaconCodeBlocksString *string `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []Squawk

	VideoMapFile      string                     `json:"video_map_file"`
	CoordinationFixes map[string]AdaptationFixes `json:"coordination_fixes"`
	SingleCharAIDs    map[string]string          `json:"single_char_aids"` // Char to airport
	BeaconBank        int                        `json:"beacon_bank"`
	KeepLDB           bool                       `json:"keep_ldb"`
	FullLDBSeconds    int                        `json:"full_ldb_seconds"`

	HandoffAcceptFlashDuration int  `json:"handoff_acceptance_flash_duration"`
	DisplayHOFacilityOnly      bool `json:"display_handoff_facility_only"`
	HOSectorDisplayDuration    int  `json:"handoff_sector_display_duration"`

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

	CoordinationLists []CoordinationList `json:"coordination_lists"`
	RestrictionAreas  []RestrictionArea  `json:"restriction_areas"`
	UseLegacyFont     bool               `json:"use_legacy_font"`
}

type STARSControllerConfig struct {
	VideoMapNames                   []string      `json:"video_maps"`
	DefaultMaps                     []string      `json:"default_maps"`
	Center                          math.Point2LL `json:"-"`
	CenterString                    string        `json:"center"`
	Range                           float32       `json:"range"`
	MonitoredBeaconCodeBlocksString *string       `json:"beacon_code_blocks"`
	MonitoredBeaconCodeBlocks       []Squawk
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
	*FlightPlan
	FlightPlanType      int
	CoordinationTime    CoordinationTime
	CoordinationFix     string
	ContainedFacilities []string
	Altitude            string
	SP1                 string
	SP2                 string
	InitialController   string // For abbreviated FPs
}

type CoordinationTime struct {
	Time time.Time
	Type string // A for arrivals, P for Departures, E for overflights
}

// Flight plan types (STARS)
const (
	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute = iota

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

func (fa *STARSFacilityAdaptation) GetCoordinationFix(fp *STARSFlightPlan, acpos math.Point2LL, waypoints []Waypoint) (string, bool) {
	for fix, adaptationFixes := range fa.CoordinationFixes {
		if adaptationFix, err := adaptationFixes.Fix(fp.Altitude); err == nil {
			if adaptationFix.Type == ZoneBasedFix {
				// Exclude zone based fixes for now. They come in after the route-based fix
				continue
			}

			// FIXME (as elsewhere): make this more robust
			if strings.Contains(fp.Route, fix) {
				return fix, true
			}

			// FIXME: why both this and checking fp.Route?
			for _, waypoint := range waypoints {
				if waypoint.Fix == fix {
					return fix, true
				}
			}
		}

	}

	var closestFix string
	minDist := float32(1e30)
	for fix, adaptationFixes := range fa.CoordinationFixes {
		for _, adaptationFix := range adaptationFixes {
			if adaptationFix.Type == ZoneBasedFix {
				if loc, ok := DB.LookupWaypoint(fix); !ok {
					// FIXME: check this (if it isn't already) at scenario load time.
					panic(fix + ": not found in fixes database")
				} else if dist := math.NMDistance2LL(acpos, loc); dist < minDist {
					minDist = dist
					closestFix = fix
				}
			}
		}
	}

	return closestFix, closestFix != ""
}

func MakeSTARSFlightPlan(fp *FlightPlan) *STARSFlightPlan {
	return &STARSFlightPlan{
		FlightPlan: fp,
		Altitude:   fmt.Sprint(fp.Altitude),
	}
}

func (fp *STARSFlightPlan) SetCoordinationFix(fa STARSFacilityAdaptation, ac *Aircraft, simTime time.Time) error {
	cf, ok := fa.GetCoordinationFix(fp, ac.Position(), ac.Waypoints())
	if !ok {
		return ErrNoCoordinationFix
	}
	fp.CoordinationFix = cf

	if dist, err := ac.DistanceAlongRoute(cf); err == nil {
		m := dist / float32(fp.CruiseSpeed) * 60
		fp.CoordinationTime = CoordinationTime{
			Time: simTime.Add(time.Duration(m * float32(time.Minute))),
		}
	} else { // zone based fixes.
		loc, ok := DB.LookupWaypoint(fp.CoordinationFix)
		if !ok {
			return ErrNoCoordinationFix
		}

		dist := math.NMDistance2LL(ac.Position(), loc)
		m := dist / float32(fp.CruiseSpeed) * 60
		fp.CoordinationTime = CoordinationTime{
			Time: simTime.Add(time.Duration(m * float32(time.Minute))),
		}
	}
	return nil
}
