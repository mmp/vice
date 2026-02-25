// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

///////////////////////////////////////////////////////////////////////////
// Types

// TestBenchConfig maps section names to test cases.
type TestBenchConfig map[string][]TestBenchCase

type TestBenchCase struct {
	Label    string                  `json:"label"`
	Aircraft []TestBenchAircraftSpec `json:"aircraft,omitempty"`
	Steps    []TestBenchStep         `json:"steps,omitempty"`
	Group    string                  `json:"group,omitempty"`
	Airport  string                  `json:"airport,omitempty"`
	// Departure-based spawn (uses CreateDeparture)
	Departure bool   `json:"departure,omitempty"`
	Runway    string `json:"runway,omitempty"`
	Category  string `json:"category,omitempty"`
}

type TestBenchStep struct {
	Command        string        `json:"command,omitempty"`
	Callsign       string        `json:"callsign,omitempty"`
	ExpectReadback StringOrArray `json:"expect_readback,omitempty"`
	RejectReadback string        `json:"reject_readback,omitempty"`
	WaitFor        string        `json:"wait_for,omitempty"`
}

// StringOrArray unmarshals from either a JSON string or an array of strings.
type StringOrArray []string

func (s *StringOrArray) UnmarshalJSON(data []byte) error {
	// Try string first.
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	// Then try array.
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*s = arr
	return nil
}

type TestBenchAircraftSpec struct {
	Callsign      string  `json:"callsign,omitempty"`
	AircraftType  string  `json:"aircraft_type,omitempty"` // e.g. "B738"; empty = sampled from airline or B738 fallback
	DistanceNM    float32 `json:"distance_nm"`
	Altitude      float32 `json:"altitude,omitempty"` // explicit altitude; 0 = use fix restriction
	Speed         float32 `json:"speed"`
	Heading       float32 `json:"heading,omitempty"`        // aircraft heading; 0 = inbound to fix/airport
	HeadingOffset float32 `json:"heading_offset,omitempty"` // added to default heading (runway heading)
	Bearing       float32 `json:"bearing,omitempty"`        // bearing FROM reference to place aircraft
	BearingOffset float32 `json:"bearing_offset,omitempty"` // added to default bearing (extended centerline)
	RelativeTo    string  `json:"relative_to,omitempty"`    // fix name or placeholder (e.g. "{if}"); empty = airport

	StarWaypoints  bool   `json:"star_waypoints,omitempty"` // populate Nav.Waypoints from a STAR at the airport
	TrafficInSight bool   `json:"traffic_in_sight,omitempty"`
	Note           string `json:"note,omitempty"`
}

type TestBench struct {
	client *client.ControlClient
	lg     *log.Logger
	config TestBenchConfig
	events *sim.EventsSubscription

	selectedApproach  int
	approaches        []testBenchApproachInfo // cached list of available approaches
	selectedDeparture int
	departures        []testBenchDepartureInfo // cached list of available departure runways

	// Spawned aircraft tracking (for clear button)
	spawnedAircraft []sim.Aircraft
	spawnedTest     *TestBenchCase // which test case owns spawnedAircraft

	// Callsign mapping: spec index -> generated callsign.
	// Populated at spawn time so steps can reference aircraft by index.
	callsignMap map[int]string
	headingMap  map[int]float32 // spec index -> spawned heading

	// Runtime-resolved placeholders, populated at spawn time.
	holdFix       string           // randomly selected fix with a published hold
	holdFixName   string           // written form of holdFix (e.g. navaid full name)
	starFix       string           // first fix of the randomly selected STAR
	starWaypoints av.WaypointArray // waypoints from the selected STAR transition

	// Test step tracking
	activeTest     *TestBenchCase
	currentStep    int
	stepResults    []string // per-step: "pass", "fail", or ""
	lastReadback   string
	lastReadbackCS av.ADSBCallsign
	statusMessage  string
}

type testBenchApproachInfo struct {
	airport    string
	approachId string
	approach   *av.Approach
}

type testBenchDepartureInfo struct {
	airport  string
	runway   string
	category string
}

///////////////////////////////////////////////////////////////////////////
// Constructor and setup

func NewTestBench(c *client.ControlClient, es *sim.EventStream, lg *log.Logger) *TestBench {
	tb := &TestBench{
		client: c,
		lg:     lg,
		events: es.Subscribe(),
	}

	// Load test scenarios via the resource system so the path is
	// resolved regardless of the working directory.
	if util.ResourceExists("test_bench_scenarios.json") {
		data := util.LoadResourceBytes("test_bench_scenarios.json")
		if err := json.Unmarshal(data, &tb.config); err != nil {
			lg.Errorf("test bench: error parsing test_bench_scenarios.json: %v", err)
			tb.config = make(TestBenchConfig)
		}
	} else {
		lg.Warnf("test bench: resources/test_bench_scenarios.json not found")
		tb.config = make(TestBenchConfig)
	}

	tb.refreshSelections()
	return tb
}

func (tb *TestBench) refreshSelections() {
	tb.approaches = nil
	for _, rwy := range tb.client.State.ArrivalRunways {
		ap := tb.client.State.Airports[rwy.Airport]
		if ap == nil {
			continue
		}
		for _, name := range util.SortedMapKeys(ap.Approaches) {
			appr := ap.Approaches[name]
			if appr.Runway == string(rwy.Runway.Base()) {
				tb.approaches = append(tb.approaches, testBenchApproachInfo{
					airport:    rwy.Airport,
					approachId: name,
					approach:   appr,
				})
			}
		}
	}

	tb.departures = nil
	for _, dr := range tb.client.State.DepartureRunways {
		tb.departures = append(tb.departures, testBenchDepartureInfo{
			airport:  dr.Airport,
			runway:   string(dr.Runway),
			category: dr.Category,
		})
	}
}

func (tb *TestBench) resetTestState() {
	tb.activeTest = nil
	tb.currentStep = 0
	tb.stepResults = nil
	tb.lastReadback = ""
	tb.lastReadbackCS = ""
	tb.callsignMap = nil
}

///////////////////////////////////////////////////////////////////////////
// Placeholder resolution

// approachFixNames returns the IAF, IF, and FAF fix names from the first
// route of the given approach that defines them.
func approachFixNames(ap *av.Approach) (iaf, ifFix, faf string) {
	for _, route := range ap.Waypoints {
		for _, wp := range route {
			if wp.IAF() && iaf == "" {
				iaf = wp.Fix
			}
			if wp.IF() && ifFix == "" {
				ifFix = wp.Fix
			}
			if wp.FAF() && faf == "" {
				faf = wp.Fix
			}
		}
		if iaf != "" || ifFix != "" || faf != "" {
			return
		}
	}
	return
}

// resolveStep substitutes placeholders in a step's strings with values
// from the currently selected approach and runtime state:
//
//	{approach}      → approach ID (e.g. "I2L")
//	{approach_name} → full name (e.g. "ILS Runway 22L")
//	{iaf}           → Initial Approach Fix name
//	{if}            → Intermediate Fix name
//	{faf}           → Final Approach Fix name
//	{hold_fix}      → fix ID for commands, written name for readbacks
//	{star_fix}      → first fix of a randomly selected STAR
func (tb *TestBench) resolveStep(tc *TestBenchCase, step TestBenchStep) TestBenchStep {
	if tb.selectedApproach < 0 || tb.selectedApproach >= len(tb.approaches) {
		return step
	}

	// Copy the slice so we don't permanently mutate the JSON-parsed config
	// (resolveStep is called every frame during drawTestCase).
	step.ExpectReadback = append([]string(nil), step.ExpectReadback...)

	ap := tb.approaches[tb.selectedApproach]
	iaf, ifFix, faf := approachFixNames(ap.approach)

	// Commands use the fix ID (e.g. "LENDY") for the command parser;
	// readbacks use the written name (e.g. "Lendy") which is what
	// FixSnippetFormatter produces for pilot readbacks.
	r := strings.NewReplacer(
		"{approach_name}", ap.approach.FullName,
		"{approach}", ap.approachId,
		"{iaf}", iaf,
		"{if}", ifFix,
		"{faf}", faf,
		"{star_fix}", tb.starFix,
	)
	step.Command = strings.ReplaceAll(r.Replace(step.Command), "{hold_fix}", tb.holdFix)
	for i, s := range step.ExpectReadback {
		step.ExpectReadback[i] = strings.ReplaceAll(r.Replace(s), "{hold_fix}", tb.holdFixName)
	}
	step.RejectReadback = strings.ReplaceAll(r.Replace(step.RejectReadback), "{hold_fix}", tb.holdFixName)

	// Resolve per-aircraft index references like "{0}", "{hdg0}" to the
	// generated callsign or heading for that aircraft spec index.
	if tb.spawnedTest == tc && tb.callsignMap != nil {
		step.Callsign = tb.resolveCallsignRef(step.Callsign)
		// Replace {hdgN} with the 3-digit zero-padded heading for aircraft N.
		for idx, hdg := range tb.headingMap {
			placeholder := fmt.Sprintf("{hdg%d}", idx)
			h := int(hdg+0.5) % 360
			if h == 0 {
				h = 360
			}
			formatted := fmt.Sprintf("%03d", h)
			step.Command = strings.ReplaceAll(step.Command, placeholder, formatted)
			for i, s := range step.ExpectReadback {
				step.ExpectReadback[i] = strings.ReplaceAll(s, placeholder, formatted)
			}
			step.RejectReadback = strings.ReplaceAll(step.RejectReadback, placeholder, formatted)
		}
	}

	return step
}

// resolveCallsignRef maps a callsign reference to the actual spawned callsign.
// It handles index references like "{0}", "{1}" for multi-aircraft tests.
// An empty string passes through unchanged (defaultCallsign handles that case).
func (tb *TestBench) resolveCallsignRef(ref string) string {
	if ref == "" {
		return ""
	}
	// Try "{N}" format.
	if len(ref) >= 3 && ref[0] == '{' && ref[len(ref)-1] == '}' {
		inner := ref[1 : len(ref)-1]
		idx := 0
		for _, ch := range inner {
			if ch < '0' || ch > '9' {
				return ref // not a numeric index
			}
			idx = idx*10 + int(ch-'0')
		}
		if cs, ok := tb.callsignMap[idx]; ok {
			return cs
		}
	}
	return ref
}

// resolveFixRef resolves a fix name (possibly a placeholder like "{if}")
// to its location and altitude restriction from the selected approach.
func (tb *TestBench) resolveFixRef(name string, apInfo testBenchApproachInfo) (loc math.Point2LL, alt float32, ok bool) {
	// Resolve placeholders.
	iaf, ifFix, faf := approachFixNames(apInfo.approach)
	name = strings.NewReplacer(
		"{iaf}", iaf, "{if}", ifFix, "{faf}", faf,
		"{hold_fix}", tb.holdFix, "{star_fix}", tb.starFix,
	).Replace(name)

	// Find the fix in the approach waypoints.
	for _, route := range apInfo.approach.Waypoints {
		for _, wp := range route {
			if wp.Fix == name {
				var fixAlt float32
				if ar := wp.AltitudeRestriction(); ar != nil {
					fixAlt = ar.Range[0]
				}
				return wp.Location, fixAlt, true
			}
		}
	}

	// Fall back to the global waypoint database.
	if p, found := av.DB.LookupWaypoint(name); found {
		return p, 0, true
	}
	return math.Point2LL{}, 0, false
}

// fixWrittenName returns the written form of a fix name, matching the
// logic in FixSnippetFormatter.Written: navaid full name if available,
// otherwise the fix ID itself.
func fixWrittenName(fix string) string {
	if aid, ok := av.DB.Navaids[fix]; ok {
		return util.StopShouting(aid.Name)
	}
	return fix
}

// resolveRuntimePlaceholders picks random fixes for {hold_fix} and {star_fix}
// based on the selected approach's airport.
func (tb *TestBench) resolveRuntimePlaceholders(apInfo testBenchApproachInfo) {
	tb.holdFix = ""
	tb.holdFixName = ""
	tb.starFix = ""
	tb.starWaypoints = nil

	r := rand.Make()

	// Pick a random published hold near this airport. A fix has a published
	// hold if it appears in EnrouteHolds (which is what HoldAtFix validates).
	dbAirport, ok := av.DB.Airports[apInfo.airport]
	if !ok {
		return
	}
	airportLoc := dbAirport.Location
	var holdFixes []string
	for fix := range av.DB.EnrouteHolds {
		if loc, ok := av.DB.LookupWaypoint(fix); ok && math.NMDistance2LL(airportLoc, loc) <= 25 {
			holdFixes = append(holdFixes, fix)
		}
	}
	if len(holdFixes) > 0 {
		tb.holdFix = holdFixes[r.Intn(len(holdFixes))]
		tb.holdFixName = fixWrittenName(tb.holdFix)
	}

	// Pick a random STAR that's active in the current scenario. Use the
	// arrival's own waypoints if available (scoped to the scenario's
	// airspace); otherwise look up the STAR in the DB.
	var starWaypointSets []av.WaypointArray
	for _, flow := range tb.client.State.InboundFlows {
		for _, arr := range flow.Arrivals {
			if arr.STAR == "" {
				continue
			}
			// Only consider arrivals serving the selected airport.
			if _, ok := arr.Airlines[apInfo.airport]; !ok {
				continue
			}
			if len(arr.Waypoints) > 0 {
				starWaypointSets = append(starWaypointSets, arr.Waypoints)
			} else if star, ok := dbAirport.STARs[arr.STAR]; ok {
				for _, wps := range star.Transitions {
					starWaypointSets = append(starWaypointSets, wps)
				}
			}
		}
	}
	if len(starWaypointSets) > 0 {
		wps := starWaypointSets[r.Intn(len(starWaypointSets))]
		tb.starWaypoints = util.DuplicateSlice(wps)
		for i := range tb.starWaypoints {
			tb.starWaypoints[i].SetOnSTAR(true)
		}
		if len(tb.starWaypoints) > 0 {
			tb.starFix = tb.starWaypoints[0].Fix
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// UI drawing

func (tb *TestBench) Draw(show *bool, p platform.Platform) {
	// Process events for readback tracking
	tb.processEvents()

	imgui.SetNextWindowSizeConstraints(imgui.Vec2{400, 200}, imgui.Vec2{800, float32(p.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Test Bench", show, imgui.WindowFlagsAlwaysAutoResize)

	// Approach selector
	if len(tb.approaches) > 0 {
		label := tb.approachLabel(tb.selectedApproach)
		imgui.Text("Approach:")
		imgui.SameLine()
		imgui.SetNextItemWidth(250)
		if imgui.BeginCombo("##approach", label) {
			for i := range tb.approaches {
				if imgui.SelectableBoolV(tb.approachLabel(i), i == tb.selectedApproach, 0, imgui.Vec2{}) {
					tb.selectedApproach = i
					tb.resetTestState()
				}
			}
			imgui.EndCombo()
		}
	} else {
		imgui.TextColored(imgui.Vec4{1, 1, 0, 1}, "No arrival approaches available")
	}

	// Departure runway selector
	if len(tb.departures) > 0 {
		label := tb.departureLabel(tb.selectedDeparture)
		imgui.Text("Departure:")
		imgui.SameLine()
		imgui.SetNextItemWidth(250)
		if imgui.BeginCombo("##departure", label) {
			for i := range tb.departures {
				if imgui.SelectableBoolV(tb.departureLabel(i), i == tb.selectedDeparture, 0, imgui.Vec2{}) {
					tb.selectedDeparture = i
				}
			}
			imgui.EndCombo()
		}
	}

	if tb.statusMessage != "" {
		imgui.TextColored(imgui.Vec4{0, 1, 0, 1}, tb.statusMessage)
	}

	imgui.Separator()

	// Draw sections
	for _, section := range util.SortedMapKeys(tb.config) {
		cases := tb.config[section]
		if imgui.CollapsingHeaderBoolPtr(section, nil) {
			for i := range cases {
				tb.drawTestCase(section, &cases[i])
			}
		}
	}

	imgui.End()
}

func (tb *TestBench) approachLabel(idx int) string {
	if idx < 0 || idx >= len(tb.approaches) {
		return "(none)"
	}
	a := tb.approaches[idx]
	return fmt.Sprintf("%s - %s (%s)", a.approachId, a.approach.FullName, a.airport)
}

func (tb *TestBench) departureLabel(idx int) string {
	if idx < 0 || idx >= len(tb.departures) {
		return "(none)"
	}
	d := tb.departures[idx]
	if d.category != "" {
		return fmt.Sprintf("%s %s (%s)", d.airport, d.runway, d.category)
	}
	return fmt.Sprintf("%s %s", d.airport, d.runway)
}

func (tb *TestBench) drawTestCase(section string, tc *TestBenchCase) {
	id := section + "/" + tc.Label
	imgui.PushIDStr(id)
	defer imgui.PopID()

	// Bordered group for each test case
	imgui.BeginGroup()

	imgui.Text(tc.Label)
	imgui.SameLine()

	if tc.Group != "" {
		if imgui.Button("Spawn STAR") {
			tb.spawnSTAR(tc)
		}
	} else if tc.Departure {
		if imgui.Button("Spawn Departure") {
			tb.spawnDeparture(tc)
		}
	} else if len(tc.Aircraft) > 0 {
		if imgui.Button("Spawn") {
			tb.spawnAircraft(tc)
		}
	}

	// Clear button — only shown on the test case whose aircraft are active
	if tb.spawnedTest == tc && len(tb.spawnedAircraft) > 0 {
		imgui.SameLine()
		if imgui.Button("Clear") {
			tb.client.DeleteAircraft(tb.spawnedAircraft, func(err error) {
				if err != nil {
					tb.lg.Warnf("test bench: clear aircraft: %v", err)
				}
			})
			tb.spawnedAircraft = nil
			tb.spawnedTest = nil
			tb.resetTestState()
			tb.statusMessage = "Cleared spawned aircraft"
		}
	}

	// Show aircraft specs
	for i, spec := range tc.Aircraft {
		note := ""
		if spec.Note != "" {
			note = " (" + spec.Note + ")"
		}
		flags := ""
		if spec.TrafficInSight {
			flags += " TIS"
		}
		altStr := fmt.Sprintf("%.0fft", spec.Altitude)
		if spec.Altitude == 0 && spec.RelativeTo != "" {
			altStr = "fix alt"
		}
		refStr := ""
		if spec.RelativeTo != "" {
			refStr = fmt.Sprintf(" from %s", spec.RelativeTo)
		}
		// Show the generated callsign if available, otherwise the spec callsign or index.
		displayCS := spec.Callsign
		if tb.spawnedTest == tc {
			if cs, ok := tb.callsignMap[i]; ok {
				displayCS = cs
			}
		}
		if displayCS == "" {
			displayCS = fmt.Sprintf("#%d", i)
		}
		imgui.TextColored(imgui.Vec4{0.7, 0.7, 0.7, 1},
			fmt.Sprintf("  %s: %.0fnm%s/%s/%.0fkts%s%s", displayCS,
				spec.DistanceNM, refStr, altStr, spec.Speed, flags, note))
	}

	// Show test steps
	if len(tc.Steps) > 0 {
		for i, rawStep := range tc.Steps {
			step := tb.resolveStep(tc, rawStep)
			isActive := tb.activeTest == tc && tb.currentStep == i

			// Per-step result indicator
			result := ""
			if tb.activeTest == tc && i < len(tb.stepResults) {
				result = tb.stepResults[i]
			}

			if result == "pass" {
				imgui.TextColored(imgui.Vec4{0, 1, 0, 1}, "  +")
			} else if result == "fail" {
				imgui.TextColored(imgui.Vec4{1, 0, 0, 1}, "  X")
			} else if isActive {
				imgui.Text("  >")
			} else {
				imgui.Text("   ")
			}
			imgui.SameLine()

			if step.WaitFor != "" {
				imgui.TextColored(imgui.Vec4{0.5, 0.8, 1, 1},
					fmt.Sprintf("[wait: %s]", step.WaitFor))
			} else {
				cs := av.ADSBCallsign(step.Callsign)
				if cs == "" {
					cs = tb.defaultCallsign(tc)
				}
				imgui.Text(fmt.Sprintf("%s %s", cs, step.Command))
			}
		}

		// Show last captured readback
		if tb.activeTest == tc && tb.lastReadback != "" {
			imgui.TextColored(imgui.Vec4{0.8, 0.8, 0, 1},
				fmt.Sprintf("    Readback [%s]: %s", tb.lastReadbackCS, tb.lastReadback))
		}
	}

	imgui.EndGroup()
	imgui.Separator()
}

///////////////////////////////////////////////////////////////////////////
// Spawning

// generateCallsign samples a random arrival airline from the scenario's
// inbound flows for the given airport and returns a realistic callsign and
// aircraft type (e.g. "AAL1234", "B738").
func (tb *TestBench) generateCallsign(airport string, existingCallsigns []av.ADSBCallsign) (callsign, acType string) {
	// Collect all arrival airlines destined for this airport.
	var airlines []av.ArrivalAirline
	for _, flow := range tb.client.State.InboundFlows {
		for _, arr := range flow.Arrivals {
			airlines = append(airlines, arr.Airlines[airport]...)
		}
	}
	if len(airlines) == 0 {
		tb.lg.Warnf("test bench: no arrival airlines found for %s", airport)
		return "", "B738"
	}

	r := rand.Make()
	al := rand.SampleSlice(r, airlines)
	acType, callsign = al.AirlineSpecifier.SampleAcTypeAndCallsign(
		r, existingCallsigns, false, al.Airport, airport, tb.lg)
	if acType == "" {
		acType = "B738"
	}
	return callsign, acType
}

func (tb *TestBench) spawnAircraft(tc *TestBenchCase) {
	if tb.selectedApproach < 0 || tb.selectedApproach >= len(tb.approaches) {
		tb.statusMessage = "No approach selected"
		return
	}

	apInfo := tb.approaches[tb.selectedApproach]
	airport := tb.client.State.Airports[apInfo.airport]
	if airport == nil {
		tb.statusMessage = "Airport not found: " + apInfo.airport
		return
	}

	nmPerLong := tb.client.State.NmPerLongitude
	magVar := tb.client.State.MagneticVariation
	rwyHeading := apInfo.approach.RunwayHeading(nmPerLong, magVar)
	ctrlFreq := sim.ControlPosition(tb.client.State.PrimaryPositionForTCW(tb.client.State.UserTCW))

	// Clear any previously spawned aircraft before spawning new ones.
	if len(tb.spawnedAircraft) > 0 {
		tb.client.DeleteAircraft(tb.spawnedAircraft, func(err error) {
			if err != nil {
				tb.lg.Warnf("test bench: auto-clear: %v", err)
			}
		})
	}

	tb.spawnedAircraft = nil
	tb.spawnedTest = tc
	tb.callsignMap = make(map[int]string)
	tb.headingMap = make(map[int]float32)
	tb.resolveRuntimePlaceholders(apInfo)
	var existingCallsigns []av.ADSBCallsign
	for i, spec := range tc.Aircraft {
		acType := spec.AircraftType
		callsign := spec.Callsign
		// Generate a realistic callsign (and aircraft type) if the spec doesn't provide one.
		if callsign == "" {
			var genType string
			callsign, genType = tb.generateCallsign(apInfo.airport, existingCallsigns)
			if callsign == "" {
				tb.statusMessage = "Failed to generate callsign — no matching airlines"
				return
			}
			if acType == "" {
				acType = genType
			}
		}
		tb.callsignMap[i] = callsign
		existingCallsigns = append(existingCallsigns, av.ADSBCallsign(callsign))

		ac, err := tb.buildAircraftWithCallsign(spec, callsign, acType, airport, apInfo, rwyHeading, nmPerLong, magVar, ctrlFreq)
		if err != nil {
			tb.statusMessage = fmt.Sprintf("Aircraft #%d: %v", i, err)
			return
		}
		tb.headingMap[i] = ac.Nav.FlightState.Heading
		tb.client.LaunchArrivalOverflight(ac)
		tb.spawnedAircraft = append(tb.spawnedAircraft, ac)
		tb.lg.Infof("test bench: launched %s at %.1fnm/%.0fft", callsign, spec.DistanceNM, ac.Nav.FlightState.Altitude)
	}

	tb.activateSteps(tc)

	tb.statusMessage = fmt.Sprintf("Spawned %d aircraft for \"%s\"", len(tc.Aircraft), tc.Label)
}

func (tb *TestBench) buildAircraftWithCallsign(spec TestBenchAircraftSpec, callsign, acType string,
	airport *av.Airport, apInfo testBenchApproachInfo, rwyHeading, nmPerLong, magVar float32,
	ctrlFreq sim.ControlPosition) (sim.Aircraft, error) {

	// Determine reference point and altitude.
	// If relative_to is set, position relative to that fix;
	// otherwise use the first STAR waypoint (if available) or the airport.
	refPos := airport.Location
	altitude := spec.Altitude

	if spec.RelativeTo != "" {
		if fixLoc, fixAlt, ok := tb.resolveFixRef(spec.RelativeTo, apInfo); ok {
			refPos = fixLoc
			if altitude == 0 {
				if fixAlt > 0 {
					altitude = fixAlt
				} else {
					return sim.Aircraft{}, fmt.Errorf("fix %q has no altitude restriction and spec has no explicit altitude", spec.RelativeTo)
				}
			}
		} else {
			return sim.Aircraft{}, fmt.Errorf("couldn't resolve fix %q", spec.RelativeTo)
		}
	} else if spec.StarWaypoints && len(tb.starWaypoints) > 0 {
		// Default to positioning near the first STAR waypoint.
		refPos = tb.starWaypoints[0].Location
	}

	// Determine the bearing from reference to place the aircraft.
	// Default: on extended centerline (reciprocal of runway heading).
	// BearingOffset is relative to the default; Bearing is absolute.
	bearing := rwyHeading + 180 + spec.BearingOffset
	if spec.Bearing != 0 {
		bearing = spec.Bearing
	}

	pos := math.Offset2LL(refPos, bearing, spec.DistanceNM, nmPerLong, magVar)

	// Aircraft heading: default is inbound (runway heading).
	// HeadingOffset is relative to the default; Heading is absolute.
	heading := rwyHeading + spec.HeadingOffset
	if spec.Heading != 0 {
		heading = spec.Heading
	}

	// Use the generated aircraft type if provided, otherwise fall back to B738.
	if acType == "" {
		acType = "B738"
	}
	perf, ok := av.DB.AircraftPerformance[acType]
	if !ok {
		// Fallback if the sampled type has no performance data.
		acType = "B738"
		perf = av.DB.AircraftPerformance["B738"]
	}

	ac := sim.Aircraft{
		ADSBCallsign:        av.ADSBCallsign(callsign),
		TypeOfFlight:        av.FlightTypeArrival,
		Mode:                av.TransponderModeAltitude,
		ControllerFrequency: ctrlFreq,
		FlightPlan: av.FlightPlan{
			ArrivalAirport: apInfo.airport,
			AircraftType:   acType,
			Rules:          av.FlightRulesIFR,
		},
		Nav: nav.Nav{
			Rand:           rand.Make(),
			FixAssignments: make(map[string]nav.NavFixAssignment),
			Perf:           perf,
			Altitude: nav.NavAltitude{
				Assigned: &altitude,
			},
			Speed: nav.NavSpeed{
				Assigned: &spec.Speed,
			},
			FlightState: nav.FlightState{
				Position:                pos,
				Heading:                 heading,
				Altitude:                altitude,
				IAS:                     spec.Speed,
				GS:                      spec.Speed,
				NmPerLongitude:          nmPerLong,
				MagneticVariation:       magVar,
				ArrivalAirport:          av.Waypoint{Fix: apInfo.airport, Location: airport.Location},
				ArrivalAirportLocation:  airport.Location,
				ArrivalAirportElevation: float32(av.DB.Airports[apInfo.airport].Elevation),
			},
			Approach: nav.NavApproach{},
		},
		TrafficInSight: spec.TrafficInSight,
		NASFlightPlan: &sim.NASFlightPlan{
			ACID:               sim.ACID(callsign),
			ArrivalAirport:     apInfo.airport,
			Rules:              av.FlightRulesIFR,
			TypeOfFlight:       av.FlightTypeArrival,
			AircraftCount:      1,
			AircraftType:       acType,
			TrackingController: ctrlFreq,
			OwningTCW:          tb.client.State.UserTCW,
		},
	}

	if spec.StarWaypoints && len(tb.starWaypoints) > 0 {
		ac.Nav.Waypoints = util.DuplicateSlice(tb.starWaypoints)
	}

	return ac, nil
}

func (tb *TestBench) spawnSTAR(tc *TestBenchCase) {
	airport := tc.Airport
	if airport == "" {
		airport = tb.client.State.PrimaryAirport
	}

	var ac sim.Aircraft
	tb.client.CreateArrival(tc.Group, airport, &ac, func(err error) {
		if err != nil {
			tb.lg.Warnf("test bench: CreateArrival %s: %v", tc.Group, err)
			tb.statusMessage = fmt.Sprintf("STAR spawn failed: %v", err)
		} else {
			tb.spawnedAircraft = append(tb.spawnedAircraft, ac)
			tb.spawnedTest = tc
			tb.lg.Infof("test bench: STAR %s spawned for %s (%s)", tc.Group, airport, ac.ADSBCallsign)
			tb.statusMessage = fmt.Sprintf("Spawned STAR %s at %s (%s)", tc.Group, airport, ac.ADSBCallsign)
			tb.activateSteps(tc)
		}
	})
}

func (tb *TestBench) spawnDeparture(tc *TestBenchCase) {
	airport := tc.Airport
	runway := tc.Runway
	category := tc.Category

	// Use the departure dropdown selection if the scenario doesn't specify.
	if airport == "" || runway == "" {
		if tb.selectedDeparture >= 0 && tb.selectedDeparture < len(tb.departures) {
			d := tb.departures[tb.selectedDeparture]
			if airport == "" {
				airport = d.airport
			}
			if runway == "" {
				runway = d.runway
				category = d.category
			}
		} else {
			tb.statusMessage = "No departure runway selected"
			return
		}
	}
	if airport == "" || runway == "" {
		tb.statusMessage = "No departure runway selected"
		return
	}

	var ac sim.Aircraft
	tb.client.CreateDeparture(airport, runway, category, av.FlightRulesIFR, &ac, func(err error) {
		if err != nil {
			tb.lg.Warnf("test bench: CreateDeparture: %v", err)
			tb.statusMessage = fmt.Sprintf("Departure spawn failed: %v", err)
		} else {
			// Launch the departure immediately (same pattern as LaunchControl).
			// CreateDeparture builds the aircraft; LaunchDeparture puts it on scope.
			tb.client.LaunchDeparture(ac, runway)

			tb.spawnedAircraft = append(tb.spawnedAircraft, ac)
			tb.spawnedTest = tc
			tb.lg.Infof("test bench: departure %s launched at %s rwy %s", ac.ADSBCallsign, airport, runway)
			tb.statusMessage = fmt.Sprintf("Launched departure %s at %s rwy %s", ac.ADSBCallsign, airport, runway)
			tb.activateSteps(tc)
		}
	})
}

///////////////////////////////////////////////////////////////////////////
// Event processing

// isTestCallsign checks whether the given callsign belongs to one of the
// test case's aircraft — either from the callsign map, JSON spec, or sim-spawned aircraft.
func (tb *TestBench) isTestCallsign(tc *TestBenchCase, cs av.ADSBCallsign) bool {
	// Check generated callsigns (only for the active spawned test).
	if tb.spawnedTest == tc {
		for _, mapped := range tb.callsignMap {
			if av.ADSBCallsign(mapped) == cs {
				return true
			}
		}
	}
	for _, spec := range tc.Aircraft {
		if av.ADSBCallsign(spec.Callsign) == cs {
			return true
		}
	}
	// For departures/STARs, check spawned aircraft (callsign assigned by sim).
	if tb.spawnedTest == tc {
		for _, ac := range tb.spawnedAircraft {
			if ac.ADSBCallsign == cs {
				return true
			}
		}
	}
	return false
}

// defaultCallsign returns the callsign to use when a step doesn't specify one.
func (tb *TestBench) defaultCallsign(tc *TestBenchCase) av.ADSBCallsign {
	// Use the callsign map first (populated at spawn time with generated callsigns).
	if tb.spawnedTest == tc {
		if cs, ok := tb.callsignMap[0]; ok {
			return av.ADSBCallsign(cs)
		}
	}
	if len(tc.Aircraft) > 0 {
		return av.ADSBCallsign(tc.Aircraft[0].Callsign)
	}
	if tb.spawnedTest == tc && len(tb.spawnedAircraft) > 0 {
		return tb.spawnedAircraft[0].ADSBCallsign
	}
	return ""
}

// matchWaitFor checks whether the given event matches a wait_for condition.
func matchWaitFor(waitFor string, event sim.Event) bool {
	text := strings.ToLower(event.WrittenText)
	switch waitFor {
	case "check_in":
		return event.RadioTransmissionType == av.RadioTransmissionContact
	case "field_in_sight":
		return strings.Contains(text, "field in sight") ||
			strings.Contains(text, "airport in sight")
	case "approach_clearance_request":
		return strings.Contains(text, "cleared for the approach") ||
			strings.Contains(text, "looking for the approach") ||
			strings.Contains(text, "need the approach")
	case "go_around":
		return strings.Contains(text, "going around") ||
			strings.Contains(text, "on the go")
	case "traffic_response":
		return strings.Contains(text, "traffic in sight") ||
			strings.Contains(text, "we've got the traffic") ||
			strings.Contains(text, "we have the traffic") ||
			strings.Contains(text, "looking") ||
			strings.Contains(text, "negative contact") ||
			strings.Contains(text, "in the clouds") ||
			strings.Contains(text, "imc")
	case "traffic_in_sight":
		return strings.Contains(text, "traffic in sight") ||
			strings.Contains(text, "we've got the traffic") ||
			strings.Contains(text, "we have the traffic")
	}
	return false
}

func (tb *TestBench) processEvents() {
	if tb.activeTest == nil {
		// Still drain events so the subscription doesn't fall behind.
		tb.events.Get()
		return
	}

	tc := tb.activeTest

	for _, event := range tb.events.Get() {
		if event.Type != sim.RadioTransmissionEvent {
			continue
		}
		if !tb.isTestCallsign(tc, event.ADSBCallsign) {
			continue
		}
		if tb.currentStep >= len(tc.Steps) {
			continue
		}

		step := tb.resolveStep(tc, tc.Steps[tb.currentStep])
		if step.WaitFor != "" {
			tb.processWaitForStep(step, event)
		} else {
			tb.processCommandStep(tc, step, event)
		}
	}
}

// processWaitForStep handles a wait_for step by checking whether the event
// matches the expected condition (check_in, field_in_sight, etc.).
func (tb *TestBench) processWaitForStep(step TestBenchStep, event sim.Event) {
	if matchWaitFor(step.WaitFor, event) {
		tb.stepResults[tb.currentStep] = "pass"
		tb.lastReadback = event.WrittenText
		tb.lastReadbackCS = event.ADSBCallsign
		tb.advanceStep()
	}
}

// processCommandStep handles a command step by verifying the pilot's readback
// against the expected/rejected text patterns.
func (tb *TestBench) processCommandStep(tc *TestBenchCase, step TestBenchStep, event sim.Event) {
	// Only evaluate actual readbacks — not check-ins or spontaneous transmissions.
	if event.RadioTransmissionType != av.RadioTransmissionReadback {
		return
	}

	// Check the step's target callsign.
	targetCS := av.ADSBCallsign(step.Callsign)
	if targetCS == "" {
		targetCS = tb.defaultCallsign(tc)
	}
	if targetCS != event.ADSBCallsign {
		return
	}

	tb.lastReadback = event.WrittenText
	tb.lastReadbackCS = event.ADSBCallsign

	// "Say again" means the pilot didn't understand; skip it so
	// the controller can re-issue the command.
	text := strings.ToLower(event.WrittenText)
	if strings.Contains(text, "say again") {
		return
	}

	// If the command step has no readback expectations, it's a
	// "fire and forget" step — pass the readback through so the
	// next step (e.g. wait_for) can evaluate it.
	if len(step.ExpectReadback) == 0 && step.RejectReadback == "" {
		tb.stepResults[tb.currentStep] = "pass"
		tb.advanceStep()
		// Re-evaluate this same event against the new current step.
		if tb.currentStep < len(tc.Steps) {
			next := tb.resolveStep(tc, tc.Steps[tb.currentStep])
			if next.WaitFor != "" && matchWaitFor(next.WaitFor, event) {
				tb.stepResults[tb.currentStep] = "pass"
				tb.lastReadback = event.WrittenText
				tb.lastReadbackCS = event.ADSBCallsign
				tb.advanceStep()
			}
		}
		return
	}

	// Verify readback against WrittenText (the structured form,
	// not the randomized spoken form). Pass if any of the expected
	// strings match.
	pass := false
	for _, expect := range step.ExpectReadback {
		if strings.Contains(text, strings.ToLower(expect)) {
			pass = true
			break
		}
	}
	if len(step.ExpectReadback) == 0 {
		pass = true
	}
	if step.RejectReadback != "" && strings.Contains(text, strings.ToLower(step.RejectReadback)) {
		pass = false
	}

	tb.lg.Debugf("test bench: step %d cmd=%q expect=%v reject=%q written=%q pass=%v",
		tb.currentStep, step.Command, step.ExpectReadback, step.RejectReadback, event.WrittenText, pass)

	if pass {
		tb.stepResults[tb.currentStep] = "pass"
	} else {
		tb.stepResults[tb.currentStep] = "fail"
	}
	tb.advanceStep()
}

func (tb *TestBench) activateSteps(tc *TestBenchCase) {
	if len(tc.Steps) > 0 {
		tb.activeTest = tc
		tb.currentStep = 0
		tb.stepResults = make([]string, len(tc.Steps))
		tb.lastReadback = ""
		tb.lastReadbackCS = ""
	}
}

func (tb *TestBench) advanceStep() {
	tb.currentStep++
}
