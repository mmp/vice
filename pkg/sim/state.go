// pkg/sim/state.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"

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
	UnassociatedFlightPlans []*STARSFlightPlan

	ACFlightPlans map[av.ADSBCallsign]av.FlightPlan // needed for flight strips...

	Airports          map[string]*av.Airport
	DepartureAirports map[string]interface{}
	ArrivalAirports   map[string]interface{}
	Fixes             map[string]math.Point2LL
	VFRRunways        map[string]av.Runway // assume just one runway per airport
	ReleaseDepartures []ReleaseDeparture

	// Signed in human controllers + virtual controllers
	Controllers      map[string]*av.Controller
	HumanControllers []string

	PrimaryController string
	MultiControllers  av.SplitConfiguration
	UserTCP           string
	Airspace          map[string]map[string][]av.ControllerAirspaceVolume // ctrl id -> vol name -> definition

	GenerationIndex int

	DepartureRunways []DepartureRunway
	ArrivalRunways   []ArrivalRunway
	InboundFlows     map[string]*av.InboundFlow
	LaunchConfig     LaunchConfig

	Center                   math.Point2LL
	Range                    float32
	ScenarioDefaultVideoMaps []string
	UserRestrictionAreas     []av.RestrictionArea

	STARSFacilityAdaptation STARSFacilityAdaptation

	TRACON            string
	MagneticVariation float32
	NmPerLongitude    float32
	PrimaryAirport    string

	WX *av.WeatherModel

	TotalIFR, TotalVFR int

	Paused         bool
	SimRate        float32
	SimDescription string
	SimStartTime   time.Time
	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..

	QuickFlightPlanIndex int // for auto ACIDs for quick ACID flight plan 5-145

	Instructors map[string]bool

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
	DepartureController string
	Released            bool
	Squawk              av.Squawk
	ListIndex           int
	AircraftType        string
	Exit                string
}

func newState(config NewSimConfiguration, manifest *VideoMapManifest, lg *log.Logger) *State {
	now := time.Now()
	ss := &State{
		Airports:   config.Airports,
		Fixes:      config.Fixes,
		VFRRunways: make(map[string]av.Runway),

		Controllers:       make(map[string]*av.Controller),
		PrimaryController: config.PrimaryController,
		MultiControllers:  config.MultiControllers,
		UserTCP:           serverCallsign,

		DepartureRunways: config.DepartureRunways,
		ArrivalRunways:   config.ArrivalRunways,
		InboundFlows:     config.InboundFlows,
		LaunchConfig:     config.LaunchConfig,

		Center:                   config.Center,
		Range:                    config.Range,
		ScenarioDefaultVideoMaps: config.DefaultMaps,

		STARSFacilityAdaptation: deep.MustCopy(config.STARSFacilityAdaptation),

		TRACON:            config.TRACON,
		MagneticVariation: config.MagneticVariation,
		NmPerLongitude:    config.NmPerLongitude,
		PrimaryAirport:    config.PrimaryAirport,

		WX: av.MakeWeatherModel(slices.Collect(maps.Keys(config.Airports)), now, config.NmPerLongitude,
			config.MagneticVariation, config.Wind, lg),

		SimRate:        1,
		SimDescription: config.Description,
		SimStartTime:   now,
		SimTime:        now,

		Instructors: make(map[string]bool),
	}

	if manifest != nil {
		ss.VideoMapLibraryHash, _ = manifest.Hash()
	}

	if len(config.ControllerAirspace) > 0 {
		ss.Airspace = make(map[string]map[string][]av.ControllerAirspaceVolume)
		if config.IsLocal {
			ss.Airspace[ss.PrimaryController] = make(map[string][]av.ControllerAirspaceVolume)
			// Take all the airspace
			for _, vnames := range config.ControllerAirspace {
				for _, vname := range vnames {
					// Remap from strings provided in the scenario to the
					// actual volumes defined in the scenario group.
					ss.Airspace[ss.PrimaryController][vname] = config.Airspace.Volumes[vname]
				}
			}
		} else {
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
	}

	// Add the TFR restriction areas
	for _, tfr := range config.TFRs {
		ra := av.RestrictionAreaFromTFR(tfr)
		ss.STARSFacilityAdaptation.RestrictionAreas = append(ss.STARSFacilityAdaptation.RestrictionAreas, ra)
	}

	for _, callsign := range config.VirtualControllers {
		// Filter out any that are actually human-controlled positions.
		if callsign == ss.PrimaryController {
			continue
		}
		if ss.MultiControllers != nil {
			if _, ok := ss.MultiControllers[callsign]; ok {
				continue
			}
		}

		if ctrl, ok := config.ControlPositions[callsign]; ok {
			ss.Controllers[callsign] = ctrl
		} else {
			lg.Errorf("%s: controller not found in ControlPositions??", callsign)
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
			windDir := ss.WX.LookupWind(ap.Location, float32(ap.Elevation)).Direction
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

func (ss *State) GetStateForController(tcp string) *State {
	// Make a deep copy so that if the server is running on the same
	// system, that the client doesn't see updates until they're explicitly
	// sent. (And similarly, that any speculative client changes to the
	// World state to improve responsiveness don't actually affect the
	// server.)
	state := deep.MustCopy(*ss)
	state.UserTCP = tcp

	// Now copy the appropriate video maps into ControllerVideoMaps and ControllerDefaultVideoMaps
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[tcp]; ok && len(config.VideoMapNames) > 0 {
		state.ControllerVideoMaps = config.VideoMapNames
		state.ControllerDefaultVideoMaps = config.DefaultMaps
		state.ControllerMonitoredBeaconCodeBlocks = config.MonitoredBeaconCodeBlocks
	} else {
		state.ControllerVideoMaps = ss.STARSFacilityAdaptation.VideoMapNames
		state.ControllerDefaultVideoMaps = ss.ScenarioDefaultVideoMaps
		state.ControllerMonitoredBeaconCodeBlocks = ss.STARSFacilityAdaptation.MonitoredBeaconCodeBlocks
	}

	return &state
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

func (ss *State) GetConsolidatedPositions(id string) []string {
	var cons []string

	for pos := range ss.MultiControllers {
		rid, _ := ss.MultiControllers.ResolveController(pos, func(id string) bool {
			_, ok := ss.Controllers[id]
			return ok // active
		})
		if rid == id { // The position resolves to us.
			cons = append(cons, pos)
		}
	}

	slices.Sort(cons)

	return cons
}

func (ss *State) ResolveController(tcp string) string {
	if _, ok := ss.Controllers[tcp]; ok {
		// The easy case: the controller is already signed in
		return tcp
	}

	if len(ss.MultiControllers) > 0 {
		mtcp, err := ss.MultiControllers.ResolveController(tcp,
			func(multiTCP string) bool {
				return slices.Contains(ss.HumanControllers, multiTCP)
			})
		if err == nil {
			return mtcp
		}
	}

	return ss.PrimaryController
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
			return ss.ResolveController(dep.DepartureController) == ss.UserTCP
		})
}

func (ss *State) GetRegularReleaseDepartures() []ReleaseDeparture {
	return util.FilterSlice(ss.ReleaseDepartures,
		func(dep ReleaseDeparture) bool {
			if dep.Released {
				return false
			}

			for _, cl := range ss.STARSFacilityAdaptation.CoordinationLists {
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
			for _, cl := range ss.STARSFacilityAdaptation.CoordinationLists {
				if slices.Contains(cl.Airports, dep.DepartureAirport) {
					return true
				}
			}
			return false
		})
}

func (ss *State) GetInitialRange() float32 {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.UserTCP]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *State) GetInitialCenter() math.Point2LL {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.UserTCP]; ok && !config.Center.IsZero() {
		return config.Center
	}
	return ss.Center
}

func (ss *State) FacilityFromController(callsign string) (string, bool) {
	if controller := ss.Controllers[callsign]; controller != nil {
		if controller.Facility != "" {
			return controller.Facility, true
		} else if controller != nil {
			return ss.TRACON, true
		}
	}
	if slices.Contains(ss.HumanControllers, callsign) || callsign == ss.PrimaryController {
		return ss.TRACON, true
	}
	if _, ok := ss.MultiControllers[callsign]; ok {
		return ss.TRACON, true
	}

	return "", false
}

func (ss *State) AreInstructorOrRPO(tcp string) bool {
	// Check if they're marked as an instructor in the Instructors map (for regular controllers with instructor privileges)
	if ss.Instructors[tcp] {
		return true
	}
	// Also check if they're signed in as a dedicated instructor/RPO position
	ctrl, ok := ss.Controllers[tcp]
	return ok && (ctrl.Instructor || ctrl.RPO)
}

func (ss *State) BeaconCodeInUse(sq av.Squawk) bool {
	if util.SeqContainsFunc(maps.Values(ss.Tracks),
		func(tr *Track) bool {
			return tr.IsAssociated() && tr.Squawk == sq
		}) {
		return true
	}

	if slices.ContainsFunc(ss.UnassociatedFlightPlans,
		func(fp *STARSFlightPlan) bool { return fp.AssignedSquawk == sq }) {
		return true
	}

	return false
}

func (ss *State) FindMatchingFlightPlan(s string) *STARSFlightPlan {
	n := -1
	if pn, err := strconv.Atoi(s); err == nil && len(s) <= 2 {
		n = pn
	}

	sq := av.Squawk(0)
	if ps, err := av.ParseSquawk(s); err == nil {
		sq = ps
	}

	for _, fp := range ss.UnassociatedFlightPlans {
		if fp.ACID == ACID(s) {
			return fp
		}
		if fp.ListIndex != UnsetSTARSListIndex && n == fp.ListIndex {
			return fp
		}
		if sq == fp.AssignedSquawk {
			return fp
		}
	}
	return nil
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
		if trk.ADSBCallsign == callsign && trk.IsAssociated() &&
			trk.FlightPlan.TrackingController == ss.UserTCP {
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

func (ss *State) GetOurTrackByACID(acid ACID) (*Track, bool) {
	for i, trk := range ss.Tracks {
		if trk.IsAssociated() && trk.FlightPlan.ACID == acid &&
			trk.FlightPlan.TrackingController == ss.UserTCP {
			return ss.Tracks[i], true
		}
	}
	return nil, false
}

// FOOTGUN: this should not be called from server-side code, since Tracks isn't initialized there.
// FIXME FIXME FIXME
func (ss *State) GetFlightPlanForACID(acid ACID) *STARSFlightPlan {
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

func (ss *State) IsExternalController(tcp string) bool {
	ctrl, ok := ss.Controllers[tcp]
	return ok && ctrl.FacilityIdentifier != ""
}

func (ss *State) IsLocalController(tcp string) bool {
	ctrl, ok := ss.Controllers[tcp]
	return ok && ctrl.FacilityIdentifier == ""
}
