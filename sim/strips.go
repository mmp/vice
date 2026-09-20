// sim/strips.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"slices"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// allocateStripCID picks a random free CID in 000-999 and marks it used.
func (s *Sim) allocateStripCID() int {
	if len(s.AvailableStripCIDs) > 0 {
		cid := s.AvailableStripCIDs[0]
		s.AvailableStripCIDs = s.AvailableStripCIDs[1:]
		return cid
	}
	return s.Rand.Intn(1000)
}

// freeStripCID releases a CID back to the pool.
func (s *Sim) freeStripCID(cid int) {
	s.AvailableStripCIDs = append(s.AvailableStripCIDs, cid)
}

// initFlightStrip assigns a strip CID and owner on the flight plan.
// No-op if the flight plan already has a strip.
func (s *Sim) initFlightStrip(fp *NASFlightPlan, owner ControlPosition) {
	if fp.StripOwner != "" {
		return
	}
	fp.StripCID = s.allocateStripCID()
	fp.StripOwner = owner
	s.lg.Debug("created flight strip", slog.String("acid", string(fp.ACID)), slog.String("owner", string(owner)))
}

func shouldCreateFlightStrip(fp *NASFlightPlan) bool {
	return fp.Rules == av.FlightRulesIFR || (fp.PlanType != LocalNonEnroute && fp.TypeOfFlight == av.FlightTypeDeparture)
}

// flightStripACIDsForTCW returns the ACIDs of all flight plans with strips
// owned by TCPs controlled by the given TCW. Caller must hold the mutex.
func (s *Sim) flightStripACIDsForTCW(tcw TCW) []ACID {
	var result []ACID
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && s.State.TCWControlsPosition(tcw, ac.NASFlightPlan.StripOwner) {
			result = append(result, ac.NASFlightPlan.ACID)
		}
	}
	for _, fp := range s.STARSComputer.FlightPlans {
		if s.State.TCWControlsPosition(tcw, fp.StripOwner) {
			result = append(result, fp.ACID)
		}
	}
	return result
}

// PushFlightStrip moves a flight strip to the given TCP.
func (s *Sim) PushFlightStrip(tcw TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp, _, _ := s.getFlightPlanForACID(acid)
	if fp == nil || !s.State.TCWControlsPosition(tcw, fp.StripOwner) {
		return ErrNoMatchingFlight
	}

	fp.StripOwner = ControlPosition(toTCP)
	s.publish()
	return nil
}

// AnnotateFlightStrip updates the annotations on a flight strip.
func (s *Sim) AnnotateFlightStrip(tcw TCW, acid ACID, annotations [9]string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp, _, _ := s.getFlightPlanForACID(acid)
	if fp == nil || !s.State.TCWControlsPosition(tcw, fp.StripOwner) {
		return ErrNoMatchingFlight
	}

	fp.StripAnnotations = annotations
	s.publish()
	return nil
}

func (s *Sim) GlobalMessage(tcw TCW, message string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		WrittenText:    message,
		FromController: s.State.PrimaryPositionForTCW(tcw),
	})
	s.publish()
}

func (s *Sim) CreateRestrictionArea(ra av.RestrictionArea) (int, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ra.UpdateTriangles()

	// Find the smallest unused key in 1-MaxRestrictionAreas (user range)
	for key := 1; key <= av.MaxRestrictionAreas; key++ {
		if _, exists := s.State.RestrictionAreas[key]; !exists {
			s.State.RestrictionAreas[key] = ra
			s.publish()
			return key, nil
		}
	}

	return 0, ErrTooManyRestrictionAreas
}

func (s *Sim) UpdateRestrictionArea(idx int, ra av.RestrictionArea) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if idx < 1 || idx > av.MaxRestrictionAreas {
		return ErrInvalidRestrictionAreaIndex
	}
	if _, exists := s.State.RestrictionAreas[idx]; !exists {
		return ErrInvalidRestrictionAreaIndex
	}

	// Update the triangulation just in case it's been moved.
	ra.UpdateTriangles()

	s.State.RestrictionAreas[idx] = ra
	s.publish()
	return nil
}

func (s *Sim) DeleteRestrictionArea(idx int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if idx < 1 || idx > av.MaxRestrictionAreas {
		return ErrInvalidRestrictionAreaIndex
	}
	if _, exists := s.State.RestrictionAreas[idx]; !exists {
		return ErrInvalidRestrictionAreaIndex
	}

	delete(s.State.RestrictionAreas, idx)
	s.publish()
	return nil
}

type ATPAConfigOp int

const (
	ATPAEnable ATPAConfigOp = iota
	ATPADisable
	ATPAEnableVolume
	ATPADisableVolume
	ATPAEnableReduced25
	ATPADisableReduced25
)

func (s *Sim) ConfigureATPA(op ATPAConfigOp, volumeId string) (msg string, err error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	defer func() {
		if err == nil {
			s.publish()
		}
	}()

	if len(s.State.ATPAVolumeState) == 0 { // no volumes adapted
		return "", ErrATPADisabled
	}

	// 8.38 Enable/disable ATPA system-wide
	if op == ATPAEnable {
		if s.State.ATPAEnabled {
			return "NO CHANGE", nil
		}

		s.State.ATPAEnabled = true
		// Reset all volume states to defaults
		for _, airportState := range s.State.ATPAVolumeState {
			for _, volState := range airportState {
				volState.Disabled = false
				volState.Reduced25Disabled = false
			}
		}
		return "ATPA ENABLED", nil
	} else if op == ATPADisable {
		if !s.State.ATPAEnabled {
			return "NO CHANGE", nil
		}
		s.State.ATPAEnabled = false
		return "ATPA INHIBITED", nil
	}

	// All other ops need ATPA enabled and a valid volume.
	if !s.State.ATPAEnabled {
		return "", ErrATPADisabled
	}

	airport := s.State.FindAirportForATPAVolume(volumeId)
	if airport == "" {
		return "", ErrInvalidVolumeId
	}
	vol := s.State.Airports[airport].ATPAVolumes[volumeId]
	volState := s.State.ATPAVolumeState[airport][volumeId]

	// 8.39: Enable/disable ATPA approach volume
	if op == ATPAEnableVolume {
		if !volState.Disabled {
			return "NO CHANGE", nil
		}
		volState.Disabled = false
		volState.Reduced25Disabled = false
		return volumeId + " ENABLED", nil
	} else if op == ATPADisableVolume {
		if volState.Disabled {
			return "NO CHANGE", nil
		}
		volState.Disabled = true
		return volumeId + " INHIBITED", nil
	}

	// 8.40: Enable/disable 2.5nm reduced separation for volume
	if !vol.Enable25nmApproach {
		return "", ErrVolumeNot25nm
	}
	if volState.Disabled {
		return "", ErrVolumeDisabled
	}

	if op == ATPAEnableReduced25 {
		if !volState.Reduced25Disabled {
			return "NO CHANGE", nil
		}
		volState.Reduced25Disabled = false
		return volumeId + " 2.5 ENABLED", nil
	} else if op == ATPADisableReduced25 {
		if volState.Reduced25Disabled {
			return "NO CHANGE", nil
		}
		volState.Reduced25Disabled = true
		return volumeId + " 2.5 INHIBITED", nil
	}

	// Should not get here...
	return "", nil
}

type FDAMConfigOp int

const (
	FDAMToggleSystem FDAMConfigOp = iota
	FDAMEnableSystem
	FDAMInhibitSystem
	FDAMToggleRegion
	FDAMEnableRegion
	FDAMInhibitRegion
	FDAMQueryStatus
)

func (s *Sim) ConfigureFDAM(op FDAMConfigOp, regionId string) (msg string, err error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	defer func() {
		if err == nil {
			s.publish()
		}
	}()

	if len(s.State.FacilityAdaptation.Filters.FDAM) == 0 {
		return "", ErrFDAMNoRegions
	}

	switch op {
	case FDAMToggleSystem:
		s.FDAMSystemInhibited = !s.FDAMSystemInhibited
		return util.Select(s.FDAMSystemInhibited, "FLIGHT-DATA AUTO-MOD PROC OFF", "FLIGHT-DATA AUTO-MOD PROC ON"), nil
	case FDAMEnableSystem:
		s.FDAMSystemInhibited = false
		return "FLIGHT-DATA AUTO-MOD PROC ON", nil
	case FDAMInhibitSystem:
		s.FDAMSystemInhibited = true
		return "FLIGHT-DATA AUTO-MOD PROC OFF", nil
	}

	if s.FDAMSystemInhibited {
		return "", ErrFDAMProcessingOff
	}

	if op == FDAMQueryStatus {
		return s.fdamStatusString(), nil
	}

	if !s.State.FacilityAdaptation.Filters.FDAM.HaveId(regionId) {
		return "", ErrFDAMIllegalArea
	}

	if s.DisabledFDAMRegions == nil {
		s.DisabledFDAMRegions = make(map[string]struct{})
	}

	switch op {
	case FDAMToggleRegion:
		if _, ok := s.DisabledFDAMRegions[regionId]; ok {
			delete(s.DisabledFDAMRegions, regionId)
			return "REGION " + regionId + " ON", nil
		}
		s.DisabledFDAMRegions[regionId] = struct{}{}
		return "REGION " + regionId + " OFF", nil
	case FDAMEnableRegion:
		delete(s.DisabledFDAMRegions, regionId)
		return "REGION " + regionId + " ON", nil
	case FDAMInhibitRegion:
		s.DisabledFDAMRegions[regionId] = struct{}{}
		return "REGION " + regionId + " OFF", nil
	}
	return "", nil
}

func (s *Sim) fdamStatusString() string {
	var enabled, disabled []string
	for _, f := range s.State.FacilityAdaptation.Filters.FDAM {
		if _, ok := s.DisabledFDAMRegions[f.Id]; ok {
			disabled = append(disabled, f.Id)
		} else {
			enabled = append(enabled, f.Id)
		}
	}

	var output string
	appendRegions := func(regions []string, header string) {
		if len(regions) == 0 {
			return
		}
		if output != "" {
			output += "\n"
		}
		output += header + "\n"
		slices.Sort(regions)
		for i, id := range regions {
			if i > 0 && i%5 == 0 {
				output += "\n"
			} else if i > 0 {
				output += " "
			}
			output += id
		}
	}
	appendRegions(enabled, "ENAB FLIGHT-DATA AUTO-MOD FLTRS")
	appendRegions(disabled, "DISAB FLIGHT-DATA AUTO-MOD FLTRS")
	return output
}

func (s *Sim) PostEvent(e Event) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(e)
}

func (s *Sim) UpdateATISGIText(_ TCW, line int, auxiliary bool, atis *string, text *string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Main and auxiliary commands use different line domains even though they
	// update the same shared arrays.
	if auxiliary {
		if line <= 0 || line >= len(s.State.ATIS) {
			return ErrIllegalLine
		}
	} else if line != 0 {
		return ErrIllegalLine
	}
	if atis != nil {
		// nil means "leave ATIS unchanged"; empty string means "clear ATIS".
		switch len(*atis) {
		case 0:
			s.State.ATIS[line] = ""
		case 1:
			if ch := (*atis)[0]; ch < 'A' || ch > 'Z' {
				return ErrIllegalATIS
			}
			s.State.ATIS[line] = *atis
		default:
			return ErrIllegalATIS
		}
	}
	if text != nil {
		s.State.GIText[line] = *text
	}

	s.publish()
	return nil
}

func (s *Sim) Facility() string {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.State.Facility
}

// GetUserState returns a deep copy of the simulation state for a client.
// Server-only fields (like Airport.Departures) are pruned to reduce bandwidth.
func (s *Sim) GetUserState() *UserState {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	state := UserState{
		CommonState:  *s.State,
		DerivedState: makeDerivedState(s),
	}

	// Make a deep copy so that any state changes after the lock is released aren't included.
	state = deep.MustCopy(state)

	// Prune server-only fields not needed by clients.
	for _, ap := range state.Airports {
		ap.Departures = nil
	}

	return &state
}

// GetDepartureController returns the TCP responsible for a departure given the
// airport, runway, and SID. Checks in order: airport/SID, airport/runway, airport only.
func (s *Sim) GetDepartureController(airport av.ICAOAirportCode, runway, sid string) TCP {
	if sid != "" {
		if tcp, ok := s.DepartureAssignments[string(airport)+"/"+sid]; ok {
			return tcp
		}
	}
	if runway != "" {
		if tcp, ok := s.DepartureAssignments[string(airport)+"/"+runway]; ok {
			return tcp
		}
	}
	if tcp, ok := s.DepartureAssignments[string(airport)]; ok {
		return tcp
	}
	return ""
}

// ScenarioRootPosition returns the root position from the scenario's default consolidation.
func (s *Sim) ScenarioRootPosition() TCP {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.scenarioRootPosition()
}

func (s *Sim) scenarioRootPosition() TCP {
	if root, err := s.ScenarioDefaultConsolidation.RootPosition(); err != nil {
		return ""
	} else {
		return root
	}
}

// AllScenarioPositions returns all positions defined in the scenario's default consolidation.
func (s *Sim) AllScenarioPositions() []TCP {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.ScenarioDefaultConsolidation.AllPositions()
}

// GetTrafficCounts returns the current IFR and VFR traffic counts.
func (s *Sim) GetTrafficCounts() (ifr, vfr int) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.TotalIFR, s.TotalVFR
}

// FutureOnCourse represents a departure that will be instructed to proceed
// on its filed route at a future time after the initial climbout delay.
type FutureOnCourse struct {
	ADSBCallsign av.ADSBCallsign
	Time         Time
}

func (s *Sim) enqueueDepartOnCourse(callsign av.ADSBCallsign) {
	wait := s.Rand.DurationRange(10*time.Second, 25*time.Second)
	s.FutureOnCourse = append(s.FutureOnCourse,
		FutureOnCourse{ADSBCallsign: callsign, Time: s.State.SimTime.Add(wait)})
}

func (s *Sim) processFutureOnCourse() {
	s.FutureOnCourse = util.FilterSliceInPlace(s.FutureOnCourse,
		func(oc FutureOnCourse) bool {
			if s.State.SimTime.After(oc.Time) {
				if ac, ok := s.Aircraft[oc.ADSBCallsign]; ok {
					s.lg.Info("departing on course", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
						slog.Int("final_altitude", ac.FlightPlan.Altitude))
					// Clear temporary altitude, unless the route's altitude
					// actions govern the aircraft's altitude and it isn't
					// climbing to cruise.
					if ac.NASFlightPlan != nil && !ac.Nav.RouteAltitudeActions {
						ac.NASFlightPlan.InterimAlt = 0
						ac.NASFlightPlan.InterimType = InterimNormal
					}
					ac.DepartOnCourse(s.State.SimTime, s.lg)
				}
				return false
			}
			return true
		})
}
