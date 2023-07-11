// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
)

const initialSimSeconds = 45

///////////////////////////////////////////////////////////////////////////
// World

type World struct {
	// Used on the client side only
	simProxy *SimProxy

	Aircraft    map[string]*Aircraft
	METAR       map[string]*METAR
	Controllers map[string]*Controller

	DepartureAirports map[string]*Airport
	ArrivalAirports   map[string]*Airport

	lastUpdate   time.Time
	updateCall   *PendingCall
	showSettings bool

	launchControlWindow *LaunchControlWindow

	pendingCalls []*PendingCall

	missingPrimaryDialog *ModalDialogBox

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	LaunchController  string
	PrimaryController string
	MultiControllers  map[string]*MultiUserController

	SimIsPaused       bool
	SimRate           float32
	SimName           string
	SimDescription    string
	SimTime           time.Time
	MagneticVariation float32
	NmPerLongitude    float32
	Airports          map[string]*Airport
	Fixes             map[string]Point2LL
	PrimaryAirport    string
	RadarSites        map[string]*RadarSite
	Center            Point2LL
	Range             float32
	DefaultMap        string
	STARSMaps         []STARSMap
	Wind              Wind
	Callsign          string
	ApproachAirspace  []AirspaceVolume
	DepartureAirspace []AirspaceVolume
	DepartureRunways  []ScenarioGroupDepartureRunway
	Scratchpads       map[string]string
	ArrivalGroups     map[string][]Arrival
	// airport -> runway -> category -> rate
	DepartureRates map[string]map[string]map[string]int
	// arrival group -> airport -> rate
	ArrivalGroupRates map[string]map[string]int
	GoAroundRate      float32

	STARSInputOverride string
}

func NewWorld() *World {
	return &World{
		Aircraft:    make(map[string]*Aircraft),
		METAR:       make(map[string]*METAR),
		Controllers: make(map[string]*Controller),
	}
}

func (w *World) Assign(other *World) {
	w.Aircraft = DuplicateMap(other.Aircraft)
	w.METAR = DuplicateMap(other.METAR)
	w.Controllers = DuplicateMap(other.Controllers)
	w.PrimaryController = other.PrimaryController
	w.MultiControllers = DuplicateMap(other.MultiControllers)

	w.SimRate = other.SimRate
	w.SimIsPaused = other.SimIsPaused
	w.SimDescription = other.SimDescription
	w.SimName = other.SimName

	w.DepartureAirports = other.DepartureAirports
	w.ArrivalAirports = other.ArrivalAirports

	w.MagneticVariation = other.MagneticVariation
	w.NmPerLongitude = other.NmPerLongitude
	w.Airports = other.Airports
	w.Fixes = other.Fixes
	w.PrimaryAirport = other.PrimaryAirport
	w.RadarSites = other.RadarSites
	w.Center = other.Center
	w.Range = other.Range
	w.DefaultMap = other.DefaultMap
	w.STARSMaps = other.STARSMaps
	w.Wind = other.Wind
	w.Callsign = other.Callsign
	w.ApproachAirspace = other.ApproachAirspace
	w.DepartureAirspace = other.DepartureAirspace
	w.DepartureRunways = other.DepartureRunways
	w.Scratchpads = other.Scratchpads
	w.ArrivalGroups = other.ArrivalGroups
	w.DepartureRates = other.DepartureRates
	w.ArrivalGroupRates = other.ArrivalGroupRates
	w.GoAroundRate = other.GoAroundRate
}

func (w *World) GetWindVector(p Point2LL, alt float32) Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := w.SimTime.Sub(base).Seconds()
	windSpeed := float32(w.Wind.Speed) +
		float32(w.Wind.Gust)*float32(1+math.Cos(sec/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := OppositeHeading(float32(w.Wind.Direction))
	vWind := [2]float32{sin(radians(d)), cos(radians(d))}
	vWind = scale2f(vWind, windSpeed/3600)
	return vWind
}

func (w *World) GetAirport(icao string) *Airport {
	return w.Airports[icao]
}

func (w *World) Locate(s string) (Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := w.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := w.Fixes[s]; ok {
		return p, true
	} else if n, ok := database.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := database.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := database.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (w *World) AllAirports() map[string]*Airport {
	all := DuplicateMap(w.DepartureAirports)
	for name, ap := range w.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (w *World) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) TakeOrReturnLaunchControl(eventStream *EventStream) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.TakeOrReturnLaunchControl(),
			IssueTime: time.Now(),
			OnErr: func(e error) {
				eventStream.Post(Event{
					Type:    StatusMessageEvent,
					Message: e.Error(),
				})
			},
		})
}

func (w *World) LaunchAircraft(ac Aircraft) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.LaunchAircraft(ac),
			IssueTime: time.Now(),
		})
}

func (w *World) SetScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.Scratchpad = scratchpad
	}

	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.SetScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetTemporaryAltitude(callsign string, alt int, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.TempAltitude = alt
	}

	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.SetTemporaryAltitude(callsign, alt),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) InitiateTrack(callsign string, success func(any), err func(error)) {
	// Modifying locally is not canonical but improves perceived latency in
	// the common case; the RPC may fail, though that's fine; the next
	// world update will roll back these changes anyway.
	if ac := w.Aircraft[callsign]; ac != nil &&
		ac.TrackingController == "" && ac.ControllingController == "" {
		ac.TrackingController = w.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.InitiateTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) DropTrack(callsign string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.TrackingController = ""
		ac.ControllingController = ""
	}

	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.DropTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffTrack(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffControl(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.HandoffControl(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptHandoff(callsign string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.HandoffTrackController == w.Callsign {
		ac.HandoffTrackController = ""
		ac.TrackingController = w.Callsign
		ac.ControllingController = w.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RejectHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.RejectHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) CancelHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) PointOut(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.PointOut(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ChangeControlPosition(callsign string, keepTracks bool) error {
	err := w.simProxy.ChangeControlPosition(callsign, keepTracks)
	if err == nil {
		w.Callsign = callsign
	}
	return err
}

func (w *World) Disconnect() {
	if err := w.simProxy.SignOff(nil, nil); err != nil {
		lg.Errorf("Error signing off from sim: %v", err)
	}
	w.Aircraft = nil
	w.Controllers = nil
}

func (w *World) GetAircraft(callsign string) *Aircraft {
	if ac, ok := w.Aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (w *World) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range w.Aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (w *World) GetAllAircraft() []*Aircraft {
	return w.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (w *World) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := w.Aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (w *World) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (w *World) GetMETAR(location string) *METAR {
	return w.METAR[location]
}

func (w *World) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (w *World) GetController(callsign string) *Controller {
	if ctrl := w.Controllers[callsign]; ctrl != nil {
		return ctrl
	}

	// Look up by id
	for _, ctrl := range w.Controllers {
		if ctrl.SectorId == callsign {
			return ctrl
		}
	}

	return nil
}

func (w *World) GetAllControllers() map[string]*Controller {
	return w.Controllers
}

func (w *World) GetUpdates(eventStream *EventStream, onErr func(error)) {
	if w.simProxy == nil {
		return
	}

	if w.updateCall != nil && w.updateCall.CheckFinished(eventStream) {
		w.updateCall = nil
		return
	}

	w.checkPendingRPCs(eventStream)

	// Wait in seconds between update fetches; no less than 100ms
	rate := clamp(1/w.SimRate, 0.1, 1)
	if time.Since(w.lastUpdate) > time.Duration(rate*float32(time.Second)) {
		if w.updateCall != nil {
			lg.Errorf("Still waiting on last update call!")
			return
		}

		wu := &SimWorldUpdate{}
		w.updateCall = &PendingCall{
			Call:      w.simProxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				wu.UpdateWorld(w, eventStream)
				w.lastUpdate = time.Now()
			},
			OnErr: onErr,
		}
	}
}

func (w *World) checkPendingRPCs(eventStream *EventStream) {
	w.pendingCalls = FilterSlice(w.pendingCalls,
		func(call *PendingCall) bool { return !call.CheckFinished(eventStream) })
}

func (w *World) Connected() bool {
	return w.simProxy != nil
}

func (w *World) GetSerializeSim() (*Sim, error) {
	return w.simProxy.GetSerializeSim()
}

func (w *World) ToggleSimPause() {
	w.pendingCalls = append(w.pendingCalls, &PendingCall{
		Call:      w.simProxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (w *World) GetSimRate() float32 {
	if w.SimRate == 0 {
		return 1
	}
	return w.SimRate
}

func (w *World) SetSimRate(r float32) {
	w.pendingCalls = append(w.pendingCalls, &PendingCall{
		Call:      w.simProxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	w.SimRate = r // so the UI is well-behaved...
}

func (w *World) CurrentTime() time.Time {
	d := time.Since(w.lastUpdate)
	if w.SimRate != 0 {
		d = time.Duration(float64(d) * float64(w.SimRate))
	}
	return w.SimTime.Add(d)
}

func (w *World) GetWindowTitle() string {
	if w.SimDescription == "" {
		return "(disconnected)"
	} else if w.SimName == "" {
		return w.Callsign + ": " + w.SimDescription
	} else {
		return w.Callsign + "@" + w.SimName + ": " + w.SimDescription
	}
}

func (w *World) PrintInfo(ac *Aircraft) {
	lg.Errorf("%s", spew.Sdump(ac))

	s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
		ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
	if ac.ApproachCleared {
		s += ", cleared approach"
	}
	lg.Errorf("%s", s)
}

func (w *World) DeleteAircraft(ac *Aircraft, onErr func(err error)) {
	if w.simProxy != nil {
		if w.LaunchController == "" || w.LaunchController == w.Callsign {
			delete(w.Aircraft, ac.Callsign)
		}

		w.pendingCalls = append(w.pendingCalls,
			&PendingCall{
				Call:      w.simProxy.DeleteAircraft(ac.Callsign),
				IssueTime: time.Now(),
				OnErr:     onErr,
			})
	} else {
		delete(w.Aircraft, ac.Callsign)
	}
}

func (w *World) RunAircraftCommands(ac *Aircraft, cmds string, onErr func(err error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.RunAircraftCommands(ac.Callsign, cmds),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
}

func sampleAircraft(icao, fleet string) *Aircraft {
	al, ok := database.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fl {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := database.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from fleet %+v, airline %s",
			aircraft, fleet, icao)
		return nil
	}

	// random callsign
	callsign := strings.ToUpper(icao)
	for {
		format := "####"
		if len(al.Callsign.CallsignFormats) > 0 {
			format = Sample(al.Callsign.CallsignFormats)
		}

		id := ""
		for _, ch := range format {
			switch ch {
			case '#':
				id += fmt.Sprintf("%d", rand.Intn(10))
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			}
		}
		if _, found := badCallsigns[callsign+id]; !found && id != "0" {
			callsign += id
			break
		}
	}

	squawk := Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &Aircraft{
		Callsign:       callsign,
		AssignedSquawk: squawk,
		Squawk:         squawk,
		Mode:           Charlie,
		FlightPlan: &FlightPlan{
			Rules:        IFR,
			AircraftType: acType,
		},
	}
}

func (w *World) CreateArrival(arrivalGroup string, airportName string, goAround bool) (*Aircraft, error) {
	arrivals := w.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := SampleFiltered(arrivals, func(ar Arrival) bool {
		_, ok := ar.Airlines[airportName]
		return ok
	})
	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, airportName)
	}
	arr := arrivals[idx]

	airline := Sample(arr.Airlines[airportName])
	ac := sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan.DepartureAirport = airline.Airport
	ac.FlightPlan.ArrivalAirport = airportName
	var ok bool
	if ac.FlightPlan.DepartureAirportLocation, ok = w.Locate(ac.FlightPlan.DepartureAirport); !ok {
		return nil, fmt.Errorf("%s: unable to find departure airport location?", ac.FlightPlan.DepartureAirport)
	}

	ac.FlightPlan.ArrivalAirport = airportName
	if ac.FlightPlan.ArrivalAirportLocation, ok = w.Locate(ac.FlightPlan.ArrivalAirport); !ok {
		return nil, fmt.Errorf("%s: unable to find arrival airport location?", ac.FlightPlan.ArrivalAirport)
	}

	ac.TrackingController = arr.InitialController
	ac.ControllingController = arr.InitialController
	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan)
	}
	ac.FlightPlan.Route = arr.Route

	// Start with the default waypoints for the arrival; these may be
	// updated when an 'expect' approach is given...
	ac.Waypoints = arr.Waypoints
	// Hold onto these with the Aircraft so we have them later.
	ac.ArrivalRunwayWaypoints = arr.RunwayWaypoints

	perf := ac.Performance()

	ac.Position = ac.Waypoints[0].Location
	ac.Altitude = arr.InitialAltitude
	ac.IAS = min(arr.InitialSpeed, perf.Speed.Cruise)
	ac.ArrivalHandoffController = arr.HandoffController

	ac.Scratchpad = arr.Scratchpad
	if arr.ExpectApproach != "" {
		ap := w.GetAirport(ac.FlightPlan.ArrivalAirport)
		if _, ok := ap.Approaches[arr.ExpectApproach]; ok {
			ac.ApproachId = arr.ExpectApproach
		} else {
			return nil, fmt.Errorf("%s: unable to find expected approach", arr.ExpectApproach)
		}
	}

	if goAround {
		ac.AddFutureNavCommand(&GoAround{AirportDistance: 0.1 + .6*rand.Float32()})
	}

	ac.Nav.L = &FlyRoute{}
	ac.Nav.S = &FlyRoute{
		SpeedRestriction: min(arr.SpeedRestriction, perf.Speed.Cruise),
	}
	ac.Nav.V = &FlyRoute{
		AltitudeRestriction: arr.ClearedAltitude,
	}

	return ac, nil
}

func (w *World) CreateDeparture(airport, runway, category string, challenge float32) (*Aircraft, error) {
	ap := w.Airports[airport]
	if ap == nil {
		return nil, ErrUnknownAirport
	}

	idx := FindIf(w.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == airport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, ErrUnknownRunway
	}
	rwy := &w.DepartureRunways[idx]

	var dep *Departure
	if rand.Float32() < challenge {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		if rand.Float32() < .5 {
			dep = rwy.lastDeparture
		} else if rwy.lastDeparture != nil {
			idx := SampleFiltered(ap.Departures,
				func(d Departure) bool {
					return ap.ExitCategories[d.Exit] == ap.ExitCategories[rwy.lastDeparture.Exit]
				})
			if idx == -1 {
				// This shouldn't ever happen...
				return nil, ErrNoValidDepartureFound
			}
			dep = &ap.Departures[idx]
		}
	}

	if dep == nil {
		// Sample uniformly, minding the category, if specified
		idx := SampleFiltered(ap.Departures,
			func(d Departure) bool {
				return rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit]
			})
		if idx == -1 {
			// This shouldn't ever happen...
			return nil, fmt.Errorf("%s/%s: unable to find a valid departure", airport, rwy.Runway)
		}
		dep = &ap.Departures[idx]
	}

	rwy.lastDeparture = dep

	airline := Sample(dep.Airlines)
	ac := sampleAircraft(airline.ICAO, airline.Fleet)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	ac.Waypoints = DuplicateSlice(exitRoute.Waypoints)
	ac.Waypoints = append(ac.Waypoints, dep.routeWaypoints...)
	ac.Position = ac.Waypoints[0].Location

	ac.FlightPlan.Route = exitRoute.InitialRoute + " " + dep.Route
	ac.FlightPlan.ArrivalAirport = dep.Destination
	var ok bool
	if ac.FlightPlan.ArrivalAirportLocation, ok = w.Locate(ac.FlightPlan.ArrivalAirport); !ok {
		return nil, fmt.Errorf("%s: unable to find arrival airport location?", ac.FlightPlan.ArrivalAirport)
	}

	ac.Scratchpad = w.Scratchpads[dep.Exit]
	if dep.Altitude == 0 {
		ac.FlightPlan.Altitude = PlausibleFinalAltitude(w, ac.FlightPlan)
	} else {
		ac.FlightPlan.Altitude = dep.Altitude
	}

	ac.TrackingController = ap.DepartureController
	ac.ControllingController = ap.DepartureController
	ac.Altitude = float32(ap.Elevation)
	ac.IsDeparture = true

	ac.Nav.L = &FlyRoute{}
	ac.Nav.S = &FlyRoute{}
	ac.Nav.V = &MaintainAltitude{Altitude: float32(ap.Elevation)}

	ac.AddFutureNavCommand(&ClimbOnceAirborne{
		Altitude: float32(min(exitRoute.ClearedAltitude, ac.FlightPlan.Altitude)),
	})

	ac.FlightPlan.DepartureAirport = airport
	if ac.FlightPlan.DepartureAirportLocation, ok = w.Locate(ac.FlightPlan.DepartureAirport); !ok {
		return nil, fmt.Errorf("%s: unable to find departure airport location?", ac.FlightPlan.DepartureAirport)
	}

	return ac, nil
}

///////////////////////////////////////////////////////////////////////////
// Settings

func (w *World) ToggleActivateSettingsWindow() {
	w.showSettings = !w.showSettings
}

type MissingPrimaryModalClient struct {
	world *World
}

func (mp *MissingPrimaryModalClient) Title() string {
	return "Missing Primary Controller"
}

func (mp *MissingPrimaryModalClient) Opening() {}

func (mp *MissingPrimaryModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Sign in to " + mp.world.PrimaryController, action: func() bool {
		err := mp.world.ChangeControlPosition(mp.world.PrimaryController, true)
		return err == nil
	}})
	b = append(b, ModalDialogButton{text: "Disconnect", action: func() bool {
		newWorldChan <- nil // This will lead to a World Disconnect() call in main.go
		uiCloseModalDialog(mp.world.missingPrimaryDialog)
		return true
	}})
	return b
}

func (mp *MissingPrimaryModalClient) Draw() int {
	imgui.Text("The primary controller, " + mp.world.PrimaryController + ", has disconnected from the server or is otherwise unreachable.\nThe simulation will be paused until a primary controller signs in.")
	return -1
}

func (w *World) DrawMissingPrimaryDialog() {
	if _, ok := w.Controllers[w.PrimaryController]; ok {
		if w.missingPrimaryDialog != nil {
			uiCloseModalDialog(w.missingPrimaryDialog)
			w.missingPrimaryDialog = nil
		}
	} else {
		if w.missingPrimaryDialog == nil {
			w.missingPrimaryDialog = NewModalDialogBox(&MissingPrimaryModalClient{world: w})
			uiShowModalDialog(w.missingPrimaryDialog, true)
		}
	}
}

func (w *World) DrawSettingsWindow() {
	if !w.showSettings {
		return
	}

	imgui.BeginV("Settings", &w.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	max := Select(*devmode, float32(100), float32(10))
	if imgui.SliderFloatV("Simulation speed", &w.SimRate, 1, max, "%.1f", 0) {
		w.SetSimRate(w.SimRate)
	}

	if imgui.BeginComboV("UI Font Size", fmt.Sprintf("%d", globalConfig.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Roboto Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range SortedMapKeys(sizes) {
			if imgui.SelectableV(fmt.Sprintf("%d", size), size == globalConfig.UIFontSize, 0, imgui.Vec2{}) {
				globalConfig.UIFontSize = size
				ui.font = GetFont(FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
			}
		}
		imgui.EndCombo()
	}
	if imgui.BeginComboV("STARS DCB Font Size", fmt.Sprintf("%d", globalConfig.DCBFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Inconsolata Condensed Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range SortedMapKeys(sizes) {
			if imgui.SelectableV(fmt.Sprintf("%d", size), size == globalConfig.DCBFontSize, 0, imgui.Vec2{}) {
				globalConfig.DCBFontSize = size
			}
		}
		imgui.EndCombo()
	}

	var fsp *FlightStripPane
	var messages *MessagesPane
	var stars *STARSPane
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		switch pane := p.(type) {
		case *FlightStripPane:
			fsp = pane
		case *STARSPane:
			stars = pane
		case *MessagesPane:
			messages = pane
		}
	})

	stars.DrawUI()

	imgui.Separator()

	if imgui.CollapsingHeader("Audio") {
		globalConfig.Audio.DrawUI()
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if messages != nil && imgui.CollapsingHeader("Messages") {
		messages.DrawUI()
	}

	if imgui.CollapsingHeader("Developer") {
		if imgui.BeginTableV("GlobalFiles", 4, 0, imgui.Vec2{}, 0) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Scenario:")
			imgui.TableNextColumn()
			imgui.Text(globalConfig.DevScenarioFile)
			imgui.TableNextColumn()
			if imgui.Button("New...##scenario") {
				ui.jsonSelectDialog = NewFileSelectDialogBox("Select JSON File", []string{".json"},
					globalConfig.DevScenarioFile, func(filename string) {
						globalConfig.DevScenarioFile = filename
						ui.jsonSelectDialog = nil
					})
				ui.jsonSelectDialog.Activate()
			}
			imgui.TableNextColumn()
			if globalConfig.DevScenarioFile != "" && imgui.Button("Clear##scenario") {
				globalConfig.DevScenarioFile = ""
			}

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Video maps:")
			imgui.TableNextColumn()
			imgui.Text(globalConfig.DevVideoMapFile)
			imgui.TableNextColumn()
			if imgui.Button("New...##vid") {
				ui.jsonSelectDialog = NewFileSelectDialogBox("Select JSON File", []string{".json"},
					globalConfig.DevVideoMapFile, func(filename string) {
						globalConfig.DevVideoMapFile = filename
						ui.jsonSelectDialog = nil
					})
				ui.jsonSelectDialog.Activate()
			}
			imgui.TableNextColumn()
			if globalConfig.DevVideoMapFile != "" && imgui.Button("Clear##vid") {
				globalConfig.DevVideoMapFile = ""
			}

			imgui.EndTable()
		}

		if ui.jsonSelectDialog != nil {
			ui.jsonSelectDialog.Draw()
		}
	}

	imgui.End()
}
