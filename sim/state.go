// sim/state.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
)

// DynamicState is the subset of CommonState that is sent to the user once a second as the Sim
// executes. In this way, we limit network traffic to what is actually changing and only send the
// static information in CommonState once, when the Sim starts.
type DynamicState struct {
	GenerationIndex int

	CurrentConsolidation map[TCW]*TCPConsolidation

	SimTime time.Time // this is our fake time--accounting for pauses & simRate..

	METAR map[string]wx.METAR

	LaunchConfig LaunchConfig

	UserRestrictionAreas []av.RestrictionArea

	Paused  bool
	SimRate float32

	ATPAEnabled     bool                                   // True if ATPA is enabled system-wide
	ATPAVolumeState map[string]map[string]*ATPAVolumeState // airport -> volumeId -> state
}

type ATPAVolumeState struct {
	Disabled          bool
	Reduced25Disabled bool
}

// CommonState represents the sim state that is both used server side and client-side.
type CommonState struct {
	DynamicState

	Airports          map[string]*av.Airport
	Controllers       map[ControlPosition]*av.Controller
	DepartureAirports map[string]any
	ArrivalAirports   map[string]any
	Fixes             map[string]math.Point2LL
	VFRRunways        map[string]av.Runway // assume just one runway per airport

	ConfigurationId string // Short identifier for the configuration (from ControllerConfiguration.ConfigId)

	Airspace map[ControlPosition]map[string][]av.ControllerAirspaceVolume // position -> vol name -> definition

	DepartureRunways []DepartureRunway
	ArrivalRunways   []ArrivalRunway
	InboundFlows     map[string]*av.InboundFlow
	Emergencies      []Emergency

	Center                    math.Point2LL
	Range                     float32
	ScenarioDefaultVideoMaps  []string
	ScenarioDefaultVideoGroup string

	FacilityAdaptation FacilityAdaptation

	Facility          string
	MagneticVariation float32
	NmPerLongitude    float32
	PrimaryAirport    string

	SimDescription string

	VideoMapLibraryHash []byte
}

// DerivedState collects state used on the client-side that is derived from Sim state that is not
// shared with the client.
type DerivedState struct {
	Tracks                  map[av.ADSBCallsign]*Track
	UnassociatedFlightPlans []*NASFlightPlan // Unassociated ones, including unsupported DBs
	ReleaseDepartures       []ReleaseDeparture
}

type ReleaseDeparture struct {
	ADSBCallsign        av.ADSBCallsign
	DepartureAirport    string
	DepartureController ControlPosition
	Released            bool
	Squawk              av.Squawk
	ListIndex           int
	AircraftType        string
	Exit                string
}

// UserState is the simulation-related state provided to a user on the client-side.
type UserState struct {
	CommonState
	DerivedState
}

// StateUpdate encapsulates the simulation state data sent from server to client each tick.
type StateUpdate struct {
	DynamicState
	DerivedState
}

///////////////////////////////////////////////////////////////////////////

// makeDerivedState creates a DerivedState from the current simulation state.  It builds Tracks from
// Aircraft and gathers flight plan information from STARSComputer. This is called when preparing
// state updates for clients.
func makeDerivedState(s *Sim) DerivedState {
	ds := DerivedState{
		UnassociatedFlightPlans: s.starsComputer().FlightPlanSlice(),
	}

	// Build ReleaseDepartures from STARSComputer.HoldForRelease
	for _, ac := range s.starsComputer().HoldForRelease {
		fp, _, _ := s.getFlightPlanForACID(ACID(ac.ADSBCallsign))
		if fp == nil {
			s.lg.Warnf("%s: no flight plan for hold for release aircraft", string(ac.ADSBCallsign))
			continue
		}
		ds.ReleaseDepartures = append(ds.ReleaseDepartures,
			ReleaseDeparture{
				ADSBCallsign:        ac.ADSBCallsign,
				DepartureAirport:    "K" + fp.EntryFix,
				DepartureController: fp.InboundHandoffController,
				Released:            ac.Released,
				Squawk:              ac.Squawk,
				ListIndex:           fp.ListIndex,
				AircraftType:        fp.AircraftType,
				Exit:                fp.ExitFix,
			})
	}

	// Build Tracks from Aircraft
	ds.Tracks = make(map[av.ADSBCallsign]*Track)
	for callsign, ac := range util.SortedMap(s.Aircraft) {
		if !s.isRadarVisible(ac) {
			continue
		}

		var approach string
		if ac.Nav.Approach.Assigned != nil {
			approach = ac.Nav.Approach.Assigned.FullName
		}

		rt := Track{
			RadarTrack:                ac.GetRadarTrack(s.State.SimTime),
			FlightPlan:                ac.NASFlightPlan,
			ControllerFrequency:       ac.ControllerFrequency,
			DepartureAirport:          ac.FlightPlan.DepartureAirport,
			DepartureAirportElevation: ac.DepartureAirportElevation(),
			DepartureAirportLocation:  ac.DepartureAirportLocation(),
			ArrivalAirport:            ac.FlightPlan.ArrivalAirport,
			ArrivalAirportElevation:   ac.ArrivalAirportElevation(),
			ArrivalAirportLocation:    ac.ArrivalAirportLocation(),
			FiledRoute:                ac.FlightPlan.Route,
			FiledAltitude:             ac.FlightPlan.Altitude,
			OnExtendedCenterline:      ac.OnExtendedCenterline(0.2),
			OnApproach:                ac.OnApproach(false), /* don't check altitude */
			ClearedForApproach:        ac.Nav.Approach.Cleared,
			Approach:                  approach,
			Fixes:                     ac.GetSTTFixes(),
			SID:                       ac.SID,
			STAR:                      ac.STAR,
			MVAsApply:                 ac.MVAsApply(),
			HoldForRelease:            ac.HoldForRelease,
			MissingFlightPlan:         ac.MissingFlightPlan,
			ATPAVolume:                ac.ATPAVolume(),
			IsTentative:               s.State.SimTime.Sub(ac.FirstSeen) < 5*time.Second,
		}

		for _, wp := range ac.Nav.Waypoints {
			rt.Route = append(rt.Route, wp.Location)
		}

		ds.Tracks[callsign] = &rt
	}

	// Make up fake tracks for unsupported datablocks
	for _, fp := range s.starsComputer().FlightPlans {
		if fp.Location.IsZero() {
			continue
		}
		callsign := av.ADSBCallsign("__" + string(fp.ACID))
		ds.Tracks[callsign] = &Track{
			RadarTrack: av.RadarTrack{
				ADSBCallsign: callsign,
				Location:     fp.Location,
			},
			FlightPlan: fp,
		}
	}

	return ds
}

func newCommonState(config NewSimConfiguration, startTime time.Time, manifest *VideoMapManifest, model *wx.Model,
	metar map[string][]wx.METAR, lg *log.Logger) *CommonState {
	// Roll back the start time to account for prespawn
	startTime = startTime.Add(-initialSimSeconds * time.Second)

	ss := &CommonState{
		DynamicState: DynamicState{
			CurrentConsolidation: make(map[TCW]*TCPConsolidation),

			METAR: make(map[string]wx.METAR),

			LaunchConfig: config.LaunchConfig,

			SimRate: 1,
			SimTime: startTime,

			ATPAEnabled:     true,
			ATPAVolumeState: initATPAVolumeState(config.Airports),
		},

		Airports:    config.Airports,
		Controllers: maps.Clone(config.ControlPositions),
		Fixes:       config.Fixes,
		VFRRunways:  make(map[string]av.Runway),

		ConfigurationId: config.ControllerConfiguration.ConfigId,

		DepartureRunways: config.DepartureRunways,
		ArrivalRunways:   config.ArrivalRunways,
		InboundFlows:     config.InboundFlows,
		Emergencies:      config.Emergencies,

		Center:                    config.Center,
		Range:                     config.Range,
		ScenarioDefaultVideoMaps:  config.DefaultMaps,
		ScenarioDefaultVideoGroup: config.DefaultMapGroup,

		FacilityAdaptation: deep.MustCopy(config.FacilityAdaptation),

		Facility:          config.Facility,
		MagneticVariation: config.MagneticVariation,
		NmPerLongitude:    config.NmPerLongitude,
		PrimaryAirport:    config.PrimaryAirport,
		SimDescription:    config.Description,
	}

	// Grab initial METAR for each airport
	for ap, m := range metar {
		if len(m) > 0 {
			ss.METAR[ap] = m[0]
		}
	}

	if manifest != nil {
		ss.VideoMapLibraryHash, _ = manifest.Hash()
	}

	if len(config.ControllerAirspace) > 0 {
		ss.Airspace = make(map[ControlPosition]map[string][]av.ControllerAirspaceVolume)
		for ctrl, vnames := range config.ControllerAirspace {
			if _, ok := ss.Airspace[ctrl]; !ok {
				ss.Airspace[ctrl] = make(map[string][]av.ControllerAirspaceVolume)
			}
			for _, vname := range vnames {
				// Remap from strings provided in the scenario to the
				// actual volumes defined in the scenario group.
				ss.Airspace[ctrl][vname] = config.Airspace.Volumes[vname]
			}
		}
	}

	// Add the TFR restriction areas
	for _, tfr := range config.TFRs {
		ra := av.RestrictionAreaFromTFR(tfr)
		ss.FacilityAdaptation.RestrictionAreas = append(ss.FacilityAdaptation.RestrictionAreas, ra)
	}

	// Consolidate all positions to the root TCW
	defaultConsolidation := config.ControllerConfiguration.DefaultConsolidation
	rootTCP, _ := defaultConsolidation.RootPosition()
	rootCons := &TCPConsolidation{PrimaryTCP: rootTCP}
	ss.CurrentConsolidation[TCW(rootTCP)] = rootCons
	for _, tcp := range defaultConsolidation.AllPositions() {
		if tcp != rootTCP {
			rootCons.SecondaryTCPs = append(rootCons.SecondaryTCPs,
				SecondaryTCP{TCP: tcp, Type: ConsolidationFull})
			ss.CurrentConsolidation[TCW(tcp)] = &TCPConsolidation{}
		}
	}

	ss.DepartureAirports = make(map[string]any)
	for name := range ss.LaunchConfig.DepartureRates {
		ss.DepartureAirports[name] = nil
	}
	for name, ap := range ss.Airports {
		if ap.VFRRateSum() > 0 {
			ss.DepartureAirports[name] = nil

			ap := av.DB.Airports[name]
			windDir := model.Lookup(ap.Location, float32(ap.Elevation), startTime).WindDirection()
			if rwy, _ := ap.SelectBestRunway(windDir, ss.MagneticVariation); rwy != nil {
				ss.VFRRunways[name] = *rwy
			} else {
				lg.Errorf("%s: unable to find runway for VFRs", name)
			}
		}
	}

	ss.ArrivalAirports = make(map[string]any)
	for _, airportRates := range ss.LaunchConfig.InboundFlowRates {
		for name := range airportRates {
			if name != "overflights" {
				ss.ArrivalAirports[name] = nil
			}
		}
	}

	return ss
}

func initATPAVolumeState(airports map[string]*av.Airport) map[string]map[string]*ATPAVolumeState {
	result := make(map[string]map[string]*ATPAVolumeState)
	for icao, ap := range airports {
		if len(ap.ATPAVolumes) > 0 {
			result[icao] = make(map[string]*ATPAVolumeState)
			for volId := range ap.ATPAVolumes {
				result[icao][volId] = &ATPAVolumeState{}
			}
		}
	}
	return result
}

func (ss *CommonState) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := ss.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := ss.Fixes[s]; ok {
		return p, true
	} else if n, ok := av.DB.Navaids[s]; ok {
		return n.Location, ok
	} else if ap, ok := av.DB.Airports[s]; ok {
		return ap.Location, ok
	} else if f, ok := av.DB.Fixes[s]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else if ap, rwy, ok := strings.Cut(s, "/"); ok {
		if ap, ok := av.DB.Airports[ap]; ok {
			if idx := slices.IndexFunc(ap.Runways, func(r av.Runway) bool { return r.Id == rwy }); idx != -1 {
				return ap.Runways[idx].Threshold, true
			}
		}
	}
	return math.Point2LL{}, false
}

///////////////////////////////////////////////////////////////////////////
// CommonState methods for controller/consolidation management

func (ss *CommonState) IsExternalController(pos ControlPosition) bool {
	// Resolve consolidated positions to whoever currently controls them
	resolved := ss.ResolveController(pos)
	ctrl, ok := ss.Controllers[resolved]
	return ok && ctrl.FacilityIdentifier != ""
}

// CenterTCW returns the TCW for any ARTCC/center controller. It picks
// the first controller with FacilityIdentifier "C". Used when STARS
// receives an FP from ERAM and only knows the track belongs to center.
func (ss *CommonState) CenterTCW() TCW {
	for tcp, ctrl := range ss.Controllers {
		if ctrl.FacilityIdentifier == "C" {
			return TCW(tcp)
		}
	}
	return ""
}

func (ss *CommonState) IsLocalController(pos ControlPosition) bool {
	// Resolve consolidated positions to whoever currently controls them
	resolved := ss.ResolveController(pos)
	ctrl, ok := ss.Controllers[resolved]
	return ok && ctrl.FacilityIdentifier == ""
}

func (ss *CommonState) ResolveController(pos ControlPosition) ControlPosition {
	// Check who actually controls this position via consolidation.
	for _, cons := range ss.CurrentConsolidation {
		if cons.ControlsPosition(pos) {
			return cons.PrimaryTCP
		}
	}

	return pos
}

func (ss *CommonState) GetPositionsForTCW(tcw TCW) []ControlPosition {
	if cons, ok := ss.CurrentConsolidation[tcw]; ok {
		return cons.OwnedPositions()
	}
	return nil
}

// TCWControlsTrack returns true if the given TCW owns the track.
// Track ownership is determined by the OwningTCW field, which is set when
// the track is accepted/spawned and updated during full consolidation.
func (ss *CommonState) TCWControlsTrack(tcw TCW, track *Track) bool {
	return track != nil && track.IsAssociated() && track.FlightPlan.OwningTCW == tcw
}

// TCWControlsPosition returns true if the given TCW controls the specified position
// (either as primary or as a secondary position).
func (ss *CommonState) TCWControlsPosition(tcw TCW, pos ControlPosition) bool {
	cons, ok := ss.CurrentConsolidation[tcw]
	return ok && cons.ControlsPosition(pos)
}

func (ss *CommonState) TCWForPosition(pos ControlPosition) TCW {
	for tcw, cons := range ss.CurrentConsolidation {
		if cons.ControlsPosition(pos) {
			return tcw
		}
	}
	return TCW(pos) // it may be a center or external controller, etc.
}

// PrimaryPositionForTCW returns the primary position for the given TCW.
// Returns the TCW as position if no consolidation state exists or if Primary is empty.
func (ss *CommonState) PrimaryPositionForTCW(tcw TCW) ControlPosition {
	if cons, ok := ss.CurrentConsolidation[tcw]; ok && cons.PrimaryTCP != "" {
		return cons.PrimaryTCP
	}
	return ControlPosition(tcw)
}

///////////////////////////////////////////////////////////////////////////
// CommonState methods for ATPA configuration

// FindAirportForATPAVolume returns the airport ICAO code that contains the given volume ID
func (ss *CommonState) FindAirportForATPAVolume(volumeId string) string {
	for icao, ap := range ss.Airports {
		if _, ok := ap.ATPAVolumes[volumeId]; ok {
			return icao
		}
	}
	return ""
}

// IsATPAVolumeDisabled checks if the given ATPA volume is disabled
func (ss *CommonState) IsATPAVolumeDisabled(volumeId string) bool {
	airport := ss.FindAirportForATPAVolume(volumeId)
	if airport == "" {
		return true // Volume not found, treat as disabled
	}
	if airportState, ok := ss.ATPAVolumeState[airport]; ok {
		if volState, ok := airportState[volumeId]; ok {
			return volState.Disabled
		}
	}
	return true
}

// IsATPAVolume25nmEnabled checks if 2.5nm reduced separation is enabled for the volume
func (ss *CommonState) IsATPAVolume25nmEnabled(volumeId string) bool {
	airport := ss.FindAirportForATPAVolume(volumeId)
	if airport == "" {
		return false
	}
	if ap, ok := ss.ATPAVolumeState[airport]; ok {
		if vol, ok := ap[volumeId]; ok {
			return !vol.Reduced25Disabled
		}
	}
	return false
}
