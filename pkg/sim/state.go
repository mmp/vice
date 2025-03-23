// pkg/sim/state.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	gomath "math"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
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
	Aircraft          map[string]*av.Aircraft
	Airports          map[string]*av.Airport
	DepartureAirports map[string]*av.Airport
	ArrivalAirports   map[string]*av.Airport
	Fixes             map[string]math.Point2LL
	VFRRunways        map[string]av.Runway // assume just one runway per airport

	// Signed in human controllers + virtual controllers
	Controllers      map[string]*av.Controller
	HumanControllers []string

	PrimaryController string
	MultiControllers  av.SplitConfiguration
	PrimaryTCP        string
	Airspace          map[string]map[string][]av.ControllerAirspaceVolume // ctrl id -> vol name -> definition

	DepartureRunways []DepartureRunway
	ArrivalRunways   []ArrivalRunway
	InboundFlows     map[string]*av.InboundFlow
	LaunchConfig     LaunchConfig

	Center                   math.Point2LL
	Range                    float32
	ScenarioDefaultVideoMaps []string
	UserRestrictionAreas     []av.RestrictionArea

	ERAMComputers           *ERAMComputers
	STARSFacilityAdaptation av.STARSFacilityAdaptation

	TRACON            string
	MagneticVariation float32
	NmPerLongitude    float32
	PrimaryAirport    string

	METAR map[string]*av.METAR
	Wind  av.Wind

	TotalIFR, TotalVFR int

	Paused         bool
	SimRate        float32
	SimDescription string
	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..

	Instructors map[string]bool

	VideoMapLibraryHash []byte

	// Set in State returned by GetStateForController
	ControllerVideoMaps                 []string
	ControllerDefaultVideoMaps          []string
	ControllerMonitoredBeaconCodeBlocks []av.Squawk
}

func newState(config NewSimConfiguration, manifest *av.VideoMapManifest, lg *log.Logger) *State {
	ss := &State{
		Aircraft:   make(map[string]*av.Aircraft),
		Airports:   config.Airports,
		Fixes:      config.Fixes,
		VFRRunways: make(map[string]av.Runway),

		Controllers:       make(map[string]*av.Controller),
		PrimaryController: config.PrimaryController,
		MultiControllers:  config.MultiControllers,
		PrimaryTCP:        serverCallsign,

		DepartureRunways: config.DepartureRunways,
		ArrivalRunways:   config.ArrivalRunways,
		InboundFlows:     config.InboundFlows,
		LaunchConfig:     config.LaunchConfig,

		Center:                   config.Center,
		Range:                    config.Range,
		ScenarioDefaultVideoMaps: config.DefaultMaps,

		ERAMComputers:           MakeERAMComputers(config.STARSFacilityAdaptation.BeaconBank, lg),
		STARSFacilityAdaptation: deep.MustCopy(config.STARSFacilityAdaptation),

		TRACON:            config.TRACON,
		MagneticVariation: config.MagneticVariation,
		NmPerLongitude:    config.NmPerLongitude,
		PrimaryAirport:    config.PrimaryAirport,

		METAR: make(map[string]*av.METAR),
		Wind:  config.Wind,

		SimRate:        1,
		SimDescription: config.Description,
		SimTime:        time.Now(),

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

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)

	fakeMETAR := func(icao []string) {
		for _, ap := range icao {
			ss.METAR[ap] = &av.METAR{
				// Just provide the stuff that the STARS display shows
				AirportICAO: ap,
				Wind:        ss.Wind.Randomize(),
				Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
			}
		}
	}

	realMETAR := func(icao []string) {
		metar, err := av.GetWeather(icao...)
		if err != nil {
			lg.Errorf("%s: error getting weather: %+v", strings.Join(icao, ", "), err)
		}

		for _, m := range metar {
			// Just provide the stuff that the STARS display shows
			ss.METAR[m.AirportICAO] = &m
		}
	}

	ss.DepartureAirports = make(map[string]*av.Airport)
	for name := range ss.LaunchConfig.DepartureRates {
		ss.DepartureAirports[name] = ss.Airports[name]
	}
	for name, ap := range ss.Airports {
		if ap.VFRRateSum() > 0 {
			ss.DepartureAirports[name] = ap

			if rwy, _ := av.DB.Airports[name].SelectBestRunway(ss /* wind */, ss.MagneticVariation); rwy != nil {
				ss.VFRRunways[name] = *rwy
			} else {
				lg.Errorf("%s: unable to find runway for VFRs", name)
			}
		}
	}

	ss.ArrivalAirports = make(map[string]*av.Airport)
	for _, airportRates := range ss.LaunchConfig.InboundFlowRates {
		for name := range airportRates {
			if name != "overflights" {
				ss.ArrivalAirports[name] = ss.Airports[name]
			}
		}
	}

	// Get the unique airports we potentially want METAR for.
	aps := slices.Collect(maps.Keys(ss.DepartureAirports))
	aps = slices.AppendSeq(aps, maps.Keys(ss.ArrivalAirports))
	aps = append(aps, ss.STARSFacilityAdaptation.Altimeters...)
	slices.Sort(aps)
	aps = slices.Compact(aps)

	if config.LiveWeather {
		realMETAR(aps)
	} else {
		fakeMETAR(aps)
	}

	return ss
}

func (s *State) GetStateForController(tcp string) *State {
	// Make a deep copy so that if the server is running on the same
	// system, that the client doesn't see updates until they're explicitly
	// sent. (And similarly, that any speculative client changes to the
	// World state to improve responsiveness don't actually affect the
	// server.)
	state := deep.MustCopy(*s)
	state.PrimaryTCP = tcp

	// Now copy the appropriate video maps into ControllerVideoMaps and ControllerDefaultVideoMaps
	if config, ok := s.STARSFacilityAdaptation.ControllerConfigs[tcp]; ok && len(config.VideoMapNames) > 0 {
		state.ControllerVideoMaps = config.VideoMapNames
		state.ControllerDefaultVideoMaps = config.DefaultMaps
		state.ControllerMonitoredBeaconCodeBlocks = config.MonitoredBeaconCodeBlocks
	} else {
		state.ControllerVideoMaps = s.STARSFacilityAdaptation.VideoMapNames
		state.ControllerDefaultVideoMaps = s.ScenarioDefaultVideoMaps
		state.ControllerMonitoredBeaconCodeBlocks = s.STARSFacilityAdaptation.MonitoredBeaconCodeBlocks
	}

	return &state
}

func (s *State) Activate(lg *log.Logger) {
	// Make the ERAMComputers aware of each other.
	s.ERAMComputers.Activate()
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

// Returns all aircraft that match the given suffix. If instructor is true,
// returns all matching aircraft; otherwise only ones under the current
// controller's control are considered for matching.
func (ss *State) AircraftFromCallsignSuffix(suffix string, instructor bool) []*av.Aircraft {
	match := func(ac *av.Aircraft) bool {
		if !strings.HasSuffix(ac.Callsign, suffix) {
			return false
		}
		if instructor || ac.ControllingController == ss.PrimaryTCP {
			return true
		}
		// Hold for release aircraft still in the list
		if ac.DepartureContactController == ss.PrimaryTCP && ac.ControllingController == "" {
			return true
		}
		return false
	}
	return slices.Collect(util.FilterSeq(maps.Values(ss.Aircraft), match))
}

func (ss *State) DepartureController(ac *av.Aircraft, lg *log.Logger) string {
	if len(ss.MultiControllers) > 0 {
		callsign, err := ss.MultiControllers.ResolveController(ac.DepartureContactController,
			func(tcp string) bool {
				return slices.Contains(ss.HumanControllers, tcp)
			})
		if err != nil {
			lg.Warn("Unable to resolve departure controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}
		return util.Select(callsign != "", callsign, ss.PrimaryController)
	} else {
		return ss.PrimaryController
	}
}

func (ss *State) GetAllReleaseDepartures() []*av.Aircraft {
	return util.FilterSliceInPlace(ss.STARSComputer().GetReleaseDepartures(),
		func(ac *av.Aircraft) bool {
			// When ControlClient DeleteAllAircraft() is called, we do our usual trick of
			// making the update locally pending the next update from the server. However, it
			// doesn't clear out the ones in the STARSComputer; that happens server side only.
			// So, here is a band-aid to not return aircraft that no longer exist.
			if _, ok := ss.Aircraft[ac.Callsign]; !ok {
				return false
			}
			return ss.DepartureController(ac, nil) == ss.PrimaryTCP
		})
}

func (ss *State) GetRegularReleaseDepartures() []*av.Aircraft {
	return util.FilterSliceInPlace(ss.GetAllReleaseDepartures(),
		func(ac *av.Aircraft) bool {
			if ac.Released {
				return false
			}

			for _, cl := range ss.STARSFacilityAdaptation.CoordinationLists {
				if slices.Contains(cl.Airports, ac.FlightPlan.DepartureAirport) {
					// It'll be in a STARS coordination list
					return false
				}
			}
			return true
		})
}

func (ss *State) GetSTARSReleaseDepartures() []*av.Aircraft {
	return util.FilterSliceInPlace(ss.GetAllReleaseDepartures(),
		func(ac *av.Aircraft) bool {
			for _, cl := range ss.STARSFacilityAdaptation.CoordinationLists {
				if slices.Contains(cl.Airports, ac.FlightPlan.DepartureAirport) {
					return true
				}
			}
			return false
		})
}

func (ss *State) GetInitialRange() float32 {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.PrimaryTCP]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *State) GetInitialCenter() math.Point2LL {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.PrimaryTCP]; ok && !config.Center.IsZero() {
		return config.Center
	}
	return ss.Center
}

func (s *State) IsDeparture(ac *av.Aircraft) bool {
	if _, ok := s.DepartureAirports[ac.FlightPlan.DepartureAirport]; ok {
		return true
	}
	return false
}

func (s *State) IsArrival(ac *av.Aircraft) bool {
	if _, ok := s.ArrivalAirports[ac.FlightPlan.ArrivalAirport]; ok {
		return true
	}
	return false
}

func (s *State) IsOverflight(ac *av.Aircraft) bool {
	return !s.IsDeparture(ac) && !s.IsArrival(ac)
}

func (s *State) IsIntraFacility(ac *av.Aircraft) bool {
	return s.IsDeparture(ac) && s.IsArrival(ac)
}

func (ss *State) InhibitCAVolumes() []av.AirspaceVolume {
	return ss.STARSFacilityAdaptation.InhibitCAVolumes
}

func (ss *State) AverageWindVector() [2]float32 {
	d := math.OppositeHeading(float32(ss.Wind.Direction))
	v := [2]float32{math.Sin(math.Radians(d)), math.Cos(math.Radians(d))}
	return math.Scale2f(v, float32(ss.Wind.Speed))
}

func (ss *State) GetWindVector(p math.Point2LL, alt float32) [2]float32 {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := ss.SimTime.Sub(base).Seconds()
	windSpeed := float32(ss.Wind.Speed) +
		float32(ss.Wind.Gust-ss.Wind.Speed)*float32(1+gomath.Cos(sec/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := math.OppositeHeading(float32(ss.Wind.Direction))
	vWind := [2]float32{math.Sin(math.Radians(d)), math.Cos(math.Radians(d))}
	vWind = math.Scale2f(vWind, windSpeed/3600)
	return vWind
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

func (ss *State) DeleteAircraft(ac *av.Aircraft) {
	delete(ss.Aircraft, ac.Callsign)
	ss.ERAMComputer().ReturnSquawk(ac.Squawk)
	ss.ERAMComputers.CompletelyDeleteAircraft(ac)
}

func (ss *State) STARSComputer() *STARSComputer {
	_, stars, _ := ss.ERAMComputers.FacilityComputers(ss.TRACON)
	return stars
}

func (ss *State) ERAMComputer() *ERAMComputer {
	eram, _, _ := ss.ERAMComputers.FacilityComputers(ss.TRACON)
	return eram
}

func (ss *State) AmInstructor() bool {
	_, ok := ss.Instructors[ss.PrimaryTCP]
	return ok
}
