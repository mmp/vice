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
	Command        string `json:"command,omitempty"`
	Callsign       string `json:"callsign,omitempty"`
	ExpectReadback string `json:"expect_readback,omitempty"`
	RejectReadback string `json:"reject_readback,omitempty"`
	WaitFor        string `json:"wait_for,omitempty"`
}

type TestBenchAircraftSpec struct {
	Callsign   string  `json:"callsign"`
	DistanceNM float32 `json:"distance_nm"`
	Altitude   float32 `json:"altitude,omitempty"` // explicit altitude; 0 = use fix restriction
	Speed      float32 `json:"speed"`
	Heading    float32 `json:"heading,omitempty"`     // aircraft heading; 0 = inbound to fix/airport
	Bearing    float32 `json:"bearing,omitempty"`     // bearing FROM reference to place aircraft
	RelativeTo string  `json:"relative_to,omitempty"` // fix name or placeholder (e.g. "{if}"); empty = airport

	TrafficInSight bool   `json:"traffic_in_sight,omitempty"`
	Note           string `json:"note,omitempty"`
}

type TestBench struct {
	client *client.ControlClient
	lg     *log.Logger
	config TestBenchConfig
	events *sim.EventsSubscription

	selectedApproach  int
	approaches        []debugApproachInfo // cached list of available approaches
	selectedDeparture int
	departures        []debugDepartureInfo // cached list of available departure runways

	// Spawned aircraft tracking (for clear button)
	spawnedAircraft []sim.Aircraft
	spawnedTest     *TestBenchCase // which test case owns spawnedAircraft

	// Test step tracking
	activeTest     *TestBenchCase
	currentStep    int
	stepResults    []string // per-step: "pass", "fail", or ""
	lastReadback   string
	lastReadbackCS av.ADSBCallsign
	statusMessage  string
}

type debugApproachInfo struct {
	airport    string
	approachId string
	approach   *av.Approach
}

type debugDepartureInfo struct {
	airport  string
	runway   string
	category string
}

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
// from the currently selected approach:
//
//	{approach}      → approach ID (e.g. "I2L")
//	{approach_name} → full name (e.g. "ILS Runway 22L")
//	{iaf}           → Initial Approach Fix name
//	{if}            → Intermediate Fix name
//	{faf}           → Final Approach Fix name
func (ds *TestBench) resolveStep(step TestBenchStep) TestBenchStep {
	if ds.selectedApproach < 0 || ds.selectedApproach >= len(ds.approaches) {
		return step
	}
	ap := ds.approaches[ds.selectedApproach]
	iaf, ifFix, faf := approachFixNames(ap.approach)

	r := strings.NewReplacer(
		"{approach}", ap.approachId,
		"{approach_name}", ap.approach.FullName,
		"{iaf}", iaf,
		"{if}", ifFix,
		"{faf}", faf,
	)
	step.Command = r.Replace(step.Command)
	step.ExpectReadback = r.Replace(step.ExpectReadback)
	step.RejectReadback = r.Replace(step.RejectReadback)
	return step
}

func NewTestBench(c *client.ControlClient, es *sim.EventStream, lg *log.Logger) *TestBench {
	ds := &TestBench{
		client: c,
		lg:     lg,
		events: es.Subscribe(),
	}

	// Load debug scenarios via the resource system so the path is
	// resolved regardless of the working directory.
	if util.ResourceExists("test_bench_scenarios.json") {
		data := util.LoadResourceBytes("test_bench_scenarios.json")
		if err := json.Unmarshal(data, &ds.config); err != nil {
			lg.Errorf("test bench: error parsing test_bench_scenarios.json: %v", err)
			ds.config = make(TestBenchConfig)
		}
	} else {
		lg.Warnf("test bench: resources/test_bench_scenarios.json not found")
		ds.config = make(TestBenchConfig)
	}

	ds.refreshSelections()
	return ds
}

func (ds *TestBench) refreshSelections() {
	ds.approaches = nil
	for _, rwy := range ds.client.State.ArrivalRunways {
		ap := ds.client.State.Airports[rwy.Airport]
		if ap == nil {
			continue
		}
		for _, name := range util.SortedMapKeys(ap.Approaches) {
			appr := ap.Approaches[name]
			if appr.Runway == string(rwy.Runway.Base()) {
				ds.approaches = append(ds.approaches, debugApproachInfo{
					airport:    rwy.Airport,
					approachId: name,
					approach:   appr,
				})
			}
		}
	}

	ds.departures = nil
	for _, dr := range ds.client.State.DepartureRunways {
		ds.departures = append(ds.departures, debugDepartureInfo{
			airport:  dr.Airport,
			runway:   string(dr.Runway),
			category: dr.Category,
		})
	}
}

func (ds *TestBench) resetTestState() {
	ds.activeTest = nil
	ds.currentStep = 0
	ds.stepResults = nil
	ds.lastReadback = ""
	ds.lastReadbackCS = ""
}

func (ds *TestBench) Draw(show *bool, p platform.Platform) {
	// Process events for readback tracking
	ds.processEvents()

	imgui.SetNextWindowSizeConstraints(imgui.Vec2{400, 200}, imgui.Vec2{800, float32(p.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Test Bench", show, imgui.WindowFlagsAlwaysAutoResize)

	// Approach selector
	if len(ds.approaches) > 0 {
		label := ds.approachLabel(ds.selectedApproach)
		imgui.Text("Approach:")
		imgui.SameLine()
		imgui.SetNextItemWidth(250)
		if imgui.BeginCombo("##approach", label) {
			for i := range ds.approaches {
				if imgui.SelectableBoolV(ds.approachLabel(i), i == ds.selectedApproach, 0, imgui.Vec2{}) {
					ds.selectedApproach = i
					ds.resetTestState()
				}
			}
			imgui.EndCombo()
		}
	} else {
		imgui.TextColored(imgui.Vec4{1, 1, 0, 1}, "No arrival approaches available")
	}

	// Departure runway selector
	if len(ds.departures) > 0 {
		label := ds.departureLabel(ds.selectedDeparture)
		imgui.Text("Departure:")
		imgui.SameLine()
		imgui.SetNextItemWidth(250)
		if imgui.BeginCombo("##departure", label) {
			for i := range ds.departures {
				if imgui.SelectableBoolV(ds.departureLabel(i), i == ds.selectedDeparture, 0, imgui.Vec2{}) {
					ds.selectedDeparture = i
				}
			}
			imgui.EndCombo()
		}
	}

	if ds.statusMessage != "" {
		imgui.TextColored(imgui.Vec4{0, 1, 0, 1}, ds.statusMessage)
	}

	imgui.Separator()

	// Draw sections
	for _, section := range util.SortedMapKeys(ds.config) {
		cases := ds.config[section]
		if imgui.CollapsingHeaderBoolPtr(section, nil) {
			for i := range cases {
				ds.drawTestCase(section, &cases[i])
			}
		}
	}

	imgui.End()
}

func (ds *TestBench) approachLabel(idx int) string {
	if idx < 0 || idx >= len(ds.approaches) {
		return "(none)"
	}
	a := ds.approaches[idx]
	return fmt.Sprintf("%s - %s (%s)", a.approachId, a.approach.FullName, a.airport)
}

func (ds *TestBench) departureLabel(idx int) string {
	if idx < 0 || idx >= len(ds.departures) {
		return "(none)"
	}
	d := ds.departures[idx]
	if d.category != "" {
		return fmt.Sprintf("%s %s (%s)", d.airport, d.runway, d.category)
	}
	return fmt.Sprintf("%s %s", d.airport, d.runway)
}

func (ds *TestBench) drawTestCase(section string, tc *TestBenchCase) {
	id := section + "/" + tc.Label
	imgui.PushIDStr(id)
	defer imgui.PopID()

	// Bordered group for each test case
	imgui.BeginGroup()

	imgui.Text(tc.Label)
	imgui.SameLine()

	if tc.Group != "" {
		if imgui.Button("Spawn STAR") {
			ds.spawnSTAR(tc)
		}
	} else if tc.Departure {
		if imgui.Button("Spawn Departure") {
			ds.spawnDeparture(tc)
		}
	} else if len(tc.Aircraft) > 0 {
		if imgui.Button("Spawn") {
			ds.spawnAircraft(tc)
		}
	}

	// Clear button — only shown on the test case whose aircraft are active
	if ds.spawnedTest == tc && len(ds.spawnedAircraft) > 0 {
		imgui.SameLine()
		if imgui.Button("Clear") {
			ds.client.DeleteAircraft(ds.spawnedAircraft, func(err error) {
				if err != nil {
					ds.lg.Warnf("test bench: clear aircraft: %v", err)
				}
			})
			ds.spawnedAircraft = nil
			ds.spawnedTest = nil
			ds.resetTestState()
			ds.statusMessage = "Cleared spawned aircraft"
		}
	}

	// Show aircraft specs
	for _, spec := range tc.Aircraft {
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
		imgui.TextColored(imgui.Vec4{0.7, 0.7, 0.7, 1},
			fmt.Sprintf("  %s: %.0fnm%s/%s/%.0fkts%s%s", spec.Callsign,
				spec.DistanceNM, refStr, altStr, spec.Speed, flags, note))
	}

	// Show test steps
	if len(tc.Steps) > 0 {
		for i, rawStep := range tc.Steps {
			step := ds.resolveStep(rawStep)
			isActive := ds.activeTest == tc && ds.currentStep == i

			// Per-step result indicator
			result := ""
			if ds.activeTest == tc && i < len(ds.stepResults) {
				result = ds.stepResults[i]
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
					cs = ds.defaultCallsign(tc)
				}
				imgui.Text(fmt.Sprintf("%s %s", cs, step.Command))
			}
		}

		// Show last captured readback
		if ds.activeTest == tc && ds.lastReadback != "" {
			imgui.TextColored(imgui.Vec4{0.8, 0.8, 0, 1},
				fmt.Sprintf("    Readback [%s]: %s", ds.lastReadbackCS, ds.lastReadback))
		}
	}

	imgui.EndGroup()
	imgui.Separator()
}

func (ds *TestBench) spawnAircraft(tc *TestBenchCase) {
	if ds.selectedApproach < 0 || ds.selectedApproach >= len(ds.approaches) {
		ds.statusMessage = "No approach selected"
		return
	}

	apInfo := ds.approaches[ds.selectedApproach]
	airport := ds.client.State.Airports[apInfo.airport]
	if airport == nil {
		ds.statusMessage = "Airport not found: " + apInfo.airport
		return
	}

	nmPerLong := ds.client.State.NmPerLongitude
	magVar := ds.client.State.MagneticVariation
	rwyHeading := apInfo.approach.RunwayHeading(nmPerLong, magVar)
	ctrlFreq := sim.ControlPosition(ds.client.State.PrimaryPositionForTCW(ds.client.State.UserTCW))

	// Clear any previously spawned aircraft before spawning new ones.
	if len(ds.spawnedAircraft) > 0 {
		ds.client.DeleteAircraft(ds.spawnedAircraft, func(err error) {
			if err != nil {
				ds.lg.Warnf("test bench: auto-clear: %v", err)
			}
		})
	}

	ds.spawnedAircraft = nil
	for _, spec := range tc.Aircraft {
		ac := ds.buildAircraft(spec, airport, apInfo, rwyHeading, nmPerLong, magVar, ctrlFreq)
		ds.client.LaunchArrivalOverflight(ac)
		ds.spawnedAircraft = append(ds.spawnedAircraft, ac)
		ds.lg.Infof("test bench: launched %s at %.1fnm/%.0fft", spec.Callsign, spec.DistanceNM, ac.Nav.FlightState.Altitude)
	}

	ds.spawnedTest = tc
	ds.activateSteps(tc)

	ds.statusMessage = fmt.Sprintf("Spawned %d aircraft for \"%s\"", len(tc.Aircraft), tc.Label)
}

// resolveFixRef resolves a fix name (possibly a placeholder like "{if}")
// to its location and altitude restriction from the selected approach.
func (ds *TestBench) resolveFixRef(name string, apInfo debugApproachInfo) (loc math.Point2LL, alt float32, ok bool) {
	// Resolve placeholders.
	iaf, ifFix, faf := approachFixNames(apInfo.approach)
	name = strings.NewReplacer("{iaf}", iaf, "{if}", ifFix, "{faf}", faf).Replace(name)

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

func (ds *TestBench) buildAircraft(spec TestBenchAircraftSpec, airport *av.Airport,
	apInfo debugApproachInfo, rwyHeading, nmPerLong, magVar float32,
	ctrlFreq sim.ControlPosition) sim.Aircraft {

	// Determine reference point and altitude.
	// If relative_to is set, position relative to that fix;
	// otherwise position relative to the airport.
	refPos := airport.Location
	altitude := spec.Altitude

	if spec.RelativeTo != "" {
		if fixLoc, fixAlt, ok := ds.resolveFixRef(spec.RelativeTo, apInfo); ok {
			refPos = fixLoc
			if altitude == 0 && fixAlt > 0 {
				altitude = fixAlt
			}
		} else {
			ds.lg.Warnf("test bench: couldn't resolve fix %q", spec.RelativeTo)
		}
	}

	// Determine the bearing from reference to place the aircraft.
	// Default: on extended centerline (reciprocal of runway heading).
	bearing := rwyHeading + 180
	if spec.Bearing != 0 {
		bearing = spec.Bearing
	}

	pos := math.Offset2LL(refPos, bearing, spec.DistanceNM, nmPerLong, magVar)

	// Aircraft heading: default is inbound (runway heading).
	heading := rwyHeading
	if spec.Heading != 0 {
		heading = spec.Heading
	}

	// Aircraft type is hardcoded to B738 for now; may want to make this
	// configurable if we need to test category-specific behavior.
	ac := sim.Aircraft{
		ADSBCallsign:        av.ADSBCallsign(spec.Callsign),
		TypeOfFlight:        av.FlightTypeArrival,
		Mode:                av.TransponderModeAltitude,
		ControllerFrequency: ctrlFreq,
		FlightPlan: av.FlightPlan{
			ArrivalAirport: apInfo.airport,
			AircraftType:   "B738",
			Rules:          av.FlightRulesIFR,
		},
		Nav: nav.Nav{
			Rand:           rand.Make(),
			FixAssignments: make(map[string]nav.NavFixAssignment),
			Perf:           av.DB.AircraftPerformance["B738"],
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
			ACID:               sim.ACID(spec.Callsign),
			ArrivalAirport:     apInfo.airport,
			Rules:              av.FlightRulesIFR,
			TypeOfFlight:       av.FlightTypeArrival,
			AircraftCount:      1,
			AircraftType:       "B738",
			TrackingController: ctrlFreq,
			OwningTCW:          ds.client.State.UserTCW,
		},
	}

	return ac
}

func (ds *TestBench) spawnSTAR(tc *TestBenchCase) {
	airport := tc.Airport
	if airport == "" {
		airport = ds.client.State.PrimaryAirport
	}

	var ac sim.Aircraft
	ds.client.CreateArrival(tc.Group, airport, &ac, func(err error) {
		if err != nil {
			ds.lg.Warnf("test bench: CreateArrival %s: %v", tc.Group, err)
			ds.statusMessage = fmt.Sprintf("STAR spawn failed: %v", err)
		} else {
			ds.spawnedAircraft = append(ds.spawnedAircraft, ac)
			ds.spawnedTest = tc
			ds.lg.Infof("test bench: STAR %s spawned for %s (%s)", tc.Group, airport, ac.ADSBCallsign)
			ds.statusMessage = fmt.Sprintf("Spawned STAR %s at %s (%s)", tc.Group, airport, ac.ADSBCallsign)
			ds.activateSteps(tc)
		}
	})
}

func (ds *TestBench) spawnDeparture(tc *TestBenchCase) {
	airport := tc.Airport
	runway := tc.Runway
	category := tc.Category

	// Use the departure dropdown selection if the scenario doesn't specify.
	if airport == "" || runway == "" {
		if ds.selectedDeparture >= 0 && ds.selectedDeparture < len(ds.departures) {
			d := ds.departures[ds.selectedDeparture]
			if airport == "" {
				airport = d.airport
			}
			if runway == "" {
				runway = d.runway
				category = d.category
			}
		} else {
			ds.statusMessage = "No departure runway selected"
			return
		}
	}
	if airport == "" || runway == "" {
		ds.statusMessage = "No departure runway selected"
		return
	}

	var ac sim.Aircraft
	ds.client.CreateDeparture(airport, runway, category, av.FlightRulesIFR, &ac, func(err error) {
		if err != nil {
			ds.lg.Warnf("test bench: CreateDeparture: %v", err)
			ds.statusMessage = fmt.Sprintf("Departure spawn failed: %v", err)
		} else {
			// Launch the departure immediately (same pattern as LaunchControl).
			// CreateDeparture builds the aircraft; LaunchDeparture puts it on scope.
			ds.client.LaunchDeparture(ac, runway)

			ds.spawnedAircraft = append(ds.spawnedAircraft, ac)
			ds.spawnedTest = tc
			ds.lg.Infof("test bench: departure %s launched at %s rwy %s", ac.ADSBCallsign, airport, runway)
			ds.statusMessage = fmt.Sprintf("Launched departure %s at %s rwy %s", ac.ADSBCallsign, airport, runway)
			ds.activateSteps(tc)
		}
	})
}

// isTestCallsign checks whether the given callsign belongs to one of the
// test case's aircraft — either from the JSON spec or from sim-spawned aircraft.
func (ds *TestBench) isTestCallsign(tc *TestBenchCase, cs av.ADSBCallsign) bool {
	for _, spec := range tc.Aircraft {
		if av.ADSBCallsign(spec.Callsign) == cs {
			return true
		}
	}
	// For departures/STARs, check spawned aircraft (callsign assigned by sim).
	if ds.spawnedTest == tc {
		for _, ac := range ds.spawnedAircraft {
			if ac.ADSBCallsign == cs {
				return true
			}
		}
	}
	return false
}

// defaultCallsign returns the callsign to use when a step doesn't specify one.
func (ds *TestBench) defaultCallsign(tc *TestBenchCase) av.ADSBCallsign {
	if len(tc.Aircraft) > 0 {
		return av.ADSBCallsign(tc.Aircraft[0].Callsign)
	}
	if ds.spawnedTest == tc && len(ds.spawnedAircraft) > 0 {
		return ds.spawnedAircraft[0].ADSBCallsign
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
	}
	return false
}

func (ds *TestBench) processEvents() {
	if ds.activeTest == nil {
		// Still drain events so the subscription doesn't fall behind.
		ds.events.Get()
		return
	}

	for _, event := range ds.events.Get() {
		if event.Type != sim.RadioTransmissionEvent {
			continue
		}

		// Check if this is from one of our test callsigns.
		tc := ds.activeTest
		if !ds.isTestCallsign(tc, event.ADSBCallsign) {
			continue
		}

		if ds.currentStep >= len(tc.Steps) {
			continue
		}
		step := ds.resolveStep(tc.Steps[ds.currentStep])

		// For wait_for steps, accept contact/unexpected transmissions
		// (spontaneous pilot reports).
		if step.WaitFor != "" {
			if matchWaitFor(step.WaitFor, event) {
				ds.stepResults[ds.currentStep] = "pass"
				ds.lastReadback = event.WrittenText
				ds.lastReadbackCS = event.ADSBCallsign
				ds.advanceStep()
			}
			continue
		}

		// For command steps, only evaluate actual readbacks — not
		// check-ins (contact) or other spontaneous transmissions.
		if event.RadioTransmissionType != av.RadioTransmissionReadback {
			continue
		}

		// Check the step's target callsign
		targetCS := av.ADSBCallsign(step.Callsign)
		if targetCS == "" {
			targetCS = ds.defaultCallsign(tc)
		}
		if targetCS != event.ADSBCallsign {
			continue
		}

		ds.lastReadback = event.WrittenText
		ds.lastReadbackCS = event.ADSBCallsign

		// "Say again" means the pilot didn't understand; skip it so
		// the controller can re-issue the command.
		text := strings.ToLower(event.WrittenText)
		if strings.Contains(text, "say again") {
			continue
		}

		// If the command step has no readback expectations, it's a
		// "fire and forget" step — pass the readback through so the
		// next step (e.g. wait_for) can evaluate it.
		if step.ExpectReadback == "" && step.RejectReadback == "" {
			ds.stepResults[ds.currentStep] = "pass"
			ds.advanceStep()
			// Re-evaluate this same event against the new current step.
			if ds.currentStep < len(tc.Steps) {
				next := ds.resolveStep(tc.Steps[ds.currentStep])
				if next.WaitFor != "" && matchWaitFor(next.WaitFor, event) {
					ds.stepResults[ds.currentStep] = "pass"
					ds.lastReadback = event.WrittenText
					ds.lastReadbackCS = event.ADSBCallsign
					ds.advanceStep()
				}
			}
			continue
		}

		// Verify readback against WrittenText (the structured form,
		// not the randomized spoken form).
		pass := true
		if !strings.Contains(text, strings.ToLower(step.ExpectReadback)) {
			pass = false
		}
		if step.RejectReadback != "" && strings.Contains(text, strings.ToLower(step.RejectReadback)) {
			pass = false
		}

		ds.lg.Debugf("test bench: step %d cmd=%q expect=%q reject=%q written=%q pass=%v",
			ds.currentStep, step.Command, step.ExpectReadback, step.RejectReadback, event.WrittenText, pass)

		if pass {
			ds.stepResults[ds.currentStep] = "pass"
		} else {
			ds.stepResults[ds.currentStep] = "fail"
		}
		ds.advanceStep()
	}
}

func (ds *TestBench) activateSteps(tc *TestBenchCase) {
	if len(tc.Steps) > 0 {
		ds.activeTest = tc
		ds.currentStep = 0
		ds.stepResults = make([]string, len(tc.Steps))
		ds.lastReadback = ""
		ds.lastReadbackCS = ""
	}
}

func (ds *TestBench) advanceStep() {
	ds.currentStep++
}
