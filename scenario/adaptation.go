// scenario/adaptation.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scenario

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

// FinalizeFacilityAdaptation validates FacilityAdaptation fields that
// require the scenario group's Locator, mapSpec, or airport data. Self-contained
// validation is done earlier in FacilityAdaptation.ValidateConfig.
func FinalizeFacilityAdaptation(s *sim.FacilityAdaptation, e *util.ErrorLogger, sg *Group,
	mapSpec *videomaps.LibrarySpec, mapSpecs map[string]*videomaps.LibrarySpec) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("facility_adaptations")

	// specForArea returns the effective spec for an area: the area's
	// own mapSpec if it has a video_map_file, otherwise the facility-level one.
	specForArea := func(ac *sim.STARSArea) *videomaps.LibrarySpec {
		if ac.VideoMapFile != "" {
			if m, ok := mapSpecs[ac.VideoMapFile]; ok {
				return m
			}
		}
		return mapSpec
	}

	// Validate area-level video_map_file entries exist in mapSpecs.
	for areaNum, ac := range s.Areas {
		if ac.VideoMapFile != "" {
			if _, ok := mapSpecs[ac.VideoMapFile]; !ok {
				e.ErrorString(`video_map_file %q in area %s not found. Options: %s`,
					ac.VideoMapFile, areaNum, strings.Join(util.SortedMapKeys(mapSpecs), ", "))
			}
		}
	}

	// Video maps: validate area-level video_maps against the effective spec.
	for areaNum, ac := range s.Areas {
		m := specForArea(ac)
		for _, name := range ac.VideoMapNames {
			if name != "" && !m.HasMap(name) {
				e.ErrorString(`video map %q in area %s "video_maps" is not a valid video map`, name, areaNum)
			}
		}
		for _, name := range ac.DefaultMaps {
			if name != "" && !m.HasMap(name) {
				e.ErrorString(`video map %q in area %s "default_maps" is not a valid video map`, name, areaNum)
			}
		}
	}

	// Video map labels are validated in fc.validateSTARSAdaptation.

	// A TRACON scenario's facility config must define either controllers or
	// video maps in areas to drive a STARS display. ARTCC scenarios use
	// ERAM and don't need these.
	var allAreaVideoMaps []string
	for _, ac := range s.Areas {
		allAreaVideoMaps = append(allAreaVideoMaps, ac.VideoMapNames...)
	}
	if sg.ARTCC == "" && len(s.Controllers) == 0 && len(allAreaVideoMaps) == 0 {
		e.ErrorString(`must specify either "controllers" or "video_maps" in "areas"`)
	}

	// Controller config centers and video maps (require Locator + mapSpec).
	if len(s.Controllers) > 0 {
		for ctrl, config := range s.Controllers {
			if config.CenterString != "" {
				if pos, ok := sg.Locate(config.CenterString); !ok {
					e.ErrorString(`unknown location %q specified for "center"`, s.CenterString)
				} else {
					config.Center = pos
					s.Controllers[ctrl] = config
				}
			}
		}

		for tcp, config := range s.Controllers {
			// Resolve mapSpec: controller video_map_file > area > facility.
			ctrlSpec := mapSpec
			if config.VideoMapFile != "" {
				if m, ok := mapSpecs[config.VideoMapFile]; ok {
					ctrlSpec = m
				} else {
					e.ErrorString(`video_map_file %q for controller %q not found. Options: %s`,
						config.VideoMapFile, tcp, strings.Join(util.SortedMapKeys(mapSpecs), ", "))
				}
			} else if ctrl, ok := sg.FacilityConfig.ControlPositions[tcp]; ok && ctrl.Area != "" {
				if ac, ok := s.Areas[ctrl.Area]; ok {
					ctrlSpec = specForArea(ac)
				}
			}
			for _, name := range config.DefaultMaps {
				if !ctrlSpec.HasMap(name) {
					e.ErrorString(`video map %q in "default_maps" for controller %q is not a valid video map`,
						name, tcp)
				}
			}
			for _, name := range config.VideoMapNames {
				if name != "" && !ctrlSpec.HasMap(name) {
					e.ErrorString(`video map %q in "video_maps" for controller %q is not a valid video map`,
						name, tcp)
				}
			}
		}
	}

	// Radar sites (require Locator).
	for name, rs := range s.RadarSites {
		e.Push("Radar site " + name)
		if p, ok := sg.Locate(rs.PositionString); rs.PositionString == "" || !ok {
			e.ErrorString("radar site position %q not found", rs.PositionString)
		} else {
			rs.Position = p
		}
		if rs.Char == "" {
			e.ErrorString(`radar site is missing "char"`)
		}
		if rs.Elevation == 0 {
			e.ErrorString(`radar site is missing "elevation"`)
		}
		e.Pop()
	}

	// Coordination fixes (require Locator + DB).
	for fix, fixes := range s.CoordinationFixes {
		e.Push("Coordination fix " + fix)
		// FIXME(mtrokel)
		/*
			if _, ok := sg.Locate(fix); !ok {
				e.ErrorString(`coordination fix "%v" cannot be located`, fix)
			}
		*/
		acceptableTypes := []string{"route", "zone"}
		for i, fix := range fixes {
			e.Push(fmt.Sprintf("Number %v", i))
			if !slices.Contains(acceptableTypes, fix.Type) {
				e.ErrorString(`type "%v" is invalid. Valid types are "route" and "zone"`, fix.Type)
			}
			if fix.Altitude[0] < 0 {
				e.ErrorString(`bottom altitude "%v" is below zero`, fix.Altitude[0])
			}
			if fix.Altitude[0] > fix.Altitude[1] {
				e.ErrorString(`bottom altitude "%v" is higher than the top altitude "%v"`, fix.Altitude[0], fix.Altitude[1])
			}
			if !av.DB.IsFacility(fix.ToFacility) {
				e.ErrorString(`to facility "%v" is invalid`, fix.ToFacility)
			}
			if !av.DB.IsFacility(fix.FromFacility) {
				e.ErrorString(`from facility "%v" is invalid`, fix.FromFacility)
			}
			e.Pop()
		}
		e.Pop()
	}

	// Single char AIDs (require sg.Airports).
	for char, airport := range s.SingleCharAIDs {
		e.Push("Airport ID " + char)
		if _, ok := sg.Airports[av.ICAOAirportCode(airport)]; !ok {
			e.ErrorString(`airport %q isn't specified`, airport)
		}
		e.Pop()
	}

	// Significant points (require Locator).
	e.Push(`"significant_points"`)
	if s.SignificantPoints == nil {
		s.SignificantPoints = make(map[string]sim.SignificantPoint)
	}
	for name, sp := range s.SignificantPoints {
		e.Push(name)

		if len(name) < 3 {
			e.ErrorString("name must be at least 3 characters")
		} else {
			sp.Name = name

			if sp.ShortName != "" && len(name) == 3 {
				e.ErrorString(`"short_name" can only be given if name is more than 3 characters.`)
			}
			if len(sp.ShortName) > 3 {
				e.ErrorString(`"short_name" cannot be more than 3 characters.`)
			}
			if sp.Location.IsZero() {
				// An explicit "location" wins; otherwise the point is
				// located by its own name.
				where := util.Select(sp.LocationStr != "", sp.LocationStr, name)
				if p, ok := sg.Locate(where); !ok {
					e.ErrorString("unable to find location of %q", where)
				} else {
					sp.Location = p
				}
			}
		}

		// Update for any changes we made
		s.SignificantPoints[name] = sp

		e.Pop()
	}
	e.Pop()

	// Altimeters (require sg.Airports).
	if len(s.Lists.SSA.Altimeters) > 6 {
		e.ErrorString(`Only 6 airports may be specified for "altimeters"; %d were given`, len(s.Lists.SSA.Altimeters))
	}
	for _, ap := range s.Lists.SSA.Altimeters {
		if _, ok := sg.Airports[ap]; !ok {
			e.ErrorString(`Airport %q in "altimeters" not found in scenario group "airports"`, ap)
		}
	}
	// The weather station stands in for the facility when it has no gridded
	// weather; the system altimeter is that airport when nothing else says so.
	if s.WeatherStation == "" {
		s.WeatherStation = s.Lists.SSA.SystemAltimeter
	}
	if s.WeatherStation != "" {
		if err := av.CheckAirport("weather station", s.WeatherStation); err != nil {
			e.Error(err)
		}
	} else if sg.ARTCC == "" {
		e.ErrorString(`neither "weather_station" nor "system_altimeter" specified`)
	}

	// Fix-pair active-runway criteria name an airport the facility works; the
	// runway itself is checked against the database in FixPairConfiguration.
	if fpc := s.FixPairConfiguration; fpc != nil {
		e.Push("fix_pair_configuration")
		for i, r := range fpc.Reassignments {
			if r.ActiveRunway == "" || r.ActiveRunway == "*" {
				continue
			}
			e.Push(fmt.Sprintf("fix_pair_reassignment[%d]", i))
			if airport, _, ok := strings.Cut(r.ActiveRunway, "/"); ok {
				if _, ok := sg.Airports[av.ICAOAirportCode(airport)]; !ok {
					e.ErrorString(`"active_runway": airport %q not found in scenario group "airports"`, airport)
				}
			}
			e.Pop()
		}
		e.Pop()
	}

	// Hold for release validation (require sg.Airports).
	for airport, ap := range sg.Airports {
		var matches []string
		for _, list := range s.Lists.Coordination {
			if slices.Contains(list.Airports, airport) {
				matches = append(matches, list.Name)
			}
		}

		hfr := ap.HoldForRelease
		for _, rwy := range ap.DepartureRoutes {
			for _, exitRoutes := range rwy {
				for _, route := range exitRoutes {
					if route.HoldForRelease {
						hfr = true
					}
				}
			}
		}

		if hfr {
			// An airport may appear in several coordination lists when they are
			// split by "owner_tcp"; facility validation forbids genuinely
			// overlapping coverage.
		} else if len(matches) != 0 {
			// And it shouldn't be any if it's not hold for release
			e.ErrorString(`Airport %q isn't "hold_for_release" but is in "lists.coordination": %s.`, airport,
				strings.Join(matches, ", "))
		}
	}

	// Coordination list airports (require sg.Airports).
	for _, list := range s.Lists.Coordination {
		e.Push(`"lists.coordination" ` + list.Name)
		for _, ap := range list.Airports {
			if _, ok := sg.Airports[ap]; !ok {
				e.ErrorString("Airport %q not defined in scenario group.", ap)
			}
		}
		e.Pop()
	}

	// Airspace awareness (require Locator + ControlPositions).
	for _, aa := range s.AirspaceAwareness {
		for _, fix := range aa.Fix {
			if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
				e.ErrorString("%s : fix unknown", fix)
			}
		}

		if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
			e.ErrorString(`lower end of "altitude_range" %d above upper end %d`,
				aa.AltitudeRange[0], aa.AltitudeRange[1])
		}

		if _, ok := sg.FacilityConfig.ControlPositions[sg.resolveController(sim.TCP(aa.ReceivingController))]; !ok {
			e.ErrorString("%s: controller unknown", aa.ReceivingController)
		}

		for _, t := range aa.AircraftType {
			if t != "J" && t != "T" && t != "P" {
				e.ErrorString(`%q: invalid "aircraft_type". Expected "J", "T", or "P".`, t)
			}
		}
	}
	for areaID, area := range s.Areas {
		e.Push("areas[" + areaID + "].airspace_awareness")
		for _, aa := range area.AirspaceAwareness {
			for _, fix := range aa.Fix {
				if _, ok := sg.Locate(fix); !ok && fix != "ALL" {
					e.ErrorString("%s : fix unknown", fix)
				}
			}

			if aa.AltitudeRange[0] > aa.AltitudeRange[1] {
				e.ErrorString(`lower end of "altitude_range" %d above upper end %d`,
					aa.AltitudeRange[0], aa.AltitudeRange[1])
			}

			if _, ok := sg.FacilityConfig.ControlPositions[sg.resolveController(sim.TCP(aa.ReceivingController))]; !ok {
				e.ErrorString("%s: controller unknown", aa.ReceivingController)
			}

			for _, t := range aa.AircraftType {
				if t != "J" && t != "T" && t != "P" {
					e.ErrorString(`%q: invalid "aircraft_type". Expected "J", "T", or "P".`, t)
				}
			}
		}
		e.Pop()
	}

	// Restriction areas: vertex resolution and spatial checks (require Locator).
	e.Push(`"restriction_areas"`)
	for idx, ra := range s.RestrictionAreas {
		if len(ra.VerticesUser) > 0 {
			// Polygons
			if ra.CircleRadius > 0 {
				e.ErrorString(`Cannot specify both "circle_radius" and "vertices".`)
			}

			ra.VerticesUser = ra.VerticesUser.InitializeLocations(sg, sg.NmPerLongitude, sg.MagneticVariation, false, e)
			var verts []math.Point2LL
			for _, v := range ra.VerticesUser {
				verts = append(verts, v.Location)
			}

			nv := len(verts)
			if ra.Closed && nv < 3 {
				e.ErrorString(`At least 3 "vertices" must be given for a closed restriction area.`)
			}
			if !ra.Closed && nv < 2 {
				e.ErrorString(`At least 2 "vertices" must be given for an open restriction area.`)
			}

			ra.Vertices = make([][]math.Point2LL, 1)
			ra.Vertices[0] = verts
			ra.UpdateTriangles()

			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.AverageVertexPosition()
			}
		} else if ra.CircleRadius > 0 {
			// Circle-related checks
			if ra.CircleRadius > 125 {
				e.ErrorString(`"radius" cannot be larger than 125.`)
			}
			if ra.CircleCenter.IsZero() {
				e.ErrorString(`Must specify "circle_center" if "circle_radius" is given.`)
			}
			if ra.TextPosition.IsZero() {
				ra.TextPosition = ra.CircleCenter
			}
		} else {
			// Must be text-only
			if (ra.Text[0] != "" || ra.Text[1] != "") && ra.TextPosition.IsZero() {
				e.ErrorString(`Must specify "text_position" with restriction area`)
			}
		}
		s.RestrictionAreas[idx] = ra
	}
	e.Pop()

	e.Pop() // config
}

// resolveERAMCoordination returns this TRACON's pseudo-ERAM coordination
// adaptation from its parent ARTCC host config, keyed by the TRACON's STARS
// computer id (its stars_id in the ARTCC's handoff_ids). configs holds all
// the facility configs, loaded — with any coordination geometry parsed and
// validated — before scenario groups are processed; the result is shared
// read-only. An ARTCC-primary scenario self-hosts: its own config's
// arts_coordination entry keyed by the ARTCC's id covers flights inbound
// from adjacent centers. Returns nil for facilities whose host adapts no
// coordination for them.
func resolveERAMCoordination(sg *Group, configs map[string]*sim.FacilityConfig) *enroute.Coordination {
	if sg.TRACON == "" {
		// ARTCC-primary: the center's coordination is adapted in its own
		// config under its own facility id.
		afc := configs[configurationsPath(sg.ARTCC, sg.ARTCC)]
		if afc == nil {
			return nil
		}
		entry, ok := afc.FacilityAdaptation.ArtsCoordination[sg.ARTCC]
		if !ok {
			return nil
		}
		return &enroute.Coordination{
			ComputerID:   sg.ARTCC,
			Coord:        entry,
			Restrictions: afc.FacilityAdaptation.Restrictions,
		}
	}
	// TRACON scenarios frequently omit "artcc"; derive the host ARTCC.
	artcc := sg.ARTCC
	if artcc == "" {
		artcc = av.DB.ARTCCForFacility(sg.TRACON)
	}
	if artcc == "" {
		return nil
	}
	afc := configs[configurationsPath(artcc, artcc)]
	if afc == nil {
		return nil
	}
	// The TRACON's computer id is its stars_id in the ARTCC's handoff_ids;
	// a TRACON the host has no handoff id for (e.g. the fictional Academy
	// facility) is keyed by its own id.
	computerID := sg.TRACON
	for _, hid := range afc.HandoffIDs {
		if hid.ID == sg.TRACON {
			computerID = hid.StarsID
			break
		}
	}
	entry, ok := afc.FacilityAdaptation.ArtsCoordination[computerID]
	if !ok {
		return nil
	}
	return &enroute.Coordination{
		ComputerID:   computerID,
		Coord:        entry,
		Restrictions: afc.FacilityAdaptation.Restrictions,
	}
}

// validateCoordinationFixes checks that each coordination fix the host's
// arts_coordination can hand this facility is one the facility can match.
// Flight plans carry 3-character fix ids, so a fix that is neither an adapted
// significant point nor an airport here is derived and then silently matched by
// nothing: not fix pairs, not adapted fix criteria, not airspace awareness.
func validateCoordinationFixes(ec *enroute.Coordination, fa *sim.FacilityAdaptation, facility string,
	e *util.ErrorLogger) {
	if ec == nil || ec.Coord == nil {
		return
	}
	e.Push(facility)
	defer e.Pop()
	e.Push(fmt.Sprintf("arts_coordination[%s]", ec.ComputerID))
	defer e.Pop()

	checkFix := func(fix string) {
		if fix != "" && !fa.ResolveEndpoint(fa.FixPairFixID(fix)) {
			e.ErrorString("%s : fix is neither an adapted significant point nor an airport at this facility",
				fix)
		}
	}
	checkFixes := func(fixes []enroute.CoordFix, defaultFix string) {
		for _, cf := range fixes {
			checkFix(cf.Fix)
		}
		checkFix(defaultFix)
	}

	for i, r := range ec.Coord.RouteBased {
		e.Push(fmt.Sprintf("route_based[%d]", i))
		checkFixes(r.Fixes, r.DefaultFix)
		e.Pop()
	}
	for i, za := range ec.Coord.ZoneBased {
		e.Push(fmt.Sprintf("zone_based[%d]", i))
		for _, bucket := range [][]enroute.ZoneEntry{za.Arrival, za.Departure, za.Overflight} {
			for _, ze := range bucket {
				checkFixes(ze.Fixes, ze.DefaultFix)
			}
		}
		e.Pop()
	}
}

// loadFacilityConfig loads and unmarshals a facility configuration file.
// Results are cached so that a facility several scenario groups share is only
// loaded once. Call Finalize separately for semantic validation.
func loadFacilityConfig(filesystem fs.FS, path string, e *util.ErrorLogger) *sim.FacilityConfig {
	facilityConfigCacheMu.Lock()
	fc, ok := facilityConfigCache[path]
	facilityConfigCacheMu.Unlock()
	if ok {
		return fc
	}

	e.Push("Facility config " + path)
	defer e.Pop()

	if fc = parseFacilityConfig(filesystem, path, e); fc == nil {
		return nil
	}
	return cacheFacilityConfig(path, fc)
}

// parseFacilityConfig reads and unmarshals a facility configuration file,
// checking for duplicate keys and unknown fields along the way.
func parseFacilityConfig(filesystem fs.FS, path string, e *util.ErrorLogger) *sim.FacilityConfig {
	contents, err := fs.ReadFile(filesystem, path)
	if err != nil {
		e.Error(err)
		return nil
	}

	if dups := util.FindDuplicateJSONKeys(contents); len(dups) > 0 {
		for _, d := range dups {
			if d.Path != "" {
				e.ErrorString("duplicate JSON key %q in %s", d.Key, d.Path)
			} else {
				e.ErrorString("duplicate JSON key %q at root level", d.Key)
			}
		}
	}

	hadErrors := e.HaveErrors()
	util.CheckJSON[sim.FacilityConfig](contents, e)
	if !hadErrors && e.HaveErrors() {
		return nil
	}

	var fc *sim.FacilityConfig
	if err := util.UnmarshalJSONBytes(contents, &fc); err != nil {
		e.Error(err)
		return nil
	}
	return fc
}

// cacheFacilityConfig records fc as the facility config for path and returns
// the config that ends up cached; one stored by another goroutine in the
// interim wins so that all callers share a single instance.
func cacheFacilityConfig(path string, fc *sim.FacilityConfig) *sim.FacilityConfig {
	facilityConfigCacheMu.Lock()
	defer facilityConfigCacheMu.Unlock()
	if existing, ok := facilityConfigCache[path]; ok {
		return existing
	}
	facilityConfigCache[path] = fc
	return fc
}

// loadFacilityConfigOverride installs the given file in place of the facility
// configuration in resources/configurations with the same filename, for
// testing changes to a facility's adaptation. Errors are reported in e, in
// which case nothing is installed and the file from resources is used as
// usual.
func loadFacilityConfigOverride(filename string, e *util.ErrorLogger) {
	e.Push("Facility config override " + filename)
	defer e.Pop()

	path, err := facilityConfigOverridePath(filename)
	if err != nil {
		e.Error(err)
		return
	}

	filesystem := func() fs.FS {
		if filepath.IsAbs(filename) {
			return util.RootFS{}
		} else {
			return os.DirFS(".")
		}
	}()

	fc := parseFacilityConfig(filesystem, filename, e)
	if fc == nil {
		return
	}

	// Validate it as the file it replaces: Finalize takes the
	// facility and whether it is an ARTCC from the path.
	fc.Finalize(path, e)
	if !e.HaveErrors() {
		cacheFacilityConfig(path, fc)
	}
}

// facilityConfigOverridePath returns the path in resources/configurations of
// the facility configuration that the given file replaces. Configuration
// filenames are unique across the ARTCC directories, so the base filename
// determines it.
func facilityConfigOverridePath(filename string) (string, error) {
	base := filepath.Base(filename)
	var match string
	err := util.WalkResources("configurations", func(path string, d fs.DirEntry, _ fs.FS, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != base {
			return nil
		}
		match = path
		return fs.SkipAll
	})
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", fmt.Errorf("no facility configuration named %q in resources/configurations", base)
	}
	return match, nil
}

// IsARTCC returns true if the facility code looks like an ARTCC
