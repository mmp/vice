// scenario/catalog.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"
	"os"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// BriefRegistry holds the per-facility brief metadata gathered at
// scenario load time and consulted at sim-creation time.
type BriefRegistry struct {
	facilities     map[string]struct{}          // which facilities have briefs
	videoMapHashes map[string]map[string][]byte // facility -> video map filenames -> hashes
	pathOverrides  map[string]string            // non-canonical brief paths (set only by --scenario-brief)
}

func NewBriefRegistry() *BriefRegistry {
	return &BriefRegistry{
		facilities:     make(map[string]struct{}),
		videoMapHashes: make(map[string]map[string][]byte),
		pathOverrides:  make(map[string]string),
	}
}

func (r *BriefRegistry) register(facility string, hashes map[string][]byte, pathOverride string) {
	r.facilities[facility] = struct{}{}
	if len(hashes) > 0 {
		r.videoMapHashes[facility] = hashes
	}
	if pathOverride != "" {
		r.pathOverrides[facility] = pathOverride
	}
}

// VideoMapHashes gives the hashes of the video maps the facility's brief
// refers to, so that clients can check they have the same ones.
func (r *BriefRegistry) VideoMapHashes(facility string) map[string][]byte {
	return r.videoMapHashes[facility]
}

// LoadBrief returns the markdown source for the given facility's brief,
// or ("", nil) if no brief was registered for it at startup. The source
// is the --scenario-brief override when present, otherwise the canonical
// briefs/<ARTCC>/<facility>.md resource. Reads only init-immutable maps,
// so needs no locking.
func (r *BriefRegistry) LoadBrief(facility string) (string, error) {
	if _, ok := r.facilities[facility]; !ok {
		return "", nil
	}
	if p, ok := r.pathOverrides[facility]; ok {
		b, err := os.ReadFile(p)
		return string(b), err
	}
	path := scenarioBriefPath(facility)
	if !util.ResourceExists(path) {
		return "", fmt.Errorf("brief resource %q not found", path)
	}
	return string(util.LoadResourceBytes(path)), nil
}

// Tables is the result of loading and validating the scenario and
// facility configuration files.
type Tables struct {
	Groups      map[string]map[string]*Group
	Catalogs    map[string]map[string]*Catalog
	MapSpecs    map[string]*av.MapLibrarySpec
	Briefs      *BriefRegistry
	Emergencies []sim.Emergency
}

func MakeTables(groups map[string]map[string]*Group, catalogs map[string]map[string]*Catalog,
	mapSpecs map[string]*av.MapLibrarySpec, briefs *BriefRegistry) *Tables {
	return &Tables{
		Groups:   groups,
		Catalogs: catalogs,
		MapSpecs: mapSpecs,
		Briefs:   briefs,
		// Load already validated emergencies.json; re-parse to hand the list to sim sessions.
		Emergencies: loadEmergencies(nil),
	}
}

// Client-side info about the available scenarios.
type Catalog struct {
	Scenarios        map[string]*Spec
	ControlPositions map[sim.TCP]*av.Controller
	DefaultScenario  string
	Facility         string
	ARTCC            string
	Area             string
	Airports         []av.ICAOAirportCode // airports in this scenario group
}

type Spec struct {
	ControllerConfiguration *sim.ControllerConfiguration
	MagneticVariation       float32
	WindSpecifier           *wx.WindSpecifier
	Timetables              []sim.TimetableSummary
	// TrafficSources are the sources this scenario can be flown with, in the
	// order they should be offered. A scenario that gives no airlines can't
	// generate its own traffic, so it offers only the published sources.
	TrafficSources []sim.TrafficSource
	// HistoricalFlightIntervals are the stretches of time the historical flight
	// data covers; it has a gap wherever the list does.
	HistoricalFlightIntervals []util.TimeInterval

	LaunchConfig sim.LaunchConfig

	Description      string
	DepartureRunways []sim.DepartureRunway
	ArrivalRunways   []sim.ArrivalRunway
	Center           math.Point2LL
}

func (s *Spec) AllAirports() []av.ICAOAirportCode {
	allAirports := make(map[av.ICAOAirportCode]bool)
	for _, runway := range s.DepartureRunways {
		allAirports[runway.Airport] = true
	}
	for _, runway := range s.ArrivalRunways {
		allAirports[runway.Airport] = true
	}
	return util.SortedMapKeys(allAirports)
}
