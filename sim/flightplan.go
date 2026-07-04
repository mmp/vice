// sim/flightplan.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

///////////////////////////////////////////////////////////////////////////
// NASFlightPlan

type NASFlightPlan struct {
	ACID                  ACID
	CID                   string
	EntryFix              string
	ExitFix               string
	ArrivalAirport        string // Technically not a string, but until the NAS system is fully integrated, we'll need this.
	ExitFixIsIntermediate bool
	Rules                 av.FlightRules
	CoordinationTime      Time
	PlanType              NASFlightPlanType

	AssignedSquawk av.Squawk

	TrackingController  ControlPosition // Who has the radar track
	HandoffController   ControlPosition // Handoff offered but not yet accepted
	LastLocalController ControlPosition // (May be the current controller.)
	OwningTCW           TCW             // TCW that owns this track

	AircraftCount   int
	AircraftType    string
	EquipmentSuffix string

	TypeOfFlight av.TypeOfFlight

	AssignedAltitude      int
	PerceivedAssigned     int // what the previous controller would put into the hard alt, even though the aircraft is descending via a STAR.
	InterimAlt            int
	InterimType           int
	AltitudeBlock         [2]int
	ControllerReportedAlt int
	VFROTP                bool

	RequestedAltitude     int
	PilotReportedAltitude int

	Scratchpad          string
	SecondaryScratchpad string

	PriorScratchpad          string
	PriorSecondaryScratchpad string

	RNAV bool

	Location math.Point2LL
	Route    string

	PointOutHistory             []ControlPosition
	InhibitModeCAltitudeDisplay bool
	SPCOverride                 string
	DisableMSAW                 bool
	DisableCA                   bool
	MCISuppressedCode           av.Squawk
	GlobalLeaderLineDirection   *math.CardinalOrdinalDirection
	QuickFlightPlan             bool
	HoldState                   bool
	Suspended                   bool
	CoastSuspendIndex           int

	// FIXME: the following are all used internally by NAS code. It's
	// convenient to have them here but this stuff should just be managed
	// internally there.
	ListIndex int

	// First controller in the local facility to get the track: used both
	// for /ho and for departures
	InboundHandoffController ControlPosition

	CoordinationFix     string
	ContainedFacilities []string
	RedirectedHandoff   RedirectedHandoff

	InhibitACTypeDisplay      bool
	ForceACTypeDisplayEndTime Time
	CWTCategory               string

	// After fps are dropped, we hold on to them for a bit before they're
	// actually deleted.
	DeleteTime Time

	// Used so that such FPs can associate regardless of acquisition filters.
	ManuallyCreated bool

	// FDAM region membership state, keyed by region ID.
	FDAMState map[string]*FDAMTrackState `json:"-"`

	// Flight strip fields
	StripCID         int             // numeric 000-999, allocated server-side
	StripAnnotations [9]string       // 3x3 annotation grid
	StripOwner       ControlPosition // which TCP position has this strip (empty = no strip)
}

func (fp *NASFlightPlan) AddPointOutHistory(tcp TCP) {
	if len(fp.PointOutHistory) >= 20 {
		fp.PointOutHistory = fp.PointOutHistory[:19]
	}
	fp.PointOutHistory = append([]TCP{tcp}, fp.PointOutHistory...)
}

// DataBlockAltitude returns the altitude shown in the ERAM data block — the
// "hard altitude or interim altitude" referenced in ERAM conflict alert and
// other altitude-cap logic. Priority: InterimAlt (TODO: confirm interim really
// wins) > AssignedAltitude > PerceivedAssigned. Returns 0 if none are set.
// TODO: AltitudeBlock once block clearances are wired up.
func (fp *NASFlightPlan) DataBlockAltitude() int {
	if fp.InterimAlt > 0 {
		return fp.InterimAlt
	}
	if fp.AssignedAltitude != 0 {
		return fp.AssignedAltitude
	}
	return fp.PerceivedAssigned
}

type NASFlightPlanType int

// Flight plan types (STARS)
const (
	UnknownFlightPlanType NASFlightPlanType = iota

	// Flight plan received from a NAS ARTCC.  This is a flight plan that
	// has been sent over by an overlying ERAM facility.
	RemoteEnroute

	// Flight plan received from an adjacent terminal facility This is a
	// flight plan that has been sent over by another STARS facility.
	RemoteNonEnroute

	// VFR interfacility flight plan entered locally for which the NAS
	// ARTCC has not returned a flight plan This is a flight plan that is
	// made by a STARS facility that gets a NAS code.
	LocalEnroute

	// Flight plan entered by TCW or flight plan from an adjacent terminal
	// that has been handed off to this STARS facility This is a flight
	// plan that is made at a STARS facility and gets a local code.
	LocalNonEnroute
)

type ACID string

func (fp *NASFlightPlan) Update(spec FlightPlanSpecifier, sim *Sim) (err error) {
	if spec.ACID.IsSet {
		fp.ACID = spec.ACID.Get()
	}
	if spec.PlanType.IsSet { // do before exit fix
		fp.PlanType = spec.PlanType.Get()
	}
	if spec.EntryFix.IsSet {
		fp.EntryFix = spec.EntryFix.Get()
	}
	if spec.ExitFix.IsSet {
		fp.ExitFix = spec.ExitFix.Get()
		fp.ExitFixIsIntermediate = spec.ExitFixIsIntermediate.GetOr(false)

		// Exit fix is shown in scratchpad for NAS flight plans.
		if fp.PlanType == LocalEnroute && spec.ExitFix.IsSet {
			fp.Scratchpad = spec.ExitFix.Get()
		}
	}
	if spec.Rules.IsSet {
		if spec.Rules.Get() == fp.Rules {
			// same as current, so clears flight rules, which in turn implies IFR
			fp.Rules = av.FlightRulesIFR
		} else {
			fp.Rules = spec.Rules.Get()
		}
		if !spec.DisableMSAW.IsSet {
			// If MSAW disable isn't set explicitly, it's set based on the updated flight rules.
			fp.DisableMSAW = fp.Rules != av.FlightRulesIFR
		}
	}
	if spec.CoordinationTime.IsSet {
		fp.CoordinationTime = spec.CoordinationTime.Get()
	}
	if spec.ImplicitSquawkAssignment.IsSet {
		fp.AssignedSquawk = spec.ImplicitSquawkAssignment.Get()
	} else if spec.SquawkAssignment.IsSet {
		var rules av.FlightRules
		fp.AssignedSquawk, rules, err = assignCode(spec.SquawkAssignment, fp.PlanType, fp.Rules, sim.LocalCodePool,
			sim.ERAMComputer.SquawkCodePool)
		if !spec.Rules.IsSet {
			// Only take the rules from the pool if no rules were given in spec.
			fp.Rules = rules
			// Disable MSAW for VFRs
			if rules != av.FlightRulesIFR && !spec.DisableMSAW.IsSet {
				fp.DisableMSAW = true
			}
		}
	}
	if spec.InterimAlt.IsSet {
		fp.InterimAlt = spec.InterimAlt.Get()
	}
	if spec.InterimType.IsSet {
		fp.InterimType = spec.InterimType.Get()
	}

	if spec.AircraftType.IsSet {
		fp.AircraftType = spec.AircraftType.Get()
		fp.AircraftCount = spec.AircraftCount.GetOr(1)
		fp.EquipmentSuffix = spec.EquipmentSuffix.GetOr("")

		if perf, ok := av.DB.AircraftPerformance[fp.AircraftType]; ok {
			fp.CWTCategory = perf.Category.CWT
		} else {
			fp.CWTCategory = ""
		}
	}
	if spec.TypeOfFlight.IsSet {
		fp.TypeOfFlight = spec.TypeOfFlight.Get()
	}
	if spec.TrackingController.IsSet {
		fp.TrackingController = spec.TrackingController.Get()
		fp.OwningTCW = sim.tcwForPosition(fp.TrackingController)
	}
	if spec.AssignedAltitude.IsSet {
		fp.AssignedAltitude = spec.AssignedAltitude.Get()
	}
	if spec.RequestedAltitude.IsSet {
		fp.RequestedAltitude = spec.RequestedAltitude.Get()
	}
	if spec.PilotReportedAltitude.IsSet {
		fp.PilotReportedAltitude = spec.PilotReportedAltitude.Get()
	}
	if spec.Scratchpad.IsSet {
		if spec.Scratchpad.Get() == "" {
			fp.Scratchpad = ""
			fp.PriorScratchpad = ""
		} else if fp.Scratchpad == spec.Scratchpad.Get() {
			fp.Scratchpad = fp.PriorScratchpad
		} else {
			fp.PriorScratchpad = fp.Scratchpad
			fp.Scratchpad = spec.Scratchpad.Get()
		}
	}
	if spec.SecondaryScratchpad.IsSet {
		if spec.SecondaryScratchpad.Get() == "" {
			fp.SecondaryScratchpad = ""
			fp.PriorSecondaryScratchpad = ""
		} else if fp.SecondaryScratchpad == spec.SecondaryScratchpad.Get() {
			fp.SecondaryScratchpad = fp.PriorSecondaryScratchpad
		} else {
			fp.PriorSecondaryScratchpad = fp.SecondaryScratchpad
			fp.SecondaryScratchpad = spec.SecondaryScratchpad.Get()
		}
	}
	if spec.RNAV.IsSet {
		fp.RNAV = spec.RNAV.Get()
	}
	if spec.RNAVToggle.IsSet && spec.RNAVToggle.Get() {
		fp.RNAV = !fp.RNAV
	}
	if spec.Location.IsSet {
		fp.Location = spec.Location.Get()
	}
	if spec.PointOutHistory.IsSet {
		fp.PointOutHistory = util.MapSlice(spec.PointOutHistory.Get(), func(s string) ControlPosition { return ControlPosition(s) })
	}
	if spec.InhibitModeCAltitudeDisplay.IsSet {
		fp.InhibitModeCAltitudeDisplay = spec.InhibitModeCAltitudeDisplay.Get()
	}
	if spec.SPCOverride.IsSet {
		fp.SPCOverride = spec.SPCOverride.Get()
	}
	if spec.DisableMSAW.IsSet {
		fp.DisableMSAW = spec.DisableMSAW.Get()
	}
	if spec.DisableCA.IsSet {
		fp.DisableCA = spec.DisableCA.Get()
	}
	if spec.MCISuppressedCode.IsSet {
		fp.MCISuppressedCode = spec.MCISuppressedCode.Get()
	}
	if spec.GlobalLeaderLineDirection.IsSet {
		fp.GlobalLeaderLineDirection = spec.GlobalLeaderLineDirection.Get()
	}
	if spec.QuickFlightPlan.IsSet {
		fp.QuickFlightPlan = spec.QuickFlightPlan.Get()
	}
	if spec.HoldState.IsSet {
		fp.HoldState = spec.HoldState.Get()
	}
	if spec.Suspended.IsSet {
		fp.Suspended = spec.Suspended.Get()
	}
	if spec.CoastSuspendIndex.IsSet {
		fp.CoastSuspendIndex = spec.CoastSuspendIndex.Get()
	}
	if spec.InhibitACTypeDisplay.IsSet {
		fp.InhibitACTypeDisplay = spec.InhibitACTypeDisplay.Get()
	}
	if spec.ForceACTypeDisplayEndTime.IsSet {
		fp.ForceACTypeDisplayEndTime = spec.ForceACTypeDisplayEndTime.Get()
	}
	return
}

///////////////////////////////////////////////////////////////////////////
// FlightPlanSpecifier

type FlightPlanSpecifier struct {
	ACID                  util.Optional[ACID]
	EntryFix              util.Optional[string]
	ExitFix               util.Optional[string]
	ExitFixIsIntermediate util.Optional[bool]
	Rules                 util.Optional[av.FlightRules]
	CoordinationTime      util.Optional[Time]
	PlanType              util.Optional[NASFlightPlanType]

	SquawkAssignment         util.Optional[string]
	ImplicitSquawkAssignment util.Optional[av.Squawk] // only used when taking the track's current code

	TrackingController util.Optional[ControlPosition]

	AircraftCount   util.Optional[int]
	AircraftType    util.Optional[string]
	EquipmentSuffix util.Optional[string]

	TypeOfFlight util.Optional[av.TypeOfFlight]

	AssignedAltitude      util.Optional[int]
	InterimAlt            util.Optional[int]
	InterimType           util.Optional[int]
	AltitudeBlock         util.Optional[[2]int]
	ControllerReportedAlt util.Optional[int]
	VFROTP                util.Optional[bool]
	RequestedAltitude     util.Optional[int]
	PilotReportedAltitude util.Optional[int]

	Scratchpad          util.Optional[string]
	SecondaryScratchpad util.Optional[string]

	RNAV       util.Optional[bool]
	RNAVToggle util.Optional[bool]

	Location util.Optional[math.Point2LL]

	PointOutHistory             util.Optional[[]string]
	InhibitModeCAltitudeDisplay util.Optional[bool]
	SPCOverride                 util.Optional[string]
	DisableMSAW                 util.Optional[bool]
	DisableCA                   util.Optional[bool]
	MCISuppressedCode           util.Optional[av.Squawk]
	GlobalLeaderLineDirection   util.Optional[*math.CardinalOrdinalDirection]
	QuickFlightPlan             util.Optional[bool]
	HoldState                   util.Optional[bool]
	Suspended                   util.Optional[bool]
	CoastSuspendIndex           util.Optional[int]

	InhibitACTypeDisplay      util.Optional[bool]
	ForceACTypeDisplayEndTime util.Optional[Time]
}

func (s FlightPlanSpecifier) GetFlightPlan(localPool *av.LocalSquawkCodePool,
	nasPool *av.EnrouteSquawkCodePool) (NASFlightPlan, error) {
	sfp := NASFlightPlan{
		ACID:                  s.ACID.GetOr(""),
		EntryFix:              s.EntryFix.GetOr(""),
		ExitFix:               s.ExitFix.GetOr(""),
		ExitFixIsIntermediate: s.ExitFixIsIntermediate.GetOr(false),
		Rules:                 s.Rules.GetOr(av.FlightRulesVFR),
		CoordinationTime:      s.CoordinationTime.GetOr(Time{}),
		PlanType:              s.PlanType.GetOr(UnknownFlightPlanType),

		AircraftCount:   s.AircraftCount.GetOr(1),
		AircraftType:    s.AircraftType.GetOr(""),
		EquipmentSuffix: s.EquipmentSuffix.GetOr(""),

		TypeOfFlight:       s.TypeOfFlight.GetOr(av.FlightTypeUnknown),
		TrackingController: s.TrackingController.GetOr(""),

		AssignedAltitude:      s.AssignedAltitude.GetOr(0),
		RequestedAltitude:     s.RequestedAltitude.GetOr(0),
		PilotReportedAltitude: s.PilotReportedAltitude.GetOr(0),

		Scratchpad:          s.Scratchpad.GetOr(""),
		SecondaryScratchpad: s.SecondaryScratchpad.GetOr(""),

		RNAV: s.RNAV.GetOr(false),

		Location: s.Location.GetOr(math.Point2LL{}),

		PointOutHistory:             util.MapSlice(s.PointOutHistory.GetOr(nil), func(s string) ControlPosition { return ControlPosition(s) }),
		InhibitModeCAltitudeDisplay: s.InhibitModeCAltitudeDisplay.GetOr(false),
		SPCOverride:                 s.SPCOverride.GetOr(""),
		DisableMSAW:                 s.DisableMSAW.GetOr(false),
		DisableCA:                   s.DisableCA.GetOr(false),
		MCISuppressedCode:           s.MCISuppressedCode.GetOr(av.Squawk(0)),
		GlobalLeaderLineDirection:   s.GlobalLeaderLineDirection.GetOr(nil),
		QuickFlightPlan:             s.QuickFlightPlan.GetOr(false),
		HoldState:                   s.HoldState.GetOr(false),
		Suspended:                   s.Suspended.GetOr(false),
		CoastSuspendIndex:           s.CoastSuspendIndex.GetOr(0),

		InhibitACTypeDisplay:      s.InhibitACTypeDisplay.GetOr(false),
		ForceACTypeDisplayEndTime: s.ForceACTypeDisplayEndTime.GetOr(Time{}),

		ManuallyCreated: true, // Always for ones created via a fp specifier
	}

	if perf, ok := av.DB.AircraftPerformance[sfp.AircraftType]; ok {
		sfp.CWTCategory = perf.Category.CWT
	}

	// Handle beacon code assignment
	var err error
	if s.ImplicitSquawkAssignment.IsSet {
		sfp.AssignedSquawk = s.ImplicitSquawkAssignment.Get()
	} else {
		var rules av.FlightRules
		sfp.AssignedSquawk, rules, err = assignCode(s.SquawkAssignment, sfp.PlanType, sfp.Rules, localPool, nasPool)
		sfp.Rules = s.Rules.GetOr(rules) // explicit rules from caller override squawk code pool rules
	}

	if sfp.Rules != av.FlightRulesIFR && !s.DisableMSAW.IsSet {
		sfp.DisableMSAW = true
	}

	// Exit fix is shown in scratchpad for NAS flight plans.
	if sfp.PlanType == LocalEnroute && s.ExitFix.IsSet {
		sfp.Scratchpad = s.ExitFix.Get()
	}

	return sfp, err
}

// Merge incorporates set fields from other into s. Fields set in other take precedence.
func (s *FlightPlanSpecifier) Merge(other FlightPlanSpecifier) {
	// Rather than have to handle each FlightPlanSpecifier individually, we get (sort of) fancy and
	// use the reflect package to iterate over the members, check which are set through
	// util.Optional, and copy those that are. This isn't necessarily super performant, but we don't
	// need to do this too much.
	sVal := reflect.ValueOf(s).Elem()
	otherVal := reflect.ValueOf(other)
	typ := otherVal.Type()

	for i := range otherVal.NumField() {
		otherField := otherVal.Field(i)
		isSet := otherField.FieldByName("IsSet")
		if !isSet.IsValid() || isSet.Kind() != reflect.Bool {
			panic(fmt.Sprintf("FlightPlanSpecifier field %q is not a util.Optional", typ.Field(i).Name))
		}
		if isSet.Bool() {
			sVal.Field(i).Set(otherField)
		}
	}
}

func assignCode(assignment util.Optional[string], planType NASFlightPlanType, rules av.FlightRules,
	localPool *av.LocalSquawkCodePool, nasPool *av.EnrouteSquawkCodePool) (av.Squawk, av.FlightRules, error) {
	if planType == LocalEnroute {
		// Squawk assignment is either empty or a straight up code (for a quick flight plan, 5-141)
		if !assignment.IsSet || assignment.Get() == "" {
			sq, err := nasPool.Get(rand.Make())
			return sq, rules, err
		} else {
			sq, err := av.ParseSquawk(assignment.Get())
			if err == nil {
				nasPool.Take(sq)
			}
			return sq, rules, err
		}
	} else {
		return localPool.Get(assignment.GetOr(""), rules, rand.Make())
	}
}

///////////////////////////////////////////////////////////////////////////

func (s *Sim) CreateFlightPlan(tcw TCW, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	fp, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
	if err != nil {
		return err
	}

	fp.OwningTCW = tcw

	// Interfacility VFR plans (5.5.13) intentionally duplicate the ACID of an
	// existing associated track, so skip the associated-aircraft check for them.
	isInterfacilityVFR := fp.PlanType == LocalEnroute && fp.Rules == av.FlightRulesVFR
	if !isInterfacilityVFR {
		if util.SeqContainsFunc(maps.Values(s.Aircraft),
			func(ac *Aircraft) bool { return ac.IsAssociated() && ac.NASFlightPlan.ACID == fp.ACID }) {
			return ErrDuplicateACID
		}
	}
	if slices.ContainsFunc(s.STARSComputer.FlightPlans,
		func(fp2 *NASFlightPlan) bool { return fp.ACID == fp2.ACID }) {
		return ErrDuplicateACID
	}

	fp, err = s.STARSComputer.CreateFlightPlan(fp)

	s.publish()

	if err == nil {
		err = s.postCheckFlightPlanSpecifier(spec)
	}

	return err
}

// CreateInterfacilityVFR creates a NAS VFR flight plan from an existing
// associated local VFR track (5.5.13). The new plan gets a NAS beacon code
// and will auto-associate with the track after a delay, creating a beacon
// mismatch until the pilot squawks the new code.
func (s *Sim) CreateInterfacilityVFR(tcw TCW, acid ACID, isIntermediate bool, requestedAlt int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	fp, ac, _ := s.getFlightPlanForACID(acid)
	if ac == nil || !ac.IsAssociated() {
		return av.ErrNoAircraftForCallsign
	}
	if fp.PlanType != LocalNonEnroute || fp.Rules != av.FlightRulesVFR {
		return ErrIllegalFunction
	}
	if fp.OwningTCW != tcw {
		return ErrIllegalFunction
	}
	if fp.AircraftType == "" {
		return ErrNoACType
	}
	if fp.Scratchpad == "" {
		return ErrNoScratchpad
	}

	var spec FlightPlanSpecifier
	spec.ACID.Set(fp.ACID)
	spec.Rules.Set(av.FlightRulesVFR)
	spec.PlanType.Set(LocalEnroute)
	spec.TypeOfFlight.Set(av.FlightTypeArrival)
	spec.AircraftType.Set(fp.AircraftType)
	spec.ExitFix.Set(fp.Scratchpad)
	spec.ExitFixIsIntermediate.Set(isIntermediate)
	spec.DisableMSAW.Set(true)
	spec.TrackingController.Set(fp.TrackingController)
	spec.CoordinationTime.Set(s.State.SimTime)
	if requestedAlt > 0 {
		spec.RequestedAltitude.Set(requestedAlt)
	}

	newFP, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
	if err != nil {
		return err
	}
	newFP.OwningTCW = tcw

	_, err = s.STARSComputer.CreateFlightPlan(newFP)

	s.publish()

	return err
}

// General checks both for create and modify; this returns errors that prevent fp creation.
func (s *Sim) preCheckFlightPlanSpecifier(spec *FlightPlanSpecifier) error {
	if spec.ACID.IsSet {
		acid := spec.ACID.Get()
		if !IsValidACID(string(acid)) {
			return ErrIllegalACID
		}
	}

	if spec.TrackingController.IsSet {
		tcp := spec.TrackingController.Get()
		// TODO: this will need to be more sophisticated with consolidation.
		if _, ok := s.State.Controllers[tcp]; !ok {
			return ErrUnknownController
		}
	}

	if spec.SquawkAssignment.IsSet {
		if str := spec.SquawkAssignment.Get(); len(str) == 4 {
			if sq, err := av.ParseSquawk(str); err == nil && s.LocalCodePool.IsReservedVFRCode(sq) {
				return ErrIllegalBeaconCode
			}
		}
	}

	// TODO: validate entry/exit fixes

	return nil
}

// General checks both for create and modify; this returns informational
// messages that don't prevent the fp from being created.
func (s *Sim) postCheckFlightPlanSpecifier(spec FlightPlanSpecifier) error {
	if spec.AircraftType.IsSet {
		if _, ok := av.DB.AircraftPerformance[spec.AircraftType.Get()]; !ok {
			return ErrIllegalACType
		}
	}

	return nil
}

func (s *Sim) ModifyFlightPlan(tcw TCW, acid ACID, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lastControlCommandTime = time.Now()

	fp, _, active := s.getFlightPlanForACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	// Can't set assigned altitude for non-associated tracks.
	if !active && spec.AssignedAltitude.IsSet {
		return ErrTrackIsNotActive
	}

	if !s.TCWCanModifyFlightPlan(tcw, fp) {
		return av.ErrOtherControllerHasTrack
	}

	if active {
		// Modify assigned
		if spec.EntryFix.IsSet || spec.ExitFix.IsSet || spec.CoordinationTime.IsSet {
			// These can only be set for non-active flight plans: 5-171
			return ErrTrackIsActive
		}

		if spec.GlobalLeaderLineDirection.IsSet {
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				ACID:                acid,
				FromController:      s.State.PrimaryPositionForTCW(tcw),
				LeaderLineDirection: spec.GlobalLeaderLineDirection.Get(),
			})
		}
	}

	fp.Update(spec, s)

	s.publish()

	return s.postCheckFlightPlanSpecifier(spec)
}

// Associate the specified flight plan with the track. Flight plan for ACID
// must not already exist.
func (s *Sim) AssociateFlightPlan(tcw TCW, callsign av.ADSBCallsign, spec FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if spec.QuickFlightPlan.IsSet && spec.QuickFlightPlan.Get() {
		base := s.State.FacilityAdaptation.FlightPlan.QuickACID
		acid := base + fmt.Sprintf("%02d", s.QuickFlightPlanIndex%100)
		spec.ACID.Set(ACID(acid))
		s.QuickFlightPlanIndex++
	}
	if !spec.ACID.IsSet {
		spec.ACID.Set(ACID(callsign))
	}

	if err := s.preCheckFlightPlanSpecifier(&spec); err != nil {
		return err
	}

	_, err := s.dispatchAircraftCommand(tcw, callsign,
		func(tcw TCW, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.IsAssociated() {
				return av.ErrOtherControllerHasTrack
			}
			if s.STARSComputer.lookupFlightPlanByACID(spec.ACID.Get()) != nil {
				return ErrDuplicateACID
			}

			fp, err := spec.GetFlightPlan(s.LocalCodePool, s.ERAMComputer.SquawkCodePool)
			if err != nil {
				return err
			}
			if _, err := s.STARSComputer.CreateFlightPlan(fp); err != nil {
				return err
			}

			return nil
		},
		func(tcw TCW, ac *Aircraft) av.CommandIntent {
			// Either the flight plan was passed in or fp was initialized  in the validation function.
			fp := s.STARSComputer.takeFlightPlanByACID(spec.ACID.Get())

			fp.Update(spec, s)

			ac.AssociateFlightPlan(fp)

			// Create a flight strip if one doesn't already exist.
			// Assign to TrackingController so the strip follows
			// the position if consolidation changes.
			if shouldCreateFlightStrip(fp) {
				owner := fp.TrackingController
				if owner == "" {
					owner = s.State.PrimaryPositionForTCW(tcw)
				}
				s.initFlightStrip(fp, owner)
			}

			s.eventStream.Post(Event{
				Type: FlightPlanAssociatedEvent,
				ACID: fp.ACID,
			})

			return nil
		})

	if err == nil {
		s.publish()

		err = s.postCheckFlightPlanSpecifier(spec)
	}
	return err
}

// Flight plan for acid must already exist; spec gives optional amendments.
func (s *Sim) ActivateFlightPlan(tcw TCW, callsign av.ADSBCallsign, acid ACID, spec *FlightPlanSpecifier) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Validate the target aircraft BEFORE taking the flight plan
	// to avoid orphaning it if the target is invalid.
	ac, ok := s.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	if ac.IsAssociated() {
		return ErrTrackIsActive
	}

	fp := s.STARSComputer.takeFlightPlanByACID(acid)
	if fp == nil {
		return ErrNoMatchingFlightPlan
	}
	if spec != nil {
		fp.Update(*spec, s)
	}

	s.lastControlCommandTime = time.Now()

	ac.AssociateFlightPlan(fp)

	s.eventStream.Post(Event{
		Type: FlightPlanAssociatedEvent,
		ACID: fp.ACID,
	})

	s.publish()

	return nil
}

func (s *Sim) DeleteFlightPlan(tcw TCW, acid ACID) (err error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	defer func() {
		if err == nil {
			s.publish()
		}
	}()

	s.lastControlCommandTime = time.Now()

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			if s.TCWCanModifyTrack(tcw, ac.NASFlightPlan) {
				fp := ac.DisassociateFlightPlan()
				fp.DeleteTime = s.State.SimTime.Add(4 * time.Minute)
				s.STARSComputer.FlightPlans = append(s.STARSComputer.FlightPlans, fp)
				return nil
			}
		}
	}

	if fp := s.STARSComputer.takeFlightPlanByACID(acid); fp != nil {
		s.deleteFlightPlan(fp)
		return nil
	}

	return ErrNoMatchingFlightPlan
}

func (s *Sim) deleteFlightPlan(fp *NASFlightPlan) {
	if s.CIDAllocator != nil && fp.CID != "" {
		s.CIDAllocator.Release(fp.CID)
	}
	if fp.StripOwner != "" {
		s.freeStripCID(fp.StripCID)
	}
	s.STARSComputer.returnListIndex(fp.ListIndex)
	if fp.PlanType == LocalNonEnroute {
		s.LocalCodePool.Return(fp.AssignedSquawk)
	} else {
		s.ERAMComputer.SquawkCodePool.Return(fp.AssignedSquawk)
	}
}
