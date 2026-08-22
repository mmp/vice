// pkg/sim/nas.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

const UnsetSTARSListIndex = 0

type ERAMComputer struct {
	SquawkCodePool *av.EnrouteSquawkCodePool
	Identifier     string
	Adaptation     av.ERAMAdaptation
}

type STARSComputer struct {
	Identifier       string
	FlightPlans      []*NASFlightPlan
	HoldForRelease   []*Aircraft
	AvailableIndices []int
}

func makeERAMComputer(fac string, loc *av.LocalSquawkCodePool) *ERAMComputer {
	ec := &ERAMComputer{
		Adaptation:     av.DB.ERAMAdaptations[fac],
		SquawkCodePool: av.MakeEnrouteSquawkCodePool(loc),
		Identifier:     fac,
	}

	return ec
}

func (ec *ERAMComputer) CreateSquawk() (av.Squawk, error) {
	return ec.SquawkCodePool.Get(rand.Make())
}

func (ec *ERAMComputer) ReturnSquawk(code av.Squawk) error {
	return ec.SquawkCodePool.Return(code)
}

func (ec *ERAMComputer) Update(s *Sim) {
}

func makeSTARSComputer(id string) *STARSComputer {
	sc := &STARSComputer{
		Identifier:       id,
		AvailableIndices: make([]int, 99),
	}

	for i := range 99 {
		sc.AvailableIndices[i] = i + 1
	}
	return sc
}

func (sc *STARSComputer) ReleaseDeparture(callsign av.ADSBCallsign) error {
	idx := slices.IndexFunc(sc.HoldForRelease,
		func(ac *Aircraft) bool { return ac.ADSBCallsign == callsign })
	if idx == -1 {
		return av.ErrNoAircraftForCallsign
	}

	if sc.HoldForRelease[idx].Released {
		return ErrAircraftAlreadyReleased
	} else {
		sc.HoldForRelease[idx].Released = true
		return nil
	}
}

func (sc *STARSComputer) GetReleaseDepartures() []*Aircraft {
	return slices.Clone(sc.HoldForRelease)
}

func (sc *STARSComputer) AddHeldDeparture(ac *Aircraft) {
	sc.HoldForRelease = append(sc.HoldForRelease, ac)
}

// Note: called with Sim holding its mutex, so we can access its members here.
func (sc *STARSComputer) Update(s *Sim) {
	// Delete any dropped flight plans after the few minute delay has passed.
	sc.FlightPlans = util.FilterSlice(sc.FlightPlans, func(fp *NASFlightPlan) bool {
		if !fp.DeleteTime.IsZero() && s.State.SimTime.After(fp.DeleteTime) {
			// Return beacon code, list index
			s.deleteFlightPlan(fp)
			return false
		}
		return true
	})

	for _, ac := range s.Aircraft {
		if !ac.IsAirborne() || ac.Squawk == 0o1200 {
			continue
		}

		if !s.isRadarVisible(ac) {
			continue
		}

		inVolumes := func(f FilterRegions) bool {
			return f.Inside(ac.Position(), int(ac.Altitude()))
		}

		filters := s.State.FacilityAdaptation.Filters

		drop := func() bool {
			if ac.TypeOfFlight == av.FlightTypeArrival && !ac.WentAround && inVolumes(filters.ArrivalDrop) {
				return true
			} else if fp := ac.NASFlightPlan; fp != nil {
				if fp.LastLocalController != "" && s.State.IsExternalController(fp.TrackingController) &&
					inVolumes(filters.SecondaryDrop) {
					return true
				}
			}
			return false
		}()
		if ac.IsAssociated() && drop {
			fp := ac.DisassociateFlightPlan()
			fp.DeleteTime = s.State.SimTime.Add(4 * time.Minute) // hold it for a bit before deleting
			sc.FlightPlans = append(sc.FlightPlans, fp)
		} else if ac.IsUnassociated() && !drop { // unassociated--associate?
			fp := sc.lookupFlightPlanBySquawk(ac.Squawk)
			associate := func() bool {
				if ac.Mode == av.TransponderModeStandby {
					// No beacon code, so can't acquire.
					return false
				}

				if fp == nil {
					// No flight plan for the beacon code
					return false
				}

				if !fp.DeleteTime.IsZero() {
					// Don't auto-associate flight plans that have been dropped.
					return false
				}

				if inVolumes(filters.SurfaceTracking) {
					// Still on the ground and not yet radar visible
					return false
				}

				if fp.ManuallyCreated {
					// Always associate manually-created flight plans automatically
					return true
				}

				if ac.TypeOfFlight == av.FlightTypeDeparture {
					// Simulate delay in tagging up between the first time it's left the surface tracking filter.
					if !ac.DepartureFPAcquisitionTime.IsZero() && s.State.SimTime.After(ac.DepartureFPAcquisitionTime) {
						return true
					} else if ac.DepartureFPAcquisitionTime.IsZero() {
						ac.DepartureFPAcquisitionTime = s.State.SimTime.Add(8 * time.Second)
					}
					return false
				} else { // arrival or overflight
					if inVolumes(filters.AutoAcquisition) {
						return true
					}
					// Inbound handoff from an external facility
					if (fp.HandoffController != "" && s.State.IsLocalController(fp.HandoffController)) ||
						// Handoff to a virtual controller
						s.State.IsLocalController(fp.TrackingController) {
						return true
					}
				}
				return false
			}()
			alertMissingFP := func() bool {
				if ac.Mode == av.TransponderModeStandby || ac.Squawk == 0o1200 || fp != nil {
					return false
				}
				// Only do the expensive inVolumes checks if they're actually necessary.
				return !ac.MissingFlightPlan && !inVolumes(filters.SurfaceTracking) && inVolumes(filters.Departure)
			}()

			if associate {
				if fp := sc.takeFlightPlanBySquawk(ac.Squawk); fp != nil {
					fp.OwningTCW = s.tcwForPosition(fp.TrackingController)

					if fp.ManuallyCreated {
						// If an aircraft tagged up on a manually created
						// FP, assume that they called and asked for flight
						// following and so are already on frequency.
						ac.ControllerFrequency = ControlPosition(fp.TrackingController)
					}
					if s.State.IsLocalController(fp.TrackingController) {
						fp.LastLocalController = fp.TrackingController
					}

					ac.AssociateFlightPlan(fp)

					s.eventStream.Post(Event{
						Type: FlightPlanAssociatedEvent,
						ACID: fp.ACID,
					})

					// If it was handed off virtual-to-virtual before the
					// track associated, send it on course now.
					if s.DeferredOnCourse[fp.ACID] {
						delete(s.DeferredOnCourse, fp.ACID)
						s.enqueueDepartOnCourse(ac.ADSBCallsign)
					}

					// Remove it from the released departures list
					sc.HoldForRelease = slices.DeleteFunc(sc.HoldForRelease,
						func(ac2 *Aircraft) bool { return ac.ADSBCallsign == ac2.ADSBCallsign })
				}
			} else if alertMissingFP {
				ac.MissingFlightPlan = true
			}
		}

		// FDAM processing: apply entry/exit actions for associated tracks.
		if ac.IsAssociated() && !s.FDAMSystemInhibited {
			s.processFDAMRegions(ac)
		}

		// Auto-handoff filter processing for associated tracks.
		if ac.IsAssociated() && !s.State.AutoHandoffSiteInhibited {
			s.processHandoffFilterRegions(ac)
		}
	}
}

// processHandoffFilterRegions implements the STARS Auto-Handoff Window (DMS
// Sec. 4.7.7, p. 4-185): when a locally-owned track enters a handoff filter
// volume and matches a row's conditions, the first matching row's Handoff
// Action fires.
func (s *Sim) processHandoffFilterRegions(ac *Aircraft) {
	fp := ac.NASFlightPlan
	if fp == nil {
		return
	}
	regions := s.State.FacilityAdaptation.Filters.Handoff
	if len(regions) == 0 {
		return
	}

	// Auto-handoff initiate is for locally-owned tracks: a local sector hands
	// the track to another sector. Tracks owned by an external facility are
	// not eligible, nor are ones already in handoff status.
	if !s.State.IsLocalController(fp.TrackingController) ||
		fp.HandoffController != "" || fp.RedirectedHandoff.RedirectedTo != "" {
		return
	}

	// AHOP may be inhibited for the owning position (STARS 4.3, p. 4-30), and
	// some tracks are permanently excluded from it (STARS 5.1.7, p. 5-15).
	if s.State.AutoHandoffInhibitedForTCW(fp.OwningTCW) || autoHandoffDisabledByCondition(fp, ac) {
		return
	}

	if fp.HandoffFilterState == nil {
		fp.HandoffFilterState = make(map[string]bool)
	}

	acType := fp.AircraftType
	activePlan := s.State.ConfigurationId
	acClasses := s.State.FacilityAdaptation.AutomaticHandoffClasses
	sigPts := s.State.FacilityAdaptation.SignificantPoints
	pos := ac.Position()
	alt := int(ac.Altitude())

	firstFired := -1
	for i := range regions {
		region := &regions[i]
		wasInside := fp.HandoffFilterState[region.Id]

		// On entry, both the volume and all conditions must match. Once
		// inside, only the volume determines continued presence, so that an
		// action which changes a qualifying field doesn't immediately toggle
		// the track back out (mirrors FDAM processing).
		var nowInside bool
		if wasInside {
			nowInside = region.AirspaceVolume.Inside(pos, alt)
		} else {
			nowInside = region.qualifies(pos, alt, fp, acType, activePlan, acClasses, sigPts) &&
				s.handoffFilterOwnerMatch(region, fp)
		}
		fp.HandoffFilterState[region.Id] = nowInside

		if nowInside && !wasInside && firstFired == -1 {
			firstFired = i
		}
	}

	if firstFired >= 0 {
		s.applyHandoffFilterAction(&regions[firstFired], fp)
	}
}

// handoffFilterOwnerMatch checks the Owning TCP / "+ Slave TCPs" condition
// (DMS Table 4-30) against the track's current owner.
func (s *Sim) handoffFilterOwnerMatch(region *HandoffFilterRegion, fp *NASFlightPlan) bool {
	if len(region.OwnerTCPs) == 0 {
		return true
	}
	return slices.ContainsFunc(region.OwnerTCPs, func(tcp ControlPosition) bool {
		// "+" also matches when an adapted Owning TCP is consolidated to the
		// flight's owning TCP (i.e. it is a secondary TCP of the owner's TCW).
		return tcp == fp.TrackingController ||
			(region.SlaveTCPs && s.State.TCWControlsPosition(fp.OwningTCW, tcp))
	})
}

// applyHandoffFilterAction executes a matched row's Handoff Action.
func (s *Sim) applyHandoffFilterAction(region *HandoffFilterRegion, fp *NASFlightPlan) {
	if region.HandoffAction == "D" {
		// Disable automatic handoff for this track; shows the delta indicator
		// and can't be undone by the controller.
		fp.AutoHandoffInhibited = true
		fp.AutoHandoffInhibitLocked = true
		return
	}

	// A track that a controller disabled for auto-handoff is left alone, as is
	// one whose owner already controls the row's receiver (both are resolved
	// through consolidation before comparing).
	if fp.AutoHandoffInhibited ||
		s.State.ResolveController(region.HORcvr) == s.State.ResolveController(fp.TrackingController) {
		return
	}

	switch region.HandoffAction {
	case "I":
		s.handoffTrack(fp, region.HORcvr)
		// Mark it automatic so that a recall makes the track ineligible for
		// further auto-handoff (STARS 5.1.17, p. 5-33).
		fp.HandoffWasAutomatic = true

	case "T":
		s.eventStream.Post(Event{
			Type:           TransferAcceptedEvent,
			FromController: fp.TrackingController,
			ToController:   region.HORcvr,
			ACID:           fp.ACID,
		})
		fp.TrackingController = region.HORcvr
		fp.OwningTCW = s.tcwForPosition(region.HORcvr)
	}
}

func (s *Sim) processFDAMRegions(ac *Aircraft) {
	fp := ac.NASFlightPlan
	if fp == nil {
		return
	}
	if fp.FDAMState == nil {
		fp.FDAMState = make(map[string]*FDAMTrackState)
	}

	acType := fp.AircraftType

	for i := range s.State.FacilityAdaptation.Filters.FDAM {
		region := &s.State.FacilityAdaptation.Filters.FDAM[i]
		if _, disabled := s.DisabledFDAMRegions[region.Id]; disabled {
			continue
		}
		state, exists := fp.FDAMState[region.Id]
		if !exists {
			state = &FDAMTrackState{}
			fp.FDAMState[region.Id] = state
		}

		wasInside := state.Inside
		// For initial entry, both the airspace volume and filter qualifiers
		// must match. Once inside, only the airspace volume determines
		// continued presence; entry actions (e.g. handoff) may change
		// flight plan fields that the qualifiers check against.
		var nowInside bool
		if wasInside {
			nowInside = region.AirspaceVolume.Inside(ac.Position(), int(ac.Altitude()))
		} else {
			nowInside = region.Match(ac.Position(), int(ac.Altitude()), fp, acType,
				s.State.FacilityAdaptation.SignificantPoints)
		}
		state.Inside = nowInside

		if nowInside && !wasInside {
			s.applyFDAMEntryActions(region, fp, state)
		} else if !nowInside && wasInside {
			s.applyFDAMExitActions(region, fp, state)
		}
	}
}

func (s *Sim) applyFDAMEntryActions(region *FDAMRegion, fp *NASFlightPlan, state *FDAMTrackState) {
	// Save pre-entry state for potential revert on exit.
	state.PreEntryOwnerLeaderDirection = fp.GlobalLeaderLineDirection

	// Scratchpad 1
	if region.NewScratchpad1 != "" {
		if region.AllowScratchpad1Override || fp.Scratchpad == "" {
			if region.NewScratchpad1 == "-" {
				fp.Scratchpad = ""
			} else {
				fp.Scratchpad = region.NewScratchpad1
			}
		}
	}

	// Scratchpad 2
	if region.NewScratchpad2 != "" {
		if region.AllowScratchpad2Override || fp.SecondaryScratchpad == "" {
			if region.NewScratchpad2 == "-" {
				fp.SecondaryScratchpad = ""
			} else {
				fp.SecondaryScratchpad = region.NewScratchpad2
			}
		}
	}

	// Owner leader direction
	if region.NewOwnerLeaderDirection != nil {
		fp.GlobalLeaderLineDirection = region.NewOwnerLeaderDirection
		s.eventStream.Post(Event{
			Type: SetGlobalLeaderLineEvent,
			ACID: fp.ACID,
		})
	}

	// TCP-specific leader direction: set via event for each pointout TCP
	if region.NewTCPSpecificLeaderDirection != nil && len(region.PointoutTCPs) > 0 {
		for _, tcp := range region.PointoutTCPs {
			s.eventStream.Post(Event{
				Type:                FDAMLeaderLineEvent,
				ACID:                fp.ACID,
				ToController:        tcp,
				LeaderLineDirection: region.NewTCPSpecificLeaderDirection,
			})
		}
	}

	// Handoff or transfer
	switch region.HandoffInitiateTransfer {
	case "I":
		if region.NewOwnerTCP != "" {
			s.handoffTrack(fp, region.NewOwnerTCP)
		}
	case "T":
		if region.NewOwnerTCP != "" {
			s.eventStream.Post(Event{
				Type:           TransferAcceptedEvent,
				FromController: fp.TrackingController,
				ToController:   region.NewOwnerTCP,
				ACID:           fp.ACID,
			})
			fp.TrackingController = region.NewOwnerTCP
			fp.OwningTCW = s.tcwForPosition(region.NewOwnerTCP)
		}
	}

	// Immediate pointout (force quicklook)
	if region.ImmediatePointout {
		for _, tcp := range region.PointoutTCPs {
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: fp.TrackingController,
				ToController:   tcp,
				ACID:           fp.ACID,
			})
		}
	}
}

func (s *Sim) applyFDAMExitActions(region *FDAMRegion, fp *NASFlightPlan, state *FDAMTrackState) {
	// Revert owner leader direction if not retained
	if !region.RetainOwnerLeaderDirection && region.NewOwnerLeaderDirection != nil {
		fp.GlobalLeaderLineDirection = state.PreEntryOwnerLeaderDirection
		s.eventStream.Post(Event{
			Type: SetGlobalLeaderLineEvent,
			ACID: fp.ACID,
		})
	}

	// Revert TCP-specific leader directions if not retained
	if !region.RetainTCPSpecificLeaderDirection && region.NewTCPSpecificLeaderDirection != nil {
		for _, tcp := range region.PointoutTCPs {
			s.eventStream.Post(Event{
				Type:         FDAMLeaderLineEvent,
				ACID:         fp.ACID,
				ToController: tcp,
				// nil LeaderLineDirection signals revert
			})
		}
	}

	// Clear saved state
	state.PreEntryOwnerLeaderDirection = nil
}

func (sc *STARSComputer) lookupFlightPlanByACID(acid ACID) *NASFlightPlan {
	if idx := slices.IndexFunc(sc.FlightPlans,
		func(fp *NASFlightPlan) bool { return acid == fp.ACID }); idx != -1 {
		return sc.FlightPlans[idx]
	}
	return nil
}

func (sc *STARSComputer) takeFlightPlanByACID(acid ACID) *NASFlightPlan {
	if idx := slices.IndexFunc(sc.FlightPlans,
		func(fp *NASFlightPlan) bool { return acid == fp.ACID }); idx != -1 {
		fp := sc.FlightPlans[idx]
		sc.FlightPlans = append(sc.FlightPlans[:idx], sc.FlightPlans[idx+1:]...)
		return fp
	}
	return nil
}

func (sc *STARSComputer) lookupFlightPlanBySquawk(sq av.Squawk) *NASFlightPlan {
	if idx := slices.IndexFunc(sc.FlightPlans,
		func(fp *NASFlightPlan) bool { return sq == fp.AssignedSquawk }); idx != -1 {
		return sc.FlightPlans[idx]
	}
	return nil
}

func (sc *STARSComputer) takeFlightPlanBySquawk(sq av.Squawk) *NASFlightPlan {
	if idx := slices.IndexFunc(sc.FlightPlans,
		func(fp *NASFlightPlan) bool { return sq == fp.AssignedSquawk }); idx != -1 {
		fp := sc.FlightPlans[idx]
		sc.FlightPlans = slices.Delete(sc.FlightPlans, idx, idx+1)
		return fp
	}
	return nil
}

func (sc *STARSComputer) CreateFlightPlan(fp NASFlightPlan) (NASFlightPlan, error) {
	if fp2 := sc.lookupFlightPlanByACID(fp.ACID); fp2 != nil {
		return fp, ErrDuplicateACID
	}

	fp.ListIndex = sc.getListIndex()

	sc.FlightPlans = append(sc.FlightPlans, &fp)

	return fp, nil
}

func (sc *STARSComputer) getListIndex() int {
	if len(sc.AvailableIndices) == 0 {
		return UnsetSTARSListIndex
	}

	idx := sc.AvailableIndices[0]
	sc.AvailableIndices = sc.AvailableIndices[1:]
	return idx
}

func (sc *STARSComputer) returnListIndex(idx int) {
	if idx != UnsetSTARSListIndex {
		sc.AvailableIndices = append(sc.AvailableIndices, idx)
	}
}
