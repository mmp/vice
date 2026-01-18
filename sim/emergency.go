// sim/emergency.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// Airlines that may use "pan-pan" when declaring an emergency--non-US airlines that operate to/from the United States
var panPanAirlines = []string{
	// North America
	"ACA", // Air Canada
	// Europe
	"AFR", // Air France
	"AUA", // Austrian Airlines
	"AZA", // Alitalia (legacy)
	"BAW", // British Airways
	"DLH", // Lufthansa
	"EIN", // Aer Lingus
	"IBE", // Iberia
	"ICE", // Icelandair
	"ITY", // ITA Airways
	"KLM", // KLM Royal Dutch Airlines
	"LOT", // LOT Polish Airlines
	"SAS", // Scandinavian Airlines
	"SWR", // Swiss International Air Lines
	"TAP", // TAP Air Portugal
	"THY", // Turkish Airlines
	"VIR", // Virgin Atlantic
	// Asia-Pacific
	"ANA", // All Nippon Airways
	"ANZ", // Air New Zealand
	"CAL", // China Airlines
	"CPA", // Cathay Pacific
	"CSN", // China Southern Airlines
	"EVA", // EVA Air
	"FJI", // Fiji Airways
	"JAL", // Japan Airlines
	"KAL", // Korean Air
	"PAL", // Philippine Airlines
	"QFA", // Qantas
	"SIA", // Singapore Airlines
	"THA", // Thai Airways
	// Middle East
	"ELY", // El Al
	"ETD", // Etihad Airways
	"QTR", // Qatar Airways
	"SVA", // Saudia
	"UAE", // Emirates
	// Latin America
	"AMX", // AeroMexico
	"AVA", // Avianca
	"CMP", // Copa Airlines
	"LAN", // LATAM Airlines
	"TAM", // LATAM Brasil
	// Africa
	"ETH", // Ethiopian Airlines
	"RAM", // Royal Air Maroc
}

// Average passenger and fuel capacity by CWT category
// Based on FAA JO 7110.126A Table A-1 and typical aircraft in each category
var cwtAveragePassengers = map[string]int{
	"A": 650, // Super (A388 only)
	"B": 450, // Upper Heavy (widebody jets: 777, 787, A330, A340, A350)
	"C": 320, // Lower Heavy (767, A310, DC10, MD11)
	"D": 200, // Non-Pairwise Heavy (military/special, use lower heavy estimate)
	"E": 240, // B757 category
	"F": 180, // Upper Large (737, A320 family, E190)
	"G": 70,  // Lower Large (regional jets: CRJ, E170/175, turboprops)
	"H": 8,   // Upper Small (bizjets: Citations, Learjets, Gulfstream)
	"I": 6,   // Lower Small (small props and very light jets)
}

var cwtFuelPounds = map[string]int{
	"A": 560000, // Super (A388)
	"B": 300000, // Upper Heavy (widebody jets)
	"C": 230000, // Lower Heavy (767, DC10, MD11, A310)
	"D": 200000, // Non-Pairwise Heavy (estimate based on military heavies)
	"E": 75000,  // B757 category
	"F": 45000,  // Upper Large (737, A320 family)
	"G": 15000,  // Lower Large (regional jets, turboprops)
	"H": 3000,   // Upper Small (bizjets)
	"I": 500,    // Lower Small (small props)
}

// FutureEmergencyUpdate represents a scheduled emergency progression update.
type FutureEmergencyUpdate struct {
	ADSBCallsign av.ADSBCallsign
	Time         time.Time
}

type EmergencyApplicability int

const (
	EmergencyApplicabilityDeparture EmergencyApplicability = 1 << iota
	EmergencyApplicabilityArrival
	EmergencyApplicabilityExternal
	EmergencyApplicabilityApproach
)

func (ea EmergencyApplicability) String() string {
	var parts []string
	if ea&EmergencyApplicabilityDeparture != 0 {
		parts = append(parts, "departure")
	}
	if ea&EmergencyApplicabilityArrival != 0 {
		parts = append(parts, "arrival")
	}
	if ea&EmergencyApplicabilityExternal != 0 {
		parts = append(parts, "external")
	}
	if ea&EmergencyApplicabilityApproach != 0 {
		parts = append(parts, "approach")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// Returns weight for this emergency type given the aircraft.
// Returns 0 if not applicable.
func (ea EmergencyApplicability) Applies(ac *Aircraft, humanController bool) bool {
	if ac.IsDeparture() {
		dist := math.NMDistance2LL(ac.Position(), ac.Nav.FlightState.DepartureAirportLocation)
		return humanController && dist <= 15 && ea&EmergencyApplicabilityDeparture != 0
	} else if ac.IsArrival() {
		if humanController {
			if ac.OnApproach(false /* ignore altitudes */) {
				return ea&EmergencyApplicabilityApproach != 0
			} else {
				return ea&EmergencyApplicabilityArrival != 0
			}
		} else {
			return ea&EmergencyApplicabilityExternal != 0
		}
	}
	return false
}

type Emergency struct {
	Name               string `json:"name"`
	ApplicableToString string `json:"applicable_to"`
	ApplicableTo       EmergencyApplicability
	Weight             float32          `json:"weight"`
	Stages             []EmergencyStage `json:"stages"`
}

// EmergencyStage represents a stage in an emergency's progression.
type EmergencyStage struct {
	Transmission        string `json:"transmission"`
	DurationMinutes     [2]int `json:"duration_minutes"`
	RequestReturn       bool   `json:"request_return"`
	StopClimb           bool   `json:"stop_climb"`
	Checklists          bool   `json:"checklists"`
	RequestEquipment    bool   `json:"request_equipment"`
	RequestDelayVectors bool   `json:"request_delay_vectors"`
	DeclareEmergency    bool   `json:"declare_emergency"`
}

// EmergencyState tracks the current state of an aircraft's emergency.
type EmergencyState struct {
	Emergency      *Emergency
	CurrentStage   int
	NextUpdateTime time.Time
}

func (s *Sim) triggerEmergency(idx int) bool {
	if len(s.State.Emergencies) == 0 {
		s.lg.Warn("triggerEmergency: no emergency types loaded")
		return false
	}

	em := &s.State.Emergencies[idx]

	// Sample aircraft with weight 0 for virtual-controlled or existing emergencies
	ac, ok := rand.SampleWeightedSeq(s.Rand, maps.Values(s.Aircraft), func(ac *Aircraft) float32 {
		if ac.EmergencyState != nil {
			return 0
		}

		humanAllocated := false
		if fp := ac.NASFlightPlan; fp != nil {
			humanAllocated = !s.isVirtualController(fp.ControllingController)
		}
		return util.Select(em.ApplicableTo.Applies(ac, humanAllocated), em.Weight, float32(0))
	})
	if !ok {
		// No aircraft available for this emergency; if this was automatically-triggered based on
		// time passage, we'll try again the next tick, selecting a new random emergency.
		return false
	}

	ac.EmergencyState = &EmergencyState{Emergency: em}

	s.lg.Info("emergency initiated", "callsign", string(ac.ADSBCallsign), "type", em.Name)

	// Trigger the first stage immediately unless it's an external arrival that hasn't been handed
	// off yet. Those trigger when controller contact happens in processEnququed.
	if ac.IsAssociated() && ac.DepartureContactAltitude != 0 {
		s.runEmergencyStage(ac)
	} else {
		// Mark as dormant until handoff; -1 signals the emergency should activate
		// when the aircraft passes a HumanHandoff waypoint.
		ac.EmergencyState.CurrentStage = -1
	}
	return true
}

func (s *Sim) updateEmergencies() {
	// Process emergency progression updates
	s.FutureEmergencyUpdates = util.FilterSliceInPlace(
		s.FutureEmergencyUpdates,
		func(feu FutureEmergencyUpdate) bool {
			if !s.State.SimTime.After(feu.Time) {
				return true // don't do anything but keep it
			}

			// Be mindful of aircraft having been being deleted.
			if ac, ok := s.Aircraft[feu.ADSBCallsign]; ok {
				s.runEmergencyStage(ac)
			}
			return false // in either case, remove it remove from the queue
		})

	if s.prespawn || s.NextEmergencyTime.IsZero() || s.State.SimTime.Before(s.NextEmergencyTime) {
		return
	}

	if len(s.State.Emergencies) > 0 {
		// Select a random emergency type
		if s.triggerEmergency(s.Rand.Intn(len(s.State.Emergencies))) {
			// Schedule next emergency if we were successful
			if s.State.LaunchConfig.EmergencyAircraftRate > 0 {
				s.NextEmergencyTime = s.State.SimTime.Add(randomWait(s.State.LaunchConfig.EmergencyAircraftRate, false, s.Rand))
			}
		}
	}
}

func (s *Sim) enqueueEmergencyUpdate(callsign av.ADSBCallsign, t time.Time) {
	s.FutureEmergencyUpdates = append(s.FutureEmergencyUpdates,
		FutureEmergencyUpdate{
			ADSBCallsign: callsign,
			Time:         t,
		})
}

// getSoulsOnBoard returns a realistic number of souls on board for the aircraft
func getSoulsOnBoard(ac *Aircraft, rng *rand.Rand) int {
	// Check if this is a cargo carrier -> no pax, just crew
	cargoCarriers := []string{
		"FDX", // FedEx
		"UPS", // UPS
		"DHL", // DHL
		"DHK", // DHL Air
		"DHX", // DHL International
		"GTI", // Atlas Air (Amazon)
		"ABX", // ABX Air (Amazon)
		"ATN", // Air Transport International (Amazon)
		"CKS", // Kalitta Air
		"WGN", // Western Global
		"CLX", // Cargolux
		"GEC", // Lufthansa Cargo
	}
	if len(ac.ADSBCallsign) > 3 && slices.Contains(cargoCarriers, string(ac.ADSBCallsign[:3])) {
		return 2 + rng.Intn(3) // 2-4 souls
	}

	// Check if we have specific data for this aircraft type from the database
	acType := ac.FlightPlan.AircraftType
	if perf, ok := av.DB.AircraftPerformance[acType]; ok && perf.Capacity.Passengers > 0 {
		maxPax := perf.Capacity.Passengers
		// Use 70-95% of capacity for a typical load; ignore crew (relatively negligible)
		load := 0.7 + rng.Float32()*0.25
		return int(float32(maxPax) * load)
	}

	// Fall back to CWT category average
	if perf, ok := av.DB.AircraftPerformance[acType]; ok {
		if avgPax, ok := cwtAveragePassengers[perf.Category.CWT]; ok {
			load := 0.7 + rng.Float32()*0.25
			return int(float32(avgPax) * load)
		}
	}

	// Final fallback. "This should never happen..."
	return 2
}

// getFuelRemaining returns realistic fuel remaining in pounds
func getFuelRemaining(ac *Aircraft, rng *rand.Rand) int {
	maxFuel := 10000 // fallback if we somehow don't find something better

	// Check if we have specific data for this aircraft type from the database
	perf := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if perf.Capacity.FuelPounds > 0 {
		maxFuel = perf.Capacity.FuelPounds
	} else if avg, ok := cwtFuelPounds[perf.Category.CWT]; ok {
		maxFuel = avg
	}

	if ac.IsArrival() {
		// Arrivals should have roughly 2 hours of fuel remaining

		// Guesstimate percentage fuel burned per hour of flight; assume it's proportional to
		// aircraft size.
		var percentBurnPerHour float32
		switch perf.Category.CWT {
		case "A", "B", "C":
			percentBurnPerHour = .06
		case "D", "E":
			percentBurnPerHour = .12
		case "F", "G", "H":
			percentBurnPerHour = .15
		case "I":
			percentBurnPerHour = .25
		}

		twoHoursFuel := int(2 * percentBurnPerHour * float32(maxFuel))
		// Add some variance ±10%
		variance := 1 + 0.2*(rng.Float32()-0.1)
		return int(float32(twoHoursFuel) * variance)
	} else {
		// Departures: estimate based on distance to destination
		// For now, use 60-90% of max fuel as a reasonable range for departures
		ratio := 0.6 + rng.Float32()*0.3
		return int(float32(maxFuel) * ratio)
	}
}

func (s *Sim) runEmergencyStage(ac *Aircraft) {
	es := ac.EmergencyState

	if es.CurrentStage >= len(es.Emergency.Stages) {
		// Done with this emergency
		ac.EmergencyState = nil
		return
	}

	if ac.IsUnassociated() {
		// This shouldn't happen
		s.lg.Warnf("%s: unassociated aircraft in emergency state. clearing emergency", ac.ADSBCallsign)
		ac.EmergencyState = nil
		return
	}

	stage := es.Emergency.Stages[es.CurrentStage]

	// Build transmission with various options
	var transmission []string
	var args []any

	transmit := func(s string, a ...any) {
		transmission = append(transmission, s)
		args = append(args, a...)
	}
	// Start with pan-pan prefix for certain airlines when declaring emergency
	if stage.DeclareEmergency {
		if slices.ContainsFunc(panPanAirlines, func(airline string) bool {
			return strings.HasPrefix(string(ac.ADSBCallsign), airline)
		}) {
			transmit("pan-pan, pan-pan, pan-pan")
		} else {
			transmit("[mayday mayday mayday|]. [we are|] declaring an emergency")
		}
	}

	transmit(stage.Transmission)

	// Sometimes (50% chance) proactively include souls on board and fuel remaining
	if stage.DeclareEmergency && s.Rand.Float32() < 0.5 {
		souls := getSoulsOnBoard(ac, s.Rand)
		transmit("[we have|] {num} souls [on board|] ", souls)

		fuel := getFuelRemaining(ac, s.Rand)

		// Sometimes report in tons instead of pounds
		if fuel > 10000 && s.Rand.Bool() {
			tons := float32(fuel) / 2000
			if tons < 10 {
				// Use one decimal place for < 10 tons
				decatons := int(tons*10 + 0.5)
				if decatons%10 == 0 {
					transmit("[and|] {num} tons of fuel [remaining|]", decatons/10)
				} else {
					transmit("[and|] {num} point {num} tons of fuel [remaining|]", decatons/10, decatons%10)
				}
			} else {
				// Use whole number for >= 10 tons
				transmit("[and|] {num} tons of fuel [remaining|]", int(tons+0.5))
			}
		} else if fuel < 100 {
			fuel = (fuel + 5) / 10 * 10 // round to 10s of pounds
			transmit("[and|] {num} pounds of [fuel|gas] [remaining|]", fuel)
		} else {
			fuel = (fuel + 50) / 100 * 100 // round to 100s of pounds
			thou, hund := fuel/1000, (fuel%1000)/100
			if hund == 0 {
				transmit("[and|] {num} thousand pounds of [fuel|gas] [remaining|]", thou)
			} else {
				transmit("[and|] {num} thousand {num} hundred pounds of [fuel|gas] [remaining|]", thou, hund)
			}
		}
	}

	// Handle stop_climb option
	if ac.IsDeparture() && stage.StopClimb {
		currentAlt := int(ac.Altitude())
		targetAlt, _ := ac.Nav.TargetAltitude()
		assignedAlt := int(targetAlt)
		if currentAlt+500 < assignedAlt {
			// Find next 1000-foot altitude above current
			stopAlt := ((currentAlt / 1000) + 1) * 1000

			// Ensure stop altitude is at least 2,500 feet AGL, rounded up to nearest 1000
			depElevation := int(ac.DepartureAirportElevation())
			minAltAGL := depElevation + 2500
			minAlt := ((minAltAGL + 999) / 1000) * 1000 // Round up to nearest 1000
			stopAlt = max(stopAlt, minAlt)

			if stopAlt < assignedAlt {
				transmit("[stopping our climb|we're going to stop at|we're going to level off at|leveling off at|maintaining] {alt}", stopAlt)
				ac.AssignAltitude(stopAlt, false) // discard readback
			}
		}
	}

	if stage.RequestReturn && ac.IsDeparture() {
		transmit("[request|request immediate|we'd like to] return to {airport}", ac.FlightPlan.DepartureAirport)
		ac.DivertToAirport(ac.FlightPlan.DepartureAirport)
	}

	if stage.RequestDelayVectors {
		transmit("[we'd like|we'd like some|we could use some|requesting] delay vectors")
		if s.Rand.Float32() < .15 {
			if ac.IsDeparture() {
				transmit("we'll let you know when we're ready to go back in")
			} else {
				transmit("we'll let you know when we're ready to go in")
			}
		}
	}

	if stage.Checklists {
		transmit("[investigating|we need to run some checklists|we're gonna run through some checklists|we need some time to troubleshoot]")
	}

	if stage.RequestEquipment {
		transmit("[we'd like equipment standing by|request ARFF waiting for us|roll the trucks for us|we're gonna need the trucks by the runway]")
	}

	// Post the radio transmission
	// Note: MakeContactTransmission automatically prepends controller position and callsign
	rt := av.MakeContactTransmission(strings.Join(transmission, ", "), args...)
	controller := ac.NASFlightPlan.ControllingController
	s.postContactTransmission(ac.ADSBCallsign, controller, *rt)

	// Schedule next stage based on current stage's duration
	es.CurrentStage++
	if es.CurrentStage < len(es.Emergency.Stages) {
		dur := stage.DurationMinutes
		delay := time.Duration(dur[0]+s.Rand.Intn(dur[1]-dur[0]+1)) * time.Minute

		es.NextUpdateTime = s.State.SimTime.Add(delay)
		s.enqueueEmergencyUpdate(ac.ADSBCallsign, es.NextUpdateTime)
	}
}
