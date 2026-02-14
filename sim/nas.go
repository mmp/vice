// sim/nas.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"
	"sort"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/rand"
)

const UnsetSTARSListIndex = 0

// FacilityTrack represents a track as seen by a specific facility computer.
type FacilityTrack struct {
	ACID          ACID
	CoupledFP     *NASFlightPlan  // Coupled flight plan (nil if uncoupled)
	Owner         ControlPosition // Controller who owns track (empty if only facility known)
	OwnerFacility string          // Facility code of the owning controller
	HandoffState  TrackHandoffState
}

// TrackHandoffState tracks the state of a cross-facility handoff for a track.
type TrackHandoffState int

const (
	TrackHandoffNone     TrackHandoffState = iota
	TrackHandoffOffered                    // TI sent/received, DA sent, awaiting TN
	TrackHandoffAccepted                   // TN received, track transferred
)

// TimestampedMessage records a NAS message along with when it was received,
// for the debug GUI inbox history (messages persist for ~5 seconds).
type TimestampedMessage struct {
	Msg        NASMessage
	ReceivedAt time.Time
}

// fpForMessage returns a stripped copy of a NASFlightPlan containing only
// the information an ERAM FP message would transmit: ACID, CID, squawk,
// altitude, route, aircraft type, entry/exit fix, and flight rules.
// Control state (TrackingController, OwningTCW, scratchpads, etc.) is
// intentionally omitted — the receiving STARS only knows the FP came
// from center.
func fpForMessage(fp *NASFlightPlan) *NASFlightPlan {
	return &NASFlightPlan{
		ACID:                  fp.ACID,
		CID:                   fp.CID,
		AssignedSquawk:        fp.AssignedSquawk,
		AssignedAltitude:      fp.AssignedAltitude,
		RequestedAltitude:     fp.RequestedAltitude,
		Route:                 fp.Route,
		AircraftType:          fp.AircraftType,
		EquipmentSuffix:       fp.EquipmentSuffix,
		AircraftCount:         fp.AircraftCount,
		EntryFix:              fp.EntryFix,
		ExitFix:               fp.ExitFix,
		ExitFixIsIntermediate: fp.ExitFixIsIntermediate,
		DepartureAirport:      fp.DepartureAirport,
		ArrivalAirport:        fp.ArrivalAirport,
		Rules:                 fp.Rules,
		TypeOfFlight:          fp.TypeOfFlight,
		RNAV:                  fp.RNAV,
		CoordinationFix:       fp.CoordinationFix,
		CoordinationTime:      fp.CoordinationTime,
		CWTCategory:           fp.CWTCategory,
		PlanType:              fp.PlanType,
	}
}

type ERAMComputer struct {
	Identifier     string
	SquawkCodePool *av.EnrouteSquawkCodePool // nil for neighbor stubs

	FlightPlans map[ACID]*NASFlightPlan
	Tracks      map[ACID]*FacilityTrack
	Inbox       []NASMessage
	RecentInbox []TimestampedMessage // Processed messages kept for debug GUI (~5s)

	// Topology
	Children map[string]*STARSComputer // TRACON code -> child STARS
	Peers    map[string]*ERAMComputer  // ARTCC code -> peer ERAM

	// Airport ICAO -> TRACON code (built from av.DB geometry)
	Airports map[string]string
}

type STARSComputer struct {
	Identifier       string
	FlightPlans      map[ACID]*NASFlightPlan
	HoldForRelease   []*Aircraft
	AvailableIndices []int

	Tracks      map[ACID]*FacilityTrack
	Inbox       []NASMessage
	RecentInbox []TimestampedMessage // Processed messages kept for debug GUI (~5s)

	// Topology
	ParentERAM *ERAMComputer

	// Facility adaptation for fix-pair routing
	FixPairs           []FixPairDefinition
	FixPairAssignments []FixPairAssignment

	// Airport ICAOs within this TRACON (from av.DB geometry)
	Airports []string
}

func makeERAMComputer(fac string, loc *av.LocalSquawkCodePool) *ERAMComputer {
	ec := &ERAMComputer{
		Identifier:  fac,
		FlightPlans: make(map[ACID]*NASFlightPlan),
		Tracks:      make(map[ACID]*FacilityTrack),
		Children:    make(map[string]*STARSComputer),
		Peers:       make(map[string]*ERAMComputer),
		Airports:    make(map[string]string),
	}

	if loc != nil {
		ec.SquawkCodePool = av.MakeEnrouteSquawkCodePool(loc)
	}

	return ec
}

func (ec *ERAMComputer) CreateSquawk() (av.Squawk, error) {
	if ec.SquawkCodePool == nil {
		return av.Squawk(0), av.ErrNoMoreAvailableSquawkCodes // neighbor stub has no pool
	}
	return ec.SquawkCodePool.Get(rand.Make())
}

func (ec *ERAMComputer) ReturnSquawk(code av.Squawk) error {
	if ec.SquawkCodePool == nil {
		return nil
	}
	return ec.SquawkCodePool.Return(code)
}

// StoreFlightPlan stores a flight plan in ERAM's FlightPlans map.
// This is called at spawn time so ERAM has the authoritative copy.
func (ec *ERAMComputer) StoreFlightPlan(fp *NASFlightPlan) {
	stripped := fpForMessage(fp)
	ec.FlightPlans[fp.ACID] = stripped
}

func (ec *ERAMComputer) Update(s *Sim) {
	// ERAM-to-ERAM distribution: forward FPs to the peer ERAM that
	// serves the arrival airport, using the airport_artccs database.
	// Only forward if the arrival airport is in a different ARTCC.
	for _, fp := range ec.FlightPlans {
		if fp.ArrivalAirport == "" {
			continue
		}
		// Look up which ARTCC serves the arrival airport
		arrAp, ok := av.DB.Airports[fp.ArrivalAirport]
		if !ok || arrAp.ARTCC == "" || arrAp.ARTCC == ec.Identifier {
			continue // Same ARTCC or unknown — no forwarding needed
		}
		// Forward to the specific peer that serves the arrival airport
		peerERAM, isPeer := ec.Peers[arrAp.ARTCC]
		if !isPeer {
			continue // No peer for that ARTCC
		}
		if _, exists := peerERAM.FlightPlans[fp.ACID]; exists {
			continue
		}
		if _, exists := peerERAM.Tracks[fp.ACID]; exists {
			continue
		}
		ec.SendMessage(s.nasNet, NASMessage{
			Type:       MsgFP,
			ToFacility: arrAp.ARTCC,
			ACID:       fp.ACID,
			Timestamp:  s.State.SimTime,
			FlightPlan: fpForMessage(fp),
		})
	}

	// ERAM trajectory modeling: distribute FPs to child TRACONs
	// ~20 minutes before aircraft entry, based on waypoint ETAs.
	const fpDistributionLookahead = 20 * time.Minute

	for _, ac := range s.Aircraft {
		if !ac.IsAirborne() || ac.Squawk == 0o1200 {
			continue
		}

		// Look up the FP from ERAM's own store (authoritative copy)
		acid := ACID(ac.ADSBCallsign)
		fp, hasFP := ec.FlightPlans[acid]
		if !hasFP {
			continue
		}

		gs := ac.GS()
		if gs < 50 {
			continue // Too slow for meaningful ETA
		}

		pos := ac.Position()
		nmPerLong := ac.NmPerLongitude()

		// Walk upcoming waypoints and check if any is an entry fix for a child TRACON
		for _, wp := range ac.Nav.Waypoints {
			eta := wp.ETA(pos, gs, nmPerLong)
			if eta > fpDistributionLookahead {
				break // Beyond lookahead window
			}

			fixName := wp.Fix
			if fixName == "" {
				continue
			}

			// Check each child TRACON to see if this fix is relevant
			for traconCode, childSTARS := range ec.Children {
				// Skip if child already has this FP (unassociated or auto-acquired)
				if childSTARSHasFP(childSTARS, fp.ACID) {
					continue
				}

				// Check if the fix is an entry fix in the child's fix pairs
				if ec.isEntryFixForTRACON(fixName, childSTARS) {
					ec.SendMessage(s.nasNet, NASMessage{
						Type:       MsgFP,
						ToFacility: traconCode,
						ACID:       fp.ACID,
						Timestamp:  s.State.SimTime,
						FlightPlan: fpForMessage(fp),
					})
				}
			}
		}

		// STAR-based prediction: if the FP has a STAR, check if any
		// child TRACON has an airport that uses this STAR.
		if ac.STAR != "" {
			for traconCode, childSTARS := range ec.Children {
				// Skip if child already has this FP (unassociated or auto-acquired)
				if childSTARSHasFP(childSTARS, fp.ACID) {
					continue
				}
				if ec.starServesTracon(ac.STAR, childSTARS) {
					ec.SendMessage(s.nasNet, NASMessage{
						Type:       MsgFP,
						ToFacility: traconCode,
						ACID:       fp.ACID,
						Timestamp:  s.State.SimTime,
						FlightPlan: fpForMessage(fp),
					})
				}
			}
		}
	}
}

// childSTARSHasFP returns true if the child STARS already has this FP,
// either as an unassociated flight plan or as part of an existing track
// (auto-acquired by squawk match).
func childSTARSHasFP(sc *STARSComputer, acid ACID) bool {
	if _, exists := sc.FlightPlans[acid]; exists {
		return true
	}
	if _, exists := sc.Tracks[acid]; exists {
		return true
	}
	return false
}

// isEntryFixForTRACON checks if a fix name is listed as an entry fix
// in the TRACON's fix pair definitions.
func (ec *ERAMComputer) isEntryFixForTRACON(fix string, sc *STARSComputer) bool {
	for _, fp := range sc.FixPairs {
		if fp.EntryFix == fix {
			return true
		}
	}
	return false
}

// starServesTracon checks if a STAR name serves any airport in the TRACON.
func (ec *ERAMComputer) starServesTracon(star string, sc *STARSComputer) bool {
	for _, icao := range sc.Airports {
		if ap, ok := av.DB.Airports[icao]; ok {
			if _, hasStar := ap.STARs[star]; hasStar {
				return true
			}
		}
	}
	return false
}

// SendMessage sends a NAS message from this ERAM computer.
// ERAM can send to peers and children directly.
func (ec *ERAMComputer) SendMessage(net *NASNetwork, msg NASMessage) {
	msg.FromFacility = ec.Identifier
	net.Route(msg)
}

func makeSTARSComputer(id string) *STARSComputer {
	sc := &STARSComputer{
		Identifier:       id,
		FlightPlans:      make(map[ACID]*NASFlightPlan),
		Tracks:           make(map[ACID]*FacilityTrack),
		AvailableIndices: make([]int, 99),
	}

	for i := range 99 {
		sc.AvailableIndices[i] = i + 1
	}
	return sc
}

// SendMessage sends a NAS message from this STARS computer.
// In the real NAS, STARS routes through its parent ERAM. Here we route
// directly via the network layer for simplicity, since the NASNetwork
// can deliver to any registered facility.
func (sc *STARSComputer) SendMessage(net *NASNetwork, msg NASMessage) {
	msg.FromFacility = sc.Identifier
	net.Route(msg)
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
	for acid, fp := range sc.FlightPlans {
		if !fp.DeleteTime.IsZero() && s.State.SimTime.After(fp.DeleteTime) {
			s.deleteFlightPlan(fp)
			delete(sc.FlightPlans, acid)
		}
	}

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
			sc.FlightPlans[fp.ACID] = fp
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
					// RemoteEnroute FP received via NAS message from ERAM:
					// associate immediately so the track shows the center scope char.
					if fp.PlanType == RemoteEnroute {
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
					if fp.PlanType == RemoteEnroute && fp.TrackingController == "" {
						// FP from ERAM doesn't include control state.
						// STARS only knows the FP came from center, so
						// OwningTCW should reflect "center" (scope char C).
						fp.OwningTCW = s.State.CenterTCW()
					} else if fp.TrackingController != "" {
						fp.OwningTCW = s.tcwForPosition(fp.TrackingController)
					}

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

					// Create a FacilityTrack so the track table reflects this acquisition.
					// For RemoteEnroute FPs (received from center via NAS), the center
					// owns the track until a TI assigns a local controller.
					ownerFacility := sc.Identifier
					owner := fp.TrackingController
					if fp.PlanType == RemoteEnroute && fp.TrackingController == "" {
						if sc.ParentERAM != nil {
							ownerFacility = sc.ParentERAM.Identifier
						}
					}
					if owner == "" && fp.HandoffController != "" {
						owner = fp.HandoffController
					}
					sc.Tracks[fp.ACID] = &FacilityTrack{
						ACID:          fp.ACID,
						CoupledFP:     fp,
						Owner:         owner,
						OwnerFacility: ownerFacility,
						HandoffState:  TrackHandoffNone,
					}

					// Also create a track at parent ERAM for RemoteEnroute FPs.
					// In the real NAS, ERAM tracks all IFR aircraft in its airspace;
					// this mirrors that by giving ERAM a track when STARS acquires
					// a center-originated flight plan.
					if fp.PlanType == RemoteEnroute && sc.ParentERAM != nil {
						if _, eramHasTrack := sc.ParentERAM.Tracks[fp.ACID]; !eramHasTrack {
							eramFP := sc.ParentERAM.FlightPlans[fp.ACID] // ERAM's own copy
							sc.ParentERAM.Tracks[fp.ACID] = &FacilityTrack{
								ACID:          fp.ACID,
								CoupledFP:     eramFP,
								Owner:         ControlPosition(""),
								OwnerFacility: sc.ParentERAM.Identifier,
								HandoffState:  TrackHandoffNone,
							}
						}
					}

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

	// Sync FacilityTrack state with NASFlightPlan handoff state.
	// This covers intra-facility handoffs (where no NAS messages are sent).
	// Cross-facility handoffs are handled by TI/TN messages and should NOT
	// be overwritten here.
	for _, track := range sc.Tracks {
		fp := track.CoupledFP
		if fp == nil {
			continue
		}
		// If HandoffController was set and track doesn't already reflect it
		if fp.HandoffController != "" && track.HandoffState == TrackHandoffNone {
			track.Owner = fp.TrackingController
			track.OwnerFacility = sc.Identifier
			track.HandoffState = TrackHandoffOffered
		}
		// If handoff was accepted (HandoffController cleared)
		// Only update for intra-facility handoffs (owner is still at this facility).
		// Cross-facility handoffs are updated by handleTN.
		if fp.HandoffController == "" && track.HandoffState == TrackHandoffOffered &&
			track.OwnerFacility == sc.Identifier {
			track.Owner = fp.TrackingController
			track.HandoffState = TrackHandoffAccepted
		}
	}
}

func (sc *STARSComputer) lookupFlightPlanByACID(acid ACID) *NASFlightPlan {
	return sc.FlightPlans[acid]
}

func (sc *STARSComputer) takeFlightPlanByACID(acid ACID) *NASFlightPlan {
	fp, ok := sc.FlightPlans[acid]
	if ok {
		delete(sc.FlightPlans, acid)
	}
	return fp
}

func (sc *STARSComputer) lookupFlightPlanBySquawk(sq av.Squawk) *NASFlightPlan {
	for _, fp := range sc.FlightPlans {
		if fp.AssignedSquawk == sq {
			return fp
		}
	}
	return nil
}

func (sc *STARSComputer) takeFlightPlanBySquawk(sq av.Squawk) *NASFlightPlan {
	for acid, fp := range sc.FlightPlans {
		if fp.AssignedSquawk == sq {
			delete(sc.FlightPlans, acid)
			return fp
		}
	}
	return nil
}

func (sc *STARSComputer) CreateFlightPlan(fp NASFlightPlan) (NASFlightPlan, error) {
	if _, exists := sc.FlightPlans[fp.ACID]; exists {
		return fp, ErrDuplicateACID
	}

	fp.ListIndex = sc.getListIndex()
	sc.FlightPlans[fp.ACID] = &fp

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

// FlightPlanSlice returns the flight plans as a slice for backward
// compatibility with code that iterates over all unassociated flight plans.
func (sc *STARSComputer) FlightPlanSlice() []*NASFlightPlan {
	result := make([]*NASFlightPlan, 0, len(sc.FlightPlans))
	for _, fp := range sc.FlightPlans {
		result = append(result, fp)
	}
	return result
}

///////////////////////////////////////////////////////////////////////////
// NAS Debug Data - serializable snapshots for the debug GUI

// NASDebugData is a serializable snapshot of the NAS network state.
type NASDebugData struct {
	ERAMFacilities  []ERAMDebugInfo
	STARSFacilities []STARSDebugInfo
}

type ERAMDebugInfo struct {
	Identifier     string
	FlightPlans    []FPDebugInfo
	Tracks         []TrackDebugInfo
	RecentMessages []InboxDebugInfo
	InboxCount     int
	Children       []string
	Peers          []string
}

type STARSDebugInfo struct {
	Identifier     string
	FlightPlans    []FPDebugInfo
	Tracks         []TrackDebugInfo
	RecentMessages []InboxDebugInfo
	InboxCount     int
	ParentERAM     string
	Airports       []string
	FixPairCount   int
}

type FPDebugInfo struct {
	ACID           string
	Squawk         string
	AircraftType   string
	Altitude       string // AssignedAlt or RequestedAlt, whichever is non-zero
	Route          string
	ArrivalAirport string
	PlanType       string
	ReceivedFrom   string // Which facility sent this FP (empty = locally created)
}

type TrackDebugInfo struct {
	ACID               string
	Squawk             string
	AircraftType       string
	Owner              string
	OwnerFacility      string
	TrackingController string
	OwningTCW          string
	HandoffController  string
	HandoffState       string
}

type InboxDebugInfo struct {
	Type         string
	FromFacility string
	ToFacility   string
	ACID         string
	Age          string // e.g. "2.1s"
}

func planTypeString(pt NASFlightPlanType) string {
	switch pt {
	case RemoteEnroute:
		return "RemoteEnroute"
	case RemoteNonEnroute:
		return "RemoteNonEnroute"
	case LocalEnroute:
		return "LocalEnroute"
	case LocalNonEnroute:
		return "LocalNonEnroute"
	default:
		return "Unknown"
	}
}

func handoffStateString(hs TrackHandoffState) string {
	switch hs {
	case TrackHandoffNone:
		return "None"
	case TrackHandoffOffered:
		return "Offered"
	case TrackHandoffAccepted:
		return "Accepted"
	default:
		return fmt.Sprintf("Unknown(%d)", int(hs))
	}
}

func makeFPDebugInfo(fp *NASFlightPlan) FPDebugInfo {
	alt := fp.AssignedAltitude
	if alt == 0 {
		alt = fp.RequestedAltitude
	}
	altStr := ""
	if alt > 0 {
		altStr = fmt.Sprintf("%d", alt)
	}
	from := fp.ReceivedFrom
	if from == "" {
		from = "local"
	}
	return FPDebugInfo{
		ACID:           string(fp.ACID),
		Squawk:         fp.AssignedSquawk.String(),
		AircraftType:   fp.AircraftType,
		Altitude:       altStr,
		Route:          fp.Route,
		ArrivalAirport: fp.ArrivalAirport,
		PlanType:       planTypeString(fp.PlanType),
		ReceivedFrom:   from,
	}
}

func makeInboxDebugInfo(tm TimestampedMessage, simTime time.Time) InboxDebugInfo {
	age := simTime.Sub(tm.ReceivedAt)
	return InboxDebugInfo{
		Type:         string(tm.Msg.Type),
		FromFacility: tm.Msg.FromFacility,
		ToFacility:   tm.Msg.ToFacility,
		ACID:         string(tm.Msg.ACID),
		Age:          fmt.Sprintf("%.1fs", age.Seconds()),
	}
}

func makeTrackDebugInfo(t *FacilityTrack) TrackDebugInfo {
	info := TrackDebugInfo{
		ACID:          string(t.ACID),
		Owner:         string(t.Owner),
		OwnerFacility: t.OwnerFacility,
		HandoffState:  handoffStateString(t.HandoffState),
	}
	if t.CoupledFP != nil {
		info.Squawk = t.CoupledFP.AssignedSquawk.String()
		info.AircraftType = t.CoupledFP.AircraftType
		info.TrackingController = string(t.CoupledFP.TrackingController)
		info.OwningTCW = string(t.CoupledFP.OwningTCW)
		info.HandoffController = string(t.CoupledFP.HandoffController)
	}
	return info
}

// GetNASDebugData returns a serializable snapshot of the network state.
func (n *NASNetwork) GetNASDebugData(simTime time.Time) NASDebugData {
	data := NASDebugData{}

	for id, ec := range n.ERAMComputers {
		info := ERAMDebugInfo{
			Identifier: id,
			InboxCount: len(ec.Inbox),
		}
		for _, fp := range ec.FlightPlans {
			info.FlightPlans = append(info.FlightPlans, makeFPDebugInfo(fp))
		}
		for _, t := range ec.Tracks {
			info.Tracks = append(info.Tracks, makeTrackDebugInfo(t))
		}
		for child := range ec.Children {
			info.Children = append(info.Children, child)
		}
		for peer := range ec.Peers {
			info.Peers = append(info.Peers, peer)
		}
		for _, tm := range ec.RecentInbox {
			info.RecentMessages = append(info.RecentMessages, makeInboxDebugInfo(tm, simTime))
		}
		sort.Strings(info.Children)
		sort.Strings(info.Peers)
		sort.Slice(info.FlightPlans, func(i, j int) bool {
			return info.FlightPlans[i].ACID < info.FlightPlans[j].ACID
		})
		sort.Slice(info.Tracks, func(i, j int) bool {
			return info.Tracks[i].ACID < info.Tracks[j].ACID
		})
		data.ERAMFacilities = append(data.ERAMFacilities, info)
	}

	for id, sc := range n.STARSComputers {
		info := STARSDebugInfo{
			Identifier:   id,
			InboxCount:   len(sc.Inbox),
			Airports:     slices.Clone(sc.Airports),
			FixPairCount: len(sc.FixPairs),
		}
		if sc.ParentERAM != nil {
			info.ParentERAM = sc.ParentERAM.Identifier
		}
		// Unassociated FPs still in the STARS pool
		for _, fp := range sc.FlightPlans {
			info.FlightPlans = append(info.FlightPlans, makeFPDebugInfo(fp))
		}
		for _, t := range sc.Tracks {
			info.Tracks = append(info.Tracks, makeTrackDebugInfo(t))
		}
		for _, tm := range sc.RecentInbox {
			info.RecentMessages = append(info.RecentMessages, makeInboxDebugInfo(tm, simTime))
		}
		sort.Slice(info.FlightPlans, func(i, j int) bool {
			return info.FlightPlans[i].ACID < info.FlightPlans[j].ACID
		})
		sort.Slice(info.Tracks, func(i, j int) bool {
			return info.Tracks[i].ACID < info.Tracks[j].ACID
		})
		sort.Strings(info.Airports)
		data.STARSFacilities = append(data.STARSFacilities, info)
	}

	sort.Slice(data.ERAMFacilities, func(i, j int) bool {
		return data.ERAMFacilities[i].Identifier < data.ERAMFacilities[j].Identifier
	})
	sort.Slice(data.STARSFacilities, func(i, j int) bool {
		return data.STARSFacilities[i].Identifier < data.STARSFacilities[j].Identifier
	})

	return data
}
