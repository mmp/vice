// pkg/sim/spawn.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
)

const initialSimSeconds = 20 * 60
const initialSimControlledSeconds = 45

type DepartureLaunchState struct {
	// For each airport, at what time we would like to launch a departure,
	// based on the airport's departure rate. The actual time an aircraft
	// is launched may be later, e.g. if we need longer for wake turbulence
	// separation, etc.
	NextLaunch time.Time

	// Aircraft that are ready to go. The slice is ordered according to the
	// departure sequence.
	Pool []DepartureAircraft

	// Index to track departing aircraft; we use this to make sure we don't
	// keep pushing an aircraft to the end of the queue.
	Index int

	// Runway -> *DepartureAircraft (nil if none launched yet)
	LastRunwayDeparture map[string]*DepartureAircraft

	VFRAttempts  int
	VFRSuccesses int
}

const (
	LaunchAutomatic = iota
	LaunchManual
)

// LaunchConfig collects settings related to launching aircraft in the sim; it's
// passed back and forth between client and server: server provides them so client
// can draw the UI for what's available, then client returns one back when launching.
type LaunchConfig struct {
	// Controller is the controller in charge of the launch settings; if empty then
	// launch control may be taken by any signed in controller.
	Controller string
	// LaunchManual or LaunchAutomatic
	Mode int

	GoAroundRate float32
	// airport -> runway -> category -> rate
	DepartureRates     map[string]map[string]map[string]float32
	DepartureRateScale float32

	VFRDepartureRateScale float32
	VFRAirports           map[string]*av.Airport

	// inbound flow -> airport / "overflights" -> rate
	InboundFlowRates            map[string]map[string]float32
	InboundFlowRateScale        float32
	ArrivalPushes               bool
	ArrivalPushFrequencyMinutes int
	ArrivalPushLengthMinutes    int
}

func MakeLaunchConfig(dep []DepartureRunway, vfrRateScale float32, vfrAirports map[string]*av.Airport,
	inbound map[string]map[string]int) LaunchConfig {
	lc := LaunchConfig{
		GoAroundRate:                0.05,
		DepartureRateScale:          1,
		VFRDepartureRateScale:       vfrRateScale,
		VFRAirports:                 vfrAirports,
		InboundFlowRateScale:        1,
		ArrivalPushFrequencyMinutes: 20,
		ArrivalPushLengthMinutes:    10,
	}

	// Walk the departure runways to create the map for departures.
	lc.DepartureRates = make(map[string]map[string]map[string]float32)
	for _, rwy := range dep {
		if _, ok := lc.DepartureRates[rwy.Airport]; !ok {
			lc.DepartureRates[rwy.Airport] = make(map[string]map[string]float32)
		}
		if _, ok := lc.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
			lc.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]float32)
		}
		lc.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = float32(rwy.DefaultRate)
	}

	// Convert the inbound map from int to float32 rates
	lc.InboundFlowRates = make(map[string]map[string]float32)
	for flow, airportOverflights := range inbound {
		lc.InboundFlowRates[flow] = make(map[string]float32)
		for name, rate := range airportOverflights {
			lc.InboundFlowRates[flow][name] = float32(rate)
		}
	}

	return lc
}

func (s *Sim) SetLaunchConfig(tcp string, lc LaunchConfig) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Update the next spawn time for any rates that changed.
	for ap, rwyRates := range lc.DepartureRates {
		var newSum, oldSum float32
		for rwy, categoryRates := range rwyRates {
			for category, rate := range categoryRates {
				newSum += rate
				oldSum += s.State.LaunchConfig.DepartureRates[ap][rwy][category]
			}
		}
		newSum = scaleRate(newSum, lc.DepartureRateScale) +
			scaleRate(float32(s.State.Airports[ap].VFRRateSum()), lc.VFRDepartureRateScale)
		oldSum = scaleRate(oldSum, s.State.LaunchConfig.DepartureRateScale) +
			scaleRate(float32(s.State.Airports[ap].VFRRateSum()), s.State.LaunchConfig.VFRDepartureRateScale)

		if newSum != oldSum {
			s.lg.Infof("%s: departure rate changed %f -> %f", ap, oldSum, newSum)
			s.DepartureState[ap].NextLaunch = s.State.SimTime.Add(randomWait(newSum, false))
		}
	}
	if lc.VFRDepartureRateScale != s.State.LaunchConfig.VFRDepartureRateScale {
		for name, ap := range lc.VFRAirports {
			r := scaleRate(float32(ap.VFRRateSum()), lc.VFRDepartureRateScale)
			s.DepartureState[name].NextLaunch = s.State.SimTime.Add(randomWait(r, false))
		}
	}
	for group, groupRates := range lc.InboundFlowRates {
		var newSum, oldSum float32
		for ap, rate := range groupRates {
			newSum += rate
			oldSum += s.State.LaunchConfig.InboundFlowRates[group][ap]
		}
		newSum *= lc.InboundFlowRateScale
		oldSum *= s.State.LaunchConfig.InboundFlowRateScale

		if newSum != oldSum {
			pushActive := s.State.SimTime.Before(s.PushEnd)
			s.lg.Infof("%s: inbound flow rate changed %f -> %f", group, oldSum, newSum)
			s.NextInboundSpawn[group] = s.State.SimTime.Add(randomWait(newSum, pushActive))
		}
	}

	s.State.LaunchConfig = lc
	return nil
}

func (s *Sim) TakeOrReturnLaunchControl(tcp string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if lctrl := s.State.LaunchConfig.Controller; lctrl != "" && lctrl != tcp {
		return ErrNotLaunchController
	} else if lctrl == "" {
		s.State.LaunchConfig.Controller = tcp
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: tcp + " is now controlling aircraft launches.",
		})
		s.lg.Infof("%s: now controlling launches", tcp)
		return nil
	} else {
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: s.State.LaunchConfig.Controller + " is no longer controlling aircraft launches.",
		})
		s.lg.Infof("%s: no longer controlling launches", tcp)
		s.State.LaunchConfig.Controller = ""
		return nil
	}
}

func (s *Sim) LaunchAircraft(ac av.Aircraft) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Ignore hold for release; this should only be called for manual launches.
	ac.HoldForRelease = false
	s.addAircraftNoLock(ac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) addAircraftNoLock(ac av.Aircraft) {
	if _, ok := s.State.Aircraft[ac.Callsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!", slog.String("callsign", ac.Callsign))
		return
	}

	s.State.Aircraft[ac.Callsign] = &ac

	ac.Nav.Check(s.lg)

	if s.State.IsIntraFacility(&ac) {
		s.TotalDepartures++
		s.TotalArrivals++
		s.lg.Info("launched intrafacility", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else if s.State.IsDeparture(&ac) {
		s.TotalDepartures++
		s.lg.Info("launched departure", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else if s.State.IsArrival(&ac) {
		s.TotalArrivals++
		s.lg.Info("launched arrival", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else {
		s.TotalOverflights++
		s.lg.Info("launched overflight", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	}
}

func (s *Sim) Prespawn() {
	s.lg.Info("starting aircraft prespawn")

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	s.prespawnUncontrolled = true
	for i := 0; i < initialSimSeconds; i++ {
		// Controlled only at the tail end.
		s.prespawnControlled = i+initialSimControlledSeconds > initialSimSeconds

		s.State.SimTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.prespawnUncontrolled, s.prespawnControlled = false, false

	s.State.SimTime = time.Now()
	s.State.SimTime = s.State.SimTime
	s.lastUpdateTime = time.Now()

	s.lg.Info("finished aircraft prespawn")
}

func (s *Sim) setInitialSpawnTimes() {
	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	randomDelay := func(rate float32) time.Time {
		if rate == 0 {
			return time.Now().Add(365 * 24 * time.Hour)
		}
		avgWait := int(3600 / rate)
		delta := rand.Intn(avgWait) - avgWait/2 - initialSimSeconds
		return time.Now().Add(time.Duration(delta) * time.Second)
	}

	if s.State.LaunchConfig.ArrivalPushes {
		// Figure out when the next arrival push will start
		m := 1 + rand.Intn(s.State.LaunchConfig.ArrivalPushFrequencyMinutes)
		s.NextPushStart = time.Now().Add(time.Duration(m) * time.Minute)
	}

	for group, rates := range s.State.LaunchConfig.InboundFlowRates {
		var rateSum float32
		for _, rate := range rates {
			rate = scaleRate(rate, s.State.LaunchConfig.InboundFlowRateScale)
			rateSum += rate
		}
		s.NextInboundSpawn[group] = randomDelay(rateSum)
	}

	for name, ap := range s.State.DepartureAirports {
		s.DepartureState[name] = &DepartureLaunchState{
			LastRunwayDeparture: make(map[string]*DepartureAircraft),
		}

		r := scaleRate(float32(ap.VFRRateSum()), s.State.LaunchConfig.VFRDepartureRateScale)
		if runwayRates, ok := s.State.LaunchConfig.DepartureRates[name]; ok {
			r += sumRateMap2(runwayRates, s.State.LaunchConfig.DepartureRateScale)
		}
		s.DepartureState[name].NextLaunch = randomDelay(r)
	}
}

func scaleRate(rate, scale float32) float32 {
	rate *= scale
	if rate <= 0.5 {
		// Since we round to the nearest int when displaying rates in the UI,
		// we don't want to ever launch for ones that have rate 0.
		return 0
	}
	return rate
}

func sumRateMap2(rates map[string]map[string]float32, scale float32) float32 {
	var sum float32
	for _, categoryRates := range rates {
		for _, rate := range categoryRates {
			sum += scaleRate(rate, scale)
		}
	}
	return sum
}

// sampleRateMap randomly samples elements from a map of some type T to a
// rate with probability proportional to the element's rate.
func sampleRateMap[T comparable](rates map[T]float32, scale float32) (T, float32) {
	var rateSum float32
	var result T
	for item, rate := range rates {
		rate = scaleRate(rate, scale)
		rateSum += rate
		// Weighted reservoir sampling...
		if rateSum == 0 || rand.Float32() < rate/rateSum {
			result = item
		}
	}
	return result, rateSum
}

func sampleRateMap2(rates map[string]map[string]float32, scale float32) (string, string, float32) {
	// Choose randomly in proportion to the rates in the map
	var rateSum float32
	var result0, result1 string
	for item0, rateMap := range rates {
		for item1, rate := range rateMap {
			rate = scaleRate(rate, scale)
			if rate == 0 {
				continue
			}
			rateSum += rate
			// Weighted reservoir sampling...
			if rand.Float32() < rate/rateSum {
				result0 = item0
				result1 = item1
			}
		}
	}
	return result0, result1, rateSum
}

func randomWait(rate float32, pushActive bool) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}
	if pushActive {
		rate = rate * 3 / 2
	}

	avgSeconds := 3600 / rate
	seconds := math.Lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
	return time.Duration(seconds * float32(time.Second))
}

func (s *Sim) spawnAircraft() {
	s.spawnArrivalsAndOverflights()
	s.spawnDepartures()
}

func (s *Sim) isControlled(ac *av.Aircraft, departure bool) bool {
	if ac.FlightPlan.Rules == av.VFR {
		// No VFR flights are controlled, so it's easy for them.
		return false
	} else {
		// Otherwise we have to dig around a bit and see if a human is initially or will be involved.
		if departure && ac.DepartureContactController != "" {
			return true
		}
		return slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff })
	}
}

func (s *Sim) spawnArrivalsAndOverflights() {
	now := s.State.SimTime

	if !s.NextPushStart.IsZero() && now.After(s.NextPushStart) {
		// party time
		s.PushEnd = now.Add(time.Duration(s.State.LaunchConfig.ArrivalPushLengthMinutes) * time.Minute)
		s.lg.Info("arrival push starting", slog.Time("end_time", s.PushEnd))
		s.NextPushStart = time.Time{}
	}
	if !s.PushEnd.IsZero() && now.After(s.PushEnd) {
		// end push
		m := -2 + rand.Intn(4) + s.State.LaunchConfig.ArrivalPushFrequencyMinutes
		s.NextPushStart = now.Add(time.Duration(m) * time.Minute)
		s.lg.Info("arrival push ending", slog.Time("next_start", s.NextPushStart))
		s.PushEnd = time.Time{}
	}

	pushActive := now.Before(s.PushEnd)

	for group, rates := range s.State.LaunchConfig.InboundFlowRates {
		if now.After(s.NextInboundSpawn[group]) {
			flow, rateSum := sampleRateMap(rates, s.State.LaunchConfig.InboundFlowRateScale)

			var ac *av.Aircraft
			var err error
			if flow == "overflights" {
				ac, err = s.createOverflightNoLock(group)
			} else {
				ac, err = s.createArrivalNoLock(group, flow)
			}

			if err != nil {
				s.lg.Errorf("create inbound error: %v", err)
			} else if ac != nil {
				if s.prespawnUncontrolled && !s.prespawnControlled && s.isControlled(ac, false) {
					s.lg.Infof("%s: discarding arrival/overflight\n", ac.Callsign)
					s.State.DeleteAircraft(ac)
				} else {
					s.addAircraftNoLock(*ac)
				}
				s.NextInboundSpawn[group] = now.Add(randomWait(rateSum, pushActive))
			}
		}
	}
}

func (s *Sim) spawnDepartures() {
	now := s.State.SimTime

	for airport, depState := range s.DepartureState {
		// Make sure we have a few departing aircraft to work with.
		s.refreshDeparturePool(airport)

		// Add hold for release to the list a bit before departure time and
		// request release a few minutes early as well.
		pool := depState.Pool
		nlist, nrel := 0, 0
		for i, dep := range rand.PermuteSlice(pool, rand.Uint32()) {
			ac := s.State.Aircraft[dep.Callsign]
			if !ac.HoldForRelease {
				continue
			}

			if !dep.AddedToList && nlist < 5 {
				pool[i].AddedToList = true
				s.State.STARSComputer().AddHeldDeparture(ac)
			}
			nlist++

			if !dep.ReleaseRequested && nrel < 3 {
				pool[i].ReleaseRequested = true
				pool[i].ReleaseDelay = time.Duration(20+rand.Intn(100)) * time.Second
			}
			nrel++
		}

		// See if we have anything to launch
		if !now.After(depState.NextLaunch) || len(pool) == 0 {
			// Don't bother going any further: wait to match the desired
			// overall launch rate.
			continue
		}

		for i, dep := range pool {
			if !s.canLaunch(airport, dep) {
				continue
			}

			ac := s.State.Aircraft[dep.Callsign]
			if i > 0 && ac.FlightPlan.Rules == av.IFR {
				// We can still launch VFRs if we have IFRs waiting for
				// release but don't want to launch a released IFR if an
				// earlier IFR in the sequence hasn't been released yet.
				continue
			}

			if s.prespawnUncontrolled && !s.prespawnControlled && s.isControlled(ac, true) {
				s.lg.Infof("%s: discarding departure\n", ac.Callsign)
				s.State.DeleteAircraft(ac)
			} else {
				// Launch!
				ac.WaitingForLaunch = false

				// Record the launch so we have it when we consider launching the
				// next one.
				dep.LaunchTime = now
				depState.LastRunwayDeparture[dep.Runway] = &dep
			}

			// Remove it from the pool of waiting departures.
			depState.Pool = append(depState.Pool[:i], depState.Pool[i+1:]...)

			// And figure out when we want to ask for the next departure.
			ap := s.State.DepartureAirports[airport]
			r := scaleRate(float32(ap.VFRRateSum()), s.State.LaunchConfig.VFRDepartureRateScale)
			if rates, ok := s.State.LaunchConfig.DepartureRates[airport]; ok {
				r += sumRateMap2(rates, s.State.LaunchConfig.DepartureRateScale)
			}
			depState.NextLaunch = now.Add(randomWait(r, false))

			break
		}
	}
}

// canLaunch checks whether we can go ahead and launch dep.
func (s *Sim) canLaunch(airport string, dep DepartureAircraft) bool {
	ac := s.State.Aircraft[dep.Callsign]

	// If it's hold for release make sure both that it has been released
	// and that a sufficient delay has passed since the release was issued.
	if ac.HoldForRelease {
		if !ac.Released {
			return false
		} else if s.State.SimTime.Sub(ac.ReleaseTime) < dep.ReleaseDelay {
			return false
		}
	}

	prevDep := s.DepartureState[airport].LastRunwayDeparture[dep.Runway]
	if prevDep == nil {
		// No previous departure on this runway, so there's nothing
		// stopping us.
		return true
	}

	// Make sure enough time has passed since the last departure.
	elapsed := s.State.SimTime.Sub(prevDep.LaunchTime)
	return elapsed > s.launchInterval(*prevDep, dep)
}

// launchInterval returns the amount of time we must wait before launching
// cur, if prev was the last aircraft launched.
func (s *Sim) launchInterval(prev, cur DepartureAircraft) time.Duration {
	cac := s.State.Aircraft[cur.Callsign]
	pac, ok := s.State.Aircraft[prev.Callsign]
	if !ok {
		// Previous was presumably deleted
		return 0
	}

	// FIXME: for now we assume we can launch on different runways
	// independently.
	if prev.Runway != cur.Runway {
		return 0
	}

	// Check for wake turbulence separation.
	wtDist := av.CWTDirectlyBehindSeparation(pac.CWT(), cac.CWT())
	if wtDist != 0 {
		// Assume '1 gives you 3.5'
		return time.Duration(wtDist / 3.5 * float32(time.Minute))
	}

	// Assume this will be less than wake turbulence
	return prev.MinSeparation
}

func (s *Sim) refreshDeparturePool(airport string) {
	depState := s.DepartureState[airport]
	// Keep a pool of 2-5 around.
	if len(depState.Pool) >= 2 {
		return
	}

	for range 3 {
		// Figure out which category to generate.
		ap := s.State.DepartureAirports[airport]
		vfrRate := scaleRate(float32(ap.VFRRateSum()), s.State.LaunchConfig.VFRDepartureRateScale)
		ifrRate := float32(0)
		rates, ok := s.State.LaunchConfig.DepartureRates[airport]
		if ok {
			ifrRate = sumRateMap2(rates, s.State.LaunchConfig.DepartureRateScale)
		}
		if ifrRate == 0 && vfrRate == 0 {
			// The airport currently has a 0 departure rate.
			return
		}

		var ac *av.Aircraft
		var err error
		var runway string
		if vfrRate > 0 && rand.Float32() < vfrRate/(vfrRate+ifrRate) {
			// Don't waste time trying to find a valid launch if it's been
			// near-impossible to find valid routes.
			if depState.VFRAttempts < 400 ||
				(depState.VFRSuccesses > 0 && depState.VFRAttempts/depState.VFRSuccesses < 200) {
				// Add a VFR
				ac, runway, err = s.createVFRDeparture(airport)
			}
		} else if ifrRate > 0 {
			// Add an IFR
			var category string
			var rateSum float32
			runway, category, rateSum = sampleRateMap2(rates, s.State.LaunchConfig.DepartureRateScale)
			if rateSum > 0 {
				ac, err = s.createDepartureNoLock(airport, runway, category)
			}
		}

		if err == nil && ac != nil {
			ac.WaitingForLaunch = true
			s.addAircraftNoLock(*ac)

			depState.addDeparture(ac, runway, s.State /* wind */)
		}
	}

	// We've updated the pool; resequence them.
	depState.resequence(s)
}

func (d *DepartureLaunchState) reset() {
	d.Pool = nil
	clear(d.LastRunwayDeparture)
}

func (d *DepartureLaunchState) addDeparture(ac *av.Aircraft, runway string, wind av.WindModel) {
	d.Pool = append(d.Pool, makeDepartureAircraft(ac, runway, d.Index, wind))
	d.Index++
}

func (d *DepartureLaunchState) resequence(s *Sim) {
	if len(d.Pool) <= 1 {
		return
	}

	// If the oldest one has been hanging around and not launched,
	// eventually force it; this way we don't keep kicking the can down the
	// road on a super indefinitely...
	minIdx := 1000000
	minIdxCallsign := ""
	for _, dac := range d.Pool {
		if dac.Index < minIdx && d.Index-dac.Index >= 7 {
			minIdx = dac.Index
			minIdxCallsign = dac.Callsign
		}
	}

	var bestOrder []DepartureAircraft
	bestDuration := 24 * time.Hour

	for depPerm := range util.AllPermutations(d.Pool) {
		// Manifest the permutation into a slice so we can keep the best one.
		var perm []DepartureAircraft
		for _, dep := range depPerm {
			perm = append(perm, dep)
		}

		// If we have decided that an aircraft that has been waiting is
		// going to go first, make sure it is so in this permutation. (We
		// could do this more elegantly...)
		if minIdxCallsign != "" && perm[0].Callsign != minIdxCallsign {
			continue
		}

		// Figure out how long it would take to launch them in this order.
		var dur time.Duration
		prevDep := d.LastRunwayDeparture[perm[0].Runway]
		for i := range perm {
			if prevDep != nil {
				dur += s.launchInterval(*prevDep, perm[i])
			}
			prevDep = &perm[i]
		}

		if dur < bestDuration {
			bestDuration = dur
			bestOrder = perm
		}
	}

	d.Pool = bestOrder
}

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
	"PSA5342": nil,
}

func (ss *State) sampleAircraft(al av.AirlineSpecifier, lg *log.Logger) (*av.Aircraft, string) {
	dbAirline, ok := av.DB.Airlines[al.ICAO]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Airline %s, not found in database", al.ICAO)
		return nil, ""
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range al.Aircraft() {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := av.DB.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from airline %+v",
			aircraft, al)
		return nil, ""
	}

	// random callsign
	callsign := strings.ToUpper(dbAirline.ICAO)
	for {
		format := "####"
		if len(dbAirline.Callsign.CallsignFormats) > 0 {
			f, ok := rand.SampleWeighted(dbAirline.Callsign.CallsignFormats,
				func(f string) int {
					if _, wt, ok := strings.Cut(f, "x"); ok { // we have a weight
						if v, err := strconv.Atoi(wt); err == nil {
							return v
						}
					}
					return 1
				})
			if ok {
				format = f
			}
		}

		id := ""
	loop:
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					// Don't start with a 0.
					id += strconv.Itoa(1 + rand.Intn(9))
				} else {
					id += strconv.Itoa(rand.Intn(10))
				}
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			case 'x':
				break loop
			}
		}
		if _, ok := ss.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
			callsign += id
			break
		}
	}

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &av.Aircraft{
		Callsign: callsign,
		Mode:     av.Altitude,
	}, acType
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createArrivalNoLock(arrivalGroup, arrivalAirport)
}

func (s *Sim) createArrivalNoLock(group string, arrivalAirport string) (*av.Aircraft, error) {
	goAround := rand.Float32() < s.State.LaunchConfig.GoAroundRate

	arrivals := s.State.InboundFlows[group].Arrivals
	// Randomly sample from the arrivals that have a route to this airport.
	idx := rand.SampleFiltered(arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			group, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := rand.SampleSlice(arr.Airlines[arrivalAirport])
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	sq, err := s.State.ERAMComputer().CreateSquawk()
	if err != nil {
		return nil, err
	}
	ac.Squawk = sq
	ac.FlightPlan = ac.NewFlightPlan(av.IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		arrivalController, err = s.State.MultiControllers.GetInboundController(group)
		if err != nil {
			s.lg.Error("Unable to resolve arrival controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}

		if arrivalController == "" {
			arrivalController = s.State.PrimaryController
		}
	}

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], &arr, arrivalController,
		goAround, s.State.NmPerLongitude, s.State.MagneticVariation, s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	facility, ok := s.State.FacilityFromController(ac.TrackingController)
	if !ok {
		return nil, ErrUnknownControllerFacility
	}
	s.State.ERAMComputers.AddArrival(ac, facility, s.State.STARSFacilityAdaptation, s.State.SimTime)

	return ac, nil
}

func (s *Sim) CreateDeparture(departureAirport, runway, category string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createDepartureNoLock(departureAirport, runway, category)
}

func (s *Sim) createDepartureNoLock(departureAirport, runway, category string) (*av.Aircraft, error) {
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r DepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]

	// Sample uniformly, minding the category, if specified
	idx = rand.SampleFiltered(ap.Departures,
		func(d av.Departure) bool {
			_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
			return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
		})
	if idx == -1 {
		// This shouldn't ever happen...
		return nil, fmt.Errorf("%s/%s: unable to find a valid departure",
			departureAirport, rwy.Runway)
	}
	dep := &ap.Departures[idx]

	airline := rand.SampleSlice(dep.Airlines)
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.IFR
	if dep.Unassociated {
		ac.Squawk = 0o1200
		rules = av.VFR
		if r := rand.Float32(); r < .02 {
			ac.Mode = av.On // mode-A
		} else if r < .03 {
			ac.Mode = av.Standby // flat out off
		}
	} else {
		sq, err := s.State.ERAMComputer().CreateSquawk()
		if err != nil {
			return nil, err
		}
		ac.Squawk = sq
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, departureAirport, dep.Destination)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(ap, departureAirport, dep, runway, *exitRoute,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State.STARSFacilityAdaptation.Scratchpads,
		s.State.PrimaryController, s.State.MultiControllers, s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	if rules == av.IFR {
		eram := s.State.ERAMComputer()
		eram.AddDeparture(ac.FlightPlan, s.State.TRACON, s.State.SimTime)
	}

	return ac, nil
}

func (s *Sim) CreateOverflight(group string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createOverflightNoLock(group)
}

func (s *Sim) createOverflightNoLock(group string) (*av.Aircraft, error) {
	overflights := s.State.InboundFlows[group].Overflights
	// Randomly sample an overflight
	of := rand.SampleSlice(overflights)

	airline := rand.SampleSlice(of.Airlines)
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.IFR
	if of.Unassociated {
		ac.Squawk = 0o1200
		rules = av.VFR
		if r := rand.Float32(); r < .02 {
			ac.Mode = av.On // mode-A
		} else if r < .03 {
			ac.Mode = av.Standby // flat out off
		}
	} else {
		sq, err := s.State.ERAMComputer().CreateSquawk()
		if err != nil {
			return nil, err
		}
		ac.Squawk = sq
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, airline.DepartureAirport,
		airline.ArrivalAirport)

	// Figure out which controller will (for starters) get the handoff. For
	// single-user, it's easy.  Otherwise, figure out which control
	// position is initially responsible for the arrival. Note that the
	// actual handoff controller will be resolved later when the handoff
	// happens, so that it can reflect which controllers are actually
	// signed in at that point.
	controller := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		controller, err = s.State.MultiControllers.GetInboundController(group)
		if err != nil {
			s.lg.Error("Unable to resolve overflight controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}
		if controller == "" {
			controller = s.State.PrimaryController
		}
	}

	if err := ac.InitializeOverflight(&of, controller, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	return ac, nil
}

func makeDepartureAircraft(ac *av.Aircraft, runway string, idx int, wind av.WindModel) DepartureAircraft {
	d := DepartureAircraft{
		Callsign: ac.Callsign,
		Runway:   runway,
		Index:    idx,
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft.
	simAc := *ac
	start := ac.Position()
	d.MinSeparation = 120 * time.Second // just in case
	for i := range 120 {
		simAc.Update(wind, nil /* lg */)
		// We need 6,000' and airborne, but we'll add a bit of slop
		if simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			break
		}
	}

	return d
}

func (s *Sim) createVFRDeparture(depart string) (*av.Aircraft, string, error) {
	ap := s.State.DepartureAirports[depart]

	// Sample among the randoms and the routes
	rateSum := 0
	var sampledRandoms *av.VFRRandomsSpec
	var sampledRoute *av.VFRRouteSpec
	if ap.VFR.Randoms.Rate > 0 {
		rateSum = ap.VFR.Randoms.Rate
		sampledRandoms = &ap.VFR.Randoms
	}
	for _, route := range ap.VFR.Routes {
		if route.Rate > 0 {
			rateSum += route.Rate
			p := float32(route.Rate) / float32(rateSum)
			if rand.Float32() < p {
				sampledRandoms = nil
				sampledRoute = &route
			}
		}
	}

	for range 5 {
		s.DepartureState[depart].VFRAttempts++

		var ac *av.Aircraft
		var runway string
		var err error
		if sampledRandoms != nil {
			// Sample destination airport: may be where we started from.
			arrive, ok := rand.SampleWeightedSeq(maps.Keys(s.State.DepartureAirports),
				func(ap string) int { return s.State.DepartureAirports[ap].VFRRateSum() })
			if !ok {
				fmt.Printf("%s: unable to sample destination airport???\n", depart)
				continue
			}
			ac, runway, err = s.createUncontrolledVFRDeparture(depart, arrive, sampledRandoms.Fleet, nil)
		} else if sampledRoute != nil {
			ac, runway, err = s.createUncontrolledVFRDeparture(depart, sampledRoute.Destination, sampledRoute.Fleet,
				sampledRoute.Waypoints)
		}

		if err == nil && ac != nil {
			s.DepartureState[depart].VFRSuccesses++
			return ac, runway, nil
		}
	}

	return nil, "", ErrViolatedAirspace
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive, fleet string, routeWps []av.Waypoint) (*av.Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy, opp := depap.SelectBestRunway(s.State /* wind */, s.State.MagneticVariation)

	ac, acType := s.State.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, s.lg)
	if ac == nil {
		return nil, "", fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.VFR
	ac.Squawk = 0o1200
	if r := rand.Float32(); r < .02 {
		ac.Mode = av.On // mode-A
	} else if r < .03 {
		ac.Mode = av.Standby // flat out off
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, depart, arrive)

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	base := math.Max(depap.Elevation, arrap.Elevation)
	base = 1000 + 1000*(base/1000) // round to 1000s.
	var alt int
	randalt := func(n int) int { return base + (1+rand.Intn(n))*1000 }
	if dist == 0 {
		// returning to same airport
		alt = randalt(4)
	} else if dist < 25 {
		// short hop
		alt = randalt(4)
	} else if dist < 50 {
		alt = randalt(8)
	} else {
		alt = randalt(16)
	}
	alt = math.Min(alt, 17000)
	alt = math.Min(alt, int(av.DB.AircraftPerformance[acType].Ceiling))
	alt += 500

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist := float32(10 + rand.Intn(20))
		hdg := float32(1 + rand.Intn(360))
		v := [2]float32{dist * math.Sin(math.Radians(hdg)), dist * math.Cos(math.Radians(hdg))}
		dnm := math.LL2NM(depap.Location, s.State.NmPerLongitude)
		midnm := math.Add2f(dnm, v)
		mid = math.NM2LL(midnm, s.State.NmPerLongitude)
	}

	var wps []av.Waypoint
	wps = append(wps, av.Waypoint{Fix: "_dep_threshold", Location: rwy.Threshold})
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp.Threshold})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp.Threshold, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wp0.FlyOver = true
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg float32
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp.Threshold, routeWps[0].Location, s.State.NmPerLongitude, s.State.MagneticVariation)
	} else {
		hdg = math.Heading2LL(opp.Threshold, mid, s.State.NmPerLongitude, s.State.MagneticVariation)
	}
	turn := math.HeadingSignedTurn(rwy.Heading, hdg)
	if turn < -120 {
		// left downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, 1.5))
	} else if turn > 120 {
		// right downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, -1.5))
	}

	var randomizeAltitudeRange bool
	if len(routeWps) > 0 {
		wps = append(wps, routeWps...)
		randomizeAltitudeRange = true
	} else {
		randomizeAltitudeRange = false
		depEnd := wps[len(wps)-1].Location

		radius := .15 * dist
		airwork := func() bool {
			if depart == arrive {
				return rand.Intn(3) == 0
			}
			return rand.Intn(10) == 0
		}()

		const nsteps = 10
		for i := 1; i < nsteps; i++ { // skip first one
			t := (float32(i) + 0.5) / nsteps
			pt := func() math.Point2LL {
				if i <= nsteps/2 {
					return math.Lerp2f(2*t, depEnd, mid)
				} else {
					return math.Lerp2f(2*t-1, mid, arrap.Location)
				}
			}()

			// At or below so that they descend for the last one
			ar := &av.AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}
			if i == nsteps-1 {
				ar = &av.AltitudeRestriction{
					Range: [2]float32{float32(arrap.Elevation) + 1500, float32(arrap.Elevation) + 2000}}
			} else if i > nsteps/2 {
				ar.Range[0] = 0 // at or below
			}

			wps = append(wps, av.Waypoint{
				Fix:                 "_route" + strconv.Itoa(i),
				Location:            pt,
				AltitudeRestriction: ar,
				Radius:              util.Select(i <= 1, 0.2*radius, radius),
			})

			if airwork && i == nsteps/2 {
				wps[len(wps)-1].AirworkRadius = 4 + rand.Intn(4)
				wps[len(wps)-1].AirworkMinutes = 5 + rand.Intn(15)
				wps[len(wps)-1].AltitudeRestriction.Range[0] -= 500
				wps[len(wps)-1].AltitudeRestriction.Range[1] += 2000
			}
		}
	}

	wps[len(wps)-1].Land = true

	if err := ac.InitializeVFRDeparture(s.State.Airports[depart], wps, alt, randomizeAltitudeRange,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State /* wind */, s.lg); err != nil {
		return nil, "", err
	}

	if s.bravoAirspace == nil || s.charlieAirspace == nil {
		s.initializeAirspaceGrids()
	}

	// Check airspace violations
	simac := deep.MustCopy(*ac)
	for range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simac.Update(s.State /* wind */, nil); wp != nil && wp.Delete {
			return ac, rwy.Id, nil
		}
		if s.bravoAirspace.Inside(simac.Position(), int(simac.Altitude())) ||
			s.charlieAirspace.Inside(simac.Position(), int(simac.Altitude())) {
			return nil, "", ErrViolatedAirspace
		}
	}

	s.lg.Infof("%s: %s/%s aircraft not finished after 3 hours of sim time",
		ac.Callsign, depart, arrive)
	return nil, "", ErrVFRSimTookTooLong
}

func (s *Sim) initializeAirspaceGrids() {
	initAirspace := func(a map[string][]av.AirspaceVolume) *av.AirspaceGrid {
		var vols []*av.AirspaceVolume
		for volslice := range maps.Values(a) {
			for _, v := range volslice {
				vols = append(vols, &v)
			}
		}
		return av.MakeAirspaceGrid(vols)
	}
	s.bravoAirspace = initAirspace(av.DB.BravoAirspace)
	s.charlieAirspace = initAirspace(av.DB.CharlieAirspace)
}
