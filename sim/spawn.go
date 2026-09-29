// sim/spawn.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/traffic"

	"github.com/goforj/godump"
)

const initialSimSeconds = 30 * 60

const initialSimControlledSeconds = 60

// PrespawnDuration is how far the clock rewinds before the selected start time
// to warm up the sim; historical flight data must cover it so that prespawn
// has traffic to fly.
const PrespawnDuration = initialSimSeconds * time.Second

func (s *Sim) Prespawn() {
	start := time.Now()
	s.lg.Info("starting aircraft prespawn")

	s.initDepartureState(s.State.SimTime)
	s.generateSchedule(s.publishedFlights(&s.State.LaunchConfig))

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

	s.lg.Info("finished aircraft prespawn")
	fmt.Printf("Prespawn in %s, rates: dep %f arrival %f overflight %f\n", time.Since(start),
		s.State.LaunchConfig.TotalDepartureRate(), s.State.LaunchConfig.TotalArrivalRate(),
		s.State.LaunchConfig.TotalOverflightRate())
	fmt.Println("LaunchConfig:")
	godump.Dump(s.State.LaunchConfig)
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
	s.updateDepartureQueues()
}

func (s *Sim) addAircraft(ac Aircraft) {
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
			if cid, err := s.CIDAllocator.Allocate(s.Rand); err == nil {
				fp.CID = cid
			} else {
				s.lg.Warn("no CID available", slog.String("callsign", string(ac.ADSBCallsign)))
			}
		}
	}

	s.Aircraft[ac.ADSBCallsign] = &ac
	s.logSpawn(&ac)

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

// errCallsignInUse means another aircraft is already flying a published flight's
// callsign: the inbound leg of a turnaround that hasn't landed yet, most often.
// A published callsign is the real one and can't be resampled, so the flight is
// discarded rather than flown under a different one.
var errCallsignInUse = errors.New("callsign is already in use")

// newScheduledAircraft creates the aircraft a schedule entry flies, whatever
// the kind of flight and wherever its traffic comes from: the callsign
// resolveScheduledCallsign settles on and a flight plan between the entry's
// airports. The caller initializes the rest as its kind of flight requires.
func (s *Sim) newScheduledAircraft(f *ScheduledFlight, kind string) (*Aircraft, error) {
	callsign, err := s.resolveScheduledCallsign(f, kind)
	if err != nil {
		return nil, err
	}
	ac := &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}
	ac.InitializeFlightPlan(av.FlightRulesIFR, f.AircraftType,
		traffic.NormalizeAirportCode(f.DepartureAirport), traffic.NormalizeAirportCode(f.ArrivalAirport))
	return ac, nil
}

// resolveScheduledCallsign checks a schedule entry's callsign against what the
// sim is currently flying when the flight is finally created. A scenario
// entry whose randomly generated callsign has since been taken draws a new one
// from its airline; a published flight's callsign is the real one and can't be
// resampled, so the clash is an error.
func (s *Sim) resolveScheduledCallsign(f *ScheduledFlight, kind string) (string, error) {
	callsign := strings.ToUpper(strings.TrimSpace(f.Callsign))
	if callsign == "" {
		return "", fmt.Errorf("%s callsign is empty", kind)
	}
	if !av.CallsignClashesWithExisting(s.currentCallsigns(), callsign, s.EnforceUniqueCallsignSuffix) {
		return callsign, nil
	}
	if f.Source != TrafficSourceScenario || f.Airline.Callsign != "" {
		return "", fmt.Errorf("%s %s: %w", kind, callsign, errCallsignInUse)
	}
	_, callsign = f.Airline.SampleAcTypeAndCallsign(db.Lookups{}, s.Rand, s.currentCallsigns(),
		s.EnforceUniqueCallsignSuffix, f.DepartureAirport, f.ArrivalAirport, s.lg)
	if callsign == "" {
		return "", fmt.Errorf("%s %s: %w", kind, f.Callsign, errCallsignInUse)
	}
	return callsign, nil
}

func (s *Sim) currentCallsigns() []av.ADSBCallsign {
	callsigns := slices.Collect(maps.Keys(s.Aircraft))
	for _, fp := range s.STARSComputer.FlightPlans {
		callsigns = append(callsigns, av.ADSBCallsign(fp.ACID))
	}
	// The manual launch slots' pending flights hold their callsigns too:
	// slots are looked up by callsign, so no two may share one.
	for _, e := range s.PendingDepartures {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, e := range s.PendingArrivals {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, e := range s.PendingOverflights {
		callsigns = append(callsigns, av.ADSBCallsign(e.Callsign))
	}
	for _, ac := range s.PendingVFR {
		callsigns = append(callsigns, ac.ADSBCallsign)
	}
	return callsigns
}

// sampleAircraft draws an aircraft type and an unused callsign. callsigns is
// what is already in use or soon to be; callers that sample repeatedly gather
// it once rather than walking the sim for each draw.
func (s *Sim) sampleAircraft(al av.AirlineSpecifier, departureAirport, arrivalAirport av.ICAOAirportCode,
	callsigns []av.ADSBCallsign, lg *log.Logger) (*Aircraft, string) {
	actype, callsign := al.SampleAcTypeAndCallsign(db.Lookups{}, s.Rand, callsigns, s.EnforceUniqueCallsignSuffix, departureAirport, arrivalAirport, lg)

	if actype == "" {
		return nil, ""
	}

	return &Aircraft{
		ADSBCallsign: av.ADSBCallsign(callsign),
		Mode:         av.TransponderModeAltitude,
	}, actype
}

// initNASFlightPlan creates a NASFlightPlan with common fields pre-populated.
// Callers must set type-specific fields (EntryFix, ExitFix, controller
// assignments, scratchpads, altitudes, etc.) after calling this function.
func (s *Sim) initNASFlightPlan(ac *Aircraft, flightType av.TypeOfFlight) NASFlightPlan {
	return NASFlightPlan{
		ACID:             ACID(ac.ADSBCallsign),
		ArrivalAirport:   ac.FlightPlan.ArrivalAirport,
		CoordinationTime: getAircraftTime(s.State.SimTime, s.Rand),
		PlanType:         RemoteEnroute,
		Rules:            av.FlightRulesIFR,
		TypeOfFlight:     flightType,
		AircraftCount:    1,
		AircraftType:     ac.FlightPlan.AircraftType,
		CWTCategory:      db.DB.AircraftPerformance[ac.FlightPlan.AircraftType].Category.CWT,
	}
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

// maxSpawnWait is the wait when the rate is zero. It also bounds the waits
// for tiny rates, which would otherwise overflow a time.Duration; on amd64
// the overflow comes out negative.
const maxSpawnWait = 365 * 24 * time.Hour

func randomWait(rate float32, pushActive bool, r *rand.Rand) time.Duration {
	if rate == 0 {
		return maxSpawnWait
	}
	if pushActive {
		rate = rate * 3 / 2
	}

	avgSeconds := 3600 / rate
	return spawnWait(r.Float32Range(.85*avgSeconds, 1.15*avgSeconds))
}

// Wait from 0 up to the rate.
func randomInitialWait(rate float32, r *rand.Rand) time.Duration {
	if rate == 0 {
		return maxSpawnWait
	}

	return spawnWait(r.Float32Range(0, 3600/rate))
}

func spawnWait(seconds float32) time.Duration {
	if seconds >= float32(maxSpawnWait/time.Second) {
		return maxSpawnWait
	}
	return time.Duration(seconds * float32(time.Second))
}

type DepartureRunway struct {
	Airport     av.ICAOAirportCode `json:"airport"`
	Runway      av.RunwayID        `json:"runway"`
	Category    string             `json:"category,omitempty"`
	DefaultRate float32            `json:"rate"`
}

type ArrivalRunway struct {
	Airport  av.ICAOAirportCode `json:"airport"`
	Runway   av.RunwayID        `json:"runway"`
	GoAround *GoAroundProcedure `json:"go_around,omitempty"`
}
