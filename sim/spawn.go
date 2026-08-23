// sim/spawn.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"

	"github.com/goforj/godump"
)

const initialSimSeconds = 30 * 60
const initialSimControlledSeconds = 60

// PrespawnDuration is how far the clock rewinds before the selected start time
// to warm up the sim; historical flight data must cover it so that prespawn
// has traffic to fly.
const PrespawnDuration = initialSimSeconds * time.Second

type RunwayLaunchState struct {
	IFRSpawnRate float32
	VFRSpawnRate float32

	// NextVFRSpawn is when to create the runway's next VFR departure, based
	// on its VFR rate; IFR departures come from the schedule. The actual
	// time an aircraft is launched may be later, e.g. if we need longer for
	// wake turbulence separation, etc.
	NextVFRSpawn Time

	// Aircraft follow the following flows:
	// VFR: ReleasedVFR -> Sequenced
	// IFR no release: Gate -> ReleasedIFR -> Sequenced
	// IFR release required: Gate -> Held -> ReleasedIFR -> Sequenced

	// At the gate, flight plan filed (if IFR), not yet ready to go
	Gate []DepartureAircraft
	// Ready to go, in hold for release purgatory.
	Held []DepartureAircraft
	// Ready to go.
	ReleasedIFR []DepartureAircraft
	ReleasedVFR []DepartureAircraft
	// Sequenced departures, pulled from Released. These are launched in-order.
	Sequenced []DepartureAircraft

	LastDeparture          *DepartureAircraft
	LastArrivalLandingTime Time           // when the last arrival landed on this runway
	LastArrivalFlightRules av.FlightRules // flight rules of the last arrival that landed

	// GoAroundHoldUntil is the time until which departures should be held
	// after a go-around. Departures auto-resume after this time.
	GoAroundHoldUntil Time

	VFRAttempts  int
	VFRSuccesses int
}

// DepartureAircraft represents a departing aircraft, either still on the
// ground or recently-launched.
type DepartureAircraft struct {
	ADSBCallsign  av.ADSBCallsign
	MinSeparation time.Duration // How long after takeoff it will be at ~6000' and airborne
	// AirborneDistance is the estimated distance in nm from the departure
	// point at which the aircraft lifts off; negative if it wasn't airborne
	// within the horizon of the takeoff-roll simulation.
	AirborneDistance float32
	SpawnTime        Time // when it was first spawned
	LaunchTime       Time // when it was actually launched; used for wake turbulence separation, etc.

	// When they're ready to leave the gate
	ReadyDepartGateTime Time

	// HFR-only.
	ReleaseRequested   bool
	ReleaseDelay       time.Duration // minimum wait after release before the takeoff roll
	RequestReleaseTime Time
}

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

// MaxPublishedRateScale is how much faster than the data's own pace published
// traffic can be flown.
const MaxPublishedRateScale = 4

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
	TimetableAirport string
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
	DepartureRates     map[string]map[av.RunwayID]map[string]float32
	DepartureRateScale float32
	// airport -> runway -> category -> enabled; which flows timetable and
	// historical traffic launch from. Scenario traffic uses the rates instead.
	DepartureEnabled map[string]map[av.RunwayID]map[string]bool
	// airport -> runway -> category -> the traffic there is nobody's to work.
	// A scenario flies a neighboring airport's operations for realism, start to
	// finish under virtual controllers; it fills out the scope but it isn't
	// traffic the user signed up for, so it stays out of what we report they
	// will see. Keyed like DepartureEnabled, and only the true entries are
	// present: an absent one is traffic a human works, which is the common case
	// and the safe assumption for a config that was never classified.
	DepartureBackground map[string]map[av.RunwayID]map[string]bool

	VFRDepartureRateScale   float32
	VFRAirportRates         map[string]float32 // name -> VFRRateSum()
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
	vfrAirports map[string]*av.Airport, inbound map[string]map[string]float32, haveVFRReportingRegions bool) LaunchConfig {
	lc := LaunchConfig{
		TrafficSource:               TrafficSourceScenario,
		PublishedArrivalRateScale:   1,
		PublishedDepartureRateScale: 1,
		GoAroundRate:                0.01,
		DepartureRateScale:          1,
		VFRDepartureRateScale:       vfrRateScale,
		VFRAirportRates:             make(map[string]float32),
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
	lc.DepartureRates = make(map[string]map[av.RunwayID]map[string]float32)
	lc.DepartureEnabled = make(map[string]map[av.RunwayID]map[string]bool)
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

func (lc *LaunchConfig) HaveDepartures() bool {
	return len(lc.DepartureRates) > 0
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

func (lc *LaunchConfig) HaveArrivals() bool {
	for _, flowRates := range lc.InboundFlowRates {
		for rate := range flowRates {
			if rate != "overflights" {
				return true
			}
		}
	}
	return false
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

func (lc *LaunchConfig) HaveOverflights() bool {
	for _, flowRates := range lc.InboundFlowRates {
		for rate := range flowRates {
			if rate == "overflights" {
				return true
			}
		}
	}
	return false
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
func (lc *LaunchConfig) WorkedAirportRates() map[string]float32 {
	rates := make(map[string]float32)
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
				rates[airport] += scaleRate(rate, lc.InboundFlowRateScale)
			}
		}
	}
	return rates
}

// DepartureIsBackground and InboundFlowIsBackground report traffic that no human
// controller works. They read the maps rather than indexing them directly so
// that a launch config nobody classified--one built without a scenario to walk--
// reports everything as the user's traffic, as it was before any of this.
func (lc *LaunchConfig) DepartureIsBackground(airport string, runway av.RunwayID, category string) bool {
	return lc.DepartureBackground[airport][runway][category]
}

func (lc *LaunchConfig) InboundFlowIsBackground(flow, airport string) bool {
	return lc.InboundFlowBackground[flow][airport]
}

// CheckRateLimits returns true if both total departure rates and total inbound flow rates
// sum to less than the provided limit (aircraft per hour)
func (lc *LaunchConfig) CheckRateLimits(limit float32) bool {
	totalDepartures := lc.TotalDepartureRate()
	totalInbound := lc.TotalInboundFlowRate()
	return totalDepartures < limit && totalInbound < limit
}

// ClampRates adjusts the rate scale variables to ensure the total launch rate
// does not exceed the given limit (aircraft per hour)
func (lc *LaunchConfig) ClampRates(limit float32) {
	baseDepartureRate := lc.TotalDepartureRate()
	baseInboundRate := lc.TotalInboundFlowRate()

	// If either rate would exceed the limit with current scale, adjust it
	if baseDepartureRate > limit {
		lc.DepartureRateScale *= limit / baseDepartureRate * 0.99
	}

	if baseInboundRate > limit {
		fmt.Printf("%f > %f -> scale %f\n", baseInboundRate, limit, limit/baseInboundRate)
		lc.InboundFlowRateScale *= limit / baseInboundRate * 0.99
	}
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

func (s *Sim) SetLaunchConfig(tcw TCW, lc LaunchConfig) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	old := s.State.LaunchConfig

	// Update the runway launch state for any rates that changed.
	for ap, rwyRates := range lc.DepartureRates {
		for rwy, categoryRates := range rwyRates {
			r := sumRateMap(categoryRates, lc.DepartureRateScale)
			s.DepartureState[ap][rwy].setIFRRate(s, r)
		}
	}

	for name, rate := range lc.VFRAirportRates {
		r := scaleRate(rate, lc.VFRDepartureRateScale)
		rwy := s.State.VFRRunways[name]
		s.DepartureState[name][av.RunwayID(rwy.Id)].setVFRRate(s, r)
	}

	if lc.VFRDepartureRateScale != old.VFRDepartureRateScale {
		r := scaleRate(patternSpawnRate, lc.VFRDepartureRateScale)
		for _, ps := range s.PatternState {
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
	s.applyScheduleConfigChanges(&old)

	s.publish()
	return nil
}

func (s *Sim) addDepartureToPool(ac *Aircraft, runway av.RunwayID, manualLaunch bool, source TrafficSource) {
	depac := makeDepartureAircraft(ac, s.State.SimTime, s.wxModel, source, s.Rand)

	ac.WaitingForLaunch = true
	s.addAircraftNoLock(*ac)

	// The journey begins...
	depState := s.DepartureState[ac.FlightPlan.DepartureAirport][runway]
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		if manualLaunch {
			depac.ReadyDepartGateTime = depac.SpawnTime
		}
		// IFRs spend some time at the gate to give them a chance to appear
		// in the FLIGHT PLAN list.
		depState.Gate = append(depState.Gate, depac)
	} else {
		// VFRs can go straight to the queue.
		depState.ReleasedVFR = append(depState.ReleasedVFR, depac)
	}
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) addAircraftNoLock(ac Aircraft) {
	if _, ok := s.Aircraft[ac.ADSBCallsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!",
			slog.String("adsb_callsign", string(ac.ADSBCallsign)))
		return
	}

	if s.CIDAllocator != nil {
		fp := ac.NASFlightPlan
		if fp == nil {
			fp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
		}
		if fp != nil && fp.CID == "" {
			if cid, err := s.CIDAllocator.Allocate(); err == nil {
				fp.CID = cid
			} else {
				s.lg.Warn("no CID available", slog.String("callsign", string(ac.ADSBCallsign)))
			}
		}
	}

	s.Aircraft[ac.ADSBCallsign] = &ac

	ac.Nav.Prespawn = s.prespawn && ac.FlightPlan.Rules == av.FlightRulesVFR

	ac.Nav.Check(s.lg)

	// Log initial route for navigation debugging
	nav.LogRoute(string(ac.ADSBCallsign), s.State.SimTime.NavTime(), ac.Nav.Waypoints)

	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		s.TotalIFR++
	} else {
		s.TotalVFR++
	}

	if ac.IsDeparture() {
		s.lg.Debug("launched departure", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else if ac.IsArrival() {
		s.lg.Debug("launched arrival", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else if ac.IsOverflight() {
		s.lg.Debug("launched overflight", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else {
		s.lg.Errorf("%s: launched unknown type?\n", ac.ADSBCallsign)
	}
}

func (s *Sim) Prespawn() {
	start := time.Now()
	s.lg.Info("starting aircraft prespawn")

	s.initDepartureState(s.State.SimTime)
	s.generateSchedule()

	s.mu.Lock(s.lg)

	// Prime the pump before the user gets involved
	s.prespawn = true
	for i := range initialSimSeconds {
		// Controlled only at the tail end.
		s.prespawnUncontrolledOnly = i < initialSimSeconds-initialSimControlledSeconds
		// Pattern aircraft only need a few minutes to get established.
		s.prespawnPatternEligible = i >= initialSimSeconds-180

		s.State.SimTime = s.State.SimTime.Add(time.Second)

		s.updateState()
	}
	// Clear Prespawn for all remaining aircraft at the end of prespawn.
	for _, ac := range s.Aircraft {
		ac.Nav.Prespawn = false
	}
	s.prespawnUncontrolledOnly, s.prespawn, s.prespawnPatternEligible = false, false, false

	s.lastSimUpdateTime = time.Now()

	s.NextVFFRequest = s.State.SimTime.Add(randomInitialWait(float32(s.State.LaunchConfig.VFFRequestRate), s.Rand))

	if s.State.LaunchConfig.EmergencyAircraftRate > 0 {
		delay := max(5*time.Minute, randomInitialWait(s.State.LaunchConfig.EmergencyAircraftRate, s.Rand))
		s.NextEmergencyTime = s.State.SimTime.Add(delay)
	}

	s.mu.Unlock(s.lg)

	s.lg.Info("finished aircraft prespawn")
	fmt.Printf("Prespawn in %s, rates: dep %f arrival %f overflight %f\n", time.Since(start),
		s.State.LaunchConfig.TotalDepartureRate(), s.State.LaunchConfig.TotalArrivalRate(),
		s.State.LaunchConfig.TotalOverflightRate())
	fmt.Println("LaunchConfig:")
	godump.Dump(s.State.LaunchConfig)
}

// initDepartureState builds the per-runway departure state and the VFR and
// pattern spawn timers. IFR departures, arrivals, and overflights need no
// timers: they come from the schedule.
func (s *Sim) initDepartureState(now Time) {
	// Randomize the next VFR spawn time; may be before or after the current
	// time.
	randomDelay := func(rate float32) Time {
		if rate == 0 {
			return now.Add(365 * 24 * time.Hour)
		}
		avgWait := 3600 / rate
		delta := s.Rand.Float32Range(-avgWait/2, avgWait/2)
		return now.Add(time.Duration(delta * float32(time.Second)))
	}

	for name := range s.State.DepartureAirports {
		s.DepartureState[name] = make(map[av.RunwayID]*RunwayLaunchState)

		if runwayRates, ok := s.State.LaunchConfig.DepartureRates[name]; ok {
			for rwy, rate := range runwayRates {
				s.DepartureState[name][rwy] = &RunwayLaunchState{
					IFRSpawnRate: sumRateMap(rate, s.State.LaunchConfig.DepartureRateScale),
				}
			}
		}

		ap := s.State.Airports[name]
		if vfrRate := ap.VFRRateSum(); vfrRate > 0 {
			rwy := s.State.VFRRunways[name]
			state, ok := s.DepartureState[name][av.RunwayID(rwy.Id)]
			if !ok {
				state = &RunwayLaunchState{}
				s.DepartureState[name][av.RunwayID(rwy.Id)] = state
			}
			state.VFRSpawnRate = scaleRate(vfrRate, s.State.LaunchConfig.VFRDepartureRateScale)
			state.NextVFRSpawn = randomDelay(state.VFRSpawnRate)

			// Initialize pattern state for airports with VFR activity,
			// but not at airports that also have IFR departures or arrivals.
			_, hasIFRDepartures := s.State.LaunchConfig.DepartureRates[name]
			_, hasIFRArrivals := s.State.ArrivalAirports[name]
			if !hasIFRDepartures && !hasIFRArrivals {
				s.PatternState[name] = &PatternState{
					NextSpawn: now.Add(randomWait(s.effectivePatternSpawnRate(), false, s.Rand)),
				}
			}
		}
	}
}

func scaleRate(rate, scale float32) float32 {
	return rate * scale
}

func sumRateMap(rates map[string]float32, scale float32) float32 {
	var sum float32
	for _, rate := range rates {
		sum += scaleRate(rate, scale)
	}
	return sum
}

func randomWait(rate float32, pushActive bool, r *rand.Rand) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}
	if pushActive {
		rate = rate * 3 / 2
	}

	avgSeconds := 3600 / rate
	seconds := r.Float32Range(.85*avgSeconds, 1.15*avgSeconds)
	return time.Duration(seconds * float32(time.Second))
}

// Wait from 0 up to the rate.
func randomInitialWait(rate float32, r *rand.Rand) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}

	seconds := r.Float32Range(0, 3600/rate)
	return time.Duration(seconds * float32(time.Second))
}

func (s *Sim) spawnAircraft() {
	s.extendSchedule()
	s.spawnScheduledFlights()
	s.spawnVFRDepartures()
	s.refillPendingLaunches()
	// Pattern aircraft complete a lap in well under a minute, so only
	// spawn them during the last 3 minutes of prespawn (and always after).
	if !s.prespawn || s.prespawnPatternEligible {
		s.spawnPatternAircraft()
	}
	s.updateDepartureSequence()
}

func getAircraftTime(now Time, r *rand.Rand) Time {
	// Hallucinate a random time around the present for the aircraft.
	delta := time.Duration(-20 + r.Intn(40))
	t := now.Add(delta * time.Minute)

	// 9 times out of 10, make it a multiple of 5 minutes
	if r.Intn(10) != 9 {
		dm := t.Minute() % 5
		t = t.Add(time.Duration(5-dm) * time.Minute)
	}

	return t
}

type DepartureRunway struct {
	Airport     string      `json:"airport"`
	Runway      av.RunwayID `json:"runway"`
	Category    string      `json:"category,omitempty"`
	DefaultRate float32     `json:"rate"`
}

type ArrivalRunway struct {
	Airport  string             `json:"airport"`
	Runway   av.RunwayID        `json:"runway"`
	GoAround *GoAroundProcedure `json:"go_around,omitempty"`
}
