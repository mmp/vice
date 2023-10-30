// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/mmp/imgui-go/v4"
	"golang.org/x/exp/slog"
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

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *PendingCall
	showSettings      bool
	showScenarioInfo  bool

	launchControlWindow *LaunchControlWindow

	pendingCalls []*PendingCall

	missingPrimaryDialog *ModalDialogBox

	// Scenario routes to draw on the scope
	scopeDraw struct {
		arrivals   map[string]map[int]bool               // group->index
		approaches map[string]map[string]bool            // airport->approach
		departures map[string]map[string]map[string]bool // airport->runway->exit
	}

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	LaunchConfig      LaunchConfig
	PrimaryController string
	MultiControllers  SplitConfiguration
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
	InhibitCAVolumes  []AirspaceVolume
	Wind              Wind
	Callsign          string
	ApproachAirspace  []ControllerAirspaceVolume
	DepartureAirspace []ControllerAirspaceVolume
	DepartureRunways  []ScenarioGroupDepartureRunway
	ArrivalRunways    []ScenarioGroupArrivalRunway
	Scratchpads       map[string]string
	ArrivalGroups     map[string][]Arrival
	TotalDepartures   int
	TotalArrivals     int

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

	w.DepartureAirports = other.DepartureAirports
	w.ArrivalAirports = other.ArrivalAirports

	w.LaunchConfig = other.LaunchConfig
	w.PrimaryController = other.PrimaryController
	w.MultiControllers = DuplicateMap(other.MultiControllers)
	w.SimIsPaused = other.SimIsPaused
	w.SimRate = other.SimRate
	w.SimName = other.SimName
	w.SimDescription = other.SimDescription
	w.SimTime = other.SimTime
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
	w.InhibitCAVolumes = other.InhibitCAVolumes
	w.Wind = other.Wind
	w.Callsign = other.Callsign
	w.ApproachAirspace = other.ApproachAirspace
	w.DepartureAirspace = other.DepartureAirspace
	w.DepartureRunways = other.DepartureRunways
	w.ArrivalRunways = other.ArrivalRunways
	w.Scratchpads = other.Scratchpads
	w.ArrivalGroups = other.ArrivalGroups
	w.TotalDepartures = other.TotalDepartures
	w.TotalArrivals = other.TotalArrivals
}

func (w *World) GetWindVector(p Point2LL, alt float32) Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := w.SimTime.Sub(base).Seconds()
	windSpeed := float32(w.Wind.Speed) +
		float32(w.Wind.Gust-w.Wind.Speed)*float32(1+math.Cos(sec/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := OppositeHeading(float32(w.Wind.Direction))
	vWind := [2]float32{sin(radians(d)), cos(radians(d))}
	vWind = scale2f(vWind, windSpeed/3600)
	return vWind
}

func (w *World) AverageWindVector() [2]float32 {
	d := OppositeHeading(float32(w.Wind.Direction))
	v := [2]float32{sin(radians(d)), cos(radians(d))}
	return scale2f(v, float32(w.Wind.Speed))
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
	//
	// As in sim.go, only check for an unset TrackingController; we may already
	// have ControllingController due to a pilot checkin on a departure.
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == "" {
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

func (w *World) AcknowledgePointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.AcknowledgePointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RejectPointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&PendingCall{
			Call:      w.simProxy.RejectPointOut(callsign),
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

func (w *World) DepartureController(ac *Aircraft) string {
	callsign := w.MultiControllers.ResolveController(ac.DepartureContactController,
		func(callsign string) bool {
			ctrl, ok := w.Controllers[callsign]
			return ok && ctrl.IsHuman
		})
	return Select(callsign != "", callsign, w.PrimaryController)
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
	if d := time.Since(w.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if w.updateCall != nil {
			lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			return
		}
		w.lastUpdateRequest = time.Now()

		wu := &SimWorldUpdate{}
		w.updateCall = &PendingCall{
			Call:      w.simProxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				d := time.Since(w.updateCall.IssueTime)
				if d > 250*time.Millisecond {
					lg.Warnf("Slow world update response %s", d)
				} else {
					lg.Debugf("World update response time %s", d)
				}
				wu.UpdateWorld(w, eventStream)
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

func (w *World) SetLaunchConfig(lc LaunchConfig) {
	w.pendingCalls = append(w.pendingCalls, &PendingCall{
		Call:      w.simProxy.SetLaunchConfig(lc),
		IssueTime: time.Now(),
	})
	w.LaunchConfig = lc // for the UI's benefit...
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (w *World) CurrentTime() time.Time {
	t := w.SimTime

	if !w.SimIsPaused && !w.lastUpdateRequest.IsZero() {
		d := time.Since(w.lastUpdateRequest)

		// Roughly account for RPC overhead; more for a remote server (where
		// SimName will be set.)
		if w.SimName == "" {
			d -= 10 * time.Millisecond
		} else {
			d -= 50 * time.Millisecond
		}
		d = max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(w.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(w.lastReturnedTime) {
		w.lastReturnedTime = t
	}
	return w.lastReturnedTime
}

func (w *World) GetWindowTitle() string {
	if w.SimDescription == "" {
		return "(disconnected)"
	} else {
		deparr := fmt.Sprintf(" [ %d departures %d arrivals ]", w.TotalDepartures, w.TotalArrivals)
		if w.SimName == "" {
			return w.Callsign + ": " + w.SimDescription + deparr
		} else {
			return w.Callsign + "@" + w.SimName + ": " + w.SimDescription + deparr
		}
	}
}

func (w *World) PrintInfo(ac *Aircraft) {
	lg.Info("print aircraft", slog.String("callsign", ac.Callsign),
		slog.Any("aircraft", ac))
	fmt.Println(spew.Sdump(ac) + "\n" + ac.Nav.FlightState.Summary())
}

func (w *World) DeleteAircraft(ac *Aircraft, onErr func(err error)) {
	if w.simProxy != nil {
		if lctrl := w.LaunchConfig.Controller; lctrl == "" || lctrl == w.Callsign {
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

func (w *World) sampleAircraft(icao, fleet string) (*Aircraft, string) {
	al, ok := database.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil, ""
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil, ""
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
		return nil, ""
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
				id += strconv.Itoa(rand.Intn(10))
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			}
		}
		if id == "0" || id == "00" || id == "000" || id == "0000" {
			continue // bleh, try again
		} else if _, ok := w.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
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
	}, acType
}

func (w *World) CreateArrival(arrivalGroup string, arrivalAirport string, goAround bool) (*Aircraft, error) {
	arrivals := w.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := SampleFiltered(arrivals, func(ar Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})
	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := Sample(arr.Airlines[arrivalAirport])
	ac, acType := w.sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = NewFlightPlan(IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := w.PrimaryController
	if w.MultiControllers != nil {
		arrivalController = w.MultiControllers.GetArrivalController(arrivalGroup)
		if arrivalController == "" {
			arrivalController = w.PrimaryController
		}
	}

	if err := ac.InitializeArrival(w, arrivalGroup, idx, arrivalController, goAround); err != nil {
		return nil, err
	}

	return ac, nil
}

func (w *World) CreateDeparture(departureAirport, runway, category string, challenge float32,
	lastDeparture *Departure) (*Aircraft, *Departure, error) {
	ap := w.Airports[departureAirport]
	if ap == nil {
		return nil, nil, ErrUnknownAirport
	}

	idx := FindIf(w.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, ErrUnknownRunway
	}
	rwy := &w.DepartureRunways[idx]

	var dep *Departure
	if rand.Float32() < challenge && lastDeparture != nil {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		pred := Select(rand.Float32() < .5,
			func(d Departure) bool { return d.Exit == lastDeparture.Exit },
			func(d Departure) bool {
				return ap.ExitCategories[d.Exit] == ap.ExitCategories[lastDeparture.Exit]
			})

		if idx := SampleFiltered(ap.Departures, pred); idx == -1 {
			// This should never happen...
			lg.Errorf("%s/%s/%s: unable to sample departure", departureAirport, runway, category)
		} else {
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
			return nil, nil, fmt.Errorf("%s/%s: unable to find a valid departure",
				departureAirport, rwy.Runway)
		}
		dep = &ap.Departures[idx]
	}

	virtualDepartureController := ap.DepartureController
	humanDepartureController := ""
	if virtualDepartureController == "" {
		humanDepartureController = w.PrimaryController
		if w.MultiControllers != nil {
			humanDepartureController = w.MultiControllers.GetDepartureController(departureAirport, runway)
			if humanDepartureController == "" {
				humanDepartureController = w.PrimaryController
			}
		}
	}

	airline := Sample(dep.Airlines)
	ac, acType := w.sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil, nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = NewFlightPlan(IFR, acType, departureAirport, dep.Destination)
	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(w, ap, dep, virtualDepartureController,
		humanDepartureController, exitRoute); err != nil {
		return nil, nil, err
	}

	return ac, dep, nil
}

///////////////////////////////////////////////////////////////////////////
// Settings

func (w *World) ToggleActivateSettingsWindow() {
	w.showSettings = !w.showSettings
}

func (w *World) ToggleShowScenarioInfoWindow() {
	w.showScenarioInfo = !w.showScenarioInfo
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

func (w *World) DrawScenarioInfoWindow() {
	if !w.showScenarioInfo {
		return
	}

	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(w.SimDescription, false, 0)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	imgui.BeginV(w.SimDescription, &w.showScenarioInfo, imgui.WindowFlagsAlwaysAutoResize)

	// Make big(ish) tables somewhat more legible
	tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
		imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if imgui.CollapsingHeader("Arrivals") {
		if imgui.BeginTableV("arr", 4, tableFlags, imgui.Vec2{}, 0) {
			if w.scopeDraw.arrivals == nil {
				w.scopeDraw.arrivals = make(map[string]map[int]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Airport(s)")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, name := range SortedMapKeys(w.ArrivalGroups) {
				arrivals := w.ArrivalGroups[name]
				if w.scopeDraw.arrivals[name] == nil {
					w.scopeDraw.arrivals[name] = make(map[int]bool)
				}

				for i, arr := range arrivals {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					enabled := w.scopeDraw.arrivals[name][i]
					imgui.Checkbox(fmt.Sprintf("##arr-%s-%d", name, i), &enabled)
					w.scopeDraw.arrivals[name][i] = enabled

					imgui.TableNextColumn()
					imgui.Text(name)

					imgui.TableNextColumn()
					airports := SortedMapKeys(arr.Airlines)
					imgui.Text(strings.Join(airports, ", "))

					imgui.TableNextColumn()
					if arr.Description != "" {
						imgui.Text(arr.Description)
					} else {
						imgui.Text("--")
					}
				}
			}

			imgui.EndTable()
		}
	}

	imgui.Separator()

	if imgui.CollapsingHeader("Approaches") {
		if imgui.BeginTableV("appr", 6, tableFlags, imgui.Vec2{}, 0) {
			if w.scopeDraw.approaches == nil {
				w.scopeDraw.approaches = make(map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Code")
			imgui.TableSetupColumn("Description")
			imgui.TableSetupColumn("FAF")
			imgui.TableHeadersRow()

			for _, rwy := range w.ArrivalRunways {
				if ap, ok := w.Airports[rwy.Airport]; !ok {
					lg.Errorf("%s: arrival airport not in world airports", rwy.Airport)
				} else {
					if w.scopeDraw.approaches[rwy.Airport] == nil {
						w.scopeDraw.approaches[rwy.Airport] = make(map[string]bool)
					}
					for _, name := range SortedMapKeys(ap.Approaches) {
						appr := ap.Approaches[name]
						if appr.Runway == rwy.Runway {
							imgui.TableNextRow()
							imgui.TableNextColumn()
							enabled := w.scopeDraw.approaches[rwy.Airport][name]
							imgui.Checkbox("##enable-"+rwy.Airport+"-"+rwy.Runway+"-"+name, &enabled)
							w.scopeDraw.approaches[rwy.Airport][name] = enabled

							imgui.TableNextColumn()
							imgui.Text(rwy.Airport)

							imgui.TableNextColumn()
							imgui.Text(rwy.Runway)

							imgui.TableNextColumn()
							imgui.Text(name)

							imgui.TableNextColumn()
							imgui.Text(appr.FullName)

							imgui.TableNextColumn()
							for _, wp := range appr.Waypoints[0] {
								if wp.FAF {
									imgui.Text(wp.Fix)
									break
								}
							}
						}
					}
				}
			}
			imgui.EndTable()
		}
	}

	imgui.Separator()
	if imgui.CollapsingHeader("Departures") {
		if imgui.BeginTableV("departures", 5, tableFlags, imgui.Vec2{}, 0) {
			if w.scopeDraw.departures == nil {
				w.scopeDraw.departures = make(map[string]map[string]map[string]bool)
			}

			imgui.TableSetupColumn("Draw")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, airport := range SortedMapKeys(w.LaunchConfig.DepartureRates) {
				if w.scopeDraw.departures[airport] == nil {
					w.scopeDraw.departures[airport] = make(map[string]map[string]bool)
				}
				ap := w.Airports[airport]

				runwayRates := w.LaunchConfig.DepartureRates[airport]
				for _, rwy := range SortedMapKeys(runwayRates) {
					if w.scopeDraw.departures[airport][rwy] == nil {
						w.scopeDraw.departures[airport][rwy] = make(map[string]bool)
					}

					exitRoutes := ap.DepartureRoutes[rwy]

					// Multiple routes may have the same waypoints, so
					// we'll reverse-engineer that here so we can present
					// them together in the UI.
					routeToExit := make(map[string][]string)
					for _, exit := range SortedMapKeys(exitRoutes) {
						exitRoute := ap.DepartureRoutes[rwy][exit]
						r := exitRoute.Waypoints.Encode()
						routeToExit[r] = append(routeToExit[r], exit)
					}

					for _, exit := range SortedMapKeys(exitRoutes) {
						// Draw the row only when we hit the first exit
						// that uses the corresponding route route.
						r := exitRoutes[exit].Waypoints.Encode()
						if routeToExit[r][0] != exit {
							continue
						}

						imgui.TableNextRow()
						imgui.TableNextColumn()
						enabled := w.scopeDraw.departures[airport][rwy][exit]
						imgui.Checkbox("##enable-"+airport+"-"+rwy+"-"+exit, &enabled)
						w.scopeDraw.departures[airport][rwy][exit] = enabled

						imgui.TableNextColumn()
						imgui.Text(airport)
						imgui.TableNextColumn()
						imgui.Text(rwy)
						imgui.TableNextColumn()
						if len(routeToExit) == 1 {
							// If we only saw a single departure route, no
							// need to list all of the exits in the UI
							// (there are often a lot of them!)
							imgui.Text("(all)")
						} else {
							// List all of the exits that use this route.
							imgui.Text(strings.Join(routeToExit[r], ", "))
						}
						imgui.TableNextColumn()
						imgui.Text(exitRoutes[exit].Description)
					}
				}
			}
			imgui.EndTable()
		}
	}

	imgui.End()
}

func (w *World) DrawScenarioRoutes(transforms ScopeTransformations, font *Font, color RGB,
	cb *CommandBuffer) {
	if !w.showScenarioInfo {
		return
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)
	pd := &PointsDrawBuilder{}
	ldr := GetLinesDrawBuilder() // for restrictions--in window coords...
	defer ReturnLinesDrawBuilder(ldr)

	// Track which waypoints have been drawn so that we don't repeatedly
	// draw the same one.  (This is especially important since the
	// placement of the labels depends on the inbound/outbound segments,
	// which may be different for different uses of the waypoint...)
	drawnWaypoints := make(map[string]interface{})

	style := TextStyle{
		Font:           font,
		Color:          color,
		DropShadow:     true,
		DrawBackground: true}

	// STARS
	for _, name := range SortedMapKeys(w.ArrivalGroups) {
		if w.scopeDraw.arrivals == nil || w.scopeDraw.arrivals[name] == nil {
			continue
		}

		arrivals := w.ArrivalGroups[name]
		for i, arr := range arrivals {
			if !w.scopeDraw.arrivals[name][i] {
				continue
			}

			w.drawWaypoints(arr.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

			// Draw runway-specific waypoints
			for _, rwy := range SortedMapKeys(arr.RunwayWaypoints) {
				wp := arr.RunwayWaypoints[rwy]
				w.drawWaypoints(wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)

				if len(wp) > 1 {
					// Draw the runway number in the middle of the line
					// between the first two waypoints.
					pmid := mid2ll(wp[0].Location, wp[1].Location)
					td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
				} else if wp[0].Heading != 0 {
					// This should be the only other case... The heading arrow is drawn
					// up to 2nm out, so put the runway 1nm along its axis.
					a := radians(float32(wp[0].Heading) - w.MagneticVariation)
					v := [2]float32{sin(a), cos(a)}
					pend := ll2nm(wp[0].Location, w.NmPerLongitude)
					pend = add2f(pend, v)
					pell := nm2ll(pend, w.NmPerLongitude)
					td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pell), style)
				}
			}
		}
	}

	// Approaches
	for _, rwy := range w.ArrivalRunways {
		if w.scopeDraw.approaches == nil || w.scopeDraw.approaches[rwy.Airport] == nil {
			continue
		}
		ap := w.Airports[rwy.Airport]
		for _, name := range SortedMapKeys(ap.Approaches) {
			appr := ap.Approaches[name]
			if appr.Runway == rwy.Runway && w.scopeDraw.approaches[rwy.Airport][name] {
				for _, wp := range appr.Waypoints {
					w.drawWaypoints(wp, drawnWaypoints, transforms, td, style, ld, pd, ldr, color)
				}
			}
		}
	}

	// Departure routes
	for _, name := range SortedMapKeys(w.Airports) {
		if w.scopeDraw.departures == nil || w.scopeDraw.departures[name] == nil {
			continue
		}

		ap := w.Airports[name]
		for _, rwy := range SortedMapKeys(ap.DepartureRoutes) {
			if w.scopeDraw.departures[name][rwy] == nil {
				continue
			}

			exitRoutes := ap.DepartureRoutes[rwy]
			for _, exit := range SortedMapKeys(exitRoutes) {
				if w.scopeDraw.departures[name][rwy][exit] {
					w.drawWaypoints(exitRoutes[exit].Waypoints, drawnWaypoints, transforms,
						td, style, ld, pd, ldr, color)
				}
			}
		}
	}

	// And now finally update the command buffer with everything we've
	// drawn.
	cb.SetRGB(color)
	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(2)
	ld.GenerateCommands(cb)
	cb.PointSize(5)
	pd.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
	cb.LineWidth(1)
	ldr.GenerateCommands(cb)
}

// pt should return nm-based coordinates
func calculateOffset(font *Font, pt func(int) ([2]float32, bool)) [2]float32 {
	prev, pok := pt(-1)
	cur, _ := pt(0)
	next, nok := pt(1)

	vecAngle := func(p0, p1 [2]float32) float32 {
		v := normalize2f(sub2f(p1, p0))
		return atan2(v[0], v[1])
	}

	const Pi = 3.1415926535
	angle := float32(0)
	if !pok {
		if !nok {
			// wtf?
		}
		// first point
		angle = vecAngle(cur, next)
	} else if !nok {
		// last point
		angle = vecAngle(prev, cur)
	} else {
		// have both prev and next
		angle = (vecAngle(prev, cur) + vecAngle(cur, next)) / 2 // ??
	}

	if angle < 0 {
		angle -= Pi / 2
	} else {
		angle += Pi / 2
	}

	offset := scale2f([2]float32{sin(angle), cos(angle)}, 8)

	h := NormalizeHeading(degrees(angle))
	if (h >= 160 && h < 200) || (h >= 340 || h < 20) {
		// Center(ish) the text if the line is more or less horizontal.
		offset[0] -= 2.5 * float32(font.size)
	}
	return offset
}

func (w *World) drawWaypoints(waypoints []Waypoint, drawnWaypoints map[string]interface{},
	transforms ScopeTransformations, td *TextDrawBuilder, style TextStyle,
	ld *LinesDrawBuilder, pd *PointsDrawBuilder, ldr *LinesDrawBuilder, color RGB) {

	// Draw an arrow at the point p (in nm coordinates) pointing in the
	// direction given by the angle a.
	drawArrow := func(p [2]float32, a float32) {
		aa := a + radians(180+30)
		pa := add2f(p, scale2f([2]float32{sin(aa), cos(aa)}, 0.5))
		ld.AddLine(nm2ll(p, w.NmPerLongitude), nm2ll(pa, w.NmPerLongitude))

		ba := a - radians(180+30)
		pb := add2f(p, scale2f([2]float32{sin(ba), cos(ba)}, 0.5))
		ld.AddLine(nm2ll(p, w.NmPerLongitude), nm2ll(pb, w.NmPerLongitude))
	}

	for i, wp := range waypoints {
		if wp.Heading != 0 {
			// Don't draw a segment to the next waypoint (if there is one)
			// but instead draw an arrow showing the heading.
			a := radians(float32(wp.Heading) - w.MagneticVariation)
			v := [2]float32{sin(a), cos(a)}
			v = scale2f(v, 2)
			pend := ll2nm(waypoints[i].Location, w.NmPerLongitude)
			pend = add2f(pend, v)

			// center line
			ld.AddLine(waypoints[i].Location, nm2ll(pend, w.NmPerLongitude))

			// arrowhead at the end
			drawArrow(pend, a)
		} else if i+1 < len(waypoints) {
			if wp.Arc != nil {
				// Draw DME arc. One subtlety is that although the arc's
				// radius should cause it to pass through the waypoint, it
				// may be slightly off due to error from using nm
				// coordinates and the approximation of a fixed nm per
				// longitude value.  So, we'll compute the radius to the
				// point in nm coordinates and store it in r0 and do the
				// same for the end point. Then we will interpolate those
				// radii along the arc.
				pc := ll2nm(wp.Arc.Center, w.NmPerLongitude)
				p0 := ll2nm(waypoints[i].Location, w.NmPerLongitude)
				r0 := distance2f(p0, pc)
				v0 := normalize2f(sub2f(p0, pc))
				a0 := NormalizeHeading(degrees(atan2(v0[0], v0[1]))) // angle w.r.t. the arc center

				p1 := ll2nm(waypoints[i+1].Location, w.NmPerLongitude)
				r1 := distance2f(p1, pc)
				v1 := normalize2f(sub2f(p1, pc))
				a1 := NormalizeHeading(degrees(atan2(v1[0], v1[1])))

				// Draw a segment every degree
				n := int(headingDifference(a0, a1))
				a := a0
				pprev := waypoints[i].Location
				for i := 1; i < n-1; i++ {
					if wp.Arc.Clockwise {
						a += 1
					} else {
						a -= 1
					}
					a = NormalizeHeading(a)
					r := lerp(float32(i)/float32(n), r0, r1)
					v := scale2f([2]float32{sin(radians(a)), cos(radians(a))}, r)
					pnext := nm2ll(add2f(pc, v), w.NmPerLongitude)
					ld.AddLine(pprev, pnext)
					pprev = pnext

					if i == n/2 {
						// Draw an arrow at the midpoint showing the arc's direction
						drawArrow(add2f(pc, v), Select(wp.Arc.Clockwise, radians(a+90), radians(a-90)))
					}
				}
				ld.AddLine(pprev, waypoints[i+1].Location)
			} else {
				// Regular segment between waypoints: draw the line
				ld.AddLine(waypoints[i].Location, waypoints[i+1].Location)

				if waypoints[i+1].ProcedureTurn == nil {
					// Draw an arrow indicating direction of flight along
					// the segment, unless the next waypoint has a
					// procedure turn. In that case, we'll let the PT draw
					// the arrow..
					p0 := ll2nm(waypoints[i].Location, w.NmPerLongitude)
					p1 := ll2nm(waypoints[i+1].Location, w.NmPerLongitude)
					v := sub2f(p1, p0)
					drawArrow(mid2f(p0, p1), atan2(v[0], v[1]))
				}
			}
		}

		if pt := wp.ProcedureTurn; pt != nil {
			if i+1 >= len(waypoints) {
				lg.Errorf("Expected another waypoint after the procedure turn?")
			} else {
				// In the following, we will generate points a canonical
				// racetrack vertically-oriented, with width 2, and with
				// the origin at the left side of the arc at the top.  The
				// toNM transformation takes that to nm coordinates which
				// we'll later transform to lat-long to draw on the scope.
				toNM := Identity3x3()

				pnm := ll2nm(wp.Location, w.NmPerLongitude)
				toNM = toNM.Translate(pnm[0], pnm[1])

				p1nm := ll2nm(waypoints[i+1].Location, w.NmPerLongitude)
				v := sub2f(p1nm, pnm)
				hdg := atan2(v[0], v[1])
				toNM = toNM.Rotate(-hdg)
				if !pt.RightTurns {
					toNM = toNM.Translate(-2, 0)
				}

				// FIXME: reuse the logic in nav.go to compute the leg lengths.
				len := float32(pt.NmLimit)
				if len == 0 {
					len = float32(pt.MinuteLimit * 3) // assume 180 GS...
				}
				if len == 0 {
					len = 4
				}

				var lines [][2][2]float32
				// Lines for the two sides
				lines = append(lines,
					[2][2]float32{
						toNM.TransformPoint([2]float32{0, 0}),
						toNM.TransformPoint([2]float32{0, -len})},
					[2][2]float32{
						toNM.TransformPoint([2]float32{2, 0}),
						toNM.TransformPoint([2]float32{2, -len})})

				// Arcs at each end; all of this is slightly simpler since
				// the width of the racetrack is 2, so the radius of the
				// arcs is 1...
				// previous top and bottom points
				prevt := toNM.TransformPoint([2]float32{0, 0})
				prevb := toNM.TransformPoint([2]float32{2, -len})
				for i := -90; i <= 90; i++ {
					v := [2]float32{sin(radians(float32(i))), cos(radians(float32(i)))}

					// top
					pt := add2f([2]float32{1, 0}, v)
					pt = toNM.TransformPoint(pt)
					lines = append(lines, [2][2]float32{prevt, pt})
					prevt = pt

					// bottom
					pb := sub2f([2]float32{1, -len}, v)
					pb = toNM.TransformPoint(pb)
					lines = append(lines, [2][2]float32{prevb, pb})
					prevb = pb
				}

				for _, l := range lines {
					l0, l1 := nm2ll(l[0], w.NmPerLongitude), nm2ll(l[1], w.NmPerLongitude)
					ld.AddLine(l0, l1)
				}

				drawArrow(toNM.TransformPoint([2]float32{0, -len / 2}), hdg)
				drawArrow(toNM.TransformPoint([2]float32{2, -len / 2}), hdg+radians(180))
			}
		}

		if wp.Fix[0] == '_' {
			// Don't draw fix names or other details for internal-use fixes...
			continue
		}
		if _, err := ParseLatLong([]byte(wp.Fix)); err == nil {
			// Also don't draw fixes that are directly specified as latlong
			// coordinates.
			continue
		}
		if _, ok := drawnWaypoints[wp.Fix]; ok {
			// And if we're given the same fix more than once (as may
			// happen with T-shaped RNAV arrivals for example), only draw
			// it once. We'll assume/hope that we're not seeing it with
			// different restrictions...
			continue
		}

		// Record that we have drawn this waypoint
		drawnWaypoints[wp.Fix] = nil

		// Draw a circle at the waypoint's location
		pd.AddPoint([2]float32(wp.Location), color)

		offset := calculateOffset(style.Font, func(j int) ([2]float32, bool) {
			idx := i + j
			if idx < 0 || idx >= len(waypoints) {
				return [2]float32{}, false
			}
			return ll2nm(waypoints[idx].Location, w.NmPerLongitude), true
		})

		// Draw the text for the waypoint, including fix name, any
		// properties, and altitude/speed restrictions.
		p := transforms.WindowFromLatLongP(wp.Location)
		p = add2f(p, offset)
		p = td.AddText(wp.Fix+"\n", p, style)

		if wp.IAF || wp.IF || wp.FAF || wp.NoPT {
			var s []string
			if wp.IAF {
				s = append(s, "IAF")
			}
			if wp.IF {
				s = append(s, "IF")
			}
			if wp.FAF {
				s = append(s, "FAF")
			}
			if wp.NoPT {
				s = append(s, "NoPT")
			}
			p = td.AddText(strings.Join(s, "/")+"\n", p, style)
		}

		if wp.Speed != 0 || wp.AltitudeRestriction != nil {
			p[1] -= 0.25 * float32(style.Font.size) // extra space for lines above if needed

			if ar := wp.AltitudeRestriction; ar != nil {
				pt := p       // draw position for text
				var w float32 // max width of altitudes drawn
				if ar.Range[1] != 0 {
					// Upper altitude
					pp := td.AddText(FormatAltitude(ar.Range[1]), pt, style)
					w = pp[0] - pt[0]
					pt[1] -= float32(style.Font.size)
				}
				if ar.Range[0] != 0 && ar.Range[0] != ar.Range[1] {
					// Lower altitude, if present and different than upper.
					pp := td.AddText(FormatAltitude(ar.Range[0]), pt, style)
					w = max(w, pp[0]-pt[0])
					pt[1] -= float32(style.Font.size)
				}

				// Now that we have w, we can draw lines the specify the
				// restrictions.
				if ar.Range[1] != 0 {
					// At or below (or at)
					ldr.AddLine([2]float32{p[0], p[1] + 2}, [2]float32{p[0] + w, p[1] + 2})
				}
				if ar.Range[0] != 0 {
					// At or above (or at)
					ldr.AddLine([2]float32{p[0], pt[1] - 2}, [2]float32{p[0] + w, pt[1] - 2})
				}

				// update text draw position so that speed restrictions are
				// drawn in a reasonable place; note that we maintain the
				// original p[1] regardless of how many lines were drawn
				// for altitude restrictions.
				p[0] += w + 4
			}

			if wp.Speed != 0 {
				p0 := p
				p1 := td.AddText(fmt.Sprintf("%dK", wp.Speed), p, style)
				p1[1] -= float32(style.Font.size)

				// All speed restrictions are currently 'at'...
				ldr.AddLine([2]float32{p0[0], p0[1] + 2}, [2]float32{p1[0], p0[1] + 2})
				ldr.AddLine([2]float32{p0[0], p1[1] - 2}, [2]float32{p1[0], p1[1] - 2})
			}
		}
	}
}

func (w *World) DrawSettingsWindow() {
	if !w.showSettings {
		return
	}

	imgui.BeginV("Settings", &w.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.SliderFloatV("Simulation speed", &w.SimRate, 1, 20, "%.1f", 0) {
		w.SetSimRate(w.SimRate)
	}

	update := !globalConfig.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	globalConfig.InhibitDiscordActivity.Store(!update)

	if imgui.BeginComboV("UI Font Size", strconv.Itoa(globalConfig.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Roboto Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range SortedMapKeys(sizes) {
			if imgui.SelectableV(strconv.Itoa(size), size == globalConfig.UIFontSize, 0, imgui.Vec2{}) {
				globalConfig.UIFontSize = size
				ui.font = GetFont(FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
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
