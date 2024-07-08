package sim

import (
	"log/slog"
	gomath "math"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

type State struct {
	Aircraft    map[string]*av.Aircraft
	METAR       map[string]*av.METAR
	Controllers map[string]*av.Controller

	DepartureAirports map[string]*av.Airport
	ArrivalAirports   map[string]*av.Airport

	ERAMComputers ERAMComputers

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
