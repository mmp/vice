// pkg/sim/state.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
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
	getweather "github.com/checkandmate1/AirportWeatherData"
)

const serverCallsign = "__SERVER__"

type State struct {
	Aircraft    map[string]*av.Aircraft
	METAR       map[string]*av.METAR
	Controllers map[string]*av.Controller

	DepartureAirports map[string]*av.Airport
	ArrivalAirports   map[string]*av.Airport

	ERAMComputers *ERAMComputers

	TRACON                   string
	LaunchConfig             LaunchConfig
	PrimaryController        string
	MultiControllers         av.SplitConfiguration
	SimIsPaused              bool
	SimRate                  float32
	SimName                  string
	SimDescription           string
	SimTime                  time.Time
	MagneticVariation        float32
	NmPerLongitude           float32
	Airports                 map[string]*av.Airport
	Fixes                    map[string]math.Point2LL
	PrimaryAirport           string
	RadarSites               map[string]*av.RadarSite
	Center                   math.Point2LL
	Range                    float32
	Wind                     av.Wind
	Callsign                 string
	ScenarioDefaultVideoMaps []string
	ApproachAirspace         []ControllerAirspaceVolume
	DepartureAirspace        []ControllerAirspaceVolume
	DepartureRunways         []ScenarioGroupDepartureRunway
	ArrivalRunways           []ScenarioGroupArrivalRunway
	Scratchpads              map[string]string
	InboundFlows             map[string]InboundFlow
	TotalDepartures          int
	TotalArrivals            int
	TotalOverflights         int
	STARSFacilityAdaptation  STARSFacilityAdaptation
	UserRestrictionAreas     []RestrictionArea

	ControllerVideoMaps        []string
	ControllerDefaultVideoMaps []string
	VideoMapLibraryHash        []byte

	mapLibrary *av.VideoMapLibrary // just cached per session; not saved to disk.
}

func newState(selectedSplit string, liveWeather bool, isLocal bool, s *Sim, sg *ScenarioGroup, sc *Scenario,
	manifest *av.VideoMapManifest, tfrs []av.TFR, lg *log.Logger) *State {
	ss := &State{
		Callsign:      serverCallsign,
		Aircraft:      make(map[string]*av.Aircraft),
		METAR:         make(map[string]*av.METAR),
		Controllers:   make(map[string]*av.Controller),
		ERAMComputers: MakeERAMComputers(sg.STARSFacilityAdaptation.BeaconBank, lg),
	}

	if !isLocal {
		var err error
		ss.PrimaryController, err = sc.SplitConfigurations.GetPrimaryController(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get primary controller: %v", err)
		}
		ss.MultiControllers, err = sc.SplitConfigurations.GetConfiguration(selectedSplit)
		if err != nil {
			lg.Errorf("Unable to get multi controllers: %v", err)
		}
	} else {
		ss.PrimaryController = sc.SoloController
	}
	ss.TRACON = sg.TRACON
	ss.MagneticVariation = sg.MagneticVariation
	ss.NmPerLongitude = sg.NmPerLongitude
	ss.Wind = sc.Wind
	ss.Airports = sg.Airports
	ss.Fixes = sg.Fixes
	ss.PrimaryAirport = sg.PrimaryAirport
	fa := sg.STARSFacilityAdaptation
	ss.RadarSites = fa.RadarSites
	ss.Center = util.Select(sc.Center.IsZero(), fa.Center, sc.Center)
	ss.Range = util.Select(sc.Range == 0, fa.Range, sc.Range)
	ss.ScenarioDefaultVideoMaps = sc.DefaultMaps
	ss.Scratchpads = fa.Scratchpads
	ss.InboundFlows = sg.InboundFlows
	ss.ApproachAirspace = sc.ApproachAirspace
	ss.DepartureAirspace = sc.DepartureAirspace
	ss.DepartureRunways = sc.DepartureRunways
	ss.ArrivalRunways = sc.ArrivalRunways
	ss.LaunchConfig = s.LaunchConfig
	ss.SimIsPaused = s.Paused
	ss.SimRate = s.SimRate
	ss.SimName = s.Name
	ss.SimDescription = s.Scenario
	ss.SimTime = s.SimTime
	ss.STARSFacilityAdaptation = deep.MustCopy(sg.STARSFacilityAdaptation)
	if manifest != nil {
		ss.VideoMapLibraryHash, _ = manifest.Hash()
	}

	// Add the TFR restriction areas
	for _, tfr := range tfrs {
		ra := RestrictionAreaFromTFR(tfr)
		ss.STARSFacilityAdaptation.RestrictionAreas = append(ss.STARSFacilityAdaptation.RestrictionAreas, ra)
	}
	for _, callsign := range sc.VirtualControllers {
		// Skip controllers that are in MultiControllers
		if ss.MultiControllers != nil {
			if _, ok := ss.MultiControllers[callsign]; ok {
				continue
			}
		}

		if ctrl, ok := sg.ControlPositions[callsign]; ok {
			ss.Controllers[callsign] = ctrl
		} else {
			lg.Errorf("%s: controller not found in ControlPositions??", callsign)
		}
	}

	// Make some fake METARs; slightly different for all airports.
	alt := 2980 + rand.Intn(40)

	fakeMETAR := func(icao string) {
		spd := ss.Wind.Speed - 3 + rand.Int31n(6)
		var wind string
		if spd < 0 {
			wind = "00000KT"
		} else if spd < 4 {
			wind = fmt.Sprintf("VRB%02dKT", spd)
		} else {
			dir := 10 * ((ss.Wind.Direction + 5) / 10)
			dir += [3]int32{-10, 0, 10}[rand.Intn(3)]
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			gst := ss.Wind.Gust - 3 + rand.Int31n(6)
			if gst-ss.Wind.Speed > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		ss.METAR[icao] = &av.METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   fmt.Sprintf("A%d", alt-2+rand.Intn(4)),
		}
	}

	realMETAR := func(icao string) {
		weather, errors := getweather.GetWeather(icao)
		if len(errors) != 0 {
			lg.Errorf("%s: error getting weather: %+v", icao, errors)
		}

		dir := -1 // VRB by default
		if d, ok := weather.Wdir.(int); ok {
			dir = d
		} else if d, ok := weather.Wdir.(float64); ok {
			dir = int(d)
		}

		var wind string
		if spd := weather.Wspd; spd <= 0 {
			wind = "00000KT"
		} else if dir == -1 {
			wind = fmt.Sprintf("VRB%vKT", spd)
		} else {
			wind = fmt.Sprintf("%03d%02d", dir, spd)
			if weather.Wgst > 5 {
				wind += fmt.Sprintf("G%02d", weather.Wgst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		ss.METAR[icao] = &av.METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   "A" + getAltimiter(weather.RawMETAR),
		}
	}

	ss.DepartureAirports = make(map[string]*av.Airport)
	for name := range s.LaunchConfig.DepartureRates {
		ss.DepartureAirports[name] = ss.Airports[name]
	}
	ss.ArrivalAirports = make(map[string]*av.Airport)
	for _, airportRates := range s.LaunchConfig.InboundFlowRates {
		for name := range airportRates {
			if name != "overflights" {
				ss.ArrivalAirports[name] = ss.Airports[name]
			}
		}
	}
	if liveWeather {
		for ap := range ss.DepartureAirports {
			realMETAR(ap)
		}
		for ap := range ss.ArrivalAirports {
			realMETAR(ap)
		}
	} else {
		for ap := range ss.DepartureAirports {
			fakeMETAR(ap)
		}
		for ap := range ss.ArrivalAirports {
			fakeMETAR(ap)
		}
	}

	return ss
}

func (s *State) GetStateForController(callsign string) *State {
	// Make a deep copy so that if the server is running on the same
	// system, that the client doesn't see updates until they're explicitly
	// sent. (And similarly, that any speculative client changes to the
	// World state to improve responsiveness don't actually affect the
	// server.)
	state := deep.MustCopy(*s)
	state.Callsign = callsign

	// Now copy the appropriate video maps into ControllerVideoMaps and ControllerDefaultVideoMaps
	if config, ok := s.STARSFacilityAdaptation.ControllerConfigs[callsign]; ok && len(config.VideoMapNames) > 0 {
		state.ControllerVideoMaps = config.VideoMapNames
		state.ControllerDefaultVideoMaps = config.DefaultMaps
	} else {
		state.ControllerVideoMaps = s.STARSFacilityAdaptation.VideoMapNames
		state.ControllerDefaultVideoMaps = s.ScenarioDefaultVideoMaps
	}

	return &state
}

func getAltimiter(metar string) string {
	for _, indexString := range []string{" A3", " A2"} {
		index := strings.Index(metar, indexString)
		if index != -1 && index+6 < len(metar) {
			return metar[index+2 : index+6]
		}
	}
	return ""
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

func (ss *State) AircraftFromPartialCallsign(c string) *av.Aircraft {
	if ac, ok := ss.Aircraft[c]; ok {
		return ac
	}

	var final []*av.Aircraft
	for callsign, ac := range ss.Aircraft {
		if ac.ControllingController == ss.Callsign && strings.Contains(callsign, c) {
			final = append(final, ac)
		}
	}
	if len(final) == 1 {
		return final[0]
	} else {
		return nil
	}
}

func (ss *State) DepartureController(ac *av.Aircraft, lg *log.Logger) string {
	if len(ss.MultiControllers) > 0 {
		callsign, err := ss.MultiControllers.ResolveController(ac.DepartureContactController,
			func(callsign string) bool {
				ctrl, ok := ss.Controllers[callsign]
				return ok && ctrl.IsHuman
			})
		if err != nil {
			lg.Error("Unable to resolve departure controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}
		return util.Select(callsign != "", callsign, ss.PrimaryController)
	} else {
		return ss.PrimaryController
	}
}

func (ss *State) GetReleaseDepartures() []*av.Aircraft {
	return util.FilterSlice(ss.STARSComputer().GetReleaseDepartures(),
		func(ac *av.Aircraft) bool {
			// When ControlClient DeleteAllAircraft() is called, we do our usual trick of
			// making the update locally pending the next update from the server. However, it
			// doesn't clear out the ones in the STARSComputer; that happens server side only.
			// So, here is a band-aid to not return aircraft that no longer exist.
			if _, ok := ss.Aircraft[ac.Callsign]; !ok {
				return false
			}
			return ss.DepartureController(ac, nil) == ss.Callsign
		})
}

func (s *State) GetVideoMapLibrary(client *ControlClient) (*av.VideoMapLibrary, error) {
	if s.mapLibrary != nil {
		return s.mapLibrary, nil
	}

	filename := s.STARSFacilityAdaptation.VideoMapFile
	ml, err := av.HashCheckLoadVideoMap(filename, s.VideoMapLibraryHash)
	if err == nil {
		s.mapLibrary = ml
		return ml, nil
	} else {
		ml, err = client.GetVideoMapLibrary(filename)
		if err == nil {
			s.mapLibrary = ml
		}
		return ml, err
	}
}

func (s *State) GetControllerVideoMaps() ([]string, []string) {
	return s.ControllerVideoMaps, s.ControllerDefaultVideoMaps
}

func (ss *State) GetInitialRange() float32 {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *State) GetInitialCenter() math.Point2LL {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok && !config.Center.IsZero() {
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

func (ss *State) GetWindVector(p math.Point2LL, alt float32) math.Point2LL {
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
	if strings.HasSuffix(callsign, "_APP") || strings.HasSuffix(callsign, "DEP") {
		return ss.TRACON, true
	}
	return "", false
}

func (ss *State) DeleteAircraft(ac *av.Aircraft) {
	delete(ss.Aircraft, ac.Callsign)
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
