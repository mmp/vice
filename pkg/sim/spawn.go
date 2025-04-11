// pkg/sim/spawn.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"iter"
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
const initialSimControlledSeconds = 90

type RunwayLaunchState struct {
	IFRSpawnRate float32
	VFRSpawnRate float32

	// For each runway, when to create the next departing aircraft, based
	// on the runway departure rate. The actual time an aircraft is
	// launched may be later, e.g. if we need longer for wake turbulence
	// separation, etc.
	NextIFRSpawn time.Time
	NextVFRSpawn time.Time

	// Aircraft follow the following flows:
	// VFR, IFR no release: Gate -> Released -> Sequenced
	// IFR release requires: Gate -> Held -> Released -> Sequenced

	// At the gate, flight plan filed (if IFR), not yet ready to go
	Gate []DepartureAircraft
	// Ready to go, in hold for release purgatory.
	Held []DepartureAircraft
	// Ready to go.
	Released []DepartureAircraft
	// Sequenced departures, pulled from Released. These are launched in-order.
	Sequenced []DepartureAircraft

	LastDeparture *DepartureAircraft

	VFRAttempts  int
	VFRSuccesses int
}

// DepartureAircraft represents a departing aircraft, either still on the
// ground or recently-launched.
type DepartureAircraft struct {
	ADSBCallsign  av.ADSBCallsign
	MinSeparation time.Duration // How long after takeoff it will be at ~6000' and airborne
	SpawnTime     time.Time     // when it was first spawned
	LaunchTime    time.Time     // when it was actually launched; used for wake turbulence separation, etc.

	// When they're ready to leave the gate
	DepartGate time.Time

	// HFR-only.
	ReleaseRequested   bool
	ReleaseDelay       time.Duration // minimum wait after release before the takeoff roll
	RequestReleaseTime time.Time
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
		for rwy, categoryRates := range rwyRates {
			r := sumRateMap(categoryRates, s.State.LaunchConfig.DepartureRateScale)
			s.DepartureState[ap][rwy].setIFRRate(s, r)
		}

		for name, ap := range lc.VFRAirports {
			r := scaleRate(float32(ap.VFRRateSum()), lc.VFRDepartureRateScale)
			rwy := s.State.VFRRunways[name]
			s.DepartureState[name][rwy.Id].setVFRRate(s, r)
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

func (s *Sim) LaunchAircraft(ac Aircraft, departureRunway string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if departureRunway != "" && ac.HoldForRelease {
		s.addDepartureToPool(&ac, departureRunway)
	} else {
		s.addAircraftNoLock(ac)
	}
}

func (s *Sim) addDepartureToPool(ac *Aircraft, runway string) {
	depac := makeDepartureAircraft(ac, s.State.SimTime, s.State /* wind */)

	ac.WaitingForLaunch = true
	s.addAircraftNoLock(*ac)

	// The journey begins...
	depState := s.DepartureState[ac.FlightPlan.DepartureAirport][runway]
	depState.Gate = append(depState.Gate, depac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) addAircraftNoLock(ac Aircraft) {
	if _, ok := s.Aircraft[ac.ADSBCallsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!",
			slog.String("adsb_callsign", string(ac.ADSBCallsign)))
		return
	}

	s.Aircraft[ac.ADSBCallsign] = &ac

	ac.Nav.Check(s.lg)

	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		s.State.TotalIFR++
	} else {
		s.State.TotalVFR++
	}

	if ac.IsDeparture() {
		s.lg.Info("launched departure", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else if ac.IsArrival() {
		s.lg.Info("launched arrival", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else if ac.IsOverflight() {
		s.lg.Info("launched overflight", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
			slog.Any("aircraft", ac))
	} else {
		fmt.Printf("%s: launched unknown type?\n", ac.ADSBCallsign)
	}
}

func (s *Sim) Prespawn() {
	s.lg.Info("starting aircraft prespawn")

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	s.setInitialSpawnTimes(t)
	s.prespawn = true
	for i := 0; i < initialSimSeconds; i++ {
		// Controlled only at the tail end.
		s.prespawnUncontrolledOnly = i < initialSimSeconds-initialSimControlledSeconds

		s.State.SimTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.prespawnUncontrolledOnly, s.prespawn = false, false

	s.State.SimTime = time.Now()
	s.lastUpdateTime = time.Now()

	s.lg.Info("finished aircraft prespawn")
}

func (s *Sim) setInitialSpawnTimes(now time.Time) {
	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	randomDelay := func(rate float32) time.Time {
		if rate == 0 {
			return time.Now().Add(365 * 24 * time.Hour)
		}
		avgWait := int(3600 / rate)
		delta := rand.Intn(avgWait) - avgWait/2
		return now.Add(time.Duration(delta) * time.Second)
	}

	if s.State.LaunchConfig.ArrivalPushes {
		// Figure out when the next arrival push will start
		m := 1 + rand.Intn(s.State.LaunchConfig.ArrivalPushFrequencyMinutes)
		s.NextPushStart = now.Add(time.Duration(m) * time.Minute)
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
		s.DepartureState[name] = make(map[string]*RunwayLaunchState)

		if runwayRates, ok := s.State.LaunchConfig.DepartureRates[name]; ok {
			for rwy, rate := range runwayRates {
				r := sumRateMap(rate, s.State.LaunchConfig.DepartureRateScale)
				s.DepartureState[name][rwy] = &RunwayLaunchState{
					IFRSpawnRate: r,
					NextIFRSpawn: randomDelay(r),
				}
			}
		}

		if vfrRate := float32(ap.VFRRateSum()); vfrRate > 0 {
			rwy := s.State.VFRRunways[name]
			state, ok := s.DepartureState[name][rwy.Id]
			if !ok {
				state = &RunwayLaunchState{}
				s.DepartureState[name][rwy.Id] = state
			}
			state.VFRSpawnRate = scaleRate(vfrRate, s.State.LaunchConfig.VFRDepartureRateScale)
			state.NextVFRSpawn = randomDelay(state.VFRSpawnRate)
		}
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

func sumRateMap(rates map[string]float32, scale float32) float32 {
	var sum float32
	for _, rate := range rates {
		sum += scaleRate(rate, scale)
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
	if s.State.LaunchConfig.Mode == LaunchAutomatic {
		// Don't spawn automatically if someone is spawning manually.
		s.spawnArrivalsAndOverflights()
		s.spawnDepartures()
	}
	s.updateDepartureSequence()
}

func (s *Sim) isControlled(ac *Aircraft, departure bool) bool {
	if ac.FlightPlan.Rules == av.FlightRulesVFR {
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

			var ac *Aircraft
			var err error
			if flow == "overflights" {
				ac, err = s.createOverflightNoLock(group)
			} else {
				ac, err = s.createArrivalNoLock(group, flow)
			}

			if err != nil {
				s.lg.Errorf("create inbound error: %v", err)
			} else if ac != nil {
				if s.prespawnUncontrolledOnly && s.isControlled(ac, false) {
					s.lg.Infof("%s: discarding arrival/overflight\n", ac.ADSBCallsign)
					s.deleteAircraft(ac)
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

	for airport, runways := range s.DepartureState {
		for runway, depState := range runways {
			// Possibly spawn another aircraft, depending on how much time has
			// passed since the last one.
			if now.After(depState.NextIFRSpawn) {
				if ac, err := s.makeNewIFRDeparture(airport, runway); ac != nil && err == nil {
					s.addDepartureToPool(ac, runway)
					r := scaleRate(depState.IFRSpawnRate, s.State.LaunchConfig.DepartureRateScale)
					depState.NextIFRSpawn = now.Add(randomWait(r, false))
				}
			}
			if now.After(depState.NextVFRSpawn) {
				if ac, err := s.makeNewVFRDeparture(airport, runway); ac != nil && err == nil {
					s.addDepartureToPool(ac, runway)
					r := scaleRate(depState.VFRSpawnRate, s.State.LaunchConfig.DepartureRateScale)
					depState.NextVFRSpawn = now.Add(randomWait(r, false))
				}
			}
		}
	}
}

func (s *Sim) updateDepartureSequence() {
	now := s.State.SimTime

	for airport, runways := range s.DepartureState {
		for depRunway, depState := range runways {
			changed := func() { // Debugging...
				if false {
					callsign := func(dep DepartureAircraft) string {
						return string(dep.ADSBCallsign) + "/" + depRunway + "/" + s.Aircraft[dep.ADSBCallsign].FlightPlan.Exit
					}
					fmt.Printf("%s: Gate %s Held %s Released %s Sequence %s\n", airport,
						strings.Join(util.MapSlice(depState.Gate, callsign), ", "),
						strings.Join(util.MapSlice(depState.Held, callsign), ", "),
						strings.Join(util.MapSlice(depState.Released, callsign), ", "),
						strings.Join(util.MapSlice(depState.Sequenced, callsign), ", "))
				}
			}

			// Clear out any aircraft that aren't in s.Aircraft any more
			// (i.e., deleted by the user). Thence we will be able to access
			// Aircraft without checking for success in the following.
			haveAc := func(dep DepartureAircraft) bool {
				_, ok := s.Aircraft[dep.ADSBCallsign]
				return ok
			}
			depState.Gate = util.FilterSliceInPlace(depState.Gate, haveAc)
			depState.Held = util.FilterSliceInPlace(depState.Held, haveAc)
			depState.Released = util.FilterSliceInPlace(depState.Released, haveAc)
			depState.Sequenced = util.FilterSliceInPlace(depState.Sequenced, haveAc)

			// See if anyone at the gate is ready to go
			depState.Gate = util.FilterSliceInPlace(depState.Gate,
				func(dep DepartureAircraft) bool {
					if now.Before(dep.DepartGate) {
						return true // keep it in Gate
					}
					ac := s.Aircraft[dep.ADSBCallsign]
					if ac.HoldForRelease {
						dep.RequestReleaseTime = now.Add(time.Duration(60+rand.Intn(60)) * time.Second)
						s.STARSComputer.AddHeldDeparture(ac)
						depState.Held = append(depState.Held, dep)
					} else {
						depState.Released = append(depState.Released, dep)
					}
					changed()
					return false // remove from Gate slice
				})

			// Handle hold for release aircraft
			for i, held := range depState.Held {
				if now.Before(held.RequestReleaseTime) {
					// Ensure FIFO processing of held
					break
				}

				if !held.ReleaseRequested {
					if s.prespawnUncontrolledOnly {
						// We got to this point but don't want controlled aircraft yet.
						s.deleteAircraft(s.Aircraft[held.ADSBCallsign])
						depState.Held = append(depState.Held[:i], depState.Held[:i+1]...)
						changed()
						break
					}
					depState.Held[i].ReleaseRequested = true
					depState.Held[i].ReleaseDelay = time.Duration(20+rand.Intn(100)) * time.Second
				}
			}
			if len(depState.Held) > 0 {
				// Held go to Released in FIFO order so only consider the first one added.
				if dep := depState.Held[0]; dep.ReleaseRequested {
					ac := s.Aircraft[dep.ADSBCallsign]
					if ac.Released && now.After(ac.ReleaseTime.Add(dep.ReleaseDelay)) {
						depState.Released = append(depState.Released, dep)
						depState.Held = depState.Held[1:]
						changed()
					}
				}
			}

			// Try to keep about 10 minutes worth of departures in the Released pool.
			minReleased := math.Max(1, int((depState.VFRSpawnRate+depState.IFRSpawnRate)/6))
			if len(depState.Released) >= minReleased {
				// Check for any released that have been hanging along a long time.
				var maxWait time.Duration
				maxWaitIdx := -1
				for i, rel := range depState.Released {
					ac := s.Aircraft[rel.ADSBCallsign]
					if w := now.Sub(ac.ReleaseTime); w > 10*time.Minute && w > maxWait {
						maxWait = w
						maxWaitIdx = i
					}
				}
				if maxWaitIdx != -1 {
					depState.Sequenced = append(depState.Sequenced, depState.Released[maxWaitIdx])
					depState.Released = append(depState.Released[:maxWaitIdx], depState.Released[maxWaitIdx+1:]...)
					changed()
				} else {
					minWait := 24 * 60 * time.Minute
					minWaitIdx := -1
					for i, dep := range depState.Released {
						// Sequence w.r.t. the aircraft in front; if none
						// in sequence, then the last departure, otherwise
						// the last one in the sequence.
						prevDep := depState.LastDeparture
						if prevDep == nil && len(depState.Sequenced) > 0 {
							prevDep = &depState.Sequenced[len(depState.Sequenced)-1]
						}

						if prevDep == nil {
							depState.Sequenced = append(depState.Sequenced, depState.Released[i])
							depState.Released = append(depState.Released[:i], depState.Released[i+1:]...)
							changed()
							break
						} else {
							wait := s.launchInterval(*prevDep, dep, true)
							if wait < minWait {
								minWait = wait
								minWaitIdx = i
							}
						}
					}
					if minWaitIdx != -1 {
						depState.Sequenced = append(depState.Sequenced, depState.Released[minWaitIdx])
						depState.Released = append(depState.Released[:minWaitIdx], depState.Released[minWaitIdx+1:]...)
						changed()
					}
				}
			}

			// See if we have anything to launch
			considerExit := len(depState.Sequenced) == 1 // if it's just us waiting, don't rush it unnecessarily
			if len(depState.Sequenced) > 0 && s.canLaunch(depState.LastDeparture, depState.Sequenced[0], considerExit) {
				dep := &depState.Sequenced[0]
				ac := s.Aircraft[dep.ADSBCallsign]

				if s.prespawnUncontrolledOnly && s.isControlled(ac, true) {
					// womp womp, discard it
					depState.Sequenced = depState.Sequenced[1:]
					s.deleteAircraft(ac)
				} else {
					// Launch!
					ac.WaitingForLaunch = false

					// Record the launch so we have it when we consider
					// launching the next one.
					dep.LaunchTime = now
					depState.LastDeparture = dep

					// Remove it from the pool of waiting departures.
					depState.Sequenced = depState.Sequenced[1:]

					// Sometimes a departure from one runway should be
					// considered when deciding if it's ok to launch from
					// another runway (e.g., closely spaced parallel runways).
					for rwy, state := range s.sameGroupRunways(airport, depRunway) {
						s.lg.Infof("%s: %q departure also holding up %q", ac.ADSBCallsign, depRunway, rwy)
						state.LastDeparture = dep
					}
				}

				changed()
			}
		}
	}
}

// sameGroupRunways returns an iterator over all of the runways in the
// ~equivalence class with the given depRwy. Such equivalences can come
// both from user-specified "departure_runways_as_one" but also from
// runways with dotted suffixes; we want to treat 4 and 4.AutoWest as one,
// for example.  Note that the iterator will return the provided runway and
// may return the same runway multiple times.
func (s *Sim) sameGroupRunways(airport, depRwy string) iter.Seq2[string, *RunwayLaunchState] {
	depRwy = av.TidyRunway(depRwy)
	runwayState := s.DepartureState[airport]
	return func(yield func(string, *RunwayLaunchState) bool) {
		// First look at departure runways as one
		for _, group := range s.State.Airports[airport].DepartureRunwaysAsOne {
			groupRwys := strings.Split(group, ",")
			if slices.Contains(groupRwys, depRwy) {
				for rwy, state := range runwayState {
					if slices.Contains(groupRwys, av.TidyRunway(rwy)) {
						if !yield(rwy, state) {
							return
						}
					}
				}
				break
			}
		}

		// Now see look for departing both e.g. "4" and "4.AutoWest"
		for rwy, state := range runwayState {
			if depRwy == av.TidyRunway(rwy) {
				if !yield(rwy, state) {
					return
				}
			}
		}
	}
}

// canLaunch checks whether we can go ahead and launch dep.
func (s *Sim) canLaunch(prevDep *DepartureAircraft, dep DepartureAircraft, considerExit bool) bool {
	if prevDep == nil {
		// No previous departure on this runway, so there's nothing
		// stopping us.
		return true
	} else {
		// Make sure enough time has passed since the last departure.
		elapsed := s.State.SimTime.Sub(prevDep.LaunchTime)
		return elapsed > s.launchInterval(*prevDep, dep, considerExit)
	}
}

// launchInterval returns the amount of time we must wait before launching
// cur, if prev was the last aircraft launched.
func (s *Sim) launchInterval(prev, cur DepartureAircraft, considerExit bool) time.Duration {
	cac, cok := s.Aircraft[cur.ADSBCallsign]
	pac, pok := s.Aircraft[prev.ADSBCallsign]

	if !cok || !pok {
		// Presumably the last launch has already landed or otherwise been
		// deleted.
		s.lg.Infof("Sim launchInterval missing an aircraft %q: %v / %q: %v", cur.ADSBCallsign, cok,
			prev.ADSBCallsign, pok)
		return 0
	}

	// When sequencing, penalize same-exit repeats. But when we have a
	// sequence and are launching, we'll let it roll.
	if considerExit && cac.FlightPlan.Exit == pac.FlightPlan.Exit {
		return 3 * time.Minute / 2
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

func (s *Sim) makeNewIFRDeparture(airport, runway string) (ac *Aircraft, err error) {
	depState := s.DepartureState[airport][runway]
	if len(depState.Gate) >= 10 {
		// There's a backup; hold off on more.
		return
	}

	if depState.IFRSpawnRate == 0 {
		return
	}

	rates, ok := s.State.LaunchConfig.DepartureRates[airport][runway]
	if ok {
		category, rateSum := sampleRateMap(rates, s.State.LaunchConfig.DepartureRateScale)
		if rateSum > 0 {
			ac, err = s.createIFRDepartureNoLock(airport, runway, category)

			if ac != nil && !ac.HoldForRelease {
				ac.ReleaseTime = s.State.SimTime
			}
		}
	}

	return
}

func (s *Sim) makeNewVFRDeparture(depart, runway string) (ac *Aircraft, err error) {
	depState := s.DepartureState[depart][runway]
	if len(depState.Held) >= 5 || len(depState.Released) >= 5 || len(depState.Sequenced) >= 5 {
		// There's a backup; hold off on more.
		return
	}

	if depState.VFRSpawnRate == 0 {
		return
	}

	// Don't waste time trying to find a valid launch if it's been
	// near-impossible to find valid routes.
	if depState.VFRAttempts < 400 ||
		(depState.VFRSuccesses > 0 && depState.VFRAttempts/depState.VFRSuccesses < 200) {
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
			depState.VFRAttempts++

			if sampledRandoms != nil {
				// Sample destination airport: may be where we started from.
				arrive, ok := rand.SampleWeightedSeq(maps.Keys(s.State.DepartureAirports),
					func(ap string) int { return s.State.DepartureAirports[ap].VFRRateSum() })
				if !ok {
					s.lg.Errorf("%s: unable to sample VFR destination airport???", depart)
					continue
				}
				ac, runway, err = s.createUncontrolledVFRDeparture(depart, arrive, sampledRandoms.Fleet, nil)
			} else if sampledRoute != nil {
				ac, runway, err = s.createUncontrolledVFRDeparture(depart, sampledRoute.Destination, sampledRoute.Fleet,
					sampledRoute.Waypoints)
			}

			if err == nil && ac != nil {
				ac.ReleaseTime = s.State.SimTime
				depState.VFRSuccesses++
				return
			}
		}
		return nil, ErrViolatedAirspace
	}
	return
}

func (s *Sim) cullDepartures(keep int, d []DepartureAircraft) []DepartureAircraft {
	if len(d) < keep {
		return d
	}

	for _, dep := range d[keep:] {
		if ac, ok := s.Aircraft[dep.ADSBCallsign]; ok {
			s.deleteAircraft(ac)
		}
	}
	return d[:keep]
}

func (d *RunwayLaunchState) cullDepartures(s *Sim) {
	keep := int(d.IFRSpawnRate+d.VFRSpawnRate) / 6
	d.Gate = s.cullDepartures(keep, d.Gate)
	d.Held = s.cullDepartures(keep, d.Held)
	d.Released = s.cullDepartures(keep, d.Released)
	d.Sequenced = s.cullDepartures(keep, d.Sequenced)
}

func (d *RunwayLaunchState) setIFRRate(s *Sim, r float32) {
	if r == d.IFRSpawnRate {
		return
	}
	d.IFRSpawnRate = r
	d.NextIFRSpawn = s.State.SimTime.Add(randomWait(r*2, false))
	d.cullDepartures(s)
}

func (d *RunwayLaunchState) setVFRRate(s *Sim, r float32) {
	if r == d.VFRSpawnRate {
		return
	}
	d.VFRSpawnRate = r
	d.NextVFRSpawn = s.State.SimTime.Add(randomWait(r*2, false))
	d.cullDepartures(s)
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createArrivalNoLock(arrivalGroup, arrivalAirport)
}

func (s *Sim) createArrivalNoLock(group string, arrivalAirport string) (*Aircraft, error) {
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
	ac, acType := s.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, airline.Airport, arrivalAirport)

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

	ac.WaypointHandoffController = arrivalController

	err := ac.InitializeArrival(s.State.Airports[arrivalAirport], &arr,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State /* wind */, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	starsFp := STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: "", // TODO
		ExitFix:  util.Select(len(ac.FlightPlan.ArrivalAirport) == 4, ac.FlightPlan.ArrivalAirport[1:], ac.FlightPlan.ArrivalAirport),
		ETAOrPTD: getAircraftTime(s.State.SimTime),

		TrackingController:    arr.InitialController,
		ControllingController: arr.InitialController,

		Rules:        av.FlightRulesIFR,
		TypeOfFlight: av.FlightTypeArrival,

		Scratchpad:          arr.Scratchpad,
		SecondaryScratchpad: arr.SecondaryScratchpad,
		RequestedAltitude:   ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     av.DB.AircraftPerformance[ac.FlightPlan.AircraftType].Category.CWT,
	}

	// VFRs don't go around since they aren't talking to us.
	goAround := rand.Float32() < s.State.LaunchConfig.GoAroundRate && ac.FlightPlan.Rules == av.FlightRulesIFR
	// If it's only controlled by virtual controllers, then don't let it go
	// around.  Note that this test misses the case where a human has
	// control from the start, though that shouldn't be happening...
	goAround = goAround && slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff })
	if goAround {
		d := 0.1 + .6*rand.Float32()
		ac.GoAroundDistance = &d
	}

	// Arrivals start out already-associated and with an enroute code.
	sq, err := s.ERAMComputer.CreateSquawk()
	if err != nil {
		return nil, err
	}
	ac.Squawk = sq
	starsFp.AssignedSquawk = sq
	ac.AssociateFlightPlan(&starsFp)

	return ac, err
}

func (s *Sim) sampleAircraft(al av.AirlineSpecifier, lg *log.Logger) (*Aircraft, string) {
	actype, callsign := al.SampleAcTypeAndCallsign(func(callsign string) bool {
		_, ok := s.Aircraft[av.ADSBCallsign(callsign)]
		return !ok
	}, lg)

	if actype == "" {
		return nil, ""
	}

	return &Aircraft{
		Aircraft: av.Aircraft{
			ADSBCallsign: av.ADSBCallsign(callsign),
			Mode:         av.Altitude,
		},
	}, actype
}

func (s *Sim) CreateIFRDeparture(departureAirport, runway, category string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createIFRDepartureNoLock(departureAirport, runway, category)
}

// Note that this may fail without an error if it's having trouble finding a route.
func (s *Sim) CreateVFRDeparture(departureAirport string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for range 50 {
		// Sample destination airport: may be where we started from.
		arrive, ok := rand.SampleWeightedSeq(maps.Keys(s.State.DepartureAirports),
			func(ap string) int { return s.State.DepartureAirports[ap].VFRRateSum() })
		if !ok {
			return nil, nil
		}
		if ap, ok := s.State.DepartureAirports[departureAirport]; !ok || ap.VFRRateSum() == 0 {
			// This shouldn't happen...
			return nil, nil
		} else {
			ac, _, err := s.createUncontrolledVFRDeparture(departureAirport, arrive, ap.VFR.Randoms.Fleet, nil)
			return ac, err
		}
	}
	return nil, nil
}

func (s *Sim) createIFRDepartureNoLock(departureAirport, runway, category string) (*Aircraft, error) {
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
	ac, acType := s.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, departureAirport, dep.Destination)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, runway, *exitRoute, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.State /* wind */, s.State.SimTime, s.lg)
	if err != nil {
		return nil, err
	}

	shortExit, _, _ := strings.Cut(dep.Exit, ".") // chop any excess
	starsFP := STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: util.Select(len(ac.FlightPlan.DepartureAirport) == 4, ac.FlightPlan.DepartureAirport[1:], ac.FlightPlan.DepartureAirport),
		ExitFix:  shortExit,
		ETAOrPTD: getAircraftTime(s.State.SimTime),

		Rules:        av.FlightRulesIFR,
		TypeOfFlight: av.FlightTypeDeparture,

		Scratchpad: util.Select(dep.Scratchpad != "", dep.Scratchpad,
			s.State.STARSFacilityAdaptation.Scratchpads[dep.Exit]),
		SecondaryScratchpad: dep.SecondaryScratchpad,
		RequestedAltitude:   ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     av.DB.AircraftPerformance[ac.FlightPlan.AircraftType].Category.CWT,
	}

	if ap.DepartureController != "" && ap.DepartureController != s.State.PrimaryController {
		// starting out with a virtual controller
		starsFP.TrackingController = ap.DepartureController
		starsFP.ControllingController = ap.DepartureController
		ac.WaypointHandoffController = exitRoute.HandoffController
	} else {
		// human controller will be first
		ctrl := s.State.PrimaryController
		if len(s.State.MultiControllers) > 0 {
			var err error
			ctrl, err = s.State.MultiControllers.GetDepartureController(departureAirport, runway, exitRoute.SID)
			if err != nil {
				s.lg.Error("unable to get departure controller", slog.Any("error", err),
					slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
			}
		}
		if ctrl == "" {
			ctrl = s.State.PrimaryController
		}

		ac.DepartureContactAltitude =
			ac.Nav.FlightState.DepartureAirportElevation + 500 + float32(rand.Intn(500))
		ac.DepartureContactAltitude = math.Min(ac.DepartureContactAltitude, float32(ac.FlightPlan.Altitude))
		ac.DepartureContactController = ctrl
		starsFP.TrackingController = ctrl
	}

	ac.HoldForRelease = ap.HoldForRelease && ac.FlightPlan.Rules == av.FlightRulesIFR // VFRs aren't held

	sq, err := s.ERAMComputer.CreateSquawk()
	if err != nil {
		return nil, err
	}
	ac.Squawk = sq
	starsFP.AssignedSquawk = sq

	// Departures aren't immediately associated, but the STARSComputer will
	// hold on to their flight plans for now.
	_, err = s.STARSComputer.CreateFlightPlan(starsFP)

	return ac, err
}

func (s *Sim) CreateOverflight(group string) (*Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createOverflightNoLock(group)
}

func (s *Sim) createOverflightNoLock(group string) (*Aircraft, error) {
	overflights := s.State.InboundFlows[group].Overflights
	// Randomly sample an overflight
	of := rand.SampleSlice(overflights)

	airline := rand.SampleSlice(of.Airlines)
	ac, acType := s.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.InitializeFlightPlan(av.FlightRulesIFR, acType, airline.DepartureAirport, airline.ArrivalAirport)

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
	ac.WaypointHandoffController = controller

	if err := ac.InitializeOverflight(&of, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.State /* wind */, s.State.SimTime, s.lg); err != nil {
		return nil, err
	}

	starsFp := STARSFlightPlan{
		ACID:     ACID(ac.ADSBCallsign),
		EntryFix: "", // TODO
		ExitFix:  "", // TODO
		ETAOrPTD: getAircraftTime(s.State.SimTime),

		TrackingController:    of.InitialController,
		ControllingController: of.InitialController,

		Rules:               av.FlightRulesIFR,
		TypeOfFlight:        av.FlightTypeOverflight,
		Scratchpad:          of.Scratchpad,
		SecondaryScratchpad: of.SecondaryScratchpad,

		RequestedAltitude: ac.FlightPlan.Altitude,

		AircraftCount:   1,
		AircraftType:    ac.FlightPlan.AircraftType,
		EquipmentSuffix: "G",
		CWTCategory:     av.DB.AircraftPerformance[ac.FlightPlan.AircraftType].Category.CWT,
	}

	// Like departures, these are already associated
	sq, err := s.ERAMComputer.CreateSquawk()
	if err != nil {
		return nil, err
	}
	ac.Squawk = sq
	starsFp.AssignedSquawk = sq
	ac.AssociateFlightPlan(&starsFp)

	return ac, nil
}

func makeDepartureAircraft(ac *Aircraft, now time.Time, wind av.WindModel) DepartureAircraft {
	d := DepartureAircraft{
		ADSBCallsign: ac.ADSBCallsign,
		SpawnTime:    now,
		DepartGate:   now.Add(time.Duration(5+rand.Intn(5)) * time.Minute),
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

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive, fleet string, routeWps []av.Waypoint) (*Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy := s.State.VFRRunways[depart]

	ac, acType := s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, s.lg)
	if ac == nil {
		return nil, "", fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.FlightRulesVFR
	ac.Squawk = 0o1200
	if r := rand.Float32(); r < .02 {
		ac.Mode = av.On // mode-A
	} else if r < .03 {
		ac.Mode = av.Standby // flat out off
	}
	ac.InitializeFlightPlan(rules, acType, depart, arrive)

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
	opp := math.Offset2LL(rwy.Threshold, rwy.Heading, 1 /* nm */, s.State.NmPerLongitude,
		s.State.MagneticVariation)
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wp0.FlyOver = true
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg float32
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp, routeWps[0].Location, s.State.NmPerLongitude, s.State.MagneticVariation)
	} else {
		hdg = math.Heading2LL(opp, mid, s.State.NmPerLongitude, s.State.MagneticVariation)
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
		ac.ADSBCallsign, depart, arrive)
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

func getAircraftTime(now time.Time) time.Time {
	// Hallucinate a random time around the present for the aircraft.
	delta := time.Duration(-20 + rand.Intn(40))
	t := now.Add(delta * time.Minute)

	// 9 times out of 10, make it a multiple of 5 minutes
	if rand.Intn(10) != 9 {
		dm := t.Minute() % 5
		t = t.Add(time.Duration(5-dm) * time.Minute)
	}

	return t
}
