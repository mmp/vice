// nav/nav_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"encoding/json"
	gomath "math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func TestMain(m *testing.M) {
	av.InitDB()
	os.Exit(m.Run())
}

// FlightTest orchestrates a simulated flight with events and assertions.
type FlightTest struct {
	t        *testing.T
	nav      *Nav
	fp       av.FlightPlan
	callsign string
	simTime  Time
	maxTicks int
	weather  func(float32) wx.Sample
	events   []flightEvent
	passed   []string // fixes passed so far
	tick     int
}

// flightEvent is a scheduled action or assertion.
type flightEvent struct {
	name    string
	trigger eventTrigger
	action  func(f *FlightTest)
	fired   bool
	active  bool // for betweenFixes: start fix has been passed
}

type eventTrigger struct {
	atFix        string // fire when this fix is passed
	beforeFix    string // fire every tick; fail if fix passed without it holding
	atTick       int    // fire at this tick number
	betweenStart string // fire every tick between start and end fixes
	betweenEnd   string
}

// ArrivalConfig configures a test arrival flight.
type ArrivalConfig struct {
	Waypoints        string  // waypoint string ("PARCH CAMRN/a13000 ...")
	DepartureAirport string  // ICAO code
	ArrivalAirport   string  // ICAO code
	AircraftType     string  // e.g. "A320", "B738"
	InitialAltitude  float32 // starting altitude in feet
	InitialSpeed     float32 // starting IAS in knots
	AssignedAltitude float32 // 0 = none
	ClearedAltitude  float32 // 0 = none
	InitialHeading   float32 // 0 = compute from route, non-zero = start in heading mode
	OnSTAR           bool    // set OnSTAR flag on all waypoints
}

// NewArrivalFlight creates a FlightTest from an ArrivalConfig.
func NewArrivalFlight(t *testing.T, cfg ArrivalConfig) *FlightTest {
	t.Helper()

	wps := parseRoute(t, cfg.Waypoints)
	if cfg.OnSTAR {
		for i := range wps {
			wps[i].SetOnSTAR(true)
		}
	}

	perf, ok := av.DB.AircraftPerformance[cfg.AircraftType]
	if !ok {
		if alias, aok := av.DB.AircraftTypeAliases[cfg.AircraftType]; aok {
			perf, ok = av.DB.AircraftPerformance[alias]
		}
		if !ok {
			t.Fatalf("unknown aircraft type %q", cfg.AircraftType)
		}
	}

	arrAirport, ok := av.DB.Airports[cfg.ArrivalAirport]
	if !ok {
		t.Fatalf("unknown arrival airport %q", cfg.ArrivalAirport)
	}
	depAirport, ok := av.DB.Airports[cfg.DepartureAirport]
	if !ok {
		t.Fatalf("unknown departure airport %q", cfg.DepartureAirport)
	}

	nmPerLongitude := math.NMPerLongitudeAt(arrAirport.Location)
	magneticVariation, err := av.DB.MagneticGrid.Lookup(arrAirport.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}

	// Deterministic randomness
	rng := &rand.Rand{PCG32: rand.NewPCG32()}
	rng.Seed(42)

	fp := av.FlightPlan{
		Rules:            av.FlightRulesIFR,
		AircraftType:     cfg.AircraftType,
		DepartureAirport: cfg.DepartureAirport,
		ArrivalAirport:   cfg.ArrivalAirport,
		Altitude:         int(cfg.InitialAltitude),
	}

	// Build waypoints with arrival airport at the end
	navWps := make([]av.Waypoint, len(wps)+1)
	copy(navWps, wps)
	navWps[len(wps)] = av.Waypoint{
		Fix:      cfg.ArrivalAirport,
		Location: arrAirport.Location,
	}

	// Compute initial heading from first to second waypoint
	var initialHeading math.MagneticHeading
	if cfg.InitialHeading != 0 {
		initialHeading = math.MagneticHeading(cfg.InitialHeading)
	} else if len(navWps) > 1 {
		trueHdg := math.Heading2LL(navWps[0].Location, navWps[1].Location, nmPerLongitude)
		initialHeading = math.TrueToMagnetic(trueHdg, magneticVariation)
	}

	n := &Nav{
		Perf:           perf,
		FinalAltitude:  cfg.InitialAltitude,
		FixAssignments: make(map[string]NavFixAssignment),
		Rand:           rng,
		Waypoints:      navWps,
		FlightState: FlightState{
			MagneticVariation:         magneticVariation,
			NmPerLongitude:            nmPerLongitude,
			Position:                  navWps[0].Location,
			Heading:                   initialHeading,
			Altitude:                  cfg.InitialAltitude,
			IAS:                       cfg.InitialSpeed,
			GS:                        cfg.InitialSpeed,
			DepartureAirportLocation:  depAirport.Location,
			DepartureAirportElevation: float32(depAirport.Elevation),
			ArrivalAirportLocation:    arrAirport.Location,
			ArrivalAirportElevation:   float32(arrAirport.Elevation),
			ArrivalAirport: av.Waypoint{
				Fix:      cfg.ArrivalAirport,
				Location: arrAirport.Location,
			},
		},
	}

	if cfg.AssignedAltitude > 0 {
		n.setAssignedAltitude(cfg.AssignedAltitude)
	}
	if cfg.ClearedAltitude > 0 {
		alt := cfg.ClearedAltitude
		n.Altitude.Cleared = &alt
	}
	if cfg.InitialHeading != 0 {
		hdg := math.MagneticHeading(cfg.InitialHeading)
		n.Heading.Assigned = &hdg
	}

	return &FlightTest{
		t:        t,
		nav:      n,
		fp:       fp,
		callsign: "TEST001",
		simTime:  NewTime(time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)),
		maxTicks: 7200,
		weather:  func(alt float32) wx.Sample { return wx.MakeStandardSampleForAltitude(alt) },
	}
}

func TestContactMessageIncludesCrossFixAltitude(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	ar := av.MakeAtAltitudeRestriction(8000)
	f.nav.CrossFixAt("DETGY", &ar, nil)

	written := strings.ToLower(f.nav.ContactMessage(nil, "", "", false, false).Written(f.nav.Rand))
	if !strings.Contains(written, "cross") || !strings.Contains(written, "detgy") || !strings.Contains(written, "8,000") {
		t.Fatalf("contact message missing cross-fix altitude restriction: %q", written)
	}
}

func TestContactMessageIncludesCrossFixSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	sr := av.MakeAtSpeedRestriction(230)
	f.nav.CrossFixAt("DETGY", nil, &sr)

	written := strings.ToLower(f.nav.ContactMessage(nil, "", "", false, false).Written(f.nav.Rand))
	if !strings.Contains(written, "cross") || !strings.Contains(written, "detgy") || !strings.Contains(written, "230 knots") {
		t.Fatalf("contact message missing cross-fix speed restriction: %q", written)
	}
}

func TestContactMessageIncludesCrossDMEAltitude(t *testing.T) {
	f := setupClearedVisual(t, "22L")

	ar := av.MakeAtAltitudeRestriction(3000)
	f.nav.CrossDMEAt(5, &ar, nil)

	written := strings.ToLower(f.nav.ContactMessage(nil, "", "", false, false).Written(f.nav.Rand))
	if !strings.Contains(written, "cross") || !strings.Contains(written, "5 d m e") ||
		!strings.Contains(written, "3,000") {
		t.Fatalf("contact message missing cross-DME restriction: %q", written)
	}
}

func TestContactMessageIncludesCrossDistanceAltitudeAndSpeed(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "SAJUL/a10000/star DETGY/a7000/star HAUPT/a6000/star",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})

	fixLoc := f.nav.Waypoints[1].Location
	priorLoc := f.nav.Waypoints[0].Location
	approachHeading := math.Heading2LL(fixLoc, priorLoc, f.nav.FlightState.NmPerLongitude)
	dir, err := math.ParseCardinalOrdinalDirection(math.ShortCompass(approachHeading))
	if err != nil {
		t.Fatalf("failed to parse direction: %v", err)
	}

	ar := av.MakeAtAltitudeRestriction(8000)
	sr := av.MakeAtSpeedRestriction(230)
	f.nav.CrossDistanceFromFixAt("DETGY", 5, dir, &ar, &sr)

	written := strings.ToLower(f.nav.ContactMessage(nil, "", "", false, false).Written(f.nav.Rand))
	if !strings.Contains(written, "cross") || !strings.Contains(written, "detgy") ||
		!strings.Contains(written, "8,000") || !strings.Contains(written, "230 knots") {
		t.Fatalf("contact message missing cross-distance restriction: %q", written)
	}
}

// AtFix fires action when the named fix is passed.
func (f *FlightTest) AtFix(fix string, action func(*FlightTest)) *FlightTest {
	f.events = append(f.events, flightEvent{
		name:    "atFix:" + fix,
		trigger: eventTrigger{atFix: fix},
		action:  action,
	})
	return f
}

// BeforeFix fires action every tick until the fix is passed.
// The check must hold continuously; the fix must eventually be passed.
func (f *FlightTest) BeforeFix(fix string, check func(*FlightTest)) *FlightTest {
	f.events = append(f.events, flightEvent{
		name:    "beforeFix:" + fix,
		trigger: eventTrigger{beforeFix: fix},
		action:  check,
	})
	return f
}

// AfterTicks fires action at the given tick count.
func (f *FlightTest) AfterTicks(n int, action func(*FlightTest)) *FlightTest {
	f.events = append(f.events, flightEvent{
		name:    "afterTicks",
		trigger: eventTrigger{atTick: n},
		action:  action,
	})
	return f
}

// BetweenFixes fires check every tick after fixA is passed and before fixB
// is passed. Useful for asserting behavior over a leg of the route.
func (f *FlightTest) BetweenFixes(fixA, fixB string, check func(*FlightTest)) *FlightTest {
	f.events = append(f.events, flightEvent{
		name:    "betweenFixes:" + fixA + "-" + fixB,
		trigger: eventTrigger{betweenStart: fixA, betweenEnd: fixB},
		action:  check,
	})
	return f
}

// Run executes the simulation loop.
func (f *FlightTest) Run() {
	f.t.Helper()

	for f.tick = 0; f.tick < f.maxTicks; f.tick++ {
		wxs := f.weather(f.nav.FlightState.Altitude)
		passedWp := f.nav.UpdateWithWeather(f.callsign, wxs, &f.fp, f.simTime, nil).PassedWaypoint

		if passedWp != nil {
			f.passed = append(f.passed, passedWp.Fix)
			f.fireAtFixEvents(passedWp.Fix)
			f.resolveBeforeFixEvents(passedWp.Fix)
			f.activateBetweenFixesEvents(passedWp.Fix)
			f.resolveBetweenFixesEvents(passedWp.Fix)
		}

		f.fireBeforeFixEvents()
		f.fireBetweenFixesEvents()
		f.fireAtTickEvents()

		// Advance simulation time by 1 second per tick
		f.simTime = f.simTime.Add(time.Second)

		// Stop when all events have fired
		if f.allEventsFired() {
			return
		}

		// Stop if aircraft has descended to near airport elevation
		if f.nav.FlightState.Altitude <= f.nav.FlightState.ArrivalAirportElevation+100 {
			break
		}
	}

	// Verify all fix-triggered events actually fired
	for _, e := range f.events {
		if !e.fired {
			if e.trigger.atFix != "" {
				f.t.Errorf("fix %q was never reached (passed: %v)", e.trigger.atFix, f.passed)
			}
			if e.trigger.beforeFix != "" {
				f.t.Errorf("fix %q was never reached, beforeFix check never ran (passed: %v)", e.trigger.beforeFix, f.passed)
			}
			if e.trigger.betweenEnd != "" {
				f.t.Errorf("fix %q was never reached for betweenFixes check (passed: %v)", e.trigger.betweenEnd, f.passed)
			}
		}
	}
}

func (f *FlightTest) fireAtFixEvents(fix string) {
	for i := range f.events {
		if f.events[i].trigger.atFix == fix && !f.events[i].fired {
			f.events[i].action(f)
			f.events[i].fired = true
		}
	}
}

func (f *FlightTest) resolveBeforeFixEvents(fix string) {
	for i := range f.events {
		if f.events[i].trigger.beforeFix == fix {
			f.events[i].fired = true
		}
	}
}

func (f *FlightTest) fireBeforeFixEvents() {
	for i := range f.events {
		e := &f.events[i]
		if e.trigger.beforeFix != "" && !e.fired {
			e.action(f)
		}
	}
}

func (f *FlightTest) fireAtTickEvents() {
	for i := range f.events {
		e := &f.events[i]
		if e.trigger.atTick > 0 && f.tick == e.trigger.atTick && !e.fired {
			e.action(f)
			e.fired = true
		}
	}
}

func (f *FlightTest) activateBetweenFixesEvents(fix string) {
	for i := range f.events {
		if f.events[i].trigger.betweenStart == fix {
			f.events[i].active = true
		}
	}
}

func (f *FlightTest) resolveBetweenFixesEvents(fix string) {
	for i := range f.events {
		if f.events[i].trigger.betweenEnd == fix && f.events[i].active {
			f.events[i].fired = true
		}
	}
}

func (f *FlightTest) fireBetweenFixesEvents() {
	for i := range f.events {
		e := &f.events[i]
		if e.trigger.betweenStart != "" && e.active && !e.fired {
			e.action(f)
		}
	}
}

func (f *FlightTest) allEventsFired() bool {
	return !slices.ContainsFunc(f.events, func(e flightEvent) bool { return !e.fired })
}

// Assertion helpers

func (f *FlightTest) AssertAltitudeBelow(alt float32) {
	f.t.Helper()
	if f.nav.FlightState.Altitude > alt {
		f.t.Errorf("tick %d: altitude %.0f exceeds %.0f", f.tick, f.nav.FlightState.Altitude, alt)
	}
}

func (f *FlightTest) AssertAltitudeNear(alt, tolerance float32) {
	f.t.Helper()
	if f.nav.FlightState.Altitude < alt-tolerance || f.nav.FlightState.Altitude > alt+tolerance {
		f.t.Errorf("tick %d: altitude %.0f not within %.0f of %.0f",
			f.tick, f.nav.FlightState.Altitude, tolerance, alt)
	}
}

func (f *FlightTest) AssertAltitudeAbove(alt float32) {
	f.t.Helper()
	if f.nav.FlightState.Altitude < alt {
		f.t.Errorf("tick %d: altitude %.0f below %.0f", f.tick, f.nav.FlightState.Altitude, alt)
	}
}

func (f *FlightTest) AssertDescending() {
	f.t.Helper()
	if f.nav.FlightState.AltitudeRate >= 0 {
		f.t.Errorf("tick %d: expected descending but altitude rate is %.0f", f.tick, f.nav.FlightState.AltitudeRate)
	}
}

func (f *FlightTest) AssertSpeedBelow(spd float32) {
	f.t.Helper()
	if f.nav.FlightState.IAS > spd {
		f.t.Errorf("tick %d: IAS %.0f exceeds %.0f", f.tick, f.nav.FlightState.IAS, spd)
	}
}

func (f *FlightTest) AssertSpeedAbove(spd float32) {
	f.t.Helper()
	if f.nav.FlightState.IAS < spd {
		f.t.Errorf("tick %d: IAS %.0f below %.0f", f.tick, f.nav.FlightState.IAS, spd)
	}
}

func (f *FlightTest) AssertSpeedNear(spd, tolerance float32) {
	f.t.Helper()
	if f.nav.FlightState.IAS < spd-tolerance || f.nav.FlightState.IAS > spd+tolerance {
		f.t.Errorf("tick %d: IAS %.0f not within %.0f of %.0f",
			f.tick, f.nav.FlightState.IAS, tolerance, spd)
	}
}

func (f *FlightTest) AssertHeadingNear(hdg float32, tolerance float32) {
	f.t.Helper()
	diff := math.HeadingDifference(f.nav.FlightState.Heading, math.MagneticHeading(hdg))
	if diff > tolerance {
		f.t.Errorf("tick %d: heading %.0f not within %.0f of %.0f",
			f.tick, float32(f.nav.FlightState.Heading), tolerance, hdg)
	}
}

func (f *FlightTest) AssertClimbing() {
	f.t.Helper()
	if f.nav.FlightState.AltitudeRate <= 0 {
		f.t.Errorf("tick %d: expected climbing but altitude rate is %.0f", f.tick, f.nav.FlightState.AltitudeRate)
	}
}

func (f *FlightTest) AssertLevelFlight() {
	f.t.Helper()
	if math.Abs(f.nav.FlightState.AltitudeRate) > 50 {
		f.t.Errorf("tick %d: expected level flight but altitude rate is %.0f", f.tick, f.nav.FlightState.AltitudeRate)
	}
}

func (f *FlightTest) AssertNotDescending() {
	f.t.Helper()
	if f.nav.FlightState.AltitudeRate < -50 {
		f.t.Errorf("tick %d: expected not descending but altitude rate is %.0f", f.tick, f.nav.FlightState.AltitudeRate)
	}
}

// AssertOnExtendedCenterline checks that the aircraft is within the given
// tolerance (in nm) of the approach extended centerline.
func (f *FlightTest) AssertOnExtendedCenterline(toleranceNM float32) {
	f.t.Helper()
	if !f.nav.OnExtendedCenterline(toleranceNM) {
		f.t.Errorf("tick %d: aircraft is more than %.1fnm from the extended centerline",
			f.tick, toleranceNM)
	}
}

// SignedCenterlineDistance returns the signed perpendicular distance (in nm)
// from the aircraft to the approach extended centerline. Positive values
// are to one side, negative to the other (consistent with
// math.SignedPointLineDistance). Requires an approach to be assigned.
func (f *FlightTest) SignedCenterlineDistance() float32 {
	f.t.Helper()
	ap := f.nav.Approach.Assigned
	if ap == nil {
		f.t.Fatalf("tick %d: SignedCenterlineDistance called with no approach assigned", f.tick)
	}
	nmPerLong := f.nav.FlightState.NmPerLongitude
	magVar := f.nav.FlightState.MagneticVariation
	cl := ap.ExtendedCenterline(nmPerLong, magVar)
	acftNM := math.LL2NM(f.nav.FlightState.Position, nmPerLong)
	cl0NM := math.LL2NM(cl[0], nmPerLong)
	cl1NM := math.LL2NM(cl[1], nmPerLong)
	return math.SignedPointLineDistance(acftNM, cl0NM, cl1NM)
}

// AssertUnable checks that the given CommandIntent is an UnableIntent.
func AssertUnable(t *testing.T, intent av.CommandIntent) {
	t.Helper()
	if _, ok := intent.(av.UnableIntent); !ok {
		t.Errorf("expected UnableIntent, got %T: %v", intent, intent)
	}
}

// Command helpers

func (f *FlightTest) AssignAltitude(alt float32) {
	f.t.Helper()
	f.nav.AssignAltitude(alt, false, f.simTime, 0)
}

func (f *FlightTest) AssignSpeed(spd float32) {
	f.t.Helper()
	sr := av.MakeAtSpeedRestriction(spd)
	f.nav.AssignSpeed(&sr, false)
}

func (f *FlightTest) ExpectApproach(id string) {
	f.t.Helper()
	airport := f.makeAirport()
	f.nav.ExpectApproach(airport, id, nil)
}

func (f *FlightTest) ExpectVisualApproach(runway string) av.CommandIntent {
	f.t.Helper()
	airport := f.makeAirport()
	return f.nav.ExpectApproach(airport, "_VIS"+runway, nil)
}

func (f *FlightTest) ClearedVisualApproach(runway string) av.CommandIntent {
	f.t.Helper()
	return f.nav.ClearedApproach("_VIS"+runway, nil, f.simTime, false)
}

// makeAirport constructs an *av.Airport from the FAAAirport in av.DB,
// resolving approach waypoint locations and adding runway threshold
// waypoints — mirroring the essential parts of Airport.PostDeserialize.
func (f *FlightTest) makeAirport() *av.Airport {
	icao := f.fp.ArrivalAirport
	faa, ok := av.DB.Airports[icao]
	if !ok {
		f.t.Fatalf("unknown airport %q", icao)
	}

	nmPerLong := f.nav.FlightState.NmPerLongitude
	magVar := f.nav.FlightState.MagneticVariation
	e := &util.ErrorLogger{}

	approaches := make(map[string]*av.Approach)
	for name, appr := range faa.Approaches {
		a := appr
		// Deep-copy waypoint slices so we don't mutate the database.
		a.Waypoints = make([]av.WaypointArray, len(appr.Waypoints))
		for i, route := range appr.Waypoints {
			a.Waypoints[i] = util.DuplicateSlice(route)
		}

		// Resolve fix names to lat/lon coordinates.
		for i := range a.Waypoints {
			a.Waypoints[i] = a.Waypoints[i].InitializeLocations(dbLocator{}, nmPerLong, magVar, true, e)
			for j := range a.Waypoints[i] {
				a.Waypoints[i][j].SetOnApproach(true)
			}
		}

		// Add runway threshold waypoint to each route.
		if rwy, ok := av.LookupRunway(icao, a.Runway); ok {
			a.Threshold = rwy.Threshold
			for i := range a.Waypoints {
				alt := rwy.Elevation + rwy.ThresholdCrossingHeight
				threshold := math.Offset2LL(rwy.Threshold,
					math.MagneticToTrue(rwy.Heading, magVar),
					rwy.DisplacedThresholdDistance, nmPerLong)
				thresholdWP := av.Waypoint{
					Fix:      "_" + a.Runway + "_THRESHOLD",
					Location: threshold,
				}
				thresholdWP.SetLand(true)
				thresholdWP.SetFlyOver(true)
				thresholdWP.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(alt)))
				a.Waypoints[i] = append(a.Waypoints[i], thresholdWP)
			}
		}

		if opp, ok := av.LookupOppositeRunway(icao, a.Runway); ok {
			a.OppositeThreshold = opp.Threshold
		}

		approaches[name] = &a
	}
	return &av.Airport{
		Location:   faa.Location,
		Approaches: approaches,
	}
}

func (f *FlightTest) ClearedApproach(id string) {
	f.t.Helper()
	f.nav.ClearedApproach(id, nil, f.simTime, false)
}

func (f *FlightTest) AssignHeading(hdg int, turn av.TurnDirection) {
	f.t.Helper()
	f.nav.AssignHeading(math.MagneticHeading(hdg), turn, f.simTime, 0)
}

func (f *FlightTest) DirectFix(fix string) {
	f.t.Helper()
	f.nav.DirectFix(fix, av.TurnClosest, f.simTime, 0)
}

func (f *FlightTest) DirectFixWithTurn(fix string, turn av.TurnDirection) {
	f.t.Helper()
	f.nav.DirectFix(fix, turn, f.simTime, 0)
}

func (f *FlightTest) ExpediteDescent() {
	f.t.Helper()
	f.nav.ExpediteDescent()
}

func (f *FlightTest) ExpediteDescentThrough(alt float32) {
	f.t.Helper()
	f.nav.ExpediteDescentThrough(alt)
}

func (f *FlightTest) GoodRateDescent() {
	f.t.Helper()
	f.nav.GoodRateDescent()
}

func (f *FlightTest) DescendViaSTAR() {
	f.t.Helper()
	f.nav.DescendViaSTAR(f.simTime)
}

func (f *FlightTest) AfterFixSpeed(fix string, spd float32) {
	f.t.Helper()
	sr := av.MakeAtSpeedRestriction(spd)
	f.nav.AfterFixSpeed(fix, &sr)
}

func (f *FlightTest) AfterFixAltitude(fix string, alt float32) {
	f.t.Helper()
	f.nav.AfterFixAltitude(fix, alt)
}

func (f *FlightTest) CompoundSpeed(segments []av.CompoundSpeedSegment) {
	f.t.Helper()
	f.nav.AssignCompoundSpeed(segments)
}

func (f *FlightTest) AtFixCleared(fix, approach string, straightIn bool) {
	f.t.Helper()
	f.nav.AtFixCleared(fix, approach, f.simTime, 0, straightIn)
}

func (f *FlightTest) AtFixIntercept(fix string) av.CommandIntent {
	f.t.Helper()
	return f.nav.AtFixIntercept(fix, f.simTime, 0)
}

func (f *FlightTest) InterceptApproach() av.CommandIntent {
	f.t.Helper()
	return f.nav.InterceptApproach(f.fp.ArrivalAirport, nil)
}

// SetWind configures a constant wind from the given direction (degrees true)
// at the given speed (knots). The wind is overlaid on standard atmosphere.
func (f *FlightTest) SetWind(fromDir, speedKts float32) *FlightTest {
	f.weather = func(alt float32) wx.Sample {
		std := wx.MakeStandardSampleForAltitude(alt)
		// Convert meteorological "from" direction and speed in knots to a
		// velocity vector in nm/s. Wind blows FROM fromDir, so the velocity
		// vector points in the opposite direction.
		toDir := fromDir + 180
		rad := float32(gomath.Pi) * toDir / 180
		speedNmPerSec := speedKts / 3600
		windVec := [2]float32{
			speedNmPerSec * float32(gomath.Sin(float64(rad))),
			speedNmPerSec * float32(gomath.Cos(float64(rad))),
		}
		return wx.MakeSample(windVec, std.Temperature().Celsius(), std.Dewpoint().Celsius(), std.Pressure())
	}
	return f
}

// dbLocator implements av.Locator using the static aviation database.
type dbLocator struct{}

func (dbLocator) Locate(fix string) (math.Point2LL, bool) {
	if p, ok := av.DB.LookupWaypoint(fix); ok {
		return p, true
	}
	if p, err := math.ParseLatLong([]byte(fix)); err == nil {
		return p, true
	}
	return math.Point2LL{}, false
}

func (dbLocator) Similar(fix string) []string { return nil }

// parseRoute parses a waypoint string using the scenario JSON format
// and resolves fix locations from av.DB. Requires av.DB to be initialized.
func parseRoute(t *testing.T, s string) av.WaypointArray {
	t.Helper()
	var wps av.WaypointArray
	// WaypointArray.UnmarshalJSON expects a JSON-encoded string
	jsonStr, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("failed to marshal waypoint string: %v", err)
	}
	if err := json.Unmarshal(jsonStr, &wps); err != nil {
		t.Fatalf("failed to parse waypoints %q: %v", s, err)
	}
	if len(wps) == 0 {
		t.Fatalf("no waypoints parsed from %q", s)
	}
	// Resolve fix names to coordinates
	e := &util.ErrorLogger{}
	wps = wps.InitializeLocations(dbLocator{}, 0, 0, false, e)
	if e.HaveErrors() {
		t.Fatalf("failed to resolve waypoint locations: %s", e.String())
	}
	return wps
}

// ApproachGeometry holds the essential geometric properties of an approach,
// allowing tests to compute positions relative to the threshold or FAF.
type ApproachGeometry struct {
	Threshold         math.Point2LL
	NmPerLongitude    float32
	MagneticVariation float32
	RunwayHeading     math.TrueHeading
	FAFLocation       math.Point2LL
}

// LookupApproachGeometry resolves the named approach from the aviation
// database, initializes its waypoint locations, and returns the geometry.
func LookupApproachGeometry(t *testing.T, airport, approachID string) ApproachGeometry {
	t.Helper()

	faa, ok := av.DB.Airports[airport]
	if !ok {
		t.Fatalf("unknown airport %q", airport)
	}

	appr, ok := faa.Approaches[approachID]
	if !ok {
		t.Fatalf("unknown approach %q at %s", approachID, airport)
	}

	nmPerLong := math.NMPerLongitudeAt(faa.Location)
	magVar, err := av.DB.MagneticGrid.Lookup(faa.Location)
	if err != nil {
		t.Fatalf("magnetic grid lookup failed: %v", err)
	}

	// Deep-copy and initialize waypoint locations.
	a := appr
	a.Waypoints = make([]av.WaypointArray, len(appr.Waypoints))
	e := &util.ErrorLogger{}
	for i, route := range appr.Waypoints {
		a.Waypoints[i] = util.DuplicateSlice(route)
		a.Waypoints[i] = a.Waypoints[i].InitializeLocations(dbLocator{}, nmPerLong, magVar, true, e)
	}
	if e.HaveErrors() {
		t.Fatalf("failed to resolve approach waypoint locations: %s", e.String())
	}

	// Set threshold from runway data.
	rwy, ok := av.LookupRunway(airport, a.Runway)
	if !ok {
		t.Fatalf("unknown runway %q at %s", a.Runway, airport)
	}
	a.Threshold = rwy.Threshold
	if opp, ok := av.LookupOppositeRunway(airport, a.Runway); ok {
		a.OppositeThreshold = opp.Threshold
	}

	rwyHdg := a.RunwayHeading(nmPerLong)

	// Find FAF location.
	var fafLoc math.Point2LL
	if wps, idx := a.FAFSegment(nmPerLong, magVar); wps != nil {
		fafLoc = wps[idx].Location
	}

	return ApproachGeometry{
		Threshold:         a.Threshold,
		NmPerLongitude:    nmPerLong,
		MagneticVariation: magVar,
		RunwayHeading:     rwyHdg,
		FAFLocation:       fafLoc,
	}
}

// ThresholdOffset returns a position at distNM along the outbound course
// from the threshold, offset laterally by lateralNM. Positive lateral =
// right of outbound, negative = left of outbound.
func (g ApproachGeometry) ThresholdOffset(distNM, lateralNM float32) math.Point2LL {
	outbound := math.OppositeHeading(g.RunwayHeading)
	onCourse := math.Offset2LL(g.Threshold, outbound, distNM, g.NmPerLongitude)
	if lateralNM == 0 {
		return onCourse
	}
	perpRight := math.OffsetHeading(outbound, 90)
	return math.Offset2LL(onCourse, perpRight, lateralNM, g.NmPerLongitude)
}

// FAFOffset returns a position at distNM along the outbound course from
// the FAF, offset laterally by lateralNM. Positive lateral = right of
// outbound, negative = left of outbound.
func (g ApproachGeometry) FAFOffset(distNM, lateralNM float32) math.Point2LL {
	outbound := math.OppositeHeading(g.RunwayHeading)
	onCourse := math.Offset2LL(g.FAFLocation, outbound, distNM, g.NmPerLongitude)
	if lateralNM == 0 {
		return onCourse
	}
	perpRight := math.OffsetHeading(outbound, 90)
	return math.Offset2LL(onCourse, perpRight, lateralNM, g.NmPerLongitude)
}
