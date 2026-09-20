// scenario/load.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/brief"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"

	"github.com/brunoga/deep"
)

///////////////////////////////////////////////////////////////////////////
// Load

func loadScenarioGroup(filesystem fs.FS, path string, e *util.ErrorLogger) *Group {
	e.Push("File " + path)
	defer e.Pop()

	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	// Check for duplicate keys in the JSON
	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	// Reject forbidden top-level keys that should now be in the facility config.
	var rawKeys map[string]json.RawMessage
	if err := json.Unmarshal(contents, &rawKeys); err == nil {
		for _, forbidden := range []string{"config", "facility_adaptations", "control_positions"} {
			if _, ok := rawKeys[forbidden]; ok {
				e.ErrorString("%q must not appear in scenario group files; it belongs in the facility configuration file", forbidden)
			}
		}
	}

	util.CheckJSON[Group](contents, e)
	if e.HaveErrors() {
		return nil
	}

	var s Group
	if err := util.UnmarshalJSONBytes(contents, &s); err != nil {
		e.Error(err)
		return nil
	}
	if s.Name == "" {
		e.ErrorString(`scenario group is missing "name"`)
		return nil
	}
	if s.TRACON == "" && s.ARTCC == "" {
		e.ErrorString(`scenario group is missing "tracon" or "artcc"`)
		return nil
	}
	s.SourceFile = path
	return &s
}

// facilityConfigPath derives the path to the facility configuration file
// from the scenario group's TRACON/ARTCC fields. The convention is:
// configurations/<ARTCC>/<facility>.json.
func facilityConfigPath(sg *Group) string {
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = av.DB.ARTCCForFacility(sg.TRACON)
	}
	return configurationsPath(artcc, sg.facility())
}

// configurationsPath returns the path of facility's configuration file, which
// lives in its ARTCC's directory.
func configurationsPath(artcc, facility string) string {
	return "configurations/" + artcc + "/" + facility + ".json"
}

// scenarioBriefPath returns the expected path of the scenario brief for
// the given facility (TRACON or ARTCC name). The convention parallels
// facility configurations: briefs/<ARTCC>/<facility>.md.
func scenarioBriefPath(facility string) string {
	artcc := av.DB.ARTCCForFacility(facility)
	if artcc == "" {
		artcc = facility
	}
	return "briefs/" + artcc + "/" + facility + ".md"
}

// facilityConfigCache caches loaded facility configs so that multiple
// scenario groups referencing the same facility (e.g., N90) share one load.
// Protected by facilityConfigCacheMu since loadFacilityConfig is called
// from parallel goroutines during scenario loading.
var (
	facilityConfigCache   = make(map[string]*sim.FacilityConfig)
	facilityConfigCacheMu sync.Mutex
)

// loadFacilityConfig loads and unmarshals a facility configuration file.
// Results are cached so that a facility several scenario groups share is only

func IsARTCC(facility string) bool {
	return len(facility) == 3 && strings.HasPrefix(facility, "Z")
}

// loadNeighborControllers loads controllers from a neighboring facility's
// config file and adds them to the scenario group's ControlPositions.
// The neighbor is identified by facility code (e.g., "ABE", "PHL", "ZDC").
// If the neighbor's config file doesn't exist, it's silently skipped since
// not all facilities in the real NAS have configs in this system.
//
// Each neighbor controller gets the canonical (longest) prefix applied to
// its position and FacilityIdentifier so that controllers from different
// facilities don't collide.
// Controllers are stored under only this canonical prefix; shorter
// references are resolved at lookup time via resolveController.
func neighborPrefix(facility string, handoffIDs []sim.HandoffID) string {
	for _, hid := range handoffIDs {
		if hid.ID == facility {
			switch {
			case hid.StarsID != "":
				return hid.StarsID
			case hid.Prefix != "":
				return hid.Prefix
			}
		}
	}
	return ""
}

func loadNeighborControllers(filesystem fs.FS, sg *Group, neighbor string,
	handoffIDs []sim.HandoffID, e *util.ErrorLogger) {
	prefix := neighborPrefix(neighbor, handoffIDs)
	if prefix == "" {
		e.ErrorString("TRACON neighbor %s not found in handoff_ids", neighbor)
		return
	}

	// Determine the ARTCC for this neighbor.
	artcc := av.DB.ARTCCForFacility(neighbor)
	if artcc == "" {
		e.Push("Scenario group: " + sg.Name)
		e.ErrorString("unknown facility %s", neighbor)
		e.Pop()
		return
	}

	path := fmt.Sprintf("configurations/%s/%s.json", artcc, neighbor)

	// Check if file exists before trying to load.
	if _, err := fs.Stat(filesystem, path); err != nil {
		return // Neighbor config doesn't exist — not an error.
	}

	fc := loadFacilityConfig(filesystem, path, e)
	if fc == nil {
		return
	}

	// Add neighbor controllers under the full prefix only.
	// Shorter references are resolved at lookup time via resolveController.
	// Don't overwrite existing positions (the primary facility takes precedence).
	neighborIsARTCC := IsARTCC(neighbor)
	for position, ctrl := range fc.ControlPositions {
		ctrlCopy := deep.MustCopy(ctrl)
		ctrlCopy.FacilityIdentifier = prefix
		ctrlCopy.Position = string(position)
		if neighborIsARTCC {
			ctrlCopy.ERAMFacility = true
		}
		pid := sim.TCP(ctrlCopy.PositionId())

		if _, exists := sg.FacilityConfig.ControlPositions[pid]; !exists {
			sg.FacilityConfig.ControlPositions[pid] = ctrlCopy
		}
	}
}

// OverrideFiles holds paths to user-provided files that replace or add to
// the contents of the resources directory, for testing facilities under
// development. They come from the command line or the "Facility
// Engineering" section of the settings window.
type OverrideFiles struct {
	Scenario      string
	VideoMap      string
	ScenarioBrief string
	// Multiple facility configurations may be overridden at once (e.g.,
	// both a TRACON's and its ARTCC's).
	FacilityConfigs []string
}

// Load loads all of the available scenarios, both from the
// scenarios/ directory in the source code distribution as well as,
// optionally, files provided on the command line, and runs the
// full startup validation pass: video map references, arrival spawn
// altitudes, and emergencies.json. It doesn't try to do any sort of
// meaningful error handling but it does try to continue on in the
// presence of errors; all errors will be printed and the program will
// exit if there are any.  We'd rather force any errors due to invalid
// scenario definitions to be fixed...
//
// If an override file has errors, they are returned in overrideErrors and it

func Load(overrides OverrideFiles, e *util.ErrorLogger, lg *log.Logger) (*Tables, string) {
	start := time.Now()

	var overrideErrors string
	addOverrideErrors := func(errs string) {
		if overrideErrors != "" {
			overrideErrors += "\n"
		}
		overrideErrors += errs
	}

	// Install the facility config overrides before anything else so that
	// every load of the files they replace picks them up.
	for _, fc := range overrides.FacilityConfigs {
		var oe util.ErrorLogger
		loadFacilityConfigOverride(fc, &oe)
		if oe.HaveErrors() {
			addOverrideErrors(oe.String())
			lg.Warnf("Facility config override has errors and will not be loaded: %s", fc)
		}
	}

	// First load the scenarios.
	scenarioGroups := make(map[string]map[string]*Group)
	briefs := NewBriefRegistry()
	catalogs := make(map[string]map[string]*Catalog)

	type scenarioWalkItem struct {
		filesystem fs.FS
		path       string
	}
	var scenarioItems []scenarioWalkItem
	err := util.WalkResources("scenarios", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking scenarios/: %v", err)
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		scenarioItems = append(scenarioItems, scenarioWalkItem{fs, path})
		return nil
	})

	type scenarioWalkResult struct {
		s    *Group
		errs util.ErrorLogger
	}
	scenarioResults := make([]scenarioWalkResult, len(scenarioItems))
	eg := &errgroup.Group{}
	eg.SetLimit(runtime.NumCPU())
	for i, it := range scenarioItems {
		eg.Go(func() error {
			scenarioResults[i].s = loadScenarioGroup(it.filesystem, it.path, &scenarioResults[i].errs)
			return nil
		})
	}
	_ = eg.Wait()

	for _, r := range scenarioResults {
		e.MergeFrom(&r.errs)
		if r.s == nil {
			continue
		}
		s := r.s
		facility := s.facility()
		if _, ok := scenarioGroups[facility][s.Name]; ok {
			e.ErrorString("%s / %s: scenario redefined", facility, s.Name)
		} else {
			if scenarioGroups[facility] == nil {
				scenarioGroups[facility] = make(map[string]*Group)
			}
			scenarioGroups[facility][s.Name] = s
		}
	}
	if err != nil {
		e.Error(err)
	}
	if e.HaveErrors() {
		// Don't keep going since we'll likely crash in the following
		return nil, ""
	}

	// Load the scenario specified on command line, if any.
	// Store it separately so we can validate it with a separate error logger
	var extraScenario *Group
	var extraScenarioFacility string
	if overrides.Scenario != "" {
		var extraE util.ErrorLogger
		fs := func() fs.FS {
			if filepath.IsAbs(overrides.Scenario) {
				return util.RootFS{}
			} else {
				return os.DirFS(".")
			}
		}()
		s := loadScenarioGroup(fs, overrides.Scenario, &extraE)
		if s != nil {
			facility := s.facility()

			// Load and validate facility config for the extra scenario.
			extraResourcesFS := util.GetResourcesFS()
			fc := loadFacilityConfig(extraResourcesFS, facilityConfigPath(s), &extraE)
			if fc != nil {
				fc.Finalize(facilityConfigPath(s), &extraE)
			}
			if fc != nil && !extraE.HaveErrors() {
				s.FacilityConfig = *deep.MustCopy(fc)

				for _, neighbor := range fc.HandoffIDs {
					neighbor := string(neighbor.ID)
					loadNeighborControllers(extraResourcesFS, s, neighbor, fc.HandoffIDs, &extraE)
				}
			}

			// These may have an empty "video_map_file" member, which
			// is automatically patched up here...
			if fc != nil && s.FacilityConfig.FacilityAdaptation.VideoMapFile == "" {
				if overrides.VideoMap != "" {
					s.FacilityConfig.FacilityAdaptation.VideoMapFile = overrides.VideoMap
				} else {
					extraE.ErrorString(`%s: no "video_map_file" in scenario and -videomap not specified`,
						overrides.Scenario)
				}
			}

			// Store the scenario for later validation (don't add to scenarioGroups yet)
			if !extraE.HaveErrors() {
				extraScenario = s
				extraScenarioFacility = facility
			}
		}

		// Capture any errors from the extra scenario
		if extraE.HaveErrors() {
			addOverrideErrors(extraE.String())
			lg.Warnf("Extra scenario file has errors and will not be loaded: %s", overrides.Scenario)
		}
	}

	// Load video map specs (header-only) for validation.
	mapSpecs := make(map[string]*videomaps.LibrarySpec)
	err = util.WalkResources("videomaps", func(path string, d fs.DirEntry, fs fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking videomaps: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if strings.HasSuffix(path, ".mappack") {
			mapSpecs[path], err = videomaps.LoadLibrarySpec(path)
		}

		return err
	})
	if err != nil {
		lg.Errorf("error loading videomaps: %v", err)
		os.Exit(1)
	}

	// Load the video map specified on the command line, if any.
	if overrides.VideoMap != "" {
		mapSpecs[overrides.VideoMap], err = videomaps.LoadLibrarySpec(overrides.VideoMap)
		if err != nil {
			lg.Errorf("%s: %v", overrides.VideoMap, err)
			os.Exit(1)
		}
	}

	// validateBriefForFacility runs the facility-has-scenarios check and the brief validation
	// pipeline, returning the per-videomap hashes and whether the facility is known.
	validateBriefForFacility := func(facility string, content []byte, e *util.ErrorLogger) (map[string][]byte, bool) {
		if _, ok := scenarioGroups[facility]; !ok {
			e.ErrorString("brief exists for facility %q which has no scenarios", facility)
			return nil, false
		}

		e.Push(facility + ".md")
		defer e.Pop()

		parsed := brief.ParseMarkdown(content, brief.ParseOptions{})
		for _, msg := range parsed.ParseErrors() {
			e.ErrorString("%s", msg)
		}

		videoMapHashes := make(map[string][]byte)
		for _, filename := range parsed.VideoMapFiles() {
			if mapSpec, ok := mapSpecs[filename]; !ok {
				e.ErrorString("Unknown video map file %q referenced in brief", filename)
			} else if hash, err := mapSpec.Hash(); err != nil {
				e.ErrorString("Unable to compute hash for video map %q: %v", filename, err)
			} else {
				videoMapHashes[filename] = hash
			}
		}
		return videoMapHashes, true
	}

	// Load facility briefs from resources/briefs/<ARTCC>/<facility>.md.
	err = util.WalkResources("briefs", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking briefs/: %v", err)
			return nil
		}

		if d.IsDir() {
			return nil
		}

		base := filepath.Base(path)
		if filepath.Ext(base) != ".md" || strings.HasPrefix(base, "#") || strings.HasPrefix(base, ".#") {
			return nil
		}

		e.Push("Brief file " + path)
		defer e.Pop()

		facility := strings.ToUpper(strings.TrimSuffix(base, ".md"))
		if expected := scenarioBriefPath(facility); path != expected {
			e.ErrorString("brief for %q expected at %q, found at %q", facility, expected, path)
			return nil
		}

		if content, err := fs.ReadFile(filesystem, path); err != nil {
			e.Error(err)
		} else if hashes, ok := validateBriefForFacility(facility, content, e); ok {
			briefs.register(facility, hashes, "")
		}

		return nil
	})
	if err != nil {
		e.Error(err)
	}

	// Phase 1: Load and validate all facility configs by walking the
	// configurations/ directory. Every .json file is loaded and validated
	// via Finalize, regardless of whether a scenario references it.
	resourcesFS := util.GetResourcesFS()
	type configWalkItem struct {
		filesystem fs.FS
		path       string
	}
	var configItems []configWalkItem
	err = util.WalkResources("configurations", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil {
			lg.Errorf("error walking configurations/: %v", err)
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		configItems = append(configItems, configWalkItem{filesystem, path})
		return nil
	})

	configErrs := make([]util.ErrorLogger, len(configItems))
	loadedConfigs := make([]*sim.FacilityConfig, len(configItems))
	eg = &errgroup.Group{}
	eg.SetLimit(runtime.NumCPU())
	for i, it := range configItems {
		eg.Go(func() error {
			fc := loadFacilityConfig(it.filesystem, it.path, &configErrs[i])
			if fc != nil {
				fc.Finalize(it.path, &configErrs[i])
			}
			loadedConfigs[i] = fc
			return nil
		})
	}
	eg.Wait()

	// All of the loaded facility configs, by path; the phases below resolve
	// cross-facility references (a TRACON's host ARTCC adaptation, neighbor
	// controllers) against this rather than reloading anything.
	facilityConfigs := make(map[string]*sim.FacilityConfig)
	for i, it := range configItems {
		if loadedConfigs[i] != nil {
			facilityConfigs[it.path] = loadedConfigs[i]
		}
	}

	for i := range configErrs {
		e.MergeFrom(&configErrs[i])
	}
	if err != nil {
		e.Error(err)
	}

	// Every facility config lives in its ARTCC's directory, and the ARTCC
	// must have a config of its own there: TRACONs resolve their pseudo-ERAM
	// coordination through it.
	missingARTCC := make(map[string]bool)
	for _, it := range configItems {
		artcc := path.Base(path.Dir(it.path))
		if artccPath := configurationsPath(artcc, artcc); !missingARTCC[artcc] {
			if _, ok := facilityConfigs[artccPath]; !ok {
				missingARTCC[artcc] = true
				e.ErrorString("%s: no config for the ARTCC itself", artccPath)
			}
		}
	}

	// Parse pseudo-ERAM coordination geometry once, so that TRACON scenario
	// groups can share it read-only (see resolveERAMCoordination). This is
	// the host ARTCC's data, so it resolves against the nav database alone
	// rather than any scenario group's fixes.
	for _, it := range configItems {
		if fc := facilityConfigs[it.path]; fc != nil && len(fc.FacilityAdaptation.ArtsCoordination) > 0 {
			e.Push("Facility config " + it.path)
			enroute.ParseGeometry(fc.FacilityAdaptation.ArtsCoordination, fc.FacilityAdaptation.Restrictions, enroute.DBLocator{}, e)
			e.Pop()
		}
	}

	// Phase 2: Attach validated configs to scenario groups and load
	// neighbor controllers. No further config validation is done here.
	// Every scenario group for a facility resolves the same coordination
	// against the same adaptation, so its fixes are checked once.
	coordinationChecked := make(map[string]bool)
	for _, tracon := range scenarioGroups {
		for name, sg := range tracon {
			fc := loadFacilityConfig(resourcesFS, facilityConfigPath(sg), e)
			if fc == nil {
				delete(tracon, name)
				continue
			}

			sg.FacilityConfig = *deep.MustCopy(fc)
			sg.ERAMCoordination = resolveERAMCoordination(sg, facilityConfigs)
			if path := facilityConfigPath(sg); !coordinationChecked[path] {
				coordinationChecked[path] = true
				validateCoordinationFixes(sg.ERAMCoordination, &sg.FacilityConfig.FacilityAdaptation,
					sg.facility(), e)
			}

			// Add missing airports referenced by altimeters and coordination
			// lists from sibling scenario groups. The facility config is
			// shared across all scenario groups for a TRACON, but sub-area
			// scenarios only define a subset of airports.
			addFromSibling := func(airport av.ICAOAirportCode) {
				if _, ok := sg.Airports[airport]; airport == "" || ok {
					return
				}
				for _, sibling := range tracon {
					if sibling == sg {
						continue
					}
					if _, ok := sibling.Airports[airport]; ok {
						if sg.Airports == nil {
							sg.Airports = make(map[av.ICAOAirportCode]*av.Airport)
						}
						sg.Airports[airport] = &av.Airport{} // This is an uninitialized, empty airport that is soley used for altimiter and coordination lists so that they're consistent across areas of a TRACON.
						// For example, for the N90 ISP files, the EWR and LGA airports aren't defined, so when their altimeter and coorindation lists were called from the N90 configuration file, there was no defined airport.
						return
					}
				}
			}
			for _, ap := range sg.FacilityConfig.FacilityAdaptation.Lists.SSA.Altimeters {
				addFromSibling(ap)
			}
			for _, cl := range sg.FacilityConfig.FacilityAdaptation.Lists.Coordination {
				for _, ap := range cl.Airports {
					addFromSibling(ap)
					// Airports in coordination lists must be hold for release.
					if a, ok := sg.Airports[ap]; ok && !a.HoldForRelease {
						a.HoldForRelease = true
					}
				}
			}

			// Load controllers from neighboring facilities.
			for _, neighborFac := range fc.HandoffIDs {
				neighbor := string(neighborFac.ID)
				loadNeighborControllers(resourcesFS, sg, neighbor, fc.HandoffIDs, e)
			}
		}
	}

	// Final tidying before we return the loaded scenarios. Per-scenario
	// Finalize is the dominant cost here; do them in parallel.
	type phase3Task struct {
		tname, groupName string
		sgroup           *Group
		mapSpec          *videomaps.LibrarySpec
		vfErr            string // pre-validation error, if any
		localCatalogs    map[string]map[string]*Catalog
		localE           util.ErrorLogger
	}
	var phase3Tasks []*phase3Task

	for tname, tracon := range scenarioGroups {
		scenarioNames := make(map[string]string)
		// Sequentially handle cheap per-TRACON checks that read shared state.
		e.Push("TRACON " + tname)
		for groupName, sgroup := range tracon {
			e.Push(sgroup.SourceFile)
			e.Push("Scenario group " + groupName)
			for scenarioName := range sgroup.Scenarios {
				if other, ok := scenarioNames[scenarioName]; ok {
					e.ErrorString("scenario %q is also defined in the %q scenario group",
						scenarioName, other)
				}
				scenarioNames[scenarioName] = groupName
			}
			e.Pop() // Scenario group
			e.Pop() // SourceFile

			t := &phase3Task{tname: tname, groupName: groupName, sgroup: sgroup}
			fa := &sgroup.FacilityConfig.FacilityAdaptation
			if vf := fa.VideoMapFile; vf == "" {
				t.vfErr = `no "video_map_file" specified`
			} else if mapSpec, ok := mapSpecs[vf]; !ok {
				t.vfErr = fmt.Sprintf("no mapSpec for video map %q found. Options: %s",
					vf, strings.Join(util.SortedMapKeys(mapSpecs), ", "))
			} else {
				t.mapSpec = mapSpec
			}
			phase3Tasks = append(phase3Tasks, t)
		}
		e.Pop() // TRACON
	}

	eg = &errgroup.Group{}
	eg.SetLimit(runtime.NumCPU())
	for _, t := range phase3Tasks {
		t.localE.Push("TRACON " + t.tname)
		t.localE.Push(t.sgroup.SourceFile)
		t.localE.Push("Scenario group " + t.groupName)
		if t.vfErr != "" {
			t.localE.ErrorString("%s", t.vfErr)
			continue
		}
		t.localCatalogs = make(map[string]map[string]*Catalog)

		eg.Go(func() error {
			t.sgroup.Finalize(&t.localE, t.localCatalogs, t.mapSpec, mapSpecs)
			return nil
		})
	}
	_ = eg.Wait()

	// Merge per-task results.
	for _, t := range phase3Tasks {
		e.MergeFrom(&t.localE)
		for facility, m := range t.localCatalogs {
			if catalogs[facility] == nil {
				catalogs[facility] = make(map[string]*Catalog)
			}
			maps.Copy(catalogs[facility], m)
		}
	}

	// Validate the extra scenario separately with its own error logger and
	// stage in a separate catalogs map that's only merged on success.
	if extraScenario != nil {
		var extraE util.ErrorLogger
		extraE.Push(extraScenario.SourceFile)
		extraE.Push("TRACON " + extraScenarioFacility)
		extraE.Push("Scenario group " + extraScenario.Name)

		localCatalogs := make(map[string]map[string]*Catalog)

		// Make sure we have what we need in terms of video maps
		fa := &extraScenario.FacilityConfig.FacilityAdaptation
		if vf := fa.VideoMapFile; vf == "" {
			extraE.ErrorString(`no "video_map_file" specified`)
		} else if mapSpec, ok := mapSpecs[vf]; !ok {
			extraE.ErrorString("no mapSpec for video map %q found. Options: %s", vf,
				strings.Join(util.SortedMapKeys(mapSpecs), ", "))
		} else {
			extraScenario.ERAMCoordination = resolveERAMCoordination(extraScenario, facilityConfigs)
			validateCoordinationFixes(extraScenario.ERAMCoordination, fa, extraScenario.facility(), &extraE)
			extraScenario.Finalize(&extraE, localCatalogs, mapSpec, mapSpecs)
		}

		extraE.Pop() // Scenario group
		extraE.Pop() // TRACON
		extraE.Pop() // SourceFile

		if extraE.HaveErrors() {
			addOverrideErrors(extraE.String())
			lg.Warnf("Extra scenario file has validation errors and will not be loaded: %s", overrides.Scenario)
		} else {
			// Merge the local catalogs into the shared one only on success.
			for facility, m := range localCatalogs {
				if catalogs[facility] == nil {
					catalogs[facility] = make(map[string]*Catalog)
				}
				maps.Copy(catalogs[facility], m)
			}
			if scenarioGroups[extraScenarioFacility] == nil {
				scenarioGroups[extraScenarioFacility] = make(map[string]*Group)
			}
			scenarioGroups[extraScenarioFacility][extraScenario.Name] = extraScenario
		}
	}

	// Load extra scenario brief file from command line, if any. Must run after the extra scenario
	// (if any) has been inserted into scenarioGroups.
	if overrides.ScenarioBrief != "" {
		var extraE util.ErrorLogger
		extraE.Push("Extra brief file " + overrides.ScenarioBrief)

		facility := strings.ToUpper(strings.TrimSuffix(filepath.Base(overrides.ScenarioBrief), ".md"))

		if content, err := os.ReadFile(overrides.ScenarioBrief); err != nil {
			extraE.Error(err)
		} else if hashes, ok := validateBriefForFacility(facility, content, &extraE); ok && !extraE.HaveErrors() {
			briefs.register(facility, hashes, overrides.ScenarioBrief)
		}

		extraE.Pop()

		if extraE.HaveErrors() {
			addOverrideErrors(extraE.String())
			lg.Warnf("Extra scenario brief file has errors and will not be loaded: %s", overrides.ScenarioBrief)
		}
	}

	// Walk all of the scenario groups to get all of the possible departing aircraft
	// types to see where V2 is needed in the performance database..
	acTypes := make(map[string]struct{})

	for _, tracon := range scenarioGroups {
		for _, sg := range tracon {
			for _, ap := range sg.Airports {
				for _, dep := range ap.Departures {
					for _, al := range dep.Airlines {
						for _, ac := range al.Aircraft() {
							acTypes[ac.ICAO] = struct{}{}
						}
					}
				}
			}
		}
	}
	var missing []string
	for t := range util.SortedMap(acTypes) {
		if av.DB.AircraftPerformance[t].Speed.V2 == 0 {
			missing = append(missing, t)
		}
	}
	lg.Infof("Missing V2 in performance database: %s", strings.Join(missing, ", "))

	loadEmergencies(e)

	timetableCatalog, err := traffic.LoadBuiltinTimetables()
	if err != nil {
		e.Error(err)
	} else {
		attachTimetables(catalogs, timetableCatalog)
	}
	attachHistoricalFlightIntervals(catalogs, lg)
	finalizeTrafficSources(catalogs, scenarioGroups, e)
	checkInboundAssignments(scenarioGroups, e)

	lg.Infof("scenario.Load total: %s", time.Since(start))
	return MakeTables(scenarioGroups, catalogs, mapSpecs, briefs), overrideErrors
}

// ListAllScenarios returns a sorted list of all available scenarios in TRACON/scenario format
func ListAllScenarios(overrides OverrideFiles, lg *log.Logger) ([]string, error) {
	var e util.ErrorLogger
	tables, _ := Load(overrides, &e, lg)
	if e.HaveErrors() {
		return nil, fmt.Errorf("failed to load scenarios")
	}

	var scenarios []string
	for tracon, groups := range tables.Groups {
		for _, group := range groups {
			for scenarioName := range group.Scenarios {
				scenarios = append(scenarios, tracon+"/"+scenarioName)
			}
		}
	}

	slices.Sort(scenarios)
	return scenarios, nil
}

// WXFacilities loads the scenarios and returns the airports and facilities
// that vice's weather pipeline should cover. (Center scenario groups specify
// an ARTCC rather than a TRACON; the ARTCCs to cover don't depend on which
// have scenarios, so only their airports are of interest here.)
func WXFacilities(lg *log.Logger) (wx.Facilities, error) {
	var e util.ErrorLogger
	tables, _ := Load(OverrideFiles{}, &e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		return wx.Facilities{}, fmt.Errorf("failed to load scenarios")
	}

	var airports, tracons []string
	for _, groups := range tables.Groups {
		for _, sg := range groups {
			for name := range sg.Airports {
				airports = append(airports, string(name))
			}
			if sg.TRACON != "" {
				tracons = append(tracons, sg.TRACON)
			}
		}
	}

	return wx.MakeFacilities(airports, tracons), nil
}

// checkArrivalSpawnAltitude flags an arrival whose initial altitude is
// too high to meet its first "at or below" restriction given the distance
// to that waypoint. Assumes 2500 fpm descent at 250 kts ground speed. If
