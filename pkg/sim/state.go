// pkg/sim/state.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	gomath "math"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"

	getweather "github.com/checkandmate1/AirportWeatherData"
)

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
	ArrivalGroups            map[string][]av.Arrival
	TotalDepartures          int
	TotalArrivals            int
	STARSFacilityAdaptation  STARSFacilityAdaptation
}

func newState(selectedSplit string, liveWeather bool, isLocal bool, s *Sim, sg *ScenarioGroup, sc *Scenario,
	lg *log.Logger) *State {
	ss := &State{
		Callsign:      "__SERVER__",
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
	ss.ArrivalGroups = sg.ArrivalGroups
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
	ss.STARSFacilityAdaptation = sg.STARSFacilityAdaptation

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
	var alt int

	fakeMETAR := func(icao string) {
		alt = 2980 + rand.Intn(40)
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
			lg.Errorf("Error getting weather for %v.", icao)
		}
		fullMETAR := weather.RawMETAR
		altimiter := getAltimiter(fullMETAR)
		var err error

		if err != nil {
			lg.Errorf("Error converting altimiter to an intiger: %v.", altimiter)
		}
		var wind string
		spd := weather.Wspd
		var dir float64
		if weather.Wdir == -1 {
			dirInt := weather.Wdir.(int)
			dir = float64(dirInt)
		}
		var ok bool
		dir, ok = weather.Wdir.(float64)
		if !ok {
			lg.Errorf("Error converting %v into a float64: actual type %T", dir, dir)
		}
		if spd <= 0 {
			wind = "00000KT"
		} else if dir == -1 {
			wind = fmt.Sprintf("VRB%vKT", spd)
		} else {
			wind = fmt.Sprintf("%03d%02d", int(dir), spd)
			gst := weather.Wgst
			if gst > 5 {
				wind += fmt.Sprintf("G%02d", gst)
			}
			wind += "KT"
		}

		// Just provide the stuff that the STARS display shows
		ss.METAR[icao] = &av.METAR{
			AirportICAO: icao,
			Wind:        wind,
			Altimeter:   "A" + altimiter,
		}
	}

	ss.DepartureAirports = make(map[string]*av.Airport)
	for name := range s.LaunchConfig.DepartureRates {
		ss.DepartureAirports[name] = ss.Airports[name]
	}
	ss.ArrivalAirports = make(map[string]*av.Airport)
	for _, airportRates := range s.LaunchConfig.ArrivalGroupRates {
		for name := range airportRates {
			ss.ArrivalAirports[name] = ss.Airports[name]
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

func getAltimiter(metar string) string {
	for _, indexString := range []string{" A3", " A2"} {
		index := strings.Index(metar, indexString)
		if index != -1 && index+6 < len(metar) {
			return metar[index+2 : index+6]
		}
	}
	return ""
}

func (s *State) PreSave() {
	// Clean up before staving; here we clear out all of the video map data
	// so we don't pay the cost of writing it out to disk, since it's
	// available to us anyway.
	s.STARSFacilityAdaptation.PreSave()
}

func (s *State) PostLoad(ml *av.VideoMapLibrary) error {
	// Tidy things up after loading from disk: reinitialize the video maps
	// and also make ERAMComputers aware of each other.
	s.ERAMComputers.PostLoad()
	return s.STARSFacilityAdaptation.PostLoad(ml)
}

func (ss *State) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := ss.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := ss.Fixes[s]; ok {
		return p, true
	} else if n, ok := av.DB.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := av.DB.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := av.DB.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return math.Point2LL{}, false
	}
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

func (ss *State) GetVideoMaps() ([]av.VideoMap, []string) {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok {
		return config.VideoMaps, config.DefaultMaps
	}
	return ss.STARSFacilityAdaptation.VideoMaps, ss.ScenarioDefaultVideoMaps
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

// This should be facility-defined in the json file, but for now it's 30nm
// near their departure airport.
func (ss *State) InAcquisitionArea(ac *av.Aircraft) bool {
	if ss.InDropArea(ac) {
		return false
	}

	for _, icao := range []string{ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport} {
		ap := av.DB.Airports[icao]
		if math.NMDistance2LL(ap.Location, ac.Position()) <= 2 {
			return true
		}
	}
	return false
}

func (ss *State) InDropArea(ac *av.Aircraft) bool {
	for _, icao := range []string{ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport} {
		ap := av.DB.Airports[icao]
		if math.NMDistance2LL(ap.Location, ac.Position()) <= 1 &&
			ac.Altitude() <= float32(ap.Elevation+50) {
			return true
		}
	}

	return false
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
