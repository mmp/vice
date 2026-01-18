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
			if ac.TypeOfFlight == av.FlightTypeArrival && inVolumes(filters.ArrivalDrop) {
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
			fp.DeleteTime = s.State.SimTime.Add(2 * time.Minute) // hold it for a bit before deleting
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

					// Remove it from the released departures list
					sc.HoldForRelease = slices.DeleteFunc(sc.HoldForRelease,
						func(ac2 *Aircraft) bool { return ac.ADSBCallsign == ac2.ADSBCallsign })
				}
			} else if alertMissingFP {
				ac.MissingFlightPlan = true
			}
		}
	}
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
