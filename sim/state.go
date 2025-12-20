// sim/state.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
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

const serverCallsign = "__SERVER__"

// State serves two purposes: first, the Sim object holds one to organize
// assorted information about the world state that it updates as part of
// the simulation. Second, an instance of it is given to clients when they
// join a sim.  As the sim runs, the client's State is updated roughly once
// a second.  Clients can then use the State as a read-only reference for
// assorted information they may need (the state of aircraft in the sim,
// etc.)
type State struct {
	Tracks map[av.ADSBCallsign]*Track

	// Unassociated ones, including unsupported DBs
	UnassociatedFlightPlans []*NASFlightPlan

	ACFlightPlans map[av.ADSBCallsign]av.FlightPlan // needed for flight strips...

	Airports          map[string]*av.Airport
	DepartureAirports map[string]interface{}
	ArrivalAirports   map[string]interface{}
	Fixes             map[string]math.Point2LL
	VFRRunways        map[string]av.Runway // assume just one runway per airport
	ReleaseDepartures []ReleaseDeparture

	// All controller positions for this scenario (both human-designated and virtual)
	Controllers map[ControllerPosition]*av.Controller

	ConfigurationId              string // Short identifier for the configuration (from ControllerConfiguration.Id)
	UserTCW                      TCW
	CurrentConsolidation         map[TCW]*TCPConsolidation // Current position consolidation
	ScenarioDefaultConsolidation PositionConsolidation     // Scenario's original hierarchy. Immutable after initialization.

	Airspace map[ControllerPosition]map[string][]av.ControllerAirspaceVolume // position -> vol name -> definition

	GenerationIndex int

	DepartureRunways []DepartureRunway
	ArrivalRunways   []ArrivalRunway
	InboundFlows     map[string]*av.InboundFlow
	LaunchConfig     LaunchConfig
	Emergencies      []Emergency

	Center                    math.Point2LL
	Range                     float32
	ScenarioDefaultVideoMaps  []string
	ScenarioDefaultVideoGroup string
	UserRestrictionAreas      []av.RestrictionArea

	FacilityAdaptation FacilityAdaptation

	Facility          string
	MagneticVariation float32
	NmPerLongitude    float32
	PrimaryAirport    string

	METAR map[string]wx.METAR

	TotalIFR, TotalVFR int

	Paused         bool
	SimRate        float32
	SimDescription string
	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..

	QuickFlightPlanIndex int // for auto ACIDs for quick ACID flight plan 5-145

	PrivilegedTCWs map[TCW]bool // TCWs with elevated privileges (can control any aircraft)
	Observers      map[TCW]bool // TCWs connected as observers (no position)

	VideoMapLibraryHash []byte

	// Set in State returned by GetStateForController
	ControllerVideoMaps                 []string
	ControllerDefaultVideoMaps          []string
	ControllerMonitoredBeaconCodeBlocks []av.Squawk

	RadioTransmissions [][]byte
}

type ReleaseDeparture struct {
	ADSBCallsign        av.ADSBCallsign
	DepartureAirport    string
	DepartureController ControllerPosition
	Released            bool
	Squawk              av.Squawk
	ListIndex           int
	AircraftType        string
	Exit                string
}

func newState(config NewSimConfiguration, startTime time.Time, manifest *VideoMapManifest, model *wx.Model, metar map[string][]wx.METAR,
	lg *log.Logger) *State {
	// Roll back the start time to account for prespawn
	startTime = startTime.Add(-initialSimSeconds * time.Second)

	ss := &State{
		Airports:   config.Airports,
		Fixes:      config.Fixes,
		VFRRunways: make(map[string]av.Runway),

		Controllers:                  maps.Clone(config.ControlPositions),
		ConfigurationId:              config.ControllerConfiguration.Id,
		UserTCW:                      serverCallsign,
		CurrentConsolidation:         make(map[TCW]*TCPConsolidation),
		ScenarioDefaultConsolidation: config.ControllerConfiguration.Positions,

		DepartureRunways: config.DepartureRunways,
		ArrivalRunways:   config.ArrivalRunways,
		InboundFlows:     config.InboundFlows,
		LaunchConfig:     config.LaunchConfig,
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
		METAR:             make(map[string]wx.METAR),

		SimRate:        1,
		SimDescription: config.Description,
		SimTime:        startTime,

		PrivilegedTCWs: make(map[TCW]bool),
		Observers:      make(map[TCW]bool),
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
		ss.Airspace = make(map[ControllerPosition]map[string][]av.ControllerAirspaceVolume)
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
	rootTCP, _ := ss.ScenarioDefaultConsolidation.RootPosition()
	rootCons := &TCPConsolidation{PrimaryTCP: rootTCP}
	ss.CurrentConsolidation[TCW(rootTCP)] = rootCons
	for _, tcp := range ss.ScenarioDefaultConsolidation.AllPositions() {
		if tcp != rootTCP {
			rootCons.SecondaryTCPs = append(rootCons.SecondaryTCPs,
				SecondaryTCP{TCP: tcp, Type: ConsolidationFull})
			ss.CurrentConsolidation[TCW(tcp)] = &TCPConsolidation{}
		}
	}

	ss.DepartureAirports = make(map[string]interface{})
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

	ss.ArrivalAirports = make(map[string]interface{})
	for _, airportRates := range ss.LaunchConfig.InboundFlowRates {
		for name := range airportRates {
			if name != "overflights" {
				ss.ArrivalAirports[name] = nil
			}
		}
	}

	return ss
}

func (ss *State) Locate(s string) (math.Point2LL, bool) {
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

func (ss *State) GetAllReleaseDepartures() []ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep ReleaseDeparture) bool {
			// When ControlClient DeleteAllAircraft() is called, we do our usual trick of
			// making the update locally pending the next update from the server. However, it
			// doesn't clear out the ones in the STARSComputer; that happens server side only.
			// So, here is a band-aid to not return aircraft that no longer exist.
			//if _, ok := ss.Aircraft[ac.ADSBCallsign]; !ok {
			//return false
			//}
			return ss.UserControlsPosition(dep.DepartureController)
		})
}

func (ss *State) GetRegularReleaseDepartures() []ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep ReleaseDeparture) bool {
			if dep.Released {
				return false
			}

			for _, cl := range ss.FacilityAdaptation.CoordinationLists {
				if slices.Contains(cl.Airports, dep.DepartureAirport) {
					// It'll be in a STARS coordination list
					return false
				}
			}
			return true
		})
}

func (ss *State) GetSTARSReleaseDepartures() []ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep ReleaseDeparture) bool {
			for _, cl := range ss.FacilityAdaptation.CoordinationLists {
				if slices.Contains(cl.Airports, dep.DepartureAirport) {
					return true
				}
			}
			return false
		})
}

func (ss *State) GetInitialRange() float32 {
	if config, ok := ss.FacilityAdaptation.ControllerConfigs[ss.PrimaryPositionForTCW(ss.UserTCW)]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *State) GetInitialCenter() math.Point2LL {
	if config, ok := ss.FacilityAdaptation.ControllerConfigs[ss.PrimaryPositionForTCW(ss.UserTCW)]; ok && !config.Center.IsZero() {
		return config.Center
	}
	return ss.Center
}

func (ss *State) BeaconCodeInUse(sq av.Squawk) bool {
	if util.SeqContainsFunc(maps.Values(ss.Tracks),
		func(tr *Track) bool {
			return tr.IsAssociated() && tr.Squawk == sq
		}) {
		return true
	}

	if slices.ContainsFunc(ss.UnassociatedFlightPlans,
		func(fp *NASFlightPlan) bool { return fp.AssignedSquawk == sq }) {
		return true
	}

	return false
}

func (ss *State) GetTrackByCallsign(callsign av.ADSBCallsign) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.ADSBCallsign == callsign {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *State) GetOurTrackByCallsign(callsign av.ADSBCallsign) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.ADSBCallsign == callsign && trk.IsAssociated() && ss.UserControlsTrack(ss.Tracks[i]) {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *State) GetTrackByACID(acid ACID) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *State) GetTrackByFLID(flid string) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if !trk.IsAssociated() {
			continue
		}
		if trk.FlightPlan.CID == flid {
			return ss.Tracks[i], true
		}
		if trk.ADSBCallsign == av.ADSBCallsign(flid) {
			return ss.Tracks[i], true
		}
		if sq, err := av.ParseSquawk(flid); err != nil && trk.FlightPlan.AssignedSquawk == sq {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

func (ss *State) GetOurTrackByACID(acid ACID) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid && ss.UserControlsTrack(ss.Tracks[i]) {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

// FOOTGUN: this should not be called from server-side code, since Tracks isn't initialized there.
// FIXME FIXME FIXME
func (ss *State) GetFlightPlanForACID(acid ACID) *NASFlightPlan {
	for _, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid {
			return trk.FlightPlan
		}
	}
	for i, fp := range ss.UnassociatedFlightPlans {
		if fp.ACID == acid {
			return ss.UnassociatedFlightPlans[i]
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////
// State methods for controller/consolidation management

func (ss *State) GetStateForController(tcw TCW) *State {
	// Make a deep copy so that if the server is running on the same
	// system, that the client doesn't see updates until they're explicitly
	// sent. (And similarly, that any speculative client changes to the
	// World state to improve responsiveness don't actually affect the
	// server.)
	state := deep.MustCopy(*ss)

	state.UserTCW = tcw

	// Now copy the appropriate video maps into ControllerVideoMaps and ControllerDefaultVideoMaps
	if config, ok := ss.FacilityAdaptation.ControllerConfigs[ss.PrimaryPositionForTCW(tcw)]; ok && len(config.VideoMapNames) > 0 {
		state.ControllerVideoMaps = config.VideoMapNames
		state.ControllerDefaultVideoMaps = config.DefaultMaps
		state.ControllerMonitoredBeaconCodeBlocks = config.MonitoredBeaconCodeBlocks
	} else {
		state.ControllerVideoMaps = ss.FacilityAdaptation.VideoMapNames
		state.ControllerDefaultVideoMaps = ss.ScenarioDefaultVideoMaps
		state.ControllerMonitoredBeaconCodeBlocks = ss.FacilityAdaptation.MonitoredBeaconCodeBlocks
	}

	return &state
}

func (ss *State) IsExternalController(pos ControllerPosition) bool {
	// Resolve consolidated positions to whoever currently controls them
	resolved := ss.ResolveController(pos)
	ctrl, ok := ss.Controllers[resolved]
	return ok && ctrl.FacilityIdentifier != ""
}

func (ss *State) IsLocalController(pos ControllerPosition) bool {
	// Resolve consolidated positions to whoever currently controls them
	resolved := ss.ResolveController(pos)
	ctrl, ok := ss.Controllers[resolved]
	return ok && ctrl.FacilityIdentifier == ""
}

func (ss *State) TCWIsPrivileged(tcw TCW) bool {
	return ss.PrivilegedTCWs[tcw]
}

func (ss *State) TCWIsObserver(tcw TCW) bool {
	return ss.Observers[tcw]
}

func (ss *State) ResolveController(pos ControllerPosition) ControllerPosition {
	// Check who actually controls this position via consolidation.
	for _, cons := range ss.CurrentConsolidation {
		if cons.ControlsPosition(pos) {
			return cons.PrimaryTCP
		}
	}

	return pos
}

func (ss *State) GetPositionsForTCW(tcw TCW) []ControllerPosition {
	if cons, ok := ss.CurrentConsolidation[tcw]; ok {
		return cons.OwnedPositions()
	}
	return nil
}

// TCWControlsTrack returns true if the given TCW owns the track.
// Track ownership is determined by the OwningTCW field, which is set when
// the track is accepted/spawned and updated during full consolidation.
func (ss *State) TCWControlsTrack(tcw TCW, track *Track) bool {
	return track != nil && track.IsAssociated() && track.FlightPlan.OwningTCW == tcw
}

// TCWControlsPosition returns true if the given TCW controls the specified position
// (either as primary or as a secondary position).
func (ss *State) TCWControlsPosition(tcw TCW, pos ControllerPosition) bool {
	cons, ok := ss.CurrentConsolidation[tcw]
	return ok && cons.ControlsPosition(pos)
}

func (ss *State) TCWForPosition(pos ControllerPosition) TCW {
	for tcw, cons := range ss.CurrentConsolidation {
		if cons.ControlsPosition(pos) {
			return tcw
		}
	}
	return TCW(pos) // it may be a center or external controller, etc.
}

// PrimaryPositionForTCW returns the primary position for the given TCW.
// Returns the TCW as position if no consolidation state exists or if Primary is empty.
func (ss *State) PrimaryPositionForTCW(tcw TCW) ControllerPosition {
	if cons, ok := ss.CurrentConsolidation[tcw]; ok {
		return cons.PrimaryTCP
	}
	return ControllerPosition(tcw)
}

// GetUserConsolidation returns the consolidation state for the current user's TCW.
// Returns nil if no consolidation state exists.
func (ss *State) GetUserConsolidation() *TCPConsolidation {
	return ss.CurrentConsolidation[ss.UserTCW]
}

// UserControlsTrack returns true if the current user controls the given track.
func (ss *State) UserControlsTrack(track *Track) bool {
	return ss.TCWControlsTrack(ss.UserTCW, track)
}

// UserControlsPosition returns true if the current user controls the given position.
func (ss *State) UserControlsPosition(pos ControllerPosition) bool {
	return ss.TCWControlsPosition(ss.UserTCW, pos)
}
