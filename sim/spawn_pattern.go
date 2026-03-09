// sim/spawn_pattern.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
)

// PatternPhase identifies which leg of the traffic pattern an aircraft is on.
type PatternPhase int

const (
	PatternUpwind PatternPhase = iota
	PatternCrosswind
	PatternDownwind
	PatternBase
	PatternFinal
	PatternRollout // on runway after touchdown
)

// PatternAircraft tracks a single aircraft doing touch-and-gos.
type PatternAircraft struct {
	ADSBCallsign av.ADSBCallsign
	Phase        PatternPhase
}

// PatternState tracks pattern aircraft at a single airport.
type PatternState struct {
	Aircraft  []PatternAircraft // max 2
	NextSpawn time.Time
}

// patternSpawnRate is the nominal rate (aircraft per hour) for pattern spawns.
const patternSpawnRate float32 = 5

// bestRunwayForWind returns the runway id best aligned with the current
// wind at the given airport. It returns "" if the airport is unknown or
// has no runways.
func (s *Sim) bestRunwayForWind(airport string) string {
	if rwy, _, ok := s.currentVFRRunway(airport); ok {
		return rwy.Id
	}
	return ""
}

// currentVFRRunway returns the best runway for VFR operations at the given
// airport based on current wind conditions. This ensures pattern aircraft
// and VFR arrivals always agree on runway selection.
func (s *Sim) currentVFRRunway(airport string) (rwy, opp av.Runway, ok bool) {
	faaAP, found := av.DB.Airports[airport]
	if !found {
		return av.Runway{}, av.Runway{}, false
	}

	as := s.wxModel.Lookup(faaAP.Location, float32(faaAP.Elevation), s.State.SimTime)
	r, o := faaAP.SelectBestRunway(as.WindDirection(), s.State.MagneticVariation)
	if r == nil || o == nil {
		return av.Runway{}, av.Runway{}, false
	}
	return *r, *o, true
}

// patternBuilder creates waypoints offset from a runway threshold.
// It replaces the duplicated offset/addpt closures across the generators.
type patternBuilder struct {
	threshold                         math.Point2LL
	depHdg, leftHdg                   float32
	elevation                         int
	nmPerLongitude, magneticVariation float32
}

func newPatternBuilder(rwy av.Runway, elevation int, nmPerLongitude, magneticVariation float32) patternBuilder {
	depHdg := rwy.Heading
	return patternBuilder{
		threshold:         rwy.Threshold,
		depHdg:            depHdg,
		leftHdg:           math.NormalizeHeading(depHdg - 90),
		elevation:         elevation,
		nmPerLongitude:    nmPerLongitude,
		magneticVariation: magneticVariation,
	}
}

func (b patternBuilder) waypoint(name string, along, lateral, deltaAlt float32, speed int16, phase uint8) av.Waypoint {
	p := math.Offset2LL(b.threshold, b.depHdg, along, b.nmPerLongitude, b.magneticVariation)
	if lateral != 0 {
		p = math.Offset2LL(p, b.leftHdg, lateral, b.nmPerLongitude, b.magneticVariation)
	}
	alt := float32(b.elevation) + deltaAlt
	wp := av.Waypoint{
		Fix:      name,
		Location: p,
		VFRPhase: phase,
	}
	wp.SetAltitudeRestriction(av.AltitudeRestriction{Range: [2]float32{alt, alt}})
	wp.Speed = speed
	return wp
}

// generatePatternLap generates waypoints for one traffic pattern circuit.
// The returned waypoints start at the rollout point (mid-runway) and end
// at the threshold with a Delete flag.
// Touch-and-go handling is done in the Delete handler in updateState.
//
// All distances are fixed in nautical miles so the pattern has a realistic
// size regardless of runway length. Standard left traffic.
func generatePatternLap(rwy, opp av.Runway, elevation int, nmPerLongitude, magneticVariation float32) []av.Waypoint {
	b := newPatternBuilder(rwy, elevation, nmPerLongitude, magneticVariation)
	rwyLen := math.NMDistance2LL(rwy.Threshold, opp.Threshold)

	// Fixed pattern dimensions (nm).
	pdist := float32(0.75)    // lateral offset from runway centerline
	upwindExt := float32(0.5) // extension past departure end of runway
	baseExt := float32(0.5)   // how far past threshold the base turn starts

	wps := make([]av.Waypoint, 0, 8)

	wps = append(wps, b.waypoint("_pat_rollout", rwyLen/2, 0, 0, 70, av.VFRPhaseRollout))
	wps = append(wps, b.waypoint("_pat_upwind", rwyLen+upwindExt, 0, 200, 70, av.VFRPhaseUpwind))
	wps = append(wps, b.waypoint("_pat_crosswind", rwyLen+upwindExt, pdist, 500, 70, av.VFRPhaseCrosswind))
	wps = append(wps, b.waypoint("_pat_downwind", 0, pdist, 1000, 70, av.VFRPhaseDownwind))
	wps = append(wps, b.waypoint("_pat_late_dw", -baseExt, pdist, 1000, 70, av.VFRPhaseDownwind))
	wps = append(wps, b.waypoint("_pat_base", -baseExt-0.5, pdist/2, 500, 65, av.VFRPhaseBase))
	wps = append(wps, b.waypoint("_pat_final", -1, 0, 200, 60, av.VFRPhaseFinal))

	// Threshold (touchdown). Touch-and-go vs full-stop is handled in
	// the Delete handler by checking TouchAndGosRemaining.
	threshold := b.waypoint("_pat_threshold", 0, 0, 0, 60, av.VFRPhaseFinal)
	threshold.SetDelete(true)
	wps = append(wps, threshold)

	return wps
}

// spawnPatternAircraft attempts to spawn pattern traffic at airports with VFR activity.
func (s *Sim) spawnPatternAircraft() {
	now := s.State.SimTime

	for name, ps := range s.PatternState {
		if len(ps.Aircraft) >= 2 {
			continue
		}
		if now.Before(ps.NextSpawn) {
			continue
		}

		ap, ok := s.State.Airports[name]
		if !ok || ap.VFRRateSum() == 0 {
			continue
		}

		faaAP, ok := av.DB.Airports[name]
		if !ok {
			continue
		}

		// Use current wind for runway selection.
		rwy, opp, ok := s.currentVFRRunway(name)
		if !ok {
			continue
		}

		if !s.canLaunchPattern(name, rwy) {
			continue
		}

		// Sample a non-jet GA aircraft
		var ac *Aircraft
		var acType string
		for range 20 {
			ac, acType = s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: ap.VFR.Randoms.Fleet}, name, name, s.lg)
			if ac == nil {
				continue
			}
			perf, ok := av.DB.AircraftPerformance[acType]
			if !ok {
				ac = nil
				continue
			}
			if perf.Engine.AircraftType == "J" {
				ac = nil
				continue
			}
			break
		}
		if ac == nil {
			ps.NextSpawn = now.Add(randomWait(patternSpawnRate, false, s.Rand))
			continue
		}

		ac.Squawk = 0o1200
		ac.InitializeFlightPlan(av.FlightRulesVFR, acType, name, name)
		ac.FlightPlan.Altitude = faaAP.Elevation + 1000 // pattern altitude (TPA)

		touchAndGos := 2 + s.Rand.Intn(4)         // 2-5 total laps
		ac.TouchAndGosRemaining = touchAndGos - 1 // first lap is in progress, remaining are after

		wps := generatePatternLap(rwy, opp, faaAP.Elevation, s.State.NmPerLongitude, s.State.MagneticVariation)

		err := ac.InitializeVFRDeparture(ap, wps, false, s.State.NmPerLongitude,
			s.State.MagneticVariation, s.wxModel, now, s.lg)
		if err != nil {
			s.lg.Warn("failed to initialize pattern aircraft", slog.Any("error", err))
			ps.NextSpawn = now.Add(randomWait(patternSpawnRate, false, s.Rand))
			continue
		}

		s.addAircraftNoLock(*ac)

		// Record as a departure for sequencing
		depac := makeDepartureAircraft(ac, now, s.wxModel, s.Rand)
		depac.LaunchTime = now
		for rwyID, depState := range s.DepartureState[name] {
			if rwyID.Base() == rwy.Id {
				depState.LastDeparture = &depac
			}
		}

		ps.Aircraft = append(ps.Aircraft, PatternAircraft{
			ADSBCallsign: ac.ADSBCallsign,
			Phase:        PatternUpwind,
		})

		ps.NextSpawn = now.Add(randomWait(patternSpawnRate, false, s.Rand))

		s.lg.Info("spawned pattern aircraft",
			slog.String("callsign", string(ac.ADSBCallsign)),
			slog.String("airport", name),
			slog.Int("touch_and_gos", touchAndGos))
	}
}

// canLaunchPattern checks whether it's safe to put a pattern aircraft on the
// runway at the given airport.
func (s *Sim) canLaunchPattern(airport string, rwy av.Runway) bool {
	// Check recent departures on same runway
	if depState, ok := s.DepartureState[airport]; ok {
		for rwyID, state := range depState {
			if rwyID.Base() == rwy.Id {
				if state.LastDeparture != nil {
					elapsed := s.State.SimTime.Sub(state.LastDeparture.LaunchTime)
					if elapsed < 90*time.Second {
						return false
					}
				}
			}
		}
	}

	// Check for arrivals on short final (< 2nm from end of approach)
	for _, ac := range s.Aircraft {
		if ac.Nav.Approach.Assigned != nil && ac.Nav.Approach.Assigned.Runway == rwy.Id {
			if dist, err := ac.Nav.DistanceToEndOfApproach(); err == nil && dist < 2.0 {
				return false
			}
		}
	}

	// Check for other pattern aircraft on upwind or rollout
	if ps, ok := s.PatternState[airport]; ok {
		for _, pa := range ps.Aircraft {
			if pa.Phase == PatternUpwind || pa.Phase == PatternRollout {
				return false
			}
		}
	}

	return true
}

// updatePatternPhases updates the phase of each pattern aircraft based on
// which waypoint they are heading toward.
func (s *Sim) updatePatternPhases() {
	for _, ps := range s.PatternState {
		for i := range ps.Aircraft {
			pa := &ps.Aircraft[i]
			ac, ok := s.Aircraft[pa.ADSBCallsign]
			if !ok {
				continue
			}

			if len(ac.Nav.Waypoints) == 0 {
				continue
			}

			switch ac.Nav.Waypoints[0].VFRPhase {
			case av.VFRPhaseRollout:
				pa.Phase = PatternRollout
			case av.VFRPhaseUpwind:
				pa.Phase = PatternUpwind
			case av.VFRPhaseCrosswind:
				pa.Phase = PatternCrosswind
			case av.VFRPhaseDownwind:
				pa.Phase = PatternDownwind
			case av.VFRPhaseBase:
				pa.Phase = PatternBase
			case av.VFRPhaseFinal:
				pa.Phase = PatternFinal
			}
		}
	}
}

// patternConflictsWithLaunch returns true if any pattern aircraft at the
// given airport is in a phase that should block departures.
func (s *Sim) patternConflictsWithLaunch(airport string) bool {
	ps, ok := s.PatternState[airport]
	if !ok {
		return false
	}

	for _, pa := range ps.Aircraft {
		switch pa.Phase {
		case PatternFinal, PatternBase, PatternRollout:
			return true
		case PatternUpwind:
			// Block if the aircraft is still close to the threshold
			if ac, ok := s.Aircraft[pa.ADSBCallsign]; ok {
				rwy, _, rok := s.currentVFRRunway(airport)
				if rok && math.NMDistance2LL(ac.Position(), rwy.Threshold) < 1 {
					return true
				}
			}
		}
	}

	return false
}

// resetPatternLap replaces the aircraft's waypoints with a new pattern lap.
// Called after a touch-and-go when TouchAndGosRemaining > 0.
func (s *Sim) resetPatternLap(ac *Aircraft) {
	airport := ac.FlightPlan.DepartureAirport

	faaAP, ok := av.DB.Airports[airport]
	if !ok {
		s.lg.Warn("no FAA airport for pattern reset", slog.String("airport", airport))
		return
	}

	// Use current wind; the runway may have changed since the last lap.
	rwy, opp, ok := s.currentVFRRunway(airport)
	if !ok {
		s.lg.Warn("no runway for pattern reset", slog.String("airport", airport))
		return
	}

	wps := generatePatternLap(rwy, opp, faaAP.Elevation, s.State.NmPerLongitude, s.State.MagneticVariation)
	ac.Nav.Waypoints = wps

	// Clear nav heading state so the aircraft follows waypoints rather
	// than flying a previously-assigned heading. The nav sets
	// Heading.Assigned when passing the last waypoint, which happens when
	// _pat_threshold is the only waypoint remaining (after touch-and-gos).
	ac.Nav.Heading = nav.NavHeading{}

	// Re-enter initial departure climb so speed/altitude management
	// handles the takeoff roll correctly.
	ac.Nav.FlightState.InitialDepartureClimb = true

	// Reset FirstSeen so that when the aircraft climbs back out of the
	// surface tracking filter, it goes through the tentative track stage
	// again (just a position symbol for 1-2 sweeps before the full track).
	ac.FirstSeen = time.Time{}
}

// recordPatternTouchAndGo records a touch-and-go for departure sequencing.
func (s *Sim) recordPatternTouchAndGo(ac *Aircraft, airport string, rwyId string) {
	if depState, ok := s.DepartureState[airport]; ok {
		for rwyID, state := range depState {
			if rwyID.Base() == rwyId {
				state.LastArrivalLandingTime = s.State.SimTime
				state.LastArrivalFlightRules = av.FlightRulesVFR

				depac := DepartureAircraft{
					ADSBCallsign: ac.ADSBCallsign,
					LaunchTime:   s.State.SimTime,
				}
				state.LastDeparture = &depac
			}
		}
	}
}

// sequenceVFRLanding is called when a VFR arrival passes its
// SequenceVFRLanding waypoint. It evaluates current wind, traffic, and
// approach angle to decide: straight-in, 45-to-downwind, or orbit.
func (s *Sim) sequenceVFRLanding(ac *Aircraft) {
	airport := ac.FlightPlan.ArrivalAirport
	rwy, opp, ok := s.currentVFRRunway(airport)
	if !ok {
		s.lg.Warn("sequenceVFRLanding: no runway", slog.String("airport", airport))
		return
	}
	faaAP, ok := av.DB.Airports[airport]
	if !ok {
		return
	}

	if s.patternClearForEntry(airport) {
		hdgToRwy := math.Heading2LL(ac.Position(), rwy.Threshold,
			s.State.NmPerLongitude, s.State.MagneticVariation)
		if math.HeadingDifference(hdgToRwy, rwy.Heading) <= 60 && s.finalClear(airport) {
			wps := generateStraightInWaypoints(rwy, faaAP.Elevation,
				s.State.NmPerLongitude, s.State.MagneticVariation)
			ac.Nav.Waypoints = wps
			ac.Nav.Heading = nav.NavHeading{}
			s.lg.Info("VFR arrival straight-in",
				slog.String("callsign", string(ac.ADSBCallsign)),
				slog.String("airport", airport))
		} else {
			wps := generatePatternEntryWaypoints(rwy, opp, faaAP.Elevation,
				s.State.NmPerLongitude, s.State.MagneticVariation)
			ac.Nav.Waypoints = wps
			ac.Nav.Heading = nav.NavHeading{}
			s.lg.Info("VFR arrival 45-to-downwind",
				slog.String("callsign", string(ac.ADSBCallsign)),
				slog.String("airport", airport))
		}
	} else {
		wps := s.generateOrbitWaypoints(airport)
		if len(wps) == 0 {
			return
		}
		ac.Nav.Waypoints = wps
		ac.Nav.Heading = nav.NavHeading{}
		s.lg.Info("VFR arrival entering orbit",
			slog.String("callsign", string(ac.ADSBCallsign)),
			slog.String("airport", airport))
	}
}

// patternClearForEntry returns true if no pattern aircraft is on downwind
// and no other VFR arrival is already on a pattern-entry path.
// Traffic on base or final is acceptable — the arrival joins at the start
// of downwind and those aircraft will have cleared by the time it gets there.
func (s *Sim) patternClearForEntry(airport string) bool {
	ps, ok := s.PatternState[airport]
	if !ok {
		return true
	}
	for _, pa := range ps.Aircraft {
		if pa.Phase == PatternDownwind {
			return false
		}
	}

	// Block if another VFR arrival is already on a pattern-entry path
	// to this airport.
	for _, ac := range s.Aircraft {
		if ac.FlightPlan.ArrivalAirport == airport && ac.TouchAndGosRemaining == 0 &&
			len(ac.Nav.Waypoints) > 0 &&
			ac.Nav.Waypoints[0].VFRPhase != av.VFRPhaseNone &&
			ac.Nav.Waypoints[0].VFRPhase != av.VFRPhaseOrbit {
			return false
		}
	}

	return true
}

// finalClear returns true if no pattern aircraft is on base or final
// and no VFR arrival is on a straight-in approach to this airport.
func (s *Sim) finalClear(airport string) bool {
	if ps, ok := s.PatternState[airport]; ok {
		for _, pa := range ps.Aircraft {
			if pa.Phase == PatternBase || pa.Phase == PatternFinal {
				return false
			}
		}
	}

	// Block if another VFR arrival is already on a straight-in.
	for _, ac := range s.Aircraft {
		if ac.FlightPlan.ArrivalAirport == airport && ac.TouchAndGosRemaining == 0 &&
			len(ac.Nav.Waypoints) > 0 && ac.Nav.Waypoints[0].VFRPhase == av.VFRPhaseStraightIn {
			return false
		}
	}

	return true
}

// generateOrbitWaypoints returns 4 waypoints forming a ~1nm-radius left
// circle centered ~3nm to the right of the runway (opposite the pattern
// side) near TPA. The center and altitude are randomized slightly so
// multiple arrivals don't stack on top of each other.
func (s *Sim) generateOrbitWaypoints(airport string) []av.Waypoint {
	rwy, _, ok := s.currentVFRRunway(airport)
	if !ok {
		return nil
	}
	faaAP, ok := av.DB.Airports[airport]
	if !ok {
		return nil
	}

	// Randomize altitude ±200ft around TPA.
	tpa := float32(faaAP.Elevation+1000) + float32(s.Rand.Intn(401)-200)

	depHdg := rwy.Heading
	// Right of runway (opposite the left-traffic pattern side).
	rightHdg := math.NormalizeHeading(depHdg + 90)

	// Randomize the center: 2.5–3.5nm right of the runway, shifted
	// ±0.5nm along the runway heading.
	lateralDist := 2.5 + s.Rand.Float32()
	alongDist := s.Rand.Float32() - 0.5
	center := math.Offset2LL(rwy.Threshold, rightHdg, lateralDist, s.State.NmPerLongitude, s.State.MagneticVariation)
	center = math.Offset2LL(center, depHdg, alongDist, s.State.NmPerLongitude, s.State.MagneticVariation)

	radius := float32(1) // nm
	wps := make([]av.Waypoint, 4)
	for i, hdg := range []float32{0, 90, 180, 270} {
		p := math.Offset2LL(center, hdg, radius, s.State.NmPerLongitude, s.State.MagneticVariation)
		wp := av.Waypoint{
			Fix:      fmt.Sprintf("_orbit_%d", i+1),
			Location: p,
			VFRPhase: av.VFRPhaseOrbit,
		}
		wp.SetAltitudeRestriction(av.AltitudeRestriction{Range: [2]float32{tpa, tpa}})
		wp.Speed = 70
		wps[i] = wp
	}
	// When the aircraft completes the orbit and passes the last waypoint,
	// the SequenceVFRLanding handler fires again to re-evaluate.
	wps[3].SetSequenceVFRLanding(true)
	return wps
}

// generatePatternEntryWaypoints returns waypoints for a standard 45-degree
// entry to downwind, using the same fixed-nm offsets as generatePatternLap.
// The last waypoint has the Delete flag set.
func generatePatternEntryWaypoints(rwy, opp av.Runway, elevation int,
	nmPerLongitude, magneticVariation float32) []av.Waypoint {
	b := newPatternBuilder(rwy, elevation, nmPerLongitude, magneticVariation)
	rwyLen := math.NMDistance2LL(rwy.Threshold, opp.Threshold)

	pdist := float32(0.75)
	baseExt := float32(0.5)

	wps := make([]av.Waypoint, 0, 6)

	wps = append(wps, b.waypoint("_pat_enter45", rwyLen/2, pdist+0.5, 1200, 70, av.VFRPhaseDownwind))
	wps = append(wps, b.waypoint("_pat_downwind", 0, pdist, 1000, 70, av.VFRPhaseDownwind))
	wps = append(wps, b.waypoint("_pat_late_dw", -baseExt, pdist, 1000, 70, av.VFRPhaseDownwind))
	wps = append(wps, b.waypoint("_pat_base", -baseExt-0.5, pdist/2, 500, 65, av.VFRPhaseBase))
	wps = append(wps, b.waypoint("_pat_final", -1, 0, 200, 60, av.VFRPhaseFinal))

	threshold := b.waypoint("_pat_threshold", 0, 0, 0, 60, av.VFRPhaseFinal)
	threshold.SetDelete(true)
	wps = append(wps, threshold)

	return wps
}

// generateStraightInWaypoints returns waypoints for a straight-in approach:
// a lineup point 2nm out at 300' AGL, the threshold, and a runway end point.
// The last waypoint has the Delete flag set.
func generateStraightInWaypoints(rwy av.Runway, elevation int,
	nmPerLongitude, magneticVariation float32) []av.Waypoint {
	b := newPatternBuilder(rwy, elevation, nmPerLongitude, magneticVariation)

	wps := make([]av.Waypoint, 0, 3)
	wps = append(wps, b.waypoint("_pat_lineup", -2, 0, 300, 70, av.VFRPhaseStraightIn))
	wps = append(wps, b.waypoint("_pat_threshold", 0, 0, 0, 60, av.VFRPhaseStraightIn))

	end := b.waypoint("_pat_end", 1, 0, 0, 60, av.VFRPhaseStraightIn)
	end.SetDelete(true)
	wps = append(wps, end)

	return wps
}
