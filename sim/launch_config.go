// sim/launch_config.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"maps"
	gomath "math"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/traffic"
	"github.com/mmp/vice/util"
)

const (
	LaunchAutomatic int32 = iota
	LaunchManual
)

// TrafficSource identifies where automatic IFR traffic comes from: the
// scenario's own traffic definitions, a built-in daily timetable, or the
// flights that really operated at the facility on the selected date.
type TrafficSource int32

const (
	TrafficSourceScenario TrafficSource = iota
	TrafficSourceTimetable
	TrafficSourceHistorical
)

func (ts TrafficSource) String() string {
	switch ts {
	case TrafficSourceScenario:
		return "Scenario"
	case TrafficSourceTimetable:
		return "Timetable"
	case TrafficSourceHistorical:
		return "Historical"
	default:
		return "unknown"
	}
}

// LaunchConfig collects settings related to launching aircraft in the sim; it's
// passed back and forth between client and server: server provides them so client
// can draw the UI for what's available, then client returns one back when launching.
type LaunchConfig struct {
	// LaunchManual or LaunchAutomatic, separate for each aircraft type
	DepartureMode  int32
	ArrivalMode    int32
	OverflightMode int32

	// TrafficSource controls whether automatic IFR aircraft come from the
	// scenario's own rate-based traffic generator, a built-in timetable, or
	// historical flight data.
	TrafficSource TrafficSource
	// TimetableID and TimetableAirport identify the selected built-in timetable
	// when TrafficSource is TrafficSourceTimetable; a scenario may offer
	// timetables for more than one of its airports, so the id alone doesn't
	// name one.
	TimetableID      string
	TimetableAirport av.ICAOAirportCode
	// TimetableStartMinute is the selected local start time, expressed as
	// minutes after midnight at the timetable's airport.
	TimetableStartMinute int
	// PublishedArrivalRateScale is how fast published IFR arrivals are flown as
	// a multiple of the rate the data holds them at: the sim reads through the
	// arrivals at that multiple of real time, anchored at the sim's start time,
	// so two flies the whole day's traffic in half the time rather than half of
	// its flights. It applies to both timetable and historical traffic.
	PublishedArrivalRateScale float32

	// PublishedDepartureRateScale is PublishedArrivalRateScale for published IFR
	// departures.
	PublishedDepartureRateScale float32

	GoAroundRate         float32
	EnableTowerGoArounds bool
	// airport -> runway -> category -> rate
	DepartureRates     map[av.ICAOAirportCode]map[av.RunwayID]map[string]float32
	DepartureRateScale float32
	// airport -> runway -> category -> enabled; which flows timetable and
	// historical traffic launch from. Scenario traffic uses the rates instead.
	DepartureEnabled map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool
	// airport -> runway -> category -> the traffic there is nobody's to work.
	// A scenario flies a neighboring airport's operations for realism, start to
	// finish under virtual controllers; it fills out the scope but it isn't
	// traffic the user signed up for, so it stays out of what we report they
	// will see. Keyed like DepartureEnabled, and only the true entries are
	// present: an absent one is traffic a human works, which is the common case
	// and the safe assumption for a config that was never classified.
	DepartureBackground map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool

	VFRDepartureRateScale   float32
	VFRAirportRates         map[av.ICAOAirportCode]float32 // name -> VFRRateSum()
	VFFRequestRate          int32
	HaveVFRReportingRegions bool

	// inbound flow -> airport / "overflights" -> rate
	InboundFlowRates map[string]map[string]float32
	// inbound flow -> airport -> enabled; which flows timetable and historical
	// traffic land. Overflights aren't included: they are always randomly
	// generated, so their rates apply regardless of the traffic source.
	InboundFlowEnabled map[string]map[string]bool
	// inbound flow -> airport -> the traffic there is nobody's to work; see
	// DepartureBackground. Overflights are included here, under the same
	// "overflights" key the rates use, since a flow may carry those for realism
	// as readily as it carries arrivals.
	InboundFlowBackground       map[string]map[string]bool
	InboundFlowRateScale        float32
	ArrivalPushes               bool
	ArrivalPushFrequencyMinutes int
	ArrivalPushLengthMinutes    int

	EmergencyAircraftRate float32 // Aircraft per hour
}

func MakeLaunchConfig(dep []DepartureRunway, vfrRateScale float32, vffRequestRate int32,
	vfrAirports map[av.ICAOAirportCode]*av.Airport, inbound map[string]map[string]float32, haveVFRReportingRegions bool) LaunchConfig {
	lc := LaunchConfig{
		TrafficSource:               TrafficSourceScenario,
		PublishedArrivalRateScale:   1,
		PublishedDepartureRateScale: 1,
		GoAroundRate:                0.01,
		DepartureRateScale:          1,
		VFRDepartureRateScale:       vfrRateScale,
		VFRAirportRates:             make(map[av.ICAOAirportCode]float32),
		VFFRequestRate:              vffRequestRate,
		HaveVFRReportingRegions:     haveVFRReportingRegions,
		InboundFlowRateScale:        1,
		ArrivalPushFrequencyMinutes: 20,
		ArrivalPushLengthMinutes:    10,
		EmergencyAircraftRate:       0,
	}

	for icao, ap := range vfrAirports {
		lc.VFRAirportRates[icao] = ap.VFRRateSum()
	}

	// Walk the departure runways to create the map for departures.
	lc.DepartureRates = make(map[av.ICAOAirportCode]map[av.RunwayID]map[string]float32)
	lc.DepartureEnabled = make(map[av.ICAOAirportCode]map[av.RunwayID]map[string]bool)
	for _, rwy := range dep {
		if _, ok := lc.DepartureRates[rwy.Airport]; !ok {
			lc.DepartureRates[rwy.Airport] = make(map[av.RunwayID]map[string]float32)
			lc.DepartureEnabled[rwy.Airport] = make(map[av.RunwayID]map[string]bool)
		}
		if _, ok := lc.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
			lc.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]float32)
			lc.DepartureEnabled[rwy.Airport][rwy.Runway] = make(map[string]bool)
		}
		lc.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate
		lc.DepartureEnabled[rwy.Airport][rwy.Runway][rwy.Category] = rwy.DefaultRate > 0
	}

	lc.InboundFlowRates = make(map[string]map[string]float32)
	lc.InboundFlowEnabled = make(map[string]map[string]bool)
	for flow, airportOverflights := range inbound {
		lc.InboundFlowRates[flow] = maps.Clone(airportOverflights)
		for ap := range airportOverflights {
			if ap != "overflights" {
				if lc.InboundFlowEnabled[flow] == nil {
					lc.InboundFlowEnabled[flow] = make(map[string]bool)
				}
				// Every flow the scenario lists for an airport is a way into
				// it. The rate says how much traffic the scenario's own
				// generator should make and nothing more, so it has no bearing
				// here: published traffic is the only thing that consults these
				// and it arrives when its data says, not at some rate. A flow a
				// scenario leaves dialed to zero is still one its controllers
				// work, so start them all on and let the user turn off the ones
				// they don't want.
				lc.InboundFlowEnabled[flow][ap] = true
			}
		}
	}

	return lc
}

// TotalDepartureRate returns the total departure rate (aircraft per hour) for all airports and runways
func (lc *LaunchConfig) TotalDepartureRate() float32 {
	var sum float32
	for _, runwayRates := range lc.DepartureRates {
		sum += sumRateMap2(runwayRates, lc.DepartureRateScale)
	}
	return sum
}

// TotalInboundFlowRate returns the total inbound flow rate (aircraft per hour) for all flows
func (lc *LaunchConfig) TotalInboundFlowRate() float32 {
	var sum float32
	for _, flowRates := range lc.InboundFlowRates {
		for _, rate := range flowRates {
			sum += scaleRate(rate, lc.InboundFlowRateScale)
		}
	}
	return sum
}

// TotalArrivalRate returns the total arrival rate (aircraft per hour) excluding overflights
func (lc *LaunchConfig) TotalArrivalRate() float32 {
	var sum float32
	for _, flowRates := range lc.InboundFlowRates {
		for ap, rate := range flowRates {
			if ap != "overflights" {
				sum += scaleRate(rate, lc.InboundFlowRateScale)
			}
		}
	}
	return sum
}

// TotalOverflightRate returns the total overflight rate (aircraft per hour)
func (lc *LaunchConfig) TotalOverflightRate() float32 {
	var sum float32
	for _, flowRates := range lc.InboundFlowRates {
		if rate, ok := flowRates["overflights"]; ok {
			sum += scaleRate(rate, lc.InboundFlowRateScale)
		}
	}
	return sum
}

// The Worked rates are the Total ones less the traffic no human ever works, and
// are what to report to someone deciding whether to fly a scenario: a departure
// position whose scenario also lands a neighboring airport for realism is not
// signing up for those arrivals. The Total rates remain what the sim will
// generate, which is what the rate limits care about.

func (lc *LaunchConfig) WorkedDepartureRate() float32 {
	var sum float32
	for airport, runwayRates := range lc.DepartureRates {
		for runway, categoryRates := range runwayRates {
			for category, rate := range categoryRates {
				if !lc.DepartureIsBackground(airport, runway, category) {
					sum += scaleRate(rate, lc.DepartureRateScale)
				}
			}
		}
	}
	return sum
}

func (lc *LaunchConfig) WorkedArrivalRate() float32 {
	return lc.workedInboundRate(false)
}

func (lc *LaunchConfig) WorkedOverflightRate() float32 {
	return lc.workedInboundRate(true)
}

func (lc *LaunchConfig) workedInboundRate(overflights bool) float32 {
	var sum float32
	for flow, flowRates := range lc.InboundFlowRates {
		for airport, rate := range flowRates {
			if (airport == "overflights") == overflights &&
				!lc.InboundFlowIsBackground(flow, airport) {
				sum += scaleRate(rate, lc.InboundFlowRateScale)
			}
		}
	}
	return sum
}

// WorkedAirportRates breaks WorkedDepartureRate and WorkedArrivalRate out by
// airport: how much IFR traffic an hour a human controller works at each of
// the scenario's airports. Overflights belong to no airport and aren't
// included.
func (lc *LaunchConfig) WorkedAirportRates() map[av.ICAOAirportCode]float32 {
	rates := make(map[av.ICAOAirportCode]float32)
	for airport, runwayRates := range lc.DepartureRates {
		for runway, categoryRates := range runwayRates {
			for category, rate := range categoryRates {
				if !lc.DepartureIsBackground(airport, runway, category) {
					rates[airport] += scaleRate(rate, lc.DepartureRateScale)
				}
			}
		}
	}
	for flow, flowRates := range lc.InboundFlowRates {
		for airport, rate := range flowRates {
			if airport != "overflights" && !lc.InboundFlowIsBackground(flow, airport) {
				rates[av.ICAOAirportCode(airport)] += scaleRate(rate, lc.InboundFlowRateScale)
			}
		}
	}
	return rates
}

// The Worked counts say how many flows of each kind of traffic a human works,
// which is both whether there is anything to show them and how much room it
// takes: the new sim and launch control rate tables offer the flows they count
// and leave the background traffic to the scenario.

// WorkedDepartureCounts gives the number of runway and category departure flows
// a human works at each airport; an airport with none is absent. DepartureRates
// and DepartureEnabled are keyed alike, so the count holds for either.
func (lc *LaunchConfig) WorkedDepartureCounts() map[av.ICAOAirportCode]int {
	counts := make(map[av.ICAOAirportCode]int)
	for airport, runwayRates := range lc.DepartureRates {
		for runway, categoryRates := range runwayRates {
			for category := range categoryRates {
				if !lc.DepartureIsBackground(airport, runway, category) {
					counts[airport]++
				}
			}
		}
	}
	return counts
}

// WorkedInboundFlowCounts gives the number of inbound flows a human works into
// each airport. Overflights serve no airport and are counted by
// WorkedOverflightGroups instead.
func (lc *LaunchConfig) WorkedInboundFlowCounts() map[av.ICAOAirportCode]int {
	counts := make(map[av.ICAOAirportCode]int)
	for flow, flowRates := range lc.InboundFlowRates {
		for airport := range flowRates {
			if airport != "overflights" && !lc.InboundFlowIsBackground(flow, airport) {
				counts[av.ICAOAirportCode(airport)]++
			}
		}
	}
	return counts
}

// WorkedOverflightGroups returns the inbound flows carrying overflights a human
// works, in sorted order.
func (lc *LaunchConfig) WorkedOverflightGroups() []string {
	var groups []string
	for flow, flowRates := range lc.InboundFlowRates {
		if _, ok := flowRates["overflights"]; ok && !lc.InboundFlowIsBackground(flow, "overflights") {
			groups = append(groups, flow)
		}
	}
	slices.Sort(groups)
	return groups
}

func (lc *LaunchConfig) HaveWorkedDepartures() bool {
	return len(lc.WorkedDepartureCounts()) > 0
}

func (lc *LaunchConfig) HaveWorkedArrivals() bool {
	return len(lc.WorkedInboundFlowCounts()) > 0
}

func (lc *LaunchConfig) HaveWorkedOverflights() bool {
	return len(lc.WorkedOverflightGroups()) > 0
}

// DepartureIsBackground and InboundFlowIsBackground report traffic that no human
// controller works. They read the maps rather than indexing them directly so
// that a launch config nobody classified--one built without a scenario to walk--
// reports everything as the user's traffic, as it was before any of this.
func (lc *LaunchConfig) DepartureIsBackground(airport av.ICAOAirportCode, runway av.RunwayID, category string) bool {
	return lc.DepartureBackground[airport][runway][category]
}

func (lc *LaunchConfig) InboundFlowIsBackground(flow, airport string) bool {
	return lc.InboundFlowBackground[flow][airport]
}

// IFRAirports returns every airport the scenario generates IFR traffic at, departures and
// arrivals separately. It reads the rate maps, not the enable maps: which flows are switched on
// can change while a sim runs, so that is judged flight by flight at spawn.
func (lc *LaunchConfig) IFRAirports() (departures, arrivals map[av.ICAOAirportCode]bool) {
	departures = make(map[av.ICAOAirportCode]bool)
	arrivals = make(map[av.ICAOAirportCode]bool)
	for airport := range lc.DepartureRates {
		departures[airport] = true
	}
	for _, rates := range lc.InboundFlowRates {
		for airport := range rates {
			if airport != "overflights" {
				arrivals[av.ICAOAirportCode(airport)] = true
			}
		}
	}
	return
}

// enabledDepartureCategories returns the categories the scenario is launching
// from a runway, and nothing more: how many aircraft go where comes from the
// published flights themselves.
func (lc *LaunchConfig) enabledDepartureCategories(airport av.ICAOAirportCode, runway av.RunwayID) []string {
	var categories []string
	for category, enabled := range lc.DepartureEnabled[airport][runway] {
		if enabled {
			categories = append(categories, category)
		}
	}
	slices.Sort(categories)
	return categories
}

// departsAirport reports whether any of an airport's departure flows are both
// enabled and a human's to work, and landsAirport whether any inbound flow into
// it is. Published traffic leaves from and lands at the flows the user leaves
// on, so an airport with all of them off flies nothing; and traffic the
// scenario flies purely for realism is nothing the user will see, so a preview
// of what they are in for leaves it out.
func (lc *LaunchConfig) departsAirport(airport av.ICAOAirportCode) bool {
	for runway, categories := range lc.DepartureEnabled[airport] {
		for category, enabled := range categories {
			if enabled && !lc.DepartureIsBackground(airport, runway, category) {
				return true
			}
		}
	}
	return false
}

func (lc *LaunchConfig) landsAirport(airport av.ICAOAirportCode) bool {
	for flow, airports := range lc.InboundFlowEnabled {
		if airports[string(airport)] && !lc.InboundFlowIsBackground(flow, string(airport)) {
			return true
		}
	}
	return false
}

// landsArrivals reports whether the scenario lands an inbound flow's traffic
// at an airport.
func (lc *LaunchConfig) landsArrivals(flow string, airport av.ICAOAirportCode) bool {
	_, ok := lc.InboundFlowRates[flow][string(airport)]
	return ok && lc.InboundFlowEnabled[flow][string(airport)]
}

// MaxPublishedRateScale is how much faster than the data's own pace published
// traffic can be flown.
const MaxPublishedRateScale = 4

// MaxLaunchRate is the most departures an hour a launch config may ask for,
// and separately the most arrivals and overflights.
const MaxLaunchRate = 150

// CheckRateLimits returns true if both total departure rates and total inbound flow rates
// sum to no more than MaxLaunchRate (aircraft per hour)
func (lc *LaunchConfig) CheckRateLimits() bool {
	totalDepartures := lc.TotalDepartureRate()
	totalInbound := lc.TotalInboundFlowRate()
	return totalDepartures <= MaxLaunchRate && totalInbound <= MaxLaunchRate
}

// ClampRates adjusts the rate scale variables to ensure the total launch rate
// does not exceed MaxLaunchRate (aircraft per hour)
func (lc *LaunchConfig) ClampRates() {
	baseDepartureRate := lc.TotalDepartureRate()
	baseInboundRate := lc.TotalInboundFlowRate()

	// If either rate would exceed the limit with current scale, adjust it
	if baseDepartureRate > MaxLaunchRate {
		lc.DepartureRateScale *= MaxLaunchRate / baseDepartureRate * 0.99
	}

	if baseInboundRate > MaxLaunchRate {
		lc.InboundFlowRateScale *= MaxLaunchRate / baseInboundRate * 0.99
	}
}

// Validate returns ErrInvalidLaunchConfig if lc has a rate or rate scale that
// is negative, infinite, or not a number, or if it asks for more traffic than
// CheckRateLimits allows. Clients can send anything, and schedule generation,
// which steps through time at these rates, never finishes with some of them.
func (lc *LaunchConfig) Validate() error {
	valid := func(r float32) bool { return r >= 0 && !gomath.IsInf(float64(r), 1) }

	ok := valid(lc.DepartureRateScale) && valid(lc.InboundFlowRateScale) && valid(lc.VFRDepartureRateScale) &&
		valid(lc.PublishedArrivalRateScale) && valid(lc.PublishedDepartureRateScale) &&
		valid(lc.EmergencyAircraftRate) && lc.VFFRequestRate >= 0 &&
		util.SeqContainsAllFunc(maps.Values(lc.VFRAirportRates), valid)
	for _, runwayRates := range lc.DepartureRates {
		for _, categoryRates := range runwayRates {
			ok = ok && util.SeqContainsAllFunc(maps.Values(categoryRates), valid)
		}
	}
	for _, flowRates := range lc.InboundFlowRates {
		ok = ok && util.SeqContainsAllFunc(maps.Values(flowRates), valid)
	}

	if !ok || !lc.CheckRateLimits() {
		return ErrInvalidLaunchConfig
	}
	return nil
}

func scaleRate(rate, scale float32) float32 {
	return rate * scale
}

// sumRateMap totals the scaled rates. The keys are taken in order because
// float addition is not associative: summing them as the map hands them out
// gives a total whose last bits vary from run to run, and the spawn times
// drawn from it vary with them.
func sumRateMap(rates map[string]float32, scale float32) float32 {
	var sum float32
	for _, key := range util.SortedMapKeys(rates) {
		sum += scaleRate(rates[key], scale)
	}
	return sum
}

// sumRateMap2 computes the total rate from a nested map structure
func sumRateMap2(rates map[av.RunwayID]map[string]float32, scale float32) float32 {
	var sum float32
	for _, categoryRates := range rates {
		for _, rate := range categoryRates {
			sum += scaleRate(rate, scale)
		}
	}
	return sum
}

// SetLaunchConfig changes the sim's launch config. published is what
// PublishedFlightsFor read for it.
func (s *Sim) SetLaunchConfig(tcw TCW, lc LaunchConfig, published []traffic.Flight) error {
	if err := s.validateLaunchConfig(lc); err != nil {
		s.lg.Warn("rejected launch config", slog.Any("launch_config", lc))
		return err
	}

	old := s.State.LaunchConfig

	// Update the runway launch state for any rates that changed. All of
	// these are taken in order since changing a rate can draw random numbers.
	for ap, rwyRates := range util.SortedMap(lc.DepartureRates) {
		for rwy, categoryRates := range util.SortedMap(rwyRates) {
			r := sumRateMap(categoryRates, lc.DepartureRateScale)
			s.DepartureState[ap][rwy].setIFRRate(s, r)
		}
	}

	for name, rate := range util.SortedMap(lc.VFRAirportRates) {
		r := scaleRate(rate, lc.VFRDepartureRateScale)
		rwy := s.State.VFRRunways[name]
		s.DepartureState[name][av.RunwayID(rwy.Id)].setVFRRate(s, r)
	}

	if lc.VFRDepartureRateScale != old.VFRDepartureRateScale {
		r := lc.patternSpawnRate()
		for _, ps := range util.SortedMap(s.PatternState) {
			ps.NextSpawn = s.State.SimTime.Add(randomInitialWait(r, s.Rand))
		}
	}

	if lc.VFFRequestRate != old.VFFRequestRate {
		s.NextVFFRequest = s.State.SimTime.Add(randomInitialWait(float32(lc.VFFRequestRate), s.Rand))
	}

	if lc.EmergencyAircraftRate != old.EmergencyAircraftRate {
		if lc.EmergencyAircraftRate > 0 {
			delay := max(5*time.Minute, randomInitialWait(lc.EmergencyAircraftRate, s.Rand))
			s.NextEmergencyTime = s.State.SimTime.Add(delay)
		} else {
			s.NextEmergencyTime = Time{} // zero time = disabled
		}
	}

	s.lg.Info("Set launch config", slog.Any("launch_config", lc))

	s.State.LaunchConfig = lc
	s.applyScheduleConfigChanges(&old, published)

	s.publish()
	return nil
}

// validateLaunchConfig adds to LaunchConfig.Validate a check that every
// departure runway and VFR airport lc gives a rate for has launch state here
// for SetLaunchConfig to update. A config the server built always does; one
// from a buggy or hostile client may not.
func (s *Sim) validateLaunchConfig(lc LaunchConfig) error {
	if err := lc.Validate(); err != nil {
		return err
	}
	for ap, rwyRates := range lc.DepartureRates {
		for rwy := range rwyRates {
			if s.DepartureState[ap][rwy] == nil {
				return ErrInvalidLaunchConfig
			}
		}
	}
	for ap := range lc.VFRAirportRates {
		if s.DepartureState[ap][av.RunwayID(s.State.VFRRunways[ap].Id)] == nil {
			return ErrInvalidLaunchConfig
		}
	}
	return nil
}
